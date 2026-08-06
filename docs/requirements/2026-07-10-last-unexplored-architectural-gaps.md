# aero-id — 第十轮全局代码扫描：五个迄今未被系统性覆盖的架构层面缺口

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全量代码扫描（`internal/` 全部 22+ 个包、120+ 源文件、22 份迁移、`cmd/`、`configs/`、`api/proto/`），
>   与 `docs/requirements/` 下 **64 份**既有分析文档、`docs/REVIEW_*` 下 **8 份**审查文档、`ROADMAP.md` 逐条交叉比对。
>   累计已覆盖 ~120+ 个扩展/优化方向后，定位以下 5 个**在所有既有分析中零系统性覆盖**的方向。
> **约束**: 不编写任何代码。每个方向附带精确代码位置和可操作的下一步。
> **前置条件**: `feat/roadmap` 已合并；所有 ROADMAP 方向、生产加固全部交付。

---

## 交叉验证声明

本文定位的 5 个方向均满足：在所有 64 份 `docs/requirements/*.md` + 8 份 `docs/REVIEW_*` + `ROADMAP.md` 的逐条比对中，
**既无标题级匹配，亦无系统性正文分析**。每个方向都是在代码中真实存在、但被所有此前分析遗漏的结构性问题。

---

## 方向一：双栈实施——7 个功能包（11,798 行）在零生产引用中

### 性质

**架构债务 · 维护风险 · 测试幻觉（高）**

### 问题描述

代码库中存在两套独立且完整的 handler→service→repository 堆栈，但只有一套（旧栈）被 `app.go` 实际引用。
新栈占用了近 **12,000 行**生产编译代码，通过所有测试，但**从未在生产路径中执行过一行**。

这意味着：
- 对新栈的 bug 修复只存在于新栈——生产路径中的 bug 不受影响
- 对旧栈的 bug 修复需要**人工同步**到新栈，反之亦然
- 新栈的测试覆盖的是**不会运行的代码**，给 CI 绿灯提供了虚假信心

### 代码级证据

**证据 1：`app.go` 引用的包路径**

```go
// internal/app/app.go:29-30
"github.com/aero/aero-id/internal/repository"   // 旧栈
"github.com/aero/aero-id/internal/service"       // 旧栈
```

生产代码中**不存在**以下引用：

```go
"github.com/aero/aero-id/internal/comment"       // 未引用
"github.com/aero/aero-id/internal/file"           // 未引用
"github.com/aero/aero-id/internal/message"        // 未引用
"github.com/aero/aero-id/internal/block"          // 未引用
"github.com/aero/aero-id/internal/notification"   // 未引用
"github.com/aero/aero-id/internal/search"         // 未引用
"github.com/aero/aero-id/internal/audit"          // 未引用
```

验证命令：`grep -r '"github.com/aero/aero-id/internal/comment"' --include="*.go" | grep -v "_test.go"` → 零非测试结果。

**证据 2：代码量分拆**

| 包路径 | 行数 | 是否被 `app.go` 引用 | 状态 |
|--------|------|---------------------|------|
| `internal/comment/` | 1,986 | ❌ | 死代码 |
| `internal/file/` | 3,106 | ❌ | 死代码 |
| `internal/message/` | 2,312 | ❌ | 死代码 |
| `internal/block/` | 1,102 | ❌ | 死代码 |
| `internal/notification/` | 1,848 | ❌ | 死代码 |
| `internal/search/` | 1,186 | ❌ | 死代码 |
| `internal/audit/` | 258 | ❌ | 死代码 |
| **合计** | **11,798** | — | ~32% of `internal/` |

**证据 3：`NotificationDispatcher` 存在两个完全独立的实现**

```go
// internal/service/notification.go — 接线版本 ✅
type NotificationDispatcher struct { ... }
func NewNotificationDispatcher(...) *NotificationDispatcher

// internal/notification/dispatcher.go — 死代码版本 ❌
type Dispatcher struct { ... }
func NewDispatcher(...) *Dispatcher
```

