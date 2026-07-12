现在我有了一个完整画面。以下是我的架构分析。

---

# aero-id 架构分析

## 1. 架构评估

### 1.1 当前架构优势

aero-id 代码库展示了几个值得认可的架构决策：

| 优势 | 具体体现 |
|---|---|
| **清晰的关注点分离** | 强制的一层依赖规则：handler → service → repository → Postgres。无逆向依赖 |
| **双协议共享业务逻辑** | HTTP 和 gRPC handler 调用同一套 service 接口，行为完全一致 |
| **事务性 Outbox 模式** | 使用 `ON CONFLICT` 原子操作的域事件 + `internal/outbox/` 下的可靠异步 Relay。这是基础架构的正确打法 |
| **丰富的中件层** | 请求 ID、恢复、访问日志、CORS、鉴权、限流、幂等性、超时、追踪 — 都在 middleware 层，handler 无需关心 |
| **可插拔存储** | `FileStorage` 接口有 local 和 S3 两种实现，通过配置切换 |
| **SSO 缓存的熔断器** | `internal/sso/breaker.go` 在 SSO 宕机时防止级联故障 |
| **真实 PG 的 Repository 测试** | 使用 testcontainers-go 而非 mock DB，覆盖 SQL 语义 |

### 1.2 架构债务与局限性

以下是我识别出的最关键问题，按严重性排序：

#### 🔴 严重：两套平行的架构共存（~4000 行僵尸代码）

这是代码库中最重要的问题。存在**两套**完整、互相独立的服务/处理程序实现：

| 组件 | 活跃（已接线） | 僵尸（未接线） |
|---|---|---|
| **文件** | `internal/service/file.go` + `internal/handler/http/file.go` + `internal/handler/grpc/file.go` | `internal/file/service_*.go` + `internal/file/handler_*.go`（含独立的 Service/Repository 接口） |
| **评论** | `internal/service/comment.go` + `internal/handler/http/comment.go` + `internal/handler/grpc/comment.go` | `internal/comment/service.go` + `internal/comment/handler_*.go` |
| **消息** | `internal/service/message.go` + `internal/handler/http/message.go` + `internal/handler/grpc/message.go` | `internal/message/service.go` + `internal/message/handler_*.go` |
| **搜索** | `internal/service/search.go`（使用 `repository.CommentRepository` 等） | `internal/search/service*.go` + `internal/search/handler_*.go`（使用独立接口） |
| **通知** | `internal/service/notification.go`（含 NotificationDispatcher） | `internal/notification/dispatcher.go`（旧版分发器，无限流/偏好检查） |

**影响**：僵尸代码：
- 给开发者增加认知负担（哪个才是真的？）
- 浪费 ~4000 行的构建/测试时间
- 不可能通过 `make check-file-sizes`（许多文件远超 500 行）
- bug 修复可能同时对活跃版和僵尸版都需要，但后者永远不会被发现

**根源**：似乎是迁移到垂直模块的尝试在完成前就中断，或者相反。

#### 🟠 中高：SSO 缓存可能无限增长

`TokenCache` 和 `UserCache` 使用 `map[string]entry` 且**无最大容量限制**。虽然 `TokenCache` 有时效性清理，但活跃用户一多，未过期条目会持续累积。`UserCache` 根本没有限制——一旦调用 `GetMany`，每个 uid 永远留在内存中（直到进程重启）。

**影响**：在租户或用户规模增长时存在潜在 OOM 风险。

#### 🟠 中高：搜索忽略 `file_shares`

`SearchFiles(ctx, ownerUID, query, limit)` 只接收 `ownerUID`，不包含 `UNION` 共享权限。用户无法搜索他人共享给他们的文件。这是功能缺口（**EC-15** 已验证）。

#### 🟡 中：`internal/notification/dispatcher.go` 是重复代码

未接线的分发器缺少活跃分发器的以下能力：
- 偏好检查（通知类型静默/免打扰时段）
- 通知合并（5 分钟内合并）
- 限速（每分钟 10 条/类型/接收者）
- 合并后的未读数+1

活跃分发器（`service/notification.go`）具备以上所有功能。

