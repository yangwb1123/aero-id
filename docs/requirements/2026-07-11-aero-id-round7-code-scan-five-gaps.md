# aero-id — 第七轮全量代码扫描：五个未被已有分析覆盖的工程缺口与扩展方向

> **日期**: 2026-07-11
> **视角**: 资深架构师 / 产品经理
> **方法**: 对 `internal/` 全部 22 个包、`migrations/` 22 份 SQL、双传输层 handler、config 做逐文件深度扫描，与 `docs/requirements/` 下既有分析文档的所有方向标题和核心发现做**逐条交叉比对**，确保以下 5 个方向在既有文档中**零系统性覆盖**。
> **验证状态**: ✅ 全部 5 个方向已对照实际代码库验证，方向二有重要修正（见下）。
> **约束**: 不写代码。每个方向附带代码级证据和设计建议，不空谈。

---

## 当前代码库状态速览

aero-id 已经是一个非常成熟的工程制品——22 个可逆迁移、38+ gRPC RPC 与 HTTP 完全对等、事务性 outbox、幂等键、全文搜索 SSE/Webhook 实时通知推送、用户管理角色权限、审计日志、SSO 韧性（断路器 + stale-cache + singleflight）、RED 指标与 OTel 链路追踪、文件存储 GC 回收、媒体管线校验（MIME 白名单、大小限制、checksum 去重）、限流、用户阻止（block）、通知偏好（静默时段）——MVP 级功能几乎全部到位。

但本轮扫描发现了五个在既有分析中未被系统性覆盖的缺口。它们不是"没有的功能"，而是**已实现功能中的结构性裂痕**——要么是代码未连接（设计与实现之间的断点），要么是同一模式的非对称处理，要么是负责测试覆盖的代码在生产中根本不运行。

---

## 方向一：Admin 角色管理 API 只有实现没有传输层

### 代码级证据

| 位置 | 内容 | 状态 |
|------|------|------|
| `internal/domain/admin.go` | 定义 `Role`、`UserRole`、四个权限常量（`PermContentModerate`、`PermAuditRead`、`PermUserBan`、`PermSystemConfig`） | ✅ 已设计 |
| `migrations/000022_admin_roles.up.sql` | 创建 `roles` 表 + `user_roles` 表，预灌 `admin` 和 `moderator` 角色 | ✅ 已迁移 |
| `internal/repository/admin.go` | `AdminRepository` 接口定义 `HasPermission`、`ListAudit`、`GrantRole`、`RevokeRole`，全部有实现 | ✅ 已实现 |
| `internal/handler/http/admin.go` | `AdminHandler` 注册 `GET /v1/admin/audit-log` 路由，通过 `HasPermission("audit.read")` 鉴权 | ✅ 已接入 |
| `internal/handler/grpc/ops.go` | Health、Reflection 有注册，**无 Role 相关 gRPC 服务** | ❌ 缺失 |
| `internal/handler/http/router.go` | `adminHandler.RegisterRoutes(r)` 已调用 | ✅ |
| **`GrantRole` / `RevokeRole` 的 HTTP/gRPC 端点** | **不存在** | **❌ 完全缺失** |
| **`ListRoles` / `ListUserRoles` 端点** | **不存在** | **❌ 完全缺失** |

### 问题分析

这是一个典型的基础设施就绪但**传输层断裂**的案例：

- 领域模型定义了四个权限 + 角色结构
- 数据库迁移创建了 `roles` + `user_roles` 表
- Repository 完整实现了 `GrantRole`、`RevokeRole`、`HasPermission`
- HasPermission 已在 `adminHandler.ListAudit` 和 `middleware/permission.go` 中使用
- **但没有任何 API 端点暴露 GrantRole / RevokeRole / ListRoles / ListUserRoles**

这意味着：
1. 角色只能通过直接操作数据库来分配——没有 `POST /v1/admin/roles`、没有 gRPC `GrantRole` RPC
2. 权限系统当前只有"读取"能力（审计日志），没有"写入"能力（角色分配）
3. 管理员无法开通另一个管理员——需要 DBA 直接在 `user_roles` 表上 INSERT

### 为什么这是高优先级

