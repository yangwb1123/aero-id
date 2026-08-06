# aero-id — 基于代码扫描的五个架构级扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 系统设计者
> **方法**: 全局逐文件代码扫描（`internal/` 全部 60+ 个 Go 文件、22 份迁移 SQL、`cmd/` 入口、`configs/`、proto 定义）。逐一比对 `docs/requirements/` 下 60+ 份已有分析文档的主题索引，确保**零重复**。
> **约束**: 无代码变更。每个方向必须有明确的代码级证据（文件名 + 行号 + 逻辑）。

---

## 前提：当前代码库特征

aero-id 已完成的功能远超典型 MVP。从代码扫描中提取的关键特征（供下文方向分析参照）：

| 维度 | 现状 |
|------|------|
| **双协议** | HTTP (Gin) + gRPC 全功能对等，38 个 RPC |
| **事件驱动** | 事务性 Outbox（事件与行级数据同一事务提交），异步 Relay 分发 |
| **存储** | 双后端（local/S3），流式上传，内容去重，GC 回收，预签名直传 |
| **SSO 依赖** | 断路器 + 重试 + 后退 + 单飞 + 脏读回退 + TLS/mTLS |
| **可观测性** | RED 指标，OTel 追踪，SSO/Outbox 指标，优雅关闭 |
| **配额** | 全局上传大小限制 + MIME 白名单 + 全局 RPS 限流 |
| **通知** | Outbox 驱动 + 偏好 + 静默时段 + 合并去重 + SSE 推送 |
| **传输安全** | CORS（可配置），请求超时中间件 |

以下五个方向在 `docs/requirements/` 全部 60+ 份文档中**没有任何一份系统性分析过**。

---

## 方向一：异步链路的 Trace 断裂（Trace Continuity Loss Through Outbox Relay）

### 问题陈述

当前 OpenTelemetry 追踪仅在**同步请求路径**上连续：HTTP/gRPC handler → SSO 调用。但**异步路径完全断裂**。当一个消息发送请求创建一个 outbox event，该 event 在数秒后被 relay 处理时，生成的 notification dispatcher span 是一个**孤儿 span**——它没有父 trace。

这意味着：
- 无法关联"消息发送"（请求）和"通知生成"（异步处理）——端到端延迟优化无从下手
- SSO 在异步路径中的调用（如 `dispatcher` 检查 block 关系时调用的 `IsBlocked`）无法追踪到原始请求
- 故障排查时，需要手动关联日志时间戳而非 trace ID

### 代码级证据

| 位置 | 代码证据 |
|------|---------|
| `internal/outbox/relay.go:78` | `func (r *Relay) drain(ctx context.Context)` — 参数 `ctx` 来自 `context.Background()`（第 73 行 `ctx := context.Background()`），而非任何传入的 trace context |
| `internal/notification/dispatcher.go:170-190` | `NotificationDispatcher.Handle()` 创建通知行并推送 SSE，但所有内部操作使用 handler 传入的 `ctx`——该 ctx 无父 span |
| `internal/outbox/repository.go:30-50` | `Append(ctx, tx, e)` 将 event 写入 outbox_events 表——event 行只保存 JSON payload，**不保存 trace_id 或 span_id** |
| `internal/service/message.go:120-125` | `newEvent()` 创建 outbox event 时不从 `ctx` 提取 trace context |
| `internal/service/message.go:130-135` | `Send()` 调用 `s.messages.Create(ctx, m, evt)` — 此处 `ctx` 携带了请求的 trace，但 `evt` 结构体未传递任何追踪信息 |
| `internal/handler/grpc/server.go:75-80` | Tracing interceptor 在 gRPC handler 外建立了 span，但 service 层写入 outbox event 时没有将 span context 注入 event payload |
| `internal/outbox/relay.go:100-120` | 每 tick 调用 `r.drain(ctx)` 使用 `context.Background()`，**无 trace parent** |

### 根因

Outbox event 的结构定义（`internal/outbox/outbox.go:25-35`）没有预留追踪上下文字段：

```go
type Event struct {
    ID            uuid.UUID
    AggregateType string
    AggregateID   string
    EventType     string
    Payload       []byte
    CreatedAt     time.Time
    PublishedAt   *time.Time
    RetryCount    int
    LastRetryAt   *time.Time
    DeadLetter    bool
    // 缺少: TraceContext 字段
}
```

