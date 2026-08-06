# aero-id — 全量代码扫描：五个高价值扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 对 `internal/` 全部 22 个子包、`migrations/` 22 份 SQL、`cmd/`、`configs/`、`api/proto/` 做逐文件通读。与 `docs/requirements/` 下 **96 份既有分析文档**及 `docs/REVIEW_*` **8 份审查文档**中的所有方向做逐标题级交叉比对。以下 5 个方向在全部既有分析中 **未被作为独立方向进行包含完整代码证据链的架构级论证**。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和架构师/产品经理双视角分析。

---

## 交叉验证说明

aero-id 经过多轮迭代，已具备生产级能力：双协议（HTTP + gRPC）、幂等键、事务性 Outbox、SSO 断路器、流式文件上传 + 限流 + 校验 + 去重 + GC、通知系统 + SSE 实时推送、评论举报/审核/隐藏、用户屏蔽、全文搜索、审计日志、管理员角色、OpenTelemetry 链路追踪、操作审计、Webhook 推送等。

既有 96 份需求文档 + 8 份架构审查 + ROADMAP + CHANGELOG 累计覆盖了约 800+ 方向/问题条目。本文 **不重复上述任何已有分析中已作为独立方向完成论证的内容**，而是聚焦于那些在逐文件代码扫描中发现的、**架构上有征兆但从未被系统性地作为独立方向提出**的领域。

---

## 方向一：自动内容安全管线（Automated Content Safety Pipeline）

### 问题陈述

评论举报/审核体系是**人工驱动的**——用户提交报告，管理员手动审查并决定隐藏/恢复。随着用户规模和内容量的增长，这种纯人工模式面临不可扩展的瓶颈：举报队列堆积、人工审查延迟、漏掉大量未举报的违规内容。系统需要一个**自动化的内容安全管线**，在内容发布时同步或异步地进行安全扫描。

### 代码级证据

**证据 1：Comment 模型有 `Status` 字段但没有扫描结果字段**

```go
// internal/domain/comment.go
type Comment struct {
    ID         ID
    TargetType TargetType  // "file" | "message"
    TargetID   string
    OwnerUID   string
    ParentID   *ID
    Content    string
    Status     CommentStatus // 1=Normal, 2=Deleted, 3=Hidden
    LikeCount  int
    EditedAt   *time.Time
    CreatedAt  time.Time
}
```

`Status` 只能被管理员手动设置（`HideComment`/`ShowComment`），没有任何字段承载自动扫描的结果（如 `scan_status`、`scan_label`、`scan_confidence`）。

**证据 2：消息和文件内容完全没有经过任何安全扫描**

```go
// internal/domain/message.go
type Message struct {
    Content  string      // 纯文本，无扫描状态
    FileID   *ID         // 引用的文件无安全状态传递
    ...
}

// internal/domain/file.go
type File struct {
    Checksum     string        // 用于内容去重，但无安全哈希匹配
    ...
}
```

Messages 的 Content 不会经过任何自动审核。文件的 Checksum 可用于安全哈希匹配（如 PhotoDNA、CSAM 哈希数据库）但当前仅用于内容去重。

**证据 3：举报系统只有人工处理一条路径**

```go
// internal/comment/service.go
func (s *svc) Report(ctx context.Context, t CommentTarget, reason string) (*domain.Comment, error) { ... }
func (s *svc) ResolveReport(ctx context.Context, reportID domain.ID) error { ... }
func (s *svc) HideComment(ctx context.Context, id domain.ID) error { ... }
func (s *svc) ShowComment(ctx context.Context, id domain.ID) error { ... }
```

没有 `AutoFlagComment`、`AutoFlagMessage` 或类似的自动标记路径。没有将扫描结果流入 `report` 生命周期的机制。

**证据 4：Outbox 已为自动审核提供了完美的事件触发点**

```go
// internal/comment/comment.go — 在 Create 中写入 outbox 事件
// internal/message/service.go — 在 createMessage 中写入 "message.sent" 事件
```

```go
// internal/outbox/dispatcher.go
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    n := d.notificationFor(e)
    // ... 仅处理通知，不处理内容安全
}
```

