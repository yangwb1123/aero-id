# 架构分析报告：aero-id 五个架构级扩展方向

## 1. 架构评估

### 当前架构的优势

aero-id 的架构已达到相当成熟的水平。从代码扫描来看，以下设计决策值得肯定：

**分层严格性**。`handler → service → repository` 的单向依赖链在 Go 微服务中执行得非常干净。service 层作为唯一的业务逻辑和授权决策中心，HTTP 和 gRPC 共享同一套逻辑，这意味着不会出现协议层面上行为不一致的 bug。这是一个许多团队在实际实现中会妥协的原则。

**事件驱动基础设施的完整性**。事务性 Outbox 模式（事件与行级数据同一事务提交）是分布式系统中保证"至少一次"投递的标准做法。当前实现已经包含重试、退避、异步 relay，甚至断路器——这些组件的组合已经构成一个工业级的异步处理管道。

**SSO 集成的健壮性**。断路器 + 重试 + 后退 + 单飞 + 脏读回退 + TLS/mTLS 的组合，说明 SSO 作为外部依赖的故障模式已经被充分预见到。这通常是系统上线后踩坑才补上的防御，在 aero-id 中已经预先存在。

**双协议支持的对称性**。HTTP (Gin) + gRPC 全功能对等且共享同一 service 层，是应对不同客户端类型（浏览器 vs 移动 App vs 服务端）的合理设计。这种对称性如果在设计阶段未能建立，后期会非常痛苦。

### 关键设计决策评估

| 决策 | 评价 |
|------|------|
| Outbox Event 不携带 Trace Context | 合理但局限。这在简单场景下够用，但一旦需要端到端延迟分析就彻底失效。属于"先走通再优化"的典型取舍 |
| SSE Hub 为进程内内存结构 | 单体部署下完全合理。但设计时对多副本场景考虑了"回放"补救而非"广播"方案，这是一个隐含的部署假设 |
| GC 使用"检查-删除"的两步模式 | 无锁设计降低了并发实现的复杂度，但留下了 TOCTOU 竞态。在 GC 默认关闭的前提下是可以接受的权衡 |
| 无全局请求体大小限制 | 最值得质疑的设计缺口。即便是最小可行产品，一个 `http.MaxBytesReader` 中间件的成本几乎为零，而不设置意味着系统在内存 DoS 面前没有第一道防线 |

### 架构债务

这份文档已经识别出五个缺口，我认为其中两个是真正的**架构债务**（如果不解决，随着系统演进修复成本会持续增长），另外三个是**功能缺口**（可以按业务节奏解决）：

**架构债务**：
1. **Outbox Event 无 Trace Context**（方向一）：每增加一个通过 Outbox 流转的事件类型，追踪断裂的代价就增加一分。当前已有 38 个 RPC，message.sent、comment.created 等多个事件通过 outbox 分发，追踪断裂影响面已经不小。
2. **SSE 架构假设为单副本**（方向三）：`hub.Hub` 使用 `sync.Map` 做订阅管理的设计隐含了"所有连接在同一进程中"的假设。如果未来部署多副本，这不仅是功能缺失，而且需要**架构层的重新设计**——从进程内内存队列变为外部消息总线。

**功能缺口**：
3. **死信管理**（方向四）：运维工具的缺失，不影响单次请求的正确性，但影响故障恢复能力。
4. **GC TOCTOU**（方向二）：数据完整性风险，但触发概率低且有缓解措施。
5. **请求体大小限制**（方向五）：安全防御的缺失，但修复成本极低。

---

## 2. 扩展方向

基于文档的五个方向，我从架构师视角进行补充性分析，而非重复文档内容。以下是我在文档基础上进一步识别的扩展方向——文档的五个方向聚焦于**系统内部缺口**，以下方向更多关注**系统间扩展和治理能力**。

### 方向 A：Outbox 事件的可回溯性和因果一致性（Traceability & Causality）

> 此为文档方向一的深化和拓展。

**为什么需要**。Outbox 是一个异步事件总线，但当前它只是一个"写入→分发"的黑箱。当系统发展到 38+ 个 RPC 和多种事件类型时，运维人员需要回答：

- "这条通知是由哪个消息发送触发的？"
- "为什么这个事件被分发到了错误的 handler？"
- "这个事件序列的执行顺序是否符合业务预期？"

