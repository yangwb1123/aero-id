现在我拥有对代码库的全面理解。以下是我从架构角度的分析。

---

# 架构分析：aero-id

## 1. 架构评估

### 当前架构的优势

**1. 明确的单向依赖**
```
handler → service → repository → DB
                 ├→ storage
                 └→ sso
```
这个约束在代码库中得到严格强制执行。`service` 层是单一的决策点，使得安全性和所有权检查易于推理。没有逻辑泄漏到传输层。

**2. 跨边界的清晰接口**  
服务接口（`MessageService`、`FileService` 等）在 `internal/service/service.go` 中定义。`UserDirectory` 抽象（在 `internal/message/message.go` 中定义的用户目录接口）将 SSO 客户端缩小为服务层实际需要的功能：批量按 uid 获取用户。这值值得称赞——它通过将 gRPC/缓存实现细节排除在业务逻辑之外，使单元测试保持纯净。

**3. 带有领域事件的可靠出站模式**  
出站模式（`internal/outbox/`）执行得稳健。通知、计数器以及可选的 webhook 都在与产生它们的数据相同的数据库事务中写入，因此下游效应是可靠的事件驱动的，而不是尽力而为的。

**4. 优雅的错误分层**  
`internal/pkg/errors` 定义了已知的业务级错误类别（`BadRequest`、`Unauthorized`、`Forbidden`、`NotFound`、`Conflict`、`Internal`），每个都携带 HTTP 和 gRPC 等价状态码。存储库返回原始的 `gorm.ErrRecordNotFound`；服务层用 `.Wrap()` 将它们包装到 `AppError` 中；处理程序呈现它们。这在各种传输方式中提供了一致、安全的错误响应，同时保持原因可追踪。

**5. 带有 UUIDv7 的分页**  
使用 UUIDv7（时间排序）作为游标的基于游标的分页是对偏移量分页的一种明智选择。它避免了“丢失/重复”问题，并且随着行更新/删除，游标仍然稳定。选择 `LIMIT n+1` 格式来检测下一页是紧凑且有效的。

**6. 特定领域的强约束**  
注释嵌套规则（恰好两层）在 `domain` 和 `service` 两个层面都实施了。这种冗余是审慎的——它在靠近源头的地方捕获错误，同时确保即使在域逻辑出现错误时约束也能通过服务层得到保持。

### 当前架构的局限性

**1. 双重包结构（FIC 中的问题 - 无标题）**  
在重构期间，代码库中存在两个在用的包方案：

- **旧方案**：`internal/service/message.go`、`internal/repository/message.go`、`internal/handler/http/message.go`，带有 `internal/service.go` 中的大接口
- **新方案(按特性划分)**：`internal/message/service.go`、`internal/message/repository.go`、`internal/message/message.go`，每个包中都有小型、专注的接口

目前`internal/message/repository.go` 和 `internal/service/message.go` 都在被引用。比如 `internal/app/app.go`（组合根）引用**两个**方案：
```go
msgRepo := repository.NewMessageRepository(gdb, outboxRepo, unreadCounter)  // 旧方案
// 而新方案在 internal/message/repository.go 和 internal/message/service.go 中
```

这种**双重方案**意味着存在功能重复（发送、收件箱、对话、标记已读、编辑、删除、反应——都写了两次）。增量式跨越在实践上是可以的，但特性工作（比如方向一中的群组对话）如果添加到一个方案而不是另一个方案中，风险就会增加。

**2. 没有实体间的外键**  
`messages` 表没有到 `files`（`file_id`）的 FK，注释也没有 FK 到它们的目标。代码层面上，服务和存储库通过应用程序逻辑强制执行引用完整性，而不是通过数据库约束。这意味着在引入级联行为（方向二）时，如果没有显式查询就会缺少数据库保证。如果没有外键，操作（如删除引用文件的文件）可能会隐藏地破坏数据的完整性。

**3. GORM 与基础设施的紧耦合**  
存储库直接依赖于 `*gorm.DB`，不仅用于查询执行，还用于事务控制。`outbox.Append()` 的签名是：
```go
Append(ctx context.Context, tx *gorm.DB, e *Event) error
```
这暴露了 ORM 的细节。虽然这对于采用来说很实用，但它使得在没有事务协调器的情况下迁移到不同的持久化策略变得更难。一个合适的抽象应该使用 `Transaction(ctx, func(Repository) error)` 模式，而不是传递 `*gorm.DB`。