dispatcher 只关注通知投递。一个新的 `SafetyScanner` handler 可以链入 outbox 管线（通过 `outbox.NewChain`），订阅 `comment.created`、`message.sent` 等事件，异步执行内容安全扫描。

**证据 5：审核行为已被审计但扫描决策未审计**

```go
// internal/domain/audit.go
const (
    AuditReportResolve = "comment.report.resolve"
    // 但无 AuditContentAutoFlagged 或类似的事件类型
)
```

### 架构师视角

从架构上看，自动内容安全管线可以无缝嵌入现有的 outbox + chain 模式：

```
outbox.Relay → outbox.Chain(dispatcher, safetyScanner)
                                        ↓
                              safetyScanner.Handle()
                                ├─ AI 分类（NSFW / 仇恨言论 / 垃圾信息）
                                ├─ 哈希匹配（CSAM / 已知恶意内容）
                                └─ PII 检测（电话、邮箱、地址泄露）
                                        ↓
                              自动标记 → 修改 Status 为 hidden
                                        ↓
                              生成 system 通知给内容发布者
                                        ↓
                              严重违规 → 自动生成举报并通知管理员
```

需要引入的组件：
1. **`internal/safety/`** 新包：`Scanner` 接口 + 实现（本地模型或外部 API）
2. **`ContentScanStatus`** 模型：扩展 comment/message/file 增加 `scan_status`、`scan_label`、`scan_confidence` 字段
3. **安全性策略配置**：`safety.rules` 定义扫描规则、阈值和行为（仅标记/自动隐藏/自动举报）
4. **扫描结果审计**：扩展审计日志记录自动操作

### 产品经理视角

自动内容安全的重要性随用户规模增长呈指数级上升。在产品早期阶段（MVP），人工审核是合理选择。但当用户达到数千甚至数万时：
- 人工审核无法覆盖所有内容，违规内容暴露时间以小时/天计
- 法律法规风险：许多司法管辖区对平台有内容审核义务（DSA、IT 法案）
- 品牌风险：一例的严重违规内容曝光可能造成不可逆的声誉损害

实现建议：采用**分层策略**——先引入哈希匹配（成本最低，匹配最确定），再加入基于规则的文本过滤，最后考虑 AI 分类。

| 优先级 | 功能 | 成本 | 风险降低 |
|--------|------|------|---------|
| P0 | 已知有害内容哈希匹配（已有 Checksum 基础设施） | 低 | 中 |
| P1 | 基于规则的文本过滤（正则 + 关键词列表） | 低 | 中 |
| P2 | AI 内容分类（NSFW / 仇恨言论） | 高 | 高 |

---

## 方向二：异步工作者编排框架（Async Worker Orchestration Framework）

### 问题陈述

当前后台异步任务全部使用**重复的临时模式（ad-hoc pattern）**——每个后台任务自行管理自己的 goroutine、ticker、done channel、start/stop 生命周期。这种模式在仅有 3-4 个后台任务时尚可接受，但随着引入文件内容管线、安全扫描、推送通知、数据生命周期管理等更多异步工作，重复的 boilerplate 将成为维护负担和错误来源。

### 代码级证据

**证据 1：四个独立的后台任务使用完全相同的生命周期模板**

```go
// internal/outbox/relay.go:93-111
func (r *Relay) Start(interval time.Duration) (stop func()) {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        ctx := context.Background()
        for { select {
            case <-t.C: r.drain(ctx)
            case <-done: t.Stop(); return
        }}
    }()
    return func() { close(done) }
}

// internal/storage/gc/sweeper.go:193-208
func (s *Sweeper) Start(interval time.Duration) (stop func()) {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        ctx := context.Background()
        for { select {
            case <-t.C: s.Run(ctx)
            case <-done: t.Stop(); return
        }}
    }()
    return func() { close(done) }
}

// internal/pkg/ratelimit/ratelimit.go:108-122
func (l *Limiter) RunCleaner(interval time.Duration) (stop func()) { ... } // 相同模式

// internal/idempotency/store.go:144-158
func (s *GormStore) RunCleaner(interval time.Duration) (stop func()) { ... } // 相同模式
```

