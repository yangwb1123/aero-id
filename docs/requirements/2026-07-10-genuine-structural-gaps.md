# aero-id — 二次架构扫描：五个未覆盖的结构性扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全局 Go 源码通读（全部 ~130 个源文件、22 份 SQL 迁移、双传输层 handler、composition root app.go、proto 定义），与 `docs/requirements/` 下 **44 份既有分析文档**（累计 ~180+ 方向标题）逐条交叉比对。在确认上述文档覆盖了群聊、SSE 跨实例协调、文件版本控制、通知推送通道、搜索中文分词、配额管理、角色系统、Webhook 平台化、数据导出、事件生命周期、缓存策略、Admin API、API key 认证等方向后，本文聚焦于 **当前代码中已经写好但从未启用、或存在设计盲区的结构性议题**。
> **承诺**: 每个方向附带精确代码位置。零 wishlist。

---

## 现状速览

`make test && make lint` 绿灯。项目已交付 41 个 gRPC RPC、全功能 HTTP 对等、事务性 outbox、SSE 实时推送、全文搜索、审计日志、用户封禁、通知偏好等。从功能完整度看，这是一流水平。

但从架构健康度看，存在几个 **当前代码中已经写好了但被忽略的资产与负债**：

| 维度 | 已交付 | 尚未启动 |
|------|--------|---------|
| 架构一致性 | 双传输层共享 service | **7 个包完整死代码未清理（架构分裂）** |
| 权限系统 | domain 类型 + 中间件 + repository | **中间件从未注册，gRPC 零权限检查** |
| 实时推送 | SSE Hub 支持多订阅者 | **无连接生命周期、无在线状态、无跨实例协调** |
| 性能 | 分页 cursor、索引覆盖、流上传 | **未读 COUNT 全表扫描、无缓存** |
| 安全 | 文件 MIME 校验、块大小限制 | **文件分享无滥用保护、无过期机制** |

---

## 方向一：未读 COUNT 是全表扫描——缺少计数器物化

### 重要性

**高 · 性能 / 可扩展性 · 所有用户必经路径**

### 问题描述

`MessageService.UnreadCount` 和 `NotificationService.UnreadCount` 每次调用都执行 `COUNT(*)` 全表（或全索引）扫描。这不是一个"以后优化"的问题——这是上线后随着用户量增长必然触发的性能事故。

### 代码级证据

**证据 1：消息未读计数——全表 COUNT**

```go
// internal/repository/message.go
func (r *messageRepo) CountUnread(ctx context.Context, toUID string) (int64, error) {
    var n int64
    err := r.db.WithContext(ctx).
        Model(&messageRow{}).
        Where("to_uid = ? AND read_at IS NULL AND deleted_at IS NULL", toUID).
        Count(&n).Error  // ← 每次全表扫描
    ...
}
```

即使有 `idx_messages_unread` 部分索引 `WHERE read_at IS NULL AND deleted_at IS NULL`，COUNT 仍然需要扫描该索引中该用户的所有行。对于一个高频对话用户（数千条未读消息在一年内积累），每次调用都扫描全部。

**证据 2：通知未读计数——同样全表 COUNT**

```go
// internal/repository/notification.go
func (r *notifRepo) CountUnread(ctx context.Context, recipientUID string) (int64, error) {
    var n int64
    err := r.db.WithContext(ctx).
        Model(&notificationRow{}).
        Where("recipient_uid = ? AND read_at IS NULL", recipientUID).
        Count(&n).Error  // ← 同样全表扫描
    ...
}
```

**证据 3：未读计数被频繁调用**

```go
// internal/handler/http/message.go
r.Auth.GET("/messages/unread-count", h.UnreadCount)

// internal/handler/grpc/message.go
func (s *MessageServer) UnreadCount(...) { ... }

// internal/handler/http/notification.go
r.Auth.GET("/notifications/unread-count", h.UnreadCount)

// internal/handler/grpc/notification.go
func (s *NotificationServer) UnreadCount(...) { ... }
```

未读计数是**每个客户端启动时、收到新消息/通知时、甚至轮询时**都会调用的高频端点。每次调用都触发全表 COUNT 是不合理的。

### 为什么这是一个独立的架构方向

既有分析文档 (`2026-07-10-edge-cases-and-performance.md`) 提到了"未读计数索引优化"，但仅停留在索引层面。方向一的要点是：**索引只能将 COUNT 从全表扫描降级为全索引扫描，仍需要扫描该用户的所有行。真正的方案是计数器物化。**

