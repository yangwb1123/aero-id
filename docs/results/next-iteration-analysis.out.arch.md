现在我对系统有了全面了解。以下是基于代码库实际状态的完整架构分析。

---

# aero-id 架构分析报告

> 日期：2026-07-12 | 基于 `main`（2026-07-11）代码库及现有分析文档

---

## 1. 架构评估

### 1.1 现有架构的优势

| 维度 | 评估 |
|------|------|
| **关注点分离** | `handler → service → repository` 单向依赖严格执行。`service` 是业务逻辑的唯一载体，`handler` 仅做传输编解码。这是正确的，并且已经保持。 |
| **双传输对等** | HTTP REST + gRPC 共享同一个 `service` 层，38 个 RPC 功能对等，这是架构纪律的体现。`app.go` 的注入方式使得双传输可以对称发展。 |
| **事务性 Outbox** | 核心写入路径在一个 GORM 事务中同时持久化业务行 + outbox 事件（`repository` 层接收 `*gorm.DB`），relay 异步 drain。这是可靠事件驱动作业的正确模式。 |
| **SSO 弹性** | 断路器 + stale-cache + singleflight + 重试回退 + 证书固定 + keepalive — 对远程依赖的容错设计是生产级的。 |
| **跨切面关注点分离** | 限流（`middleware.RateLimit`）、幂等性（`middleware.Idempotency`）、请求追踪/日志/指标都在中间件层处理，不侵入业务逻辑。 |

### 1.2 架构债务与技术债

**债务一：中间件链中的 Auth → RateLimit 顺序依赖**

`router.go` 中目前的顺序是：

```
/v1 → Auth → RateLimit → Idempotency → handler
```

`RateLimit` 依赖 `Auth` 注入 `actor_uid`（key 基于已认证用户），这意味着：
- 如果将来有未认证的 `/v1/*` 路由（例如带有 API key 的机器对机器端点），限流 key 会回退到客户端 IP，而不是 API key
- 封禁检查（你的建议中的方向二）必须放在 `Auth` 和 `RateLimit` **之间**，或者 `Auth` 之后、路由分发之前
- 当前中间件顺序支持 `RateLimit` 后 `Auth` 中追加新的检查，但如果需要封禁检查，正确的位置严格来说是在 `RateLimit` **之前**（在被限流之前拒绝被封禁用户，这样更干净）

**债务二：Repository 层围绕 `*gorm.DB` 的隐式事务边界**

每个 `repository` 方法要么从内部成员 `gdb *gorm.DB` 隐式使用连接，要么接收一个 `tx *gorm.DB` 参数。`fileShareRepo` 被单独注入 `service`（`service.WithFileShareRepo`），而不是与 `fileRepo` 统一在一个事务协调器下。跨实体的操作（如文件分享 + 审计日志 + storage_ref 更新）在同一个事务中发生，因为 `service` 方法显式调用 `tx` 方法——但这没有被强制执行。新写的代码很容易意外地遗漏 `tx` 参数，导致在单独连接上操作。

**债务三：Outbox 事件类型仅限于写入路径的一个子集**

当前 outbox 事件类型仅覆盖：

```
message.sent → message.received (notification)
message.read → message.read_receipt (notification)  
comment.replied → comment.reply (notification)
```

`domain/audit.go` 中有更多操作被审计（`AuditFileShareGrant`、`AuditUserBlock`、`AuditMessageDelete` 等），但这些**不**产生 outbox 事件。审计日志是独立写入的（直接写 `audit_events` 表，不经过 outbox）。这意味着：
- 审计事件不能触发下游处理（如：block → 取消文件分享，delete → 清理通知）
- 没有审计事件的钩子来触发基于跨实体操作的影响（如：账户删除 → 级联清理）

**债务四：SSE Hub 是单实例的**

`hub.New()` 在 `app.go` 中每实例创建一个新的内存 hub。多个副本各自维护自己的一组 SSE 连接。`notificationDispatcher` 在其本地 hub 实例上调用 `pusher.Publish()`。通知以 at-most-once 方式投递（dispatcher 在 `Create` 推送失败时**不**重试——没有错误检查），并且仅推送到连接了该特定实例的客户端。这在高可用部署中是一个已知缺口。

