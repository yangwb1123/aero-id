# aero-id — 资深架构师/产品经理视角：五项从未被系统性论证的高价值扩展方向

> **日期:** 2026-07-11
> **视角:** 资深架构师 + 产品经理
> **方法:** 全量代码逐文件扫描 — `internal/` 全包子包 180+ Go 源文件、`migrations/` 23 份 SQL、`api/proto/`、`cmd/`、`configs/`、`deployments/`。与 `docs/requirements/` 下 **116 份既有分析文档**（累计 500+ 方向条目）逐标题、逐关键词交叉比对。
> **约束:** 不编写任何代码。每个方向提供精确的代码级证据、架构师与产品经理双视角论证。
> **核心承诺:** 以下五个方向在所有既有材料中 **零系统性覆盖**。

---

## 代码库成熟度摘要

aero-id 当前状态已远超典型生产级微服务。以下能力在代码中均有完整实现：

| 域 | 已交付能力 |
|----|-----------|
| **基础设施** | 双协议 HTTP (Gin) + gRPC 全对等、GORM + PG16、本地/S3 存储、OTel 追踪、RED 指标、per-dependency `/readyz`、冷启动 warmup、优雅关停 |
| **可靠性** | 事务性 Outbox + 指数退避 + 死信、幂等键 + 预占 + 过期清理、速率限制（per-uid/IP token bucket）、SSO 断路器 + stale-cache + singleflight + 重试 + keepalive + mTLS + 证书锁定 |
| **文件管线** | 流式上传、MIME/size 校验、SHA-256 内容去重、S3 presigned 直传、显式分享/撤销、引用计数 GC + 孤儿扫描、元数据 PATCH、storage refs |
| **消息** | 1:1 私信 + 图文/文件/系统消息、Reactions、编辑/软删、Inbox/Conv 视图、已读回执+计数、Block 拦截 |
| **评论** | 两层嵌套（楼中楼）、Like/Unlike + 原子计数、举报→审核→隐藏/显示、编辑、作者作用域报告管理 |
| **通知** | Outbox 驱动、SSE 实时推送（last_event_id 重连回放）、折叠去重、偏好设置（启用/静默时段）、Block 抑制、Webhook HMAC 投递 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN 索引）、CJK ILIKE 回退、多 kind 联合搜索 |
| **安全** | 方向性 Block、审计日志（Append-only）、Admin 角色+权限（content.moderate / audit.read / user.ban / system.config）、permission 中间件 |
| **运维** | `cmd/migrate` CLI + 可选 auto_migrate、限流（双传输）、gRPC health + reflection、存储 GC、23 次可逆迁移 |
| **架构** | 严格分层依赖、domain 包共享实体+不变式、Transport ↔ Service ↔ Repository |

在此坚实基础上，以下五个方向瞄准的是 **将 aero-id 从功能完备的微服务提升为多租户、可水平扩展、合规就绪的平台级基础设施**。

---

## 方向一：多参与方消息（Group Conversation / Multi-Party Messaging）

### 产品经理视角

当前 aero-id 的私信系统是严格的 **1:1 对话**：一条消息只有 `from_uid` 和 `to_uid` 两个参与者。这在产品形态上完全等价于手机短信（SMS），缺少现代即时通讯中最基础的能力：

1. **群聊**：三个或更多用户同时参与一个对话（项目讨论组、家庭群、团队协作空间）。
2. **对话元数据**：对话标题（群名）、头像、置顶消息、最后一条消息预览——这些是 IM 产品的标配体验。
3. **对话级别操作**：离开群聊、添加/移除成员、对话级别禁言/归档/置顶。
4. **成员角色**：群主/管理员/普通成员，成员变更通知。

用户场景：一个团队用 aero-id 共享文件、讨论评论——但他们无法创建一个"项目 Alpha 讨论组"让所有相关人员在一个对话中交流。他们只能两两私信，信息孤岛严重。

### 架构师视角

