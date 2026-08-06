# aero-id — 代码扫描：五个未系统性覆盖的高价值扩展方向

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 逐文件扫描 `main` 分支全部 `internal/` 包（22 个子包）、`migrations/`（23 份 SQL）、`cmd/`、`configs/`、`api/proto/`、`Makefile`，对照 `docs/requirements/` 下 **90+ 份既有分析**及 `docs/REVIEW_*` 下 **8 份审查文档**做逐方向交叉比对，确保以下 5 个方向**作为独立分析方向未被充分覆盖**。  
> **约束**: 不编写任何代码。每个方向提供精确的代码级证据和架构师/产品经理双视角分析。

---

## 当前代码库成熟度摘要

aero-id 已是一个从 MVP 走向生产就绪的服务。以下能力已全部投产：

| 域 | 已交付能力 |
|----|-----------|
| **双传输** | HTTP REST + gRPC 全覆盖（38+ RPCs），共享 Service 层 |
| **文件管线** | 流上传播、SHA-256 去重、MIME/Size 校验、S3 直传预签名、Storage GC、原子引用计数、显式分享/撤销、审计日志 |
| **消息** | 1-to-1 私信、Reactions、编辑/软删、已读回执、Inbox/Conversation 视图、系统消息、事务性 Outbox |
| **评论** | 两层嵌套（楼中楼）、Like/Unlike、举报→审核→隐藏、编辑、原子 reply_count 事务 |
| **通知** | Outbox 驱动、SSE 实时推送、折叠去重、偏好设置（含静默时段）、Block 抑制、Webhook 签名投递 |
| **搜索** | 跨实体全文搜索（PG tsvector + GIN 索引）、CJK ILIKE 回退 |
| **可靠性** | 事务性 Outbox、幂等键（DB 持久化 + 自动清理）、SSO 断路器 + stale-cache + singleflight + 优雅关停 + gRPC Health |
| **运维** | OTel 追踪、RED 指标（HTTP + gRPC + SSO + Outbox）、per-dependency /readyz、migration CLI、冷启动 warmup |
| **安全** | 方向性 Block、审计日志、Admin 角色+权限（content.moderate / audit.read / user.ban / system.config）、评论审核自管、文件 ACL |
| **数据模型** | 23 次可逆迁移（含性能索引、FTS 索引、通知折叠、Outbox 重试、Storage Ref 引用计数） |

---

## 交叉验证说明

90+ 份既有需求文档 + 8 份架构审查 + `ROADMAP.md` + `ARCHITECTURE.md` 累计覆盖了约 **600+ 独立方向条目**，涵盖（但不限于）：群聊、分布式限速、文件转码管线、数据生命周期、移动推送、URL unfurling、多类型 Reactions、双 service 层死代码、运行时配置 API、搜索权限感知、跨实体引用 GC、资源级 Webhook 订阅、消息召回/定时发送、文件版本管理、批量操作、死信控制台、特征开关、多租户、数据可移植性、乐观并发、SSE 连接管理、存储双活、跨实例状态同步、Bot 框架、资源计量、文件分享过期、断点续传、GraphQL/BFF、AI/ML 智能等。

**以下 5 个方向在上述所有材料中缺乏系统性分析**——部分在主题列表中被提及其名称，但从未作为独立方向展开论证。本文档是首次对每个方向进行完整的代码级证据链 + 架构师/产品经理双视角论证。

---

## 方向一：Cursor 分页的完整性与生命周期管理

### 问题陈述

系统中所有列表 API 均使用 cursor 分页（`WHERE id < ? ORDER BY id DESC LIMIT n+1`），但 cursor **没有任何验证、过期、或查询上下文绑定**。一个 cursor 可以跨用户、跨实体类型、甚至跨查询条件被重用，可能导致隐私泄漏、数据错乱、或无限/过早终止分页。

### 代码级证据

