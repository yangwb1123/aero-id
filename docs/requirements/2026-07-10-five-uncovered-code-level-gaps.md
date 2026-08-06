# aero-id — 第八轮全量扫描：五个代码级别的结构性缺口

> **日期**: 2026-07-10  
> **范围**: 全代码库交叉比对（internal/ 全部包、migrations/、configs/、cmd/）  
> **方法**: 逐文件扫描 + 与 `docs/requirements/` 下 45+ 份既有分析文档的每一条章节标题和核心论据做逐条比对，确保以下五个缺口**零系统性覆盖**  
> **视角**: 资深架构师 / 产品经理  
> **约束**: 不写代码，每个缺口附代码级证据

---

## 前情：当前未被覆盖的领域

经全量交叉比对，已有分析覆盖了以下领域（不作为本文档方向）：

| 已有覆盖的领域 | 对应文档 |
|---|---|
| 协作工作空间 / Workspace | 多份文档均有 |
| 分布式基础设施（共享限流、缓存、outbox） | 多份文档均有 |
| 文件版本化 / 回收站 / 生命周期 | 多份文档均有 |
| 内容安全 / 合规 / GDPR / 审核管道 | 多份文档均有 |
| 开发者平台 / API 密钥 / SDK / OpenAPI | 多份文档均有 |
| 架构断裂 / 双套实现 / 死代码 | 4 份文档 |
| Admin 角色管理 API 未完整接入 | 多份文档均有 |
| 文件分享无通知链路 | 2 份文档 |
| 预签名引用计数缺失 | 3 份文档 |
| 性能 / 索引 / N+1 / 计数器物化 | 多份文档均有 |

以下五个方向是经过上述逐条比对后确认的**零系统性覆盖**的缺口。

---

## 方向一：SSE 通知重连回放的 50 条硬上限——长离线期间的静默丢失

### 代码级证据

```go
// internal/handler/http/events.go:118
missed, err := h.fetcher.ListSince(c.Request.Context(), uid, since, 50)
//                                                           ^^ hardcoded
```

```go
// internal/repository/notification.go:106
func (r *notificationRepo) ListSince(..., limit int) ([]*domain.Notification, error) {
    // ...
    Limit(limit).Find(&rows)
    // 严格按 created_at ASC 取前 limit 条
}
```

### 问题分析

`EventsHandler.replayMissed` 在客户端重连时（携带 `Last-Event-ID`）调用 `ListSince` 查询错过的通知。该调用：

1. **硬编码 limit=50** — 不存在配置化、不存在分页循环、不存在 cursor 传递
2. **使用 `created_at ASC` 排序** — 返回的是最旧的 50 条未读通知，而非最新的
3. **无截断日志** — 当实际错过的通知 >50 条时，多余的被静默丢弃，客户端和用户完全不知情

这意味着：

- 用户如果断开连接 >X 小时（取决于通知频率），重连后只能看到最早错过的 50 条，最新错过的通知**丢失**
- 排序是 `ASC`（最早优先），意味着用户看到的不是"我离线期间发生了什么"，而是"我离线后最早发生了什么"
- 50 条上限在长离线场景下极容易被突破（例如群消息场景一小时内可产生数百条通知）

### 为什么这是高优先级

这是**数据丢失 (data loss)** 问题，不是性能问题。SSE 的设计承诺是"断线重连后恢复错过的消息"，但当前实现对这个承诺最多只能兑现 50 条。超出部分静默丢弃，用户无感知。

在以下场景中影响尤为严重：

- 用户过夜断开（8h+ 离线），早上的通知大量丢失
- 高流量频道中的用户（多人群聊、多人协作文档评论）
- 移动端网络不稳定导致频繁断线重连，每次只恢复前 50 条

### 建议方案

1. **分页回放**：在 `replayMissed` 中循环调用 `ListSince`，用最后一条回放通知的 `created_at` 作为下一页的 `since`，直到全部回放完毕或超过合理的最大回放条数（如 500）
2. **截断预警**：当截断发生时，在回放流末尾插入一条 `event: notice` 的 SSE 消息告知客户端有更多未显示的通知，客户端可以显示"还有 X 条未读通知"的提示
3. **Limit 配置化**：将 50 提升为可配置参数（如 `server.http.sse_replay_limit`），默认 200
4. **修复排序方向**：将 `created_at ASC` 改为 `created_at DESC`，确保用户先看到最新错过的通知

