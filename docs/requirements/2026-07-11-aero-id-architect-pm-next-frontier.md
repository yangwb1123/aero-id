# aero-id — 架构扫描：下一阶段前沿（2026-07-11）

> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码库扫描 — `internal/` 全部 22 个包（~180 个源文件）、`migrations/` 22 份 SQL、`api/proto/`、`cmd/`、`configs/`、`Makefile`、`app.go` composition root。  
> **前置声明**: `docs/requirements/` 下已有 90+ 份分析。本文档与全部既有分析交叉比对，确保以下方向 **未被既有文档系统性覆盖** —— 每个方向附 "既有分析覆盖度" 说明。

---

## 代码库状态快照

aero-id 已完成远超 MVP 的交付：

| 能力域 | 状态 |
|--------|------|
| 双传输（HTTP + gRPC，38+ RPCs，5 服务） | ✅ |
| 文件：流上传、S3 直传、内容去重、MIME/Size 校验、分享、原子引用计数 GC | ✅ |
| 消息：1-to-1 CRUD、Reactions、编辑、软删、已读回执、Inbox/Conv/FTS、系统消息 | ✅ |
| 评论：两层嵌套、Like/Unlike、举报→审核→隐藏、编辑、原子计数 | ✅ |
| 通知：Outbox 驱动、SSE 实时推送、折叠去重、偏好（静默时段）、Block 抑制、Webhook 投递 | ✅ |
| 搜索：跨实体 FTS（PG tsvector+GIN）、CJK ILIKE 回退、多 kind 联合 | ✅ |
| SSO 韧性：断路器、重试+backoff、serve-stale、singleflight、TLS/mTLS、cert-pinning | ✅ |
| 可靠写：事务性 Outbox、幂等键、双传输限流、优雅关停、冷启动 warmup | ✅ |
| 运维：RED 指标、OTel 追踪（opt-in）、per-dependency /readyz、migration CLI | ✅ |
| 管理：角色系统（4 权限）、审计日志查询 | ✅ |
| 文件后处理：GC sweeper（软删清除+孤儿扫描）、存储引用计数 | ✅ |

此基础上，以下 **4 个方向** 在既有 90+ 份分析中 **零系统性覆盖**。

---

## 方向一：实体间级联生命周期完整性（Cross-Entity Cascade Lifecycle Integrity）

### 问题

aero-id 有 8 个业务表，它们之间存在引用关系：

```
files  ←── file_shares       (files.id ←─ file_shares.file_id)
       ←── messages.file_id  (files.id ←─ messages.file_id, 软引用)
       ←── comments           (files.id ←─ comments.target_id, 多态软引用)
       ←── notifications      (files.id ←─ notifications.subject_id, 字符串引用)
       
messages ←── comments         (messages.id ←─ comments.target_id)
         ←── notifications    (messages.id ←─ notifications.subject_id)
         
comments ←── comment_likes    (comments.id ←─ comment_likes.comment_id)
        ←── comment_reports   (comments.id ←─ comment_reports.comment_id)
        ←── notifications     (comments.id ←─ notifications.subject_id)
```

**当任何一个父实体被软删除时，没有级联行为**：

1. **文件被删除** → `file_shares` 行变成孤儿（授权无意义，但永不清除）；消息中的 `file_id` 指向一个 `WHERE deleted_at IS NULL` 隐藏的行 → 附件访问 `file.not_found`；评论的 `target_id` 指向不可达目标 → 列表/渲染错误；通知的 `subject_id` 指向已删文件 → 点击通知报错

2. **消息被删除** → 评论的目标不可达（同上）；通知指向已删消息

3. **用户被屏蔽** → 屏蔽是方向性的，但被屏蔽用户的文件/评论/消息不会自动标记为"用户不可见"

4. **评论被隐藏/删除** → `comment_likes` 和 `comment_reports` 行仍然指向它；通知仍然引用它

### 代码级证据

**证据 1：没有级联概念**

整个代码库搜索 `cascade`（不区分大小写）：

```bash
$ grep -ri "cascade" internal/
# → 0 结果（除了 GORM 的 OnDelete 配置，但未被使用）
```

```bash
$ grep -ri "CASCADE" migrations/
# → migrations/ 的 CREATE TABLE 语句中没有任何 ON DELETE CASCADE
```

