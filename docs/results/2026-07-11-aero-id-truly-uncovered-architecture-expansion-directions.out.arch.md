现在我已充分理解了当前架构和文档内容，以下是我的全面架构分析。

---

# 架构评估与技术设计分析

## 1. 架构评估

### 当前架构的优势

文档准确地指出当前代码库已具备扎实的生产级基础。从架构角度看，值得注意的优势包括：

**分层清晰且严格一致**。`handler → service → repository → Postgres(GORM)` 的单向依赖链、双传输（HTTP/gRPC）共享同一 service 层、`sso` 作为独立边界，这些决策在横向扩展时提供了强大的基础。`internal/app/app.go` 中的组合根（composition root）实现清晰地管理了所有后台协程的启动/关闭生命周期，是 Golang 服务中值得学习的设计模式。

**Outbox 模式是关键的架构差异优势**。消息、评论、通知中的事务性 outbox 使用意味着写路径上的领域事件不会丢失——这对任何后续的事件驱动扩展（方向五的订阅系统、方向三的活动 feed、方向二的级联清算）都是坚实的前提。如果 outbox 不存在，所有五个方向的复杂度都会加倍。

**存储 GC 设计是一个优秀的可扩展模式**。`storage/gc/sweeper.go` 通过接口抽象（`FileRepo`、`RefCounter`、`Lister`）实现了可测试的、可选启用的后台清洗逻辑。这种模式清晰地划分为"被清理实体不知道清洗器存在"的干净边界。

**限流、断路器、陈旧缓存容错、mTLS 等运维可靠性特征**表明架构团队从一开始就考虑了生产环境运营——这一点被文档证实。

### 架构局限性与技术债

**1. `(from_uid, to_uid)` 隐式路由 —— 最核心的领域模型债务**

文档在方向一中定位了这一点，但其影响比显式的群聊缺失更深：

- **查询复杂度不可扩展**：`ListConversation(ctx, uidA, uidB)` 是一个没有中间抽象层的笛卡尔积查询。任何需要 N 方参与的查询（如"列出我参与的所有活跃会话"）都需要 `O(N²)` 的 `WHERE (from_uid = X AND to_uid = Y) OR (from_uid = Y AND to_uid = X)` 模式。
- **gRPC 的 IDL 映射**：proto 的 `MessageService` 只有 1:1 方法，proto API 的演进将需要新的 service 定义（`ConversationService`），这与当前 IDL 风格一致但需要关注。
- **无意的 from_uid/to_uid 语义复用**：`ListInbox(ctx, toUID)` 隐含一个"收件箱"概念，即单用户视角。如果将来引入"频道"消息（用户主动订阅），这个查询语义会被打破。

**2. 通知分发器的硬编码 Switch 语句 —— 服务代码中的架构违反**

`internal/notification/dispatcher.go` 中的 `switch e.EventType { case "message.sent": … }` 是文档方向五识别的"事件类型与分发逻辑耦合"：

- 添加新的通知类型需要修改分发器的 switch 语句，违反了**开闭原则**（对扩展开放，对修改关闭）。
- 测试新的通知路径需要集成整个通知栈 —— 无法通过简单的"注册一个 handler"来实现。
- 这个模式已经出现了两次（outbox 事件 → 通知，outbox 事件 → 更早的某种转换？），说明这是一个重复的架构模式。

**3. 跨实体的数据生命周期不对称 —— 文件有 GC，消息/评论没有**

这是文档方向二的洞见。深层架构含义如下：

- `service` 层的 `Delete()` 只执行软删除，但"什么触发硬删除"没有统一机制。文件通过 `storage/gc/sweeper.go` 处理，消息/评论则没有。这表明**GC 架构不是跨实体的抽象**，而是绑定在 `storage` 包下的特定实现。
- `app.go` 中的 `stopGC` 函数只处理文件——其他后台协程（`stopNotifClean`、`stopOutboxClean`）处理不同的职责，但没有统一的数据清理管理器。
- 迁移 `000025_data_export_jobs.up.sql` 的存在表明数据导出已被认为是需要的，但其范围仅限于导出请求，没有覆盖**级联账户删除**或**基于保留策略的清理**。

