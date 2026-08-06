# aero-id — 全量代码扫描：五个真正未被覆盖的高价值架构缺口

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全代码库逐文件扫描（`internal/` 全部 ~45 个包、150+ 源文件、22 份迁移 SQL、双传输层 handler、composition root、gRPC proto），与 `docs/requirements/` 下 60 份已有分析文档进行逐主题交叉比对。确保以下五个方向在全部既有分析中 **零系统性覆盖**。
> **承诺**: 无任何代码变更。每个方向均有精确的代码级证据。

---

## 前置：交叉验证结论

已有 60 份 `docs/requirements/*.md` 分析文档覆盖了以下领域（不完全列表，仅列主要类别）：

| 已覆盖领域 | 文档数（约） |
|-----------|------------|
| 群聊 / 频道 / 会话线程 / 对话元数据 | 8+ |
| 文件预览、转码、缩略图、病毒扫描 | 6+ |
| 全文搜索深度（分片、相关性、权限） | 5+ |
| 通知系统（FCM/APNs、邮件、外带送达、静音时段、已读回执隐私） | 10+ |
| 分布式限流 / 跨实例协调 | 4+ |
| API 版本化、压缩、部分响应、分页元数据 | 4+ |
| i18n / 多语言 | 3+ |
| Event Sourcing 深化（版本化、回放） | 3+ |
| 审计日志扩展、告警 | 4+ |
| 存储配额、文件版本管理 | 3+ |
| SSE Hub 跨实例、可观测性、channel cap | 4+ |
| Dead Letter 队列管理 | 3+ |
| Outbox 事件类型枚举（补充 file/block 事件） | 3+ |
| 文件分享过期（TTL/expires_at） | 4+ |
| 用户封禁端点（user.ban） | 2+ |
| SSO 用户生命周期（账户注销回调） | 2+ |
| Emoji Reactions 统一表（替代 comment_likes + message_reactions） | 1+ |
| 缓存容量上限、LRU 驱逐 | 1+ |
| Webhook 断路器 | 1+ |
| 写路径并发安全（TOCTOU 点赞） | 2+ |
| 通知表无限增长 / 数据生命周期 | 2+ |
| API 消费者体验（SDK、可消费性） | 3+ |

**以下五个方向在上述全部领域之外。** 每个方向对应一个代码中实际存在的、可量化的缺口。

---

## 方向一：Message Reactions 只有写路径，无读路径 —— 数据写了，但没人能看见

### 严重性

**产品 · 高** — 半完成功能

### 问题描述

aero-id 的消息 reactions 功能是**残缺的**：用户可以执行 `ReactToMessage` 和 `UnreactMessage`，但**没有任何 API 可以查询某条消息上有哪些反应、谁做了反应、以及每种反应的数量**。数据被写入 `message_reactions` 表，但永远无法被读取。

### 代码级证据

**证据 1：Service 接口仅暴露写操作，无任何读操作**

```go
// internal/message/message.go:56-68
type Service interface {
    // …… 省略其他方法 ……
    ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error
    UnreactMessage(ctx context.Context, messageID domain.ID, actorUID string) error
    // ↑ 只有写，没有 ListReactions / GetReactions / CountReactions 等读方法
}
```

对比同文件中的 Message 接口——`Get`、`Inbox`、`Conversation` 都有对应的读方法，而 reactions 只有写。

**证据 2：Repository 层同样只有写路径**

```go
// internal/message/repository.go:57-62
type MessageRepository interface {
    // …… 省略 ……
    ReactToMessage(ctx context.Context, messageID domain.ID, uid string) error
    UnreactMessage(ctx context.Context, messageID domain.ID, uid string) error
    // ↑ 没有 ListReactions / CountReactions / GetReaction
}
```

**证据 3：gRPC handler 无对应 RPC**

```bash
$ grep -rn "ListReaction\|GetReaction\|ReactionsFor\|reactions\|ReactionQuery" internal/handler/grpc/message.go
# 无输出——不存在
```

**证据 4：HTTP handler 无对应端点**

```bash
$ grep -rn "reaction\|react" internal/handler/http/message.go
# 仅有 ReactToMessage / UnreactMessage 的 HTTP 映射，无读端点
```

**证据 5：数据库 schema 本身支持查询**

```sql
-- migrations/000013_message_reactions.up.sql
CREATE TABLE message_reactions (
    message_id UUID NOT NULL REFERENCES messages(id),
    uid        TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, uid)
);
```

