Now I have a comprehensive picture of the project. Let me produce the architectural analysis.

---

# 架构分析：aero-id 技术设计评估与演进方向

## 1. 架构评估

### 当前架构的优势

aero-id 在工程层面有几个值得称道的设计决策，从代码扫描和 ADR 记录来看，团队在取舍上有清晰的优先级：

**强分层边界（最高价值资产）。** handler → service → repository 的单向依赖链被严格执行。service 层是唯一的业务逻辑场所，handler 仅做协议转换。这个看似简单的约束在双传输（HTTP + gRPC）场景下产生了显著的工程回报——所有 38 个 gRPC RPC 与对应的 HTTP 路由行为完全一致，无分支逻辑重复。

**SSO 依赖的容错设计。** 断路器的状态机（Closed/Open/Half-Open）与错误分类逻辑（仅 `Unavailable`/`DeadlineExceeded` 触发熔断，业务错误不触发）是正确的设计。serve-stale cache fallback 配合 singleflight 消减缓存击穿，使 SSO 瞬间不可用时 aero-id 仍能服务已有会话。这是很多早期项目忽略的关键弹性。

**Outbox 模式的选择。** 在缺乏消息队列基础设施的前提下，事务性 outbox + relay 轮询是实现"写操作与异步副作用原子提交"的最优零依赖方案。代码中 `NotificationDispatcher` 在 outbox handler 链中与 webhook handler 组合（chain of responsibility），结构清晰，便于未来扩展。

**文件管道成熟度。** 流式上传、内容去重（checksum 计算）、MIME 类型白名单、presigned URL 直传 S3、GC 回收孤儿 blob——这条路径从上传到生命周期终结有完整的覆盖，远超一般 MVP 的文件处理水平。

### 关键设计缺陷与技术债

**双代码库分歧（最紧迫的架构债务）。** 这是当前架构中最危险的问题。代码中存在两套平行的服务实现：

| 包 | 示例文件 | 当前状态 |
|---|---|---|
| `internal/service/` | `file.go`, `message.go`, `comment.go` | **活跃使用** — `app.go` 注入的是此包 |
| `internal/comment/`, `internal/file/`, `internal/message/` | `service.go`, `handler_http.go` | **未使用**，但持续维护 |

域包（`internal/comment/` 等）有自己的 `Repository` 接口、`serviceImpl`、完整的 HTTP 和 gRPC handler——它们看起来像是架构迁移过程中的一个中间产物，但迁移从未完成。这导致：

- 同一域（如 comment）的领域逻辑可能分布在两个包中，埋下行为分歧的风险
- 新加入的开发者在两个结构之间抉择时无所适从
- 测试覆盖率被稀释（一处在 `internal/comment/service_test.go`，另一处在 `internal/service/comment_test.go`）

**决策建议**：立即选择一条路径并清理另一条。从 `app.go` 的注入方式看，保留 `internal/service/` 包、删除域包是最低成本的路径。或者把迁移继续走完——将 `internal/service/` 中的逻辑迁移到域包中，更新 `app.go` 注入点。第三条路（保留两个）是不可接受的。

**Hub（SSE）是单点瓶颈。** `internal/hub/hub.go` 中 `sync.Map` 加 per-uid 锁的设计在单进程内是优雅的，但它是**进程内状态**。多实例部署时：

- 用户连接实例 A，通知事件可能被 outbox relay 在实例 B 上处理
- 实例 B 发布消息到其本地 Hub，实例 A 上的 SSE 连接永远不会收到
- 结果是：多实例+SSE=功能损坏

当前项目有一个实例（单进程部署），这个设计是合理的。但如果部署策略是水平扩展（对生产环境这是必然的），SSE 推送会静默失效。这需要在上线前解决，而非等到用户投诉。

**速率限制是 per-instance 且无可降级路径。** `internal/pkg/ratelimit/ratelimit.go` 的注释诚实地说道："PER-INSTANCE: distributed/global rate limiting would need shared state (e.g. Redis) and is intentionally out of scope"。这意味：
- N 实例 = N × 速率配额，攻击者只需 N 倍放大
- 限流从"安全边界"降级为"聊胜于无"
- 用户在某些实例上被限流，刷新后可能通过另一个实例继续操作

