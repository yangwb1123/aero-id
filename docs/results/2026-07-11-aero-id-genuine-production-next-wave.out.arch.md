Now I have a comprehensive understanding of the codebase. Here is my architectural analysis.

---

# 架构分析报告：aero-id

## 1. 架构评估

### 1.1 当前架构的优势

**严格的分层依赖**（handler → service → repository）是该系统最核心的架构资产。这种单向依赖带来了几个关键好处：

- **双协议对等性**：HTTP 和 gRPC 共享同一套 service 层，业务逻辑不会因为传输协议不同而产生分歧。实际代码中 `app.go:245-270` 的 wiring 证实了这一点——`fileSvc` / `msgSvc` 等被同时注入 `NewRouter()` 和 `NewServerWithOptions()`。
- **可测试性**：service 层依赖的都是接口（`repository.FileRepository`、`storage.FileStorage`、`sso.UserDirectory`），可以在单元测试中 mock。`internal/service/mocks` 目录为此存在。
- **错误边界清晰**：repository 返回原始错误，service 层统一转换为 `AppError`（携带 HTTP + gRPC 双码），handler 层只做渲染。这个契约在整个代码库中被一致遵守。

**Outbox 模式** 的实现质量很高。`internal/outbox/` 包提供了：
- 事务性写入（`Append` 在创建领域对象的同一事务中执行）
- 可组合的 handler chain（`dispatcher` → `webhook`）
- 退避重试 + dead letter
- Partial index 优化未发布事件的查询

**Periodic Job 模式** 被多次复用（idempotency purge、storage GC、outbox relay），形成了一个隐式的架构模式。每个 job 暴露 `Start(interval)` → `stop func()` 签名，为未来添加定时任务（如通知清理、审计日志归档）提供了现成的模板。

**文件存储的接口抽象** 使 `local` 和 `s3` 后端可切换，且 `SignedDownloader` 分离了下载流和 presigned URL 两种模式。

### 1.2 局限性

**1. 两层 service 架构的重叠和歧义**

实际代码中存在两套 service 实现：

| 路径 | 风格 | 状态 |
|---|---|---|
| `internal/service/message.go` | 传统 service 层 | **已 wiring**（`app.go:245`） |
| `internal/message/service.go` | 自包含领域模块（service + repo + handler） | **未 wiring**，疑似 WIP |

`internal/message/` 包包含完整的 repository、service、handler（HTTP + gRPC），但 `app.go` 仍在引用 `service.NewMessageService()`。这说明系统处于**架构过渡期**——正在从扁平 service 层向领域模块化演进，但未完成。这带来了维护歧义：新的 message 功能应该加在哪里？测试应该写在哪一层？

这不是 fatal 问题，但如果继续向模块化演进，需要明确迁移策略：要么全部采用 `internal/domain/` 模块（如 `internal/message/`），要么全部留在 `internal/service/`。

**2. gRPC 权限拦截器存在但覆盖不全**

`PermissionInterceptor`（`server.go:223`）的实现很扎实，但其权限映射表 `requiredPermForMethod()` 只覆盖了 3 个 AdminService 方法：

```go
// server.go:249-255
func requiredPermForMethod(fullMethod string) (string, bool) {
    m := map[string]string{
        "/aero_id.v1.AdminService/ListAudit": domain.PermAuditRead,
        "/aero_id.v1.AdminService/GrantRole": domain.PermSystemConfig,
        "/aero_id.v1.AdminService/RevokeRole": domain.PermSystemConfig,
    }
    perm, ok := m[fullMethod]
    return perm, ok
}
```

这意味着当未来添加 `BanUser`、`ModerateComment` 等 gRPC RPC 时，如果开发者忘记在此注册，权限门禁就不会生效。这是一个隐含的风险点。

**相比之下**，HTTP 端的 `RequirePermission()` middleware 在路由注册时显式绑定（如 `admin.Use(middleware.RequirePermission(domain.PermAuditRead, h.permChecker))`），更不容易遗漏。

**3. SSE Hub 缺少连接管理**

