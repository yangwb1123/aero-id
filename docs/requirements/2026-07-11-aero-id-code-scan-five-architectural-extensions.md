# aero-id — 全量代码扫描：五个已实现面上尚未触及的架构扩展方向

> **日期:** 2026-07-11
> **视角:** 资深架构师 / 产品经理
> **方法:** 对 `main` 分支（不含 `.claude/worktrees`）全部 150+ 源文件做逐层通读，覆盖 `internal/` 全部 20+ 子包、`migrations/` 全部 23 份 SQL、`cmd/`、`configs/`、`api/proto/`。与前序 85+ 份 `docs/requirements/` 分析文档、`ROADMAP.md`、`docs/CHANGELOG.md` 做交叉比对，确保以下 5 个方向在全部既有材料中**零系统性覆盖**。

---

## 当前代码库成熟度

aero-id 已是一个高度成熟的生产级服务平台。以下能力已全部投产：

| 域 | 已交付能力 |
|----|-----------|
| **双传输** | HTTP REST + gRPC 全覆盖（38+ RPCs），共享 Service 层 |
| **文件管线** | 流上传播、SHA-256 去重、MIME/Size 校验、S3 presigned 直传、Storage GC、原子引用计数、显式分享/撤销、审计日志 |
| **消息** | 1-to-1 私信、Reactions（二元）、编辑/软删、已读回执、Inbox/Conv 视图、系统消息、事务性 Outbox |
| **评论** | 两层嵌套、Like/Unlike、举报→审核→隐藏、编辑、原子 reply_count 事务 |
| **通知** | Outbox 驱动、SSE 实时推送、折叠去重、偏好（静默时段）、Block 抑制、Webhook |
| **搜索** | 跨实体 FTS（PG tsvector + GIN）、CJK ILIKE 回退、多 kind 联合搜索 |
| **可靠性** | 事务性 Outbox、幂等键、限流（双传输）、SSO 断路器 + stale-cache + singleflight + 优雅关停 + gRPC Health |
| **运维** | OTel 追踪、RED 指标、per-dependency /readyz、migration CLI、冷启动 warmup、死信支持 |
| **安全** | 方向性 Block、审计日志、Admin 角色+权限（content.moderate/audit.read/user.ban/system.config）、评论举报→处理、文件 ACL |
| **基础设施** | 本地/S3 双存储后端、存储引用计数 GC、消息/评论 reactions（message_reactions + comment_likes 表）、Webhook 签名投递 |

然而，以下 5 个方向在**全部 85+ 份既有分析**中均未被作为独立方向提出。每个方向均提供精确的代码级证据。

---

## 方向一：多类型 Reactions 系统 — 从二元 Like 到富媒体表情回应

### 现有实现

当前 reactions 模型是严格二元的：

**消息 Reactions：** `messageReactionRow` 仅包含 `(MessageID, UID, CreatedAt)`，无反应类型字段。

```go
// internal/repository/db.go:229-232
type messageReactionRow struct {
    MessageID uuid.UUID `gorm:"column:message_id;primaryKey"`
    UID       string    `gorm:"column:uid;primaryKey"`
    CreatedAt time.Time `gorm:"column:created_at"`
}
```

`ReactToMessage` 只做 upsert（`ON CONFLICT DO NOTHING`），`UnreactMessage` 删除行。**不存在"对同一条消息用多种表情回应"的概念。**

**评论 Like：** 同构——`comment_likes` 表也是 `(CommentID, UID)` 二元模型，`LikeCount` 是整数字段。

```go
// internal/domain/comment.go:18
LikeCount  int
```

Service 层的 `ReactToMessage`/`UnreactMessage` 没有 `reactionType` 参数：

```go
// internal/message/message.go:42-43
ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error
UnreactMessage(ctx context.Context, messageID domain.ID, actorUID string) error
```

HTTP handler 路径也无反应类型：

```go
// internal/handler/http/message.go:172-188
func (h *MessageHandler) ReactToMessage(c *gin.Context) { ... }
func (h *MessageHandler) UnreactMessage(c *gin.Context) { ... }
```

