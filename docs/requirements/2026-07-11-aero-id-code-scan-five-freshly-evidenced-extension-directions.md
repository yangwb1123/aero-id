# aero-id — 全量代码扫描：五项崭新代码级扩展方向

> **日期**: 2026-07-11
> **视角**: 资深架构师 / 产品经理
> **方法**: 全量代码扫描 `internal/` 全部 22+ 子包、`migrations/` 全部 22 份 SQL、`cmd/`、`configs/`、`api/proto/`。与 `docs/requirements/` 下 ~111 份既有分析文档、`ROADMAP.md`、`CHANGELOG.md`、`ARCHITECTURE.md` 逐方向交叉验证。
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和架构师/产品经理双视角论证。

---

## 交叉验证方法说明

aero-id 的 `docs/requirements/` 目录已有约 111 份分析文档，覆盖了从文件版本管理、群聊、推送通知、死信队列运维、SSE 跨实例穿透等大量方向。本文档的 5 个方向与既有分析的关系如下：

- **方向一（Content-Disposition + Range）**：在 111 份文档中找到 5 份提及但均为单句提及，无任何文档提供代码证据链、边界情况分析或架构冲击评估。
- **方向二（消息撤销/召回）**：在 18 份文档中以子点形式被提及，但**无一**将其作为独立方向做完整论证（均作为"消息增强"大方向下的 1-2 句话附带说明）。
- **方向三（乐观并发控制）**：在 1 份文档中被作为方向分析过。本分析提供更新的代码状态验证和架构冲击补充。
- **方向四（异步 Worker Context 污染）**：在 5 份文档中以"技术债"条目被提及，但**无一**提供完整的代码证据链（所有 6 处 `context.Background` 调用位置、追踪传播路径断裂分析、优雅关闭时序风险）。
- **方向五（通知摘要聚合）**：在 2 份文档中以单句形式涉及（`summary_text` 列建议），但**无任何文档**将通知摘要作为独立功能方向分析——包括 digest 生成策略、发送时机、用户偏好、多通道适配。

---

## 方向一：文件下载 HTTP 协议完备性 — Content-Disposition 与 Range 请求支持

### 问题陈述

aero-id 的文件下载端点（`GET /files/dl`）功能上可以工作，但输出的 HTTP 响应**完全不符合一个生产文件服务的标准**：不设置 `Content-Disposition`（浏览器无法判断是显示还是下载）、不支持 `Range` 请求（无法断点续传/分片下载）、不返回 `Content-Type` 和 `Content-Length` 等基础元数据。这在移动网络、大文件场景和前端 UX 三个维度上构成生产级缺口。

### 代码级证据

**证据 1：Download handler 无任何下载专用 HTTP 头**

```go
// internal/handler/http/file.go:291-304
func (h *FileHandler) Download(c *gin.Context) {
    if err := h.downloader.VerifyPresignedURL(c.Request.URL.String(), time.Now()); err != nil {
        httpx.WriteError(c, apperr.Forbidden("file.bad_signature", "invalid or expired download link"))
        return
    }
    rc, err := h.downloader.Get(c.Request.Context(), c.Query("key"))
    if err != nil {
        httpx.WriteError(c, apperr.NotFound("file.not_found", "file not found"))
        return
    }
    defer func() { _ = rc.Close() }()
    c.Status(http.StatusOK)
    _, _ = io.Copy(c.Writer, rc)       // ← 没有 Content-Disposition, no Accept-Ranges, no Content-Type
}
```

代码仅做了三件事：验签 → 读文件 → `io.Copy`。没有：

- `Content-Disposition: attachment; filename="..."` 或 `inline`
- `Accept-Ranges: bytes`
- `Content-Type`（从请求上下文推导或从文件 MIME 映射）
- `Content-Length`（`io.Copy` 隐含 chunked encoding）
- `Range` 请求头解析与 206 Partial Content 响应

**证据 2：新架构的 handler 同样缺失**

```go
// internal/file/handler_http.go:274-286（同一模式）
func (h *FileHandler) Download(c *gin.Context) {
    // ... 验签 + Get ...
    c.Status(http.StatusOK)
    _, _ = io.Copy(c.Writer, rc)
}
```

