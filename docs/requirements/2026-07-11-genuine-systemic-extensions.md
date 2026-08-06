# aero-id — 系统性扩展方向扫描

> **日期**: 2026-07-11  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 逐文件全量代码扫描（`internal/` 全 22 个包、`migrations/` 22 份 SQL、`cmd/`、`configs/`、`api/proto/`、`Makefile`、`docs/`），与 `docs/requirements/` 下 **70+ 份既有分析**、`docs/REVIEW_*` 下 **8 份**审查文档、`ROADMAP.md` 全部方向逐条交叉比对，确认以下 5 个方向**在所有既有分析中零系统性覆盖**。  
> **约束**: 不编写任何代码。  
> **前置**: 代码库已交付 ROADMAP 全部功能 + 生产加固轮全部任务。当前 `main`（commit `80b013e`）是 aero-id 截至目前最成熟的版本。

---

## 为何跳过已被反复讨论的主题

70+ 份既有文档已覆盖的方向包括（不完全列表）：群聊、分布式限速、文件后处理管线、数据生命周期与合规、移动推送、Block 穿透文件分享、机器可读错误分类、会话隐式派生、双栈技术债、通知限流键表增长、SSE Hub 瓶颈、管理员 API 不完整、文件分享无过期、软删除恢复、评论点赞计数器漂移、SSO 证书固定、HTTP 超时中间件、Outbox 优雅关闭、SSE 实时推送、审计日志、全文搜索、消息 Reactions、统一 gRPC 校验器、消息已读回执、SSO 断路器 + stale-cache + singleflight、文件流上传 + 大小限制 + MIME 校验 + 内容去重 + 直传预签名 + 存储 GC、事务性 Outbox + 幂等键、通知 + 未读数、评论举报/审核/隐藏、用户屏蔽、文件分享、指标/Tracing/健康检查、Rate limiting、gRPC ops surface、迁移 CLI、消息编辑、评论编辑、管理员角色…… 累计约 **120+ 独立方向**。

本文不重复上述任何内容。

---

## 五个系统性扩展方向

| # | 方向 | 类别 | 优先级 | 核心原因 |
|---|------|------|--------|----------|
| 1 | **通用延迟任务与定时作业框架**（Deferred/Scheduled Job Framework） | 基础设施 | P1 | 系统没有"在未来某个时间点做某事"的机制；所有操作都是即时的 |
| 2 | **资源级作用域搜索与跨实体聚合查询**（Scoped Search & Cross-Entity Aggregation） | 产品 | P1 | 当前搜索是全局的，无法限定搜索范围或跨表聚合 |
| 3 | **API Schema Contract 与 Consumer-Driven Contract Testing**（API Contract & CDC） | 流程/质量 | P2 | 双传输层、38+ RPC 没有一份正式 API 契约，消费者无法独立演进 |
| 4 | **合成事务探针——端到端健康监控**（Synthetic Transaction Probes） | 运维 | P1 | 健康检查只测连接，不测业务路径——真正的"可用"含义更广 |
| 5 | **异步管线可观测性——Context 传播穿透后台作业**（Async Context Propagation） | 运维/调试 | P2 | Outbox relay、GC sweep、后台定时器全部使用 `context.Background()`，导致追踪链断裂 |

---

## 方向一：通用延迟任务与定时作业框架（Deferred/Scheduled Job Framework）

### 代码级证据

当前，系统中有且仅有四种"异步"机制，**没有一种支持"在未来某个确定时间执行"**：

```go
// 机制 1：Outbox Relay —— 立即执行，无延迟语义
// internal/outbox/relay.go
func (r *Relay) Start(pollInterval time.Duration) func() {
    for _, e := range events {      // 轮询到后立即调用 handler
        r.handler.Handle(ctx, e)    // ← 没有 DelayUntil，没有 ScheduledAt
    }
}

// 机制 2：GC Sweeper —— 周期性执行，但不可由用户触发
// internal/storage/gc/sweeper.go
func (s *Sweeper) Start(interval time.Duration) func() {
    // ← 固定间隔，不可动态调度单次执行
}

// 机制 3：定时器（Cleaner、Rate Limit 清理等）—— 内部 Ticker
// internal/pkg/ratelimit/ratelimit.go
func (l *Limiter) RunCleaner(every time.Duration) func() {
    // ← 时间间隔硬编码在启动时传入，不可按需触发
}

// 机制 4：HTTP/gRPC handler —— 即时同步
// 所有业务操作（SendMessage、CreateComment、DeleteFile）都是同步完成
```

