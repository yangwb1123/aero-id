# 架构分析报告：aero-id

## 目录
1. [架构评估](#1-架构评估)
2. [扩展方向](#2-扩展方向)
3. [接口设计建议](#3-接口设计建议)
4. [技术选型](#4-技术选型)
5. [实施路线图](#5-实施路线图)

---

## 1. 架构评估

### 1.1 当前架构的优势

**分层清晰，职责分离严格。** 横向分层（handler → service → repository/storage/sso）是已证明的成功模式。代码库的执行质量令人印象深刻：

- **service 层是唯一的业务逻辑所在地**，handler 只做编解码 → 调用 → 渲染，repository 只做 GORM 持久化。这意味着任何跨协议的行为差异都是 bug，而非设计使然。
- **Option 模式**（`WithBlockChecker`、`WithFileAuditLogger`、`WithStorageRefs`）使依赖注入既可测试又增量可扩展。新能力不需修改现有构造函数签名。
- **Transactional outbox 模式**（`internal/outbox`）已然落地。`message.sent`、`message.read`、`comment.replied` 事件与领域数据在同一事务写入，由 Relay 异步消费。这为未来的通知推送、webhook、下游处理提供了稳固基础，且不阻塞请求路径。
- **接口提取精确**：`UserDirectory`（仅是 `GetUsers` 子集）、`BlockChecker`（仅是 `IsBlocked`）、`SignedDownloader` 都是最小化接口，使测试 mock 极轻、领域边界清晰。

**组合根（`app.go`）是架构的 P&L 表。** 所有依赖在初始化时一次性注入，没有一个 `init()` 或全局状态（除 zap logger 外）。这使得检测循环依赖、审计配置泄漏、理解整体拓扑变得容易。约 200 行的 `New()` 完整反映了系统结构。

**工程约束（文件 ≤ 500 行，函数 ≤ 50 行，复杂度 ≤ 10）被强制执行。** 这在实践中有效。我通读了 60+ 行以上的函数，它们都没有感觉臃肿——它们整洁地使用早期返回和守卫子句。这是一个训练有素的代码库。

### 1.2 当前架构的局限性

以下不是否定，而是未来增长的约束：

**L1 — 领域模型贫血。** 目前 `domain.Message`、`domain.File`、`domain.Comment` 是纯数据 DTO（所有字段公开，无方法）。尽管在 ACID 环境中这尚可接受，但当一个实体开始承载多种状态——已调度/已发送/已撤回、已发布/旧版本/草稿——纯数据结构便不足以约束非法转换。未来需要状态封装。

**L2 — 审计是 fire-and-forget，不是事件。** 代码库正确识别了这一点。14 处 `_ = s.audit.Log(...)` 意味着审计日志静默失败且不可重试。在需要合规可追溯性的账户删除工作流中，这行不通。审计流入应通过 outbox（如消息事件），而非 side-effect。

**L3 — 没有物化的读模型（即 CQRS 缺失）。** 当前所有读者查询直接访问写模型表。这对当前规模尚可，但随着：a) `reply_count` 去归一化计数需在写入路径递增；b) 评论列表需穿越两层嵌套树；c) 搜索命中需联合类型——数据模型开始在不同的读取上下文中承载不同形状。这里的真实技术债不在于数据库性能，而在于**读取路径的耦合**：变更写模式会影响读查询，反之亦然。

**L4 — 软删除是单向操作。** 软删除后，记录对原始用户不可见（通过 `WHERE deleted_at IS NULL` 范围），但并不真正消失。GC 只在文件上运行。消息和评论软删除无限累积。这不是功能缺陷（它被有意省略）；这是运维局限——在用户删除账户后，无法优雅地处理「该用户的数据发生了什么」这一问题。

**L5 — 不存在核心域事件驱动跨实体的层。** 虽然 outbox 用于「消息 → 通知」，但不存在共享的域事件总线来协调跨实体的变更。例如：删除文件应使引用它的消息中的附件不可用；撤回消息应级联删除评论。当前，跨实体约束在 service 层以临时方式处理（例如，从 service 调用 `s.files.Get` 后跟在 `s.messages.Create` 后）。

### 1.3 关键设计决策评估

| 决策 | 评估 |
|---|---|
| **No accounts table, uid 引用 SSO** | ✅ 合理。SSO 外部化消除了双写一致性问题。然将 UID 设为 `TEXT` 无校验，引入脏数据风险。应有 UID 格式校验器用于防御性校验。 |
| **横向分层，非垂直模块纵切** | ✅ 合理。文件、消息、评论自然耦合（评论可附于文件或消息；消息可引用文件）。垂直切分会导致跨模块调用的基础架构重复。 |
| **GORM 而非 sqlx** | ✅ 对 3 张稳定表且主要 CRUD 的场景合理。`JSONB` 的 `map[string]any` 是 GORM 的强项。缺点是复杂查询（如嵌套树 + 分页 + 去归一化计数）更难调优。 |
| **Token 缓存 60秒** | ✅ 正确权衡。撤销最多延迟 60 秒。采用 `sync.Map` + 后台清扫简单且有效。 |
| **Transactional outbox** | ✅ 当前代码库最佳决策之一。可靠的通知和未来的 webhook 是架构基础。 |
| **认证通过拦截器注入 actor_uid** | ✅ 干净地对齐了 Go 的 context 模式。 |
| **两层评论嵌套** | ✅ 产品约束在领域层正确校验。但 `ValidateNewComment` 仅存在于领域层——历史上，一旦 `comment_versions` 表允许回滚到不同嵌套深度的版本，业务规则可能会失效。 |
| **文件存储双实现（local + S3）** | ✅ `FileStorage` 接口抽象良好。local 后端通过 HMAC 签名 URL 处理 presigned 请求，无需外部依赖。 |

### 1.4 架构债务（非阻塞，但逐渐累积）

1. **`internal/hub/sse.go` 作为实时推送** — SSE hub 是进程内的。在多副本部署中，连接到副本 A 的用户不会收到触发于副本 B 的通知。这不是当前的 bug，但如果实时是首要功能，则存在架构债务。这在单进程环境中完全合理，因此我将其列为设计**约束**而非缺陷。

2. **`search` 作为 service 中的联合类型** — 搜索返回联合结果（`*Message`/`*Comment`/`*File`）。当后端搜索要求在这些类型之间进行标准化打分时，当前的 service 级查询模式（分别查询每个仓库并在 Go 中排序）将无法伸缩。需采用像 MeiliSearch 或 Typesense 这样的专用搜索引擎。

3. **去归一化计数缺乏一致性守卫** — `reply_count` 在每次插入时增加；`like_count` 同样增加。没有后台协调来修正从 bug 或并发中产生的数据漂移。在账户删除过程中，级联删除回复将导致 `reply_count` 不同步。需要定期的对账作业。

---

## 2. 扩展方向

### 方向 A：域事件总线与跨实体编排

**为什么需要：** 当前，跨实体约束——「删除文件应使引用它的消息中的附件不可用」「撤回消息应级联删除评论」——以临时方式实现（service 中的 `s.files.Get` → `s.messages.Create`）。随着实体数量和交互增加，这会导致隐式依赖网和循环引用风险。

**核心挑战：**
- 避免在 service 之间产生导入循环（当前 `fileService` 不导入 `messageService`，反之亦然）
- 事件类型版本化（已发出的事件可能会被未来消费者以不同方式解释）
- 回滚语义：如果下游处理程序失败，上游事务是否应回滚？（通常不应，但需记录）

**预期的架构变更：**

```
Current:     ServiceA → ServiceB (direct call)
Proposed:    ServiceA → eventBus.Emit("file.deleted") → EventHandler → ServiceB
```

引入 `internal/domain/eventbus.go`：

```
type EventBus interface {
    Emit(ctx context.Context, event DomainEvent) error
    Subscribe(eventType string, handler EventHandler) (unsubscribe func())
}
```

每个 service 在 `app.go` 中将自身注册为事件订阅者：

```go
eventBus.Subscribe("file.deleted", messagingAdapter.onFileDeleted)
eventBus.Subscribe("account.deleted", fileAdapter.onAccountDeleted)
eventBus.Subscribe("message.recalled", commentAdapter.onMessageRecalled)
```

事件总线可以是纯同步的（当前同一事务内，因此保证 ACID）或通过 outbox 异步的。关键点在于**显式的订阅注册**消除了隐式 service-to-service 导入。

**对现有系统的影响：** 低。服务接口不变。在 `app.go` 中添加几行订阅注册。从现有的临时交叉实体逻辑（如评论服务中的 `s.messages.Get`）迁移是增量的。

**选项权衡：**

| 选项 | 优点 | 缺点 |
|---|---|---|
| **A1：同步内存总线**（在当前 tx 中运行） | 简单；零延迟；与 outbox 事务一致 | 处理程序失败会失败上游操作；在单个进程中耦合所有处理 |
| **A2：通过 outbox 的异步事件** | 已存在基础架构；错误隔离；重试 | 最终一致性；一次发出的事件在重播时可能具有不同的语义 |
| **A3：混合**（同步用于文件校验，异步用于通知） | 两者优点 | 更复杂的合约（事件处理程序需声明其同步/异步性质） |

**推荐：** 从 **A1** 开始（低开销），当需要异步解耦时进化到 **A3**。

---

### 方向 B：实体版本化与变更历史

**为什么需要：** 当前编辑是破坏性覆盖（无历史记录）。对于消息、评论和文件元数据，这在法律/合规语境中是不够的。「用户删除了那条评论吗？不，他们可能编辑了它——但我们没有记录。 」当前审计日志记录操作但不记录内容，留下了可追溯性缺口。

**核心挑战：**
- 与现有数据模型的后向兼容性（历史为空的旧行应作为版本 1 继续工作）
- 两层嵌套规则（`comment_versions` 在回滚时必须验证嵌套深度，该深度可能在创建时所处的嵌套规则不同于当前规则发生变化）
- 存储影响：每条消息 100 条编辑 x 1KB = 每用户 100KB。对于即时通讯类消息使用率，可接受。对于评论（更持久），很重要。
- 编辑在版本历史中的可见性：如果用户编辑了消息以移除敏感内容，删除所有历史记录的「硬删除」选项可能会破坏合规性。需要分级保留策略。

**预期的架构变更：**

为每个版本化的实体新建 `*_versions` 表：

```sql
CREATE TABLE message_versions (
  id          UUID PRIMARY KEY,
  message_id  UUID NOT NULL REFERENCES messages(id),
  version     INT NOT NULL,
  content     TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL,
  UNIQUE(message_id, version)
);
```

当前 `domain.Message.EditedAt` 成为最后一次编辑的时间戳。`EditedAt` 为 null → 无历史记录。

**对现有系统的影响：** 中等。`Edit()` 现有方法将改为追加版本而非覆盖。`Get()` 默认返回最新版本；可选择 `?version=N`。写路径的变化是最小的（在编辑事务中追加一行）。读路径可延迟。

**与方向 A 的关系：** 版本化是从事件中自然产生的——每个编辑都是一个 `message.edited` 事件，其携带 `{old_content, new_content}`。事件总线使版本收集成为一个独立的关注点。

---

### 方向 C：消息调度、草稿与撤回

**为什么需要：** 即时通讯平台的标准功能。调度允许在设定时间发送消息（时区感知的传递）。草稿允许在不提交的情况下创作。撤回（Recall）从接收方收件箱中移除消息（不隐藏发送方，而是撤回消息本身，可能触发撤回通知）。

**核心挑战：**
- 撤回不同于软删除：撤回阻止接收方查看，向接收方通知撤回，且不触发审计事件（其是发送方行使的权利）。软删除在发送方自己的视图中隐藏消息，同时该行保留在接收方侧。
- 已调度消息的时区处理：`ScheduledAt` 应以 UTC 存储，但在向用户显示时应通过 `meta["tz"]` 或用户偏好进行本地化。
- 已调度消息的撤回：如果消息在发送前被撤回（即在调度生效之前），调度任务不应发出任何内容。

**预期的架构变更：**

向 `domain.Message` 添加字段：

```
ScheduledAt  *time.Time   // nil = 立即发送
Status       MessageStatus // draft | scheduled | sent | recalled
RecalledAt   *time.Time   // 当 Status == recalled 时设置
```

当前「创建 → 立即持久化」路径变为「创建 → 插入为 `scheduled`/`draft` → 根据需要过渡状态」。

需要调度 worker（outbox 风格的轮询或 cron 作业）从 `messages` 表中拾取 `WHERE status = 'scheduled' AND scheduled_at <= now()`。

**对现有系统的影响：** 中等-高。工作流从「一次写入，始终可见」变为状态机。识别三态的消息 API：

| 状态 | 发送方可执行的操作 | 接收方是否可见 |
|---|---|---|
| `draft` | 编辑、删除、发送 | 否 |
| `scheduled` | 撤回、重新调度 | 否（直至 `scheduled_at`） |
| `sent` | 撤回 | 是 |
| `recalled` | 无 | 否（但发送方看到「已撤回」状态） |

`Recall()` 是一个新方法，它：
1. 在接收方的 inbox 中将消息标记为已撤回（不删除该行，但将其从常规列表查询中排除）
2. 向接收方发出 `message.recalled` 通知（通过 outbox）
3. 为不可逆的操作记录审计事件

---

### 方向 D：API 传输优化（字段选择、ETag、批量操作）

**为什么需要：** 这是作者分析中 P0 优先的方向——本质上是 5 行中间件代码用于 gzip，但 ETag 和字段选择需要更多设计。目前，`GET /v1/files/:id` 返回 `storage_type`（元数据臃肿），且所有列表返回没有分页元数据的完整实体。对于移动客户端（低带宽、高延迟），这直接损害用户体验。

**核心挑战：**
- 字段选择需要服务层和 DTO 了解哪些字段被请求。Go 的零值问题（`omitempty` JSON）使序列化复杂的部分响应变得繁琐。
- ETag 需要存储每个资源的弱校验和。对于 `updated_at`（存在），这就是 `ETag: W/"<timestamp>"`——但资源不变时，etag 稳定吗？文件通过 `UpdatedAt` 更改（是），消息通过 `EditedAt`（否——建模为单独的字段，因此 `UpdatedAt` 需要更新）。这决定了条件请求的设计。

**预期的架构变更：**

1. **Gzip 中间件：** 5 行（`gin-contrib/gzip` 或自定义）。无架构变更。
2. **ETag 支持：** 添加 `ETagger` 接口，每个领域的 `Get` 返回 `etag`（例如，`sha256hex(updated_at.String())`）。Handler 读取 `If-None-Match` 并返回 `304 Not Modified`。这是 handler 层的变化，service 不变。
3. **字段选择：** 最大的变化。选项：
   - **选项 D-a（简单）：** `?fields=id,name,mime_type,size` 查询参数 → 在 handler 中解析 → 作为 `FieldMask` 传递给 service → DTO 省略未选字段。需要更改所有 DTO 序列化器（使用 `json:"...,omitempty"` 但需小心零值）。
   - **选项 D-b（GraphQL 风格）：** 添加单独的 GraphQL 端点。过度，不符合当前 REST 风格。
   - **选项 D-c（跳过）：** 推迟，接受当前的完整性响应。对 MVP 合理。

**推荐：** D-a 但限制为**列表**操作，而非单个 Get。列表通常最需要带宽缩减。单个 Get 通常是直接资源获取，完整表示合理。

---

### 方向 E：用户数据可移植性与账户删除

**为什么需要：** GDPR/CCPA 合规数据导出和删除。此外，也是用户信任的基础功能——如果用户离开平台，他们的数据应能够带走并被彻底删除。

**核心挑战：**
- 级联删除必须尊重引用完整性：删除消息 → 删除其评论 → 删除通知（如果级联逻辑出错，通知接收方会留下指向已删除实体的悬空链接）。
- 审计完整性：在发出删除命令时，审计日志不能静默失败（当前的火抛忘模式）。级联删除必须使用 outbox 事件以保证幂等性。
- 数据导出需要可枚举所有用户数据的能力，跨越消息（发送和接收）、文件（拥有和分享给用户）、评论。需要粉丝查询。

**预期的架构变更：**

- `AccountLifercycleService`: 一个编排账户删除/导出的新 service。
- 账户删除工作流：
  1. 验证用户身份（SSO）并记录审计事件（通过 outbox）
  2. 级联软删除所有拥有的数据（消息、文件、评论）
  3. 将用户标记为`deleted`（可通过 `uid` + 时间戳列，或通过 `deleted_users` 表）
  4. 调度一个 GC 传递以硬删除标记为删除的行
- 数据导出端点 `GET /v1/export`：以分页 JSON Lines 流返回所有用户数据，压缩。

**对现有系统的影响：** 高触摸面。影响所有 3 个 domain service + 审计 + 存储 GC。这是作者正确识别为高优先级但跨域复杂的内容。

---

## 3. 接口设计建议

### 3.1 核心设计原则

**原则 1：Service 接口是合同，repository 是实现细节。** 当前代码库在这方面做得正确。`MessageService`、`FileService`、`CommentService` 接口在 `service.go` 中定义，所有 handler 依赖这些接口。repository 接口在 `repository/` 包中定义，由 service 依赖。**不打破这个抽象。**

**原则 2：选项模式优于方法重载。** 当前模式（`WithBlockChecker`、`WithFileAuditLogger`）是良好的。保留它。当引入调度或版本化时，添加 `WithScheduler`、`WithVersionRepo`，而不是修改构造函数签名。

**原则 3：领域事件不应导入 Go 接口。** 事件是数据，而非行为。当前 `outbox.Event` 是一个具有 JSON 载荷的 struct。这很好——但如果事件载荷成为版本化的 protobuf 消息，则应使用单独的 `DomainEvent` 类型，且不依赖于 protobuf 生成。保持 domain 纯净。

**原则 4：新业务逻辑从 route 注册开启。** `RouteRegistrar` 接口（在 `router.go` 中）允许在不接触 router 包的情况下增量添加功能。这是刻意设计的。任何需要注册新路由的新扩展都应作为实现 `RouteRegistrar` 的独立 handler（例如 `RecallHandler`、`ExportHandler`）出现。

### 3.2 需要的新抽象

**N1：`FieldMask` / 选择器类型**

当前，service 接口接受完整输入并返回完整实体。对于字段选择，需要将一组选定字段通过堆栈向下传递。

```
type FieldSelector []string  // 或使用 protobuf 的 FieldMask
```

每个 service 方法可选地接受 `FieldSelector`。若为 nil（或空），则返回完整实体。这个类型应在 `internal/pkg/fieldmask` 中，而非 domain 中——它是传输级概念。

**N2：`PaginationMeta`**

当前分页返回 `NextCursor` 字符串 + 实体列表。当请求字段选择时，分页元数据（`total_count`、`next_cursor`、`has_more`）应作为单独的结构体返回，而非作为 `*List` DTO 的一部分拼合：

```go
type PageResult[T any] struct {
    Items      []T
    NextCursor string
    HasMore    bool
    // 可选，在需要时设置：
    TotalCount int64
}
```

这消除了 `*List` 类型（`FileList`、`MessageList`、`CommentList`）的泛滥，并简化了泛型处理——Go 1.18+ 原生支持。

**N3：`EntityStateMachine`（用于调度/撤回）**

当消息获得状态机时（`draft → scheduled → sent → recalled`），需要与 GORM 无关的纯 Go 状态机：

```go
// 纯业务规则，无传输或持久化概念
type MessageStateMachine struct {
    status      MessageStatus
    scheduledAt *time.Time
    recalledAt  *time.Time
}

func (m *MessageStateMachine) CanTransitionTo(target MessageStatus) error
func (m *MessageStateMachine) TransitionTo(target MessageStatus, now time.Time) error
```

这驻留在 `domain/message.go` 中，并确保非法转换（`draft → recalled`）在通过 HTTP 或 gRPC 持久化之前被拒绝。

### 3.3 向后兼容性

**对于当前 MVP：** 所有当前端点必须继续工作。新功能应通过新端点或新 HTTP 方法提供：

| 功能 | 路由策略 | 兼容性影响 |
|---|---|---|
| 字段选择 | 在现有端点上添加 `?fields=` 查询参数 | 无破坏性：缺少 fields 参数 → 完整响应 |
| ETag | 添加 `If-None-Match` 支持 | 无破坏性：缺少标头 → 完整 200 |
| 草稿 | 新端点 `POST /v1/drafts`、`PATCH /v1/drafts/:id/send` | 新路径不影响现有端点 |
| 撤回 | `POST /v1/messages/:id/recall` | 新操作，不影响现有 GET/DELETE |
| 版本历史 | `GET /v1/messages/:id/versions`、`GET /v1/messages/:id?version=N` | 可选 `version` 参数，旧客户端使用默认值（最新） |
| 数据导出 | `GET /v1/export` | 全新端点 |
| 账户删除 | `POST /v1/account/delete` + `DELETE /v1/account` | 新端点 |

**过渡模式：** 对于状态模型（`draft → sent`），现有 `Send()` 端点应在无调度参数的情况下继续工作（立即发送）。草稿和调度通过具有额外字段的新输入类型引入：

```go
type SendMessageInput struct {
    // ...现有字段
    // 新：nil = 立即发送，否则在 scheduled_at 发送
    ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
    // 新：nil = 直接发送，否则从草稿发送
    DraftID *ID `json:"draft_id,omitempty"`
}
```

---

## 4. 技术选型

### 4.1 需要评估的新技术

基于扩展方向，以下是对新依赖的评估：

| 技术 | 所需者 | 评估 |
|---|---|---|
| **gzip 中间件**（gin-contrib/gzip） | 方向 D | ✅ 当前项目已具有 `gin-contrib/gzip` 作为可选依赖（从 router.go 注释看）。只需在中间件链中添加。零风险。 |
| **MeiliSearch / Typesense** | 搜索扩展 | ⏳ 当前 search 在 service 层运行（分别查询每个 repo 并合并）。对于 < 10 万条记录，这没问题。当需要竞争性评分、模糊匹配、按消息/文件/评论排序的联合分面时，引入专用搜索引擎。**现在为时过早。** |
| **Event sourcing 库**（如 `github.com/looplab/eventhorizon`） | 方向 A | ❌ 拒绝。当前 outbox 模式足够，引入全事件溯源所带来的复杂性（投影重建、快照、事件版本化）远超过收益。保持事件为包内结构体。 |
| **状态机库**（如 `github.com/looplab/fsm`） | 方向 C | ⚠️ 可能过度。`MessageStateMachine` 是一个具有 4 种状态的文件内实现。纯 Go 状态监控比外部依赖更易于推理。 |
| **golang.org/x/exp/slices**（或 Go 1.21+ slices） | 泛型分页 | ✅ 已内置于 Go 1.26。对于泛型 `PageResult[T]` 有用。 |
| **protoc-gen-validate**（PGV） | gRPC 输入校验 | ⚠️ 有用但非必需。当前 go-grpc handler 进行手动输入校验。PGV 通过 proto 注解自动生成校验，但增加了构建复杂度。评估开销是否值得。 |

### 4.2 决策框架

对于任何新依赖，应用三法则：

1. **它能与 GORM 生态共存吗？** 如果新库引入了冲突的事务管理器（例如，试图管理自己的连接池，或与 `gorm.DB` `Session` 冲突）→ 拒绝。
2. **它是否在 main goroutine 之外使用 `context.Background()`？** 如果是（例如，后台 worker）→ 确保它遵守关闭合约（当前 `app.go` 的 `shutdown()` 仔细地按依赖顺序停止后台 goroutine）。
3. **它是否在 domain/ 包中引入了外部导入？** 如果是 → 仅当 domain 规则不可表达为纯 Go 时才接受。当前的 domain 包零外部依赖。保持纯净。

### 4.3 自建 vs. 采购

| 功能 | 自建 | 采购/采用 | 理由 |
|---|---|---|---|
| **字段选择** | ✅ 自建 | | 本质上是 30 行 DTO 序列化器。无需外部库。 |
| **ETag 支持** | ✅ 自建 | | 在 gin handler 中添加 ≤ 50 行。 |
| **富媒体嵌入** | ✅ 自建 | | 解析文本 → 提取引用 → 从 `storage_refs` 查询 → 将预览注入响应。直接的领域逻辑。 |
| **全文搜索** | | ✅ MeiliSearch（当需要时） | 联合评分、模糊匹配、自动完成是搜索引擎的 sweet spot。Postgres `ILIKE` 适用于 MVP。 |
| **飞书/钉钉/企业微信 webhook** | ✅ 自建 | | 当前 `outbox.WebhookHandler` 已泛化 HMAC 签名。适配是数据映射。 |
| **数据导出 + 账户删除** | ✅ 自建 | | 强制业务逻辑深度耦合于具体领域。数据可移植性必须由 domain service 编排，而非泛化工具。 |

---

## 5. 实施路线图

### 5.1 优先级排序

我同意作者提议的优先级，并添加架构依赖注释：

| 优先级 | 方向 | 关键依赖 | 估计工作量 |
|---|---|---|---|
| **P0** | D3 — HTTP 压缩 + ETag | 无 | ≤ 1 个冲刺 |
| **P0.5** | D5 — 数据导出 + 使用情况仪表板 | 无（新端点，无现有逻辑变更） | 1-2 个冲刺 |
| **P0.5** | D5 — 账户删除级联 | 需要方向 A（事件总线）用于可审计的级联 | 2-3 个冲刺 |
| **P1** | D4 — 版本化 | 需要字段选择（D3）用于可选版本响应 | 2-3 个冲刺 |
| **P1** | D3 — 字段选择 | 无 | 2 个冲刺 |
| **P1** | D2 — 草稿/调度基本操作 | 无 | 2 个冲刺 |
| **P2** | D2 — 调度 worker + 撤回 | 需要方向 A（事件总线）用于撤回通知 | 2 个冲刺 |
| **P2** | D1 — 富媒体嵌入 | 需要字段选择（D3）用于高效嵌入序列化 | 2 个冲刺 |
| **P2** | D3 — 批量操作 | 需要字段选择（D3）用于部分批量响应 | 1 个冲刺 |

### 5.2 阶段划分

#### 阶段 0：基础优化（冲刺 1）
- **D3.gzip**：5 行生产代码，启动后立即节省带宽。
- **D3.ETag**：为 `GET /v1/files/:id`、`GET /v1/messages/:id` 等添加 ETag 支持。纯 handler 层变化，service 不变。
- **D5.Export**：`GET /v1/export` 端点，以压缩 JSON Lines 格式流式传输用户数据。新端点，无需更改现有代码。
- **D5.Usage**：`GET /v1/account/usage` 返回计数（`total_files`、`total_messages`、`total_comments`、`storage_bytes`）。可通过 COUNT 查询轻松实现。

**里程碑 M0：** 带宽减少 40-60%（估测），数据导出可操作。

#### 阶段 1：领域事件支撑（冲刺 2-3）
- **方向 A — 事件总线**：引入 `EventBus` 接口，将 `Emit/Subscribe` 模式添加到 `app.go` 中。迁移现有的 3 个 outbox 事件（`message.sent`、`message.read`、`comment.replied`）通过事件总线路由，而非直接写入 `outbox.Repository`。

**里程碑 M1：** 跨实体编排具有显式合约。审计不再是 fire-and-forget。

#### 阶段 2：用户可移植性与版本化（冲刺 3-5）
- **D5.AccountDelete**：在事件总线上构建。账户删除发出 `account.deleted` 事件 → File/Message/Comment 处理程序级联删除 → GC 收集。
- **D4.Versioning**：为消息和评论引入 `*_versions` 表。通过 `GET /v1/messages/:id?version=N` 和 `GET /v1/messages/:id/versions` 进行可选读取。
- **D3.FieldSelector**：在列表端点上引入 `?fields=`，从 Files 开始。

**里程碑 M2：** GDPR 自服务门户可操作。编辑具有审计追踪。

#### 阶段 3：调度与富媒体（冲刺 5-7）
- **D2.Draft/Schedule**：消息状态机（`draft → scheduled → sent`）。调度 worker（outbox 轮询）。
- **D2.Recall**：在方向 A（事件总线）以上构建撤回通知。
- **D1.Embed**：富媒体解析 + `storage_refs` 集成。

**里程碑 M3：** 面向用户的即时通讯能力相当于典型平台。富媒体内联预览。

### 5.3 风险与缓解

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| **事件总线引入 service 间循环依赖** | 低 | 高 | 在 `app.go`（组合根）中注册订阅者，而非在 `NewXService()` 构造函数中。保持 `EventHandler` 是仅导入接口的闭包。 |
| **版本化迁移将写延迟加倍** | 中 | 中 | 版本插入发生在与内容更新相同的事务中——不会发生应用级双重延迟。PG 索引追加是顺序的。监控 `append` 延迟。 |
| **账户删除级联使表级锁（dead tuple bloat）** | 中 | 中 | 大规模删除（例如，有 10 万条消息的账户）应以批次进行，而非单一大事务。批次大小可配置。 |
| **撤回与现有 `SoftDelete` 冲突且 `Delete` 合并为撤回语义** | 中 | 高 | 通过 API 映射严格分离：`DELETE /v1/messages/:id` 保持为软删除（发送方隐藏）。`POST /v1/messages/:id/recall` 是撤回。文档加粗。 |
| **调度 worker 竞态（两个 worker 发送同一条消息）** | 低 | 中 | `UPDATE messages SET status = 'sent' WHERE id = ? AND status = 'scheduled' RETURNING *` — 行锁使竞态原子化。 |

### 5.4 管理建议

**在阶段 0 之后停止并测量。**

在添加调度、版本化或富媒体之前——测量当前 ETag/gzip/字段选择减少的带宽节省。导出端点的负载测试。性能数据应驱动阶段 1-3 的优先级，而非相反的假设。

**工程约束持续适用。** 在添加字段选择器、版本化表或事件总线时，定期运行 `make check-file-sizes && make lint && make test`。`app.go` 正在接近 200 行——由事件总线产生的额外事件订阅注册将在其接近 400 行之前触发分割（也许是将组件注册提取到 `app/registrations` 包中）。

**在阶段 2 之后考虑 `internal/service/mocks/` 维护。** 当前领域有 3 个仓库接口 × 每个 service。在方向 A 事件总线之后，事件处理程序需要模拟。如果在事件总线上进行测试变得复杂，考虑采用像 `github.com/stretchr/testify/mock` 这样的 mock 库——但保持其使用范围以维持轻量级。

---

## 总结

aero-id 代码库是一个执行良好、架构干净的 Go 服务的罕见示例。其横向分层、选项模式和 outbox 基础架构为其下一阶段做好了准备。扩展的最大风险不是代码质量，而是**增量复杂性**——调度、版本化、事件总线、撤回和富媒体各自顺滑，但共同引入的认知负载需要纪律维持。

保持组合根（`app.go`）整洁。保持领域纯净。保持事件显式。系统将随需求良好增长。
