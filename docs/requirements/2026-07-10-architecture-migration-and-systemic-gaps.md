# aero-id — 架构迁移状态与系统性缺口分析

> **日期**: 2026-07-10
> **视角**: 资深架构师
> **方法**: 全局代码扫描（120+ 源文件，22 个迁移，全部 `internal/` 包）
> **定位**: 本文件与 `docs/requirements/` 下 7 份现有方向文档互补。那些文档覆盖了 **20+ 产品方向**（群聊、活动流、Webhook、多副本、WebSocket、存储配额、文件版本、定时动作、自适应限流、数据平台、移动推送、SDK、OAuth、导入迁移、多区域部署、审核工作台、文件加工管线、消息线程、多表情、会话统一、内容安全、审计告警）。本文件聚焦于**前三份文档完全没有触及的架构层系统性缺口**。

---

## 核心发现：未完成的领域驱动重构

### 现状：两套并行的完整实现

扫描发现代码库中存在 **两套独立的、可运行的 handler→service→repository 堆栈**，但只有一套被 `app.go` 实际使用：

| 层次 | 旧栈（生产在用） | 新栈（未接入生产） |
|------|----------------|-------------------|
| **HTTP handler** | `internal/handler/http/{file,message,comment,...}.go` | `internal/{comment,file,message,notification,block,search,audit}/handler_http.go` |
| **gRPC handler** | `internal/handler/grpc/{file,message,comment,...}.go` | `internal/{...}/handler_grpc.go` |
| **Service** | `internal/service/{file,message,comment,...}.go` (5,268 行) | `internal/{...}/service.go` |
| **Repository** | `internal/repository/{file,message,comment,...}.go` (3,662 行) | `internal/{...}/repository.go` |
| **Domain types** | `internal/domain/{file,message,comment,...}.go` | 复用同一 `internal/domain` |
| **代码量** | ~15,567 行 | ~11,798 行 |
| **状态** | `app.go` 实际引用 | **未被任何生产代码引用**（仅测试 + search 使用） |

### 严重性评估

| 风险 | 等级 | 说明 |
|------|------|------|
| **代码漂移** | 🔴 高 | 两套实现独立演化。旧栈持续被维护和新功能修改；新栈的代码可能已经过时。修复一个 bug 时需要修改两处，或 bug 在旧栈修了但新栈仍然存在。 |
| **误导性架构文档** | 🟡 中 | `ARCHITECTURE.md` 和 `AGENTS.md` 描述的架构与实际运行的不一致。新来的开发者看到 `internal/{comment,file,message}` 的整洁结构会误以为这是标准架构。 |
| **测试覆盖错觉** | 🟡 中 | 新栈的测试（`internal/{domain}/service_test.go` 等）全部通过，但这些测试覆盖的代码在生产中不被执行。旧栈的测试覆盖率可能比 CI 报告的低。 |
| **无主代码** | 🔴 高 | 新栈的 11,798 行代码没有任何"主人"——没有团队负责保持它同步。下次大规模重构时，没有人知道这些代码是否可以删除。 |

### 处理建议

1. **立即行动：确定新栈的战略意图。** 回答：新栈是"即将切换的目标"还是"一次性的代码实验"？前者需要制定迁移计划；后者需要归档并标记为已废弃。
2. **如果选择迁移：** 
   - 逐步将 `app.go` 的依赖从旧栈切换到新栈，一次一个域（先切换 file，验证通过后再切换 comment，etc.）
   - 每个域切换后运行 `make test && make test-integration`
   - 切换完成后删除旧栈代码
3. **如果废弃新栈：**
   - 在新栈的每个包根目录加 `// Deprecated: unused; see internal/service` 注释
   - 在 `AGENTS.md` 和 `ARCHITECTURE.md` 中记录此决策
   - 将测试转移到旧栈（如需要）

---

## 方向一：跨域关注点治理（Cross-Domain Concern Governance）

### 问题

当前架构中，跨域关注点（审计、权限、通知、阻断检查）的实现方式不一致：

