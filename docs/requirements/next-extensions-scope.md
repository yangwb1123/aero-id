# aero-id — 高价值扩展方向 (2026-07-11 扫描)

> **前置说明：** 本文档基于 2026-07-11 全局代码扫描。当前代码库已经完整交付了
> ROADMAP 中全部 5 个方向（可观测性、SSO 韧性、文件管道、写入正确性、产品面）
> 以及其后全部生产强化项（限流、gRPC ops、outbox 重试/死信、存储 GC、FTS 搜索、
> SSE 实时推送、管理员角色、通知偏好/折叠、Webhook 投递、审计日志、消息反应等）。
> 以下 5 个方向是下一个迭代的高价值增量，每个都基于现有代码的真实缺口，非空想。

---

## 方向一：用户数据生命周期与合规（GDPR / 账户注销传播）

### 为什么需要

aero-id 的架构原则是"SSO 拥有身份"，但在当前实现中，SSO 删除了一个用户之后，
aero-id 对此**一无所知**。所有表（`files`、`messages`、`comments`、`notifications`、
`audit_events`、`blocks`、`user_roles`、`notification_preferences`）都以 uid 字符串
引用用户，但没有以下任何机制：

1. **用户注销传播** — SSO 没有 gRPC 回调/webhook 通知 aero-id "用户 X 已删除"。
   删除后的用户数据永远沉淀在 aero-id 中，形成合规风险（GDPR "被遗忘权"）。
2. **数据导出** — 无 API 让用户一次性导出自己在 aero-id 的全部数据（文件、消息、评论）。
3. **数据匿名化** — 软删除是 owner 删除自己的资源，不是"该用户所有关联数据匿名化/清除"。
   一个用户删除 SSO 账户后，其消息收件箱里仍能看到 "来自 uid=xxx 的消息"——xxx 已成孤儿。
4. **保留策略** — 无系统级数据保留期限（如"已删消息 90 天后物理清除"）。
   存储 GC 只处理文件 blob，不处理 messages/comments/notifications 的软删除行。

### 现有基础

- 所有业务表都有 `deleted_at`（GORM 软删），存储 GC 已有 `storage/gc/sweeper.go`
  框架可参考扩展为**通用数据保留策略回收站**。
- `audit_events` 已记录敏感操作，可为数据删除操作提供审计追溯。
- 通知/消息/评论的读端已经做了 block 过滤（`block.BlockChecker`），同理可扩展为
  "已注销用户数据匿名化过滤"。

### 建议实现路径

```
Phase 1 — 数据导出 API（P1）
  └─ GET /v1/export （异步生成包含用户全部数据的归档文件）
  └─ 通过 outbox 事件驱动 worker 组装归档并上传至存储

Phase 2 — 用户注销传播（P0）
  └─ SSO 侧增加 gRPC 流/Webhook 回调，通知 aero-id 某 uid 已注销
  └─ aero-id 接收事件后：
      ├─ 将该 uid 的 owner_uid/from_uid/to_uid 字段匿名化（替换为 "deleted" 标记）
      ├─ 或：将其所有资源 soft-delete（根据配置策略）
      └─ 写入审计日志

Phase 3 — 通用保留策略回收站（P1）
  └─ 将 storage/gc/sweeper.go 模式推广到 messages/comments/notifications
  └─ 配置级保留窗口（如 "已删消息保留 90 天"）
  └─ 到期物理清除 + 审计记录
```

### 涉及改动范围

| 层 | 改动 |
|---|---|
| `domain/` | 新增 `DeletedAccountPlaceholder` 常量；扩展 `AuditEvent` action 枚举 |
| `repository/` | 新增或扩展现有 repo 的 `HardDeleteBefore`、`AnonymizeUID` |
| `service/` | 新增 `DataLifecycleService`（导出 / 注销 / 保留） |
| `sso/` | 新增 `OnUserDeleted` gRPC 流回调客户端 |
| `handler/` | 新增导出/注销端点 |
| `migrations/` | 可能增加 `anonymized_uid` 列或 `data_retention_config` 表 |

