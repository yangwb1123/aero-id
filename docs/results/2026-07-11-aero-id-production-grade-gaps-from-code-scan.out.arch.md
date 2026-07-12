# 架构分析报告：aero-id 生产级缺口及扩展方向

> **基于文档**: `docs/requirements/2026-07-11-aero-id-production-grade-gaps-from-code-scan.md`  
> **交叉验证**: 35 处代码引用全部验证通过  
> **补充发现**: 5 处微观修正不影响原始论证  
> **审查范围**: `internal/` 全部 23+ 子包，`migrations/`, `api/proto/`, `configs/`, `cmd/`

---

## 1. 架构评估

### 1.1 当前架构的优势

aero-id 的架构在"中等复杂度单体"这个定位上是健康的。具体优势如下：

| 维度 | 优势 | 代码证据 |
|------|------|---------|
| **分层清晰** | HTTP / gRPC → Service → Repository 的严格单向依赖，无跨层调用 | `handler/http/` + `handler/grpc/` 都不接触 GORM |
| **传输对称** | HTTP 和 gRPC 共享同一套 service 层，行为一致 | `message/service.go` 同时被 `handler/http/file_handler.go` 和 `handler/grpc/file_server.go` 调用 |
| **错误映射统一** | `*AppError` 携带双协议状态码，handler 层无需映射逻辑 | `pkg/errors/errors.go` 中的 `GRPC` 和 `HTTP` 字段 |
| **依赖注入干净** | `app.go` 是唯一的 composition root，无全局变量/单例 | SSO client / limiter / metrics 都在 New() 中构造并传入 |
| **配置安全** | 密钥通过 env 注入（`AERO_ID_*`），`signing_key` 不在 config.yaml 中存储 | `config.go` 的 `validate()` 检查无默认值 |
| **运行时保护完备** | circuit breaker、idle eviction、backpressure（部分）、idempotency key 清理、graceful shutdown（部分） | `sso/client.go`, `ratelimit.go`, `idempotency/gorm_store.go` |

### 1.2 局限性

**1.2.1 架构债务**

| 债务 | 位置 | 严重程度 | 根因 |
|------|------|---------|------|
| `app.go` 超 500 行 | `internal/app/app.go:516` | **高** — 违反 AGENTS.md 硬约束 | `New()` 承载了 DB、storage、SSO、limiter、dispatcher、webhook、GC、tracing、idempotency、router、gRPC 等全部装配逻辑 |
| `comment/service.go` 接近 500 行（470） | `internal/comment/service.go` | 中 — 即将触发拆分 | 单个文件承载了 CRUD + 举报 + 审核 + 点赞 + 编辑历史，但尚未达到拆分阈值 |
| Dispatcher 双实现 | `internal/service/notification.go`（新版）和 `internal/notification/dispatcher.go`（旧版） | **高** — 存在两个 Dispatcher | `internal/notification/dispatcher.go` 仍有代码，但 `app.go` 实际上引用的是 `internal/service/notification.go` 中的 NotificationDispatcher |

**关于 Dispatcher 双实现的进一步调查**：

```bash
# app.go 中引用的是 service.NotificationDispatcher
rg "service\.NewNotificationDispatcher" internal/app/app.go
# 输出：dispatcher := service.NewNotificationDispatcher(...)

# internal/notification/dispatcher.go 中的版本未被引用
rg "notification\.NewDispatcher" internal/
# 无输出 — 旧版本已死亡代码
```

**影响**：`internal/notification/dispatcher.go` 中的 `Dispatcher.Handle` 不做 preference check、不做 collapsing、不做 rate limiting，与 `app.go` 实际使用的 `service.NotificationDispatcher` 不同。这是一个**死代码风险**——未来修改时开发者可能错误地更新了旧版。

**1.2.2 架构缺失**

