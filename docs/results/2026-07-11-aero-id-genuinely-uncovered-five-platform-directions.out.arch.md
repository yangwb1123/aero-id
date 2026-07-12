基于对代码库的全面审查（包括领域模型、存储库、服务层、中间件、outbox、hub/SSE 基础设施、GC 扫描器、审计、迁移等），以下是架构分析。

---

# aero-id 架构深度分析

## 1. 架构评估

### 1.1 优势：分层方案执行得当

当前架构（**横向分层**：`handler → service → repository + storage + sso`）是其级别服务的一个极佳选择。它忠实地执行了原始设计文档的 D2 决策，并带来了可衡量的好处：

- **双传输协议统一**：`internal/service/service.go` 中定义了良好结构的接口（`FileService`、`MessageService`、`CommentService`、`BlockService`、`NotificationService`、`AdminService`）。HTTP（Gin）和 gRPC 处理程序都调用这些接口，确保行为完美一致。这已在 `handler/grpc/parity_test.go` 中得到验证。
- **真实领域驱动**：`domain/` 包不仅仅是 DTO——它封装了不变性（`CanReadFile`、`CanReadMessage`、`CanReadComment`、`CommentStatus`、`ReportStatus`、`TargetType`、`Visibility`、`MessageKind.IsSystem()`）。测试验证规则，如两层评论嵌套和 `mustActor` 守卫。
- **Outbox + Dispatcher 已投入生产**：`internal/outbox/` 不是一个占位符。它是一个完整的工作事务发件箱——事件（反）序列化、Relay 带指数退避和死信、`MarkPublished` 生命周期——与 `notification.Dispatcher` 集成。SSE Hub (`internal/hub/`) 提供实时推送。这**已经是**一个坚固的异步基础设施核心。
- **GC 扫描器作为参考架构**：正如分析指出的，`sweeper.go` 的 `Run()`/`Start()` 模式（单次传递/间隔循环 + 返回停止函数）正是后续 worker 所需的模式。
- **审计已经存在于基础层面**：`domain.AuditLogger` 接口和 `audit.Repository`（PostgreSQL 中的 `audit_events` 表）已就位。`file.share.grant`、`user.block`、`admin.role.grant` 等已由服务层记录。这与方向三（一致性审计）中建议的部分基础设施重叠。
- **中间件链覆盖面广**：认证、速率限制、幂等性、CORS、请求 ID、访问日志、恢复、追踪、指标、超时——每个都已实现并测试。幂等性中间件 (`middleware/idempotency.go`) + `idempotency.Store` 值得特别提及，因为它解决了重复提交问题，而许多服务只有在事后才添加此功能。

### 1.2 局限性：已识别架构债务

当前架构达到了其最初设计目标，但存在三个结构性债务：

| 债务 | 位置 | 影响 |
|--------|----------|--------|
| **同步上传，无后台任务基础设施** | `file/service_crud.go:Create` | SHA-256 哈希 + `store.Put` 阻塞 HTTP goroutine。大文件、转码或数据导出需要异步 worker 基础设施，而这些尚未存在。Outbox 用于*副作用*（通知），而不是用于*用户驱动的任务*（导出、转码）。 |
| **服务层文件大小超出限制** | `service/comment.go` (472 行), `service/message.go` (394 行), `service/notification.go` (392 行) | AGENTS.md 中声明的 500 行限额*尚未*被突破，但 `service/comment.go` 已经接近。由于 `cyclomatic complexity ≤ 10` 和 `function length ≤ 50`，接近限额意味着下一次添加可能会触发违规。在出现问题之前需要**主动拆分**。 |
| **API 合约需要文档，但自描述性不足** | 路由 + DTO 分散在各处 | 没有 OpenAPI 规范、没有版本协商、没有废弃标头基础设施。集成者必须阅读代码。gRPC 反射被禁用（`config.yaml` 中的 `reflection_enabled: false`）。 |

### 1.3 关键设计决策验证

| 决策 (来自设计文档) | 状态 | 评估 |
|---|---|---|
| D1: SSO 拥有身份，aero-id 通过 uid 引用 | ✅ 完全执行 | `owner_uid` / `from_uid_/_to_uid` 无处不在；无本地账户表；SSO 令牌验证 + 缓存 |
| D2: 横向分层而非按模块分块 | ✅ 已验证 | 正确的依赖方向：域 → 存储库 → 服务 → 处理程序；包间无循环 |
| D3: 使用 GORM | ✅ 可接受 | 低体量 CRUD 是合理的权衡；SQL 复杂查询（全文搜索）正确处理 |
| D6: 点赞推迟到 V2 | ✅ 但已完成 | `comment.likes` 表存在，`Like`/`Unlike` 在存储库/服务层已实现 |
| D8: 不内置追踪/指标 | ⚠️ 已撤销 | `internal/pkg/metrics` 存在；`middleware/tracing.go` 存在；`middleware/metrics.go` 存在。好：在需要时添加。 |

