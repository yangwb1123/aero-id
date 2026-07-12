现在我有了全面的了解。以下是架构分析。

---

# 架构分析：aero-id

## 1. 架构评估

### 当前架构的优势

- **严格单向依赖**：`handler → service → repository → GORM`，外加正交的 `storage` 和 `sso`。这在宏观和微观层面都得到了贯彻，没有循环导入。代码评审很容易追踪数据流。
- **领域实体隔离**：`internal/domain` 中的纯 Go 类型，没有任何 GORM 或 gRPC 的耦合。像 `CommentStatus` 或 `FileCategory` 这样的不变性在 domain 包中集中实施，并通过编译器类型安全强制执行。
- **事务性 Outbox 模式**：核心洞察是正确的——在发送消息、创建评论、点赞等操作中，领域变更和 outbox 事件通过共享的 GORM `*gorm.DB` 在同一个数据库事务中写入。这使得异步通知可靠，无需两阶段提交。
- **Service 作为真相来源**：HTTP 和 gRPC handler 共享同一 service 层。传输代码保持为纯编组/解组，没有业务逻辑。两个传输协议之间的行为是位级一致的。
- **接口驱动适配器**：`storage.FileStorage`、`outbox.Repository`、`sso.Client`（通过缓存包装器）都定义明确的接口。`storage` 后端（本地 vs S3）可以切换，无需更改业务逻辑。
- **Gauges 的原子度量**：`metrics.Registry` 使用原子操作（`atomic.Uint64` 配合 `math.Float64bits`）用于 `outboxPending` 和 `outboxLagSecs`，这样 outbox drain（每 5 秒运行一次）永远不会阻塞 HTTP/gRPC 的 RED 指标写入。这是很好的工程实现。

### 局限性

1.  **Outbox 不是多副本安全的。** `FetchUnpublished`（`outbox/repository.go:64-72`）是一个简单的 `SELECT ... WHERE published_at IS NULL AND NOT dead_letter ORDER BY ... LIMIT`，没有 `FOR UPDATE SKIP LOCKED`。当两个中继实例（两个 `aero-id` 副本）同时轮询时，它们会取回相同的行，尝试处理它们，并导致重复通知。当前架构隐式地假设了单副本部署，但代码库和配置层次结构表明最终将进行水平扩展。

2.  **SSE Hub 完全在内存中且不跨副本。** `Hub` 由 `sync.Map` 支持，订阅映射中每个 uid 有一个 `map[int64]chan SSEEvent`。没有持久性，没有共享的 Redis/pub-sub 层。任何第二个副本都会拥有自己的、孤立的订阅映射，因此用户连接到副本 A 永远不会收到发往副本 B 用户的推送。此外，`hub.go:36` 中的通道缓冲区为 16——对于分块上传进度推送来说太浅，对于节流通知来说总是不够。

3.  **后台上下文是空的。** 六个后台进程（`RunOutboxCleaner`、`RunAuditCleaner`、`RunNotificationCleaner`、Outbox `Relay.Start`、Rate-limiter `RunCleaner`、GC sweeper、Idempotency `RunCleaner`）使用 `ctx := context.Background()`。没有超时，没有跟踪传播，没有终止信号（关闭是通过单独的 `done` 通道处理的）。跨进程的 6 个不同的停止函数 (`stopRelay`, `stopGC`, `stopRateLimit`, `stopIdemClean`, `stopAuditClean`, `stopNotifClean`, `stopOutboxClean`) 构成了一个脆弱的手动编排模式。

4.  **推送时存在 SSE 交付不一致。** `dispatcher.go:74-80` → `relay.go:120-125`：dispatcher 在 `MarkPublished` 之前推送 SSE。如果进程在 `hub.Publish` 之后但在 `repo.MarkPublished` 之前崩溃，事件会重播 -> 接收者会收到两次通知。如果时序严格性要求至少一次语义，则顺序必须反转：首先标记，然后推送——并接受在故障情况下，推送可能会丢失（最多一次）。

5.  **文件内容验证仅检查 HTTP header。** `mimeAllowed` 解析 `Content-Type` 字符串（`file.go:132-138`）并对照配置列表检查。`CompleteUpload` (`service_upload.go:80-91`) 只做 `Stat()` 来获取大小——它从不读取字节。恶意客户端可以用 `Content-Type: image/png` 上传特洛伊木马，但内容却是 ELF 可执行文件。