`internal/hub/hub.go` 的 `Subscribe()` 对订阅数量不做任何限制。一个恶意用户可以通过 `GET /v1/events` 创建数千个 SSE 连接，每个连接在 hub 中注册一个 channel（buffer=16），导致 server 内存无上限增长。没有 per-uid 并发限制、全局最大连接数、idle 超时机制。

当前的心跳（30s ping）只能防止 proxy 超时断开，不能主动清理僵尸连接。

**4. 搜索缺乏标准化分页**

`SearchService.Search()` 返回 `[]*domain.SearchHit`，没有 cursor 和 next page 的概念。这与系统中所有 List 端点使用的 `domain.Page` + `NextCursor` 模式不一致。搜索是一个「系统内」的关键路径，分页不一致意味着：
- 前端需要两套分页逻辑
- 搜索结果无法深度链接到某一页

**5. local 存储的 presigned upload 不可用**

`PresignUpload()` 在 local 后端返回 `BadRequest`。这意味着开发环境和 CI 无法测试完整的 presigned 上传流程，导致生产与开发行为不一致。Local 后端在开发中使用广泛，这个限制影响 DevX 和 CI 质量保证。

### 1.3 关键设计决策评估

| 决策 | 评价 |
|---|---|
| SSO 外包身份认证 | **正确**。符合微服务单一职责，但 SSO 合约只有 3 个字段（uid/nickname/avatar），限制了 aero-id 的 user-facing 功能 |
| UUIDv7 做主键兼作游标 | **正确**。时序友好，规避了顺序 ID 的安全问题和 UUIDv4 的索引碎片 |
| 两层评论嵌套 | **合理**。在简洁性和功能之间取得了平衡。最常见的场景（楼+楼中楼）得到支持，无限嵌套会带来复杂的权限和显示问题 |
| 软删除 + GC 清理 | **合理**。软删除保障数据可恢复；GC sweep 清理孤立的 blob 和过期 row。但 outbox event 和 notification 没有类似的清理机制 |
| 事务性 outbox + 通知推送分离 | **优秀**。通过 outbox 保证领域事件至少投递一次，通过 Dispatcher 实现通知创建和推送的解耦 |
| 不自动执行 migration | **正确且安全**。生产环境应当通过部署流水线显式执行 migration |

---

## 2. 高价值架构扩展方向

### 方向 A：领域模块化（Domain Module Pattern）

**为什么需要**：
当前存在 `internal/service/`（老的扁平层）和 `internal/message/`（新的领域模块）并存。message 模块的 service/repository/handler 被拆到一个独立的包中，形成了「自包含领域模块」的模式。这是一个结构性提效——将 message 完全模块化后，其他领域（comment、file）也可以跟进，最终消除 `internal/service/` 中臃肿的接口定义文件（`service.go:1-150` 定义了所有 service 接口）。

**核心挑战**：
- **依赖管理**：模块之间如何引用？message 模块需要引用 file 模块做附件校验（`internal/message/service.go:65` 的 `s.files.Get()`）。模块化后，跨模块依赖应该通过接口而非具体类型。
- **事务边界**：跨模块操作（如删除用户时同时清理消息和评论）需要一致的事务边界。当前通过 GORM `*gorm.DB` 传递事务，模块化后需要抽象的 `Transaction` / `UnitOfWork`。
- **过渡策略**：不能一次性重写所有模块，需要平滑迁移。建议采用 Strangler Fig 模式：新功能写在模块化包中，旧 service 层逐步废弃。

**预期架构变更**：
```
internal/
├── message/           # 模块化后：service.go + repository.go + handler*.go
│   ├── service.go     # 仅暴露 Service 接口
│   ├── repository.go  # 仅暴露 Repository 接口
│   └── internal/      # 实现细节
├── comment/
│   └── ...
├── file/
│   └── ...
└── service/           # 迁移完成后废弃
```

**对现有系统的影响**：
- 短期增加维护成本（两套代码并存）
- 长期降低包间耦合，使每个模块可以独立测试和演进
- 每个模块的 handler 可以通过统一的 `ServiceRegistrar` 接口注册（已有 `internal/handler/grpc/server.go:29-33` 的 `ServiceRegistrar` 接口，可以复用）

