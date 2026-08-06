# aero-id — Edge Cases, Performance 与系统性优化方向

> 日期: 2026-07-10
> 视角: 资深架构师 + SRE
> 方法: 全局代码扫描（`internal/` 全量 120+ 源文件、22 个迁移、配置、已有 8 轮审查）
> 前置: `feat/roadmap` 已合并，38 个 gRPC RPC + 全 HTTP 对等 + 生产加固已交付
> 关联文档: `2026-07-10-architecture-extensions.md`（产品形态扩展方向）

---

## 文档定位

本文档与 `2026-07-10-architecture-extensions.md` 互补。前者从**产品经理视角**提出 5 个高价值功能方向（群聊、活动流、策略引擎、审核工作台、文件加工管线）；本文从**资深架构师/SRE 视角**，聚焦于：

1. **Edge Cases** — 当前实现中边界情况处理的分析与修补建议
2. **性能优化** — 可量化的性能瓶颈与改进方案
3. **系统性优化方向** — 3-5 个以生产健壮性为核心的扩展方向

---

## 一、Edge Cases 系统分析

### 1.1 并发安全（Concurrency Safety）

| # | Edge Case | 现状 | 风险等级 | 建议 |
|---|-----------|------|---------|------|
| EC-1 | **并发点赞取消点赞** | `comment_likes` 的 `(comment_id, uid)` 唯一约束防止重复点赞。但 `like_count` 的 UPDATE 在 service 层通过 `db.Model(...).UpdateColumn("like_count", gorm.Expr("like_count + 1"))` 做原子递增。然而**同时点赞和取消点赞**的并发场景下，如果先 SELECT 判断状态再 UPDATE，存在 TOCTOU 窗口。SELECT 结果在 UPDATE 时已过时。 | 中 | 使用单条 UPSERT 语句（`ON CONFLICT DO NOTHING`/`ON CONFLICT DO DELETE`）在 SQL 层面决定 toggle，避免应用层判断 |
| EC-2 | **评论回复计数与软删除不同步** | 当评论被软删除后，其父评论的 `reply_count` 不会递减。删除一条评论后，列表仍然显示"有 3 条回复"，但点击只能看到 2 条（因为 1 条已软删除）。这不是数据不一致，但**对用户是误导**。 | 低 | 删除时可选递减 `reply_count`，或在查询时用 `WHERE deleted_at IS NULL` 计数而不是依赖缓存的列值 |
| EC-3 | **文件分享授权与并发删除竞态** | 用户 A 分享文件给用户 B，同时用户 A 删除该文件。当前 `DeleteFile` 直接删除行 + blob，没有检查是否有活跃的分享记录。如果分享记录先于删除发生，B 可能已经获取了 presigned URL，但文件已从后端删除 → B 看到 404。 | 中 | 删除文件前检查（或至少记录）活跃分享；删除后将现有 presigned URL 标记为无效 |
| EC-4 | **Outbox 重复投递（at-least-once）** | Outbox relay 在 `MarkPublished` 之后但在确认提交前崩溃 → 重启后同一事件被再次投递。`NotificationDispatcher` 当前对重复投递不做去重。用户可能收到两条相同的通知。 | 中高 | 通知表在 `(recipient_uid, type, subject_type, subject_id)` 上加唯一约束，UPSERT 插入；或在 dispatcher 中做幂等检查 |
| EC-5 | **SSE channel cap=16 丢失事件** | `hub.Hub.Subscribe` 创建的 channel buffer 为 16。如果客户端消费慢（如移动端断连），超过 16 个事件被丢弃。`hub.pub` 使用 `select { case ch <- evt: default: }` 静默丢弃。用户永远看不到被丢弃的通知。 | 中 | 增加 cap 到 128 或 256；添加客户端侧事件序列号监测；在事件 payload 中加入序列号使客户端能检测到丢失 |

### 1.2 数据完整性（Data Integrity）