6.  **Counter 没有最终的漂移修复机制。** `Like`/`Unlike` 和 `CreateWithReplyBump` 在同一事务中使用原子 SQL `UPDATE ... SET like_count = like_count + 1`。这在事务边界内是一致的，但在异常情况下（硬删除、级联操作、回填），磁盘上的计数永远会与基础行偏离，且没有检测机制。

### 架构债务

| 债项 | 严重性 | 备注 |
|---|---|---|
| 后台 goroutine 缺少生命周期接口 | Medium | 6 个停止函数 vs. 一个统一的 `Closer` 接口 |
| `metrics.Registry` 在同一结构体下混合了 RED、SSO、outbox | Low | 工作正常，但随着自定义指标增多会变得脆弱 |
| SSE Hub `Publish` 对 channel 满采用静默丢弃 | Medium | 静默数据丢失——应记录或增加缓冲区 |
| `app/new.go` 作为组合根已有 380+ 行 | Medium | 接近 400 行触发值。应将工厂分组拆分 |
| `internal/comment/comment.go` 同时包含 `Repository` 接口和 `Service` 接口 | Low | 应该拆分到 `comment/repository.go` 和 `comment/service.go` |
| `internal/outbox/relay.go` 中的 `MetricsSink` 接口是 `outbox` 包的循环依赖解决方式 | Low | 足够干净，但接口应移至使用方 |

---

## 2. 扩展方向

###  方向 A：从单副本到多副本 Outbox（P0，延续方向一）

**为什么需要：** 当前 outbox 中继器在超过 1 个副本部署时会损坏。如果高可用性要求至少 2 个副本（这是一个常见的 SLO），则这是阻塞项。

**核心挑战：**
- `FetchUnpublished` 需要 `FOR UPDATE SKIP LOCKED` 以实现领券式语义。这使得中继从轮询变为领券——每个副本领取 N 个事件，其他副本会看到不同的集合。
- 每个事件需要添加一个 `locked_at` / `locked_by` 列，以便在副本崩溃后清理领券超时。
- 回退行为：`SKIP LOCKED` 需要 Postgres 9.5+（可以接受）。

**预期的架构变更：**

```
outbox/repository.go:
  FetchUnpublished  →  SELECT ... FOR UPDATE SKIP LOCKED LIMIT N
  + 添加 Unlock(ids) 用于处理优雅关闭
  + 添加 StaleLockCleaner  goroutine（替代当前独占的 Relock）

relay.go:
  每次 drain 现在会领取 → 处理 → 标记，而非 获取 → 处理 → 标记
  在持有锁期间，处理失败应不释放锁（而是递增重试计数并保留锁？）
  或者处理成功应释放锁 + 标记为已发布

config:
  添加 outbox.lock_timeout (默认 30s)
```

**对现有系统的影响：** 中等。relay.go 中的 drain 循环需要重新设计，但 domain 的其余部分不变。测试（`relay_test.go`）需要重写以练习领券行为。

###  方向 B：后台进程统一生命周期与可观测性（P1，延续方向二）

**为什么需要：** 六个后台 goroutine，六个停止函数，零指标，零健康检查断线检测。当 outbox 中继器在凌晨 3 点静默停止时，除非检查指标，否则无法检测到。

**核心挑战：**
- 每个进程有不同的停止方式（channel close vs ticker stop vs context cancel）。
- 需要标准化的指标（运行状态、心跳、最后成功时间、处理计数、错误计数）。
- 健康检查端点应报告后台进程的状态（Runnable → healthz 值）。

**预期的架构变更：**

```go
// internal/service/background.go （新包）
type Process interface {
    Name() string
    Run(ctx context.Context) error   // 阻塞直到 ctx 完成或返回错误
    Health() ProcessHealth
}

type ProcessHealth struct {
    Running      bool
    LastTick     time.Time
    TickCount    int64
    ErrorCount   int64
    LastError    string
}

// internal/app/runner.go （新文件）
type Runner struct {
    processes []Process
    metrics   *metrics.Registry
}
func (r *Runner) RunAll(ctx context.Context) error  // 使用 errgroup
func (r *Runner) HealthZ() map[string]ProcessHealth
```

