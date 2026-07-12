现在我已全面理解代码库和分析文档。以下是完整架构分析。

---

# 架构分析报告：aero-id 平台层缺失与扩展方向

> **基于**: `docs/requirements/` 中两份同名分析文档 + 全量代码扫描（`internal/`、`migrations/`、`api/proto/`、`docs/`）
> **视角**: 系统架构
> **日期**: 2026-07-12

---

## 1. 架构评估

### 1.1 当前架构的优势

aero-id 的代码质量在其规模（~18K Go 行）上值得肯定。以下优势支撑了当前的开发效率和可维护性：

**优势 A：严格的单向依赖链**

```
handler → service → repository → GORM
               ├→ storage (interface)
               └→ sso (gRPC + cache)
```

这是当前架构最大的资产。它确保：Transport 层可替换（HTTP ↔ gRPC），业务逻辑可独立测试（service 层通过 mock），持久化可迁移（GORM 后屏障）。ADR-001 的决策是正确的，且到目前为止被严格遵守。

**优势 B：Option-based 依赖注入模式**

```go
func NewMessageService(messages repository.MessageRepository, files repository.FileRepository, dir UserDirectory, opts ...MessageOption) MessageService
```

这种模式允许 service 按需附加 cross-cutting 能力（audit, block checker），而不需要在构造函数签名中为所有可能的功能预留参数。这是 Go 生态中优雅的 DI 模式，值得在后续扩展中继续使用。

**优势 C：Outbox 模式作为异步边界**

ADR-003 选择 outbox 模式而非同步通知调用，是防止数据不一致的正确决定。`outbox.Relay` 的 backoff/retry/dead-letter 设计扎实，retry 指数退避带 1h cap，有 MetricsSink 接口，日志聚合在一行内——这些都是生产级考量。

**优势 D：SSO 客户端断路器设计**

ADR-004 的断路器分类（仅 availability errors 触发开路）展示了对分布式系统细微之处的理解。错误分类的正确性直接决定断路器是保护系统还是加剧问题。

### 1.2 当前架构的局限性

分析文档已识别三个平台级缺口。我补充以下**结构性观察**：

**局限 1：基础设施抽象层尚未出现**

当前 `internal/` 以业务域（file, message, comment）和组织层（handler/service/repository）两维正交划分。这符合"横向分层而非纵向模块"的设计原则（Section 1.3 of design doc）。但这也意味着**跨领域的系统级抽象没有自然的归属包**。

具体体现：
- `internal/hub/` 是一个独立的 domain 级包（连接管理），但缺少同级的 `internal/pubsub/`、`internal/coordinator/`、`internal/admission/` 等基础设施抽象包
- `internal/pkg/` 下的工具包（ratelimit, errors, idgen）层次偏低，不适合放置跨进程协调抽象
- 这导致了分析文档中的现象：3 个问题各自独立出现，但从未被统一识别为缺少的基础设施层

**局限 2：SSE Hub 和 Outbox Relay 之间存在隐式耦合**

`notification/dispatcher.go` 在同一个 `Handle` 方法中同时：
1. 写 notification 行到 DB（持久化操作）
2. 调用 `d.pusher.Publish(...)` 推 SSE（实时通知）

如果 pusher 调用阻塞或变慢（例如跨实例广播时加网络延迟），会拖慢整个 outbox drain 循环。当前在单进程模式下因 Hub.Publish 是 O(1) 的 channel send 而不明显，但一旦引入跨实例广播，这个耦合会成为性能瓶颈。

**局限 3：没有"可观察的变更"模式**

`domain.Message` 有 `EditedAt *time.Time` 但无版本或变更历史；`AuditEvent.Meta` 是 `map[string]any` 但不含 before/after。这反映了代码库整体缺乏"变更即事件"的设计理念——每次编辑都应该是一个可观察的事实，而不仅是字段覆盖。

### 1.3 架构债务与技术债

| 类型 | 描述 | 影响面 | 修复复杂度 |
|------|------|--------|-----------|
| **设计债务** | 无跨实例协调抽象 | SSO 碎片/限流失效/重复处理 | 中（需引入新接口 + 至少一个实现） |
| **设计债务** | 无连接级准入控制 | 过载时 OOM 风险 | 低（一天级改动） |
| **设计债务** | 编辑是原地覆盖 | 不可回溯/不合规 | 中（需新增表 + service 层改动） |
| **技术债** | Outbox relay 无 `FOR UPDATE SKIP LOCKED` | 多实例下重复处理 | 低（小时内改动） |