### 为什么需要

1. **产品差距。** 2026 年的即时通讯和社交平台（Slack、Discord、Telegram、微信）全部支持多表情回应。二元 Like/Unlike 是根本性的用户体验不足。
2. **架构可扩展。** 现有 `message_reactions` 表只需增加一个 `emoji` 列（`VARCHAR(32)`），复合主键变为 `(message_id, uid, emoji)`。迁移成本极低。
3. **无需改动 domain 事件模型。** reactions 的变更不需要 outbox 事件参与，与通知解耦。

### 建议方案

```
message_reactions 表变更:
  当前: (message_id PK, uid PK, created_at)
  目标: (message_id PK, uid PK, emoji PK, created_at)

comment_likes 表迁移:
  当前: (comment_id PK, uid PK, created_at)
  目标: (comment_id PK, uid PK, emoji PK, created_at) — 或保留为单独的 likes 表

新增 domain 类型:
  ReactionType string — 如 "👍", "❤️", "😂", "😮", "😢", "🙏"

Service 接口变更:
  ReactToMessage(ctx, messageID, actorUID, emoji string) error
  UnreactMessage(ctx, messageID, actorUID, emoji string) error
  → 同时保留无 emoji 的重载（默认 ❤️ 或 👍）以保持向后兼容

新增 APIs:
  GET /v1/messages/:id/reactions → 按 emoji 分组计数
  GET /v1/comments/:id/reactions → 同上（comment 也支持多反应）
```

### 边界情况

- **表情枚举 vs 自由输入：** 建议定义允许的 emoji 集合（6-12 个常用），而非允许任意 Unicode 字符，以避免滥用和存储膨胀
- **每个用户每种表情一个：** 复合主键天然防止重复
- **Reaction 通知风暴：** 当一条消息获得 100 个 😂 时，是否向发送者推送 100 条通知？建议走折叠逻辑（同现有 `WithDispatcherCollapse`）
- **后向兼容：** 现有的 `ReactToMessage`（无 emoji）可等价于 `emoji = "👍"`，迁移脚本补全默认值
- **计数缓存一致性：** 当前 `LikeCount` 列在 domain 层、DB 行中都有，分散存储。需确保原子更新或废弃该列，改为实时 count

---

## 方向二：不完备的 Outbox 事件表面 — 已存在域操作但未发布事件

### 现有实现

`notification/dispatcher.go` 的 `Handle` 方法仅处理 **3 种** outbox 事件类型：

```go
// internal/notification/dispatcher.go:56-62
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    switch e.EventType {
    case "message.sent":
    case "message.read":
    case "comment.replied":
    default:
        return nil // ← 其他事件静默忽略
    }
}
```

然而，系统中有大量域操作**已经存在业务逻辑**，但对应的 outbox 事件要么根本未发布，要么发布了但未被任何 handler 消费：

| 域操作 | 状态 | 代码证据 |
|--------|------|---------|
| `file.created` 文件上传 | ✅ 事件已发布（`file/service_crud.go`） | 但 dispatcher 忽略 → **无通知、Webhook 不投递** |
| `file.shared` / `file.unshared` 文件分享 | ❌ 未发布事件 | `file/service_share.go` 操作后无 `outbox.NewEvent` |
| `comment.liked` / `comment.unliked` 评论点赞 | ❌ 未发布事件 | `comment/service.go` 的 Like/Unlike 不产生事件 |
| `message.reacted` / `message.unreacted` 消息回应 | ❌ 未发布事件 | `message/service.go` 的 React/Unreact 不产生事件 |
| `user.blocked` / `user.unblocked` 用户屏蔽 | ❌ 未发布事件 | `block/block.go` 仅写入 audit log，不产生 outbox 事件 |
| `file.deleted` 文件删除 | ❌ 未发布事件 | `file/service_crud.go` 的 Delete 不发布事件 |
| `comment.created` 评论创建 | ❌ 未发布事件 | 当前仅发布 `comment.replied`（只在回复时），top-level 评论无事件 |

### 为什么需要

