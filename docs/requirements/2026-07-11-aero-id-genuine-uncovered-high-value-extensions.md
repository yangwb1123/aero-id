# aero-id — 全局代码扫描：五个未覆盖的高价值扩展方向

> **日期:** 2026-07-11
> **方法:** 全量逐文件扫描 cmd/ · internal/ · api/proto/ · migrations/ · configs/ · Makefile
> **视角:** 资深架构师 + 产品经理
> **约束:** 不编写任何代码。每个方向提供精确的代码级证据。与 docs/requirements/ 下既有 ~75 份分析文档逐方向交叉比对，**确保这五个方向在既有材料中未被系统性覆盖**。

---

## 代码库现状

aero-id 已经是一个高度成熟的双传输数据服务平台。以下能力均已交付（代码级可验证）：

| 域 | 已交付能力 | 证据位置 |
|---|---|---|
| **基础设施** | HTTP (Gin) + gRPC 全对等（41 RPCs）、GORM + PG16、本地/S3 双存储、OTel 追踪、RED 指标、per-dependency `/readyz`、冷启动 warmup、优雅关停、`cmd/migrate` CLI + 可选 auto_migrate | `internal/app/app.go`, `internal/handler/http/`, `internal/handler/grpc/`, `internal/pkg/tracing/`, `internal/pkg/metrics/`, `cmd/migrate/` |
| **可靠性** | 事务性 Outbox + 指数退避 + 死信 + Webhook HMAC 投递、幂等键、速率限制（per-actor per-IP）、SSO 断路器 + stale-cache + singleflight + 重试 + keepalive + mTLS + 证书锁定、存储 GC + 孤儿扫描 + 引用计数 | `internal/outbox/`, `internal/idempotency/`, `internal/sso/`, `internal/storage/gc/`, `internal/pkg/ratelimit/` |
| **文件管线** | CRUD、流式上传、MIME/size 校验、SHA-256 去重（per-owner）、Presigned 直传 S3、显示分享/撤销、元数据 PATCH、Storage Refs 引用计数 | `internal/file/`, `internal/service/file*.go`, `internal/storage/` |
| **消息** | 1:1 私信 + 图文/文件/系统消息、Reactions（React/Unreact 双向、原子、去重）、已读回执 + 计数、编辑/软删、Inbox/Conv 视图、Block 自动拦截 | `internal/message/`, `internal/service/message.go` |
| **评论** | 两层嵌套（楼中楼）、Like/Unlike（原子计数器）、编辑、软删、举报→审核→隐藏/显示、作者作用域报告管理 | `internal/comment/`, `internal/service/comment.go` |
| **通知** | Outbox 驱动的异步创建 + 折叠去重 + 速率限制、SSE 实时推送（last_event_id 重连回放）、偏好设置（启用/静默时段/时区）、Block 抑制、Webhook 投递 | `internal/notification/`, `internal/hub/`, `internal/service/notification.go` |
| **搜索** | 跨实体 FTS（PG tsvector + GIN 索引 + CJK ILIKE 回退）、多 kind（消息/评论/文件）联合搜索，按 owner_uid 隔离 | `internal/search/`, `migrations/000016_fts_indexes.up.sql` |
| **安全管理** | 方向性 Block（inbound only）、审计日志（Append-only，fire-and-forget）、Admin 角色 + 权限（content.moderate / audit.read / user.ban / system.config）、permission 中间件 | `internal/block/`, `internal/audit/`, `internal/domain/admin.go`, `internal/middleware/permission.go` |
| **运维** | gRPC health + reflection、23 次可逆迁移（含性能索引、FTS、通知折叠、Outbox 重试、Admin roles、Storage refs）、Makefile targets 完整 | `internal/handler/grpc/ops.go`, `migrations/`, `Makefile` |

---

## 方向一：Admin 控制面缺失 gRPC 对等层 — 数据中心 ops 只有 HTTP

### 问题

