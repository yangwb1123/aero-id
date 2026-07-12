Now I have a thorough understanding of the codebase. Here is the architectural analysis.

---

# 架构分析：aero-id — 五个未覆盖方向的深度评估

## 1. 架构评估

### 当前架构的优势

1. **清晰的单向依赖**：`handler → service → repository` 的分层在《AGENTS.md》中有严格定义，代码也确实执行了这一点。两个传输层（HTTP/Gin + gRPC）共享同一业务逻辑层，符合预期。

2. **事务性 Outbox 模式实现完整**：`internal/outbox/` 提供了完整的 outbox 模式——事件与领域行在同一 DB 事务中提交，由 Relay 异步 drain。支持指数退避、dead-letter 标记、以及可选的 webhook 链式投递。这是写路径正确性的关键基础设施。

3. **可插拔的存储和后端抽象**：`storage.FileStorage` 接口完美地抽象了 local/S3 两种后端；SSO 客户端内置了断路器、serve-stale 缓存、singleflight 等生产级韧性模式。

4. **可观测性基础**：OpenTelemetry tracing、RED metrics、per-dependency readiness probe、结构化日志均已就位。

### 关键架构债务

#### 债务 #1：双架构并存（中度）
最显著的问题是代码库中存在**两套架构模式并存**：

- **旧模式**：`internal/service/{file,message,comment,admin,block,...}.go` → `internal/repository/{file,message,comment,...}.go` — 所有业务逻辑集中在扁平的服务中。
- **新模式**：`internal/{file,message,comment,block,notification,search}/` — 每个 domain 一个包，内部再分 `service.go`、`handler_http.go`、`handler_grpc.go`、`repository.go`。

新模式的 service 文件（如 `internal/message/service.go`）定义了完整的 `Service` 接口，而旧模式的服务文件（如 `internal/service/message.go`）变成了**瘦包装器**，委托给新模式。但这两层之间存在不一致：

- 旧服务层有时重复定义接口（如 `internal/service/service.go` 的 `MessageService` 接口 vs `internal/message/message.go` 的 `Service` 接口）
- 审计日志逻辑在两个层中都有实现
- 权限检查分散：`middleware/permission.go` 的 `PermissionChecker` 接口 → `internal/service/admin.go` 实现 → `internal/repository/admin.go` 查询 DB

**影响**：新贡献者要理解两套抽象；某些检查绕过了一层可能导致不一致；增加遗留债务的维护成本。

#### 债务 #2：Admin 权限定义了但未完全实现（低度）
`domain.PermUserBan = "user.ban"` 和 `domain.PermContentModerate = "content.moderate"` 已定义，但：
- 没有任何 service 方法实现 ban/unban 用户
- 没有任何 service 方法实现全局内容审核（hide/unhide across users）
- HTTP 和 gRPC handler 均无对应端点
- `requiredPermForMethod` 的 gRPC RBAC 映射中无对应条目

#### 债务 #3：通知类型硬编码（中度）
`Dispatcher.notificationFor()` 中的 `switch` 只处理 3 种事件类型，`default` 返回 `nil`（静默丢弃）。但 outbox 中已经发布了 `file.shared` 等事件，这些事件被无声吞噬。没有注册机制让新事件类型可以插拔式加入。

#### 债务 #4：`internal/domain/` 中的 actor 上下文操作函数重复（低度）
`service.ActorFrom()` / `service.WithActor()` 与 `domain.ActorFrom()` / `domain.MustActor()` 同时存在，签名略有不同。`domain` 包本应是纯实体层，不应承担上下文职责。

---

## 2. 扩展方向

以下是基于五个确认的缺口提炼出的高价值扩展方向，按业务价值排序。

### 方向 A：控制面管理平台（Admin Console）— P1

**为什么需要**：当前 admin 能力碎片化。角色管理（grant/revoke）已实现，但用户封禁（PermUserBan）和全局内容审核（PermContentModerate）未实现。缺乏 admin dashboard 端点意味着运营人员需要直接操作数据库才能完成日常管理任务。

**核心挑战**：
- 封禁操作需要跨 domain 一致：封禁用户后，需要拒绝其新消息、将其文件设为不可访问、隐藏其评论——这需要多个 service 协作
- 被内容审核（hide）的评论需要通知原作者并禁止再编辑
- 封禁/解封操作的幂等性与审计日志

