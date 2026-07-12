现在我已通读代码库并获得全面的理解。以下是我的架构分析。

---

# 架构分析：aero-id 幂等、通知、Outbox 与限流子系统

## 1. 架构评估

### 核心优势

| 属性 | 观察 | 价值 |
|---|---|---|
| **分层架构** | `handler{http,grpc} → service → repository`，严格单向依赖 | 关注点分离清晰；可独立测试每一层 |
| **组合根集中化** | `internal/app/app.go` 通过构造函数注入将所有依赖串联 | 无需全局状态、服务定位器或 init() 反模式 |
| **接口驱动设计** | 每个 repo/service/storage 都有一个 Go 接口；生产实现与测试替身可互换 | 可测试性高；松散耦合 |
| **事务性 Outbox** | 领域事件在与数据行相同的 DB 事务中写入；Relay 异步排空 | 强有力的保证：通知永远不会丢失（最多一次→至少一次） |
| **函数式选项模式** | 服务构造函数贯穿使用 `WithXxx()` 选项 | 组合灵活；默认值保持向后兼容；新选项不破坏现有调用者 |
| **可观测性** | RED 指标 + OTel 分布式追踪 + 结构化 zap 日志 | 可调试；生产可见性 |

### 关键架构限制

**1. NotificationDispatcher 中存在有毒的数据竞争模式**

`Handle()` 中 Create/FindRecentByTypeAndActor 的顺序被破坏：

```
Create(n)                    // 总是先写入
FindRecentByTypeAndActor()   // 然后检查是否存在可折叠的未读项
→ 新行 n 即使会被折叠也会留在表中
```

团队将其辩解为"审计追踪"，但这在数据库和用户通知源中都留下了幽灵行。列表查询必须过滤这些，或者用户每次刷新都会看到重复的"新"通知。**这是数据正确性的 P1。**

**2. 幂等性密钥存储：未受保护的攻击面**

幂等表每行有 `WRITE` 权限（`uid, endpoint, idem_key` 上唯一），但没有每用户存储限制。考虑：

- 每行 200-300 字节
- `reservationTTL = 2min`，但 `Complete` 在 `idempotency_ttl: 24h` 时长内保留数据
- `purgeExpired()` 以 1h 为周期运行，对操作没有区分
- 一个经过身份验证的用户旋转密钥可以以 ~600 字节/秒的速度填满数据库：大约 17 小时达到 1GB

**这本质上是一个存储耗尽型 DoS。** SSO 身份验证将攻击者限制为经过身份验证的用户，但任何一个用户都可以填满共享表。

**3. 限流器——单桶不区分端点**

`ratelimit.go:56-64` 和 `grpc/server.go:124-129` 都产生格式为 `"uid:<uid>"` 或 `"ip:<ip>"` 的密钥。所有端点共享同一个全局桶。

因此：`DELETE /files/:id`（重操作）与 `GET /notifications?cursor=xxx`（轻操作）争夺相同的固定 RPS。一个恶意操作可以耗尽所有端点的共享预算，有效地导致全系统 DoS。

**4. 死信运维盲区**

`outbox/repository.go:114-115` 正确地将失败超过 `maxRetries` 的事件标记为 `dead_letter = true`，但没有任何 API 来检查、重放或清除它们。AdminHandler 甚至不接受 `outbox.Repository` —— 只有一个通往 `adminRepo` 的通道。当事件死信时，运维人员除了查询数据库别无选择。

### 技术债

| 债务 | 位置 | 影响 |
|---|---|---|
| 仪表化告警中使用 `time.Now()` 而非注入的时钟 | `notification.go:233`（`time.Now().UTC()`） | `FindRecentByTypeAndActor` 在测试中无法进行时间旅行 |
| `purgeExpired()` 静默丢弃错误 | `store.go:153`（`_ = s.db.Where(...).Error`） | 清理无声失败——没有日志、没有指标、没有告警 |
| 无发布前事件的死信审核 | `relay.go:94` — `r.repo.MarkFailed` 从不记录被死信的事件 | 无人在事件死信时知晓 |
| `outbox.Handler` 接口不携带因果上下文 | `outbox.go:42-46` | 下游处理器无法建立正确的因果关系 |

---

