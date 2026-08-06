# aero-id — 资深架构师视角：五项未被既有分析覆盖的高价值扩展方向

> **日期**: 2026-07-10  
> **方法**: 对 `cmd/`、`internal/` 全部 ~200 个 Go 源文件、22 份可逆 SQL 迁移、`api/proto/` 双传输层定义、`configs/config.yaml`、`deployments/`、`Makefile` 做逐文件通读。与 `docs/requirements/` 下 75+ 份既有分析、`docs/REVIEW_*` 下 8 份深度审查文档、`ROADMAP.md`、`ARCHITECTURE.md` 做逐标题/逐关键词交叉比对。  
> **视角**: 资深架构师 × 产品经理  
> **约束**: 不编写任何代码。每个方向附精确的代码位置证据和双视角论证。

---

## 交叉验证摘要

以下关键词在全部 75+ 份既有分析文档中**无系统性覆盖**（标题级零命中、正文零系统性分析）：

| 方向 | 搜索关键词 | 既有文档命中数 | 判定 |
|------|-----------|---------------|------|
| Admin 控制面（角色/权限 API 化） | `grant.*role.*endpoint\|revoke.*role.*api\|list.*role\|list.*user.*role\|role.*management.*api\|permission.*manage` | **0** | ❌ |
| 水平扩展（多实例架构） | `horizontal.*scale\|multi.instance\|distribut.*hub\|cluster.*deploy\|shared.*cache.*redis\|distribut.*ratelimit` | **0** | ❌ |
| 数据可移植性（GDPR 出口/用户生命周期） | `data.*export\|gdpr.*export\|user.*data.*download\|account.*close\|data.*portab\|bulk.*export\|user.*lifecycle` | **0** | ❌ |
| API 开发者体验（OpenAPI/Swagger/Client SDK） | `openapi\|swagger\|api.*document\|client.*sdk\|grpc.*gateway\|api.*portal\|postman\|api.*reference` | **0** | ❌ (仅 2 篇过路提及) |
| 运营面板与 SLO 体系 | `dashboard.*metric\|alert.*rule\|prometheus.*alert\|slo.*enforce\|error.*budget\|grafana.*dashboard\|pager\|on.call` | **0** | ❌ |

---

## 方向一：Admin 控制面 —— 角色/权限系统的 API 化与外露

### 严重性

**产品完整性 · 高** — roles/permissions 的表和 repository 已完整实现，但 API 面几乎为零

### 问题描述

aero-id 在设计之初就预见到了管理面需求：`migrations/000022_admin_roles` 创建了 `roles` 和 `user_roles` 表，`internal/domain/admin.go` 定义了 4 个权限常量（`PermContentModerate`、`PermAuditRead`、`PermUserBan`、`PermSystemConfig`），seed 了两个内置角色（`moderator`、`admin`），`internal/middleware/permission.go` 实现了 `RequirePermission` 中间件，`internal/repository/admin.go` 实现了 `GrantRole`/`RevokeRole`。

**然而这个系统几乎完全没有对外 API：**

- `GET /v1/admin/audit-log` — **唯一的 admin 端点**
- 没有 `POST /v1/admin/roles/grant`
- 没有 `POST /v1/admin/roles/revoke`
- 没有 `GET /v1/admin/users/:uid/roles`
- 没有 `GET /v1/admin/roles` (列出所有可用角色)
- `RequirePermission` 中间件虽然实现了但**没有任何路由使用它**
- gRPC 侧完全没有 admin service（`internal/handler/grpc/admin.go` 不存在）

### 代码级证据

**证据 1：Repository 有 GrantRole/RevokeRole 但无端点调用**

```go
// internal/repository/admin.go:20-24
type AdminRepository interface {
    HasPermission(ctx context.Context, uid, perm string) (bool, error)
    ListAudit(ctx context.Context, action string, page domain.Page) ([]*domain.AuditEvent, string, error)
    GrantRole(ctx context.Context, uid, roleCode, grantedBy string) error   // ← 有实现
    RevokeRole(ctx context.Context, uid, roleCode string) error             // ← 有实现
}
```

但 grep 全库只有 `HasPermission` 和 `ListAudit` 被调用：

