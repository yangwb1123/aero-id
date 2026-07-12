现在我已充分了解所有背景信息，以下是完整的架构分析报告。

---

# 架构师分析报告：aero-id 运营架构盲区

## 1. 架构评估

### 1.1 当前架构的优势

aero-id 的"四层分离 + 双协议"架构是**教科书级的单体清洁架构实现**，其优势值得首先确认：

- **严格的单向依赖**（handler → service → repository，storage 和 sso 为侧边依赖）为每一次架构演进保留了清晰的切割面。未来若需将某一领域（如 search）拆出为独立服务，service 接口已天然形成契约边界。
- **事务性 Outbox 模式**（ADR-003）是对写一致性的正确投资。当前 outbox 仅用于通知，但它已是事件驱动架构的基石——方向二（CQRS）直接复用此基础设施。
- **SSO 降级矩阵**（Degradation Matrix.md）的完备度令人印象深刻。38 个端点逐行标注「DB/SSO/S3 故障→行为→实现状态」是运营成熟度的黄金标准——大多数生产系统在 v0.x 阶段不具备此透明度。
- **Auth 与限流中间件的分离设计**（Auth 在 RateLimit 之前）是正确的顺序——从不浪费限流容量在非法请求上。

### 1.2 关键局限与架构债务

**局限 1：弹性机制已验证但未验证组合行为。** 这是报告方向三（混沌工程）的核心理由，但我想补充一点——当前风险不仅在于"组合行为未测试"，更在于**断路器阈值、重试次数、超时时间三者之间缺乏理性的数学关系**。举例：SSO 超时 5s + 断路器 5 次失败后打开 + cooldown 10s = 一个 SSO 慢响应（4.5s 持续 25s）会消耗所有 25s 窗口的 5×4.5s=22.5s 等待，导致 25 秒内所有认证请求排队。这是一个潜伏的性能放大器。

**局限 2：限流器是全局公平的——即全局不公平的。** 平铺的 token bucket（per-actor 单桶）使得一个用户在上传 50 个文件时，其消息发送也被限流。这不是"不同用户之间的不公平"（这通过 per-actor 桶已解决），而是"同一用户的不同操作之间的不公平"。方向一（QoS）定位准确。

**架构债务：套件 A vs. 套件 B（双实现）是最高优先级的技术债。** REVIEW_2026-06-30_ARCHITECTURE_DEEP.md 已经详细分析了这个问题。我强烈建议在启动方向一~五之前，先做出清除决定——无论选择方案 A（删 B 留 A）还是方案 B（完成重构），当前状态的运行时成本是：

- 每个新功能的开发成本 ×2（需要在两个套件中都实现）
- 代码审查的认知负载 ×2（审查者需要理解两套代码的差异）
- 潜在的回归风险（修改套件 B 的某些共享类型可能无意识影响套件 A）

**建议：先完成套件选择，再启动运营架构方向。** 估算 3-5 天。

**技术债：idempotency.RunCleaner 未启动。** 已实现但 app.go 中从未调用，导致 idempotency_keys 表的过期行永不清理。这是"一行代码修复"级别的问题，不应被忽视。

## 2. 扩展方向分析

### 2.1 方向一的深度评估：请求优先级与差异化 QoS

**为什么重要度可能比报告中评估的更高。**

报告给出了合理的 P1 优先级和 3-4 周估算。但我认为在真实生产场景中，这个方向的**实际影响范围比描述更广**——它不仅保护核心流量，它定义了系统的**伸缩行为边界**。

**建议纳入但不限于报告的"三层模型"：**

```
优先级队列不仅仅是限流的加强版——它是请求调度器的核心。
```

考虑以下未被报告覆盖的场景：

| 场景 | 当前行为 | 风险级别 |
|------|---------|---------|
| Outbox relay 轮询（每 5s）与用户 API 请求竞争 | 共享同一连接池 | 中——relay 是后台任务，不应影响交互式请求的延迟 |
| Storage GC sweeper 的 DB 查询与生产读写竞争 | sweeper 的 `SELECT ... FOR UPDATE` 可能阻塞写 | 中高——GC 期间写延迟可能飙升 |
| NotificationDispatcher 批量创建通知 | 大批量 INSERT 可能填满 WAL 缓冲区，影响其他写的提交延迟 | 中 |