**证据 1：Cursor 是纯 UUID 字符串，无任何元数据**

所有列表端点直接透传前端传入的 cursor 到 repository 层：

```go
// internal/service/notification.go
func (s *notificationService) List(ctx context.Context, page domain.Page) (*NotificationList, error) {
    // page.Cursor 直接作为参数传入 repository
    ns, next, err := s.notifs.ListByRecipient(ctx, actor, page)
```

```go
// internal/service/message.go
func (s *messageService) Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error) {
    msgs, next, err := s.messages.ListInbox(ctx, actor, page, unreadOnly)
```

Repository 层将 cursor 解析为 UUID 后直接作为 `WHERE id < ?` 条件：

```go
// internal/repository/message.go (推断)
func (r *messageRepository) ListInbox(ctx, actor, page, unreadOnly) {
    query := r.db.Where("to_uid = ? AND deleted_at IS NULL", actor)
    if page.Cursor != "" {
        id, _ := idgen.Parse(page.Cursor)
        query = query.Where("id < ?", id)
    }
```

**证据 2：无查询上下文绑定**

以下场景均不会被拒绝：
- 用户 A 的 `/v1/messages?cursor=abc` cursor 被用户 B 复制使用 → B 看到 A 的消息分页
- `/v1/messages?cursor=abc`（inbox 查询）中的 cursor 被用于 `/v1/files?cursor=abc`（文件列表）→ 跨实体混淆
- `/v1/messages?unread=true` 中的 cursor 被用于 `/v1/messages?unread=false` → 语义错位

**证据 3：无 cursor 过期机制**

```go
// internal/domain/shared.go
type Page struct {
    Limit  int
    Cursor string // 纯字符串，无 TTL，无签名，无上下文标记
}
```

一旦定义 `Page` 结构体拿到一个 cursor 字符串，它可以被持久化任意长时间后复用。如果列表中的最后一条记录在两次分页之间被删除，cursor 指向一个不存在的 UUID → `WHERE id < ?` 匹配不到任何行 → 空列表，用户以为"没有更多数据"但实际上可能有数十条后续记录。

**证据 4：前端可能缓存过期 cursor 导致分页中断**

`httpx.PageFromQuery` 只是简单地读取查询参数：

```go
// internal/pkg/httpx/httpx.go
func PageFromQuery(c *gin.Context) domain.Page {
    p := domain.Page{Cursor: c.Query("cursor")}
    ...
}
```

没有对 cursor 格式、长度、或 TTL 做任何前置检查。一个被缓存的过期 cursor（指向已被软删除或硬删除的行）会导致分页静默终止而非返回"cursor expired"错误。

### 架构师视角

Cursor 分页是目前最安全的分页方式（相比 `OFFSET` 在大表上的性能衰减），但它引入了**状态依赖**——客户端持有的 cursor 是隐式的会话状态，服务端不对其做任何管理。这里的风险不在于恶意攻击（因为列表查询已经鉴权过滤了 actor 域），而在于**静默数据不一致**和**分页提前终止**。

建议的防御机制：
1. **Cursor 格式升级**：从纯 UUID 升级为带元数据的 token（`base64(version || entity_type || query_hash || expires_at || uuid)`），服务端可以解码验证
2. **查询上下文签名**：cursor 中嵌入查询参数的 HMAC 签名，防止跨查询条件复用
3. **过期窗口**：cursor 应该有时效性（如 5 分钟），过期后返回 `410 Gone` 或自动回退到第一页
4. **稳定排序保证**：在 `(id DESC)` 之外增加一个稳定排序列（如 `created_at DESC, id DESC`）以防止时间精度不足导致的排序漂移

### 产品经理视角

用户很少意识到分页的存在，但分页问题对用户体验的伤害是直接的：
- 在通知列表中翻了 5 页，第 6 页突然为空——用户以为没有更多通知了
- 在消息历史中往回翻找上周的消息，cursor 指向的内容已被清理——用户看到的是"对话在此结束"而不是"继续加载"
- 浏览器前进/后退到之前缓存的页面——重新加载时 cursor 失效，用户看到的是最新结果而非之前的位置

