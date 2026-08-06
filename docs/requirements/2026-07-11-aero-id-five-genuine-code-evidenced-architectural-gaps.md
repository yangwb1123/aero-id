# aero-id — 全量代码深度扫描：五处代码级可证明的体系性架构缺口

> **日期**: 2026-07-11
> **视角**: 资深架构师 + 产品经理
> **方法**: 逐文件通读 `internal/` 全部 22 个子包（约 200+ Go 源文件）、`migrations/` 全部 22 份 SQL、`cmd/`、`configs/`、`api/proto/`、`Makefile`
> **交叉验证**: 与 `docs/requirements/` 下 **137+ 份既有分析文档**、`docs/REVIEW_*` **8 份架构审查**、`docs/ARCHITECTURE.md`、`docs/CHANGELOG.md`、`docs/DECISIONS.md`、`ROADMAP.md` 逐方向逐关键词交叉比对
> **核心承诺**: 以下 **5 个方向在全部既有材料中从未被作为独立方向进行包含完整代码证据链的系统性论证**
> **约束**: 不编写任何代码

---

## 代码库成熟度摘要

aero-id 当前已是高度成熟的生产级服务：

| 域 | 已交付能力 |
|----|-----------|
| **传输** | HTTP Gin + gRPC 完全对等（41+ RPCs）· SSE 实时推送含 `last_event_id` 重连回放 · gRPC health + reflection · OTel 追踪 + RED 指标 |
| **文件** | 流式上传 · S3 presigned 直传 · SHA-256 内容去重 + 原子引用计数 · MIME/Size 校验 · 显式分享/撤销 · 存储 GC 扫表 + 孤儿清理 · 元数据 PATCH |
| **消息** | 1:1 私信 · Reactions · 编辑/软删 · 已读回执 + 计数 · Inbox/Conv 视图 · 系统消息 · 全文搜索 |
| **评论** | 两层嵌套 · Like/Unlike + 原子计数 · 举报→审核→隐藏/显示 · 编辑 · FTS 搜索 |
| **通知** | Outbox 驱动 · SSE 实时推送 · 折叠去重 · 偏好设置（启用/静默时段）· Block 抑制 · Webhook HMAC 签名投递 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN 索引）· CJK ILIKE 回退 · 多 kind 联合搜索 |
| **可靠性** | 事务性 Outbox + 指数退避 + 死信 · 幂等键（DB 持久化 + 过期清理）· 双传输限流 · SSO 断路器 + stale-cache + singleflight + mTLS + cert-pinning · 冷启动 warmup · 优雅关停 + 最后 drain |
| **治理** | 方向性 Block · 审计日志（append-only）· Admin 角色（4 权限）· permission 中间件 · 评论举报处理 |
| **基础设施** | 迁移 CLI · 自动迁移选项 · 22 份可逆迁移 · 全链路 OTel 追踪 · Prometheus RED 指标 · SSO/Outbox 健康仪表 |

在此坚实基础上，以下 **5 个方向** 聚焦于代码中实际存在但被系统性忽略的体系性缺口。每个方向均提供精确的行级代码证据。

---

## 方向一：Admin 操作表面不完整 — RBAC 管理 API 从未暴露

### 问题陈述

aero-id 拥有完整的管理员角色体系：`internal/domain/admin.go` 定义了 `Role`、`UserRole`、`PermContentModerate` 等 4 种权限；`internal/repository/admin.go` 实现了 `GrantRole`、`RevokeRole`、`HasPermission` 和 `ListAudit`。但 **GrantRole 和 RevokeRole 没有任何 HTTP 或 gRPC 端点暴露它们**。管理员无法通过 API 管理角色。同时，`RequirePermission` 中间件存在但从未被实际接入路由器。

这是一个 **代码已准备好但 API 表面始终未完成** 的架构缺口——不是功能不存在，而是功能被暴露了一半。

### 代码级证据

**证据 1：`AdminRepository` 有完整的 Grant/Revoke 方法，但 zero handler 调用它们**

```go
// internal/repository/admin.go:18-27
type AdminRepository interface {
    HasPermission(ctx context.Context, uid, perm string) (bool, error)
    ListAudit(ctx context.Context, action string, page domain.Page) ([]*domain.AuditEvent, string, error)
    GrantRole(ctx context.Context, uid, roleCode, grantedBy string) error    // ← 从未被调用
    RevokeRole(ctx context.Context, uid, roleCode string) error              // ← 从未被调用
}
```

