现在我已经验证了几乎所有证据。以下是我的完整评估，包括**验证修正**和**关键的盲区发现**。

---

## 验证明细

### ✅ 方向三（Admin 控制平面）—— 证据需要大幅修正

你提到的 `admin.go` 只有一条路由的部分**已经过时**。实际代码库状态比分析文档描述的**更先进**：

**HTTP 端（`internal/handler/http/admin.go`）：**
- `GET /admin/audit-log`（PermAuditRead）✅
- `GET /admin/roles`（PermAuditRead）✅
- `GET /admin/roles/:uid`（PermAuditRead）✅
- `POST /admin/roles/grant`（PermSystemConfig）✅
- `POST /admin/roles/revoke`（PermSystemConfig）✅

**gRPC 端（`internal/handler/grpc/admin.go`）：** 完全对等：
- `GrantRole`, `RevokeRole`, `ListRoles`, `ListUserRoles`, `ListAudit` 全部实现

**结论修正：**
- 角色管理 API **已存在**（Grant/Revoke/List/ListUserRoles），你的文档说"没有任何 API 可以管理它"不准确
- `PermSystemConfig` **已被使用**（见于 `admin.go:40` 和 `grpc/server.go:253-254`）
- **真正未实现的权限 API**：`PermContentModerate`（0 处引用）、`PermUserBan`（0 处引用）
- **真正缺失的能力**：全局 Moderation Queue、用户封禁/解封、系统配置运行时修改、`POST /admin/comments/:id/hide`（全局隐藏，非作者自审）

**方向三的优先级判定不变（P1），但具体工作范围应调整为：**
1. ✅ 跳过（已交付）：角色 CRUD
2. ⬜ 待做：`PermContentModerate` → 全局 moderation queue（`ListAllReports` + `AdminHide`）
3. ⬜ 待做：`PermUserBan` → ban/unban API + 消息/评论/文件发送拦截
4. ⬜ 待做：系统配置端点

---

### ✅ 方向一（群组消息）—— 证据准确

- `domain/message.go`: 确实只有 `FromUID` / `ToUID`，无 `ConversationID` 或 `GroupID`
- `repository/message.go:143`: 对话查询确认是 `(uidA, uidB) OR (uidB, uidA)` 二元组
- NotificationDispatcher 已支持多接收方分发模式（`Handle()` 单 event 产出一条 notification）

**修正点：** `messages.kind` 的 `IsValid()` 方法（`shared.go:26-33`）使用 **switch 硬编码** white-list，增加新的 kind 需要同时修改验证逻辑。建议改为基于 map 或 DB 配置的开放验证。

---

### ✅ 方向二（SSO 合约）—— 证据准确

- `api/proto/sso/v1/sso.proto`: 确实只有 `uid/nickname/avatar` 三个字段
- `enrichUsers()` 在 `service.go:254-260` 被每个 List 端点调用（comment/message/notification）
- `SystemSenderUID = "system"` 硬编码（`shared.go:82`）

**新增发现：** gRPC 服务端也有权限映射（`grpc/server.go:253-254`），但 SSO 权限检查路径是 `HasPermission()`→`adminRepo.HasPermission()`，这个是 aero-id 自身的角色系统——**不涉及 SSO**。文档中说的"SSO 不推送用户变更"影响的不仅是数据脏读，还有一个更直接的问题：**如果 SSO 删除用户，aero-id 的 `user_roles` 表会保留孤立引用**，admin user 列表也会展示不存在的人。

---

### ✅ 方向四（数据生命周期）—— 证据准确

- `internal/outbox/repository.go:73`: 只查询 `published_at IS NULL AND NOT dead_letter`，已发布的 event 永远不清除
- 唯一 retention 是 `storage/gc/sweeper.go` 的文件 GC，配置于 `config.go:161` 的 `storage.gc.retention`
- 无 `export` / `gdpr` / `account.delet` 相关代码

**新增发现：** `internal/idempotency/store.go:146` 已有 `purgeExpired()` 到期清理模式。这是一个可复用的模式——outbox、通知、审计日志可以沿用同样的 Periodic Job 模式。

---

### ✅ 方向五（存储层）—— 证据准确

- `storage/storage.go`: `FileStorage` 接口有 `PresignGet`/`PresignPut`，但没有 CDN/front 抽象
- `app.go:379-404`: `buildStorage()` 是 `switch cfg.Storage.Type` 静态选择
- local 后端没有带宽管理

**补充证据：** `local.go` 的 `Get()` 直接 `io.Copy(w, f)` 无速率限制。

---

## 🔍 关键盲区 —— 文档未识别的方向

### 盲区 A：gRPC 权限检查是手动的，无统一拦截器

```
internal/middleware/permission.go        → RequirePermission() Gin 中间件（统一）
internal/handler/grpc/server.go:253-254 → 路由→权限的 map，但@一查发现：
```

```bash
$ grep -n "PermAuditRead\|PermContentModerate\|PermUserBan\|PermSystemConfig" internal/handler/grpc/server.go
```

只有 `GrantRole`/`RevokeRole` 映射到 `PermSystemConfig`，其他 gRPC RPC（如评论审核）**没有任何权限检查**。这意味着一个有 gRPC 端点的客户端可以绕过 HTTP-admin 的权限保护。

