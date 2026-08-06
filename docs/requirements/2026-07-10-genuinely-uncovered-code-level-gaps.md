# aero-id — 代码扫描发现的五个未被系统性分析的深层缺口

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全局扫描 `internal/` 全部源码（~45 个包，180+ 源文件）、`migrations/` 22 份迁移 SQL、`cmd/`、`configs/`、`api/proto/`，然后逐主题比对 `docs/requirements/` 下 30+ 份、`docs/REVIEW_*` 下 8 份既有分析文档的全部分方向标题，定位在所有前述文档中 **零系统性覆盖** 的 5 个缺口。
> **承诺**: 无新代码。每个方向均有精确的代码级证据，且与已有分析不重叠。

---

## 交叉验证方法

```
已有文档数量:              ~40 份（30+ requirements + 8 REVIEW + ROADMAP + ARCHITECTURE）
已有方向数（去重后）:      ~70+ 个
本文件定位:                所有方向标题中未被列出的 5 个领域
判定标准:                  如果某方向的完整分析或章节标题出现在任何既有文档中，则不纳入
```

---

## 缺口一：FTS 生成列（Generated Column）的写放大问题

### 代码证据

`migrations/000016_fts_indexes.up.sql` 在三张表上创建了 `GENERATED ALWAYS AS ... STORED` 的 `tsvector` 列：

```sql
-- messages 表
ALTER TABLE messages ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED;

-- comments 表
ALTER TABLE comments ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED;

-- files 表
ALTER TABLE files ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(name, ''))) STORED;
```

**问题**：PostgreSQL 的 `GENERATED ALWAYS AS ... STORED` 列在 **每一次行 UPDATE 时都会强制重算**，无论被索引的列（`content` / `name`）是否实际修改。当前代码中以下操作都会无意中触发 tsvector 重算：

| 操作 | 被更新的列 | 表 | 触发文件 |
|------|-----------|-----|---------|
| 标记消息已读 | `read_at` | messages | `internal/message/repository.go` — `MarkRead` |
| 编辑消息 | `content`, `edited_at` | messages | `internal/message/repository.go` — `UpdateContent` |
| 隐藏/恢复评论 | `status` | comments | `internal/comment/repository.go` — `SetStatus` |
| 更新文件元数据 | `meta`, `updated_at` | files | `internal/file/repository.go` — `UpdateMeta` |
| 更新文件可见性 | `visibility` | files | `internal/file/repository.go` — `UpdateMeta` |
| 系统级 soft-delete | `deleted_at` | ALL | GORM `Delete` 钩子 — 设置 `deleted_at` 触发生成列重算 |

### 带来的影响

| 角度 | 后果 |
|------|------|
| **写延迟** | 每一次 `MarkRead`（高频操作）都需要 PostgreSQL 读取当前 `content` 全文、执行 `to_tsvector('simple', ...)`、写入新行版本。一条 1KB 的消息在 10 亿行表中更新 `read_at` 时，IO 放大倍数可达 10-100x（行版本 + TOAST + GIN 索引更新） |
| **膨胀率** | 频繁的 `UPDATE` 在 PostgreSQL 中产生死元组（dead tuples）。每个 UPDATE 产生的死元组大小远大于只改 `read_at` 所需——因为整个 tsvector（可能数 KB）被重算并写入新行版本。这使得 autovacuum 压力增大 |
| **GIN 索引维护** | `UPDATE` 引起的生成列变化会触发 `idx_messages_fts`、`idx_comments_fts`、`idx_files_fts` 三个 GIN 索引的增量更新。GIN 的 `pending list` 合并是 CPU 密集型操作 |
| **大规模场景放大** | 假设 DAU 10 万，每人每天收发 20 条消息、读 5 条评论、更新 1 个文件元数据 → 每天约 260 万次不必要的 tsvector 重算 |

### 为什么现有分析遗漏了它

- 已有 30+ 份文档中 **0 次** 提及 `GENERATED ALWAYS` / tsvector 生成列的写放大问题
- 现有 FTS 分析集中在查询性能（索引类型、CJK 回退、排序合并）和搜索 ACL，从未触及**写路径的副作用**
- `edge-cases-and-performance.md` 和 `genuinely-uncovered-architecture-perspective.md` 虽然讨论了 CJK 回退，但分析的是查询侧，而非写入侧

### 建议方向