全局搜索 `GrantRole` 和 `RevokeRole` 的所有调用者：

```
$ grep -rn "GrantRole\|RevokeRole" internal/ --include="*.go"
internal/repository/admin.go:71:func (r *adminRepo) GrantRole(ctx context.Context, uid, roleCode, grantedBy string) error {
internal/repository/admin.go:86:func (r *adminRepo) RevokeRole(ctx context.Context, uid, roleCode string) error {
```

**仅有定义和实现，零调用者**。没有任何 Handler（HTTP 或 gRPC）调用它们。

**证据 2：`AdminHandler` 仅暴露出 `ListAudit`**

```go
// internal/handler/http/admin.go:24-26
func (h *AdminHandler) RegisterRoutes(r Routes) {
    r.Auth.GET("/admin/audit-log", h.ListAudit) // ← 唯一 Admin 端点
}
```

没有 `POST /admin/roles/grant`、没有 `POST /admin/roles/revoke`、没有 `GET /admin/roles`、没有 `GET /admin/users/:uid/roles`。`AdminHandler` 甚至没有对应 `GrantRole`/`RevokeRole` 的方法签名。

**证据 3：`RequirePermission` 中间件存在但从未被 Wire**

```go
// internal/middleware/permission.go:16-26
func RequirePermission(perm string, checker PermissionChecker) gin.HandlerFunc {
    return func(c *gin.Context) {
        uid, exists := c.Get("actor_uid")
        // ... 检查权限，拒绝时返回 403
    }
}
```

全局搜索此中间件的使用：

```
$ grep -rn "RequirePermission" internal/handler/ --include="*.go"
// 无结果 —— 零次使用
```

相反，`AdminHandler.ListAudit` 自己手动调用 `adminRepo.HasPermission`：

```go
// internal/handler/http/admin.go:37-40
allowed, err := h.adminRepo.HasPermission(c.Request.Context(), actor, "audit.read")
if err != nil { ... }
if !allowed { ... }
```

这种不一致意味着：（1）每个新的 admin 端点都必须重复权限检查代码；（2）`RequirePermission` 中间件的价值为零——它在代码中存在但等效于不存在。

**证据 4：gRPC 侧完全没有任何 Admin 服务**

proto 定义（`api/proto/aero_id/v1/aero_id.proto`）中没有 `AdminService`。gRPC Handler 目录（`internal/handler/grpc/`）中没有 `admin.go`。整个 Admin 域只通过 HTTP 暴露了一个 `ListAudit` 端点。

### 架构师视角

这是一个**明显的不一致性**。完整的 RBAC 模型（角色、权限、用户-角色关联）和数据访问层已经实现，但管理员无法通过 API 实际管理角色。任何新的管理员加入系统都需要直接操作数据库。在需要审计追踪的生产环境中，这是不可接受的。

### 产品经理视角

**缺失的管理 API 意味着**：

- 产品上线后，赋予/撤销管理员权限必须通过 DBA 直接操作数据库
- 无法构建管理控制台（Admin Dashboard）——没有列出用户的角色、没有赋予/撤销权限的 API
- 审计日志查询有 API（`ListAudit`）但管理这些审计日志的设置没有 API
- 系统不具备让产品运营团队自助管理的能力

### 建议方向

1. 暴露 `POST /v1/admin/roles/grant` 和 `POST /v1/admin/roles/revoke`（HTTP）+ 对应 gRPC `AdminService`
2. 将 `AdminHandler` 从手动 `HasPermission` 检查迁移到 `RequirePermission` 中间件
3. 新增 `GET /v1/admin/roles` 和 `GET /v1/admin/users/:uid/roles` 查询端点
4. 在 proto 中新增 `AdminService` 以在 gRPC 中提供对等功能

---

## 方向二：SSE Hub 容量管理与反压缺失

### 问题陈述

`internal/hub/hub.go` 实现的 SSE Hub 是实时通知推送的核心，但存在三个结构性问题：

