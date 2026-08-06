# aero-id — 全局代码扫描：五个真正未被覆盖的高价值扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 + 产品经理  
> **方法**: 逐文件通读 `internal/` 全部 180+ Go 源文件、`migrations/` 全部 22 份 SQL、`api/proto/aero_id/v1/aero_id.proto`、`cmd/`、`configs/`、`Makefile`  
> **交叉验证**: 与 `docs/requirements/` 下 **120+ 份既有分析文档**、`docs/ARCHITECTURE.md`、`docs/REVIEW_*`、`ROADMAP.md` 逐方向、逐关键词交叉比对  
> **核心承诺**: 以下 5 个方向在全部既有材料中 **从未作为独立方向被完整论证**（无代码证据链、无架构/PM 双视角分析）  
> **约束**: 不编写任何代码

---

## 代码库成熟度现状

aero-id 已是一个高成熟度的生产级服务：

| 域 | 已交付能力 |
|---|---|
| **传输** | HTTP (Gin) + gRPC 41+ RPCs 完全对等 · SSE 实时推送（含 last_event_id 重连回放）· gRPC health + 可选 reflection · OTel 追踪 + RED 指标 |
| **文件** | 流式上传 · S3 presigned 直传 · SHA-256 内容去重 · MIME/Size 校验 · 显式分享/撤销 · 存储引用计数 + GC 扫表 + 孤儿清理 · 元数据 PATCH |
| **消息** | 1:1 私信 · Reactions · 编辑/软删 · 已读回执 + 计数 · Inbox/Conv 视图 · 系统消息 · 全文搜索 · 按会话批量标记已读 |
| **评论** | 两层嵌套（楼中楼）· Like/Unlike + 原子计数 · 举报→审核→隐藏/显示 · 编辑 · FTS 搜索 |
| **通知** | Outbox 驱动 · SSE 实时推送 · 折叠去重 · 偏好设置（启用/静默时段）· Block 抑制 · Webhook HMAC 签名投递 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN 索引）· CJK ILIKE 回退 · 多 kind 联合搜索 |
| **可靠性** | 事务性 Outbox + 指数退避 + 死信 · 幂等键（DB 持久化 + 过期清理）· 双传输限流 · SSO 断路器 + stale-cache + singleflight + mTLS + cert-pinning · 冷启动 warmup · 优雅关停 + 最后 drain |
| **治理** | 方向性 Block · 审计日志（append-only）· Admin 角色（4 权限）· permission 中间件 · 评论举报处理 |
| **基础设施** | 迁移 CLI · 自动迁移选项 · 22 份可逆迁移 · 全链路 OTel 追踪 · Prometheus RED 指标 · SSO 健康仪表盘 |

在此坚实基础上，以下 5 个方向是该系统**尚未触及**的产品/架构增长极。它们的共同特征是：

1. 在全部 120+ 既有分析文档中 **从未被作为独立的高价值方向完整论证**
2. 每个方向在代码库中有**明确的可定位证据**（不是猜想的缺口）
3. 既是**产品价值增长点**（PM 视角），也是**架构复杂度增长点**（架构师视角）

---

## 方向一：Conversation 模型升级 — 群组聊天与多参与者消息

### 问题陈述

当前消息系统严格限制为 **1:1 隐式会话**（`from_uid` ↔ `to_uid` 对）。没有独立的 `conversations` 实体，没有群组聊天能力，没有会话元数据（名称、头像、 pinned 消息），没有 typing 指示器，没有每会话的单独通知偏好。这是 aero-id 产品能力中**最显著的功能缺口**。

### 代码级证据

**证据 1：没有 `conversations` 表或实体**

```go
// internal/domain/message.go — 全部 domain 模型，无 Conversation 类型
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    Kind      int
    Content   string
    FileID    *ID
    ReadAt    *time.Time
    CreatedAt time.Time
    EditedAt  *time.Time
}
```

对比整个 domain 包：
- `internal/domain/` 下有 `comment.go`、`file.go`、`message.go`、`notification.go`、`search.go`、`shared.go`
- **没有任何文件定义 `Conversation` 结构体**