**4. 单一的内存基础设施**  
在部署中，以下组件是单一实例并驻留在内存中：

| 组件 | 文件 | 范围 |
|---|---|---|
| SSE Hub | `internal/hub/hub.go` | `sync.Map`，按 uid 分片 |
| SSO 令牌缓存 | `internal/sso/token_cache.go` | `map[string]tokenEntry` + `sync.RWMutex` |
| SSO 用户缓存 | `internal/sso/user_cache.go` | `map[string]userEntry` + `sync.RWMutex` |
| 速率限制器 | `internal/pkg/ratelimit/ratelimit.go` | 内存中的令牌桶 |
| 单次飞行组 | `internal/sso/client.go` | `singleflight.Group` |

其中，**SSE Hub** 对于水平扩展来说是最关键的瓶颈——多实例部署会导致每个实例拥有自己的连接池，而发布仅传递给连接建立的那个实例。

**5. 非通用通知路由**  
`internal/notification/dispatcher.go` 中的 `Dispatcher.Handle` 直接实现特定的事件类型：
```go
switch e.EventType {
case "message.sent": return d.notificationMessageSent(e)
case "message.read": return d.notificationMessageRead(e)
case "comment.replied": return d.notificationCommentReplied(e)
default: return nil
}
```
使用 switch-case 的方法意味着每种新的事件类型都需要更改分派器，并且群组消息事件（方向一）需要在此处添加新的大小写分支。一套基于注册表（注册码由事件类型标识）的方法会更具可扩展性，但考虑到目前只有 3 种事件类型，这还不是严重问题。

**6. 缺少针对跨领域问题的封装**  
速率限制器、SSE Hub 和 SSO 缓存都在应用层被注入和引用（`internal/app/app.go`），而不是通过抽象的接口。这使得在没有对组合根进行重大修改的情况下，无法独立更换每个组件。

### 关键设计决策评估

| 决策 | 判断 | 理由 |
|---|---|---|
| 向外键传达 ID 为字符串的 SSO | ✅ 正确的 | 身份验证是不相关关注点；通过 gRPC 委托给 SSO 可避免身份验证冗余。 |
| 通知的出站行为 | ✅ 正确的 | 为通知提供即时的“恰好一次”语义，而无需成熟的发布-订阅或事件流平台。 |
| 软删除贯穿全局 | ✅ 正确的 | 审计记录和恢复能力对于用户生成的数据至关重要。 |
| 应用启动时不自动迁移 | ✅ 正确的 | 通过将迁移作为部署管道中的显式步骤，可以避免意外的模式变更。 |
| 自包含的 Prometheus 指标 | ⚠️ 合理的| 避免了对 `client_golang` 的外部依赖，但指标输出必须手动与基础设施期望对齐。 |
| 整个系统的 `uuid.UUID` | ✅ 正确的 | UUIDv7 提供时间排序，减少了 B 树碎片化。 |
| gorilla/mux 替换为 gin | ✅ 正确的 | Gin 更快，社区广泛使用，本项目中已有的工具链兼容。 |
| 消息没有 `conversation_id` | ⚠️ 尚可的| 对于只支持 1:1 的情况来说可以接受，但群组对话需要架构变更。 |
| 使用 GORM 进行查询 | ⚠️ 合理的| 提高了开发速度，但使得针对特定查询使用原始 SQL 时的优化更难。 |

---

## 2. 扩展方向

### 方向 A：群组对话和对话管理

**为什么需要**  
当前消息模型是纯 1:1 的：消息通过 `(from_uid, to_uid)` 进行寻址。收件箱列出了所有发给收件人的邮件，对话是 `uidA ↔ uidB` 的集合。此模型无法支持：
- N 人讨论（群聊）
- 对话级别的元数据（标题、头像、归档状态）
- 离开或离开后重新加入对话
- 对话范围的读取状态与每个消息读取状态相对

这是请求中最明显的空白。

**核心挑战**
1. **现有消息的迁移范围**  
   现有的每一条消息都需要分配到一个“鸽笼”对话（`uidA ↔ uidB` 对）。延迟创建（在首次查询时检测）修改了 `Inbox()` 和 `Conversation()` 的核心查询路径，这是对已有严格限制的核心功能进行的侵入性修改。
