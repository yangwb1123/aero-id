现在我已经彻底了解了代码库。以下是我的架构分析。

---

# 架构分析：aero-id

## 1. 架构评估

### 1.1 当前架构的优势

**严格的单向依赖层**是代码库中最强的架构决策。`handler → service → repository/storage/sso` 约束意味着：

- **HTTP 和 gRPC Handler 是纯粹的翻译层**。它们不包含业务逻辑，只包含 I/O（解码→调用服务→渲染）。这验证了开闭原则：添加新传输（例如 WebSocket、AMQP）不需要重复逻辑。
- **服务层是单一事实来源**。所有权检查和业务规则（例如评论嵌套深度两层、内容去重、文件可见性 ACL）位于一个中心位置。不可能通过一个传输绕过检查，因为另一个传输也调用相同的 `service.CommentService`。
- **Repository 接口是测试契约**。服务测试使用 `internal/service/mocks` 中的伪造对象，不需要数据库。

**组合根（`app.go`）** 非常出色地处理了接线。SSO 预热、速率限制器清理器、幂等性键清理器、存储 GC 以及出站中继都具有带有返回 `stop` 函数的 `Start()` 模式。`shutdown()` 方法以正确的、负载均衡器友好的顺序排出它们（先将 gRPC 健康状态翻转为 NOT_SERVING，然后停止中继，再关闭 HTTP）。

**Like/Unlike 实现** 是正确且经过深思熟虑的。文档正确地发现了它的安全性：

- **Like**：`ON CONFLICT DO NOTHING` + `RowsAffected == 1` 保护——如果两个并发事务插入相同的 `(comment_id, uid)` 对，其中一个的 `RowsAffected` 将为 0，且 `like_count` 不会增加。这是无锁的。
- **Unlike**：`DELETE` + `RowsAffected == 1` 在 Read Committed 下是安全的，因为 `DELETE` 会等待竞争事务提交，然后重新评估可见性。`GREATEST(like_count - 1, 0)` 可防止计数器下溢。

**出站模式** 被正确实现。事件在与应用数据相同的数据库事务中追加（例如，`CreateWithReplyBump` 中的 comment.replied）。中继使用带有指数退避的轮询。Webhook handler 有 `http.Client.Timeout`。

### 1.2 当前架构的局限性

**1. 上下文传播不一致**

出站中继是一个显着的风险。`relay.go` 使用 `context.Background()` 作为其主循环上下文。在 `drain()` 内部，两个关键路径使用这个后台上下文：

```
// dispatcher.go — 使用 ctx (= context.Background()) 调用
d.notifs.Create(ctx, n)         // ← 无超时 — 可能无限期挂起
d.blocks.IsBlocked(ctx, ...)    // ← 同样无超时
// relay.go
r.repo.MarkFailed(ctx, ...)     // ← 同样无超时
```

这是一个 P0 级的架构缺陷：**后端的 DB 连接池耗尽会导致整个出站中继死锁**。由于中继轮询和分发 *所有* 事件，如果 dispatcher 挂起，新事件永远不会被处理。Webhook 有 `http.Client.Timeout` 可以缓解自身风险，但 dispatcher（在 chain 中排在第一位）没有。

**2. 文件去重竞争**

文件创建路径有一个 TOCTOU（检查时间与使用时间）竞争：

```
// file.go — 第 71-82 行
existing, ferr := s.files.FindByChecksum(ctx, actor, checksum)
if ferr == nil {
    // 命中去重 — 重用现有的 storageKey
} else if !stderrors.Is(ferr, repository.ErrNotFound) {
    // 错误
}
// ... 继续创建新文件行
```

由于 `checksum` 列上没有唯一约束（只是 `migrations/000001` 中的一个索引），并且没有 `SELECT ... FOR UPDATE`，因此两个并发上传具有相同校验和的同一所有者会产生两个文件行和两个 blob。存储引用计数（`WithStorageRefs`）减少了 blob 泄漏，但行重复仍然存在。

**3. SSE 连接管理不足**

SSE Hub 没有：
- **每 uid 连接限制** — 一个用户可以打开无限数量的 SSE 连接
- **全局连接限制** — 许多用户总共可以打开无限数量的连接
- **连接活跃超时** — 连接理论上是永久的
- **大于 16 的事件缓冲区** — 写入缓慢的客户端会丢弃事件，而不是阻塞