```bash
$ grep -rn "GrantRole\|RevokeRole" internal/ --include="*.go"
# internal/repository/admin.go  — 只有定义和实现，零调用
```

**证据 2：RequirePermission 中间件从未被挂载**

```go
// internal/middleware/permission.go:17-20
func RequirePermission(perm string, checker PermissionChecker) gin.HandlerFunc {
```

Grep 全库（除中间件自身定义和测试外）零引用。所有路由在 `router.go` 中注册时没有经过权限过滤——只有原始的 auth 中间件确保用户已登录，但 `moderator` vs `admin` vs `普通用户` 没有区分。

**证据 3：gRPC 侧无 admin service**

```bash
$ ls internal/handler/grpc/admin.go
ls: cannot access 'internal/handler/grpc/admin.go': No such file or directory
```

对比 HTTP 侧有 `internal/handler/http/admin.go`，gRPC 侧完全缺失。

**证据 4：proto 无 admin service**

```protobuf
// api/proto/aero_id/v1/aero_id.proto — 搜索 service 关键字
// 现有: FileService, MessageService, CommentService, NotificationService,
//       BlockService, SearchService, PreferenceService
// 缺少: AdminService
```

### 架构师视角

这个系统已具备角色/权限的**数据层**和**中间件**，但卡在**传输层**——有管子没水龙头。从架构看这是典型的"下半身完整、上半身缺失"。引入 admin API 不需要改动任何现有业务逻辑，只需：

1. 在 proto 中新增 `AdminService`（GrantRole/RevokeRole/ListRoles/ListUserRoles）
2. 在 `internal/handler/grpc/` 和 `internal/handler/http/` 中各新增一个 handler 文件
3. 在 `router.go` 中使用现有的 `RequirePermission(PermSystemConfig, adminRepo)` 中间件

**影响范围**极窄（仅 handler 层），风险极低。

### 产品经理视角

角色系统是 B2B/SaaS 产品的准入门槛。任何多租户或多管理员场景下，运营团队需要：
- 给新入职的运营人员授予 moderator 角色
- 从离职人员回收权限
- 查看当前谁有管理权限
- 审计谁做了什么操作（audit log 已有，但只有查询没有管理）

**用户故事示例：**
> 作为运营主管 Alice，我希望通过 API 将新成员 Bob 设为 moderator，而不是让 DBA 往 `user_roles` 表里 INSERT。

### 建议

添加以下 API 端点：

| 端点 | 方法 | 功能 | 所需权限 |
|------|------|------|---------|
| `/v1/admin/roles` | GET | 列出所有角色及其权限 | `audit.read` |
| `/v1/admin/roles/grant` | POST | 为指定 uid 授予角色 | `system.config` |
| `/v1/admin/roles/revoke` | POST | 撤销指定 uid 的角色 | `system.config` |
| `/v1/admin/users/:uid/roles` | GET | 查询用户的角色列表 | `audit.read` |
| `/v1/admin/users/:uid` | GET | 用户管理信息（角色、状态） | `user.ban` |
| `/v1/admin/reports` | GET | 全局举报队列（所有用户的举报） | `content.moderate` |

实施工作量：**低**（1-2 天，纯 handler 层 + proto）

---

## 方向二：水平扩展架构 —— 从单实例到多实例集群

### 严重性

**架构可伸缩性 · 高** — 当前 4 个核心组件绑死在单实例内存上，无法水平扩展

### 问题描述

aero-id 当前是一个**单实例服务**。虽然代码层面可以启动多个进程监听不同端口，但以下四个组件全部使用**进程内内存**作为存储，导致多实例之间存在数据不一致和功能失效：

| 组件 | 存储位置 | 多实例问题 |
|------|---------|-----------|
| SSE Hub | `sync.Map` (内存) | 用户轮询到实例 A 的 SSE 连接，但通知从实例 B 的 outbox relay 发出，用户收不到实时推送 |
| 限流器 Rate Limiter | `ratelimit.New()` (内存) | 每个实例有独立的 token bucket；用户可通过轮询不同实例来突破 20rps 限制 |
| 幂等键 Idempotency Store | `idempotency.NewGormStore()` (DB) | **这个已使用数据库**，但 cleanup ticker 每个实例独立运行 |
| SSO 缓存 Token/User Cache | `sync.Map` (内存) | 每个实例冷启动后独立预热，cache miss 放大到 SSO；实例 A 已缓存的 token 在实例 B 上重新查询 |

