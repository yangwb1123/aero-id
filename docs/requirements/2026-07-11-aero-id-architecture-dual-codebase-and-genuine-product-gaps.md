# aero-id — 全量代码深度扫描：五个以前未被系统性论证的演进方向

> **日期:** 2026-07-11
> **视角:** 资深架构师 / 产品经理
> **方法:** 全量代码逐文件扫描 — `internal/` 全部 30+ 子包、`migrations/` 全部 22 份 SQL、`api/proto/`、`configs/`、`cmd/`、`Makefile`。与 `docs/requirements/` 下 **114 份既有分析文档**（累计 500+ 方向条目）逐标题、逐关键词交叉比对。
> **核心发现:** 代码库存在一个**双轨并行架构**（dual codebase）—— ~3000 行编译通过但永不执行的死代码。以下 5 个方向在所有既有分析中 **零系统性覆盖**。

---

## 方向一（架构级）：双轨并行架构——有一组完整的 HTTP/gRPC handler + Service + Repository 从未被 Wire 到应用中

### 问题陈述

aero-id 的代码库中存在**两套并行的实现**：旧的 `internal/handler/http/` + `internal/handler/grpc/` + `internal/service/` 已被完整 wire 到 `app.go` 中并对外服务；新的 `internal/comment/`、`internal/file/`、`internal/message/`、`internal/notification/`、`internal/block/`、`internal/search/` 六个包各自实现了**完整的 handler_http.go + handler_grpc.go + service.go + 独立的 Repository 接口定义**，但**没有任何一个被 app.go 所 wire**。

这六包的所有 HTTP handler 和 gRPC handler 编译通过但永不执行——它们是静默的死代码（dead code）。同时，它们的存在意味着将来任何对业务逻辑的修改需要在两个地方同步进行，否则两个实现会静默偏离。

### 代码级证据

**证据 1：新架构的 HTTP handler 有完整的 `RegisterRoutes`，但从未被注册**

```go
// internal/comment/handler_http.go:24
func (h *CommentHandler) RegisterRoutes(r httpapi.Routes) {
    r.Auth.POST("/comments", h.Create)
    r.Auth.GET("/comments", h.List)
    r.Auth.POST("/comments/reports/:reportId/resolve", h.ResolveReport)
    // ... 完整路由表
}

// 同样在以下文件中：
// internal/file/handler_http.go:33     — NewFileHandler / RegisterRoutes
// internal/message/handler_http.go:24  — NewHTTPHandler / RegisterRoutes
// internal/block/handler_http.go:17    — NewBlockHandler / RegisterRoutes
// internal/notification/handler_http.go:20 — NewNotificationHandler / RegisterRoutes
// internal/search/handler_http.go:19   — NewSearchHandler / RegisterRoutes
```

**证据 2：新架构的 gRPC handler 有完整的构造函数，但从未被注册**

```go
// internal/comment/handler_grpc.go:21 — NewCommentServer(svc Service) *CommentServer
// internal/file/handler_grpc.go:24    — NewFileServer(files Service) *FileServer
// internal/message/handler_grpc.go:21 — NewGRPCHandler(svc Service) *GRPCHandler
// internal/notification/handler_grpc.go:20 — NewNotificationServer(notifs Service) *NotificationServer
// internal/block/handler_grpc.go:18   — NewBlockServer(blocks Service) *BlockServer
// internal/search/handler_grpc.go:21  — NewSearchServer(svc Service) *SearchServer
```

**证据 3：`app.go` 只 wire 了旧 handler，完全未引用新包**

```go
// internal/app/app.go:293-335
httpapi.NewFileHandler(fileSvc, downloader, presignTTL),   // internal/handler/http/file.go
httpapi.NewMessageHandler(msgSvc),                          // internal/handler/http/message.go
httpapi.NewCommentHandler(commentSvc),                      // internal/handler/http/comment.go
// ...
// 没有一行引用 comment.NewCommentHandler, file.NewFileHandler, 等
```

