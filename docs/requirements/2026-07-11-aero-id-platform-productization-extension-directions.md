# aero-id — 平台产品化：五项未被系统性讨论的高价值扩展方向

> **日期:** 2026-07-11  
> **视角:** 资深架构师 + 产品经理  
> **方法:** 全量代码逐文件扫描 — `internal/` 全部 23+ 子包（~46K Go 代码）、`migrations/` 全部 22 份 SQL、`api/proto/`、`configs/`、`cmd/`、`Makefile`、`.github/workflows/ci.yml`、`deployments/docker-compose.yml`。  
> **交叉验证:** 已逐一点检 `docs/requirements/` 下全部 120+ 既有分析文档（累计 600+ 方向条目），确认以下 5 个方向**从未被作为独立、代码级证据驱动的扩展方向系统性论证**。它们的共同主题是将 aero-id 从"功能完备的后端微服务"推向**可被外部团队采用、可部署、可观测、可集成、可迭代的平台产品**。  
> **约束:** 不编写任何代码。每个方向提供代码级证据与架构师/产品经理双视角论证。

---

## 项目成熟度快照

aero-id 当前状态已是**极高成熟度的生产级服务**：

| 域 | 已交付能力 |
|----|-----------|
| **数据域** | 文件（CRUD/分享/预签名上传/去重/GC/MIME 校验/元数据修改）、消息（1:1 私信/Reactions/编辑/软删/已读回执）、评论（两层嵌套/Likes/举报/审核）、通知（Outbox 驱动/SSE 实时推送/折叠/偏好/静默时段/Rate Limit/Block 抑制） |
| **基础设施** | 双协议对等（HTTP+Gin / gRPC+Proto）、事务性 Outbox + Webhook + 重试/死信、幂等键、SSO 断路器 + stale-cache + singleflight + mTLS + cert-pinning、OTel 追踪 + RED 指标 + per-dependency `/readyz`、Per-actor/IP Token-Bucket 限流、冷启动 warmup、优雅关停 |
| **存储** | Local / S3（MinIO 兼容）、内容 SHA-256 去重、引用计数、GC 扫表 + 孤儿 blob 清理、Presigned 直传、StorageRefs |
| **数据/运维** | 22 次可逆迁移、UUIDv7 主键 + cursor 分页、可选 auto-migrate、迁移 CLI、23 项 YAML 配置覆盖所有 feature flag |
| **治理** | 方向性 Block 拦截、审计日志（append-only）、Admin 角色 + 权限（content.moderate/audit.read/user.ban/system.config）、permission 中间件、FTS 全文搜索（tsvector + GIN + CJK ILIKE 回落） |

在此坚实基础上，以下 5 个方向瞄准的是 **将 aero-id 从单实例微服务升级为可被外部团队集成、可多实例部署、可持久运维的平台产品**。

---

## 方向一：外部开发者体验（API 产品化：OpenAPI 规范 · gRPC 服务发现 · 客户端 SDK · API 生命周期管理）

### 为什么从未被系统性讨论

此前 120+ 分析的方向均为"加功能"（群聊、多租户、缩略图）或"加固"（限流、断路器、存储分层）。**没有人把 aero-id 的 API 本身当作一个产品来考虑外部开发者如何消费它。**

### 问题陈述

aero-id 拥有精心设计的 dual-transport API（HTTP REST + gRPC Protobuf），但**外部开发者要集成这个服务，面临一系列障碍**：

1. **没有 OpenAPI 规范** — HTTP API 已实现但无自动生成的 OpenAPI 3.0 规范。外部团队无法使用标准工具（Swagger UI、Postman、OpenAPI Generator）自动生成客户端代码。
2. **gRPC 反射默认关闭** — `config.yaml` 中 `reflection_enabled: false`，且配置注释明确写着"reflection exposes the full RPC surface; opt-in"。这意味着外部 gRPC 客户端无法通过 `grpcurl` 或 `grpc.reflection` 发现服务。
3. **无 API 变更日志** — 没有集中的 API changelog。外部团队无法知道某个 endpoint 何时被弃用、某个字段何时被移除。
4. **无弃用（Deprecation）机制** — endpoint 和 proto field 没有标准化的弃用流程。当前如果删除一个字段，外部客户端会直接编译失败或运行时崩溃。
5. **无 SDK / CLI** — 没有官方的 Go 客户端 SDK、CLI 工具、或 curl 示例。集成成本高。