| 关注点 | 实现方式 | 问题 |
|--------|---------|------|
| **审计日志** | `domain.AuditLogger` 接口，service 层调用 | 只在部分敏感操作调用（block, share, report resolve）；**文件上传、消息发送、评论创建无审计** |
| **权限检查** | 散布在各 service 方法中（`mustActor` + `canReadFile`/`canReadMessage` 等） | 无统一权限引擎。`domain.CanReadFile` 逻辑极简（仅 owner/public），**不检查 file_shares** |
| **阻塞检查** | `block.BlockChecker` 接口 | 在 message/comment/notification 中重复集成，但**search 未集成** |
| **通知分发** | `outbox.Handler` → `NotificationDispatcher` | 工作良好，但事件类型注册在 dispatcher 中硬编码，新增事件类型需要修改 dispatcher |
| **限流** | `RateLimitMiddleware`/`Interceptor` | 仅按 uid + 全局限流，**不支持按 endpoint 类型区分权重** |

### 建议：跨域能力层（Cross-Cutting Capability Layer）

不要在每个 service 中手工组合这些关注点，而是引入一个**可组合的中间件链**在 service 边界：

```go
// 伪代码：跨域能力执行链
type OperationContext struct {
    Actor    string
    Resource Resource      // {Type: "file", ID: "xxx", Owner: "uid"}
    Action   string        // "file.create", "message.send", "comment.edit"
}

type Capability interface {
    Check(ctx context.Context, op *OperationContext) error
}

// 按顺序执行的能力链
capabilities := []Capability{
    &RateLimit{WeightByAction: map[string]int{"file.create": 10, "message.send": 1}},
    &Authorization{Policy: myPolicyEngine},    // 统一的 ACL 检查
    &Blocking{BlockChecker: blockRepo},        // 自动检查阻断关系
    &AuditLog{AuditLogger: auditRepo},         // 自动记录所有操作
}

// 在 service 边界调用
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*File, error) {
    actor := domain.MustActor(ctx)
    if err := s.capabilities.Check(ctx, &OperationContext{
        Actor: actor, Resource: Resource{Type: "file"}, Action: "file.create",
    }); err != nil {
        return nil, err
    }
    // ... 纯业务逻辑
}
```

### 收益

- 新增审计事件类型不再需要修改业务代码（仅注册新 Action）
- 权限策略可以集中修改（如"管理员可读任何文件"只需改一处）
- 新增跨域关注点（如"操作频率异常检测"）不需要侵入每个 service
- 新域（如未来的群聊、频道）自动继承所有跨域能力

### 估算工作量

- 定义 `OperationContext` + `Capability` 接口：**低**
- 实现各 Capability（从现有代码中提取）：**中**（需要解耦现有 service 中的内联检查）
- 接入 app.go 的 service 构造函数：**低**
- **总计：2-3 周**

---

## 方向二：领域事件契约化与消费者注册表（Event Contract Registry）

### 问题

`outbox` 系统是 aero-id 的事件中枢，但目前缺乏两大关键能力：

**1. 事件类型无注册表。** 事件类型是字符串常量，散布在 `domain/notification.go`、`domain/audit.go`、`outbox/chain.go`、`notification/dispatcher.go` 中。要回答"系统中有哪些事件类型？哪些消费者订阅了它们？"需要 grep 整个代码库。当前事件类型：

```
message.sent, message.read_receipt, comment.reply, comment.report.created,
comment.report.resolved, file.share.grant, file.share.revoke,
user.block, user.unblock, message.delete
```

每个事件类型的 payload 结构是隐式的（`json.RawMessage`），消费者通过 `json.Unmarshal` 解析。**没有 payload schema 定义，没有跨消费者的兼容性检查。**

**2. 消费者链不可观测。** `outbox.Chain` 按顺序执行消费者。如果中间某个消费者 panic 或 hang，后续消费者全部被阻塞。当前无超时保护，无每个消费者的独立状态。

### 建议

#### A. 事件契约注册表

```go
// 在 internal/events 包中统一定义
package events

// Registry 包含所有已知的事件类型及其元数据
var Registry = EventRegistry{
    "message.sent": {
        Schema:         reflect.TypeOf(MessageSentPayload{}),
        Description:    "A user sends a message to another user",
        Consumers:      []string{"notification", "activity-feed", "webhook"},
        SensitiveFields: []string{"content"}, // 脱敏标记
    },
    "comment.reply": {
        Schema:         reflect.TypeOf(CommentReplyPayload{}),
        Description:    "A user replies to a comment",
        Consumers:      []string{"notification", "activity-feed"},
    },
    // ...
}

type MessageSentPayload struct {
    MessageID string `json:"message_id"`
    FromUID   string `json:"from_uid"`
    ToUID     string `json:"to_uid"`
    Kind      string `json:"kind"`
    Content   string `json:"content"` // sensitive
}
```

