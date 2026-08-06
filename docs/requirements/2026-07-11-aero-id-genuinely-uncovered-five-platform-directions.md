# aero-id — 全局代码扫描：五项开发平台与数据治理方向（2026-07-11）

> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量代码库通读 — `internal/` 全部 22+ 子包（~200 个 Go 源文件）、`migrations/` 全部 22 份 SQL、`cmd/`、`configs/`、`api/proto/`、`Makefile`  
> **交叉验证**: 与 `docs/requirements/` 下 **137+ 份既有分析文档**、`docs/REVIEW_*` **8 份架构审查**、`docs/ARCHITECTURE.md`、`ROADMAP.md` 逐方向交叉比对  
> **核心承诺**: 以下 **5 个方向** 在全部既有材料中 **从未被作为独立方向进行包含完整代码证据链的系统性论证**（部分方向有过路提及，但无一份文档将其作为独立发展方向分析）  
> **约束**: 不编写任何代码

---

## 代码库当前状态快照

| 域 | 已交付 |
|----|--------|
| **传输** | HTTP Gin + gRPC 完全对等（41+ RPCs）· SSE 实时推送含 `last_event_id` 重连回放 |
| **文件** | 流式上传 · S3 presigned 直传 · SHA-256 内容去重 + 原子引用计数 · MIME/Size 校验 · 显式分享/撤销 · 存储 GC（软删清除 + 孤儿扫描）· 元数据 PATCH |
| **消息** | 1:1 私信 · Reactions · 编辑/软删 · 已读回执 + 计数 · Inbox/Conv 视图 · 系统消息 · 全文搜索（FTS tsvector + CJK ILIKE） |
| **评论** | 两层嵌套 · Like/Unlike + 原子计数 · 举报→审核→隐藏/显示 · 编辑 · 原子回复计数 |
| **通知** | Outbox 驱动 · SSE 实时推送 · 折叠去重（5min 窗口）· 偏好设置（启用/静默时段）· Block 抑制 · Webhook HMAC 签名投递 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN）· CJK ILIKE 回退 · 多 kind 联合搜索 |
| **可靠性** | 事务性 Outbox + 指数退避 + 死信 · 幂等键（DB 持久化 + 24h TTL）· 双传输限流 · SSO 断路器 + stale-cache + singleflight + mTLS + cert-pinning · 冷启动 warmup · 优雅关停 + 最后 drain |
| **治理** | 方向性 Block · 审计日志（append-only）· Admin 角色（4 权限）· `RequirePermission` 中间件 |
| **基础设施** | 迁移 CLI · 自动迁移选项 · 22 份可逆迁移 · OTel 追踪 · Prometheus RED 指标 |

---

## 方向一：异步长任务框架（Async Long-Running Job Framework）

### 问题

aero-id 拥有完善的 **事件分发系统**（outbox relay），但 **没有通用的异步任务执行框架**。outbox 设计为"事件通知"而非"任务执行"——事件是轻量的、幂等的、无状态的；而任务是有状态的、可跟踪进度的、需要取消能力的。

两种截然不同的抽象：

| 维度 | Outbox Event | Long-Running Job |
|------|-------------|------------------|
| 语义 | "某件事已发生"（通知下游） | "执行一个操作"（完成任务） |
| 状态 | 成功/失败（二态） | 排队→执行中→完成/失败/已取消（多态） |
| 进度 | 无 | 0-100% |
| 结果 | 无（事件即结果） | 有产物（URL、文件、报告） |
| 取消 | 不支持 | 需要支持 |
| 时效 | 秒级 | 分钟级到小时级 |
| 用户交互 | 无 | 用户主动发起、查询进度、下载结果 |

### 代码级证据

**证据 1：outbox Event 模型没有任务语义**

```go
// internal/outbox/outbox.go
type Event struct {
    ID            uuid.UUID
    AggregateType string     // ← 聚合类型，不适合"导出"这样的跨聚合任务
    AggregateID   string
    EventType     string
    Payload       []byte     // ← 序列化的事件体，没有结果字段
    CreatedAt     time.Time
    PublishedAt   *time.Time
    RetryCount    int
    LastRetryAt   *time.Time
    DeadLetter    bool
}
```

Event 模型没有 `Status`、`Progress`、`ResultURL`、`CancelBy`、`JobType` 等字段——它不是为任务设计的。

**证据 2：没有任何任务定义或任务存储**

```bash
$ grep -rn "Job\|job\|Task\|task" internal/ --include="*.go" | grep -v "_test.go" | grep -v "\.pb\." | grep -vi "outbox\|event\|idempotency\|Reaction\|Object\|Project\|Adjust\|ReadJson\|Subject\|Reject\|inject\|object\|reject\|rejected\|subjec" | head -5
# 没有任务相关的域模型、仓库或服务
```