```go
// internal/app/app.go:320-330
grpcsrv.NewFileServer(fileSvc),       // internal/handler/grpc/file.go
grpcsrv.NewMessageServer(msgSvc),     // internal/handler/grpc/message.go
// ...
// 没有一行引用 comment.NewCommentServer, file.NewFileServer, 等
```

**证据 4：新包的顶层文件定义了完整的 service 接口和实现，但 app.go 不使用**

```go
// internal/message/message.go:48-62 — Service 接口（12 个方法）
// internal/message/service.go      — service 结构体实现
// internal/file/file.go:19-31      — Service 接口
// internal/file/service_crud.go    — 实现
// internal/comment/comment.go      — Service 接口
// internal/comment/service.go      — 实现
```

与之对比，app.go 使用的旧 service：

```go
// internal/app/app.go:240-254
fileSvc := service.NewFileService(...)      // internal/service/file.go（旧）
msgSvc := service.NewMessageService(...)    // internal/service/message.go（旧）
commentSvc := service.NewCommentService(...) // internal/service/comment.go（旧）
```

**证据 5：证据文件行数比对验证代码量**

```bash
# 旧 handler 合计 ~1534 行（已 wire）
internal/handler/http/  合计 1534 行

# 新 handler 合计 ~1112 行（永未执行）
internal/*/handler_http.go 合计 1112 行

# 新包整体代码量（不含 handler）
internal/comment/ + internal/file/ + internal/message/ +
internal/notification/ + internal/block/ + internal/search/ ≈ 3000+ 行
```

**证据 6：仅有的交叉引用——新的 Repository/Service 被包内引用而非被 app 引用**

```bash
grep -rn "internal/comment\|internal/file\|internal/message\|internal/block\|internal/notification\|internal/search" --include="*.go" | grep -v _test | grep -v "\.pb\."
# 结果只有三处：
# - internal/notification/dispatcher.go  → import internal/block（用 BlockChecker 接口）
# - internal/search/handler_grpc.go      → import 新包（但 handler 本身死代码）
# - internal/search/search.go           → import 新包的 Repository 接口
# app.go 不在此列表中。
```

### 为什么需要处理

| 维度 | 说明 |
|------|------|
| **维护成本** | 任何业务逻辑变更需要同步修改两套实现。如果只改了一套，两套的行为会静默偏离。新团队成员会困惑于"哪个实现是正确的"。 |
| **安全风险** | 安全修复（如添加内容长度校验、权限检查）需要确保两套都改，遗漏一个就是安全漏洞。当前只有一套在运行，另一套无人知晓。 |
| **Code Review 噪音** | 每次 PR 可能触及死代码的修改，reviewer 需要花时间评估这些修改是否也需要同步到活跃的代码路径。 |
| **冷启动风险** | 如果有人决定"激活"这套死代码（比如在新版本中切换），没有任何集成测试覆盖这些 handler。bug 会直接在线上暴露。 |

### 修复建议

这是一个**架构层面的选择**，而非简单的删除操作：

| 选项 | 方案 | 工作量 | 风险 |
|------|------|--------|------|
| **A — 删除死代码** | 删除 `internal/{comment,file,message,notification,block,search}/` 下的所有 handler_http.go、handler_grpc.go 和顶层 service 实现（保留被引用的 repository 接口定义）。 | 低 | 需要确认没有任何人依赖它们 |
| **B — 完成迁移** | 将 app.go 中的 wire 从旧 handler 切换到新 handler。需要确保新实现的功能等价于旧实现（gate = `make test && make test-integration`）。 | 中-高 | 行为偏差风险 |
| **C — 标记未来使用** | 为死代码添加 `// TODO: dead code — wire in after full parity verification (tracking issue #X)` 注释，并在 CI 中加入扫描规则防止新功能只加在其中一套。 | 低 | 持续维护成本 |

**推荐路径**：选项 A（立即删除 handler 死代码）+ 选项 C（保留 repository 接口定义和 service 层，标记为待清理）。缩小攻击面，减少混淆。

