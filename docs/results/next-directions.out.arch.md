现在我已经对代码库有了透彻的理解，我对你提供的分析文档进行了详细的架构评估。

---

# 架构分析：aero-id 扩展方向

## 1. 架构评估

### 当前架构优势

**分层纯度优秀。** 代码库强制执行严格的单向依赖（handler → service → repository），且没有违反。与众多"泄漏的"Go 项目不同，`internal/handler` 仅处理传输 I/O——零业务逻辑。服务层是所有逻辑的唯一真实来源，两个传输协议（HTTP 和 gRPC）行为一致。

**事务性发件箱是实现基石。** `outbox` 包的实现具有完备的指数退避、死信策略、重试计数和 MetricsSink 接口——这些特性在同类项目中经常被省略。`Relay.drain()` 中退避跳跃的处理（`now.Before(next)`）表明作者考虑过竞争条件。链式 handler 模式（`dispatcher → webhookHandler`）在 `app.go` 中注册的方式清晰地将连接职责与业务逻辑分离。

**配置是第一性的。** viper 具有彻底的 env 覆盖、启动时验证（`config.validate()`）以及为零值提供合理默认值——预示着运营方面的问题。`cleanup_every` 默认值处理（如果 <= 0 则回退到 10分钟）是一个很好的防御性编码实践。

**并发设计是审慎的。** per-uid 分片的 SSE Hub（`sync.Map[uid] → *uidSubs`）意味着发布给 Alice 永远不会阻塞 Bob——这是一个正确的设计决策，避免了在连接数很多的情况下出现全局锁争用。速率限制器使用 per-bucket 锁定（通过在父级 `Limiter` 的互斥锁内锁定一个 `bucket`），并与空闲驱逐配合使用。

### 架构债务（非阻塞性问题，但会累积）

1. **SSE Hub 对慢速消费者实施静默丢弃。** `hub.pub()` 中的 `select default` 分支意味着如果 SSE 客户端连接速度慢（例如，一个暂停了 30 秒的手机标签页），事件会被静默丢弃，且客户端无法察觉。`last_event_id` 重放机制（通过 `repository/notification.go:ListSince` 实现）缓解了这个问题，但代价是拉取延迟（下次 SSE 重连时）。对于方向 2（推送通知），这尤其危险——因为推送通知是幂等的（设备可能最终会收到多条通知），所以可以接受，但如果一个设备令牌反复使通道饱和，也应考虑一种反压机制。

2. **发件箱重试语义是纯粹的 at-least-once。** 没有幂等性保证。如果 `dispatcher.Handle` 成功创建了通知行但在 `MarkPublished` 之前崩溃，`Relay` 会重放该事件。dispatcher 中的重复检测（`WithDispatcherCollapse`）减轻了这一点，但它是时间窗口内的，而非去重。对于方向 2（推送通知），这意味着同一事件可能导致推送发送两次。对于群组聊天（方向 1），如果崩溃情况恰巧发生，可能会发送重复的消息通知。

3. **每个文件单一的职责——但正在接近极限。** `app.go` 约 300 行——对于组合根来说是可以接受的，但正在接近翻倍的风险。`app.New` 是纯构造逻辑，但列出了 13 个仓库构造、14 个带选项的服务构造、以及多个条件性基础设施（GC 清扫器、Webhook chain、速率限制器）。如果新增方向 1–5，组合根可能会膨胀到 500+ 行。按领域（`app/messages.go`、`app/notifications.go`、`app/files.go`）拆分 `app.go` 是一个值得考虑的预重构。

4. **在代码中嵌入审计日志记录**（在每个服务方法中作为 `if s.audit != nil { _ = s.audit.Log(...) }`）会导致样板代码和审计不完整。如果方向 4（GDPR）在每一处都需要审计日志，这种方法会成倍增加代码行数。基于装饰器或中间件的审计方案（包装服务层）会更简洁，但需要在 Go 中使用接口代理。

### 关键设计决策验证

