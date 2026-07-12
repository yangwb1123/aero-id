现在我已彻底理解代码库。以下是我的完整架构分析。

---

## 1. 架构评估

### 1.1 优势

当前架构在几个关键方面表现出色：

**分层严格，职责分离**。`handler → service → repository/storage/sso` 的单向依赖被严格执行，没有任何基础设施泄漏的痕迹——所有业务逻辑都恰好在 `internal/service/` 中，路由/HTTP 逻辑在 `internal/handler/http/` 中，gRPC 逻辑在 `internal/handler/grpc/` 中。

**出站事件模式（Transactional Outbox）实现精良**。出站事件在创建/标记已读/删除消息的同一数据库事务中写入，因此下游通知变为可靠而非尽力而为。出站事件的重试逻辑包括指数退避、死信、事件清理器、基于 Tick 的轮询以及 MetricsSink 的可观测性——这是现实世界中生产就绪的实现。

**SSO 客户端精心设计**。双重缓存策略（令牌缓存 + 用户缓存）、带熔断功能的阶梯式健康检查、StaleTTL 用于 SSO 宕机时的降级、可配置的重试，全部通过单一 `sso.Client` 提供。

**存储抽象**。`FileStorage` 接口对于面向文件的系统来说恰到好处——它有 `Put`、`Get`、`Delete`、`Stat` 以及用于直接上传的 `PresignGet/PresignPut`。可选 `Lister` 接口保持 GC 逻辑的整洁。

**带有背压处理的 SSE Hub**。通道缓冲区为 16，在慢速消费者时静默丢弃——这比阻塞发布者更可取。根据配置选择性逐出空闲桶的速率限制器也不错。

**工程约束**。500 行的文件大小限制、50 行的函数长度、10 的圈复杂度——根据 `AGENTS.md`，这些已强制执行。代码库遵循这些约束。

### 1.2 局限性

**共享状态缺失（水平扩展的瓶颈）**。这是最大、最根本的限制：

| 组件 | 当前范围 | 问题 |
|---|---|---|
| 速率限制器 | 每个实例内存 | 在 L7 LB 后面有两个实例时，速率限制变为 2x |
| 幂等性存储 | GORM（每个 PG） | 实例 #1 可能接受具有幂等键的请求，但实例 #2 在 PG 上不会看到它 |
| 出站事件中继 | 每个实例轮询 | 两个实例获取相同事件 → 重复通知 |
| SSO 缓存 | 每个实例内存 | 在 N 个实例上缓存放大 N 倍 |

**出站事件中继是高可用性的关键故障点**。`FetchUnpublished` 使用无锁 `WHERE published_at IS NULL ORDER BY created_at ASC`。在水平扩展时，两个实例将在每个 tick 上获取并尝试处理相同的事件集。当前一个实例处理所有事件，另一个实例处理空集，但这是一个竞态条件，随时可能以重复通知的方式爆发。

**当前架构中遗漏了对话聚合**。`ListInbox` 返回一个平面时间线。`MarkConversationRead` 确实存在，但底层的方法是单向的——它读取收到但未读的消息，但这与"按对话聚合"不同。产品含义：移动端必须实现自己的按发件人分组逻辑，并且像"每个对话的未读计数"这样的功能是直接不可能的。

**群组消息缺少关键基础设施**。`MessageKind` 枚举预留但缺少 `GroupKind`。`Message` 结构体没有 `GroupID`、`GroupName`、多对多收件人。搜索层按 `(from_uid, to_uid)` 过滤——无法按群组过滤。

**文件后处理管线是一个功能空白**。`File` 没有 `status` 字段（"待处理"、"处理中"、"完成"、"失败"）。`File.Meta` 是一个 `map[string]any`，这意味着没有强类型模式。当前 GC 可以在管线处理完成之前清理 blob。这是目前架构中最危险的时间问题。

**Webhook 交付是单端点且不可管理**。`WebhookHandler` 被硬编码到配置中。没有订阅表、重试历史、速率限制、暂停、密钥轮换 API。单一端点是一个单点故障。

---

## 2. 扩展方向

### 方向 A：用于多实例操作的 Redis 共享状态层

