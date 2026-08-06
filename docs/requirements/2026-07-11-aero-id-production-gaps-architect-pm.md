# aero-id — 资深架构师 / PM 视角：五个高价值扩展方向

> **日期:** 2026-07-11  
> **方法:** 全局代码扫描（`internal/` 全包、`migrations/` 22 份 SQL、`api/proto/`、`configs/`、`cmd/`、`deployments/`）  
> **约束:** 不编写任何代码。每个方向必须具有精确的代码级证据。
> **目标:** 在已有 110+ 份分析文档之后，提出当前代码库中**真实存在的、尚未被充分覆盖**的高价值扩展方向，每个方向包含 Why（为什么需要）、What（具体做什么）、Code Evidence（代码证据呈现）三部分。

---

## 执行摘要

aero-id 已经是一个非常成熟的双传输数据服务平台。以下五个方向不是"修补缺失功能"，而是在现有架构基础上**进入下一产品阶段**所需的扩展点。每个方向都对应一个当前代码中**有明确证据但未被解决**的架构缺口。

---

## 方向一：公共/匿名只读访问（Public Anonymous Read Access）

### 为什么需要

当前所有 `/v1/*` 数据接口都要求 Bearer token 鉴权（通过 SSO 验证）。唯一的公共路径是 HMAC 签名的文件下载（`GET /files/dl`）。这意味着：

- 用户不能把一个评论讨论页（比如文件下的评论区）作为公开链接分享给没有 aero-id 账户的人
- 不能构建公开的文件画廊/作品集页面
- 不能实现类似 Disqus 的嵌入评论区（第三方网站嵌入需要匿名读权限）
- **架构上，整个数据层对匿名流量完全封闭**，从根本上限制了产品的社交传播能力

### 代码证据

**证据 1：路由层强制所有数据接口鉴权**

```go
// internal/handler/http/router.go:76-79
routes := Routes{
    Public: e.Group("", publicMW...),       // 只有 /files/dl、/healthz、/readyz、/metrics
    Auth:   e.Group("/v1", authMW...),      // 所有 /v1/* 强约束
}
```

`Public` 组目前**没有任何数据路由**——它只服务于基础设施端点。`Auth` 组 (`/v1/*`) 的第一层中间件就是 `middleware.Auth()`，缺失 token 即返回 401。

**证据 2：Service 层无条件要求 Actor**

```go
// internal/domain/actor.go:18-23
func MustActor(ctx context.Context) (string, error) {
    uid, ok := ActorFrom(ctx)
    if !ok {
        return "", apperr.Unauthorized("auth.unauthorized", "missing authenticated actor")
    }
    return uid, nil
}
```

每个 service 方法的入口都调用 `MustActor`——没有例外路径允许匿名访问。例如：

```go
// internal/file/service_crud.go:18 — Get 方法
func (s *fileService) Get(ctx context.Context, id domain.ID) (*File, error) {
    actor, err := domain.MustActor(ctx)  // ← 匿名请求在这里被拒绝
    ...
    allowed, err := s.canRead(ctx, actor, f) // ← 权限检查也只作用于已鉴权的用户
}
```

**证据 3：File 的 Visibility 枚举只有 Private/Public，但 Public 未提供匿名读取路径**

```go
// internal/domain/shared.go:30-33
const (
    VisibilityPrivate Visibility = 0  // 仅 owner
    VisibilityPublic  Visibility = 1  // 所有人（但现实：仍需 token 才能读到）
)
```

`VisibilityPublic` 的语义是"所有人可读"，但服务层实际上仍然要求 actor——public 文件实际上只对"持有有效 token 的任意用户"可见，而非真正的匿名可读。

**证据 4：Comment 完全没有面向公共的读取端点**

Comments 通过 `target_type` + `target_id` 关联到文件或消息。即使文件是 public，它的评论区也无法匿名访问——`ListComments` 是 `/v1/comments` 下的端点，被 `authMW` 保护。

### 建议方向