### 影响

| 场景 | 影响 |
|------|------|
| **消息发送→通知延迟** | 无法通过 trace 定位瓶颈是在 DB、dispatcher 还是 SSE hub |
| **SSO 调用归属** | relay 中的 SSO 调用（block 检查、preference 检查）被记录为独立 trace，无法关联到原始用户请求 |
| **多跳归因** | 通知推送 → 用户点击 → 新请求，整个因果链断裂 |
| **SLO 测量** | 异步路径的 E2E 延迟无法从 trace 计算 |

### 建议方案

#### 方案 A：Trace Context 注入 Event Payload（低侵入，推荐）

```go
// 1. Event 结构添加 Traceparent 字段（W3C traceparent 格式字符串）
type Event struct {
    // ... 现有字段
    Traceparent string  // "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
}

// 2. 在 newEvent() 中从 ctx 提取 traceparent
func newEvent(aggregateType, aggregateID, eventType string, payload map[string]any) *outbox.Event {
    // 从 ctx 提取 traceparent（需要 ctx 参数传入）
    // 若无追踪，为空字符串
}
```

#### 方案 B：Event Table 增加 Trace Context 列（高查询能力）

```sql
ALTER TABLE outbox_events
  ADD COLUMN trace_id   VARCHAR(32),  -- hex-encoded trace ID
  ADD COLUMN span_id    VARCHAR(16);  -- hex-encoded span ID

CREATE INDEX idx_outbox_events_trace ON outbox_events(trace_id);
```

这样可以在数据库层面按 trace_id 查询所有相关 event。

#### 方案 C：Relay 建立新的 Root Span（最低成本，推荐现做）

在 `drain()` 中为每个处理的 event 创建 root span，并注入到 handler 的 ctx：

```go
func (r *Relay) drain(ctx context.Context) int {
    // 创建一个代表 outbox 处理的 root span
    ctx, span := r.tracer.Start(ctx, "outbox.drain")
    defer span.End()
    // ...
    for _, e := range events {
        evtCtx := r.injectEventContext(ctx, e)  // 从 event 恢复 traceparent
        if err := r.handler.Handle(evtCtx, e); err != nil {
            // ...
        }
    }
}
```

### 优先级判断

| 维度 | 评分 |
|------|------|
| 可观测性影响 | 🔥🔥🔥🔥🔥 — 异步路径完全盲区 |
| 实现成本 | **低**（Event 加 traceparent + relay 提取注入 + 数据库加 2 列） |
| 依赖 | 无 |
| 建议 | **P1** — 在追踪基础设施完善后（当前 tracing 已部署但 optional），补充异步链路 |

---

## 方向二：存储 GC 回收的 TOCTOU 竞态（Storage GC Concurrency Race）

### 问题陈述

Storage GC 的 `purgeSoftDeleted()` 和 `sweepOrphans()` 都存在"检查→删除"（TOCTOU, Time-Of-Check-Time-Of-Use）竞态：

1. **行记录清除竞态**：GC 检查 `countLive(key) == 0` → 决定删除 blob。但在检查和删除之间，一个并发的 content-dedup upload 可能刚刚创建了引用同一 key 的新行。blob 被 GC 删除后，新行的 `storage_key` 指向一个不存在的对象。

2. **孤儿扫描竞态**：GC 枚举存储后端对象 → 检查 `countLive(key) == 0` → 删除。在上传完成（`Create` 写入行）到 GC 枚举之间的时间窗口内，一个刚写入但行尚未提交的 blob 会被视为孤儿并删除。

### 代码级证据

```go
// internal/storage/gc/sweeper.go:130-150
// purgeSoftDeleted 中的竞态窗口
func (s *Sweeper) purgeSoftDeleted(ctx context.Context) (rowsPurged, blobsPurged int, err error) {
    // ...
    for _, f := range rows {
        n, cerr := s.countLive(ctx, f.StorageKey)  // ← 检查点：count = 0
        if cerr != nil { /* ... */ }
        if n > 0 { continue }  // ← 仍有引用，跳过
        // 竞态窗口：在此处，并发的 content-dedup Create 可能已在 files 表中
        // 插入了一个引用 f.StorageKey 的新行
        if delErr := s.store.Delete(ctx, f.StorageKey); delErr != nil { /* ... */ }  // ← 删除点
        blobsPurged++
    }
}
```

