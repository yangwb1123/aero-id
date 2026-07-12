Now I have a thorough understanding of the codebase. Here is my architectural analysis.

---

# 架构分析报告：aero-id

## 1. 架构评估

### 1.1 当前架构优势

**严格的分层单向依赖** — handler → service → repository → DB 的架构清晰执行，每个层次职责单一且不向上依赖。`domain` 包作为纯净的实体 + 不变量层，不依赖任何基础设施代码。

**Composition Root 模式** — `app.go` 是唯一负责依赖注入的地方，所有可选特性（审计日志、文件分享、引用计数、限流、Webhook 等）都以 Option 模式注入，不会因未使用的特性给测试或部署增加负担。

**双传输层共享 Service 层** — HTTP (Gin) 和 gRPC 共享同一套 `service.AdminService` / `service.FileService` 等接口，行为一致性由架构保证而非靠口头约定。

**事务性 Outbox 模式** — 领域事件的可靠传递通过 outbox 表 + Relay 异步消费实现，避免了发件路径中的 best-effort 副效应。事件在创建数据库行的事务中一并写入 outbox 表，确保原子性。

**故障隔离** — SSO 客户端实现了断路器 + 过期缓存提供服务 + 重试退避，使 SSO 不可用时老用户的请求仍能通过（降级到 stale 缓存）而非直接失败。

**可选特性以接口注入** — 引用计数 (`StorageRefRepository`)、文件分享 (`FileShareRepository`)、审计日志 (`AuditLogger`)、限流器 (`ratelimit.Limiter`) 都是以 nil-safe 可选方式注入的，默认行为是退回到 legacy 逻辑。

### 1.2 当前架构局限性

#### 局限性一：In-memory 状态导致无法水平扩展（P0 级架构债务）

四个核心模块持有进程内独占状态：

| 模块 | 数据结构 | 问题 |
|------|---------|------|
| SSE Hub | `sync.Map[uid → *uidSubs]` | 同一 uid 的不同设备连接到不同实例时，只能收到连接到的那台实例推送的事件 |
| 限流器 | `ratelimit.Limiter` token bucket | N 个实例各独立限流，实际限流阈值是配置值的 N 倍 |
| SSO TokenCache | `map[string]*cacheEntry` | 每个实例独立缓存同一 token，缓存命中率随实例数呈倒数下降 |
| SSO UserCache | 带单飞的批量缓存 | 同上，同一 uid 的查询在多个实例间不共享 |
| Outbox 读指针 | 数据库轮询 | √ 正确，但多个实例同时轮询产生无意义争用 |

**影响**：部署 2 个实例不带来 2 倍吞吐量提升，反而因缓存未命中率上升、SSE 连接错位导致整体效率低于 1.5 倍。

#### 局限性二：软删除级联策略未定义（P1 级数据正确性隐患）

AGENTS.md 明确声明软删除是系统约定：`gorm.DeletedAt` 无处不在。

但实际行为是：

- 当一条 `messages` 被软删除后，其 `comments`（通过 `target_id` 引用）、`message_reactions` 仍然存在且可查询
- `comment_reports` 引用 `comments(id)` 但无 CASCADE，一旦 `comments` 软删除，report 记录成了孤儿
- `file_shares` 完全无外键约束，文件软删除后 shares 行依然存在

**问题的根源不在于 ON DELETE CASCADE 缺漏，而在于软删除的语义从未在领域层定义**。读路径上没有任何 `WHERE parent.deleted_at IS NULL` 的传播约束，导致：

1. 查询某消息的评论时，可能返回父消息已被删除的评论
2. 统计文件引用数时，已删除文件仍被计入
3. 审计日志查询已删除资源的记录时，引用完整性断裂

#### 局限性三：通知事件类型覆盖不完整（P1 级功能缺口）

通知系统目前的 `notificationFor` switch 仅处理三个 case：

