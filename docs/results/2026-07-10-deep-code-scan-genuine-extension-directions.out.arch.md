现在我掌握了足够的架构知识来撰写全面的分析。

# 架构分析：aero-id 服务

## 1. 架构评估

### 优势

1. **严格的单向依赖** — 从 handler → service → repository/storage/sso 的层次结构，强制职责分离。HTTP 和 gRPC 共享同一个 `service` 层，确保跨传输的**行为一致性**。这直接允许在单个 transport 上添加新功能，无需重复业务逻辑。

2. **事务性发件箱模式** — 领域事件在与它们产生的数据相同的数据库事务中写入 `outbox_events`，然后由 `Relay` 异步排空。这种设计消除了一种常见的微服务反模式：在请求路径中进行尽力而为的 POST。`NotificationDispatcher` 将其利用于通知投递——未来消费者（webhook、搜索引擎索引器、审计）可以插入而无需修改产生事件的代码。

3. **功能切换** — 偏好检查器、块检查器、速率限制器、通知折叠机制均以可选依赖项形式注入到 `NotificationDispatcher` 中。`With*` 函数选项模式使测试简单（零依赖项）和部署灵活（功能在 ops 配置时才上线）。

4. **幂等性** — KV 存储与 TTL 清理配合，使得 POST 操作幂等，这对于重试场景下的客户端体验非常重要。

5. **可观测性基础设施** — 指标注册表、RED 指标、追踪初始化以及每个组件的 MetricsSink 接口（SSO、发件箱中继、审计日志）。这种方法是“事先设计，而非事后补救”——在生产首次出现故障之前就已准备好。

6. **冷启动优化** — SSO 预热探测、连接池建立以及预填充缓存，确保当第一个请求到达时依赖项已准备就绪。

### 局限性

1. **审计日志为尽力而为（而非事务性）** — `AuditLogger.Log` 是一个在数据变更*后*写入 `audit_events` 的同步追加。该文档正确识别了这一点：**审计日志不可防崩溃**。如果服务在业务行提交后但在审计日志写入前崩溃，审计事件将**静默丢失**。这是比一次性通知更严重的问题——合规性/安全性通常要求审计必须在事务上与产生审计的动作一起提交。

2. **外键约束不匹配软删除** — 该文档的“软删除级联”发现非常尖锐。当前状态是：`message_reactions` 使用 `ON DELETE CASCADE`（对于软删除不一致），而 `comment_likes` 和 `messages.file_id` *阻止*删除（导致 500 错误），而 `file_shares` 和 `comments.target_id` **根本没有外键**（生成孤立的引用）。结果是一个混合系统，其中某些删除静默成功留下孤立数据，某些删除以运行时错误失败。这在没有测试的情况下对用户来说是无法预测的。

3. **SSO 缓存没有失效机制** — `UserCache` 有 TTL 驱逐，但一旦 TTL 过期就完全依赖陈旧服务。没有 `Invalidate(uids)` 方法，也没有 SSO 事件 webhook。这意味着当用户更改其个人资料（名称、头像）时，aero-id 可能缓存这些数据最多 TTL 时间段，从而导致过时的显示信息。如果 TTL 较长（例如 30 分钟），这会成为用户体验问题。

4. **发件箱中继使用拉取（而非推）** — `Relay.Start(pollInterval)` 定期轮询 `outbox_events`。5 秒轮询间隔引入了**至少 5 秒的通知投递延迟**。在用户期望即时消息的场景中（例如消息接收回执、点赞），这可能会明显降低用户体验。PostgreSQL 的 `LISTEN/NOTIFY` 或逻辑复制槽将提供亚秒级投递，代价是额外的运维复杂度。

5. **每个包一个 Go 文件** — `internal/` 下有 22 个目录，有些只有一个文件。虽然包结构是合理的，但某些包（`repository/`）正在大幅增长（`db.go` 为 ~10K，`comment.go` 为 ~15K），并朝着 500 行的 AGENTS.md 限制发展。监控职责拆分的紧张关系：`repository/db.go` 包含 *六个* 不同的模型（fileRow、messageRow、commentRow、auditEventRow、notificationRow、commentLikeRow）以及它们之间的相互转换构造函数。一个文件中的六个领域实体违反了“每个文件一个职责”的原则。

### 架构债务

