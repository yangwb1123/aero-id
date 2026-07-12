现在我有了完整的项目全景。让我基于代码库的实际情况，而非假设，提供架构分析。

---

# aero-id 架构分析报告

## 1. 架构评估

### 1.1 当前架构的优势

**1. 严格的分层依赖方向——最大的架构资产**

代码库强制执行 `handler → service → repository (storage, sso)` 的单向依赖。这是整个系统中最重要的架构决策，理由如下：

- HTTP 和 gRPC 两个 handler 包同级，共享同一套 service 接口（`internal/service/service.go` 中的 `FileService`、`MessageService`、`CommentService` 等）。这确保了双协议行为一致，已通过 `parity_test.go` 验证。
- service 层是业务逻辑和授权的唯一真实来源。repo 返回原始错误，service 通过 `*.Wrap(cause)` 转换为 `AppError`，handler 只负责渲染。
- **成果**：38 个 gRPC RPC 与 HTTP 路由完全对等，没有业务逻辑泄露到 handler。

**2. 领域模型先于实现**

`internal/domain/` 包保存了不依赖框架的实体和规则。`ValidateNewComment`（嵌套约束）和 `ClassifyFile`（MIME → 分类映射）是纯函数，可独立测试。这比把规则埋在 service 层更抗腐化。

**3. 事务性 outbox 模式正确实现**

`internal/outbox/` 的 `Event.Append(tx)` 要求调用者传入写操作所在的 `*gorm.DB` 事务，保证领域事件与数据行在一个数据库事务中提交。`Relay` 使用退避 + 死信 + 指标上报，是一个健壮的实现。`internal/notification/` 和 `internal/audit/` 已利用此机制。

**4. 存储抽象具有可演进性**

`FileStorage` 接口设计良好：`Put/Get/Delete/PresignGet/Stat` 覆盖核心操作。`Lister` 作为可选的补充接口，用于 GC 的孤儿检测——这是 Go 中建模可选能力的惯用方式。

**5. 配置体系扎实**

`config.Load` 验证启动时的必需 key，每个字段都可被 `AERO_ID_*` 环境变量覆盖。SSO 客户端的重试、熔断、服务过期降级均有配置——这类生产韧性特征通常是事后补充的。

### 1.2 关键设计决策评估

| 决策 | 评价 | 风险 |
|------|------|------|
| GORM 作为 ORM | 对 CRUD 密集场景合理，但 `db.Transaction` 在代码库中几乎未被使用 —— repository 层没有将事务作为一等概念暴露 | 非原子跨聚合写入 |
| UUIDv7 主键兼作游标 | 设计优雅，避免了 `OFFSET` 分页。但 `ListAudit` 按 `created_at DESC, id DESC` 排序，而 `FetchUnpublished` 按 `created_at ASC` —— 索引策略分别优化 | 如需双向分页需要调整索引 |
| 单进程双协议 | 简化了部署运维，但 HTTP 大文件上传 + gRPC 大消息可能相互影响 goroutine 池 | 需要 per-handler `context` 超时（ROADMAP P0-1 已识别） |
| 仅内存缓存 | SSO token 60s TTL + user 300s TTL，合理。但重启后冷启动时无预热（SSO warmup probe 已实现） | 重启后第一个请求穿透到 SSO |
| 评论仅两层嵌套 | 业务上合理，但 `ValidateNewComment` 在 service 和 domain 中重复校验——domain 已有，service 仍调用 | 轻微职责重叠 |
| outbox 轮询驱动 | `LISTEN/NOTIFY` 更实时，但轮询 5s 间隔对于 MVP 足够 | 需要优雅关闭时的 drain（ROADMAP P0-4） |

### 1.3 架构债务与技术债

**P0 级别（必须修复）**

1. **缺少 `context` 超时传递**（ROADMAP P0-1）：`server.ReadTimeout`/`WriteTimeout` 在 server 级别有设置，但 handler 内部没有基于 `context` 的超时。大文件上传或慢查询可能耗尽整个超时窗口。需要在中间件中将剩余超时注入 `context`，或使用 `http.TimeoutHandler`。

2. **Outbox relay 优雅关闭**（ROADMAP P0-4）：当前 `stopRelay` 只是停止 ticker，不做最后一次 drain。快速重启可能丢失事件。