两个结构体都实现 `outbox.Handler` 接口，但各有各的选项函数体系
（`WithDispatcherBlockChecker`、`WithDispatcherPusher` 等），签名不完全相同。
仅一个版本在生产中运行——修复其中任意一个的 bug 不会影响另一个。

**证据 4：`hub.Pusher` vs `service.NotificationPusher`——两个类似接口**

```go
// internal/hub/hub.go:133 — 新栈接口（3 个参数）
type Pusher interface { Publish(uid, eventType, data string) }

// internal/service/notification.go:52 — 旧栈接口（4 个参数，含 eventID）
type NotificationPusher interface { Publish(uid, eventType, data string, eventID int64) }
```

各自绑定不同的 Dispatcher 实现。适配桥梁是 `internal/hub/adapter.go`，增加了不必要的间接层。

### 影响分析

| 风险 | 等级 | 说明 |
|------|------|------|
| **测试幻觉** | 🔴 高 | 新栈的测试全部通过，但测试的对象在生产中不运行。CI 绿灯不等于生产正确性 |
| **修复遗漏** | 🔴 高 | 开发者修复旧栈的 bug 时，不知道（或忘记）需要同步到新栈。6 个月后两套实现的逻辑差异将与 git log 无关 |
| **新人困惑** | 🟡 中 | 新工程师阅读 `internal/` 会看到两套完整的 handler→service→repository 堆栈，无法判断哪一套是生产运行的 |
| **迁移成本** | 🟡 中 | 如果要切换到新栈，需要审计新栈是否覆盖了旧栈的全部功能。`search` 包引用新栈的 repository 接口（而非旧栈的），意味着切换前需要对齐接口定义 |

### 建议的迁移路径

```
Phase 1 — 审计（2-3 天）
  1. 逐包对比新栈和旧栈的功能清单（service 方法、handler 路由、repository 查询）
  2. 记录新栈缺少的功能（若有）
  3. 为每个新包建立功能覆盖矩阵

Phase 2 — 统一（3-5 天）
  1. 选择目标架构：旧栈 → 新栈（领域驱动） 或 新栈 → 旧栈（扁平分层）
  2. 向 app.go 逐个引入新包的 service 实现
  3. 移除旧包中重复的 handler/service/repository
  4. 每迁移一个功能域，运行 `make test && make test-integration`

Phase 3 — 清理（1 天）
  1. 删除未被引用的旧包文件
  2. 删除 `internal/domain/` 中可能被遗留的重复类型
  3. 更新 AGENTS.md 中的包布局描述
```

---

## 方向二：幂等键（Idempotency-Key）缺少请求体一致性校验

### 性质

**数据完整性 · 防误用（中）**

### 问题描述

当前幂等性中间件在收到携带 `Idempotency-Key` 的重试请求时，会直接重放存储的响应——
**不验证重试请求的请求体是否与原始请求一致**。这意味着：

1. 客户端发送 `POST /v1/messages` 内容为 "Hello" → 成功存储
2. 客户端（编程错误或网络库自动重试）用同一 key 发送 "Goodbye" → **得到第一条消息的响应**
3. 客户端认为 "Goodbye" 已发送，但实际存储的是 "Hello"

幂等性保证了**写一次（exactly-once）**，但不能保证**重试的内容是预期的内容**。
客户端可能产生沉默的数据不一致。

### 代码级证据

```go
// internal/middleware/idempotency.go:27-70
func Idempotency(store idempotency.Store, ttl time.Duration) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := c.GetHeader("Idempotency-Key")
        if c.Request.Method != http.MethodPost || key == "" {
            c.Next()
            return
        }
        uid := c.GetString("actor_uid")
        endpoint := c.Request.Method + " " + c.FullPath()
        ctx := c.Request.Context()

        rec, reserved, err := store.Reserve(ctx, uid, endpoint, key)
        switch {
        case err != nil:
            c.Next() // fail open
        case reserved:
            // ...首次请求：处理并存储响应
        case rec.Completed:
            replayIdempotent(c, rec) // ← 无条件重放，不检查请求体
        default:
            // 并发冲突处理
        }
    }
}
```