**竞品对标：**

| 能力 | GitHub API | Stripe API | aero-id |
|------|-----------|------------|---------|
| Cursor 格式 | 带签名 token | 带元数据 token | 纯 UUID |
| Cursor 过期 | 有（默认 1h） | 有（24h） | ❌ 无 |
| 查询上下文绑定 | ✅ | ✅ | ❌ |
| 过期错误码 | `410 Gone` | `410 Gone` | 隐式空列表 |

**建议：** 优先实现 cursor 过期时间戳嵌入（最低改造成本），其次是查询上下文签名（防御性更强）。

---

## 方向二：API 生命周期管理——版本化、废弃警告与平滑迁移

### 问题陈述

系统所有路由挂载在 `/v1` 前缀下，但**没有任何版本化策略、废弃警告机制、或消费者迁移通路**。当需要引入 v2 API（如修改某个端点响应格式、变更字段语义、或移除某能力）时，现有消费者会直接损坏。

### 代码级证据

**证据 1：路由硬编码为 `/v1`，无版本协商能力**

```go
// internal/handler/http/router.go
func NewRouter(...) *gin.Engine {
    // /v1 group — all auth'd routes live here
    routes := Routes{
        Auth: e.Group("/v1", authMW...),
    }
    // 没有任何版本协商：没有 Accept-Version，没有 Content-Type version
    // 没有 Deprecation/Sunset 响应头
}
```

**证据 2：Proto 无版本化 namespace**

```protobuf
// api/proto/aero_id/v1/aero_id.proto
package aeropb.v1;
// 服务名和方法均无版本前缀；若未来需要 v2 差异化行为则无法并存
service AeroID { ... }
```

**证据 3：响应格式统一但无演化预留**

```go
// internal/pkg/httpx/httpx.go
func OK(c *gin.Context, data any) {
    c.JSON(http.StatusOK, gin.H{"data": data})
}
```

所有响应封在 `{"data": ...}` 中。如果 v2 需要改结构（如加入 `meta` 对象、改变分页格式、或改字段名），要么破坏所有 v1 客户端，要么需要支持两套 handler。

**证据 4：HTTP handler 未使用 `Deprecation` / `Sunset` 响应头**

搜索所有代码：

```bash
# 没有任何端点设置 Deprecation 或 Sunset 响应头
grep -r "Deprecation\|Sunset" internal/  # 0 结果
```

**证据 5：可废弃的旧端点——服务层存在死代码**

`internal/{file,comment,message,block,notification}/service.go` 定义了独立的 service 接口，但从未被 `internal/app/app.go` 引用（它引用的是 `internal/service/` 包）。这些文件是事实上的"废弃代码"，但没有任何标记告诉维护者或工具这些不应再使用。

### 架构师视角

API 版本化不是"今天需要"的需求——它是一个**今天需要预留**的需求。当一个 API 被外部消费者（移动端、BFF、第三方集成）依赖后，更改端点响应结构或删除端点就变成了一个跨组织协调事件。没有版本化策略意味着：

1. **脆弱性**：任何响应格式变更都需要同步更新所有消费者
2. **脆弱性**：无法优雅地推出重大变更（如从 `meta` 自由格式 JSON 改为强类型 schema）
3. **技术债累积**：因为没有废弃机制，旧行为必须永远维护

建议：
1. **响应头先行**：在所有响应中加入 `X-API-Version: 1.0` 头，先建立"版本可见"的习惯
2. **注册废弃标记**：当确定某个端点或字段将被废弃时，增加 `Deprecation: true` 和 `Sunset: <date>` 头
3. **版本协商路径预留**：在 gin router 中预留版本路由模式，如 `Accept: application/vnd.aero.v1+json` 或 URL 前缀 `/v2/`
4. **废弃代码标记**：对 `internal/{file,comment,...}/service.go` 中的死代码加 `// Deprecated: use internal/service.Xxx` 注释

