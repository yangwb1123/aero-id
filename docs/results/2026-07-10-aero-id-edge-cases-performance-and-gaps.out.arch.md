现在我已经全面了解了实际代码库，可以提供一份深思熟虑的架构分析了。实际实现比设计文档和验证报告所描述的内容都要丰富得多。

---

# 架构分析：aero-id

## 1. 架构评估

### 1.1 优势

**严格的单向依赖层**是目前为止最有力的架构决策。实际的代码库执行了清晰的 `handler → service → repository/storage` 约束，这带来了几个有形的好处：

- **双协议传输一致性**：HTTP 和 gRPC 共享同一套 `service` 接口，消除了 REST 与 RPC 之间的行为偏差。No "but it works in REST" bugs。
- **可测试性**：`service` 位于依赖倒置边界，可以通过 mock repo/storage/SSO 进行单元测试，无需启动服务器。
- **错误隔离**：repository 返回原始 GORM 错误，service 将其转换为 `AppError`，handler 渲染它——每一层只处理自己的抽象。

**事务性发件箱模式**是一个关键的可靠性增益。`message.Create`、`comment.CreateWithReplyBump` 和 `message.MarkReadWithEvent` 都在同一数据库事务中写入发件箱事件。这意味着：

- 如果数据库提交失败，事件永远不会发出
- 异步处理（通知、webhooks）不会因为请求路径中的竞态条件而丢失
- Relay 使用指数退避和死信处理，提供 at-least-once 语义

**每个后台守护进程的 goroutine 生命周期管理**（`Relay.Start`、`Sweeper.Start`、`TokenCache.RunCleaner`、`GormStore.RunCleaner`、`Limiter.RunCleaner`）都返回 `stop func()`，在关闭时被调用——这是优雅退出的正确模式。

**无锁 SSO 缓存**（`sync.RWMutex` 用于 `items` 字段）以及后台清理 goroutine 是务实的选择。对于节点级缓存，分布式缓存协调的开销是不合理的。

### 1.2 限制与架构债务

**发件箱轮询与单事件 DB 写入**（方向一）是值得关注的成熟度缺口。实际代码在每次 `Dispatcher.Handle` 调用时执行单次 `INSERT INTO notifications` 用于三个事件类型。这对于当前负载是可行的，但有几个隐含的限制：

- 每个通知一次 DB 往返，即使在消息批量发送的情况下
- 没有跨事件类型对通知行进行批量插入
- `notificationFor` 中的 switch 硬编码了三种事件类型——添加新事件类型需要修改 dispatcher，破坏了 OCP

**上下文流失**（方向四）是一个真实的问题。`relay.go`、`sweeper.go`、`TokenCache.RunCleaner`、`Limiter.RunCleaner` 和 `GormStore.RunCleaner` 都使用 `context.Background()`。在实践中，这意味着：

- 父上下文取消（例如 HTTP 请求超时）不会传播到后台工作
- 没有分布式追踪上下文与异步处理关联
- 后台 DB 查询使用与请求路径不同的上下文跟踪

**搜索结果拼接而非排序合并**（方向五）是一个功能差距。当用户搜索所有三种类型时，响应包含：

1. 最多 `limit` 条消息（按全文搜索排序）
2. 然后最多 `limit` 条评论（按全文搜索排序）  
3. 然后最多 `limit` 个文件（按全文搜索排序）

这有三个问题：
- 结果总是按消息→评论→文件排序，而不是按关联性
- 每类结果最多消耗 `limit` 个槽位，跨类型混合时总计数不可预测
- 没有类型层面的分页或总分页

**软删除级联缺口**（方向三——核心主张）是合理的技术债务。删除文件不会级联删除其分享记录。删除消息不会级联删除其反应。删除评论不会级联删除其子评论。当资源被删除时，这会留下孤立数据。当前的行为在功能上是正确的（软删除隐藏行，数据通过 GC 清理），但缺少级联操作会产生跨表一致性的窗口。