**估算**: 低。仅修改 `events.go` 的 `replayMissed` 方法，约 20 行代码调整 + repository 无变动。

---

## 方向二：文件生命周期零 Outbox 事件——文件域被排除在事件驱动架构之外

### 代码级证据

所有文件操作中涉及 outbox 事件的生产路径：

```go
// internal/service/file.go — 全文件搜索 newEvent / outbox.NewEvent: 0 条
// internal/file/service_crud.go — 全文件搜索 newEvent / outbox.NewEvent: 0 条
// internal/file/service_upload.go — 全文件搜索 newEvent / outbox.NewEvent: 0 条
// internal/file/service_share.go — 全文件搜索 newEvent / outbox.NewEvent: 0 条
```

对比消息和评论域的生成：

```go
// internal/service/message.go:105
evt := newEvent("message", m.ID.String(), "message.sent", map[string]any{...})
// internal/service/message.go:237
evt := newEvent("message", m.ID.String(), "message.read", map[string]any{...})
// internal/service/comment.go:118
evt = newEvent("comment", c.ID.String(), "comment.replied", map[string]any{...})
```

当前 outbox 中已定义的事件类型全集：

```go
// internal/notification/dispatcher.go:83-87
case "message.sent": ...
case "message.read": ...
case "comment.replied": ...
// 无任何 file.* 事件类型
```

```go
// internal/service/notification.go:261-298
case "message.sent": ...
case "message.read": ...
case "comment.replied": ...
// 无任何 file.* 事件类型
```

### 问题分析

文件垂直方向是所有核心操作中**唯一一个完全不产生 outbox 事件的领域**。具体受影响的操作路径：

| 文件操作 | 是否有 outbox 事件 | 影响 |
|----------|-------------------|------|
| Create (直接上传) | ❌ | Webhook 无法感知新文件 |
| PresignUpload → CompleteUpload | ❌ | Webhook 无法感知直传完成 |
| Delete (软删除) | ❌ | 外部系统无法同步删除 |
| UpdateMetadata | ❌ | 外部系统无法感知元数据变更 |
| ShareFile | ❌ | 已有多份文档提及，但仅是其中一项 |
| UnshareFile | ❌ | 同上 |

这意味着：

1. **Webhook 通路对文件域完全关闭** — 即使配置了 `outbox.webhook.url`，外部系统也收不到任何文件相关的事件
2. **文件域没有异步处理的入口** — 未来如果要实现"文件上传后自动转码"、"文件分享后通知"等异步处理，都需要修改 service 层来生成事件
3. **架构不一致** — 消息和评论域已经拥抱了事件驱动模式，文件域仍然是纯同步模式

### 建议方案

新增以下 outbox 事件类型（与现有模式一致，在 `internal/service/file.go` 中添加）：

| 事件类型 | 触发点 | 备注 |
|----------|--------|------|
| `file.created` | `Create` 成功后 | 直接上传完成 |
| `file.uploaded` | `CompleteUpload` 成功后 | 预签名直传完成 |
| `file.deleted` | `Delete` 成功后 | 软删除触发（不是 GC 物理删除） |
| `file.updated` | `UpdateMetadata` 成功后 | 元数据变更（含 visibility 变更） |
| `file.shared` | `ShareFile` 成功后 | 已有多份文档建议 |
| `file.unshared` | `UnshareFile` 成功后 | 分享撤销 |

每个事件在文件操作成功的 DB 事务外（或事务内，使用 `db.Transaction`）写入 outbox 表。

**估算**: 低-中。约 6 个 `newEvent` 调用 + 对应的 outbox append 逻辑。每处约 3-5 行代码。文件分享的 notification 消费端已在现有分析中有详细方案。

---

## 方向三：Outbox 中继优雅关闭期间无超时兜底——`context.Background()` 阻塞 shutdown

### 代码级证据

```go
// internal/outbox/relay.go:159-168
func (r *Relay) Start(interval time.Duration) (stop func()) {
    // ...
    go func() {
        ctx := context.Background()
        for {
            select {
            case <-t.C:
                r.drain(ctx)
            case <-done:
                t.Stop()
                r.drain(context.Background()) // ← 使用 Background，无 deadline
                return
            }
        }
    }()
    return func() { close(done) }
}
```