---

## 方向二：群组会话与多人群聊

### 为什么需要

当前消息模型严格 1:1（`from_uid` → `to_uid`）。这是 MVP 的合理边界，但生产级协作
平台（Slack、Discord、飞书/钉钉）的核心能力是**多人群聊**。缺口：

1. **无"会话"（conversation）实体** — 当前 `Conversation` 是隐式的（= 两个 uid 的
   配对），没有会话 ID、会话元数据（标题、头像、创建时间）。
2. **无成员管理** — 不能创建群组、邀请/踢除成员、查看群成员列表。
3. **无群聊消息路由** — 消息路由逻辑是 `to_uid = recipient`，群聊需要 `to_conversation_id`。
4. **无已读汇总** — 群聊消息需要按会话维度汇总已读/未读成员，而非仅仅作者读回执。
5. **通知聚合** — 群聊中每条消息都发独立通知会大量刷屏，需要通知折叠/静音/@提及。

### 现有基础

- **通知系统**已完整支持 collapse、preference、block 过滤、SSE 实时推送
  ——群聊的通知聚合可以直接复用。
- **Outbox 事件驱动**已就绪，群聊的"新成员加入"、"消息已读汇总"等事件可由此发布。
- **`messages.kind`** 列已在 schema 中（SMALLINT，`1=文本` `2=图片` `3=文件附件`
  `4=系统通知`），但服务层仅处理 `kind=1`，`kind=4` 可用于"XXX 加入了群聊"等
  系统通知消息。
- **权限/角色基础设施**（`admin.go` + `migrations/000022`）可用于群组管理员身份。

### 建议实现路径

```
Phase 1 — 会话模型与 API（P1）
  └─ conversations 表（id, type[direct|group], title, avatar, created_by, created_at, updated_at, deleted_at）
  └─ conversation_members 表（conversation_id, uid, role[member|admin], joined_at, left_at）
  └─ messages 表新增 conversation_id 列（可为 NULL，向下兼容现有 1:1 消息）
  └─ API: CreateConversation, AddMember, RemoveMember, ListConversations, ListMembers

Phase 2 — 群消息收发（P1）
  └─ SendMessage 扩展：to_conversation_id 参数
  └─ 服务层：消息入 messages 表 + 通过 outbox 事件为每个成员生成通知
  └─ 通知折叠：相同会话的连续消息合并为一条 "N 条新消息" 通知 + collapse_count

Phase 3 — 已读汇总与 @提及（P2）
  └─ 已读回执表（conversation_id, uid, last_read_message_id, updated_at）
  └─ @提及：消息解析 mentions → 为被提及者产生高优先级通知
```

### 涉及改动范围

| 层 | 改动 |
|---|---|
| `domain/` | 新增 `Conversation`、`ConversationMember` 实体 |
| `repository/` | 新增 `conversation_repository.go` |
| `service/` | 新增 `ConversationService`；扩展现有 `MessageService` |
| `handler/` | HTTP + gRPC 端点和 proto |
| `notification/` | 群聊通知折叠逻辑（复用 collapse 机制） |
| `migrations/` | 2-3 个迁移文件 |

---

## 方向三：文件后处理管道（缩略图 / 审核 / 内容提取）

### 为什么需要

文件上传管道当前是纯同步的：存入存储 → 写 `files` 行 → 完成。后续没有**异步处理**。
这是已被原设计文档识别的缺口（见 `docs/superpowers/specs/2026-05-22-aero-id-design.md`
第 11.2 节 AI 时代增强），在 AI/内容协作场景下尤为重要：

1. **缺失缩略图** — 图片/视频上传后没有缩略图生成，文件列表/clients 端只能显示
   图标。UX 体验显著低于 Dropbox/Google Drive。
2. **缺失病毒/恶意文件扫描** — 上传任何文件都不经过 ClamAV 或类似扫描器，
   服务本身成为恶意软件分发载体（信任风险，尤其是文件分享功能启用后）。