`replayIdempotent` 函数（同文件 90-96 行）只注入 `Idempotent-Replayed: true` 头部后发送存储的响应。
对传入的请求体**不做任何检查**。

对比 Stripe 的幂等性语义——Stripe 要求 `Idempotency-Key` 对特定请求体绑定：
相同的 key + 不同的请求体 → `409 Conflict`（而非静默重放）。

### 影响分析

| 场景 | 当前行为 | 预期行为 |
|------|---------|---------|
| 同 key + 同 body | 幂等重放 ✅ | ✅ |
| 同 key + **不同** body | **静默重放旧响应** ❌ | 应返回 409（幂等 Key 冲突） |
| 同 key + 同 endpoint + 不同 uid | 无冲突（key 按 uid 分片） ✅ | ✅ |

### 建议修复

在 `replayIdempotent` 分支前，计算传入请求体的 SHA256 摘要，与存储的 `body_hash`
（需要扩展 `idempotency.Record` 结构体）比较。不匹配时返回 `409 Conflict`，
错误码 `idempotency.key_mismatch`。

需要：
- `internal/pkg/idempotency/`：`Record` 增加 `BodyHash string`
- `internal/middleware/idempotency.go`：首次请求时计算并存储 `BodyHash`；重放时比较
- 迁移 000004：`idempotency_keys` 表增加 `body_hash` 列

---

## 方向三：全部后台定时任务使用 `context.Background()` 且无超时传播

### 性质

**运维韧性 · 优雅关闭可靠性（中-高）**

### 问题描述

aero-id 有 4 个后台定时任务（outbox relay、storage GC sweeper、idempotency 过期清理、
rate limiter 空闲桶清理），全部使用 `context.Background()` （而非可超时的派生 context）。
这意味着在极端条件下（DB 连接池耗尽、存储后端无响应），这些任务可能**无限期阻塞**，
并在关闭时阻止 goroutine 正常退出。

### 代码级证据

**证据 1：Outbox relay — 定时轮询和关闭 drain 均使用 Background**

```go
// internal/outbox/relay.go:163-171
func (r *Relay) Start(interval time.Duration) (stop func()) {
    go func() {
        ctx := context.Background()             // ← Background, 无超时
        for {
            select {
            case <-t.C:
                r.drain(ctx)
            case <-done:
                t.Stop()
                r.drain(context.Background())   // ← 关闭时的 drain 也是 Background
                return
            }
        }
    }()
}
```

如果 `drain` 中的 `repo.FetchUnpublished` 或 `handler.Handle` 调用卡住（例如 DB 无响应），
`stop()` 会被**阻塞到操作完成**。`app.shutdown()` 中的 `stopRelay()` 调用将无法按期返回。

**证据 2：Storage GC sweeper — Start 同样使用 Background**

```go
// internal/storage/gc/sweeper.go:231-243
func (s *Sweeper) Start(interval time.Duration) (stop func()) {
    go func() {
        ctx := context.Background()             // ← Background, 无超时
        for {
            select {
            case <-t.C:
                _, _ = s.Run(ctx)               // 无超时的清除操作
            case <-done:
                t.Stop()
                return
            }
        }
    }()
}
```

Sweeper 的关闭甚至**不做最后一次 drain**（与 relay 不同），直接退出。
如果 `Run` 正在执行并卡住，`t.Stop()` 不中断它——goroutine 泄露。

**证据 3：Idempotency cleaner — 使用 Background 且错误吞没**

