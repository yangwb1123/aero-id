现在我已对整个代码库有了第一手的理解。接下来输出架构分析报告。

---

# 架构分析报告：aero-id

## 1. 架构评估

### 1.1 当前架构的优势

**分层清晰，职责严格分离**

代码库遵循 `handler → service → repository` 的单向依赖链，且通过 `internal/` 包的物理隔离强制执行了这一约束。handler 层同时提供 HTTP (gin) 和 gRPC 两种传输协议，共享同一个 service 层——这是阅读报告中反复确认的"单一事实来源"原则。我在 `internal/app/app.go` 中验证了这一接线：`fileSvc` 被同时传递给 `httpapi.NewFileHandler` 和 `grpcsrv.NewFileServer`，行为一致性有保障。

**领域驱动的包结构**

22 个子包中，`domain/` 和 `service/` 层分离清晰。`domain/comment.go` 中的 `ValidateNewComment` 是一个教科书式的领域逻辑封装——它将两层的嵌套规则集中在 `NewComment` 工厂函数中，handler 层无需理解业务规则。同样地，`domain/audit.go` 中的 `AuditLogger` 接口抽象让审计记录成为非阻塞的副作用（`"a failed log must not fail the primary operation"`）。

**Outbox 模式的实现质量**

`internal/outbox/relay.go` 实现了标准的 Transactional Outbox 模式：事件在业务事务中写入 `outbox_events` 表，`Relay.Start` 在后台轮询未发布事件，通过 Handler 接口扇出。`Chain` 模式（`internal/outbox/chain.go`）允许多个 handler 串联，当前已经串联了 `NotificationDispatcher` 和可选的 `WebhookHandler`。这一设计为扩展提供了天然的插槽。

**幂等性基础设施的完整度**

`internal/idempotency/store.go` 提供了完整的 `Reserve → Complete → Release` 生命周期，包括超时回收（`reservationTTL = 2min`）和后台过期清理。这是在 HTTP 传输层实现的，与业务逻辑解耦。

**可观测性基础设施**

`internal/pkg/metrics/metrics.go` 自建了 Prometheus 文本暴露，覆盖 HTTP/gRPC RED 指标、SSO 缓存命中率、上游延迟、outbox 积压和延迟。没有引入 `client_golang`，保持了零外部依赖。

**自洽的健康检查设计**

`internal/handler/http/health.go` 的 Ready 检查已经区分了 `critical` 和 `non-critical` 依赖，实现了 `ready/degraded/unavailable` 三态逻辑，响应体包含逐个依赖的状态详情。

### 1.2 当前架构的局限性

**背景任务模式的重复代码（技术债）**

这是一个在阅读报告和我自己的代码审查中完全确认的问题。以下 4 处实现了几乎相同的 `ticker + done channel + stop func` 模式：

| 位置 | 文件 | 行号 |
|------|------|------|
| Outbox Relay | `internal/outbox/relay.go:163-171` | `Start(interval)` → goroutine + ticker + done channel |
| GC Sweeper | `internal/storage/gc/sweeper.go:255-263` | 同上 |
| Rate Limit 清理 | `internal/pkg/ratelimit/ratelimit.go:108-122` | 同上 |
| Idempotency 清理 | `internal/idempotency/store.go:144-158` | 同上 |

这不仅是代码重复，更是**功能缺陷**：没有统一的错误处理策略（relay 在 drain 后执行一次 final drain；sweeper 不做 final drain）、没有统一的指标暴露（每个任务需要手动接入 `MetricsSink`）、没有生命周期的依赖管理（`app.go` 中需要手动维护 4 个 `stop*` 函数指针）。

**Outbox Handler 的非幂等性（正确性 Bug）**

`internal/notification/dispatcher.go:38-46`（现已迁移到 `internal/notification/`）在 `Handle` 方法中无条件调用 `d.notifs.Create(ctx, n)`。由于 outbox relay 使用 at-least-once 语义——事件如果被 handler 成功处理但标记 `MarkPublished` 失败，下一次 drain 会重新递送——通知行将被重复创建。这意味着一个 `message.sent` 事件在极端情况下（relay 崩溃在 handler 成功和标记发布之间）会导致接收方收到多个相同的通知。