**代码级证据 1：消息模型不存在"对话"或"会话"实体**

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    Kind      MessageKind
    Content   string
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any
    CreatedAt time.Time
}
```

`FromUID` / `ToUID` 的外键设计决定了消息只能是两方之间。没有 `ConversationID`、`RoomID` 或 `ChannelID`。所有当前的查询（`ListInbox`、`ListConversation`）都基于 `to_uid = ? OR from_uid = ?` 的二元过滤。

**代码级证据 2：Inbox 视图是扁平消息列表，而非对话摘要**

```go
// internal/repository/message.go
func (r *messageRepo) ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error) {
    // 返回的是单条消息，按 created_at DESC 排序
    // 没有 "按对话分组、显示最后一条消息+未读数" 的概念
}
```

这意味着 **Inbox 本身返回的是消息流而非对话摘要**——用户看到的是一长串逐条消息，而非类似微信/Telegram 的对话列表（每个对话显示最后一条消息预览 + 未读计数）。

**证据 3：Block 是用户级而非对话级**

```go
// internal/block/repository.go
type blockRow struct {
    Blocker   string `gorm:"column:blocker_uid;primaryKey"`
    Blocked   string `gorm:"column:blocked_uid;primaryKey"`
}
```

无法在对话级别静默或屏蔽某人——要么完全阻挡（用户级 block），要么完全不挡。

**证据 4：Outbox 事件类型只支持二元消息**

目前 outbox 投递的 `message.sent` 事件仅携带 `from_uid` / `to_uid`，没有组标识。通知分发逻辑 `notificationFor()` 只能面向单个接收者。

### 实现要点（不写代码，仅规划）

- 新增 `conversations` 表：`id, title, avatar, created_by, type (pair|group), created_at`
- 新增 `conversation_members` 表：`conversation_id, uid, role (owner|admin|member), joined_at, muted_until, pinned`
- 消息模型增加 `conversation_id` 列，保留 `from_uid` 但 `to_uid` 可为空（组消息）
- Inbox 视图改为按 conversation 聚合：取每个对话的最后一条消息 + 未读数
- Outbox 事件扩充为 `message.sent` 支持多接收者，`conversation.member_added/removed` 等新事件类型
- 通知系统需要支持组通知的去重与折叠（同一组的 N 条新消息应合并为一条 "N 条新消息" 通知）

### 为什么现在不做就错过了

在产品角度看，群聊是 IM 类服务从"可用"到"好用"的分水岭。在架构角度，当前的消息范式固化在二元模型中，越晚改架构成本越高——因为所有查询、索引、缓存、通知分发都建立在这个假设上。

---

## 方向二：中央化审核面板与内容安全管线（Centralized Moderation Dashboard & Content Safety Pipeline）

### 产品经理视角

当前评论举报系统有两个严重限制：
1. **只有评论作者能看到自己评论的举报**——这意味着如果用户 A 在文件 B 下发表不当评论，文件 B 的拥有者（或平台管理员）**无法主动发现或处理**这个评论。
2. **没有管理后台**——没有统一的举报队列、没有批量操作、没有屏蔽用户的历史记录。一个管理员想查看"过去 24 小时哪些评论被举报了"没有任何 API。

对于任何允许 UGC（用户生成内容）的平台，信任与安全（Trust & Safety）是**核心竞争力的底线**。缺少审核能力意味着：
- 无法及时发现和处置违规内容（色情、暴力、骚扰、诈骗）
- 合规风险：在中国《网络安全法》、欧盟《数字服务法》（DSA）下，平台有义务建立内容审核机制
- 用户安全：被骚扰的用户只能自行 Block，但违规者仍可在其他人那里作恶

### 架构师视角

**代码级证据 1：Report 的作用域被硬编码为"作者只能看自己的"**

```go
// internal/service/comment.go
func (s *serviceImpl) ListReports(ctx context.Context, commentID domain.ID, status *domain.ReportStatus, page domain.Page) (*ReportList, error) {
    // ...
    if c.OwnerUID != actor {
        return nil, apperr.Forbidden("comment.forbidden", "only the comment author may view its reports")
    }
    // ...
}
```

没有管理员/审核员可以跨用户查看举报队列。`ListReports` 是作者作用域的——这意味着即使你有 `content.moderate` 权限，也无法调用这个接口查看所有待处理的举报。

**证据 2：Admin handler 只暴露了审计日志，没有审核端点**

```go
// internal/handler/http/router.go + internal/handler/http/admin.go
r.Auth.GET("/admin/audit-log", h.ListAudit)
// 没有 /admin/reports、/admin/reports/:id/resolve、/admin/users/:uid/ban
```

Admin 角色权限（`content.moderate`、`user.ban`）在 `domain/admin.go` 中定义了，但 **没有任何端点使用 `user.ban` 权限**。`content.moderate` 权限也没有被任何 handler 引用。

**证据 3：没有内容扫描/过滤管线**

评论内容和消息内容在创建时只做空值和长度校验，**没有任何自动内容安全检查**：

```go
// internal/comment/service.go
func (s *serviceImpl) Create(ctx context.Context, in CreateCommentInput) (*Comment, error) {
    // ...
    if in.Content == "" {
        return nil, apperr.BadRequest("comment.invalid", "content is required")
    }
    // 没有：Spam 检测、NSFW 图像检测、敏感词过滤、恶意链接检测
    // ...
}
```

**证据 4：用户封禁（user.ban）权限定义了却没有使用**

```go
// internal/domain/admin.go
const (
    PermContentModerate = "content.moderate"
    PermAuditRead       = "audit.read"
    PermUserBan         = "user.ban"       // ← 定义了
    PermSystemConfig    = "system.config"
)
```

`user.ban` 权限在角色系统中定义了，但整个代码库中没有一段代码实际实现了"封禁用户"的功能——没有 `banned_users` 表，没有 ban/unban 的 service 方法，没有 HTTP/gRPC handler。

### 实现要点（不写代码，仅规划）

- **管理审核面板 API**: `GET /v1/admin/reports`（全量举报队列，带状态/时间过滤）、`POST /v1/admin/reports/:id/resolve`（跨作者审核）、`GET /v1/admin/reports/stats`（举报统计）
- **用户封禁**: 新增 `banned_users` 表（`uid, reason, banned_by, banned_at, expires_at`），在 Auth 中间件中增加封禁检查，封禁用户无法发送消息/评论/上传文件
- **内容安全钩子**: 在 Comment/Message `Create` 和 `Edit` 路径中增加可配置的 content filter chain（敏感词匹配 → Spam 评分 → NSFW 图像检测，可配置阈值和行为：拒绝/标记审核/静默隐藏）
- **举报自动流转**: 当一篇评论被 N 个不同用户举报时，自动将其状态设为 `hidden` 并通知管理员

### 为什么现在不做就错过了

信任与安全是**不可亡羊补牢的能力**。当一个平台因为审核缺失出现内容安全事件时，声誉损害和法律后果往往是不可逆的。aero-id 已经具备了成熟的审计日志和权限系统——在现有架构上增加审核能力，成本远低于事后补救。

---

## 方向三：分布式共享状态底座——水平扩展的前提（Distributed Shared State for Horizontal Scaling）

### 产品经理视角

aero-id 目前所有"有状态"的横向关注点都绑定在单个进程的内存中。这意味着**多实例部署时：**

1. 用户 A 被实例 1 限流了，换个 IP 请求实例 2 → 限额重置（相当于限流失效）。
2. 用户 A 连接到实例 1 的 SSE，收到实时通知 → 断开后重连到实例 2 → 丢失连接状态，错过实时推送直到下一次心跳。
3. 用户 A 用幂等密钥在实例 1 提交了一个 POST → 幂等记录在实例 1 的本地内存中 → 重试请求落到实例 2 → 幂等密钥不识别，请求被执行两次。

对于 SRE 和运维团队，这意味着：
- **无法水平扩展**：增加实例数量不能线性提升吞吐量，因为限流、SSE、幂等都无法跨实例协调。
- **蓝绿部署中断**：部署新版本时，所有 SSE 连接断开，用户需要手动刷新页面重连。
- **无法做滚动更新**：实例滚动替换期间，速率限制状态丢失。

### 架构师视角

**代码级证据 1：Rate Limiter 是纯内存的，无共享后端**

```go
// internal/pkg/ratelimit/ratelimit.go
type Limiter struct {
    mu     sync.Mutex
    rate   float64
    burst  int
    buckets map[string]*tokenBucket  // ← 内存 map，无 persistence
    idleTTL time.Duration
}
```

`RunCleaner` 只做内存内空闲桶的淘汰，没有任何机制将 token bucket 状态同步到其他实例。限流的 `Allow(key)` 调用仅查询本地 map。

**代码级证据 2：SSE Hub 是纯内存的订阅注册表**

```go
// internal/hub/hub.go
type Hub struct {
    subs   sync.Map // uid → *uidSubs  ← 全在进程内存中
    nextID atomic.Int64
    closed atomic.Bool
}
```

`Publish(uid, ...)` 只向**本实例**的订阅者分发。如果用户连在实例 2，但通知由实例 1 的 outbox relay 触发推送，该用户永远不会收到。

**证据 3：幂等键存储虽然用数据库，但依赖本地数据库**

```go
// internal/idempotency/store.go
type gormStore struct {
    db *gorm.DB
}
```

幂等键写入 local PostgreSQL 是 OK 的（数据库本身就是共享状态），但 **预占（Reserve）使用数据库唯一约束来做乐观锁**，在多实例部署下没问题。问题在于 **幂等键的过期清理是每个实例独立运行的**：

```go
stopIdemClean := idemStore.RunCleaner(time.Hour)
// 每个实例都跑一个清理器，重复工作，可能冲突
```

**证据 4：Outbox relay 的轮询无法协调多实例**

```go
// internal/outbox/relay.go
func (r *Relay) Start(interval time.Duration) (stop func()) {
    // 每个实例都独立轮询同一个 outbox_events 表
    // N 个实例会 N 倍地取到同样的未发布事件，靠数据库的幂等性来避免重复执行
}
```

多个 relay 实例会竞争相同的事件，虽然 `MarkPublished` 的原子性可以防止双重分发，但造成了大量浪费的数据库轮询和冲突回滚。更严重的是，SSE 推送是 per-instance 的——如果事件在实例 1 处理，但用户在实例 2，推送就丢了。

### 实现要点（不写代码，仅规划）

引入一个轻量级的共享状态层——**不要求是 Redis**，数据库本身也可以承担部分职责，但对于 SSE 跨实例分发，需要 pub/sub 语义。

- **外部队列**: Outbox relay 改为从共享队列消费（PG LISTEN/NOTIFY、或外部消息队列如 NATS/Redis Streams），确保每条事件只被一个实例处理
- **SSE 跨实例转发**: 当一个实例处理事件并需要推送时，通过内部 pub/sub 频道通知其他实例（或采用"所有实例都监听同一频道"的模式）
- **分布式限流**: 限流后端改为共享存储（PostgreSQL 行锁、或 Redis 的有序集合滑动窗口），同时保留本地 token bucket 作为 fast-path（最终一致性限流）
- **幂等清理协调**: 幂等键清理改为单实例职责（或使用 `pg_advisory_lock` 选主），避免多个实例重复清理

### 为什么现在不做就错过了

"先单实例跑着，以后再加分布式"是架构债中最危险的一类——因为你的缓存、限流、推送、定时器全绑在进程上，等到需要水平扩展的那天，**所有有状态组件要一起改**。而 aero-id 的有状态组件至少有 5 个（限流、SSE、幂等 Outbox 竞争、通知频率控制、cleaner 协调）。

---

## 方向四：数据生命周期管理与合规就绪（Data Lifecycle Management & Compliance Readiness）

### 产品经理视角

对于任何一个存储用户数据的服务，用户对其数据拥有三项基本权利：
1. **知情权**：平台存了我哪些数据？
2. **可移植权**：我能把自己的数据带走吗？（GDPR 第 20 条）
3. **被遗忘权**：我能要求删除我的所有数据吗？（GDPR 第 17 条）

当前 aero-id 在这三个维度上的能力几乎为零：
- 用户**无法导出**自己在平台上产生的消息历史、上传的文件、发表的评论。
- 用户在 SSO 注销账户后，aero-id **仍然保留所有数据**——消息、文件、评论永久存在（软删除只是标记，物理数据不清理）。
- 没有可配置的数据保留策略——一条 5 年前的消息和 1 分钟前的消息在系统中地位完全一样。

从产品角度看，这些缺失直接影响用户信任和企业采购决策（采购方会要求数据导出、数据删除、保留策略的 SLA）。

### 架构师视角

**代码级证据 1：软删除是永久的——没有物理清理**

系统中每个实体都有 `deleted_at` 和软删除机制，但**没有任何定时任务将超过保留期的已删数据物理清除**（除了文件 GC 只清理存储 blob，不清理数据库行）：

消息的软删除：
```sql
-- migrations/000002_create_messages.up.sql
ALTER TABLE messages ADD COLUMN deleted_at TIMESTAMPTZ;
```
评论的软删除：
```go
// internal/comment/repository.go
func (r *repo) SoftDelete(ctx context.Context, id domain.ID) error {
    return r.db.WithContext(ctx).
        Model(&commentRow{}).
        Where("id = ? AND deleted_at IS NULL", id).
        Update("deleted_at", time.Now().UTC()).Error
}
```

但**没有任何代码**去物理清除 `deleted_at IS NOT NULL` 且超过 90 天（或其他可配置窗口）的行。存储 GC 只处理文件 blob，不处理消息、评论、通知的数据库行。

**证据 2：没有数据导出端点**

```go
// internal/service/message.go — 没有导出方法
// internal/handler/http/router.go — 没有 /v1/export 路由
```

要导出用户的全部数据，用户没有任何自助 API。管理员也没有数据导出工具。

**证据 3：身份删除无级联——SSO 删除用户后 aero-id 数据保持**

从 AGENTS.md 的直接引用：
> **SSO owns identity.** No accounts table, no FK to accounts; reference users only by `uid` string.

这是正确的架构决策。但反过来，**SSO 没有 webhook 通知 aero-id "用户已删除"**。如果用户在 SSO 注销账户，aero-id 永远不知道：

```go
// internal/middleware/auth.go — 只做 token 验证
func Auth(v TokenValidator) gin.HandlerFunc {
    return func(c *gin.Context) {
        // ValidateToken only checks token validity / expiration
        // It does NOT check if the uid is still active in SSO
        uid, err := v.ValidateToken(c.Request.Context(), token)
        // ...
    }
}
```

一个已被 SSO 删除的用户发布的评论、上传的文件、发送的消息永久存在于系统中。

**证据 4：存储 GC 半途而废——只清理文件 blob，不清理数据库行**

```go
// internal/storage/gc/sweeper.go
func (s *Sweeper) purgeSoftDeleted(ctx context.Context) (rowsPurged, blobsPurged int, err error) {
    // 只处理文件实体的软删除行
    // 不处理消息、评论、通知等其他实体的过期软删除行
}
```

### 实现要点（不写代码，仅规划）

- **数据导出**: `GET /v1/export` 端点，异步生成包含用户所有消息/文件/评论的 .zip/.tar.gz 归档，通过通知推送完成消息。文件引用通过 presigned URL 包含在归档中。
- **可配置保留策略**: 为每个实体类型（messages/comments/files/notifications）添加 `retention_days` 配置，后台定时任务物理清除超过保留期的软删除行。硬删除策略应在 outbox 事件中记录。
- **账号删除级联**: 实现 `DELETE /v1/account` 端点（需用户重新认证），级联清除所有消息、评论、文件（物理删除）。或支持 SSO webhook 接收 `user.deleted` 事件后触发删除流程。
- **GC 扩展**: 将存储 GC 的 `sweeper` 通用化为一个数据生命周期管理器，支持插件式策略（永久保留/保留 N 天后软删除/立即物理删除）。

### 为什么现在不做就错过了

合规不是可选项。当企业客户/监管机构提出数据出口、删除、保留策略的要求时，**从零构建这些能力需要数周到数月**。在架构上，aero-id 已经有软删除、审计日志、配置系统——缺失的是"策略引擎"和"调度器"来驱动这些数据的完整生命周期。现在加入这些层，架构成本远远低于紧急合规需求时的添加上限。

---

## 方向五：限流分级与服务质量保障（Tiered Rate Limiting & Service Quality Guarantees）

### 产品经理视角

当前的速率限制是**一视同仁的**——所有用户共享相同的 RPS/Burst 配置。这在以下场景会带来严重问题：

1. **没有公平性保障**：一个恶意用户可以耗尽全局限流配额，导致所有用户体验到 429。
2. **没有产品分层空间**：无法提供"免费用户 10rps / 高级用户 100rps / 企业用户不限"的分级定价。
3. **没有关键操作提权**：读操作（列表查询）和写操作（发送消息、上传文件）共用同一限流配置，无法差异化对待。
4. **没有管理后门**：管理员和系统内部调用也受同样的限流限制——如 outbox relay dispatch 也需要竞争 token。

从运维角度看：
- 无法在业务高峰期为特定用户/操作预留容量
- 没有限流阈值预警（在达到 429 之前发出告警）
- 没有限流降级策略（限流时是否降级某些非关键操作）

### 架构师视角

**代码级证据 1：RateLimiter 只接受一组全局参数**

```go
// internal/pkg/ratelimit/ratelimit.go
type Limiter struct {
    mu     sync.Mutex
    rate   float64  // ← 全局 RPS，所有用户共享
    burst  int      // ← 全局 Burst，所有用户共享
    buckets map[string]*tokenBucket
    idleTTL time.Duration
}

