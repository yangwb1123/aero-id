# aero-id 架构演进：下一波高价值扩展方向

> **作者视角**：资深架构师 / 产品经理  
> **扫描基准**：`main` 分支全部 Go 源文件 + 22 个 migration + proto + config  
> **方法**：基于当前已实现的全部功能（见扫描总结），寻找尚未覆盖但一旦补齐就能开启新能力象限的系统级方向。**不重复已有文档中的内容**。

---

## 当前能力总结（扫描范围）

| 模块 | 已实现 |
|------|--------|
| 文件 | CRUD + 内容校验去重 + 流式上传 + MIME 白名单 + 大小上限 + 预签名直传S3 + 显式共享(grant/revoke) + 元数据PATCH + 软删/硬删 + 存储GC(sweeper) + 引用计数 |
| 消息 | CRUD + 会话内收件箱 + 编辑 + 软删 + 已读回执 + 整会话标已读 + 未读数 + Reactions + 系统消息 + 文件附件 |
| 评论 | CRUD + 两层嵌套 + 编辑 + Like/Unlike + 举报 + 审核(Hide/Unhide/Resolve) + 回复计数原子事务 |
| 通知 | Outbox驱动 + 实时SSE推送 + 折叠(collapse) + 偏好(开关+静音时段) + 发送速率限制 + 阻止感知 + 未读Feed |
| 搜索 | PostgreSQL tsvector/GIN 全文搜索(消息/评论/文件) + 种类过滤 |
| 权限 | 方向性用户阻止(block) + 管理角色/权限(admin/moderator) + 审计日志 |
| 写路径 | 幂等键 + 事务性Outbox + 原子计数更新 |
| 可靠性 | SSO断路器 + singleflight + 陈旧缓存容错 + 重试退避 + TLS/mTLS + gRPC keepalive |
| 可观测 | RED指标 + OpenTelemetry追踪 + 按依赖就绪检查 + SSO健康仪表 |
| 流控 | 每actor/IP令牌桶限流 + 空闲桶回收 |
| 双协议 | 38个gRPC RPCs / 20+ HTTP routes，完全功能对等 |
| 迁移 | 22个可逆迁移，支持自动迁移或CLI |

---

## 扩展方向

---

### 方向一：资源配额与用量管理（Quota & Usage Enforcement）

**为什么需要它。**

当前系统对用户行为没有任何"量"的约束——文件上传大小上限是全局配置（`MaxUploadBytes`），但**单个用户可以无限制地上传文件数量、发送消息条数、创建评论数量**。这在以下场景会直接成为问题：

1. **成本失控**：一个恶意用户上传数 TB 文件，存储账单暴涨，但没有任何机制可以限制或预警。
2. **公平性**：免费版用户和付费版用户共享同样的资源池，无法做分层服务（例如免费用户最多 100 个文件、1GB 存储、每天 100 条消息）。
3. **DB 膨胀**：消息和评论表没有记录级上限——虽然每条记录很小，但亿万级增长会影响索引性能和维护窗口。

**当前已有但不够的部分：**
- 全局 `MaxUploadBytes`（只限制了单文件大小，不限制数量和总容量）
- 全局 `rate limiting`（限制了 QPS，不限制累计存储/记录数）
- 没有 `quota` 的概念——没有配额定义、检查、预警和强制拦截。

**建议设计：**

```
quota_plans 表                    → 定义配额模板（免费/专业/企业）
user_quotas 表 (uid, plan_id)     → 用户当前生效的配额计划
usage_records 表 (uid, resource_type, period_start, used)  → 滚动周期用量
```

- 资源类型：`storage.total_bytes`, `storage.file_count`, `message.sent_count`, `comment.created_count`
- 周期：`lifetime`（总量）/ `monthly`（月度重置）/ `daily`（每日重置）
- 强制点：在 service 层每个 Create/Upload 操作前检查，超限返回 `429 QuotaExceeded` / `403 Forbidden`
- 软限制 vs 硬限制：软限制（日志警告，通知用户）/ 硬限制（拒绝操作）
- 用量接近上限时（85%/95%）触发 Notification 推送

**涉及改动规模：**
- `internal/domain/quota.go` + `internal/repository/quota.go` + migration
- `internal/service/quota.go`：配额检查中间件 / 辅助函数
- `internal/service/file.go` / `message.go` / `comment.go`：在写入前增加 `checkQuota(ctx, actor, resourceType, delta)`
- HTTP/gRPC 端点：`GET /v1/quotas`（查看当前配额和用量），管理员设置配额
- 可考虑利用 outbox 异步记录用量更新（不阻塞写路径）

