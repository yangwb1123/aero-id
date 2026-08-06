# aero-id — 架构演进与运维成熟度：五个未覆盖的高阶方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 + 产品经理
> **方法**: 从 `internal/` 全量代码（~45 个包）和 22 份迁移文件出发，逐一比对 `docs/requirements/` 下 25+ 份和 `docs/REVIEW_*` 下 8 份既有文档的主题索引（累计覆盖 ~65 个扩展方向），定位**在所有前述分析中未被系统性覆盖**的 5 个架构演进与运维成熟度方向。
> **核心原则**: 每个方向必须满足 ① 有代码级证据（具体文件 + 行号 + 逻辑），② 非重复已有分析，③ 从架构师/PM 双视角论证为什么**现在**就该关注。
> **无代码变更**

---

## 前置：当前代码库状态

aero-id 是一个高度成熟的双传输（HTTP + gRPC）Go 服务。完整能力矩阵已在 ROADMAP.md 和 CHANGELOG.md 中详述，这里只列出与本文分析直接相关的模块状态：

| 维度 | 当前状态 | 本文关注的缺口 |
|------|---------|---------------|
| **API 面** | 38 个 gRPC RPC + 30+ HTTP 端点，100% parity | 无 OpenAPI 规范，无客户端 SDK，无 API 版本策略，无限流反馈头 |
| **数据迁移** | 22 份迁移，`cmd/migrate` CLI，`database.auto_migrate` 启动选项 | 无零停机迁移策略，无大表 backfill 模式，无迁移回滚验证自动化 |
| **测试** | 3400+ 个测试，27 包单元 + 21 包集成（testcontainers），go vet 绿灯 | 无基准/性能回归测试，无混沌测试，无 fuzz 测试，无竞态测试 CI |
| **可观测性** | RED 指标，OTel 追踪，每依赖健康检查，优雅关停 | 无 SLO 框架，无 error budget 追踪，无容量规划模型，无成本归因 |
| **架构治理** | AGENTS.md + ROADMAP.md + DECISIONS.md，文件大小/复杂度限制 | 无 API surface 治理，无依赖健康仪表盘，无技术债预算机制，无架构适应度函数 |

---

## 方向一：API 消费者契约与开发者体验（API Consumer Contract & Developer Experience）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/handler/http/router.go:26-30` | `RouteRegistrar` 接口允许各 handler 注册路由，但没有任何描述性元数据（路径、方法、参数、响应 schema）——路由对消费者完全不透明 |
| `internal/handler/http/message.go` — 全部 | HTTP handler 接受 JSON 请求体，但错误响应格式（`{"error":{"code":"...","message":"..."}}`）没有任何公开文档——消费者必须阅读 Go 代码理解 |
| `internal/middleware/ratelimit.go` — `AbortWithStatusJSON(429, ...)` | 限流拒绝时只返回 429 + JSON 体，**无 `Retry-After`、无 `X-RateLimit-*` 头**。通过的请求也不附加任何限流信息。客户端无法智能调整请求速率 |
| `internal/handler/grpc/validate.go` — 全部 | gRPC 请求校验逻辑分散在 handler 中，校验规则无统一 schema、无自动生成文档 |
| `internal/pkg/errors/errors.go` — `AppError` 结构 | 错误代码（`"auth.unauthorized"`、`"file.not_found"`）无中央注册表、无版本化策略。将来重命名或废弃某个错误码时，无法通知消费者 |
| `internal/service/mocks/mocks.go` — 全部 | mock 测试表明 service 接口设计良好，但 **没有从 proto 或 HTTP handler 自动生成客户端 SDK 的工具链** |
| `api/proto/aero_id/v1/aero_id.proto` | gRPC proto 文件是唯一的结构化契约，但仅有 gRPC 服务定义——HTTP API 的端点、参数、认证方式、状态码映射、错误码全无机器可读描述 |
| `internal/config/config.yaml` | 无 `api.versioning`、`api.deprecation.notice_period`、`api.supported_versions` 配置项 |

### 为什么需要

**从产品视角：** aero-id 已经是一个平台级服务（文件、消息、评论、通知、搜索、阻断），但它的 API 消费者体验停留在"看 Go 源代码使用"。没有 OpenAPI 规范意味着：

- **没有文档站点**（Swagger UI / Redoc）——第三方开发者必须阅读源代码或 README 表格
- **没有自动 SDK 生成**——每个消费者手写 HTTP 调用
- **没有限流反馈**——消费者被 429 打脸时才意识到撞了限流阈值
- **没有迁移指南**——如果明天重命名 `GET /v1/messages/inbox` 为 `GET /v1/conversations`，现有消费者全部断裂

这三个缺口共同构成了一堵墙：**任何想要集成 aero-id 的团队必须投入大量逆向工程成本**。在 SaaS/平台业务中，API 开发者体验（DX）是增长杠杆。

**从架构视角：** API 是系统唯一的外部契约。当前完全没有机器可读的契约管理：

1. **Schema 即文档**（OpenAPI / protobuf）是最佳实践——让 IDE 自动补全、让 CI 检查不兼容变更、让客户端代码自动生成
2. **限流头是弹性架构的组成部分**——标准化的 `Retry-After` + `X-RateLimit-*` 头让客户端实现自适应退避，减少 429 风暴
3. **API 版本策略是长期演进的先决条件**——没有它，每次不兼容变更都需要同步发布所有消费者，阻碍快速迭代

### 建议方案

#### 1. OpenAPI 规范生成管线

目前 proto 文件已包含完整的 gRPC 服务定义。建议：

```makefile
# 新增 Makefile target
.PHONY: openapi
openapi:
	# 从 proto 生成 OpenAPI v2（protoc-gen-openapiv2）
	protoc --openapiv2_out=docs/ \
		--openapiv2_opt=allow_merge=true,merge_file_name=aero-id \
		api/proto/aero_id/v1/*.proto
	# 可选：v2 → v3 转换（如果需要 3.1 功能）
	# 可选：从 OpenAPI 生成 TypeScript/Go 客户端 → clients/ 目录

.PHONY: openapi-lint
openapi-lint:
	# CI 步骤：检查 OpenAPI 变更（spectral lint + breaking change detection）
	npx @stoplight/spectral lint docs/openapi.yaml
	npx @optools/breaking-change-detector docs/openapi.yaml
```

