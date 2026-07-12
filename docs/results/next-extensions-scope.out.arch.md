现在我已拥有全面信息。以下是完整的架构分析。

---

# aero-id 架构分析报告

> **分析范围**: 基于代码库实际状态（`main` 分支）、设计文档、ROADMAP 和最新实施计划
> **分析日期**: 2026-07-12
> **方法**: 代码走读 + 包依赖分析 + 接口层次审计

---

## 1. 架构评估

### 1.1 当前架构核心优势

| 维度 | 现状 | 评价 |
|------|------|------|
| **分层** | handler → service → repository，严格单向 | ✅ 典范级实现 |
| **双传输** | HTTP(Gin) + gRPC 共享 service 层 | ✅ 避免逻辑重复，ADR-001 决策合理 |
| **SSO 韧性** | 断路器 + TokenCache + UserCache + stale-while-revalidate + singleflight + 重试 | ✅ 业界最佳实践的融合 |
| **事务性 Outbox** | 域事件与数据在同一 txn 提交，relay 异步分发 | ✅ 保证最终一致性，ADR-003 |
| **存储抽象** | `FileStorage` 接口 + local/S3 后端 | ✅ 策略模式标准用法 |
| **领域模型** | `internal/domain/` 12 个文件，纯实体 + 值对象 | ✅ 贫血模型规避得当 |
| **错误映射** | `AppError` → HTTP status + gRPC code 统一转换 | ✅ 服务层不沾染传输细节 |
| **可观测性** | RED metrics + OTel tracing + per-dependency health + access log | ✅ 已覆盖 ROADMAP P0 方向 |

### 1.2 关键架构发现

#### 🔴 发现一：双包体系——最严重的技术债

代码库中存在**两套平行的代码组织**：

```
架构 A（已接入，生产中）               架构 B（未接入，孤立代码）
────────────────────────              ──────────────────────
internal/service/                     internal/file/      (service_crud.go, service_share.go, ...)
internal/handler/http/                internal/message/   (service.go, handler_http.go, ...)
internal/handler/grpc/                internal/comment/   (service.go, handler_http.go, ...)
internal/repository/                  internal/notification/
                                      internal/block/
                                      internal/search/    (整个 package 是死代码)
```

**证据链**：

1. `internal/app/app.go` 的 wiring 仅使用架构 A：
   ```go
   // app.go line 257-260 — 使用 centralized 版本
   searchSvc := service.NewSearchService(msgRepo, commentRepo, fileRepo)
   httpapi.NewSearchHandler(searchSvc)       // internal/handler/http/search.go
   grpcsrv.NewSearchServer(searchSvc)        // internal/handler/grpc/search.go
   ```

2. 架构 B 的 `internal/search/search.go` 定义了自己的 `NewService()`，但**唯一调用者在 `internal/search/service_test.go` 中**，没有任何生产代码引用。

3. `internal/file/file.go` 定义了 `Repository` 接口（16 个方法），但 `internal/app/app.go` 使用的是 `repository.FileRepository`（来自 `internal/repository/file.go`）——两者接口签名不同。

4. `internal/search/handler_grpc.go` 引用了 `file.Repository` 和 `message.MessageRepository` 的域包接口版本，但这些 handler 本身从未被注册。

**影响**：
- ~1800 行代码（含测试）永远不会在生产路径执行
- 新开发者难以判断"应该在哪里加新路由/新方法"
- 维护负担：两个版本需要同步更新
- 编译通过但逻辑死寂——比编译错误更危险

**根因**：可能是从 centralized 架构向 domain-package 架构迁移**半途而废**，或反之（centralized 版本是重构产物，但旧包未清理）。

---

#### 🟡 发现二：`internal/app/app.go` 违反 500 行硬约束

```
internal/app/app.go: 516 行 > 500 ✅ 触发 AGENTS.md 的 Refactoring Triggers
```

文件过大导致：
- `New()` 函数超过单文件建议复杂度——它同时负责：DB 连接、存储初始化、tracing 启动、SSO 客户端、所有 Repo 构造、所有 Service 构造、Router/Server 构造、后台 goroutine 启动
- 可测试性差：无法轻松 mock 部分依赖
- 违反了 AGENTS.md 明确声明的 engineering constraint

#### 🟢 发现三：`internal/service/mocks/mocks.go` 586 行

虽然是测试 mock 文件，但 586 行的单个文件通常意味着：
- mock 未按领域拆分
- 可以考虑使用 `go.uber.org/mock`（原 `gomock`）或 `moq` 生成器替代手写 mock

---

