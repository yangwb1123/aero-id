# 架构师评审：aero-id 五个实现缺口分析

## 1. 架构评估

### 当前架构的优势

这份分析本身已经暴露了架构设计的一个重要优点：**约束力强的分层结构使缺口的发现变得可靠**。因为 `internal/` 的每一层（handler → service → repository → Postgres）都有清晰的责任边界，分析者可以直接对比 "接口定义存在 → 实现是否完整" 来定位缺损。这是一种架构可审计性（auditability）的证据——好的架构让缺口无所遁形。

具体而言：

- **分层单向依赖**（handler→service→repository）使每个缺口的定位精确到层。缺口二的发现路径（domain 常量 → 迁移种子数据 → handler 清单 → repository 接口）完美验证了这种分层约束的检验价值。
- **Composition root**（`internal/app/app.go`）作为单点真实来源，使得缺口五的跨实例分析可以直接枚举所有后台组件，逐一检查其状态管理模式。
- **事务性 outbox 模式**已就位，缺口四的邮件渠道扩展天然可以利用现有消费链，这是架构前瞻性的体现。

### 关键局限性

分析文档指出的缺口恰好映射到架构的五个结构性债务：

| 架构维度 | 当前状态 | 隐含假设 | 风险 |
|---------|---------|---------|------|
| **状态管理模式** | per-instance state 的三个组件（Hub/Limiter/Cache）与一个 shared state（Idempotency Store）并存，无统一策略 | "部署以单实例为默认" | 水平扩展时必须逐组件排查状态模型，增加迁移成本 |
| **Observability 契约** | `/metrics` 仅暴露 Prometheus 标准指标，组件级度量（连接池深度、channel 丢弃率、缓存命中率）缺失 | "组件健康可自证" | 生产故障定位需要猜——事件丢失还是连接断了？ |
| **后台任务调度** | 所有 goroutine（relay/sweeper/cleaner）在所有实例上重复执行，无 leader election | "同一个 JVM/进程运行所有东西" | 水平扩展时产生竞争条件和重复工作 |
| **跨传输层一致性** | HTTP 和 gRPC handler 各写各的，共享 service 层，但缺口二、三表明审计和授权逻辑的缺失同时在两个传输层出现 | "传输层只做编解码" | 缺口修复需要同时在两个 handler 中操作，增加出错几率 |
| **配置模型** | `config.yaml` + 环境变量覆盖，但 SSE 缓冲区大小、连接生命周期参数等不在 config 中 | "配置只覆盖基础设施和外部依赖" | 组件级调优参数硬编码，运维无法在不改代码的情况下调整行为 |

### 关键设计决策评估

**已做对的决策**：

1. **Idempotency Store 放在 Postgres 中**：这是正确支持水平扩展的唯一一个状态组件。不是因为巧合，而是因为幂等性天然需要强一致性——放在 shared DB 是正确的选择。其他三个 per-instance 组件（Hub/Limiter/Cache）都可以容忍最终一致性或 best-effort，但 idempotency 不能。

2. **Outbox 模式的选择**：事务性 outbox + 独立 relay 的设计为异步处理（通知、邮件、webhook）提供了可靠的基础。分析文档中指出的邮件渠道缺失并不是 outbox 模式的缺陷——恰恰相反，outbox 使得添加新消费者成为配置变更而非架构变更。

3. **GORM + migration 分离**：不自动应用 migration 的设计（`make migrate-up` 独立运行）在 CI/CD 场景中是正确的——migration 应作为部署流水线的独立步骤，而非应用启动的一部分。

**需要重新审视的决策**：

1. **SSE Hub 的无界连接策略**：`sync.Map` + 无连接数上限的设计隐含假设"SSE 连接是稀有资源"。在移动端场景中，用户可能会在多个标签页/设备上打开连接，每个设备都建立一条 SSE 连接。没有 per-uid 上限和全局上限，这是一个资源耗尽攻击向量。

2. **`notification_preferences` 缺少 channel 维度**：当前模型的 `Enabled bool` 是二元开关——要么全部接收，要么全部关闭。产品层面，用户可能需要"评论回复走 App 内推送，系统公告走邮件"这样的差异化配置。这是数据模型抽象不足的案例。