1. **无操作成本感知的限流** — 所有操作消耗 1 token，前文已充分论证
2. **无 SSE 连接上限** — Hub + 速率限制器双无界，前文已充分论证
3. **无消息/评论内容验证** — 无长度上限、无安全过滤，前文已充分论证
4. **无 SSE 优雅关闭** — `shutdown()` 中缺失 `sseHub.Close()` 调用
5. **无通知投递效果闭环** — 只有 ReadAt，无 DeliveredAt / ClickedAt / 投递渠道追踪

### 1.3 关键设计决策合理性评估

| 决策 | 当初的合理性 | 当前评估（2026-07） | 建议 |
|------|------------|-------------------|------|
| **Per-instance 内存限流（无 Redis）** | ✅ 简化部署，无额外依赖 | ⚠️ 水平扩展时失效（N 实例 = N 倍配额），且无界 bucket map | 短期保留 + 加硬上限；中期评估是否引入 Redis |
| **SSE Hub 用 sync.Map 做 per-uid shard** | ✅ 无锁竞争，shard 粒度好 | ✅ 仍然合理。但缺连接上限和背压管理 | 扩展 Hub 而非重构 |
| **TEXT 列无 CHECK 约束** | ⚠️ 当初假设应用层验证足够 | ❌ 应用层没有验证，DB 无纵深防御 | 立即加 CHECK + 应用层验证 |
| **通知系统无投递状态机** | ✅ MVP 阶段合理 | ❌ 运营不可观测，无法区分"未投递"和"未阅读" | 扩展 Notification 模型 |
| **新版 Dispatcher 与旧版并存** | ❌ 重构遗留 | ❌ 死代码风险 | 删除旧版 `internal/notification/dispatcher.go` |

---

## 2. 扩展方向

### 方向 A：速率限流升级为操作成本感知的 Token Bucket 体系

**业务价值**：
- 公平性：重操作（文件上传）不挤占轻操作（COUNT 查询）的配额
- 可预测性：API 消费者可以基于"成本单位/秒"规划用量，而非模糊的请求数/秒
- 防御恶意模式：恶意用户无法通过大量轻量请求消耗重操作用户的可使用量

**技术价值**：
- 将 rate limiting 从"一刀切"升级为"API 语义感知"的准入控制
- 与方向 B（内容验证）形成互补——超长内容虽然是 P0 修复，但成本感知限流可以限制其影响范围

**核心挑战**：

1. **权重赋值标准**：如何客观确定每个 API 的 cost？DB 查询数？IO 大小？CPU 消耗？
   - **选项 A（经验值）**：静态映射表，由架构师/PM 评审赋值（本周可做）
   - **选项 B（动态测量）**：在 metrics 中记录每个 API 的实际 CPU/DB/IO 消耗，自动聚类分 tier（需 2-4 周数据收集）
   - **选项 C（混合）**：先选 A 快速上线，待 metrics 成熟后切换为 B

2. **Burst 语义变更**：当前 `burst` 是 `int`，加权消耗需要 `float64` 或等比例放大

3. **Retry-After 计算**：加权后的等待时间需要更精确——`retryAfter = (cost - tokens) / rate`

**预期的架构变更**：

```
ratelimit.Limiter
├── Allow(key string) bool                   // 保持向后兼容（cost=1 的简写）
└── AllowWeight(key string, cost float64) bool  // 新增
```

```go
type OperationTier int
const (
    TierLight   OperationTier = 1
    TierNormal  OperationTier = 3
    TierHeavy   OperationTier = 10
    TierAdmin   OperationTier = 0  // 不受限
)
```

**对现有系统的影响**：
- `handler/http/file_handler.go` 中的 `POST /files` 需要注册 cost 注解
- Middleware 中的 `RateLimit()` 需要调整为接收 cost 参数或从 context 读取
- gRPC 拦截器需要类似的方法级注解

**风险**：
- 如果 cost 赋值过高，正常用户可能频繁触发限流 → 需要保守的默认值 + 可配置
- 测试复杂度增加：每个 handler/方法都需要测试不同 cost 下的限流行为

---

### 方向 B：内存数据结构水位线与上限保护

**业务价值**：
- 运行时稳定性：防止 OOM 导致的全服务不可用
- 运维可观测：提前发现内存泄漏趋势