| 债务 | 严重程度 | 影响 |
|--------|----------|--------|
| 已废弃的内置包（`internal/comment/`、`internal/file/`、`internal/message/`、`internal/block/`、`internal/audit/`、`internal/search/`、`internal/notification/`）占用了 ~10 年的租约 | 高 | 混乱、重复的导入、误导开发人员 |
| `repository/db.go` 包含所有领域实体的 GORM 映射 | 中 | 撞到 500 行限制；单个文件的变更冲突 |
| `message_reactions` 使用 `ON DELETE CASCADE`，但其他软删除引用表没有 | 高 | 未来数据损坏（错过级联边缘情况） |
| 不存在 `comment_reports → comments` 的 `ON DELETE SET NULL` | 中 | 报告可能变为孤立或阻止删除 |
| 审计日志查询排序为 `(created_at DESC, id DESC)`，但索引为 `(actor_uid, created_at DESC)` — 丢失 `id` | 低 | 高负载下额外排序，生产中发现 |

---

## 2. 扩展方向

### 方向 1：事务性审计——从发件箱到“审计作为事件”

**为什么需要：** 当前的 `AuditLogger.Log` 尽力而为方法在合规性上是不可接受的。任何需要 SOC 2、HIPAA 或 FINRA 审计追踪的生产部署**必须**确保审计事件不会静默丢失。即使没有强制性合规性要求，将审计从同步追加迁移到发件箱也解决了“数据变更提交但审计日志缺失”的崩溃恢复问题。

**核心挑战：**
- 审计事件必须在*与产生它们的数据变更相同的数据库事务中*发生。这意味着将审计事件写入 `outbox_events`，而不是直接写入 `audit_events`。
- 需要一个新的发件箱处理程序（`AuditHandler`）来排空 `audit.*` 事件并将它们物化到 `audit_events` 表中。
- 审计事件的延迟将由发件箱轮询间隔决定（当前默认约 5 秒）。合规性背景下的取证可能要求更紧的延迟。
- 由于 `outbox_events` 的行级锁定和事务日志膨胀，高吞吐量的审计事件（例如每个文件下载、每个消息的查看）可能会压垮数据库。

**预期的架构变更：**
- 添加 `AuditEvent` 作为发件箱事件类型（`"audit.event"`），与 `"message.sent"` 和 `"comment.replied"` 并列。
- 实现 `outbox.Handler` 来排空审计事件并将它们写入 `audit_events`。
- 在 `app.go` 中的发件箱链中添加 `AuditHandler`（在 `NotificationDispatcher` 之前或之后）。
- 为高吞吐事件（例如 `"file.downloaded"`、`"message.viewed"`）添加限流/采样：仅记录每用户每分钟的第一个事件，或使用可配置的采样率。
- 考虑审计事件的分区 `audit_events`：按 `(action, created_at)` 或其他时间范围分区，因为审计表增长最快。

**对现有系统的影响：**
- 服务中的现在式 `audit.Log(ctx, event)` 调用需要更改为 `outboxRepo.Append(ctx, tx, event)` 调用——但它们的签名已经是事务感知的（它们接收 `ctx` 和一个隐式的 `*gorm.DB`）。重新布线相对较小。
- `internal/service/mocks` 中的模拟需要从 `AuditLogger` 切换到发件箱 `Repository` 模式。
- 部署需要零停机迁移：并行运行旧版（同步）和新版（发件箱）审计日志一段时间，验证它们产生相同的结果。
- 审计消耗了 *当前发件箱* 关注点中最大的上下文切换。现在就在做。不要推后。

### 方向 2：SSO 缓存失效——一种混合方法

**为什么需要：** 用户缓存不失效意味着用户个人资料更改在 aero-id 中最多可能需要 `UserCacheTTL` 的时间才能反映出来。对于即时通讯应用，过时的头像和显示名称会降低用户体验质量。另外，如果 SSO 停用了用户，aero-id 将缓存该用户的存在状态，直到 TTL 过期，从而在停用后仍可访问文件/消息。

**核心挑战：**
- SSO 是外部服务的生态系统。aero-id 不能强制要求 SSO 提供 webhook——它必须适应 SSO 的现有能力。
- 轮询（每 N 分钟批量检查最近修改的 UID）是最简单的选项，但会为每个轮询周期增加可预测的负载。该负载与活跃用户数相关，而不是与变更率相关。
- Webhook（SSO 调用 aero-id 的端点）是响应式的，但要求 SSO 团队实现调用，将 aero-id 与 SSO 的内部状态变化耦合。
- gRPC 流（SSO 推送失效事件）提供最低的延迟和最高的效率，但增加了 gRPC 流的运维复杂度。

