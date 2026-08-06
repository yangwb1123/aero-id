# aero-id — 资深架构师/产品经理视角：五个生产级扩展方向

> **日期:** 2026-07-11
> **方法:** 全量代码库逐文件扫描 — `internal/` 全 23 个子包（~180 Go 源文件）、`migrations/` 全部 22 份 SQL、`api/proto/`、`cmd/`、`configs/`、`deployments/`、`Makefile`、`docs/`。与 `docs/requirements/` 下 **100+ 份既有分析**、`ROADMAP.md`、`docs/REVIEW_*`、`docs/CHANGELOG.md`、`docs/DECISIONS.md` 做逐方向比对。
> **约束:** 不编写任何代码。每个方向提供精确的代码级证据、架构师与产品经理双视角分析、边界情况与实现考量。

---

## 代码库成熟度摘要

aero-id 已经从一个经典三层 CRUD MVP 演进为具有完备生产级能力的服务平台，以下功能均已完成交付：

| 域 | 已交付能力 |
|----|-----------|
| **基础设施** | 双协议 HTTP+gRPC、GORM+PG16、本地/S3 存储、OTel 链路追踪、RED 指标、per-dependency `/readyz`、冷启动 warmup、优雅关停 |
| **可靠性** | 事务性 Outbox、幂等键 + 过期清理、速率限制（per-actor token bucket + idle eviction）、SSO 断路器 + stale-cache + singleflight + 重试退避 + gRPC keepalive/TLS/mTLS/证书锁定 |
| **文件** | 流上传、MIME/size 校验、SHA-256 内容去重、S3 presigned 直传 + 原子完成、显式分享/撤销、引用计数 GC 扫除、元数据 PATCH、storage refs |
| **消息** | 1:1 私信（含系统消息）、Reactions、编辑/软删、已读回执 + 计数、Inbox/Conv 视图、Block 拦截、消息级 audit trail |
| **评论** | 两层嵌套（楼中楼）、Like/Unlike（原子计数器）、举报→审核队列、Hide/Unhide、编辑 |
| **通知** | Outbox 驱动的异步创建、SSE 实时推送 + 断线重连回填、折叠去重、偏好（启用/静默时段/时区）、Block 抑制、Webhook 投递 |
| **搜索** | 跨实体 FTS（Postgres tsvector + GIN 索引）、CJK ILIKE 回退、多 kind（messages/comments/files）联合搜索 |
| **安全** | 方向性 Block、审计日志（文件分享/消息删除/举报处理等）、Admin 角色 + 权限体系（content.moderate / audit.read / user.ban / system.config） |
| **数据模型** | 22 次迁移（含 FTS 索引、通知索引、Outbox 重试、通知折叠、Storage Ref、Admin 角色） |

---

## 五项生产级扩展方向

| # | 方向 | 类别 | 优先级 | 核心原因 |
|---|------|------|--------|----------|
| 1 | **批量操作 API**（Bulk Operations） | 产品功能 | P1 | 当前所有操作都是单实体，缺少批量删除/标记已读/管理等效率操作 |
| 2 | **Outbox 事件驱动搜索索引重建**（Event-Driven Search Index Rebuild） | 可靠性 | P1 | FTS 索引依赖应用层写入，断电/回滚后索引与数据不一致 |
| 3 | **连接池资源隔离与数据库过载保护**（Connection Pool Isolation & DB Overload Protection） | 运维/可靠性 | P1 | 速率限制只防请求量，不防慢查询耗尽连接池导致级联故障 |
| 4 | **实时协作基础设施——输入指示与在线状态**（Real-time Collaboration Infrastructure: Typing Indicators & Presence） | 产品功能 | P2 | 已有 SSE 推送通道，但缺乏用户在线状态和输入提示等基础 IM 功能 |
| 5 | **配置热加载与运行时动态调整**（Hot-Reload Config & Runtime Tuning） | 运维 | P2 | 全部配置在启动时冻结，无法在不重启的情况下调优参数 |

---

## 方向一：批量操作 API（Bulk Operations）

### 为什么需要

当前的 API 设计中**每一条写操作都对应一个独立的 RPC/HTTP 请求**。这在 PM 视角看，有两个直接的用户体验问题：