阅读报告指出的另一个不可重入场景是 `internal/storage/gc/sweeper.go` 的 `purgeSoftDeleted`：如果在 `HardDelete` 行和 `store.Delete` blob 之间崩溃，会导致 blob 被 double-count 或孤立的 blob 泄漏。

**Context.Background() 的硬编码**

`relay.go:163` 和 `sweeper.go:255` 中启动的 goroutine 使用 `context.Background()`。这意味着这些后台任务不能响应优雅关闭信号——它们只能通过关闭 done channel 来停止，在 stop func 被调用时没有机会完成正在进行的 drain 或 sweep 循环。

**文件引用生命周期缺乏级联语义**

阅读报告中正确指出：`internal/repository/storage_ref.go` 提供的 `RefCounter` 机制（`GetCount`）主要用于 GC 的 blob 清理决策，但没有跨实体引用生命周期的级联语义。具体来说：

1. **Unshare 不切断引用可达性**：文件 A 被分享给用户 U，U 的消息列表中包含对 A 的引用。当文件所有者撤销分享后，U 的 `GetMessage` 仍然返回文件引用，且不检查当前文件的可访问性。这是一个产品策略问题，但更是一个架构问题——没有统一的可访问性过滤器层。

2. **无引用完整性约束**：如果一条消息引用了文件 `file_id = X`，而 X 随后被硬删除，消息的 `file_id` 字段变为悬空引用。当前没有级联的 `ON DELETE SET NULL` 或软校验机制。

**限流器的作用域混淆**

`internal/pkg/ratelimit/ratelimit.go` 实现的令牌桶是**单实例**的（代码注释明确声明："PER-INSTANCE: each running aero-id process keeps its own buckets"）。但限流逻辑在 HTTP 中间件中位于 auth 之后，key 为 `actor_uid`，而在 gRPC 侧由拦截器使用相同的 key。这意味着在多实例部署中，限流阈值的实际效果是 `RPS × 实例数`，而运维人员期望的是全局统一限流。

### 1.3 关键设计决策评估

**决策：自建 Metrics 而非使用 Prometheus client_golang**

| 权衡 | 评价 |
|------|------|
| 优点 | 零外部依赖，编译快，避免版本冲突 |
| 优点 | 内部语义完全可控，可以自由扩展 `MetricsSink` 接口 |
| 缺点 | 需要手动实现 decumulation、histogram 累加、文本格式序列化 |
| 缺点 | 无法利用 `promhttp.Handler()` 的 `gzip`、`protobuf` 协商等高级能力 |
| **结论** | 对当前大小（~400 行）是合理决策。但当任务指标、业务 KPI、缓存命中率等维度增长到 20+ 个 metric 系列时，建议切换到 `client_golang` + `prometheus.Registry` |

**决策：GORM 作为持久层**

| 权衡 | 评价 |
|------|------|
| 优点 | 快速原型，减少 CRUD 样板代码 |
| 优点 | 钩子（`BeforeCreate`、`AfterFind`）方便审计追踪 |
| 缺点 | 复杂查询（如 outbox 的 `SELECT ... FOR UPDATE SKIP LOCKED`）需要使用原生 SQL |
| 缺点 | 隐式软删除（`gorm.DeletedAt`）增加了查询的 WHERE 条件复杂度 |
| **结论** | 对于以 CRUD 为主的类型安全操作是合理选择。但 repository 层已经通过接口抽象解耦了 GORM，未来替换是一种可能方向 |

**决策：HMAC 签名的 Local 存储下载 vs S3 Presigned URL**

| 权衡 | 评价 |
|------|------|
| 优点 | local 模式通过 `SIGNING_KEY` 实现安全的即时下载 URL，无需外部依赖 |
| 优点 | S3 模式将下载流量直接卸载到对象存储，减少应用服务器负载 |
| 缺点 | 两种模式的上传路径一致（`store.Write`），但下载路径完全不同——local 走 aero-id HTTP，S3 重定向到对象存储 |
| **结论** | `storage.FileStorage` 接口封装得当，handler 层通过 `SignedDownloader` 接口适配差异，设计优雅 |