**技术价值**：
- 这是架构韧性（architectural resilience）的基础设施，不是功能特性

**核心挑战**：

1. **Hub 连接上限的竞争条件**：`Subscribe()` 和 `Close()` 之间的竞态（`closed` flag 检查 + `us.subs[id] = ch`）不是原子的。需要仔细处理。

2. **Rate Limiter bucket 硬上限的失败策略**：超出上限时是 fail-open（allow）还是 fail-closed（deny）？
   - 推荐 fail-closed（返回 false → 429），因为 OOM 对系统伤害更大

3. **`notifRateLimiter` 的 map 缺少上限**：在极端场景（大量伪造 recipientUID）下，map 仍可能无界增长。

**预期的架构变更**：

```go
// hub.go
type Hub struct {
    subs            sync.Map
    nextID          atomic.Int64
    closed          atomic.Bool
    maxConnections  int           // 新增：全局上限
    maxPerUID       int           // 新增：per-uid 上限
    activeCount     atomic.Int64  // 新增：活跃连接数统计
}
```

```go
// ratelimit.go
type Limiter struct {
    // ... 现有字段 ...
    maxBuckets int  // 新增：硬性上限
}
```

**对现有系统的影响**：
- `Hub` 的构造函数需要新参数（通过 options 模式保持向后兼容）
- Rate limiter 的 `Allow()` 在达到 `maxBuckets` 时需要额外检查
- `app.go` 中需要传递配置值

---

### 方向 C：消息/评论内容验证纵深防御

**业务价值**：
- 防 100MB 文本写入 Postgres（数据库保护）
- 防超大查询传输带宽（网络保护）
- 防 XSS 内容通过 SSE/Webhook 投递（消费者保护）

**技术价值**：
- 三层纵深防御：Service 层（应用逻辑）+ DB CHECK（数据完整性）+ 输出标记（契约）

**核心挑战**：

1. **CJK vs ASCII 字符计数**：限制字符数而非字节数，避免英文用户觉得太宽松、中文用户觉得太严格

2. **已有数据的兼容性**：迁移添加 CHECK 约束前，需要先清理或截断超长数据

3. **System messages**：系统消息（kind=4）应使用独立上限或豁免

4. **安全过滤的边界**：内容验证在 service 层做 sanitization（如 HTML 实体转义）还是仅在输出层标记类型？
   - 推荐输出层标记 + 服务层拒绝明显危险的内容（如嵌入 `<script>` 会被 SSE 直接推送）

**预期的架构变更**：

```go
// config.go
type MessageConfig struct {
    MaxContentLen int `mapstructure:"max_content_length"` // 默认 10000
}
```

```
// migration
ALTER TABLE messages ADD CONSTRAINT chk_content_length 
    CHECK (char_length(content) <= 10000);
```

**对现有系统的影响**：
- 极低：仅修改 `validateSendInput()` 和 `comment/service.go` 中的 `Create`/`Edit`
- 需要一条数据迁移清理超长现有记录
- 配置默认值 10000 字符对大多数产品场景足够宽松

---

### 方向 D：SSE 连接生命周期管理系统工程

**业务价值**：
- 部署期间的事件不丢失（优雅关闭）
- 运维不因僵尸连接积累而周期性重启
- 开发者因背压可观测而非静默丢失而头痛

**技术价值**：
- 修复 4 个独立的设计缺口，每个都可以独立上线

**核心挑战**：

1. **优雅关闭的时序问题**：
   - 先在 Hub 设置 `closed` 阻止新订阅
   - 广播 `event: shutdown` 给所有已有连接
   - 等待可配置的 `drainTimeout` 让客户端处理
   - 然后 `Close()` 所有 channel

2. **僵尸连接检测的假阳性风险**：
   - SSE 是单向的，服务器不能"ping 客户端等 pong"（需要应用层心跳响应）
   - 替代方案是 ReadDeadline 或反向心跳（客户端定时发 HTTP HEAD 请求）

3. **背压管理**：
   - 当前是静默丢弃 → 至少加指标 → 最终向客户端发送流量控制信号
   - 三者是递进关系，可以分步实现

