# aero-id — 全局深度扫描：四个体系性架构盲区

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 逐文件扫描 `internal/` 全部 ~180 个 Go 源文件、`migrations/` 全部 22 份 SQL、`api/proto/`、`configs/`、`cmd/`、`Makefile`、`docs/`。与 `docs/requirements/` 下 **94 份既有分析**、`docs/REVIEW_*` 下 **8 份审查文档**、`ROADMAP.md`、`docs/CHANGELOG.md`、`docs/ARCHITECTURE.md`、`docs/DECISIONS.md` 逐方向交叉比对。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和架构师/产品经理双视角论证。

---

## 交叉验证声明

以下方向在 94 份既有需求分析、8 份架构审查、ROADMAP、CHANGELOG、ARCHITECTURE 和 DECISIONS 文档中 **零系统性覆盖**——没有文档将它们作为独立方向提出并做完整代码证据链论证。

---

## 方向一：SSE Hub 实例碎片化——多部署下实时推送覆盖面分裂

### 问题陈述

SSE（Server-Sent Events）实时推送是 aero-id 通知系统的关键 UX 差异化能力。然而，当前实现**仅针对单实例运行**。多实例部署下，用户连接到实例 A，则由实例 B 的 outbox relay 分发的通知将无法实时到达用户——用户必须等到 SSE 重连（通过 `last_event_id` 回放遗漏事件）才能看到这些通知。

这直接破坏了"实时推送"的核心体验承诺。

### 代码级证据

**证据 1：Hub 是纯内存的进程级结构，无跨实例共享机制**

```go
// internal/hub/hub.go:19-29
type Hub struct {
    subs   sync.Map // uid → *uidSubs
    nextID atomic.Int64
    closed atomic.Bool
}

func New() *Hub {
    return &Hub{}
}
```

`sync.Map` 中的映射完全在进程内存中。没有 Redis Pub/Sub、没有 gRPC broadcast、没有共享数据库轮询——没有任何跨实例协调机制。

**证据 2：Dispatcher 在本地推送——仅通知本进程的 Hub**

```go
// internal/service/notification.go（Dispatcher.Handle 方法，约第 200 行）
if d.pusher != nil {
    payload, _ := json.Marshal(n)
    d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
}
```

`d.pusher` 在 `app.go` 中只被赋值为一个 `*hub.Hub` 实例。这意味着：

```
实例 A: outbox relay 处理 event_X → DB 写入通知行 → A.hub.Publish(uid_X, ...)
实例 B: outbox relay 处理 event_Y → DB 写入通知行 → B.hub.Publish(uid_Y, ...)
```

如果用户连接到实例 A（通过负载均衡器），那么 event_Y 永远无法实时到达——尽管 DB 中有通知行。

**证据 3：SSE reconnection replay 是唯一的补偿机制——但不完整**

```go
// internal/handler/http/events.go:78-108
func (h *EventsHandler) replayMissed(c *gin.Context, uid, lastEventID string) {
    // ...
    missed, err := h.fetcher.ListSince(c.Request.Context(), uid, since, 50)
    // ... 重放遗漏通知
}
```

这个机制只在客户端主动重连时触发。在两次重连之间的时间窗口内，用户完全不知道有其他实例产生的通知。而且 `limit=50` 意味着如果在断连期间积累了超过 50 条通知，部分通知会丢失。

**证据 4：`app.go` 中只有一个 Hub 实例**

```go
// internal/app/app.go:132
sseHub := hub.New()

// internal/app/app.go:278
httpapi.NewEventsHandler(sseHub, notifRepo),
```

无多实例配置项，无分布式通道注入点。

### 为什么这是盲区

既有分析覆盖了"多实例并发安全"（outbox 重复处理、写偏斜）和"分布式协调"（分布式限速、分布式缓存），但 **没有文档将 SSE Hub 的实例碎片化作为独立的实时推送盲区来分析**。这是一个纯产品体验问题——功能存在（SSE 推送已实现），但在多实例部署下 UX 承诺断裂。

### 架构师视角

根本问题在于 **Hub 的 Publish 边界与 outbox relay 的 Publish 边界不一致**。outbox relay 在每个实例上都运行，且都在共享 DB 中写入通知行（这是正确的），但 Hub 的 Publish 是进程本地操作。修复有两种路径：