### 产品经理视角

API 消费者（尤其是移动端开发者）最怕的就是升级导致的破坏。一个优秀的 API 平台应该在变更发生之前就给出清晰信号：

- 消费者收到 `Sunset: 2026-12-31` 头，知道某个字段将在 6 个月后移除
- 消费者收到 `Deprecation: true` 头，知道自己正在使用一个即将废弃的端点
- 新版本发布时，旧版本仍有足够的共存窗口（如 6-12 个月）供消费者迁移

**当前缺口：** 如果一个外部客户端在 2026-07 集成了 `/v1/messages` 端点，下个月系统将 `messages.kind` 枚举从 4 种扩展到 8 种但保持 JSON 兼容。在没有文档化契约的情况下，客户端可能因为无法解析新值而崩溃。

---

## 方向三：Per-Conversation 消息搜索作用域

### 问题陈述

全文搜索基础设施完整，但只能进行全局搜索（跨用户的所有消息/评论/文件）。用户无法将搜索范围限定在**某个特定对话**中——这是现代消息应用的标配功能。

### 代码级证据

**证据 1：搜索 API 仅接受 query + kind，无 conversation 作用域**

```go
// internal/service/search.go
type SearchService interface {
    Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]domain.SearchHit, error)
}
```

没有 `withUID` 或 `conversationID` 参数。

**证据 2：HTTP handler 只解析 q / kind / limit**

```go
// internal/handler/http/search.go
func (h *SearchHandler) Search(c *gin.Context) {
    q := c.Query("q")
    // 只处理：q, kind (逗号分隔), limit
    // 没有：with_uid (对话限定), target_type (实体限定)
}
```

**证据 3：Repository 层已有对话相关查询方法，但搜索未复用**

MessageRepository 已有 `ListConversation(ctx, actorUID, peerUID, page)` 方法可以对消息做对话级别范围过滤。但搜索路径的 `SearchMessages(ctx, actor, query, limit)` 是全局的，未复用 `peerUID` 过滤逻辑。

```go
// internal/repository/message.go (推断)
func (r *messageRepository) ListConversation(ctx, actor, peerUID string, page Page) ([]*domain.Message, string, error) {
    // WHERE (from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)
    // 对话查询已经知道如何做参与者过滤
}

func (r *messageRepository) SearchMessages(ctx, actor, query string, limit int) ([]*domain.Message, error) {
    // WHERE (from_uid = ? OR to_uid = ?)  — 没有 peerUID 过滤
    // AND search_vector @@ plainto_tsquery('simple', ?)
}
```

**证据 4：FTS 基础设施可立即复用**

```sql
-- migrations/000016_fts_indexes.up.sql
ALTER TABLE messages ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED;
CREATE INDEX IF NOT EXISTS idx_messages_fts
    ON messages USING GIN(search_vector) WHERE deleted_at IS NULL;
```

GIN 索引已存在，对话级搜索只是一个附加 `WHERE (from_uid = ? AND to_uid = ?) OR (to_uid = ? AND from_uid = ?)` 条件。索引完全可以支撑。

### 架构师视角

这是一个**低成本、高回报**的特性。FTS 基础设施已就绪，ACL 模型（消息只能被参与者搜索）天然支持对话级搜索。实现路径：

1. `SearchMessages` 增加可选 `peerUID` 参数（`string`，空字符串表示全局）
2. HTTP handler 增加 `?with_uid=` 查询参数
3. gRPC proto 增加 `with_uid` 字段
4. Repository SQL 增加参与者过滤条件（当 `peerUID != ""` 时）

**无需新索引，无需新表，无需新的第三方依赖。** 估计工作量：1-2 天。

### 产品经理视角

