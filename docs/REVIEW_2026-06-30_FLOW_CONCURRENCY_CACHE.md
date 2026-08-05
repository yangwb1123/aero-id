# aero-id — 第七轮审查：数据流追踪 · 并发模型 · 缓存策略 · 文档完备性

> 日期: 2026-06-30
> 角度: 端到端事件流追踪 · Goroutine/锁分析 · 缓存失效图 · 文档 vs 代码一致性

---

## 0. 与之前六轮的关系

| 维度 | 前六轮 | 本轮 |
|------|--------|------|
| 数据流 / 事件追踪 | 未覆盖 | ✅ **端到端 Trace** |
| 并发模型 / Goroutine | 部分提及（后台 goroutine，优雅关机） | ✅ **锁分析、竞态可能、channel 模式** |
| 缓存策略 | 提及 SSO cache | ✅ **全缓存面分析** |
| 文档完备性 | 未覆盖 | ✅ **README/ARCHITECTURE/DECISIONS vs 代码一致性** |
| 跨领域关注点 | 分散在各篇 | ✅ **统一审查（日志/追踪/错误处理）** |
| 扩展方向 | 30 个 | ✅ **5 个全新方向** |

---

## 1. 端到端数据流追踪

### 1.1 消息发送的完整生命周期

以 `POST /v1/messages` → gRPC `SendMessage` 为例：

```
┌────────────────────────────────────────────────────────────────────┐
│ 1. HTTP/GRPC 传输层                                                 │
│    Auth(SSO ValidateToken) → RateLimit → Idempotency(Reserve)     │
│    → [handler] SendMessage(decode request, call service.Send)     │
└───────────┬────────────────────────────────────────────────────────┘
            │
┌───────────▼────────────────────────────────────────────────────────┐
│ 2. service.MessageService.Send(ctx, input)                         │
│    • mustActor(ctx) — 从 context 提取 uid                           │
│    • 验证 input: to_uid, kind, content/file_id                     │
│    • BlockChecker: s.blocks.IsBlocked(ctx, toUID, actor)           │
│    • FileID 验证: s.files.Get(ctx, *fileID)                        │
│    • 构造 domain.Message + outbox event                            │
│    • s.messages.Create(ctx, msg, evt)  ← 同一事务                   │
│        └─ db.Create(msg)                                           │
│        └─ outboxRepo.Append(ctx, tx, evt)                          │
└───────────┬────────────────────────────────────────────────────────┘
            │
┌───────────▼────────────────────────────────────────────────────────┐
│ 3. 出箱 relay (独立 goroutine, 5s tick)                             │
│    • r.db.Where(published_at IS NULL AND NOT dead_letter)          │
│      .Order(created_at, id).Limit(batch).Find(&events)             │
│    • for each event: handler.Handle(ctx, event)                    │
│      • logSink: 输出结构化日志                                      │
│      • webhookSink: POST 到配置的 webhook URL                      │
│      • notifSink: 创建通知 + SSE 推送                               │
│    • 成功: r.db.MarkPublished(ids)                                 │
│    • 失败: r.db.MarkFailed(id, retryCount, maxRetries)            │
│        • 达到 maxRetries → dead_letter = true                      │
└───────────┬────────────────────────────────────────────────────────┘
            │
┌───────────▼────────────────────────────────────────────────────────┐
│ 4. 通知系统 (通知 sink 内部)                                        │
│    • CreateNotification in DB (独立于原始请求的 goroutine)           │
│    • dispatcher.Publish(recipient_uid, type, data)                │
│        └─ hub.pub(uid, SSEEvent{ID, Type, Data})                  │
│        └─ 对每个订阅者 chan: select { case ch <- evt: default: }  │
│    • 如果没有 SSE 连接 → notification 留在 DB, 下次 SSE 连接时     │
│      replayMissed 批量推送                                          │
└───────────┬────────────────────────────────────────────────────────┘
            │
┌───────────▼────────────────────────────────────────────────────────┐
│ 5. 客户端 SSE 接收                                                 │
│    • EventSource.onmessage → parse → 更新 UI                       │
│    • last_event_id = notification.created_at 的毫秒时间戳           │
│    • 断线重连: Last-Event-ID 头 → replayMissed                     │
└────────────────────────────────────────────────────────────────────┘
```

**问题 1a：出箱事件 handler 的执行环境。** `relay.go` 中 handler 在 `Tick` 循环中串行执行——所有事件被 drain 后在同一个 goroutine 中逐个 `Handle`。如果某个事件处理很慢（如 webhook `http.Post` 超时 30s），会延迟同一批次中所有后续事件的处理。

```go
// relay.go 伪代码：
func (r *Relay) drain(ctx context.Context) {
    events := r.repo.FetchUnpublished(ctx, r.batch)
    for _, e := range events {
        if err := r.handler.Handle(ctx, e); err != nil {
            r.repo.MarkFailed(ctx, e.ID, ...)
        } else {
            r.repo.MarkPublished(ctx, e.ID)
        }
    }
}
```

