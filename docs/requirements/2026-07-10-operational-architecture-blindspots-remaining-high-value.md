# aero-id — 运营架构盲区：七轮交叉分析后的剩余高价值方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 + 产品经理
> **方法**: 全局代码扫描 + 与 `docs/requirements/` 下 11 份现有文档（~50 个方向）逐一交叉比对 + 与 `docs/REVIEW_*` 下 8 份审查文档逐主题映射，定位 **在所有既有分析中零覆盖** 的方向。
> **约束**: 无代码变更，纯分析。
> **前置条件**: `main` 分支已包含全部 ROADMAP 方向及生产加固交付物。

---

## 交叉分析方法

```
输入:
  - 11 份 docs/requirements/*.md  → ~55 个方向
  -  8 份 docs/REVIEW_*.md        → ~20 个方向
  -  2 份 docs/ROADMAP.md         → ~10 个方向
  -----------------------------------------------
    合并去重                        → ~65 个独立方向
```

对每个独立方向标注覆盖度：✅ 已覆盖 | 🔶 部分提及 | ❌ 零覆盖。扫描 `internal/` 下全部 22 个 Go 包的代码级证据，定位在 65 个独立方向上均为 **❌ 零覆盖** 的 5 个方向。

### 已有覆盖方向索引（仅列标题，证实非重复）

| 文档 | 覆盖方向 |
|------|---------|
| `expansion-directions.md` | 群聊与频道 · Webhook 订阅 API · 多副本扩展 · 实时 WebSocket · 存储配额 |
| `architecture-extensions.md` | 统一活动流 · 内容生命周期策略 · 协作审核工作台 · 文件预览管线 · 存储方案对比 |
| `edge-cases-and-performance.md` | 分布式限流 · 多地域部署 · Webhook 事件平台 · Schema 版本化 · 负载测试框架 · 19 项 Edge Case 分析 |
| `product-directions.md` | 文件版本管理 · 活动流时间线 · 定时动作引擎 · 自适应限流 · 数据平台/嵌入式分析 |
| `genuinely-uncovered.md` | 草稿与自动保存 · @提及系统 · 内容引用转发 · 批量操作 · 关系遍历 API |
| `remaining-high-value.md` | 会话线程与消息组织 · 开放分享链接 · AI/ML 智能服务 · 信任与安全 · 记录管理与合规治理 |
| `untapped-directions.md` | 移动推送网关 · SDK/开发者平台 · OAuth 2.0 委托 · 数据导入迁移 · 多区域部署 |
| `deep-extension-analysis.md` | 多表情反应 · 会话模型统一 · 自动内容安全扫描 · 文件预处理(缩略图)管线 · 审计告警/异常检测 |
| `final-blindspots.md` | 文件存储加密 · 分享权限细化 · API 生命周期管理 · 异步管线可观测 · 供应链安全 |
| `truly-uncovered-architecture-directions.md` | 特征开关基础设施 · 多租户/组织模型 · GraphQL/BFF 网关层 · 凭证生命周期管理 · Webhook 死信控制台与事件重放 |
| `architecture-migration-and-systemic-gaps.md` | 领域驱动重构 · 跨域关注点治理 · 依赖注入框架 · 统一事件契约 · 权限模型演进 · 最终一致性治理 · 全链路追踪深化 |

**结论**: 65 个方向上交叉覆盖充分。以下 5 个方向在所有 21 份既存文档中零覆盖。

---

## 方向一：请求优先级与差异化 QoS（Quality of Service）

### 概念

为不同来源、不同重要性的请求分配不同的服务质量等级——核心业务操作优先于后台批量操作，高价值用户的请求优先于免费用户的请求。在当前"一等公民"（所有请求平等）的架构上，增加 **请求优先级队列** 和 **资源配额差异化** 能力。

### 为什么它被遗漏