### 代码级证据

**证据 1：SSE Hub 无跨实例通信**

```go
// internal/hub/hub.go:22-27
type Hub struct {
    subs   sync.Map // uid → *uidSubs  ← 进程内
    nextID atomic.Int64
    closed atomic.Bool
}
```

任何需要水平扩展的实时推送系统都会使用外部 pub/sub（Redis、NATS、Kafka），但这里使用的是 `sync.Map`。`hub.Publish()` 只在当前进程内查找 subscriber。

**证据 2：Rate limiter 为进程级 token bucket**

```go
// internal/pkg/ratelimit/ratelimit.go
type Limiter struct {
    mu     sync.Mutex
    rps    float64
    burst  int
    buckets map[string]*bucket  // ← 进程内
    idleTTL time.Duration
}
```

当部署 2 个实例时，每个实例各有 20rps 的配额——总吞吐量提升到 40rps，不受控。

**证据 3：Relay 定时器各实例独立**

```go
// internal/outbox/relay.go
func (r *Relay) Start(interval time.Duration) func() {
    ticker := time.NewTicker(interval)
    go func() {
        for {
            select {
            case <-ticker.C:
                r.drain(ctx)  // ← 每个实例独立 drain
```

多实例时所有实例同时 drain 同一个 outbox_events 表，导致：
- 重复的通知创建（虽然后续处理中有去重，但增加了 DB 压力）
- 竞争条件：两个实例同时 FetchUnpublished 可能拿到同一条事件

**证据 4：Token/User 缓存未共享**

```go
// internal/sso/token_cache.go
type TokenCache struct {
    mu    sync.RWMutex
    entries map[string]*tokenEntry  // ← 进程内
}
```

实例 A 已验证并缓存了 token X，实例 B 收到同一 token 时需要重新请求 SSO。

### 架构师视角

水平扩展能力是 SaaS 产品从 MVP 走向生产级的必经门槛。当前设计意味着：
- 部署 2+ 实例后 SSE 实时推送**不可用**（用户收到 50% 的推送）
- 限流器**形同虚设**（总 RPS 随实例数线性增长）
- 缓存效率**随实例数线性下降**（cache miss 翻倍）
- Outbox relay**竞争加剧**（2 倍 drain 冲突概率）

建议的演进路径：

| 阶段 | 组件 | 方案 | 依赖 |
|------|------|------|------|
| P0 | SSE Hub | 引入 Redis pub/sub 作为跨实例广播 | Redis |
| P0 | Rate Limiter | 引入 Redis 集中式 token bucket | Redis |
| P1 | SSO 缓存 | 引入 Redis 共享缓存（可选，非关键） | Redis |
| P1 | Outbox relay | 引入分布式锁或分片策略 | Redis / PostgreSQL advisory lock |

**为什么选择 Redis？**
- aero-id 已有 PostgreSQL 作为主存储，但 SSE/pub-sub 是 Redis 的强项
- 引入外部依赖增加运维复杂度，但 SSE 的实时性需求决定了不引入 Redis 就无法多实例
- 替代方案：PostgreSQL LISTEN/NOTIFY（但 PostgreSQL 的 NOTIFY 有 8KB payload 上限、无订阅者管理、性能不如 Redis）

### 产品经理视角

从单体到多实例通常是以下业务压力触发的：
- **DAU 突破 10 万** → 单实例 CPU/内存达到极限
- **P99 延迟 > 500ms** → 需要负载均衡分散请求
- **可用性 SLA 要求 > 99.9%** → 需要多实例冗余部署

如果这个服务的目标是承载超过单一实例的流量，Redis 化就是**必经之路**。如果目标只是小团队内部工具，这个方向可以降级为"预留接口但暂不实现"。

### 建议

