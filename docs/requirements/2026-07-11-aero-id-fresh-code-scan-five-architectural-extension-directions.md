# aero-id — 全量代码扫描：五个架构级扩展方向

> **日期**: 2026-07-11
> **视角**: 资深架构师 / 产品经理
> **方法**: 逐文件通读 `internal/` (22+ 子包, ~180 源文件)、`migrations/` (23 份 SQL)、`api/proto/`、`cmd/`、`configs/`、`Makefile`、`docs/`。与 `docs/requirements/` 下 **96+ 份既有分析文档**、`ROADMAP.md`、`docs/CHANGELOG.md`、`docs/DECISIONS.md`、`docs/ARCHITECTURE.md` 做逐方向交叉比对。
> **约束**: 不编写代码。每个方向提供精确的代码级证据并从架构师/产品经理双视角论证价值。

---

## 当前代码库成熟度

aero-id 已从一个三层 CRUD MVP 演化为高度成熟的生产级服务平台。以下能力已全部投产：

| 域 | 已交付能力 |
|----|-----------|
| **基础设施** | 双协议 HTTP+gRPC、GORM+PG16、本地/S3 存储、OTel 追踪、RED 指标、per-dependency `/readyz`、冷启动 warmup、优雅关停 |
| **可靠性** | 事务性 Outbox、幂等键、速率限制、SSO 断路器 + stale-cache + singleflight + 重试退避 + gRPC keepalive/TLS/mTLS/证书锁定 |
| **文件** | 流上传、MIME/size 校验、SHA-256 去重、S3 presigned 直传、显式分享/撤销、引用计数 GC、元数据 PATCH、storage refs |
| **消息** | 1:1 私信、Reactions、编辑/软删、已读回执 + 计数、Inbox/Conv 视图、系统消息、Block 拦截 |
| **评论** | 两层嵌套、Like/Unlike、原子计数、举报→审核队列、Hide/Unhide、编辑 |
| **通知** | Outbox 驱动、SSE 实时推送、折叠去重、偏好(静默时段)、Block 抑制、Webhook 投递 |
| **搜索** | 跨实体 FTS (PG tsvector+GIN)、CJK ILIKE 回退、多 kind 联合搜索 |
| **安全** | 方向性 Block、审计日志、Admin 角色+权限(content.moderate/audit.read/user.ban/system.config) |
| **数据模型** | 23 次迁移（含 FTS 索引、通知索引、Outbox 重试、通知折叠、Storage Ref、Admin 角色） |

---

## 交叉验证说明

96+ 份既有分析文档累计覆盖了极其广泛的主题。以下 5 个方向在既有分析中**未作为独立方向进行完整的代码级证据链论证**——即没有任何文档从架构原则、代码证据、边界情况、产品影响四个维度系统性地分析过它们。

每个方向提供：
- **代码级证据** — 精确到行号和文件
- **产品经理视角** — 用户/业务价值
- **架构师视角** — 设计权衡与实现路径

---

## 方向一：评论/消息中富媒体嵌入 & 跨实体引用系统（Rich Media Embed & Cross-Entity Reference Infrastructure）

### 问题陈述

当前用户可以在消息中附加文件（`messages.file_id`），评论挂在文件/消息上（`comments.target_*`），但内容本身是纯文本——**用户无法在内容中嵌入其他实体的富媒体预览**。粘贴一个文件 ID 不会自动渲染为文件卡片，粘贴一个消息 ID 不会出现消息引用气泡，粘贴一个外部 URL 不会展开成标题+描述+图片的链接卡片。

这不是一个"缺失功能"——这是**缺失一个通用的嵌入基础设施**。

### 代码级证据

**证据 1：Message.Content 纯文本模型**

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    Kind      MessageKind  // 1=text, 2=image, 3=file, 4=system
    Content   string       // ← 纯字符串。无结构化的实体嵌入
    FileID    *ID          // ← 唯一的附件机制，且只能挂一个文件
    Meta      map[string]any
    // ...
}
```

`Kind=2`（图片消息）和 `Kind=3`（文件消息）的呈现依赖客户端在 `FileID` 上额外查询文件 API 获取元信息。没有内联渲染机制。

**证据 2：Content 无文本内引用解析**

当用户在消息中粘贴 "请参考 file:abc-123" 或 "看这条评论 cmt:xyz-789"，没有任何 service 层或 handler 层会解析这些引用。`Content` 被直接存储和返回：

```go
// internal/message/service.go — Send 方法
func (s *service) Send(ctx context.Context, in SendMessageInput) (*domain.Message, error) {
    // ... 验证、权限检查 ...
    msg := &domain.Message{
        Content: in.Content,  // ← 原始文本，不做引用解析
        // ...
    }
    // 写入 DB + outbox 事件
}
```

**证据 3：无实体引用 schema**

没有任何数据库表或模型来存储实体间的引用关系。`FileID` 是唯一的跨实体引用字段，且只存在于消息模型：

```
实体引用关系（当前）：
  Message → File       (via FileID, 可选)
  Comment → File       (via target_type=file + target_id)
  Comment → Message    (via target_type=message + target_id)

