# 架构师分析报告：aero-id 平台产品化扩展方向

> **分析基准**: `docs/requirements/2026-07-11-aero-id-platform-productization-extension-directions.md`  
> **代码基准**: `internal/` ~47K Go 代码（截至 2026-07-12）  
> **分析方法**: 全量代码交叉验证 + 架构依赖分析 + 技术债评估

---

## 1. 架构评估

### 1.1 当前架构的优势

aero-id 在架构层面达到了一个**很高的成熟度**，尤其值得称赞的是：

| 优势 | 代码证据 | 架构价值 |
|------|---------|---------|
| **双协议对等架构** | HTTP (Gin) + gRPC 共享同一 `service` 层 | 单一业务真相源，消除行为漂移 |
| **Router 抽象层** | `RouteRegistrar` 接口解耦路由注册与具体 handler | 模块可以独立添加路由，无需修改核心框架 |
| **可选能力模式（OPT-IN）** | GC 扫表、限流、自动迁移、gRPC reflection 全部默认关闭 | 安全默认值，运维显式启用 |
| **优雅关停 + 滑动健康检查** | gRPC health → NOT_SERVING → 关停 relay/GC → HTTP shutdown → gRPC graceful stop | 零停机部署的关键基础 |
| **冷启动 warmup** | SSO 探针 + DB ping 在后台异步预热 | 减少首次请求的冷启动延迟 |
| **Outbox 模式 + 责任链** | `outbox.Chain(dispatcher, webhookHandler)` | 可扩展的事件处理管线，无需修改核心 |
| **存储接口设计** | `FileStorage` 接口 + 可选 `Lister` 接口 | 正交能力设计，后端无需实现不需要的功能 |

### 1.2 关键设计决策回顾（基于实际代码验证）

**决策 D2（横向分层 vs 纵向模块）** 在实际代码中出现了分歧。设计文档说"不按模块纵切"，但实际代码结构中 **同时存在两种模式**：

```
internal/          # 传统的"横切"模式
├── service/       # 统一 service 层
├── repository/    # 统一 repo 层
├── handler/http/  # 统一 handler

internal/          # 新增的"纵切"模块
├── file/          # service + handler + repo 内聚
├── message/
├── comment/
├── notification/
├── block/
├── search/
├── audit/
├── outbox/
├── hub/
```

这是一个值得关注的 **架构漂移** 信号。两种模式各有优劣，但并存会导致：
- 新开发者困惑应该在哪创建新代码
- 跨模块调用路径不一致（有些走 `service.NewFileService()`，有些走 `file.NewXxx()`）
- 自动化分析工具（如 import cycle detection）复杂度加倍

**建议**: 在 AGENTS.md 或 ADR 中明确记录模式选择策略，避免继续漂移。

**决策 D8（metrics/tracing 不内置）** 已被实际代码推翻——`internal/pkg/metrics` 和 `internal/pkg/tracing` 都已实现，且深度集成到 SSO 断路器、Outbox relay、HTTP/gRPC 中间件中。这是**正确的演进**，但设计文档已过时。

### 1.3 架构债务

#### 债务 1：`gin.H` 无类型响应（严重）

这是最突出的问题。整个 HTTP handler 层使用 `gin.H{"key": val}` 构建响应，没有任何编译时类型安全。当 proto 与 HTTP 响应字段不一致时，没有机制可以检测。

```
internal/handler/http/file.go      → gin.H{"files": ..., "next_cursor": ...}
internal/handler/http/message.go   → gin.H{"inbox": ..., "outbox": ...}
internal/handler/http/comment.go   → gin.H{"comments": ..., ...}
internal/handler/http/search.go    → gin.H{"results": ...}
```

**影响范围**: 约 10+ 个 handler 文件 × 每个 3-8 个响应类型  
**修复成本**: 中（为每个响应定义 Go struct，从 proto 或 domain 类型映射）  
**断裂风险**: 高（每一次重构字段名都会静默破坏 HTTP 客户端）

