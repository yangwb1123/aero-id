现在我有了对代码库的全面了解。让我来撰写架构分析。

---

# 架构分析：aero-id 服务

## 1. 架构评估

### 1.1 当前架构的优势

**严格分层。** 该代码库最突出的优势是其实施的严谨性。单向依赖（`handler → service → repository`）不仅体现在文档中，而且通过可检查的编译边界得到了严格执行。`service` 包是业务逻辑的单一真实来源；HTTP 和 gRPC 传输层共享它并通过共同的 DTO 集 `service.go:30-130` 进行通信。这是真正的双协议实现，而非事后应对。

**事务性发件箱模式。** `outbox` 包（`internal/outbox/`）是实现领域驱动设计 "最终一致性" 的最干净的 Go 实现之一。`repository.Create` 方法接收 `*outbox.Event` 参数，在同一 GORM 事务中追加事件；`Relay` 异步 drain。这使得通知传递（以及可选的 webhook）可靠——不是尽最大努力。`outbox/relay.go:42` 中的退避逻辑（指数退避，上限 1 小时）和 000018 中的死信处理是成熟的运维模式。

**SSO 依赖治理。** `internal/sso/` 包展示了正确的依赖管理：
- `singleflight.Group`（`client.go:92`）避免了令牌验证的惊群效应
- 熔断器（`breaker.go`）可防止级联故障
- 宽限期缓存（`TokenCache.GetStale`）允许在 SSO 短暂中断时继续服务
- `UserCache` 对 `fetchUsers` 使用回调进行批处理

**以安全为先的设计。** 所有权检查嵌入在 `service` 层（`canReadFile`、`canReadMessage`）。Block 感知渗透到消息发送和通知分发中。这些不是在中间件中浅层检查的权限，而是每个操作中的业务规则——这更安全，但也更难分析，正如下面关于 N+1 风险的讨论。

### 1.2 关键的设计问题

**死代码：第二个分发器。** `internal/notification/dispatcher.go` 中有一个 `notification.Dispatcher`，但从未被引入。实际使用的分发器是 `internal/service/notification.go` 中的 `service.NewNotificationDispatcher`。`internal/notification/dispatcher.go` 中的代码是更简单的早期版本。它应该被移除，或者正式确定为 `notification` 包的公共 API。截至目前，它在持续地产生迷惑。

**处理层与领域层之间的界限模糊。** `internal/block/` 是它自己的一座 "小岛"，混合了领域实体、存储接口和业务逻辑。`block.BlockChecker` 接口在 `internal/block/block.go` 中定义，但 `internal/service/message.go` 将它作为 `BlockChecker` 接口引用。block 中的逻辑（不能屏蔽自己、需要 uid）也与 `domain/` 模型重叠。这是一种应该在构建更多模块前清理的架构债务。

**通知分发到收件箱：缺少批量 N+1 优化。** `service/message.go:Inbox` 方法（第 73 行）对返回的每条消息都没有执行批量屏蔽检查。如果用户有 50 条收件消息，而其中 3 条来自他们屏蔽的发件人，收件箱将在每次页面加载时发出 50 次单独的 `SELECT` 语句。这是一个真实的 N+1 问题——在传输模式下是 O(n)，而不仅仅是文档中提到的每条消息的 `checkSendBlock`。

**发件箱中继缺乏扇出隔离。** `outbox/relay.go:67-99` 中的 `drain` 循环按顺序处理每个事件，调用 relay handler 来进行处理，而 relay handler 本身由 `outbox.NewChain(dispatcher, webhookHandler)` 组成。如果 webhook 端点对单个事件耗时 5 秒，整个派发批次都会延迟。100 个事件的批次可能在 webhook 上消耗约 500 秒（即使链式调用，每个事件 5 秒），而通知仍然正确地写入，但延迟会稳步增加。