1. **跨实例 broadcast**：通过 Redis Pub/Sub 或 gRPC stream 将 Publish 事件广播到所有实例，让每个实例的 Hub 都推送给本地连接的用户。这是最彻底的方案，但引入 Redis 依赖。
2. **SSE-only 的 DB polling fallback**：在 SSE handler 中增加一个短间隔（如 5-10s）的后备轮询，检查 DB 中是否有新通知超出当前 SSE 流的已知范围。无需额外基础设施，但增加延迟和 DB 负载。

### 产品经理视角

对于 MVP 或单实例部署，这不是问题。但对于任何面向生产的多实例部署（K8s 多副本），实时推送将随机间歇性失效——用户可能会抱怨"消息收到但没有通知"、"有时候收不到推送"。这种不一致的体验比完全没有推送更糟糕，因为它破坏了用户对产品的信任。

### 边界情况

- **负载均衡器亲和性（session stickiness）**：如果 LB 配置了基于 uid 的 sticky session，用户可以始终连接到同一实例，部分缓解该问题。但这需要 LB 支持，且部署拓扑变更（滚动更新）会破坏亲和性。
- **SSE 重连延迟**：默认的浏览器 SSE 重连延迟通常是 3-5 秒，浏览器在断连后自动重连。但在此期间产生的通知仍然丢失。
- **Replay 容量溢出**：`ListSince` 的 `limit=50` 在断连时间较长时会截断历史，部分通知永远消失。

### 建议优先级

**P1-高**（产品体验）。在进入多实例部署前必须解决。

---

## 方向二：Admin API 架构违例与功能碎片化

### 问题陈述

aero-id 构建了完整的 admin 角色/权限基础设施（`internal/domain/admin.go`、`migrations/000022_admin_roles.up.sql`），但管理 API 面只有**一个端点**——`GET /v1/admin/audit-log`，且直接注入 `repository.AdminRepository` 到 HTTP handler 中，违反了架构的单向依赖规则（handler → service → repository）。

### 代码级证据

**证据 1：Admin handler 直接注入 Repository（架构违例）**

```go
// internal/handler/http/admin.go:10-14
type AdminHandler struct {
    adminRepo repository.AdminRepository  // ← 直接引用 repository 包！
}

func NewAdminHandler(adminRepo repository.AdminRepository) *AdminHandler {
    return &AdminHandler{adminRepo: adminRepo}
}
```

在整个代码库中，这是**唯一**一个 handler 直接持有 repository 实现的地方。其他所有 handler 都通过 `service` 接口，这是项目架构契约的明文违例。

**证据 2：Permission 检查是手动的而非使用中间件**

```go
// internal/handler/http/admin.go:27-35
allowed, err := h.adminRepo.HasPermission(c.Request.Context(), actor, "audit.read")
if err != nil {
    httpx.WriteError(c, ...)
    return
}
if !allowed {
    httpx.WriteError(c, apperr.Forbidden(...))
    return
}
```

虽然项目已有 `middleware.RequirePermission`：
```go
// internal/middleware/permission.go:18-19
func RequirePermission(perm string, checker PermissionChecker) gin.HandlerFunc {
```

但 admin handler 内部自行检查权限，没有使用该中间件。这意味着每个新的 admin 端点必须重复这段代码。

**证据 3：角色管理面完全空白**

```go
// internal/domain/admin.go
const (
    PermContentModerate = "content.moderate"
    PermAuditRead       = "audit.read"
    PermUserBan         = "user.ban"
    PermSystemConfig    = "system.config"
)
```

种子数据：
```sql
-- migrations/000022_admin_roles.up.sql
INSERT INTO roles (code, name, permissions) VALUES
    ('moderator', 'Moderator',   ARRAY['content.moderate', 'audit.read']),
    ('admin',     'Administrator', ARRAY['content.moderate', 'audit.read', 'user.ban', 'system.config']);
```

已有权限定义和角色种子数据，但**没有任何 API 端点**：
- 列出/创建/更新/删除角色
- 为用户授予/撤销角色
- 查询用户的权限
- 按角色列出用户

**证据 4：`AdminRepository` 只有两个方法**

```go
// internal/repository/admin.go
type AdminRepository interface {
    HasPermission(ctx context.Context, uid, perm string) (bool, error)
    ListAudit(ctx context.Context, action string, page domain.Page) ([]*domain.AuditEvent, string, error)
}
```

没有角色 CRUD，没有用户角色管理，没有权限查询。

