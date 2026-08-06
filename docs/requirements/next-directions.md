# aero-id — 下一阶段高价值扩展方向

> 基于 2026-07-11 全代码库扫描的结果。MVP 已全面交付（38 gRPC RPC、双传输完全对等、事务 outbox、SSE 实时推送、全文搜索、管理角色/审计、存储 GC、SSO 弹性、可观测性），此文档跳过已实现的路线图方向，专注于下一批高增量方向的识别。**不要求编写任何代码。**

---

## 当前状态摘要

工程约束（500 行/文件、50 行/函数、复杂度 ≤10）全部遵守。单元测试 27 个包通过，集成测试（testcontainers）21 个包通过。以下是当前系统能力的快速排查：

| 领域 | 状态 |
|---|---|
| 文件（CRUD、分享、预签名上传、去重、GC） | ✅ 已完成 |
| 消息（1:1 私信、反应、编辑/删除、已读回执） | ✅ 已完成 |
| 评论（两层嵌套、点赞、举报/审核、编辑/删除） | ✅ 已完成 |
| 通知（outbox 驱动、SSE 实时推送、折叠、偏好、屏蔽感知、频率限制） | ✅ 已完成 |
| 全文搜索（tsvector + GIN） | ✅ 已完成 |
| 管理角色（moderator/admin + 权限） | ✅ 已完成 |
| 审计日志 + 查询 | ✅ 已完成 |
| SSO 弹性（熔断、重试、stale-cache、singleflight、TLS/mTLS） | ✅ 已完成 |
| 可观测性（RED 指标、OTel 追踪、outbox/SSO 健康仪表盘） | ✅ 已完成 |
| 速率限制（in-memory per-actor） | ✅ 已完成 |
| 幂等性键 | ✅ 已完成 |
| HTTP ↔ gRPC 完全对等 | ✅ 已完成 |
| 存储 GC（软删除清理 + 孤儿 blob 扫描） | ✅ 已完成 |

---

## 推荐方向（前 5）

### 方向 1：消息扩展——群组会话 + 多参与者

**优先级：P1（高商业价值）**

**为什么需要。** 当前消息模型严格为 1:1（`messages.from_uid` → `messages.to_uid`）。没有群聊，没有会话元数据（主题、头像、参与者列表），也没有加入/离开事件。对于任何面向社区或团队的产品，群组消息是几乎普遍需求——缺少它会将 aero-id 限制为仅通知和 1:1 功能。

**现有基础是什么。** `messages.kind` 枚举已预留 `system` 类型，outbox 可生成 `message.sent` 事件，通知系统可向群组中的 N 个收件人广播。`Message` 实体已有 `Meta map[string]any`，因此迁移期间可用它携带临时群聊元数据，而无需立即更改架构。参与者列表的 `user_roles` 模式提供了一个可重用的关联模式。

**需要什么。**
- 引入 `conversations` 域实体 + 表（参与者列表、类型 `pair|group`、可选主题/头像）。
- `conversation_participants` 连接表带有 `joined_at` / `left_at`，以便查询活跃成员。
- 向 `messages` 添加 `conversation_id` 外键（可为空以向后兼容现有的 1:1 消息；迁移应回填 1:1 会话）。
- 服务层：`CreateGroupConversation`、`AddParticipant`、`RemoveParticipant`、`ListConversations`（涵盖的会话 + 未读计数）。
- 每个群组的通知：outbox 事件扇出至 N 个参与者（现有调度器模式；`comment.replied` 单个收件人 → 群组为 N 个收件人）。
- 在 HTTP 和 gRPC 中添加 `GET /v1/conversations`、`POST /v1/conversations`、`POST /v1/conversations/:id/participants`。

**边界情况。**
- 参与者离开后仍可访问旧消息（保留历史记录；离开仅控制新消息可见性）。
- 删除群组会话：软删除 + 为参与者清理通知。
- 屏蔽：如果 A 屏蔽了 B，则 A 不应收到 B 在共享群组中发送的通知，但消息本身如何处理？设计决策：群组消息可以是"可见但静音"，也可以是"完全隐藏"。
- 原子性：`SendMessage` + 事件扇出至 N 必须要么全部提交，要么都不提交。事务 outbox 可以处理，但扇出循环必须位于收件人计数的事务中。