### 方向 B：统一权限治理（Unified Permission Governance）

**为什么需要**：
当前权限检查存在「三叉戟」局面：
1. HTTP: `middleware.RequirePermission()`（显式路由注册时绑定）
2. gRPC: `PermissionInterceptor` + `requiredPermForMethod()` map（隐式 central registry）
3. Service 层内：部分方法自己检查（如 `checkSendBlock`）

三种方式的维护者可能不一致，容易产生盲区。评价文档的 Blind Area A 指出的「gRPC 权限检查不全」只是症状，根因是**缺少统一的、声明式的权限元数据**。

**核心挑战**：
- 每个 RPC 方法（HTTP + gRPC）都需要声明其所需的最小权限
- gRPC 的 `requiredPermForMethod` 是纯函数映射，容易遗忘
- 需要一种机制确保开发者添加新端点时必须显式声明权限，而非「默认放行」

**建议方案（二选一）**：

**选项 A：proto annotation + 代码生成**
- 在 proto 文件中给每个 RPC 添加自定义 option `(aero_id.v1.required_permission)`
- 用 protoc plugin 生成权限注册表
- 优点：声明式，权限与 API 定义在一起；缺点：需要开发 protoc plugin

**选项 B：注册表模式（Registry Pattern）**
- 创建一个中心化的 `MethodPermissionRegistry`，在 service 初始化时注册
- HTTP router 和 gRPC interceptor 都查询此 registry
- 优点：无代码生成；缺点：显式注册

```go
// 示例接口
type PermissionRegistry interface {
    RequiredPermission(method string) (string, bool)
    Register(method, permission string)
}
```

**对现有系统的影响**：
- 已有基础设施（`PermissionInterceptor`、`RequirePermission`、`middleware.PermissionChecker` 接口）均可复用
- 需要将 HTTP 的 `admin.Use(middleware.RequirePermission(perm, checker))` 模式改为从 registry 读取
- 需要为所有 gRPC 方法添加 registry 注册（而非仅 3 个）

### 方向 C：群组消息（Group Messaging）—— 评估文档方向一

**为什么需要**：
当前消息是严格的 1:1 模型（`from_uid` / `to_uid` 二元组），不支持群聊。这是产品路线图上最显性的 gap——群组消息是即时通讯的 baseline 能力，也是区别于私信的关键差异化。

**核心挑战**：
- **数据结构**：需要引入 `conversation`（或"group"）实体，以及 `conversation_participants` 关联表。message 表增加 `conversation_id`。
- **ACL 变更**：当前 `canReadMessage(actor, m)` 检查 `actor == from_uid || actor == to_uid`。群消息需要检查 actor 是否为 conversation 的 member。
- **通知分发**：群消息需要发送给所有成员（排除发送者自身）。`internal/notification/dispatcher.go` 的 `Handle()` 方法当前只为单个 `recipient_uid` 创建一条 notification。可以改为从 domain event 中取出参与者列表，循环创建——但需要事务保障。
- **SSE 推送**：群消息需要推送到群内所有在线成员的 SSE 连接。当前 hub 是按 `uid` 分发的，架构上支持多接收方，只需在 Dispatcher 中循环 `push()`。

**建议架构变更**：
```
Domain 新增：
- Conversation{ID, Type(dm|group), Name, Avatar, CreatedAt, ...}
- ConversationMember{ConversationID, UID, Role, JoinedAt}

Message 变更：
- 增加 ConversationID（可为空，兼容现有 DM）
- Service: SendToConversation(ctx, convID, content) 方法

Repository 新增：
- conversation_repository.go：CreateMember、ListMembers、IsMember

ACL 变更：
- canReadMessage → 如果是 DM 按现有逻辑，如果是群消息检查 IsMember
```

**对现有系统的影响**：
- 向后兼容：现有 DM 的 `conversation_id` 为 NULL
- `ListConversation(ctx, uidA, uidB)` 需要改为 `ListConversationMessages(ctx, convID)` 或保持兼容签名
- 通知分发逻辑需要扩展（NotificationDispatcher 的 `Handle` 当前只创建一条 notification，需要改为循环）