3. **文件分享的扁平权限模型**：所有被分享者获得相同权限（只读），且无过期时间。这在初期简化了实现，但限制了向协作场景（可编辑/可评论/可再分享）的演进。

### 技术债评估

| 债务类型 | 严重程度 | 偿还成本 | 建议时机 |
|---------|---------|---------|---------|
| SSE Hub 无度量 | 高（生产盲点） | 低（50 行） | 立即 |
| ban 端点缺失 | 高（安全断裂） | 低（80 行 + 1 迁移） | 立即 |
| 后台任务无 leader election | 高（水平扩展后直接故障） | 中（200 行 + 依赖分析） | 扩展前必须 |
| 文件分享无过期 | 中（合规缺口） | 低（150 行） | 本轮 |
| 邮件渠道缺失 | 中（产品缺口） | 中（400 行 + SMTP 依赖） | 下一轮 |
| 跨实例 Hub/Limiter/Cache | 高（架构瓶颈） | 高（依赖选型，可能 1000+ 行） | 分阶段 |

**关键判断**：这不是一个"架构崩坏"的系统，而是一个**在设计初期做了合理的单实例假设，现在需要在生产部署前补上水平扩展基础设施**的系统。缺口的"代码足迹极小但架构影响极大"特征说明架构主体是健康的，只是在"默认单实例"向"可水平扩展"的过渡中出现了断层。

---

## 2. 扩展方向

以下 5 个方向按价值/复杂度比排序。

### 方向一：多传输层统一授权框架（Security Gateway Layer）

**为什么需要**：
当前授权逻辑在 service 层散点分布：`HasPermission` 检查散落在不同 service 方法中。缺口二（`user.ban`）的发现暴露出这个模式的问题——当添加一个新权限端点时，开发者需要在两个传输层（HTTP + gRPC）各自添加路由和 handler，但真正的**授权守卫逻辑**（先检查 `user.ban` 权限，再执行封禁）应该是一处定义、处处生效。

**核心挑战**：
- 如何在保持单层责任的同时，不引入 AOP 式魔法（过度抽象）
- gRPC 拦截器和 HTTP 中间件的语义差异（gRPC context metadata vs HTTP headers）
- service 层已经承担了授权职责，再抽离一层可能造成职责模糊

**架构变更**：
```
当前：
handler/http/admin.go → service/admin.go → repository
handler/grpc/admin.go → service/admin.go → repository

建议：
handler/http/admin.go → middleware/authorization.go → service/admin.go → repository
                        ↑ uniform permission check before entry
                        ↓
                 authorization.New(store).Require("user.ban")
```
即在 HTTP 路由注册和 gRPC 服务注册处插入一个统一的 **Permission Guard**，它读取 context 中的 actor UID + role，与所需的权限比较，在进入 service 之前拒绝。

**现有系统影响**：
- 低。当前权限常量已经定义在 `domain` 包中，统一守卫是新增层而非重构层
- HTTP 和 gRPC 都支持中间件机制，不需要修改 service 层
- 需要新增 `AuthorizationChain` 类型，允许不同的端点组合多个权限要求

### 方向二：事件驱动架构的渠道抽象层（Notification Channel Abstraction）

**为什么需要**：
缺口四（邮件渠道缺失）和缺口一（SSE Hub 单点）共同指向一个更根本的需求：**通知的"交付"和"编排"应该分离**。当前 Dispatcher 同时负责编排（哪些用户收到通知）和交付（通过 SSE 推送）。邮件渠道加入后，Dispatcher 需要同时处理 SSE + Email + 未来的 push notification，这会使其膨胀成一个难以测试的巨型分发器。

**核心挑战**：
- 每个渠道有不同的速率限制、重试策略、幂等性要求（SSE 无需幂等、邮件需要、Push 需要）
- 邮件发送是阻塞 IO，SSE 是异步推送——混合在同一个 Dispatch 循环中会互相影响
- 需要支持每个用户每个通知类型的渠道偏好