四个 Start/RunCleaner 函数**逐字拷贝**了相同的 goroutine + ticker + done channel + stop func 模板。模板约有 15 行，被精确复制了 4 次。

**证据 2：Composition root 中需要分别管理和追踪这些 stop 函数**

```go
// internal/app/app.go:24-27
type App struct {
    stopRelay     func()
    stopGC        func()
    stopRateLimit func()
    stopIdemClean func()
    ...
}
```

```go
// internal/app/app.go:212-228
func (a *App) shutdown() {
    if a.stopRelay != nil { a.stopRelay() }
    if a.stopGC != nil { a.stopGC() }
    if a.stopRateLimit != nil { a.stopRateLimit() }
    if a.stopIdemClean != nil { a.stopIdemClean() }
    ...
}
```

每次新增一个后台任务就需要：在 `App` 结构体中添加一个 `stopX` 字段，在 `New` 中赋值，在 `shutdown` 中调用。这是**脆弱的手动注册模式**。

**证据 3：所有后台任务都使用 `context.Background()`，无父 span 和健康检查**

```go
ctx := context.Background() // 出现在 relay.go、sweeper.go 等所有后台任务中
```

没有 parent context → 无法传播 traces → 后台任务中的操作在分布式追踪中不可见。也无法通过 context 传播优雅关停信号（虽然每个任务有独立的 stop chan，但不是 context-based）。

**证据 4：无统一的指标注册、健康状态、错误处理和并发控制**

每个后台任务各自决定如何记录指标（有 metrics sink 的 → 没有的自行忽略）、如何处理错误（日志并继续 vs. 退避 vs. 停止）、以及如何控制并发（目前都单线程，但有并发需求时无框架支持）。

### 架构师视角

建议引入轻量级的 **`internal/pkg/task`** 包，提供以下抽象：

```go
// 任务描述
type Task struct {
    Name     string
    Interval time.Duration
    Fn       func(ctx context.Context) error
    // 可选
    OnError  ErrorHandler // 默认: log + continue
    Timeout  time.Duration
    Metrics  bool
}

// 管理器
type Scheduler struct {
    tasks []*Task
}

func (s *Scheduler) Start(ctx context.Context) (stop func()) { ... }
func (s *Scheduler) Health() []TaskHealth { ... }
```

收益：
1. **消除 boilerplate**：所有定时任务一次编写
2. **统一的生命周期管理**：一个 `Scheduler.Start(ctx)` 替代 4 个独立的 Start
3. **统一的错误处理策略**：可配置退避、重试、dead-letter
4. **统一的 metrics 暴露**：自动记录每次运行时长、成功/失败计数
5. **统一的 trace 传播**：从 `Scheduler` 的 parent span 创建子 span
6. **统一的健康检查**：每个任务暴露其最后一次运行的状态和时间

### 实现路径概要

1. 新增 `internal/pkg/task` 包，定义 `Task` 和 `Scheduler`
2. 将所有现有后台任务（relay、GC、rate limit cleaner、idem cleaner）迁移到 Scheduler
3. 为每个任务添加 `task Name` + 可选的 `Timeout`
4. 新增 `GetTaskHealth()` 端点，暴露每个任务的状态、最后运行时间、错误数
5. 在 App 中移除 4 个 stopFunc 字段，替换为一个 `scheduler.Stop()`

---

## 方向三：Outbox Handler 幂等性保障（Idempotent Outbox Handlers）

### 问题陈述

当前 outbox relay 保证**至少一次（at-least-once）** 交付，但下游 handler（通知 dispatcher、webhook handler）**是幂等（idempotent）的**。当 handler 成功处理事件但在 relay 标记 `published` 前崩溃，事件会在重启后被重复处理，导致重复的通知行和重复的 Webhook 调用。

### 代码级证据

**证据 1：NotificationDispatcher.Handle 无事件级去重**