1. 定义 `Pusher` 接口的多实例实现 `RedisPusher`（`internal/hub/redis.go`），通过 Redis pub/sub 广播事件，各实例的 `Hub` 订阅本实例 UID 相关 channel
2. 为 rate limiter 添加 `RedisLimiter` 后端，使用 Redis 的 `INCR`+`EXPIRE` 实现集中式滑动窗口
3. 添加配置项 `sse.mode`（`local`/`redis`）和 `rate_limit.backend`（`local`/`redis`），保留本地模式作为降级选项
4. Outbox relay 添加 `select_for_update SKIP LOCKED` 或用 Redis 分布式锁分片

实施工作量：**中-高**（7-14 天，涉及基础设施层改造）

---

## 方向三：数据可移植性与用户生命周期管理

### 严重性

**合规 · 中高** — 无用户数据出口和用户关闭账户的生命周期管理

### 问题描述

aero-id 存储用户的文件、消息、评论等数据，但**没有任何机制让用户取回自己的数据**，也没有完整的用户账户关闭流程。这不仅是产品缺口——在 GDPR（第 20 条 数据可移植权）和类似法规下，这可能是合规风险。

### 代码级证据

**证据 1：无数据导出端点**

```bash
$ grep -rn "export\|download.*all\|get.*all.*data\|user.*archive" internal/ --include="*.go" | grep -v "_test.go" | grep -v "file.*DownloadURL\|presign"
# 零命中
```

没有任何端点可以让用户导出自己的消息、评论和文件。

**证据 2：无批量删除端点**

```go
// internal/service/service.go
type MessageService interface {
    Delete(ctx context.Context, id domain.ID) error  // ← 单条删除
    // 缺少: DeleteAllConversation, DeleteAllMessages
}

type FileService interface {
    Delete(ctx context.Context, id domain.ID) error  // ← 单文件删除
    // 缺少: DeleteAllFiles
}

type CommentService interface {
    Delete(ctx context.Context, id domain.ID) error  // ← 单条删除
    // 缺少: DeleteAllComments
}
```

用户如果要"清空我的数据"，需要逐个调用 Delete API——对于有上千条消息的用户来说完全不现实。

**证据 3：没有用户关闭账户的编排逻辑**

用户关闭账户需要：
1. 软删除/硬删除所有消息
2. 软删除/硬删除所有评论
3. 删除所有文件（或转移所有权）
4. 记录审计日志
5. 通知 SSO 该用户已关闭

当前没有任何一个服务方法执行这个编排。这些操作分散在 `fileService`、`messageService`、`commentService` 中，但没有聚合入口。

**证据 4：数据出口格式未定义**

即便逐条查询消息/评论，返回的 JSON 结构是面向 API 消费者而非面向数据迁移的。没有标准化的导出格式（如 JSON Lines、CSV、ZIP 归档）。

### 架构师视角

用户数据出口应设计为**异步任务**：用户发起请求 → 系统异步打包 → 用户通过临时链接下载。这避免了大查询超时和内存 OOM。

架构设计点：

```
POST /v1/user/export  →  创建 ExportTask 行  → 后台 worker 处理  →  完成通知
  (用户触发)              (状态: pending)      (消息→JSONL,       (用户收到链接)
                                                文件→归档ZIP,
                                                评论→JSONL)
```

需要新增的组件：
- `export_task` 表（id, uid, status, created_at, completed_at, download_url, expires_at）
- `ExportService`（编排导出流程，生成 ZIP 归档）
- 后台 worker（可以是独立 goroutine 或复用 outbox 机制）
- 定时清理过期的导出文件

### 产品经理视角

数据可移植性在大陆以外市场是**法律要求**（GDPR、CCPA、LGPD）。即便当前服务不面向欧盟用户，这是一个信任信号——用户知道"我的数据属于我，我可以随时带走"。

**优先级判断：**
| 维度 | 评分 |
|------|------|
| 合规重要性 | ★★★★★ — GDPR 罚款可达全球营收 4% |
| 用户信任 | ★★★★☆ — 数据主权是用户信任的基础 |
| 实施复杂度 | ★★★☆☆ — 主要是编排和归档逻辑，不涉及业务规则变更 |
| 现在做 vs 以后做 | 建议在用户数 < 1 万时实现，数据量大了回填成本高 |

### 建议