2. **搜索隔离**  
   没有 `conversation_id` 进行过滤，群组消息就会混入个人搜索结果。`SearchMessages()` 查询需要连接或使用 `conversation_id` 进行过滤。
3. **读取状态的复杂性**  
   对于群组对话来说，每个消息的读取跟踪是昂贵的（`O(N)` 每个消息的更新操作）。需要对话级别的读取指针（`last_read_message_id` 每个参与者）。
4. **对话成员的准入控制**  
   在 1:1 对话中，成员资格是推断的。在群组对话中，需要显式的增删操作。这增加了并发控制的复杂性。

**预期的架构变更**
- `domain/message.go`：添加 `ConversationID *ID`（可选，用于 1:1；群组需要）
- 新表：`conversations`、`conversation_members`、`conversation_last_read`
- `internal/message/service.go`：`SendMessageInput` 变为 `ConversationID` 字段；路由逻辑根据 `ConversationID` 是否设置进行分支
- 用于批量更新对话读取状态的新存储库方法
- 组通知的分派器变更（向所有成员推送，而不是单个对等方）

**对现有系统的影响**
- 默认情况下保持向后兼容：没有 `conversation_id` 的消息继续使用 `(from_uid, to_uid)` 对进行处理
- `Inbox()` 需要 union 或额外的查询：自己的 1:1 消息 + 群组对话消息
- 迁移期间性能会受到影响：在查询路径中添加 `conversation_id IS NULL OR ...` 子句

### 方向 B：分布式基础设施抽象

**为什么需要**  
目前，3 个关键组件被锁定在内存和单个实例中：
1. **SSE Hub**：多实例意味着通知丢失
2. **SSO 缓存**：多实例意味着每个实例单独从 SSO 拉取，放大了出口流量
3. **速率限制器**：在多个实例之间不起作用

在任何水平扩展计划中，这些都是前期的工作。

**核心挑战**
1. **SSE 的发布-订阅选择**  
   选项：
   - **Redis Pub/Sub**：简单、常见、项目依赖已熟悉。如果 Redis 重启，消息会有丢失风险。
   - **Redis Streams**：消费组提供持久性，但增加了复杂性。
   - **专用流处理**对于初始需求来说过于复杂。
2. **缓存失效**  
   Redis 中的缓存需要 TTL 策略（当前是 `/expiry` 键）。跨多个实例的并发写入需要谨慎处理。
3. **作为抽象而非迁移的目标**  
   关键步骤是**先定义接口**，然后分别实现内存和 Redis 版本。这样方向一可以与方向 B 并行进行，方向一在消息路由层仅预留接口。

**预期的架构变更**
- 新包：`internal/pkg/cache/` 包含 `CacheBackend` 接口（`Get`、`Set`、`Delete`、`GetStale`）
- 将 `TokenCache` 和 `UserCache` 适配到 `CacheBackend` 接口中
- 将 `Hub.Publish()` 后面挂接一个 `Publisher` 接口（`internal/hub/publisher.go` 中的 `Publisher`）
- 用于令牌桶的分发速率限制器（`internal/pkg/ratelimit/` 中的可选后端）
- 新配置段 `[cache]` 与 `type: memory|redis`

**对现有系统的影响**
- 如果定义正确，内存实现的方向将完全零改变的
- 如果通过接口定义，从 `sync.Map` 迁移到 Redis 的过程可以逐步完成，一次一个组件
- 这不会改变任何业务逻辑

### 方向 C：资源级联和引用完整性

**为什么需要**  
当前，如果用户删除了某条消息，该消息引用的文件在 `files` 表中仍然是活跃的（软删除）。同样，删除文件不会级联删除该文件上的注释。虽然这种行为可能有些时候是可取的（“是的，删除该文件，但保留注释”），但系统没有提供任何策略：**用户没有注册表来了解文件删除时的行为**。

当提供者删除文件时，指向该文件的共享链接会变成损坏状态。在规模上，这会产生孤立状态，在数据层面很难清理。

**核心挑战**
1. **软删除与硬删除**  
   注释是软删除的，但存储库引用（`storage_refs` 表）可能需要在源被软删除后保持活跃，以确保 GC 不会过早地收集 blob。需要维护引用计数。