**影响：** 慢事件处理延迟了同批次的快速事件。如果 webhook 端点持续超时，同批次中创建通知的 `notifSink` 也会被延迟。

**建议：** 使用 worker pool 并发处理事件（`semaphore.NewWeighted` 控制并发度），保证 `notifSink` 和 `webhookSink` 不相互阻塞。注意：并发处理时需要保证 `MarkPublished` 是幂等的。

**问题 1b：`notifSink` 的 DB 操作在 relay 的事务外。** `CreateNotification` 是独立的 `db.Create()` 调用，不在 relay 的 drain 事务中。如果 `CreateNotification` 成功但 `MarkPublished` 失败，同一条 outbox 事件会被重放 → `CreateNotification` 执行两次 → 用户收到**重复的通知**。

```go
// notifSink:
repo.Create(ctx, &domain.Notification{...})  // 独立事务
// 如果下面 MarkPublished 失败，下次 drain 重放此事件 → 重复通知
```

**影响：** 在 relay crash 或 DB 短暂不可用的情况下，用户可能收到重复通知。

**建议：** 通知创建应该使用 `INSERT ... ON CONFLICT DO NOTHING` 语义（基于 outbox event ID 做唯一约束），或者在创建通知时记录 `outbox_event_id` 用于去重。

**问题 1c：原始的 `message.sent` 事件已经触发通知，但 `SendSystem` 的调用者在调用后也手动创建了通知？** 需要检查 `notificationService` 是否监听 `message.sent` 事件。如果 `SendSystem` 被通知层调用，在 `message.sent` 又被通知层消费 → **循环递归**。

查看 `notifSink`：

```go
// adapter.go — notifSink 是 handler，监听 outbox 事件 → 创建 notification
```

如果 `message.sent` → notifSink → `CreateNotification` → outbox event `notification.created` → notifSink 再次消费 → 无限循环？需要确认 notifSink 是否过滤事件类型。

实际上，我们看到的 notifSink 实现应该只处理 `message.sent`、`comment.new` 等事件，忽略 `notification.created` 事件，以避免循环。需要确认是否有这种过滤。

### 1.2 文件上传的完整生命周期

```
┌────────────────────────────────────────────────────────────────────┐
│ 1. PresignUpload                                                    │
│    Auth → s.store.PresignUpload(ctx, actor, name, contentType)     │
│    → 返回 presigned URL（S3 5min / local HMAC 签名）                │
└───────────┬────────────────────────────────────────────────────────┘
            │
┌───────────▼────────────────────────────────────────────────────────┐
│ 2. 客户端直接 PUT 到 presigned URL                                  │
│    • S3/MinIO：标准 S3 PUT 操作                                    │
│    • Local：HMAC 签名验证 → 写入本地文件系统                         │
└───────────┬────────────────────────────────────────────────────────┘
            │
┌───────────▼────────────────────────────────────────────────────────┐
│ 3. CompleteUpload                                                  │
│    Auth → 验证 storage key 前缀匹配 actor → 读取文件流 →            │
│    SHA256 哈希 → 计算大小 → s.files.Create(...)                     │
│    • 去重检查: FindByChecksum → 如果存在则删除刚上传的 blob          │
│    • 创建 file row + 存储引用计数递增                                │
└────────────────────────────────────────────────────────────────────┘
```

**问题：Presign → Complete 之间存在竞态条件。** 用户在步骤 1 获得 presigned URL，在步骤 3 调用 `CompleteUpload` 时。但文件可能在步骤 1 之后被 GC sweeper 删除（如果 storage key 被另一个已删除文件使用过）。或者另一个用户可能在 TTL 内通过 presigned URL 写入不同的内容。

**建议：** presigned URL 的 storage key 使用随机 UUID（而不是 `actor/name_hash`），避免 key 冲突。`CompleteUpload` 在找不到 blob 时应返回清晰错误，如 "upload session expired or file not found"。

### 1.3 评论创建的生命周期

```
handler → service.CreateComment(ctx, input)
  1. mustActor → owner_uid
  2. 验证 target_type/target_id 存在
  3. 嵌套检查:
     - parent_id == nil AND root_id == nil → 顶级评论 ✅
     - parent_id != nil AND parent.root_id == nil → 二级（回复顶级）✅
     - parent_id != nil AND parent.root_id != nil → 三级 → nesting_too_deep ❌
  4. 创建评论 row + 递增 target 的 reply_count（在事务中）
  5. 创建 outbox event `comment.created`
  └→ relay → notifSink → 通知 target_uid + SSE
```

**问题：`reply_count` 递减缺失。** 当回复被软删除时，`reply_count` 不会递减。前三次分析已提及但未深入探讨影响。