这将用单个 `Runner.RunAll(ctx)` 替换 6 个 `stop*` 函数，该函数使用 `context.CancelFunc`。每个进程获取一个带有跟踪父上下文的命名上下文，因此 pprof/traces 显示 goroutine 来源。

**对现有系统的影响：** 低。这是对 `app/new.go` 和 `app/app.go` 的重构。现有的 goroutine 主体被包装成 `Process` 实现。行为没有变化。

###  方向 C：文件内容验证层（P1，延续方向三）

**为什么需要：** 允许用户声明 `Content-Type: image/png` 但上传 ELF 二进制文件是一个安全问题。存储后端无法区分——它存储字节。当 CDN/浏览器在下载时处理错误类型时，也是用户信任问题。

**核心挑战：**
- 在 `CompleteUpload` 路径中读取内容：需要流式读取前 512 字节（魔数检测）而不将整个文件缓冲到内存中。
- S3/MinIO 已经存在对象——我们必须 `GetObject` 一个范围（前 512 字节），检查，然后丢弃。这增加了一次额外的 S3 GET 往返和成本。
- 需要存储检测到的 MIME（`File.DetectedMimeType` 字段），以便审计可以区分用户声明的和服务器检测到的。

**预期的架构变更：**

```go
// internal/domain/file.go
type File struct {
    // ... 现有字段
    DetectedMimeType string  // ← 新增：由读取前 512 字节的检测器填充
}

// internal/pkg/magic （新包）
type Detector interface {
    Detect(r io.Reader) (mimeType string, err error)
}
// 使用 https://github.com/h2non/filetype （纯 Go，零依赖）
// 或 stdlib net/http 的 DetectContentType

// internal/file/service_upload.go
func (s *fileService) CompleteUpload(...) {
    // ... 现有 Stat 检查 ...
    detector := magic.New()
    detectedMIME, _ := s.store.PeekContent(ctx, key, 512, detector)
    // 检查 detectedMIME 是否与 claimMIME 在类别层面匹配
    // 将 detectedMIME 存储在新建的 File 记录中
}
```

存储接口获得一个新方法：

```go
// storage/storage.go
type FileStorage interface {
    // ... 现有方法 ...
    PeekContent(ctx context.Context, key string, n int64, detector ContentDetector) (string, error)
}
```

**对现有系统的影响：** 低。新字段 `DetectedMimeType` 在域模型中增加了 4 字节。`PeekContent` 可以回退到 `Get(ctx, key)` 读取前 N 字节，因此不破坏现有后端。对于 S3，它使用 `GetObject` 加 `Range` 头。

###  方向 D：Counter 漂移检测与修复（P2）

**为什么需要：** 虽然 `like_count` 和 `reply_count` 在事务内是一致的，但任何从备份恢复、迁移期间的逻辑错误或手动数据操作都会导致无限期的漂移。用户会看到「23 个赞」，但实际只有 22 行。

**核心挑战：**
- 漂移检测需要 `SELECT COUNT(*)` 扫描，这对于大评论（数十万赞）来说成本很高。
- 修复需要 `UPDATE comments SET like_count = (SELECT COUNT(*) FROM comment_likes WHERE comment_id = comments.id)`，这会在修复运行期间锁定行。
- 需要速率限制，以便修复不会淹没生产数据库。

**预期的架构变更：**

```go
// internal/repository/counter.go （新文件）
type CounterReconciler interface {
    ReconcileCommentLikes(ctx context.Context, batchSize int) (fixed int64, errors int64, err error)
    ReconcileReplyCounts(ctx context.Context, batchSize int) (fixed int64, errors int64, err error)
}

// 实现：
// 1. 查找 like_count != actual_count 的 comments（使用子查询）
// 2. 使用 UPDATE ... FROM (SELECT ...) 进行批量修复
// 3. 记录到 audit log
// 4. 通过指标公开已修复 + 已检查的计数

// RunCounterReconciler 是一个新的后台进程（进程 #7），定期运行
func RunCounterReconciler(...) Process { ... }
```

**对现有系统的影响：** 非常低。只读后台进程，不更改任何现有数据路径。默认情况下可以关闭。