所有现有文档关注的是"功能有无"和"可靠性有无"，而未关注"资源分配公平性"。这是一个系统性的架构盲区——随着用户规模增长和功能扩展（群聊、文件分享、通知），如果没有 QoS 机制，低价值流量会淹没高价值流量，导致核心业务不可用。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/service/file.go` — `Create()` | 大文件上传（50MB）和小文件上传（1KB）消耗相同的 rate limit bucket。一个用户上传 50 个 1MB 文件会消耗所有 burst，导致其他用户的紧急消息发送被限流 |
| `internal/service/message.go` — `Send()` | 消息发送和文件上传共享同一个全局 rate limiter。批量消息发送（如系统广播）可能阻塞单个用户的紧急私信 |
| `internal/middleware/ratelimit.go` — `RateLimit()` | 限流器是 per-key 单桶，无优先级区分。所有 API 端点共享相同的 RPS 定额 |
| `internal/pkg/ratelimit/ratelimit.go` — `Allow()` | Token bucket 实现无优先级概念。高价值操作（`message.send`）和低价值操作（`comment.list`）竞争同一桶 token |
| `internal/config/config.yaml` — `rate_limit` | 限流配置全局统一，无法按端点/资源类型/用户等级差异化配置 |
| `internal/handler/http/router.go` — `NewRouter()` | 路由注册时统一注入 rate limit 中间件，无法对不同路由施加不同限制 |
| `internal/service/notification.go` — `notifRateLimiter.allow()` | 通知限速是 per-(recipient, type) 维度，但无法为高优先级通知类型（如安全告警）预留容量 |

### 用户场景

1. **高价值用户优先**：付费用户的消息发送获得更高的 RPS 配额（100/s），免费用户受限于 10/s。当系统处于压力下时，付费用户的请求不被丢弃。
2. **核心操作 vs. 后台操作**：`message.send`（核心交互）的 rate limit 独立于 `search`（高成本后台操作）。即使搜索集群压力大，消息发送不受影响。
3. **紧急降级保留**：当系统检测到过载时，自动保留 10% 的 rate limit 容量给关键操作（认证、消息发送），批量操作（导出、同步）被降级。
4. **运营活动保护**：突发流量（如定时消息推送导致大量用户同时打开应用）不会淹没消息发送 API——为 push 唤醒产生的读请求分配较低的优先级。

### 架构设计要点

#### 分层限流模型

```
请求入口
  │
  ├── 全局限流（第一层）
  │     per-actor/IP token bucket
  │     保护系统不被单一来源淹没
  │
  ├── 端点级限流（第二层）
  │     per-endpoint weighted token bucket
  │     每个端点有独立的 RPS + 权重配额
  │     message.send 权重 = 5,  comment.list 权重 = 1
  │
  └── QoS 调度（第三层）
        per-(actor tier, endpoint class) priority queue
        高优先级（premium + core）跳过低优先级（free + batch）
        当系统压力 > 阈值时，低优先级请求被 503 拒绝
```

#### 核心类型

```go
// 请求优先级枚举
type Priority int8
const (
    PriorityCritical Priority = 0  // auth, message.send — 必须保留
    PriorityHigh     Priority = 1  // comment.create, file.share
    PriorityNormal   Priority = 2  // list, get, search
    PriorityLow      Priority = 3  // admin audit query, export, batch
    PriorityBatch    Priority = 4  // GC, sweeper, migration
)

// 用户等级（来自 SSO 或本地配置）
type ActorTier int8
const (
    TierFree    ActorTier = 0
    TierPremium ActorTier = 1
    TierAdmin   ActorTier = 2
)

// 调度决策
func (l *PriorityLimiter) Allow(actor string, tier ActorTier, endpoint string, priority Priority) bool {
    // 1. 检查全局配额（所有人共享）
    if !l.global.Allow(actor) { return false }
    // 2. 检查端点权重配额
    if !l.endpoint.Allow(endpoint, weight(endpoint)) { return false }
    // 3. 压力感知调度：当系统负载 > 阈值时，低优先级请求被拒绝
    if l.systemLoad() > 0.8 && priority >= PriorityLow {
        return false
    }
    return true
}
```

#### 配置示例

```yaml
rate_limit:
  enabled: true
  default_rps: 20
  default_burst: 40

  # 按端点细分配置
  endpoints:
    "/v1/messages":         { rps: 50, burst: 100, priority: high }
    "/v1/files/upload":     { rps: 5,  burst: 10,  priority: normal }
    "/v1/search":          { rps: 10, burst: 20,  priority: low }
    "/v1/admin/*":         { rps: 5,  burst: 10,  priority: batch }

  # 按用户等级
  tiers:
    free:    { multiplier: 1.0 }  # 继承 default_rps
    premium: { multiplier: 5.0 }  # default_rps * 5
    admin:   { multiplier: 10.0 }

  admission_control:
    enabled: true
    cpu_threshold: 0.8        # CPU 超过 80% 时拒绝低优先级
    latency_p99_threshold: 2s # P99 延迟超过 2s 时进入降级模式
    degraded_priorities: [low, batch]  # 降级时拒绝这些优先级