**核心挑战**。文档中的方案 A（Event 加 Traceparent 字段）解决了追踪链路的技术链路问题，但未触及**业务因果链**——一个消息发送触发一个通知，通知触发一个 SSE 推送，SSE 推送触发客户端重新请求。每一跳的因果关联需要的事件 ID 链需要在 Payload 中传递，而非仅仅在 tracing 系统中。

**预期的架构变更**。

```
当前：
Message.Send → Outbox event → Dispatcher → Notification → SSE Push
                  ↑ 无因果关联

建议：
Message.Send → Outbox event (carries causal_parent_id = message.send.tx_id)
             → Dispatcher → Notification (carries causal_parent_id = event.id)
                          → SSE Push (carries causal_parent_id = notification.id)
```

这需要一个 `causal_chain` 字段或 payload 中的标准化的 `CausationID` / `CorrelationID` 字段。W3C 的 `traceparent` 是技术链路追踪，而业务因果链需要的是应用层的 `Correlation-ID` 约定。

**对现有系统的影响**。影响范围在 outbox 模型层和 dispatcher 层。Event 结构增加 1-2 个字段，dispatcher 在 Handle 时从 event 提取并传递给下游。**不破坏现有接口**，是向前兼容的添加。

---

### 方向 B：事件版本的契约治理（Event Schema Governance）

**为什么需要**。当前 Outbox Event 的 Payload 是 `[]byte`（在 `outbox.go` 中定义为 `Payload []byte`），没有任何 schema 约束。当事件消费者和生产者由不同团队维护时，这就成了问题：

- 生产者给 Payload 加了一个字段，消费者反序列化失败
- 两个部署版本使用了不同的事件格式
- 无法对事件进行向后兼容性验证

**核心挑战**。aero-id 的 Outbox 目前是自产自消——同一个服务写入事件并处理事件。但从架构演进角度看，以下情况会使事件 schema 治理变得必要：

1. 其他服务开始消费 aero-id 的 outbox 事件
2. 事件被写入长期存储供分析使用
3. 需要重放历史事件到新的 consumer

**预期的架构变更**。引入事件 schema 注册表（可以是简单的 Go 结构体版本标记，也可以是完整的 CloudEvents + Schema Registry）：

```go
// 方案一：每个事件类型注册序列化/反序列化函数
// 方案二：使用 protobuf 定义事件 schema，Payload 为 Any
// 方案三：每个事件版本带 major/minor 版本号
```

**对现有系统的影响**。当前所有 `Payload` 使用 `map[string]any` + JSON 序列化。引入版本化需要在 three places 改动：生产者写入时标注版本、outbox 表存储版本、消费者反序列化时按版本处理。这是一个潜在的**重大架构变更**，建议在出现跨服务消费事件的需求时才启动。

**优先级**。P2 — 当前自产自消模式下不需要。

---

### 方向 C：通知渠道抽象层（Notification Channel Abstraction）

> 此为文档方向三的深化。

**为什么需要**。当前的通知架构是：Outbox → Dispatcher → Notification（DB 行）→ SSE Push。但通知渠道的多样性在未来几乎是必然的：

- 当前：SSE
- 近期：Email（Webhook → SendGrid/Mailgun）
- 中期：Push Notification（Firebase/APNs）
- 远期：Slack/Teams/Discord Webhook

**核心挑战**。当前 Dispatcher 直接将通知推送到 SSE Hub（`dispatcher.go:136-139`），这意味着添加新渠道需要修改 Dispatcher 的核心逻辑。渠道抽象需要解决：

1. **渠道选择**：根据通知类型、用户偏好、静默时段选择渠道
2. **渠道编排**：主渠道失败 → 降级到备用渠道
3. **渠道速率**：不同渠道的发送速率限制不同（Email 慢，SSE 快）
4. **渠道幂等**：同一通知不应通过多个渠道重复发送

**预期的架构变更**。

```
当前：
Dispatcher → SSE Pusher

建议：
Dispatcher → NotificationRouter → Channel[SSE, Email, Push]
              ↑ 在路由层选择渠道
              ↑ 支持"全部发送"和"优先级发送"策略
```

引入 `Channel` 接口：

```go
type Channel interface {
    Name() string
    Send(ctx context.Context, n *domain.Notification, prefs *domain.NotificationPreferences) error
    // Rate limit, retry, backoff 由框架层统一处理
}
```

**对现有系统的影响**。Dispatcher 需要重构以支持多 channel，而不是直接调用 SSE pusher。这是中等规模的重构——需要抽取出 `NotificationRouter`，将 SSE 实现为第一个 Channel。**现有 SSE 行为不变**，只是调用路径从 `Dispatcher → Hub.Publish` 变为 `Dispatcher → Router → SSEChannel → Hub.Publish`。