**4. 通知系统的实体边界模糊**

`Notifications` 表在所有 schema 中最为简单：

```
notifications(id, recipient_uid, type, actor_uid, subject_type, subject_id, read_at, created_at)
```

没有 `scope_uid`、`scope_resources` 或任何方式的按"主题"分组（如"针对文件 F-123 的所有通知"）。`subject_type`/`subject_id` 是"软"外键（无 FK 约束），因此跨实体的查询（"此消息上所有通知"）都基于字符串模式匹配，而非关联。

---

## 2. 扩展方向

我通过评估**业务价值 × 技术可行性 × 对现有架构的影响**，筛选了五个方向。以下三个方向（存在一些重叠）代表了最具变革意义的机会：

### 方向 A（P0）：数据生命周期治理 —— 将 GC 从垂直扩展到水平抽象

**当前状态**：文件 GC 是一个良好封装的子包（`storage/gc`），绑定到 `storage.FileStorage` 接口。消息/评论 GC 不存在。数据导出存在迁移但未证明已完全集成。账户级联删除不存在。

**核心挑战与设计决策**：

*挑战 1：GC 的抽象归属*。当前模式将 sweeper 寄宿在 `storage/` package 下，但其清理范围超出了存储。建议的架构变更：创建一个新的 `internal/lifecycle/` 包，包含：

```
internal/lifecycle/
├── sweeper.go          # 通用调度器（替代 app.go 中多个 stop* 函数）
├── policy.go           # 保留策略配置/存储
├── gc/
│   ├── message.go      # 消息 GC（实现了 Sweepable 接口）
│   ├── comment.go      # 评论 GC
│   └── notification.go # 陈旧通知清理
├── export/
│   └── job.go          # 数据导出管理器
├── account/
│   └── cascade.go      # 账户注销级联处理器
└── archive/
    └── cooler.go        # 冷存储归档
```

关键词接口：

```go
type Sweepable interface {
    ListExpired(ctx context.Context, before time.Time, limit int) ([]domain.ID, error)
    HardDelete(ctx context.Context, ids []domain.ID) error
    Name() string // 用于日志/指标
}
```

*挑战 2：保留策略的存储位置*。选项 A：配置驱动（config.yaml 中的静态值）——简单但缺乏运行时变更能力。选项 B：数据库 `retention_policies` 表——更灵活，允许按实体类型/按租户覆盖。推荐混合方式：配置提供默认值，数据库允许特定覆盖。

*挑战 3：级联账户删除的事务边界*。当 SSO 发送"账户 U-xxx 已注销"回滚时，aero-id 需要执行跨多个 repository 的级联操作。当前没有跨实体的 saga 或补偿机制。推荐使用 outbox 事件：`account.delete_requested` → dispacher 执行级联并记录每个实体的结果。

**对现有系统的影响**：
- `app.go`：将 `stopGC`、`stopNotifClean`、`stopOutboxClean`、`stopIdemClean` 合并为单个 `lifecycle.Start(ctx, interval)`。
- 消息/评论 repository：添加 `ListSoftDeletedBefore` 和 `HardDelete`（文件已有）。
- `dispatcher.go`：添加 `account.deleted` 事件处理器。
- 数据导出：现有迁移 `000025` 定义了一张表——需要 service + handler 来完成流程。

---

### 方向 B（P1）：消息路由的 `conversation_id` 抽象 —— 非破坏性的路线变更

**当前状态**：消息由 `(from_uid, to_uid)` 对路由。方向一描述了群聊需求，但架构问题比群聊本身更深：**没有会话抽象意味着没有权限层、没有元数据层、没有状态层**。