**核心挑战：** ActorTier 信息的来源。报告提到"来自 SSO 或本地配置表"，但需要更仔细的设计决策。

**选项 A：SSO 返回用户等级（嵌入 ValidateToken 响应）。**
- 优势：单一事实源，等级变更即时生效。
- 劣势：需要修改 SSO proto 接口——如果 SSO 是外部团队维护，变更周期不可控。
- 适用场景：SSO 团队可控或已有 tier 字段。

**选项 B：aero-id 本地配置表 + 定期同步。**
- 优势：不依赖 SSO 改动，可独立迭代。
- 劣势：等级与 SSO 状态可能短暂不一致（例如用户从 premium 降级后，aero-id 的本地缓存最长 TTL 内仍视其为 premium）。
- 适用场景：SSO 不可变更或快速验证期。

**我的建议：先做选项 B（本地配置表 + 手动/同步），接口设计为可切换回选项 A。** 原因：QoS 是保护系统稳定性的机制，其有效性不应等待外部依赖的变更。本地"白名单/等级表"可以在第一时间投入使用，SSO 集成作为后续优化。

```yaml
# 配置示例（扩展报告中的方案）
rate_limit:
  tier_source: "local"  # 或 "sso"
  local_tiers:           # 仅当 tier_source=local 时生效
    "uid_abc123": "premium"
    "uid_def456": "premium"
  default_tier: "free"
  # ... 其余配置如报告所述
```

### 2.2 方向二的深度评估：读负载卸载与 CQRS

**这是五个方向中架构杠杆最高的，也是风险最高的。**

报告给出的三阶段演进路径（只读副本 → 物化视图 → 事件驱动读模型）是正确的。但需要指出一些在仅分析层面容易忽略的问题：

**Phase 1（只读副本）的关键陷阱：**

1. **副本延迟感知。** 当前 service 层不感知读一致性要求。`message.Send` 后立刻 `GET /v1/messages/inbox` 可能看不到新消息（因为副本延迟）。解决方案：引入 `ReadConsistency` 枚举：

```go
type ReadConsistency int8
const (
    ReadConsistencyStrong   ReadConsistency = 0  // 强制走 writer
    ReadConsistencyEventual ReadConsistency = 1  // 允许走 reader
)
```

但这意味着**每个 Query 都必须显式声明其一致性需求**——这是一个跨 38 个端点的改造。

2. **事务内的读操作。** 当前事务中若有读操作（如 `CreateComment` 后读 `reply_count`），这些读操作如果路由到 reader 副本可能读到过期数据。解决方案：事务内的所有读操作强制走 writer。

**Phase 2（物化视图）的取舍：**

选项 A：Postgres 原生 `MATERIALIZED VIEW` + `REFRESH MATERIALIZED VIEW CONCURRENTLY`。
- 优势：零代码变更——只在 SQL 层面操作。
- 劣势：非并发刷新时阻塞读；并发刷新有行级锁冲突风险；刷新频率受限于轮询间隔（即使 outbox 事件驱动，也受 relay 的 5s 轮询间隔约束）。
- 适用于：数据一致性要求不高的读模型（如用户列表、搜索索引）。

选项 B：应用层物化——由 outbox 事件处理器直接写入物化读模型表（如 `inbox_entries`）。
- 优势：写后立即可读（在 outbox 事件被消费后）；完全控制物化逻辑；可以处理跨表 JOIN。
- 劣势：需要额外的表维护；事件处理失败时的数据不一致风险（outbox 重试机制已覆盖）。
- 适用于：核心读路径（收件箱、通知列表、评论 feed）。

**我的建议：Phase 2 选 B（应用层物化），Phase 1 作为短期过渡。** 理由：应用层物化的延迟更低（不依赖 MV refresh 轮询），更匹配 aero-id 已有的 outbox 基础设施。

**Phase 3（事件驱动 CQRS）的复杂性被低估了。** 报告估算 4-8 周，我认为这是在对 outbox 基础设施完全了解的前提下——当前 outbox 只有一个消费者（NotificationDispatcher）。CQRS Phase 3 意味着：