**架构变更**：
```
当前：
outbox.Handler = Dispatcher (负责: 写入通知表 + SSE推送)

建议：
outbox.Handler → NotificationRouter (读偏好 → 选择渠道)
                    ├── InAppChannel (写入通知表 + SSE推送)
                    ├── EmailChannel (SMTP发送 + 写入delivery_log)
                    └── PushChannel (FCM/APNs + 写入delivery_log)
```
每个 `Channel` 实现相同的接口（如 `Deliver(ctx, Notification) error`），速率限制在每个 Channel 内部独立管理。

**现有系统影响**：
- 中。需要重构 Dispatcher 的职责，但 outbox 的输入输出不变，不影响已有通知的写入
- `notification_preferences` 表需要 migration 增加 `channels` 列
- 邮件模板需要新系统，是主要工作量所在

### 方向三：跨实例状态共享的可插拔抽象层（Pluggable State Backend）

**为什么需要**：
缺口五的三个 per-instance 组件（Hub、Limiter、Cache）各自有不同的一致性要求，但它们的共性是需要一个**可切换的后端**：单实例时用 in-memory，水平扩展时用 Redis（或 NATS）。这个切换应该是配置驱动的，不需要改代码。

**核心挑战**：
- 三种状态类型对后端的要求不同：Hub 需要 pub/sub（广播），Limiter 需要计数（Redis INCR + TTL），Cache 需要键值存储（Redis GET/SET + TTL）
- 后端切换必须是零信任的——Redis 不可用时必须优雅降级到 in-memory，而不是崩溃
- 引入 Redis 是新基础设施依赖，运维团队需要评估

**架构变更**：
```
当前：
hub.Hub         ──→ sync.Map
ratelimit.Limiter ──→ map[string]*window
sso.UserCache     ──→ sync.Map + TTL sweeper

建议：
hub.Hub         ──→ hub.Backend (in-memory | redis.PubSub)
ratelimit.Limiter ──→ limiter.Backend (in-memory | redis.SortedSet)
sso.UserCache     ──→ cache.Backend (in-memory | redis.String)
```
每个 Backend 接口只包含该组件需要的原语（Publish/Subscribe、Increment/GetWindow、Get/Set），而不是暴露整个 Redis 客户端。

**现有系统影响**：
- 中到高。这是架构层面的变更，但可以分组件独立推进（每个组件是一个 PR）
- 配置层需要增加 `hub.backend`、`limiter.backend`、`cache.backend` 选项
- 测试策略需要同时测试 in-memory 和 Redis 两种后端
- 对现有功能的侵入性较低——service 层的接口不变，只是底层存储替换

### 方向四：文件协作权限模型升级（Share Permission Model Evolution）

**为什么需要**：
缺口三揭示的文件分享缺少过期时间是一个功能缺口，更深层的设计问题是**文件分享的权限模型过于扁平**。当前 `file_shares` 表只有一条记录表示"某人可访问某文件"，没有区分只读、可下载、可评论、可再分享。这不仅限制了产品功能（协作编辑、共享相册），在合规场景中也无法满足"不同被分享者应有不同访问级别"的要求。

**核心挑战**：
- 权限升级需要迁移已有数据（现有分享记录默认获得什么权限）
- 权限检查路径（`IsSharedWith` → 下载 URL 生成 → 访问审计）需要感知权限级别
- `max_access_count` 的原子性检查在并发场景下需要乐观锁或行级锁

**架构变更**：
```
迁移 000006 表结构进化：
file_shares (
    file_id      UUID NOT NULL,
    grantee_uid  TEXT NOT NULL,
    owner_uid    TEXT NOT NULL,
    permission   TEXT NOT NULL DEFAULT 'read',    -- new: read | download | comment | write
    expires_at   TIMESTAMPTZ,                     -- new
    max_access   INT,                             -- new: max access count
    access_count INT NOT NULL DEFAULT 0,          -- new
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (file_id, grantee_uid)
)
```
应用层：`ShareFile` 接受可选的 permission/expiry/accessLimit 参数；`DownloadURL` 中检查 permission >= download。

**现有系统影响**：
- 低到中。表结构变更通过 migration 解决，现有分享记录默认获得 'read' 权限
- 检查逻辑是纯新增（当前没有过期和计数检查），不会破坏已有功能
- 关键在于 `access_count` 的原子递增——推荐使用 `UPDATE ... SET access_count = access_count + 1 WHERE ... AND (max_access IS NULL OR access_count < max_access)`，依赖 Postgres 的 MVCC 保证安全