**债务五：存储 GC 是可选的，但在 S3 上默认关闭**

`app.go` 中写入：
```go
} else if cfg.Storage.Type == "s3" {
    log.Warn("storage gc is DISABLED with s3 backend — stale blobs will accumulate")
}
```
GC 是在可选基础上设计的，但对于 S3 存储的部署，**没有任何机制可以防止存储持续增长**。没有 GC，S3 上的孤立 blob 永远不会被回收。

### 1.3 关键设计决策评估

| 决策 | 评估 |
|------|--------|
| **没有账户表，由 SSO 拥有的身份** | 正确解耦。减少 GDPR 数据范围（账户数据存储在别处），避免密码管理。但确实使级联删除复杂化（见方向三讨论）。 |
| **UUIDv7 主键 + cursor 分页** | 正确的选择。UUIDv7 天生按时间排序，允许 `WHERE id < cursor ORDER BY id DESC LIMIT n+1` 无需 OFFSET，性能稳定。 |
| **两层评论嵌套** | 产品决策看起来合理，但 `domain` 中强制执行的 2 层限制，如果没有 `nesting_too_deep` 错误回退，就无法将来扩展到 N 层。 |
| **Transactional Outbox vs 2PC/Saga** | Outbox 是本系统中正确的事件驱动架构模式。没有跨服务协调（无 Saga 需要），因为所有实体都驻留在同一个 Postgres 数据库中/同一个 Go 进程中。 |
| **GORM vs 手写 SQL** | GORM 对于像此类大小且具有简单映射（struct → table）的 CRUD 服务来说是可以接受的。`domain/notification.go` 和 `domain/file.go` 中的显式模型定义保持映射清晰。功能较重的查询（搜索）使用手写 SQL 通过 `gorm.Raw`。 |

---

## 2. 扩展方向

基于现有代码分析，这里有 5 个高价值架构扩展方向：

### 方向 1：跨实例状态共享层（Redis 后端）

**为什么需要（业务 + 技术价值）**

现有代码在多处明确记录了每个实例的限制：

| 组件 | 问题 |
|------|------|
| 限流器 | 每个实例，公平性在 `N` 个副本上乘数效果 |
| 幂等性存储 | 每个实例，幂等性保证无法跨副本 |
| 通知折叠 | `FindRecentByTypeAndActor` 在每个实例上单独执行，无法跨实例折叠 |
| SSO 缓存 | 每个实例，滚动部署导致冷缓存 |
| Outbox relay | 多实例轮询冲突（尽管使用 `SKIP LOCKED` 可以缓解） |
| SSE Hub | 只能实时推送到连接了本地实例的客户端 |

这实际上意味着如果部署超过 1 个副本，系统会降级。对于高可用部署来说，这是一个阻塞性问题。

**核心挑战**

- 引入 Redis 增加了运维复杂度（高可用 Redis、网络延迟、序列化格式）
- 向关键路径添加网络跃点将增加延迟
- 缓存失效策略（Redis 中的 TTL 与应用级驱逐）
- 从纯 GORM 状态过渡到 Redis 作为可丢弃/可重建状态层（幂等性键、速率限制器桶）

**预期架构变更**

```
app.go:
  redisClient := redis.New(...)
  rateLimiter := ratelimit.NewRedis(redisClient, ...)
  idemStore := idempotency.NewRedisStore(redisClient, ...)
  ssoCache := sso.NewRedisCache(redisClient, ...)
  sseHub := hub.NewRedisBacked(...)
```

基础设施包需要抽象：
- `ratelimit.Store` 接口（现有内存实现，新的 Redis 实现）
- `idempotency.Store` 接口（现有 GORM 实现，新的 Redis 实现）
- `sso.Cache` 接口（现有内存 + TTL，新的 Redis + TTL）

Redis 应该是**可选的**：如果未配置，则回退到内存/crdb 实现。

**对现有系统的影响**

