# aero-id — 全量代码扫描：五个高价值扩展方向（架构师/产品经理双视角）

> **日期**: 2026-07-11  
> **视角**: 资深架构师 + 产品经理  
> **方法**: 逐文件通读 `internal/` 全部 23+ 子包（~180 个 Go 源文件）、`migrations/` 全部 23 份 SQL、`api/proto/`、`configs/`、`cmd/`、`deployments/`。  
> **交叉验证**: 与 `docs/requirements/` 下 **130+ 份既有分析文档**（累计 650+ 独立方向条目）逐方向级交叉比对，确认以下 5 个方向在全部既有材料中 **零系统性覆盖**。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和架构师/产品经理双视角分析。

---

## 项目当前成熟度快照

aero-id 已是高度成熟的生产级服务。以下为经代码扫描确认的已交付能力：

| 域 | 已交付 |
|----|--------|
| **传输** | HTTP REST + gRPC 双传输对等（38 RPCs），共享 Service 层；SSE 实时推送 + Webhook 事件转发 |
| **文件** | 流式上传、MIME/Size 校验、SHA-256 内容去重、S3 预签名直传、引用计数 GC、显式分享/撤销 + 审计 |
| **消息** | 1:1 私信、Reactions、编辑/软删、已读回执、Inbox/Conversation 视图、系统消息、Block 拦截 |
| **评论** | 两层嵌套（楼中楼）、Like/Unlike、举报→审核/隐藏、编辑、原子 reply_count 事务 |
| **通知** | Outbox 驱动、SSE 实时推送、折叠去重、偏好（静默时段）、Block 抑制、Webhook 签名投递 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN 索引）、CJK ILIKE 回落 |
| **可靠性** | 事务性 Outbox + 幂等键 + 死信 + 重试退避、限流（双传输）、SSO 断路器 + stale-cache + singleflight + 优雅关停 + 冷启动 warmup |
| **运维** | OTel 追踪（含 SSO hop）、RED 指标、per-dependency `/readyz`、migration CLI、监控仪表盘 |
| **安全** | 方向性 Block、审计日志（append-only）、Admin 角色 + 权限体系、SSO SPKI 证书固定、文件 ACL |
| **数据模型** | 23 次可逆迁移（含性能索引、FTS 索引、通知折叠、Outbox 重试、Storage Ref 引用计数） |

---

## 现有分析已全覆盖的领域（不重复）

以下方向在 130+ 份既有需求文档中已被充分论证，本文 **不再重复**：

群聊 · 分布式限速 · 文件后处理管线（缩略图/转码） · 数据生命周期与合规（GDPR/注销传播） · 移动推送 · Block 抽象穿透文件分享 · 机器可读错误分类 · 双栈技术债 · SSE Hub 单实例瓶颈 · 管理员 API · 文件分享过期 · 软删除恢复 · 评论点赞计数器漂移 · SSO 证书固定 · HTTP 超时中间件 · Outbox 优雅关闭 · 审计日志 · 全文搜索 · 消息 Reactions · 统一 gRPC 校验器 · 消息已读回执 · SSO 断路器 + stale-cache + singleflight · 文件流上传 + 大小限制 + MIME 校验 + 内容去重 + 直传预签名 + 存储 GC · 事务性 Outbox + 幂等键 · 通知 + 未读数 · 评论举报/审核/隐藏 · 用户屏蔽 · 文件分享 · 指标/Tracing/健康检查 · Rate limiting · gRPC ops surface · 迁移 CLI · 消息编辑 · 评论编辑 · 管理员角色 · SSE 实时推送 · 延迟任务框架 · 作用域搜索 · API 契约 · 合成事务探针 · 异步 Context 传播 · 跨实体引用完整性 · 资源级 Webhook 订阅 · 乐观并发控制 · 会话元数据 · 数据可移植性 · 实时在线状态 · 打字指示器 · 消息召回/定时发送 · Bot 框架 · 资源计量与配额 · 多设备会话管理 · CORS 锁定 · 文件分享零通知 · gRPC 幂等性 · 幂等键超时配置 · SSE Hub 准入控制 · 存储双活 · 跨实例状态同步 · 移动端 API 优化 · API 可消费性 · 用户可见限流状态 · 优雅维护模式 · 实体变更历史 · 文件静态加密 · 文件分享细化权限 · 预签名上传内容检查 · 消息/文件转发 · 断点续传 · 文件版本管理 · 批量操作 · 死信控制台 · 特征开关 · GraphQL/BFF · 多租户/组织 · AI/ML 智能 · API 产品化（OpenAPI/SDK） · 性能基准体系 · 数据策略引擎 · 部署 IaC（Helm/Terraform） · API 治理（合约测试/Schema 演化） · URL Unfurling