这是 P1，但对于部署来说是一个真实的问题：一个恶意（或 buggy）客户端可以通过耗尽文件描述符、goroutine 或 `sync.Map` 内存来耗尽服务器资源。唯一的保护是 gin 的 `WriteTimeout`（当有活跃流量时，SSE 会不断重置它）。

**4. 出站中继缺少 `FOR UPDATE SKIP LOCKED`**

中继的 `FetchUnpublished` 是一个简单的 `SELECT`。在多实例部署中，两个实例可以获取相同的事件。两个实例都尝试处理它们。两个实例都可能成功（Power 事件），或者一个失败，导致另一个的处理工作重复。缺少 `FOR UPDATE SKIP LOCKED` 意味着锁争用发生在应用层，而不是数据库层。

**5. 数据生命周期存在缺口**

- `notifications` — 行永久累积，没有 TTL 物理删除
- `outbox_events` — `MarkPublished` 保留行，没有 TTL 清理。即使添加 dead_letter 标记，行也会保留
- `audit_events` — 无 TTL 策略
- `messages`、`comments` — 软删除，无物理清理路径

对于那些在 `app.go` 中有后台清理器的表（幂等性键有 `RunCleaner`，存储 GC 有 `Start`），没有等效的清理未使用的通知或过期的审计事件行的机制。

### 1.3 架构债务

| 债务 | 严重程度 | 原因 |
|------|----------|---------|
| `context.Background()` 在中继中传播 | **P0** | 一个阻塞的 DB 调用会死锁整个中继 |
| 文件去重竞争 | **P1** | 可重复行，存储膨胀 |
| 无 `FOR UPDATE SKIP LOCKED` | **P0** | 多实例事件重复 |
| SSE 无上限 | **P1** | 资源耗尽向量 |
| 数据生命周期策略 | **P2** | 表无限增长 |
| 通知速率限制器是每个实例的 | **P1** | 在多实例中，一个流氓 actor 可以向每个中继滴答发送通知（每个实例最多 10 次，总共 30 次） |

---

## 2. 扩展方向

### 2.1 引入分布式速率限制和节流阀

**为什么**：`notifRateLimiter` 是每个实例本地的，每个中继滴答独立工作。在三实例部署中，一个流氓 actor 可以实现 3 倍的目标速率。这既是 UX 问题（通知洪水）又是 DoS 向量（可能对 SSO 或下游接收者造成拒绝钱包攻击）。

**核心挑战**：`rate` 乘以 `n` 个实例。修复方法是将速率限制状态移动到共享存储（Redis、Postgres advisory lock，或像 `pg_available_extension` 这样的数据库侧窗口函数）。

**预期的架构变更**：

```
[当前]
notifRateLimiter (sync.Mutex + map[string][]time.Time) — 每个进程

[将来]
interface RateLimiter {
    Allow(ctx context.Context, key string, rate int) (bool, error)
}
实现：
  - LocalRateLimiter (sync.Map) — 单实例，低延迟
  - RedisRateLimiter (Sliding Window + Sorted Set) — 多实例
  - PGRateLimiter (advisory lock or pg_try_advisory_xact_lock) — 无外部依赖
```

Dispatcher 通过 `WithDispatcherRateLimiter(RateLimiter)` 接收一个 `RateLimiter`，而不是一个 `int`。

**对现有系统的影响**：最小。接口变化仅影响调度程序构造。测试可以使用 `LocalRateLimiter`。

**选项**：
- **A（低努力）**：使用 `pg_try_advisory_xact_lock(key_hash, rate)` 进行应用级速率限制。没有新依赖，但吞吐量有限。
- **B（推荐）**：引入 Redis；将其用于速率限制、可选的分布式缓存，以及未来可能的出站协调（`FOR UPDATE SKIP LOCKED` 取代了 redis 对出站的需求）。
- **C（远期）**：使用像 `RedisRateLimiter` + `RedisCache` 这样的通用速率限制库。

### 2.2 专门的通知清理器（类似存储 GC）

**为什么**：通知和审计事件行会永久累积。对于包含数十亿行的大规模部署，所有查询最终都会变慢（即使有索引），因为表膨胀。主用户查询 `ListByRecipient` 是一个 `WHERE recipient_uid = ? ORDER BY id DESC` — 删除的行上的死元组会向 autovacuum 施加压力，并且即使有 btree 索引，大量扫描也会更慢。