**预期的架构变更：**

我推荐一个三级方法：

1.  **立即** — 添加 `UserCache.Invalidate(uids ...string)` 和 `InvalidateAll()`。它们有 `O(1)` 复杂度，可以立即在生产中使用。对 `/v1` 管理员端点进行最小实现（通过 SSO 身份验证的角色检查）。这一步骤对 SSO 团队没有外部依赖。

2.  **短期** — 添加一个可配置的*对等缓存失效端点*：一个轻量级的 HTTP 端点，由 `AERO_ID_SSO_PEER_TOKENS`（共享密钥列表）保护，其他服务的 SSO 组件可以调用它来通知 aero-id 其用户已被修改。这使得 SSO 团队可以在不实现 webhook 的情况下触发失效——他们只需从一个管理脚本调用端点。

3.  **长期** — 当 SSO 有能力时，添加可选的 gRPC 流，或在 SSO 中实现 webhook 支持。

**对现有系统的影响：**
- `UserCache` 接口扩展了 `Invalidate` / `InvalidateAll`，但这对现有调用者是向后兼容的。
- 管理员端点需要一个新的路由和 `middleware.AdminOnly` 守卫。
- 可选的 gRPC 流端点需要一个新的 protobuf 服务和 `go-grpc` 依赖项——但这将是 SSO 的依赖项，而不是 aero-id 的。

### 方向 3：领域事件流水线——发件箱作为集成主干

**为什么需要：** 当前，发件箱只有两个消费者：`NotificationDispatcher` 和可选的 `WebhookHandler`。在许多方面，发件箱是该系统中*最有价值的基础设施*——它提供了“记录一次，扇出多次”的事务保证。将发件箱提升为一级集成机制，可以解锁：

- **搜索引擎索引**：搜索服务可以消耗 `file.created`、`message.sent`、`comment.created` 事件来建立索引，而无需轮询。
- **分析管道**：摄取 `file.downloaded`、`comment.liked`、`message.read_receipt` 事件进入外部系统（例如 BigQuery、Snowflake）。
- **跨服务协作**：其他微服务可以消耗 aero-id 的事件（通过 webhook 或消息队列桥接）来触发下游操作，而无需轮询 API。

**核心挑战：**
- 性能：在发件箱轮询间隔（~5 秒）下，某些使用场景（例如实时分析）的延迟可能太高。考虑使用 PostgreSQL 的 `LISTEN/NOTIFY` 进行亚秒级通知，但仍保留轮询作为后备。
- 重放：新消费者（例如搜索引擎）需要重放历史事件进行初始索引。发件箱需要支持*重放*：通过 `EventType` 和时间范围查询已发布的事件。
- 背压：“扇出到许多消费者”需要在消费者落后时防止发件箱增长无限大。dead-letter 处理程序和日志监控是必需的最低要求。
- 事件向后兼容性：一旦事件被生产，变更其 JSON 负载可能会破坏消费者。计划为每个事件类型引入显式的模式版本。

**预期的架构变更：**
- 为每个新消费者添加 `outbox.ReplayableHandler`——一个知道如何消耗历史事件以及实时事件的消费者。
- 添加一个 `Relay.Replay(ctx, eventType string, from, to time.Time, handler Handler)` 方法来遍历已发布的事件。
- 添加模式版本控制：每个负载都以 `{"version": 1, "data": {...}}` 开头。
- 考虑为此实现一个专用的 `events` 表（`outbox_event_publications`），记录事件何时被消费者处理。

**对现有系统的影响：**
- 现有的事件消费者（`NotificationDispatcher`、`WebhookHandler`）不需要更改——它们不关心版本控制，除非我们破坏了向后兼容性。
- `app.go` 中的发件箱链可能需要拆分为*扇出多路复用器*：将一个事件分派给多个处理程序的组件，但每个处理程序独立处理其错误（一个处理程序失败不会阻止其他处理程序）。
- 需要监控：每个消费者的处理延迟，消费者错误率，发件箱积压大小。

### 方向 4：查询路径的读取模型（CQRS 轻量级）

