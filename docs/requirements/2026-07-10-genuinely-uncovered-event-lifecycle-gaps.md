# aero-id — 全局扫描后的五个高价值扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 逐文件扫描 `internal/` 全部包、`migrations/` 全部 22 个迁移、`api/proto/`、`cmd/`、`configs/`，提取代码级证据。与 `docs/requirements/` 下既有 ~40 份分析文档（累计 ~150+ 个方向标题）逐条比对，确保以下 5 个方向**未被系统性覆盖**。
> **承诺**: 每个方向附带具体代码位置 + 行号 + 关键逻辑，不写 wishlist。
> **不编写任何代码**。

---

## 前置：当前代码成熟度

aero-id 已交付远超典型 MVP 的能力：

| 维度 | 已交付 | 未覆盖 |
|------|--------|--------|
| 存储 | 双后端、流上传、直传预签名、内容去重、MIME 校验、GC 回收 | **无事件生命周期** |
| 消息 | 全 CRUD、Reactions、编辑、软删除、已读回执、对话查询、FTS 搜索 | **Reactions 无事件、无通知** |
| 评论 | 两层嵌套、Like/Unlike、举报/审核/隐藏、编辑 | **点赞无通知、回复通知缺发送者身份** |
| 通知 | Outbox 驱动、偏好设置（静默时段）、合并去重、SSE 推送 | **缺外带渠道、缺 Reactions/点赞通知** |
| 搜索 | PostgreSQL FTS（tsvector + ILIKE 回退）、三实体联合搜索 | **limit 语义不精确** |
| 可靠性 | 事务性 Outbox、幂等性 Key、限流、SSO 断路器、断线重连 SSE | **Read 端点无缓存控制** |
| 运维 | RED 指标、OTel 追踪、优雅关闭、自动迁移、健康检查 | — |

`make test && make lint` 绿灯。以下 5 个方向是完全扫描后发现的高价值但未被系统性分析的方向。

---

## 方向一：Message Reactions 缺少事件生命周期

### 重要性

**高 · 用户可见的 UX 断层**

### 问题描述

`ReactToMessage` 和 `UnreactMessage` 是 aero-id 中**唯一**产生用户可见域副作用但不发射 outbox 事件、不产生通知、不触发 SSE 推送的操作。

相比之下：
- 发送消息 → `message.sent` 事件 → 通知 (`message.received`) + SSE 推送 ✅
- 标记已读 → `message.read` 事件 → 通知 (`message.read_receipt`) + SSE 推送 ✅
- 评论回复 → `comment.replied` 事件 → 通知 (`comment.reply`) + SSE 推送 ✅
- **消息 Reaction** → ❌ **完全不产生任何事件** ❌

这意味着当 Alice 对 Bob 的消息点上"👍"时：
1. Bob 不会收到任何通知（无论是 in-app 还是 SSE 实时）
2. 外部系统的 webhook 无法感知此事件
3. 审计日志中没有反应记录

### 代码级证据

**证据 1：MessageService.ReactToMessage 无事件发射**

```go
// internal/service/message.go:~298-310
func (s *messageService) ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error {
    m, err := s.messages.Get(ctx, messageID)
    // ... authorization ...
    if err := s.messages.ReactToMessage(ctx, messageID, actorUID); err != nil {
        // ...
    }
    return nil
    // ← 没有 newEvent() 调用
    // ← 没有事件发射
    // ← 与 Send() 和 MarkRead() 不同
}
```

对比 `Send`（~70 行）：
```go
evt := newEvent("message", m.ID.String(), "message.sent", map[string]any{...})
```
和 `MarkRead`（~200 行）：
```go
evt := newEvent("message", m.ID.String(), "message.read", map[string]any{...})
```

**证据 2：Repository 层也是纯 INSERT**

```go
// internal/repository/message.go:ReactToMessage
func (r *messageRepo) ReactToMessage(ctx context.Context, messageID uuid.UUID, uid string) error {
    // 只是检查消息存在 + INSERT reaction 行
    // 没有事务性 outbox append
    res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
        Create(&messageReactionRow{...})
    return nil
}
```

**证据 3：Dispatcher 无对应 handler**

```go
// internal/service/notification.go:notificationFor()
func (d *NotificationDispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":      // ✅ 处理
    case "message.read":      // ✅ 处理
    case "comment.replied":   // ✅ 处理
    // ← "message.reacted" 不存在
    }
}
```

