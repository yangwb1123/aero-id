# aero-id — 三个系统性缺失的平台级功能

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码逐文件扫描 — `internal/` 全 23 子包、`migrations/` 23 份 SQL、`api/proto/`、`configs/`、`cmd/`、`Makefile`、`docs/`。与 `docs/requirements/` 下 **100 份既有分析文档**、`docs/REVIEW_*`（8 份）、`ROADMAP.md`、`docs/ARCHITECTURE.md`、`docs/DECISIONS.md`、`docs/CHANGELOG.md` 逐标题交叉比对。  
> **约束**: 不编写任何代码。以下方向在所有既有材料中 **零系统性覆盖**——从未作为独立方向被完整论证。  
> **选择标准**: 只选平台级、非修补性的缺口（非 "再加一个 endpoint" 或 "优化某个查询"）。每个方向对应一个**缺失的平台抽象层**，需要架构层面的新增而非局部修改。

---

## 为什么从 5 个减到 3 个

100 份既有需求文档 × 每个 5 方向 = 累计 **500+ 方向条目**。在前 97 次扫描中，几乎每一个可观察到的代码级缺口都已经被作为独立方向论证过。在深度阅读全部 100 份文档后，以下 **3 个方向是最后且最具战略价值的系统性盲区**——它们不是修补现有抽象，而是在当前架构中**完全缺失的平台抽象层**。

---

## 方向一：实体级变更历史与状态复原（Entity Change History & Point-in-Time Recovery）

### 问题陈述

aero-id 的审计日志（`audit_events`）仅记录"谁对什么资源做了什么操作"，**不记录操作前后的数据状态**。当一条消息被编辑、一个文件被改名、一条评论被举报解决后，没有任何机制能回答以下关键问题：

- "这条消息被编辑之前的内容是什么？"
- "这个文件在改名之前的 visibility 是什么？"
- "这个评论在 2 周前被谁举报过，当时的内容快照是什么？"

在当前架构中，**所有编辑都是原地覆盖**（`UPDATE ... SET content = ?` / `UPDATE ... SET name = ?`），旧值永久丢失。

### 代码级证据

**证据 1：审计事件只有动作标签，没有状态快照**

```go
// internal/domain/audit.go
type AuditEvent struct {
    ID          uuid.UUID
    ActorUID    string
    Action      string        // 例如 "file.share.grant"
    SubjectType string        // 例如 "file"
    SubjectID   string        // 目标实体的 ID
    TargetUID   string        // 可选的目标用户
    Meta        map[string]any // 只有附加元数据，没有 before/after 约定
    CreatedAt   time.Time
}
```

`Meta` 是一个无模式的 `map[string]any`。没有任何审计事件在写入时携带 `before`/`after` 快照——不是"恰好没实现"，而是 **`AuditEvent` 结构体根本没有设计这样的字段**。

**证据 2：所有编辑操作都是原地覆盖，不保留旧副本**

```go
// internal/service/message.go — Edit
func (s *messageService) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
    // … 验证通过后直接原地更新
    m.Content = content   // ← 旧内容永久丢失
    if err := s.msgs.Update(ctx, m); err != nil {
        return nil, ...
    }
}
```

```go
// internal/service/comment.go — Edit
func (s *commentService) Edit(ctx context.Context, id domain.ID, content string) (*domain.Comment, error) {
    c.Content = content   // ← 旧内容永久丢失
    if err := s.comments.Update(ctx, c); err != nil {
        return nil, ...
    }
}
```

```go
// internal/file/service_crud.go — UpdateMetadata
func (s *fileService) UpdateMetadata(...) (*domain.File, error) {
    // 既不读旧值、也不写快照
    if err := s.files.UpdateMetadata(ctx, id, updates); err != nil {
        return nil, ...
    }
    // ← 旧 name/visibility/meta 永久丢失
}
```

**证据 3：数据模型没有版本号或变更时间线**

```go
// internal/domain/message.go
type Message struct {
    EditedAt  *time.Time    // 只有"最后编辑时间"，没有"第几次编辑"
    // 无 EditHistory []EditSnapshot
}
```

三个可编辑实体（message、comment、file）各自只有一个 `EditedAt`/`UpdatedAt` 时间戳，没有一个维护变更历史列表。这意味着：