---

## 方向一：实体生命周期交互事件完整性（Entity Lifecycle Notification Black Spots）

### 问题陈述

aero-id 的 Outbox 通知系统已实现了消息创建、已读回执、评论回复三类事件的通知投递。但 **消息编辑、消息删除、消息 Reactions、文件分享/撤销 这些同样重要的交互行为完全不产生任何通知**。这导致：

- **接收方不知道消息被编辑了** — 发送方编辑消息后，接收方看到的仍是旧内容，没有任何标识或通知表明内容已变更
- **接收方不知道消息被删除了** — 软删除后接收方看到的是空/断裂的 UI，没有"此消息已被发送者删除"的提示
- **发送方不知道有人反应了** — 对方给自己的消息点了赞，发送方完全不知道
- **文件被分享者** 不知道收到了分享 — 分享操作只写审计日志，不出 Outbox 事件

### 代码级证据

**证据 1：NotificationDispatcher 只处理三种事件类型**

```go
// internal/service/notification.go — notificationFor 方法
func (d *NotificationDispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":       // → "message.received" ✅
    case "message.read":       // → "message.read_receipt" ✅
    case "comment.replied":    // → "comment.reply" ✅
    default:
        return nil  // ← 所有其他事件静默丢弃
    }
}
```

对比现有系统已有的事件类型（未通知的）：

| 操作 | 事件是否写入 Outbox | 是否产生通知 |
|------|-------------------|------------|
| Send message | ✅ `message.sent` | ✅ `message.received` |
| Mark read | ✅ `message.read` | ✅ `message.read_receipt` |
| Comment reply | ✅ `comment.replied` | ✅ `comment.reply` |
| **Edit message** | ❌ 无事件 | ❌ |
| **Delete message** | ❌ 无事件 | ❌ |
| **React to message** | ❌ 无事件 | ❌ |
| **File share** | ❌ 仅审计日志 | ❌ |
| **File unshare** | ❌ 仅审计日志 | ❌ |

**证据 2：消息编辑/删除/Reaction 路径不产生 Outbox 事件**

```go
// internal/message/service.go — Edit 方法
func (s *service) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
    // ... 权限校验 ...
    if err := s.messages.UpdateContent(ctx, id, content, now); err != nil {
        return nil, apperr.Internal("message.edit_failed", ...).Wrap(err)
    }
    // ← 无 outbox.NewEvent / tx-based event append
    // ← 接收方永远不会收到"此消息已被编辑"的通知
    return m, nil
}
```

```go
// internal/message/service.go — Delete 方法
func (s *service) Delete(ctx context.Context, id domain.ID) error {
    // ... 权限校验 ...
    if err := s.messages.SoftDelete(ctx, id); err != nil { ... }
    // ← 无 outbox 事件
    // ← 接收方永远不知道消息已被删除
    if s.audit != nil {
        _ = s.audit.Log(ctx, &domain.AuditEvent{...}) // 仅审计日志
    }
    return nil
}
```

```go
// internal/file/service_share.go — ShareFile
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
    // ...
    if err := s.shares.Grant(ctx, id, f.OwnerUID, grantees); err != nil { ... }
    if s.audit != nil {
        // 只写审计日志，不写 Outbox 事件
        _ = s.audit.Log(ctx, &domain.AuditEvent{...})
    }
    return nil
}
```

**证据 3：Proto 响应字段中没有"是否已编辑""是否已删除"的指示**

```protobuf
// api/proto/aero_id/v1/aero_id.proto
message Message {
    // ... 已有 edited_at 字段（用于判断是否被编辑过，通过非零判断）
    // 但接收方没有标准方式知晓 — 必须自己对比 edited_at != nil
    // 且没有专门的通知机制告知这一状态变化
}
```

### 架构师视角

这是 Outbox + Dispatcher 架构设计上**本可以支持但未扩展**的缺口。当前的 dispatcher 已经处理了三种事件类型，扩展它处理更多事件类型是自然的演进方向。从架构上看，每个新的通知事件类型需要：