Per-instance 限流对慢速攻击（slow-rate attack）完全无效。

**通知延迟有硬上限。** Outbox relay 的轮询间隔（默认 5 秒）决定了通知延迟的下界。消息发送到通知推送之间有 5 秒的固定延迟，这对消息场景可能可接受，但对评论回复、@提及等低延迟期望的场景是不够的。更根本的是，轮询模式在低流量时浪费资源（空轮询），在高流量时引入批处理延迟。

---

## 2. 扩展方向

### 方向一：多实例部署就绪（P0 — 上线前提条件）

**为什么需要。** 当前架构假设单实例运行。Hub（SSE）是进程内状态，速率限制是 per-instance，outbox relay 多实例冲突（每个实例独立 `FetchUnpublished`，幂等靠 `PublishedAt` 去重但浪费资源）。如果生产部署策略是水平扩展（哪怕只有 2 副本），这些都会静默损坏。

**核心挑战。** 

1. **跨实例 SSE 分发**：实例 A 处理了消息发送事件，需要将通知推送到实例 B 上的 SSE 连接。这需要一个跨实例 pub/sub 通道。
2. **分布式速率限制**：需要一个共享的水位状态，所有实例原子地读写。
3. **Outbox leader 选举**：避免所有实例同时轮询同一张 outbox 表。

**方案选择与权衡。**

| 方案 | 跨实例 SSE | 速率限制 | Outbox 协调 | 新依赖 | 复杂度 |
|------|-----------|---------|------------|-------|-------|
| A: PG 原生 | PG LISTEN/NOTIFY + advisory lock | PG advisory lock 协调内存桶 | PG advisory lock 选主 | 无 | 低 |
| B: Redis | Redis Pub/Sub | Redis 原子桶 | RedLock/Redis SETNX | Redis | 中 |
| C: 消息队列 | 队列->独立推送网关 | 网关侧统一限流 | 队列分片自动选主 | RabbitMQ/NATS | 高 |

**推荐路径**：方案 A（PG 原生）——这是该文原文作者的核心建议，我认为是完全正确的。

- 利用 `pg_advisory_lock` 实现 outbox leader 选举（已有一个 GORM 连接池可用）
- 本地内存桶处理 99% 的限流判断（零延迟），每 ~100ms 通过 PG advisory lock 同步一次全局水位
- PG `LISTEN/NOTIFY` 作为跨实例事件通道，仅在 leader 实例上启动 subscription
- PG 不可用时降级为纯本地模式（fail-open）

这一路径**零新基础设施依赖**，上线后 1-2 天内可交付。未来规模需要时才引入 Redis（那时 team 对流量特征有数据支撑，决策更有依据）。

**对现有系统的影响。** 

- Hub 继续保持为单进程 SSE 注册表 + 分发点
- 新增一个 `CrossInstanceBus` 抽象，包装 PG LISTEN/NOTIFY
- Outbox relay 增加选主逻辑，非 leader 实例跳过轮询
- Rate limiter 增加 `PGWatermark` 可插拔后端

---

### 方向二：数据生命周期管理（P0 — 合规必要条件）

**为什么需要。** 当前系统：

- 无用户级联删除（账号删除后，文件、消息、评论变为孤儿数据）
- 无数据导出接口（GDPR 数据主体访问请求无法满足）
- 消息和评论没有完整级联（删除消息不删除其评论和通知记录）
- 在 outbox 和 idempotency 表中有过期行，但无统一的 TTL 策略

**核心挑战。** 

1. **与 SSO 的解耦**：aero-id 不拥有账户数据，不知道用户何时被删除。必须设计一个机制，让 SSO 通知 aero-id 用户已删除。
2. **级联的范围**：仅删除用户直接拥有的行（文件、发送的消息、评论）是不够的——还需要清理其他用户空间中对该用户的引用（接收的消息、被 @提及的记录、block 记录）。
3. **操作的可观测性**：删除大量数据（特别是文件存储中的 blob）是长时间操作，需要进度跟踪和失败恢复。