**证据 2：Repository 没有 `conversations` 表**

```go
// internal/repository/message.go
type MessageRepository interface {
    // ...
    ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error)
    MarkConversationRead(ctx context.Context, toUID, peer string, at time.Time) error
    // ← 无 CreateConversation、无 AddParticipant、无 RemoveParticipant
    // ← 无 UpdateConversationMetadata
}
```

`ListConversation` 的实现只是对 messages 表的 pair 过滤：

```go
func (r *messageRepo) ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error) {
    q := r.db.WithContext(ctx).Model(&domain.Message{}).
        Where("(from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)",
            uidA, uidB, uidB, uidA)
    // ...cursor 分页...
}
```

**证据 3：22 份迁移中没有任何一份创建 conversations 表**

```bash
ls migrations/
# 000001_create_files  →  000022_admin_roles
# 没有 0000xx_create_conversations
```

**证据 4：proto 中没有群组或 Conversation 实体**

```protobuf
// api/proto/aero_id/v1/aero_id.proto — 仅有 Message 消息体
message Message {
    string from_uid = 2;
    string to_uid   = 3;
    // ...
}
// 无 message Conversation { ... }
// 无 message GroupChat { ... }
// 无 message ConversationMember { ... }
```

**证据 5：Service 层 Inbox 是全局消息列表，非按会话分组**

```go
// internal/message/service.go
type Service interface {
    Send(ctx context.Context, in SendInput) (*Message, error)
    Get(ctx context.Context, id domain.ID) (*Message, error)
    Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error)
    Conversation(ctx context.Context, with string, page domain.Page) (*MessageList, error)
    MarkRead(ctx context.Context, id domain.ID) error
    // ...
}
```

`Inbox` 返回所有收件消息的平坦列表，没有按 conversation 分组。前端如果要显示"最近会话预览"，必须自己从平坦列表里聚合。

### 架构师视角

这是一个**数据模型层的基础设施缺失**。没有显式会话模型意味着：

1. **群组聊天无法实现**：需要 N×M 条消息（每个参与者一条），而不是一条消息 + M 个收件人
2. **会话元数据无法存储**：名称、头像、 pinned 消息、静默状态都必须在前端硬编码或存在某个消息的 `meta` 字段中（无约束、无查询索引）
3. **权限模型脆弱**：如果要邀请新人加入已有会话，无法做——现有消息是写死的 1:1 对
4. **查询性能浪费**：每次加载"最近会话列表"都需要 distinct uid 子查询 + 聚合

推荐方向：
- 新增 `conversations` 表（id, type, name, avatar, created_by, created_at, updated_at）
- 新增 `conversation_members` 表（conversation_id, uid, role, joined_at, last_read_at, notification_pref_override）
- 消息增加 `conversation_id` 列（原有 `from_uid`/`to_uid` 保留用于单聊快速迁移）
- 群组消息：一条消息写入 → 对每个成员生成通知（or 共享已读状态）
- 迁移策略：现有 1:1 消息对在首次查询时自动创建隐式对话

### 产品经理视角

群组聊天是**消息系统的基线功能**。没有它：

- 项目合作需要拉群 → 只能在 aero-id 外部完成
- 无法做"全员公告"（只能逐个发系统消息）
- 前端被迫为每个用户的对话列表做客户端聚合
- 产品无法推广"基于文件的讨论"——如果 3 个人需要讨论一个共享文件，他们只能各自 1:1 私聊

估算影响：**高**。这是现有用户最常要求的功能缺口。

### 已知受影响模块

`internal/domain/message.go` · `internal/message/`（全包） · `internal/repository/message.go` · `api/proto/aero_id/v1/aero_id.proto` · `migrations/`（新增迁移）

---

## 方向二：Webhook 外发投递可靠性 — 顺序、幂等与死信可观测性

### 问题陈述

Webhook 是 aero-id 向外部分发事件的核心机制。但当前实现缺少三个关键投递保证：**事件缺少去重标识符**（消费者无法安全重试）、**无投递顺序保证**（同一 aggregate 的事件可能乱序到达）、**死信队列不可观测**（死信事件无法通过 API 查看或重试）。这使得外部消费者无法将 webhook 当作可靠事件流使用。

