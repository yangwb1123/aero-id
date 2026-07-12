# 架构评估报告：aero-id 交付基础设施缺口与技术债务

## 1. 架构评估

### 1.1 当前架构的优势

| 维度 | 优势 | 代码证据 |
|------|------|----------|
| **模块边界** | 严格的单向依赖：`handler → service → repository → GORM`，`storage`、`sso` 作为独立支柱接入。`internal/app/app.go` 是唯一的组合根，没有循环依赖或跨层调用。 | `app.go:83-120` 的 repo 构造；各 handler 只持有 service 接口 |
| **领域隔离** | `domain/comment.go` 内聚了评论的创建校验（`ValidateNewComment`）、嵌套约束（`ErrNestingTooDeep`），且不依赖 GORM 或任何外部包——纯 Go 值和错误。 | `domain/comment.go:42-52` |
| **异步可靠** | 事务性 Outbox 模式（`internal/outbox/`）将通知、webhook 等副作用从请求路径解耦。带指数退避、死信隔离和 Prometheus 指标。 | `relay.go:92-150` 的 drain 逻辑；`metrics.go` 的 `ObserveOutboxDrain` |
| **可观测性** | 自包含 Prometheus exposition（`metrics.Registry`），无 `client_golang` 依赖；OpenTelemetry tracing 可选但全局安装 W3C TraceContext 传播器。 | `metrics.go` 全文件；`tracing.go:56-58` |
| **配置管理** | YAML + 环境变量覆盖（`AERO_ID_*` 前缀），`config.Load` 启动时校验必填项。清晰的分离。 | `configs/config.yaml` |
| **测试分层** | 单元测试（无 Docker 需求）+ 集成测试（`//go:build integration`，testcontainers）分离。 | `testpg/testpg.go` 的 `go:build integration` |

### 1.2 架构局限与技术债

#### P0（必须修复）

**1. Relay 的 Context 断裂（`relay.go:111`）**

```go
ctx := context.Background()  // 在 Start() 的 goroutine 中硬编码
```

- 结果：所有 outbox relay 处理（通知投递、webhook）丢失了原始的 trace context、超时控制、取消传播。
- 当前 `WebhookHandler.Handle(ctx)` 虽然接收了 `ctx`，但它的 `context` 是无根的——span 是孤儿，超时只能靠 HTTP client 的 `Timeout` 字段兜底。
- 这是一种**架构债务**：`Handler` 接口设计时假设 relay 会传播有意义的 context，但 relay 的实现没有遵守契约。

**2. Docker Compose 不可启动（`deployments/docker-compose.yml`）**

```yaml
AERO_ID_SSO_ENDPOINT: "sso:9000"  # SSO 服务不存在
```

- 结果是 `docker compose up` 必然失败——aero-id 容器依赖的 SSO endpoint 无法解析。
- 这直接阻塞了：
  - 本地开发调试（`make run` 只能跑单体，无法验证完整的集成流程）
  - CI 中复用 Compose 环境跑集成测试（用户分析中方向二的路径）
  - 新贡献者的 onboarding 体验

#### P1（重要但非阻塞）

**3. GORM 泄漏到 Repository 接口外部**

`repository/comment.go` 的 `CreateWithReplyBump` 接受 `*gorm.DB` 作为参数（通过 `outbox.Repository.Append` 的签名间接暴露）。更广泛地说，`outbox.Repository` 接口的 `Append(ctx, tx *gorm.DB, e *Event)` 接受了 GROM 事务对象——但 `service` 层应该对持久化实现一无所知。

- 当前这不是紧急问题，因为 `service` 层不直接调用 `Append`——它传递 `*outbox.Event` 到 repository，后者在事务内调用 `Append`。但接口设计上 `Repository.Append(tx *gorm.DB)` 绑定到了 GROM API。
- 长远来看，如果底层换成别的 ORM 或 raw SQL，这个接口需要重新设计。

