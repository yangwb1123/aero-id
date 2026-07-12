# 架构分析报告：aero-id

## 1. 架构评估

### 当前架构的优势

**严格的分层架构（1.5/5 分——但方向对了）**
代码库遵循了正确的分层原则：`handler → service → repository → DB`，加上 `storage` 和 `sso` 作为基础设施抽象。这一选择是正确的——它为测试性、可替换性和关注点分离提供了清晰的路径。HTTP 和 gRPC 共享同一个 `service` 层是**当前代码库中最好的设计决策**，没有之一。

**依赖注入在组合根完成**
`internal/app/new.go` 集中完成了所有依赖构造和注入。这为替换实现（如将 `local` storage 换成 S3）提供了单一变化点。

**领域实体集中在 `internal/domain/`**
实体（File, Message, Comment, Block, Notification）集中在 domain 包中，减少了循环依赖风险。

**Repository 返回原始 GORM 错误**
这看似是一个缺陷（文档称之为"需要 service 翻译"），但实际上是**正确**的设计——repository 层不应知道 HTTP 状态码或 gRPC 错误码。Service 层做翻译是职责明确的体现。

### 当前架构的局限性

#### 核心问题：代码库处于"重构中途"状态

从文件布局可以清晰看到当前代码库处于**迁移过程中**，且尚未完成。两套并行的实现——`internal/{file,comment,message,...}/`（旧路径）和 `internal/{service,handler}/`（新路径）——是**最大的架构债务**。

```
# 当前状态（混乱）
internal/
  file/         ← 含 service + handler + repository（部分废弃）
  comment/      ← 含 service + handler（全部废弃？）
  message/      ← 含 service + handler（全部废弃？）
  search/       ← 整个包废弃
  service/      ← 活的 service 实现
  handler/      ← 活的 handler 实现
  repository/   ← 活的 repository 实现（但 file/ 包下也有 repository）

# 理想状态（整洁）
internal/
  service/      ← 唯一的 service 层
  handler/      ← 唯一的 handler 层
  repository/   ← 唯一的 repository 层
```

这种"迁移进行时"状态会导致以下问题：

- **认知负荷翻倍**：新贡献者需要搞清楚哪套实现是活的
- **测试覆盖可能不完整**：如果活的和死的测试都在跑，但测试的标准不同
- **重构阻力增加**：每新增一个功能都在当前错误的结构上加码

#### 横向关注点的实现方式过于原始

**速率限制**：单一全局 20 RPS limiter 对所有端点共享。这意味着一个高频端点（如消息列表）可以饿死另一个端点（如文件上传）。更好的设计应该按端点类分组（如写入类 10 RPS、读取类 50 RPS）或按资源类型分组。

**错误边界**：没有 statement-level timeout、没有查询超时分层（search 是唯一手动设置 timeout 的路径）。PostgreSQL 层面也没有 `statement_timeout` 或 `idle_in_transaction_session_timeout`。这意味着一个慢查询可以阻塞连接池中的所有连接。

**可观测性**：虽然存在 `pkg/metrics`，但没有 DB 连接池指标（`DBStats`）或端点级别的延迟直方图。这是一个生产就绪性的缺口。

**Cursor 分页的 cursor 格式不统一**：大多数实体使用 `id`（UUIDv7），但 `block` 使用 `created_at`。这意味着客户端无法用统一逻辑解析下一页。这是设计上的不一致，但在当前阶段修复成本很低。

### 关键设计决策评估