**为什么需要**：当前每个实例状态限制了 aero-id 的水平扩展能力。在生产环境中运行两个实例而不共享状态意味着重复出站事件、双速率限制以及幂等键失效。Redis 是引入共享状态最直接的路径。

**核心挑战**：
- **原子性与一致性**：幂等性存储迁移到 `SET resource:… NX EX TTL` 很简单，但中继需要 `FOR UPDATE SKIP LOCKED` 或 leader 选举，而不仅仅是替换而已。
- **与现有 GORM 的协调**：出站事件的中继需要多个实例取到不同的事件。`SELECT … FOR UPDATE SKIP LOCKED` 在 PG 行锁级别工作（无需 Redis）。引入 Redis 只用于幂等性和可选缓存加速，而不是用于工作窃取。
- **缓存过期策略**：SSO 用户缓存使用多级 L1（本地） + L2（Redis）。如果不加注意，所有实例同时使其 L1 过期会导致"惊群效应"——必须使用 `singleflight` 或概率性提前重算。

**架构变更**：
```
当前: idempotency.Store (GORM)  → 迁移到 idempotency.Store (Redis + GORM 后备)
      relay.FetchUnpublished (无锁)  → 添加 FOR UPDATE SKIP LOCKED（无 Redis）
      sso.TokenCache (内存)  →  sso.TokenCache (内存 L1 + 可选的 Redis L2)
      sso.UserCache (内存)  →  sso.UserCache (内存 L1 + 可选的 Redis L2)
```

**影响**：短时间引入（800-1200 LOC，如分析所述）。Redis 是外部依赖，但 aero-id 已经使用 Postgres 和 MinIO——Redis 是标准基础设施。主要的架构决策点是 Redis 是可选（降级为 GORM）还是必需（GORM 后备仅用于启动）。

**权衡**：
- *选项 A1*：Redis 为必需，无 GORM 后备。更简单、更一致，但 Redis 宕机意味着幂等性丢失。
- *选项 A2*：Redis + GORM 后备（写入两者，从 Redis 读取）。与上述文档一致，但存在状态分歧的窗口。
- *选项 A3*：仅 `FOR UPDATE SKIP LOCKED` 用于出站；幂等性保持 GORM；SSO 缓存保持内存。零新依赖，但多实例扩容仅部分解决。

我推荐 **A3 → A2 阶段性推进**。首先用 `FOR UPDATE SKIP LOCKED` 修复出站中继（这纯粹是 SQL——零运维成本）。然后，如果扩容压力证明合理，再引入 Redis 用于幂等性。SSO 缓存在内存中已经足够（用户目录请求是批量且已缓存的）——Redis 带来的收益最低。

---

### 方向 B：对话聚合列表（提升至 P1.5）

**为什么需要**：这是"用户能感觉到的最小可用产品差距"。移动端应用目前接收平面消息列表，并必须在前端实现 O(n) 按对话分组。每个对话的未读计数、滑动标记已读、对话级通知整理——所有这些都需要后端聚合。从架构上讲，这复用现有基础设施最多（仅 SQL，无新依赖）。

**核心挑战**：
- **DISTINCT ON 的可伸缩性**：分析中提到的 `DISTINCT ON` 方案在用户有 1000+ 对话时会退化，因为 PostgreSQL 必须对所有 `(LEAST, GREATEST)` 对进行排序，而不能使用任何现有索引（现有索引是 `from_uid` 和 `to_uid` 单向的）。
- **`scan` 方向上的索引缺失**：当前索引是 `CREATE INDEX … ON messages (to_uid, created_at DESC)`。聚合查询需要 `(from_uid)` 和 `(to_uid)` 上的索引，因为我们在两个方向上遍历。对于 `DISTINCT ON`，PostgreSQL 需要 `(LEAST, GREATEST)` 上的表达式索引，以高效地跳过已见过的对。

**建议的方案（带有表达式索引的 Lateral Join）**：
```sql
-- 表达式索引使 DISTINCT 无需额外排序
CREATE INDEX idx_messages_conv ON messages (
    LEAST(from_uid, to_uid), GREATEST(from_uid, to_uid), created_at DESC
) WHERE deleted_at IS NULL;

-- 查询：每个对话最多 1 行，按最近消息排序
SELECT DISTINCT ON (conv_key) m.*
FROM messages m,
LATERAL (SELECT LEAST(from_uid, to_uid) || ':' || GREATEST(from_uid, to_uid) AS conv_key) ck
WHERE (from_uid = :actor OR to_uid = :actor) AND deleted_at IS NULL
ORDER BY conv_key, created_at DESC;
```