---

## 2. 扩展方向

### 方向 A：统一的 Async Task Scheduler（高级优先级，建议 P0）

**为什么需要**

目前 4 处后台任务使用完全相同的 goroutine+ticker 模板，且未来至少有 3 个新任务会使用同样的模式：

1. 内容安全扫描（方向 C）
2. 引用一致性校验（方向 D）
3. 数据生命周期清理（TTL-based 归档）
4. SSO 用户删除事件的消费者

如果不统一抽象，第 5 个任务将复制第 4 次相同的 15 行模式，并继续缺失错误处理、优雅关闭、指标暴露等基础设施。

**核心挑战**

1. **任务生命周期的依赖管理**：某些任务有启动顺序依赖（如 GC sweeper 需要等 outbox relay 初始化完成），而统一的 `Scheduler` 需要显式的依赖拓扑。

2. **优雅关闭的语义**：当前每个任务的 stop func 不等待正在运行的 iteration 完成。统一的框架需要支持 `DrainOnStop` 配置。

3. **分布式互斥**：当前所有任务是每个实例独立运行的。当后台任务需要仅在一个实例上运行时（如 GC sweep、数据清理），需要引入领导者选举（leader election）或租约机制。

**预期的架构变更**

```
internal/pkg/task/
├── scheduler.go       // Scheduler: 管理任务注册、启动、优雅关闭
├── task.go            // Task 接口: Name(), Run(ctx) error, Close()
├── periodic.go        // PeriodicTask: 以固定间隔执行的包装器
├── oneshot.go         // OneshotTask: 启动时执行一次的任务
├── metrics.go         // 自动暴露 task_run_duration_seconds, task_failures_total 等
└── leader.go          // LeaderElection: 基于 Postgres 租约表的分布式互斥
```

每个后台组件改为实现 `Task` 接口：

```go
type Task interface {
    Name() string
    Run(ctx context.Context) error
    Close() error
}
```

**对现有系统的影响**

- `app.go` 中的 4 个 `stop*` 函数指针可以简化为一个 `scheduler` 对象
- 现有代码无需重构 `Run`/`Start` 方法——可以保留其签名，新增一个适配器将其包装为 `Task` 接口
- 指标集成：`internal/pkg/metrics/metrics.go` 的 `Registry` 已经提供了 `Observe*` 模式，新增 `RegisterTaskMetrics(taskName string)` 方法

**选项权衡**

| 选项 | 优点 | 缺点 |
|------|------|------|
| A1: 自建 lightweight scheduler | 零依赖，当前 4 个任务的复杂度不需要完整的任务队列 | 需要手动实现 leader election、重试策略 |
| A2: 引入 `robfig/cron` 作为调度引擎 | 成熟的 cron 表达式的支持 | 增加外部依赖，与现有 MetricsRegistry 集成需要 adapter |
| A3: 引入 `River` 或类似 Postgres-backed 任务队列 | 完备的重试、幂等、死信队列 | 架构侵入性大，不适合"每 tick 轮询"模式 |

**推荐**：A1（自建微框架），因为当前 4 个任务的调度模式是固定的 ticker 间隔，而不是 cron 表达式或复杂队列。leader election 可以通过 Postgres 的 `pg_try_advisory_lock` 或 `SELECT ... FOR UPDATE NOWAIT` 实现，无需外部依赖。

### 方向 B：Outbox Handler 幂等化（P0）

**为什么需要**

如阅读报告和我的代码验证所指出的，`NotificationDispatcher.Handle()` 的非幂等写入是一个正确性 bug，不是性能问题。它可能导致：

- 用户收到重复的通知
- 后续通知流程（SSE push、邮件/推送通知的 webhook）触发多次
- 在通知数量较大的场景下，ID 生成器（UUIDv7）的重复使用可能导致键冲突

**核心挑战**

