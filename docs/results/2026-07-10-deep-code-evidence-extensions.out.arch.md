现在我对项目和已有分析文档有了全面了解。以下是基于 aero-id 当前代码库和架构文档的综合架构分析。

---

# aero-id 架构深度分析与战略扩展建议

> 分析基于 `main` 分支（2026-07-11）全代码扫描及 130+ 份既有分析文档（`docs/requirements/`）。
> 本分析的定位：**在已有分析的盲区上叠加，不重复已有方向**（如 checksum 验证、多设备同步、群组消息、分布式限流等已有充分讨论）。

---

## 1. 架构评估

### 1.1 现有架构的核心优势

aero-id 的架构设计品质超出一般 MVP 水平，有几个值得特别指出的优势：

| 优势 | 具体表现 | 架构价值 |
|------|---------|---------|
| **严格的分层边界** | handler → service → repository/SSO/storage，每层仅有单向依赖 | 替换传输层、存储后端、认证源均只需改一层 |
| **事务性 Outbox** | 写路径通过 outbox 保证业务操作与事件发布的原子性 | 规避了微服务领域最棘手的一致性问题 |
| **断路器设计精度** | 仅在 `Unavailable/DeadlineExceeded` 时触发，不因业务错误误开 | ADR-004 明确记录了此决策的权衡，实现正确 |
| **配置可追溯** | 所有密钥通过环境变量注入，无硬编码；配置加载时验证 | 生产安全性好，且避免配置漂移 |
| **幂等性设计** | 写操作全链路支持幂等性 key，包括 gRPC 侧 | 应对客户端重试时唯一可靠的方式 |

### 1.2 架构债务与技术债

以下债务按影响严重程度排列：

**债务 A — 测试分层断裂（严重性: 高）**

`internal/middleware/` 下的 6 个文件（auth.go, ratelimit.go, metrics.go, recovery.go, cors.go, request_id.go）**完全无单元测试**。这些是每个请求的必经路径，任何回归都直接影响生产。当前仅靠集成测试兜底，但集成测试覆盖的路径远少于单元测试。

更严重的是跨域（cross-cutting）的逻辑缺乏抽象：
- `auth.go` 中的 token 校验逻辑与 SSO 客户端紧耦合
- 没有 middleware 的独立接口定义 → 无法 mock 测试

**债务 B — Outbox 优雅关闭缺失（严重性: 中-高）**

`outbox/relay.go` 使用 ticker 轮询，`stopRelay()` 只是停止 ticker 循环，不执行最后一次 drain。快速重启（蓝绿部署、滚动更新）时，`pollInterval` （当前 5s）内未发布的 outbox 事件**静默丢失**。虽然数据库中的 event 不会丢（`published_at IS NULL`），但通知写入和 webhook 投递的延迟被放大。

**债务 C — 上下文传播不一致（严重性: 中）**

后台 goroutine 中多处使用 `context.Background()`：
- `storage/gc` sweeper
- `outbox/relay` 中 `FetchUnpublished` 后的 handler 调用
- `sso` 客户端中的缓存预热和健康检查

这意味着后台操作的 trace 链断裂，无法与前端请求关联。在诊断 outbox relay 延迟或 GC 误删问题时，链路追踪的缺失显著增加了 MTTR。

**债务 D — 无实例间协调机制（严重性: 中）**

非功能相关，但直接影响多副本部署的正确性：
- Rate limiter per-instance：N 副本 → 有效吞吐 × N，限流形同虚设（已有分析，不展开）
- Outbox relay 多实例同时轮询：竞态条件未防护（`SELECT ... FOR UPDATE SKIP LOCKED` 缺失）
- Notification collapse 多实例各自为政：同一用户多次收到同一通知（`FindRecentByTypeAndActor` 基于内存时间窗口）

**债务 E — Schema 与存储元数据耦合（严重性: 低-中）**