**影响 / 工作量。** 高产品影响，中等工作量。表结构紧凑；参与者管理 via 事务；通知扇出重用现有的 `NotificationDispatcher`，只需为 N 个收件人循环。

---

### 方向 2：推送通知通道（移动端 + Webhook 统一）

**优先级：P1（高运营价值）**

**为什么需要。** 今天的通知仅通过 SSE 传递（需要一个实时 HTTP 连接）。断开连接的用户永远不会收到通知，除非他们轮询 `GET /v1/notifications`（这是一个拉取操作，无用户可见提示）。移动应用无法连接 SSE；它们需要 FCM（Firebase Cloud Messaging）/ APNs。即使对于 Web，依赖 SSE 意味着用户必须保持标签页打开。

现有的 outbox → 通知调度器管道很健全（速率限制、屏蔽、偏好、折叠、webhook 传递）。缺少的部分是**设备注册**和**到 FCM/APNs 的桥接**。

**现有基础是什么。**
- `NotificationDispatcher`（`internal/service/notification.go`）已在 outbox 事件到达时写入通知行。
- `hub.Pusher` 接口（`internal/hub/adapter.go`）用于 SSE 实时推送。
- 出站 webhook（`internal/outbox/webhook.go`）支持 HMAC 签名交付，但面向外部服务器，而非端用户设备。
- `NotificationPreference`（`internal/domain/notification.go`）有 `QuietHoursStart`/`End`，移动推送可尊重这些设置。
- 度量标准已跟踪发布/失败的出站事件，因此可监控推送投递。

**需要什么。**
- `device_registrations` 表（`uid`, `platform` `text`（"apns"|"fcm"），`device_token` `text`，`created_at`，`updated_at`）——`UNIQUE(uid, platform, device_token)`。
- HTTP 端点 `POST /v1/devices`（注册/更新令牌）和 `DELETE /v1/devices/:token`（注销）。
- 一个可插拔的 `PushProvider` 接口（`Send(ctx, deviceToken, title, body, data map[string]string) error`），并为 FCM 和 APNs 各有一个实现。
- 与 `NotificationDispatcher` 集成：通知行写入后，在同一个处理器中推送。或者，更好：推送是一个独立的出站 handler，通过 `outbox.Chain` 链式调用，因此推送失败不会影响通知持久化。
- `notification_preferences` 中添加 `push_enabled` 列，以便用户可在不关闭应用内通知的情况下静音推送。

**边界情况。**
- 令牌轮换：APNs/FCM 令牌会过期；`POST /v1/devices` 必须幂等地更新现有令牌。
- 静默失败：推送投递是尽力而为；移动平台不保证送达。监控：计数器跟踪发送与确认。
- 速率限制：FCM/APNs 有每个设备/每个应用的速率限制。调度器层 `notifRateLimiter` 可扩展至包含推送。
- 用户删除：注销级联时 soft-delete 所有设备令牌。

**影响 / 工作量。** 高产品影响（移动端必备），中等工作量。核心基础设施（调度器、偏好、outbox、度量标准）已就位；增量是设备注册 + 与 FCM/APNs 的集成。

---

### 方向 3：资源配额与使用治理

**优先级：P1（高运营价值）**

**为什么需要。** 目前唯一强制执行的限制是每个文件的最大上传字节数（`storage.max_upload_bytes`，可通过配置设置）。对于多租户生产服务，缺乏资源治理意味着：
- 一个用户可通过发送数百万条消息或评论来耗尽数据库存储。
- 一个用户可消耗所有可用的 S3 存储，导致其他用户无法上传文件。
- 没有分层的公平性（租户/项目/用户级别）。
- 没有可见性来诊断谁在消耗什么。
- 当前速率限制（RPS）是全局的，而不是每个端点、每个用户的——它不能防止消息/评论/文件创建速率的突发。