1. **Webhook 可用性受损。** `Outbox Webhook` 已配置可向外部 HTTP 端点投递事件（HMAC 签名、事件类型过滤），但由于 dispatcher 只识别 3 个类型，外加 `file.created` 有事件但被 dispatcher 忽略，webhook 消费者实际上只能收到消息和评论回复事件。文件分享、评论点赞等关键事件无法被外部订阅。
2. **通知生态不完整。** 用户无法收到"有人赞了你的评论"、"有人回应了你的消息"、"XXX 与你共享了一个文件"等通知。这些是社交平台的基本 UX。
3. **扩展成本极低。** 每个缺失的事件只需要在对应的 service 方法中加一行 `outbox.NewEvent(...)` 调用，并在 dispatcher 中增加一个 case。

### 建议方案

**Phase 1 — 补齐事件发布（低成本的零依赖修改）：**

```go
// file/service_share.go — 新增 outbox 事件发布
func (s *fileService) GrantAccess(...) {
    // ... 现有逻辑 ...
    evt := outbox.NewEvent("file", fileID, "file.shared", map[string]any{
        "file_id": fileID,
        "owner_uid": owner,
        "granted_to": targetUID,
    })
    // 通过 tx 写入 outbox（与 share row 同一事务）
}

// comment/service.go — Like/Unlike 事件
evt := outbox.NewEvent("comment", commentID, "comment.liked", payload)
```

**Phase 2 — Dispatcher 新增消费者：**

```go
// dispatcher 新增 case
case "file.shared":
    return d.notificationFileShared(e)
case "comment.liked":
    return d.notificationCommentLiked(e)
case "message.reacted":
    return d.notificationMessageReacted(e)
```

### 边界情况

- **事件风暴保护：** 对于批量操作（如一条评论被 100 人点赞），可以为 `comment.liked` 类型启用折叠（现有 `WithDispatcherCollapse(5min)` 已就绪）
- **Block 检查复用：** 新增的通知类型应服从业已实现的 block 过滤逻辑
- **自操作过滤：** 用户给自己的评论点赞不应触发通知（现有 dispatcher 已做：`from == to` 时 return nil）
- **未读计数影响：** 新增的通知类型应关联到未读计数体系（`notification.unread_count` 已实现）

---

## 方向三：对话级搜索（Conversation-Scoped Search）

### 现有实现

`MessageRepository` 的 `SearchMessages` 接口按用户可见范围搜索——只接收 `uid` 和 `query`，不接收 `withUID`：

```go
// internal/message/message.go:46
SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error)
```

`internal/service/search.go` 调用链：

```go
// internal/service/search.go:86
msgs, err = s.messages.SearchMessages(searchCtx, actor, query, limit)
```

SQL 实现中只根据 `from_uid`/`to_uid` 做参与者过滤，无 `conversation_id` 概念：

```sql
-- 内部 repository/message.go SearchMessages 的简化 SQL
WHERE (from_uid = ? OR to_uid = ?)
  AND to_tsvector('english', content) @@ plainto_tsquery('english', ?)
```

HTTP 端点和 gRPC handler 同样不支持搜索域限制：

```go
// internal/handler/http/search.go:28-30
q := c.Query("q")
kind := c.Query("kind")   // 只支持按 kind 过滤
limit := c.Query("limit")
// 无 with_uid 或 conversation_id 参数
```

### 为什么需要

1. **产品刚需。** 所有主流消息平台都支持对话内搜索。用户能搜索自己的全部消息，但不能限定在"和 Alice 的聊天中搜索"——这相当于 Gmail 的全局搜索与对话内搜索的区别。
2. **数据权限模型自然延伸。** 现有系统已精确知道用户与谁有对话（ListConversation 从 `from_uid = ? AND to_uid = ?` 查询），搜索只需增加一个 `peerUID` 过滤条件即可复用同一 ACL。
3. **实现成本极低。** 跨 4 个文件和 1 个 gRPC proto 字段的变更，不涉及新表或数据迁移。

### 建议方案