- 引入 `Scope` 概念：为每个实体（文件、评论、消息）增加 `read_scope` 字段（`private` / `authenticated` / `public`）
- 在 Service 层引入 `optionalActor` 读取路径：当 context 中没有 actor 时，只允许访问 `read_scope = public` 的资源
- 在 Router 层为公开读取端点创建新的路由组（例如 `/public/v1/comments`），使用轻量级的 `optionalAuth` 中间件（有 token 则解析 uid，无 token 则以匿名身份继续）
- HTTP handler 和 gRPC handler 需新增区分对待：公共端点在 response 中不暴露敏感字段（如 owner_uid 对匿名用户不可见）

---

## 方向二：Outbox 死信管理（Dead Letter Observability & Recovery）

### 为什么需要

Outbox 是 aero-id 异步处理的核心基础设施——通知分发、webhook 投递都依赖它。当事件处理失败超过 `maxRetries`（默认 20 次，约 4.3 小时）时，事件被标记为 `dead_letter`，**之后永远不再被 relay 拾取**。当前没有任何机制来：

- 发现死信的存在（无告警、无指标区分死信与普通待发布事件）
- 查看死信的内容（payload 信息完全不可见）
- 手动重试死信（修复下游问题后无法重新投递）
- 清理过期的死信（死信行在表中永久累积）

这在生产环境中意味着：一次下游故障 → 一堆事件永久丢失 → 操作员完全不知情。

### 代码证据

**证据 1：死信只有标记，没有任何管理 API**

```go
// internal/outbox/repository.go:93-108
func (r *GormRepository) MarkFailed(ctx context.Context, id uuid.UUID, retryCount int, maxRetries int, at time.Time) error {
    updates := map[string]any{
        "retry_count":   retryCount,
        "last_retry_at": at,
    }
    if retryCount >= maxRetries {
        updates["dead_letter"] = true  // ← 唯一动作：打标
    }
    // ...
}
```

没有任何对应的 `ListDeadLetters`、`ReplayDeadLetter`、`DeleteDeadLetter` API。`FetchUnpublished` 明确排除了死信：

```go
// internal/outbox/repository.go:65-78
func (r *GormRepository) FetchUnpublished(ctx context.Context, limit int) ([]*Event, error) {
    // ...
    .Where("published_at IS NULL AND NOT dead_letter")  // ← 死信被永久忽略
    ...
}
```

**证据 2：死信数量无监控指标**

```go
// internal/outbox/relay.go:127-137
func (r *Relay) drain(ctx context.Context) int {
    // ...
    events, err := r.repo.FetchUnpublished(ctx, r.batch)
    // ...
    r.observeDrain(len(events), oldestAge, start) // ← 只观察"待处理"事件数
    // ...
}
```

`MetricsSink` 的 `ObserveOutboxDrain` 只报告 `pending`（当前批次中的待发布事件数），**不报告死信总数**。一个死信堆积的场景下，`pending` 可能始终为 0（因为没有新的待发布事件），操作员完全无感。

**证据 3：死信行永久累积，无清理机制**

`outbox_events` 表没有 TTL 或清理策略。已发布的 (`published_at IS NOT NULL`) 和死信 (`dead_letter = true`) 的行永远不被删除。`migrations/` 中的 22 份迁移没有任何 `outbox_events` 的清理或归档语句。

### 建议方向

- 在 `MetricsSink` 中增加 `ObserveDeadLetterCount(int)` 指标，让操作员可以监控死信堆积
- 为 `outbox.Repository` 增加 `ListDeadLetters(ctx, page)` 和 `CountDeadLetters(ctx) (int, error)` 方法
- 在 Outbox relay 中增加 `ReplayDeadLetter(ctx, eventID) error` 方法：将事件重新标记为未发布（`dead_letter = false, published_at = NULL, retry_count = 0`）
- 提供 HTTP/gRPC 管理端点来查询和重试死信（挂在 `/v1/admin/dead-letters` 下）
- 增加可配置的保留策略：已发布事件和死信的自动清理（基于创建时间）

---

## 方向三：跨资源引用完整性（Cross-Resource Referential Integrity on Soft Delete）

### 为什么需要

