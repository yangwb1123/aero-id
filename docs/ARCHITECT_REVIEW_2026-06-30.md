# aero-id — 架构审查与扩展方向分析

> 日期: 2026-06-30
> 作者: 资深架构师 / 产品经理视角
> 范围: 完整代码库全景扫描（所有 `.go`、`.proto`、`.sql`、`config.yaml` 及文档）
> 约束: 未编写任何代码，仅做分析

---

## 全景扫描总结

### 已实现的完整功能清单（截至 migration 000017）

| 领域 | 功能 |
|------|------|
| **文件** | CRUD、内容校验（大小上限 + MIME 白名单）、SHA256 内容去重、流式上传（S3 multipart / local）、presigned 直连上传（S3）、软删除 + 引用计数安全删除、元数据更新（PATCH）、显式共享/取消共享、共享列表查询、下载 URL presign、GC sweeper（孤儿 blob + 过期软删除） |
| **消息** | 1:1 发送/收件箱/会话视图、已读/未读标记 + 计数、全文搜索、系统消息、Reactions（React/Unreact）、软删除 + 编辑、MarkConversationRead |
| **评论** | 两级嵌套严格校验、目标可见性守卫、Like/Unlike（atomic counter）、举报/审核队列（Resolve/Dismiss）、Hide/Unhide、软删除 + 编辑、作者自管审核 |
| **通知** | Outbox 驱动异步写、SSE 实时推送（带心跳）、未读计数 + 列表、标记已读/全部已读、Block 感知（被拦截则跳过通知）、Webhook 锤链交付 |
| **用户拦截** | 方向性模型（blocker=recipient, blocked=actor → suppress inbound）、拦截/解拦截/列表查询、审计日志记录 |
| **全文搜索** | PostgreSQL tsvector + GIN 索引, 跨 messages/comments/files 三类实体，严格按调用方 uid 隔离可见数据 |
| **审计日志** | `audit_events` 表记录: file share grant/revoke、report resolve、block/unblock、message delete（fire-and-forget） |
| **传输层** | HTTP（Gin v1.10）+ gRPC（41 RPCs）双传输，共享同一 `service` 层；gRPC 含标准健康服务 + 可配置 reflection |
| **中间件（全链路）** | Auth（Bearer → SSO ValidateToken）、Rate Limit（in-memory token bucket, opt-in）、Recovery（panic→500）、CORS、Request ID、Access Log、Timeout（per-route ctx deadline）、Tracing（OpenTelemetry）、Idempotency（24h dedup）、Metrics（RED + SSO health gauges） |
| **SSO 客户端弹性** | Circuit breaker（仅 availability 错误触发）、singleflight（缓存抖动坍缩）、serve-stale + grace window、retry + exponential backoff、gRPC keepalive、TLS/mTLS + 证书公钥锁定（SPKI pinning） |
| **存储后端** | Local（HMAC-signed URL 下载）+ S3（presigned URL 直连上传/下载）、GC sweeper 兼容二者、可配置 retention 窗口 |
| **写入路径正确性** | 事务性 outbox（行+事件同事务）、幂等键（`idempotency_keys` 表）、atomic reply_count/like_count |
| **运维面** | Prometheus `/metrics`（RED + outbox lag + SSO cache 指标）、per-dependency `/readyz`（DB+SSO）、`/healthz`、`cmd/migrate` CLI、`auto_migrate` 启动选项 |

### 架构健康度评估

| 维度 | 评分 | 说明 |
|------|------|------|
| 分层清晰度 | ⭐⭐⭐⭐⭐ | handler → service → repository + storage/sso，严格单向依赖 |
| 测试覆盖 | ⭐⭐⭐⭐ | 27 包单元测试 + 21 包 testcontainers 集成测试，但 gRPC handler 测试有缺口 |
| 错误处理 | ⭐⭐⭐⭐⭐ | 统一的 `*AppError` 跨传输，仓库层返回 raw → service 层 wrap + 翻译 |
| 安全模型 | ⭐⭐⭐⭐ | Bearer 认证、签名下载URL、审计日志，但缺乏管理员角色体系 |
| 生产就绪度 | ⭐⭐⭐⭐ | 可观测性齐全、弹性 SSO、限流，但 GC/限流 opt-in 可能遗忘 |
| 可扩展性 | ⭐⭐⭐ | 单实例限流、无缓存层、1:1 消息模型是架构瓶颈 |