**建议的架构变更**：
- 在 `internal/service/admin.go` 中扩展现有 `AdminService` 接口，增加 `BanUser(ctx, uid, reason, duration)` / `UnbanUser(ctx, uid)` / `ListBans(ctx, page)` 方法
- 新增 `internal/ban/` 域包（类似 `internal/block/`），包含单独的 `Repository`（banned_users 表）、`Service`、handler
- 在 `message`、`file`、`comment` 的 service 层检查封禁状态（通过 `BanChecker` 接口）
- 为封禁仓库添加 `BannedAt`、`ExpiresAt`（支持临时封禁）、`Reason`、`BannedBy` 列

**对现有系统的影响**：
- 低侵扰：新增域包 + 现有 service 的接口依赖（`BanChecker`）
- 与现有 `block` 系统正交（block 是用户主动社交过滤，ban 是运营强制）
- 审计日志路径已存在

**选项**：
| 选项 | 优点 | 缺点 |
|------|------|------|
| A1. 在 `internal/service/admin.go` 中扩展 | 保持 admin 入口统一，复用现有权限检查 | 使 admin service 膨胀，违反单一职责 |
| A2. 新建 `internal/ban/` 域包 | 符合 domain 拆分模式，明确职责边界 | 需要新的 handler + repository + migration |

**建议**：选 A2，因为与代码库中现有的 domain 拆分模式一致。

---

### 方向 B：分布式限流 — P1

**为什么需要**：当前每实例 token bucket 在 N 实例部署下允许 N × RPS 的吞吐，使限流形同虚设。对于 SSO 回调、消息发送等下游容量有限的路径，这可能导致生产事故。

**核心挑战**：
- 需要外部协调存储（Redis 或 PostgreSQL advisory locks）
- 原子扣减 + 分布式一致性 → 性能 vs 精确度的权衡
- 优雅降级：Redis 不可用时的 fallback 策略

**建议的架构变更**：
- 在 `internal/pkg/ratelimit/` 中新增 `RedisLimiter` 实现，接口与现有 `Limiter` 兼容，但增加 `Allow(key string) (bool, time.Duration)`（需要返回 retry-after）
- 配置层新增 `rate_limit.backend`（`local` | `redis`），`rate_limit.redis` 地址
- `RedisLimiter` 使用 `INCR` + `EXPIRE`（滑动窗口）或 Lua 脚本（更精确的 token bucket）
- 在 `internal/pkg/ratelimit/` 中保留 `Limiter` 作为 `local` 实现

**技术选型权衡**：
| 方案 | 精度 | Redis 依赖 | 代码复杂度 |
|------|------|-----------|-----------|
| Lua token bucket | 高（精确速率） | 是 | 中 |
| Redis INCR 滑动窗口 | 中（窗口边界效应） | 是 | 低 |
| PostgreSQL row-level locks | 高 | 否（复用现有 PG） | 中，但增加 PG 负载 |
| 本地 + 概率化近似 | 低 | 否 | 低 |

**建议**：先实现 Lua token bucket + Redis fallback 到 local。Redis 不可用时降级为 local（精度降低但不停服）。

**对现有系统的影响**：
- `RateLimitInterceptor` 和 HTTP middleware 使用同一接口，切换无感知
- 需要新增 Redis 连接池（可与 SSO cache 复用或独立）
- 配置新增 `redis` 段落（复用或新增）

---

### 方向 C：会话/消息生命周期管理 — P2

**为什么需要**：当前消息只有基本的 CRUD + 已读标记。缺少企业级 IM 的基础功能：会话级操作（mute、archive、pin）、正在输入指示、消息撤回等。对于面向消息的产品，这是关键特性。

**核心挑战**：
- `messages` 表无 `conversation_id`。当前会话隐式定义为 (min(from_uid,to_uid), max(from_uid,to_uid)) 对。加入显式 conversation 需要数据迁移
- Mute/Archive/Pin 是会话级属性，不是消息级属性——需要重新建模
- 正在输入指示（typing indicator）需要 WebSocket/SSE 实时通道（当前 hub 只支持 SSE 推送通知）