没有任何表或队列用于存储"延迟任务"：

```bash
$ grep -r "cron\|schedule\|delay\|defer\|timer\|ScheduledAt\|DelayUntil" internal/ --include='*.go' | grep -v _test | grep -v mock | wc -l
# → 0
```

这意味着以下需求当前**完全无法实现**：

| 业务场景 | 所需机制 |
|----------|---------|
| "30 分钟后撤回这条消息" | 延迟任务 `scheduleAt = now + 30m` |
| "5 分钟后发送定时消息" | 延迟任务 `scheduleAt = now + 5m` |
| "文件 7 天后自动删除" | 用户级 TTL `deleteAt = created_at + 7d` |
| "晚 8 点静默时段延迟发送" | 检查时间 + 自动 `scheduleAt = nextNonQuiet` |
| "定期数据归档/备份" | cron 表达式触发 |
| "用户邀请过期（48h 后自动撤销）" | 定时检查 `invite_expires_at` |

### 为什么需要它

从 **架构角度**，缺少通用延迟任务框架是当前基础设施最显著的"能力空洞"：

1. **产品功能制约**：消息撤回超时、自动销毁、定时发送等功能全部卡在这个能力上
2. **业务逻辑耦合**：当前所有的延迟需求只能通过外部定时器或 hack 解决（比如用 `time.AfterFunc` 做简单的延迟）
3. **扩展性**：如果今后要实现相册回收站自动清空、会话过期自动关闭、订阅过期自动降级等功能，都需要延迟任务

### 建议方案

延迟任务框架的核心设计要素：

```
┌──────────────────────────────────────────────────┐
│  scheduled_jobs 表                               │
│  id UUID PRIMARY KEY                              │
│  owner_uid TEXT          -- 谁创建的              │
│  job_type TEXT           -- "message.recall" 等   │
│  payload JSONB          -- 执行上下文             │
│  scheduled_at TIMESTAMPTZ -- 目标执行时间          │
│  status SMALLINT         -- pending/running/done  │
│  retry_count INT         -- 失败重试次数           │
│  created_at / updated_at                          │
│                                                   │
│  索引: (status, scheduled_at ASC) -- 轮询            │
└──────────────────────────────────────────────────┘
```

实现路径：

1. **新增 `scheduled_jobs` 表**（迁移 000023）
2. **实现 `JobScheduler` 包**（类似 `outbox.Relay` 但基于 `scheduled_at`）
3. **复用 Outbox 的 Worker 模式**：轮询到期的 job → 调用 handler → 标记 done/重试
4. **为 job 注册 handler**：job_type → Handler 映射（类似 outbox.Chain）
5. **API 端点**：`POST /v1/jobs` 允许系统/管理员创建延迟任务

第一阶段不需要 cron 表达式——一个带 `scheduled_at` 的简单轮询足够覆盖 80% 的需求。

### 影响范围

- 新包：`internal/jobs/`（scheduler + handler 接口 + repository）
- 复用 infrastructure：outbox 的轮询模式、重试机制、metrics 计数
- 产品收益：消息定时发送、自动撤回、自动清理 全部成为可能

---

## 方向二：资源级作用域搜索与跨实体聚合查询（Scoped Search & Cross-Entity Aggregation）

### 代码级证据

当前搜索实现（`internal/search/`）是**全局 + 单实体类型选择**：

```go
// internal/search/search.go
type SearchRequest struct {
    Query string   // 关键词
    Kind  string   // "all" | "messages" | "comments" | "files" → 选择实体类型
    Page  domain.Page
}

// internal/search/handler_http.go
func (h *SearchHandler) Search(c *gin.Context) {
    // 解析 query + kind + page
    // 调用 service.Search(ctx, req)
    // 返回全局匹配结果
}
```

数据库层的搜索是**独立的全表查询**：

```go
// internal/repository/search_integration_test.go
// message FTS:  WHERE to_tsvector('simple', content) @@ plainto_tsquery('simple', $1)
//               AND from_uid = $2 OR to_uid = $2   -- 仅限参与者
// comment FTS:  WHERE to_tsvector('simple', content) @@ plainto_tsquery('simple', $1)
//               AND ... (target 存在且用户可读)
// file FTS:     WHERE to_tsvector('simple', name || ' ' || COALESCE(meta->>'description', ''))
//               @@ plainto_tsquery('simple', $1)
```

