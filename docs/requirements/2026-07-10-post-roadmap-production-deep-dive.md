# aero-id — 生产级深度扫描：3 个高价值架构扩展方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全量代码扫描 (`internal/` 全部 22 个包、`migrations/` 22 份 SQL、`cmd/`、`configs/`、`api/proto/`)，结合已有 35+ 份分析文档交叉比对，确保以下方向具有**代码级证据支撑**且**未被其他文档系统性覆盖**。
> **无代码变更**。

---

## 当前代码库成熟度

aero-id 已完成全量 MVP 功能交付：

| 维度 | 已实现 |
|------|--------|
| **存储** | 双后端（local/S3）、流上传、直传预签名、内容去重、MIME 校验、GC 回收、原子引用计数 |
| **消息** | 全 CRUD、Reactions、编辑、软删除、已读回执、Inbox/Conversation、FTS 搜索、系统消息 |
| **评论** | 两层嵌套、Like/Unlike、举报/审核/隐藏、编辑、原子 reply_count |
| **通知** | Outbox 驱动、偏好设置（静默时段）、合并去重、SSE 实时推送、未读计数 |
| **可靠性** | 事务性 Outbox、幂等键、限流（per-actor）、SSO 断路器 + stale-cache + singleflight、优雅关闭 + gRPC health drain |
| **可观测性** | RED 指标、OTel 链路追踪、SSO 健康仪表盘、Outbox 指标 |
| **基础设施** | 管理 CLI（`cmd/migrate`）、自动迁移、gRPC 反射 |
| **平台** | 用户阻止（block）、角色权限、审计日志、Webhook 事件推送 |

`make test && make lint` — 全绿。

已有 35+ 份分析文档覆盖了 50+ 个扩展方向，从群聊到静态加密，从内容安全到分布式限流。本文聚焦于**三个现有文档均未系统性覆盖的、基于当前架构结构性断点的方向**。

---

## 方向一：消息模型的结构性升级 —— 从 1:1 二元组到标准化对话抽象

### 为什么需要它

当前消息模型是严格的 1:1 对话：`messages` 表仅有 `(from_uid, to_uid)` 二元组，`Conversation(uidA, uidB, page)` 等价于 `WHERE (from_uid=A AND to_uid=B) OR (from_uid=B AND to_uid=A)`。

这个模型在设计层面阻塞了 5 个高频需求，且每个需求在当前架构下的实现成本会随着未来迭代指数级上升：

| # | 需求 | 当前状态 | 被阻碍的原因 |
|---|------|---------|------------|
| 1 | **群聊**（Group Chat） | `to_uid` 是单一字符串，无法表示多人会话 | 无 `conversations` 表、无 participants、无成员角色 |
| 2 | **对话级元数据**（名称、头像、主题） | 不存在；对话只是两个 uid 的配对 | 无 `conversations` 表可承载元数据 |
| 3 | **精准已读水位线**（per-conversation `last_read_message_id`） | `MarkConversationRead` 批量标记某方全部消息为已读，无法恢复精确位置 | 无 `conversation_members` 可存储 `last_read_id` |
| 4 | **对话归档/静音/置顶** | 不存在；收件箱只能显示全部对话 | 无 `conversation_settings`（每个成员在每个对话中的状态） |
| 5 | **消息转发/引用**（Reply-to / Quote） | `FileID` 存在但 `ReplyToMessageID` 不存在 | 消息体无 `in_reply_to` 字段 |

### 代码级证据

**证据 1**：`Conversation()` 的查询逻辑示例如下（简化）：

```go
// internal/service/message.go — Conversation(ctx, withUID, page)
// 实际是: WHERE (from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)
// 没有 conversations 表、没有 conversation_id 列
```

**证据 2**：`MarkConversationRead` 的实现是一次性标记某方所有未读：

```go
// internal/repository/message.go
func (r *repo) MarkConversationRead(ctx, toUID, peer string, at time.Time) error {
    // UPDATE messages SET read_at = ? WHERE from_uid = ? AND to_uid = ? AND read_at IS NULL
    // 仅是按收发双方二元组匹配，没有精确到"用户读到哪条消息为止"
}
```