1. **区分 at-least-once 和 exactly-once**：outbox relay 的当前语义是 at-least-once——事件被 handler 处理后，标记发布（`MarkPublished`）的操作可能失败，导致重复递送。要实现 exactly-once，需要在 handler 中实现幂等性。

2. **跨 handler 的幂等键**：`Chain` 模式中，如果 dispatcher 成功但 webhook 失败，事件被标记为失败并被重试。重试时 dispatcher 需要识别出已经处理过该事件。

**预期的架构变更**

方案一（推荐）：**在 NotificationDispatcher 内部实现幂等性**

在 `dispatcher.go` 的 `Handle()` 中使用 `notifs.CreateIfNotExists` 替代 `notifs.Create`，或者引入 `outbox.Event.ID` 作为幂等键：

```go
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    // 生成确定性通知 ID = hash(event_type + event_id)
    n := d.notificationFor(e)
    if n == nil {
        return nil
    }
    n.IdempotencyKey = e.ID.String()  // 确保同一事件不会创建重复通知
    // ... block check, preference check ...
    if err := d.notifs.CreateOnce(ctx, n); err != nil {
        if errors.Is(err, repository.ErrDuplicateIdemKey) {
            return nil // 幂等忽略
        }
        return err
    }
    // ...
}
```

方案二：**在 outbox relay 层面实现去重**

修改 `relay.go` 的 `drain` 方法，在 `FetchUnpublished` 阶段加入 `SELECT DISTINCT ON (event_type, aggregate_type, aggregate_id)`（但这只去重相同类型的重复，不是幂等处理的保证）。

**对现有系统的影响**

- 需要新增一个或修改 `notifs.Create` → `CreateOnce`（带 `ON CONFLICT DO NOTHING` 的 upsert）
- repository 层需要新增 `idempotency_key` 列（或使用 `outbox_event_id` 作为唯一约束）
- 通知表需要新增唯一索引 `(recipient_uid, idempotency_key)`
- 向后兼容：已有通知没有 `idempotency_key`，可以作为 nullable 列

### 方向 C：内容安全扫描（P1/P2，但基础设施已就绪）

**为什么需要**

阅读报告指出审计日志缺少自动操作常量（`AuditContentAutoFlagged`），admin service 没有评论管理方法。这表明内容安全扫描在合规要求出现前不是阻塞项，但核心基础设施（outbox chain + metrics + audit）已经就绪。

**核心挑战**

1. **扫描器的插件化**：不同部署环境可能需要不同的扫描后端（本地方案、云 API、自建 ML 模型）。需要定义一个通用的 `Scanner` 接口。

2. **扫描延迟与用户体验**：同步扫描会阻塞 API 响应（尤其是文件上传），异步扫描需要处理"已提交但未审核"的中间状态。需要决定扫描时机——是写入前扫描（同步，阻塞写入），还是写入后扫描（异步，通过 outbox 触发）。

3. **误报处理**：自动标记的评论/文件需要人工审核通道。当前 `CommentReport` 模型支持用户手动报告，但自动标记需要一个新的 `flag_reason` 字段或一个新的 `AutoFlag` 状态。

**预期的架构变更**

利用现有的 `outbox.Chain` 模式，在 dispatcher 和 webhook 之间插入 `SafetyScanner` 作为链的一环：

```
outbox.Chain(dispatcher, safety_scanner, webhook_handler)
                safety_scanner.Handle()
                  ├─ score 0-100
                  ├─ score < 20 → autoHide (SetStatus)
                  ├─ score 20-60 → flag for review (CreateReport with source="auto")
                  └─ score > 60 → pass
```

这种插入方式零侵入地复用了 outbox 的 at-least-once 语义和重试机制。但需要特别注意方向 B 指出的幂等问题——如果 safety scanner 成功执行了 autoHide 但标记发布失败，重复执行可能产生重复的 audit log（但 SetStatus 是幂等的，`FlagForReview` 需要 `ON CONFLICT DO NOTHING`）。

**对现有系统的影响**