- outbox 需要支持**多消费者组**（通知处理器 + N 个读模型更新器）
- 事件次序保证：如果 `MessageCreated` 事件必须在 `MessageDeleted` 事件之前被读模型消费，需要保证 at-least-once + 有序消费
- 实际估算应调整为 **6-10 周**

### 2.3 方向三的深度评估：混沌工程

**P2 优先级合理，但实施路径需更务实。**

报告列出了 5 个实验场景和期望行为。我认同这些场景的选择，但需要补充：

**核心挑战不在于"注入故障"，而在于"验证正确的系统行为"。** 混沌工程的真正价值不是制造混乱，而是**验证可观测性指标与预期行为之间的闭环**。

```
混沌实验 → 系统行为 → 指标变化 → 告警触发 → 自动恢复
            ↑                              ↓
            └──────── 验证这个闭环是否正确 ────┘
```

当前 aero-id 的可观测性（metrics、tracing、logging）已经就位，但**告警和自动恢复尚未建立**。这意味着混沌工程验证的前置条件是方向五（SLO 框架）的部分产出——至少需要：

- `aeroid_sso_errors_total` 告警规则
- `aeroid_outbox_oldest_age_seconds` 告警规则
- 断路器状态变化的告警

**建议将混沌工程拆为两个阶段：**

| 阶段 | 内容 | 前置条件 | 估算 |
|------|------|---------|------|
| Phase A | 实验框架 + 运维手册（文档化期望行为）| 方向五的告警规则就绪 | 2 周 |
| Phase B | 5 个实验的运行 + 结果分析 | Phase A + staging 环境 | 2 周 |

### 2.4 方向四的深度评估：审计异常检测

**P2 优先级合理，但有一个被忽略的关键前提：`audit_events` 表的规模管理。**

当前审计记录是 fire-and-forget（错误被忽略）。异常检测需要大量历史数据来建立基线。但如果没有**审计日志保留策略**，`audit_events` 表将在生产环境中无限增长——很快成为最大的表。

**建议将审计日志的 TTL/保留策略 作为方向四的前置任务：**

```yaml
audit:
  retention: 90d          # 90 天后自动归档/删除
  archive: "s3"           # 归档到 S3 而非直接删除
  anomaly_detection_window: 7d  # 异常检测只查询最近 7 天的数据
```

**规则引擎的设计取舍：**

**选项 A：滑动窗口 SQL 查询。**
- 每 N 分钟执行一次 `SELECT actor_uid, action, count(*) FROM audit_events WHERE created_at > now() - interval '5 minutes' GROUP BY actor_uid, action HAVING count(*) > threshold`。
- 优势：零基础设施——只依赖 Postgres。
- 劣势：`audit_events` 表增长后查询性能下降；高频规则（minute 级窗口）可能成为慢查询。
- 适用：1-5 条规则，日审计量 < 100 万条。

**选项 A 的 D 阶段：子查询优化**。
- 用 `audit_anomaly_counts` 物化表 + outbox 事件驱动更新（CQRS 同模式），避免全表扫描。

**选项 B：流处理引擎（如 Kafka + Flink/KSQL）。**
- 优势：亚秒级延迟；天然支持滑动窗口；可扩展至数百条规则。
- 劣势：Kafka 是高运维负担的依赖；对于 P2 优先级，引入新基础设施的 ROI 存疑。
- 适用：审计量 > 500 万条/天，规则 > 20 条。

**我的建议：选项 A（SQL 查询）起步。** 当审计量增长到影响性能时，过渡到 A 的 D 阶段（物化计数表）。流处理引擎是 over-engineering，至少在当前阶段如此。

### 2.5 方向五的深度评估：SLO 框架与自动伸缩

**报告将这个方向定位为 P2，但我认为它应该是 P1 与方向一同级。**

理由：当前可观测性基础设施（RED 指标、outbox lag gauges）已经就位但**未被消费**。SLO 框架是将指标从"数据"转化为"决策"的桥梁。没有 SLO，方向一（QoS）的 admission control 无法校准（你无法回答"什么时候该拒绝低优先级请求"），方向三（混沌工程）的断言无法量化。

