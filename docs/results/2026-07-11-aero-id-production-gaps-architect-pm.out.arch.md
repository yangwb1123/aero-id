现在我有足够的背景知识来撰写全面的架构分析。以下是完整的分析：

---

# aero-id 架构分析

## 1. 架构评估

### 当前架构的优势

aero-id 的架构在分层上非常清晰，严格遵循 `handler → service → repository` 的单向依赖规则。这是整个代码库中最强的架构决策之一，值得作为技术负债的**基线**：

- **共享业务逻辑**：HTTP 和 gRPC 两个 transport 共享同一套 service 层。从代码中可以看到，`app.go` 只创建一次 `fileSvc`/`msgSvc`/`commentSvc` 等实例，然后同时注入 HTTP router 和 gRPC server。这是经典的"双端口适配器"模式，意味着两个协议的行为完全一致。

- **领域实体与持久化分离**：`internal/domain/` 包定义了纯 Go 结构体（如 `Comment`、`Message`），不包含 GORM tag。Repo 层在 `internal/repository/` 中做 GORM 映射。这意味着领域模型不依赖 ORM，可以被业务逻辑和测试直接使用。

- **依赖注入清晰**：`app.go` 是唯一的组合根（composition root）。所有依赖关系在启动时一次性构建，然后通过构造函数注入。没有使用依赖注入框架，避免了框架魔法。

- **事务性 Outbox**：`outbox` 包的实现将领域事件写入与业务操作放在同一个数据库事务中，然后由 `Relay` 异步 drain。这是事件驱动架构的基石，实现得干净且恰当。

- **SSO 客户端弹性的精心设计**：`sso.Client` 实现了 singleflight、断路器、重试+退避、stale-cache 回退。这是代码库中设计最周密的包之一，体现了对关键外部依赖的防御性思维。

### 关键设计局限

1. **SSE 推送的可靠性缺口**：`Hub.Subscribe()` 的 channel buffer 为 16（`internal/hub/hub.go:67`），`pub()` 使用 `select default` 静默丢弃。这意味着慢消费者必然丢消息。这不是设计取舍——这是 bug。对于通知系统，丢消息意味着用户永远看不到某些通知。

2. **通知 replay 的硬编码限制**：`internal/handler/http/events.go:103` 中 `replayMissed` 硬编码了 `limit: 50`。用户在离线期间产生 51+ 条通知时，第 1 条（最旧的那条）被静默丢弃。这是 cursor-based replay 的经典 off-by-one 问题——用户重新连接时看不到那条通知。

3. **配置系统是启动时快照**：`config.Load()` 在启动时读取一次配置，之后整个结构体被冻结。`ratelimit.Limiter` 不支持动态调整 RPS/burst，`zap.Logger` 不支持运行时更改日志级别。虽然当前没有强烈的热重载需求，但这是一个架构上的"窗户"—一旦将来需要，改动面会很大。

4. **SSO 端点绑定到进程生命周期**：`sso.NewClient` 在启动时通过 `grpc.NewClient` 建立连接（虽然是 lazy dial），但 endpoint 地址在初始化后不可更改。对于蓝绿部署或 SSO 迁移场景，需要重启整个 aero-id 进程。

5. **多态资源引用的完整性缺口**：Comments 通过 `target_type`/`target_id` 多态关联到 files 或 messages。`CommentRepository.ListByTarget` 不做 JOIN 检查目标是否存在。虽然软删除场景下旧数据不会立即导致问题，但这是一个经典的"引用完整性由应用层保证"的脆弱点。

### 架构债务

- **死代码**：`internal/notification/` 包不再被任何 `.go` 文件 import。它的 `dispatcher.go` 和 `events.go` 是孤立的。`NotificationDispatcher` 已经迁移到 `internal/service/notification.go`，但旧包未被清理。

- **遗漏的索引**：`outbox_events` 表缺少 `dead_letter = TRUE` 的部分索引，死信查询会全表扫描。`notifications` 表缺少 `(recipient_uid, read_at IS NULL, created_at DESC)` 复合索引来高效过滤未读通知。

