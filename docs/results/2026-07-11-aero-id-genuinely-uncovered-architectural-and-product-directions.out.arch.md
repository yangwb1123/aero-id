# Aero-ID 架构深度分析与扩展设计

## 1. 架构评估

### 1.1 当前架构的优势

经过对 `AGENTS.md` 和项目结构的分析，当前架构在以下方面表现出明显的设计成熟度：

| 维度 | 优势 | 依据 |
|------|------|------|
| **依赖方向** | 严格单向依赖 `handler → service → repository`，杜绝循环引用 | `AGENTS.md` Architecture 章节 |
| **关注点分离** | 每层职责清晰：handler 仅做 I/O 编解码，service 承载全部业务逻辑 | 同上 |
| **错误处理** | 分层错误转换：repo 返回原始 DB 错误 → service 包装为 `AppError` → handler 渲染 | `AGENTS.md` Conventions |
| **软删除** | 所有实体统一使用 `gorm.DeletedAt`，一致性高 | 同上 |
| **配置** | 统一 `configs/config.yaml` + 环境变量覆盖，`config.Load` 启动验证 | 同上 |
| **工程约束** | 文件 ≤500 行、函数 ≤50 行、复杂度 ≤10、if 嵌套 ≤3 层——强制执行 | `AGENTS.md` Engineering Constraints |
| **ID 设计** | UUIDv7 兼具时间排序和全局唯一性，天然支持游标分页 | `AGENTS.md` Conventions |
| **SSO 集成边界** | 不存储账号、不签发 token、仅通过 gRPC 验证——SSO 边界清晰 | `AGENTS.md` Invariants |

这种设计架构在 Go 微服务中属于 **"教科书级"** 的实现——尤其值得指出的是：

1. **Repository 返回原始错误**而非过早包装，是 GORM 生态中的正确做法——上层才知道用哪个 HTTP 状态码映射。
2. **Service 层作为"策略层"**同时被 HTTP 和 gRPC 复用，消除了"Write Once, Sync Twice"的常见缺陷（这是大量 Go 项目中 `handler` 层因"方便"而滋生业务逻辑的典型陷阱）。
3. **工程约束的被强制执行**——每个触发点的明确处理方案——使得代码库在面对复杂需求增长时仍能维持可维护性。

### 1.2 架构的局限性

然而，从长远规模化视角看，存在以下结构性局限：

#### 🔴 观察力不足——缺乏 Metrics/可观测性骨架

当前架构在 `service`、`repository`、`sso`、`storage` 层都没有预留 **Metrics 钩子（钩子点）**。典型的症状：

- `service` 层的每个方法既没有 `Duration` 记录，也没有 `ErrorCount` 计数
- `sso` 的 `TokenCache` 命中率不可观测
- `repository` 没有 SQL 查询耗时分布
- 回顾评审文档中"异步可观测性"方向确实已被覆盖，但当前代码库中**没有任何埋点**，说明可观测性还是"文档级愿景"而非"代码级事实"

#### 🟡 配置系统欠缺动态性

当前 `config.Load` 在启动时一次加载，此后不可变更。这意味着：

- 特性开关需要代码变更 + 重启
- `SIGNING_KEY` 轮转需要停机
- 日志级别调整需要重启
- 限流阈值无法在线调整

评审文档显示 "配置热重载 + 特性开关"已在 `2026-07-10-truly-uncovered-architecture-directions.md` 中有完整设计（含表设计、middleware、admin API），但**未实施**。

#### 🟡 缺少中间件抽象层

HTTP 和 gRPC 的认证、鉴权、限流、日志、请求 ID 等横切关注点目前通过 `middleware` 包处理，但：

- 这些中间件是否是统一的 Middleware 接口？还是各自为政？
- HTTP 和 gRPC 的认证逻辑是否有复用？还是各自实现？
- 从 `AGENTS.md` 看，有 `middleware` 包但行为未详细描述

#### 🟡 存储抽象层（FileStorage）设计需要审视

当前 `storage` 支持 `local | s3`，从描述看：

- `local` 模式使用 HMAC 签名的 presigned URLs
- `s3` 是 MinIO 兼容
- 问题：**presigned URL 的生成在哪个层？** 如果在 service 层，会耦合 storage 细节；如果在 storage 层，那 service 需要返回某种"凭证"而非直接文件

### 1.3 架构债务汇总