- **低** — 这些都是独立接口替换。没有业务逻辑会改变
- **无** — Redis 宕机不应导致服务降级。回退到内存/crdb 行为
- 迁移路径：作为配置选项（`cache.backend: memory | redis`）

### 方向 2：域事件统一（Outbox 作为所有状态变更的唯一来源）

**为什么需要**

目前审计日志、通知和 outbox 事件之间是脱节的：

```
Write path:
  业务行  ← GORM 事务
  outbox 事件  ← 同一个 tx
  审计日志行  ← 同一个 tx（某些路径）或独立 tx（其他路径）
```

这意味着：
- 审计事件无法触发下游工作（例如：`user.block` → 取消活跃文件分享）
- 通知是硬编码到 dispatcher 中的 `switch` 语句，而不是从受支持事件类型的注册表派生
- 审计和通知可以说是同一底层的两个不同投影——系统的副作用日志

**核心挑战**

- 将现有 11+ 审计事件类型映射到 outbox 事件类型
- 确保 `audit_repository.Log()` 通过 outbox 而不是直接写入
- 需要定义哪些 outbox 事件产生通知与哪些仅用于审计跟踪
- 避免现有 NOTIFY 路径的性能退化

**预期架构变更**

```
service 层中：
  删除： auditRepo.Log(ctx, ...)          // 直接写
  添加： outboxRepo.Append(ctx, tx, ...)  // 与业务行在同一个 tx 中

新的 outbox handler： AuditHandler
  接收 outbox 事件 → 写入 audit_events 表（如果事件类型有 audit=true）

通知 dispatcher 变成一个 outbox.Handler，带有基于注册表的 map：
  eventType → notificationType mapping
  而不是当前的 switch 语句
```

**对现有系统的影响**

- **中高** —— 这触及每个写入路径。存储库接口需要更改
- 审计行为保持不变，但基础设施完全不同
- 需要小心迁移：审计日志需要在新旧模式下表现一致，直到旧路径被移除
- **收益**：开箱即用的死信可见性、重试、幂等性，用于所有异步副作用

### 方向 3：数据生命周期管理（导出 + 级联删除）

根据你的观察，这是被低估的。

**为什么需要**

- **合规**：GDPR/CCPA 数据可移植性和擦除请求是强制性的
- **产品**：在没有账户处理能力的情况下，无法真正提供“删除账户”的操作
- **现有架构缺口**：每个实体（文件、消息、评论、通知）当前都有自己的 `service` 方法和自己的 `repository`。没有编排器。

**核心挑战**

- **事务边界**：级联删除涵盖文件、消息、评论、通知、偏好、block 关系、角色分配 —— 每张表都需要在同一个事务边界内删除（如果使用级联，则同一连接；如果使用 outbox 驱动的异步级联，则不同连接）
- **存储清理**：删除文件还需要从 S3/本地存储中清理 blob。目前这是 GC 职责，但 GC 是可选的
- **跨实例一致性**：删除触发后，其他实例需要收到通知（通过 outbox）
- **测试复杂性**：需要为每个实体覆盖大量边界（已删除的 actor 已评论消息、已分享文件等）

**预期架构变更**

```
new package: internal/lifecycle

type LifecycleService interface {
  ExportUserData(ctx, uid string) (*UserDataExport, error)
  ScheduleAccountDeletion(ctx, uid string) (jobID, error)
  CancelDeletion(ctx, jobID string) error
}

执行：
  阶段 1：数据导出（只读，隔离）
    - 收集 uid 引用的所有行（文件、消息、评论等）
    - 解析 SSO 用户信息
    - 生成 JSON/YAML
    
  阶段 2：计划删除（软模式）
    - 将所有内容标记为 `deleting_at`（软状态）
    - 创建 outbox 事件（account.deletion_scheduled）
    - 在宽限期内允许取消
    
  阶段 3：实际删除
    - 通过 outbox 驱动的异步作业
    - 每张表（存储库层）的 `DeleteByOwner(ctx, tx, uid)` 
    - S3 清理通过 storage_refs 表
```

**对现有系统的影响**