**方案选择与权衡。**

| 方案 | 触发方式 | 一致性 | 实现成本 |
|------|---------|--------|---------|
| A: 管理员手动调用 | `POST /v1/admin/account/:uid/purge` | 最终一致（后台异步任务） | 低 |
| B: SSO 事件回调 | outbox 事件类型 `account.deletion.requested`，SSO 调 aero-id webhook | 最终一致 | 中 |
| C: 定期扫描 | 后台 Job 扫描所有 orphan uid 并清理 | 最终一致（可能有窗口） | 中 |

**推荐路径**：先用方案 A（管理员手动触发），交付时间 < 1 天。同时预留 webhook 扩展点，后续切换到方案 B。方案 C 不推荐——扫描 orphan uid 需要与 SSO 全量用户数据集做 diff，操作成本高，且存在隐私安全风险（在处理窗口期内，已删除用户的数据可能是可见的）。

**数据导出**：实现一个异步导出 Job——生成 JSON/CSV 文件放入用户的文件列表，用户自己通过文件下载端点获取。不需要实时生成，后台队列处理即可。

**对现有系统的影响。** 

- 新增 `internal/admin/purge.go` 服务（利用已有的 `AdminService` 接口）
- 新增 `data_export` 表记录导出请求状态
- 扩展现有 `file_gc.go` 的逻辑，使其也处理用户级联删除
- SSO webhook 端点（可选）复用 outbox webhook 的签名验证逻辑

---

### 方向三：策略驱动的审核引擎（P1 — 平台化基础）

**为什么需要。** 当前的审核模型停留在"评论区"：`CommentReport` 结构体、`Hide`/`Unhide`/`ResolveReport` 端点。消息和文件域完全没有审核能力——没有 `hidden_at` 或 `report` 字段。但产品需求（NSFW 内容隐藏、垃圾消息自动抑制、举报处理）是跨域的。

**核心挑战。**

1. **统一报告模型**：文件、消息、评论虽然有不同的领域对象，但举报的核心属性相同（报告者、被报告内容引用、理由、状态）。
2. **自动化策略**：如果审核策略是 if-else 硬编码在 service 层，每次新增规则都意味着 service 逻辑修改。
3. **自动与人工的衔接**：自动隐藏的内容应该进入人工审核队列，人工审核后的决策需要可追溯。

**推荐方案：Moderation Strategy Chain（责任链模式）。**

```
Service.Create
  → 持久化内容
  → ModerationChain.Execute(subject)
      → Strategy1: HashMatch (已知违规内容哈希库)
      → Strategy2: ClassifierRPC (外部分类器)
      → Strategy3: RateCheck (同一 IP 近期举报率)
  → 返回 ModerationAction{Action: hide|flag|allow, Reason: string}
  → 更新内容状态（auto_hidden_at / flagged_at）
  → 写入审计日志
```

上线初期是空链（no-op），后续注入具体策略时无需修改 service 代码。

**跨域统一报告表。**

```sql
CREATE TABLE moderation_reports (
    id              UUIDv7 PRIMARY KEY,
    reporter_uid    TEXT NOT NULL,      -- 报告人
    target_type     TEXT NOT NULL,      -- 'file' | 'message' | 'comment'
    target_id       UUIDv7 NOT NULL,    -- 对应的主键
    reason          TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',  -- pending | resolved | dismissed
    resolved_by     TEXT,               -- 处理人 uid
    resolved_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL
);
```

**对现有系统的影响。** 

- `CommentReport` 可以逐步迁移到统一表（向后兼容，先写双写）
- `ModerationStrategy` 接口定义在 `internal/domain/` 中
- 现有 `Hide`/`Unhide` 端的逻辑保留，底层指向统一状态
- 审核的 API 可同时暴露 per-domain（向后兼容）和新跨域端点

---

### 方向四：统一活动流与 @提及（P1-P2 — 产品差异化）