**证据 3**：`Message` 领域模型无对话相关字段：

```go
// internal/domain/message.go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string           // ← 单一对方 uid
    Kind      MessageKind
    Content   string
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any
    CreatedAt time.Time
}
```

**证据 4**：proto 定义中无 `conversation_id`：

```go
// api/proto/aero_id/v1/aero_id.proto
message Message {
    string id = 1;
    string from_uid = 2;
    string to_uid = 3;          // ← 单一接收方
    int32 kind = 4;
    // ... no conversation_id, no in_reply_to
}
```

### 产品价值

消息是 aero-id 的核心用例。当前模型将产品锁定在"最原始的私信"形态。从 1:1 到标准化对话的升级是**解锁群聊、消息组织、收件箱管理、已读状态、消息引用等 5 个功能序列的前提基础设施**。每推迟一步，后续迁移成本就更高——因为未来的群聊迁移需要在已有海量 1:1 消息数据上重建对话模型。

### 架构建议

**Phase 1 — 对话表引入（向后兼容）**：

```sql
CREATE TABLE conversations (
    id UUID PRIMARY KEY,          -- UUIDv7
    kind VARCHAR(16) NOT NULL DEFAULT 'pair',  -- 'pair' | 'group' | 'channel'
    name VARCHAR(255),
    owner_uid VARCHAR(64) NOT NULL,
    meta JSONB DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ        -- soft delete
);

CREATE TABLE conversation_members (
    conversation_id UUID REFERENCES conversations(id),
    uid VARCHAR(64) NOT NULL,
    role VARCHAR(16) NOT NULL DEFAULT 'member',  -- 'owner' | 'admin' | 'member'
    last_read_id UUID,            -- 该成员在该对话中读到的最新消息 id
    muted BOOLEAN NOT NULL DEFAULT false,
    archived BOOLEAN NOT NULL DEFAULT false,
    joined_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (conversation_id, uid)
);

-- messages 表增加 conversation_id（可空，向后兼容）
ALTER TABLE messages ADD COLUMN conversation_id UUID REFERENCES conversations(id);
ALTER TABLE messages ADD COLUMN in_reply_to UUID REFERENCES messages(id);
```

**Phase 2 — 迁移现有 1:1 数据**：后台 job 为每对活跃的 `(uidA, uidB)` 自动创建 `conversation` 行，`conversation_members` 两行，然后将历史消息的 `conversation_id` 回填。

**Phase 3 — API 升级**：新增 `GET /v1/conversations`（对话列表，而非平铺 inbox）、`POST /v1/conversations`（创建群聊）、`POST /v1/conversations/:id/members`（邀请成员）。现有 `GET /v1/conversations/:uid` 保持兼容。

### 规模估算

| Phase | 涉及文件 | 新表/列 | 端点 | 人天 |
|-------|---------|---------|------|------|
| 1 | 3 migrations, 5 Go files | conversations, members + messages.in_reply_to | 0 (仅 infra) | 3-4 |
| 2 | 2 Go files (migration backfill job) | 0 | 0 | 1-2 |
| 3 | 8-10 Go files (domain, repo, service, HTTP, gRPC, proto) | 0 | 4 端点 + 4 gRPC RPC | 5-7 |
| **总计** | | | | **9-13** |

---

## 方向二：平台级单机并发协调器（Distributed Coordination Layer）

### 为什么需要它

当前 aero-id 的所有并发协调机制都是**单实例、进程内**的：

| 组件 | 当前机制 | 多实例时的行为 |
|------|---------|---------------|
| **Rate Limiter** | 内存 token-bucket，per-instance | 每个实例独立限流；N 个实例相当于限流阈值放大 N 倍 |
| **Idempotency Store** | `idempotency_keys` DB 表 | ✅ 天然跨实例（共享 Postgres） |
| **Outbox Relay** | 每个实例独立轮询、独立发布 | ❌ **每个实例都处理同一批事件 → 重复通知** |
| **SSO Token Cache** | 内存 LRU + TTL，per-instance | 冷启动期间每个实例分别穿透到 SSO |
| **SSO User Cache** | 同上 | 同上 |
| **Storage GC Sweeper** | 每个实例独立运行 | ❌ 多个实例同时扫描、删除同一批软删除行 → 竞态/重复工作 |
| **Hub (SSE)** | 内存 channel 注册表，per-instance | ❌ 推送只发给连接在当前实例上的客户端；其他实例上的同 uid 连接收不到 |