**架构变更**：在 `service/` 中添加一个 `ConversationList` 返回类型（`ConversationSummary` 列表，包含最后一条消息、未读计数、参与人信息）。`MessageRepository` 获得 `ListConversations(actor, page)`。没有新包。纯 SQL + 索引。

**影响**：~400-600 LOC。无新依赖。无迁移风险（如果查询较慢，索引是安全的在线 DDL）。

---

### 方向 C：文件后处理管线（与方向 A 联动）

**为什么需要**：目前上传按原样存储。无缩略图生成、无 PDF 预览提取、无病毒扫描。有了视频/音频类别后，这种缺失变得很明显。但方向 A（Redis/外部队列）是 P0 级别的依赖，因为：

- 当前出站中继是单进程、基于 Tick 的。如果管线 worker 附加到中继上，**转码可能阻塞出站事件投递**。大视频的 FFmpeg 转码可能需要数分钟——在此期间无法投递通知。
- 需要一个外部消息队列（Redis Streams 或类似）来解耦管线 worker。aero-id 的现有出站表不适合长时间运行的工作负载（它的 TTL 是秒级，而不是分钟级，事件行会被清理器清理掉）。

**核心挑战**：
- **File 状态字段**：当前 `File` 结构体中缺少 `status`。管线需要 `file_status` 枚举：`uploaded → processing → ready | failed`。读取路径必须考虑管线尚未完成的情况——例如，如果文件正在处理中，下载应该拒绝还是返回占位图？
- **GC 安全交互**：当前 `storage.GC` 会清理 `ref_count = 0` 的 blob。如果文件上传但管线尚未完成，GC 可能删除源 blob。解决方案：管线在开始时对存储持有临时引用，完成后释放。或者文件在就绪前有 `file_status != ready` 防止删除。
- **缩略图 ref_count**：缩略图 blob 的引用计数涉及需要与原文件独立的决策。建议：独立计数——删除文件 → 两个 blob 的 ref_count 都变为 0 → GC 清理两者。
- **管线超时**：FFmpeg 转码可能花费远超出站中继默认值的时长。管线需要独立进程 + 回调来更新 `File.Meta`。

**架构变更**（新包 `internal/pipeline/`）：
```
pipeline/dispatcher.go  — 订阅 Redis Stream（或 PG LISTEN/NOTIFY），启动 worker
pipeline/thumbnail.go  — 特定于图片的缩略图生成
pipeline/video.go      — FFmpeg 转码（可选）
pipeline/worker.go     — 通用 worker 循环 + 状态更新

domain/file.go  — 添加 File.Status + Status enum (uploaded, processing, ready, failed)
```

**风险**：新增 2000-3000 LOC。外部 FFmpeg 依赖。XSS/SSRF 风险来自处理不受信任的用户内容——必须对上传的文件进行沙盒化。

---

### 方向 D：群组消息

**为什么需要**：群组消息是直接消息的自然演进。没有它，aero-id 无法为团队或社区场景服务。

**核心挑战**：
- **架构影响是深远的**。群组消息改变了消息传递模型从 1:1 到 N:M。这影响：
  - *消息模型*：`Message.GroupID`（可选），`group_participants` 表
  - *幂等性*：在群组上下文中保证"恰好一次"发送需要每个参与人的幂等性键，而不仅仅是每个收件人链路的。
  - *通知*："Alice 向群组 X 发了一条消息"应通知所有成员，但不应为发送者发送——且每个成员的偏好 + 屏蔽状态都必须独立检查。
  - *搜索*：当前按 `(from_uid, to_uid)` 过滤。群组消息需要 `group_id` 作为额外的过滤维度。
  - *索引*：新的复合索引 `(group_id, created_at DESC)` 对于群组对话列表是必需的。现有索引不覆盖这个方向。
  - *权限*：加入群组/离开群组/踢出参与者是全新的业务逻辑，需要审计跟踪。