**更重要的是，错误预算应为方向一提供校准信号：**

```
错误预算充足 → 宽松的 QoS 阈值（更多低优先级请求被放行）
错误预算紧张 → 收紧的 QoS 阈值（更少低优先级请求，更激进的 admission control）
```

这是一个**闭环控制系统**——没有 SLO 就没有校准闭环。

**伸缩信号的完整性需要考虑以下边界情况：**

| 信号 | 正确性条件 | 误报风险 | 缓解 |
|------|-----------|---------|------|
| P99 延迟 > 2× 基线 | 需要清晰的基线定义（过去 1h/24h/7d 的 P99？） | 流量形状变化（如上线新功能）可能暂时抬升基线 | 使用 EWMA 平滑基线，而非固定阈值 |
| CPU > 70% 持续 5 分钟 | CPU 可能因 GC（Go runtime）而周期性飙升 | 5 分钟窗口短于 Go GC cycle（如果内存大） | 窗口至少为 2× Go GC 周期 |
| Outbox 延迟 > 30s | 需要排除 scheduled downtime（如维护窗口） | 夜间低峰期延迟可能自然降低 | 仅在工作时间内触发告警 |

**建议的 SLO 定义层次（比报告更具体）：**

```
层 1：用户面 SLO（面向最终用户体验）
  ├── message.send P99 延迟 < 500ms
  ├── file.upload P99 延迟 < 5s（≥10MB 文件放宽至 10s）
  ├── notification.delivery P95 延迟 < 60s
  └── 所有端点可用性 > 99.9%

层 2：运营面 SLO（面向运维团队）
  ├── SSO gRPC P99 延迟 < 1s
  ├── outbox 事件延迟 P99 < 30s
  ├── 存储 P99 延迟 < 200ms（本地）/ < 500ms（S3）
  └── DB 连接池利用率 < 80%
```

## 3. 接口设计建议

### 3.1 关键模块的接口设计原则

**限流器的接口重构（方向一的核心）**

当前 `Allow()` 的问题是签名过于简单：`func (l *Limiter) Allow(key string) bool`。方向一需要一个更富表达的接口：

```go
// 当前（过于简单）
func (l *Limiter) Allow(key string) bool

// 建议重构为
type RateLimitRequest struct {
    ActorUID   string
    Tier       ActorTier    // free | premium | admin
    Endpoint   string       // "/v1/messages" | "/v1/files/upload"
    Priority   RequestPriority
    BodySize   int64        // 可选：用于 IO 密集型端点的加权计算
}

type RateLimitResult struct {
    Allowed    bool
    RetryAfter time.Duration
    Reason     string       // "rate_limited", "priority_denied", "admission_control"
    Limit      int          // 当前配额上限
    Remaining  int
    ResetAt    time.Time
}

func (l *PriorityLimiter) Allow(ctx context.Context, req RateLimitRequest) RateLimitResult
```

**设计原则：显式优于隐式。** 每个限流决策上下文（actor、endpoint、priority）应显式传入，而非从 context 隐式提取。这使得限流的可测试性和可审计性大幅提升。

### 3.2 是否需要引入新的抽象层

**是的，三个新抽象层：**

**① `internal/ratelimit` 包层级提升**

当前 `internal/pkg/ratelimit` 是一个底层 token bucket 实现。方向一需要将其扩展为**限流调度器**——它不仅管理 token，还管理优先级队列、压力感知、层级回退。建议：

```
internal/
  ratelimit/              # 新包：限流调度器
    priority.go           # Priority enum + ActorTier
    limiter.go            # PriorityLimiter 主接口
    tier.go               # 用户等级配置
    admission.go          # 压力感知 admission controller
    endpoint.go           # 端点级权重配置
  pkg/ratelimit/          # 保留作为底层 token bucket
    bucket.go             # 核心 token bucket 算法
```

**② 读路由抽象层**

```go
// internal/pkg/db/router.go
type Router interface {
    Writer(ctx context.Context) *gorm.DB
    Reader(ctx context.Context, consistency ReadConsistency) *gorm.DB
}
```