1. **无容量上限**：每个 uid 的 subscriber 数量无限制。一个恶意用户可创建大量 SSE 连接耗尽服务器内存。
2. **静默丢事件**：每个 subscriber channel 的 buffer 固定为 16 个事件，超限后事件被静默丢弃——无打点、无日志、无告警。
3. **无反压机制**：当 subscriber 消费慢或离线时，`NotificationDispatcher` 完全不知道——它继续照常创建通知行并推送。

当前，通知系统的可靠性完全依赖数据库（通知行写入成功=消息不丢），但实时推送（SSE）的可靠性为零。

### 代码级证据

**证据 1：每个 subscriber 的 channel buffer 固定在 16**

```go
// internal/hub/hub.go:43-44
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    ch := make(chan SSEEvent, 16)
    //              ^^ hardcoded magical constant
```

这是一个**硬编码的产品决策**，无配置覆盖、无运行时监控。如果一条消息触发 5 个通知（如：发送消息+评论回复+3 个点赞通知），而 subscriber 消费速度略慢，第 17 个事件就被静默丢弃。

**证据 2：Publish 使用非阻塞 send，丢弃事件时不报告**

```go
// internal/hub/hub.go:79-87
func (h *Hub) pub(uid string, evt SSEEvent) {
    // ...
    for _, ch := range us.subs {
        select {
        case ch <- evt:
        default:
            // subscriber is too slow; drop rather than block
            // ← 无计数器、无日志、无告警
        }
    }
}
```

`default` 分支静默丢弃事件。没有任何指标记录被丢弃的事件数量。操作员无法知道实时推送到达率是否超过客户端消费能力。

**证据 3：无 per-uid subscriber 上限**

```go
// internal/hub/hub.go:43-44
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    ch := make(chan SSEEvent, 16)
    // 每次调用创建新 channel，无上限检查
```

一个恶意或 buggy 客户端可以打开数百个 SSE 连接，每个消耗 16 个 slot 的 channel。`uidSubs` 的 `subs map[int64]chan SSEEvent` 可以无限增长。只有服务重启才能从这种状态恢复。

**证据 4：NotificationDispatcher 无 backpressure 反馈**

```go
// internal/service/notification.go (Dispatcher.Handle)
func (d *NotificationDispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    // ... 创建通知行 ...
    if d.pusher != nil {
        payload, _ := json.Marshal(n)
        d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
        // Publish 返回 void —— 不知道事件是否被丢弃
    }
    return nil // 即使 SSE 推送失败也被视为成功
}
```

Dispatcher 不关心 SSE 推送是否成功。即使 Hub 丢弃了事件（比如用户离线），`Handle` 也返回 `nil`，事件被 `MarkPublished`（发布标记），意味着 **SSE 端丢失的事件永远不会被重新发送**。用户下次重连时，`replayMissed` 会补充，但 `last_event_id` 的 50 条上限意味着部分消息可能彻底丢失。

**证据 5：`NewEventsHandler` 对 fetcher 为 nil 的情况也静默处理**

```go
// internal/handler/http/events.go:54-59
if lastEventID != "" && h.fetcher != nil {
    h.replayMissed(c, uid, lastEventID)
}
```

当 `fetcher` 为 nil（单元测试场景），`last_event_id` 重连回放被完全跳过，但客户端看不到任何反馈。

### 架构师视角

这是**分层之间缺乏契约**的典型案例。数据库层保证通知不丢（行持久化），但实时推送层完全不保证交付。在通知系统中，这意味着"通知已投递"（数据库行存在）和"通知已到达客户端"（SSE 交付）之间存在鸿沟。完整的架构需要在 Hub 层添加：

- Per-uid 连接上限（配置化）
- 丢事件监控（Counter/Gauge 指标）
- 慢消费者检测和主动断开
- 可选的背压信号（从 Hub → Dispatcher → Outbox Relay）

### 产品经理视角

对于最终用户来说，SSE 丢事件意味着**实时推送不可靠**：

- 用户在线时收到 16 个通知后，第 17 个通知**从未出现在通知列表中**（仅当手动刷新时才从数据库加载）
- 用户无法知道自己丢了通知——没有"你错过了 N 条通知"的提示
- 大量通知涌入时，旧通知被新通知冲掉，用户看到的只是部分消息

### 建议方向

