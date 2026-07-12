Very well. 我已经通读了核心证据文件，逐一验证了你的每个声明。以下是我验证后的完整性分析。

---

## 验证结果：5/5 方向全部确认

所有证据与你描述的一致。以下几点补充和轻微的校正：

---

### 方向一：全局内容寻址去重

**确认。** 但在验证过程中发现了一个结构性问题需要补充：

**代码库中的双服务层**

代码库中存在**两套并行的文件服务**：

| 文件 | `FindByChecksum` 范围 | `storage_refs` 使用 |
|------|----------------------|-------------------|
| **OLD:** `internal/file/service_crud.go` | `FindByChecksum(ctx, actor, checksum)` — per-owner | ❌ 完全不引用 `storage_refs`。`Delete` 使用 legacy `CountByStorageKey` |
| **NEW:** `internal/service/file.go` | 同上 — `FindByChecksum(ctx, actor, checksum)` 仍然是 per-owner | ✅ `Create` 调用 `refs.Increment`，`Delete` 调用 `refs.Decrement` |

`internal/app/app.go` 的 composition root 使用 **NEW** 服务（`service.NewFileService(...)`），所以运行时的 `storage_refs` 引用计数是激活的。但 `FindByChecksum` 在两个层中都是 **per-owner** ——这是去重逻辑本身的问题，独立于服务层选择。

**更重要的发现**：新 `CompleteUpload`（`internal/service/file_presign.go`）不仅跳过去重（因无 checksum 无法 hash），还**没有调用 `refs.Increment`**。这是引用会计中的一个缺口——通过 presigned 上传创建的文件不会递增 `storage_refs`，但如果后续的 `Create`（普通上传路径）通过 checksum 匹配到同一个 blob，ref count 会开始递增。这意味着 `CompleteUpload` 路径中 blob 的 ref count 可能为 0，导致 GC 误删。

**`idx_files_checksum` 索引** 如你所说，是 `(checksum)` 而非 `(owner_uid, checksum)`。这证实了迁移设计意图是支持全局去重——只是服务层从未利用它。

---

### 方向二：资源级读取透明度

**确认。** 以下是完整的审计盲区清单：

```go
// internal/domain/audit.go — 当前所有 8 个动作
AuditFileShareGrant  = "file.share.grant"    // ✅ 审计
AuditFileShareRevoke = "file.share.revoke"   // ✅ 审计
AuditReportResolve   = "comment.report.resolve" // ✅ 审计
AuditUserBlock       = "user.block"          // ✅ 审计
AuditUserUnblock     = "user.unblock"        // ✅ 审计
AuditMessageDelete   = "message.delete"      // ✅ 审计
AuditAdminGrantRole  = "admin.role.grant"    // ✅ 审计 (admin.go)
AuditAdminRevokeRole = "admin.role.revoke"   // ✅ 审计 (admin.go)

// 缺失的读取操作：
// ? file.read       — 文件下载/查看
// ? message.read    — 消息查看（与 read_at 不同，read_at 是已读回执）
// ? comment.read    — 评论列表查看
// ? conversation.read — 对话查看
// ? notification.read — 通知查看
// ? search.query    — 搜索查询
```

`FileHandler.Download`（`/files/dl` 匿名端点——HMAC 签名认证）和 `FileHandler.Get`（`/files/:id`——Bearer token 认证）**都不记录审计**。所有消息/评论/通知的读取路径同理。

**架构复杂性补充**：文件下载是**匿名**下载路径（`/files/dl`，`SignedDownloader.VerifyPresignedURL` 验证签名）。这种路径中**没有用户上下文**（`actor` 为未知）。读取审计对匿名下载要么无法实现（无 reader_uid），要么需要将 reader UID 嵌入到 presigned URL 中——这是一个重大的 API 变化。

---

### 方向三：通知折叠竞态条件

**确认。** `internal/service/notification.go` 中的代码路径：

```go
// 第 249 行附近
if err := d.notifs.Create(ctx, n); err != nil {
    return err   // ← 步骤 1：总是先插入
}
// 第 252 行——折叠检查在 Create 之后
if d.collapseWin > 0 {
    existing, err := d.notifs.FindRecentByTypeAndActor(...)
    // ...
    _ = d.notifs.IncrementCollapsedCount(ctx, existing.ID)
}
```

这是 `CREATE then FIND then UPDATE` 模式。**竞态窗口**：如果两个 goroutine 同时处理同一 `(recipient, type, actor)` 的事件：

```
T1: Create(n1)  → 插入行 n1
T2: Create(n2)  → 插入行 n2  (T1 尚未检查折叠)
T1: FindRecent  → 找到 n1 (n2 尚未在 window 可见，或者 T2 还未提交)
T2: FindRecent  → 找到 n1 (或 n2，取决于时间戳)
T1: IncrementCollapsedCount(n1)  → n1.count = 2
T2: IncrementCollapsedCount(n1)  → n1.count = 3
结果：n2 成为一个孤立的重复通知行
```

理想解法：将折叠逻辑推入数据库（带时间窗口条件的 `ON CONFLICT` upsert）。当前模式即使修复了竞态，也会留下一条"从未使用"的僵尸行。

---