**为什么不先做别的？**
这不是最性感的功能，但这是**付费商业化**和**防滥用**的前提条件。没有配额系统，aero-id 无法安全地作为 multi-tenant 服务对外提供。

---

### 方向二：存储层媒体处理流水线（Media Pipeline）

**为什么需要它。**

aero-id 的定义是"管理账户名下数据的服务"，核心资产之一是**文件**。但当前文件存储是"黑盒"——上传什么存什么，下载什么给什么。缺少以下关键能力：

1. **缩略图/预览**：上传一张 4000×3000 的照片后，列表页、分享页仍要加载原图。没有自动生成的小尺寸缩略图。
2. **格式转码**：用户上传 HEIC/WebP/TIFF 格式图片，部分客户端不支持；上传 MOV/AVI 视频无法在线播放。没有自动转码为通用格式（JPEG/MP4）。
3. **文档预览**：PDF、Office 文档只能下载后本地打开，无法在浏览器中直接预览。
4. **病毒扫描 / 安全检测**：恶意文件上传后直接持久化存储，没有扫描环节。

**当前已有但不够的部分：**
- MIME 白名单限制了可上传的类型（但无法处理允许类型内的恶意文件）
- 内容哈希去重（checksum dedup）——但与其他安全/处理流水线完全解耦
- 存储 GC sweeper 清理僵尸文件——但没有任何"处理中"的事务保护

**建议设计：**

```
异步处理管线（基于 outbox + 内置 worker）：
  上传成功 → outbox 事件 "file.uploaded"
      → worker 1: virus scan (ClamAV / 第三方 API)
      → worker 2: thumbnail generation (imaging library)
      → worker 3: format transcoding (FFmpeg)
      → 结果写回 files.meta (jsonb) 或新建派生文件记录
```

- 缩略图可以作为派生文件存储在同一 storage backend，通过 `files.meta.thumbnails` 或 `file_derivatives` 表关联
- 视频转码需要长时间运行——不适合同步请求路径，必须异步
- 扫描结果（pass/fail/infected）写回 `files.meta.scan_status`
- 扫描失败的文件可以被标记为 `quarantine` 状态，阻止下载

**涉及改动规模：**
- `internal/processor/` 包：Scanner, Thumbnailer, Transcoder 接口 + 实现
- `internal/outbox/handler` 的扩展：注册 `file.uploaded` 事件的处理链
- `files` 表需要派生文件引用或新增 `file_derivatives` 表
- 配置项：processor 超时、并发度、失败策略

**为什么现在值得做？**
文件是 aero-id 的核心资产。用户对"文件服务"的预期远不止存储——预览、安全、兼容性是基本要求。这决定了 aero-id 是"存储 API"还是"内容管理平台"。

---

### 方向三：分布式中介状态层（Distributed Coordination Layer）

**为什么需要它。**

当前 aero-id 是完全的单实例架构：
- `TokenCache` / `UserCache`：进程内 `sync.Map` + TTL 清扫 goroutine
- `ratelimit.Limiter`：进程内 token bucket
- `SSE Hub`：进程内 subscriber registry
- `outbox.Relay`：单实例轮询
- 无分布式锁、无领导者选举、无共享会话

**这意味着：**

| 场景 | 当前行为 | 问题 |
|------|----------|------|
| 部署两个实例 | 各自有独立的 token cache，A 实例验证过的 token B 实例还要再查 SSO | 缓存命中率减半，SSO 压力翻倍 |
| 限流器双实例 | 每个实例独立计数，用户实际上可以发送 2x 限流阈值的请求 | 限流效果打折扣 |
| SSE 推送 | 用户连接到实例 A，但通知写入 DB 后 Relay 只在实例 B 触发 | 用户收不到实时推送 |
| Outbox 轮询 | 双实例同时轮询 `FetchUnpublished` → 重复处理甚至重复发送通知 | 数据一致性问题 |
| 缓存失效 | 无法广播失效消息（如 SSO 通知 token 已撤销） | 撤销延迟不可控 |

**建议设计（渐进式，不要求一步到位）：**