**架构变更**（新包 `internal/group/`）：
```
domain/group.go  — Group, GroupMember, GroupMessage 实体
service/group.go — Create, Join, Leave, Kick, Send (群组), ListMessages
handler/http/  — 群组路由
handler/grpc/  — 群组 RPC
repository/    — GORM 持久化
```

**新的表**：`groups`、`group_members`，可能还有 `group_messages` 或 `messages` 上的新列。

**影响**：~1500-2000 LOC（如分析中所述）。新的审计事件类型。需要在出站调度器中注册新的 `group.*` 通知类型。中风险——跨越多层，但边界清晰。

**推荐方法**：先做**群组消息的核心实体 + 仅 HTTP 之上的基本 CRUD**。在**方向 B（对话聚合）**之后进行，因为群组消息自然扩展了对话概念——先有单对单聚合，再有群组聚合。

---

### 方向 E：Webhook 订阅管理

**为什么需要**：当前 webhook 是一个静态、单一端点。外部集成方无法订阅特定事件、轮换密钥或查看投递历史。随着 aero-id 被消费，每个客户端都需要独立、有弹性的 webhook 投递。

**核心挑战**：
- **背压**：单个有问题的 webhook 订阅者（持续 5xx）会堆积出站事件。需要 `max_consecutive_failures` + 自动暂停 + 告警。
- **签名轮换**：订阅者需要一个轮换机制，在某处存储多个活动密钥，并在订阅生命周期内优雅地切换。
- **投递历史**：查看最近投递尝试及其状态。"投递失败"的可见性对于调试外部集成至关重要。
- **速率限制**：每个订阅的速率限制——不要因为重试风暴而击溃外部系统。

**架构变更**（新表 `webhook_subscriptions`、`webhook_deliveries`）：
```
domain/webhook.go  — Subscription, Delivery 实体
service/webhook.go — Create, Update, RotateSecret, ListDeliveries, Pause, Resume
handler/http/      — 订阅 CRUD 路由
handler/grpc/      — 订阅管理 RPC
```

**影响**：~1200-1800 LOC。纯 CRUD 管理 + 出站投递。扩展 `outbox.Chain`——加上为每个订阅运行个别调度器，而不仅仅是一个通用的 `WebhookHandler`。

**注意**：这不应与**方向 C（文件管线）**混淆，后者使用外部队列。Webhook 投递是一个重试有限、最适合从现有出站表发起的同步 HTTP 调用。如果投递失败，事件在死信之前会保持发布未完成并重试——这正是现有中继设计的工作方式。

---

## 3. 接口设计建议

### 3.1 当前接口合同的评估

代码库中的接口设计整体出色：

- **`service/` 接口**是解耦的（`FileService`、`MessageService` 等），返回明确的结构体（`*FileList`、`*MessageList`），而不是 `(any, error)`。没有返回原始 go 结构体——因此调用方（HTTP/gRPC 处理程序）始终知道他们得到什么。
- **`repository/` 接口**被服务层使用，并且足够窄以避免泄漏。`MessageRepository` 有 16 个方法——这是合理的。它没有接近"上帝接口"。
- **`storage.FileStorage`** 很小（7 个方法），每个方法都有明确的后端行为契约（哪些返回 `ErrNotSupported`）。

### 3.2 新抽象的引入时机

**当前不需要新抽象层的地方**：

1. **对话聚合（方向 B）**：不需要新抽象。只是 `MessageRepository` 上新方法的问题。这是低摩擦高 ROIC 的原因。

2. **Webhook 管理（方向 E）**：不需要新抽象。完整的 CRUD（`Create/Update/Delete/List`）加上对 `outbox.Handler` 的修改以按订阅进行交付扇出。

**新抽象会带来价值的地方**：

1. **群组消息（方向 D）**：如果有多个群组相关包（`group`、`group_message`、`group_member`），`internal/group/` 包是合理的。但如果保持较轻量（`Message` 上的 `GroupID` + `group_participants` 表），它可以保留在 `internal/message/` 中。我建议**专门的 `internal/group/`** 包——群组管理（加入/离开/特权）与消息传递是独立的领域。