**要点**：架构基础牢固。大部分的 5 个方向不是架构范围变更，而是围绕现有基础的缺失功能层。

---

## 2. 扩展方向

以下是基于当前状态、产品路线图和已识别的代码库差距，按优先级排序的五个扩展方向。

### 方向 A：后台任务和异步 Worker 基础设施 (P0)

**为什么需要**：分析反馈正确地确定了这是阻塞性基础设施。如果数据导出是一个真正的需求，没有异步 worker，就不能导出大量数据集而不会使 HTTP 处理程序超时。使用场景包括：
- 数据导出（CSV/JSON 账户数据转储）
- 文件转码（图像缩略图、视频转换）
- 在后台延迟删除（与级联相关）
- 批量操作（已读全部、批量删除）

**核心挑战**：
1. **将用户任务与系统副作用分离**：当前的 outbox (`internal/outbox/`) 适用于*系统*副作用（通知）。用户任务需要不同的抽象——具有 `Status`（待处理/运行中/完成/失败）、`Progress`（0-100%）、`ResultURL`（下载结果）和 `JobType`（"export.csv"、"transcode.video"）的 `Job` 实体。
2. **任务编排和生命周期**：状态机（待处理 → 运行中 → 完成/失败/取消）+ 重试策略 + 过期。
3. **存储后端生成的任务**：来自 worker 的进度更新必须可观察——通过轮询 API 端点或 WebSocket/SSE 事件。

**预期的架构变更**：
- 新增包：`internal/job/`（`job.go`、`repository.go`、`service.go`、`handler_{http,grpc}.go`）
- 新增 DB 表：`jobs`（`id`、`owner_uid`、`type`、`status`、`progress`、`params`（JSONB）、`result_url`、`error_message`、`created_at`、`updated_at`）
- Worker 模式：`Run()` + `Start()` 风格，源自 `gc/sweeper.go`（已验证的模式）
- Handler 端点：`POST /v1/jobs`（创建任务）、`GET /v1/jobs/:id`（轮询状态）、`GET /v1/jobs`（列出我的任务）

**对现有系统的影响**：
- 最小化：outbox 不受影响。Job worker 独立于现有的 notification dispatcher 运行。
- 文件服务中的 `Create` 可以保持同步，或者可选择地将大型上传委托给后台任务——这是产品决策，不是架构决策。

### 方向 B：API 合约表面和开发者体验 (P1)

**为什么需要**：没有 OpenAPI 规范，任何集成都必须阅读源代码进行逆向工程。gRPC-Web 的缺失意味着浏览器客户端无法直接使用 gRPC。废弃标头和版本协商的缺失使 API 演进变得危险。

**核心挑战**：
1. **OpenAPI 文档生成**：Gin 没有内置 OpenAPI 支持。注解（如 `swaggo/swag`）是侵入性的，如果文档与代码不同步，则逐渐变得不可靠。
2. **gRPC-Web 或 gRPC-Gateway**：gRPC-Gateway 为 REST 客户端生成 HTTP→gRPC 代理。但 aero-id 已经有自己的 HTTP 处理程序。完全转变意味着要么弃用 Gin 处理程序，要么双重维护它们，直到弃用。
3. **API 版本协商和废弃**：目前所有路由都以 `/v1` 为前缀，但没有机制可以优雅地弃用旧的端点。需要 `Deprecation`、`Sunset` 和 `Link`（RFC 8594）标头的基础设施。

**预期的架构变更**：
- **选项 A（轻量级）**：使用 `swaggo/swag` 注解为当前路由生成 OpenAPI 3.0 规范。零运行时开销。文档与代码共存，但定期审计以保持同步。
- **选项 B（适度）**：添加一个 `api/openapi/` 目录，其中包含手动编写的 OpenAPI 规范（使用代码生成工具如 `ogen` 进行数据模型验证）。这避免了注解膨胀，并提供了一个签署过的 API 参考。
- **选项 C（演进性）**：逐步将 HTTP 处理程序迁移到 gRPC-Gateway，目标是最终淘汰 Gin。这是最高风险的，但减少了维护两个处理程序层的工作量。

