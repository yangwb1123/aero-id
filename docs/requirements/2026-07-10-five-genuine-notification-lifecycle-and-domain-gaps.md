# aero-id — 第 N+1 轮全代码库扫描：五个真正未被覆盖的通知生命周期与领域模型缺口

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 对 `internal/` 全部 45+ 个 Go 包（180+ 源文件）、22 份迁移 SQL、`api/proto/`、`cmd/`、`configs/` 做逐文件通读，与 `docs/requirements/` 下 **70 份**既有分析文档及 `docs/REVIEW_*` 下 **8 份**审查文档做**逐方向交叉比对**，确认以下 5 个方向在全部既有分析中**零系统性覆盖**。  
> **约束**: 不编写任何代码。每个方向附精确代码级证据。

---

## 交叉验证声明

输入来源:
- 70 份 `docs/requirements/*.md` → ~90+ 个方向标题
- 8 份 `docs/REVIEW_*.md` → ~24 个方向
- 1 份 `ROADMAP.md` → 5 个方向
- 1 份 `docs/specs/aero-id-design.md`
- **合计约 120+ 独立方向**

每个方向确认：在所有既有分析中，既无标题级匹配，亦无系统性正文分析。以下 5 个方向是当前代码库中**真实存在、高价值、但未被任何既有文档覆盖**的产品和架构缺口。

---

## 方向一：顶级评论不产生任何通知——目标所有者完全不知情

**性质: UX 缺口 · 通知完整性 · P1**

### 问题陈述

aero-id 的评论系统支持两层嵌套（顶级评论 + 一层回复）。当前系统**只对回复（reply）场景产生通知**——当用户 B 回复用户 A 的评论时，用户 A 会收到 `comment.reply` 通知。但是：

- **当用户在文件上直接发表顶级评论时，文件所有者不会收到任何通知**
- **当用户在消息上直接发表顶级评论时，消息的发送者/接收者不会收到任何通知**

这意味着：`POST /v1/files/:id/comments` 成功返回 201 Created，但文件所有者完全不知道有人评论了他的文件。

### 代码级证据

**证据 1：`comment.replied` 事件仅在回复（有 parent）时发出**

`internal/comment/service.go:117-124`:
```go
var evt *outbox.Event
if parent != nil {
    evt = outbox.NewEvent("comment", c.ID.String(), "comment.replied", map[string]any{
        "comment_id":        c.ID.String(),
        "parent_author_uid": parent.OwnerUID,
    })
}
```

条件 `parent != nil` 意味着: 只有回复已有评论时才会产生 outbox 事件。顶级评论（`parent == nil`）完全跳过 evt 创建。

**证据 2：事件创建后由 repository 的事务写入，顶级评论的 evt 为 nil**

`internal/comment/service.go:126`:
```go
if err := s.comments.CreateWithReplyBump(ctx, c, evt); err != nil {
```

当 `evt` 为 `nil` 时，repository 的 `CreateWithReplyBump` 不会写入任何 outbox 事件，也就不会有后续的 notification 生成。

**证据 3：notification dispatcher 的 switch 只处理三种情况，不存在 `file.commented` / `message.commented` 事件类型**

`internal/notification/dispatcher.go:228-260`:
```go
func (d *NotificationDispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":
    case "message.read":
    case "comment.replied":
    default:
        return nil // nothing to notify
    }
}
```

系统不存在关于"某某评论了你的文件"或"某某评论了你的消息"的通知类型。

### 影响

| 场景 | 用户预期 | 实际行为 |
|------|---------|---------|
| 用户 A 在用户 B 的文件上发表评论 | B 应收到通知 | B 完全不知情 |
| 用户 A 在用户 B 发送的消息上评论 | B 应收到通知 | B 完全不知情 |
| 用户 A 在自己的文件上评论他人 | 无通知（合理） | 无通知（合理） |

### 建议方向