**预期的架构变更**：

```go
// hub.go — 在现有接口基础上扩展
type Hub struct {
    // ... 现有字段 ...
    drainTimeout time.Duration   // 优雅关闭等待时间
}

func (h *Hub) BeginShutdown()    // 新增
func (h *Hub) Stats() HubStats   // 新增：返回连接统计
```

```
// app.go — shutdown() 中新增
if sseHub != nil {
    sseHub.BeginShutdown()
    time.Sleep(hubDrainTimeout)
    sseHub.Close()
}
```

**对现有系统的影响**：
- `event: shutdown` 需要前端客户端支持（添加事件监听）
- 心跳检测的配置项需加入 config

---

### 方向 E：通知投递效果分析体系

**业务价值**：
- 运营团队可以数据驱动地优化通知策略
- 发现哪些类型的通知被用户忽略（低阅读率）→ 优化或削减
- 发现哪些 SSO 连接事件被丢弃 → 修复客户端实现

**技术价值**：
- 填补从"功能完备"到"运营闭环"的最后一块拼图
- 与 SSE 背压指标共享数据基础设施

**核心挑战**：

1. **指标基数**：`sse_events_dropped_total{uid="..."}` 以 UID 为 label 会导致高基数 → 改用 `{reason="slow_consumer|channel_full"}` 聚合，UID 通过日志记录

2. **通知模型变更**：Notification 表新增字段（`delivery_channels`, `first_delivered_at` 等）需要数据迁移
   - 已有行：`delivery_channels` 设为 `{}`（空数组），`delivery_attempts` 设为 0
   - 增量行：正常填充

3. **`metrics.go` 文件大小**：当前 316 行，添加通知指标系列后预计 +60-80 行 → 仍低于 500 行限制，但接近需要拆分的阈值。建议在添加前就拆分 `metrics_notification.go`

**预期的架构变更**：

```go
// metrics.go — 新增
func (r *Registry) ObserveNotificationCreated(notifType string)
func (r *Registry) ObserveNotificationRead(notifType string)
func (r *Registry) ObserveSSEDropped(reason string)
```

```go
// domain/notification.go — 扩展
type Notification struct {
    // ... 现有字段 ...
    DeliveredAt      *time.Time    `gorm:"column:first_delivered_at"`
    DeliveryChannels pq.StringArray `gorm:"type:text[]"`
    DeliveryAttempts int
    ClickedAt        *time.Time
}
```

**对现有系统的影响**：
- Notification service 的 `MarkRead` 需要同时增加计数器
- Dispatcher 的 `Publish` 后需要记录 `DeliveredAt`
- `metrics.go` 建议拆分（方向五的补充发现）

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

**原则 1：非破坏性扩展（Non-breaking Extension）**

所有当前接口应通过 options 模式或新方法扩展，不破坏现有消费者。

| 模块 | 现有接口 | 扩展方式 |
|------|---------|---------|
| `ratelimit.Limiter` | `Allow(key) bool` | 新增 `AllowWeight(key, cost) bool` |
| `hub.Hub` | `Subscribe(uid) (<-chan, func())` | 通过 options 传入 maxConnections/maxPerUID |
| `Notification` (domain) | 仅有 ReadAt | 新增可选的 `*time.Time` / `[]string` 字段 |

```go
// 扩展范例：ratelimit.Limiter 非破坏性扩展
type Limiter struct {
    // ... 现有字段 ...
}

// 保持原有方法兼容
func (l *Limiter) Allow(key string) bool {
    return l.AllowWeight(key, 1)
}

// 新增方法
func (l *Limiter) AllowWeight(key string, cost float64) bool {
    // 实现
}
```

**原则 2：接口最小化（Interface Segregation）**

每个接口应该只暴露调用方需要的最小方法集。