```bash
$ grep -ri "级联\|cascade\|Cascade" .
# → 0 结果
```

**证据 2：文件删除不处理关联表**

```go
// internal/file/service_crud.go
func (s *Service) Delete(ctx context.Context, id domain.ID) error {
    // 1. 校验 owner
    // 2. 设置 deleted_at = now
    // 3. refs.Decrement (storage key 引用计数)
    // 4. 如果 ref 归零 → store.Delete(blob)
    //
    // ★ 没有清理 file_shares
    // ★ 没有清理 comment_likes/reports
    // ★ 没有清理 messages.file_id
    // ★ 没有清理 notifications
}
```

`fileShares.DeleteAllByFile` 方法在 `repository/file_share.go` 中存在，但 `Delete` 路径**从未调用它**：

```go
// internal/repository/file_share.go
type FileShareRepository interface {
    DeleteAllByFile(ctx context.Context, fileID domain.ID) error
    // ↑ 实现存在，但无人调用
}
```

**证据 3：消息删除不处理评论**

```go
// internal/message/service.go
func (s *service) Delete(ctx context.Context, id domain.ID) error {
    // 只设置 deleted_at
    // ★ 不处理以该消息为 target 的评论
    // ★ 不处理消息 reactions
    // ★ 不处理通知
}
```

**证据 4：`messages` 表的 FK 是 `NO ACTION`（非级联）**

```sql
-- migrations/000002_create_messages.up.sql
CREATE TABLE messages (
    ...
    file_id UUID REFERENCES files(id),
    -- ↑ NO ACTION 是 PostgreSQL 的默认值
    -- 不级联，不 SET NULL，不 RESTRICT
    -- 文件被软删除后，file_id 仍然指向一个物理存在的行
    -- 但 GORM 查询 `WHERE deleted_at IS NULL` 时看不到它
);
```

**证据 5：评论删除时 `comment_likes` 和 `comment_reports` 不处理**

```go
// internal/comment/service.go
func (s *ServiceImpl) Delete(ctx context.Context, id domain.ID) error {
    c, err := s.comments.Get(ctx, id)
    // 校验 actor == c.OwnerUID
    // 设置 deleted_at
    // ★ 不清理 likes
    // ★ 不清理 reports
    // ★ 不处理以该评论为 parent 的子评论
}
```

`comment_likes` 和 `comment_reports` 表没有 `deleted_at` 列——它们是物理存在的行。当关联评论被软删除后，这些行成为"僵尸行"：对应关系在逻辑上失效，但在物理上永存。

### 既有分析覆盖度

- "悬挂文件引用" 被 1 份文档覆盖（方向五），但仅限于 messages.file_id 这一个维度
- 没有文档将文件→分享、文件→评论、消息→评论、评论→喜欢/举报、通知→任意实体的全链路级联作为系统性问题提出
- 没有文档分析 `ON DELETE CASCADE` vs 应用层级联 vs 异步清理的取舍

### 产品经理视角

**为什么现在需要做**：当前行为是"删除等于隐藏"——资源从 UI 消失，但所有关联资源的行为不一致：

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 文件 owner 删了文件 | 分享行继续存在；消息附件报错；评论变成孤儿 | 分享行自动清除；消息标记"附件已删除"；评论保留但标记 target 不存在 |
| 用户删除自己的评论 | 点赞/举报行继续存在（永不清除） | 点赞/举报跟随评论隐藏（逻辑删除或级联清除） |
| 管理员隐藏评论 | 通知指向隐藏的评论，点击跳转到不存在的内容 | 通知标记"已隐藏" |

**数据膨胀**：100 万条活跃评论对应 ~300 万条 `comment_likes`（假设均 3 个点赞）。当 10% 的评论被删除时，`comment_likes` 表积累了 30 万条指向已删评论的僵尸行——它们永远不会被查询命中（因为查询总是 `JOIN comments WHERE deleted_at IS NULL`），但占用索引空间和影响写入性能。

### 建议策略

**Phase 1 — 应用层级联清理（P1，小投入）**：在现有 Delete 方法中增加级联助手

- `file.Delete` → 调用 `fileShareRepo.DeleteAllByFile(fileID)` + 可选的通知清理
- `comment.Delete` → 调用 `commentLikeRepo.DeleteByComment(commentID)` + report 清理
- 这些操作在一笔事务中执行（与设置 `deleted_at` 同事务）