```go
// internal/notification/dispatcher.go
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    n := d.notificationFor(e)
    if n == nil { return nil }
    // ... block check ...
    if err := d.notifs.Create(ctx, n); err != nil { // <-- 每次创建新通知行
        return err
    }
    // ... SSE push ...
    return nil // ← 成功返回，即使 SSE push 失败也是 nil（不检查 pusher 错误）
}
```

没有检查"此事件的 notification 是否已存在"。每次 Handle 都无条件 Create。

**证据 2：Relay 的 MarkPublished 在 handler 成功后异步执行**

```go
// internal/outbox/relay.go:80-89
for _, e := range events {
    // ...
    if err := r.handler.Handle(ctx, e); err != nil {
        r.repo.MarkFailed(ctx, e.ID, ...) // 标记失败
        continue
    }
    published = append(published, e.ID)   // 收集成功事件
}
// ...
r.repo.MarkPublished(ctx, published)     // 批量标记已发布
```

在 `Handle` 成功和 `MarkPublished` 之间存在时间窗口。如果进程在该窗口内崩溃，重启后事件仍然是 `published_at IS NULL`，relay 会再次拾取并处理。

**证据 3：WebhookHandler.Handle 也是非幂等的**

```go
// internal/outbox/webhook.go
func (h *WebhookHandler) Handle(ctx context.Context, e *outbox.Event) error {
    if !h.matches(e.EventType) { return nil }
    // ...
    resp, err := h.client.Do(req) // POST 到外部端点
    // 如果成功返回但在 MarkPublished 前崩溃 → 重复 POST
}
```

如果外部 API 不是幂等的（比如创建了一条记录的 API），重复的 Webhook 调用会导致外部系统重复数据。

**证据 4：Event ID 是天然的幂等键，但未被使用**

```go
// internal/outbox/outbox.go
type Event struct {
    ID uuid.UUID  // UUIDv7，全局唯一
    ...
}
```

每个 outbox 事件都有全局唯一的 `ID`。下游 handler 可以用 `(event_id, handler_name)` 作为幂等键，检查是否已处理过。但没有任何 handler 这样做。

### 架构师视角

这是一个经典的 **at-least-once 投递 + 非幂等消费**的可靠性问题。有两种修复路径：

**方案 A（轻量级）：Handler 自带幂等检查**
- `NotificationDispatcher.Handle` 在处理前检查是否已有 `(event_id, "notification")` 的映射记录
- 新增 `notification.event_id` 列（可选，NULL 兼容现有数据），唯一约束确保同一个 outbox event 不会产生两条通知
- 对现有的已通知记录，`event_id` 列为 NULL，不做回溯校验

```go
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    // 幂等检查
    if d.notifs.ExistsByEventID(ctx, e.ID.String()) {
        return nil // 已处理过，跳过
    }
    n := d.notificationFor(e)
    // ...
}
```

**方案 B（彻底）：引入 Outbox Handler Idempotency 框架**
- 在 `outbox.Handler` 接口外层增加 `IdempotentHandler` 包装器
- 使用轻量级内存映射（`sync.Map`）或 DB 表 `handler_idempotency` 记录已处理的事件 ID
- 包装器在调用真实 handler 前检查幂等性，处理成功后持久化

```go
type IdempotentHandler struct {
    inner Handler
    store IdempotencyStore // 可以是 DB 表、内存 map 或 Redis
}

func (h *IdempotentHandler) Handle(ctx context.Context, e *Event) error {
    key := h.eventKey(e)
    if h.store.Processed(key) {
        return nil
    }
    defer h.store.MarkProcessed(key)
    return h.inner.Handle(ctx, e)
}
```

### 优先级

| 方案 | 实现成本 | 正确性保证 | 推荐 |
|------|---------|-----------|------|
| A：Handler 自带幂等检查 | 低（1-2 天） | 仅通知，不通用 | 近期（P0） |
| B：IdempotentHandler 包装器 | 中（3-5 天） | 全 handler 覆盖 | 中长期（P1） |

**立即行动的 P0 项**：在 NotificationDispatcher 中添加幂等检查——创建通知前检查 `(event_id, handler_id)` 组合是否已存在。