## 2. 扩展方向

以下扩展按回报递减和独立依赖关系排序。

### 方向 A：请求级资源感知限流 ⭐⭐⭐⭐⭐

**为什么需要：**

当前的单桶 `"uid:<uid>"` 方案无法区分昂贵操作（上传、块操作）和廉价操作（列表、计数）。一个行为不当的客户端可以：

1. 用请求淹没一个昂贵的端点 → 为每个用户填满桶
2. 所有其他端点饥饿，因为桶被耗尽

这是活跃的损害——即使限流器"已启用"，它也无法保护。

**核心挑战与技术难点：**

- 桶策略选择：每个端点一个桶与每个端点/方法组合相比，每个用户一个桶
- 配置表面：需要一种表达方式，如 `rate_limit: { per_endpoint: { "POST:/v1/files": { rps: 5, burst: 10 } } }`
- gRPC 的等效表达（完整方法名 → 配置）
- 每个端点配置在运行时变更的启动重新加载
- 健康的 HTTP 和 gRPC 必须始终绕过

**预期架构变更：**

```
internal/
  pkg/ratelimit/
    limiter.go        # 当前：单一密钥→令牌桶
                      # 新：多桶策略 ─ 全局 + 每个端点 + 每个用户每个端点
    config.go         # 配置解析与验证
    middleware.go      # HTTP 与 gRPC 共享
  middleware/
    ratelimit.go      # 必须为每个端点路由生成丰富的密钥
```

密钥从 `"uid:<uid>"` 变为 `"uid:<uid>:endpoint:<method>"`。多桶策略：

- **选项 A（推荐）**：分层桶——每个用户每个端点一个桶，顶层有每个用户的全局桶
- **选项 B**：每个用户每个端点的权重桶——重端点会消耗更多令牌
- **选项 C**：每个端点一个桶，公平排队——端点独立，但不限制单个用户

**对现有系统的影响：**

- 向后兼容：当前行为（`"uid:<uid>"`）是层次结构的最外层桶
- 配置变更但模式兼容：现有 `rate_limit.rps` 变为全局默认值；新增 `rate_limit.endpoints`
- 无数据库变更；纯内存

### 方向 B：因果关系与事件溯源审计 ⭐⭐⭐⭐

**为什么需要：**

目前，跨事件类型的因果顺序是偶然的（`created_at ASC`）。当一个 `message.sent` 产生一个通知，而一个 `message.read` 紧随其后时，通知可能以错误的顺序到达。没有 `CausationID`：

- 不可能进行事件溯源重建
- 不可能在死信处置时确定根本原因
- 无法实现读取你写入的一致性的幂等重放

**核心挑战：**

- 在 `Event` 结构中引入 `CausationID`（嵌套到通知领域）
- 填充它是困难的：生产者（消息、评论服务）必须在写入事件时传递导致的事件 ID
- `Notification` 实体没有 `EventID` 或 `CausationID`
- Relay 必须保证每个处理程序调用都具有正确的因果上下文
- 现有行需要回填迁移

**预期架构变更：**

```
internal/
  outbox/
    outbox.go             # Event 新增 CausationID uuid.UUID
    relay.go              # 在 handler.Handle(ctx, e) 之前注入因果上下文
  domain/
    notification.go       # Notification 新增 CausationEventID
  service/
    notification.go       # Handler 传播 CausationID → Notification
  repository/
    notification.go       # 迁移：新增 causation_event_id 列
```

**对现有系统的影响：**

- 向后兼容：新列为空；现有事件按 `created_at` 排序
- 生产者必须选择新字段；初始为可选，随后设为必填
- 需要迁移；可能很大（数百万行）

### 方向 C：死信运维面板与自动报警 ⭐⭐⭐⭐

**为什么需要：**

目前，死信事件会从 `FetchUnpublished` 中消失但保留在表中。没有运维人员知道它们；没有 API 来重放、检查或清除它们。事件在破纪录的生产故障中丢失。

**核心挑战：**

- Admin API 扩展：为死信操作添加 HTTP + gRPC 端点
- 重放语义：重放一个死信事件也应重放其下游效应（通知行、webhook）
- 死信率自动报警指标（`rate(outbox_dead_letters_total[5m]) > 0`）
- 删除安全：在保留期之前不可永久删除；软删除