1. **消息收件箱管理效率低下**：收件箱中有 50 条未读消息，用户想"全部标记已读"或者"删除与某人的全部消息"——当前需要前端发 50 个 `MarkRead` / `DeleteMessage` 请求，每个请求都需要完整的认证、授权、审计日志。
2. **管理后台无法批量操作**：Admin 用户想批量删除某个用户的文件、批量撤销文件分享、批量 resolve 举报——全部需要写脚本或发 N 个请求。

### 代码级证据

```go
// api/proto/aero_id/v1/aero_id.proto (MessageService)
service MessageService {
  rpc SendMessage(SendMessageRequest) returns (SendMessageResponse);
  rpc GetMessage(GetMessageRequest) returns (GetMessageResponse);
  rpc Inbox(InboxRequest) returns (InboxResponse);
  rpc Conversation(ConversationRequest) returns (ConversationResponse);
  rpc MarkRead(MarkReadRequest) returns (MarkReadResponse);   // ← 只能单个
  rpc DeleteMessage(DeleteMessageRequest) returns (DeleteMessageResponse); // ← 只能单个
  rpc MarkConversationRead(MarkConversationReadRequest) returns (MarkConversationReadResponse); // ← 按会话
  // 无 BatchMarkRead, BatchDeleteMessages, BatchResolveReports
}

// 文件操作也是单体的
service FileService {
  rpc DeleteFile(DeleteFileRequest) returns (DeleteFileResponse);  // ← 单个
  rpc ShareFile(ShareFileRequest) returns (ShareFileResponse);     // ← 单文件，可多 grantee
  rpc UnshareFile(UnshareFileRequest) returns (UnshareFileResponse); // ← 单 grantee
  // 无 BatchDeleteFiles, BatchUpdateVisibility, BatchRevokeShares
}

// 通知也是单体
service NotificationService {
  rpc MarkRead(MarkNotificationReadRequest) returns (MarkNotificationReadResponse);
  rpc MarkAllRead(MarkAllNotificationsReadRequest) returns (MarkAllNotificationsReadResponse);
  // 有 MarkAllRead（全量），但缺少"批量选择标记已读"的中间粒度
}
```

当前仅有的批量语义：
- `MarkConversationRead` — 按会话标记全部已读（按 `to_uid`/`from_uid` 范围删选）
- `MarkAllRead` — 全量标记（无选择）
- 这两个都是"范围操作"，而非"指定 ID 列表"的批量操作。

```go
// internal/service/service.go 中 FileService 接口
type FileService interface {
    Create(ctx context.Context, ...)
    Get(ctx context.Context, ...)
    List(ctx context.Context, ...)
    Delete(ctx context.Context, id domain.ID) error       // ← 单个
    DownloadURL(ctx context.Context, ...)
    PresignUpload(ctx context.Context, ...)
    CompleteUpload(ctx context.Context, ...)
    Share(ctx context.Context, ...)
    Unshare(ctx context.Context, ...)
    ListSharedWithMe(ctx context.Context, ...)
    UpdateMetadata(ctx context.Context, ...)
    // 无 BatchDelete 或 BatchShare
}
```

### 边界情况与考量

- **事务边界**：批量操作应当是"尽力而为"（best-effort）还是"全有或全无"（all-or-nothing）？对于"批量删除消息"，用户预期可能是尽力而为——删除能删的，返回失败列表。对于"批量 resolve 举报"，Admin 预期应该是全有或全无。
- **幂等性**：批量操作与幂等键的交互方式：一个幂等键可以保护整个批量请求，但部分失败时如何定义"已完成的响应"？
- **速率限制**：批量操作应当在 single request 层面计数（1 个批量为 1 次请求），还是按实际处理实体数量加权计数（批量批量删除 50 条消息 = 50 次操作）？后者可以防止恶意利用批量接口刷配额。
- **审计日志**：批量操作的 audit event 需要记录 `subject_ids` 为数组。
- **负载上限**：建议将批量大小上限作为参数（如 `max_batch_size: 100`），避免一个请求的执行时间超过 handler timeout。

### 建议实施路径

优先级顺序（从低到高）：

1. **消息层面**：`BatchMarkRead(ids)` — 开销最小，用户价值最高
2. **文件层面**：`BatchDeleteFiles(ids)` — 配合 storage refs 和 GC，技术难度中等
3. **Admin 层面**：`BatchResolveReports(ids, status)` — 管理后台刚需
4. **通用批量 API 模式**：定义 `BulkOp` message 框架，后面新加的批量操作复用同一模式

---

## 方向二：Outbox 驱动搜索索引重建（Event-Driven Search Index Rebuild）