3. **Partial index 缺失**：`outbox_events` 的 `(published_at IS NULL, created_at ASC)` 部分索引未统一创建（migration 中可能已有，但需要验证）。全表扫描轮询的成本会随未发布事件堆积而增加。

**P1 级别（应规划）**

4. **repository 层事务抽象缺失**：`repository` 包没有暴露事务接口（如 `Begin(ctx) (Tx, error)`）。`service/comment.go` 中的 `Create` + `IncReplyCount` 在事务外执行——虽已通过 outbox 间接修复，但直接的事务包装更可靠。

5. **权限模型的 scope 缺陷**：当前 `HasPermission` 通过 `roles.permissions TEXT[]` 数组检查，无资源范围概念。一个拥有 `content.moderate` 权限的管理员，对所有评论都有管理权限——无法限定到某个组织。

6. **Service 接口膨胀**：`service.go` 中的 `FileService` 接口有 14 个方法。随着功能增加，单个接口会持续增长。应考虑按职责拆分（如 `FileCRUDService`、`FileShareService`、`FileUploadService`）。

7. **Handler 层 gRPC 错误码映射无统一层**（ROADMAP P1-2）：每个 gRPC handler 独立调用 `pkg/errors.GRPCError`，错误码映射散落各处。

---

## 2. 扩展方向

### 方向 A：实时推送（WebSocket/SSE）—— P0

**为什么需要**：这是最大的用户体验提升点。当前通知和消息依赖轮询（`GET /v1/messages?unread=true`），延迟高、请求量大。`internal/hub/` 包已经实现了 SSE 连接注册表和每用户推送机制（`Hub.Publish`），但未被 handler 层使用。

**核心挑战**：
- 连接管理：每个 uid 多个设备（`uidSubs` 已支持），连接断开检测
- 背压：`Hub` 使用带缓冲（16）channel 的无阻塞发送，慢消费者静默丢事件——需要权衡丢 vs 阻塞
- 重连恢复：SSE 的 `Last-Event-ID` 可以在重连时拉取丢失事件
- 扩展性：单进程 Hub 无法跨实例广播——如需多实例部署，需引入 Redis Pub/Sub 或 NATS

**预期架构变更**：
```
新增：
  internal/handler/http/sse.go  — SSE 端点（/v1/events）
  internal/handler/grpc/events.go — gRPC 双向流等同功能

修改：
  internal/notification/dispatcher.go  — 事件写入 outbox 时同步推送到 Hub
  internal/app/new.go  — 在 composition root 中注入 Hub
```

**对现有系统的影响**：低。Hub 已存在，只需添加 SSE handler 并在 dispatcher 中增加推送调用。不修改现有 API 契约。

**建议的技术选项**：

| 方案 | 优点 | 缺点 |
|------|------|------|
| **方案 A：纯 SSE** | 实现简单，aero-id 已有 `Hub` 基础设施；HTTP 兼容性好 | 单进程，无法跨实例扩展 |
| **方案 B：SSE + Redis Pub/Sub** | 支持多实例水平扩展；Redis 可能是组织中已存在的组件 | 增加 Redis 依赖和运维复杂度 |
| **方案 C：gRPC Server-Sent Events** | 与 gRPC 协议栈一致；双向流支持推送确认 | gRPC 客户端要求高，对外 API 仍需 HTTP |

**建议**：先采用方案 A（纯 SSE），通过 `Hub.Publish` 在出站 dispatcher 上同步推送。在需要水平扩展时升级为方案 B，但保留 SSE 作为对外暴露的协议。

---

### 方向 B：Trash Bin（回收站）—— P1

**为什么需要**：当前软删除（`gorm.DeletedAt`）是不可逆的。用户误删文件或消息后无法恢复。`storage.GC` 已经会清理超过保留窗口的软删除行。需要给用户一个 "回收站" 视图来恢复或确认永久删除。

**核心挑战**：
- 保留策略：回收站应有一个 TTL（如 30 天），之后自动永久删除
- 存储成本：文件 blob 直到回收站 TTL 过期才会被 GC 清理。需要让用户和操作者清楚知晓
- 硬删除与软删除的交互：`Delete` 当前设置 `deleted_at`。恢复操作清除 `deleted_at`。GC 跳过未到期的软删除行