**核心挑战与设计决策**：

*挑战 1：迁移策略 —— 不要破坏现有客户端*。这是整个代码库中影响最大的 schema 变更。建议的**增量路径**：

```
阶段 1：添加 conversation 基础设施（迁移 N + 无服务变更）
  - CREATE TABLE conversations (id UUID PRIMARY KEY, type TEXT, title TEXT, created_by TEXT, created_at)
  - CREATE TABLE conversation_members (conversation_id UUID, uid TEXT, role TEXT, joined_at, PRIMARY KEY(conversation_id, uid))
  - 创建后台 job 为现有每一对 (from_uid, to_uid) 生成隐式 direct conversation。
    （注意：这是 O(n²) 操作——需要离线/后台 job，而非在线迁移。）

阶段 2：消息引擎双写（迁移 N+1）
  - messages 表添加 conversation_id TEXT（可为 NULL，不带 FK）
  - Send() 路径：既有 `to_uid` 又有 `conversation_id` 写入
  - ListConversation() 回退：有 conversation_id 时优先使用，否则 fallback 到旧逻辑

阶段 3：读取路径切换
  - 所有 ListConversation / ListInbox 查询优先使用 conversation_id
  - 前端/gRPC 客户端可选迁移到 new API

阶段 4：弃用 to_uid（至少三个月后）
  - 删除 to_uid 列，强制使用 conversation_id
```

*挑战 2：API 兼容性*。Proto 定义需要新增 `ConversationService`，但现有 `MessageService` 的 RPC 应继续为 direct conversations 工作。建议：

```protobuf
service ConversationService {
    rpc CreateGroup(CreateGroupRequest) returns (Conversation);
    rpc AddMember(AddMemberRequest) returns (google.protobuf.Empty);
    rpc RemoveMember(RemoveMemberRequest) returns (google.protobuf.Empty);
    rpc ListMyConversations(ListConversationsRequest) returns (ListConversationsResponse);
    rpc GetConversation(GetConversationRequest) returns (Conversation);
}
service MessageService {
    // 现有 1:1 RPC 保持不变，但内部实现委托给 conversations。
    // 新增：
    rpc SendToConversation(SendToConversationRequest) returns (Message);
}
```

*挑战 3：权限模型*。群聊需要成员级角色：owner、admin、member、read-only。当前 block 检查（`checkSendBlock(ctx, in.ToUID, actor)`）是方向性的两方操作。群聊需要更细粒度的权限：`CanSendToConversation(ctx, actor, conversationID)`。

**对现有系统的影响**：
- 领域：新增 `Conversation`、`ConversationMember` 实体。
- repository：新增 `conversation.go` + `conversation_member.go`。
- service：现有 `messageService` 的逻辑需要拆分——`Send()` 将根据 `conversation_id` 是否存在路由到不同的验证/权限逻辑。
- handler（HTTP + gRPC）：新增路由/RPC。
- 通知：分发器需要新增 `group.message`、`user.added_to_group`、`member.removed` 事件。
- 迁移：至少 4 个新迁移文件。

---

### 方向 C（P1-P2）：Presence + Activity Feed —— 以 SSE Hub 作为信号基础设施

**当前状态**：SSE Hub (`internal/hub/hub.go`) 已实现成熟的连接生命周期管理（订阅/取消订阅/断连/关闭）。方向三恰当地指出，"在线"信号是"连接存在"的副产品——但需要架构上承认这一语义。

**核心挑战与设计决策**：

*挑战 1：Presence 的持久化边界*。

选项 | 优点 | 缺点 | 推荐
---|---|---|---
仅内存（sync.Map + goroutine） | 极低延迟，无数据库开销 | 服务重启时丢失状态；跨实例问题（需要 Redis Pub/Sub 或类似方案） | ❌ 多实例时不足
Redis | 天然支持 TTL/过期、Pub/Sub | 新增依赖；运维开销 | ✅ 如果团队已有 Redis
Postgres 心跳表 | 无新增依赖 | 写放大；查询延迟；不适用于高吞吐心跳 | ❌ 仅作为审计追踪

