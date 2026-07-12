现在我已经对代码库进行了深入了解。让我来撰写出架构分析报告。

---

# aero-id 架构评估与技术设计报告

## 1. 架构评估

### 1.1 当前架构优势

**分层严格，职责清晰。** 代码库严格执行 `handler → service → repository`（及 `storage`/`sso`）的单向依赖。`app.go` 作为组合根统一装配所有组件。这是当前最重要的架构优势——它使得将单体推广到多实例所需的每次更改都集中在可预测的位置。

**特定优势：**

| 属性 | 证据 | 评价 |
|--------|---------|--------|
| 传输对称性 | HTTP 和 gRPC handlers 调用相同的 `service.AdminService`、`service.FileService` 等 | 消除逻辑分支；未来新传输（WebSocket、MCP）有明确的开销模式 |
| 事务边界 | 域写入 + outbox Append 在单个 DB 事务内完成（参见 `service/message.go` → `OutboxRepo.Append(ctx, tx, e)`） | 出站交付的原子性保证。这是必不可少的。 |
| 后台进程模式 | `outbox.Relay`、`storage/gc.Sweeper`、`idempotency.RunCleaner` 都遵循 `Start(interval) → stop func()` 合约 | 统一的生命周期管理。GC 扫除器已确立可复用的模式。 |
| 配置与密钥 | Viper + env overlay；密钥严格通过 env 提供 | 无密钥提交风险。清除启动时验证。 |
| SSO 状态机 | 客户端具有断路器（Closed/Open/Half-Open）+ 陈旧缓存回退 + 健康探测 | 妥善处理 SSO 中断场景；陈旧回退意味着 SSO 中断不会导致全面停机。 |
| 测试策略 | 真实的 PG 用于集成（testcontainers）、模拟用于服务、bufconn 用于 gRPC | 高可靠性信号；非模拟的仓库测试可捕获 SQL 语义错误。 |

### 1.2 当前架构局限

**局限 1：假设单实例进程状态。** 最严重的架构限制。`hub.Hub` 是一个进程内 `sync.Map` 订阅者。`notification.Dispatcher` 调用 `d.pusher.Publish(n.RecipientUID, ...)`，后者仅遍历本地订阅者的 channel。在多实例部署中：

- 连接到实例 B 的用户连接到实例 A 的 outbox relay → 产生通知 → 推送本地 hub → B 上的用户看不到它
- 重连恢复（`limit=50`）是脆弱的——高频用户可能丢失通知
- 速率限制器也是每实例的（参见 `app.go` 中的注释：*"distributed limiting would need shared state"*）

**影响：** 水平扩缩容目前会破坏通知交付。产品问题（P1）。

**局限 2：幂等性仅适用于 HTTP。** gRPC unary 调用不携带 Idempotency-Key 标头。即使在 HTTP 上，窗口期竞争也存在：`idempotency middleware` 在 `c.Next()` 之后调用 `Complete()`，而 outbox 事件是在该窗口内通过域事务写入的。如果中游者在此窗口内崩溃并重试，域行（使用 `OnConflict`）不会重复，但 outbox 事件却会——原因是在 outbox 表上 `(aggregate_type, aggregate_id)` 没有唯一约束。

**影响：** 数据正确性错误。重试的 HTTP POST 和任何 gRPC 写入都会产生虚假的 outbox 事件 → 重复的通知/Webhook。属于 P1。

**局限 3：软删除孤儿无系统级策略。** 每种功能在删除父行时有自己的行为：
- `file_shares`：没有 FK，没有 `deleted_at` 列——文件软删除后，分享记录完全孤立
- `message_reactions`：`ON DELETE CASCADE`——仅对物理删除有效；软删除后，反应行持续存在
- `comment_likes`/`comment_reports`：FK 单向，无 CASCADE

GC 扫除器仅存在于 `storage/gc/sweeper.go`（文件行 + blob 回收）。没有通用的框架来处理跨实体的级联软删除。

**影响：** 低速退化。在百万行级别，独立查询（"我点赞了哪些评论？"）必须 JOIN `comments.deleted_at IS NULL`，否则会显示已删除的内容。目前属于 P3。