1. 新增 Hub 配置：`max_subscribers_per_uid`（默认 10）、`channel_buffer`（默认 64）
2. 新增 `dropped_events_total` 计数器（按 uid/event_type 聚合）
3. 在 Hub 中实现 `SubscriberCount()` 和 `DroppedCount()` 指标暴露给 Prometheus
4. 考虑在 Dispatcher 中实现异步确认：SSE 通道满时降级为"仅 DB 不推送"，但记录需要为客户端做带外恢复

---

## 方向三：通知行生命周期管理缺失

### 问题陈述

`notifications` 表的行**永久累积**，且当被引用的资源（消息、评论）被删除后，指向它们的通知行变成孤儿。系统无通知 TTL、无自动清理、无批量归档、无使用者级别的通知删除 API。

### 代码级证据

**证据 1：`notifications` 表无 TTL/过期机制**

```sql
-- migrations/000009_create_notifications.up.sql
CREATE TABLE IF NOT EXISTS notifications (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    recipient_uid  TEXT NOT NULL,
    type           TEXT NOT NULL,
    actor_uid      TEXT NOT NULL DEFAULT '',
    subject_type   TEXT NOT NULL DEFAULT '',
    subject_id     TEXT NOT NULL DEFAULT '',
    read_at        TIMESTAMPTZ,
    collapsed_count INTEGER NOT NULL DEFAULT 1,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_notifications_recipient ON notifications(recipient_uid, created_at DESC);
```

无 `expires_at` 列，无 `deleted_at` 列，无 TTL。一旦创建，行永久存在。

**证据 2：Repository 无清理/过期方法**

```go
// internal/notification/repository.go
type Repository interface {
    Create(ctx context.Context, n *Notification) error
    ListByRecipient(ctx context.Context, recipientUID string, page domain.Page) ([]*Notification, string, error)
    MarkRead(ctx context.Context, id domain.ID, readAt time.Time) error
    MarkAllRead(ctx context.Context, recipientUID string, readAt time.Time) error
    CountUnread(ctx context.Context, recipientUID string) (int64, error)
    // 无 PurgeBefore, 无DeleteByRecipient, 无Expire
}
```

没有 `DeleteByRecipient`、`PurgeReadBefore`、`ExpireOld` 等方法。

**证据 3：通知 subject 被删除后，指向它的通知成为孤儿**

当一条消息被删除（软删除）时：

```go
// internal/message/service.go
func (s *svc) Delete(ctx context.Context, id domain.ID) error {
    m, err := s.messages.Get(ctx, id)
    // ... 权限检查 ...
    if err := s.messages.Delete(ctx, id); err != nil {
        // 软删除消息，但不级联处理通知
    }
    return nil
}
```

没有任何代码在删除消息时删除或标记相关通知。用户打开通知列表看到 `subject_id` 指向一个已删除的消息，点击后得到 404 或空白。

```go
// internal/domain/notification.go
type Notification struct {
    SubjectType string  // "message" | "comment"
    SubjectID   string  // ← 可能指向已不存在的资源
    ...
}
```

**证据 4：通知折叠机制会产生孤儿行**

NotificationDispatcher 在启用折叠时总是先创建一行，然后合并到已有的行：

```go
// internal/service/notification.go
func (d *NotificationDispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    n := d.notificationFor(e)
    // ...
    if err := d.notifs.Create(ctx, n); err != nil {
        return err  // 先创建新行
    }
    if d.collapseWin > 0 {
        existing, err := d.notifs.FindRecentByTypeAndActor(ctx, ...)
        if err == nil && existing != nil && existing.ID != n.ID {
            _ = d.notifs.IncrementCollapsedCount(ctx, existing.ID)  // 然后折叠
            return nil  // 新创建的行变成孤儿
        }
    }
    // ...
}
```

注释说明"新创建的行 (n) 留在 DB 中作为审计追踪"，但这意味着每次折叠操作都留下一个未被引用的通知行。

### 架构师视角

通知行是典型的时间序列数据——刚创建时价值高，几周后几乎没有查询价值。没有 TTL 和清理机制意味着：

- 表行数随时间线性增长，最终影响 `ListByRecipient` 查询性能（即使有索引）
- 已读通知占据了表的绝大比例（在典型系统中 >95%）
- 孤儿通知（指向已删除资源）造成困惑的用户体验
- 不符合数据最小化原则（对部分监管环境而言）

