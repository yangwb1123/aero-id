现在我已掌握核心架构，可以撰写全面的分析报告了。

---

# aero-id 架构分析

## 1. 架构评估

### 1.1 优势

**严格的分层隔离。** handler → service → repository/storage/sso 这一单向依赖是该项目最强大的设计决策。handler 层仅做 I/O 编解码，service 层持有全部业务逻辑和授权逻辑，repository 层返回原始持久化错误。这也正是 AGENTS.md 中"HTTP 和 gRPC 共享同一个 service 层且行为必须一致"这一要求的坚实基础——跨两个传输协议的 1:1 语义验证是可证明的。

**成熟的事务性 outbox 模式。** outbox 基础设施（`internal/outbox/`）已经是一个经过了充分实战检验的、支持重试/死信/Webhook 链式处理的组件。事件是与领域行在同一事务中写入的，Relay 异步轮询并分发给下游 Handler。Comment 和 Message 服务已广泛使用此机制；Dispatcher 通过偏好检查、阻止检查、速率限制和折叠将 `NotificationDispatcher` 作为 Handler 注入——这是一个架构亮点。

**特性完备的 SSO 集成设计。** 双向缓存（带有过期/过期后容错能力的 TTL）、熔断器、带指数退避的重试、mTLS/证书锁定、keepalive 调优——这些细节表明 SSO 集成是在考虑生产弹性的情况下构建的。

**可选功能选项模式。** 每个 Service 的构造器都接受函数式选项（`WithBlockChecker`、`WithDispatcherPusher` 等），默认情况下这些功能是关闭的。这使得可以从简单配置起步逐步叠加，而无须一次性完成所有抽象，且完全避免了对零值接口进行 nil 检查。

**生命周期管理。** `App` 结构体以有向无环图（DAG）方式管理 goroutine：中断时先通过 gRPC 健康检查发出排空信号，再依次停止 outbox relay、GC、速率限制器、idempotency 清理、HTTPServer，最后是 gRPC。

### 1.2 局限性与架构债务

**每个实例的内存状态。** Hub（SSE 注册表）、速率限制器、TokenCache、UserCache——全部在内存中，没有跨实例协调。这意味着：
- 水平扩缩会导致 SSE 连接断开（连接的实例不持有订阅）。
- 速率限制在实例之间不一致：如果客户端轮换实例，可能会超出 RPS 预算。
- 缓存击穿：每个冷实例都会对 SSO 产生完整的令牌验证负载。

代码本身对此很坦诚（`ratelimit.go` 中有注释说明"分布式需要用 Redis，不在范围内"），但这确实限制了生产环境可扩展性。

**文件服务是"盲"转发。** `FileService.Create()` 将数据流式传输到后端，计算 SHA-256 哈希值，执行基于所有者的去重，然后持久化行记录。它不产生 outbox 事件，不触发后处理（缩略图、病毒扫描、转码），且 `domain.File` 没有表示处理状态的字段。考虑到文件是 aero-id 最重的数据类型，这是一个关键的架构差距。

**通知类型是到处散落的字符串常量。** `notificationFor()` 方法内有 `"message.received"`、`"message.read_receipt"`、`"comment.reply"`。没有集中的 `NotifType` 枚举，没有 `iota` 常量组，因此存在打字错误风险，且 IDE 无法进行重命名重构。这将每次添加新事件类型时与文档附录之间的交叉引用变成了人工操作步骤。

**CommentStatusHidden 含义模糊。** 值 3 既表示作者自行隐藏（通过 Hide() 端点），也表示审核者隐藏（通过 ResolveReport）。如果未来引入自动审核（方向五），系统将无法区分"作者已阅后撤回"和"监管行动"——这两种情况具有不同的撤销策略和通知语义。

**评论审核虽有端点，但缺乏统一工作台。** 验证报告正确地指出：存在面向作者的 `ListReports` 和 `ResolveReport` 端点（支持自我审核），但管理员视角的全局审核队列不存在。当前架构将审核建模为分散的功能，而不是一个审核模块。

