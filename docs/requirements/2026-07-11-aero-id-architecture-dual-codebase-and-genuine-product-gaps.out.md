现在我已经对整个代码库进行了全面的审查。以下是逐方向的验证：

---

## 验证结果

### 方向一（架构级）：双轨并行架构——✅ 已确认

**核心声明已验证**。六个新包（`internal/comment/`、`internal/file/`、`internal/message/`、`internal/notification/`、`internal/block/`、`internal/search/`）各自包含完整的 `handler_http.go` + `handler_grpc.go` + `service.go` 实现，并且：

- `app.go` **从未导入**任何这六个包（其导入限定于 `internal/handler/http`、`internal/handler/grpc`、`internal/service`——旧的代码路径）
- `grep -rn "comment\.NewCommentHandler\|file\.NewFileHandler\|message\.NewHTTPHandler" --include="*.go"` **在任何非测试 .go 文件中均返回零匹配**
- 六包中仅有交叉引用的是 `internal/notification/dispatcher.go`（从 `internal/block` 导入 `BlockChecker` 接口）和 `internal/search/search.go`（从 `internal/comment`、`internal/file`、`internal/message` 导入 Repository 接口）——但是这些是**接口的导入**，而非 handler/service 的 Wire。

| 包 | HTTP handler | gRPC handler | Service 实现 | 已由 app.go 进行 Wire？ |
|---|:---:|:---:|:---:|:---:|
| `internal/comment/` | `handler_http.go` (176 行) | `handler_grpc.go` (207 行) | `service.go` (285 行) | ❌ |
| `internal/file/` | `handler_http.go` (66 行) | `handler_grpc.go` (144 行) | 3 个文件 (281 行) | ❌ |
| `internal/message/` | `handler_http.go` (56 行) | `handler_grpc.go` (94 行) | `service.go` (18 行) | ❌ |
| `internal/notification/` | `handler_http.go` (75 行) | `handler_grpc.go` (46 行) | `service.go` (36 行) | ❌ |
| `internal/block/` | `handler_http.go` (44 行) | `handler_grpc.go` (49 行) |（在 `block.go` 中定义）| ❌ |
| `internal/search/` | `handler_http.go` (42 行) | `handler_grpc.go` (48 行) | `search.go` (100 行) | ❌ |

**合计死 handler 代码：约 1300 行**，编译通过但永不执行。

### 方向二（产品级）：通知缺少 Subject 预览——✅ 已确认

**两者均进行验证**：

1. `internal/handler/http/notification.go:71-86`（已进行 Wire 的旧 handler）：
   ```go
   type notificationResponse struct {
       ID, RecipientUID, Type, ActorUID, SubjectType, SubjectID string
       ReadAt *time.Time; CreatedAt time.Time
       // ⚠️ 无 SubjectPreview string
   }
   ```

2. `internal/domain/notification.go`（领域模型）：
   ```go
   type Notification struct {
       ID, RecipientUID, Type, ActorUID, SubjectType, SubjectID string
       ReadAt *time.Time; CollapsedCount int; CreatedAt time.Time
       // ⚠️ 无 SubjectPreview string
   }
   ```

3. `internal/notification/handler_http.go:16-18`（新死 handler）——直接编组 `list.Notifications`（[]*Notification）为 JSON，也未添加预览字段。

4. `internal/notification/dispatcher.go:80-96`（通知创建）——设置 `SubjectType: "message"`, `SubjectID: p.MessageID`，但**从未**从出站负载提取内容预览。

### 方向三（数据正确性级）：父评论删除后的孤儿回复——✅ 已确认

在两个仓库实现（`internal/repository/comment.go` 和 `internal/comment/repository.go`）中均已验证：

- **`SoftDelete`**（第 104-110 行和第 103-109 行）：`WHERE id = ?` 删除单行；不清除子节点。
- **`ListReplies`**（第 138-153 行和第 134-149 行）：仅按 `root_id`、`status = normal`、`id <> root_id` 过滤。**不检查** `deleted_at IS NOT NULL` 的父节点；不 `JOIN` 父节点可见性。
- **领域**：`domain/comment.go` 定义了 `CommentStatusHidden`（2）和 `CommentStatusNormal`（1），但没有 `CommentStatusOrphaned` 或 `CommentStatusParentDeleted`。

客户端显示回复但父节点为 "[已删除]" 时，没有机制指示这种情况。

### 方向四（运维级）：DELETE 幂等性与审计粒度——✅ 已确认

**幂等性差异**（已验证）：

