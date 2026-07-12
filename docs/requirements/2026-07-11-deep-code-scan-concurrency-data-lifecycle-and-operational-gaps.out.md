好的，现在我已经看到了完整的 Like/Unlike 实现。

```go
// Like 使用 ON CONFLICT DO NOTHING + RowsAffected == 1 保护
res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(...)
if res.RowsAffected == 1 {
    tx.Model(...).UpdateColumn("like_count", gorm.Expr("like_count + 1"))
}

// Unlike 使用 RowsAffected == 1 + GREATEST 保护
res := tx.Where(...).Delete(&commentLikeRow{})
if res.RowsAffected == 1 {
    tx.Model(...).UpdateColumn("like_count", gorm.Expr("GREATEST(like_count - 1, 0)"))
}
```

**这个实现是完全线程安全的。** `DELETE` 在 Read Committed 下会等待竞争事务提交，然后重新评估可见性（如果另一事务先删除了该行，则 `RowsAffected = 0`）。此分析在此处是错误的。

**文件去重竞争**是方向二中唯一经确认的真实写偏斜：

```go
// file.go:71-82 — 纯 SELECT，无 FOR UPDATE
if existing, ferr := s.files.FindByChecksum(ctx, actor, checksum); ferr == nil {
    // dedup hit
} else if !stderrors.Is(ferr, repository.ErrNotFound) {
    // error
}
// ... 继续创建新文件
```

无唯一约束（`migrations/000001: CREATE INDEX idx_files_checksum ON files(checksum)` — 仅是索引），无 `FOR UPDATE`。两个相同的并发上传会产生两个文件行和两个 blob。

### ✅ 方向三：SSE 连接管理 — 全部确认，P1 评级正确

| 风险 | 验证 |
|------|------|
| **无每 uid 限制** | `hub.go:Subscribe` — 无上限 |
| **无全局限制** | 同上 |
| **僵尸连接风险** | `events.go:68-87` — 仅依赖于 `c.Request.Context().Done()`（在 gin 中，在写失败之前无法检测到 TCP 半关闭）。ping 每 30 秒发送一次，因此最多需要 30 秒才能检测到断开连接。 |
| **无活跃超时** | 循环没有 context deadline — 连接理论上是永久的 |
| **缓冲区硬编码 16** | `hub.go:46` — `make(chan SSEEvent, 16)` |

需要注意的是：gin 的 `http.Server{WriteTimeout}` 在 config 中设置了，但由于正常的 SSE 写入（数据 + ping），写入超时会不断被重置。在配置中为 0 的 `ReadTimeout` 不适用于 SSE（客户端从不发送请求体）。因此，实际上连接可以是永久性的。

### ⚠️ 方向四：数据生命周期 — 大部分正确，需要重要修正

你对哪些表缺少软删除/TTL 清理的目录是准确的。但是，**有几张表是故意不通用的**：

| 实体 | 你的说法 | 实际情况 |
|------|---------|---------|
| `messages` | 软删除，无清理 | ✅ 正确 |
| `comments` | 软删除，无清理 | ✅ 正确 |
| `notifications` | "无软删除"（硬删除未实现） | ✅ 两种方式都不是 — 行会永久累积 |
| `outbox_events` | 无物理删除 | ✅ `MarkPublished` + `dead_letter`；没有 TTL 清除 |
| `audit_events` | 无 TTL | ✅ 正确 |
| `comment_likes` | "体积小" | ✅ 正确 — 这是 GORM 未映射的联结表；因此也是正确的 |
| `idempotency_keys` | ✅ 已实现 | ✅ 确认：`stopIdemClean := idemStore.RunCleaner(time.Hour)` |

需要为每条消息添加下行迁移可能成本高昂（数十亿行），但在此阶段进行设计是合理的。该分析正确地指出了这一点。

### ✅ 方向五：Outbox 处理程序超时 — 已确认，但发现被高估

**webhook 实际上是有超时保护的。** 查看代码：