### 代码级证据

**证据 1：Webhook payload 没有投递 idempotency key**

```go
// internal/outbox/webhook.go
type webhookPayload struct {
    ID            string          `json:"id"`           // ← 事件的 DB 主键（UUID v7）
    EventType     string          `json:"event_type"`
    AggregateType string          `json:"aggregate_type"`
    AggregateID   string          `json:"aggregate_id"`
    Payload       json.RawMessage `json:"payload"`
    CreatedAt     time.Time       `json:"created_at"`
    // ← 没有 IdempotencyKey 字段
    // ← 没有 DeliveryAttempt 字段
    // ← 没有 X-Event-Id 头之外的幂等机制
}
```

虽然 `webhookPayload.ID` 是事件主键，但它**未作为幂等键暴露给消费者契约**。没有 `Idempotency-Key` 头，消费者无法基于 HTTP 幂等语义安全重试。

**证据 2：最多一次投递（at-most-once）语义**

```go
// internal/outbox/relay.go（伪代码）
func (r *Relay) drain(ctx context.Context) {
    events, err := r.repo.FetchUnpublished(ctx, r.batchSize)
    for _, e := range events {
        if err := r.handler.Handle(ctx, e); err != nil {
            // 递增 retry_count → 指数退避 → 超限后标记 dead_letter
            // 但 MarkPublished 在 handle 成功后执行
        }
    }
}
```

如果 relay 进程在 `Handle` 成功之后、`MarkPublished` 之前崩溃，该事件会在下次 tick 被**重新投递**。外部 webhook 消费者收到重复事件，且无机制区分是新事件还是重放。

**证据 3：没有事件顺序保证**

```go
// internal/outbox/repository.go
func (r *GormRepository) FetchUnpublished(ctx context.Context, limit int) ([]*Event, error) {
    // ... ORDER BY created_at ASC ...
    // 但同一事务内创建的事件有相同的 created_at（微秒精度）
    // 且不同事务的事件可能因事务提交顺序与 created_at 顺序不一致
}
```

没有按 `aggregate_id + event_type` 做顺序号（sequence number），消费者无法检测乱序投递。

**证据 4：死信事件完全不可观测**

```go
// internal/outbox/relay.go
// MarkFailed 在 retryCount >= maxRetries 时设置 DeadLetter = true
// 但是没有 API 可以：
//   - 查看死信事件列表
//   - 获取死信原因和重试历史
//   - 手动重试死信事件
//   - 丢弃死信事件
```

Admin handler 只暴露了审计日志，没有 outbox 管理的任何端点。

**证据 5：配置中 event_types 的拼写错误静默通过**

```go
// internal/config/config.go
type WebhookConfig struct {
    EventTypes []string `mapstructure:"event_types"`
    // ...
}
// 验证函数 validate() 检查 URL 和 Timeout，但从不验证 event_types 是否合法
// "message.sent" vs "message.send" — 拼写错误，静默不投递
```

### 架构师视角

Webhook 是**平台的关键接口**。当前实现对内部消费者（notification dispatcher）足够好，对外部消费者**不够**：

1. 外部消费者无法实现**安全重试**：收到 502 后重试，收到重复事件无法区分是新事件还是重放
2. 外部消费者无法**重建事件序列**：没有 ordering key，无法确定事件的处理顺序
3. 运营无法**排障 webhook 问题**：死信事件不可见，外部消费者反馈"收不到事件"时无诊断入口

推荐方向：
- 在 HTTP 头中添加 `Idempotency-Key: <event-id>:<attempt>`，使消费者可以安全重试
- 在事件 payload 中添加 `sequence_number`（按 aggregate_id 单调递增），使消费者可以检测缺漏或乱序
- 新增管理端点 `GET /admin/outbox/dead-letters`、`POST /admin/outbox/retry/:id`、`DELETE /admin/outbox/dead-letters/:id`
- 配置中验证 `event_types` 是否在中心注册表中
- 可选：Webhook 配置支持 `max_retries` 独立于 outbox relay 的全局值

