## 代码级验证报告

我已通读所有被引用的源码文件。以下是对五项分析的**逐一验证与补充**：

---

### ✅ 方向一：Middleware 排序缺陷 — RATE LIMIT 在 IDEMPOTENCY 之前

**验证结论：确认存在。** 代码证据与描述完全吻合。

```
// router.go:99-108
authMW := []gin.HandlerFunc{middleware.Auth(validator)}
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter))   // ← 第二位
}
if cfg.Idempotency != nil {
    authMW = append(authMW, middleware.Idempotency(...))              // ← 第三位
}
```

执行链：`Auth → RateLimit → Idempotency → Handler`

`idempotency.go:73` 中幂等回放调用 `replayIdempotent()` → `c.Abort()`，但此时在它之前运行的 `ratelimit.go:32` 已经可以 `c.AbortWithStatusJSON(http.StatusTooManyRequests, ...)` 提前截断管线。幂等中间件的 `captureWriter` 和缓存逻辑永远不会被执行。

**额外发现**：`router.go:65` 的行内注释说"the rate limiter runs AFTER auth so it keys on the resolved actor uid, then idempotency so keys are scoped per user"——它正确解释了为什么 Auth 在第一位，但**完全没有考虑 RateLimit 与 Idempotency 的交互问题**。注释本身就暴露了这个盲区。

**实际影响量化**：当限流 burst = 1 时，任何幂等重试的第一次命中都会 429（因为第一次成功已经消耗了唯一 token），导致**操作永久不可完成**。

---

### ✅ 方向二：Outbox Webhook Chain 缺乏隔离

**验证结论：确认存在。** 且比描述更严重——不仅是"Dispatcher 先创建行再发 SSE 然后 Webhook 失败"，而且重试会产生**重复的 notification 行**。

```go
// app.go:185-207
var relayHandler outbox.Handler = dispatcher
if cfg.Outbox.Webhook.URL != "" {
    webhookHandler := outbox.NewWebhookHandler(whCfg, log)
    relayHandler = outbox.NewChain(dispatcher, webhookHandler)  // 顺序串联
}
relay := outbox.NewRelay(outboxRepo, relayHandler, ...)
```

```go
// chain.go:11-15
for _, h := range c.handlers {
    if err := h.Handle(ctx, e); err != nil {
        return err   // ← 任意 handler 失败 → 全局失败
    }
}
```

注意 `relay.go:122-127` 中失败的逻辑：
```go
if err := r.handler.Handle(ctx, e); err != nil {
    // ...
    if err := r.repo.MarkFailed(ctx, e.ID, e.RetryCount+1, r.maxRetries, now); err != nil { ... }
    continue  // ← 跳过 MarkPublished，下一次 drain 继续处理同一个事件
}
```

重试时 `relay.go:93-96` 的 `FetchUnpublished` 会重新取出同一个 outbox 事件 → Dispatcher 再次创建一条 `notification` 行（**新的 UUIDv7**，因为 `notificationFor()` 调用 `idgen.NewID()`）→ **重复通知进入用户的 inbox**。当前没有 per-event 幂等机制防止这种重复。

**补充证据**：`notification.go:220` 中 Dispatcher 的 `Create` 调用没有检查重复：
```go
if err := d.notifs.Create(ctx, n); err != nil {
    return err
}
```

**附加发现**：`internal/notification/dispatcher.go`（旧版 dispatcher）已经是**死代码**——没有任何生产代码 import 它。它的逻辑（3-arg Publish，无 collapse/rate-limit/preference）未使用，但可能会给未来开发者造成困惑。

---

### ✅ 方向三：全文搜索不感知 User Block

**验证结论：确认存在。** 六个搜索方法全部缺失 block 过滤。

| 方法 | SQL where 条件 | Block 过滤 |
|------|---------------|-----------|
| `SearchMessages` (line 322) | `(from_uid = ? OR to_uid = ?)` | ❌ |
| `SearchMessagesLike` (line 345) | `(from_uid = ? OR to_uid = ?)` | ❌ |
| `SearchComments` (line 378) | `owner_uid = ?` | ❌ |
| `SearchCommentsLike` (line 401) | `owner_uid = ?` | ❌ |
| `SearchFiles` (line 226) | `owner_uid = ?` | ❌ |
| `SearchFilesLike` (line 248) | `owner_uid = ?` | ❌ |