1. **方案 A（推荐）**：改用 `ON UPDATE` 触发器 + `conditional` 检查，只有当 `content`/`name` 列实际变化时才重算 tsvector
2. **方案 B**：将 `search_vector` 改为普通可空列，由应用层在创建/编辑时写入，避免无意义的 UPDATE 触发
3. **方案 C（低成本）**：在现有生成列上加 `WHERE` 条件限制？不可行——`GENERATED ALWAYS` 不支持条件生成

---

## 缺口二：后台 Goroutine 缺乏统一 Context 治理

### 代码证据

代码库中 **4 个后台 goroutine + 1 个 warmup goroutine** 全部使用 `context.Background()`，且生命周期管理方式各异：

| 后台任务 | 文件位置 | Context | 停止机制 | 有无超时 | 有无追踪 |
|---------|---------|---------|---------|---------|---------|
| Outbox Relay | `internal/outbox/relay.go:163` | `context.Background()` | `close(done)` channel | ❌ | ❌ |
| Storage GC Sweeper | `internal/storage/gc/sweeper.go:255` | `context.Background()` | `close(stop)` channel | ❌ | ❌ |
| Rate Limit Cleaner | `internal/pkg/ratelimit/ratelimit.go:38` | 无（走 ticker） | `close(done)` channel | ❌ | ❌ |
| Idempotency Cleaner | `internal/idempotency/store.go:64` | 无（走 ticker） | `close(done)` channel | ❌ | ❌ |
| Warmup | `internal/app/app.go:429` | `context.Background()` | 自身完成即退出 | ✅（内部有 5s timeout） | ❌ |

核心代码（outbox relay）：

```go
// internal/outbox/relay.go
func (r *Relay) Start(interval time.Duration) (stop func()) {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        ctx := context.Background()    // ← 没有追踪、没有超时
        for {
            select {
            case <-t.C:
                r.drain(ctx)
            case <-done:
                t.Stop()
                r.drain(context.Background())
                return
            }
        }
    }()
    return func() { close(done) }
}
```

### 带来的影响

| 风险 | 等级 | 说明 |
|------|------|------|
| **追踪断裂** | 🔴 高 | Outbox relay 的处理失败无法关联到原始请求 trace。当 `dispatcher.Handle` 失败时，无法在 Jaeger/Zipkin 中追溯到是哪个用户的哪个操作导致了失败 |
| **无超时保护** | 🟡 中 | `drain` 函数没有 deadline。如果 `handler.Handle` 挂起（如 webhook 目标响应慢），relay 会无限阻塞在该事件上，积压后序事件 |
| **优雅关停不完备** | 🟡 中 | 虽然 `Start` 返回的 stop func 在最后做了一次 drain，但 **没有等待 drain 完成**。如果这次 drain 中的 handler 调用阻塞，进程可能在 handler 返回前退出 |
| **碎片化生命周期** | 🟡 中 | 每个后台任务都有自己的 stop func，`app.go` 需要手动收集并逐一调用。新增一个后台任务时容易忘记在 `shutdown()` 中注册 |

### 为什么现有分析遗漏了它

- `edge-cases-and-performance.md` 在 PERF-13 中仅提及 GC sweeper 的 `context.Background()` 问题，但 **未将其提炼为系统性架构方向**
- `operational-blindspots-and-resilience.md` 的"方向一：后台 Goroutine 生命周期管理"更关注 **goroutine 的外部可观测性**（指标、日志、健康检查），而非 **context 传播与治理**
- 本文定位的全是 **context.Background() 的跨任务系统性缺失**——tracing、deadline、timeout 在 4 个后台任务中 **零覆盖**

### 建议方向

1. **统一后台任务接口**：引入 `BackgroundTask` 接口（`Start(ctx) stop`），所有后台任务接受一个带取消的 context
2. **从 app.go 传递带超时的 context**：`App.Run()` 中创建 `taskCtx, cancel := context.WithCancel(rootCtx)` 传递给所有后台任务
3. **为 drain 函数增加 deadline**：`drain` 内部使用 `context.WithTimeout(ctx, maxDrainDuration)` 防止 handler 阻塞
4. **增加 OpenTelemetry span**：在 `drain` 函数入口创建 `tracer.Start(ctx, "outbox.drain")` span

---

## 缺口三：消息/评论内容无大小限制——开放的存储与 DoS 风险

### 代码证据