**为什么需要。** 当前三个域（文件、消息、评论）是互不相通的孤岛。用户无法：

- 在一个统一的时间线中看到所有与自己相关的活动
- 通过 @mention 触发通知（用户必须"知道去找"特定的消息或评论）
- 跨域搜索（消息 + 评论 + 文件在同一结果集中）

@mention 是建立跨域活动图的低投入切入点。

**核心挑战。**

1. **解析准确性**：`@handle` 需要在 service 层解析，但 handle → uid 映射只在 SSO 侧。aero-id 的 `UserCache` 是 uid 索引的，没有 handle 查找能力。
2. **通知去重**：如果同一个 @mention 出现在私信和评论中，用户不应收到两条重复的通知。
3. **搜索排序**：跨域搜索结果如何排序（时间戳？相关度？）需要统一的评分算法。

**推荐路径（两步走）。**

**第一步（无新表）**：在 `notifications` 表中新增 `notification_type = 'mention'`，`reference_type`/`reference_id` 指向源内容。正则解析仅在 service 层完成，调用 SSO `ResolveHandle` 方法（需要 SSO 端先暴露此 RPC）。复现通知系统的已有折叠/推送机制。

**第二步**：当 @mention 流量达到规模后，建立 `mentions` 表：

```sql
CREATE TABLE mentions (
    id          UUIDv7 PRIMARY KEY,
    mentioner_uid TEXT NOT NULL,     -- @ 的人
    mentioned_uid TEXT NOT NULL,     -- 被 @ 的人
    target_type TEXT NOT NULL,       -- 'message' | 'comment'
    target_id   UUIDv7 NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_mentions_uid ON mentions(mentioned_uid, created_at DESC);
-- unique: (mentioner_uid, mentioned_uid, target_type, target_id)
```

**对现有系统的影响。** 

- 通知模型增加 `mention` 类型（不影响现有枚举）
- Service 层增加 `@handle` 解析，位置在 `CreateComment` / `SendMessage` 之后
- SSO 客户端增加 `ResolveHandle` RPC（若 SSO 不支持，则降级到不解析）

---

### 方向五：可观测性与运营成熟度补全（隐含 P0）

虽然 ROADMAP 中标注方向一（可观测性）已完成，但代码扫描显示仍有缺口：

1. **健康检查不够细粒度**：`/readyz` 返回整体状态（OK/FAIL），但没有 per-upstream 的健康面板。SSO 断路器状态、outbox relay 滞后、storage GC 进度等信息无法通过健康端点获取。
2. **RED 指标仅覆盖请求路径**：outbox relay（处理延迟、积压、死信数）、storage GC（清理量、耗时）、SSO cache（命中率、过期驱逐率）没有指标。
3. **无结构化日志上下文**：当前日志通过 `zap.Logger` 写入，但缺乏跨请求的 trace 关联。`request_id` 在 HTTP handler 级别生成，但 gRPC 请求、outbox relay 处理等场景没有统一的 trace ID。

**推荐方案。**

1. **扩展健康端点**：`/readyz?verbose=true` 返回 JSON 格式的 per-dependency 状态（DB: ok, SSO: degraded/circuit_open, OutboxLag: 2.3s, StorageGCLastRun: "3m ago"）
2. **扩展 Metrics**：在 `internal/pkg/metrics` 中注册 outbox 和 GC 自定义指标
3. **Context propagation**：在 outbox relay 和 background workers 中使用带 trace 的 context

---

## 3. 接口设计建议

### 关键接口设计原则

**领域接口 > 技术接口。** 当前 `internal/service/service.go` 中定义了 `FileService`、`MessageService`、`CommentService` 等接口。这些接口是面向 handler 层的（输入是扁平 DTO，输出是 domain 对象 + 错误码）。这是正确的选择。但在域包（`internal/file/` 等）中，类似的接口被重定义或扩展，引发了双代码库问题。

**建议的统一策略：**

```
internal/service/service.go     ← 对外接口定义（唯一）
internal/service/file.go        ← 实现
internal/domain/file.go         ← 纯数据模型
internal/repository/file.go     ← GORM 实现
```