**优先级**。P1 — 如果不是立即需要 Email/Push 渠道，可以先定义接口，现有实现仍然调用 SSE。但**接口应该现在定义**，否则未来的渠道添加会需要大量重构。

---

### 方向 D：存储层的租户隔离和数据生命周期策略（Storage Multi-Tenancy & Lifecycle）

**为什么需要**。aero-id 的文件存储支持 local 和 S3 后端。但文件的生命周期管理仅有 GC（软删除 → 硬删除）这一种策略。更完整的存储治理需要：

1. **保留策略**：不同类型的文件有不同的保留期限（消息附件 90 天，用户头像永久）
2. **访问频次分层**：热数据在本地/S3 标准存储，冷数据迁移到 S3 Glacier
3. **租户隔离**（虽然是单 SSO，但可以通过 `owner_uid` 实现逻辑隔离）：单个用户/实体的存储使用量配额
4. **合规删除**：某些类型的数据需要"不可恢复的删除"（覆写而非仅标记删除）

**核心挑战**。现有的 `FileStorage` 接口（`internal/storage/storage.go`）非常简洁——`Save`/`Delete`/`PresignUpload`/`PresignDownload`。生命周期策略需要在这个接口之上增加：

- 存储类（Storage Class）的概念
- 保留策略的元数据与文件关联
- 审计删除操作

**预期的架构变更**。

```go
// 在 domain 或存储层增加策略定义
type RetentionPolicy struct {
    Duration    time.Duration // 保留时长
    Action      RetentionAction // Delete | Archive | Notify
    HardDelete  bool // 是否需要覆写
}

type StorageClass string
const (
    StorageClassStandard StorageClass = "STANDARD"
    StorageClassArchive  StorageClass = "ARCHIVE"  // S3 Glacier / local cold path
)
```

**对现有系统的影响**。这是**渐进式的**。当前可以先在 Service 层的 `Create`/`Delete` 中根据文件类型（domain.FileKind）隐式应用保留策略，不改变存储接口。后期再引入正式的策略引擎。

**优先级**。P2 — 在文件存储规模增长到需要分级管理之前的投资回报较低。

---

### 方向 E：服务拓扑和依赖图的可声明化（Service Topology Declaration）

**为什么需要**。这是一个元架构层面的方向。当前 aero-id 的依赖图（SSO、Postgres、MinIO/S3、可能未来的 Redis、消息队列）是**隐式的**——它们分散在 config、app.go 的初始化代码和代码的 import 关系中。当系统复杂度增长时，运维人员和新开发者需要一个明确的依赖清单：

- 哪些是硬依赖（服务不可用则完全不可用）？
- 哪些是软依赖（降级模式如何）？
- 哪些是最低可用版本的依赖？

**核心挑战**。这属于"知道该做但总被搁置"的架构工作。技术上不复杂，但需要纪律来维护。

**预期的架构变更**。引入一个 `ServiceTopology` 结构（或 YAML 文件），声明所有外部依赖及其健康检查逻辑：

```yaml
# service-topology.yaml
dependencies:
  - name: sso
    type: grpc
    critical: true
    health_check: /v1/healthz
    circuit_breaker: true
    degradation: ["stale_read", "local_cache"]
  - name: postgres
    type: database
    critical: true
    degradation: none
  - name: minio
    type: storage
    critical: false
    fallback: local
```

**对现有系统的影响**。无影响。这是一个辅助性的治理工具，不改变运行时代码。可以在 `app.go` 启动时加载该文件并验证，或在文档中作为架构参考。

**优先级**。P2 — 运维治理工具，无功能影响。

---

## 3. 接口设计建议

### 核心原则

**为扩展设计，为测试可替换性设计**。当前 aero-id 的接口设计总体良好（`FileStorage`、`Repository`、`SSOClient` 都有清晰的接口定义），但以下方面可以改进：

### 3.1 Outbox 层的事件序列化接口需要显式化

当前 `Event.Payload` 是 `[]byte`，序列化方式隐式约定为 JSON。这是一个**隐式契约**——生产者和消费者通过约定相同的 Go 类型来交换数据。建议：

```go
// 当前：隐式
type Event struct {
    Payload []byte  // 调用者自行 marshal/unmarshal
}

// 建议：显式序列化接口
type EventSerializer interface {
    Marshal(any) ([]byte, error)
    Unmarshal([]byte, any) error
}

// 默认实现 JSONSerializer
// 未来可以增加 protobufSerializer, avroSerializer
```