**预期架构变更**：
```
修改：
  internal/service/file.go  — Delete → 软删除，新增 Restore(id) 方法
  internal/service/message.go  — Delete → 软删除，新增 Restore(id) 方法
  internal/service/service.go  — FileService 和 MessageService 接口增加 Restore

  internal/storage/gc/sweeper.go  — 区分 "等待回收站到期" 和 "已到期可清理"

新增：
  internal/handler/http/trash.go  — 回收站视图：ListTrash, Restore, PermanentDelete
  migrations/  — 软删除建立索引以支持回收站分页查询
```

**对现有系统的影响**：中。`Delete` 语义不变（仍设置 `deleted_at`），但 GC 需要区分 "刚刚软删除" 和 "回收站已过期"。需要迁移：`storage.gc.retention` 应缩短为回收站 TTL，用户在执行永久删除前有宽限期。

---

### 方向 C：AI Moderation Pipeline—— P1

**为什么需要**：`comments` 已有 `status`（1 正常 2 已删 3 屏蔽）和 `comment_reports` 表（用户举报）。但审核是手动的——管理员查看举报队列，手动隐藏/取消隐藏。通过 outbox 支持自动审核流水线，可以让内容在发布时就自动标记。

**核心挑战**：
- 外部 AI 审核服务的可用性和延迟：审核调用不应阻塞用户请求
- 误判与申诉流程：自动标记的内容需要允许用户申诉
- 敏感数据：内容（消息、评论）可能会被发送到外部 LLM 进行处理，需要 consent 管理（领域设计中已预埋）

**预期架构变更**：
```
新增：
  internal/ai/  — 审核器接口 + 外部 LLM provider 适配器
  internal/ai/moderator.go  — Moderator 接口（Moderate(content) → Result）

修改：
  internal/outbox/chain.go  — 增加审核处理环节
  internal/comment/service.go  — 自动隐藏被标记的内容
  internal/domain/  — 增加 ContentStatus 状态枚举
  configs/config.yaml  — 增加 ai.* 配置节
```

**对现有系统的影响**：低到中。利用现有的 outbox relay 链（`internal/outbox/chain.go`）添加审核处理环节。出站事件 `comment.created` 通过审核链后，如果内容被标记，则发出一条 `comment.hidden` 事件。

**决策**：审核结果同步写回 vs 异步写回？

| 方案 | 延迟影响 | 复杂度 | 用户体验 |
|------|---------|--------|---------|
| **同步**（在 Create 响应中嵌入审核结果） | 增加 Create 延迟 | 低 | 立即知道是否被屏蔽 |
| **异步**（通过 outbox 执行审核，事件驱动状态变更） | 无延迟影响 | 中 | 发布后稍后才被屏蔽 |
| **混合**（快速预检 + 异步深度审核） | 轻微延迟 | 高 | 最佳平衡 |

**建议**：采用异步方案（通过 outbox），审核结果写回后通过通知通道推送给用户。这样 Create 延迟不受影响，且用户在内容被标记时会收到通知。

---

### 方向 D：Multi-Org 多组织 —— P2

**为什么需要**：当前模型支持 "用户拥有数据"，但没有组织概念。对于 B2B 场景，需要组织拥有数据，组织内的用户共享资源。

**核心挑战**：
1. **scope 迁移**：现有 `roles` 表没有 `scope` 列。添加 scope 列（org_id/global）不会破坏现有角色，但需要迁移所有现有角色为 `scope='global'`。`RequirePermission` 中间件需要升级以支持 scope 感知的检查。
2. **数据隔离**：文件、消息、评论需要 `org_id` 列来隔离数据。现有索引需要重建。
3. **用户与组织关系**：需要 `org_members(org_id, uid, role)` 表。SSO 调用需要扩展以支持组织上下文（SSO 可能需要提供用户所属组织）。

**预期架构变更**：
```
迁移（多个步骤）：
  Phase 1: roles 添加 scope 列 + org_id PK 扩展
  Phase 2: 所有数据表添加 org_id 列（可为空——向下兼容）
  Phase 3: 添加 org_members 表

代码变更：
  internal/domain/  — 增加 Org / OrgMember 实体
  internal/repository/  — 增加 OrgRepository
  internal/service/  — 增加 OrgService

  internal/middleware/permission.go  — RequirePermission 支持 scope 感知
  internal/service/service.go  — 所有 service 增加 WithOrg context 注入
```