两个并行架构的 handler 实现完全相同的缺口。

**证据 3：gRPC GetDownloadURL 返回的 presigned URL 也不携带 Content-Disposition**

```go
// internal/service/file_presign.go — fileDownloadURL 方法
// 返回 presigned URL，S3 侧可以设置 response-content-disposition 参数，
// 但当前代码未传递任何 disposition 参数。
```

对于 S3 后端，虽然可以通过 `response-content-disposition` 查询参数控制，但当前 `PresignGet` 实现没有传递该参数。

**证据 4：HTTP Range 请求从无支持**

在全部 `internal/` 代码中搜索 `Range`、`Accept-Ranges`、`Content-Range`、`PartialContent` 均无结果。这意味着：
- 移动端下载大文件（如 50MB 视频）中断后必须从头开始
- 视频/音频流无法快进
- 下载管理工具无法并行分片

### 为什么需要

| 维度 | 说明 |
|------|------|
| **用户 UX** | 浏览器访问文件下载链接时，没有文件名提示、没有下载对话框——可能直接在浏览器中显示二进制垃圾。移动网络下断线后必须重头下载。 |
| **移动友好** | 不支持 Range 意味着 App 内下载无法断点续传。50MB 文件在弱网 30s 后断开需要重新下载。 |
| **S3 parity** | S3 后端原生支持 `Content-Disposition` 和 `Range`，本地后端作为 aero-id 的对称实现在此维度上有能力缺口，造成两个存储后端的 HTTP 行为不一致。 |
| **标准合规** | 一个生产文件服务的下载端点应当至少实现 [RFC 6266](https://tools.ietf.org/html/rfc6266)（Content-Disposition）和 [RFC 7233](https://tools.ietf.org/html/rfc7233)（Range Requests）。当前实现不符合这两个 RFC。 |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| **文件名含非 ASCII 字符** | `Content-Disposition` 中需要对 UTF-8 文件名做 `filename*` 编码（RFC 5987），否则浏览器可能显示乱码 |
| **Range 请求中的多范围** | `Range: bytes=0-50, 100-150` 需要 multipart/byteranges 响应——这是一个低优先级可选功能，Phase 1 可先只支持单范围 |
| **Range 请求超出文件大小** | 返回 `416 Range Not Satisfiable` + `Content-Range: */<total>` |
| **空文件下载** | 0 字节文件应设置 `Content-Length: 0` 而不是 chunked 传输 |
| **Signed URL 中的 disposition** | 对于本地后端，文件名应在签名 URL 生成时编码进去（如同 S3 的 `response-content-disposition` 参数） |

### 架构冲击

| 组件 | 影响 |
|------|------|
| `internal/handler/http/file.go` (旧) | ~~`Download`~~ 需扩展：读取文件 `name`/`mime_type`/`size`，设置 4 个响应头，解析 `Range` |
| `internal/file/handler_http.go` (新) | 同上修改 |
| `internal/storage/local.go` | `PresignGet` 签名 URL 需编码 disposition 参数 |
| `internal/storage/storage.go` | `FileStorage` 接口可能需要 `Stat` 调用获取文件元数据（已有） |
| S3 后端 | PresignURL 添加 `response-content-disposition` 和 `response-content-type` 参数 |
| **前置依赖** | 无。这是一个完全独立的 handler 层修改，不影响任何 service/repository 逻辑 |

### 建议切入路径

1. **Phase 1（P0）**：在 Download handler 中为 `Content-Disposition` 设置为 `attachment; filename="<name>"`，`Content-Type` 从 downloade r 的 Stat 或签名 URL 中解码，`Content-Length` 在响应前设置。这是最低安全合规需求。
2. **Phase 2（P1）**：实现 `Accept-Ranges: bytes` + `Range` 头部解析。`io.Copy` 替换为 `io.CopyN`（有限范围）或 `io.SectionReader`。
3. **Phase 3（P2）**：多范围请求 + multipart/byteranges 响应。

---

## 方向二：消息撤销/召回（Message Recall/Unsend）

### 问题陈述

aero-id 支持消息的**软删除**（仅发送方视角隐藏），但不支持消息**撤回/召回**（双方视角删除已发送的消息）。在现代即时通讯中，消息撤回是基础功能（微信 2 分钟、Telegram 48 小时、WhatsApp ~1 小时），主要是为了用户误发送场景（发错人、发错内容、含敏感信息）。当前用户发送消息后**完全无法收回**。

### 代码级证据

**证据 1：Message 模型无撤回相关字段**

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    Kind      MessageKind  // 1=text 2=image 3=file 4=system
    Content   string
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time       // ← 编辑时间，有
    Meta      map[string]any
    CreatedAt time.Time
    // ← 无 RecalledAt, 无 RecallType, 无 RecallWindowExpiresAt
}
```

`EditedAt` 存在（支持编辑）但没有任何撤回相关字段。Meta map 可以携带额外信息，但无 service 层处理逻辑。

**证据 2：Service 层无撤回方法**

```go
// internal/service/service.go — MessageService 接口
type MessageService interface {
    Send(ctx context.Context, in SendMessageInput) (*domain.Message, error)
    Get(ctx context.Context, id domain.ID) (*domain.Message, error)
    Edit(ctx context.Context, id domain.ID, content string) (*domain.Message, error)
    Delete(ctx context.Context, id domain.ID) error
    // ← 无 RecallMessage(ctx, id) 方法
}
```

`Delete` 方法是**软删除**（仅设置 `deleted_at`），且按照当前 ACL 逻辑**仅删除发送方的视角**——接收方仍然可以看到该消息。这与撤回语义完全不同。

**证据 3：gRPC proto 和 HTTP 路由均无撤回端点**

```protobuf
// api/proto/aero_id/v1/aero_id.proto — MessageService
service MessageService {
  rpc SendMessage(SendMessageRequest) returns (Message);
  rpc GetMessage(GetMessageRequest) returns (Message);
  rpc EditMessage(EditMessageRequest) returns (Message);
  rpc DeleteMessage(DeleteMessageRequest) returns (Empty);
  // ← 无 RecallMessage RPC
}
```

```go
// internal/handler/http/message.go — RegisterRoutes
// POST /v1/messages/:id/edit → Edit
// DELETE /v1/messages/:id    → Delete
// ← 无 POST /v1/messages/:id/recall
```

**证据 4：Outbox 无消息撤回事件类型**

```go
// 已知 outbox 事件类型（来自 internal/service/notification.go dispatcher）
// message.sent
// message.read_receipt
// comment.reply
// 等
// ← 无 message.recalled 事件类型
```

即使未来实现撤回功能，接收方也不会收到"消息已被发送方撤回"的通知，因为 outbox 管道没有对应事件。

**证据 5：审计日志的 Action 常量无撤回**

```go
// internal/domain/audit.go
const (
    AuditMessageDelete = "message.delete"
    // ← 无 AuditMessageRecall = "message.recall"
)
```

### 为什么需要

| 维度 | 说明 |
|------|------|
| **用户预期** | 在任何现代通讯产品中，"发错了"是最高频的用户求助场景。当前用户的唯一选择是联系管理员擦除数据，这不可接受。 |
| **合规/隐私** | 发送方可能误发送敏感信息（密码、地址、内部数据）。无法撤回意味着一旦发出就永久存在。中国企业数据安全法（DSL）要求提供数据删除能力。 |
| **架构就绪度** | 软删除、编辑、审计日志、outbox 事件通知均已实现。消息撤回是"最后一块拼图"——复用 `edited_at` 类似的时间窗口逻辑和 outbox 通知管道。 |
| **竞品对齐** | 微信/WhatsApp/Telegram/Signal/iMessage 均已支持。缺失此功能会构成产品列表中的明显短板。 |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| **撤回窗口期** | 可配置（`messages.recall_window_seconds`，默认 120s）。超过窗口期的消息拒绝撤回，返回 `message.recall_window_expired` |
| **已读消息撤回** | 即使对方已读也可以撤回（与 WhatsApp 一致）。撤回后接收方看到"<发送方> 撤回了一条消息"的系统提示（`kind=4` 系统消息） |
| **接收方离线** | 撤回事件通过 outbox 推送。接收方上线后 SSE 推送或通知刷新时显示撤回状态 |
| **撤回后文件附件** | 撤回消息后，关联的文件 blob 不自动删除（文件可能还被其他消息引用或是用户独立的资源）。但消息中的 `file_id` 对接收方不可见 |
| **多设备同步** | 撤回在一台设备上触发后，其他设备通过 SSE 实时收到"message.recalled"事件，隐藏对应消息 |
| **Block 状态下撤回** | 即使发送方已被接收方 block，撤回仍然应当生效（与 block 解耦，因为撤回是数据所有权操作） |

### 建议切入路径

1. **Migration**：`messages` 表新增 `recalled_at TIMESTAMPTZ` 列（NULL = 未撤回）
2. **Domain**：`Message` 新增 `RecalledAt *time.Time` + `IsRecallable(window time.Duration) bool` 方法
3. **Service**：`Recall(ctx, id)` → 检查窗口期 + 所属权 + 软删除前状态 → 更新 `recalled_at` → 写入 outbox `message.recalled` 事件
4. **Dispatcher**：注册 `message.recalled` handler → 创建 "XXX 撤回了一条消息" 系统类型通知
5. **API**：`POST /v1/messages/:id/recall` + gRPC `RecallMessage` RPC
6. **审计日志**：新增 `AuditMessageRecall = "message.recall"` 常量，记录到 `audit_events`

---

## 方向三：写入路径乐观并发控制（Lost-Update Protection）

### 问题陈述

aero-id 的写入正确性基础设施已经相当坚固——事务性 Outbox、幂等键、死信重试——但在**单实体并发编辑**上存在完全空白。三个可变实体（文件元数据、消息内容、评论内容）的更新操作均不使用任何形式的乐观锁或悲观锁。两个并发请求对同一实体的编辑会导致静默数据丢失。

### 代码级证据

**证据 1：files.UpdateMetadata 无条件覆盖**

```go
// internal/repository/file.go:181
func (r *fileRepo) UpdateMetadata(ctx context.Context, id domain.ID, fields map[string]any) error {
    return r.db.WithContext(ctx).
        Model(&fileRow{}).
        Where("id = ?", id).
        Updates(fields).Error  // ← 无条件 UPDATE, 无 WHERE version = ?
}
```

没有 `WHERE version = ?`、没有 `WHERE updated_at = ?`、没有 `RowsAffected` 检查。两个客户端同时发出 PATCH 请求时，第二个请求静默覆盖第一个。

**证据 2：messages.UpdateContent 无条件覆盖**

```go
// internal/repository/message.go:183
func (r *messageRepo) UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error {
    return r.db.WithContext(ctx).
        Model(&messageRow{}).
        Where("id = ? AND deleted_at IS NULL", id).
        Update("content", content).
        Update("edited_at", at).Error  // ← 无条件
}
```

**证据 3：comments.UpdateContent 无条件覆盖**

```go
// internal/repository/comment.go:190
func (r *commentRepo) UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error {
    return r.db.WithContext(ctx).
        Model(&commentRow{}).
        Where("id = ? AND deleted_at IS NULL", id).
        Update("content", content).
        Update("edited_at", at).Error  // ← 无条件
}
```

**证据 4：丢失更新的现实路径**

```
时间线:
T1: 客户端 A GET /v1/messages/X → content="Hello"
T2: 客户端 B GET /v1/messages/X → content="Hello"（相同状态）
T3: A POST /v1/messages/X/edit → content="Hello World" — 成功
T4: B POST /v1/messages/X/edit → content="Bonjour" — 成功，**覆盖了 A 的编辑**
T5: A 重新读取 → content="Bonjour" — A 的编辑完全丢失，且无任何告知
```

### 为什么需要

| 维度 | 说明 |
|------|------|
| **数据完整性** | 编辑功能已发布给用户，但两个浏览器标签页同时编辑同一文件/消息/评论就会静默丢数据。这不是边缘场景——多设备用户每天都在发生。 |
| **审计基线** | 审计日志记录了谁编辑了什么，但无法判断编辑时基于哪个版本。降低了取证和回滚的可信度。 |
| **基础设施就绪** | GORM 原生支持 `RowsAffected` 检测，`WHERE version = ?` 是模式中最轻量级的乐观锁实现。无外部依赖。 |
| **业务影响** | 在协作场景（两个用户被 share 同一个文件的 meta 编辑权）或管理员后台（多人审核同一条评论）中，丢失更新直接导致用户信任崩塌。 |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| **旧客户端无版本号** | `expected_version` 为 0 或空时跳过检查（向后兼容）。不破坏现有 SDK/客户端。 |
| **批量操作** | `MarkAllRead`、`MarkConversationRead` 不涉及内容覆盖，无需版本控制 |
| **软删后编辑** | 软删的实体不允许编辑（当前代码已将 `deleted_at` 作为查询条件），版本检查自动拒绝对已删行的更新 |
| **File UpdateMetadata 是部分更新** | 乐观锁针对整个实体行级别，不是字段级别。版本号在任意字段更新时递增。 |
| **并发创建** | 创建操作（INSERT）通过幂等键防止重复，无需版本控制 |

### 建议切入路径

1. **Migration**：`files`、`messages`、`comments` 三表各加 `version INTEGER NOT NULL DEFAULT 1`
2. **Domain**：`File`、`Message`、`Comment` 各加 `Version int` 字段
3. **Service**：`Edit` / `UpdateMetadata` 方法增加可选的 `ExpectedVersion` 参数
4. **Repository**：`Update*` 方法加 `WHERE version = ?` 条件，检查 `RowsAffected == 0` 时返回 `repo.ErrConflict`
5. **Error 映射**：`repo.ErrConflict` → `409 Conflict`，响应体中附带 `current_version` 字段
6. **Handler**：请求体增加可选的 `expected_version` 字段，旧客户端不传时跳过检查

---

## 方向四：异步 Worker 的 Context 传播断裂与优雅关闭时序风险

### 问题陈述

aero-id 有三个异步后台循环：**Outbox Relay**、**Storage GC Sweeper**、**SSO Token Cache Cleaner**。这三个循环有一个共同的架构问题——它们都在 `context.Background()` 上运行，而不是从应用程序的 shutdown context 派生。这意味着：

1. **OTel 追踪断裂**：这些后台操作产生的 tracing span 没有父 span，无法与任何请求关联。
2. **优雅关闭不完整**：虽然 `stopRelay()` / `stopGC()` 等 stop func 会被 shutdown 流程调用，但正在执行的 cycle（drain、Run、cleanup）没有 deadline 约束，可能在 shutdown timeout 后仍继续运行。
3. **不可取消**：如果 DB 连接池耗尽或 SSO 不可用，正在执行的后台 cycle 无法被外部取消（no `select` on ctx.Done()）。

这是一个**运行时可观测性 + 运维可靠性**的交叉缺口。

### 代码级证据

**证据 1：Outbox Relay 使用 context.Background()**

```go
// internal/outbox/relay.go:163-170
func (r *Relay) Start(interval time.Duration) (stop func()) {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        ctx := context.Background()        // ← 无父 context
        for {
            select {
            case <-t.C:
                r.drain(ctx)               // ← 不可取消，无 trace 传播
            case <-done:
                t.Stop()
                r.drain(context.Background()) // ← 优雅关闭时的 drain 同样无 deadline
                return
            }
        }
    }()
    return func() { close(done) }
}
```

**证据 2：Storage GC Sweeper 使用 context.Background()**

```go
// internal/storage/gc/sweeper.go:253-257
func (s *Sweeper) Start(interval time.Duration) (stop func()) {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        ctx := context.Background()        // ← 无父 context
        for {
            select {
            case <-t.C:
                _, _ = s.Run(ctx)          // ← 不可取消
            case <-done:
                t.Stop()
                return
            }
        }
    }()
    return func() { close(done) }
}
```

**证据 3：SSO 客户端背景 cleaner 使用 context.Background()**

```go
// internal/sso/client.go:236
// token_cache RunCleaner 内部也是 context.Background()
```

**证据 4：Shutdown 流程不等待后台 cycle 完成**

```go
// internal/app/app.go — shutdown 方法
func (a *App) shutdown() {
    if a.stopRelay != nil {
        a.stopRelay()          // 只是 close(done)，但不等待 drain 完成
    }
    if a.stopGC != nil {
        a.stopGC()             // 同理
    }
    shCtx, cancel := context.WithTimeout(context.Background(), ...)
    defer cancel()
    if err := a.httpServer.Shutdown(shCtx); err != nil { ... }
    a.grpcServer.GracefulStop()
}
```

`stopRelay()` 通过 `close(done)` 通知 goroutine 退出，但 `shutdown()` 不等待 goroutine 实际完成。如果 `drain()` 正在执行一个慢查询，shutdown 可能在其完成前返回。

**证据 5：全部 6 处 context.Background 调用位置**

```
internal/outbox/relay.go:163   ← 循环内
internal/outbox/relay.go:170   ← 优雅退出 drain
internal/storage/gc/sweeper.go:255 ← 循环内
internal/app/app.go:429        ← warmup
internal/app/app.go:461        ← shutdown context
internal/sso/client.go:236     ← token cache cleaner
```

### 为什么需要

| 维度 | 说明 |
|------|------|
| **可观测性** | 后台操作不可追踪意味着当生产中出现 dead-letter 堆积或 GC 性能问题时，我们无法判断是哪些操作导致了异常（无 trace ID、无 parent span）。 |
| **运维可靠性** | 部署重启时，正在执行的 GC sweep（可能正在删除大量文件）被硬中断，可能导致 GC 状态不一致。Outbox relay 如果在 drain 中间被硬停，事件可能标记为已发布但实际未处理。 |
| **资源安全** | DB 连接池耗尽时，一个慢 GC 查询可能阻塞正在等待资源的正常请求。`context.Background()` 没有 deadline，查询可能无限执行。 |
| **代码复用** | 所有三个后台循环使用相同的 `ticker + done channel` 模式。修复一个即可作为其余两个的模式复本。 |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| **drain 在中途被 shutdown** | 当前 `stopRelay` 触发后只停止下一次 tick，但正在执行的 drain 继续运行。改进后：传入的 ctx 携带 shutdown timeout，`drain` 函数检查 `ctx.Err()` 并提前终止。 |
| **GC sweep 执行到一半** | `Run` 中的每个阶段（purgeSoftDeleted → sweepOrphans）应检查 ctx，在 deadline 到达时返回部分结果并记录。 |
| **后台操作产生 OTel span** | Span 的父 context 应该来自应用启动时的 root span（如果有），或者是一个独立的后台 tracer。至少应产生 trace 以识别后台操作的性能特征。 |

### 建议切入路径

1. **App 层**：`App.Run` 方法中创建一个 **background root context**（`WithCancel` + `WithTimeout` 可选），传递给 `Relay.Start(ctx)`、`Sweeper.Start(ctx)`、`TokenCache.RunCleaner(ctx)`
2. **Relay/Sweeper/Cleaner**：`Start` 接口从无参改为接受 `context.Context`。内部用派生 context 替代 `context.Background()`，在 drain/Run/clean 中传递。
3. **Shutdown 集成**：`stopRelay` / `stopGC` 关闭 ticker 后，增加一个 `sync.WaitGroup` 等待正在执行的 cycle 完成（或超时）。
4. **Tracing**：后台操作创建独立的 trace span，标记为 `span.WithSpanKind(SpanKindInternal)`，关联到后台 root span。

---

## 方向五：通知摘要聚合（Notification Digest / Summary）

### 问题陈述

当前通知系统是**事件驱动、单条推送**的：每条 `message.sent`、`comment.reply`、`message.read_receipt` 事件触发一条独立的通知行创建和 SSE 推送。虽然同一事件类型 5 分钟内可折叠（`CollapsedCount`），但这仅适用于"同类型连续爆发"场景（如连续收到 10 条消息）。它不支持：

- **跨类型聚合**："你有 3 条新消息和 2 条评论回复"（notifications 表没有交叉类型聚合能力）
- **时间区间摘要**：每日/每周摘要（"昨天有 15 条未读消息和 4 条评论回复"）
- **外部通道摘要**：邮件/短信推送必须携带人类可读的摘要文本，当前 SSE payload 是 JSON 对象，不包含摘要文本
- **免打扰摘要**：用户在静默时段收到的通知不应丢失，而应在静默结束后以摘要形式展示

### 代码级证据

**证据 1：通知行无摘要和跨类型聚合字段**

```sql
-- migrations/000009_create_notifications.up.sql
CREATE TABLE notifications (
    id              UUID PRIMARY KEY,
    recipient_uid   TEXT NOT NULL,
    type            TEXT NOT NULL,         -- "message.received", "comment.reply", "message.read_receipt"
    actor_uid       TEXT NOT NULL DEFAULT '',
    subject_type    TEXT NOT NULL DEFAULT '',
    subject_id      TEXT NOT NULL DEFAULT '',
    read_at         TIMESTAMPTZ,
    collapsed_count INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL
    -- ← 无 summary_text, 无 effective_type（折叠后类型）, 无 digest_id（摘要批次标识）
);
```

虽然 `collapsed_count` 支持相同事件的计数折叠，但：

- 同一条通知只有一个 `type`，无法跨类型聚合
- 没有 `summary_text` 列（如"王磊 回复了你的评论"的人读文本）
- 没有 `digest_id` 列将多条通知归并到同一批摘要

**证据 2：Dispatcher 仅支持单类型折叠，不生成摘要**

```go
// internal/notification/dispatcher.go — createCollapsed
// 在当前 5 分钟窗口内查找同类型、同收件人、同 actor、同 subject_type 的未读通知
// → collapsed_count++
// 不支持跨类型聚合、不支持摘要生成
```

**证据 3：NotificationPreference 有静默时段但无摘要策略**

```go
// internal/domain/notification.go — NotificationPreference
type NotificationPreference struct {
    UID              string
    NotificationType string
    Enabled          bool
    QuietHoursStart  string // "HH:MM"
    QuietHoursEnd    string
    Timezone         string
    // ← 无 DigestMode (none | realtime | daily | weekly)
    // ← 无 DigestTime (摘要发送时间, 如 "09:00")
}
```

用户在静默时段收到通知后，这些通知在当前逻辑中被抑制（`prefChecker.IsAllowed` 返回 false），但**永远不会被重放或摘要**。静默时段的通知实际上是静默丢失。

**证据 4：SSE 推送的数据是序列化的 notification 对象，无摘要呈现**

```go
// internal/handler/http/events.go:75
// for _, n := range missed {
//     data, _ := json.Marshal(n)  // ← 序列化整个 notification 对象
//     fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", ..., string(data))
// }
```

SSE 推送的是原始 notification 对象。客户端必须自行解析 JSON 并拼接摘要文本（如"你有一条来自 Alice 的新消息"）。没有服务端生成的摘要文本，对非浏览器通道（邮件、短信）完全不可用。

### 为什么需要

| 维度 | 说明 |
|------|------|
| **用户体验** | 用户早晨打开应用看到"你有 15 条未读通知"比逐一查看 15 条通知更高效。Telegram 的"通知摘要"和 iOS 的通知分组都是这一模式的体现。 |
| **静默时段完整性** | 静默模式下通知被抑制但不丢失，静默结束后应以摘要形式展示。当前静默时段等于通知丢失。 |
| **外部通道前提** | 邮件/短信推送无法发送复杂的 JSON payload。它们需要一个人类可读的摘要文本（如"Alice 回复了你的评论：'好的，...'"）。summary_text 是邮件/短信推送的前置依赖。 |
| **隐私/合规** | 用户可能希望收到"你有 N 条新通知"而非每条通知的具体内容（如在公共场合）。摘要模式尊重了用户的注意力控制和隐私偏好。 |

### 边界情况

| Edge Case | 处理 |
|-----------|------|
| **摘要间隔内无新通知** | 不产生空摘要。定时器触发时如果队列为空，跳过本次摘要周期。 |
| **实时与摘要混合** | 用户可以为不同通知类型设置不同策略：`message.received → realtime`，`comment.reply → digest`。通知偏好扩展 `DigestMode` 字段（none/realtime/daily/weekly）。 |
| **摘要中折叠跨类型通知** | 摘要文本范例："_你有 5 条来自 3 个联系人的新消息和 2 条评论回复_"。摘要行记录为 `digest_events` 表中的一个批次 ID，关联多条原始通知。 |
| **摘要生成后用户阅读了部分** | 不需要逐个标记已读——摘要中的 `read_at` 标记整个摘要批次为已读。不影响原始通知的逐条已读状态。 |
| **摘要通道失败** | 摘要通过 outbox 事件 `notification.digest` 驱动，走标准的重试/死信通道。 |

### 建议切入路径

1. **Migration**：`notifications` 表新增 `summary_text TEXT` 列；`notification_preferences` 表新增 `digest_mode TEXT NOT NULL DEFAULT 'realtime'` 和 `digest_time TEXT`
2. **Domain**：扩 `NotificationPreference` + `Notification` 增加摘要字段
3. **Dispatcher**：新增 `digestWorker` 定时扫描偏好为 `daily`/`weekly` 的用户，查询自上次摘要后未读的通知，生成摘要通知行（`type="notification.digest"`，`summary_text` 为聚合文本）
4. **Summary 生成**：`summaryTextFor(notifications []Notification) string` 将 N 条通知转化为人类可读摘要——"5 条消息、2 条评论回复"
5. **API**：通知列表端点返回 `summary_text`，SSE payload 包含摘要和原始数据两种粒度的字段
6. **静默补发**：静默时段抑制的通知在静默结束后标记为"待摘要"，由 digestWorker 统一处理

---

## 优先级总结

| # | 方向 | 产品价值 | 架构杠杆 | 风险降低 | 估算 | 前置依赖 |
|---|------|---------|---------|---------|------|---------|
| 1 | Content-Disposition + Range | 🔥🔥🔥 | 低 | 🔥🔥🔥🔥（浏览器 UX+移动网络） | 低 | 无 |
| 2 | 消息撤销/召回 | 🔥🔥🔥🔥 | 中 | 🔥🔥🔥🔥（数据安全+合规） | 中 | 无 |
| 3 | 乐观并发控制 | 🔥🔥🔥 | 高 | 🔥🔥🔥🔥（数据完整性） | 低 | 无 |
| 4 | Worker Context 修复 | 🔥🔥 | 中 | 🔥🔥🔥（运维可靠性） | 低 | 无 |
| 5 | 通知摘要聚合 | 🔥🔥🔥🔥 | 中 | 🔥🔥（UX+外部通道前提） | 中 | SSE/通知已完成 |

### 建议实施顺序

**Sprint 1（P0 — 最低成本高回报）**：方向一（Content-Disposition，1 天）+ 方向四（Context 修复，1-2 天）
- 方向一是纯 handler 层修改，不涉及服务层/数据层，风险最低
- 方向四修复 6 处 `context.Background()`，模式统一后 rest 两个 worker 可 copy-paste

**Sprint 2（P1 — 数据安全）**：方向三（乐观锁，1-2 天）+ 方向二（消息撤回，3-5 天）
- 乐观锁是 3 个 repository + migration + handler 的小改动
- 消息撤回涉及 domain/service/dispatcher/API 四层，需要完整的设计

**Sprint 3（P2 — 产品增强）**：方向五（通知摘要，3-5 天）
- 依赖方向四中关于 digestWorker (定时调度) 的模式——可以与方向四的 context 传播修复配合设计
- 是未来邮件/短信推送通道的前置条件

---

*本文档基于 `main` 分支（2026-07-11）的代码扫描，仅使用 `docs/requirements/` 中既有分析文档进行交叉验证，未编写任何代码。*
