现在我有了充分的代码库理解。以下是完整的架构分析：

---

# aero-id 架构扩展分析

## 1. 架构评估

### 当前架构的优势

aero-id 的代码库在工程质量上远超典型 MVP。几个值得强调的设计决策：

**严格单向依赖与清晰的层边界**。`handler → service → repository + storage + sso` 的依赖方向没有反向引用。这带来了以下优势：HTTP 和 gRPC 双协议可以共享同一套 service 层逻辑；协议切换不会触及业务规则；service 层可以独立于传输协议进行单元测试。

**事务性 Outbox 模式**。事件与领域行数据在同一事务提交，消除了"通知丢失"这一经典问题。这是经过深思熟虑的——许多项目选择在领域操作后手动调用推送，而不是通过 outbox 保证可靠性。

**防御性 SSO 客户端**。断路器 + 指数后退 + 单飞（singleflight）+ 脏读回退 + TLS/mTLS 支持，构成了面向不可靠外部依赖的高度韧性设计。

**内容去重的文件存储**。使用 SHA-256 checksum 进行去重，结合存储引用计数（`storage_refs`），这是经过深思熟虑的优化——既节省存储空间，又避免在去重场景下意外丢失 blob。

### 关键设计决策评估

| 决策 | 评估 |
|------|------|
| **Outbox relay 使用 `context.Background()`** | 合理的初期简化，但现在已成为可观测性的显性短板。relay 是系统唯一异步路径，trace 断裂导致 E2E 延迟定位盲区。需要修复，但修复成本低。 |
| **SSE Hub 设计为进程内内存结构** | 单副本部署时完全合理。但代码库中没有任何抽象层预示将来跨实例扩展——`hub.Hub` 是具体类型在多个地方直接引用（`app.go:126`、`dispatcher.go` 中作为 `hub.Pusher`）。引入广播抽象将是接口提取，而非架构重构。 |
| **存储 GC 作为可选功能（默认关闭）** | 正确决策。GC 修复的是存储泄漏问题，不是数据完整性依赖。默认关闭避免了竞态问题在非必要场景下触发。 |
| **HTTP 请求体大小不做逐路由限制** | **架构债务**。对于有文件上传功能的应用来说，只限制上传 API 而忽略其他所有 API 是合理的"先交付后完善"决策。但进入生产环境前这个缺口必须补上。 |

### 架构债务与技术债

**明确的技术债**：

1. **方向五（P0）—— HTTP 请求体大小限额缺失**。最直接的架构缺口。这不是功能缺失，是基础防御深度的缺失。与 SERVICE 层 `max_upload_bytes` 只限制文件上传不同，消息、评论、搜索等所有 JSON API 都暴露在无限制的请求体下。投入产出比极高——一个中间件文件 + 两行集成代码。

2. **方向四（P1）—— 死信事件全生命周期管理缺失**。Outbox 模式的核心运维配套。没有死信管理 = 事件丢失不可见。当前 `PurgeDeadLetters` 只能被动清理，运维无法查看、重试或告警。

**设计的局限性而非债**：

3. **方向一（P1）—— 异步链路追踪断裂**。当前 tracing 基础设施已经比较完善（HTTP/gRPC 拦截器、OTLP 导出器），唯一的缺口是 outbox relay 使用 `context.Background()` 导致异步 span 成为孤儿。

4. **方向三（P1）—— 跨实例 SSE 推送断裂**。这本质上是单体化设计的局限性，不是债——在单副本部署下它完美工作。需要多副本时才需要解决。

5. **方向二（P2）—— Storage GC TOCTOU 竞态**。由于 GC 默认关闭，目前不是生产问题。但 GC 打开时的数据完整性风险是真实存在的。

---

## 2. 建议的架构扩展方向

### 方向一：异步链路可观测性连续性（当前文档 P1 → 建议升级至 P0.5）