2. **文件管线（方向 C）**：`internal/pipeline/` 是一个高价值新包。它需要一个 `Pipeline` 接口：
   ```go
   type Pipeline interface {
       // Start launches the worker. Returns a stop func.
       Start(ctx context.Context) (stop func())
       // Status reports the pipeline's health.
       Status(ctx context.Context) PipelineStatus
   }
   ```
   每个管线阶段（缩略图、转码、病毒扫描）都应实现 `Stage` 接口或通过配置组合。管线编排不应在 aero-id 进程中——它应该作为一个单独的二进制文件运行（`cmd/pipeline/main.go`），通过回调 webhook 或直接数据库写入与 aero-id 通信。

3. **Redis 共享状态（方向 A）**：一个新的 `internal/pkg/sharedstate/` 包，封装 `RedisStore` + `GORMStore` 带降级逻辑。

### 3.3 向后兼容性

当前设计在不破坏现有客户端方面做得不错：

- **分页**使用 `(cursor, limit)`，其中游标是一个不透明的 ID。可以添加新字段而不破坏游标。
- **字段掩码更新**：`UpdateFileMetadataInput` 使用指向指针的字段（`Name *string`），其中 `nil` 表示"不更改"。这是明确且兼容的。
- **通知类型**是字符串。可以添加新类型。客户端忽略未知类型。
- **出站事件**是 JSON 有效负载。新字段在 JSON 中被静默忽略。

一个风险：**`domain.NotificationType` 是一个字符串常量，但调度器使用硬编码的 switch**。添加新的 `group.*` 事件类型意味着修改调度器中的 switch。没有扩展点。这是可接受的——switch 只有 7 个分支，按事件类型增长——但如果它超过 15 个事件类型，可能值得考虑 `map[string]NotifBuilder` 策略映射。

---

## 4. 技术选型

### 4.1 是否需要新依赖？

| 方向 | 新依赖 | 理由 |
|---|---|---|
| **方向 B（对话聚合）** | 无 | 纯 SQL |
| **方向 E（Webhook 管理）** | 无 | 纯 CRUD |
| **方向 D（群组消息）** | 无 | 纯 GORM |
| **方向 A（Redis 共享状态）** | **Redis** | 可选的——如果仅使用 PG FOR UPDATE SKIP LOCKED 则不需要。如果需要真正的共享缓存，则需要 Redis。 |
| **方向 C（文件管线）** | **Redis Streams** 或 **NATS** | 用于解耦管线的外部队列。或者，使用 PG LISTEN/NOTIFY，但缺少消费者组支持。 |

### 4.2 Redis 使用决策树

对于方向 A，这是推荐的方法：

```
需要哪个 Redis 功能？
├── 仅幂等性去重
│   └── 选项：SET NX EX — 仅 Redis（需要实例运行）
├── 出站事件工作窃取
│   └── 选项：PG FOR UPDATE SKIP LOCKED（无 Redis）— 推荐
├── SSO 缓存共享（减少冷启动时间）
│   └── 仅在 5+ 实例时才需要 — 选项：Redis
└── 速率限制协调
    └── 仅在 3+ 实例时才需要 — 选项：Redis SLIDING WINDOW
```

**推荐**：仅对幂等性和 SSO 缓存（如果需要）使用 Redis，方向 A 的相位为 P1。出站事件顺序保证使用 PG 级别的 `FOR UPDATE SKIP LOCKED`（P0，因为它阻止了生产级水平扩展）。速率限制保持每个实例（O(1) 且简单）。

### 4.3 外部队列（方向 C）

管线需要一个外部队列来解耦 worker。有两个选择：

| 选项 | 优点 | 缺点 |
|---|---|---|
| **PG LISTEN/NOTIFY** | 零新依赖。与现有出站事务协调。 | 无消费者组——如果两个 worker 监听，两者都获取事件。隐式去重但无法确认。缺乏背压。 |
| **Redis Streams** | 消费者组、确认、背压。已经是在考虑中的相同依赖。 | 方向 A 尚未引入 Redis。 |
| **NATS** | 云原生的轻量级。内置去重。 | 新的依赖类型。 |