**4. `like_count` 在全表扫描路径上**

`repository/comment.go` 的 `Like`/`Unlike` 方法在单个事务中使用 `INSERT ... ON CONFLICT DO NOTHING` + `UPDATE like_count = like_count +/- 1`。

- 高并发下这个模式没有 TOCTOU 问题（用户分析已确认：语句级别的原子性保证快照一致性），但：
  - `Like` 在 `comment_likes` 表上执行 `INSERT` + `SELECT`（读回 `like_count`）——两个语句
  - `Unlike` 同理
  - 大量并发点赞会竞争 `comment` 行锁
- 性能瓶颈不是计数器漂移，而是**行锁争用** + **两轮 round-trip**

**5. 文件大小逼近限制边界**

`internal/service/comment.go` 当前 472 行，`internal/app/app.go` 516 行（超过 500 行限制）。`internal/repository/comment.go` 也较大（~420 行）。这些文件如果继续增长，将触发 AGENTS.md 中 500 行的硬限制。

#### P2（值得关注）

**6. SSE Hub 没有持久化或背压通知**

`hub.Hub` 使用 16 个缓冲 channel + 非阻塞发送。慢消费者静默丢弃事件。

- 这对实时推送是可以接受的，但如果客户端断开重连，它依赖 Last-Event-ID + 通知库查询来恢复缺失事件。
- 目前的实现没有内置的“重放缓冲区”——`notification/service.go` 在连接重建时需要从数据库查询未读通知。

**7. 配置的 tracing `sample_ratio` 语义不直观**

`tracing.go:87-91`：`sample_ratio: 0` 的解释是 "always sample"（因为 `0 <= 0 => ParentBased(AlwaysSample)`）。但用户可能期望 `0` 表示 "不采样"。这是文档和代码之间的缺口。

**8. `idempotency_ttl` 只在 HTTP 层配置，gRPC 没有幂等性支持**

`app.go:211` 中 `IdempotencyTTL` 只传递给 HTTP router。gRPC 层没有任何幂等性中间件。如果客户端通过 gRPC 重试请求，可能产生重复的 side-effect。

---

## 2. 扩展方向

### 方向 A：Relay Context 治理与 Trace 传播（P0）

**为什么需要**：
- 当前 `context.Background()` 导致 outbox 处理的 traces 断裂——所有 span 从 relay 开始就成为孤立的 trace，无法关联回触发事件的原始请求。
- 对可观测性是关键缺口：生产排障时无法追踪"哪个请求导致了这条通知"。

**核心挑战**：
- 第二层（trace 传播）需要持久化 W3C TraceContext（`trace_id` + `span_id`）到 outbox 表，并且在 relay 侧恢复 span 作为子 span。
- 数据模型变更需要 migration，对已有 outbox 行需要 graceful 处理（没有 trace_id 的事件不影响 relay）。

**预期架构变更**：

```
outbox.Event ← 新增 TraceContext 字段
outbox.Repository.Append ← 自动提取 otel.GetTextMapPropagator 序列化
Relay.drain ← 取出事件后先恢复 span context，再 handler.Handle
Handler 签名 ← 不需要改（context 已经携带 trace）
```

**架构影响**：
- `outbox.Event` 结构体增加 `TraceParent []byte`（或类似 W3C propagator 的 wire format）
- `outbox.NewEvent` 新增可选参数 `WithTraceContext(ctx)`，从 `context` 提取
- `relay.drain` 中对每个事件：`ctx = otel.GetTextMapPropagator().Extract(ctx, ...)` 后传给 `handler.Handle`
- 对现有行兼容：无 trace 的事件走 noop span，不影响业务逻辑

**建议实现路径**：
1. 第一层（立即）：为 relay goroutine 创建带超时的 `ctx`（修复悬空 goroutine 风险）
2. 第二层（短期）：修改 `NewEvent` + `Append` 在创建 event 时捕获 trace context
3. 第三层（短期）：`relay.drain` 在 `handler.Handle(ctx, e)` 前恢复 span