所有接口定义集中一处，不同的包可以有不同的实现方式，但接口是唯一的契约。

### 需要引入的新抽象

**1. `CrossInstanceBus` 接口。**
```go
type CrossInstanceBus interface {
    Publish(ctx context.Context, channel string, payload []byte) error
    Subscribe(ctx context.Context, channel string, handler func(payload []byte)) (func(), error)
}
```

当前实现可以是 PG `LISTEN/NOTIFY`。未来可以切换为 Redis Pub/Sub 或 NATS。接口加在 `internal/hub/` 之下，与 Hub 组合使用。

**2. `AsyncJob` 接口。**
```go
type JobType string

type AsyncJob struct {
    ID          JobID
    Type        JobType      // 'data_export' | 'account_purge'
    Payload     []byte       // JSON-encoded parameters
    Status      JobStatus    // pending | running | completed | failed
    Progress    float64      // 0.0 - 1.0
    ResultRef   *string      // 导出文件的 file_id
    ErrorMsg    *string
    CreatedAt   time.Time
}

type JobRepository interface {
    Create(ctx context.Context, j *AsyncJob) error
    UpdateProgress(ctx context.Context, id JobID, progress float64) error
    Complete(ctx context.Context, id JobID, resultRef string) error
    Fail(ctx context.Context, id JobID, errMsg string) error
    FetchNext(ctx context.Context) (*AsyncJob, error)
}
```

**3. `ModerationStrategy` 接口。**
```go
type Subject struct {
    Type        TargetType
    ID          ID
    OwnerUID    string
    Content     string
    Meta        map[string]any
}

type ModerationAction struct {
    Action      ModerationActionType  // allow | flag | hide
    Reason      string
    StrategyName string
    Confidence  float64
}

type ModerationStrategy interface {
    Name() string
    Eval(ctx context.Context, subject Subject) (*ModerationAction, error)
}
```

### 向后兼容策略

- **新域接入**：对新域（如 upcoming `posts`）使用新的接口模式，旧域保持不动，避免触发现有端点
- **报告表迁移**：`CommentReport` 到 `ModerationReport` 的迁移采用双写策略（先写新表，异步同步旧表），待所有消费者切换到新表后删除旧代码
- **API 版本化**：当前无版本前缀以外的版本化机制。引入 `Accept: application/vnd.aero-id.v2+json` 或 URL 前缀 `/v2/` 的决策应推迟到至少有两个不兼容变更时。过早版本化是浪费。

---

## 4. 技术选型

### 当前技术栈评估

| 层次 | 当前选型 | 评估 | 建议 |
|------|---------|------|------|
| HTTP 框架 | Gin v1.10 | ✅ 适合，go 生态最佳选择 | 保留 |
| ORM | GORM v2 + PG driver | ⚠️ 功能完整但抽象渗漏（SQL 优化需要 raw queries） | 保留，但新增复杂查询用 raw SQL |
| 配置 | Viper | ✅ 标准选型 | 保留 |
| 日志 | Zap | ✅ 高性能结构化日志 | 保留 |
| 存储 | local / S3（接口） | ✅ 正确的抽象 | 保留 |
| ID 生成 | UUID v7 | ✅ 时序友好，适合 cursor 分页 | 保留 |
| 测试容器 | testcontainers-go | ✅ 集成测试的最佳实践 | 保留 |
| 迁移 | golang-migrate | ✅ 标准选型 | 保留 |

### 不需要引入的新依赖

**Redis。** 至少在 Phase 1 阶段（当前文档建议的 S1-S2 排期），PG 原生的 advisory lock + LISTEN/NOTIFY 可以覆盖所有需要分布式协调的场景。引入 Redis 意味着：

- 新增一个必须 7×24 高可用的基础设施组件（不可能仅将 Redis 设为 best-effort）
- 部署复杂度上升（Redis Sentinel/Cluster）
- 增加了每次上线前的变更面
- 延迟路径：aero-id → Redis → PG vs aero-id → PG