`File.Meta` 被设计为自由 schema（JSON），但缺乏：
- Schema 版本标记
- 字段注册表（哪些 key 被管线/缩略图/GC 使用）
- 迁移策略（改造型 vs 追加型）

当前可以正常工作，但随着管线（方向 3）和更多元数据的加入，会对 Meta 进行无约束的追加写入，导致未来解析困难。

### 1.3 关键设计决策回顾

| 决策 | 评价 | 
|------|------|
| 双传输共享 service 层 | **正确**。ADR-001 的取舍合理，service 方法签名已做到无 Gin/gRPC 依赖 |
| SSO 唯一认证源 | **正确**。ADR-002 的安全价值高，但缺少 token 吊销的传播机制（SSO 侧职责）|
| 事务性 Outbox | **正确**。ADR-003，但缺少"至少一次"交付保证的双重确认机制 |
| 方向性阻塞 | **正确**。ADR-005 的语义清晰，但忽略了一种场景：用户 B block A 后，A 是否应被告知？当前无反馈 |
| 禁止 `CREATE INDEX CONCURRENTLY` | **技术上保守但正确**。事务内 CONCURRENTLY 非法，但对于大表上的索引创建，停机窗口会很长 |

---

## 2. 扩展方向（架构层面）

以下扩展方向与已有 130+ 份分析文档**不重叠**，聚焦于架构层面的空白。

### 方向一：事件驱动 Schema Registry 与事件版本管理（Event Schema Governance）

**为什么需要**

当前 outbox 系统工作得非常好，但存在三个架构层面的缺失：

1. **事件 Schema 无注册表**——`outbox.Event` 的 `Payload` 是 `[]byte` 表示的 JSON，没有任何契约约束。消费者（NotificationDispatcher, WebhookHandler）通过隐式的字段名访问，没有编译期验证。
2. **无版本演进策略**——当事件 payload 需要增加字段时，旧的 `webhook_subscriptions` 收不到新字段，但 outbox 也无法通知它们"这是一个新版本的事件"。
3. **无跨域事件目录**——`api/proto/` 下有 gRPC 的 `.proto` 定义，但 outbox 事件没有 proto 定义。两者是割裂的：gRPC 响应和事件 payload 包含相同的数据，但走不同的序列化路径。

**核心挑战**

- 事件 schema 需要与 gRPC `.proto` 共享类型定义，避免重复
- 版本策略（向上兼容：追加字段 vs 向后不兼容：删除/重命名字段）
- 已有 outbox 行（DB 中 7+ 事件类型）的迁移成本

**架构变更**

```
当前:  outbox.Event.Payload → json.RawMessage（无 schema）

目标:
  api/event/v1/
    file_uploaded.proto
    message_sent.proto
    comment_created.proto
    ...
  ↓ protoc → Go types in internal/domain/events/
  ↓ outbox.Event.Payload = protojson.Marshal(eventV1)
```

- `domain/events/` 包定义每个事件类型的 Go struct，带 proto tag
- Outbox 写入时校验 payload 是否符合已知 schema
- Webhook `Content-Type: application/cloudevents+json` 头部指示事件版本

**对现有系统的影响**

- 低影响：只需修改 `outbox.Event` 的写入路径
- 现有事件在 `payload` 列中存储为 JSON，无需回填迁移（读时兼容新旧格式）
- Webhook 接收方的 breaking change 需通过新的 subscription `api_version` 字段控制

---

### 方向二：SLO 驱动的可观测性框架（SLO-Driven Observability）

**为什么需要**

当前的可观测性层完整（RED metrics + OTel tracing + 健康检查），但是**没有回答业务问题**：

- "消息发送的 P99 延迟是多少？" → 可以算，但需要从 `http_request_duration_seconds` 中手动筛选
- "文件上传成功率低于 99.5% 了吗？" → 不知道，没有 SLO 指标
- "过去 1 小时有多少 outbox 事件进入了死信队列？" → 没有暴露
- "SSO 降级期间，有多少请求使用了 stale cache？" → 没有计数