所有数据面（data-plane）操作都实现了 HTTP ↔ gRPC 完全对等（41 RPCs, 6 services），但**管理面（control-plane）操作完全停留在 HTTP-only**。这意味着依赖 gRPC 的内部运维工具（istio、k8s operators、内部 CLI）无法以统一协议管理 aero-id 集群。

### 代码级证据

**证据 1：`internal/handler/http/admin.go` 存在，但 `internal/handler/grpc/` 下无 `admin.go`**

```go
// internal/handler/http/admin.go — 注册了 HTTP 路由
func (h *AdminHandler) RegisterRoutes(r Routes) {
    r.Auth.GET("/admin/audit-log", h.ListAudit)
}
```

而 `internal/handler/grpc/` 中没有任何 admin 相关 server 实现。`internal/handler/grpc/ops.go` 只包含健康检查和 reflection，不包含 admin RPC。

**证据 2：gRPC proto 文件中没有 AdminService**

```protobuf
// api/proto/aero_id/v1/aero_id.proto — 6 个 service，无 AdminService
service FileService { ... }
service MessageService { ... }
service CommentService { ... }
service NotificationService { ... }
service BlockService { ... }
service SearchService { ... }
service PreferenceService { ... }
```

而 HTTP handler 暴露了 admin 端点。

**证据 3：`domain/admin.go` 定义了 `PermUserBan` 权限，但没有任何端点实现用户封禁**

```go
// internal/domain/admin.go:12
const (
    PermContentModerate = "content.moderate"
    PermAuditRead       = "audit.read"
    PermUserBan         = "user.ban"       // ← 已定义的权限
    PermSystemConfig    = "system.config"
)
```

`internal/handler/http/admin.go` 仅实现了 `ListAudit`（审计日志查询）。**用户封禁（ban/unban）完全没有 API 端点**——尽管权限常量、角色表（`user_roles`）、迁移（`000022_admin_roles`）均已就绪。

**证据 4：`outbox.Repository` 有死信事件标记，但无运维 API**

```go
// internal/outbox/outbox.go — Repository 接口
type Repository interface {
    Append(ctx, tx, e) error
    FetchUnpublished(ctx, limit) ([]*Event, error)
    MarkPublished(ctx, ids) error
    MarkFailed(ctx, id, retryCount, maxRetries, at) error  // ← 死信标记
    // 缺少:
    //   FetchDeadLetters(ctx, page) ([]*Event, error)
    //   ReplayEvent(ctx, id) error
    //   PurgeDeadLetters(ctx, before) error
}
```

运维人员只能通过直接查数据库看到死信事件。无 API 可以查询、重放或清理。

### 产品经理视角

- **运维可观测性缺口**：当 webhook 端点故障或通知处理出错时，事件沉默地进入死信。没有 API 让运维人员查询或重放失败事件，只能直接操作数据库。
- **gRPC 的承诺是"统一协议"**：gRPC 健康检查已经集成到 k8s 探活机制，但 admin RPC 缺失意味着内部 toolchain 仍需同时维护 HTTP 和 gRPC 两种客户端。
- **用户封禁（ban）是内容平台的产品标配**：权限系统已预留 `user.ban`，但没有任何端点允许管理员封禁恶意用户——只能通过 `internal/repository/admin.go` 的 repo 层写数据库。

### 建议

1. **proto 中新增 AdminService**（3-4 个 RPC）
   - `ListAuditEvents(action, actor_uid, page)` → 查询全量审计日志
   - `UserBan(uid, reason, duration)` / `UserUnban(uid)`
   - `ListDeadLetters(page)` → 查询死信事件
   - `ReplayDeadLetter(event_id)` → 重置死信事件进入重试队列
2. **HTTP 端点保持同步扩展**（REST 版本的同一套接口）
3. **用户封禁在 service 层的实现**：ban 不影响现有数据，但在 `Auth` 中间件检查 uid 是否在 ban 列表中；被封禁用户的所有写请求返回 `Forbidden`
4. **死信查询利用已有仓库层能力**：`outbox.Repository` 已标记 `dead_letter = true`，只需新增 `Where("dead_letter = true")` 查询方法