```sql
-- 现有逻辑：CREATE reply 时递增 reply_count
UPDATE files SET reply_count = reply_count + 1 WHERE id = ?;
-- 但 DELETE 时没有：
-- UPDATE files SET reply_count = reply_count - 1 WHERE id = ?;
```

**影响：** 长期运行后，`reply_count` 高估实际回复数。用户界面显示"12 条回复"但实际可见（未删除）回复可能只有 9 条。

**建议：** 在 `SoftDelete` 事务中加上 `reply_count = reply_count - 1`。由于 `reply_count >= 0`（已有默认值 + CHECK 约束待添加），递减到负数会报错——需要确保不会过度递减。

---

## 2. 并发模型深度分析

### 2.1 Goroutine 生命周期全景

| Goroutine | 启动位置 | 数量 | 取消方式 | 生命周期 |
|-----------|---------|------|---------|---------|
| HTTP server | `app.go:Start` → `httpServer.ListenAndServe` | 1 | `httpServer.Shutdown(ctx)` | 随 app 退出 |
| gRPC server | `app.go:Start` → `grpcServer.Serve` | 1 | `grpcServer.GracefulStop()` | 随 app 退出 |
| Outbox relay Tick | `relay.Start(ctx)` → `time.NewTicker` loop | 1 | `ctx.Done()` → `close(done)` | app 退出时停止 |
| GC sweeper Tick | `sweeper.Start(ctx)` → `time.NewTicker` loop | 1 | `ctx.Done()` → `close(done)` | app 退出时停止（默认 disabled） |
| SSO TokenCache cleaner | `tokenCache.RunCleaner(ctx)` | 1 | `ctx.Done()` | app 退出时停止 |
| Idempotency cleaner | **从未启动**（`store.RunCleaner()` 存在但未调用） | 0 | N/A | N/A |
| Rate limit cleaner | `ratelimit.RunCleaner(ctx)` | 1 | `ctx.Done()` | app 退出时停止 |
| HTTP handler goroutine | Gin 自动池化 | N | `c.Request.Context().Done()` | 请求结束时终止 |

### 2.2 锁使用分析

```
sync.Mutex:
  - Hub.uidSubs.mu       — 每个 uid 一个锁（分片）
  - service.mu           — service.go 中的字段
  - Registry.mu           — metrics 全局锁

sync.Map:
  - Hub.subs             — uid → *uidSubs 映射
  - TokenCache.cache      — token → entry 缓存

atomic.Int64:
  - Hub.nextID            — 单调递增订阅者 ID
  - Hub.closed            — Hub 是否关闭

sync.Once:
  - sso.Client.dialOnce   — 懒连接 gRPC（在第一次 RPC 时连接）

channel:
  - hub.subs[uid][id]     — buffered(16) SSE 事件通道
```

**问题 2a：`Registry.mu` 是全局锁。** 所有 HTTP → Metrics 写入都竞争这把锁。在 10k QPS 下（每个请求导致 `ObserveHTTP` → `mu.Lock()` + histogram + counter），锁竞争可能显著。

**测量：** `ObserveHTTP` 内部：
1. 计算 secs
2. Lock
3. map 写入（reqKey）
4. histogram bucket update
5. Unlock

10k QPS → 每 100μs 一次 Lock/Unlock。如果每个操作耗时 2μs（map + slice update），锁争用是可以接受的。但如果 histogram 数组较大（11 buckets）且每次遍历所有 bucket，操作时间 ~5μs，在 10k QPS 下锁利用率 ~5%——**当前风险较低**。推荐在 100k QPS 前保持当前设计。

**问题 2b：`service.mu` 的用途。** 查看 `service.go`：

```go
type service struct {
    mu     sync.Mutex
    ...
}
```

`mu` 被用在哪些操作？读取 `service.go`：

```go
func (s *service) SetUserDirectory(dir UserDirectory) {
    s.mu.Lock()
    s.dir = dir
    s.mu.Unlock()
}
```

如果 `mu` 只被 `SetUserDirectory` 使用（启动配置），那没有争用问题。但如果用于保护其他状态，需要确认是否所有读操作也加锁。

**问题 2c：Hub 的 `pub` 中是 per-uid lock。** ✅ 这是**优秀的设计**——分片锁避免了针对不同 uid 的发布/订阅之间的竞争，只有操作同一 uid 时才会序列化。

### 2.3 Channel 模式分析

**SSE 事件通道：** `chan SSEEvent` 的 buffer 大小是 **16**。`pub` 使用 `select { case ch <- evt: default: }` 非阻塞模式。

**风险：断线重连后的 event 风暴。** 如果用户离线 5 分钟（SSE 断开），期间产生了 50 个通知。重新连接 SSE 时：

1. `replayMissed` 通过 `ListSince` 拉取错过的 50 条通知 → 依次写入通道
2. 客户端还没有建立 EventSource → 前 16 条填满 buffer → 第 17 条开始 `default` 分支触发 → silent drop
3. 客户端 EventSource 建立后收到前 16 条，丢失 34 条