**搜索能力缺失清单**：

| 场景 | 当前是否支持 |
|------|-------------|
| "搜索某个文件下的所有评论" | ❌ —— 搜索没有 `scope_type`/`scope_id` 参数 |
| "搜索我与张三的对话" | ❌ —— 搜索范围是全局，不能限定到特定会话 |
| "搜索最近 7 天的消息" | ❌ —— 搜索没有时间范围参数 |
| "搜索同时包含图片附件的文件" | ❌ —— 搜索不处理组合条件 |
| "按文件大小/类型过滤搜索结果" | ❌ —— 搜索只有关键词 |
| "在搜索结果中继续搜索（二次搜索）" | ❌ —— 搜索是独立的，不支持结果内过滤 |

### 为什么需要它

从 **产品角度**，全表搜索在数据量小的时候够用，但随着用户数据增长，用户需要**缩小搜索范围**来快速定位：

- "找到张三上周发给我的那个文件"——需要会话限定 + 时间范围 + 文件类型
- "在这个项目相关文档中搜索'合同'"——需要 scope 限定

从 **架构角度**，当前搜索是"一次性"设计（one-shot query），没有考虑：
- **搜索建议**（search-as-you-type suggestions）
- **搜索结果聚合**（按文件类型/日期分组的 facet count）
- **搜索历史**（用户最近搜索记录，方便重复搜索）

### 建议方案

分两到三阶段演进：

**Phase 1 — 作用域参数扩展**（低成本，高收益）
- 为搜索请求增加可选参数：`scope_type`（`"conversation"`/`"file"`/`"target"`）+ `scope_id`
- service 层解析作用域后生成对应的 SQL WHERE 条件
- 增加 `since`/`until` 时间范围参数
- 增加 `file_type`（`"image"`/`"document"`/`"video"`）过滤参数

**Phase 2 — 聚合/分面**（中等成本）
- 搜索结果附带 `facets`：`{"types": {"messages": 12, "files": 3, "comments": 8}}`
- 附带时间分布：`{"by_date": {"2026-07-10": 5, "2026-07-11": 18}}`
- 这些 facets 来自同一个 `tsquery` 的 `COUNT(*) ... GROUP BY` 查询

**Phase 3 — 搜索增强**（高成本，需要评估性能）
- 搜索建议：`prefix` 查询 + tsearch 的 `LIKE` / `pg_trgm` 支持
- 搜索历史：新增 `search_history` 表 + 自动记录 + 显示最近 N 条
- 二次搜索：在前一次结果的 `id` 集合上添加新的过滤条件

### 影响范围

- `internal/search/` 包的接口定义和实现需要扩展
- HTTP/gRPC 的请求/响应 proto 需要新增字段
- FTS 索引可能需新增（如时间范围的 B-tree + tsearch 复合索引）
- proto 变更（`SearchRequest` 增加 `scope_type`/`scope_id`/`since`/`until` 等）

---

## 方向三：API Schema Contract 与 Consumer-Driven Contract Testing（API Contract & CDC）

### 代码级证据

尽管 aero-id 通过 protobuf 定义了 gRPC 接口，但它**不存在一份完整的、版本化的 HTTP API 契约**：

```bash
# HTTP 路由定义
$ grep -c "r\.(Auth|Public)\.\(GET\|POST\|DELETE\|PATCH\)" internal/handler/http/*.go
# → ~38 条路由，跨 8 个 handler 文件，散布在各注册器中

# 没有 OpenAPI / Swagger 规范
$ find . -name "*.yaml" -o -name "*.yml" -o -name "*.json" | xargs grep -l "openapi\|swagger\|paths:" 2>/dev/null
# → 0 结果
```

proto 文件定义了 gRPC RPC 但**不描述 HTTP 路由、请求格式、响应结构、错误码**：

```protobuf
// api/proto/aero_id/v1/aero_id.proto
service AeroID {
  rpc ListFiles(ListFilesRequest) returns (ListFilesResponse);
  rpc SendMessage(SendMessageRequest) returns (Message);
  // ...
  // 但 HTTP 的 multipart 上传、query 参数分页、响应 JSON 结构
  // 没有任何 proto 或 schema 描述
}
```

HTTP handler 中的请求/响应绑定是**手动且隐式**的：