### 代码级证据

**证据 1**：Outbox relay 以固定的独立 goroutine 运行，无实例协调：

```go
// internal/outbox/relay.go — Start 方法
func (r *Relay) Start(interval time.Duration) (stop func()) {
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        ctx := context.Background()
        for {
            select {
            case <-t.C:
                r.drain(ctx)  // 每个实例各自 drain
            case <-done:
                t.Stop()
                r.drain(context.Background())  // 最后再 drain 一次
                return
            }
        }
    }()
    return func() { close(done) }
}
```

对应的 app.go 中没有任何 election/leader 机制——所有 relay 实例都在拉取同一批 `published_at IS NULL` 的事件：

```go
// internal/app/app.go
stopRelay := relay.Start(pollInterval) // 每个实例都启动一个 relay
```

**证据 2**：SSE Hub 是纯内存的：

```go
// internal/hub/hub.go
type Hub struct {
    subs   sync.Map // uid → *uidSubs  内存 map，跨多实例不存在
    // ...
}
```

**证据 3**：Rate limiter 是内存的，无共享状态：

```go
// internal/pkg/ratelimit/ratelimit.go
type Limiter struct {
    mu     sync.Mutex
    buckets map[string]*bucket  // 内存 map，不跨进程
    // ...
}
```

**证据 4**：SSO 缓存也是内存的：

```go
// internal/sso/token_cache.go
type TokenCache struct {
    mu   sync.RWMutex
    entries map[string]*cacheEntry  // 内存 map
    // ...
}
```

### 产品价值

在当前的单实例部署中这些都不是问题。但一旦水平扩展（不管是 Kubernetes 多副本弹性伸缩，还是分区部署），以下问题立刻出现：

1. **Outbox 重复通知** —— 最严重。两个实例同时处理 `message.sent` 事件 → 接收方收到两条相同通知。这不是"延迟"问题，而是**数据正确性问题**。
2. **SSE 消息丢失** —— 推送只发给连接到当前实例的客户端。如果一个用户同时打开两个浏览器标签（或 Web + Mobile），只有连接到实际发送事件的那个实例的客户端能收到推送。其他客户端必须等到下一次轮询（如果有的话）。
3. **限流失效** —— 多实例时有效限流阈值放大 N 倍，相当于无限制。
4. **GC 重复工作** —— 两个 sweeper 同时删同一批文件——不会出错（引用计数保护了 blob），但浪费 CPU 和存储 API 调用量。

### 架构建议

#### 方案 A：数据库级协调（最简单，推荐 Phase 1）

- **Outbox relay**：在 `FetchUnpublished` 中加入 `SELECT ... FOR UPDATE SKIP LOCKED LIMIT N`。实例 A 锁定一批事件（`published_at IS NULL AND locked_by IS NULL`）并将自己的实例 ID 写入 `locked_by`；实例 B 的 `FETCH` 自动跳过这些事件。这是 PostgreSQL 提供的原生协调，零外部依赖。
- **GC sweeper**：类似方案——`FOR UPDATE SKIP LOCKED` 锁定待处理的软删除行。
- **SSE Hub**：引入 `last_event_id` + 客户端重连重放（当前已有 `replayMissed`）。多实例时，SSE 推送到"当前实例的连接"，未送达的通过 Outbox 事件 + 数据库持久化的通知来兜底。客户端重连时通过 `Last-Event-ID` 自动补全遗漏通知。

#### 方案 B：外部协调服务（适合 Phase 2，需要更复杂的需求）

