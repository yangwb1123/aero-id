# 架构分析报告：aero-id 三个缺失的平台级抽象层

> **分析基准**: `docs/requirements/2026-07-11-aero-id-three-systemic-missing-platform-level-features.md`
> **视角**: 资深架构师
> **范围**: 架构评估、扩展方向、接口设计、技术选型、实施路线图

---

## 1. 架构评估

### 1.1 当前架构的优势

分析文档的扫描精度令人印象深刻——它没有停留在 API 表面，而是直插 `internal/hub/hub.go` 的 `sync.Map`、`internal/pkg/ratelimit/ratelimit.go` 的 `sync.Mutex`、`internal/outbox/relay.go` 的无锁 `FetchUnpublished`。这些代码行本身就是架构决策的化石记录。

aero-id 当前架构有三个值得肯定的设计属性：

**优势一：清晰的单向依赖层次。** `handler → service → repository/storage/sso` 的层级是严格的。这意味着新增三个抽象层（变更历史、跨进程协调、准入控制）都可以**插入现有层次之间而不破坏它**——这是架构可演化性的最好信号。

**优势二：限流与审计已在恰当位置。** 虽然文档论证了它们各自的盲区，但 `RateLimit` 中间件和 `AuditEvent` 的存在说明架构师在做 1.0 时有正确的优先级判断：**先保证系统的基本可观测性和自我保护，再优化到生产级**。

**优势三：UUIDv7 + 游标分页的统一 ID 策略。** 这为变更历史的时间线索引提供了天然支持——`entity_edits` 表可以用同一套 ID 生成和分页机制，零额外架构设计。

### 1.2 当前架构的局限性

三个方向的共同根因是一个架构决策的连锁效应：

**根因：系统在 1.0 阶段假定了"单进程 + 单一安全边界"。**

```go
// 这三个结构的并列出现不是偶然的：
sync.Map      // hub.go        — 单进程状态
sync.Mutex    // ratelimit.go  — 单进程串行化
for { FetchUnpublished() }     // relay.go — 单进程轮询假定
```

这个假定导致三个逻辑上的独立问题在代码层合并为一个架构限制：

| 假定 | 失效场景 | 后果 |
|------|----------|------|
| 所有连接在同一进程 | 水平扩展到 2+ 实例 | SSE 通知静默丢失 |
| 所有请求来自同一计数器 | 水平扩展到 2+ 实例 | 限流配额膨胀 N 倍 |
| 所有写操作只有最新值有意义 | 任何需要审计/争议的场景 | 编辑前内容永久丢失 |
| 所有请求必须被处理 | 突发流量/慢依赖 | goroutine 堆积 → OOM |

### 1.3 架构债务与技术债的区分

这里有一个需要明确区分的关键点：

**架构债务（需重构）** ：三个方向中，**变更历史**和**准入控制**是纯新增抽象层——它们不需要改动现有代码的流程，只需在现有流程中插入新的写点（变更历史）或在入口处增加新的拦截点（准入控制）。这是**债率最低**的架构增强。

**技术债（需改造）** ：**多实例协调**涉及对现有 `Hub.Publish`、`Limiter.Allow`、`Relay.drain` 的内部改造。这比前两者更接近技术债——它需要在保持现有接口不变的前提下，替换内部实现（从本地 sync primitive 变为分布式 primitive）。好在这三个结构都通过接口/结构体封装了实现细节，改造是局部的。

**一个重要的架构观察**：这三个方向如果单独处理都会导致局部优化，**但统一放在一个"分布式基础架构层"的概念下实现，可以产生 1+1+1>3 的效果**——因为三个方向共享同一个基础设施需求（可插拔的后端抽象，fallback 到单实例）。

---

## 2. 高价值架构扩展方向

> 根据文档分析 + 系统的建构成熟度曲线，我给出以下 5 个扩展方向，其中 3 个来自文档，2 个从文档的盲区中推导而来。

### 方向 1：实体级变更历史（Entity Change History）— 文档方向一

**业务价值/技术价值**