**领域模块开始分散，但尚未完全拆分。** 文件树显示已经有像 `internal/comment/`、`internal/message/`、`internal/file/` 这样的领域包——说明已经从 AGENTS.md 中记录的旧式 `internal/service/` + `internal/repository/` + `internal/handler/{http,grpc}` 单结构布局开始迁移。这是一项进行中的工作，尚未完成。

---

## 2. 扩展方向

### 方向 A：文件处理管线（P0）

**为什么需要：** 文件操作是 aero-id 中最重的数据路径。没有后处理，系统就无法提供缩略图预览（对基于消息的文件共享至关重要）、内容类型感知的转码（图像调整大小、视频压缩）或病毒扫描（对用户生成内容的安全审核至关重要）。这直接影响到 P0 指标——核心存储用例。

**核心挑战：**

1. **异步管线编排。** 当前文件创建是同步的。管线需要异步步骤，且每个步骤都可能失败或需要重试。你已有的 outbox 模式可以很好地充当这里的调度队列。

2. **状态建模与幂等性。** `domain.File` 需要 `ProcessingStatus` 枚举（Pending/Processing/Completed/Failed）。缩略图产生多个派生键（原始、缩略图、裁剪版本）。从失败步骤恢复必须保证幂等。

3. **存储成本。** 派生制品（缩略图、转码）消耗对象存储空间。需要废止策略（一旦原始文件被删除，制品也要随之清除）。

**预期的架构变更：**

```
file.Service.Create()  →  domain.File 写入 + outbox 事件 "file.created"
                                    ↓
                file.Processor (一个新的 outbox.Handler)
                  ├── virus scan (同步拒绝或标记)
                  ├── thumbnail generation (image/x)
                  ├── transcoding (video/x)
                  └── status → "completed" 更新
```

- 新文件：`internal/service/file_processor.go`——包含 `FileProcessor` outbox Handler，由 outbox Relay 驱动。
- `domain.File` 变更：增加 `ProcessingStatus`、`ThumbnailKeys []string`、`VirusScanAt *time.Time`。
- 新配置：`file_processing.thumbnail_sizes`、`file_processing.virus_scan_enabled`、`file_processing.allowed_mime_types`（对上传时 MIME 过滤的继承/扩展）。
- 存储接口变更：`FileStorage` 可能需要类似于 `Put` 的方法，用于存储从现有键派生的制品——或者采用带有源键的 `Copy` 方法。

**变更影响：** 文件 Get/Download 端点必须回退到缩略图（若已存在），意味着需要一个新的 `GET /files/:id/thumbnail` 路径，该路径映射 `ThumbnailKeys[0]` 到 presigned URL。由于添加了状态字段，客户端需要能够优雅地处理 Pending 状态。

### 方向 B：推送通知基础设施（P0）

**为什么需要：** 仅靠 SSE 无法覆盖移动端和离线场景。桌面浏览器标签页关闭时，用户就收不到通知。如果 aero-id 要成为实时通信平台，它必须通过 FCM（Android）和 APNs（Apple）提供。2026 年用户体验基线要求：即时通讯 app 必须支持推送通知。

**核心挑战：**

1. **设备注册生命周期。** 需要一个新的 `devices` 表（`uid`、`platform`、`push_token`、`enabled`、`created_at`、`updated_at`）。令牌会轮换，用户会注销——需要 CRUD 端点加上批量失效机制。

2. **推送凭证的密钥管理。** FCM 使用一个服务器密钥；APNs 使用带有密钥 ID 和团队 ID 的 p8 证书或 JWT。这些是全局性的基础设施密钥，不能与业务代码混在一起——需要安全启动时加载和轮换。

3. **通知去重。** SSE 推送可能早于 FCM 推送到达。如果客户端同时接收两者，用户会看到重复通知。需要去重策略（SSE 中包含 `event_id`，移动端忽略已处理过的 ID）。

4. **设备令牌的 SSO 关联。** 你目前不存储设备令牌——但推送通知需要感知用户身份。一个用户有 N 个设备，每个设备有一个令牌。UA 需要将通知路由到正确的令牌集。

**预期的架构变更：**