**令牌缓存缺乏 LRU 上限。** `sso/token_cache.go` 使用 TTL 过期但没有容量上限。在拥有数十万活跃令牌的系统上，内存使用会无限增长。TTL 限制了存活时间，但令牌在到期前仍可能积累。`RunCleaner` 每 30 秒扫描一次，但这纯粹是时间驱动的，而非容量驱动的。

### 1.3 架构债务

1. **`internal/notification/dispatcher.go` 与 `internal/service/notification.go` 重复分发逻辑。** 前者应被移除，后者应被提升为 `notification` 包的公共 `Dispatcher` 实现。

2. **`pkg/ratelimit.Limiter` 是一个具体类型，而非接口。** 请求路径上的限流器中间件 `middleware/ratelimit.go` 依赖于具体类型 `*ratelimit.Limiter`。替换为基于 PG 的限制意味着更改中间件签名。`Limiter` 本应被抽象为接口。

3. **`internal/service/service.go` 混合了 DTO、辅助函数和服务接口。** 这是一个 353 行的文件，包含 `CreateFileInput`、`AdminService`、`BlockService`、`mustActor` 和 `canReadFile`。根据 500 行限制尚可，但按照项目的凝聚力标准，它耦合了多种职责。DTO 定义值得放在单独的文件中（例如 `types.go`），服务接口值得放在 `interfaces.go` 中。

4. **`internal/block/` 中的域模型复制。** `domain/` 包定义了核心实体，但 `internal/block/block.go` 内有自己的 `BlockChecker`、`Repository`、`Service` 和 `BlockList`。这是包设计上的一个分支——对等包可以有自己的接口，但域类型（如 `domain.Page`）的重复定义表明分层规则可能需要调整，以允许 `internal/block` 导入 `domain`。

---

## 2. 扩展方向

### 方向 A：全局审核工作台（P0——高业务价值，低工程成本）

**为什么需要：** 审核和用户信任是平台的基线功能。代码库中已经存在未连接的轮子：
- 迁移 000022：种子了 `roles` 和 `permissions`
- `repository/admin.go`：`HasPermission` 已实现
- `domain/admin.go`：定义了 `PermUserBan`、`PermContentModerate`，但没有端点

**核心挑战：** 没有数据模型过渡方面的挑战。最大挑战是**授权模型设计**——审核员是全局性的（可以审核任何内容）还是受限范围的（每个角色可审核）。代码库的基于权限的模型支持两者，但业务需要做出选择。

**预期的架构变更：**
- `bans` 表 + 存储库（约 80 行）
- `handler/{http,grpc}/ban.go` 带 `POST /v1/admin/bans` 和 `POST /v1/admin/unbans`
- 用于举报队列的跨作者端点（现有 `comment_reports` + `ListReports` 以作者为范围——需要全局 `ListAllReports`）
- 全局评论/消息隐藏——复用现有 `service.Hide`，添加权限守卫

**影响评估：** 轻微。仅添加新端点，不更改现有 API 签名。通知和发件箱不受影响。

### 方向 B：文件版本控制（P0——数据完整性，中等成本）

**为什么需要：** 文件版本控制对于数据平台来说是基础。对于上传后内容发生变化的文件，依赖 `file_id` 引用当前最新版本会导致引用断裂。然而，`storage_refs` 表（迁移 000021）已经按 storage_key 跟踪引用计数，这意味着 GC 不需要架构变更——它已经按单个 blob 工作，而不是按文件行工作。

**核心挑战：**
1. **共享语义：** `file_shares` 按 `file_id` 操作。如果 Bob 共享了版本 1，而 Alice 上传了版本 2，Bob 的共享现在指向 Alice 的内容。需要 `file_versions.version` 和共享时的版本固定。
2. **下载 URL 语义：** `FileService.DownloadURL` 默认返回最新版本。需要可选的 `version` 参数。
3. **内容去重：** 当前去重使用 `sha256(content) → existing file_id`。对于版本化，这需要按 `(sha256, owner_uid)` 的范围进行，而不是按单一的 `sha256`。