- **高** —— 需要向每个存储库接口添加 `DeleteByOwner` 方法
- 事务协调需要 `*gorm.DB` 透传，或者 outbox 驱动的异步级联
- 强烈建议：先在 `service` 层实现一个**干运行模式**，列出会删除的内容，而不实际删除
- 现有 `gorm.DeletedAt` 软删兼容：只需设置 `deleted_at`，GC 稍后物理清理

### 方向 4：封禁中间件 + 管理端点（你的观察 #2）

**为什么需要**

- **安全**：被封禁用户不应被接受 SSE 连接，且应在**任何其他处理之前**在所有 `/v1/*` 路由上被拒绝
- **审计完整性**：目前在中间件中没有封禁检查。SSE 事件处理程序（`events.go`）验证令牌但不检查封禁状态
- **与审计日志一致**：在没有管理 API 的情况下通过 SQL 手动管理封禁会绕过 `audit_events`

**核心挑战**

- **中间件位置**：你的评估是正确的——它应该在 Auth 之后、RateLimit 之前。但 `router.go` 目前将 Auth 和 RateLimit 构建为单个 `authMW` 切片。需要在它们之间插入新的中间件
- **缓存**：封禁检查需要高效——不应在每个请求上加载。SSO 的用户缓存可以扩展为包含封禁状态，或者需要专用的封禁检查缓存
- **SSE 集成**：SSE 连接建立（`hub.Subscribe`）还需要在订阅之前检查封禁状态

**预期架构变更**

```go
// new middleware: middleware/ban.go
func Ban(checker BanChecker) gin.HandlerFunc {
    return func(c *gin.Context) {
        uid, _ := c.Get("actor_uid")
        banned, err := checker.IsBanned(c.Request.Context(), uid.(string))
        // 如果被封禁则 403
    }
}

// router.go 中的新中间件顺序：
authMW := []gin.HandlerFunc{
    middleware.Auth(validator),
    middleware.Ban(banChecker),   // <-- 新
}
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter))
}
```

**管理端点**（一起构建，不跳过）：

| 端点 | 用途 |
|------|---------|
| `POST /v1/admin/bans` | 封禁用户（需要 `user.ban` 权限） |
| `DELETE /v1/admin/bans/{uid}` | 解除封禁用户 |
| `GET /v1/admin/bans` | 列出活跃封禁 |

每个端点都会写入 `audit_events`（`AuditUserBan` / `AuditUserUnban` 操作类型）。

**对现有系统的影响**

- **低** —— 一项新的中间件 + 一个新的存储库（`user_bans` 表）+ 管理端点
- SSO 仍对 token 进行身份验证，但 aero-id 会拒绝已处理的封禁
- BanChecker 接口可以被 mock 用于测试

### 方向 5：群组消息传递（Group Messaging）

正如现有扩展文档中所记录的那样——当拥有 1:1 消息传递、通知、文件共享和评论时，群组是下一步合乎逻辑的演进。

**为什么需要**

- 扩展产品能力，从 1:1 对话到群组聊天
- 几乎所有现有基础设施都可以复用（outbox、通知、SSE、block、搜索）

**核心挑战**

- 架构影响低，但范围广：消息模型更改、新的服务、新的存储库、群组 ACL
- 已读回执需要扩展到群组（每个成员的 `last_read_message_id` 而不是二进制标志）
- 群组包含被 block 的用户：在发送时过滤，而不是在消息创建后
- 大型群组（1000+ 成员）的推送：outbox relay 需要根据事件类型对收件人进行批量 dispatch

**预期架构变更**

```
新的实体：  group, group_members
消息更改：  Message.GroupID (nullable FK)
新的存储库： groupRepository
新的服务：   groupService
现有的服务影响： messageService（发送到群组）、notificationService（群组事件）
```

**对现有系统的影响**

- **中** —— 范围广但影响浅。不需要基础设施变更
- 现有的 block、通知、搜索和文件分享可以全部适配

---

## 3. 接口设计建议

### 3.1 接口设计原则

对于这个代码库：