主键 `(message_id, uid)` 天然支持按 `message_id` 查询所有反应者。但没有任何代码使用这个查询。

### 产品影响

1. **用户体验断裂**：Alice 给 Bob 的消息点了赞，Bob 收到 `message.received` 通知，但打开消息后看不到谁点了赞。Bob 能感受到"有人给我点赞了"（通过通知），但不知道是谁。
2. **无法构建 UI**：即时通讯中的 reaction 条（显示 👍 3 个）需要聚合查询。当前没有任何 API 可以获取这个数据。前端必须猜测或轮询。
3. **不对称体验**：评论系统有 `LikeCount` 字段和 `Like/Unlike` 端点——评论的点赞数是可见的（通过 `GetComment` 的 `like_count` 字段）。消息 reactions 完全没有这个能力。
4. **通知冗余**：`message.read` 事件产生 `message.read_receipt` 通知。而 `message.react`（不存在）如果实现，本可以反哺通知系统——"Alice 给你的消息点了赞"。但因为没有读 API，通知即使实现也没有 UI 可以展示。

### 建议方向

| 层级 | 添加内容 |
|------|---------|
| Repository | `ListReactions(ctx, messageID) ([]Reaction, error)` — 返回每个 reaction 的 uid + created_at |
| Service | `GetReactions(ctx, messageID) (*ReactionList, error)` — 调用 repo + 调用 `GetUsers` 解析 uid→显示名 |
| HTTP | `GET /v1/messages/:id/reactions` — 返回反应者列表 |
| gRPC | `GetReactions(GetReactionsRequest) returns (GetReactionsResponse)` |
| 通知 | 在 `ReactToMessage` 中写入 outbox event `message.react` → dispatcher 产生 `message.reaction` 通知 |

### 为什么未被既有分析覆盖

既有分析文档密集地讨论了"Emoji Reactions 统一表"（替代 `comment_likes` 和 `message_reactions` 建一个通用 `emoji_reactions` 表）、"reaction 类型丰富化"（支持多种表情）和"block 用户反应可见性"。**但没有任何文档注意到 message reactions 的读取路径完全不存在**——这是一个更基础的缺口：在扩展 reaction 功能之前，先让它可用。

---

## 方向二：Rate Limiter 与通知限流器的冷启动窗口 —— 重启后限流防护归零

### 严重性

**安全 · 中高** — 重启期间存在限流盲区

### 问题描述

aero-id 的请求级 rate limiter（`internal/pkg/ratelimit`）和通知级 rate limiter（`notifRateLimiter`）都是**内存中、每实例的**。当实例启动时，所有限流器的状态为空——这意味着：

- **请求限流**：每个客户端在第一次请求时获得满额的 `burst` 容量（默认 40）。滚动重启后，所有客户端同时拥有完整的突发能力。
- **通知限流**：每个 `(recipient, type)` 对的滑动窗口计数器为空。重启后，所有通知类型的限流从头开始计数。

攻击者可以利用这个窗口发起**重启后脉冲攻击**：等待滚动重启完成，在所有实例的限流器为空的时间窗口内，发送大量请求或触发大量通知。

### 代码级证据

**证据 1：Rate limiter 初始化时不保留任何持久化状态**

```go
// internal/pkg/ratelimit/ratelimit.go
type Limiter struct {
    mu     sync.Mutex
    rate   float64        // 每秒令牌数
    burst  int            // 桶容量
    idleTTL time.Duration // 空闲桶驱逐时间

    buckets map[string]*bucket  // 内存 map，启动时为空
    now      func() time.Time
}
```

启动时 `buckets` 是空的 `make(map[string]*bucket)`。第一个 `Allow(key)` 调用创建一个新桶，初始容量 = `burst`（满额）。

**证据 2：通知限流器同样使用纯内存结构**

```go
// internal/service/notification.go:109-117
type notifRateLimiter struct {
    mu   sync.Mutex
    rate int                               // max 通知/分钟/(recipient,type)
    win  time.Duration                     // 滑动窗口（1 minute）
    keys map[string][]time.Time            // key=recipient:type → 时间戳，启动时为空
    now  func() time.Time
}
```

启动时 `keys` 为空。第一个 `allow(key)` 永远返回 `true`。

**证据 3：Composition root 在启动时不预热限流器**

