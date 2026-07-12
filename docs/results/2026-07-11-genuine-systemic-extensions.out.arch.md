# 架构分析：aero-id 系统性扩展方向扫描

> **分析者**：资深架构师  
> **输入文档**：`docs/requirements/2026-07-11-systematic-expansion-direction-scan.md`  
> **代码基线**：commit `80b013e`（生产加固轮完成后的最成熟版本）  
> **分析方法**：文档 5 个方向 + AGENTS.md 已知约束 + 代码库既有架构 + 行业模式对比

---

## 1. 架构评估

### 1.1 当前架构的优势

aero-id 在经历了 ROADMAP 全部功能和生产加固轮后，已经拥有几个值得肯定的架构特征：

- **严格单向依赖**：`handler → service → repository → Postgres`。这是领域驱动设计中"依赖倒置"的务实落地。layered architecture 最怕的就是跨层调用和循环依赖——当前代码库完全没有这个问题。
- **双传输层共享 service**：HTTP 和 gRPC 共享同一份 `service` 层。这不仅保证了行为一致性，也意味着任何新业务逻辑只需在 service 中实现一次，两个传输层自动受益。
- **Outbox 模式的正确使用**：把写操作和 side-effect（通知、推送、webhook）解耦，这在一个消息/评论系统中是"正确的事"。很多项目在早期直接同步推送，后期被迫重构成 outbox——aero-id 一开始就对了。
- **存储抽象**：`FileStorage` 接口支持 local（HMAC 签名 URL）和 s3（MinIO），这是基础设施可变性的正确封装。
- **SSO 集成带断路器 + stale-cache + singleflight**：在依赖外部服务的边界做了充分的容错设计，说明团队已经有过"上游抖动拖垮下游"的实战教训。

### 1.2 关键设计决策的合理性复盘

| 决策 | 是否合理 | 隐含代价 |
|------|---------|---------|
| SSO 持有所有身份，零 account 表 | ✅ 正确 | 所有用户枚举、离线授权都要通过 gRPC |
| UUIDv7 作为 PK + 游标分页 | ✅ 正确 | 排序依赖天然时间序，但限制了自定义排序 |
| 软删除 + GORM `DeletedAt` | ✅ 合理 | 查询需要 `WHERE deleted_at IS NULL`，逃不过 |
| 迁移不自动应用 | ❌ 有争议 | 生产中 CI/CD 会自动跑迁移，本地手动跑是摩擦 |
| Outbox relay 每轮都用 `context.Background()` | ⚠️ 可理解但有问题 | 这正是方向五的病根——当初省事，现在要还债 |

### 1.3 架构债务和技术债

按严重程度排列：

**架构层面债务**：

1. **延迟任务能力空洞**（方向一）——不是需要修的东西，而是从来没建过的东西。这是架构最显著的"空白格子"。一个基础设施没有延迟执行能力，其**时间维度的表达能力为零**。
2. **搜索只有"单一维度"**（方向二）——搜索被设计成了 `query + kind + page` 的三元组。没有 scope、没有 facet、没有 filter。这在 MVP 阶段是合理的（先让用户能搜到东西），但到了"最成熟版本"阶段已经是产品瓶颈。
3. **运维可观测性在异步边界断裂**（方向五）——这不是一个紧急 bug，但它是"隐形技术债"：系统不报错、不告警，但当需要排障时，trace 缺失会直接转化为平均故障恢复时间（MTTR）的翻倍。

**技术债务**：

1. **无 HTTP API 契约**（方向三）——38 条路由散布在 8 个 handler 文件中，没有一份 OpenAPI 规范。这是典型的"用 gin 做快速开发，但没补文档"的遗留问题。对于内部服务而言优先级较低，但一旦有外部消费者就是阻塞项。
2. **手动请求参数解析**——每个 handler 中 `c.Query()` 和 `strconv.Atoi()` 散布，没有统一的 schema 校验层。这增加了 handler 的 boilerplate 和出错概率。
3. **健康检查浅层化**（方向四）——`sqlDB.Ping` 和 `SSO.HealthCheck` 只能验证"连接未被断开"，不能验证"业务路径可用"。这是几乎所有 Go 服务的通病，但通病也是债。