**预期的架构变更：**
- `file_versions` 表（外键到 `files`，递增的版本号，storage_key，checksum，size）
- `file.service_crud.go` 中的 `CreateVersion` 方法
- `DownloadURL` 的可选 `*int version` 参数
- 将 `file_shares` 的 PK 扩展为 `(file_id, version, grantee_uid)`——或者更简单，共享最新版本，但用可选版本覆盖

**影响评估：** 中等。存储后端本身不需要更改；blob 存储已经是键值对。元数据和共享逻辑需要更改。

### 方向 C：群组/多人消息（P1——高产品价值，高工程成本）

**为什么需要：** 群组消息是当今消息平台的基本要求。然而，架构隐喻需要根本性转变——从 1:1 消息（`from_uid ↔ to_uid` 索引范围）转为 N:N 会话（`conversation_members` 连接表）。

**核心挑战：**
1. **消息模型重构：** 当前 `Send(to_uid, content)` 签名贯穿全栈——`domain.Message`、`repository.MessageRepository`、`service.MessageService`、HTTP 和 gRPC 处理程序。添加 `conversation_id` 需要新的 `SendToConversation` 方法（或对现有方法添加可选参数，代价是更复杂的签名）。
2. **通知分解：** 当前通知分发器每个事件写入一个收件人。在群组中，`message.sent` 需要扇出到 N 个成员。逐条写入通知行在 N=100 时 O(N)，但在 N=100,000 时则行不通。需要批量 `CREATE`。
3. **Block 语义：** 在 1:1 中，`IsBlocked(A, B)` → B 无法给 A 发消息，简单明了。在群组中，如果 A 屏蔽了 B，而 Silvia（A 和 B 都知道的）创建了一个同时包含 A 和 B 的群组——A 会看到 B 的消息吗？B 会看到 A 的吗？存在三种合理的选择：(a) 保留双方可见，但屏蔽仅调整通知，(b) 使屏蔽者对被屏蔽者的消息完全不可见，(c) 在创建群组时检查“任何被屏蔽者”并拒绝。仅仅定义这个就是产品方面的工作。
4. **桌面消息列表：** 当前的 `Inbox` 返回个人对话（`WHERE (from_uid = ? OR to_uid = ?)` 排序后）。群组对话将与个人消息交错。需要一个统一的 `ConversationListView`，其模式为 `(last_message_at, participant_uids[], unread_count, conversation_type)`。

**预期的架构变更：**
- `conversations` + `conversation_members` 表
- 新的 `internal/conversation/` 包（不仅仅是现有 `message` 包的扩展——跨域职责需要独立的存储库和服务）
- 添加上下文参数（例如 `ConversationID *domain.ID`）后，`MessageService.Send` 方法签名更改
- 添加 `JoinConversation`/`LeaveConversation`/`ListConversations` 端点

**影响评估：** 重度。这是更改最多的方向。消息服务、通知模型、domain 实体、迁移——几乎所有非审核模块都需要更改。

### 方向 D：Webhook 订阅管理（P1——生态价值，中等成本）

**为什么需要：** 单一 webhook 端点（当前由 `outbox/webhook.go` 的 `WebhookConfig` 配置）是单例的。平台生态系统的消费者需要注册自己的端点。

**核心挑战：**
1. **扇出隔离：** 当前 webhook 处理程序是同步调用。对于 N 个订阅，需要 N 个 goroutine（或者一个工作池）。如果 1 个端点超时，它不能阻塞其他端点。
2. **递送历史：** `deliveries` 表是必需的用于操作可见性，但为递送记录创建 outbox 事件会引入 outbox 内的二次 outbox——一种递归反模式。递送记录应直接写入 DB（使用短超时），而不经过 outbox。
3. **订阅重新交付 backfill：** 当一个订阅注册时，它是否应接收过往事件？如果订阅者想要从 0 开始的事件流，可能需要初始 backfill——这超出了标准 outbox relay 的能力（relay 按事件顺序进行，不会回溯历史）。

