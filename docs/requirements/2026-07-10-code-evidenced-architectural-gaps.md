# aero-id — 基于深层代码扫描的架构缺口与扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 连续通读 `internal/` 全部 22 个 Go 包（~130 源文件）、22 份 SQL 迁移、双传输层 handler、proto 定义、config 后，与 `docs/requirements/` 下 50+ 份既有分析文档做逐条交叉比对，确保以下 5 个方向未被系统性覆盖。
> **约束**: 不编写任何代码。每个方向附带精确代码位置和设计建议。

---

## 前期发现汇总

此前 50+ 份分析已覆盖约 70+ 个方向，涵盖：

- 产品功能：群聊、文件版本、@提及、活动流、消息线程、内容集合、表情反应
- 架构演进：多副本缓存粘滞路由、分布式限流、GraphQL/BFF、多区域部署、事件溯源
- 运维成熟度：SLO 框架、混沌测试、配置漂移检测、零停机迁移、异地灾备
- 安全合规：E2EE、静态加密、内容安全检测、API key 认证、证书固定
- 正确性/Edge Case：并发计数竞争、FTS 写放大、Outbox 顺序保证、SSE 断线重连

**本文定位的 5 个方向全部是当前代码中存在结构性裂痕、但未进入任一份既有分析的问题。** 它们不是"未来的功能"，而是**代码中已经写好的不一致、未完成的抽象、或设计假设被突破**的地方。

---

## 方向一：双重文件服务实现——同一个领域两条代码线

### 性质

**高 · 架构债务 · 维护风险** — 结构分裂（Split Brain）

### 问题描述

aero-id 的代码库中存在 **两套独立的文件服务实现**，它们位于不同的包路径、实现不同的接口、互不调用，但代表同一个垂直领域。这是当前代码库最大的结构性债务。

### 代码级证据

**实现 A（在线）** — `internal/service/file.go`

- 文件: 241 行，包含 `CREATE`、`GET`、`LIST`、`DELETE`、`canRead`、`mimeAllowed`、内容去重、引用计数（`WithStorageRefs`）
- 接口: `service.FileService`（定义在 `internal/service/service.go:170-198`）
- 选项: `WithMaxUploadBytes`、`WithAllowedMimeTypes`、`WithFileAuditLogger`、`WithFileShareRepo`、`WithStorageRefs`
- **生产接入:** `internal/app/app.go:238-245` 组合并注入

```go
// internal/app/app.go:238-245 — 实际接的是 service 包
fileSvc := service.NewFileService(fileRepo, store, cfg.Storage.Type,
    service.WithMaxUploadBytes(cfg.Storage.MaxUploadBytes),
    service.WithAllowedMimeTypes(cfg.Storage.AllowedMimeTypes),
    service.WithFileShareRepo(fileShareRepo),
    service.WithFileAuditLogger(auditRepo),
    service.WithStorageRefs(storageRefRepo),
)
```

**实现 B（仅 search 使用）** — `internal/file/` 包（独立 7 个文件）

| 文件 | 行数 | 内容 |
|------|------|------|
| `internal/file/file.go` | 111 | `Service` 接口 + `fileService` 结构体 + 选项 |
| `internal/file/service_crud.go` | 168 | CRUD 操作（与实现 A **重复的逻辑**） |
| `internal/file/service_share.go` | 86 | 文件共享 |
| `internal/file/service_upload.go` | 104 | 预签名上传 |
| `internal/file/repository.go` | 86 | 仓库接口定义 |
| `internal/file/repository_share.go` | 89 | 共享仓库实现 |
| `internal/file/repository_integration_test.go` | - | 集成测试 |

**生产接入校验:** 搜索

```bash
$ grep -rn "github.com/aero/aero-id/internal/file" --include="*.go" | grep -v "_test.go" | grep -v "\.pb\."
internal/search/handler_grpc.go:10  # 仅 search 导入
internal/search/search.go:8        # 仅 search 导入
```

**`internal/file/` 包只有 search 模块导入它。** 所有 HTTP/gRPC handler、app.go 组合根都使用 `internal/service/`。

### 影响分析