```go
// 新增 SearchMessagesInConversation
SearchMessagesInConversation(ctx, actorUID, peerUID, query string, limit int) ([]*domain.Message, error)

// HTTP 查询参数扩展
GET /v1/search?q=xxx&kind=messages&with_uid=alice

// gRPC proto 扩展
message SearchRequest {
    string query = 1;
    repeated string kinds = 2;
    int32 limit = 3;
    string with_uid = 4;   // ← 新增
}

// SQL 实现
WHERE ((from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?))
  AND to_tsvector('english', content) @@ plainto_tsquery('english', ?)
```

### 边界情况

- **用户未与 `with_uid` 对话过：** 返回空结果，而非错误。不应暴露"用户 A 与用户 B 是否有对话"的信息
- **Block 穿透：** 如果 `with_uid` 已被当前用户 block，返回空结果
- **搜索 + 文件：** 文件搜索是按 `owner_uid` 范围的，不适用对话级概念。`with_uid` 参数只在 `kind=messages` 时生效
- **搜索 + 评论：** 评论也是所有者范围，对话级搜索不适用
- **性能：** 增加 `with_uid` 过滤条件实际上缩小了搜索范围（额外的 WHERE 子句），有利于索引使用而非损害

---

## 方向四：外部内容嵌入与公开分享链接（oEmbed / Share Links）

### 现有实现

文件可见性模型是二元的 `VisibilityPrivate` / `VisibilityPublic`，但公开文件只有一个 `/v1/files/:id` 端点（需要 auth token）：

```go
// internal/domain/shared.go
const (
    VisibilityPrivate Visibility = 0
    VisibilityPublic  Visibility = 1
)
```

文件共享只支持"授予其他 aero-id 用户读权限"：

```go
// internal/file/service_share.go
GrantAccess(ctx, fileID, targetUID string) error   // 给另一个 uid 授权
RevokeAccess(ctx, fileID, targetUID string) error
```

HTTP 路由中，唯一不要求 Bearer token 的公共端点是 `/files/dl`（HMAC 签名验证）和 `/healthz`/`/readyz`：

```go
// internal/handler/http/router.go
e.GET("/healthz", health.Live)
e.GET("/readyz", health.Ready)
// /v1 下的所有路由都通过 authMW（要求 Bearer 令牌）
```

**没有任何端点可以：**
- 生成一个无需认证的、有时效的共享链接
- 返回 oEmbed JSON 供外部网站嵌入
- 返回 Open Graph 标签用于社交分享预览
- 为公开展示提供 iframe/script 标签

### 为什么需要

1. **文件共享的上限。** 当前的文件共享只能在 aero-id 用户生态内进行。无法将文件共享给非 SSO 用户，也无法分享到外部平台（社交媒体、团队 Wiki、文档工具）。
2. **嵌入生态缺失。** 现代平台（Notion、Confluence、Slack、Discord）都支持 oEmbed 协议——只要粘贴一个链接就自动展开为富媒体卡片。aero-id 没有 oEmbed 端点，也无法被其他平台嵌入。
3. **外部协作场景。** 用户需要将文件临时共享给客户、合作伙伴等非平台用户。密码保护的链接 + 过期时间是标准功能。

### 建议方案

```
新增表: share_links
  id          UUIDv7 PK
  file_id     UUID FK
  owner_uid   string
  token       string (随机 64 字符，作为 URL token)
  password    string (bcrypt 哈希，可选)
  expires_at  timestamptz (可选)
  max_downloads int (可选)
  created_at  timestamptz

新增端点 (no auth required):
  GET  /s/:token          → 文件下载或预览页
  POST /s/:token/verify   → 密码验证

新增端点 (auth required):
  POST /v1/files/:id/share-links     → 创建共享链接
  GET  /v1/files/:id/share-links     → 列出链接
  DELETE /v1/files/:id/share-links/:linkId → 撤销链接

新增端点 (oEmbed):
  GET /api/oembed?url=...&format=json
  返回文件标题、MIME 类型、尺寸、缩略图、创建者等

新增端点 (Open Graph / 社交预览):
  GET /s/:token/preview → HTML 页面含 OG 标签
```