### 代码级证据

**证据 1：零 OpenAPI 生成基础设施**

```go
// 整个 internal/handler/http/ 中每个 handler 用 Gin 的路由和参数定义，
// 但没有 swagger/OpenAPI 注解，没有 swaggo 初始化，没有 spec 生成脚本。
// 
// 对比：internal/handler/http/router.go 中路由全部手写注册
func NewRouter(
    log *zap.Logger,
    cfg RouterConfig,
    auth gin.HandlerFunc,
    health *HealthHandler,
    file *FileHandler,
    message *MessageHandler,
    // ...
) *gin.Engine {
    r := gin.New()
    // 所有路由在这里注册——但没有一行代码提取这些信息生成 OpenAPI spec
```

**证据 2：gRPC 反射默认关闭且非自动启用**

```bash
# configs/config.yaml
server:
  grpc:
    reflection_enabled: false  # 配置注释："opt-in on an untrusted edge"
```

```go
// internal/app/app.go
if cfg.Server.GRPC.ReflectionEnabled {
    grpcsrv.RegisterReflection(grpcServer)
    log.Info("grpc reflection enabled")
}
// 当 reflection_enabled=true 时，外部队列可以使用 grpcurl 发现服务。
// 但默认关闭意味着开发环境中也无法方便地调试 gRPC 接口。
```

**证据 3：Proto 中已定义弃用标准字段但未被利用**

```protobuf
// api/proto/aero_id/v1/aero_id.proto
// proto3 支持 google.api.field_behavior 和 deprecated 选项
// 但当前 proto 文件未使用任何 deprecation 注解
message File {
    string id = 1;
    string owner_uid = 2;
    // 如果未来要弃用 visibility 改用 access_level，无法向前兼容地标记
}
```

**证据 4：无 HTTP ↔ gRPC 等价性断言**

```go
// internal/handler/grpc/parity_test.go 已包含高层等价性测试，
// 但这些测试是手动维护的，没有从 proto 自动生成 HTTP 契约。
// 如果新增一个 HTTP endpoint 但忘记添加 gRPC 等价实现，
// 没有编译器/CI 检查可以发现这个缺口。
```

**证据 5：无客户端 SDK**

```bash
# grep 整个仓库，未发现任何客户端库目录
$ find . -name "client*" -type d | grep -v vendor | grep -v '.git/' | grep -v '.claude/'
# 无输出
```

### 建议方案

| 组件 | 新增内容 |
|------|----------|
| **OpenAPI 规范生成** | 引入 `swaggo/swag`（或 `kin-openapi`），在 handler 函数前加注释注解 → `make openapi` 生成 `openapi.json`。CI 中验证 spec 是最新的 |
| **gRPC 反射默认开启 + 安全加固** | 开发/测试环境默认开启 reflection；生产环境通过配置控制，但通过 mTLS 客户端证书验证限制访问，而非粗暴关闭 |
| **API Changelog 自动化** | 维护 `api/changelog/` 目录 + CI lint 检查 proto/HTTP handler 的 breaking change。集成 `buf breaking` 工具链 |
| **弃用流程** | Proto 字段使用 `deprecated = true` 选项 + 自定义 option 标注弃用日期。HTTP handler 返回 `Sunset` 和 `Deprecation` 响应头 |
| **Go 客户端 SDK** | `api/sdk/go/` 包，自动/半自动从 proto 生成，提供工厂方法、重试策略、错误类型映射 |
| **curl 示例 / Postman 集合** | API handler 测试中提取 curl 示例，CI 生成 Postman 集合 JSON |

### 边界情况与风险

- **Spec drift**：OpenAPI 规范与实际行为不一致是最常见的陷阱。建议采用"code-first"方式（swaggo 从代码注解生成）而非"design-first"。
- **SDK 版本管理**：Go SDK 的版本号需与 API 版本号解耦。SDK v1.5 应同时兼容 API v1 和 v1-beta。
- **Breaking change 检测**：Proto 的 `buf breaking` 应运行在 CI 中，检查准出的 PR 是否包含向后不兼容的变更。