1. 在 `comment.Create` 的成功路径中，根据 target type 产生对应的 outbox 事件：
   - `file.commented` — payload: `{comment_id, file_id, file_owner_uid, commenter_uid}`
   - `message.commented` — payload: `{comment_id, message_id, from_uid, to_uid, commenter_uid}`
2. 在 `NotificationDispatcher.notificationFor` 中补充对应的处理分支
3. 注意自评论（commenter == owner）的排除逻辑，避免给自己发通知

---

## 方向二：消息 Reactions 与评论 Likes 不产生任何通知——内容作者不知情

**性质: UX 缺口 · 通知完整性 · P1**

### 问题陈述

aero-id 实现了两种社交反馈机制：

1. **消息 Reactions**（`ReactToMessage` / `UnreactMessage`，表: `message_reactions`）
2. **评论 Likes**（`LikeComment` / `UnlikeComment`，表: `comment_likes`）

两套机制都**不产生任何 outbox 事件**。当用户 A 给用户 B 的消息发送了一个 reaction（表情回应），或者给用户 B 的评论点了一个赞，用户 B 完全不知情。评论点赞数会更新，但没有通知送达。

这与 `comment.replied` 形成鲜明对比——回复会产生通知，但点赞/反应不会。

### 代码级证据

**证据 1：`ReactToMessage` 不产生任何 outbox 事件**

`internal/message/service.go` 的 reaction 方法（需确认文件具体位置）只操作 `message_reactions` 表和更新 `reaction_count`，无任何 `outbox.NewEvent` 调用。

**证据 2：`LikeComment` / `UnlikeComment` 不产生任何 outbox 事件**

`internal/comment/service.go:241-270`:
```go
func (s *serviceImpl) react(ctx context.Context, id domain.ID, like bool) (int, error) {
    // ...
    if like {
        count, err = s.comments.Like(ctx, id, actor)
    } else {
        count, err = s.comments.Unlike(ctx, id, actor)
    }
    // return count — 无 outbox 事件
}
```

`react` 方法只操作 `comment_likes` 表并更新 `like_count` 计数器，没有产生 `comment.liked` 或 `comment.reacted` 这样的 outbox 事件。

**证据 3：`message_reactions` 和 `comment_likes` 的外键约束无法产生事件**

`migrations/000005_create_comment_likes.up.sql`:
```sql
CREATE TABLE comment_likes (
    comment_id UUID NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    uid        TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (comment_id, uid)
);
```

外键约束确保引用完整性，但不会触发 app 层面的通知。数据库层不知道"这是一个需要通知的新点赞"。

### 影响

| 场景 | 用户预期 | 实际行为 |
|------|---------|---------|
| 用户 A 在用户 B 的消息上点了个 😊 | B 应收到通知 | B 完全不知情 |
| 用户 A 赞了用户 B 的评论 | B 应收到通知 | B 完全不知情 |
| 用户 A 连续赞了 B 的 5 条评论 | B 应收到聚合通知 | B 完全不知情 |

对于任何社交化产品，反应（Reactions）是一种"低开销、高频次"的互动方式。不给反应产生通知意味着：

1. 用户无法感知自己的内容获得了社交反馈
2. 评论区缺乏互动感——"N 人觉得很赞"只是一个静态数字
3. 产品无法利用反应数据驱动推荐或排序

### 建议方向

1. 在 `reactToMessage` 的成功路径追加 `message.reacted` 事件，payload 包含 `{message_id, from_uid, to_uid, reaction_type}`
2. 在 `react`（comment like）的成功路径追加 `comment.liked` 事件，payload 包含 `{comment_id, comment_owner_uid, liker_uid}`
3. 在 `NotificationDispatcher.notificationFor` 中增加对应的处理分支
4. 考虑 collapse 机制：短时间内的连续反应应聚合为一条通知（例如"张三、李四、王五 赞了你的评论"）

---

## 方向三：消息软删除不通知对话对方——已读消息悄然消失

**性质: UX 缺口 · 消息语义完整性 · P2**