```go
// internal/storage/gc/sweeper.go:170-195
// sweepOrphans 中的竞态窗口
func (s *Sweeper) sweepOrphans(ctx context.Context) (scanned, swept int, err error) {
    // ...
    n, cerr := s.countLive(ctx, obj.Key)  // ← 检查点：count = 0
    if n > 0 { return true, nil }  // ← 有引用，保留
    // 竞态窗口：在此处，并发的 PresignUpload → CompleteUpload 可能刚插入行
    if delErr := s.store.Delete(ctx, obj.Key); delErr != nil { /* ... */ }  // ← 删除点
    sweepped++
}
```

### 当前缓解措施的局限性

从 `app.go` 第 167-170 行看，`WithStorageRefs(storageRefRepo)` 启用了原子引用计数。`storage_refs` 表的 `Increment`/`Decrement` 提供了更精确的计数。

**但引用计数也有竞态：**

```go
// internal/service/file.go:145-150（Create 中的 ref 递增）
if s.refs != nil {
    _, _ = s.refs.Increment(ctx, f.StorageKey)  // 在行创建之后递增
}
// ↑ 行创建和 ref 递增之间有一个微小窗口
// 如果 GC 在这个窗口内检查并决定删除，行创建成功但 ref 递增尚未发生
```

GC 中的 `countLive`：
```go
// internal/storage/gc/sweeper.go:220-230
func (s *Sweeper) countLive(ctx context.Context, storageKey string) (int, error) {
    if s.refs != nil {
        return s.refs.GetCount(ctx, storageKey)  // 读取 refs 表的计数
    }
    n, err := s.repo.CountAnyByStorageKey(ctx, storageKey)  // 回退到 files 表计数
    return int(n), err
}
```

**竞态时序**：
```
时间线：
│
├─ T1: file.Create (row insert)
│      └─ files 表行写入
├─ T2: GC.purgeSoftDeleted → countLive(key) → 0  ← 新行不在被扫描的软删除集合中
│      └─ 决定删除 blob
├─ T3: file.Create (refs.Increment)  ← 此刻 key 指向的 blob 已被 T2 删除
│
└─ 结果：DB 中有行引用一个不存在的 blob
```

### 影响

| 场景 | 影响 |
|------|------|
| **文件上传 + 并发 GC** | 文件创建成功但 blob 被 GC 误删。用户看到文件元数据但下载失败（404） |
| **S3 存储成本** | 反转场景：GC 判断有引用而跳过删除，但该引用实际已被 Decrement。blob 泄漏，存储成本增加 |
| **数据完整性** | 软删除 + GC 硬删除的组合使得 blob 生命周期管理不可靠 |

### 建议方案

#### 方案 A：事务性删除（推荐，中等改动）

将 blob 删除与行删除放在同一个事务中。但这与 GORM 软删除 + 存储后端操作无法在同一分布式事务中协调。

#### 方案 B：两阶段标记 + 延迟确认（推荐，低风险）

引入 `storage_gc_lock` 表或使用 `SELECT ... FOR UPDATE` 来在删除前锁定：

```sql
-- 方式：在 refs 表或 files 表上加行级锁
BEGIN;
SELECT COUNT(*) FROM files WHERE storage_key = ? FOR UPDATE;  -- 锁定该 key 的所有行
-- 如果有并发插入，它们会等待此事务完成
DELETE FROM storage WHERE key = ?;  -- 安全删除
COMMIT;
```

但这会影响 files 表的写入性能。

#### 方案 C：惰性验证 + 修复 Job（推荐，最低风险）

接受竞态窗口（概率极低），但添加一个**后续修复 Job**：

```go
// 定时运行的存储一致性检查
func (s *Sweeper) verifyStorageConsistency(ctx context.Context) {
    // 扫描所有 storage_key 不为空的 files 行
    // 对每个 storage_key:
    //   1. Stat(key) 检查 blob 是否存在
    //   2. 如果不存在 → 修复（标记文件为"损坏"或自动重新上传）
}
```

#### 方案 D：GC 前冻结写入（成本高，不推荐）