| 操作 | 第二次调用 | 代码位置 |
|------|-----------|---------|
| `MessageService.Delete` | 返回 `nil`（幂等） | `internal/service/message.go:228` — ⚡ `if stderrors.Is(err, repository.ErrNotFound) { return nil }` |
| `FileService.Delete` | 返回 `apperr.NotFound`（**非**幂等） | `internal/service/file.go:153-155` — `return apperr.NotFound(...)` |

**审计粒度**（已验证的 `internal/domain/audit.go`）：
```go
const (
    AuditFileShareGrant  = "file.share.grant"
    AuditFileShareRevoke = "file.share.revoke"
    AuditReportResolve   = "comment.report.resolve"
    AuditUserBlock       = "user.block"
    AuditUserUnblock     = "user.unblock"
    AuditMessageDelete   = "message.delete"       // ⚠️ 仅此而已，没有审计文件删除操作
    AuditAdminGrantRole  = "admin.role.grant"
    AuditAdminRevokeRole = "admin.role.revoke"
)
```

**GC 没有审计**（已验证 `internal/storage/gc/sweeper.go:119-140`）：
- `purgeSoftDeleted` 方法执行 `HardDelete` 并记录日志（`s.log.Warn`/`Info`），但**从未调用** `auditRepo.Log()`。

### 方向五（性能级）：搜索缺乏分页——✅ 已确认

**两者均进行验证**：

1. `internal/search/search.go:14`（新服务接口）：
   ```go
   type Service interface {
       Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error)
       // ⚠️ 无 cursor
   }
   ```

2. `internal/service/service.go`（旧服务接口）——由 `internal/handler/http/search.go` 调用的 `service.SearchService` 同样返回 `([]domain.SearchHit, error)`，无游标。

3. `internal/handler/http/search.go:48-57`（旧 HTTP handler）：
   - 解析 `q`、`kind`、`limit`（最大 50）
   - **无 cursor/page 参数**
   - 响应：`{"hits": [...]}` 且**无** `next_cursor` 或 `has_more`

4. `internal/search/search.go:52-75` — 按种类追加结果：消息 → 评论 → 文件。**无跨实体排序**。

### 方向六（架构级·附加）：Service 接口不对等——❌ 已修正/部分错误

**声明**：“旧层缺失：`DownloadURL`、`PresignUpload`、`CompleteUpload`、`ShareFile`、`UnshareFile`、`ListSharedWithMe`”

**此声明对于当前代码来说是不正确的。** `internal/service/service.go:124-145` 中被 Wire 的旧 `service.FileService` 接口包含以下方法：

```go
Create, Get, List, Delete,                       // CRUD
DownloadURL,                                      // ✅ 存在
PresignUpload, CompleteUpload,                    // ✅ 存在
ShareFile, UnshareFile, ListSharedWithMe,         // ✅ 存在
UpdateMetadata                                    // ✅ 存在
```

**所有 11 个方法都存在。** 旧接口和新接口在功能上是等价的。该文档的 grep 证据 `grep -c "^\s\+[A-Z][a-zA-Z]*("` 存在缺陷——它匹配的是*所有*接口方法，而不仅仅是 `FileService`。

但是，该文档的**更广泛的观点**是正确的：两套代码路径（旧 handler → 旧 service 接口 vs 死 handler → 新 service 接口）是功能重复的冗余源。该特定声明对于接口范围来说不准，**但死代码问题是不容置疑的**。

---

## 总结矩阵

| # | 方向 | 状态 | 备注 |
|---|------|------|--------|
| 1 | 双轨并行架构 | ✅ 已确认 | ~1300 行死 handler 代码，编译通过但永不执行 |
| 2 | 通知缺少 Subject 预览 | ✅ 已确认 | 响应、领域模型、调度器——均无 preview |
| 3 | 父评论删除后的孤儿回复 | ✅ 已确认 | 两个仓库：SoftDelete 非级联，ListReplies 无父节点检查 |
| 4 | DELETE 幂等性与审计粒度 | ✅ 已确认 | File: 非幂等 vs Message: 幂等；缺失 AuditFileDelete；GC 无审计 |
| 5 | 搜索缺少分页与排序 | ✅ 已确认 | 两个服务接口均无游标；结果按种类未排序追加 |
| 6 | Service 接口不对等 | ⚠️ **已修正** | 旧 FileService 接口实际上包含所有 11 个方法。声明不准确，但双轨架构问题仍然存在。 |

**优先级建议**：与文档一致。方向 1（清除死代码消除混淆）和方向 4（幂等性统一 + 审计跨度）是不引入新功能的最紧迫的架构/正确性修复。方向 2 和 3 是影响客户端体验的中等优先级产品改进。方向 5 是需要接口变更的更低优先级的 API 改进。
