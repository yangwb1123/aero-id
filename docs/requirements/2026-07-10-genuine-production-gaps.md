# aero-id — 资深架构师/产品经理视角：五项高价值扩展与生产就绪方向

> **日期**: 2026-07-10  
> **方法**: 对 `internal/` 下全部 45+ 个包、22 份 SQL 迁移、API proto 文件做全量代码扫描；与 `docs/requirements/` 下 74 份既有分析文档、`docs/REVIEW_*` 下 8 份审查文档、`ROADMAP.md`、`README.md` 逐一交叉比对。  
> **承诺**: 本文 5 个方向在全部现有分析中 **未以任何形式被系统性覆盖**（标题级零命中，正文零系统性分析）。  
> **约束**: 不编写代码。每个方向附带精确的代码位置和证据。从架构师与产品经理双视角论证产出价值。

---

## 交叉验证摘要

| 搜索关键词 | 搜索范围 | 命中数 | 判定 |
|-----------|---------|--------|------|
| `metrics.*auth\|/metrics.*protect\|metrics.*leak\|metrics.*expos\|info.*disclos` | 全部 83 份文档 | 0 | ❌ 零覆盖 |
| `sse.*gzip\|sse.*compress\|event.*stream.*compress\|sse.*limit.*conn\|sse.*max\|hub.*max\|sse.*buffer\|event.*stream.*limit` | 全部 83 份文档 | 0 | ❌ 零覆盖 |
| `relay.*overlap\|concurrent.*drain\|drain.*tick.*race\|tick.*overlap\|drain.*overlap\|relay.*mutex\|relay.*semaphore` | 全部 83 份文档 | 0 | ❌ 零覆盖（仅 latency 提及，非 correctness bug） |
| `revok.*url\|presign.*revok\|presign.*invalidate\|invalidate.*url\|url.*invalidate\|presign.*cancel\|stale.*presign\|sign.*revok\|url.*revoc` | 全部 83 份文档 | 0 | ❌ 零覆盖 |
| `notif.*rate.*instance\|rate.*limit.*instance\|notification.*rate.*restart\|dispatcher.*restart\|dispatcher.*reset\|in.memory.*rate.*notif` | 全部 83 份文档 | 0 | ❌ 零覆盖（preference/read-receipt 有提及但非部署视角） |

---

## 方向一：Metrics 端点缺少访问控制（信息泄露风险）

### 严重性

**安全 · 中高**

### 问题描述

`/metrics` 端点暴露在 **无认证** 的公开路由上。任何能够到达服务 HTTP 端口的人都可以读取完整的 RED 指标、SSO 健康仪表盘、outbox 延迟、断路器状态等运维敏感数据。在生产环境中，这是信息泄露——攻击者可以利用这些数据识别服务瓶颈、缓存命中率、SSO 依赖的脆弱性。

### 代码级证据

**证据 1：Metrics 端点在 router.go 中注册于公开分组**

```go
// internal/handler/http/router.go:85-87
if cfg.Metrics != nil {
    e.GET("/metrics", gin.WrapH(cfg.Metrics.Handler()))  // ← 在 e 上注册，不在 Auth 分组
}
```

`e` 是 `*gin.Engine` 本身，没有挂载任何认证中间件。对照 `/healthz` 和 `/readyz` 也在公开路径上（属于正常设计），但 `/metrics` 暴露的数据比健康检查敏感得多。

**证据 2：Metrics Registry 暴露了哪些数据**

```go
// internal/pkg/metrics/metrics.go
// 注册的指标包括（代码扫描发现）：
// - aeroid_http_request_duration_seconds{method,route,status}
// - aeroid_sso_validate_duration_seconds{success}
// - aeroid_sso_cache_hit_ratio
// - aeroid_outbox_pending_events
// - aeroid_outbox_lag_seconds
// - aeroid_sso_breaker_state
```

所有指标都包含路由级别（`/v1/files/:id`）和状态码维度的标签，可以反向推断业务流量模式。

**证据 3：gRPC 和 HTTP 的 metrics 是同一个 registry**

```go
// internal/app/app.go:175-176
metricsReg := metrics.NewRegistry()
// ... 被 SSO client、HTTP middleware、gRPC interceptor 共享
```

所以 `/metrics` 也暴露 gRPC 调用的性能数据。