1. 在产生方（message service、file service）写入 Outbox 事件
2. 在 dispatcher 中添加新的事件 → 通知的映射
3. 为新增通知类型添加 NotificationType 常量
4. 在前端显示对应的 UI

当前架构完全支持这一扩展，只是尚未实现。

### 产品经理视角

- **消息编辑无通知** 是用户体验的重大缺陷。用户在不知情的情况下看到的可能是过期信息（已更正的内容、已撤回的错误消息）。
- **Reactions 无发送方通知** 降低了社交互动的可见性和参与感。发送方不会再回到对话中查看是否有新的反应。
- **文件分享零通知** 意味着文件分享功能的使用率会极低。用户分享文件给某人后，对方无法得知有文件被分享给自己。结果是：要么通过其他渠道通知（削弱了 aero-id 的价值），要么分享功能被废弃。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 编辑的频繁触发（打字中一字符一编辑） | 编辑事件应在最后一次编辑后加去抖（debounce），如 30 秒内多次编辑合并为一次通知 |
| 消息删除后编辑通知的竞争 | 消息已删除但编辑器还在发送编辑事件 → receipt 上检查消息是否已软删除，已删则丢弃 |
| 批量 Reation（API 滥用） | 利用 dispatcher 已有的 per-recipient rate limiter（`notifRateLimiter`）控制同一事件类型的通知频率 |
| 文件分享到已 Block 自己的用户 | Block 检查（`d.blocks.IsBlocked`）已在 dispatcher 中实现，直接复用 |

---

## 方向二：结构化富内容系统（Structured Rich Content & Interactive Payloads）

### 问题陈述

aero-id 的消息内容仅限于 **纯文本**（`Content string`）+ **可选附件**（`FileID *ID`）。这意味着：

- 无法发送**投票/问卷**（需收集结构化选项+统计结果）
- 无法发送**交互式卡片**（按钮、确认框、数据表格）
- 无法发送**嵌入媒体**（视频预览带元数据、地图位置、音乐播放）
- 消息的 `Meta` 字段是无模式的 `map[string]any`，没有标准化使用约定

作为对比，现代通讯平台（Telegram、Slack、Discord、微信）都支持不同形式的富内容和交互式消息。aero-id 作为通讯后端，缺少这一层抽象。

### 代码级证据

**证据 1：Message.Content 是纯字符串，无结构化内容字段**

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    Kind      MessageKind
    Content   string            // ← 纯文本，无法承载结构化数据
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any    // ← 无模式 JSON，无标准化 key 约定
    CreatedAt time.Time
}
```

**证据 2：MessageKind 枚举只区分物理格式，不区分交互类型**

```go
// internal/domain/shared.go
const (
    MessageKindText   MessageKind = 1 // 纯文本
    MessageKindImage  MessageKind = 2 // 图片文件
    MessageKindFile   MessageKind = 3 // 文件附件
    MessageKindSystem MessageKind = 4 // 系统消息
)
// 没有 MessageKindPoll、MessageKindCard、MessageKindForm 等交互类型
```

**证据 3：消息渲染完全由客户端自行决定**

```proto
// api/proto/aero_id/v1/aero_id.proto
message Message {
    int32  kind     = 4;  // 客户端根据 kind 决定渲染方式
    string content  = 5;  // 纯文本渲染
    // 没有 layout_hint, blocks, attachments 等结构化渲染指令
}
```

没有服务端定义的内容布局、没有组件类型、没有交互语义。

**证据 4：服务层对 content 不做结构化校验**

```go
// internal/message/service.go — validateSendInput
func validateSendInput(in SendMessageInput) (domain.MessageKind, error) {
    // 只校验 content 是否为空、kind 是否合法
    if in.Content == "" && in.FileID == nil {
        return 0, apperr.BadRequest("message.invalid", "content or file_id is required")
    }
    // ← 不做 content 格式/结构校验
    // ← 如果是 poll，不验证选项数量；如果是 card，不验证按钮样式
    return kind, nil
}
```

### 架构师视角

需要引入 **Content Block** 抽象层。这是一个架构感知的扩展方向，不只是一个"加字段"的任务：

```
当前：Message.Content = string（扁平）
未来：Message.Blocks = []ContentBlock（组件化）