实体引用关系（缺失）：
  Message → Message    (引用/回复消息)
  Message → Comment    (引用评论)
  Comment → Comment    (引用另一个评论)
  File     → Message   (文件描述中引用消息)
  任何实体 → 外部 URL  (链接预览)
```

**证据 4：文件列表 API 不返回缩略图/预览 URL**

```go
// internal/file/file.go — File 领域模型
type File struct {
    ID          ID
    Name        string
    Size        int64
    MimeType    string
    StorageKey  string
    // 无 ThumbnailKey, PreviewKey, Width, Height, Duration
}
```

即使是图片/视频文件，API 也只返回原始文件的 presigned URL。客户端必须完整下载才能显示预览。

**证据 5：FileHandler 的 DownloadURL 只返回原始文件**

```go
// internal/file/handler_http.go — 注册的路由
// GET  /v1/files/:id/url     → DownloadURL (返回 presigned 原始文件 URL)
// 无 GET  /v1/files/:id/thumbnail → 无缩略图端点
```

### 产品经理视角

这是 **UX 质感差距最大**的单一缺口。在 2026 年的即时通讯产品中，链接预览、消息引用、文件卡片是用户期望的基础能力。缺失这些能力意味着：

1. **信息密度低** — 分享一个文件时，接收方看不到文件名、大小、类型的卡片
2. **上下文丢失** — "请看我在文件 X 的评论 Y" 需要用户手动查找
3. **移动端体验差** — 带宽受限的移动设备需要下载整个文件才能判断是否值得打开
4. **外部链接零转化** — 粘贴 URL 不展开预览，降低内容消费的点击率

### 架构师视角

这需要一套 **异步嵌入解析管线**：

```
写入路径：
  Content 文本
    ↓
  Entity Reference Parser（识别 entity:uuid 模式和 URL 模式）
    ↓
  异步 Outbox Event（如 embed.parse_needed）
    ↓
  Embed Resolver Worker：
    ├→ 内部实体解析（file/message/comment → 查询元数据）
    └→ 外部 URL unfurling（HTTP GET + Open Graph / oEmbed 解析）
    ↓
  嵌入元数据写入 `embeds` 表（entity_type, entity_id, ref_type, ref_id, preview_data JSONB）
```

读取路径（惰性求值——解析在写后进行，读时不阻塞）：

```sql
CREATE TABLE embeds (
    id              UUID PRIMARY KEY,
    source_type     TEXT NOT NULL,   -- 'message' | 'comment' | 'file'
    source_id       UUID NOT NULL,
    ref_type        TEXT NOT NULL,   -- 'file' | 'message' | 'comment' | 'url'
    ref_id          TEXT,            -- 对于内部实体是 UUID; 对于 URL 是其规范化形式
    preview_data   JSONB,            -- {title, description, image_url, ...}
    status         SMALLINT NOT NULL DEFAULT 0,  -- 0=pending, 1=resolved, 2=failed
    created_at     TIMESTAMPTZ NOT NULL,
    resolved_at    TIMESTAMPTZ
);