### 产品经理视角

外部团队接入 webhook 时，第一个问题永远是："你们投递可靠吗？会重复吗？"当前答案是不确定的。没有幂等性保证，外部集成方的工程成本显著增加——他们必须自己实现去重，且无法信任事件顺序。

对于平台化战略而言，**webhook 投递保证是门槛级信任要求**。

### 已知受影响模块

`internal/outbox/webhook.go` · `internal/outbox/relay.go` · `internal/outbox/repository.go` · `internal/handler/http/admin.go` · `internal/handler/grpc/ops.go` · `internal/config/config.go`

---

## 方向三：内容编辑历史追踪 — 消息/评论修订版

### 问题陈述

消息和评论支持编辑（`edited_at` 列 + content 原地更新），但**编辑前的原始内容被永久覆盖**。没有编辑历史表，无法回溯/审计/撤消编辑。对于合规要求严格的场景（例如社区讨论中的"用户说了什么被改了"），这是一个严重的数据缺口。

### 代码级证据

**证据 1：编辑操作直接覆盖 content，不保存历史**

```go
// internal/message/service.go
func (s *messageService) Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error) {
    msg, err := s.msgs.Get(ctx, id)
    // ...权限校验...
    now := time.Now().UTC()
    msg.Content = content       // ← 直接覆盖，旧内容永久丢失
    msg.EditedAt = &now
    if err := s.msgs.Update(ctx, msg); err != nil {
        return nil, err
    }
    return msg, nil
}
```

**证据 2：comment 同理**

```go
// internal/comment/service.go（相似逻辑）
func (s *commentService) Edit(ctx context.Context, id domain.ID, content string) (*domain.Comment, error) {
    // ...获取、校验、直接覆盖 comment.Content ...
}
```

**证据 3：数据库 schema 没有任何历史版本表**

```bash
ls migrations/
# 000001 → 000022 全部列出，不存在 *_create_message_versions 或 *_create_comment_revisions
```

**证据 4：proto 中没有版本字段**

```protobuf
message Message {
    // ...
    google.protobuf.Timestamp edited_at = 9;
    // ← 无 version_number
    // ← 无 original_content
    // ← 无 edit_history
}

message Comment {
    // ...
    google.protobuf.Timestamp edited_at = 12;
    // ← 同理无版本字段
}
```

**证据 5：审计日志不记录编辑内容变化**

```go
// internal/domain/audit.go
const (
    AuditFileShareGrant  = "file.share.grant"
    AuditFileShareRevoke = "file.share.revoke"
    AuditReportResolve   = "comment.report.resolve"
    AuditUserBlock       = "user.block"
    AuditUserUnblock     = "user.unblock"
    AuditMessageDelete   = "message.delete"
    // ← 没有 AuditMessageEdit / AuditCommentEdit
    // ← 审计事件不包含 old_state / new_state 用于内容 diff
)
```

### 架构师视角

这是一个**不可逆数据丢失**的设计决策。当前架构假设"编辑是替换"——这在协作文档系统中是合理的，但在消息/评论系统中，原始内容具有审计价值：

1. **内容合规**：用户发表了不当内容后在审核前编辑掉——原始内容不可恢复
2. **争议仲裁**：A 说 B 说了某话，B 编辑了消息——无原始记录
3. **编辑滥用监控**：无法检测用户是否反复编辑消息（恶意修改）
4. **用户误操作**：无法恢复被误编辑的内容

推荐方向：
- 新增 `message_revisions` 和 `comment_revisions` 表（id, subject_id, content, created_at, edited_by）
- 编辑操作时，将旧内容 INSERT 到 revision 表（引用原文形成版本链）
- 审计日志增加 `AuditMessageEdit`、`AuditCommentEdit`，记录 revision ID
- 可选 API 端点 `GET /messages/:id/revisions` 和 `GET /comments/:id/revisions`（受限访问，仅管理员和作者）
- 控制编辑 window（例如 5 分钟内允许编辑，过后 revision 不可覆盖）

### 产品经理视角