OpenAPI 规范的来源有两个选择：

| 策略 | 优点 | 缺点 |
|------|------|------|
| A. 从 proto 自动生成（protoc-gen-openapiv2） | 与 gRPC 契约同步，零额外维护 | 不覆盖 HTTP-only 端点（/files/dl、/healthz、/metrics）；OpenAPI v2 不如 v3 表达力强 |
| B. 手写 OpenAPI 3.1 + CI lint | 完整覆盖所有端点，表达力最强 | 手工维护容易与代码不同步 |

**推荐路径：** 先用策略 A 快速生成基线，然后在 `docs/openapi-patches/` 下手动补充 HTTP-only 端点。CI 中的 breaking change 检查基于 proto 变更驱动。

#### 2. 限流头标准化

```go
// internal/middleware/ratelimit.go — 新增头
const (
	HeaderRateLimitLimit     = "X-RateLimit-Limit"      // 窗口内限制数
	HeaderRateLimitRemaining = "X-RateLimit-Remaining"   // 当前窗口剩余
	HeaderRateLimitReset     = "X-RateLimit-Reset"       // 窗口重置 Unix 时间戳
	HeaderRateLimitPolicy    = "X-RateLimit-Policy"      // 策略描述，如 "20r/s"
	HeaderRetryAfter         = "Retry-After"             // 429 时必设（秒）
)

// 在 Allow() 方法中返回剩余容量和重置时间
type Result struct {
	Allowed   bool
	Remaining int
	ResetAt   time.Time
	Limit     int
}
```

#### 3. API 版本头与弃用策略

```go
// 新增中间件：VersionHeader
func VersionHeader() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-API-Version", cfg.App.Version)         // 当前服务版本
		c.Header("X-API-Deprecated", "")                    // 弃用警告（如有）
		c.Header("X-API-Sunset", "")                        // 弃用 endpoint 的退役日期 RFC 1123
	}
}
```

#### 4. 错误码注册表

```go
// internal/pkg/errors/registry.go — 新增
type ErrorCode struct {
	Code        string // 如 "file.not_found"
	HTTPStatus  int
	GRPCCode    codes.Code
	Description string // 开发者文档
	Since       string // 引入版本，如 "0.2.0"
	Deprecated  string // 弃用提示，如 "use file.not_exist instead"
}

var Registry = map[string]ErrorCode{
	"auth.unauthorized":     {Code: "auth.unauthorized", HTTPStatus: 401, GRPCCode: codes.Unauthenticated, ...},
	"file.not_found":        {Code: "file.not_found", HTTPStatus: 404, GRPCCode: codes.NotFound, ...},
	// ... 全部 ~30 个错误码
}
```

集成到 `WriteError` 中：每次返回错误时设置 `X-Error-Code` 头，方便消费者快速定位错误文档。

### 边界情况

| # | 场景 | 处理方式 |
|---|------|---------|
| EC1 | 消费者同时使用 v1 和 v2 端点 | API 版本独立路由：`/v1/files` 和 `/v2/files` 并存，无版本默认 v1 |
| EC2 | 旧版 SDK 继续发送已弃用的请求头 | `X-API-Deprecated: true` + `X-API-Sunset: <date>` 告知消费者，但不拒绝请求 |
| EC3 | 限流头泄漏内部配额策略 | 公开限流策略阈值是可接受的（攻击者可通过实验探测），收益 > 风险 |
| EC4 | OpenAPI 规范与代码不同步 | CI 加入 `openapi-lint` + `proto-diff` 检查：当 proto 变化时必须重新生成 openapi |
| EC5 | 错误码在运行时被移除 | 错误码注册表在启动时验证所有已注册错误码均在代码中定义（反射扫描），启动失败 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| **用户/消费者价值** | 🔥🔥🔥🔥🔥 — 团队集成 aero-id 的成本从"读 Go 代码"降为"读文档 + 用 SDK" |
| **架构杠杆** | 🔥🔥🔥🔥 — OpenAPI 是 API 治理的基础设施，限流头是弹性设计的一部分 |
| **实现成本** | 中（2-3 周） |
| **依赖前置** | 无 |

**建议**: **P1** — 这是平台化道路上的第一步。推荐在下一迭代启动，与公开分享链接（direction in remaining-high-value-directions.md）并行推进。

---

## 方向二：生产级数据迁移与 Schema 演化架构（Production Migration & Schema Evolution Architecture）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/pkg/migrate/migrate.go:1-200` | `applyTx` 将每个迁移的 SQL + 版本簿记封装在一个事务中。**所有 DDL 都在单个事务内执行**——PostgreSQL 不支持事务内的 `CREATE INDEX CONCURRENTLY`，所以迁移 000016（FTS 索引）的注释明确说明"non-concurrent, acceptable" |
| `internal/pkg/migrate/migrate.go:87-95` | `applyTx` 使用 `db.Transaction` 包裹整个迁移。如果迁移包含 `ALTER TABLE ... ADD COLUMN ... NOT NULL`，但表中有 100 万行旧数据，整个迁移会锁表并回填默认值，在生产中引起长时间锁等待 |
| `migrations/000016_fts_indexes.up.sql` | 注释承认无法使用 `CONCURRENTLY`，因为迁移在事务内。GIN 索引在大表上构建时会阻塞写入 |
| `migrations/` — 全部 22 个 up.sql | 所有迁移都是"所有或全不"的。没有一个迁移使用 **multi-step 策略**（如 ① 添加可空列 ② 后台 backfill ③ 设置 NOT NULL）。这在大型生产数据库上是不安全的 |
| `internal/app/app.go:79-94` | `AutoMigrate` 选项在启动时执行所有 pending 迁移。如果生产部署意外开启此选项（或 CI 中误触），可能在高负载时触发长时间的 DDL 锁 |
| `configs/config.yaml` | 无 `database.migration.lock_timeout`、`database.migration.statement_timeout` 等保护性配置 |
| `internal/handler/http/health.go` | `/readyz` 检查 DB 和 SSO 健康，但**迁移正在运行时 `/readyz` 和 `/healthz` 都显示 green**——负载均衡器可能将流量路由到正在执行写锁 DDL 的实例 |
| `internal/pkg/migrate/migrate.go:50-60` | 无 `dry-run` 模式、无 `validate` 子命令、无 `sql` lint 集成。生产前无法验证迁移是否安全 |