| 问题 | 具体表现 |
|------|---------|
| **逻辑分歧风险** | 两套实现各自演化。假如修复了 A 的某个 bug 但忘记同步到 B，search 查询行为会与 handler 行为不一致 |
| **接口不一致** | 实现 A 的 `WithStorageRefs` 在实现 B 中不存在（`internal/file/file.go` 选项列表无此选项）。未来新增功能需要在两个地方实现 |
| **重复测试成本** | 每个 bug 修复需要在两套代码上验证 |
| **入坑成本** | 新开发者看到 `internal/file/` 和 `internal/service/file.go`，不知道哪个是"正确的" |

`internal/comment/` 和 `internal/message/` 包也存在同样的双重结构，同样仅被 search 使用。

### 建议

1. **短期**：确认 `internal/service/` 中的三个文件服务是权威实现。在 `HARNESS.md` 或 `AGENTS.md` 中添加架构规则：`internal/file/`、`internal/comment/`、`internal/message/` 是 **仅 search 使用的视图层**，所有业务逻辑必须走 `internal/service/`。
2. **中期**：制定迁移计划将 `internal/service/` 中的逻辑逐步迁移到 feature 包，或者反过来——消除两套实现。搜索应调用 `service.FileService` 接口而不是直接导入 `internal/file/`。
3. **可执行检查**：添加 `make check-no-duplicate-file-service` 脚本，确保两端的关键方法签名未漂移。

---

## 方向二：通知折叠（Collapse）产生幽灵通知行

### 性质

**高 · 正确性缺陷 · 用户可见 Bug** — 当前行为与设计意图不一致

### 问题描述

通知折叠功能的目的是将同一类型、同一 actor 在短时间内（默认 5 分钟）的多个通知合并为一行，用 `collapsed_count` 记录合并次数。但实现中有一个 **先创建、再折叠** 的顺序问题，导致每次折叠都在数据库中留下一个**不应存在的"幽灵"通知行**。

### 代码级证据

`internal/service/notification.go:230-260` 的 `Handle` 方法：

```go
// 第 1 步：无条件创建通知行
if err := d.notifs.Create(ctx, n); err != nil {
    return err
}

// 第 2 步：检查是否需要折叠（此时已创建了 n）
if d.collapseWin > 0 {
    existing, err := d.notifs.FindRecentByTypeAndActor(
        ctx, n.RecipientUID, n.Type, n.ActorUID, d.collapseWin)
    if err == nil && existing != nil && existing.ID != n.ID {
        // 在第 1 步创建的 n 之上，递增已存在的行的计数
        _ = d.notifs.IncrementCollapsedCount(ctx, existing.ID)
        // 推送更新后的 existing 到 SSE
        if d.pusher != nil {
            existing.CollapsedCount++
            existing.CreatedAt = time.Now().UTC()
            payload, _ := json.Marshal(existing)
            d.pusher.Publish(n.RecipientUID, n.Type, string(payload),
                existing.CreatedAt.UnixMilli())
        }
        return nil  // ← 返回成功，但新行 n 留在数据库中
    }
}
```

**问题链：**
1. `Create` 在数据库中写入一行 `n`（collapsed_count=1, read_at=NULL）
2. `FindRecentByTypeAndActor` 发现已存在 `existing`（5 分钟内同一类型+actor）
3. 递增 `existing.collapsed_count`，推送 `existing` 到 SSE
4. **`n` 没有从数据库中删除**

**结果：** 用户收到的通知列表中包含：
- `existing`（collapsed_count=2，显示了正确的合并计数）
- `n`（collapsed_count=1，作为一条单独的"假"未读通知）

### 影响分析

| 场景 | 用户看到 | 期望 |
|------|---------|-----|
| Alice 连续发送 3 条消息给 Bob | Bob 收到 4 条未读通知：1 条 collapsed_count=3 + 2 条多余幽灵行 | 1 条 collapsed_count=3 |
| 5 分钟内 10 条 comment.reply | 11 条通知，其中 1 条 collapsed_count=10 + 9 条幽灵行 | 1 条 collapsed_count=10 |
| 每 5 分钟推送一次的用户 | 未读数 = 通知数 × 2 | 未读数 = 通知数 |

### 建议

两个修复方案：

**方案 A（推荐）**：在折叠命中时删除刚创建的行
```
Order:
  1. FindRecentByTypeAndActor 先运行
  2. 如果命中 → 直接 IncrementCollapsedCount + push，不 Create
  3. 如果未命中 → Create
```