2. **注释中缺少的目标活性检查**  
   正如反馈文档正确指出的，`ListByTarget` 不会 JOIN 检查目标行是否被删除。单个存储库可以过滤注释状态，但不能通过连接过滤目标状态。表现上，对已删除文件的注释仍然显示。
3. **所有权跨歧异性**  
   文件有 `owner_uid`；注释也有 `owner_uid`；但消息同时有 `from_uid` 和 `to_uid`。级联策略在每种实体类型上可能不同（文件删除应该级联到注释吗？消息删除应该级联到文件吗？）。

**预期的架构变更**
- `domain/shared.go`：为级联策略定义一个接口（`CascadePolicy`）
- 存储库方法：用于级联删除的 `CascadeDeleteFile`、`CascadeDeleteMessage`
- 一个后台作业（类似于 `storage/gc/sweeper.go`），用于协调表之间的级联——在请求路径之外
- 可选：多态目标注释的 FK（在应用程序级别，而不是数据库级别）

**对现有系统的影响**
- 低——注释已经过滤了 `status = CommentStatusNormal`
- 最大的变更是在 `domain` 层：为级联策略添加枚举类型
- 在请求路径上添加级联删除会在删除操作上增加 `O(relatives)` 的数据库负载

### 方向 D：多租户隔离（空间/项目）

**为什么需要**  
aero-id 将自身定位为“账户拥有的数据”服务。当前，所有文件、消息和注释都是跨所有用户平坦化存在的（通过 `owner_uid` 关联）。在 SSO 层提供“组织/团队/空间”概念之前，目前无法按租户进行隔离。然而，**模式本身**可以预先准备。

方向一的群组对话自然映射到空间：每个对话是一个空间，具有独立的成员资格、文件和消息。一旦群组对话实现，将“空间”抽象提升为一级概念可能会自然发生。

**核心挑战**
1. **SSO 依赖性**  
   aero-id 目前没有用于空间/项目管理的表。这些信息必须来自 SSO 层（它提供 `org_id` / `team_id`）。没有这一层，空间管理要么需要在 aero-id 内部实现（职责蔓延），要么保持不在范围内。
2. **跨空间搜索**  
   如果引入了空间，搜索需要限定在用户有访问权限的空间内进行。这改变了 `SearchMessages` 查询的结构。
3. **数据迁移规模**  
   将现有的每所有者文件或每用户消息迁移到空间是一项重大的数据迁移。

**预期的架构变更**
- `files` 和 `messages` 表中可选的 `space_id` 列
- 插入时生成空间访问控制的中间件
- 跨空间路由通知

**对现有系统的影响**
- 在实现群组对话之前，最低限度地无事可做（群组对话是空间的基本单元）
- 低优先级；可以被推迟，直到租户需求实现

### 方向 E：可编程 Webhook 和事件驱动集成

**为什么需要**  
出站模式已经就位。当前的 `outbox.WebhookHandler` 将事件传递到单个端点。事件驱动的集成（连接外部系统、触发工作流、与其他服务同步）对于平台服务的自然演进来说是可以预期的。

**核心挑战**
1. **仅支持单个端点**  
   当前实现只支持一个 webhook URL。多个订阅者需要路由逻辑。
2. **重试和背压**  
   出站中继根据最大重试次数和死信队列来传递事件。如果 webhook 下游的调用者延迟了，它可能会影响出站传递管道。
3. **事件格式稳定性**  
   一旦外部系统依赖于非正式的有效负载结构，有效负载结构就成为契约。必须定义并版本化一个兼容的格式。

**预期的架构变更**
- `outbox` 包：多路由 `Router` 接口（按事件类型分发）
- 可选的每个订阅者重试策略
- 一个死信通知机制（用于告警的“事件已死信”事件）

**对现有系统的影响**
- 低——出站管道已经模块化
- 如果重试策略变得过于复杂，可能会引入对工作队列的依赖

---

## 3. 接口设计建议

### 关键模块的接口设计原则

**1. 服务接口应该保持精简，按特性划分**  

当前，`internal/service/service.go` 中的 `MessageService` 接口有 13 个方法。随着群组会话的引入，这个数字只会增长。代码库已经在向按特性划分的包（`internal/message/message.go`、`internal/block/block.go`）迁移，这一点确认了方向是正确的。