推荐：**Redis**（如果团队愿意新增依赖），否则选**进程内 map + Postgres 冷却写入**（每 30 秒将联机时间戳写入 `user_last_activity` 表）。

*挑战 2：输入指示器的通信通道*。SSE 原生不支持 client→server 消息。选项：

选项 A：**独立 WebSocket 端点**。通过 `gorilla/websocket` 新增 `/v1/ws` 端点，用于 typing 事件和未来的双向实时消息。架构影响最大但最灵活。

选项 B：**HTTP POST + 速率限制**。`POST /v1/typing` → rate-limited → SSE 广播。实现最简单，但延迟比 WS 高。

选项 C：**WebTransport over HTTP/3**。过于未来主义，不推荐。

推荐：**选项 B 用于 MVP，选项 A 作为方向一的必要基础设施**（群聊强烈需要双向实时）。

*挑战 3：Activity Feed 的查询复杂度*。当前 outbox 事件已经包含所有需要的数据（`message.sent`、`file.uploaded`、`comment.created`），但将这些事件聚合成"feed"将需要：

- 一个 `activity_events` 表（作为 outbox 事件的物化视图）
- 按 `(subscriber_uid, created_at DESC)` 查询
- 每个用户的订阅列表（关注哪些用户/实体）

这实际上与方向五的订阅系统共享基础设施。

**对现有系统的影响**：
- `hub.go`：新增 `PresenceHub`（或扩展现有 Hub）以跟踪连接状态和广播 presence 事件。
- 新包 `internal/presence/`：处理在线/离线状态、心跳、优雅的离线窗口。
- `internal/handler/http/events.go`：presence 事件新增 `presence.online`/`presence.offline` 事件类型。
- `middleware/auth.go`：可选记录 `last_activity_at`（对性能几乎零开销）。
- 测试：presence 的模拟连接生命周期——可通过 `hub.Pusher` 接口模拟。

---

## 3. 接口设计建议

### 3.1 关键原则

**原则 1：每个跨实体的新抽象应定义接口，而非继承**。当前代码库通过接口实现了优秀的解耦（`FileStorage`、`Pusher`、`BlockChecker`）。方向的演进应延续同一模式。例如，`LifecycleManager` 不应是新的基类，而应是一个包含 `Sweepable` 注册的调度器。

**原则 2：领域模型应追赶物理模型**。当前 `Message.ToUID string` 是一个物理列名，暴露为领域属性。引入 `conversation_id` 后，`Message` 领域对象应引用 `ConversationID` 而非 `ToUID`：

```go
type Message struct {
    ID             ID
    FromUID        string
    ConversationID ID       // 新
    Kind           MessageKind
    Content        string
    // ToUID 阶段性地移除
}
```

**原则 3：配置优先，覆盖性存储在服务启动后实现**。保留策略、通知路由规则等应首先可从 `config.yaml` 配置，然后逐步支持数据库覆盖。

**原则 4：每个新的后台进程应通过 `lifecycle.Manager`（而非 `app.go` 中的独立字段）启动**。

### 3.2 需要的新接口

**接口：`Sweepable`**（已在上文定义）——为 GC 创建统一抽象层。

**接口：`LifecycleManager`**——替代 `app.go` 中分散的 `stop*` 函数：

```go
type LifecycleManager struct { /* … */ }
func NewLifecycleManager(log *zap.Logger) *LifecycleManager
func (m *LifecycleManager) Register(name string, interval time.Duration, job JobFunc) (stop func())
func (m *LifecycleManager) Shutdown(ctx context.Context) error
```

**接口：`ConversationAuthorizer`**——抽象群聊中的权限检查，使 `messageService` 不直接依赖 `conversation` 领域：