### 1.3 架构负债

| 负债 | 严重程度 | 理由 |
|--------|----------|---------|
| 幂等性 + outbox 窗口期竞争 | **高** | 错误正确性。生产数据中的重复通知。 |
| SSE Hub 非分布式 | **高** | 阻碍水平扩展。部署架构的硬限制。 |
| 软删除孤儿 | **中** | 无即时代码路径会出错，但数据随时间退化，且治理欠佳。 |
| 每实例速率限制器 | **低** | 在负载均衡器后可接受，但无共享状态意味着突发可能不均。 |
| 无分布式缓存 | **低** | 在单个实例下运行良好；扩展到 N > 2 个实例时，每个实例都命中 SSO 来解析用户。 |

---

## 2. 扩展方向

### 方向 A：跨实例事件总线（集群感知的 Hub）

**为什么需要：** 当前的单进程 Hub 是横向扩展的硬障碍。一旦 aero-id 部署在多个副本之后，断开连接的用户在重新连接之前不会收到通知（最多 50 条，需 3-5 秒）。对于任何严肃的生产部署，这是一个 P1 级别的产品缺陷。

**核心挑战和技术难点：**
- 需要轻量级的发布/订阅层，所有实例共享。**选项：**（a）Redis Pub/Sub，（b）PG `LISTEN/NOTIFY`，（c）NATS 或 RabbitMQ 的外部消息代理。
- Hub 的 `pub()` 目前直接在调度程序 goroutine 中遍历本地 channel。引入网络跃点会增加延迟并可能阻塞中游者。
- 分区耐受性：如果一台实例失去与总线的连接，它应该回退到本地广播（最终一致）。

**预期架构变更：**

```
                    ┌─ outbox relay ──→ Dispatcher ──→ Hub.pub()
                    │         ↓                    ↓
                    │   [current]           [sync.Map local subs]
                    │
                    │   [future]
                    │         ↓                    ↓
                    │   Dispatcher ──→ Hub.pub() ──→ local subscribers
                    │                      ↓
                    │               Event Bus Client ──→ Redis Pub/Sub / PG NOTIFY
                    │                                    ↓
                    │                               Other instances
```

- `hub.Hub` 获得一个可选的 `EventBus` 接口：`Publish(uid, event)` 和 `Subscribe(uid) chan Event`
- 当存在总线时，`pub()` 除了发布到本地 subscriber 外，还调用 `EventBus.Publish`
- 后台 goroutine 监听 `EventBus.Subscribe` 并将远程事件注入本地 hub 的 per-uid subscriber 映射

**备选方案评估：**

| 方案 | 延迟 | 运营开销 | 一致性 | 推荐度 |
|--------|-------|-------------|-------------|----------|
| Redis Pub/Sub（独立或 Elasticache） | <1ms | 中（管理 Redis） | at-most-once，无持久化 | ⭐⭐⭐ 最佳整体方案 |
| PG `LISTEN/NOTIFY` | ~1ms | 无（复用现有 PG） | at-most-once，单通道 8KB 负载 | ⭐⭐ 对于 MVP 可接受，但扩展性不佳 |
| NATS（JetStream 启用） | <0.5ms | 高（额外基础设施） | at-least-once，持久化 | ⭐⭐⭐ 当团队已有 NATS 时 |
| 外部轮询（无总线） | 3-5s | 无 | at-least-once | ⭐ 降低到 3-5s 重连，失去实时性 |

**对现有系统的影响：**
- Hub 的 API 不变——现有订阅者不受影响
- `notification.Dispatcher` 无需更改（它调用 `hub.Pusher.Publish`）
- `app.go` 需要一个新的配置部分 `hub.bus.type`（`none` | `redis` | `pg`）
- 对以当前单实例模式运行的部署无影响（`bus=none`）

### 方向 B：统一的幂等性 + Outbox 去重

**为什么需要：** 目前，具有相同幂等性键的 HTTP POST 的同步重试可以创建重复的 outbox 事件，即使域行是幂等的（通过 `OnConflict`）。同样，gRPC 写入完全缺乏保护。这是一个数据正确性问题。