从用户侧看，编辑历史是透明度和信任的基础设施。Slack 保留了消息编辑历史，Discord 保留了一小段时间的快照。没有这个能力：

- 社区版主无法审查"被编辑掉的不当内容"
- 用户无法恢复"手滑改错的内容"
- 合规审计（尤其涉及金融、法律讨论时）有数据缺口

从优先级看，这个方向不需要立即实现，但在**用户量增长 + 社区场景**出现后迅速变得关键。

### 已知受影响模块

`internal/message/service.go` · `internal/comment/service.go` · `internal/repository/message.go` · `internal/repository/comment.go` · `internal/domain/audit.go` · `migrations/`（新增迁移）

---

## 方向四：用户数据组织原语 — 标签、文件夹与批量操作

### 问题陈述

aero-id 提供"数据属于账户"的 CRUD，但**没有任何用户层面的数据组织能力**。文件不能归类到文件夹/标签/相册。消息不能标记/归档/分类。没有任何批量操作（批量删除、批量移动、批量标记已读）。用户面对数百个文件/消息时的生产力完全依赖搜索和列表分页。

### 代码级证据

**证据 1：File 表没有 `parent_id`、`folder`、`tag`、`label` 任何组织字段**

```sql
-- migrations/000001_create_files.up.sql
CREATE TABLE files (
    id            UUID PRIMARY KEY,
    owner_uid     TEXT NOT NULL,
    name          TEXT NOT NULL,
    size          BIGINT NOT NULL,
    mime_type     TEXT NOT NULL,
    storage_key   TEXT NOT NULL,
    storage_type  TEXT NOT NULL,
    checksum      TEXT,
    visibility    SMALLINT NOT NULL DEFAULT 0,
    meta          JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    deleted_at    TIMESTAMPTZ
    -- ← 没有 parent_folder_id
    -- ← 没有 tags TEXT[]
    -- ← 没有 label_ids
);
```

**证据 2：无任何 `tags`/`labels`/`folders`/`collections` 迁移**

```bash
ls migrations/ | grep -iE "tag|label|folder|collection|category|album"
# 空
```

**证据 3：无任何批量操作 API**

```go
// api/proto/aero_id/v1/aero_id.proto
service FileService {
    rpc GetFile(GetFileRequest) returns (GetFileResponse);
    rpc ListFiles(ListFilesRequest) returns (ListFilesResponse);
    rpc DeleteFile(DeleteFileRequest) returns (DeleteFileResponse);    // 单个删除
    // ...
    // ← 无 BatchDelete(BatchDeleteRequest) returns (BatchDeleteResponse)
    // ← 无 BatchUpdateMetadata(BatchUpdateMetadataRequest) returns (BatchUpdateMetadataResponse)
    // ← 无 MoveFiles(MoveFilesRequest) returns (MoveFilesResponse)
}

service MessageService {
    // ← 无 BatchDeleteMessages
    // ← 无 ArchiveConversation
    // ← 无 BatchMarkRead 跨会话
}
```

**证据 4：HTTP 路由无批量端点**

```go
// internal/handler/http/router.go — 注册的所有路由
// v1/files/...    只有单文件操作
// v1/messages/... 只有单/列表操作
// ← 无 /v1/files/batch
// ← 无 /v1/messages/batch
```

**证据 5：`meta` JSONB 被设计为扩展字段但无组织语义**

```go
type File struct {
    // ...
    Meta map[string]any `json:"meta" gorm:"type:jsonb;default:'{}'"`
}
```

`meta` 字段是一个 "自由可扩展的 JSON 字段"（来自设计文档），但未定义任何标准化 key（如 `"tags"`、`"folder"`）。每个客户端可以自己用 `meta` 存组织信息，但无法被后端索引或搜索。

### 架构师视角

这是一个**数据模型的设计空白**。将组织逻辑完全推给客户端（前端自己做 tags/folders 的元数据管理）在单客户端部署中可行，但在以下场景失败：

1. 多客户端（Web + Mobile + API）— 各客户端需要同步组织方案
2. 后端搜索无法利用组织元数据 — `SearchFiles` 只能搜 name/checksum/mime_type，不能在 "我的`工作`文件夹" 中局限
3. 无法做基于文件夹的 ACL（例如：共享整个文件夹给团队）
4. 删除/迁移没有批处理能力