```go
// internal/idempotency/store.go:177-192
func (s *GormStore) purgeExpired() {
    _ = s.db.Where("expires_at < ?", time.Now().UTC()).Delete(&idempotencyRow{}).Error
    // 内部使用 GORM 的默认 context（Background），且错误被 `_` 吞没
}
```

**证据 4：Rate limiter cleaner — 同理**

```go
// internal/pkg/ratelimit/ratelimit.go:114-127
func (l *Limiter) RunCleaner(interval time.Duration) (stop func()) {
    go func() {
        for {
            select {
            case <-t.C:
                l.sweepIdle(l.now())   // 纯内存操作，不会阻塞——此例合规
            }
        }
    }()
}
```

| 后台任务 | 位置 | 是否在 Stop 处做 drain | 超时 | 错误处理 |
|---------|------|----------------------|------|---------|
| Outbox relay | `internal/outbox/relay.go:163` | ✅（但使用 Background） | ❌ | ✅ |
| Storage GC sweeper | `internal/storage/gc/sweeper.go:231` | ❌ | ❌ | 仅 log |
| Idempotency cleaner | `internal/idempotency/store.go:169` | N/A | ❌ | 吞没 |
| Rate limiter cleaner | `internal/pkg/ratelimit/ratelimit.go:114` | N/A | N/A（纯内存） | N/A |

### 影响分析

在健康状态下，这些操作都很快速，Background 不是问题。但在退化状态下（DB 连接池耗尽、
存储后端超时），`shutdown()` 中的 10s `ShutdownTimeout` 可能不足以让所有后台
goroutine 完成。残留的 goroutine 在进程退出后才会消失——但如果使用 `http.Server.Shutdown`
加超时后调用 `os.Exit`，未完成的 DB 操作可能导致连接泄漏。

### 建议修复

为每个后台任务的主循环注入可 timeout 的 context：

```
Phase 1 — 为每个后台操作定义一个 package-level timeout 常量（如 30s）
Phase 2 — 在 Start 的 goroutine 中创建 `ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)`
Phase 3 — 在 Stop 的 drain 路径使用更短的 timeout（如 10s）
Phase 4 — 对于 sweeper，在 Stop 中也做一次 drain（与 relay 保持一致）
Phase 5 — Idempotency cleaner 使用 `db.WithContext(ctx)` 传递超时
```

---

## 方向四：SSE Hub 零连接级可观测性——操作者无法感知用户是否在丢失事件

### 性质

**可观测性盲区（中）**

### 问题描述

SSE Hub 支持多用户、多设备连接，通道缓冲区为 16 且非阻塞发送。但操作者**完全无法观测**：

- 当前有多少活跃连接（总连接数 / 每 uid 连接数）
- 事件丢弃率（当消费者慢时 silent drop）
- 事件吞吐量（每秒推送多少事件）
- 连接持续时间分布

这意味着：当用户抱怨收不到实时推送时，SRE/开发者没有仪表盘可以排查——必须通过
代码走读猜测问题是否出在 hub 层。

### 代码级证据

```go
// internal/hub/hub.go:35-38
type Hub struct {
    subs   sync.Map // uid → *uidSubs
    nextID atomic.Int64
    closed atomic.Bool
    // ← 没有任何指标计数器: activeConns, eventsPushed, eventsDropped, ...
}
```

```go
// internal/hub/hub.go:105-110 — pub 方法静默丢弃
func (h *Hub) pub(uid string, evt SSEEvent) {
    // ...
    for _, ch := range us.subs {
        select {
        case ch <- evt:
            // 成功推送 — 不可观测 ✅←但无计数器
        default:
            // 消费者太慢 — 静默丢弃 ❌←无计数器、无 log
        }
    }
}
```

唯一的可观测性路径是 HTTP handler 的 `access_log` ——它记录请求级别的信息，
但无法反映 hub 内部的实时推送质量。

### 对比其他可观测性模块

aero-id 的其他基础设施模块都有良好的指标出口：