**指引**：
- 每个文件或包 5 个或更少的方法
- 相关的操作（增删改查 与 读取）拆分成不同的接口
- 在接口定义的地方导入它们的入参/出参类型，而不是在 `domain` 包中定义

**2. 跨切面问题应当通过接口抽象，而非通过依赖注入实现**  

当前，速率限制器和 SSE Hub 作为具体类型被注入。组合根（`app.go`）知道它们的内存实现。为了将来的可扩展性：

```
// 问题：组合根耦合到内存实现
limiter := ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)

// 更好的方案：定义接口，组合根只提供一个实现
type RateLimiter interface {
    Allow(key string) bool
}
// 在生产中 wired 为 memoryLimiter 或 redisLimiter
```

**3. 存储库接口应当保持无 GORM**  

`outbox.Append(ctx, tx *gorm.DB, e *Event)` 暴露了 ORM 事务。更好的方案是将事务封装在存储库接口中：

```go
// 而不是传递 *gorm.DB
type UnitOfWork interface {
    Repository() OutboxRepository
    Commit() error
    Rollback() error
}

type MessageRepository interface {
    Create(ctx context.Context, uow UnitOfWork, m *domain.Message, evt *outbox.Event) error
}
```

这会增加一些复杂性，但消除了跨包对 GORM 的依赖。

### 新的抽象层

**1. `internal/pkg/cache/` — 用于 SSO 令牌和用户缓存**  

```go
type CacheBackend interface {
    Get(ctx context.Context, key string) ([]byte, bool)
    GetStale(ctx context.Context, key string) ([]byte, bool)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration)
    Delete(ctx context.Context, key string)
    // 可选：原子性的 CAS 操作，用于速率限制
    Increment(ctx context.Context, key string, delta int64) (int64, error)
}
```

内存和 Redis 的实现。初始阶段只实现 `memory`——不影响特性工作。

**2. `internal/hub/publisher.go` — 用于 SSE 传递**  

```go
type Publisher interface {
    Publish(ctx context.Context, uid string, event hub.SSEEvent)
    Subscribe(ctx context.Context, uid string) (<-chan hub.SSEEvent, func())
    Close()
}
```

当前 Hub 直接实现此接口。Redis 实现将在后面使用 Pub/Sub 或 Streams。

**3. `internal/pkg/lock/` — 用于分布式锁**  

这在方向一和方向二中有用：类似于“我有资格向此对话发送消息吗？”或“我可以删除此文件而不干扰并发的 blob 回收吗？”的操作需要它。

```go
type DistributedLock interface {
    Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error)
    Release(ctx context.Context, key string) error
}
```

### 保持向后兼容性

**1. API 演化应当通过可选字段进行**  

在 HTTP 处理程序中引入 `conversation_id`：
```go
type sendMessageRequest struct {
    ToUID          string         `json:"to_uid"`
    ConversationID *string        `json:"conversation_id,omitempty"` // 可选
    Kind           int16          `json:"kind"`
    Content        string         `json:"content"`
    FileID         string         `json:"file_id"`
    Meta           map[string]any `json:"meta"`
}
```

如果 `conversation_id` 为 nil，则退回到 `(from_uid, to_uid)` 路由，这是 1:1 的行为。

**2. 新的 API 端点不应破坏旧的端点**  

群组会话 API 应当在新的端点上进行构建（`/v1/conversations/:id/messages` 与 POST `/v1/messages` 相对）。旧端点继续处理 1:1 消息。

**3. 以不同方式构建的新存储库方法不应修改现有方法**  

存储库方法应当采用一组不同的参数，或者后置到它们自己的文件中（`repository_conversation.go`）。

**4. 数据迁移应提供回滚路径**  

模式迁移应当具有 `down.sql` 文件（当前的操作规范）。数据迁移（将现有消息分配到对话中）应该是可逆的，或者设计为幂等的。

---

## 4. 技术选型

### 关于引入新技术栈的建议

**优先级 P0：Redis（或 Valkey）**

**理由**：
- 解决了最紧迫的瓶颈：SSE Hub 没有跨实例共享状态，SSO 缓存放大、速率限制无法水平扩展
- 无论架构方向如何，都是必要的——即使是 1:1 的应用，扩展也需要它
- 与当前技术栈有良好的协同：没有新的协议、没有新的同步原语

