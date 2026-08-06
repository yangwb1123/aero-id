# aero-id — 架构扫描：五个高价值扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码库逐文件扫描（`internal/` 全 23 个包、`migrations/` 23 份 SQL、`cmd/`、`configs/`、`api/proto/`、`Makefile`），结合 `app.go` composition root 的实际 wiring 逐一验证。  
> **约束**: 不编写代码。以下方向在 `docs/requirements/` 下 **80+ 份既有分析**、`docs/REVIEW_*` 下 **8 份审查文档**、`ROADMAP.md` 全部已交付方向中，**零系统性覆盖**。  
> **核心承诺**: 每个方向提供精确的代码级证据、架构师/产品经理双视角论证，并说明为什么这是当前系统性盲区。

---

## 当前代码库成熟度定位

aero-id 是一个**高度成熟**的服务。以下能力已全部交付：

| 域 | 已交付能力 |
|----|-----------|
| **双传输** | HTTP REST + gRPC 全覆盖（38+ RPCs），共享 Service 层 |
| **文件管线** | 流上传播、SHA-256 去重、MIME/Size 校验、S3 presigned 直传、Storage GC、原子引用计数、显式分享/撤销、审计日志 |
| **消息** | 1-to-1 私信、Reactions、编辑/软删、已读回执、Inbox/Conv 视图、系统消息、事务性 Outbox |
| **评论** | 两层嵌套、Like、举报→审核→隐藏、编辑、原子 reply_count 事务 |
| **通知** | Outbox 驱动、SSE 实时推送、折叠去重、偏好（静默时段）、Block 抑制、Webhook |
| **搜索** | 跨实体 FTS（pgvector + GIN）、CJK ILIKE 回退 |
| **可靠性** | 事务性 Outbox、幂等键、限流（双传输）、SSO 断路器 + stale-cache + singleflight + 优雅关停 + gRPC Health |
| **运维** | OTel 追踪、RED 指标、per-dependency /readyz、migration CLI、冷启动 warmup |
| **安全** | 方向性 Block、审计日志、Admin 角色+权限（moderator/admin）、评论举报→处理、文件 ACL |
| **数据模型** | 23 次迁移（含性能索引、FTS 索引、通知索引、Outbox 重试、通知折叠、Storage Ref） |

然而，以下五个方向是 **系统性盲区**——它们不是修补缺陷，而是解锁新的产品域或基础设施能力。每个方向均提供精确的代码级证据说明为什么它当前不存在，以及为什么需要它。

---

## 方向一：跨实体引用完整性——孤儿内容垃圾回收（Cross-Entity Referential GC）

### 代码级证据

系统中存在**三条跨实体引用链**，当前没有任何维护机制：

**引用链 1：Comment → File / Message（目标实体）**

```go
// internal/domain/comment.go
type Comment struct {
    TargetType TargetType  // "file" | "message"
    TargetID   ID          // ← 指向外部实体的 UUID，无 FK 约束
    ...
}
```

当一个文件或消息被删除后，指向它的 Comment 全部变成孤儿——`TargetID` 指向一个已软删除或不存在的 ID。系统**不查询、不标记、不隐藏、不清理**这些孤儿评论。它们会继续出现在 `ListByTarget` 查询结果中（因为 `status = CommentStatusNormal` 且软删除只保护自身）。

**引用链 2：Message → File（附件）**

```go
// internal/domain/message.go
type Message struct {
    FileID  *ID   // ← 指向一个文件，无 FK，无级联
    ...
}
```

消息携带的文件（`MessageKindImage` / `MessageKindFile`）在文件被删除后，消息的 `FileID` 成为悬挂指针。前端拿到这个 ID 后 `Get` 文件会得到 404。

**引用链 3：Notification → Subject（主题实体）**

```go
// internal/domain/notification.go
type Notification struct {
    SubjectType string  // "message" | "comment"
    SubjectID   string  // ← 字符串形式的 ID，不是 domain.ID
    ...
}
```

当一条消息被删除后，已分发的 `message.received` 通知中的 `SubjectID` 指向一个不存在/已删除的消息。用户点击通知会看到错误页面。

### 为什么这是盲区

现有分析全部聚焦于**存储 blob GC**（`internal/storage/gc/sweeper.go`）——它只清理文件存储层的死对象，完全不处理业务语义层面的引用关系。存储 GC 清理的是 S3/local 上的二进制 blob；这里讨论的是数据库行之间的引用完整性。这是两个完全不同层次的问题。