**方案 B**：保留 Create 但折叠命中后立即 HardDelete 新行。需要一个额外的 `Delete` 操作但保持代码顺序改动最小。

无论哪种方案，都需要为 `NotificationRepository` 添加 `Delete` 方法并编写测试验证 `collapsed_count` 与列表行数的一致性。

---

## 方向三：gRPC 传输层缺少幂等键——写操作语义不对等

### 性质

**高 · API 契约一致性 · 生产缺陷**

### 问题描述

HTTP 传输层通过 `middleware.Idempotency` 中间件提供完整的幂等键支持（`Idempotency-Key` 头 → 原子性 Reserve → Complete/Release 操作）。但 **gRPC 传输层完全没有等幂性支持**。这意味着同一个业务的写操作（发消息、创建评论、举报）在不同传输层上拥有不同的可靠性保证。

### 代码级证据

**HTTP 幂等键实现（存在）：**

| 位置 | 组件 | 功能 |
|------|------|------|
| `internal/middleware/idempotency.go` | Gin 中间件 | 拦截 `POST + Idempotency-Key`，Reserve → 执行 → Complete/Release |
| `internal/handler/http/router.go:68-70` | 路由注册 | `cfg.Idempotency != nil` 时插入中间件链 |
| `internal/idempotency/store.go` | 存储层 | `GormStore`：Postgres 持久化，含 Reserve 过期回收（2min）和定时清理 |
| `internal/app/app.go:73` | 组合 | `idemStore := idempotency.NewGormStore(gdb)` |
| `internal/app/app.go:129-130` | 注入 | `Idempotency: idemStore, IdempotencyTTL: cfg.Server.HTTP.IdempotencyTTL` |

**gRPC 幂等键（完全缺失）：**

```bash
$ grep -rn "idempot\|Idempot" internal/handler/grpc/ --include="*.go"
# 空结果 — gRPC handler 层零等幂性代码
$ grep -rn "idempot\|Idempot" internal/pkg/ --include="*.go"
internal/pkg/errors/errors.go      # 无相关代码
# gRPC 无等幂中间件、无等幂拦截器、无 proto 元数据
```

gRPC 的 `server.go` 的拦截器链包含 `TracingInterceptor`、`MetricsInterceptor`、`RecoveryInterceptor`、`AuthInterceptor`、`RateLimitInterceptor`，但**没有 `IdempotencyInterceptor`**。

### 影响分析

| 场景 | HTTP 行为 | gRPC 行为 |
|------|---------|----------|
| 客户端因网络超时自动重试 POST | 幂等键保障：仅第一执行，后续回放响应 | ⚠️ 没有保障：可能重复创建资源 |
| 服务端 500 后客户端重试 | 幂等层 Release → 允许重新执行 | ⚠️ 无法区分"上次失败的重试"和"新请求" |
| 客户端同时发送 2 个相同 POST | 幂等层 409 Conflict（in_progress） | ⚠️ 同时执行，可能重复写入 |

当前 proto 定义（`api/proto/aero_id/v1/aero_id.proto`）中，所有 mutating RPC 的请求消息都没有 `idempotency_key` 字段。

### 建议

1. 在 `api/proto/aero_id/v1/aero_id.proto` 的 mutating RPC 请求消息中添加 `idempotency_key` 字段（可选字符串）
2. 实现 `IdempotencyInterceptor`（gRPC UnaryServerInterceptor），与 HTTP 中间件共享 `idempotency.Store` 接口
3. 拦截器键生成：`uid + fullMethod + idempotency_key`
4. 因为 gRPC 的 `Complete` 回放需要响应序列化，可考虑简化为 **至少一次语义 + 应用层去重**（通过 `idempotency_key` 唯一约束），比 HTTP 的"完全回放"更简单

---

## 方向四：Outbox 死信不可见——无管理面无法运维

### 性质

**中高 · 运维盲区 · 可恢复性缺口**

### 问题描述

outbox relay 将超过 `maxRetries` 次失败的事件标记为 `dead_letter = true`，之后 relay 的 `FetchUnpublished` 查询（`WHERE published_at IS NULL AND NOT dead_letter`）会跳过这些事件。但当前**没有任何 API 能让运维人员查看、重新入队、或清除死信事件**。它们只是在数据库中无声积累。