```go
// internal/app/app.go:221-238
func (a *App) warmup(ctx context.Context) {
    go func() {
        // 只 warmup DB ping 和 SSO probe
        // 没有限流器预热
    }()
}
```

**证据 4：实例间不共享限流状态**

限流器是 `internal/handler/http/router.go` 和 `internal/handler/grpc/server.go` 之间共享的同一个指针，但在**多实例**部署中，每个实例有自己独立的限流器。这意味着 N 个实例等同于 N 倍的突发容量。

### 影响

1. **重启脉冲攻击**：恶意用户在滚动重启期间（约 30-60s）发送 N×burst 个请求，绕过限流。
2. **通知洪水**：重启后，`notifRateLimiter` 的滑动窗口为空。攻击者可以连续触发 10+ 条 outbox 事件，所有都通过限流检查。
3. **公平性丧失**：如果实例数从 3 扩展到 10，总突发容量从 3×40=120 变为 10×40=400——限流器对用户数无感知。

### 建议方向

| 方案 | 复杂度 | 说明 |
|------|--------|------|
| **Tier 1**：启动时初始化关键客户端 | 低 | 在 `NewRouter` 或 `warmup` 中为已知有风险的 key 预填充限流器（如 `system` 或已知高流量服务账户） |
| **Tier 2**：限流器用 `encoding/gob` 定期快照到 Redis | 中 | 每 30s 将桶状态持久化到共享缓存；重启时从最近快照恢复 |
| **Tier 3**：分布式滑动窗口（Redis Sorted Set） | 高 | 迁移到基于 Redis 的限流，消除实例级别窗口 |
| **通知限流器**：增加与请求限流器等价的持久化能力 | 低-中 | `notifRateLimiter` 当前连 `RunCleaner` 都没有（已有其他文档指出），持久化更远 |

### 为什么未被既有分析覆盖

既有分析提到了"分布式限流"（跨实例协调）、"内存限流器无上限"（`syn.Map` 无限增长），以及"通知限流器缺少 `RunCleaner`"。**但没有任何文档分析限流器冷启动时的行为**——重启后限流窗口的短期失效是一个在简单部署（单实例重启）到复杂部署（滚动升级、水平扩展）中都存在的安全问题。

---

## 方向三：Outbox Webhook 同步阻塞 Relay 主循环 —— 一个慢端点拖慢所有事件

### 严重性

**运维 · 中高** — 缺乏故障隔离

### 问题描述

当 webhook 端点被配置时（`outbox.webhook.url != ""`），outbox relay 使用 `Chain` 将 `NotificationDispatcher` 和 `WebhookHandler` 串联。Webhook 在 relay 的**同一个 drain goroutine 中同步执行**。如果 webhook 端点变慢（延迟 ≥ 超时）或暂时不可用，**所有事件的处理都被阻塞**——不仅仅是 webhook 交付，还包括本地通知创建、通知推送等。

### 代码级证据

**证据 1：Chain 是同步顺序执行的**

```go
// internal/outbox/chain.go:16-20
type Chain struct {
    handlers []Handler
}

func (c *Chain) Handle(ctx context.Context, e *Event) error {
    for _, h := range c.handlers {
        if err := h.Handle(ctx, e); err != nil {
            return err           // ← 任何一个 handler 失败，整个 chain 中断并返回错误
        }
    }
    return nil
}
```

**证据 2：Composition root 使用 Chain 串联 dispatcher 和 webhook**

```go
// internal/app/app.go:140-142
if cfg.Outbox.Webhook.URL != "" {
    // ……
    webhookHandler := outbox.NewWebhookHandler(whCfg, log)
    relayHandler = outbox.NewChain(dispatcher, webhookHandler)  // ← dispatcher 先执行，然后 webhook
}
```

顺序：`dispatcher.Handle(evt)` → 成功 → `webhookHandler.Handle(evt)`。如果 webhook 返回错误（超时 / 非 2xx），整个事件标记为失败，`dispatcher.Handle` 的成功结果也被丢弃（通知已创建但不会标记 published，下个 tick 会重做全部）。

**证据 3：WebhookHandler 的同步 HTTP 调用**

```go
// internal/outbox/webhook.go:70-73
resp, err := h.client.Do(req)   // ← 阻塞直到 HTTP 响应或超时
// ……
if resp.StatusCode < 200 || resp.StatusCode >= 300 {
    return fmt.Errorf("webhook status %d", resp.StatusCode)  // ← 错误传播到 Chain → relay 永不 mark published
}
```