### 架构师视角

引用完整性是**数据正确性**问题。没有这些约束，系统对外呈现的是一张失效链接网。理想情况下，每种目标类型应有对应的引用维护策略：

- **软级联**：当文件被删除时，引用该文件的 Comment 应自动标记为隐藏（`status = CommentStatusHidden`），或至少关联的 `ListByTarget` 默认过滤掉已删除目标。
- **消息附件变体**：删除文件时，引用该文件的消息应保留，但 `file_id` 应能被前端识别为"已删除"（如设置一个专门的 deleted sentinel，或消息的 `file_id` 保持但文件查询返回一个轻量的"不可用"标记）。
- **通知清除**：当引用实体被删除时，是否允许通过 outbox 发布一条"orphan:cleanup"事件来隐式标记相关通知？

### 产品经理视角

对用户而言，点击一条已删除文件的评论、或打开一个已删除文件的对话消息，是最糟糕的体验断裂。产品应该做到：

- "该评论引用的内容已被删除"——而不是空白或 404
- 批量删除文件时，应该提示用户"该文件被 N 条评论引用，这些评论也将被隐藏"
- 通知中心的"查看消息"按钮不应指向空白

### 实施思路

1. 为每个引用字段增加引用计数（类似 `storage_refs` 模式），或采用软-软模式（在目标删除时设置 `target_deleted_at` 标记位）
2. 在 `service.file.Delete` 中注入引用查询&标记逻辑
3. 或通过 outbox 发布 `file.deleted` / `message.deleted` 事件，由独立的清理 Handler 处理引用关系

---

## 方向二：每个资源的 Webhook 订阅（Per-Entity Webhook Subscriptions）

### 代码级证据

当前系统的外发事件机制**只有一个**——全局 Outbox Webhook：

```go
// internal/outbox/webhook.go
type WebhookConfig struct {
    URL        string            // ← 一个 URL，全局
    EventTypes []string          // ← 按事件类型过滤，不能按资源过滤
    ...
}
```

```go
// internal/outbox/chain.go — 全局链
func (c *Chain) Handle(ctx context.Context, e *outbox.Event) error {
    for _, h := range c.handlers {
        if err := h.Handle(ctx, e); err != nil {
            return err
        }
    }
    return nil
}
```

```go
// internal/app/app.go — wiring 中配置一次
whCfg := outbox.WebhookConfig{
    URL:        cfg.Outbox.Webhook.URL,
    ...
}
webhookHandler := outbox.NewWebhookHandler(whCfg, log)
relayHandler = outbox.NewChain(dispatcher, webhookHandler)
```

**这意味着：** 无法让外部系统只订阅"文件 X 被修改时通知我"或"评论 Y 有新回复时推给我"——Webhook 是全量的，要么全收要么按事件类型过滤。没有按 `aggregate_id` 过滤的能力。

### 为什么这是盲区

内部通知系统（`NotificationDispatcher`）虽然能做到按 UID 分发，但那是**同一个进程内**的 SSE 推送，不是供外部系统集成的 Webhook。现有 80+ 份分析文档讨论的是"通知中心 UX"、"通知折叠"、"Block 抑制"，从来没有讨论过**让外部实体订阅指定资源变更**的架构。这是一个完全不同的集成模式——它不是推给用户，而是推给第三方系统（CI/CD 流水线、外部自动化、合规审计端）。

### 架构师视角

需要一张 `resource_webhooks` 表：

| 列 | 类型 | 说明 |
|----|------|------|
| id | UUID PK | |
| owner_uid | TEXT | 谁创建了这个订阅 |
| resource_type | TEXT | "file" / "message" / "comment" |
| resource_id | TEXT | 具体实体的 ID（空 = 通配该类型下的所有） |
| event_type | TEXT | "updated" / "deleted" / "shared" / "commented" |
| target_url | TEXT | 回调 URL |
| signing_key | TEXT | HMAC 签名密钥 |
| created_at | TIMESTAMPTZ | |

每次写入操作（文件更新、消息发送、评论添加）通过 outbox 发布事件时，Relay 除了通知调度外，还应查找匹配的 `resource_webhooks` 行并触发外部回调——**这完全在现有 Outbox 框架的可扩展范围内**。

### 产品经理视角