-- 查询：当客户端获取消息/评论内容时，JOIN embeds 获取所有嵌入预览
```

**影响范围**：
- 新增 `internal/embed/` 包（parser + resolver + repository）
- 新增 `embeds` 表（迁移 #0024）
- 消息/评论 API 返回体增加 `embeds[]` 字段
- Outbox 新增 `embed.parse` 事件类型
- ImageMagick / FFmpeg 可选依赖（缩略图生成）
- 外部 URL unfurling 需要 HTTP 客户端 + Open Graph 解析

**边界情况**：
- **URL 解析超时** — 外部 URL 可能慢/失败；状态标记为 failed，写重试退避
- **循环引用** — A 引用 B 引用 A；解析器需检测，不做嵌入
- **隐私** — 引用私密文件时，只有有权访问的用户能看到预览；嵌入解析在 service 层做 ACL 守卫
- **内容更新** — 引用的实体被编辑/删除后，嵌入预览需失效或更新

---

## 方向二：消息调度、草稿与发送控制（Message Scheduling, Drafts & Delivery Control）

### 问题陈述

消息发送是即时且不可逆的。用户无法：
- 提前撰写消息并定时发送
- 保存未完成的草稿
- 撤回已发送的消息（recall）
- 设置消息过期自动销毁

当前的消息模型（`SendMessageInput` → Send → outbox → notification）是一条严格同步的发送流水线，没有任何时间维度的控制。

### 代码级证据

**证据 1：Send 方法无调度参数**

```go
// internal/message/message.go — Service 接口
type Service interface {
    Send(ctx context.Context, in SendMessageInput) (*domain.Message, error)
    SendSystem(ctx context.Context, toUID, content string, meta map[string]any) (*domain.Message, error)
    // 无 SendScheduled, 无 SendAt, 无 Draft 相关方法
    // 无 Recall, 无 Withdraw 相关方法
}

type SendMessageInput struct {
    ToUID   string
    Kind    domain.MessageKind
    Content string
    FileID  *domain.ID
    Meta    map[string]any
    // 无字段: ScheduledAt, DraftID
}
```

**证据 2：Message 模型无调度/状态字段**

```go
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
    // 无 Status (draft/scheduled/sent/recalled)
    // 无 ScheduledAt
    // 无 ExpiresAt
    // 无 RecalledAt
}
```

**证据 3：消息发送后立即持久化并分发通知**

```go
// internal/message/service.go — Send 伪代码
func (s *service) Send(ctx context.Context, in SendMessageInput) (*domain.Message, error) {
    actor, _ := mustActor(ctx)
    // ... 校验 ...
    msg := &domain.Message{
        ID:        idgen.NewID(),
        FromUID:   actor,
        ToUID:     in.ToUID,
        Content:   in.Content,
        Kind:      in.Kind,
        CreatedAt: time.Now().UTC(),
    }
    evt := outbox.NewEvent("message", msg.ID.String(), "message.sent", messageSentPayload{...})
    if err := s.messages.Create(ctx, msg, evt); err != nil { // ← 一个事务写 DB + outbox
        return nil, err
    }
    return msg, nil
}
```

消息一旦创建，outbox relay 立即拾取事件并投递通知。没有"暂存→审核→发送"的工作流，没有延迟发送路径。

**证据 4：MessageRepository 无草稿/调度查询**

```go
// internal/message/message.go — MessageRepository 接口
type MessageRepository interface {
    Create(ctx context.Context, m *domain.Message, evt *outbox.Event) error
    Get(ctx context.Context, id domain.ID) (*domain.Message, error)
    ListInbox(...)
    ListConversation(...)
    MarkRead(ctx context.Context, id domain.ID, at time.Time) error
    // 无 ListDrafts, 无 ListScheduled, 无 MarkRecalled
}
```

**证据 5：HAR-002（设计文档 §3.3）明确指出 messages.file_id 不级联删除，但无任何回忆/撤销机制**

撤回消息需要完整的 recall 工作流：
1. 标记消息状态为 `recalled`（不可见但保留记录）
2. 发送 `message.recalled` outbox 事件
3. 通知接收方（"消息已被撤回"）
4. 可选：从所有接收方设备删除本地缓存

当前 `SoftDelete`（删除方可见）与 recall（双方都不可见）语义不同。

### 产品经理视角

这是 **成熟消息平台与 MVP 消息的区分线**。

1. **定时发送** — 用户在深夜撰写消息但不希望打扰对方，或希望在对方工作时间发送。这在 Slack、Telegram 等平台已是标配
2. **草稿同步** — 用户在 Web 上开始撰写消息，希望在移动端继续完成。当前无草稿持久化
3. **消息撤回** — "发错了"是最常见的用户反馈之一。当前只有编辑（Edit）——但编辑无法删除已读的敏感内容
4. **阅后即焚** — 临时会话的隐私需求（虽然当前为 MVP 范围外，但消息过期是基础架构能力）

### 架构师视角

**定时发送** 可以利用现有的 outbox 基础设施：

```sql
-- 给 messages 表增加调度字段
ALTER TABLE messages ADD COLUMN status         SMALLINT NOT NULL DEFAULT 1;  -- 1=draft, 2=scheduled, 3=sent, 4=recalled
ALTER TABLE messages ADD COLUMN scheduled_at   TIMESTAMPTZ;                 -- NULL = 立即发送
ALTER TABLE messages ADD COLUMN expires_at     TIMESTAMPTZ;                 -- NULL = 永不过期
ALTER TABLE messages ADD COLUMN recalled_at    TIMESTAMPTZ;
```

实现方案：
1. **草稿** — `status=draft`，在 `/v1/messages/drafts` 端点读写，不触发 outbox
2. **定时发送** — 新增 `scheduled.message.send` outbox 事件类型 + `ScheduledMessageWorker` 轮询 `scheduled_at`。这是一个通用的"延迟任务"基础设施——与 outbox relay 同构，但按调度时间触发
3. **撤回** — 新端点 `POST /v1/messages/:id/recall` → `status=recalled` + outbox 事件 `message.recalled` → 通知接收方
4. **过期** — sweeper 定期扫描 `expires_at IS NOT NULL AND expires_at < now()` → 自动软删或真正硬删

**边界情况**：
- **定时发送在发送前编辑** — 用户修改已调度的消息草稿需要合并逻辑
- **撤回窗口** — 大多数平台只允许发送后几分钟内撤回（Telegram 48h, WhatsApp ~1h）。撤回超时需配置
- **多设备草稿同步** — 草稿需跨设备同步（当前无此能力，但 status=draft 模式天然支持）
- **重试发送** — 定时发送如果因为目标用户被 block 而失败，需要回退状态为 draft 并通知用户

---

## 方向三：API 消费者契约与传输优化（API Consumer Contract & Transport Optimization）

### 问题陈述

当前 aero-id 的 HTTP 和 gRPC API 是全量返回的"胖响应"模式。所有列表/详情接口返回完整实体字段，没有任何机制让客户端选择需要的字段、批量获取资源、或协商更高效的序列化格式。对于移动端、低带宽场景或高频调用方，这造成了不必要的传输浪费和延迟。

### 代码级证据

**证据 1：无字段选择/部分响应支持**

```go
// internal/domain/file.go
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64
    MimeType    string
    StorageKey  string    // ← 服务端内部使用的 key，客户端不需要
    StorageType string    // ← 客户端不需要知道
    Checksum    string    // ← 客户端不需要
    Visibility  Visibility
    Meta        map[string]any
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