### 1.4 我关注的一个结构性问题

这五个方向中，**方向一（延迟任务）和方向四（合成探针）存在潜在的架构冲突**：

- 延迟任务框架需要一个 `scheduled_jobs` 表 + 轮询 worker
- 合成探针也需要轮询 + 执行一系列操作

如果分别实现，你会得到两套独立的"定时执行"基础设施——同样的轮询逻辑、同样的重试机制、同样的 metrics 暴露重复实现两遍。

**架构决策点**：延迟任务框架是否可以泛化为合成探针的基础设施？即，合成探针是否可以"注册为一个特殊的延迟任务 handler"？如果可以，这就避免了两套轮询基础设施。

---

## 2. 扩展方向（重构建议）

以下是我从架构角度对这个五个方向的重新梳理和优先级排列。我将它们归类为三个层次：**基础设施层**（影响所有业务）、**产品体验层**（影响用户价值）、**运维治理层**（影响运营效率）。

---

### 2.1 【P0-基础设施】统一延迟执行引擎（Unified Delayed Execution Engine）

**为什么需要**

这是当前架构中最根本的能力缺失。当前系统的表达能力是**即时**（同步 API）+ **周期**（ticker-based 后台作业），但缺失了**定时**（在未来确定时间点执行）。这个能力空洞的影响范围远不止"消息定时发送"：

- 如果未来要实现**文件 TTL 自动删除**，你需要定时任务
- 如果未来要实现**邀请过期自动撤销**，你需要定时任务
- 如果未来要实现**订阅过期自动降级/续费提醒**，你需要定时任务
- 如果未来要实现**下班后延迟投递**，你需要定时任务
- 如果未来要实现**相册回收站自动清空**，你需要定时任务

这些都是产品需求，但它们全部卡在同一个基础设施能力上。

但更关键的架构洞察是：**合成事务探针（方向四）本质上也依赖定时执行**。如果有了统一延迟执行引擎，合成探针可以作为该引擎的一个特殊用途来构建，而不是作为一套独立的基础设施。

**核心技术挑战**

| 挑战 | 说明 |
|------|------|
| **轮询 vs 推送** | 数据库轮询（如当前 outbox 做法）实现简单但有延迟，且轮询频率与数据库负载成反比。在低延迟（秒级）和低负载之间需要权衡 |
| **至少一次语义** | worker 崩溃后重启，正在执行的 job 可能被重新执行。需要幂等性或 `status` 原子更新 |
| **时间精度** | 对于"30 分钟后撤回"，精度到分钟已经足够；对于"晚 8 点准时发送"，精度到秒。需要明确精度要求 |
| **cron vs 一次性 job** | 定期清理（每夜 3 点）和单次延迟（30 分钟后撤回）是两种不同的调度模式，但底层存储可以统一 |

**建议的架构决策**

```
核心接口设计（不写代码，只描述签名语义）：

Interface: JobStore
  - Insert(ctx, job) error           // 注册一个延迟 job
  - ClaimDue(ctx, limit) ([]Job, error) // 原子性地获取到期 job（FOR UPDATE SKIP LOCKED）
  - MarkDone(ctx, id) error
  - MarkFailed(ctx, id, err) error
  - Delay(ctx, id, until time.Time) error // 延期（重试时用）

Interface: JobHandler
  - Handle(ctx, job) error          // 每个 job_type 注册一个 handler

Worker loop（复用 outbox.Relay 的轮询模式，但基于 scheduled_at 而非 created_at）:
  - 每 N 秒轮询 ClaimDue
  - 分发到对应的 JobHandler
  - 成功 → MarkDone；失败 → MarkFailed + 可选 Delay（指数退避）
```

**权衡选项**