1. 新增 `POST /v1/user/export`：发起异步数据导出（返回 task_id）
2. 新增 `GET /v1/user/export/:task_id`：查询导出进度和下载链接
3. 新增 `POST /v1/user/close`：关闭账户（软删除所有数据 + SSO 通知）
4. 实现导出 worker：遍历用户的消息/评论/文件，打包为 JSON Lines + ZIP
5. 导出文件设置 7 天过期，自动清理

实施工作量：**中**（3-5 天）

---

## 方向四：API 开发者体验 —— OpenAPI 规范、gRPC 文档与 Client SDK

### 严重性

**开发者体验 · 中** — 外部集成方无法"发现"API，只能读 Go 源码

### 问题描述

aero-id 有完整的双传输 API 表面（HTTP 38+ 路由，gRPC 38 RPC），但：
- HTTP API **没有 OpenAPI/Swagger 规范**——无法自动生成客户端代码、无法集成 API 网关、新开发者需要读路由代码来猜请求格式
- gRPC 服务虽然有 proto 文件，但**没有发布为可浏览的文档**（protoc-gen-doc、buf registry 等）
- **没有官方客户端 SDK**——调用方需要自己拼 HTTP 请求

### 代码级证据

**证据 1：HTTP 路由器没有 OpenAPI 生成**

```go
// internal/handler/http/router.go — gin engine
// 没有任何 OpenAPI 注释、swaggo 注解、或手动维护的 spec 文件
```

`router.go` 以及所有 handler 文件的函数签名使用 Gin 上下文直接解析请求和序列化响应，没有中间格式（如 OpenAPI 结构体）。

**证据 2：gRPC proto 文件无文档注释**

```protobuf
// api/proto/aero_id/v1/aero_id.proto
message GetFileRequest { string id = 1; }    // ← 无注释说明 id 的格式
message SendMessageRequest {
  string to_uid = 1;
  int32 kind = 2;                             // ← 无注释说明 kind 的取值范围
  string content = 3;
  string file_id = 4;
}
```

proto 文件中的 message 和 field 几乎没有任何注释。虽然 gRPC reflection 可以动态发现服务签名，但无法传达 semantics（"kind=4 表示系统消息"、"cursor 是上一页最后一条的 UUIDv7"）。

**证据 3：无 API 版本化**

```go
// internal/handler/http/router.go
routes := Routes{
    Auth:   e.Group("/v1", authMW...),  // ← 只有 /v1，没有 /v2
}
```

未来引入 breaking change（如群聊中的 Message 结构体变化）时无法平滑迁移。

**证据 4：无 API 变更管理**

`ROADMAP.md` 记录了已交付功能，但 `CHANGELOG.md` 和 `docs/CHANGELOG.md` 只记录变更摘要，没有 API 兼容性说明。

### 架构师视角

对于纯内部服务，API 文档的缺失可以容忍。但 aero-id 的服务结构表明它被设计为**平台服务**（有独立的 SSO、文件存储、消息传递）。如果未来的消费方不止一个前端（比如有移动端、第三方集成、webhook 回调消费者），缺少 API 规范就会导致：

- 每个客户端团队自己抓包/读源码来理解 API
- 没有契约测试（contract testing），后端改动可能导致客户端静默崩溃
- API 网关（Kong、APIGee、AWS API Gateway）无法自动映射路由

建议的工具链：
- **HTTP**: `swaggo/swag` 或 `kin-openapi` 生成 OpenAPI 3.0 规范
- **gRPC**: `buf build` + `buf generate` 集成 `protoc-gen-doc` 或推送到 BSR（Buf Schema Registry）
- **版本化**: proto 包名 `aero_id.v1` → `aero_id.v2`；HTTP 使用 `Content-Type` 协商或 URL 前缀

### 产品经理视角

API 文档直接影响**外部集成方的开发效率**和**平台生态的扩展速度**。

**典型场景：**
> 合作方需要集成消息通知功能到他们的平台。他们没有 API 文档，需要：
> 1. 阅读 Go 源码理解请求/响应格式
> 2. 测试各种边界情况
> 3. 每次后端更新时手动检查兼容性
> 
> 如果有 OpenAPI 文档 + 客户端 SDK：3 天集成 → 3 小时集成

### 建议