**慎重的做法**：
- 不要一次性强行引入。首先定义接口（方向 B）：`CacheBackend`、`Publisher`、`DistributedLock`
- 第 1 阶段：这些接口的内存实现（零新依赖，与特性工作并行进行）
- 第 2 阶段：这些接口的 Redis 实现（特性工作完成后的独立工作流）

**备选方案（不推荐 P0）**：

| 选项 | 权衡 |
|---|---|
| **NATS** | 更好的发布-订阅语义，持久性。但需要新的基础设施组件和操作知识。目前过度了。 |
| **RabbitMQ** | 熟悉的、强大的。但对于简单的缓存+发布-订阅用例来说过于重量级。 |
| **KeyDB** | Redis API 兼容，多线程。但社区较小，未在基础设施检查中验证。 |
| **Dragonfly** | 声称比 Redis 更快，但对我们的规模来说未经测试。 |

**关于 Redis 的推荐**：使用标准 Redis（或 Valkey，作为一个开源分支）用于缓存和发布-订阅场景。对于消息传递，保持使用双写（写入 Postgres，然后发布到 Redis 发布-订阅）——在这一点上，专用的消息队列会导致不必要的架构复杂性。

**不推荐**：
- **Kafka / Pulsar**：针对流式处理进行了优化，对于缓存和发布-订阅来说过度了。如果在未来对事件驱动集成（方向 E）的需求显著增长，可以考虑。
- **etcd / ZooKeeper**：对于共识和领导者选举来说是强大的，但 aero-id 不需要。简单的分布式锁应当使用 Redis 的 `SET NX EX` 模式来处理。
- **Elasticsearch**：不要用。当前的 FTS（PostgreSQL `tsvector`）对于初始规模来说已经足够了，除非用户有数千万条消息。到那时，为了延迟而引入专用搜索基础设施之前，评估备选方案（pgroonga、ZincSearch）。

### 第三方依赖评估标准

```yaml
添加到 go.mod 的依赖项应当符合以下条件：
  1.  已许可（MIT、Apache 2.0、BSD）——无 GPL（AGPL 会污染与 SSO gRPC 接口）
  2.  在 Go 1.26 上编译——无 CGo / 平台特定的汇编
  3.  通过 `go vet` 和 `staticcheck`——无 vet 冲突
  4.  已完整测试——代码覆盖率 > 70%
  5.  内存消耗可预测——不导致 OOM
  6.  没有在应用程序启动时执行代码的 init()——无副作用
  7.  在 M1/M2/M3 MacOS 上构建——当前的开发设置
  8.  由活跃的团队维护——在过去 6 个月内合并了 PR
```

**当前已存在的依赖项**：GORM 1.25、gin 1.10、viper 1.19、zap 1.27、testcontainers 0.32、AWS SDK v2、protobuf 1.36。这些都已经成熟且得到了良好维护。

**潜在新增依赖项**：
- `github.com/redis/go-redis/v9`：用于方向 B。经过全面测试，APL 许可，在 Go 生态系统中是标准。允许零代码变更从哨兵模式迁移到集群模式。
- `github.com/IBM/sarama`（用于 Kafka）：如果需要流式事件方向。不推荐用于 P0。

### 自研 vs 购买决策

| 组件 | 决策 | 理由 |
|---|---|---|
| **SSE Hub 分布** | 自研 | Redis Pub/Sub 包装器很简单（< 50 行）。购买意味着引入沉重的外部依赖。 |
| **缓存** | 自研（适配器） | 接口 + redis 实现大约 100 行。无需完整的缓存框架。 |
| **速率限制** | 自研（现有） | 当前实现已经很好地适应了。将其适配到 Redis + Lua 中可以通过令牌桶滑动窗口大约 50 行完成。 |
| **工作队列** | 自研（现有出站模式） | 出站模式处理可靠的交付。Kafka/Pulsar 的购买方案会引入需要操作的项目级事件驱动架构。 |
| **文件转换** | 购买（S3 事件 + Lambda） | 生成缩略图/转换通常在 aero-id 进程之外处理。使用 S3 事件触发 Lambda 比在 Go 中运行 ImageMagick 更合适。 |
| **实时协作** | 自研 | 方向一（群组对话）需要向对话成员推送消息。这本质上是一个应用并发问题，不是现成的产品。 |