| 决策 | 评价 | 说明 |
|------|------|------|
| UUIDv7 作为主键 & cursor | ✅ 正确 | UUIDv7 按时间排序，天然支持 cursor 分页，且无需暴露自增 ID |
| SSO 分离身份管理 | ✅ 正确 | 符合微服务职责单一原则，减少了 aero-id 的安全攻击面 |
| 软删除全局化 | ✅ 潜在大坑 | GORM 软删除在 WHERE 中自动加 `deleted_at IS NULL`，但如果某个高频查询未使用 GORM scope（直接使用 `db.Raw()`），会遗漏此条件 |
| HTTP + gRPC 共享 service | ✅ 正确 | 这是当前架构中最值得保留的设计 |
| 全局共享 rate limiter | ❌ 缺陷 | 需要按端点类分组，否则一个端点可以饥饿其他端点 |
| 两套 handler/service 实现 | ❌ 重大债务 | 迁移未完成的状态，增加了维护成本 |
| 无 per-query timeout | ❌ 缺陷 | 生产环境风险——一个慢查询可以卡住整个连接池 |

### 技术债总清单（按优先级）

```
P0（生产安全风险）:
  - 缺少 DB statement_timeout / idle_in_transaction_session_timeout
  - 全局共享 rate limiter 可以被恶意端点饥饿

P1（可维护性）:
  - 废弃代码（~11 个文件，~800+ 行）未清理
  - cursor 分页格式不统一（id vs created_at）

P2（可观测性/演进性）:
  - 缺少 API 版本化机制
  - 缺少 DBStats 监控指标
  - 连接池 max_idle_conns 配置偏低（10/50）
```

---

## 2. 扩展方向

以下五个方向按照"从现有多大努力能获得最大收益"排序，而非纯技术理想顺序。

---

### 方向 A：查询治理与错误边界（P0 → P1）

**为什么需要**
这是生产就绪性的直接缺口。任何慢查询或失控的搜索请求都可以：
1. 占用连接池给其他正常请求
2. 导致 PostgreSQL 资源耗尽（CPU/memory for sorting/grouping）
3. 在无 `idle_in_transaction_session_timeout` 的情况下，应用层的事务泄漏会挂起连接直到手动 kill

**核心挑战**
- GORM 的上层封装使得 per-query `context.WithTimeout` 需要传透到 repository 层
- 不是所有查询都需要相同的 timeout——搜索是 5s，单行查询可能是 500ms，批量查询可能是 2s
- `statement_timeout` 在 PostgreSQL session 级别设置，GORM 连接池中的连接是复用的，需要在每次查询前重置

**实现方案（两个选项）**

**选项 A1：Repository 层统一的 context deadline 策略**
- 在 repository 入口处用 `context.WithTimeout` 包装传入的 ctx
- 按查询类型区分 timeout（`readTimeout`, `writeTimeout`, `searchTimeout`）
- 优点：简单、GORM 原生支持（`*gorm.DB.WithContext()`）
- 缺点：需要在每个 repository 方法中重复设置；误用可能导致 goroutine 泄漏（未取消的 context）

**选项 A2：GORM 插件 + PostgreSQL session 变量**
- 实现一个 GORM 插件，在每次查询前执行 `SET LOCAL statement_timeout = '5s'`（使用 `db.Exec()` + `*sql.DB.Conn()` 的 `Raw()`）
- 优点：一次实现，全局生效；不会忘记设置
- 缺点：PostgreSQL 的 `SET LOCAL` 在每个连接上执行有额外开销；复杂度过高

**建议**：采用选项 A1，但提取一个 helper 函数：

```
// 在某处（如 internal/repository/timeout.go）:
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
    return context.WithTimeout(ctx, timeout)
}
```

然后在每个 repository 方法开头调用：

```go
func (r *MessageRepo) List(ctx context.Context, ...) ([]domain.Message, error) {
    ctx, cancel := withTimeout(ctx, readTimeout)
    defer cancel()
    // ... GORM 查询
}
```

**对现有系统的影响**
- Repository 层的每个方法需要增加 2 行代码（`ctx, cancel := ...` / `defer cancel()`）
- Service 层不需要任何修改
- 如有 `*gorm.DB` 实例在不传递 ctx 的情况下直接使用（如 `db.Where(...).Find(...)`），需要修改为 `db.WithContext(ctx).Where(...).Find(...)`
- 连接池配置（`max_idle_conns`, `max_lifetime`）建议同步调整