### 方向 B：集成测试在 CI 中的正确运行（P0/P1）

**为什么需要**：
- 当前 CI 只跑 `go test ./... -count=1 -race`，跳过了所有 `//go:build integration` 的测试。
- 仓库变更（尤其是 SQL 查询、repository 逻辑、storage 后端）后，集成测试从未被验证。
- `internal/repository/testpg/` 存在但未被使用——这是已支付的基建成本未被利用。

**核心挑战**：
- testcontainers 拉取 Postgres/MinIO 镜像的时间开销（首次约 30-60s，缓存后约 5-10s）
- 10+ 个集成测试包并行跑可能造成资源争抢（端口冲突、容器名称冲突）
- MinIO 的 testcontainers 模块尚未在 `testpg.go` 中封装（目前只有 Postgres）
- CI runner 资源限制可能导致 testcontainers 启动超时

**预期架构变更**：
- `testpg` 包扩展：支持 MinIO testcontainers，或至少提供 `MinIOStart(t)` 辅助函数
- `ci.yml` 增加 job：`go test -tags=integration ./internal/repository/... ./internal/storage/... -count=1 -race -timeout 15m`
- 或者：分离 `integration` job 为独立步骤，允许缓存镜像层

**架构影响**：
- 无代码变更（测试辅助库已有），只需 CI 配置变更 + 少量测试基础设施扩展
- 建议先最小化：仅跑 `./internal/repository/...` 和 `./internal/storage/...` 的集成测试，不跑全量

**建议实现路径**：
1. 修 Compose（方向 C）后，测试可以复用 compose 环境——更快
2. 不改 Compose 但直接加 testcontainers step——改动更小（用户的选择）
3. 折衷：CI 中增加一个 `integration` job，与 `test` 并行，跑有限包

### 方向 C：SSO Mock 化与 Compose 可启动（P1）

**为什么需要**：
- 当前 Compose 无法启动，阻塞本地开发和集成验证
- SSO 作为唯一的外部 gRPC 依赖，在开发/测试环境中应该可以 mock 或 stub

**核心挑战**：
- SSO 接口是 gRPC proto 定义的，mock 需要一个 gRPC server stub
- `sso.Client` 连接是懒加载的，所以只要 SSO endpoint 可解析且返回有效响应，服务就能运行
- 但 mock SSO 需要维护一套 fake gRPC handler，与真实 SSO 的 proto 同步

**预期架构变更**：
- **方案一（推荐）**：在 `deployments/` 中添加一个轻量 SSO stub 容器，实现最少的 `ValidateToken`/`GetUsers` RPC，使用固定测试 key/uid
- **方案二**：在 `internal/sso` 包中增加 `StubServer`（`//go:build !production`），用于开发模式
- **方案三**：修改 Compose 移除 SSO 依赖，让 aero-id 在 `SSO endpoint unreachable` 时以降级模式运行（但违反 AGENTS.md 的 "validated-but-empty uid is rejected" 约束）

**架构影响**：
- 方案一最佳：Docker Compose 增加 `sso-stub` 服务（~50 行 Go 代码 + 多阶段构建）
- 对主代码无侵入，只影响部署配置

### 方向 D：去中心化计数器（Like/reply）的性能重构（P1/P2）

**为什么需要**：
- `like_count` 和 `reply_count` 的当前实现每次写入都需要 `SELECT count(*)` 回读或行级 `UPDATE counter +/- 1`。
- 在点赞/取消点赞高并发场景下（如热门评论），行锁争用可能是瓶颈。
- `reply_count` 的 `IncReplyCount` 也不在一个原子 UPSERT 中——它和 `CreateWithReplyBump` 同一个事务，但 `IncReplyCount` 单独暴露时没有事务保护。

**选项对比**：