- **Leader Election**：基于 PostgreSQL 的 `pg_advisory_lock` 或 etcd/Redis 选主。只有 leader 实例运行 relay 和 GC。
- **分布式缓存**：共置 Redis 作为 SSO Token/User 缓存和 Rate Limiter 后端，消除冷启动穿透和限流不一致。
- **SSE 广播**：实例间通过 Redis Pub/Sub 或 gRPC stream 转发 SSE 事件，确保 push 到达所有实例上的连接。

#### 推荐路线

1. **Phase 1（短期，P0）**：Outbox relay 改 `FOR UPDATE SKIP LOCKED`（PG 原生、零外部依赖）。GC sweeper 同理。SSE 依赖 `last_event_id` + DB 通知兜底。
2. **Phase 2（中期，P1）**：引入 Redis 共享缓存层（Rate Limit + SSO Cache），消除多实例限流失效和缓存冷启动穿透。
3. **Phase 3（长期，P2）**：支持多实例同时生存时，SSE 跨实例广播可通过 Redis Pub/Sub 实现，或升级为 WebSocket + 客户端 reconnect 自动 failover。

### 规模估算

| Phase | 依赖 | 人天 |
|-------|------|------|
| 1: `SELECT ... FOR UPDATE SKIP LOCKED` | PostgreSQL（已满足） | 2-3 |
| 2: Redis 共享缓存 | Redis 部署 | 4-6 |
| 3: SSE 跨实例广播 | Redis Pub/Sub 或 gRPC inter-instance | 3-5 |

---

## 方向三：服务网格入口治理 —— API 网关能力层

### 为什么需要它

aero-id 是一个双传输层服务（HTTP Gin + gRPC），但目前缺少**服务网格入口治理**的基本要素：

- **没有 OpenAPI 规范** —— HTTP API 完全由 Gin 路由 + handler 结构体定义，无 OpenAPI/Swagger 文档生成。外部消费者无法以标准方式发现 API。
- **没有 API 版本化策略** —— `/v1/` 前缀是硬编码的，但无 v1→v2 的版本共存、弃用通知（`Deprecation` header）、或向后兼容契约。
- **没有精确的 API 速率分区** —— 限流是全局的（per-actor）。但搜索端点（高 CPU）、文件上传端点（高带宽）、消息列表端点（高频率轮询）的行为特征完全不同，需要不同的限流策略。
- **没有请求大小/时限的按端点划分** —— `RequestTimeout` 是全局的。但大文件上传需要长 timeout，而 ping/列表请求需要短 timeout。
- **没有 API 使用量统计 / 消费者计数** —— 不知道每个 SSO uid 访问了哪些端点、频率多高、错误率如何。

### 代码级证据

**证据 1**：全局限流、无端点分区：

```go
// internal/middleware/ratelimit.go — RateLimit 中间件
func RateLimit(limiter *ratelimit.Limiter) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := resolveKey(c)
        if !limiter.Allow(key) {
            // 429 Too Many Requests — 所有端点共享同一个限流器
        }
    }
}
```

`app.go` 中只实例化了一个 `ratelimit.Limiter`，所有路由共享。

**证据 2**：全局超时、无端点定制：

```go
// internal/middleware/timeout.go — Timeout 中间件
func Timeout(d time.Duration) gin.HandlerFunc {
    // 对每一个请求设置相同的 context deadline
    // 文件上传需要更长超时，列表查询需要更短超时——当前无法区分
}
```

**证据 3**：无 OpenAPI 生成工具集成。Gin 路由通过函数式注册（`re.RegisterRoutes(routes)`），路由元数据散落在各个 handler 文件中，无集中式规范输出。这意味着无法自动生成客户端 SDK、无法做 API 变更对比、无法做消费者契约测试。

**证据 4**：`/v1` 是硬编码字符串：

```go
// internal/handler/http/router.go
routes := Routes{
    Auth:   e.Group("/v1", authMW...),  // 硬编码版本前缀
    Public: e.Group("", publicMW...),
}
```

没有设计级别的版本切换层——要发布 `/v2/` 必须重写整个 `NewRouter`。

### 产品价值