未观察到严重的技术债（废弃代码、过度抽象、不一致的命名等）。代码质量整体良好。

---

## 2. 扩展方向

基于分析文档的三个方向 + 我发现的补充机会，以下是按优先级排列的扩展方向。

### 方向 1（P1）：连接级准入控制（Admission Control Layer）

**为什么需要**

当前系统在过载时的唯一反应是"让每个请求慢慢 timeout"。在 Go 的 goroutine 模型中，这等价于"让内存持续增长直到 OOM killer 介入"。这是连锁崩溃的标准诱因。

具体风险场景：
- SSO 延迟升高 → 请求在 `sso.ValidateToken` 上排队 → goroutine 堆积 → OOM
- 突发流量（营销活动）→ DB 连接池满 → 请求在 Go 层面堆积 → GC 压力 → 响应变慢 → 更多堆积
- 恶意慢连接（slow loris）→ 每个连接 1 goroutine → 资源耗尽

**核心挑战**

1. **何时触发**：静态上限（如 maxConcurrent=1000）简单但不够智能。动态阈值（基于 moving average latency + GC 压力）更难实现但更有价值
2. **粒度**：全局？per-route？per-user？粗粒度保护简单但可能误伤正常流量
3. **优先级**：管理操作（如封禁用户）应在过载时优先于普通查询

**预期架构变更**

```
internal/admission/             # 新包
    ├── controller.go           # 核心：semaphore + 过载检测
    ├── config.go               # 配置结构
    ├── middleware.go           # gin.HandlerFunc / gRPC UnaryInterceptor
    └── metrics.go              # 观测指标
```

- `internal/middleware/` 新增 `AdmissionControl` 中间件
- `internal/hub/hub.go` 的 `Subscribe` 方法增加连接数上限检查
- `configs/config.yaml` 新增 `admission.max_concurrent`、`admission.max_sse_connections`

**对现有系统的影响**

- 零侵入：admission control 作为中间件在 router 层注入，不改变 handler/service/repository 任何代码
- 默认关闭（opt-in），与 rate limit 一致——开发环境无感
- SSE 连接上限只在 Hub 层加一个 `atomic.Int64` 计数器 + 配置值检查，不影响现有订阅逻辑

### 方向 2（P1）：多实例协调层（Multi-Instance Coordination Layer）

**为什么需要**

分析文档已充分论证——SSE Hub 碎片化、限流失效、outbox 重复处理。这本质上是"单进程假设"架构成熟度瓶颈。在水平扩展部署中，这 3 个问题会同时爆发。

**核心挑战**

1. **抽象设计**：需要找到一个足够泛化的接口，既能覆盖 SSE 广播、又能覆盖限流同步、还能覆盖分布式锁。但不可过度抽象为"万能消息总线"
2. **回退策略**：无外部依赖时退化为当前行为——这是最重要的非功能需求
3. **外部依赖选择**：Redis 是最自然的选项（Pub/Sub + 分布式限流 + Redlock），但引入 Redis 意味着运维复杂度增加

**预期架构变更**

```
internal/coordinator/           # 全新包 — 多实例协调抽象
    ├── pubsub.go               # PubSub 接口
    ├── lock.go                 # DistributedLock 接口
    ├── ratelimit.go            # DistributedRateLimiter 接口
    ├── local/                  # 退化实现（当前行为）
    │   ├── pubsub.go
    │   ├── lock.go
    │   └── ratelimit.go
    └── redis/                  # 基于 Redis 的实现（可选）
        ├── pubsub.go
        ├── lock.go
        └── ratelimit.go
```

变更波及面：
- `internal/hub/hub.go`：Publish 方法通过 `coordinator.PubSub` 广播 + 本地推送
- `internal/notification/dispatcher.go`：不再直接持有 `hub.Pusher`，改为持有 `coordinator.PubSub`
- `internal/outbox/relay.go`：drain 中加分布式锁或 `FOR UPDATE SKIP LOCKED`
- `internal/pkg/ratelimit/ratelimit.go`：新增分布式实现选项

**对现有系统的影响**

