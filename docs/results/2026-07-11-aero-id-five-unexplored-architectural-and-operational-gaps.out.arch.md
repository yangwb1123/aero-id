Now I have a thorough understanding of the codebase. Here is my architectural analysis.

---

# 架构评审：aero-id

## 1. 架构评估

### 优势

**分层清晰，单向依赖严格。** `handler → service → repository` 的链路在 AGENTS.md 中有明确记录，并且两个传输层（HTTP 和 gRPC）确实共享同一个 service 接口——这是正确的决定，避免了业务逻辑在传输层之间分叉。

**领域事件的 Transactional Outbox。** 将通知、webhook 等内容放在与领域数据相同的数据库事务中发起的决策是可靠的。它会以一定的读取延迟和存储成本为代价，消除因下游系统暂时不可用而导致的数据丢失。

**依赖反转。** `NotificationDispatcher` 使用可选的函数式选项（`WithDispatcherBlockChecker`、`WithDispatcherPreferenceChecker`、`WithDispatcherRateLimiter`），因此测试中可以使用 noop，生产中使用真实实现，而无需修改调度器的核心逻辑。

**精心设计的可选功能机制。** 速率限制、存储 GC 和出站 webhook 都是可选加入的，因此开发/CI 环境不需要在配置中模拟外部依赖。每个可选机制都配有明确的日志记录，且在后备时能正常运行。

### 局限性 / 架构债务

| # | 问题 | 位置 | 影响 |
|---|---|---|---|
| 1 | **`app.go` 超过 500 行** | `internal/app/app.go` — 516 行 | 违反 AGENTS.md 中的硬性约束。组合根目录已成为一个单体结构，其中混合了基础设施连接、服务构建、路由注册和关闭流程。 |
| 2 | **新旧代码库并存** | `internal/service/` ← 旧；`internal/message/`、`internal/file/` 等 ← 新 | 事件字符串发散（旧包的 `newEvent` vs 新包的 `outbox.NewEvent`）。新包定义了重复的 `Repository` 接口（`internal/notification/notification.go` 中的 `type Repository interface`），而旧包则使用 `internal/repository`。这带来了架构漂移风险——编辑一个包可能不会影响另一个。 |
| 3 | **事件类型字符串没有共享常量** | `internal/service/message.go:105` 中的 `"message.sent"`，`internal/message/service.go:95` 中的 `"message.sent"`，`internal/service/notification.go` 中的 switch case | 事件类型既是生产者契约，又是消费者契约。一种类型字符串的拼写错误（例如，`"message.sent`" 与 `"message.send"`）在运行时是静默失败——在调度器的 `default` 分支中默默丢弃。 |
| 4 | **通知类型映射是字符串切换，不是基于常量的查找** | `internal/service/notification.go` — 第 262-298 行的 switch | 编译时无法检测到未映射的事件。`domain/notification.go` 中定义的常量（`NotifTypeMessageReceived` 等）没有被使用。 |
| 5 | **Payload 没有版本控制** | `outbox.Event.Payload` 是 `[]byte`，没有 `$version` 或 `$schema` 字段 | 由旧包和新包产生的 `"message.sent"` 事件可能包含不同的字段（新包可能添加 `"thread_id"`，而旧包则不添加）。没有版本字段的 Webhook 消费者无法检测到断裂的变化。 |
| 6 | **单实例速率限制，无操作感知** | `internal/pkg/ratelimit` — 每个请求消耗 1 个 token；`internal/middleware/ratelimit.go` — 不检查路径 | 一个执行大量写操作的请求与一个轻量级 GET 请求消耗相同的 token 预算。无法对写入进行不同的节流。另外，速率限制是内存中的——水平扩展无法共享状态。 |
| 7 | **无存储配额** | `domain.File` 没有保留字段；`service_crud.go` 不查询用户使用情况 | 对账单/存储变现不是一个限制（只能基于硬编码的 `maxBytes` 强制限制）。随着用户数量增长，一个用户可以占用无限存储空间。 |
| 8 | **无中央调度器框架** | `outbox.Relay.Start`、`gc.Sweeper.Start`、`limiter.RunCleaner`、`idemStore.RunCleaner` | 每个周期性任务都在其自己的包中实现自己的 ticker 循环。第四个周期性任务将导致代码重复（一次性启动、panic 恢复、超时和关机协调）。 |
| 9 | **运行时配置不可变** | `config.Load` 调用一次 `viper.ReadInConfig()`；没有后续的重新加载 | 更改速率限制参数、MIME 白名单或特性开关需要完全重启。对于运维事件（速率限制调整、功能回退）而言，这是次优的。 |

