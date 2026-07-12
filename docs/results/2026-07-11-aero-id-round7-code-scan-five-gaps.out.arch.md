# 架构评审分析：aero-id

## 1. 架构评估

### 当前架构的优势

**清晰的分层结构**是该系统最突出的优势。`handler → service → repository` 的单向依赖链严格遵循了经典的六边形架构风格，带来了几个关键收益：

- **传输层可互换性**：HTTP 和 gRPC 共享同一 `service` 层，验证了"传输层是细节"的原则。代码证据表明两个传输层对同一业务逻辑的行为一致，这是架构约束被严格执行的体现。
- **测试关注点分离**：service 层的 mock 化测试、repository 层的 testcontainers 集成测试，分别对应不同粒度的验证需求。方向一的验证过程也反向证明了——当有人质疑传输层缺失时，逐层追踪就能快速定位问题所在。
- **软删除 + 审计日志作为横切关注点**：在 repository 层统一做软删除、在 service 层统一审计，避免了业务代码中散落这些逻辑。

**有限的状态空间**是另一个隐含优势：

- 固定的通知类型枚举（8 种而非无限可扩展的设计）、严格的两层评论嵌套、Binary 的权限模型（有/无），这些约束降低了系统的认知负荷。对于 aero-id 这种定位为"SSO 下游的数据服务"的中间件，这种约束是合理的。

### 架构局限性

**事件驱动模式的一致性断裂**是最核心的架构问题。

代码库中同时存在两种不同的"副作用处理"范式：

| 范式 | 使用位置 | 特征 |
|------|---------|------|
| **Outbox 事件** | `message.Service.Send()` → `outbox.NewEvent` | 可靠异步、至少一次送达 |
| **直接调用** | `file.Service.ShareFile()` → `audit.Log` | 同步、无通知、无重试 |

这是一个**架构分裂**——当消息模块已经证明 outbox 模式的可行性后，文件模块选择了不同的路径。这导致了方向三和方向五指出的所有问题。这不是简单的"遗漏实现"，而是**两个模块对同一架构决策的不一致执行**。

**上下文治理的"空心化"**是第二个系统性缺陷。

`context.Background()` 出现在 `relay.go`、`sweeper.go`、drain 逻辑中，表明这些 goroutine 被设计为"脱离请求生命周期运行的长驻进程"，但却没有建立自己的生命周期管理契约。具体来说：

- 没有统一的 `lifecycle` 接口（Start/Stop 的契约是隐式的，通过 `app.go` 的硬编码编排）
- 没有 graceful shutdown 的超时层级（`context.Background()` 意味着"永远等待"，这在 shutdown 序列中是危险的）
- 没有 goroutine 的健康检查和 panic 恢复（如果某个 relay goroutine panic，整个进程可能挂掉）

**死代码包**（`internal/search/` 和 `internal/notification/`）是架构债务的典型信号。17 个文件无人引用，说明发生过一次"原地重写"而非"演进式重构"——旧实现被废弃后未清理，新实现放在了不同的包路径下。这带来了两个风险：

1. **误 import 风险**：如果新开发者不熟悉历史，可能 import 旧的 search 包而不是 service 层的实现
2. **认知负载**：新开发者需要花时间理解"为什么有两个 search 包"

### 关键设计决策评估

| 决策 | 评价 | 理由 |
|------|------|------|
| UUIDv7 作为 PK + 游标分页 | ✅ 正确 | 时间有序性天然支持游标，无需 extra sort key |
| 外置 SSO 管理身份 | ✅ 正确 | 职责分离清晰，避免了 aero-id 膨胀为身份平台 |
| 双传输层共享 service | ✅ 正确 | 已验证有效（方向一的反证） |
| 软删除全局统一 | ✅ 正确 | 一致的数据恢复能力 |
| Outbox 模式 | ⚠️ 正确但实施不完整 | 模式本身正确，但覆盖率不足 |
| 非 auto-migrate | ⚠️ 可接受 | 生产环境应控制 schema 变更，但开发体验不佳 |
| 后台 goroutine 无统一治理 | ❌ 有缺陷 | 缺乏 context 传播、shutdown 超时、panic 恢复 |
| 通知类型硬编码枚举 | ⚠️ 当前可接受 | 如果未来通知类型超过 20 种，应考虑可扩展设计 |