| 事件 | 来源 Service | 是否产生通知 |
|------|-------------|------------|
| `message.sent` | `message/service.go` | ✅ 产生 `message.received` |
| `message.read` | `message/service.go` | ✅ 产生 `message.read_receipt` |
| `comment.replied` | `comment/service.go` | ✅ 产生 `comment.reply` |
| `comment.react` | `comment/service.go:249` (`react()`) | ❌ 不产生 outbox 事件 |
| `message.react` | `message/service.go` | ❌ 不产生 outbox 事件 |
| `file.shared` | `file/service_share.go` | ❌ 不产生 outbox 事件 |

这意味着：**用户永远不会收到"有人赞了你的评论"、"有人对你的消息做出了反应"、"有人与你分享了文件"的通知**。这不是功能优先级问题，而是从服务建模阶段就没有把 "哪些领域事件需触发通知" 纳入领域设计。

#### 局限性四：管理操作面有四个定义但两个无实现

```go
const (
    PermContentModerate = "content.moderate" // ❌ 无端点映射
    PermAuditRead       = "audit.read"       // ✅ mapped
    PermUserBan         = "user.ban"         // ❌ 无用户封禁表，无端点
    PermSystemConfig    = "system.config"    // ✅ mapped for Grant/RevokeRole
)
```

`PermContentModerate` 和 `PermUserBan` 是领域常量但没有任何代码引用它们。没有 `user_bans` 表或 migration，没有 moderator 视角的全局内容隐藏/审核端点。现有的 `comment.Hide()`/`comment.Unhide()` 是**作者**操作，不是管理员操作。

---

## 2. 扩展方向

### 方向一：进程内状态可共享化（架构现代化 P0）

**为什么需要**：这是当前架构中唯一的水平扩展阻断器。当业务增长到需要 2+ 实例时，SSE、限流、缓存的进程隔离性将导致行为不一致、性能异常甚至错误。

**核心挑战**：

- SSE Hub 需要**发布/订阅中间件**（Redis Pub/Sub 或 NATS），每个实例既是发布者也是订阅者
- 限流器需要**分布式计数器**（Redis Atomic Counters 或 Lua 脚本）
- 缓存层需要**共享缓存**（Redis 或 Memcached）
- 挑战在于：这些变更需要**无停机迁移**，不能破坏现有单实例部署

**预期架构变更**：

```
当前: Hub sync.Map → Pub/Sub Adapter 接口 → Redis Pub/Sub 实现
当前: ratelimit.Limiter → Limiter 接口 → 可插拔实现 (local / redis)
当前: TokenCache / UserCache → 共享缓存层 (可选 Redis 后端)
```

```
                     ┌─────────────┐
                     │  Redis      │
                     │  Pub/Sub    │
                     └──────┬──────┘
                            │
              ┌─────────────┼─────────────┐
              │             │             │
         Instance A    Instance B    Instance C
         (Hub sub)    (Hub sub)     (Hub sub)
```

**对现有系统的影响**：

- Hub Push 接口不变（适配器模式），不影响 Notification Dispatcher
- 限流器的 `Allow()` 语义不变，仅后端替换
- 缓存的 Get/Set 接口不变
- **风险评估**：低——现有单实例逻辑只需提取接口并添加可选实现

**备选方案**：

| 方案 | 复杂度 | 一致性保证 | 运维依赖 |
|------|--------|-----------|---------|
| Redis Pub/Sub + Redis Cache | 中 | AP（最终一致） | 需 Redis |
| NATS JetStream | 中偏高 | CP + 持久化 | 需 NATS |
| PostgreSQL LISTEN/NOTIFY + 共享表 | 低 | AP（最终一致） | 无新依赖 |
| 应用层 Leader Election + 单点处理 | 低 | CP | 选主逻辑 |

**推荐**：先为限流器和缓存提取接口并实现 Redis 后端，SSE Hub 使用 PG LISTEN/NOTIFY 作为轻量级跨实例桥梁。这是最少新增依赖、最大的逐步迁移灵活性。

---

### 方向二：软删除级联语义在领域层定义（数据安全 P1）

**为什么需要**：当前系统中每个读路径都需要**自行决定**是否过滤已删除的父资源，这是重复的 bug 来源。随着业务扩展（消息/评论/文件数增长），孤儿记录将导致混乱的审计结果和不一致的用户体验。

**核心挑战**：