| # | Edge Case | 现状 | 风险等级 | 建议 |
|---|-----------|------|---------|------|
| EC-6 | **文件上传完成前崩溃** | 预签名上传路径（`PresignPut` + `CompleteUpload`）在 S3 直接上传完成后调用 `CompleteUpload`。如果客户端上传成功但 `CompleteUpload` 请求丢失（网络问题），S3 上有 blob 但 DB 无行。GC sweeper 的 orphan sweep 最终会清理它（但 GC 默认 disabled！）。 | 高 | 默认启用 GC 的 orphan sweep？或在 `CompleteUpload` 前用 S3 事件通知触发补偿。目前 `deployments/docker-compose.yml` 中的默认配置未启用 GC |
| EC-7 | **软删除记录的硬删除级联** | `sweeper.purgeSoftDeleted` 在硬删除文件行后试图删除 blob。但如果 blob 被多个文件共享（去重场景），`CountAnyByStorageKey` 返回的计数包括了**同一批次中刚刚硬删除的行**？不对——`HardDelete` 先在 DB 中删行，然后阶段性地检查 `CountAnyByStorageKey`。但如果一批中有两个文件引用同一个 blob，第一个文件的行已删除，`CountAnyByStorageKey` 返回 1（因为第二个文件的行还在——虽然是软删除状态），blob 不会被删除。但如果第二个文件也在同一批中，它在硬删除后不再计入计数——**blob 被遗漏**。 | 中 | 在删除 blob 前统一收集所有 storage key，对所有 key 做一次 `SELECT COUNT(*) FROM files WHERE storage_key = ? AND deleted_at IS NULL`（忽略软删除状态），而不是分批检查 |
| EC-8 | **消息的 `to_uid` 是 SSO uid，无外键约束** | 当前 design 有意不设 FK。但如果 SSO 删除了一个用户（账户注销），aero-id 中的消息、评论、文件都会留下悬空的 `owner_uid`。当前没有任何处理逻辑。 | 中 | 设计"账户注销回调"端点（SSO → aero-id webhook），级联软删除该用户的所有资源 |
| EC-9 | **通知偏好静音时段跨时区** | `NotificationPreference` 的 `quiet_hours_start/end` 是 `HH:MM` 字符串，`timezone` 是 IANA 时区名。当前 dispatcher 中似乎**未实现静音时段过滤**（`WithDispatcherPreferenceChecker` 可能只检查了 `enabled` 布尔值）。如果确实未实现，静音时段配置存储了但不生效。 | 中 | 确认 `PreferenceChecker` 是否实现了时区感知的静音时段判断；如未实现，补充实现 |

### 1.3 资源管理（Resource Management）

| # | Edge Case | 现状 | 风险等级 | 建议 |
|---|-----------|------|---------|------|
| EC-10 | **SSO 缓存无容量上限** | `TokenCache` 和 `UserCache` 使用 `sync.Map` + TTL 过期。但缓存大小**无上限**。活跃用户数 × 每个用户的 token 数 = 潜在的无限增长。一个攻击者可以通过创建大量不同 token 来填满缓存。 | 中高 | 使用 LRU 或有界 map（如 `hashmap` 包）；在 `Set` 时检查缓存大小并在超过阈值时驱逐最旧条目 |
| EC-11 | **搜索查询无超时/资源限制** | 全文搜索可能执行昂贵的 `tsquery` 操作，尤其是在没有索引覆盖的列上。当前 repository 层直接执行 GORM 查询，**没有 `statement_timeout`**。恶意的模糊搜索查询可能占用 DB 连接数秒。 | 中 | 在 service 层设置 context timeout（如 3s）；在 repository 层使用 `db.Set("gorm:query_options", "SET LOCAL statement_timeout = '3s'")` |
| EC-12 | **限流器在 gRPC 上的表现** | HTTP 限流通过 `RateLimit` 中间件实现（返回 429）。gRPC 限流在 `server.go` 中以拦截器方式实现（返回 `ResourceExhausted`）。但两者共享同一个 `*ratelimit.Limiter` 实例。如果攻击者同时通过 HTTP 和 gRPC 发送请求，**限流是共享的**——一个通道的突发流量会耗尽另一个通道的配额。这可能是预期的行为（限制用户总请求率），也可能是非预期的（用户以为 HTTP 被限后还可以通过 gRPC 绕过）。 | 低 | 文档化此行为；或提供 per-transport 限流选项 |
| EC-13 | **大文件上传导致 handler 超时** | `request_timeout` 默认 30s。对于 50MB 文件的预签名直传，超时不在 aero-id 控制范围内（直接上传到 S3）。但对于**本地存储后端的非流式上传**，如果 `max_upload_bytes` 为 50MB 且网络慢，30s 可能不够。当前 timeout middleware 在 context 超时时不会清理已上传的部分数据。 | 低 | 为文件上传设置独立的、更长的 timeout；或在超时时触发补偿清理 |
| EC-14 | **Idempotency key 哈希碰撞** | Idempotency key 由客户端提供并按 `(uid, endpoint, key)` 三元组索引。如果两个客户端（不同 uid）恰好使用相同的 key，它们不会冲突。但如果同一个客户端对两个不同端点使用相同的 key，它们被视为并行——这是正确的行为。但如果客户端使用**极长的 key**（如 10KB），存储和索引开销会上升。 | 低 | 在 middleware 中对 key 做 SHA256 哈希，存储 hash 而非原始 key |