**为什么需要**：
aero-id 的可观测性基础设施（RED 指标 + OTel 追踪）已经较为完善，但目前只覆盖同步请求路径。异步路径（Outbox relay → Dispatcher → SSE push）完全不可观测。这意味着当用户反馈"消息发送了但对方没收到通知"时，运维只能靠日志时间戳手动关联——这与 trace ID 关联的可靠性不是一个量级。更重要的是，SLO 测量无法覆盖异步 E2E 延迟。

**核心挑战**：
- 需要决定 trace context 的传递粒度：是按 event 传递（每一个 outbox event 携带其创建者的 trace context）还是按 drain 批次传递（一个批次共享一个 root span）
- W3C `traceparent` 格式的字符串在 event payload 中存储是否合适，还是应该在 outbox_events 表单独加列
- 从 event 恢复 span context 并在 relay 中创建子 span 时，如何处理已经结束的 parent span（parent 可能早在 timeout 后结束）

**架构变更**：
```
变更前：
  HTTP handler → service(写event) ──→ relay(context.Background()) → dispatcher(orphan span)
变更后：
  HTTP handler → service(写event+保存traceparent) ──→ relay(提取traceparent → 建立子span) → dispatcher(sub span)
```

具体来说：
- `outbox.Event` 结构体增加 `Traceparent` 字段（推荐 W3C traceparent 格式字符串）
- `NewEvent()` 从 `context.Context` 提取 traceparent（需要 `ctx` 参数）
- relay `drain()` 中解析 traceparent 并为每个 event 建立子 span
- 数据库 `outbox_events` 表增加索引列（`trace_id varchar(32)`）

**对现有系统的影响**：
影响极小。这是非破坏性变更：
- Event 结构体新增字段，默认空字符串 = 保持现有行为
- NewEvent 签名变更需要更新所有调用方（`grep -r "NewEvent"` 确认调用方数量）
- relay 的 span 创建是新增逻辑，不影响现有处理流程
- data_retention 中已有死信/published 清理，新列自动跟随清理

**选项分析**：

| 选项 | 成本 | 效果 |
|------|------|------|
| A: Event 加 Traceparent 字段 | 低 | 可以关联请求→event，但 relay 中需要解析 W3C traceparent |
| B: 数据库加 trace_id/span_id 列 | 低-中 | 可在 DB 层按 trace_id 查询关联 events |
| C: relay 建立新 root span（不追踪回请求） | 极低 | 异步路径自身有 trace 了，但与请求的因果链仍然断裂 |
| **推荐: A + C 组合** | 低 | A 保证因果关系，C 保证 relay 自身有结构化的 span 层级 |

### 方向二：存储 GC 的数据完整性保证（当前文档 P2，建议维持 P2）

**为什么需要**：
GC 的 TOCTOU 竞态是典型的数据完整性问题，但触发条件较苛刻（同时需要：内容去重场景 + 并发上传 + GC 精确的时间窗口）。在 GC 默认关闭、引用计数已部署的情况下，风险进一步降低。但一旦打开 GC 并启用 orphan sweep，数据损坏风险是真实存在的——行存在但 blob 被删除，用户下载时 404。

**核心挑战**：
- GC 与文件创建/删除之间没有协调机制——它们操作不同的资源（DB 行 vs 存储对象），无法使用同一分布式事务
- `storage_refs` 表的引用计数减少了竞态窗口，但没有消除它：`Increment` 在行创建之后执行（`file.go:145-150`），窗口依然存在
- `FOR UPDATE` 锁在 `countLive` 查询上可以冻结并发插入，但需要仔细评估对写入吞吐的影响

**架构变更**：
当前架构只有一个方向——定期巡检（Sweeper）。建议引入第二个机制：

```
当前：
  Sweeper.purgeSoftDeleted → countLive(key) → 删除blob
建议扩展：
  Sweeper.purgeSoftDeleted → countLive(key) → 删除blob → 后续一致性验证Job
                                  ↓
                        一致性验证Job（独立goroutine，不同周期）
                          └─ 统计blob存在性 vs 行引用
                          └─ 修复（标记损坏、告警、或自动重上传）
```

**对现有系统的影响**：
一致性验证 Job 是纯新增，不影响现有 GC 逻辑。可配置为独立的 `gc.verify_interval`（默认关闭）。

