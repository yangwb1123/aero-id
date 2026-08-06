# aero-id — 资深架构师/产品经理视角：五项代码级可证明的体系性缺口

> **日期:** 2026-07-11
> **视角:** 资深架构师 + 产品经理
> **方法:** 全量代码库逐文件通读 — `internal/` 全部 50+ 子包、`migrations/` 23 份 SQL、`cmd/`、`configs/`、`api/proto/`。与 `docs/requirements/` 下现存 **137 份分析文档** 的标题与方向关键词做交叉比对。
> **约束:** 不编写任何代码。每个方向提供精确到行号的代码级证据。
> **核心主张:** 以下五项不是"高层次的架构畅想"，而是 **代码中实际存在的、可证明的具体缺口**——它们不在 137 份既有分析中作为独立方向被系统性论证。

---

## 与既有分析的分界声明

经逐方向比对，以下五个方向在 `docs/requirements/` 全部既有材料中 **未被作为独立方向系统性地提出和论证**。部分方向（如"消息反应"或"文件安全"）可能在个别文档中被侧面提及为子问题，但从未被作为一个独立的高价值方向进行完整分析。

---

## 方向一：消息反应模型过于原始（Binary Reaction Model）—— 无类型、无事件、无通知

### 问题陈述

当前消息的"反应"（reaction）功能是一个纯二元的、功能残缺的实现：

1. **反应无类型**：用户要么"已反应"要么"未反应"，无法选择具体的表情符号（❤️👍😄😢😡）。
2. **反应无事件**：React/Unreact 操作**不走事务性 Outbox**，是一条直达数据库的写入，不具备可靠性和可观测性。
3. **反应无通知**：反应操作不产生任何通知。消息发送者无法知道有人对自己的消息做出了反应。

这是一个产品功能、可靠性、和用户体验的三重缺口。

### 代码级证据

**证据 1 —— 数据库表结构无 reaction_type 字段：**

`migrations/000013_message_reactions.up.sql`:
```sql
CREATE TABLE IF NOT EXISTS message_reactions (
    message_id UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    uid        TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (message_id, uid)
);
```

该表仅有 `(message_id, uid)` 作为联合主键。没有 `reaction_type` 列（或等价的 `emoji` 列），意味着一个用户在一消息上**最多只能有一种"反应"**，且无法区分是👍还是❤️。

**证据 2 —— Repository 层的 React/Unreact 不接收类型参数：**

`internal/message/repository.go` (约第 178-205 行):
```go
func (r *messageRepo) ReactToMessage(ctx context.Context, messageID uuid.UUID, uid string) error {
    // 无 reactionType 参数
    // 直接 INSERT，不做类型校验
    res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
        Create(&messageReactionRow{MessageID: messageID, UID: uid, CreatedAt: time.Now().UTC()})
    // ...
}

func (r *messageRepo) UnreactMessage(ctx context.Context, messageID uuid.UUID, uid string) error {
    // DELETE 基于 message_id + uid，没有类型过滤
}
```

**证据 3 —— Service 层的 ReactToMessage 不发射 Outbox 事件：**

`internal/message/service.go` (约第 163-176 行):
```go
func (s *service) ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error {
    m, err := s.messages.Get(ctx, messageID)
    if err != nil {
        // ...
    }
    if !canReadMessage(actorUID, m) {
        return apperr.Forbidden(...)
    }
    // 直接调用 repository，没有 outbox.NewEvent()
    // 没有事件，就没有异步通知投递
    if err := s.messages.ReactToMessage(ctx, messageID, actorUID); err != nil {
        // ...
    }
    return nil
}
```

对比 `Send` 方法（约第 56-65 行）：
```go
func (s *service) createMessage(ctx context.Context, actor string, in SendMessageInput) (*domain.Message, error) {
    // ...
    evt := outbox.NewEvent("message", m.ID.String(), "message.sent", map[string]any{...})
    if err := s.messages.Create(ctx, m, evt); err != nil {
        // ...
    }
}
```