```go
type ConversationAuthorizer interface {
    CanSend(ctx context.Context, actor string, convID domain.ID) error
    IsMember(ctx context.Context, uid string, convID domain.ID) (bool, error)
    GetRole(ctx context.Context, uid string, convID domain.ID) (string, error)
}
```

**接口：`ActivitySource`**——用于活动 feed 的事件类型过滤器（从 outbox 事件中筛选出"值得 feed"的事件）：

```go
type ActivitySource interface {
    IsFeedEvent(eventType string) bool
    Enrich(ctx context.Context, evt *outbox.Event) (*ActivityEvent, error)
}
```

### 3.3 向后兼容性策略

- **所有新 API 均使用版本化路由**（如 `/v2/conversations`、`/v2/events` 支持 presence）。现有 `/v1/messages` 保持不变。
- **gRPC proto**：新增服务到 `aero_id.v2` package（或当前 package 内的新 service）。客户端可以平滑过渡。
- **数据库 schema**：新列可为空（`conversation_id TEXT`），旧代码忽略它。旧实体不受影响。
- **配置**：新的配置键以 `AERO_ID_LIFECYCLE_*`、`AERO_ID_PRESENCE_*`、`AERO_ID_CONVERSATION_*` 为前缀，遵循现有的 `AERO_ID_*` 约定。

---

## 4. 技术选型

### 4.1 需要引入的新技术栈

| 方向 | 推荐技术 | 理由 | 替代方案 | 评估标准 |
|------|----------|-------|----------|----------|
| Presence（高吞吐心跳） | **Redis** | TTL 管理、Pub/Sub、跨实例 | 仅 Postgres | 选择 Redis 仅当团队确定要处理 5 万+ 并发 SSE 连接；否则进程内 map 已足够 |
| 数据导出（归档生成） | **标准库 `archive/zip`** + **Postgres COPY** | 无新增依赖；COPY 可高效导出大型数据集 | 流式 JSONL | Zip 是通用的用户期望格式；COPY 避免在应用层缓冲所有数据 |
| 通知渠道（SMS/邮件） | **无新增依赖**（抽象为 `Channel` 接口） | SMTP（通过 net/smtp 或 sendmail 实现 MVP） | Twilio/SendGrid SDK | 数据平面中立；用户配置自己的 SMTP 或 webhook，而非依赖平台 |
| 跨架构事件流（大方向五） | **现有 outbox** | 已存在，事务上可靠 | 新增 Kafka/NATS | 在 outbox 被证明因吞吐量不足前不增加消息系统 |

**总体建议**：**严格限制新基础设施依赖**。目前三个方向中，方向 B（conversations）无需新的基础设施（纯 schema + 新领域），方向 A（lifecycle）也不需要。方向 C 在 Redis 和进程内方案之间可权衡。方向五可能最终需要轻量级事件总线，但可以先将路由规则实现为 Postgres 驱动。

### 4.2 自建 vs 采购决策

| 场景 | 自建理由 | 采购/集成理由 |
|------|----------|--------------|
| 消息中的协作空间（方向一） | 核心差异化特性；对平台业务逻辑至关重要 | 外部协作 SDK（如 Layer）会引入数据驻留和控制权问题——**不推荐** |
| 数据导出/GDPR（方向二） | 法律合规需求；产品中核心体验；aero-id 拥有所有数据 | 数据主体请求DSR工具（如 OneTrust）不一定集成到当前架构——推荐**自建** |
| Presence/在线状态（方向三） | 轻量级；无需第三方连接 | 如果已有 Redis，可以利用其 Pub/Sub——**混合** |
| 通知路由（方向五） | 高度产品特定；需要与现有偏好模型集成 | Email/SMS 渠道应分段外包给 SendGrid/Twilio——**集成**用于下游，**自建**用于路由逻辑 |