| 方案 | 优点 | 缺点 |
|------|------|------|
| **Postgres 轮询**（建议采用） | 零额外依赖，复用现有 DB 连接池和事务保证，与 outbox 模式一致 | 轮询延迟不可控（秒级），DB 负载增加，不适合高频（毫秒级）任务 |
| **Redis 有序集合（ZSET）** | 低延迟（毫秒级），`ZRANGEBYSCORE` 天然适合 | 引入新依赖，持久化需额外保证（RDB/AOF），事务保证弱于 Postgres |
| **外部调度器（如 Sidekiq/Celery 风格）** | 职责分离，语言无关 | 引入重量级依赖（Redis + worker 进程），运维复杂度增加 |

**我的建议**：**Postgres 轮询**，至少第一阶段如此。原因：
1. 与 outbox relay 的架构模式一致——团队已有经验
2. 零额外依赖——当前最重要的约束是保持 aero-id 的部署复杂度可控
3. 产品需求的时间精度在秒到分钟级，Postgres 轮询完全满足
4. 未来如果出现毫秒级需求，可以在 Postgres 上层加 Redis 加速层，而不需要推翻重来

**对现有系统的影响**

- 新表 `scheduled_jobs`（迁移 000023）
- 新包 `internal/jobs/`（scheduler + store + handler registry）
- 与 outbox 共享：重试逻辑、metrics 计数（`jobs_scheduled_total`、`jobs_duration_seconds`、`jobs_errors_total`）
- 可选的产物：方向四的合成探针注册为 `job_type = "synthetic.probe"` 的周期性 job

**向后兼容性**：完全无影响。现有系统没有一个领域操作使用延迟语义，所以新增能力的零风险。

---

### 2.2 【P1-产品体验】搜索能力分层演进

**为什么需要**

当前搜索是"一次性全表匹配"。随着用户数据增长，搜索体验会从"勉强能用"退化为"基本不可用"。这不是危言耸听——当用户在一个会话中有数千条消息时，无范围限定的全文搜索返回的结果集太大，用户体验极差。

同时，搜索是整个系统中**唯一的信息发现入口**（没有 tag 体系、没有分类树、没有图数据库），它的能力直接决定了用户能找到什么。

**核心技术挑战**

1. **复合索引设计**——FTS 的 `tsvector` 索引是一个倒排索引，但 `WHERE to_tsvector(...) @@ plainto_tsquery(...) AND conversation_id = ?` 这种带过滤条件的 FTS 查询，Postgres 只能选择用 FTS 索引或 conversation_id 的 B-tree 索引，不能同时用两者（除非用 `tsvector` + `conversation_id` 的复合索引但需要精心设计）。
2. **分面聚合性能**——在搜索结果上同时做 `GROUP BY type` 和 `GROUP BY date` 的分面聚合，意味着同一个 `tsquery` 要跑多次 `COUNT`。在结果集大的时候，这是额外代价。
3. **搜索建议**——`pg_trgm` 或 `LIKE` 的前缀匹配在大数据量下性能差，可能需要单独的表或搜索引擎。

**建议的分层演进**

```
Layer 1: 作用域过滤（当前迭代，低成本）
  - SearchRequest 增加可选字段：scope_type, scope_id, since, until, kind 扩展子类型
  - Service 层根据 scope 生成附加 WHERE 条件
  - 索引策略：为每个 scope 场景评估是否需要复合索引
  
Layer 2: 分面聚合（中期，中等成本）
  - 搜索结果附带 facets（按 type、按日期、按发送者）
  - 同一个 tsquery + WHERE 条件下的 COUNT 聚合
  - 前端可以展示"在消息中找到 12 条，在文件中找到 3 条"
  
Layer 3: 增强搜索（长期，高成本）
  - 搜索建议（prefix query + pg_trgm 或外部搜索引擎）
  - 搜索历史（search_history 表）
  - 二次搜索（在前次结果集合上增加过滤条件）
  - 如果 Layer 1 + 2 后性能仍不满足，考虑迁移到 Elasticsearch/Meilisearch
```

**架构决策点**

是否引入外部搜索引擎（ES/Meilisearch/Typesense）？