这不会改变当前代码——`JSONSerializer` 作为默认实现，现有行为完全一致。但为事件 schema 治理（方向 B）埋下了伏笔。

### 3.2 Notification Channel 接口应提前定义

如前文方向 C 所述，Notification 的渠道抽象是**现在就适合定义接口、但可以有单一实现**的场景。

```go
// 建议在 internal/notification 中定义
type NotificationChannel interface {
    // Name 返回渠道标识（"sse", "email", "push"）
    Name() string
    // Send 发送通知。实现者负责重试、限速和错误处理
    Send(ctx context.Context, n *domain.Notification, prefs *domain.NotificationPreferences) error
    // BatchSend 批量发送（可选接口）
    BatchSend(ctx context.Context, ns []*domain.Notification, prefs map[string]*domain.NotificationPreferences) error
}
```

**优势**：
- 新渠道添加只需实现接口，无需修改 Dispatcher 核心
- 测试时可以注入 mock channel
- 渠道特定的逻辑（Email 的 HTML 模板、Push 的设备 token 管理）隔离在各自的文件中

**向后兼容性**：将现有 SSE Pusher 包装为 `SSEChannel`，Dispatcher 改为调用 `NotificationRouter.SelectChannels(n.Type, prefs)` → 对每个 channel 调用 `Send`。现有 SSE 行为不变。

### 3.3 死信管理操作应遵循 Repository 模式

文档方向四建议了死信的管理 API。从架构一致性来看，死信操作应该遵循现有的 `Repository` 模式——新的 `DeadLetterRepository` 方法作为 `outbox.Repository` 接口的扩展，而非创建独立的死信服务。

```go
// 扩展 outbox.Repository，而非创建新接口
type Repository interface {
    // 现有方法
    Append(ctx context.Context, tx *gorm.DB, e *Event) error
    FetchUnpublished(ctx context.Context, limit int) ([]*Event, error)
    MarkPublished(ctx context.Context, id uuid.UUID) error
    MarkFailed(ctx context.Context, id uuid.UUID, retryCount int, maxRetries int, at time.Time) error

    // 新增死信方法（P1）
    FetchDeadLetters(ctx context.Context, page Page, filter DeadLetterFilter) ([]*Event, string, error)
    RetryDeadLetter(ctx context.Context, id uuid.UUID) error
    PurgeDeadLetters(ctx context.Context, before time.Time) (int, error)

    // 新增统计方法（P1）
    DeadLetterStats(ctx context.Context) (*DeadLetterStats, error)
}
```

**设计原则**：死信不是独立的新实体——它们是 `outbox_events` 表中 `dead_letter = true` 的行。因此操作应该属于同一个 `Repository`，而非独立的死信服务。这避免了出现"文件明明在一起、接口却分在两个包"的架构不一致。

### 3.4 GC 的竞态缓解应保持在存储层内部

文档方向二的方案 B（SELECT FOR UPDATE）和方案 C（修复 Job）是不同的设计选择。

**我的建议**：接受竞态窗口的存在，但增加一个**存储层一致性校验**的接口。这个接口属于 `Storage` 层，不属于 GC：

```go
// internal/storage 包
type ConsistencyChecker interface {
    // Verify 检查一个 storage_key 对应的 blob 是否健康
    // 返回状态和必要的修复动作
    Verify(ctx context.Context, key string) (BlobStatus, error)
}

type BlobStatus int
const (
    BlobHealthy BlobStatus = iota
    BlobMissing // DB 有引用但 blob 不存在
    BlobOrphan  // blob 存在但 DB 无引用
)
```

这样，GC 的 TOCTOU 问题被降级为"罕见但可检测和修复"的故障模式，而非"静默数据损坏"。`Verify` 可以在后台 Job 中运行，也可以在文件下载时作为健康检查触发。

---

## 4. 技术选型

### 4.1 跨实例 SSE 广播：Redis Pub/Sub vs NATS vs 自建

这是方向三的核心技术选型。