---

## 5. 实施路线图

### 优先级调整（基于输入文档的微调）

```
P0（两个工作流并行）
├── 工作流 A：群组对话（方向一）
│   └── 第 1 步：添加 conversation_id 字段并迁移现有消息
│   └── 第 2 步：conversations 表和成员表
│   └── 第 3 步：更新发送/收件箱/对话查询以处理这两条路径
│   └── 第 4 步：通知适配器用于群组推送
└── 工作流 B：分布式原语（方向五）
    └── 第 0 步（立即）：在 internal/pkg/ 中定义 CacheBackend、Publisher、Lock 接口
    └── 第 1 步：内存实现（零新依赖）
    └── 第 2 步：Redis 实现（配置驱动，可选）
    └── 第 3 步：迁移 SSO 缓存以使用 CacheBackend
    └── 第 4 步：将 Hub 适配到 Publisher 接口
    └── 第 5 步：将速率限制器适配到 CacheBackend

P1（在工作流 B 之后立即开始）
├── 资源级联（方向二）
│   └── 注意：如果数据模型不变，则独立且风险低
│   └── 关键：目标活性检查（ListByTarget 未连接到目标表）
└── 可编程 Webhook（方向三）
    └── 注意：如果配置了 webhook URL，则可以在初始阶段以内存模式上线

P2（持续，不阻塞）
└── 文件转换（方向四）
    └── 完全独立；可以使用 S3 事件触发外部
    
以后
└── 多租户隔离（空间）
    └── 等待 SSO 层提供空间概念
```

### 阶段划分和里程碑

**阶段 0：立即开始（1-2 周）**

**目标**：为所有后续工作奠定基础，而不阻塞特性工作。

| 任务 | 文件 | 输出 |
|---|---|---|
| 定义 `CacheBackend` 接口 | `internal/pkg/cache/cache.go` | 具有内存实现的接口 |
| 定义 `Publisher` 接口 | `internal/hub/publisher.go` | 具有当前 Hub 实现的接口 |
| 定义 `Lock` 接口 | `internal/pkg/lock/lock.go` | 具有内存实现的接口 |
| 在 `config.go` 中添加 `[cache]` 配置段 | `internal/config/config.go` | 配置段 |
| 为 SSE Hub 的 `Publish` 添加 `ctx` 参数 | `internal/hub/adapter.go` | 向后兼容的适配器 |

**里程碑：M0 — 接口层就绪**  
验证：`make test && make lint` 通过。zero new dependencies。

**阶段 1：群组对话核心（4-6 周）**

| 任务 | 文件 | 输出 |
|---|---|---|
| 迁移 `000023_create_conversations` | `migrations/` | 向上 + 向下 |
| 迁移 `000024_create_conversation_members` | `migrations/` | 向上 + 向下 |
| 添加 `ConversationID *ID` 到 `domain.Message` | `internal/domain/message.go` | 新增字段 |
| 添加 `ConversationRepository` 接口 | `internal/message/repository.go` | 接口 + 实现 |
| 在消息服务中更新 `SendMessageInput` | `internal/message/message.go` | `ConversationID` 字段 |
| 更新通知分派器以处理群组事件 | `internal/notification/dispatcher.go` | 新增 `case` |
| 在收件人中添加 SSE 推送 | `internal/hub/hub.go` | 群组消息的批量发布 |

**里程碑：M1 — 群组对话在 monolith 中可用**  
验证：`make test && make test-integration && make lint`。群组消息端到端可用。

**阶段 2：分布式基础设施（2 周，如果在阶段 0 之后开始，则与阶段 1 并行）**

| 任务 | 文件 | 输出 |
|---|---|---|
| 添加 `go-redis` 依赖 | `go.mod` | 依赖 |
| `CacheBackend` 的 Redis 实现 | `internal/pkg/cache/redis.go` | Redis 后端 |
| 将 SSO 令牌缓存迁移到 `CacheBackend` | `internal/sso/token_cache.go` | 通过接口使用缓存 |
| 将 SSO 用户缓存迁移到 `CacheBackend` | `internal/sso/user_cache.go` | 通过接口使用缓存 |
| `Publisher` 的 Redis 实现 | `internal/hub/redis_publisher.go` | Redis Pub/Sub |
| `Lock` 的 Redis 实现 | `internal/pkg/lock/redis.go` | Redis SET NX EX |
| 将速率限制器迁移到 `CacheBackend` | `internal/pkg/ratelimit/ratelimit.go` | Redis 支持的速率限制器 |