**为什么需要：** `MessageList`、`CommentList` 和 `NotificationList` 查询当前扫描主表并应用排序和分页。随着数据的增长（数百万行），模式 `WHERE id < cursor ORDER BY id DESC LIMIT n+1` 仍然效率很高，但是：
- 加入 `sso.GetUsers(uids)` 来解析用户显示信息，为每个列表操作增加了一次网络往返。这随着列表请求的扩展而扩展，导致了 N+1 问题（尽管缓存在集群范围内）。
- 过滤未读消息（`unread_only=true`）需要计算未读计数，当前是物化的（好！），但查询 `messages` 表然后根据排序过滤在架构上不干净。
- 通知折叠被推送到写入路径中的 `NotificationDispatcher`——但读取折叠的通知需要读取多个行并聚合它们。

**核心挑战：**
- aero-id 没有特定于查询表。所有读取都直接抽取主 GORM 模型。
- 在消息和通知表的预期大小下（每个用户数千条消息，总共数亿条），带排序的 `WHERE id < cursor` 扫描仍然是最优的——但前提是索引正确。
- 真正的 CQRS 需要单独的读取通道、异步更新逻辑以及最终一致性——对于 aero-id 当前的范围来说，这是一个重大的架构跳跃。

**预期的架构变更：**

我推荐一个**轻量级读取模型方法**，而不是完整的 CQRS：

1.  **物化用户显示信息**到本地表：一个 `user_profiles` 表，通过发件箱事件从 SSO 异步填充，或通过 TTL 缓存刷新填充。然后列表查询可以 INNER JOIN 到 `user_profiles`，避免 RPC 往返。

2.  **预先计算的会话视图**：在 `message_view` 中物化最新消息（每个会话），由发件箱事件 `message.sent` 和 `message.read` 更新。这会转换：
    ```sql
    -- 从：JOIN messages, GROUP BY conversation, ORDER BY MAX(created_at)
    -- 到：SELECT * FROM message_view WHERE participant_uid = ?
    ```

3.  **每个收件人的通知信使**：以与用户相关的高频率，可以考虑每个收件人分区通知表（对于 Postgres 来说是 `PARTITION BY LIST(recipient_uid)`，尽管在应用层面管理分区更常见）。

**对现有系统的影响：**
- 较低：`user_profiles` 表只是一个新的发件箱消费者和一个新的小迁移。
- 中等：会话视图需要新的发件箱事件类型（`"conversation.updated"`）和一个新的发件箱处理程序。
- 低：`service.go` 中的 `UserDirectory` 接口保持不变，直到我们切换到本地读取。

### 方向 5：多区域部署就绪

**为什么需要：** 当前架构假定单个数据库实例和单个 SSO 端点。对于企业部署、全球用户群或高可用性设置，无状态应用层在位于下游的数据库和 SSO 方面受到限制。多区域意味着：

- **区域感知存储**：S3 存储后端应选择地理上最近的区域。
- **跨区域 SSO 请求**：认证流量需要跨区域路由到 SSO。
- **发件箱最后写入冲突**：如果两个区域的数据库实例不同步（最终一致性），发件箱消耗可能会产生双重通知或冲突的写入。

**核心挑战：**
- Postgres 作为有状态单点存在。真正的多区域设置需要 Postgres 级别的复制（流式复制、Citus、CockroachDB）。
- SSO 认证需要跨区域协调——如果 SSO 不是多区域的，所有流量都必须路由到 SSO 的主区域，增加了延迟。
- `uuid-v7` 主键是单调递增的，这在全局范围内工作良好，但需要分布式序列。幸运的是，UUIDv7 是时间排序的，这足够好，但跨区域的分区工作（区域 A 的 ID 可能与区域 B 的 ID 排序不同）取决于时钟同步。
- 发件箱的幂等性无法跨区域保证，除非使用全局事务 ID 或区域前缀。

**预期的架构变更：**
- 为应用实例添加区域感知路由：基于地理位置或 DNS 解析的尾延迟感知路由。
- 添加基于区域的发件箱分区（每个区域有独立的 `outbox_events` 表/模式）或使用全局唯一的发件箱消费者 ID（例如 `region-a`、`region-b`）来防止双重消耗。
- 添加区域感知的跨区域批量 SSO 用户解析。

**对现有系统的影响：**
- 非常高的近期影响——当前设置不是为多区域设计的。锁存器、连接和发件箱中继都假定单一区域。这必须是长期的演进路线图。

---

## 3. 接口设计建议

### 现有接口的评估

当前的服务接口定义良好，是围绕特定于领域的 DTO 进行构建的。它们是：

- **足够小**：`FileService` 有 10 种方法，但每个方法都有清晰的单一职责。
- **向后兼容**：新字段是添加到 DTO 的，而不是从方法签名中添加的（良好实践）。
- **上下文感知**：所有方法都接收 `context.Context` 作为第一个参数，使得追踪和超时无缝工作。