| 债务类别 | 严重程度 | 涉及区域 |
|---------|---------|---------|
| **零可观测性埋点** | 🔴 高 | 所有层——service、repository、sso、storage |
| **配置系统静态化** | 🟡 中 | config 包、所有依赖配置的模块 |
| **跨层错误处理缺乏标准化** | 🟡 中 | `AppError` 定义可能不完整（未见 `ErrorCode`、`Retryable` 等字段） |
| **迁移策略代码级缺失** | 🟡 中 | 文档有安全迁移模式，但代码中无 `Migration` 接口或版本标记 |
| **API 版本化仅 HTTP 层级** | 🟢 低 | gRPC 服务版本化策略未明确 |

---

## 2. 扩展方向

### 方向一：可观测性骨架（Observability Skeleton）

**为什么需要：**
- 当前无法回答以下基本问题：`CreateFile` 的 P99 延迟是多少？`sso.ValidateToken` 的失败率是多少？`TokenCache` 的命中率是否健康？
- 在扩展新功能之前，必须建立"能看清系统在做什么"的能力。否则每一次新功能的上线都是盲人摸象。
- 评审文档指出"5条异步管线"和"W3C TraceContext 延续方案"已有文档覆盖但未实施——当前是实施时机。

**核心挑战：**
1. **攻击性最小化**：埋点不得增加 measurable 延迟（需要选高性能指标库）
2. **Type-safe Metrics 定义**：不要用字符串标签，要用强类型枚举
3. **TraceContext 传播**：需要从 SSO gRPC 调用到 DB 查询完整保留 trace ID
4. **现有代码零侵入**：不应为了加 metrics 而改变函数签名

**预期的架构变更：**

```
internal/
  telemetry/          ← NEW PACKAGE
    metrics.go          # 预定义的 Prometheus/OTel metrics（counter/histogram/gauge）
    trace.go            # TraceProvider 配置
    middleware.go       # HTTP/gRPC 统一 instrumentation middleware

service/
  file_service.go     # 现状
  file_obs.go         # NEW - FileService 的 metrics 包装（使用 decorator 模式）
```

**关键设计决策——Metrics 采集方案：**

| 方案 | 优点 | 缺点 | 推荐 |
|------|------|------|------|
| **Prometheus 客户端库（直接）** | 零依赖、Go 标准生态、Grafana 生态成熟 | 手动注册、无自动 instrumentation | ⭐ **首选** |
| **OpenTelemetry Metrics SDK** | 厂商无关、未来可切换 | API 偏重、Go 生态不如 Java 成熟 | 次选——可做未来迁移目标 |
| **自建计数器** | 无第三方依赖 | 要自建 scrape endpoint、无标准 dashboard | 不推荐 |

**推荐选型：** `prometheus/client_golang` + `otel` 仅用于 Trace（trace 用 OTel 是因为其 W3C TraceContext 标准化是跨服务追踪的唯一合理选择）。

**对现有系统的影响：**
- 零影响——通过 decorator/wrapper 模式包裹现有 service，不改变签名
- 需要新增 `go.mod` 依赖 2-3 个包
- 测试需要 mock metrics 注册表

---

### 方向二：配置热重载 + 特性开关系统

**为什么需要：**
- 评审文档指出已有完整设计（含表设计、middleware、admin API）但未实施
- 典型场景：限流阈值在线调整、黑名单增删、日志级别切换、新功能灰度（feature flag）
- 配置变更不应成为"提交 PR → CR → 部署"这一小时级周期的一部分

**核心挑战：**
1. **原子切换**：变更配置时不应使系统处于不一致状态（所有 goroutine 看到同一版本）
2. **类型安全**：配置值不应是 `string`，而应该是强类型（`time.Duration`、`int`、`bool`）
3. **监听机制**：底层如何检测变更？文件 watch？HTTP admin API？两者都要？
4. **向下兼容**：新系统必须与现有 `configs/config.yaml` 完全兼容

**预期的架构变更：**

```
internal/
  config/
    loader.go         # 现有 — 启动加载
    watcher.go        # NEW — fsnotify + sub-second 重载
    dynamic.go        # NEW — 类型安全的热配置值（LiveConfig[T]）

  feature/            # NEW PACKAGE
    flag.go           # FeatureFlag 枚举定义
    store.go          # 持久化开关（Postgres via repository）
    middleware.go      # HTTP/gRPC 中间件注入 flag context

middleware/
  feature.go          # NEW — 特性开关中间件
  ratelimit.go        # 现有，但改为读取动态配置
```