### 1.3 关键设计决策评估

| 决策 | 评估 |
|---|---|
| **无自动迁移**（D10） | 正确。生产环境中多副本前竞争是真实问题。迁移应存在于 CI/CD 流水线中。 |
| **评论两层嵌套**（D5） | 正确。深度嵌套的评论树增加了 UI 和查询复杂性，而回报递减。 |
| **GORM 而非 sqlx** | 在当前复杂度下可以接受。如果添加跨多个表的复杂 OLAP 查询，应重新评估。 |
| **发件箱而非消息队列** | 务实。发件箱 + 轮询对当前负载来说是可以的。当需要发布/订阅到多个消费者、延迟至少一次交付或外部重试管理时，切换到真正的消息代理是有意义的。 |
| **每用户 SSE hub，非 WebSocket** | 正确的权衡。SSE 对推送通知来说更简单，单向，与 HTTP/2 配合良好。当需要双向通信时切换到 WebSocket。 |

---

## 2. 扩展方向

### 方向 1：统一搜索基础设施（P1）

**为什么需要**：当前的"拼接三种类型"方法在用户跨越消息、评论和文件进行搜索时，导致不可预测的排名。随着数据增长，相关性差距会成为产品的缺陷。

**核心挑战**：
- PostgreSQL 全文搜索在不同类型之间无法很好地进行排序（`ts_rank` 分数在消息、评论和文件之间不可比较）
- 每类使用独立 `limit` 意味着分页语义在跨类型搜索中无法正常工作
- 文件 FTS 只索引 `name`（迁移 `000016`），所以文件搜索只在文件名与查询匹配时有效，而不是文件内容

**预期的架构变更**：
```
当前：searchService 循环遍历三种类型并拼接

建议：统一搜索索引
     - 创建物化视图或联合搜索表，通过类型感知权重标准化排名分数
     - 或者引入外部搜索索引（Meilisearch / Typesense）进行跨类型全文搜索
     - 将搜索结果包装在包含排序顺序和类型感知分页光标的结构中
```

**对现有系统的影响**：
- 向后兼容：添加 `/v2/search` 端点，带有统一的排序和分页；`/v1/search` 保持现有行为（已弃用）
- 内部重构：`searchService.Search` 签名不变，但实现使用统一查询
- FTS 迁移：添加全文搜索特定迁移以支持内容索引（文件描述等）

**选项**：

| 选项 | 复杂度 | 延迟改进 | 运维开销 |
|---|---|---|---|
| A：带有加权排名物化视图的 PG FTS | 低 | 中等 | 无 |
| B：Meilisearch 独立服务 | 中等 | 高 | 新服务维护 |
| C：保留现状但添加类型内分页 | 低 | 无 | 无 |

**推荐**：选项 A，因为 PG 已经具有所需的构建块（`tsvector`、`ts_rank`、物化视图），无需外部依赖性。

---

### 方向 2：使用消息队列的异步事件处理（P1）

**为什么需要**：当前发件箱轮询 + 单事件 DB 写入模式无法扩展到高吞吐量。每个通知一次 `INSERT` 是在串行事件处理后进行的。向系统添加更多事件类型（`file.shared`、`comment.liked`、`user.blocked`）会使线性瓶颈更加严重。

**核心挑战**：
- 当前 relay 每次轮询迭代最多处理 100 个事件（`defaultBatchSize`），但为每个事件调用 `Dispatcher.Handle` → `notifs.Create`
- 使用 `context.Background()` 意味着事件处理没有分布式跟踪关联
- 告死信事件会一直保留，直到达到最大重试次数，占用轮询带宽
- webhook 通过 `outbox.NewChain` 链接，意味着 webhook 失败会延迟 notification 交付（反之亦然）