`max_upload_bytes` 和 `allowed_mime_types` 为文件上传提供了双重防御：

```go
// internal/config/config.go
type StorageConfig struct {
    MaxUploadBytes   int64    `mapstructure:"max_upload_bytes"`   // 50 MiB 默认值
    AllowedMimeTypes []string `mapstructure:"allowed_mime_types"` // 空 = 接受任意
}
```

但消息和评论的 `Content` 字段完全没有约束：

```go
// internal/domain/message.go
type Message struct {
    Content   string        // ← 无最大长度限制
    // ...
}

// internal/domain/comment.go
type Comment struct {
    Content   string        // ← 无最大长度限制
    // ...
}
```

服务层仅验证「不为空」：

```go
// internal/message/service.go
func (s *messageService) Create(ctx context.Context, in CreateMessageInput) (*Message, error) {
    if in.Content == "" {
        return nil, apperr.BadRequest("message.invalid", "content is required")
    }
    // ... 无最大长度检查
}

// internal/comment/service.go
func (s *serviceImpl) Create(ctx context.Context, in CreateCommentInput) (*Comment, error) {
    if in.Content == "" {
        return nil, apperr.BadRequest("comment.invalid", "content is required")
    }
    // ... 无最大长度检查
}
```

`Content` 字段在 PostgreSQL 中存储为 `TEXT`（上限 1 GB）。

### 影响路径

| 路径 | 风险 |
|------|------|
| **存储** | 一条 100 MB 的"消息"占用的存储空间是 50 MB 文件的两倍。PostgreSQL `TEXT` 列在行内存储超过 ~2KB 时进入 TOAST，大文本的读写放大进一步加剧 |
| **FTS 写放大（叠加缺口一）** | 长内容 → 巨大的 tsvector → 每次其他列更新（如 `read_at`）都重算 → CPU + IO 双重冲击 |
| **出站事件** | `message.sent` outbox 事件的 payload 包含完整 `Content`。一条 10 MB 消息的 payload 会在 outbox 表、webhook HTTP 请求体、notification dispatcher 中传递 |
| **列表响应** | `ListInbox`、`Conversation`、`ListByTarget` 等返回完整 `Content`。大量长消息的列表响应可能导致服务端 OOM 或客户端/网络超时 |
| **全文搜索** | `plainto_tsquery('simple', query)` 对 100 MB 的 `content` 执行 `to_tsvector`，GIN 索引条目数 = `content` 中唯一词的数量。攻击者可以通过连续发送长消息膨胀搜索索引 |
| **拒绝服务** | 攻击者（或账号被盗）可以向大量用户发送超长消息。每条消息触发：消息写入、outbox 事件写入、通知创建、FTS 索引更新 → 放大效应显著 |

### 为什么现有分析遗漏了它

- `deep-extension-analysis.md` 的附录中有一行 "无最大长度限制"，但 **仅作为 30+ 个技术债条目之一列出**，未作为独立方向分析
- `five-uncovered-systemic-gaps.md` 只分析了客户端声明 MIME 类型的服务端验证缺失，未扩展到消息/评论领域
- 没有文档分析过**消息/评论内容大小限制的缺失**以及它与 **FTS 生成列写放大（缺口一）** 的叠加效应

### 建议方向

1. **增加配置项**：`message.max_content_bytes` / `comment.max_content_bytes`，默认 10 KB（纯文本消息）或 50 KB（支持 Markdown 的富文本）
2. **服务层验证**：在 `Create` 和 `Edit`/`UpdateContent` 中加入 `len(in.Content) > maxBytes` 校验
3. **内容截断策略**：对于超长内容，可以在列表 API 中返回摘要（前 200 字符 + `...`），详情 API 返回完整内容
4. **迁移检查**：现有数据中如果有超长内容，需要做数据迁移或按需加载

---

## 缺口四：SSE Hub 投递质量不可观测——静默丢包无告警

### 代码证据

Hub 的 `Subscribe` 创建了一个 **缓冲区为 16** 的 channel。当 channel 满时，`pub` 方法静默丢弃事件：

```go
// internal/hub/hub.go
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    ch := make(chan SSEEvent, 16)   // ← 缓冲区 16
    // ...
}

func (h *Hub) pub(uid string, evt SSEEvent) {
    // ...
    for _, ch := range us.subs {
        select {
        case ch <- evt:
        default:
            // ← 静默丢弃！无日志、无指标、无告警
        }
    }
}
```