- GORM 的软删除（`UPDATE SET deleted_at`）不会触发 PostgreSQL 的 `ON DELETE CASCADE`
- 如果改为硬删除 + CASCADE，则违反 AGENTS.md "soft deletes everywhere" 约定
- 如果维持软删除，需要在 Repository 层对每个涉及父资源子查询的路径显式加 `deleted_at IS NULL` 条件
- **需要确定级联策略**：父软删除时，子资源应「软删除」「隐藏」「置为 orphan」还是「不变仅查询过滤」？

**策略选项**：

| 策略 | 实现代价 | 数据安全 | 用户可见影响 |
|------|---------|---------|------------|
| A: 查询传播 `deleted_at IS NULL` | 中（每个跨表查询都要加条件） | 高 | 用户看不到已删除资源上的内容 |
| B: 软删除父时自动软删除子（application-level cascade） | 高（需定义级联操作的顺序和事务边界） | 最高 | 子资源消失，如同从未存在过 |
| C: 子资源标记 `parent_deleted_at` | 中（新增列 + 查询条件） | 高 | 用户看到"该内容已被删除"占位符 |
| D: 父删除时子资源置为隐藏（`status = hidden`） | 中（需要状态机扩展） | 中 | 子内容不可见但记录保留 |

**推荐**：策略 C + D 混合——父软删除时给子资源写入 `parent_deleted_at` 时间戳（非破坏性标记），读路径检查此标记并显示占位符。这样既保护数据完整性又不丢失审计线索。

---

### 方向三：通知事件模型扩展（功能完整性 P1）

**为什么需要**：当前仅 3 种事件的通知覆盖是领域模型设计阶段就留下的缺口。缺少"点赞""反应"通知不仅影响用户参与度指标，还意味着系统的"通知"这一核心价值主张在领域建模时就被忽略了。

**核心挑战**：

- 新的通知事件需要对应的 payload 类型 -> outbox 事件 -> dispatcher mapping
- "点赞/反应"类事件有高频低价值的特性，需要 collapse/rate-limit 策略
- 需要决定：哪类事件需要通知？（不是每个 like 都需要通知）

**新增事件建议**：

| 事件类型 | 产出通知 | Collapse 策略 | 优先级 |
|---------|---------|-------------|-------|
| `comment.liked` | `like.received` | 按 (recipient, comment) 合并 15min 窗口内多个 like | P1 |
| `message.reacted` | `reaction.received` | 按 (recipient, message) 合并 | P1 |
| `file.shared` | `share.received` | 无需 collapse（低频） | P2 |
| `file.commented` | 已有 `comment.replied` 覆盖 | — | — |

**架构变更**：

```
// 新增领域类型
const (
    NotifTypeLikeReceived   NotificationType = "like.received"
    NotifTypeReaction       NotificationType = "reaction.received"
    NotifTypeShareReceived  NotificationType = "share.received"
)

// message/service.go react() 中新增:
evt := outbox.NewEvent("message", messageID, "message.reacted", payload)

// comment/service.go react() 中新增:
evt := outbox.NewEvent("comment", commentID, "comment.liked", payload)
```

**影响**：工作量主要集中在 service 层的 outbox 事件发射和通知 dispatcher 的新的 payload 解析 + notificationFor case。领域模型、仓储层、传输层无须变更。

---

### 方向四：管理员操作面补齐（运维效率 P1）

**为什么需要**：现有管理员接口已经完成了 GrantRole/RevokeRole/ListRoles/ListUserRoles/ListAudit 的全栈实现（HTTP + gRPC + Service + Repository），但未使用的 `PermContentModerate` 和 `PermUserBan` 意味着运营人员无法通过 API 管理违规内容和封禁用户——只能操作数据库。这是运营安全风险。

**具体缺口**：

1. 无 `POST /admin/users/:uid/ban` 端点
2. 无 `POST /admin/users/:uid/unban` 端点
3. 无 `GET /admin/comments/reported`（全局审核队列——当前只有作者可见自己的评论报告）
4. 无 `POST /admin/comments/:id/hide`（管理员视角隐藏，不同于作者 hide）

**实现可行性**：四个新增端点的每个大约 20-40 行 handler + 10 行路由 + service 层若干行。**注意**：需要新增 `user_bans` 表和对应的 domain model + repository + migration。