3. **缺失内容审核** — 图片/文本没有自动审核（NSFW 检测、敏感内容识别），
   评论举报系统目前只能人工处理，没有 AI 辅助队列排序。
4. **缺失 OCR / 文本提取** — 无法搜索图片中的文字或 PDF 内容（`files.search_vector`
   仅索引文件名 `name`，不索引 `extracted_text`）。设计文档中已预留此列。
5. **缺失 Embedding 语义检索** — 设计文档的 Phase 1+2 路线图中规划了
   `embedding(VECTOR)` 列 + `pgvector` 扩展，目前零实现。

### 现有基础

- **Outbox 事件系统**已完整实现（`internal/outbox/`），是异步 worker 的完美驱动。
  每次文件上传可产生 `file.uploaded` 事件 → worker 消费处理。
- **Storage GC / sweeper**模式可复用作为 worker 框架（后台 goroutine + interval/ticker）。
- **`files.checksum`** 列已经存在且可在服务层做内容寻址去重，worker 判断"此文件
  是否已处理过"时可以直接依赖 checksum。
- **`files.meta`** 列是 JSONB，可存储处理结果（如 `{"thumbnail_key": "...",
  "scan_status": "clean", "ocr_text": "..."}`）。
- **评论举报/审核队列**（`comment_reports` + `status` + 服务层 `ResolveReport`）
  可作为内容审核的人工反馈回路，与 AI 审核联合使用。

### 建议实现路径

```
Phase 1 — 异步 Worker 框架（P0）
  └─ 将 outbox handler 链模式（internal/outbox/chain.go）扩展为通用 worker 框架
  └─ 引入 worker 注册表：handler 按 event_type 订阅
  └─ 支持并发度配置（每个 worker 的 goroutine 池大小）

Phase 2 — 缩略图生成 + 恶意文件扫描（P1）
  └─ 监听 file.uploaded 事件
  └─ 图片：生成多种尺寸缩略图，存储至 storage，meta 记录 thumbnail_key
  └─ 所有文件：调用 ClamAV（via CLI daemon 或 REST）扫描，meta 记录 scan_status
  └─ 风险文件自动变更 visibility=private / 通知上传者

Phase 3 — 内容提取与语义检索（P2）
  └─ OCR / PDF 文本提取 → 写入 files.extracted_text 或 search_vector 列
  └─ embedding 提取 → 写入 files.embedding（需 pgvector 迁移 + 新索引）
  └─ 搜索服务增加向量召回分支（基于 embedding 的语义搜索 + FTS 混合排序）
```

### 涉及改动范围

| 层 | 改动 |
|---|---|
| `internal/worker/` | 新包：通用异步 worker 框架（注册表、并发池、优雅关闭） |
| `internal/file/` | 新增 `processor.go`：thumbnail / scan / ocr / embedding |
| `internal/outbox/` | 扩展 handler chain，支持 per-type 订阅 |
| `storage/` | 新增 `PresignPut` 返回值复用（缩略图写入） |
| `domain/` | File 结构体增加 `ExtractedText`、`Embedding` 字段 |
| `migrations/` | `pgvector` 扩展安装 + `files.embedding` 列 |
| `config/` | Worker 配置（ClamAV 地址、缩略图尺寸、embedding 模型端点） |

---

## 方向四：面向水平扩展的分布式架构基座

### 为什么需要

当前架构的所有缓存和限流器都是**进程内内存**的。这对单实例部署完全够用，
但只要运行 2+ 个副本（Kubernetes 滚动更新、多 AZ 高可用），就会出现问题：

1. **速率限制不是全局的** — 每个实例各自计数。用户 A 访问实例 1 被限流，但
   请求打到实例 2 可能不被限流（尤其是 RPS 配置较低时）。限流效果随副本数稀释。
2. **SSO token 缓存不共享** — 新实例冷启动时 token 全部穿透到 SSO。
   虽然已有 `singleflight` 和 `stale-ttl` 缓解，但大规模滚动更新时仍然会造成
   SSO 请求尖峰（cache stampede）。