### 边界情况

- **临时封禁 vs 永久封禁**：`duration` 为 0 表示永久；到期后自动解封需要后台 sweeper（可使用 GC 同一调度模式）
- **封禁通知**：被封禁用户在下一次请求时收到 Forbidden 即可，不需要主动通知
- **管理员审计**：封禁和解封操作本身应写入审计日志（`audit_events` 表）——利用已有的 `AuditLogger` 接口
- **死信重放后再次失败**：允许多次重放，每次走标准退避流程

---

## 方向二：分布式速率限制缺失 — 水平扩展后门洞开

### 问题

当前速率限制器是**每实例（per-instance）内存实现**。当部署 N 个 aero-id 实例时，每个实例独立允许 `N × rps` 的请求量。配置的限流阈值在水平扩展时线性稀释。

### 代码级证据

**证据 1：`internal/pkg/ratelimit/ratelimit.go` 文档明确声明 "per-instance"**

```go
// Package ratelimit provides a small, dependency-free, concurrency-safe
// per-key token-bucket limiter. It is used by the HTTP middleware and the gRPC
// interceptor to throttle requests per authenticated actor uid (falling back to
// client IP for unauthenticated traffic).
//
// The limiter is PER-INSTANCE: each running aero-id process keeps its own
// buckets in memory. Distributed/global rate limiting would need shared state
// (e.g. Redis) and is intentionally out of scope here.
```

**证据 2：`internal/middleware/ratelimit.go` 和 `internal/handler/grpc/server.go` 使用同一 per-instance limiter**

```go
// internal/app/app.go:255
limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
```

这个 limiter 被同时传给 HTTP router 和 gRPC server。多实例部署下，每个实例各自独立限流。

**证据 3：配置文件中没有任何 Redis/共享状态的相关项**

```yaml
# configs/config.yaml — rate_limit 段
rate_limit:
  enabled: false
  rps: 20
  burst: 40
  idle_ttl: 10m
  cleanup_every: 1m
  # 没有: redis_addr, redis_password, global_mode, sync_interval 等
```

### 产品经理视角

- **生产级风险**：假设配置 `rps: 100`，部署 5 个实例。实际系统允许的 RPS 是 `5 × 100 = 500`——**限流完全失效**。恶意用户可以轮流攻击不同实例的 IP 来绕过限流。
- **水平扩展时必须考虑的问题**：当前项目没有 Kubernetes HPA 或多实例部署指南，但这是一个迟早要面对的场景。分布式限流是不可跳过的基础设施。
- **SSO 客户端已经展示了正确的模式**：SSO 客户端有完整的断路器、stale-cache、重试机制——说明了项目对可靠性的重视。速率限制在这方面是明显的空缺。

### 建议

1. **定义 `LimiterBackend` 接口**，分离 token-bucket 算法实现与状态存储

   ```go
   // 新增 internal/pkg/ratelimit/backend.go
   type Backend interface {
       Allow(ctx context.Context, key string, rate, burst float64) (bool, time.Duration, error)
       Close() error
   }
   ```

2. **保留 `MemoryBackend`**（当前实现）作为默认（零依赖、单实例模式）
3. **新增 `RedisBackend`**（基于 Redis Sliding Window 或 Token Bucket via EVAL/EVALSHA）
4. **配置选项**：`rate_limit.backend: "memory" | "redis"`，redis 后端时增加 `rate_limit.redis_addr` 等
5. **hint: 不必 perfect**：即使是 Redis 后端也存在时钟偏差和网络延迟问题，但一致性远高于每实例独立限流

### 边界情况