### 架构债务清单（按严重程度排列）

1. **P0**：事件通知覆盖率不一致（outbox 模式在文件分享域的缺失）——架构范式未严格执行
2. **P0**：后台 goroutine 生命周期管理缺失——生产稳定性风险
3. **P1**：死代码包——代码库卫生问题，有实际的误 import 风险
4. **P2**：shutdown 时序断裂——最终 drain 可能与 HTTP shutdown 并发执行

---

## 2. 扩展方向

### 方向 A：统一事件总线 —— 将 Outbox 模式提升为架构规范（P0，最高优先级）

**为什么需要**：

当前 outbox 模式仅在 `message` 域使用，但其解决的问题是**全系统性**的——任何改变共享状态的操作都可能触发下游关注方需要被通知。文件分享通知、权限变更通知、数据更新通知，这些是同一类问题的不同实例。只要存在一个统一的 outbox 基础设施，新业务模块只需注册事件类型，不需要重复实现异步通知的可靠性保障。

从业务价值看，方向三和方向五指出的文件分享通知缺失，是用户可直接感知的差距——用户分享文件后接收方不知情，这是协作场景的体验断裂。

**核心挑战**：

1. **事务边界问题**：当前 `service_share.go` 在单个事务中执行 `Grant` + `audit.Log`，但 outbox 事件写入需要保证"业务操作成功则事件必达"。最可靠的方案是**事务性 outbox**——将事件写入与业务操作放在同一个数据库事务中，由独立的 relay 进程读取并投递。这需要重构 `Grant` 操作的持久化流程。

2. **事件 Schema 治理**：当前通知类型有 8 种枚举，但没有定义事件 payload 的结构化 schema。如果 outbox 被提升为系统级基础设施，需要为每个事件类型定义清晰的结构和版本策略，否则下游消费者（通知服务、搜索索引等）将难以演进。

3. **幂等性保障**：relay 的"至少一次"语义需要消费者侧幂等。当前 `message.sent` 事件消费后产生 `NotifTypeMessageReceived` 通知，但幂等性保障是否完备需要审计。

**预期的架构变更**：

```
当前:                         目标:
service/file.go               service/file.go
  ├→ repository.Grant()         ├→ repository.Grant()
  └→ audit.Log()                 └→ outbox.Publish(event)  ← 事务内
                                     
                                internal/outbox/
                                  ├→ publisher.go    (事务性写入接口)
                                  ├→ relay.go        (增强：支持多种消费端)
                                  └→ registry.go     (事件类型注册表)
```

核心变化：`outbox.Publish()` 不应再是 `service` 层的主动调用，而应变为 repository 层的事务回调——类似领域事件的发布机制：

```go
// 概念接口，非代码实现
type OutboxHook func(ctx context.Context, tx *gorm.DB) OutboxWriter

// repository 在事务提交前调用 hook
repo.Grant(ctx, grant, OutboxHook(func(tx) OutboxWriter {
    return tx.Create(&OutboxEvent{Topic: "file.shared", Payload: ...})
}))
```

**对现有系统的影响**：

- `internal/outbox/` 需要重构以支持泛化的事件发布，当前可能仅针对 `message` 域硬编码
- 需要引入事件注册机制（可以是简单的 map，也可以引入事件 schema registry）
- 需要为文件分享事件定义 `file.shared` 和 `file.unshared` 两个新事件类型
- 消费端需要新增 handler 来处理这两个事件并生成通知

| 方案 | 优点 | 缺点 |
|------|------|------|
| **方案 A1**：事务性 outbox（推荐） | 最强一致性保证 | 需要重构 repository 层，增加事务回调机制 |
| **方案 A2**：Saga/2PC | 适合跨服务事务 | 对单体过重，引入分布式事务复杂度 |
| **方案 A3**：异步双写（先操作再发事件） | 改动最小 | 缺少一致性保证，操作成功但事件丢失的风险 |