### 方向五：后台任务领导选举框架（Background Task Coordinator）

**为什么需要**：
缺口五指出 outbox relay 和 GC sweeper 在多个实例上重复执行的问题。这不是一个组件的问题，而是所有后台 goroutine 共有的模式——它们是**后台任务**，不是**请求处理任务**。后台任务天然需要 singleton 语义（全集群只有一个执行者），而请求处理任务天然需要 shared-nothing 语义（所有实例各自处理请求）。

**核心挑战**：
- 领导选举不能使用 Redis（因为 Redis 本身可能不可用），必须使用 Postgres（故障域对齐）
- 选举的租约（lease）时间需要精心选择：太短导致频繁选举，太长导致故障恢复延迟
- 不同的后台任务可能需要独立的 leader（一个实例做 relay，另一个实例做 GC）
- 选举代码必须与业务逻辑分离，才能在不同的任务中复用

**架构变更**：
```
internal/leadership/
    leadership.go   // LeaderElection 接口
    pg_election.go  // 基于 pg_advisory_lock 或 Postgres 租约表的实现

用法：
election := leadership.NewPG(connPool, "outbox-relay")
if election.Acquire(ctx) {
    // 本实例成功获取领导权，执行 outbox relay
    defer election.Release()
    for {
        select {
        case <-ctx.Done():
            return
        case <-election.Lost():
            return  // 领导权被抢占或租约过期
        default:
            // process outbox
        }
    }
}
```

**现有系统影响**：
- 中。需要新增 `internal/leadership/` 包和 1 个 migration（leader_leases 表）
- 需要逐个改造后台任务（outbox relay、GC sweeper、rate limit cleaner、idem cleaner）
- 每个任务的改造是独立的，可以分步实施
- 主要风险在选举的时序竞争（两个实例同时认为自己是 leader），需要严格的测试覆盖

---

## 3. 接口设计建议

### 3.1 当前接口设计的优缺点

**做得好的**：

- **`repository` 接口很小**：每个 domain 一个接口，方法数 3-8 个。这使得测试 mock 简单，也使得缺口二、三的分析可以通过接口签名快速判断缺失的方法。
- **`service` 方法的签名统一**：`func (s *XxxService) Action(ctx, params) error` 模式一致，gRPC 和 HTTP handler 的适配成本低。
- **`outbox.Handler` 接口极简**：`Handle(ctx context.Context, events []OutboxEvent) error`——一个方法涵盖所有 outbox 消费者。这使得 Dispatcher 和未来的 EmailChannel 可以并列实现同一个接口。

**需要改进的**：

- **`storage.FileStorage` 缺少上下文的传递**：文件存储的接口定义未传递 `ctx context.Context`（需要确认），这在分布式追踪中可能导致 trace 断裂。
- **`hub.Hub` 接口暴露了内部实现**：`pub(uid, event)` 方法签名与 `adapter.go` 的 `Publish(uid, eventType, data, eventID)` 不一致，说明适配层的引入是后加的。

### 3.2 核心模块的新接口

**Notification Channel 接口**：
```go
// 统一渠道接口，每个渠道独立实现
type Channel interface {
    // Name 返回渠道标识（"in_app" / "email" / "push"）
    Name() string
    // Deliver 送达一条通知。阻塞直到送达完成或明确失败。
    // 实现者应自行处理重试和幂等。
    Deliver(ctx context.Context, notif domain.Notification, recipient domain.User) error
    // RateLimiter 返回该渠道的速率限制器（可选）
    RateLimiter() ratelimit.Limiter
}
```

**后端抽象接口**（以 `hub.Backend` 为例）：
```go
// Backend 是 SSE Hub 的底层状态管理，支持两种实现：in-memory 和 Redis pub/sub
type Backend interface {
    // Subscribe 为 uid 创建事件通道。返回的 channel 在 Close 时应被关闭。
    Subscribe(uid string, bufSize int) (<-chan SSEEvent, error)
    // Unsubscribe 取消订阅。
    Unsubscribe(uid string, ch <-chan SSEEvent) error
    // Publish 向指定 uid 的所有订阅者广播事件。
    Publish(uid string, evt SSEEvent) error
    // Close 关闭后端。
    Close() error
}
```