### 代码级证据

**死信标记路径**：

`internal/outbox/relay.go:123-130`：
```go
if err := r.repo.MarkFailed(ctx, e.ID, e.RetryCount+1, r.maxRetries, now); err != nil {
    r.log.Warn("outbox: mark failed failed", ...)
}
```

`internal/outbox/repository.go`（MarkFailed 实现）：
```go
// 当 retryCount >= maxRetries 时设置 dead_letter = true
```

**跳过查询**（来自迁移 `000018_outbox_retry.up.sql`）：
```sql
CREATE INDEX idx_outbox_unpublished
    ON outbox_events(created_at, id)
    WHERE published_at IS NULL AND NOT dead_letter;
```

**缺失的管理面**：

```bash
# 搜索 admin handler 中的 outbox 相关代码
$ grep -rn "dead_letter\|DeadLetter\|outbox.*event\|outbox_event" internal/handler/ --include="*.go"
internal/handler/grpc/ops.go     # 只有 Health + Reflection，无 outbox 操作
internal/handler/http/admin.go   # 只有 ListAudit，无 outbox 死信查询
```

| 缺失的操作 | 必要性 |
|-----------|--------|
| 查询死信列表（分页） | 需要知道哪些事件永久失败了 |
| 重新入队死信（设置 dead_letter=false） | Webhook 端点修复后恢复失败事件 |
| 清除死信（DELETE） | 控制死信表大小 |
| 按 event_type 过滤 | 快速定位特定类型的事件（如 webhook 事件）|

### 影响分析

- 死信事件中可能包含**关键业务数据**（如消息通知、评论回复通知），无法恢复意味着用户永久错过通知
- 一个配置错误（如错误的 webhook URL）可能导致大量死信积累，占存储且无法清理
- 没有死信告警，运维人员不知道 outbox 正在丢事件

### 建议

1. **HTTP 端点**：在 `AdminHandler` 中扩展：
   - `GET /v1/admin/outbox/dead-letters?cursor=X&limit=20` — 分页查询死信
   - `POST /v1/admin/outbox/dead-letters/:id/requeue` — 重新入队单条
   - `POST /v1/admin/outbox/dead-letters/requeue-all` — 全部重新入队
   - `DELETE /v1/admin/outbox/dead-letters/:id` — 清除单条
2. **gRPC 端点**：`admin.OutboxAdminService` 中的对应 RPC
3. **审计日志**：所有死信操作写入 `audit_events` 表
4. **告警指标**：在 `metrics` 注册死信计数 gauge，当 `dead_letter > 0` 时触发告警

---

## 方向五：SSE Hub 是单实例的——多副本部署时实时推送断裂

### 性质

**高 · 架构演进阻塞 · 水平扩展阻力**

### 问题描述

SSE Hub（`internal/hub/hub.go`）完全基于进程内内存：每个连接订阅一个 Go channel，通知 dispatcher 通过 `Publish` -> `hub.pub` 直接写入 channel。这在单实例部署下工作完美，但在多副本部署下，**只有连接到事件产生所在实例的用户才能收到实时推送**。其他实例上的用户只能等待下一次 outbox relay tick（默认 5s）→ 从 notifications 表拉取 → 或者等客户端下一次 SSE 重连时的 `last_event_id` 回放。

### 代码级证据

`internal/hub/hub.go:65-80`：
```go
type Hub struct {
    subs   sync.Map // uid → *uidSubs (进程内 map)
    nextID atomic.Int64
    closed atomic.Bool
}

func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
    // 注册到当前进程的 map
    entry, _ := h.subs.LoadOrStore(uid, &uidSubs{subs: make(map[int64]chan SSEEvent)})
    // ...
    return ch, unsub
}

func (h *Hub) pub(uid string, evt SSEEvent) {
    raw, ok := h.subs.Load(uid)
    if !ok {
        return // 用户不在当前进程 → 事件静默丢失
    }
    // ... 写入 channel
}
```

组合路径 (`internal/app/app.go`):
```go
sseHub := hub.New()                              // 一个单例 Hub
dispatcher := service.NewNotificationDispatcher(  // 同一个 dispatcher 持有 hub
    notifRepo, ..., service.WithDispatcherPusher(sseHub),
)
```