| 产出 | 工具 | 优先级 |
|------|------|--------|
| OpenAPI 3.0 spec（HTTP 端点全覆盖） | `swaggo/swag` 或手动维护 | P0 |
| gRPC 文档页面 | `protoc-gen-doc` + GitHub Pages | P0 |
| Postman Collection | 从 OpenAPI 导入后补充环境变量和示例 | P1 |
| Go Client SDK | 基于 OpenAPI 生成或手动编写 | P1 |
| TypeScript/JS Client SDK | 基于 OpenAPI 生成 | P2 |
| API 变更管理流程 | CHANGELOG + 兼容性检查 CI | P1 |

实施工作量：**中**（3-5 天初始规范 + 持续维护）

---

## 方向五：运营面板与 SLO 体系

### 严重性

**可观测性 · 中** — Metrics 已采集但无面板、无告警、无 SLO 定义

### 问题描述

aero-id 已经实现了非常完整的 metrics 采集（RED 指标：Rate/Errors/Duration 覆盖 HTTP 和 gRPC、outbox 延迟、SSO 缓存命中率、断路器状态、存储 GC 进度），但**所有这些指标只停留在 `/metrics` 端点**——没有任何面板将其可视化、没有告警规则在指标异常时通知运维、没有 SLO 定义来衡量服务的健康度。

### 代码级证据

**证据 1：Metrics 已注册但从未被消费**

```go
// internal/pkg/metrics/metrics.go
// 注册了以下指标（实际代码扫描确认）：
// - aeroid_http_requests_total{method, route, status}    ← 计数
// - aeroid_http_request_duration_seconds{method, route, status}  ← 延迟
// - aeroid_grpc_requests_total{method, code}
// - aeroid_grpc_request_duration_seconds{method, code}
// - aeroid_sso_validate_duration_seconds{success}
// - aeroid_sso_cache_hit_ratio
// - aeroid_sso_breaker_state
// - aeroid_outbox_pending_events
// - aeroid_outbox_lag_seconds
// - aeroid_outbox_drain_duration_seconds
// - aeroid_file_gc_purged_total
// - aeroid_file_gc_orphans_swept
```

全库没有 `grafana`、`prometheus.*rule`、`alert`、`dashboard` 目录或配置。

**证据 2：无健康度汇聚端点**

`/healthz` 返回 `200 OK`（存活），`/readyz` 检查 DB + 可选 SSO。但没有**整体健康评分**或**依赖项状态摘要**端点（如 `/health` 返回 JSON 格式的每个依赖状态和延迟）。

**证据 3：无 SLO 定义**

没有任何地方定义：
- P99 延迟目标（例如：文件上传 P99 < 2s，消息发送 P99 < 500ms）
- 错误率预算（例如：5xx < 0.1%）
- 可用性目标（例如：99.9% uptime）

**证据 4：无结构化日志的告警路由**

```go
// internal/pkg/logger/logger.go
// 使用 zap 结构化日志，但日志没有路由到告警系统（如 Sentry、Datadog、PagerDuty）
```

Error 级别的日志（如 outbox 重试耗尽、SSO 断路器打开、存储 GC 失败）当前只写入日志文件，不会触发任何通知。

### 架构师视角

可观测性的**最后一公里**是"数据 → 洞察 → 行动"的闭环。当前 aero-id 完成了"数据"阶段，但"洞察"和"行动"完全缺失：

```
数据采集 (✓) → 指标可视化 (✗) → 告警规则 (✗) → 告警通知 (✗)
                                → SLO 定义 (✗) → 错误预算 (✗) → 发布决策 (✗)
```

建议的推进路径：

| 阶段 | 产出 | 工具 |
|------|------|------|
| P0 | Grafana Dashboard 模板 | Grafana + /metrics scrape |
| P0 | Prometheus 告警规则 | `prometheus-alertmanager` |
| P1 | SLO 定义文档 | Service-Level Indicator 指标 + 目标值 |
| P1 | 错误预算计算 | Prometheus recording rules |
| P2 | 告警到 PagerDuty/OpsGenie | Alertmanager route + receiver |

### 产品经理视角

运维面板和 SLO 对**外部客户不直接可见**，但对**内部工程效率和信心的提升**巨大：