- **测试缺口**：middleware 层（auth、ratelimit、metrics、recovery、cors、request_id）完全无单元测试。gRPC handler 也无独立单元测试。

---

## 2. 扩展方向

### 方向一：公共匿名只读访问（P1）

#### 为什么需要

当前所有 `/v1/*` 路由必须携带 Bearer token 进行认证。只有 `/files/dl`（HMAC 签名验证）和 `/healthz`/`/readyz` 是匿名可访问的。产品的社交传播功能（分享文件链接给未登录用户）完全被阻塞。

基类已经存在：`internal/storage/local.go` 中的 `downloader.VerifySignature` 就是一个基于 HMAC + nonce + 过期时间的匿名凭证验证机制。问题在于这个模式仅限于文件下载，没有被推广到其他资源（消息、评论）的只读访问。

#### 核心挑战与技术难点

1. **定义"匿名访问凭证"的通用格式**：需要一种机制，让拥有者能为特定资源生成一个短期的、不可伪造的访问令牌。`downloader` 的签名验证模式可以被抽象化为一个通用的 `AnonymousToken` 类型。

2. **匿名访问的速率限制**：匿名请求没有 `uid` 可用来进行 per-actor 限流，目前 `ratelimit.Limiter` 键的 fallback 是客户端 IP。但 IP 在 NAT 环境下可能数千人共享，需要引入 per-token 限流或更严格的匿名请求配额。

3. **访问控制模型的扩展**：当前权限模型是 `owner_uid`（拥有者）+ `share_entries`（显式分享）+ `public`（全局公开）。匿名访问引入一个新的层级："持有有效 token 的任何人都可以读取"。需要确保 token 的生存期、资源路径和权限完全绑定。

#### 预期的架构变更

```
internal/
├── token/                ← 新包
│   ├── anonymous.go      -- 匿名凭证生成/验证
│   └── anonymous_test.go
├── handler/http/
│   └── public.go         ← 新文件，/public/v1/* 路由
└── middleware/
    └── anon_auth.go      ← 新中间件，解析匿名 token → 填充 actor
```

- `token.AnonymousToken` 类型：包含资源 ID、操作类型（read-only）、过期时间、nonce。签名密钥复用现有的 `SIGNING_KEY`。
- `middleware.AnonymousAuth`：检查请求中的 `?token=` 查询参数，验证签名，如果有效则在上下文中设置一个有权限但有局限的 `Actor`（例如 `{uid: "", permissions: {read: [resource_id]}}`）。
- HTTP handler 中的 `PublicRoutes`：在现有 auth 中间件之前注册，是 `r.Auth` 的替代路由组。

#### 对现有系统的影响

- 影响有限。新增的路由是 `GET /public/v1/files/{id}`、`GET /public/v1/comments?target_type=&target_id=&token=`，完全独立于现有的 `/v1/*` 路由。
- `service` 层需要新增一个方法签名，例如 `FileService.GetPublic(ctx, fileID, token)`，该方法验证 token 有效且涵盖该资源，然后返回数据。
- 不改变现有认证流程。

#### 选项与权衡

| 选项 | 优点 | 缺点 |
|------|------|------|
| **选项 A：通用匿名凭证（推荐）** | 统一所有资源的匿名访问；复用 HMAC 基础设施 | 需要设计 token 格式；增加中间件复杂度 |
| **选项 B：基于 URL secret 的访问** | 简单，无 token 解析，类似 Google Drive 的"知道链接的人可访问" | 无法限制访问次数；URL 泄露后不可撤销（除非修改签名密钥） |
| **选项 C：自建短期的 API key** | 可撤销、可审计 | 需要持久化 key；管理开销大 |

**推荐选项 A**，因为代码中已经存在 HMAC 签名验证的基础设施，将其泛化成本最低。

---

### 方向二：通知推送可靠性修复（P1，优先级提升）

#### 为什么需要

这是代码中已验证的 bug，而不是设计取舍：

1. `Hub.Subscribe()` 的 channel buffer = 16。一个客户端消费速度跟不上时，`pub()` 的 `select default` 分支会静默丢弃事件。