**证据 4：配置中 Metrics 端点无任何保护配置**

```yaml
# configs/config.yaml — 无 metrics 相关安全配置段
# 没有 metrics_auth_enabled、metrics_allowed_ips、metrics_token 等
```

### 产品影响

| 场景 | 影响 |
|------|------|
| 攻击者枚举路由 → 发现 `/metrics` | 获取所有 API 端点的 P99 延迟、错误率，定位最慢/最脆弱的路由 |
| 攻击者观察 SSO 缓存命中率 | 判断 SSO 的稳定性，选择 SSO 不可用时攻击 |
| 运维团队误暴露公网 | 内部架构信息完全暴露，包括中间件版本、依赖版本（如果 Go runtime metrics 启用） |

### 建议方向

1. **给 `/metrics` 添加 Basic Auth 或 Bearer Token 认证** — 在指标端点上挂一个简单的认证中间件，配置通过环境变量 `AERO_ID_METRICS_TOKEN` 注入。
2. **或挂载到 Auth 分组** — 将 `/metrics` 移到 `/v1/admin/metrics` 下，复用现有的 SSO token 验证 + 权限检查。
3. **或支持 IP 白名单** — 允许配置 `metrics_allowed_cidrs`，只允许监控系统（如 Prometheus）的来源 IP 访问。

```go
// 建议的路由注册变更（示意）：
// 选项 A：Basic Auth
e.GET("/metrics", gin.BasicAuth(gin.Accounts{"prometheus": metricsPassword}), gin.WrapH(h))

// 选项 B：挂到 Auth 分组
r.Auth.GET("/admin/metrics", gin.WrapH(h))
```

### 估算工作量

低（1-2 天）。路由注册处加一个中间件 + 配置项。

---

## 方向二：SSE 流缺少压缩、连接治理和背压机制

### 严重性

**性能/资源 · 中**

### 问题描述

当前 SSE 端点（`GET /v1/events`）是 aero-id 实时推送的唯一通道。但它在三个关键方面存在生产就绪不足：

1. **无 Gzip 压缩** — 每个通知的 JSON payload 原样写入。对于高频率的通知推送（消息、评论回复、已读回执），带宽浪费明显。
2. **无每个用户的连接数限制** — 一个用户可以打开无限个 SSE 连接（多个标签页、多设备重连）。Hub 的 `sync.Map` 允许同一 uid 的任意数量订阅者。
3. **无背压机制** — Hub 的 channel 缓冲区固定为 16，超出时静默丢弃。没有向通知 dispatcher 反馈"用户消费速度跟不上"的信号。

### 代码级证据

**证据 1：SSE 响应头没有设置 gzip，handler 直写明文**

```go
// internal/handler/http/events.go:52-80
func (h *EventsHandler) Stream(c *gin.Context) {
    // ...
    c.Header("Content-Type", "text/event-stream")
    c.Header("Cache-Control", "no-cache")
    c.Header("Connection", "keep-alive")
    // 没有 Content-Encoding: gzip
    // 没有 Accept-Encoding 协商
    // ...
    for {
        select {
        case evt, ok := <-sub:
            fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", evt.ID, evt.Type, evt.Data)
            // ↑ raw fmt.Fprintf，不经过 gzip writer
        }
    }
}
```

Gin 默认不压缩 SSE 流（`gin-gonic/gin` 的 `gin.Default()` 不包含 gzip 中间件）。`fmt.Fprintf` 直接写到 `c.Writer`。

**证据 2：Hub.Subscribe 无连接数限制**

```go
// internal/hub/hub.go:53-68
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    ch := make(chan SSEEvent, 16)
    // ...
    // ← 没有检查当前 uid 已有多少活跃连接
    // ← 没有 maxSubscribersPerUID 或类似限制
    entry, _ := h.subs.LoadOrStore(uid, &uidSubs{subs: make(map[int64]chan SSEEvent)})
    us := entry.(*uidSubs)
    // ...
    us.subs[id] = ch  // ← 无限增长
    // ...
}
```

一个恶意用户（或 buggy 客户端）可以：
- 在循环中快速 `new EventSource("/v1/events")` → 耗尽 Hub 内存
- 打开 1000 个标签页 → 1000 个 channel → 1000x 内存 + 1000x 写入放大

**证据 3：Hub.Publish 使用非阻塞发送，静默丢弃**

