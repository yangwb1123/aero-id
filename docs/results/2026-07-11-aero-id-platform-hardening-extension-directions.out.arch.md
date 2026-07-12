现在我已彻底审查了该代码库。以下是我的全面架构分析。

---

# 架构分析：aero-id

## 1. 架构评估

### 1.1 当前架构的优势

**明确的分层与单向依赖。** 代码库严格执行 `handler → service → repository/storage/sso` 的分层。组合根（`internal/app/app.go`）是唯一的依赖注入点。这为每个层面提供了清晰的关注点分离和测试隔离。

**利用接口实现依赖倒置。** `FileStorage`、`Store`（幂等性）、`AuditLogger`、`UserDirectory` 均定义为接口。存储后端（local vs S3）通过配置切换，且 `Lister` 作为可选接口添加——这是典型的 Go 式组合，优于继承。

**事务性发件箱模式已经到位。** `outbox.Relay` + `outbox.GormRepository` 确保领域事件以与数据行相同的事务被持久化，然后在外部进行扇出。这解决了分布式系统中一个被低估的可靠性问题，并且实现得干净利落——轮询间隔可配置，最大重试次数，以及可选的 webhook 链。

**限流器已正确嵌入。** 令牌桶实现（`internal/pkg/ratelimit`）是线程安全的，支持空闲驱逐，并结合了基于 actor 的键控（IP 回退）。它在 HTTP 和 gRPC 层的使用方式一致（`middleware.RateLimit` / `RateLimitInterceptor`）。

**断路器隔离 SSO 故障。** 三态断路器（closed/open/half-open）与 stale-cache 回退相结合，防止 SSO 中断级联影响到认证。

**优雅降级基础设施已部分到位。** 冷启动预热会探测每个依赖的健康状况，并将结果记录为警告而非致命错误。`/readyz` 端点报告每个依赖的健康和关键性。

### 1.2 局限性

**错误模型在传输层不一致。** 这是最严重的架构问题。`AppError` 通过 `GRPC` 状态码和 `HTTP` 状态码携带一个结构化 `Code`（如 `file.too_large`）。gRPC 层（`server.go` 中的 `grpcError`）丢弃 `Code`，仅返回 gRPC 状态码和消息。这意味着 gRPC 消费者看不到 `comment.nesting_too_deep` 或 `file.share_invalid`——他们只看到 `InvalidArgument`。HTTP 层也丢弃了 `Code`，但至少通过 JSON 将其发送出去。这个问题确认了评估中的观察结果。

**领域实体缺少业务操作。** 领域层包含贫血实体（`Comment`、`File`、`Message`）和简单的验证函数（`ValidateNewComment`），但没有领域服务或聚合根。类似 `canReadFile`、`canReadMessage` 的方法作为包级函数存在于 `service` 层。这种贫血的领域模型增加了服务层中的认知负担，并使业务规则更难测试和推理。

**存储管道没有元数据处理。** `FileStorage.Put(ctx, ownerUID, name, reader)` 原样写入字节。MIME 验证仅在服务层触发，但不会沿管道传递。没有图像转换、地理位置剥离或病毒扫描钩子。这对于合规性（GDPR、地理位置数据）和平台增长来说是一个硬性限制。

**缺少响应级系统状态标头。** 消费者无法判断 aero-id 是否处于降级模式。没有 `X-Aero-Status: healthy/degraded` 之类的标头，也没有 `X-RateLimit-Limit/Remaining/Reset` 标头。这使得客户端无法自适应地调整其行为。

**幂等性一直未与限流集成。** Idempotency middleware 位于限流之后。幂等键上的重试会消耗限流令牌。修复此问题需要将幂等性检查上移至限流之前。

### 1.3 架构债务

- **gRPC 端的错误码丢弃。** 这是关于跨传输协议契约一致性的架构债务。
- **81 个错误码分散在 76 个文件中。** 尽管 `errors.go` 提供了良好的构造函数，但错误码的扩散表明缺乏集中注册。
- **Author-Scoped 自管理作为 RBAC 的替代。** 当前的自管理模型（评论作者管理自身评论的报告、隐藏/取消隐藏是自管理的）是故意为之，但在多管理员场景下会达到极限。
- **存储 GC 为 opt-in。** 配置需要在生产环境主动打开 `storage.gc.enabled`；否则，残片会不断累积。这属于"默认不安全"。

