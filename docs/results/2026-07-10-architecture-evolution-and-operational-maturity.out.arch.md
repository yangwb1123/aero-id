现在我已经掌握了完整上下文。以下是我的架构分析。

---

# 架构师视角：对《架构演进与运维成熟度》分析文档的深入审阅

## 审阅基础

分析对象：`docs/requirements/2026-07-10-architecture-evolution-and-operational-maturity.md`（808 行）
系统设计基础：`docs/superpowers/specs/2026-05-22-aero-id-design.md`
工程约束框架：`AGENTS.md`
当前代码规模：1,441 个 `.go` 文件，~253K 行，38 个 gRPC RPC + 30+ HTTP 端点，22 份迁移，3400+ 测试

---

## 1. 架构评估：对原文档五个方向的评价

### 1.1 整体评价

原文档的整体质量很高。五个方向的选题抓住了 aero-id 从"功能完整"走向"平台级成熟"的关键缺口。以下从架构师视角对每个方向的深度、完整性和潜在盲区做评价。

---

### 1.2 方向一：API 消费者契约与开发者体验

**强度**：★★★★★（五个方向中最扎实）

- 证据链完整：从 router 注册机制的`RouteRegistrar`到限流 middleware 缺乏响应头，到错误码无注册表，每一处都指向同一个核心问题——API 没有机器可读的契约管理。
- 提议的 OpenAPI 生成策略（proto → OpenAPI v2 + 人工补丁 HTTP-only 端点）是务实的。这避免了手写 OpenAPI 与代码脱节的问题。
- 四个子维度的覆盖（OpenAPI 规范、限流头、版本策略、错误码注册表）完整且正交。

**一个值得商榷的决策**：

> 策略 A（从 proto 自动生成 OpenAPI）作为唯一来源。

虽然节省维护，但有一个本质问题：**Proto 定义的是 gRPC 契约，并非 HTTP 契约**。HTTP 端点有额外的关注点——状态码映射（是 200 还是 201？）、缓存策略、认证方式（Bearer vs Cookie）、错误响应格式。这些在 proto 中没有表达力。