### 为什么需要

**从架构视角：** aero-id 已经是一个有用户数据的产品。22 份迁移见证了它的成长。但当数据量增长到 GB/TB 级别时，当前的 migration 模式存在两个致命问题：

1. **写锁 DDL 导致停机**。`ALTER TABLE ... ADD COLUMN ... DEFAULT ... NOT NULL` 锁全表。`CREATE INDEX`（非 CONCURRENTLY）在 Postgres 中锁表写入。回填/索引构建时间与数据量成正比——10GB 表可能需要 5-30 分钟。这是生产停机事件。

2. **迁移失败恢复困难**。如果迁移 000018 已执行 90%、在 `CREATE INDEX` 时超时，事务回滚——但部分索引可能已经写入磁盘（PostgreSQL 的 DDL 是事务性的，回滚会清理）。但如果是 `ALTER TABLE ... RENAME COLUMN` 后 crash → 表结构处于半应用状态 → 恢复复杂。

**从产品视角：** 迁移失败意味着服务不可用。aero-id 没有维护窗口的概念——迁移要么在启动时自动（`auto_migrate`）要么通过 CLI 手动。没有一个让**数据库迁移成为可管理的运维操作**。

### 建议方案

#### 1. 零停机迁移模式清单

定义一套**安全迁移模式**，所有新的迁移必须遵循其中一种：

| 安全模式 | 适用场景 | 步骤 |
|---------|---------|------|
| **Add Column Nullable** | 新增可选字段 | ① `ALTER TABLE ADD COLUMN`（无 DEFAULT，NULLABLE），0 锁 |
| **Add Column + Backfill** | 新增必填字段 | ① `ALTER TABLE ADD COLUMN`（NULLABLE），② 后台分批 UPDATE 回填，③ `ALTER TABLE ALTER COLUMN SET NOT NULL` |
| **Create Index Concurrently** | 新增索引 | ① `CREATE INDEX CONCURRENTLY`（在事务外执行，不阻塞写入），② 需单独迁移或 CLI 命令 |
| **Rename Column** | 列改名（安全版） | ① 添加新列，② 应用层双写（写入新旧两列），③ 后台回填旧数据，④ 切换读取到新列，⑤ 移除旧列 |
| **Multi-Phase** | 高风险变更 | 拆分为 2-3 个独立迁移发布，间隔 1+ 个部署周期 |

#### 2. 迁移执行上下文改进

```go
// internal/pkg/migrate/migrate.go — 新增执行选项
type RunOptions struct {
	LockTimeout      time.Duration // 如 "30s"，SET lock_timeout = '30s'
	StatementTimeout time.Duration // 如 "10m"，SET statement_timeout = '10m'
	DryRun           bool          // 仅打印 SQL，不执行
}

// 新增 migrate validate 子命令
func Validate(dir string) ([]ValidationWarning, error) {
	// 分析每个迁移的 SQL 语句
	// 检测安全模式：CREATE INDEX（非 CONCURRENTLY）→ warning
	// 检测 ALTER TABLE ADD COLUMN ... NOT NULL（无 DEFAULT）→ error
	// 检测 RENAME COLUMN → warning（需要 multi-phase）
}
```

#### 3. 迁移运行时的 `/readyz` 行为

```go
// internal/handler/http/health.go — 迁移中降级
type HealthHandler struct {
	migrating atomic.Bool // 迁移开始时 true，完成后 false
}

// Ready 检查在迁移进行中时，即使 DB 可连接也返回 503
func (h *HealthHandler) Ready(c *gin.Context) {
	if h.migrating.Load() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "migrating"})
		return
	}
	// ... 现有健康检查逻辑
}
```

#### 4. 迁移回滚测试自动化

```go
// migrations/migrations_test.go — 扩展测试
func TestMigrations_UpDownCycle(t *testing.T) {
	// 现有测试：apply all up, then all down
	// 新增：
	// 1. 创建测试表并插入样本数据（模拟生产数据量）
	// 2. 执行单个迁移 up
	// 3. 验证数据完整性（COUNT 匹配、列值正确）
	// 4. 执行 down
	// 5. 验证表结构恢复原始状态
}
```

### 边界情况

| # | 场景 | 处理方式 |
|---|------|---------|
| EC1 | 迁移在 `CREATE INDEX CONCURRENTLY` 时被 SIGTERM | CONCURRENTLY 操作在后台运行，不阻塞。重启后迁移状态已记录（bookkeeping 表），跳过此版本。但索引可能处于 INVALID 状态——需要 `REINDEX` |
| EC2 | 多副本同时执行 `auto_migrate` | 迁移使用 advisory lock `pg_try_advisory_lock(migration_version)` 串行化：第一个副本获得锁执行，其他副本等待锁或跳过 |
| EC3 | 迁移后回滚导致数据丢失 | 回滚必须严格按 .down.sql 执行。down.sql 应在 `DELETE FROM` 等破坏性操作前做 `SELECT COUNT(*) INTO backup_table` |
| EC4 | 大表上 `ADD COLUMN DEFAULT` 锁表 | 使用三步法：① 添加列（NULLABLE、无 DEFAULT），② 后台 UPDATE 分批设置默认值，③ `ALTER COLUMN SET DEFAULT` + `SET NOT NULL` |

### 优先级判断