**建议的架构变更**：
- 新增 `conversations` 表 + `conversation_participants` 表
  ```sql
  CREATE TABLE conversations (
      id UUID PRIMARY KEY,
      created_at TIMESTAMPTZ NOT NULL DEFAULT now()
  );
  CREATE TABLE conversation_participants (
      conversation_id UUID REFERENCES conversations(id),
      uid TEXT NOT NULL,
      muted_at TIMESTAMPTZ,     -- NULL = not muted
      archived_at TIMESTAMPTZ,   -- NULL = not archived
      pinned BOOLEAN NOT NULL DEFAULT false,
      PRIMARY KEY (conversation_id, uid)
  );
  ```
- 在 `internal/message/` 中新增 `ConversationService`：`GetOrCreate(ctx, uidA, uidB)`、`Mute(ctx, convID)`、`Archive(ctx, convID)`、`SetTyping(ctx, convID)`
- 修改 `messages` 表，增加 `conversation_id` 列（可 NULL 向后兼容）
- 新的 `conversation.*` outbox 事件类型，通知另一参与方
- 利用现有 `hub.Pusher` 扩展 SSE 事件流支持 typing indicator

**数据迁移策略**：
| 策略 | 复杂度 | 停服时间 |
|------|--------|---------|
| 写路径新增 conv_id + 后台回填现有消息 | 高 | 无 |
| 写时自动创建会话（lazy） | 中 | 无 |
| 一次性迁移脚本停服执行 | 低 | 有 |

**建议**：写路径 lazy 创建 + 后台异步回填。先在写时检查若 `conversation_id IS NULL` 则自动查找或创建对应会话，后续逐步补全历史数据。

---

### 方向 D：内容生命周期管理（TTL + GDPR 导出 + 批量清除）— P2

**为什么需要**：当前无任何内容过期机制。文件、消息永久存储，违反 GDPR 的"数据最小化"原则。也没有用户数据导出能力。存储成本随时间线性增长。

**核心挑战**：
- TTL 的触发时点：创建时设置 vs 系统定时扫描
- TTL 到期后的动作：硬删除 vs 软删除 + 通知用户 vs 自动归档
- GDPR 导出需要跨 domain 聚合数据（文件 + 消息 + 评论 + 通知）
- 批量清除的限速和可观测性

**建议的架构变更**：
- 在 `domain.File` 和 `domain.Message` 中增加 `ExpiresAt *time.Time`
- 扩展 storage GC sweeper（`internal/storage/gc/`）为通用的**内容生命周期管理器**：
  ```
  internal/storage/gc/
  ├── sweeper.go          # 现有的文件 GC（重命名为 file_sweeper.go）
  ├── lifecycle.go        # 新增：通用 TTL 扫描器
  ├── lifecycle_test.go
  ```
- `LifecycleManager` 接口：
  ```go
  type LifecycleManager interface {
      Run(ctx context.Context) (Stats, error)
  }
  ```
  文件 TTL sweeper 和消息 TTL sweeper 各自实现，共享调度框架
- 新增 `/v1/export` 端点（异步）：触发后台 job 生成包含用户所有数据的 JSON 文件并存至 storage，完成后通知用户
- 配置层新增 `data_retention` 段落：`message_ttl`、`file_ttl`、`enable_export`

**对现有系统的影响**：
- 低侵扰：TTL 字段为 NULLable，历史数据无变化
- 导出路径是新功能，不修改现有逻辑
- GC sweeper 的扩展保持 OPT-IN 原则

---

### 方向 E：通知系统覆盖扩展 — P1

**为什么需要**：当前只有 3 种通知类型被实际投递，大量 outbox 事件被静默丢弃。这不是设计约束而是实现疏漏——`file.shared` 事件已经在 outbox 中被发布但 dispatcher 忽略它。用户收到文件分享通知是一个合理预期。

**核心挑战**：
- Dispatcher 的 `notificationFor()` 是硬编码 switch，不可扩展
- 现有通知域模型 `Notification` 只有 `SubjectType`/`SubjectID` 的结构化字段，不同类型的额外数据需要新的列或 payload JSON
- 通知 collapse（当前 5 分钟窗口）对某些类型（如 typing indicator 等高速事件）不适用
- 通知偏好（`NotificationPreference`）已实现但 dispatcher 中尚未实际使用

**建议的架构变更**：