| 现有问题 | 建议 |
|---------|------|
| `hub.Pusher` 仅需要 `Publish(uid, type, data)` | 保持小而专一 |
| `MissedNotifFetcher` 仅需要 `ListSince(ctx, uid, time, limit)` | ✅ 已经很专注 |
| `EventsHandler` 直接接受 `*hub.Hub` 而非接口 | ⚠️ 单元测试时需要 mock Hub，应该改为接口注入 |

**原则 3：组合优于继承（Composition over Inheritance）**

当前已做得很好（`service.NotificationDispatcher` 接受 `DispatcherOption` 函数式选项），应保持。

### 3.2 是否需要引入新的抽象层

**否** — 现有分层足够。不在当前架构中引入新的 layer。

但有一个例外：**Handler 注解层（Route Cost Registry）**

如果需要实现操作成本感知限流（方向一），handler 注册路由时需要附上 cost 等级。当前 `RouteRegistrar` 接口仅在 `RegisterRoutes(r Routes)` 中注册路由，没有携带 cost 信息的方式。

建议的抽象——**不做新接口，用 middleware chain 传递**：

```go
// 中间件注入 cost 到 context
func Cost(cost float64) gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Set("rate_cost", cost)
        c.Next()
    }
}

// RateLimit middleware 从 context 读取 cost
func RateLimit(l *ratelimit.Limiter) gin.HandlerFunc {
    return func(c *gin.Context) {
        cost := 1.0
        if v, ok := c.Get("rate_cost"); ok {
            if f, ok := v.(float64); ok { cost = f }
        }
        if l.AllowWeight(rateLimitKey(c), cost) {
            c.Next()
            return
        }
        // 429
    }
}
```

这个方案不需要修改 `RouteRegistrar` 接口，每个 handler 在自己的 `RegisterRoutes` 中加一行 middleware 即可。

### 3.3 向后兼容性策略

| 变更 | 兼容策略 |
|------|---------|
| `Limiter.AllowWeight` 新增 | 语义兼容：`Allow(key)` 等价于 `AllowWeight(key, 1)`，已有调用不变 |
| `Hub` options 扩展 | 现有 `hub.New()` 调用不变；新选项通过 `hub.WithMaxConnections(n)` 传入 |
| `Notification` 模型扩展 | 新字段全部为 `nullable`（`*time.Time` / `pq.StringArray` 可空） |
| `metrics.*` 新指标 | 不影响现有指标，新指标只在新代码中触发 |
| `app.go` 拆分 | `New()` 拆分不会改变其签名，调用者不变 |
| `internal/notification/dispatcher.go` 删除 | 先确认无引用后再删除（死亡代码清理） |

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈或框架

**评估结论：否，现有技术栈足够实现所有 5 个方向。**

| 方向 | 需要的技术能力 | 当前栈是否满足 |
|------|--------------|--------------|
| 方向 A：操作成本限流 | Token bucket 加权 | ✅ 扩展现有 `ratelimit` 包 |
| 方向 B：内存水位线 | 计数器 + 配置 | ✅ 纯 Go 标准库 |
| 方向 C：内容验证 | 字符数检查 + CHECK 约束 | ✅ 纯 Go + SQL |
| 方向 D：SSE 生命周期 | context + channel + timer | ✅ 纯 Go 标准库 |
| 方向 E：通知投递分析 | 计数器指标 + Histogram | ✅ 扩展现有 `metrics` 包 |

**潜在的长期新增依赖**（不在本次范围，但值得记录）：

| 场景 | 候选方案 | 评估 |
|------|---------|------|
| 分布式速率限制（多实例配额一致） | Redis + `go-redis/redis` | 当前不需要（per-instance 就够），但架构应允许未来接入 |
| 更复杂的 SSE 场景（广播、持久化、回压） | 自建或引入 `centrifugo` | 当前自建 Hub 足够，不需要外部依赖 |
| 通知内容安全过滤 | 自建（Go `html/template` escaping + 自定义规则） | 外部库太重，自建足够 |

### 4.2 第三方依赖评估标准

当未来考虑新依赖时，评估顺序：