### 1.4 安全边界（Security Boundaries）

| # | Edge Case | 现状 | 风险等级 | 建议 |
|---|-----------|------|---------|------|
| EC-15 | **搜索 API 的跨用户数据泄露** | `SearchService` 被设计为搜索用户有权访问的内容。但 `SearchFiles` 的默认实现**只搜索自己的文件**。搜索其他用户的公共文件或通过分享授权的文件呢？附录中已指出 `search 结果不检查 file_shares 权限`。 | 中高 | 在 search repository 层 UNION 可见的文件：`WHERE f.owner_uid = ? OR f.visibility = 'public' OR f.id IN (SELECT file_id FROM file_shares WHERE shared_with_uid = ?)` |
| EC-16 | **block 用户仍可被搜索到** | 如果用户 B 被用户 A block，B 的消息不应出现在 A 的搜索结果中。但当前 search 查询**不检查 block 关系**。 | 中 | 在搜索查询中加入 `NOT IN (SELECT blocked_uid FROM user_blocks WHERE blocker_uid = ?)` 过滤 |
| EC-17 | **消息反应中的隐私泄露** | 用户可以对消息添加 reaction。其他参与者可以看到谁添加了什么 reaction。但如果一个用户被 block，他们是否应该能看到被 block 用户的 reaction？当前不明确。 | 低 | 在 ListReactions 时检查 block 关系 |
| EC-18 | **文件上传不写审计日志** | 文件上传、更新元数据等操作不记录审计日志。虽然 `audit_log` 迁移（000014）和 `AuditEvent` 模型已存在，但文件域操作未接入。如果后续需要审计"谁在什么时候上传了什么文件"，没有数据。 | 低中 | 在 `CompleteUpload`、`UpdateMetadata`、`DeleteFile` 中写入 `audit_events` |

---

## 二、性能优化机会

### 2.1 数据库查询优化

