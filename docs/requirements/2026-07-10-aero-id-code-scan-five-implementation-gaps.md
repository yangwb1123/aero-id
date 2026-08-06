# aero-id — 基于代码扫描的五个实现缺口分析

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全局代码扫描（全部 `internal/` 包、22 份迁移 SQL、双传输层 handler、composition root），逐文件确认代码级证据  
> **前提**: 已有 `docs/requirements/` 下 18 份分析文档覆盖了约 90 个方向，本文档聚焦于**代码中实际存在的、未被系统分析的实现缺口**——即架构意图与实现之间的断层  
> **零代码变更**

---

## 核心发现

aero-id 的完成度极高：几乎所有 ROADMAP 方向和 post-ROADMAP 生产强化都已交付。但代码扫描揭露了 5 个**架构意图已设计/部分实现但未完成**的缺口——它们不是新功能请求，而是已有代码的自然延伸被截断了。用工程术语说：这是**设计意图与实现之间的 Delta**。

| # | 缺口 | 代码证据类型 | 影响范围 |
|---|------|-------------|---------|
| 1 | SSE Hub 是单实例瓶颈 — 缓存 16 静默丢弃，无可观测性，无跨实例能力 | 代码分析 | 运维/可扩展性 |
| 2 | `user.ban` 权限已明确设计但无对应端点、无存储、无 handler | 常量+迁移 vs handler 清单 | 安全/功能 |
| 3 | 文件分享为永久授权，无过期时间，无分享访问审计 | 迁移 DDL + service 分析 | 安全/合规 |
| 4 | 通知系统完全缺少邮件等外带送达渠道 | 全仓 grep | 用户留存/运营 |
| 5 | 跨实例状态同步基础设施为零 — Hub、Limiter、Cache 各自为政 | composition root 分析 | 可扩展性/可靠性 |

下面逐一展开。

---

## 缺口一：SSE Hub 是单实例瓶颈，无可观测性，无跨实例能力

### 代码证据

**位置**: `internal/hub/hub.go`

**发现 1 — 固定缓冲区 + 静默丢弃**（第 46 行、第 94 行）：
```go
// hub.go:46-47
// buffer of 16 events; slow consumers will silently drop events rather than
// block Publish.
```
```go
// hub.go:93-97
select {
case ch <- evt:
default:
    // subscriber is too slow; drop rather than block
}
```
每次 `Publish` 调用时，若订阅者的 16-event channel 已满，事件**静默丢弃**——不记录日志，不增加计数器，调用者（`NotificationDispatcher`）不知道事件已丢失。

**发现 2 — 无连接度量**：
- `Hub` 结构体只有 `subs sync.Map`、`nextID atomic.Int64`、`closed atomic.Bool`
- 无 `gauge` 暴露当前连接数、按 uid 的连接分布、推送速率
- `/metrics` 端点上完全看不到 SSE Hub 的健康状态

**发现 3 — 无跨实例能力**（`adapter.go`）：
```go
// hub/adapter.go
func (h *Hub) Publish(uid, eventType, data string, eventID int64) {
    h.pub(uid, SSEEvent{ID: eventID, Type: eventType, Data: data})
}
```
`hub.pub` 仅在本进程的 `sync.Map` 中查找订阅者。在水平扩展部署中，若:
- 实例 A 处理了 `message.sent` 事件 → outbox dispatcher → `Hub.Publish`
- 实例 B 持有同一用户的长连接 SSE

则事件**永远不会到达实例 B 上的客户端**。

**发现 4 — 无连接生命周期管理**：
`EventsHandler.Stream` 在 `defer unsub()` 清理通道，但：
- 无 `max_connection_duration`（SSE 连接被代理/浏览器保持，理论上可无限期存活）
- 无 `inactivity_timeout`（客户端静默期过后应主动断开）
- 无连接数上限保护（防止单 uid 恶意创建大量连接耗尽 Hub 内存）

### 为什么需要修复