**里程碑：M2 — 水平扩展已启用**  
验证：两个及以上实例上的集成测试显示共享缓存状态和 SSE 传递。

**阶段 3：资源级联（2-3 周，在 M1 之后）**

| 任务 | 文件 | 输出 |
|---|---|---|
| 为 `ListByTarget` 添加目标活性检查 | `internal/repository/comment.go` | JOIN 或子查询 |
| 为级联删除定义 `CascadePolicy` | `internal/domain/shared.go` | 枚举类型 |
| 级联策略实现 | `internal/service/comment.go` | 删除时清理 |
| 为级联添加存储库方法 | `internal/repository/` | 级联更新/删除 |

**里程碑：M3 — 级联引用完整性**  
验证：删除文件会删除其注释。删除对话会移除消息。验证通过 `make test-integration`。

**阶段 4：Webhook 增强（可选，2 周）**

| 任务 | 文件 | 输出 |
|---|---|---|
| 将出站事件路由到多个订阅者 | `internal/outbox/chain.go` | 按事件类型路由 |
| 向 webhook 有效负载添加事件类型版本 | `internal/outbox/webhook.go` | 兼容性支持 |
| 死信监控机制 | `internal/outbox/relay.go` | 暴露的死信指标 |

**里程碑：M4 — Webhook 事件驱动集成就绪**

### 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| 双重包结构（特性与旧方案并行）会导致代码重复 | 高 | 中 | 在特性工作开始前，先将 `internal/message/` 拆分为清晰的职责；确保旧方案不用于新特性 |
| 群组对话数据迁移（现有消息到对话） | 中 | 高 | 使用惰性转换（首次查询时创建对话）；提供回滚工具；在 staging 环境中验证 |
| SSE Hub Redis 实现引入消息丢失语义 | 中 | 中 | 在 M2 中使用 Redis Pub/Sub 之前，先在阶段 0 协议中使用 `Publisher` 接口；丢失的消息可以通过从数据库回溯来弥合 |
| 缓存失效复杂性：Redis TTL 与 SSO 提供的到期时间 | 低 | 中 | SSO 的 `expires_at` 已经有一个上限（`TokenCacheTTL`）；在迁移期间同时保持两个缓存；在 2 个部署周期后关闭旧的 |
| 引入 Redis 增加了操作依赖 | 中 | 中 | Redis 在典型的生产部署中是标准配置（轻松可配置为 AWS ElastiCache / Memorystore）；作为配置中的“可选”启动；允许在没有 Redis 的情况下运行回退模式 |
| 方向一中的通知路由更改会影响当前功能 | 低 | 高 | 在 pre-prod 环境中通过 `make test-integration` 验证端到端的通知流；添加集成测试，用于通过 Redis Pub/Sub 与非 Redis 实例进行组通知 |

---

## 关键要点摘要

1. **这里的架构是扎实的。** 单一职责层、出站模式、软删除、领域实体——这些都是决策得当的一个经过深思熟虑的系统。

2. **最紧迫的工作不是任何特性。** 而是在 `internal/pkg/cache/` 中定义分布式原语的接口（`CacheBackend`、`Publisher`、`Lock`）——不需要新的依赖项，并且使所有后续工作都能够轻松地在不更改接口的情况下交换可实现性。

3. **双重包结构有风险，但可以管理。** 只要明确划分了特性代码属于哪个位置（新方案用于新特性，旧方案用于遗留维护），这在实践中是可行的。

4. **群组对话需要数据迁移，但可以按惰性方式完成。** 不必一次性迁移所有内容——在查询时检测对话时即时创建。保持查询代码同时支持 `conversation_id` 和非 `conversation_id` 路径。

5. **SSE Hub 是妨碍水平扩展的最大基础设施瓶颈。** 在正常工作流程中，应优先解决它，然后才是缓存和速率限制。

6. **文件级联（方向二）有一个被低估的问题：** `ListByTarget` 不检查目标实体是否存活。在发布方向一之前，应修复此问题，因为群组对话引入了更多需要通过级联方式清理的目标种类。