### 4.3 关于 "Go 1.26 + module github.com/aero/aero-id" 的说明

当前 pinned Go 版本为 1.26。方向一（群聊）和方向五（通知路由）分别在领域模型和分发器架构方面引入了复杂性。在 Go 1.26 中，`errors.Join`、`sync.Map Range` 语义等特性均有良好支持。不需要升级 Go 版本或引入泛型来驱动这些方向。

---

## 5. 实施路线图

### 优先级排序（修正版）

我基本同意文档提出的优先级，但有以下关键区别：

| 优先级 | 方向 | 我的评级 | 理由 |
|--------|------|----------|------|
| **P0** | 方向二（数据生命周期管理） | **✅ 强同意** | 合规要求，数据完整性，对查询性能的累积影响。最大"技术债 → 可观测收益"。没有新的领域实体，影响可预测且可安全回滚。 |
| **P0（第一阶段）** | 方向一（conversation_id 抽象）的**基础设施层** | **🆕 阶段化** | 我不同意将群聊视为单一的大方向。Conversation ID 抽象是删除 `to_uid` 列的必要基础设施模块。**单独部署**conversation 表+迁移 job（无新 API）= 低风险。群聊 UI/RPC 是 P1。 |
| **P1** | 方向三（Presence） | **🔄 降级** | 高用户可见度，但"对方是否在线"的体验差异在 1:1 场景中较小。Presence 在群聊（方向一）中更有价值。将 Presence 的架构设计推迟到方向一发布后。 |
| **P1** | 方向四（策略化文件访问控制） | **✅ 同意** | 分享链接+过期时间是企业/合规需求。可以在不影响现有 Share API 的情况下完成。 |
| **P2** | 方向三（完整 Activity Feed） | **🔄 推迟** | 活动 feed 与方向五（订阅）共享基础设施。在订阅系统设计完成之前构建它们是重复工作。 |
| **P2** | 方向五（统一通知路由） | **✅ 同意** | 最大的架构影响——重构分发器、添加订阅表、集成外部频道。在核心消息/文件 API 稳定之前不要做。 |

### 阶段划分

**阶段 0：基础设施模块（4-6 周，独立于所有产品路线图）**

| 里程碑 | 关键交付物 | 风险 |
|--------|-----------|------|
| M0.1 | `lifecycle.Manager` 通用抽象，合并 `app.go` 中 7 个 `stop*` 函数 | 低——函数式迁移，无需 schema 变更 |
| M0.2 | `Sweepable` 接口 + 消息/评论 GC 实现 | 低——注意 PurgeBatch 大小以避免锁争用 |
| M0.3 | Conversation 表 + 后台迁移 job（为现有对创建隐式 direct conversations） | 中——迁移 job 的 O(n²) 复杂度。大表需要离线或限速处理 |

*风险缓解*：阶段 M0.3 的迁移 job 需要一个 backfill 速率限制：每秒处理 X 对，并检查 `conversations` 表中是否存在现有记录。如果表很大（数亿条消息），可能需要并发 worker 和进度表。

**阶段 1：核心产品扩展（8-12 周）**

| 里程碑 | 关键交付物 | 风险 |
|--------|-----------|------|
| M1.1 | 数据生命周期 API：保留策略配置、账户注销 webhook、数据导出端点完成 | 中——文件 GC 已存在，消息/评论 GC 遵循相同模式 |
| M1.2 | 文件分享增强：`expires_at`、`permission` 列、分享链接端点 | 低——schema 扩展 + API |
| M1.3 | Conversation API：`CreateGroup`、`AddMember`、`RemoveMember`、`ListMyConversations` | 中——新权限模型。Block 检查需要 conversation-aware 版本 |

**阶段 2：体验扩展（8-12 周）**