### 需要改进的地方

#### 3.1 接口中的错误类型
服务接口返回 `(*domain.File, error)`，但具体的错误类型未在接口中指定。调用者必须进行类型断言为 `*errors.AppError` 才能访问 HTTP/gRPC 代码。这创建了调用者对 `errors` 包的隐式依赖。

**选项 A：保持现状** — 简单，且不影响 Go 错误约定。当接口定义 + godoc 记录预期错误类型时，类型断言是惯用的。

**选项 B：定义每个接口支持的错误代码** — 在 godoc 中添加：
```go
// 可能的错误：ErrNotFound、ErrForbidden、ErrUnauthorized、ErrConflict。
// 调用者可以使用 errors.As 或 errors.Is 来检查。
type FileService interface { ... }
```
这会向接口消费者传达契约，而不会强迫类型检查。

**选项 C：定义特定于域的错误变量** — 导出 `var ErrFileNotFound = apperr.NotFound(...)` 等，但这会受到从接口到 `apperr` 的跨包导入影响。不推荐。

**建议：** 选项 B。它轻量级，完全是文档，并且不强制导入。当错误条件很重要时，调用者可以 `errors.Is(err, service.ErrNotFound)`。

#### 3.2 现有的 DTO 布局
当前，所有 DTO 都定义在 `internal/service/service.go` 中。随着服务的增长，这变得越来越嵌套。
计划拆分为 `internal/service/types.go` 或每个服务的 DTO 文件（已经存在 `file.go`、`message.go` 等）。映射自然：`file_presign.go` → `PresignUploadInput` 和 `PresignUploadResult`。

#### 3.3 审计接口的抽象层级
当前：
```go
type AuditLogger interface {
    Log(ctx context.Context, e *domain.AuditEvent) error
}
```
这是*同步*的。对于基于发件箱的重构，两种合理的替代方案：

**选项 A：保留同步 AuditLogger，但通过发件箱实现（添加发件箱 AuditLogger 适配器）**
```go
type outboxAuditLogger struct {
    repo outbox.Repository
    db   *gorm.DB
}
func (a *outboxAuditLogger) Log(ctx context.Context, e *domain.AuditEvent) error {
    tx := a.db.WithContext(ctx)  // 从调用者获取当前事务
    return a.repo.Append(ctx, tx, outbox.NewEvent("audit", e.ID.String(), "audit.event", e))
}
```
通过直接在 `AuditLogger` 接口下交换实现，服务代码不需要改变。

**选项 B：将 `AuditEvent` 添加为发件箱事件类型，并逐步淘汰 AuditLogger 接口**
这是更彻底的，需要逐个服务进行审计跟踪：更改 commentService、messageService 等以直接调用发件箱 repo 而不是通过审计记录器。

**建议：** 选项 A 作为迁移路径。选项 B 作为长期目标。消除 `AuditLogger` 接口的冗余是技术债清理。

#### 3.4 存储抽象
当前的 `FileStorage` 接口：
```go
type FileStorage interface {
    Upload(ctx, key, mimeType string, reader io.Reader, size int64) error
    Download(ctx, key string) (io.ReadCloser, error)
    Stat(ctx, key string) (*ObjectInfo, error)
    Delete(ctx, key string) error
    PresignUploadURL(ctx, key, mimeType string, ttl time.Duration) (string, error)
    PresignDownloadURL(ctx, key string, ttl time.Duration) (string, error)
}
```
这是**良好的**。S3 和本地实现都已存在。如果添加了 GCS 或 Azure Blob，它们只需实现该接口即可。

一个缺失的抽象是：
```go
// DownloadURLSigner 创建文件的临时签名下载 URL。
// 它被 service 层和 http 下载处理程序使用。
type DownloadURLSigner interface {
    SignDownloadURL(ctx, fileID domain.ID, ttl time.Duration) (string, error)
}
```
当前，`local` 存储实现了 `SignedDownloader`（在 HTTP 包中定义），这跳过了服务层。这隐藏了授权——下载网址是在处理程序级别签名，而不是通过服务。需要重构以将 `DownloadURL` 的授权保留在服务层。

---

## 4. 技术选型

### 当前技术栈