**关键设计决策——配置热重载架构：**

```
┌─────────────┐    ┌──────────────┐    ┌────────────────┐
│  fsnotify    │───→│  ConfigWatcher│───→│  sync.Map       │
│  (file watch)│    │  (merge+validate)│  │  (atomic store) │
└─────────────┘    └──────────────┘    └───────┬────────┘
                                               │
                              ┌────────────────┼────────────────┐
                              ▼                ▼                ▼
                        LiveConfig[int]   LiveConfig[bool]  LiveConfig[[]string]
                        (RateLimit.QPS)   (Feature.Disable)  (Auth.AllowedIPs)
```

**对现有系统的影响：**
- 对现有代码零侵入——`LiveConfig[T]` 可替换现有 `Config` 结构体中的对应字段
- 需要新增 `fsnotify` 依赖
- **注意**：`config.Load` 的启动验证逻辑可复用——重载时调用同一组 validator

**特性开关中间件设计权衡：**

| 方案 | 优点 | 缺点 |
|------|------|------|
| **Handler-level middleware** | 细粒度控制、按路由开启 | 每个 handler 需显式注册 |
| **Path-prefix middleware** | 零 handler 改动 | 无法处理 `GET /files/:id` 和 `DELETE /files/:id` 不同开关 |
| **Go build tags 编译时开关** | 零运行时开销 | 需要重新编译、无法热切换 |

**推荐：** 组合使用 Path-prefix middleware（用于粗粒度） + Handler-level annotation（用于细粒度，通过 `context` 传递 flag，handler 自行查 flag）。

---

### 方向三：迁移/版本化基础设施

**为什么需要：**
- 评审文档指出"零停机迁移"已有系统性文档覆盖（含 EC1-EC4、dry-run、`/readyz` 降级）
- 现实：每次架构变化（存储后端切换、DB schema 迁移、API 版本升级）都需要代码级迁移支持
- 当前缺少**`Migration` 接口**——每次迁移都是手写脚本或文档描述

**核心挑战：**
1. **并行运行**：新旧版本需要在一段时间内共存
2. **验证机制**：dry-run 环境和结果比较逻辑
3. **回滚**：迁移失败后的处理（自动回滚 vs 手动回滚 vs 保持现状）
4. **可观测性**：迁移进度、速率、数据一致性验证

**预期的架构变更：**

```
internal/
  migrate/            ← NEW PACKAGE
    migration.go       # Migration 接口
    registry.go        # 迁移注册中心
    runner.go          # 串行/并行执行器
    dry_run.go         # dry-run 比较器
    progress.go        # 进度追踪（metrics）

  storage/
    migration.go       # NEW — 文件存储迁移器（local ↔ s3）
```

**对现有系统的影响：**
- 不改变现有 repository 和 storage 的接口——迁移器是**上层调度器**
- 每次新迁移 = 实现 `Migration` 接口 + 注册
- 需要确认 `/readyz` 是否能反映迁移状态（迁移中=false，迁移完成=true）

---

### 方向四：SSO 客户端生产化

**为什么需要：**
- 当前 `sso` 包已有 `TokenCache`（TTL-capped）和 `UserCache`（batch），但缺少：
  - **Circuit breaker**——SSO 不可用时优雅降级而非失败
  - **Stale-while-revalidate**——缓存过期后后台刷新
  - **批量解析的 O(1) 缓存复杂度保证**
  - **Healthcheck 集成**——/readyz 应反映 SSO 连接状态
- 评审文档指出旧有一处引用（后台刷新 goroutine）是不准确的——stale 处理是同步的——这需要通过代码级生产化解决

**核心挑战：**
1. **熔断与降级**：阈值设置、半开状态、fallback 行为
2. **缓存一致性**：SSO 那边用户信息变更后，aero-id 怎么知道？
3. **gRPC 长连接管理**：连接池大小、健康检查、重连策略
4. **批量合并**：多个并行请求等待同一批量解析时的请求合并（request coalescing）

**预期的架构变更：**

```
internal/
  sso/
    client.go          # 现有 — gRPC 客户端
    circuit_breaker.go  # NEW — 熔断器
    cache.go           # 现有 — 升级为 st-ale-while-revalidate
    coalescer.go       # NEW — 请求合批器
    health.go          # NEW — 健康检查集成
```