**现有基础是什么。**
- `repository` 包已具有 `CountByStorageKey` 和其他聚合查询。`CountUnread`、`ListByOwner` 等方法显示新模式很容易添加。
- `config.yaml` 有一个 `max_upload_bytes` 字段——配额模式可以并排存在。
- （预期的）每个用户的存储使用聚合：`SELECT SUM(size) FROM files WHERE owner_uid = ? AND deleted_at IS NULL`。
- 速率限制器（`internal/pkg/ratelimit`）提供每个键的令牌桶；它可以扩展为按用户和按端点的限制。
- audit 事件（`audit_events`）记录敏感操作，可查询以进行计费/审计。

**需要什么。**
- `user_quotas` 表（`uid text PRIMARY KEY`，`max_storage_bytes bigint`，`max_messages_per_day int`，`max_comments_per_day int`，……）。默认值来自全局配置；可覆盖每个用户。
- 服务层 `QuotaService`：`CheckQuota(ctx, uid, resource string) (bool, error)`。由文件/消息/评论服务在创建路径中调用。
- 文件上传后的存储使用增量检查（在 `fileService.Create` 中）。
- 消息/评论的每日计数器（基于简单的 Postgres 计数 `WHERE owner_uid = ? AND created_at > now() - interval '1 day'`，或为高频写入使用 Redis 计数器）。
- 新的 HTTP/gRPC 端点：`GET /v1/quotas`（我的配额），`GET /v1/admin/quotas?uid=`（管理员的全局视图），`PUT /v1/admin/quotas/:uid`（覆盖）。
- 通过 `pkg/errors` 提供友好的 `QuotaExceeded` 错误代码（映射到 HTTP 429 / gRPC `ResourceExhausted`）。
- 度量标准：`aeroid_quota_denials_total{resource="..."}` 计数器，以检测滥用模式。

**边界情况。**
- 每日计数器的原子性：Postgres `INSERT ... ON CONFLICT DO UPDATE` 对于低容量而言足够；Redis 对于高容量而言更好。
- 软删除：为了配额计费，文件是否算作已删除？可能"硬删除"释放存储，"软删除"不计入存储配额但保持计数（去重检查仍需要它）。
- 抢先检查与最终检查：`CheckQuota` 在写入之前提供尽力而为的防护；事务中的最终检查可防止竞态。
- 大型覆盖集：为 `max_storage_bytes = 0`（"无限"）或 `-1`（"阻止"）引入哨兵值。

**影响 / 工作量。** 高运营价值，中等工作量。核心模式简单；集成点仅位于服务创建路径中。

---

### 方向 4：数据可移植性——导出/删除工作流（GDPR）

**优先级：P2（合规/信任）**

**为什么需要。** aero-id 存储每个用户的所有消息、评论、文件元数据、通知和偏好。对于生产部署，尤其是在欧洲运营的部署，必须遵守 GDPR 的"被遗忘权"（第 17 条）和数据可移植性（第 20 条）。即使不受 GDPR 约束，为用户提供导出和删除其数据的按钮也是良好的信任建立行为。

今天，没有端到端的"删除我的帐户"工作流——每段数据（消息、评论、文件、通知、块）都必须单独删除。文件 blob 可能在某些行已删除后仍作为孤立对象存在（GC 最终会处理，但时间不确定）。用户想要其数据的副本时，没有导出格式。

**现有基础是什么。**
- 所有实体使用 GORM 的软删除（`DeletedAt`），因此"删除用户的所有内容"可以是一个事务性更新，将 `deleted_at` 设置为现在。
- `fileService.Delete` 已经处理 blob 删除（带引用计数）。
- 存储 GC 清理孤立 blob（但延迟可变，可达 `retention`，通常为 7 天）。
- `outbox` 和通知行引用用户 ID，可通过级联或批量更新清理。
- 审计日志包含 `actor_uid`/`target_uid`，可从中移除或匿名化 PII。
- 搜索服务已经可以跨实体搜索，可用于构建导出包。