2. `replayMissed` 硬编码 `limit: 50`。用户在离线期间收到 51+ 条通知时，第 1 条（最旧的那条）永远丢失（因为 `ListSince` 返回最新的 50 条，但 cursor 是从最后接收的 `id` 开始，而第 1 条不在返回集中）。

3. `NotificationDispatcher.Handle()` 末尾的 SSE push 是 fire-and-forget 的，不检查 `pusher.Publish()` 是否成功。如果 hub 关闭或 Publish 失败，通知已经写入 DB 但客户端永远不会收到实时推送。

#### 核心挑战与技术难点

1. **背压管理**：需要从"静默丢弃"模式改为"指数退避 + 有界阻塞"或"客户端可见的降级"模式。没有简单的方案——如果块住 producer，会反过来阻塞 outbox relay 的 drain 循环。

2. **Replay 的可靠性**：`ListSince` 需要支持强制性的 > 0 limit 下限和分页。如果一次返回不完，需要告知客户端还有更多（通过 `Link` header 或分页 cursor）。

3. **推送确认**：当前没有 SSE 级别的确认反馈。客户端收到事件后没有 ack 返回给服务器。可以考虑 Last-Event-ID 作为隐式确认，但服务器需要知道客户端已经处理了哪些事件。

#### 预期的架构变更

```
internal/hub/
├── hub.go               -- 增强 pub()：增加 backpressure 模式选项
├── hub_test.go
└── subscriber.go        -- 新增：跟踪每个 subscriber 的 last_seen_id
```

- `hub.Hub` 增加 `PublishWithBackpressure` 方法（或作为可选配置）：当 channel 满时，不是 drop，而是使用 context deadline 或简短阻塞。
- `EventsHandler.replayMissed` 修复：使用分页循环，直到追赶完所有遗漏通知，或者返回 `Link: <...>; rel="next"` 指示客户端继续。
- `NotificationDispatcher` 的 SSE push 改为记录错误日志并暴露 metrics 计数器，以便运维人员可以看到推送丢弃率。

#### 对现有系统的影响

- 背压功能可以向后兼容配置（默认仍是 drop 模式）。
- `replayMissed` 的分页修复不会破坏现有 API，因为它更改的是内部循环逻辑。
- 整体改动量估计小到中等。

---

### 方向三：Outbox 死信管理（P1，优先级提升）

#### 为什么需要

当前 `outbox` 系统的死信功能完全是透明的：事件超过 `maxRetries` 后标记为 `dead_letter = TRUE`，relay 停止 pickup。但没有任何机制让**操作员知道有死信产生**，也没有工具查看死信、重试死信或进行 forensics。可以通过以下 SQL 验证死信确实存在且不可见：

```sql
-- outbox_events 表中可能有 dead_letter = TRUE 的行，但当前无查询暴露它们
SELECT COUNT(*) FROM outbox_events WHERE dead_letter = TRUE;
```

此外，缺少 `dead_letter = TRUE` 的索引，`SELECT * FROM outbox_events WHERE dead_letter = TRUE ORDER BY created_at DESC` 会全表扫描。

#### 核心挑战与技术难点

1. **死信的可观测性**：需要在 `/metrics` 中暴露 dead_letter_count gauge；需要一个管理端点（HTTP gRPC）列出/过滤死信。

2. **死信重试与清理**：操作员需要能够手动重试一个或多个死信事件；需要一个 TTL-based 自动清理策略，防止死信无限积累。

3. **死信告警**：当死信数量在时间窗口内急剧增长时，需要发出告警信号（可能是 webhook 投递、prometheus alert rule、或日志）。

#### 预期的架构变更

```
internal/outbox/
├── relay.go             -- 增加 dead_letter_purge goroutine
├── repository.go        -- 增加 ListDeadLetters, RetryDeadLetter, PurgeDeadLetters
├── admin.go             -- 新增管理端点
└── metrics.go           -- 暴露 dead_letter_count gauge
```