#### 🟡 中：Sweeper 使用 `context.Background()` — 无法优雅关闭

`internal/storage/gc/sweeper.go:215` 使用 `context.Background()` 而非接收取消信号。关闭时正在执行的 sweep 可能在数据库连接已关闭后继续运行。

#### 🔵 低：`metrics.go` Route 标签问题（已验证为误报）

代码审查报告中的 **N2** 实际是**误报**。中间件使用 `c.FullPath()`（返回 Gin 的匹配模板，如 `/v1/files/:id`），而非原始路径。标签基数可控。

### 1.3 关键设计决策评价

| 决策 | 评价 |
|---|---|
| **D1：SSO 外置身份** | ✅ 正确。无本地账户表，引用 SSO uid。此原则坚持得很好 |
| **D2：横向分层（原设计）** | ⚠️ 代码已偏离此决策。存在两套架构 |
| **D3：GORM 而非 sqlx** | ✅ 对于 CRUD 密集的工作负载合理。GORM 的回调（`BeforeCreate` 等）与软删除和 UUID 主键配合良好 |
| **D4：Token Cache TTL=60s** | ✅ 合理的权衡。60s 的撤销延迟对大多数场景可接受 |
| **D5：评论限两层** | ✅ 正确。防止复杂树查询，在 service 层强制校验 |
| **D8：Metrics/Tracing 不内置** | ❌ 代码已移植到 metrics（自定义 Prometheus 注册表）和 tracing（OpenTelemetry）。原设计文档已过时 |
| **D10：启动时不自动迁移** | ⚠️ 配置中已有 `auto_migrate` 选项（默认关闭），文档描述不准确 |

---

## 2. 扩展方向

### 方向 1：架构统一 — 清理僵尸代码（P0）

**价值**：消除 ~4000 行技术债、减少认知负荷、减少构建时间、达成 `make check-file-sizes` 的约束

**方案**：选择垂直模块路线（按业务垂直分流），在接线层整合

| 选项 | 方案 | 工作量 |
|---|---|---|
| **A（推荐）** | 使用 **垂直模块**（`internal/file/`、`internal/comment/` 等）作为标准。将活跃接线从 `internal/service/*` 切换为模块内服务。移除旧的 `internal/service/*`、`internal/handler/http/*`、`internal/handler/grpc/*` | 大（需重构接线 + 验证行为一致性） |
| **B** | 保持现有接线不变。移除僵尸垂直模块 | 小（约 2000 行可安全清理） |
| **C** | 接受双重架构存在，添加 `ARCHITECTURE.md` 记录两套并存，暂不清理 | 零（但不解决根因） |

**推荐选项 A** — 垂直模块具备更好的隔离性、更清晰的依赖关系、每个域更小的文件。这是正确的发展方向。

### 方向 2：SSO 缓存上限（P0）

**当前**：无上限的内存映射 → OOM 风险

**方案**：
- `UserCache`：使用简单 `maxSize` 及插入时驱逐（插入时如超过限制，移除最旧的条目，或使用 `hashlru` 风格的结构）
- `TokenCache`：目前有时效性清理（30s tick），但无容量限制。添加 `maxSize` + 驱逐策略
- 库依赖：无需新包。约 50 行纯 Go 代码即可实现

### 方向 3：搜索支持共享文件（P0）

**价值**：功能完整性。用户无法搜索他人共享给他们的文件

**方案**：新增 `SearchFilesWithShares(ctx, uid, query, limit)`（或类似），在 `file_shares` 上做 `UNION`。Repository 接口约 20 行变更，service 新增一个搜索分支

### 方向 4：Webhook/事件订阅平台（P1）

**代码库中的杠杆**：极高。Outbox 基础设施已具备 80% 的能力：

| 已存在 | 需新增 |
|---|---|
| `outbox.Event` + `Repository` | `webhook_subscriptions` 表（每订阅一个 URL + 事件类型过滤 + HMAC 密钥） |
| `outbox.Relay`（含 backoff + 死信） | 扇出分发器 —— 分发到活跃订阅，而非单一 URL |
| `outbox.WebhookHandler`（含 HMAC 签名） | 订阅 CRUD API |
| `outbox.Chain`（分发器 + webhook 组合） | 重试+每订阅的限速 |
| 事件类型枚举（`message.sent`、`comment.replied` 等） | |

