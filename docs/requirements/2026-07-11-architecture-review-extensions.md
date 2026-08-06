# Architecture Review — 高价值扩展方向

> 审核日期：2026-07-11  
> 审核范围：`main` 分支全代码库扫描（internal/ 全部 Go 源码、22 个迁移、proto、配置）  
> 方法：阅读全部源文件，识别已实现的能力边界、缺失的抽象层、以及架构中隐含但未暴露的扩展点

---

## 当前状态概览

aero-id 已经是一个非常成熟的双传输（HTTP + gRPC）服务。已完成的能力包括：

| 领域 | 已交付 |
|------|--------|
| 文件管道 | CRUD、分片上传、内容去重、显式共享、presigned S3 直传、存储 GC 回收、MIME/大小校验、元数据更新 |
| 消息能力 | 发送/收件箱/对话/已读、撤回、编辑、系统消息、Reactions、全部标记已读 |
| 评论系统 | 两层级嵌套、点赞、编辑、举报/审核队列、隐藏/取消隐藏 |
| 通知系统 | Outbox 驱动的异步通知、已读回执、偏好设置（开关/免打扰时段/时区）、折叠、速率限制、SSE 实时推送 |
| 安全与反滥用 | 黑名单（方向性）、幂等键、速率限制（per-actor/per-IP）、报告审核 |
| SSO 韧性 | 断路器、serve-stale、singleflight、重试+退避、TLS/mTLS、证书固定、keepalive |
| 可观测性 | RED 指标、OpenTelemetry 追踪、per-dependency 就绪检查、outbox/SSO 健康指标 |
| 架构正确性 | 事务性 outbox、原子引用计数、审计日志、迁移工具、后台协程优雅关闭 |

以下分析聚焦于 **下一个合理的前沿**——即基于当前架构的瓶颈和空白提出的高价值扩展。

---

## 方向一：Workspace / Group Collaboration（工作区与群组协作）

### 为什么需要

当前的数据模型完全是 **两方的**：
- 文件：一个 owner → N 个被共享者（单向）
- 消息：一个 sender ↔ 一个 recipient（直线）
- 评论：属于一个 target（文件或消息），作者单人

没有 **多对多协作空间** 的概念。产品上这意味着：
- 一个团队不能共享一个文件仓库（必须逐个 uid 共享，无法统一管理）
- 不能发送群组消息（只有 1v1 私信）
- 评论不能 scope 到一个项目/频道

从架构上看，这是当前最大的 **产品差距**——代码库已经具备所有基础设施（事务、通知、权限检查、审计），只缺一个 group/workspace 领域对象。

### 必须解决的架构问题

```
workspace
  ├── members (uid ↔ workspace, role: owner/admin/member)
  ├── channels (逻辑子空间，可选)
  ├── files   (workspace_id 外键，取代 owner_uid-only 权限模型)
  └── messages (多收件人 semantics)
```

1. **权限模型扩展**：当前是 `owner_uid` / `visibility` / `share` 三元模型。workspace 引入第四种：`workspace_id` + 角色。文件/消息/评论的读 ACL 需要变为：
   ```
   can_read =
     owner_uid == actor
     OR visibility == public
     OR workspace_id != nil AND actor ∈ workspace.members
     OR explicitly_shared(actor)
   ```
2. **写入路径不变**：事务性 outbox、幂等键、审计日志——现有基础设施直接复用。
3. **通知扩展**：群组活动（新人加入、频道消息、文件更新）需要新的通知类型 `workspace.*`，现有 dispatcher 扩展即可。
4. **迁移成本**：新增 ~3 张表（workspaces, workspace_members, workspace_files），现有表加 `workspace_id` 列。不破坏现有 API。

### 边界情况

- **嵌套权限冲突**：一个文件同时属于 workspace (group-read) 且被 owner 设为 private → workspace 成员能否读取？需要优先级规则（explicit share > workspace > visibility）。
- **离开 workspace 后的资源归属**：文件转移（给另一个成员）或自动变为 orphan。
- **群组消息的已读追踪**：group-message 的 `read_at` 是 per-member 的，需要 `message_reads` 桥表。