### 设计建议

1. 在 `ReactToMessage` / `UnreactMessage` 中发射 `"message.reacted"` 事件
2. Dispatcher 新增 `notificationMessageReacted()` handler，向对话另一方推送通知
3. 注意：Reaction 是高频率操作，应当支持 collapsing（5 分钟内合并多个 reaction 为一个通知："Alice 和另外 3 人对你的消息作出反应"）

---

## 方向二：Comment Reply 通知缺少回复者身份

### 重要性

**中 · 通知数据不完整**

### 问题描述

当用户回复一条评论时，被回复者会收到一条 `"comment.reply"` 通知——但这条通知**不携带回复者的身份信息**（`actor_uid` / `replier_uid`）。

领域模型和 dispatcher 为 `replier_uid` 定义了字段，但 producer（`commentService.Create`）**从未填充它**。这意味着通知接收方只知道"有一条新回复"，但不知道是谁回复的——直到他们主动打开 app 查看。

### 代码级证据

**证据 1：Payload 类型定义了 replier_uid 字段**

```go
// internal/service/notification.go:~285
type commentRepliedPayload struct {
    CommentID       string `json:"comment_id"`
    ParentAuthorUID string `json:"parent_author_uid"`
    ReplierUID      string `json:"replier_uid"` // 已定义
}
```

**证据 2：Producer 不填充**

```go
// internal/service/comment.go:~75
evt = newEvent("comment", c.ID.String(), "comment.replied", map[string]any{
    "comment_id":        c.ID.String(),
    "parent_author_uid": parent.OwnerUID,
    // ← replier_uid 不在此处
    // ← 消费者永远拿不到回复者身份
})
```

**证据 3：Dispatcher 中 ActorUID 为空**

```go
// internal/service/notification.go:~320
func (d *NotificationDispatcher) notificationCommentReplied(e *outbox.Event) *domain.Notification {
    // ...
    return &domain.Notification{
        // ...
        ActorUID: p.ReplierUID, // 永远是空字符串
    }
}
```

### 设计建议

在 `commentService.Create` 的 `newEvent` 调用中加入 `"replier_uid": actor`。这只需增加一个 key-value pair。

---

## 方向三：Comment Like/Unlike 完全无通知链路

### 重要性

**中 · 社交交互 UX 缺失**

### 问题描述

用户可以对评论点赞（Like）和取消点赞（Unlike），这些操作：
1. 更新 `like_count` 整数
2. 写入/删除 `comment_likes` 表

但**完全不产生任何事件**。被点赞的评论作者永远不会知道有人给自己的评论点了赞。这与回复通知（方向二）形成鲜明对比——回复有通知，点赞没有。

这是一个社交产品的核心交互不对称：`like` 是比 `reply` 更低门槛的表达，但前者完全无声。

### 代码级证据

**证据 1：Service.Like/Unlike 没有事件**

```go
// internal/comment/service.go:react()
func (s *commentService) react(ctx context.Context, id domain.ID, like bool) (int, error) {
    // ... authorization + validation ...
    if like {
        count, err = s.comments.Like(ctx, id, actor) // 纯 DB 操作
    } else {
        count, err = s.comments.Unlike(ctx, id, actor)
    }
    // ← 没有 newEvent()
    // ← 没有通知
}
```

**证据 2：Repository 层只操作 DB**

```go
// internal/repository/comment.go:Like()
func (r *commentRepo) Like(ctx context.Context, commentID domain.ID, uid string) (int, error) {
    // 事务：INSERT comment_likes + UPDATE like_count
    // ← 没有 outbox.Append
}
```

**证据 3：Outbox 事件类型中不存在 "comment.liked"**

通览 `internal/service/notification.go` 中 `notificationFor()` 的 switch 分支，只有 `message.sent`、`message.read`、`comment.replied`。不存在 `comment.liked` 或 `comment.unliked`。

### 设计建议

1. 在 `commentService.react()` 中发射 `"comment.liked"` 事件（含 `comment_id`、`liker_uid`、`like` bool）
2. Dispatcher 新增 handler：当为用户自身点赞时不通知；其他人点赞时通知
3. 建议 collapsing：如同消息 reactions，评论点赞也应当合并（"Alice 和另外 5 人赞了你的评论"）

