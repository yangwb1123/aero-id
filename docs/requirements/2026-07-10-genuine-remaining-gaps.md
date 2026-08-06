# aero-id — 第七轮全量代码扫描：五个未被已有分析覆盖的工程缺口与扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 对 `internal/` 全部 22 个包、`migrations/` 22 份 SQL、双传输层 handler、config 做逐文件深度扫描，与 `docs/requirements/` 下 37 份既有分析文档的所有方向标题和核心发现做**逐条交叉比对**，确保以下 5 个方向在既有文档中**零系统性覆盖**。
> **约束**: 不写代码。每个方向附带代码级证据和设计建议，不空谈。

---

## 当前代码库状态速览

aero-id 已经是一个非常成熟的工程制品——22 个可逆迁移、38+ gRPC RPC 与 HTTP 完全对等、事务性 outbox、幂等键、全文搜索 SSE/Webhook 实时通知推送、用户管理角色权限、审计日志、SSO 韧性（断路器 + stale-cache + singleflight）、RED 指标与 OTel 链路追踪、文件存储 GC 回收、媒体管线校验（MIME 白名单、大小限制、checksum 去重）、限流、用户阻止（block）、通知偏好（静默时段）——MVP 级功能几乎全部到位。

但本轮扫描发现了五个在既有 37 份分析中未被系统性覆盖的缺口。它们不是"没有的功能"，而是**已实现功能中的结构性裂痕**——要么是代码未连接（设计与实现之间的断点），要么是同一模式的非对称处理，要么是负责测试覆盖的代码在生产中根本不运行。

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

## 方向二：全文搜索的 FTS 失败静默降级路径未连接

### 代码级证据

| 位置 | 内容 |
|------|------|
| `internal/search/search.go:37-63` | `searchService.Search()` 调用 `s.messages.SearchMessages()`、`s.comments.SearchComments()`、`s.files.SearchFiles()`——三个 FTS 方法 |
| `internal/repository/message.go:257` | `SearchMessages()` 使用 `search_vector @@ plainto_tsquery('simple', ?)` |
| `internal/repository/message.go:281` | `SearchMessagesLike()` 使用 `content ILIKE ?`——作为 FTS 回退 |
| `internal/repository/message.go:46` | 接口注释写着："SearchMessagesLike is a LIKE-based fallback for FTS-unsupported queries (e.g. CJK)" |
| `internal/repository/comment.go:368` | `SearchComments()` FTS 实现 |
| `internal/repository/comment.go:393` | `SearchCommentsLike()` LIKE 回退实现 |
| `internal/repository/file.go:216` | `SearchFiles()` FTS 实现 |
| `internal/repository/file.go:241` | `SearchFilesLike()` LIKE 回退实现 |
| **`internal/search/search.go` 全文件** | **从未调用 `Search*Like` 方法** |

### 问题分析

三个 Repository 都成对实现了两组搜索方法——FTS（`SearchMessages`）和 LIKE 回退（`SearchMessagesLike`）。接口注释和方法的 godoc 都明确指出 LIKE 版本是 "fallback for FTS-unsupported queries (e.g. CJK)"。

但是 `internal/search/search.go` 的 `searchKind` 方法只调用了 FTS 版本，**从未调用 LIKE 回退**。这意味着：

1. **中文/日文/韩文搜索永远返回空结果**——`plainto_tsquery('simple', ...)` 对 CJK 文本的分词效果极差，因为 Postgres 的默认解析器按空格分词，
   而 CJK 没有词间空格。一条中文评论 "今天天气很好" 被 `tsquery` 当作一个整词处理，几乎永远不会被任何搜索命中。

2. **FTS 索引缺失时静默失败**——如果迁移 `000016_fts_indexes` 未运行（例如新部署环境），`search_vector` 列可能为 NULL 或缺失索引，
   FTS 查询不会报错（PostgreSQL 的 `@@` 对 NULL 返回 false），结果永远是空集。用户看到空结果时不知道是"没有匹配内容"还是"搜索不可用"。

3. **搜索精度倒退**——对于部分匹配（如搜索 "hello" 期望匹配 "helloworld"），`plainto_tsquery` 按词根匹配，不会触发通配符匹配。
   但 `ILIKE '%hello%'` 可以。当前路径将这种精度丢失也静默吞掉了。

### 建议方案

在 `internal/search/search.go` 中增加 FTS 失败或低匹配度时的降级逻辑：