| 方案 | 场景 | 成本 |
|------|------|------|
| **Postgres FTS 一路走到黑** | 数据量 < 1 亿行，查询 QPS < 1000 | 零额外依赖，但索引膨胀和写入性能会下降 |
| **引入轻量搜索引擎**（Meilisearch/Typesense） | 搜索是核心用户体验，延迟要求高 | 新运维依赖，但搜索体验质的飞跃 |

**我的建议**：先做 Layer 1（作用域过滤），评估性能。如果用户在 5 万条消息的会话中搜索延迟 < 200ms，则 Postgres 够用。如果延迟 > 500ms，再考虑搜索引擎。

**对现有系统的影响**

- Proto 变更：`SearchRequest` 增加 `scope_type`, `scope_id`, `since`, `until` 等
- Repository 层：当前搜索 SQL 是独立的，扩展 WHERE 条件不破坏现有逻辑
- FTS 索引：可能需要新增复合索引（如 `conversation_id` + `tsvector`）
- **向后兼容性**：`scope_type` 等新字段均为可选，未设置时行为完全不变

---

### 2.3 【P1-运维】合成事务探针作为延迟执行引擎的消费者

这是我对方向四的关键架构建议：**不要构建独立的探针轮询系统，而是将其建立在延迟执行引擎之上**。

**设计思路**

```
延迟执行引擎                   合成探针
┌──────────────┐            ┌──────────────────┐
│ JobScheduler │◄──────────│ SyntheticProber  │
│              │  注册为    │ (JobHandler 实现)│
│ 轮询到期 job │  周期性job │                  │
│ → 调用 handler│           │ 1. auth-as-user  │
└──────────────┘            │ 2. upload-file   │
                            │ 3. get-file      │
                            │ 4. delete-file   │
                            │ 5. search        │
                            │ 6. check-outbox  │
                            └──────────────────┘
```

**为什么这是更好的架构**

1. **避免两套轮询基础设施**——探针需要"每 60 秒执行一次"，延迟框架天然支持周期性 job
2. **一致的重试和 metrics**——探针失败的重试、告警、metrics 全部复用 `jobs` 包的机制
3. **可观测性**——探针执行本身会被 trace（受益于方向五的 context 传播）

**对现有系统的影响**

- 不需要 `SyntheticProber` 的独立轮询 goroutine
- 只需在 `app.go` 中注册一个特殊的 `job_type = "synthetic.probe"` handler
- 配置项：`health.synthetic.enabled`, `health.synthetic.interval`, `health.synthetic.credentials`
- **关键设计原则**：合成探针使用隔离的监控服务账户 uid（如 `admin:monitor`），所有操作在该 uid 的作用域内，不污染真实用户数据
- 探针结果暴露为 Prometheus metrics：`health_synthetic_duration_seconds`, `health_synthetic_success_total`, `health_synthetic_error_total`

---

### 2.4 【P2-API 治理】API Schema Contract 的温和引入

**为什么需要**

从架构师的视角看，这个问题的重要程度**取决于系统边界**：
- 如果 aero-id 是一个纯粹的"内部服务"，所有消费者是同一组织的其他后端服务（通过 gRPC），那么这个方向的优先级应为 P3
- 如果 aero-id 未来会开放给前端 SPA、移动端、第三方开发者（通过 HTTP），则优先级应为 P1

文档中将其标为 P2，我认为**在当前阶段是合适的**。但架构师需要设定一个触发条件：**当第一个外部消费者出现时，自动升为 P1**。

**建议的渐进式方案**

```
Phase A（当前迭代，低成本）:
  - 为 HTTP handler 增加请求/响应的结构体类型定义文档（godoc）
  - 确认 gRPC proto 与 HTTP handler 的行为同步（手动 review）
  - 建立 API 变更审查流程：任何 handler 的响应格式变更需团队评审

Phase B（中期，中等成本）:
  - 编写 OpenAPI 3.1 规范（只覆盖核心路由，如消息/文件/评论 CRUD，约 15-20 条）
  - 引入 schema 驱动请求校验（Gin 的 binding 中间件或用 OpenAPI 代码生成）
  - CI 检查 OpenAPI 规范与 handler 实现是否脱节

Phase C（长期，高成本）:
  - Consumer-Driven Contract Tests
  - 自动生成 HTTP 客户端 SDK（基于 OpenAPI）
  - 破坏性变更自动检测
```

