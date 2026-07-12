代码验证完成。以下是我对你这份分析的逐方向评审——

---

## 总体评价

这是一份**基调和方向都非常正确**的架构分析。五个方向都瞄准了真实存在的缺口，且具备可操作的代码证据。但我在实际代码审计中发现了 **2 处事实错误**和 **3 个值得补充的洞见**。

---

## 已发现的校正点

### 事实错误 1（重要）：方向五引用的 Dispatcher 是死代码

你引用的是 `internal/notification/dispatcher.go:133-158`：

```go
// internal/notification/dispatcher.go:133-158  (your citation)
```

**这个文件已经不再被 import 了。** 实际生产代码使用的是 `internal/service/notification.go` 中的 `service.NotificationDispatcher`。`app.go:174` 调用的是：

```go
dispatcher := service.NewNotificationDispatcher(notifRepo, ...)
```

当前代码库中没有任何 `.go` 文件 import `internal/notification/` 包：

```bash
$ grep -rn "internal/notification" --include="*.go" . | grep -v "_test.go" | grep -v ".claude/worktrees"
# 空输出
```

**不过，你的论点本身仍然成立**——新的 Dispatcher 在 `Handle()` 方法末尾（`internal/service/notification.go:253-258`）仍然是 fire-and-forget 的 SSE push，不检查结果：

```go
if d.pusher != nil {
    payload, _ := json.Marshal(n)
    d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
}
return nil
```

建议：将引用文件改为 `internal/service/notification.go`，行号更新到 ~253-259。

### 事实错误 2（轻微）：app.go 行号偏移

你的文档写的是：

> ```go
> // internal/app/app.go:50-58
> func New(ctx context.Context, cfg *config.Config, log *zap.Logger) (*App, error) {
>     // cfg 是 *config.Config...
>     limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
> ```

但当前 `app.go:50-58` 位于 **App struct 定义内部**（`stopIdemClean func()` 和 `defaultRateLimitIdleTTL` 常量之间），而非 `New()` 函数体。`New()` 函数起始于约第 **72 行**，`ratelimit.New()` 调用在约 **第 238 行**。论点不受影响，但行号需要修正。

---

## 深度补充：每个方向中值得注意的额外细节

### 方向一 — 公共匿名只读访问

✅ 你的代码证据全部准确。可以补充一个关键点：

文件 download（`GET /files/dl`）已经是匿名可访问的——通过 HMAC 签名验证（`downloader.VerifySignature`）。这意味着匿名访问的**基础设施基础已经存在**（HMAC 签名 + nonce + 过期时间）。问题在于**数据读取路径完全没利用这个基础**。你的建议方向可以更具体地说：将 `downloader` 的签名验证模式推广为"匿名访问凭证"模式（`GET /public/v1/files/{id}?token=<short-lived-anon-token>`）。

### 方向二 — Outbox 死信管理

✅ 证据准确。补充一个我去代码中实际看到的东西：

`outbox_events` 表有**两个轻量级索引**但缺少复合索引用于管理查询：

```sql
-- migrations/000008: 只有 created_at DESC
CREATE INDEX idx_outbox_events_created_at ON outbox_events(created_at DESC);

-- migrations/000018: 部分索引只用于 FetchUnpublished
CREATE INDEX idx_outbox_unpublished_no_dead
    ON outbox_events(created_at, id)
    WHERE published_at IS NULL AND NOT dead_letter;
```

这意味着 `SELECT * FROM outbox_events WHERE dead_letter = TRUE ORDER BY created_at DESC LIMIT 100` 会走 `idx_outbox_events_created_at` 但需要过滤大量非死信行。建议补充索引 `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_outbox_dead_letter ON outbox_events(created_at DESC) WHERE dead_letter = TRUE`。

### 方向三 — 跨资源引用完整性

✅ 证据准确。一个我独立验证的细节：`ReactToMessage` 的第一行**验证消息存在**（`First(&row, "id = ?", messageID)`），但由于 GORM 的默认 scope 加了 `deleted_at IS NULL`，所以对已软删除的消息会返回 `ErrNotFound`。这意味着**对于消息 reactions，已删除的消息不会出现悬空引用**——react 到被删消息时是 `ErrNotFound`。但文档中说的"reactions 在消息删除后残留"确实是真的——它们只是不再能被新的 react 操作访问，但数据行还在。