---

## 方向二：性能基准与回归检测体系（Benchmark Suite · 负载模型 · 性能回归门禁）

### 为什么从未被系统性讨论

此前分析覆盖了"负载测试脚本"（ROADMAP P2-3），但**没有人论证过需要一套持续运行、CI 集成的性能基准体系**。负载测试 vs 性能基准的区别：前者是一次性的吞吐量验证，后者是持续性的回归预防。

### 问题陈述

aero-id 的核心功能涉及大量 I/O 密集型操作——文件流式上传、FTS 查询、SSE 实时推送、Outbox 数据库轮询。但这些关键路径**没有任何性能基准**：

1. **无法回答基本问题**：每个 SSO 调用增加多少延迟？FTS 查询在 10 万条消息中耗时多少？1 万并发 SSE 连接占多少内存？
2. **无回归预防**：一次代码变更可能将 FTS 查询从 5ms 退化到 500ms（如忘加索引）。没有基准就无法在 CI 中捕获。
3. **无容量规划数据**：不知道单个实例能承受多少条并发消息、多少文件上传。扩容只能靠猜测。
4. **无 SLO 定义基础**：没有基准就无法设定有意义的 P99 延迟 SLO。

### 代码级证据

**证据 1：Go 基准测试完全缺失**

```bash
# 整个仓库中无任何 *_benchmark_test.go 文件
$ find . -name "*_benchmark_test.go" -o -name "*_benchtest.go" | grep -v vendor | grep -v '.git/' | grep -v '.claude/'
# 无输出

# go test -bench=. 结果：
# ?   	github.com/aero/aero-id/internal/pkg/idgen	[no test files]
# 即使是 idgen（核心 ID 生成器，每秒可能调用数万次）也没有基准测试
```

**证据 2：依赖延迟无基线**

```go
// internal/sso/client.go — ValidateToken 路径包括：
// 1. TokenCache.Get (memory)
// 2. singleflight.Do (可能)
// 3. gRPC dial + call (网络)
// 4. UserCache.Get (memory)
// 这些步骤的延迟分解完全未知——没有 instrumented 基准
```

**证据 3：关键数据路径无性能观测**

```go
// internal/repository/message.go — SearchMessages
// 执行 tsquery 全文搜索，涉及 GIN 索引扫描 + ts_rank 排序
// 但没有任何关于"10 万行 + 1 万停止词下的 P50/P99 延迟"的数据

// internal/outbox/relay.go — drain 每次轮询 FetchUnpublished
// 在 outbox 表增长到百万行后的性能表现完全未经测试
```

**证据 4：CI 中无性能回归检测**

```yaml
# .github/workflows/ci.yml
# 当前 CI 包含：lint → vet → build → test
# 无 benchmark step，无 perf comparison，无负载测试
```

### 建议方案