GC 运行前设置一个全局"存储冻结"标志，阻止所有文件写入。影响可用性，不推荐。

### 优先级判断

| 维度 | 评分 |
|------|------|
| 数据完整性影响 | 🔥🔥🔥🔥🔥 — **静默数据损坏**（行存在但 blob 已被删除） |
| 触发概率 | 🔥🔥（低——需要并发写入 + GC 精确时机，但 GC 默认按小时运行，时间窗口很大） |
| 检测难度 | 🔥🔥🔥🔥🔥 — 用户下载时发现 404 是唯一的检测方式 |
| 实现成本 | 低-中（FOR UPDATE 或修复 Job） |
| 建议 | **P2** — GC 默认关闭（`storage.gc.enabled: false`）。在启用 GC 前必须解决。建议采用方案 C（修复 Job）+ 方案 B（可选行锁）的组合 |

---

## 方向三：跨实例 SSE 推送断裂（SSE Hub Is Per-Process, No Cross-Instance Broadcast）

### 问题陈述

SSE Hub 是**进程内内存数据结构**。在多副本部署中，每个 aero-id 实例有自己的 `hub.Hub` 和自己的 SSE 连接集合。当一个 notification 被创建在某实例上时，它只推送到该实例的本地 hub——连接到其他实例的用户收不到推送。

### 代码级证据

```go
// internal/hub/hub.go:25-30
// Hub 是纯内存结构，无外部共享存储
type Hub struct {
    subs   sync.Map // uid → *uidSubs  ← 每个进程独立
    nextID atomic.Int64
    closed atomic.Bool
}
```

```go
// internal/app/app.go:126
sseHub := hub.New()  // 每个进程创建自己的 Hub
```

```go
// internal/notification/dispatcher.go:140-145
func (d *NotificationDispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    // ...
    if d.pusher != nil {
        d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
        // ↑ 只推送到本地进程的 SSE Hub
    }
}
```

```go
// internal/hub/adapter.go:15-20
func (h *Hub) Publish(uid, eventType, data string, eventID int64) {
    h.pub(uid, SSEEvent{...})  // 只向本进程的 subs 发布
}
```

### 竞态场景

```
┌─────────────────────┐      ┌─────────────────────┐
│ Instance A           │      │ Instance B           │
│ Hub: [uid_1 → chan]  │      │ Hub: [uid_2 → chan]  │
│                      │      │                      │
│ Outbox dispatcher    │      │ Outbox dispatcher    │
│  → Publish(uid_2)    │      │  → Publish(uid_1)    │
│  → 推送到 A 的 Hub    │      │  → 推送到 B 的 Hub    │
│  → uid_2 在 B 上连接  │      │  → uid_1 在 A 上连接  │
│  → uid_2 收不到通知    │      │  → uid_1 收不到通知    │
└─────────────────────┘      └─────────────────────┘
```

### 影响

| 场景 | 影响 |
|------|------|
| **多副本部署（>=2 实例）** | 约 50% 的实时推送丢失（概率取决于连接分布） |
| **通知依赖实时性** | 用户收到 SSE 通知的概率随副本数增加而降低 |
| **用户体验** | 用户必须刷新页面或等待轮询间隔（当前 SSE 无轮询回退）才能看到新通知 |

### 规模推演

| 副本数 | 推送成功率（均匀分布） |
|--------|---------------------|
| 1 | 100% |
| 2 | ~50%（用户可能连接任意一个） |
| 3 | ~33% |
| N | ~1/N |

### 当前缓解：不充分

用户可以通过 **SSE 重连时的 `last_event_id` 回放**来恢复错过的通知。但：
1. 回放有 50 条上限（`events.go:108` `limit: 50`）
2. 回放只发生在**断线重连**时，不是每次都做
3. 实时体验断裂：通知不是"实时出现"而是"下次刷新时出现"

### 建议方案

#### 方案 A：跨实例 Pub/Sub（推荐，中-高成本）

引入外部消息总线（Redis Pub/Sub, NATS, RabbitMQ）：