| 方案 | 一致性 | 性能 | 复杂度 |
|------|--------|------|--------|
| **A. 现状（事务内 +/- 1）** | 强一致 | 行锁竞争 | 低 |
| **B. 异步累加器（Redis/内存）** | 最终一致 | 极高 | 高（引入新依赖） |
| **C. 计数器表 + UPSERT** | 快照隔离 | 高（无行锁） | 中 |
| **D. `SELECT count(*)` 查询时即时计算（不加 denormalized counter）** | 强一致 | 低（全表扫描） | 极低 |

**建议**：**方案 C**——将 `like_count` 从 `comments` 表的 denormalized 列，改为单独的 `comment_counters` 表：

```sql
CREATE TABLE comment_counters (
    comment_id   UUID PRIMARY KEY REFERENCES comments(id),
    like_count   INT NOT NULL DEFAULT 0,
    reply_count  INT NOT NULL DEFAULT 0,
    version      INT NOT NULL DEFAULT 1  -- 乐观锁
);
```

更新使用 `UPDATE ... SET like_count = like_count + 1, version = version + 1 WHERE comment_id = ? AND version = ?`（乐观锁）。读时先读 counters 表——不阻塞 `comments` 主流。

但这引入了**跨表事务**：`CreateWithReplyBump` 需要同时插入 `comments` 行和初始化 `comment_counters` 行。事务性仍然成立，但复杂性增加。

**替代简化方案**（当前架构内）：将 `Like`/`Unlike` 的 `UPDATE like_count +/- 1` + `SELECT like_count` 合并为 `RETURNING` 语句，减少一次 round-trip：

```sql
UPDATE comments SET like_count = like_count + 1 WHERE id = ? RETURNING like_count
```

但 GORM 的 `UpdateColumn` 不支持 `RETURNING`——需要回退到 raw SQL。

**架构影响**：
- 如果选方案 C，需要新增 repository 接口方法、migration、domain 结构调整
- 如果选简化版（RETURNING），只影响 repository 实现，接口不变

### 方向 E：gRPC 幂等性支持（P2）

**为什么需要**：
- HTTP 层有 `idempotency_ttl` 和 `IdempotencyStore`，但 gRPC 层没有。
- gRPC 客户端（尤其是自动重试的）可能重复提交 `CreateComment`、`Like` 等有 side-effect 的请求。
- 当前 `Like` 是幂等的（ON CONFLICT DO NOTHING），但 `CreateComment` 不是——会创建重复评论。

**核心挑战**：
- gRPC 中间件需要从 metadata 中读取幂等键（HTTP 从 `Idempotency-Key` header 读取）
- 中间件需要共享同一个 `IdempotencyStore`（当前只有 HTTP handler 持有）
- gRPC response 的 cache 不如 HTTP 的直接（需要 stream-aware caching）

**架构影响**：
- `app.go` 中的组合根需要传 `IdempotencyStore` 到 gRPC server 构造
- `internal/handler/grpc` 新增 unary interceptor 检查幂等键
- 共享 `internal/idempotency` 包

---

## 3. 接口设计建议

### 3.1 关键模块接口设计原则

当前接口设计的几个亮点值得保持：

1. **Repository 接口定义在访问者所在的包**
   - `repository/comment.go` 定义了 `CommentRepository` 接口，`service/comment.go` 通过它访问持久化。
   - 这遵循了"依赖倒置"：高层（service）定义它需要的抽象，低层（repository）实现它。

2. **Service 接口定义在 service 包外部**
   - `NewCommentService` 返回 `CommentService` 接口（定义在 `service/comment.go` 中），handler 只依赖接口。
   - 但接口定义在 `service` 包内部——handler 包需要 import `service` 包。这是 Go 社区的标准做法，但严格接口隔离的做法是把接口定义在使用侧（handler 包）。当前做法没问题，不需要改。

3. **Option 模式扩展**
   - `WithCommentAuditLogger`、`WithBlockChecker` 等选项函数，允许在不同 transporter 中按需注入功能。
   - **建议**：保持此模式，避免向 service 构造函数传递大量配置参数。