**对现有系统的影响：**
- `sso.Client` 接口需要扩展吗？——如果添加熔断能力，有两种方式：
  - **透明代理**：熔断器包裹在 `Client` 接口外，对 service 层不可见（推荐，零影响）
  - **接口扩展**：在 `Client` 接口添加 `Health() Status` 方法（破坏兼容性，不推荐）

---

### 方向五：API 合约化与 HTTP ↔ gRPC 一致性加固

**为什么需要：**
- 评审文档指出"API 合约化"在多个既有文档中已有系统性覆盖
- 当前架构要求 HTTP 和 gRPC 行为一致，但**没有自动化验证**——只能靠人工 review
- 新增 API 时需要写三份：protobuf 定义、HTTP handler、gRPC handler——目前没有机制确保它们行为一致

**核心挑战：**
1. **行为等价性验证**：HTTP handler 和 gRPC handler 调用同一 service 方法，但错误映射不同怎么办？
2. **合约优先 vs 代码优先**：protobuf 作为单一事实来源，还是 HTTP 路由 + protobuf 双源？
3. **OpenAPI 生成**：从 protobuf 自动生成 OpenAPI 规范？
4. **向后兼容检查**：protobuf 变更是否破坏现有客户端？

**预期的架构变更：**

```
api/
  openapi/            ← NEW
    aero-id.yaml       # 从 proto 自动生成的 OpenAPI 规范（或手动维护）

internal/
  handler/
    http/
      errors.go        # 现状 — 增加 error mapping 测试
    grpc/
      errors.go        # 现状 — 与 http/errors.go 共享同一个映射表

  test/
    contract/          ← NEW
      suite.go         # 同时验证 HTTP 和 gRPC handler 对同一输入返回等价错误
```

**关键决策——合约优先策略：**

| 策略 | 优点 | 缺点 |
|------|------|------|
| **protobuf 为单一事实源** | 一致性最强、自动生成 OpenAPI | gRPC 不原生支持 HTTP 状态码映射、需额外 transformers |
| **OpenAPI 为单一事实源** | REST 生态工具链丰富 | gRPC 需额外适配层、protobuf 和 OpenAPI 双源需同步 |
| **双源 + 合约测试** | 兼容性和灵活性兼顾 | 维护成本高、不一致可能晚于测试发现 |

**推荐：** 短期（P1）采用**合约测试**方式——对每个 handler 编写"HTTP 发出请求 → gRPC 发出请求 → 比较错误码和行为"。长期（P2）评估 protobuf 作为单一源。

---

## 3. 接口设计建议

### 3.1 核心设计原则

基于当前架构的严格分层，接口设计应遵循以下原则：

**原则一：接口属于消费者**
- `repository` 接口由 `service` 定义（已在当前架构中如此）
- `storage` 接口由 `service` 定义
- `sso.Client` 接口由 `service` 定义
- **不要**在定义接口的包中实现它——这是 Go 的 common wisdom，当前架构已遵循

**原则二：Decorator 模式优于接口扩展**
- 当需要为现有接口添加横切关注点（metrics、logging、circuit breaker）时，使用 decorator/wrapper
- 不改变现有接口签名
- 例如：`MetricsFileService` 包裹 `FileService`，实现相同的 `FileService` 接口

**原则三：新增接口应遵循 "最小接口" 原则**
- 每个接口不超过 3 个方法
- 如 `FileService` 超过 5 个方法，应考虑拆分为 `FileCommandService` 和 `FileQueryService`
- 当前 `AGENTS.md` 指出"文件 ≤500 行"——暗示已有拆分意识

### 3.2 需要引入的新抽象层

| 抽象层 | 必要性 | 形式 |
|-------|--------|------|
| **TelemetryProvider** | 🔴 必须 | interface — 封装 metrics 注册、trace provider、logger |
| **LiveConfig[T]** | 🟡 强烈推荐 | generic struct — 类型安全的动态配置值 |
| **Migration** | 🟡 强烈推荐 | interface — `Up(ctx) error` + `Down(ctx) error` + `Verify(ctx) (bool, error)` |
| **CircuitBreaker** | 🟢 推荐 | interface — 包裹 gRPC/HTTP 调用的熔断器 |
| **FeatureFlag** | 🟢 推荐 | 枚举 + middleware，非接口 |

### 3.3 向后兼容策略