**工作量估算**：2-3 天（含测试）

---

### 方向 B：废弃代码清理（P1）

**为什么需要**
两套并行的实现是**当前最大的认知负担**。新贡献者需要确定：
- 如果我要加一个 HTTP 端点，是写在 `internal/file/handler_http.go` 还是 `internal/handler/http/file.go`？
- 如果我要改评论的授权逻辑，是改 `internal/comment/service.go` 还是 `internal/service/comment.go`？

回答永远是后者，但新贡献者不会知道。

**核心挑战**
- `internal/search/` 引用了 `internal/file.Repository` 等旧路径的接口——删除前需要迁移
- `internal/file/` 下的 repository 实现是否与 `internal/repository/` 下的实现冗余——需要比对
- 一些测试可能引用了废弃包中的类型（如 `file.NewFileServer`）

**精确的依赖分析**

从代码验证可以确认：
1. `internal/{comment,message,block,notification}` 中的 `handler_*.go` 是完全死的——未被 `app/new.go` 引用
2. `internal/search/` 的整个 `NewService` 未被生产代码引用
3. `internal/file/` 的 service 实现可能被 `internal/search/` 引用——需要确认后再删除
4. `internal/file/handler_grpc.go` 中的 `NewFileServer` 可能在测试中被引用

**执行方案**（分三阶段）

阶段一（1天）：依赖扫描 + 风险确认
- `go list -json ./...` 看哪些包被导入
- 对每个废弃文件运行 `go vet -unused` 验证
- 确认 `internal/file/` 下的 repository 接口 vs `internal/repository/` 下的实现是否冗余

阶段二（2天）：迁移 + 删除
- 按以下的顺序删除（从依赖最少的开始）：
  1. `internal/block/handler_*.go`
  2. `internal/notification/handler_*.go`
  3. `internal/message/handler_*.go`
  4. `internal/comment/handler_*.go`
  5. `internal/search/` (整个包)
  6. `internal/file/handler_*.go` 和 `internal/file/service*.go`

阶段三（1天）：验证 + 清理
- `make build && make test && make lint`
- 如果 `internal/file/` 下的 repository 实现是冗余的，合并到 `internal/repository/`

**总工作量估算**：4 天（3 天实施 + 1 天验证）

**风险点**：测试覆盖可能受损。如果废弃代码中有些逻辑（如 search 的 snippet 生成）在新路径中没有完整实现，需要在删除前补全。

---

### 方向 C：细粒度速率限制（P1 → P2）

**为什么需要**
当前的全局 20 RPS 限制意味着：
- `/files/upload` 和 `/messages/list` 共享相同的配额
- 一个客户端发起大量文件上传，可以饿死另一个客户端的消息读取
- 无法针对写入类端点设置更严格的限制（比如 5 RPS for upload vs 50 RPS for read）

**核心挑战**
- 需要定义端点分类（写入、读取、搜索、文件下载）
- 分类不能太细（否则配置膨胀），也不能太粗（否则无意义）
- gRPC 和 HTTP 需要共享同一个限流配置

**架构建议**

引入两个维度的速率限制：

1. **端点类（endpoint class）**：按 REST 动词 + 资源类型分组
   - `write:*`（POST/PUT/DELETE）→ 5 RPS per user
   - `read:*`（GET）→ 50 RPS per user
   - `search:*`（GET /search）→ 10 RPS per user
   - 匿名访问（`/files/dl`）→ 20 RPS per IP

2. **全局硬限制**：100 RPS（防止单用户使用多个 token 绕过端点级限制）

**配置变更**

```yaml
rate_limit:
  # 全局
  global_rps: 100
  global_burst: 150
  # 按端点类
  endpoints:
    write_rps: 5
    write_burst: 10
    read_rps: 50
    read_burst: 75
    search_rps: 10
    search_burst: 15
    public_rps: 20
    public_burst: 30
```