---

## 2. 扩展方向

### 方向 1：自适应限流与速率限制标头 —— P0

**为什么需要：** 消费者需要预测限制。没有 `X-RateLimit-Limit/Remaining/Reset`，客户端就像在暗中摸索。此外，基于 IP 的限流器缺乏对共享白名单用户或紧急流量的感知能力。将此与系统状态（方向 5）结合，能够实现从盲目重试到智能退避的转变。

**核心挑战与技术难点：**
- 令牌桶本身提供了"剩余令牌数"，但这必须以每个请求为粒度进行暴露，同时避免在快速路径上锁竞争。
- 剩余时间是通过 `(burst - tokens) / rate` 估算的，这种估算天生是近似的。
- 如果启用「写操作幂等键绕过速率限制」（将幂等性检查移至限流之前），这会形成一个有两个入口和一条退出路径的复杂中间件链。

**预期架构变更：**
- 为 `Limiter` 添加 `Limit(key string) (remaining int, reset time.Duration)` 方法。
- 考虑是否将幂等性中间件上移至速率限制中间件之前。
- 添加 `X-RateLimit-*` 标头到 HTTP 中间件；添加 `RateLimitInfo` 字段到 gRPC 元数据。

**对现有系统的影响：** 低风险。限流器已经是可选且无状态的。新的标头/元数据只是附加信息。幂等性重新排序需要仔细考虑，以确保在幂等响应重放期间不会改变语义。

### 方向 2：媒体隐私管线 —— P1（根据评估建议从 P1 提升为 P0）

**为什么需要：** 用户上传的文件可能包含 EXIF 中的地理位置数据。在没有处理的情况下进行存储会带来 GDPR 合规风险。地理数据需要根据用户的显式同意被剥离、标记或保留。此外，在部署反病毒扫描和内容审查（CSAM 扫描）之前，平台无法扩展到不支持的内容类型。

*业务价值：* GDPR 合规（最高罚款可达全球年营业额的 4%）。内容安全（CSAM 扫描）。平台信任（用户知道文件在被使用之前都经过处理）。

*技术价值：* 管道模式是可扩展的——每个处理器都可以独立添加、测试和配置。

**核心挑战与技术难点：**
- 管道必须在 `FileStorage.Put` 内部运行，但 `FileStorage` 作为一个接口，没有感知管道的概念。
- 对于大文件，管道不能完全缓冲到内存中。
- 从本地后端生成的签名下载 URL 不包含 `Content-Disposition: attachment`。用户上传了包含 HTML 的 `.txt` 文件并通过签名 URL 访问？这是潜在的 XSS。
- 管道需要故障模式：是拒绝整个上传（硬阻断），还是让上传通过但标记（软阻断）？
- ImageMagick / libvips 等外部工具会引入进程级别的依赖。

**预期架构变更：**
- 引入一个 `ProcessingPipeline` 接口（`Process(ctx, reader) -> (reader, meta, error)`），该接口运行在 `storage.Put` 内部或作为包装存储的装饰器。
- 将 `Content-Disposition: attachment` 和 `X-Content-Type-Options: nosniff` 添加到签名下载 HTTP 响应中。
- 添加 `storage.processors` 配置节，用于启用/禁用每个处理器。

**对现有系统的影响：** 中等风险。将管道引入 `storage.Put` 意味着存储层的责任更重。替代方案是保持 `FileStorage` 纯净无状态，并将管道移动到 `service.Create` 中——但这样在 S3 直接上传模式下，管道会被绕过。也许 S3 客户端也需要一个处理过的上传路径。

### 方向 3：降级契约与系统状态传播 —— P0

**为什么需要：** 已经存在 SSO 的断路器 + 备用缓存，还有 `/readyz`，但业务逻辑对此一无所知。当断路器打开时，服务应该继续为经过身份验证的读取操作提供服务，同时对写入操作返回 503。当前的行为是默默回退到备用缓存，而不是主动告知客户端。