| 维度 | 评分 |
|------|------|
| **用户/业务价值** | 🔥🔥🔥🔥 — 用户无感知，但迁移失败=停机。对企业客户 SLA 直接负责 |
| **架构杠杆** | 🔥🔥🔥🔥🔥 — 安全的迁移模式决定了系统能否持续演化 |
| **实现成本** | 中（1-2 周治理 + 工具 + 回滚验证） |
| **依赖前置** | 无，但需要从现在开始要求所有新迁移遵循安全模式 |

**建议**: **P1** — 在生产数据量达到 10GB 或 100 万行之前建立迁移规范。当前 22 份迁移已经很多，建议从下一个迁移（23 号）开始强制执行安全模式审查。

---

## 方向三：分布式测试与质量保障基础设施（Distributed Testing & Quality Assurance Infrastructure）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `Makefile` — 全部 | `test`、`test-integration`、`lint` 三个 target。**无 `test-race`、无 `bench`、无 `fuzz`、无 `chaos`** |
| `internal/service/*_test.go` — 全部 | Service 测试使用 mock repo，验证业务逻辑和错误路径。但 **无并发安全测试**（`go test -race` 无 CI 步骤），**无性能回归测试** |
| `internal/repository/*_test.go` — 全部 | Repository 测试使用 testcontainers（真实 PG），验证 SQL 语义。但 **无查询性能验证**（`EXPLAIN ANALYZE`），**无高并发写入测试** |
| `internal/handler/grpc/handlers_test.go` — 全部 | gRPC handler 测试使用 bufconn。覆盖了序列化和错误码。但 **无输入 fuzz 测试**、**无超时/取消传播测试** |
| `internal/handler/http/*_test.go` — 全部 | HTTP handler 测试使用 `httptest.NewRecorder`。覆盖了路由和响应码。但 **无恶意输入泛化测试**（超大 JSON、非 UTF-8、SQL 注入变体） |
| `internal/handler/grpc/comment_edge_test.go` | 边缘条件测试存在，但 **无自动生成的测试数据**（所有测试数据是手写固定 fixture） |
| `internal/hub/hub_test.go` | Hub 测试覆盖了订阅/发布/关闭。但 **无高并发订阅/发布竞争测试**（模拟 1000 并发连接） |
| `internal/sso/breaker_test.go` | 断路器测试使用 `clock.Mock`。但 **无集成断路器 + 实际 gRPC server 的端到端测试** |
| `.github/workflows/ci.yml` — 全部 | CI 运行 `test` + `lint` + `test-integration`。每个 job 约 5-8 分钟。**无并行化**、无跨作业缓存 |
| `internal/service/mocks/mocks.go` | Mock 生成良好。但 **无 mock 调用参数验证**（如验证 mock 被正确参数调用） |

### 为什么需要

**从架构视角：** 测试金字塔已经健全（3400+ 测试），但存在三个结构性缺口：

1. **无性能回归检测**。没有基准测试意味着无法知道 "代码变更 A 导致搜索查询慢了 3 倍"。在 QPS 增长时，性能退化是渐变而非突变的——没有基准线就无法早期发现。

2. **无并发安全 CI 门禁**。代码中使用 `sync.Mutex`、`sync.Map`、`channel`、`atomic`——但没有 `go test -race` 的 CI 失败门槛。竞态条件（race condition）是生产中最难排查的 Bug 之一。

3. **无混沌测试覆盖依赖故障**。SSO 断路器、serve-stale、outbox 重试——这些弹性模式在集成测试中覆盖了 happy path，但**没有一个测试验证 SSO 完全宕机 5 分钟时的系统行为**。没有混沌测试，这些弹性模式只是"设计存在"而非"经验证可以工作"。

**从产品视角：** 质量问题在被用户报告之前不应被团队认为是"已解决"。当前测试覆盖了"是否工作"（功能），但没有覆盖"是否够快"（性能）和"是否扛得住"（弹性）。

### 建议方案

#### 1. 基准测试套件

```go
// internal/repository/message_bench_test.go — 新增
func BenchmarkListInbox(b *testing.B) {
	// Arrange: testcontainers → 插入 10000 条消息（多个用户）
	// Warm up: 执行 3 次
	// Benchmark: b.ResetTimer(); for i < n { ListInbox(ctx, uid, page) }
	// Assert: P99 < 50ms
}
```

```makefile
# Makefile 新增 target
.PHONY: bench
bench:
	go test -bench=. -benchtime=3x -count=5 ./internal/... 2>&1 | tee bench.txt
	# 比较 bench.txt 与 bench.baseline.txt 的变化
	@echo "=== Performance Change Detection ==="
	@go run tools/benchdiff/main.go bench.baseline.txt bench.txt
```

#### 2. 竞态检测 CI 门禁

```makefile
# Makefile
.PHONY: test-race
test-race:
	go test -race -count=1 ./internal/... 2>&1  # 排除 testcontainers（需要 Docker）
	go test -race -tags=integration -count=1 -run "^TestSSO|^TestOutbox|^TestBlock" ./internal/...
```

#### 3. 混沌测试包

```go
// internal/chaos/chaos_test.go — 新增
// 使用 testcontainers + gRPC mock SSO 验证弹性模式

func TestSSOCircuitBreaker_OpensOnUnavailable(t *testing.T) {
	// Arrange: 启动真实 PG + real/handler + mocksso（可注入故障）
	// Act: 发送请求，同时使 mocksso 返回 Unavailable
	// Assert: 1-3 次失败后断路器打开，后续请求走 serve-stale
}

func TestSSOFullOutage_SystemDegraded(t *testing.T) {
	// Arrange: SSO 完全不可用 5 分钟
	// Act: 发送 50 个文件/消息/搜索请求
	// Assert: 已认证用户（缓存中有 token）可以正常读写
	//          新用户（无缓存 token）收到 401
	//          现有通知推送（SSE）正常
}
```

#### 4. Fuzz 测试（API 输入）