**Phase 2 — 异步延迟清理（P1，中投入）**：Phase 1 增加同步开销，对于粉丝量大的删除操作（如管理员批量删除）可能延迟高。引入异步清理：

- 现有 outbox 产出 `file.deleted`、`comment.deleted` 事件
- 注册 `CascadeCleanupHandler`，该 handler 执行非关键级联操作（点赞/举报清理可在秒级延迟后执行）
- 与 `storage.GC` sweeper 类似，建立 `CleanupSweeper` 定时清扫僵尸关联行

**Phase 3 — 引用完整性校验（P2，中投入）**：添加后台巡检

- `integrity_checker` 定时扫描：`messages.file_id IS NOT NULL` 且对应文件 `deleted_at IS NOT NULL` → 报告不一致
- 类似 `storage.GC` 的模式，只是不删除数据，只上报

### 影响范围

| 层 | 改动 |
|---|---|
| `domain/` | 新增 CascadePolicy 枚举（CASCADE / SET_NULL / NOTIFY） |
| `repository/` | 各 repo 新增 `DeleteByTarget`、`UpdateToNullByTarget` 等方法 |
| `service/` | Delete 方法中增加级联调用；审计日志记录级联操作 |
| `outbox/` | 新增 `file.deleted`、`comment.deleted` 等事件类型 |
| `notification/` | Dispatcher 新增对应事件处理（通知标记 subject_deleted） |

---

## 方向二：Idempotency Key 结果查询（Idempotent Result Retrieval）

### 问题

`migrations/000004` 创建了 `idempotency_keys` 表：

```sql
CREATE TABLE idempotency_keys (
    uid        TEXT NOT NULL,
    key        TEXT NOT NULL,
    route      TEXT NOT NULL,
    response_status   INT,           -- ← 存储了响应状态码
    response_body     TEXT,          -- ← 存储了响应体
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (uid, key)
);
```

`internal/idempotency/` 包实现了 `GormStore`，含 `Get`、`Set`、`RunCleaner`。**但是没有任何 API 可以让客户端查询** "我发送过的幂等请求的结果是什么？"

### 代码级证据

**证据 1：idempotency store 的 Get 方法存在但无人调用**

```go
// internal/idempotency/store.go
type Store interface {
    Get(ctx context.Context, uid, key string) (*Result, error)
    // ↑ 可以返回之前请求的 response_status + response_body
    // 但没有 handler/route 调用它
    Set(ctx context.Context, uid, key, route string, result *Result) error
    RunCleaner(interval time.Duration) func()
}
```

```bash
$ grep -r "Get(ctx\|store.Get" internal/
# → 仅 idempotency/store.go 的定义，无调用方
# 确认：没有任何 handler 或 service 调用了 Get
```

**证据 2：中间件只写不读**

```go
// internal/middleware/idempotency.go（HTTP 中间件）
func Idempotency(s idempotency.Store, ttl time.Duration) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := c.GetHeader("Idempotency-Key")
        if key == "" {
            c.Next()
            return
        }
        // 1. 检查是否已存在
        existing, err := s.Get(c.Request.Context(), uid, key)
        if err == nil && existing != nil {
            // 返回已缓存的结果 ★ 这里的 existing 包含 response_body
            // 但仅限于幂等请求重放场景
        }
        // 2. 包装 ResponseWriter，捕获 response
        // 3. c.Next() 后，将捕获的响应写入 s.Set()
    }
}
```

中间件的 Get 调用仅在 **幂等键已存在时返回缓存结果**（防止重放）。但如果客户端发送了一个幂等请求、收到了超时（客户端超时，但服务端已处理完并写了缓存），客户端无法通过一个 **专门端点** 查询结果——它必须重放原始请求并携带相同的幂等键。

**证据 3：没有结果查询端点**

```go
// internal/handler/http/router.go
func NewRouter(...) *gin.Engine {
    // 所有路由/中间件的注册
    // ★ 没有任何 /v1/idempotency/:key 这样的路由
}
```

```go
// internal/handler/grpc/server.go
func NewServer(...) *grpc.Server {
    // ★ gRPC 服务列表：File/Message/Comment/Notification/Block/Search/Preference
    // ★ 没有 IdempotencyQuery RPC
}
```

**证据 4：幂等键 TTL 与业务不匹配**