对于 `comments` 的多态引用，确实没有任何保护——`ListByTarget` 不会 JOIN 检查目标文件是否存在。这是一个真实问题。

### 方向四 — 配置热重载

✅ 证据准确。补充一个我发现的细节：

虽然 `zap.Logger` 支持 `lvl.SetLevel()`，但 aero-id 的 `internal/pkg/logger/logger.go` 包装没有暴露这个方法。同样，`ratelimit.Limiter` 缺少 `SetRate()/SetBurst()`。但更重要的是，最大的热重载痛点其实是 **SSO 端点切换**——当前 `sso.NewClient` 在启动时一次性创建 gRPC 连接，如果 SSO 端点需要蓝绿切换，必须重启整个 aero-id。建议在方向四中突出这个优先级。

### 方向五 — 通知推送可靠性

✅ 核心方向正确，但需要注意上述的死代码引用问题。补充验证：

`ListSince` 的 50 硬编码确实存在（`internal/handler/http/events.go:103`）。虽然 GORM 查询加了 `Limit(limit)`，但由于 events handler 传入的是常量 `50`，用户离线期间如果产生 51+ 条通知，第 1 条永远丢失。

另外，`hub.Hub.Subscribe` 中 channel buffer 是 16（`internal/hub/hub.go:67`：`make(chan SSEEvent, 16)`）。如果客户端消费速度跟不上 16 条积压，`pub()` 中的 `select default` 会静默丢弃。

---

## 优先顺序建议——我的调整

我认同你的 P1/P2 划分，但对排列有不同意见：

| 你的排列 | 我的建议 | 理由 |
|---------|---------|------|
| P1 #1 公共匿名只读 | **P1 #1** | 同意。直接影响产品社交传播能力 |
| P1 #2 配置热重载 | **P1 #3** | 降级：当前没有生产运维压力信号表明配置变更频率高到需要热重载。限流器可通过 Prometheus 指标 + 水平扩展解决突发流量 |
| P1 #3 通知推送可靠性 | **P1 #2** | 升级：SSE 丢消息是**已验证的 bug**（buffer=16 + select default drop），而不是设计取舍。用户信任影响大于运维便利 |
| P2 #4 Outbox 死信管理 | **P1 #4** | 升级：死信对操作员完全透明，不可接受。但工作量低，可以快速实现 |
| P2 #5 跨资源引用完整性 | **P2** | 同意。长期正确性问题，短期内无功能错误 |

---

## 扩展：一个可能被遗漏的方向——多租户 / 工作空间隔离

经过 110+ 轮分析后，还有一个缺口我认为值得提及：**当前所有数据以 `owner_uid` 为唯一租户维度，但用户无法将数据组织到"项目"或"工作空间"中**。没有 `workspace_id` 或 `space_id` 列，也没有跨资源的"空间级"权限模型。这意味着：

- 无法实现"以群组/团队为单位的文件共享"（当前只有 1:1 share 和 public 文件）
- 无法让用户将文件 + 评论 + 消息组织在一个命名空间（比如"项目 #123"）下
- 所有 `ListByOwner` 查询都是 flat 的，没有"我的文件 → 按项目/标签过滤"的维度

如果你的产品路线图包括 B2B/团队协作，这是一个需要提前规划的架构级方向。

---

## 总结

这是一份**质量很高的分析文档**。五个方向都基于真实的代码证据，论证逻辑清晰，建议方向具体可操作。需要修正的主要是：

1. **Dispatcher 引用文件**：`internal/notification/dispatcher.go` → `internal/service/notification.go`（Dead code 问题）
2. **app.go 行号**：50-58 → 实际在 72+ / 238+（不影响论点）
3. **优先顺序**：建议将通知推送可靠性提升到 P1 #2

你希望我将这些修正点整理成一份 patch 输出到文件吗？