**推荐**：如果方向 A 已交付 Redis，则使用 **Redis Streams**。如果没有，则使用 **PG LISTEN/NOTIFY 带 worker-level 锁**（`SELECT … FOR UPDATE SKIP LOCKED` 在一个专用的 `pipeline_jobs` 表上）。LISTEN/NOTIFY 通知 worker 去轮询，但锁确保只有一个人获取工作。这不需要新的基础设施。

### 4.4 自建 vs 采购

对于文件管线：
- **缩略图生成**：必须是自建的（FFmpeg/ImageMagick 调用），因为它直接在对象存储上操作。
- **病毒扫描**：通过 `clamav-daemon` 与 ClamAV 套接字集成（标准做法，非差异化因素）。
- **AI 审核**：如果需要，作为可选阶段通过回调与外部 API 集成。

对于群组消息：完全自建。这是核心域。

对于 Webhook 平台：自建——定制太重要了（每个订阅速率限制、暂停语义、按客户身份）。任何现成的解决方案都需要太多适配器。

---

## 5. 实施路线图

### 5.1 修订后的优先级矩阵

我确认原始分析的优先级排序，但有以下修正的依赖关系：

| 阶段 | 方向 | 依赖 | 估计 LOC | 风险 | 排序理由 |
|---|---|---|---|---|---|
| **P0** | 方向 B（对话聚合列表） | 无 | ~400-600 | 🟢 低 | 最高 ROIC。零新依赖。修复产品差距。 |
| **P0** | 方向 A 的子集（`FOR UPDATE SKIP LOCKED`） | 无 | ~50 | 🟢 低 | 一行代码。阻止生产中的重复出站事件。 |
| **P1** | 方向 D（群组消息——核心仅 HTTP） | 无 | ~1500-2000 | 🟡 中 | 最高功能价值。范围仅限 CRUD + 消息发送。通知支持是 P2。 |
| **P1** | 方向 A 的其余部分（Redis 共享状态） | Redis 依赖 | ~800-1200 | 🟡 中 | 为水平扩展解锁。也为方向 C 启用 Redis Streams。 |
| **P1** | 方向 C（文件管线） | Redis（如果可用）或 PG LISTEN/NOTIFY | ~2000-3000 | 🟠 中高 | 需要外部队列。与方向 A 联动。沙盒推荐。 |
| **P2** | 方向 E（Webhook 平台） | 无 | ~1200-1800 | 🟢 低 | 纯 CRUD + 现有出站集成。可以在后期部署而不中断。 |
| **P2** | 群组消息高级功能（通知、搜索、@提及） | 方向 D 完成 | ~800-1000 | 🟡 中 | 建立在前一阶段之上。 |

### 5.2 分阶段里程碑

**第 1 阶段：稳固基础（2 周）** — P0 项目

里程碑："无重复，无平面列表"
- 向 `FetchUnpublished` 添加 `FOR UPDATE SKIP LOCKED`
- 添加 `ListConversations` SQL + 表达式索引 + `ConversationSummary` 返回类型
- `MarkConversationRead` 现在有未读计数同步
- 产品展示：每个对话的未读列表，对话级标记已读

**第 2 阶段：群组消息——最小可行产品（4 周）** — P1 项目

里程碑："用户可以创建群组并发送消息"
- 表：`groups`、`group_members`
- 服务：`CreateGroup`、`JoinGroup`、`LeaveGroup`、`SendGroupMessage`、`ListGroupMessages`
- HTTP API：群组 CRUD + 消息发送
- 搜索：按 `group_id` 过滤（添加 `search_vector` 更新以包含 group_id）

**注意**：群组消息的**通知**在此阶段被推迟。群组消息只需提交到数据库。出站事件在 P2 中完成。

**第 3 阶段：水平扩展（3 周）** — 方向 A（Redis）

里程碑："生产就绪的多实例扩容"
- `internal/pkg/sharedstate/` 中的 `SharedStore` 接口（`GormStore` + `RedisStore`）
- 幂等性存储：如果配置了 Redis，`SET NX EX`，否则回退到 GORM
- SSO 缓存：添加可选的 L2 Redis 层
- 速率限制：如果配置了 Redis，则协调，否则保持每个实例
- 出站事件：保持 `FOR UPDATE SKIP LOCKED`（不需要 Redis）