### 价值评估

| 维度 | 评估 |
|------|------|
| 产品价值 | ⭐⭐⭐⭐⭐ —— 打开团队协作场景，从工具变成平台 |
| 架构复用度 | ⭐⭐⭐⭐ —— 现有 outbox/notif/block/audit 全部可复用 |
| 实现成本 | 中高 —— 新领域对象 + 权限模型重构 |
| 风险 | 低 —— 完全向后兼容，不改现有 API |

---

## 方向二：Horizontal Scalability Infrastructure（水平扩展基础设施）

### 为什么需要

当前架构有几个 **单实例瓶颈**，一旦多副本部署就会暴露：

| 组件 | 当前 | 问题 |
|------|------|------|
| 速率限制 | 内存 token bucket (per-instance) | N 个实例意味着速率限制阈值放大 N 倍 |
| SSO Token 缓存 | 内存 map (per-instance) | 每个实例冷启动都要穿透 SSO，缓存命中率 = 1/N |
| SSO User 缓存 | 同上 | 同上 |
| SSE Hub | 内存 subscriber map (per-instance) | 实例 A 上的事件不会推送到实例 B 上的客户端 |

如果 aero-id 计划在生产环境中以 2+ 副本运行（无论是 K8s 还是其他编排），这些就成了 **正确性问题而非性能问题**。

### 必须解决的架构问题

1. **分布式速率限制**（Redis Sliding Window / GCRA）：
   - 现有 `ratelimit.Limiter` 接口保持不变，新增 `RedisLimiter` 实现
   - 回退策略：Redis 不可用时降级到本地（宽松但不阻断）
   - 配置项：`rate_limit.driver: "local" | "redis"`

2. **分布式缓存**（Redis）：
   - SSO Token/User 缓存：`sso/cache.go` 的接口提取，新增 Redis 后端
   - `StaleTTL` 机制仍然有用：Redis 不可用时从本地二级缓存 serve-stale
   - 配置项：`sso.cache_driver: "local" | "redis"`

3. **分布式 SSE**（Redis Pub/Sub 或 NATS）：
   - `hub.Hub` 作为本地注册表不变，增加一个 "bridge" goroutine 订阅 Redis channel
   - 本地 Publish 时同时 publish 到 Redis channel 和本地 subscribers
   - 收到 Redis 消息时投递给本地 subscriber

### 边界情况

- **Redis 故障**：每个组件都必须有降级行为（本地 token bucket、本地 cache、仅本地 SSE）。不可让 Redis 故障变成 aero-id 故障。
- **缓存失效风暴**：SSO token 缓存失效时全部实例同时回源 → singleflight 在分布式环境下需要 Redis 分布式锁 / leader 选举来协调。
- **SSE 消息顺序**：跨实例的 SSE 事件不保证严格顺序。对于通知，`id` 字段（created_at unix-millis）允许客户端检测乱序。

### 价值评估

| 维度 | 评估 |
|------|------|
| 产品价值 | ⭐⭐⭐ —— 对用户不可见，但对运营团队是刚需 |
| 架构复用度 | ⭐⭐⭐⭐ —— 接口已存在，新增 impl 即可 |
| 实现成本 | 中 —— 3 个组件各需 2-3 天 |
| 风险 | 中 —— 需要 Redis 依赖，但降级路径可控制 |

---

## 方向三：File Versioning & Lifecycle Management（文件版本控制与生命周期管理）

### 为什么需要

aero-id 的名称就包含 "id"（身份），但它的核心资产是 **文件**。当前的文件模型是 **不可变的**：
- 上传 → 创建 row → 软删除 → GC 清理
- 没有 "覆盖" 或 "新版本" 的概念
- 更新操作（`PATCH /files/:id`）只改元数据，不改内容

这在产品上意味着：
- 用户误上传错误的文件版本 → 只能删了重传（丢失原 id / 链接）
- 团队协作编辑同一文件 → 无法追踪谁改了哪个版本
- 没有回收站 / 软删除恢复窗口（软删除后 GC 一跑就真没了）

### 必须解决的架构问题