| 决策 | 评估 | 对方向的影响 |
|---|---|---|
| 每个发件箱 event 使用 UUIDv7 主键 | ✅ 支持使用 `ORDER BY id` 进行基于时间的排序和去重 | 方向 1（群组）将受益于确定性的会话外 event 顺序 |
| `Chain.Handle` 出现第一个错误时立即停止 | ✅ 有记录且可预测；但方向 2 需要对其重新排序 | 推送 handler 必须放在 `Dispatcher` **之前**，这样如果推送失败，通知行已经创建完毕 |
| 速率限制器是 token bucket（非 leaky bucket） | ✅ 突发能力支持（`burst` 参数）对于短突发是必要的 | 方向 5（分布式限流器）应保留 token bucket 语义 |
| SSE Hub → 非阻塞发送 | ⚠️ 有意的权衡——不会阻塞 Publish，但可能会丢弃 | 方向 2 的推送 channel 是带缓冲的（buffer 16），但这在 FCM 客户端中可能不够；需要单独的 goroutine-per-subscriber 或背压 |
| 每个实体使用 `gorm.DeletedAt` 实现软删除 | ✅ 方向 4（GDPR）需要恢复能力 | 但在 "delete my data" 级联方面也带来了问题（保留 vs 删除通过某人发送给其他人的消息） |

---

## 2. 扩展方向

我评估了你的五个方向，并基于架构影响、技术风险和业务价值给出了定级。

### 优先级 P0（基础架构层——必须优先于领域扩展完成）

#### 方向 2：推送通知（P0）

**为什么需要：** SSE 只在客户端有活跃的 WebSocket/EventSource 连接时才能投递。移动应用、非活动标签页和断开连接的客户端会错过消息——而推送通知是移动端即时通讯的核心产品功能。

**核心挑战：**
1. **设备注册生命周期管理。** 推送令牌会过期（FCM 令牌大约每 6 个月轮换一次）、被用户撤销、或被 APNs 列入黑名单。你需要一个 `device_registrations` 表，其中的令牌有 TTL、有刷新逻辑、并且有从无响应令牌中优雅恢复的能力。
2. **发件箱 chain 顺序问题。** 当前的 `Chain` 实现是顺序且短路的。如果推送 handler 在 `Dispatcher` 之后运行，Dispatcher 失败会阻止推送；如果推送 handler 失败（例如 FCM 返回 503），它会阻止通知行被写入。**推荐的方案：** 将 chain 改为 fan-out（让所有 handler 都运行，收集错误），或者将推送 handler 放在 `Dispatcher` **之前**（如果推送失败，通知仍然会被记录下来）。或者，使用独立的 `Dispatcher` 和 `PushHandler`，通过两个独立的 relay 实例，由 event type 进行筛选。

```
方案 A：Chain fan-out（所有 handler 都运行，无论错误与否）
  → 更安全：通知行 + 推送尝试均记录
  → 需要从 Chain 改为 FanOut Handler

方案 B：推送跑在 Dispatcher 前面
  → 推送失败不会阻止行写入
  → 但更改了 Chain 的语义；现有的 Webhook handler 跟在 Dispatcher 后面

方案 C：两个独立的 relay 实例
  → 并行投递：互不影响
  → 操作上更复杂（两个 relay loop，两个配置的轮询间隔）
```

**推荐的方案：** 方案 C，但通过配置实现（不是硬编码的两个 relay，而是 `outbox.relays` 数组，每个 handler 链对应一个 relay）。这与当前 relay 架构的简洁性保持一致。

3. **速率限制和退避。** FCM/APNs 有严格的速率限制（FCM 每个项目每台设备 240 条/分钟）。如果群组消息（方向 1）一次性扇出到 1000 个成员，推送层需要 per-device 限速，并且针对相同的有效载荷支持 FCM 的 `sendAll`（批量发送）。

**预期的架构变更：**
- 新建：`internal/push/` 包（`push.Service`、`push.FCMClient`、`push.APNsClient`）
- 新建：`domain/device.go`（`DeviceRegistration`、`DevicePlatform` 枚举）
- 新建：`repository/device.go`
- 新建：`migrations/`：`device_registrations` 表
- 变更：`app.go` — 注册第二个 relay（或 fan-out chain）
- 变更：`config.go` — `PushConfig`（FCM 密钥、APNs 证书、限速设置）

**对现有系统的影响：** 低——纯粹的补充功能。现有的 `Notification` 行不受影响。