**核心挑战和技术难点：**
- Outbox 表当前没有用于去重的唯一约束。添加 `UNIQUE(aggregate_type, aggregate_id, idempotency_key)` 将键关联到事件，但 gRPC 没有幂等性键概念。
- 对于 gRPC，去重必须在应用级别使用请求哈希或业务键（例如，`message.sent` + `{from_uid, to_uid, content_sha}`）完成。
- 迁移：现有的 outbox 行可能有 `idempotency_key IS NULL`。唯一约束需要为空值为一个 sentinel（例如，`coalesce(idempotency_key, '')`）。

**预期架构变更：**
- 向 `outbox_events` 添加 `idempotency_key TEXT` 列
- 创建部分唯一索引：`CREATE UNIQUE INDEX ... ON outbox_events(aggregate_type, aggregate_id) WHERE idempotency_key IS NULL` + `CREATE UNIQUE INDEX ... ON outbox_events(aggregate_type, aggregate_id, idempotency_key) WHERE idempotency_key IS NOT NULL`
- `outbox.Repository.Append`：使用 `ON CONFLICT DO NOTHING` 并检查 `RowsAffected`
- 向 `service.OutboxWriter` 接口添加 `WithIdempotencyKey(key string)` 选项
- HTTP 中间件传递幂等性键 → service → outbox
- gRPC 拦截器可选择从元数据中提取幂等性键，或生成基于内容的哈希

**对现有系统的影响：**
- 新模式要求 service 方法传播幂等性键（或空值）
- 现有行继续工作——NULL 兼容
- 对于 HTTP：更改在中间件层；service 层获知键
- 对于 gRPC：需要新的拦截器元数据合约

### 方向 C：通用软删除引用框架（Ref GC）

**为什么需要：** 当前的软删除政策是通过每种功能的 repo 方法零散地制定的。`files` 有专用的 GC 扫除器。`file_shares`、`message_reactions`、`comment_likes`、`comment_reports` 在父级软删除后全部保持孤立。需要一个统一的框架来防止数据退化，而无需向每个 `service.Delete` 方法添加级联逻辑。

**核心挑战和技术难点：**
- 相关实体有不同的模式：有些有 FK（`comment_likes.references comments(id)`），有些没有（`file_shares` 没有 FK）。框架需要映射声明（"表 X 引用表 Y 列 Z"）。
- 仅软删除——级联不应是物理删除，而应设置 `deleted_at`。
- 性能：扫描大表以查找孤立父级可能会很昂贵。改为基于触发器的方案。
- 与现有 GC 扫除器交互：文件行及其 blob 回收已经处理。ref GC 应处理非文件实体。

**预期架构变更：**
- 新包 `internal/refgc/` 遵循 `storage/gc/sweeper.go` 的模式（`Run`/`Start` 生命周期）
- 依赖声明：每个实体注册 `ParentRef{Table, FKColumn, ParentTable, ParentPK}` 或类似的配置
- 每个 pass：查询在过去 retention_window 内设置了 `deleted_at` 的父行 → 批量更新依赖行设置 `deleted_at = now()`
- 可选：审计日志记录级联删除
- 配置：`refgc.enabled`、`refgc.retention`、`refgc.interval`

**设计选项：**

| 方案 | 优点 | 缺点 |
|--------|--------|--------|
| **A：后台扫除器**（推荐） | 与现有 GC 模式一致；异步，无请求路径影响；retention 窗口允许 "未删除" | 次优一致性窗口 |
| **B：应用级级联（在每个 service.Delete 中）** | 即时一致 | 向每个 `Delete` 方法添加责任；违反 "service 只做一件事" |
| **C：PG 触发器** | 数据库强制；无应用代码更改 | 触发器不受版本控制（需单独迁移）；更难测试；GORM 绕过 |

**对现有系统的影响：**
- 现有的 `service.Delete` 方法保持不变——它们设置 `deleted_at` 就返回
- Ref GC 扫除器异步清理
- 与 `storage/gc` 无冲突——不同的关注点
- `file_shares` 需要 `deleted_at` 列 + 迁移