**核心挑战**

- SLO 指标需要跨请求类型聚合（HTTP + gRPC 度量同一操作可能走不同的 metric label）
- 业务 SLO（文件上传成功率 > 99.5%）和技术 SLO（HTTP 500 < 0.1%）需要分离
- 告警疲劳：SLO 驱动意味着对每个燃烧率（burn rate）都需要有告警策略

**架构变更**

```
当前:  metrics.Registry → prometheus.Counter/Histogram（技术指标）

目标:
  type SLI struct {
    Name        string
    Description string
    GoodTotal   *prometheus.CounterVec  // 成功 + 预期的失败
    ValidTotal  *prometheus.CounterVec  // 所有合法请求（排除爬虫等）
    Latency     *prometheus.HistogramVec
  }
  SLO {
    Target     float64 // e.g. 0.995
    Window     time.Duration // 30d
    BurnRate   float64 // 告警阈值 e.g. 2x/6x/24x
  }
```

需要现成的实现模式——**Google SRE Workbook** 中的四种燃烧率告警规则（`Multiwindow-Multi-Burn-Rate`）在 Prometheus 中天然可表达，但 aero-id 当前没有预定义的 recording rules 或 alerting rules。

**对现有系统的影响**

- 中影响：需要对每个 handler 增加 SLI 计数器（good/valid/total）
- 不影响现有 metrics 命名（追加 `_sli_*` 命名空间的 counter）
- 需要在部署仓库中增加 PrometheusRule 资源
- 关键路径（file upload, message send, comment create）优先实施

---

### 方向三：领域事件溯源与审计重构（Audit → Event Sourcing Light）

**为什么需要**

当前审计系统（`audit_events` 表）是 fire-and-forget 的旁路写入：

```go
// 在 service 中：
auditRepo.Log(ctx, AuditEntry{...}) // fire and forget
```

这意味着审计日志与业务操作**没有原子性保证**。更关键的问题是：

- 审计事件和 outbox 事件包含**重叠但不一致**的数据
- 当 audit write 失败时，业务操作已成功，审计静默丢失
- 没有回放机制：无法从一个时间点重建某个实体的状态
- 审计事件的 schema 只在 `AuditEntry` 结构体中描述，无法被外部系统消费

**核心挑战**

- 审计和 outbox 是两种不同性质的事件：outbox 触发副作用，审计用于合规追溯
- 将它们合并会增加 outbox payload 的大小，但重复写入两条记录是不一致的来源
- 事务性审计写入（在业务事务中 INSERT audit_events）会延长事务持有锁的时间

**架构变更**

选项 A（推荐）：**统一事件表 + materialized view**

```
事件表 (domain_events)
  id          uuid PRIMARY KEY
  aggregate_type  text  -- "message", "file", "comment"
  aggregate_id    text
  event_type      text  -- "sent", "read", "deleted"
  payload         jsonb
  metadata        jsonb  -- {actor_uid, source_ip, idempotency_key}
  created_at      timestamptz

-- materialized view for audit compliance
audit_view: filter WHERE event_type IN ('sent','deleted','shared','permission_changed')
            + metadata 中的 actor 信息
```

选项 B（渐进）：**outbox + audit 双写入，事务化**

当前两个写入路径（`outboxRepo.Append` 和 `auditRepo.Log`）在 service 层被事务包裹，但 audit 不在事务内。改为将 audit entry 也写入 outbox（作为独立的 `audit.*` 事件类型），由 `AuditWriter` handler 消费写入 `audit_events` 表。

**对现有系统的影响**

- 选项 A 影响大（需要迁移现有 `audit_events` 数据到新模型）
- 选项 B 影响小（仅增加一个 outbox handler + 事件类型），但两阶段写入的原子性问题未根除
- 推荐选项 A 作为 P2 目标，在已有的 outbox 模型上逐步进化