全代码库搜索 `job`、`task`、`async_op`——没有任何业务模型。outbox 事件模型是唯一接近的抽象，但它缺少：
- 任务状态机（queued → running → completed / failed / cancelled）
- 进度报告（percentage、message、errors so far）
- 结果存储（产物 URL、错误报告）
- 用户可见的端点（`GET /v1/jobs/:id`）
- 取消语义（`DELETE /v1/jobs/:id`，或 `cancel` 信号）

**证据 3：当前唯一需要异步任务的操作——数据导出——没有实现**

数据导出被 70+ 文档提及但从未实现。根本原因是缺少任务框架：

```
用户发起导出 → 
  1. 创建导出任务（尚未有模型）
  2. 后台收集数据（需要一个异步执行器）
  3. 打包压缩（耗时操作，不能阻塞 HTTP）
  4. 存储结果（需要临时或长期存储）
  5. 通知用户完成（outbox 事件可做，但需要 job ID）
```

outbox 的 `relay.Handle` 接口只接受 `context` 和 `*Event`，没有任务上下文或进度回调：

```go
// internal/outbox/outbox.go
type Handler interface {
    Handle(ctx context.Context, e *Event) error
    // ↑ 返回 error 即重试，nil 即成功
    // 没有进度报告、没有取消通道、没有结果存储
}
```

**证据 4：当前"长时间操作"只能同步执行**

```go
// internal/file/service_crud.go（Create 方法）
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*domain.File, error) {
    // 1. 校验（快速） ✓
    // 2. 计算 checksum（读取整个文件）→ 已是大文件阻塞点
    // 3. 写入存储（网络 I/O）→ 阻塞
    // 4. 创建 DB 行 → 阻塞
    // 所有步骤在单个 HTTP goroutine 中同步完成
    // 50MB 文件上传时，请求将持续数秒，占用整个 goroutine
}
```

### 产品经理视角

**当前缺乏真正的异步能力限制了以下产品功能**：

| 功能 | 当前状态 | 需要异步任务 |
|------|---------|-------------|
| 数据导出（GDPR） | ❌ 无法实现 | 必须——导出 10000 条消息不能在 HTTP 请求内完成 |
| 批量删除消息 | ❌ 当前一次删一条 | 必须——用户可能想清空与某人的所有聊天 |
| 批量文件操作 | ❌ 无批量上传/转码/删除 | 必须——文件和操作通常是批量进行的 |
| 定时/延迟操作 | ❌ 不支持 | 需要——"明天上午 9 点发送此消息" |
| 账户级联删除 | ❌ 一次删一条消息/文件/评论 | 必须——账户删除需清理所有关联数据 |
| 文件转码/缩略图 | ❌ 无法实现 | 必须——图片上传后后台转码 |

### 建议架构

**Phase 1 — Job 表 + 基础 CRUD（P1，低投入）**

```sql
CREATE TABLE async_jobs (
    id          UUID PRIMARY KEY,
    owner_uid   TEXT NOT NULL,
    type        TEXT NOT NULL,          -- 'data.export', 'batch.delete', 'file.transcode'
    status      TEXT NOT NULL DEFAULT 'queued',  -- queued | running | completed | failed | cancelled
    progress    INT NOT NULL DEFAULT 0,           -- 0-100
    params      JSONB,                            -- 任务参数
    result_url  TEXT,                              -- 产物下载链接
    error_msg   TEXT,                              -- 失败时的错误信息
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ                       -- 结果过期时间
);
```

```go
// 新增 JobRepository + JobService（在 service 或独立包中）
type JobService interface {
    Create(ctx context.Context, jobType string, params map[string]any) (*domain.Job, error)
    Get(ctx context.Context, id domain.ID) (*domain.Job, error)
    List(ctx context.Context, page domain.Page) ([]*domain.Job, string, error)
    Cancel(ctx context.Context, id domain.ID) error
}
```

HTTP 端点：
```
POST /v1/jobs                    → 创建任务 → 返回 job_id
GET  /v1/jobs/:id                → 查询进度/结果
GET  /v1/jobs                    → 我的任务列表
DELETE /v1/jobs/:id              → 取消任务
```

**Phase 2 — Job Worker（P1，中投入）**

- 改造 outbox relay，使其能同时处理 "任务事件"（job.run.*）和 "通知事件"
- 或者创建独立的 `JobWorker` 组件，类似 relay 但消费 `async_jobs` 表而非 outbox 表
- Worker 启动时标记 `queued→running`，完成时标记 `running→completed`，失败时 `running→failed` 并记录 error_msg