**核心挑战**：
- 通知 TTL 必须是可配置的（例如，保留 90 天，之后物理删除）
- 审计事件可能需要不同的保留期（例如，7 年合规性 vs 90 天操作日志）
- 批量删除必须分块，以避免在复制延迟或锁争用（`DELETE ... WHERE id < ? LIMIT 1000`）
- 软删除的行（消息、评论）在物理删除之前有一个上游依赖关系（清除子实体）

**预期的架构变更**：

```
internal/repository/cleanup/
  notification_cleaner.go  // 物理删除早于 TTL 的已读通知
  audit_cleaner.go         // 按保留策略归档 + 删除
  outbox_cleaner.go        // 物理删除早于 TTL 的已发布/dead-letter 行
```

在 `app.go` 中：

```go
cleanup := cleanup.New(notifRepo, auditRepo, outboxRepo, cleanup.Config{
    NotificationTTL: 90 * 24 * time.Hour,
    AuditTTL:        365 * 24 * time.Hour,
    OutboxTTL:       7 * 24 * time.Hour,
    BatchSize:       1000,
}, log)
stopCleanup := cleanup.Start(1 * time.Hour)
```

清理器和**存储 GC**（已经存在）之间的区别在于，存储 GC 处理 blob 生命周期，而清理器处理行生命周期。

**对现有系统的影响**：中等。需要添加新的后台清理器，并可能针对高容量表进行下行迁移。为了向后兼容，TTL 应该默认为一个很大的值（或零，表示“永不清理”）。

### 2.3 出站中继高可用性 + 幂等性保证

**为什么**：没有 `FOR UPDATE SKIP LOCKED`，多实例部署会看到事件重复。这对于 webhook 传递尤其成问题，因为外部系统可能收到重复的 POST。幂等性键（HTTP 边）不会保护 webhook 接收者，因为出站事件没有幂等性键。

**核心挑战**：
- 中继需要至少一次的传递语义
- Webhook 接收者需要幂等性保证
- `MarkPublished` 不能是最后写的，因为中继可能在标记之前崩溃，导致重复

**选项**：

**选项 A（推荐 — 数据库专用）**：将 `FetchUnpublished` 更改为使用 `FOR UPDATE SKIP LOCKED`，并对行设置租约超时。如果中继在处理过程中崩溃，租约将过期，另一个中继会重新处理该事件。这保证了至少一次传递，且无需新依赖项。

```sql
-- 伪 SQL
BEGIN;
SELECT * FROM outbox_events
WHERE published_at IS NULL
  AND (leased_until IS NULL OR leased_until < NOW())
ORDER BY created_at ASC
LIMIT 100
FOR UPDATE SKIP LOCKED;
UPDATE outbox_events SET leased_until = NOW() + INTERVAL '30 seconds'
WHERE id IN (...);
COMMIT;
```

**选项 B（中等努力）**：引入 Redis 用于出站协调。使用 `BRPOPLPUSH` 或带有确认的流在实例之间分配事件。

**对现有系统的影响**：选项 A 仅影响 `outbox.Repository` 接口和 `FetchUnpublished` 的实现。语义保持不变。

### 2.4 连接管理抽象（SSE + 未来的 WebSocket）

**为什么**：SSE Hub 是 ad-hoc 的，缺少标准的连接管理模式。添加 WebSocket 传输将意味着复制订阅/取消订阅逻辑、ping 管理以及缺少的资源控制。

**预期的架构变更**：

```
internal/pubsub/
  conn.go          // 连接接口：ID, Context(), Send()
  subscriber.go    // 按主题（uid）的订阅注册表
  conn_metrics.go  // 活跃连接数，丢弃的事件数
  
internal/handler/http/
  events.go        // 保留为 SSE 适配器：Conn ←→ http.ResponseWriter
```

新的连接注册表应该是：

- 可限流的（每 uid 和全局上限）
- 已检测（主动连接数、丢弃的事件数、ping 延迟）
- 对慢速消费者流畅降级（可配置的缓冲区大小 + 背压策略）

**对现有系统的影响**：重大但受控。`Hub` 将变为 `pubsub.Registry`。现有的 `hub.Adapter`（`Publish` 方法）保持向后兼容性，因此 dispatcher 不需要更改。只有 HTTP `EventsHandler` 会被重构。

**理由**：当通知量增长时（从消息到评论再到未来的交互），实时推送成为核心基础设施组件。值得进行适当的抽象。

### 2.5 可观察性和服务网格就绪