- **Redis 不可用时的降级**：允许局部降级（返回 `allow=true` + 日志告警）或拒绝所有请求（fail-close）——需可配置
- **key 爆炸**：每 uid 一个 key → 大量短生命周期 token 会膨胀 Redis 内存。需设置 `EXPIRE` TTL 与 idleTTL 对齐
- **迁移成本**：MemoryBackend 和 RedisBackend 可以在同一部署中共存（通过接口抽象），不影响现有 HTTP/gRPC 中间件

---

## 方向三：会话生命周期管理缺失 — 消息只有"发和删"，没有"组织和控制"

### 问题

1:1 私信系统实现了发送、读取、编辑、删除、已读回执、reaction、block 拦截，但缺少用户对**会话列表本身**的控制。当前会话是通过 `(from_uid, to_uid)` 对隐式重建的，没有会话实体（conversation entity）。

- 用户无法静音（mute）一个会话——只能 block 对方
- 用户无法归档（archive）一个会话——对方发新消息又出现了
- 用户无法置顶（pin）一个会话——每次从头找
- 用户无法看到对方的"正在输入"（typing indicator）
- 用户无法查看会话中的共享文件

### 代码级证据

**证据 1：`messages` 表没有 `conversation_id` 列——会话是隐式的**

```sql
-- migrations/000002_create_messages.up.sql
CREATE TABLE IF NOT EXISTS messages (
    id         UUID PRIMARY KEY,
    from_uid   TEXT NOT NULL,
    to_uid     TEXT NOT NULL,
    kind       SMALLINT NOT NULL DEFAULT 1,
    content    TEXT NOT NULL DEFAULT '',
    file_id    UUID,
    read_at    TIMESTAMPTZ,
    ...
    -- 没有 conversation_id 列
);
```

会话通过 `Conversation(ctx, withUID)` service 方法隐式重建——查询两 uid 之间的所有消息。

**证据 2：`inbox`（收件箱）视图是最近消息列表，用户不能控制**

```go
// internal/message/repository.go — Inbox 方法
// 返回按 created_at DESC 分组的最新消息，每组 (from_uid, to_uid) 取最新一条
// 没有 mute_flag、archived_at、pinned_at 过滤
```

**证据 3：`internal/service/message.go` 没有 mute、archive、pin 等方法**

```go
// internal/service/service.go
type MessageService interface {
    Send(...)    error
    Get(...)     (*Message, ...)
    Inbox(...)   (*InboxResult, error)
    Conversation(...) (*ConversationResult, error)
    MarkRead(...) error
    UnreadCount(...) (int64, error)
    Edit(...)    error
    Delete(...)  error
    MarkConversationRead(...) error
    React(...)   error
    Unreact(...) error
    // 缺少:
    //   Mute(ctx, withUID string) error
    //   Unmute(ctx, withUID string) error
    //   Archive(ctx, withUID string) error
    //   Pin(ctx, withUID string) error
    //   Unpin(ctx, withUID string) error
    //   Typing(ctx, toUID string) error
}
```

**证据 4：没有任何 typed 消息用于 typing indicator**

`messages.kind` 已有 `MessageKindSystem = 4`，但没有实时 typing indicator 机制。Typing 事件本可以通过 WebSocket/SSE 推送——SSE hub (`internal/hub/hub.go`) 已具备实时推送能力，但 `hub.Pusher.Publish` 接口只被 `notification/dispatcher` 使用。

### 产品经理视角

- **竞品对标**：Telegram、WhatsApp、微信都支持 mute/archive/pin/typing。这些不是"锦上添花"而是"基础体验"。
- **Blocking 不应是 Mute 的唯一选择**：当前用户只有两种选择来管理不需要的对话——继续接收或 block 对方。Mute 提供了中间状态："不打扰我，但我保留聊天记录"。
- **Typing indicator 是即时通讯的黄金功能**：当前 SSE 基础设施已就绪——只需新增一个轻量的事件类型。
- **实现成本低**：
  - Mute/Archive/Pin 只需要 `conversation_settings` 表（`uid1`, `uid2`, `muted`, `archived_at`, `pinned_at`）
  - Typing indicator 只需要一个 SSE 事件类型和一条 `Publish` 调用
  - Inbox 查询增加 WHERE 过滤条件