**Phase 1 — Redis 共享缓存（低侵入）：**
- `sso.TokenCache` / `sso.UserCache` 加上 Redis 后端选项（保留内存作为 L1）
- `ratelimit.Limiter` 可选用 Redis Sliding Window 实现
- 配置开关：`cache.backend = memory | redis`

**Phase 2 — 分布式协调（中等）：**
- 引入 `internal/coordinator/` 包（可用 PostgreSQL advisory lock 或 Redis Redlock）
- Outbox relay 使用 `SELECT ... FOR UPDATE SKIP LOCKED` 或分布式锁确保单实例 drain
- SSE 推送：通知写入时通过 outbox 触发，实例收到事件后检查本地 hub 是否有订阅者

**Phase 3 — 无状态化（高阶）：**
- 移除所有进程内"必须"状态，全部下沉到 Redis/Postgres
- 实例可以安全地弹性扩缩，请求可路由到任意实例

**为什么现在值得做？**
aero-id 当前所有进程内状态都正确工作——**在单实例下**。一旦流量需要水平扩展，每一个缓存和协调点都会成为阻塞点。越早引入共享缓存层，越能平滑过渡到多实例部署。

---

### 方向四：协作与社交图谱扩展（Beyond Binary Sharing）

**为什么需要它。**

当前 aero-id 的社交模型非常原始：
- 文件共享：owner 单向 grant/revoke 读权限给指定 uid
- 消息：两两私信
- 评论：挂在文件/消息上的两级树
- 阻止：方向性屏蔽

缺少所有**协作**和**群组**相关的概念：

1. **群组 / 频道**：无法向一组人发送消息（群聊），只能点对点。
2. **收藏 / 书签**：用户可以"收藏"文件或消息以便后续查找，但目前无法实现。
3. **关注 / 订阅**：用户可以关注某个文件（订阅其新评论），但只能被动接收通知。
4. **评论 @ 提及**：在评论或消息中 `@username` 不会触发通知，收件人无法感知。
5. **文件协作**：文件共享只有读权限，没有写权限（共同编辑文件元数据、添加版本）。
6. **内容集合**：无法将多个文件组织成相册/文件夹/集合。

**当前已有但不够的部分：**
- outbox + notification 体系已经完善——`@` 提要和群聊消息通知可以复用
- SSE hub 已经存在——实时协作（如"正在输入"）可以直接 push
- file share 的 `file_shares` 表已经建立了关系基础，但只有一对一的读权限
- 搜索已经实现了 tsvector 全文搜索——但用户无法"收藏"搜索结果

**建议设计（最高用户价值的子集）：**

| 功能 | 核心改动 | 优先级 |
|------|----------|--------|
| **群聊** | `conversation_participants` 表，消息的 `to_uid` → `conversation_id` | P0 |
| **@ 提及** | 在 `comment.create` / `message.send` 时解析 `@uid` 生成额外 outbox 事件 | P0 |
| **文件收藏** | `file_bookmarks` 表 + 简单 CRUD + 列表 | P1 |
| **内容集合** | `collections` + `collection_items` 表（多态外键），让用户可以组织内容 | P1 |

**涉及改动规模（以群聊为例）：**

- 新增 `conversations` 表 + `conversation_participants` 表 + migration
- `messages` 表增加可选 `conversation_id` 列
- `domain.Conversation` 实体 + 业务规则（参与者管理、退出、全员通知）
- 群聊消息通过 outbox 为所有参与者生成通知（已阻止者除外，block 已实现）
- HTTP/gRPC 端点：创建群 / 加人 / 踢人 / 发群消息 / 列出会话

**为什么现在值得做？**
从"个人工具"到"团队协作平台"的跨越，需要群组和协作能力。这是一个**产品方向的决定**：aero-id 是个人数据存储，还是团队协作平台？如果是后者，群聊和协作是最短路径。

---

### 方向五：数据可移植性与合规接口（Data Portability & Compliance API）

**为什么需要它。**

当前 aero-id 对**用户数据的生命周期终点**没有系统级的支持：

1. **账号删除时数据如何处理？** SSO 删除账号后，aero-id 中的文件/消息/评论变成"孤儿记录"。OwnerUID 指向一个已经不存在的用户。
2. **用户如何导出自己的数据？** 没有 `GET /v1/export` 端点。用户无法一次性下载自己所有文件、消息和评论。
3. **合规性**：GDPR 第 20 条（数据可携带权）要求服务商提供结构化、通用、机器可读格式的数据导出。CCPA 第 1798.105 条（删除权）要求删除用户数据。