### 边界情况

- **URL 安全：** token 应使用 `crypto/rand` 生成，64 字符 base64url
- **密码保护 vs 性能：** bcrypt 验证对下载路径有性能影响，可考虑在密码验证后设置短期 session cookie
- **撤销语义：** 撤销共享链接后，已分发出去的 URL 应立即失效（GC 友好：每日清理过期 token）
- **文件删除后：** 共享链接在文件软删除后返回 410 Gone
- **带宽滥用：** 公开共享链接触发了与认证文件不同的威胁模型——需额外的限流（按 IP、按 token）
- **oEmbed CORS：** 需要适当的 `Access-Control-Allow-Origin` 头部支持跨域嵌入

---

## 方向五：存储用量计量与配额管理（Storage Quotas & Usage Metering）

### 现有实现

文件上传路径仅在 `Service` 层有大小限制（单个文件上限），但**无任何按用户聚合的用量管控**：

```go
// internal/file/service_upload.go
// 已实现的限制：单个文件大小
if cfg.maxUploadBytes > 0 && in.Size > cfg.maxUploadBytes {
    return nil, apperr.BadRequest("file.too_large", "file exceeds max upload size")
}

// 未实现的限制：无以下检查
// - 用户当前已用存储总量
// - 用户尚有剩余配额
// - 用户月度带宽消耗
```

File 模型有 `Size int64` 字段，但无聚合查询：

```go
// internal/domain/file.go
type File struct {
    Size     int64    // 单个文件大小，已知
    // 无 OwnerUID → 总用量映射
    // 无计费/配额相关字段
}
```

Repository 层没有类似 `SumStorageByOwner(ctx, ownerUID) (int64, error)` 的接口。

### 为什么需要

1. **运营风险。** 在无配额的存储服务中，单一用户可以上传 TB 级别的数据，导致存储成本失控。`MaxUploadBytes` 只限制了单文件大小，但用户可以通过上传大量文件绕过。
2. **多租户基础。** 未来任何多租户/组织场景都需要配额管理来隔离资源使用（见已有讨论但未实现的多租户方向）。
3. **产品透明度。** 用户无法查看自己的存储使用情况。没有 `GET /v1/usage` 端点就没有用量仪表盘的基础。
4. **数据清洗触发。** 当用户接近配额上限时，系统可以主动通知用户清理文件（结合现有的 GC sweeper），而非直接拒绝上传。

### 建议方案

```go
// 新增 Repository 接口
interface {
    SumStorageByOwner(ctx context.Context, ownerUID string) (int64, error)
    CountFilesByOwner(ctx context.Context, ownerUID string) (int64, error)
}

// 新增 Service 接口
type UsageService interface {
    GetUsage(ctx context.Context) (*Usage, error)
    // Usage 包含:
    //   StorageUsed: int64 (字节)
    //   FileCount: int64 (文件数)
    //   StorageQuota: int64 (配额，从配置或用户元数据读取)
    //   BandwidthUsed: int64 (月度带宽，需新增 bandwidth_log 表)
    //   BandwidthQuota: int64
}

// Service 层 Create 路径新增检查
if cfg.storageQuota > 0 {
    used, _ := s.files.SumStorageByOwner(ctx, actor)
    if used + in.Size > cfg.storageQuota {
        return nil, apperr.Forbidden("file.quota_exceeded", "storage quota exceeded")
    }
}

// 新增端点
GET /v1/usage → 当前用户的存储和带宽用量

// 可选：新增 bandwidth_logs 表
// 记录每次下载的文件大小和时间，用于带宽计量
```

### 边界情况

- **软配额 vs 硬配额：** 建议先用软配额（超限告警 + 阻止上传），避免影响已有文件的下载和共享
- **GC 后的配额释放：** 文件软删除后应计入已用空间（避免删除后立刻被 GC 清理导致配额计算不一致），硬清理后才释放
- **共享文件归属：** 共享文件的存储空间应计入**所有者**的配额，而非访问者的配额
- **带宽计量：** 文件下载的带宽消耗应从所有者的配额中扣除（可选）
- **配额配置来源：** 初始可来自 `config.yaml`（全局统一配额），未来可扩展为从 SSO 获取用户级别的配额元数据
- **并发一致性：** `SumStorageByOwner + Check` 不是原子操作。在并发上传场景中可能超限。可接受（软配额）或用 `SELECT ... FOR UPDATE` 保护