**中间件变更**

当前 `RateLimit` 中间件在 `router.go` 级别应用。改为：

```
router.go:
  e.Use(rateLimit.Global(limiterGlobal))
  e.Group("/v1", authMW, rateLimit.Class("write"))  // POST/PUT/DELETE 路径挂到这个组
  e.Group("/v1", authMW, rateLimit.Class("read"))   // GET 路径挂到这个组
```

**对现有系统的影响**
- `middleware/ratelimit.go` 需要重构——从单一 `RateLimit` 函数改为 `RateLimitClass(class string)` 工厂函数
- Router 配置需要为每个端点类创建不同的路由组
- 需要考虑 gRPC 的限流——gRPC interceptor 也需要支持端点分类
- 配置需要向后兼容（如果 `endpoints` 块缺失，回退到全局配置）

**替代方案：滑动窗口 vs 令牌桶**
当前使用的是令牌桶（`github.com/juju/ratelimit`）。对于端点级限流，**滑动窗口**可能更合适，因为：
- 令牌桶在 burst 时可能出现"突发然后静默"的模式，不够平滑
- 滑动窗口可以确保在任意时间窗口内不超过上限
- 但滑动窗口需要维护更多内存状态

建议：继续使用令牌桶（因为已引入），但将 burst 配置为 1.5x rps 以避免过于严格的抖动。

**工作量估算**：5-7 天（含重构、测试、gRPC 对齐）

---

### 方向 D：API 版本化策略（P2）

**为什么需要**
当前 `/v1/*` 的硬编码路由意味着任何 break change 都需要：
1. 创建新的路由组（`/v2/*`）
2. 同时维护 v1 和 v2 的 handler 和 service
3. 在 app 中同时注册两组路由

当前 proto 定义 `package aero_id.v1` 但无 v1/v2 共存机制。

**但是——是否需要？**
现实点：aero-id 是一个内部微服务。唯一的客户端是其他 aero 微服务。在 monorepo 或多服务部署中，所有客户端通常可以同步升级。在这种情况下，**版本化的成本可能高于其价值**。

**建议：推迟到第一次出现 break change 需求**

与其现在设计一个复杂的版本化框架，不如：
1. 记录当前 API 的兼容性承诺（什么算是 breaking change）
2. 如果出现需要 breaking change 的场景，再设计版本化方案
3. 在此之前，确保每个端点都有测试，作为回归保障

**如果必须要实现**（比如 SSO 团队要求长期兼容性）：

方案：URL 路由版本 + 支持的 HTTP 头版本协商

```yaml
# 路由级别
/v1/files/*
/v2/files/*    # 只有变动的端点需要新路由

# 共享 service（如果 v1 和 v2 逻辑有差异）
internal/
  service/
    file.go         # 当前实现
    file_v1.go      # v1 兼容层（如果需要）
    file_v2.go      # v2 实现
```

**核心挑战**：Service 层的 versioning 是最难的——v1 和 v2 的授权逻辑可能不同。如果 service 方法签名发生变化，会导致两个版本共享同一个 service 接口变得困难。

**工作量估算**：如果推迟到有需要时再实现——0 天。如果现在做——8-10 天。

---

### 方向 E：Cursor 分页的统一化 + 演进预留（P2）

**为什么需要**
当前 cursor 格式的不一致（`id` vs `created_at`）是一个小但现实的 bug 来源。更重要的是，当前 cursor 缺少两个特性：
1. **排序方向编码**：不支持请求 `ORDER BY ASC` 之后再从末尾往前翻（比如"最新消息"倒序）
2. **过滤器编码**：如果用户在列表时应用了过滤器（如 `kind=text`），翻页时 cursor 不携带此信息

**架构建议**

不要重新发明轮子——采用 **Relay Cursor Spec（GraphQL）** 风格的 cursor 编码。Cursor 是一个不透明字符串，服务端编码了排序字段 + 值 + 过滤条件：