**预期的架构变更：**
- `webhook_subscriptions` 表（URL、signing_key、event_types 筛选、状态、创建时间）
- `internal/outbox/subscription.go` ——管理订阅 CRUD
- goroutine 扇出器（`webhook_fanout.go`）——包装 Chain 以支持每个订阅独立的 HTTP 调用
- `deliveries` 表——侧写，带有短超时

**影响评估：** 中等。对核心消息路径没有影响。影响范围限于 outbox 包和新的 subscription 端点到处理层级。

**注意：** 文档建议将此作为 P0，但我将其降级为 P1。原因是单一 webhook 端点已经为集成用例提供了强大的支持。平台化是之后的产品增长方向，而非安全基线。

### 方向 E：分布式限流（P1——运维价值，低到中等成本）

**为什么需要：** 当前限流器是进程内（`pkg/ratelimit/ratelimit.go`，注释：“per-instance”）。在多副本部署中，一个用户可能以 N 倍于预期速率的方式被限制，因为每个进程独立地限制。对于写操作（`Send`、`Create`、`Upload`），这会造成安全风险。

**核心挑战：**
1. **读/写路径分离：** 公共路径（`/healthz`、`/readyz`）已经跳过了限流。写路径需要一个比读路径更严格的分布式限制器。PG advisory lock 方法适用于写路径，因为它只对每个写请求产生一次 DB 开销。读路径可以保持进程内。
2. **抽象是缺失的。** `middleware/ratelimit.go` 依赖于 `*ratelimit.Limiter`（具体类型）。在 P0 期间应将 `Limiter` 提取为接口。

**选项：**

| 方案 | 延迟 | 一致性 | 运维复杂度 |
|------|------|---------|-------------|
| A. PG advisory lock（写路径） | 每次写入增加 1-3ms | 强 | 低——不需要新模式 |
| B. Redis + 滑动窗口 | ~1ms | 读写最终 | 中等——需要 Redis 集群 |
| C. 混合：PG 写入 + 进程内读取 | 写入增加 1-3ms，读取为 0 | 写入强，读取弱 | 低 |

**推荐：** 从方案 C 开始（P0）。将 `Limiter` 提取为接口。对写路径使用 PG advisory lock，保留进程内限制用于读取。如果需要，可以热升级到方案 B（Redis）而无需更改签名。

**影响评估：** 轻微。限流器中间件已经是一个横切关注点。接口提取不改变任何服务逻辑。

---

## 3. 接口设计建议

### 3.1 接口设计原则

以下原则对所有新接口都适用：

1. **每个包一个接口文件，放在使用方，而非定义方。** 遵循 `internal/sso/token_cache.go` 的模式——`TokenCache` 定义在其消费方（SSO 客户端），使用方按值持有它。这样避免了包依赖循环。

2. **服务接口应保持一致性。** 当前 `service/service.go` 将 `AdminService`、`FileService`、`MessageService` 等都定义为接口。任何新服务（例如 `ConversationService`）都应遵循相同的模式：在 `service/service.go`（或拆分后的 `service/interfaces.go`）中定义接口，在 `service/conversation.go` 中实现。

3. **存储后端已经是抽象的——保持这种状态。** `storage.FileStorage` 接口是干净的，拥有 `Put`、`Get`、`Delete`、`PresignGet`、`PresignPut`、`Stat`、`NewKey`。`Lister` 是可选的。版本控制不需要更改此接口——版本控制是元数据映射，而非存储布局映射。

### 3.2 新抽象层

**需要：限流器接口。** 

```go
// internal/middleware/ratelimit.go 需要此变更
type RateLimiter interface {
    Allow(key string) bool
    RetryAfter() time.Duration
}
```