### 关键设计决策的合理性

**`uid` 字符串 vs. 用户表：** 正确。SSO 拥有身份证明；aero-id 对用户一无所知，只引用 `uid`。这使身份验证责任分明，并避免了密码/令牌管理。缺点是索引 `text` 列而非 `bigint` FK——在数百万用户规模下，这可能影响存储和索引性能。

**UUIDv7 主键：** 正确。时间有序、无冲突、可充当数据库端和 API 端的光标。无需单独的光标管理。

**事务性发件箱：** 对于可靠的消息传递而言是一个正确的模式，但代价是会为每个写入操作增加一次额外的数据库写入。如果不加注意，发件箱表和通知表都会出现异步落后。

---

## 2. 扩展方向

### 方向 A：事件类型注册表和 Payload Schema 契约（P0）

**为什么需要：**
没有共享常量，生产者-消费者耦合关系隐含在 switch 字符串中。添加新的事件类型需要触摸至少 3 个位置（生产者、发件箱通知映射、可能还有 webhook 过滤），且编译时没有任何保护措施。同时，旧包和新包产生的 `"message.sent"` 事件 payload 可能会在没有通知消费者的静默情况下分叉。

**核心挑战：**
- `outbox.Event` 结构体非常小且无版本控制；添加 schema 注册意味着要么迁移所有现有行，要么在读取端处理旧格式。
- 旧的 `newEvent` 和新的 `outbox.NewEvent` 使用不同的构造路径；对于发件箱事件应该只有一个来源。

**预期的架构变更：**
1. 将事件类型字符串提取到 `internal/domain/eventtype/`（或 `internal/outbox/eventtype.go`）中作为一个具有类型安全方法的 Go 常量块，而非裸字符串。
2. 向 `outbox.Event` 添加可选的 `SchemaVersion int` 字段（默认为 0 / 未设置；v1 为 1，以此类推）。
3. 将 `NotificationDispatcher.notificationFor` 中的 switch 替换为一个 `map[EventType]PayloadParser` 查找表。
4. 将 `newEvent` 嵌入到出站包中并删除所有重复的实现——只有一个 `outbox.NewEvent`。

**对现有系统的影响：**
低。在入口处添加 `SchemaVersion`，但读取端将其视为可选。将 switch 替换为注册表这是一项内部重构——API 不会改变。旧的事件在版本字段中将有一个零值，而新事件将有 v1——消费者可以根据需要逐步地按照零值来适应。

### 方向 B：操作感知速率限制 + 分布式协调（P0）

**为什么需要：**
每请求 1 个 token 意味着一个昂贵的写入操作（上传 50MB 文件）与一个轻量级列表请求消耗相同的预算。需要保护写入路径（消息发送、文件上传、评论创建），同时允许读取流量以更高速度通过。

**核心挑战：**
- 操作感知的权重需要在路由层知道操作类型（路径 + HTTP 方法或 gRPC 方法名称）。
- 处理每个 pod 本地的桶——如果扩展到 3 个副本，每个副本都有自己的桶，一个坚定的攻击者可以获得 3 倍于预期吞吐量。分布式状态需要 Redis/类似的组件。