**对现有系统的影响**：
- 选项 A：最小化——仅在现有处理程序上添加注解。
- 选项 B：适度——需要一个新的 OpenAPI 规范文件生成步骤。
- 选项 C：高——需要 gin 处理程序的逐步弃用和重写。不推荐立即执行。

**推荐**：选项 A 立即执行，选项 B 作为中期目标。

### 方向 C：搜索发现层增强（修正范围）(P1)

**为什么需要**：正如分析反馈所述，相关性排序**已经**通过 `ts_rank` 实现。缺少的地方是：
1. 排序分数**未暴露**给客户端（`SearchHit` 中无 `Score` 字段；`searchHitResponse` DTO 中无 `score`）
2. 高亮片段**未返回**（`ts_headline` 未使用）
3. CJK 回退（`SearchMessagesLike`、`SearchCommentsLike`、`SearchFilesLike`）按 `created_at DESC` 排序，不按相关性排序
4. 通知不支持搜索（`NotificationService` 中无 `Search` 方法）
5. 无搜索建议端点（`GET /v1/search/suggest` 不存在）

**核心挑战**：
1. **`ts_headline` 集成**：PostgreSQL 的 `ts_headline` 需要配置（`StartSel`、`StopSel`、`MaxWords`、`MinWords`、`ShortWord`）以生成可展示的片段，而不会破坏 HTML 或泄漏内部标记。
2. **搜索结果的评分归一化**：`ts_rank` 返回归一化浮点数，但跨实体（文件对比评论对比消息）的归一化不同。客户端需要可比较的分数。
3. **CJK 回退改良**：按 ILIKE 的 `Search*Like` 函数是文本搜索的正确回退（PostgreSQL 的 `simple` 字典不能很好地处理 CJK），但 `created_at DESC` 排序使它们对于搜索来说基本无用。

**预期的架构变更**：
- `domain/search.go`：`SearchHit` 添加 `Score float64` 和 `Snippet string` 字段
- `internal/repository/message.go`、`internal/repository/file.go`、`internal/repository/comment.go`：在选择列表中添加 `ts_headline(...) AS snippet`
- `internal/service/search.go`：将片段映射到 DTO
- `internal/handler/http/search.go` 和 `internal/handler/grpc/search.go`：在响应中包含评分和片段
- `GET /v1/search/suggest`：新端点，使用 `pg_trgm` 进行模糊前缀匹配

**对现有系统的影响**：最小化。搜索基础设施已经存在。这纯粹是丰富现有响应以包含更多数据。没有模式更改。没有新索引。

### 方向 D：文件访问控制增强（产品驱动）(P2)

**为什么需要**：目前的文件共享是一个简单的两态门——"可以访问"或"不可以访问"。`file_shares` 表使用复合主键 `(file_id, grantee_uid)`，这意味着不能有针对同一文件但不同权限的多个共享条目。没有过期时间，没有权限级别（预览、下载、编辑），没有共享令牌。

**核心挑战**：
1. **从两态到多态权限**：需要新的数据模型——`file_shares` 需要 `permission` 列（"read"、"preview"、"download"、"admin"）和 `expires_at`。`ACLCheck` 函数需要进化，从二分法变为等级检查。
2. **向后兼容性**：现有共享条目（无权限列）必须默认为 `read`（当前隐式授予的）。
3. **共享令牌/链接**：如果有人想与非 SSO 用户共享文件，需要 `share_token` 和匿名访问路径。

**预期的架构变更**：
- 迁移：向 `file_shares` 添加 `permission SMALLINT`、`expires_at TIMESTAMPTZ`、`share_token TEXT`
- `domain/access.go`：将 `CanReadFile` 逐渐替换为 `CheckFilePermission(actor, file, required Permission) bool`，使用按位比较
- `domain/file.go`：可能添加 `DownloadAllowed` 和 `PreviewOnly` 字段（用于元数据）
- `internal/service/file_share.go`：添加 `ShareWithPermission`、`CreateShareLink`、`RevokeShareLink`

**对现有系统的影响**：
- 这可能是一个重要的模式更改——从 `file_shares` 中的每个收件人单行（无权限）到多行（不同权限）。
- 组合主键使此更改变得繁琐（不能简单地添加新列并完成）。

### 方向 E：数据一致性审计框架 (P2)