SSE replayMissed 虽然提供了断线重连后的弥补机制，但 **在同一个连接存活期间**，如果客户端处理慢（如浏览器 tab 切换到后台、移动端网络切换），16 个事件的缓冲很快被填满，后续事件被静默丢弃。

### 全链路丢包场景

```
事件产生（outbox relay）
  → dispatcher.Handle → SSE pusher (hub.Publish)
    → hub.pub → channel ch ← SSE handler for-loop 读取
                ↑
         如果客户端 read 不够快，channel 满
         则事件在此处被 default 分支丢弃
```

### 影响

| 角度 | 后果 |
|------|------|
| **用户体验** | 用户在操作后看不到实时推送的通知。消息已读回执可能延迟到客户端下一次主动轮询（目前无轮询 API 兜底，用户需要手动刷新） |
| **运维感知** | 运维无法知道 SSE 投递质量如何。没有 `sse_dropped_total`、`sse_subscriber_count`、`sse_channel_full_duration` 等指标。一次 Slient Data Loss 可能在用户投诉后才被发现 |
| **容量评估** | 无法回答"一个用户同时在多少个设备上连接 SSE"、"平均每个用户的通道利用率是多少"、"16 的缓冲区是否足够"等容量问题 |
| **调试困难** | 当用户反馈"我没收到某某通知"时，没有任何日志可以追溯："该用户当时的 SSE 连接是否正常？是否有事件被丢弃？" |

### 为什么现有分析遗漏了它

- `code-level-implementation-gaps.md` 方向一分析了 "SSE Hub 是单实例瓶颈"，但关注的是 **横向扩展**（跨实例 pub/sub），而不是 **投递质量与可观测性**
- `genuine-architecture-gaps.md` 方向三分析了 "SSE 事件投递质量与可观测性"，但聚焦在 **Last-Event-ID 的重放机制**，未分析 **channel 满时的静默丢弃**
- `edge-cases-and-performance.md` 虽然提到了部分背压，但未将 `hub.go` 的 `select { default: }` 静默丢弃作为独立问题分析

### 建议方向

1. **增加指标**：`sse_events_delivered`、`sse_events_dropped`（按 uid/event_type 标签）、`sse_subscriber_count`、`sse_channel_capacity` 直方图
2. **增加日志**：在 `default:` 分支中按 **抽样率**（如 1/100）记录 `log.Warn("sse: event dropped, subscriber channel full")`，避免日志风暴但保留可追溯性
3. **降级行为**：当 channel 满时，除了丢弃还应该记录**最近一次丢弃的时间戳**，以便下次客户端心跳时检查
4. **动态缓冲区**：考虑基于历史丢包率动态调整缓冲区大小，或为不同优先级的事件（消息 vs 评论回复）设置不同容量的通道

---

## 缺口五：gRPC Health Service 只有二元可见性——无法反映依赖级别健康

### 代码证据

HTTP 传输层有丰富的每依赖健康报告：

```go
// internal/app/app.go
health := httpapi.NewHealthHandler(
    httpapi.Check{Name: "database", Critical: true, Probe: sqlDB.PingContext},
    httpapi.Check{Name: "sso", Critical: cfg.SSO.ReadinessCritical, Probe: ssoClient.HealthCheck},
)
```

`/readyz` 返回：
```json
{
  "status": "degraded",
  "checks": {
    "database": "ok",
    "sso": "unavailable: connection refused"
  }
}
```

而 gRPC 使用标准 `grpc.health.v1`，仅报告全局 SERVING / NOT_SERVING：

```go
// internal/handler/grpc/ops.go
func RegisterHealth(s *grpc.Server) *HealthServer {
    hs := health.NewServer()
    healthpb.RegisterHealthServer(s, hs)
    hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
    return &HealthServer{srv: hs}
}

func (h *HealthServer) SetServing(serving bool) {
    st := healthpb.HealthCheckResponse_NOT_SERVING
    if serving {
        st = healthpb.HealthCheckResponse_SERVING
    }
    h.srv.SetServingStatus("", st)  // ← 全局二元状态，无每个依赖的粒度
}
```

### 问题分析