1. **水平扩展阻塞器**：SSE 是通知系统的核心 UX 通道。当前架构强制所有通知流量路由到单一实例，或承担通知丢失的风险。
2. **生产可观察性缺口**：运维人员无法知道有多少活跃 SSE 连接、丢弃了多少事件、哪些 uid 的连接通道阻塞。这是潜在的盲点事故源。
3. **16 槽位的硬编码**：对于高频通知场景（批量消息接收、批量评论回复），16 槽极易溢出。Channel 大小应是可配置的，且在溢出时应当有度量和降级策略。

### 建议方向

- 为 SSE Hub 增加连接数、丢弃数、按类型推送次数的 Prometheus 度量
- 引入可配置的缓冲区大小和溢出行为（阻塞 vs 丢弃 vs 回压）
- 为跨实例场景引入插拔式 pub/sub 后端（Redis Pub/Sub、NATS），当前 in-memory hub 作为单实例降级模式
- 增加 SSE 连接生命周期参数：最大持续时间、不活动超时、每 uid 最大连接数
- 在 `/healthz`/`/readyz` 中反映 Hub 的健康状态

---

## 缺口二：`user.ban` 权限已明确设计但无对应端点、无存储、无 handler

### 代码证据

**位置 1 — `internal/domain/admin.go`**（第 9 行）：
```go
const (
    PermContentModerate = "content.moderate"
    PermAuditRead       = "audit.read"
    PermUserBan         = "user.ban"         // ban/unban users
    PermSystemConfig    = "system.config"
)
```

**位置 2 — `migrations/000022_admin_roles.up.sql`**（第 22 行）：
```sql
INSERT INTO roles (code, name, permissions) VALUES
    ('moderator', 'Moderator',   ARRAY['content.moderate', 'audit.read']),
    ('admin',     'Administrator', ARRAY['content.moderate', 'audit.read', 'user.ban', 'system.config'])
ON CONFLICT (code) DO NOTHING;
```

管理员角色的权限列表包含 `'user.ban'`——设计意图明确。然而**全仓扫描后未发现**：
1. 无 `banned_users` 表或 `users` 表上的 `status` 列
2. 无 `AdminHandler` 上的 `POST /v1/admin/ban`/`POST /v1/admin/unban` 端点
3. 无 gRPC 对应的 `BanUser`/`UnbanUser` RPC
4. 无 `repository` 层的 `Ban`/`Unban`/`IsBanned` 方法
5. 无 `AdminRepository` 接口上的 ban 相关方法（当前只有 `HasPermission` 和 `ListAudit`）

**对比**: `PermContentModerate` 对应的评论举报处理（`comment_reports` 表 + 审核 resolve/dismiss 端点）已实现。`PermAuditRead` 对应的 `GET /v1/admin/audit-log` 已实现。只有 `user.ban` 是**孤立的权限——有常量、有种子数据，但无可执行的代码路径**。

### 为什么需要修复

1. **安全模型的完整性断裂**：权限系统中最严肃的权限（封禁用户）是 stub。运营团队可以查询审计日志、可以审核评论，但无法执行封禁——必须绕过 aero-id 直接操作数据库或依赖 SSO 的封禁能力（而 SSO 可能没有封禁概念）。
2. **合规风险**：当检测到滥用行为时，没有程序化的封禁路径意味着恢复手段延迟或缺失。
3. **审计断裂**：没有封禁端点意味着封禁操作不会进入 `audit_events` 表。所有其他敏感操作有审计追踪，封禁没有——这是最需要审计的操作为什么反而没有？

### 建议方向

- 新增 `banned_users` 表（`uid TEXT PRIMARY KEY, reason TEXT, banned_by TEXT, banned_at TIMESTAMPTZ, expires_at TIMESTAMPTZ`）
- 新增 `HasPermission(actor, "user.ban")` 守卫的管理员封禁/解封端点
- 封禁应级联影响：被封禁用户的消息/评论可见性、通知创建、文件分享授权
- 所有封禁/解封操作写入 `audit_events` 表

---

## 缺口三：文件分享为永久授权，无过期时间，无分享访问审计

### 代码证据