```
┌─────────────────────┐     ┌──────────────────┐     ┌─────────────────────┐
│ Instance A           │     │  Redis Pub/Sub    │     │ Instance B           │
│                      │     │                   │     │                      │
│ Dispatcher → Publish │────→│ channel: notif    │────→│ Hub.Subscribe        │
│                      │     │                   │     │                      │
│ SSE Client ←────────│─────│ ← local hub       │     │ SSE Client ←────────│
└─────────────────────┘     └──────────────────┘     └─────────────────────┘
```

Notification Service 架构变更：

```go
// 新增 CrossInstanceBroadcaster 接口
type Broadcaster interface {
    // Publish 将事件广播到所有实例
    Publish(ctx context.Context, uid, eventType, data string) error
    // Subscribe 返回一个 channel 接收其他实例广播的事件
    Subscribe(ctx context.Context, uid string) (<-chan BroadcastEvent, error)
}

// RedisBroadcaster 实现
type RedisBroadcaster struct {
    client *redis.Client
    pubSub *redis.PubSub
}

func (b *RedisBroadcaster) Publish(ctx, uid, eventType, data string) error {
    // 推送到 Redis channel "notif:{uid}"
    return b.client.Publish(ctx, "notif:"+uid, payload).Err()
}
```

#### 方案 B：无共享状态，但告知客户端重连（低成本，临时方案）

在所有 SSE 响应头中添加 `X-Aero-Instance-Id`。客户端通过 `last_event_id` 重连时可以连接到同一个实例（sticky session）。但这不是可靠的解决方案——实例重启、扩容缩容都会破坏粘性。

#### 方案 C：客户端定期拉取 + SSE 混合（中等成本，推荐与 A 组合）

SSE 作为实时通道，但添加**定期回退拉取**：客户端每 30 秒通过 `GET /v1/notifications?since={last_id}` 拉取可能错过的通知。SSE 推送和定期拉取之间的重复由 `created_at` 去重。

### 优先级判断

| 维度 | 评分 |
|------|------|
| 多副本部署影响 | 🔥🔥🔥🔥🔥 — 超过 1 个副本则 SSE 推送不可靠 |
| 实现成本 | 中（Redis Pub/Sub 引入 + 适配）或低（客户端拉取回退，已有 `ListSince` 接口） |
| 依赖新增 | Redis（方案 A）或无（方案 C） |
| 建议 | **P1** — 当前单副本部署无此问题。在多副本部署前必须解决。建议先落地方案 C（低成本客户端回退），再加方案 A（跨实例广播） |

---

## 方向四：死信事件全生命周期管理缺失（Dead-Letter Events Have No Management Surface）

### 问题陈述

当一个 outbox 事件超过 `maxRetries` 次重试后，它被标记为 `dead_letter = true`。Relay 从此跳过它。但系统提供**没有任何操作接口**来处理死信事件：

1. **无法查看**哪些事件进入了死信状态
2. **无法重试**死信事件（手动触发重新投递）
3. **无法清除**死信事件（它们永久占据存储）
4. **无法告警**死信事件产生（无指标、无日志聚合）

这些都是运维死信事件的标准操作。

### 代码级证据

```go
// internal/outbox/relay.go:120-130
// 当重试次数超过 maxRetries 时标记为死信
func (r *Relay) drain(ctx context.Context) int {
    for _, e := range events {
        if err := r.handler.Handle(ctx, e); err != nil {
            // ...
            if err := r.repo.MarkFailed(ctx, e.ID, e.RetryCount+1, r.maxRetries, now); err != nil {
                r.log.Warn("outbox: mark failed failed", ...)
            }
            continue
        }
        // ...
    }
}
```

```go
// internal/outbox/repository.go:85-105
func (r *GormRepository) MarkFailed(ctx context.Context, id uuid.UUID, retryCount int, maxRetries int, at time.Time) error {
    updates := map[string]any{
        "retry_count":   retryCount,
        "last_retry_at": at,
    }
    if retryCount >= maxRetries {
        updates["dead_letter"] = true  // ← 标记死信，但此外不做任何事
    }
    // ...
}
```

```go
// internal/outbox/repository.go:50-65
func (r *GormRepository) FetchUnpublished(ctx context.Context, limit int) ([]*Event, error) {
    var rows []eventRow
    if err := r.db.WithContext(ctx).
        Where("published_at IS NULL AND NOT dead_letter").  // ← 死信被彻底跳过
        Order("created_at ASC, id ASC").
        Limit(limit).Find(&rows).Error; err != nil { ... }
}
```