**需要什么。**
- **导出服务：** `POST /v1/my/data/exports` 创建异步导出任务。任务收集：拥有的文件列表（元数据，而非 blob），消息（发送+接收），评论，通知，偏好，块。输出：一个压缩归档（JSON Lines 或 CSV）。大型数据集通过 outbox 发布 `export.ready` 事件并带有下载链接。
- **删除服务：** `DELETE /v1/my/data/account`（或 `POST /v1/my/data/erase`）启动异步级联删除：
  1. 标记所有消息为已发送且已接收（`from_uid = ? OR to_uid = ?`）→ `deleted_at = now()`。
  2. 标记所有评论为 `status = deleted`。
  3. 软删除文件（让 GC 处理 blob）。
  4. 删除通知、块、偏好、设备令牌、幂等性键。
  5. 匿名化审计日志：用哈希或 `"deleted-user"` 替换 `actor_uid`/`target_uid`。
  6. 使 SSO 令牌缓存条目失效。
- 步骤 3-6 的幂等性：PowerShell `DELETE /v1/my/data/account` 是幂等的。
- 导出/删除进度的管理视图：`GET /v1/admin/data-tasks`。

**边界情况。**
- 部分取消：如果用户在导出期间发送新消息，导出应使用时间点快照或包含新数据。时间点更容易：`WHERE created_at <= snapshot_time`。
- 重新注册：如果用户稍后用同一个 SSO uid 重新注册，则不应继承旧数据。
- 匿名化的审计完整性：替换 `actor_uid` 而不是删除行，以保留"谁在何时做了什么"审计线索。
- 大型帐户：具有 10 万条消息的导出必须流式传输，而非在内存中缓冲。
- 级联取消引用：`file_shares`、`comment_likes`、`message_reactions` 行必须由所有者的删除级联处理。

**影响 / 工作量。** 中等产品影响（信任/合规），中等工作量。大部分模式已存在；复杂性在于编排导出/删除工作流（状态机、通知）以及正确处理边界情况。

---

### 方向 5：分布式速率限制与多实例协调

**优先级：P2（可扩展性基础）**

**为什么需要。** 当前速率限制器（`internal/pkg/ratelimit`）是**进程内、仅内存**的。对于水平扩展的部署，每个 pod 维护自己的隔离令牌桶。通过将 N 个 pod 放置在负载均衡器后面：
- 每个用户的全局 RPS 限制膨胀了 N 倍（一个用户可以向每个 pod 发送 20 RPS，总共 20N）。
- 对端点滥用的防护失效。
- 幂等性键存储（`idempotency_keys`）在 Postgres 中，因此跨实例是正确的——但速率限制缺失。
- 出站 relay 轮询 Postgres，因此其行为是全局正确的——但 relay 本身是单 pod（如果运行多个 relay，它们会竞争事件）。

对于生产扩展，aero-id 需要**共享速率限制状态**。好消息：代码库设计良好，因此集成点很少。

**现有基础是什么。**
- `ratelimit.Limiter` 有一个干净的接口（`Allow(key string) bool`，`AllowN(key string, n int) bool`，`RunCleaner`）。
- 当前实现（`token_bucket.go`）基于 `sync.Map`——一个 `DistributedLimiter` 可以实现相同的接口。
- HTTP 和 gRPC 层通过 `WithRateLimit` 选项注入限制器——因此替换驻留是局限性的。
- 幂等性密钥存储已使用 Postgres（`idempotency.NewGormStore`）；同样的模式可用于全局限制器。
- 配置（`config.yaml` 中的 `rate_limit`）已控制启用/禁用、RPS、突发、空闲 TTL。

**需要什么。**
- **选项 A（轻量级）：基于 Postgres 的全局限制器。** 使用 `INSERT ... ON CONFLICT DO UPDATE` 的 `rate_limit_buckets` 表 + 原子更新 + 后台清理器。优点：无新基础设施，原子语义。缺点：每个允许检查的延迟更高（5-10 毫秒），数据库负载。
- **选项 B（生产级）：基于 Redis 的限制器。** 带有可选 Redis 后端的 `DistributedLimiter`。使用 Redis `INCR` + `EXPIRE`（滑动窗口算法，或用于固定窗口的 `SETEX`）。优点：~1ms 延迟，与 Postgres 解耦。缺点：新的基础设施依赖。
- 两种选项都实现 `ratelimit.Limiter` 接口，因此决策可推迟。
- 配置标志：`rate_limit.backend: "local" | "postgres" | "redis"`。
- 度量标准：`aeroid_rate_limit_decisions_total{decision="allow"|"deny",backend="..."}`。
- 出站 relay 协调：如果多 pod 运行 relay，它们会竞争事件（`FetchUnpublished` + `MarkPublished` 是原子性的，但 relay 会做重复工作）。在 relay 之间添加 leader 选举（通过 Postgres 咨询锁或 `leader_elections` 表）使单个 relay 成为活跃的 relay；其他备用。