// GET /v1/files/:id 返回完整的 File 结构，包括 StorageKey/StorageType/Checksum
```

移动端获取文件列表时，`StorageKey`、`Checksum`、`StorageType` 字段占用不必要的带宽。对于每页 20 条记录、每条多出 ~100 字节无用字段，20KB 的响应中可能有一半是浪费的。

**证据 2：无批量操作端点**

```go
// HTTP 路由
// DELETE /v1/files/:id        → 单条删除
// DELETE /v1/messages/:id     → 单条删除
// 无 POST /v1/files/batch-delete → 批量删除
// 无 POST /v1/messages/batch-delete → 批量删除

// gRPC proto
service FileService {
    rpc GetFile(GetFileRequest) returns (GetFileResponse);
    rpc DeleteFile(DeleteFileRequest) returns (DeleteFileResponse);
    // 无 BatchGetFiles, 无 BatchDeleteFiles
}
```

用户想要删除 50 条过期消息，当前需要 50 次 API 调用。

**证据 3：无条件请求（ETag/If-None-Match）**

```go
// internal/handler/http/file.go — Get 方法
func (h *FileHandler) Get(c *gin.Context) {
    id, err := idgen.Parse(c.Param("id"))
    // ...
    file, err := h.svc.Get(ctx, id)
    // ← 直接返回 200 + 完整实体。无 ETag 检查，无 If-None-Match → 304 逻辑
    httpx.OK(c, fileToResponse(file))
}
```

高频刷新的客户端（如桌面同步客户端）无法使用缓存，每次都要拉取完整响应。

**证据 4：列表 API 不支持仅返回特定字段**

```go
// HTTP: GET /v1/files → 总是返回完整的 File 列表
// 无 ?fields=id,name,size,mime_type → 无字段选择
```

搜索 API 同样返回完整实体：

```go
// internal/search/search.go — SearchHit 包含完整的 Message/Comment/File
type SearchHit struct {
    Kind    SearchKind
    Message *Message   // ← 完整 Message，可能只匹配了 content 字段
    Comment *Comment   // ← 完整 Comment
    File    *File      // ← 完整 File
}
```

搜索结果页可能只需要显示标题/摘要，但返回了全部字段。

**证据 5：HTTP 响应压缩配置缺失**

```go
// internal/app/app.go — HTTP server 配置
httpServer := &http.Server{
    Addr:         ...,
    Handler:      router,    // ← 无 gzip/compress 中间件
    ReadTimeout:  ...,
    WriteTimeout: ...,
}
```

大列表响应（如 /v1/notifications 返回 100 条含用户信息的通知）在移动网络上传输未压缩的 JSON。

### 产品经理视角

1. **移动端体验** — 用户在弱网（3G/地铁）场景下，每次列表刷新都要下载完整实体数据。部分响应 + 压缩可将传输量减少 60-80%
2. **批量操作** — 管理后台/批量处理场景下，单条操作的 N+1 API 调用是显著的 UX 摩擦
3. **第三方集成** — 如果未来开放 API 给第三方开发者（方向五），胖响应和缺少缓存机制直接增加了他们的调用成本和复杂度
4. **搜索体验** — 搜索结果返回完整实体对象，但用户通常只看到标题+摘要+时间。多余的字段传输降低了搜索页面的加载速度

### 架构师视角

这四个优化可以分层实现，成本递增：

**1. HTTP 响应压缩（低投入，立竿见影）**

```go
// 在 router 中间件链中添加 gzip
import "github.com/gin-contrib/gzip"
e.Use(gzip.Gzip(gzip.DefaultCompression))
```

移动端 JSON 压缩率可达 5-10x。5 行代码改动。

**2. 字段选择（fields 查询参数，中投入）**

```go
// 内部复用模式
type FieldSelector []string