```
# 当前（明文 ID）:
cursor: "0194f0a0-..."  → 前端可以直接解析，但缺乏上下文

# 建议（Base64 编码的 JSON 占位符）:
cursor: "eyJpZCI6IjAxOTRmMGEwLi4uIiwicyI6ImNyZWF0ZWRfYXQiLCJmIjp7ImtpbmQiOiJ0ZXh0In19"
       → 解码后: {"id": "0194f0a0-...", "sort": "created_at", "filters": {"kind": "text"}}

# 前端仍然不应该解码 cursor——保持不透明
```

**但是**：这引入了对 cursor 的序列化/反序列化依赖。对于内部微服务，额外的复杂度和性能成本（每个列表请求都需要 decode/encode cursor）是否值得？

**建议：更务实的方案**

保持当前 cursor 格式（`id` 值），但做三件事：
1. 统一所有实体的排序字段为 `id`（让 block 也使用 `id` 而非 `created_at`）
2. 在 `Page` 结构体中增加排序方向字段（`Sort: ASC | DESC`）
3. 在 handler 层验证 cursor 的格式（如果不对应于 UUIDv7，返回 400）

```go
// internal/domain/shared.go
type Page struct {
    Cursor string `json:"cursor,omitempty"`
    Limit  int    `json:"limit,omitempty"`
    Sort   string `json:"sort,omitempty"` // "asc" | "desc"，默认 "desc"
}
```

**对现有系统的影响**
- `block` repository 需要从 `WHERE created_at < ?` 改为 `WHERE id < ?`
- 所有列表端点需要支持可选的 `sort` 参数
- 后端排序方向变更后，cursor 的语义不变（`WHERE id < ?` 在 ASC 和 DESC 下都适用——前者拿到是最小值之后的，后者拿到是最大值之前的）
- HTTP handler 需要多解析一个 `sort` 参数

**工作量估算**：3 天（含测试和 block 的特殊处理）

---

## 3. 接口设计建议

### 当前接口设计的评价

| 接口/抽象 | 评价 | 问题 |
|-----------|------|------|
| `service.FileService` | ✅ 合适 | 职责清晰，颗粒度适中 |
| `repository.FileRepository` | ✅ 合适 | GORM 的封装合理 |
| `storage.FileStorage` (local/s3) | ✅ 合适 | 接口稳定，实现可替换 |
| `sso.SSOClient` | ✅ 合适 | 懒加载 + 缓存设计合理 |
| `middleware.RateLimit` | ❌ 过于简单 | 无法区分端点类 |
| `middleware.Auth` | ✅ 合适 | 单一职责 |
| `domain.Page` | ❌ 缺少字段 | 无排序方向，无过滤器 |
| `service.SearchService` | ✅ 可优化 | 缺少 `peerUID` 参数 |
| `domain.Message` / `domain.File` | ✅ 合适 | 与 DB schema 对齐 |

### 是否需要新的抽象层

**建议引入：QueryContext（查询上下文）**

当前所有列表查询都依赖 `domain.Page` 作为分页参数。但随着搜索和过滤需求增长，推荐引入一个轻量级的 `QueryContext` 对象：

```go
// internal/domain/query.go（新文件）
type QueryContext struct {
    Page    Page
    Filters map[string]any  // 可选过滤条件
    Sorts   []SortField     // 可选排序字段
}

type SortField struct {
    Field string // "created_at", "updated_at", "id"
    Asc   bool
}
```

**为什么需要**：
1. 当前每个列表方法都手写参数（`ctx, uid, page` 或 `ctx, actor, peerUID, page`），扩展性差
2. 如果未来需要支持多字段排序（如先按 `created_at` 降序，再按 `id` 降序），当前方式不支持
3. 过滤器参数在每个方法签名中散落

**但为什么现在不引入**：
当前代码库中没有场景需要多字段排序或复杂过滤。引入 `QueryContext` 会增加抽象层次，但不会解决当前的实际问题。