**预期架构变更：**

```
internal/
  handler/http/
    admin.go              # 新增路由：GET /v1/admin/dead-letters, POST /v1/admin/dead-letters/:id/replay
  handler/grpc/
    ops.go                 # gRPC 端点的等效路由
  service/
    admin.go               # DeadLetterService：ListDeadLetters, ReplayDeadLetter, PurgeDeadLetters
  outbox/
    repository.go          # 新增方法：FetchDeadLetters, RequeueDeadLetter
```

**对现有系统的影响：**

- 对核心通知路径无影响
- 向后兼容：新增端点，无现有调用者
- 推荐 `ReplayDeadLetter` 创建一个新事件（新 ID），而非重用失效的事件

### 方向 D：通知折叠事务安全 ⭐⭐⭐⭐

**为什么需要：**

当前竞争条件（Create → FindRecent → 可能产生幽灵行）会污染用户的通知源。修复方法是将 Create 和折叠合并到同一个事务中。

**核心挑战：**

- 当前设计将 Create 和折叠作为 GORM 中的独立调用，Repos 之间没有共享 tx
- 正确修复需要要么：（a）一个 STORED PROCEDURE/SQL 片段来处理"插入或折叠"，要么（b）一个重写为 CreateOrCollapse
- 通知的读后写模式需要 `SELECT FOR UPDATE` 或序列化隔离级别以防止竞争
- `IncrementCollapsedCount` 必须与初始创建在同一个事务中

**预期架构变更：**

```
方法 A（推荐——Postgres 特性）：
  repository/notification.go
    ⇢ CreateOrCollapse() 一个 ON CONFLICT 语句，当匹配时递增

方法 B（GORM 事务）：
  service/notification.go
    ⇢ Handle() 使用跨 Create + FindRecent + IncrementCollapsedCount 的共享 tx
```

**对现有系统的影响：**

- 对领域模型无影响
- 对崩溃行为的变更：折叠现在从不会产生幽灵行（而非"它们是审计追踪"）
- `IncrementCollapsedCount` 现在在 tx 内部调用；不能原子处理

### 方向 E：每个用户的幂等性驱逐上限 ⭐⭐⭐⭐

**为什么需要：**

当前幂等性密钥表没有每用户配额。一个经过身份验证的攻击者可以填满表，导致所有用户的写入路径的 `INSERT` 性能下降（唯一索引膨胀）并增加磁盘使用。

**核心挑战：**

- 配额语义：软阈值（警告）与硬阈值（拒绝）
- 驱逐策略：LRU（最久未使用）与先过期先清除（FIFO）对比
- `Reserve()` 需要执行 `COUNT` 或维护一个计数器——这给每用户的热路径增加了一次数据库往返
- 计数器本身必须保持同步（考虑旋转密钥的并发请求）
- 迁移现有行：无状态变更，只需在新的应用逻辑中强制执行

**预期架构变更：**

```
内部/
  幂等性/
    store.go              # Reserve 检查 `SELECT COUNT(*) ... WHERE uid=? AND expires_at > now()`
                          # 超过时返回 ErrQuotaExceeded；在 service 或 handler 层映射为 429
    config.go             # 新增 max_keys_per_user 配置
```

**对现有系统的影响：**

- 热路径增加一次数据库往返（`COUNT` 查询）
- 向后兼容：0 或默认值 = 无限制
- 大型迁移：现有用户可能需要超过新阈值的豁免

---

## 3. 接口设计原则

### 跨模块原则

1. **接口归消费者定义** — 已经是 `NotificationPusher` (由 hub.Adapter 满足)、`BlockChecker` (由 blockRepo 和 blockService 满足)。继续此模式。

2. **不要导出内部结构** — 延续 `idempotencyRow`、`eventRow`、`notificationRow` 等不导出的模式。构造者通过构造函数注入。

3. **错误从不撒谎** — Repo 返回底层错误 → 服务用 `AppError.Wrap()` 包裹 → Handler 渲染。保持此转换不可绕过。

### 所需的新抽象

**1. 限流策略接口**