#### B. 消费者隔离与契约测试

```go
// 每个消费者有独立的 goroutine + channel + 超时
type ConsumerInstance struct {
    Name    string
    Handle  func(ctx context.Context, e *Event) error
    Timeout time.Duration
    // 每个消费者的独立指标
    Metrics struct {
        Success atomic.Int64
        Failed  atomic.Int64
        Latency prometheus.Histogram
    }
}

// Contract Test: 验证每个消费者能正确解析事件 payload
func TestConsumers_MessageSent(t *testing.T) {
    payload := events.MessageSentPayload{/* ... */}
    data, _ := json.Marshal(payload)
    evt := &outbox.Event{EventType: "message.sent", Payload: data}
    for _, consumer := range consumers {
        t.Run(consumer.Name, func(t *testing.T) {
            err := consumer.Handle(context.Background(), evt)
            assert.NoError(t, err)
        })
    }
}
```

### 收益

- 新增事件类型时，编译器检查 payload schema + 自动触发所有消费者测试
- 消费者监控：每个消费者的成功率、延迟可视化
- 消费者故障隔离：一个消费者超时不阻塞其他消费者
- 为事件版本化（向后兼容的事件 schema 演进）奠定基础

### 估算工作量

- 创建 `internal/events` 包 + 注册表：**低**
- 改造现有消费者（dispatcher, webhook）为独立 goroutine + 超时：**中**
- 契约测试框架：**低**
- **总计：2-3 周**

---

## 方向三：声明式资源权限模型（Declarative Resource Authorization）

### 问题

当前的权限检查是**过程式且分散的**——每个 service 方法中自行判断谁能访问什么：

| 资源 | 访问规则 | 代码位置 |
|------|---------|---------|
| **File** | owner 全权 + public 可读 + 分享授权可读 | `service/file.go` (Get, Delete, Update, Share 各写一遍) |
| **Message** | from_uid / to_uid 有读写权限 | `service/message.go` (Get, Inbox 各写一遍) |
| **Comment** | owner 可编辑/隐藏/删除；owner 可见自己的举报队列 | `service/comment.go` |
| **Admin** | role-based (`user_roles` → `RequirePermission` middleware) | `middleware/permission.go` |

问题在于：
- **遗漏风险高**：`SearchFiles` 不检查 file_shares（已发现）；`ListSharedWithMe` 是独立的端点，但通用 `Get` 不检查分享权限
- **三套模型**：基于 owner 的（file/message）、基于角色的（admin）、基于关系的（block）
- **无法审计**：没有集中查询"用户 X 对资源 Y 有什么权限"的统一入口

### 建议：资源访问矩阵（Resource Access Matrix）

定义一个声明式的资源权限 DSL，在一个地方描述所有资源类型和操作的授权规则：

```go
// internal/auth/policy.go

// AccessRule 定义谁可以对某类资源执行某操作
type AccessRule struct {
    Resource  string          // "file", "message", "comment"
    Action    string          // "read", "write", "delete", "share", "moderate"
    // 判断函数链：任一满足即可
    AllowIf   []func(ctx, actor, resource) bool
}

// 全局规则表
var Policy = []AccessRule{
    // === File ===
    {Resource: "file", Action: "read", AllowIf: []func(ctx, actor, resource) bool{
        isOwner,           // f.OwnerUID == actor
        isPublic,          // f.Visibility == public
        isSharedToActor,   // file_shares.shared_with_uid == actor
        isAdmin,           // actor has admin role
    }},
    {Resource: "file", Action: "delete", AllowIf: []func(ctx, actor, resource) bool{
        isOwner,
        isAdminWithPerm("content.moderate"),
    }},
    {Resource: "file", Action: "share", AllowIf: []func(ctx, actor, resource) bool{
        isOwner,
    }},

    // === Message ===
    {Resource: "message", Action: "read", AllowIf: []func(ctx, actor, resource) bool{
        isParticipant,     // from_uid == actor || to_uid == actor
    }},
    {Resource: "message", Action: "send.to", AllowIf: []func(ctx, actor, resource) bool{
        notBlockedByRecipient,
    }},

    // === Comment ===
    {Resource: "comment", Action: "edit", AllowIf: []func(ctx, actor, resource) bool{
        isOwner,
    }},
    {Resource: "comment", Action: "moderate", AllowIf: []func(ctx, actor, resource) bool{
        hasRole("moderator"),
        isOwner, // 作者可以隐藏自己的评论（当前逻辑）
    }},
}

// Check 方法被 service 层统一调用
func Check(ctx context.Context, actor string, resource Resource, action string) error {
    for _, rule := range Policy {
        if rule.Resource == resource.Type && rule.Action == action {
            for _, allow := range rule.AllowIf {
                if allow(ctx, actor, resource) { return nil }
            }
            return ErrForbidden
        }
    }
    return ErrUnknownResource
}
```