### 方向 D：通用 Outbox 事件类型系统（可观测性与可扩展性）

**为什么需要：** 当前 outbox 事件类型是散落在 `dispatcher.go` 的 switch 语句中的字符串字面量（`"message.sent"`、`"message.read"`、`"comment.replied"`）。添加新事件类型（例如，`file.shared`、`block.created`）需要修改 `notificationFor` switch 并可能构建新的链式 handler。没有注册表，没有模式验证，没有可观测性。

**核心挑战和技术难点：**
- 事件类型需要注册一个 handler 工厂和一个模式（protobuf 或 JSON schema）
- Outbox relay 的链式 `Handler` 接口目前是 `Handle(ctx, *Event) error`——简单的链式结构
- 添加可观测性：每个事件类型的计数器、投递延迟直方图、死信队列可见性
- Webhook 路由目前是一个全有或全无的配置（所有事件类型或配置的子集）。更细粒度的路由会有所帮助。

**预期架构变更：**
- `internal/outbox/eventtype.go`：事件类型注册表——将类型字符串映射到 `EventTypeInfo{Schema, HandlerFactory, MetricsLabels}`
- `dispatcher.notificationFor` 变得由注册表驱动，而非 switch
- Webhook 配置获得每事件类型的 URL 映射
- 指标：每个事件类型的 `outbox_events_published_total`、`outbox_events_latency_seconds`、`outbox_dead_letter_total`
- 死信队列 UI/管理 API（或至少可查询的 DLQ 视图）

**对现有系统的影响：**
- 现有 switch 驱动的代码可以适应注册表——更改是机械性的
- 新的事件类型声明自己并自动获得处理和可观测性
- 现有的 webhook 配置子集继续工作

### 方向 E：跨域搜索的通用索引策略

**为什么需要：** 目前搜索（`service/search.go`）使用特定领域的 PG tsvector 查询。文件、消息和评论各自有独立的搜索索引。没有统一的搜索体验（"找到关于 X 的一切"）。随着系统的增长，不同领域中的相同实体（例如，提到文件的评论）需要跨领域搜索。

**核心挑战和技术难点：**
- 当前搜索实现是每领域分开的——没有全局索引
- 文本提取尚未到位（参见设计文档中的 "AI 列预埋"）
- Proper 全文搜索需要：
  - 中文/日文/韩文分词（PG 原生不支持）
  - 文档排名（`ts_rank` 可以，但需要调优）
  - 分面（按类型、日期、所有者过滤）

**预期架构变更：**
- 选项 A（增量）：标准化现有 tsvector 索引——例如，为所有可搜索实体使用单一物化视图 `search_entries(entity_type, entity_id, owner_uid, content_tsv, created_at)`，每 5 分钟刷新一次
- 选项 B（外部）：集成 Meilisearch 或 Typesense 以实现即时搜索 + 分面
- "AI 列预埋"：添加 `extracted_text` + `embedding vector(384)` 列，由异步 worker 填充。这支持未来语义搜索

**推荐：** 选项 A 用于短期（利用现有的 PG），选项 B 用于中期（当搜索成为首要功能时）。选项 C（pgvector 嵌入）应保持作为第 3 阶段。

---

## 3. 接口设计建议

### 3.1 关键模块应遵循的接口原则

**原则 1：后台进程的 `Start(interval) → stop func()` 契约。** `outbox.Relay.Start`、`storage/gc.Sweeper.Start`、`idempotency.RunCleaner` 都遵循此模式。新后台进程（`refgc`、总线订阅者）必须遵循完全相同的形式。这使得 `app.go` 中的生命周期管理保持统一。

**原则 2：`Pusher` 接口抽象出交付机制。** 当前的 `hub.Pusher` 接口只有一个方法 `Publish(uid, eventType, data string)`。跨实例变体可以实现相同的接口，通过 Redis/NATS 或仅通过本地 `sync.Map` 进行路由。服务层无需更改。