---

### 方向 3：资源配额与使用治理（P0）

**为什么需要：** 当前唯一的限制是上传文件大小（`max_upload_bytes`）。没有每个用户的存储配额、每日消息限制、或评论发帖速率限制，系统容易受到无限资源消耗的影响。一个用户可以上传 10 万个文件并耗尽磁盘空间。针对这些未加保护的操作进行限流，是生产化前的底线。

**核心挑战：**
1. **跨多个实体的配额状态一致性。** 存储额度需要聚合 `files.size` 所有未删除文件的 WHERE owner_uid 查询。这应该在写入时通过计数器表（`resource_usage`）进行维护，而不是在每次上传时扫描整个文件表——否则第 10000 次上传会因 SUM 扫描而变得昂贵。
2. **哨兵值语义。** 对于 `max_storage_bytes = 0` 当 "无限制" 与 "不允许任何存储" 之间的边界情况很难处理。你提了一个很好的方案：使用两个独立字段（`quota_override_enabled bool`、`quota_value`）或者使用反向逻辑（`-1 = infinite`、`-2 = blocked`）。我推荐使用指针方法（`*int64`），nil 表示 "使用默认值"。
3. **HTTP 429 与 gRPC ResourceExhausted。** `pkg/errors` 当前缺少 `ErrQuotaExceeded`。需要新增一个，其 HTTP 映射为 429（与限速相同的状态码，但错误代码不同，以便客户端区分——例如 `quota.exceeded` vs `rate.limit_exceeded`）。gRPC 映射应为 `codes.ResourceExhausted`。

**预期的架构变更：**
- 新增：`domain/quota.go` — `Quota`、`ResourceUsage`、`QuotaExceededError`
- 新增：`repository/quota.go` — 计数器表（`resource_usage`、`quota_overrides`）
- 变更：`service/file.go` — 上传前增加配额检查
- 变更：`service/message.go` — 每日消息配额
- 变更：`service/comment.go` — 每日评论配额
- 变更：`pkg/errors/errors.go` — 新增 `QuotaExceeded(code, msg string)`
- 变更：`config.go` — `QuotaConfig`（每个储存储量、每条消息默认值）
- 变更：`middleware/ratelimit.go` — 可选地在写入路径中注入配额检查

**对现有系统的影响：** 低到中等——新增一个计数器模式。所有配额检查都是新增的拦截器，而不是修改现有路径。不会破坏 API 合同。

---

### 优先级 P1（领域扩展——主要的产品差异化）

#### 方向 1：群组聊天（P1）

**为什么需要：** 如前所述，这是最大的产品差异化优势。当前的消息模型（`FromUID`、`ToUID`）映射为严格的 1:1 E2EE 风格。增加群组可以解锁：
- 团队聊天/频道
- 广播消息（一个发件人 → 多个收件人）
- 通过在 `messages` 上添加 `conversation_id` 实现会话组织

**核心挑战（按难度排序）：**
1. **消息 schema 迁移（可实现）。** 给 `messages` 添加一个可为空的 `conversation_id` 列，兼容旧版 1:1 对话（将 `CONCAT(LEAST(from_uid, to_uid), GREATEST(from_uid, to_uid))` 塞入现有的 conversation 行）。回填现有的对话是一项不阻塞的数据操作，但需要迁移计划。

2. **发件箱扇出（困难）。** 一个群组消息：一个物理行，N 个逻辑收件人。目前的发件箱模式是："消息创建 → 一个 outbox event（message.sent）→ Dispatcher 创建**一条**通知行指向收件人"。在群组场景中，发件箱 event 需要映射到**每个**参与者的一条通知。有两种选择：

```
方案 A：扇出发件箱（一个 event → Dispatcher 迭代参与者列表并创建 N 条通知）
  → 更少的发件箱行
  → Dispatcher 需要在 event payload 中接收参与者列表
  → 如果 N=1000 且 Dispatcher 在迭代到一半时崩溃，重建状态很麻烦

方案 B：扇出写入路径（发送方在事务中创建 N 个 outbox events）
  → 每个 event 对应一个收件人——与当前模式兼容
  → 发送方的事务更大且更慢
  → N=1000 时，事务超时风险

方案 C：混合（创建 1 个发件箱事件，一个专门的 "群组扇出 handler" 将其拆分成每个收件人的事件）
  → 支持原子消息创建
  → 需要发件箱中的第二个 relay/event 类型（message.group_sent）
  → 在 relay 的 chain 中增加一个处理阶段
```