---

## 一、Edge Cases 处理缺口

### 1.1 并发热点：like_count / reply_count / reaction 的高争用

**位置：** `internal/repository/comment.go` 的 `Like()`/`Unlike()` 方法

**问题：** `Like()` 先 INSERT `comment_likes`（带 `ON CONFLICT DO NOTHING`），再独立 UPDATE `comments.like_count`。两步**不在同一个行级锁保护下**。在热点评论（如高赞帖）上，并发 like/unlike 可能导致 count 最终值落后于实际行数——或更糟，多减。

```go
// 当前模式（有竞态窗口）：
1. INSERT INTO comment_likes (...) ON CONFLICT DO NOTHING
2. UPDATE comments SET like_count = (SELECT count(*) FROM comment_likes WHERE comment_id = ?)
```

**建议：** 改为原子操作 `UPDATE comments SET like_count = like_count + 1 WHERE id = ?`，同时在仓库层使用 `SELECT ... FOR UPDATE` 或依赖数据库的 `RETURNING` 来获取更新后的值。

### 1.2 通知洪泛（Denial-of-Wallet / UX）

**位置：** `internal/service/notification.go` — `NotificationDispatcher.Handle()`

**问题：** Outbox relay 目前无 per-recipient 或 per-sender 节流。一个恶意用户可以：
- 快速发送 1000 条消息 → 每条触发 `message.sent` → 接收方创建 1000 条 `message.received` 通知行 → SSE 收到 1000 个 push
- 即使接收方已经 Block 了发送方，`Handle()` 仍被调用（只是不写通知行），relay CPU 被浪费

**建议：**
- 在 `Handle()` 中加 per-recipient 速率门限（如每分钟同类型最多 10 条通知）
- 引入通知去重/折叠：5 分钟内同一发送方同一类型的同类事件合并为一条（`"张三发送了 3 条消息"`）
- Block 检查前置到 `FetchUnpublished` 后的批量处理阶段以减少无用调用

### 1.3 文件 GC 竞态与引用计数

**位置：** `internal/service/file.go` — `Delete()`, `internal/storage/gc/sweeper.go`

**问题：**
1. `Delete()` 通过 `CountByStorageKey` 检查是否还有别的文件引用同一 blob。但软删除到 GC 实际清理 blob 之间有时间窗口——期间若另一个用户上传了相同 checksum 的文件（内容去重指向同一 storage_key），GC 可能误删 live blob。
2. GC sweeper 默认 **opt-in**（`gc.enabled: false`）。忘记开启则存储无限增长。

**建议：**
- 引入引用计数（`file_storage_refs` 表或 `files.storage_ref_count` 列），每次 `Create()` +1、`Delete()` -1，GC 只删 refcount==0 的 blob
- 在启动日志中：若 `storage.type != "local"` 且 `gc.enabled == false`，打印一条明显 WARN
- 考虑在默认配置中为 S3 后端自动启用 GC（仅需设置 `interval` 和 `retention` 即可安全运行）

### 1.4 SSE 连接生命周期 + 重连续传

**位置：** `internal/handler/http/events.go`, `internal/hub/hub.go`

**问题：**
- 无 `last_event_id` 支持：服务器重启后 SSE 连接断开，客户端重连后会丢失断开期间的所有通知
- Hub 的 channel cap=16：10 个设备在线 × 每条通知 10 次 channel send，在 burst 场景下慢消费者的 channel 会 silently drop
- `Hub.Close()` 关闭所有 channel 后，SSE handler 的 select 能正确退出（`ok=false`），但无连接断开的日志记录

**建议：**
- 实现 SSE 标准 `last_event_id` 字段：客户端携带上次收到的事件 ID，服务端查询通知表返回更新的通知
- Hub 改用 fanout 模式减少锁争用，或为每个 uid 使用无锁 ring buffer
- 记录 SSE 连接的生命周期日志（uid、连接时长、断开原因、推送事件数）

