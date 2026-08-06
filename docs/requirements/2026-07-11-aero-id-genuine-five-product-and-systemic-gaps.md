# aero-id — 全量代码扫描：五个体系性产品与架构扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 逐文件扫描 `internal/` 全部 ~180 个 Go 源文件、`migrations/` 全部 22 份 SQL、`api/proto/`、`configs/`、`cmd/`、`Makefile`、`README.md`、`ROADMAP.md`。与 `docs/requirements/` 下 **50+ 份既有需求文档**做逐方向交叉比对。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和架构师/产品经理双视角论证。

---

## 前言：为何这 5 个方向仍是盲区

aero-id 在过去迭代中完成了几乎所有 ROADMAP 承诺的功能：文件流水线（流式上传、预签名、内容去重、MIME 校验、存储 GC）、写入正确性（事务性 Outbox、幂等键、死信重试）、通知系统（偏好、折叠、限速、SSE 实时推送）、安全基元（屏蔽、审计、管理员角色、限流）、可观测性（RED 指标、Tracing、断路器）、完整 HTTP↔gRPC 双栈等价。

在此成熟度之上，以下 **5 个方向** 在既有需求文档（`docs/requirements/*.md`）中 **零系统性覆盖**——没有任何文档将它们作为独立方向提出并做完整代码证据链论证。它们不是"再加一个 endpoint"类的局部修补，而是需要**架构层新增**的平台级能力。

---

## 方向一：对话级模型 / 群聊（Conversation Model & Group Chat）

### 问题陈述

当前消息模型是纯粹的**点对点式**：每条消息只有 `FromUID` 和 `ToUID` 两个用户标识。没有 `conversation_id`、没有 `participants` 表、没有 `group_metadata`。这导致以下产品需求**全部无法实现**：

- 群聊（三人及以上）
- 对话级元数据（标题、头像、置顶消息、静音状态）
- "对话级"未读计数（告诉我对话 X 里有几条未读，而不是全局总和）
- 对话内搜索（`SEARCH WHERE conversation_id = ? AND content ILIKE ?`）
- 离开/加入对话、对话级权限（仅管理员可邀请新人）
- 多用户打字指示器

### 代码级证据

**证据 1：`Message` 结构体无对话标识符**

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string        // ← 只有单用户目标
    Kind      MessageKind
    Content   string
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any
    CreatedAt time.Time
}
```

没有 `ConversationID`、没有 `ConversationType`。整个系统中不存在 `Conversation` 实体。

**证据 2：全部 22 份迁移中无对话相关表**

```sql
-- migrations/000001 ~ 000022：涵蓋文件、消息、评论、点赞、举报、Outbox、
-- 通知、屏蔽、审计、索引、偏好、折叠、存储引用、管理员角色
-- 但没有 CREATE TABLE conversations、conversation_participants、
-- conversation_messages、conversation_mutes 等
```

`make migrate-up` 产物中完全没有对话相关架构。

**证据 3：服务层接口以点对点方式设计**

```go
// internal/message/message.go — Service 接口
type Service interface {
    Send(ctx context.Context, in SendMessageInput) (*domain.Message, error)
    Conversation(ctx context.Context, withUID string, page domain.Page) (*MessageList, error)
    MarkConversationRead(ctx context.Context, withUID string) error
    // ...
}
```

`Conversation(ctx, withUID)` 方法名暗示"对话"，但实际实现是**按两个 UID 过滤**——本质是 `(to_uid = ? AND from_uid = ?) OR (to_uid = ? AND from_uid = ?)` 双向查询，不是真正的对话实体。

**证据 4：Outbox 事件 payload 只传递单用户信息**

```go
// internal/service/notification.go
type messageSentPayload struct {
    MessageID string `json:"message_id"`
    FromUID   string `json:"from_uid"`
    ToUID     string `json:"to_uid"`        // ← 单接收者
}
```

即使在未来扩展中，要通知 N 个群成员，当前事件模型也需要 N 条独立事件。没有 `[]string` 的支持。

### 架构冲击

| 维度 | 影响 |
|------|------|
| **新表** | `conversations`、`conversation_participants`、`conversation_mutes`、`conversation_pins` |
| **领域模型** | 新增 `Conversation` / `ConversationRole` 实体；`Message` 加入 `ConversationID` |
| **迁移策略** | 非破坏性：现有消息的 `FromUID|ToUID` 可被后台 job 归入自动创建的 1:1 对话 |
| **服务层** | 新增 `ConversationService`；`MessageService.Send` 增加 `ConversationID` 参数 |
| **通知 / Outbox** | `message.sent` payload 扩展为支持 `ConversationID` 和 `RecipientUIDs []string` |
| **SSE Hub** | 群聊场景下需要 hub 向多用户推送；当前逐用户推送模型可重用但需放大 |
| **Route 设计** | `/v1/conversations` 命名空间 + 现有 `/v1/messages` 扩展可选 `?conversation_id=` |

### 产品价值

群聊是即时通讯的基础设施，不是可选项。缺失对话抽象意味着：
- 无法支持团队、项目组、兴趣群等常见社交场景
- 第三方集成（如 "通知所有项目成员"）只能逐用户撒消息
- 系统消息（如 "用户 X 加入了项目"）没有归属的容器

---

## 方向二：用户在线状态 / 活跃度探针（User Presence & Online Status）

### 问题陈述

SSE Hub 精确知道**哪些 uid 当前有活跃连接**，但这个信息被囚禁在进程内存中，未向任何用户暴露。没有 `GET /v1/presence?uids=...` 端点，没有 `last_seen_at` 列，没有"用户 X 在线"的实时通知。

这是一个**信息不对称**问题：系统掌握在线状态，但用户看不到。而任何即时通讯产品中，"对方是否在线"都是最基础的社交信号之一。

### 代码级证据

**证据 1：Hub 精确追踪每个 uid 的活跃连接数**

```go
// internal/hub/hub.go:22-29
type Hub struct {
    subs   sync.Map // uid → *uidSubs    ← 精确映射 uid → 订阅者列表
    nextID atomic.Int64
    closed atomic.Bool
}