我推荐方案 C——它在原子消息创建和可恢复扇出之间取得了正确的平衡，且与当前发件箱架构保持一致。

3. **未读计数（棘手）。** 目前的 `unread_counter` 表是 per-(uid, conversation_type) 的——群组需要 per-(uid, conversation_id) 的计数。这需要迁移 `unread_counters` 以支持 `conversation_id`（可以为空以兼容旧版）。当用户标记一个群组对话为已读时，与该对话 ID 匹配的所有消息都需要标记为已读——这是一个范围操作，不是逐条操作。

4. **索引覆盖。** 如你所注意到的，`idx_messages_inbox` 等现有索引是 `WHERE deleted_at IS NULL` 的部分索引。群组查询是 `WHERE conversation_id = ? ORDER BY created_at DESC`。需要一个新的部分索引 `idx_messages_conversation (conversation_id, created_at DESC) WHERE deleted_at IS NULL AND conversation_id IS NOT NULL`。由于 `conversation_id` 在旧行中可能为空，规划器可能选择在遗留数据上做顺序扫描——在大型表上通过部分索引 + `IS NOT NULL` 过滤条件来缓解。

**预期的架构变更：**
- 新增：`domain/conversation.go` — `Conversation`、`ConversationParticipant`、`ConversationRole`
- 新增：`repository/conversation.go`
- 变更：`domain/message.go` — 添加 `ConversationID *ID`
- 变更：`repository/message.go` — 添加 `ListByConversation`、`MarkConversationRead`（范围操作）
- 变更：`service/message.go` — `SendGroup` 方法、`AddParticipant`、`RemoveParticipant`
- 新增：`internal/handler/grpc` 中的 gRPC 群组 RPC
- 变更：`config.go` — 最大群组大小、参与者超时等

**对现有系统的影响：** 中等——主要是新增内容，但 `messages` schema 实体发生了变更。`conversation_id` 上的索引为现有的 `messages` 行新增了大小开销。在方向 1 之前**可以先做**方向 2 和方向 3（它们是完全正交的）。

---

### 优先级 P2（运营/合规——在规模化之前做好）

#### 方向 4：GDPR 数据可移植性（P2）

**为什么需要：** 正如你所说，这是你的分析中最独特的增量——`docs/requirements/` 中没有任何其他文件涉及过。如果 aero-id 要在欧盟运营，这是合规的基线要求。

不过，我会重新将等级定为 P2 而非 P1，原因是：**GDPR 是一个运营合规要求，而非产品特性**。如果在业务签约要求 GDPR 合规的客户之前没有它，就可以接受。而方向 1（群组聊天）和方向 2（推送）则直接推动产品采纳。

**核心挑战：**
1. **出口策略是正确的，但实现起来很复杂。** "元数据 + blob 的下载链接（presigned URL）"是正确的架构。但需要注意：
   - 导出 archive 需要是一个 ZIP（或 tar.gz），包含一个 JSON 元数据清单以及可供下载的 presigned URL。如果有很多文件，archive 里的 presigned URL 可能在用户下载完成之前就过期了。需要为导出设置一个更长的 presigned TTL（例如 24 小时），或者一个单独的 `export` bucket，其中包含对象的短暂副本。
   - 大型导出不应该发生在请求线程中。你需要一个 `export_jobs` 表和一个队列（目前没有）——或者你可以复用发件箱 event 来异步驱动导出。

2. **"删除我的数据"的语义。** 这是最难的部分。当你删除一条消息时，收件人是否也应该失去查看该消息文本的权限？在 WhatsApp 中，发送者删除其账户不会从对方删除消息——它会显示"此消息的发送者已删除其账户"。我推荐**同样的策略**：
   - 软删除消息（设置 `from_uid = NULL` 但保留 `content`，以便收件人仍有历史记录）
   - 或者，硬删除消息文本，但留下一个占位符（"该用户已删除此消息"）

   GDPR 的"被遗忘权"要求你**自己的**数据被删除——不是你曾经发送给他人的消息。目前的架构（`from_uid` 上为 NULL 的软删除）已经支持这种区别。