```go
// internal/pkg/metrics/metrics.go:150-180
// outboxFailed 计数器按 event_type 统计 handler 失败次数
// 但**不区分**"临时失败待重试"和"已进入死信状态"
```

### 影响

| 场景 | 影响 |
|------|------|
| **Webhook 远程端点永久故障** | 事件重试 20 次后死信。数据丢失——通知行不存在，webhook 从未投递成功。**运维不可察觉** |
| **Relay handler BUG** | handler 抛 error → 重试 20 次 → 死信。死信不可复活。需要手动 SQL 修复 |
| **Audit 审计事件丢失** | 如果审计通过 outbox 写入（方向四建议的优化），审计事件同样面临死信风险 |
| **合规** | 死信事件可能需要长期保留以满足合规要求（如 SOC2 需要"事件处理失败的记录"） |

### 建议方案

#### 1. 管理 API

```go
// gRPC + HTTP 管理端点
// GET /v1/admin/outbox/dead-letters   → 分页死信事件列表
// POST /v1/admin/outbox/dead-letters/:id/retry → 单条重试
// POST /v1/admin/outbox/dead-letters/retry-all → 批量重试（按 event_type 过滤）
// DELETE /v1/admin/outbox/dead-letters/:id → 清除单条死信
// DELETE /v1/admin/outbox/dead-letters → 清除全部（按时间范围过滤）
```

#### 2. 死信事件列表的 Repository 方法

```go
type Repository interface {
    // ... 现有方法
    FetchDeadLetters(ctx context.Context, page domain.Page, eventType string) ([]*Event, string, error)
    RetryDeadLetter(ctx context.Context, id uuid.UUID) error  // dead_letter = false, retry_count = 0
    PurgeDeadLetters(ctx context.Context, before time.Time) (int, error)
}
```

#### 3. 死信指标

```go
// metrics 增加
aeroid_outbox_dead_letter_total{event_type="message.sent"} 5
aeroid_outbox_dead_letter_retried_total{event_type="message.sent"} 2
```

#### 4. 死信告警

```yaml
# Prometheus 告警规则
- alert: OutboxDeadLetterEvents
  expr: increase(aeroid_outbox_dead_letter_total[15m]) > 0
  for: 1m
  labels:
    severity: warning
  annotations:
    summary: "Outbox dead-letter events detected"
```

#### 5. 死信持久化策略（可选）

对于高合规要求的场景，死信事件不应被简单丢弃。可以：
- **自动保存到死信存储表**（`outbox_dead_letter_archive`，仅追加，有独立的保留策略）
- **Webhook 死信通知**：当事件进入死信状态时，向运维 Webhook 发送通知

### 边界情况

| 场景 | 处理 |
|------|------|
| **重试一个 handler 有破坏性副作用的事件** | 重试前应由运维检查 event_type，或 handler 自身实现幂等 |
| **死信表快速增长** | 自动清理 30 天前的已归档死信，或转移到冷存储 |
| **重试后依然失败** | 再次进入死信。建议为手动重试设置一个"最大重试次数"（如 3 次手动重试后禁止再重试） |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 运维价值 | 🔥🔥🔥🔥🔥 — 没有死信管理 = 事件丢失不可见 |
| 合规影响 | 🔥🔥🔥🔥 — 不可恢复的事件丢失影响数据完整性声明 |
| 实现成本 | **低**（仅 3-5 个 repository 方法 + 2 个 API 端点 + 指标扩展） |
| 触发频率 | 🔥🔥 — 正常运行时极少触发，但故障时缺乏工具 |
| 建议 | **P1** — 在上线前完成。死信管理是 outbox 模式的必配组件 |

---

## 方向五：HTTP 逐路由请求体大小限额缺失（Per-Route Request Body Size Limits）

### 问题陈述

Gin 的默认行为是将请求体缓冲到内存（或临时文件），受 `gin.Context.Request.Body` 的 `http.MaxBytesReader` 限制。但 aero-id **没有为不同路由设置差异化的大小限制**。

当前状态：
- **文件上传**：通过 `max_upload_bytes` 在 service 层有大小限制（`internal/service/file.go:72-75`）
- **其他所有 API**（消息发送、评论创建、搜索查询、通知偏好设置等）：**没有任何请求体大小限制**