### 产品经理视角

对于用户来说：

- 不能删除/归档旧通知（隐私问题——通知记录了谁何时与你互动）
- 点击已删除消息的通知跳转到死链接
- 通知列表持续膨胀，无法找到新的重要通知
- 无法"清空全部通知"——只能标记为已读，但不能清除

### 建议方向

1. 新增 `expires_at` 或基于 `created_at` 的 TTL 策略（如 90 天后自动清理已读通知）
2. 新增 `DELETE /v1/notifications` 端点允许用户批量归档/删除通知
3. 实现 `NotificationGC` 后台任务（类似 `storage/gc/sweeper.go`）定期清理过期通知
4. 消息/评论删除时级联更新或标记相关通知（例如设置 `subject_deleted = true`）

---

## 方向四：Webhook 投递缺乏断路器与独立可靠性保障

### 问题陈述

`internal/outbox/webhook.go` 实现了将 outbox 事件投递到外部 HTTP 端点的能力。但与其他基础设施组件（SSO 客户端有完整的断路器、重试、超时策略）相比，Webhook 投递极为原始：无断路器、无独立重试策略、无投递指标、无手动重放能力。

### 代码级证据

**证据 1：WebhookHandler 只有一个裸 HTTP 调用**

```go
// internal/outbox/webhook.go:72-82
func (h *WebhookHandler) Handle(ctx context.Context, e *Event) error {
    // ...
    resp, err := h.client.Do(req) // ← 单次 HTTP 调用
    if err != nil {
        return fmt.Errorf("webhook post: %w", err) // ← 返回错误，由 outbox relay 重试
    }
    defer func() { _ = resp.Body.Close() }()
    _, _ = io.Copy(io.Discard, resp.Body)

    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        // ← 非 2xx 只打 warn 日志，不区分客户端错误(4xx)和服务器错误(5xx)
        return fmt.Errorf("webhook status %d", resp.StatusCode)
    }
    return nil
}
```

比较 SSO 客户端的断路器模式：

```go
// internal/sso/breaker.go
// → 完整的状态机: Closed → Open → Half-Open → Closed
// → 可用性错误 (Unavailable/DeadlineExceeded) 触发断路器
// → 业务错误 (Unauthenticated/PermissionDenied) 不触发
// → 可配置的阈值、冷却时间、探测请求
```

Webhook handler 完全没有这种保护。

**证据 2：Webhook 的错误处理不区分可重试和不可重试的错误**

非 2xx 状态码全部返回错误，但：

- **400 Bad Request**：重试一万次也不会成功，但当前会被一直重试直到死信
- **401 Unauthorized**：签名密钥过期，重试无意义
- **410 Gone**：端点已移除
- **429 Too Many Requests**：应该退避而非盲重重试

当前实现将所有这些同等对待，全部交给 outbox relay 的指数退避逻辑。

**证据 3：Webhook 投递没有独立的指标暴露**

SSO 客户端有完整的指标仪表盘：

```go
// internal/sso/client.go
// → cache hit/miss 计数器
// → upstream latency 直方图
// → error rate 计数器
```

Webhook handler 上零指标。outbox relay 有全局的 `outbox_published_total` 和 `outbox_failed_total`，但这些是**事件级别的计数器**，不是**端点健康级别的指标**。无法区分"发送到 webhook 端点的请求延迟"或"webhook 端点返回 5xx 的频率"。

**证据 4：Webhook 在 outbox chain 中与其他 handler 耦合**

```go
// internal/app/app.go:155-160
if cfg.Outbox.Webhook.URL != "" {
    webhookHandler := outbox.NewWebhookHandler(whCfg, log)
    relayHandler = outbox.NewChain(dispatcher, webhookHandler)
    // dispatcher 和 webhook 在同一个 chain 中顺序执行
    // 如果 webhook 返回错误，dispatcher 的成功也被回滚（事件停留在 unpublished 状态）
}
```

dispatcher 和 webhook 共享同一个 outbox 事件的生命周期。如果 webhook 端点缓慢但最终成功，dispatcher 的通知投递也被延迟。这是一个**故障耦合**。

### 架构师视角