| 技术 | 目的 | 评估 |
|------|---------|------------|
| Go 1.26 | 语言 | 现代，适合用途 |
| Gin | HTTP 路由 | 成熟且广泛使用。对于 aero-id 的当前规模来说性能足够。 |
| gRPC-go | gRPC 服务器 | 标准。第二个传输层增加了运维复杂度，但为 SSO 集成提供了第一方协议。 |
| GORM v2 | ORM | **争议性选择**。对于 aero-id 的作用域（低读/写，无连接池放大）来说，其开销可以接受，但自动迁移、N+1 查询和隐藏加载是常见的陷阱。当前代码谨慎使用它（无 `Preload`，无 `AutoMigrate`），因此风险得到控制。 |
| PostgreSQL 16 | 数据库 | 优秀。跨所有表的 UUIDv7 键确保了写入可扩展性。 |
| MinIO / S3 | 对象存储 | 对于文件存储来说合理。 |
| Testcontainers | 集成测试 | 对 CI 很重要。 |
| go.uber.org/zap | 日志 | 标准。 |
| OpenTelemetry | 追踪 | 良好的投资。 |

### 是否需要引入新的技术栈

1. **无消息队列** — 发件箱模式 + Postgres 轮询目前已经足够。添加 Kafka/RabbitMQ/Redis Stream 会增加运维复杂度（集群管理、监控、网络）以换取延迟改善。如果延迟要求低于 1 秒，请重新考虑。**决定：继续使用 Postgres 发件箱。**

2. **无单独的 Go web 框架** — Gin 已经提供。迁移到 chi、echo 或 fiber 将提供增量收益（更快的路由、更好的标准库适配器），但将失去 Gin 的成熟中间件生态系统（CORS、速率限制、恢复）。**决定：继续使用 Gin，但监视 Gin 的社区活力。**

3. **无缓存层** — 发件箱是去重的（幂等性键处理客户端重试），但未提供读取缓存。添加 Redis/Memcached 对于 `sso.GetUsers` 的 UID→用户映射缓存可能是有益的，但当前的应用内 `UserCache` 是进程本地的，因此是无状态的。对于部署在多个副本的微服务来说，共享的 Redis 缓存是有价值的，但增加了运维复杂度和延迟尾延迟分布。**决定：在需要多个副本之前推迟（当单机缓存不命中率 < 5% 时，Redis 没有 ROI）。**

4. **schema 变更的迁移工具** — `golang-migrate/migrate` 在实践中有问题（回滚语义、暂停）。**替代方案：** `golang-migrate` 已经存在。它是成熟的。GORM 的 `AutoMigrate` 不是生产路径。**决定：继续使用 `golang-migrate`。**

### 第三方依赖评估标准

对于 aero-id，评估新依赖项的标准：

1.  **许可证兼容性**：必须为 Apache 2.0、MIT、BSD (2/3) 或 ISC。LGPL v3 可以接受（未修改）。晚于当前的 AGPL/MongoDB/BSL 是不行的。

2.  **维护者活跃度**：Github 在过去 12 个月内的提交。对于安全关键路径（HTTP、gRPC、TLS）至少要有 2 位活跃维护者。

3.  **直接依赖深度**：如果引入一个新的 direct 依赖项会拉取超过 10 个 transitive 依赖项，它需要具有强大的价值主张。

4.  **Go 版本兼容性**：必须与 Go 1.26 兼容（当前版本），且在重要的情况下有 Go 1.28 的计划。

5.  **不透明度风险**：团队是否深度理解该包？GORM 已经是“部分理解”。添加另一个 ORM（例如 ent）会增加风险。

### 自建 vs 采购/集成

| 决策 | 风险 | 推荐 |
|--------|------|------------|
| SSO 客户端集成 | 低 | 集成（已存在）。没有理由自建 SSO。 |
| 文件存储（S3/MinIO） | 低 | 集成。对象存储是商品。 |
| 审计日志存储 | 低 | 自建（Postgres 表）。简单、可审计、非商品。 |
| 通知引擎 | 中 | 自建。发件箱 + 通知调度器已经写入。使用外部推送通知（例如 Firebase）将需要一个新的桥梁。 |
| 速率限制 | 低 | 进程内自建。跨 Pod 协调需要分布式状态（如果严格准确是强制约束，则使用 Redis；否则可以放宽）。 |
| 事务发件箱 | 中 | 自建（已实现）。在 Postgres 上实现“一次写入，扇出”，具有重试和死信。成熟的解决方案已经存在（例如 `watermill`、`eventuous`），但基于 Postgres 的成熟方案不普遍；集成成本可能超过收益。当前实现是健壮的。 |