###  方向 E：多传输协议身份传播的 SSO 断路器形式化（P1）

**为什么需要：** SSO 是一个外部 gRPC 依赖。当它宕机时，现有断路器（在 `internal/sso` 中）会快失败，但 HTTP 和 gRPC 传输协议各有不同的错误传播策略。当 SSO 宕机时，应该有一个一致的「SSO 降级」策略，而不是在两个传输层重复猜测重试逻辑。

**核心挑战：**
- SSO 断路器已由 `sso.Client` 实现，但健康状态同时反馈给 HTTP（通过 `X-Aero-Degraded` 头）和 gRPC（通过 `healthz` 报告 NOT_SERVING）。
- 传输层的重试策略（HTTP 中间件重试 vs gRPC 拦截器重试）可能不一致。
- 当 SSO 降级时，SSO 缓存提供有限的保护，但 `TokenCache` TTL 默认为 5 分钟，`UserCache` TTL 为 10 分钟。降级时期望的行为是「通过每个句柄」？还是「硬拒绝验证」？

**预期的架构变更：**

```go
// internal/sso/degradation.go （新文件）
// 单体化降级决策：
type DegradationStrategy int
const (
    DegradeHard     // 当 SSO 宕机时拒绝所有请求
    DegradeStale    // 允许缓存有效负载通过，拒绝新的/未知的令牌
    DegradePermissive // 允许所有请求通过（仅开发使用）
)

// 通过配置注入
type ClientConfig struct {
    // ...现有字段...
    Degradation DegradationStrategy
}
```

**对现有系统的影响：** 低。添加枚举，更改断路器的 `.Allow()` 以咨询策略。

---

## 3. 接口设计建议

### 当前接口质量评估

| 接口 | 评估 | 建议 |
|---|---|---|
| `storage.FileStorage` | 好。7 个方法，清晰的语义。 | 添加 `PeekContent` 用于 magic 检测。考虑 `PresignUpload` + `PresignGet` 的名称空间一致性。 |
| `outbox.Repository` | 好。5 个方法。 | `FetchUnpublished` 需要 `FOR UPDATE SKIP LOCKED` 变体。 |
| `outbox.Handler` | 好。1 个方法。 | 考虑 `Handle(ctx, *Event) error` 添加 `ack`/`nack` 语义以支持基于拉取的交付。 |
| `comment.Repository` | 方法过多（16 个）。 | 通过关注点拆分：`CommentReader`（Get, List, Search）、`CommentWriter`（Create, Edit, Delete）、`ReactionStore`（Like, Unlike）、`ReportStore`（CreateReport, ListReports, ResolveReport） |
| `notification.Repository` | 5 个方法，整洁。 | 无需更改。 |
| `message.MessageRepository` | 良好边界。 | 考虑将 `SearchMessages` 提取到单独的 `Searcher` 接口。 |

### 建议的新接口

**1. `BackgroundProcess`（统一生命周期）**

```go
// internal/app/process.go
type BackgroundProcess interface {
    Name() string
    Run(ctx context.Context) error   // 阻塞直到 ctx 取消；返回错误表示崩溃
    Health() ProcessHealth           // 用于健康检查端点
}

type ProcessHealth struct {
    Running       bool
    StartedAt     time.Time
    LastTick      time.Time
    TickCount     int64
    ErrorCount    int64
    LastErrorMsg  string
}
```

这使得 `app/shutdown.go` 变成了一个 `for _, p := range processes { p.Shutdown() }` 循环，而不是 7 个特定的 `stop*` 调用。

**2. `ContentDetector`（用于 magic 检测）**

```go
// internal/pkg/magic/detector.go
type ContentDetector interface {
    Detect(r io.Reader) (mimeType string, confidence float64, err error)
}

// 标准实现：stdlib net/http 的 DetectContentType（使用前 512 字节）
// 允许插拔：filetype、tika、或自定义规则
```

**3. `CounterReconciler`（用于一致性修复）**

```go
// internal/repository/reconciler.go
type CounterReconciler interface {
    ReconcileCommentLikeCounts(ctx context.Context, batchSize int, dryRun bool) (ReconcileReport, error)
    ReconcileCommentReplyCounts(ctx context.Context, batchSize int, dryRun bool) (ReconcileReport, error)
}

type ReconcileReport struct {
    Scanned int64
    Fixed   int64
    Errors  int64
    Details []ReconcileItem
}
```