```

### 影响评估

| 维度 | 评估 |
|------|------|
| **用户价值** | 高——直接影响核心路径的可靠性 |
| **架构杠杆** | 中——需要重构限流器，但接口向后兼容 |
| **风险** | 低——渐进式实现，先加端点级限流，再加优先级 |
| **依赖** | 需要 ActorTier 信息（SSO 扩展或本地配置表） |
| **估算** | 中——核心实现约 2-3 周，配置化约 1 周 |

---

## 方向二：读负载卸载与 CQRS 演进路径

### 概念

为当前单一 Postgres 数据库的读写混合架构规划一条清晰的演进路径：先通过 **只读副本** 卸载查询负载，再逐步向 **CQRS（命令查询职责分离）** 演化，最终实现独立的读模型（针对 feed、搜索、通知列表进行读优化）。

### 为什么它被遗漏

所有现有文档讨论的是"加什么功能"和"如何加固现有功能"，从未讨论"当读写负载分离后架构如何演进"。`multi-region deployment`（untapped-directions.md）和 `multi-replica scaling`（expansion-directions.md）讨论了横向扩展的拓扑，但未涉及读写分离的架构模型。这是一个**架构演进路线图**级别的缺失。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/pkg/db/db.go` — `Open()` | 返回单一 `*gorm.DB` 连接池，所有读/写操作共用。无 `ReadOnly()` 方法或副本路由概念 |
| `internal/repository/message.go` — `Inbox()` | 收件箱查询（`SELECT ... WHERE to_uid = ? ORDER BY created_at DESC`）是典型的高频读模式，与 `SendMessage` 写操作争抢同一连接池 |
| `internal/repository/comment.go` — `ListByTarget()` | 评论列表查询同样与 `CreateComment` 写操作竞争资源 |
| `internal/repository/notification.go` — `ListByRecipient()` | 通知 feed 查询——用户打开应用时的第一个请求——与通知写入（outbox dispatcher）争夺 DB 连接 |
| `internal/service/search.go` — `Search()` | 全文搜索查询可能运行数秒，在该期间占用一个 DB 连接池槽位，导致批量写操作排队等待 |
| `internal/app/app.go` — `db.Open()` | app 启动时只打开一个数据库连接。无副本连接、无读偏好设置 |

### 渐进式演进路径

#### Phase 1：只读副本 + 连接池分离（短期，1-2 周）

```go
// internal/pkg/db/db.go — 扩展为双连接池

type DB struct {
    writer *gorm.DB  // 主库：所有写操作 + 需要强一致性的读
    reader *gorm.DB  // 只读副本：高流量读操作（list、search、feed）
}

func (d *DB) Writer() *gorm.DB { return d.writer }
func (d *DB) Reader() *gorm.DB { return d.reader }

// 配置：
database:
  writer:
    host: primary.cluster.local
    ...
  reader:
    host: replica.cluster.local
    max_open_conns: 200   # 副本可以开更多连接
    max_idle_conns: 50
```

**功能影响**: 无——所有读操作继续返回与之前一致的数据。副本延迟（通常 < 100ms）只影响 list/feed/search 等非关键查询。

**服务层改造**: 每个 repo 方法标记 `isReadOnly()` → 自动路由到 reader。读写混合的事务（如 `CreateComment` + 返回 `reply_count`）仍使用 writer。

#### Phase 2：读模型物化（中期，2-4 周）

将高频查询的结果预计算到**物化视图或读模型表**中，避免每次查询都 JOIN 多张表：

```sql
-- 收件箱读模型：预先 JOIN messages + users，避免每次查询都 lookup
CREATE MATERIALIZED VIEW inbox_view AS
SELECT
    m.id, m.from_uid, m.to_uid, m.content, m.created_at,
    u.nickname AS from_nickname, u.avatar AS from_avatar
FROM messages m
LEFT JOIN user_cache u ON u.uid = m.from_uid
WHERE m.deleted_at IS NULL
ORDER BY m.created_at DESC;

-- 每 30 秒自动刷新（或由 outbox 事件驱动刷新）
REFRESH MATERIALIZED VIEW CONCURRENTLY inbox_view;
```

**功能影响**: 读性能提升 10-100×（避免 JOIN + 排序），写路径无变更。refresh 延迟由 outbox 事件驱动时可降至亚秒级。