*业务价值：* 在 SSO 中断时保持读取操作的可用性。在 SSO 恢复时减少"惊群"效应。

**预期架构变更：**
- 在 `sso.Client` 上添加一个 `Status(ctx) Degradation` 方法，该方法公开断路器状态和缓存状态。
- 在 `app` 包中添加一个 `degradation.Monitor`，它轮询每个依赖项，并维护一个全局的"系统状态"。
- 添加 `X-Aero-Status` 响应标头。
- 在服务层中根据降级状态改变行为（例如，如果 SSO 断路器打开且缓存还未命中，则允许读取但拒绝写入）。

**对现有系统的影响：** 中等风险。需要一个新的 `degradation` 包和一个全局监控器。服务层需要访问它，这意味着需要一个新的构造函数参数或一个通过 context 传递的值。

### 方向 4：错误码治理与集中式注册 —— P1

**为什么需要：** 81 个错误码分布在 76 个文件中使得文档几乎不可能维护。gRPC 端丢失的代码意味着二进制消费者无法对结构化错误做出反应。

*业务价值：* 消费者契约稳定性。更简单的 SDK 生成。更好的自动化测试（基于错误码断言而非消息字符串）。

**预期架构变更：**
- 将 `AppError.Code` 字符串重构为一个由中央（可能是代码生成的）文件提供的类型化常量。
- 添加 `Retryable bool`、`FieldErrors map[string]string`、`DocURL string` 到 `AppError`。
- 修复 `grpcError` 以将 `ae.Code` 放到 gRPC 状态细节中（通过 `status.New(codes.InvalidArgument, msg).WithDetails(&errorDetail{Code: ae.Code})` 使用 `google.golang.org/genproto/googleapis/rpc/errdetails`）。
- 添加一个 `make check-error-codes` 脚本，验证所有服务层方法是否使用已注册的代码。

**对现有系统的影响：** 中等风险。引入 `Retryable` 需要审计所有调用点以确定适当的值。更改 gRPC 错误映射是一项破坏性变更（但如果 1.0 版之后才部署且文档记录清晰，可能可以接受）。

### 方向 5：分布式速率限制 —— P2（之前从 P1 降级）

**为什么需要：** 实例级限流意味着在横向扩展时，每个节点的有效容量为 `rate * N`。恶意行为者可以通过在节点间轮换请求来绕过限制。

*核心挑战：* 需要 Redis/Allegro Tarjan 的共同愿景。现有架构专门为了避免外部依赖而将限流设计为按实例进行。引入 Redis 会带来运维依赖性。

*技术上可以简化：* 令牌桶算法允许通过 synchronized Redis Lua 脚本或 `INCR` + `EXPIRE` 滑动窗口实现粗略的共享状态。但延迟很重要——与 SSO 验证请求 SSO 同时进行验证，意味着每个请求额外增加一次 RTT。

*不建议：* 对于当前规模，实例级限流配合合理的 `rate * N_instances` 因子部署，应该就足够了。在转向分布式限流之前，先实现 `X-RateLimit-*` 标头并添加幂等性感知的限流。

---

## 3. 接口设计建议

### 3.1 当前接口设计原则

代码库在接口分离方面大体不错。`FileStorage` 是一个连贯的抽象，具有获取、放置、删除、预设、列出、统计和新建键的功能。幂等性的 `Store` 接口具有清晰的关注点：`Reserve`、`Complete`、`Release`。

### 3.2 引入的新抽象

**`ProcessingPipeline`（可选）**
```go
type Processor interface {
    Name() string
    Process(ctx context.Context, r io.Reader, meta map[string]any) (out io.Reader, outMeta map[string]any, err error)
}
type Pipeline struct { processors []Processor }
```
管道可以依据 `storage.{type}.pipeline` 配置来配置启用。

**`DegradationMonitor`（推荐）**
```go
type Degradation string
const (
    DegradationNone  Degradation = ""
    DegradationSSO   Degradation = "sso_degraded"
    DegradationDB    Degradation = "db_degraded"
    DegradationS3    Degradation = "storage_degraded"
)
type Monitor interface {
    Status(ctx context.Context) (Degradation, error)
}
```
将其注入到 `app.App` 中，`service` 层可以通过一个可选的服务选项来引用它（如 `service.WithDegradationMonitor(m)`）。