Reaction 操作完全不产生 Outbox 事件——这意味着：
- 反应操作不是幂等的（但 HTTP handler 走的是 POST，没有幂等键保障）
- 反应操作失败无法重试（没有 outbox relay 做异步兜底）
- 反应操作的速率无法追踪

**证据 4 —— 通知派发器不处理 reaction 事件：**

`internal/notification/dispatcher.go` (约第 68-77 行):
```go
func (d *Dispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":     return d.notificationMessageSent(e)
    case "message.read":     return d.notificationMessageRead(e)
    case "comment.replied":  return d.notificationCommentReplied(e)
    default:                 return nil   // ← message.reacted 不存在
    }
}
```

**证据 5 —— Comment Like 同样无通知：**

`internal/comment/service.go` (约第 159-180 行):
```go
func (s *serviceImpl) Like(ctx context.Context, id domain.ID) (*ReactionResult, error) {
    count, err := s.react(ctx, id, true)
    // ...
}
func (s *serviceImpl) react(ctx context.Context, id domain.ID, like bool) (int, error) {
    // 直接调 comments.Like/Unlike
    // 无 outbox 事件，无通知给评论作者
}
```

`internal/comment/comment.go` `Repository` 接口的 `Like`/`Unlike` 签名不带 `*outbox.Event` 参数，而 `CreateWithReplyBump` 签名带有——这是设计不一致的明显信号。

### 为什么需要修复

| 视角 | 论证 |
|------|------|
| **产品** | 2026 年的即时通讯产品，反应没有表情类型、没有通知——这是不可接受的。用户无法表达"点赞"和"笑哭"的区别，消息作者不知道自己的内容被认可。对标 iMessage、Telegram、WhatsApp 的完整反应体验。 |
| **架构** | 反应操作是唯一不走事务性 Outbox 的写操作，形成了一个系统性的可靠性盲区。Outbox 是 aero-id 的异步可靠性的核心保证，反应操作不应成为例外。 |

### 建议方案

1. **`message_reactions` 加 `reaction_type` 列**（VARCHAR(32)，支持 `thumbs_up` / `heart` / `laugh` / `surprised` / `cry` / `angry`）
2. **`comment_likes` 扩展为 `comment_reactions`**（加入 `reaction_type`，消除与 `message_reactions` 的不对称）
3. **`ReactToMessage` 和 `react`（comment）增加 Outbox 事件发射**，类型为 `message.reacted` / `comment.liked`
4. **`Dispatcher` 增加 `message.reacted` 和 `comment.liked` 通知处理**，通知作者的被反应对象
5. **HTTP handler 的 `POST /messages/:id/reactions` 增加请求体接受 `type` 参数**

### 边界情况

- **重复反应覆盖**：同一用户对同一消息多次 POST 同一类型应该是幂等的；改变类型应被视为覆盖旧反应
- **反应类型枚举**：支持的类型应当由服务端控制（不允许自定义 emoji），防止滥用
- **已删除消息的反应**：消息被软删除后，前端不应再显示 reaction 按钮；服务端应在 Get 时过滤
- **通知限频**：N 个人对同一条消息连续反应不应产生 N 条单独通知（应合并为"3 人赞了你的消息"）

---

## 方向二：SSE 断线重连的 Missed Events Recovery 存在硬编码 50 条上限

### 问题陈述

SSE 实时推送通道提供了基于 `Last-Event-ID` 的断线重连回放功能。但是回放逻辑使用了硬编码的 `limit=50`，且**没有分页或继续令牌机制**。如果一个客户端断开连接超过 50 条通知的时间窗口，丢失的通知将永远无法恢复。

### 代码级证据

**证据 1 —— `replayMissed` 硬编码 limit=50：**