### 向后兼容性

- 所有新接口都添加了可选实现。服务类型断言检查（`if lister, ok := store.(Lister); ok { ... }`），就像现有的 `Lister` 模式一样。
- 领域模型的新字段（例如 `File.DetectedMimeType`）可为零值。现有行返回空字符串，这是有效输入。
- `BackgroundProcess` 与现有停止函数共存。可以在不进行全量迁移的情况下逐步采用。

---

## 4. 技术选型

### 现有技术栈评估

| 技术 | 评估 |
|---|---|
| **Go 1.26** | 合理。Gorm v2 + stdlib 覆盖所有需求。 |
| **GORM v2** | 合适的 ORM。`clause.OnConflict`、`Expr`、事务等都如文档所示。直接使用 `*gorm.DB` 作为事务句柄的架构是正确的。 |
| **gRPC + protobuf** | 适用于 SSO 通信和内部传输。无需更改。protobuf v1.34.1 + grpc v1.5.1 的版本固定是良好的管控。 |
| **Prometheus 指标** | 自制的 `metrics.Registry` 干净且快速。但它需要为每个新的指标组手动编写模板。当团队希望添加每个端点 RED、每个缓存、每个进程、每个存储后端指标时，这会变得繁琐。 |
| **testcontainers** | 集成测试的正确选择。 |

### 建议的新依赖

| 方向 | 建议 | 评估 | 回退 |
|---|---|---|---|
| Magic 检测 | `github.com/h2non/filetype v1.1` | 纯 Go，零 C 依赖，`go.mod` 增加 < 100KB。许可证：MIT。 | `net/http.DetectContentType` |
| SSE 多副本 | `github.com/redis/go-redbus` 或 `github.com/go-redis/redis` | 如果 SSE 需要水平扩展，Shared Redis pub/sub 是最简单的选择。 | `sync.Map`（仅单副本） |
| OpenTelemetry 指标 | 使用 `go.opentelemetry.io/otel/sdk/metric` 替代自制方案 | 自制的 registry 已经足够。对于 > 500 个时间序列，切换到 OTel SDK 以获得标准推拉模式。 | 自制 registry（对于当前规模没问题） |
| 后台进程恢复 | 不需要新的依赖。`golang.org/x/sync/errgroup` 已在模块中。 | 使用 `errgroup.WithContext` 来统一生命周期。 | |

### 自建 vs 采购

| 决策 | 建议 | 理由 |
|---|---|---|
| 指标 registry | **保持自制** | 当前 200 行的实现精确满足了 RED + SSO + Outbox 的需求。在需要 OTel 推送到 Prometheus Remote Write 之前，无添加依赖的必要。 |
| SSE Hub | **保持自制用于单副本；采购 Redis Pub/Sub 用于多副本** | 自制的 `sync.Map` hub 架构正确，但无法水平扩展。多副本 SSE 本质上是一个分布式 pub/sub 问题；Redis 是经过考验的标准解决方案。Pub/Sub 的消息缓冲需要每个主题一个 Redis 列表 + `BLMOVE`。 |
| Magic 检测 | **采购 `h2non/filetype` 或使用 `net/http.DetectContentType`** | 编写专用的魔数检测器是繁琐且容易出错的。stdlib 的 `DetectContentType` 涵盖常见类型；`filetype` 补充了专门的类型（HEIC、WebP、3MF）。 |
| 断路器 | **保持自制** | SSO 的现有断路器（`internal/sso/breaker.go`）是干净的。无需引入 Hystrix 或 Resilience4j。 |

---

## 5. 实施路线图

### 优先级排序

| 优先级 | 方向 | 努力量 | 风险 | 业务价值 |
|---|---|---|---|---|
| **P0** | A：多副本 Outbox | 1-2 天 | 高：会影响所有异步通知 | 需要 > 1 个副本用于 HA |
| **P1** | B：统一后台生命周期 | 2 天 | 中：大型重构 | 运营可观测性 |
| **P1** | C：文件内容验证 | 1 天 | 低 | 安全加固 |
| **P1** | E：SSO 降级形式化 | 0.5 天 | 低 | 行为一致性 |
| **P2** | D：Counter 漂移检测 | 2 天 | 低 | 数据完整性 |