```go
// webhook.go:57
client: &http.Client{Timeout: cfg.Timeout},  // ← HTTP 客户端级别超时
// webhook.go:89
req, err := http.NewRequestWithContext(ctx, http.MethodPost, ...)  // ← context 传播
// webhook.go:94
resp, err := h.client.Do(req)  // ← 受 Client.Timeout 限制
```

`http.Client.Timeout` 即使 context 是 `context.Background()` 也能工作。**webhook 不可能无限期挂起。**

但**真正的风险在于 dispatcher（在 chain 中排在第一位）的 DB 调用**：

```go
// notification/dispatcher.go:54
if err := d.notifs.Create(ctx, n); err != nil {  // ← context.Background()，无超时
    return err
}
// dispatcher.go:43
blocked, err := d.blocks.IsBlocked(ctx, n.RecipientUID, n.ActorUID)  // ← 同样无超时
```

如果 DB 因连接池耗尽或死锁而挂起，`notifs.Create` 将无限期阻塞，而 **webhook 甚至永远不会被调用**（因为 chain 在 dispatcher 处停止）。分析正确地指出了 relay 的 `drain()` 中的 `handler.Handle(ctx, e)` 调用，其中 `ctx = context.Background()`。建议的修复是正确的：使用 `context.WithTimeout(ctx, handlerTimeout)` 包装每个处理程序调用。

因此，尽管 webhook 的细节不准确，**但根本问题确实存在**。

让我也快速检查一下 webhook handler 是否可以从 dispatcher 卡住中恢复，还是说整个 relay 会死锁。看 relay.go：

```go
for _, e := range events {
    if err := r.handler.Handle(ctx, e); err != nil {
        r.log.Warn(...)  // 记录并继续
        r.repo.MarkFailed(ctx, e.ID, ...)  // 也可能卡住！
        continue
    }
    published = append(published, e.ID)
}
```

即使一个事件处理程序挂起，整个 relay 都会在 **那个 select / channel / DB 操作上** 阻塞。没有超时意味着卡住是永久性的 — 除非 relay 协程被杀死（通过 `stopRelay()`）或相邻的 tick 根本不会发生。

这是一个 P0。

---

## 最终评估

| # | 方向 | 分析准确性 | 实际 P0 数量 | 注释 |
|---|------|-----------|-------------|--------|
| 1 | 多实例安全 | **P0 正确** | 1 | `FOR UPDATE SKIP LOCKED` 确实是缺失了 |
| 2 | 写偏斜 | **过度报告** — Like/Unlike 实际上是安全的 | 0.5 | 文件去重竞争是唯一的真正问题；计数器安全 |
| 3 | SSE 资源管理 | **P1 正确** | 0 (P1) | 僵尸连接 + 无上限 — 真实情况，但需要更长期的重构 |
| 4 | 数据生命周期 | **P2 正确** | 0 (P2) | 准确的目录；合理的非紧急问题 |
| 5 | Outbox 超时 | **P0 评级正确，发现需要修正** | 1 | 不是 webhook — 是 dispatcher 对 context.Background() 的 DB 调用 |

**修复后的 P0 计数：2 个（方向 1 + 方向 5）**
**修复后的 P1 计数：2 个（方向 2 的文件去重 + 方向 3）**

方向 5 的主要建议（关于 context timeout）应提及 dispatcher（notification/dispatcher.go）而不是 webhook，因为 webhook 已经有 `http.Client.Timeout`。

**你遗漏了一个发现。** 在 direction 5 中，`MarkFailed` 也使用 `ctx`（即 `context.Background()`）：

```go
// relay.go:110
if err := r.repo.MarkFailed(ctx, e.ID, e.RetryCount+1, r.maxRetries, now); err != nil {
```

如果 dispatcher 卡住，relay 就会在它上面卡住。但如果 dispatcher 返回错误，relay 会尝试 `MarkFailed`，而该操作也可能卡住。relay 唯一的出口是外部调用 `stopRelay()` 关闭 channel。主修复（用 `WithTimeout` 包装每个处理程序调用）可以解决这两个问题。