**预期的架构变更**：
```
当前：Relay → Handler (Dispatcher [+ Webhook])

建议：Relay → EventBus → [Dispatcher, Webhook, AuditLogger, ...] 作为独立消费者
     - 介绍事件总线抽象（内存或 RabbitMQ/NATS）
     - 将 Dispatcher.Handle 拆分为特定事件的消费者
     - 并行处理批处理中的事件（具有优雅的速率限制）
     - 将上下文传播添加到所有后台路径
```

**对现有系统的影响**：
- 发件箱保持不变（事件仍以事务方式写入）
- `outbox.Handler` 接口保持不变——添加一个 `FanOutHandler` 包装器，将事件分派给注册的消费者
- Dispatcher 获得自己的 goroutine 池，具有速率限制和重试
- 无需更改现有业务代码（`service_share.go` 等）

**上下文传播策略**：
```
当前：context.Background()
建议：context.WithValue(context.Background(), "trace_id", event.TraceID)
     - 在创建出箱事件时，从请求上下文传播跟踪元数据
     - 在 Relay 中，将跟踪 span 包装在处理周围
     - 聚合指标：事件延迟、消费者延迟、失败率
```

---

### 方向 3：面向领域的模块边界（P2）

**为什么需要**：当前 `internal/service` 包同时包含接口和实现。随着系统发展，这导致了包之间脆弱的循环引用（评论引用文件存储用于目标验证；消息引用文件存储用于附件）。正式化模块边界可以防止包依赖的混乱。

**核心挑战**：
- `internal/comment/repository.go` 导入 `internal/outbox`——评论模块知道基础架构事件模式
- `internal/message/repository.go` 导入 `internal/outbox`——同样的问题
- 发件箱事件负载类型（`messageSentPayload`、`commentRepliedPayload`）在 notification dispatcher 中定义，这些模块中没有形式化的事件契约

**预期的架构变更**：
```
当前：
    internal/{file,message,comment} - 混合域/仓库
    internal/outbox - 基础架构
    internal/notification - 针对发件箱事件类型进行 switch

建议：
    internal/domain/events.go - 类型化事件定义（Event接口 + structs）
    internal/{file,message,comment} - 纯仓库，通过域事件向外发射
    internal/outbox - 仅抽象（事件存储接口）
    internal/notification - 注册为域事件驱动消费者
```

**对现有系统的影响**：
- 将发件箱负载类型从 Dispatcher 移动到 `domain` 包（`event.MessageSent`、`event.CommentReplied` 等）
- 将 GORM 特定的 `gorm.DB` 从仓库构造中抽象出来（仓库接收 `*gorm.DB`，但应接收接口）
- 将发件箱事件创建与仓库事务分离（使用事件收集模式）

---

### 方向 4：存储 GC 和数据生命周期管理（P2）

**为什么需要**：当前 GC 扫荡器（`internal/storage/gc/sweeper.go`）仅处理软删除的行和孤立 blob。它不处理：

- 消息和评论的旧内容版本（编辑历史增加表 bloat）
- 为已删除用户（SSO 后）孤立的数据
- 跨垂直领域（删除文件 → 级联删除消息附件引用 → 通知）的数据生命周期策略

**核心挑战**：
- 多个垂直领域的级联删除涉及协调（删除文件 → 确定哪些消息引用它 → 清理通知 → 清理共享记录）
- 编辑历史可能很有价值（合规性、用户支持），但需要存储策略
- 当前 GC 只在删除后清理 blob，不参与主动保留管理

**预期的架构变更**：
```
当前：仅文件 GC (sweeper.go)

建议：统一数据生命周期管理器
     - 每个实体的保留策略配置
     - 死信通知自动清理（阅读后或 TTL 后）
     - 多步骤级联删除工作流（通过发件箱事件协调）
     - 修剪编辑历史（保留最后 N 个版本或最后 M 天）
```

**对现有系统的影响**：
- 添加 `cascade-delete` 发件箱事件类型
- 添加数据生命周期配置（`data_retention` 配置结构）
- 添加 `DataLifecycleManager` 作为与 GC 扫荡器配对的第二个后台组件