| # | 问题 | 涉及文件 | 影响 | 建议 |
|---|------|---------|------|------|
| PERF-1 | **Notification 列表查询使用全表扫描** | `internal/repository/notification.go` — `ListByRecipient` | 用户有 10,000+ 条通知时，按 `created_at DESC` cursor 扫描，但 `(recipient_uid, created_at DESC)` 索引可能不存在或不是覆盖索引。当前迁移 `000017` 可能已创建此索引，需要验证是否有效。 | 验证 `idx_notifications_recipient_created` 索引是否包含 `read_at` 列（用于未读过滤）。如果是部分索引 `WHERE read_at IS NULL`，已读通知的列表查询仍然慢 |
| PERF-2 | **Outbox FetchUnpublished 在全表上 ORDER BY** | `internal/outbox/repository.go` — `FetchUnpublished` 查询 `WHERE published_at IS NULL AND dead_letter = false ORDER BY created_at ASC LIMIT ?` | 当 outbox 表很大（>100 万条）时，即使有 `(published_at, created_at)` 索引，`ORDER BY created_at` 可能需要排序。如果很多事件被死信标记（dead_letter=true），大量非活跃行仍然被扫描。 | 使用 `(published_at, dead_letter, created_at)` 复合索引；定期清理死信事件（DELETE FROM outbox_events WHERE dead_letter = true AND created_at < now() - 7d） |
| PERF-3 | **跨领域搜索使用多个独立查询** | `internal/search/` — 全文本搜索分别在 messages、comments、files 三个表上独立执行 `tsquery`，然后在应用层合并排序 | 当用户搜索常见词时，每个查询返回大量结果（LIMIT n+1），三个查询 x 三种排序 = 9 次索引扫描。如果使用 `websearch_to_tsquery('simple', ?)` 可能无法利用中文分词。 | 考虑使用 PostgreSQL 的 `tsquery` + `tsvector` 统一在物化视图中索引三个表；或为中文环境配置 `zhparser` 扩展 |
| PERF-4 | **文件列表查询无分页优化** | `internal/repository/file.go` — `ListByOwner` 可能简单按 `created_at DESC LIMIT` 查询 | 如果用户有 10 万个文件，"下一页"的 cursor 查询会随着 offset 增大而变慢（虽然使用了 cursor > id 模式而非 OFFSET，但 deep cursor 仍在走全索引扫描）。 | 验证 `(owner_uid, id DESC)` 索引的效能；考虑覆盖索引 `INCLUDE (name, mime_type, size, created_at)` 避免回表 |
| PERF-5 | **消息 Inbox 查询 JOIN 过多** | `internal/repository/message.go` — Inbox 查询需要 JOIN `file_shares`、`user_blocks`、`messages` 自身 | 一个用户的 inbox 查询可能 JOIN 4-5 张表来过滤可见性和 block 关系。如果 inbox 很大（>5000 条未读），查询延迟 > 100ms。 | 使用物化视图或缓存来维护每个用户的 inbox 摘要；或引入 `last_message_created_at` 列到 `channel_members`（群聊方向） |

### 2.2 缓存与内存优化

| # | 问题 | 涉及文件 | 影响 | 建议 |
|---|------|---------|------|------|
| PERF-6 | **SSO TokenCache 无预热机制** | `internal/sso/token_cache.go` | 服务重启后，所有活跃用户的 token 都会 cache miss，穿透到 SSO。对于一个有 10K 在线用户的集群，重启后前 10K 个请求都会 hit SSO，导致"thundering herd"（尽管 singleflight 可以缓解重复请求）。 | 添加异步预热：在启动时从 idempotency 表的最近活跃 uid 中加载 token 到缓存；或持久化 token 缓存到本地文件 |
| PERF-7 | **GORM 查询不做预编译（Prepared Statement）** | `internal/repository/*.go` | GORM 默认使用 prepared statement cache（`gorm:prepare_stmt`），但复杂查询可能需要多次编译。此外 GORM 的 `Find` 在每次查询前都反射解析 model 结构。 | 确认 GORM 的 `SkipDefaultTransaction` 已开启（避免不必要的 BEGIN/COMMIT）；确认 `PrepareStmt` 已开启 |
| PERF-8 | **通知插入批量未优化** | `internal/notification/dispatcher.go` — `Dispatch` | 当一条消息发送给 200 个频道成员时（群聊场景），dispatcher 会为每个成员单独 INSERT 一条通知。200 条 INSERT 在同一个事务中是好的，但如果 dispatcher 与 relay 在同一线程中，200 条 INSERT + SSE push 可能阻塞 outbox event pipeline。 | 使用批量 INSERT（一次 INSERT 多行）；将 SSE push 与 DB 写入解耦（使用带有缓冲 channel 的异步 pusher） |
| PERF-9 | **Metrics 注册表使用全局 map** | `internal/pkg/metrics/metrics.go` | 当前 metrics 使用 sync.Map + 按名称注册。在高并发下，每个请求的 RED 指标更新涉及两次 map 查找。这对于性能影响很小（ns 级），但如果有数千个唯一 route，map 竞争会随核心数增加而加剧。 | 使用预注册的固定指标集（启动时注册所有 route），避免运行时 map 查找 |

### 2.3 网络与 I/O 优化