aero-id 的实体之间存在隐式的引用关系：
- `messages.file_id` → 引用 `files` 表中的文件
- `comments` 通过 `target_type`/`target_id` 多态引用 `files` 或 `messages`
- `message_reactions` 引用 `messages`（通过 `message_id`）

当前删除操作只做**软删除**，并且只清理被删除实体自身的存储资源。**引用者完全不感知目标已被删除**，导致：

1. 一条消息引用的文件被删除后，消息仍然显示文件 ID，但无法下载——用户看到的是"死链"
2. 一个文件下的评论区在文件被删除后仍然可以被列出（文件已不存在，但评论仍然返回）
3. 一条消息被删除后，它的 reactions 仍然存在（虽然 reactions 原本就只是 audit trail，但无意义的 orphan 行在累积）

### 代码证据

**证据 1：文件删除不清理引用它的消息**

```go
// internal/file/service_crud.go:62-82
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    actor, err := domain.MustActor(ctx)
    // ...
    if err := s.files.Delete(ctx, id); err != nil {  // ← 只做了文件的软删除
        return ...
    }
    // 只检查 storage_key 是否需要清理
    if n, cerr := s.files.CountByStorageKey(ctx, f.StorageKey); cerr == nil && n == 0 {
        _ = s.store.Delete(ctx, f.StorageKey)
    }
    // ← 没有检查或清理引用这个文件的消息
    // ← 没有检查或清理挂在这个文件下的评论
    return nil
}
```

**证据 2：消息删除不清理 reactions 和引用**

```go
// internal/message/service.go — 软删除
func (s *messageService) Delete(ctx context.Context, id domain.ID) error {
    // ...
    m.DeletedAt = gorm.DeletedAt{Time: time.Now().UTC(), Valid: true}
    if err := s.msgs.SoftDelete(ctx, m.ID); err != nil {  // ← 只软删消息行
        return ...
    }
    // ← 没有清理 message_reactions
    // ← 没有清理挂在这个消息下的评论
    return nil
}
```

**证据 3：评论的多态引用完全无 FK 约束**

```go
// internal/domain/comment.go
type Comment struct {
    // ...
    TargetType TargetType  // "file" 或 "message"
    TargetID   ID          // 被引用实体的 ID
    // ...
}
```

`target_type` + `target_id` 是纯逻辑引用——数据库上没有 FK，repository 层在 ListByTarget 查询时不验证目标实体是否仍然存在：

```go
// internal/repository/comment.go — ListByTarget
func (r *commentRepo) ListByTarget(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) ([]*domain.Comment, string, error) {
    // ← 只按 target_type + target_id 查询，不 JOIN files/messages 验证存在性
    q := r.db.WithContext(ctx).Where("target_type = ? AND target_id = ? AND status = ?", t, target, domain.CommentStatusNormal)
    // ...
}
```

### 建议方向

- 在 Service 层的 `File.Delete` 中增加 `PostDelete` 钩子：级联清理引用该文件的消息中的 `file_id`（设置为 NULL），并可选将挂在该文件下的评论的 `status` 切换为 `CommentStatusHidden`
- 在 Service 层的 `Message.SoftDelete` 中增加 `PostDelete` 钩子：级联清理 `message_reactions`（硬删除），并隐藏引用该消息的评论
- 在 `comments` 的 `ListByTarget` 查询中增加对目标实体存在性的验证（当 `target_type = "file"` 时 JOIN files 检查 `deleted_at IS NULL`）
- 对于多态引用，考虑引入一个软性的 "引用健康度" 后台扫描 + 报告

---

## 方向四：配置热重载与动态调优（Hot Reload & Dynamic Configuration）

### 为什么需要

当前所有配置项在进程启动时通过 `config.Load()` 一次性加载，此后任何变更都需要**完全重启服务**。在生产场景中，以下操作需要重启整个双传输服务：

- 调整限流参数（RPS / Burst）以应对流量高峰
- 切换日志级别（debug ↔ info）以排查问题
- 更新 SSO 端点或证书（蓝绿部署期间的渐进切换）
- 开启/关闭 tracing 采样
- 调大/调小 outbox poll interval

这是不必要的运维负担，也不符合云原生 12-Factor 的"通过环境配置"升级为"通过管理 API 动态调整"的生产成熟度要求。