```go
// internal/app/app.go:454-470
func (a *App) shutdown() {
    if a.grpcHealth != nil {
        a.grpcHealth.SetServing(false) // 通知 LB drain
    }
    if a.stopRelay != nil {
        a.stopRelay() // → close(done) → drain(context.Background())
    }
    // ... 后续停止 GC、HTTP Server、gRPC Server
    shCtx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.HTTP.ShutdownTimeout)
    defer cancel()
    if err := a.httpServer.Shutdown(shCtx); err != nil { ... } // 使用带 timeout 的 context
    a.grpcServer.GracefulStop()
    // ...
}
```

### 问题分析

`relay.drain` 在最后的优雅关闭路径中使用 `context.Background()`，没有任何 deadline。这意味着：

1. **一个慢的 webhook 端点（例如 30s 超时 + 3 次重试）可以让 drain 阻塞数分钟**
2. `shutdown()` 中的 `grpcHealth.SetServing(false)` 已经触发，LB 已经开始向此实例 drain 流量，但新的请求还在到达（因为 `httpServer.Shutdown(shCtx)` 要等到下一行才调用）
3. `httpServer.Shutdown` 有 `ShutdownTimeout`（默认 10s），但 `stopRelay()` 阻塞在这之前——**实际的 shutdown timeout 形同虚设**
4. 如果 `drain` 阻塞时间超过客户端的连接等待时间，活跃请求会被硬中断，造成 502/503

对比来看，`httpServer.Shutdown` 和 `tracer.Shutdown` 都正确使用了 `ShutdownTimeout` context，唯独 outbox relay 的最终 drain 没有。

### 建议方案

1. **将 deadline 传递给 drain**：`Start` 接受一个 parent context 参数，在 stop 时创建一个带 timeout 的子 context：
   ```go
   func (r *Relay) Start(ctx context.Context, interval time.Duration) (stop func()) {
       // ...
       case <-done:
           t.Stop()
           drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
           defer cancel()
           r.drain(drainCtx)
           return
   }
   ```

2. **在 app.go 中传递 shutdown context**：
   ```go
   relay.Start(shutdownCtx, interval)
   // stopRelay 最终使用 shutdownCtx 的 deadline
   ```

3. **限制 final drain 的 handler 调用次数**：只处理一个 batch，跳过仍在 backoff 窗口内的事件（当前代码已有此逻辑，但无总体超时保护）

**估算**: 低。Relay 签名变更（新增 `ctx context.Context`）+ app.go 传递 `shCtx`。约 15 行代码。

---

## 方向四：全文本搜索的 per-kind 超时是硬编码常量——无法配置、不可观测

### 代码级证据

```go
// internal/service/search.go:27-29
const (
    searchTimeout = 5 * time.Second // ← 硬编码
    // ...
)

// internal/service/search.go:81-82
searchCtx, cancel := context.WithTimeout(ctx, searchTimeout)
msgs, err = s.messages.SearchMessages(searchCtx, actor, query, limit)
// 如果超时，err 是 context.DeadlineExceeded，被包装为 apperr.Internal 返回给客户端
```

```go
// internal/service/search.go 的 HTTP handler:
// internal/handler/http/search.go — 直接调用 svc.Search()
// 不传递 timeout，复用 handler 的 request timeout

// internal/service/search.go 的 gRPC handler:
// internal/handler/grpc/search.go — 直接调用 svc.Search()
// 不传递 timeout
```

```go
// internal/pkg/metrics/metrics.go — 全文件搜索 search: 0 条
// 搜索没有独立的 RED 指标
```

### 问题分析

`searchTimeout = 5 * time.Second` 是一个硬编码常量。这在三个方面存在问题：

1. **不可配置**：如果生产环境的搜索查询因数据量大而经常超时，运维人员无法调整这个值。必须修改源码、重新编译、重新部署。
2. **不可观测**：搜索超时和搜索延迟没有独立的指标。`metrics.Registry` 记录了所有 HTTP/gRPC 请求的 RED 数据，但没有按"搜索"维度的聚合。无法制作搜索 P99 延迟仪表盘。
3. **按 kind 累加**：如果同时搜索 3 个 kind（messages + comments + files），总等待时间是 `3 × 5s = 15s`，加上 HTTP handler 自身的 `request_timeout`（默认 30s），这个组合可能导致令人困惑的行为：搜索偶尔返回 5s 就超时（单个 kind），偶尔 15s 后才超时（三个 kind 都超时）。
4. **并发搜索被顺序执行**：当前 searchKind 是串行循环。三个 kind 串行执行，即使 P99 延迟只有 100ms，最差情况也需要 3 × 5s = 15s 才能返回错误，用户体验差。