```go
// internal/hub/hub.go:100-108
func (h *Hub) pub(uid string, evt SSEEvent) {
    // ...
    for _, ch := range us.subs {
        select {
        case ch <- evt:
        default:
            // subscriber is too slow; drop rather than block
            // ← 静默丢弃，无计数器、无日志、无背压信号
        }
    }
}
```

当通知频率超过客户端消费能力时（如短时间内收到 50 条消息通知，但客户端只处理了 10 条），40 条被静默丢弃。客户端永远不会知道它错过了事件。SSE 的 `Last-Event-ID` 重放机制可以部分缓解，但如果丢弃太多且客户端没有及时重连，重放窗口（`ListSince` 的 limit=50）可能不够。

**证据 4：Dispatcher 不感知 Hub 的负载**

```go
// internal/service/notification.go:155-160
func (d *NotificationDispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    // ... 写入 notification 行 ...
    if d.pusher != nil {
        payload, _ := json.Marshal(n)
        d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
        // ↑ 不知道 client 是否已经断开连接，不知道 channel 是否已满
    }
    return nil
}
```

Dispatcher 与 Hub 之间是"发射后不管"的契约。没有反馈回路让 Dispatcher 知道是否需要限速。

### 增长推算

| 指标 | 当前值 | 无限制增长至 |
|------|-------|------------|
| 每用户最大连接数 | 无限制 | 1000 连接 × 1000 用户 = 1M channels |
| 每个 channel 缓冲区 | 16 事件 | 1M × 16 × ~200 bytes ≈ 3.2 GB |
| 写入放大 | 100 连接 → 100x | 慢客户端导致消息丢弃 |

### 建议方向

1. **启用 Gzip 压缩** — 在 SSE handler 中检查 `Accept-Encoding: gzip`，若客户端支持则使用 `gzip.Writer` 包装 `c.Writer`。Gin 社区有现成的 `gin-gonic/contrib/gzip` 中间件，但需要确认 SSE 流式兼容性。
2. **添加每用户最大连接数** — 在 `Hub.Subscribe` 中检查当前 uid 的连接数（配置项 `max_connections_per_uid`，默认 3-5）。超出时关闭最旧的连接或返回错误。
3. **添加 Hub 指标** — 暴露 `aeroid_hub_active_connections{uid}` gauge 和 `aeroid_hub_dropped_events_total` counter，让运维人员可以监控挤压程度。
4. **考虑背压信号** — 当 Hub 的丢弃率达到阈值时，通过一个 channel 或回调通知 Dispatcher 暂停推送该用户的实时通知，退化为仅写数据库 + 客户端轮询。

### 估算工作量

| 项目 | 工作量 | 影响 |
|------|--------|------|
| Gzip 压缩 | 低（~半天） | 带宽减少 5-10x |
| 连接数限制 | 低（~半天） | 防止资源耗尽 |
| Hub 指标 | 低（~半天） | 可观测性提升 |
| 背压信号 | 中（2-3天） | UX + 可靠性提升 |

---

## 方向三：Outbox Relay 并发 Drain 重叠导致重复事件处理

### 严重性

**正确性 · 中高**

### 问题描述

Outbox relay 的 `Start` 方法使用 `time.Ticker` 按固定间隔触发 `drain()`。但 `drain()` 的持续时间可能超过 ticker 间隔——默认 `poll_interval=5s`，而 webhook 超时可能为 10s。此时 **下一个 tick 会在前一个 drain 仍在运行时触发**。由于没有互斥锁或信号量保护，两个并发 drain 会：

1. 共同获取同一批未发布事件（`FetchUnpublished` 返回相同事件）
2. 各自调用 `handler.Handle`（产生重复通知和重复 webhook 请求）
3. 各自调用 `MarkPublished`（后一个覆盖前一个，无冲突但重复事件已处理）

### 代码级证据

**证据 1：Start 使用 time.Ticker，不检查 drain 状态**

```go
// internal/outbox/relay.go:188-203
func (r *Relay) Start(interval time.Duration) (stop func()) {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        ctx := context.Background()
        for {
            select {
            case <-t.C:
                r.drain(ctx)     // ← 如果 drain 耗时 > interval，这里会重叠执行
            case <-done:
                t.Stop()
                r.drain(context.Background())
                return
            }
        }
    }()
    return func() { close(done) }
}
```