```go
// internal/handler/grpc/message_fuzz_test.go — 新增
func FuzzSendMessage(f *testing.F) {
	f.Add("valid-uid", "", -1)        // empty content
	f.Add("", "valid-content", 999)    // empty to_uid
	f.Add("valid-uid", strings.Repeat("A", 10001), 1) // content overflow
	f.Add("valid-uid", "\x00\x01\x02", 1) // binary injection
	f.Fuzz(func(t *testing.T, toUID, content string, kind int) {
		req := &aeropb.SendMessageRequest{
			ToUid: toUID, Content: content, Kind: int32(kind),
		}
		// 验证：不 panic，不返回 INTERNAL，返回合理错误
		_, err := client.SendMessage(ctx, req)
		if err != nil {
			st, ok := status.FromError(err)
			if !ok { t.Errorf("unexpected non-grpc error: %v", err) }
			if st.Code() == codes.Internal { t.Errorf("internal on fuzz input") }
		}
	})
}
```

### 边界情况

| # | 场景 | 处理方式 |
|---|------|---------|
| EC1 | CI 中跑 race 测试导致超时 | race 检测显著降低性能。建议：CI 中只跑核心包的 race（service、sso、hub、middleware），集成测试跑 subset |
| EC2 | 混沌测试依赖 SSO mock 不准确 | 使用 `internal/sso/ssopb` 的实际 gRPC 服务接口，用 `bufconn` 启动一个可以注入故障的 fake SSO server |
| EC3 | 基准测试在不同的 CI runner 上不稳定 | 使用相对比较（"本次比 baseline 慢 20% 以上标记失败"）而非绝对阈值 |
| EC4 | Fuzz 测试发现真正的安全漏洞 | 所有 fuzz 发现的 crash 自动归档到 `security/advisories/`，阻断 CI |

### 优先级判断

| 维度 | 评分 |
|------|------|
| **用户/业务价值** | 🔥🔥🔥 — 用户不直接看到测试，但质量下降的后果用户直接承受 |
| **架构杠杆** | 🔥🔥🔥🔥🔥 — 基准测试 + 混沌测试直接保护架构弹性假设 |
| **实现成本** | 低-中（基准 3 天，race CI 0.5 天，混沌 2 周，fuzz 1 周） |
| **依赖前置** | 无（testcontainers 已就位，SSO mock 已就位） |

**建议**: **P2** — `test-race` 可以立即加入（0.5 天）。基准测试建议在性能敏感区（搜索、收件箱分页、outbox drain）新增。混沌测试在第一个多副本部署前必须完成。

---

## 方向四：运行时可观测性与自主运维框架（Runtime Observability & Autonomous Operations Framework）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `internal/pkg/metrics/metrics.go` — `Registry` 结构 | `ObserveHTTP` 记录每个 route 的 RED 指标。但 **没有业务指标**——如 "messages_sent_total"、"files_uploaded_total"、"active_users"、"notification_delivery_rate" |
| `internal/pkg/metrics/metrics.go:70-90` | RED 指标只按 `method` + `path` + `status_class` 分桶。**没有按 `uid` 分桶**（隐私考虑）、**没有按 `error_code` 分桶** |
| `internal/app/app.go:199-205` | `warmup` 运行一次 DB ping + SSO probe。但 **无定期健康数据采集**——系统不知道依赖延迟的趋势 |
| `internal/outbox/relay.go` | 有 `pending_count`、`oldest_age`、`drain_duration` 三个 gauge。但 **无单事件延迟**（event 从创建到处理的时间）、**无事件类型分布** |
| `internal/sso/client.go` | 有 `validate_duration`、`breaker_state` 等指标。但 **无缓存命中率**（当前无法区分 cache hit vs stale grace vs upstream call） |
| `internal/handler/http/health.go` | `/readyz` 返回 binary ok/err。**无详细健康状态**（哪个依赖慢了多少毫秒） |
| `configs/config.yaml` | 无 `slo.*`、`alerting.*`、`threshold.*` 配置节 |
| `internal/service/comment.go` — `Create` | 每条评论创建的业务逻辑被追踪、被记录。但 **无"创建一条评论"的业务指标**——团队看不到"今天有多少评论"的趋势 |
| `internal/storage/gc/sweeper.go` | GC 有 `purged_total`、`orphan_swept_total` 指标。但 **无存储增长趋势**——"过去一周存储增长了 12%" |

### 为什么需要

**从运维视角：** aero-id 当前的可观测性聚焦于**技术健康**（请求延迟、错误率、数据库连接池状态）。
但运维一个生产服务需要的远不止 RED 指标：

- **业务健康**：消息量、文件上传量、通知送达率、活跃用户数——这些指标告诉你系统是否在"正常工作"
- **趋势分析**：存储增长、用户增长、请求量增长——这些指标告诉你多久需要扩容
- **容量规划**：当前 50 个 DB 连接在 100 DAU 下够用，当 10000 DAU 时呢？
- **成本归因**：哪个用户或功能消耗了最多的存储、带宽、DB 连接？

**从产品视角：** 没有业务指标，团队无法回答最基本的问题：

- "上周上线的新功能被用了多少？"
- "评论系统还是消息系统消耗了更多存储？"
- "我们的用户分布在哪？他们在什么时间段最活跃？"
- "通知的端到端送达延迟是多少？"

**从架构视角：** 当前可观测性缺少四个关键能力：

1. **SLO 框架**——"P99 延迟 < 200ms"是一个目标，但没有工具跟踪达标率
2. **error budget**——"过去 30 天内超过 SLO 的请求比例"是上线决策的量化依据
3. **容量模型**——"延迟与并发数的关系曲线"是预判瓶颈的输入
4. **成本归因**——"用户 A 的存储成本是 $0.03/月"是定价和限权的依据

### 建议方案

#### 1. 业务 RED 指标层

```go
// internal/pkg/metrics/business.go — 新增
type BusinessMetrics struct {
	MessagesSent       prometheus.Counter   // 标签: kind(from_uid, to_uid 不记录→隐私)
	FilesUploaded      prometheus.Counter   // 标签: mime_type, size_bucket
	CommentsCreated    prometheus.Counter   // 标签: target_type
	NotificationsSent  prometheus.Counter   // 标签: notification_type
	ActiveUsers        prometheus.Gauge     // 过去 1h 内有操作的用户数
	StorageUsed        prometheus.Gauge     // 总存储使用量（字节）
	NotificationLag    prometheus.Histogram // 事件创建→通知 DB 写入的延迟
}
```