```go
type RateLimitStrategy interface {
    // Allow returns true if the request may proceed.
    // key 是 "uid:xxx:endpoint:POST:/v1/files"
    // cost 是此请求的令牌消耗（默认 1；昂贵操作可消耗更多）
    Allow(ctx context.Context, key string, cost float64) bool
    // RetryAfter 给客户端何时重试的提示
    RetryAfter() time.Duration
}
```

这使当前 `Limiter` 保持但允许插入分层的、Redis 后端或每端点策略。

**2. 通知事务性锁定**

额外的方法是当前 repo 协议中最薄弱的接口契约：

```go
// 替换 Create + FindRecentByTypeAndActor + IncrementCollapsedCount
// 为单个原子操作
CreateOrCollapse(ctx context.Context, n *domain.Notification, window time.Duration) error
```

**3. 死信管理**

```go
type DeadLetterStore interface {
    List(ctx context.Context, page domain.Page) ([]*DeadLetterEntry, string, error)
    Requeue(ctx context.Context, id uuid.UUID) error
    PurgeBefore(ctx context.Context, before time.Time) (int64, error)
}
```

### 向后兼容性设计

| 变更 | 兼容性策略 |
|---|---|
| 每端点限流 | 新配置可选；默认降级到当前全局行为 |
| 通知事务性创建 | 仅在启用折叠时激活；非折叠路径不变 |
| 每个用户幂等性配额 | 默认 0 = 无限制；启用时拒绝超出者 429 |
| CausationID | 新字段可为空；无它的事件按 created_at 排序 |
| 死信管理 API | 仅新增端点；无现有调用者受影响 |

---

## 4. 技术栈建议

### 不需要新的基础设施

当前技术栈（Go 1.26、Postgres、GORM、MinIO/S3、gRPC、Gin）足以解决所有发现的问题。以下**不建议新增基础设施**：

| 不推荐新增 | 原因 |
|---|---|
| Redis 用于分布式限流 | 单进程桶已足够；多副本可通过 sidecar 轮询协调或使用 Postgres 的 pg_advisory_lock |
| Kafka/RabbitMQ 用于 outbox | Postgres 轮询 + 背压方案对通知工作负载（小事件、高扇出）已是最优 |
| 专门的死信队列系统 | Postgres 行 + API 表面已足够；无需第二个基础设施 |

### 应该使用的标准库与 Postgres 原语

| 场景 | 方案 | 原因 |
|---|---|---|
| 每用户幂等性计数器 | `INSERT ... ON CONFLICT DO UPDATE` + `COUNT` 子查询 | 零新增依赖；ACID 安全 |
| 限流 | 进程内令牌桶（已有） | 延迟 < 1µs；无网络往返 |
| 通知事务创建 | `SELECT ... FOR UPDATE` 或可序列化事务 | 防止竞争；Postgres 内置 |
| 死信指标 | `rate(outbox_dead_letters_total[5m])` 作为 Prometheus 告警规则 | 标准 OSS 监控；aero-id 已暴露 /metrics |

### 何时自建 > 采购 / 使用第三方

| 决策 | 方向 | 论证 |
|---|---|---|
| 限流 | 自建（已有） | 对于单进程桶，无满足租户需求的 Go 库具有更好的 API |
| 幂等性 | 自建（已有） | 需要数据库集成；可与之交易的第三方库稀少 |
| 通知折叠 | 增强现有 | 数据库原语（SELECT FOR UPDATE）优于全新库 |
| 死信管理 | 自建新模块 | 简单的 CRUD API + 重放——无第三方管理 |

---

## 5. 实施路线图

### 优先级矩阵

| # | 方向 | 优先级 | 工作量 | 影响 | 依赖 |
|---|---|---|---|---|---|
| 1 | 通知折叠事务安全 | **P0** | 小（修改 Service + Repo 层） | 修复数据错误——幽灵通知 | 仅代码；无依赖 |
| 2 | 每个用户幂等性配额 | **P0** | 中（Store 变更 + 配置 + 错误处理） | 修复安全漏洞——存储耗尽 DoS | 无 |
| 3 | 每端点限流 | **P1** | 中（策略接口 + 配置 + 中间件变更） | 修复活跃 DoS——一个用户可以饿死所有端点 | 无 |
| 4 | 死信运维 API | **P1** | 中（3 个新端点在 HTTP + gRPC Admin 上） | 生产运维——工程师当前手动查询 | 无 |
| 5 | 因果关系 ID | **P2** | 大（模式迁移 + 生产者采用 + 排序逻辑） | 事件溯源接合剂；无它也可存活 | 方向 #1 优先 |