### 建议

1. **新增 `conversation_settings` 表**（或扩展现有表）
   ```sql
   CREATE TABLE conversation_settings (
       uid1       TEXT NOT NULL,  -- lexicographically smaller
       uid2       TEXT NOT NULL,  -- lexicographically larger
       muted_uid  TEXT,            -- which uid muted; null = neither
       archived_at TIMESTAMPTZ,    -- per-uid archive (store two rows or JSON)
       pinned_at   TIMESTAMPTZ,
       PRIMARY KEY (uid1, uid2)
   );
   ```
2. **Inbox 查询增加过滤**
   - `muted`：仍在收件箱中但标记为灰色
   - `archived`：不在收件箱，但可在归档列表查看
   - `pinned`：置顶到收件箱顶部（不按时间序）
3. **Typing indicator 通过 SSE 推送**
   - 客户端调用 `POST /v1/messages/typing`（body: `{to_uid: "..."}`）
   - 服务层通过 `hub.Pusher.Publish(toUID, "typing", fromUID)` 推送
   - SSE 客户端接收 `event: typing`，显示"对方正在输入..."
   - 客户端应节流（throttle）发送，避免每秒多次推送
4. **Mute 不等于 Block**：Block 阻止消息送达（`internal/service/message.go:Send` 中已有 `IsBlocked` 检查），而 Mute 只影响通知——消息仍然送达。

### 边界情况

- **两人互相 mute**：mute 是方向性的，A mute B 不影响 B 看到 A 的消息
- **Mute 后消息仍然入 inbox**：消息继续显示，只是不产生通知。需要 `internal/notification/dispatcher.go` 中的 mute 检查（在推送前过滤）
- **archive 后的新消息**：只要对方发了新消息，会话自动从归档恢复
- **pin 的数量限制**：建议最多 5 个 pin，超出返回错误
- **typing 事件的时效性**：typing event 是"瞬时的"，不需要持久化。发送后 3 秒无新 typing → 自动停止显示

---

## 方向四：内容生命周期政策缺失 — TTL、自动过期、合规销毁、数据导出

### 问题

当前系统支持软删除和基于 retention 的存储 GC（仅作用于已软删除的文件行），但没有任何**内容生命周期管理（Content Lifecycle Management, CLM）**能力：
- 用户不能设置消息或文件的 TTL（"阅后即焚"）
- 系统不能按全局政策自动过期旧内容（合规要求）
- 没有 GDPR 数据导出 endpoint（用户需要索要自己的所有数据）
- 没有批量数据清除（"删除我所有内容"）

### 代码级证据

**证据 1：`domain.File` 和 `domain.Message` 都没有 `expires_at` 字段**