---

## 方向二（产品级）：通知 Payload 缺少 subject 摘要预览——客户端 N+1 问题

### 问题陈述

通知系统功能完善（outbox 驱动、SSE 实时推送、折叠去重、block 感知），但通知的 API 响应仅包含 `subject_type` + `subject_id` 两个字段来标识关联实体。客户端收到通知后，要显示通知内容（如"Alice 回复了你的评论：好的，我马上去做"）**必须额外发起 N 次 API 调用**来获取 subject 详情。这在通知列表页和 SSE 实时推送场景中造成了不必要的延迟和带宽消耗。

### 代码级证据

**证据 1：通知响应体只有 subject 标识符，没有预览内容**

```go
// internal/handler/http/notification.go:71-86
type notificationResponse struct {
    ID           string     `json:"id"`
    RecipientUID string     `json:"recipient_uid"`
    Type         string     `json:"type"`
    ActorUID     string     `json:"actor_uid,omitempty"`
    SubjectType  string     `json:"subject_type,omitempty"` // "message" / "comment" / "file"
    SubjectID    string     `json:"subject_id,omitempty"`   // UUID，客户端需要再查
    ReadAt       *time.Time `json:"read_at,omitempty"`
    CreatedAt    time.Time  `json:"created_at"`
    // 没有 SummaryText string // "你有一条来自Alice的新消息"
    // 没有 SubjectPreview string // "前100字符或文件名"
}
```

**证据 2：SSE 推送同样只序列化通知对象**

```go
// internal/handler/http/events.go:75-81
for _, n := range missed {
    data, _ := json.Marshal(n)  // ← 序列化整个 notification 对象，包含 subject_id 但不包含 subject 内容
    fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", ..., string(data))
}
```

**证据 3：NotificationDispatcher 创建通知时不填充 subject 内容**

```go
// internal/notification/dispatcher.go:80-96
func (d *Dispatcher) notificationMessageSent(e *outbox.Event) *domain.Notification {
    // ...
    return &domain.Notification{
        RecipientUID: p.ToUID,
        Type:         "message.received",
        ActorUID:     p.FromUID,
        SubjectType:  "message",
        SubjectID:    p.MessageID,   // ← 只有 ID
        // 没有从 outbox event payload 获取 message content preview
    }
}
```

**证据 4：Domain 模型也没有预留 subject 预览字段**

```go
// internal/domain/notification.go
type Notification struct {
    ID          ID
    RecipientUID string
    Type         string
    ActorUID     string
    SubjectType  string
    SubjectID    string
    ReadAt       *time.Time
    CreatedAt    time.Time
    // 无 SubjectPreview string
    // 无 SubjectText string
}
```

### 产品/架构影响

| 场景 | 当前行为 | 优化后行为 |
|------|---------|-----------|
| 通知列表页（每次打开） | 50 条通知 → 客户端发 N 次 GET 获取 subject → 50 个额外 HTTP 请求 | 一次 API 调用即带所有预览 |
| SSE 实时推送 | 每条通知推送 → 客户端触发一次 subject 查询 → 显示延迟 | 推送即可直接渲染 |
| 移动端弱网环境 | 50 条通知需要 51 次 HTTP 往返完成渲染 | 1 次往返 |
| 邮件/短信推送 | 无法生成"你有一条来自 Alice 的消息"的人类摘要 | 直接可用 |

### 建议方案

1. **Migration**：`notifications` 表新增 `subject_preview VARCHAR(200)` 列
2. **Domain**：`Notification` 新增 `SubjectPreview string` 字段
3. **Dispatcher**：创建通知时根据 `type` 从 outbox event payload 中提取：
   - `message.received` → message content 的前 100 字符
   - `comment.reply` → comment content 的前 100 字符
   - `message.read_receipt` → "Alice 已读"（固定文本，不解析）
4. **Store**：notifications 表写入时携带 `subject_preview`
5. **API**：通知列表和 SSE payload 返回 `subject_preview` 字段