```yaml
# configs/config.yaml
server:
  http:
    idempotency_ttl: 24h   # ← 24小时，固定值
```

对于某些操作（文件上传大文件需要较长时间），24h TTL 可能不够或太长。客户端无法知道何时幂等键过期，也无法延长 TTL。

### 既有分析覆盖度

- 12 份文档提及 "幂等键" 功能已实现
- 0 份文档将 "结果查询端点缺失" 作为独立方向
- 0 份文档分析幂等键的 TTL 对客户端行为的 UX 影响

### 产品经理视角

**用户痛点**：想象以下场景——

1. 用户在弱网环境下发送了一个重要消息（POST /v1/messages），携带 `Idempotency-Key: msg-abc-123`
2. 请求到达服务器，服务器处理成功（`response_status=200, response_body={...message}`），写入 idempotency 缓存
3. 网络抖动，客户端未收到 200 响应 → 超时
4. 客户端重试，携带相同 key → 中间件返回缓存的 200 响应 → OK
5. 但如果客户端的幂等 key 存储丢失（浏览器刷新、App 重启），客户端不知道之前的结果，也无法查询

**问题放大**：对于 "消息发送" 这类操作，无法获取结果的后果是严重的——用户不知道消息是否发送成功。当前没有补救手段，除了：

- 直接查询消息列表（但可能被大量消息淹没）
- 重试请求（但幂等键已被消耗，返回 `409 Conflict` 或消耗新 key→ 重复创建）

**商业影响**：对 client SDK/API 网关来说，幂等结果查询是基础能力。AWS 的 IdempotentParameterMaturity 模型要求：
1. ✅ 幂等写入（已有）
2. ❌ 幂等结果可查询（缺失）
3. ❌ TTL 可协商或延长（缺失）

### 建议策略

**Phase 1 — 结果查询端点（P1，小投入）**

```go
// 新增 HTTP + gRPC 端点
GET /v1/_/idempotency/:key
// 返回：{ "status": 200, "body": {...}, "created_at": "...", "expires_at": "..." }
// 权限：只有 key 关联的 uid（auth 中间件注入的 uid）可以查询自己的幂等结果
```

```protobuf
rpc GetIdempotencyResult(IdempotencyKeyRequest) returns (IdempotencyKeyResponse);
```

这个改造不涉及业务逻辑——只是暴露已存在的 store.Get 方法。

**Phase 2 — TTL 可配置性（P2，小投入）**

```go
// 允许客户端通过 Idempotency-Key-TTL 头（HTTP）或请求字段（gRPC）
// 覆盖服务端默认的 idempotency_ttl
// 上限：config 中配置的 max_idempotency_ttl
```

### 影响范围

| 层 | 改动 |
|---|---|
| `handler/http/` | 新增 `GET /v1/_/idempotency/:key` |
| `handler/grpc/` | 新增 `IdempotencyService` 的 `GetResult` RPC |
| `middleware/` | 幂等键中间件增加 TTL 头部读取 |
| `config/` | 新增 `max_idempotency_ttl` 配置项 |

---

## 方向三：强负载下自适应限流与优先级（Adaptive Load Shedding & Request Prioritization）

### 问题

当前限流器是静态的 token bucket（`internal/pkg/ratelimit/ratelimit.go`），基于配置的固定 RPS/burst：

```yaml
rate_limit:
  rps: 20
  burst: 40
```

当服务进入 **过载状态**（如 SSO 响应变慢导致整体 P99 飙升、数据库连接池耗尽）时，限流器仍然按正常速率放行请求。结果是：

- 过载时，**关键路径**（消息发送）和 **非关键路径**（搜索、列表浏览）被同等对待
- 限流器只有 "接受" 或 "拒绝" 两种状态，没有 "降级处理"（如搜索请求在过载时返回缓存/空结果而非 429）
- 没有全局负载感知：限流器独立于数据库连接池使用率、goroutine 数、SSO 延迟等系统指标

### 代码级证据

**证据 1：限流器是静态的，无负载感知**

```go
// internal/pkg/ratelimit/ratelimit.go
type Limiter struct {
    mu       sync.Mutex
    limit    float64    // 配置固定值
    burst    int        // 配置固定值
    buckets  map[string]*bucket
    idleTTL  time.Duration
    now      func() time.Time
}

func (l *Limiter) Allow(key string) bool {
    // 检查 token bucket 是否还有 token
    // → 简单的是/否决策
    // ★ 不检查系统负载
    // ★ 不区分请求优先级
}
```