Redis 的引入应是一个经过数据驱动的决策——当 PG advisory lock 的延迟成为瓶颈时（通常需要 1000+ QPS per lock），或者当 PG 连接池因占用而变得紧张时，才是评估 Redis 的时机。

**消息队列（RabbitMQ / NATS / Kafka）。** Outbox 模式+轮询在当前规模是正确选择。引入消息队列意味着：

- 增加 at-least-once 或 exactly-once 的投递保证（当前已通过 `PublishedAt` 实现 at-least-once）
- 增加运维复杂性（队列监控、消费者滞后、死信管理）
- 消息队列的 backpressure 机制与 outbox 的 backpressure 机制各自独立

何时需要消息队列：当 `FetchUnpublished` 查询成为 PG 的慢查询热点时（通常 ~10K+ 未发布事件同时存在时），或者需要跨服务的事件驱动的场景。

**全文搜索引擎（Elasticsearch / Meilisearch）。** 当前使用 PostgreSQL `tsvector` + GIN 索引，配合调用者 uid 隔离。这个方案在数据集 < 100 万行时表现良好。当全文搜索成为核心用户体验（首页搜索框）时再考虑专用搜索引擎。

### 需要优先引入的新依赖

| 依赖 | 为什么需要 | 替代方案 | 建议时机 |
|------|----------|---------|---------|
| OpenTelemetry SDK（已在 go.sum 中） | 已有依赖但未充分利用 | 自建 metrics | 立即启用，完善 trace 传播 |

**这就够了。** 当前基础设施组合（Postgres + S3/MinIO + 可选本地存储）的运维复杂度极低，引入任何新依赖都必须能够回答："这个依赖的不可用时，系统是否还能降级运行？"

### 自建 vs 采购的决策框架

对于 aero-id 的场景：

| 决策 | 自建 | 采购/集成 | 判断依据 |
|------|------|----------|---------|
| 速率限制 | ✅ PG advisory lock | Redis | 自建零新依赖，引入 Redis 后迁出成本低 |
| 审核策略 | ✅ 责任链模式 | 外部审核 API | 审核策略是核心产品差异化，外包会失去控制 |
| 推送通知 | ✅ 推送网关抽象 | Firebase/APNs (移动端) | 推送网关的抽象层可以同时接入多个后端 |
| 全文搜索 | ✅ PG tsvector | Elasticsearch | 当前规模 PG 足矣，ES 是未来方案 |

---

## 5. 实施路线图

### 优先级框架

```
P0 — 不可上线        → 安全、合规、多实例正确性
P1 — 上线后尽快       → 平台化、可维护性
P2 — 产品差异化       → 用户体验、搜索、审核
P3 — 未来探索         → 活动图、AI 审核、分析
```

### 阶段划分

**Sprint S1：多实例基础（~5 天）**

| 任务 | 工时 | 依赖 | 交付物 |
|------|------|------|--------|
| PG advisory lock 工具包 | 1d | 无 | `internal/pkg/pglock/lock.go` |
| 分布式速率限制（双桶） | 2d | pglock | 限流跨实例协调，fail-open |
| Outbox leader 选举 | 1d | pglock | 单实例轮询，心跳 + 超时接管 |
| PG LISTEN/NOTIFY 总线 | 1d | 无 | `internal/hub/cross_instance.go` |
| 端到端测试（多实例场景） | 1d | 以上全部 | 集成测试覆盖多实例竞争条件 |

**关键风险**：PG advisory lock 在锁竞争下的性能表现。缓解措施：本地桶处理 99% 请求；锁请求超时后降级到本地桶（允许短期的配额偏离）。

**Sprint S2：数据生命周期（~4 天）**

| 任务 | 工时 | 交付物 |
|------|------|--------|
| 管理员 purge 端点 | 1d | `POST /v1/admin/account/:uid/purge` |
| 级联删除逻辑（文件、消息、评论、通知） | 1.5d | 统一 `CascadeDelete(uid)` |
| 异步数据导出 | 1d | 导出 Job + 通知用户 |
| TTL 清理策略（outbox + idempotency） | 0.5d | 配置化的过期策略 |