1. **接口不删除方法**：只追加（Go 接口的兼容性要求）。如果必须删除，定义新接口并废弃旧接口。
2. **protobuf 字段不删除**：标记为 `reserved` 而非删除。
3. **HTTP API 版本化**：当前 `/v1/` 前缀。如果需要 v2，新建 `internal/handler/http/v2/`，service 层复用。
4. **配置兼容**：`LiveConfig[T]` 默认值与旧配置一致，启动时从静态配置初始化，随后可热变更。

---

## 4. 技术选型

### 4.1 需要引入的新依赖评估

| 库 | 用途 | 评估 | 推荐度 |
|----|------|------|--------|
| `prometheus/client_golang` | Metrics 收集 | 稳定、Go 生态第一选择、Prometheus 是 CNCF 毕业项目 | ⭐ P0 |
| `otel` (go.opentelemetry.io/otel) | Trace 传播 | 仅用于 W3C TraceContext，不要用其 metrics SDK | ⭐ P0 |
| `fsnotify/fsnotify` | 文件变更监听 | 原生跨平台文件 watch、轻量、Go 团队维护 | ⭐ P0 |
| `sony/gobreaker` | 熔断器 | 成熟、已用于生产多年、接口简洁 | ⭐ P1 |
| `getkin/kin-openapi` | OpenAPI 验证 | OpenAPI 标准验证、可集成到合约测试 | 🟡 P2 |

### 4.2 第三方依赖评估标准

对于任何一个新引入的依赖，使用以下核查清单（当前项目缺少）：

```
[ ] 许可证          — 必须是 Apache 2.0、MIT、BSD，排除 AGPL/SSPL
[ ] 维护状态        — 过去 6 个月有 commit，有 tagged release
[ ] 版本稳定性      — 有 v1.x 或至少稳定 beta
[ ] 依赖树大小      — 引入后 go.sum 增加 ≤50 行
[ ] Go 版本兼容性   — 兼容 Go 1.26
[ ] 测试覆盖率      — 作者代码覆盖率 ≥70%，或经过生产验证
[ ] 卸除路径         — 如果不再需要，删除依赖的难度（替换成本）
```

### 4.3 自建 vs 采购决策依据

本项目的上下文（Go 微服务、SSO 外部、文件存储、消息/评论系统）决定了**我们处于构建层，不是中间件层**：

| 组件 | 决策 | 理由 |
|------|------|------|
| Metrics 收集 | **采用 Prometheus 库** | 标准件，不自己发明 |
| 熔断器 | **采用 gobreaker** | 算法复杂（状态机、滑动窗口），自建容易出 bug |
| 特性开关存储 | **自建** | 业务逻辑简单（CRUD + middleware），与现有 repository 模式集成 |
| 配置热重载 | **半自建** | `fsnotify` 做文件监听 + 自建类型安全的 `LiveConfig[T]` |
| 迁移编排 | **自建** | 业务特定（存储迁移、DB 迁移、API 版本迁移），没有现成库能理解 aero-id 的领域 |

---

## 5. 实施路线图

### 5.1 优先级分类

```
P0 — 不可拖延、有其他任务的前提依赖、涉及安全/稳定性
P1 — 高价值但可以短期没有、需要在 P0 稳定后开始
P2 — 锦上添花、可以无限期推迟
```

### 5.2 阶段划分与里程碑

#### 阶段 0：基础建设（P0）—— 2-3 周

**目标：建立"看得清、控得住"的能力**

| 任务 | 工作量 | 依赖 | 产出物 |
|------|--------|------|--------|
| **0.1 telemetry 包 + metrics decorators** | 1 周 | 无 | FileService/MessageService 的 P99 延迟、错误率可观测 |
| **0.2 W3C TraceContext 传播** | 0.5 周 | 0.1 | request → SSO gRPC → DB 全链路 trace |
| **0.3 LiveConfig[T] 框架** | 0.5 周 | 无 | 至少支持 `int`、`bool`、`time.Duration`、`[]string` |
| **0.4 熔断器（SSO 客户端）** | 0.5 周 | 无 | SSO 不可用时 /readyz 降级 + 熔断 |

**里程碑 M0：** `make check-file-sizes && make lint && make test` 全绿 + 以下可观测：
- Grafana dashboard 显示各 service 方法的 P50/P90/P99
- `curl localhost:8080/readyz` 反映 SSO 连接状态

**风险：** 代码最长——如果 `service` 层文件接近 500 行限制，需要在阶段 0 先拆分。

#### 阶段 1：动态能力（P0/P1）—— 2-3 周

**目标：可动态调整、可灰度验证**