#### 债务 2：双包结构不一致（中等）

`internal/{file,message,comment,notification,block,search,audit,outbox}/*` 与 `internal/{service,repository,handler}/**` 共存。依赖关系复杂：

```
internal/service/file.go  → 调用 internal/file/service.go 中的方法？
internal/repository/file.go → 与 internal/file/repository.go 的关系？
```

需要确认是否出现了同一逻辑的两种实现，或仅仅是"旧代码逐步迁移到新包结构"的中间状态。

#### 债务 3：Access Control 硬编码（中等）

`internal/domain/access.go` 中的 `CanReadFile` 和 `CanReadMessage` 是简单的 if-else 函数。随着分享功能（`fileShareRepo`）和 Block 功能的加入，访问控制逻辑已经 **散布到多个层**：

- `domain/access.go` — 基础的 owner/visibility 检查
- `file/service_share.go` — 分享逻辑中的权限检查
- `block/repository.go` — Block 逻辑中的权限抑制
- `middleware/auth.go` — token 级别的鉴权

没有统一入口，违反 "单一真相源" 原则。

#### 债务 4：proto → HTTP 响应无自动映射（低-中）

gRPC handler 使用 proto 生成的 Go struct（编译时类型安全），而 HTTP handler 使用 `gin.H`。两套传输没有共享响应模型，导致：

- 新增字段需要同时修改 proto 和 HTTP handler
- `internal/handler/grpc/parity_test.go` 只能检查 endpoint 存在性，无法验证响应兼容性

---

## 2. 扩展方向

以下 5 个方向建立在已有分析文档的基础上，但**从架构视角重新评估了优先级和技术方案**，并与现有代码的架构债务关联。

### 方向 A：响应模型统一化 + OpenAPI 自动生成

**定位**: 方向 1（API 产品化）的前置条件，也是债务 1+4 的偿还。

**为什么需要**:
- 消除 `gin.H` 的技术债（当前最迫切的架构问题）
- 为 OpenAPI 规范生成提供结构化基础（swaggo 注解需要已知类型）
- 使 HTTP 响应体与 proto schema 始终一致，消除"双协议漂移"

**核心挑战**:
- **不引入 proto → HTTP 的强绑定**：HTTP 响应需要比 proto message 更丰富或更简化的结构（例如添加 `next_cursor`、`users` 等客户端辅助字段）。如果直接复用 proto struct，proto 的字段变更可能意外改变 HTTP 响应。
- **避免大型重构回归**：10+ handler 的响应结构替换测试覆盖要求高。

**架构变更**:

```
当前: gin.H{"results": items, "next_cursor": cur}
      ├── HTTP response (无编译时检查)
      
目标: 
type SearchResponse struct {
    Results    []FileItem `json:"results"`
    NextCursor string     `json:"next_cursor"`
}
      ├── swaggo: @Success 200 {object} SearchResponse
      ├── OpenAPI spec 自动生成
      └── HTTP response (编译时类型安全)
```

**关键设计权衡**: **两个选项**

| 方案 | 做法 | 优势 | 风险 |
|------|------|------|------|
| **A1: 独立响应类型** | 每个 handler 定义独立的 HTTP 响应 struct | 解耦 proto；可自定义；渐近式迁移 | 类型膨胀（~30 types）；维护协议映射成本 |
| **A2: proto 衍生响应** | 从 proto 生成 HTTP 响应 struct（protoc-gen-openapiv2 + grpc-gateway） | 单真相源；零漂移 | 强耦合；HTTP 自定义字段需额外处理 |

**推荐**: **A1 起步，逐步向 A2 靠拢**。先在核心路径（File/Message/Search）建立独立响应类型，积累经验后再评估是否引入 grpc-gateway。

**对现有系统的影响**:
- 纯新增+重构；不改变路由、中间件、service 层
- 可以逐一 handler 迁移，每个 PR 改 1-2 个 handler