**位置 1 — `migrations/000006_create_file_shares.up.sql`**：
```sql
CREATE TABLE file_shares (
    file_id      UUID NOT NULL,
    grantee_uid  TEXT NOT NULL,
    owner_uid    TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (file_id, grantee_uid)
);
```
- **无 `expires_at` 列**——分享授权永久有效
- **无 `max_access_count` 列**——无法限制访问次数
- **无 `permission` 列**——所有被分享者拥有相同权限（只读），无法区分只读/可下载/可再分享

**位置 2 — `internal/file/service_share.go`**：
```go
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
    // ...
    if err := s.shares.Grant(ctx, id, f.OwnerUID, grantees); err != nil {
        // audit log is written here for who was GRANTED access
    }
}
```
授权时写入审计事件。但**分享文件的下载/访问本身不被审计**——没有机制记录"被授权者 X 在时间 T 下载了文件 Y"。

**位置 3 — 全仓扫描**：
`internal/file/repository_share.go` 中 `IsSharedWith` 仅检查是否存在永久的分享授权记录。没有访问计数，没有到期检查，没有访问日志。

### 为什么需要修复

1. **安全最佳实践缺失**：时间受限的文件分享是所有现代文件平台（Google Drive、Dropbox、AWS S3 Presigned URL）的标准能力。永久授权意味着任何被分享的用户理论上可无限期访问文件，即使分享者已删除被分享者的记录（Revoke 是手动操作，无法保证及时）。
2. **合规需求**：在合规场景中（合同共享、机密文档），文件分享必须有到期机制和访问审计追踪。
3. **产品成熟度差距**：文件分享是 aero-id 的核心功能之一。没有过期时间的分享在面向 B 端场景中是不可接受的。

### 建议方向

- 迁移 000006 增加 `expires_at TIMESTAMPTZ`、`max_access_count INT` 列（NULL = 不限）
- `Service.ShareFile` 接受可选过期参数，校验后将过期时间写入 DDL
- `DownloadURL` 路径中检查：若文件是通过分享授权访问，写入 `audit_events`（`file.share.access`），并检查是否已达次数上限或已过期
- 后台协程定期扫描已过期的 `file_shares` 记录并自动删除（或在读取路径中惰性检查）

---

## 缺口四：通知系统完全缺少邮件等外带送达渠道

### 代码证据

**位置 1 — 全仓 `grep -rn "email\|smtp\|mail\|sendmail" --include="*.go" .`**：
```
无结果
```

**位置 2 — `internal/domain/notification.go`**：
```go
type NotificationPreference struct {
    UID              string
    NotificationType string
    Enabled          bool
    QuietHoursStart  string
    QuietHoursEnd    string
    Timezone         string
    CreatedAt        time.Time
    UpdatedAt        time.Time
}
```
偏好设置只有 `Enabled` 和静默时段——**无 `Channel` 字段**（如 `email` / `push` / `sms`）。用户无法选择通知通过哪种渠道送达。

**位置 3 — `internal/notification/dispatcher.go`**：
Dispatcher 实现 `outbox.Handler` 接口，其 `Handle` 方法只执行以下操作：
1. 将 outbox 事件转化为 `domain.Notification` 结构体
2. 检查 block 关系
3. 写入 `notifications` 表
4. 调用 `pusher.Publish`（SSE 实时推送）

**没有任何分支尝试通过 SMTP、FCM/APNs 或任何其他渠道送达通知**。

**位置 4 — `internal/app/app.go`** composition root：
Dispatcher 构造时所有选项均已展示——`WithDispatcherBlockChecker`、`WithDispatcherPreferenceChecker`、`WithDispatcherRateLimiter`、`WithDispatcherCollapse`、`WithDispatcherPusher`——全部是 in-app 级别的选项，无任何外带渠道选项。

### 为什么需要修复

1. **用户留存基线**：没有邮件通知意味着用户必须主动打开应用才能知道有新消息或评论回复。这在移动端消息类产品中是不可接受的——系统消息必须能主动触达用户。
2. **SSE 的天然局限**：SSE 需要客户端保持长连接。移动端浏览器/App 在后台会断开连接，WebSocket 同样。邮件是唯一普遍可用的异步送达渠道。
3. **Outbox 架构已准备好**：事务性 outbox 的设计本意就是支持多消费者。当前所有 outbox 事件仅流向 notification dispatcher + (可选的) webhook。增加一个 Email handler 作为 `outbox.Chain` 的第三个链接是架构上的自然延伸。