这是三个方向中**商业价值最显性**的一个。在 SaaS 中，"数据可回溯性"不是功能特性而是合规底线。GDPR 第 17 条（被遗忘权）要求平台能够证明数据已经被正确删除/修改；第 20 条（数据可携带权）要求能够导出历史版本。没有变更历史，这两个条款的审计路径是不完整的。技术价值在于：它是**数据可观测性的最后一个维度**——我们已经有了 Who 和 What，现在补上 Before 和 After。

**核心挑战和技术难点**

这个方向的难度不在于实现，而在于**边界定义**：

1. **快照粒度**：每条编辑都记录完整对象快照，还是只记录变更字段？前者简单但存储冗余（未被修改的字段也重复存储）；后者节省空间但重建历史时需要按版本拼接（reconstitution problem）。
2. **事务一致性**：变更历史行必须在同一事务中写入，与业务更新操作一起提交或回滚。这要求 service 层的每个"写"方法都变成"读旧值 → 写入 edit 行 → 更新新值"的事务三步曲。当前 service 层没有这个模式，需要引入一个统一的 `EditWithHistory(ctx, entityID, updateFn)` helper。
3. **软删除交互**：`gorm.DeletedAt` 是软删除，但变更历史应该截断还是保留？如果一个实体被软删除然后被硬删除（如果未来有硬删除操作），历史该怎么处理？

**预期的架构变更**

```
internal/
  domain/
    entity_edit.go          ← 新增：domain 实体定义
  repository/
    entity_edit.go          ← 新增：GORM CRUD
  service/
    editor.go               ← 新增：通用变更历史封装
    message.go              ← 修改：Edit 方法中嵌入历史记录
    comment.go              ← 修改：Edit 方法中嵌入历史记录
  handler/
    http/
      history.go            ← 新增：GET /v1/{entity}/{id}/history
```

**对现有系统的影响**：线性影响——新增表和端点，不改现有数据模型。Service 层的 Edit 方法需要包装但逻辑不变。安全性需要关注 `GET /v1/{entity}/{id}/history` 的鉴权（only owner/admin）。

### 方向 2：跨进程协调抽象层（Multi-Instance Coordination Layer）— 文档方向二

**业务价值/技术价值**

这是三个方向中**技术挑战最大**的一个——它触及了 aero-id 从"Go 单体应用"到"分布式系统"的架构成熟度跃迁。业务价值在于：**实时推送（SSE）是用户能否感知到"系统正常工作"的分界线**。Rate limit 失效和 outbox 重复处理是内部问题，但 SSE 通知丢失是用户直接体验到的问题。

**核心挑战和技术难点**

1. **中间件依赖的引入**。当前系统零外部中间件（Postgres 是数据存储不算），引入 Redis 或 NATS 等意味着 CI/CD、本地开发、测试环境都需要管理一个新基础设施。**可回退设计**（有 Redis → 分布式；无 Redis → 退化当前行为）是必须满足的非功能性约束。
2. **PubSub 接口的语义定义**。`Publish(topic, payload)` 的语义需要仔细设计：是 at-most-once（性能优先）还是 at-least-once（可靠性优先）？SSE Hub 的订阅是 ephemeral 的——如果用户在实例 A 上订阅，消息发布到 Redis 但实例 A 正好重启了，这条消息是否算丢失？对于 SSE 场景，答案是"算丢失，但可以接受"（用户重新订阅后拿不到历史消息）。对于 outbox 场景，答案是"绝对不能丢失"（事务日志保证）。
3. **分布式限流的精度-成本权衡**。基于 Redis 的固定窗口限流（INCR + EXPIRE）有竞态窗口问题；滑动窗口（sorted set）精度高但成本也高（每个请求 O(log N)）。需要为不同场景提供不同策略。

**预期的架构变更**