1. **业务接口属于 `service` 包**：`service.go` 中定义的 `FileService`、`MessageService` 等接口是整个系统稳定性的来源。它们既是 HTTP 和 gRPC handler 的契约，也是 mock 的目标。

2. **存储库接口应保持在各自包的本地**：目前，`notification.Repository` 在 `notification/notification.go` 中定义，这是一个好的模式。但其他存储库（如 `fileShareRepo`）被作为具体类型注入，而不是接口。新代码：始终定义清晰的存储库接口。

3. **跨实体操作需要新的编排层或用例接口**：目前，跨实体逻辑存在于 `service` 层，使用多个存储库依赖。对于数据导出/删除等复杂编排，考虑使用一个新的 `lifecycle.Service` 来编排多个存储库，而不是将这些方法添加到现有的服务中。

### 3.2 是否需要新的抽象

| 抽象 | 需要？ | 理由 |
|----------|--------|---------|
| `StorageCache` 接口（内存 + Redis） | ✅ 是 | 允许在同一接口下可切换的内存/Redis 实现。SSO 缓存当前在 `sso/client.go` 中是明确的；提取 `TokenCache` 和 `UserCache` 接口将允许 Redis 实现 |
| `TransactionCoordinator` | ⚠️ 也许 | 如果方向三（级联删除）向前推进，则 `*gorm.DB` 透传需要类型安全的事务协调，而不是每个方法都接收 `*gorm.DB` |
| `LifecycleService`（数据导出/删除） | ✅ 是 | 新的跨实体域，不适合任何现有服务 |
| `EventRegistry` | ✅ 是 | 如果方向二（统一事件）向前推进，则需要替代 notifDispatcher 中当前的 `switch` 语句 |

### 3.3 向后兼容性

当前的 HTTP + gRPC 双传输意味着：
- **不要重命名任何 protobuf 字段**（gRPC 客户端二进制依赖）
- **不要删除 HTTP 端点**而不同时弃用（使用 `Deprecated` 注解 + 日志警告）
- **向列表端点添加新字段**是安全的，只要 JSON/protobuf 编码器不会因为未识别的字段而崩溃（我们的编码器是安全的）
- **新查询参数**始终向后兼容（忽略未知参数是约定）

---

## 4. 技术选型

### 4.1 需要评估的新技术

| 技术 | 针对方向 | 决策框架 |
|----------|-----------|----------------|
| **Redis** | 方向一（跨实例状态） | 必须评估是否因为一个两副本的部署而值得增加运维负担。备选：如果只有两个实例，一个简单的基于文件的租约 + 更多 aggressive 的轮询就足够了。如果目标是 HA（3+ 副本），Redis 是值得的。 |
| **MinIO 的 bucket 通知（SQS 式）** | 目前没有——Mid 到长期（文件处理流水线） | 目前不需要。存储 GC 是主动的，只要运行就会清理。 |
| **pg_event_listen/notify** | Outbox relay 的替代方案 | 值得考虑。目前 relay 每 5 秒轮询。使用 `NOTIFY` 可以在写入后立即唤醒 relay，而无需轮询。但 `LISTEN` 是每个连接的，需要为 relay 使用一个专用连接（`sql.DB` 的连接池不直接支持 `LISTEN`）。每种方法都有利弊。 |

### 4.2 依赖评估标准

评估新依赖的清单：

1. **API 稳定性**：v1+、承诺向后兼容、Go 模块语义版本控制
2. **许可证兼容性**：与现有依赖兼容（MIT、Apache 2.0、BSD）
3. **Go 模块集成**：在 `go.sum` 中受信任，无 C 依赖（除非明确需要）
4. **运维成本**：是否需要新守护进程（Redis 需要的，etcd/ZooKeeper 绝对不需要）
5. **社区健康**：活跃维护、响应迅速的问题跟踪器、漏洞补丁在 7 天内

### 4.3 自建 vs 采购

自建（当前方法）适合此代码库，原因如下：