**为什么需要**：正如分析所指出的，即使每个操作都是事务性的，也没有框架来检测由于 bug、手动数据库操作或旧代码路径导致的长期漂移。`Auditor`/`reconciler` 接口存在但未用作主动验证工具。

**核心挑战**：
1. **计数器漂移检测**：`comments.like_count` 应与实际 `comment_likes` 行数匹配。`notifications.collapsed_count` 应与折叠组中的实际通知匹配。没有内置漂移检测。
2. **存储引用完整性**：`storage_refs` 表 (迁移 000021) 存在但没有验证，存储引用计数是否与存储桶中的实际 blob 匹配。
3. **生产安全执行**：检查器必须只读（SELECT 仅模式），永远不执行修复，并且可以通过可配置的节流运行，以避免生产负载。

**预期的架构变更**：
- 新增包：`internal/reconciler/`（`drift.go`，定义 `DriftReport` 结构体；`checker.go`，实现可插拔的检查器）
- 每个检查器实现一个接口：

```go
type Checker interface {
    Name() string
    Check(ctx context.Context) (*DriftReport, error)
}
```

- 检查器示例：`LikeCountChecker`、`NotificationCollapseChecker`、`StorageRefChecker`、`DeletedOrphanChecker`
- 后端：写入 `reconciliation_reports` 表或通过现有审计基础设施记录

**对现有系统的影响**：
- 最小化。检查器在数据库中是只读的。它们可以独立运行，甚至在单独的进程中（CLI 工具或 cronjob）。
- 注意：检查器输出可能大量——报告必须有节流和页面限制。

---

## 3. 接口设计建议

### 3.1 当前接口契约需要稳定化

当前的服务接口定义在 `internal/service/service.go` 中是好的——它们很窄（在职责范围内），使用良好的 DTO（`CreateFileInput`、`MessageList`、`CommentList`），并返回具体类型（`*domain.File`）而不是接口。这是正确的。

**但需要注意的是**：`FileService` 接口有 **14 个方法**，这是过多的。这是接口实现文件（`service/comment.go` 有 472 行，`service/message.go` 有 394 行）过大的一个因素。考虑拆分：

```
FileService (14 methods) →
  FileCrudService (Create, Get, List, Delete, UpdateMetadata)
  FileShareService (ShareFile, UnshareFile, ListSharedWithMe)
  FileDownloadService (DownloadURL)
  FileUploadService (PresignUpload, CompleteUpload)
```

这并非对 YAGNI 的违规——这是对已经超过 400 行的代码的主动重构以防它达到 500。

### 3.2 介绍：可插拔中间件上下文

目前，中间件（`Auth`、`RateLimit`、`Idempotency`）都以 Gin 风格编写——通过 `c.Set()` 和 `c.Request.WithContext()` 将值插入上下文。这对于 HTTP 来说没问题，但 gRPC 已经有自己的拦截器链。**无需更改**，但要注意任何新的 cross-cutting 关注点（如废弃标头注入）都需要在两种传输方式中实现。

### 3.3 新抽象：JobWorker 接口

对于方向 A（后台任务），引入一个干净的接口，它模拟 GC 扫描器的模式：

```go
// JobWorker performs a single unit of background work.
type JobWorker interface {
    // Run executes one job identified by id, updating progress through the
    // reporter. Implementations MUST respect context cancellation.
    Run(ctx context.Context, id domain.ID, reporter ProgressReporter) error
}

// ProgressReporter allows a worker to push progress updates back to the
// jobs table atomically.
type ProgressReporter interface {
    Report(ctx context.Context, jobID domain.ID, progress int, message string) error
    Complete(ctx context.Context, jobID domain.ID, resultURL string) error
    Fail(ctx context.Context, jobID domain.ID, err error) error
}
```

### 3.4 向后兼容性策略

对于方向 B（API 合约）：
- **新端点**最初都驻留在 `/v1` 下，但带有可选的 `Accept-Version` 标头支持。
- **废弃**：添加一个 `DeprecationMiddleware`，它检查可配置的即将废弃的路由映射并注入 `Sunset` / `Deprecation` / `Link` 标头。
- **OpenAPI 规范**作为 `api/openapi/v1.yaml` 提交。如果使用代码生成，则自动验证响应与规范的一致性。

---

## 4. 技术选型

### 4.1 异步任务框架（方向 A）