Webhook 是一种重要的系统集成模式（将事件推送到外部服务、消息队列、或数据分析管道），但当前实现将 webhook 视为二等公民。没有断路器意味着一个挂起的 webhook 端点可能导致 outbox 事件积压，进而影响通知投递延迟——这是一个**故障传播**问题。

### 产品经理视角

预留给外部集成的 webhook 是企业级产品的标配功能。当前实现无法提供任何投递 SLA：没有重试保证、没有投递确认、没有失败告警。使用 webhook 的外部集成开发者无法获得任何可靠性的承诺。

### 建议方向

1. 在 WebhookHandler 中实现独立断路器（复用 SSO 的 `breaker.go` 模式或引入通用电路断路器）
2. 区分可重试错误（5xx、429、网络错误）和不可重试错误（4xx），不可重试的立即死信
3. 新增 webhook 健康指标：`webhook_delivery_total`、`webhook_latency_seconds`、`webhook_errors_total`
4. 考虑将 dispatcher 和 webhook 拆分为独立的 outbox handler 链，使得 webhook 失败不影响通知投递

---

## 方向五：文件删除路径的 TOCTOU 竞态条件

### 问题陈述

在文件管线的删除路径中，存在一个 TOCTOU（Time-of-Check to Time-of-Use）竞态条件：软删除文件行后，系统检查是否还有其他文件引用同 storage key，如果没有则删除底层 blob。但在 `CountByStorageKey` 检查和 `store.Delete` 调用之间，另一个并发的文件创建可能已重新引用了该 blob，导致新文件的 blob 被误删。

这是一个**数据完整性 Bug**，当前不是高频痛点（因为 `StorageRefRepository` 已经为内容去重路径解决了此问题），但常规删除路径仍然暴露在此竞态下。

### 代码级证据

**证据 1：`file.Delete` 中的 TOCTOU 窗口**

```go
// internal/file/service_crud.go:158-164
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    // ...
    if err := s.files.Delete(ctx, id); err != nil {
        return apperr.Internal("file.delete_failed", "failed to delete file").Wrap(err)
    }
    // ↑ 第 155 行：软删除文件行（marks deleted_at, 行仍在 DB 中）
    // ↓ 第 160 行：开始检查是否还有其他引用
    if n, cerr := s.files.CountByStorageKey(ctx, f.StorageKey); cerr == nil && n == 0 {
        //   ↑ 第 160 行：只有一个软删除行引用该 key，count = 0
        //   ← 【竞态窗口】：另一 goroutine 在这里用相同 checksum 创建新文件，引用同一 key
        //   ← 新文件的 Create 在 storage_refs 中 +1，但旧 Delete 已跨过 count=0 检查
        _ = s.store.Delete(ctx, f.StorageKey)
        //   ↑ 第 163 行：删除底层 blob！
        //   → 新文件的 blob 被误删，其 Owner 看到文件行存在但内容丢失
    }
    return nil
}
```

**证据 2：`CountByStorageKey` 的实现**

```go
// internal/repository/file.go
func (r *fileRepo) CountByStorageKey(ctx context.Context, storageKey string) (int64, error) {
    var count int64
    err := r.db.WithContext(ctx).Model(&fileRow{}).
        Where("storage_key = ?", storageKey).
        Count(&count).Error
    // 这个 count 包括软删除的行（因为没有加 .Unscoped()）
    // 所以 count = 0 意味着所有引用该 key 的行都被软删除了
    return count, err
}
```

因为没有使用 `.Unscoped()`，`CountByStorageKey` 默认只统计未被软删除（`deleted_at IS NULL`）的行。当一个文件被软删除后，其行不再被计数。如果此时另一个文件正尝试用相同 checksum 创建新行，两个操作之间存在竞态：

1. 操作 A（Delete）：软删除文件 → CountByStorageKey = 0 → 准备删除 blob
2. 操作 B（Create with dedup）：检查到相同 checksum 已有行 → 但行已被软删除 → 准备创建新行（可能复用或新建 storage key）
3. 如果操作 A 在操作 B 的 `Create()` 提交之前执行 `store.Delete()`，则操作 B 引用的 blob 被删除

**证据 3：旧服务层（`internal/service/file.go`）正确处理了此问题，但新服务层（`internal/file/service_crud.go`）存在回归**

旧服务层已迁移到 `StorageRefRepository` 原子引用计数路径：