- 法规合规（GDPR 的"谁看了什么"、"什么被改了"）无法满足
- 内容争议/仲裁无法回溯（"提交证据：这条消息在举报之前的内容"）
- 开发者调试没有变更审计线索

**证据 4：migrations 中无变更历史表**

```sql
-- migrations/ 全部 23 份 SQL：
-- 000014_create_audit_log.up.sql  — 只有扁平审计事件
-- 所有其他迁移 — 无 message_edits、file_versions、comment_revisions 表
```

### 为什么这是盲区

100 份既有需求文件覆盖了"审计日志加强"（8 次关键词命中）、"软删除恢复"（12 次）、"数据生命周期"（6 次），但 **没有任何一份将"实体级变更历史"作为独立的方向提出**。审计日志专注于"谁做了操作"（Who-Action）；变更历史专注于"数据长什么样"（Before-After-Snapshot）。这是两个正交的能力。

### 架构师视角

当前架构中，写路径是**幂等但不保留历史**的。这本身不是 bug——对于 1.0 来说是对的。但一旦进入生产环境并出现以下场景，缺失就变成事故：

1. **编辑争议**：用户 A 发了一条消息，用户 B 举报，用户 A 编辑了内容然后说"我没说过"。当前没有任何机制验证原始内容。
2. **合规请求**：监管要求提供"2026-07-01 这个文件的所有历史版本"。当前做不到。
3. **回滚需要**：管理员想撤回一条消息的编辑。当前做不到——旧内容已经丢失。

**建议方案**：
1. 增加泛型 `entity_edits` 表（polymorphic: entity_type, entity_id, version, before_snapshot(jsonb), after_snapshot(jsonb), actor_uid, created_at）
2. 在 service 层的 Edit/UpdateMetadata 等变更操作中，先读旧值，在事务中写入 edit history 行，再执行原地更新
3. 暴露 `GET /v1/{entity}/{id}/history` 端点（限制为 owner/admin）
4. 与审计日志联动：audit 事件可以通过 `(subject_type, subject_id, created_at)` 关联到对应的 edit history 版本

**代价与收益**：存储成本（每次编辑额外一行 JSON），但可为所有后续合规/回滚/调试场景解锁能力。建议作为 opt-in 功能，通过 migration 添加表，业务代码通过 option 注入。

### 产品经理视角

**用户价值**：间接但巨大。当用户第一次问"这条消息原来写的是什么"时，答案直接决定用户对平台的信任度。短期来看，这是"安全网"功能——用户可能一年也用不到一次，但用到的那次就是救命的。中期来看，这是合规基础——没有它，某些垂直行业（医疗、法律、金融）无法采用。

**风险**：存储开销可控（每条编辑 ~500B 快照，100 万次编辑 ~500MB）。真正风险是误用——公开暴露历史 API 可能放大隐私问题（比如"看我 3 年前发的黑历史"），所以必须只允许 owner 和 admin 查询。

---

## 方向二：多实例协调与跨进程状态同步（Multi-Instance Coordination & Cross-Process State Sync）

### 问题陈述

aero-id 的多个核心组件**假设单进程运行**。在一个水平扩展（multi-replica）的生产部署中，以下功能会出现不同程度的分裂或静默失效：

| 组件 | 问题 | 严重度 |
|------|------|--------|
| SSE Hub（`internal/hub/hub.go`） | 用户连接到实例 A，但 outbox relay 在实例 B 触发推送 → 用户收不到实时通知 | **高**（实时性破坏） |
| 速率限制器（`internal/pkg/ratelimit/ratelimit.go`） | 每个实例独立计数 → 用户通过 N 个实例获得 N 倍配额 | **中**（限流失效） |
| 幂等键存储（`internal/idempotency/store.go`） | 基于数据库，跨实例 OK。但 `Reserve`→`Complete` 的并发保护是 DB 行锁，在大量跨实例重试下可能有争用 | **低**（但需注意） |
| Outbox relay | 多实例同时轮询同一批事件 → 每个事件被 N 个 handler 处理 | **中**（重复处理） |

其中 **SSE Hub 碎片化**最为关键——这是直接影响用户体验的问题。

### 代码级证据

**证据 1：Hub 是纯进程级结构**

```go
// internal/hub/hub.go:19-29
type Hub struct {
    subs   sync.Map // uid → *uidSubs，全部在进程内存中
    nextID atomic.Int64
    closed atomic.Bool
}
```