**推荐方案**：方案 A1，因为 aero-id 是单体服务（数据库层面），事务性 outbox 是最成熟可靠的模式。但需要谨慎设计 `OutboxHook` 的 API，避免将 GORM 的 `*gorm.DB` 泄露到 service 层。

---

### 方向 B：后台进程生命周期治理框架（P0-并列，同时进行）

**为什么需要**：

方向四指出的 `context.Background()` 和 shutdown 时序问题，在系统扩容时会被放大。如果未来需要引入更多后台 goroutine（如 TTL 清理、数据归档、缓存预热、事件重试等），当前的 `app.go` 硬编码编排方式会迅速膨胀为不可维护的 spaghetti。

从工程价值看，这是系统的**可靠性基础设施**——当前虽然运行正常，但任何后台 goroutine 的 panic 或 hang 都会导致服务降级甚至进程挂掉。

**核心挑战**：

1. **编排 vs 注册**：当前是 `app.go` 中显式调用 `startA()`, `startB()`, `stopA()`, `stopB()`。更可维护的方式是引入 `Lifecycle` 接口，让各组件自注册。

2. **超时层级**：shutdown 需要三级超时——① 单个组件的 graceful 超时（如 drain 等待 5s）→ ② 组件组的超时（所有 outbox relay 共 10s）→ ③ 总 shutdown 超时（30s）。当前只有总超时，且 final drain 使用 `context.Background()` 绕过了它。

3. **健康检查集成**：后台进程的健康状态应暴露给 `/healthz` 和 `/readyz`，以便 K8s 探针能检测到 relay 或 sweeper 的异常。

**预期的架构变更**：

```go
// 引入 lifecycle 接口
type Service interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Name() string
}

// app.go 改为注册式
type App struct {
    services []Service
}

func (a *App) Register(svc Service) {
    a.services = append(a.services, svc)
}

func (a *App) shutdown() {
    // reverse-order shutdown with hierarchical timeouts
    for i := len(a.services) - 1; i >= 0; i-- {
        svc := a.services[i]
        shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        svc.Stop(shCtx)
        cancel()
    }
}
```

**对现有系统的影响**：

- 需要重构 `relay.go`、`sweeper.go`、rate limit cleaner、idempotency cleaner 以适应 `Service` 接口
- `app.go` 的 `startXxx`/`stopXxx` 方法可以简化为 `Register` 调用
- 需要为 `/healthz` 增加对后台进程的状态报告
- 这是纯内部重构，对 API 消费者无影响

| 方案 | 优点 | 缺点 |
|------|------|------|
| **方案 B1**：标准 Lifecycle 接口（推荐） | 简单、Go 惯用、易测试 | 需要逐个组件适配 |
| **方案 B2**：errgroup + signal.Notify | Go 标准库方案，适合扇出 | 难以支持分阶段 shutdown |
| **方案 B3**：引入 fx/uber-go 框架 | 自动依赖注入 + 生命周期 | 引入第三方依赖，学习成本 |

**推荐方案**：方案 B1，与 Go 生态的 `io.Closer` 模式一脉相承，且不引入额外依赖。可以与方向 A 同步进行，因为 outbox 重构后的 relay 组件正好适配新的 `Service` 接口。

---

### 方向 C：事件驱动架构的消费端规范化（P1，在 A 之后）

**为什么需要**：

方向三的修正指出 `notificationFor` 已处理 7 种事件类型，但通知的生成逻辑散布在 `notification.go` 的多个 `if` 分支中。随着事件类型继续增加（如 `file.shared`、`file.unshared`），这种集中式 `switch/if-else` 会变成一个巨型函数，违反 cyclomatic complexity ≤ 10 的约束。

从架构角度看，outbox 是事件的生产端，notification 是消费端。规范消费端的 handler 注册机制，与方向 A 的生产端规范化形成**完整的事件驱动闭环**。

**核心挑战**：