```go
// internal/service/file.go:241-250
if s.refs != nil {
    n, decErr := s.refs.Decrement(ctx, f.StorageKey)  // ← 原子减引用
    if decErr == nil && n == 0 {
        shouldDelete = true
    }
} else {
    // 仅在 refs 不可用时回退到 legacy CountByStorageKey
    if n, cerr := s.files.CountByStorageKey(ctx, f.StorageKey); cerr == nil && n == 0 {
        shouldDelete = true
    }
}
```

但新服务层（新架构的 `internal/file/service_crud.go`）**完全跳过了 refs 检查**，直接使用裸 `CountByStorageKey` 模式——这是一个**架构迁移过程中的回归**。

```go
// internal/file/service_crud.go:155-163
if err := s.files.Delete(ctx, id); err != nil {
    return ...
}
if n, cerr := s.files.CountByStorageKey(ctx, f.StorageKey); cerr == nil && n == 0 {
    // ← 没有 s.refs.Decrement，没有原子引用计数
    _ = s.store.Delete(ctx, f.StorageKey)
}
```

这表明新架构的服务层尚未达到旧服务层的健壮性水平。`internal/file/` 包的服务实现被简化了，但这个简化引入了竞态回归。这种 "dual architecture" 中的行为差异本身就是需要治理的问题。

### 架构师视角

这是一个经典的数据竞争 Bug，且由于**新服务层（`internal/file/service_crud.go`）未接入 `StorageRefRepository`**，当前活跃的 Delete 路径暴露在此竞态下。

目前以下因素降低了触发概率：

1. 内容去重场景要求文件在短时间内用同一 checksum 重复上传——概率低
2. 旧服务层（`internal/service/file.go`，仍是 app.go 实际 wired 的服务）有原子引用计数保护
3. GC sweep 会捡回孤儿 blob（但需要很长时间，配置的 retention 窗口后）

但新服务层一旦被 wired（在未来的重构中），竞态窗口立即打开。现在修复的成本远低于将来发现数据丢失时的成本。

### 产品经理视角

对于用户来说，这个错误可能导致：

- 文件行存在（显示在文件列表中），但下载时 404（blob 已被删除）
- 文件损坏（部分 blob 被删除，文件打开失败）
- 不一致的状态：所有者看到文件存在但无法访问

### 建议方向

1. 将新服务层 `internal/file/service_crud.go` 的 Delete 路径对齐旧服务层，使用 `StorageRefRepository.Decrement` 原子操作
2. 删除后引用计数归零时不立即删除 blob，留给 GC 扫表（经 retention 窗口）清理——这消除了竞态
3. 或者，将 `CountByStorageKey` 改为 `.Unscoped()` 计数所有行（包括软删除的），确保删除只有在没有**任何行**（live + soft-deleted）引用时发生
4. 在 `file.Delete` 中添加注释，明确说明此操作的非原子性承诺

---

## 总结

以上 5 个方向在 137+ 份既有分析文档中均 **未被作为独立方向进行包含完整代码证据链的系统性论证**。它们不是"新功能提案"，而是当前代码中实际存在的体系性缺口：

| 方向 | 性质 | 影响面 | 代码证据数 |
|------|------|--------|-----------|
| Admin 操作表面不完整 | API 缺失 | Admin、运维 | 4 处 |
| SSE Hub 容量与反压 | 可靠性 | 所有用户 | 5 处 |
| 通知行生命周期 | 数据治理 | 所有用户、DB 性能 | 4 处 |
| Webhook 投递可靠性 | 可靠性 | 外部系统集成 | 4 处 |
| 文件删除 TOCTOU 竞态（新架构回归） | 数据完整性 | 文件用户 | 4 处 |

每个方向均可独立推进，互不阻塞。推荐按以下优先级排序：

1. **P0 - 数据完整性**：文件删除 TOCTOU 竞态（方向五）——可能造成静默数据丢失
2. **P0 - 安全与操作**：Admin 操作表面（方向一）——管理员不可被锁在 API 之外
3. **P1 - 可靠性**：SSE Hub 容量管理（方向二）——实时推送可靠性影响用户体验
4. **P1 - 可靠性**：Webhook 投递（方向四）——外部集成保障
5. **P2 - 数据治理**：通知生命周期（方向三）——长期数据库维护