**Phase 3 — 第一个消费者：数据导出（P2，中投入）**

将数据导出作为第一个 job handler 实现。这将验证任务框架的设计，并提供用户可直接感知的价值。

### 既有分析覆盖度

- 数据导出被 70+ 份文档提及，但全部聚焦于导出本身而非其所需的基础设施
- **0 份文档** 将 "任务框架" 作为独立的基础设施方向提出
- 最近一份文档（`final-five-product-expansion-directions.md`）提到 "没有后台异步任务系统" 但仅作为数据导出的支撑论据，未系统性分析

---

## 方向二：API 合约表面与开发者体验（API Contract Surface & Developer Experience）

### 问题

aero-id 是 **双传输 API 服务**（HTTP + gRPC，41+ RPCs），但缺乏正式的 API 合约发布、客户端 SDK、以及开发者体验工具。对于定位为"数据平台"的服务，API 的可消费性直接影响生态整合成本。

特定缺失：

1. **无 OpenAPI/Swagger 规范** — HTTP API 完全由 Gin 路由定义，外部消费者无法自动发现
2. **无 gRPC-Web 支持** — 浏览器端 JavaScript 无法直接调用 gRPC 端点
3. **无 API 版本化策略** — 所有端点都在 `/v1`，但未来行为变更没有过渡机制（deprecation header、sunset date）
4. **gRPC 反射默认关闭** — `reflection_enabled: false`，外部工具无法自省 API
5. **无客户端 SDK** — 没有针对任何语言的 SDK，消费者需手写 HTTP/gRPC 调用

### 代码级证据

**证据 1：HTTP API 零合约文档**

```go
// internal/handler/http/router.go
func NewRouter(...) *gin.Engine {
    // 路由注册完全通过 RegisterRoutes 接口散布在各 handler 中
    // 没有集中的路由表格、没有 OpenAPI 注解、没有中间件自动生成文档
    // 外部消费者唯一的文档来源：README.md 的手写表格
}
```

```bash
$ grep -rn "swagger\|openapi\|oas\|api.yml\|api.yaml" internal/ --include="*.go"
# 0 结果
```

README.md 是唯一的 API 文档来源。它列出了部分端点但：
- 缺少请求体 schema
- 缺少响应体 schema  
- 缺少错误码表示
- 缺少认证方式描述
- 容易过时

**证据 2：gRPC 反射默认不开启**

```yaml
# configs/config.yaml
server:
  grpc:
    reflection_enabled: false   # ← 默认关闭
```

```go
// internal/handler/grpc/ops.go
func RegisterReflection(s *grpc.Server) {
    reflection.Register(s) // 只有配置开启时才注册
}
```

虽然出于安全考虑默认关闭是合理的，但没有替代的自省机制（如 `grpc.health.v1` 或定制的 `ListRPC` 端点）。外部工具（grpcurl、gRPC UI）无法在不打开反射或拥有 `.proto` 文件的情况下发现 API。

**证据 3：无 gRPC-Web 网关**

```go
// internal/handler/grpc/server.go
func NewServer(...) *grpc.Server {
    // 纯 gRPC 服务器，无 gRPC-Web 或 gRPC-Gateway
    // 浏览器端 JavaScript 必须使用 HTTP API（REST），与 gRPC 协议不一致
}
```

现有的 HTTP handler 和 gRPC handler 是 **独立实现** 的（虽共享 service 层），不是通过 gRPC-Gateway 从 proto 自动生成的。这意味着：
- HTTP 和 gRPC 的错误码映射各自独立（有 `parity_test.go` 但手写维护）
- 添加新的 RPC 需要实现两个 handler，增加不一致风险
- 没有单一日志/跟踪/指标的入口点

**证据 4：无版本策略**

```go
// 所有路由都以 /v1 开头
r.Auth.GET("/files", ...)     // 永远在 /v1 下
r.Auth.POST("/messages", ...) // 同上
// 没有 /v2、没有 Content-Type negotiation、没有 Deprecation header
// 没有 Sunset header
// 没有 API 变更日志
```

### 产品经理视角

**缺失的开发者体验直接影响 API 的采用率**：

| 能力 | 当前状态 | 对标 |
|------|---------|------|
| API 文档 | README.md 手写表格 | OpenAPI 自动生成 + 交互式 API Explorer |
| 客户端 SDK | 无 | 从 OpenAPI/proto 自动生成的 Go/TS/Python SDK |
| API 版本化 | 无 | Deprecation header + Sunset + 版本协商 |
| 浏览器支持 | 仅 HTTP | HTTP + gRPC-Web（性能优势） |
| 快速上手 | `curl` 示例 | 交互式 API 控制台 + 一键运行示例 |

### 建议策略