`internal/handler/http/events.go` (约第 80-95 行):
```go
func (h *EventsHandler) replayMissed(c *gin.Context, uid, lastEventID string) {
    millis, err := strconv.ParseInt(lastEventID, 10, 64)
    if err != nil || millis <= 0 {
        return
    }
    since := time.UnixMilli(millis)
    missed, err := h.fetcher.ListSince(c.Request.Context(), uid, since, 50)
    //                                                    ^^^^^^^^^^  硬编码 50
    if err != nil {
        c.Error(err)
        return
    }
    for _, n := range missed {
        // 回放...
    }
    // 没有分页，没有 next_cursor，没有 "还有更多" 标识
}
```

**证据 2 —— `MissedNotifFetcher` 接口无分页支持：**

`internal/handler/http/events.go` (约第 18-22 行):
```go
type MissedNotifFetcher interface {
    ListSince(ctx context.Context, recipientUID string, since time.Time, limit int) ([]*domain.Notification, error)
    // 无 next_cursor 返回值
    // 无 total_count 返回值
    // 无 "has_more" 标识
}
```

**证据 3 —— 可能的通知产生速率估算：**

在高活跃场景下，如果多个操作（消息、评论、回复、后续的 reaction 通知等）在短时间内产生大量通知，50 条可能在几分钟内就被填满。一个断开 10 分钟的用户可能会永久丢失事件。

### 为什么需要修复

| 视角 | 论证 |
|------|------|
| **产品** | 用户失去实时连接后重新打开应用，期望看到所有未读通知。如果因硬编码限制丢失了超过 50 条的通知，用户体验出现不可解释的数据丢失——"你刚才说有人回复了我，但我在通知列表找不到"。 |
| **技术** | LTE/5G 网络切换、移动端后台进程被 kill、浏览器休眠等场景频繁触发 SSE 重连。每次重连都面临丢失超过 50 条通知的风险。Multi-device 用户（手机+桌面+Web）更可能长时间断开其中某个设备。 |

### 建议方案

1. **将 `ListSince` 改为分页形式**，返回 `nextSince`（或 `nextID`）时间戳，客户端回放完一批后再请求下一批
2. **SSE event id 改用 notification ID（UUIDv7 天然有序）** 而非 `created_at` unix-millis（精度只能到毫秒，无法区分同一毫秒内的多条通知）
3. **失败安全设计**：当某次回放因数据量过大超时时，返回 200 但带上 `X-Missed-Event-Overflow: true` 头部或 SSE comment event 告知客户端有更多事件
4. **可选的整页回放 API**：新增 `GET /v1/notifications/missed?since=<id>&limit=100` 作为脱离 SSE 协议的独立回放通道

### 边界情况

- **分页死循环**：如果通知被持续快速产生，客户端的分页永远追赶不上。应设最大回放深度（例如最多 1000 条）
- **回放期间的新通知**：回放和实时流共享同一个 SSE 连接，需要考虑回放事件与实时事件的排序（回放事件早于实时事件）
- **容量冲击**：批量回放大量通知时，SSE 的 16-event channel buffer 可能填满。需要控制回放速率

---

## 方向三：文件 MIME 类型校验完全信任客户端声明——无服务端内容验证

### 问题陈述

当前文件上传的 MIME 类型校验是基于客户端在 HTTP header 或请求体中声明的 `Content-Type`/`MimeType`，而不是基于文件实际内容的分析。这意味着：

1. 一个恶意用户可以声明 `image/jpeg` 而上传可执行 ELF/PE 文件
2. 安全扫描（ClamAV 等）无法在文件上传时介入——因为没有钩子点
3. 存储后端的文件类型与实际内容可能不一致，导致下游消费时产生意外行为

### 代码级证据

**证据 1 —— 上传路径只验证客户端声明的 MIME：**

`internal/file/service_crud.go` (约第 33 行):
```go
if !s.mimeAllowed(in.MimeType) {
    return nil, apperr.BadRequest("file.unsupported_type", "file content type is not allowed")
}
// 验证的是 in.MimeType —— 来自客户端声明的 Content-Type
```

`internal/file/service_upload.go` `CompleteUpload` (约第 48 行):
```go
if !s.mimeAllowed(in.MimeType) {
    return nil, apperr.BadRequest("file.unsupported_type", "file content type is not allowed")
}
// same: 验证客户端声明
```