在 `app.go` 中初始化，通过 service 构造函数注入。service 在关键业务操作后调用：

```go
// internal/service/message.go — Send()
func (s *messageService) Send(ctx context.Context, msg domain.Message) error {
	// ... 现有业务逻辑 ...
	
	// 业务指标
	if s.metrics != nil {
		s.metrics.MessagesSent.WithLabelValues(strconv.Itoa(int(msg.Kind))).Inc()
	}
	return nil
}
```

#### 2. SLO 框架

```go
// internal/pkg/slo/slo.go — 新增
type SLO struct {
	Name        string        // "search_p99_latency"
	Target      float64       // 0.99 (99%)
	Threshold   time.Duration // 200ms
	Window      time.Duration // 30d 滑动窗口
	BurnRate    float64       // 告警触发阈值，如 0.1（10% error budget 消耗）
}

// SLO 从 Prometheus 指标计算
type SLOResult struct {
	Compliance float64 // 0.0-1.0, 达标占比
	BudgetLeft float64 // 剩余 error budget 比例
	Breach     bool    // 是否违反 SLO
}

// 暴露为 Prometheus 指标
// aeroid_slo_compliance{name="search_p99_latency"} 0.987
// aeroid_slo_budget_remaining{name="search_p99_latency"} 0.43
```

建议首个 SLO 集：

| SLO | 目标 | 窗口 | 告警条件 |
|-----|------|------|---------|
| API 写入延迟 P99 < 500ms | 99% | 30d | 10 分钟 burn rate > 10 |
| 搜索 P99 延迟 < 200ms | 99% | 30d | 10 分钟 burn rate > 10 |
| 通知投递延迟 P99 < 5s | 99.5% | 7d | 5 分钟 burn rate > 5 |
| 文件上传成功率 > 99.9% | 99.9% | 30d | 10 分钟失败率 > 1% |

#### 3. 依赖健康趋势仪表盘

```go
// internal/handler/http/health.go — 扩展为详细健康端点
// GET /admin/health/detail
type DependencyHealth struct {
	Name     string  // "database", "sso", "storage.s3"
	Latency  float64 // 最后一次探测的延迟（ms）
	Status   string  // "healthy" | "degraded" | "down"
	Trend    string  // "stable" | "increasing" | "decreasing"
	P50      float64 // 过去 5 分钟 P50 延迟
	P99      float64 // 过去 5 分钟 P99 延迟
	Since    time.Time // 当前状态持续时长
}
```

#### 4. 存储成本归因

```go
// internal/storage/gc/cost.go — 新增
// 按 owner_uid 聚合存储使用量和成本估算
type StorageCostReport struct {
	ByUser  map[string]UserStorageCost // 每个用户的成本
	Total   StorageCost
	Period  string // "2026-07"
}

type UserStorageCost struct {
	UID        string
	FileCount  int64
	TotalBytes int64
	EstimatedCostUSD float64 // 基于 storage.gc.cost_per_gb 配置
}
```

容量规划信号：

```go
// 每周自动生成的报告
// - "过去 30 天存储增长 15%（+50GB），按当前趋势 90 天后将满"
// - "数据库连接池使用率 70%，从 50 个连接增长趋势看 6 个月后需要增加"
```

### 边界情况

| # | 场景 | 处理方式 |
|---|------|---------|
| EC1 | 业务指标随用户量线性增长（10 亿个 messages_sent_total）| Prometheus Counter 使用 float64（~10^16 精度），绰绰有余 |
| EC2 | SLO window 跨版本部署不一致 | SLO 窗口基于 Prometheus 服务器时间，与部署无关 |
| EC3 | 成本归因不精准（去重文件） | 去重文件的物理存储按比例分摊（`storage_refs.count`/总引用数）× 物理大小 |
| EC4 | 业务指标中泄露用户行为隐私 | 只聚合（counter/gauge/histogram），不记录 uid 标签。成本归因仅在 admin 端点返回 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| **用户/业务价值** | 🔥🔥🔥🔥🔥 — "今天服务健康吗？"是最基本的运维问题，当前回答是 "healthz 200"——远远不够 |
| **架构杠杆** | 🔥🔥🔥🔥🔥 — SLO 框架驱动所有后续决策（容量、上线、告警） |
| **实现成本** | 中（业务指标 3 天，SLO 框架 1 周，健康仪表盘 2 天，成本归因 2 天） |
| **依赖前置** | Prometheus 已就位（metrics 包存在），告警平台需自行配置 |

**建议**: **P1** — 业务指标层成本极低（3 天）但收益巨大（"昨天有多少消息被发送？"）。SLO 框架建议在第一个 P0 故障后启动——error budget 是最有说服力的运维投资证明。

---

## 方向五：架构演进治理与适应度函数（Architecture Evolution Governance & Fitness Functions）

### 代码级证据

| 位置 | 证据 |
|------|------|
| `AGENTS.md` — 工程约束 | 文件 ≤ 500 行、函数 ≤ 50 行、圈复杂度 ≤ 10——这些都是**静态分析约束**。没有**动态/行为约束**（如"搜索不能在 < 100ms 内返回"、"outbox 事件不能在 5 分钟内未发布"） |
| `HARNESS.md` — 全部 | 定义了 `check-file-sizes`、`lint-strict` 等 enforce 机制。但 **没有验证架构边界**（如"handler 不能直接调用 repository"——这个约束是约定而非强制） |
| `internal/app/app.go` — 全部 | 依赖注入通过构造函数显式传递，结构良好。但 **无运行时依赖图验证**——无法确认识别废弃的依赖关系 |
| `go.mod` — `redis/go-redis` | 依赖存在但疑似未使用（见 SCHEMA_QUERY_TEST 分析）。**无 dead dependency 检测 CI** |
| `docs/DECISIONS.md` | 记录了 10 个关键决策。但 **无决策日期、无替代方案分析、无后续验证**。"为什么选择 GORM"的决策理由在文档中，但没有人验证这个决策是否仍然成立 |
| `internal/service/` — Suite A vs `internal/{file,comment,message}/` — Suite B | 两套代码实现相同功能。**无一个文档解释为什么两套存在、哪一套是当前的、清理计划是什么** |
| `.github/workflows/ci.yml` — 全部 | CI 验证测试 + lint + 构建。但 **无架构约束验证**（如"无循环依赖"、"无 handler 直接调 repository"） |
| `internal/pkg/idgen/idgen.go` | UUIDv7 实现。但 **无 ID 冲突检测、无 ID 空间监控**（"过去 1 小时生成了多少个 ID？"） |