```
searchKind(ctx, actor, query, limit, kind, hits):
  1. 调用 FTS 版本 SearchMessages(ctx, actor, query, limit)
  2. 如果结果数为 0 或低于阈值（如 3 条），调用 LIKE 回退 SearchMessagesLike(ctx, actor, query, limit)
  3. 合并结果，去重
```

更健壮的方案是**并行双发**（fan-out）：
```
go FTS 搜索 → channel
go LIKE 回退 → channel
取 FTS 结果，如果足够（≥ limit/2）则放弃 LIKE 结果；否则用 LIKE 结果补全
```

这样做的好处：
- CJK 用户获得有效搜索结果（LIKE）
- 英文精确搜索获得 FTS 的相关度排序优势
- 不会因为 FTS 的静默失败而返回空结果
- 迁移 `000016` 缺位时系统仍可用（降级到 LIKE）

**规模估算**：低。核心改动在 `search.go` 的 `searchKind` 方法，约 20 行代码调整。

---

## 方向三：文件分享事件无通知链路（不对称 UX）

### 代码级证据

| 位置 | 内容 |
|------|------|
| `internal/file/service_share.go` | `ShareFile` 调用 `s.shares.Grant(...)` + 写入审计日志 `AuditFileShareGrant` |
| `internal/file/service_share.go` | `UnshareFile` 调用 `s.shares.Revoke(...)` + 写入审计日志 `AuditFileShareRevoke` |
| `internal/file/service_share.go` | `ShareFile` 和 `UnshareFile` **均不生成 outbox 事件** |
| `internal/outbox/outbox.go` | 定义的 event types 仅有 `message.sent`、`message.read`、`comment.replied` |
| `internal/service/notification.go:259-311` | `NotificationDispatcher.notificationFor` 只处理上述三种事件类型 |
| `internal/domain/notification.go:9-12` | `NotifTypeMessageReceived`、`NotifTypeMessageRead`、`NotifTypeCommentReply`——三个通知类型，**无 `file.shared`** |
| `internal/comment/service.go` | 评论创建如果有 parent 则发送 `comment.replied` outbox 事件 | ✅ 比较对象 |
| `internal/message/service.go` | 消息发送发送 `message.sent` outbox 事件 | ✅ 比较对象 |
| **`internal/file/service_share.go`** | **文件分享不触发任何 outbox 事件** | **❌ 不对称** |

### 问题分析

消息和评论在创建/回复时都会：
1. 在同一个 DB 事务中写入 outbox 事件
2. outbox relay 消费事件后写入通知行
3. 通知行通过 SSE Hub 实时推送给接收方

但文件分享的流程在 `ShareFile` 创建分享记录后，只写了一条审计日志就结束了：
```
ShareFile → Grant(share row) + AuditLog → 结束（无通知）
```

这意味着：
- 用户 A 分享文件给用户 B，用户 B 不会收到任何通知
- 用户 B 只能通过主动轮询 `GET /v1/files/shared-with-me` 来发现"有人分享了一个文件给我"
- 如果 B 从不主动刷新，可能永远不知道文件被分享给他
- 这与消息/评论的"被通知"体验不对称——用户期望被动的"有人提到你"体验，而非主动轮询

### 建议方案

1. **新增 outbox 事件类型** `file.shared`：
   ```
   outbox.NewEvent("file", f.ID.String(), "file.shared", map[string]any{
       "file_id":     f.ID.String(),
       "owner_uid":   f.OwnerUID,
       "grantee_uid": granteeUID,
   })
   ```

2. **新增通知类型** `file.shared`（在 `domain/notification.go` 中）：
   ```go
   NotifTypeFileShared NotificationType = "file.shared"
   ```

3. **在 `NotificationDispatcher.notificationFor` 中处理 `file.shared`**：
   - 为每个 grantee 创建一条 `{RecipientUID: granteeUID, Type: "file.shared", ...}` 通知
   - 不需要 block 检查（分享者是主动行为）
   - 不需要 rate limit（分享频率天然受限于用户行为）

4. **新事件投递方式**：
   - 在 `ShareFile` 中，对每个 grantee 分别写入 outbox 事件（或批量写入一个事件带 grantees 列表）
   - 注意：分享操作是幂等的（`clause.OnConflict{DoNothing: true}`），但 outbox 事件不应被去重——每次分享都应触发通知