### 收益

- **一处修改，全局生效**：增加"VIP 用户配额翻倍"只需在 `isOwner` 旁加一个新条件
- **可审计**：`GET /v1/admin/auth/check?actor=X&resource=file:abc&action=delete` 返回允许/拒绝 + 匹配的规则
- **可测试**：为每个规则编写独立测试，用表格驱动覆盖所有组合
- **消除遗漏**：不再需要逐个端点检查权限——所有端点都通过统一的 `Check()` 调用

### 估算工作量

- 定义 `internal/auth` 包 + 规则引擎：**中**
- 迁移现有 7 个 service 的内联权限检查：**中**（涉及 ~20 个方法）
- 为遗漏的检查点补全（search + file_shares 权限）：**低**
- **总计：3-4 周**

---

## 方向四：异步收件箱与最终一致性治理（Async Inbox & Eventual Consistency Governance）

### 问题

当前系统有以下最终一致性的"灰色地带"，没有明确治理：

| 场景 | 当前行为 | 问题 |
|------|---------|------|
| **消息发送 → 通知** | 事务性 outbox → relay (5s 间隔) → 通知写入 | 5-15s 延迟：消息已出现在收件箱，但通知未推送。用户轮询收件箱看到消息，但未读计数未更新 |
| **评论回复 → reply_count** | `CreateWithReplyBump` 使用原子 tx 写入 `reply_count` | 工作良好，但软删除不递减 |
| **文件分享 → 搜索结果可见** | 搜索不检查 file_shares | 文件已被分享，但搜索不到——用户困惑 |
| **Block → 已发送的消息** | `BlockChecker` 在发送时检查 | 如果 A 在 B 发消息后 Block B，B 的消息仍然在 A 的收件箱中。已存在的消息不会被追溯隐藏 |
| **消息编辑 → 通知** | 编辑不触发 outbox 事件 | 接收方不知道消息被编辑过 |
| **文件 GC → 存储释放** | GC 周期 1h（默认 disabled） | 用户删除文件后存储不会立即释放 |

### 建议：最终一致性清单（Eventual Consistency Checklist）

为每个业务操作建立一个"一致性清单"，明确什么是一致的、什么是有延迟的、什么是不保证的：

```go
// internal/consistency/checklist.go

// ConsistencyChecklist 是每个操作的一致性契约
type ConsistencyChecklist struct {
    Operation          string             // "message.send"
    Immediate          []Guarantee        // 同步事务保证
    Eventual           []Guarantee        // 最终一致，延迟 ≤ 5s
    BestEffort         []Guarantee        // 尽力而为，无延迟保证
    NotGuaranteed      []string           // 明确不保证
}

var Checklist = map[string]ConsistencyChecklist{
    "message.send": {
        Operation: "message.send",
        Immediate: []Guarantee{
            {What: "消息在发送方和接收方的收件箱中可见", How: "事务性写入 messages 表"},
            {What: "发送方不能 Block 接收方", How: "发送前检查 BlockChecker"},
        },
        Eventual: []Guarantee{
            {What: "接收方收到通知", MaxLag: 5 * time.Second, How: "outbox relay → notification"},
            {What: "收件箱未读计数更新", MaxLag: 5 * time.Second, How: "通知创建 → count query"},
        },
        BestEffort: []Guarantee{
            {What: "SSE 实时推送", How: "NotificationDispatcher 推送，客户端可能断连"},
            {What: "Webhook 递送", How: "outbox webhook handler，重试最多 20 次"},
        },
        NotGuaranteed: []string{
            "接收方已读消息前看到消息",
            "编辑消息后通知接收方（当前不产生事件）",
        },
    },
    // ... 每个操作一张清单
}
```

### 收益

- **可测量的 SLO**：每个最终一致保证都有 `MaxLag`，可以监控 `outbox_lag_seconds < 5` 的达标率
- **消除认知负担**：团队对"删除一个文件后存储多久释放"有统一理解
- **指导测试**：每个 Guarantee 对应一个明确的集成测试用例
- **契约文档**：清单可以直接输出为 API 文档的一部分