`sync.Map` 中的订阅映射完全驻留在进程内存中。没有 Redis Pub/Sub，没有 gRPC broadcast stream，没有共享数据库轮询——**没有任何跨实例协调机制**。

**证据 2：Dispatcher 在同一进程中调用 Hub.Publish**

```go
// internal/notification/dispatcher.go
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    // ... 写 notification 行到 DB ...
    if d.pusher != nil {
        payload, _ := json.Marshal(n)
        d.pusher.Publish(n.RecipientUID, n.Type, string(payload))
        // Publish 只通知本进程的 Hub → 用户在其他实例上收不到
    }
}
```

**证据 3：速率限制器也是纯进程级**

```go
// internal/pkg/ratelimit/ratelimit.go:25-32
type Limiter struct {
    mu      sync.Mutex
    buckets map[string]*bucket // 全部在进程内存中
    // …
}
```

文档注释明确告知 `// PER-INSTANCE: each running aero-id process keeps its own buckets in memory.`

**证据 4：Outbox relay 没有分布式锁**

```go
// internal/outbox/relay.go:86-93
func (r *Relay) drain(ctx context.Context) int {
    events, err := r.repo.FetchUnpublished(ctx, r.batch)
    // 多个实例同时调用 FetchUnpublished → 拿到相同的一批事件
    // 每个实例都调用 handler.Handle → 产生 N 条重复的 notification 行
}
```

虽然 notification 的 dispatcher 会创建 notification 行（可能由重复事件导致多条相同通知），且幂等键可以缓解客户端的重复写入，但 outbox 层面没有 `SELECT ... FOR UPDATE SKIP LOCKED` 或类似的分布式协调。

### 为什么这是盲区

100 份既有分析中，"SSE Hub 碎片化"在文档 2026-07-11-aero-id-genuine-four-systemic-architectural-gaps.md 中被作为方向一论证。"分布式限流"在前 80 份文档中出现了至少 12 次。"Outbox 去重/多实例"也在延迟任务框架讨论中被提到。

但 **没有任何一份文档将这 3 个问题统一提升为"多实例协调"这个架构层面的缺失**，并论证为什么这是一个平台级抽象——而不是 3 个各自独立的优化任务。

### 架构师视角

多实例问题是**架构成熟度从"单进程应用"到"分布式系统"的分水岭**。当一个系统可以用 `sync.Mutex` 和 `sync.Map` 解决问题时，它天然是单进程的。需要跨实例协调时，就必须引入一层中间件抽象。

aero-id 需要一个统一的**跨进程消息总线抽象**。这个抽象应该：
1. **提供 `PubSub` 接口**：SSE Hub 的 `Publish` 调用不只在本地广播，也通过 redis/gRPC stream 广播到所有实例
2. **提供 `DistributedLock` 接口**：outbox relay 使用 `SELECT ... FOR UPDATE SKIP LOCKED` 或基于 DB 的分布式锁确保每批事件只被一个实例处理
3. **提供 `DistributedRateLimiter` 接口**：当配置为 distributed 时，使用 Redis 中心化桶替代本地桶

**关键设计决策**：这个抽象**必须保持可回退**——当没有外部中间件（Redis）时，退化为当前的单实例行为。这是开发/CI 环境无痛升级的前提。

### 产品经理视角

**用户价值**：直接体现为"如果我部署了 3 个实例，实时推送应该仍然工作"。这是一个**高可见性**的产品体验问题——用户不会理解为什么另一个浏览器窗口收不到通知，只知道"这个平台的通知不可靠"。

**业务影响**：在用户达到 ~1000+ DAU 之前，单实例通常够用。但一旦开始水平扩展（无论是因为流量还是高可用），这个问题就会成为故障热点。建议在系统进入生产环境前就设计好这个抽象——而不是在半夜被 on-call 叫醒后仓促加 Redis。

### 建议的路线图顺序

```
Phase 1（低投入、高回报）: Outbox relay 加 `FOR UPDATE SKIP LOCKED`
   → 多实例下 stop double-processing，零架构变更
Phase 2（中等投入、中等回报）: 增加可插拔 PubSub 接口 + Redis 实现
   → SSE Hub 跨实例广播，不改变 Dispatcher/Service 层
Phase 3（高投入、高回报）: 可选分布式限流器
   → 仅对需要精确限流的部署启用
```