func ParseFieldSelector(s string) FieldSelector { ... }

func (f FieldSelector) SelectFile(file *domain.File) map[string]any {
    m := make(map[string]any, len(f))
    for _, field := range f {
        switch field {
        case "id":        m["id"] = file.ID
        case "name":      m["name"] = file.Name
        case "size":      m["size"] = file.Size
        // ...
        }
    }
    return m
}
```

- 通用实现：在 `internal/pkg/httpx` 中增加 FieldSelector 工具
- 每个实体 handler 适配
- gRPC 侧通过 `google.protobuf.FieldMask` 支持

**3. ETag + 条件请求（中投入）**

```go
// 为每个实体计算轻量级哈希
func fileETag(f *domain.File) string {
    h := sha256.New()
    h.Write([]byte(f.ID.String()))
    h.Write([]byte(f.UpdatedAt.Format(time.RFC3339Nano)))
    h.Write([]byte(f.Name))
    return hex.EncodeToString(h.Sum(nil))
}
```

- 适用于频繁轮询的场景（文件同步、通知列表）
- 服务端需缓存 ETag（或使用 UpdatedAt 隐式 ETag）

**4. 批量操作端点（高投入，需设计/安全性论证）**

```go
// HTTP: POST /v1/files/batch
// Body: { "action": "delete", "ids": ["id1", "id2", ...] }
// gRPC: rpc BatchDeleteFiles(BatchDeleteFilesRequest) returns (BatchDeleteFilesResponse)
```

- 批量操作必须原子或提供结果级别的错误报告
- 需要幂等性（现有 idempotency key 机制扩展）
- 需要考虑批量大小限制（防滥用）

**边界情况**：
- **字段选择与兼容性** — 新增字段不应破坏字段选择语义；排除字段应返回错误而非静默忽略
- **ETag 与软删除** — 软删除后 ETag 需要更新；如果实体被删除后恢复，ETag 必须改变
- **批量操作的部分失败** — 需要结构化错误响应 `{ "failed": [{ "id": "x", "error": "not_found" }] }`

---

## 方向四：实体变更历史 & 内容版本化（Entity Change History & Content Versioning）

### 问题陈述

当前消息和评论支持编辑（`edited_at`），但编辑过程完全不可追踪——一旦保存，旧版本永久丢失。文件支持元数据更新（PATCH），但文件内容无法更新（无覆盖上传），更没有版本历史。

对于需要审计、合规或内容恢复的场景，缺乏版本历史是一个严重缺口。

### 代码级证据

**证据 1：编辑是破坏性覆盖**

```go
// internal/repository/message.go — UpdateContent
func (r *repo) UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error {
    return r.db.WithContext(ctx).
        Model(&domain.Message{}).
        Where("id = ?", id).
        Updates(map[string]any{
            "content":    content,   // ← 覆盖原内容，前一个版本永远丢失
            "edited_at":  at,
        }).Error
}