---

### 方向 B：策略引擎 — 统一访问控制 + 生命周期

**定位**: 方向 3（数据策略引擎）的精简版，聚焦于**先还债再扩展**。

**为什么需要**:
- 债务 3（访问控制分散）影响安全和可维护性
- 当前 `CanReadFile`/`CanReadMessage` 无法表达分享、Block、团队空间等组合场景
- GC sweeper 的全量统一 retention 策略在文件类型增多后不可持续

**核心挑战**:
- **避免过度工程化**：引入 OPA/Casbin 对于单一服务的策略引擎可能过重。需要评估是否真的需要 Rego，或一个 Go 的组合策略模式就足够。
- **性能预算**：每次文件读取都评估策略引擎会增加延迟。策略评估必须在 P50 < 1ms 的预算内。

**架构变更建议（架构师修正）**：

分析文档建议直接引入 OPA/Casbin。**我建议分期实施**：

| 阶段 | 变更 | 技术方案 |
|------|------|---------|
| **Phase B1** | 统一访问控制入口 | 将分散的 `CanReadFile`+`fileShareRepo`+`blockRepo`+... 整合为一个 `AccessEvaluator` 接口 |
| **Phase B2** | 引入组合式策略 | 用 Go 的组合函数（Composite+Chain of Responsibility）实现简单策略链，不引入外部依赖 |
| **Phase B3** | 评估是否升级到 OPA | 仅当策略数量 > 20 条或需要热更新策略时引入 Rego |

```
// Phase B1 目标接口
type AccessEvaluator interface {
    CanRead(ctx context.Context, actor string, resource Resource) Decision
    CanWrite(ctx context.Context, actor string, resource Resource) Decision
    // ...
}

// Phase B2 组合策略示例
type chainEvaluator struct {
    policies []Policy
}
func (c *chainEvaluator) evaluate(ctx context.Context, input AccessRequest) Decision {
    for _, p := range c.policies {
        d := p.Evaluate(ctx, input)
        if d.IsDeny() { return d }    // Deny overrides
        if d.IsAllow() { continue }   // Allow checked at end
    }
    return DecisionAllow  // 默认允许（向后兼容）
}
```

**对现有系统的影响**:
- Phase B1: 中等重构（替换 `domain.CanReadFile` 调用点，约 5-8 处）
- Phase B2: 低影响（新增文件，不修改现有逻辑）
- Phase B3: 中影响（替换 B2 的策略实现，接口不变）

---

### 方向 C：基准体系 + 容量模型

**定位**: 方向 2（性能基准）—— 完全赞同分析文档的论据，补充架构层面建议。

**关键补充：不仅仅是基准，还需要"可观测性基础设施"**：

分析文档正确识别了零基准测试的问题，但遗漏了一个关键点：**aero-id 虽然有 metrics 注册表，但没有任何 dashboard JSON 和告警规则**。这意味着：

1. 即使收集了指标，也无法可视化
2. 即使有了基准，也无法在生产中监控退化

**建议合并**:
- 基准体系 + 运行时监控指标 + Grafana dashboard = **"性能管理三件套"**
- CI benchmark 门禁只捕获代码级退化，Grafana 告警捕获基础设施级/负载级退化

```
┌─────────────────────────────────────────────────────┐
│                   Performance Management            │
├─────────────┬───────────────────┬───────────────────┤
│  CI Bench   │  Production SLO   │  Capacity Model   │
│  (benchstat) │  (RED dashboards)  │  (load test)      │
│             │                   │                   │
│  idgen      │  SSO P99 < 200ms │  x users → y rps  │
│  FTS        │  FTS P99 < 100ms │  → z instances    │
│  SSE        │  Outbox lag < 1s │                   │
└─────────────┴───────────────────┴───────────────────┘
```