**对现有系统的影响**

- **Phase A 影响为 0**——只是文档和流程变化
- **Phase B 影响 handler 代码**——引入 binding 中间件可以使 `c.Query("limit")` 自动校验为 `int` 且 `> 0`，消除手动 `strconv.Atoi`
- **向后兼容性**：OpenAPI 规范编写时以**当前行为**为准，不做任何改变

**一个重要权衡**：是否要将 gRPC proto 作为"单一事实源"（使用 `google.api.http` annotations）？

| 方案 | 优点 | 缺点 |
|------|------|------|
| **proto 作为单一源 + grpc-gateway** | 一份定义，双端生成 | 引入 grpc-gateway 运行时，改变 HTTP 层的实现方式 |
| **proto + 独立 OpenAPI** | HTTP 层不需要改变 | 两份定义可能漂移，手动维护成本高 |

**我的建议**：当前阶段选择**独立 OpenAPI**。原因：
1. 引入 grpc-gateway 意味着改变 HTTP handler 的整个工作方式——从 Gin 的 `c.Query()` 变成 gRPC 的自动绑定，这是一个较大的重构
2. Gin 已经深度整合，重写 HTTP 层的 ROI 在当前阶段不明确
3. 独立 OpenAPI 可以与现有的 Gin handler 共存，只需要写文档，不需要改代码

---

### 2.5 【P2-可观测性】异步 Context 传播——小成本、大收益

**为什么需要**

这是五个方向中**成本最低、但调试收益最直接**的变更。Outbox relay 使用 `context.Background()` 意味着：当运维人员打开 Jaeger/Tempo 查看一条消息的整个生命周期时，**trace 在 outbox 边界断裂**。前面是 API 调用的 trace，后面是后台作业的 orphan span。

在出故障时，trace 断裂意味着排障人员需要用时间戳手动关联日志，这在低频场景下尚可忍受，在高频场景下（每秒数百条消息）几乎不可能。

**核心挑战**

技术难度很低——核心是三步：
1. 在 outbox event 的创建点捕获 `trace_id` + `span_id`
2. 在 outbox relay 消费时恢复 `parent span context`
3. 在消费端点（如 SSE push、webhook dispatch）中正确结束 span

真正的挑战是**一致性**：不是所有的 outbox event 创建点都有 `context.Context`（极少数系统边界可能没有），需要兜底策略——没有 trace 上下文就用 `context.Background()`，行为不变。

**对现有系统的影响**

- Schema 变更：`ALTER TABLE outbox_events ADD COLUMN trace_context JSONB`
- 结构体变更：OutboxEvent 新增 `TraceContext *TraceCarrier` 字段
- 写入点（所有 `ssoProxy.AppendEvent` 调用）都有 `ctx`，不需要修改调用签名
- 消费点（outbox relay）需要恢复上下文
- **向后兼容性**：旧 event 的 `trace_context` 为 NULL，relay 处理时回退到 `context.Background()`——完全兼容

---

## 3. 接口设计建议

### 3.1 延迟执行引擎的关键抽象边界

我绘制了一个核心模块划分（非代码，是接口边界定义）：

```
┌─────────────────────────────────────────────────────────────────┐
│                    延迟执行引擎边界定义                            │
│                                                                 │
│  ┌─────────────┐   ┌──────────────┐   ┌────────────────────┐   │
│  │ JobScheduler │──→│  JobStore    │   │  Handler Registry │   │
│  │ (轮询循环)   │   │ (DB层)       │   │ (job_type → impl) │   │
│  └──────┬──────┘   └──────────────┘   └────────┬───────────┘   │
│         │                                       │                │
│         └─────────────── 调用 ──────────────────┘                │
│                                                                 │
│  Handler 的注册方（业务方）：                                    │
│    - jobs.RegisterHandler("message.recall", recallHandler)      │
│    - jobs.RegisterHandler("synthetic.probe", probeHandler)      │
│    - jobs.RegisterHandler("file.ttl-cleanup", cleanupHandler)   │
└─────────────────────────────────────────────────────────────────┘
```