---

## 方向四：跨实体引用生命周期管理（Cross-Entity Reference Lifecycle）

### 问题陈述

aero-id 中存在大量跨实体引用（File 被 Message 引用、Message 被 Notification 引用、Comment 引用 File/Message、Share 引用 File、Block 引用 User），但**没有任何系统化的机制来处理引用源变更或删除时的级联效果**。当引用的实体被软删除后，引用方仍然指向一个逻辑上不存在的目标，系统行为在各端点间不一致。

### 代码级证据

**证据 1：Message 引用 File，但 File 删除后 Message 仍持有 FileID 且无处理**

```go
// internal/message/service.go:71-86
func (s *service) checkSendFile(ctx context.Context, fileID *domain.ID, actor string) error {
    if fileID == nil { return nil }
    f, err := s.files.Get(ctx, *fileID)
    if stderrors.Is(err, domain.ErrNotFound) {
        return apperr.BadRequest("message.invalid", "referenced file does not exist")
    }
    // ... 仅检查文件存在和所有权
    return nil
}
```

但在**读取**消息时：

```go
// internal/message/service.go:115-130
func (s *service) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
    m, err := s.messages.Get(ctx, id)
    // ... 权限检查
    return m, nil // 直接返回消息，FileID 指向可能已软删除的文件
}
```

消息中 `FileID` 指向的文件可能已被软删除。调用方收到一个引用后需要自己去检查文件是否存在——这是个 UX 和 API 一致性缺口。

**证据 2：文件分享的授权撤销不清理引用**

```go
// internal/file/service_share.go
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error { ... }
func (s *fileService) UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error {
    // 从 file_shares 表中删除授权行，不通知 grantees
    // 也不删除任何已存在的对被分享文件的引用（消息、评论等）
}
```

当文件所有者撤销分享时，grantee 已经嵌入在消息中的 `FileID` 引用**不会失效**。Grantee 仍然可以通过消息中的 `FileID` 看到文件存在，但下载时会失败。

**证据 3：用户屏蔽不清理已存在的对话**

```go
// internal/block/block.go
func (s *service) BlockUser(ctx context.Context, targetUID string) error {
    // 创建 block 行，拦截未来的消息和通知
    // 但不清理历史消息、不失效已存在的通知行
}
```

Block 后，已存在的消息和通知行保留在数据库中。被屏蔽方仍能看到历史消息（在 Inbox/Conversation 中），只是不再收到新消息。

**证据 4：没有"用户注销/删除"的处理端点**

```go
// internal/handler/http/router.go — 所有已注册的 handler
```

没有任何一个 handler 处理 SSO 用户删除事件。当用户从 SSO 删除后，aero-id 中的文件、消息、评论、分享、块列表、审计日志全部变成**悬挂数据（orphan data）**——指向一个不存在的 `uid`。

**证据 5：Notification 引用 SubjectID（可删除的 Comment/Message）**

```go
// internal/domain/notification.go
type Notification struct {
    SubjectType string // "message", "comment"
    SubjectID   string // 引用 ID，无外键约束
    ...
}
```

如果引用的 Comment 或 Message 被软删除，Notification 仍然存在。调用方 GET `/v1/notifications` 时，需要自己去检查 SubjectID 是否仍然有效。

### 架构师视角

这是一个系统性的设计缺口。核心问题是：**aero-id 在所有领域中都使用软删除，但没有建立级联处理协议**。每个引用场景（File → Message、File → Share、Message → Notification、Block → Conversation）都有不同的处理方式。

建议引入一个**引用生命周期策略（Reference Lifecycle Policy）**：

| 引用类型 | 源删除时引用行为 | 实现方式 |
|---------|----------------|---------|
| Message → FileID | File 软删除后，Message 显示"文件已删除" | 查询时检查 File.DeletedAt |
| Comment → TargetID | Target 删除后，Comment 正常显示 | 读取时按需检查 |
| Notification → SubjectID | Subject 删除后，Notification 显示"内容已删除" | 通知查询时 Join 检查 |
| FileShare → File | File 删除后，授权自动失效 | Grant 查询时 JOIN 检查 |
| Block → UID | Block 关系解除后不清理历史 | 不变 |

