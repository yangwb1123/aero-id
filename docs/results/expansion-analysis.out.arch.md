现在我已对整个代码库有了透彻的理解。以下是架构分析：

---

# aero-id 架构分析报告

## 1. 架构评估

### 当前架构的优势

代码库已从原始 MVP 大幅演进，经历了两次成熟度飞跃：ROADMAP 中的 5 个方向，以及后续的生产加固阶段。留下的架构形成了明确的特性组合。

**分层架构 — 严格执行。** 代码仍然遵循 handler → service → repository 的单向依赖。但是，分层已从扁平的文件（`internal/service/*.go`）重构为*按领域组织的包*（`internal/file/`、`internal/message/`、`internal/comment/` 等），每个包都有自己的驱动层——handler_http、handler_grpc、service、repository——以及配套测试。这是架构质量的显著提升：单个文件现在直接映射到一个域关注点，使文件大小约束自然满足。

**事务性 outbox 是正确的架构核心。** outbox relay 已成为可靠性的基石：它写入通知、驱动 SSE 推送，并通过 chain 模式支持可选的 webhook 投递。失败事件获得带指数退避的重试，最终进入死信队列。这是支撑当今所有异步行为的架构。

**双传输对等。** 38 个 gRPC RPC 和匹配的 HTTP 路由共享同一个服务层，具有统一的错误代码映射。这避免了传输偏离的问题——这是双协议代码库中常见的架构债务来源。

**已实现优雅的降级。** SSO 客户端拥有断路器、serve-stale 回退、singleflight 和带退避的重试。这意味着上游 SSO 宕机时，aero-id 会优雅降级，而不是全面瘫痪——这是重要的弹性属性。

### 约束限制

**AGENTS.md 中规定 500 行的文件大小限制正在被充分利用。** 一些文件（例如 `internal/app/app.go` 约 300 行，`internal/storage/gc/sweeper.go` 约 220 行）严格遵守这一约束。但领域包中的 service 文件（例如 `internal/message/service.go`）已经开始膨胀，多条并发线路正在逼近极限。重新审视 `internal/message/service.go` 中 40-50 行的函数——其中一些函数在单个方法中处理作者身份、阻塞检查和通知事件写入。

**事务性 outbox 在三个地方手动构造。** 消息、评论和通知包各自动态构建 outbox 事件负载。event type 常量散布在各处而非集中定义。`internal/notification/dispatcher.go` 中的 `EventType` 字符串——`"message.sent"`、`"message.read"`、`"comment.replied"`——在写入端和分发端都有字面量。这是一个等待维护问题的耦合点：添加新事件类型需要触及两个包，且编译器不检查拼写错误。

**SSE Hub 是单实例单体的。** `internal/hub/hub.go` 中的 `Hub` 是一个内存 uid → 订阅者注册表。无法跨多个 aero-id 实例共享。任何水平扩展尝试都会因以下问题而失败：(1) 连接到实例 A 的用户收不到实例 B 分发的通知，以及 (2) 在实例 B 上处理事件的 outbox relay 会尝试推送到实例 B 的本地 hub——该 hub 可能没有该用户的订阅者。

**限流器也是单实例的。** `internal/pkg/ratelimit/ratelimit.go` 在每个进程的内存中维护令牌桶。在 N 个实例上，每个用户的有效容量是配置值的 N 倍——除非做出改变，否则这是一个架构性的安全漏洞。

**Outbox relay 没有领导选举。** 每个 aero-id 实例都运行自己的 relay（`internal/outbox/relay.go`），轮询并处理同一组未发布的事件。在较松的约束下（使用 `UPDATE ... WHERE published_at IS NULL` 的乐观锁定），两个实例可以获取同一批事件并都尝试处理它们。通知调度器（`internal/notification/dispatcher.go`）在 `notifications` 表上没有唯一约束来防止重复行。在负载下，这将产生重复通知。