#### 🟡 发现四：Outbox Relay 无优雅 Drain（ROADMAP P0-4 仍未修复）

`internal/outbox/relay.go` 的 `Start(pollInterval)` 返回的 stop 函数仅停止 ticker，不执行最后一次 drain。快速重启（滚动更新、CrashLoopBackOff）可能导致：

- 已提交 txn 的 outbox 事件在未来 5s 内未发布
- 通知延迟或丢失（取决于下游消费幂等性）

已在 ROADMAP 标记为 P0-4，但实现计划 (`docs/imp plan/2026-07-12`) 未包含此修复。

#### 🟢 发现五：配置热更新不存在

所有配置（限流 RPS、日志级别、追踪采样率）在启动时固定。生产环境需要重启来调整限流阈值或采样率——这是合理的简化（YAGNI in design doc），但值得记录。

---

### 1.3 关键设计决策评估

| ADR | 决策 | 评价 |
|-----|------|------|
| ADR-001 | 双传输共享 service | ✅ 正确：已验证在 41 个 gRPC RPC + HTTP 路由上工作 |
| ADR-002 | SSO 为唯一认证源 | ✅ 正确：aero-id 不签发 token 缩小攻击面 |
| ADR-003 | 事务性 Outbox | ✅ 正确：notification 可靠性核心保障 |
| ADR-004 | 断路器仅对可用性错误触发 | ✅ 正确：UNAUTHENTICATED 不熔断避免误判 |
| ADR-005 | 方向性阻塞（单向） | ✅ 合理：避免双向阻塞的过度约束 |
| ADR-006 | Autonomous Software Factory | ⚠️ 操作策略，非技术架构决策 |

缺失的 ADR：关于**包结构选择**（centralized vs. domain-package）无任何 ADR 记录。这是当前债务的根源。

---

## 2. 扩展方向

### 方向 A：包架构统一与代码清理（P0）

**为什么需要**：
消除"两套代码跑在同一个二进制"的局面。这是所有后续扩展的前提——在混乱的结构上加功能只会加速熵增。

**核心挑战**：
- 确认哪个版本是"源 truth"——建议保留 centralized 版本（已接入），废弃 domain-package 版本
- 需要处理 `internal/search/` 中对 domain-package 接口的引用——将其改为引用 centralized 接口
- 测试需要跟随迁移

**预期的架构变更**：
```
删除（优先）或归档：
  internal/file/          → 其 Repository 接口并入 centralized repo
  internal/message/       → 同上
  internal/comment/       → 同上
  internal/notification/  → 同上
  internal/search/        → 整个包移除（全死代码）
  internal/block/         → 同上评估

保留并明确职责：
  internal/service/       → 唯一业务逻辑层
  internal/handler/http/  → 唯一 HTTP 传输层
  internal/handler/grpc/  → 唯一 gRPC 传输层
  internal/repository/    → 唯一持久化层
```

**对现有系统的影响**：
- 编译期安全——删除死代码不会影响运行时行为
- 测试套件行数减少 ~15%
- CI 速度提升（少编译 ~1800 行）

---

### 方向 B：`app.go` 拆分与依赖注入容器化（P0）

**为什么需要**：
516 行的 `New()` 函数是**违反工程约束**的技术债，同时也是可测试性瓶颈。

**核心挑战**：
- 需要在不改变组件接口的前提下拆分
- 保持优雅关停顺序正确

**选项分析**：

| 选项 | 方案 | 优点 | 缺点 |
|------|------|------|------|
| **B1** | 按逻辑职责拆为多个 builder：`buildDatabase()`, `buildSSO()`, `buildServices()`, `buildTransport()` | 低侵入，无新依赖，AGENTS.md safe | 仍然是手写 DI |
| **B2** | 引入 `uber-go/fx` 注入框架 | 自动构建图，生命周期管理 | 新依赖、学习曲线、AGENTS.md 未涉及 |
| **B3** | 保持现状 + 加 `//nolint:funlen` | 零成本 | 不解决根本问题 |

**推荐 B1**：先拆为函数级 builder，保留 future 迁移到 fx 的可能。

**预期变更**：
```go
// app.go → 拆分为
// app.go          — Run/Shutdown orchestrator (~80 行)
// app_database.go — buildDatabase, buildRepositories (~120 行)
// app_sso.go      — buildSSOClient (~80 行)
// app_service.go  — buildServices (~100 行)
// app_transport.go— buildHTTPServer, buildGRPCServer (~80 行)
```

---

### 方向 C：分布式缓存与有状态水平扩展（P1）