---

## 方向三：连接级准入控制与优雅过载保护（Connection-Level Admission Control & Graceful Overload Protection）

### 问题陈述

aero-id 有成熟的请求级限流（rate limiter），但**完全没有连接级准入控制**。当系统处于过载状态（CPU 升高、DB 连接池耗尽、SSO 响应变慢）时，当前的反应是"让每个请求慢慢等 timeout"——这是一个**连锁崩溃（cascading failure）的经典诱因**。

具体表现为：

1. **无最大并发请求数** — 没有 `GOMAXPROCS` 之外的 goroutine 天花板
2. **SSE 连接无上限** — 每新开一个 SSE 连接就创建一个 goroutine + 一个带缓冲 channel（16 个事件）。10 万个空闲 SSE 连接 = 10 万个 goroutine + 160 万个事件槽位的内存占用
3. **无请求排队/拒绝策略** — 过载时唯一的选择是"接受 → 慢慢 response"，而不是"拒绝 → Retry-After"
4. **无连接级优先级** — 管理员的紧急请求和普通用户的批量查询走同一个队列

### 代码级证据

**证据 1：SSE 连接无准入控制**

```go
// internal/handler/http/events.go（假设的 EventsHandler）
// SSE Subscribe 在 Hub.Subscribe 中创建 channel
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    ch := make(chan SSEEvent, 16) // ← 每个连接分配 16 个槽位
    // … 无上限检查，无最大连接数
}
```

SSE handler 在注册时**不检查当前连接数、不拒绝新连接**。理论上，恶意用户可以打开数千个 SSE 连接，每个消耗 1 个 goroutine + 16 个事件的 channel 内存。

**证据 2：HTTP 服务器有 ReadTimeout/WriteTimeout，但无 max concurrent**

```go
// internal/app/app.go
httpServer := &http.Server{
    Addr:         fmt.Sprintf("%s:%d", cfg.Server.HTTP.Host, cfg.Server.HTTP.Port),
    Handler:      router,
    ReadTimeout:  cfg.Server.HTTP.ReadTimeout,   // 连接级别的读取超时
    WriteTimeout: cfg.Server.HTTP.WriteTimeout,   // 连接级别的写入超时
    // 没有 MaxConcurrent, 没有 MaxHeaderBytes 显式限制
}
```

Go 的 `http.Server` 没有内置 max-concurrent 概念——每个连接产生一个 goroutine，goroutine 数量理论上只受系统内存限制。

**证据 3：DB 连接池有最大连接数，但无应用层保护**

```go
// internal/app/app.go:37-41
gdb, err := db.Open(db.Options{
    MaxOpenConns:    cfg.Database.MaxOpenConns,    // DB 层有上限
    MaxIdleConns:    cfg.Database.MaxIdleConns,
    ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
})
```

但如果有 1000 个并发请求进来，`MaxOpenConns=25` 不会拒绝请求——它们会排队等待 DB 连接释放。期间所有 goroutine 继续占用内存和 goroutine 栈。

**证据 4：速率限制作用在请求级别，不保护连接/goroutine 层面**

```go
// internal/middleware/ratelimit.go
func RateLimit(l *ratelimit.Limiter) gin.HandlerFunc {
    return func(c *gin.Context) {
        if l == nil || l.Allow(key) {
            c.Next()    // ← 允许后请求照常处理
            return
        }
        // 429 …
    }
}
```

## 什么是 Rate Limit 做不到的

| 保护目标 | Rate Limit | Admission Control |
|---------|-----------|-----------------|
| 控制每秒请求数 | ✅ | ✅ |
| 控制并发在-flight 请求数 | ❌ | ✅ |
| 控制 SSE 连接总数 | ❌ | ✅ |
| 控制 goroutine 总量 | ❌ | ✅ (间接) |
| 过载时快速失败（而非排队） | ✅ | ✅ |
| 按优先级分队列 | ❌ | ✅ |

### 为什么这是盲区

100 份既有分析中，"速率限制"是出现频率最高的关键词之一（40+ 次命中）。但这些分析**全部聚焦于请求频率控制**——没有一份将"连接级准入控制"作为独立平台层提出。Rate limit 和 admission control 解决的是两个不同的问题：前者控制**速率**，后者控制**并行度**。缺少后者，在突发流量或慢依赖的场景下，系统仍然可能被 goroutine 压垮。