**当前已有但不够的部分：**
- 软删（`gorm.DeletedAt`）存在，但无数据保留策略（多久后硬删？）
- 存储 GC sweeper 负责硬删超期软删的行——但这是存储层面的清理，不是用户层面的数据删除
- Admin 角色和审计日志存在，但无法响应合规性请求（"导出用户 U123 的全部数据"）

**建议设计：**

**数据导出（Data Subject Access Request — DSAR）：**
```go
// 异步，因为可能要打包 GB 级数据
POST /v1/export-requests    → 创建导出任务
GET  /v1/export-requests/:id → 查询进度（pending/processing/completed）
GET  /v1/export-requests/:id/download → 下载 ZIP（.json + 原始文件）
```
- 导出范围：profile（UID）、files（元数据 + 文件内容）、messages（全部收发）、comments
- 格式：每个实体一个 JSON Lines 文件 + 原始文件按存储 key 打包
- 基于 outbox 异步处理，完成后 SSE 通知

**数据删除 / 匿名化：**
```go
POST /v1/account-data/deletion  → 标记 uid 下全部数据的删除
```
- 软删所有文件/消息/评论 → 进入 GC 清理管道
- 或匿名化（`owner_uid` 替换为 `deleted-user-<uuid>`，保留数据但解除个人关联）
- 对 SSO 已删除的用户：aero-id 应定期（或通过 webhook）清理孤儿数据

**保留策略（Data Retention Policy）：**
- 配置化保留期：消息保留 X 年，文件保留 Y 年，评论永久保留
- 超过保留期的数据自动进入 GC 硬删队列（可复用现有 `storage/gc` sweeper）

**涉及改动规模：**
- `internal/service/export.go`：打包逻辑（使用 `archive/zip`）
- `internal/service/deletion.go`：按 uid 批量软删/匿名化
- migration：`export_requests` 表
- 利用已有 outbox 做异步导出 + notification 通知导出完成
- `GET /v1/account-data/export` HTTP 端点 + gRPC 对等

**为什么现在值得做？**
这不是一个"万一被问到再补"的功能。GDPR/CCPA 的合规要求是**法律义务**，而不是产品选择。如果 aero-id 服务欧洲或加州用户，数据可移植性和删除权是必须实现的能力。提前设计合规接口比被监管机构询问后再紧急补救要经济得多。

---

## 实施顺序建议

| 方向 | 依赖关系 | 估量 | 商业价值 | 建议 |
|------|----------|------|----------|------|
| **D1. 配额管理** | 无依赖，独立模块 | 中 | 高（商业化前提） | **P0 — 现在开始** |
| **D2. 媒体流水线** | 需要 outbox（已实现） | 高 | 高（产品差异化） | **P1 — 紧跟配额** |
| **D3. 分布式协调** | 需要运维支持（Redis） | 中-高 | 中（弹性前提） | **P1 — 流量达到单实例瓶颈时** |
| **D4. 协作与群组** | 依赖 D3（SSE 多实例） | 高 | 高（团队协作） | **P2 — 确定产品方向后** |
| **D5. 合规接口** | 无依赖 | 中 | 中（法律合规） | **P1 — 在服务 EU/CA 用户前完成** |

---

## 附录：扫描中发现的中小规模优化点（无需完整方向即可解决）

| 问题 | 位置 | 建议 |
|------|------|------|
| SSE hub 不支持 `?last_event_id` 持久化恢复（内存断开即丢失） | `internal/hub/hub.go` | 增加可选的持久化后端（Redis Streams / PG） |
| Outbox relay 优雅关闭时不做最后一次 drain | `internal/outbox/relay.go` | `Stop()` 时触发一次同步 drain |
| gRPC handler 错误码映射散落在各 handler 中 | `internal/handler/grpc/*.go` | 引入统一错误拦截器（参考 ROADMAP P1-2） |
| SSE hub 无认证——知道 uid 即可 subscribe | `internal/handler/http/events.go` | 已经在 auth 组下，但需要确认 auth MW 生效 |
| 文件上传缺少 multipart 进度通知 | `internal/handler/http/file.go` | 大型文件上传时用户无反馈 |
| 批量操作端点缺失（批量删文件、批量标记已读） | 各 handler | 减少 API 调用次数 |