### 方向 D：数据生命周期治理（Data Lifecycle Governance）

**为什么需要**：
当前系统中多个表无 retention 策略：
- **Outbox events**：`published_at IS NULL AND NOT dead_letter` 的 event 会被 relay 消费，但已发布的 event 和 dead-letter event **永远保留**
- **Notifications**：无清理机制
- **Audit log**：无清理机制
- **Idempotency keys**：已实现 `purgeExpired()`（`internal/idempotency/store.go:146`），这是唯一有 TTL 清理的

GDPR 合规要求「数据最小化原则」——不应无限期保留。此外，大量积累的历史数据会降低查询性能（即使有索引）。

**核心挑战**：
- 删除策略需要精确：通知可能需要保留 90 天，审计日志可能需要保留 1 年（法规要求），outbox 已发布 event 可以 7 天后清理
- 软删除 vs 硬删除的权衡：软删除保留数据可恢复性，但需要二次清理
- 需要避免清理 job 对生产写入产生冲击（batch 大小、sleep interval）

**建议方案**：
复用 `internal/idempotency/store.go` 的 `RunCleaner()` 模式，为每个需要清理的表创建一个独立的 Periodic Job：

```go
// 模式复用
type Cleaner func(ctx context.Context) (int64, error)

func RunPeriodic(ctx context.Context, interval time.Duration, batch int, cleaner Cleaner) func()
```

建议的 retention 策略：
| 表 | 保留期 | 清理策略 |
|---|---|---|
| Outbox events（已发布） | 7 天 | 硬删除 |
| Outbox events（dead letter） | 30 天 | 硬删除（有告警的情况下） |
| Notifications | 90 天 | 硬删除 |
| Audit log | 1 年 | 归档（若有法规要求）或硬删除 |
| Idempotency keys | 当前 TTL（默认 24h） | 已有实现 |

**对现有系统的影响**：
- 无破坏性变更
- 每个 cleaner job 需要独立的 migration（添加 `expires_at` 列或依赖 `created_at`）
- 可以做成 OPT-IN，由配置控制过期时间

### 方向 E：SSO 合约扩展 + 用户状态治理

**为什么需要**：
当前 SSO 只返回 3 个字段（uid/nickname/avatar），且 aero-id 不知道用户是否存在（SSO 删除用户后，`user_roles` 表会残留孤立引用）。这影响多个场景：

1. **Admin 列表展示**：`ListUserRoles` 可能返回已不存在的用户
2. **用户封禁**：`PermUserBan` 已有常量但零实现——因为 aero-id 没有用户状态概念
3. **消息发送**：发送给不存在或已删除的用户会失败——但 aero-id 无从知晓

**核心挑战**：
- SSO 是外部系统，合约变更需要 SSO 团队配合（跨团队依赖）
- 用户状态（active/banned/deleted）应该由 SSO 作为 source of truth 还是 aero-id 本地缓存？
- 如果 SSO 提供 `GetUserStatus(uid)` 端点，aero-id 可以在关键操作前验证

**建议方案**：

**短期（无需 SSO 变更）**：
- aero-id 本地维护一个 `user_status` 表，记录 `uid`、`status`（active/banned/deleted）、`cached_at`
- SSO 调用 `GetUsers()` 时同步缓存用户状态
- `SendMessage()` 前检查 `to_uid` 不存在或被 banned 返回错误

**长期（需要 SSO 变更）**：
- SSO 扩展 proto：`rpc GetUserStatus(uid) returns (UserStatusResponse)` 
- 或者 SSO 推送用户变更事件（webhook 或 outbox 事件）

**对现有系统的影响**：
- `user_roles` 表的外围清理：需要加一个 `fk` 或定期清理 job 删除孤立 `user_roles` 行
- 新 API 端点：`POST /v1/admin/users/:uid/ban` 和 `POST /v1/admin/users/:uid/unban`

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

**原则一：所有跨模块依赖走接口，不走具体类型**

当前 `internal/message/service.go:65` 直接引用 `file.repository`：