**证据 2 —— Storage `Put` 方法不进行内容检查：**

`internal/storage/storage.go`:
```go
type FileStorage interface {
    Put(ctx context.Context, key string, r io.Reader) error
    // 没有 content-type 参数，没有内容检测
    // 纯字节写入，不做任何检查
}
```

**证据 3 —— `domain.File.Checksum` 存在但仅用于去重，不做内容分类：**

`internal/domain/file.go`:
```go
type File struct {
    // ...
    Checksum string   // SHA-256，用于去重
    // 无 ContentTypeVerified bool
    // 无 ActualMimeType string
    // 无 ScanStatus string
}
```

**证据 4 —— MIME 类型允许列表通过配置控制，但无法校验实际内容：**

`internal/file/file.go` (约第 1-30 行):
```go
func (s *fileService) mimeAllowed(mime string) bool {
    if len(s.allowedMimes) == 0 {
        return true // 空列表 = 允许任何 MIME，但"允许任何"意味着完全不校验
    }
    for _, a := range s.allowedMimes {
        if mime == a {
            return true
        }
    }
    return false
}
```

当配置了 `allowed_mime_types: ["image/jpeg", "image/png"]` 时，客户端只需声明 `Content-Type: image/jpeg` 即可上传任意二进制内容。

### 为什么需要修复

| 视角 | 论证 |
|------|------|
| **安全** | 这是最基础的文件上传安全防御。没有服务端内容验证，aero-id 可以被用作恶意文件的分发平台。特别是在有公开文件（`visibility=public`）的场景下，任何用户都可以通过声明合法 MIME 来托管任意文件。 |
| **合规** | 对于需要文件类型审计的场景（金融、医疗），服务端必须验证存储内容的实际类型，不能依赖客户端声明。 |
| **产品** | 下游消费方（缩略图生成、文档预览、图片自适应）依赖准确的 MIME 类型来做处理决策。虚假的类型会导致处理管道崩溃或产生不可用的结果。 |

### 建议方案

1. **增加 Content-Type sniffing**：在 `Put` 调用之前，对文件流的前 512 字节执行 `http.DetectContentType()` 或使用 `libmagic` 绑定
2. **存储检测到的真实类型**：`fileRow` 加 `detected_mime_type` 列。如果检测类型与声明类型不一致，记录差异但不一定拒绝（兼容某些客户端类型声明不精确的情况）
3. **配置可选的"严格模式"**：当 `storage.strict_mime_check: true` 时，声明类型与检测类型不匹配直接拒绝写入
4. **文件扫描钩子**：在 Outbox 事件 `file.uploaded`（当前不存在）中插入病毒扫描，但这需要先实现文件后处理管道

### 边界情况

- **无内容文件**：空文件（size=0）无法通过 sniff 检测类型，应跳过验证
- **MIME aliases**：`image/jpeg` vs `image/jpg` 等别名应统一处理
- **复合文档**：Office Open XML（.docx/.xlsx）实际上是 ZIP 压缩包，sniff 会检测为 `application/zip`，需要特殊逻辑
- **性能**：对非常大的文件只读取前 512 字节做 sniff 是高效的，但需要确保 `TeeReader` 与哈希计算的兼容性

---

## 方向四：跨用户的消息发送不验证接收方是否存在——可发消息给不存在的 SSO 用户

### 问题陈述

当前消息发送功能在 `Send` 和 `SendSystem` 中均不验证接收方（`ToUID`）是否在 SSO 中存在。这意味着：

1. 用户可以向一个已删除/封禁/不存在的 SSO 身份发送消息，操作表面成功
2. 消息被写入数据库，但接收方永远无法看到（因为该身份不存在或已封禁）
3. 发送者得到的是误导性的"发送成功"反馈——实际上消息进入了数据库黑洞
4. 没有通知被投递（因为通知派发阶段的目标 uid 不存在）

### 代码级证据

**证据 1 —— `Send` 方法仅检查 Block，不验证接收方存在：**