### 建议方案

1. **配置化超时**：
   ```yaml
   # configs/config.yaml
   search:
     timeout: 5s   # per-kind timeout, 0 = use request context
     max_query_len: 200
   ```

2. **并行搜索 + 竞态**：用 `errgroup` 或 `sync.WaitGroup` 并行执行多个 kind 的搜索，任一超时则取消所有：
   ```go
   g, ctx := errgroup.WithContext(ctx)
   g.Go(func() error { return searchMessages(ctx, ...) })
   g.Go(func() error { return searchComments(ctx, ...) })
   g.Go(func() error { return searchFiles(ctx, ...) })
   return g.Wait() // 最先失败的或全部成功
   ```

3. **搜索 RED 指标**：在 `metrics.Registry` 中增加搜索维度的计数器：
   - `aeroid_search_total{kind="messages|comments|files", result="hit|miss|timeout"}`
   - `aeroid_search_duration_seconds{kind="..."}`

4. **搜索熔断**：当某个 kind 的搜索连续超时时，临时跳过该 kind（类似 SSO 的断路器模式），保证部分搜索结果可用。

**估算**: 低-中。配置化 + 并行化约 30 行代码调整。指标增加约 20 行。并行化需要注意结果合并时的去重和排序。

---

## 方向五：Config 验证缺失跨字段一致性检查——多个配置组合在生产中静默降级

### 代码级证据

```go
// internal/config/config.go:75-109 — validate() 完整实现
func (c *Config) validate() error {
    if c.SigningKey == "" { return ... }
    if c.Database.DBName == "" { return ... }
    if c.SSO.Endpoint == "" { return ... }
    switch c.Storage.Type { case "local", "s3": default: return ... }
    if c.RateLimit.Enabled {
        if c.RateLimit.RPS <= 0 { return ... }
        if c.RateLimit.Burst <= 0 { return ... }
    }
    if c.Storage.GC.Enabled {
        if c.Storage.GC.Interval <= 0 { return ... }
        if c.Storage.GC.Retention <= 0 { return ... }
    }
    if c.Outbox.Webhook.URL != "" {
        if _, err := time.ParseDuration(c.Outbox.Webhook.Timeout); err != nil { return ... }
    }
    return nil
}
```

### 问题分析

当前 validate() 只做单字段的非空/范围检查，但缺少以下**跨字段一致性校验**，这些校验在生产中一旦配错会导致服务静默降级而非启动失败：

| 缺失校验 | 配置示例 | 实际后果 |
|----------|---------|----------|
| `sso.stale_ttl <= sso.token_cache_ttl` | `token_cache_ttl: 300s, stale_ttl: 60s` | 缓存过期后 60s 就放弃 stale 数据，stale 回退形同虚设 |
| `storage.gc.sweep_orphans=true` 但 `storage.type=local` 无公共前缀 | `type: local, gc: { sweep_orphans: true }` | `local.List()` 遍历整个存储根目录，如果混杂其他应用的临时文件，"orphan" 检测会误删不属于 aero-id 的文件 |
| 没有 `signing_key` 熵度检查 | `signing_key: "test"` | 本地存储的 HMAC 签名可被伪造，导致 `/files/dl` 路径的未授权下载 |
| Webhook URL 格式有效性 | `url: "not-a-url"` | `http.Post` 在运行时才报错，outbox 事件不断重试直到 dead-letter |
| SSO `max_attempts > 1` 但 `timeout` 过小 | `timeout: 2s, max_attempts: 3, retry_backoff: 1s` | 3 次重试最少需要 1+2+4=7s > 2s 的超时，所有重试都在超时后被取消，等同于单次尝试 |
| `rate_limit.enabled=true` 但 `idle_ttl` 远大于 `cleanup_every` | `idle_ttl: 24h, cleanup_every: 1m` | 每分钟扫描一次但 24h 才过期，内存中积累大量已不活跃的 bucket |
| `notification_preferences` 的 `quiet_hours_*` 依赖 `timezone` | `quiet_hours_start: "18:00"` 但 `timezone: ""` | 默认 UTC 时区，用户可能在非预期时间收到静默通知 |

### 建议方案

在 `validate()` 中增加跨字段校验：