**建议：** `replayMissed` 应该在用户 EventSource 已建立后（即 SSE 循环开始时）逐条写入 `c.Writer`，而不是通过 Hub 通道发送。当前代码的实现：

```go
// events.go: replayMissed → 直接 fmt.Fprintf(c.Writer, ...)
```

👆 **当前实现是正确的！** `replayMissed` 直接写入 HTTP ResponseWriter，不经过 Hub channel。只有实时事件（`<-sub` 中接收的）才走 channel。所以离线后的 event 风暴风险**不存在**。✅

### 2.4 潜在竞态条件

**竞态 1：文件去重删除 vs 并发的 DownloadURL。** 两个用户同时上传相同文件：

1. 用户 A 上传 → SHA256: `abc` → 写入 storage key `/a/file`
2. 用户 B 上传 → SHA256: `abc` → 写入 storage key `/b/file`
3. A 的 CompleteUpload: 发现 checksum `abc` 已存在（B 刚写入的？还是之前的？）
4. A 的代码：_ = s.store.Delete(ctx, key) 删除刚写入的 blob ×
5. 但 B 可能同时完成了 CompleteUpload，也调用了 Delete → **B 的 blob 被删**

**当前机制的缓解：** 两个用户写入不同 storage key（`/a/file` vs `/b/file`），所以 A 删除的是自己的 `/a/file`，B 删除的是自己的 `/b/file`——不对对方的 blob 产生影响。但如果去重逻辑检测到 checksum 匹配，A 会**复用** B 的 storage key，然后删除自己的 blob——这是安全的，因为 A 的 key 是唯一的。✅

**竞态 2：MarkRead 并发标记。** 两个设备同时 `MarkRead` 同一消息：
1. 设备 A: `Get` → `ReadAt == nil`
2. 设备 B: `Get` → `ReadAt == nil`
3. 设备 A: `MarkReadWithEvent → UPDATE SET read_at = now()`
4. 设备 B: `MarkReadWithEvent → UPDATE SET read_at = now()`（再次更新）

最终 `read_at` 是最晚的。消息只被标记一次已读（由于 `if m.ReadAt != nil { return nil }` 在服务层检查，但两个 goroutine 同时检查时都看到 nil）。**实际影响：** 产生两次 `message.read` outbox 事件 → 发送方收到两次已读通知。

**建议：** 在 DB 层使用条件更新 `UPDATE messages SET read_at = $1 WHERE id = $2 AND read_at IS NULL` + `RowsAffected == 0` 判断是否应跳过通知发送。服务层也可以做，但 DB 层提供了更强的保证。

---

## 3. 缓存策略分析

### 3.1 当前所有缓存一览

| 缓存 | 位置 | 类型 | 容量 | TTL | 失效策略 |
|------|------|------|------|-----|---------|
| SSO Token 缓存 | `sso/token_cache.go` | `map[string]*tokenEntry` | **无限制**（可能 OOM） | TokenCacheTTL (60s) + StaleTTL (300s) | RunCleaner 每 60s 清理过期项 |
| SSO User 缓存 | `sso/user_cache.go` | `map[string]*userEntry` | **无限制** | UserCacheTTL (5min) + StaleTTL (10min) | RunCleaner 每 5min 清理 |
| SSE Hub | `hub.go` | `sync.Map[uid]*uidSubs` | **无限制** | 无 TTL（连接断开时清除） | 客户端断开时 unsub |
| Rate limit | `ratelimit.go` | `sync.Map[uid]*bucket` | **无限制** | Sliding window（无全局上限） | RunCleaner 清理过期 bucket |
| Idempotency | `idempotency/store.go` | 数据库（idempotency_keys 表） | 表行数 | ExpiresAt | RunCleaner **未启动** |

### 3.2 严重的 OOM 风险：SSO Token 和 User 缓存

```go
// token_cache.go
type TokenCache struct {
    mu    sync.Mutex
    cache map[string]*tokenEntry  // ← 无限制增长
}
```

**场景：** 假设每秒有 1000 个不同的 SSO token 通过 aero-id 被验证（1000 RPS，不同的用户/会话）。每个 token 在缓存中停留 60s（`TokenCacheTTL`）+ 300s（`StaleTTL`）= 360s。缓存峰值 = `1000 × 360 = 360,000` 个条目。

每个条目：key ~100B（`string` of JWT-like token + uid），entry struct ~80B。总计 = 360k × 200B ≈ **72MB**。这对于一个 1GB 的服务来说不是问题。

但如果 RPS 增长到 10k（不同 token）：360 万条目 × 200B = **720MB**。在 512MB ~ 1GB 的容器中，这可能接近 OOM。

**User 缓存同理。** `BatchGetUsers` 可能被频繁调用（每次 inbox 列表 > 1KB 新用户数据）。如果每天活跃 100 万用户，每个用户信息大约 200B，UserCacheTTL = 5min，峰值 = 100 万 × 200B = **200MB**。