**第 4 阶段：文件智能（4 周）** — 方向 C

里程碑："上传文件 → 自动缩略图生成"
- `File.Status` 字段 + 迁移
- 新 `internal/pipeline/` 包，带有 `Stage` 接口
- 阶段 1：缩略图生成（图片 → webp 缩略图）
- 阶段 2：视频转码（可选——仅 FFmpeg）
- GC 交互：只有在文件状态为 `ready` 时才允许 blob 清理
- GC 通过 `storage_refs` 持有临时引用

**第 5 阶段：完整集成（3 周）** — P2 项目

里程碑："具有可见性的 Webhook 平台 + 群组通知"
- `webhook_subscriptions` + `webhook_deliveries` 表
- 订阅 CRUD API
- 按订阅的出站中继扇出
- 自动暂停 + 告警（metric 发出）
- 群组通知（`group.message.sent` 事件 → 调度器 → 每个成员通知）

### 5.3 风险与缓解

| 风险 | 可能性 | 影响 | 缓解 |
|---|---|---|---|
| **重复出站事件生产** | 高（当前） | 中（重复通知） | **P0**：`FOR UPDATE SKIP LOCKED`。立即修复。 |
| **管线阻塞出站中继** | 中 | 高（通知延迟） | 隔离管线 worker 为单独进程。中继只触发，不处理。 |
| **FFmpeg 耗尽资源** | 中 | 高（OOM） | 按并发转码任务限制 worker。对大型视频设置超时 + 大小限制。 |
| **群组消息的幂等键** | 低 | 中（重复发送） | 使用 `(sender, group_id, client_idempotency_key)` 作为消息级别的幂等性。 |
| **Redis 变为 SPOF** | 低（如果正确配置） | 高 | GORM 回退路径。幂等性降级为"尽力而为"。 |
| **Webhook 事件堆积** | 低 | 中（磁盘压力） | `max_consecutive_failures` 自动暂停 + 告警。订阅读取清理程序。 |

### 5.4 关于原始分析的具体回应

**关于方向 A 的相位**：我同意方向 A 不应是 P0，而是 P0.5。但我想进一步——方向 A 的*子集*（`FOR UPDATE SKIP LOCKED`）*是* P0，因为它跨实例阻止重复出站事件。这是 1 行 SQL 变化，零风险，高影响。我将其重新分类。

**关于方向 C 联动方向 A**：我同意。我在[第 3 阶段：水平扩展](#第-3-阶段水平扩展3-周)中明确将其制定为顺序依赖。方向 C 在方向 A 之后（如果使用 Redis Streams）或与之同时（如果使用 PG LISTEN/NOTIFY）。

**关于对话列表的 DISTINCT ON 性能**：我提供了索引建议（表达式索引 + Lateral Join），使查询可伸缩到 10K+ 对话。原始分析的 SQL 对于 MVP 是可接受的，但应配备表达式索引。

**关于方向三的 GC 问题**：在[第 4 阶段：文件智能](#第-4-阶段文件智能4-周)中明确解决——`File.Status` 阻止在文件完成前删除，且管线持有临时引用。这与分析一致。

**关于方向四的向后兼容性**：`ConversationSummary` 返回类型是对 `ListInbox` 的补充而非替代。现有的 `ListInbox` 保持稳定。客户端可以迁移到 `ListConversations` 以获得聚合体验。

---

## 总结

aero-id 的代码库是我在 Go 微服务中见过的最干净实现之一。分层被严格执行，出站事件被正确实现，接口被设计为窄且专注。局限性是预期的——它们源于这是一个单实例的开启阶段，准备转向水平扩展和功能完整。

我推荐五阶段的序列（P0 → P2），从一个 1 行的 SQL 修复（`FOR UPDATE SKIP LOCKED`）开始，并在对话聚合上提供高价值的产品功能，使移动端不必在前端做分组逻辑。群组消息和文件管线是第二阶段——架构上后果更严重，但回报也相应更高。Webhook 平台是健康的 P2，当外部集成需求出现时按需交付。

任何这些方向的代码级可行性分析，包括精确的 API 签名、迁移设计或文件清单，请让我知道。