每个 repository 方法调用 `db.Writer()` 或 `db.Reader()`，而非直接使用 `*gorm.DB`。这使得 Phase 1→Phase 2→Phase 3 的演进可在 repository 层面无感知完成。

**③ 错误预算/SLO 计算层**

```go
// internal/slo/
type ServiceSLO struct {
    Name           string
    TargetLatency  time.Duration
    TargetErrorRate float64
    Window         time.Duration
}
type ErrorBudget struct { /* 见报告 */ }
func (r *Registry) ComputeBudget(ctx context.Context, slo ServiceSLO) (*ErrorBudget, error)
```

这一层应独立于 `internal/pkg/metrics`——不修改已有的 metrics 采集代码，而是在其之上添加 SLO 计算逻辑。

### 3.3 向后兼容性策略

所有五个方向的核心原则：**新增配置和接口，不改动已有接口签名。**

| 方向 | 兼容性策略 |
|------|-----------|
| QoS | `ratelimit.Allow()` 保留原有签名（仅扩展出新的 `AllowWithPriority`）。默认配置下行为不变（所有请求 `PriorityNormal`） |
| CQRS | `DB` 类型扩展 `Reader()/Writer()` 方法，但保持 `DB.DB()` 返回 writer（向后兼容）。新的 repository 方法使用新 API，旧方法过渡期继续工作 |
| 混沌工程 | 完全隔离——实验框架不修改任何生产代码路径 |
| 审计分析 | `audit.Log()` 签名不变。分析层为只读消费者 |
| SLO/伸缩 | metrics 注册表扩展新方法，不修改已有方法 |

## 4. 技术选型

### 4.1 是否需要新基础设施

| 方向 | 所需基础设施 | 建议 |
|------|-------------|------|
| QoS | 无（纯进程内限流器改造） | ❌ 不需要新基础设施 |
| CQRS Phase 1 | Postgres 流复制/Read Replica | ✅ 需要（云厂商 RDS 原生支持） |
| CQRS Phase 2-3 | 无（应用层物化，用现有 Postgres） | ❌ 不需要新基础设施 |
| 混沌工程 | Staging/类生产环境 + 故障注入工具 | ✅ 需要（Kubernetes Chaos Mesh 或 tc + stress-ng） |
| 审计分析 | 无（SQL 规则引擎，用现有 Postgres） | ❌ 不需要新基础设施 |
| SLO/伸缩 | Prometheus + HPA | ✅ 需要（但 Prometheus 已有部署） |

**关键决策：是否引入 Kafka 或其他消息队列？**

我不建议引入。理由：
- 当前 outbox 基于 Postgres 轮询，5s 延迟对于通知和读模型更新来说是可接受的。
- aero-id 的规模在可预见的未来不会超过 Postgres outbox 的处理能力（单个 outbox relay 可处理数千 TPS）。
- Kafka 引入的运维复杂度（ZooKeeper/KRaft、topic 管理、消费组 rebalance）远高于它解决的问题。
- **如果未来需要，outbox 的接口抽象使得迁移到 Kafka 成为可选项而非必须项。**

### 4.2 第三方依赖评估

| 候选依赖 | 用途 | 评估 | 建议 |
|---------|------|------|------|
| `uber-go/ratelimit` | 高级限流器（漏桶算法） | 与现有 token bucket 互补 | ❌ 不需要——Go 标准库 `golang.org/x/time/rate` 已覆盖，只需在其上包装优先级逻辑 |
| `redis` | 分布式限流计数器 | 需要 Redis 运行实例 | ❌ 不需要——当前限流是单进程的，未来如果需要分布式限流可以引入，但那是独立的架构方向 |
| `sony/gobreaker` | 断路器（SSO 已有） | 用于混沌工程实验？ | ❌ 不需要——SSO client 已有断路器，混沌工程只需要验证它 |
| Chaos Mesh | Kubernetes 混沌注入 | 需要 K8s 集群 | ⚠️ 建议评估期——如果不在 K8s 上，用 `tc` + `pkill` 即可 |
| `prometheus/client_golang` | 指标采集（已依赖） | SLO 计算需要增加新的 metric 类型 | ✅ 已在依赖中，扩展使用即可 |
| `git-cliff` | CHANGELOG 自动化 | 独立 CLI，非 Go 依赖 | ✅ 建议采用 |
| `goreleaser` | 发布自动化 | 独立 CLI，非 Go 依赖 | ✅ 建议采用 |