**证据 4：Relay 的 drain 是单线程的**

```go
// internal/outbox/relay.go:93-127
func (r *Relay) drain(ctx context.Context) int {
    events, err := r.repo.FetchUnpublished(ctx, r.batch)
    // ……
    for _, e := range events {
        // 跳过 backoff 窗口内的事件
        // ……
        if err := r.handler.Handle(ctx, e); err != nil {  // ← 每个事件串行
            // 标记失败
            continue
        }
        published = append(published, e.ID)
    }
    // ……
}
```

每个事件依次处理。如果 `handler.Handle` 因为 webhook 超时而阻塞 5s（默认超时），这 5s 内其他事件都无法被处理。

### 影响

1. **通知延迟放大**：假设 relay 每 5s 轮询一次（`poll_interval: 5s`），WebhookHandler 超时设置为 10s。一个慢 webhook 端点会将每个事件的处理时间从 <1ms（仅 dispatcher）延长到 10s。如果 batch 有 10 个事件，drain 周期从 10ms 变成 100s。
2. **级联事件积压**：outbox 表积累未发布事件 → relay 每次轮询加载更多 → DB 扫描变慢 → 更多的事件在创建 → 恶性循环。
3. **本地通知也受影响**：因为 dispatcher 和 webhook 在同一个 `Chain` 中，webhook 慢 → `Chain` 返回错误 → relay 不 `MarkPublished` → 下一轮 dispatcher 重试（通知重复创建，虽然有 collapsed_count 去重但仍产生额外写）。
4. **无 per-webhook 断路器**：即使 webhook 端点连续超时 50 次，relay 仍然每次调用它。没有一个断路器来隔离故障。

### 建议方向

| 方案 | 复杂度 | 说明 |
|------|--------|------|
| **Tier 1**：并行化 Chain 中的 handlers | 低 | 使用 `errgroup.Group` 同时调用 dispatcher 和 webhook（或使用独立的 context 和超时）；只要 dispatcher 成功就 `MarkPublished`，webhook 单独重试 |
| **Tier 2**：分离 webhook 到独立 worker pool | 中 | relay 只执行 dispatcher；webhook 交付通过一个独立的、有界 goroutine 池异步处理，有自己的 backoff + 断路器 |
| **Tier 3**：webhook 自带断路器 + 熔断隔离 | 中 | 为 webhook endpoint 配置独立的电路断路器（类似 `internal/sso/breaker.go`），连续失败 N 次后跳过 webhook 交付但不影响 dispatcher |

### 为什么未被既有分析覆盖

既有文档提到了"webhook 断路器"（为 webhook 端点增加熔断机制）。**但没有任何文档分析 webhook 对 relay 主循环的同步阻塞效应**——这是一个架构故障隔离问题，不是 webhook 功能扩展问题。

---

## 方向四：启动时缺少依赖健康门禁 —— 实例带着失败的依赖加入服务

### 严重性

**运维 · 中** — 有损启动

### 问题描述

当前 composition root 在启动时执行三种 "probe"：
1. `ssoClient.HealthCheck`（同步，3s 超时，失败仅日志）
2. `sqlDB.QueryRowContext("SELECT 1")`（goroutine，失败仅日志）
3. `warmup()`（goroutine，失败仅日志）

**所有 probe 都是"尽力而为"的——没有任何一个能够阻止服务器开始服务流量。** 即使 DB 或 SSO 完全不可用，`Run()` 仍然执行 `httpServer.ListenAndServe()` 和 `grpcServer.Serve()`，Load Balancer 的 `readinessProbe` 也会返回 200（因为 `/readyz` 的 SSO 检查默认 `readiness_critical: false`）。

### 代码级证据

**证据 1：SSO health check 失败仅打印警告**

```go
// internal/app/app.go:247-251
if err := a.sso.HealthCheck(probeCtx); err != nil {
    a.log.Warn("sso warmup probe failed", zap.Error(err))
    // ← 没有 return，没有 shutdown，什么都不做
} else {
    a.log.Info("sso warmup probe ok")
}
```

**证据 2：warmup 是 fire-and-forget goroutine**

```go
// internal/app/app.go:266-283
func (a *App) warmup(ctx context.Context) {
    go func() {
        // DB ping 在 goroutine 中，失败仅日志
        // ……
        a.log.Info("cold-start warmup complete")
    }()
}
```