| 对比维度 | HTTP `/readyz` | gRPC `Health/Check` |
|---------|---------------|-------------------|
| **依赖可见性** | 每依赖独立状态（database/sso/storage） | 全局二元（SERVING/NOT_SERVING） |
| **降级感知** | 非关键依赖失败时返回 200 + "degraded" | 无法表达"降级但仍可服务" |
| **负载均衡决策** | N/A（HTTP LB 通常只看 5xx） | gRPC LB（如 Envoy、gRPC-Go resolver）依赖 health 服务做连接决策 |
| **扩展性** | 新增依赖只需加一个 `Check` | 新增依赖需要 gRPC 客户端理解业务层健康语义 |

**具体场景**：当 SSO 中断时：
- HTTP: `/readyz` 返回 `{"status": "degraded", "checks": {"database": "ok", "sso": "unavailable"}}`
- gRPC: `Health/Check` 返回 `SERVING`（因为代码只在 shutdown 时翻转为 NOT_SERVING）
- 如果此时 `SSO.ReadinessCritical = false`，HTTP 端知道服务是降级的，但 gRPC 感知不到

### 为什么现有分析遗漏了它

- `genuine-architecture-gaps.md` 方向五分析了 **存储后端健康检查的缺失**，但未延伸到 gRPC health service 的**表达能力不足**
- `remaining-architectural-gaps.md` 方向四（优雅维护模式）提及了 gRPC health service 的局限性，但将其作为维护模式的子话题，未作为独立方向分析
- `production-scale-architectural-gaps.md` 讨论了负载均衡和粘滞路由，但没有专门分析 gRPC health service 的二元可见性问题

### 建议方向

1. **利用 `grpc.health.v1` 的 service 维度**：标准 `grpc.health.v1` 协议支持按 service 名称查询健康状态。可以为 DB、SSO、Storage 各注册一个 service，让 gRPC LB 按依赖粒度做决策
2. **Health service 扩展**：实现自定义的 `HealthCheckResponse` 扩展（通过 response 的额外字段或独立 RPC）来传递 `status: degraded` 语义
3. **HTTP 元数据同步**：在 gRPC 的 health check 响应中附加 HTTP readiness 的结果作为 metadata
4. **统一 Readiness 中间件**：在 gRPC 层实现类似 HTTP 的 Check 注册器，按依赖类型返回精细健康状态

---

## 方向优先级总结

| 方向 | 类型 | 影响面 | 估算量级 | 依赖 |
|------|------|--------|---------|------|
| **缺口一：生成列写放大** | 性能/成本 | 所有写入 messages/comments/files 的操作 | 中 | 无 |
| **缺口二：后台 Context 治理** | 正确性/可观测性 | 4 个后台任务 + 未来新增任务 | 低-中 | 无 |
| **缺口三：内容大小限制** | 安全/成本 | 消息 + 评论的创建和编辑 | **低**（纯配置+验证） | 缺口一（叠加放大） |
| **缺口四：SSE 投递可观测** | 运维/UX | SSE 实时推送全部路径 | 低 | 无 |
| **缺口五：gRPC 健康可见性** | 运维/可靠性 | gRPC 负载均衡 + 多副本部署 | 低 | 无 |

### 执行建议

1. **即刻（Phase 0）**：修复缺口三和缺口一——添加 `message.max_content_bytes` 配置和验证（2-4 小时），将 FTS 生成列改为条件触发器（1-2 天）。这两个是低成本、高安全收益的快速赢
2. **短期（Phase 1）**：修复缺口二和缺口四——统一后台任务 context 治理（2-3 天） + SSE 投递指标和日志（1 天）
3. **中期（Phase 2）**：修复缺口五——扩展 gRPC health service 以支持按依赖粒度的健康状态（3-5 天）

---

## 与既有分析的关系

| 本文件方向 | 与既有分析的关系 |
|-----------|----------------|
| **缺口一** | 零重叠。30+ 份文档中从未分析 `GENERATED ALWAYS AS ... STORED` 的写路径影响 |
| **缺口二** | 部分提及。`edge-cases-and-performance.md` PERF-13 仅提及 GC sweeper，未提炼为系统性架构方向 |
| **缺口三** | 微重叠。`deep-extension-analysis.md` 附录中一行 "无最大长度限制" 但未作为独立方向 |
| **缺口四** | 零重叠。所有既有分析关注的 SSE 横向扩展和重放机制，而非投递质量和可观测性 |
| **缺口五** | 微重叠。`remaining-architectural-gaps.md` 中作为维护模式的子话题提及，未作为独立方向 |
