Based on exhaustive code verification across all five directions, I can now provide a precise accuracy assessment.

---

## 验证结果总结

对代码库中 23+ 个子包、所有 domain 模型、repository 实现、handler 层、service 层、migrations 和 config 进行了交叉验证。以下是每条证据的准确度评估。

---

### ✅ 方向一：多参与方消息 — 准确度 **95%**

| 证据 | 验证结果 |
|------|---------|
| 证据 1: Message 模型只有 `FromUID`/`ToUID` | **✅ 准确** — `internal/domain/message.go:12-13` 确认无 `ConversationID` |
| 证据 2: Inbox 查询返回扁平消息列表 | **✅ 准确** — `internal/repository/message.go:171-190` `ListInbox` 按 `created_at DESC` 排序返回完整消息行，无对话聚合 |
| 证据 3: 无群成员表或会话元数据表 | **✅ 准确** — 23 份迁移文件中没有 `conversations` 或 `conversation_members` |
| 证据 4: MarkConversationRead 基于 (to_uid, from_uid) | **✅ 准确** — `internal/repository/message.go:248-249` 确认 `WHERE to_uid = ? AND from_uid = ?` |

**补充发现**：HTTP handler 存在 `/v1/conversations/:uid` 和 `/v1/conversations/:uid/read` 端点（`internal/handler/http/message.go:34-35`），但"对话"是在查询层通过 `(from_uid=a AND to_uid=b) OR (from_uid=b AND to_uid=a)` 动态计算得出的**虚拟概念**，没有任何持久化的会话实体。文档的结论（需要真正的会话实体）是准确的。

---

### ⚠️ 方向二：管理后台与内容审核 — 准确度 **50%**

此方向的部分证据已严重**过时**——代码库在文档编写后有显著演变。

| 证据 | 验证结果 | 详情 |
|------|---------|------|
| 证据 1: RequirePermission 零引用 | **❌ 过时** | `internal/handler/http/admin.go:31` 和 `:40` 分别在 `/admin` 和 `/admin/roles` 路由组中使用 `RequirePermission(PermAuditRead,...)` 和 `RequirePermission(PermSystemConfig,...)` |
| 证据 2: AdminHandler 仅暴露审计日志 | **❌ 过时** | `admin.go` 现在暴露 5 个端点：`GET /admin/audit-log`, `GET /admin/roles`, `GET /admin/roles/:uid`, `POST /admin/roles/grant`, `POST /admin/roles/revoke`。且通过 `service.AdminService` 而非直接 repository |
| 证据 2: handler 直接调用 HasPermission | **❌ 过时** | 权限检查现在通过中间件统一进行，handler body 中不再有内联的 `HasPermission` 调用 |
| 证据 3: PermUserBan 有定义无实现 | **✅ 部分准确** | `domain/admin.go:9` 定义了 `PermUserBan = "user.ban"`，代码库中**确实没有**用户封禁功能。但存在 `user_blocks` 表（迁移 000012）和 `block.BlockChecker` 接口——用于消息/评论层面的阻止功能，不等同于封禁 |
| 证据 4: content.moderate 未接入 | **✅ 准确** | `domain/admin.go:7` 定义的 `PermContentModerate` 在全代码库中仅此一处引用。`setStatusAsAuthor`（`service/comment.go:267-268`）强制的 `c.OwnerUID != actor` 检查确实阻止管理员路径 |
| 证据 5: GrantRole/RevokeRole 无传输层接入 | **❌ 过时** | 两个端点都已通过 HTTP handler 暴露（`POST /admin/roles/grant`, `POST /admin/roles/revoke`） |

**修正后的结论**：文档高估了方向二的未完成度。`RequirePermission` 中间件、角色管理、审计日志均已正确接入。但 `PermContentModerate`（全局评论隐藏）和 `PermUserBan`（用户封禁）**确实**仍是空白。修复方向二的成本比文档评估的更低——基础设施已基本就位。

---

### ✅ 方向三：乐观并发控制 — 准确度 **100%**