**推荐方案**：**方案 C（一致性验证 Job）为主 + 方案 B（`SELECT ... FOR UPDATE`）可选**。原因：
- 方案 C 完全无侵入：接受极小概率的竞态，但通过后续验证发现并修复
- 方案 B 的 `FOR UPDATE` 可能在 content-dedup 高频场景下引入写锁竞争
- TOCTOU 竞态的实际影响被引用计数大幅降低，一致性验证作为兜底足够

### 方向三：SSE 推送的跨实例广播（当前文档 P1，建议维持 P1）

**为什么需要**：
当前多副本部署（即使只有 2 个实例）会导致约 50% 的实时推送丢失。这不是功能缺失，而是"单副本部署时不是问题，多副本部署时是灾难"的扩展性负债。如果 aero-id 计划高可用部署（>=2 副本），这个问题就是 P0。

**核心挑战**：
- 引入外部消息总线（Redis Pub/Sub / NATS / RabbitMQ）意味着增加基础设施依赖和运维复杂度
- 如果采用 Redis，需要考虑 Redis 高可用方案
- 跨实例广播带来消息顺序保证的问题——不同实例的 relayer 可能以不同顺序处理事件
- `last_event_id` 回放机制的上限（50 条）需要同步扩展

**架构变更**：

```
当前（单副本）：
  Dispatcher → hub.Pusher → Hub.pub → SSE client

未来（多副本）：
  Dispatcher ──→ Broadcaster.Publish(channel:notif:{uid})
       │                                │
       │                                ▼
       │                   Redis Pub/Sub / NATS
       │                                │
       ▼                                ▼
  Hub.Publish ←───── Broadcaster.Subscribe(channel:notif:{uid})
       │
       ▼
  SSE client
```

**对现有系统的影响**：
需要引入新的 `Broadcaster` 接口（与 `Pusher` 平行的抽象层），但现有代码（`dispatcher.go` 引用 `hub.Pusher`）不需要修改——`Broadcaster` 是对推送到远程 channel 的补充，不替代 `Pusher`。

**推荐方案**：**方案 C（客户端定期拉取回退）先行，方案 A（Redis Pub/Sub）作为高配选项**。原因：
- 方案 C 不需要任何新基础设施——`GET /v1/notifications?since=...` 接口已存在
- 定期拉取 + SSE 混合模式在实践中非常成熟（如 Slack、Discord 都采用类似策略）
- Redis Pub/Sub 的引入需要 Redis 高可用部署，应在明确多副本需求后实施

**选项分析**：

| 选项 | 成本 | 效果 | 风险 |
|------|------|------|------|
| A: Redis Pub/Sub | 中（Redis 部署 + 适配） | 完全解决 | Redis 成为新依赖；Pub/Sub 不持久化 |
| B: Sticky Session | 低（响应头） | 缓解 | 实例重启/扩缩容破坏粘性 |
| C: 客户端定期拉取 | 低（接口已存在） | 大幅缓解（< 30s 延迟） | 增加服务端读负载 |
| **推荐 A + C** | 中 | 实时性 + 可靠性 | 方案 A 为主动推送，C 为兜底 |

### 方向四：死信事件全生命周期管理（当前文档 P1，建议维持 P1）

**为什么需要**：
Outbox 模式的"最后一个可靠性缺口"——事件重试耗尽后被永久静默丢弃。当前 `PurgeDeadLetters` 只能被动清理，没有查看、重试、告警能力。在合规场景（SOC2、SOX）中，"事件处理失败但有记录"是明确的审计要求。

**核心挑战**：
- 死信重试可能复现导致首次失败的问题——如果 handler 有破坏性副作用，重试是危险的。建议 handler 自身实现幂等，或者在管理 API 返回 event_type 供运维判断
- 死信表（归档）与 outbox_events 表分离的需要——如果保留所有死信事件，outbox_events 表会无限增长

**架构变更**：

