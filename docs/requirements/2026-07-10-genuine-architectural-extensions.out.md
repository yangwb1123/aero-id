所有五项主张均已对照当前代码库进行了系统验证。以下是逐项判定结果，包含任何细微的修正。

---

## 验证结果

### 方向一：通知派发器绕过特性服务层 ✅ 已确认

**核心主张成立：** 写入路径（dispatcher）和只读路径（handlers）使用完全不同的代码路径，而且 dispatcher 直接依赖 repository。

**证据核实：**
- `internal/service/notification.go:163` — `NewNotificationDispatcher()` 签名接受 `repository.NotificationRepository`，而非 Service 接口
- `internal/service/notification.go:218`（大致位置） — `Handle()` 调用 `d.notifs.Create(ctx, n)`，绕过所有业务层
- 生产环境使用的特性服务（在 HTTP/gRPC handlers 中引用的 `service.NewNotificationService`）只有 `List`、`MarkRead`、`MarkAllRead`、`UnreadCount`——没有 `Create`
- `internal/handler/http/events.go:93` — `replayMissed()` 调用 `h.fetcher.ListSince()`，而 `app.go` 中传入的是原始 `notifRepo`（repository），再次绕过服务层

**还有一个额外的发现：** `internal/notification/` 包中还有一个独立的、未接入生产的并行通知服务实现，它也有 `List`/`MarkRead` 等方法但没有 `Create`。这增加了代码库中令人困惑的重复。

---

### 方向二：存储后端无可插拔架构 ⚠️ 已确认（一处更正）

**核心主张成立：** `buildStorage()` 使用硬编码的 switch 语句，没有注册机制，配置结构体是固定而非通用的。

**证据核实：**
- `internal/app/app.go:93-115` — `buildStorage()` 中的硬编码 switch ✅
- 没有 `Register()` 函数或插件机制 ✅
- `config.StorageConfig` 包含具体的 `Local` 和 `S3` 结构体字段，而非通用 map ✅
- `SignedDownloader` 通过返回 `nil` 隐式处理缺失情况，而非通过能力接口 ✅

**更正一处：** 该文档称 `internal/service/file_presign.go` 包含 `s3Storage` 类型断言。当前代码库已没有这个代码——`PresignUpload()` 和 `DownloadURL()` 使用 `FileStorage` 接口方法（`PresignPut`/`PresignGet`），这些方法对不支持的 backend 会返回 `storage.ErrNotSupported`。此项在分析之后可能已被重构。

---

### 方向三：管理员角色系统仅实现一半 ✅ 已确认

**核心主张成立：** `GrantRole` 和 `RevokeRole` 存在于 repository 接口中，但未向任何传输层暴露。

**证据核实：**
- `internal/repository/admin.go:25-30` — `AdminRepository` 接口定义了 `GrantRole`、`RevokeRole`、`HasPermission`、`ListAudit` ✅
- `internal/handler/http/admin.go:25-56` — 唯一注册的路由是 `r.Auth.GET("/admin/audit-log", h.ListAudit)`；没有 grant/revoke 端点 ✅
- 在 proto 中搜索 `admin`：`api/proto/` 下没有结果——gRPC 中没有 AdminService ✅
- 迁移 `000022` 创建了表并 seed 了内置角色，但没有管理 API ✅
- App.go 将 `adminRepo` 直接传递给 `NewAdminHandler`，没有经过服务层封装

**严重程度升级：** 这实际上是 **P0 运维安全风险**——紧急撤销需要手动连接数据库并编写原始 SQL，而且没有任何审计跟踪。

---

### 方向四：幂等键预留-提交竞态 ✅ 已确认（有细微差别）

**核心主张成立：** Reserve 和 Complete 在不同的数据库事务中执行，如果业务处理时间超过 2 分钟，会存在竞态窗口。

**证据核实：**
- `internal/idempotency/store.go:22` — `reservationTTL = 2 * time.Minute` ✅
- `Reserve()` 创建一行（status=0, expires_at=now+2min）✅
- 业务逻辑（handler 中的文件创建等）发生在 Reserve 和 Complete 之间的**独立事务**中 ✅
- `Complete()` 无条件更新行——**没有 status=0 的检查**，也没有与 expires_at 的比对 ✅
- 如果 leader A 处理时间超过 2 分钟，leader B 可以通过 `Reserve()`（含 stale-reclaim 逻辑）获取该 key，然后两组代码都可以同时调用 `Complete()`

**有一处细微差别：** 该代码确实包含一个 stale-reclaim 机制（`internal/idempotency/store.go` 第 50 行附近），当 `status=0 && now > expires_at` 时会重新认领。但竞态仍然存在，因为：
1. Leader A（最初调用 Reserve）仍在处理中
2. Leader B 调用 Reserve → 发现过期 → 重新认领并获得 `reserved=true`
3. Leader A 最终调用 Complete → 无条件更新行（status > 0, 设置 response）
4. Leader B 调用 Complete → 无条件覆盖行
5. 结果：两个不同请求的响应有同一个幂等键，最后一个覆盖前一个

---

### 方向五：软删除级联策略缺失 ⚠️ 已确认（一处更正）

**核心主张成立：** 消息、文件和评论的软删除不会级联到依赖行。

**证据核实：**
- `internal/message/service.go` — `Delete()` 调用 `s.messages.SoftDelete(ctx, id)`，但没有级联到 `message_reactions` ✅
- `internal/file/service_crud.go` — `Delete()` 调用 `s.files.Delete(ctx, id)`，但没有级联到 `file_shares` ✅
- `internal/comment/service.go` — `Delete()` 调用 `s.comments.SoftDelete(ctx, id)`，但没有递减 `like_count`/`reply_count` ✅
- 迁移 `000013` 创建 `message_reactions` 时带有 `FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE`，但由于 GORM 使用 `UPDATE deleted_at` 进行软删除，该级联约束永远不会生效 ✅

**更正一处：** 该文档称 `file_shares` 中的 `ListFilesSharedWith` 查询"不会排除软删除的文件"。实际上，GORM 的默认作用域会在使用 `Model(&fileRow{})` 时自动添加 `WHERE deleted_at IS NULL`，所以已软删除的文件会被排除。但问题是：文件被软删除*后*，孤立的 `file_shares` 行仍然存在。如果之后执行物理删除（硬删除），这些引用就悬空了。文档论点的主旨——孤儿授权记录——仍然是正确的，只是查询层面的过滤方式需要修正说明。

---

## 最终判定

| 方向 | 判定 | 说明 |
|--------|---------|---------|
| 1: 通知层 | ✅ 已确认 | 写入路径绕过服务层；SSE 重放直接走 repository |
| 2: 可插拔存储 | ⚠️ 已确认 | 已确认（S3 类型断言的声称已过时——已重构为接口方式） |
| 3: 管理员 API | ✅ 已确认 | GrantRole/RevokeRole 已就绪但无 API 端点——升级为 P0 |
| 4: 幂等键竞态 | ✅ 已确认 | 2 分钟窗口 + 无条件 Complete = 真实竞态 |
| 5: 级联删除 | ⚠️ 已确认 | 已确认（shared-with 查询过滤的声称已修正——GORM 作用域确实排除了已删除文件，但孤立的 share 行仍然是问题） |

**一处过时的主张**（S3 类型断言）和**一处需修正的分析细节**（GORM 作用域在共享查询中的行为）。核心架构结论坚实成立——所有五个方向确认有效。