**建议**：为 gRPC 实现 **UnaryServerInterceptor** 等效于 `RequirePermission()`，从 context 中取 actor_uid，查角色，拒绝无权限请求。当前只有 HTTP 端有统一保护。

---

### 盲区 B：SSE 订阅没有限流/连接数限制

```
internal/handler/http/events.go:68-74
  sub, unsub := h.hub.Subscribe(uid)
  // 没有检查该 uid 已有多少活跃连接
  // 没有 total_connections 上限
  // 没有 per-uid 并发连接数上限
```

一个恶意用户可以打开数千个 SSE 连接，每个连接在 hub 中维护一个 channel，导致 **server 内存 OOM**。

**建议**：在 `Subscribe()` 或 `EventsHandler.Stream()` 中加入：
- 全局最大连接数（`hub.MaxConns`）
- 每 uid 最大连接数（如 5）
- 超时 idle 断连（当前 heartbeat 防 proxy 超时，但不清理僵尸连接）

---

### 盲区 C：搜索端点无分页，返回非绑定结果

```
internal/service/search.go（搜索接口）
  SearchMessages/Files/Comments 返回 []*domain.Xxx 和 limit 参数，
  但无 cursor/pagination，List 端点使用的标准化 Page 类型未在搜索中复用。
```

当前搜索返回一个非绑定的 top-N 结果。长期来看，搜索需要分页 + 相关度 cursor。

---

### 盲区 D：批量操作完全缺失

代码库全部是**单实体操作**。以下高频需求目前必须循环 N 次 RPC：
- 批量删除消息（"清空与某人的对话"需要逐条 DELETE）
- 批量标记已读（`MarkConversationRead` 除外——这个已做了批量）
- 批量分享文件
- 批量授予角色

**建议**：考虑在 POST body 中支持 `{"ids": [...]}` 模式，或 GraphQL 风格的批量端点。

---

### 盲区 E：Presigned Upload 对 Local 后端不可用

```
internal/service/file_presign.go
  PresignUpload() 在 local 后端返回 BadRequest("files.local_presign_unsupported")
```

这意味着使用 local 存储的开发环境无法测试完整的 presigned 上传流程。开发与生产行为不一致。

---

### 盲区 F：无管理端消息/评论强制删除（admin override）

Admin 当前只能审计日志和管角色。如果需要强制删除违规消息/评论，**当前只能直接操作数据库**。`PermContentModerate` 定义了但无实现端点。

---

### 盲区 G：无文件版本控制 / 元数据不可追溯

文件元数据 PATCH（`UpdateMetadata`）直接覆盖，无版本历史。谁在什么时候改了什么文件名——无追溯。对比审计日志的 append-only 设计，文件元数据没有这个保障。

---

## 更新后的方向矩阵（修订版）

| # | 方向 | 业务价值 | 工程复杂度 | 优先级 | 核心依赖 | 修正说明 |
|---|------|---------|-----------|--------|---------|---------|
| 1 | 群组消息与频道 | 高 | 中 | P1 | 无外部依赖 | ✅ 准确 |
| 2 | SSO 合约进化 | 高 | 中 | P0-P1 | 需要 SSO 团队 | ✅ 准确，新增孤立引用问题 |
| 3 | Admin 控制平面 | 中-高 | **低** | **P0** | 基础设施已就位 | ⚠️ **缩小范围**：角色 CRUD 已交付，只需补 Moderation Queue + UserBan + Config |
| 4 | 数据生命周期管理 | 高 | 中 | P1 | 合规驱动 | ✅ 准确，新增 idempotency purge 可复用模式 |
| 5 | 存储层进化 | 中-高 | 高 | P2 | 需要 CDN/基础设施 | ✅ 准确 |
| — | **盲区 A: gRPC 权限拦截器** | **高** | **低** | **P0** | 无 | 安全缺口，HTTP 有保护而 gRPC 无 |
| — | **盲区 B: SSE 限流** | **中** | **低** | **P1** | 无 | 生产稳定性风险 |
| — | **盲区 C: 搜索分页** | 中 | 低 | P2 | 无 | 体验优化 |
| — | **盲区 D: 批量操作** | 中 | 中 | P2 | 无 | 体验优化 |
| — | **盲区 E: Local presigned** | 低 | 低 | P3 | 无 | DevX 一致性 |

## 建议执行顺序（修订版）

1. **即刻（P0）**：盲区 A（gRPC 权限拦截器）—— 这是安全漏洞，HTTP 与 gRPC 保护不同
2. **即刻（P1）**：方向三剩余部分（Moderation Queue + UserBan API）—— `PermContentModerate` 和 `PermUserBan` 已定义但零使用
3. **短期（P1）**：方向一（群组消息）—— 基础设施全就位
4. **短期（P1）**：方向四（数据导出）—— 合规最显性需求
5. **短期（P1）**：盲区 B（SSE 限流）—— 避免生产事故
6. **中期（P1）**：方向二（SSO 合约）
7. **长期（P2+）**：方向五 + 盲区 C/D/E