---

### 方向 5：文件共享通知与事件（P1）

**为什么需要**：目前，分享和取消分享文件不会生成通过通知系统的任何事件。用户分享文件时，接收者不会收到通知。这是产品缺口——分享是一种协作动作。通知提供认可（知道新内容何时可用）和反滥用信号（跟踪谁在分享什么）。

**核心挑战**：
- 共享接收者可能被分享者屏蔽——通知必须在分发前检查屏蔽表
- 批量分享（向 `n` 个接收者分享一个文件）可能产生 `n` 个通知——考虑了速率限制
- 对文件的取消分享可能应默默地执行（不通知前接收者），但审计日志坚持

**预期的架构变更**：
```
在 service_share.go 中：
    ShareFile: 添加 outbox.NewEvent("file", id, "file.shared", ...)
    UnshareFile: 添加 outbox.NewEvent("file", id, "file.unshared", ...)

在 dispatcher.go 中：
    添加 "file.shared" 和 "file.unshared" 到 notificationFor switch
    为接收者创建 "file.shared" 通知

在文件存储库中：
    当分享接收者被删除时，添加级联行删除（或 GC 清理）
```

**对现有系统的影响**：
- 极小。对 `dispatcher.go` 添加两处修改（与现有 `message.sent`/`message.read`/`comment.replied` 模式一致）
- 通过共享存储库检查幂等性（如果接收者已看到通知，则跳过）
- 为 `file.shared_notification_type` 添加通知偏好检查

---

## 3. 接口设计建议

### 3.1 模块接口设计原则

**发件箱事件契约**：事件负载类型目前嵌入在 dispatcher 中。它们应该位于单独的 `internal/domain/events.go` 文件中：

```go
// Event types — 从 dispatcher 提取到 domain
type MessageSentEvent struct {
    MessageID domain.ID `json:"message_id"`
    FromUID   string    `json:"from_uid"`
    ToUID     string    `json:"to_uid"`
}

type FileSharedEvent struct {
    FileID      domain.ID `json:"file_id"`
    OwnerUID    string    `json:"owner_uid"`
    GranteeUIDs []string  `json:"grantee_uids"`
}
```

这是必要的，以便：
- 仓库可以在不导入 notification 包的情况下引用事件类型
- 单元测试可以使用类型化事件而不进行 JSON 解析
- 未来的消费者可以通过检查类型注册来发现事件类型

**服务工厂应接收接口，而非具体结构体**：在 `app.go` 中，服务是用具体仓库类型构建的：

```go
msgRepo := repository.NewMessageRepository(gdb, outboxRepo)
msgSvc := service.NewMessageService(msgRepo, fileRepo, ssoClient, ...)
```

这已经相当干净了——`NewMessageService` 接收 `message.MessageRepository` 接口，而不是 `*messageRepo`。好。但是 `fileRepo` 作为 `*fileRepo` 传递，而不是作为 `file.Repository`——这是个微小但可修复的细节。

### 3.2 抽象层决策

**需要：工具式的事件注册**。当 `dispatcher.notificationFor` switch 每次需要编辑 dispatcher 以添加新事件类型时，它就违反了 OCP。引入注册表模式：

```go
type EventHandler func(ctx context.Context, e *outbox.Event) *domain.Notification

type Dispatcher struct {
    handlers map[string]EventHandler
    ...
}

func NewDispatcher(notifs Repository, opts ...) *Dispatcher {
    d := &Dispatcher{
        handlers: make(map[string]EventHandler),
        notifs:   notifs,
    }
    // 注册核心事件类型
    d.Register("message.sent", d.handleMessageSent)
    d.Register("message.read", d.handleMessageRead)
    d.Register("comment.replied", d.handleCommentReplied)
    return d
}

func (d *Dispatcher) Register(eventType string, h EventHandler) {
    d.handlers[eventType] = h
}
```

这允许 future 模块注册自己的处理程序，而无需修改 dispatcher。