```
internal/
  coordination/
    pubsub.go              ← 新增：PubSub 接口
    pubsub_local.go        ← 新增：退化实现（chan 接力）
    pubsub_redis.go        ← 新增：Redis Pub/Sub 实现
    lock.go                ← 新增：DistributedLock 接口
    lock_db.go             ← 新增：基于 FOR UPDATE SKIP LOCKED
    lock_redis.go          ← 新增（可选）：基于 Redlock
    ratelimit.go           ← 新增：DistributedRateLimiter 接口
    ratelimit_redis.go     ← 新增：Redis 滑动窗口实现
  hub/
    hub.go                 ← 修改：依赖 PubSub 接口而非直接 sync.Map
  notification/
    dispatcher.go          ← 修改：通过 PubSub 广播而非本地 Publish
  outbox/
    relay.go               ← 修改：使用 DistributedLock 确保单实例处理
  pkg/
    ratelimit/
      ratelimit.go         ← 修改（或废弃）：装饰为 coordination 的本地退化
```

**对现有系统的影响**：中等——影响 3 个现有组件（hub、dispatcher、relay）的内部实现，但接口不变。Service 层和 Handler 层完全不感知。

### 方向 3：连接级准入控制层（Connection-Level Admission Control）— 文档方向三

**业务价值/技术价值**

这是三个方向中**事故预防价值最高**的一个。"让请求排队直到 timeout"是分布式系统中连锁崩溃的最常见诱因，而 Admission Control 是阻断这一链条的最后一层防线。技术价值在于：它是**容错的最后一道门**——Rate limit 控制速率，Circuit breaker 控制对外部依赖的调用，Admission control 控制自身 goroutine 的堆积。

**核心挑战和技术难点**

1. **Semaphore 参数确定**。`maxConcurrent = ?` 没有标准答案——它取决于每个请求的平均内存占用 + CPU 时间 + DB 连接占用。一个常见的方法是通过压测找到"当系统吞吐量开始下降时的并发数"（即 Little's Law 的拐点），然后设为该值的 70%。
2. **与 Rate Limiter 的协作**。两个中间件的执行顺序和语义需要协调：是 Admission Control 先拒绝（减少无用限流检查开销），还是 Rate Limiter 先拒绝（让过载检测优先保护下游）？我的建议是 Admission Control 在前（因为它是保护自身，成本更低），Rate Limiter 在后。
3. **动态过载检测的反馈回路**。基于 moving average delay + GC 压力的动态阈值调整是一个有风险的反馈回路——如果设置不当会导致**震荡**（阈值反复上下跳动）。建议 Phase 2 先做静态配置（hand-tuned max），Phase 3 再做动态，且动态调整使用**滞后机制**（hysteresis：升高阈值比降低阈值需要更强的信号）。

**预期的架构变更**

```
internal/
  middleware/
    admission.go           ← 新增：MaxConcurrent + SSE 连接上限
    priority.go            ← 新增：优先级队列支持（可选）
  config/
    admission.go           ← 新增：配置结构体
```

**对现有系统的影响**：极小——新增一个中间件，插入 route group。SSE 连接上限需要修改 `Hub.Subscribe` 的入口，但改动量在 20 行内。

### 方向 4：域事件总线（Domain Event Bus）— 文档未覆盖的推导方向

> 这个方向是方向二（跨进程协调）和方向一（变更历史）的交集。

**为什么需要**

当前 aero-id 的 outbox 事件是基础设施层面的（"一条记录被发布了"），不是业务层面的事件（"一个文件被删除了"）。二者的区别是：基础设施事件关心"什么被写了"，域事件关心"什么发生了"。域事件的消费者不需要知道是哪个 service 产生了它——它只需要知道"一个文件被删除了，所以需要清理所有与这个文件相关的通知"。

docs 中提到的"outbox relay 重复处理"问题，根源不在于没有分布式锁，而在于**事件的语义太底层**——当 relay 不知道事件的业务含义时，它只能"至少一次"地转发，导致重复。

**核心挑战**

1. **事件定义的扩张**。没有清晰边界的话，域事件会膨胀到"每个操作对应一个事件"。需要定义一个**最小有用事件集**：文件上传、文件删除、消息编辑、评论举报、权限变更。
2. **事件 schema 演进**。域事件需要 schema（protobuf 或 Avro）来保证向后兼容。用 JSON 抛进 outbox table 的方式会很快失控。

**预期的架构变更**