**预期的架构变更：**
1. 定义 weight 接口：`type CostEstimator func(ctx, path, method) int`。
2. 扩展 `ratelimit.Limiter` 以接受 `Allow(key, cost int)`（当前签名接受 `Allow(key string)` 并固定消耗 1 个 token）。
3. 为分布式协调添加一个抽象的 `Backend` 接口——`local`（现有的一致哈希桶）和 `redis`（基于滑动窗口的排序集合/计数器），通过配置选择（可选加入，与出站或 S3 后端相同）。
4. 默认情况下保留内存后端（无需 Redis 依赖），并将 `redis` 作为大规模生产的可选后端。

**对现有系统的影响：**
中等。`Allow` 签名需要更改，但用法的数量有限（HTTP 中间件 + gRPC 拦截器）。在 `middleware.RateLimit` 中添加路径检查需要迁移到 `gin.RouteInfo` 查找，或者基于 URL 模式进行简单的字符串匹配。

### 方向 C：可组合的通知类型系统 + 插件式分发（P1）

**为什么需要：**
当前模型在 `NotificationDispatcher` 中硬编码了 3 种通知类型。添加第 4 种类型（例如，`"file.shared"`）需要修改调度器——违反了开闭原则。每个通知还需要不同的字段（有些需要 actor，有些不需要；有些需要 `parent_author_uid`，有些不需要）。

**核心挑战：**
- 将通知类型、生成和分发职责清晰地分离。目前，`notificationFor` 做了所有事情：解包、验证、提醒、策略应用。
- 折叠和速率限制是全局性的，而不是每个策略的。一个高频率的通知类型（“被点赞”）可能需要与一个关键的通知类型（“账号被封禁”）不同的限制。

**预期的架构变更：**
1. 定义 `Notifier` 接口：`type Notifier interface { Name() string; ShouldNotify(ctx, e *outbox.Event) (*NotificationSpec, bool); }`。
2. 将 switch 替换为 `map[string]Notifier`，其中注册在 `NewNotificationDispatcher` 之外完成。
3. 从 `NotificationDispatcher` 中提取速率限制——每个通知器可以携带自己的限流器，或者全局配置为每个通知类型设置不同的限制。
4. 使用 Go 1.26 的 `iter.Seq`（如果可用）或一个切片进行注册，以便同一个通知器可以注册多个类型。

**架构影响：**
中等偏低。`Notifier` 接口小而简洁。`NotificationDispatcher` 需要从硬编码的 switch 迁移到注册表，但策略检查（封禁、偏好、速率限制）保持不变。现有的 3 种类型变为 3 个通知器实例。

### 方向 D：每用户存储配额和用量跟踪（P1）

**为什么需要：**
没有上行限制和存储计量，系统就无法支持存储变现、套餐层级，甚至无法防止单一用户耗尽共享存储后端。当前，`File.Create` 检查 `Size > s.maxBytes`，但在所有者或用户维度上并没有总和检查。

**核心挑战：**
- 实现一个跨 Postgres 行高效的可扩展的 `storage_usage` 计数器。`SELECT SUM(size) FROM files WHERE owner_uid = $1 AND deleted_at IS NULL` 的成本随着文件数量的增加而增加——随着一个用户拥有 100 万个文件，扫描速度变慢。
- 并发插入存在竞态条件——两个同时上传可能都会检查 `usage + file1.size` 和 `usage + file2.size` 是否都在限额内，但这两者合起来会超过限额。

**预期的架构变更：**
1. 为每个（所有者、存储类型）创建一个带有单行的 `storage_usage` 表——包含 `total_bytes`、`file_count`。更新在 `File.Create` 和 `File.SoftDelete` 中通过 `UPDATE … RETURNING …` 以原子方式进行。
2. 在 `File.Create` 中增加一个检查：`if usage.total_bytes + file.Size > quota` → 拒绝。
3. 将配额（`max_bytes`）从服务级选项（`WithMaxUploadBytes`）转移到存储库的配置中——或者移动到数据库中以便支持套餐层级。
4. 处理内容去重（`FindByChecksum` 重用 blob 但不计入限额）——已去重的上传不应再次计费。