### 估算工作量

- 定义 `internal/consistency` 包 + 清单结构：**低**
- 为前 10 个核心操作填写清单：**低中**
- 为每个 Eventual Guarantee 新增监控指标 + 告警规则：**中**
- **总计：1-2 周**

---

## 方向五：请求生命周期的可观测性债务（Observability Debt Across Request Lifecycle）

### 问题

当前可观测性覆盖了一些关键维度但存在结构性缺口：

| 维度 | 现有覆盖 | 缺口 |
|------|---------|------|
| **RED 指标**（Rate/Errors/Duration） | HTTP + gRPC 完整覆盖 | 但仅到 handler 边界。**service 层 + repository 层的延迟不可见** |
| **SSO 健康** | 缓存命中率、调用延迟、错误率 | 覆盖良好 |
| **Outbox 监控** | Published/failed/pending/lag | 覆盖良好 |
| **分布式追踪** | OpenTelemetry（HTTP → SSO） | 但**追踪未跨过 outbox**：事务写入 outbox 后的异步处理（notification dispatch, webhook）不在同一个 trace 中 |
| **业务层日志** | `access_log` 记录每个请求 | 但不记录**操作粒度**——"谁修改了文件 XX 的权限"无法从日志中检索 |
| **数据库查询性能** | 无 | **没有慢查询日志**，没有 `pg_stat_statements` 视图 |

### 建议：全链路可观测性矩阵

#### A. Service + Repository 层指标

```go
// 在 app.go 中用 Prometheus 包装器包裹 repository：
type tracedFileRepo struct {
    inner file.Repository
    latency *prometheus.HistogramVec
}

func (t *tracedFileRepo) Get(ctx context.Context, id domain.ID) (*File, error) {
    start := time.Now()
    f, err := t.inner.Get(ctx, id)
    t.latency.WithLabelValues("file.Get", errCode(err)).Observe(time.Since(start).Seconds())
    return f, err
}
```

每个 repository 方法被自动记录：
- `aeroid_repo_duration_seconds{repo="file",method="Get",error="false"}`
- 结合 p50/p95/p99 定位慢查询

#### B. Outbox 跟踪延续

在 outbox relay 的 `Handle` 调用中创建 child span：

```go
func (r *Relay) handleWithTrace(ctx context.Context, e *Event) error {
    // 创建 child span，继承自写入 outbox 事件的 trace
    ctx, span := tracer.Start(ctx, fmt.Sprintf("outbox.%s", e.EventType))
    defer span.End()
    span.SetAttributes(
        attribute.String("event.id", e.ID.String()),
        attribute.String("event.type", e.EventType),
        attribute.String("aggregate.type", e.AggregateType),
    )
    return r.handler.Handle(ctx, e)
}
```

这样，一个完整请求的 trace 可以跨越：HTTP handler → service → repository → outbox → relay → notification dispatcher → hub → SSE push。

#### C. 结构化业务日志

在 `AuditLogger.Log` 中，除了写入 `audit_events` 表，同时输出一条结构化日志：

```go
func (a *auditService) Log(ctx context.Context, e *domain.AuditEvent) error {
    // 写入 audit_events 表
    if err := a.repo.Create(ctx, e); err != nil {
        return err
    }
    // 同时输出结构化日志（用于日志检索系统）
    a.logger.Info("audit",
        zap.String("actor", e.ActorUID),
        zap.String("action", e.Action),
        zap.String("subject_type", e.SubjectType),
        zap.String("subject_id", e.SubjectID),
        zap.Any("meta", e.Meta),
        zap.Time("created_at", e.CreatedAt),
    )
    return nil
}
```

### 收益

- **30 秒定位慢请求**：从 Prometheus 查到慢 route → 下钻到 repository 延迟 → 定位到具体 SQL
- **跨异步边界追踪**：用户投诉"通知没收到"可以通过 trace ID 追踪到 outbox 是否发布了事件
- **安全审计日志可作为日志检索**：不再需要连数据库查 `audit_events`（当前唯一入口）
- **成本效益**：大部分基础设施（OTel、Prometheus）已就位，只需要补充 instrumentation

### 估算工作量

- Repository 层 tracing wrapper：**中**（~7 个 repo，每个 5-15 个方法）
- Outbox trace 延续：**低**（修改 relay.go 约 10 行）
- 结构化审计日志输出：**低**（修改 audit service 约 5 行）
- 慢查询监控：**低**（配置 `pg_stat_statements` + dashboard）
- **总计：1-2 周**