**为什么需要**：
当前限流器、TokenCache、UserCache 均为进程内内存。水平扩展（多副本）时：
- 限流状态每副本独立——用户可能被一个副本限流后切换到另一个副本获得全额配额
- SSO 令牌缓存每副本独立——冷启动时每个副本都穿透到 SSO
- 无法做全局性的请求速率控制

**核心挑战**：
- 引入外部缓存依赖（Redis 或 ValKey）增加运维复杂度
- 缓存失效和一致性协议
- 限流跨副本协调的精确性（精确 vs 近似）

**架构变更**：
```
当前：                       引入 Redis 后：
TokenCache (内存) ─── SSO    TokenCache (Redis) ← SSO
UserCache  (内存) ─── SSO    UserCache  (Redis) ← SSO
Limiter    (内存) ─── req    Limiter    (Redis Sliding Window) ← req
                             ↓
                            Redis Cluster
```

**设计决策**：
- **选项 C1（全量 Redis）**：TokenCache + UserCache + Limiter 全部搬到 Redis。优点是统一缓存层；缺点是引入 Redis 依赖且缓存序列化开销。
- **选项 C2（仅限流 Redis）**：TokenCache 保持本地内存（已有 singleflight + stale 容错），仅限流搬到 Redis 实现全局配额。优点是运维简单；缺点是 SSO 缓存重复穿透问题在多个副本下加剧。
- **选项 C3（本地 + Pub/Sub 通知）**：限流和缓存保持本地，使用 Redis Pub/Sub 或 gossip 协议在缓存失效时广播通知。复杂度高，不推荐。

**推荐 C2**（渐进式）：限流搬到 Redis，缓存仍本地但加预热机制。后续可按需迁移缓存。

---

### 方向 D：形式化降级契约（P1，已经在实施计划中）

**为什么需要**：
目前降级行为是隐式的——SSO 断路器打开后，请求在 service 层返回 `ErrServiceUnavailable`，但：
- 没有统一的状态报告（客户端不知系统处于降级状态）
- 没有区分解读/写操作的降级策略
- 没有降级行为文档

**核心挑战**：
- 降级探测本身不能成为新的故障点（R6 in 实施计划）
- 16 个 handler（8 HTTP + 8 gRPC）需要逐个适配
- 降级状态需要跨副本一致（或近似）

**架构变更**：
```
新增：
  internal/middleware/degradation.go    — DegradationState 管理器 + 中间件
  docs/DEGRADATION_MATRIX.md           — 降级行为矩阵

修改：
  所有 handler → 根据降级状态决定：503/429/200(stale)/正常
```

**设计决策**：
- **探测 vs 推断**：建议使用探测（主动发起健康检查）而非推断（从最近错误率推断），因为更精确
- **严格 vs 宽松**：建议宽松模式——探测失败标记降级但不拒绝请求，除非连续 N 次失败

---

### 方向 E：数据生命周期自动化（P1，已在计划中）

**方向 F**：实时通知优化（SSE/WebSocket——P2，已在 ROADMAP 中）

---

## 3. 接口设计建议

### 3.1 当前接口体系的问题

| 问题 | 表现 | 影响 |
|------|------|------|
| **重复接口** | `internal/file/file.go` 定义 `Repository`，`internal/repository/file.go` 定义 `FileRepository` | 两套接口签名不同，新人选择困难 |
| **接口粒度不一** | `internal/repository/message.go` 的 `MessageRepository` 有 13 个方法；`internal/repository/file.go` 的 `FileRepository` 也有 10+ 方法 | 违反接口分离原则（ISP） |
| **对外依赖** | `internal/search/search.go` 依赖 `file.Repository`, `message.MessageRepository`（域包版本） | 死代码的依赖链 |

### 3.2 建议原则

1. **单一仓库接口**：`internal/repository/<domain>.go` 是唯一的 Repository 接口定义。所有消费者（service、search、handler）依赖此接口。
2. **接口作为消费者契约**：接口定义在消费方包（如 `internal/service/`）而非实现方包。当前 `internal/service/service.go` 中定义的 `FileService`, `MessageService` 等接口——这是正确的模式。
3. **复用已有抽象层**：`UserDirectory`（取 SSO 用户）、`AuditLogger`（审计日志）已经在 `internal/service/service.go` 中恰当定义。新功能遵循此模式。

### 3.3 推荐接口重构

```
清理前：                              清理后：
internal/repository/file.go (接口)     internal/repository/file.go (唯一接口)
internal/file/file.go     (接口)       └→ 删除，引用 centralized 版本
internal/file/repository.go (实现)     └→ 删除，使用 centralized 实现
internal/repository/file.go (实现)     internal/repository/file.go (唯一实现)
```