// pub delivers an event to all current subscribers for uid.
func (h *Hub) pub(uid string, evt SSEEvent) {
    raw, ok := h.subs.Load(uid)
    // ...
}
```

`subs sync.Map` 以 `uid` 为键存储活跃订阅者。这个信息是**实时的**且**按用户精确到个位数**。但它完全只供 `Publish` 内部使用——外部不可见。

**证据 2：没有任何实体存储或暴露在线状态**

```go
// internal/domain/shared.go — 无 Presence 相关类型

// internal/domain/message.go
// internal/domain/file.go
// internal/domain/comment.go
// internal/domain/notification.go
// internal/domain/audit.go
// internal/domain/access.go
// internal/domain/actor.go
// internal/domain/admin.go
// 以上所有 domain 实体中均无 "last_seen_at"、"is_online"、"presence_status" 等字段
```

**证据 3：SSO User 结构体不包含在线信息**

```go
// internal/sso/client.go — 从 SSO 批量获取的用户信息
type User struct {
    UID       string
    Name      string
    AvatarURL string
    // 无 IsOnline、LastSeenAt、Presence 相关字段
}
```

SSO 返回的用户信息中只包含静态资料，不含实时在线状态。

**证据 4：HTTP 路由注册中无 presence 端点**

```go
// internal/handler/http/router.go — Routes 注册
// Auth group 下已有: /files, /messages, /comments, /notifications,
// /blocks, /events, /notification-preferences, /search, /admin/audit-log
// 但 没 有 /presence
```

### 架构冲击

| 维度 | 影响 |
|------|------|
| **新组件** | `internal/presence/` — 轻量级服务，桥接 Hub 的在线状态到 API |
| **Hub 扩展** | 需要暴露 `IsOnline(uid string) bool` 和 `OnlineUIDs() []string`（或批量查询） |
| **持久化** | 多实例部署下在线状态需跨实例同步（Redis 或类似）；但单实例可直接读 Hub 内存 |
| **通知** | 当用户上线时，可触发 `presence.online` 事件通知关注者（需新增"关注者"模型） |
| **API** | `GET /v1/presence?uids=a,b,c` + 可选的订阅端点 `(SSE) presence.*` 事件类型 |
| **限流** | 批量查询需限制 `uids` 参数长度（如 100），防止滥用 |

### 产品价值

- 即时通讯最基础的**社交上下文**——"对方是否在线"影响用户回消息的预期
- SSO 无需关心在线状态——aero-id 掌握此信息，应在此层暴露
- 可衍生"最近活跃"排序（如在线用户排在联系人列表前面）
- 为"打字中"指示器铺路（需要 WebSocket 或 SSE 双向通道）

---

## 方向三：自动化内容审核管线（Automated Content Moderation Pipeline）

### 问题陈述

当前内容审核完全依赖人工举报 → 人工处理（隐藏/恢复正常）。中等规模的社区中，纯人工审核不可持续——恶意用户可批量发言后被举报才处理，临时和垃圾内容会在可见窗口中传播数小时甚至数天。

缺少以下关键能力：
1. **自动垃圾评分**：基于规则或 ML 模型对新内容（消息、评论、文件描述）打分
2. **自动屏蔽**：超过阈值的自动隐藏或标记
3. **图片/文件哈希匹配**：检测已知违规图片（PhotoDNA / PDQ 或类似哈希）
4. **用户信誉系统**：频繁被举报或被自动标记的用户触发升级操作
5. **审核操作日志**：自动操作需记录以支持申诉

### 代码级证据

**证据 1：当前审核完全是被动的手动流程**

```go
// internal/comment/comment.go — Service 接口中的审核操作
type Service interface {
    Report(ctx context.Context, id domain.ID, reason string) error  // ← 用户举报
    Hide(ctx context.Context, id domain.ID) error                   // ← 手动隐藏
    Unhide(ctx context.Context, id domain.ID) error                 // ← 手动恢复
    ListReports(ctx context.Context, ...) (*ReportList, error)      // ← 人工查看队列
    ResolveReport(ctx context.Context, ...) error                   // ← 人工处理
    // 没有任何 AutoModerate / ScoreContent / FlagForReview 等方法
}
```

所有操作都是"有人举报 → 有人审查 → 有人决定"的模式。没有自动步骤。

**证据 2：`CommentReport` 没有自动标记相关的字段**

```go
// internal/domain/comment.go
type CommentReport struct {
    ID          ID
    CommentID   ID
    ReporterUID string        // 举报者（用户），不是系统
    Reason      string
    Status      ReportStatus
    CreatedAt   time.Time
    ResolvedAt  *time.Time
    ResolvedBy  string
}
```

没有 `AutoFlagged bool`、没有 `SpamScore float64`、没有 `AutoAction` 字段。

**证据 3：消息创建无内容审核步骤**

```go
// internal/service/message.go — Send 方法逻辑
func (s *service) Send(ctx context.Context, in SendMessageInput) (*domain.Message, error) {
    // 1. 获取 actor
    // 2. 校验 kind
    // 3. 检查屏蔽
    // 4. 创建消息 + outbox 事件
    // ★ 没有任何内容扫描、垃圾评分、链接黑名单检查
}
```

**证据 4：文件上传后无扫描**

```go
// internal/file/service_upload.go — 上传流程涉及：
// - size check
// - MIME type check
// - checksum check (dedup)
// - storage Put
// ★ 但没有病毒扫描、没有内容哈希比对、没有 OCR 分析
```

### 架构冲击

| 维度 | 影响 |
|------|------|
| **新组件** | `internal/moderation/` — 评分器、规则引擎、任务队列 |
| **审核管线接口** | `ModerationScorer` 接口：`Score(ctx, entity) → (score, reasons, error)` |
| **异步化** | 审核不应阻塞写入路径 → 用 Outbox 事件触发异步审核任务 |
| **可插拔后端** | 规则引擎（如 SpamAssassin 风格）和 ML HTTP 端点都可嵌入 `Scorer` |
| **自动操作** | `score > threshold_auto_hide` → 自动隐藏；`> threshold_flag` → 标记待审 |
| **熔断** | 误判风险：自动隐藏操作需可逆，保留完整操作审计 |
| **新表** | `moderation_scores`（实体ID, 实体类型, 评分器, 评分, 原因, created_at） |

### 产品价值

- 没有自动化审核，社区规模稍大就会充斥垃圾信息
- 评论和消息是用户生成内容的两种形式，两者都需要保护
- 文件（图床场景）比文本内容更容易被滥用于传播违规内容
- 审核系统的缺失是**信任基础设施**的缺失——不只是功能缺失

---

## 方向四：数据可移植性与用户导出（Data Portability & User Data Export）

### 问题陈述

aero-id 存储用户的文件、消息和评论，但没有任何机制让用户获取自己数据的完整副本。这不仅是一个 UX 缺口，更是**合规风险**——GDPR（第 20 条"数据可移植权"）、CCPA（"知情权"）等法规要求平台以结构化、机器可读的格式提供用户数据。

此外，用户迁移/注销时缺少数据取出机制，会增加用户对平台的锁定感。

### 代码级证据

**证据 1：没有任何导出端点或服务**

```go
// internal/service/service.go — 所有 Service 接口列表
type FileService interface { ... }     // CRUD + 分享 + 预签名
type MessageService interface { ... }  // 收发 + 标记已读 + 反应
type NotificationService interface { ... }
type CommentService interface { ... }  // CRUD + 编辑 + 举报
type BlockService interface { ... }
// ★ 没有任何 ExportService、DataPortabilityService
```

```go
// internal/handler/http/router.go — 所有路由组
// Public: /files/dl
// Auth: /v1/...（文件、消息、评论、通知、屏蔽、事件、偏好、搜索、管理）
// ★ 没有任何 /v1/export 或 /v1/data-portability
```

**证据 2：没有批量数据获取的 Repository 方法**

```go
// internal/file/file.go — Repository 接口
type Repository interface {
    Get(ctx context.Context, id domain.ID) (*File, error)
    ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*File, string, error)
    // 有 List 但受分页限制，没有"列出我所有文件不分页"的方法
    // 导出场景需要全量数据
}
```

```go
// internal/message/message.go — MessageRepository
type MessageRepository interface {
    ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
    ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error)
    // 同样受分页限制
}
```

当前分页设计（`LIMIT n+1` cursor）是面向实时列表场景的，数据导出需要不同的扫描策略（全量游标或时间窗口扫描）。

**证据 3：没有异步导出任务基础设施**

尽管项目已有 `outbox.Relay`（异步事件处理）和 `storage/gc.Sweeper`（定时 job），但没有：
- 通用 `BackgroundJob` 框架
- 文件打包/压缩能力
- 导出结果存储和过期机制
- 导出进度通知（可通过现有通知系统派发 `export.completed`）

### 架构冲击

| 维度 | 影响 |
|------|------|
| **新服务** | `ExportService` 接收导出请求 → 异步生成 → 通知用户 |
| **新组件** | `internal/export/` — 导出 job 定义、数据收集器、打包器、存储 |
| **数据收集器** | 每个实体类型一个 Collector（`FileCollector`、`MessageCollector`、`CommentCollector`） |
| **格式** | JSON Lines（.jsonl）或按类型分目录压缩（.zip） |
| **存储** | 导出产物存到 `storage.FileStorage`（复用现有文件存储），短暂保留后清理 |
| **通知** | Outbox 事件 `export.completed` → 用户收到 SSE 推送 + 通知行 |
| **合规约束** | 导出需包含关联数据（如消息的发送方 UID → 需调用 SSO 解析为可读名称） |
| **限流** | 每个用户每 24h 只能发起一次导出请求 |

### 产品价值

- **合规底线**：GDPR/CCPA 覆盖的运营区域中，数据可移植性是法律要求而非功能选项
- **用户信任**：提供导出入口降低"被锁定"焦虑
- **架构复用**：利用了已有的 outbox（异步通知）、storage（存储产物）、notification（通知完成）组件
- **相对低风险**：纯读取性功能，不影响写入路径一致性

---

## 方向五：资源收藏与实体间引用（Bookmark / Pin / Cross-Entity Reference）

### 问题陈述

用户在平台上会产生大量内容，但缺少"保存以备后用"的基础机制：
- 看到一条重要消息 → 无法收藏
- 看到一个有用文件 → 无法加入"收藏夹"
- 需要快速回到某条评论 → 无法固定
- 希望将消息 A 引用到消息 B → 没有引用/回复锚点机制

缺少的书签/引用系统是**生产力差距**——用户浏览了大量内容但无法组织它们。这在文件管理（aero-id 的核心场景）中尤其突出。

### 代码级证据

**证据 1：没有任何实体有 `IsBookmarked`、`IsPinned` 或类似字段**

```go
// internal/domain/file.go
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64
    MimeType    string
    StorageKey  string
    StorageType string
    Checksum    string
    Visibility  Visibility
    Meta        map[string]any
    CreatedAt   time.Time
    UpdatedAt   time.Time
    // ★ 无 Bookmarked、Pinned、Starred 字段
}

// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    Kind      MessageKind
    Content   string
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any
    CreatedAt time.Time
    // ★ 同
}

// internal/domain/comment.go
type Comment struct {
    // ★ 无 Bookmark、Pin 字段
}
```

**证据 2：迁移中无书签/收藏表**

全部 22 份迁移涵盖：
- 文件、消息、评论（主体实体）
- 评论点赞、消息反应（反应）
- 文件分享、文件分享（权限）
- 评论举报、用户屏蔽（安全）
- Outbox、通知、审计、偏好、索引、折叠、存储引用、管理员角色

**没有 `bookmarks`、`pins`、`saved_items`、`favorites` 等表。**

**证据 3：跨实体引用缺乏通用模型**

```go
// internal/domain/shared.go — 现有的跨实体机制
// TargetType 仅在 Comment 中使用（comment 的 target）
type TargetType string

const (
    TargetTypeFile    TargetType = "file"
    TargetTypeMessage TargetType = "message"
)
```

`TargetType` 概念限于 Comment → {File, Message}，不可泛化为通用引用。

**证据 4：消息之间无引用/回复锚点**

```go
// internal/domain/message.go
type Message struct {
    // ★ 没有 ReplyToID、QuoteMessageID、ReferencedEntity 等字段
}
```

用户无法"引用"上一条消息回复（常见于 Slack/Discord 的回复串功能）。

### 架构冲击

| 维度 | 影响 |
|------|------|
| **新表** | `bookmarks`（uid, entity_type, entity_id, collection, created_at）——支持集合归类 |
| **通用引用模型** | 扩展 `TargetType` 为通用 `EntityRef`（type + id），用于评论、书签、引用 |
| **服务层** | `BookmarkService`（Crud + ListByCollection + Remove） |
| **API** | `POST /v1/bookmarks`、`GET /v1/bookmarks?collection=...`、`DELETE /v1/bookmarks/:id` |
| **消息增强** | `Message.QuoteMessageID` 支持引用回复（沿用 `file_id` 类似的设计模式） |
| **通知** | 书签本身不需要通知，但"你的消息被收藏"可作为一种新的通知类型 |
| **性能** | 书签查询需要 `(uid, entity_type, entity_id)` 唯一索引 + `(uid, collection, created_at)` 列表索引 |

### 产品价值

- 文件管理场景尤其需要"收藏组织"功能（文件夹、标签、收藏集）
- 消息引用回复是提升对话可读性的关键 UX
- 书签是**最轻量级**的用户参与度机制——比评论门槛低，比"like"信息量丰富
- 可为后续"稍后阅读"、"项目收藏夹"等高级功能奠定基础

---

## 总结：五个方向的优先级评估

| # | 方向 | 产品冲击 | 架构复杂度 | 依赖 | 优先级 |
|---|------|---------|-----------|------|--------|
| 1 | 对话级模型 / 群聊 | 🔥🔥🔥🔥🔥 | 🔧🔧🔧🔧 | 消息服务、Outbox、Hub | **P1** |
| 2 | 用户在线状态 / 活跃度探针 | 🔥🔥🔥 | 🔧🔧 | Hub（已存在） | **P2** |
| 3 | 自动化内容审核管线 | 🔥🔥🔥🔥🔥 | 🔧🔧🔧🔧🔧 | Outbox（可复用）、外部服务 | **P1** |
| 4 | 数据可移植性 / 用户导出 | 🔥🔥🔥 | 🔧🔧🔧 | Outbox、Storage（可复用） | **P2** |
| 5 | 资源收藏与实体间引用 | 🔥🔥🔥🔥 | 🔧🔧 | 无外部依赖 | **P1** |

### 特别说明

方向一（群聊/对话模型）和方向三（自动审核）获得了最高优先级评分，原因：
- **群聊**是即时通讯领域的核心能力缺口——当前架构只能点对点交流，限制了几乎所有社交场景
- **自动审核**是信任与安全的基础设施——缺少它在社区规模化时是不可持续的
- 两者都需要架构层面的新增（对话实体、审核管线），但不是近期不可完成的大工程

方向五（书签/引用）虽然架构复杂度低，但用户感知价值高，适合作为**快速交付**的独立增量。