| 模块 | 指标 | 位置 |
|------|------|------|
| SSO client | 缓存命中率、上游延迟、错误率 | `internal/sso/client.go`（通过 `Metrics` 接口） |
| Outbox relay | published/failed 计数、lag 延迟 | `internal/outbox/relay.go:71-78` |
| HTTP/gRPC | RED 指标 | `internal/middleware/metrics.go` |
| **SSE Hub** | **无** | ❌ |

### 建议修复

在 `Hub` 结构体中增加：

```go
type Hub struct {
    // ... 现有字段
    metrics struct {
        activeSubscriptions atomic.Int64   // 当前活跃连接数
        eventsPushed        atomic.Int64   // 累计推送事件数
        eventsDropped       atomic.Int64   // 累计丢弃事件数（slow consumer）
        bytesPushed         atomic.Int64   // 累计推送字节数
    }
}
```

在 `Subscribe`/`unsub` 时更新 `activeSubscriptions`，在 `pub` 的成功和丢弃路径
分别更新 `eventsPushed`/`eventsDropped`。将这些值暴露到 `/metrics` 端点
（通过注册到 `metrics.Registry` 的 gauge 和 counter）。

---

## 方向五：架构层约定无自动化验证——handler→service→repository 方向靠人工审查保证

### 性质

**治理风险（中）**

### 问题描述

AGENTS.md 明确定义架构方向为：

```
handler/{http,grpc}  →  service  →  repository  →  Postgres (GORM)
```

且已定位两个**已存在**的违反实例：

```go
// internal/app/app.go:308
httpapi.NewEventsHandler(sseHub, notifRepo)     // ← handler 直接接收 repository
httpapi.NewAdminHandler(adminRepo)               // ← handler 直接接收 repository
```

但没有任何自动化工具阻止新的违反引入。CI 通过的检查包括 `go vet`、`funlen`、`cyclop`、
`check-file-sizes`，但**不检查导入方向或者包依赖**。

### 代码级证据

**证据 1：违反实例 1——EventsHandler 绕过 service**

```go
// internal/handler/http/events.go:14-16
type EventsHandler struct {
    hub     *hub.Hub           // OK — hub 是基础设施
    fetcher MissedNotifFetcher // ← 具体类型是 repository.NotificationRepository
}

// internal/app/app.go:308
httpapi.NewEventsHandler(sseHub, notifRepo) // notifRepo 是 repository.NotificationRepository
```

EventsHandler 直接使用 repository 的 `ListSince` 方法——没有经过 service 层的
权限校验、用户富化或错误包装。

**证据 2：违反实例 2——AdminHandler 绕过 service**

```go
// internal/handler/http/admin.go:10-18
type AdminHandler struct {
    adminRepo repository.AdminRepository // ← handler 直接持有 repository
}

func (h *AdminHandler) ListAudit(c *gin.Context) {
    // 权限检查直接在 handler 中做：↓
    allowed, err := h.adminRepo.HasPermission(c.Request.Context(), actor, "audit.read")
    // ...
    events, next, err := h.adminRepo.ListAudit(...) // ← handler 直接调 repository
}
```

架构规则说"所有业务逻辑 + 授权在 service 层"。这里授权和查询都在 handler 中。

**证据 3：CI 配置中没有架构约束检查**

```yaml
# .golangci.yml（部分）
linters:
  enable:
    - funlen      # 函数长度 ✓
    - cyclop      # 圈复杂度 ✓
    # ... 没有 import 方向检查 ❌
    # ... 没有包级别依赖规则 ❌
```

`HARNESS.md` 定义了 `check-file-sizes` 和 `lint-strict` 脚本，但均不检查
`internal/` 包之间的导入关系。

### 影响分析