**建议：**
1. 为内存缓存添加容量上限（如 max 50k entries），使用 LRU 淘汰（`github.com/hashicorp/golang-lru`）
2. 或者在配置中暴露缓存大小限制（`cache.max_entries: 50000`）
3. `RunCleaner` 目前只删除过期条目，不压缩 map 底层——Go map 的 GC 问题：即使删除了 90% 的条目，map 的底表（buckets）不会缩小。建议定期（如每 30 分钟）重建 map

### 3.3 Cache Stampede 风险

**SSO Token Cache** 使用 `singleflight` 吗？看代码：

```go
// client.go
func (c *Client) ValidateToken(ctx context.Context, token string) (string, error) {
    // 从缓存获取
    if uid, ok := c.tokenCache.Get(token); ok {
        return uid, nil
    }
    // 缓存 miss → 调用 SSO
    resp, err := c.validateUpstream(ctx, token)
    ...
}
```

如果 1000 个 goroutine 同时请求同一个新 token（token 刚到期，所有请求同时 miss）：
1. 1000 个 goroutine 同时进入 `validateUpstream`
2. 1000 个并发 gRPC 调用 SSO 验证同一个 token
3. SSO 在毫秒级返回（1000 QPS 对 SSO 可能可以接受）
4. 但 999 个调用是浪费的——它们本可以共享第一次调用的结果

**建议：** 使用 `golang.org/x/sync/singleflight` 或实现类似模式——在缓存 miss 时，只让一个 goroutine 去调用 SSO，其他 goroutine 等待这次调用的结果。

### 3.4 缺少的缓存

**当前未缓存但适合缓存的资源：**

| 资源 | 建议 TTL | 为什么适合缓存 |
|------|---------|---------------|
| `/v1/messages/unread-count` | 5-10s | 用户反复刷新未读数但不希望每次都 COUNT |
| `/v1/me` 用户信息 | 5min | 用户自信息不频繁变化 |
| `/v1/blocks` | 30s | 检查当前用户是否被目标用户屏蔽 |
| `file_urls`（DownloadURL 重定向） | presigned URL 有效期内 | 同一文件对同一用户的下载 URL 不变化 |

**不建议缓存：**
- `/v1/me/inbox` → 分页依赖 cursor，每个用户每个设备的分页状态不同
- `/v1/notifications` → 实时性要求高
- `/v1/conversation/:uid` → 高频变化

---

## 4. 文档完备性分析

### 4.1 `README.md` vs 代码

**需要验证：** README 中的 API 文档与实际 handler 代码是否一致。特别关注：

- HTTP 端点表中列出的端点是否都在 `router.go` + 各 handler 的 `RegisterRoutes` 中实现了？
- 请求/响应格式（JSON 字段名、分页响应结构）是否与实际输出一致？

### 4.2 `ARCHITECTURE.md` vs 代码

`docs/ARCHITECTURE.md` 描述的高层架构：

```
handler/{http,grpc}  →  service  →  repository  →  Postgres
                                    └→  storage  →  local | s3
                                    └→  sso      →  gRPC
```

**对应代码：** 当前 `app.go` 使用 Suite A（`internal/service/`, `internal/repository/`），架构图与代码一致。但最近添加的 Suite B（`internal/{file,comment,message,...}/`）是**架构图中不存在**的包结构。

**如果 README 中的架构图没有更新以反映 Suite B，文档就过时了。**

### 4.3 `DECISIONS.md` vs 代码

`docs/DECISIONS.md` 记录架构决策。需要检查的重大决策：

- 为什么选择 GORM 作为 ORM？
- 为什么使用双传输（HTTP + gRPC）？
- 为什么审计日志写入是 best-effort（`_ = s.audit.Log(...)`）？
- 为什么 `idempotency.RunCleaner` 被定义但不启动？
- 为什么 Suite B 存在但未被 ref？（如果后续添加的，但未完成迁移）

如果 `DECISIONS.md` 中没有记录这些决策，它就无法帮助新开发者理解代码库为何处于当前状态。

### 4.4 文档与代码的一致性问题

| 问题 | 位置 | 严重性 |
|------|------|--------|
| Suite B 包在架构图中未体现 | `ARCHITECTURE.md` | High |
| Idempotency cleaner 设计了但未启动——无决策记录 | `DECISIONS.md` | Medium |
| 端点表可能遗漏新增端点 | `README.md` | Medium |
| 审计日志 best-effort 写入无决策理由 | `DECISIONS.md` | Low |

---

## 5. 跨领域关注点一致性审查

### 5.1 错误处理模式一致性

**模式检查：** 所有 `service.*` 的方法是否遵循相同的错误转换模式？