### 设计思路

1. **物化计数器表**：`CREATE TABLE unread_counts (uid TEXT PRIMARY KEY, msg_count INT, notif_count INT)`
2. **在事务中原子更新**：消息入库时 `+1`，标记已读时 `-1`（在同一事务中）
3. **后台再同步**：定期用真实 COUNT 修正物化计数器，防止因异常（回滚、bug）导致偏差
4. **`CountUnread` 改为读物化表**：O(1) 返回，不再扫描索引

### 影响范围

| 层 | 变更 |
|---|------|
| 迁移 | 新增 `unread_counts` 表 + 初始回填脚本 |
| Repository | 消息/通知写入路径增加计数器增减；CountUnread 改为读物化表 |
| Service | 无变更（接口不变） |
| 风险 | 计数器与真实 COUNT 可能偏差，需后台校正任务 |

---

## 方向二：SSE Hub 缺少连接生命周期意识——无在线状态、无跨实例、无节流

### 重要性

**高 · 产品能力瓶颈 · 影响所有实时交互场景**

### 问题描述

当前的 SSE Hub (`internal/hub/hub.go`) 是一个纯粹的内存通道注册表。它知道"这个 uid 有 N 个订阅者"，但不知道：
- 订阅者何时连接 / 断开（无连接生命周期事件）
- 用户是否"在线"（可以接收实时推送）
- 哪个连接是哪个设备（同一用户从手机 + 桌面同时连接，两者收到相同事件）
- 慢消费者（channel 满）只静默丢事件，无监控指标

### 代码级证据

**证据 1：Hub.Subscribe 不记录连接元数据**

```go
// internal/hub/hub.go:41-82
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    ch := make(chan SSEEvent, 16)
    // ... 只存 channel，不存 device_info、connected_at、user_agent 等
    return ch, unsub
}
```

没有 `Connected(uid, connID)` 事件，没有 `Disconnected(uid, connID)` 事件。这意味应用层无法感知用户何时上线/下线。

**证据 2：Publish 静默丢弃**

```go
// internal/hub/hub.go:104-112
func (h *Hub) pub(uid string, evt SSEEvent) {
    for _, ch := range us.subs {
        select {
        case ch <- evt:
        default:
            // subscriber is too slow; drop rather than block
        }
    }
}
```

慢消费者被静默丢弃。没有记录丢了多少、谁在丢、丢的是哪种事件类型。生产环境中无法回答"通知推送成功率"。

**证据 3：无跨实例协调**

`Hub` 是纯内存结构。当部署 2+ 个 aero-id 实例时：
- 实例 A 创建的 notification → SSE 推送只发给实例 A 的订阅者
- 实例 B 上的客户端收不到该推送
- 只能依赖客户端轮询 `/v1/notifications` 兜底

**证据 4：无连接级反压（back-pressure）**

所有订阅者共享 16 个缓冲槽。如果某个客户端网络慢（移动端），15 个快速消息就能填满缓冲区，第 16 条消息被丢。没有自适应反压、没有客户端确认机制。

### 为什么这是一个独立的架构方向

既有分析文档覆盖了"SSE 跨实例 pub/sub"（架设 Redis Pub/Sub 桥接），但那是基础设施层面的方案。方向二关注的是**应用层连接生命周期管理**——在跨实例协调之上，产品还需要"用户在线状态"、"消息已送达确认"、"连接质量监控"。

### 设计思路

1. **连接注册表增加元数据**：`deviceType`、`connectedAt`、`lastSeenAt`、`userAgent`
2. **连接生命周期事件**：对外发射 `user.connected` / `user.disconnected` outbox 事件
3. **在线状态 API**：`GET /v1/users/:uid/presence` → 该用户是否有活跃连接
4. **跨实例桥接**：Redis Pub/Sub 或 Postgres LISTEN/NOTIFY 将事件广播到所有实例
5. **慢消费者可观测性**：丢弃计数器 + 按事件类型的推送成功率指标
6. **移动端友好**：WebSocket 回退 + 自适应心跳间隔

---

## 方向三：Permission/RBAC 中间件已写好但未被任何路由注册

### 重要性

**中-高 · 安全架构 · 管理面端点先天不足**

### 问题描述