```go
// internal/domain/file.go
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64
    MimeType    string
    StorageKey  string
    StorageType string
    Checksum    string
    Visibility  Visibility
    Meta        map[string]any
    CreatedAt   time.Time
    UpdatedAt   time.Time
    // 没有 ExpiresAt *time.Time
}
```

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    Kind      MessageKind
    Content   string
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any
    CreatedAt time.Time
    // 没有 ExpiresAt *time.Time
}
```

**证据 2：`storage/gc/sweeper.go` 只处理软删除的文件行，不处理未删除但有 TTL 的行**

```go
// internal/storage/gc/sweeper.go — PurgeDeleted 方法
// 条件: deleted_at IS NOT NULL AND deleted_at < cutoff
// 不是: expires_at IS NOT NULL AND expires_at < cutoff
```

**证据 3：没有任何 user data export 端点**

```bash
# 所有 HTTP handler 列表
internal/handler/http/
├── admin.go        # /admin/audit-log
├── block.go        # /v1/blocks
├── comment.go      # /v1/comments/...
├── events.go       # /v1/events (SSE)
├── file.go         # /v1/files/...
├── health.go       # /healthz, /readyz
├── message.go      # /v1/messages/...
├── notification.go # /v1/notifications/...
├── preference.go   # /v1/notification-preferences
├── search.go       # /v1/search
# 没有: /v1/export, /v1/data-export, /v1/account/export
```

**证据 4：没有任何"清除所有内容"的端点**

用户只能逐条删除消息、文件、评论。没有 `DELETE /v1/account/data` 或类似批量清除端点。

### 产品经理视角

- **合规刚需**：GDPR 第 15 条（数据访问权）和第 17 条（被遗忘权）要求平台提供服务。缺少数据导出和批量删除是合规风险。
- **用户信任**："阅后即焚"（ephemeral messaging）是 Telegram/Signal 等安全通讯应用的核心卖点。
- **存储成本控制**：运营者现在只能通过 GC 清理软删除数据，无法设置内容级自动过期政策。对于敏感内容（如临时分享的文件），超时自动过期可以防止数据泄露。
- **`Meta` 字段已预留扩展空间**：文件的 `Meta map[string]any` 和消息的 `Meta` 可以承载 TTL 设置，不需要 schema 变更——只需 service 层和 sweeper 读取 `meta.ttl`。

### 建议

1. **TTL 支持**
   - 文件 `Create` / 消息 `Send` 接受可选 `ttl` 参数（单位秒，0 = 永不过期）
   - 存储 TTL 在 `Meta.ttl`（JSON map），或扩展 schema 加 `expires_at` 列
   - 新增轻量 sweeper（或扩展现有 GC）：`WHERE expires_at IS NOT NULL AND expires_at < now()`
   - TTL 过期行为：文件自动软删除 + 通知 owner；消息自动软删除（发件人和收件人都不可见）

2. **数据导出**
   - 新增 `GET /v1/account/export` 端点
   - 异步处理：请求后排入后台作业，完成后通过通知推送下载链接
   - 导出格式：JSON（消息、文件元数据、评论、通知等），文件内容使用 presigned URL 引用
   - 使用已有的 outbox 机制触发导出完成通知

3. **批量数据清除**
   - 新增 `DELETE /v1/account/data` 端点（需要提前确认，如 `confirm=true` 参数）
   - 事务性软删除用户所有消息、文件、评论
   - 写入审计日志（`audit_events`）
   - 纳入 outbox：发送 `account.data_cleared` 事件

4. **独立于 GC 的 TTL sweeper**：与存储 GC 分开运行（GC 处理软删除+孤儿，TTL sweeper 处理未删除但过期的内容），调度频率可配置

### 边界情况

- **TTL 与软删除的关系**：TTL 过期 = 自动软删除。用户仍可手动恢复（在 GC retention 窗口内）。更激进的策略：TTL 过期 = 硬删除（不可恢复），适合加密通讯场景。
- **已读后的 TTL**：可配置为"发送后 N 秒"或"已读后 N 秒"两种模式。"阅后即焚"通常指读取后自动删除。
- **导出大小限制**：数据导出可能很大（GB 级文件）。限制为 7 天内可下载一次，超大数据分片打包。
- **清除的确认机制**：`DELETE /v1/account/data` 应先发确认邮件/通知（涉及不可逆操作，安全第一）。

---

## 方向五：通知系统的全量覆盖缺口 — file_shared、user_blocked、content_flagged 等事件未通知

### 问题

当前通知系统只覆盖了三种事件类型：`message.received`、`message.read_receipt`、`comment.reply`。这与产品已实现的功能面存在严重不对等——文件分享、Block、审计事件等敏感操作完全未接入通知。

### 代码级证据

**证据 1：`internal/notification/dispatcher.go` 只处理三种 event type**

```go
// internal/notification/dispatcher.go:73-80
func (d *Dispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":
        return d.notificationMessageSent(e)
    case "message.read":
        return d.notificationMessageRead(e)
    case "comment.replied":
        return d.notificationCommentReplied(e)
    default:
        return nil  // ← 所有其他事件被静默丢弃
    }
}
```

**证据 2：`file.shares.granted` 事件已通过 outbox 发布，但 notification 处理返回 nil**

```go
// internal/file/service_share.go — Grant 方法
// 发布 outbox 事件 file.shared，但 dispatcher 不认识它
```

类似地：
- `user.blocked` / `user.unblocked` 事件同样未被处理
- `comment.reported` 事件未被处理（举报一个评论后，被举报者不会收到通知）
- `file.deleted` / `message.deleted` 没有被通知

**证据 3：`sharee` 在被分享文件后完全不知情**

文件所有者调用 `POST /v1/files/:id/share` 后，被分享者（sharee）只能主动查询 `GET /v1/files/shared-with-me` 发现新分享的文件。没有任何通知推送或 SSE 事件告知被分享者。

**证据 4：`domain/audit.go` 定义了多个敏感操作类型，没有对应通知**

```go
// internal/domain/audit.go
const (
    AuditFileShareGrant  = "file.share.grant"
    AuditFileShareRevoke = "file.share.revoke"
    AuditReportResolve   = "comment.report.resolve"
    AuditUserBlock       = "user.block"
    AuditUserUnblock     = "user.unblock"
    AuditMessageDelete   = "message.delete"
)
```

这些审计操作中很多**既是审计事件，也是需要通知受影响的用户的事件**。例如 A block B——B 不需要被通知"你被 block 了"（block 是方向性的），但 A 可能想知道 B 是否已收到消息（已有已读回执），而 B 在尝试发消息给 A 时会得到错误提示。更关键的是：

- **被分享文件**：`AuditFileShareGrant` → sharee 应收到 `file.shared_with_you` 通知
- **评论被举报**：评论 owner 应收到 `comment.reported` 通知
- **举报被处理后**：举报者应收到 `report.resolved` 通知

### 产品经理视角

- **通知覆盖面是平台成熟度的关键指标**：当前只有 3 种通知类型，但可能触发通知的产品场景至少有 8-10 种。用户发出分享后石沉大海、评论被举报后无反馈——这些都是真实的 UX 缺陷。
- **现有基础设施零成本扩展**：outbox 事件已经在发布，dispatcher 已经接收到它们。只需在 `notificationFor` switch 中增加 case 分支，每个新增通知类型约 10-20 行代码。
- **系统消息（`MessageKindSystem`）扩展性**：某些通知场景（如"你的文件被下载了"）更适合使用系统消息推送而非 notification 表。

### 建议

向 `internal/notification/dispatcher.go` 新增以下通知类型（按优先级排序）：

| 优先级 | 事件类型 | 触发场景 | 通知内容 |
|--------|---------|---------|---------|
| **P0** | `file.shared` | 文件所有者分享了文件 | `"张三 分享了一个文件 报告.pdf"` → sharee |
| **P0** | `file.share_revoked` | 文件所有者撤销了分享 | `"张三 取消了对文件 报告.pdf 的分享权限"` → sharee |
| **P1** | `comment.reported` | 用户举报了评论 | `"你的评论被举报，正在审核中"` → 评论 owner |
| **P1** | `report.resolved` | 管理员处理了举报 | `"评论 #xxx 的举报已被处理（已隐藏/无违规）"` → 举报者 |
| **P1** | `user.blocked_by` | 被 block 的用户尝试发消息 | 无需主动通知，在 Send 时返回 `Forbidden("blocked")` |
| **P2** | `file.expired` | 文件 TTL 过期自动删除 | `"文件 报告.pdf 已过期，已自动删除"` → owner |
| **P2** | `storage.quota_warning` | 存储使用量接近配额 | `"存储使用量已达 85%，请清理或升级"` → owner |

**实施注意**：
- 每个通知类型定义自己的 payload struct（类似 `messageSentPayload`）
- Block 检查已经在 dispatcher 入口路径中，新增的通知类型自动受益
- SSE 推送自动覆盖所有通知类型的实时投递
- 通知偏好（`notification_preferences`）已支持按类型开关——新增类型应注册到偏好系统中

### 边界情况

- **通知风暴**：如果文件被分享给 100 人，不产生 100 条通知——应使用 `cc` 方式（一条通知列出前 5 个受让人 + "等 95 人"）
- **已删除的内容**：通知引用的内容（文件、评论）可能已在通知送达前被删除。`subject_id` 查询时应返回"内容已删除"占位，而非 500 错误
- **Block 后的通知抑制**：如果 B block 了 A 后 A 分享文件给 B——不应产生通知。现有 `dispatcher` 的 `block.BlockChecker` 查询已经覆盖该场景

---

## 总结：优先级与建议

| # | 方向 | 类型 | 优先级 | 估算影响 | 为什么现在做 | 既有文档覆盖 |
|---|------|------|--------|----------|------------|------------|
| 1 | **Admin 控制面 gRPC + 用户封禁 + 死信运维 API** | 架构完备性 / Ops | **P0** | 补齐控制面协议对等 + 死信可观测性 | gRPC 是双协议核心承诺；死信运维是生产必备 | **未覆盖** |
| 2 | **分布式速率限制** | 扩展性 / 安全 | **P0** | 解锁水平扩展；消除限流盲区 | 多实例部署是时间问题，越早引入技术债越少 | **未覆盖** |
| 3 | **会话生命周期管理（Mute/Archive/Pin/Typing）** | 产品体验 | **P1** | 基础产品能力完善 | 低代码变更，高 UX 提升，复用 SSE 基础设施 | **未覆盖** |
| 4 | **内容生命周期（TTL/自动过期/GDPR 导出/批量清除）** | 合规 / 产品 / 成本 | **P1** | 合规 + 存储成本 + 用户信任 | 数据保护法规日趋严格；TTL 是"阅后即焚"协同基础 | **未覆盖** |
| 5 | **通知全量覆盖（file.shared, report.resolved 等缺失场景）** | 产品完整性 | **P1** | 补全产品功能面与通知面的映射 | 现有 outbox 基础设施已就绪，增量成本极低 | **未覆盖** |

### 与既有 docs/requirements/ 中 ~75 份文档的差异化确认

每个方向上方的分析均与既有文档做了交叉比对：

- **方向一**：之前的文档提到"死信运维"和"gRPC parity"，但没有覆盖 **AdminService 的整体缺失**（用户封禁端点、死信查询/重放 API 的完全缺失、admin gRPC proto 的零存在）。
- **方向二**：既有文档提到 rate limit 但只作为"功能已存在"提及，**未分析分布式场景下的限流失效问题**。`internal/pkg/ratelimit` 的文档注释明确将其标记为 out-of-scope——这是产品化前的已知技术债。
- **方向三**：既有文档讨论过"group messaging"（多人群组），但**未覆盖 1:1 会话本身的组织管理**（mute/archive/pin/typing）。这些是独立于群组功能的基础能力。
- **方向四**：既有文档覆盖了"文件版本管理"和"内容管道"，但**未覆盖生命周期管理**（TTL、自动过期、GDPR 导出、批量清除）——这是合规/产品/成本的交集。
- **方向五**：既有文档覆盖了"通知系统"的实现，但**未做场景覆盖度审计**——没有发现只有 3/10+ 的场景被通知覆盖了这个事实。

### 依赖关系

- **#1（Admin ops）** 不依赖其他方向，可以立刻开始
- **#2（分布式限流）** 不依赖其他方向，可以独立实现
- **#3（会话管理）** 部分依赖 #5 工作——mute 需要通知系统的 mute 过滤
- **#4（生命周期）** 依赖 GC sweeper 扩展，但不阻塞其他方向
- **#5（通知覆盖）** 不依赖其他方向，可以立刻开始——只增加新 case 和 payload type