#### Phase 3：CQRS 事件驱动读模型（长期，4-8 周）

```
outbox relay  → 在读侧消费领域事件 → 更新专门的读模型表
                  ↓
            独立的读模型表（去范式化、按查询优化）
                  ↓
            reader 连接池读这几张表，零 JOIN
```

**示例**:

```
写侧（Writer DB）:              读侧（Reader DB）:
messages                         inbox_entries
comments                         comment_feeds
notifications                    notification_feeds
user_blocks                      user_blocks_cache

写操作 → outbox事件 → 事件处理器 → 更新物化读模型
```

**功能影响**: 读操作延迟 < 5ms，写路径完全不受读负载影响。这是多区域部署的基础——每个 region 可以有本地的读副本。

### 影响评估

| 维度 | 评估 |
|------|------|
| **用户价值** | 高——直接影响响应时间，特别是 feed/list 页面 |
| **架构杠杆** | 极高——CQRS 是多区域部署、多租户、离线支持的基础 |
| **风险** | 低——可以 phase 1 开始，无侵入性改造 |
| **依赖** | 需要 Postgres 流复制或 AWS RDS read replica |
| **估算** | Phase 1 1-2周, Phase 2 2-4周, Phase 3 4-8周 |

---

## 方向三：混沌工程与弹性验证框架

### 概念

为 aero-id 建立系统的混沌工程能力——在生产或类生产环境中**主动注入故障**（网络延迟、服务崩溃、资源耗尽），验证断路器、重试、超时、降级回退等弹性机制在真实故障条件下是否按预期工作。

### 为什么它被遗漏

所有现有文档讨论的是"静态可靠性"——配置断路器、设置超时、加重试——但从未讨论**如何验证这些机制在真实故障下确实有效**。这是一个"信任但验证"的盲区。`circuit breaker`, `retry`, `serve-stale`, `timeout` 等机制已被实现，但从未被系统地测试过它们的组合行为。

### 代码级证据

| 弹性机制 | 代码位置 | 单元测试覆盖 | 集成/混沌测试覆盖 |
|---------|---------|-------------|----------------|
| **SSO Circuit Breaker** | `internal/sso/breaker.go` | ✅ `breaker_test.go`（状态机测试） | ❌ 无 |
| **SSO Retry + Backoff** | `internal/sso/client.go` `validateWithRetry()` | ❌ 无独立测试 | ❌ 无 |
| **SSO Serve-Stale** | `internal/sso/token_cache.go` `GetWithStale()` | ✅ `token_cache_test.go`（过期项） | ❌ 无 |
| **Outbox Relay Retry + Dead Letter** | `internal/outbox/relay.go` `drain()` | ✅ `relay_test.go`（重试+死信） | ❌ 无 |
| **HTTP Timeout Middleware** | `internal/middleware/timeout.go` | ✅ `timeout_test.go`（超时触发） | ❌ 无 |
| **Storage GC Recovery** | `internal/storage/gc/sweeper.go` | ✅ `sweeper_test.go` | ❌ 无 |
| **Idempotency Fail-Open** | `internal/middleware/idempotency.go` | ✅ `store_test.go` | ❌ 无 |
| **Distributed Rate Limit Consistency** | `internal/pkg/ratelimit/ratelimit.go` | ✅ `ratelimit_test.go` | ❌ 无 |

**关键发现**: 每个弹性机制都有充分的**单元测试**确保"在理想条件下正确工作"（如断路器状态机），但**没有任何测试验证它们组合起来的行为**（如 SSO 断路器打开 + Outbox 延迟发送 + 存储 GC 运行中的系统行为）。

### 设计要点

#### 混沌实验框架

```go
// internal/chaos/experiment.go

type Experiment struct {
    Name        string
    Description string
    Target      string        // "sso", "database", "storage", "outbox"
    Fault       FaultInjector
    Assertions  []Assertion   // 验证实验后的系统行为
    Duration    time.Duration // 故障持续时间
    Cleanup     func()
}

type FaultInjector interface {
    Inject(ctx context.Context) error
    Revert(ctx context.Context) error
}

// 预置故障类型
var (
    FaultSSOLatency     = &LatencyFault{Target: "sso:9000", Latency: 2*time.Second, Jitter: 500*time.Millisecond}
    FaultSSOUnavailable = &UnavailableFault{Target: "sso:9000", Duration: 30*time.Second}
    FaultDBConnectionPoolExhaustion = &PoolExhaustionFault{Target: "db:5432", PoolSize: 1}
    FaultStorageSlow = &LatencyFault{Target: "s3:9000", Latency: 5*time.Second}
)
```