---

### 方向五：通知 Dispatcher 与 SSE Hub 的提取与统一（架构规范化 P2）

**为什么需要**：代码库中存在**两个 `Dispatcher`** 实现：

| 文件 | 是否被 app.go 引用 | 功能 |
|------|-------------------|------|
| `notification/dispatcher.go` | ❌ 未被 app.go 引用 | 基础版，仅有 block check + SSE push |
| `service/notification.go` | ✅ 被 app.go 引用 | 完整版，含 preference check + rate limit + collapse + SSE push |

这是重构遗留物。虽然 `notification/dispatcher.go` 可能已死代码，但继续维护两个 dispatcher 增加认知负载。

**建议动作**：

1. 删除 `notification/dispatcher.go`（确认无测试依赖后）
2. 将 `service/notification.go` 中的 `NotificationDispatcher` 提取到 `notification/` 包（职责归位），保留服务层的通知 CRUD 不变
3. 将 4 参数的 `NotificationPusher` 接口与 3 参数的 `hub.Pusher` 接口统一——两者的差异是 `eventID int64` 参数。考虑将 `Publish` 签名统一为包含 eventID（hub adapter 说明文档说这是可提取的差异）

---

## 3. 接口设计建议

### 3.1 关键接口稳定性原则

现有体系中有四个关键接口已经稳定且应保持向后兼容：

| 接口 | 位置 | 稳定性 | 备注 |
|------|------|--------|------|
| `FileStorage` | `internal/storage/storage.go` | ⭐⭐⭐ | 已经支持 Put/Get/Delete/PresignGet/PresignPut/NewKey/Stat，组合良好 |
| `AdminRepository` | `internal/repository/admin.go` | ⭐⭐⭐ | 完整覆盖 HasPermission + CRUD 角色 + 审计查询 |
| `NotificationPusher` | `internal/service/service.go` | ⭐⭐ | 稳定但需要接口签名统一 |
| `BlockChecker` | `internal/service/service.go` | ⭐⭐⭐ | 单一方法 `IsBlocked(blocker, blocked)`，足够通用 |

### 3.2 需要引入的新抽象层

**（1）跨实例状态后端接口**

```go
// 建议新增：internal/pkg/distributed/hub.go
type PubSub interface {
    Subscribe(ctx context.Context, channel string) (<-chan Message, error)
    Publish(ctx context.Context, channel string, msg Message) error
    Close() error
}
```

对应的限流器接口：

```go
// 建议新增：internal/pkg/ratelimit/backend.go
type Backend interface {
    Allow(key string, rps float64, burst int) (bool, error)
    Close() error
}
```

**（2）缓存后端接口**

SSO 缓存（TokenCache/UserCache）当前使用纯 `map`。提取接口后可以替换为 Redis：

```go
// 已有 sso/token_cache.go - 提取接口
type TokenStore interface {
    Get(ctx context.Context, key string) (string, bool, error)
    Set(ctx context.Context, key, value string, ttl time.Duration) error
    GetStale(ctx context.Context, key string) (string, bool, error)
}
```

### 3.3 向后兼容策略

所有接口提取遵循以下原则：

1. **新增接口 + 保留原有默认实现**：引入 `PubSub` 接口时保留现有的 `sync.Map` 实现作为默认值（`LocalHub`）
2. **Option 模式新增 Backend**：如 `NewHub(WithPubSub(redisPubSub))`，不传参数时用默认 local 实现
3. **接口签名不做 break change**：`Publish(uid, type, data)` 保持不变，`eventID` 作为可选项（改为 `var eventID ...int64` 或保持在 SSEEvent 结构体中）

---

## 4. 技术选型

### 4.1 是否需要引入新技术栈

**短期（P0-P1 实现）：不需要新增依赖。**

当前 Go 标准库 + GORM + gRPC + Gin 的组合足以解决所有 P0/P1 方向的问题。引入新的中间件（如 Redis）只有在方向一（进程内状态共享化）的 P0 实现时才需要。

**中期（方向一实现时）：建议引入 Redis。**