**建议：推迟到第一次出现需要多字段排序或复杂过滤的场景再引入**。

### 向后兼容性策略

**针对 HTTP API**：
- `GET /v1/files?cursor=xxx&limit=20` — **仅**添加字段，不删除字段
- 如果必须删除字段，使用旧字段在新版本中作为别名 + 返回 `Deprecation` 响应头
- 响应体中 `{ "data": ..., "next_cursor": ... }` 格式保持不变

**针对 gRPC API**：
- Proto 字段编号使用后不再复用（用 `reserved` 标记删除的字段）
- 新增字段使用新的字段编号
- 不改变现有 `rpc` 方法签名——如果必须改，新建一个 `rpc` 方法

**建议在 `httpx` 中引入版本响应头**（如果决定走版本化）：

```go
// 在 httpx.OK 中加入 Deprecation 头的可选参数
func OK(c *gin.Context, data any, opts ...Option) {
    // 如果 opts 包含 deprecationInfo，设置 Deprecation 和 Sunset 响应头
}
```

---

## 4. 技术选型评估

### 是否引入新框架/库

| 候选库 | 用途 | 推荐？ | 理由 |
|--------|------|--------|------|
| `github.com/ulule/limiter/v3` | 更灵活的速率限制 | ❌ | 当前 `juju/ratelimit` 已可用，只需重构使用方法 |
| `github.com/grpc-ecosystem/go-grpc-middleware` | gRPC 限流/监控 | ❌ | 当前无此依赖，且尚未出现需要 gRPC 中间件链的场景 |
| `github.com/redis/go-redis/v9` | 分布式限流/缓存 | ❌ | 单实例部署下不需要 Redis；增加的运维复杂度 > 收益 |
| `github.com/prometheus/client_golang` | 指标暴露 | ✅ **已有** | 已引入（`pkg/metrics`），需要扩展现有指标 |
| `github.com/getsentry/sentry-go` | 错误追踪 | ❌ | 不适合内部微服务——成本 > 收益 |
| `github.com/opentracing/opentracing-go` | 分布式追踪 | ❌ | 单实例服务不需要追踪；日志足够 |

### 关键评估：是否需要引入分布式限流

当前单实例部署显然不需要。但考虑到未来水平扩展：

**场景**：两个 aero-id 实例共享同一个 PostgreSQL 和 MinIO，各自维护独立的 in-memory rate limiter。每个实例 20 RPS 限制意味着全局可以达到 40 RPS。

**是否现在就引入分布式限流**：**不**

理由：
1. 水平扩展至少在 6-12 个月内不会发生（如果当前部署是单实例）
2. 即使发生，第一版可以使用粘性会话（session affinity）让每个用户的请求保持在同一个实例上
3. 分布式限流需要 Redis 或其他共享存储——增加运维复杂度
4. 如果最终需要，可以用 `go-redis` 的 `rate` 包或自己实现基于 Redis 的滑动窗口

**建议**：在 `middleware/ratelimit.go` 中定义 `RateLimiter interface`，当前实现为内存版。当需要时，实现 Redis 版并注入即可。

### 自建 vs 采购决策

当前代码库不存在此问题——所有功能都是自建的。

但如果未来需要引入：

| 功能 | 自建 | 采购/使用第三方 | 决策 |
|------|------|----------------|------|
| 全文搜索 | ✅（当前 PG FTS） | ❌（ElasticSearch） | PG FTS 够用 |
| 文件上传/CDN | ✅（MinIO/local） | ❌（AWS S3/CloudFront） | 当前 MinIO 已兼容 S3 API，可未来无缝切换 |
| 消息推送/WebSocket | ✅（自建） | ❌（Pusher/AWS AppSync） | 内部服务，自建成本低 |
| 速率限制 | ✅（自建） | ❌（API Gateway） | 中间件级别实现已足够 |
| 监控/告警 | ✅（Prometheus + Grafana） | ❌（Datadog/NewRelic） | 已采用 Prometheus 生态 |