✅ **示例（正确模式）：**
```go
func (s *xService) Method(ctx context.Context, ...) error {
    actor, err := mustActor(ctx)
    if err != nil { return err }
    // ... 业务逻辑 ...
    if err := s.repo.DoSomething(ctx, ...); err != nil {
        return apperr.Internal("x.failed", "msg").Wrap(err)
    }
    return nil
}
```

**不一致的实例：** `service/file_share.go` 是否使用与 `service/message.go` 相同的模式？

**问题 5a：`_ = s.audit.Log(...)` 的变体不一致。** 有些地方 silent drop audit 错误，有些根本不写：

```go
// message.go Delete:
if s.audit != nil {
    _ = s.audit.Log(ctx, &domain.AuditEvent{...})  // silent drop
}

// file.go Create:
// 完全不写 audit log！
```

文件上传（CreateFile）——一个需要审计的关键操作（谁上传了什么文件、大小多少）——**完全没有审计日志**。

**问题 5b：上下文只传递 actor，不传递其他元数据。** `service.WithActor(ctx, uid)` 只注入了 uid。但 `access_log` 记录了 `request_id`、`trace_id`、`actor_uid`——这些元数据没有传递到服务层。如果服务层需要记录日志或创建事件，它无法关联到原始的请求 ID。

**建议：** 扩展 `ServiceContext` 结构体，包含 `request_id`、`trace_id`、`user_agent`、`client_ip` 等元数据，在 `WithActor` 时一并注入。

### 5.2 日志模式一致性

**当前日志点分布：**

| 位置 | 日志内容 | 级别 |
|------|---------|------|
| `middleware/access_log.go` | HTTP 请求 | INFO |
| `handler/grpc/server.go` | gRPC 请求（通过拦截器） | INFO |
| `outbox/relay.go` | Drain 结果 | INFO |
| `storage/gc/sweeper.go` | GC pass | INFO |
| `service/*.go` | 很少日志 | - |
| `repository/*.go` | 很少日志 | - |

**问题：服务层和仓库层缺少结构化日志。** 当审计日志写入失败时，`_ = s.audit.Log(...)` silently drops——没有 WARN 日志。当数据库查询异常慢时（虽然 GORM 可能慢日志），没有应用层日志帮助定位。

### 5.3 追踪传播完整性

**HTTP → gRPC 的追踪传播：** HTTP 请求的 trace context 通过 `extract(HTTP headers) → span` 进入，然后 `c.Request = c.Request.WithContext(ctx)` 传递到服务层。

但**服务层调用 SSO 时**：

```go
// sso/client.go
resp, err := c.validateUpstream(ctx, token)
```

`ctx` 是从 HTTP/gRPC handler 传递下来的 context，包含了 span。所以 OTel span 是连续的。✅

但**出箱 relay 的 context**：

```go
// relay.go
func (r *Relay) Start(parent context.Context) {
    go func() {
        ticker := time.NewTicker(r.interval)
        for {
            select {
            case <-parent.Done():
                ...
            case <-ticker.C:
                r.drain(ctx) // ctx 来自哪里？
            }
        }
    }()
}
```

前三次分析指出 relay 使用 `context.Background()`。**实际上，`app.go` 传递的是 `rootCtx`（由 `signal.NotifyContext` 创建的 context）。** 但如果 `rootCtx` 上没有 span，relay 内的所有操作（DB 查询、SSO 调用、通知创建）都在**无追踪 span** 的情况下执行。

**建议：** 在 relay 的 drain 循环中启动一个包装 span：`tracer.Start(ctx, "outbox.drain")`，并确保 `sso/client.go` 中的 `BatchGetUsers` 也使用带 span 的 context。

---

## 6. 高价值扩展方向（全新方向，前六次未覆盖）

---

### 🎯 方向 A：会话内搜索（Conversation Search）

**概念：** 允许用户在特定对话内搜索消息内容，而不是全局搜索所有消息。

**为什么需要它：**
1. **当前 `Search` API 是全局搜索。** `GET /v1/search?query=hello` 返回所有匹配的消息，但不按对话分组。用户想查找"张三发给我的关于合同的讨论"时需要翻遍所有搜索结果。
2. **会话搜索的索引已经存在。** `messages` 表的 `search_vector` GIN 索引可以配合 `(from_uid = $1 AND to_uid = $2) OR (from_uid = $2 AND to_uid = $1)` 条件使用——无需新索引。
3. **UX 价值极高。** 对于有大量对话的用户（客服人员、项目经理），会话内搜索是"救命的"生产工具。
4. **实现简单。** 在现有 `Search` handler 中增加 `?with_uid=` 参数，服务层追加参与者条件即可。

**实现要点：**
- API：`GET /v1/search?query=hello&with_uid=abc123` 或 `GET /v1/conversations/:uid/search?query=hello`
- SQL：`WHERE search_vector @@ plainto_tsquery('simple', $query) AND ((from_uid = $me AND to_uid = $peer) OR (from_uid = $peer AND to_uid = $me)) AND deleted_at IS NULL`
- `$me` 来自 token（actor），`$peer` 来自请求参数
- 排序：`created_at DESC`（按时间排序）或 `ts_rank(search_vector, query) DESC`（按相关性排序）
- 不需要新表、新索引、新迁移