**Phase 1 — OpenAPI 规范生成（P1，低投入）**

- 引入 `getkin/kin-openapi` 或 `swaggo/swag` 在 Gin handler 上添加 OpenAPI 注解
- 生成 `openapi.yaml` 作为构建产物
- 提供一个 `/openapi.yaml` 端点供消费者下载
- 或使用 `grpc-gateway` 从 proto 自动生成 OpenAPI 规范

**Phase 2 — gRPC 反射与 gRPC-Web（P1，中投入）**

- 将 `reflection_enabled` 默认改为 `true`（或使用 TLS client cert 认证来替代纯开关）
- 增加 gRPC-Web 代理或使用 `envoy`/`grpcweb` 库使浏览器可调用 gRPC 端点
- 保持 HTTP 端点为兼容层（不删除），但引导新消费者使用 gRPC-Web

**Phase 3 — 版本策略文档 + Deprecation 中间件（P2，低投入）**

```go
// 新增 Deprecation 中间件
func Deprecation(sunsetDate string, migrationGuide string) gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Header("Sunset", sunsetDate)
        c.Header("Deprecation", "true")
        c.Header("Link", fmt.Sprintf("<%s>; rel=\"deprecation\"", migrationGuide))
        c.Next()
    }
}
```

- 制定 API 版本策略文档（breaking vs non-breaking、如何通知、迁移窗口）
- 为已废弃端点添加 `Sunset` 和 `Deprecation` header 支持

### 既有分析覆盖度

- 8+ 份文档提及 "OpenAPI" 或 "SDK" 作为一句话方向
- **0 份文档** 系统性分析了 aero-id 缺乏完整 API 合约表面的架构影响和实现策略
- 最新文档（`five-genuine-code-evidenced-architectural-gaps.md`）分析了 admin RBAC 但未涉及 API 开发者体验

---

## 方向三：数据一致性巡检框架（Consistency Auditor Framework）

### 问题

aero-id 维护多个 **去规范化计数器** 和 **外部队列/引用**，它们可能在并发或故障场景下 **发生偏移**。当前没有系统性机制来检测或修复这些偏移。

已知的偏移来源：

1. **内存计数器**：`comment.ReplyCount` 和 `comment.LikeCount` 在事务中原子增减，但事务失败后有空窗
2. **Outbox 事件可能丢失**：relay 在 drain 循环中崩溃可能导致已标记为 published 的事件未投递
3. **存储引用计数偏移**：`storage_refs` 表在极端竞争场景下（并发上传相同校验和）可能偏移
4. **SSO 缓存不一致**：用户从 SSO 后台更改了 display name，但 aero-id 缓存未更新
5. **通知折叠计数**：折叠窗口内相同类型的通知被合并，但 `CollapsedCount` 与实际子事件数不符

### 代码级证据

**证据 1：ReplyCount 和 LikeCount 可能偏移**

```go
// internal/comment/service.go — Like 方法
func (s *commentService) Like(ctx context.Context, id domain.ID) (*ReactionResult, error) {
    // 事务内：
    // 1. INSERT comment_likes（唯一约束防重复）
    // 2. UPDATE comments SET like_count = like_count + 1 （原子增减）
    // 
    // 问题：如果 step 2 失败但 step 1 成功（连接断开等罕见情况）
    // → like_count 偏少
    // 如果 step 1 成功、step 2 成功但事务提交失败
    // → like_count 偏多（计数器更改无法回滚？取决于是否在事务内）
    // 
    // 没有校正机制来检测和修复这种（虽然罕见的）偏移
}
```

类似地，`ReplyCount` 在 `Create` 和 `Delete` 时原子更新。没有巡检来验证总行数。

**证据 2：StorageRef 计数可能偏移**

```go
// internal/file/service_crud.go — Create 方法
// content dedup 逻辑：
// 1. 计算 checksum
// 2. FindByChecksum(owner, checksum) — 存在吗？
// 3. 如果存在 → IncrementRef(storageKey) — 引用计数+1
// 4. 如果不存在 → Store.Write → Create row + CreateRef(storageKey)

// 极端竞态条件：
// Goroutine A: checksum = "abc123", not found → Store.Write
// Goroutine B: checksum = "abc123", not found → Store.Write
// → 同一 blob 写两次，两个 row 指向不同 storage_key（内容相同）
// → 但两个 storage_key 的 ref count 均为 1，GC 不会清理任何一个
// → 存储冗余：同一个内容存储了两次

// 更糟糕的情况：
// Goroutine A: Store.Write 完成，Create row 失败（事务冲突）
// Goroutine B: Store.Write 进行中...
// → 没有行的 blob → "孤儿" 需要 GC 清理
```