`internal/message/service.go` (约第 31-38 行):
```go
func (s *service) Send(ctx context.Context, in SendMessageInput) (*domain.Message, error) {
    actor, err := mustActor(ctx)
    // ...
    if err := s.checkSendBlock(ctx, in.ToUID, actor); err != nil {
        return nil, err       // 只检查了 block 状态
    }
    if err := s.checkSendFile(ctx, in.FileID, actor); err != nil {
        return nil, err       // 只检查了文件所有权
    }
    return s.createMessage(ctx, actor, in)
    // 没有对 in.ToUID 做任何 SSO 存在性检查
}
```

**证据 2 —— `SendSystem` 也不验证接收方存在：**

`internal/message/service.go` (约第 68-82 行):
```go
func (s *service) SendSystem(ctx context.Context, toUID, content string, meta map[string]any) (*domain.Message, error) {
    if toUID == "" {
        return nil, apperr.BadRequest(...)  // 只检查了空字符串
    }
    if content == "" {
        return nil, apperr.BadRequest(...)
    }
    // 不检查 toUID 是否真实存在
    // 系统消息可以写入已删除用户
}
```

**证据 3 —— SSO Client 没有提供 `IsActive(uid)` 一类的轻量存在性检查：**

`internal/sso/client.go`:
```go
// ValidateToken(token) → uid — 验证令牌，返回当前用户
// GetUsers(uids) → map[uid]User — 批量解析用户，返回找到的用户
// 没有 IsActive(uid) bool
// 没有 Exists(uid) bool
// 没有健康状态检查（除了 HealthCheck 用于 /readyz）
```

`internal/sso/user_cache.go`:
```go
type userCacheEntry struct {
    user  User
    expiresAt time.Time
}
func (c *UserCache) Get(uid string) (User, bool) { ... }
func (c *UserCache) Set(uid string, u User)       { ... }
// 没有 IsActive() / Exists()
// 没有 Invalidate()
```

**证据 4 —— uid 在 domain 层是裸字符串，不携带状态：**

`internal/domain/actor.go`:
```go
func WithActor(ctx context.Context, uid string) context.Context {
    return context.WithValue(ctx, actorKey, uid)
}
// uid 没有 is_active / is_banned / is_deleted 等元数据
```

### 为什么需要修复

| 视角 | 论证 |
|------|------|
| **产品** | 从用户视角，向一个不存在的用户发消息应该立即得到有意义的错误（"该用户不存在"）或自动阻止发送。当前的行为是静默失败——最糟糕的产品体验之一。 |
| **数据完整性** | `messages` 表中的 `to_uid` 如果没有对应的 SSO 用户存在，这些行就是数据垃圾。它们占用存储空间、混淆搜索结果、干扰未读计数。 |
| **安全** | 攻击者可以通过大量向随机 uid 发送消息来制造数据膨胀攻击（storage amplification），如果未来按存储量收费还会造成经济损失。 |

### 建议方案

1. **在 `Send` 中增加接收方存在性检查**：调用 `s.dir.GetUsers(ctx, []string{in.ToUID})` 检查结果中是否包含该 uid
2. **引入轻量级缓存 `ActiveUserCache`**：缓存 `(uid → is_active)` 状态，避免每次 Send 都穿透到 SSO。TTL 可设 5 分钟
3. **`SendSystem` 同样增加检查**，对非活跃 uid 返回 `apperr.NotFound("message.recipient_not_found")`
4. **批量清理任务**：后台 job 扫描 `to_uid` 不在 SSO 中的消息，标记为不可达（加 `delivery_status = 'undeliverable'` 列或类似标记）
5. **GetUsers 的缓存过期处理**：当 SSO 发出用户删除事件（方向一）时，失效对应的缓存条目

### 边界情况

- **SSO 批量查询返空**：`GetUsers` 可能返回空的 map，不代表所有 uid 都不存在——可能是 SSO 超时。需要区分"不存在"和"查询失败"
- **测试/开发环境**：在 SSO 不完整的开发环境中，此检查可能造成干扰。应当通过配置开关控制
- **系统消息的例外**：系统消息（如"你的账号已被升级"）可能发送给尚未走完 SSO 完整注册流程的用户，应白名单放行