---

### 🎯 方向 B：消息转发 / 引用回复（Forward / Quote Reply）

**概念：** 用户可以将一条消息转发给另一个用户，或在回复时引用原始消息内容（quote reply）。

**为什么需要它：**
1. **引用回复是即时通讯的基础功能。** Telegram/Slack/Discord 都支持回复时引用原文。缺少此功能使得对话上下文难以跟踪（"你是在回复哪条消息？"）。
2. **转发是从 1:1 消息扩展到"分享"的第一步。** 当前消息只能发送给一个人。转发支持将有趣的消息分享给另一个用户——这是内容传播的起点。
3. **数据模型已预留。** `messages.file_id` 支持附件转发。`messages.Meta` 可以携带 `reply_to_message_id` 或 `forwarded_from` 字段。
4. **实现低侵入性。** 引用回复只需要在 `Meta` 中增加 `reply_to_id`，在 UI 中显示被引用的消息摘要。转发需要复制消息内容（或引用原始消息 ID + 发送者）。

**实现要点：**
- API：
  ```
  POST /v1/messages  { "to_uid": "abc", "content": "回复内容", "reply_to_id": "msg-uuid" }
  POST /v1/messages  { "to_uid": "abc", "forward_from": "msg-uuid" }
  ```
- 引用回复：`meta.reply_to.message_id`, `meta.reply_to.snippet`（原文前 200 字符摘要）
- 转发：`meta.forwarded.from_uid`, `meta.forwarded.message_id`, `meta.forwarded.content`, `meta.forwarded.file_id`
- 权限检查：引用转发的消息必须是参与者的（调用 `canReadMessage`）
- 通知：引用回复触发 `message.sent`（同现有 flow），但携带 `reply_to_id` → UI 显示"张三回复了你"+"原文摘要"
- 不需要新表、不需要新迁移、不需要新 outbox 事件类型

---

### 🎯 方向 C：图片 EXIF 数据剥离（隐私保护）

**概念：** 当用户上传 JPEG/PNG/WebP 图片时，自动剥离 EXIF 元数据（GPS 位置、相机型号、拍摄时间等），保护用户隐私。

**为什么需要它：**
1. **隐私合规趋势。** 越来越多的平台（WhatsApp、Signal、Twitter）默认剥离图片 EXIF 数据。泄露 GPS 坐标可能导致严重的安全问题。
2. **实现路径清晰。** 在文件上传的 `CompleteUpload` → `s.files.Create` 之间增加一个异步或同步的"图片处理"步骤。
3. **可作为插件架构。** 当前 `FileStorage` 接口可以扩展为包含 `Process(ctx, key) error` 方法，允许在存储后处理文件。
4. **与病毒扫描方向 (B in 6th) 互补。** 两种管道都是文件上传后的异步处理。

**实现要点：**
- 仅在 `mime_type` 以 `image/` 开头时处理
- 使用 Go 的 `image` 标准库或 `github.com/rwcarlsen/goexif/exif` 库读取和剥离 EXIF
- 处理时机：
  - 同步：在 `Create()` 中的 `s.store.Put()` 之后、`s.files.Create()` 之前（阻塞请求，但用户可接受 50-200ms 的额外延迟）
  - 异步：通过 outbox 事件 `file.uploaded` → `fileProcesor` 处理（不阻塞请求，但用户在短时间内可下载含 EXIF 的原文件）
- 异步更优：使用 outbox 事件 `file.uploaded` 触发处理，处理完成后更新 `file.meta.stripped_exif = true`
- 元数据可保留在 `meta.original_exif_*` 中，仅在用户显式请求时返回（`?include_exif=true`）
- 配置：`processing.exif_strip: true`（默认启用），`processing.exif_strip_for_types: ["image/jpeg", "image/png"]`

---

### 🎯 方向 D：会话导出（Conversation Export）

**概念：** 允许用户导出整个对话为 JSON、TXT、或 HTML 文件。支持选择性导出（日期范围、包含/排除文件附件）。

**为什么需要它：**
1. **GDPR 数据可移植性。** 数据导出是欧洲 GDPR 的要求。用户有权利以结构化、机器可读的格式获取自己的数据。
2. **用户自主权。** 即使不在合规要求下，用户也应该能导出自己的对话——用于备份、迁移到其他平台、法庭证据等。
3. **可以通过消息 API 组合实现。** 导出 = 分页遍历对话消息 + 格式化为目标格式 → 上传为文件 → 返回下载链接。
4. **实现不需要新基础设施。** 导出是一个异步任务（对话很长时可能需要 10s+ 的处理时间）。可以使用 outbox 事件 + 后台 goroutine 管道。