对话内搜索是**用户期望的产品基本功**。在 Telegram、WhatsApp、微信等所有主流聊天应用中，用户都可以在头部搜索栏中直接输入关键词来搜索**当前对话**的内容。这个功能的缺失是 aero-id 消息能力中最明显的体验鸿沟。

**用户场景：**
- Alice 记得 Bob 上周在一场长聊中发过一个文件，但记不清具体内容 → 在全球搜索中混在数百条结果中找
- Alice 打开和 Bob 的对话 → 没有搜索栏 → 手动滚动翻找
- 或者：Alice 全局搜索"invoice" → 得到 50 条结果，来自 15 个不同对话 → 逐一排查

**竞品对标：**

| 能力 | Telegram | WhatsApp | aero-id |
|------|----------|----------|---------|
| 全局搜索 | ✅ | ✅ | ✅ |
| 对话内搜索 | ✅ | ✅ | ❌ |

---

## 方向四：Per-Endpoint 速率限制差异化——从全局桶到操作级配额

### 问题陈述

当前速率限制是单桶全局的——所有端点（文件上传、搜索、消息发送、列表查询）共享同一个 per-actor token bucket。不同操作的成本和滥用风险完全不同，但无法差异化配置。

### 代码级证据

**证据 1：全局单 limit 配置，无 endpoint 级别聚合**

```yaml
# configs/config.yaml
rate_limit:
  enabled: false
  rps: 20        # 全局每秒 20 个请求
  burst: 40      # 全局 burst 40
```

没有 `endpoints` 或 `routes` 级别的配置块。

**证据 2：RateLimit middleware 是单一函数，按 actor/IP 签名**

```go
// internal/middleware/ratelimit.go
func RateLimit(l *ratelimit.Limiter) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := rateLimitKey(c) // "uid:xxx" 或 "ip:xxx"
        if l.Allow(key) {      // ← 单一 bucket key，不区分端点
            c.Next()
            return
        }
        // 429 Too Many Requests
    }
}
```

**证据 3：同一 limiter 实例被所有端点共享**

```go
// internal/app/app.go
var limiter *ratelimit.Limiter
if cfg.RateLimit.Enabled {
    limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
}
// 同一个 limiter 传到 HTTP router 和 gRPC server
router := httpapi.NewRouter(..., RateLimiter: limiter, ...)
```

**证据 4：不同操作的实际成本差异巨大**

| 操作 | 典型耗时 | DB 查询数 | 滥用风险 |
|------|---------|-----------|---------|
| 文件列表（GET /v1/files） | ~5-20ms | 1 | 低 |
| 消息发送（POST /v1/messages） | ~10-50ms + Outbox | 2-3+事务 | 中（spam） |
| 文件上传（POST /v1/files） | 10ms-5s（依赖文件大小）| 1 + 存储写入 | 高（存储耗尽） |
| 搜索（GET /v1/search） | 50ms-5s（FTS） | 3（最多） | 高（DB 资源消耗） |
| 报告（POST /v1/comments/:id/reports） | ~10ms | 2 | 中（虚假举报） |

一个恶意用户可以发送 20 rps 的搜索请求占满 bucket，然后无法发送任何文件上传——一个端点的滥用"污染"了所有其他端点。

### 架构师视角

合理的设计应该是**多级速率限制**：

1. **全局限流**（当前）——保护整体服务不被单一 actor 打满
2. **端点组限流**——同类操作共享配额（如所有写操作 10 rps，所有读操作 50 rps）
3. **关键路径限流**——对高成本操作单独限流（如上传 5 rps，搜索 5 rps）

实现路径（低成本）：

```go
// 扩展 ratelimit.Limiter 支持路径通配符
type EndpointRule struct {
    Pattern string  // e.g. "POST /v1/files/*", "GET /v1/search"
    RPS     float64
    Burst   int
}

// 或在 middleware 中检查路径
key := rateLimitKey(c) + ":" + c.Request.Method + ":" + c.FullPath()
```