**为什么**：代码库有 OpenTelemetry 追踪和基本的 RED 指标（在 `metrics.Registry` 中），但它们是可选的，并且没有涵盖关键路径。具体来说：

- 出站中继没有跨度（`drain()` 不受轨迹追踪）
- 没有跟踪因缺少 `FOR UPDATE SKIP LOCKED` 而导致的事件重复
- 没有在中继失败时发出告警的事件计数器

**核心挑战**：
- 跨度传播必须跨越出站中继的 `context.Background()` — 修复上下文传播是此问题的前提条件
- RED 指标（速率、错误、持续时间）对于每个操作都是有用的：like/unlike、comment create、file upload dedup hit/miss

**预期的架构变更**：
- 每个出站处理程序获取一个带有服务名称的 `tracer.Start(ctx, "handler."+eventType)` 跨度
- 服务层公开记录操作时序和错误标签的 `metrics.Observe()` 调用
- 添加健康仪表板端点（`/metrics`、`/healthz`、`/readyz`）

**对现有系统的影响**：最小。指标已经通过 `WithMetrics(metricsReg)` 模式进行了检测。缺失的是出站中继和服务层。

---

## 3. 接口设计建议

### 3.1 当前接口评估

接口拆分是合理的，但有几个需要改进的地方：

**强项**：
- `FileStorage` 接口（`Put`、`Get`、`Delete`） — 简洁，适用于 local 和 s3
- `outbox.Handler` 接口 — 非常适合 chain 和 dispatcher 模式
- `repository.*` 接口 — 足够的抽象以允许业务层测试

**弱点**：
- `NotificationPusher` 接口是无类型的（`Publish(uid, eventType, data string, eventID int64)`） — 接收者和调用者之间没有契约
- `NotificationDispatcher` 有 5 个可选依赖项（块、偏好、推送器、速率限制器、折叠），作为 `DispatcherOption` 传递 — 可选依赖项的数量表明 dispatcher 承担了太多责任
- 没有`SSOClient` 接口 — `sso.Client` 是具体的，因此无法对服务层进行单元测试以验证 SSO 交互（`enrichUsers`、`mustActor`）

### 3.2 建议的抽象

**1. 引入 `sso.UserDirectory` 作为接口**

当前，`mustActor` 和 `enrichUsers` 是包级函数，调用 `sso.Client`。要使服务层完全无需 SSO 即可测试，请将 SSO 交互抽象如下：

```go
// internal/service/service.go 或 sso/
type UserDirectory interface {
    Lookup(ctx context.Context, uid string) (*domain.User, error)
    BatchLookup(ctx context.Context, uids []string) (map[string]*domain.User, error)
    ValidateToken(ctx context.Context, token string) (string, error) // 返回 uid
}
```

`service.New*(..., dir UserDirectory)` 接收 `UserDirectory` 而不是具体的客户端。这与 `WithBlockChecker` 和 `WithPreferenceChecker` 的模式相匹配。

**2. 拆分调度程序的关注点**

`NotificationDispatcher.Handle` 有 100 多行代码，用于检查偏好、速率限制、阻止抑制、折叠和下推。每个职责都应该是可组合的处理程序：

```go
type NotificationHandler interface {
    Handle(ctx context.Context, n *domain.Notification) error
}

// 组合子：
prefHandler := notification.NewPreferenceFilter(prefChecker)
rateHandler := notification.NewRateLimitFilter(rateLimiter)
blockHandler := notification.NewBlockFilter(blockChecker)
collapseHandler := notification.NewCollapseHandler(notifRepo, collapseWin)

// chain := prefHandler → rateHandler → blockHandler → collapseHandler → persistHandler
```

但是这带来了新的复杂度。当前的方法——一个带有可选构造函数的 dispatcher——对于现有的关注点数量是可以接受的。只有在增加 2-3 个更多的过滤器（例如，优先级、设备路由）时，才应进行重构。

### 3.3 向后兼容性

由于这是一个内部服务，没有外部消费者，向后兼容性是一个较小的问题。但是：

- **gRPC 协议**是版本化的（`aeropb`）。字段可以用 `optional` 或 `deprecated` 添加，但不能重命名或删除。
- **HTTP API** 应避免破坏性更改。添加字段到响应 JSON 是安全的；删除或重命名字段则不安全。
- **幂等性键**模式意味着客户端可以重放请求，因此**请求是幂等**是幂等性 API 的硬性要求。