```go
// internal/handler/http/comment.go
func (h *CommentHandler) List(c *gin.Context) {
    targetType := c.Query("target_type")        // 手动解析
    targetID, _ := idgen.Parse(c.Query("target_id")) // 手动解析
    limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
    cursor := c.Query("cursor")
    // ← 没有 schema 约束、没有自动校验、没有文档
}
```

这意味着：

1. **HTTP API 没有版本化文档**——新开发者只能读代码或抓包理解接口
2. **没有消费者契约测试**——如果修改了某个 JSON 字段名或格式，现有客户端会静默损坏，且 CI 不会发现
3. **gRPC 和 HTTP 的契约不一致**——验证请求时出错可能有不同类型的错误、不同的响应结构（HTTP 是 JSON、gRPC 是 status.Error），但已知的功能保持一致；然而没有自动化工具确保它们不会漂移
4. **外部无法代码生成客户端**——没有 OpenAPI 规范，无法生成 Go/Python/TypeScript SDK

### 为什么需要它

从 **架构师角度**，这是一个**可扩展性负债**而不是功能缺失：

1. **消费者独立性**：当有外部团队或生态应用需要调用 aero-id API 时，没有 schema 意味着他们必须手动解析 HTTP 响应——这与 gRPC 的代码生成体验形成鲜明对比
2. **变更风险管理**：HTTP API 的响应格式变更无法被自动化检测为破坏性变更
3. **API 治理**：双传输同构的理念很好，但缺乏一个"规范"来确保它们真正保持同步
4. **开发者体验**：设计评审、API 答疑、文档生成全部靠口口相传

### 建议方案

利用已有的 proto 定义，增加自动化的 HTTP API 契约：

**Step 1 — protobuf 作为单一事实源**
- 在 proto 中增加 `google.api.http` annotation（grpc-gateway 风格）但不需要引入 grpc-gateway
- 或者新建 `api/http/v1/aero_id.yaml` OpenAPI 3.1 规范，手工维护并用之验证 handler

**Step 2 — schema 驱动请求校验**
- 引入 Gin 的 binding 中间件，根据 OpenAPI schema 自动验证请求参数
- 消除 handler 中的手动 `strconv.Atoi` 和 `c.Query` 解析

**Step 3 — Consumer-Driven Contract Tests**
- 编写一组合约测试（pact.io / 自建），代表已知消费者的期望
- CI 中运行，确保每次修改不会破坏现有消费者

### 影响范围

- 新增（或生成）`api/openapi/` 目录下的 OpenAPI 规范
- handler 中的请求解析代码可能被简化为结构体绑定
- CI 流水线新增契约验证步骤
- 对新消费者的 SDK 生成有利

---

## 方向四：合成事务探针——端到端健康监控（Synthetic Transaction Probes）

### 代码级证据

当前健康检查（`/healthz`、`/readyz`）是**组件级连通性探测**，不反映真正的服务可用性：

```go
// internal/handler/http/health.go
func (h *HealthHandler) Ready(c *gin.Context) {
    for _, check := range h.checks {
        if err := check.Probe(ctx); err != nil {
            // check.Name = "database" → sqlDB.PingContext
            // check.Name = "sso"     → ssoClient.HealthCheck
        }
    }
    // ← 只检查基础设施连接，不验证业务功能是否正常
}
```

一个典型的"false health"场景：

```
DB 正常 👍  →  /readyz → 200
SSO 正常 👍 →  /readyz → 200
  ↓
但: token cache 已损坏（脏数据导致所有请求返回 403）
但: outbox relay 已死锁（新通知无法发送）
但: 存储后端磁盘已满（文件上传返回 500）
但: 限速器内存泄漏（OOM 边缘）

负载均衡器看到的是 "健康" → 继续发送流量
```

**这在实际生产系统中是常见的事故模式**——组件级健康检查通过，但端到端业务路径失败。

### 现有可观测工具的局限性

```go
// internal/middleware/metrics.go — 只记录请求指标，不主动探测
// internal/middleware/tracing.go — 只在真实请求中生成 trace
// sso.Client.HealthCheck() — 只发送一个空的 gRPC 请求验证连接

// 没有任何代码是"模拟一个真实用户操作并验证完整流程"
```

### 为什么需要它

从 **运维角度**，合成监控是生产基础设施的**最后一道防线**：