```go
func (c *Config) validate() error {
    // ... 现有检查 ...

    // SSO stale 必须有意义
    if c.SSO.StaleTTL > 0 && c.SSO.StaleTTL <= c.SSO.TokenCacheTTL {
        return fmt.Errorf("sso.stale_ttl (%v) must be > sso.token_cache_ttl (%v) "+
            "to provide a useful serve-stale window; disable stale with stale_ttl=0",
            c.SSO.StaleTTL, c.SSO.TokenCacheTTL)
    }

    // SSO retry budget 必须在 timeout 内
    if c.SSO.MaxAttempts > 1 {
        minBudget := time.Duration(c.SSO.MaxAttempts) * c.SSO.Timeout / time.Duration(c.SSO.MaxAttempts)
        if c.SSO.RetryBackoff*time.Duration(1<<(c.SSO.MaxAttempts-1)) > c.SSO.Timeout {
            log.Warn("sso retry budget may exceed timeout; retries may all timeout")
        }
    }

    // GC orphan 检查
    if c.Storage.GC.SweepOrphans && !c.Storage.GC.Enabled {
        return fmt.Errorf("storage.gc.sweep_orphans requires storage.gc.enabled=true")
    }

    // Webhook URL 格式检查
    if c.Outbox.Webhook.URL != "" {
        if _, err := url.Parse(c.Outbox.Webhook.URL); err != nil {
            return fmt.Errorf("outbox.webhook.url is not a valid URL: %w", err)
        }
    }

    // signing_key 熵检查
    if len(c.SigningKey) < 32 {
        log.Warn("signing_key is shorter than 32 characters; consider a longer key")
    }

    return nil
}
```

**估算**: 低。仅 `config.go` 的 validate() 方法增加约 30 行校验逻辑。**不改变任何业务行为**，仅在配置错误时提前失败或发警告。

---

## 优先级建议矩阵

| 方向 | 影响类型 | 严重程度 | 实现估算 | 建议起点 |
|------|----------|----------|----------|----------|
| 方向一：SSE 重放 50 条上限 | 数据丢失 | **P0** | 低（~20 行） | 改分页循环 + 排序方向 |
| 方向二：文件零 outbox 事件 | 架构完整性 | **P1** | 低-中（~30 行 + 通知消费端） | 先从 `file.created` + `file.deleted` 开始 |
| 方向三：outbox relay 无超时 shutdown | 可靠性 | **P1** | 低（~15 行） | 改 `Start` 签名 + 传 deadline |
| 方向四：搜索超时硬编码 | 可运维性 | **P2** | 低-中（~50 行） | 先做配置化超时 |
| 方向五：config 跨字段校验缺失 | 可运维性 | **P2** | 低（~30 行） | 逐条增加到 validate() |

### 推荐执行顺序

```
立即（此 PR）： 方向一（SSE 分页回放） + 方向三（relay shutdown timeout）
                 ↓ 两个都是低工作量高安全性改进
本周内：        方向五（config 校验）—— 代码量最小，持续防护
                 ↓
本 sprint：     方向二（file.created + file.deleted outbox 事件）
                方向四（搜索超时配置化）
```

---

## 附录 A：扫描日志

在本次全量扫描过程中检查的关键文件清单（共 47 个源文件，覆盖所有功能域）：

| 包 | 检查的文件 |
|----|-----------|
| app | `app.go`（全文件） |
| config | `config.go`, `config_test.go` |
| service (旧) | `file.go`, `message.go`, `comment.go`, `search.go`, `notification.go`, `service.go` |
| domain | `notification.go`, `audit.go`, `admin.go`, `errors.go`, `access.go` |
| handler/http | `events.go`, `search.go`, `admin.go`, `router.go` |
| handler/grpc | `ops.go`, `search.go` |
| repository | `notification.go`, `file.go`, `message.go`, `comment.go`, `admin.go` |
| outbox | `relay.go`, `outbox.go`, `webhook.go` |
| storage/gc | `sweeper.go` |
| hub | `hub.go` |
| pkg/metrics | `metrics.go` |
| middleware | `ratelimit.go`, `idempotency.go`, `metrics.go`, `tracing.go`, `timeout.go`, `permission.go` |
| 文件域 (新) | `service_upload.go`, `service_crud.go`, `service_share.go`, `file.go` |
| 搜索 (新) | `search.go`, `handler_http.go`, `handler_grpc.go` |
| migration | 全部 22 个 `*.up.sql` |

*本文档仅做分析，未修改任何代码。所有建议基于 2026-07-10 的代码库扫描结果。*