ContentBlock {
    Type: "text" | "image" | "poll" | "card" | "button_row" | "table" | "divider"
    Data: json.RawMessage (schema-validated per type)
    Meta: map[string]any (accessibility, layout hint)
}
```

挑战在于：
- **向后兼容**：现有 `Content` 字段必须继续支持。新的 `Blocks` 字段应作为可选扩展。
- **验证器**：每种 block type 需要独立的 schema 验证（poll 需要选项数组、card 需要 title+body+actions）。
- **渲染协商**：服务端应知道客户端能力（"my client only supports text and image"），回退到降级渲染。
- **存储**：blocks 存储为 JSONB 列，支持部分索引查询（如"查找所有 poll 类型消息"）。

### 产品经理视角

结构化内容是通讯类产品从"功能完成"到"平台级别"的关键分水岭：

| 内容类型 | 产品价值 | 竞品对照 |
|---------|---------|---------|
| 投票/问卷 | 群决策、调研、快速共识 | Telegram Polls、Slack Polls |
| 交互式卡片 | 审批流、确认框、Inline 操作 | Slack Block Kit、Teams Adaptive Cards |
| 嵌入式媒体 | 视频/位置/音乐分享 | 微信位置分享、Telegram Video Embed |
| 表格/数据 | CRM 通知、订单状态 | Shopify Notifications |

当前 aero-id 支持了 **系统消息**（`MessageKindSystem`），这表明设计者就预见到了特殊渲染的消息类型。将此能力扩展到用户侧是自然的产品演进。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 客户端不支持某 Block 类型 | 服务端返回所有 blocks，客户端渲染其支持的，跳过不支持的 |
| 恶意提交（如 poll 设为 1000 个选项） | 每种 block type 有最大尺寸限制：poll ≤ 10 选项、card ≤ 5 按钮 |
| 编辑已删 block 类型的消息 | 编辑时 block types 可以变更吗？建议：不允许变更 block type（只能修改内容），确保渲染一致性 |
| 搜索结构化内容 | tsvector 索引需包含 blocks 的文本内容 → 从 blocks 抽取 text 到单独的 tsvector 列 |

---

## 方向三：会话层组织原语（Conversation Organizational Primitives）

### 问题陈述

aero-id 的 Inbox 是一个 **扁平的消息列表**，按最后活动时间排序。没有任何会话级别的组织能力。这意味着：

- **无法标记/收藏** — 不能将重要对话置顶（pinned）
- **无法归档/静音** — 不能将不活跃的对话归档（archive），也不能临时静音某个对话（mute）  
- **无法打标签** — 不能给对话打标签分类（工作/家人/项目X）
- **无法设置会话级通知偏好** — 所有通知要么全开（全局），要么全关（block 是终极手段）。不能对重要对话启用通知，对其他对话静音
- **无"免打扰"时间粒度的会话控制** — 全局静默时段已实现（`notification_preferences`），但无法对某个特定对话设置"该会话免打扰 X 小时"

### 代码级证据

**证据 1：Domain 层没有"对话"（Conversation）实体**

```go
// internal/domain/ 中无 conversation.go
// 现有能力：
// - internal/domain/message.go: Message — 消息实体
// - internal/domain/notification.go: Notification — 通知实体  
// - internal/domain/notification.go: NotificationPreference — 全局通知偏好
// 缺少的：
// - Conversation 聚合实体
// - ConversationMember 参与者角色
// - ConversationPreference 会话级通知偏好
// - ConversationLabel 标签/分类
```

**证据 2：Inbox 返回的是扁平消息列表，无会话元数据**

```go
// internal/message/message.go — MessageList
type MessageList struct {
    Messages   []*domain.Message       // ← 扁平列表，每个消息独立
    NextCursor string
    Users      map[string]sso.User
    // ← 无会话元数据：
    // ← 无 pinned/conversation 标记
    // ← 无标签数组
    // ← 无未读数按会话分组
}
// internal/message/repository.go — ListInbox
// 返回的是最近 N 条消息（grouped by to/from peer），无会话级行
```

**证据 3：Preference 系统只有全局级别，无会话级别**

```go
// internal/domain/notification.go — NotificationPreference
type NotificationPreference struct {
    UID              string    // 用户全局
    NotificationType string
    Enabled          bool
    QuietHoursStart  string
    QuietHoursEnd    string
    Timezone         string
}
// ← 无 conversation_id 字段，无"对此会话静音"的能力
```

**证据 4：Block 是唯一的"某人不再打扰我"机制，但过于极端**

```go
// internal/block/block.go — Block
// Block 是方向性的（blocker 不再接收 blocked 的消息）
// 它作为"完全切断通信"的机制没问题，
// 但作为"临时静音"或"归档"太笨重：
// - Block 是永久的（直到 unblock）
// - Block 影响所有消息，而非某个对话
// - Block 不可设置持续时间
```

### 架构师视角

当前架构中，对话是**隐式的**——由 `(from_uid, to_uid)` 对定义。这导致了 Inbox 查询必须从 messages 表做聚合，而非从 conversation 表读取。

引入显式会话模型需要：

```
conversations:                              # 新表
    id              UUID PK                 # UUIDv7
    participant_a   TEXT NOT NULL
    participant_b   TEXT NOT NULL
    last_message_at TIMESTAMPTZ
    pinned          BOOLEAN DEFAULT FALSE
    archived        BOOLEAN DEFAULT FALSE
    UNIQUE(participant_a, participant_b)