1. **发现沉默故障**：如存储写入但不返回错误但数据丢失、SSO 返回成功但 uid 错误、计数一致性问题——这些只有通过"写入→读取→验证"的合成事务才能发现
2. **SLO 验证**：P99 延迟、可用性 99.9% 等 SLO 需要主动探测，不能只依赖用户流量
3. **回滚触发器**：部署后的自动验证——跑一遍合成事务，失败则阻止继续分发流量
4. **容量规划的基线数据**：合成事务的延迟趋势可以反映系统退化，比被动指标早数小时发现

### 建议方案

设计一个轻量级的合成事务框架：

```
合成事务          →  预期结果
─────────────────────────────────────────
1. 用测试 token 认证  →  200，拿到 uid
2. 上传一个文件      →  201，文件元数据返回
3. 获取文件元数据    →  200，与上传的一致
4. 删除文件          →  204
5. 获取文件再次      →  404（已删除）
6. 调用搜索          →  200，包含预期结果
7. 检查 outbox lag   →  lag < 可接受值
8. 检查存储 GC 进度  →  无过期数据残留
```

实现路径：

1. 在 `internal/handler/http/` 中增加 `/healthz/synthetic` 端点
2. 在 `app.go` 中注入一个轻量级的 `SyntheticProber`
3. 该探针使用特殊的"监控服务账户"的 SSO token（由 SSO 为监控目的签发）
4. 探针的 token、监控 uid 等通过配置注入（`health.synthetic.uid`, `health.synthetic.token`）
5. 探针在后台定期运行，结果暴露为 metrics（`health_synthetic_*`）
6. **关键设计原则**：合成数据使用独立的存储隔离空间（如特殊的 `monitor_` 前缀 uid），不污染真实用户数据
7. 合成事务应包含清理步骤，不能留垃圾数据

### 影响范围

- 新包：`internal/healthz/synthetic.go`（如果有更多健康检查逻辑）或直接在 `health.go` 中扩展
- 配置：新增 `health.synthetic.enabled`、`health.synthetic.token`、`health.synthetic.interval` 等
- metrics：新增 `health.synthetic.{operation}.duration`、`.success`、`.error` 系列
- 独立于真实用户数据路径，不影响生产流量

---

## 方向五：异步管线可观测性——Context 传播穿透后台作业（Async Context Propagation）

### 代码级证据

这是最隐蔽的问题——所有后台作业使用 `context.Background()`，导致 **OpenTelemetry trace 被割裂**：

```go
// internal/outbox/relay.go
func (r *Relay) Start(pollInterval time.Duration) func() {
    ticker := time.NewTicker(pollInterval)
    go func() {
        for {
            select {
            case <-ticker.C:
                r.fetchAndHandle()
            case <-stop:
                return
            }
        }
    }()
}

func (r *Relay) fetchAndHandle() {
    ctx := context.Background()  // ← trace 上下文丢失！
    events, err := r.repo.FetchUnpublished(ctx, r.batchSize)
    for _, e := range events {
        err := r.handler.Handle(ctx, e)  // ← 此处无 trace 父子关系
    }
}

// internal/storage/gc/sweeper.go
func (s *Sweeper) Start(interval time.Duration) func() {
    go func() {
        for {
            select {
            case <-ticker.C:
                s.sweep(context.Background())  // ← trace 上下文丢失！
            }
        }
    }()
}

// internal/pkg/ratelimit/ratelimit.go (RunCleaner)
func (l *Limiter) RunCleaner(every time.Duration) func() {
    go func() {
        for {
            select {
            case <-ticker.C:
                l.clean()  // ← 无 trace 上下文
            }
        }
    }()
}

// internal/handler/http/events.go (SSE 长连接)
// 每个 SSE 推送是单独的 goroutine，推送时没有 trace 上下文
```

**结果是什么？** 假设用户张三发送一条消息：

1. HTTP request → `POST /v1/messages` ✅ trace 生成（`span A`）
2. Service 写入 outbox event ✅ （`span A-1`）
3. 请求返回 200 给客户端 ✅
4. **Outbox relay** 在几秒后触发 → `context.Background()` ❌ 
5. Dispatcher 创建通知 → **没有父 span** ❌ （`span B` 是孤儿）
6. SSE push 推送给接收方 → **没有父 span** ❌
7. 如果后续有 webhook 投递 → **同样没有父 span** ❌

这意味着如果张三说"消息发了但对方没收到"，运维人员无法从 trace 中看到 outbox→dispatcher→push 整个链路的耗时和失败点——**只有零散的片段**。