| 考虑因素 | 评估 |
|-----------|--------|
| **业务独特性** | 高 —— 数据模型、通知规则、文件共享语义专门针对产品 |
| **现有投资** | 已投入大量工作 —— 180+ Go 文件、完整的架构、集成测试 |
| **替代方案** | 对于消息/文件/评论平台，替代方案将是 Firebase、SendBird、Stream —— 每个都引入了供应商锁定的外部依赖 + 成本 + 不同的数据模型，且匹配度低 |
| **需要外包的组件** | Push Notification（FCM/APNs）—— 由于平台复杂性，应使用 Firebase 等服务，而不是自建 |

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 方向 | 理由 |
|----------|-----------|--------|
| **P0** | 方向 4：封禁中间件 + 管理端点 | 合规安全基线。在产品上线时未封禁用户就上线是不可行的。 |
| **P0** | 方向 3 阶段 1：数据导出 | 合规 GDPR 数据可移植性。相对隔离。 |
| **P0** | 方向 3 阶段 2：账户删除 | 合规删除权利。技术上复杂，但上线前必须。 |
| **P1** | 方向 1：跨实例状态共享（Redis） | 生产 HA。如果生产仅部署 1 个副本，则非 P0。但对于 HA，需要它。 |
| **P1** | 方向 2：统一域事件 | 架构提升。修复当前审计/通知/outbox 之间的脱节。P0 依赖项：数据导出/删除需要 outbox。 |
| **P2** | 方向 5：群组消息传递 | 产品扩展。在核心合规和基础设施之后，这是扩大用户群的下一个方法。 |
| **P2** | 方向 3 阶段 3：物理清理（硬删除 + S3 GC） | 运维。只有一次软删除之后才需要。 |

### 5.2 阶段划分

**阶段 1（P0 合规 + 安全基线）—— 估计 2-3 周**

```
第 1-4 天：  封禁中间件 + admin API（方向 4）
  - user_bans 表 + 迁移
  - BanChecker + middleware
  - SSE 订阅上的封禁检查
  - ADMIN API（create/list/delete bans）+ audit_events
  
第 5-10 天： 数据导出（方向 3 阶段 1）
  - ExportUserData API
  - 跨所有实体的收集器
  
第 11-15 天： 账户删除（方向 3 阶段 2）
  - DeleteByOwner 添加到每个存储库
  - service 层的编排（lifecycle service）
  - outbox 事件用于异步级联
  - 取消 API + 宽限期
```

**阶段 2（P1 基础设施 + 架构）—— 估计 3-4 周**

```
第 1-10 天：  Redis 集成（方向 1）
  - 速率限制
  - 幂等性存储
  - SSO 缓存（token + user）
  - SSE Hub 跨实例（需要更深入的设计）
  
第 11-20 天： 统一事件（方向 2）
  - AuditHandler（outbox → audit_events）
  - 删除直接 auditRepo.Log() 调用
  - 将通知 dispatcher 从 switch 改为注册表模式
```

**阶段 3（P2 产品扩展）—— 估计 3-4 周**

```
第 1-15 天：  群组消息传递（方向 5）
第 16-20 天： 物理清理（方向 3 阶段 3）
  - 硬删除作业
  - S3 GC 集成  
```

### 5.3 风险点和缓解策略

| 风险 | 可能性 | 影响 | 缓解 |
|------|----------|--------|------------|
| **级联删除在大型数据集上超时** | 中 | 高 | 使用 outbox 驱动的异步作业 + 分批处理。为每张表添加 `delete_by_owner` 限量查询 |
| **Redis 宕机降级** | 低（如果 HA Redis） | 中 | 每个 Redis 集成必须优雅降级到内存/DB 回退。速率限制可以暂时放宽。幂等性可以暂时丢失。 |
| **群组消息传递推动新通知架构** | 中 | 中 | 群组消息传递对通知的要求（每个消息发送给 N 个收件人）可能会暴露当前 dispatcher 中的性能限制（顺序 Create() + 每个事件推送）。预先对 100 个成员进行基准测试。 |
| **封禁中间件导致 100% 路径上的延迟增加** | 低 | 低 | 在 Auth 之后但在路由之前进行封禁检查。缓存封禁状态（SSO 用户缓存或专用缓存），TTL 为 1 分钟。 |
| **统一事件系统回归** | 中 | 高 | 每个事件类型都需要写入路径 → outbox 行为不变的集成测试。测试双通道（事件 + 现有直接写入）以确保一致性。 |