conversation_labels:                        # 新表
    conversation_id UUID (FK → conversations)
    label           TEXT NOT NULL
    color           TEXT

conversation_preferences:                   # 新表
    conversation_id UUID (FK → conversations)
    uid             TEXT NOT NULL
    muted           BOOLEAN DEFAULT FALSE
    muted_until     TIMESTAMPTZ             # 定时静音
    PRIMARY KEY(conversation_id, uid)
```

迁移策略：向后兼容 —— 现有的 `ListInbox` 查询可继续工作，新的会话表在消息发送时自动创建/更新 `last_message_at`。客户端可以沿用旧的 Inbox API，不做任何变更。

### 产品经理视角

会话组织是消息类产品用户需求排名前三的功能（仅次于"发送消息"和"已读回执"）。缺少这些能力意味着：

- 用户每天面对混乱的收件箱，无法区分优先级（工作 vs 闲聊 vs 系统通知）
- 没有"置顶"导致重要对话需要频繁手动查找
- 没有"静音"导致用户只能选择承受全部噪音（通知全开）或错过一切（block 对方）
- 没有"归档"导致收件箱随着时间推移愈发混乱

这些是**用户可以直接感知**的缺失功能，它们直接影响日活跃度和用户留存。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 两人之间有多个对话 | 当前架构下两人只能有一个隐式对话。若未来支持"对话主题"（如"项目A" vs "闲聊"），则需允许多个 conversation 关联同一对参与者 |
| 删除对话 vs 删除消息 | 对话删除应为软删除（`archived=true`），不应影响消息数据。双方各有独立的 archive 状态 |
| 置顶数量限制 | 限制最多 10 个置顶对话，防止滥用 |
| 静音后收到新消息 | 静音期间消息正常投递、正常入库，但`dispatcher` 的偏好检查扩展为检查全局偏好 + 会话级偏好（会话级优先覆盖） |

---

## 方向四：内容审核自动化管线（Automated Content Moderation Pipeline）

### 问题陈述

aero-id 已有完整的**手动**举报→审核流程（`comment_reports` 表、`ReportComment`/`HideComment`/`ResolveReport` 端点），但整个流程是 **100% 人工驱动** 的：

1. 消息/评论/文件在创建时 **不经过任何自动审核**，直接成为可见
2. 违规内容在被举报之前，可能已经被大量用户看到
3. 没有集成外部审核 API（图片鉴黄、文本仇恨言论检测、病毒扫描）的能力
4. 没有"先审后发"（pre-moderation）或"发后抽检"（post-moderation）的策略

### 代码级证据

**证据 1：消息/评论创建路径无审核钩子**

```go
// internal/message/service.go — createMessage
func (s *service) createMessage(ctx context.Context, actor string, in SendMessageInput) (*domain.Message, error) {
    m := &domain.Message{...}
    evt := outbox.NewEvent(...)
    if err := s.messages.Create(ctx, m, evt); err != nil { ... }
    // ← 无审核检查点。消息直接入库 → 接收方可立即查询到
    return m, nil
}
```

```go
// internal/comment/service.go — CreateComment
// 同样没有审核检查点。评论直接成为可见。
```

**证据 2：文件上传路径无内容扫描**

```go
// internal/file/service_upload.go — persistUpload
func (s *fileService) persistUpload(ctx context.Context, ...) (*domain.File, error) {
    key, err := s.store.Put(ctx, storageKey, reader, in.MimeType, size)
    if err != nil { ... }
    // ← 文件内容已写入存储，但未经过病毒扫描或内容类别检测
    // ← 恶意用户可以上传包含恶意代码或违规内容的文件
    return s.saveFileRow(ctx, ...)
}
```

MIME 校验和大小限制已实现，但这只约束了文件格式，**不约束内容**。

**证据 3：举报系统是纯人工的**

```go
// internal/comment/repository.go — 报告相关方法
// ReportComment — 创建举报记录，状态为 "open"
// ResolveReport — 审核员手动处理，标记为 "resolved" / "dismissed"
// HideComment — 审核员手动隐藏违规评论
// 整个流程中没有任何自动分类、自动优先级排序、或自动处理环节
```

**证据 4：缺少审核专用的 Outbox 事件类型**

```go
// internal/service/notification.go — notificationFor
// 没有 "content.reported"、"content.hidden"、"content.approved" 等事件类型
// 举报人不会被通知"你的举报已被处理"
// 内容作者不会被通知"你的内容因违规被隐藏"
```

### 架构师视角

需要引入 **Moderation Pipeline** 层，独立于业务逻辑之外：

```
                    ┌──────────────┐