### 3.4 向后兼容策略

对于涉及包移动的变更：
1. 保留旧包中的类型别名指向新位置（`type File = domain.File` 模式）
2. 一个 PR 内完成：创建新接口 → 替换所有消费者引用 → 删除旧包
3. 不跨越多个 PR 完成——否则中间态存在"两种方式都行"的困惑期

---

## 4. 技术选型

### 4.1 当前依赖评估

| 依赖 | 用途 | 评估 |
|------|------|------|
| `gin-gonic/gin` | HTTP 路由 | ✅ 合理，但已显重型——如果只做 REST，可考虑 `chi` 或标准库 `net/http` + `servemux`（Go 1.22+） |
| `gorm.io/gorm` | ORM | ⚠️ 选型合理但存在争议：隐式迁移、N+1 风险、连接池管理。当前使用正确（无 `AutoMigrate` 在生产环境） |
| `spf13/viper` | 配置 | ✅ 合理，env override 模式 |
| `uber-go/zap` | 日志 | ✅ 合理 |
| `google.golang.org/grpc` | gRPC | ✅ 必要 |
| `go.opentelemetry.io/otel` | 追踪 | ✅ 合理，但当前版本需确认是否支持 `v1.30+`（实施计划 R3 提及） |
| `testcontainers-go` | 集成测试 | ✅ 合理 |

### 4.2 是否需要引入新栈

| 候选技术 | 价值 | 风险 | 建议 |
|----------|------|------|------|
| **Redis/ValKey** | 分布式限流 + 缓存共享 | 运维复杂性 + 序列化开销 | **P1 考虑**：从限流开始，缓存后续 |
| **`uber-go/fx`** | DI 容器化 | 学习曲线 + 调试困难 | **暂不推荐**：先拆 `app.go` |
| **`go.uber.org/mock`** | 自动生成 mock | 生成步骤 + CI 集成 | **推荐**：替代手写 586 行 mock |
| **`golang-migrate/migrate`**（已在用） | 迁移 | 已使用 | ✅ 继续使用 |
| **`k6` 或 `wrk`** | 负载测试 | 新工具链 | **P2 推荐**：ROADMAP 已标记 |
| **`git-cliff`** | CHANGELOG 自动化 | 与现有 `docs/CHANGELOG.md` 兼容 | ✅ 实施计划已规划 |

### 4.3 自建 vs 采购决策框架

对于 aero-id 的上下文，建议遵循：

```
                       是否核心差异化能力？
                       /               \
                      是                否
                     /                   \
       团队是否有能力维护？        是否有成熟的 OSS 方案？
       /           \               /           \
      是            否            是            否
     /               \           /               \
  自建            采购/PaaS    用 OSS         再评估需求
```

**应用实例**：
- **通知推送**：如果只是 SSE 推送给在线连接，自建合理（当前 `internal/hub/` 已实现）。如果是跨平台推送（APNS/FCM），应采购或集成第三方。
- **全文搜索**：PG tsvector + GIN 索引已实现。如果未来需要高精度搜索，考虑 `Meilisearch` 或 `Typesense`（OSS，比 Elasticsearch 轻量）。
- **文件处理**：图片缩略图、视频转码——不应在 aero-id 内实现，应通过 outbox 事件触发的下游服务处理。

---

## 5. 实施路线图

### 5.1 优先级排序

| 优先级 | 方向 | 代码参考 | 价值主张 | 预估工期 |
|--------|------|---------|---------|---------|
| **P0** | **A: 包架构统一** | `internal/file/`, `internal/message/`, `internal/comment/`, `internal/notification/`, `internal/block/`, `internal/search/` | 消除债务，清理死代码，新人入职速度 | 2-3 天 |
| **P0** | **B: `app.go` 拆分** | `internal/app/app.go:516行` | 恢复工程约束合规，提升可测试性 | 1 天 |
| **P0** | **传输协议加固**（实施计划方向四） | `internal/handler/grpc/server.go` | 修复已知 DoS 向量 | 2 天 |
| **P1** | **降级契约文档**（实施计划 TASK-010） | 新建 `docs/DEGRADATION_MATRIX.md` | 设计文档地基 | 0.5 天 |
| **P1** | **数据生命周期**（实施计划方向五） | `audit_events`, `notifications`, `outbox_events` | 控制存储无限增长 | 2 天 |
| **P1** | **发布工程**（实施计划方向一） | `.goreleaser.yaml`, GitHub Actions | CI/CD 自动化 | 2-3 天 |
| **P2** | **分布式限流（Redis）** | `internal/pkg/ratelimit/` | 水平扩展基石 | 3 天 |
| **P2** | **降级契约代码**（实施计划 TASK-011~014） | 所有 handler | 生产韧性 | 5-7 天 |
| **P2** | **可观测性成本优化**（实施计划方向二） | 采样、追踪、日志聚合 | 成本控制 | 2-3 天 |
| **P2** | **Outbox 优雅 Drain** | `internal/outbox/relay.go` | 滚动更新时的数据安全 | 0.5 天 |