| 技术 | 用途 | 替代方案 | 选择理由 |
|------|------|---------|---------|
| Redis | Pub/Sub + Rate Limit + Cache | NATS / PG LISTEN/NOTIFY | 社区普及度高，运维成熟，Go 客户端成熟（go-redis/redigo），一个中间件解决三个问题 |
| Redis Stack | 限流 + Pub/Sub + Cache + 可选 Rate Limit Module | 原生 Redis | 不需要 Stack 的功能，基础 Redis 足够 |

**如果选 PG LISTEN/NOTIFY 作为跨实例 SSE Hub 桥梁**：

- 优点：无新增依赖，事务性发送（通知与数据库操作绑定）
- 缺点：PG NOTIFY 消息大小有限制（8000 bytes）；一个通道堵塞会影响数据库连接
- 适用场景：中小规模部署（< 5 实例），作为 Redis 的轻量替代

### 4.2 第三方依赖评估标准

鉴于 AGENTS.md 明确了工程约束（文件 ≤500 行，函数 ≤50 行，复杂度 ≤10），依赖引入的评估标准应扩展为：

| 评估维度 | 门槛 |
|---------|------|
| 必要性 | 能否在 50 行内用标准库实现？能否用现有代码组合？ |
| 兼容性 | 是否与 Go 1.26 + GORM v2 + gRPC 兼容？ |
| 安全 | 是否有已公开的 CVE？依赖树深度是否 > 3 层？ |
| 维护度 | 最近 commit 是否在 6 个月内？issue 回复是否及时？ |
| 入侵性 | 是否要求全局状态/init()/codegen？是否要求特定的项目布局？ |

### 4.3 自建 vs 采购

当前场景全为自建，这是正确的——aero-id 是一个通讯服务的基础设施层，没有第三方 SaaS 能替代其核心功能（消息、文件、评论、通知）。

唯一可能需要采购的是 **SSO**（当前通过 gRPC 调用外部 SSO，但 SSO 本身不在 aero-id 范围内）。aero-id 的 SSO 客户端已经做了正确的隔离：断路器 + 缓存 + 降级，所以 SSO 的可用性不影响 aero-id 的自身稳定性。

---

## 5. 实施路线图

### 5.1 优先级排序

基于验证报告的分析和我的代码审查，建议的优先级排序：

```
P0: 方向一（进程内状态共享化——限流器接口提取 + 缓存接口提取）
P1: 方向二（软删除级联语义定义 + 查询传播）
P1: 方向三（通知事件模型扩展——添加 like.received / reaction.received）
P1: 方向四（管理员操作面补齐——PermContentModerate + PermUserBan 端点）
P2: 方向五（Dispatcher 合并与 Hub 接口统一）
P2: 清理 notification/dispatcher.go 死代码
```

对验证报告的微调建议 **同意**：方向二（管理端）优先级应高于方向一（通知覆盖），因为其实现工作量极小但安全收益显著。

### 5.2 阶段划分

**Phase 1（2 周）—— 管理端补齐 + 软删除语义定义**

| 里程碑 | 交付物 | 依赖 |
|-------|--------|------|
| M1 | `user_bans` 表 + migration + domain entity | 无 |
| M2 | `POST /admin/users/:uid/ban`, `POST /admin/users/:uid/unban` | M1 |
| M3 | `POST /admin/comments/:id/moderate/hide` (moderator) | 无 |
| M4 | 软删除级联策略文档 + domain invariant 更新 | 架构决策文档 |
| M5 | 所有跨表查询加上 `parent.deleted_at IS NULL` | M4 |

**门禁**：`make test && make lint && make test-integration`

**Phase 2（2 周）—— 通知事件扩展**

| 里程碑 | 交付物 | 依赖 |
|-------|--------|------|
| M6 | `comment.liked` outbox 事件 + payload | 无 |
| M7 | `message.reacted` outbox 事件 + payload | 无 |
| M8 | Dispatcher 新增两个 case + collapse 策略 | M6, M7 |
| M9 | 合并 EventType 常量到 domain | M8 |

**门禁**：`make test && make lint`

**Phase 3（3 周）—— 接口提取（为水平扩展做准备）**