3. **级联删除的范围。** 删除一个用户意味着：所有文件（硬删除 blob 或将其置为孤儿）、所有评论（设为匿名或硬删除）、所有消息（设为匿名）、所有通知、所有设备注册。这需要在 `service` 层有一个 `DataDeletionService`，通过 `domain.UserErasurePolicy` 接口协调删除，该接口对每个领域做出确定性决策。

**预期的架构变更：**
- 新增：`domain/export.go` — `ExportJob`、`ExportFormat`
- 新增：`service/export.go` — 生成 archive，presigned URL 处理
- 新增：`service/deletion.go` — 级联 `DeleteUserData(uid)` 协调
- 新增：`repository/export.go` — 导出作业持久化
- 新增：`migrations/`：`export_jobs` 表
- 变更：`service/file.go` — 添加 `AnonymizeFileOwnership`、`PurgeUserBlobs`
- 变更：`service/message.go` — 添加 `AnonymizeUserMessages`
- 变更：`handler/http` — 导出/删除端点（可能需要在 admin scope 下）

**对现有系统的影响：** 低到中等——新增功能，不更改现有查询。但与已有系统存在深度交互（blob 引用计数、文件共享权限）。

#### 方向 5：分布式速率限制与多实例协调（P2）

**为什么需要：** 在你能证明需要多实例之前（例如，Postgres 连接池饱和或 SSE 连接数超过 10k），不应添加。当前的 per-instance 速率限制器在单 pod 部署中是足够的。如果你的 CP 使用率不足 30% 且无连接瓶颈，就不添加。

**核心挑战：**
1. **Redis 依赖的引入。** 这是巨大的——你会增加一个运行时依赖，增加延迟（每个 RPC 的往返时间），并增加运维复杂度（Redis 集群管理、内存预算、键驱逐策略）。当前并没有使用 Redis。

2. **滑动窗口 vs 固定窗口。** 你正确地注意到了边界流量突增的问题。像 `INCR` + `EXPIRE` 这样的固定窗口是*可以*的，但我推荐使用 **GCRA（通用信元速率算法）** 或 **Sliding Window Log**。一个使用 Redis sorted set 的滑动窗口实现如下：
   ```
   ZREMRANGEBYSCORE key 0 (now - window)
   ZCARD key → 如果 < limit 则 ZADD，否则拒绝
   ```
   对于 1.0 版本，一个简化的方法是：`INCR` + `EXPIRE`，速率限制器窗口长度为 **1 秒**，窗口之间用 `MULTI/EXEC` 包装以避免竞态条件。这对于大多数用例来说都足够好。

3. **发件箱 relay 协调。** 你关于 Postgres 咨询锁的建议（`pg_try_advisory_lock`）很到位。我推荐：
   - `SELECT pg_try_advisory_lock(:lock_id)` 在 `Relay.Start()` 循环开始时调用
   - 如果返回 `false`（锁已被持有），跳过本次轮询
   - 使用 `lock_id = 123456`（或作为配置参数注入，以便一台机器可以运行多个 relay）

   这种方式比 `leader_elections` 表更轻量——无需额外 schema，纯 SQL，无心跳复杂度。

**预期的架构变更：**
- 新增：`internal/pkg/ratelimit/redis.go` — `RedisSlidingWindowLimiter` 实现同一个 `Limiter` 接口
- 变更：`config.go` — `RateLimitConfig.Distributed`（Redis 端点、滑动窗口类型、锁 ID）
- 变更：`outbox/relay.go` — 可选的 advisory lock 协调
- 变更：`app.go` — 有条件地构建分布式限流器或本地限流器

**对现有系统的影响：** 中等——在路径中新增了 Redis 作为可选依赖。不影响现有 API 或模式。错误情况下（Redis 宕机）应优雅降级为本地限流器或开放限流器。

---

## 3. 接口设计建议

### 关键模块接口原则