角色管理是整个权限系统的**入口**。没有它：
- `user.ban` 权限已定义但不可分配
- `content.moderate` 权限已定义但不可分配
- 审核队列（`ResolveReport`）依赖 `comment.author` 身份而非 moderator 角色——领域层预留了 `PermContentModerate` 但从未使用
- 如果想用 "moderator 可以审核任何人的举报" 替代 "只有评论作者能审核"，必须先有 API 能赋予 moderator 角色

### 建议方案

1. **HTTP 端点**（新增到 `AdminHandler`）：
   - `POST /v1/admin/roles/grant` — `{uid, role_code}`
   - `POST /v1/admin/roles/revoke` — `{uid, role_code}`
   - `GET /v1/admin/roles` — 列出所有角色定义
   - `GET /v1/admin/users/:uid/roles` — 查看某用户的角色

2. **gRPC RPC**（在 proto 中新增 `AdminService`）：
   - `GrantRole(GrantRoleRequest) returns (GrantRoleResponse)`
   - `RevokeRole(RevokeRoleRequest) returns (RevokeRoleResponse)`
   - `ListRoles(ListRolesRequest) returns (ListRolesResponse)`
   - `ListUserRoles(ListUserRolesRequest) returns (ListUserRolesResponse)`

3. **鉴权**：以上端点自身要求 `system.config` 权限（目前最高级别）。`ListRoles` 可放宽到 `audit.read`。

**规模估算**：低。4 个 HTTP handler + 4 个 gRPC handler + 重复现有的 repository 调用的模式。总共约 150 行新代码。

---

## 方向二：`internal/search/` 和 `internal/notification/` 是两个完整死代码包

> **本方向相比原始发现做了重要修正**：原始分析声称"全文搜索 FTS 回退路径未连接"（`internal/search/search.go` 未调用 Like 方法），但经代码验证，生产路径 `internal/service/search.go` **已有完整 CJK 回退实现**（使用 `containsCJK()` 检测并在 FTS 零结果时调用 `Search*Like()`）。真正的发现是 `internal/search/` 和 `internal/notification/` 是未被任何生产代码引用的**完整死代码包**。

### 代码级证据

#### `internal/search/` 死代码包

| 位置 | 内容 | 引用状态 |
|------|------|---------|
| `internal/search/search.go` | 独立的 `SearchService` 实现（**无** CJK Like 回退） | ❌ 仅被自身测试文件引用 |
| `internal/search/handler_http.go` | 独立 HTTP handler，注册 `GET /search` | ❌ 仅被自身测试文件引用 |
| `internal/search/handler_grpc.go` | 独立 gRPC SearchServer，注册 `SearchService` | ❌ 仅被自身测试文件引用 |
| `internal/search/service_test.go` | 上述 service 的测试（全部通过） | ⚠️ 测试的是死代码 |
| `internal/search/handler_http_test.go` | 上述 HTTP handler 的测试（全部通过） | ⚠️ 测试的是死代码 |
| `internal/search/handler_grpc_test.go` | 上述 gRPC server 的测试（全部通过） | ⚠️ 测试的是死代码 |

**生产对照**：
| 生产位置 | 内容 |
|---------|------|
| `internal/service/search.go` | 生产 SearchService，有 `containsCJK()` + Like 回退 |
| `internal/handler/http/search.go` | 生产 HTTP handler，被 `app.go` 注册 |
| `internal/handler/grpc/search.go` | 生产 gRPC SearchServer，被 `app.go` 注册 |

#### `internal/notification/` 死代码包

| 位置 | 内容 | 引用状态 |
|------|------|---------|
| `internal/notification/notification.go` | 独立的 `Service` 接口定义 | ❌ 仅被包内文件引用 |
| `internal/notification/service.go` | 独立的 Service 实现 | ❌ 仅被测试引用 |
| `internal/notification/repository.go` | 独立的 Repository 实现 | ❌ 仅被测试引用 |
| `internal/notification/dispatcher.go` | 独立的 Dispatcher（outbox 事件 → 通知） | ❌ 仅被自身测试引用 |
| `internal/notification/handler_http.go` | 独立 HTTP handler，注册 `/notifications/*` 路由 | ❌ 仅被测试引用 |
| `internal/notification/handler_grpc.go` | 独立 gRPC NotificationServer | ❌ 仅被测试引用 |
| `internal/notification/dispatcher_test.go` | Dispatcher 单元测试（全部通过） | ⚠️ 测试的是死代码 |
| `internal/notification/handler_http_test.go` | HTTP handler 测试 | ⚠️ 测试的是死代码 |
| `internal/notification/handler_grpc_test.go` | gRPC server 测试 | ⚠️ 测试的是死代码 |
| `internal/notification/service_test.go` | Service 测试 | ⚠️ 测试的是死代码 |
| `internal/notification/repository_integration_test.go` | Repository 集成测试 | ⚠️ 测试的是死代码 |