### 建议方向

- 在 `NotificationPreference` 中增加 `channels TEXT[]` 字段（枚举 `in_app`、`email`、`push`）
- 在 outbox 处理链上增加 `EmailDispatcher`，由独立的 SMTP 工作者池驱动
- 将 SSE Publish 从 Dispatcher 内部分离，改为通过 channel 选择器分发：根据用户偏好判断走 SSE 还是邮件还是两者都走
- 异步邮件发送应通过独立的 worker goroutine 池（非请求路径）以保证不影响通知写入延迟
- 设计邮件模板系统，区分不同类型的通知生成合适的邮件正文

---

## 缺口五：跨实例状态同步基础设施为零

### 代码证据

**位置 1 — SSE Hub**（已在缺口一中分析）：
`internal/hub/hub.go` 使用 `sync.Map` 作为订阅注册表。所有连接和推送在同一进程内完成。

**位置 2 — Rate Limiter**（`internal/pkg/ratelimit/ratelimit.go`）：
```go
// 源码声明明确
limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
```
`README.md` 的注释可见设计约束："per-instance (distributed limiting would need shared state)"。
这意味着 N 个实例等于 N 倍速率上限——用户可以发送 N 倍于预期的请求量。

**位置 3 — SSO Cache**（`internal/sso/token_cache.go` + `user_cache.go`）：
Token 和用户缓存各自使用 `sync.Map` + TTL sweeper。冷启动时每个实例独立缓存穿透到 SSO。
在 N 实例滚动部署期间，新实例的缓存为全空，导致 SSO 调用量暂时飙升 N 倍。

**位置 4 — Idempotency Store**（`internal/idempotency/store.go`）：
Idempotency 键存储在 Postgres 中，天然跨实例一致。这是**唯一**一个正确支持水平扩展的跨实例状态。
其他三个（Hub、Limiter、Cache）都是 per-instance 的。

### 当前代码的隐含假设

```go
// internal/app/app.go — composition root 中所有 goroutine 的启动顺序
// 假设一个实例处理所有请求
go func() {
    a.grpcServer.Serve(a.grpcLis)
}()
go func() {
    a.httpServer.ListenAndServe()
}()
```
没有服务发现、没有实例标记、没有基于实例 ID 的分区逻辑。所有后台组件（relay、GC sweeper、rate limit cleaner、idem cleaner）在不同实例上是重复且冲突的——如果启动两个实例，它们都会轮询 outbox、都会运行 GC sweeper，导致重复处理和竞争条件。

### 为什么需要修复

1. **水平扩展是生产部署的基本要求**：单实例在负载高峰、滚动更新、AZ 故障场景下都是单点故障。当前架构中的三个 per-instance 状态组件使得水平扩展**不可能正确实现**。
2. **Outbox 和 GC 的重复执行**：两个实例会各自消费 outbox 事件（导致重复通知）和各自运行 GC sweeper（导致资源竞争）。这两个组件必须通过 leader election 或分区才能安全运行。
3. **用户规模增长的自然瓶颈**：50 并发连接、20 RPS 限流、本地缓存——这些数字在规模增长一个数量级后都会成为瓶颈。

### 建议方向

这是一个架构层面的系统性工程，建议分阶段：

**Phase 1 — 可观察性先行**：
- 为 Hub、Limiter、Cache 增加实例级别的度量（当前连接数、限流命中率、缓存命中率）
- 在 `/readyz` 上暴露实例 ID 和组件健康状态

**Phase 2 — 后台组件 leader election**：
- Outbox relay 和 GC sweeper 通过 Postgres 乐观锁或 `pg_advisory_lock` 实现 leader election
- 仅 leader 实例消费 outbox 事件和执行 GC
- 新实例启动时自动竞争 leader，失败时降级为非 leader 模式

**Phase 3 — 跨实例状态共享**：
- Rate limiter 增加 Redis 后端选项（当前 in-memory 作为降级模式）
- SSO cache 增加 Redis 后端选项（TTL 一致，减少冷启动穿透）
- SSE Hub 增加 Redis Pub/Sub 后端（事件通过 channel 广播到所有实例）