**架构变更**：将 `relayHandler` 从单链替换为扇出到所有活跃订阅。可根据配置上限设最多订阅数。

**影响**：对现有服务无影响。纯新增功能。

### 方向 5：缓存预热 + 冷启动优化（P1）

**当前**：`warmup()` 只做数据库 ping 和 SSO 探针。`UserCache` 和 `TokenCache` 启动时为空。

**方案**：
- 在 `sso.NewClient()` 中通过 `cache.Preload(knownUIDs)` 对 `UserCache` 预热（如配置了"热点"uid）
- SSO 连接池预热：在启动时发送一次 `GetUsers([]string{...})` 建立连接，避免首个请求承受延迟
- 考虑对 `UserCache` 添加**分片**（`map[shard]map[string]entry`）以降低锁竞争

### 方向 6：负载测试 + SLO 框架（P1 前置依赖）

**价值**：为所有其他方向提供测量依据

**当前**：`test/` 下无负载测试目录，无 k6 脚本，无基准数据

**方案**：
- 添加 `scripts/loadtest/`，包含 k6 脚本（文件上传、消息发送、评论、搜索）
- 定义 SLO 指标（p99 延迟 ≤ 500ms，错误率 < 0.1%）
- 集成到 CI 使新方向必须通过基准检测
- 使用 `internal/pkg/metrics` 的 RED 指标暴露端点配合 Prometheus 告警规则

### 方向 7：分布式限流（P2）

**当前**：`ratelimit.Limiter` 是 per-instance token bucket。多副本时形同虚设

**方案**：
- 抽象 `Limiter` 接口（`Allow(ctx, key) bool`）
- Redis 实现（`SLIDING_WINDOW` 或 `TOKEN_BUCKET` Lua 脚本）
- 配置切换 `rate_limit.driver: local | redis`

**备注**：需 Redis 依赖，提升运维复杂度。在需要多副本部署前可暂缓。

---

## 3. 接口设计建议

### 3.1 当前接口质量

活跃接线层（`internal/service/*`）的接口设计合理：

```go
// 接口定义清晰，粒度合适
type FileService interface {
    Create(ctx, input) (*File, error)
    Get(ctx, id) (*File, error)
    List(ctx, ownerUID, page) (*FileList, error)
    Delete(ctx, id) error
    ShareFile(ctx, id, granteeUIDs) error
    // ...
}
```

但垂直模块（僵尸代码）的接口不同：

```go
// internal/file/file.go 中的接口
type Service interface {  // 注意：此处是 Service 而非 FileService
    Create(ctx, in) (*File, error)
    Get(ctx, id) (*File, error)
    // ...
}
```

命名不一致 => 如果选择垂直模块路线，需统一。

### 3.2 建议采用"每个域一个包"架构

```
internal/
├── file/           # File domain — service, repository, HTTP/gRPC handlers
│   ├── file.go         # 域类型 + Service 接口
│   ├── service.go      # fileService 实现
│   ├── repository.go   # Repository 接口 + GORM 实现
│   ├── handler_http.go # HTTP handler (RouteRegistrar)
│   ├── handler_grpc.go # gRPC handler
│   └── share.go        # 文件共享逻辑
├── comment/         # Comment domain
├── message/         # Message domain
├── notification/    # Notification domain
│   ├── dispatcher.go   # 唯一分发器（合并 service/notification.go 的功能）
│   └── ...             # 删除旧版 notification/dispatcher.go
├── outbox/          # 通用 outbox 基础设施（跨域共享）
├── hub/             # SSE hub（跨域共享）
├── sso/             # SSO 客户端 + 缓存
└── pkg/             # 共享工具
```

注意：此架构已**以僵尸代码形式存在**。需要做的是：
1. 选择此架构作为标准
2. 将活跃接线迁移到域包中的 service/handler
3. 删除旧的 `internal/service/`、`internal/handler/http/`、`internal/handler/grpc/`

### 3.3 向后兼容