**建议修正**：采用混合策略——OpenAPI 3.1 作为"规范来源"（authoritative source），但通过工具链部分自动化：
- 使用 `protoc-gen-openapiv2` 生成基线
- 在 `openapi/overlays/` 目录下手写 [OpenAPI Overlay](https://spec.openapis.org/overlays/latest.html) 补充 HTTP-specific 元数据
- CI 检查：proto 变更 → 重新生成基线 overlay → 验证 overlay 兼容

这样既减少了手工维护量，又保留了 OpenAPI 3.1 的表达力。

**一个缺口**：原文档没有讨论 **API 文档托管和发布**。OpenAPI 规范生成了，但开发者从哪里访问？建议在部署管线中集成：
- CI 将 OpenAPI 规范发布到内部开发者门户（Backstage / Stoplight）
- 或者托管在 `/docs/openapi.json`（通过 Gin 静态文件服务，仅内部网络可访问）

---

### 1.3 方向二：生产级数据迁移与 Schema 演化架构

**强度**：★★★★☆

- 代码级证据非常具体（`applyTx` 的事务封装、`CREATE INDEX CONCURRENTLY` 不支持、无 `lock_timeout`、无 `/readyz` 迁移中降级）。
- 安全迁移模式清单（Add Column Nullable、Add Column + Backfill、Create Index Concurrently、Rename Column、Multi-Phase）是行业标准实践。
- 迁移中 `/readyz` 降级的提议是亮点——大多数项目直到第一次生产事故才想到这一点。

**两个盲区**：

**盲区 1：回滚的版本管理策略**

原文档讨论了回滚测试自动化，但没有讨论一个更根本的问题：**当迁移 000022 执行后发现 bug，应该回滚 000022 还是发布 000023 来修复？**

两种策略各有权衡：

| 策略 | 适用场景 | 风险 |
|------|---------|------|
| 回滚（000022 down） | 刚刚部署，数据量小 | 如果 000022 包含数据迁移（`UPDATE ... SET ...`），回滚会丢失数据 |
| 前滚（000023 修复） | 已上线一段时间，数据量大 | 需要额外写修复迁移，可能涉及数据修复逻辑 |

**建议**：在迁移规范中明确记录每条迁移的"回滚风险等级"：
- `ROLLBACK_SAFE`：仅 DDL 添加（列/索引），无数据变更
- `ROLLBACK_DESTRUCTIVE`：包含数据迁移或列删除，回滚将丢失数据
- 在 CI 的 `migrate validate` 中检查并输出风险等级

**盲区 2：跨版本部署兼容性**

aero-id 当前迁移在启动时执行（`auto_migrate`）或通过 CLI。但在零停机部署中，新旧两个版本的进程可能同时运行，它们需要兼容同一个 Schema。

考虑场景：新版本部署了迁移 000023（`ALTER TABLE files ADD COLUMN storage_class TEXT`），但旧版本进程还在运行——它们不知道 `storage_class` 列的存在。如果旧版本进程执行 `INSERT INTO files (...) SELECT ...` 会失败。

**建议**：建立灰度兼容性检查清单：
- 新增列必须 `NULLABLE` 或有 `DEFAULT`
- 新版本之前至少一次部署周期不要求新列 `NOT NULL`
- `CREATE INDEX CONCURRENTLY` 必须在事务外执行
- 列删除必须使用双写策略（旧列保留 2+ 部署周期）

原文档没有覆盖这个场景，但在多副本部署中是必须的。

---

### 1.4 方向三：分布式测试与质量保障基础设施

**强度**：★★★☆☆

- 证据链集中在"缺少什么测试类型"（benchmark、race、chaos、fuzz），代码引用合理。
- 混沌测试的具体测试用例（SSO 断路器、SSO 完全宕机 5 分钟）选择得当，聚焦在系统最脆弱的依赖链上。

**两个根本性问题未讨论**：

**问题 1：测试维护成本的增长曲线**

aero-id 已有 3400+ 个测试。新增 benchmark、fuzz、chaos 测试套件后，CI 时间会显著增长：

| 测试类型 | 预估 CI 时间增量 | 频率建议 |
|---------|-----------------|---------|
| `go test -race` | +2-3 分钟（全包） | 每次 push |
| 基准测试（每个包） | +30 秒-2 分钟 | 每周或 pre-release |
| 混沌测试 | +3-5 分钟 | per-release 或 nightly |
| Fuzz 测试 | +2-10 分钟（取决于 fuzz 时间） | nightly |

建议建立 **测试分层 CI 策略**：
- **Gate**（每次 push，< 10 分钟）：`test` + `lint` + `test-race`（仅核心包）
- **Quality**（每晚，< 30 分钟）：`test-integration` + `bench`（比较 baseline）+ `fuzz`（短周期）
- **Release**（tag 触发，< 1 小时）：全量集成 + 混沌 + fuzz（长周期）

**问题 2：Mock 与现实的不一致风险**

原文档提到混沌测试使用 "mocksso"——但 mock 只能模拟预设的故障模式。实际的 SSO 服务可能有 unexpected 行为（部分超时、返回错误但 body 为空、TLS 握手间歇失败等）。

建议：**在混沌测试套件中包含一层"故障注入代理"**——在 SSO gRPC 连接之前插入一个可以注入延迟、错误、乱序响应的代理层，而不是仅在 mock 层面模拟。这比 mock 更真实。

---

### 1.5 方向四：运行时可观测性与自主运维框架

**强度**：★★★★☆

- 最突出的洞察是：当前可观测性聚焦于**技术健康**（RED）但缺失**业务健康**。这一区分对运维成熟度至关重要。
- 业务 RED 指标层的提议（`messages_sent_total`、`files_uploaded_total` 等）成本低收益高。
- SLO 框架的引入填补了从"指标"到"目标"的鸿沟。
- 成本归因虽然是好主意，但在当前阶段是否有足够的用户量使其有价值？如果 DAU < 100，成本归因的 ROI 不高。

**一个重要的结构性缺失**：**告警策略**

原文档讨论了 SLO 和 error budget，但没有讨论**什么时候告警、告警给谁、告警以什么方式**。可观测性如果没有配套的告警体系，只是一堆好看的仪表盘。

建议补充：
- **告警分级的明确定义**：P0（服务宕机/核心功能不可用）→ P1（SLO 违反）→ P2（容量预警/趋势异常）
- **告警路由策略**：P0 → On-call（电话/短信），P1 → Slack，P2 → 周报
- **告警静默和抑制**：避免告警风暴（如 SSO 故障触发 50 个告警）
- **runbook 自动关联**：每条告警链接到对应的标准操作流程

---

### 1.6 方向五：架构演进治理与适应度函数

**强度**：★★★★★

- 这是五个方向中最有前瞻性的。大多数项目在"代码能跑"阶段不会考虑适应度函数。
- 代码级证据深入（`AGENTS.md` 只有静态约束、Suite A/B 双实现无解释、`redis/go-redis` 死依赖、`DECISIONS.md` 无日期和替代方案分析）。
- 提议的适应度函数（验证 handler 不导入 repository、验证所有 gRPC 方法有 HTTP 对应等）精准地覆盖了当前架构文档中约定的但未强制执行的关键约束。

**一个实际挑战**：适应度函数的维护成本

适应度函数使用 `go/packages` + AST 遍历。Go 的类型系统使得 import 路径检查相对直接，但以下场景会很脆弱：
- 重构时包名变更 → 适应度函数需要同步更新
- 临时绕过（`// archtest:skip`）堆积 → 适应度函数失去效力

建议：
- 适应度函数设计为 **增量添加，定期审查**——不要一次性全部实现，优先覆盖**最近发生过违反的约束**
- 使用 `// archtest:skip` 日志审计：每周运行一次 `grep -r "archtest:skip"`，超过 3 个活跃 skip 走技术债流程
- 考虑将架构适应度函数与 ADR（架构决策记录）关联——每个 ADR 可以附带 1-2 个适应度函数，当 ADR 被标记为 deprecated 时，对应的适应度函数自动标记为待移除

---

## 2. 扩展方向：补充分析

原文档的五个方向覆盖全面。但作为补充，我认为还有 **3 个高价值方向未被系统性覆盖**：

### 2.1 存储架构的冷热分层与生命周期管理

**为什么需要**：aero-id 管理文件存储，但当前所有文件存储在单一后端（local 或 s3）。随着用户量和文件量的增长，存储成本将成为运营开支的主要组成部分。

**核心挑战**：
- 冷热数据识别策略：最后访问时间、文件大小、文件类型
- 分层迁移对用户无影响（下载时透明重定向）
- 大文件（>100MB）和小文件（<1KB）的存储成本结构不同
- 合规性：某些文件可能需要保留特定时长

**架构变更**：
```
Current: FileStorage interface → {local, s3}
Future:  FileStorage interface → {hot, warm, cold} layer
                                    ├── hot: local SSD (低延迟, 高成本)
                                    ├── warm: S3 Standard (中延迟, 中成本)
                                    └── cold: S3 Glacier / 本地归档 (高延迟, 低成本)
         + StorageTierManager (后台 goroutine, 定期评估迁移标准)
         + StoragePolicy (配置冷热阈值、保留期限)
```

**影响**：
- 对 service 层影响小：FileStorage 接口不变，新增 `tier` 字段在响应中
- 对 repository 层影响小：`files` 表新增 `storage_tier` 和 `last_accessed_at` 列
- 需要新增后台调度器来处理分层迁移

### 2.2 多集群/多区域部署拓扑

**为什么需要**：当前架构假设单区域部署。如果 aero-id 需要服务于全球用户（或跨区域灾备），数据就近性和高可用性需要重新设计。

**核心挑战**：
- 跨区域数据一致性：PostgreSQL 逻辑复制 vs 应用层双写
- SSO 服务的区域亲和性：SSO 是否有区域部署？
- 文件存储的区域性：S3 跨区域复制
- 流量路由策略：基于用户地理位置 DNS 路由

**架构变更**：
```
Current: 单区域 deploy → 单 Postgres → 单 S3 bucket
Future:   区域 A (primary) → Postgres A → S3 A
          区域 B (read replica) → Postgres B (逻辑复制) → S3 B (CRR)
          ├── 写入路由到 primary
          ├── 读取路由到最近区域
          ├── gRPC 服务间通信通过区域端点
          └── 区域故障切换 → DNS 切换 → 应用层重试
```

**影响**：
- 这部分**不急**——在用户量达到跨区域规模之前（10 万 DAU+），单区域足够
- 但需要在架构决策中留下扩展点：gRPC 连接支持多端点、Postgres 连接串支持读写分离
- 建议在 `config.yaml` 中声明式预留多区域配置结构，即时当前只用 default 区域

### 2.3 平台级配额与用量计量系统

**为什么需要**：aero-id 从功能角度已有 rate limiting，但缺少**资源配额管理**——用户可以上传多少文件、发送多少消息、消耗多少存储空间。这在企业级场景（按用户/按团队/按租户计费）中不可或缺。

**核心挑战**：
- 配额类型：存储容量、消息数量/频率、评论数量、API 调用次数
- 配额周期：天、月、总量
- 配额超额行为：拒绝、降级（如压缩图片）、计费
- 配额计算的一致性：事务内计数 vs 异步计数（最终一致性）
- 管理层级：系统默认 → 用户级别 → 租户级别

**架构变更**：
```
新增模块：
  ├── internal/quota/ — 配额定义和验证接口
  │   ├── QuotaStore 接口（DB 或基于 outbox 的事件计数）
  │   ├── QuotaValidator（预检查：写入前验证）
  │   └── QuotaExhausted 错误码
  └── internal/quota/counter/ — 计数实现
      ├── RelationalCounter（实时、一致性高、DB 压力大）
      └── OutboxCounter（最终一致性、扩展性好）

影响范围：
  ├── service layer: 每个写操作前调用 quota.Validate()
  ├── repository: 新增 quotas 表和 usage_logs 表
  ├── handler: 新增 GET /v1/quota（用户查看自己的配额使用）
  └── config: 新增 quota.defaults.* 配置节
```

**建议优先级**：P2 — 在第一个企业客户提出需求之前预留接口，但不急于实现。

---

## 3. 接口设计建议

### 3.1 当前接口设计质量评估

整体良好。`internal/service/` 作为业务逻辑的唯一入口是正确决策。但存在几处可以改进的设计问题：

### 3.2 关键接口改进建议

#### 3.2.1 FileStorage 接口的演进方向

当前接口（基于代码分析推断）：

```go
type FileStorage interface {
    Upload(ctx context.Context, key string, reader io.Reader) error
    Download(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error)
}
```

**问题**：这个接口假设了"文件即字节流"，没有考虑：
- 元数据（Content-Type、Content-Disposition）
- 存储属性（加密、存储层级、版本）
- 多部分上传（大文件分片）

**建议演化**：

```go
type FileMeta struct {
    Key           string
    ContentType   string
    ContentLength int64
    ETag          string
    StorageTier   StorageTier // hot | warm | cold
    Encrypted     bool
}

type FileStorage interface {
    Upload(ctx context.Context, key string, reader io.Reader, opts ...UploadOption) (*FileMeta, error)
    Download(ctx context.Context, key string) (io.ReadCloser, *FileMeta, error)
    Delete(ctx context.Context, key string) error
    PresignGet(ctx context.Context, key string, expiry time.Duration) (string, error)
    Head(ctx context.Context, key string) (*FileMeta, error)             // 新增
    Copy(ctx context.Context, srcKey, dstKey string) error               // 新增
    List(ctx context.Context, prefix string) ([]FileMeta, error)         // 新增
    MultiPartUpload(ctx context.Context, key string, opts ...UploadOption) MultiPartUploader // 新增
}
```

返回 `*FileMeta` 而非仅 `error`，让调用方无需额外 `Head` 调用即可获取元数据。`UploadOption` 使用 functional options 模式保持向后兼容。

#### 3.2.2 是否需要新的抽象层？

**需要：健康检查链的抽象层**

当前 `/readyz` 检查 DB + SSO。提议扩展为依赖链检查。需要引入一个接口：

```go
// internal/health/checker.go
type Checker interface {
    Name() string
    Check(ctx context.Context) (*CheckResult, error)
}

type CheckResult struct {
    Status   HealthStatus // healthy | degraded | down
    Latency  time.Duration
    Detail   string
    Since    time.Time
    Metadata map[string]interface{}
}
```

然后让 `internal/app/app.go` 中的启动流程注册多个 Checker：`DBHealthChecker`、`SSOHealthChecker`、`StorageHealthChecker`、`MigrationChecker`。`/readyz` 聚合所有 Checker 的结果。

#### 3.2.3 向后兼容性策略

对于当前提案中的所有接口变更，遵循以下原则：

1. **Additive only**：新增接口或方法，不修改现有方法签名
2. **Optional parameters**：使用 functional options 或 `With*` 模式
3. **Deprecation header**：在被废弃的响应中添加 `X-API-Deprecated` 头
4. **Version coexistence**：新旧 endpoint 在至少一个部署周期内并存
5. **Default behavior**：未配置的新特性使用安全的默认值（如 storage tier 默认 `hot`）

---

## 4. 技术选型评估

### 4.1 现有技术栈的健康度评估

| 技术 | 版本 | 健康度 | 备注 |
|------|------|--------|------|
| Go | 1.26 | ✅ | 最新稳定版 |
| Gin | v1.10.0 | ✅ | 合理，但原设计文档曾标注"out of scope"的 Prometheus/metrics 已在 CHANGELOG 中实现——证明项目在演进 |
| GORM | v1.25.11 | ⚠️ | GORM v2 最新稳定版，但项目已出现 Suite A/B 双实现，可能有 GORM 的滥用或瓶颈 |
| gRPC | v1.80.0 | ✅ | 合理 |
| Postgres driver | pgx v5 | ✅ | 行业标准 |
| testcontainers | v0.32.0 | ✅ | 集成测试必备 |
| AWS SDK v2 | v1.30.4 | ✅ | Go 1.26 兼容 |
| OTel | v1.43.0 | ✅ | 最新版 |
| Viper | v1.19.0 | ✅ | 配置管理合理 |

**关注点：GORM 的健康评估**

当前系统中 `redis/go-redis` 出现在 `go.mod` 但未使用（原文档指出），说明存在**死依赖**。同样需要检查 GORM 的使用是否有类似问题：
- GORM 的 `datatypes` 包被 `import`，但 JSONB 字段可能已通过其他方式处理
- 建议运行 `go mod tidy` 并在 CI 中添加 `go mod verify` 步骤

### 4.2 是否需要引入新技术？

基于原文档五个方向的评估：

| 方向 | 建议引入 | 现有替代 | 推荐 |
|------|---------|---------|------|
| API 消费者契约 | OpenAPI 工具链 | 无 | ✅ 引入 `protoc-gen-openapiv2` + `spectral` |
| 生产级迁移 | `golang-migrate` 已存在 | — | 不需要新技术，改进现有工具使用方式 |
| 分布式测试 | 测试工具已在 `go test` 中 | — | 不需要新技术 |
| 可观测性 | Prometheus 已存在 | — | 不需要新技术，新增指标注册 |
| 架构治理 | 仅标准库 | — | 不需要新技术 |

**结论**：五个方向均不需要引入新的重量级技术栈。所有改进都可以在现有 Go 标准库 + Prometheus 客户端 + protoc 插件 + Makefile 的框架内完成。

### 4.3 自建 vs 采购的决策框架

虽然当前所有方向都是自建，但建议建立正式的决策框架以指导未来的选择：

| 决策维度 | 自建门槛 | 采购门槛 |
|---------|---------|---------|
| **核心差异化** | ✅ 自建 | ❌ 避免采购（失去控制权） |
| **与现有架构的集成深度** | 高（需要自定义适配层）→ 自建 | 低（简单 API 调用）→ 可采购 |
| **社区成熟度** | 无成熟替代或替代品质量差 | ✅ 有成熟 SaaS/开源替代 |
| **维护成本** | 预期长期维护成本 < 采购费用 | ⚠️ 采购通常是更好的经济选择 |
| **合规要求** | 数据无法离境 / 需要审计所有操作 | 供应商有合规认证 |

**示例应用**：
- API 文档站点：**采购**（Backstage / Stoplight / SwaggerHub）比自建 GitBook 站点更经济
- SLO 告警：**利用现有** Prometheus + Alertmanager，不自建告警平台
- 混沌测试：**自建**（基于 testcontainers + Go 标准库），因为集成度高、代码量小

---

## 5. 实施路线图审阅与优化

### 5.1 对原文档路线图的评价

原文档的路线图（Q3 2026 → Q1 2027 分三期）整体合理，P1/P2 划分也符合直觉。但有几个**依赖关系和时序问题**需要调整：

### 5.2 优化后的建议路线图

```
Phase 0: Foundation (2 周)               ← 原文档中缺失，建议新增
├── go mod tidy → 移除死依赖
├── go test -race 加入 CI gate
├── 限流头标准化 (X-RateLimit-*)
├── 版本头中间件
└── 错误码注册表 (registry.go)

Phase 1: API Maturity + Migration Safety (3 周)    ← P1
├── OpenAPI 生成管线 (proto → OpenAPI v2 → overlay)
├── 安全迁移模式清单 + migrate validate 子命令
├── 迁移中 /readyz 降级
└── 业务 RED 指标层 (messages_sent_total 等)

Phase 2: Observability Foundations (3 周)          ← P1 (原文档为 Q3-Q4)
├── 依赖健康详细端点 /admin/health/detail
├── 存储使用趋势 gauge
├── SLO 框架 (search_p99_latency 为首个 SLO)
└── 错误预算计量面板 (Grafana)

Phase 3: Quality Infrastructure (3 周)              ← P2
├── 核心路径基准测试 (搜索、收件箱、outbox drain)
├── SSO 断路器混沌测试
├── API 输入 fuzz 测试
└── 架构适应度函数 x3 (handler→repository 边界验证)

Phase 4: Governance & Scale (持续)                  ← P2
├── ADR 模板 + DECISIONS.md 迁移
├── 技术债寄存器
├── 混沌测试扩展 (端到端弹性验证)
└── 存储生命周期管理 (冷热分层)    ← 本文新增
```

### 5.3 关键依赖关系修正

原文档的依赖关系基本正确，但有三个遗漏：

1. **业务指标层（方向四 Phase 1）→ 依赖 SLO 框架（方向四 Phase 2）**：严格来说业务指标是 SLO 的输入，但不需要等到 SLO 框架完成——计数器独立于 SLO 框架，可以并行推进。**可以并行生产**。

2. **错误码注册表（方向一 Phase 1）→ 依赖 OpenAPI 规范（方向一 Phase 1）**：错误码应该先于 OpenAPI 完成，因为 OpenAPI 规范需要引用错误码的枚举值。**错误码注册表应先于 OpenAPI**（1-2 天前置）。

3. **混沌测试（方向三 Phase 1）→ SSO mock（已存在）**：原文档正确，但混沌测试还需要 **testcontainers 中的故障注入能力**——需要评估现有 testcontainers 环境是否支持。建议先在 Phase 2 （而非 Phase 1）启动混沌测试，留出时间准备故障注入代理。

### 5.4 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| OpenAPI 与代码不同步（方向一） | 中 | 高 | CI 中加入 `proto-diff` + `openapi-lint` 检查：proto 变更后必须重新生成 OpenAPI |
| 迁移锁表导致生产事故（方向二） | 低-中 | 极高 | 在 Phase 0 即使现迁移安全审查——不等工具链完成，从下一个迁移开始人工审查 |
| 混沌测试误伤测试 DB（方向三） | 低 | 中 | 混沌测试使用独立的 Postgres 实例（testcontainers），不共享 |
| SLO 框架过度工程化（方向四） | 中 | 低 | 限制 SLO 框架的初始范围：只支持 P99 延迟 + 成功率两类 SLI，不一开始就支持复杂指标 |
| 适应度函数维护成本超过收益（方向五） | 低-中 | 中 | **增量**策略：第一个 sprint 只实现 3 个最高价值的适应度函数，运行 2 周后评估效果再决定是否扩展 |
| Suite A/B 双实现清理被推迟 | 高 | 中 | 在 Phase 4 的 ADR 迁移中明确指定一个套件为"active"、一个为"deprecated"，并禁止对被弃用套件的新开发 |
| 团队能力瓶颈（所有方向同时推进） | 中 | 高 | 两个开发者并行最多只能同时推进 2 个方向。建议 Phase 0-1 集中 2 人，Phase 2-3 扩展至 3 人 |

---

## 总结

这份《架构演进与运维成熟度》分析文档整体达到资深架构师水准。五个方向的选题精准、证据链完整、方案可执行。我的主要建议可以概括为五个关键调整：

| # | 建议 | 针对方向 | 性质 |
|---|------|---------|------|
| 1 | OpenAPI 策略从"纯自动生成"调整为"混合策略（自动基线 + 手动 overlay）" | 一 | 方案修正 |
| 2 | 迁移管理中补充"回滚风险等级"和"跨版本兼容性" | 二 | 补充完善 |
| 3 | 引入测试分层 CI 策略（Gate / Quality / Release），而非全部归入 push 触发 | 三 | 策略建议 |
| 4 | 补充告警策略体系（分级、路由、抑制、runbook）| 四 | 补充完善 |
| 5 | 路线图新增 Phase 0（基础设施清理）| 全局 | 时序修正 |

以上五个调整不改变原文档的核心结构——五个方向、优先级划分、技术方案都成立。它们只是在边缘细节上让文档更鲁棒。