### 阶段划分

**阶段 1——安全与正确性（2 周）**

```
第 1-3 天:  方向 2（幂等性配额）
  - 将 max_keys_per_user 添加到 config
  - Reserve() 在 ON CONFLICT 之前计数现有行
  - 超出时返回 ErrQuotaExceeded → 429
  - 配置验证：0 = 无限制，非零是硬阈值
  - 单元测试：配额耗尽、接近、并发
  - make test && make lint ✅

第 4-7 天:  方向 1（通知折叠）
  - 修复：Create → FindRecent 竞争 → CreateOrCollapse ON CONFLICT
  - 移除"幽灵行审计"合理化
  - 数据库迁移：无（仅应用逻辑变更）
  - 集成测试：用 testcontainers 验证并发折叠消除重复项
  - make test && make lint && make test-integration ✅

第 8-10 天:  方向 1 的补丁——修复 time.Now 注入
  - 更改 service/notification.go 以接受 now function
  - 所有时间使用注入的时钟
  - 添加时间旅行测试

里程碑 1: 安全基线 + 数据正确性
```

**阶段 2——限流粒度（1 周）**

```
第 1-3 天:  方向 3（每端点限流）
  - 提取 RateLimitStrategy 接口
  - 实现分层桶：全局 → 每个用户 → 每个端点
  - HTTP 中间件：从路由模式派生密钥
  - gRPC 拦截器：从 FullMethod 派生密钥
  - 配置：每个端点覆盖 rate_limit.endpoints
  - 单元测试：桶层级、饥饿保护
  - make test && make lint ✅

第 4-5 天:  集成与部署
  - 默认配置：当前行为（向后兼容）
  - 添加每个端点配置以记录速率桶
  - 文档：配置表格与示例

里程碑 2: 活性 DoS 保护
```

**阶段 3——运维可见性（1 周）**

```
第 1-3 天:  方向 4（死信管理）
  - 获取死信端点（HTTP + gRPC）：页面 + 过滤
  - 重放端点：使用新 ID 创建新事件，保留 payload + event_type
  - 清除端点：管理员在保留期前批量删除
  - 指标：dead_letter_total、dead_letter_age_seconds
  - 单元测试 + 集成测试
  - make test && make lint && make test-integration ✅

第 4-5 天:  告警与文档
  - Prometheus 告警规则：rate(dead_letter_total[5m]) > 0
  - 运维剧本：如何处理死信
  - 在 README 或 docs/ops.md 中记录

里程碑 3: 生产运维就绪
```

**阶段 4——因果关系（2 周，可选）**

```
第 1-3 天:  数据库设计
  - ALTER TABLE outbox_events ADD COLUMN causation_id UUID
  - ALTER TABLE notifications ADD COLUMN causation_event_id UUID
  - 索引：(aggregate_type, aggregate_id, causation_id)

第 4-7 天:  生产者采用
  - outbox.NewEvent() 添加 causationID 参数
  - 消息服务在写入 message.sent 时传递 message.read 的 causation_id
  - 评论服务类似

第 8-10 天:  排序变更
  - FetchUnpublished 添加因果排序（按因果链而非 created_at 排序）
  - Relay 注入因果上下文到处理程序

里程碑 4: 事件溯源就绪
```

### 风险与缓解

| 风险 | 可能性 | 影响 | 缓解 |
|---|---|---|---|
| 每个用户幂等性计数增加数据库负载 | 中 | 中 | 使用覆盖索引 (`uid`, `expires_at` 含 `WHERE expires_at > now()`) 使 COUNT 成为仅索引扫描 |
| 每个端点限流配置过于复杂 | 低 | 高 | 默认行为（全局桶）保持不变；每个端点覆盖以 `rate_limit.endpoints` 下的简单 YAML 映射表达 |
| 通知 CreateOrCollapse ON CONFLICT 与 GORM 配合不佳 | 中 | 中 | 降级到 `SELECT FOR UPDATE` + 纯 SQL（Postgres 特定）在 GORM 日志表达式中 |
| 死信重放产生重复通知 | 低 | 中 | 重放使用新事件 ID；通知的幂等性是事件级；幂等性密钥（如果存在）覆盖重叠窗口 |
| 因果关系 ID 成为架构负担 | 中 | 低 | 所有生产者可逐步采用；旧事件保持向后兼容排序 |