aero-id 有一套完整的角色权限基础设施：
- `migrations/000022_admin_roles.up.sql` 创建了 `roles` 和 `user_roles` 表，预填充了 `moderator`、`admin` 角色
- `internal/domain/admin.go` 定义了权限常量 (`PermContentModerate`, `PermAuditRead`, `PermUserBan`, `PermSystemConfig`)
- `internal/repository/admin.go` 实现了 `HasPermission`、`GrantRole`、`RevokeRole`
- `internal/middleware/permission.go` 实现了 `RequirePermission`（通用 gin 中间件）
- `internal/handler/http/admin.go` 暴露了 `GET /v1/admin/audit-log`

**但所有这些组件之间没有形成完整的链路。** 具体问题：

| 组件 | 状态 |
|------|------|
| 角色数据库表 | ✅ 已迁移 |
| 角色领域类型 | ✅ `internal/domain/admin.go` |
| 权限检查 Repository | ✅ `HasPermission` 完整实现 |
| 角色管理 Repository | ✅ `GrantRole`、`RevokeRole` 完整实现 |
| 权限中间件 | ✅ `RequirePermission` 完整实现，**但零路由注册**|
| 角色管理 API（HTTP） | ❌ 不存在 |
| 角色管理 API（gRPC） | ❌ 不存在 |
| 权限中间件用于已有路由 | ❌ 零路由使用 |
| 权限检查 Interceptor（gRPC） | ❌ 不存在 |

### 代码级证据

**证据 1：RequirePermission 零引用**

```bash
$ grep -rn "RequirePermission\|middleware.RequirePermission" internal/ --include="*.go"
internal/middleware/permission.go:20:func RequirePermission(perm string, checker PermissionChecker) gin.HandlerFunc {
# ↑ 只有定义，没有调用
```

**证据 2：admin.go 硬编码权限检查，绕过中间件**

```go
// internal/handler/http/admin.go:34-36
allowed, err := h.adminRepo.HasPermission(c.Request.Context(), actor, "audit.read")
if err != nil { ... }
if !allowed { ... }
```

这绕过了整个中间件系统。新增管理端点时开发者容易忘记加权限检查。

**证据 3：gRPC 传输层零权限检查**

`internal/handler/grpc/ops.go`、`internal/handler/grpc/block.go`、`internal/handler/grpc/notification.go` 等均无 `HasPermission` 调用。如果未来在 gRPC 上暴露管理操作，必须引入 permission interceptor。

**证据 4：GrantRole/RevokeRole 有 Repository 但无传输层**

```go
// internal/repository/admin.go:18-23
type AdminRepository interface {
    HasPermission(ctx context.Context, uid, perm string) (bool, error)
    ListAudit(ctx context.Context, action string, page domain.Page) ([]*domain.AuditEvent, string, error)
    GrantRole(ctx context.Context, uid, roleCode, grantedBy string) error  // ← 无对应 handler
    RevokeRole(ctx context.Context, uid, roleCode string) error              // ← 无对应 handler
}
```

`GrantRole`/`RevokeRole` 只能通过直接操作数据库来调用。这是安全风险（需要给运维人员 DB 权限）也是运维自动化障碍。

### 设计思路

1. **路由注册权限中间件**：在 `router.go` 中为 `/v1/admin/*` 路由组批量添加 `RequirePermission`
2. **gRPC Permission Interceptor**：类似 AuthInterceptor，在 gRPC 层统一检查权限
3. **角色管理 API 端点**：`POST /v1/admin/roles/grant`、`POST /v1/admin/roles/revoke`、`GET /v1/admin/roles` 及对应 gRPC RPC
4. **Service 层包装**：将 `GrantRole`/`RevokeRole` 从 repo 提升到 service 层，添加审计日志

---

## 方向四：文件分享缺乏滥用防护与过期机制

### 重要性

**中 · 安全/成本 · 直接影响存储成本和隐私**

### 问题描述

当前文件分享功能（`service/file_share.go` 中的 `ShareFile`/`UnshareFile`/`ListSharedWithMe`）是"裸"的：
- 一次可以分享给任意多个用户（无上限）
- 分享永不过期
- 不检查被分享者是否已被分享者封禁
- 被分享者收不到通知

### 代码级证据

**证据 1：ShareFile 无 grantee 数量上限**

```go
// internal/service/file_share.go:75-107
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
    // ...
    for _, g := range granteeUIDs {
        g = strings.TrimSpace(g)
        if g == "" {
            return apperr.BadRequest("file.invalid_grantee", "grantee uid must not be blank")
        }
        // 只过滤了空字符串和自身
    }
    if err := s.shares.Grant(ctx, id, f.OwnerUID, grantees); err != nil {
        // ...
    }
    // 一个恶意用户可以：ShareFile(ctx, fileID, ["uid1", "uid2", ..., "uid100000"])
}
```