```
当前：
  MarkFailed → dead_letter = true → PurgeDeadLetters（仅被动清理）

建议扩展：
  MarkFailed → dead_letter = true
       │
       ├── 管理API: GET /v1/admin/outbox/dead-letters (分页)
       ├── 管理API: POST /v1/admin/outbox/dead-letters/{id}/retry
       ├── 管理API: DELETE /v1/admin/outbox/dead-letters/{id}
       ├── 指标: aeroid_outbox_dead_letter_total{event_type}
       └── 告警: increase(aeroid_outbox_dead_letter_total[15m]) > 0
```

**对现有系统的影响**：
纯新增，不影响现有 outbox 路径：
- Repository 新增 `FetchDeadLetters`、`RetryDeadLetter`、`PurgeDeadLetters` 方法
- `RetryDeadLetter` 只需要设置 `dead_letter = false, retry_count = 0, published_at = NULL`
- 管理 API 可以挂载在独立的 `admin` 路由组，与业务 API 分离
- 指标字段新增，不改变现有 `MetricsSink` 接口

### 方向五：HTTP 请求体大小限额（当前文档 P0，建议维持 P0）

**为什么需要**：
这是五个方向中唯一的 **P0**——不仅是安全风险，而且是基础防御深度的缺失。当前 `max_upload_bytes` 只限制了文件上传 API，消息发送（`POST /v1/messages`）、评论创建、搜索查询等所有其他 API 暴露在无限制请求体下。这不需要复杂的架构讨论，实现成本极低（一个中间件文件）。

**架构变更**：
```
internal/middleware/bodylimit.go（新文件）

NewRouter 中增加：
  e.Use(middleware.BodySizeLimit(cfg.RequestBodyLimit))
  // 可选：逐路由覆盖
  // e.POST("/v1/files", middleware.BodySizeLimit(50<<20), ...)
```

**对现有系统的影响**：
- 在 Gin 请求体被读取之前检查，只影响超大请求的客户端
- 返回 HTTP 413（`http.MaxBytesReader` 的行为），不是 500
- gRPC 端的 `MaxCallRecvMsgSize` 已在 config 中（`max_recv_msg_size: 4194304`），不需要修改

---

## 3. 接口设计原则与抽象层

### 关键抽象缺口

当前代码库在几个关键边界上缺乏抽象层，需要在新方向中引入：

**1. Outbox Handler 链**

当前 `outbox.Handler` 接口只有一个 `Handle(ctx, *Event) error` 方法。在未来，如果 webhook 投递 + SSE 推送 + 通知创建都需要订阅 outbox event，当前的单一 handler 模式需要扩展为 handler 链：

```go
// outbox/handler.go
type Handler interface {
    Handle(ctx context.Context, e *Event) error
}

// 新增：HandlerChain 按顺序执行多个 handler
type HandlerChain []Handler

func (c HandlerChain) Handle(ctx context.Context, e *Event) error {
    for _, h := range c {
        if err := h.Handle(ctx, e); err != nil {
            return err
        }
    }
    return nil
}
```

但当前不需要——建议保持简单，在需要时再引入。

**2. SSE 广播抽象**

当前 `hub.Pusher` 接口（`Publish(uid, eventType, data string)`）是推送抽象的最小化接口。方向三需要的 `Broadcaster` 接口应该与 `Pusher` 平行而非替代：

```go
// hub/broadcaster.go
type Broadcaster interface {
    // Publish 发布通知到所有实例
    Publish(ctx context.Context, uid, eventType, data string) error
    // Subscribe 订阅当前实例的通知通道
    Subscribe(ctx context.Context, uid string) (<-chan SSEEvent, error)
}
```

不要将 `Broadcaster` 与 `Pusher` 合并。`Pusher` 已经在 3+ 个地方被引用，保持它不受影响。

**3. 死信管理接口**

死信 API 应该挂载在管理路由组上，与业务 API 完全隔离：

```go
// handler/http/admin.go 或 handler/http/outbox_admin.go
type OutboxAdminHandler struct {
    outbox outbox.Repository  // 或一个专用的 DeadLetterService
}

func (h *OutboxAdminHandler) RegisterAdminRoutes(r Routes) {
    r.Admin.GET("/outbox/dead-letters", h.ListDeadLetters)
    r.Admin.POST("/outbox/dead-letters/:id/retry", h.RetryDeadLetter)
    r.Admin.DELETE("/outbox/dead-letters/:id", h.DeleteDeadLetter)
}
```