| # | 问题 | 涉及文件 | 影响 | 建议 |
|---|------|---------|------|------|
| PERF-10 | **SSO gRPC 连接无连接池预热** | `internal/sso/client.go` | 服务启动后第一个 token 验证请求需要建立 gRPC 连接（TCP + TLS + HTTP/2 握手）。加上 keepalive 后连接会保持，但启动后的首个请求延迟增加 500ms-1s。 | warmup 阶段发送一个虚拟请求（`HealthCheck` 已在 `Run()` 中执行，但 health 接口可能不是最优的"暖流"）。使用 `grpc.Dial` + `Connect()` 在后台启动连接 |
| PERF-11 | **O Tel 追踪在高吞吐下的开销** | `internal/middleware/tracing.go` + `internal/pkg/tracing/tracing.go` | 每个请求都会创建一个 span（即使 tracing 禁用时是 no-op span，仍有 `otel.Tracer()` 查找开销）。当 tracing 启用时，属性记录和传播会增加请求延迟（约 5-10μs/span）。 | 验证 no-op tracer 的零成本保证；在高吞吐路由（如 health check）上使用 sampler 跳过采样；考虑对 `/metrics` 和 `/healthz` 路由不做 tracing |
| PERF-12 | **JSON 序列化在 response path 中重复** | `internal/pkg/httpx/httpx.go` + 各 handler | 每个 handler 调用 `c.JSON()`，Gin 使用 `encoding/json` 进行序列化。对于大型列表（如搜索返回 20 条结果），序列化时间占响应时间的 >20%。 | 考虑使用 `jsoniter`（已兼容 `encoding/json` 接口）或 `sonic`（更快但需要 C 编译器）；对列表响应使用 `c.JSON()` 而非 `c.Render()` |

### 2.4 后台任务优化

| # | 问题 | 涉及文件 | 影响 | 建议 |
|---|------|---------|------|------|
| PERF-13 | **GC sweeper 使用 background context** | `internal/storage/gc/sweeper.go` — `Start()` 使用 `context.Background()` | Background context 无法传播追踪信息，也无法被 shutdown 信号取消。如果 shutdown 发生在 sweep 中途，sweeper 仍然继续运行直到当前 pass 完成（可能数分钟）。 | 在 `App.shutdown()` 中传递带超时的 context 给 sweeper 的 stop 方法；在 sweeper 的 `Run()` 中检查 context cancellation |
| PERF-14 | **Outbox relay 的 ticker 频率固定** | `internal/outbox/relay.go` — `Start(pollInterval)` | Relay 以固定间隔轮询，无论是否有待处理事件。低负载时浪费 DB 查询；高负载时轮询间隔可能成为吞吐瓶颈。 | 实现 adaptive polling：当 `FetchUnpublished` 返回满批时减少等待时间（backoff to min），当返回 0 时退回到 max 间隔 |
| PERF-15 | **Idempotency cleaner 每次全表扫描** | `internal/idempotency/store.go` — `purgeExpired()` | 每小时扫描整个 `idempotency_keys` 表删除过期行。如果该表有数亿行（即使大部分已过期），DELETE 会触发大量的 vacuum 和 WAL 日志。 | 使用分区表按创建时间分区；或使用 `pg_cron` 调度更频繁的小批量删除；或改为在 INSERT/Complete 时同步清理过期的 key |

---

## 三、系统性优化方向（5 个）

与 `2026-07-10-architecture-extensions.md` 的产品功能方向不同，以下方向聚焦于**系统健壮性、可观测性和工程效率**。

---

### 方向 A：分布式限流与全局配额管理

**现状：** 当前限流是 per-instance 的（内存 token bucket）。如果 aero-id 部署多个副本，每个副本有独立的 20rps 限制 → 用户可以有效地以 `20rps × N 个副本` 的速率发起请求。这不是真正的限流。

**为什么需要：**

1. **多副本部署下限流失效。** 当前即使 `rate_limit.enabled=true`，只要有两个副本，限流效果就减半。生产环境通常至少有 2 个副本。这意味着限流提供的保护是**不可靠的**。
2. **无法实施全局性限制。** 如"每个用户每小时最多创建 10 个文件"、"每个 IP 每天最多上传 100MB"等策略需要全局状态。
3. **无 API 配额管理。** 不同于按请求率限流，API 配额（每月 10,000 次调用）需要持久存储和不同的时间窗口。

**架构影响：**

- 新增 `global_ratelimit` 包，使用 Redis（或 PostgreSQL advisory lock）作为共享状态
- 两种模式：
  - **严格模式**：每次请求检查 Redis（增加 1-2ms 延迟），精确限流
  - **概率模式**：本地 token bucket + 定期与 Redis 同步（最终一致性，低延迟）