`internal/service/search.go` 整个文件没有任何 `block`、`BlockChecker` 或 `IsBlocked` 引用。对比：

```go
// service/message.go:69-72 — 正确实现
blocked, err := s.blocks.IsBlocked(ctx, in.ToUID, actor)
```

```go
// service/notification.go:216-219 — Dispatcher 中正确实现
blocked, err := d.blocks.IsBlocked(ctx, n.RecipientUID, n.ActorUID)
```

**搜索服务是唯一没有接入 block 检查的 read path。**

**补充风险**：ILIKE 回退路径（`SearchMessagesLike` 等）同样不受保护。CJK 搜索使用 ILIKE 时一样会返回已屏蔽用户的内容。

---

### ✅ 方向四：SSE 重连回放缺乏去重

**验证结论：确认存在。** 核心问题是 `last_event_id` 使用单一 `created_at` 时间戳（毫秒精度）作为断点。

```go
// events.go:106-108
millis, err := strconv.ParseInt(lastEventID, 10, 64)
since := time.UnixMilli(millis)
missed, err := h.fetcher.ListSince(c.Request.Context(), uid, since, 50)
```

```go
// notification.go:156-157
Where("recipient_uid = ? AND created_at > ?", recipientUID, since).
Order("created_at ASC, id ASC").
```

注意排序是 `created_at ASC, id ASC`——已经有了双字段排序，但断点仍然只用 `created_at > since`，这意味着同一毫秒内的所有事件在重连时**要么全部被包含**（当断点落在该毫秒之前），**要么全部被跳过**（当断点落在该毫秒之后）。

**此外还有一个更隐蔽的场景**：

`events.go:86-93` 中实时推送的 event ID 与重放路径中的 event ID 使用同一个来源：
```go
// 实时推送 (line 90)
fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", evt.ID, evt.Type, evt.Data)
// 重放推送 (line 112)
eventMillis := n.CreatedAt.UnixMilli()
fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", eventMillis, n.Type, string(data))
```

`evt.ID` 由 `hub.Publish` 传入（来自 `notification.go:246,259` 的 `existing.CreatedAt.UnixMilli()` / `n.CreatedAt.UnixMilli()`），而重放路径用的是从数据库读出来的 `n.CreatedAt.UnixMilli()`。两者在原理上是同一个值，但路径不同——实时推送经过 in-memory Hub channel，重放路径经过 Postgres 查询。如果这两个值不同（因为 Dispatcher 在创建 notification 行时用的 `time.Now().UTC()` 和 `ListSince` 读出来的 `CreatedAt` 之间有时间差？实际上，Dispatcher 中 `n.CreatedAt = time.Now().UTC()` 设置后立刻写入 DB，然后 `pusher.Publish` 传入 `n.CreatedAt.UnixMilli()`。实时路径和 DB 路径使用的是同一个对象，所以理论上一致。但在 `collapsed` 路径下：
```go
// notification.go:248 — collapsed 路径
d.pusher.Publish(n.RecipientUID, n.Type, string(payload), existing.CreatedAt.UnixMilli())
// 用的是 existing 的 CreatedAt，不是新创建的 n 的
```

所以 collapsed notification 的 SSE event ID 是**旧行的 CreatedAt**，而重放时 `ListSince` 查找的是**新行的 CreatedAt**（因为 collapsed 不创建新行，`ListSince` 返回的是那个 existing 行），两者在时间上差了一个 collapse 窗口。但这不影响正确性。

---

### ✅ 方向五：Presigned 文件下载端点缺少安全响应头

**验证结论：确认存在。** `file.go:289-303` 中 Download handler 的整个实现：

```go
func (h *FileHandler) Download(c *gin.Context) {
    // ... 验证 presigned signature ...
    rc, err := h.downloader.Get(c.Request.Context(), c.Query("key"))
    // ...
    c.Status(http.StatusOK)
    _, _ = io.Copy(c.Writer, rc)   // ← 纯裸流，无任何安全头
}
```