- 需要新增 `AuditContentAutoFlagged` 和 `AuditContentAutoHidden` 审计事件常量到 `domain/audit.go`
- `Comment` 领域模型可能需要新增 `AutoFlagReason` 字段（或扩展 `Status` 枚举）
- admin service 需要新增评论管理方法：`ListFlaggedComments`、`ApproveComment`、`DismissFlag`
- `internal/pkg/ratelimit/ratelimit.go` 的 `LimiterFunc` 适配器可以复用为扫描器的限流装饰器

### 方向 D：文件引用生命周期管理（P1）

**为什么需要**

阅读报告指出的"文件撤销分享后引用可达性"问题有 4.3 影响的产品侧解释 (a)/(b)/(c)，但当前选择了 (c)（从不检查），且没有文档。这会在以下场景引发问题：

1. **GDPR 被遗忘权**：用户要求删除所有数据时，SSO 发送用户删除事件，aero-id 需要级联删除或匿名化该用户的所有消息、评论、文件和引用。没有引用生命周期管理，这一操作需要全表扫描来查找 `owner_uid` 或 `file_id` 引用。

2. **存储成本优化**：当文件被所有引用者 unshare 后，它应该被 GC 回收。但当前 GC 只处理软删除的行，不处理"活跃但零引用"的文件。

**核心挑战**

1. **引用计数的一致性**：`storage_ref` 表已经是一个引用计数器（`GetCount`），但它的更新是手动维护的——不是通过 trigger 或级联操作。添加/移除引用和计数器更新之间缺乏事务性保证。

2. **跨实体查询的代价**：在每次 `GetMessage` 时检查文件的 `visibility` 需要额外的 JOIN 或子查询，这对已有性能压力的查询路径（消息列表）有显著影响。

3. **引用完整性校验的调度**：定期校验 ref count 与真实引用数的一致性需要扫描所有引用表，这应该是方向 A 的 Scheduler 框架中的一个后台任务。

**预期的架构变更**

```
internal/repository/reference.go  // 新文件：统一引用计数器管理
  GetReferenceCount(key) int
  IncrementReference(key) error    // 在 file share 或 message create 时调用
  DecrementReference(key) error    // 在 unshare 或 message/comment delete 时调用
```

同时增加一个 `ReferenceIntegrityChecker` 后台任务，周期性地校验 `storage_ref` 计数与实际引用的行数是否一致。

**对现有系统的影响**

- `UnshareFile`、`DeleteMessage`、`DeleteComment` 等操作的 service 层需要增加引用计数减一的操作
- `GetMessage`（消息详情）需要按策略选择 (a)/(b)/(c) 中的一个，并在返回消息时（至少异步地）检查文件可访问性
- 如果选择策略 (b)，需要返回一个 `file_unavailable` 占位符而不是完整的 `File` 对象

### 方向 E：结构化健康检查与实时运营视图（P1）

**为什么需要**

阅读报告指出当前 `/healthz` 的响应缺少 `latency_ms`、`last_success`、`failure_count` 等字段。在现有的 degraded 逻辑基础上，增加这些字段对运营商（PagerDuty、Grafana）的告警和诊断能力至关重要。

此外，`net/http/pprof` 在当前代码中不存在（我在 `router.go` 和全部 internal 中无一处 import `net/http/pprof`），这对于生产环境的性能问题排查是一个关键缺项。

**核心挑战**

1. **pprof 的暴露策略**：pprof 默认暴露了 `goroutine`、`heap`、`threadcreate`、`block`、`mutex` 等 profile。在生产环境中，需要通过 `pprof.Lookup("goroutine").WriteTo` 的安全封装或 deny list 来避免敏感信息泄露。

2. **健康检查的 Overhead**：增加 `latency_ms` 字段意味着每个检查需要记录历史时间窗口内的平均延迟。这需要在 `HealthHandler` 中维护一个滑动窗口或 EWMA 计算。

3. **运行时指标的外化**：除了 pprof，当前 `metrics.Registry` 不包含 Go 运行时指标（`go_memstats_*`、`go_goroutines`）。这些指标可以通过 `runtime.ReadMemStats` 获取，但需要考虑采集频率对大内存扫描的影响。

**对现有系统的影响**