| 证据 | 验证结果 |
|------|---------|
| 证据 1: 无实体包含版本字段 | **✅ 准确** — `domain/file.go`, `domain/message.go`, `domain/comment.go` 均无 `Version` 或 `LockVersion` |
| 证据 2: GORM 更新使用无条件覆盖 | **✅ 准确** — `repository/file.go:168` `UpdateMetadata` 使用 `Updates(fields)`；`repository/comment.go:137` `UpdateContent` 同理 |
| 证据 3: 消息编辑存在竞态条件 | **✅ 准确** — `service/message.go:256-263` 中 `Get → 业务逻辑 → UpdateContent` 三步之间无锁 |
| 证据 4: 文件可见性更新存在竞态 | **✅ 准确** — `service/file.go` 中 `ownedFile → 构造 fields → UpdateMetadata` 同理 |

所有证据完整可复现。这是低成本高价值的修复方向。

---

### ✅ 方向四：资源配额系统 — 准确度 **100%**

| 证据 | 验证结果 |
|------|---------|
| 证据 1: 唯一限制是全局配置 | **✅ 准确** — `config/config.go:148` `MaxUploadBytes` 和 `AllowedMimeTypes` 是全局性的；无 per-user 配额字段 |
| 证据 2: 无使用量追踪基础设施 | **✅ 准确** — 全库 grep `usage`/`quota` 零命中；迁移文件中无相关表 |
| 证据 3: 服务层无配额检查点 | **✅ 准确** — `service/file.go:83` 仅检查全局 `s.maxBytes`；`service/message.go:43` 发送流程无 per-user 频率检查 |
| 证据 4: 全局 RPS 不能替代配额 | **✅ 准确** — `pkg/ratelimit/ratelimit.go` token bucket 是瞬时流量控制，每分钟/每秒重置 |

---

### ✅ 方向五：平台可集成性 — 准确度 **100%**

| 证据 | 验证结果 |
|------|---------|
| 证据 1: HTTP API 无 OpenAPI/Swagger | **✅ 准确** — `go.mod` 无任何 OpenAPI 依赖；handler 无 swaggo 注解 |
| 证据 2: gRPC proto 未转为开发者文档 | **✅ 准确** — `api/proto/aero_id/v1/aero_id.proto` 中的注释是参数/枚举说明，无使用文档 |
| 证据 3: Webhook 事件类型未文档化 | **✅ 准确** — `notification/dispatcher.go:82-86` 的 `notificationFor` 中事件类型是字符串；`"message.sent"`, `"message.read"`, `"comment.replied"` 散布在 `service/message.go` 的 `newEvent` 调用中 |
| 证据 4: 无客户端 SDK | **✅ 准确** — codebase 无 SDK 目录、Postman 集合或集成示例 |
| 证据 5: 无 API 版本化策略 | **✅ 准确** — `router.go:74` 硬编码 `/v1` 前缀；proto 包名 `aero_id.v1` 暗示版本但无兼容性保证或弃用策略 |
| 补充: 无 ResourceExhausted | **✅ 准确** — `pkg/errors/errors.go` 只有 6 种错误类型（BadRequest/Unauthorized/Forbidden/NotFound/Conflict/Internal），无配额系统的错误码 |

---

## 修正后的优先级矩阵

| 方向 | 商业影响 | 工程复杂度 | 文档评估准确度 | 实际工作剩余量 |
|------|---------|-----------|--------------|--------------|
| 方向一：群聊/会话 | ★★★★★ | ★★★★ | 95% | 几乎全新设计 |
| 方向二：管理后台/审核 | ★★★★ | ★★ → **★** | 50% (高估缺口) | 仅需接 PermContentModerate + PermUserBan |
| 方向三：乐观并发 | ★★★ | ★ | 100% | 3 个模型加 version 字段 + repo 层加 WHERE 子句 |
| 方向四：资源配额 | ★★★★ | ★★★ | 100% | 全新表+服务+检查点 |
| 方向五：开发者体验 | ★★★ | ★★ | 100% | protoc-gen-doc + OpenAPI 生成 + SDK CI |

**关键纠正**：方向二的工作量比文档评估的**低得多**。`RequirePermission` 中间件已就位、角色管理已暴露、审计日志已连接。实际只需：
1. 在 comment handler 注册一组受 `RequirePermission(PermContentModerate, ...)` 保护的路由
2. 在 comment service 添加一个 `ModerateSetStatus` 方法，跳过 ownerUID 检查
3. 实现 ban/unban 的 HTTP/gRPC 端点（新表 + service + handler）

这两项功能的工作量估计约 1-2 天，而非文档暗示的数周。

方向一（群聊）确实是最大的架构缺口，文档对此的判断准确。Message 模型到 domain 层的 redesign 是根本性的。