```go
type service struct {
    files FileRepository // 接口，正确
    ...
}
```

这是正确的做法。但 `internal/service/message.go` 依赖的是 `repository.FileRepository`，而 `internal/message/service.go` 定义了自己的 `FileRepository` 接口（只声明了 `Get` 方法）。**建议统一**：每个消费方模块定义自己需要的最小接口（Interface Segregation），而非依赖生产方模块暴露的宽接口。

**原则二：Service 接口的返回值应使用专用 DTO，而非 raw domain object**

当前部分 service 方法返回 raw `*domain.File`、`*domain.Message`，调用方（handler）需要自己处理 nil 检查和数据转换。这个问题在 gRPC handler 中尤为明显——每个 handler 都在写 `for i, r := range roles { out[i] = ... }` 的转换循环。

**建议**：Service 返回 DTO 或标准结果类型，统一包含 entity + enrichment data + pagination。

```go
// 现有模式（OK 但不够一致）
type MessageList struct {
    Messages   []*domain.Message
    NextCursor string
    Users      map[string]sso.User
}

// 建议增加统一的 Result 模式
type ListResult[T any] struct {
    Items      []T
    NextCursor string
}

// MessageList → ListResult[*domain.Message] + enrichUsers() 分离
```

### 3.2 需要引入的新抽象层

**1. `OutboxSender` / `EventBus`**

当前 service 层通过 `outbox.Event` 参数传递领域事件（如 `messageRepo.Create(ctx, msg, &evt)`）。这导致 repository 层承担了「写入 outbox」的职责——repository 本应只关心持久化，outbox 写入是基础设施层关切。

**建议**：引入一个轻量级的 `EventBus` 接口：

```go
type EventBus interface {
    Emit(ctx context.Context, event *outbox.Event) error
}
```

service 层在业务操作完成后调用 `bus.Emit()`，而不是将 event 作为 repository 的参数传递。这解耦了业务持久化和事件发布，也便于在测试中 mock。

**2. `TransactionManager`**

当前跨表事务核心隐患——`internal/repository/db.go` 暴露了 raw `*gorm.DB`。service 层需要传递 db 对象来保证事务一致性。

**建议**：引入 `TransactionManager`：

```go
type TransactionManager interface {
    RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}
```

service 层通过 `RunInTx()` 包裹业务逻辑，内部实现 commit/rollback。repository 方法通过 `ctx` 获取事务 db（或使用 `context.WithValue` 传递，类似当前的 `service.WithActor` 模式）。

### 3.3 向后兼容性策略

**API 变更原则**：
1. **Additive only**：已有端点不改变请求/响应格式。新字段使用 optional 或默认值。
2. **gRPC field 使用 proto3 optional**：已有 `aero_id.proto` 文件正确使用了 `optional` 关键字（如 `UpdateFileMetadataRequest` 中的 `optional string name`）。所有新增字段都应该遵循此模式。
3. **列举而非修改**：如果需要弃用某个字段或行为，使用 `Deprecated` 注解后至少保留一个发布周期再移除。

**数据模型变更**：
- 添加 `conversation_id` 到 message 表：允许 NULL，现有 DM 的该字段为 NULL
- 添加 `user_status` 表：不影响现有查询
- 添加 `expires_at` 列：NOT NULL DEFAULT 'infinity' 或 NULL

---

## 4. 技术选型建议

### 4.1 当前栈评估

| 依赖 | 评估 | 建议 |
|---|---|---|
| `gin` | 成熟，社区活跃，适合 REST API | 保留 |
| `gorm` | 当前使用正确（基本 CRUD + scopes），但大型系统容易产生性能问题 | 保留，但建议对高频查询使用 raw SQL 或 sqlx |
| `grpc` | 架构决策正确（内部服务间通信） | 保留 |
| `viper` | 成熟的配置库 | 保留 |
| `zap` | 高性能结构化日志 | 保留 |
| `uuid` (v7) | 适合主键 + 游标 | 保留 |
| `migrate` | 轻量级迁移工具 | 保留 |

### 4.2 可能需要引入的技术