### 代码证据

**证据 1：Config 在启动时一次性加载，无更新路径**

```go
// internal/app/app.go:50-58
func New(ctx context.Context, cfg *config.Config, log *zap.Logger) (*App, error) {
    // cfg 是 *config.Config，由 main.go 在调用 New 之前加载一次
    // 此后 cfg 永远不会被更新
    ...
    limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
    // ↑ 一旦创建，rate 和 burst 就固定了
    ...
}
```

**证据 2：Rate Limiter 的 rate 和 burst 是创建时固定的**

```go
// internal/pkg/ratelimit/ratelimit.go:39-49
type Limiter struct {
    rate  float64       // 创建后不可变
    burst float64       // 创建后不可变
    // ...
}
func New(rate float64, burst int, idleTTL time.Duration) *Limiter {
    return &Limiter{rate: rate, burst: float64(burst), ...}
}
```

没有 `SetRate(newRPS float64)` 或 `SetBurst(newBurst int)` 方法。调整限流参数的唯一方法是重启。

**证据 3：Logger 级别在启动时固定**

```go
// internal/pkg/logger/logger.go
func New(cfg LogConfig) (*zap.Logger, error) {
    // 在启动时根据 cfg.Level 构建，不提供运行时调整级别的接口
    ...
}
```

`zap.Logger` 支持运行时 `lvl.SetLevel()`，但 aero-id 没有暴露这个能力——没有管理端点，没有信号处理器。

**证据 4：Tracing 启用/禁用需要重启**

```go
// internal/app/app.go
traceShutdown, err := tracing.Init(ctx, tracing.Config{
    Enabled:     cfg.Tracing.Enabled,    // ← 启动后不可变
    SampleRatio: cfg.Tracing.SampleRatio, // ← 启动后不可变
})
```

生产环境中，通常希望在不重启的情况下临时开启 tracing 采样以诊断一个慢请求。

### 建议方向

- 为 `rate.Limiter` 增加动态调参方法（`SetRate`、`SetBurst`），限流中间件/拦截器持有 `*Limiter` 引用即可生效
- 增加 `/v1/admin/config` 端点（需 `system.config` 权限），允许 GET 当前运行配置、PATCH 可热更新的参数子集
- 提供一个 `SIGHUP` 信号处理器（或每 30s 的 re-read goroutine），重新加载配置文件并应用变更
- 为 tracer 和 logger 增加运行时级别的 setter 方法

---

## 方向五：通知传递可靠性——SSE 之后的生命周期（Notification Delivery Beyond SSE）

### 为什么需要

当前的通知管道是：outbox → dispatcher → 数据库行 → SSE 推送。这里存在三个可靠性缺口：

1. **SSE 推送是尽力而为的**：`hub.Hub` 的 channel buffer 为 16 条，满时直接 drop（`select default: break`）。如果用户的 SSE 连接因为网络抖动断开 15 秒，期间产生的通知就永远丢失了——虽然通知行已经写入数据库，但用户**没有被实时告知**，只能等下次轮询列表时才看到。
2. **无离线推送**：SSE 要求客户端保持长连接。移动端 APP 或浏览器标签页关闭后，用户完全收不到通知。没有 FCM/APNs 推送、没有邮件摘要、没有 SMS。
3. **无通知重试**：dispatcher 的 `Handle` 方法成功后，通知行已创建、SSE 已推送（尝试），**没有任何后续检查**来确保用户真的看到了通知。

### 代码证据

**证据 1：SSE 推送是非阻塞的，慢消费者直接丢**

```go
// internal/hub/hub.go:81-93
func (h *Hub) pub(uid string, evt SSEEvent) {
    // ...
    us.mu.Lock()
    defer us.mu.Unlock()
    for _, ch := range us.subs {
        select {
        case ch <- evt:
        default:           // ← buffer 满直接丢弃，没有重试
            // subscriber is too slow; drop rather than block
        }
    }
}
```

**证据 2：Dispatcher 的 Handle 成功返回后，通知生命周期就结束了**