| 组件 | 新增内容 |
|------|----------|
| **核心路径基准** | 为 ID 生成（idgen）、FTS 查询（repository/*_search.go）、通知创建、权限检查添加 `_benchmark_test.go` |
| **基准数据生成器** | 利用已有的 `testpg`（testcontainers Postgres）加载不同规模的数据集（1K / 10K / 100K 行），运行基准并记录结果 |
| **CI 基准门禁** | `benchstat` 比较 PR 分支与 main 的基准结果。如果 P50 延迟退化 > 10%，标记为性能回归 |
| **端到端负载测试** | `test/load/` 目录 + k6 脚本：模拟典型用户行为（登录 → 发消息 → 上传文件 → 搜索 → 读取通知），记录吞吐量和延迟分布 |
| **容量规划模型** | 基于基准数据建立简单的线性模型：每个活跃用户产生 X 条消息/天、Y MB 文件/天 → 因此 N 个用户需要 M 个实例 |
| **SLO Dashboards** | Grafana dashboard 导出 JSON，包含核心 RED 指标的阈值告警（SSO P99 > 200ms、FTS P99 > 100ms、Upload P99 > 2s） |

### 边界情况与风险

- **基准数据代表性**：合成数据无法完全模拟真实用户行为。建议逐步引入生产流量回放（如 GoReplay 录制→回放）。
- **CI 环境波动**：GitHub Actions 的性能不稳定。基准结果应使用相对比较（PR 分支 vs main 分支在同一 runner 上），而非绝对值。
- **基准维护成本**：每个新功能都要更新基准。在 PR template 中加 `[ ] Benchmarks added/updated` 检查项。

---

## 方向三：数据策略引擎（Policy-as-Code — 生命周期 · 访问控制 · 合规自动化）

### 为什么从未被系统性讨论

已有分析讨论了"数据生命周期管理"（冷热分层、TTL）和"GDPR 导出/删除"，但**没有人提出一个统一的、可扩展的策略引擎**来自动化这些决策。当前每条策略（如 retention、access control）是硬编码在代码中的。

### 问题陈述

aero-id 存储了三种用户数据类型——文件、消息、评论。每种类型有不同的敏感度、合规要求、和保留需求。但当前**所有策略是硬编码的**：

1. **生命周期策略硬编码**：GC sweeper 使用配置中的 `retention` 对所有文件一视同仁。无法说"法律封存的文件永不删除"或"30 天前的系统通知自动过期"。
2. **访问控制硬编码**：`CanReadFile` 和 `CanReadMessage` 是 domain 包中的 if-else 函数。要增加新的访问规则（如"文件可被某个团队查看"）需要改代码。
3. **合规策略硬编码**：审计日志 append-only（正确），但无法配置"审计日志保留 7 年后自动归档"或"涉及 GDPR 用户的数据操作单独标记"。
4. **无策略验证**：策略在变更时无法进行语法验证或冲突检测。运维人员修改配置后，直到运行时才能发现策略错误。

### 代码级证据

**证据 1：`CanReadFile` 和 `CanReadMessage` 是纯代码 if-else**

```go
// internal/domain/access.go
func CanReadFile(actor string, f *File) bool {
    return f.Visibility == VisibilityPublic || f.OwnerUID == actor
}

func CanReadMessage(actor string, m *Message) bool {
    return m.FromUID == actor || m.ToUID == actor
}
// 每增加一个新的访问规则（如"共享文件的被授权者"、"团队空间的成员"），
// 都需要修改这两行函数。没有组合策略、没有规则优先级、没有策略缓存。
```

**证据 2：`Sweeper` 对所有文件使用统一 retention**

```go
// internal/storage/gc/sweeper.go
func (s *Sweeper) purgeSoftDeleted(ctx context.Context) (int, int, error) {
    before := s.now().Add(-s.cfg.Retention) // 所有文件相同 retention
    // 无文件级策略、无类型级策略、无用户级豁免
}
```

**证据 3：审计日志无自动归档策略**

```go
// internal/audit/repository.go — Append
// 审计日志只增不删。没有"超过 1 年的审计日志自动转储到冷存储"的策略。
// 在一个高活跃度的生产系统中，审计日志增长不可控：
// 1000 次操作/天 × 365 天 = 365,000 行/年 —— 永不清除
```

**证据 4：搜索结果的可见性过滤是 Golang code，非声明式策略**

```go
// internal/repository/message.go — SearchMessages 使用硬编码的
// WHERE (from_uid = ? OR to_uid = ?) 来隔离消息搜索结果。
// 如果未来增加"部门管理员可以查看部门成员的消息"，需要重写 SQL。
```

### 建议方案

| 组件 | 新增内容 |
|------|----------|
| **策略引擎（Policy Engine）** | 引入轻量级 Go 策略评估库（如 `open-policy-agent/opa` 的 Rego + Go SDK，或更简单的 `casbin`）。策略与代码分离 |
| **生命周期策略** | Rego 规则示例：`retention = "7d" { file.type == "temp" }; retention = "forever" { file.legal_hold == true }; retention = "365d" { file.type == "message_attachment" }` |
| **访问控制策略** | Rego 规则：`allow = true { input.actor == input.file.owner }; allow = true { input.file.shared_to[input.actor] }; allow = true { input.actor.role == "admin" }` |
| **策略管理 API** | `POST /v1/admin/policies`（需要 `system.config` 权限）、`GET /v1/admin/policies`、`GET /v1/admin/policies/:id/evaluate`（干运行测试） |
| **策略缓存层** | 编译后的策略 AST 在内存中缓存，通过配置版本号触发刷新。评估链路增加 OTel span |
| **GC 策略化** | `Sweeper` 改为读取策略引擎的 retention 决策，而非全局配置。不同文件类型、不同用户可配置不同保留期 |

### 边界情况与风险

- **策略评估性能**：每次文件读取请求都评估 Rego 规则会增加 1-5ms 延迟。需要缓存层和评估结果 cache。
- **策略爆炸**：随着规则的增加，策略冲突的风险上升。每次策略变更应执行冲突检测（"规则 A 说 allow，规则 B 说 deny，哪条优先？"）。
- **默认拒绝**：安全最佳实践是默认拒绝。迁移现有策略时需要仔细验证没有遗留权限缺口。
- **运维复杂度**：引入 OPA/Casbin 增加了运维心智负担。建议配置了策略引擎但默认使用"allow all"向后兼容模式。

---

## 方向四：基础设施即代码与部署自动化（IaC — 生产部署拓扑 · Helm Charts · 监控栈 · 灾备拓扑）

### 为什么从未被系统性讨论

此前分析聚焦于**服务自身的功能扩展**，但缺少了"让别人能部署这个服务"的视角。对于用 aero-id 构建产品的团队来说，部署方式与 API 功能同等重要。

### 问题陈述

aero-id 有详尽的 `docker-compose.yml` 用于本地开发，但**完全没有生产部署制品**：

1. **无 Kubernetes 清单** — 没有 Deployment、Service、ConfigMap、Secret 的 YAML。也无法利用 K8s 的滚动更新、自动扩缩容、自愈能力。
2. **无 Helm Chart** — 无法通过 `helm install aero-id` 一键部署。需要手动编写所有 K8s 资源。
3. **无 Terraform 模块** — 无法声明式管理基础设施（RDS PostgreSQL、S3 Bucket、ALB/NGINX Ingress、Route53 DNS）。
4. **无监控栈配置** — Prometheus 规则、Grafana Dashboard、告警通知（PagerDuty/Slack）需要手动配置。
5. **无灾备/恢复流程** — 数据库备份策略、S3 跨区域复制、实例故障切换—这些完全未定义。

### 代码级证据

**证据 1：唯一部署产物是 docker-compose.yml，且纯开发用途**

```yaml
# deployments/docker-compose.yml
# 仅定义了 aero-id、postgres、minio、sso-mock。
# 无健康检查、无资源限制、无 volume 持久化策略、无网络策略。
# 注释可能标明"开发用"但无生产配置。
```

**证据 2：K8s 资源完全不存在**

```bash
$ find . -name "*.yaml" -path "*/k8s/*" -o -name "*.yaml" -path "*/kubernetes/*" | grep -v vendor | grep -v '.git/' | grep -v '.claude/'
# 无输出
```

**证据 3：ConfigMap 设计尚未考虑多环境**

```yaml
# configs/config.yaml 包含所有配置，但：
# - 无 dev/staging/prod 的 profile 隔离
# - 密码通过 env 注入（正确），但其他环境差异配置（DB pool size、限流阈值）无法分离
# - 没有 ConfigMap ↔ 文件映射的抽象
```

**证据 4：监控基础设施配置缺失**

```bash
$ find . -name "*.json" -path "*/grafana/*" -o -name "*.rules" -path "*/prometheus/*" | grep -v vendor | grep -v '.git/' | grep -v '.claude/'
# 无输出
# 虽然 internal/pkg/metrics 暴露了完整的 RED 指标，
# 但没有配套的 Grafana dashboard JSON 和 Prometheus alerting rules。
```

**证据 5：无数据库备份策略代码**

```bash
# 没有 scripts/backup-db.sh、没有 pg_dump 的 cronjob 配置、
# 没有 WAL 归档配置、没有恢复到时间点的脚本。
```

### 建议方案

| 组件 | 新增内容 |
|------|----------|
| **Helm Chart** | `deployments/charts/aero-id/` — 包含 Deployment（http + grpc 双端口）、Service、ConfigMap、Secret（kv）、HPA、PDB、ServiceMonitor（prometheus-operator）、Ingress |
| **Terraform 模块** | `deployments/terraform/` — RDS PostgreSQL 实例 + S3 Bucket + IAM 角色 + 安全组。输出 RDS endpoint 和 S3 bucket name 供 Helm values 引用 |
| **多环境配置** | `configs/config.dev.yaml`、`configs/config.staging.yaml`、`configs/config.prod.yaml`，共享基础配置用 `configs/config.base.yaml` |
| **Grafana Dashboards** | `deployments/monitoring/grafana/` — HTTP + gRPC RED 仪表盘、SSO 健康仪表盘（断路器状态 + 缓存命中率 + 延迟）、Outbox 延迟仪表盘、存储增长仪表盘 |
| **Prometheus 告警规则** | `deployments/monitoring/prometheus/` — `AeroIdHighSSOLatency`、`AeroIdOutboxLag`、`AeroIdDeadLetterEvents`、`AeroIdHighErrorRate`、`AeroIdCircuitBreakerOpen` |
| **灾备脚本** | `scripts/backup/` — PostgreSQL 自动备份（pg_dump + S3 上传）、S3 跨区域复制配置、灾备切换 runbook（markdown） |

### 边界情况与风险

- **Helm values 大型化**：随着配置项增加，values.yaml 可能变得巨大。建议使用预设 profiles（`values.dev.yaml`、`values.staging.yaml`、`values.prod.yaml`）。
- **Terraform 状态管理**：必须使用远程状态后端（S3 + DynamoDB lock），禁止本地状态文件。
- **Secrets 管理**：Secret 应通过外部密钥管理（AWS Secrets Manager / HashiCorp Vault）注入，而非 Helm values。避免在 Git 中提交生产密钥。
- **版本兼容性**：Helm Chart 版本应与 aero-id 应用版本同步变更（SemVer）。

---

## 方向五：API 治理 — 消费者驱动合约测试与 Schema 演化管理（Consumer-Driven Contract Testing · Schema Registry · API 版本协商）

### 为什么从未被系统性讨论

已有分析提到"API 弃用"和"API changelog"夹杂在其他方向中，但**没有人将 API 合约治理作为独立的、代码级可操作的扩展方向**。当外部团队开始依赖 aero-id 的 API 时，合约断裂是代价最高的问题。

### 问题陈述

aero-id 有 HTTP 和 gRPC 两套 API，它们共享同一 service 层。但**没有任何机制保证 API 的向后兼容性**：

1. **Proto breaking change 无 CI 检测**：PR 可以合并一个删除了 proto 字段的变更，导致下游 gRPC 客户端（用该字段的）在运行时收到 unexpected field 错误。
2. **HTTP JSON 响应格式无合约**：HTTP API 的响应体是手写的 `gin.H` map。如果某次重构改变了字段名（如 `user_name` → `username`），所有 HTTP 客户端都会静默断裂。
3. **无消费者驱动的合约**：没有已知消费者（known consumers）的期望清单。无法在 CI 中验证"这个变更不会破坏移动端 / Web 端 / 第三方集成"。
4. **无 API 版本协商**：所有请求走 `/v1/*`。如果引入一个不兼容的 v2，无法在一个 endpoint 上同时服务 v1 和 v2 的消费者。

### 代码级证据

**证据 1：Proto 无"不兼容"检测**

```makefile
# Makefile 中的 proto 目标只做代码生成，不运行 buf breaking
proto:
	protoc \
	  --proto_path=api/proto \
	  --go_out=. --go_opt=module=github.com/aero/aero-id \
	  --go-grpc_out=. --go-grpc_opt=module=github.com/aero/aero-id \
	  api/proto/sso/v1/sso.proto \
	  api/proto/aero_id/v1/aero_id.proto
# 没有 buf lint 步骤，没有 breaking change 检测
```

**证据 2：HTTP 响应体是无结构的 gin.H**

```go
// internal/handler/http/search.go
func (h *SearchHandler) Search(c *gin.Context) {
    // ...
    httpx.OK(c, gin.H{"results": items})
    // gin.H 是 map[string]any，无编译时类型检查
    // 如果某天 results 被重命名为 hits，所有 HTTP 客户端断裂
}
```

```go
// internal/handler/http/message.go 中的响应结构也类似：
c.JSON(http.StatusOK, gin.H{
    "inbox": msgs,
    "next_cursor": next,  // 手写字符串 key
    "users": users,
})
// 字段名 typo 不会被编译器捕获
```

**证据 3：无已知消费者清单（consumer list）**

```bash
# 没有任何文件记录"谁在消费 aero-id API"
# 没有 contracts/ 目录
# 没有 consumers.json
```

**证据 4：无 API 版本中间件**

```go
// internal/middleware/auth.go — Auth 中间件
// 无 API 版本协商（请求头 Accept: application/vnd.aero.v2+json）
// 所有请求无差别路由到同一 handler

// internal/handler/http/router.go
r.Auth.GET("/files", fileHandler.List) // 固定 /v1/files
// 如果要引入 /v2/files，需要手动创建新的路由组
```

**证据 5：Parity 测试只检查存在性，不检查合约兼容性**

```go
// internal/handler/grpc/parity_test.go
// 测试验证每个 HTTP endpoint 是否有对应的 gRPC 调用
// 但不验证 HTTP 响应体结构与 proto message 的映射关系
// 当 Response Body 与 Proto Schema 不一致时，测试照常通过
```

### 建议方案

| 组件 | 新增内容 |
|------|----------|
| **buf 工具链集成** | `Makefile` 增加 `buf lint` 和 `buf breaking` 步骤。CI 中在 proto 生成前运行 `buf breaking --against .git#branch=main` |
| **OpenAPI 规范 + gRPC 反射 as source of truth** | 从 proto 自动生成 OpenAPI 规范（`grpc-gateway` 或 `protoc-gen-openapiv2`），确保 HTTP API 与 proto schema 始终一致 |
| **消费者合约测试框架** | 引入 `pact-go`（或自建轻量合约测试）。每个已知消费者提供期望的请求/响应对。CI 中验证 API 行为满足所有注册合约 |
| **API 版本路由** | 扩展 router 支持 `Accept-Version` 头或 URL 前缀版本号。v1 和 v2 handler 可共存 |
| **结构化响应类型** | HTTP handler 中的 `gin.H` 替换为从 domain 类型序列化的结构体，确保响应体字段名与 proto schema 一致（通过 JSON 标签） |
| **Deprecation Policy** | 制定标准的 API 弃用流程：V1 → 标记 deprecated（Sunset 头）→ 6 个月后移除。所有弃用记录在 `api/deprecations.md` |

### 边界情况与风险

- **合约测试维护**：每个外部消费者需维护一份合约。消费者少时（<3）可能不值得。建议至少有一个已知消费者后才引入。
- **版本爆炸**：gRPC 的 proto 包不允许同时有两个版本的同一 service。gRPC 的版本策略通常是在 proto package 上加后缀（`v1`、`v1beta1`），但长期维护多个版本的成本很高。
- **假阳性**：合约测试过于严格会导致频繁的假阳性失败。合约应定义"必须保持的契约"而非"所有的行为"。
- **OpenAPI spec drift**：如果 spec 是从代码和 proto 分别生成的，两份 spec 可能不一致。必须确保单一数据源（proto → spec）。

---

## 优先级与依赖关系

| # | 方向 | 优先级 | 前置依赖 | 估算工作量 | 核心受益者 |
|---|------|--------|----------|-----------|-----------|
| 1 | **外部开发者体验（API 产品化）** | P1 | 无 | 中（1-2 月） | 外部集成团队 |
| 2 | **性能基准与回归检测体系** | P1 | 无 | 中（1-2 月） | 平台/SRE |
| 3 | **数据策略引擎** | P2 | 方向 1（策略管理 API 需 OpenAPI spec） | 高（2-3 月） | 合规/平台 |
| 4 | **基础设施即代码与部署自动化** | P0 | 无（可立即开始） | 中-高（2-3 月） | SRE/运维 |
| 5 | **API 治理 — 合约测试与 Schema 演化** | P2 | 方向 1（SDK + OpenAPI 先行） | 中（1-2 月） | 所有 API 消费者 |

**建议执行顺序：** 方向 4 → 方向 2 → 方向 1 → 方向 5 → 方向 3

- **方向 4（IaC + 部署）** 应最先启动：没有自动化部署，所有功能都是空中楼阁。Helm Chart 和 Terraform 模块可以从现在开始构建，不依赖任何代码变更。
- **方向 2（性能基准）** 紧随其后：在投入更多功能之前，建立性能基线。这不仅是工程决策的依据，也是容量规划的输入。
- **方向 1（API 产品化）** 在 IaC 之后：当外部团队可以部署 aero-id 时，下一步就是让他们能轻松集成 API。
- **方向 5（API 治理）** 在方向 1 之后：有了 OpenAPI 规范和 SDK，才需要合约测试和版本管理。
- **方向 3（数据策略引擎）** 最后：最有野心但依赖性最强（需要策略管理 API，而 API 产品化是前提）。

---

## 各方向与既有能力的架构映射

```
方向一：API 产品化
  ┌──────────────┐     OpenAPI 3.0     ┌──────────────┐
  │  proto files  │ ─────────────────→ │  openapi.json │ ← swaggo/gnostic
  │ (gRPC schema) │                     └──────┬───────┘
  └──────────────┘                             │
        │                                      ├──→ SDK Generator (Go/TS/Python)
        │                                      ├──→ Postman Collection
        │                                      └──→ API Portal
        │
  gRPC refection (默认开启 via mTLS guard)

方向二：性能基准
  internal/pkg/idgen/     ──→ idgen_benchmark_test.go
  internal/repository/    ──→ *_search_benchmark_test.go
  internal/hub/           ──→ hub_benchmark_test.go
  test/load/              ──→ k6 scripts (message flow, file upload, search)
  CI: benchstat vs main   ──→ performance regression gate

方向三：数据策略引擎
  Rego/Casbin Policies ──→ PolicyEngine.Evaluate(ctx, input) → Decision
         │
         ├── GC Sweeper  ──→ 读取 policy 获取 retention 而非全局配置
         ├── Access Control ──→ CanReadFile/CanReadMessage 替换为 policy eval
         └── Audit Archiver  ──→ 审计日志超期自动归档

方向四：IaC 与部署自动化
  Terraform ──→ RDS + S3 + IAM + Network
  Helm      ──→ Deployment + Service + HPA + PDB + ConfigMap + Secret + ServiceMonitor
  Monitoring──→ Grafana dashboards (JSON) + PrometheusRules (YAML)
  Backup    ──→ pg_dump cronjob + S3 upload + restore script

方向五：API 治理
  buf ──→ proto lint + breaking change detection
  Pact──→ consumer-driven contract tests
  API Version Middleware ──→ Accept-Version header → route to correct handler
  Structured Response Types ──→ gin.H → typed structs with JSON tags from proto
```

---

## 总结

这五个方向不同于此前 120+ 分析中的任何一个。它们不是"加 endpoint"或"修 bug"，而是：

| 方向 | 核心本质 | 在当前代码库中的状态 |
|------|---------|---------------------|
| API 产品化 | 从"可调用的 API"到"可集成 API 产品" | Proto 完整，但无规范、无 SDK、无生命周期 |
| 性能基线 | 从"功能正确"到"性能可知" | 零基准测试，零 CI 回归检测 |
| 策略引擎 | 从"硬编码逻辑"到"声明式策略" | CanReadFile/Sweeper 全部硬编码 |
| 部署 IaC | 从"docker-compose 可运行"到"生产一键部署" | 仅开发环境 docker-compose |
| API 治理 | 从"发布即永久"到"受控的 API 演化" | 无合约、无版本、无弃用机制 |

它们共同回答了一个问题：**"当外部团队想要使用、集成和部署 aero-id 时，需要什么？"** ——这个视角在之前的 120+ 分析中从未被系统性讨论过。