**边界情况。**
- Redis 故障切换：当 Redis 宕机时怎么办？备选方案：回退到本地限制器（宽松限制）或拒绝所有（保守）。可配置。
- 同步精度：Redis `INCR` + `EXPIRE` 在故障切换时可能将计数翻倍（Redis 的`INCR` 不是分布式的）；可在可接受的误差范围内容忍（率限制是尽力而为的，而非硬性边界）。
- 大型空闲时间范围：清理器必须在分布式后端上运行（Redis `EXPIRE` 自动过期键；Postgres 需要像现有 `RunCleaner` 这样的后台清理器）。
- 迁移：本地限制器 → 共享限制器不需要数据迁移——桶不是持久的。

**影响 / 工作量。** 中等产品影响（启用扩展），中等工作量。接口已存在；实现是一个相当独立的模块。

---

## 执行策略

与原始路线图一样，这些方向的排序考虑了**依赖关系**。关键见解是方向 3（配额）可从方向 5（分布式限制器）中受益，但不需要它，反之亦然。方向 1（群组聊天）和方向 2（推送）是独立的，可并行进行。

| # | 方向 | 优先级 | 工作量 | 依赖关系 |
|---|---|---|---|---|
| 1 | 群组聊天 + 多参与者 | P1 | 中等 | 无 |
| 2 | 移动推送通知 | P1 | 中等 | 无（使用现有的调度器/出站链） |
| 3 | 资源配额与治理 | P1 | 中等 | 无（速率限制器是一个并行总线） |
| 4 | 数据可移植性（GDPR） | P2 | 中等 | 无（但建议在方向 3 之后，以便配额限制导出大小） |
| 5 | 分布式速率限制 | P2 | 中等 | 无（独立的；方向 4 导出可能希望速率限制） |

### 建议排序

1. **方向 2（推送通知）**——产品影响最大的独立增量。非移动应用用户可以继续使用 SSE；移动应用获得所需功能。工作量适中且为独立。
2. **方向 3（配额）**——生产问责制的基础。可防止一个租户影响其他租户，并为核心数据提供可见性。
3. **方向 1（群组聊天）**——最大的产品差异点。扩展消息模型以支持团队/群组通信。
4. **方向 5（分布式限制器）**——在需要扩展超过 1 个 pod 之前为可选。用作方向 3 的增强。
5. **方向 4（GDPR 导出/删除）**——合规/信任特性。可推迟到启动后，但建议在第一个生产用户之前做好设计。

---

## 附录：被评估但被拒绝的方向（在此阶段）

| 候选方向 | 拒绝原因 |
|---|---|
| 文件版本控制 | 架构上很重，需要存储后端中的多版本对象存储（即 S3 版本控制）或每个文件的多行。对于当前产品阶段而言价值可疑。 |
| 实时协作（文档编辑） | 范围完全不同（OT/CRDT）；不是数据存储服务。 |
| 第三方 OAuth 作为 SSO 供应商 | aero-id 委派身份给 SSO；添加其他身份提供商属于 SSO 的范畴，而非 aero-id。SSO 客户端是通用的（gRPC），但这是外部职责。 |
| 消息/文件的端到端加密 | 产品上可取，但在存在内容去重、全文搜索和审核的情况下，加密设计很困难。最好在当前架构之上作为专用层处理。 |
| WebSocket 替代 SSE | SSE 已实现并投入生产（支持 last_event_id 重放）。WebSocket 将增加服务器端复杂性（每连接 goroutine 与 SSE 的 goroutine-per-flush 对比）。坚持使用 SSE 直到出现具体的双向需求。 |

---

*扫描日期：2026-07-11 · 无任何代码更改。*