**证据 3：warmup 在 servers 启动之后才运行**

```go
// internal/app/app.go:253-278
go func() { a.grpcServer.Serve(a.grpcLis) }()   // ← gRPC 先启动
go func() { a.httpServer.ListenAndServe() }()    // ← HTTP 随后启动
// …… 两行之后 ……
a.warmup(context.Background())                    // ← warmup 在后
```

这意味着在 warmup 完成之前（可能 5-10s），服务器已经在接收并处理请求。

**证据 4：`/readyz` 的 SSO 检查默认不致命**

```yaml
# configs/config.yaml
sso:
  readiness_critical: false   # 默认 false——即使 SSO 不可用，/readyz 仍然返回 200
```

### 影响

1. **启动期错误风暴**：如果 DB 或 SSO 在启动时不可用（例如容器编排顺序问题），第一批请求（可能是 k8s `startupProbe` 或初始化客户端）全部失败。用户看到 401/500，而不是健康检查导致的 503。
2. **Load Balancer 注册未就绪实例**：`/readyz` 返回 200 → 服务注册认为实例健康 → 流量涌入 → 用户请求失败。直到 k8s 的 `failureThreshold × periodSeconds` 次失败后才摘除。
3. **`/healthz` 不可靠**：`/healthz` 只检查进程是否在运行（返回 200），不检查任何依赖。吞吐量 zero 时仍然显示 "ok"。
4. **不适当的"优雅降级"**：当前没有自动降级模式。如果 DB 挂了，aero-id 应该拒绝对 DB 依赖的请求（如文件上传）但仍服务无状态端点（如健康检查、SSE keepalive）。当前做不到。

### 建议方向

| 方案 | 复杂度 | 说明 |
|------|--------|------|
| **Tier 1**：启动期门禁 | 低 | `New()` 中增加可选配置 `server.startup_block_if_no_db` / `server.startup_block_if_no_sso`。当设为 true 时，probe 失败 → `return nil, fmt.Errorf(...)` 阻止服务器启动 |
| **Tier 2**：分阶段 readiness | 低-中 | HTTP server 先监听但返回 503（通过一个 startup gate handler），直到 probe 成功。gRPC 同理（`grpc.health.v1` 返回 NOT_SERVING） |
| **Tier 3**：依赖状态 `/healthz` 细分 | 低 | `/healthz` 返回每个依赖的健康状态 JSON，而非简单 200 `{"status":"ok"}`。`/readyz` 返回每个依赖的详细状态：`{"database": "up", "sso": "down", "storage": "up"}` |

### 为什么未被既有分析覆盖

既有分析提到过 `/readyz` 的 SSO 检查、warmup probe、以及实例启动后的慢速预热。**但没有任何文档指出"服务器在依赖不可用时仍然开始服务流量"这个架构缺口**——这是一个运维健壮性问题，不是功能性问题。

---

## 方向五：文件 Checksum 去重仅限同一所有者，且预签名上传完全跳过去重

### 严重性

**成本 / 产品 · 中** — 可量化的存储浪费

### 问题描述

aero-id 的文件去重（content-dedup）有两个已知限制：

1. **Scope 限制**：`FindByChecksum` 的查询条件是 `owner_uid = ? AND checksum = ?`。如果用户 A 上传了一个文件，用户 B 上传了完全相同的文件，它会被存储两次——即使两个用户共享同一个 blob 在技术上是安全的（存储层使用 storage key 寻址，不绑定 owner）。
2. **预签名上传完全跳过去重**：`CompleteUpload` 路径不对上传内容计算 checksum——服务器从未看到内容字节，因此无法执行去重。同一个文件通过预签名路径上传 N 次，存储 N 次。

### 代码级证据

**证据 1：`FindByChecksum` 按 owner 过滤**

```go
// internal/file/repository.go:139-146
func (r *fileRepo) FindByChecksum(ctx context.Context, ownerUID, checksum string) (*File, error) {
    var row fileRow
    err := r.db.WithContext(ctx).
        Where("owner_uid = ? AND checksum = ?", ownerUID, checksum).  // ← scope 到 owner
        First(&row).Error
    // ……
}
```

**证据 2：`Create` 中使用 owner-scoped 的去重**

```go
// internal/file/service_crud.go:53-55
if existing, ferr := s.files.FindByChecksum(ctx, actor, checksum); ferr == nil {
    // 找到了 → 复用 storage key，删除刚上传的 blob
    _ = s.store.Delete(ctx, key)
    key = existing.StorageKey
    reused = true
}
```