**`ErrorRegistry`（推荐，如果要对错误码进行治理）**
```go
type ErrorInfo struct {
    Code       string
    HTTP       int
    GRPC       codes.Code
    Message    string
    Retryable  bool
    DocURL     string
}
```
这可以由 linter 驱动：测试可以验证所有服务错误是否来自注册表，以及注册表中的所有代码是否在至少一个地方被使用。

### 3.3 后续兼容性

通过使用配置和可选接口（如 `Lister` 的处理方式）维护现有的 1.0 行为。
- `ProcessingPipeline`：默认情况下是一个空管道（直接传递）。添加处理器属于 opt-in。
- `DegradationMonitor`：默认行为是始终报告 `DegradationNone`。
- `ErrorRegistry`：纯粹的代码组织变更；1.0 的消费者看不到任何变化。gRPC 错误细节的出现属于 wire 格式的变更，但对现有消费者不具备破坏性——他们是忽略还是读取这些细节，都无所谓。

---

## 4. 技术选型

### 4.1 所需的新技术

**对于媒体管道（方向 2）：**
- **libvips（通过纯 Go 绑定，如 `govips`）：** 相比 ImageMagick 更轻量级、更快速，内存占用更低。适用于图像文件的元数据剥离（EXIF 清理）和缩略图生成。许可证为 LGPLv2.1+，可作为系统库依赖使用。
- **`github.com/rwcarlsen/goexif`：** 用于 EXIF 数据提取和校验——但建议使用 libvips 的 `vips_image_remove_metadata()` 以简化操作。
- **`ClamAV`（通过 `github.com/dutchcoders/go-clamd` 或与 `clamd` 的 Unix 套接字通信）：** 用于上传文件的反病毒扫描。这属于 opt-in，且为平台级特性。资源消耗可能较高，因此应当通过超时和基于大小的限制来使用。

**对于分布式限流（方向 5，非当前优先）：**
- **Redis：** 通过 Lua 脚本 `INCR` + `EXPIRE` 实现滑动窗口——简单，现成，但每个请求增加一次网络往返。
- **Tarjan（`github.com/allegro/bigcache` + 流言协议）：** 无中心化，但需要节点间协调。对于 aero-id 的规模而言可能过度设计。

**对于错误注册表和代码生成（方向 4）：**
- 不需要新的依赖项。一个 `make generate` 目标，使用 `text/template` 从 YAML 文件生成 Go 代码就足够了。Golang 的 `go:generate` 直接支持这一点。

### 4.2 第三方依赖评估标准

1. **为什必要？** 功能是否无法通过十几行标准库代码实现？
2. **许可证兼容性？** 必须为 MIT、BSD、Apache 2.0。避免 AGPL/SSPL。
3. **核心依赖性？** 例如 `govips` 需要系统 libvips。这会增加 CI/Docker 复杂度和攻击面。
4. **发布频率？** 上次提交距今不到 1 年？是否有已知的 CVE？
5. **测试覆盖？** 包是否经过测试且足够稳定，能够支持生产环境使用？

### 4.3 自建 vs 采购

**限流：** 自建且运行良好。令牌桶实现是 80 行可清晰测试的代码。只需向 `Limiter` 添加 `Limit()` 方法。

**媒体管道：** 对于 EXIF 剥离和缩略图生成，应使用 libvips（利用现有工具，避免重复造轮子）。对于自定义元数据注入（如内容标记），应当自建——因为这属于独特业务逻辑。

**反病毒扫描：** 应当自建集成，但 ClamAV 是后端。Aero-id 不需要自建扫描引擎。

**错误码注册表：** 自建 YAML + `go:generate`。不需要外部框架。

---

## 5. 实施路线图

### 5.1 优先级排序与阶段划分