```
internal/
  domain/
    events.go              ← 新增：域事件类型定义（interface + struct）
  service/
    event_publisher.go     ← 新增：在 service 的关键操作完成后调用
  outbox/
    store.go               ← 修改：扩展支持域事件（与 outbox 共用同一张表）
```

**对现有系统的影响**：中低——outbox 机制已经存在，域事件只是在其上增加一个类型标签和 schema。Service 层需要增加 `publishEvent(ctx, event)` 调用，但不需要改动核心逻辑。

### 方向 5：配置后端的可插拔化（Pluggable Configuration Backend）— 文档未覆盖的推导方向

**为什么需要**

当前 `configs/config.yaml` 是文件加载 + 环境变量覆盖的模式。在多实例部署中，每个实例需要不同的配置覆盖（如实例 ID、端口），或者需要动态刷新配置（如动态调整 admission threshold、rate limit 配额）。文件 + 环境变量模式不支持无重启的配置更新。

**核心挑战**

1. **动态配置 vs 静态配置的边界**。不是所有配置都应该动态——数据库连接信息、监听端口等属于启动时静态配置。限流配额、准入阈值、log level 等属于运行时动态配置。这个边界需要明确定义。
2. **配置变更的安全性**。如果限流配额被错误地设置为 0，或者准入最大并发被设置为 100000，系统需要在提交前做基本合理性校验。

**预期的架构变更**

```
internal/
  config/
    loader.go              ← 修改：支持分层加载（file → env → remote）
    dynamic.go             ← 新增：动态配置接口 + 监听器
```

**对现有系统的影响**：低——配置层是独立的，变更不影响业务逻辑。

---

## 3. 接口设计建议

### 3.1 核心接口设计原则

对于这三个新增抽象层，我建议遵循以下接口设计原则：

**原则一：所有分布式抽象必须可回退到单实例行为。**
这是架构成熟度跃迁中的**帕累托最优解**。它使得开发、测试、单实例生产部署不受影响，而多实例部署只需额外启动一个 Redis 实例。接口设计应该这样组织：

```go
// bad — 强制引入 Redis
type PubSub interface {
    Publish(topic string, msg []byte) error
    Subscribe(topic string) (<-chan Message, error)
}

// good — 构造函数决定后端
type PubSub interface {
    Publish(ctx context.Context, topic string, msg []byte) error
    Subscribe(ctx context.Context, topic string, opts ...SubOption) (<-chan Message, error)
}

func NewPubSub(cfg PubSubConfig) PubSub {
    if cfg.Redis != nil {
        return newRedisPubSub(cfg.Redis)
    }
    return newLocalPubSub()  // chan-based, 同进程
}
```

**原则二：接入点不可变，实现在接入点后切换。**
Hub、Limiter、Relay 这三个组件的公共方法签名应该保持不变。变化的是它们依赖的内部实现。这是封装原则的体现——Service 和 Handler 层不应该感知到"现在是多实例模式"。

**原则三：变更历史应该是隐式的而非显式的。**
Service 的 `Edit` 方法不应该知道自己正在记录历史。这是通过"在事务包装器中嵌入历史记录逻辑"实现的——Service 调用 `repo.Update(ctx, entity)`，事务包装器在 `Update` 执行前先 `SELECT` 旧值、在 `Update` 执行后 `INSERT` 变更历史行。

### 3.2 是否需要引入新的抽象层

**不需要在 domain 层面引入新抽象。** 三个方向的抽象都在**基础设施层**：

| 方向 | 抽象层 | 作用域 |
|------|--------|--------|
| 变更历史 | repository/tx wrapper | 封装在 repo 层的事务内 |
| 跨进程协调 | `internal/coordination/` | 新包，独立于业务逻辑 |
| 准入控制 | `internal/middleware/` | HTTP 中间件 + Hub 修改 |

这三个抽象层都不需要修改 domain entity、handler transport、service 的业务逻辑。

**但是，需要引入一个"变更通知接口"供 service 层调用**：
```go
// EntityHistoryRecorder — 在 service 层的写操作中调用
type EntityHistoryRecorder interface {
    RecordEdit(ctx context.Context, entityType string, entityID string,
        before, after map[string]any, actorUID string) error
}
```