1. **事件到通知的映射**：当前 `notificationFor` 处理的是一个事件可能产生 0 到 N 个通知的场景（如 `message.sent` 需要通知接收方）。这不是简单的 1:1 映射。

2. **通知聚合**：可能需要在消费端做通知聚合（如"王五 在 3 个文件中提到了你"合并为一条通知），这需要事件的窗口化处理。

3. **通知渠道**：当前似乎只有 "系统内通知"模式，未来可能需要支持推送、邮件等渠道。Handler 的返回类型需要预留扩展性。

**预期的架构变更**：

```go
// handler 注册模式
type EventHandler func(ctx context.Context, event OutboxEvent) ([]Notification, error)

type NotificationRouter struct {
    handlers map[string]EventHandler  // topic → handler
}

// 每个域自注册 handler
func init() {
    DefaultRouter.Register("file.shared", HandleFileShared)
    DefaultRouter.Register("message.sent", HandleMessageSent)
}
```

**对现有系统的影响**：

- `notificationFor` 的逻辑需要拆分为每个事件类型独立的 handler
- 需要引入 `EventHandler` 的注册机制
- 需要明确一个事件产生多个通知时的原子性（全成功或全回滚？）
- 对 testability 是正向影响——每个 handler 可以独立测试

---

### 方向 D：存储抽象层增强 —— 内容寻址 + 事件溯源（P2，中期方向）

**为什么需要**：

当前 `FileStorage` 接口支持 `local` 和 `s3` 两种实现，但这个抽象层级仅覆盖了文件的存/取。文件分享的场景暴露出一个问题——**分享的语义和存储的语义是正交的**。分享不改变文件内容，只改变访问权限。如果未来需要支持文件版本、文件锁定、或者对分享的文件做访问日志，当前的抽象需要增强。

此外，方向三/五中文件变更的通知问题，如果引入**文件事件流**（文件创建、更新、删除均产生事件），可以从根源上解决。

**核心挑战**：

1. **内容寻址**：如果引入内容寻址存储（基于 SHA256 定位文件内容），可以消除重复存储但需要处理引用计数——多个用户分享同一文件不应存储多份。

2. **事件溯源文件元数据**：将文件元数据的变更建模为事件流（创建→更新→删除），可以自然地驱动通知、审计、搜索索引。但会增加系统复杂度。

**对现有系统的影响**：

- 需要评估是否真有必要——当前文件存储模式是否遇到实际瓶颈
- 内容寻址会改变文件删除的语义（删除一个引用不影响其他引用）
- 事件溯源是较大的架构变更，建议等到 outbox 基础设施成熟后再考虑

**推荐**：暂缓此方向，标记为 **P2 候选**。仅在业务需求明确（如重复文件检测、版本历史）时启动。

---

### 方向 E：权限模型的泛化 —— 超越简单的 Grant/Revoke（P2，远期方向）

**为什么需要**：

当前权限模型是 Binary 的——要么有权限要么没有。对于消息和文件分享，这可能足够。但通知场景暗示了一个更精细的权限粒度：用户可能希望控制"谁可以给我发文件分享通知"或者"我只接收某些文件的变更通知"。

从架构角度看，当前的 `Grant`/`Revoke` 操作直接调用 repository，没有经过权限策略引擎。如果未来引入更复杂的权限规则（如基于角色的访问控制、时间窗口、速率限制），需要更灵活的架构。

**核心挑战**：

1. **策略引擎**：是否需要引入策略引擎（如 OPA/Rego）还是保持硬编码？对于 aero-id 的定位（SSO 下游的数据服务），硬编码可能更合适。

2. **与 SSO 的权限集成**：SSO 已经管理角色，aero-id 管理资源级权限。两者如何交互？方向一显示 Admin 角色管理 API 已实现，说明角色来自 SSO，但资源级权限（谁可以访问哪个文件）在 aero-id 维护。

**推荐**：**P2 候选项**。在当前场景下，Binary 权限模型加上 UUIDv7 级的资源隔离已经满足需求。只有当出现"只读分享"、"限时分享"等需求时才需要推进此方向。