| 阶段 | 方向 | 工作内容 | 预估影响 |
|---|---|---|---|
| **Sprint 1** (P0) | 方向 1 + 方向 5 合并 | `X-RateLimit-*` 标头 + `X-Aero-Status` 标头 | 仅涉及中间件；无业务逻辑变更 |
| **Sprint 2** (P0) | 方向 5 — 降级传播 | 将降级状态传播到业务逻辑层（SSO 断路器 → 读写策略） | 中等；需要新包 + 服务层感知 |
| **Sprint 3** (P0) | 方向 4 — gRPC 错误码修复 | 发送结构化代码和 gRPC 状态细节 | 低/中；跨传输契约对齐 |
| **Sprint 4** (P1 → P0) | 方向 2 — 媒体管道 | 添加 `Content-Disposition` + `Pipeline` 接口 + EXIF 清除 | 高；存储层变更 |
| **Sprint 5** (P1) | 方向 4 — 错误注册表 | 从 81 个错误码迁移到中央注册表 + `make check-error-codes` | 中；大量查找替换 |
| **Sprint 6** (P1) | 方向 3 — 自适应限流 | 幂等性感知 + 实例级自适应行为 | 中 |
| **P2** | 方向 3 — 分布式限流 | Redis 看涨期权 | 高影响面，可推迟 |

### 5.2 风险与缓解策略

**高风险：Sprint 4（媒体管道）。**
- *风险：* 图像处理延迟不可预测。大文件可能超时。
- *缓解：* 在 `Process` 中使用 context 超时。对于超大图像，跳过管道。可以配置为警告而非阻断。
- *退出：* 将 `ProcessingPipeline` 作为 opt-in 实现（默认情况下是一个空管道）。

**中风险：Sprint 2（降级传播）。**
- *风险：* 在降级模式下拒绝写入操作可能会让用户困惑。
- *缓解：* 写入操作返回 `503 Service Unavailable`，并在响应中包含 `X-Aero-Status: degraded` 标头和 `Retry-After`。幂等性仍然有效，因此客户端可以安全地重试。
- *测试：* 为断路器打开 + 写入操作场景添加集成测试。

**低风险：Sprint 1（速率限制标头）。**
- *风险：* 对于未启用限流器的安装，添加标头会增加不必要的开销。
- *缓解：* 仅在 `cfg.RateLimit.Enabled` 时添加标头。添加一个字符串比较的开销可以忽略不计。

### 5.3 里程碑

- **里程碑 1（Sprint 1-2）：** 速率限制状态标头 + `X-Aero-Status` + 降级感知。至此，每个请求都会告诉客户端"你还有多少请求额度，以及我是否处于降级状态"。
- **里程碑 2（Sprint 3）：** 跨传输协议的错误码一致。gRPC 和 HTTP 都暴露相同的结构化错误码。SDK 可以生成类型化的错误。
- **里程碑 3（Sprint 4）：** 媒体管道。所有上传都经过内容处理：EXIF 剥离、MIME 验证、可选的病毒扫描。`Content-Disposition` 保护已就位。
- **里程碑 4（Sprint 5-6）：** 治理。错误码全部注册，`make check-error-codes` 处于绿色状态。限流器能感知幂等性。

### 5.4 架构演进过程

```
      当前状态                                       里程碑 1                                      里程碑 4
┌─────────────────────┐            ┌──────────────────────────────┐        ┌──────────────────────────────────────┐
│ handler → service   │   Sprint 1 │ handler → service            │ Sprint │ handler → service                      │
│       ↓             │   +------→ │    ↑ ↕ degradation.Monitor   │ 5-6    │    ↑ ↕ degradation.Monitor             │
│ repository/storage  │            │ repository/storage           │ +----→ │ repository/storage → pipeline          │
│       ↓             │            │ sso → breaker + stale-cache  │        │ sso → breaker + stale-cache            │
│ sso → breaker       │            │ ratelimit → X-RateLimit-*    │        │ ratelimit → X-RateLimit-* + idem-aware │
└─────────────────────┘            └──────────────────────────────┘        │ errors → registry + gRPC details       │
                                                                            └──────────────────────────────────────┘
```

主要观察结论：该代码库从一个扎实的、工程化的基础开始运行——分层、发件箱模式、断路器、幂等性、预设 URL。当前的设计阶段是一次"从功能完备到平台级"的演进。这 5 个方向都是*正确的方向*，其优先级排序（基于我审查代码后的调整建议）是合理且可行的。