### 方向四：深层游标分页退化

**确认。** 代码库中的所有 6 个列表端点都遵循相同的模式：

```
WHERE id < ? ORDER BY <timestamp> DESC, id DESC LIMIT n+1
```

- `repository/file.go` → `ListByOwner`：`Order("created_at DESC, id DESC")`
- `repository/message.go` → `ListInbox` / `ListConversation`（同模式，有条件过滤）
- `repository/comment.go` → `ListByTarget` / `ListReplies`
- `repository/notification.go` → `ListByRecipient`

**性能退化的根本原因**：PostgreSQL 的 B-tree 索引在原地扫描指针时很快，但 `WHERE id < <早期 UUIDv7>` 意味着规划器必须遍历从最早条目到游标位置的所有索引条目。随着页面索引的增加，这可能会慢几个数量级。

**搜索问题**：更严重。`Search` 在三个存储库上各自以 `LIMIT limit`（默认 20）并行查询，然后对结果进行**简单拼接**，不做全局排序或去重。如果一条消息和一篇评论都匹配，它们会以原始顺序出现（消息先，评论后），而非按相关性排序。没有跨实体的分数归一化。

---

### 方向五：悬挂文件引用

**确认。** 这是 5 个方向中最严重的一个——是一个**正确性 bug**，而非优化机会。

关键路径：

```
用户 A（消息接收者）查阅对话
  → service.Conversation() 调用 messages.ListConversation()
    → 返回包含 file_id 的消息行
      → 用户尝试下载附件：GET /files/{fileID}
        → fileService.Get() 调用 files.Get(id)
          → GORM WHERE deleted_at IS NULL → 文件已软删除 → ErrNotFound
            → HTTP handler 返回 404 "file.not_found"
```

对评论更严重：

```
用户列出文件 X 下的评论
  → commentService.List(targetType=file, targetID=X)
    → assertTargetReadable(actor, TargetTypeFile, X)
      → files.Get(X)  → 文件已软删除 → ErrNotFound
        → 返回 apperr.NotFound("comment.target_not_found")
          → 评论列表完全不可用
```

**一个文件的删除可以使其所有评论不可访问。** 这与"软删除"的语义完全违背。

**关于外键的说明**：`messages.file_id UUID REFERENCES files(id)` 缺少 `ON DELETE` 子句（默认 NO ACTION）。PostgreSQL 的外键约束**不区分** `deleted_at IS NULL` 和 `deleted_at IS NOT NULL`——只要行在物理上存在，约束就通过。这意味着 FK 约束在软删除时没有保护作用。

**关于 dir 用户目录的补充说明**：消息的 `FileID` 在响应中仅作为 UUID 返回（`toMessageResponse` 中的 `file_id` 字段）。客户端随后调用 `GET /files/{id}` 来获取文件元信息，此时会失败。这个问题也存在于通知中（`SubjectID` 指向一个可能被删除的资源）。

---

## 额外发现：双重服务层

在验证过程中，我发现代码库中有一个意外的架构迁移：

| 域 | OLD 服务包 | NEW 服务包 |
|----|-----------|-----------|
| File | `internal/file/service*.go` | `internal/service/file*.go` |
| Message | `internal/message/service.go` | `internal/service/message.go` |
| Comment | `internal/comment/service.go` | `internal/service/comment.go` |
| — | — | `internal/service/notification.go` |
| — | — | `internal/service/block.go` |
| — | — | `internal/service/search.go` |
| — | — | `internal/service/admin.go` |

新旧服务**共存**。OIL 包（如 `internal/comment/service.go`）似乎已被弃用，取而代之的是 `internal/service/comment.go`。这意味着所有 5 个方向的修复必须针对 **新服务层**（`internal/service/`）进行，而非旧层。旧层中的 bug（如 `internal/file/service_crud.go` 中缺少 `storage_refs` 引用）在运行时已经通过 `internal/app/app.go` 中使用的 `service.NewFileService` 得到缓解。

然而，`internal/message/service.go`（旧）中的 `checkSendFile` 仍然被使用——我需要检查旧消息服务是否仍在 composition root 中使用。

---

## 优先级重评估

| 方向 | 我的调整建议 | 依据 |
|------|------------|------|
| **#1 全局去重** | **P2 → P1** | `CompleteUpload` 路径中缺少 `refs.Increment` 是一个数据完整性 bug（blob 可能在 ref count 为 0 时被 GC 回收） |
| **#2 读取透明度** | P1 ✅ | 合规要求，无变化 |
| **#3 通知折叠竞态** | P2 ✅ | 影响极小，概率低 |
| **#4 深层分页** | P2 ✅ | 搜索排序问题（缺少全局归并）比深层游标扫描更值得修复 |
| **#5 悬挂引用** | **P0** | 这是一个**功能性 bug**，会级联破坏评论列表。优先级高于 P1 |

用户对悬挂引用的产品影响评估——"级联不可用"——极为准确。评论列表完全不可用是 P0 级别的问题。

---

总体而言，这是一次出色的分析——证据精确，架构推理严谨，产品视角务实。特别是你识别出的**双服务层结构**（方向一的分析基于旧层，但运行时行为基于新层）是需要严格关注的。