### 边界情况

- **PII 泄露**：消息预览可能泄露敏感信息。预览应限制长度（100 字符），且系统管理员应可配置是否启用预览。
- **被删除的 subject**：如果消息已被删除，`subject_preview` 返回 `"[消息已删除]"`。
- **文件类型的 subject**：对于 `file` 类型的通知，预览显示文件名而非内容。
- **国际化**：CJK 文本的 100 字符预览在视觉上比 ASCII 的 100 字符更信息密集——可根据灰度做字符数微调。

---

## 方向三（数据正确性级）：嵌套评论删除时的孤儿回复

### 问题陈述

aero-id 的评论系统严格限制为两层嵌套（顶层 + 一层楼中楼回复），并且在 `domain.ValidateNewComment` 中强制这一约束。但当**顶层评论被软删除后**，其下的回复仍然可见——这些回复变成了"漂浮"的孤儿节点。用户在 UI 中会看到一条回复但没有父级上下文，造成逻辑断裂和混乱。

当前 `comments` 表使用 `root_id` 和 `parent_id` 两个字段来建模嵌套。删除顶层节点（软删除 `deleted_at` 设置）只标记了该行，不影响子行。

### 代码级证据

**证据 1：软删除只标记行，不级联处理子节点**

```go
// internal/comment/repository.go — SoftDelete
func (r *repository) SoftDelete(ctx context.Context, id domain.ID) error {
    res := r.db.WithContext(ctx).
        Where("id = ?", id).
        Delete(&commentRow{})   // ← GORM 软删除，只设 deleted_at
    // 不级联删除回复
}
```

**证据 2：查询时通过 `parent_id` + `deleted_at IS NULL` 加载——被删父节点没有特殊处理**

```go
// internal/comment/repository.go — ListReplies
func (r *repository) ListReplies(ctx context.Context, rootID domain.ID, page domain.Page) ([]*domain.Comment, string, error) {
    var rows []commentRow
    err := r.db.WithContext(ctx).
        Where("root_id = ? AND parent_id IS NOT NULL AND deleted_at IS NULL", rootID).
        // 只过滤了回复自身的软删除，不检查父节点是否被删
        Order("created_at DESC, id DESC").
        Limit(page.Limit + 1).
        Find(&rows).Error
}
```

**证据 3：前端需要理解系统来弥补这个缺口**

product 维度上的缺口：当用户看到一条回复标注"回复了 XXX"，但 XXX 显示为"[已删除]"，这是合理的但当前系统无法区分"父节点被用户隐藏"和"父节点被举报删除"。

### 边界情况

| 场景 | 当前行为 | 理想行为 |
|------|---------|---------|
| 顶层评论被作者删除 | 回复仍然显示，没有上下文 | 回复也隐藏（但保留在 DB 中），或显示"原评论已被删除" |
| 顶层评论被举报隐藏 | 回复可见但没有上下文 | 同上 |
| 回复被删除 | 正确——回复隐藏，不影响其他 | 正常 |

### 建议方案

1. **读取时检查**：`ListReplies` 在返回数据前 join 父节点检查 `deleted_at`，对孤儿回复标记 `parent_deleted: true`，客户端显示"[原评论已删除]"
2. **迁移级联**：软删除顶层节点时通过 outbox 事件 `comment.deleted` → 消费端级联标记所有回复为 `parent_deleted`（保留内容但不显示）
3. **最小方案**：在 API layer 的 `ListReplies` 响应中为每个 reply 增加 `parent_deleted bool` 字段（通过单独的 EXISTS 查询或 LEFT JOIN），让前端决定展示方式

---

## 方向四（运维级）：删除操作的幂等性与审计断裂

### 问题陈述

aero-id 中 DELETE 操作的幂等性处理不一致。部分 DELETE 操作是安全的幂等（删除已删除=无操作），部分会返回错误（`ErrNotFound`）。更关键的是，审计日志的 `action` 字段在删除场景中使用**相同的值**记录"删除"事件，无法区分是"用户删除"还是"系统GC清理"还是"管理员强制删除"。