// internal/repository/comment.go — UpdateContent 同理
func (r *repo) UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error {
    return r.db.WithContext(ctx).
        Model(&domain.Comment{}).
        Where("id = ?", id).
        Updates(map[string]any{
            "content":   content,    // ← 同样覆盖
            "edited_at": at,
        }).Error
}
```

**证据 2：Message 模型有 EditedAt 但无 PreviousVersions**

```go
// internal/domain/message.go
type Message struct {
    // ...
    EditedAt  *time.Time     // ← 存在，但只标记"编辑过"
    // 无 PreviousVersions, 无 EditHistory
}
```

**证据 3：文件内容不可覆盖更新**

```go
// internal/file/file.go — Service 接口
type Service interface {
    Create(...)           // 新文件
    UpdateMetadata(...)   // 仅更新 name/visibility/meta，不更新内容
    Delete(...)
    // 无 ReplaceContent, 无 NewVersion, 无 Overwrite
}
```

文件上传后，其内容（StorageKey 指向的 blob）是不可变的。用户如需更新文件内容，必须上传新文件（新 ID）再删除旧文件。没有版本关联。

**证据 4：AuditLogger 记录操作但不记录状态快照**

```go
// internal/domain/audit.go
type AuditEvent struct {
    ActorUID    string
    Action      string          // "message.delete", "file.share.grant"
    SubjectType string
    SubjectID   string
    Meta        map[string]any  // ← 当前只记录操作元数据，不记录编辑前后的内容快照
    CreatedAt   time.Time
}
```

对于编辑操作，审计日志没有记录 `{before: "...", after: "..."}`。

**证据 5：gRPC proto 无版本相关字段**

```protobuf
message Message {
    // ... 现有字段 ...
    google.protobuf.Timestamp edited_at  = 9;
    // 无 int32 version, 无 repeated EditHistoryEntry history
}

message File {
    // ...
    // 无 int32 version_count
}
```

### 产品经理视角

1. **内容恢复** — 用户编辑了评论但后悔了，或文件被误修改后需要回退到前一天版本。这是被大量用户请求的基础能力
2. **合规审计** — 对于受监管的内容，必须能够证明"谁在什么时间改了什么东西"。当前的破坏性编辑无法满足合规要求
3. **协作透明** — 在协作场景中，查看内容的编辑历史有助于理解其演变过程
4. **差异对比** — 用户需要知道两个版本之间改了哪些内容（diff）

### 架构师视角

版本化可以采用 **快照表** 模式（简单，对主查询无侵入）：

```sql
CREATE TABLE message_versions (
    id          UUID PRIMARY KEY,
    message_id  UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    content     TEXT NOT NULL,
    edited_by   TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    version     INT NOT NULL,         -- 单调递增版本号
    diff        TEXT,                 -- 可选：与前一版本的差异（JSON patch 格式）
    UNIQUE (message_id, version)
);

CREATE TABLE comment_versions (
    -- 同理
);