- 新增配额概念：`resource_quotas` 表 `(uid, quota_type, used, limit, window_start, window_end)`
- 对文件上传、消息发送等资源消耗型操作增加配额检查钩子
- 配置项：`rate_limit.mode: local|redis`，`rate_limit.redis.endpoint`

**估算：3-4 周**

---

### 方向 B：多数据中心/地域部署与数据本地化

**现状：** 所有数据存储在单一 PostgreSQL 实例中。没有跨地域复制、没有读副本、没有数据本地化支持。

**为什么需要：**

1. **GDPR 数据本地化要求。** 如果 aero-id 服务欧洲用户，数据必须存储在欧盟境内。当前单一数据库无法实现地域级的数据隔离。
2. **读扩展性。** 当前读写都指向同一个数据库。随着用户增长数据库连接数会达到上限（PostgreSQL 通常建议 max_connections ≤ 500）。如果使用连接池（PgBouncer），查询延迟会增加。
3. **灾难恢复。** 没有跨地域冗余，一个 AZ 故障即导致完全不可用。

**架构影响：**

- 引入 **read replica** 支持：需要区分哪些查询可以走只读副本（列表、搜索）和哪些必须走主库（写入、cursor 分页中的一致性读取）
- 引入 **数据分片（Sharding）** 概念：按 `uid % N` 或地域将数据分布到不同的 PostgreSQL 实例
- 新增 `database.read_replica` 配置：`host:port` 列表，查询路由
- 服务层需要 `ReplicaSelector`：根据查询类型（RO/RW）选择连接
- 对 outbox relay 的影响：outbox 必须写入主库，relay 从主库读取
- 对 SSO 的影响：token 验证仍然是跨地域的（SSO 是中心化服务）

**简化方案：** 不实现应用层分片，而是使用 PostgreSQL 的**逻辑复制** + 应用层读写分离。这是最小投入方案。

**估算：4-6 周（完整分片），2-3 周（仅读写分离）**

---

### 方向 C：Webhook/事件订阅平台（Event Platform）

**现状：** Outbox 支持一个 webhook 端点（`outbox.webhook.url`），事件以 HMAC 签名推送到单一 URL。整个系统只有一个事件消费者。

**为什么需要：**

1. **外部系统集成是扩展性的核心。** 当前 webhook 是"推送所有事件到一个 URL"的全有或全无模式。第三方开发者无法注册只接收特定类型事件（如 `file.uploaded`）的 webhook。
2. **事件重放与死信管理缺失。** 如果 webhook endpoint 持续返回 500，事件被重试 20 次后标记为死信，但没有死信队列查看、重新投递、或导出功能。
3. **现有 outbox 基础设施可复用。** 事件模型（`aggregate_type`、`event_type`、`payload`）已经定义了。需要的是多订阅者 + 过滤器。

**架构影响：**

- 新增 `webhook_subscriptions` 表：`(id, owner_uid, url, event_types[], signing_key, enabled, created_at)`
- 新增 `subscription_delivery` 表：记录每次投递的结果（状态码、延迟、错误消息）
- 改造 `outbox/handler.go`：从单链（chain）改为扇出（fan-out）到所有匹配的 subscription
- 新增端点：
  - `POST /v1/webhooks` — 创建订阅
  - `GET /v1/webhooks` — 列出我的订阅
  - `DELETE /v1/webhooks/:id` — 删除订阅
  - `GET /v1/webhooks/:id/deliveries` — 查看投递历史
  - `POST /v1/webhooks/:id/replay` — 重新投递最近的失败事件
- 新增 `event_types` 枚举扩展：当前只有 notification 相关事件，增加 `file.*`、`message.*`、`comment.*`、`moderation.*` 系列

**关键设计决策：**