---

## 优先级与成本评估

| # | 方向 | 产品价值 | 架构杠杆 | 实施成本 | 推荐时序 | 最快价值子项 |
|---|------|---------|---------|---------|---------|-------------|
| 1 | **多类型 Reactions** | ★★★★★ | ★★☆☆☆ | ★★☆☆☆ | **P1** | 先改 `message_reactions` 表加 emoji 列 + 接口参数，保留向后兼容 |
| 2 | **不完备 Outbox 事件** | ★★★★☆ | ★★★★★ | ★☆☆☆☆ | **P0** | 1 天可完成：3 个 service 加 `outbox.NewEvent` + dispatcher 加 case |
| 3 | **对话级搜索** | ★★★★☆ | ★★☆☆☆ | ★☆☆☆☆ | **P1** | 1-2 天：加 `with_uid` 参数 + SQL 过滤 + HTTP/gRPC handler 透传 |
| 4 | **外部嵌入与分享链接** | ★★★★★ | ★★★★☆ | ★★★★☆ | **P2** | 先做 `share_links` 表 + CRUD（2-3天），oEmbed 可后续 |
| 5 | **存储用量与配额** | ★★★★☆ | ★★★☆☆ | ★★★☆☆ | **P1** | 先加 `SumStorageByOwner` 查询 + `GET /v1/usage`（1天），配额强制可后续 |

### 快速获胜项（1-2 天内可完成的低成本子项）

1. **补齐 `file.shared` outbox 事件** — `file/service_share.go` 中加一行 `outbox.NewEvent`，dispatcher 加一个 case，成本极低
2. **对话级搜索 `with_uid` 参数** — 仅添加 SQL WHERE 子句和 HTTP/gRPC 参数透传，零新依赖
3. **存储用量查询端点** — `SumStorageByOwner` + `GET /v1/usage`，无新表，纯聚合查询
4. **`message_reactions` 表加 `emoji` 列** — SQL 迁移 + 两个接口方法重载

---

## 附：扫描中发现的额外技术观察

这些未被列为独立方向，但值得关注：

| 分类 | 观察 | 影响 |
|------|------|------|
| **API 一致性** | `ReactToMessage` 的 HTTP handler 使用 `POST/DELETE /messages/:id/reactions`（无请求体），而 gRPC handler 接受空请求。缺少反应类型意味着未来扩展需要破坏性 API 变更。 | 低 |
| **可观测性** | `storage/gc/sweeper.go` 的运行统计（Stats）通过日志和返回值暴露，但未纳入 `/metrics` RED 体系。GC 的耗时、清理量、失败数对运维有参考价值。 | 低 |
| **配置冗余** | `internal/service/service.go` 中的 `actorKey`/`WithActor`/`ActorFrom` 与 `internal/domain/actor.go` 完全重复。前者是迁移遗留。 | 中（代码整洁性） |
| **预防性维护** | `internal/storage/signing.go` 的 HMAC 签名使用 `crypto/hmac` + `sha256`（强哈希）。但如果未来 Switching to SHA-3 或需要 key rotation，当前没有版本化签名机制。 | 低 |
| **搜索结果排序** | `internal/search/search.go` 按 `kind` 顺序追加结果（先消息、再评论、再文件），无跨 kind 的相关度排序。多 kind 搜索的用户体验有限。 | 中 |
| **通知折叠粒度** | `WithDispatcherCollapse(5min)` 按 `(recipient, type, actor, subject_type, subject_id)` 折叠。但不处理 `comment.liked` 场景——100 人点赞产生 100 条通知 vs 折叠为"XXX 和 99 人赞了你的评论"是不同需求。 | 中 |