**存储引用计数存在并发窗口。** `internal/file/service_crud.go` 中的 `Create` 路径写入文件行，然后增量引用计数。`internal/storage/gc/sweeper.go` 中的 GC sweeper 在检查引用计数和执行 blob 删除之间有一个竞争窗口。`RefCounter` 接口减少了这个窗口（通过 `GetCount` 而非 `CountAnyByStorageKey`），但并未消除它。一个恰好在新文件行写入 blob 之前运行的清扫周期可能会删除该 blob。

## 2. 扩展方向

### 方向一：跨实例水平扩展（P0）

**为什么。** 这是从单实例部署转向多实例部署的门控依赖。没有这个，就无法实现高可用或弹性伸缩。该分析正确地将此识别为生产环境的首道闸门，但在考虑哪些组件需要共享状态方面，可以更进一步。

**核心挑战。** 三个不共享的状态孤岛：
1. **SSE Hub** — 内存中的 uid → 订阅者映射。实例 A 无法推送给在实例 B 上连接的用户。
2. **内存限流器** — 令牌桶是进程本地的，意味着 N 个实例 → 每个用户的有效容量为 N 倍。
3. **Outbox relay 竞争** — 没有 leader 选举，所有实例都处理事件。

**架构变更。**

对于 SSE 推送：PG LISTEN/NOTIFY 由分析推荐，这里再加一个考量——现有 outbox relay 是持久订阅者，可以驱动推送。但每个实例仍然需要知道向谁推送。更好的方法是将 SSE 消息路由委托给*单个 relay 实例*（通过 leader 选举确定），为整个集群分发通知。PG LISTEN/NOTIFY 可用于在实例之间广播推送命令，使 relay 成为唯一写入 hub 的实例。

对于 leader 选举，分析推荐 `pg_advisory_lock`。这是正确的——零额外基础设施，与 PG 事务生命周期绑定。但需要澄清的是：leader 选举影响的是 outbox relay 和（可选的）GC sweeper——而不是 HTTP/gRPC 服务器。*每个*实例都应服务 API 请求；水平扩展是关于*写入*协调的，不是读取。

对于限流，可插拔的 `LimiterBackend` 接口（分析中已描述）是正确的抽象。短期：内存实现作为默认。长期：Redis 实现（sliding window 或 token bucket），在 Redis 不可用时可降级到本地模式。

**对现有系统的影响。**
- SSE hub 需要额外的广播层（PG LISTEN/NOTIFY），这引入了一个新的 `hub.Backend` 接口
- Outbox relay 需要 leader 选举（`pg_try_advisory_lock`），增加了一个 `elected` 检查
- 限流器需要一个 `LimiterBackend` 接口和后台实现
- 这些变更被限制在 `internal/hub/`、`internal/outbox/` 和 `internal/pkg/ratelimit/` 中

---

### 方向二：群聊与会话（P1）

**为什么。** 这是产品价值最高的方向。当前 `messages` 模型（`from_uid`、`to_uid`）将系统限制为 1:1 私信。群聊解锁了最高数量的用户-to-用户交互模式。这也是*实现难度最高*的方向，因为权限模型（block 语义、@mentions、通知路由、阅读状态）比 1:1 消息复杂得多。

**核心挑战。**
1. **数据模型分离**：正如分析指出的，不应复用 `messages` 表。`conversation_messages` 表是新的领域，拥有自己的查询模式、自己的通知逻辑和自己的权限模型。
2. **Block 语义**：在群聊中，Block 对读路径与写路径的影响不同。分析正确地指出 A 屏蔽 B 意味着 A 看到 B 的消息为软删除（`deleted: true`，`sender: null`）。技术上，这要求在查询时进行 block 检查——每次会话消息查询都必须对每条消息进行 actor 级别的 block 筛选。这是 per-query 的 O(n) 扫描，除非有索引支持。
3. **阅读状态**：在 1:1 中，`mark_read` 很简单。在群聊中，阅读状态是 per-member 的。新的 `conversation_read_cursors` 表（`{conversation_id, member_uid, last_read_message_id}`）是必需的。
4. **通知路由**：消息发送到 N 个收件人，但应根据 block 状态和偏好筛选每个收件人。这要求从 outbox 事件处理程序进行 N 次查找，而不是当前 1:1 模式下的一次查找。
5. **@mentions**：需要消息体解析来提取提及的 uid，高优先级通知路由，以及提及 UID 的富文本呈现。