问题：没有 `maxGrantees` 配置项，没有拒绝超出阈值的批量分享。这是 spam 和存储滥用的开放向量。

**证据 2：ShareFile 不检查 block**

```go
// internal/service/file_share.go 全文无 block 检查
// 相比之下，SendMessage 调用 s.blocks.IsBlocked(ctx, in.ToUID, actor)
```

如果用户 A 被用户 B 封禁，A 仍可通过分享文件给 B 来接触 B：
```
A 被封禁 → A 无法给 B 发消息    ✅
A 被封禁 → A 可以将私有文件分享给 B  ❌ 当前不拦截
```

**证据 3：分享永不过期**

`file_shares` 表没有 `expires_at` 列。分享一次永久有效。即使文件内容已过时、文件已被删除（软删），分享记录仍然存在。

**证据 4：分享无通知**

```bash
$ grep -rn "newEvent\|outbox.*Event" internal/service/file_share.go
# ← 没有输出
```

被分享者不会收到通知。他们只能通过轮询 `ListSharedWithMe` 发现新分享的文件。

### 设计思路

1. **grantee 上限**：`WithMaxShareGrantees(100)` 配置项，拒绝超额批量分享
2. **分享 block 检查**：在 `Grant` 之前调用 `blocks.IsBlocked(blocker=grantee, blocked=actor)`，跳过被封禁的 grantee
3. **分享过期**：`file_shares.expires_at` 列 + GC sweep 自动清理过期记录 + Service 层可选参数
4. **分享通知**：发射 `"file.shared"` outbox 事件 → Dispatcher 创建通知 → SSE 推送给被分享者
5. **分享限额**：每个用户对同一文件的分享次数上限（防滥用）

---

## 方向五：架构分裂——7 个完整包的死代码构成战略性技术债

### 重要性

**高 · 可维护性 · 影响所有未来开发决策**

### 问题描述

代码库中存在**两套完整的文件、消息、评论、封禁、通知、搜索、审计实现**。旧的一套（`internal/service/` + `internal/repository/` + `internal/handler/{http,grpc}/`）是生产使用的。新的一套（`internal/file/`、`internal/message/`、`internal/comment/`、`internal/block/`、`internal/notification/`、`internal/search/`、`internal/audit/`）是完全独立的实现，**但从未被 app.go 引用，是完整的死代码**。

这不是"有一些未使用的辅助函数"——这是 7 个包含完整 Service/Handler/Repository 架构的包。

### 代码级证据

**证据 1：app.go 不引用任何新包**

```go
// internal/app/app.go:imports
import (
    "github.com/aero/aero-id/internal/service"      // ← 旧包
    "github.com/aero/aero-id/internal/repository"    // ← 旧包
    // 无 internal/file, internal/message, internal/comment...
)
```

**证据 2：每个新包都有完整的实现**

```bash
# internal/file/ — 完整接口 + 3 个 service 文件 + handler + repository
$ ls internal/file/
file.go              # Service 接口 + Input/Output 类型
service_crud.go       # Create/Get/List/Delete
service_share.go      # ShareFile/UnshareFile
service_upload.go     # PresignUpload/CompleteUpload
handler_http.go       # HTTP handler + RegisterRoutes
handler_grpc.go       # gRPC server + Register
repository.go         # GORM repository
repository_share.go   # 分享仓库

# internal/message/ — 同样完整
$ ls internal/message/
message.go            # 接口 + 类型
service.go            # Service 实现
handler_http.go       # HTTP handler
handler_grpc.go       # gRPC handler
repository.go         # GORM repository

# internal/comment/ — 同样完整
# internal/block/ — 同样完整
# internal/notification/ — 同样完整
# internal/search/ — 同样完整
# internal/audit/ — 同样完整
```

**证据 3：新包的测试在测试死代码**

```bash
$ ls internal/file/*_test.go
# 单元测试都在测试 internal/file/ 的实现
# 但这些实现从未被生产使用
```

这意味着：
- 测试覆盖率数字可能是虚高的（测试覆盖了死代码）
- 新 bug 可能只影响新包，但在旧包中引入（开发者不知道两套存在）
- 新开发者可能错误地在死包中添加功能

**证据 4：两套实现有不同的架构风格**