**规模估算**：低-中。新增一个 outbox 事件类型 + 一个通知类型 + dispatcher handler。约 60 行代码。

---

## 方向四：后台 Goroutine 缺少统一 Context 治理与优雅 Drain

### 代码级证据

| 位置 | 内容 | 使用的 Context |
|------|------|------|
| `internal/outbox/relay.go:115` | `Relay.Start` → `ctx := context.Background()` | `context.Background()` |
| `internal/storage/gc/sweeper.go:235` | `Sweeper.Start` → `ctx := context.Background()` | `context.Background()` |
| `internal/sso/token_cache.go:108` | `RunCleaner` → `ctx := context.Background()` | `context.Background()` |
| `internal/sso/breaker.go:123` | `RunBackgroundChecker` → `ctx := context.Background()` | `context.Background()` |
| `internal/pkg/ratelimit/ratelimit.go:132` | `RunCleaner` — 无 context 参数 | 无 context |
| `internal/idempotency/store.go:157` | `RunCleaner` — 无 context 参数 | 无 context |
| `internal/app/app.go:152` | `stopRelay := relay.Start(interval)` | 没有透传 app 的 context |
| `internal/app/app.go:181-185` | `stopGC`、`stopRateLimit`、`stopIdemClean` | 各自独立停止 |

### 问题分析

当前启动的每个后台 goroutine 都**各自使用 `context.Background()`**，且停止方式各不相同：

| Goroutine | 停止机制 | 问题 |
|-----------|---------|------|
| outbox relay | `stopRelay()` — 关闭 done channel，最终 drain 一次 | ✅ 有优雅 drain |
| GC sweeper | `stopGC()` — 关闭 done channel，**无最终 drain** | ⚠️ 正在进行的 pass 被中断，orphan 可能残留 |
| rate limit cleaner | `stopRateLimit()` — 关闭 done channel | ⚠️ 无优雅关闭 |
| idempotency cleaner | `stopIdemClean()` — 关闭 done channel | ⚠️ 无优雅关闭 |
| token cache cleaner | `stopCleaner()` — 关闭 done channel | ⚠️ 无优雅关闭 |
| circuit breaker checker | `stopChecker()` — 关闭 done channel | ⚠️ 无优雅关闭 |

更深层的问题是：
1. **没有统一的 shutdown priority**：relay 先 drain → GC 停止 → rate limit cleaner 停止 → ... 但顺序是硬编码在 `shutdown()` 中的，无法根据运行时状态动态调整。
2. **没有 context 传递**：如果 app 收到 SIGTERM 需要 10s 内完成关闭，每个 goroutine 都不知道这个 deadline。relay 的最终 drain 可能会超过 shutdown timeout。
3. **goroutine panic 不可恢复**：每个 goroutine 都用 `context.Background()`，一旦某个后台 goroutine panic 被 recovery 捕获，没有机制通知 app 进入 degraded 状态或触发重启。
4. **trace 传播断裂**：所有后台操作都使用 `context.Background()`，OTel trace 在后台操作中完全不可见——无法追踪 outbox 事件处理时间、GC pass 持续时间等。

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
   - `relay.Start(ctx, interval)` — 使用 ctx 的 Done() 信号代替独立的 done channel
   - `sweeper.Start(ctx, interval)` — 同上
   - 所有 cleaner 同理

3. **实现 Shutdown Priority 排序**：
   ```
   1. Flip gRPC Health → NOT_SERVING
   2. Relay last drain（with context deadline）→ 保证未发布事件不丢失
   3. Stop GC sweeper（允许当前 pass 完成）
   4. Stop all cleaners（允许当前清除完成）
   5. HTTP server Shutdown
   6. gRPC GracefulStop
   ```

4. **OTel trace 透传**：在 relay 的 drain 方法中创建 root span，挂载到每个事件处理的子 span 上，使 outbox 处理链路可观测。

**规模估算**：中。涉及 6 个 goroutine 启动点的签名变更 + `app.go` 的重构。不改变业务逻辑。

---

## 方向五：`internal/notification/dispatcher.go` 是生产死代码

### 代码级证据