这个接口的实现可以是异步的（通过 outbox），也可以是同步的（在同一个事务中）。选择取决于一致性要求——我的建议是同步（同一事务），因为 history 不应该丢失。

### 3.3 向后兼容性设计

三个方向各自的向后兼容策略：

**变更历史**：完全向后兼容。新增表 `entity_edits` 不影响现有读路径。写路径需要修改，但旧数据不受影响（没有历史记录的实体在查询历史时返回空列表）。

**跨进程协调**：向后兼容的关键是**回退机制**。`PubSub` 的 `newLocalPubSub()` 使用 chan 接力，行为完全等同于当前的 `sync.Map` 模式（甚至略有改进——chan 接力比 `sync.Map` 的 `Range` 更安全，因为避免了迭代期间发布的问题）。

**准入控制**：中间件的默认配置应该**完全关闭**（`max_concurrent = 0` 表示无限制）。这确保现有的配置文件不需要修改。

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

**三个方向的中间件依赖分析**：

| 方向 | 需要新技术栈？ | 最低要求 | 推荐选项 |
|------|--------------|----------|---------|
| 变更历史 | 否 | PostgreSQL JSONB | 无需新依赖 |
| 跨进程协调：PubSub | 是 | Redis Pub/Sub 或 Postgres LISTEN/NOTIFY | **Redis**（Postgres LISTEN/NOTIFY 不够可靠——slots 限制、无持久化） |
| 跨进程协调：DistributedLock | 否 | `SELECT ... FOR UPDATE SKIP LOCKED`（Postgres 9.5+） | PostgreSQL 原生，无需新依赖 |
| 跨进程协调：分布式限流 | 是 | Redis INCR + EXPIRE | Redis（可选） |
| 准入控制 | 否 | sync primitive + atomic | 无需新依赖 |

**结论：Redis 是可选的、按需引入的中间件，不是必须的。**

### 4.2 Redis 引入的评估

| 维度 | 评估 |
|------|------|
| **必要性** | 非必须。如果 SSE 跨实例广播不是硬需求，`FOR UPDATE SKIP LOCKED` 已解决 outbox 的核心问题 |
| **运维成本** | 需要管理一个 Redis 实例，备份/监控/升级 |
| **CI 影响** | 需要 testcontainer（已有 Docker 依赖基础） |
| **开发影响** | 本地开发需要多一个 docker-compose service |
| **替代方案** | Postgres `LISTEN/NOTIFY`（但不推荐——错误处理弱、slot 上限、无持久化） |

**建议立场**：Redis 作为**可选增强**引入，不成为硬依赖。开发环境默认用 localPubSub（退化为单实例行为），生产环境根据部署规模按需启用 Redis。

### 4.3 技术选型决策矩阵

| 决策点 | 选项 A | 选项 B | 建议 |
|--------|--------|--------|------|
| 变更历史存储格式 | JSONB（PostgreSQL） | 独立表每字段一列 | **JSONB** — 灵活，避免 N 张历史表 |
| 变更历史写入时机 | 同步（同一事务） | 异步（outbox event） | **同步** — history 不应丢失，性能开销可忽略 |
| PubSub 后端 | Redis Pub/Sub | PostgreSQL LISTEN/NOTIFY | **Redis** — 更可靠（见 4.2） |
| 分布式锁 | PostgreSQL `FOR UPDATE SKIP LOCKED` | Redis Redlock | **PostgreSQL** — 足够简单，无须 Redlock 的复杂度 |
| 准入控制实现 | channel semaphore | `sync.WaitGroup` + counter | **channel semaphore** — 内置 cancel 支持，更灵活 |
| SSE 连接上限计数器 | `atomic.Int64` + 配置检查 | Redis counter | **atomic.Int64** — 进程级，简单且够用 |
| 域事件 schema | protobuf | JSON + type field | **protobuf** — 如果已有 protobuf 基础设施；否则 JSON 够用 |

### 4.4 自建 vs 不建的决策依据