**证据 5：gRPC 侧完全没有 admin handler**

```bash
$ grep -rn "Admin\|admin" internal/handler/grpc/ --include='*.go' | grep -v "_test\|aeropb"
# → 只有 ops.go 中的 ops 端点，无 admin 服务
```

admin 端点仅存在于 HTTP 传输（`internal/handler/http/admin.go`），gRPC 无对应实现。这是传输层对偶性（parity）的一个缺口——其他所有功能都完成了 HTTP ↔ gRPC 对偶。

### 为什么这是盲区

已有分析覆盖了"管理员角色系统"（作为已交付功能列出）和"双服务层架构债务"，但**没有文档将 Admin API 的架构违例和功能碎片化作为独立方向**——这是一个跨架构纪律、功能完整性和运维成熟度的综合问题。

### 架构师视角

该问题有三个层面：

1. **架构违例**：`AdminHandler` 直接引入 `repository` 包，破坏了 handler → service → repository 的单向依赖。这使该 handler 无法被测试（无法 mock repository），也无法被 gRPC 复用（gRPC handler 不共享该代码路径）。修复方式：创建 `AdminService` 并通过 service 层处理权限检查和审计查询。

2. **功能不完整**：已有完整的角色/权限数据模型（`roles` + `user_roles` 表、种子数据），但没有管理端点来操作它们。管理员只能直接操作数据库来管理角色——这是一个运维缺陷。

3. **传输对偶性缺口**：其他所有功能都完成了 HTTP/gRPC 对偶（38 个 RPCs 覆盖 5 个 services），但 admin 端点只有 HTTP。需要新增 `AdminService` gRPC 服务和对应的 HTTP 端点。

### 产品经理视角

对于运营团队来说，无法通过 API 管理角色意味着：
- 每次添加新管理员都需要直接 DB 操作（有风险、不可审计）
- 无法构建管理控制台 UI
- 无法实现自助式的权限变更审批流程
- 审计查询端点单一，无法按用户/时间段/操作类型过滤

### 建议补全的 Admin API 端点

| 端点 | 权限 | 用途 |
|------|------|------|
| `GET /v1/admin/roles` | `system.config` | 列出所有角色 |
| `POST /v1/admin/roles` | `system.config` | 创建自定义角色 |
| `PUT /v1/admin/roles/:code` | `system.config` | 更新角色权限 |
| `DELETE /v1/admin/roles/:code` | `system.config` | 删除角色 |
| `GET /v1/admin/roles/:code/users` | `audit.read` | 列出某角色的所有用户 |
| `POST /v1/admin/users/:uid/roles` | `system.config` | 为用户授予角色 |
| `DELETE /v1/admin/users/:uid/roles/:code` | `system.config` | 撤销用户角色 |
| `GET /v1/admin/audit-log` | `audit.read` | 扩展过滤（已存在，需加强） |

### 边界情况

- **自我降级**：管理员从自己账户移除 `system.config` 权限后，不应失去对所有 admin API 的访问
- **角色引用完整性**：删除角色时，需处理关联的 `user_roles` 行
- **内置角色不可变**：`moderator` 和 `admin` 种子角色应禁止删除，但允许修改权限

### 建议优先级

**P2-中**（运维效率+架构合规）。

---

## 方向三：软删除引用碎片——依赖表孤儿行累积

### 问题陈述

aero-id 对所有主实体（文件、消息、评论）使用 GORM 的软删除（`gorm.DeletedAt`）。但当一个父行被软删除时，其依赖表中的关联行（文件分享记录、消息 Reactions、评论 Likes、评论 Reports）不会被清理或标记为孤儿。长期运行后，这些孤儿行会：

1. **使查询效率下降**：`file_shares`、`message_reactions`、`comment_likes`、`comment_reports` 表中积累了不可见的行
2. **导致功能层面误判**：软删除的消息仍可能有 reaction 记录，软删除的文件仍显示为"已分享"
3. **浪费存储空间**：无上限的孤儿行累积

### 代码级证据

**证据 1：文件软删除不清除分享记录**

```go
// internal/file/service.go（Delete 方法）
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    actor, err := mustActor(ctx)
    // ... 权限验证 ...
    return s.files.Delete(ctx, id)  // 只软删 files 行
}
```

`file_shares` 表（`migrations/000006_create_file_shares.up.sql`）依赖 `file_id`，但没有任何代码在文件删除时级联清理分享行。