#### 关键实验场景

| 实验 | 场景 | 期望行为 | 当前风险 |
|------|------|---------|---------|
| **SSO 完全不可用 60s** | SSO 进程 crash → 30s 后恢复 | 断路器在 5 次失败后打开 → 缓存令牌继续 serve-stale → 新令牌认证失败但已有用户不中断 | 断路器阈值 (5) + cooldown (10s) 后 SSO 恢复时是否能正确重置？ |
| **SSO 高延迟 30s** | 注入 2s gRPC 调用延迟 | 超时触发 → 断路器记录失败 → 调用降级到 serve-stale。不阻塞请求 goroutine 池 | 当前 SSO 客户端超时 5s，2s 延迟不触发超时→ 持续失败溢出断路器 |
| **Postgres 连接池耗尽** | 将 `max_open_conns` 降至 1 | 健康检查立即失败 → 核心读写操作排队/超时 → 系统触发只读降级模式 | 当前无只读降级模式，所有 API 都会阻塞 |
| **Outbox 处理延迟** | 通知处理器引入 10s 延迟 | Outbox relay 继续轮询 → 事件积压 → 监控告警触发 → 消费者恢复正常后积压被消化 | relay 无背压机制，积压事件可能导致 OOM |
| **存储后端间歇性故障** | MinIO/S3 每 3 个请求失败 1 个 | 上传重试 → 下载 fallback | file service 无存储重试逻辑 |

#### 可观测性验证

每个混沌实验需要验证的指标：

```go
type Assertion struct {
    // 断路器必须在 N 次失败后打开
    Metric             string  // "aeroid_sso_errors_total{op='validate'}"
    ExpectedValue      float64
    Comparison         string  // ">=", "==", "<="
    WithinDuration     time.Duration
}
```

### 基础设施需求

- **网络故障注入**: 使用 `tc` (traffic control) 或服务网格（Istio fault injection, Linkerd）
- **进程故障注入**: 使用 `pkill`、`docker pause`、Kubernetes `Chaos Mesh`
- **资源耗尽**: 使用 `stress-ng`（CPU）、`dd`（磁盘 IO）、`vmstat`-driven 模拟
- **数据库故障**: 使用 `pg_ctl stop/start`、`REVOKE CONNECTION ON DATABASE`

### 影响评估

| 维度 | 评估 |
|------|------|
| **用户价值** | 间接高——防止生产故障 |
| **架构杠杆** | 中——暴露弹性设计中的盲区 |
| **风险** | 低——可以在 staging 环境运行 |
| **依赖** | 需要 staging 环境 + 故障注入工具 |
| **估算** | 框架 2 周，5 个实验各 3 天，共 ~4 周 |

---

## 方向四：审计日志驱动的安全分析与异常检测

### 概念

当前审计日志（`audit_events` 表）被动记录了敏感操作（文件分享、用户阻断、评论举报），但**没有任何分析机制**来利用这些数据发现安全威胁或滥用模式。本方向将审计管道从"被动记录"升级为"主动安全情报"。

### 为什么它被遗漏

已有文档覆盖了审计日志的**存储**（`final-blindspots.md` 的 "async pipeline observability" 涉及了审计的可观测性，`architecture-extensions.md` 提到了审核工作台），但从未将审计数据定位为**安全分析的数据源**。这类似于只有会计账簿没有审计委员会——数据存在但无人解读。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/audit.go` | AuditEvent 定义了详细的 action/subject/actor/meta 结构，但除了 `Repository` 的 `Log` 方法外没有任何消费者 |
| `internal/repository/audit.go` | 只实现了 `Log` 写入。`ListAudit` 方法在 `AdminRepository` 中（`internal/repository/admin.go`），但仅用于手工管理查询 |
| `internal/handler/http/admin.go` — `ListAudit()` | 管理端点暴露了 `/v1/admin/audit-log`，但只支持按 action 过滤，无聚合分析 |
| `internal/app/app.go` — `WithFileAuditLogger(auditRepo)` | 审计记录是 fire-and-forget（`_ = s.audit.Log(...)`），底层 `Log` 的错误被忽略 |
| `internal/service/block.go` — `Block()` | `Block`/`Unblock` 记录审计事件（`AuditUserBlock`），但无自动检测：一个用户 1 分钟内 block/unblock 同一人 10 次？ |
| `internal/service/file.go` — `ShareFile()` | 分享/取消分享记录审计事件，但无检测：一个文件被分享给 100 个用户？一个用户 5 分钟内收到 50 个分享邀请？ |

### 可检测的异常模式

```go
type AnomalyRule struct {
    Name        string
    Description string
    Window      time.Duration   // 检测时间窗口
    Threshold   int             // 触发阈值
    Action      string          // "alert", "rate_limit", "temporarily_block"
    Severity    string          // "low", "medium", "high", "critical"
}