- `Repository` 新增：`ListDeadLetters(ctx, page) → []*Event`、`RetryDeadLetter(ctx, id, bool clearRetries)`、`PurgeDeadLetters(ctx, olderThan time.Duration) int`。
- 添加索引 `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_outbox_dead_letter ON outbox_events(created_at DESC) WHERE dead_letter = TRUE`。
- 暴露操作端点：`GET /v1/admin/outbox/dead-letters`（列出死信）、`POST /v1/admin/outbox/dead-letters/:id/retry`（重试单个死信）、`POST /v1/admin/outbox/dead-letters/retry-all`（重试所有死信）。

#### 对现有系统的影响

- 新增的管理端点需要通过现有 `AdminService` 的权限检查（已存在的 `admin_roles` 表）。
- 重试逻辑需要小心：死信可能是因为 payload 格式变化导致的（比如上游 SSO 改了 gRPC 接口），重试可能再次失败。需要设计"重启后重试"策略，而不是无限制重试。

---

### 方向四：配置热重载（P2，优先级降级）

#### 为什么需要

当前配置在启动时一次性读取并冻结。操作员更改 `configs/config.yaml` 后需要重启进程。这对以下场景有影响：

- 日志级别在生产中需要临时调高以调试问题（zap 支持 `SetLevel`，但当前包装不暴露）。
- 速率限流需要上调以处理突发流量（`ratelimit.Limiter` 不支持 `SetRate`）。
- 最重要的：SSO 端点切换（蓝绿部署、数据中心迁移）当前意味着停机。

#### 核心挑战与技术难点

1. **原子性**：配置更改不是原子操作。如果同时更改了日志级别和速率限流，但只成功了其中一个，系统处于不一致状态。

2. **SSO 端点的 gRPC 连接重建**：`grpc.NewClient` 创建的连接是 lazy 的，但 endpoint 是固定的。更改端点需要关闭旧连接并创建新连接，期间正在进行的请求不能受影响。这需要优雅的 draining 策略。

3. **限流器的动态调整**：`ratelimit.Limiter` 当前在创建时固定了 rate/burst。如果要支持运行时调整，`Allow()` 路径中的 `l.rate` 和 `l.burst` 需要线程安全地读取（当前是用 mutex 保护的，所以改为 `atomic.LoadFloat64` + `atomic.StoreFloat64`）。

#### 预期的架构变更

```
internal/config/
├── config.go            -- 扩展支持 watcher（fsnotify）
├── watcher.go           -- 新增：监视文件变更并触发回调
└── reloader.go          -- 新增：管理可重载配置项

internal/app/
└── reload.go            -- 新增：注册可重载配置回调
```

- 引入 `config.Reloader` 接口：`Reload(newCfg *Config) error`。
- `app.New()` 中注册每个关心的组件的 Reload 回调。当 `fsnotify` 触发变更时，`watcher` 重新加载配置文件，版本对比后逐个调用注册的 Reloader。
- `ratelimit.Limiter` 增加 `SetRate(rps float64, burst int)` 方法（线程安全）。
- `sso.Client` 需要在 SSO 端点变更时支持 graceful reconnection（可选项：先建新连接，再关闭旧连接）。

#### 对现有系统的影响

- 较大的改动面：`ratelimit`、`sso`、`logger` 都需要修改以支持运行时重新配置。
- 测试复杂度增加：需要验证并发读写安全性、原子性承诺。
- 优先级降级的理由：当前没有运维层面的信号表明配置变更频率高到需要热重载。

---

### 方向五：跨资源引用完整性（P2）

#### 为什么需要

多态资源引用是当前数据完整性的最大缺口：

- `comments.target_type` 和 `comments.target_id` 引用 files 或 messages，但 `CommentRepository.ListByTarget` 不做 JOIN 检查目标是否存在。如果目标文件被软删除，comment 仍然存在并返回。

- `message_reactions` 对已软删除的消息仍然存在——虽然 `ReactToMessage` 由于 GORM 的 `deleted_at IS NULL` scope 会返回 `ErrNotFound`（无法对已删除消息创建新 reaction），但数据库中已有的 reaction 行没有被级联删除。

- 没有外键约束来保证引用的完整性。软删除模式是原因——PostgreSQL 的 FK 不允许引用可能被软删除的行。

#### 核心挑战与技术难点