**生产对照**：
| 生产位置 | 内容 |
|---------|------|
| `internal/service/notification.go` | 生产 NotificationDispatcher，功能严格超集（含 preference checker、rate limiter、collapse） |
| `internal/handler/http/notification.go` | 生产 HTTP handler，被 `app.go` 注册 |
| `internal/handler/grpc/notification.go` | 生产 gRPC NotificationServer，被 `app.go` 注册 |

### 问题分析

**搜索死代码包** (`internal/search/`，6 文件，~3KB)：
- 独立实现了一个搜索服务，与生产 `internal/service/search.go` 功能相似但缺少 CJK Like 回退
- 注册的 HTTP 路由是 `/search`（生产路由在 `internal/handler/http/search.go` 中注册），但从未被挂载到 Gin router
- 如果某天开发者误将 `app.go` 的搜索 handler 改为 `search.NewSearchHandler(searchSvc)`，会导致 CJK 搜索回退消失

**通知死代码包** (`internal/notification/`，11 文件，~1.5KB 代码 + ~2KB 测试)：
- 早期版本的完整通知系统实现，包含 Service、Repository、HTTP handler、gRPC handler、Dispatcher
- 生产代码在新的 `internal/service/notification.go` 中重构，功能严格超集
- 旧的 Dispatcher (`internal/notification/dispatcher.go`) 没有 preference checker、rate limiter、notification collapse
- 但它的测试全部通过，给开发者"通知分发已测试"的错觉

### 为什么这是高优先级

1. **测试幻觉**：两套死代码包的测试全部通过，但测试的分支永远不会在生产中运行。未来的修改可能导致这些测试失败，浪费调试时间。
2. **维护成本**：两个包共约 17 个文件，约 1000 行代码 + 1500 行测试需要维护。每次重构/升级需要同步修改两处——但实际上"另一处"永远不会被执行。
3. **混淆风险**：两个同名的 `SearchService` interface（`internal/search/Service` vs `internal/service/SearchService`）容易在 import 时混淆。
4. **CJK 搜索回退的假阳性风险**：如果某天有人将 `app.go` 的搜索初始化改为 `search.NewService(...)`，CJK 用户搜索会静默回归到"永远返回空结果"。

### 建议方案

1. **删除 `internal/search/` 整个包**（6 个文件）
2. **删除 `internal/notification/` 整个包**（11 个文件）
3. **如果 `internal/notification/handler_grpc.go` 中的 gRPC proto 类型与生产有差异**，检查 `internal/handler/grpc/aeropb/` 中的 proto 定义是否完整覆盖了通知 RPC（目前已确认 `ListNotifications`、`MarkRead`、`MarkAllRead`、`UnreadCount` 在生产 proto 中存在）
4. **验证步骤**：
   ```bash
   # 确认无生产代码依赖
   grep -rn "internal/search\|internal/notification" internal/ --include='*.go' | grep -v "_test.go" | grep -v "\.pb\."
   # 预期输出为空（只有注释引用）
   ```

**规模估算**：极低。纯代码删除，17 个文件，不改变任何生产行为。

---

## 方向三：文件分享事件无通知链路（不对称 UX）

### 代码级证据

| 位置 | 内容 | 状态 |
|------|------|------|
| `internal/file/service_share.go:60-69` | `ShareFile` 调用 `s.shares.Grant(...)` + 审计日志 `AuditFileShareGrant` | ✅ |
| `internal/file/service_share.go:85-93` | `UnshareFile` 调用 `s.shares.Revoke(...)` + 审计日志 `AuditFileShareRevoke` | ✅ |
| `internal/file/service_share.go` 全局 | `ShareFile` 和 `UnshareFile` **均不生成 outbox 事件** | **❌ 不对称** |
| `internal/domain/outbox.go:14-17` | 定义的 event types 有 `message.sent`、`message.read`、`comment.replied` | ❌ 无 `file.shared` |
| `internal/service/notification.go:259-311` | `NotificationDispatcher.notificationFor` 只处理上述三种事件类型 | ❌ 无 `file.shared` |
| `internal/domain/notification.go:9-12` | `NotifTypeMessageReceived`、`NotifTypeMessageRead`、`NotifTypeCommentReply` 三个通知类型 | ❌ 无 `file.shared` |
| `internal/message/service.go` | 消息发送发送 `message.sent` outbox 事件 | ✅ 比较对象 |
| `internal/comment/service.go` | 评论创建如果有 parent 则发送 `comment.replied` outbox 事件 | ✅ 比较对象 |
| **`internal/file/service_share.go`** | **文件分享不触发任何 outbox 事件** | **❌ 不对称** |