### 3.2 需要引入的新抽象层

**短期**（P0-P1 范围）：

1. **`internal/outbox/trace.go`**（或集成到现有 `outbox` 包）
   - 职责：在 `NewEvent` 时从 context 提取 TraceContext，在 `Relay.drain` 时恢复。
   - 不需要新接口，只需要工具函数。

2. **`internal/sso/stub.go`**（开发模式 mock）
   - 可选实现 `sso.Client` 接口的 mock，用 `//go:build develop` 标记。
   - 供本地开发和 Compose 使用，不进入生产构建。

**中期**（P1-P2 范围）：

3. **计数器接口抽象**
   - 如果走向方案 C（独立计数器表），需要 `CounterRepository` 接口：
   ```go
   type CounterRepository interface {
       Increment(ctx, commentID, field string) (int, error)
       Decrement(ctx, commentID, field string) (int, error)
       Get(ctx, commentID) (LikeCount, ReplyCount, error)
   }
   ```
   - 好处：后端可以从 `comment_counters` 表切换到 Redis 或其他高速缓存，service 层不变。

### 3.3 向后兼容性策略

所有 migration 必须遵循：

1. **Additive changes**：新列/表先用 `ALTER TABLE ... ADD COLUMN ... DEFAULT NULL`，老版本代码忽略新列。
2. **Two-phase removal**：删除列时，先在 v1 发布中停止写入但保持读取，在 v2 中删除列和代码。
3. **Outbox schema 变更**：`trace_parent` 列必须 `DEFAULT NULL`，已有行不需要回填——relay 检测到 nil 时走 noop span。

---

## 4. 技术选型

### 4.1 引入 OTel 是正确的选择

当前 tracing 使用 OpenTelemetry 标准 SDk，是行业共识。建议：

- **保持** OTel 作为唯一的 tracing 基础设施
- **不引入** 其他 APM 供应商 SDK——通过 OTel Collector 导出到任何后端（Jaeger、Datadog、Grafana Tempo）
- **已正确实现**：noop fallback + W3C TraceContext 全局传播

### 4.2 不需要引入 client_golang

当前自包含的 Prometheus exposition 实现（`metrics.Registry`）：

- **优点**：零依赖，构建速度快，编译时间短
- **缺点**：缺少高级功能（直方图分位数、Exemplar、原生直方图）
- **建议**：保持现状。如果将来需要更高级的 metrics，再迁移到 `otelmetric`（OpenTelemetry metrics）——届时可以从 `metrics.Registry` 平滑迁移，因为调用点（`ObserveHTTP`、`ObserveGRPC`）已经是抽象接口。

### 4.3 不使用 Redis 作为计数器后端

在方向 D 中，我明确反对引入 Redis 做计数器：

- 当前栈只有 Postgres + MinIO/S3
- 引入 Redis 增加了运维复杂度和部署依赖
- 对于 `comment_likes` 表，Postgres 的 UPSERT + 乐观锁足够满足预期负载
- 如果未来成为真实瓶颈，在线做 benchmark 验证后再引入不迟

### 4.4 建议使用的第三方库

| 用途 | 推荐 | 理由 | 是否引入 |
|------|------|------|---------|
| gRPC 幂等性 | 手写中间件 | 业务逻辑简单，不需要库 | 否 |
| 测试：SSO stub | 手写 gRPC server | 接口固定，代码量小 | 是（内部包） |
| 测试：数据工厂 | 自建 factory 函数 | 已有 `domain.NewComment` 等 | 否 |
| **不引入**：Task runner | 无需 | Outbox 模式已覆盖 | 否 |
| **不引入**：Event bus | 无需 | Outbox relay 是事件总线 | 否 |

---

## 5. 实施路线图

### 优先级排序

```
P0（阻塞）     ← 发布阻断，跳过则系统不完整可用
P1（重要）     ← 生产就绪的必要条件
P2（值得做）   ← 提升质量但不阻塞
```