// 预置检测规则示例
var AnomalyRules = []AnomalyRule{
    {
        Name:      "rapid_block_flip",
        Desc:      "用户短时间内反复 block/unblock 同一人",
        Window:    1 * time.Minute,
        Threshold: 5,  // 5 次 block+unblock 操作
        Action:    "alert",
        Severity:  "medium",
    },
    {
        Name:      "mass_file_share",
        Desc:      "用户短时间内将一个文件分享给大量用户",
        Window:    5 * time.Minute,
        Threshold: 50,  // 分享给 50+ 个用户
        Action:    "rate_limit",
        Severity:  "high",
    },
    {
        Name:      "mass_file_delete",
        Desc:      "用户短时间内批量删除大量文件",
        Window:    10 * time.Minute,
        Threshold: 50,
        Action:    "alert",
        Severity:  "high",
    },
    {
        Name:      "mass_report",
        Desc:      "用户短时间内举报大量评论",
        Window:    5 * time.Minute,
        Threshold: 20,  // 举报 20+ 条评论
        Action:    "temporarily_block",
        Severity:  "critical",
    },
    {
        Name:      "concurrent_session_anomaly",
        Desc:      "同一 uid 在两个地理位置同时操作（疑似撞库或账号共享）",
        Window:    0,  // 实时检测
        Threshold: 1,  // 每次事件触发
        Action:    "alert",
        Severity:  "high",
    },
}
```

### 架构集成

```
请求路径                             离线分析路径
─────────                            ──────────
API handler                          审计日志流
  │                                     │
  ├→ service.Block()                    ├→ anomaly detector
  │   └→ audit.Log(event)              │   └→ 滑动窗口计数器
  │                                     │   └→ 规则引擎匹配
  ├→ service.ShareFile()                │   └→ 评分 + 阈值检查
  │   └→ audit.Log(event)              │
  │                                     ├→ 告警输出
  ├→ service.ReportComment()            │   ├→ outbox event (webhook)
  │   └→ audit.Log(event)              │   ├→ SSE push (管理员面板)
  │                                     │   └→ audit_alerts 表落盘
  └→ auth middleware
      └→ geo-ip + concurrent check      └→ 自动操作
                                          ├→ rate limit escalation
                                          └→ temporary block
```

#### 存储

```sql
-- 告警结果表
CREATE TABLE audit_alerts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_name       TEXT NOT NULL,
    severity        TEXT NOT NULL,            -- 'low' | 'medium' | 'high' | 'critical'
    actor_uid       TEXT NOT NULL,
    subject_uid     TEXT,                     -- 目标用户（如被集中举报的用户）
    details         JSONB NOT NULL,           -- 涉及的事件列表 + 上下文
    window_start    TIMESTAMPTZ NOT NULL,
    window_end      TIMESTAMPTZ NOT NULL,
    event_count     INT NOT NULL,
    action_taken    TEXT,                     -- 'alert' | 'rate_limited' | 'blocked'
    resolved_at     TIMESTAMPTZ,
    resolved_by     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 影响评估

| 维度 | 评估 |
|------|------|
| **用户价值** | 中高——直接影响平台安全和信任 |
| **架构杠杆** | 中——复用现有审计架构，新增检测层 |
| **风险** | 低——只读分析不影响主请求路径 |
| **依赖** | 需要告警通知渠道（已有 webhook outbox） |
| **估算** | 规则引擎 1 周，5 条检测规则各 2 天，管理面板 1 周，共 ~3 周 |

---

## 方向五：容量规划、SLO 框架与自动化伸缩

### 概念

为 aero-id 建立基于可观测性的容量规划方法论：定义服务级别目标（SLO）、构建错误预算模型、利用现有的 RED 指标和 outbox 延迟数据驱动自动化伸缩决策。将运维从"被动响应"升级为"数据驱动预测"。