现有 `*ratelimit.Limiter` 将隐式实现它。添加 PG 变体只需要另外一个实现。

**可选：发件箱处理程序框架。** 当前 `outbox.Chain` 将处理程序安排在线性序列中。对于 Webhook 平台化，需要一个扇出处理程序：

```go
type FanoutHandler struct {
    handlers []Handler  // 每个都独立运行
    timeout  time.Duration
    pool     *workerpool.Pool
}
func (f *FanoutHandler) Handle(ctx context.Context, e *Event) error {
    // 为每个处理程序启动 goroutine；等待所有或超时
    // 聚合部分成功
}
```

这不会更改 `Relay` 的接口，只会更改编排方式。

### 3.3 向后兼容性

对于方向 C（群组消息），保持兼容性取决于 API 演进策略：

- **HTTP：** 对新端点使用路径版本控制（例如 `/v2/conversations`）。保持 `/v1/messages` 不变。
- **gRPC：** `MessageService.Send` 获得一个可选的 `conversation_id` 字段。`Send` 的现有调用者继续工作（`conversation_id = ""` → 1:1 始终是默认值）。

对于文件版本控制：

- `DownloadURL` 获得可选的 `?version=N` 查询参数。省略时默认为最新版本。
- 共享变体：`POST /v1/files/:id/share` 获得可选的 `{ "version": N }` 属性。

---

## 4. 技术选型

### 4.1 是否需要新的技术栈？

| 方向 | 需要的依赖 | 评估 |
|------|------------|------|
| 审核工作台 | 无 | 纯 SQL + 现有模式 |
| 文件版本控制 | 无 | 纯 SQL + 现有存储抽象 |
| 群组消息 | 无新依赖 | 纯 SQL 变更；如果期望 >10k 会话，考虑 PG 分区 |
| Webhook 平台化 | 无 | 纯 SQL + http.Client |
| 分布式限流 | PG（P0）或 Redis（P1） | PG：无新依赖。Redis：需要 go-redis |
| 未读计数物化 | 无 | 迁移 000023 已存在 |

**结论：** 对于所评估的任何方向，都不需要新的运行时依赖。引入 Redis 是选择性的优化，而非基线要求。

### 4.2 第三方依赖评估标准

代码库目前使用：
- `gin-gonic/gin`（HTTP 路由）
- `google.golang.org/grpc`
- `gorm.io/gorm`（ORM）
- `go.uber.org/zap`（日志）
- `google/uuid`（UUIDv4 和 v7）
- `go.opentelemetry.io/otel`（追踪）

这是一个合理的集合。任何新的依赖都应通过以下标准：

1. **许可证兼容性**（BSD/MIT/Apache 2.0——不是 AGPL）
2. **Go 1.26 兼容性**（从 module 需求推断）
3. **没有不必要的传递依赖**（例如，不要让限流器依赖 Redis 引用整个 go-redis 链）
4. **API 稳定性**（v1 标签或合理保证无破坏性变更）

### 4.3 自建 vs 采购

**限流器抽象：** 自建（约 80 行接口 + 实现）。这一领域（滑动窗口、令牌桶）过于简单，不值得使用外部库。PG advisory lock 实现约需 60 行。

**Webhook 平台化：** 自建。outbox 模式已就位；剩余的构建块（订阅 CRUD、扇出发送）是业务逻辑，而非基础设施。

**群组消息：** 完全自建。没有现成的“群组消息”引擎可以插入 1:1 消息模型并符合发件箱/通知约束。

---

## 5. 实施路线图

### 5.1 优先级