**Leader Election 接口**：
```go
// LeaderElection 为后台任务提供单实例执行保障
type LeaderElection interface {
    // Acquire 尝试获取领导权。阻塞直到获取成功或 ctx 取消。
    // 返回的 cancel 函数应在任务结束时调用以释放领导权。
    Acquire(ctx context.Context) (cancel func(), err error)
    // IsLeader 返回当前实例是否为 leader（非阻塞）。
    IsLeader() bool
    // Lost 返回一个 channel，当领导权丢失时收到信号。
    Lost() <-chan struct{}
}
```

### 3.3 向后兼容性策略

针对五个缺口的修复，向后兼容性策略如下：

| 缺口 | 变更类型 | 兼容策略 |
|------|---------|---------|
| 缺口一 Hub 度量 | 纯新增（加计数器、暴露 gauge） | 完全向后兼容，旧客户端不受影响 |
| 缺口一 Hub 生命周期参数 | 新增配置项，默认值为 current 行为 | `max_connection_duration=0` 表示无限制，保持现有行为 |
| 缺口二 ban 端点 | 纯新增端点 | 旧客户端不使用即可，不影响已有 API |
| 缺口三 分享过期 | 新增列 + 可选参数 | `expires_at=NULL` 表示不过期，现有分享记录默认为 NULL |
| 缺口四 邮件渠道 | 纯新增通道 | 不修改现有 Dispatcher 行为，通过配置启用 |
| 缺口五 leader election | 后台任务加入选举 | 单实例场景下总是 leader，行为不变 |

**关键原则**：所有缺口修复都遵循"新行为可选加入，旧行为不变"的兼容策略。这使得修复可以灰度上线，先部署代码再启用配置，出现问题回退配置即可。

---

## 4. 技术选型

### 4.1 各缺口涉及的依赖评估

| 缺口 | 可能需要的新依赖 | 必要性 | 替代方案 |
|------|----------------|--------|---------|
| 缺口一 跨实例 Hub | redis/go-redis (v9) / nats-io/nats.go | 高 | 自建基于 Postgres LISTEN/NOTIFY 的 pub/sub |
| 缺口三 分享过期 | 无（纯 SQL 变更） | — | — |
| 缺口四 邮件渠道 | go-mail/mail (v2) | 高 | 自建 SMTP 协议实现（不推荐） |
| 缺口五 限流 Redis | redis/go-redis (v9) | 中 | 自建基于 Postgres 的分布式限流（性能差） |
| 缺口五 缓存 Redis | redis/go-redis (v9) | 中 | 使用同一个 Redis 实例的不同 DB |

### 4.2 Redis 选型评估

对于缺口一和缺口五，Redis 是唯一现实的跨实例状态共享选项。评估如下：

| 维度 | Redis | NATS | Postgres LISTEN/NOTIFY |
|------|-------|------|----------------------|
| **Pub/Sub 广播** | ✅ 原生支持 | ✅ 原生支持 | ⚠️ 支持但有限制（通道数、Payload 大小 8KB） |
| **分布式限流** | ✅ INCR + EXPIRE 原子操作 | ❌ 不适合 | ✅ 但性能差（行锁竞争） |
| **缓存** | ✅ GET/SET + TTL | ❌ 不适合 | ⚠️ 需要额外表 + TTL 清理 |
| **运维复杂度** | 中（新增依赖） | 中（新增依赖） | 低（复用已有 Postgres） |
| **故障域** | 独立 | 独立 | 与业务数据库一致 |
| **Go 客户端成熟度** | redis/go-redis（极成熟） | nats-io/nats.go（成熟） | lib/pq（内置） |