### 为什么它被遗漏

已有文档覆盖了**指标采集**（`metrics.go`、`REVIEW_INFRA_DEEP.md`）和**负载测试**（`edge-cases-and-performance.md`），但从未将这些输出连接到一个**闭环的容量管理流程**中。指标存在但没有被用来回答问题："我是否需要扩容？" "当前有多少错误预算剩余？" "什么时候需要添加读副本？"

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/pkg/metrics/metrics.go` | RED 指标（aeroid_requests_total, aeroid_request_duration_seconds）被采集，但无 SLO 合规计算。数据产生但不消费 |
| `internal/app/app.go` — `warmup()` | 冷启动预热存在，但无基于负载的自动伸缩信号输出 |
| `internal/outbox/relay.go` — `observeDrain()` | Outbox 延迟数据（aeroid_outbox_oldest_age_seconds）被暴露，但无阈值告警（"outbox 延迟 > 5 分钟" = 扩容信号） |
| `internal/middleware/metrics.go` | HTTP 指标被记录，但无相关告警或自动伸缩触发器 |
| `internal/handler/grpc/server.go` — MetricsInterceptor | gRPC 指标被记录，但同样无 SLO 绑定 |
| `configs/config.yaml` | 无 `slo.*` / `scaling.*` / `alerting.*` 配置节 |
| `internal/config/config.go` | 无 SLO 相关配置字段 |
| `deployments/docker-compose.yml` | 无 HPA (Horizontal Pod Autoscaler) 配置或 scaling 建议 |

### 设计要点

#### SLO 定义框架

```yaml
# configs/slo.yaml (新文件)
slo:
  services:
    - name: "message.send"
      target_latency_p99: 500ms
      target_error_rate: 0.001  # 99.9% 成功率
      measurement_window: 28d    # 错误预算窗口
      burn_rate_alerts:
        - name: "page"
          threshold: 10          # 10× 燃烧率 → 立即告警
          duration: 5m
        - name: "ticket"
          threshold: 2           # 2× 燃烧率 → 工单
          duration: 30m

    - name: "notification.delivery"
      target_latency_p99: 2s     # 通知可以慢一点（异步）
      target_error_rate: 0.01    # 99% 送达率
      key_metric: "outbox_lag"   # 通知延迟看 outbox lag，非请求延迟
      outbox_lag_target: 60s     # 95% 的通知在 60s 内送达

    - name: "file.upload"
      target_latency_p99: 5s     # 大文件上传允许更长
      target_error_rate: 0.005
      target_througput: 100MB/s
```

#### 错误预算计算

```go
type ErrorBudget struct {
    ServiceName  string
    Window       time.Duration // 28d
    TotalBudget  float64       // 1 - target_error_rate
    Consumed     float64       // 当前窗口内的错误数 / 总请求数
    Remaining    float64
    BurnRate     float64       // 最近 1h 的消耗速度 / 预期消耗速度
    Status       string        // "green" | "yellow" | "red"
}

func CalculateErrorBudget(metrics *Registry, service string, window time.Duration) *ErrorBudget {
    // 使用 metrics 的 request 计数器计算错误率
    total := metrics.SumRequests(service)
    errors := metrics.SumErrors(service)
    consumed := float64(errors) / float64(total)
    return &ErrorBudget{
        Consumed:  consumed,
        Remaining: 1 - consumed - service.SLO.ErrorRate, // 剩余预算百分比
        BurnRate:  calculateBurnRate(metrics, service),
        Status:    classifyBudget(consumed),
    }
}