**关键设计原则**：

1. **JobStore 是唯一的数据层抽象**——JobScheduler 不直接操作 DB，通过 JobStore 接口。使得未来更换存储（如迁移到 Redis）只需要实现一个新 JobStore。
2. **Handler 是无状态的**——handler 接收 `(ctx, job) error`，不应持有状态。所有上下文通过 job payload 传递。
3. **Job payload 是 JSONB**——不定义强类型的 Go struct 绑定。这使得跨版本兼容更简单（旧版 worker 反序列化新版 payload 时忽略未知字段）。
4. **周期性 job 和一次性 job 在存储层面统一**——周期性 job 是"handler 执行完成后，根据调度策略重新注册一个新的一次性 job"。

### 3.2 搜索模块的接口演进

当前搜索的 `SearchRequest` 定义：

```go
type SearchRequest struct {
    Query string
    Kind  string   // "all" | "messages" | "comments" | "files"
    Page  domain.Page
}
```

演进后的建议：

```go
type SearchRequest struct {
    Query     string        // 搜索关键词
    Kind      string        // 实体类型（可选，默认 "all"）
    
    // --- Layer 1 新增：作用域过滤 ---
    ScopeType string        // "conversation" | "target" | "file" （可选）
    ScopeID   string        // 对应的资源 ID（可选）
    
    // --- Layer 1 新增：时间范围过滤 ---
    Since     *time.Time    // 最小创建时间（可选）
    Until     *time.Time    // 最大创建时间（可选）
    
    // --- Layer 1 新增：实体属性过滤 ---
    FileType  string        // "image" | "document" | "video"（可选，仅当 kind=files）
    FromUID   string        // 消息/评论发送者（可选）
    
    // --- Layer 2 新增：分面聚合控制 ---
    Facets    *FacetConfig  // 需要哪些分面（可选，默认空）
    
    Page      domain.Page   // 分页（保持不变）
}
```

**向后兼容性**：所有新字段都是可选（pointer 或空 string）。未设置时，行为与当前完全一致。

### 3.3 合成探针的关键决策点

探针的 **credential 管理** 是一个关键设计决策：

| 方案 | 优点 | 缺点 |
|------|------|------|
| **固定监控服务账户 token** | 实现简单，与 SSO token 校验流程相通 | token 会过期，需要刷新逻辑 |
| **SSO 的 machine-to-machine token** | 符合 SSO 的设计哲学，长期维护成本低 | 需要 SSO 端支持 client credentials flow |
| **特殊的内置 token（绕过 SSO）** | 不依赖 SSO 可用性（避免循环依赖） | 引入特殊 auth 路径，增加攻击面 |

**我的建议**：采用**固定监控服务账户 token**，并与 SSO 协调将 token 设置为"永不过期或超长有效期"。同时，配置支持定期轮换。

**循环依赖问题**：如果合成探针依赖 SSO 来验证 token，当 SSO 宕机时探针会失败——但这正是探针想要发现的。正确行为是：探针失败 → metric 暴露 → 告警触发 → 运维介入。

---

## 4. 技术选型

### 4.1 是否需要引入新技术栈

分析五个方向的技术需求与当前技术栈的匹配度：

| 方向 | 当前栈是否够用 | 是否需要引入 | 建议 |
|------|--------------|-------------|------|
| 延迟任务 | ✅ Postgres 轮询 | **不需要** | 复用 outbox relay 模式 |
| 作用域搜索 | ✅ Postgres FTS | **不需要** | 先用复合索引，性能不够再评估 |
| API Contract | ✅ 无（文档） | **不需要** | OpenAPI YAML 文件即可，无运行时依赖 |
| 合成探针 | ✅ 现有 | **不需要** | 建立在延迟执行引擎之上 |
| Async Context | ✅ OpenTelemetry | **不需要** | 只需 outbox schema 增加 JSONB 列 |

**结论**：五个方向都不需要引入新的运行时依赖（无 Redis、无 ES、无消息队列、无新数据库）。这是非常高的"投入产出比"。