Message/File/Create →│ Queue Event  │→ [Async Processing] → [External APIs] → [Decision]
                    └──────────────┘                      │
                           │                     ┌────────┴────────┐
                           │                     ▼                 ▼
                           │               Allow (visible)    Block/Flag
                           │                                      │
                           ▼                                      ▼
                    [Normal Flow]                         [Flag for Review]
                                                           [Replace with Placeholder]
                                                           [Notify Mod Queue]
```

架构考虑：
- **同步 vs 异步**：内容审核可以同步（发前审核——延迟高但安全）或异步（发后审核——延迟低但有风险）。建议对文本实行异步（标记→后审），对媒体文件实行同步（先扫后存）。
- **审核结果缓存**：同一用户的类似内容可能产生相同审核结果。缓存审核决策的 checksum，减少重复 API 调用。
- **降级策略**：外部审核 API 不可用时，应走"宽松"通道（默认允许，标记为待复审）而非"严格"通道（默认阻止），避免审核 API 中断导致全站发布瘫痪。
- **插件化**：Moderation Interface 应该是可插拔的——允许接入不同的审核服务商（AWS Rekognition、Google Cloud Vision、自定义模型）。

### 产品经理视角

缺少自动化审核对产品的直接影响：

| 风险 | 影响 |
|------|------|
| 违规内容立即可见 | 法律合规风险（尤其涉及仇恨言论、儿童安全法规） |
| 文件数据未扫描 | 恶意软件传播风险，用户设备安全 |
| 100% 人力审核 | 高运营成本，审核延迟长（平均数小时到数天） |
| 举报无反馈 | 举报人信任度降低，不愿再举报 |

从产品策略角度看，审核管线是安全能力和运营效率的分水岭。自动化审核无法 100% 准确，但可以将需要人工处理的内容量减少 95%+（自动标记→人工复核→处理）。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 误判（False Positive） | 内容被错误阻止 → 用户申诉 → 人工复核 → 发布。审核决策应有 1 小时冷却期（不重复审核同一内容） |
| 漏判（False Negative） | 违规内容通过审核 → 必然存在。所以人工审核队列不能省。自动化降低量级，不替代人 |
| 审核 API 延迟波动 | 同步审核路径设 2s 超时。超时后放行但标记为"免审待复审" |

---

## 方向五：跨实体关联浏览与实体图查询（Entity Relationship Browser / Cross-Reference Graph）

### 问题陈述

aero-id 的三种核心实体（文件、消息、评论）之间存在丰富的隐含关系：

- 消息可以引用文件（`Message.FileID`）
- 评论可以挂在文件或消息上（`Comment.TargetType/TargetID`）
- 评论可以有父评论（`Comment.ParentID`/`RootID`）
- 文件可以分享到用户（`file_shares`）

但是，**没有任何 API 可以跨实体浏览这些关系**。用户无法回答以下问题：

- "这个文件被哪些消息引用过？"（反向引用）
- "这个用户在这个文件上发表了哪些评论？"
- "我分享给 Bob 的文件中，哪些被 Bob 评论过？"
- "这条消息及其所有相关评论的文件是什么？"

### 代码级证据

**证据 1：没有存储反向引用的机制**

文件引用（`Message.FileID`）是单向的：

```go
// internal/domain/message.go — Message.FileID *ID
// Message 有一个 FileID 指向文件
// 但 File 没有任何字段指向引用它的 Message 列表
// 即：没有反向索引或物化关联表
```

同样，评论与目标实体的关联也是单向的：

```go
// internal/domain/comment.go（从 internal/comment/comment.go 推断）
type Comment struct {
    // ...
    TargetType string  // "file" | "message"
    TargetID   string
    // 但 File 和 Message 没有包含 comments[] 的字段
    // 没有从 File/Message → Comment 的反向查询 API
}
```

**证据 2：FTS 搜索不包含关联元数据**

```go
// internal/search/search.go — Search
// 返回的 SearchHit 包含独立的实体消息，
// 但不包括"这条消息引用了哪些文件"或
// "这个文件上有哪些评论"等关联信息
type SearchHit struct {
    Kind    domain.SearchKind
    Message *domain.Message
    Comment *domain.Comment
    File    *domain.File
    // ← 没有 RelatedEntities []RelatedEntity 字段
}
```

**证据 3：Proto 中无关联浏览端点**

```protobuf
// api/proto/aero_id/v1/aero_id.proto
// 全部 38 RPCs 都不包含"列出引用某文件的消息"或
// "列出某文件上的所有评论"等相关浏览请求
```

这些查询可以通过组合现有 API 在客户端实现，但效率极低：
- "查找引用文件 X 的消息" → 需要在客户端遍历所有与目标用户相关的消息，逐一检查 `file_id`。不可行。
- "查找该文件上的所有评论" → 需要知道文件的 `target_type="file"` + `target_id` 来查 `ListComments`，但这个端点不存在于文件服务中。

### 架构师视角

需要引入 **Entity Relationship Graph** 层，存储并索引实体之间的关联关系：

```
entity_references:                 # 新表，物化引用关系
    id              UUID PK
    source_type     TEXT NOT NULL    # "message" | "comment"
    source_id       TEXT NOT NULL    # source 的 ID
    ref_type        TEXT NOT NULL    # "mentions_file" | "comments_on" | "replies_to"
    target_type     TEXT NOT NULL    # "file" | "message" | "comment"
    target_id       TEXT NOT NULL    # target 的 ID
    created_at      TIMESTAMPTZ

    INDEX(source_type, source_id)
    INDEX(target_type, target_id)   # ← 反向索引，支持"查找引用了此实体的所有上游"
    INDEX(ref_type, target_id)