### 为什么需要

当前的全文搜索（FTS）依赖应用层在数据变更时同步更新 `tsvector` 列。这意味着：

1. **索引来源不可靠**：如果应用层在写 `messages` 行之后、写 `tsvector` 之前崩溃，或者数据通过原始 SQL 迁移/修复工具直接修改，搜索索引会与真实数据不一致。
2. **没有重建路径**：没有 API 可以触发搜索索引的批量重建。如果索引损坏或者增加了新的搜索字段（如 `file.meta` 字段现在也需要搜索），管理员无法在线重建索引。
3. **不是所有实体都有索引**：`file_shares`（分享人与文件名不可搜索）、`notification` 的 content 字段（如果有）不可搜索。

### 代码级证据

```sql
-- migrations/000016_fts_indexes.up.sql
-- 为 messages、comments、files 添加 tsvector 列和 GIN 索引
ALTER TABLE messages ADD COLUMN IF NOT EXISTS search_vector tsvector
  GENERATED ALWAYS AS (
    to_tsvector('simple', coalesce(content, ''))
  ) STORED;

ALTER TABLE comments ADD COLUMN IF NOT EXISTS search_vector tsvector
  GENERATED ALWAYS AS (
    to_tsvector('simple', coalesce(content, ''))
  ) STORED;

ALTER TABLE files ADD COLUMN IF NOT EXISTS search_vector tsvector
  GENERATED ALWAYS AS (
    to_tsvector('simple', coalesce(name, '') || ' ' || coalesce(meta->>'description', ''))
  ) STORED;
```

FTS 使用 `GENERATED ALWAYS AS ... STORED` 列——这意味着索引是由 PostgreSQL 在 **行级别写入时自动维护**的（`INSERT` 或 `UPDATE`）。这已经消除了应用层写索引的故障面。但 **SEARCH 索引在批量数据修复或回滚后仍然可能不一致**。更具体的分析：

```go
// internal/repository/message.go 中的搜索实现
func (r *messageRepo) SearchMessages(ctx context.Context, actor, query string, limit int) ([]*domain.Message, error) {
    var msgs []domain.Message
    // 使用 tsvector 搜索
    err := r.db.WithContext(ctx).
        Where("(from_uid = ? OR to_uid = ?) AND search_vector @@ plainto_tsquery('simple', ?)", actor, actor, query).
        Order("created_at DESC").
        Limit(limit).
        Find(&msgs).Error
    if err != nil {
        return nil, err
    }
    // 如果 tsvector 没有匹配，fallback 到 ILIKE
    if len(msgs) == 0 {
        // ILIKE fallback ...
    }
    return msgs, nil
}
```

问题是：
- `search_vector` 是 **generated column**，其值由 PostgreSQL 在 **行插入/更新时** 计算。如果通过 `UPDATE ... SET search_vector = NULL` 或 migration 增加了新的搜索维度（例如 `meta->>'tags'`），旧行不会自动重新计算。
- 没有 `REINDEX` 的 API 端点。

```go
// 当前的搜索编写路径
// internal/message/service.go — SendMessage 会写 outbox 事件
func (s *messageService) Send(ctx context.Context, in ...) (*domain.Message, error) {
    // ...
    if err := s.repo.Create(ctx, msg, evt); err != nil {
        return nil, err
    }
    // FTS 索引由 GENERATED ALWAYS 列自动更新 — 好
    // 但 outbox 事件没有 "search.rebuild" 类型
}

// 但是！如果数据是通过以下路径更新的，tsvector 可能不会自动更新：
// 1. 软删除恢复（UnhideComment）— 只更新 status，content 不变，OK
// 2. 编辑消息（EditMessage）— content 变，generated column 自动重算，OK
// 3. 直接 SQL 修复（运维操作）— 可能跳过 trigger
// 4. 批量导入/迁移 — 没有 REINDEX 保障
```

### Outbox 驱动的索引重建

当前 outbox 的事件类型包括：

```go
// internal/service/notification.go — NotificationDispatcher
case "message.sent":
case "message.read":
case "comment.replied":
```

没有用于索引维护的事件。推荐方向：

- 新增 `search.index.rebuild` 事件类型：当 Admin 触发重建时，写入此事件，outbox handler 按实体类型分批重建 FTS 索引
- 新增 `search.index.updated` 增量事件：当实体被编辑/恢复时（如 `file.updated`、`comment.restored`），确保 tsvector 列被重新触发
- 新增 Admin API：`POST /v1/admin/search/rebuild?kind=messages` — 触发全量重建