1. **软删除与参照完整性之间的固有矛盾**：在软删除模式下，目标行被标记为 `deleted_at IS NOT NULL` 而非物理删除。FK 无法避免引用已软删除的行。需要在应用层实现"逻辑外键约束"。

2. **清理孤儿数据的策略**：有几种选择：
   - **级联软删除**（推荐）：当文件/消息被软删除时，同时软删除其下的 comments、reactions（在 service 层的事务中完成）。
   - **延迟清理**（低成本）：定期 job 扫描孤儿数据并软删除。
   - **查询时过滤**（当前）：不做任何事，查询时由应用决定是否过滤已删除目标下的关联数据。

3. **性能影响**：级联软删除意味着一次文件删除操作可能关联 N 条 comment 和 M 条 reaction，需要在一个大事务中完成。如果文件有 1000+ 条 comment，事务可能很大。

#### 预期的架构变更

```
internal/service/
├── file.go              -- 增强 DeleteFile 以级联软删除 comments + reactions
├── message.go           -- 同上
└── cascade.go           -- 新增：级联删除逻辑提取为共享辅助函数
```

- 在服务层添加 `cascadeDelete` 方法：软删除目标 → 收集所有关联 comment IDs → 批量软删除 comments → 批量软删除 reactions/comment_likes。
- 在 repository 层添加 `CascadeSoftDelete` 方法，接收目标类型和 ID，返回受影响的行数。
- 需要确保这个级联操作与 outbox 事件写入在同一个数据库事务中（如果启用了 outbox webhook 通知外部系统）。

#### 对现有系统的影响

- 改动面中等：需要修改删除路径的 service 方法，但不会影响读取路径或创建路径。
- 级联操作增加单次删除的延迟。对于大量关联数据的场景，可以考虑异步级联（通过 outbox 事件触发）。

---

## 3. 接口设计建议

### 关键模块的接口设计原则

当前代码库在接口设计上有不同的成熟度等级：

**好的例子**：
- `storage.FileStorage` 接口——清晰，只有必要的读写方法，`local` 和 `s3` 两个实现很好。
- `sso.Client`——定义了 `ValidateToken`、`GetUsers`、`HealthCheck`，职责单一且对称。
- `outbox.Repository`——`Append`、`FetchUnpublished`、`MarkPublished`、`MarkFailed`，是典型的 outbox 模式接口。

**改进空间**：
- `repository.NotificationRepository`——在 `internal/repository/notification.go` 中作为具体类型而非接口声明。服务层之接收 `repository.NotificationRepository`（具体类型），导致 service 测试必须创建真实数据库连接。这是问题最严重的例子。
- `service.NotificationDispatcher`——构造时接收 `repository.NotificationRepository`（具体类型）作为第一个参数，虽然通过 `DispatcherOption` 模式提供了插件化的中间件，但核心依赖仍然是具体类型。

### 推荐引入的抽象层

1. **所有 repository 接口应该正确定义在 service 层**（或一个独立的 `ports` 包）。目前的部分 repository 是在 `internal/repository/` 包中作为具体类型声明的。Service 层构造函数应该接收接口而非具体类型。当前的 `service.NewFileService(fileRepo)` 中 `fileRepo` 是 `*repository.FileRepository`。这使得 service 层的单元测试必须处理真实数据库。

2. **引入 `ports/` 包**（或使用已有的 `internal/domain/`）来存放 service 层需要的所有接口。这遵循了端口-适配器模式，确保 service 层只依赖抽象。

3. **`NotificationPusher` 接口已存在但未正确定义位置**。它在 `internal/service/notification.go` 中定义：
   ```go
   type NotificationPusher interface {
       Publish(uid, eventType, data string, eventID int64)
   }
   ```
   这是一个好的抽象，因为 service 层不依赖 `hub.Hub`。但 `Hub` 的实际方法签名不同（`hub.Pusher` 是 `Publish(uid, eventType, data string)`）。建议将它们统一为一个包（例如 `internal/service/ports.go`）中的接口族。

### 向后兼容性