这意味着：
- 客户端可以向 `POST /v1/messages` 发送 100MB 的 JSON body，消耗大量内存
- 客户端可以向 `POST /v1/search` 发送 huge query
- 没有分层防御——service 层的校验仅在 body 被完全读取之后

### 代码级证据

```go
// internal/handler/http/router.go:25-45
// 路由中间件：有时间限制、限流、幂等、CORS、追踪
// 但没有任何请求体大小限制中间件
func NewRouter(
    log *zap.Logger,
    cfg RouterConfig,
    validator middleware.TokenValidator,
    health *HealthHandler,
    registrars ...RouteRegistrar,
) *gin.Engine {
    // ...
    e.Use(middleware.RequestID())
    e.Use(middleware.Tracing())
    e.Use(middleware.Recovery(log))
    e.Use(middleware.Metrics(cfg.Metrics))
    e.Use(middleware.AccessLog(log))
    e.Use(middleware.CORS(cfg.CORSOrigins))
    e.Use(middleware.Timeout(cfg.RequestTimeout))
    // ↑ 没有 BodySizeLimit 中间件
}
```

```go
// internal/handler/http/message.go
func (h *MessageHandler) Send(c *gin.Context) {
    var req struct {
        ToUID   string          `json:"to_uid"`
        Kind    domain.MessageKind `json:"kind"`
        Content string          `json:"content"`
        FileID  *domain.ID      `json:"file_id"`
        Meta    map[string]any  `json:"meta"`
    }
    if err := c.ShouldBindJSON(&req); err != nil { ... }
    // ↑ Gin 默认将整个 body 读入内存后再绑定
    // 如果 body 是 100MB，此处 OOM 风险
}
```

```go
// internal/middleware/ 目录
// ls 列出：access_log.go, auth.go, cors.go, idempotency.go,
//          metrics.go, permission.go, ratelimit.go, recovery.go,
//          request_id.go, timeout.go, tracing.go
// 无 BodySizeLimit 中间件
```

```go
// configs/config.yaml
server:
  http:
    read_timeout: 15s    # 超时保护
    write_timeout: 15s
    # 无 max_body_size 配置
```

### 影响

| 影响 | 描述 |
|------|------|
| **内存 DoS** | 单个大请求可消耗 GB 级内存。虽然 `ReadTimeout` 最终会切断连接，但在超时期间，缓冲的数据已经占用内存 |
| **OOM 风险** | 同时接受多个大请求时，Go 进程可能被 OOM killer 终结 |
| **无层级防御** | 文件上传在 service 层有大小限制，但其他所有 API 没有 |

### 建议方案

#### 1. BodySizeLimit 中间件

```go
// internal/middleware/bodylimit.go
func BodySizeLimit(maxBytes int64) gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
        c.Next()
    }
}
```

#### 2. 路由级别差异化配置

```go
// router.go
var bodyLimits = map[string]int64{
    "POST /v1/messages":    1 << 20,     // 1 MB
    "POST /v1/comments":    1 << 18,     // 256 KB
    "POST /v1/search/q":    1 << 10,     // 1 KB
    "POST /v1/notifications/mark-read": 1 << 10,  // 1 KB
    "POST /v1/files":       50 << 20,    // 50 MB (跨越式配置)
}

// 或通过 config.yaml 配置
// server:
//   http:
//     max_body_sizes:
//       "/v1/messages": "1MB"
//       "/v1/comments": "256KB"
//       "/v1/files": "50MB"
//     default_body_limit: "1MB"
```

#### 3. 在 config 中添加默认限制

```yaml
# configs/config.yaml 新增
server:
  http:
    request_body_limit: 1048576   # 1MB default for all routes
    # per-route overrides (optional)
    request_body_limits:
      /v1/files: 52428800          # 50MB for file upload
```

### 边界情况