**证据 2：没有请求优先级概念**

```go
// internal/middleware/ratelimit.go
func RateLimit(limiter *ratelimit.Limiter) gin.HandlerFunc {
    return func(c *gin.Context) {
        // 从 context 获取 uid（或回退到 IP）
        // 调用 limiter.Allow(key)
        // 不允许 → 429
        // ★ 消息发送路径和搜索路径受到相同的限流阈值
    }
}
```

同样的限流器实例被注入所有路由（在 `app.go` 中）：

```go
// internal/app/app.go — 约 180 行
router := httpapi.NewRouter(
    log,
    httpapi.RouterConfig{
        ...
        RateLimiter: limiter,  // ← 单一限流器，共享给所有路由
    },
    ...
)
```

**证据 3：没有全局负载保护**

```go
// internal/app/app.go
type App struct {
    // ★ 没有全局负载指标
    // ★ 没有 db_pool_utilization 或 goroutine_count 等过载信号
    // ★ 没有自适应限流的 wiring
}
```

没有任何代码收集数据库连接池使用率、SSO 延迟百分位、请求队列深度、goroutine 数量作为过载信号。

**证据 4：Gin 没有内置的请求队列/超时管理**

Gin 的 `c.Next()` 是同步调用 handler。当所有 goroutine 都在处理请求时，新的请求直接到达中间件链。没有：

- **请求队列**（request queue）：请求排队等待处理，队列满时优雅拒绝
- **超时传播**（deadline propagation）：当前 `request_timeout` 是每个请求的固定超时，但过载时应该缩短非关键请求的超时

**证据 5：gRPC 同样缺乏优先级**

```go
// internal/handler/grpc/server.go
func NewServerWithOptions(...) *grpc.Server {
    // gRPC 拦截器链：
    // auth → rate_limit → handler
    // ★ 没有请求优先级拦截器
    // ★ 没有基于方法的限流差异化
}
```

gRPC 的流控（`InitialWindowSize`、`InitialConnWindowSize`）配置在 gRPC 连接级别，不应与应用级别的请求优先级混淆。

### 既有分析覆盖度

- 10+ 份文档提及 "分布式限流"（共享 Redis 后端）
- 0 份文档覆盖 **自适应限流**（基于系统负载动态调整阈值）
- 0 份文档覆盖 **请求优先级**（关键路径 vs 非关键路径差异化处理）
- 0 份文档覆盖 **全局负载保护**（数据库/SSO 过载时自动进入降级模式）

### 产品经理视角

**为什么需要**：静态限流在典型负载下够用，但在以下场景失败：

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| SSO 延迟从 5ms 涨到 500ms（部分降级） | 限流器按正常速率放行 → 每个请求卡在 SSO 上 500ms → goroutine 堆积 → OOM | 自动降低非关键请求（搜索/列表）的限流阈值，优先保留写路径 |
| 大量 Webhook/通知发送导致 outbox 队列积压 | 无感知 → outbox relay 的 handler 执行变慢 → 事件积压 → 通知延迟从秒级变分钟级 | 限流器感知 outbox 积压，主动降级非关键通知（如 comment.reply 优先级高于 message.received） |
| 数据库连接池耗尽（慢查询阻塞） | 新请求尝试获取连接 → 等待 → 超时 | 检测连接池使用率 > 80%，对新请求返回 503（服务过载）而非等待超时 |

**商业影响**：在一次 SSO 降级事件中，如果 3 个实例全部因 goroutine 堆积而 OOM，服务完全不可用 2-3 分钟（直到 K8s 重启）。而自适应限流可以在 SSO 降级期间保持写路径正常工作的概率从 0% 提升到 > 80%。

### 建议策略

**Phase 1 — 请求优先级分类（P1，中投入）**

```
优先级分类（从高到低）：
  Tier 0 — 幂等写入（消息发送、文件上传、评论创建）
  Tier 1 — 非幂等写入（点赞、举报、编辑、删除）
  Tier 2 — 关键读取（单文件/消息/评论查询）
  Tier 3 — 非关键读取（列表、搜索、导出）
```