**对现有系统的影响：**
中等。对 `File.Create` 路径进行更改。需要一次迁移来填充 `storage_usage` 表中的现有数据。内容去重在 `FindByChecksum` 路径中已得到处理，因此如果文件重新使用现有的 blob，则不应增加限额。

### 方向 E：运行时配置子系统（P2）

**为什么需要：**
速率限制、MIME 白名单和特性开关在启动时固定。这意味着响应运维事件（例如，在攻击期间降低速率限制、为内容格式错误关停 MIME 类型）需要一次完整的部署循环。热加载将把 MTTR 从“CI → 部署 → 健康检查”的分钟级时间降低到亚秒级。

**核心挑战：**
- 哪些配置可以安全地热加载？数据库密码或签名密钥**不应该**——它们是静态的。速率限制参数、MIME 白名单、日志级别和安全地重新加载应该可以。
- 原子性：当配置部分更新时，您不希望一半的服务组件使用新值而另一半使用旧值。需要一个类似 `sync/atomic.Value` 的模式。

**预期的架构变更：**
1. 定义一个 `RuntimeConfig` 结构体，其中只包含支持热加载的字段（`RateLimit`、`AllowedMimeTypes`、`LogLevel`、特性开关），并通过 `atomic.Pointer[RuntimeConfig]` 公开。
2. 添加一个 `/v1/admin/config` 端点（PATCH 用于部分更新，GET 用于查看当前值）。
3. 向 `config.Load` 添加一个 `SIGHUP` 信号处理器或文件监视器（`fsnotify`），以便检测到热加载的配置而不需要 API 调用。
4. 让消费组件（`Limiter`、`fileService` 的 MIME 检查器、`Logger`）定期轮询或通过 channel 接收更新。

**对现有系统的影响：**
低。`RuntimeConfig` 是一个新结构。现有的 `config.Config` 保持不变。消费组件获得一个 `SetRuntimeConfig` 方法或定期快照函数。监听 `SIGHUP` 可以向后兼容。

---

## 3. 接口设计建议

### 原则

1. **在发件箱包内统一事件构造。** 应该只有一个 `NewEvent` 函数，并且所有生产者（消息、评论、文件共享）都使用它。旧包中的 `newEvent`（`internal/service/message.go`）应被删除，转而使用 `outbox.NewEvent`。对外暴露的签名应该是：
   ```go
   func NewEvent(aggregateType, aggregateID, eventType string, schemaVersion int, payload any) *Event
   ```

2. **将通知分发从字符串切换迁移到注册表。** `NotificationDispatcher` 不关心有哪些事件类型存在——它遍历一个已注册的通知策略列表。这类似于策略模式：
   ```go
   type Dispatcher interface {
       Name() string
       NotificationFor(e *outbox.Event) (*domain.Notification, bool)
   }
   ```

3. **引入操作权重的概念。** 速率限制应该接受一个成本参数——不是每个请求固定消耗 1。中间件从路径/方法或 gRPC 完整方法中得出成本：
   ```go
   type CostEstimator interface {
       Cost(ctx context.Context, path string, method string) int
   }
   ```

4. **存储配额应使用原子更新而非 SELECT SUM。** 一个 `storage_usage` 行——每个用户一行——通过 `UPDATE … SET total_bytes = total_bytes + $1 WHERE … AND total_bytes + $1 <= quota RETURNING …` 进行更新。这可以通过单次原子 SQL 语句解决并发竞态问题。

### 新抽象层的需求