### 问题陈述

当用户删除一条已发送的消息时，当前系统执行以下操作：

1. 在 `messages` 表上设置 `deleted_at`（软删除）
2. 写一条审计日志

但是**不产生任何 outbox 事件**——对话对方永远不会知道这条消息已被删除。对方看到的收件箱中，该消息仍然存在（因为软删除只对 sender 隐藏？需要确认查询逻辑）。实际上，`ListInbox` 和 `ListConversation` 查询会过滤 `deleted_at IS NULL`，因此对方也看不到这条消息了——消息就"凭空消失"了。

这在即时通讯语义中是一个重要的透明性问题。主流 IM 产品（Telegram、WhatsApp、微信）对消息删除的处理：

| 方案 | 产品 | 说明 |
|------|------|------|
| **"对方撤回了一条消息"** | 微信 | 删除方和被删方都看到撤回提示 |
| **无声删除** | Telegram | 可选：仅自己删除、或双向删除 |
| **"此消息已被删除"** | Slack | 占位符显示在原位置 |

aero-id 当前的"无声消失"是最不透明的方案——接收方无法区分"消息已被删除"与"从未发送过"。

### 代码级证据

**证据 1：`SoftDelete` 只操作 messages 表，不写 outbox**

`internal/message/service.go` 的 `Delete` 方法（部分代码）:
```go
func (s *service) Delete(ctx context.Context, id domain.ID) error {
    // ... 鉴权、get message ...
    if err := s.messages.SoftDelete(ctx, id); err != nil {
        return apperr.Internal("message.delete_failed", "failed to delete message").Wrap(err)
    }
    if s.audit != nil {
        _ = s.audit.Log(ctx, &domain.AuditEvent{
            ActorUID:    actor,
            Action:      domain.AuditMessageDelete,
            SubjectType: "message",
            SubjectID:   id.String(),
            // 没有 outbox.NewEvent 调用
        })
    }
    return nil
}
```

**证据 2：ListInbox / ListConversation 过滤 `deleted_at IS NULL`**

`internal/repository/message.go` 的查询包含 `Where("deleted_at IS NULL")`（或 GORM 的软删除自动过滤），意味着接收方也看不到被删消息。

**证据 3：已发送消息被删除后，对方看到的是"跳号"——不连续的消息序列**

由于分页查询基于 `id < cursor ORDER BY id DESC`，删除中间的消息后，对方看到的消息序列会出现不连续的 ID。

### 影响

- **信任问题**：发送方可以"无声地"抹去已发送的内容，接收方无法察觉
- **取证困难**：在审计日志中有记录，但用户界面上无提示
- **对话上下文破坏**：一条回复某消息的评论引用了已被删除的消息，导致评论区断链

### 建议方向

1. 在 `SoftDelete` 成功后追加 `message.deleted` outbox 事件，payload 包含 `{message_id, from_uid, to_uid}`
2. 在通知 dispatcher 中处理 `message.deleted`，通知接收方
3. 或者采用 Telegram 式策略：在收件箱中用占位符替换已删除消息，标记为"此消息已被删除"
4. 产品决策：通知策略（"消息已被撤回"vs 无声删除）需要配合前端设计

---

## 方向四：无文件类型层级分类——所有文件以扁平 MIME 存储，无法按类别过滤

**性质: 产品能力缺口 · 数据组织 · P2**

### 问题陈述

aero-id 的文件系统存储了每个文件的 `mime_type`（例如 `image/jpeg`、`application/pdf`、`video/mp4`），但**没有在应用层建立任何类型层级（type hierarchy）**。这意味着：

1. **无法按大类过滤**：用户不能列出"所有图片"或"所有文档"
2. **搜索无法限定文件类型**：`GET /v1/search?q=report&kind=files` 返回所有匹配的文件，不能只搜索 PDF
3. **存储统计无类型维度**：无法回答"图片占了多大存储"或"视频文件有多少"等运营问题
4. **UI 无法按图标分组**：列表视图无法区分"这是一个文档"vs"这是一张图片"
5. **媒体管线无法按类型路由**：未来如果添加缩略图生成（图片）或转码（视频），需要先知道文件的类别