### 向后兼容性

所有五个方向都可以设计为非破坏性变更：

| 方向 | 兼容策略 |
|------|---------|
| 方向一 | `Traceparent` 字段默认空字符串；`NewEvent` 新增 `ctx` 参数（需要更新调用方但语义不变）；数据库新列 `DEFAULT ''`；relay 在 traceparent 为空时不建立子 span |
| 方向二 | 一致性验证 Job 默认不启动；`gc.verify_interval: 0` 表示禁用 |
| 方向三 | 客户端拉取回退作为后端特性，SSE 接口不变；Redis Pub/Sub 通过 config 控制 `sse.broadcast.type: redis` |
| 方向四 | 管理 API 新端点，不影响现有 outbox 路径；`RetryDeadLetter` 不改变 `MarkFailed` 的行为 |
| 方向五 | 新中间件 + 新 config 字段 `server.http.request_body_limit`；设置默认值（如 1MB）之前需要告知现有客户端 |

---

## 4. 技术选型建议

### 方向三：消息总线选型

| 技术 | 优势 | 劣势 | 适用场景 |
|------|------|------|---------|
| **Redis Pub/Sub** | 已有生态熟悉度（如果已有 Redis）；极轻量 | Pub/Sub 不持久化（断连丢失消息）；Redis 单点故障 | 通知消息不重要到必须持久化 |
| **Redis Streams** | 消息持久化；消费者组支持；支持回溯 | 比 Pub/Sub 复杂；需要管理消费者组 | 需要消息可靠传递的场景 |
| **NATS** | 高性能，极低延迟；原生 Clustering；at-most-once / at-least-once 语义可选 | 新基础设施 | SSE 推送场景（延迟敏感） |
| **RabbitMQ** | 功能最丰富；AMQP 标准 | 重（16MB 二进制）；运维复杂 | 已经使用了 RabbitMQ 的组织 |

**推荐**：如果 aero-id 团队已在使用 Redis（例如用于会话缓存），选择 **Redis Pub/Sub**（最小增量运维）。如果未使用 Redis，建议从方案 C（客户端拉取回退）开始，等到多副本部署成为明确需求时再考虑 NATS 或 Redis Streams。

### 不需要引入的新技术栈

从架构角度，五个方向都不需要引入全新的技术栈：
- 方向一、二、四、五都是纯 Go 代码变更
- 方向三的 Redis Pub/Sub 是可选引入，且只在多副本部署时必要

### 自建 vs 采购

五个方向都没有"采购"的合理性——它们都是与 aero-id 核心领域逻辑紧密耦合的架构补全：
- OTel 追踪是标准库集成，不存在采购选项
- SSE 跨实例广播是应用层逻辑，外部 SaaS（如 Pusher、Ably）不适合，因为通知数据包含敏感的用户 ID
- 死信管理与 outbox 模型紧密绑定，外部不能替代

---

## 5. 实施路线图

### Phase 0 — 立即（单日内完成，P0 级别）

```
方向五：HTTP 请求体大小限额
├── 实现: internal/middleware/bodylimit.go（~30行）
├── 集成: internal/handler/http/router.go（+1行 e.Use）
├── 配置: configs/config.yaml（新增 server.http.request_body_limit + 逐路由覆盖）
└── 验证: 发送大请求 → 413
```

**风险**：零。这是纯防御性增强，不改变任何已有行为。

**注意**：设定默认值时需要考虑现有客户端的请求体大小。推荐默认 1MB，文件上传路由单独覆盖为 50MB。如果现有客户端发送 >1MB 的消息体（极不可能），需要提前告知。

### Phase 1 — 上线前完成（P1 级别）

分两个子阶段，**1a（方向一 + 方向四共享 outbox 模型变更）**优先于 **1b（方向三）**。