---

## 方向优先级对比

| # | 方向 | 技术债消减 | 生产价值 | 用户可见 | 实现成本 | 建议排序 |
|---|------|:---------:|:--------:|:--------:|:--------:|:--------:|
| **0** | 解决双架构迁移状态 | 🔴🔴🔴 | 🟡🟡 | 🟡 | 中 | **P0** — 必须先做 |
| **1** | 跨域关注点治理 | 🔴🔴🔴 | 🟢🟢🟢 | 🟢🟢 | 中高 | **P1** — 架构中台化 |
| **2** | 事件契约注册表 | 🔴🔴 | 🟢🟢 | 🟢 | 中 | **P1** — 事件平台化 |
| **3** | 声明式权限模型 | 🔴🔴🔴 | 🟢🟢🟢 | 🟢🟢 | 中高 | **P2** — 安全基础 |
| **4** | 最终一致性治理 | 🔴🔴 | 🟢🟢 | 🟢 | 低中 | **P2** — SLO 基础 |
| **5** | 全链路可观测性 | 🔴🔴 | 🟢🟢🟢 | 🟡🟡 | 中 | **P2** — 运维基础 |

### 执行建议

**Phase 0（立即，1 周）：** 确认新域包的命运。回答"迁移还是废弃"——这个二选一决策是其他所有方向的阻塞条件。先做这个决策，否则后续任何方向都会受到双栈不一致的影响。

**Phase 1（2-3 周）：** 在决策明确后，启动方向 2（事件契约注册表）和方向 4（最终一致性治理）。这两者成本低、风险低、为后续所有事件驱动的扩展提供基础。方向 4 尤其可以和方向 2 共用同一个 `internal/events` 包。

**Phase 2（3-5 周）：** 方向 1（跨域关注点治理）和方向 3（声明式权限模型）。这两个方向深度耦合——权限模型是跨域关注点的核心子集。建议一并设计、分步实施。

**Phase 3（1-2 周）：** 方向 5（全链路可观测性）。此项依赖方向 2 的事件注册表（outbox trace 延续需要事件类型识别）和方向 4 的清单（SLO 监控）。安排在最后。

---

## 附录：扫描发现的具体技术债（未被其他文档覆盖）

| # | 项目 | 位置 | 严重性 | 说明 |
|---|------|------|--------|------|
| D-1 | `internal/service/service.go` 中的 `WithActor`/`ActorFrom` 与 `internal/domain/actor.go` 中的 `WithActor`/`ActorFrom` 重复 | 两个文件 | 中 | 两组实现使用了不同的 context key 类型，如果在一个 ctx 上混用两者会各自读取不到对方设置的 actor。旧栈用 `service.ctxKey(iota)`，新栈用 `domain.ctxKey(iota)`。 | 
| D-2 | `httpapi.Routes` 接口在两个位置定义 | `internal/handler/http/router.go` (旧栈) + `internal/comment/handler_http.go` 引用的同接口 | 低 | 接口定义位置不同但签名相同，目前无冲突。但如果两个栈都注册到同一个 gin 引擎，路由会冲突。 |
| D-3 | `internal/pkg/errors.AppError` 的 `Wrap` 方法被滥用 | 多个文件 | 低 | 当前 `service.Wrap(err)` 的形式会丢失原始错误的信息。用 `fmt.Errorf("...: %w", err)` 包装的错误不会被 `errors.As` 捕获。建议使用 `errors.Wrap` 或保留原始 error chain。 |
| D-4 | 新域包的 `repository.go` 与旧 `internal/repository/` 使用不同的表名约定 | 对比 | 低 | 检查 GORM 表名是否一致——如果双栈都尝试使用不同的表名映射，会出现运行时错误。当前因新栈未启用，此问题未暴露。 |
| D-5 | `internal/handler/http/router.go` 已实现 `RegisterRoutes(r Routes)` 模式，但未用于按域分离路由注册 | `router.go:80-90` | 低 | 当前所有路由在一个 `NewRouter` 函数中集中注册。虽然各 handler 有 `RegisterRoutes` 方法，但 `NewRouter` 仍是手写调用。如果未来要支持"按域启用"（如仅启用文件功能），可以改为自动发现。 |

---

*本文基于全局代码扫描，无代码变更。建议作为技术债清理和架构治理 Sprint 的输入。*