---

### 方向四：配置运行时反射与自检接口（Config Introspection API）

**为什么需要**

当前配置系统（Viper + `config.Load`）是一次性的：启动时加载，运行时不变化。

在运维场景中，一个健康的全局配置系统应当支持：

| 场景 | 当前能力 | 需要 |
|------|---------|------|
| 调试时检查当前配置 | 重启后从日志中 grep | `GET /debug/config` （敏感值 mask）|
| 比较部署版本间配置差异 | 人工 diff configs/config.yaml | 运行时 `/debug/config/diff?baseline=...` |
| 验证配置变更效果 | 改文件 → 重启 | 只读管理端点，支持 dry-run |
| 运行时调整限流参数 | 需改配置重启 | `PATCH /debug/config` 支持有限字段的热更新 |

**核心挑战**

- 哪些字段可以热更新（rate limit RPS/Burst）vs 哪些不能（DB DSN, SSO endpoint）
- 热更新时的竞态条件（rate limiter 更新时是否有正在进行的请求持有旧值）
- 安全性：`/debug/config` 必须严格限制为 ops 角色可访问，且 mask 数据库密码、signing key 等敏感字段

**架构变更**

```go
type ConfigManager struct {
    mu       sync.RWMutex
    cfg      *config.Config
    liveload map[string]ConfigField  // 可热更新的字段及其 setter
}

// 每个可热更新字段注册一个 setter
func (m *ConfigManager) RegisterLiveField(path string, setter func(val any) error)
```

- `handler/http/debug.go` → 新增 debug handler（只在 `--debug` 模式或特定角色下注册）
- 每个可热更新字段需要在 `config.go` 中声明 `live: true` tag

**对现有系统的影响**

- 低影响：可选功能，默认不启用
- 不影响现有配置加载路径
- 安全性依赖于 middleware 的 role check（`admin.roles` 已有 `admin.access` 权限可用于此）

---

### 方向五：架构合规性自动审计（Architecture Constraint Automation）

**为什么需要**

当前约束（`AGENTS.md` 中的 500 行/文件、50 行/函数、复杂度 ≤10）依靠 `make check-file-sizes` 和 `golangci-lint` 在 CI 中执行。

但架构层面的约束没有被检查：

| 约束 | 当前 | 需要 |
|------|------|------|
| handler 层不调 repository | 依赖人工 review | `go vet` 级别的 import 规则 |
| service 层不引入 gin/grpc | 依赖人工 review | 同理 |
| storage 实现不依赖 service | 依赖人工 review | 同理 |
| domain 不导入 external 包 | 代码 review 未覆盖 | analogous to `internal/pkg` 的 lint 规则 |
| gRPC handler 和 HTTP handler 行为一致 | 靠集成测试（覆盖不全）| 接口契约测试 |

**核心挑战**

- Go 没有官方的"禁止 import"机制——需要通过自定义 `go vet` 检查器或 `staticcheck` 的 import 规则
- 约束的演进：新 package 创建后需要自动知晓其被允许的 import 范围
- 禁止 import 和需要 import 的边界会变化（如 domain 需要引入 `sso` client 时）

**架构变更**

```go
// 在 internal/pkg/lint/ 中定义架构规则：
var ArchRules = []Rule{
    // handler → service → repository
    {Pkg: "internal/handler", AllowedImports: []string{"internal/domain", "internal/service", "internal/pkg"}},
    {Pkg: "internal/service", AllowedImports: []string{"internal/domain", "internal/repository", "internal/sso", "internal/storage", "internal/pkg"}},
    {Pkg: "internal/repository", AllowedImports: []string{"internal/domain", "internal/outbox", "internal/pkg"}},
    {Pkg: "internal/domain", AllowedImports: []string{"internal/pkg"}},
}
```

- 通过自定义 `go vet` 分析器（`golang.org/x/tools/go/analysis`）实现
- 在 `make lint-strict` 中集成