**原则 3：通过选项模式实现可测试性。** 服务构造函数使用函数选项（`WithBlockChecker`、`WithAuditLogger` 等）。所有新服务都应遵循此模式，以避免大量构造函数签名。

**原则 4：对仓库层错误使用 `errors.Is` 进行语义检查。** 仓库返回原始错误（包括 `gorm.ErrRecordNotFound`）。服务通过 `errors.Is` 对它们进行检查，然后将其包装在 `AppError` 中。任何新仓库都必须保持此契约。

### 3.2 新抽象

**事件总线接口（方向 A）：**

```go
// Package bus defines a cross-instance event distribution contract.
// Implementations: redis (Pub/Sub), pg (LISTEN/NOTIFY), local (no-op for
// single-instance deployments).
type EventBus interface {
    // Publish sends an event for uid to all interested instances.
    Publish(ctx context.Context, uid string, evt hub.SSEEvent) error
    // Subscribe returns a channel that receives events for uid published
    // by other instances. The caller must consume from it.
    Subscribe(ctx context.Context, uid string) (<-chan hub.SSEEvent, error)
    // Close terminates the bus connection.
    Close() error
}
```

`Hub` 获得一个可选的 `EventBus`。当存在时，`pub()` 除了发布到本地 subscriber 外，还调用 `bus.Publish`。一个后台 goroutine 调用 `bus.Subscribe` 并将远程事件馈送到本地 `uidSubs` 映射。

**引用 GC 接口（方向 C）：**

```go
// RefGC declares a parent-dependent relationship the sweeper should enforce.
type RefRule struct {
    ParentTable   string // e.g., "files"
    ParentPK      string // e.g., "id"
    ChildTable    string // e.g., "file_shares"
    ChildFK       string // e.g., "file_id"
    // ChildExtraFilter is AND-ed into the UPDATE's WHERE (e.g., "deleted_at IS NULL").
    ChildExtraFilter string
}

type RefGCSweeper interface {
    RegisterRule(rule RefRule)
    Run(ctx context.Context) (Stats, error)
    Start(interval time.Duration) (stop func())
}
```

### 3.3 向后兼容性

所有建议的方向都设计为无中断：

- **事件总线**：`bus=none` 默认——行为与今天完全相同（单进程 hub）。启用时，仅添加功能。
- **Outbox 去重**：`idempotency_key` 列可为 NULL。现有行不受影响。唯一的 DB 更改是添加索引。`Append` 使用 `ON CONFLICT DO NOTHING`——当键相同时，现有 Append（无 idempotency_key）继续写入新行。
- **Ref GC**：扫除器默认关闭。现有的 `service.Delete` 方法保持不变。没有对查询路径的影响。
- **事件类型系统**：注册表是累加器——现有的 switch 语句替换为注册表查找，行为相同。
- **跨域搜索**：物化视图是新增的——现有查询保持不变。

---

## 4. 技术选型

### 4.1 应引入的新技术

| 方向 | 建议 | 备选 |
|--------|---------|----------|
| **事件总线** | **Redis**（主）或 **PG LISTEN/NOTIFY**（轻量） | NATS（需要外部基础设施） |
| **Outbox 去重** | **仅 DB 约束**——无需新依赖 | 应用级锁定（慢，分布式事务） |
| **Ref GC** | **纯 Go + SQL**——无需新依赖 | PG 触发器（更难测试/版本控制） |
| **事件类型系统** | **纯 Go 注册表**——无需新依赖 | 模式注册表（Confluent，过度设计） |
| **跨域搜索** | **PG tsvector 物化视图**（短期），**Meilisearch**（中期） | Elasticsearch（Ops-heavy） |

### 4.2 Redis 的评估标准（如果需要）

| 标准 | Redis 评分 | 备注 |
|----------|--------------|--------|
| **运营经验** | 中 | 团队可能已经运行 Redis 用于缓存 |
| **延迟** | <1ms（进程外，同可用区） | 对于 SSE 推送足够 |
| **持久性** | 不要求（Pub/Sub 默认无持久性） | 对于实时通知可接受；持久性通过 outbox + 重连回退提供 |
| **一致性** | at-most-once | 与当前单进程 hub 一致 |
| **扩展性** | 水平可扩展（Redis 集群） | 如果需要，支持 100+ aero-id 实例 |
| **成本** | 低（t3.micro 运行 1k+ 连接） | 与数据库成本相比可忽略 |