```
service.NotificationPusher（当前接口）
  ├── hub.Hub（SSE——已有渠道）
  └── push.FCMClient（新增）
      └── push.APNsClient（新增）

Dispatcher 现在将通知推送到两个 pusher：
  Publish(uid, eventType, data, eventID)  →  SSE（在线）
                                          →  FCM/APNs（始终推送，设备自行去重）
```

- 新包：`internal/push/{fcm,apns,router}.go`。
- 新表：`devices`（`uid TEXT, platform TEXT CHECK(platform IN ('ios','android')), push_token TEXT, enabled BOOLEAN`）。
- 新端点：`POST /v1/devices`、`DELETE /v1/devices/:token`、`PATCH /v1/devices/:token`。
- 配置变更：`push.fcm.credentials_path`、`push.apns.key_path`、`push.apns.key_id`、`push.apns.team_id`。

**变更影响：** `NotificationPusher` 接口需要扩展吗？目前它是四参数 `Publish(uid, eventType, data, eventID)`。FCM/APNs 推送还需要设备平台信息和通知的 Title/Body 文本。建议要么（a）添加一个重载方法，要么（b）将 payload 改为携带渲染提示的结构化类型。方案（b）更简洁，但对仅 SSE 的后端是破坏性变更——需要一个适配器。

### 方向 C：分布式会话/缓存层（P1）

**为什么需要：** 架构以内存状态（SSO 缓存、速率限制器、Hub）为假设前提。在多副本部署中：
- 实例 A 上验证过的令牌，在实例 B 上需要重新验证，击穿各个缓存。
- 速率限制器每个实例独立工作，允许在 D 个副本中有效速率达到 D×RPS。
- SSE 连接绑定到单个实例——副本 B 上的用户无法收到副本 A 上生成的事件的通知。

**核心挑战：**

1. **缓存拓扑。** 你希望 SSO 令牌缓存和用户缓存命中共享内存中的 Redis/KeyDB。写入后失效？还是 TTL 驱逐？对于 SSO 令牌，TTL 驱逐是可以接受的（因为令牌本身已经有过期时间）。对于用户缓存，写入后失效（接收 SSO 的变更事件）更精确，但增加了复杂性。

2. **速率限制原子性。** 每个副本的 `ratelimit.Limiter` 使用滑动窗口或令牌桶。Redis 等效方案需要像 `INCR` + `EXPIRE` 这样的原子操作来进行每窗口计数，或者使用 Lua 脚本进行更复杂的窗口逻辑。不过，值得注意的是：这带来了对 Redis 的新增基础设施依赖。

3. **跨副本的 SSE 扇出。** 当副本 A 上的 dispatcher 创建通知时，副本 B 上的用户 SSE 连接不会收到它。解决此问题的方法有：（a）通过 Redis Pub/Sub 扇出——将一个事件发布到 Redis，所有副本订阅并推送到本地 Hub；（b）使用与消息队列（NATS、RabbitMQ）结合的 sticky sessions（粘性会话）。方案（a）对现有代码侵入性最小。

**预期的架构变更：**

```
Redis 连接池 → SSO 缓存层（替代 sync.Map）
                   ↓
           速率限制层（替代每副本 map）
                   ↓
            Hub 跨副本扇出（可选）
```

- 新配置：`cache.redis.url`、`cache.redis.pool_size`（可选——可用默认值兜底）。
- 缓存抽象：`internal/pkg/cache/token_cache.go`——带本地 L1 + Redis L2 的接口（Caffeine 风格分层，但更简单）。
- 速率限制：为 Redis 添加 `RateLimiter` 适配器，保留现有每副本实现。
- Hub 扩展：如果使用 Redis Pub/Sub，Hub 需要订阅频道并将远端事件注入本地订阅者。

**变更影响：** SSO 令牌验证是每一次经过身份验证的请求的热路径。引入 Redis 查询增加了延迟（尽管只是微秒级）。当前的 `sync.Map` 实现没有网络往返。折中方案：保留 L1 本地缓存（短 TTL，如 10 秒），Redis 作为 L2。这样可以在大多数情况下保持亚毫秒级延迟，同时仍支持跨副本缓存共享。

### 方向 D：群组消息/对话（P1）