**对现有系统的影响**

- 低影响：新增一个 vet 分析器，不改变现有代码
- 初始运行时可能会发现少量违规（如某些 service 错误码按枚举直接依赖了 `pkg/errors`，这是允许的）
- 需要在 AGENTS.md 中更新约束规则

---

## 3. 接口设计建议

### 3.1 Outbox Event 接口进化

当前：
```go
type Event struct {
    ID            uuid.UUID
    AggregateType string
    AggregateID   string
    EventType     string
    Payload       json.RawMessage
    Metadata      outbox.Metadata
    CreatedAt     time.Time
}
```

建议进化方向：

```go
// Phase 1 — Schema 版本化
type Event struct {
    // ... 现有字段
    SchemaVersion uint32   // 新增：payload 的 schema 版本
    EventSchema   string   // 新增："urn:aero-id:message.sent:1"
}

// Phase 2 — 事件链追踪
type Metadata struct {
    TraceID       string   // 新增：本事件的 trace
    CausationID   uuid.UUID // 新增：产生本事件的事件 ID（事件溯源）
    CorrelationID string   // 新增：跨多个事件的业务关联 ID
}
```

**向后兼容性策略**：
- `SchemaVersion == 0` 视为旧事件，`domain/events` 中定义默认解析器
- `EventSchema` 为可选字段，只有注册在 registry 中的 schema 才校验 payload

### 3.2 Service 层接口设计原则

当前 service 层存在的问题：

1. **方法签名膨胀**——`FileService` 有 7+ 个 public method，其中许多接受 4+ 参数
2. **返回类型不一致**——某些方法返回 `(*domain.File, error)`，某些返回 `([]domain.File, *Page, error)`
3. **回调型参数**——`WithBlockChecker`、`WithMessageAuditLogger` 等 option 模式是好设计，但不应是每个 service 构造函数的标配

建议统一的 service 接口协议：

```go
// 输入：xService.XxxInput
// 返回：(xXxxOutput, error)
// 构造：NewXxxService(repo, opts...)  // opts 仅用于可选的外部依赖

type SendMessageInput struct {
    Actor     string    // from ctx
    ToUID     string
    Content   string
    FileID    *uuid.UUID
    IDempotencyKey string
}

type SendMessageOutput struct {
    Message   *domain.Message
    CreatedAt time.Time
}
```

**原则**：
1. 每个 public method 接受唯一的 Input struct，包含 actor（从 ctx 提取后传入）
2. 返回唯一的 Output struct，保持扩展性
3. Option 模式仅用于**可选的**外部依赖（block checker, audit logger），核心依赖（repo, SSO client）由构造函数参数传入

### 3.3 是否需要新的抽象层

**需要** — `Domain Event Bus` 抽象

当前 outbox 的消费逻辑是硬编码的：

```
outbox.Relay → NotificationDispatcher (+ optional webhook)
```

如果未来需要增加 Push Notification worker、缩略图管线 worker、审计日志 writer、数据回放工具，每次都需要修改 relay 的 handler chain。建议引入事件总线抽象：

```go
type EventBus interface {
    Publish(ctx context.Context, events ...Event) error
    Subscribe(eventType string, handler EventHandler)
    // 用于异步 worker 的阻塞读取
    Consume(ctx context.Context, handler EventHandler) error
}
```

- 当前 `Repository.Append` 作为 `Publish` 的实现
- `Relay.Start` 作为 `Consume` 的实现
- 新 worker 只需调用 `bus.Subscribe("file.uploaded", thumbnailHandler)`

**不需要** — 引入 `Application Layer` 或 `UseCase Layer`

当前 `service` 层已经承载了业务编排。新增应用层会重演 handler 层的协议转换问题。

---

## 4. 技术选型

### 4.1 是否需要新的技术栈

