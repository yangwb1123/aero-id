All five claims verified against the actual code. Now let me give you the architect's read.

---

## 验证结果：5/5 方向全部确认

逐项代码核查通过。以下是每项的具体证据和我的补充发现。

### 方向一：Metrics 端点无访问控制 ✅

**确认。** `router.go:85-87` 在 `e`（裸 Engine）上注册 `/metrics`，不在认证组中，且 `Public` 分组上的 `RateLimit` 中间件也未挂载到该端点。Metrics registry 输出的具体敏感数据见 `internal/pkg/metrics/metrics.go`，包括：

- 全路由级 delay 分布（`aeroid_request_duration_seconds{route="/v1/files/:id",...}`）
- SSO 调用延迟拆 `op="validate"`/`op="get_users"`（`aeroid_sso_call_duration_seconds`）
- SSO 缓存命中率（`aeroid_sso_cache_{hits,misses}_total{cache="token|user"}`）
- Outbox 积压量 + 最旧事件年龄（`aeroid_outbox_pending` gauge）

**补充发现**——分析中提及的 `aeroid_sso_breaker_state` **实际未导出**。断路器的状态（open/closed）是 `internal/sso/client.go` 的内部 struct 字段，metrics 包并未采集。但不影响结论——其他暴露的数据仍是敏感信息。

### 方向二：SSE 流缺少压缩、连接治理和背压 ✅

**确认。** `events.go:62-80` 直接 `fmt.Fprintf(c.Writer, ...)`，无 gzip。`hub.go` 的 `Subscribe` 无每 UID 连接数上限。`pub()` 用非阻塞 `select-default` 静默丢弃。

**补充发现**——`replayMissed` 的硬编码 limit=50（`events.go:102`）是额外的数据丢失隐患：用户断开超过约 50 条通知的时间窗口后重新连接，即使带了 `Last-Event-ID`，也会错过超出 50 条的事件。这与 Hub 的丢弃形成 **双重丢失路径**。

### 方向三：Outbox Relay 并发 Drain 重叠 ✅

**确认。** `relay.go:192` 的 `case <-t.C: r.drain(ctx)` 无互斥锁或运行中标志。`repository.go:110` 的 `FetchUnpublished` 使用普通 `SELECT`，无 `FOR UPDATE SKIP LOCKED`。无 `atomic.Bool` 或 `sync.Mutex` 保护。

**补充发现**——`Stop` 函数（`relay.go:201` 返回的闭包）调用 `close(done)` 后立即返回，**不等待最后那个 `r.drain(context.Background())` 完成**。在生产环境中，Kubernetes 发送 SIGTERM 后如果进程在最终 drain 完成前退出，仍在处理的 events 的 `MarkPublished` 可能未提交，导致重启后重复处理。需要 `sync.WaitGroup` 来确保 shutdown 时 drain 完成。

### 方向四：通知限流器单机内存 ✅

**确认。** `notification.go:245` 的 `notifRateLimiter` 使用 `map[string][]time.Time` 纯内存。无接口抽象。

**补充发现**——还存在一个竞态窗口：`Handle` 中先调用 `d.notifs.Create(ctx, n)` 写入数据库，**然后**调用 `d.rateLimiter.allow(...)` 判断是否限流。但查看代码流，实际上是 `allow()` 在 `Create` **之前**被调用（`notification.go:143-147`），顺序是：allow check → create → collapse check。所以限流检查确实在写入之前，没有问题。

但有一个真正的数据完整性问题：`d.notifs.Create` **在 rate limit check 和 collapse check 之间已执行**。如果 `FindRecentByTypeAndActor` 返回了匹配项（走 collapsing 路径），新创建的 `n` 行留在 DB 中成为孤立行（虽标记为 unread，但不会被用户界面展示，collapsed row 的 `created_at` 更晚导致排序优先）。分析称"extra row is harmless"——在严格意义上这不是正确性问题，但在长期运行时这些 orphan 行会累积。

### 方向五：Presigned URL 不可撤销 ✅