**为什么需要：** 消息模型严格是一对一的（`FromUID`/`ToUID`）。任何涉及 N>2 人的用户需求都需要在应用层之上进行组合。群组消息是即时通讯的基本特性，对于 aero-id 而言，这是一个垂直增长的潜力方向。

**核心挑战：**

1. **消息域模型重构。** 当前 `domain.Message` 包含 `FromUID`、`ToUID`。群组需要 `ConversationID`，其中包含一个成员表，每个成员都存储 `LastReadAt`。这是一个破坏性变更，影响到所有消息查询。

2. **成员管理。** 谁可以邀请？谁可以移除？群组创建者是否拥有特殊权限？需要群组成员模型、角色（admin/member）以及审计日志。

3. **通知复杂性。** 群组消息中一次 `message.sent` 事件会被扇出到 N-1 个接收者。当前的 outbox 事件模式 `message.sent` 只有一个 `to_uid`，无法表示扇出。你需要一个新的 `group.message.sent` 事件，dispatcher 会为每个成员分别创建一条通知行。

4. **与现有一对一模型的向后兼容性。** 理想情况下，一对一对话应该作为成员为 2 的群组的特例来对待。但考虑到所有现有代码都使用 `(from_uid, to_uid)` 模式，拆掉重做的成本很高。

**预期的架构变更：**

```
新表：conversations、conversation_members（role TEXT, last_read_at）
新 domain 对象：GroupConversation

message.Service.Conversation() 跨两个表进行联合查询：
  - 如果 with_uid ≠ 群组 ID，则走原有的纯 SQL 路径。
  - 如果 with_uid 是群组 ID，则新的代码路径查询群组查询。
```

**两个可选项：**

| 方案 | 优点 | 缺点 |
|------|------|------|
| **方案 A：与现有模型共存** | 对当前一对一路径零破坏性变更 | 群组消息和 P2P 消息有不同的查询路径；联合通知需要两个事件模式 |
| **方案 B：将 P2P 重构为群组成员=2 的子类型** | 统一的消息模型；简化的通知扇出 | 大规模破坏性变更；需要重写所有现有消息查询和测试；迁移成本极高 |

建议方案 A 作为第一阶段，方案 B 作为涉及审计的长期重构。

**通知影响：** `message.sent` 事件模式需要扩展以支持 `conversation_id`。dispatcher 中对 `to_uid` 的检查必须修改，使其在 `conversation_id` 存在时进行扇出，而不是发送给单个 `to_uid`。

### 方向 E：自动化审核与审核工作台（P2）

**为什么需要：** 目前审核是分散的且纯人工操作。在用户生成内容的规模上，人工审核无法满足需求。自动化审核（基于模式匹配、用户声誉或外部推理 API）是内容审核的关键防线。此外，缺少审核工作台意味着运营团队无法集中管理工作队列。

**核心挑战：**

1. **审核与同态不可区分性。** `CommentStatusHidden`（值 3）既用于作者隐藏也用于审核隐藏。系统无法区分用户主动撤回内容和因违反政策而被隐藏的内容。修复方案：要么（a）增加第四个状态值 `CommentStatusModerated`，要么（b）增加一个 `HiddenReason` 枚举字段（`self` / `moderator` / `auto_flag`）。

2. **外部推理集成。** 自动化扫描（图片/文本）需要调用外部 API（AWS Rekognition、Google 的 Perspective API 或自建模型）。这些 API 有延迟和成本。异步处理是必须的。

3. **通知类别。** 当评论被审核隐藏或标记时，作者应收到通知。目前 notificationFor() 中没有 `comment.hidden` 或 `comment.flagged` 事件模式。

**预期的架构变更：**

```
管理员端点：GET /v1/admin/reports（全局队列，按状态筛选）
            POST /v1/admin/reports/:id/action（resolve/dismiss/ban-user）

自动审核：
  comment.Service.Create()  →  outbox 事件 "comment.created"
                  ↓
      comment.ModerationHandler（outbox.Handler）
        ├── spam 评分（启发式 / API）
        ├── 图片扫描（如果有附件）
        ├── 用户信誉检查
        └── 决定：NORMAL | HIDDEN（auto_moderated=True）+ 通知

Comment/File domain 扩展：
  - HiddenReason 枚举（self / moderator / auto）
  - AutoModeratedAt *time.Time
  - Comment.ReportCount（物化计数，替代每次统计查询）
```