| 层 | 用途 | 会包含在内的包 | 对现有代码的影响 |
|---|---|---|---|
| `eventtype` | 事件类型常量 + payload 版本注册表 | `internal/domain/eventtype/` | 低——主要是一个提取重构 |
| `notify` | 通知分发注册表 | `internal/notify/` | 中等——从 `service` 中提取当前调度器 |
| `scheduler` | 集中式周期性任务 | `internal/pkg/scheduler/` | 低——将 ticker 样板提取到一个包中 |
| `quotas` | 存储+配额计量 | `internal/quotas/` | 中等——新的表 + 服务逻辑 |

不需要引入运行时配置层——使用 `atomic.Pointer` 和一个从 `config.Load` 中提取的简单 `RuntimeConfig` 结构体。

### 向后兼容性

- **Payload 版本控制**：新字段 `SchemaVersion`，零值意味着“旧格式”。调度器检查版本 0 使用旧的解析逻辑，版本 1 使用新的逻辑。旧事件保持不变。
- **通知分发**：将 switch 语句引入注册表需要一个适配器；初始实现应保留现有行为。
- **速率限制**：需要将 `Allow(key string)` 更改为 `Allow(key string, cost int)`——这是一个破坏性更改，但影响范围较小。用新的签名替换旧签名，用 `cost = 1` 更新中间件，然后引入路径感知的成本估算。
- **配置**：热加载字段应作为可选字段添加到配置文件中。没有它们的配置继续使用启动时的值。

---

## 4. 技术选型

### 新依赖

| 需求 | 推荐方案 | 理由 |
|---|---|---|
| 分布式速率限制 | **自己构建，使用 Lightweight Redis（可选）** | aero-id 有自己基于 token bucket 的速率限制；添加 Redis 作为可选后端是分层架构的自然演进，不需要外部框架。当 Redis 没有配置时，内存后端保持正常运行。 |
| 配置热加载 | **自己构建，使用 `atomic.Pointer` + `fsnotify`（可选）+ `SIGHUP`** | 需要修改的字段很少（速率限制参数、MIME 白名单、日志级别）。一个通用的热重载框架会过度设计。通过在需要精细控制时监听信号并定期轮询，使用双路径实现。 |
| 配置模式验证 | **保留 `viper` + 自定义验证器** | aero-id 已经使用了 Viper。当有运行时配置时，添加一个验证器层用于动态更新——区分“此字段可以热加载”和“此字段需要重启”。 |
| 指标/可观测性 | **已有（`internal/pkg/metrics` + 可选的 OTLP 跟踪）** | 已经足够好。不需要 Prometheus 客户端库——只需使用现有的注册表。 |
| 任务调度 | **自己构建，使用简单的 `internal/pkg/scheduler`** | 当前有三种 ticker 循环模式（出站 relay、GC 清理器、速率限制清理器、幂等性清理器）。它们共享相同的结构：`Start(interval) → stop()`。提取这个功能不需要依赖库。 |

### 自建 vs. 采购

**事件类型注册表 → 自建。** 没有现成的 Go 库能解决“在一系列具有版本化 payload 的字符串常量上映射到通知策略”这一特定问题。这完全是业务逻辑，其重构成本低廉（一个带 `map[string]PayloadParser` 的文件）且代码库特定。

**分布式速率限制 → 自建，Redis 作为后端。** 市场上存在 API 网关解决方案（Kong、Envoy），但 aero-id 是一个有自己传输层的后端服务——引入 AP 网关来处理速率限制是一种架构上的越级。更好的做法是让服务自身处理，同时使用 Redis 进行协调。

**事件 schema 注册表 → 如果超出 20 种事件类型则引入 Confluent Schema Registry 或类似方案。** 但是，aero-id 目前只有 3 种事件类型。一个简单的版本字段加上每个版本的 Go 结构体就足够了，直到事件类型增长到 20 种或更多。

### 第三方依赖评估标准