**建议**：
- **优先选择 Redis**，因为一个 Redis 实例可以同时 serve Pub/Sub（Hub）、分布式限流（Limiter）、缓存（Cache）三个需求，运维成本低于维护两个独立中间件
- **暂不考虑 NATS**，除非未来出现非通知场景的事件驱动需求（如 CQRS/Event Sourcing），NATS 的高吞吐持久化能力才有用武之地
- **不使用 Postgres LISTEN/NOTIFY 替代 pub/sub**，因为 8KB payload 限制和缺乏自动重连机制会导致 SSE 事件丢失或截断

**注意**：引入 Redis 是一个**新的基础设施依赖**。评估团队是否有 Redis 运维能力。如果没有，可以考虑托管的 Redis 服务（ElastiCache、Memorystore），或选择将 SSE Hub 和 Rate Limiter 保持 in-memory + 使用 Postgres 做 leader election 的折中方案（缺口五 Phase 2 即可满足大部分需求）。

### 4.3 邮件依赖评估

缺口四需要 SMTP 客户端。选项：

| 依赖 | 优点 | 缺点 |
|------|------|------|
| `go-mail/mail` (v2) | API 简洁，支持 SSL/TLS/STARTTLS，支持 HTML+附件 | 需要配合模板引擎 |
| `gomail-v2`（已弃用但 fork 活跃） | 文档多，社区熟悉 | 已停止维护，安全问题 |
| `net/smtp`（标准库） | 无依赖 | 过于底层，需要自己处理连接池、重试、认证 |
| 自建封装（基于 `net/smtp`） | 完全控制 | 重复造轮子，且容易遗漏安全配置 |

**建议**：使用 `go-mail/mail`（v2），包路径 `github.com/go-mail/mail`。它是当前 Go 社区邮件发送的事实标准。配合 `html/template` 标准库做邮件模板渲染，不需要引入模板引擎依赖。

### 4.4 自建 vs 采购的关键决策原则

对于 aero-id 当前阶段（中等规模，Go 服务，Postgres 为主存储）：

| 决策场景 | 应自建 | 应采购/用开源 |
|---------|--------|------------|
| 核心业务逻辑（通知路由、分享授权） | ✅ 核心竞争力的组成部分 | ❌ |
| 通用基础设施（SMTP 发送、Redis 客户端） | ❌ 有成熟开源库 | ✅ |
| 数据迁移（DDL 变更、数据回填） | ✅ Postgres 原生 SQL | ❌ |
| 分布式协调（leader election） | ✅ 基于 Postgres 的轻量实现 | ❌ 引入 etcd/consul 过重 |
| 邮件模板管理 | ✅ Go template + 文件系统或 DB 存储 | ⚠️ 可考虑 SendGrid 等第三方 API |

**核心原则**：
- **选用成熟库封装，不自己实现协议**（SMTP、Redis 协议都有经过实战检验的 Go 库）
- **核心逻辑（通知路由、权限守卫）需要自己实现**，这是 aero-id 的业务价值所在
- **轻量级基础设施（leader election）基于已有存储实现**，不引入新依赖
- **当缺口五的跨实例需求扩展后，再评估是否引入 etcd**——目前 Postgres 选举足够

---

## 5. 实施路线图

### 优先级排序

```
P0 ─── 安全断裂 + 生产前提
├── 缺口二 (user.ban)       — 安全模型完整性的最小切口
└── 缺口五 Phase 1-2        — 水平扩展的基础设施
    ├── 可观察性度量注入        — 组件级 metrics
    └── 后台任务 leader election — outbox + GC 的 singleton 保障

P1 ─── 合规缺口 + 运维盲点
├── 缺口三 (分享过期+审计)    — 文件系统的合规基线
└── 缺口一 Phase 1 (Hub 度量) — 生产监控的必需品

P2 ─── 产品体验 + 高阶架构
├── 缺口四 (邮件渠道)         — 用户留存的增强
├── 缺口一 Phase 2-3         — 可配置缓冲区 + 跨实例广播
└── 缺口五 Phase 3           — Redis 后端接入
```

### 阶段划分

#### 阶段 0：立即执行（1-2 天）

目标：在安全模型中最明显的断裂闭合前，不部署新功能。