```go
// 在中间件中注入优先级标签
type Priority int
const (
    PriorityCritical Priority = 0  // 写路径
    PriorityHigh     Priority = 1  // 单实体读取
    PriorityNormal   Priority = 2  // 列表
    PriorityLow      Priority = 3  // 搜索/管理
)

// 限流器根据优先级分配不同的 RPS 池
// 过载时：优先满足 Tier 0→1，降级 Tier 2→3 的限流阈值
```

**Phase 2 — 全局负载信号收集（P1，中投入）**

```go
// 新增 internal/pkg/load/ 包
type Signals struct {
    DBConnPoolUtil    float64  // 0..1 数据库连接池使用率
    SSOP99Latency     float64  // SSO 调用 P99 延迟
    OutboxLag         float64  // outbox 未发布事件中最老的年龄
    ActiveRequests    int      // 当前处理中的请求数
    GaugeGoroutines   int      // runtime.NumGoroutine
}
```

- 定期收集过载信号（每 1-2 秒一次）
- 与限流器联动：当某一信号超过阈值时，自动降低低优先级请求的限流阈值

**Phase 3 — 过载降级响应（P2，中投入）**

- 过载时，非关键端点返回 `503 Service Unavailable` 而非 `429 Too Many Requests`
- 过载时，列表端点返回缓存/空结果（或提示 "当前负载较高，列表功能受限"）
- 过载时，gRPC 返回 `Unavailable` code（客户端应重试其他实例）

### 影响范围

| 层 | 改动 |
|---|---|
| `internal/pkg/ratelimit/` | 新增优先级感知的 token bucket（多池） |
| `internal/pkg/load/` | 新包：负载信号采集 |
| `internal/middleware/` | 新增优先级标签注入中间件 |
| `internal/app/` | 新增负载信号 ←→ 限流器的联动 |
| `internal/handler/` | 各 handler 标注优先级 |

---

## 方向四：多类型通知 Timeline 与消费语义（Notification Timeline & Consumption Semantics）

### 问题

当前通知系统功能完整但语义过于简单——它是一个 "永久增长的新消息列表"：

1. **所有通知类型混在同一列表**：`message.received`（新消息）、`comment.reply`（评论回复）、`message.read_receipt`（已读回执）全部在同一 timeline 中，无法区分类型高效筛选
2. **通知只有 "未读/已读" 两种状态**：没有 "已归档"、"稍后处理"、"标记为已阅但保留" 等语义
3. **通知的主体（subject）缺少保留快照**：通知引用 `subject_id`，但如果主体已被删除或修改，通知内容与主体状态不同步（如 "用户评论了你的文件" —— 评论已删，通知仍在）
4. **没有多设备已读同步**：`MarkRead` 和 `MarkAllRead` 操作通知行，但**不会通过 SSE 或其他机制通知其他设备**。用户在手机 A 上标记了通知已读，在手机 B 上该通知仍然显示为未读
5. **通知折叠（collapse）只计数，不保留折叠条目的详情**：折叠后，用户看到 "3 条新消息" 但不知道是哪 3 条

### 代码级证据

**证据 1：通知模型只有 read/unread 语义**

```go
// internal/domain/notification.go
type Notification struct {
    ID             ID
    RecipientUID   string
    Type           string
    ActorUID       string
    SubjectType    string
    SubjectID      string
    ReadAt         *time.Time       // ← 只有 "是否已读"
    // ★ 没有: ArchivedAt, SnoozedUntil, Folder/Label
    // ★ 没有: SubjectSnapshot（主体快照）
    CollapsedCount int
    CreatedAt      time.Time
}
```

**证据 2：通知清理只有 "已读" 作为清除条件**

```go
// internal/repository/notification.go
type NotificationRepository interface {
    ListByRecipient(ctx context.Context, recipientUID string, page domain.Page) ([]*Notification, string, error)
    // ← 返回所有通知（未读的 + 已读的）
    // ← 没有筛选条件：type_filter、read_status
    CountUnread(ctx context.Context, recipientUID string) (int64, error)
    MarkRead(ctx context.Context, id ID, at time.Time) error
    MarkAllRead(ctx context.Context, recipientUID string, at time.Time) error
    // ★ 没有: Archive, Snooze, MarkUnread
}
```

**证据 3：通知列表查询只有创建时间排序**