| 选项 | 优点 | 缺点 | 对 aero-id 的评估 |
|---|---|---|---|
| **基于数据库的作业队列（新建 `jobs` 表 + worker goroutine）** | 零新依赖；事务一致（与发件箱相同）；worker 与 GC 扫描器模式匹配 | 无内置持久性重试拓扑（没有不同的消费者组）；并发受数据库连接限制 | ✅ 推荐：与项目的"PostgreSQL 存储一切"理念一致。worker 可以在与 HTTP 服务器相同的进程中运行，也可以作为单独的二进制文件运行 |
| **Redis + `redis/rq` 或 `asynq`** | 专门的作业队列；TTL/重试/调度；好用的 API | 新的运行时依赖（Redis）；不是事务性的（作业入队可能与数据库写入不同步） | ❌ 对于单服务范围来说过度设计。如果添加了 Redis 用于缓存（目前没有），则重新考虑 |
| **RabbitMQ / NATS** | 流式处理；持久性；dead-letter 交换 | 运营复杂度；额外的移动部件 | ❌ aero-id 不需要消息代理。单个 worker goroutine 就足够了 |

**决策**：jobs 表 + 基于 goroutine 的 worker，与 GC 扫描器和发件箱中继的模式匹配。如果以后需要不同的消费者组，可以提取为单独的二进制文件。

### 4.2 OpenAPI 文档（方向 B）

| 选项 | 优点 | 缺点 |
|---|---|---|
| **`swaggo/swag`** | 自动从注释生成；与 Gin 配合良好 | 注释可能会落后于代码；必须运行生成步骤 |
| **手动编写 OpenAPI YAML + 验证** | 签署过的规范；API 产品团队可以使用；与代码生成器无关 | 手动维护；可以与实际行为不同步 |
| **在集成测试中从 Gin 路由生成 OpenAPI** | 始终与路由匹配；零注解 | 仅生成签名，不生成请求/响应模式；构建基础设施 |

**推荐**：从 `swaggo/swag` 开始（选项 A），因为它对现有代码库的侵入性最小。如果文档一致性成为一个问题，则迁移到手动维护的规范（选项 B）。

### 4.3 配置管理——当前状态 OK

`internal/config/config.go` 很好地使用了 Viper。无需更改。保持 `AERO_ID_*` 环境变量模式。

### 4.4 审计存储——当前状态 OK

写入 `audit_events` 表。它已经是 append-only，具有结构化元数据（JSONB）。不需要专门的审计存储后端。

### 4.5 指标和追踪——注意趋势

当前指标实现在 `internal/pkg/metrics/` 中是针对出站请求（SSO）的 Prometheus 计数器。如果需要，方向 A（后台任务）可能需要作业级指标（排队时间、处理持续时间、失败率）。不要预建；在方向 A 达到概念验证阶段时再添加。

---

## 5. 实施路线图

### 优先级总结

| 优先级 | 方向 | 原因 |
|---|---|---|
| **P0** | 方向 A：后台任务 | 如果数据导出是真正的需求，这是阻塞性基础设施 |
| **P1** | 方向 B：API 合约 | 低投入，高开发者体验回报 |
| **P1** | 方向 C：搜索增强 | 评分/片段已经实现；需要暴露；低风险 |
| **P2** | 方向 D：文件 ACL | 产品路线图驱动；重大模式更改 |
| **P2** | 方向 E：一致性审计 | 百万行级别的需求较低；监控很关键时才需要 |

### 阶段 1：立即（接下来的 2-4 周）

**目标**：零架构债务 + 低垂的果实

| 项目 | 工作包 | 风险 | 缓解 |
|---|---|---|---|
| **服务层拆分**（`service/comment.go` 472 行，`service/message.go` 394 行） | 提取为 `service/crud_*.go` 或按域拆分（`comment_service.go`、`message_service.go`） | 低——纯重构；现有测试保持不变 | 为拆分按域建立的文件运行 `make check-file-sizes` |
| **搜索：暴露评分和片段**（方向 C） | 向 `SearchHit` 添加 `Score` + `Snippet`；向 SQL 查询添加 `ts_headline`；更新 HTTP/gRPC DTO | 低——纯添加；无模式更改 | 验证并添加接受评分/片段作为查询参数的集成测试 |
| **OpenAPI swaggo 引导**（方向 B） | 添加 `swaggo/swag` 依赖；为路由添加 `// @Summary` / `// @Param` 注释；运行 `swag init` | 低——不影响运行时行为 | 提交生成的 `docs/` 目录，检查 git diff 以确认没有意外更改 |

### 阶段 2：中期（第 4-12 周）

**目标**：异步任务框架 + API 合同