**对现有系统的影响**：高。这是系统范围的变化。角色模型、授权检查、数据隔离都需要修改。

**建议**：在 Phase 0（当前）中不执行此方向，直到有明确的 B2B 客户需求。同时，以低成本的方式保留扩展能力：确认 `roles` 表的 `permissions TEXT[]` 列在 Postgres 中可以存储额外的结构信息。

---

### 方向 E：Observability Hardening—— P0

**为什么需要**：ROADMAP 已识别此需求且已部分实现（metrics、tracing、outbox 指标）。但仍存在差距：
- **HTTP handler 内无 context 超时**（P0-1）
- **中间件单元测试缺失**（P0-2）：`auth.go`、`ratelimit.go`、`metrics.go` 等无独立测试
- **慢查询无追踪**：GORM 日志级别为 `warn`，但慢查询（>200ms）未通过 trace 暴露

**核心挑战**：无技术挑战，是工程纪律和执行优先级的问题。

**预期架构变更**：低。现有基础设施已到位，需要增加测试和 context 超时传递。

---

## 3. 接口设计建议

### 3.1 应引入的关键抽象

**3.1.1 `TxProvider`——事务抽象**

当前 repository 层没有暴露事务。service 无法在同一事务中执行多个写操作。这导致：

- `comment.Create` + `IncReplyCount` 在事务外执行
- 无法将 outbox 事件与业务操作原子提交（虽可通过在 service 中传递 `*gorm.DB` 解决，但泄漏了 ORM 细节）

**建议的抽象**：

```go
// internal/repository/tx.go
type Tx interface {
    // Commit applies the transaction.
    Commit() error
    // Rollback discards the transaction.
    Rollback() error
    // DB returns the underlying *gorm.DB for use within the transaction.
    DB() *gorm.DB
}

type TxProvider interface {
    Begin(ctx context.Context) (Tx, error)
}
```

**权衡**：这是对 GORM 的明显泄漏，但代码库已经绑定 GORM。所有事务性操作都由同一数据库处理。引入此抽象可以让 service 执行跨实体的原子写入，而无需耦合到 `*gorm.DB`。

**3.1.2 `CursorPager`——统一分页**

当前每个 `List*` 方法独立处理游标分页，存在重复的逻辑（n+1 检测、游标编码）。统一的分页抽象减少样板代码和 bug。

```go
type CursorPage struct {
    Cursor string
    Limit  int
}

type CursorResult[T any] struct {
    Items      []T
    NextCursor string
}
```

**3.1.3 `OutboxDispatcher`——为 outbox 事件提供类型安全接口**

当前 `outbox.NewEvent` 接受 `any` 有效载荷并进行 JSON 封送。可以在 outbox 包中引入类型化的事件和调度器：

```go
type EventDefinition[T any] struct {
    AggregateType string
    EventType     string
}

func (d EventDefinition[T]) NewEvent(id string, payload T) *Event { ... }
```

**权衡**：Go 泛型增加了编译时类型安全性，但可能会使阅读代码变得更复杂。适用于 `comment.created` 这类已确定模式的事件。

### 3.2 向后兼容性指南

1. **新增 API 端点**优于修改现有端点。例如，`/v1/trash` 不应是 `/v1/files?trash=true`，而应是 `/v1/trash/files`。
2. **HTTP/gRPC 双协议保证**：任何新的 HTTP 路由必须具有对等的 gRPC RPC。gRPC proto 定义的演进应按 `package v1` → `v2` 模式进行。
3. **配置的演进**：添加新配置字段时，必须在启动验证中提供合理的默认值或明确标记为可选。
4. **数据库迁移**：始终提供可逆的迁移上/下对，且上迁移绝不能破坏软删除、游标分页或现有索引。

---

## 4. 技术选型

### 4.1 评估框架

对于每一项新依赖，按以下标准进行评估：

| 标准 | 含义 |
|------|------|
| **必要性** | 是否可以通过合理的工作量在现有代码中实现，或依赖确实带来了独特的能力？ |
| **与 Go 1.26 的兼容性** | 依赖是否在其 module 声明中声明了兼容的 Go 版本？ |
| **依赖传递** | 它带来了多少新的传递依赖？是否通过 `go mod why` 评估了？ |
| **测试兼容性** | 是否与 testcontainers-go（当前测试策略的核心）兼容？ |
| **运维开销** | 是否引入了新的运行时进程（Redis、NATS）或配置面？ |