| 部署场景 | 11:00:00 事件在实例 A 产生 | 11:00:00 用户在实例 B 上 |
|---------|--------------------------|------------------------|
| 单实例 | ✅ 即时推送 | ✅ 即时推送 |
| 多实例（2+） | ✅ 推送实例 A 上该用户的连接 | ❌ 实例 B 上该用户的连接收不到推送 |
| 客户端断线重连 | ✅ `last_event_id` 回放 | ✅ `last_event_id` 回放（但有延迟+可能重复）|

### 影响分析

- **用户体验降级**：在多副本部署下，用户依赖轮询/断线重连机制获取通知，实时性从 <100ms 退化到 5s+（outbox poll interval）
- **不可预测性**：用户连接恰好落在哪个实例上决定了他能否收到实时推送
- **扩展瓶颈**：当前架构无法通过增加副本数来提升 SSE 连接容量（因为 SSO 连接绑定到进程）
- **已有基础设施未利用**：配置中已有 `outbox.webhook.url`，意味着团队已有事件分发到外部端点的能力——但 SSE 跨实例协调完全没有走这条路径

### 建议

三种架构方案（按复杂性递增）：

**方案 A：使用 outbox webhook 做 SSE 跨实例广播**
- outbox relay 已经能把事件推送到外部 webhook URL
- 引入一个轻量级的"实例间 SSE 总线"（Redis Pub/Sub 或 NATS），所有实例订阅同一个 channel
- 每个实例的 notification dispatcher 在创建通知后，将事件发布到 Redis Pub/Sub
- 每个实例的 SSE Hub 订阅 Redis，收到跨实例事件后推送到本地的 subscriber channel
- 复杂度：**中**。依赖 Redis（但多数部署已有）

**方案 B：轮训退化（无状态降级）**
- 移除 SSE 的进程内推送，让所有客户端依赖 outbox poll + `last_event_id` 回放
- 架构更简单、无状态，但实时性从 <100ms 降到 5s+ (outbox poll interval)
- 复杂度：**低**。适合不追求毫秒级实时的场景

**方案 C：gRPC 双向流替代 SSE（多实例原生支持）**
- 用 gRPC 双向流 + envoy/grpc-gateway 替换 SSE
- 利用 gRPC 的负载均衡感知（如 `round_robin`），每个连接绑定到后端实例
- 但跨实例推送问题仍然存在（只是换了一层协议）
- 复杂度：**高**。需要更换前端协议、重写 handler、重新设计断线重连

**推荐方案 A**：利用已有的 Redis（如果部署中已有）或 outbox webhook 基础设施，添加跨实例事件总线。

---

## 总结：优先级与影响矩阵

| # | 方向 | 类型 | 优先级 | 影响面 | 估算 |
|---|------|------|--------|--------|------|
| 1 | 双重文件服务实现（架构分裂） | 架构债务 | **P1** | 长期维护风险、开发效率 | 中（标记+文档）到高（迁移） |
| 2 | 通知折叠产生幽灵行 | 正确性 Bug | **P0** | 所有启用 collapse 的用户 | 低（改动 2 行） |
| 3 | gRPC 无幂等键 | API 契约不一致 | **P1** | 所有 gRPC 写入端用户 | 中 |
| 4 | Outbox 死信不可见 | 运维盲区 | **P1** | 生产 outbox 可靠性 | 中 |
| 5 | SSE Hub 单实例 | 架构演进阻塞 | **P1** | 多副本部署场景 | 高 |

---

## 补充：建议立即修复的 2 个低成本问题

### 1. 通知折叠幽灵行的修复

这是最**低成本高价值**的修复——只涉及 `service/notification.go` 中两行代码顺序的调整，不需要 schema 变更，不需要新端点。但影响所有使用了通知折叠功能的用户。

### 2. 在 `AGENTS.md` 中添加架构分裂规则

在 `AGENTS.md` 的 `Invariants` 段中添加一条：

> **`internal/file/`、`internal/comment/`、`internal/message/` 包是仅 search 使用的视图层**，所有业务逻辑必须走 `internal/service/`。任何业务逻辑的修改必须在 `internal/service/` 中进行，**必须同步** 对应 feature 包的 search 接口（如有变更）。

这将立即防止两套代码持续漂移。