| 方向 | 需要引入 | 替代方案 | 建议 |
|------|---------|---------|------|
| 分布式限流 | **Redis** | PostgreSQL advisory lock（性能隐患）| 需要，但作为可选依赖 |
| Push Notification | **FCM/APNs** HTTP client | 轮询（无法满足移动端需求）| 需要，新的 outbox handler |
| 缩略图处理 | **libvips** / **ffmpeg** | 调用外部服务（额外部署）| 建议自建，通过 outbox worker 异步处理 |
| 事件 Schema Registry | **protobuf**（已有依赖） | JSON Schema registry | 不需要新依赖，利用现有 protoc 工具链 |
| 混沌工程 | **toxiproxy** / **bytehound** | 无 | 测试基础设施，非运行时依赖 |
| 配置热更新 | 无（Go `atomic.Value` + `sync.RWMutex`）| viper WatchConfig | 不需要新依赖 |

**Redis 的决策框架**：

| 维度 | 分析 |
|------|------|
| 必要性 | 只有在多副本（≥2）生产环境时才是必需的 |
| 降级能力 | 所有 Redis 功能必须有 memory fallback（当前 `ratelimit` 已是 memory） |
| 运维成本 | 需要管理 Redis 连接池、超时、failover（当前 sso 客户端的模式可复用） |
| 现有代码影响 | 接口层（`Limiter`, `IdempotencyStore`）已抽象，只需新增 `redis` backend 实现 |

**建议**：Redis 作为**可选**后端引入，`config.Storage.Type: redis`，与 `storage.type: local|s3` 模式一致。

### 4.2 自建 vs 采购

| 场景 | 自建 | 采购/OSS集成 | 推荐 |
|------|------|------------|------|
| Spam 检测 | 简单关键词/正则黑名单 | 集成 Akismet / 开源 spam 引擎 | 初期自建，后期可插拔接口 |
| 缩略图管线 | Go `imaging` / `libvips` binding | Cloudinary / imgix（SaaS）| 自建 + 可插拔 `ThumbnailGenerator` 接口 |
| Push Notification | `firebase.google.com/go/messaging` | 无 SaaS（是 SDK）| OSS SDK 是唯一选择 |
| 内容审核 | 自建队列 + 人工 | 外部审核 API | 自建（已有 `ReportComment` 基础设施）|

**自建 vs 采购的决策原则**：

```
是否核心差异化能力？
  ├── 是 → 自建（消息、文件、评论的核心逻辑）
  └── 否 → 是否有成熟 OSS？
       ├── 是 → 集成 OSS（protobuf、gin、gorm）
       └── 否 → 是否高运维成本？
            ├── 是 → 采购 SaaS（适用于极度专业的领域如 OCR、视频转码）
            └── 否 → 自建轻量实现
```

---

## 5. 实施路线图

### 优先级排序

| 优先级 | 方向 | 估算 | 依赖性 | 风险 |
|--------|------|------|--------|------|
| **P0** | SLO 驱动的可观测性框架（方向二） | 3-5 天 | 无 | 低（追加指标，不改行为）|
| **P0** | 架构合规性自动审计（方向五） | 2-3 天 | 无 | 低（可选 vet，不阻塞构建）|
| **P1** | 配置运行时自检接口（方向四） | 3-5 天 | admin role 基础设施 | 中（安全敏感的调试端点）|
| **P1** | Domain Event Schema 注册表（方向一） | 5-8 天 | protobuf 工具链已就绪 | 中（已有事件的兼容性）|
| **P2** | 审计→事件溯源（方向三） | 8-12 天 | 方向一事件 schema | 高（数据迁移复杂）|

### 阶段划分

**Phase 0 — Observability Foundation（P0，1 周）**

里程碑：
- [ ] 实施 Multi-Window-Burn-Rate SLO 指标（file.upload, message.send, comment.create）
- [ ] 增加 `metrics/sli.go` 通用 SLI 工具
- [ ] 增加 Prometheus alerting rules（`rules/aero-id-slo.yml`）
- [ ] 实施自定义 `go vet` 分析器 `archlint`
- [ ] 在 `make lint-strict` 中集成架构约束检查
- [ ] 清理第一批违规