**核心挑战**:
- **测试环境 vs 生产环境的性能差异**：CI runner（GitHub Actions）与生产环境（K8s pod 规格）不同。基准结果只能做**相对比较**（PR vs main 同一环境），不能做 SLO 定义。
- **基准维护成本**：每个新功能都需要添加或更新基准。建议在 PR template 中增加 checkbox，并在 CODEOWNERS 中指定性能负责人。

**对现有系统的影响**:
- 纯新增，不修改现有代码
- `_benchmark_test.go` + `test/load/k6/` + `deployments/monitoring/`

---

### 方向 D：IaC 生产化

**定位**: 方向 4（部署 IaC）—— 完全赞同分析文档的 P0 定位，但**补充架构层面的分解建议**。

分析文档将 IaC 作为整体 P0，但 IaC 本身可分解为优先级不同的子项目：

```
P0（禁止部署前完成）
├── Helm Chart（Deployment + Service + ConfigMap + Secret + HPA + PDB + ServiceMonitor）
├── 多环境配置（configs/config.{dev,staging,prod}.yaml）
└── 健康检查完善（/healthz 存活, /readyz 就绪, /metrics 指标暴露）

P1（上线第一周完成）
├── Terraform 模块（RDS + S3 + IAM + 安全组）
├── Grafana Dashboard 基础版（HTTP RED + SSO + Outbox）
└── Prometheus 告警规则基础版

P2（上线后逐步完善）
├── 数据库备份脚本（pg_dump + S3 + 恢复验证）
├── GitHub Actions 部署流水线
└── 灾备 runbook
```

**关键设计决策**: **Helm vs Kustomize vs raw YAML**

| 方案 | 优势 | 劣势 | 推荐场景 |
|------|------|------|---------|
| **Helm** | values 模板化；依赖管理；社区成熟 | 模板调试困难；Chart 随 APi 版本增加配置复杂度 | 对外分发（推荐） |
| **Kustomize** | 原生 kubectl；无学习曲线 | 功能较弱；无依赖管理 | 内部单一团队 |
| **raw YAML + envsubst** | 最简；零依赖 | 不可编排；难管理 | MVP 验证期 |

**推荐**: Helm + 预置 profiles（`values.dev.yaml`/`values.prod.yaml`），因为 aero-id 是一个**需要被外部团队部署的服务**，Helm Chart 是 Go 生态的标准分发格式。

---

### 方向 E：API Schema 治理 + Consumer Contract

**定位**: 方向 5（API 治理）—— 分析文档的建议方案偏高成本。从架构角度提供**渐进式替代方案**。

**架构师修正**: Pact 合约测试框架对于当前阶段（0-3 个已知消费者）**过度设计**。Pact 的维护成本（Pact Broker、合约版本管理、provider verification CI job）在当前阶段可能超过收益。

**建议渐进方案**：

| 阶段 | 做什么 | 工具/方法 | 成本 |
|------|--------|----------|------|
| **Phase E1** | Proto breaking change CI 检测 | `buf breaking --against .git#branch=main` | 低（现有 Makefile 增加一步） |
| **Phase E2** | OpenAPI spec 一致性验证 | lint openapi.json vs proto schema | 低-中 |
| **Phase E3** | 已知消费者清单 + 手动回归测试 | `CONSUMERS.md` + 每个 release 前手动验证 | 低 |
| **Phase E4** | 消费者驱动合约测试（可选） | Pact — 仅在消费者 ≥ 3 且手动回归成本过高时引入 | 高 |

**buf 工具链的集成位置**：

```makefile
# 在 proto 生成前验证
proto: proto-lint proto-breaking
	protoc --go_out=... --go-grpc_out=...
	@echo "proto generated"

proto-lint:
	buf lint api/proto

proto-breaking:
	buf breaking api/proto --against .git#branch=main
```

---

## 3. 接口设计建议

### 3.1 关键模块接口原则

基于现有代码的分析，我建议以下接口设计原则：

**原则 1：单一抽象方向**