---

## 方向五：管理操作（角色授予/用户封禁）缺少审计追踪与 HTTP/gRPC 端点

### 问题陈述

aero-id 的 admin 系统存在一个半完成的实现缺口：`AdminRepository` 接口已经定义了 `GrantRole` 和 `RevokeRole` 方法，但：

1. **没有 HTTP/gRPC handler 暴露这些操作**——角色授予/撤销只能通过直接操作数据库完成
2. **没有审计记录**——即使通过数据库直接操作，这些行为也不会记录到 `audit_events` 表
3. **用户封禁（`PermUserBan` 权限已定义）没有对应的 API 和审计日志**——`AuditUserBlock` 是用户级别的双向 block，而非管理员对用户的单向 ban

这是一个"定义了权限但没有操作入口"的悬空架构问题。

### 代码级证据

**证据 1 —— `GrantRole`/`RevokeRole` 在 Repository 中存在但 Handler 中不可达：**

`internal/repository/admin.go` (约第 9-15 行):
```go
type AdminRepository interface {
    HasPermission(ctx context.Context, uid, perm string) (bool, error)
    ListAudit(ctx context.Context, action string, page domain.Page) ([]*domain.AuditEvent, string, error)
    GrantRole(ctx context.Context, uid, roleCode, grantedBy string) error  // 存在但无处调用
    RevokeRole(ctx context.Context, uid, roleCode string) error           // 存在但无处调用
}
```

**证据 2 —— HTTP handler 仅暴露了 `ListAudit`：**

`internal/handler/http/admin.go` (约第 18-19 行):
```go
func (h *AdminHandler) RegisterRoutes(r Routes) {
    r.Auth.GET("/admin/audit-log", h.ListAudit)
    // 没有 POST /admin/roles/grant
    // 没有 POST /admin/roles/revoke
    // 没有 POST /admin/users/ban
    // 没有 POST /admin/users/unban
}
```

**证据 3 —— gRPC handler 中完全没有 admin 端点：**

`internal/handler/grpc/ops.go`: 只有 `HealthServer`、`RegisterHealth`、`RegisterReflection`——没有 admin service 注册。

**证据 4 —— 审计动作常量中缺少管理操作的分类：**

`internal/domain/audit.go` (约第 14-21 行):
```go
const (
    AuditFileShareGrant  = "file.share.grant"
    AuditFileShareRevoke = "file.share.revoke"
    AuditReportResolve   = "comment.report.resolve"
    AuditUserBlock       = "user.block"
    AuditUserUnblock     = "user.unblock"
    AuditMessageDelete   = "message.delete"
    // 没有 AuditRoleGrant
    // 没有 AuditRoleRevoke
    // 没有 AuditUserBan
    // 没有 AuditSystemConfig
)
```

**证据 5 —— `PermUserBan` 已定义但无对应实现：**

`internal/domain/admin.go`:
```go
const (
    PermContentModerate = "content.moderate"
    PermAuditRead       = "audit.read"
    PermUserBan         = "user.ban"          // 已定义
    PermSystemConfig    = "system.config"     // 已定义
)
```

`PermUserBan` 权限已定义，但系统中不存在"管理员封禁用户"的功能——唯一的"block"是用户间的双向屏蔽，不是管理封禁。

### 为什么需要修复

| 视角 | 论证 |
|------|------|
| **合规** | SOC2/GDPR 要求对管理操作（特别是角色授予和用户封禁）做不可篡改的审计追踪。"谁在什么时间给了谁管理员权限"是最基本的审计需求。 |
| **安全** | 没有 API 入口意味着运营人员只能直接操作数据库来管理角色。这是一种风险极高的做法——无审计、无权限校验、无操作回滚。 |
| **架构完整性** | 代码中存在 `GrantRole`/`RevokeRole` 方法，但没有任何途径可以调用它们——这是一个悬空的半成品。`PermUserBan` 定义了能力但从未实现。 |