**1. OpenTelemetry（已有但 OPT-IN）**

`internal/pkg/tracing` 已经实现了 OTel tracing，但默认关闭。**建议生产环境启用**，至少启用 `tracing.sample_ratio: 0.1`（10% 采样），以便在排障时能追踪请求链路。这对群组消息（跨服务调用）和 webhook 投递尤其有用。

**2. 无新框架建议**

当前技术栈是**够用且合适的**。以下常见的引入建议在这里**不推荐**：

| 技术 | 不推荐理由 |
|---|---|
| Redis | 当前只有进程内缓存（TTL 60s token cache + 300s user cache），流量级别不需要分布式缓存。引入 Redis 会增加运维复杂度 |
| Message Queue（RabbitMQ/Kafka） | Outbox 模式已经提供至少一次投递语义。除非吞吐量超过 outbox relay 的处理能力（PollInterval=5s），否则不需要 |
| GraphQL | 当前只有简单的 CRUD + List 端点，REST 足够。GraphQL 会增加 handler 复杂性 |
| AsyncAPI / event schema registry | Outbox 事件只有 3 种类型（message.sent/message.read/comment.replied），当前规模不需要 |

### 4.3 自建 vs 采购的决策框架

当前项目中的所有组件都是自建的。对于评估文档提到的未来方向：

| 场景 | 建议 | 理由 |
|---|---|---|
| 全文本搜索 | **自建**（已有 PostgreSQL FTS + ILIKE fallback） | PG FTS 对小规模（百万级记录）足够。引入 Elasticsearch 会增加运维复杂度，业务价值不明确 |
| CDN + 全球分发 | **采购**（CloudFront/Cloudflare R2） | CDN 是基础设施非差异化能力，自建收益低。`FileStorage` 接口已经抽象好，S3 兼容的 CDN 只需新实现 |
| 邮件/推送通知 | **采购**（SendGrid/Firebase/APNs） | 自建邮件服务器或推送网关需要处理复杂的投递和退订逻辑，属于非核心能力 |
| 用户封禁/合规 | **自建** | 这是业务逻辑，属于 aero-id 的核心权限域，无法外包 |

---

## 5. 实施路线图

### 5.1 优先级排序

基于评价文档的更新矩阵，结合架构分析，我调整的优先级如下：

| 优先级 | 项目 | 理由 | 预估工时 |
|---|---|---|---|
| **P0** | Blind Area A: gRPC 权限拦截器补全 | 安全漏洞，HTTP 与 gRPC 保护不对等 | 1 天 |
| **P0** | Blind Area B: SSE 连接限流 | 生产稳定性风险，可能 OOM | 1-2 天 |
| **P0** | 方向三剩余: Moderation Queue + UserBan | `PermContentModerate`/`PermUserBan` 已定义但零使用 | 3-5 天 |
| **P1** | 方向一: 群组消息 | 核心能力 gap，产品价值最高 | 2-3 周 |
| **P1** | 方向四: 数据生命周期（outbox/notification/audit retention） | GDPR 合规 + 控制表膨胀 | 1 周 |
| **P1** | Direction A: 领域模块化（一致性收敛） | 消除两个 service 实现共存的技术债务 | 2 周（分阶段） |
| **P1** | Direction E: 用户状态治理 + SSO 合同扩展 | 安全 + 合规 | 2 周联合 SSO 团队 |
| **P2** | Direction B: 统一权限治理 | 提升工程质量，但已有手动方案兜底 | 1 周 |
| **P2** | Blind Area C: 搜索分页 | 体验优化，非阻塞性 | 1-2 天 |
| **P2** | Blind Area D: 批量操作 | 体验优化 | 3-5 天 |
| **P3** | Blind Area E: Local presigned upload | DevX 一致性 | 1 天 |
| **P3** | 方向五: 存储层进化（CDN 抽象） | 依赖基础设施，业务紧迫性低 | 2 周 |

### 5.2 阶段划分

**阶段 1：安全与稳定（1-2 周）**