---

## 3. 接口设计建议

### 3.1 原则

- **现有接口保持不变**——对于每个扩展方向（方向 A 到 E），已有接口中的新方法都是可选的，采用函数式选项默认关闭。这一点与现有的 `With*` 模式一致。
- **领域事件模式应遵循命名约定**——`{entity}.{action}`（例如 `file.created`、`comment.auto_moderated`、`message.group_sent`）。这些应当在一个共享的位置集中声明。
- **Handler 的输入输出是领域类型，而非传输类型**——HTTP 和 gRPC handler 都应使用相同的 `service.CommentService` 接口。这一原则目前已经得到强有力的遵守，应当保持。

### 3.2 新的抽象层

**1. 通知类型常量。** 将每个事件类型的字符串字面量提取到 `internal/domain/notification.go` 中的一个 `type NotifType string` 常量组中：

```
const (
    NotifTypeMessageReceived  NotifType = "message.received"
    NotifTypeReadReceipt      NotifType = "message.read_receipt"
    NotifTypeCommentReply     NotifType = "comment.reply"
    // future additions:
    NotifTypeCommentFlagged   NotifType = "comment.flagged"
    NotifTypeCommentHidden    NotifType = "comment.hidden"
    NotifTypeFileShared       NotifType = "file.shared"
)
```

**影响：** `notificationFor()` 中的 switch 语句将使用常量，而非字符串。添加新类型变得可搜索、可重构，且易于与文档保持同步。

**2. 带渲染提示的结构化通知 payload，替代裸露的 JSON。** 目前，`Publish(uid, eventType, data, eventID)` 中的 `data string` 参数是序列化后的 `domain.Notification` JSON。对于推送方向（FCM/APNs），输出渲染器需要知道 Title 和 Body。引入一个 `NotificationPayload` 结构体：

```go
type NotificationPayload struct {
    EventID   int64  `json:"event_id"`
    EventType string `json:"event_type"` 
    Title     string `json:"title"`
    Body      string `json:"body"`
    DeepLink  string `json:"deep_link,omitempty"`
    Raw       json.RawMessage `json:"raw,omitempty"` // 原始领域 payload
}
```

**与现有接口的兼容性：** 添加一个接收 `NotificationPayload` 的新 `PublishPayload` 方法，保留旧的 `Publish` 方法不变，用适配器包装。或者，采用内部重载——SSE 推送可以使用 `Title/Body/DeepLink` 生成富通知，同时保留 `Raw` 供客户端自行处理。

### 3.3 向后兼容性策略

- **为领域模型添加新字段，而非修改现有字段。** 例如，在 `domain.Message` 中添加 `ConversationID *ID` 字段，可空，保留 `FromUID/ToUID` 用于一对一消息。
- **仓库中的新表（devices、conversations）不修改现有模式。**
- **新的 outbox 事件类型**（`file.created`、`comment.created`）对现有 handler 是透明的——dispatcher 中的 default case 会忽略它们。
- **对于文件处理管线**，`domain.File` 中的新 `ProcessingStatus` 字段默认为 `""` 或 `"pending"`，因此现有客户端读取行记录时不需要感知处理进度。

---

## 4. 技术选型

### 4.1 新增依赖概览