CREATE TABLE file_versions (
    id              UUID PRIMARY KEY,
    file_id         UUID NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    storage_key     TEXT NOT NULL,     -- 每个版本新 blob
    name            TEXT NOT NULL,
    size            BIGINT NOT NULL,
    checksum        TEXT,
    mime_type       TEXT NOT NULL,
    created_by      TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    version         INT NOT NULL,
    UNIQUE (file_id, version)
);
```

**实现要点**：

1. **编辑时自动快照** — `UpdateContent` 和 `File.Create`/`UpdateMetadata` 在更新行之前，先将当前版本的完整内容 INSERT 到 `*_versions` 表
2. **版本号** — 每次编辑递增版本号，在应用层或 DB 层通过 `SELECT COALESCE(MAX(version), 0) + 1 FROM message_versions WHERE message_id = ?` 保证
3. **API** — `GET /v1/messages/:id/versions`（列出版本）、`GET /v1/messages/:id/versions/:version`（获取特定版本）、`POST /v1/messages/:id/restore?version=N`（回滚）
4. **清理策略** — 配置化的版本保留策略（保留最近 N 个版本或保留 N 天）。由 storage GC 类似的 sweeper 执行
5. **对主查询的零影响** — 版本表只在对版本历史有显式请求时才查询。主列表/详情查询仍然是单表扫描

**文件版本的特殊性**：
- 每个文件版本指向不同 `storage_key`（新 blob），因此需要额外的存储 GC 来处理被取代的旧 blob
- 文件版本的回滚意味着恢复旧 blob 的引用 + 更新当前版本的元数据
- 内容去重仍然有效——同一 checksum 的版本共享同一存储 key，引用计数同步更新

**边界情况**：
- **编辑并发** — 两个请求同时编辑同一消息。版本号需要乐观锁 `UPDATE ... WHERE version = :old_version`
- **版本保留** — 无限制保留导致存储成本膨胀。默认保留最近 10 个版本或 30 天
- **软删除与版本** — 消息软删除后，版本记录是否保留？建议保留（可审计），用户界面仅显示"内容已被删除"
- **大文件版本** — 文件版本支持需要先实现存储引用计数（已完成 `storage_refs` 表）来安全回收旧 blob

---

## 方向五：用户数据可移植性 & 账户生命周期管理（User Data Portability & Account Lifecycle Management）

### 问题陈述

aero-id 持有用户的文件、消息、评论、通知等全部个人数据，但**当前没有任何 API 允许用户导出他们的数据、查看数据使用情况、或发起账户删除**。这是一个在产品合规（GDPR、CCPA）和用户体验双方面的严重缺口。

### 代码级证据

**证据 1：无数据导出端点**

所有列表 API 都是分页查询，不支持全量导出：

```go
// HTTP 路由
// GET  /v1/files          → 分页列表
// GET  /v1/messages       → 分页收件箱
// GET  /v1/conversations/:uid → 分页对话
// GET  /v1/comments       → 分页评论
// 无 GET  /v1/export           → 全量数据导出
// 无 GET  /v1/export/files     → 文件批量下载
// 无 GET  /v1/export/messages  → 消息导出（JSON/CSV）
```

**证据 2：无账户删除端点**

```go
// Admin API
// GET /v1/admin/audit-log → 审计日志查询
// 无 DELETE /v1/account          → 用户自行删除账户及所有数据

// gRPC proto
service FileService {
    // ... 38 个 RPC
    // 无 DeleteAccount, 无 ExportAccountData
}
```

**证据 3：软删除数据无限累积**

所有实体使用 GORM 的软删除（`deleted_at`）：

```go
// migrations/000001_create_files.up.sql
CREATE TABLE files (
    -- ...
    deleted_at TIMESTAMPTZ     -- ← 软删除标记
);

// migrations/000002_create_messages.up.sql
CREATE TABLE messages (
    -- ...
    deleted_at TIMESTAMPTZ     -- ← 软删除标记
);
```

当前 storage GC sweeper 清理软删除的文件行（opt-in），但消息、评论、通知、文件分享、审计日志的软删除数据永不清零。

```go
// internal/storage/gc/sweeper.go — 只处理文件
func (s *Sweeper) Run(ctx context.Context) (Stats, error) {
    purged, blobs, err := s.purgeSoftDeleted(ctx)  // ← 只 purge files 表
    // 不处理 messages, comments, notifications 的软删除
}
```

**证据 4：AuditLogger 是 fire-and-forget——无法保证审计日志完整性**

```go
// internal/service/file_share.go
if s.audit != nil {
    _ = s.audit.Log(ctx, &domain.AuditEvent{...})  // ← 错误被静默忽略
}
```

审计日志的缺失不影响主操作——但如果审计日志是合规需求，这种 fire-and-forget 模式不满足要求。

**证据 5：无数据使用情况统计**

```go
// HTTP API
// GET /v1/files         → 文件列表
// GET /v1/messages      → 消息列表
// 无 GET /v1/account/usage → 显示：存储使用量、消息总数、评论总数、历史下载量
```

用户不知道他们用了多少存储空间、发了多少消息。

### 产品经理视角

1. **合规刚需** — GDPR 第 15 条（访问权）和第 17 条（被遗忘权）直接要求数据导出和账户删除能力。CCPA 的删除权同理
2. **用户信任** — "我可以随时带走我的数据"是建立用户信任的基础。没有导出功能的平台会让人有"数据被锁定"的感觉
3. **用户自助服务** — 当前账户删除必须由管理员手动操作（如果有 admin API 的话），不支持用户自助发起
4. **透明度** — 用户不清楚自己的数据规模。存储使用量看板可以促使用户主动清理不必要的文件，减轻存储成本

### 架构师视角

**1. 数据导出系统**

```go
type ExportRequest struct {
    Formats map[string]string // "files" → "zip", "messages" → "json", "comments" → "csv"
    // 可选: 日期范围, 数据范围
}