- **协作场景**：用户 A 分享文件给用户 B，B 希望在该文件被更新时收到外部通知（如 Webhook → 企业微信 / Slack）
- **自动化场景**：开发者希望在用户上传新文件到某个目录时触发一个 CI 流水线
- **合规场景**：法务部门订阅全部 `file.deleted` 事件以维护删除日志

### 实施思路

1. 新增 `resource_webhooks` 表 + 迁移
2. 新增 CRUD 端点（HTTP + gRPC）让用户管理自己的订阅
3. 新增 `EntityWebhookHandler` 实现 `outbox.Handler` 接口，在 `NewChain` 中串联
4. 在 relay drain 时，对每个事件查询匹配的订阅并执行回调

---

## 方向三：乐观并发控制——基于版本戳的冲突检测（Optimistic Locking via Version Stamps）

### 代码级证据

系统中**没有任何实体携带版本戳**（`version` / `etag` / `row_version`）。所有更新都是"最后一次写入获胜"：

```go
// internal/repository/comment.go
func (r *commentRepo) UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error {
    res := r.db.WithContext(ctx).Model(&commentRow{}).
        Where("id = ?", id).
        Updates(map[string]any{
            "content":    content,
            "edited_at":  at,
            "updated_at": at,
        })
    // ← 没有 WHERE version = :old_version 子句
    if res.Error != nil { ... }
    if res.RowsAffected == 0 { return ErrNotFound }
    return nil
}
```

```go
// internal/file/service_crud.go — UpdateMetadata 同样无版本控制
func (s *fileService) UpdateMetadata(ctx context.Context, id domain.ID, in UpdateFileMetadataInput) (*File, error) {
    ...
    fields := map[string]any{ ... }
    fields["updated_at"] = now
    if err := s.files.UpdateMetadata(ctx, id, fields); err != nil { ... }
    ...
}
```

幂等键（`internal/idempotency`）解决了**同一个请求重试**的问题，但不解决**两个不同客户端基于过期状态同时修改**的问题。例如：

1. 用户 A 和 B 同时打开评论 C
2. A 将内容改为 "X"，B 将内容改为 "Y"
3. B 的写入在 A 之后发生，A 的修改被静默覆盖——双方都不会收到冲突警告

### 为什么这是盲区

现有 80+ 份分析讨论过"并发点赞的幂等性"、"事务中的计数器一致性"（通过 `ON CONFLICT DO NOTHING` 和 `GREATEST` 实现），但**从未讨论过对同一行数据的任意字段的并发修改的冲突检测**。这与 idempotency 是完全正交的问题——idempotency 保证客户端重试安全，versioning 保证多客户端协作安全。

### 架构师视角

需要在每个实体的数据库行中增加一个 `version INT NOT NULL DEFAULT 1` 列，所有更新操作改为：

```sql
UPDATE comments SET content = ?, version = version + 1
WHERE id = ? AND version = ?  -- 乐观锁检查
```

当 `RowsAffected == 0` 时，说明版本冲突，应返回一个 `409 Conflict`（`AppError` 已有 `Conflict` 构造函数，但在整个代码库中几乎未被用于这个场景）。

对于 `File`、`Message`、`Comment`、`Notification` 等核心实体，这是相同的模式。可以通过 GORM 钩子（`BeforeUpdate`）或公共的 `repoHelper.optimisticUpdate` 来统一实现。

### 产品经理视角

对用户编辑体验的提升：

- "该内容已被他人修改，请刷新后重试"——而不是静默覆盖
- 协作场景（如团队内部评论）中，没有人希望自己的修改被无声吃掉
- 编辑历史记录可以基于版本号建立差异比较

### 实施思路

1. 为 `files`、`messages`、`comments`、`notifications` 表增加 `version` 列（迁移 000024）
2. 在 `domain` 层增加 `Version int` 字段
3. 所有 `Update*` 方法增加 `WHERE version = ?` 子句
4. 版本冲突返回 `apperr.Conflict("entity.version_conflict", ...)`
5. 在 HTTP 响应头中增加 `ETag: <version>` 供客户端缓存使用

---

## 方向四：会话元数据与生命周期管理（Conversation Metadata & Lifecycle Management）

### 代码级证据

当前消息模型是**纯隐式会话**：

```go
// internal/domain/message.go
type Message struct {
    ID      ID
    FromUID string
    ToUID   string
    Kind    MessageKind
    Content string
    ...
}
```