**架构变更。**
- 新增 `internal/conversation/` 领域包，结构类似现有包（handler_http、handler_grpc、service、repository）
- 迁移：`conversations`、`conversation_members`、`conversation_messages`、`conversation_read_cursors`
- Outbox 事件类型：`conversation.message_sent`、`conversation.member_added`、`conversation.member_left`
- 通知调度器扩展，支持 per-member block 筛选和批量插入
- 搜索扩展，支持群聊消息

**对现有系统的影响。**
- `internal/message/` 保持不变——新的群聊领域不会影响 1:1
- `internal/notification/dispatcher.go` 需要新的处理程序分支
- `internal/search/search.go` 需要新的 Repository 接口契约

---

### 方向三：文件后处理管线（P1）

**为什么。** 文件上传是入口，产生多个下游需求：病毒扫描、缩略图生成、恶意内容检测、全文内容提取。分析正确地将此视为现有 outbox 基础设施的自然延伸，但评估了其真正的架构影响。

**核心挑战。**
1. **资源控制**：大文件处理可能消耗大量内存和时间。每个 stage 都需要进程内并发控制（信号量）、基于临时文件（而非内存）的 blob 处理以及超时（per-stage context deadline）。文件太大或超时的 stage 不应阻止文件本身可用。
2. **stage 失败**：分析提出的 pipeline 设计（`Stage` 接口，独立失败）是正确的。但需要仔细考虑：一个失败的病毒扫描 stage 是否阻止文件访问？可能会——但需要可配置的策略（阻塞模式与仅记录模式）。
3. **outbox 负载大小**：当前 outbox 事件负载包含元数据而非完整 blob。处理管线需要引用 blob——意味着事件负载必须包含 `storage_key` 和 `owner_uid`。这是现有 `file.uploaded` 事件模式的一个微小扩展。

**架构变更。**
- 新增 `internal/postprocessing/` 包，包含 `Pipeline` 编排器 + `Stage` 接口
- Stage 实现：`VirusScanStage`、`ThumbnailStage`、`ContentExtractionStage`、`NSFWStage`
- 每个 stage 下载 blob → 处理 → 将结果写入（或跳过）
- 结果（缩略图 storage_key、提取的文本、检测标签）写回 `files` 表的新列或关联的 `file_processing_results` 表
- 全文内容索引列（`files.extracted_text`）——需要迁移

**对现有系统的影响。**
- 向 `files` 表添加新列（`extracted_text`、`processing_status`）需要迁移
- outbox 事件模式扩展，包含 `storage_key`
- `internal/app/app.go` 中的新装配线
- 没有现有 API 被破坏——处理管线在后台运行

---

### 方向四：滥用防护与信誉系统（P1）

**为什么。** 分析正确地将其分解为一个快速胜利（分层限制）和一个长期工程（信誉系统）。我将进一步将其分解为三层，每层有不同的架构含义。

**第一层：端点级限制（快速胜利，零架构变更）。** 当前 `ratelimit.Limiter` 对所有端点应用单一 `rps`/`burst`。最简单的可操作变更——在配置中添加每端点覆盖——完全在现有的 token bucket 层内。在 config 中添加上下文路由匹配（当前限流器没有路由感知，只有 key 感知）需要中间件感知。

实际上，当前限制器在 `middleware/ratelimit.go` 中作为通用 gin 中间件运行——它根据 uid（如果已认证）或 IP 对每个请求限流。无法通过路由进行区分。实现每端点限制需要：

- 路径 a：一个包装器，在限流中间件中检查路由（可使用 `c.FullPath()`），并为每个路由应用不同的 bucket。在单个 `Limiter` 实例中，通过向后端 BucketMap 添加路由感知来完成。
- 路径 b：每个端点一组单独的中间件注册（更清晰，但分散）。