### 4.3 自建 vs. 集成

| 组件 | 决策 | 理由 |
|----------|--------|--------|
| 事件总线客户端 | **自建**（~100 行 Go + go-redis） | 接口很小；在 Hub 内集成；避免外部库 API 泄漏到域代码中 |
| Outbox 去重 | **自建**（唯一约束 + `ON CONFLICT`） | 数据库级别；无中间件；零依赖 |
| Ref GC 扫除器 | **自建**（遵循现有的 sweeper 模式） | `storage/gc` 中已确立的模式；添加表声明即可 |
| 全文搜索 | **集成 Meilisearch**（中期） | 在 N > 100 万行时，PG tsvector 扩展性不佳；Meilisearch 提供即时搜索、分面、中文/日文/韩文支持 |

---

## 5. 实施路线图

### 5.1 优先级排序

| 方向 | 优先级 | 理由 |
|----------|----------|--------|
| **方向 B：Outbox + 幂等性去重** | **P0**（下一个冲刺） | 数据正确性 bug。当前的窗口期竞争会在大流量下产生重复通知。低风险、高价值的更改（2 个索引 + 1 个 `ON CONFLICT` 子句）。 |
| **方向 A：事件总线** | **P1**（第 2 个冲刺） | 阻止水平扩展。产品关键功能（实时通知）。P0 完成后，它成为扩展的首要障碍。 |
| **方向 C：Ref GC** | **P2**（第 3 个冲刺） | 数据退化，非损坏。遵循已建立的 sweeper 模式。 |
| **方向 D：事件类型系统** | **P3**（正在进行） | 运营改善。可以与功能工作交错进行。 |
| **方向 E：跨域搜索** | **P3/第 4 阶段** | 功能增量。当搜索成为首要功能需求时执行。 |

### 5.2 阶段划分

**阶段 1：数据正确性（接下来 2 周）**

*里程碑：无重复的 outbox 事件，即使在并发重试下。*
- 迁移：向 `outbox_events` 添加 `idempotency_key` + 唯一索引
- `outbox.Repository.Append`：添加 `ON CONFLICT DO NOTHING` + `RowsAffected` 检查
- HTTP 中间件：将 Idempotency-Key 传播到 service 上下文
- gRPC 拦截器：从 gRPC 元数据中提取（可选），否则为每个请求生成基于内容的哈希

**关键风险：** 现有生产行没有 `idempotency_key`。唯一索引必须处理 NULL。**缓解措施：** 对非 NULL 键使用部分索引，对 NULL `aggregate_id` + `aggregate_type` 对使用单独索引。

*验证*：`make test && make test-integration` 绿色。压力测试：10 个并发 POST 使用相同的幂等性键 → 恰好 1 个 outbox 事件。

**阶段 2：水平扩展（接下来 3 周）**

*里程碑：通知可在 N > 1 个 aero-id 实例间可靠交付。*
- 引入 `internal/bus/` 包（接口 + Redis 实现 + noop 实现）
- Hub 获得可选的 `EventBus`；`pub()` 多播到本地 subscriber + 总线
- `app.go`：新增 `hub.bus.type` 配置（`none` | `redis`），`hub.bus.redis.url`，以及可选 `hub.bus.redis.channel`（默认：`aero-id:events`）
- 添加 Redis 到开发 docker-compose
- 扩展测试套件以涵盖多实例场景（启动 2 个应用实例，通过一个发送事件，在另一个上验证）

**关键风险：** Redis 引入新的运营依赖和潜在的网络故障模式。**缓解措施：** Noop 总线是默认设置。Redis 故障记录为警告但不崩溃——实例回退到仅本地交付。

*验证*：2 个实例的 docker-compose 设置。通过实例 A 创建消息 → 实例 B 上的用户 SSE 客户端收到通知（<1s 延迟）。