| 扩展方向 | 新依赖 | 决策依据 |
|----------|--------|----------|
| 推送通知（B） | `firebase/google-cloud-go`（FCM）、`github.com/sideshow/apns2`（APNs） | FCM SDK 是移动推送的事实标准；`apns2` 是使用最广泛的 Go APNs 库，维护活跃 |
| 缓存/分布式会话（C） | `github.com/redis/go-redis/v9` | 最成熟的 Go Redis 客户端；若要求轻量级，可考虑 `rueidis`（Resp3 原生） |
| 文件处理（A） | 图片：`github.com/disintegration/imaging`（调整大小）；视频：`ffmpeg`（进程外） | 图片调整大小在 Go 中绑定 CPUS 效率足够；转码仍是进程外任务，因为 cgo 集成复杂 |
| 文件处理（A） | 病毒扫描：`ClamAV`（进程外，通过 `clamd` TCP） | ClamAV 是标准。Go 绑定（`github.com/dutchcoders/go-clamd`）通过 TCP 套接字通信，实现进程隔离 |
| 自动审核（E） | 可选：`OpenAI SDK` 或 `Perspective API` 客户端 | 不固定在某个供应商上；外部推理应通过 `ModerationEngine` 接口抽象，便于切换或自建模型 |

### 4.2 自建 vs 采购

| 决策 | 推荐 | 理由 |
|------|------|------|
| 图片缩略图 | 自建（`imaging` 库） | 需求简单（调整大小、裁剪）、延迟要求低（响应式）、无第三方 API 成本 |
| 病毒扫描 | 采购（ClamAV 引擎） | ClamAV 是开源标准，成熟且高效。自行签名病毒特征数据不可行 |
| 内容审核（文本） | 自建启发式 → 采购 API | 从基于规则的过滤开始（关键词、重复模式）。规模扩大后接入外部推理 API |
| 推送通知 | 采购（FCM / APNs） | 无商业替代方案。需要苹果和谷歌的原生基础设施 |
| 分布式缓存 | 采购（Redis / KeyDB） | Redis 是行业标准。自建一致性哈希缓存是不必要的工程投入 |

### 4.3 评估标准

针对新的 Go 依赖采用**最小入侵策略**：

1. **首选标准库**——`net/http`（你已经有了）、`crypto`（用于 HMAC ——你已经有了）。
2. **次选广泛采用的、活跃的第三方库**——`go-redis`、`firebase` SDK。
3. **进程外组件**——需要 `exec.Command` 或 gRPC 双工流。不应在 Go 进程内绑定 C 库（除非绝对必要）。ClamAV 通过 TCP 通信；ffmpeg 通过子进程通信。

### 4.4 不合规项

- **不需要消息队列（RabbitMQ / Kafka / NATS）。** outbox 模式已经足够。扇出用例（群组消息、推送通知）可以通过 outbox 本身处理，不需要单独的消息中间件。直到 outbox 吞吐量出现瓶颈之前，都不应该引入消息队列。
- **不需要 GraphQL。** aero-id 的查询模式是 RESTful + 游标分页。GraphQL 会增加无谓的复杂度。

---

## 5. 实施路线图

### 5.1 优先级排序

```
P0  [方向 A：文件处理管线]
    └─ 依赖：方向 A 只依赖于现有 outbox，不依赖于方向 B/C/D/E
    └─ 原因：核心存储用例；影响所有文件上传

P0  [方向 B：推送通知基础设施]
    └─ 依赖：方向 B 需要设备注册端点（新表） + FCM/APNs 客户端
    └─ 原因：移动端即时通讯的基本要求
    
P1  [方向 C：分布式缓存/会话]
    └─ 依赖：方向 C 对方向 A/B 在架构上独立，但能提升所有路径的性能
    └─ 原因：规模化多副本部署的阻塞项
    
P1  [方向 D：群组消息]
    └─ 依赖：方向 D 需要消息模型重构，与方向 B（群组推送通知扇出）存在部分重叠
    └─ 原因：垂直增长方向；P1 而非 P0 是因为核心 1:1 用例已可用
    
P2  [方向 E：自动审核与工作台]
    └─ 依赖：方向 E 需要方向 A 的 outbox 事件（文件扫描），且新的通知类型需要方向 B 的推送管道
    └─ 原因：P2 是因为监管是支持性功能，而非核心基础设施
```

### 5.2 阶段划分

**第一阶段：基础管线（4-6 周）** [方向 A 的 subset + 方向 B]