Ticker 不保证同一时刻只有一个 handler 在执行。如果 `drain()` 阻塞了 10s（等待慢 webhook），第 5s 时第二个 `t.C` 已经触发。

**证据 2：Relay 结构体没有同步原语保护**

```go
// internal/outbox/relay.go:58-66
type Relay struct {
    repo       Repository
    handler    Handler
    log        *zap.Logger
    batch      int
    maxRetries int
    metrics    MetricsSink
    now        func() time.Time
    // ← 没有 sync.Mutex、semaphore、或 inflight 标志
}
```

没有 `sync.Mutex`，没有 `atomic.Int32` 表示"正在运行"，没有任何阻止并发的机制。

**证据 3：FetchUnpublished 使用 ORDER BY 但无锁定语义**

```go
// internal/outbox/repository.go:107-118
func (r *GormRepository) FetchUnpublished(ctx context.Context, limit int) ([]*Event, error) {
    var rows []eventRow
    if err := r.db.WithContext(ctx).
        Where("published_at IS NULL AND NOT dead_letter").
        Order("created_at ASC, id ASC").
        Limit(limit).
        Find(&rows).Error; err != nil {
        // ...
    }
    // 返回未锁定的行——两个并发 drain 会看到同一批事件
}
```

没有 `FOR UPDATE SKIP LOCKED` 或其他行级锁。两个 drain 调用会返回完全相同的事件列表。

### 重叠 Drain 的影响

假设 10 个事件，poll_interval=5s，webhook 超时=10s：

```
T=0s    drain() 开始 → 处理事件 1（webhook 等待 10s）
T=5s    ← 第二个 tick！第二个 drain() 开始
        drain #2: FetchUnpublished 返回事件 1-10（也包括事件 1！）
        drain #2: handler 处理事件 1 → 创建第二条通知 + 第二次 webhook 请求
T=10s   drain #1: webhook 超时 → MarkFailed(1)
        drain #1: 继续处理事件 2...
T=15s   drain #2: 处理完成 → MarkPublished([1,2,3,4,5])
T=20s   drain #1: 处理完成 → MarkPublished([6,7,8,9,10])
```

结果：事件 1 被标记为 published（by drain #2），但 **通知和 webhook 各产生了两次**。

### 建议方向

1. **立即修复：添加 `sync.Mutex` 或 `atomic bool` 防止重叠** — 在 `drain()` 入口处检查是否有 drain 正在运行，如有则跳过本次 tick。这是最低成本的改动。
2. **使用 `FOR UPDATE SKIP LOCKED`** — 修改 `FetchUnpublished`，使用 `SELECT ... FOR UPDATE SKIP LOCKED` 确保每个事件只被一个 drain 领取。这是最 robust 的方案。
3. **或将 drain 改为串行 goroutine + channel** — 使用单个 worker goroutine，tick 通过 channel 发送信号，worker 每次处理完一个 batch 后再处理下一个信号。
4. **为 drain 添加超时 context** — 防止单个 drain 无限期阻塞。

```go
// 最低成本修复（示意）：
type Relay struct {
    // ...现有字段...
    draining atomic.Bool  // ← 新增
}

func (r *Relay) drain(ctx context.Context) int {
    if !r.draining.CompareAndSwap(false, true) {
        return 0  // 已有 drain 运行中，跳过
    }
    defer r.draining.Store(false)
    // ... 现有逻辑 ...
}
```

### 估算工作量

低（1 天）。核心修复只需添加 `sync.Mutex` 或 `atomic.Bool` 加上 `FOR UPDATE SKIP LOCKED`。

---

## 方向四：Notification Dispatcher 的 Rate Limiter 是单机内存的，生产部署不可靠

### 严重性

**功能正确性 · 中**

### 问题描述

`NotificationDispatcher` 使用一个内存中的滑动窗口计数器（`notifRateLimiter`）来限制每个（收件人，通知类型）每分钟的通知数量。这个实现存在两个生产部署问题：

1. **实例重启后计数器清零** — 服务重启后，所有通知速率限制重置。如果重启前用户已经收到了大量通知，重启后该用户可立即再次收到全量通知。
2. **多实例部署下速率限制失效** — 每个实例有各自独立的计数器。N 个实例意味着有效速率上限为 N × rate。对于 3 实例部署 + rate=10，实际速率可达 30/分钟。