### 4.2 具体评估

| 候选 | 场景 | 结论 | 原因 |
|------|------|------|------|
| **Redis** | SSE 跨实例广播，速率限制共享状态 | **P2 前不引入** | 当前单进程部署足以满足需求。引入 Redis 会增加配置面、连接管理和运维负担 |
| **NATS / RabbitMQ** | 增强版 outbox，取代轮询 | **观察** | 当前基于 PG 的 outbox 对 J 队已够用。如果在 5 秒轮询间隔下出站延迟成为问题，再考虑 |
| **`pgvector`** | AI 嵌入存储，语义搜索 | **P2** | 领域设计中已预埋为 Phase 2 需求。在需要语义检索前不引入 |
| **`stripe/stripe-go`** | 付费功能 | **不引入** | 代码库范围限于数据存储，不包含支付 |
| **`prometheus/client_golang`** | 自定义指标收集 | **已引入**（通过 `internal/pkg/metrics`） | 已满足当前指标需求 |
| **`gorilla/websocket`** | WebSocket 推送 | **接受**（或标准库 `nhooyr.io/websocket`） | 如果需要双向实时通信。但 SSE（标准库，无需额外依赖）是更简单的首选 |
| **`microsoft/gocosi` / `hashicorp/vault`** | 密钥管理 | **不引入** | 密钥通过环境变量传递，符合当前安全模型 |

### 4.3 自建 vs 采购决策

对于 outbox 模式（当前与 `internal/outbox/` 自建）：

| 维度 | 自建（当前方案） | 采购/集成（如 Temporal/Cadence） |
|------|----------------|----------------------------------|
| 延迟 | 5s 轮询 | 毫秒级 |
| 复杂度 | 低（~500 行 Go） | 高（分布式工作流引擎） |
| 运维 | 仅依赖 PG | 需要工作流服务集群 |
| 重试/死信 | 支持 | 原生支持 |
| 可观测性 | 基本指标 | 原生 Web UI + 追踪 |

**结论**：对于当前规模和未来 12 个月的预期增长，自建方案是正确的选择。仅当需要保证排序、扇出到多个消费者或分钟级 SLA 时才考虑 Temporal。

---

## 5. 实施路线图

### 5.1 优先级矩阵

| 方向 | 业务价值 | 技术价值 | 工作量 | 优先级 | 阶段 |
|------|---------|---------|--------|--------|------|
| SSE 实时推送（方向 A） | ★★★★★ | ★★★ | 中 | **P0** | Phase 1a |
| Trash Bin（方向 B） | ★★★★ | ★★ | 低 | **P1** | Phase 1b |
| AI Moderation（方向 C） | ★★★ | ★★★★ | 中 | **P1** | Phase 2 |
| Multi-Org（方向 D） | ★★★★ | ★★ | 高 | **P2** | Phase 3 |
| Observability 强化（方向 E） | ★★★ | ★★★★★ | 低 | **P0** | Phase 0（立即） |
| 事务抽象（接口改进 3.1.1） | ★★ | ★★★★ | 低 | **P1** | Phase 1a |
| 统一分页（接口改进 3.1.2） | ★ | ★★★ | 低 | **P2** | 持续优化 |

### 5.2 阶段划分

#### Phase 0：生产就绪补全（当前 sprint）

| # | 工作项 | 工作量 | 验收标准 |
|---|--------|--------|---------|
| P0-1 | Handler context 超时 | 低 | per-handler `ctx` deadline 继承自 `http.Request.Context()` |
| P0-2 | 中间件测试覆盖 | 中 | `auth.go`、`ratelimit.go`、`metrics.go`、`recovery.go`、`cors.go`、`request_id.go` 有单元测试 |
| P0-3 | gRPC handler 单元测试 | 中 | `comment.go`、`message.go`、`file.go`、`notification.go` gRPC handler 验证序列化和错误码 |
| P0-4 | Outbox relay 优雅关闭 | 低 | stop func 执行最后一次 drain |
| P0-5 | 确认并添加 outbox 部分索引 | 低 | `(published_at IS NULL, created_at ASC)` 索引 |