```go
// internal/notification/dispatcher.go:133-158
func (d *NotificationDispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    n := d.notificationFor(e)
    // ... block check, preference check, rate limit ...
    if err := d.notifs.Create(ctx, n); err != nil {    // ← DB 写入成功
        return err
    }
    // SSE 推送（非阻塞，不检查结果）
    if d.pusher != nil {
        payload, _ := json.Marshal(n)
        d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
    }
    return nil // ← 返回 nil 意味着"通知已成功投递"，outbox 事件标记为 published
}
```

一旦 outbox 事件被标记为 `published`，**无论用户是否收到了 SSE 推送**，这条通知都不会再被处理。没有"重新推送未送达通知"的机制。

**证据 3：Notifications 表没有 delivery_status 字段**

```go
// internal/domain/notification.go
type Notification struct {
    ID             ID
    RecipientUID   string
    Type           string
    ActorUID       string
    SubjectType    string
    SubjectID      string
    ReadAt         *time.Time
    CollapsedCount int
    CreatedAt      time.Time
}
```

没有 `delivery_status`（`pending`/`pushed`/`failed`/`delivered`）、`push_attempts`、`last_push_at` 等字段。系统无法区分"用户已读"和"从未收到"。

**证据 4：SSE last_event_id 恢复有 50 条上限**

```go
// internal/handler/http/events.go:73
h.replayMissed(c, uid, lastEventID)

// events.go:103
missed, err := h.fetcher.ListSince(c.Request.Context(), uid, since, 50) // ← 硬编码 50
```

如果用户离线时间较长、产生了超过 50 条通知，则前 N-50 条将完全丢失（`ListSince` 不会分页，客户端也不会请求更多）。

### 建议方向

- 为通知增加 `delivery_status` 列，初始为 `pending`，SSE 推送成功后更新为 `pushed`（或通过客户端 ACK 标记为 `delivered`）
- 添加通知推送重试循环：创建一个 `notification-pusher` 后台工作者，定期扫描 `delivery_status != 'delivered' AND created_at > recent` 的通知并重新推送
- 实现 SSE ACK 协议：客户端发送 `{"action": "ack", "notification_id": "..."}`，服务端标记为 `delivered`
- 将离线推送集成点抽象为一个 `PushGateway` interface（支持 SSE + 可选的 FCM/APNs/邮件适配器）
- 移除 `ListSince` 的硬编码 50 上限，改用分页游标（标准 `Page` 模式），确保离线重连时可以恢复所有遗漏的通知

---

## 优先顺序建议

| # | 方向 | 类型 | 优先级 | 工作量 | 理由 |
|---|------|------|--------|--------|------|
| 1 | **公共匿名只读访问** | 产品/架构 | **P1** | 中 | 直接影响社交传播和第三方集成能力；限制小（不影响现有鉴权路径） |
| 2 | **配置热重载** | 运维/基础设施 | **P1** | 低-中 | 消除重启需求，提升生产灵活性；限流器热调参可即时应对流量突发 |
| 3 | **通知推送可靠性** | 产品/可靠性 | **P1** | 中-高 | 用户体验最大痛点：丢失通知 = 丢失用户信任 |
| 4 | **Outbox 死信管理** | 运维/可靠性 | **P2** | 低 | 生产必需要有，但当前缺乏不会导致数据丢失（仅丢失异步通知） |
| 5 | **跨资源引用完整性** | 数据质量/架构 | **P2** | 中 | 长期正确性需要，但短期内数据软删除本身不会导致功能错误 |

---

## 与现有能力的关系

| 方向 | 依赖哪些已有能力 | 受益哪些已有能力 |
|------|-----------------|------------------|
| 公共匿名只读 | middleware/auth.go、service 层 actor 模式 | 文件 visibility 枚举、share 系统 |
| 配置热重载 | config 层、rate limiter、zap logger | 管理角色/权限系统、audit log |
| 通知推送可靠性 | outbox、notification、hub/SSE | dispatcher、notification preference |
| Outbox 死信管理 | outbox relay、repository、metrics | admin 角色、audit log |
| 跨资源引用完整性 | 软删除、storage_refs | storage GC、audit log |