- Phase 1（FOR UPDATE SKIP LOCKED）：仅改 `repository/outbox.go` 的 `FetchUnpublished`，零架构变更
- Phase 2（PubSub 接口）：影响 `hub.Hub` 和 `notification.Dispatcher`，但接口保持向后兼容
- Phase 3（分布式限流）：`ratelimit.Limiter` 添加分布式选项，不影响调用方

### 方向 3（P2）：实体变更历史（Entity Change History）

**为什么需要**

合规（GDPR 审计需求）、仲裁（内容争议回溯）、调试（"谁在什么时候改了什么东西"）三个场景都需要编辑历史。当前架构中旧值永久丢失——这是数据治理层面的缺口。

**核心挑战**

1. **存储模型**：泛型 `entity_edits` 表 vs 各实体独立历史表。泛型表减少 migration 数量但丧失 FK 约束；独立表更规范化但改动量大。建议泛型表 + `entity_type` 约束
2. **性能**：每次编辑多一次 INSERT。对高频编辑场景（如协作编辑），这可能是热点
3. **隐私**：公开历史 API 可能暴露用户已删除的内容版本。必须严格限制访问权限（owner + admin only）

**预期架构变更**

```
migrations/000017_create_entity_edits.up.sql
migrations/000017_create_entity_edits.down.sql

internal/domain/entity_edit.go   # 新增 domain 模型
internal/repository/entity_edit.go # 新增 repository
internal/service/ — edit 方法修改：
    └── message.go  # Edit 方法：先读旧值 → INSERT entity_edit → UPDATE
    └── comment.go  # Edit 方法：同上
    └── file/service_crud.go # UpdateMetadata：同上
```

此外：
- `internal/domain/audit.go` 的 `AuditEvent` 可考虑在 `Meta` 中加入 `version` 字段关联到 `entity_edits`
- 可选：暴露 `GET /v1/{entity}/{id}/history` 只读端点

**对现有系统的影响**

- 这是侵入性最高的改动——需要修改 3 个 service 的 Edit 方法
- 但改动模式高度重复（read-before-write → INSERT history → UPDATE），可提取为 helper
- 存储空间增加：每次编辑 ~500B，100 万编辑 ~500MB，在 Postgres 中可接受
- 无传输层影响：历史 API 是新增端点，不影响现有端点

### 方向 4（P2）：可观测性增强 —— 结构化审计与变更加载（Observability & Audit Enrichment）

这是分析文档中未单独提炼但值得列为独立方向的机会。

**为什么需要**

当前 `AuditEvent` 记录了"谁对什么做了什么"，但缺少：
- 请求追踪上下文（trace ID、span ID）
- 操作结果（success/failure + 错误码）
- 操作耗时
- 变更前后快照（与方向 3 联动）

**核心挑战**

1. 审计日志与业务操作耦合：当前 `AuditLogger.Log` 在 service 层调用，但 trace ID 是在 handler/middleware 层注入的。需要跨层传递
2. 存储膨胀：每增加一个字段，audit_events 表的行大小增大

**预期架构变更**

```go
// internal/domain/audit.go
type AuditEvent struct {
    ID          uuid.UUID
    TraceID     string        // 新增：关联请求追踪
    ActorUID    string
    Action      string
    SubjectType string
    SubjectID   string
    TargetUID   string
    Before      *AuditSnapshot // 新增：操作前快照（可选）
    After       *AuditSnapshot // 新增：操作后快照（可选）
    Outcome     string        // 新增：success / failure / rejected
    Duration    time.Duration // 新增：操作耗时
    Meta        map[string]any
    CreatedAt   time.Time
}
```

**对现有系统的影响**

- 这是增量增强：新增字段为 optional，不影响现有写入路径
- 与方向 3 联动：`Before`/`After` 可指向 `entity_edits` 行 ID，避免重复存储

### 方向 5（P3）：存储层统一 —— 去 GORM 依赖（Optional Future）

这不是当前优先事项，但值得在路线图中标记。GORM 作为 ORM 在简单 CRUD 上工作良好，但：
- 复杂查询（如 search 的 tsvector）需要原生 SQL 或 GORM 的 `Raw()`
- 迁移管理用外部 CLI（`golang-migrate`），与 GORM 的 AutoMigrate 不同步
- GORM 的 callback 和 hook 机制在当前代码库中未充分利用

**核心挑战**

- 全量替换成本高（23 个 migration 文件 × 上/下 + repository 层代码）
- GORM 的事务管理与 outbox 模式的原子性要求高度耦合