### 1.5 幂等键存储无 TTL 清理

**位置：** `internal/idempotency/store.go`, `configs/config.yaml`（`idempotency_ttl: 24h`）

**问题：** `idempotency_keys` 表定义了 TTL 配置但无后台清理机制。随着时间的推移，该表将无限膨胀。

**建议：** 在 `Store` 中增加可选的后台过期扫描 goroutine（类似 `RateLimiter.RunCleaner` 或 GC sweeper 模式），或依赖外部 `pg_cron` 定期执行 `DELETE FROM idempotency_keys WHERE created_at < now() - interval '24h'`。

### 1.6 全文搜索查询超时与资源控制

**位置：** `internal/repository/*_search.go`

**问题：** `plainto_tsquery` + `ts_rank` ORDER + `LIMIT` 查询无 `statement_timeout` 保护。特制长查询或复杂 `tsquery` 可能锁住数据库连接数十秒。

**建议：**
- 搜索执行前 `SET LOCAL statement_timeout = '5s'`
- 服务层限制查询最大长度（如 200 字符）
- 为每种 search kind 设置独立的超时（messages 搜索可能比 files 更慢）

### 1.7 Outbox relay 无失败退避

**位置：** `internal/outbox/relay.go`

**问题：** Handler 失败的事件被保留并重试，但重试间隔固定为 `poll_interval`（默认 5s）。如果 webhook endpoint 持续 500，所有事件都会以固定间隔反复重试——造成无用轮询和日志噪音。

**建议：** 引入 per-event 的 exponential backoff：
- `outbox_events` 表增加 `retry_count` 和 `last_retry_at` 列
- `FetchUnpublished` 过滤条件增加 `last_retry_at IS NULL OR last_retry_at < now() - backoff(retry_count)`
- 上限间隔 1h，最大重试次数可配置（超出则标记 `dead_letter`）

---

## 二、性能优化机会

### 2.1 通知列表查询索引确认

**热点查询：** `SELECT ... FROM notifications WHERE recipient_uid = ? AND read_at IS NULL ORDER BY created_at DESC LIMIT n+1`

**检查：** `000017_notification_indexes.up.sql` 是否创建了覆盖此查询的复合索引。理想的索引是 `(recipient_uid, read_at, created_at DESC)` 或部分索引 `(recipient_uid) WHERE read_at IS NULL`。

### 2.2 FTS 中文分词支持

**问题：** 当前使用 `plainto_tsquery('simple', ?)`。`simple` 字典对中文（CJK）不做分词——"你好世界"会被当成一个 token 而不是 "你好" + "世界"。对中文内容的搜索结果会严重漏召回。

**建议：**
- 安装 PostgreSQL 扩展如 `zhparser` 或 `jieba`，为中文内容使用专门的文本搜索配置
- 或在应用层使用 go 分词库（如 `gojieba`）对查询和内容预处理后使用 `english` 字典的 tsvector
- 短期方案：降级为 `LIKE '%query%'` 至少能搜到，但性能差；FTS 在中文场景几乎不可用

### 2.3 SSE 多设备推送的锁争用

**问题：** `hub.pub()` 持有 `RLock` 后遍历所有 channel 逐个非阻塞 send。当同一 uid 有 10+ 设备在线且通知量较大时，`RLock` 的持有时间与设备数成正比，与 `Subscribe()`/`Unsubscribe()` 的 `Lock` 形成竞争。

**建议：**
- 为每个 uid 使用独立的 `sync.RWMutex` 替代全局锁（"per-uid sharding"）
- 或使用无锁设计：`sync.Map` + per-channel select（复杂度高，收益可能不大）

### 2.4 Rate Limiter 全局锁

**位置：** `internal/pkg/ratelimit/ratelimit.go`

**问题：** 每个 `Allow()` 调用获取全量 `l.mu.Lock()`。在高并发下（20k+ RPS，数千 key），这是一个真实争用点。