- 里程碑 1a：`file.created` outbox 事件 + `domain.File.processing_status`——无视觉产出，合约已建立。
- 里程碑 1b：设备注册端点（`POST/DELETE /v1/devices`）+ `devices` 表。
- 里程碑 1c：FCM 推送集成——安卓设备在有未读通知时收到推送。
- 里程碑 1d：缩略图管线——图片文件在创建后 30 秒内获得缩略图。
- 里程碑 1e：`FileProcessor` outbox handler，可扩展至任意处理器（对管线自身进行测试）。

**第二阶段：规模化（6-8 周）** [方向 C + 方向 D]

- 里程碑 2a：引入 Redis 连接——SSO 缓存迁移至 L1（本地）/ L2（Redis）分层模型。
- 里程碑 2b：速率限制器迁移至 Redis——以向下兼容的方式（新旧实现通过接口切换）。
- 里程碑 2c：群组对话——`conversations` + `conversation_members` 表；`message.sent` 事件支持 `conversation_id`；群组创建/邀请/离开端点。
- 里程碑 2d：群组通知扇出——dispatcher 为每个群组成员创建一条 `message.received` 行。

**第三阶段：监管与自动化（8-10 周）** [方向 E + 方向 A 补全]

- 里程碑 3a：审核工作台——`GET /v1/admin/reports` 端点 + 全局审核队列 UI（可由现有 handler 支持）。
- 里程碑 3b：`comment.created` outbox 事件 + `ModerationHandler`（从关键词匹配的启发式扫描开始）。
- 里程碑 3c：`HiddenReason` 字段 + `CommentStatusModerated`（值 4）——区分作者操作和监管操作。
- 里程碑 3d：通知类型常量提取 + 结构化的 `NotificationPayload`（领域收敛）。

### 5.3 风险与缓解策略

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| 方向 A（文件处理）在 outbox 行上产生背压 | 中 | 高 | 为文件处理添加独立的 outbox handler，使用单独的轮询间隔；为慢速处理器预留缓冲池 |
| 方向 B（推送通知）引入应用程序凭据泄露 | 低 | 严重 | FCM/APNs 密钥通过 `AERO_ID_PUSH_*` 环境变量注入（遵循现有约定）；不要在日志或错误消息中记录凭据 |
| 方向 C（Redis）变成后知后觉的单点故障 | 中 | 高 | 每副本回退到本地缓存（L1 始终在线）；不将 Redis 作为每个请求的必选路径——采用尽力而为 + 兜底方式 |
| 方向 D（群组消息）扩展通知扇出超出 outbox 容量 | 中 | 中 | 对通知行采用批量插入，而非逐行模式；监视 relay 延迟作为早期预警信号；在 outbox 表中为 `conversation_id` 建立索引 |
| 方向 E（自动审核）产生误报导致用户困惑 | 高 | 中 | 所有自动隐藏操作均要求人工确认（24 小时内无操作则自动解除隐藏）；在审核队列中显示清晰的原因；提供申诉流程 |
| 多个并行方向在同一条代码路径上产生合并冲突 | 中 | 中 | 每个方向在独立分支上开发；先合并方向 A（文件 outbox）→ 方向 B（推送）→ 方向 D（群组）；方向 C 和方向 E 并行开发，与主干保持同步 |

---

## 总结

aero-id 的架构对于当前领域范围（带文件存储、评论和通知的一对一消息）来说是经过深思熟虑且经过实战检验的。分层抽象、事务性 outbox 模式和可选功能选项等决策均是合理的。

五个架构缺口中的每一个都已经有了一个验证过的代码基础来扩展：
- 文件 outbox 与消息 outbox 的机制相同——缺失的是事件发射。
- 推送通知只是 `NotificationPusher` 接口的新实现——SSE 路径不变。
- 分布式缓存是现有内存原件的替代品——接口保持不变。
- 群组消息是一个新的领域模型，与现有的 1:1 模型共存，而不是取而代之。
- 自动审核作为一个新的 outbox handler，插入到已建立的 relay 中。

真正的架构风险不在于任何单个方向，而在于**组合起来**——当文件处理在与推送通知相同的 outbox relay 上进行时，背压可能最终导致消息通知发送延迟。建议为文件创建的 handler 使用一个单独的 relay 实例（或者至少确保文件处理 handler 在幂等性和错误传播方面行为正确，不阻塞消息专用通道）。