// 异步导出工作流：
// POST /v1/exports → 创建导出任务
// GET  /v1/exports/:id → 查询进度
// GET  /v1/exports/:id/download → 下载结果（presigned URL，限时有效）
```

导出作为异步任务实现：
- 创建导出请求 → 写入 `exports` 表 → outbox 事件 `export.requested`
- Export Worker 消费事件：
  - 流式查询用户所有数据
  - 写入临时存储（与文件存储共用同一 `FileStorage` 接口）
  - 完成后标记状态并发送通知
- 文件导出通过文件的 `PresignGet` 生成临时下载 URL 列表，打包为 manifest JSON，而非直接打包二进制流（对于大文件更高效）

**2. 账户删除系统**

```go
// DELETE /v1/account → 标记账户删除
// 工作流：
// 1. 验证身份（重新输入密码/token）
// 2. 设置 grace period（默认 7 天，可恢复）
// 3. grace period 到期后：
//    a. 硬删除（或匿名化）所有文件行 → storage GC 清理 blob
//    b. 消息匿名化（from_uid → "deleted_user", content → ""）
//    c. 评论匿名化同理
//    d. 清除通知
//    e. 清除审计日志中关联的事件
```

**3. 资源使用量看板**

```go
// GET /v1/account/usage → {
//   "storage": { "used_bytes": 123456789, "file_count": 42 },
//   "messages": { "total": 1234, "unread": 5 },
//   "comments": { "total": 89, "hidden": 1 },
//   "shares": { "granted": 12, "received": 5 }
// }
```

**4. 软删除数据生命周期策略**

```go
// 为所有实体添加统一的清理策略：
type CleanupPolicy struct {
    Entity    string        // "messages" | "comments" | "notifications" | "audit_events"
    Retention time.Duration // 软删除数据保留多久
    BatchSize int
}
```

**边界情况**：
- **导出超时** — 大型用户导出可能需要数小时；需要异步执行 + 进度通知
- **导出存储** — 导出文件也需要存储空间；设置过期自动删除（如 7 天后清理）
- **账户删除与内容依赖** — 删除用户后，对方对话中的消息显示为"已删除用户"。这需要在 message 和 comment 中保留引用（匿名化而非删除）
- **Grace period 中的操作** — 用户在 grace period 内登录后，应能取消删除请求
- **审计日志保留** — 即使账户删除，审计事件可能需按法规保留更长时间（如金融行业 5-7 年）

---

## 优先级排序

| 优先级 | 方向 | 投入 | 业务价值 | 技术依赖 |
|--------|------|------|---------|---------|
| **P0** | 方向三：API 传输优化 | 低-中 | 中（用户体验 + 带宽成本） | 无 |
| **P1** | 方向五：数据可移植性 | 中 | 高（合规 + 用户信任） | 现有 Export 基础设施 |
| **P1** | 方向四：版本化 | 中-高 | 中-高（审计 + 恢复） | 依赖存储引用计数（已实现） |
| **P2** | 方向一：富媒体嵌入 | 中-高 | 高（产品质感） | 依赖 outbox worker |
| **P2** | 方向二：消息调度 | 高 | 中-高 | 依赖延迟任务基础设施 |

### 建议的迭代路线

- **短期（1-2 周）**：方向三中的 HTTP 压缩 + ETag 支持。低投入、立竿见影
- **中期（2-4 周）**：方向五（数据导出 + 使用量看板）配合方向四的基础版本快照机制。账户删除需最后做，因为涉及多个实体的级联处理
- **长期（4-8 周）**：方向一的嵌入解析管线 + 方向二的调度/撤回。这两个方向共享延迟任务基础设施（通用的 ScheduledTaskRunner）

---

## 总结

五个方向的共同主题是 **从一个功能完整的"后台服务"演进为一个面向用户的产品平台**：

1. **富媒体嵌入** 提升内容消费的 UX 质感
2. **消息调度/撤回** 完善消息核心体验
3. **API 传输优化** 降低带宽成本、提升弱网体验
4. **版本历史** 提供审计追溯和内容恢复能力
5. **数据可移植性** 满足合规要求并建立用户信任

这些方向与已有 96+ 份分析文档中覆盖的群聊、搜索、通知、文件管线、SSO 弹性等方向**不冲突且互补**——它们是 aero-id 在完成基础设施和后管线建设后，面向最终用户的自然演进。