---

## 3. 接口设计建议

### 关键接口设计原则

**原则一：事务边界在 repository 层，不要在 service 层手搓事务**

当前 `service_share.go` 的逻辑是：

```
grant → audit.Log
```

这两个操作在同一事务中吗？答案是**不确定的**——因为事务管理在 repository，`audit.Log` 可能在自己的事务中。对于 outbox 事件来说，这会导致"grant 成功但事件丢失"的情况。

建议：Repository 层应提供允许在同一次数据库往返中执行"业务操作 + outbox 事件写入"的接口模式：

```
repository.Grant(ctx, grant, outbox.Event{...}) → 同一事务内写 grant 表 + outbox 表
```

这可以通过接口设计来实现——不向 service 层暴露 `*gorm.DB`，但允许传入"事务内回调"：

```go
// repository 接口
type GrantRepository interface {
    Grant(ctx context.Context, grant *domain.Grant, events ...*domain.OutboxEvent) error
    Revoke(ctx context.Context, owner, target, resource string) error
}
```

**原则二：减少 if-else 丛林，用注册表替代**

当前 `notificationFor` 的多个 `if/switch` 分支是明显的 bad smell。引入 `EventHandler` 注册表后，添加新事件类型不需要修改核心逻辑，只需新增一个文件：

```go
// 新增 file.go 中的事件处理
func init() {
    registry.Register("file.shared", handleFileShared)
    registry.Register("file.unshared", handleFileUnshared)
}
```

**原则三：后台进程的契约应显式化**

所有后台 goroutine 应实现统一的接口：

```go
type Daemon interface {
    Run(ctx context.Context) error  // ctx 用于 shutdown 信号
    Name() string
    Health() HealthStatus
}
```

而不是当前隐式的 `Start()` / `stop()` 配对。

### 是否需要新的抽象层

**需要引入的抽象层**：

| 抽象层 | 必要性 | 优先级 |
|--------|--------|--------|
| `Daemon` 生命周期接口 | 高——解决方向四 | P0 |
| 事件注册表 | 高——解决方向三/五一致性问题 | P1 |
| 事务性 outbox 接口 | 高——方向 A 的核心 | P0 |
| 通知创建策略（何时静默、何时通知） | 中——但当前可放在单个 handler 中 | P2 |

**不需要引入的抽象层**：

- **工作流引擎**（如 Temporal/Cadence）——对 aero-id 过重
- **事件 Schema Registry**（如 Confluent Schema Registry）——当前通知类型固定，不需要运行时 schema 验证
- **独立的通知服务**——aero-id 是单体服务，通知逻辑放在消费端 handler 中即可

### 向后兼容性

方向 A（统一事件总线）和方向 B（生命周期治理）都是**内部重构**，对 API 消费者**完全透明**。不需要迁移周期。

死代码删除（方向二）也是安全操作——只要确认确实无引用，删除后不影响任何功能。

潜在的风险点只有：如果引入 `EventHandler` 注册表（方向 C），需要确保现有 `notificationFor` 的 7 种事件类型在新旧系统下的行为完全一致。建议采用**并行运行 + diff 验证**的策略——新老 handler 同时运行一段时间，对比输出是否一致。

---

## 4. 技术选型

### 是否需要引入新的技术栈

**结论：不需要引入新的外部依赖。** 四个有效方向都可以在当前技术栈（Go 标准库 + GORM + Postgres + MinIO/s3）内解决。

具体分析：

| 方向 | 是否需要新依赖 | 理由 |
|------|---------------|------|
| 方向 A（事件总线） | 不需要 | 事务性 outbox 在 Postgres 级别实现，Go 标准库的 `database/sql` + GORM 即可 |
| 方向 B（生命周期治理） | 不需要 | Go 标准库的 `context` + `sync` + `signal` 完全够用 |
| 方向二（死代码删除） | 不需要 | 纯删除操作 |
| 方向 C（消费端规范化） | 不需要 | 纯 Go 接口设计 |