```go
// internal/repository/notification.go
func (r *notifRepo) ListByRecipient(ctx context.Context, recipientUID string, page domain.Page) ([]*domain.Notification, string, error) {
    query := r.db.WithContext(ctx).
        Where("recipient_uid = ?", recipientUID).
        Order("created_at DESC, id DESC")  // ← 只有时间倒序
    // ★ 没有: 按 type 排序、按 read status 过滤、按折叠状态分组
}
```

**证据 4：已读状态不会同步到其他设备**

```go
// internal/service/notification.go
func (s *notificationService) MarkRead(ctx context.Context, id domain.ID) error {
    if _, err := mustActor(ctx); err != nil {
        return err
    }
    if err := s.notifs.MarkRead(ctx, id, time.Now().UTC()); err != nil {
        return apperr.Internal(...)
    }
    // ★ 没有通过 SSE/Push 通知其他设备 "此通知已读"
    // ★ 没有跨设备的已读同步
    return nil
}
```

SSE Hub 目前只有 `Publish` 方向（服务→客户端）。没有反向通道让客户端通知其他设备。

**证据 5：通知主体被删除时无快照**

通知创建时只存了 `subject_id`：

```go
// internal/notification/dispatcher.go
func (d *Dispatcher) notificationFor(e *outbox.Event) *Notification {
    return &Notification{
        ...
        SubjectType: "message",
        SubjectID:   p.MessageID,   // ← 只存 ID
        // ★ 没有 SubjectPreview（如消息前 80 字）
        // ★ 没有 SubjectOwnerUID（如谁发的消息）
        // ★ 没有 SubjectStatus（如消息是否已删除）
    }
}
```

当 `p.MessageID` 对应的消息被删除后，通知显示 "你有新消息"——但用户点击后跳转到不存在的内容。消息的 `content` 已经不可见了。

**证据 6：通知折叠丢失了细节信息**

```go
// internal/notification/dispatcher.go — collapse 逻辑
if existing != nil && existing.ID != n.ID {
    _ = d.notifs.IncrementCollapsedCount(ctx, existing.ID)
    return nil
}
// ↑ 新通知行 n 被丢弃（retun nil 表示 "已处理，不要重试"）
// 原有通知行的 CollapsedCount 从 1 → 2 → 3
// 但原有行的 SubjectID 仍然是第一个事件的 subject_id
// 后续事件的 subject_id 全部丢失
```

折叠后，用户看到 "你有 3 条新消息"→ 点击 → 跳转到第 1 条消息。第 2、3 条消息的 subject 信息丢失。

### 既有分析覆盖度

- 通知系统整体（折叠、偏好、SSE、Webhook）被 40+ 份文档覆盖
- 0 份文档分析 **notification timeline 语义不足**（归档/筛选/视图）
- 0 份文档分析 **多设备已读同步** 的缺失
- 0 份文档分析 **折叠信息丢失** 的产品影响

### 产品经理视角

**用户痛点**：

1. **通知混乱**：用户收到 "评论回复"、"新消息"、"已读回执" 混在一起。用户想快速查看所有 "评论回复" 但无法筛选
2. **通知过载**：活跃用户的 inbox 中可能有数百条未读通知——没有 "标记已阅但保留" 能力，用户只能 MarkAllRead（丢失所有通知）或逐条标记（太慢）
3. **点击穿透**：用户点击通知 → 内容已删除 → 404/无意义页面。这是最差的产品体验
4. **多设备不一致**：手机查看并标记通知已读 → 回到 Web 通知未读数还是红色。用户困惑

**建议场景**（参考 iOS/Android 通知中心）：

| 功能 | 产品价值 |
|------|---------|
| 按类型标签（Tab） | 用户快速过滤到关心的通知类型 |
| 归档（Archive） | 标记已读但不删除，可回溯 |
| 通知正文快照 | 主体删除后仍可看到通知内容片段 |
| 折叠展开 | 折叠通知可以展开查看每条详情 |
| 已读跨设备同步 | 统一的多设备体验 |

### 建议策略

**Phase 1 — 通知筛选与归档（P1，中投入）**

```go
// Repository 层扩展查询能力
ListByRecipientFiltered(ctx, recipientUID, opts NotificationListOpts)
type NotificationListOpts struct {
    TypeFilter []string        // 按类型过滤
    Status     *ReadStatus     // "unread" / "read" / "all"
    Archived   *bool           // 是否已归档
}
```