**Phase 4 — 无状态化改造**：
- 目标：实例可以随时被终止和启动而不影响服务
- 所有 per-instance 状态要么外移到共享存储，要么被设计为可丢失（best-effort）
- 只有 leader election 状态保留在 Postgres 中

---

## 缺口之间的关系与依赖

```
缺口五 ── 跨实例状态同步
  ├── 直接阻塞缺口一（SSE Hub 跨实例广播）
  ├── 影响缺口二（封禁状态需要全局一致）
  └── 不相关：缺口三（文件分享过期在 DB 中，天然跨实例一致）
       └── 不相关：缺口四（邮件通过 SMTP，无状态）

缺口二（user.ban）和缺口三（分享过期）都是功能完整性缺口
缺口四（邮件渠道）是一个独立的渠道扩展
```

修复优先级建议：

| 优先级 | 缺口 | 理由 |
|--------|------|------|
| P0 | **缺口五 Phase 1-2**（leader election + 度量） | 生产水平扩展的前提条件 |
| P0 | **缺口二**（user.ban 端点） | 安全模型的完整断裂，实现成本最低 |
| P1 | **缺口三**（分享过期+审计） | 文件是核心资产，安全合规高权重 |
| P1 | **缺口一 Phase 1**（Hub 可观测性） | 运维盲点，修复成本低 |
| P2 | **缺口四**（邮件渠道） | 产品体验提升，但依赖缺口五（leader）的 Phase 2 |
| P2 | **缺口一 Phase 3**（跨实例 Hub） | 高复杂度，依赖缺口五的 Phase 3 |

---

## 附录：与已有 18 份分析文档的交叉确认

为本文档每项缺口在已有文档中的覆盖情况进行了确认：

| 已有文档 | 涉及缺口一 | 涉及缺口二 | 涉及缺口三 | 涉及缺口四 | 涉及缺口五 |
|----------|-----------|-----------|-----------|-----------|-----------|
| architect-perspective-extensions.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| architecture-extensions.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| architecture-migration-and-systemic-gaps.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| deep-extension-analysis.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| edge-cases-and-performance.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| final-blindspots.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| five-uncovered-high-value-directions.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| genuinely-uncovered-directions.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| high-value-extensions.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| operational-architecture-gaps.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| operational-blindspots-and-resilience.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| product-directions.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| remaining-architectural-gaps.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| remaining-high-value-directions.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| senior-architect-perspective.md | ❌ | ❌ | ❌ | ❌ | ❌ |
| truly-uncovered-architecture-directions.md | ❌ | ❌ | ❌ | 部分(移动推送) | ❌ |
| untapped-directions.md | ❌ | ❌ | ❌ | 部分(移动推送) | ❌ |
| expansion-directions.md | ❌ | ❌ | ❌ | ❌ | ❌ |

**说明**：
- 「部分」表示该文档提及了类似概念（如移动推送、多副本部署）但未从本缺口的角度深入分析
- 「❌」表示该方向的系统性分析在该文档中不存在
- 缺口四（邮件渠道）与已有文档中的「移动推送」是不同的范畴——前者是 SMTP 邮件，后者是 FCM/APNs 原生推送，两者的基础设施、限流策略、模板引擎完全不同

---

## 结论

这五个缺口有一个共同特征：**它们的代码足迹极小，但架构影响极大**。修复它们不需要大规模重写——大多数是「补充半截实现」的工作量：

- 缺口二（ban 端点）大约 50-80 行新增代码 + 1 份迁移
- 缺口三（分享过期）大约 100-150 行新增 + 1 份 ALTER TABLE 迁移
- 缺口一（Hub 可观测性）大约 50 行度量代码
- 缺口四（邮件）约 300-400 行（SMTP 客户端 + 模板 + 配置）
- 缺口五（跨实例）是唯一的大工程，但可分成 3 个阶段，每个阶段独立交付

从架构完整性和生产就绪度来看，建议优先关闭缺口二和缺口一（Phase 1），它们的安全和运维价值最高，实现成本最低。