**实现要点：**
- API：`POST /v1/conversations/:uid/export` → 返回 `{ task_id: "uuid", status: "pending" }`
- 后台 goroutine 消费 export 任务 → 遍历全部消息 → 生成文件
- 导出格式：
  - JSON：`{ "participants": [...], "messages": [{...}, ...] }`（最完整，包含 metadata）
  - TXT：`[2026-06-30 10:00] Alice: Hello\n[2026-06-30 10:01] Bob: Hi`（可读性高）
  - HTML：类似 iMessage/WhatsApp 导出的样式（附 CSS）
- 导出完成后：`POST /v1/notifications` → 用户收到"导出完成"通知 + `export_file_id`
- options: `?format=json|txt|html&include_files=true&since=2026-01-01&until=2026-06-30`
- 文件被放入用户自己的文件列表（`files` 表），可下载和删除

---

### 🎯 方向 E：Webhook 事件重试管理（Dead Letter Queue UI）

**概念：** 为无法送达的 webhook 事件提供管理界面和重试机制——查看死信队列、手动重试、查看投递日志。

**为什么需要它：**
1. **当前 webhook 有 retry 但无管理入口。** `outbox_events` 表已有 `retry_count`、`last_retry_at`、`dead_letter` 列（migration 018）。relay 自动重试直到 `maxRetries`，然后标记 `dead_letter`。但运维人员**无法查看**哪些事件进入了死信队列，**无法手动重试**它们。
2. **死信事件包含业务关键数据。** 如果 webhook URL 配置错误，`message.sent` 事件一直在重试并最终死信——但消息本身已经被成功创建和投递。只有 webhook 消费者错过了通知。运维人员需要知道哪些事件死了、为什么死了、什么时候死的。
3. **开发者体验。** Webhook 消费者开发时，需要查看投递日志来调试接收端代码。当前没有`/debug/webhooks/events` 端点来查看出箱事件。
4. **当前数据已存在——只需要 API。** `outbox_events` 表包含所有需要的数据。只需要：
   - `GET /v1/admin/webhooks/events?status=dead_letter`
   - `POST /v1/admin/webhooks/events/:id/retry` → `UPDATE SET dead_letter=false, retry_count=0`
   - `GET /v1/admin/webhooks/events/:id` → 查看事件详情 + payload

**实现要点：**
- 管理员认证（当前无 admin 角色，可以先用 `actor == configured_admin_uid` 的简单检查）
- API：
  - `GET /v1/admin/outbox/events?status=dead_letter|pending|all&event_type=message.sent`
    - 返回 `{ events: [{id, event_type, retry_count, last_retry_at, created_at, dead_letter}], next_cursor }`
  - `GET /v1/admin/outbox/events/:id` → 返回完整事件 + payload
  - `POST /v1/admin/outbox/events/:id/retry` → 重试一个死信事件
  - `POST /v1/admin/outbox/events/retry-all` → 重试所有死信事件（谨慎——可能导致 webhook 风暴）
- 安全：这个端点应该绑定在 admin 端口（`:9091`）或要求 admin token/header
- 可选的 webhook 投递日志：记录每次 `POST` 到 webhook 的响应状态码和延迟

---

## 7. 本轮结论：30 个扩展方向全景

经过七轮审查，已覆盖 **35 个高价值扩展方向**，分类总结如下：

```
IMMEDIATE（低实现成本，高用户价值）:
  • 表情反应（表已存在，API 未启用！）——方向 A（第 7 轮）
  • 会话内搜索——方向 A（第 7 轮）
  • 消息草稿——方向 B（第 5 轮）
  • 在线状态 / 打字指示——方向 A（第 5 轮）

SHORT TERM（中等成本，产品差异化）:
  • 通知偏好设置——方向 E（第 2 轮）
  • 会话归档/静音——方向 B（第 2 轮）
  • 消息引用回复——方向 B（第 7 轮）
  • 批量操作——方向 D（第 6 轮）
  • 数据分析管线——方向 D（第 5 轮）
  • 已读回执历史——方向 E（第 5 轮）

MEDIUM TERM（高成本，平台能力）:
  • 群聊——方向 A（第 1 轮）
  • 多租户/组织空间——方向 A（第 2 轮）
  • 可插拔认证——方向 δ（第 4 轮）
  • API Keys / 服务账号——方向 C（第 5 轮）
  • 文件病毒扫描——方向 B（第 6 轮）
  • EXIF 剥离——方向 C（第 7 轮）
  • gRPC-Web——方向 β（第 4 轮）

LONG TERM（基础设施投入）:
  • 数据生命周期管理——方向 γ（第 4 轮）
  • 分布式限流——方向 E（第 1 轮）
  • 跨区域部署——方向 E（第 3 轮）
  • 硬删除回收站——（第 3 轮）
  • SLO 框架——方向 ε（第 4 轮）
  • 自诊断仪表盘——方向 E（第 6 轮）
```