**第二层：信誉系统（中等架构变更）。** 分析提出的三阶段方法：
- 第一阶段：举报计数器（`users.report_count`、`users.resolved_report_count`）——纯 SQL，无模型
- 第二阶段：静态信誉分（基于举报数/账户年龄/活动度的加权公式，每 24 小时批量计算）
- 第三阶段：动态信誉分（实时更新）

从架构角度看，第二阶段是最佳平衡点：它通过定时任务提供大多数好处，避免了实时信誉更新对性能的影响。`users` 表的挑战——aero-id 没有账户行。信誉数据必须存储在新表中（`user_reputation`），由 SSO uid 键控。如果信誉分影响到限制阈值，限制器后端需要查找该表——在请求路径中增加一个 DB 查询。

**第三层：管理 API（低架构影响）。** `domain/admin.go` 中的 `Role` 和 `UserRole` 模型已存在。`internal/handler/http/admin.go` 存在。已有权限常量（`PermContentModerate`、`PermAuditRead`、`PermUserBan`、`PermSystemConfig`）。缺失的是：管理端点用于管理角色、管理举报、查看系统范围的审计日志。这是一个纯粹的表面层工作——不改变核心领域。

**对现有系统的影响。**
- 第一层：对 `internal/middleware/ratelimit.go` 和 `internal/pkg/ratelimit/ratelimit.go` 的最小变更
- 第二层：新表（`user_reputation`），新的定时任务（批处理信誉计算），限流器集成
- 第三层：新的 API 端点覆盖现有领域层——对核心逻辑影响最小

---

### 方向五：文件版本控制（P2）

**为什么。** 文件版本控制从现有的一次性上传模型转变为协同编辑模型。分析提出了一个清晰的 `file_versions` 模型，我同意其评估——新建 `file_versions` 表（而非列化版本）是正确的。

**核心挑战。**
1. **共享与版本交互**：`file_shares` 表引用 `file_id`。分享链接应默认解析到最新版本，带有可选的 `?version=N` 查询参数。权限检查保持在 `file_id` 级别——版本只改变 blob 定位。
2. **版本保留**：不能无限保留版本。需要可配置的保留策略（保留最近的 N 个版本，保留 X 天），由定时任务执行。
3. **存储引用计数与版本**：旧版本引用旧 blob，这些 blob 必须从 `storage_refs` 引用计数中保持存活。GC 需要区分"由主文件行引用的 blob"和"仅由版本历史引用的 blob"。
4. **预签名上传与版本**：当前预签名上传路径（`PresignUpload`、`CompleteUpload`）假定是一个全新的文件。版本化将该路径扩展为："上传到现有文件 → 创建新版本 → 更新当前存储字段"。

**对现有系统的影响。**
- 新表：`file_versions`
- `internal/file/repository.go` 扩展，新增版本感知方法
- `internal/file/service_upload.go` 扩展，支持版本化的预签名上传
- `internal/storage/gc/sweeper.go` 需要版本感知引用检查
- `internal/handler/http/file.go` 扩展，支持 `?version=N` 查询参数
- `internal/handler/grpc/file.go` 扩展，支持版本化 RPC

---

## 3. 接口设计建议

### 3.1 用于跨实例共享状态的 Backend 接口模式

分析提出了用于限流的 `LimiterBackend` 接口。这一模式应扩展到 SSE hub：

```go
// internal/hub/backend.go
type Backend interface {
    Publish(ctx context.Context, uid string, evt SSEEvent) error
}
```

当前的内存实现成为默认后端。支持 PG LISTEN/NOTIFY 的实现成为可选的。这使得 hub 可以插入式替换——核心 Hub 结构保持不变，仅广播通道变化。

### 3.2 Outbox 事件类型常量