| 里程碑 | 关键交付物 | 风险 |
|--------|-----------|------|
| M2.1 | Group messaging：`SendToConversation`、消息在群组中的可见性、通知扩展 | 高——群聊消息的实时分发延迟，SSE 在组中的负载表现 |
| M2.2 | Presence（MVP）：基于 SSE 连接生命周期的在线/离线状态，`GET /v1/presence/:uid` | 中——多实例一致性 |
| M2.3 | 输入指示器：`POST /v1/typing` → SSE `typing.start/stop` | 低——HTTP POST + 速率限制 |

**阶段 3：高级架构（12-16 周，取决于业务优先级）**

| 里程碑 | 关键交付物 | 风险 |
|--------|-----------|------|
| M3.1 | 订阅系统 + 通知路由引擎 | 高——重构分发器，新增 `user_subscriptions` 表，添加电子邮件/SMS 渠道，保持向后兼容 |
| M3.2 | Activity Feed | 中——基于订阅系统的事件聚合；避免双写 (notification + feed) |

### 风险矩阵

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|--------|------|----------|
| Conversation 迁移 job 超时 | 中 | 高 | 限速批量作业；可为回填进程设置 `progress_log` 表；允许暂停/恢复 |
| presence 出现脑裂（多实例） | 中 | 中 | 使用 Redis 集中式 TTL；如果 Redis 不可用则优雅降级，仅返回"上次活动时间" |
| 群聊通知涌现（用户被添加到群组时收到多条通知） | 高 | 中 | 为 `group.member_added` 实现通知折叠（现有 `CollapsedCount` 模式）；限制 SSE 推送速率 |
| 合规要求生命周期的保留策略违反业务预期 | 低 | 高（法律） | 保留策略默认为"永久"（无删除）——仅限使用可选 `retention_policies` 表 opt-in |
| Outbox 事件类型增长失控（订阅系统增加 10× 事件类型） | 中 | 中 | 建立事件类型注册表（`.proto` 或 Go 常量）；验证所有注册的事件类型在订阅表中都有对应映射 |

---

## 关键架构决策总结

| 决策 | 选项 | 推荐 |
|------|------|------|
| Conversation migration 策略 | 一次性 vs 阶段式 | **阶段式**（4 阶段：基础设施→双写→切换→弃用）。零停机时间。 |
| Presence 存储 | Redis vs 进程内 | **进程内先，Redis 后**。仅在多实例部署需要时引入 Redis。 |
| 通知架构 | 保留现有的 switch dispatcher vs 替换为订阅引擎 | **保留现有 dispatcher，在其并行路径上构建订阅系统**。方向五将新事件路由到新引擎，旧事件保持现有路径——零回归风险。 |
| 数据导出格式 | Zip vs JSONL vs CSV | **Zip 包含 JSON + 原始文件**。Zip 是平台无关的标准打包格式。 |
| 生命周期管理器 | `app.go` 中的 `stop*` 字段 vs 单个 `LifecycleManager` | **单个 `LifecycleManager`**。降低 `app.go` 的复杂性和行数（当前 150+ 行）。 |

---

## 结论

这篇分析文档出色地完成了其核心目标：识别了当前架构能力边界之外但有高价值的方向。我将其实质性的五个方向细化为三个具体的架构项目（数据生命周期治理、Conversation ID 抽象、Presence 基础设施），并阶段化实施以管理风险。

对于架构团队的具体建议：

1. **立即开始阶段 0**（`lifecycle.Manager` + 消息/评论 GC & Conversation 基础设施）。这些是独立模块，可以在不干扰现有开发流程的情况下部署。
2. **进行"Conversion ID 迁移"设计审查**，特别关注回填性能。这决定了方向一的时间线。
3. **延迟 Presence 和 Activity Feed**，直到方向一的群聊 API 稳定。它们的实时感在群聊中比在 1:1 场景中价值更高。
4. **不建议为新基础设施（Redis）做超过严格需要的投资**。方向二、方向四和方向一（第一阶段）对基础设施的影响为零。方向三在需要时再评估。