### 4.2 如果未来需要引入外部搜索引擎的标准

虽然当前建议用 Postgres FTS 一路走到黑，但作为架构师，应该设定**触发条件**和**评估标准**：

**触发引入搜索引擎的条件**（满足任意一项即应启动评估）：
1. `Search` P99 延迟 > 1s（当前 Postgres FTS 路径下）
2. 单个 `search_queries` 表的行数 > 1000 万条（或 FTS 索引大小 > 10GB）
3. 搜索 QPS > 500（需要更多连接处理搜索查询）
4. 需要全文搜索之外的搜索能力（如拼音搜索、纠错、同义词、排序规则等）

**候选方案评估矩阵**：

| 方案 | 运维复杂度 | 一致性保证 | 查询延迟 | Go 客户端成熟度 |
|------|-----------|-----------|---------|---------------|
| Elasticsearch | ⭐⭐⭐ | 最终一致 | <10ms | go-elasticsearch 活跃 |
| Meilisearch | ⭐⭐ | 最终一致 | <5ms | meilisearch-go 活跃 |
| Typesense | ⭐⭐ | 最终一致 | <5ms | typesense-go 活跃 |
| Postgres FTS | ⭐（零额外） | 强一致 | <200ms | 原生 database/sql |

### 4.3 自建 vs 采购的决策框架

对于 aero-id 这种内部基础设施服务，决策原则是：

1. **核心差异化能力**（如业务逻辑）——自建
2. **通用基础设施**（如搜索、队列）——优先用 Postgres 能力，不引入新依赖
3. **只有到达性能瓶颈**后才考虑外部专用系统

**适用于当前五个方向**：

| 能力 | 决策 | 理由 |
|------|------|------|
| 延迟任务 | 自建（利用 Postgres） | 核心差异化需求，Postgres 轮询足够 |
| 搜索 | 先自建（Postgres FTS），设触发条件 | 非核心差异化搜索需求，但性能可预测 |
| API 文档 | 自建（写 YAML） | 不需要运行时组件 |
| 合成探针 | 自建（注册为 job handler） | 业务逻辑与系统紧密耦合 |
| 异步 trace | 自建（schema 变更 + OTel SDK） | 纯运维能力，不需要独立系统 |

---

## 5. 实施路线图

### 5.1 优先级重排

文档中的优先级排序（三个 P1 + 两个 P2）整体合理。我基于架构耦合关系做了微调：

```
迭代 N+1（当前迭代）
├── ✅ 延迟执行引擎（基础设施能力空洞，多个产品需求的阻塞依赖）
├── ✅ 异步 Context 传播（低成本、高收益、完全独立）
│
迭代 N+2
├── ✅ 作用域搜索 Layer 1（Scope 过滤 + 时间范围）
├── ✅ 合成探针（建立在延迟执行引擎之上，作为验证）
│
迭代 N+3 及以后
├── 🔄 搜索 Layer 2（分面聚合）
├── 🔄 API Schema Contract Phase A（文档 + 流程）
├── 🔄 搜索 Layer 3（搜索增强——仅当需要时）
```

**微调理由**：
1. 异步 Context 传播从 P2 建议移到 N+1——因为**成本极低**（一个 JSONB 列 + 一行 OTel API 调用）但**调试收益直接**。这种高 ROI 的小改动不应该等到 P2 迭代。
2. 合成探针从"并行 P1"建议调整为 N+2——因为它最适合**建立在延迟执行引擎上线之后**，作为"吃自己的狗粮"（dogfooding）来验证引擎的可用性。
3. API Schema Contract 保持在 P2——除非外部消费者出现。

### 5.2 阶段划分和里程碑

**Phase 1：基础设施夯实（预计 1-2 周）**