风险与缓解：
- **风险**：SLO 指标增加额外的 counter 和 latency 开销
- **缓解**：预分配 label 值避免热路径上的内存分配；通过 `metrics.WithRegister` 延迟注册

**Phase 1 — Ops Quality（P1，1-2 周）**

里程碑：
- [ ] 实现 `GET /debug/config` + `GET /debug/config/diff?commit=`（敏感值 mask）
- [ ] 实现 `rate_limit.rps` 热更新（通过 `ConfigManager.RegisterLiveField`）
- [ ] 定义 `api/event/v1/*.proto` 事件 schema
- [ ] 实现 `domain/events/` 包 + 事件验证
- [ ] Webhook 发送 `application/cloudevents+json` 格式

风险与缓解：
- **风险**：配置热更新导致稳定性问题
- **缓解**：只允许数值和 bool 字段热更新；对每个 setter 进行 `(old, new)` 校验；变更记录到 audit log
- **风险**：事件 schema 注册增加写入延迟
- **缓解**：schema 验证是可跳过的（`assertValid` 在 `-tags=debug` 下编译），生产默认跳过验证

**Phase 2 — Correctness（P2，2-3 周）**

里程碑：
- [ ] 设计 `domain_events` 统一事件表 schema
- [ ] 编写数据迁移（将现有 `audit_events` + `outbox_events` 合并）
- [ ] 修改 service 层：所有状态变更 → outbox 事件 + 物化 audit view
- [ ] 实现事件回放 CLI 工具（`cmd/replay`）
- [ ] 端到端集成测试验证

风险与缓解：
- **风险**：数据迁移导致生产可用性受损
- **缓解**：双写模式（新旧同时写入）运行一个 TTL 周期后切换；提供回滚脚本
- **风险**：事件溯源思维转变需要团队适应
- **缓解**：从"轻量审计"切入，不强制所有 domain 使用——file 和 message 可以先试点

### 风险全景矩阵

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| 事件 schema 版本化导致旧 webhook 接收方 break | 中 | 高 | 平滑版本升级：`api_version` 字段 + 默认兼容模式 |
| 配置热更新被误用导致服务异常 | 低 | 高 | 细粒度权限 + 变更审计 + 自动回退（5min 无心跳 revert）|
| 架构 vet 规则过严阻碍开发 | 中 | 低 | `-tags=strict` 控制；CI 中仅阻止 main 分支，feature 分支只 warning |
| 统一事件表迁移导致数据丢失 | 低 | 极高 | 先克隆线验证 + 双写 + 保留旧表 30d 回滚窗口 |

---

## 总结

aero-id 的架构在 Go 微服务领域中属中上水平。当前的主要矛盾是：

> **代码质量和架构纪律已超过 MV P 阶段应有的水平，但工具化和自动化的闭环尚未跟上。**

已有的 130+ 分析文档覆盖了功能扩展方向（群组消息、多设备同步、文件管线、限流、webhook 等），我在本分析中聚焦于 5 个**架构基础设施方向**——它们不直接增加用户可见的功能，但降低了后续所有扩展的交付风险。

**立即行动项（P0）**：
1. 实施 SLO 框架——让可观测量化而非停留在 RED 指标
2. 实施架构合规审计——让 `AGENTS.md` 中的约束可执行而非靠 review

**下阶段（P1）**：
3. 配置自检——减少排障 MTTR
4. 事件 schema 治理——为 webhook 平台和事件溯源铺路

**长期（P2）**：
5. 轻量事件溯源——根治审计一致性问题

每个方向都有明确的"最小可行"切入点（P0 方向的增量在 2-5 天内），可在不阻塞已有功能开发的前提下并行推进。