**建议**：作为 P3，仅在 GORM 版本升级成为障碍时考虑。当前不是替换时机。

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

**原则 1：可回退性（Fallback-first）**

所有新增的基础设施抽象必须提供退化实现，保持开发/CI 环境的零依赖体验。

```go
// 示例：PubSub 接口设计
type PubSub interface {
    Publish(ctx context.Context, channel string, msg []byte) error
    Subscribe(ctx context.Context, channel string) (<-chan Message, func(), error)
}

// localPubSub 是退化实现：仅本进程内广播
type localPubSub struct {
    mu      sync.RWMutex
    subs    map[string][]chan Message
}

// redisPubSub 是生产实现
type redisPubSub struct {
    client *redis.Client
    pubsub *redis.PubSub
}
```

这个模式的精髓在于：**默认路径是单进程**，外部依赖是 opt-in 增强。

**原则 2：接口在消费方定义（Consumer-side interface）**

当前代码已遵循此模式——`hub.Pusher` 接口在 `hub` 包中定义，被 `notification.Dispatcher` 消费。继续沿用。

**原则 3：Option 化新能力**

类似 `WithBlockChecker`、`WithAuditLogger` 模式，新的 cross-cutting 能力（变更历史、分布式限流）通过 option 函数注入，而不是修改构造函数签名。这确保了：

- 向后兼容：现有 `NewXXXService` 调用不加 option 也编译通过
- 显式 opt-in：由 composition root（`app/app.go`）决定启用哪些能力

**原则 4：最小化传输层变动**

新的平台能力不应暴露新的 HTTP/gRPC 端点（除非确实需要客户端可见的功能，如变更历史查询）。内部增强（如 outbox 分布式锁、admission control）应完全在中间件/基础设施层完成。

### 3.2 是否需要新的抽象层

**是。需要引入 `internal/coordinator/` 包。**

当前 `internal/` 的包结构以业务域为主，缺少一个放置跨进程协调抽象的自然位置。选项分析：

| 选项 | 描述 | 优点 | 缺点 |
|------|------|------|------|
| **新建 `internal/coordinator/`** | 独立的协调抽象层，含 PubSub/Lock/RateLimit 接口 + 实现 | 清晰归属，易测试 | 新包，初始较空 |
| **扩展现有 `internal/pkg/ratelimit/`** | 在 ratelimit 包中加入分布式版本 | 改动最少 | pubsub 和 lock 无处安放；语义不匹配 |
| **注入到 `internal/hub/`** | 让 hub 包直接支持 Redis Pub/Sub | Hub 职责太重（订阅管理 + 跨进程协调） | 违反单一职责 |

**推荐：新建 `internal/coordinator/`**。它将三个子问题统一为一个架构层，正如分析文档所指出的——这是"从单进程到分布式系统的跃迁"所需的抽象。

### 3.3 向后兼容策略

| 改类型 | 兼容策略 | 示例 |
|--------|---------|------|
| 新增接口方法 | 默认退化实现（no-op）| `PubSub.Publish` 退化为本地 channel send |
| 新增配置选项 | 默认关闭（opt-in）| `admission.enabled: false` |
| 新增中间件 | 在 router 组中可选加入 | 不改变现有路由注册代码 |
| 修改 service 方法 | 只新增不修改签名 | `Edit` 方法内部加 `if s.history != nil { ... }` |
| 新增端点 | 新路径，不影响旧路径 | `GET /v1/{entity}/{id}/history` |

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

**Redis 是唯一值得认真考虑的引入。**

| 技术 | 是否需要 | 理由 |
|------|---------|------|
| Redis | **是（可选）** | Pub/Sub 跨实例广播 + 分布式限流 + 分布式锁，一个依赖解决三个问题 |
| RabbitMQ / Kafka | **否** | 当前场景不需要持久化消息队列；outbox 模式已覆盖 |
| etcd / Consul | **否** | 分布式锁场景单一，Redis 足够；etcd 运维成本高 |
| Envoy / 服务网格 | **否** | 项目规模不需要；连接级准入控制在应用层实现更灵活 |
| Prometheus（已有） | 维持 | 已集成，用于 admission control metrics |
| OpenTelemetry（已有） | 维持 | 已集成，需在 audit logging 中透传 trace ID |

### 4.2 Redis 引入的评估