架构级改进：
1. **新增 `internal/domain/lifecycle.go`**：定义 `ReferencePolicy` 枚举和 `CheckReference` 接口
2. **在每个 Service 的 Read 路径中应用策略**：如在 `file.Get` 中加入对已软删除文件的"幽灵引用"指示
3. **用户注销端点**：`POST /v1/account/purge` 或监听 SSO 的 `account.deleted` 事件，触发：
   - 软删除所有文件（GC 后续清理）
   - 保留消息/评论但标记 `owner_deleted`
   - 清理分享授权
   - 清理通知（可选保留历史）
   - 保留审计日志
4. **引用完整性指标**：新增 gauge 记录悬挂引用的数量

### 产品经理视角

用户注销/删除是一个法律合规问题（GDPR 第 17 条"被遗忘权"）。缺少用户数据清理端点不仅是产品缺口，而且是合规风险。即使 SSO 处理了身份删除，aero-id 作为数据控制者仍有义务在收到删除请求时清理用户的内容。

---

## 方向五：逐实例健康状况聚合与运维仪表盘（Per-Instance Health Aggregation & Ops Dashboard Foundation）

### 问题陈述

当前 aero-id 的健康检查（`/healthz`、`/readyz`、`/metrics`）是**实例级别的**，只反映当前进程的健康状态。在水平扩展部署中（多实例运行），运维人员无法获得**集群级别**的整体健康视图：不知道有多少实例健康、哪里存在流量热点、某实例是否开始内存泄漏。也没有异常的主动检测机制——健康检查只回答"它现在活着吗"，不回答"它正在正常地死吗"。

### 代码级证据

**证据 1：`/readyz` 只做二值检测，无渐变/评分**

```go
// internal/handler/http/health.go
type Check struct {
    Name     string
    Critical bool
    Probe    func(context.Context) error
}
// /readyz 返回 200（全部通过）或 503（任一 Critical 失败）
// 没有 "部分降级" 的概念，没有每个检查的耗时/成功率
```

如果一个实例的 SSO 响应时间从 5ms 恶化到 5s，`/readyz` 仍然返回 200。没有渐变式降级信号。

**证据 2：`/metrics` 暴露的是聚合计数器，不是实例级别的诊断信号**

```go
// internal/pkg/metrics/metrics.go
// 暴露 HTTP 请求数/延迟/错误率、gRPC 指标、SSO 缓存命中率
// 但没有：goroutine 数量、内存使用量、GC 停顿时长、打开的文件描述符、数据库连接池状态
```

缺少**Go 运行时指标**（`runtime.ReadMemStats`、`runtime.NumGoroutine`）和**进程级别指标**（`/proc/self/status` 的 FD 计数）。

**证据 3：没有异常主动检测（Anomaly Detection）**

```go
// 没有任何地方记录/比较历史指标
// 没有任何告警阈值配置
// 没有任何自诊断机制
```

如果 goroutine 数量在 5 分钟内从 10 增长到 1000（goroutine leak），系统不会检测到异常，也不会自动触发 `pprof` 转储供事后分析。

**证据 4：无内置 pprof 端点**

```go
// internal/handler/http/router.go
// 注册了健康检查、metrics、认证路由，但没有 pprof 端点
```

Go 标准库的 `net/http/pprof` 没有被启用。当出现性能问题时，运维人员无法在线获取 goroutine 堆栈、heap profile、CPU profile。

**证据 5：Outbox relay 有 metrics sink 但 GC sweeper 没有**

```go
// internal/outbox/relay.go:53-62
type MetricsSink interface {
    ObserveOutboxPublished(eventType string)
    ObserveOutboxFailed(eventType string)
    ObserveOutboxDrain(pending int, oldestAge, dur time.Duration)
}
```

```go
// internal/storage/gc/sweeper.go
// Run() 返回 Stats，但 Stats 不会被记录为 metrics
```