| 里程碑 | 交付物 | 依赖 |
|-------|--------|------|
| M10 | 限流器 `Backend` 接口提取 + local 实现作为默认值 | 无 |
| M11 | SSO 缓存 `TokenStore`/`UserStore` 接口提取 | 无 |
| M12 | Hub `PubSub` 接口提取（默认 local sync.Map） | 无 |
| M13 | Redis 后端实现（可选，在需要扩展时开启） | M10, M11, M12 |

**门禁**：`make test && make lint && make test-integration`

**Phase 4（清理期）—— 架构债务清理**

| 里程碑 | 交付物 | 依赖 |
|-------|--------|------|
| M14 | 删除 `notification/dispatcher.go` 死代码 | 确认无引用 |
| M15 | Hub Publish 接口签名统一（3 参数 vs 4 参数） | 无 |
| M16 | 补充未使用 permission 常量的 godoc 注释 + 删除或加 TODO | 无 |

**门禁**：`make test && make lint`

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| Phase 1 的软删除级联查询添加遗漏 | 中 | 高 - 数据一致性 | 1. 编写架构测试（`make check-cascade`），自动验证已知关联路径<br>2. 第一阶段只覆盖已知路径（message→comments, comments→reports/likes, file→shares），其余 TODO + issue 跟踪 |
| Phase 3 的接口提取导致现有测试失效 | 低 | 中 | 1. Mock 接口而非具体实现<br>2. 用 `compile-time assertion` 确保旧实现满足新接口 |
| Redis 中间件引入运维复杂度 | 中 | 中 | 1. Phase 3 的接口提取阶段不引入 Redis，仅提取接口<br>2. Redis 后端在 Phase 3 末尾作为可选实现提供<br>3. 单实例部署默认用 local 实现，零配置变更 |
| 通知新增事件后用户收到大量 "like" 通知 | 中 | 中 - UX 退化 | 1. 默认关闭 like/reaction 通知（用户需 opt-in）<br>2. 严格的 collapse 窗口（15 分钟）<br>3. NotificationPreference 中默认行 = disable |

### 5.4 不推荐做的事（明确不纳入路线图）

1. **不引入事件总线或消息队列（Kafka/RabbitMQ）**：outbox + Relay 足以应对当前规模。引入消息队列会增加至少一个系统组件和一倍的运维成本。如果将来需要事件流重放或事件溯源，可再评估。

2. **不将 UserCache/TokenCache 的 TTL 架构改为全局长缓存**：当前的 TTL + stale 模式已经在单实例下表现良好。如果需要共享缓存，用 Redis 做缓存后端即可。

3. **不重构 domain 包**：当前 domain 包的实体定义清晰、不变量集中在 `ValidateNewComment` 和 `CanReadFile`/`CanReadMessage` 中，不需要重构。

4. **不引入 CQRS/Event Sourcing**：当前系统的读写路径已经清晰分离，outbox 就是事件源的基础设施。但完整的事件溯源需要巨大的架构投资，在当前阶段收益不匹配成本。

---

## 总结

aero-id 的架构在代码组织层面是优秀的——分层清晰、依赖方向正确、可选特性通过接口注入。其核心架构债务集中在四个领域：

1. **进程内状态（最严重）**——SSE Hub、限流器、缓存均为 per-instance，是水平扩展的唯一真实障碍
2. **软删除级联未定义**——数据一致性隐患，需要领域层定义策略并在 repo 层传播查询过滤
3. **通知事件覆盖不完整**——React/Like 类事件不产生 outbox 事件，是领域设计缺口
4. **管理员操作面部分未实现**——PermContentModerate 和 PermUserBan 是空壳

验证报告整体准确（6/7 证据类别通过），但报告关于"管理员端点未暴露"的结论基于**早期版本的代码审查**——实际代码中 AdminHandler HTTP 和 gRPC 均已完整实现 GrantRole/RevokeRole/ListRoles/ListUserRoles/ListAudit，这些端点可通过 HTTP `POST /v1/admin/roles/grant` 和 `POST /v1/admin/roles/revoke` 以及 gRPC `AdminService.GrantRole`/`RevokeRole` 调用。唯一真实缺口是 `content.moderate` 和 `user.ban` 权限对应的端点。