更优雅的方式是将 limiter 与路由组绑定——读路由组用高 RPS，写路由组用低 RPS，上传路由组用更低 RPS。由于 gin 的 `RouterGroup` 支持中间件嵌套，可以为不同路由组注册不同 limiter 实例。

### 产品经理视角

速率限制对用户体验的影响是直接的。当全局桶被一个爆发端点耗尽时，所有其他操作（即使是轻量级的）都会被拒绝。用户会困惑"我只是在刷消息列表，为什么被限流了？"

优雅的做法是让限流策略对用户**透明且可预期**：
- 限流时返回清晰描述：`rate.limit_exceeded: upload limit (5/hour) exceeded, retry after 12 minutes`
- 在响应头中加入消耗状态：`X-RateLimit-Resource: upload`、`X-RateLimit-Limit: 10`、`X-RateLimit-Remaining: 0`
- 不同操作有独立的配额，一个端点的爆发不会影响其他端点

---

## 方向五：数据库 Statement-Level 超时与查询资源治理

### 问题陈述

系统配置了 `request_timeout: 30s`（全局 HTTP 超时），但**没有 Statement-Level 的数据库查询超时**。一个慢查询（FTS 扫描大表、缺少索引的过滤、或死锁检测）可能独占数据库连接长达 30 秒，在并发场景下快速耗尽连接池（`max_open_conns: 50`），使所有后续请求排队等待数据库连接。

### 代码级证据

**证据 1：无 per-query context deadline 传播**

Repository 层接收 service 传入的 `ctx` 并通过 `db.WithContext(ctx)` 传递给 GORM，但多数调用链中从未对 `ctx` 设置 deadline：

```go
// internal/repository/comment.go (模式)
func (r *commentRepository) ListByTarget(ctx context.Context, ...) ([]*domain.Comment, string, error) {
    query := r.db.WithContext(ctx).Where(...)
    // ← 如果 ctx 没有 deadline，这个查询可以运行到数据库 driver 的默认超时
    // （通常为 0 = 永远等待）
}
```

**证据 2：只有搜索路径设置了 per-kind timeout**

```go
// internal/service/search.go
const searchTimeout = 5 * time.Second

for _, kind := range kinds {
    searchCtx, cancel := context.WithTimeout(ctx, searchTimeout)
    // 只有搜索路径有独立的查询超时
    // 消息列表、文件列表、通知列表等一切其他查询路径均无超时
}
```

**证据 3：GORM 查询不设置 statement_timeout**

GORM 支持通过 `Set("gorm:query_options", "SET LOCAL statement_timeout = '3s'")` 设置 PostgreSQL session 级别的语句超时，但当前**没有任何一个 repository 方法使用此机制**。

```bash
grep -r "statement_timeout\|statement_timeout" internal/  # 0 结果
```

**证据 4：连接池共享读写路径，慢查询影响所有操作**

```go
// internal/app/app.go
gdb, err := db.Open(db.Options{
    MaxOpenConns:    cfg.Database.MaxOpenConns, // 50
    MaxIdleConns:    cfg.Database.MaxIdleConns, // 10
})
```

所有仓库（文件、消息、评论、通知、搜索、审计、块）共享同一个 `*gorm.DB` 实例和其底层的 `sql.DB` 连接池。一个在 `files` 表上的慢 FTS 查询（全表扫描）会占用一个连接 5 秒，此时如果同时有 10 个搜索请求 + 10 个文件列表请求 + 10 个消息发送事务在并发，连接池可能迅速耗尽。后续的写入操作（消息发送、评论创建）会排队等待空闲连接，增加整个 API 的 P99 延迟。

**证据 5：无数据库连接池健康指标**