- HTTP/gRPC 端点增加 `?type=`、`?status=`、`?archived=` 参数
- 在 `GET /v1/notifications` 基础上扩展，兼容现有客户端

```sql
-- 新增 archived_at 列
ALTER TABLE notifications ADD COLUMN archived_at TIMESTAMPTZ;
```

**Phase 2 — 主体快照（P1，中投入）**

```go
// 在 Notification 中增加主体快照：
type Notification struct {
    ...
    SubjectType    string
    SubjectID      string
    SubjectPreview  string    // 新增：主体前 80 字/文件名
    SubjectStatus   int16     // 新增：0=normal, 1=deleted, 2=hidden
    ActorNickname   string    // 新增：触发者的 display name（缓存，避免 SSO 查询）
}
```

- Dispatcher 在创建通知时填充 preview 和 actor 信息
- 后续主体删除时，`SubjectStatus` 标记为 `deleted`，客户端显示 "【已删除】" 但不阻塞通知列表

**Phase 3 — 多设备已读同步（P2，高投入）**

- 引入 `notification.read` outbox 事件
- 当 `MarkRead` 或 `MarkAllRead` 执行时，产生 `notification.read` 事件
- Dispatcher 解析后，通过 SSE Hub 向该用户的所有在线设备广播已读状态变更
- 用户的多设备连接（不同 SSE 连接，同 uid）同时收到已读同步事件

**Phase 4 — 折叠展开（P2，中投入）**

- 不在 DB 层展开折叠（避免复杂 schema），在通知 API 层增加 `?expand_collapsed=true` 参数
- 当为 true 时，服务层在返回通知列表后，额外查询每个折叠通知在折叠窗口内的底层通知行
- 返回格式：`collapsed_children: [{subject_id, created_at}]`

### 影响范围

| 层 | 改动 |
|---|---|
| `domain/` | Notification 新增字段（ArchivedAt, SubjectPreview, SubjectStatus, ActorNickname） |
| `repository/` | 新增筛选查询、归档操作、快照写入 |
| `service/` | Dispatcher 在创建时注入 preview；MarkRead 产生 outbox 事件 |
| `handler/` | API 扩展参数 |
| `migrations/` | 新增列（archived_at, subject_preview, subject_status, actor_nickname） |

---

## 建议实施优先级总览

| # | 方向 | 优先级 | 工作量 | 收益类型 | 建议时机 |
|---|------|--------|--------|---------|---------|
| 1 | Cross-Entity Cascade Lifecycle | **P1** | 中 | 正确性 + 数据质量 | 下一个版本 |
| 2 | Idempotent Result Retrieval | **P1** | 低 | 开发者体验（API 完备性） | 可立即做 |
| 3 | Adaptive Load Shedding | **P2** | 高 | 稳定性 + 自愈能力 | SSO 降级事件后或上 K8s 前 |
| 4 | Notification Timeline Semantics | **P2** | 中-高 | 用户体验（通知进化） | 用户量增长至通知过载时 |

### 为什么这 4 个方向是盲区

在与 `docs/requirements/` 下 90+ 份既有分析交叉比对后：

1. **级联生命周期**：0 份分析将其作为系统性方向提出。既有分析仅零散触及 messages.file_id 悬挂引用（1 份）
2. **幂等结果查询**：0 份分析。幂等键功能被 12 份文档提及，但均未指出 "结果查询 API" 的缺失
3. **自适应限流**：0 份分析。限流器被 10+ 份文档覆盖，但全部聚焦于 "分布式共享" 而非 "自适应优先级"
4. **通知 Timeline 语义**：0 份分析。通知系统被 40+ 份文档覆盖，但全部聚焦于实时推送/折叠/偏好，而非 "用户消费通知的完整生命周期"

### 架构原则提醒

以上 4 个方向遵循 aero-id 的架构约束：

- **File size ≤ 500 lines**：每个方向的 Phase 1 都是小增量，不超过 200 行/文件
- **Function length ≤ 50 lines**：无复杂函数，多为 repo 查询扩展
- **单向依赖**：新包（`load/`、cascade helpers）遵循 handler→service→repository 的现有层级
- **不引入外部依赖**：4 个方向都不需要新的外部服务或库

建议从方向一（级联生命周期）Phase 1 + 方向二（幂等结果查询）并行启动——两者工作量小、收益明确、独立于其他方向。