---

## 5. 实施路线图

### 优先级分类

| 优先级 | 定义 | 时间范围 |
|----------|------|-----------|
| **P0** | 阻止用户操作的安全漏洞或数据损坏 | 立即 (1-2 周) |
| **P1** | 降低体验或增加操作风险的重大功能缺失 | 短期 (2-6 周) |
| **P2** | 在没有它的情况下可以生存的增强功能，但值得规划 | 中期 (6-12 周) |

### 阶段 1：立即（P0 — 第 1-2 周）

1.  **删除 7 个死数据包** — 删除 `internal/comment/`、`internal/file/`、`internal/message/`、`internal/block/`、`internal/audit/`、`internal/search/`、`internal/notification/`。这些包是在重构为 `internal/service/` + `internal/repository/` 布局之前的*原始服务实现*。它们没有在生产中使用，没有导入，但使 `go list ./...` 输出膨胀，并导致使用类似包命名的新开发人员困惑。删除它们会除去 ~4000 行死代码。

    *风险缓解*：删除脚本（`rm -rf ...`）后运行 `make test && make lint`。如果任何测试引用这些包，它们将立即失败，揭示隐式依赖关系。修复这些关系。

2.  **处理 FK 约束冲突** — 这是最危险的问题：`comment_likes` 和 `messages.file_id` 上的 FK 约束会在删除操作时产生 500 错误。两个立即行动：
    - **迁移**：将 `comment_likes(comment_id)` 上的 FK 约束从 `NO ACTION` 更改为 `ON DELETE SET NULL (comment_id)` *或* 删除 FK（因为 aero-id 在逻辑上处理软删除，所以硬级联是不适当的）。
    - **测试**：添加集成测试，该测试删除一个带有 likes 的注释，并验证没有错误且没有孤立行。
    - `messages(file_id)` 上的 FK 问题更复杂——如果删除一个文件时还有消息引用它，它们应该被删除，将 `file_id` 设置为 `NULL`，还是保留为孤立？aero-id 的“软删除”语义（`DeletedAt` 非空）意味着文件在物理上并未被移除，因此对于引用旧文件的消息来说，允许软删除将 `file_id` 保留为有效是合理的。将 FK 改为 `ON DELETE SET NULL (file_id)`。
    - *文档并添加、修复或删除* `message_reactions` 上的 `ON DELETE CASCADE`（当前它级联了——当软删除时这是错误的）。

3.  **审计日志作为一种发件箱事件** — 关键的合规性改进。删除同步的 `AuditLogger.Log` 并将其替换为在数据变更的同一事务中写入 `outbox_events`。然后 `AuditHandler` 排空审计事件并将它们物化到 `audit_events`。

    *迁移策略*：分两步。首先并行运行同步日志和发件箱版本。然后逐步淘汰同步版本。

4.  **添加 comment.liked、message.reacted、file.shared 事件**到发件箱。当前，发件箱处理程序 `notificationFor` 识别 `message.sent`、`message.read` 和 `comment.replied`，但不识别列出的其他三个。这些是导致错过通知的生产用户问题。

### 阶段 2：短期（P1 — 第 3-6 周）

5.  **SSO 用户缓存失效机制** — 实现 `UserCache.Invalidate(uids)` 和 `InvalidateAll()`。添加一个由共享令牌保护的管理员 HTTP 端点 `/admin/sso-invalidate`。

    *设计决策*：是否使端点特定于 uid 还是全部？路径：
    - `POST /admin/sso-invalidate`（正文：`{"uids": ["uid1", "uid2"]}`）— 特定于 UID，适用于定向修复。
    - `POST /admin/sso-invalidate-all` — 暴力清除整个缓存。对部署很实用。

    两者都实现。

6.  **发件箱重放能力** — 添加 `Relay.Replay(eventType string, from, to time.Time)`，允许新消费者建立初始状态。
    *为什么现在*：如果阶段 1 添加了新的审计事件类型，审计消费者需要重放历史事件，以便在迁移期间不会丢失审计事件。

7.  **拆分 `repository/db.go`** — 将每个领域实体（`fileRow`、`messageRow`、`commentRow`、`auditEventRow`、`notificationRow`、`commentLikeRow`、`fileShareRow`、`commentReportRow`、`messageReactionRow`）拆分为它自己的文件：`repository/file_row.go`、`repository/message_row.go` 等。这使 GORM 映射靠近它们专用的仓库文件（`repository/file.go` 等），减少文件大小，并减少编辑冲突。

    *注意*：由于 Go 的 `_test.go` 文件与实现并置，重构必须保持测试覆盖。