1. **版本链**：
   ```sql
   -- 新增
   ALTER TABLE files ADD COLUMN current_version INT NOT NULL DEFAULT 1;
   ALTER TABLE files ADD COLUMN file_group_id UUID;  -- 同一个 "逻辑文件" 共享此 id
   
   CREATE TABLE file_versions (
       id              UUID PRIMARY KEY,
       file_group_id   UUID NOT NULL,
       version_number  INT NOT NULL,
       storage_key     TEXT NOT NULL,
       checksum        TEXT,
       size            BIGINT NOT NULL,
       mime_type       TEXT NOT NULL,
       created_by      TEXT NOT NULL,
       created_at      TIMESTAMPTZ NOT NULL,
       deleted_at      TIMESTAMPTZ
   );
   ```
2. **存储去重**：版本间的 content-dedup 自动生效（`storage_refs` 已经支持）。
3. **API 扩展**：
   - `POST /v1/files/:id/versions` — 上传新版本（替换内容，保留 id/链接）
   - `GET /v1/files/:id/versions` — 版本列表
   - `GET /v1/files/:id/versions/:v` — 获取特定版本
   - `POST /v1/files/:id/versions/:v/restore` — 恢复旧版本
4. **回收站**：软删除保护期（如 30 天），GC 不清理删除未满 30 天的行。当前 GC 的 `Retention` 配置已可用于此。

### 边界情况

- **共享与版本**：如果我共享了一个文件，然后上传新版本，被共享者看到的是新版本还是旧版本？需要一个策略（auto-update / pin-version）。
- **大文件版本链**：100 个版本每个 1GB → 存储膨胀。需要一个保留策略（保留最近 N 个版本，或每月压缩快照）。
- **dedup 与版本删除**：删除一个版本时只减少 ref_count，不删除物理 blob，除非 ref_count 归零。`storage_refs` 已正确处理。

### 价值评估

| 维度 | 评估 |
|------|------|
| 产品价值 | ⭐⭐⭐⭐ —— 文件服务从 "存储桶" 变成 "文档管理系统" |
| 架构复用度 | ⭐⭐⭐⭐ —— `storage_refs` 天然支持版本去重；现有存储接口不变 |
| 实现成本 | 中 —— 版本表 + API 扩展 + GC 适配 |
| 风险 | 低 —— 全是 additive，不改变现有文件行语义 |

---

## 方向四：Content Moderation Pipeline at Scale（规模化内容审核管道）

### 为什么需要

当前的审核能力是 **手动且被动的**：
- 用户举报评论 → `comment_reports` 表
- 评论作者（不是管理员）可以 resolve/dismiss 举报
- 隐藏评论是 hide/unhide（soft-delete 的一种）
- 没有针对 **消息** 的举报机制
- 没有自动扫描（病毒、图片 NSFW、垃圾文本）
- 没有分级处理（自动隐藏 → 人工复核 → 封禁）

在生产环境中，一旦用户量上来，这会直接变成 **安全/合规风险**：
- 恶意用户可以通过消息发送恶意链接 / 不当内容
- 评论区的垃圾内容没有自动过滤
- 没有用户 ban 的系统集成（admin_roles / user_ban 权限已定义但 ban/unban 端点未实现）

### 必须解决的架构问题

1. **自动扫描管道**：
   ```
   Upload / Send / Comment
       ↓
   Content Scanner (interface)
       ├── Text: 关键字过滤 / ML 分类器 / 正则
       ├── Image: NSFW detection / 图片哈希黑名单
       └── File: ClamAV / 沙箱检测
       ↓
   Action: pass | flag_for_review | auto_hide | reject
   ```
   - scanner 应该是插件式的（interface），初始实现可以是简单的关键字/正则匹配
   - 扫描在异步 outbox handler 中进行，不阻塞上传/发送路径
   - 结果写入 `content_reviews` 表

2. **消息举报**：
   - 新增 `message_reports` 表（镜像 `comment_reports` 结构）
   - HTTP 端点：`POST /v1/messages/:id/report`