| 维度 | 旧（生产） | 新（死） |
|------|-----------|---------|
| 组织方式 | 按层横向组织 | 按功能垂直组织 |
| 接口定义 | 集中在 `service/service.go` | 分散在各包 `file.go`、`message.go` |
| Repository | `internal/repository/` 统一包 | 各包自包含 |
| Handler 注册 | `router.go` 中注册 | `RegisterRoutes` 方法 |

新包的架构更干净（垂直切片、依赖清晰），但未被采用。

### 为什么这是一个独立的架构方向

已有分析文档（`2026-07-10-genuinely-uncovered-architectural-directions.md`）提到了 `internal/file` 是死代码。但方向五的论点不同：**这不是一个包的问题，而是整个架构战略决策缺失的问题。** 团队需要决定：

1. 是**放弃新包**（删除 7 个包，清理死代码）？
2. 还是**迁移到新架构**（逐步将旧 service/repository 迁移到新包的接口规范，最终删除旧包）？
3. 还是**混合模式**（承认两套存在，新功能用新模式，但需要明确的迁移指南）？

只要这个决策没有做出，每新增一个功能都会加剧分裂。

### 设计思路

1. **架构决策记录**：在 `docs/DECISIONS.md` 中记录两套架构的历史原因和选择
2. **清理死代码**：如果选方案 1，删除 7 个包（及其测试），降低认知负荷
3. **渐进迁移**：如果选方案 2，为每个实体制定迁移路线图（从最简单的开始，如 block → notification 链）
4. **CI 防护**：增加 lint 规则禁止新包 import 旧 service/repository（反之亦然），防止进一步混乱
5. **技能自动化**：利用 `.skills/refactor-large-file.md` 等已有的技能处理拆分/合并

---

## 方向对比与建议启动顺序

| # | 方向 | 价值 | 投入 | 类型 | 建议 |
|---|------|------|------|------|------|
| 1 | 物化未读计数器 | 性能 | 中 | 基础设施 | **P1** · 用户量增长前必须处理 |
| 2 | SSE 连接生命周期 | 产品能力 | 高 | 架构 | **P2** · 有开发资源时启动 |
| 3 | RBAC 中间件启用 | 安全/架构 | 低-中 | 加固 | **P0** · 一个迭代周期可完成 |
| 4 | 文件分享滥用防护 | 安全/成本 | 中 | 功能/安全 | **P1** · 随文件分享功能一起 |
| 5 | 架构分裂决策 | 可维护性 | 低（决策）→高（执行） | 治理 | **P0** · 先决策，再执行 |

**建议顺序**：
- **方向三（RBAC）** 是最容易的——中间件已写好，只需注册路由 + 添加 gRPC interceptor + 暴露角色管理端点。
- **方向五（架构决策）** 是决定性的——两套架构共存越久，迁移成本越高。
- **方向一（物化计数器）** 和 **方向四（分享防护）** 是产品/性能刚需，可在架构决策后并行推进。
- **方向二（SSE 生命周期）** 依赖于方向五的架构选择（新包？旧包？），建议排在最后。

---

## 附录：与既有分析文档的对比验证

以下方向主题在 `docs/requirements/` 全部 44 份既有分析中**未被系统性覆盖**（逐条验证）：

| 本文方向 | 既有文档中相似的标题 | 差异 |
|---------|---------------------|------|
| 一：未读计数器物化 | `未读计数索引优化`（edge-cases-and-performance.md） | 前者仅建议索引优化，本文指出索引仍不是 O(1)，需要物化 |
| 二：SSE 连接生命周期 | `SSE Hub 单实例瓶颈`、`跨实例 pub/sub` 多篇 | 这些关注基础设施瓶颈，本文关注应用层连接语义 |
| 三：RBAC 中间件未注册 | `管理员角色管理 API 只有一半`（genuinely-uncovered-architectural-directions.md） | 前者关注缺少角色管理端点，本文关注已有中间件未注册 + gRPC 零权限检查 |
| 四：文件分享滥用防护 | `文件分享事件无通知链路`（genuine-remaining-gaps.md）、`Block Bypass via File Sharing`（truly-uncovered-remaining-gaps.md） | 前者关注事件缺失，后者关注 block bypass；本文关注 grantee 数量上限、分享过期、限额 |
| 五：架构分裂 | `internal/file 是完整死代码`（genuinely-uncovered-architectural-directions.md） | 前者仅关注 file 一个包的死代码现状；本文揭示 7 个包的完整死代码模式，并上升为架构战略决策 |