| 任务 | 工作量 | 依赖 | 产出物 |
|------|--------|------|--------|
| **1.1 特性开关表 + middleware** | 1 周 | 0.3 | admin API 控制开关、HTTP 中间件注入 context |
| **1.2 从静态配置迁移至 LiveConfig** | 0.5 周 | 0.3 | 限流阈值、日志级别可热调整 |
| **1.3 合约测试框架** | 1 周 | 无 | HTTP ↔ gRPC 错误映射一致性自动验证 |
| **1.4 SSO 请求合并器** | 0.5 周 | 0.4 | `GetUsers([]uid)` 并发请求合并 |

**里程碑 M1：** 特性开关已在 Pre-Prod 运行 + 合约测试作为 CI 门禁

**风险：** `feature` 包是否要引入 DB 读写？如果特性开关存储需要 DB，则引入 `feature` 对 `repository` 的依赖——这是合理的吗？

**处理：** `feature` 包直接依赖 `repository`（因为 `feature` 本质上是业务配置，不是基础设施）。这与 `service → repository` 的依赖方向一致。

#### 阶段 2：架构弹性（P1）—— 3-4 周

**目标：零停机迁移能力、存储层灵活切换**

| 任务 | 工作量 | 依赖 | 产出物 |
|------|--------|------|------|
| **2.1 Migration 接口 + runner** | 1 周 | 0.1 | 可执行、可回滚、可观察的迁移 |
| **2.2 存储迁移器（local ↔ s3）** | 1.5 周 | 2.1 | 文件在存储后端之间安全迁移 |
| **2.3 /readyz 集成迁移状态** | 0.5 周 | 2.1 | 迁移中 /readyz 返回 503 |

**里程碑 M2：** 存储迁移已在测试环境验证 + 回滚测试通过

**风险：** 迁移中的数据一致性验证——怎么知道迁移后的文件与源文件完全一致？

**缓解：** `Migration.Verify()` 方法检查 `checksum`（sha256）+ `count` + `size`。dry-run 模式只做校验不做实际拷贝。

#### 阶段 3：API 合约化（P2）—— 2-3 周

**目标：protobuf 为单一事实源、自动化向后兼容检测**

| 任务 | 工作量 | 依赖 | 产出物 |
|------|--------|------|------|
| **3.1 OpenAPI 生成流水线** | 1 周 | 无 | `make openapi` → `api/openapi/aero-id.yaml` |
| **3.2 protobuf 向后兼容检查** | 0.5 周 | 3.1 | CI 中检测 breaking changes |
| **3.3 HTTP ↔ gRPC 错误映射自动化验证** | 1 周 | 1.3 扩展 | 每个 handler 的错误映射测试 |

**里程碑 M3：** OpenAPI 规范 CI 生成 + 合约测试覆盖所有现有 handler

---

### 5.3 依赖图

```
阶段 0         阶段 1         阶段 2         阶段 3
──────        ──────        ──────        ──────
Telemetry ──→  ──→ 合约测试 ←────────────── 错误映射自动化
  │                                        
  ├─→ TraceContext                     
  │                                        
LiveConfig ──→ 特性开关                    
  │                                        
  └─→ 熔断器 ──→ SSO 合并器                 
                                            
                      Migration ──→ 存储迁移器
                          │                   
                          └─→ /readyz 集成    
                                              
                              protobuf → OpenAPI 生成
```

### 5.4 风险点与缓解策略汇总

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| Metrics 埋点增加显著延迟 | 低 | 高 | 使用 `promhttp.Histogram` 的分桶配置、异步收集、benchmark 验证 |
| LiveConfig 热重载导致不一致 | 中 | 高 | `sync.Map` + 版本号 + 重载前验证 + 原子切换 |
| 特性开关存储与 repository 耦合 | 中 | 低 | `feature.Store` 接口可被 mock，测试不影响 |
| 存储迁移数据校验不一致 | 中 | 高 | SHA256 checksum + dry-run + 迁移完成后的 full audit |
| SSO 熔断器误开 | 低 | 中 | 合理阈值（错误率 >50% 在 10s 窗口）+ 半开状态定期探测 |
| 合约测试维护成本 | 高 | 中 | 只测关键路径（CRUD + 认证 + 错误映射），不追求全覆盖 |
| 文件超过 500 行约束 | 中 | 高 | 每个阶段开始前先检查最长文件，必要时先拆分再实施 |