**不需要（目前）：单独的查询层**。GORM 对表驱动工作负载来说没问题。如果需要跨多个实体的复杂 OLAP 查询，引入 `internal/query` 包只有此时才合理。根据 YAGNI，目前不要引入。

### 3.3 向后兼容性

**搜索 API**：当前的 `GET /v1/search?q=...&kind=messages,comments,files&limit=20` 返回扁平的热点数组。要支持统一排序：

1. 添加 `/v2/search`，返回统一排序（`sort_order` 字段和类型感知光标）
2. `/v1/search` 使用 `Deprecated` 响应头继续工作
3. 现有客户端无影响

**通知 Webhook**：链模式（`outbox.NewChain(dispatcher, webhookHandler)`）以 FIFO 顺序运行两个处理程序。如果 webhook 端点宕机，它会延迟通知交付。切换到扇出模式会改变递送保证——但可以通过配置控制，无需重大变更。

---

## 4. 技术选型

### 4.1 是否需要外部消息队列

| 论点 | 赞成 | 反对 |
|---|---|---|
| 发件箱轮询缩放 O(n) 事件 | ✔️ | |
| 需要至少一次交付保证以供审计 | ✔️ | |
| 当前工作负载很小 | | ✔️ |
| Postgres 在单个节点上可以处理很多 | | ✔️ |
| 添加消费者需要代码更改 | ✔️ | |
| 运维复杂度增加 | | ✔️ |

**决策**：保留当前发件箱轮询。当任何情况发生时，迁移到 NATS / RabbitMQ：
- 发件箱事件吞吐量超过每秒 500 个事件
- 添加第三个消费者（通知 + webhooks + 审核）
- 需要不同消费者组的独立重试策略

**时间框架**：可能在每天活跃用户达到约 10 万时。

### 4.2 是否需要外部搜索引擎

| 论点 | 赞成 | 反对 |
|---|---|---|
| PG FTS 已可用 | | ✔️ |
| 跨类型排序在 PG 中不工作 | ✔️ | |
| Meilisearch 零配置 | ✔️ | |
| 运维: 2 个 vs 1 个进程 | | ✔️ |
| PG FTS 在 < 50 万行时有效 | | ✔️ |

**决策**：首先通过物化视图优化 PG FTS。如果响应时间超过 200 毫秒或排名不可预测，再评估 Meilisearch。PG 的 `ts_rank` 归一化对于早期使用来说已足够。

### 4.3 自建 vs 采购：文件存储管理

当前架构已经位于 `storage.FileStorage` 接口之后，这是一个正确的抽象。关于 GC 扫荡器：

- **自建**：当前实现（sweeper.go）恰好在安全方面出错（引导查询、最小孤儿年龄）。对当前规模来说足够了。
- **采购**：外部存储管理器（例如 MinIO 生命周期策略）可以处理 blob 过期，但不能处理引用计数——这是由并发的去重上传所必需的。

**决策**：保留自建 GC。只有当存储跨多个区域时，才考虑 MinIO 生命周期策略。

---

## 5. 实施路线图

### 优先级调整

| 优先级 | 方向 | 理由 |
|---|---|---|
| **P1** | 文件共享通知（方向 5） | 产品差距，增长痛点，实施简单 |
| **P1** | 上下文传播到后台（方向 4 的一部分） | 可观察性差距，调试困难，影响所有后台守护进程 |
| **P1** | 统一搜索排序（方向 1） | 用户体验差距，影响所有搜索用户 |
| **P2** | 事件驱动的模块边界（方向 3） | 架构债务，但增量可解决 |
| **P2** | 发件箱→消息队列迁移（方向 2） | 可扩展性，但仅在压力下才需要 |
| **P2** | 数据生命周期管理器（方向 4） | 运维债务，仅在行数 > 1 亿时重要 |

### 阶段划分

**阶段 1：即时修复（1-2 周）**