**证据 2：消息软删除不清除 Reactions**

```go
// internal/message/service.go（Delete 方法）
func (s *service) Delete(ctx context.Context, id domain.ID) error {
    actor, err := mustActor(ctx)
    // ... 权限验证 ...
    return s.messages.SoftDelete(ctx, id)  // 只软删 messages 行
}
```

`message_reactions` 表（`migrations/000013_message_reactions.up.sql`）仅通过消息 ID 关联，无级联删除。

**证据 3：评论软删除不清除 Likes 和 Reports**

```go
// internal/comment/service.go（Delete 方法）
func (s *commentService) Delete(ctx context.Context, id domain.ID) error {
    // ...
    return s.comments.Delete(ctx, id)  // 软删 comment 行，设置 deleted_at
}
```

`comment_likes`（`000005`）、`comment_reports`（`000007`）表中的行继续引用已被软删除的评论 ID。

**证据 4：没有后台清理任务来处理孤儿行**

```bash
$ grep -rn "file_shares\|message_reaction\|comment_like\|comment_report" internal/storage/gc/ --include='*.go'
# → 无输出
```

存储 GC（`internal/storage/gc/sweeper.go`）只处理文件 blob 的回收，不处理业务表层面的引用碎片。

**证据 5：SQL schema 无级联删除**

```sql
-- migrations/000006_create_file_shares.up.sql
CREATE TABLE IF NOT EXISTS file_shares (
    file_id  TEXT NOT NULL REFERENCES files(id),
    -- ...
);
-- 不包含 ON DELETE CASCADE
```

所有依赖表的外键都只是逻辑引用，没有 `ON DELETE CASCADE` 或任何级联语义。

### 为什么这是盲区

已有分析覆盖了"跨实体引用完整性"（方向一在 `2026-07-11-aero-id-architecture-next-frontiers.md`），但那篇文档关注的是**硬删除后孤儿实体**（如删除用户后其文件/消息的处理）——即用户数据生命周期管理。本文方向关注的是一种**完全不同的问题**：**软删除后依赖表的孤儿行累积**，这是存储效率 + 查询性能 + 功能正确性的综合退化问题，且与硬删除后的级联删除是正交的两个维度。

### 架构师视角

问题的本质是引用一致性的两个维度被混淆了：

| 维度 | 已有方案 | 遗漏 |
|------|---------|------|
| 用户删除后内容处理 | ❌ 未实现（SSO 不通知用户删除事件） | 跨实体 GC |
| **父行软删除后依赖行清理** | ❌ 未实现 | **本方向** |
| 物理 blob 无引用后删除 | ✅ Storage GC Sweeper | — |

修复方案：

1. **短路径**：在 service 层 Delete 方法中增加依赖清理（在同一个事务中软删除依赖行）
2. **长路径**：新增一个**引用碎片清理器**（类似 `storage/gc/sweeper.go`），定期扫描软删除父行并清理其依赖行

对于方案 1，需要权衡：软删除的目的是保留历史记录以供恢复。级联标记依赖行也为"软删除"状态（添加 `deleted_at` 列到依赖表）是最合理的方式——即依赖行继承父行的删除状态。

### 产品经理视角

用户行为→感知的路径：

- **用户删除一条消息** → 消息从对话中消失 ✓
- **但 Reactions 仍然存在** → 未读计数可能显示 error，UI 可能展示幽灵 reaction（不可见但占用位置）
- **用户删除一个文件** → 文件不可见 ✓
- **但分享记录仍然存在** → 接收方的共享列表可能显示"已删除的文件"或空状态
- **用户删除一条评论** → 评论隐藏 ✓
- **但点赞仍然存在** → 评论的点赞计数可能包括已删除评论的点赞

这些问题在用户规模小的 MVP 阶段可以忽略，但随着数据量增长会逐步显现。

### 边界情况

- **恢复场景**：软删除后恢复父行 → 依赖行应自动恢复（需要软删除 + 级联标记的机制）
- **硬删除 + 清理器**：超过保留期后的硬删除应确实清除所有依赖行
- **性能影响**：级联标记依赖行可能在大型表上产生锁竞争

### 建议优先级

**P2-中**（数据完整性+长期运维）。短期可接受但建议规划修复窗口。

---

## 方向四：Outbox 事件 + 幂等键集成缺陷——崩溃恢复后重复事件

### 问题陈述