```
目标：修复已知的安全漏洞和稳定性风险
产出：
- gRPC PermissionInterceptor 完整覆盖所有 Admin 方法
- SSE Hub 添加全局 MaxConns + per-uid MaxConns + idle timeout
- /v1/admin/comments/:id/hide（全局隐藏）
- /v1/admin/users/:uid/ban + /v1/admin/users/:uid/unban
- user_roles 孤立引用清理 job
里程碑：新增 P0 功能可用，gRPC 和 HTTP 权限保护对等
```

**阶段 2：核心能力扩展（3-4 周）**

```
目标：填补产品核心能力 gap
产出：
- 群组消息：Conversation + ConversationMember 模型、SendToConversation API、
  群消息通知分发、群消息 SSE 推送
- 数据生命周期：outbox event 清理、notification 清理、audit log 清理
- domain 模块化：选定 message 作为第一个完成模块化迁移的领域
里程碑：群组消息 beta 可用，数据 retention 策略生效
```

**阶段 3：治理与一致化（2-3 周）**

```
目标：提升系统一致性和工程质量
产出：
- 统一权限注册表（PermissionRegistry）
- 模块化迁移完成（comment、file 跟进）
- 搜索分页标准化
- 批量操作端点（P2 子集）
里程碑：两个 service 实现合并完成，搜索与列表分页模式一致
```

**阶段 4：深度演进（持续）**

```
目标：长期架构进化
产出：
- SSO 合约扩展（跨团队）
- CDN 抽象层
- 存储层版本管理
- EventBus 解耦
```

### 5.3 风险点与缓解策略

| 风险 | 影响 | 可能性 | 缓解策略 |
|---|---|---|---|
| **领域模块化期间代码歧义** | 开发者不知道该在 `internal/service/` 还是领域模块中添加代码 | 高 | 1. 明确使用单一路径的决策（建议：新功能走新模块，旧功能迁出前保持不变）2. 在 README 或 CONTRIBUTING.md 中写清楚过渡策略 3. 添加 `make lint` 规则防止同一个领域出现在两个包中 |
| **SSO 合约变更被阻塞** | 跨团队依赖导致方向二停滞 | 中 | 1. 不阻塞其他方向 2. 短期在 aero-id 本地维护用户状态缓存 3. 准备 proto 变更提案供 SSO 团队 review |
| **群组消息导致消息表查询退化** | 现有 `ListConversation(uidA, uidB)` 如果不再使用新索引，可能全表扫描 | 中 | 1. 新加 `conversation_id` 索引 2. 现有 DM 保持原查询路径 3. Benchmark before/after |
| **数据清理误删** | retention job 误删了尚未消费的数据 | 低 | 1. 所有清理 job OPT-IN（当前已有 storage GC 的前例）2. 默认保留期保守（如 outbox: 30 天）3. 先 `SELECT COUNT(*)` 记录行数到 log 再 DELETE |
| **模块化过程中过度抽象** | 为每一个依赖都定义接口，导致接口通胀 | 中 | 1. 遵循 Interface Segregation Principle：消费方定义自己需要的最小接口 2. 除非有多个实现，否则不需要接口 3. Code review 准则：一个接口只有一个或两个实现时，质疑其必要性 |

---

## 总结

**架构的健康程度**：当前 aero-id 的架构质量中上。严格的分层、清晰的职责边界、outbox 模式的正确实现、统一的错误处理——这些都是坚实的架构基础。最大的系统性风险不是具体的设计错误，而是**过渡期的一致性**（两个 service 实现并存、gRPC 权限覆盖不全、SSE 无连接管理）。

**最优先的行动**：
1. **修复 gRPC 权限安全缺口**（1 天，P0）——基础设施已就位，只需补全 `requiredPermForMethod` 映射和添加缺失的 PermContentModerate/PermUserBan 端点
2. **SSE Hub 添加连接上限**（1 天，P0）——这是最简单的生产事故预防措施
3. **消弭领域模块化歧义**（决策 2 天 + 执行 2 周，P1）——决定是继续模块化还是回退，然后统一

这三个行动都不需要引入新技术栈，完全在现有架构能力范围内，但能消除最紧迫的安全、稳定和工程效率风险。