**里程碑**：`make test && make test-integration && make lint` 全部绿色。

#### Phase 1：实时性与数据安全

| # | 工作项 | 工作量 | 依赖 |
|---|--------|--------|------|
| 1a-1 | SSE 端点 `/v1/events` | 中 | 无 |
| 1a-2 | dispatcher 中推送通知到 Hub | 低 | 1a-1 |
| 1a-3 | 事务抽象（`TxProvider`） | 低 | 无 |
| 1a-4 | Trash Bin：软删除 view + Restore API | 中 | 1a-3（恢复可能是事务性操作） |
| 1a-5 | storage.GC 在清理前等待回收站 TTL | 低 | 1a-4 |

**里程碑**：用户能在 5 秒内收到通知推送；误删内容可在 30 天内恢复。

#### Phase 2：智能管道

| # | 工作项 | 工作量 | 依赖 |
|---|--------|--------|------|
| 2-1 | AI 审核接口定义（`internal/ai`） | 低 | 无 |
| 2-2 | 审核链 + outbox 集成 | 中 | 2-1 |
| 2-3 | 审核结果写回 + 通知 | 中 | 2-2 |
| 2-4 | gRPC 统一错误码映射 | 低 | 无 |

**里程碑**：新评论在创建后 < 10s 内被自动审核；被标记的内容用户会收到通知。

#### Phase 3：多组织（按需触发）

| # | 工作项 | 工作量 | 依赖 |
|---|--------|--------|------|
| 3-1 | roles scope 迁移文档 + 迁移 | 中 | 无（但需与 SSO 团队协调） |
| 3-2 | org_members 表 + repository | 中 | 3-1 |
| 3-3 | 数据表 org_id 列 + 索引重建 | 高 | 3-2 |
| 3-4 | scope 感知的权限中间件 | 中 | 3-1 |
| 3-5 | org 上下文通过 gRPC 元数据传递 | 中 | 3-3 |

**里程碑**：组织管理员能管理组织成员、数据隔离在组织边界内。

### 5.3 风险点与缓解措施

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| SSE 连接数导致内存 OOM | 低 | 中 | 限制每用户最大连接数（例如 10）；Hub 的 channel 大小（16）限制积压；对 SSE 端点应用速率限制 |
| AI 审核延迟阻塞出站 channel | 中 | 中 | 审核 handler 应使用 `context.WithTimeout`；响应慢时静默失败（审核不阻塞主流） |
| Multi-Org 迁移导致角色系统回归 | 中 | 高 | 在单独的 CI job 中运行 Pre-migration 和 post-migration 的 `HasPermission` 测试；使用蓝色/绿色部署策略 |
| outbox relay 死锁 | 低 | 高 | 实现每次 drain 的超时；dead letter 指标告警；如果 relay 在特定事件上卡住，支持手动跳过 |
| SSO 中断期间冷重启 | 低 | 高 | SSO warmup probe 已实现（`internal/app/app.go` 中启动后探测）。但 SSO down 时新实例无法验证任何 token——确保 `serve_stale_ttl` 在 SSO 恢复前保护已缓存的身份 |

### 5.4 跨切面关注点

**每个阶段都应包含的**：
1. **双协议一致性**：每个新的 HTTP 路由必须具有对等的 gRPC RPC，且应该在已有的 `parity_test.go` 测试链中得到验证。
2. **可观测性扩展**：新增端点需要 RED 指标（请求率、错误率、持续时间）。
3. **审计日志**：管理操作（GrantRole、BanUser、PermanentDelete）应通过 `AuditLogger` 进行审计。
4. **软删除安全性**：所有新的写操作必须验证软删除行不会被操作重新激活（`gorm.DeletedAt` 检查）。

### 5.5 总结：关键主题

这份代码库最令人印象深刻的地方不是具体的功能，而是**架构的内聚性**和**设计决策的前瞻一致性**。从 `domain` 包中的实体定义，到 `service` 层中的 ACL 封装，再到 `handler` 层的双协议对等，每个抽象都服务于一个明确的目的，没有过度抽象。

最大的风险不是技术债，而是**在功能压力下损害分层纪律**。如果出现一种情况，需要在 handler 中添加直接访问 repo 的逻辑来快推一个功能——不要这样做。相反，花额外的时间通过 service 层传递。代码库的结构使得这样做很容易；保持它。