对于每个新依赖，要求确保：
1. **最小化传递依赖**——首选零依赖或仅标准库。
2. **Go 1.26 兼容**——必须在最新的 Go 工具链下编译。
3. **许可证兼容**——MIT、Apache 2.0、BSD。没有 AGPL。
4. **没有 cgo**——容器化部署需要在没有 C 编译器的情况下进行静态编译。

---

## 5. 实施路线图

### 优先级

| 优先级 | 方向 | 论证 |
|---|---|---|
| **P0** | A — 事件类型注册表 + Payload Schema | 基础设施——安全地添加新事件类型需要此功能。按照当前代码路径执行到第 5 种事件类型，风险很高。 |
| **P0** | B — 操作感知的速率限制 | 成本影响——根据使用模式，对于服务于 1K+ 个并发用户的系统，这是第一道防线。该实施对于 aero-id 的启动时配置原则是兼容的（可选加入，默认为全接受）。 |
| **P1** | C — 可组合的通知类型系统 | 产品能力——为通知迭代打开了可能性（频道、推送、聚合），同时不影响当前行为。 |
| **P1** | D — 存储配额 | 产品能力——实现存储变现、套餐层级和用户限制。虽然是一个 P1 产品功能，但在存储成本成为问题之前，它应该在第 1 批付费用户落地。 |
| **P2** | E — 运行时配置 | 运维效率——在不部署的情况下调整速率限制或 MIME 白名单功能非常有用，但对于系统运行并非必不可少。 |

### 阶段划分

**阶段 1（第 1-2 周）——事件基础 + 清理**

- 将 `newEvent`（旧包）统一为 `outbox.NewEvent`（新包）。删除旧代码路径。
- 将事件类型字符串提取到 `internal/domain/eventtype/types.go` 中作为导出的常量。
- 向 `outbox.Event` 添加 `SchemaVersion int`（代码迁移后，默认 0，后续设为 1）。
- 将 `NotificationDispatcher.notificationFor` 中的 switch 语句替换为一个 `map[string]notificationBuilder`。
- 将 `app.go` 拆分到 500 行以下（例如，将基础设施构建提取到 `app_storage.go`、`app_outbox.go`、`app_services.go` 中）。
- **里程碑：** `make test && make lint` 全部通过。没有外部 API 变化。删除旧的 `newEvent` 代码路径。

**阶段 2（第 3-4 周）——速率限制 + 中央调度程序**

- 将 `ratelimit.Limiter.Allow(key string)`（当前）更改为 `Allow(key string, cost int)`。用 `cost=1` 更新中间件。
- 引入 `CostEstimator` 接口——默认实现根据路径/方法返回成本（写 = 5，读 = 1）。
- 实现 `internal/pkg/scheduler`：`type Job func(ctx context.Context) error`，将其与 `Start(interval, jitter, timeout)` 和 `Stop()` 一起包装。将 `outbox.Relay.Start`、`gc.Sweeper.Start`、`limiter.RunCleaner` 和 `idemStore.RunCleaner` 迁移到它上面。
- **里程碑：** 可以基于操作权重对写入进行不同的节流。所有 4 个 ticker 循环共享一个共同的基础设施。

**阶段 3（第 5-6 周）——通知可扩展性**

- 定义 `notify.Notifier` 接口。为当前的 3 种类型（`MessageSentNotifier`、`MessageReadNotifier`、`CommentRepliedNotifier`）提取 3 个实现。
- 将通知速率限制从全局更改为每个通知器（或每个类型配置）。
- 添加 helper 函数用于跨通知器通用的折叠和封禁检查。
- **里程碑：** 添加第 4 种通知类型只需要一个 30 行的新 `Notifier` 结构体，而不需要修改调度器。

**阶段 4（第 7-8 周）——存储配额**