**原则 1：使限流器接口与后端无关。** 当前 `ratelimit.Limiter` 已有理想的边界——只有 `Allow(key string) bool` 和 `RetryAfter() time.Duration`。`RedisSlidingWindowLimiter` 应实现完全相同的接口，以便 `middleware` 包无需变更。

```go
// 当前（保留）：
type Limiter struct { ... }
func (l *Limiter) Allow(key string) bool
func (l *Limiter) RetryAfter() time.Duration

// 新增（同一个包）：
type RedisLimiter struct { ... }
func (r *RedisLimiter) Allow(key string) bool
func (r *RedisLimiter) RetryAfter() time.Duration
```

**原则 2：使用领域事件驱动推送，而非调度循环。** 不要为推送通知创建一个新的 relay 循环。将 `PushHandler` 作为 `outbox.Handler` 实现，根据 event type 筛选（跳过非推送事件）。将其注册为与 `Dispatcher` 并行运行的第二个 relay，或者注册为 `Chain` 中的第二个 handler。这种方式无需新的基础设施——只是现有发件箱模式的一个新 handler。

```go
// push/handler.go
type Handler struct {
    fcm    FCMClient
    apns   APNsClient
    filter map[string]bool  // 由配置构建：消息推送 event types
}

func (h *Handler) Handle(ctx context.Context, e *outbox.Event) error {
    if !h.filter[e.EventType] {
        return nil  // 此 handler 不处理该类型
    }
    tokens, err := h.findDeviceTokens(ctx, e.RecipientUID)
    // ... 发送到 FCM/APNs ...
}
```

**原则 3：群组参与者应该使用独立的 repository，而非嵌入到 `Message` 中。** 创建一个 `ConversationRepository` 和一个 `ConversationParticipant` 实体。不要向已有消息中添加参与者的重数组列。考虑到"群组 hasMany 参与者"这一规范化的关系，才是正确的。

**原则 4：配额检查应该作为装饰器（中间件）实现，而非嵌入到每个服务方法中。** 为编写路径（文件上传、消息发送、评论创建）创建一个 `QuotaMiddleware` 接口。这避免了跨服务的重复配额检查代码。

```go
type QuotaMiddleware interface {
    CheckStorageQuota(ctx context.Context, ownerUID string, size int64) error
    CheckDailyMessageQuota(ctx context.Context, senderUID string) error
    CheckDailyCommentQuota(ctx context.Context, ownerUID string) error
}
```

### 是否需要新的抽象层

**是的——一个方向 2 的 `push` 包。** 当前没有 push 抽象（没有 `PushClient` 接口，没有 `DeviceRepository`）。这应是一个新的 `internal/push` 包，带有明确的接口：

```go
type PushClient interface {
    Send(ctx context.Context, tokens []string, payload PushPayload) (*PushResult, error)
    // Send 的底层实现针对 FCM 的 sendAll 进行了批量处理
}

type PushPayload struct {
    Title       string
    Body        string
    Badge       int
    Data        map[string]string  // 自定义键值对
    Category    string             // iOS 交互式通知类型
}
```

对于方向 4，是否需要**导出 service**。目前没有生成 archive 或在异步作业中协调大量行导出的抽象。创建一个 `ExportService` 是有意义的，该服务使用发件箱事件来标记导出作业的开始/完成（回调，而非轮询）。

### 向后兼容性

- **方向 1（群组消息）：** 必须与现有的 1:1 消息完全向后兼容。迁移后，1:1 对话应有可预测的 `conversation_id`（例如 `LEAST(uid1, uid2)_GREATEST(uid1, uid2)` 的哈希）。现有 API 端点（`GET /v1/conversations/:uid`）应保持可用，不走群组代码路径。
- **方向 2（推送）：** 完全增量。不更改现有 API。
- **方向 3（配额）：** 可选限额。如果未设置配额，一切照旧。如果超出配额，则返回 429。
- **方向 4（GDPR）：** 新增端点——不影响现有工作流。
- **方向 5（分布式限流）：** `middleware/ratelimit.go` 应保持不感知后端。本地和 Redis 限流器应实现相同的 `Allow/RetryAfter` 接口。

---

## 4. 技术选型

### 是否需要新的技术栈