```go
// internal/message/service.go — 查询是隐式对偶
func (s *service) Conversation(ctx context.Context, withUID string, page domain.Page) (*MessageList, error) {
    // ← 无会话 ID，无会话元数据，直接在 from_uid/to_uid 上查询
    messages, next, err := s.messages.ListConversation(ctx, actor, withUID, page)
    ...
}
```

```go
// internal/repository/message.go
func (r *messageRepo) ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error) {
    q := r.db.WithContext(ctx).Where(
        "(from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)",
        uidA, uidB, uidB, uidA,
    )
    // ← 没有 "会话" 实体，没有 subject，没有 pin，没有 mute，没有 archive
    ...
}
```

这意味着：

- 用户无法给自己的聊天窗口命名
- 无法固定重要会话
- 无法静音某个会话的通知
- 无法归档旧会话（只对单条消息做了软删除）
- 无法查看"会话信息"（参与者列表、创建时间、最后活跃时间）

### 为什么这是盲区

现有分析讨论过"群聊"（多参与者），那是横向扩展——让 N 个人参与同一对话。但这里的问题是**垂直缺失**——即使只有两个人聊天，也没有任何会话层元数据。`ROADMAP.md` 和所有 80+ 份文档从未触及"会话生命周期管理"这个层面。这与群聊是正交的：群聊依赖会话实体（`conversation_id`），但现有的 1-to-1 消息同样需要它。

### 架构师视角

需要新增一个 `conversations` 表：

```sql
CREATE TABLE conversations (
    id          UUID PRIMARY KEY,     -- UUIDv7
    kind        SMALLINT NOT NULL DEFAULT 1, -- 1: 1-to-1, 2: group (未来)
    subject     TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE conversation_participants (
    conversation_id UUID NOT NULL REFERENCES conversations(id),
    uid             TEXT NOT NULL,
    role            SMALLINT NOT NULL DEFAULT 1, -- 1: member, 2: admin, 3: left
    pinned          BOOLEAN NOT NULL DEFAULT false,
    muted           BOOLEAN NOT NULL DEFAULT false,
    archived_at     TIMESTAMPTZ, -- 非空 = 已归档
    last_read_at    TIMESTAMPTZ,
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (conversation_id, uid)
);
```

改造方案（向后兼容）：

- 现有消息增加 `conversation_id` 列（可空，向后兼容旧数据）
- 首次发送消息时隐式创建 `conversations` 行（`kind = 1-to-1`），并插入两个参与者的记录
- `ListConversation(ctx, withUID)` 内部改为查 `conversation_id`，而非直接查 `(from_uid, to_uid)` 对
- `Inbox` 改为列出该用户参与的所有会话（含 `last_message` 预览和 `unread_count`）

### 产品经理视角

这是**消息 UX 从原型到产品**的关键一步：

- 聊天列表应该显示"用户 B"+"最后一条消息预览"+"未读数"，而不是按时间排序的所有消息
- 用户可以置顶重要聊天、静音嘈杂群聊、归档不再关注的历史对话
- 会话级"已读"（`last_read_at`）远比逐个消息已读更实用
- 用户离开会话后不再收到通知（对应 `role = 'left'`）

### 实施思路

1. 新增 3 张表：`conversations`、`conversation_participants`、消息增加 `conversation_id` 列
2. 数据迁移：为现有所有消息对自动创建 `conversations` 行（离线脚本）
3. 新增 API：`PUT /v1/conversations/:id/pin` / `mute` / `archive` / `leave`
4. 改造现有 API：`GET /v1/inbox` 改为返回会话列表；`GET /v1/conversations/:id/messages` 替代现有的 `ListConversation`
5. 通知系统：在会话维度聚合通知（"用户 B 给你发了 N 条消息"）

---

## 方向五：数据可移植性——用户数据的批量导出/导入（Data Portability & Bulk Export）

### 代码级证据

系统管理用户的三类核心数据：文件、消息、评论。但**所有操作都是单条级别**的：

```go
// internal/service/service.go — 所有接口都是单粒度
type FileService interface {
    Create(ctx context.Context, in CreateFileInput) (*domain.File, error)  // 单文件
    Get(ctx context.Context, id domain.ID) (*domain.File, error)           // 单文件
    ...
}
```

```go
// internal/message/service.go
func (s *service) Send(ctx context.Context, in SendMessageInput) (*domain.Message, error)  // 单条
func (s *service) Get(ctx context.Context, id domain.ID) (*domain.Message, error)           // 单条
```