当前事件类型字符串（`"message.sent"`、`"message.read"`、`"comment.replied"`）作为字面量散布在 `internal/message/service.go`、`internal/notification/dispatcher.go` 和 outbox 事件构造代码中。提取到单个包（`internal/outbox/events.go`）并作为类型化常量：

```go
package outbox

type EventType string

const (
    EventMessageSent      EventType = "message.sent"
    EventMessageRead      EventType = "message.read"
    EventCommentReplied   EventType = "comment.replied"
    EventFileUploaded     EventType = "file.uploaded"
)
```

分发器中的 switch-case 变为编译器可检查的，而不是脆弱的字符串匹配。

### 3.3 处理管线 Stage 接口

分析中描述的 stage 模式应包含错误语义：

```go
type StageResult struct {
    // Processed indicates the stage completed (even if with a no-op).
    Processed bool
    // BlobKey, when non-empty, points to a new blob the pipeline produced
    // (e.g. a thumbnail) that should be recorded.
    BlobKey string
    // Labels are key-value metadata to merge into the file's processing results.
    Labels map[string]string
}

// StageResultSkip is returned when a stage decides the input is ineligible
// (e.g. a video thumbnail stage receives a .txt file).
var StageResultSkip = &StageResult{}

type Stage interface {
    Name() string
    Process(ctx context.Context, blob io.Reader, info domain.File) (*StageResult, error)
}
```

关键设计选择：stage 接收 `io.Reader` 而非 `storage_key`，因此管线编排器负责从存储中取 blob。这保持了存储接口的清晰性——stage 不直接依赖存储。

### 3.4 新方向限流的配置模型

我们建议使用分析中提出的嵌套配置结构，但扩展路径匹配：

```yaml
rate_limit:
  enabled: true
  default:
    rps: 10
    burst: 20
  per_route:
    "POST /v1/messages":      { rps: 5, burst: 10 }
    "POST /v1/comments":      { rps: 3, burst: 6 }
    "POST /v1/files/upload":  { rps: 2, burst: 4 }
  per_user_age:
    "< 1h":  { multiplier: 0.3 }
    "< 24h": { multiplier: 0.5 }
    "< 7d":  { multiplier: 0.7 }
```

路由匹配需要中间件知道 Gin 的 `c.FullPath()` + HTTP 方法。当前限制器不访问这些信息。最小变更是在中间件中将路由模式提取为键的一部分，而不是在限制器包内部。一个选项是传递路由模式作为每个中间件实例的参数，而不是尝试泛型路由匹配。

### 3.5 向后兼容性

- **新端点**：任何添加的方式——无论是群聊、文件版本还是管理 API——都使用新的 URL 前缀（`/v1/conversations`、`/v1/admin`），因此现有客户端不受影响。
- **文件版本控制**：`GET /v1/files/:id` 添加可选的 `?version=N`。默认行为（无版本）解析到最新版本，保持向后兼容。
- **限制器**：每路由限制是现有全局限制的扩展。如果 `per_route` 缺失，限制器会优雅地降级到默认值。
- **处理管线**：纯异步——在后台运行，不改变上传 API 的响应形状。添加 `processing_status` 列，但读取路径忽略它，直到客户端选择使用它。

## 4. 技术选型

### 4.1 新依赖评估

**Redis 用于分布式状态。** 分析正确地将其视为路径依赖。我的立场是：**应避免 Redis，在 PG 本地方案被证明不足之前不要引入它。** 理由：
- PG LISTEN/NOTIFY 可以处理 SSE 通知所需的吞吐量（~每秒几十次）
- `pg_advisory_lock` 零开销地处理 leader 选举
- 持久性：Redis 引入了一个新的故障模式（Redis 宕机 → 限流失效 → 端口被淹没），需要处理

降低 PG 本地方案优先级的门槛：
1. 如果 PG LISTEN/NOTIFY 延迟在负载下超过几毫秒
2. 如果每实例 leader 选举轮询频率成为负载生成器
3. 如果需要滑动窗口限流的全局共享状态