**建议：**
- 引入分片锁（sharded mutex，如 64 个 shard，按 key hash 映射）
- 或在分布式场景下直接替换为 Redis-based limiter（见扩展方向 5）

### 2.5 冷启动预热

**当前预热：** 仅 SSO warmup probe（3s timeout）。数据库连接池懒加载到首次请求。

**建议：** 在 `/readyz` 翻转为 healthy 后（或 `Run()` 中），异步触发：
- 数据表预热查询：`SELECT 1 FROM messages LIMIT 1`（触发连接建立 + GORM 准备）
- SSO 缓存预热：批量解析当天活跃用户（如果 SSO 支持）
- S3 存储连接检查：`HeadBucket` 验证凭证
- 预计算一些查询计划（PostgreSQL `pg_prewarm` 扩展）

### 2.6 Outbox Pending 堆积监控

**问题：** `FetchUnpublished` 在 `published_at IS NULL` 上应有部分索引（`000015`）。当事件生产速度 > 消费速度时，pending 事件量线性增长，每次轮询扫描行数增加。

**当前防护：** 已有 `ObserveOutboxDrain` → `pending` 指标 + `oldestAge` 指标。需要设置告警。

**建议：** 在操作手册（runbook）中定义：
- `pending > 1000` → WARN（堆积中）
- `oldestAge > 5min` → CRITICAL（消费停滞）

---

## 三、高价值扩展方向

### 🔥 Direction 1：多人群聊 / 频道系统

| 项目 | 内容 |
|------|------|
| **当前状态** | 消息系统纯 1:1 模型——`Message.ToUID` 是单字符串，`ListInbox`/`ListConversation` 假设双人会话。SSE 推送范围覆盖的是 1:1 场景。 |
| **缺失的核心能力** | 三人以上群聊、频道（topic-based pub/sub）、群管理员、成员管理（邀请/退出/踢出）、消息@提及 |
| **实现复杂度** | **高**——架构级变更 |

#### 为什么需要它

1. **产品层面最大的单一功能缺口。** 文件共享（已实现）天然需要群聊语境——"我分享这个文件给项目组"比逐人分享合理得多。当前必须先分享给 A，再分享给 B，没有"分享给群聊"的概念。
2. **多对多消息是社交/协作平台的核心场景。** 从 MVP 到产品的跨越，往往就是从 1:1 到群聊的这一步。
3. **天然复用现有通知架构。** 群聊会在 outbox 事件面引入新通知类型（`group.message.sent`、`group.member.joined`、`@mention`），完全可以用现有的 `NotificationDispatcher` 模式处理。
4. **SSE 推送已就绪。** 实时性基础设施到位，只缺 data model。

#### 实现要点

- **Schema 变更：** 新增 `conversations` 表（类型：1:1 / group）、`conversation_members` 表（角色：owner/admin/member）、改造 `messages` 加 `conversation_id` 替代 `to_uid`
- **迁移策略：** 为现有 1:1 消息自动创建镜像 conversation（`SELECT DISTINCT LEAST(from_uid, to_uid), GREATEST(from_uid, to_uid)`）
- **群聊通知：** 每条群消息写入 N 条通知（N=成员数，排除发送者 + 被拦截者），引入批量写入优化
- **API 变更：** `SendMessage` 增加 `conversation_id` 参数；新增 `CreateConversation`、`AddMember`、`RemoveMember`、`LeaveConversation` 等端点
- **注意：** 这是架构级变更，建议作为独立 EPIC 规划，分 2-3 个里程碑落地

---

### 🔥 Direction 2：文件预览管道（Thumbnailing / Transcoding）

| 项目 | 内容 |
|------|------|
| **当前状态** | 文件存储为原始 blob——上传什么存什么。下载时直接返回原始文件。`File.MimeType` 字段记录了类型但不做任何后处理。 |
| **缺失的核心能力** | 图片缩略图自动生成、视频首帧截图、文档（PDF/DOCX）预览、音频转码。无异步后处理 Job 系统。 |
| **实现复杂度** | **中-高**——依赖 Job Queue 系统 |

#### 为什么需要它