3. **用户 ban 系统**：
   - `user_bans` 表（uid, reason, banned_by, expires_at, created_at）
   - 写入路径增强：被 ban 的用户不能发送消息、不能评论、不能上传文件
   - 管理员端点：`POST /v1/admin/users/:uid/ban`，`POST /v1/admin/users/:uid/unban`
   - 现有 `adminRepo.HasPermission` 检查 `user.ban` 权限

4. **审核队列**：
   - 统一视图：合并 `comment_reports` + `message_reports` + `content_reviews`
   - 管理员端点：`GET /v1/admin/reports`（分页、按状态/类型过滤）
   - `POST /v1/admin/reports/:id/resolve`（支持批量）

### 边界情况

- **误判回退**：自动扫描误判正常内容 → 用户申诉流程（`POST /v1/reports/:id/appeal`）
- **区域合规**：不同地区的审核策略不同（GDPR、中国网络安全法等），需要策略可配置。
- **扫描性能**：大文件病毒扫描可能耗时数秒→必须在 outbox handler 中异步处理，且设置超时。
- **AP 操作**：禁止用户后，用户正在进行的 WebSocket 连接不应立即断开（继续现有会话）但新操作被拒。

### 价值评估

| 维度 | 评估 |
|------|------|
| 产品价值 | ⭐⭐⭐⭐⭐ —— 安全合规的底线，用户规模化的前提 |
| 架构复用度 | ⭐⭐⭐ —— 需要新表+新服务，但 outbox/audit/handler 模式可复用 |
| 实现成本 | 高 —— 扫描器集成 + 举报扩展 + 管理 UI API + ban 系统 |
| 风险 | 中 —— 扫描器是外部依赖，集成复杂度高 |

---

## 方向五：Observability Deepening — Distributed Tracing Completeness & SLO Framework（可观测性深化与 SLO 框架）

### 为什么需要

当前的可观测性已经不错（RED metrics、OTel tracing、per-dependency readyz），但有 **三个关键空白**：

1. **Trace 不完整**：
   - OTel trace 覆盖了 SSO 调用，但 **不覆盖数据库查询**（GORM 未接入 OTel）。这意味着端到端延迟的瓶颈（DB 还是 SSO？）无法通过 trace 定位。
   - Outbox relay 的 handler 执行没有 trace span。
   - Storage GC sweep 没有 trace span。

2. **没有 SLO 指标**：
   - 虽然 `metrics.Registry` 记录了请求延迟直方图，但没有 **SLO 对齐的目标指标**（比如 "99% 的请求在 500ms 内完成" → 需要一个 `slo_budget` gauge）。
   - 没有错误预算消耗的告警规则模板。

3. **日志结构化不足**：
   - 当前 access log 和业务 log 是 zap 结构化日志，但没有 **统一的错误分类**（哪个层级的？是否可以降级？是否需要告警？）。
   - 没有 trace ID 自动注入所有日志行（`middleware/tracing.go` 设置了 span，但 logger 没有自动提取 trace_id 加入字段）。

### 必须解决的架构问题

1. **GORM OTel 插件**：
   - GORM 提供了 `gorm.io/plugin/opentelemetry` 插件，一行集成即可让所有查询获得 DB trace span
   - 在 `db.Open` 中附加插件
   - 需要配置采样率避免 trace 洪流

2. **统一 Logger + Trace 集成**：
   - 自定义 `zap.Core`，自动从 context 提取 `trace_id` / `span_id` 加入每条日志的字段
   - 所有 `log.Info` / `log.Error` 调用自动携带 trace context
   - 使日志 → trace 的关联变成零成本

3. **SLO 指标框架**：
   - 定义 SLO 边界：`slo_latency_budget`（如 p99 < 500ms 为成功）、`slo_error_budget`
   - 从现有 RED metrics 派生 SLO 燃烧率指标
   - 暴露 `/metrics` 时增加 `slo_*` 指标
   - 提供一个 `/debug/slo` 端点返回当前燃烧状态摘要

4. **Outbox / GC Trace 注入**：
   - `outbox.Relay.drain` 每次 drain 创建一个 root span
   - 每个 outbox event 的 handler 执行创建子 span
   - Storage GC 的每个 pass 创建 root span

### 边界情况