如果确实引入 Redis，应通过接口抽象（`LimiterBackend`、`hub.Backend`、`outbox.LeaderElector`）实现，以便测试可以在没有 Redis 的情况下模拟。

**不应引入消息队列。** Outbox 模式已经提供了异步事件投递。RabbitMQ/Kafka 会增加运维开销（配置、监控、持久性边界），而 outbox + 退避 + 死信已经覆盖了处理管线用例。唯一的例外是：如果处理管线需要几千字节以上的事件负载（例如嵌入完整 blob 元数据），消息队列可能更有意义——但那时也可以使用 PG 的 `LISTEN` + outbox 的 `payload` 列（已经是 JSONB，可扩展）。

**不应引入搜索引擎。** 当前搜索（`internal/search/`）跨消息、评论和文件执行 `LIKE`/`ILIKE` 查询。如果全文搜索需求激增，PostgreSQL tsvector + GIN 索引——已在 `files.extracted_text` 列中预留——可以在不引入外部搜索引擎的情况下处理大多数查询。在 tsvector 被证明不足之前，不要添加 Elasticsearch/Meilisearch。

### 4.2 构建与外部集成

管理 API（方向一，第三层）应使用现有的 Gin 路由分组和中间件，挂载在 `/v1/admin` 下。不需要新的框架——现有结构可以处理管理端点。管理员授权使用现有的 `middleware/permission.go`，该中间件检查 `HasPermission(ctx, uid, perm)`。

文件后处理 stage（方向三）可以在单独的 Go 进程中运行，也可以作为同一进程中的 goroutine 运行。在 outbox relay 内作为 goroutine 运行是最简单的起点，但如果 stage 变得计算密集（如视频转码），则单独的进程可以更好地隔离。

### 4.3 自建与采购

**自建：** 信誉系统（方向四，第二层）应自建。没有现成的 Go 库可以计算"基于举报数、账户年龄和活跃度的信誉分"——这是特定于领域的逻辑。举报计数器 + 加权公式 + 每日批次计算是一个小代码量。

**自建：** 文件版本控制（方向五）。Git LFS 是一个可能的现成方案，但它针对的是开发工作流程，而不是通用文件服务器。自建一个带有 `file_versions` 表的版本模型更轻量级，与现有 ACL 模型（文件分享、可见性）集成，并且允许自定义保留策略。

**采购：** 病毒扫描（方向三的 stage）。ClamAV（开源）是对文件执行扫描的标准方案。通过进程接口或 gRPC 服务集成它，而不是将其嵌入 aero-id 进程。同样，NSFW 检测最好通过外部服务完成（或者跳过——取决于部署环境）。

## 5. 实施路线图

### 优先级调整

分析的优先级分组基本合理，但我根据*架构依赖关系*（而不仅仅是产品价值）做了一些调整：

| 优先级 | 方向 | 架构依赖 | 产品影响 | 估计工作量 |
|---------|--------|-----------------|--------------|--------------|
| **P0** | 端点级限制（快速胜利） | 无 | 减少直接攻击面 | 3-5 天 |
| **P0** | 水平扩展（leader + 分布式 hub） | 无 | 生产就绪——多实例部署 | 2-3 周 |
| **P1** | 管理 API 表面 | 无 | 操作可见性 | 1-2 周 |
| **P1** | 文件后处理管线 | = P0 leader election | 安全、内容提取 | 3-4 周 |
| **P1** | 群聊 | = P0 leader election | 最高产品价值 | 4-6 周 |
| **P2** | 信誉系统（第二阶段+） | = 端点级限制 | 减少长期滥用 | 2-3 周 |
| **P2** | 文件版本控制 | = 存储 GC | 协作文件 | 3-4 周 |

### 阶段划分