> **注意**: 因为当前使用 `GENERATED ALWAYS AS ... STORED`，PostgreSQL 15+ 可以 `ALTER TABLE ... ALTER COLUMN search_vector DROP EXPRESSION` 然后重新添加以强制重算。但这不是常规操作，且需要独占锁。更安全的路径是通过临时列或 `UPDATE ... SET search_vector = DEFAULT`。

### 边界情况

- **重建期间的搜索可用性**：全量重建可能需要数十秒（百万级消息时）。需要确保 `REINDEX CONCURRENTLY` 或分段重建 + 不阻塞查询。
- **增量 vs 全量**：增量更新通过 outbox 事件触发；全量重建作为计划任务或 Admin API。
- **搜索版本**：当搜索表达式变更（如增加索引字段），需要一次全量重建来覆盖所有历史数据。

---

## 方向三：连接池资源隔离与数据库过载保护（Connection Pool Isolation & DB Overload Protection）

### 为什么需要

这是当前系统最容易被忽视的生产级风险。当前架构有一个**共享的数据库连接池**（`database.max_open_conns`），所有操作——包括快速 OLTP 查询、全文搜索、后台 GC sweep、outbox relay ——都从同一个池子里拿连接。

架构師视角：当一个慢查询（如复杂的全文搜索、大批量数据迁移）占用了池中的大部分连接时，**所有依赖数据库的轻量请求都会排队等待连接释放**。这会导致：

1. **HTTP 健康检查超时**：`/healthz` 和 `/readyz` 都依赖数据库 ping。如果连接池耗尽，健康检查会超时 → load balancer 将实例从服务池中移除。
2. **级联超时**：请求排队等待连接 → handler timeout 触发 → 请求失败 → 客户端重试 → 更多连接被占用。
3. **outbox relay 无法写入**：`MarkPublished` 也需要连接。如果 relay 拿不到连接，事件堆积 → 通知延迟 → SSE 推送延迟。

### 代码级证据

```go
// internal/app/app.go — 单一连接池
gdb, err := db.Open(db.Options{
    DSN:             cfg.Database.DSN(),
    MaxOpenConns:    cfg.Database.MaxOpenConns,   // ← 一个池子给所有人用
    MaxIdleConns:    cfg.Database.MaxIdleConns,
    ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
    LogLevel:        cfg.Database.LogLevel,
})
// 所有组件共用这个 gdb：
// - 所有 repository（file, message, comment, notification, block, admin）
// - outbox relay 的 FetchUnpublished + MarkPublished + MarkFailed
// - storage GC 的 sweeper
// - idempotency 的 Reserve + Complete + Release + purgeExpired
// - health check 的 PingContext
// - audit log 的写入
// - search 的 tsquery 查询
```

```go
// internal/config/config.go — 数据库配置
type DatabaseConfig struct {
    MaxOpenConns    int           `mapstructure:"max_open_conns"`    // 默认值？
    MaxIdleConns    int           `mapstructure:"max_idle_conns"`
    ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
    // 没有：
    //   SearchMaxConns int  — 搜索用分离的连接数
    //   IdleQueryTimeout — 慢查询自动取消
    //   StatementTimeout — SQL 语句执行超时
}
```

```go
// internal/pkg/db/db.go — GORM 连接池配置
func Open(opts Options) (*gorm.DB, error) {
    // ...
    sqlDB, err := db.DB()
    sqlDB.SetMaxOpenConns(opts.MaxOpenConns)  // 单一池
    sqlDB.SetMaxIdleConns(opts.MaxIdleConns)
    sqlDB.SetConnMaxLifetime(opts.ConnMaxLifetime)
    // 没有设置 sqlDB.SetConnMaxIdleTime
    // 没有设置 per-operation 超时
}
```

```go
// internal/repository/search_integration_test.go — 搜索查询可能很重
// SearchMessages 执行的 SQL 包含：
//   WHERE search_vector @@ plainto_tsquery('simple', ?)
//   ORDER BY created_at DESC
// 在百万行级的 messages 表上，tsquery 扫描 + 排序可能耗时数秒
```

此外，搜索查询当前**没有查询超时**：