3. **SSO user 缓存不共享** — 同上，冷实例的 `GetUsers` 调用全部穿透。
4. **Idempotency key 存储依赖单库** — 当前的 idempotency key 存于 PostgreSQL，
   在写路径上有单点瓶颈（对于高吞吐场景，idempotency 表会成为写热点）。
5. **通知 SSE Hub 只在一个实例上有效** — 用户通过 WebSocket/SSE 连接实例 1，
   但 outbox relay 在实例 2 上处理了事件并推送通知，实例 1 的 SSE Hub 收不到。

### 现有基础

- **配置层**（`config.go`）已有 `RateLimitConfig` 预留字段，文档明确注释
  "PER-INSTANCE；distributed limiting would need shared state"，说明设计时已考虑。
- **Redis 在 go.mod 中尚未引入**，但 `go.opentelemetry.io/otel` 已引入，说明团队
  对引入外部依赖不抗拒。
- **SSO 客户端已实现 serve-stale、singleflight、circuit-breaker**，为缓存降级
  提供了最佳实践，可推广到 Redis 缓存模式。
- **Outbox relay 等后台循环**已全面支持优雅关闭，分布式模式下生命周期管理
  可以向 sidecar/工作队列迁移。

### 建议实现路径

```
Phase 1 — 集中式缓存层（P0）
  └─ 引入 Redis 客户端（go-redis/redis）
  └─ TokenCache + UserCache 可切换后端：Memory（现有）↔ Redis（新增）
  └─ 共享限流器：滑动窗口 + Redis Sorted Set / Cell Rate Limiting（generic cell rate algorithm）
  └─ Abstract Redis 服务作为可选的 shared 后端，fallback 到内存

Phase 2 — 跨实例 SSE 桥接（P1）
  └─ 通过 Redis Pub/Sub 分发通知推送事件
  └─ 每个实例的 outbox relay 在发布 SSE 时先发到 Redis Pub/Sub
  └─ 各实例的 SSE Hub 订阅 Redis Pub/Sub 频道 → 推送给本地连接
  └─ 结果：用户连接到任意实例都能收到实时通知

Phase 3 — Idempotency 卸载到 Redis（P2）
  └─ 可选的 idempotency store 后端：PostgreSQL（现有）↔ Redis（低延迟、TTL 自动过期）
  └─ 配置开关，默认 Postgres（零依赖），生产可选 Redis
```

### 涉及改动范围

| 层 | 改动 |
|---|---|
| `internal/pkg/ratelimit/` | 新增 RedisSlidingWindowLimiter 实现 |
| `internal/sso/` | TokenCache / UserCache 增加 Redis 后端适配器 |
| `internal/hub/` | 增加 Redis Pub/Sub 桥接（HubBridge） |
| `internal/idempotency/` | 新增 Redis 后端 |
| `internal/config/` | 新增 Redis 配置节（可选的） |
| `internal/app/` | 条件化注入 Redis 客户端 |

---

## 方向五：管理员 API 面补齐与运营工具

### 为什么需要

`migrations/000022` 建立了完整的管理员角色系统（roles 表 + permissions 数组 +
user_roles 关联 + `content.moderate` / `audit.read` / `user.ban` / `system.config`
四个内建权限），`domain/admin.go` 定义了权限常量，`repository/admin.go` 实现了
`HasPermission`、`GrantRole`、`RevokeRole`、`ListAudit`。但**管理员 API 面只暴露了
一条**：`GET /v1/admin/audit-log`。这意味着：

1. **没有角色管理 API** — 无法通过 API 授予/撤销角色，只能直接操作 DB。
2. **没有用户封禁 API** — `user.ban` 权限常量和 `user_roles` 表已存在，但没有
   封禁逻辑（实际哪张表记录封禁？是否存在 `banned_users` 表？没有）。
3. **没有系统配置 API** — `system.config` 权限已定义，但无 `system_config` 表
   或配置动态更新端点。