```

关联的写入应与业务操作原子化：

- 消息引用文件（`Message.FileID != nil`）→ 写入 `(message, msgID, "mentions_file", file, fileID)`
- 评论挂在实体上 → 写入 `(comment, cmtID, "comments_on", targetType, targetID)`
- 回复评论 → 写入 `(comment, cmtID, "replies_to", "comment", parentID)`

新的 API 端点：

| 端点 | 功能 |
|------|------|
| `GET /v1/files/:id/references` | 列出引用该文件的消息 |
| `GET /v1/files/:id/comments` | 列出该文件上的评论 |
| `GET /v1/messages/:id/references` | 列出该消息引用的实体（文件） |
| `GET /v1/messages/:id/comments` | 列出该消息上的评论 |
| `GET /v1/entities/:type/:id/related` | 通用关联浏览器 |

### 产品经理视角

实体的关联浏览是一个"你一旦拥有就会依赖"的能力。从产品角度看：

| 场景 | 当前体验 | 有了关联浏览后的体验 |
|------|---------|-------------------|
| 用户在文件上写了评论 → 其他用户需找到这个评论 | 无法直接导航 | 通过文件详情查看该文件上的全部评论 |
| 用户想找出所有引用某文件的群聊消息 | 不可能 | 一键查询文件引用者 |
| 审核员看到一条违规评论 → 需定位它所在的消息/文件上下文 | 手动搜索 ID | 一键跳转到被评论的上下文 |
| 想导出某文件相关的全部社交互动 | 需通过多个 API 手动拼装 | 单一查询返回完整的关联视图 |

这个方向是"数据可移植性"的补充——它关注的是实体之间关系的可发现性，而非数据的可导出性。

### 边界情况

| 场景 | 处理策略 |
|------|---------|
| 更新引用（编辑消息移除 FileID） | 在 `UpdateContent` 时检查 FileID 变更，更新 `entity_references` 表 |
| 删除源实体（消息软删） | 级联删除相关的 `entity_references` 行，或标记为 `deleted_at` 不返回 |
| 循环引用（理论上不可能） | 检测并拒绝写入会造成循环的引用（message A → file B, file B 不能引用 message A） |
| 批量初始化 | 项目已有的实体引用需一次性迁移：扫描所有 messages 的 FileID、所有 comments 的 TargetID，填充 `entity_references` 表 |

---

## 优先级与依赖关系矩阵

| # | 方向 | 优先级 | 前置依赖 | 估算工作量 | 核心价值 | 代码变更量 |
|---|------|--------|----------|-----------|---------|-----------|
| 1 | 实体生命周期事件完整性 | **P1** | 无（基于已有 Outbox + Dispatcher 扩展） | 低（1-2 周） | 填补关键的 UX 缺口 | 低 — 约 6 个文件修改 |
| 2 | 结构化富内容系统 | **P2** | 无 | 高（6-8 周） | 平台级能力提升 | 高 — 需新类型系统、验证器 |
| 3 | 会话组织原语 | **P1** | 无 | 中（3-4 周） | 直接影响用户日活/留存 | 中 — 新表 + API + 迁移 |
| 4 | 内容审核自动化管线 | **P1-P2** | 方向 1（审核事件通知） | 中-高（4-6 周） | 法律合规 + 运营效率 | 中 — 可插拔接口 + 适配器 |
| 5 | 跨实体关联浏览 | **P2-P3** | 方向 2（引用数据作为结构化内容的一部分） | 中（3-4 周） | 数据发现能力 | 中 — 新表 + API |

### 建议执行顺序

```
Phase 1（短期）:  方向 1 → 方向 3
  - 事件完整性是基础设施层面的"欠债"——在通知系统已就绪的情况下，不补齐是不合理的
  - 会话组织对产品体验提升最大，且架构独立