---

## 关于你的具体观察的补充说明

> **方向二的 `user_bans` 中间件位置**

完全同意。在 `router.go` 中，当前中间件构建如下：

```go
authMW := []gin.HandlerFunc{middleware.Auth(validator)}
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter))
}
```

封禁中间件应**插入在两者之间**：

```go
authMW := []gin.HandlerFunc{
    middleware.Auth(validator),
    middleware.Ban(banChecker),    // <-- 在这里：Auth → Ban → RateLimit
}
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter))
}
```

为什么在这个顺序：`Auth` 设置 uid，`Ban` 检查 uid，`RateLimit` 使用 uid。如果封禁，在 `RateLimit` 之前拒绝，以在限流之前减少开销。

> **「基础封禁中间件（不实现管理端点）」这个权衡值得讨论**

强烈同意。没有管理 API 的封禁中间件：

1. **绕过审计**：`audit_events` 表是 append-only 的，包含 `AuditUserBlock` / `AuditUserUnblock` 等操作的管理操作审计日志。手动 SQL 封禁不会写入这些行
2. **没有审计线索**：谁封禁了用户、何时以及为什么？无法追溯
3. **运维反模式**：每次需要封禁用户时都直接操作 SQL 在生产中是一个危险信号

要么：**作为 P0 一起构建**管理端点 + 封禁中间件
要么：在配置中添加一个警告注释，说明“此中间件在管理端点实现之前处于非活跃状态”

> **方向三（数据导出+账户删除）的复杂度被轻微低估了**

你的评估是正确的。每个实体需要：

| 实体 | 存储库依赖 | 级联影响 |
|--------|-----------------|-------------------|
| 文件 | fileRepo + storageRefRepo + storage | 需要 S3/本地清理 |
| 消息 | msgRepo + outboxRepo | 删除相关内容 |
| 评论 | commentRepo + outboxRepo | 删除回复 |
| 通知 | notifRepo + unreadCounter | 为已删除用户清理 |
| 偏好 | prefRepo | 简单，无级联 |
| block | blockRepo | 简单，无级联 |
| 角色 | adminRepo | 为已删除用户清理 |
| 文件分享 | fileShareRepo | 取消分享已删除用户 |

由于跨多个存储库，事务编排是关键约束。使用 `outbox` 事件驱动级联删除将是这里正确的模式：

1. `service` 创建一个 `account.deletion.started` outbox 事件（持久化在同一个 `tx` 中作为状态转换）
2. relay 调度可重试的作业以处理每个实体（每批一个事件）
3. 每个作业在单独的 `tx` 中删除一批行
4. 当所有实体都处理完毕时，最终 `account.deletion.completed` 事件

这避免了在单个巨大的事务中锁定多张表。

> **性能优化 #2（SSE 断线续连索引）**

已验证：`idx_notifications_recipient_created` 在 `(recipient_uid, created_at DESC)` 上已覆盖 `WHERE recipient_uid = ? AND created_at > ?` 查询。这是正确的。不需要额外的索引。可以在任何设计文档中标注为“已验证”。

---

## 总结

aero-id 的架构很合理，并且经过了深思熟虑。已实施的核心基础设施（outbox、双传输、SSO 弹性、RBAC、审计）是生产级的。列出的 5 个方向代表了实际的架构缺口——而不是理论上的可能性——基于实际代码扫描。

**推荐的下一步行动**：从 **阶段 1 P0** 开始：封禁 + 数据导出 + 账户删除。这三者对于合规上线工作来说是不可协商的。任何需要在产品上线之前完成。如果时间有限，我建议：

1. **封禁中间件 + 完整管理 API**（~5 天）
2. **数据导出**（~5 天）
3. **账户删除**（~10 天，与 #2 并行工作可以重叠）

在 #3 开始之后，可以并行启动 **Redis + 统一事件系统**（阶段 2）——它们是独立的工作流。