### 为什么需要

**从架构视角：** 架构治理不是一个一次性活动。它是一个持续过程——确保代码库在增长过程中不退化。

aero-id 目前有**被动治理**（Makefile 中的文件大小检查、Lint 检查）但缺乏**主动治理**：

1. **没有架构适应度函数**（Architecture Fitness Functions）——自动验证架构约束的测试。例如："验证 handler 层不导入 repository 包"、"验证所有 gRPC handler 在 HTTP 端有对应实现"
2. **没有技术债预算机制**——当团队决定"这次先不做正确的事，日后偿还"时，没有方法追踪这个债务
3. **没有架构决策的涟漪效应追踪**——一个决策（如"使用 GORM"）在半年后被质疑时，没有数据支持它是仍然正确还是已经成为瓶颈

**从产品视角：** 架构退化的最终承受者是用户。当代码库难以修改时，新功能开发周期变长、Bug 修复变慢、上线风险变高。aero-id 已经暴露了架构退化信号：

- Suite A 和 Suite B 并存——新开发者该改哪个？
- `redis/go-redis` 在 go.mod 中但未使用——依赖管理退化
- AGENTS.md 的约束在代码审查中靠人遵守，不是自动检测

### 建议方案

#### 1. 架构适应度函数（Fitness Functions）

```go
// internal/archtest/archtest_test.go — 新增
// 使用 Go 的 ast 包或 go/packages 遍历源码结构

func Test_Handler_Does_Not_Import_Repository(t *testing.T) {
	// 遍历 internal/handler 下的所有 Go 文件
	// 检查 import 路径：如果包含 "internal/repository/" → FAIL
}

func Test_Service_Does_Not_Import_Storage_Directly(t *testing.T) {
	// service 层只能通过 domain.Owner 或 storage 接口与 storage 交互
	// 不能直接 import "internal/storage/local"
}

func Test_All_GRPC_Methods_Have_HTTP_Parity(t *testing.T) {
	// 解析 proto 文件中的所有 RPC
	// 检查 HTTP handler 中是否有对应的路由
	// 缺失的输出 warning（非 FAIL）
}

func Test_No_Direct_DB_Access_Outside_Repository(t *testing.T) {
	// 检查除了 internal/repository/ 外的文件是否 import gorm
	// FAIL 如果 service 或 handler 直接操作 gorm.DB
}

func Test_All_External_Dependencies_Are_Pinned(t *testing.T) {
	// 检查 go.mod 中的每个 direct dependency 是否有版本 pin
	// 不包括 indirect 依赖
}
```

#### 2. 技术债寄存器

```go
// internal/techdebt/register.go — 新增
type TechDebtItem struct {
	ID          string    // "TECH-001"
	Title       string    // "Suite A and Suite B code duplication"
	Severity    string    // "high" | "medium" | "low"
	Effort      string    // "3 weeks"
	DetectedAt  time.Time
	DetectedBy  string    // "arch-review-2026-06"
	Owner       string    // "platform-team"
	Status      string    // "registered" | "triaged" | "in_progress" | "resolved" | "accepted"
	CostPerWeek int       // 估算每周维护成本（开发人时）
	DueDate     time.Time // 建议解决日期
}

// Makefile target: make techdebt-report
// 从 internal/techdebt/ 读取寄存器 + 从 git log 自动推导新债务
// 输出：
//   TECH-001: Suite A/B duplication (high) — 3w effort, est. 2h/week overhead
//   TECH-003: redis/go-redis unused dependency (low) — 30min effort
//   总计: 3 items, estimated overhead ~4h/week
```

#### 3. 架构决策记录（ADR）自动化

```go
// docs/adr/ — 新增目录
// 每个架构决策用 Markdown 文档记录
// 模板：
//
// # ADR-{N}: {Title}
//
// ## 日期
// {YYYY-MM-DD}
//
// ## 状态
// [proposed | accepted | deprecated | superseded by ADR-{M}]
//
// ## 背景
// 为什么需要做这个决策？
//
// ## 决策
// 我们选择了什么方案？
//
// ## 替代方案
// - 方案 A（优点/缺点）
// - 方案 B（优点/缺点）
//
// ## 影响
// 正面的：...
// 负面的：...
//
// ## 验证
// 如何验证这个决策仍然正确？ [指标/测试/周期性审查]

// Makefile target: make adr-new
// make adr-list
// make adr-verify  # 验证每个 ADR 的决策是否仍成立
```

建议将现有的 `DECISIONS.md` 中的 10 个决策迁移到单独的 ADR 文件中。

#### 4. 依赖健康仪表盘

```go
// internal/dephealth/dephealth.go — 新增
// 分析 go.mod 中的每个依赖：
// - 版本新鲜度（与最新 release 比较）
// - 安全公告（扫描已知 CVE）
// - 使用范围（被多少文件 import）
// - 健康度评分（版本新鲜度 × 使用广度 × CVE 状态）

// Makefile target: make dep-audit
// 输出每个依赖的健康评分 + 建议
```

### 边界情况