对于当前单消费者（单一前端应用）而言，这些缺失是可容忍的。但一旦 aero-id 开放给**外部集成方**（通过 Webhook、gRPC、或 REST API），以下问题逐一浮现：

1. **集成方无法评测 API** —— 没有 OpenAPI 规范意味着每个第三方开发者必须先阅读 Go 源码或 proto 文件，而非用 OpenAPI Generator 自动生成客户端。
2. **API 变更即破坏** —— 没有弃用策略，无法安全地重命名端点、更改字段、或移除参数。`/v1/messages/inbox` 如果改成 `/v1/conversations`，旧客户端立即断裂。
3. **无法防护特定端点** —— 搜索端点若被滥用（高频搜索），全局限流会连带保护消息发送和文件上传。反之，文件上传设过紧的限流会限制用户正常使用。
4. **无成本归属** —— 不知道哪些用户/集成方消耗了最多 API 调用，无法做 tier-based 限流。

### 架构建议

#### Phase 1 — API 可发现性（P1）

集成 `gin-swagger` 或 `kin-openapi` 从 handler + schema 注解生成 OpenAPI 3.0 规范。HTTP handler 的请求/响应结构体加 `// @Description` 注解，输出 `openapi.json` 端点（可选 gated）。

#### Phase 2 — 端点级治理（P1-P2）

引入中间件配置模型，支持按路由模式覆盖全局默认值：

```go
type RoutePolicy struct {
    Pattern        string        // e.g. "/v1/search:POST"
    RateLimitRPS   *float64      // nil = use global
    RequestTimeout *time.Duration // nil = use global
    MaxBodyBytes   int64
}
```

在 `RouterConfig` 中增加 `RoutePolicies []RoutePolicy` 配置项，中间件按请求路径匹配后应用。

#### Phase 3 — API 版本化策略（P2）

- 定义版本化契约（`/v1/` vs `/v2/`）的共存方式
- 引入 `Deprecation: true` 和 `Sunset: <date>` 响应头
- 建立 API 弃用工作流：标记 → 通知 → 移除（至少 2 个版本的过渡期）
- 利用 proto 的 `reserved` 字段做 gRPC 的向后兼容

### 规模估算

| Phase | 涉及范围 | 人天 |
|-------|---------|------|
| 1: OpenAPI 集成 | 1 个新 Go 包 + handler 注解 + 1 个 HTTP 端点 | 3-5 |
| 2: 端点级策略 | 中间件重构 + config 扩展 + 路由匹配器 | 3-4 |
| 3: 版本化策略 | 路由层重构 + proto 兼容性规划 + 弃用工具 | 4-6 |

---

## 综合优先级

| 方向 | 类别 | 优先级 | 估算(人天) | 价值 |
|------|------|--------|-----------|------|
| 1. 对话消息模型升级 | 架构演进 / 功能地基 | **P1** | 9-13 | 解锁群聊、消息组织、已读水位等 5 个产品序列 |
| 2. 分布式协调层 | 可靠性 / 基础设施 | **P0** | 2-3 (Phase 1) | 多实例部署的前提：阻止重复通知和限流失效 |
| 3. API 网关能力层 | 可消费性 / 平台化 | **P1** | 6-10 | 开放 API 和第三方集成的门槛条件 |

**实施顺序建议**：

```
Phase 1a: Outbox relay + GC → FOR UPDATE SKIP LOCKED (方向二, Phase 1, 2-3天)
    ↓
Phase 1b: OpenAPI 规范生成 (方向三, Phase 1, 3-5天)
    ↓
Phase 2:  对话表 + members 表 + 历史数据回填 (方向一, Phase 1-2, 5-6天)
    ↓
Phase 3:  端点级限流/超时策略 (方向三, Phase 2, 3-4天)
    ↓
Phase 4:  API 版本化 + SSE 跨实例广播 (方向一 Phase 3 / 方向三 Phase 3, 7-11天)
```

---

*本文档基于代码库 `main` 分支（2026-07-10）全量扫描生成，与 35+ 份既有分析文档交叉比对。不编写任何代码。*