**Redis（方向 5）：** 是的，用于分布式限流。最低版本：Redis 6.2（支持 `ZREMRANGEBYSCORE`、`EXPIRE`、`MULTI/EXEC`）。不需要 Redis Stack 或 RedisGears。

**FCM/APNs（方向 2）：** 是的，用于移动推送。Go 有一个官方的 Firebase Admin SDK（`firebase.google.com/go/v4/messaging`）。对于 APNs，推荐使用 `github.com/sideshow/apns2`。不过需要注意：FCM SDK 可以处理 APNs 和 FCM——如果你的目标同时包含 iOS 和 Android，一种合理的方法是仅使用 FCM，并通过`APNs`配置（FCM 的 `apns` 字段）发送给 iOS。

**No Redis Cluster / Sentinel（方向 5）：** 对于 1.0 版本，使用单个 Redis 实例，由你现有的部署编排处理故障转移。只有在需要对限流器状态进行故障转移时才需要 Sentinel。最简单的方案：为一个有状态存储实现 `Limiter` 接口，该存储使用 Postgres（对 `rate_limit_buckets` 表使用 `SELECT ... FOR UPDATE`）。这避免了新增一个依赖项，尽管延迟高于 Redis。

**第三方依赖评估：**

| 依赖项 | 用途 | 评估 |
|---|---|---|
| `firebase.google.com/go` | FCM 推送 | ✅ 官方维护。大型。仅使用 `messaging` 子包。 |
| `github.com/sideshow/apns2` | APNs 推送 | ✅ 成熟。HTTP/2。支持 JWT 和证书认证。 |
| `github.com/go-redis/redis/v9` | Redis 客户端 | ✅ 事实上的标准。如果你只需要基本命令，也可以用 `rueidis`（更小）。 |
| `github.com/google/uuid`（已有） | UUID 生成 | ✅ 已使用。 |

**自建与采购的决策：**

| 服务 | 推荐方案 | 理由 |
|---|---|---|
| 推送通知 | 自建（`internal/push`） | FCM/APNs 是标准协议。无需中间件服务。 |
| 用户导出 archive | 自建（`service/export.go`） | 业务逻辑是完全特定的（选择哪些行、如何组织 archive）。 |
| 分布式限流 | 自建（`ratelimit.RedisLimiter`） | GCRA 算法很简单。无需现成的限流服务。 |
| 群组聊天 | 自建（新增 `conversation` 领域） | 这是核心产品，不是可购买的组件。 |

**不推荐的：**
- **Firebase Cloud Messaging 之外的第三方推送服务（OneSignal、Airship）** ——对于像 aero-id 这种规模的系统来说，第三方服务会增加不必要的成本层，且提供的价值很少（它们主要是针对营销推送和受众细分进行优化，而非事务性通知）。
- **Redis 价格限制器（例如 Redis Stack 内置的限流功能）** ——内置的 `Redis Stack` 限流功能是专有的且不可移植。自建一个使用标准 Redis 数据结构的 GCRA 更简单，且如果将来需要，也允许你更换后端。

---

## 5. 实施路线图

### 总体阶段划分

| 阶段 | 内容 | 时间线估计 |
|---|---|---|
| **Phase 0** | 预重构—拆解 `app.go`，新增 `ErrQuotaExceeded`，新增 `PushClient` 接口骨架（无实现） | 1 周 |
| **Phase 1A** | 方向 3（配额）—资源计数器、每日限额、限额检查中间件 | 2 周 |
| **Phase 1B** | 方向 2（推送）—设备注册、FCM 集成、发件箱推送 handler | 3 周 |
| **Phase 2** | 方向 1（群组）—会话 schema、参与者模型、消息扇出 | 4 周 |
| **Phase 3A** | 方向 4（GDPR）—数据导出/删除服务、级联策略 | 3 周 |
| **Phase 3B** | 方向 5（分布式限流）—Redis 限流器、发件箱协调 | 2 周 |

总计：约 15 周（工程月数：3–4 个月，取决于团队规模）。

### 关键路径与依赖