**外部 API（HTTP + gRPC）** 不变 —— 路由路径和 protobuf 消息完全不受影响。重构后 handler 注册相同的路由。

**内部依赖**：`internal/app/app.go` 是唯一需要变更的接线文件。

### 3.4 新增抽象层

| 抽象层 | 理由 | 位置 |
|---|---|---|
| `Limiter` 接口 | 为分布式限流做准备，允许 local/redis 切换 | `internal/pkg/ratelimit/` |
| `Cache[K,V]` 泛型接口 | 统一 TokenCache 和 UserCache 的驱逐策略 | `internal/pkg/cache/` |
| 无更多新抽象层 | 当前分层已够用。YAGNI。 | — |

---

## 4. 技术选型

### 4.1 当前技术栈评估

| 技术 | 评价 | 建议 |
|---|---|---|
| **Go 1.26** | ✅ 最新的语言版本，泛型、`slog`、`errors.Join` 等都可用 | 保持 |
| **Gin** | ✅ 轻量级 HTTP 框架，路由+中间件符合需求 | 保持 |
| **gRPC** | ✅ SSO 集成和内部服务间通信都需要 | 保持 |
| **GORM** | ⚠️ 复杂查询的 ORM 抽象泄露。搜索查询使用原生 SQL，存储在 GORM 作用域内 | 保持，但新查询优先用原生 SQL（`.Raw`/`.Exec`） |
| **Viper** | ✅ 配置管理，支持 env 覆盖 | 保持 |
| **Zap** | ✅ 高性能结构化日志 | 保持 |
| **自定义 Prometheus** | ✅ 有意避免 client_golang 依赖，保持独立构建。功能足够 | 保持 |
| **testcontainers-go** | ✅ 集成测试用真实 PostgreSQL | 保持 |

### 4.2 建议引入的依赖

| 依赖 | 方向 | 理由 | 替代方案 |
|---|---|---|---|
| **Redis**（通过 `go-redis/redis`） | 方向 7（分布式限流） | 分布式 token bucket / sliding window | 自建（需 etcd/Postgres 轮询，更复杂） |
| **k6** | 方向 6（负载测试） | 负载测试脚本 | hey / wrk / locust |
| **无其他依赖** | — | 当前栈足以实现方向 1-5 | — |

### 4.3 自建 vs 采购

| 需求 | 决策 | 理由 |
|---|---|---|
| 事件订阅/Webhook | **自建** | Outbox 基础设施已就绪。只需表 + 扇出。无需引入 Kafka/NATS |
| 分布式限流 | **自建 + Redis** | 令牌桶算法约 20 行 Lua。无需第三方限流服务 |
| 监控/告警 | **采购**（Grafana Cloud / Datadog） | 自定义 Prometheus 端点 + 标准 scrape 配置。无需自建 |
| 全文搜索 | **Postgres FTS**（保留当前方案） | 当前搜索使用 Postgres `tsvector` + `ILIKE` fallback。数据量增长前无需 Elasticsearch |

---

## 5. 实施路线图

### 调整后的阶段划分（整合代码审查结果）