### 5.2 阶段划分

#### 阶段 1：清理与安全（Sprint 1，第 1-5 天）

```
Day 1-3:  方向 A（包清理）+ 方向 B（app.go 拆分）
Day 4-5:  传输协议加固（方向四，实施计划 TASK-015~021）
```

**里程碑 M1**：`make test && make lint` 通过，死代码清零，`app.go` ≤ 300 行

#### 阶段 2：基础设施稳固（Sprint 2，第 6-12 天）

```
Day 6-7:   降级矩阵文档 + Outbox 优雅 Drain
Day 8-9:   数据生命周期清洁器（方向五，TASK-022~025）
Day 10-12: 发布工程（方向一，TASK-001~005）
```

**里程碑 M2**：Docker CI 运行 + 数据清洁器上线运行

#### 阶段 3：生产韧性（Sprint 3，第 13-20 天）

```
Day 13-15: 分布式限流（Redis 集成）
Day 16-20: 降级契约代码（TASK-011~014）
```

**里程碑 M3**：多副本部署时限流一致 + 所有 handler 具有形式化降级行为

#### 阶段 4：成本优化（Sprint 4，第 21-26 天）

```
Day 21-23: 可观测性成本优化（方向二，TASK-006~009）
Day 24-26: 可并行处理：测试覆盖率补全 + 负载测试脚本
```

### 5.3 风险点与缓解策略

| # | 风险 | 概率 | 影响 | 缓解措施 |
|---|------|------|------|---------|
| R1 | 包清理误删正在使用的代码 | L | H | 先用 `go build ./...` + `go vet ./...` + `make test` 三重验证；逐步删除而非一次性 PR |
| R2 | `app.go` 拆分破坏关停顺序 | L | M | 拆分后编写集成测试验证关停顺序；优雅关停顺序在 `shutdown()` 中有 explicit ordering |
| R3 | Redis 引入增加运维复杂度 | M | M | 先仅用于限流（可选依赖，不存在时降级为本地限流）；缓存保持本地内存 |
| R4 | 降级契约实现成本低估 | H | H | 先核心 handler（文件+消息），后续迭代边缘 handler；使用包装器模式减少重复代码 |
| R5 | GitHub Actions + Goreleaser 配置调试时间超预期 | M | L | 使用 `goreleaser build --snapshot` 本地验证，不依赖 CI 调试 |

### 5.4 不做事项（明确 Out of Scope）

- **微服务化拆分**：aero-id 目前 21 个包 ~28000 行 Go，单体化合理。拆分微服务需要等业务增长 10x 后再评估。
- **gRPC 到 REST 的自动转码**（grpc-gateway）：当前双传输是手工维护的。grpc-gateway 可减少重复，但引入 proto annotation 侵入性大，且与现有 HTTP handler 不兼容。建议维持现状。
- **配置热更新**：viper 支持 `WatchConfig`，但 aero-id 的组件在构造时注入，热更新需要重新 wiring——复杂度太高，YAGNI。
- **多租户/组织管理**：业务上无需求，架构上会要求所有表加 `org_id` 列。

---

## 总结

| 发现类别 | 严重程度 | 建议行动 |
|---------|---------|---------|
| 双包体系（~1800 行死代码） | 🔴 Critical | Sprint 1 清理 |
| `app.go` 516 行违反约束 | 🟡 High | Sprint 1 拆分 |
| Outbox relay 无优雅 drain | 🟡 High | Sprint 2 修复 |
| SSO 缓存跨副本不共享 | 🟡 Medium | Sprint 3（P2）引入 Redis |
| 无降级契约文档 | 🟢 Low | Sprint 2 创建 |
| 无负载测试工具 | 🟢 Low | Sprint 4（P2）引入 k6 |

**最优先行动**：本周内完成**包架构统一**（方向 A）+ **`app.go` 拆分**（方向 B）。这是所有后续工作的地基——在结构清晰的代码上做降级契约、数据生命周期、分布式缓存，才能保证可维护性不急剧恶化。