---

## 4. 技术选型

### 4.1 当前技术栈评估

| 技术 | 评估 | 行动 |
|------|------|------|
| Gin | ✅ 适合 RPC 类 API。轻量，经过实战检验 | 保留 |
| GORM | ⚠️ 导致隔离测试困难，隐藏错误（`Find` 是 `nil` vs `empty`），生硬的 `Unscoped()` | 保留，但限制在仓库层 |
| gRPC | ✅ 适合内部通信 | 保留 |
| Viper | ✅ 可以 | 保留 |
| Zap | ✅ 经过实战检验，高性能 | 保留 |
| Testcontainers | ✅ 集成测试的理想选择 | 保留 |

### 4.2 新依赖项评估

**Redis**（用于分布式速率限制 + 可选缓存）：

- **何时添加**：当你部署 2+ 个实例进行生产时
- **替代方案**：PostgreSQL 顾问锁（无新依赖项，但吞吐量较低），或保持每个实例的限制（同意某些漂移）
- **评估**：仅当速率限制需要严格跨实例时才添加 Redis。对于许多部署来说，每个实例 10/分钟的速率是可以接受的

**Prometheus** 客户端（指标）：

- **状态**：已经部分存在（`metrics.Registry` 是为它设计的）
- **行动**：完成实现，添加 `/metrics` 端点，并检测出站中继

**OpenTelemetry SDK**：

- **状态**：已添加（`internal/pkg/tracing`）
- **行动**：完成跨度覆盖，特别是针对出站中继路径

### 4.3 自建 vs 采购

**自建（现有策略 — 正确）**：
- `hub.Hub` — 对于简单的 SSE 注册表来说，合理的自建，但应该升级为 `pubsub.Registry`
- `notifRateLimiter` — 简单的滑动窗口，可以保留自建
- `outbox.Relay` — 核心业务逻辑，应保留自建
- `storage.{local,s3}` — 文件存储抽象，应保留自建

**候选购买/借用**：
- 分布式速率限制使用 `github.com/go-redis/redis_rate/v10` — 节省了实现 Lua 脚本或 Redis 事务的时间
- 用于 event-sourced 审计日志的 `debezium` 或 `wal-g` — 但需要 CDC 基础设施，远超过当前需求

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 方向 | 风险 | 努力 | 影响 |
|--------|------|------|------|------|
| **P0** | 上下文传播至中继 + `FOR UPDATE SKIP LOCKED` | **停机/死锁** | 中 | 核心可靠性 |
| **P1** | 文件去重：添加唯一约束 + 创建前验证 | 重复行/膨胀 | 小 | 数据质量 |
| **P1** | SSE 资源管理：每 uid 上限 + 全局上限 | 资源耗尽 | 小 | 服务器稳定性 |
| **P1** | 通知速率限制器：使速率限制器成为接口（分布式就绪） | 多实例通知洪水 | 中 | 可靠通知 |
| **P2** | 数据生命周期：通知 + 审计 + 出站清理器 | 表膨胀 | 中 | 长期运营 |
| **P2** | 可观察性：指标 + 完整跨度覆盖 | 调试困难 | 小 | 运营效率 |
| **P3** | SSE → pubsub 重构（WebSocket 就绪） | 没有一个传输 | 大 | 未来扩展性 |

### 5.2 阶段划分和里程碑

**阶段 0：核心可靠性（第 1 周）** — 修正 P0

1. 在 `drain()` 内部，用 `context.WithTimeout(ctx, handlerTimeout)` 包装每个 `handler.Handle(ctx, e)` 调用
2. 将 `FetchUnpublished` 更改为使用 `FOR UPDATE SKIP LOCKED` 并设置租约超时
3. 添加 `context.WithTimeout` 到 `MarkFailed` 和 `MarkPublished` 的仓库调用
4. 验证：`make test`、`make test-integration`、在多实例 docker-compose 上手动测试

**阶段 1：基础设施加固（第 2-3 周）** — 修正 P1

1. 文件去重：添加 `UNIQUE (owner_uid, checksum)` 约束（带上行迁移以去重现有行），更新创建路径以处理 `ErrDuplicate`，添加 `SELECT ... FOR UPDATE` 或唯一约束保护
2. SSE 资源管理：在 `hub.Subscribe` 中添加每 uid 和全局上限。添加指标
3. 通知速率限制器：提取 `RateLimiter` 接口。保留 `LocalRateLimiter` 实现。添加允许每个实例可配置速率的选项