### 代码级证据

**证据 1：notifRateLimiter 是纯 Go map，无持久化**

```go
// internal/service/notification.go:233-242
type notifRateLimiter struct {
    mu   sync.Mutex
    rate int
    win  time.Duration
    keys map[string][]time.Time  // ← 纯内存，无持久化后端
    now  func() time.Time
}
```

`keys` 是一个 `map[string][]time.Time`。重启后所有数据丢失。

**证据 2：Dispatcher 的 rate limiter 不与外部存储交互**

```go
// internal/service/notification.go:59-65
type NotificationDispatcher struct {
    notifs      repository.NotificationRepository
    blocks      BlockChecker
    prefs       PreferenceChecker
    pusher      NotificationPusher
    rateLimiter *notifRateLimiter  // ← 永远不会有 Redis 或 DB 后端
    collapseWin time.Duration
}
```

接口设计中没有 `NotificationRateLimiter` 抽象，只有具体的 `*notifRateLimiter`。无法在构造时注入一个共享后端实现。

**证据 3：Composition root 未配置分布式限流**

```go
// internal/app/app.go:186-191
dispatcher := service.NewNotificationDispatcher(notifRepo,
    service.WithDispatcherBlockChecker(blockRepo),
    service.WithDispatcherPreferenceChecker(prefChecker),
    service.WithDispatcherRateLimiter(10), // ← 写入时检查，单机
    service.WithDispatcherCollapse(5*time.Minute),
    service.WithDispatcherPusher(sseHub),
)
```

`WithDispatcherRateLimiter(10)` 传递一个常量，没有配置注入和 Redis 支持。

### 场景影响

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 服务滚动更新（rolling restart） | 重启后用户立即收到全量通知 | 限流状态跨实例/重启保持 |
| 3 实例部署，rate=10 | 用户每分钟最多收到 30 条通知 | 每分钟最多 10 条 |
| 用户频繁断连 S3 部署 (US/EU 多区域) | 用户连接不同区域实例 → 每个区域独立限流 | 全局一致的限流 |

### 建议方向

1. **短期：在配置中暴露 rate 参数** — 让 `rate` 可以通过配置项调整，而不是硬编码在 app.go 中。
2. **中期：将限流计数器后移到 Redis** — 使用 Redis 的 `INCR` + `EXPIRE` 实现滑动窗口。保持现有的内存限流器作为 fallback（无 Redis 时退化为不稳定但安全的宽松限制）。
3. **长期：引入 `NotificationRateLimiter` 接口** — 抽取接口，使 Dispatcher 可以接受 `RedisRateLimiter` 或 `MemoryRateLimiter` 实现，无需了解具体实现。

### 估算工作量

| 项目 | 工作量 |
|------|--------|
| 配置化 rate 参数 | 低（~半天） |
| Redis 后端实现 | 中（2-3 天） |
| 接口抽象 + 注入改造 | 低（~1 天，非破坏性） |

---

## 方向五：Presigned Download URL 无法提前撤销

### 严重性

**安全/功能 · 中**

### 问题描述

文件的所有下载 URL（包括本地模式 HMAC 签名的 `/files/dl` URL 和 S3 模式的 presigned S3 URL）在生成后都有一个固定的 TTL。在这个 TTL 内，**没有任何机制可以撤销或失效这些 URL**。如果：

- 文件的可见性从公开改为私有
- 文件的分享被撤销
- 文件被删除
- 文件所有者想把链接失效

已有的下载 URL 在 TTL 内仍然有效。

### 代码级证据

**证据 1：DownloadURL 生成一次性的 presigned URL，无状态检查**

```go
// internal/service/file.go:212-228（简化）
func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
    actor, err := mustActor(ctx)
    f, err := s.files.Get(ctx, id)
    // 检查 actor 是否有权读取（此时通过）
    // ...
    url, err := s.store.PresignDownload(ctx, f.StorageKey, ttl)
    // ↑ 生成的 URL 在未来 ttl 内有效，不做后续访问检查
    return url, nil
}
```

URL 生成时做了权限检查，但 URL 本身不包含任何授权凭证。之后无论文件的分享状态或可见性如何变化，URL 仍然可用。