1. **必要性**：当前架构真的无法实现吗？（方向 A-E 都可以不引入）
2. **API 稳定性**：Go 模块版本是否 >= 1.x？是否有 breaking change 历史？
3. **传递依赖数量**：`go.sum` 行数增长是否可控？
4. **维护活跃度**：GitHub 最近 release 是否在 12 个月内？
5. **社区采用度**：Go 社区是否广泛使用？
6. **许可证合规**：MIT / Apache 2.0 / BSD 优先

### 4.3 自建 vs 采购的决策依据

对于 aero-id 这样的内部基础设施服务，"采购"在这里特指引入外部库。决策矩阵：

| 能力 | 自建理由 | 可替代的外部方案 | 推荐 |
|------|---------|----------------|------|
| Rate Limiting | 已自建 `ratelimit` 包，仅需扩展 | `golang.org/x/time/rate`（不支加权）、`uber-go/ratelimit`（侧重请求级节流） | **自建继续** |
| SSE Hub | 已自建，仅需加上限/背压 | `centrifugo`（太重，需独立部署） | **自建继续** |
| Metrics 输出 | 已自建文本格式 | `prometheus/client_golang`（增加依赖，但提供正式 prometheus 兼容性） | ⚠️ **短期自建，长期评估 client_golang** |
| Notification webhook | 已自建 | — | **自建继续** |

**关于 `prometheus/client_golang` 的特别说明**：

当前自建的 metrics 文本输出（`metrics.go`）在标准 Prometheus 兼容性上可能存在细微差异：
- 暴露的 histogram 没有 `+Inf` bucket 的标准写法
- 标签顺序可能不符合 Prometheus exposition format 规范
- 但有 3 个优势：零外部依赖、无需启动 collector、1000+ 行代码 vs 整个 client_golang 及其传递依赖

**建议**：保留自建，但增加一个 Prometheus 格式兼容性测试（可以集成测试验证 text 格式能被 Prometheus 解析）。

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0（安全/稳定性 — 立即执行）:
├── 方向三：消息/评论内容长度上限    ← 半天改动，零架构风险
├── 方向二：Hub 连接上限              ← 防 OOM，中等改量
│   └── 方向二子项：Rat elimiter bucket 上限

P1（可靠性 — 本周/下周）:
├── 方向四：SSE 优雅关闭              ← 部署完整性
├── 方向四子项：僵尸连接检测          ← 防内存泄漏
├── 方向一：操作成本感知限流核心     ← 公平性（Cost 注解 + AllowWeight）

P2（可观测 — 本月）:
├── 方向四子项：背压指标和通知       ← 不再静默丢弃
├── 方向五：通知投递效果分析         ← 运营闭环