```go
// internal/pkg/ratelimit/ratelimit.go
// 无 metrics 暴露
```

```go
// internal/idempotency/store.go
// RunCleaner 无 metrics
```

后台任务的可见性不一致——outbox relay 有完整的 metrics，而 GC sweeper、rate limit cleaner、idempotency cleaner 没有任何指标。

### 架构师视角

从运维成熟度的角度，建议建立**三层健康视图**：

**L1 — 实例健康（当前已有，但可增强）**
- 增强 `/healthz` 和 `/readyz` 为**结构化的健康报告**（JSON 响应而非纯 200/503）
- 每个检查返回：`name`、`status`（ok/degraded/failed）、`latency_ms`、`last_success`、`failure_count`
- 启用 pprof 端点（`/debug/pprof/`），gRPC 端口通过配置控制

**L2 — 运行时诊断信号**
- 通过现有 metrics 注册表增加：
  - `runtime.go.goroutines` — goroutine 数量
  - `runtime.go.alloc_bytes` — 堆分配
  - `runtime.go.gc_duration_seconds` — GC 停顿
- 后台任务统一 `Scheduler`（方向二）后，自动暴露每个任务的 `task.duration_seconds`、`task.failures_total`

**L3 — 集群健康聚合（需要外部配套设施）**
- 健康检查响应中包含 `instance_id`（启动时生成，持久化到文件或环境变量）
- 所有结构化健康报告通过 Prometheus 拉取 → Grafana 看板聚合
- 预定义告警规则（goroutine 异常增长、SSO 延迟 P99 > 5s、dead letter 计数增长）

### 产品经理视角

运维可见性直接影响 SLO 和 MTTR（平均修复时间）。在无集群健康视图的情况下：
- 实例的内存泄漏可能在影响 50% 的流量后才被发现
- 无法区分"一台实例挂了"和"所有实例都挂了"
- 无法为每个实例的金丝雀发布（canary deploy）提供信心

```yaml
# 推荐的增强健康检查响应
GET /readyz
{
  "status": "degraded",
  "instance_id": "aero-id-v1-abc123",
  "checks": [
    {"name": "database", "status": "ok", "latency_ms": 2},
    {"name": "sso", "status": "degraded", "latency_ms": 3200, "failure_count": 3, "last_success": "2026-07-11T10:00:00Z"},
    {"name": "storage", "status": "ok", "latency_ms": 45}
  ],
  "runtime": {
    "goroutines": 42,
    "alloc_mb": 128,
    "gc_pause_ms_max": 15,
    "uptime_seconds": 86400
  }
}
```

---

## 优先级总结

| # | 方向 | 类别 | 优先级 | 成本 | 核心收益 |
|---|------|------|--------|------|---------|
| 1 | **自动内容安全管线** | 合规/产品 | P1 | 中-高 | 从人工审核→自动安全扫描，合规前置 |
| 2 | **异步工作者编排框架** | 架构/运维 | P1 | 低 | 消除 4 份重复 boilerplate，统一生命周期，为所有未来后台任务提供基础设施 |
| 3 | **Outbox Handler 幂等性** | 可靠性 | **P0** | 低 | 修复 at-least-once 投递导致的通知/Webhook 重复问题 |
| 4 | **跨实体引用生命周期** | 数据完整性/合规 | P1 | 中 | 解决软删除导致的悬挂引用，实现用户数据清理（GDPR） |
| 5 | **实例健康聚合与诊断** | 运维 | P1 | 低-中 | 结构化健康报告 + pprof + Go 运行时指标，提升 MTTR |

### 快速获胜项（1-2 天内可完成）

1. **方向三的 P0 项**：在 `NotificationDispatcher` 中添加 `event_id` 列和幂等检查——1 天
2. **方向五的 pprof 端点**：在 HTTP router 中注册 `debug/pprof`——30 分钟
3. **方向五的运行时指标**：在 `metrics.Registry` 中增加 goroutine/alloc/gc 指标——半天
4. **方向二的 `internal/pkg/task` 包初始化**：定义 `Task` 和 `Scheduler` 接口——1 天