```go
// internal/comment/service.go
func (s *commentService) Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error)  // 单条
```

没有任何 `ExportAllData` 或 `ImportBulk` 端点。用户无法：

- 一次性下载自己所有文件的原始内容 + 元数据
- 导出所有消息记录（JSON/CSV）
- 将数据从一个 aero-id 实例迁移到另一个
- 满足数据保护法规（如 GDPR Article 20 的"数据可移植权"）

通知系统虽然有 `ListByRecipient`（分页列出所有通知），但也没有聚合导出能力。

### 为什么这是盲区

现有分析覆盖了**备份/恢复**（DB 级别）、**审计日志**（操作追踪级别）、数据生命周期合规（数据保留和删除），但**从未在用户级数据可移植性**这个维度进行系统分析。根本原因是 aero-id 的架构假设"SSO 持有账户，aero-id 持有用户数据"——两者是分离的，但 SSO 的可移植性不解决 aero-id 中的数据。

### 架构师视角

导出需要处理几个设计约束：

1. **无状态导出**：导出是重量级操作，不应阻塞请求路径。应采用异步模式：用户发起导出请求 → outbox 发布 `data.export.requested` 事件 → 后台 Handler 收集数据 → 打包 → 上传为导出文件 → 通知用户完成。
2. **分片导出**：文件的总大小可能达到 GB 级别。导出应分片（每片 ≤ 100MB），以 ZIP 格式打包，或提供逐类型导出（先导出文件清单 + 消息文本，再导出文件二进制）。
3. **导入的复杂性**：导入更难——需要重新分配 ID、处理引用冲突、验证 SSO UID 匹配。第一期不建议实现导入，仅实现导出。
4. **鉴权**：每次导出请求都需要 SSO 重新验证（非会话 token 可能已经过期）。

### 产品经理视角

数据可移植性是**用户信任**的基础设施：

- GDPR/CCPA 合规是面向消费者的应用的基础要求
- 用户在离开服务时有权利带走自己的数据
- "迁移到另一个实例"是企业客户的关键评估标准
- 用户可能只是想保存自己与某人的聊天记录——不是 full export 而是 per-conversation export

### 实施思路

1. **异步导出 API**：
   - `POST /v1/data/export` 发起导出（接受 `type_filter: ["files","messages","comments"]` 参数）
   - `GET /v1/data/export/:id/status` 查询进度
   - `GET /v1/data/export/:id/download` 下载生成的压缩包（需要签名 URL，复用 `local.go` 的 presigned 路径）

2. **导出后台 Handler**（`outbox.Handler` 实现）：
   - 接收 `data.export.requested` 事件
   - 分批查询用户的所有消息、评论元数据、文件清单
   - 文件：先导出元数据清单（JSON），再推 blob 下载 URLs（非 blob 本身）
   - 压缩为 ZIP，每个类型一个子目录

3. **通知集成**：导出完成后，通过现有通知系统推送"你的数据已准备好下载"通知

4. **第一期限定**：仅导出元数据 + 下载 URL 列表，不包含文件二进制。二进制导出作为第二期（需要存储端批量读取能力）。

---

## 总结

| # | 方向 | 类别 | 当前状态 | 为什么是高价值 |
|---|------|------|----------|---------------|
| 1 | **跨实体引用 GC** | 数据完整性 | 存在三条悬挂引用链，无任何维护 | 修复静默数据腐化，提升全链路可靠性 |
| 2 | **每个资源 Webhook** | 集成/自动化 | 只有全局 Webhook，无法按资源粒度订阅 | 解锁外部集成生态，满足自动化和合规场景 |
| 3 | **乐观锁版本戳** | 数据一致性 | 无任何版本控制，最后写入获胜 | 防止多客户端协作时的静默覆盖，关键于编辑场景 |
| 4 | **会话元数据与生命周期** | 产品能力 | 隐式会话，无名称/置顶/静默/归档 | 消息 UX 从"原型"到"产品"的必经之路 |
| 5 | **数据可移植性导出** | 合规/信任 | 只有逐条操作，无批量导出 | 满足 GDPR/CCPA 合规，建立用户信任基础 |

**建议路标**：方向三（版本戳）和方向一（引用 GC）是**基础设施正确性**问题，建议优先处理（P1）；方向四（会话元数据）和方向二（资源 Webhook）是**产品能力层**（P2）；方向五（数据可移植性）可用性依赖方向二的异步通知能力，建议最后启动（P2-P3）。