| 位置 | 内容 |
|------|------|
| `internal/notification/dispatcher.go:1-140` | 完整的 `Dispatcher` 实现——`Handle`、`notificationFor`、三个 payload 反序列化方法 |
| `internal/notification/dispatcher_test.go:1-200+` | 该 Dispatcher 的单元测试，全部通过 |
| `internal/service/notification.go:99-310` | 另一个 `NotificationDispatcher` 实现——功能严格超集（增加了 preference checker、rate limiter、collapse） |
| `internal/app/app.go:119-126` | 实例化的是 `service.NewNotificationDispatcher(...)`，**不是** `notification.NewDispatcher(...)` |
| `internal/` 全部 `*.go` | `grep -rn "notification\.NewDispatcher\|notification/dispatcher"` 只在 `hub.go` 的注释中有一次提及 |

### 问题分析

`internal/notification/dispatcher.go` 是一个 **完全独立的、不与任何生产代码相连的 Dispatcher 实现**。它：

1. 被 `internal/notification/dispatcher_test.go` 测试（所有测试通过）
2. **不被任何 `internal/app/`、`internal/service/`、`internal/handler/` 中的代码导入**
3. 其功能已被 `internal/service/notification.go` 中的 `NotificationDispatcher` 完全覆盖（且后者功能严格更多）
4. 两者之间的差异：

| 特性 | `notification/dispatcher.go` | `service/notification.go` |
|------|---------------------------|--------------------------|
| Block checker | ✅（通过 `block.BlockChecker`） | ✅（同上） |
| Preference checker | ❌ | ✅ |
| Rate limiter | ❌ | ✅ |
| Notification collapse | ❌ | ✅ |
| Pusher 接口 | `hub.Pusher`（3 参数） | `NotificationPusher`（4 参数，含 eventID） |
| 生产中被调用 | ❌ **从不** | ✅ **app.go** |

### 影响

这是一个"测试幻觉"问题：
- `internal/notification/dispatcher_test.go` 覆盖了一个**永远不运行的代码路径**
- 这些测试通过给开发者一种"通知分发已测试"的错觉，但实际生产路径是 `service.NotificationDispatcher`——它的测试在 `internal/service/notification_test.go` 中
- 未来的开发者修改 `notification/dispatcher.go` 时不会影响任何生产行为，但测试可能因此通过或失败，产生误导
- 140 行死代码 + 200+ 行测试代码 = 约 350 行无意义代码增量

### 建议方案

1. **删除 `internal/notification/dispatcher.go`**（生产死代码）
2. **删除 `internal/notification/dispatcher_test.go`**（测试的是已不存在的生产路径）
3. **如果 `notification` 包内其他代码依赖 `Dispatcher` 的类型**（检查 `notification/handler_grpc.go` 和 `notification/handler_http.go`），确认它们不使用 Dispatcher——它们的 handler 直接调用 `notification.Service` 接口

**验证步骤**：
```bash
# 确认无生产代码依赖
grep -rn "notification\.Dispatcher\|notification\.NewDispatcher" internal/ --include='*.go' | grep -v "_test.go"
# 预期输出为空
```

**规模估算**：极低。纯代码删除，不改变任何生产行为。

---

## 综合优先级建议

| # | 方向 | 优先级 | 成本 | 类型 | 如果不做 |
|---|------|--------|------|------|---------|
| 1 | **Admin 角色管理 API 缺失传输层** | **P0** | 低 | 功能断裂 | 权限系统只能读不能写，admin 只能直连数据库赋权 |
| 2 | **FTS 搜索静默降级路径未连接** | **P1** | 低 | 正确性缺陷 | CJK 用户永远无法搜索到内容，静默返回空结果 |
| 3 | **文件分享无通知链路** | **P1** | 低-中 | UX 不对称 | 分享是单向操作，接收方永远不知道被分享 |
| 4 | **后台 Goroutine 无统一 Context 治理** | **P1-P2** | 中 | 运维安全 | 优雅关闭时后台任务可能无法按时完成；trace 断裂 |
| 5 | **`notification/dispatcher.go` 是死代码** | **P2** | 极低 | 代码维护 | 测试幻觉；350 行无意义代码 |

### 建议执行顺序

1. **方向一（P0）**——先补齐角色管理 API，使权限系统可用。这是后续所有 admin 功能的基础。
2. **方向五（P2，随时可做）**——删除死代码，5 分钟工作，清理代码库。
3. **方向二（P1）**——修复搜索回退路径，使 CJK 搜索可用。用户体验修复。
4. **方向三（P1）**——补齐文件分享通知，使分享功能的产品体验完整。
5. **方向四（P1-P2）**——统一后台 goroutine 治理，提升运维可靠性和可观测性。