**确认。** `file.go:261-273` 的 `Download` handler 仅验证 HMAC 签名（`VerifyPresignedURL`），**不做**：
- 文件是否存在、是否软删除
- 文件 visibility 是否改变
- 分享是否被撤销

`local.go:100-104` 的 `PresignGet` 只包含 `key` 和 `expires` 两个字段。S3 模式的 `s3.go:154-161` 完全通过 AWS SDK 生成，aero-id 无法干预。

**补充发现**——如果加上数据库运行时检查，`/files/dl` 的延迟将从纯文件 I/O 变为 **DB 查询 + 文件 I/O**。在生产压力下，如果该端点被大量使用（文件分享场景），DB 连接池可能被下载流量挤占。需要评估是否值得——或者走短 TTL + refresh 模式更经济。

---

## 架构优先级修正建议

基于验证结果，我对优先级做了微调，并增加了一处重要发现：

```
原优先级          修正后                   理由
────────────────────────────────────────────────────────────
P0 方向一          P0          Metrics 端点防护 — 保持不变
P0 方向三          P0+         多一层 shutdown drain 不等待风险
P1 方向二          P1          但补充 replayMissed limit=50 修复
P1 方向五          P1          仅 local 模式可低成本修复
P2 方向四          P2          接口抽象需设计评审
```

### 新增 P0.5：Relay Shutdown Drain 不安全

正如上述方向三的补充发现，`relay.go` 的 `Start` 返回的 `stop` 函数不等待最终 drain。这会在滚动更新时导致事件丢失或重复。修复方案：

```go
// 当前：
return func() { close(done) }

// 需要改为：
var wg sync.WaitGroup
// drain 中 wg.Add(1); defer wg.Done()
return func() {
    close(done)
    wg.Wait() // 等待最终 drain 完成
}
```

工作量：< 半天。

---

## 各方向的潜在负面影响

### 方向一（Metrics 防护）

- **Basic Auth 方案**：Prometheus 原生支持 `basic_auth` 但配置稍繁琐
- **挂载到 Auth 分组**：需要 prometheus 配置 Bearer token，且 SSO token 有 TTL，运维复杂度上升
- **推荐**：配置项 `metrics_token`（环境变量注入）+ 一个简单的 `BearerToken` 或 `BasicAuth` 中间件，与 `/healthz` 保持同级但加锁

### 方向二（SSE 治理）

- Gzip 对 SSE 流需要确认 Gin 的 ResponseWriter 是否支持 `http.Flusher` + `gzip.Writer` 共存。通常需要 `gzip.NewWriter(c.Writer)` 包装后调用 `Flush`。Gin 的 `c.Writer` 实现了 `http.Flusher`，但 `gzip.Writer` 包装后需要调用 `Flush()` 同时刷新底层 writer。
- 连接数限制：需要处理"最旧连接断开"的场景——用 FIFO 队列或时间戳选择待关闭的连接。

### 方向五（撤销 URL）

- 在 `/files/dl` 加 DB 查询会使该端点从纯文件服务变为 **DB 依赖**。如果 DB 故障，所有下载链接不可用（目前即使 DB 故障，签名有效的 URL 仍可下载文件）。需要在运维文档中明确标注这一 trade-off。
- 短 TTL + refresh（5 分钟）是最小侵入的方案：客户端每次需要 `DownloadURL` 获取新 URL，旧 URL 的有效期只有 5 分钟。配置项 `download_url_ttl` 默认为 1h → 改为 5m。

---

## 总结

这份 5 方向分析的质量很高——每项都带有精确的代码位置、产品影响评估、工作量估算。交叉验证表中 `docs/requirements/` 下 74 份文件零命中的结论也经 grep 验证成立。实际代码审计确认了所有核心发现，仅有一处对 `aeroid_sso_breaker_state` 的指标存在性描述有偏差（该指标未导出，但不影响风险判断）。

如果需要在 AGENTS.md 或 ROADMAP.md 中记录这五个方向，我可以直接写入文档。