### 建议方案

| 组件 | 新增内容 |
|------|----------|
| **HTTP handler** | `POST /v1/admin/roles/grant` · `POST /v1/admin/roles/revoke` · `POST /v1/admin/users/ban` · `POST /v1/admin/users/unban` |
| **gRPC handler** | 新增 `AdminServiceServer` 实现，包含对应的 RPC 方法 |
| **审计常量** | 新增 `AuditRoleGrant`、`AuditRoleRevoke`、`AuditUserBan`、`AuditUserUnban`、`AuditSystemConfig` |
| **Service 层** | 抽离 `AdminService`（当前 admin 操作在 handler 中直接调 repository），增加权限校验逻辑 |
| **迁移** | `user_roles` 表的 `granted_by` 列已存在，但需利用它做操作审计 |

### 边界情况

- **不可自降级**：管理员不能撤销自己的管理员角色（防止误操作导致系统无管理员）
- **角色继承链**：如果存在 `superadmin > admin > moderator` 的层次，高级角色不应被低级角色管理
- **封禁 vs 删除**：封禁（ban）应使该用户的所有内容不可见但不丢失；删除（delete）应触发数据清理流程
- **封禁通知**：被管理员封禁的用户应收到一条系统消息说明原因和申诉渠道

---

## 优先级与影响范围

| # | 方向 | 优先级 | 影响域 | 估算工作量 | 核心受益者 |
|---|------|--------|--------|-----------|-----------|
| 1 | 反应模型扩展（类型+事件+通知） | **P1** | 产品体验 · 可靠消息 | 低-中（改表+events+handler） | 最终用户 |
| 2 | SSE 恢复窗口容量限制 | **P1** | 数据完整性 · 用户体验 | 低（改接口分页+evt id） | 移动端/多设备用户 |
| 3 | 服务端文件类型验证 | **P1** | 安全 · 合规 · 存储质量 | 低（加 content sniffing） | 平台安全 |
| 4 | 发送消息前验证接收方存在 | **P2** | 产品体验 · 数据质量 | 低（轻量 SSO 查询） | 最终用户 |
| 5 | 管理操作审计与 API | **P1** | 合规 · 安全 · 架构完整性 | 中（handler+audit+permission） | 运维 · 合规 |

**实施路线建议**：
- **方向 2 和 3 最轻量**（各 ~1 个工作日），可作为立即可交付的改进
- **方向 1 和 4 有产品价值但需要简单的数据库迁移+handler 更新**（各 ~2 日）
- **方向 5 中等工作量**（~3 日），但填补了关键的合规和架构完整性空白

---

## 各方向与代码库既有能力的关系图

```
方向一：反应模型扩展
  当前: React(message_id, uid) → 二元，无事件，无通知
  目标: React(message_id, uid, type) → outbox event → 通知 → SSE push
  影响: message_reactions 表 · outbox 事件类型 · dispatcher · message domain

方向二：SSE 恢复窗口
  当前: ListSince(uid, since, 50) → 回放最多 50 条
  目标: ListSince(uid, since, limit) → (notifs, nextCursor) → 分页回放
  影响: MissedNotifFetcher 接口 · events.go handler · event id 格式

方向三：文件类型验证
  当前: MIME 校验仅检查客户端声明的 Content-Type
  目标: http.DetectContentType() 前 512 bytes + 可选的严格模式
  影响: service_crud.go · service_upload.go · file domain model

方向四：发送验证
  当前: Send(toUID) → 不检查 SSO 存在 → 写入数据库 → 沉默黑洞
  目标: Send(toUID) → GetUsers([toUID]) → 不存在则 404 → 安全拒绝
  影响: message service · sso client · ActiveUserCache

方向五：管理操作
  当前: GrantRole/RevokeRole 存在于 repository 但无 handler 调用
  目标: HTTP + gRPC 端点 → service 层权限校验 → repository → audit log
  影响: admin handler(HTTP+gRPC) · audit constants · admin domain
```