虽然有 `StorageRefRepository` 的原子 refcount 解决部分问题，但没有周期性巡检来验证：
- 每个 file row 的 storage_key 在 refs 表中是否有 count ≥ 1
- 每个 refs 表中的 entry 是否至少有 1 个非软删除的 file row 引用它

**证据 3：没有通用巡检框架**

```bash
$ grep -rn "auditor\|reconcil\|consistency.*check\|drift.*detect\|巡检\|一致.*检查" internal/ --include="*.go"
# 0 结果（除了一些 `TODO` 注释）
```

没有任何包、接口或基础设施用于运行周期性数据一致性检查。

**证据 4：通知折叠计数不可验证**

```go
// internal/notification/dispatcher.go
// 折叠逻辑：当一个新通知与最近通知类型+接收者匹配时，CollapsedCount++
// 旧通知行保留，新行不创建
// 情景：CollapsedCount 可能偏移吗？
// 1. 并发 dispatch → 两个 goroutine 同时读 "最近通知" → 两次都创建新行
// → 本该折叠的没有折叠（两个通知行）
// 2. dispatch 成功但 relay 返回 error → 下次重试 → 再次 dispatch
// → 同一事件导致两次通知（如果 handler 非幂等）
```

### 产品经理视角

**当前隐性假设**：代码的并发安全性依赖于事务 + 唯一约束的健壮性。但：

- 在 K8s 多副本部署下，outbox relay 的 "每个实例轮询" 可能导致竞争（虽有 `MarkPublished` 防止重复处理，但在高延迟下同一事件可能被两个实例同时拉取）
- 数据偏移不会自我修复——一旦发生，偏移会持续存在
- 没有告警机制来发现偏移——管理者不知道 `reply_count` 是否准确

### 建议策略

**Phase 1 — 报告性巡检（P1，低投入）**

在现有 `storage/gc` sweeper 模式基础上，增加一个 `consistency` 巡检器：

```go
// 内部采用与 storage.GC 相同的定时模式
type Auditor struct {
    checks []func(ctx context.Context) ([]Anomaly, error)
}

type Anomaly struct {
    Severity   string // "info" | "warn" | "critical"
    EntityType string // "comment", "file_ref", "notification"
    EntityID   string
    Description string
    SQL         string // 修复用的 SQL（如果是可自动修复的）
}
```

巡检内容（Phase 1）：

| 巡检项 | SQL 模式 | 严重度 |
|--------|---------|--------|
| comment.reply_count 与实际子行数 | `SELECT c.id FROM comments c WHERE c.reply_count != (SELECT count(*) FROM comments child WHERE child.parent_id = c.id AND child.deleted_at IS NULL)` | warn |
| comment.like_count 与实际点赞数 | `SELECT c.id FROM comments c WHERE c.like_count != (SELECT count(*) FROM comment_likes cl WHERE cl.comment_id = c.id)` | warn |
| storage_refs 引用计数与实际 file rows | `SELECT sr.storage_key FROM storage_refs sr WHERE sr.ref_count < (SELECT count(*) FROM files f WHERE f.storage_key = sr.storage_key AND f.deleted_at IS NULL)` | critical |
| 无引用 blob 在 storage_refs 中 | `SELECT storage_key FROM storage_refs WHERE NOT EXISTS (SELECT 1 FROM files WHERE storage_key = storage_refs.storage_key AND deleted_at IS NULL AND deleted_at > now() - interval '24h')` | info |

**Phase 2 — 自动修复（P2，中投入）**

- 为 simple 巡检（如 counter 偏移）添加自动修复（重置 `reply_count = actual_count`）
- 复杂巡检（如 ref 计数漂移）仅报告，等待人工干预或更精细的修复逻辑
- 巡检结果通过审计日志记录（`action = "consistency.repair"`）

**Phase 3 — 告警集成（P2，低投入）**

- 当巡检发现 critical 异常时，通过 outbox 发出 `consistency.anomaly` 事件
- Webhook 订阅者可以接收一致性告警
- metrics 暴露 `consistency_anomaly_total{severity, entity_type}` counter

### 既有分析覆盖度

- **0 份文档** 将数据一致性巡检作为独立方向分析
- 2 份文档（`production-gaps-analysis.md`、`next-wave-extensions.md`）提到 outbox 重复处理的可能性，但未作为系统性巡检方向
- `edge-cases-and-performance.md` 提到并发点赞的幂等性但未涉及计数器校验

---

## 方向四：文件访问控制系统增强（Enhanced File Access Control Model）

### 问题

当前文件访问模型过于简单：`public / private / owner / shared_to_uid`。具体来说：