| 维度 | 评估 |
|------|------|
| **运维复杂度** | 中等。需管理 Redis 集群或 Sentinel，但 aero-id 规模下单一实例足够 |
| **延迟增加** | 本地 Redis ~0.5ms，同区域 ~2ms。对 SSE 推送延迟敏感场景可接受 |
| **可用性依赖** | 关键：Redis 不可用 → 系统回退到单进程行为，不退化为故障 |
| **版本兼容** | Go 的 `go-redis` 库成熟稳定。`redis/v9` 支持 Pub/Sub Sharded |

**决策建议**：Redis 引入为 **opt-in 部署选项**——不在代码层强制依赖，仅在配置 `coordinator.backend: "redis"` 时加载。

### 4.3 自建 vs 引入依赖

| 能力 | 自建 | 引入依赖 | 决策 |
|------|------|---------|------|
| 分布式限流 | 基于 Redis 的滑动窗口 ~200 行 | go-redis + 现有 token bucket 逻辑 | **依赖 Redis，自建逻辑**——限流算法是业务逻辑，不应外部化 |
| Pub/Sub | 基于 Redis 的 `SUBSCRIBE`/`PUBLISH` ~100 行 | 无合适 Go 库封装此场景 | **自建**——太简单，不值得引入库 |
| 分布式锁 | `SET NX EX` 或 Redlock ~150 行 | `go-redsync` (1.5K stars) | **依赖 Redsync**——分布式锁的边界情况（锁过期、续期、时钟漂移）极难自建正确 |

**总结**：
- Redis 客户端库：`go-redis/v9`（已有成熟选型）
- 分布式锁：`go-redsync`（或借鉴其实现）
- Pub/Sub 封装：自建包装层
- 分布式限流器：自建，基于 Redis 的 `INCR` + `EXPIRE` 滑动窗口

### 4.4 变更历史存储选型

| 方案 | 优点 | 缺点 | 决策 |
|------|------|------|------|
| **Postgres JSONB（推荐）** | 无需引入新存储；与主事务原子提交；支持 GIN 索引 | 历史版本膨胀后查询变慢 | **推荐** |
| 独立历史表（message_edits, file_versions 等） | 强类型，好查询 | 每个实体需要独立 migration + repository | 设计过度 |
| MongoDB 存历史 | 无 schema 变更 | 多存储引入 TCC，无法与主事务原子提交 | 不推荐 |
| 事件溯源（EventStore） | 最强合规能力 | 架构范式转换 | 不推荐（项目规模不对等） |

**最终推荐**：Postgres JSONB + 泛型 `entity_edits` 表。与 outbox 事件共用同一个数据库，保持架构简洁。

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0（立即 — 安全底线）
├── Outbox `FOR UPDATE SKIP LOCKED` ← 1 天，零架构变更
└── MaxConcurrent 中间件 ← 0.5 天，防 OOM

P1（短期 — 平台基础）
├── 实体变更历史表 + service 层注入 ← 3 天
├── PubSub 接口 + Local 退化实现 ← 2 天
├── SSE 连接上限 ← 0.5 天
└── 结构化审计增强（trace ID + outcome）← 2 天

P2（中期 — 分布式增强）
├── Redis Pub/Sub 实现 → 跨实例 SSE ← 1 周
├── Redis 分布式限流器 ← 2 天
└── 实体变更历史 API 端点 ← 1 天