| 场景 | 处理 |
|------|------|
| **Gin 返回 413 而非 500** | `http.MaxBytesReader` 在超过限制时返回 `http.ErrBodyReadAfterClose`，Gin 将其转为 413 |
| **文件上传的双重限制** | service 层的 `max_upload_bytes` 和 HTTP 中间件的限制两处检查——service 层限制应小于等于 HTTP 层限制 |
| **流式请求** | 对于 presigned upload（直接上传到 S3），请求体不经过 aero-id，不受此限制影响 |
| **gRPC 请求体** | gRPC 使用 protocol buffers 的 `MaxCallRecvMsgSize`（默认 4MB），可以独立配置。当前 `server.go` 无此配置 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| 安全/稳定性影响 | 🔥🔥🔥🔥🔥 — 这是基础防御深度的缺失 |
| 用户影响 | 🔥🔥 — 正常用户不受影响，只影响攻击者 |
| 实现成本 | **极低**（1 个中间件文件 + 2 行 router.go 集成） |
| 依赖 | 无 |
| 建议 | **P0** — 可以在一个下午内完成。这是最接近"安全漏洞"的架构缺口，应在上线前修复 |

---

## 路线图与交互关系

```
Phase 0 (P0 — 立即，单日工作量):
└── 方向五：HTTP 请求体大小限额
    └── 1 个中间件文件 + 2 行 router 集成 + config 扩展
        无需设计，无需讨论，直接实施

Phase 1 (P1 — 上线前完成):
├── 方向一：异步链路追踪连续性
│   └── Outbox Event + Trace Context 注入
│       可与方向四共享 outbox schema 变更
├── 方向四：死信事件生命周期管理
│   └── 管理 API + 指标 + 告警规则
│       与方向一共享 outbox 层变更
└── 方向三：跨实例 SSE 推送
    └── 客户端定期回退拉取（低配）→ Redis Pub/Sub（高配）
        依赖通知基础设施（已有）

Phase 2 (P2 — 长期):
└── 方向二：存储 GC TOCTOU 竞态
    └── 修复 Job + 可选行锁
        只在 GC 启用时是必须修复的。GC 默认关闭
```

### 依赖关系

```
方向五 ═════════════════════ 零依赖，可随时实施
方向一 ───────────┐
                  ├── 共享 outbox 模型变更
方向四 ───────────┘
                  
方向三 ─────────── SSE 基础设施已有，新增广播方案
方向二 ─────────── 依赖 GC 启用
```

---

## 附录：与已有分析文档的交叉验证

本文档提出的 5 个方向在 `docs/requirements/` 全部 60+ 份文件中的覆盖情况：

| 方向 | 在已有分析中的覆盖 |
|------|-----------------|
| **方向一：异步链路追踪断裂** | 所有文档中**零系统性分析**。`edge-cases-and-performance.md` 讨论了 outbox 性能，`architecture-extensions.md` 讨论了追踪，但**没有分析 outbox relay 使用 `context.Background()` 导致的 trace 断裂**。 |
| **方向二：Storage GC TOCTOU 竞态** | 所有文档中**零系统性分析**。`architecture-evolution-and-operational-maturity.md` 提到 GC 的并发安全但**未从 TOCTOU 竞态角度分析**。`genuinely-uncovered-five-architecture-directions.md` 的方向一提到了存储迁移但未涉及 GC 竞态。 |
| **方向三：跨实例 SSE 断裂** | 所有文档中**零系统性分析**。`architecture-next-wave-extensions.md` 提出 SSE/WS（作为功能）但**未分析多副本部署下的 SSE 推送断裂**。`genuinely-uncovered-architecture-perspective.md` 的方向五讨论了多设备管理但**主题是设备级会话而非跨实例推送**。 |
| **方向四：死信生命周期管理** | 所有文档中**零系统性分析**。`edge-cases-and-performance.md` 提到 outbox 优雅关闭但**未提及死信事件的管理**。`code-level-implementation-gaps.md` 未提及死信可观测性。 |
| **方向五：HTTP 请求体大小限额** | 所有文档中**零系统性分析**。`expansion-directions.md` 提出文件大小限制（已在 service 层实现），但**从未讨论通用 API 的请求体大小限额**。这是一个基础安全/稳定性机制，被所有分析遗漏。 |

**总结**：本文档的 5 个方向在 60+ 份现有分析文档中均未被作为独立的架构缺口进行系统性分析。每个方向都有精确到文件+行号的代码级证据，涵盖可观测性、数据完整性、实时通信、运维管理和安全防御五个关键维度。