### 阶段划分

#### 阶段 0 — 立即执行（1-2 天）

| # | 任务 | 类型 | 具体 |
|---|------|------|------|
| 1 | Relay context 第一层 | P0 | `relay.go:Start()` 中每个 tick 创建带超时的 `context` |
| 2 | Relay context 第二层 | P0 | `NewEvent` 捕获 trace context，`drain` 恢复 span |
| 3 | CI 集成测试 | P1 | `ci.yml` 增加 `integration` job 跑 repository/storage 集成测试 |

**风险**：如果 testcontainers 在 CI 中不稳定（镜像拉取超时），需要降级到可选的 `-tags=integration` 步骤。

#### 阶段 1 — 基础修复（3-5 天）

| # | 任务 | 类型 | 具体 |
|---|------|------|------|
| 4 | Compose 可启动 | P1 | 在 Compose 中添加 SSO stub 容器，或修改 `AERO_ID_SSO_ENDPOINT` 使其可选 |
| 5 | 拆分大文件 | P1 | `internal/app/app.go`（516行）拆分为 `app.go` + `app_storage.go` + `app_outbox.go` |
| 6 | gRPC 幂等性 | P2 | 将 `IdempotencyStore` 注入 gRPC server，添加 unary interceptor |

**风险**：拆分 `app.go` 时需要确保 `New()` 函数的逻辑不被破坏——建议按 concern 提取 `buildXxx()` 辅助函数（已经有 `buildStorage()` 模式了，扩展它）。

#### 阶段 2 — 优化（5-10 天）

| # | 任务 | 类型 | 具体 |
|---|------|------|------|
| 7 | 计数器性能重构 | P1/P2 | 将 `Like`/`Unlike` 合并为 `RETURNING` 语句，或迁移到 `comment_counters` 表 |
| 8 | Outbox 巡检工具 | P2 | 实现 `agent/cmd/reconcile` 检查死信、悬挂引用、counter 溢出 |

**风险**：阶段 2 需要 migration（如果选方案 C），必须确保 down migration 完整。

### 风险矩阵

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|----------|
| testcontainers CI 中 Postgres 镜像拉取超时 | 中 | 高（集成测试不可用） | 设置 `DOCKER_IMAGE_CACHE`；先跑无 Docker 的 unit test，integration 作为可选步骤 |
| SSO stub 与真实 SSO proto 不同步 | 低 | 中（测试漏掉真实 SSO 行为） | 定期 `make proto` + CI 校验 stub 编译通过 |
| 计数器重构引入 regresion | 低 | 高（like_count 不准确） | 在 `comment_likes` 表上保留 `SELECT count(*)` 的巡检 SQL |
| `app.go` 拆分后 `New()` 构造顺序错乱 | 中 | 中（启动失败） | 拆分后 `make test && make lint` 必须通过；集成测试验证启动路径 |
| GORM `RETURNING` 不支持 | 确定 | 低（只是回退到 raw SQL） | repository 保留 GORM `UpdateColumn` 作为备选路径 |

---

## 总结建议

从当前代码质量看，aero-id 的架构基础是扎实的——清晰的关注点分离、事务性 outbox 模式、合理的可观测性配置。主要缺口集中在三个方面：

1. **可观测性连贯性**（Relay context 断裂）——这是唯一真正需要代码变更的 P0 问题
2. **CI 覆盖面和开发体验**（集成测试未运行、Compose 不可用）——影响持续交付信心，但不阻塞代码正确性
3. **可维护性边界**（大文件拆分、计数器长期性能）——需要规划但不需要立即行动

我的建议是：**先做阶段 0，然后阶段 1，阶段 2 根据实际负载数据决定是否执行**。方向 E（gRPC 幂等性）可以推迟到 gRPC 客户端接入真实流量后再评估——如果不使用自动重试，幂等性的价值有限。