| 任务 | 工作量 | 交付物 |
|------|--------|--------|
| 缺口二：`banned_users` 表 migration | 1 份迁移 SQL | 确保反转迁移存在 |
| 缺口二：repository `Ban/Unban/IsBanned` | 1 个文件 | `repository/admin_ban.go` |
| 缺口二：service `BanUser/UnbanUser` | 1 个方法 | `HasPermission("user.ban")` 守卫 |
| 缺口二：HTTP `POST /v1/admin/ban` + gRPC `BanUser` | 2 个 handler | 路由注册 + 错误映射 |
| 缺口二：ban 操作写入 `audit_events` | 1 行 service 调用 | 复用现有 audit 接口 |

**验证标准**：
- `curl -X POST /v1/admin/ban -H "Authorization: Bearer <admin-token>" -d '{"uid":"xxx","reason":"spam"}'` 返回 200
- `curl GET /v1/admin/audit-log?action=user.ban` 中出现该操作
- 被封禁用户尝试 `/v1/messages` 返回 403

#### 阶段 1：生产基础设施加固（1 周）

| 任务 | 工作量 | 交付物 |
|------|--------|--------|
| 缺口一 Phase 1：Hub metrics（连接数、丢弃数、推送速率） | 1 个文件 | `hub_metrics.go` + Prometheus gauge 注册 |
| 缺口一 Phase 1：SSE 连接生命周期（max_duration + inactivity_timeout） | 修改 `hub.go` + config | 配置项 + 健康检查 |
| 缺口五 Phase 1：所有组件度量覆盖率 review | 审查 + 补充 | metrics 清单文档 |
| 缺口五 Phase 2：leader election 包 | `internal/leadership/` 包 | PG 选举实现 + 测试 |
| 缺口五 Phase 2：outbox relay + GC sweeper 改为选举模式 | 修改 `app.go` 启动逻辑 | 两个任务使用选举 |

**验证标准**：
- 两个实例并行运行，仅 leader 实例消费 outbox 和执行 GC
- leader 实例故障后，非 leader 实例在租约超时后自动接管
- `/metrics` 上出现 `aero_hub_active_connections`、`aero_hub_dropped_events_total` 等指标

#### 阶段 2：文件系统合规 (3-4 天)

| 任务 | 工作量 | 交付物 |
|------|--------|--------|
| 缺口三：`file_shares` 表加列 migration | 1 份迁移 SQL | `expires_at`、`max_access`、`access_count` |
| 缺口三：`ShareFile` 增加可选参数 | 修改 service + handler 参数 | 向后兼容 |
| 缺口三：下载路径中检查过期 + 访问计数 | 修改 `DownloadURL` 路径 | 原子递增 + 审计写入 |
| 缺口三：后台惰性清理已过期分享的协程 | 可选 | GC sweeper 的扩展 |

**验证标准**：
- 不传过期时间的分享继续保持永久有效（向后兼容）
- 设置了 `expires_at` 的分享在过期后返回 403
- 设置了 `max_access=3` 的分享在第四次访问时返回 403
- 每次分享访问在 `audit_events` 中有记录

#### 阶段 3：多通道通知 (1-2 周)

| 任务 | 工作量 | 交付物 |
|------|--------|--------|
| 缺口四：`NotificationPreference` 加 `channels` 列 migration | 1 份迁移 SQL | TEXT[] 列 + 默认值 |
| 缺口四：`Channel` 接口定义 + `InAppChannel` 重构现有 Dispatcher | 重构 dispatcher.go | 不改变外部行为 |
| 缺口四：`EmailChannel` 实现（SMTP 客户端 + 连接池） | 新文件 | SendGrid / 通用 SMTP |
| 缺口四：邮件模板系统（基于 `html/template`） | 模板文件 + 渲染函数 | 通知类型 → 邮件正文 |
| 缺口四：配置层（SMTP host/port/user/pass） | config.yaml + env overrides | 所有值可从环境变量覆盖 |

**验证标准**：
- 用户设置 `channels=['in_app', 'email']` 后，收到通知时同时收到 SSE 事件和邮件
- 用户设置 `channels=['email']` 后，仅收到邮件
- 邮件模板正确渲染通知内容，包含回复链接
- SMTP 不可用时，通知写入本地日志且不阻塞通知表写入

#### 阶段 4：跨实例状态共享（2-3 周，可选）