| 维度 | Redis Pub/Sub | NATS | 自建 (gRPC Stream) |
|------|--------------|------|-------------------|
| **匹配度** | 发布-订阅，完美匹配 SSE 广播场景 | 发布-订阅，匹配但功能过剩 | 需要自行管理实例发现和拓扑 |
| **基础设施** | aero-id 未使用 Redis，需要新引入 | 需要新引入 | 零新依赖 |
| **可靠性** | 无持久化（Pub/Sub 丢失离线消息） | 支持持久化 | TCP 级别可靠 |
| **运维** | Redis 是标准组件，团队有经验概率高 | 需要学习新运维知识 | 需要开发实例发现 |
| **延迟** | 微秒级 | 微秒级 | 毫秒级（取决于 gRPC 连接池） |
| **规模** | 万级通道，十亿级消息 | 百万级通道 | 与实例数线性相关 |
| **认证** | Redis 密码/ACL | JWT/Token | mTLS |
| **成本** | 内存开销 | 轻量 | 开发时间 |

**我的推荐**：**Redis Pub/Sub** 是 SSE 广播场景最自然的选择，但前提是系统已经或准备使用 Redis（缓存、会话、限流等常见用途）。如果系统不想引入 Redis，**NATS** 是一个极轻量、运维简单的替代方案——单个二进制、无需持久化存储、性能极高。

**不推荐自建 gRPC Stream**，因为需要自己实现实例发现（Consul/etcd/DNS）、连接管理、重连逻辑。这些是消息中间件已经解决的问题。

**评估标准**：引入新中间件的决策应遵循"两条用一"原则——新中间件至少解决两个问题时才值得引入。如果 Redis 仅为 SSE 广播引入，可能不值得。如果 Redis 同时用于 rate limit 的分布式计数、缓存、session 存储——那 SSE 广播就是一个"顺便"的收益。

### 4.2 Trace Context 传播：W3C Trace Context vs OpenTelemetry 原生协议

文档方向一提到了 W3C Trace Context（`traceparent` 头）。在 Go 的实现中，有两个选择：

| 方案 | 优势 | 劣势 |
|------|------|------|
| **W3C Traceparent 字符串** | 语言无关、存储简单、调试友好 | 需要自行 parse/format |
| **OpenTelemetry SpanContext 直传** | 直接使用 OTel SDK 方法、支持 baggage | 耦合 OTel 版本、序列化复杂 |

**我的推荐**：使用 W3C Traceparent 字符串格式（方案 A）。原因：

1. Outbox Event 存储在数据库中，字符串格式的 traceparent 可直接通过 SQL 查询（`WHERE traceparent LIKE '00-%'`）
2. 与 HTTP 头部的 `traceparent` 标准一致，便于调试（curl 命令可以直接复制）
3. 不绑定到特定版本的 OpenTelemetry SDK

但有一个重要的补充：**Event 结构中的 Traceparent 应为可选字段**，默认空字符串。这样旧的事件（没有 traceparent）不会被新代码拒绝。这是向后兼容的基本要求。

### 4.3 Schema Registry：自建 vs 使用 Confluent Schema Registry

方向 B（事件 Schema 治理）是远期方向。如果需要，技术选型如下：

| 方案 | 优势 | 劣势 |
|------|------|------|
| **Go 结构体版本标记** | 零新依赖、简单 | Schema 分散在代码中，跨语言困难 |
| **Protobuf Any + FileDescriptorSet** | Go 原生友好、代码生成 | 需要维护 .proto 文件 |
| **Confluent Schema Registry** | 行业标准、REST API、兼容性检查 | Java 依赖、运维重 |

**我的推荐**：在 aero-id 自产自消的场景下，**不需要** Schema Registry。使用 Go 结构体版本标记就足够了：

```go
type EventPayload struct {
    Version int    `json:"_version"` // >= 1
    // V1 字段
    // V2 新增字段用 omitempty
}
```

如果未来跨服务消费事件的需求出现，优先考虑 **Protobuf Any**——因为 aero-id 已经使用 protobuf 定义 gRPC 接口，开发团队对 protobuf 有经验。

---

## 5. 实施路线图

### 优先级调整

文档的路线图（P0:方向五 → P1:方向一、三、四 → P2:方向二）是合理的。我在此基础上做微调：