对于变更历史记录器，**自建**是正确的选择——因为它紧密耦合于 aero-id 的 domain 模型和事务边界。通用的审计框架（如 `gormigrate` 的 history 插件）要么太通用（难以定制快照策略），要么太复杂（需要侵入式 struct tag 处理）。

对于 PubSub 抽象，**不建**——集成现有的 redis-go 库（`go-redis` 或 `rueidis`），只包装接口。不是因为不能建，而是因为不值得：Redis Pub/Sub 的语义已经足够简单，没有二次抽象的必要。

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0 — 当前 sprint           P1 — 下个 sprint         P2 — 下下个 sprint
│                          │                         │
├─ Admission Control P1    ├─ 变更历史 P1            ├─ 准入控制 P3 (动态)
│  (MaxConcurrent 中间件)  │  (entity_edits 表)      │   (moving average 检测)
│                          │                         │
├─ 多实例协调 P1           ├─ 多实例协调 P2          ├─ 多实例协调 P3
│  (FOR UPDATE SKIP LOCKED)│  (PubSub + Redis)       │   (分布式限流)
│                          │                         │
│                          ├─ 域事件总线 P1          ├─ 配置可插拔化
│                          │  (最小事件集定义)       │
```

**排序逻辑**：
- **P0 全是短周期（半天到 1 天）、高回报、低风险**的改动。目的是快速消除最危险的运营事故（OOM 和 outbox 重复）。
- **P1 全是中等周期、高战略价值**的改动。变更历史是合规基础，PubSub 是架构跃迁的基础设施。
- **P2 全是锦上添花、依赖前期工作**的改动。动态过载检测需要 P0 的准入控制基础设施；分布式限流的容量规划数据来自 P1 的多实例协调实践。

### 5.2 阶段划分

#### Phase 0：快速止血（2 天）

**目标**：消除当前架构中可能导致生产事故的已知风险。

| 任务 | 工期 | 交付物 | 风险 |
|------|------|--------|------|
| `MaxConcurrent` 中间件 | 0.5 天 | `internal/middleware/admission.go` | 低 — pure additive |
| SSE 连接上限 | 0.5 天 | `Hub.Subscribe` 加 counter | 低 — 20 行改动 |
| Outbox `FOR UPDATE SKIP LOCKED` | 1 天 | `internal/outbox/relay.go` 改 fetch SQL | 中 — SQL 语义变化需经集成测试验证 |

**里程碑**：所有生产部署默认启用 `MaxConcurrent`（配置为 `max_open_conns × 2`）。

#### Phase 1：数据可观测性（1 周）

**目标**：所有编辑操作记录变更快照，暴露历史查询 API。

| 任务 | 工期 | 交付物 |
|------|------|--------|
| `entity_edits` 表 + migration | 0.5 天 | `migrations/...entity_edits.up.sql` |
| `EntityHistoryRecorder` interface + impl | 1 天 | `internal/repository/entity_edit.go` |
| Service 层嵌入变更记录 | 2 天 | `internal/service/editor.go` + 修改 Edit/UpdateMetadata |
| `GET /v1/{entity}/{id}/history` endpoint | 1.5 天 | `internal/handler/http/history.go` |

**里程碑**：管理员可以通过 API 查询任意实体的编辑历史。

#### Phase 2：基础设施跃迁（2 周）

**目标**：系统能够在多实例部署下正确运行（SSE 跨实例广播 + outbox 单实例处理）。

| 任务 | 工期 | 交付物 |
|------|------|--------|
| `coordination.PubSub` interface + local impl | 1 天 | `internal/coordination/pubsub.go`, `pubsub_local.go` |
| Redis Pub/Sub 实现 | 2 天 | `internal/coordination/pubsub_redis.go` |
| Hub 迁移到 PubSub | 1 天 | `internal/hub/hub.go` 修改 |
| Dispatcher 迁移到 PubSub | 1 天 | `internal/notification/dispatcher.go` 修改 |
| Outbox 迁移到 DistributedLock | 1 天 | `internal/coordination/lock.go`, `lock_db.go` |
| 集成测试（多实例模拟） | 3 天 | `testcontainers` + 多进程模拟 |

**里程碑**：启动 2 个 aero-id 实例时，SSE 通知在两个实例上都能收到。

#### Phase 3：弹性增强（可选，2 周）

**目标**：系统有过载自适应能力和配置热加载能力。

| 任务 | 工期 | 交付物 |
|------|------|--------|
| 动态过载检测（latency + GC） | 3 天 | `internal/middleware/admission.go` 扩展 |
| 分布式限流（Redis） | 2 天 | `internal/coordination/ratelimit_redis.go` |
| 最小域事件集 | 3 天 | `internal/domain/events.go` + outbox 扩展 |

**里程碑**：系统过载时自动降级（降低 admission threshold），而非崩溃。

### 5.3 风险点和缓解策略

| 风险 | 可能性 | 影响 | 缓解策略 |
|------|--------|------|----------|
| `FOR UPDATE SKIP LOCKED` 导致死锁或长时间等待 | 低 | 高（DB 性能） | 设置 `lock_timeout`，在 DB 连接层加超时兜底 |
| Redis Pub/Sub 消息丢失（Redis 崩溃） | 低 | 高（实时通知丢失） | 设计为 at-most-once 语义（SSE 场景可接受）；outbox 不走 Redis（走 `FOR UPDATE SKIP LOCKED`） |
| `entity_edits` 表写入增加主事务延迟 | 中 | 中（编辑操作变慢） | 变更历史为**异步写入**（同一事务但不在主实体上加锁，使用 defer tx? 不——在同一个事务中写入历史不增加额外锁，只增加一行 INSERT） |
| MaxConcurrent 配置不合理导致正常请求被拒绝 | 中 | 中（误判为过载） | 默认关闭（0 = unlimited），由运维人员根据压测结果打开；监控 alarm 在拒绝率 > 1% 时触发 |
| Public history API 泄露隐私 | 高 | 高（合规风险） | 历史 API 必须做所有权校验（只有 owner 和 admin 可查询），写入审计日志中每次历史查询 |
| 多组件同时引入分布式抽象导致配置复杂度爆炸 | 中 | 中 | 三个组件共用一个 `CoordinationConfig`（含 `Redis` 子段），在 `app.go` 中统一初始化 |

---

## 6. 一个更深层的架构观察

文档的三个方向看似独立，但我看到它们共享一个**更深层的根因**：

> **aero-id 的数据模型本质上是一个"当前快照"模型，没有足够的语义信息来推理它自己的历史、分布式身份和健康状态。**

- 没有变更历史 → 无法推理**数据的时间演化**
- 没有跨进程协调 → 无法推理**系统的空间分布**
- 没有准入控制 → 无法推理**自身的健康边界**

这三个问题如果用一句话回答，是：

> **"当前架构假设系统只有一个副本、数据只有最新状态、资源是无限的。"**

这个假定的解除顺序应该是：

```
P0: 接受"资源有限" → 准入控制
P1: 记录"数据演化" → 变更历史
P2: 承认"多副本" → 跨进程协调
```

从这个顺序看，P0+1 可以在不引入新中间件的情况下完成，P2 才需要 Redis。这个序列本身就是一种**风险递进式架构成熟度演进**——每一步都只引入上一阶段已经证明了的成熟度所必要的复杂性。

---

## 总结

| 维度 | 评估 |
|------|------|
| **文档质量** | 高。三个方向都提供了代码级证据和架构盲区论证，且互相独立不重叠 |
| **架构适配性** | 三个方向都能在现有分层架构中自然嵌入，不破坏层次依赖 |
| **技术债影响** | 中低。两个方向是纯新增，一个方向（多实例协调）需要改造三个组件 |
| **新基础设施** | 仅 Redis 一个可选项，PostgreSQL 已覆盖大部分场景 |
| **建议优先级** | P0 准入控制 + outbox 锁 → P1 变更历史 → P2 多实例协调 + Redis |

最终建议：**"先止血（P0），再补数据可观测性（P1），最后做分布式化（P2）"** 的顺序既控制了风险递进，又保证了每个阶段都有独立的交付价值。不引入不必要的复杂性，不为"可能需要"而提前引入 Redis。