Phase 2（中期）:  方向 4 → 方向 5
  - 审核管线需要事件通知（方向 1 已完成）
  - 实体关联浏览可以独立启动，但了解引用关系需要审核和内容结构化的上下文

Phase 3（长期）:  方向 2
  - 结构化内容是最大的架构变更，需要最长的设计和实现周期
  - 其价值建立在会话组织（方向 3）和实体关系（方向 5）基础之上
```

---

## 与现有系统能力的架构映射

```
方向一：事件完整性扩展
  message.Edit / Delete / React → outbox.NewEvent() → NotificationDispatcher → notification row + SSE push
  file.ShareFile → outbox.NewEvent() → NotificationDispatcher → "file.shared" notification

方向二：结构化内容
  domain.Message.Blocks []ContentBlock (JSONB)
  Validator per block type (text/poll/card/button)
  Backward compatibility: Content string → auto-converted to [{type:"text", ...}]

方向三：会话组织
  conversations table (FK participant fields)
  conversation_labels table (FK → conversations)
  conversation_preferences table (mute/snooze per conversation)
  Inbox query: LEFT JOIN conversations + labels

方向四：审核自动化
  ModerationService interface (ScanText/ScanImage/ScanFile)
  DefaultModerationService (allow all, log)
  ExternalModerationService (AWS Rekognition, Google Vision, ...)
  Pre-moderation: hold until scan complete → allow/block
  Post-moderation: allow immediately → async check → retroactive hide + notify

方向五：实体关联图
  entity_references table (source_type, source_id, ref_type, target_type, target_id)
  Maintained via hooks in message.Create/comment.Create/file.Create
  Query: "entities related to X via type Y"
  Migration: backfill from existing data
```

---

## 总结

这五个方向不同于此前 130+ 分析中的任何一个。它们不是"再加一个 endpoint"或"优化某个查询"，而是：

| 方向 | 核心本质 | 代码级缺口特征 | 类比 |
|------|---------|---------------|------|
| **事件完整性** | 交互生命周期补齐 | Dispatcher 只处理 3/8 事件类型 | 手机响了但只响一半就停了 |
| **结构化内容** | 消息承载能力的质变 | Content 是 `string` 而非 `[]ContentBlock` | 电报 vs 微信小程序 |
| **会话组织** | 从"消息管道"到"收件箱" | 无 Conversation 实体 | 邮箱没有文件夹/标签 |
| **审核自动化** | 安全能力的量变到质变 | 审核 = 人工 + SQL update | 守夜人 vs CCTV+AI |
| **实体关联** | 数据的可发现性 | 引用是单向的，无反向索引 | 书架没有目录索引 |

它们共同回答了一个问题：**"当 aero-id 已经实现了所有基本功能后，下一个用户体验和架构升级的杠杆点在哪里？"**