- 创建 `storage_usage` 表。填充现有用户数据的迁移。
- 在 `fileService.Create` 中添加配额检查（在写 blob 之后、在提交行之前）。
- 处理内容去重（`FindByChecksum` 路径不应增加限额）。
- 通过配置支持配额覆盖（服务级默认值，每个用户的最大值在数据库层）。
- **里程碑：** 超出配额的 Upload 会返回一个 `429` 配额错误。现有数据会正确迁移。

**阶段 5（第 9-10 周）——运行时配置**

- 定义 `RuntimeConfig` 结构体，包含 `RateLimitEnabled`、`RateLimitRPS`、`RateLimitBurst`、`AllowedMimeTypes`、`LogLevel`。
- 通过 `atomic.Pointer` 暴露，并添加一个 `PATCH /v1/admin/config` 端点（需要 `perm_system_config`）。
- 添加可选的 `SIGHUP` 重新加载。
- 更新消费组件以从运行时快照轮询。
- **里程碑：** 运维人员可以在不重启的情况下调整速率限制和 MIME 白名单。

### 风险和缓解策略

| 风险 | 可能性 | 影响 | 缓解措施 |
|---|---|---|---|
| **阶段 1 中事件字符串不匹配导致通知静默丢弃** | 低 | 高 | 添加一个指标：`outbox_events_unhandled_total`。如果任何事件类型在调度器中没有映射，将其记录下来。在阶段 0 的生产监控规则中捕捉。 |
| **阶段 2 中速率限制向分布式迁移导致回归** | 中 | 中 | 对更改进行功能标记。默认情况下，Redis 后端是禁用的。首先在非生产环境进行测试，分阶段部署。 |
| **阶段 4 中并发上传在配额检查中产生竞态条件** | 中 | 高 | 使用原子 `UPDATE … RETURNING`，而不是读取-检查-写入模式。在存储库层测试确认正确的行为。 |
| **阶段 3 中通知器注册表破坏了向后兼容性** | 低 | 高 | 现有的 3 种类型以精确复制当前行为的方式进行包装。回归套件（集成测试）涵盖所有 3 种路径。 |
| **阶段 5 中运行时配置的竞态条件** | 低 | 中 | 使用 `atomic.Pointer` 进行快照——消费者在没有任何锁定握手的情况下获取整个结构体。这是 Go 标准库推荐的模式。 |

### 总体时间线

```
Week 1-2  │ 阶段 1 ─── 事件统一 + schema 版本控制 + app.go 拆分
Week 3-4  │ 阶段 2 ─── 操作感知的速率限制 + 调度程序框架
Week 5-6  │ 阶段 3 ─── 通知器注册表 + 可扩展的通知类型
Week 7-8  │ 阶段 4 ─── 存储配额 + 用量跟踪
Week 9-10 │ 阶段 5 ─── 运行时配置 + 管理 API
          │
          │ 整个过程中持续：make test && make lint
          │ 每阶段结束后进行集成测试运行
```

阶段 1 和 2（P0）是基础设施安全的必备前提——事件分歧和操作不可知速率限制是在用户增长之前必须解决的真实风险。阶段 3 和 4（P1）是对业务能力的解锁，但不会中断现有功能。阶段 5（P2）是运维效率提升，可以安全地推迟，直到人们亲身感受到重启的痛点。

### 留给读者的最终想法

该分析报告确认了原始评估中的所有 5 个发现。我观察到的最大的架构风险**并非**验证中列出的任何一个独立项目，而是**新旧代码库布局之间正在进行但尚未完成的状态迁移**。`internal/service/`（旧）和 `internal/message/`、`internal/file/` 等（新）之间的重叠意味着任何修复或功能都必须两处都应用，或者冒着让它们分叉的风险。我建议在开始任何其他方向之前，**将阶段 1 的范围扩大到包括在剩余的旧包之上完成迁移**。AGENTS.md 将 `internal/service/` 列为规范位置——如果新包是要取代它，那么完成迁移，并更新 AGENTS.md 来反映实际的布局。