```
Milestone 1.1: 延迟执行引擎上线
  - scheduled_jobs 表迁移（000023）
  - internal/jobs/ 包（JobStore + JobScheduler + Handler 注册）
  - 复用 outbox relay 的轮询 + metrics 模式
  - 迁移 000024: outbox_events 增加 trace_context JSONB 列

Milestone 1.2: Context 传播贯穿
  - OutboxEvent 结构体增加 TraceContext 字段
  - 写入 outbox 时捕获 span context
  - Outbox relay 消费时恢复 parent span
  - 验证：在 Jaeger/Tempo 中可以看到完整 trace
```

**Phase 2：产品增强 + 运维加固（预计 2-3 周）**

```
Milestone 2.1: 作用域搜索上线
  - SearchRequest proto 扩展（scope_type, scope_id, since, until）
  - Repository 层扩展搜索 SQL 的 WHERE 条件生成
  - 复合索引评估和创建
  - 验证：搜索"张三上周发的文件"

Milestone 2.2: 合成探针上线
  - 将 synthetic probe handler 注册到延迟执行引擎
  - 监控服务账户 credential 配置
  - metrics 暴露（health_synthetic_*）
  - 告警规则配置
```

**Phase 3：精细化和开放（按需，不作 deadline）**

```
Milestone 3.1: 搜索分面聚合
Milestone 3.2: OpenAPI 规范初版
Milestone 3.3: Consumer-Driven Contract Tests
```

### 5.3 风险点和缓解策略

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|---------|
| **延迟执行引擎与 outbox 模式重叠** | 中 | 架构混乱 | 明确职责边界：outbox 处理的是"事件 → handler"（已发生的领域事件），延迟任务处理的是"时间 → job"（计划中的未来操作）。前者是事件溯源中的过程管理，后者是定时调度。两者存储和语义不同 |
| **搜索性能退化** | 中-高 | 用户体验下降 | Layer 1 上线后即添加 P99 search 延迟监控；设置触发条件（延迟 > 500ms）自动评估外部搜索引擎 |
| **合成探针产生垃圾数据** | 低 | 存储浪费 | 探针使用隔离的监控 uid，所有操作定义完整的 tear-down 步骤；清理失败应触发告警 |
| **trace_context 存储膨胀** | 低 | 存储成本 | JSONB 列大小约 100 字节/行，以 outbox_events 的 TTL（通常 7 天）为生命周期，影响可忽略 |
| **团队分散投入导致半成品** | 中 | 多个方向都做了一半 | **"一次只做一件事"**：Phase 1 完成后再开始 Phase 2。不要在 Phase 1 未完成时并行启动 Phase 2 任务 |

---

## 6. 总结性洞察

### 6.1 最关键的架构决定

这五个方向中，**延迟执行引擎是唯一一个具备"架构杠杆效应"的决定**——它不仅是方向一本身的价值，也是方向四（合成探针）的基础设施。从这个意义上说，它是最值得投入的方向。

### 6.2 文档的五个方向诊断——评估

我通读扫描文档后认为：这份扫描的诊断质量很高。五个方向全部命中了真实的能力缺口，没有"为了有五个方向而凑数"的痕迹。尤其值得肯定的是：

- **提供了代码级证据**（从具体 Go 文件中引用了 `context.Background()` 的行号），这使得它不是空谈
- **与 70+ 既有文档零重复**，说明方法是严格的
- **每个方向都有明确的影响范围评估**，没有忽略对既有系统的冲击

### 6.3 一个我关心的未提及问题

文档没有提到**五个方向带来的运维模型变化**：

- 延迟执行引擎的运行意味着 `jobs` 表需要监控——`pending` 数、`failed` 数、最大延迟时间
- 合成探针需要告警规则（`health_synthetic_success_total` 为 0 持续 N 分钟 → PagerDuty）
- 作用域搜索上线后，需要关注 `seq_page_read` 的增长（复合索引是否被正确使用）

这些不是代码变更，但作为架构师，运维模型的变更应该被写进"DoD"（Definition of Done）：**"该功能上线后，对应的监控面板是否已更新？告警规则是否已添加？"**

---

*以上分析基于文档 `docs/requirements/2026-07-11-systematic-expansion-direction-scan.md` 以及 aero-id 代码库在当前 commit 的已知架构。所有建议均不涉及具体代码实现，聚焦于架构层面。*