这实际上是一个积极的信号——说明现有的技术栈选择是正确的，只是架构实施的一致性不足。

### 第三方依赖的评估标准

如果未来需要引入新依赖，建议遵循以下评估框架：

```
标准                              权重    评估项
──────────────────────────────────────────────────
1. 与 Go 标准库的互补性           高     是否填补标准库空白，而非包装标准库
2. 依赖注入深度                   高     是浅层依赖（一个文件可替换）还是深层依赖（全局耦合）
3. API 稳定性                     高     v1.x 版本、Go 1 兼容承诺、无 breaking change 历史
4. 测试友好性                     中     是否提供 interfaces 而非 concrete types
5. 社区活跃度                     中     GitHub Stars > 1k, 最近 6 个月有 release
6. 与现有依赖的兼容性             中     不引入与 GORM/grpc/protobuf 版本冲突的传递依赖
7. 维护成本                       低     是否需要专人维护、升级频率是否合理
```

以这个标准看，`uber-go/fx`（依赖注入 + 生命周期管理）在标准 1 上存疑——Go 标准库提供了 `errgroup`、`signal.Notify`、`sync.WaitGroup`，已经能够实现一个可维护的生命周期管理，引入 fx 的理由不够充分。

### 自建 vs 采购/引入的决策依据

对于本次提出的架构改进，所有方向都是**自建**方案。因为：

1. aero-id 的架构需求（事务性 outbox、后台进程管理、事件 handler 注册）都是 Go 生态中**模式层面的问题**，而非能力层面的问题。Go 标准库和 Postgres 已提供足够的能力，缺失的是将这些能力组织为一致架构模式的纪律。

2. 引入外部依赖来解决"架构不一致"问题，本质上是**用框架的约束来弥补团队的架构纪律**。这在某些场景下是合理的（如团队有大量新人），但成本也很高——框架的惯例可能与现有代码冲突，迁移成本可能抵消收益。

3. 如果未来需要跨服务的异步通信，才需要引入消息队列（如 NATS、Kafka）。但这是**跨服务通信需求**驱动的，不是当前架构改进的需要。

---

## 5. 实施路线图

### 优先级排序

```
P0（立即执行）：
  ├─ 方向二 死代码删除            [1-2 天，低风险]
  ├─ 方向 A 核心：事务性 outbox    [1-2 周，中风险]
  └─ 方向 B 生命周期治理           [1 周，中风险]

P1（下一阶段）：
  ├─ 方向 A 扩展：文件分享事件      [1 周，低风险]
  └─ 方向 C 消费端 handler 规范化   [1 周，低风险]

P2（远期）：
  ├─ 方向 D 存储抽象增强            [2 周+，高风险]
  └─ 方向 E 权限模型泛化            [按需评估]
```

### 阶段划分

**Phase 1：清理与基础设施重构（2-3 周）**

| Step | 任务 | 产出 | 依赖 |
|------|------|------|------|
| 1.1 | 删除 `internal/search/` 和 `internal/notification/` 死代码 | PR + verify `make check-file-sizes && make lint && make test` | 无 |
| 1.2 | 定义 `Daemon` 接口 + 重构 `relay.go`、`sweeper.go`、rate limit/idempotency cleaner | `internal/app/lifecycle.go` + 所有后台进程适配 | 无 |
| 1.3 | 重构 `app.go` 使用注册式启动/停止 | `internal/app/app.go` 重构 | 1.2 |
| 1.4 | 定义事务性 outbox 接口 + repository 回调模式 | `internal/outbox/publisher.go` | 1.2（需 relay 先稳定） |
| 1.5 | 将 `message.sent` 等现有事件通过新 outbox 接口发布 | 验证新老路径行为一致 | 1.4 |
| 1.6 | 修复 shutdown 时序：确保 final drain 在 HTTP/gRPC 停止前完成 | `app.go shutdown()` 修复 | 1.2 |

**Phase 2：文件分享事件通知（2 周）**