对比 `SSE handler` 和普通 JSON API handler，它们都会设置 `Content-Type`：
- `events.go:79` → `c.Header("Content-Type", "text/event-stream")`
- Gin/httpx 的所有 JSON 响应自动设置 `Content-Type: application/json`

Download handler 是唯一**完全不设任何 Content-Type 的端点**。

**额外发现**：该端点在 `router.go` 中属于 public 组（无 Auth），且与主应用同域名（`/files/dl` 前缀）。这意味着 XSS 攻击可以访问同域的 cookie/localStorage，与 S3 presigned URL 方式（跨域）的安全等级不同。

**S3 backend 的安全性对比**：`s3.go:97-103` 的 `Get` 调用也没有设置 `ResponseContentType`，但在 S3 场景下，presigned URL 直接指向 S3（不是通过 `/files/dl`），且 S3 默认 Content-Type 为 `binary/octet-stream`。但 `Put` 时也同样未设置 ContentType（`s3.go:78-85`），S3 可能会自动检测 MIME 类型。所以 S3 场景的相对风险较低，但不是完全免疫。

**此外**：文件上传路径（`Upload` → `service.FileService.Create`）中的 MIME 类型没有经过 magic bytes 校验——服务端信任客户端声明的 `Content-Type`。这增加了攻击者上传伪装文件的可能性。

---

## 附加发现（文档中未覆盖）

### 🟡 6. 旧版 notification package 死代码

`internal/notification/dispatcher.go`（及其配套的 `handler_http.go`、`handler_grpc.go`、`service.go`）不参与任何 production wire path。`app.go` 使用的是 `internal/service/notification.go` 中重构后的 `NotificationDispatcher`。

```bash
grep -rn "aero/aero-id/internal/notification" --include="*.go" | grep -v "_test.go"
# → 无输出
```

建议清理以减少认知负担，避免未来误修改。

### 🟡 7. HMAC 签名密钥注入方式一致性

`sso.go` 中的 TLS cert pinning 和 `webhook.go` 中的 HMAC signing key 通过 config 注入；`storage/local.go` 的 `SigningKey` 同理。但 `local.go:128` 的 `VerifyPresignedURL` 的错误消息中暴露了内部信息：

```go
return errors.New("presign: expired")
return errors.New("presign: bad signature")
return errors.New("presign: missing params")
```

这些错误文本在 HTTP 响应中不暴露（handler 统一返回 `apperr.Forbidden("file.bad_signature", "invalid or expired download link")`），但如果未来有人在 handler 之外调用 `VerifyPresignedURL` 并暴露错误，就会存在信息泄露风险。

---

## 综合优先级评估（微调后）

| # | 方向 | 严重性 | 修复成本 | 优先级 | 修改文件数 |
|---|------|--------|---------|--------|-----------|
| **1** | Middleware 排序 | ⚠️ 中 | 极低（改两行顺序） | **P0** | 1 |
| **2** | 文件下载 XSS | 🔴 高 | 低（加 3 行 header） | **P0** | 1 |
| **3** | Search 不感知 Block | ⚠️ 高 | 低（6 处 SQL 加 NOT EXISTS） | **P1** | 4（3 repo + 1 构造注入） |
| **4** | Webhook 阻塞全局管道 | ⚠️ 高 | 中（Chain → fan-out 或独立队列） | **P1** | 3（chain + relay + app） |
| **5** | SSE 重放去重 | ⚠️ 中 | 中（断点格式从 ts → (ts,id) 复合） | **P2** | 3（events + notif repo + 客户端） |
| **6** | 死代码清理 | 🟢 低 | 低（删除 ~4 个文件） | **P3** | 4+ |

方向一和方向五确实可以同一个 PR 解决——各改一个文件，互不冲突，无需测试重构。方向三的 SQL 改法统一为：

```sql
AND NOT EXISTS (
  SELECT 1 FROM user_blocks
  WHERE blocker_uid = ? AND blocked_uid = messages.from_uid
)
```

方向四需要协调客户端（`last_event_id` 格式从 `"1712345678000"` 变为 `"01JABC...def,1712345678000"`），但在服务端可以先做向后兼容。

文档的分析质量很高，证据链完整，代码引用精准，优先级建议合理。