aero-id 同时实现了两种写路径防护机制：
- **事务性 Outbox**：数据行和 side-effect 事件在同一个 DB 事务中提交
- **HTTP 幂等键**：通过 `Idempotency-Key` 头部防止客户端重试导致重复操作

这两个机制**各自独立工作**，但它们的**交互边界存在一个系统性漏洞**：当服务器在提交事务后、完成幂等键记录前崩溃，客户端重试将创建一个**重复的 outbox 事件**，导致接收方收到**重复的通知/Webhook 投递**。

### 代码级证据

**证据 1：幂等键在 handler 返回前预留，在 handler 返回后完成——存在"已提交未完成"窗口**

```go
// internal/middleware/idempotency.go:36-58
func Idempotency(store idempotency.Store, ttl time.Duration) gin.HandlerFunc {
    return func(c *gin.Context) {
        // ... 解析 key ...
        rec, reserved, err := store.Reserve(ctx, uid, endpoint, key)
        if reserved {
            cw := &captureWriter{...}
            c.Writer = cw
            c.Next()  // ← handler 在此执行并提交事务

            // ← 崩溃窗口开始
            if status := c.Writer.Status(); status >= 500 {
                _ = store.Release(ctx, uid, endpoint, key)
            } else {
                _ = store.Complete(ctx, uid, endpoint, key, ...)
            }
            // ← 崩溃窗口结束
        }
    }
}
```

**证据 2：Handler 中的写操作在一个事务中同时提交数据和 outbox 事件**

以 SendMessage 为例：

```go
// internal/message/service.go
func (s *service) createMessage(ctx context.Context, actor string, in SendMessageInput) (*domain.Message, error) {
    // ...
    evt := outbox.NewEvent("message", ...)
    if err := s.messages.Create(ctx, msg, evt); err != nil {
        // messages.Create 在一个事务中同时写入 messages 行和 outbox_events 行
    }
    return msg, nil
}
```

**证据 3：Outbox 事件没有反重放保护**

```go
// internal/outbox/repository.go（Append 方法）
func (r *GormRepository) Append(ctx context.Context, tx *gorm.DB, e *Event) error {
    row := eventToRow(e)
    return tx.Create(row).Error  // ← 直接 INSERT，无任何 ON CONFLICT
}
```

没有基于事件内容+聚合 ID 的唯一约束，同一操作的两个事件都会成功写入。

**证据 4：Domain 行有 ON CONFLICT 保护，outbox 事件没有**

对于某些域实体，Create 可能使用 `OnConflict`（例如幂等创建），但 outbox 事件总是`INSERT`。这意味着崩溃恢复后的重试不会产生重复域行（ON CONFLICT DO NOTHING 保护），但会**附加一个新的 outbox 事件**。

### 崩溃场景完整时序

```
时间线：
  1. 客户端 POST /v1/messages (Idempotency-Key: K)
  2. 中间件: Reserve(uid, "POST /v1/messages", K) → reserved=true
  3. Handler: BEGIN TX
  4. Handler: INSERT INTO messages (...)  ← 域行
  5. Handler: INSERT INTO outbox_events (event_type='message.sent', ...)  ← 事件
  6. Handler: COMMIT                      ← 所有数据已提交
  7. ★ 服务器崩溃 ★
  8. 客户端: 网络超时/连接断开，未收到 200 OK
  9. 客户端: 重试 POST /v1/messages (Idempotency-Key: K)
  10. 中间件: Reserve(uid, "POST /v1/messages", K)
      → 发现现有记录，status=0（预留中），但 expires_at 已过（2分钟 TTL）→ 回收预留 → reserved=true
  11. Handler: BEGIN TX
  12. Handler: INSERT INTO messages (...) ON CONFLICT DO NOTHING
      → 已存在，无操作（或更新无影响）
  13. Handler: INSERT INTO outbox_events (event_type='message.sent', ...)
      → ★ 第二个事件被写入！★
  14. Handler: COMMIT
  15. 中间件: Complete(...) → 200 OK
  16. Outbox relay 处理两个事件 → 接收方收到两条重复通知！
```

### 为什么这是盲区