**阶段 2：数据生命周期（第 4 周）** — 修正 P2

1. 为 `notifications`、`outbox_events`、`audit_events` 添加清理器
2. 每个清理器获取一个 `cleanup.go` 文件和一个后台 `Start()` 协程，类似于存储 GC 模式
3. 配置默认为“永不清理”，因此现有部署不会意外删除数据

**阶段 3：可观察性（第 5 周）** — 完成指标和追踪

1. 将现有 `metrics.Registry` 桥接到 Prometheus（在 `/metrics` 暴露）
2. 为出站中继添加跨度（每个 `drain()` 和每个处理程序调用）
3. 为 Like/Unlike 操作添加直方图

**阶段 4：实时抽象（第 6-8 周）**

1. 将 `hub.Hub` 重构为 `pubsub.Registry`
2. 添加 WebSocket 传输（可选——取决于需求）
3. 为传输切换添加连接接口

### 5.3 风险点和缓解策略

| 风险 | 可能性 | 影响 | 缓解 |
|------|---------|------|------|
| 添加唯一约束到带有重复项的 `files(owner_uid, checksum)` 上 | 中 | 高 | 阶段 1 之前运行检测 + 去重迁移；如果数据质量可疑，阻止部署 |
| `FOR UPDATE SKIP LOCKED` 在 pg < 9.5 上失败 | 低 | 高 | 要求 Postgres >= 12（已经在 go.mod 中定位） |
| 清理器在大型表上导致大量锁 | 中 | 中 | 使用小批量（`LIMIT 1000`），带 `pg_sleep` 或批量删除 |
| 在上下文中添加超时导致随机事件丢失 | 中 | 中 | 从高超时（例如 30 秒）开始，监控 `ObserveOutboxFailed` 速率，然后调整 |
| SSE 每 uid 上限破坏合法用户的多设备风扇输出 | 中 | 低 | 上限为每个 uid 10 个连接，记录证据，使用 `X-SSE-Max-Connections` 标头传达限制 |

### 5.4 实施序列（按文件）

**阶段 0 的关键文件**：

| 文件 | 更改 |
|------|------|
| `internal/outbox/relay.go` | 用 `context.WithTimeout` 包装 `handler.Handle`，添加 `handlerTimeout` 参数到 `NewRelay` |
| `internal/outbox/repository.go` | 将 `FetchUnpublished` 更改为 `FOR UPDATE SKIP LOCKED`，添加 `leased_until` 列 |
| `internal/outbox/` + 迁移 | 将 `leased_until` 列 + 索引添加到 `outbox_events` |
| `internal/app/app.go` | 传递 `handlerTimeout` 到 `NewRelay` |

**阶段 1 的关键文件**：

| 文件 | 更改 |
|------|------|
| `internal/service/file.go` | 添加校验和唯一约束+捕获创建中的 `ErrDuplicate` |
| `internal/repository/file.go` | 添加 `FindByChecksumForUpdate` 或依赖唯一约束 |
| `migrations/0000XX_files_unique_checksum.up.sql` | `CREATE UNIQUE INDEX ... ON files(owner_uid, checksum)` |
| `internal/hub/hub.go` | 添加 `maxSubsPerUID` 和 `maxSubsGlobal` |
| `internal/service/notification.go` | 将 `rateLimiter` 更改为 `RateLimiter` 接口 |
| `internal/service/service.go` | 定义 `RateLimiter` 接口 |

---

## 总结

该代码库具有强大的架构基础：严格的单向依赖、组合根的安全接线、正确的事务出站传送以及安全的 Like/Unlike 实现。两个 P0 级风险——**上下文中继中缺少超时**和**缺失 `FOR UPDATE SKIP LOCKED`**——破坏了多实例和长时间运行部署的可靠性保证。下一个 P1 级问题——**文件去重竞争**和**SSE 资源耗尽**——是明确且可修复的。

该架构从早期/中期阶段的服务转向运营就绪系统的演变，需要在核心可靠性领域（阶段 0）以及数据生命周期管理（阶段 2）和可观察性（阶段 3）方面进行投资。抽象质量很高，足以支持这些更改，而无需大规模重写。

最具影响力的架构改进是**在出站中继中添加上下文超时**（影响：高，努力：中等）。这修复了一个真实的死锁场景，并开启了有意义的追踪和指标。