- **Trace 采样与成本**：高吞吐下 trace 采样率必须可配置（当前 `SampleRatio` 已存在），且 GORM 插件需要尊重采样决策。
- **SLO 燃烧率告警**：需要输出 Prometheus `slo:` 格式的 recording rules，而不是代码内硬编码阈值。
- **日志量控制**：trace_id 注入每条日志会增加 ~100 bytes/行，在高 QPS 下需要考虑日志成本和存储。

### 价值评估

| 维度 | 评估 |
|------|------|
| 产品价值 | ⭐⭐⭐ —— 对用户透明，对 on-call 工程师是救命级 |
| 架构复用度 | ⭐⭐⭐⭐⭐ —— GORM 插件是直接集成；logger 包装是局部修改 |
| 实现成本 | 低-中 —— GORM 插件 < 1 天；logger+trace 集成 < 1 天；SLO 框架 2-3 天 |
| 风险 | 极低 —— 所有变更都是 additive，不影响业务逻辑 |

---

## 综合优先级建议

| # | 方向 | 产品价值 | 架构价值 | 实现成本 | 建议时机 |
|---|------|----------|----------|----------|----------|
| 1 | **Workspace / Group Collaboration** | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 中高 | P1 — 团队协作是核心产品跃升 |
| 2 | **Content Moderation Pipeline** | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ | 高 | P1 — 安全合规的底线 |
| 3 | **File Versioning & Lifecycle** | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 中 | P2 — 文件服务的自然进化 |
| 4 | **Horizontal Scalability** | ⭐⭐⭐ | ⭐⭐⭐⭐ | 中 | P2 — 多副本部署前的预研 |
| 5 | **Observability Deepening** | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | 低-中 | P0-P1 — 运维成熟度的最后一公里 |

### 依赖关系

- **方向 1 (Workspace)** 依赖方向 4 (Moderation) 的权限模型吗？—— 不直接依赖，但两者都涉及 ACL 扩展，建议先做方向 4 的 user ban 部分（较小），再做方向 1。
- **方向 3 (File Versioning)** 依赖方向 4 (Scalability) 吗？—— 不依赖。
- **方向 5 (Observability)** 是纯增量，不依赖其他方向，**建议立即开始**——成本最低且为后续所有变更提供可观测性保障。

### 建议分批路线

```
Batch A (下一步)
  ├── Observability Deepening (方向 5) — 低投入高回报
  └── User Ban + Message Reports (方向 4 的子集) — 安全底线

Batch B
  ├── File Versioning (方向 3)
  └── Content Moderation Pipeline (方向 4 主体)

Batch C
  ├── Workspace / Group Collaboration (方向 1)
  └── Horizontal Scalability (方向 2)
```

---

## 附录：扫描中注意到的小型改进点（不构成独立方向，但值得记录）

1. **GORM OTel 插件缺失**：`db.Open` 未配置 `opentelemetry.Plugin`，所有 DB 查询缺乏 trace 可见性。
2. **`storage/gc/sweeper.go` 的 `Run` 方法使用 `context.Background()`**：导致 GC trace 无法与请求关联。建议改为传递带 span 的 context。
3. **Hub adapter 的 `Publish` 签名**：`service.NotificationPusher` 定义了 `Publish(uid, eventType, data string, eventID int64)`，但 `hub.Publish` 的 `eventID` 参数在所有调用处都是 `n.CreatedAt.UnixMilli()` — 可以考虑由 hub 内部生成 sequence ID 以消除对 caller 的依赖。
4. **管理端点仅有 `/v1/admin/audit-log`**：`admin_roles` 表已 seed moderators 和 admins，但
   - 没有分配/撤销角色的端点
   - 没有列出所有用户的端点
   - "user.ban" 权限定义了但没有实现 ban/unban API
5. **SSE heartbeat 固定 30s**：不可配置，某些反向代理可能需要更短的心跳间隔。
6. **`notification_preferences` 的 `timezone` 列目前仅在表中定义**：service 层的 `IsAllowed` 方法需要使用 timezone 计算 quiet hours 的正确本地时间，当前实现是否完整需要验证。