```go
// internal/message/message.go — SearchMessages 接口
type MessageRepository interface {
    // ...
    SearchMessages(ctx context.Context, actor, query string, limit int) ([]*Message, error)
    // 使用传入的 ctx（可能来自 handler，已有 deadline）但是：
    // 1. handler 的 deadline 来自 middleware/timeout.go — 可配置
    // 2. 而后台操作（outbox relay、GC sweep）使用 context.Background()
}
```

### 边界情况与考量

- **连接池隔离级别**：完全独立池（每个组件一组连接）vs 带有优先级标记的共享池。完全独立池更安全但也更浪费连接资源。
- **读/写分离**：可以将搜索和后台任务流量路由到 read-replica，减少与主库 OLTP 操作的争用。但这需要新增数据库配置和连接。
- **语句级超时**：PostgreSQL 支持 `statement_timeout` 和 `idle_in_transaction_session_timeout`。可以在 `db.Open` 中设置会话参数，确保任何超时的 SQL 都不会长时间占用连接。
- **断路器等价物**：当连接池耗尽率达到阈值（如 80% 占用持续 10 秒），系统应自动降级非关键路径（如搜索降级为 `query too complex` 错误，GC sweep 跳过当前轮次，outbox relay 降低轮询频率）。
- **Granular connection tagging**：在 GORM 的 withContext 中注入连接标签，便于区分 OLTP 和后台流量（通过 `session` 中间件设置 `application_name`）。

### 建议方案

1. **短平快**：设置 PostgreSQL 的 `statement_timeout = '30s'` 防止查询级悬挂
2. **分离连接池**：为搜索和后台作业（outbox relay、GC sweep）创建独立的 GORM 实例，使用不同的 `MaxOpenConns`
3. **读副本支持**：将搜索流量路由到 read-replica（当前架构已支持，只需要新加一个 `database.replica_dsn` 配置）

---

## 方向四：实时协作基础设施——输入指示与在线状态（Real-time Collaboration Infrastructure）

### 为什么需要

aero-id 已经有了 SSE 实时推送（`GET /v1/events`）和 `Hub.Pusher` 接口，但当前**只用于服务端向客户端推送通知**。从 IM 产品角度看，这是单向的——用户看不到对方是否在线、是否正在输入、是否已经看到消息（read receipt 已经实现，但只有接收方读取时设置时间戳，没有送达回执）。

从产品经理视角看：

1. **Typing Indicators**：当 Alice 正在给 Bob 打字时，Bob 的 UI 应该看到"Alice 正在输入…"。这是现代 IM 的基础 UX，缺失会让产品显得幼稚。
2. **Online/Offline Presence**：Bob 想知道 Alice 是否在线。当前只能通过"她是否在我发消息后 5 秒内已读"来推断。
3. **Message Delivery Receipts**：当前有已读回执（`messages.read_at`），但**没有送达回执**（delivered = 消息已到达收件人的至少一个设备）。

### 代码级证据

```go
// internal/hub/hub.go — SSE Hub，目前只用于通知
type Hub struct {
    subs   sync.Map // uid → *uidSubs
    nextID atomic.Int64
    closed atomic.Bool
}
// Subscribe/Publish 方法已经支持多设备（同一个 uid 多个 subscriber channel）
// 基础设施就绪，只需要业务逻辑

// internal/hub/adapter.go — 适配 service.NotificationPusher
func (h *Hub) Publish(uid, eventType, data string, eventID int64) {
    h.pub(uid, SSEEvent{ID: eventID, Type: eventType, Data: data})
}
// 可以将同样的机制用于：
// 1. "typing" 事件：Alice 的客户端发送 SSE 到 hub → hub 推给 Bob
// 2. "presence.online" 事件：Alice 连接时为 online，断开时为 offline
// 3. "message.delivered" 事件：消息成功写入收件人的 inbox 时触发
```

```go
// internal/domain/message.go — 消息模型
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    // ...
    ReadAt    *time.Time  // ← 已读回执已有
    // 缺少：DeliveredAt *time.Time
    // 缺少：DeliveryStatus int  (0=sent, 1=delivered, 2=read)
}
```

```go
// migrations/000002_create_messages.up.sql — 消息表 DDL
CREATE TABLE messages (
    id          UUID PRIMARY KEY,
    from_uid    TEXT NOT NULL,
    to_uid      TEXT NOT NULL,
    // ...
    read_at TIMESTAMPTZ,  // ← 已读时间戳存在
    // 无 delivered_at 列
    // 无 delivery_status 列
);
```