**第一阶段：生产加固（P0，3-4 周）**
- 端点级限制——在每个路由级别添加配置驱动的限制，而不改变架构
- Outbox leader 选举——通过 `pg_advisory_lock` 添加，使单个实例成为 relay 领导者
- PG LISTEN/NOTIFY 用于跨实例 SSE 广播——使任何实例都能推送给任何其他实例的已连接客户端
- 用于分布式限流的 `LimiterBackend` 接口——使用内存实现进行测试，并做好 Redis 准备
- Outbox relay 优雅关闭（现有 ROADMAP P0-4：存储拉取的最新更改已在 relay.go 中没有优雅关闭？实际上 Stop 确实会进行一次最终 drain——`close(done)` 触发 `r.drain(context.Background())`）

**第二阶段：新领域（P1，6-10 周）**
- 群聊（`internal/conversation/`）：该领域中最复杂的工程工作。需要新的数据库迁移、服务层、阻塞语义和通知路由。4-6 周估算包括与现有通知调度程序的集成。
- 文件后处理管线（`internal/postprocessing/`）：pipeline 编排器 + stage 接口 + 病毒扫描/缩略图/提取 stage。3-4 周。
- 管理 API 表面（`/v1/admin/*`）：端点、权限检查、举报管理。1-2 周——大部分是表面工作，因为领域模型已存在。

**第三阶段：深度系统（P2，5-7 周）**
- 信誉系统（第一阶段已完成 ⇒ 第二阶段批处理计算 ⇒ 第三阶段实时更新）：2-3 周用于第二阶段（批处理定时任务）。第三阶段（实时）可能增加到 3-4 周并推后。
- 文件版本控制：3-4 周用于新的 `file_versions` 表、上传路径变更和 GC 集成。

### 风险与缓解

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|-----------|--------|-------------|
| 群聊领域过于复杂，难以在 6 周内完成 | 中 | 进度延迟 | 从最小可行产品开始：无 @mention，无 read-receipt，有基本 block 筛选。迭代添加。 |
| PG LISTEN/NOTIFY 在负载下延迟过高 | 低 | 需要切换技术栈 | 在 `hub.Backend` 接口后面进行抽象。在负载测试期间进行基准测试。如果不够则切换到 Redis。 |
| 文件处理 stage 导致 OOM | 低-中 | 生产事件 | 使用信号量限制并发、基于临时文件的处理、per-stage 超时。在 stage 之前对大文件进行采样。 |
| 信誉系统被规避 | 中 | 需要重新设计 | 审计轨迹：所有信誉影响都记录在 `audit_events` 中。定期重新校准公式。关注基于行为的信号（举报比率），而不仅仅是数量。 |
| outbox relay leader 崩溃，导致事件延迟 | 低 | 事件处理暂停 | 优雅关闭触发最终 drain。备用实例在 `pg_advisory_lock` 上等待，该锁在崩溃后自动释放。 |

### 发现的两个补充项

该分析提到了两个评估，我在此存档：

1. **幂等键未集成到 outbox 写入路径中。** 这是正确的——`internal/idempotency/store.go` 在 outbox 写入事务*之外*运行（它在中间件层面进行检查）。如果事件在写入 outbox 之后但在事务提交之前崩溃，重试会从幂等键检查器看到一个全新的事务——该检查器从未看到初始的、已回滚的事件。缓解措施：在 outbox 追加查询周围添加一个幂等性保护，或将此列为已知的窗口，在 outbox 写入之前应用幂等性检查（更简单但会失去原子性——幂等性检查无法与 outbox 写入在同一事务中原子地进行）。

2. **文件版本控制下的孤儿 blob GC。** 在版本控制下，GC 的引用计数必须考虑 `file_versions` 行。当前 GC 检查 `CountAnyByStorageKey` 或 `GetCount`（在 `RefCounter` 中）。如果启用了版本控制但 refs 包还不知道 `file_versions` 表，GC 可能会将一个旧的、仅版本引用的 blob 视为孤立 blob 并删除它。缓解措施：在 GC 引用检查中加入 `file_versions`，或使所有 blob 删除都经过引用计数（而非计数），这样 `file_versions` 行也会递增引用计数。当前 `RefCounter` 模式支持这一点——只需确保版本写入路径和删除路径也调用 `Increment`/`Decrement`。