- **扇出同步 vs. 异步**：扇出在主 relay 循环中是同步的（一个事件 → N 个 webhook）。当 N 很大时（100+ 订阅者），relay cycle 变慢。解决方案：使用 `worker pool` 并行投递，超时独立。
- **投递担保**：每个订阅者有独立的 `max_retries` 和 `retry_backoff`。一个订阅者的失败不应影响其他订阅者。
- **事件格式**：标准化为 [CloudEvents](https://cloudevents.io/) 格式，使外部系统有统一的消费体验。
- **市场潜力**：webhook 平台是"aero-id 成为平台"的关键一步。第三方开发者可以构建自己的服务来响应文件上传、消息发送等事件。

**估算：4-5 周**

---

### 方向 D：Schema 版本化与数据迁移编排

**现状：** 迁移是顺序编号的 SQL 文件（000001 到 000022），通过 `cmd/migrate` CLI 或 `auto_migrate` 启动时执行。迁移不可逆？—— 有 `.down.sql`，但生产环境从未执行过 down。

**为什么需要：**

1. **零停机迁移。** 当前迁移在应用启动前执行（或通过独立 CLI 提前运行）。对于生产环境，可能需要：
   - 添加列（ALTER TABLE ADD COLUMN）— 安全
   - 重命名列 — 需要新旧列共存两个版本
   - 分割表 — 需要新旧表同步
   目前没有框架来处理这些复杂场景。
2. **多版本兼容性。** 如果新代码需要 `message_reactions` 表（注意：000013 已添加），但回滚到旧代码会因为表不存在而崩溃。迁移框架不记录"哪个代码版本需要哪个迁移"。
3. **数据迁移 vs.  schema 迁移。** 当前迁移只处理 schema。需要数据迁移（如"将所有 `MessageKindText` 从 1 改为 'text'"）时，必须手动写 SQL 或 Go 脚本。没有版本化的数据迁移支持。

**架构影响：**

- 引入 **migration 版本与 app 版本绑定**：`migrations/app_version.go` 记录每个迁移与 app 版本的对应关系
- 新增 **expand/migrate/contract** 三阶段模式：
  - **Expand**（v1 → v2 过渡期）：旧代码运行；新列添加（允许 NULL/默认值）
  - **Migrate**：后台 backfill 新旧数据格式
  - **Contract**：所有实例都升级到 v2 后，删除旧列/旧表
- 新增 `migrate status --verbose` 命令：显示每个迁移的状态、执行时间、对应的 app 版本
- 新增 `migrate data` 子命令：版本化的数据迁移脚本（Go 模板 + SQL）
- 新增**迁移回滚测试**：CI 中运行 `migrate up && migrate down N && migrate up` 验证双向

**估算：3-4 周**

---

### 方向 E：负载测试套件与 SLO 框架

**现状：** 项目有单元测试和集成测试，但没有一个负载测试脚本。性能指标（P99 延迟、吞吐量、错误率）没有任何基准线。`ROADMAP.md` 中提到了"负载测试套件"但标记为 P2-3 后 来交付——实际上未实现。

**为什么需要：**

1. **没有性能基准，无法检测回归。** 每次代码变更后，性能可能下降而不被察觉。例如，添加一个 JOIN 到查询中，不恰当的索引可能导致 P99 延迟从 10ms 飙升到 500ms——单元测试覆盖不到。
2. **限流和背压的验证。** 当前限流配置（20rps）是否合理？在没有负载测试的情况下，这是猜测。如果误配置了限流为 200rps，aero-id 可能在 100 个并发用户下就承受不住。
3. **容量规划。** "生产环境需要几个副本？"——当前无法回答这个问题。负载测试提供数据驱动的容量规划。
4. **SLO 定义与监控。** 没有性能基准就无法定义 SLO（服务等级目标）。"99% 的请求在 500ms 内返回"——没有压力测试就无法验证这个 SLO 是否可达。

**架构影响：**

- 引入 k6 脚本（位于 `scripts/loadtest/`）：
  - `smoke.js` — 低并发（1-5 用户）验证端到端
  - `stress.js` — 逐步增加并发到目标值
  - `soak.js` — 长时间（1h+）保持稳定负载
  - `spike.js` — 突发峰值（如 10x 正常负载）
- 新增 `test/loadtest/` 目录：
  - `docker-compose.loadtest.yml` — 轻量级测试环境（PostgreSQL + MinIO + SSO mock）
  - `Makefile` 目标：`make loadtest-smoke`、`make loadtest-stress`
- 新增 SLO 定义文档：`docs/SLO.md`
- 新增 CI 步骤：在 PR 上运行 smoke test，在 main 上运行 stress test
- 关键的 SLO 指标：
  - `/v1/files` POST P99 < 2s（包含上传时间，取决于文件大小）
  - `/v1/messages` GET P99 < 200ms
  - `/v1/search` GET P99 < 500ms
  - `/v1/notifications` GET P99 < 100ms
  - SSO 验证 P99 < 300ms（包括网络往返）
  - 限流条件下错误率 < 1%（429 不计入）

**估算：2-3 周**

---

## 四、执行优先级建议

将本文的 5 个方向与 `2026-07-10-architecture-extensions.md` 的 5 个方向放在一起，建议的全局优先级：

```
Phase 0（立即可做，1 周内）
  ├── EC-1 ~ EC-18 中的关键 edge case 修复：
  │   └── EC-4 (outbox 重复投递) & EC-10 (SSO 缓存 OOM) & EC-15 (搜索权限泄露)
  ├── PERF-6 (SSO 缓存预热)
  └── PERF-14 (outbox adaptive polling)

Phase 1（短期，2-3 周）
  ├── 方向 A（分布式限流）— 多副本部署的安全基线
  ├── 方向 E（负载测试 + SLO）— 所有后续方向的可衡量性依赖
  └── 方向 2（统一活动流，来自 companion doc）— 低风险高价值

Phase 2（中期，4-6 周）
  ├── 方向 C（事件订阅平台）— 平台化核心
  ├── 方向 D（Schema 版本化 + 数据迁移）— 零停机部署的前提
  └── 方向 1（群聊与频道，来自 companion doc）— 产品升级

Phase 3（中长期，6-8 周）
  ├── 方向 B（多地域部署）— 扩展性约束
  ├── 方向 4（协作审核工作台，来自 companion doc）
  └── 方向 5（文件加工管线，来自 companion doc）
```

**Phase 0 的 edge case 修复应作为代码审查的一部分立即启动**——它们修正现有的生产隐患，不需要架构变更。

---

## 附录 A：未被覆盖的深层风险

| 风险 | 描述 | 严重性 |
|------|------|--------|
| **Go 1.26 兼容性** | AGENTS.md 指定 Go 1.26，但 `go.mod` 中可能包含仅在旧版本中测试过的依赖。每次 Go 发布新的 minor 版本时，标准库的微小行为变化（如 `net/http` 的 `Timeout` 处理、`json.Unmarshal` 的 `nil` 行为）可能引入回归。 | 低 |
| **GORM 版本锁定** | GORM v2 有多个 patch 版本。当前使用的版本可能有未修复的 bug（如带 deleted_at 的 JOIN 查询行为）。应锁定 go.sum 并定期更新。 | 低 |
| **SSO 服务发现** | 当前 SSO endpoint 是静态配置的（`sso.endpoint: 127.0.0.1:9000`）。在 Kubernetes 中，如果 SSO 服务重启并更换了 IP，静态配置的连接会中断。虽然 gRPC keepalive 最终会重建，但中间会有分钟级的不可用窗口。 | 中 |
| **审计日志不可导出** | `audit_events` 表只能通过 SQL 查询访问。没有 API 端点支持导出审计日志用于合规审计。GDPR 的数据可移植性请求（DSR）需要工程支持。 | 中 |

---

## 附录 B：Edge Case 修复清单（Phase 0 推荐）

| ID | 修复项 | 文件 | 复杂度 | 风险降低 |
|----|--------|------|--------|---------|
| EC-4 | 通知表 UPSERT 防重复 | `internal/notification/dispatcher.go` | 低 | 高 |
| EC-10 | 缓存大小上限 + LRU | `internal/sso/token_cache.go` | 中 | 高 |
| EC-11 | 搜索查询 statement_timeout | `internal/repository/*_search.go` | 低 | 中 |
| EC-6 | GC orphan sweep 默认启用 | `deployments/docker-compose.yml` | 低 | 中 |
| EC-15 | 搜索关联 file_shares | `internal/repository/file.go` | 中 | 高 |
| EC-16 | 搜索排除 blocked 用户 | `internal/repository/*_search.go` | 低 | 中 |
| EC-18 | 文件操作审计日志 | `internal/file/service_*.go` | 低 | 中 |
| PERF-6 | SSO 缓存预热 | `internal/sso/token_cache.go` + `app.go` | 低 | 中 |