```
Phase 0（预重构）[1w]
  ↓
Phase 1A（配额）[2w]                 Phase 1B（推送）[3w]
  ↓                                       ↓
Phase 2（群组）[4w] ← 需要配额（消息计数）     ↓（设备注册）
  ↓                                       ↓
Phase 3A（GDPR）[3w] ← 需要设备注册 + 群组级联   ↓
  ↓                                       ↓
Phase 3B（分布式限流）[2w] ← 可独立于所有         ↗
```

### 风险点与缓解策略

**风险 1：方向 2（推送）中的发件箱 chain 语义。**
- **影响：** 如果选择方案 C（两个独立的 relay），一个 relay 出错不会影响另一个。但如果选择保留单 chain，Dispatcher 失败会阻止推送，反之亦然。
- **缓解：** 从一开始就实现 fan-out Chain（或将推送 handler 放在 Dispatcher 之前）。不要在代码中留下 todo 注释——要一次性做出正确的设计决策。

**风险 2：方向 1（群组）通过发件箱扇出 1000+ 个成员。**
- **影响：** 一个带 1000 个参与者的群组消息会创建 1000 个通知行 + 1000 次未读计数更新。如果 Dispatcher 在处理第 500 个时崩溃，那 500 个参与者没收到通知。
- **缓解：** 使用我在方向 1 下建议的**方案 C（中间发件箱拆分事件）**。创建一个 `message.group_sent` 事件类型和一个 `GroupFanOutHandler`，后者枚举参与者并为每个参与者创建 `message.participant_notify` 事件。这使得扇出是可恢复的（每个参与者事件都有自己的重试状态）。

**风险 3：方向 4（GDPR）中删除用户数据的级联范围。**
- **影响：** 一个错误的级联删除（例如，删除用户 A 时从用户 B 的对话中删除了消息文本）可能会导致用户 B 丢失数据。
- **缓解：** 采用"软匿名"策略。不要硬删除内容，而是将 `from_uid` 设为 NULL（或占位符 UID `deleted_user`），并保留内容。如果你必须出于 GDPR 删除内容，则将其替换为占位符文本（[该用户已删除此消息]）。这种做法已在 WhatsApp 和 Signal 中使用。

**风险 4：方向 5 的 Redis 故障切换。**
- **影响：** 如果 Redis 宕机，速率限制检查会失败——如果降级不够优雅，可能会允许无限制的请求。
- **缓解：** 实现**故障降级**（如果 Redis 不可用，则回退到本地内存限流器 + 告警），而非故障关闭（拒绝所有请求）。`RedisLimiter.Allow()` 应捕获连接错误并回退到 `inMemoryLimiter.Allow(key)`。

**风险 5：方向 2（推送）中无响应的设备令牌。**
- **影响：** FCM 为无响应的令牌返回规范的错误代码`UNREGISTERED`。如果不清除它们，垃圾令牌会随着时间推移堆积。
- **缓解：** 在 `PushHandler.Handle()` 中添加一个 `invalidTokenCallback`，自动从 `device_registrations` 中移除无响应的令牌。添加一个每日 cron（发件箱事件）来轮换接近过期的令牌。

---

## 总结

你提供的分析文档在代码证据方面很扎实，比我预期的更准确。五个方向中有四个（群组、推送、配额、限流）在 `docs/requirements/` 中的 100+ 个分析中已经得到支持，但方向 4（GDPR）是真正的新颖内容——在合规性方面具有实用价值。

你需要做出的最重要的设计决策是：

**立即：** 决定 `outbox.Chain` 的方向 2 的**短链行为**。我推荐 fan-out（独立）方法——可以尽早开始。

**Phase 0：** 将 `app.go` 拆分成领域绑定的文件（`app_messages.go`、`app_notifications.go`、`app_files.go`）。早在膨胀成问题之前，组合根就已经是"技术债务"的首要指标。

**方向 2 和方向 1 可以并行进行：** 推送和设备注册（方向 2）不依赖于群组架构（方向 1）。它们可以同时由不同的人开发。

**唯一的"do not do"设计：** 不要在群组消息的 `messages` 中添加 `participants` JSON 数组。群组是会话模型的规范类型——`conversations` 和 `conversation_participants` 表是正确建模这个问题的唯一方法。任何捷径都将在半年内导致数据一致性问题。