既有分析已经覆盖了"幂等性"（方向 4 in ROADMAP）、"事务性 Outbox"（方向 4）、"Outbox relay at-least-once → exactly-once"（`2026-07-10-aero-id-uncovered-architectural-extension-directions.md` 方向 4）、以及"多实例并发安全"（`2026-07-11-deep-code-scan-concurrency-data-lifecycle-and-operational-gaps.md` 方向 1）。但**所有这些分析都将幂等性和 outbox 视为独立的防护层**，**没有文档分析它们之间的集成间隙**。这是一个系统性的、两个正确实现的组件交互时产生的边界行为。

| 已覆盖 | 未覆盖 |
|--------|--------|
| 幂等键防重放 ✓ | 幂等键 + Outbox 集成间隙 ✗ |
| Outbox 事务提交 ✓ | 崩溃恢复后重复事件 ✗ |
| Relay 级 at-least-once ✓ | 写路径级 at-most-once ✗ |

### 架构师视角

该问题的根因是**幂等键的承诺边界与 outbox 事件的提交边界不一致**：

- 幂等键承诺："给定的 (uid, endpoint, key) 对应的操作最多执行一次"——但承诺是在 HTTP 响应层面
- Outbox 事件承诺："与域数据在同一事务中提交，因此两者同时可见或同时不可见"——但可见不保证唯一

修复方式：

1. **建议方案：在 outbox 事件中包含 idempotency key**。在 `outbox_events` 表中增加 `idempotency_key TEXT` 列，并在 `(aggregate_type, aggregate_id, idempotency_key)` 上添加唯一约束。`Append` 使用 `ON CONFLICT DO NOTHING`，重试写入时静默跳过已存在的事件。这使 outbox 事件也成为幂等的。

2. **备选方案：将幂等键作为 aggregate_id 的一部分**。将幂等键编码到 outbox event 的 aggregate_id 中（如 `"msg:" + msg_id + ":" + idem_key`），利用已有的 `(aggregate_type, aggregate_id)` 唯一性检测重复。

方案 1 更清晰，但对已有 schema 有侵入性（需迁移）。方案 2 无需改 schema 但增加概念负担。

### 产品经理视角

用户不关心架构细节，但**重复通知的直接体验是破坏性的**：

- "消息已读"通知 × 2：发送方看到两条"XXX 已读你的消息"，困惑
- 评论回复通知 × 2：原评论作者收到两条相同的回复提醒，可能认为有人刷屏
- 文件分享通知 × 2：收件人看到两条"XXX 与你分享了文件 YYY"
- Webhook 消费者收到重复事件：下游系统（计费、通知推送、数据分析）可能产生重复操作

在大多数情况下这不是灾难性的——额外的通知是烦人而非危险。但对于 Webhook 消费者（如计费系统、自动化流程），重复事件可能产生实质性的业务影响。

### 边界情况

- **仅影响 POST 操作**：GET/DELETE/PUT 不产生 outbox 事件
- **仅当 Idempotency-Key 被使用时**：不使用幂等键的请求不受影响（它们是独立的）
- **窗口期 2 分钟**：预留 TTL 为 2 分钟，因此只有在 2 分钟内重试的客户端才会触发该问题
- **慢响应加重问题**：如果 handler 本身执行时间超过 2 分钟，尚未崩溃时客户端重试也会触发同等问题

### 建议优先级

**P1-高**（数据正确性）。这是已交付的两个正确功能之间的已知集成缺陷，在进入生产流量前值得修复。

---

## 总结与排期建议

| # | 方向 | 类型 | 优先级 | 范围 | 修复复杂度 |
|---|------|------|--------|------|-----------|
| 1 | SSE Hub 实例碎片化 | 产品体验 | P1 | `hub` + `events.go` + `app.go` | 中（Redis Pub/Sub 或 DB polling fallback） |
| 2 | Admin API 架构违例+功能碎片化 | 架构合规+功能完整 | P2 | `admin.go` + `service` + `grpc` | 中（提取 service + 新增端点） |
| 3 | 软删除引用碎片累积 | 数据完整性+运维 | P2 | 各 service Delete 方法 + 迁移 | 低-中（级联软清理或后台清理器） |
| 4 | Outbox 事件 + 幂等键集成间隙 | 数据正确性 | P1 | `outbox` + `middleware/idempotency` | 低（加唯一约束 + ON CONFLICT） |

### 实施建议

**Sprint 1（高优先级）**：方向 4（低风险、高价值）→ 方向 1（产品体验关键、需 infra 决策）

**Sprint 2（中优先级）**：方向 2（功能完整+架构合规）→ 方向 3（长期数据健康）