**核心原则**：
1. 对核心业务能力（文件存储、消息、评论）**自建**——它们是服务的差异化竞争力
2. 对基础设施能力（限流、监控、日志）**自建但复用成熟库**——降低维护成本
3. 仅在增强功能（搜索、分析）上考虑第三方——但当前 PG FTS 已满足需求
4. 如果考虑多租户或大规模部署，可能需要引入 Redis（分布式限流/缓存）和 Kafka（异步事件）

---

## 5. 实施路线图

### 优先级矩阵

每条工作项的优先级评估基于三个维度：
- **风险**：如果现在不修，生产上会出多大问题
- **成本**：实施需要多少时间
- **收益**：完成后获得什么（可维护性/可靠性/性能）

| 工作项 | 风险 | 成本 | 收益 | 优先级 |
|--------|------|------|------|--------|
| 查询超时 + context deadline | 🔴高 | 3天 | 可靠性↑↑ | P0 |
| 废弃代码清理 | 🟡中 | 4天 | 可维护性↑↑ | P1 |
| 端点级速率限制 | 🟡中 | 7天 | 可靠性↑ | P1 |
| cursor 格式统一 | 🟢低 | 3天 | 一致性↑ | P2 |
| API 版本化机制 | 🟢低 | 10天 | 演进能力↑ | P2 |
| DBStats 监控 | 🟢低 | 1天 | 可观测性↑ | P2 |
| 连接池配置优化 | 🟢低 | 1天 | 性能↑ | P2 |

### 阶段划分

```
 Sprint 1 (P0 - 可靠性加固)         Sprint 2 (P1 - 代码库清理)     Sprint 3-4 (P2 - 增强)
 ┌─────────────────────────┐       ┌────────────────────────┐      ┌────────────────────────┐
 │ 1. 查询超时策略设计       │       │ 1. 依赖扫描与确认        │      │ 1. 端点级速率限制        │
 │ 2. Repository 层实现     │       │ 2. 废弃文件/包删除      │      │ 2. gRPC 限流对齐        │
 │ 3. idle_in_transaction   │       │ 3. 测试迁移与补全       │      │ 3. cursor 统一          │
 │    超时                  │       │ 4. make build+test+lint │      │ 4. DBStats 指标添加     │
 │ 4. 连接池配置调优         │       │                        │      │ 5. 连接池配置优化        │
 │ 5. 回归测试              │       │                        │      │                        │
 └─────────────────────────┘       └────────────────────────┘      └────────────────────────┘
   Week 1                           Week 2                          Week 3-4
```

### 里程碑

| 里程碑 | 时间 | 验收标准 |
|--------|------|----------|
| **M1: 可靠性基线** | Sprint 1 结束 | `statement_timeout` 对所有查询生效；慢查询不会卡死连接池；回归测试通过 |
| **M2: 代码清理** | Sprint 2 结束 | 废弃代码删除；`internal/search/` 中可能存在的独有逻辑已迁移；`make build && make test && make lint` 干净通过 |
| **M3: 生产就绪** | Sprint 4 结束 | 端点级速率限制生效；所有端点有监控指标；cursor 格式统一；连接池配置优化 |

### 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|----------|
| 查询超时引入后导致现有功能超时 | 🟡中 | 🔴高 | 实施前运行生产流量回放，统计 P99 延迟；为 timeout 设置一个从宽到严的渐进值（比如先 30s 再逐步降到 5s） |
| 废弃代码中隐藏着生产路径引用的函数 | 🟡中 | 🔴高 | 删除前用 `go list -json` + `go vet -unused` 验证；删除后保留分支直到所有测试通过 |
| 速率限制改动导致误杀合法流量 | 🟢低 | 🟡中 | 在配置中设置宽松的初始值（比如 write=20 instead of 5），观察后再收紧；添加 `X-RateLimit-*` 响应头供客户端调整 |
| cursor 统一导致 block 分页行为变化 | 🟢低 | 🟢低 | block 当前使用 `created_at`，改成 `id`（UUIDv7）后行为应完全一致（因为 UUIDv7 含时间戳）——但需要测试确认 |