| Step | 任务 | 产出 | 依赖 |
|------|------|------|------|
| 2.1 | 定义 `file.shared` / `file.unshared` / `file.updated` / `file.deleted` 事件类型 | `internal/domain/events.go` | Phase 1 |
| 2.2 | 在 `service_share.go` 的 `Grant`/`Revoke` 中添加 outbox 事件发布 | `internal/file/service_share.go` 重构 | 1.4 |
| 2.3 | 在 `service/file.go` 的更新/删除操作中添加 outbox 事件发布 | `internal/file/service_crud.go` 重构 | 1.4 |
| 2.4 | 新增事件消费 handler（生成 NotifTypeFileShared 等通知） | `internal/notification/handler_file.go` | Phase 2.1 |

**Phase 3：消费端规范化（1-2 周）**

| Step | 任务 | 产出 | 依赖 |
|------|------|------|------|
| 3.1 | 定义 `EventHandler` 接口 + `EventRegistry` | `internal/outbox/registry.go` | Phase 1 |
| 3.2 | 拆分 `notificationFor` 为每个事件类型独立的 handler | 每个 handler 一个文件 | 3.1 |
| 3.3 | 添加集成测试验证新旧 handler 行为一致性 | `internal/notification/` integration tests | 3.2 |

### 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **事务性 outbox 引入性能瓶颈** | 低 | 中 | outbox 表在同一个 Postgres 实例，事务内多一次写入，但对于 aero-id 的规模（非高吞吐场景）可接受。监控 `outbox` 表的积压情况，必要时可引入异步 consumer。 |
| **重构 relay 导致消息丢** | 中 | 高 | 采用**并行运行策略**：新老 relay 同时运行一段时间，对比 outbox 事件的消费状态。验证通过后再移除旧 relay。 |
| **shutdown 时序修复导致启动变慢** | 低 | 低 | 引入合理的超时配置。如果某个组件在 5s 内无法 graceful 停止，应该强制停止而非无限等待。为每种组件分别配置超时。 |
| **死代码删除后才发现有引用** | 低 | 中 | 删除前使用 `grep -rn "internal/search" internal/ --include='*.go'` 确认零引用。删除后在 CI 中确保 `make test && make lint` 通过。 |
| **通知 handler 拆分引入回归** | 中 | 中 | 为每个 handler 编写独立的单元测试，对比输入事件 → 输出通知的映射行为。在 Phase 3 中保留旧的 `notificationFor` 作为基准，通过 diff 验证新 handler 的输出一致。 |

### 监控与可观测性建议

在实施上述架构变更的同时，建议补充以下可观测性能力（这部分是"免费附加"的，因为在重构过程中自然需要）：

1. **outbox relay 延迟**：记录从事件入 outbox 表到被 relay 消费的时间分布 → 可观测性度量
2. **后台 goroutine 健康报告**：`/healthz` 返回每个 daemon 的状态（running/stopped/error）
3. **shutdown 超时记录**：每次 shutdown 记录每个组件的停止时间，如果接近超时阈值则告警
4. **事件积压告警**：如果 outbox 表积压 > 1000 条未消费事件，发出告警

---

## 总结

从架构角度看，aero-id 的**核心设计是健康的**——分层清晰、职责分离、状态空间有限。但**架构治理的一致性有瑕疵**，主要表现为两个系统性缺陷：

1. **事件驱动模式的覆盖率不一致**（outbox 在 message 域存在，在 file 域缺失）
2. **后台进程的生命周期管理是隐式而非显式的契约**（无统一接口、无超时层级、无健康报告）

这两个缺陷都不是"需要重写"的问题，而是**需要在现有架构基础上加强治理**的问题。路线图的核心逻辑是：

> 先建立基础设施（事务性 outbox、统一生命周期管理）→ 填补文件域的空白（事件生产 + 事件消费）→ 规范化消费端架构（handler 注册表）

建议立即启动 Phase 1，并同步推进方向二的死代码清理（作为快速胜利来建立动量）。Phase 1 中的所有变更都是内部重构，对 API 消费者透明，可以在正常的迭代周期中并行进行。