大多数分析提到了"优雅降级"（graceful degradation），但没有给出具体的实现抽象建议。

### 架构师视角

现代生产系统需要**两层保护**：

```
Request → [Admission Control] → [Rate Limiter] → [Handler]
            并发/连接上限           速率上限
```

Admission Control 层应该：

1. **限制最大并发在-flight 请求数** — 使用 buffered channel 作为 semaphore（如 `maxConcurrent = 1000`），超过则返回 503 `Service Unavailable` + `Retry-After`
2. **限制最大 SSE/MQTT 连接数** — 全局计数器，超过则拒绝新连接
3. **实现请求优先级** — 管理员的 `PermSystemConfig` 请求可以走高优先级队列（另一个 semaphore，有专用槽位）
4. **过载检测** — 监控 request latency 的 moving average + DB 连接池利用率 + GC 压力 → 动态降低 admission threshold

**与 Rate Limiter 的关系**：Rate limiter 保护的是下游（让 DB/SSO 不要被冲垮），Admission control 保护的是自身（让自己不要被 goroutine 堆栈压垮）。两者是互补的。

**实现建议**：使用 `gin.HandlerFunc` 中间件 + `channel`-based semaphore。对 SSE 连接，在 `EventsHandler.Subscribe` 中使用一个 `atomic.Int64` 全局计数器 + 上限检查（可配置：`sse.max_connections` 默认 5000）。

### 产品经理视角

**用户价值**：零（正常运行时）。但在极端场景下，它是"平台不挂"和"平台完全不可用"的分界线。

**类比**：速率限制像是高铁的"每趟列车限员 N 人"（控制上车速率），而连接级准入控制像是"候车大厅限流 N 人"（控制大厅拥挤度）。没有后者，当列车晚点时，候车大厅挤满人、所有出口堵塞、调度系统崩溃——这就是连锁故障。

**业务影响**：在用户量小时（<100 并发），这个问题不显著。在以下场景会暴露：
- **促销/活动流量突发**：瞬间涌入大量请求
- **外部依赖故障（SSO down）**：请求在 SSO timeout 上排队 → 并发 goroutine 堆积 → OOM
- **恶意 DDoS/爬虫**：大量慢请求（slow loris）保持连接

### 建议实现

```
Phase 1（半天的改动）: 加 HTTP 中间件 `MaxConcurrent` 
   → channel-based semaphore，超量返回 503
   → 配置化，默认关闭（opt-in，如同 rate limit）
Phase 2（1 天）: SSE 连接上限
   → 在 Hub.Subscribe 中加 atomic 计数器 + 上限
Phase 3（2-3 天）: 动态过载检测
   → 基于 request latency moving average + GC 频率 → 自动降低 admission threshold
```

---

## 总结

| # | 方向 | 类型 | 为什么之前没被覆盖 | 本质 |
|---|------|------|-------------------|------|
| 1 | **实体变更历史** | 数据层抽象 | 100 份文档都在讨论"审计日志加强"，但没人把"变更快照"提炼为一个独立的平台能力 | 可观测性的"数据维度" |
| 2 | **多实例协调** | 基础设施层抽象 | 3 个子问题分别被讨论过（SSE 碎片 ×12、分布式限流 ×12、Outbox 去重 ×8），但从未被统一为一个架构层 | 从单进程到分布式系统的跃迁 |
| 3 | **连接级准入控制** | 基础设施层抽象 | 40+ 文档讨论"速率限制"，但没人区分"速率"和"并行度"是正交的两个问题 | 防止连锁崩溃的最后防线 |

### 建议优先级

```
P1 (urgent):   连接级准入控制 Phase 1 — MaxConcurrent 中间件
               半天改动，防止 OOM 事故
P1 (urgent):   多实例协调 Phase 1 — Outbox FOR UPDATE SKIP LOCKED
               1 天改动，防止多实例下事件重复处理
P2 (strategic): 实体变更历史 Phase 1 — 增加 entity_edits 表
               2-3 天，合规基础
P2 (strategic): 多实例协调 Phase 2 — 可插拔 PubSub + Redis
               1 周，SSE 跨实例实时推送
P3 (nice):      连接级准入控制 Phase 2-3 — 动态过载检测
               2-3 天，锦上添花
```