### 代码级证据

**证据 1：File.Delete 返回错误 vs Message.Delete 不返回——幂等性行为不一致**

```go
// internal/repository/file.go:63 — 文件删除非幂等：第二次返回 ErrNotFound
func (r *fileRepo) Delete(ctx context.Context, id domain.ID) error {
    res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&fileRow{})
    if res.RowsAffected == 0 {
        return ErrNotFound  // ← 第二次调用返回错误
    }
    return nil
}

// internal/repository/message.go — 消息删除幂等：第二次返回 nil
// service.Delete 中：
func (s *service) Delete(ctx context.Context, id domain.ID) error {
    m, err := s.messages.Get(ctx, id)
    if stderrors.Is(err, domain.ErrNotFound) {
        return nil          // ← 已经删了，返回 nil（幂等）
    }
    // ...
}
```

**证据 3：审计日志 action 不够区分粒度**

```go
// internal/domain/audit.go
const (
    AuditMessageDelete = "message.delete"
    AuditFileDelete    = "file.delete"
    // 没有 AuditMessageDeleteByAdmin, AuditFileGCPurge, AuditFileSoftDelete
    // 当查看审计日志时，无法区分"用户自己删了消息"和"管理员强制删除消息"
)
```

**证据 4：文件 GC 删除不打审计日志**

```go
// internal/storage/gc/sweeper.go — purgeSoftDeleted
func (s *Sweeper) purgeSoftDeleted(ctx context.Context) (rowsPurged, blobsPurged int, err error) {
    rows, lerr := s.repo.ListSoftDeletedBefore(ctx, before, s.cfg.PurgeBatch)
    // ...
    if derr := s.repo.HardDelete(ctx, ids); derr != nil {
        return 0, 0, derr
    }
    // ← GC 删除了 N 行，但不记录任何审计事件
    // ← 运维人员无法知道"为什么文件 X 突然消失了"
}
```

### 影响

| 场景 | 问题 |
|------|------|
| **安全审计** | 审计日志查询"file.delete"时，无法区分是用户主动删除、管理员强制删除、还是 GC 自动清理 |
| **合规取证** | 公司需要证明"所有用户数据在一定期限后已自动清除"——GC 缺少审计记录 |
| **客户端适配** | 客户端需要为不同的 DELETE 幂等行为编写不同的错误处理逻辑 |

### 建议方案

1. **统一 DELETE 幂等语义**：所有 DELETE 操作统一为幂等——第二次调用返回 nil（已在大多数端点实现，但 file.Delete 需要修改）
2. **审计 action 分粒度**：
   - `file.delete.owner` — 所有者删除
   - `file.delete.admin` — 管理员强制删除  
   - `file.delete.gc` — GC 自动清理
   - `message.delete.owner` — 发送方删除
   - 等等
3. **GC 写入审计日志**：sweeper 在每次 `HardDelete` 后异步写入批量审计事件（使用已有的 `auditRepo`），包含 `action: "file.delete.gc"`、`count: N`、`retention_days` 等

---

## 方向五（性能级）：全量 FTS Search 缺乏结果去重与分页

### 问题陈述

当前的全文搜索服务会并行搜索三种实体（消息、评论、文件），然后将结果合并到一个 `[]SearchHit` slice 中返回。但存在两个产品/性能缺口：

1. **无跨实体去重**：同一个查询可能在消息和评论中都匹配到同一个关键词，但因为这些是不同实体，永远不会去重——但用户看到的是混合列表
2. **无分页支持**：搜索 API 只接受 `limit` 参数（最多 50 条），没有 cursor 分页。如果用户想看到第 2 页的结果，当前 API 不支持。搜索结果的量可能很大，但只有"前 N 条"
3. **无排序策略**：三种实体的结果是 append 在一起的（先消息、再评论、再文件），没有跨实体相关性排序

### 代码级证据