P3（远期 — 智能演化）
├── 动态过载检测 ← 3 天
├── 请求优先级队列 ← 2 天
└── 与 ROADMAP.md 对齐的长期规划
```

### 5.2 阶段划分与里程碑

**Phase 0：安全底线（Week 1）**

| 任务 | 产出 |
|------|------|
| 1. Outbox `FetchUnpublished` 加 `FOR UPDATE SKIP LOCKED` | 多实例下零重复处理 |
| 2. 实现 `admission.MaxConcurrent` 中间件 | 配置化的并发上限，超量返回 503 |
| 3. 实现 `admission.MaxSSEConnections` | Hub 连接数上限 |

**Phase 1：变更历史 + 协调抽象（Week 2-3）**

| 任务 | 产出 |
|------|------|
| 1. Migration: `entity_edits` 表 | 可存储任意实体的变更快照 |
| 2. Domain: `EntityEdit` 模型 + Repository | 读写历史行 |
| 3. Service: Edit 方法注入变更历史 | message/comment/file 编辑不在原地覆盖 |
| 4. `internal/coordinator/` 包 + PubSub 接口 + Local 实现 | 可插拔的跨进程协调框架 |

**Phase 2：Redis 集成（Week 4-5）**

| 任务 | 产出 |
|------|------|
| 1. `coordinator/redis/pubsub.go` | PubSub Redis 实现 |
| 2. Hub 集成 `coordinator.PubSub` | SSE 推送跨实例广播 |
| 3. `coordinator/redis/ratelimit.go` | 分布式限流 |
| 4. `GET /v1/{entity}/{id}/history` API | 对外变更历史查询 |
| 5. End-to-end 集成测试（3 个实例验证） | 多实例协调验证 |

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| Redis 故障导致 SSE 推送中断 | 中 | 高（实时性破坏）| 回退到单进程行为，不降级为故障；监控 Redis 连接健康 |
| 变更历史 JSONB 膨胀影响主表查询性能 | 低 | 中（查询变慢）| entity_edits 独立表，不 JOIN 主查询；历史查询走独立索引 |
| 多实例协调抽象过度设计（"万能总线"反模式） | 中 | 低（可回滚）| 坚持接口最小化：只暴露 Publish/Subscribe/Lock 三个方法；不引入事件模式匹配 |
| 分布式限流与本地限流行为不一致 | 低 | 中（预期偏差）| 维护一个行为对比测试套件，验证两种实现给出相同 Allow/Reject 决策 |
| 工程约束被突破（文件 > 500 行） | 中 | 中（需拆分）| 新包文件规模严格 < 300 行起步；`internal/coordinator/` 按接口各一文件 |

### 5.4 关键设计决策清单

```
┌────────────────────────────────────────────────────────────┐
│ 决策 1: entity_edits 使用泛型表 vs 实体独立表              │
│ 选项 A: 泛型表 + entity_type/entity_id  ← 推荐             │
│ 选项 B: message_edits, file_versions, comment_revisions    │
│ 权衡: A 改动少但无 FK；B 规范但改动量大                    │
├────────────────────────────────────────────────────────────┤
│ 决策 2: 多实例协调外部依赖选择                              │
│ 选项 A: Redis Pub/Sub + Redlock  ← 推荐                    │
│ 选项 B: PostgreSQL LISTEN/NOTIFY（零依赖）                   │
│ 权衡: A 延迟低、功能全；B 零依赖但扩展性差、无限流能力     │
├────────────────────────────────────────────────────────────┤
│ 决策 3: Admission Control 在应用层 vs 代理层                │
│ 选项 A: Gin 中间件 + channel semaphore  ← 推荐              │
│ 选项 B: Envoy 限流过滤器                                    │
│ 权衡: A 与代码库集成好、无额外运维；B 独立于应用但引入 Envoy │
├────────────────────────────────────────────────────────────┤
│ 决策 4: 变更历史存储为 JSONB vs 独立列                      │
│ 选项 A: JSONB (before_snapshot, after_snapshot)  ← 推荐    │
│ 选项 B: 每个实体字段独立列                                  │
│ 权衡: A 通用但无 schema 验证；B 强类型但无法泛型化          │
└────────────────────────────────────────────────────────────┘
```

---

## 6. 总结

分析文档识别了三个真实且重要的架构盲区。从架构视角看，它们不是孤立的技术缺口，而是**同一个根本问题的三个侧面**：aero-id 的架构成熟度处于"单进程应用 → 分布式系统"转换的前夜。

| 维度 | 当前状态 | 目标状态 |
|------|---------|---------|
| 数据层 | 幂等但不保留历史 | 变更即事件，可回溯 |
| 基础设施层 | 单进程假设 | 可回退的多实例协调 |
| 弹性层 | 速率限制但不保护并行度 | 两层保护：admission + rate limit |

这三项增强的共通设计原则是**可回退性（fallback-first）**——所有新抽象在无外部依赖时退化为当前行为。这是开发体验和生产弹性的双重保障。

**最重要的建议**：先做 Phase 0 的两项改动（`FOR UPDATE SKIP LOCKED` + `MaxConcurrent` 中间件）。它们各自不到一天，不会触发任何文件超过 500 行的约束，但能直接消除多实例部署中最危险的两种故障模式。成本最低的防护网应该最先装。