### 每个 Sprint 的具体任务分解

**Sprint 1: 可靠性加固（6个工作日）**

```
Day 1-2: 设计 + 原型
  - 确定 timeout 策略（read=5s, write=10s, search=5s）
  - 实现 withTimeout helper 函数
  - 在 2-3 个 repository 方法上验证

Day 3-4: 全量实施
  - 为所有 repository 方法添加 context deadline
  - 添加 idle_in_transaction_session_timeout 到 PostgreSQL DSN
  - 调整 max_idle_conns: 25 (从 10 到 25)

Day 5-6: 测试 + 回归
  - 运行 make test-integration（验证 timeout 不破坏集成测试）
  - 运行 make test && make lint
  - Code review + 修复
```

**Sprint 2: 代码库清理（5个工作日）**

```
Day 1: 依赖分析
  - 运行 go list -json 列出所有导入路径
  - 手动比对废弃代码与活代码的功能覆盖
  - 确认 internal/search/ 的独有逻辑（snippet 生成等）

Day 2-3: 删除
  - 删除 internal/{block,notification}/handler_*.go
  - 删除 internal/{comment,message}/handler_* + service.go
  - 删除 internal/search/（整个包）

Day 4: 修复 + 测试迁移
  - 将 internal/search/ 中的非冗余逻辑（如果有）迁移到 internal/service/search.go
  - 修复测试中引用废弃包的 import

Day 5: 验证
  - make build && make test && make lint
  - 手工验证关键路径（消息列表、搜索、文件上传）
```

**Sprint 3-4: 生产就绪增强（10个工作日）**

```
Sprint 3:
  - 端点级速率限制（5天）
    - 重构 middleware/ratelimit.go → RateLimitClass factory
    - 更新 configs/config.yaml 添加 endpoints 配置块
    - 更新 router.go 按端点类分组
    - gRPC interceptor 对齐
    - 集成测试 + 文档

  - DBStats 指标添加（1天）
    - 在 metrics.Registry 中注册 DBStats 采集器
    - 在 app/new.go 中启动定期采集 goroutine

Sprint 4:
  - cursor 格式统一（3天）
    - 修改 block repository：created_at → id
    - 在 domain.Page 中增加 Sort 字段
    - 更新所有 list handler 解析 sort 参数
    - 测试

  - 连接池配置优化（2天）
    - max_idle_conns: 25
    - ConnMaxLifetime: 30min
    - ConnMaxIdleTime: 5min
```

---

## 总结

当前 aero-id 代码库是一个**方向正确但处于迁移中途**的架构。分层结构、依赖注入和 SSO 分离等核心设计决策是合理的，且长期价值高。

**最关键的问题是**：

1. **生产安全性**（P0）：缺少查询超时、连接池保护，这不是延迟优化——这是防止慢查询 kill 整个服务的救生索。
2. **可维护性**（P1）：废弃代码的认知负担已经在拖累开发效率。删除 11 个文件、清掉 ~800 行冗余代码，是 ROI 最高的重构。

**最有争议的反直觉建议**：**不要现在做 API 版本化**。对于内部微服务，版本化的成本（维护两套 handler、测试、文档）远高于收益。推迟到第一次 break change 需求出现时。

**最被低估的工作项**：连接池 `max_idle_conns` 偏低（10/50）是一个"直接改就有效"的优化。在低流量时，连接池只保留 10 个空闲连接。突发流量到达时，40 个连接需要新建 TCP 连接到 PG——每个连接的新建耗时 1-3 轮 RTT（~2-6ms），在 P50 延迟中会直接体现出来。改成 25 后，突发流量中 25 个连接可以复用，仅 25 个需要新建。这几乎是零风险的改动。