### 问题分析

消息和评论在创建/回复时都会：
1. 在同一个 DB 事务中写入 outbox 事件
2. outbox relay 消费事件后写入通知行
3. 通知行通过 SSE Hub 实时推送给接收方

但文件分享的流程在 `ShareFile` 创建分享记录后，只写了一条审计日志就结束了：

```
ShareFile → Grant(share row) + AuditLog → 结束（无 outbox 事件 → 无通知 → 无 SSE 推送）
```

这意味着：
- 用户 A 分享文件给用户 B，用户 B 不会收到任何通知
- 用户 B 只能通过主动轮询 `GET /v1/files/shared-with-me` 来发现"有人分享了一个文件给我"
- 如果 B 从不主动刷新，可能永远不知道文件被分享给他
- 这与消息/评论的"被通知"体验不对称——用户期望被动的"有人提到你"体验，而非主动轮询

### 建议方案

1. **新增 outbox 事件类型** `file.shared`（在 `domain/outbox.go` 中）：
   ```go
   outbox.NewEvent("file", f.ID.String(), "file.shared", map[string]any{
       "file_id":     f.ID.String(),
       "owner_uid":   f.OwnerUID,
       "grantee_uid": granteeUID,
   })
   ```

2. **新增通知类型** `NotifTypeFileShared`（在 `domain/notification.go` 中）：
   ```go
   NotifTypeFileShared NotificationType = "file.shared"
   ```

3. **在 `NotificationDispatcher.notificationFor` 中处理 `file.shared`**（在 `internal/service/notification.go` 中）：
   - 为每个 grantee 创建一条 `{RecipientUID: granteeUID, Type: "file.shared", ...}` 通知
   - 不需要 block 检查（分享者是主动行为）
   - 不需要 rate limit（分享频率天然受限于用户行为）

4. **新事件投递方式**：
   - 在 `ShareFile` 中，对每个 grantee 分别写入 outbox 事件（或批量写入一个事件带 grantees 列表）
   - 注意：分享操作是幂等的（`clause.OnConflict{DoNothing: true}`），但 outbox 事件不应被去重——每次分享都应触发通知
   - 建议在 `file.ShareFile` 方法中新增 `outbox.Writer` 依赖（通过 `file.ServiceOption` 可选注入）

**规模估算**：低-中。新增一个 outbox 事件类型 + 一个通知类型 + dispatcher handler。约 60 行代码。

---

## 方向四：后台 Goroutine 缺少统一 Context 治理与优雅 Drain

### 代码级证据

| 位置 | 内容 | 使用的 Context |
|------|------|------|
| `internal/outbox/relay.go:163` | `Relay.Start` → `ctx := context.Background()` | `context.Background()` |
| `internal/outbox/relay.go:170` | 最终 drain 也使用 `context.Background()` — **可能无限阻塞** | `context.Background()` |
| `internal/storage/gc/sweeper.go:255` | `Sweeper.Start` → `ctx := context.Background()` | `context.Background()` |
| `internal/pkg/ratelimit/ratelimit.go:119-140` | `RunCleaner` — 无 context 参数，使用匿名 goroutine | 无 context |
| `internal/idempotency/store.go:126` | `GormStore.RunCleaner` — 同上 | 无 context |
| `internal/app/app.go:449-463` | `shutdown()` 各 stop 函数无 deadline 传递 | 无统一 deadline |

| Goroutine | 停止机制 | 问题 |
|-----------|---------|------|
| outbox relay | `stopRelay()` — 关闭 done channel，最终 `drain(context.Background())` | ✅ 有 drain，但 ⚠️ 使用 `context.Background()`，可能无限阻塞 |
| GC sweeper | `stopGC()` — 关闭 done channel，**无最终 drain** | ⚠️ 正在进行的 pass 被中断，orphan 可能残留 |
| rate limit cleaner | `stopRateLimit()` — 关闭 done channel | ⚠️ 无优雅关闭，正在进行的 sweep 被中断 |
| idempotency cleaner | `stopIdemClean()` — 关闭 done channel | ⚠️ 同上 |