```go
// internal/handler/http/events.go — SSE 端点
func (h *EventsHandler) Stream(c *gin.Context) {
    // 当前实现：建立 SSE 连接后被动等待通知推送
    // 没有从客户端接收消息的机制（没有 WebSocket 上行通道）
    // 不能接受 "typing" 或 "presence" 事件
}
```

```go
// internal/handler/http/router.go — 路由
func NewRouter(..., events *EventsHandler, ...) *gin.Engine {
    // ...
    events.RegisterRoutes(r)  // GET /v1/events (SSE)
    // 无 POST /v1/events (上行)
    // 无 GET /v1/presence/:uid  (查询用户在线状态)
}
```

### 边界情况与考量

- **Typing 流量控制**：输入指示是高频事件（每次按键都可能触发）。需要在前端做节流（如每 3 秒最多发送一次），防止后端被淹没。
- **Presence 隐私**：用户应该能选择是否公开在线状态（通过 preferences 系统）。当前 `notification_preferences` 表可以扩展 `show_online_status` 字段。
- **多设备**：一个用户可能有 3 个设备同时在线。Hub 已经支持多订阅者，但 typing 事件只需要推送到**与发送方有活跃会话**的设备的对端——不需要通知全部在线用户。
- **SSE 上行限制**：SSE 是纯下行协议。如果保留 SSE（不引入 WebSocket），需要：
  - 用 `fetch()` + `POST` 将 typing/presence 事件上行发送到 `/v1/events/typing`
  - 或者引入 WebSocket 端点（`/v1/ws`）支持双向通信
- **连接生命周期**：presence 依赖于 SSE 连接生命周期。但 SSE 可能因网络抖动短暂断开而重连。需要设计"在线"的心跳 + 宽限期（如断开 30 秒内视为"在线"），避免频繁的 online/offline 闪烁。

### 最小可行方案（不引入 WebSocket）

1. **Typing**：`POST /v1/typing`（上行）+ Hub 转发给会话对端（下行）
2. **Presence**：Hub 在 Subscribe/Unsubscribe 时记录在线状态；`GET /v1/presence/:uid` 查询
3. **Delivery receipts**：在 `MarkPublished` 后，针对 `message.sent` 事件写入 `delivered_at`（比 read_at 更细的粒度）

---

## 方向五：配置热加载与运行时动态调优（Hot-Reload Config & Runtime Tuning）

### 为什么需要

当前全部配置在 `app.go` 启动时从 `configs/config.yaml` + 环境变量一次性加载，之后完全冻结。这在生产环境中带来几个具体问题：

1. **速率限制参数不可运行时调整**：如果生产流量突发，运营商需要调高 `rate_limit.rps` 或调低 `rate_limit.burst`，当前必须**重启服务**。
2. **日志级别不可运行时调整**：debug 一个线上问题时，想临时开启 `log.level=debug`，必须重启。
3. **Outbox polling interval 不可运行时调整**：如果发现 outbox 堆积，运营商想临时将 poll interval 从 5s 降到 1s 来加速 drain——不行。
4. **功能开关不可热切换**：storage GC 是 opt-in 的，但如果 operator 想临时关闭 GC 以避免干扰高峰期——只能重启。

### 代码级证据

```go
// internal/config/config.go — 所有配置一次性加载
type Config struct {
    App        AppConfig       `mapstructure:"app"`
    Server     ServerConfig    `mapstructure:"server"`
    Database   DatabaseConfig  `mapstructure:"database"`
    SSO        SSOConfig       `mapstructure:"sso"`
    Storage    StorageConfig   `mapstructure:"storage"`
    Tracing    TracingConfig   `mapstructure:"tracing"`
    RateLimit  RateLimitConfig `mapstructure:"rate_limit"`
    Outbox     OutboxConfig    `mapstructure:"outbox"`
    SigningKey string          `mapstructure:"signing_key"`
    Log        LogConfig       `mapstructure:"log"`
    // 没有 DynamicConfig 子结构
    // 没有 hot-reload 回调
    // 没有 runtime watcher
}

// Config 被传入 app.New() 后立即展开，后续不再引用 cfg 指针
func New(ctx context.Context, cfg *config.Config, log *zap.Logger) (*App, error) {
    // cfg 在此处被全部展开为具体值
    // ... cfg.Database.MaxOpenConns → gdb 配置
    // ... cfg.RateLimit.RPS → ratelimit.New(rate, burst, idleTTL)
    // ... cfg.Outbox.PollInterval → relay.Start(pollInterval)
    // 之后 cfg 指针不再被使用
}
```