```
Week 1 (P0):
  └── 方向五：Body Size Limit (1 天 · 零依赖)

Week 2-3 (P1a — 高收益低风险):
  ├── 方向四：Dead Letter 管理 (3 天)
  │   └── Repository 扩展 + HTTP 管理 API + 指标 + 告警
  │   └── 注意：与方向一共享 outbox 模型变更，可先做接口设计再同步实施
  └── 方向一：Async Trace Continuity (2 天)
      └── Event 加 Traceparent + relay 提取注入 + DB 迁移
      └── 可与方向四共享 outbox_schema 变更

Week 4-6 (P1b — 架构决策关键期):
  └── 方向三：Cross-Instance SSE (5-10 天，取决于方案选择)
      ├── 第一步：客户端定期回退拉取 (2 天，低成本临时方案)
      └── 第二步：Redis Pub/Sub 广播 (3-5 天，正式方案)
           └── 需要先评估是否需要引入 Redis

Quarter 2-3 (P2):
  └── 方向二：GC TOCTOU 竞态 (3-5 天)
      └── 一致性校验 Job + 可选行锁
      └── 在 GC 启用前完成
```

### 风险分析

| 风险 | 影响 | 概率 | 缓解 |
|------|------|------|------|
| **方向五被忽略** | 安全漏洞 | 低 | P0 标记，一日修复，无需评审 |
| **方向一改动破坏旧事件 replay** | 已有事件丢失 trace | 中 | 新字段默认空字符串，旧事件无影响 |
| **方向三引入 Redis 的运维成本** | 增加基础设施复杂度 | 高 | 先做客户端回退拉取（零依赖），Redis 作为增强选项 |
| **方向四重试死信导致重复通知** | 用户收到重复消息 | 中 | 幂等检查（notifications 表去重），重试通知前检查是否已投递 |
| **方向二两阶段锁影响写入性能** | files 表写入吞吐下降 | 低 | 优先使用修复 Job 而非行锁 |
| **五个方向同时推进导致资源分散** | 每个方向完成质量下降 | 中 | 严格按照 P0→P1→P2 推进，前一个方向完工再启动下一个 |

### 实施建议

**先做 P0，但不要止于 P0**。方向五（BodySizeLimit）确实是当天可完成的修复。但一个常见陷阱是：团队做完 P0 后认为"安全了"，然后 P1 方向被无限期搁置。实际上，方向四（死信管理）和方向一（异步追踪）的实施成本相当低，应该在 P0 完成后的同一 sprint 内启动。

**方向三是唯一需要外部决策的**。其他四个方向完全在 aero-id 的控制范围内。方向三的 Redis 引入决策应该提前进行——即使决定不引入 Redis，客户端回退拉取方案也值得做。

**方向二的 TOCTOU 修复可以延迟，但一致性校验 Job 应该提前做**。即使不启用 GC，一个独立运行的一致性校验 Job（Verifier）可以被动检测数据损坏。实现成本低，收益是数据完整性的事前发现机制。

**五个方向之间有两条共享路径**：

```
路径 A: outbox 模型扩展
  ┌─ 方向一：Event 加 Traceparent
  └─ 方向四：DeadLetter 管理扩展 Repository 接口
  建议: 先定义 Event 结构变更，两个方向共用一次 schema 迁移

路径 B: notification 管道扩展
  ┌─ 方向三：跨实例 SSE 广播
  └─ 方向 C(我的补充)：Channel 抽象
  建议: 在方向三的 Redis 广播基础上，顺手将 Dispatcher → Hub.Publish 
        抽离为 Channel 接口。两者共享同一段重构
```

---

## 总结

这份文档的五个方向是**经过代码验证的架构缺口**，每个方向在代码库中都有明确的位置和逻辑证据。从架构评估的角度看：

- **最紧急也最简单**：方向五（BodySizeLimit）— 零依赖、可单独完成
- **最具长期架构价值**：方向一（异步追踪连续性）— 它修复的是可观测性的系统盲区，且与方向四共享 outbox 层变更，边际成本更低
- **最影响部署架构决策**：方向三（跨实例 SSE）— 如果团队计划部署多副本，这个方向决定了 SSE 是"单实例可用"还是"多实例可靠"
- **最容易推迟**：方向二（GC TOCTOU）— GC 默认关闭时竞态不会触发，但建议在上线前完成一致性校验 Job

**最大架构风险不在五个方向内，而在五个方向的推进节奏**。如果 P0 完成后续工作无限搁置，系统会在以下场景中暴露出问题：

1. 流量突增（大请求耗尽内存—方向五缺失）
2. 外部依赖故障（死信不可见—方向四缺失）
3. 多副本部署（SSE 推送丢失—方向三缺失）
4. 性能问题排查（异步链路不可追踪—方向一缺失）
5. GC 启用（数据损坏—方向二缺失）

建议将方向一、四、五纳入**上线前的 DoD**（Definition of Done），方向三纳入**多副本部署的 DoD**，方向二纳入**GC 启用的 DoD**。