**关键风险**：级联删除在大量数据下的执行时间。缓解措施：采用批量删除（每批 100 条） + 异步 Job 模式，避免在请求路径中执行。

**Sprint S3：审核平台（~5 天）**

| 任务 | 工时 | 交付物 |
|------|------|--------|
| 统一 `moderation_reports` 表 + 迁移脚本 | 1d | 双写 + 数据迁移 |
| `ModerationStrategy` 接口定义 | 0.5d | 空链部署 |
| 消息/文件隐藏字段 + API | 1.5d | 跨域隐藏/取消隐藏 |
| 管理面板审核队列端点 | 1d | 列表 + 处理 + 追溯 |
| 内容哈希匹配策略（示范策略实现） | 1d | 可工作的策略示例 |

**关键风险**：CommentReport 的数据迁移可能导致不一致。缓解措施：先在非生产环境运行迁移，验证 `COUNT(*)` 一致后再切换。

**Sprint S4：@提及与通知增强（~4 天）**

| 任务 | 工时 | 交付物 |
|------|------|--------|
| 通知类型增加 `mention` | 0.5d | 不影响现有类型枚举 |
| Service 层 @handle 解析 | 1d | 正则解析 + SSO handle 查找 |
| 通知去重（同一事件多连接） | 1d | 通知折叠优化 |
| 未读通知批量已读优化 | 0.5d | SQL 索引优化 |
| SSE 跨实例推送（复用 S1 总线） | 1d | 多实例 SSE 推送有效 |

**Sprint S5：架构债务清理（~3 天）**

| 任务 | 工时 | 交付物 |
|------|------|--------|
| 删除未使用的域包 | 1d | 清除 `internal/comment/`, `internal/file/`, `internal/message/` |
| 将所有导入指向 `internal/service/` | 0.5d | 编译通过 |
| 测试确认无行为变化 | 1d | 全部测试通过 |
| 补充缺失的 handler 层单元测试 | 0.5d | middleware、gRPC handler 测试 |

### 风险矩阵

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| PG advisory lock 成为瓶颈 | 低（< 10 实例） | 中（延迟增加） | 本地桶兜底；锁超时降级 |
| 级联删除误删数据 | 中 | 高（数据丢失） | 软删 + TTL 保护；操作审计；管理员二次确认 |
| 多实例 SSE 推送延迟增加 | 中 | 低（UX 受影响） | 降级到客户端轮询（已有端点） |
| 审核策略误判合法内容 | 中 | 高（用户体验） | 自动化策略仅 flag + 隐藏，不可删除；申诉通道 |
| 双代码库合并冲突 | 高 | 中 | 冻结域包更改，集中清理一次完成 |
| @handle 解析与 SSO 解耦失败 | 中 | 中 | 降级到纯文本（不解析），不影响核心功能 |

### 排期汇总

```
Week 1:  S1 — 多实例就绪（不可跳过，上线前提）
Week 2:  S2 — 数据生命周期（合规前提）+ S5 开始
Week 3:  S3 — 审核平台（平台化基础）
Week 4:  S4 + S5 完成
         ← 门禁：make test && make lint && make test-integration
```

---

## 总结

aero-id 在工程层面是一个质量很高的项目——分层清晰、弹性设计扎实、基础设施占用极简。当前最需要解决的三个紧迫问题是：

1. **双代码库的分歧**（架构债务，立即清理）
2. **多实例正确性**（Hub、限流、outbox 都假设单实例，上线前必须解决）
3. **合规数据管理**（无用户级联删除、无数据导出，对于存储用户数据的服务这是不可上线的）

用户提供的分析报告及其附加的架构视角（PG 原生方案 vs Redis 方案）是正确的。引入 Redis 应该被推迟到有数据证明需要时，而非作为架构默认假设。`pg_advisory_lock` + 本地内存双桶方案是当前阶段的最优解，这一选择体现了"不做比做更重要"的架构纪律。