| 优先级 | 方向 | 理由 |
|--------|------|-------|
| **P0** | 审核工作台 | 安全运营基线；60% 已经就位 |
| **P0** | 限流器抽象（写入路径 PG） | 低投入，安全收益高，为 Redis 做好准备 |
| **P0** | SSE 分页修复 + Inbox N+1 | 现有 bug 影响正常功能 |
| **P1** | 文件版本控制 | 数据完整性，但非安全关键 |
| **P1** | 通知折叠（如果尚未启用） | 迁移 000020 就位；只需在 app.go 中设置 `WithDispatcherCollapse` |
| **P1** | Webhook 平台化 | 生态系统需求，但不是生产阻碍 |
| **P2** | 群组消息 | 最高产品价值，但由于架构影响也最高风险 |
| **P2** | 分布式限流 Redis | 仅在多副本部署成为瓶颈时才是必需的 |

### 5.2 阶段

```
Phase A（2 周）——安全与可靠性基线
  Day 1-3:  审核工作台基础（bans 表 + 端点，全局举报列表）
  Day 4-6:  限流器接口提取 + PG advisory lock 用于写入路径
  Day 7-9:  SSE 重播分页（replayMissed 中的循环）+ Inbox 批量 block 检查
  Day 10:   通知折叠启用（如果在 app.go 中配置了但未按文档设置）
  Day 11-14: 集成测试 + 文档 + 缓冲时间

Phase B（3 周）——数据平台化
  Week 1:   文件版本控制——file_versions 表 + CreateVersion 端点
  Week 2:   共享版本固定、DownloadURL 的 ?version= 参数
  Week 3:   去重适配、GC 确认、集成测试

Phase C（5-6 周）——平台化协作
  Week 1-2: Webhook 平台化——subscriptions 表 + CRUD + 扇出架构
  Week 3-5: 群组消息——conversations 表 + 成员资格 + send/get
  Week 6:   通知适配 + 屏蔽语义 + 集成测试

Phase D（按需）
  - Redis 限流器后端
  - 未读计数物化表（如果 000023 不够）
  - 容量规划工具
```

### 5.3 风险与缓解措施

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|--------|------|---------|
| 群组消息中的通知扇出在 N>1000 时 O(N) | 中 | 高 | 在 Phase C 之前进行批量插入基准测试；如果超出预期延迟，则从同步写入切换为异步物化 |
| Webhook 扇出中缺乏 Partial-success 语义 | 中 | 中 | 在 Phase C 将 partial-success 列为阶段 1 可交付物（“deliveries”表的模式） |
| 文件版本控制的共享语义使 API 复杂化 | 中 | 低 | 从“永远共享最新版本”开始；为显式版本固定添加 ?version= 作为后续优化 |
| 分布式限流在低流量下增加可察觉的延迟 | 低 | 低 | 基准测试 PG advisory lock 延迟；如果超过 10ms P99，添加本地缓存折返 |

---

## 总结

该代码库是为单体部署而精心设计的，具有明确的分层和强大的事件驱动基础。双协议实现（HTTP + gRPC）和事务性发件箱是其最大的工程优势。

需要考虑的四个关键架构要点：

1. **审核工作台应该首先完成。** 代码库已经完成了 60%——权限、角色种子、审核日志。缺少的 40%（封禁端点、全局审核队列）是纯粹的增量添加，对现有系统没有风险。

2. **群组消息是一个平台重塑，而非功能添加。** 这不是添加一个 `group_id` 列那么简单。它需要重新思考消息模型、通知扇出和屏蔽语义。在 Phase A 中至少应该做一个轻量级的技术选型（“是添加 `conversation_id` 到现有表，还是创建新的 `conversation` 包？”），这样 Phase C 的规划就能基于真实的约束而不是假设。

3. **发件箱应该保持原样。** 它是系统中最成熟的子系统。在它之上添加 Webhook 平台化是合理的，但不要重写 relay。扇出是唯一需要的架构变更。

4. **技术债务项目（`internal/notification/dispatcher.go` 死代码、`BlockChecker` 接口重复、缺少限流器抽象）应在设计工作与重构被触发之前**尽早解决。如果项目在 Phase A 之后开始对审核和群组进行规模化开发，这些债务中的每项都会成倍增加开发成本。