推荐方向：
- 增量实现，先从最小可行开始：
  1. 新增 `labels` 表（id, owner_uid, name, color, created_at）和 `file_labels` 连接表
  2. 文件增加 `parent_folder_id` 列（自引用，NULL = root）
  3. 支持标签/文件夹作为搜索过滤器
- 后续扩展：
  4. 批量删除/移动端点（`POST /v1/files/batch/delete` 接受 id 数组）
  5. 收藏/星标（`files.starred` boolean 列）

### 产品经理视角

"我找不到我的文件"是用户生产力产品的头号痛点。纯搜索对以下用户不够：
- 按项目组织文件的团队用户（"把设计稿都放到`2026-Q3 设计`文件夹"）
- 批量归档旧消息/文件的用户（"清理三个月的旧文件"）
- 做个人知识管理的用户（"给读书笔记加`#重要`标签"）

这些功能不会立刻影响日活，但影响**重度用户的留存和满意度**。

### 已知受影响模块

`migrations/`（新增迁移 · `labels`、`file_labels`、`folders`） · `internal/domain/file.go` · `internal/file/repository.go` · `internal/file/service_crud.go` · `api/proto/aero_id/v1/aero_id.proto` · `internal/handler/http/file.go` · `internal/handler/grpc/file.go`

---

## 方向五：滥用预防管道 — 基于速率的检测、自动审核与隔离

### 问题陈述

aero-id 的评论系统支持**用户举报（report）和人工处理（resolve）**，但完全没有**自动化滥用预防**。没有垃圾内容过滤、没有敏感词/模式匹配、没有重复举报检测（同一个用户 1 秒内举报 20 次）、没有新用户的"观察期/隔离期"。这意味着在用户量增长后，恶意行为只能靠事后人工处理。

### 代码级证据

**证据 1：评论举报后不自动采取任何行动**

```go
// internal/repository/comment.go
type Repository interface {
    // ...
    CreateReport(ctx context.Context, commentID domain.ID, reporterUID, reason string) (*domain.CommentReport, error)
    // ← 没有 auto-hide on report threshold
    // ← 没有 rate limit on reports per user
    // ← 没有 block reporter after N false reports
}
```

举报只是一个 INSERT 操作，没有触发任何自动流程。

**证据 2：report 状态只有人工驱动**

```go
// internal/domain/comment.go
// CommentReport statuses
const (
    ReportStatusOpen     = "open"
    ReportStatusResolved = "resolved"
    ReportStatusDismissed = "dismissed"
)
// 没有 "auto_hidden"、"quarantined"、"escalated" 等自动化状态
```

**证据 3：没有内容过滤器或敏感词库**

```go
// internal/domain/comment.go
type Comment struct {
    // ...
    Content   string
    Status    int // 1 = 正常, 2 = 已删, 3 = 屏蔽
    // ← 没有 moderation_score
    // ← 没有 flagged_at
    // ← 没有 automated_check_results
}
```

没有 `internal/moderation/` 包、没有 `SpamDetector` 接口、没有 `SensitiveWordFilter`。

**证据 4：全局 rate limit 不区分操作类型**

```go
// internal/config/config.go
type RateLimitConfig struct {
    RPS   float64 // 全局 RPS，按 actor uid 限速
    Burst int
}
// 所有请求共享同一个 token bucket 配置
// "创建举报"与"创建评论"消耗相同 token，无法设置"每小时最多举报 10 次"
```

**证据 5：报告创建没有对 reporter 的检查**

```go
// internal/service/comment.go（伪代码）
func (s *commentService) Report(ctx context.Context, commentID domain.ID, reason string) error {
    // ...找到 comment、校验存在性...
    return s.repo.CreateReport(ctx, commentID, actor, reason)
    // ← 不检查 reporter 是否在短时间内创建了大量举报
    // ← 不检查 reporter 是否对同一个目标重复举报
    // ← 不检查被举报评论是否已经被其他管理员"已解决"
    // ← 不自动隐藏达到阈值（如 3 个不同用户举报）的评论
}
```