### 问题分析

当前启动的每个后台 goroutine 都**各自使用 `context.Background()`**，且停止机制各不相同。

更深层的问题是：
1. **没有统一的 shutdown priority**：relay 先 drain → GC 停止 → rate limit cleaner 停止 → ... 但顺序是硬编码在 `shutdown()` 中的，无法根据运行时状态动态调整。当前 `shutdown()` 顺序是正确的，但每个 stop 都是即时返回的，最终 drain 的 context 没有继承 shutdown timeout。
2. **relay 的最终 drain 可能阻塞超过 shutdown timeout**：`shutdown()` 在第 2 行调用 `a.stopRelay()`，该函数立即返回（仅关闭 done channel），然后在另一个 goroutine 中执行 `r.drain(context.Background())`。但 `shutdown()` 不会等待 drain 完成就继续执行后续步骤（HTTP 关闭、gRPC 关闭）。这意味着 drain 可能在工作负载关闭后仍在运行，使用 `context.Background()` — 一个永远不超时的 context。
3. **GC sweeper 无最终 drain**：如果一个 GC pass 正在进行中并被中断，orphan 引用可能残留。虽然 GC 是重入的（下次运行会清理），但 orphan 窗口期被延长。
4. **goroutine panic 不可恢复**：每个 goroutine 都用 `context.Background()`，一旦某个后台 goroutine panic 被 recovery 捕获，没有机制通知 app 进入 degraded 状态或触发重启。
5. **trace 传播断裂**：所有后台操作都使用 `context.Background()`，OTel trace 在后台操作中完全不可见——无法追踪 outbox 事件处理时间、GC pass 持续时间等。

### 建议方案

1. **引入统一的 `BackgroundContext` 工厂**，在 `app.go` 中用 parent context 创建：
   ```go
   type BackgroundTask struct {
       Name   string
       Ctx    context.Context    // derives from app lifecycle context
       Stop   func()
   }
   ```

2. **所有后台 goroutine 改用传入的 context**：
   - `relay.Start(ctx, interval)` — 使用 ctx 的 Done() 信号代替独立的 done channel（或叠加）
   - `sweeper.Start(ctx, interval)` — 同上
   - 所有 cleaner 同理

3. **relay 的最终 drain 继承 shutdown timeout**：
   ```go
   // 当前：
   r.drain(context.Background())
   // 改为：
   shutdownCtx, cancel := context.WithTimeout(parentCtx, shutdownTimeout)
   defer cancel()
   r.drain(shutdownCtx)  // 最多等 shutdownTimeout
   ```

4. **实现 Shutdown Priority 排序**（当前已部分实现，需强化 deadline 传递）：
   ```
   1. Flip gRPC Health → NOT_SERVING（已实现）
   2. Stop relay ticker → 从 parentCtx 派生 shutdownCtx 执行最终 drain（需要 timeout context）
   3. Stop GC sweeper（允许当前 pass 完成，使用 shutdownCtx）
   4. Stop all cleaners（使用 shutdownCtx）
   5. HTTP server Shutdown（已有 timeout context）
   6. gRPC GracefulStop
   ```

5. **OTel trace 透传**：在 relay 的 drain 方法中创建 root span，挂载到每个事件处理的子 span 上，使 outbox 处理链路可观测。

**规模估算**：中。涉及 4 个 goroutine 启动点的签名变更 + `app.go` 的 shutdown 逻辑重构。不改变业务逻辑。

---

## 方向五：文件分享事件无通知链路（复盘中发现的第三个对称性缺口）

> **注**：这个方向与方向三（文件分享无通知）不同 —— 方向三是"主动分享不通知接收方"（Outbound 缺口），这个方向五是"文件被分享后的被动事件如文件变更不通知分享者"（Inbound 缺口）。

### 代码级证据