核心问题是 `dispatcher.go` 中硬编码的 event→notification 映射。解决方案是从硬编码 switch 演进为**注册式通知工厂**：

```go
// internal/notification/dispatcher.go 重构建议

// NotificationFactory 是一个将 outbox.Event 转换为 domain.Notification 的函数。
// 返回 nil 表示"该事件不应产生通知"。
type NotificationFactory func(e *outbox.Event) (*domain.Notification, bool)

type Dispatcher struct {
    notifs    Repository
    blocks    block.BlockChecker
    prefs     PreferenceChecker
    pusher    hub.Pusher
    rateLimit *ratelimit.Limiter
    factories map[string]NotificationFactory  // eventType → factory
}

func (d *Dispatcher) Register(eventType string, factory NotificationFactory) {
    d.factories[eventType] = factory
}
```

这样每种通知类型可以独立注册，新增类型无需改动 dispatcher 核心：

需要立即添加的工厂：
| 事件类型 | 通知类型 | 优先级 |
|---------|---------|--------|
| `file.shared` | `file.shared_with_you` | P0（已有事件被丢弃） |
| `comment.reported` | `comment.report_received` | P1 |
| `message.reaction` | `message.reaction_received` | P1 |
| `comment.liked` | `comment.liked` | P1 |
| `system.announcement` | `system.announcement` | P0（系统消息） |

**对现有系统的影响**：
- 重构 dispatcher，但不改变现有 3 种通知类型的行为（向后兼容）
- `Register` 模式使 dispatcher 成为无状态的映射表，方便测试
- 在 app.go 中集中注册所有工厂（类似路由注册）

---

## 3. 接口设计建议

### 核心原则

1. **优先使用 domain 包接口，而非 service 包接口**。当前 `internal/service/` 中定义的接口（如 `service.MessageService`）与 `internal/message/` 中定义的接口（`message.Service`）是重复的。建议逐步淘汰 `internal/service/` 的包装层，让 handler 直接依赖 domain 包接口。过渡期可以在 `internal/service/` 中保留兼容别名。

2. **通知工厂注册式设计**（如上文所述）应成为跨 domain 事件消费的标准模式。outbox 的 `Handler` 接口本身就是正确的抽象，当前问题在于 `Dispatcher` 内部实现不是可扩展的。

3. **BanChecker 接口**应放在 `internal/domain/` 或 `internal/safety/` 包中，被 message、file、comment 等多个 service 引用，避免循环依赖。

### 是否需要新的抽象层

- **需要**：一个 `internal/events/` 包，统一定义所有 outbox 事件类型常量。当前事件类型字符串分散在多个包中（message 中硬编码 `"message.sent"`，dispatcher 中匹配 `"message.sent"`），没有单一事实来源。
  ```go
  package events
  
  const (
      MessageSent         = "message.sent"
      MessageRead         = "message.read"
      CommentReplied      = "comment.replied"
      FileShared          = "file.shared"
      CommentReported     = "comment.reported"
      CommentLiked        = "comment.liked"
      MessageReactionAdded  = "message.reaction.added"
  )
  ```

- **不需要**：现阶段不需要在 service 和 handler 之间增加额外抽象层。当前两 transport 共享同一 service 接口的模式已足够。

### 向后兼容性

- 所有 DB schema 变更使用 NULLable 新列或新表，不影响现有查询
- 新增 `conversations` 表后，现有 `messages` 的 `conversation_id` 可为 NULL，旧记录通过 `(from_uid, to_uid)` 对兼容
- 现有通知类型行为不变，新增通知类型通过注册添加，不影响现有消费者
- 限流从 local 改为 redis 后，`ratelimit.Limiter` 接口不变，仅实现切换

---

## 4. 技术选型

### 需要引入的新技术

| 方向 | 需要引入 | 理由 | 替代方案 |
|------|---------|------|---------|
| 分布式限流 | **Redis** | 方案 B 的分布式协调需要外部存储 | PostgreSQL advisory locks（性能不如 Redis） |
| 会话管理 | **无** | 复用现有 Postgres + SSE hub | 无需新依赖 |
| GDPR 导出 | **无** | 异步 job 使用现有 outbox 机制 | 无需新依赖 |
| Admin 封禁 | **无** | 新建表 + repository | 无需新依赖 |