| 改动项 | 工作量评估 | 风险 |
|--------|-----------|------|
| 在 /metrics 加入 go_memstats | 0.5 天 | 低 |
| 在 /readyz 加入 latency_ms 和 last_success | 1 天 | 低，向后兼容（只是新增字段） |
| 注册 pprof routes（/debug/pprof/*） | 0.5 天 | 低 |
| 加入 goroutine profile 到 /debug/pprof 并 accesslog 记录 | 0.5 天 | 中，需通过 middleware 限制 |
| **总计** | **2.5 天** | |

---

## 3. 接口设计建议

### 3.1 关键模块的接口设计原则

**原则 1：面向协议而非实现**

当前的 `outbox.Handler` 接口就是这一原则的优秀例子：

```go
type Handler interface {
    Handle(ctx context.Context, e *Event) error
}
```

仅 8 行，没有假设任何传输机制（HTTP、gRPC、本地调用），使 `Chain`、`WebhookHandler`、`NotificationDispatcher` 可以互换和组合。未来所有新增的后台处理逻辑都应遵循这一原则。

**原则 2：接口的单一消息传递**

`MetricsSink`（在 `outbox/relay.go` 中定义）也是好的例子——每个方法对应一个可观测性事件。但需要注意，当前 `MetricsSink` 只是 `relay` 包的内部接口。如果方向 A 的 Scheduler 框架诞生，应该将指标暴露提升为统一的 `MetricsProvider` 或 `Instrumentation` 接口。

**原则 3：Options 模式的延续**

当前多个 service 构建函数使用 `*Option` 函数式选项模式（如 `WithDispatcherBlockChecker`、`WithFileAuditLogger`）。这一模式应该延续到所有新抽象中。例如：

```go
type TaskOption func(*TaskConfig)
func WithTaskMetrics(reg *metrics.Registry) TaskOption { ... }
func WithTaskDrainOnStop(enabled bool) TaskOption { ... }
```

**原则 4：不要过早抽象**

当前 `internal/pkg/task` 尚不存在，但 4 处相同的 goroutine+ticker 模式已经超出了"过早抽象"的阈值。参考 AGENTS.md 的工程约束——"Same switch pattern appears in 3+ files → extract to shared type/strategy"——4 处重复代码已经触发了 refactor 触发器。

### 3.2 是否需要引入新的抽象层

**需要：Task 抽象层**

```
internal/pkg/task/
├── task.go          // 核心接口
├── runner.go        // 生命周期管理
├── periodic.go      // Periodic adaptor
├── metrics.go       // 自动接入 metrics.Registry
└── group.go         // 任务组的依赖管理和有序启动
```

这应该是一个轻量级（200-300 行）的抽象，将 4 个现有后台任务包装为 `Task` 实例，而不改变它们的内部实现。

**需要：Reference Lifecycle 抽象层**

当方向 D 的引用生命周期管理被实施时，需要一个新的包或现有 `repository/storage_ref.go` 的扩展，提供原子化的引用计数增/减操作和一致性校验。

**不需要：新的传输层抽象**

当前 HTTP + gRPC 的双传输模式工作良好。不需要引入新的传输协议（如 GraphQL、WebSocket 的业务消息）——SSE hub 已经覆盖了实时推送场景。

### 3.3 向后兼容性策略

1. **健康检查的增强**（方向 E）：在响应的 JSON 中新增字段而不同时删除现有字段。`{"status": "degraded", "checks": {...}, "latency_ms": {...}}` 的格式向下兼容所有现有客户端。

2. **Outbox 幂等化**（方向 B）：`idempotency_key` 列应为 nullable，已有行取 `NULL`。在 repository 层，`CreateOnce` 方法回退到 `Create` 当 `idempotency_key` 为空时。

3. **Metrics 扩展**（方向 A+E）：新增的指标系列（`task_*`、`go_memstats_*`、`health_*`）不会破坏已有 `/metrics` 的 Prometheus 格式，监控系统只会看到新的 series 被注册。

4. **Task 抽象化**（方向 A）：现有 `Start()` 方法保留其签名，新增的 `Task` 接口通过适配器模式（`PeriodicTask{s.Start}`）接入，不会破坏任何现有的测试或调用者。

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

根据五个方向的分析和对现有代码的审查，以下为各方向的技术选型分析：

| 方向 | 推荐方案 | 技术栈 | 新增依赖 |
|------|---------|--------|---------|
| A: Task Scheduler | 自建微框架 | 纯 Go 标准库 | 无 |
| B: Outbox 幂等化 | DB 唯一约束 + CreateOnce | Postgres `ON CONFLICT` | 无 |
| C: 内容安全扫描 | 插件式 Scanner 接口 | 标准库 `plugin` 或配置驱动 | 可选: cloud API SDK |
| D: 引用生命周期 | 现有 Postgres 增强 | DB 级约束 + 后台校验 | 无 |
| E: 结构化健康检查 | Go 标准库 pprof + runtime | `net/http/pprof` + `runtime` | 无 |

**结论：五个方向均不需要引入新的外部依赖。**

这实际上是当前代码库设计质量的一个强信号——核心基础设施（Postgres、outbox、metrics、config）已经覆盖了所有扩展方向的基础需求。仅在方向 C 中，如果选择接入外部内容安全 API（如 Google Cloud Vision、AWS Rekognition），会需要对应的 SDK 依赖，但这应该是可选/可替换的——通过 Scanner 接口注入。

### 4.2 关键依赖评估标准

对于未来可能进入的第三方依赖，建议使用以下评估矩阵：

| 标准 | 权重 | 说明 |
|------|------|------|
| 零对外部基础设施的假设 | 高 | 依赖不应假设 Redis、etcd 等外部服务存在（当前 Postgres 是唯一的基础设施依赖） |
| 零 CGO | 高 | 当前纯 Go，CGO 会破坏交叉编译 |
| 许可证兼容性 | 中 | 需要 MIT/Apache2/BSD 兼容，避免 AGPL |
| 活跃维护 | 中 | 至少最近 12 个月有提交 |
| 无 init() 副作用 | 高 | 避免在 init 中注册全局状态（如 `prometheus.MustRegister`） |

### 4.3 自建 vs 采购的决策依据

| 场景 | 决策 | 依据 |
|------|------|------|
| 后台任务调度 | **自建** | 4 个简单 ticker，不需要分布式任务队列的复杂度 |
| 内容安全扫描（基础） | **自建** | 规则引擎 + 关键词过滤 + 正则匹配 <= 200 行 |
| 内容安全扫描（高级） | **采购对接（通过接口替换）** | 需要 LLM 或图片审核时，接入第三方 API |
| 分布式限流 | **自建** | Postgres-backed 或基于 `pg_advisory_lock`，无需引入 Redis |
| 告警/监控 | **采购对接** | 通过 Prometheus 格式暴露，Grafana 等现有工具消费 |

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 方向 | 预期时间 | 依赖 |
|--------|------|---------|------|
| **P0** | B: Outbox 幂等化 | 2-3 天 | 无 |
| **P0** | A: 统一 Task Scheduler | 3-5 天 | 无 |
| **P1** | E: 结构化健康检查 | 2.5 天 | E 的部分（pprof）无依赖 |
| **P1** | D: 引用生命周期 | 4-7 天 | 依赖 A 的 Scheduler 框架 |
| **P1/P2** | C: 内容安全扫描 | 5-10 天 | 依赖 A（Scheduler）和 B（幂等性） |

### 5.2 阶段划分

**阶段一："基础设施加固"（第一周）**

```
里程碑 1.1（P0 完成，2-3 天）：
  ├─ 通知 dispatcher 增加幂等键（ON CONFLICT DO NOTHING）
  ├─ 迁移 idempotency_key 列（nullable）
  └─ make test && make lint 全绿

里程碑 1.2（P0 完成，3-5 天）：
  ├─ 实现 internal/pkg/task 微框架（接口 + periodic adaptor）
  ├─ 将 relay / sweeper / limiter-cleaner / idem-cleaner 包装为 Task
  ├─ app.go 中的 4 个 stop* 指针简化为一个 scheduler
  ├─ 新增 Task 指标：task_run_duration_seconds, task_failures_total
  └─ make test && make lint 全绿
```

**风险与缓解**：

- **风险**：Refactor 后台任务可能引入竞态条件（现有的 goroutine 关闭时序）
- **缓解**：在新的 `scheduler.Run()` 中先停止旧任务再启动新任务，使用 `sync.WaitGroup` 确认正在执行的 `Run()` 完成
- **验证**：`make test-integration`（需要 Docker，会产生真实的 Postgres 连接和定时器行为）

**阶段二："运营可观测性"（第二周）**

```
里程碑 2.1（P1 完成，2-3 天）：
  ├─ /metrics 新增 go_memstats 系列
  ├─ /debug/pprof/ 注册（受安全 middleware 保护）
  ├─ /readyz 新增 latency_ms、last_success、failure_count
  └─ make test && make lint 全绿
```

**阶段三："数据完整性"（第三周）**

```
里程碑 3.1（P1 完成，4-7 天）：
  ├─ 引用计数器的原子化操作（Increment/Decrement 在同一事务中）
  ├─ 为 UnshareFile、DeleteMessage、DeleteComment 增加引用计数操作
  ├─ 后台任务 ReferenceIntegrityChecker（挂载到 Scheduler）
  └─ make test && make lint 全绿
```

**阶段四："内容安全"（按需，第四周起）**

```
里程碑 4.1（P1/P2）：
  ├─ Scanner 接口定义（插件化）
  ├─ 基础规则引擎（关键词 + 正则匹配）
  ├─ 将 Scanner 插入 outbox.Chain
  ├─ 新增审计事件常量 AuditContentAutoFlagged 等
  ├─ admin service 新增评论管理方法
  └─ make test && make lint 全绿
```

### 5.3 风险矩阵

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **Outbox 幂等化引入死锁** | 低 | 高 | 在 `CreateOnce` 中使用 `INSERT ... ON CONFLICT DO NOTHING RETURNING id` 而非 `SELECT ... FOR UPDATE`，避免行锁 |
| **Task 抽象化破坏后台任务关闭时序** | 中 | 高 | 保留现有 `stop()` 签名作为 Task 内部实现，通过 `sync.WaitGroup` 保障关闭前当前 iteration 完成 |
| **健康检查增强暴露敏感信息** | 低 | 中 | pprof routes 仅在 `config.Server.Debug` 或特定 IP 白名单下注册 |
| **GC Sweeper 迁移到 Scheduler 后改变行为** | 中 | 中 | 在 `app.go` 中先用 adapter 模式验证，再逐步替换。跑 `make test-integration` 验证 MinIO 上的完整 GC 流程 |
| **引用计数器原子化导致性能退化** | 低 | 中 | `IncrementReference` 和 `DecrementReference` 在同一个写事务中执行，使用 `UPDATE storage_refs SET ref_count = ref_count + 1 WHERE key = ?` 的原子 SQL 而非读-改-写 |

---

## 总结

aero-id 的代码库质量非常高。分层清晰、接口设计简洁、事务边界明确、可观测性基础设施完整。五个扩展方向都基于确凿的代码级证据，且每个方向在现有的基础设施上都有直接或间接的铺垫：

- **方向 A**（统一 Scheduler）有且只有当前 4 个后台任务的重复模板作为触发证据
- **方向 B**（Outbox 幂等化）有 `dispatcher.go` 和 `relay.go` 中 at-least-once 语义与无条件写入之间的 gap 作为触发证据
- **方向 C**（内容安全）有 `outbox.Chain` 的天然插槽位置
- **方向 D**（引用生命周期）有 `storage_ref` 计数器 + GC sweeper 的 RefCounter 已有机制
- **方向 E**（结构化健康检查）有 `/readyz` 的三态逻辑和缺失的 pprof 作为触发证据

阅读报告中"跨越方向边界的洞察"（方向一 + 方向三的管线互操作性、方向二 + 方向五的指标互操作性、方向三 + 方向四的数据完整性交叉）是准确的，应在实施路线图中作为设计原则来利用。