### 为什么需要它

从 **可诊断性角度**，这是当前观测体系中最明显的漏洞：

1. **故障定位困难**：outbox 处理失败时，没有 trace 能从原始 API 请求贯穿到 outbox handler
2. **延迟分析盲区**：用户感知到延迟时（消息发了但通知未推送），trace 无法显示 outbox 延迟——没有从请求到推送的完整 trace
3. **排障成本**：当前只能通过日志时间戳手动关联请求和异步处理，在低延迟问题或高频场景下几乎不可能

### 建议方案

关键思路：**每个 outbox event 存储创建时的 trace 上下文**，在 relay 处理时恢复该上下文。

```
Step 1 — 在 outbox 表中增加 trace 上下文字段

ALTER TABLE outbox_events ADD COLUMN trace_context JSONB;
-- trace_context: {
--   "trace_id": "0af7651916cd43dd8448eb211c80319c",
--   "span_id": "b7ad6b7169203331",
--   "trace_flags": "01"
-- }
```

Step 2 — 写入 outbox 时捕获上下文

```go
func appendEvent(ctx context.Context, e *Event) {
    span := trace.SpanFromContext(ctx)
    if span.IsRecording() {
        sc := span.SpanContext()
        e.TraceContext = map[string]string{
            "trace_id":    sc.TraceID().String(),
            "span_id":     sc.SpanID().String(),
            "trace_flags": fmt.Sprintf("%02x", sc.TraceFlags()),
        }
    }
}
```

Step 3 — Relay 处理时恢复上下文

```go
func (r *Relay) handleEvent(e *Event) {
    ctx := context.Background()
    if e.TraceContext != nil {
        // 从 e.TraceContext 重建 parent span context
        parentCtx := extractTraceContext(e.TraceContext)
        ctx = trace.ContextWithRemoteSpanContext(ctx, parentCtx)
        ctx, span := tracer.Start(ctx, "outbox.handleEvent")
        defer span.End()
    }
    r.handler.Handle(ctx, e)
}
```

Step 4 — 逐步推广到其他后台作业

- `Sweeper` 需要类似的 trace 上下文恢复
- `RateLimit Cleaner`（低优先级，因为是内部维护操作）
- `Idempotency Cleaner`（低优先级）

### 影响范围

- **Outbox schema**：新增 `trace_context JSONB` 列（迁移 000024）
- **Outbox Event 结构体**：新增 `TraceContext map[string]string`
- **所有写入 outbox 的地方**：需要传递 ctx（大部分已经有了）
- **所有消费 outbox 的地方**：relay + notification dispatcher + webhook handler
- **Storage GC sweeper**：类似改造，但 sweeper 是纯内部操作，优先级较低

---

## 优先级排序建议

| # | 方向 | 优先级 | 难度 | 核心收益 | 建议时机 |
|---|------|--------|------|---------|---------|
| 1 | 通用延迟任务框架 | **P1** | 中 | 解锁产品功能（定时发送、自动撤回、TTL 自动清理） | 下一个迭代 |
| 2 | 合成事务探针 | **P1** | 低-中 | 运维安全——在真实用户受影响前发现故障 | 并行 |
| 3 | 资源级作用域搜索 | **P1** | 中 | 搜索体验质变——从"全局关键词"到"精确过滤" | 搜索迭代时 |
| 4 | API Schema Contract | **P2** | 中 | 长期 API 治理、生态建设的前提 | 有外部消费者时 |
| 5 | 异步 Context 传播 | **P2** | 低 | 调试效率提升、trace 完整性 | 接入完备观测体系时 |

---

## 总结

aero-id 已经是一个非常成熟的服务——横跨两个传输层、5 个核心业务域、全功能通知/搜索/审计/管理后台。这五个方向不是修补现有功能的缺陷，而是**扩展系统的能力边界**：

- **延迟任务**扩展了系统的**时间维度**（从"即时"到"定时"）
- **作用域搜索**扩展了信息的**检索精度**（从"全量匹配"到"精准定位"）
- **API Contract**扩展了系统的**消费者边界**（从"内嵌调用"到"开放生态"）
- **合成探测**扩展了运维的**信心边界**（从"组件活着"到"业务通着"）
- **Async Context**扩展了**可观测的深度**（从"同步 trace"到"全链路 trace"）

每个方向都能在现有基础设施上相对低成本地搭建，但解锁的产品/运维价值都十分显著。