当前代码中同时存在 `internal/service/*` 和 `internal/{domain}/*` 两种包结构。**请选择其一作为主线**：

| 选项 | 处理方式 | 影响 |
|------|---------|------|
| **统一到 `internal/{domain}/*`** | 将 `internal/service/*` + `internal/repository/*` 逐步迁移到 `internal/{file,message,...}/*` | 需迁移 ~8 个文件；消除混乱 |
| **统一到 `internal/service/*`** | 将 `internal/{file,message,...}/*` 迁回中央目录 | 需迁回 ~6 个模块 |
| **保持现状 + 记录边界规则** | 明确哪些场景用纵切，哪些用横切 | 零迁移成本；但团队需严格遵循规则 |

**推荐**: **选项 3（保持现状 + 记录规则）**，因为迁移成本 > 收益。在 AGENTS.md 中明确：
> - 核心业务实体（File/Message/Comment）使用纵切模式 `internal/{entity}/*`
> - 跨实体能力（搜索、审计、限流、通知）使用横切模式 `internal/{capability}/*`
> - 共享基础设施（pkg/*）始终横切

**原则 2：新的抽象层只在解耦收益 > 复杂度成本时引入**

下面列出哪些情况**值得**引入新抽象，哪些**不值得**：

| 候选抽象 | 引入理由 | 复杂度成本 | 结论 |
|---------|---------|-----------|------|
| **Response 响应结构体** | 编译时类型安全；OpenAPI 生成前提 | 低（30 个 struct） | **值得** |
| **AccessEvaluator 统一接口** | 消除安全逻辑分散 | 低（1 个接口 + 现有实现包装） | **值得** |
| **Policy Engine（OPA/Casbin）** | 声明式策略 | 高（依赖+运维+学习曲线） | **暂缓**（Phase B2 先尝试组合模式） |
| **DAL 层抽象（Repository 接口）** | 当前 GORM 已满足；替换 ORM 概率极低 | 中（接口+mock 生成） | **不值得** |
| **Event 总线抽象（EventBus 接口）** | 解耦 Outbox → Dispatcher → Webhook | 当前 `outbox.Chain` 已足够 | **不值得** |
| **Cache 抽象层** | 替换 `sync.Map` 为 Redis | 可能需要，但当前无需求 | **暂缓** |

**原则 3：向后兼容优先**

所有对 HTTP/gRPC API 的变更必须遵循：
1. **新增字段**：永远安全
2. **重命名字段**：先添加新字段 + 标记旧字段为 deprecated → 3 release 后移除
3. **删字段**：不可接受。用 deprecated + 零值返回替代
4. **改语义**：需要 API 版本号（但尽量避免，成本高）

### 3.2 推荐的架构新增接口

```go
// 1. 统一访问控制（Phase B1）
type AccessEvaluator interface {
    CanRead(ctx context.Context, actor string, resource Resource) (bool, error)
    CanWrite(ctx context.Context, actor string, resource Resource) (bool, error)
    CanDelete(ctx context.Context, actor string, resource Resource) (bool, error)
    CanShare(ctx context.Context, actor string, resource Resource, target string) (bool, error)
}
// Resource 是 domain 类型的接口抽象
type Resource interface {
    ResourceType() string  // "file" | "message" | "comment"
    OwnerUID() string
    Visibility() Visibility
    // ...其他 ACL 相关属性
}

// 2. 策略决策（Phase B2）
type Policy interface {
    Name() string
    Evaluate(ctx context.Context, input AccessRequest) Decision
}
type Decision int
const (
    DecisionIndeterminate Decision = iota // 此策略无意见
    DecisionAllow                         // 允许
    DecisionDeny                          // 拒绝
)

// 3. 结构化响应（方向 A）—— 每个 handler 包内定义
// 不在公共包定义，避免循环依赖
```

---

## 4. 技术选型

### 4.1 现有技术栈评估

| 组件 | 当前方案 | 评估 | 建议 |
|------|---------|------|------|
| **HTTP 框架** | Gin | 成熟、轻量、Go 生态标准 | 保持，**不应替换** |
| **ORM** | GORM | 适合当前 schema 稳定、CRUD 为主的场景 | 保持，但注意避免 N+1 查询 |
| **配置** | Viper | 功能完备 | 保持 |
| **gRPC** | gRPC-go + protobuf | 成熟标准 | 保持 |
| **Proto 工具链** | protoc | 缺少 lint + breaking change 检测 | **补充** buf 工具链 |
| **SSO 客户端** | 自建 gRPC 客户端 + 双缓存（TokenCache + UserCache）+ 断路器 | 实现质量高 | 保持 |
| **OpenAPI** | 无 | 主要缺口 | **引入** swaggo 或 protoc-gen-openapiv2 |
| **容器/部署** | Docker Compose | 开发够用，生产不足 | **补充** Helm + Terraform |
| **监控** | 自有 metrics 注册表（`internal/pkg/metrics`） | 收集能力强，但无 Dashboard 和告警规则 | **补充** Grafana JSON + Prometheus rules |
| **测试** | 标准 Go test + testcontainers | 质量好 | **补充** benchmark + k6 负载测试 |

### 4.2 是否需要引入新技术栈

以下从架构角度分析分析文档建议的新技术栈：

#### swaggo（或 protoc-gen-openapiv2）

- **必要性**: **高**。OpenAPI 规范是外部开发者体验的基础，也是消除 `gin.H` 技术债的前置条件。
- **评估**: 
  - `swaggo/swag` — code-first，Go 注解风格，与 Gin 集成好
  - `protoc-gen-openapiv2` — design-first，从 proto 自动生成，单真相源
- **建议**: 先引入 `swaggo` 快速获得 OpenAPI 规范，后续评估是否切换到 proto 驱动

#### buf 工具链

- **必要性**: **高**。Proto breaking change 检测是最低成本的高收益投资。
- **评估**: buf 是 gRPC/Protobuf 生态的标准工具，社区活跃，Go 集成成熟。
- **建议**: 立即引入。与现有 protoc 工作流兼容。

#### OPA / Casbin

- **必要性**: **低-中**。取决于团队对策略引擎的需求迫切程度。
- **评估**: 
  - OPA/Rego — 表达能力极强，但学习曲线陡峭，运维心智负担高
  - Casbin — 更轻量，Go 原生，但表达能力有限
  - **自建组合策略（Go 函数组合）** — 零依赖，适合当前阶段
- **建议**: **暂缓**。先实施 Phase B1（统一入口）+ Phase B2（组合策略），等策略需求复杂到 > 20 条规则时再评估。

#### k6

- **必要性**: **中**。性能基线体系的负载测试组件。
- **评估**: k6 是负载测试的事实标准，JavaScript 脚本编写，与 CI 集成好。
- **建议**: 引入。轻量，有价值。

#### Pact

- **必要性**: **低**。当前阶段过度设计。
- **建议**: **暂缓**。先用 buf breaking + 手动回归测试。仅当外部消费者 ≥ 3 时引入。

### 4.3 自建 vs 采购决策

| 能力 | 自建理由 | 采购/集成理由 | 决策 |
|------|---------|--------------|------|
| **OpenAPI 规范生成** | Go 生态工具链成熟 | swaggo 开源+免费 | **集成** swaggo |
| **访问控制** | 当前逻辑简单（owner+visibility+share+block）；OPA 过重 | — | **自建** Phase B1+B2 |
| **性能基准** | Go testing 原生支持 | 无法"采购" | **自建** `_benchmark_test.go` |
| **Grafana Dashboard** | 公司已有 Grafana 实例 | 需要对接现有监控 | **自建** dashboard JSON |
| **Proto 校验** | — | buf 是标准 | **集成** buf |
| **API 版本管理** | 需要定制路由逻辑 | — | **自建** 版本中间件 |

---

## 5. 实施路线图

### 5.1 修正后的优先级排序

相比分析文档的原始优先级（4 → 2 → 1 → 5 → 3），我提出 **修正版**，核心差异是**方向 A（响应模型统一）作为方向 1 的前置条件被拆分为独立事项**，且**优先级上调**：

```
P0（现在 - 8 周）—— 基础稳固
├── A-0: 响应模型统一化（消除 gin.H 技术债）
├── D-P0: Helm Chart + 多环境配置
├── E-1: buf lint + buf breaking（CI 集成）
└── C-0: 核心路径 benchmark（idgen + FTS + SSE）

P1（8 - 16 周）—— 产品化
├── A-1: OpenAPI 规范生成（swaggo）
├── D-P1: Terraform 模块 + Grafana dashboards + Prometheus rules
├── B-1: AccessEvaluator 统一接口 + 策略链
└── C-1: k6 负载测试 + CI benchstat 门禁

P2（16 - 24 周）—— 治理 + 扩展
├── E-2: API 版本中间件 + 弃用流程
├── B-2: 策略引擎（如需要，升级到 OPA/Casbin）
├── D-P2: 备份脚本 + 灾备 runbook
└── E-3: 消费者合约测试（按需）
```

### 5.2 阶段划分与里程碑

**阶段 0："还债"（Week 1-4）**

| 里程碑 | 交付物 | 验证标准 |
|-------|--------|---------|
| M0.1 | 所有 HTTP handler 返回类型从 `gin.H` 替换为 struct | `make lint` 通过，`make test` 通过，无 `gin.H` 在响应构造中出现 |
| M0.2 | CI 中加入 `buf lint` + `buf breaking` | 提交一个 proto 字段移除的 PR → CI 失败 |
| M0.3 | 核心路径 benchmark 数据基线 | `make bench` 输出可重复的 P50/P99 数据 |
| M0.4 | Helm Chart 基本可用 | `helm template .` 输出有效的 K8s YAML |

**阶段 1："产品化"（Week 5-12）**

| 里程碑 | 交付物 | 验证标准 |
|-------|--------|---------|
| M1.1 | 通过 swaggo 生成 `openapi.json`，CI 验证 spec 最新 | 修改 handler 后不更新注解 → CI 失败 |
| M1.2 | Terraform 模块可部署 RDS + S3 + IAM | `terraform plan` 输出预期资源创建 |
| M1.3 | Grafana dashboard 导出 JSON，导入后展示核心 RED 指标 | 对接测试环境的 Prometheus 后仪表盘有数据 |
| M1.4 | `AccessEvaluator` 接口整合 `CanReadFile`+`shareRepo`+`blockRepo` | 所有权限检查通过单一接口 |

**阶段 2："治理"（Week 13-24）**

| 里程碑 | 交付物 | 验证标准 |
|-------|--------|---------|
| M2.1 | API 版本中间件支持 `Accept-Version` 头 | v1 和 v2 客户端同时正常工作 |
| M2.2 | 弃用流程文档 + `Sunset` 响应头 | 标记 deprecated 的 endpoint 返回 `Sunset` 头 |
| M2.3 | k6 负载测试套件 | `make load-test` 输出吞吐量和 P99 延迟 |
| M2.4 | 数据库自动备份 + 恢复验证 | `scripts/restore.sh` 从备份恢复到一个新实例 |

### 5.3 风险点与缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **R1: gin.H → struct 重构发现隐式兼容性问题** | 中 | 高 | 逐一 handler 迁移，每个 PR 合并前运行 `make test-integration` |
| **R2: Helm Chart 与真实 K8s 环境不匹配** | 中 | 中 | 在 stage 集群中实践部署；使用 `helm --dry-run --debug` 预检 |
| **R3: buf breaking 过于严格，阻塞正常开发** | 低 | 中 | 配置 `buf.yaml` 时的 `ignore_only` 列表；运行 `buf breaking --exclude` 排除已知变更 |
| **R4: 策略引擎引入后，现有权限逻辑出现缺口** | 中 | 高 | Phase B1 只封装不改变逻辑；新旧路径并行运行一段时间，对比决策结果 |
| **R5: benchmark 结果波动导致 CI 假阳性频繁** | 高 | 低-中 | 使用相对比较（PR vs main），而非绝对阈值；3 次运行取中位数 |
| **R6: 方向 D（IaC）与其他方向争夺注意力资源** | 中 | 中 | 将 IaC 工作分配给不同的团队成员（IaC 与代码开发技能集不同） |

### 5.4 依赖关系矩阵

```
         ↓ 依赖
         A-0  A-1  B-1  B-2  C-0  C-1  D-P0 D-P1 E-1  E-2
A-0      ─     Y    ─    ─    ─    ─    ─    ─    ─    ─
A-1      Y    ─    ─    ─    ─    ─    ─    ─    ─    ─
B-1      ─    ─    ─    ─    ─    ─    ─    ─    ─    ─
B-2      ─    Y    Y    ─    ─    ─    ─    ─    ─    ─
C-0      ─    ─    ─    ─    ─    ─    ─    ─    ─    ─
C-1      ─    ─    ─    ─    Y    ─    ─    ─    ─    ─
D-P0     ─    ─    ─    ─    ─    ─    ─    ─    ─    ─
D-P1     ─    ─    ─    ─    ─    ─    Y    ─    ─    ─
E-1      ─    ─    ─    ─    ─    ─    ─    ─    ─    Y
E-2      ─    Y    ─    ─    ─    ─    ─    ─    Y    ─
```

**关键路径**: A-0 → A-1 → B-2 → E-2（响应结构 → OpenAPI → 策略引擎 → API 版本管理）

**可以并行**:
- {A-0, C-0, D-P0, E-1} — 四个独立 P0 工作流，无交叉依赖
- {A-1, B-1, C-1, D-P1} — 四个独立 P1 工作流
- {B-2, E-2} — 两个 P2 工作流

---

## 6. 总结

### 核心结论

1. **当前架构质量高**，但存在两个关键的架构债务需要优先偿还：`gin.H` 无类型响应和双包结构不一致。

2. **分析文档的 5 个方向整体合理**，但我的优先级修正建议是：
   - 将"响应模型统一化"从方向 1 拆出并提升到 P0
   - 将方向 4（IaC）分解为 P0/P1/P2 子项目
   - 降低方向 3（策略引擎）的技术方案门槛，采用渐进策略
   - 简化方向 5（API 治理），暂缓 Pact 等高成本方案

3. **最快能产生价值的三项行动**（可在 1-2 周内完成）：
   - 在 Makefile + CI 中加入 `buf lint` 和 `buf breaking`（2 天）
   - 为 idgen、FTS、SSE 添加 `_benchmark_test.go`（3 天）
   - 启动 Helm Chart 编写（5 天出初版）

4. **最需要谨慎执行的事项**：
   - `gin.H` → struct 重构（需要高测试覆盖率保护）
   - 策略引擎引入（避免过度工程化）
   - API 版本管理（避免版本爆炸）

### 对分析文档的回应

关于用户提供的交叉验证意见（方向 1 与方向 4 可以并行）：

**完全赞同**。方向 1（API 产品化）和方向 4（IaC）的冲突点为零。它们分别解决不同 Persona 的痛点：IaC 让 SRE/运维团队能部署 aero-id，API 产品化让应用开发者能集成 aero-id。两个工作流完全可以由不同的人并行推进。

实际上，**方向 1 的响应模型统一化（A-0）前置条件不影响 IaC，IaC 也不依赖 API 文档**。两者并行还能产生协同效应：有了 Helm Chart 后，外部团队可以一键部署 + 一键尝试 API，形成完整的"评估体验"。