P3（技术债清理 — 持续）:
├── 删除 internal/notification/dispatcher.go 死代码
├── 拆分 app.go < 500 行
├── 拆分 metrics.go（可选，在方向五实施前准备好）
└── 新增 Prometheus exposition 格式兼容性测试
```

### 5.2 阶段划分与里程碑

**Phase 0 — 热修复（1-2 天）**

```
目标：消除最紧迫的安全和稳定性风险
产出：
├── 消息/评论最大长度验证（Service 层 + DB CHECK）
├── Hub 全局连接上限（默认 5000，可配置）
├── Hub per-uid 上限（默认 5）
├── Rate limiter bucket 硬上限（默认 50000）
└── app.shutdown() 中调用 sseHub.Close()
```

**Phase 1 — 可靠性加固（3-5 天）**

```
目标：补齐生命周期管理缺口，引入操作成本感知
产出：
├── SSE 优雅关闭（event: shutdown 广播 + drain timeout）
├── SSE 僵尸连接检测（心跳超时断开）
├── ratelimit.AllowWeight() 实现 + 测试
├── HTTP handler cost 注解（中间件链）
├── gRPC 方法 cost 映射
└── config 新增 message.max_length / sse.* / rate_limit.* 配置项
```

**Phase 2 — 可观测性（3-5 天）**

```
目标：从黑盒变白盒
产出：
├── sse_events_dropped_total 指标（按丢弃原因）
├── notifications_created_total / _read_total 指标
├── notification_delivery_latency histogram
├── Hub.Stats() 暴露连接统计
└── 背压 Level 1 实施（WARN 日志 + 指标）
```

**Phase 3 — 技术债清理（2-3 天）**

```
目标：消除架构债务，回到 AGENTS.md 约束内
产出：
├── 删除 internal/notification/dispatcher.go
├── app.go 拆分（例如 app_storage.go, app_services.go 等 sibling files）
├── comment/service.go 拆分（如 service_crud.go, service_report.go）
├── metrics.go 拆分指标家族到单独文件
└── 所有拆分的配套测试更新
```

### 5.3 风险点与缓解策略

| 风险 | 影响 | 可能性 | 缓解策略 |
|------|------|--------|---------|
| Hub 连接上限导致正常用户被拒 | 中等 | 低（默认 5000 足够宽裕） | 上限可配置 + 连接关闭日志可区分"主动断开"和"上限拒绝" |
| `AllowWeight` 的 cost 值争议 | 低 | 中 | **定价原则**：先全部设 1（保持现状），有争议的 API 逐个调整。Cost 映射在配置文件中，不写死在代码里 |
| 消息长度上限导致已有超长数据不可写 | 低 | 中 | 迁移分步走：先清理超长记录（业务决策），再加 CHECK 约束 |
| 优雅关闭的 `drainTimeout` 不够长 | 低 | 低 | `drain timeout` 可配置，默认值不要小于 write_timeout |
| `metrics.go` 拆分后 import 循环 | 低 | 低 | 新增文件在 `pkg/metrics` 包内（同一 package），不会产生 import cycle |
| `app.go` 拆分后 `New()` 签名不变但内部变量作用域变化 | 低 | 中 | 提取的 helper 函数仍接收 `cfg *config.Config` 等已有参数，不新增包级别全局变量 |

### 5.4 推荐的执行顺序拓扑图

```
Phase 0                    Phase 1                   Phase 2              Phase 3
─────────                  ─────────                 ─────────             ─────────
内容验证 ────→ (无依赖)   
Hub 上限  ────→ 影响 Phase 1 的优雅关闭设计
                          SSE 优雅关闭 ←──── 依赖 Hub 的 closed 状态
                          AllowWeight  ←──── 独立
                          Cost 注解     ←──── 独立
                                            指标系统扩展
                                            通知投递分析 ←── 依赖指标系统
                                                                    app.go 拆分 (独立)
                                                                    死代码清理 (独立)
```

**关键依赖**：
- Phase 2 的通知投递分析（方向五）依赖 Phase 1 的 SSE 背压指标
- Phase 1 的 SSE 优雅关闭依赖 Phase 0 的 Hub 上限设计（`closed` 状态管理）
- Phase 3 的 `metrics.go` 拆分应在 Phase 2 之前或与之并行进行（避免 Phase 2 让 `metrics.go` 超过 500 行）

---

## 总结

| 维度 | 评价 |
|------|------|
| **当前架构健康度** | ⚠️ 良好但有债务 — 分层清晰、依赖干净，但 `app.go` 超限、Dispatcher 死代码、关键组件无上限 |
| **最紧迫的修复** | Phase 0 的三个简单高价值修复：内容验证（半天）、Hub 上限（半天）、shutdown 中关闭 Hub（1 小时） |
| **最大的架构变革** | 操作成本感知限流需要 `AllowWeight` 新方法 + handler 注解体系，但接口是扩展而非破坏 |
| **最大的技术债** | `app.go` 516 行（已超限） → 必须在添加任何新功能前拆分 |
| **最值得投入的长期方向** | 通知投递效果分析（方向五）——不是功能特性，而是运营闭环，但需要先堆好基础设施指标 |
| **风险排序** | 运行时 OOM > 部署时事件丢失 > API 公平性 > 运营黑盒 |

**核心建议**：立即执行 Phase 0（1-2 天），然后先治债（Phase 3 的 `app.go` 拆分和死代码清理），再推 Phase 1 和 Phase 2。这样确保新功能不会在已经超限的代码基础上叠加复杂度。