1. **产品形态从"文件存储"到"文件管理"的关键一步。** 没有预览功能的文件服务只是一个带认证的 S3 bucket。
2. **消息和评论中的文件分享需要缩略图。** 收件人需要看到图片缩略图或文档预览卡片，而不是只显示文件名和大小。
3. **移动端和低带宽用户的必备体验。** 原始照片可能 10MB+，没有缩略图意味着每次列表加载都要下载完整文件。
4. **驱动 Job Queue 基础设施的建设。** 缩略图生成需要异步 Worker 系统——这是"广义后台任务处理"能力的起点，后续 GC 引用计数、数据导出、批量操作都可以复用。

#### 实现要点

- **Job Queue 包：** 新建 `internal/jobqueue`，支持 Postgres-backed 队列（基于 outbox 模式泛化）或独立 worker 进程
- **预览事件流：** 文件上传完成 → emit `file.created` outbox event → NotificationDispatcher 新 Handler 分支触发 thumbnail 生成 job
- **派生文件模型：** 缩略图存为派生文件（`parent_id` 指向原始文件），支持 `GET /files/:id?variant=thumb_small` 查询参数
- **MIME 类型策略：** 可配置的 `allowed_preview_types`（默认: image/*, application/pdf）
- **存储：** 缩略图存为独立 blob，复用已有的 storage backend，keys 命名约定 `thumb/{original_key}_sm.webp`
- **注意：** 缩略图生成需要外部工具（`ffmpeg`、`libvips` 或 `ImageMagick`），引入容器依赖

---

### 🔥 Direction 3：平台级审核 / 管理员角色系统

| 项目 | 内容 |
|------|------|
| **当前状态** | 审核是作者自管的——只有评论作者可以查看评论的举报、hide/unhide、resolve/dismiss。无全局管理员角色、无内容置顶、无消息全局撤回、无用户封禁。`UserBlock` 是用户自保机制，不影响该用户对其他用户的操作。 |
| **缺失的核心能力** | 管理员角色 + 权限系统、全局内容审查台、用户级封禁（account ban）、消息全局撤回、审计日志查询 API |
| **实现复杂度** | **中**——新增模块，不破坏现有模型 |

#### 为什么需要它

1. **生产环境合规要求。** 没有管理员无法应对：儿童安全（CSAM）举报、版权投诉（DMCA）、垃圾信息清理、恶意用户封禁。这是从"玩具"到"平台"的硬性门槛。
2. **当前审核模型不可扩展。** 作者自管在 100 用户时足够，100 万用户时不可能。需要专职审核团队，而审核团队需要管理后台。
3. **SSO 不提供角色信息（按设计——SSO 只做身份认证）。** aero-id 需要一个内部轻量的角色系统，标记某些 uid 为 "moderator" 或 "admin"。不要依赖外部 SSO 提供角色——那会耦合两个系统的生命周期。
4. **审计日志已实现但无法查询。** 当前审计只写不读。管理员需要"谁在什么时候对什么做了什么"的检索能力——这是合规审计的基本要求。

#### 实现要点

- **角色表：** `roles`（code, name, permissions[]）、`user_roles`（uid, role_code, granted_by, granted_at）
- **权限点：** `content.moderate`（全局审核评论/消息）、`user.ban`、`audit.read`、`system.config`
- **Auth 中间件扩展：** 新增 `RequirePermission("content.moderate")` 中间件工厂
- **审核端点：** `GET /v1/admin/reports`（跨所有举报）、`POST /v1/admin/users/:uid/ban`、`POST /v1/admin/messages/:id/global-delete`
- **审计查询：** `GET /v1/admin/audit-log?actor_uid=xxx&action=xxx&from=xxx&to=xxx`（分页）
- **模型注意：** 不要引入"超级管理员"神奇角色——权限是组合的，审计日志中记录审核员身份

---

### 🔥 Direction 4：通知偏好设置 + 免打扰

| 项目 | 内容 |
|------|------|
| **当前状态** | 通知是 all-or-nothing。用户无法选择通知类型、无法设置免打扰时段、无法静音特定用户。`Block` 是全有全无的——拦截后连系统消息都收不到。 |
| **缺失的核心能力** | 通知类型开关、免打扰时段（每天/每周）、用户级静音列表、Push 优先级、摘要模式 |
| **实现复杂度** | **低-中**——可以渐进实现 |

#### 为什么需要它

1. **通知疲劳是用户流失的首要原因之一。** 没有偏好设置，用户面临选择：要么接收所有通知（不堪其扰），要么完全关闭通知（错过重要消息）。两种都导致留存率下降。
2. **当前 SSE 推送无服务端过滤。** 偏好过滤应该在 `NotificationDispatcher` 层处理——比客户端过滤更可靠（客户端可能 JS 未加载、Push 已发送到设备）。
3. **实现成本低，复用现有模式。** `Dispatcher` 已经有 `WithDispatcherBlockChecker` 的回调模式；再加一个 `WithDispatcherPreferenceChecker` 即可。这是一个日间 200 行的功能，但影响面广。
4. **为 Push Notification（移动端）做准备。** 移动推送成本高（FCM/APNs 有配额和费用），必须在服务端精细控制推送频率和类型——偏好设置是前置条件。

#### 实现要点

- **偏好表：** `notification_preferences`（`uid`, `notification_type`, `enabled`, `quiet_hours_start`, `quiet_hours_end`, `timezone`, `muted_actor_uids[]`）
- **PreferenceChecker 接口：** 与 `BlockChecker` 并列，`Handle()` 中写入前检查
- **SSE 推送前也检查：** 即使通知行已写入，SSE push 前也过偏好过滤（考虑已写入但用户设置了免打扰的场景）
- **API：** `GET /v1/notification-preferences`、`PUT /v1/notification-preferences`（全量更新或 patch）
- **默认值：** 所有通知类型默认开启，与当前行为兼容（向后兼容）
- **免打扰的边界情况：** 系统消息（密码重置、安全告警）应始终发送——偏好不应覆盖安全通知

---

### 🔥 Direction 5：分布式限流 + 实体缓存层

| 项目 | 内容 |
|------|------|
| **当前状态** | 限流是 per-instance in-memory（注释明确说 "intentionally out of scope" for distributed）。业务层无缓存——每个 `GetFile`、`GetMessage`、`ListInbox` 都穿透到数据库。 |
| **缺失的核心能力** | Redis 集中缓存（L1 memory + L2 Redis）、分布式限流、缓存失效通知、查询缓存预热 |
| **实现复杂度** | **中**——增量引入，不改变接口 |

#### 为什么需要它

1. **多实例部署是生产标配。** 当前限流设计是"每实例 20 RPS"——如果横向扩展到 5 实例，实际容量是 100 RPS，完全不可控。一个用户可以轻松绕过限流。
2. **业务数据是 read-heavy 负载。** 消息一旦写入不再变更（除软删除和编辑）。文件元数据几乎不变。缓存命中可将 P99 延迟从 5ms 降到 <100μs，同时减少数据库连接数（当前 `max_open_conns: 50`，读缓存后可降低到 20）。
3. **架构已完美分层，加缓存层是替换接口实现。** `Repository` 接口已定义，包装为 `cachedRepository` 是标准 Decorator 模式，不需要改任何 handler 或 service 代码。
4. **基础设施成本优化。** 数据库是通常是最贵的依赖。减少 read IO 意味着可以降级数据库实例规格或延长连接池寿命。

#### 实现要点

- **缓存抽象：** 定义 `internal/pkg/cache` 包，接口包含 `Get/Set/Delete`，实现选型 `Redis` + 进程内缓存（`freecache`/`ristretto`）作为 L1
- **Repository 包装：** 每个 repository 方法判断是否可缓存：
  - `GetFile(id)` → 缓存，key: `file:{id}`, TTL: 5min
  - `GetMessage(id)` → 缓存，key: `message:{id}`, TTL: 5min
  - `ListInbox` / `GetConversation` → 不缓存（一致性太复杂）
- **缓存失效：** `Create`/`Update`/`Delete` 操作后直接 `Delete` 对应 key（write-through 策略简化为 cache-aside + 主动失效）
- **分布式限流：** 用 Redis `CL.THROTTLE` 命令或 Lua script 实现 `redis-based token bucket`
- **配置开关：** `cache.enabled: false`（默认关闭），`cache.redis.addr`，与现有单实例模式兼容
- **注意：** Redis 引入会增加运维复杂度（高可用、网络延迟），建议作为可选增强而非必须依赖

---

## 四、技术债清单（按严重程度排序）

| 级别 | 项目 | 位置 | 估算修复时间 |
|------|------|------|------------|
| **High** | `like_count` 非原子 UPDATE，高并发下可能漂移 | `internal/repository/comment.go` | 半天 |
| **High** | `idempotency_keys` 表无 TTL 清理机制 | `internal/idempotency/store.go` | 半天 |
| **High** | SSE 无重连续传（`last_event_id`）——服务器重启丢通知 | `internal/handler/http/events.go` | 1 天 |
| **Medium** | GC sweeper 默认关闭，生产容易忘记开启 | `configs/config.yaml` + `app.go` | 1 小时（加 WARN 日志） |
| **Medium** | FTS 中文分词无效——`simple` 字典不切分 CJK | `internal/repository/*_search.go` | 2 天（含扩展安装） |
| **Medium** | Outbox handler 失败无退避，可能无限重试 | `internal/outbox/relay.go` | 1 天 |
| **Medium** | 搜索无 `statement_timeout`，可能锁住连接 | `internal/repository/*_search.go` | 半天 |
| **Low** | 文件删除的引用计数竞态（软删除窗口） | `internal/service/file.go` + GC | 2 天 |
| **Low** | Outbox `FetchUnpublished` 扫描行数随堆积增长 | 已有指标可监控 | 半天的 runbook 编写 |
| **Low** | 通知洪泛无速率门限 | `internal/service/notification.go` | 1 天 |

---

## 五、建议的路线图（优先级 + 依赖关系）

```
Q3 2026
├── P0 (立即)
│   ├── ✅ like_count 原子化（技术债修复）
│   ├── ✅ idempotency TTL 清理（技术债修复）
│   └── ✅ GC 默认开启+WARN 日志（防遗忘）
│
├── P1 (短期，1-2 月)
│   ├── 🔥 Direction 4: 通知偏好设置（低复杂度，高 UX 回报）
│   ├── 🔥 Direction 3: 管理员角色系统（合规门线）
│   ├── 🔧 SSE last_event_id 重连续传
│   └── 🔧 Outbox handler exponential backoff
│
├── P2 (中期，2-4 月)
│   ├── 🔥 Direction 1: 多人群聊（架构级变更，分阶段）
│   │   └── Phase 1: conversations + members 表、迁移现有 1:1
│   │   └── Phase 2: 群聊发送/成员管理端点
│   │   └── Phase 3: 群通知 + @提及
│   ├── 🔥 Direction 2: 文件预览管道（需 Job Queue）
│   │   └── Phase 1: Job Queue 基础设施
│   │   └── Phase 2: 图片缩略图生成
│   │   └── Phase 3: 文档/视频预览支持
│   └── 🔧 FTS 中文分词支持
│
└── P3 (长期)
    ├── 🔥 Direction 5: 分布式限流 + 缓存层（需 Redis 运维支撑）
    ├── 🔧 通知洪泛速率门限 + 折叠
    ├── 🔧 文件引用计数改进
    └── 🔧 冷启动预热策略
```

---

## 附录：审查方法论

- **范围：** 全量扫描 `/home/u1/aero-id/` 目录下所有 `.go`、`.proto`、`.sql`、`.yaml`、`.md` 文件
- **方法：** 逐包阅读 + 关键路径追踪 + 数据流分析 + 文档对比（`ARCHITECTURE.md` + `ROADMAP.md` + `DECISIONS.md` + `TODO.md`）
- **不关注：** `.claude/worktrees/`（历史工作区快照），以 `main` 目录（当前工作目录）为准
- **版本基线：** 17 migrations（`000001` → `000017`），41 个 gRPC RPCs，完整 HTTP↔gRPC 传输层对等