| # | 场景 | 处理方式 |
|---|------|---------|
| EC1 | 架构适应度函数阻碍了紧急修复 | 适应度函数可以有 `skip` 注释（`// archtest:skip`），但有日志和 ticket 追踪 |
| EC2 | 技术债寄存器变成"僵尸记录" | 季度审查：每个 open 的 debt item 重新评估，确认是否需要更新估值或关闭 |
| EC3 | Suite A/B 双实现共存 | 明确一个为"活动"（active）、一个为"弃用"（deprecated），在 code review 中禁止对被弃用套件的新开发 |
| EC4 | 依赖健康评分导致不必要的升级 | 仅报告"有 CVE"的依赖为严重，版本新鲜度为参考信息 |

### 优先级判断

| 维度 | 评分 |
|------|------|
| **用户/业务价值** | 🔥🔥🔥 — 用户不直接感知，但架构退化最终以"慢、不稳定、难改"的形式传递给用户 |
| **架构杠杆** | 🔥🔥🔥🔥🔥 — 适应度函数让架构约束从"文档"变为"可执行的测试" |
| **实现成本** | 低（适应度函数 2 天，技术债寄存器 0.5 天，ADR 迁移 1 天，依赖健康 1 天） |
| **依赖前置** | 无 |

**建议**: **P2** — 架构适应度函数可以按需增量添加（从最重要的约束开始：handler → repository 边界）。ADR 建议从下一个重大决策开始使用。技术债寄存器在团队感到"债务积累"时创建。

---

## 综合优先级与执行路线图

```
时间线：
Q3 2026 (now)                         Q4 2026                           Q1 2027
│                                     │                                 │
├── 方向一 Phase 1 (P1)              ├── 方向三 Phase 1 (P2)           ├── 方向四 Phase 2 (P1)
│   ├─ OpenAPI 从 proto 生成         │   ├─ go test -race CI          │   ├─ SLO 框架上线
│   ├─ 限流头标准化                  │   ├─ 核心路径基准测试          │   ├─ 容量规划模型
│   ├─ 错误码注册表                  │   ├─ SSO 混沌测试              │   ├─ 成本归因
│   └─ 版本头中间件                  │   └─ Fuzz 测试 (handler)      │   └─ 趋势告警
│                                     │                                 │
├── 方向二 Phase 1 (P1)              ├── 方向五 Phase 1 (P2)           │
│   ├─ 安全迁移模式清单              │   ├─ 架构适应度函数 x3        │
│   ├─ migrate validate 子命令       │   ├─ 技术债寄存器创建          │
│   ├─ 迁移中 /readyz 降级           │   ├─ ADR 模板 + 迁移 DECISIONS │
│   ├─ 回滚验证自动化                │   └─ Suite A/B 清理计划        │
│   └─ 迁移 lock_timeout 保护         │                                 │
│                                     ├── 方向三 Phase 2 (P2)           │
├── 方向四 Phase 1 (P1)              │   ├─ 更多混沌测试              │
│   ├─ 业务 RED 指标层               │   ├─ 端到端弹性验证            │
│   ├─ 依赖健康详细端点              │   └─ 模糊测试 CI 集成           │
│   └─ 存储使用趋势 gauge             │                                 │
│                                     │                                 │
└── 持续：每个方向保持季度审查        └── 持续                           └── 持续
```

### 立即可以启动的（3 天内完成）

| 任务 | 所属方向 | 天数 |
|------|---------|------|
| 限流头标准化（`X-RateLimit-*` + `Retry-After`）| 方向一 | 0.5 |
| 版本头中间件（`X-API-Version`）| 方向一 | 0.5 |
| 错误码注册表（`internal/pkg/errors/registry.go`）| 方向一 | 1 |
| `go test -race` 加入 CI | 方向三 | 0.5 |
| 安全迁移模式清单审查（migration #23 开始）| 方向二 | 1 |
| 业务指标层（messages_sent_total 等）| 方向四 | 1 |

### 关键依赖关系

- 方向一的 OpenAPI 规范需要 API 稳定——建议在第一个 breaking change 之前完成
- 方向二的迁移安全模式对所有未来迁移都有约束力——越早建立越好
- 方向三的混沌测试依赖 SSO mock（已存在）和 testcontainers（已存在）——技术基础已到位
- 方向四的 SLO 框架依赖 Prometheus（已存在）——不需要新基础设施
- 方向五的适应度函数独立于所有其他方向——可随时插入

---

## 附录：与既有文档的交叉验证

| 方向 | 在既有 ~25 份 requirements + 8 份 REVIEW 文档中的覆盖 | 本文贡献 |
|------|---------------------------------------------------|---------|
| **1. API 消费者契约** | `untapped-directions.md` 覆盖了 SDK/开发者平台，聚焦于"生成客户端代码"。未覆盖 OpenAPI 规范管线、限流头标准化、错误码注册表、版本策略 | 首次覆盖完整的 API 消费者契约 + 开发者体验四个维度 |
| **2. 生产迁移架构** | `remaining-architectural-gaps.md` 提到"零停机 Schema 迁移"（2 段简述）。无系统性分析 | 首次提出完整的安全迁移模式清单、执行上下文改进、迁移中 /readyz 行为、回滚验证自动 |
| **3. 分布式测试** | `SCHEMA_QUERY_TEST.md` 提到缺少 benchmark 和 race。`FLOW_CONCURRENCY_CACHE.md` 分析 goroutine 模式。无系统性测试基础设施缺口分析 | 首次提出完整的四个缺口（基准/竞态/混沌/fuzz）+ CI 集成方案 |
| **4. 可观测性与自主运维** | `SECURITY_OPS.md` 提到 SLO 框架（方向 C）。`INFRA_DEEP.md` 提到自诊断端点。无业务指标、成本归因、容量规划模型 | 首次提出业务 RED 指标层、SLO 框架实现、成本归因模型、依赖健康趋势仪表盘 |
| **5. 架构治理** | 无任何既有文档系统性地讨论架构适应度函数、技术债寄存器、ADR 自动化、依赖健康仪表盘 | 全新方向 |

---

*本文档基于 `main` 分支 2026-07-10 状态的全局代码扫描完成。无代码变更。*