**Redis 引入评估**：
- 项目中已有 go-redis/cruex 等 Redis 客户端在 go.sum 中（通过 SSO 或其他依赖间接引入），直接引入的增量不大
- Redis 也适合未来其他场景：分布式 rate limiting、session cache、消息队列（替代 outbox poll 模式）
- 风险：增加运维复杂度。缓解策略：限流降级为 local（Redis 不可用时限流精度降低但不停服）

### 自建 vs 采购决策

| 决策点 | 建议 | 理由 |
|--------|------|------|
| 通知工厂框架 | 自建 | 与现有 outbox/dispatcher 无缝集成，代码量小（~50 行核心逻辑） |
| GDPR 导出引擎 | 自建 | 数据模型完全私有，现有 outbox + storage 已提供所需基础设施 |
| 分布式限流 | 用 Redis 自建 | 开源方案（如 `github.com/go-redis/redis_rate`）可参考，但项目需要精确控制降级行为 |
| 封禁系统 | 自建 | 与外部 SSO 解耦，无需采购第三方 abuse 平台 |

---

## 5. 实施路线图

### 优先级排序

| 方向 | 优先级 | 估算人周 | 理由 |
|------|--------|---------|------|
| **A. Admin 封禁** | P0 | 2-3 | `PermUserBan` 已定义但为零实现，是已公开的缺口 |
| **E. 通知覆盖扩展** | P0 | 2-3 | `file.shared` 已发布但被丢弃，是功能性缺陷，用户可见 |
| **B. 分布式限流** | P1 | 3-4 | 多实例部署下当前限流失效，生产风险 |
| **D. 内容生命周期** | P1 | 4-5 | GDPR 合规需求，存储成本控制 |
| **C. 会话管理** | P2 | 5-8 | 高业务价值但对现有架构改动最大 |

### 阶段划分

**Phase 1 (2-3 周) — 修复已知缺陷**

1. 实现封禁域（`internal/ban/` + migration + 集成到 message/file/comment）
2. 重构通知 dispatcher 为注册式工厂，立即注册 `file.shared` 工厂
3. 在 `internal/events/` 中建立统一事件类型常量

**Phase 2 (3-4 周) — 基础设施增强**

1. 引入 Redis 依赖 + 配置
2. 实现 `RedisLimiter`（Lua token bucket）+ 降级策略
3. 扩展现有 GC sweeper 为通用生命周期管理器，添加 TTL 扫描

**Phase 3 (5-8 周) — 产品能力扩展**

1. `conversations` 表 + migration + 数据回填
2. ConversationService + mute/archive/pin
3. GDPR 导出端点 + 异步导出 job

### 风险和缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| Redis 成为新单点 | 中 | 高 | 降级到 local 限流（精度降但服务不中断）；Redis 配置从现有 SSO 连接复用 |
| 会话数据迁移出错 | 中 | 中 | 写路径 lazy 创建，不回填历史（标记迁移列可 NULL）；分阶段 rollout |
| 通知工厂注册遗漏 | 低 | 低 | 测试验证：对每个已知 outbox 事件类型断言有对应工厂 |
| 封禁与 block 语义混淆 | 中 | 中 | 明确文档：block = 用户主动社交过滤，ban = 运营强制。两个独立的查询路径 |
| 导出大文件 OOM | 低 | 中 | 导出使用 streaming JSON 写入 storage（而非内存缓冲），已有 streaming 模式可复用 |

---

## 总结

当前 aero-id 的架构质量整体良好——layered architecture、outbox pattern、可观测性、韧性模式都已就位。五个缺口反映的是**演进中的未完成状态**而非根本性设计缺陷。最主要的架构债务是两套模式（domain 包 vs service 包）的并存，但这个过渡状态在活跃开发的项目中是正常现象。

建议的三个立即行动：
1. **实现封禁系统**（已有 schema 未使用）
2. **修复通知覆盖**（已有事件被丢弃）
3. **建立统一事件类型常量**（消除跨包 magic string 重复）

这三个都是 P0，代码量小（避免用"人周"单位显得很大，实际每个大约 1-2 天），且直接修复用户可见的缺陷。Redis 引入和会话管理可以进入技术储备列表，在下一轮规划中评估优先级。