`actor` 就是当前用户。如果 B 的 checksum 与 A 已有文件相同，`FindByChecksum(ctx, "B", checksum)` 返回 `ErrNotFound`。

**证据 3：`CompleteUpload` 完全不做 checksum 计算**

```go
// internal/file/service_upload.go:57-97
func (s *fileService) CompleteUpload(ctx context.Context, in CompleteUploadInput) (*File, error) {
    // …… 权限、MIME、大小检查 ……
    info, err := s.store.Stat(ctx, in.StorageKey)
    // ……
    f := &File{
        // …… Name, Size, MimeType …… 但 Checksum 为空
    }
    if err := s.files.Create(ctx, f); err != nil {
        _ = s.store.Delete(ctx, in.StorageKey)
        return nil, apperr.Internal("file.create_failed", "failed to persist file").Wrap(err)
    }
    return f, nil
}
```

`f.Checksum` 没有赋值（零值），因此后续即使同一用户上传相同内容也无法匹配。

### 影响

1. **存储浪费可量化**：如果在即时通讯场景中，用户 A 发送一张图片给用户 B → 用户 B 转发给用户 C → 存储 2 次（A→B 上传 1 次，B→C 上传 1 次）。如果一个用户向 5 个不同的人发送相同的文件，存储 5 次。交叉用户场景中（用户 C 上传与用户 A 相同的文件），浪费加倍。
2. **预签名上传是零去重**：如果系统使用预签名上传路径（S3 生产场景），所有去重完全失效——这是目前最大的存储成本泄漏点。
3. **`storage_refs` 表无法发挥价值**：迁移 000021（`storage_refs`）已经创建了引用计数表来支持 N:1 的 storage key→file 关系。但跨用户去重不实现，`ref_count` 永远是 1。

### 建议方向

| 方案 | 复杂度 | 说明 |
|------|--------|------|
| **Tier 1**：跨用户去重 | 低 | 将 `FindByChecksum` 改为无条件查询（移除 `owner_uid = ?` 条件），或增加 `visibility` 和 block 的安全过滤：仅允许引用 `visibility = public` 的文件，或用户未被 block 的文件的 storage key |
| **Tier 2**：预签名上传的去重 | 中 | 在 `CompleteUpload` 中，从 S3 获取对象的 ETag（通常是 MD5，如果使用单个 PUT 请求上传）。将 ETag 作为 checksum 使用——虽然不如 SHA256 强，但可以捕获完全相同的文件 |
| **Tier 3**：完整 S3 多部分上传 checksum | 高 | 要求客户端在 `PresignUpload` 时提供 SHA256 承诺（checksum），在 `CompleteUpload` 时计算 S3 存储对象的实际 checksum（使用 S3 Select 或下载后计算） |

**安全注意事项**：跨用户去重可能带来隐私问题——用户 A 知道用户 B 有相同的文件（通过共享 storage key 间接推断）。建议仅在 `visibility = public` 或显式授权的文件间做跨用户去重。

### 为什么未被既有分析覆盖

既有分析文件详细讨论了文件流水线（预览、转码、病毒扫描、缩略图）和存储配额。**但没有任何文档注意到去重作用域限制和预签名上传的零去重问题**——这是一个成本效率缺口，而非功能缺口。

---

## 总结

| # | 方向 | 类别 | 代码证据数 | 优先级 | 建议先行方案复杂度 |
|---|------|------|-----------|--------|-----------------|
| 1 | Message Reactions 无读 API | 产品半完成 | 5 | **P1** | 低（仅加查询 + endpoint） |
| 2 | Rate Limiter 冷启动窗口 | 安全/运维 | 4 | **P1** | 低（预填充关键 key） |
| 3 | Outbox Webhook 同步阻塞 Relay | 架构故障隔离 | 4 | **P1** | 低（并行化 Chain） |
| 4 | 启动时缺少依赖健庻门禁 | 运维健壮性 | 4 | **P2** | 低（Tier 1 启动门禁） |
| 5 | 文件 Checksum 去重作用域和预签名跳过 | 成本/效率 | 3 | **P2** | 低（Tier 1 跨用户去重） |

所有五个方向都满足以下标准：① 有明确代码级证据；② 在 60+ 份既有分析文档中零系统性覆盖；③ 具有可量化的产品/安全/运维/成本影响。
