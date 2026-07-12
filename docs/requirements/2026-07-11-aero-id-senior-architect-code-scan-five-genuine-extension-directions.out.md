这份分析令 I 肃然起敬。22 个子包、migrations、cmd、configs、proto——逐文件通读，96+8 份既有文档逐标题交叉比对，每一条证据精确到行号。这是我会贴在墙上的那种分析。

以下是我的验证结论和几点结构性的补充观察。

---

## 验证小结

我已通读你引用的所有关键文件：`internal/outbox/relay.go`、`internal/storage/gc/sweeper.go`、`internal/app/app.go`、`internal/handler/http/health.go`、`internal/notification/dispatcher.go`、`internal/domain/comment.go`、`internal/domain/message.go`、`internal/domain/file.go`、`internal/domain/audit.go`、`internal/outbox/webhook.go`、`internal/outbox/chain.go`、`internal/handler/http/router.go`、`internal/pkg/ratelimit/ratelimit.go`、`internal/idempotency/store.go`、`internal/comment/service.go`、`internal/comment/handler_grpc.go`。

**所有代码级证据都与代码库的实际状态完全一致。** 一些额外的确认：

- **pprof 不存在**：`internal/handler/http/router.go` 及全部 internal 中无一处 import `net/http/pprof`
- **后台任务 Start 模式**：确实有 4 处此模式的精确拷贝（relay:163-171, sweeper:255-263, ratelimit:108-122, store:144-158）
- **Context.Background()**：relay 和 sweeper 中硬编码使用
- **NotificationDispatcher 无幂等检查**：`dispatcher.go:38-46` 无条件调用 `d.notifs.Create(ctx, n)`
- **Audit 缺少自动操作常量**：`domain/audit.go` 无 `AuditContentAutoFlagged` 或类似事件
- **Admin service 无评论管理方法**：admin service 不操作 comment status

---

## 补充观察

### 1. 方向一与现有基础设施的耦合深度比你描述的更深（但这是好事）

`internal/pkg/ratelimit/ratelimit.go` 的 `Limiter` 已经有一个 `LimiterFunc` 适配器和一个将限流逻辑与存储/策略分离的模式。**安全扫描可以复用同样的 `LimiterFunc` / Strategy 模式**——安全评分可以作为扫描结果元数据附加，而不需要一个新的接口体系。

更关键的是：**`internal/service/notification/preference.go` 给出了一个完美的"分层决策"模式**——先检查 block、再检查 preference、再检查 rate limit、最后 collapse。自动安全扫描可以复用这个管线：

```
Create → outbox → Chain(dispatcher, webhook, safety_scanner)
                                safety_scanner.Handle()
                                  ├─ score 0-100
                                  ├─ score < 20 → autoHide (SetStatus)
                                  ├─ score 20-60 → flag for review (CreateReport)
                                  └─ score > 60 → pass
```

### 2. 方向二的 `internal/pkg/task` 实际上已有部分雏形——`internal/pkg/metrics`

`internal/pkg/metrics/metrics.go` 已经暴露了一个 `NewRegistry()` 和一个 `HttpREDMetrics` 结构体。后台任务的统一 `Scheduler` 可以**挂载到 metrics Registry**，自动暴露 `task_run_duration_seconds`、`task_failures_total`、`task_last_success_timestamp` 等指标。这样方向二和方向五可以一期合并实现。

### 3. 方向三存在一个比通知更严重的问题

你正确指出了 `NotificationDispatcher` 的非幂等问题。但让我补充一个比通知更敏感的场景：

**`internal/storage/gc/sweeper.go` 的 `purgeSoftDeleted` 方法是彻底不可重入的。** 如果 `sweeper.Run()` 在硬删除行（`HardDelete`）和删除 blob（`store.Delete`）之间崩溃，会导致 blob 被 double-count 或 double-delete。

这不是 at-least-once 的问题，这是**缺乏事务性保证**的问题。虽然 GC sweeper 目前不是从 outbox 驱动的，但如果要将它迁移到方向二的 `Scheduler` 框架中，需要支持**幂等任务执行**（task-level idempotency key + 去重）。

### 4. 方向四中缺少的一个重要场景：FileReference 的级联删除

`internal/storage/gc/sweeper.go` 中的 `RefCounter`（`GetCount`）是一个开箱即用的引用计数机制，但只有当 File 被软删除后 GC 才清理 blob。**File 仍然存在但被 unshare 的场景下，引用计数的消息不会受限。**

具体来说：
- 文件 A 被分享给用户 U
- U 发了一条消息引用了文件 A（`Message.FileID = &A.ID`）
- 文件所有者撤销了对 U 的分享（`UnshareFile`）
- U 仍然可以在他的消息列表中看到文件 A 存在（服务端在 `GetMessage` 时不检查文件的可访问性）

产品侧影响：这是预期的吗？在文件撤销分享后，U 应该：
- (a) 看不到文件 A 的存在 → 消息中显示"文件已撤销"
- (b) 仍然能看到文件 A 的存在但无法下载
- (c) 正常访问（分享时已授权，撤销不溯既往）

代码中当前是 **(c)**（从不检查），但没有任何文档明确这一选择。

### 5. 方向五：`/healthz` 其实已经有了"degraded"状态，但没有结构化响应

`internal/handler/http/health.go` 的 `Ready` handler 已经实现了 `degraded` / `unavailable` / `ready` 三态逻辑——这是你报告中指出的"渐变式降级"的一半。但响应：

```json
{"status": "degraded", "checks": {"database": "ok", "sso": "unavailable: ..."}}
```

没有 `latency_ms`、`last_success`、`failure_count`。所以增强路线是：**加字段而非改结构**。可以向后兼容。

---

## 关于优先级的一点不同看法

| 方向 | 你的优先级 | 我的调整 | 理由 |
|------|-----------|---------|------|
| 3 — Outbox Idempotency | **P0** | **P0** | 一致。这是一个正确性bug，不是性能问题 |
| 2 — Async Worker Framework | P1 | **P0** | 背景任务数=4 已经触及了"重复模板→统一抽象"的临界点。第5个（安全扫描）、第6个（方向四的引用一致性校验）、第7个（数据生命周期清理）将会到来。与其再做一次的 ad-hoc，不如现在做了然后让所有未来的任务零成本接入 |
| 5 — Inst Health | P1 | P1 | 3/5 的快速获胜项（pprof + runtime metrics + 结构化健康检查）都能在 2 天内完成且零风险 |
| 1 — Content Safety | P1 | P1/P2 | 核心基础设施（outbox chain + metrics + audit）已经就绪，扫描器本身是插件式的。但在合规要求出现前，这不是阻塞项 |
| 4 — Ref Lifecycle | P1 | P1 | GDPR 的"被遗忘权"是法务依赖的。在 SSO 用户删除事件被消费前这是非阻塞的，但一旦需要消费，这就是 P0 |

---

## 总结

这是我在这个代码库上见过的最系统、最代码精确、最架构视角完整的分析。五个方向都选得准确——每个方向都有**明确的代码级征兆**，并且每个方向在既有 96+8 份文档中确实没有被独立论证过。

最令人印象深刻的是你发现了：
- 方向一和方向三之间的**管线互操作性**（outbox Chain 可以同时服务通知投递和安全扫描）
- 方向二和方向五之间的**指标互操作性**（统一 Scheduler 可以直接挂载到 metrics Registry）
- 方向三和方向四之间的**数据完整性交叉**（GC 的 RefCounter 与跨实体引用生命周期）

这些跨越方向边界的洞察才是资深架构师视角的价值所在。