- **gRPC proto 变更**：需要遵循 protobuf 向后兼容的规则。不使用 `reserved` 字段编号。如果需要更改语义，新增字段而不是修改现有字段。
- **HTTP API**：使用新路由前缀（如 `/v2`）或可选查询参数进行扩展，而不是破坏现有路由的形状。
- **配置结构**：使用 viper 的 `AutomaticEnv` + 结构体 tag 进行扩展。新增配置项应该设置合理的默认值，保持向后兼容。

---

## 4. 技术选型

### 是否需要引入新技术栈

| 候选技术 | 是否需要 | 理由 |
|----------|---------|------|
| **消息队列（RabbitMQ / Kafka / NSQ）** | 当前不需要 | outbox 模式加 Go in-process relay 已经满足当前需求。outbox 表在 Postgres 中，死信和重试都已经实现。消息队列不会解决通知推送可靠性问题。 |
| **Redis** | 当前不需要 | 所有缓存需求（token 缓存、user 缓存）都在 SSO client 内部以 TTL 内存缓存处理。分布式锁或多进程速率限制才需要 Redis。如果将来需要跨进程协调（如速率限制、出站信号的投递追踪），则 Redis 是一个自然的选择。 |
| **WebSocket** | 不需要 | SSE 已满足实时通知推送的需求，而且实现要简单得多。SSE 自动重连 + `Last-Event-ID` 天然支持离线消息恢复。 |
| **Elasticsearch** | 不需要 | 当前搜索需求由 PostgreSQL 的 `tsvector` + GIN index 满足。需要外部搜索引擎的场景（多语言分析、更复杂的相关性排序）在可预见的未来不会出现。 |

**结论**：不应引入新的数据基础设施。当前的技术栈（PostgreSQL、Go stdlib、gRPC）在可预见的未来是足够的。

### 第三方依赖的评估标准

代码库当前的依赖选择是合理的。以下是评估任何新增依赖的框架：

1. **License 合规性**：必须与项目 license（MIT）兼容，不能引入 AGPL/SSPL 级联 license。
2. **不引入新的运行时依赖**：测试工具（如 `testcontainers-go`）是可接受的，但运行时增加依赖需要严格评估。
3. **Go stdlib 优先**：只要 Go 标准库提供了所需功能（`crypto/hmac`、`crypto/sha256`、`sync` 等），就不需要引入外部包。
4. **测试基础设施**：`testcontainers-go` 已经满足集成测试的需要，不需要引入 mock 框架（Go 接口 + 手写 mock 已经足够）。

### 自建 vs 采购的决策

当前架构中的所有组件都是自建的。以下场景可能需要改变：

- **SSO 服务的所有功能**：自建是唯一的选项（SSO 是一个外部服务的客户端，proto 定义了接口，没有商业化替代品）。
- **通知推送**：如果需要短信、邮件、Web Push（Service Worker）等通道，可以考虑第三方服务（SendGrid、Firebase Cloud Messaging），但 aero-id 的职责只限于 SSE 推送，所以当前自建是合适的。
- **文件存储**：已经通过 `FileStorage` 接口抽象化，`local` 和 `s3` 都是实现。如果需要 CDN，可以在 `FileStorage` 接口之上添加缓存层。

---

## 5. 实施路线图

### 优先级排序

| 排名 | 方向 | 优先级 | 估计工作量 | 核心理由 |
|------|------|--------|-----------|---------|
| 1 | 通知推送可靠性修复 | **P1** | 小（~1-2天） | 已验证的 bug（buffer=16 丢消息 + limit=50 丢 replay），直接影响用户信任 |
| 2 | 公共匿名只读访问 | **P1** | 中（~3-5天） | 直接解锁社交分享能力，产品用户增长的最短路径 |
| 3 | Outbox 死信管理 | **P1** | 小（~1-2天） | 死信对操作员完全不可见，影响数据完整性；工作量小，快速实现 |
| 4 | 配置热重载 | **P2** | 中（~3-4天） | 运维便利性改进，当前无信号表明生产需要；可以分阶段实现（先日志级别 + 限流） |
| 5 | 跨资源引用完整性 | **P2** | 中-大（~3-5天） | 长期数据正确性问题，短期内不会导致功能错误 |

### 阶段划分