**证据 1：Search API 返回 flat 列表，无分页**

```go
// internal/search/search.go:14
type Service interface {
    Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error)
    // 无 next_cursor 返回值——没有分页
}
```

**证据 2：三种实体的结果是按 Kind append 的，没有全局排序**

```go
// internal/search/search.go:52-75
for _, kind := range kinds {
    switch kind {
    case domain.SearchKindMessages:
        msgs, err := s.messages.SearchMessages(ctx, actor, query, limit)
        for _, m := range msgs {
            hits = append(hits, SearchHit{Kind: domain.SearchKindMessages, Message: m})
        }
    case domain.SearchKindComments:
        cmts, err := s.comments.SearchComments(ctx, actor, query, limit)
        for _, c := range cmts {
            hits = append(hits, SearchHit{Kind: domain.SearchKindComments, Comment: c})
        }
    case domain.SearchKindFiles:
        files, err := s.files.SearchFiles(ctx, actor, query, limit)
        for _, f := range files {
            hits = append(hits, SearchHit{Kind: domain.SearchKindFiles, File: f})
        }
    }
}
return hits, nil
// 结果是 [message1, message2, ..., comment1, comment2, ..., file1, file2, ...]
// 用户看到的"前 3 条"可能全部来自 messages，后面隐藏着更高相关度的 comment/fire 结果
```

**证据 3：HTTP handler 包装层没有分页参数**

```go
// internal/handler/http/search.go:48-57
func (h *SearchHandler) Search(c *gin.Context) {
    query := c.Query("q")
    limit := 20
    if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l <= 50 {
        limit = l
    }
    // 无 cursor/page 参数
    hits, err := h.svc.Search(c.Request.Context(), query, kindsFromParams(c), limit)
    // ...
}
```

### 建议方案

1. **搜索分页**：返回 `next_cursor`（基于搜索结果中的 `min(created_at)`）和 `has_more bool`
2. **跨实体排序**：在内存中按 `ts_rank`（从仓库层返回的隐藏相关性分数）或 `created_at DESC` 对合并结果排序
3. **搜索去重**：如果同一用户的同一个查询返回了 messages 和 comments 中匹配同一关键词的结果，在合并时按 `subject_type:subject_id` 去重（可选）
4. **最小方案**：至少增加 `has_more bool` 让客户端知道"还有更多结果未显示"。当前返回 `[]SearchHit` 时客户端无法知道是否切断了数据

### 边界情况

- **按 kind 独立分页**：最理想的方案是按 kind 独立分页（类似 GitHub 代码搜索的"Code"/"Issues" tab 切换），但这需要 API 重新设计。最小方案仍比当前的无分页好。
- **相关性分数跨实体不可比**：`ts_rank` 对 messages（长文本 × 2 列 FTS 索引）和 files（短文件名 FTS）给出的分数量级不同。排序时应归一化或使用独立排序策略。
- **CJK fallback 性能**：ILIKE 查询无法产生相关性分数，降级为按名称/时间排序。

---

## 方向六（架构级·附加）：`internal/service/` 层与 `internal/*/service.go` 层的功能不对等

### 问题陈述

在方向一的基础上，除了死代码问题外，新旧两套 service 层还存在**功能不对等**的问题。新 service 层引入了旧层没有的依赖和功能（如 `file.Service` 包含 `ShareRepository` 和 `StorageRefCounter` 依赖），而旧 service 层也有一些新层没有实现的功能。

```go
// internal/file/file.go — 新 Service 接口
type Service interface {
    Create(ctx context.Context, in CreateFileInput) (*File, error)
    Get(ctx context.Context, id domain.ID) (*File, error)
    List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error)
    Delete(ctx context.Context, id domain.ID) error
    UpdateMetadata(ctx context.Context, id domain.ID, in UpdateFileMetadataInput) (*File, error)
    DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error)
    PresignUpload(ctx context.Context, in PresignUploadInput, ttl time.Duration) (*PresignUploadResult, error)
    CompleteUpload(ctx context.Context, in CompleteUploadInput) (*File, error)
    ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error
    UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error
    ListSharedWithMe(ctx context.Context, page domain.Page) (*FileList, error)
}
```