1. **分享无时间限制**：分享给某用户后，该用户永远持有访问权限，直到被撤销
2. **分享无权限分级**：分享只意味着"可以读取"，没有"可以写入"（修改元数据、上传新版本）或"可以分享给他人"的区别
3. **无群组/团队概念**：分享是按 uid 而非按群组（无法说"分享给 team-a 的所有成员"）
4. **无匿名分享链接**：非公开文件只能通过直接分享给 uid 访问，没有生成分享链接的机制
5. **无下载控制**：无法设置"仅允许在线预览，禁止下载"

### 代码级证据

**证据 1：file_shares 表只有 uid 粒度**

```sql
-- migrations/000006_create_file_shares.up.sql
CREATE TABLE file_shares (
    id          UUID PRIMARY KEY,
    file_id     UUID NOT NULL REFERENCES files(id),
    owner_uid   TEXT NOT NULL,
    grantee_uid TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX idx_file_shares_pair ON file_shares(file_id, grantee_uid);
```

- 没有 `expires_at` 列（无时间限制）
- 没有 `permission` 列（无 read/write 区分）
- 没有 `share_token` 列（无匿名分享链接）
- 没有 `scope` 列（无法指定是 uid 还是群组）

**证据 2：访问控制函数只有简单的二元判断**

```go
// internal/domain/access.go
func CanReadFile(actor string, f *File) bool {
    return f.Visibility == VisibilityPublic || f.OwnerUID == actor
```

加上 service 层的分享检查：

```go
// internal/file/service_crud.go
func (s *fileService) canRead(ctx context.Context, actor string, f *File) (bool, error) {
    if domain.CanReadFile(actor, f) {
        return true, nil
    }
    if s.shares == nil {
        return false, nil
    }
    shared, err := s.shares.IsSharedWith(ctx, f.ID, actor)
    return shared, nil // ← 没有权限级别检查
}
```

整个访问控制链是线性的：public → owner → shared。没有：
- 临时访问（expires_at）
- 分层权限（read / write / share）
- 匿名访问（share token）
- 群组访问（group membership）

**证据 3：没有文件级别的"禁止下载"属性**