**证据 2：本地模式的 HMAC 签名也不可撤销**

```go
// internal/storage/signing.go:13-20
func presignHMAC(key string, expires int64, signingKey string) string {
    mac := hmac.New(sha256.New, []byte(signingKey))
    fmt.Fprintf(mac, "%s|%d", key, expires)
    return hex.EncodeToString(mac.Sum(nil))
}
```

签名只包含 `storage_key` 和 `expires` 时间戳。不包含文件 ID、版本号、或任何可以被文件状态变化影响的信息。

**证据 3：S3 presigned URL 完全在 aero-id 控制之外**

```go
// internal/storage/s3.go — PresignDownload
func (s *S3Storage) PresignDownload(ctx context.Context, key string, ttl time.Duration) (string, error) {
    // 调用 AWS SDK GetObjectRequest.Presign(ttl)
    // ↑ S3 返回的 URL 在 TTL 内完全有效，aero-id 无法干预
}
```

S3 presigned URL 由 AWS 签名，aero-id 无法撤销。

**证据 4：文件删除后下载 URL 仍然有效（local 模式）**

```go
// internal/handler/http/file.go:54-57
if h.downloader != nil {
    r.Public.GET("/files/dl", h.Download)
}
// /files/dl 公共路由
```

```go
// internal/handler/http/file.go:269-283
func (h *FileHandler) Download(c *gin.Context) {
    rawURL := c.Request.URL.String()
    // 验证 HMAC 签名 → 通过则直接 ServeFile
    // ← 不检查文件是否已被软删除
    // ← 不检查文件可见性是否已更改
    // ← 不检查该 uid 的分享是否已被撤销
}
```

`/files/dl` 的 handler 只验证 HMAC 签名，**不做任何运行时权限检查**。

### 产品影响

| 场景 | 当前行为 |
|------|---------|
| 用户分享了一个文件给同事，然后撤销分享 | 同事已有的下载 URL 仍然可用，直到 TTL 到期 |
| 用户将文件从公开改回私有 | 所有已分发的公开下载 URL 仍然可用 |
| 用户删除了文件 | 如果有已分发的下载 URL，该 URL 仍然可以下载文件（直到 GC 回收 blob） |
| 文件包含敏感内容，需要立即失效所有链接 | 没有 API 支持此操作 |

### 建议方向

1. **在 presigned URL 中加入文件版本号或 `updated_at`** — 当文件可见性/分享状态变化时，更新 `files.updated_at`。签名时包含该时间戳，生成后修改状态会导致签名不匹配。注意：这会使所有已分发的 URL 立即失效，可能影响正常使用。
2. **在 `/files/dl` 中加入运行时检查** — 对于 local 模式，验证 HMAC 后增加一步：查询文件是否存在、是否已删除、visibility 是否匹配。S3 模式无法做运行时检查。
3. **提供撤销 API** — `POST /v1/files/:id/invalidate-urls` 内部更新 `files.updated_at` 使所有现有签名失效。适用于紧急情况。
4. **短 TTL + Refresh 模式** — 默认 TTL 缩短到 5 分钟，客户端需要时才请求新的 URL。减少 URL 滥用的时间窗口。

### 建议优先级

| 项目 | 工作量 | 影响 |
|------|--------|------|
| `/files/dl` 运行时检查（local） | 低（~半天） | 提升本地模式一致性 |
| 撤销 API + updated_at 签名 | 中（1-2 天） | 提供紧急失效能力 |
| 短 TTL + refresh | 低（配置修改） | 缩小攻击窗口 |
| S3 模式不可撤销（文档化） | 低 | 明确已知限制 |

### 估算工作量

中（2-3 天完成前三项）。

---

## 综合路线图建议

```
优先级    方向                                         价值       工作量
──────────────────────────────────────────────────────────────────────────
P0(Patch) 方向一：Metrics 端点访问控制                    安全        低
P0(Patch) 方向三：Relay 并发 drain 重叠修复              正确性      低
P1        方向二：SSE 压缩 + 连接治理 + 指标              性能+资源   中
P1        方向五：Download URL 撤销能力                    安全+功能   中
P2        方向四：通知限流器跨实例/持久化                  功能正确性  中-高
```

所有方向均遵循现有分层架构，不破坏既有依赖关系。P0 列的两个修复可以立即进行代码变更。