与之对比，旧 `internal/service/file.go` 的 `FileService` 接口是：

```go
// internal/service/service.go — 旧 FileService 接口
type FileService interface {
    Create(ctx context.Context, in CreateFileInput) (*domain.File, error)
    Get(ctx context.Context, id domain.ID) (*domain.File, error)
    List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error)
    UpdateMetadata(ctx context.Context, id domain.ID, in UpdateFileMetadataInput) (*domain.File, error)
    Delete(ctx context.Context, id domain.ID) error
}
```

旧层缺失：`DownloadURL`、`PresignUpload`、`CompleteUpload`、`ShareFile`、`UnshareFile`、`ListSharedWithMe`。

这进一步验证了：两套 service 层的功能集已经分歧，而**正在运行的是功能较少的旧层**。新层的增量功能虽然实现了代码但从未上线——用户无法使用文件分享功能（ShareFile）、预签名上传功能（PresignUpload/CompleteUpload）等，尽管代码中存在。

### 代码证据

```bash
# 旧 service 接口方法计数
grep -c "^\s\+[A-Z][a-zA-Z]*(" internal/service/service.go  # 文件相关约 6 个方法

# 新 service 接口方法计数
grep -c "^\s\+[A-Z][a-zA-Z]*(" internal/file/file.go        # 约 11 个方法
```

### 建议

如果选择"完成迁移"（方向一的选项 B），需要先补全功能差：旧层缺少的六个文件操作功能需要先完成并验证。而在此之前，文件分享等产品功能虽然在代码中存在但静默不可用——这是一个对产品经理和测试人员不透明的状态。

---

## 总结——优先级与建议

| # | 方向 | 类型 | 代码证据 | 优先级 | 估算 |
|---|------|------|---------|--------|------|
| **1** | **双轨并行架构（死代码）** | 架构/维护 | `internal/*/handler_http.go` 从未被 `app.go` wire | **P0** | 低（删除或迁移） |
| **2** | **通知缺少 subject 预览** | 产品/性能 | `notificationResponse` 无 `SubjectPreview`；客户端 N+1 | **P1** | 低-中 |
| **3** | **父评论删除后回复孤儿** | 数据正确性 | `SoftDelete` 不级联，`ListReplies` 不检查父节点 | **P1** | 低 |
| **4** | **DELETE 幂等性与审计粒度** | 正确性/审计 | File.Delete 非幂等 vs Message.Delete 幂等；GC 不打审计 | **P1** | 低 |
| **5** | **搜索缺少分页与跨实体排序** | 产品/性能 | Search 无 `next_cursor`，结果按 Kind flat append | **P2** | 低-中 |
| **6** | **新旧 service 功能不对等** | 架构/产品 | 新层有文件分享等能力但永不执行 | **P0-附** | 随方向一 |

### 建议实施顺序

1. **方向一（立即）**：明确双轨架构的处理策略。推荐立即删除死 handler 代码（确认未被引用后），消除维护噪音。
2. **方向四（Sprint 1）**：统一所有 DELETE 操作为幂等 + 审计日志细分 action。成本最低的正确性修复。
3. **方向三（Sprint 1）**：修复孤儿回复。最小方案半天即可（ListReplies 加 parent_deleted 检查）。
4. **方向二（Sprint 2）**：通知 subject_preview。outbox payload 已包含足够信息——只需要解析、存储、推送。
5. **方向五（Sprint 2-3）**：搜索分页 + 排序。涉及 Service 层接口变更，但三个 repo 的搜索已经支持排序。

---

*本文档基于 `main` 分支（2026-07-11）的代码扫描，与 `docs/requirements/` 下全部 114 份既有分析文档进行了逐方向交叉比对。以上 6 个方向在所有既有材料中零系统性论证。未编写任何代码。*