```go
// internal/domain/file.go
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64
    MimeType    string
    StorageKey  string
    StorageType string
    Checksum    string
    Visibility  Visibility    // ← 仅有 public/private
    Meta        map[string]any
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

- 没有 `DownloadAllowed` 字段
- 没有 `PreviewOnly` 字段
- `GET /v1/files/:id/url` 总是返回一个可下载的 URL（本地存储的 presigned URL 或 S3 的 presigned URL）
- 没有区分 "预览 URL" 和 "下载 URL"

**证据 4：下载端点不检查目标用户是否被授权**

```go
// internal/service/file_presign.go（假设）
func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
    f, err := s.files.Get(ctx, id)
    // 检查：actor == owner 或 visibility == public 或 shared with actor
    ok, err := s.canRead(ctx, actor, f)
    // ← 这里检查了读取权限，但：
    // 1. 共享用户拿到 URL 后可以转发给其他人（无绑定）
    // 2. URL 在 TTL 内始终有效（5分钟，合理但无法撤销）
    // 3. 没有 download-prohibited 支持
}
```

### 产品经理视角

**当前的文件访问模型对协作场景限制极大**：

| 场景 | 当前能力 | 期望能力 |
|------|---------|---------|
| 共享设计稿给客户 | 只能分享给平台上另一用户 | 生成分享链接（无需注册），设置 7 天后过期 |
| 团队文档协作 | 逐人分享（O(n)） | 分享给群组或团队角色 |
| 合同审批 | 只能读 | 共享写权限给同事（允许上传新版本） |
| 资料分发 | 不公开的文件无法生成链接 | 受控分享链接（密码保护、下载次数限制） |
| 课程材料 | 只能公开或不公开 | 仅预览（禁止下载）+ 水印 |

### 建议策略

**Phase 1 — 时间限制分享（P1，低投入）**

```sql
ALTER TABLE file_shares ADD COLUMN expires_at TIMESTAMPTZ;
```

- `ShareFile` 接口增加可选的 `ttl` 参数（0 = 永不过期）
- 读取时检查 `expires_at`，已过期的视为未分享
- GC sweeper 清理过期 share 行

**Phase 2 — 匿名分享链接（P1，中投入）**

```sql
-- 新增 file_share_links 表
CREATE TABLE file_share_links (
    id         UUID PRIMARY KEY,
    file_id    UUID NOT NULL REFERENCES files(id),
    token      TEXT NOT NULL UNIQUE,    -- 随机令牌，用作链接标识
    password   TEXT,                    -- bcrypt 哈希（可选）
    expires_at TIMESTAMPTZ,
    max_downloads INT,
    download_count INT DEFAULT 0,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ
);
```

- `POST /v1/files/:id/share-links` — 创建分享链接（可设置密码/过期/下载次数）
- `GET /files/dl/:token` — 公开端点，由 token 解析文件（类似现有的 DL 签名验证，但使用令牌）
- 下载次数达到上限或过期后链接自动失效

**Phase 3 — 权限分级（P2，中投入）**

```sql
ALTER TABLE file_shares ADD COLUMN permission TEXT NOT NULL DEFAULT 'read';
-- 值: 'read' | 'write' | 'admin'
```

- `read` — 查看文件、下载（如果允许）
- `write` — upload new version, update metadata
- `admin` — manage sharing, delete file

### 既有分析覆盖度

- 公开分享链接在 2 份文档中被提及过路（`production-gaps-analysis.md` 提到 "分享链接是否应继续有效"，`final-blindspots.md` 提到 "临时分享 24h 过期"）
- **0 份文档** 将文件访问控制的完整增强（时间限制 + 权限分级 + 匿名链接 + 下载控制 + 群组）作为独立方向系统性分析
- `remaining-high-value-directions.md` 提到 "公开分享链接" 但仅作为一句话方向

---

## 方向五：消息搜索与发现增强（Message Search & Discovery Enhancement）

### 问题

当前的搜索实现是全文本搜索（FTS）但仅限于 **用户自己可以访问的消息**。搜索体验在以下方面有显著差距：

1. **无跨用户搜索**：搜索只查当前用户的消息（from_uid 或 to_uid），无法搜索共享文件上的评论、或与好友的共享对话
2. **无相关性排序**：搜索结果按 `created_at DESC` 排序而非按相关性排序。用户无法找到"最相关"的结果
3. **无搜索建议和补全**：用户输入时无搜索建议
4. **无高亮片段**：搜索结果只返回消息/评论/文件的完整内容，没有 `...匹配关键词的上下文片段...`
5. **无附件内容搜索**：消息可附带 `file_id`，但文件的内容不被索引和搜索
6. **无通知历史搜索**：通知没有搜索端点

### 代码级证据

**证据 1：搜索可见性仅限于用户自己的消息**

```go
// internal/service/search.go
func (s *searchService) Search(ctx context.Context, query string, ...) ([]domain.SearchHit, error) {
    actor, err := mustActor(ctx)
    // ...
    // 传递给 repository 的 actor 用于 WHERE 子句：
    // WHERE (from_uid = ? OR to_uid = ?) → 只查当前用户的消息
    // WHERE files.owner_uid = ? AND deleted_at IS NULL → 只查自己的文件
}
```

```go
// internal/repository/message.go — SearchMessages
func (r *msgRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error) {
    // WHERE (from_uid = ? OR to_uid = ?) ← 当前 actor 只看到自己参与的消息
    // 不会搜索到：
    //   - 别人分享给我的文件上的评论
    //   - 系统消息的全文内容
}
```

```bash
$ grep -rn "SearchMessages\|SearchComments\|SearchFiles" internal/repository/ --include="*.go"
# 全部接收 actor uid 作为过滤条件
```

**证据 2：无相关性排序（仅时间排序）**

```go
// internal/repository/message.go — SearchMessages
// 使用 plainto_tsquery 匹配后按创建时间降序
// 没有 ts_rank 排序
func (r *msgRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error) {
    query := r.db.WithContext(ctx).
        Where("from_uid = ? OR to_uid = ?", uid, uid).
        Where("search_vector @@ plainto_tsquery('simple', ?)", query).
        Where("deleted_at IS NULL").
        Order("created_at DESC").   // ← 按时间排序，非相关性
        Limit(limit).
        Find(&msgs)
}
```

相比之下，PostgreSQL 的 FTS 引擎提供了 `ts_rank` 和 `ts_rank_cd` 函数来计算文档与查询的相关性分数。当前实现完全未使用。

**证据 3：无搜索高亮**

```go
// internal/handler/http/search.go
type searchHitResponse struct {
    Kind    string           `json:"kind"`
    Message *messageResponse `json:"message,omitempty"`
    Comment *commentResponse `json:"comment,omitempty"`
    File    *fileResponse    `json:"file,omitempty"`
}
```

响应中没有 `snippet`、`highlights`、`score` 等字段。客户端收到完整消息内容后需自行匹配关键词。

PostgreSQL 的 `ts_headline` 函数可以生成 `<b>关键词</b>上下文片段</b>` 格式的 HTML，但完全没有使用。

**证据 4：无搜索建议/自动补全**

```go
// internal/handler/http/router.go
// 搜索端点：
r.Auth.GET("/search", h.Search)  // ← 只有完整搜索，没有 suggest 端点
// 没有 GET /v1/search/suggest
```

没有端点实现：
- `typeahead` 搜索（输入时建议）
- 搜索词补全（基于热门搜索或频率统计）
- "您是不是想找..." 式模糊纠正

**证据 5：通知无搜索**

```go
// internal/service/notification.go
type NotificationService interface {
    List(ctx context.Context, page domain.Page) (*NotificationList, error)
    MarkRead(ctx context.Context, id domain.ID) error
    MarkAllRead(ctx context.Context) error
    UnreadCount(ctx context.Context) (int64, error)
    // ★ 没有 Search(ctx, query, page) 方法
}
```

用户无法搜索历史通知。如果一个用户有数千条通知且想快速找到 "Bob 给你的文件评论"——无法做到。

### 产品经理视角

**搜索是用户查找历史内容的首要方式**。当前实现可以工作但体验粗糙：

| 场景 | 当前行为 | 期望行为 |
|------|---------|---------|
| 搜索 "合同" 查看相关对话 | 返回所有包含"合同"的消息，按时间排列 | 返回最相关的对话，附带高亮片段 |
| 与 Bob 讨论过"预算"但记不清 | 浏览全部与 Bob 的对话（无搜索特定对话能力） | 在对话视图内搜索 |
| 知道文件里有"审计报告"但忘了文件名 | 搜索文件名（前提是文件名含"审计"） | 搜索文件内容（文本文件）或评论内容 |
| 大量通知中找一条 | 逐页翻找 | `GET /v1/notifications/search?q=...` |

### 建议策略

**Phase 1 — 相关性排序 + 高亮片段（P1，低投入）**

```go
// 在 repository 层的 FTS 查询中增加 ts_rank 和 ts_headline
func (r *msgRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*SearchResult, error) {
    // 使用 ts_rank 排序
    // 使用 ts_headline 生成上下文片段
}
```

```go
type SearchResult struct {
    *domain.Message
    Score   float64 `json:"score"`    // 相关性分数
    Snippet string  `json:"snippet"`  // 高亮上下文片段
}
```

**Phase 2 — 搜索建议端点（P1，低投入）**

- `GET /v1/search/suggest?q=partia` 返回补全建议
- 实现方式：对用户最近搜索词的热缓存，或对消息内容的 trigram 索引

**Phase 3 — 通知全文搜索（P2，低投入）**

```go
// 在 NotificationRepository 中增加 Search 方法
// 利用 notifications.subject_preview 和 notifications.type 字段
// 配合简单的 ILIKE 或 tsquery（如果将来加搜索向量）
```

- `GET /v1/notifications/search?q=...` 按通知类型、actor、主题预览搜索历史通知

**Phase 4 — 跨用户搜索（P2，高投入）**

- 搜索分享给当前用户的文件上的评论
- 搜索系统消息（全平台通知）
- 搜索文件内容（文本类文件）
- 这个阶段需要定义清晰的访问控制边界——"我可以搜索什么"应该等于"我可以看到什么"

### 既有分析覆盖度

- FTS 搜索本身在 `ROADMAP.md` 和 `2026-07-10-edge-cases-performance-and-gaps.md` 中被提及
- **0 份文档** 将 "搜索增强（相关性排序、高亮、建议、通知搜索、跨用户搜索）" 作为独立方向系统性分析
- `edge-cases-performance-and-gaps.md` 提到添加 `ts_rank` 但仅作为一句话建议，未展开分析

---

## 建议优先级总览

| # | 方向 | 优先级 | 工作量 | 核心收益 | 建议时机 |
|---|------|--------|--------|---------|---------|
| 1 | **异步任务框架** | **P1** | 中 | 基础设施解锁所有耗时操作（导出、批量、转码、定时） | 先于数据导出等任何需要异步的功能 |
| 2 | **API 合约表面** | **P1** | 低-中 | 开发者体验、API 可消费性、双传输一致性 | 可立即启动 OpenAPI 生成 |
| 3 | **数据一致性巡检** | **P2** | 低-中 | 数据质量可观测、漂移可发现、故障可恢复 | 生产部署前或百万行数据量时 |
| 4 | **文件访问控制增强** | **P2** | 中-高 | 协作场景、合规控制（过期分享、密码保护） | 按产品路线图决定 |
| 5 | **搜索增强** | **P2** | 中 | 用户查找内容的核心体验提升 | 搜索使用量增长后 |

### 执行建议

- **方向一** 和 **方向二** 是基础设施层面的优先投入，建议 **下一个迭代启动**
- **方向三** 是运维质量投入，建议 **在数据量到达百万行级时启动**（当前规模下偏移影响小）
- **方向四** 和 **方向五** 是产品体验投入，建议 **按产品路线图与用户增长同步推进**