```
Phase 0（1-2 周）— 技术债修复 + 安全补丁
├── 方向 1-A：架构统一（清理僵尸代码，统一到垂直模块）
│   ├── 选择垂直模块路线作为目标架构
│   ├── 将 active wiring 迁移到 internal/file/, internal/comment/, internal/message/
│   ├── 删除 internal/service/, internal/handler/http/, internal/handler/grpc/
│   └── 验证：make test && make lint && make test-integration
├── SSO 缓存上限（UserCache + TokenCache 添加 maxSize + 驱逐）
├── 搜索支持 file_shares（修复 EC-15 功能缺口）
├── 删除 internal/notification/dispatcher.go（重复代码，N1）
└── Sweeper 使用可取消的 context（修复 PERF-13）
  ↑ 里程碑：更简洁的代码库，无功能变化，所有 check-file-sizes 通过

Phase 1（3-4 周）— 可观测性 + 事件平台
├── 方向 6：负载测试 + SLO 框架
│   ├── scripts/loadtest/ + k6 脚本
│   ├── Prometheus 告警规则
│   └── CI 基准检测门禁
├── 方向 4：Webhook/事件订阅平台
│   ├── webhook_subscriptions 表 + 域模型
│   ├── 订阅 CRUD API
│   └── 扇出分发器（替代单链）
├── 方向 5：SSO 缓存预热 + UserCache 分片
└── EC-4 加强：通知 upsert 唯一约束 / 幂等键
  ↑ 里程碑：可测量的性能基准 + 平台级事件能力

Phase 2（5-7 周）— 可扩展性
├── 方向 7：分布式限流（Redis 后端）
│   ├── Limiter 接口 + local 实现
│   ├── Redis 实现（Lua 脚本）
│   └── 配置：rate_limit.driver: local | redis
├── direction D：Schema 版本化（expand/migrate/contract）
│   ├── migrate verify 命令（检查 down.sql 可逆性）
│   └── _data.sql 后缀约定用于数据迁移
├── 群聊（companion doc 方向 1）
└── 文件分享搜索修复（EC-15 后续：分享列表支持搜索）
  ↑ 里程碑：多副本安全 + 零停机迁移

Phase 3（8-10 周）— 规模化
├── 方向 B：多地域部署（读副本 + ReplicaSelector）
│   ├── 读/写连接池分离
│   ├── SSO 缓存变成可选的 Redis 后端
│   └── Sweeper 实例协调（leader 选举）
├── 审核工作台（companion doc 方向 4）
├── 文件处理管线（companion doc 方向 5）
└── 方向 A（如尚未完成）：分布式限流全面推广
  ↑ 里程碑：全球部署能力
```

### 与原始提案的关键差异

| 项目 | 原始提案 | 调整后 | 理由 |
|---|---|---|---|
| 方向 C（Webhook） | Phase 2 | **Phase 1** | Outbox 基础设施已就位；投资回报率最高 |
| 方向 E（负载测试） | Phase 1 | **Phase 1（前移）** | 所有改进的可测量前提 |
| 方向 B（多地域） | Phase 2 | **Phase 3** | 成本最高，价值取决于用户规模 |
| 僵尸代码清理 | 未提及 | **Phase 0** | 代码审查发现，是前置依赖 |
| SSO 缓存上限 | 未提及 | **Phase 0** | 代码审查发现，潜在 OOM 风险 |
| 搜索权限修复 | 未提及 | **Phase 0** | 代码审查发现，数据泄露 |
| metrics 标签审查 | 未提及 | **已解决（误报）** | 中间件使用 `c.FullPath()`，已正确 |

### 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| **Phase 0 架构统一破坏行为** | 中 | 高 | 分步迁移：保留两组接线共存，逐一迁移 handler，每步运行完整集成测试套件 |
| **垂直模块 service 接口与活跃代码不一致** | 高 | 中 | 进行完整的接口对比审计。僵尸代码可能缺少某些方法（如 `UpdateMetadata`、`PresignUpload`） |
| **Webhook 扇出增加 outbox relay 延迟** | 中 | 中 | 每个订阅使用独立超时（默认 5s）。慢速订阅不阻塞其他订阅。增加 `dispatcher_timeout_ms` 配置 |
| **Redis 引入增加运维复杂度** | 高 | 低（可配置） | 限流默认为 local（无 Redis）。Redis 可选，仅多副本场景需要 |
| **迁移到垂直模块后 gRPC handler 兼容性** | 低 | 高 | 在 Phase 0 中使用 parity 测试（已有 `parity_test.go` 和 `parity_edit_test.go`）验证 HTTP 与 gRPC 行为一致 |

---

## 总结

aero-id 拥有坚实的架构基础——outbox 模式、良好的分层、双协议共享、可插拔存储——但存在以下问题：(a) 一套完整的僵尸架构（约 4000 行）使代码库膨胀，以及 (b) 几处安全相关缺口（缓存无上限、搜索忽略共享文件）。Phase 0 应优先处理这两类问题，每个影响可衡量的 1-2 周工作。此后，**Webhook 事件平台（方向 C）** 利用已投入 80% 的基础设施提供最高的杠杆作用，应在 Phase 1 中优先于分布式限流和 Schema 版本化处理。