### 阶段 3：中期（P2 — 第 7-12 周）

8.  **用正确的软级联替换硬删除级联** — 将 `message_reactions` 从 `ON DELETE CASCADE` 迁移到 `ON DELETE SET NULL`，或删除 FK 约束并依赖应用逻辑。将 `comment_likes`、`comment_reports` 和 `messages.file_id` 迁移到一致的软级联策略（例如 `ON DELETE SET NULL` + 应用端的状态 `status=hidden` 字段）。

9.  **用户资料的本地读取模型** — 向 `user_profiles` 表添加发件箱消费者。然后列表查询可以 JOIN `user_profiles` 而不是进行 RPC 往返。这将减少每个列表请求的延迟。

    *权衡*：用户资料信息变得最终一致（发件箱延迟，~5 秒），而不是强一致（RPC 到 SSO）。对于以毫秒为单位获取显示名称，这是一个可以接受的折衷方案。

10. **添加审计索引迁移** — `idx_audit_events_action` 在 `(action, created_at DESC)` 上，以及 `idx_audit_events_action_actor` 在 `(actor_uid, action, created_at DESC, id DESC)` 上。这将修复 List 查询中缺失的覆盖索引。

11. **添加发件箱积压警报的监控** — 如果 `outbox_events` 中未发布事件的数量超过阈值（例如 1000），则触发报警。当前日志是仅有的观察点；在用户影响之前主动警报将减少 MTTR。

### 风险与缓解措施

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|----------|--------|-------------|
| 删除死代码会破坏某些依赖它们的东西 | 低 | 中 | 在删除后对所有包运行 `make test && make lint`。 |
| 发件箱审计使发件箱成为合规性瓶颈 | 低 | 高 | 通过 Prometheus 监控发件箱积压。如果持续超过 1000，优先处理发件箱性能。 |
| FK 约束迁移需要停机 | 中 | 低 | 使用 `SET CONSTRAINTS DEFERRED`（当使用 DEFERRABLE 时）或 `ALTER TABLE ... VALIDATE CONSTRAINT` 进行零停机。添加之前验证约束。 |
| 通知折叠的 SSO 缓存延迟 | 中 | 低 | 5 秒发件箱延迟几乎感知不到。缓存失效 webhook 可以消除它。 |
| 拆分 `repository/db.go` 导致 git 冲突 | 低 | 低 | 拆分提交必须重命名，而非复制。先提交拆分，然后提交内容变更。 |

### 里程碑摘要

| 里程碑 | 组件 | 依赖项 | 完成度量 |
|----------|---------|--------------|----------------|
| M1：清理 | 死代码、FK 约束、删除了冗余的 CASCADE | 无 | `make test && make lint` 通过；删除带有引用的行时无 500 错误 |
| M2：审计可靠性 | 发件箱审计日志、指标 | M1 | 审计事件在重启后仍然存在；没有异步写入丢失；每个审计操作的 Prometheus 计数器递增 |
| M3：通知覆盖 | comment.liked、message.reacted、file.shared 事件 | M1 | 所有三种事件类型都出现在 notificationFor switch 语句中；集成测试验证它们 |
| M4：SSO 缓存失效 | UserCache.Invalidate、管理员端点 | 无 | 单元测试通过；失效后 `GetUsers` 返回更新后的数据 |
| M5：性能基线 | 读取模型、审计索引 | M2、M3、M4 | 列表查询次 P99 < 50ms（包括 SSO 用户解析）；审计列表查询不使用外部排序通过 |
| M6：软级联一致性 | 所有 FK 约束匹配行为 | M1 | 删除文件/评论/消息后，子行被隐藏；`message_reactions` 在软删除时无级联 |

### 并行工作流

截至第 3 周，三个团队可以并行工作：

```
团队 A：M1（死代码）+ M3（事件覆盖）+ M4（SSO 缓存）
团队 B：M2（发件箱审计）+ 审计索引
团队 C：FK 约束修复 + 软级联文档
```

并发取决于代码审查能力。根据 AGENTS.md 约束（文件 ≤500 行，函数 ≤50 行），团队 A 的工作是最简单的（添加新事件类型；添加发件箱审计入口点）。团队 B 的工作是最需要密切关注的（改变的审计预期行为）。团队 C 的工作是手动的，但具有低码量、高影响。