| 项目 | 工作包 | 风险 | 缓解 |
|---|---|---|---|
| **Jobs 表 + 创建/列表端点** | 迁移；`internal/job/` 包；`POST /v1/jobs` / `GET /v1/jobs/:id` / `GET /v1/jobs` | 低——新端点，不影响现有端点 | 幂等性中间件确保双重提交安全 |
| **Worker 框架（`Run()`/`Start()` 模式）** | 基于 `gc/sweeper.go` 的 `JobWorker` 接口；基于 goroutine 的 worker，带有可配置的并发限制 | 低——已验证的模式 | 并发限制可配置；指标跟踪队列积压 |
| **数据导出示例任务** | 构建一个导出用户文件/消息/评论的 `ExportWorker`；写入可下载的 CSV | 中等——大型数据集需要流式处理 | 对导出数据集大小使用 PostgreSQL 游标 + 流式 HTTP 下载 |
| **搜索建议端点**（方向 C） | `GET /v1/search/suggest?q=` 使用 `pg_trgm` | 低——纯添加；如果 `pg_trgm` 扩展不存在则优雅降级 | 添加 `CREATE EXTENSION IF NOT EXISTS pg_trgm` 迁移 |
| **gRPC 反射启用 + gRPC-Web 代理**（方向 B） | 在非生产配置中启用 `reflection_enabled`；添加 `envoy` 或 `grpcweb` 代理 | 低——配置更改；无代码更改 | 用于 gRPC-Web 的独立 docker-compose 配置文件 |

### 阶段 3：长期（第 12+ 周）

**目标**：文件 ACL 演进 + 一致性审计

| 项目 | 工作包 | 风险 | 缓解 |
|---|---|---|---|
| **文件 ACL 增强**（方向 D） | 将 `file_shares` 从复合主键迁移到带权限/过期时间的新模式；引入 `CheckFilePermission`；向后兼容性层 | 高——模式更改；现有共享可能需要升级脚本 | 分两步迁移：(1) 添加新列 + 降级脚本；(2) 填充默认值，移除旧约束 |
| **共享令牌/链接**（方向 D） | 向 `file_shares` 添加 `share_token`；新的 `GET /files/dl/:token`（无认证）端点 | 中——如果链接泄露，安全性影响 | 共享令牌必须是带过期时间的加密随机数 |
| **一致性检查器框架**（方向 E） | `internal/reconciler/` 包；`LikeCountChecker`；`NotificationCollapseChecker`；CLI 命令用于按需运行 | 低——只读检查器；不会损坏数据 | 检查器结果写入结构化日志 + 可选的 `reconciliation_reports` 表 |
| **通知搜索**（方向 C） | 向 `NotificationService` 添加 `Search(ctx, query, page)` | 低——遵循其他搜索的现有模式 | 受益于阶段 2 建议端点中相同的基础设施 |

### 风险热点

| 风险 | 受影响方向 | 缓解 |
|---|---|---|
| **Jobs 表与现有 Outbox 表竞争** | A | 设计时保持清晰区分：outbox 用于*系统*事件；jobs 用于*用户*任务。它们共享相同的 `events_outbox` 基础架构，但有不同的生命周期语义 |
| **文件 ACL 模式更改影响现有共享** | D | 在阶段 3 之前不要删除旧的 `file_shares` 模式。添加新列，在 API 层处理两个模式，验证没有回退，然后弃用旧的 |
| **搜索回退性能** | C | `SearchMessagesLike`（ILIKE）在达到数十万行时不能扩展。在阶段 3 中计划迁移到 PGroonga 或 `pg_bigm` 用于 CJK |
| **swagger 注释与代码不同步** | B | 将 `swag init` 构建步骤添加到 `Makefile`；在 CI 中添加一个检查，如果生成的规范与提交的不匹配则失败 |

### 建议的下一步立即操作

1. **拆分 `service/comment.go`**（超过 470 行）。将其拆分为 `service/comment_service.go`（主要流程）、`service/comment_reactions.go`（Like/Unlike）和 `service/comment_reports.go`（Report/Resolve）。这解决了最接近的 AGENTS.md 限额违规。

2. **说服利益相关者方向 C（搜索）是 P1 而非 P0**，因为核心基础设施（`ts_rank`）已经存在。工作仅仅是暴露现有数据，而不是构建新功能。

3. **开始在 `docs/` 中记录当前 API 路由**，即使没有 swaggo。`internal/handler/http/router.go` 中当前的注释式路由（例程的）`// GET /v1/files` 已经是一个良好开始——将它们升级为正式的 OpenAPI 路径对象。