---

## 方向四：所有 GET 读取端点缺少缓存控制（Cache Control）

### 重要性

**中 · 性能与客户端效率**

### 问题描述

aero-id 的所有 GET 读取端点——文件元数据、消息详情、评论列表、通知列表、收件箱、对话记录——返回内容时**完全没有** `ETag`、`Last-Modified` 或 `Cache-Control` 响应头。

这导致：
1. 客户端无法做条件请求（If-None-Match / If-Modified-Since），每次都必须重新请求完整数据
2. 前端/移动端不得不自己实现缓存策略，或不断轮询
3. 对频繁刷新的场景（通知未读数、收件箱列表），服务端负载比理论上限高 N 倍
4. 即使在写操作极少的数据上（如文件元数据），客户端也无法利用浏览器/CDN 缓存

### 代码级证据

**证据 1：HTTP Handler 无缓存头设置**

所有 `internal/handler/http/*.go` 文件中的 GET handler——例如：

```go
// internal/handler/http/file.go (~50 行)
func (h *FileHandler) Get(c *gin.Context) {
    // ... 解析 ID + 调用 service ...
    httpx.OK(c, gin.H{"file": ...})
    // ← 没有 c.Header("Cache-Control", ...)
    // ← 没有 c.Header("ETag", ...)
}
```

```go
// internal/handler/http/message.go — Inbox handler
// ← 同样无缓存控制
```

```go
// internal/handler/http/notification.go — List handler
// ← 同样无缓存控制
```

**证据 2：httpx 工具函数无缓存基础设施**

```go
// internal/pkg/httpx/httpx.go
func OK(c *gin.Context, data gin.H) {
    c.JSON(http.StatusOK, data)
    // ← 没有设置任何缓存相关头
}
```

**证据 3：gRPC handler 也缺乏类似机制**

gRPC 虽然没有 HTTP 缓存协议，但对于高频读取的 Unary RPC（`GetFile`、`GetMessage`、`Inbox`、`ListNotifications`），也没有实现服务端 ETag 比较逻辑（即使 proto 中不直接支持 HTTP 缓存，服务端可以提前返回 cached response 减少计算量）。

### 设计建议

| 端点 | 建议策略 | 理由 |
|------|---------|------|
| `GET /v1/files/:id` | `ETag: sha256(updated_at + checksum)`，`Cache-Control: private, max-age=60` | 文件元数据变更不频繁 |
| `GET /v1/messages/:id` | `Last-Modified: created_at`，`Cache-Control: private, no-cache` | 消息内容不变，但已读状态可能变 |
| `GET /v1/notifications` | `Cache-Control: private, max-age=0`（强制 revalidate）+ `ETag: hash(last_notification_id)` | 高频率轮询端点，减少 payload |
| `GET /v1/conversations/:uid` | `Cache-Control: private, max-age=10` | 高频轮询 |
| `GET /v1/messages?unread=true` | `Cache-Control: private, max-age=5` | 未读数常被轮询 |

还可以引入公共的 `CachePolicy` 中间件，根据路由自动注入适当的缓存头。

---

## 方向五：File 域操作完全在 Outbox 事件生态系统之外

### 重要性

**高 · 扩展性与集成瓶颈**

### 问题描述

所有文件域的操作——上传、删除、分享、取消分享、元数据更新——**完全不产生 outbox 事件**。这使得：

1. 外部系统通过 webhook 无法感知文件变更（看不到文件上传、删除、分享事件）
2. 无法在文件操作上触发下游处理管线（如上传后自动生成缩略图、病毒扫描、转码）
3. 文件域无法从 aero-id 自身的通知/SSE 生态获益（如"Alice 与你分享了一个文件"的通知）
4. 无法构建文件事件的审计流（现有审计日志是直接写入 `audit_events` 表，与 outbox 分离）

相比之下，消息域的 `Send` → `message.sent` → 通知 + SSE + webhook，评论域的 `Create`（含 parent） → `comment.replied` → 通知 + SSE + webhook。文件域是 event ecosystem 的空白区。

### 代码级证据

**证据 1：FileService 中无任何 newEvent 调用**

全局搜索 `newEvent` 的调用位置：