**总体评估：五个方向不需要引入新的 Go 第三方依赖。** 所有能力都可以基于现有依赖（`golang.org/x/time/rate`、`prometheus/client_golang`、标准库 `container/heap` 用于优先级队列）实现。

### 4.3 自建 vs 采购的决策框架

对于运营架构方向，我的原则是：

```
自建的条件（同时满足）：
  1. 核心差异化竞争力（QoS 策略、SLO 模型）
  2. 开源方案不匹配需求
  3. 可控的复杂度（不引入新基础设施）

采购/集成的条件（满足任一）：
  1. 基础设施层（Prometheus、K8s HPA、Postgres 副本）
  2. 运维工具（Chaos Mesh、git-cliff、goreleaser）
  3. 不是核心差异化竞争力
```

按照这个框架：

| 组件 | 类别 | 策略 |
|------|------|------|
| 优先级限流器 | 差异化 | **自建**——在现有 token bucket 上包装优先级逻辑 |
| 读一致性路由 | 差异化 | **自建**——`Router` 接口 + repository 改造 |
| 物化读模型 | 差异化 | **自建**——基于 outbox 的事件处理器 |
| 混沌实验框架 | 运维工具 | **集成**——Chaos Mesh（K8s）或 tc（传统部署） |
| 审计异常检测规则 | 差异化 | **自建**——SQL 查询规则引擎 + 滑动窗口 |
| SLO 错误预算计算 | 差异化 | **自建**——基于 Prometheus metrics 的计算层 |
| CHANGELOG 自动化 | 运维工具 | **集成**——git-cliff |
| 发布管线 | 运维工具 | **集成**——goreleaser |

## 5. 实施路线图

### 5.1 优先级排序（修订版）

我基本同意报告推荐的执行顺序，但做以下调整：

```
原推荐：QoS → CQRS → 混沌工程 → 审计分析 → SLO/伸缩
修订版：SLO基础→ QoS → CQRS Phase 1 → 混沌工程 Phase A → CQRS Phase 2-3 → 审计分析 → 混沌工程 Phase B → SLO/伸缩完善
```

**关键修订理由：**

1. **SLO 基础前移至第一**——不是完整的 SLO 框架，而是"错误预算定义 + 关键指标告警规则"。这是方向三（混沌验证的必要前提）和方向一（admission control 校准的必要前提）的共同基础。
2. **CQRS Phase 1（只读副本）提前至 QoS 之后立即执行**——因为它是成本最低、收益最高（连接池压力即时缓解）的架构改进。
3. **混沌工程拆为两阶段**——Phase A（框架 + 文档化）在 CQRS Phase 1 之后，Phase B（实际实验）在 CQRS Phase 3 之后。

### 5.2 阶段划分与里程碑