| 任务 | 文件 | 复杂度 |
|---|---|---|
| 向 `ShareFile`/`UnshareFile` 添加发件箱事件 | `service_share.go`、`dispatcher.go` | 低（3 个新事件处理程序） |
| 将 `context.Background()` 替换为中继、扫荡器的带跟踪上下文 | `relay.go`、`sweeper.go` | 低 |
| 将 `notificationFor` switch 替换为事件注册表 | `dispatcher.go` | 低 |

**里程碑**：带有通知的文件共享、后台任务的跟踪跨度、可扩展的事件注册表

**阶段 2：搜索改进（2-3 周）**

| 任务 | 复杂度 |
|---|---|
| 创建跨类型搜索的 PG 物化视图 | 中等（需要加权排名公式） |
| 添加 `/v2/search`，带有统一排序和类型感知光标 | 低 |
| 弃用 `/v1/search`，具有 6 个月生命周期 | 低 |

**里程碑**：相关搜索结果，跨类型的正确分页

**阶段 3：模块边界（3-4 周）**

| 任务 | 文件 | 复杂度 |
|---|---|---|
| 将事件类型从 dispatcher 移动到 `internal/domain/events.go` | 领域层 | 低 |
| 从仓库提取 `*gorm.DB` 依赖到接口 | `repository.go` 文件 | 中等 |
| 添加事件收集模式（仓库收集事件，发件箱刷新它们） | 出箱层 | 中等 |

**里程碑**：无循环包依赖，发件箱从具体 GORM 类型解耦

**阶段 4：可扩展性（仅在达到阈值时触发）**

| 任务 | 触发条件 |
|---|---|
| 发件箱→NATS/RabbitMQ 迁移 | 出箱事件吞吐量 > 500/秒 |
| 外部搜索索引 (Meilisearch) | 搜索延迟 > 200ms 或用户抱怨相关性 |
| 数据生命周期管理器 | 表大小 > 50GB 或每用户行数 > 10 万 |

### 风险与缓解措施

| 风险 | 可能性 | 影响 | 缓解措施 |
|---|---|---|---|
| 统一搜索排序产生比拼接更差的结果 | 中等 | 高 | A/B 测试 /v1 vs /v2；保留两者并行运行 1 个月 |
| 发件箱事件类型增长变得不可管理 | 低 | 中等 | 事件注册表模式（阶段 1 的一部分）在发生之前解决这个问题 |
| 共享通知放大流量（批量分享→大量通知） | 中等 | 低 | 按类型对每个接收者进行速率限制（已在 dispatcher 中） |
| 死信事件填满表 | 低 | 中等 | 对死信事件进行告警（`dead_letter = true`）和 TTL 清理 |
| 在 GC 期间发生并发上传导致孤立 blob | 低 | 低 | 最小孤儿年龄（已在 sweeper 中）；内容去重检查 (CountAnyByStorageKey) 可防止竞争 |

---

## 总结

aero-id 代码库展示了**坚实的架构基础**，尽管其最初的设计文档（2026-05-22）预测的范围要窄得多。实际的实现添加了正确的基础设施组件——事务性发件箱、SSE 推送、速率限制、幂等性、指标、存储 GC——早在用户要求它们之前。这为在竞争压力到来之前解决功能差距（搜索、通知覆盖、上下文传播）提供了舒适的空间。

需要注意的关键架构债务：
1. **事件类型注册耦合**（dispatcher 中的 switch 语句）——在阶段 1 使用注册表模式修复
2. **上下文流失**（后台守护进程的 `context.Background()`）——在阶段 1 修复，以启用正确的可观察性
3. **搜索排名**（拼接而非合并）——阶段 2，对用户体验影响最大
4. **共享域缺口**（缺少文件共享通知）——阶段 1 修复，这是一个可见的产品差距

代码库目前的形状足够健康，可以并行进行阶段 1 任务。不需要基础设施架构变更——没有消息队列，没有外部搜索引擎，没有新的数据库——直到业务增长 10-100 倍以上。