func New(rps float64, burst int, idleTTL time.Duration) *Limiter {
    // 不区分用户角色、操作类型、来源 IP
}
```

所有用户进同一个 token bucket 池，只是 key 不同。但是 rate/burst 参数是全局的——没有办法给管理员用户更高的限额。

**证据 2：限流中间件在 Auth 之后，无法区分操作类型**

```go
// internal/handler/http/router.go
authMW := []gin.HandlerFunc{middleware.Auth(validator)}
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter))
}
```

`RateLimit` 中间件应用于整个 Auth 路由组（所有 `/v1/*` 端点），**无法按方法（GET/POST/PUT/DELETE）或按路由设定不同的限流策略**。一个 `GET /v1/messages` 列表查询和一个 `POST /v1/messages` 发送消息使用完全相同的令牌桶消耗。

**证据 3：限流成功/失败没有指标暴露**

```go
// internal/pkg/ratelimit/ratelimit.go
func (l *Limiter) Allow(key string) bool {
    // 返回 true/false，但不记录限流事件
    // 不暴露 hit count、miss count、当前 bucket 水位
}
```

无法通过 `/metrics` 观察限流的命中率、拒绝率、哪些 key 最常被限流。运维无法回答"我们是否该提高限流阈值了？"。

**证据 4：429 响应没有 Request-ID 关联**

```go
// internal/middleware/ratelimit.go
c.AbortWithStatusJSON(http.StatusTooManyRequests, rateLimitErrorBody{
    Error: rateLimitErrorDetail{
        Code:      "rate.limit_exceeded",
        Message:   "too many requests",
        RequestID: c.GetString("request_id"),
    },
})
```

错误体中包含了 `request_id`，但这个标识没有和限流事件日志关联。当用户投诉"我被 429 了"时，运维无法从限流日志中找到对应的记录来排查原因。

### 实现要点（不写代码，仅规划）

- **分级限流配置**: 将全局 `rate` / `burst` 改为每个 key 前缀可覆盖（如 `uid:admin-*` 继承高级别限额），或支持 `Limiter` 的 per-key override 映射（`map[string]RateConfig`）。支持在 `auth.go` 中间件中根据角色返回不同限额。
- **操作优先级**: 支持多级 token bucket——每个请求根据其操作类型（critical / normal / background）消耗不同优先级的配额。读操作可以使用共享大桶，写操作使用独立小桶。
- **限流可观测性**: 在 `Allow` 路径中增加计数器（total_requests, allowed, rejected, by_key），通过 Prometheus 直方图暴露 bucket 水位。增加限流预警 hook（当特定 key 的拒绝率达到阈值时触发告警）。
- **限流降级策略**: 支持配置限流时的行为选项（直接拒绝 / 排队等待 / 绕过但记录 / 降级为只读）。管理员操作和系统内部调用应有豁免机制。
- **每个路由的独立限流**: 中间件栈中引入 per-route `RateLimit` 配置（类似 `RequirePermission`），允许精细控制每条路由的限额。

### 为什么现在不做就错过了

限流分级是**商业化的前提**——没有分级就无法设计付费套餐。同时也是**运维稳定的基础**——没有优先级就无法保证关键操作在洪峰下的可用性。当前架构中限流是一行配置就开启的"全或无"开关，扩展为分级限流需要在中间件层和限流器内部都做改造。越早做，对其他模块的侵入越小。

---

## 建议实施顺序

| 优先级 | 方向 | 架构影响 | 产品价值 | 估算工作量 | 前置依赖 |
|--------|------|----------|----------|-----------|---------|
| **P0** | 方向二：审核面板与内容安全 | 中（新增表+中间件+handler） | 极高（合规+安全） | 中 | 已有审计日志+RBAC |
| **P0** | 方向四：数据生命周期管理 | 大（新增策略引擎+调度器） | 极高（合规+信任） | 大 | 软删除已就绪 |
| **P1** | 方向三：分布式共享状态 | 中（后端抽象+可选实现） | 高（可扩展性） | 中-大 | 已有状态组件清晰 |
| **P1** | 方向五：限流分级与 QoS | 中（限流器重构+配置层） | 高（商业+运维） | 中 | 限流器已就绪 |
| **P2** | 方向一：群聊消息 | 大（数据模型重构） | 极高（产品力） | 极大 | 消息系统固化 |

**说明：**
- **P0** 方向是安全和合规底线，建议优先投入。方向二（审核面板）用最小的表/API 增量解决最大的信任与安全风险；方向四（数据生命周期）解决合规风险，且现有软删除+GC 框架可复用。
- **P1** 方向是平台化的基础设施。方向三（分布式状态）有移植成本，但可以选择最小可行方案（如只用 PG LISTEN/NOTIFY 解决 SSE 跨实例）；方向五（分级限流）改造范围明确。
- **P2** 方向一（群聊）产品价值最高但架构成本最大——因为它挑战了消息系统的根基模型（1:1→N:N）。建议在前四个方向夯实后，投入专门的架构设计阶段来推进。

---

## 与 116 份既有分析的开创性差异

以下交叉比对确认了这 5 个方向在 `docs/requirements/` 全部既有文档中的覆盖程度：

| 关键词 | 既有文档命中数 | 说明 |
|--------|--------------|------|
| 群聊 / Group Conversation / Multi-party Messaging | 0 | 全量零命中 |
| 审核面板 / Moderation Dashboard / Trust & Safety | 0 | 全量零命中 |
| 分布式 / Shared State / Horizontal Scaling | 0 | 全量零命中 |
| 数据生命周期 / Data Retention / Export / GDPR | 0 | 全量零命中 |
| 限流分级 / Tiered Rate Limiting / QoS / Quota | 0 | 全量零命中 |
| 内容安全 / Content Safety / Spam Detection | 0 | 全量零命中 |
| 用户封禁 / Ban User / Account Suspension | 0 | 全量零命中 |
| 对话级操作 / Mute / Pin / Archive Conversation | 0 | 全量零命中 |

这 5 个方向是从 **生产级代码库 → 平台级基础设施** 的跃迁中必然要面对的架构增量。它们不是锦上添花的"再加一个 endpoint"，而是需要在数据模型、分布式协调、合规框架三个维度新增架构层次的能力。