**阶段 3：数据生命周期治理（第 5-6 周）**

*里程碑：软删除引用在 retention_window 内得到清理。*
- `internal/refgc/` 包按照 `storage/gc/sweeper.go` 的模式
- 声明规则：文件 → file_shares（通过 `file_id`）、评论 → comment_likes（通过 `comment_id`）、评论 → comment_reports（通过 `comment_id`）、消息 → message_reactions（通过 `message_id`）
- 向 `file_shares` 添加 `deleted_at` 列（当前缺少）
- 配置：`refgc.enabled`、`refgc.retention`（默认 7 天）、`refgc.interval`（默认 1 小时）
- 在 `app.go` 中注册

**关键风险：** 扫除器在依赖行被清理后更新它们，如果查询恰好在这两个行之间执行，可能会产生暂时不一致的视图。**缓解措施：** 扫除器在单个事务中运行 UPDATE，并且 retention 窗口意味着父级已经软删除至少 7 天——到那时，不一致的视图在功能上是预期的。

*验证*：设置场景：文件 + 分享 → 软删除文件 → 运行扫除器 → 分享行 `deleted_at` 已设置。集成测试验证。

**阶段 4：运营成熟度与搜索（第 7-8 周）**

*里程碑：事件类型可见性 + 死信队列管理 + 初始跨域搜索。*
- 事件类型注册表（取代 `dispatcher.notificationFor` 中的 switch）
- 每个事件类型的指标（发布、失败、延迟、死信）
- 死信队列管理 API（列出、重试、丢弃）
- 物化视图 `search_entries` 用于统一搜索
- 可选的 AI 列预埋：`files.extracted_text`、`messages.extracted_text`

*验证*：`/metrics` 显示每个事件类型的 `outbox_events_published_total`。管理 API `GET /v1/admin/outbox/dead-letters` 返回死信。

### 5.3 风险矩阵

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|------------|--------|--------------|
| Redis 中断（事件总线） | 低 | 中（实时通知降级） | 回退到仅本地 hub；outbox 持久性意味着重连恢复可以工作；添加断路器 |
| Outbox 去重迁移阻塞写入 | 低 | 高（服务中断） | 在低流量时段运行迁移；`idempotency_key` 列 DEFAULT NULL，因此 ALTER TABLE 是 INSTANT |
| Ref GC 意外清理活跃引用 | 低 | 中（数据丢失） | retention 窗口（默认 7 天）提供安全网；仔细测试 `WHERE` 子句 |
| gRPC 幂等性键冲突 | 中 | 低（错误的去重） | 基于内容的哈希 + 每个域的命名空间前缀可最小化冲突 |
| 跨域搜索物化视图刷新性能 | 中 | 低（陈旧索引） | 增量刷新（PG 14+ `REFRESH MATERIALIZED VIEW CONCURRENTLY`）；5 分钟刷新间隔 |

### 5.4 决策日志

| # | 决策 | 理由 | 时间范围 |
|---|----------|--------|-----------|
| D1 | 对 outbox 去重使用 DB 级 `ON CONFLICT`，而非应用锁定 | 零依赖、可测试、原子性。应用级锁定是分布式、脆弱且乐观的。 | 现在（阶段 1） |
| D2 | 事件总线：默认为无操作，可选择 Redis | 保持单实例部署零依赖。Redis 是广泛理解的运营依赖。 | 阶段 2 |
| D3 | 引用 GC：后台扫除器，而非请求路径级联删除 | 与现有的 `storage/gc` 模式一致。请求路径影响为零。允许 "未删除"（删除保留时间窗口）。 | 阶段 3 |
| D4 | gRPC 幂等性：可选元数据键 + 基于内容的回退 | 强制幂等性会破坏现有的 gRPC 客户端。基于内容的回退保守但安全。 | 阶段 1 |
| D5 | 跨域搜索：从 PG 物化视图开始，需要时迁移到 Meilisearch | 零新基础设施。物化视图刷新对于 <10M 行足够。Meilisearch 提供即时搜索、分面、中文/日文/韩文支持。 | 阶段 4 |