### 阶段划分

**阶段 1（P0 — 冲刺 1）：多副本 Outbox**

```
里程碑：
  - outbox/repository.go：FetchUnpublished 使用 FOR UPDATE SKIP LOCKED
  - 新列：outbox_events.locked_at，outbox_events.locked_by
  - 新 goroutine：StaleLockCleaner（运行频率较 relay 频率低）
  - relay.go：当锁获取失败时优雅回退（等待并重试）
  - 测试：repository_integration_test.go 使用并发 relay 模拟
  - make test && make lint 通过
```

**阶段 2（P1 — 冲刺 2）：后台生命周期 + 可观测性**

```
里程碑：
  - app/process.go：引入 BackgroundProcess 接口
  - app/runner.go：errgroup 驱动的 Runner
  - 迁移：outbox.RunOutboxCleaner → Process 包装器
  - 迁移：repository.RunAuditCleaner → Process 包装器
  - 迁移：repository.RunNotificationCleaner → Process 包装器
  - 迁移：outbox.Relay.Start → Process 包装器
  - 迁移：idempotency.RunCleaner → Process 包装器
  - 迁移：rate limit RunCleaner → Process 包装器
  - 迁移：GC sweeper → Process 包装器
  - 健康检查端点：/debug/processes 输出每个进程的状态
  - 指标：aeroid_process_up{name="..."}，aeroid_process_tick_count{name="..."}，aeroid_process_error_count{name="..."}
  - app/app.go：用 Runner.RunAll(ctx) 替换单个 stop* 字段
  - make test && make lint 通过
```

**阶段 3（P1 — 冲刺 2 并行）：文件内容验证**

```
里程碑：
  - 内部/pkg/magic：Detector 接口 + magicNumber 实现
  - storage.FileStorage：PeekContent 方法 + S3 后端实现
  - domain.File.DetectedMimeType：新字段 + 迁移
  - service_upload.go：在 CompleteUpload 中调用 PeekContent
  - 当声明 MIME 与检测到的 MIME 类别不匹配时的日志/拒绝策略（可配置）
  - 测试：带有虚假内容和 S3 后端的集成测试
  - make test && make lint 通过
```

**阶段 4（P2 — 冲刺 3）：Counter 一致性**

```
里程碑：
  - repository/reconciler.go：CounterReconciler 接口
  - 实现：ReconcileCommentLikeCounts（使用 COUNT(*) 子查询）
  - 实现：ReconcileCommentReplyCounts
  - 新 Process：RunCounterReconciler（默认关闭，通过配置 opt-in）
  - 指标：aeroid_counter_reconciler_scanned，aeroid_counter_reconciler_fixed，aeroid_counter_reconciler_errors
  - 审计日志：每次校正都记录到 audit_events
  - make test && make lint 通过
```

### 风险与缓解

| 风险 | 可能性 | 影响 | 缓解 |
|---|---|---|---|
| `FOR UPDATE SKIP LOCKED` 增加了 outbox 轮的延迟 | 低 | 中 | 在 `app/new.go` 中使 `outbox.lock_timeout` 可配置。保留按 backoff 跳过的机制。 |
| 后台进程包装器引入了竞态条件 | 中 | 高 | 构建时测试：`app/runner_test.go` 模拟进程 panic 并验证 ctx 传播。已经存在的集成测试覆盖了干净关闭。 |
| Magic 检测错误地拒绝了有效文件 | 低 | 中 | 使用 `confidence` 阈值。对于 < 0.9 置信度，记录警告但基于 header 授权。可通过 `storage.validation_mode: strict | warn | off` 配置。 |
| Counter 修复在运行时锁定行 | 中 | 低 | 批量处理（一次 100 行）。使用 `statement_timeout` 保护。白天以 `dry_run: true` 运行，晚上在维护窗口运行 `dry_run: false`。 |
| SSE 多副本强制使用 Redis 依赖 | 中 | 中 | 使 SSE 后端可插拔：`config.sse.backend: memory | redis`。内存模式是开发/单副本默认值。Redis 模式仅在配置时引入依赖。 |