### 关键设计决策

**决策 1：应使用什么幂等性逐出策略？**

A）**首先到期先清除（FIFO）** ——驱逐给定用户最旧的密钥。
- 优点：实现简单；行为可预测
- 缺点：可能驱逐一个仍在进行中的密钥，导致重复

B）**最久未使用（LRU）** ——驱逐最近未完成/释放的密钥。
- 优点：保留最近使用的密钥；降低重复风险
- 缺点：需要跟踪 `last_accessed_at`

C）**严格配额** ——拒绝新密钥直到现有密钥过期。
- 优点：DB 存储完全有界；无重复风险
- 缺点：过度拒绝合法重试

**推荐：C 用于默认配额，A 用于过量时降级（服务 429，但不驱逐）。** 安全胜于吞吐量。

**决策 2：通知折叠是否应该使用 `SELECT FOR UPDATE`？**

A）**序列化隔离** ——以可序列化级别运行整个处理程序。
- 优点：Postgres 可串行化是非阻塞的（乐观）；重试对冲突透明
- 缺点：对重试复杂；增加应用复杂性

B）**悲观锁定** ——在查找折叠目标之前 `SELECT ... FOR UPDATE`。
- 优点：简单；竞争直接解决
- 缺点：阻塞（低竞争下的可忽略延迟）

C）**ON CONFLICT 纯 SQL** ——用条件 `ON CONFLICT` 包裹在一个函数中。
- 优点：对应用代码完全透明；原子操作
- 缺点：Postgres 特定；函数需要维护

**推荐：C（ON CONFLICT 纯 SQL）** 用于经典场景。避免应用层的锁定复杂性，利用 Postgres 的 INSERT ... ON CONFLICT 作为单语句原子 UPSERT。如果 GORM 抽象失败，降级到 B（SELECT FOR UPDATE 在事务中）——这仍然更简单，且比序列化重试循环更可预测。

**决策 3：死信重放是否应该创建新事件 ID？**

A）**重用相同 ID** ——重置 `retry_count=0, dead_letter=false`。
- 优点：保留事件 ID；重放后对下游透明
- 缺点：幂等性密钥（如果匹配）可能阻止重放；事件修改了一个"不可变"的记录

B）**新 ID** ——创建具有相同 payload、event_type、causation_id 但新 ID 的新事件。
- 优点：事件溯源完整性——事件一次不可变；重放是操作者的显式操作
- 缺点：日志中的"重复"事件

**推荐：B（新 ID）。** 事件溯源 101：事件是不可变的。重放是一个新事件。Relay 负责幂等性。

---

## 总结

代码库基础非常稳固。五个发现的问题代表了安全/正确性（P0）与运维/韧性（P1）问题，而非结构性问题。具体来说：

| 发现 | 严重性 | 根本原因 | 修复 |
|---|---|---|---|
| 幂等性存储耗尽 | 安全（P0） | 每用户创建时缺少配额检查 | ON CONFLICT 前增加 COUNT |
| 通知折叠竞争 | 正确性（P0） | 非事务性读-改-写 | CreateOrCollapse ON CONFLICT |
| 每端点限流 | 活性（P1） | 固定密钥格式不区分端点 | 分层桶策略 |
| 死信运维盲区 | 运维/可观测性（P1） | Outbox repo 从 Admin 中遗漏 | 新增 API + 指标 |
| 因果排序 | 设计（P2） | Event/Notification 中缺少 CausationID | 新增 schema 字段 + 排序逻辑 |

**推荐执行顺序**：P0 修复在单个 2 周的阶段性冲刺中→P1 运维在后续 2 周的冲刺中→P2 因果关系在按需基础上。不需要新增基础设施。不需要架构重组。数据库原语（COUNT、ON CONFLICT、SELECT FOR UPDATE）加上进程桶已足够。