4. **没有内容全局审核 API** — `content.moderate` 权限可用于跨用户隐藏评论/消息，
   但目前 `HideComment`/`UnhideComment` RPC 只在校验 actor 是 owner 或 moderator
   后才执行（实际代码中 `service/comment.go` 的 `HideComment` 调用 `commentRepo`，
   权限已在 handler 层校验，但无专门的管理员批量隐藏/取消隐藏 API）。

### 现有基础

- **`repository/admin.go`** — `HasPermission`、`GrantRole`、`RevokeRole`、`ListAudit`
  均已实现并经过测试。
- **`internal/handler/http/admin.go`** — 已注册 `GET /v1/admin/audit-log`，权限校验
  模式可复用。
- **权限基础设施** — 中间件 `middleware/permission.go` 已存在（可做逐 handler
  注入）。
- **评论举报/审核** — `comment_reports` 表 + `ListReports`/`ResolveReport` RPC 已存在，
  但缺少"被隐藏内容的通知追诉"和"查看所有被隐藏内容的管理员视图"。
- **`domain/admin.go`** — `PermContentModerate`、`PermAuditRead`、`PermUserBan`、
  `PermSystemConfig` 四个权限常量均已定义。

### 建议实现路径

```
Phase 1 — 角色管理 API（P1）
  └─ POST /v1/admin/roles/grant   (PermUserBan 或 PermSystemConfig)
  └─ POST /v1/admin/roles/revoke  (同上)
  └─ GET  /v1/admin/roles         (列出所有角色及其成员)
  └─ gRPC 等价 RPC

Phase 2 — 用户封禁系统（P1）
  └─ banned_users 表（uid, reason, banned_by, banned_at, expires_at）
  └─ 中间件 / 服务层的封禁检查（与已有的 block 检查并列）
  └─ POST /v1/admin/users/ban   (PermUserBan)
  └─ POST /v1/admin/users/unban (PermUserBan)
  └─ gRPC 等价 RPC

Phase 3 — 系统配置 & 运营面板（P2）
  └─ system_config 表（key, value, updated_by, updated_at）
  └─ GET/PUT /v1/admin/config (PermSystemConfig)
  └─ 扩展审计日志端点：支持按时间范围、按 subject_type 过滤
```

### 涉及改动范围

| 层 | 改动 |
|---|---|
| `domain/` | 新增 `BannedUser`、`SystemConfig` 实体 |
| `repository/admin.go` | 新增 `BanUser`、`UnbanUser`、`ListBanned`、`GetSystemConfig`、`SetSystemConfig` |
| `service/` | 新增 `AdminService`（或扩展现有 service） |
| `handler/http/admin.go` | 新增角色管理、封禁管理、系统配置路由 |
| `handler/grpc/` | 新增/admin 的 RPC 实现 |
| `middleware/` | 封禁检查中间件（在 auth 之后、handler 之前） |
| `migrations/` | `banned_users` 表、`system_config` 表 |
| `config/` | 可选：system config 的持久化开关 |

---

## 总结：建议优先级

| # | 方向 | 优先级 | 工作量 | 依赖 | 理由 |
|---|------|--------|--------|------|------|
| 1 | 用户数据生命周期/合规 | **P0** | 中 | 无 | 法律合规风险，延迟迭代可能产生维权成本 |
| 2 | 群组会话 | **P1** | 高 | 无 | 最高用户可见价值，但工作量大，建议独立迭代 |
| 3 | 文件后处理管道 | **P1** | 高 | 无（worker 可自建） | 解锁缩略图、审核、语义搜索，技术杠杆最大 |
| 4 | 水平扩展基座 | **P0** | 中 | Redis | 上 K8s/多副本前必经之路 |
| 5 | 管理员 API 面补齐 | **P2** | 低-中 | 无 | 现有基础设施 80% 已完成，快速收尾 |

**建议执行顺序：** 方向四（分布式基座）→ 方向一（合规）→ 方向五（管理 API）→
方向三（文件处理）→ 方向二（群聊）。方向四和方向一有部分重叠（缓存共享可用于
加速 SSO 用户注销传播的查询），可并行启动小团队分工。