```
internal/service/message.go:      newEvent("message",  ...)  // ✅ Send, MarkRead
internal/service/comment.go:      newEvent("comment",  ...)  // ✅ Create (reply)
internal/service/*file*.go:       // ← 没有 newEvent 调用
```

验证：`internal/service/file.go`、`internal/service/file_share.go`、`internal/service/file_presign.go` 全文无 `newEvent`。

**证据 2：Dispatcher 也没有文件相关的事件类型**

```go
// internal/service/notification.go:notificationFor()
switch e.EventType {
case "message.sent":      // ✅
case "message.read":      // ✅
case "comment.replied":   // ✅
// ← 没有 "file.created"、"file.deleted"、"file.shared" 等
}
```

**证据 3：文件分享的 UX 不对称**

当前文件分享（`ShareFile`）只操作 `file_shares` 表 + 写审计日志。被分享者**不会收到任何通知**——既没有 in-app 通知，也没有 SSE 实时推送，必须手动调用 `ListSharedWithMe` 才能发现。

**证据 4：审计日志与 outbox 分离**

文件操作使用独立的 `AuditLogger` 写入 `audit_events` 表，与事务性 outbox 平行——这意味着：
- 外部 webhook 无法收到文件事件（webhook 只从 outbox 读取）
- 审计事件与域事件处于不同的可靠性级别（outbox 有重试 + 死信，audit 无重试）

### 设计建议

1. 在 `FileService.Create` 中发射 `"file.created"` 事件
2. 在 `FileService.Delete` 中发射 `"file.deleted"` 事件
3. 在 `fileService.ShareFile` 中发射 `"file.shared"` 事件（含 grantee uid）
4. 在 `fileService.UpdateMetadata` 中发射 `"file.updated"` 事件
5. Dispatcher 新增：
   - `"file.shared"` → 通知被分享者（"Alice 与你分享了一个文件"）
   - `"file.created"` / `"file.deleted"` → 可选择输出到 webhook 供外部系统使用
6. 注意：文件上传是高频操作，`"file.created"` 事件应关闭通知创建（不骚扰用户），但打开 webhook 输出，让外部集成管线监听。

---

## 总结：优先级与依赖关系

| 方向 | 影响范围 | 代码变更量 | 建议优先级 |
|------|---------|-----------|-----------|
| 一：Message Reactions 添加事件生命周期 | 用户通知 UX | 低（~30 行 + 新事件类型） | P1 |
| 二：Comment Reply 补全 replier_uid | 通知数据完整性 | 极低（+1 key） | P1（随方向一一起） |
| 三：Comment Like 添加通知链路 | 用户社交 UX | 低（~40 行 + 新事件类型 + collapsing） | P2 |
| 四：GET 端点增加缓存控制 | 性能/带宽 | 中（中间件 + 各 handler 调整） | P2 |
| 五：File 域接入 Outbox 事件生态 | 架构扩展性/集成 | 中-高（4 个事件类型 + dispatcher + 通知） | P2 |

**依赖关系**：方向一的 Infrastructure（事件类型定义 + Dispatcher 模式）可直接复用于方向三和方向五。建议从方向一+二开始，作为进入 Outbox 事件生态的最小成本增量。

---

## 附录：与既有分析文档的对比验证

以下方向主题在 `docs/requirements/` 所有 ~40 份既有分析中**未被系统性覆盖**（逐条验证）：

| 本文方向 | 既有文档中相似的标题 | 差异 |
|---------|---------------------|------|
| 一：Message Reactions 无事件 | `多表情反应体系`（deep-extension-analysis.md） | 前者关注"增加更多表情类型"，未分析事件生命周期缺失 |
| 二：Comment Reply 缺 replier_uid | `文件分享事件无通知链路`（genuine-remaining-gaps.md） | 后者不同实体；本文是回复通知的数据完整性缺陷 |
| 三：Comment Like 无通知 | `方向一：内容引用与转发`（genuinely-uncovered-directions.md） | 不同功能 |
| 四：GET 缓存控制 | `方向五：缓存策略与条件请求`（production-ux-optimization.md） | 后者主要关注文件下载管线（Range/ETag/CDN），本文关注读取 API 的条件缓存 |
| 五：File 域无事件 | `方向二：文件分享事件无通知链路`（genuine-remaining-gaps.md） | 后者仅覆盖分享一个操作；本文覆盖全部文件操作的事件缺失 |