- 没有面板：运维靠"感觉"判断系统是否健康
- 有面板：发现 P99 延迟从 200ms 飙到 2s → 定位到 SSO 缓存命中率从 95% 降到 30% → 发现新实例无缓存 → 修复
- 没有 SLO：不确定是否可以在周五下午发版
- 有 SLO：错误预算还剩 80%，安全发版

### 建议

1. 提交 Grafana Dashboard JSON 到 `deployments/grafana/dashboard.json`
2. 提交 Prometheus 告警规则到 `deployments/prometheus/alerts.yml`
3. 在 `docs/SLO.md` 中定义核心 SLO：

   | 指标 | SLI | 目标 |
   |------|-----|------|
   | 消息发送 P99 | `aeroid_http_request_duration_seconds{route="/v1/messages", quantile="0.99"}` | < 1s |
   | 消息发送错误率 | `aeroid_http_requests_total{route="/v1/messages", status=~"5.."} / aeroid_http_requests_total{route="/v1/messages"}` | < 0.1% |
   | 文件上传 P99 | `aeroid_http_request_duration_seconds{route="/v1/files", quantile="0.99"}` | < 5s |
   | 通知投递延迟 P99 | `aeroid_outbox_lag_seconds` | < 30s |
   | 可用性 | `/readyz` 成功响应率 | ≥ 99.9% |

4. 新增 error-level 日志的告警 hook（可选：集成 Sentry 或简单的 webhook 通知）

实施工作量：**低**（1-2 天面板 + 规则定义；SLO 是持续流程）

---

## 综合优先级矩阵

| # | 方向 | 实施工作量 | 业务价值 | 技术风险 | 建议优先级 |
|---|------|-----------|---------|---------|-----------|
| 1 | Admin 控制面 | **低** (1-2d) | ★★★★☆ | 极低 | **P0** — 零风险完成半成品功能 |
| 2 | 水平扩展架构 | **高** (7-14d) | ★★★★★ (流量场景) / ★☆☆☆☆ (小规模) | 中 | **按需** — Redis 化仅在多实例部署时需要 |
| 3 | 数据可移植性 | **中** (3-5d) | ★★★★☆ (合规) / ★★☆☆☆ (无合规需求) | 低 | **P1** — 建议在用户增长前完成 |
| 4 | API 开发者体验 | **中** (3-5d) | ★★★☆☆ (上下文相关) | 低 | **P1** — 有外部集成方时升为 P0 |
| 5 | 运营面板与 SLO | **低** (1-2d) | ★★★★☆ | 极低 | **P0** — 立即提升可观测性闭环 |

---

## 附录：未采用的候选方向及原因

以下方向经评估后被排除，附原因：

| 候选方向 | 排除原因 |
|---------|---------|
| 群聊/多参与者会话 | 已在多份既有分析中覆盖（`internal/message` 缺少 `ConversationID`） |
| 内容审核面板（Moderation Backplane） | 已覆盖（comment reports + admin roles 的审核扩展） |
| 双架构过渡（旧 internal/service/ vs 新 internal/file/ 等） | 已有分析提及，且主要是 tech debt 而非产品特性 |
| 文件存储配额 | 部分覆盖，实施复杂度高、产品价值取决于业务场景 |
| 消息草稿/Draft | 已有 1 份文档提及，且产品价值有限 |
| Mobile Push Notification | 已有分析覆盖（6 篇提及）且 SSE 已提供实时推送 |
| Feature Flag 系统 | 与产品阶段不符（MVP 阶段，非大型平台） |
| SSO Provider Federation | 当前单 SSO 模型清晰；多 provider 只有在多租户时才需要 |

---

## 总结

这 5 个方向代表了 aero-id 从**功能完整**到**平台成熟**所需的关键跳跃：

1. **Admin 控制面**是**最低成本最高收益**的改进——代码已写了一半，只差 API 封装
2. **水平扩展**是决定服务架构天花板的**架构决策**——是否引入 Redis 决定了未来的部署拓扑
3. **数据可移植性**是**合规护城河**——用户数越多、地域越广，越重要
4. **API 开发者体验**是**平台化起点**——没有文档的 API 不是平台，是内部接口
5. **运营面板与 SLO** 是**工程效率倍增器**——让运维从"救火"变为"数据驱动"