**证据 6：消息没有举报机制**

```go
// internal/message/repository.go
type MessageRepository interface {
    // ...
    // ← 无 CreateMessageReport
    // ← 无 ReportMessage
}
```

评论有举报，但消息（私信）完全没有举报能力——如果用户收到垃圾/骚扰消息，没有任何机制报告。

### 架构师视角

这是一个**随着规模线性恶化的架构缺口**。今天用户量小，人工审核足够。但：

1. 评论举报没有阈值自动隐藏 → 恶意用户在举报处理前，不当内容一直可见
2. 没有重复举报检测 → 可以耗尽审核人员的精力
3. 消息没有举报能力 → 垃圾消息/骚扰无反馈通道
4. 新用户没有信任层级 → 刚注册的账户可以立刻发布内容，没有任何缓慢提升的信任机制

推荐方向：
- **第一阶段**（最小可行）：
  - 新增 `reporter_rate_check`：同一 uid 在 N 分钟内最多创建 M 条举报
  - 新增 `auto_hide_threshold`：当同一评论收到 ≥N 个不同用户的举报时，自动将 status 设为 "屏蔽"
  - 新增 `message_reports` 表 + 举报端点
- **第二阶段**（可配置策略）：
  - 配置驱动的敏感词/模式列表（对 content 做简单的字符串/正则匹配，命中后自动标记或阻止）
  - 新用户"凉身期"：评论/消息在审核通过前仅作者可见（类似论坛的"新手隔离"）
- **第三阶段**（高级）：
  - 举报分析：识别"同一 IP 多账户连环举报"模式
  - 用机器学习/外部 API（如 Google Perspective API）做自动评分（但该阶段需要讨论是否进入 aero-id 范围）

### 产品经理视角

没有自动滥用预防，产品在增长阶段面临重大风险：

- 垃圾评论一夜之间污染评论区 → 用户体验急剧下降
- 恶意用户反复举报正常内容 → 审核团队被淹没
- 私信骚扰无举报入口 → 用户感到不安全，信任破裂

在产品早期，这三个问题的缺失不是致命的，但**一旦出现在媒体报道或用户差评中，修复成本远高于预先建设**。

### 已知受影响模块

`internal/comment/service.go` · `internal/comment/repository.go` · `internal/domain/comment.go` · `internal/message/repository.go` · `internal/message/service.go` · `internal/config/config.go` · `internal/middleware/ratelimit.go`（需分层速率限制）

---

## 总结

| # | 方向 | 类型 | 优先级 | 产品价值 | 架构价值 | 既有分析覆盖度 |
|---|------|------|--------|---------|---------|---------------|
| 1 | **Conversation 模型升级** — 群组聊天与多参与者消息 | 产品 / 架构 | **P1** | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 零覆盖（最显著的功能缺口） |
| 2 | **Webhook 投递可靠性** — 顺序、幂等与死信可观测 | 平台 / 运维 | **P1** | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | 零覆盖 |
| 3 | **内容编辑历史追踪** — 消息/评论修订版 | 合规 / 产品 | **P2** | ⭐⭐⭐ | ⭐⭐⭐ | 零覆盖（OCC 版本冲突在别处有分析，但编辑历史未覆盖） |
| 4 | **用户数据组织原语** — 标签、文件夹与批量操作 | 产品 / UX | **P2** | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ | 零覆盖 |
| 5 | **滥用预防管道** — 速率检测、自动审核与隔离 | 信任 / 安全 | **P1-P2** | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 零覆盖（纯人工审核，无任何自动化） |

**优先级说明**：
- **P1** = 高价值，建议 1-2 个 sprint 内启动
- **P2** = 高价值但可推迟至用户量/平台需求触发

**这些方向的共同特征**：它们不是"再加一个 CRUD 端点"或"再加一个存储后端"式的功能扩展，而是**系统在 MVP→平台演进中必然面临的架构空白**。每个方向在当前代码库中都有精确的代码级证据，且全部 120+ 既有分析文档中从未被作为独立方向予以完整论证。