```
Phase 1a（共享 outbox 模型变更）:
├── 方向一：异步链路追踪连续性
│   ├── outbox.Event 增加 Traceparent 字段
│   ├── NewEvent 签名变更为 NewEvent(ctx, aggregateType, aggregateID, eventType, payload)
│   ├── relay.drain() 从 event 恢复 span context
│   └── 数据库迁移: ALTER TABLE outbox_events ADD COLUMN trace_id, span_id
│
└── 方向四：死信事件生命周期管理
    ├── repository: FetchDeadLetters, RetryDeadLetter, PurgeDeadLetters
    ├── handler: 管理 API（GET / POST / DELETE）
    ├── metrics: aeroid_outbox_dead_letter_total
    └── 告警规则: Prometheus alert

Phase 1b（SSE 推送可靠性）:
├── 方向三：客户端定期拉取回退
│   ├── 在 SSE 重连之外，客户端每 30s 调用 GET /v1/notifications?since=...
│   └──（这是前端/客户端变更，后端已有 ListSince 接口）
└── 可选：Redis Pub/Sub 广播
    ├── hub.Broadcaster 接口
    ├── redis.Broadcaster 实现
    └── config: sse.broadcast.type
```

**风险与缓解**：

| 风险 | 缓解策略 |
|------|---------|
| Phase 1a 中 `NewEvent` 签名变更影响所有调用方 | `grep -rn "NewEvent"` 确认调用方（预期 < 10 处）；Go 编译器会在遗漏处报错 |
| 方向一的 trace context 在 event payload 中的序列化开销 | 两个 VARCHAR 列（trace_id 32 + span_id 16）在每行 ~50 字节，对 outbox_events 表影响极小 |
| 方向四死信重试可能复现相同错误 | 管理 API 返回 event_type 供运维判断；handler 应实现幂等；建议手动重试也设最大次数（如 3 次） |
| 方向三客户端拉取增加 DB 读负载 | ListSince 接口已针对 cursor 分页优化；如果负载显著增加，可以考虑短 TTL 缓存 |

### Phase 2 — 长期维护（P2 级别）

```
Phase 2：存储 GC TOCTOU 竞态修复
├── 一致性验证 Job（推荐方案 C）
│   ├── 接受极小概率竞态
│   ├── 定期扫描 files 表的 storage_key → Stat 检查 blob 存在性
│   └── 发现损坏 → 标记/告警/修复
└── 可选：行锁保护（方案 B）
    ├── countLive 使用 SELECT COUNT(*) FROM files WHERE storage_key=? FOR UPDATE
    └── 需要基准测试验证对写入吞吐的影响
```

**风险**：一致性验证 Job 本身的并发性需要保护——不能同时运行两个验证 Job。建议使用 `sync.Mutex` 或 DB 端的 `SELECT ... FOR UPDATE NOWAIT`。

### 依赖关系

```
Phase 0 (P0): 方向五 ──── 无依赖
                           │
Phase 1a (P1): 方向一 ────┤
                方向四 ────┴── 共享 outbox 模型变更
                           │
Phase 1b (P1): 方向三 ──── 依赖 SSE 基础设施（已存在）
                           │
Phase 2 (P2):  方向二 ──── GC 启用时才需要
```

### 跨方向的共享影响

方向一和方向四共享 `outbox` 包中的模型和 repository 变更，建议同时实施以减少迁移次数。

方向一（Traceparent 字段）新增的数据库列（`trace_id`, `span_id`）可能被方向二的一致性验证 Job 用于记录 blob 检查的 trace——这是一个正向协同，但不必为此推迟方向一的实施。

---

## 总结

这五个方向涵盖了 aero-id 架构的四个关键维度：

| 维度 | 方向 |
|------|------|
| **安全性** | 方向五（请求体限额） |
| **可观测性** | 方向一（异步链路追踪） |
| **可靠性** | 方向二（GC 数据完整性）、方向四（死信管理） |
| **可扩展性** | 方向三（跨实例 SSE） |

实施优先级建议：**P0 → P1a → P1b → P2**，预计总工作量在 **5-8 个工程日**内（包括测试、文档、部署验证）。其中 Phase 0 在单日内可交付。