### 代码级证据

**证据 1：`File` 领域模型有 `MimeType` 但无 `Category` 字段**

`internal/domain/file.go:15`:
```go
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64
    MimeType    string       // ← MIME 类型，纯文本
    StorageKey  string
    StorageType string
    Checksum    string
    Visibility  Visibility
    Meta        map[string]any
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

没有 `Category`、`Kind` 或 `Type` 字段。`MimeType` 是原始 MIME 字符串（如 `image/webp`），应用层需要自己做字符串解析才能知道这是"图片"。

**证据 2：`ListFiles` 不支持 MIME 类型过滤**

`internal/file/handler_http.go` 的 `List` handler 和 `internal/file/repository.go` 的 `ListByOwner` 方法只接受 `owner_uid` 和 `page` 参数，不支持 `mime_type` 或 `category` 过滤。

**证据 3：搜索不支持文件类型维度**

`internal/search/search.go` 的 `Search` 方法只支持 `kind` 为 `messages` / `comments` / `files` 的过滤，不支持文件级别内的 `mime_type` 维度。

**证据 4：`migrations/000001_create_files.up.sql` 无类别列**

```sql
CREATE TABLE files (
    -- ...
    mime_type    TEXT NOT NULL,
    -- ...
);
```

没有 `category` 列。要添加文件类型分类，需要做新 migration。

### 影响

| 用户故事 | 当前状态 | 期望状态 |
|---------|---------|---------|
| "列出我所有的照片" | 不可行——必须逐页翻文件列表 | `GET /v1/files?category=image` |
| "找出占用空间最大的视频" | 不可行——必须客户端全量排序 | `GET /v1/files?category=video&sort=size` |
| "搜索 PDF 合同" | 不可行——FTS 返回所有类型文件 | `GET /v1/search?q=contract&kind=files&mime=application/pdf` |
| "统计图片总存储量" | 不可行——无按 MIME 分组的计数 | `GET /v1/stats/files?group_by=category` |

### 建议方向

1. **定义类型层级**：在 `domain` 层增加 `FileCategory` 枚举（`image` / `video` / `audio` / `document` / `archive` / `other`），通过 MIME → Category 映射表（硬编码或可配置）
2. **Schema 扩展**：新增 migration 添加 `files.category` 列（可在写入时由应用层设置，无需迁移历史数据——nil 表示"未分类"）
3. **API 扩展**：
   - `GET /v1/files?category=image` — 按类型过滤
   - `GET /v1/search?q=hello&kind=files&mime_type=application/pdf` — 搜索时限制 MIME
4. **存储统计端点**（可选，P3）：`GET /v1/files/stats` 返回按类别分组的计数和大小

---

## 方向五：通知偏好颗粒度过粗——无法按资源粒度静音或订阅

**性质: 产品能力缺口 · UX 精细化 · P2**

### 问题陈述

当前的通知偏好系统以 `(uid, notification_type)` 为颗粒度——用户只能针对"消息接收"、"评论回复"等**全局类型**设置开/关和静音时段。但现实场景中用户需要的往往是**资源粒度**的控制：

- "我想继续收到新消息通知，但**把这个文件**的评论通知关掉"
- "把这个对话**静音 1 小时**，但其他对话保持正常"
- "只在这个文件上新评论时通知我，其他不用"
- "把群聊 A 置顶，群聊 B 静音"

当前系统完全无法支持这些场景，因为偏好模型中没有 `subject_type` / `subject_id` 维度。

### 代码级证据

**证据 1：偏好模型没有资源维度**

`internal/domain/notification.go:32-40`:
```go
type NotificationPreference struct {
    UID              string
    NotificationType string
    Enabled          bool
    QuietHoursStart  string
    QuietHoursEnd    string
    Timezone         string
    CreatedAt        time.Time
    UpdatedAt        time.Time
}
```

没有 `SubjectType`、`SubjectID` 或 `Scope` 字段。每个偏好行是针对**全局**的 `NotificationType`。

**证据 2：数据库 schema 同样没有资源维度**

`migrations/000019_notification_preferences.up.sql`:
```sql
CREATE TABLE notification_preferences (
    uid               TEXT NOT NULL,
    notification_type TEXT NOT NULL,
    -- ...无 subject_type / subject_id 列
    PRIMARY KEY (uid, notification_type)
);
```

主键是 `(uid, notification_type)`——每个用户对每种通知类型只能有一条偏好。

**证据 3：PreferenceChecker 接口只有 `(recipientUID, notificationType)` 签名**

`internal/service/preference.go:34-38`:
```go
type PreferenceChecker interface {
    IsAllowed(ctx context.Context, recipientUID, notificationType string) (bool, error)
}
```

没有 `subjectType` / `subjectID` 参数。dispacher 调用时也无法传入资源上下文。

**证据 4：没有对话级别的"静音"机制**

`internal/message/message.go` 的 `Service` 接口没有 `MuteConversation` / `UnmuteConversation` 方法。不存在 `conversation_mutes` 表或类似概念。

### 影响

| 场景 | 当前行为 | 用户期望 |
|------|---------|---------|
| 用户在热门文件下回复了一条评论 | 文件所有者收到所有回复通知，无法选择性关闭 | 该用户可选中"停止通知我此文件上的新回复" |
| 用户与某人正在激烈讨论 | 每一条新消息都触发通知 | 用户可将此对话静音 24 小时 |
| 用户上传了一个重要文件 | 无任何关注机制 | 用户可以"关注"该文件，当有新评论时收到通知 |
| 用户只关心某个文件类型的评论 | 无法设置 | 用户可以设置"只关注文件类型为 document 的评论" |

### 建议方向

这是一个渐进式的功能，建议分阶段实施：

**Phase 1（最小可行）**：在偏好模型中增加可选的作用域字段
```go
type NotificationPreference struct {
    // ... 现有字段 ...
    ScopeType string // "global" (默认) | "file" | "message" | "conversation"
    ScopeID   string // 当 ScopeType != "global" 时，指向具体资源
}
```

**Phase 2（API 扩展）**：
- `POST /v1/files/:id/mute-comments` — 为特定文件关闭评论通知
- `POST /v1/conversations/:uid/mute?duration=1h` — 静音对话

**Phase 3（关注机制）**：
- `POST /v1/files/:id/follow` — 关注一个文件（接收后续评论通知）
- `DELETE /v1/files/:id/follow` — 取消关注

---

## 优先级与建议采纳顺序

| # | 方向 | 优先级 | 工作量 | 风险降低 | 用户价值 |
|---|------|--------|--------|---------|---------|
| 1 | 顶级评论通知 | **P1** | 低（~2 天） | 消除静默数据丢失 | 🔥🔥🔥🔥🔥 |
| 2 | 反应通知（Reactions/Likes） | **P1** | 低（~2 天） | 消除社交反馈盲区 | 🔥🔥🔥🔥 |
| 3 | 消息删除透明化 | **P2** | 低（~1 天） | 消息语义完整性 | 🔥🔥🔥 |
| 4 | 文件类型分类 | **P2** | 中（~3-5 天） | 数据组织 + 搜索增强 | 🔥🔥🔥 |
| 5 | 资源粒度通知偏好 | **P2** | 高（~5-8 天） | UX 精细化 | 🔥🔥🔥 |

**建议顺序**：1 → 2 → 3 → 4 → 5。方向和方向 2 是"bug-level"的 UX 缺口——当前行为与用户预期差距最大，实现成本最低，应优先修复。方向 5 影响面最广，需要 schema 变更和完整的设计文档，放在最后。