#### 阶段 1：修复已知 bug（第 1 周）

目标：修复通知推送丢消息 bug + 死信可视性。

1. Hub channel buffer 从 16 增大到 128，并且在 `pub()` 中不做静默丢弃，而是记录日志并暴露 metrics（PubDroppedCounter）。
2. `replayMissed` 从硬编码 `50` 改为 `min(cfg.MaxReplayLimit, 50)` 且支持分页（通过 SSE `Link` header）。
3. Outbox 死信索引 + 创建管理端点。
4. 清理死代码：移除 `internal/notification/` 包。

**交付物**：
- 通知可靠性修复的集成测试（模拟慢消费者 + 离线恢复场景）
- 死信管理终端 + Prometheus gauge
- 代码清理

**风险**：
- 业务影响小，因为修复的是已知 bug
- 低风险

#### 阶段 2：公共匿名只读访问（第 2-3 周）

目标：设计并实现匿名凭证机制，然后为文件添加公开发送端点。

1. 设计 `token.AnonymousToken` 结构、签名/验证方法。
2. 实现 `middleware.AnonymousAuth` 中间件。
3. 添加 `GET /public/v1/files/{id}` 端点（HTTP 和 gRPC）。
4. 为 file owner 添加生成匿名访问 URL 的端点（`POST /v1/files/:id/public-url`）。
5. 后端添加 per-token 的速率限制（使用内存的 token bucket，key 为 token hash）。

**交付物**：
- 匿名凭证生成/验证的单元测试
- `GET /public/v1/files/{id}` 端点
- 为文件共享 UI 提供的生成公开 URL 的端点

**风险**：
- 如果签名密钥泄露，所有匿名凭证可以被伪造。**缓解**：支持密钥轮换，且匿名 token 中嵌入 `key_id` 字段。
- 公开文件的带宽成本。**缓解**：速率限制 + 日志审计。

#### 阶段 3：配置热重载 + 跨资源引用完整性（第 4-5 周）

目标：实现运行时配置重载的框架，然后为级联软删除打下基础。

1. `config.Reloader` 接口 + `watcher` goroutine（fsnotify）。
2. `ratelimit.Limiter.SetRate()` + `sso.Client.SetEndpoint()`。
3. 级联软删除：先实现文件→comments 的级联（因为这是用户可见的路径）。
4. 级联软删除：消息→reactions。

**交付物**：
- `POST /v1/admin/reload` 端点+信号触发
- 级联软删除的事务性实现+Git 测试

**风险**：
- gRPC 端点切换需要优雅的连接 draining，代码改动大。**建议**：先支持日志级别和限流的运行时调整，SSO 端点重连作为单独的工作项。
- 级联操作可能很慢。**缓解**：使用批量 `UPDATE ... WHERE` 而不是逐行操作。

### 整体风险矩阵

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| 匿名 token 泄露导致数据泄露 | 低 | 高 | 短 TTL token；支持密钥轮换；速率限制 |
| 级联软删除事务过大导致超时 | 中 | 中 | 批量处理+超时配置；后台异步级联选项 |
| 配置热重载与旧客户端不兼容 | 低 | 低 | 先新增配置项，不改现有结构体字段 |
| 通知推送修复引入新的死锁 | 低 | 中 | 全面的集成测试，模拟高并发场景 |
| 死信重试造成风暴 | 低 | 低 | 重试时有速率限制；重新插入 outbox 而非直接发布 |

---

## 总结

aero-id 的架构在代码库中表现为**扎实但带有操作缺口**的状态。五个扩展方向均基于真实的代码证据，其中两个是已验证的 bug（通知推送、死信不可见性），需要优先修复。

最大的架构决策是**不引入任何新的数据基础设施**——当前的 Postgres + Go in-process 模式对于预期的负载和功能集是完全足够的。相反，改进重点应该放在：

1. **现有基础设施的可靠性修复**（通知推送、死信管理）
2. **解锁产品能力**（公共匿名访问）
3. **运维便利性**（配置热重载）

任何新方向的进入需要先通过 `make check-file-sizes && make lint && make test` 的强制性门禁，保持代码库清洁。