| 风险 | 等级 | 说明 |
|------|------|------|
| **架构退化** | 🟡 中 | 新的 handler 开发者可以不经审查地导入 repository 包 |
| **已有违反固化** | 🟡 中 | EventsHandler 和 AdminHandler 的违反不会被 CI 发现或阻止 |
| **新人认知负担** | 🟡 中 | 新成员阅读 AGENTS.md 的架构图，可能与实际代码冲突 |
| **双栈迁移风险** | 🟡 中 | 迁移 Phase 2 中如果误引用了新栈的 repository 接口，可能引入隐形依赖 |

### 建议修复

```bash
# Phase 1 — 在 scripts/ 中添加 check-architecture.sh
# 规则：internal/handler/{http,grpc} 不能导入 internal/repository
# 规则：internal/service 不能导入 internal/handler

# Phase 2 — 在 CI 中运行此检查（.github/workflows/ci.yml）
# 添加到 lint 步骤之后

# Phase 3 — 修复已知违反
# - EventsHandler: 改为接收 service.NotificationService（增加 ListSince 方法到 service 接口）
# - AdminHandler: 改为接收 service.AdminService（或在 service 包中增加 AdminService）
```

---

## 优先级与建议的采纳顺序

| 方向 | 类型 | 建议优先级 | 估算工作量 | 依赖 |
|------|------|-----------|-----------|------|
| **方向三**：后台任务使用 `context.Background()` | 运维韧性 | **P0** | 低（1-2 天） | 无 |
| **方向二**：幂等键请求体一致性校验 | 数据完整性 | **P1** | 低（1-2 天） | 无 |
| **方向一**：双栈合并 | 架构债务 | **P1** | 高（5-8 天） | 需要团队对齐目标架构 |
| **方向五**：CI 架构约束自动化 | 治理 | **P1** | 低（1 天） | 无 |
| **方向四**：SSE Hub 可观测性 | 可观测性 | **P2** | 低（1 天） | 无 |

### 快速修复建议（无需完整投入方向即可做的）

1. **为 outbox relay 关闭 drain 增加超时** —— `internal/outbox/relay.go:170`，
   将 `context.Background()` 替换为 `context.WithTimeout(context.Background(), 10*time.Second)`
2. **为 sweeper 关闭增加 drain** —— `internal/storage/gc/sweeper.go:240`，
   在 `done` 分支调用一次 `s.Run(context.WithTimeout(context.Background(), 30*time.Second))`
3. **检查 `internal/audit/` 是否可安全移除** —— 该包无生产引用，可直接删除
4. **为 EventsHandler 创建 service 层接口** —— 在 `service` 包中增加
   `ListSince(ctx, recipientUID, since, limit)` 方法 → 消除一个已知的架构违反

---

## 附录：与既有分析的交叉验证清单

| # | 已有方向主题 | 覆盖文档数量 | 是否与本文重复 |
|---|------------|------------|--------------|
| 群聊/频道 | 8 份 | ✅ 重复——本文未涉及 |
| 消息线程/话题 | 3 份 | ✅ 重复 |
| 活动流/时间线 | 5 份 | ✅ 重复 |
| 全文搜索增强 | 4 份 | ✅ 重复 |
| WebSocket/SSE 增强 | 6 份 | ✅ 重复 |
| 通知外带渠道 | 3 份 | ✅ 重复 |
| 文件存储版本/配额 | 3 份 | ✅ 重复 |
| 双栈存在（提及，非系统性分析） | 5 份 | ⚠️ **本文展开量化和迁移方案** |
| 双栈迁移计划/playbook | **0 份** | ✅ **本文唯一覆盖** |
| 幂等键请求体校验 | **0 份** | ✅ **本文唯一覆盖** |
| Background 超时的系统模式 | **0 份** | ✅ **本文唯一覆盖** |
| SSE Hub 可观测性缺失 | 1 份（侧写） | ⚠️ 本文从零度量角度展开 |
| CI 架构约束自动化 | 1 份（侧写） | ⚠️ 本文从强制执行角度展开 |

---

*本文分析基于 `main` 分支 `2026-07-10` 状态。不涉及任何代码变更。*