```go
// internal/pkg/metrics/metrics.go
// Registry 记录：HTTP 请求 RED、gRPC 请求 RED、SSO 缓存命中率、SSO 延迟、
//               Outbox 发布/失败/积压
// 没有：DB 连接池状态（活跃连接、空闲连接、等待数量、等待时间、连接获取错误）
```

### 架构师视角

数据库超时管理是**防止级联故障的最后一道防线**。当查询因以下原因变慢时（而这些在生产中是**何时发生**而非**是否发生**的问题）：

- 表膨胀导致索引扫描退化
- 并发死锁导致查询排队
- 数据库 CPU 高负载下的查询调度延迟
- 不充分的索引导致顺序扫描

如果没有 statement_timeout，每个慢查询会持有一个连接直到 PostgreSQL `statement_timeout`（默认 0 = 不超时）或 client-side timeout（依赖 Gin 的 `request_timeout` = 30s）触发。在 30s 内，50 连接池可以因为 50 个慢查询而完全阻塞——系统进入"似乎活着但什么都不响应"的状态。

建议：
1. **GORM 层面**：使用 `db.Set("gorm:query_options", "SET LOCAL statement_timeout = '5s'")` 为所有 Ro（读）操作设置 5s 超时，为写操作设置更长时间（10s）
2. **Service 层**：在关键查询路径（列表、搜索）增加 context deadline（搜索已有，消息/文件/通知列表没有）
3. **Metrics**：通过 `db.DB().Stats()` 定期采集 `OpenConnections`、`InUse`、`Idle`、`WaitCount`、`WaitDuration`，暴露在 `/metrics` 端点
4. **读/写分离长期策略**：配置可选的 `database.reader_dsn`，将读查询导向 read-replica

### 产品经理视角

用户不关心数据库连接池。用户关心的是"为什么我发送消息失败了？" 而慢查询导致的连接耗尽会让所有操作间歇性 500 或超时——最糟糕的 UX：**系统看似在线但所有操作都失败**。

一个好的 API 平台应该有：
- **可预测的性能**：慢搜索不应该影响消息发送
- **有意义的错误提示**：`service temporarily degraded` 而不是 `connection pool exhausted`
- **自动恢复**：慢查询超时后自动释放连接，服务自动恢复正常

---

## 优先级与实施路径

| # | 方向 | 类别 | 优先级 | 工作量估算 | 快速起点 |
|---|------|------|--------|-----------|---------|
| 1 | Cursor 分页完整性 | 正确性 / UX | **P1** | 低 | 在 `Page.Cursor` 中嵌入 `expires_at` 时间戳 → 1-2 天 |
| 2 | API 生命周期管理 | 平台成熟度 | **P2** | 低 | 在所有响应头加 `X-API-Version`，标记死代码 → 0.5 天 |
| 3 | Per-Conversation 搜索 | 产品功能 | **P1** | 低 | Repository 层加可选 `peerUID` 参数 → 1-2 天 |
| 4 | Per-Endpoint 限流差异化 | 安全/运维 | **P1** | 中 | 先实现"读写分离桶" → 3-5 天 |
| 5 | DB Statement-Level 超时 | 稳定性 | **P0** | 低 | 在 GORM 连接选项中配置 `statement_timeout` → 0.5 天 |

### 推荐执行顺序

1. **P0: DB Statement-Level 超时**（方向五）——最便宜、最高影响的稳定性加固。配置后即可防止"一个慢查询拖垮整个服务"的场景。
2. **P1: Cursor 分页完整性**（方向一）——低成本的正确性修复。从 cursor 嵌入 `expires_at` 开始。
3. **P1: Per-Conversation 搜索**（方向三）——最高产品价值的低成本特性。复用已有 FTS 基础设施。
4. **P1: Per-Endpoint 限流差异化**（方向四）——安全模型升级。先实现读/写分离，再细化到端点组。
5. **P2: API 生命周期管理**（方向二）——平台长期健康的基础，但当前无紧迫的外部消费者，可放在最后。