```
M0：基础就绪（2 周）
  ├── 完成套件 A/B 选择（3-5天）
  ├── 修复 idempotency.RunCleaner（1天）
  ├── 定义 5 个用户面 SLO + 5 个运营面 SLO 指标（2天）
  ├── 配置 Prometheus 告警规则（3天）
  └── gRPC RequestID 拦截器（REVIEW_INFRA 建议，2天）

M1：流量保护（3-4 周）
  ├── PriorityLimiter 实现（2周）
  │   ├── Priority + ActorTier 枚举定义
  │   ├── 端点级权重配置
  │   ├── 三层限流器（全局→端点→QoS）
  │   ├── 本地 ActorTier 配置表（选项 B）
  │   └── admission control（CPU + 延迟阈值）
  ├── 配置化限流规则（1周）
  └── 端到端测试 + 基准（0.5周）

M2：读路径卸载（2-4 周，与 M1 可部分并行）
  ├── DB.Router 接口引入（3天）
  ├── repository 方法标记 ReadConsistency（1周）
  ├── Postgres 只读副本配置（3天，基础设施团队）
  └── 验证副本延迟在可接受范围内（2天）

M3：弹性文档化（2 周，与 M2 并行）
  ├── 混沌实验框架代码（实验定义 + 故障注入器接口）
  ├── 5 个实验的详细文档（期望行为 + 验证步骤）
  └── Staging 环境故障注入工具链配置

M4：事件驱动读模型（4-6 周）
  ├── outbox 多消费者支持（1周）
  ├── 收件箱读模型物化（inbox_entries 表 + 事件处理器，2周）
  ├── 通知列表读模型物化（2周）
  └── 性能基准：P99 延迟下降验证（0.5周）

M5：安全分析（3 周，与 M4 可部分并行）
  ├── 审计保留策略 + 归档（1周）
  ├── 5 条异常检测规则实现（1.5周）
  └── 管理面板 + 告警输出（0.5周）

M6：混沌验证 + 运营闭环（3-4 周）
  ├── 5 个混沌实验在 Staging 环境运行（2周）
  ├── 结果分析 + 弹性加固迭代（1周）
  ├── 完整 SLO 仪表盘 + 错误预算可视化（0.5周）
  └── HPA 配置 + 自动伸缩验证（0.5周）
```

**总计：约 17-23 周（4-6 个月）。**

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **SSO ActorTier 不可用**（选项 A 依赖外部团队） | 中 | 中 | 采用选项 B（本地配置表），接口设计为可切换 |
| **Postgres 只读副本延迟 > 100ms** | 低 | 高 | Phase 1 前置——先验证副本延迟；如果延迟不可接受，降级为同步复制 |
| **outbox 多消费者导致事件顺序混乱** | 中 | 中 | 为每个消费者分配独立的 outbox 表或使用 version vector 检测冲突 |
| **混沌实验导致 Staging 环境不可用** | 中 | 低 | 在非工作时间运行实验；确保实验有自动回退（timeout-based） |
| **审计表增长超出预期** | 中 | 中 | 前置 retention 策略；使用表分区（按时间） |
| **SLO 告警疲劳**（太多告警导致团队忽略真正重要的告警）| 高 | 高 | 严格遵守"每 SLO 最多 2 个告警（燃烧率告警 + 预算耗尽告警）"的原则 |
| **QoS 优先级分类争议**（团队争论"哪个端点该有高优先级"）| 高 | 中 | 不做完美分类——先做简单分类（核心 3 个端点高优先级，其余默认），用数据迭代优化 |

### 5.4 与现有 ROADMAP 的衔接

当前 ROADMAP 的未交付方向（P2-3 负载测试套件、WebSocket/SSE 实时推送）不会与以上五个方向冲突。实际上：

- **负载测试套件（P2-3）** 是方向三（混沌工程）的自然前驱——混沌实验需要基准性能数据作为"正常"基线。
- **WebSocket/SSE（P1-1）** 与方向二（CQRS）的物化读模型互补——读模型提供优化的数据，WebSocket 推送提供实时传输通道。

**建议：在 M5-M6 之间（即审计分析和混沌工程之后）评估是否插入 ROADMAP 的剩余方向。**

---

## 总结

这份交叉分析报告是当前阶段最有价值的文档——它不是在"添加功能"的维度上扩展，而是在**系统运营成熟度**的维度上推动 aero-id 从"功能完备"到"运营卓越"的跃迁。五个方向之间的依赖关系清晰，每一条都有完整的代码级证据链支持。

我的核心建议只有两条：

1. **不要跳过前置清理**——在启动任何方向之前，必须解决套件 A/B 的双实现问题和 idempotency 的孤儿代码。这是基础卫生。
2. **SLO 不是第五步**——它是正确的**第零步**（定义基线）和**跨所有方向的校准器**。建议将"SLO 基础"（错误预算定义 + 关键告警）作为一个独立的前置步骤，而非等到最后再处理。

如果只选一个方向投入（假设资源受限），我选择方向一（QoS）。理由：它是唯一一个**直接防止生产故障**的机制——在系统过载时保护核心路径。其他方向都是在"系统已经稳定"的前提下提升效率或验证正确性。