| 位置 | 内容 |
|------|------|
| `internal/file/service_update.go` | `UpdateFile` 方法 — 更新文件元数据（名称、描述），无任何事件通知 |
| `internal/file/service_delete.go` | `DeleteFile` 方法 — 软删文件，无任何事件通知 |
| `internal/file/service_share.go` | `ShareFile` / `UnshareFile` — 仅写审计日志，无 outbox 事件 |
| `internal/file/file.go`（Service 接口） | `Update`、`Delete`、`ShareFile`、`UnshareFile` 均无 outbox 相关返回 |
| `internal/repository/share.go:35-47` | `ListFilesSharedWith` 查询已分享的文件列表 | ✅ 存在 |
| `internal/repository/share.go:50-60` | `ListSharedUsers` 查询文件的分享列表 | ✅ 存在 |
| `internal/handler/http/file.go:273-279` | `ListSharedWithMe` 端点 | ✅ 存在 |

### 问题分析

当一个文件被分享后，文件的拥有者对文件进行修改（更新名称、删除文件、重新上传），当前没有任何机制通知分享接收方。具体场景：

1. **文件更新**：用户 A 分享文件给用户 B。之后 A 修改了文件名或描述。B 不知道文件已更新，仍然引用旧名称。
2. **文件删除**：A 删除（软删）了已分享的文件。B 无法再访问，但之前可能已下载或收藏。系统无任何通知告知 B "文件已被删除"。
3. **文件取消分享**：A 回收了分享权限。B 下次尝试访问时收到 404，但不知道为什么。

这与 Google Drive、Dropbox 等成熟文件同步服务的 UX 形成对比——这些服务在文件被修改、删除或分享变更时都会通知所有相关方。

### 建议方案

1. **定义新的事件类型**（在 `domain/outbox.go` 中）：
   - `file.updated` — 文件元数据更新
   - `file.deleted` — 文件软删
   - `file.share_revoked` — 分享被撤销

2. **新增通知类型**（在 `domain/notification.go` 中）：
   - `NotifTypeFileUpdated`
   - `NotifTypeFileDeleted`
   - `NotifTypeFileShareRevoked`

3. **在对应服务方法中写入 outbox 事件**：
   - `file.UpdateFile` → 写入 `file.updated` 事件
   - `file.DeleteFile` → 写入 `file.deleted` 事件
   - `file.UnshareFile` → 写入 `file.share_revoked` 事件

4. **事件投递的注意点**：
   - 对于 `file.updated` 和 `file.deleted`，需要查询 `file_shares` 表获取所有受影响的 grantee
   - 这些事件需要批量写入 outbox（每个 grantee 一条，或在 payload 中包含 grantees 列表）
   - 注意事务边界：更新/删除文件和写 outbox 事件应在同一个事务中

**规模估算**：中。新增 3 个事件类型 + 3 个通知类型 + dispatcher handler + 各 service 方法修改。约 120 行代码。

---

## 综合优先级建议

| # | 方向 | 优先级 | 成本 | 类型 | 如果不做 |
|---|------|--------|------|------|---------|
| 1 | **Admin 角色管理 API 缺失传输层** | **P0** | 低 | 功能断裂 | 权限系统只能读不能写，admin 只能直连数据库赋权 |
| 2 | **`internal/search/` + `internal/notification/` 死代码包** | **P0** | 极低 | 代码维护 + 安全风险 | CJK 搜索回退的假阳性风险；17 个无意义文件增加维护成本 |
| 3 | **文件分享无通知链路（Outbound）** | **P1** | 低-中 | UX 不对称 | 分享是单向操作，接收方永远不知道被分享 |
| 4 | **后台 Goroutine 无统一 Context 治理** | **P1** | 中 | 运维安全 | 优雅关闭时 relay 最终 drain 可能无限阻塞；orphan GC 残留；trace 断裂 |
| 5 | **文件分享事件无通知链路（Inbound）** | **P2** | 中 | UX 不完整 | 文件更新/删除时分享接收方不知情 |

### 建议执行顺序

1. **方向二（P0）**——删除 17 个死代码文件，5 分钟工作，消除 CJK 搜索回退的假阳性风险。
2. **方向一（P0）**——补齐角色管理 API，使权限系统可用。这是后续所有 admin 功能的基础。
3. **方向三（P1）**——补齐文件分享通知（Outbound），使分享功能的产品体验完整。
4. **方向四（P1）**——统一后台 goroutine 治理，提升运维可靠性和可观测性。
5. **方向五（P2）**——补齐文件变更通知（Inbound），使文件分享体验全面对标成熟服务。