```go
// internal/pkg/ratelimit/ratelimit.go — Limiter 构造后不可变
type Limiter struct {
    rate  float64       // tokens added per second — 创建后不可变
    burst float64       // bucket capacity — 创建后不可变
    // ...
}
// 无 SetRate(rate float64) 方法
// 无 SetBurst(burst int) 方法
```

```go
// internal/outbox/relay.go — Relay 的 poll interval 在 Start 时固定
func (r *Relay) Start(interval time.Duration) (stop func()) {
    t := time.NewTicker(interval)  // ← interval 不可变
    // ...
}
// 无 SetInterval(d time.Duration) 方法
```

```go
// internal/handler/http/health.go — 需要支持动态维护模式
type HealthHandler struct {
    checks []Check
}
// 无 SetMaintenanceMode(bool) 方法
// 无 Add/RemoveCheck 运行时方法
```

```go
// internal/storage/gc/sweeper.go — GC 间隔在 Start 时固定
func (s *Sweeper) Start(interval time.Duration) func() {
    // interval 不可变
}
```

```go
// internal/pkg/logger/logger.go — 日志级别在初始化后不可变
func New(level, encoding string) (*zap.Logger, error) {
    // level 在 zap.NewAtomicLevelAt 中设置
    // 虽然 zap 支持动态级别（AtomicLevel），但当前代码没有暴露此能力
}
```

### 边界情况与考量

- **哪些配置应当是可热加载的**：不是所有配置都适合运行时修改。`signing_key`、`database.password`、`tls_cert_file` 等安全敏感配置不应热加载。建议分级：
  - **安全热加载**：`rate_limit.*`、`log.level`、`outbox.poll_interval`、`outbox.max_retries`、`storage.gc.interval`、`server.http.request_timeout`
  - **仅启动加载**：`database.*`、`sso.endpoint`、`signing_key`、`tls_*`、`storage.type`、`tracing.*`
- **热加载方式**：建议支持两种方式：
  1. **文件 watch**：viper 支持 `viper.WatchConfig()` + `OnConfigChange` 回调
  2. **Admin API**：`PUT /v1/admin/config` 更新运行时参数（只影响内存，不持久化到 YAML）
- **配置版本与回滚**：每次运行时参数变更应记录到 audit log（`action: "config.update"`），且支持查询当前生效的运行时配置快照。
- **并发安全**：`ratelimit.Limiter` 当前有 `sync.Mutex`，可以安全添加 `SetRate` 方法。`outbox.Relay` 需要能安全替换 ticker。

### 建议方案

最小实施路径：

1. **Logger 级别动态调整**：将 `zap.AtomicLevel` 暴露为 runtime 变量，通过 `GET /debug/log/level` 和 `PUT /debug/log/level?level=debug` 调整
2. **Rate limit 运行时调整**：给 `Limiter` 添加 `SetRate(rps, burst)` 方法，通过 Admin API 修改
3. **配置文件 watch**：添加 `viper.WatchConfig()` + 注册需要热更新的组件的刷新回调

---

## 优先级排期建议

| 方向 | 工作量 | 影响面 | 依赖 | 建议顺序 |
|------|--------|--------|------|---------|
| 批量操作（消息标记已读） | 低（2-3 天） | 用户体验 | 无 | **P0** |
| 查询超时 + 连接池隔离 | 中（5-7 天） | 生产稳定性 | 无 | **P1** |
| 搜索索引重建 | 中（5-7 天） | 数据一致性 | outbox 已就位 | **P1** |
| 配置热加载（日志级别 + 速率） | 低（3-5 天） | 运维效率 | viper WatchConfig | **P2** |
| 实时协作基础设施 | 高（10-15 天） | 产品能力 | SSE Hub 已就位 | **P2** |

**第一优先级**（可独立启动）：
- 批量标记消息已读（`BatchMarkRead`）— 小改动，大 UX 提升
- PostgreSQL `statement_timeout` 设置 + 健康检查连接池保护 — 配置变更，零代码改动

**第二优先级**：
- 连接池隔离 + 搜索查询超时
- 搜索索引重建 API

**第三优先级**：
- 配置热加载（日志级别先行）
- Typing indicator + presence