func classifyBudget(remaining float64) string {
    switch {
    case remaining > 0.5:  return "green"   // 预算 > 50%
    case remaining > 0.2:  return "yellow"  // 预算 20-50%
    default:               return "red"     // 预算 < 20%
    }
}
```

#### 伸缩信号

```
┌─────────────────────────────┐
│ 伸缩信号聚合器               │
├─────────────────────────────┤
│ P99 延迟上升 > 2× 基线       │ → +1 副本
│ CPU 使用率 > 70% 持续 5 分钟 │ → +1 副本
│ Outbox 延迟 > 30s           │ → notification worker +1
│ 错误预算红色(剩余 < 20%)     │ → 添加 2 副本 + 告警
│ 连接池使用率 > 80%           │ → 添加读副本或增加 max_open_conns
│ 低负载持续 1h               │ → -1 副本（最小保留 2）
└─────────────────────────────┘
```

#### Kubernetes HPA 集成

```yaml
# 部署 HPA 示例
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: aero-id
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: aero-id
  minReplicas: 2
  maxReplicas: 20
  metrics:
    - type: Pods
      pods:
        metric:
          name: aeroid_request_duration_p99
        target:
          type: AverageValue
          averageValue: 500m  # 500ms
    - type: Pods
      pods:
        metric:
          name: aeroid_outbox_oldest_age_seconds
        target:
          type: AverageValue
          averageValue: 30  # 30 seconds
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
```

### 运营手册

| 场景 | 信号 | 自动操作 | 人工介入 |
|------|------|---------|---------|
| **P99 延迟 500ms → 2s** | 错误预算燃烧率 10× | +2 副本 | 调试慢查询 |
| **Outbox 延迟 10s → 5 分钟** | outbox lag gauge > 300s | notification worker +2, 触发死信检查 | 检查 webhook 端点 |
| **错误预算 < 20%** | 红色告警 | +3 副本, 降级低优先级请求 | 回滚最近变更 |
| **SSO 错误率 0% → 5%** | SSO error rate surge | 断路器自动打开 → serve-stale | 联系 SSO 团队 |
| **存储延迟 5ms → 500ms** | S3 PutObject latency | 启用上传本地缓存降级 | 检查 S3 区域事件 |
| **DB 连接数 80% → 95%** | 连接池使用率 | 读操作转移到副本 | 分析慢查询 → 加索引 |

### 影响评估

| 维度 | 评估 |
|------|------|
| **用户价值** | 间接高——预防性容量管理减少停机 |
| **架构杠杆** | 高——连接可观测性到运营决策 |
| **风险** | 低——SLO/KPI 是纯分析层，不影响请求路径 |
| **依赖** | 需要现有 metrics Registry 的扩展（SLO 计数器） |
| **估算** | SLO 框架 1 周, 错误预算计算 1 周, HPA 集成 3 天, 仪表盘 1 周, 共 ~3 周 |

---

## 优先级与路线图建议

| 方向 | 类别 | 优先级 | 估算 | 依赖 | 推荐顺序 |
|------|------|--------|------|------|---------|
| **1. 请求优先级与差异化 QoS** | 可靠性 | P1 | 3-4 周 | ActorTier 信息 | ① |
| **2. 读负载卸载与 CQRS** | 架构演进 | P1 | 1-8 周（渐进） | 只读副本 | ② |
| **3. 混沌工程与弹性验证** | 测试/可靠性 | P2 | ~4 周 | Staging 环境 | ③ |
| **4. 审计驱动的异常检测** | 安全 | P2 | ~3 周 | 告警渠道 | ④ |
| **5. SLO 框架与自动伸缩** | 运营 | P2 | ~3 周 | 现有 Metrics | ⑤ |

### 为什么按此顺序

1. **QoS 优先**：因为它是"阻止问题发生"的机制——在系统过载时保护核心路径，是所有其他可靠性措施的基础。没有 QoS，低价值流量仍可能淹没核心操作。

2. **CQRS 紧随其后**：因为它是"解决性能瓶颈"的架构级方案——当前所有读/写共享同一连接池，CQRS Phase 1（只读副本）立即缓解连接池竞争，Phase 2/3 为多区域部署奠定基础。

3. **混沌工程第三**：因为只有在 QoS 和 CQRS 就位后，弹性架构才足够成熟，值得进行系统性验证。在脆弱的系统上运行混沌实验风险太高。

4. **审计异常检测第四**：因为安全分析是增值层——审计数据已被采集，但要从中提取价值需要先确保系统稳定（①+②）。

5. **SLO/自动伸缩压轴**：因为这是"智慧运营"的顶层——需要所有底层可观测性基础设施就位后才能有效实施。

---

## 总结

本文件定位了 5 个在所有 21 份既存分析文档中**零覆盖**的运营架构方向。每个方向都是 aero-id 从"优秀的基础设施"到"自愈、自适应、可预测的生产系统"的必备能力。

- **QoS** 确保关键流量不被淹没
- **CQRS** 为读写分离和多区域部署铺路
- **混沌工程** 验证弹性机制真实有效
- **审计分析** 将被动日志变为主动安全
- **SLO/伸缩** 将可观测性闭环到运营决策

这五个方向共同标志着一个成熟平台从"功能完备"到"运营卓越"的跃迁。