| 任务 | 工作量 | 交付物 |
|------|--------|--------|
| 缺口一 Phase 3：`hub.Backend` 接口 + Redis Pub/Sub 实现 | 1 个文件 | `hub_redis.go` |
| 缺口五 Phase 3：`limiter.Backend` 接口 + Redis SortedSet 实现 | 1 个文件 | `limiter_redis.go` |
| 缺口五 Phase 3：`cache.Backend` 接口 + Redis String 实现 | 1 个文件 | `cache_redis.go` |
| 配置层增加后端选择逻辑 | 修改 app.go | 根据配置初始化不同后端 |
| 测试：in-memory 和 Redis 后端的等效性测试 | 集成测试 | 两种后端通过相同的行为测试集 |

**验证标准**：
- 三个实例并行运行，SSE 事件从任意实例发布到所有实例的订阅者
- 速率限制在所有实例间共享（20 RPS 的总限制，而非每实例 20 RPS）
- SSO 用户缓存一致性（冷启动新实例时从 Redis 而非 SSO 加载）

### 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| Leader election 时序竞争（两个实例同时认为自己是 leader） | 中 | 高 | 使用 Postgres 行级锁 + 严格的租约 TTL + 集成测试覆盖竞争场景 |
| Redis 引入后增加故障域（Redis 不可用导致服务降级） | 低 | 中 | 所有 Redis 后端必须实现优雅降级到 in-memory，启动时检测 Redis 可用性 |
| 邮件发送延迟影响通知表写入 | 低 | 高 | 邮件通道使用独立 worker 池（goroutine pool），不阻塞通知的 in-app 交付 |
| 分享过期检查引入新的竞态条件 | 低 | 中 | `access_count` 递增使用 `UPDATE ... SET = old + 1 WHERE` 原子语句 + 重试 |
| 缺口一跨实例广播在 Redis 故障时导致事件丢失 | 低 | 中 | 退回到每个实例独立广播（best-effort），不在 Redis 恢复前阻塞 |
| 邮件模板泄露用户敏感信息（错误地将用户 A 的数据渲染到用户 B 的邮件中） | 极低 | 极高 | 模板渲染函数必须接受 Context 中的用户信息，使用 `text/template` 而非 `html/template` 时注意自动转义 |

### 关键里程碑

```
第 1 天  ── 阶段 0 完成：ban 端点上线，安全断裂闭合
第 5 天  ── 阶段 1 完成：水平扩展的杠杆已打下（leader election + metrics）
第 9 天  ── 阶段 2 完成：文件系统合规达标
第 16 天 ── 阶段 3 完成：多通道通知上线（SSE + Email）
第 23 天 ── 阶段 4 完成（可选）：跨实例状态共享
         ── 第 23 天后：aero-id 具备水平扩展能力
```

---

## 总结

这五份分析文档识别的五个缺口精准地指向了同一个底层问题：**aero-id 的架构是在"单实例正确"的假设下设计的，而生产部署需要"水平扩展正确"的基础设施**。五个缺口中有三个（Hub、Limiter、Cache）直接是 per-instance 状态的问题，第四个（后台任务）是 singleton 语义问题，第五个（邮件渠道）是通知系统的自然演进。

从架构师的视角，我最推荐的两件事是：

1. **立即闭合缺口二**（ban 端点）。它的代码量极小、安全影响极大，延迟一天闭合就多一天的运营风险。

2. **为阶段 1 配置充裕时间**。Leader election 看似是「锁 + 租约」的简单概念，但在生产环境中，选举的竞争条件、租约恢复、优雅降级是分布式系统的经典陷阱。花时间在这个基础设施上是值得的——它是后续所有水平扩展工作的起点。

缺口三（分享过期）是最容易被推迟的，但它在合规场景中对 B 端客户的说服力至关重要。如果 aero-id 已经有 B 端客户或潜在客户，缺口三应该提升到 P0。

最后，这份分析文档本身的质量值得肯定。它的"代码足迹极小但架构影响极大"的判断完全正确——这说明团队对架构有深刻的理解，缺口不是因为能力不足而是因为在单实例开发和水平部署之间的过渡期出现的断层。这个诊断本身就是架构健康度的证据。
