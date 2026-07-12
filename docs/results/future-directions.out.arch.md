# 架构分析报告：aero-id

## 一、架构评估

### 1.1 当前架构的优势

**严格的分层依赖**是该项目最突出的架构决策。`handler → service → repository/storage/sso` 的单向依赖链带来了几个关键收益：

- **双协议共享业务逻辑**：HTTP REST 和 gRPC handler 共用同一套 service 层，行为完全一致。目前已通过 `parity_test.go` 和 `edge_test.go` 验证了这一承诺——这是架构纪律的直接体现，而非事后补丁。
- **可独立测试的 service 层**：`service.NotificationPusher` 接口只声明 `Publish(uid, eventType, data string, eventID int64)`，*hub.Hub 通过 adapter.go 满足它——两者无 import 循环且可以分别 mock。
- **限界清晰**：`internal/` 内部按领域能力水平拆分（file/, message/, comment/, notification/, block/ 等），每个包内 handler + repository + service 三件套齐备，职责不溢出。

**事务性 outbox 模式**是该架构中最重要的弹性基础设施。通过 `outbox.Relay` + 重试/退避/死信，将"通知创建"这类次要副作用从请求路径中剥离，确保了：

- 通知产出不阻塞主业务（消息发送、评论回复）
- 失败可自动恢复（重试退避至 1h，最大 20 次后进死信）
- 可观测性（`MetricsSink` 上报 drain 延迟、积压深度、发布/失败计数）

**通知管道的链式组合**值得特别提及：

```
Outbox Event → NotificationDispatcher (block check → preference check → rate limit → collapse → create) → SSE push
```

每一步都是独立选项，通过 `WithDispatcher*` 函数式选项注入。这是在 Go 中做策略模式组合的正交设计范例——*Chain* + *Decorator* 而非堆砌 if-else。

### 1.2 架构局限性

**当前架构在以下维度存在明确的扩展瓶颈：**

#### 1.2.1 单进程瓶颈

当前是单进程 monolith。所有 outbox 消费、SSE 连接管理、请求处理都在同一进程中。扩展方向：
- 水平扩容需要引入外部消息队列（当前 relay 的轮询 + DB 级锁在多个实例下会重复消费）
- SSE Hub 是进程内内存对象，多实例部署后用户连接到不同实例将收不到彼此的推送

#### 1.2.2 通知扇出是同步链

当前 `NotificationDispatcher.Handle` 是一个同步链路：先 DB Insert，再 SSE Publish。SSE Publish 失败（用户离线、channel full）不会阻断通知创建——这是正确行为，但代码中并未显式语义化。需要确保这一契约不被后续修改破坏。

#### 1.2.3 缺乏存储配额基础设施

当前 `fileService` 只做单文件大小限制（`WithMaxUploadBytes`），没有按账户的总配额 consume 机制。这是方向二的核心痛点——涉及 `storage_quotas` 表、原子 UPDATE 语义、以及服务层的新接口。

#### 1.2.4 群组场景的扇出放大

如果引入群组（方向一），一条群组消息需要扇出 N 条通知/N 条收件箱记录。当前 `NotificationDispatcher` 是单事件入 → 单通知出的模式，群组的 1:N 扇出需要新的抽象。

### 1.3 关键设计决策回顾

| 决策 | 评价 | 建议 |
|---|---|---|
| UUIDv7 + cursor 分页 | ✅ 正确。避免 OFFSET 的稳定性问题，天然支持 time-ordered 分页 | 保持 |
| 软删除 + gorm.DeletedAt | ✅ 符合预期。但注意 `CountByStorageKey` 需要 `WHERE deleted_at IS NULL` | 已做 |
| 函数式选项模式注入可选能力 | ✅ 优秀。`With*` 模式让可选行为（audit log, block check, rate limit）不污染核心逻辑 | 保持 |
| outbox event 用 JSON payload | ⚠️ 合理但有代价。无 schema 校验，重构时字段名变更无编译期保护 | 考虑引入 protobuf 做 event schema（见下） |
| 通知折叠在 dispatcher 层实现 | ⚠️ 正确但增加了 dispatcher 的复杂度。折叠和去重逻辑放在通知 infras 层而非领域层 | 当前可接受，群组场景需重审 |
| SSE Hub 用有缓冲 channel + 静默丢弃 | ✅ 生产级正确。16 个 buffer slot + 非阻塞发送防止慢消费者阻塞整个 hub | 保持 |

### 1.4 技术债务

1. **新旧 dispatcher 共存**：`internal/notification/dispatcher.go`（旧版，仅有 block checker + pusher）与 `internal/service/notification.go`（新版 `NotificationDispatcher`，含 rate limiter + collapse + preference checker）并存。旧版是否已废弃？`app.go` 中引用的是 `service.NewNotificationDispatcher`，旧版 `notification/dispatcher.go` 需要清理。

2. **event payload 缺乏类型契约**：`messageSentPayload`、`commentRepliedPayload` 等事件结构体在 `internal/service/notification.go` 和 `internal/notification/dispatcher.go` 中重复定义。应该提取到 `domain/event_types.go` 并加上版本号。

3. **Channel buffer 策略**：SSE subscriber channel 的 `buffer size = 16` 是硬编码的，且 `non-blocking send` 意味着慢消费者会静默丢事件。需要一个可配置的 buffer size + 丢事件告警。

---

## 二、扩展方向

### 方向 A：存储配额管理（P0）

**为什么需要**：当前文件服务只校验单文件上限（`WithMaxUploadBytes`），没有账户总配额约束。无配额意味着一个用户可以填满整个磁盘/S3 bucket，属于运维安全基线缺陷。

**核心挑战**：
- 并发上传时原子性：`UPDATE storage_quotas SET used_bytes = used_bytes + ? WHERE used_bytes + ? <= quota` 是一条原子 SQL，不需要应用层事务锁。但 `fileService.Create` 中需要先写入 blob（`storage.Put`）、再检查 quota、再回滚 blob（如果超限）。这意味着配额检查必须在 `Put` 之后、`files.Create` 之前——事务边界复杂。
- 配额统计一致性：软删除文件需要释放配额，但 `fileService.Delete` 是异步的（软删后 blob 由 GC 清理），配额释放应该和软删同步。
- 多存储后端一致性：local 和 S3 的配额计算方式一致，但延迟释放的策略不同（S3 按量计费，local 磁盘空间固定）。

**预期架构变更**：

```
service/file_service.go:
  Create: Put → checkQuota(atomic update) → Create row
  Delete: soft delete → releaseQuota(atomic decrement)
  
repository/storage_quota.go:
  storage_quotas(uid TEXT PK, used_bytes BIGINT, quota BIGINT, updated_at)
  
服务层新增:
  WithQuotaChecker(repo StorageQuotaRepository)
```

**对现有系统的影响**：
- 新增 `internal/quota/` 包（或放在 `internal/service/` 加 `WithQuotaRepo` 选项）
- 新增 `storage_quotas` 迁移脚本
- 不影响现有 HTTP/gRPC 接口（配额超出时返回 `402 Payment Required` 或 `403 Forbidden`）
- GC sweeper 回收 blob 时不调整配额（配额已在软删时释放）

**技术选型**：
- 使用 `INSERT ... ON CONFLICT DO UPDATE` 实现原子 upsert（首次写入时创建配额行）
- 不需要独立的配额服务——在 service 层做，保持当前 monolith 模式

---

### 方向 B：群组消息与大规模通知扇出（P1）

**为什么需要**：当前只有 1:1 私信（messages）。群组是几乎所有社交/协作产品的核心能力。群组的核心技术挑战不是消息存储（一条消息），而是**通知扇出**（一条群组消息 → N 个成员通知）。

**核心挑战**：
- 扇出放大：1000 人群发一条消息 → 1000 条 notification row。需要批量 insert（当前是逐条 insert via NotificationDispatcher）。
- 扇出效率：1000 条 insert 在同一个 DB 事务中？还是分批？`COPY` 协议？
- 读取放大：成员列表中 N 人收到通知，但 M 人已阅读/忽略。`MarkAllRead` 的语义在群组中如何定义？
- 通知去重：用户同时是群组成员 + 被 @ 直接提及，不应收到两条重复通知。
- 群组退出/禁言：退出群组后不应再收到历史消息的延迟通知。

**预期架构变更**：

```
接口变更：
  service/group_service.go (新):
    - CreateGroup, JoinGroup, LeaveGroup, SendGroupMessage
    - GroupMessage → outbox event (group.message.sent)

通知变更：
  NotificationDispatcher:
    - 新增 batchHandle 方法，接收 []*outbox.Event
    - 支持事件：group.message.sent → 1条通知 × group.members
    - 批量 insert 而非逐条
  
基础设施：
  outbox/relay.go: 当前逐条调用 handler.Handle，需支持 batch handler 接口
  或：group service 直接在事务内写 outbox events，每条 event 对应一个成员
```

**当前 outbox 的局限**：
- `outbox.Handler` 接口是 `Handle(ctx, *Event) error`，逐条处理。群组场景需要 `HandleBatch(ctx, []*Event) error`。可以考虑新增一个可选的 `BatchHandler` 接口，relay 检测到 handler 实现则走 batch 路径。
- 当前 relay 每次 `drain` 只取 `batchSize`（默认 100）个事件。如果一条群组消息需要扇出 1000 条 event，需要 10 轮 drain。更优方式：group service 写入一条聚合 event（含 N 个成员列表），dispatcher 收到后批量展开。

**对推送层的影响**：
- SSE hub 不受影响（每条通知仍然是一个 `Publish` 调用）
- Push notification（方向四）的扇出同理：dispatcher 需要控制 pusher 调用的并发度/速率，避免同时向 1000 个手机推送拖垮下游

---

### 方向 C：实时推送分层 —— SSE 与通知渠道解耦（P1）

**为什么需要**：当前 `NotificationDispatcher` 在 `Handle` 方法的最后一步调 `d.pusher.Publish()`——SSE 推送和通知创建是同步绑定的。这带来了几个问题：
1. SSE 推送失败（hub channel full / 用户离线）不应该导致通知创建失败——当前确实是 best-effort 的，但语义没有明确表达。
2. 如果将来支持 Push Notification（APNs/FCM）、WebSocket 等其他渠道，需要在 dispatcher 里逐个添加。
3. SSE 是实时渠道，push notification 是尽力投递渠道，两者的可靠性要求和重试策略不同。

**核心挑战**：
- 如何设计一个渠道无关的"推送通知"抽象，让 dispatcher 不需要感知有哪几种渠道
- 渠道失败隔离：SSE 掉线不影响 push notification，push notification 失败不影响 notification row 的写成功
- 渠道选择逻辑：用户在线走 SSE，离线走 push，24h 不活跃不发——这需要在 dispatcher 之外做

**预期架构变更**：引入渠道中间件模式

```
当前（紧耦合）：
  Dispatcher.Handle → create notification row → SSE pusher

目标（松耦合）：
  Dispatcher.Handle → create notification row → async channel fan-out
                                                     ├─ SSE (drop on full)
                                                     ├─ PushNotification (retry 3x)
                                                     ├─ WebSocket (drop on full)
                                                     └─ Email (low-pri, batch)
```

**实现建议**：在 `outbox.Handler` 和 `NotificationDispatcher` 之间插入一个异步扇出层，而非在 dispatcher 内部逐个推。dispatcher 只负责创建 notification row，然后发布到一个内部的 `chan NotificationPublishEvent`，多个独立的 fan-out goroutine 消费。

```
NotificationDispatcher.Handle
  → create row in DB
  → enqueue to internal chan (非阻塞，channel full = 降级丢弃)

fan-out goroutines (per channel type):
  SSE goroutine:   reads chan → sseHub.Publish (非阻塞)
  Push goroutine:  reads chan → push notification service (阻塞, 重试)
  Webhook:         reads chan → webhook call (超时控制)
```

**与现有的 hub.Chain 的关系**：当前 `outbox.NewChain` 是同步顺序链——handler1 → handler2 → handler3。实时推送扇出需要的是**扇出链**（fan-out chain）：一个 handler 的产出派发给多个下游，彼此隔离失败。

---

### 方向 D：内容寻址文件存储 + 去重重构（P2）

**为什么需要**：当前文件去重逻辑在 `fileService.Create` 中：sha256 计算 → 查 `FindByChecksum` → 复用已有 `storage_key`。存在几个问题：
1. 去重范围是 `(owner_uid, checksum)`——同一用户多次上传相同文件才复用，不同用户上传相同文件仍会存多份。
2. 存储引用计数（`StorageRefRepository.Increment/Decrement`）是一个独立系统，`fileService.Delete` 中有两套逻辑（refs 模式 vs 传统 `CountByStorageKey`）。
3. "先写入 blob，再 checksum 去重"——如果文件已存在，需要 Delete 刚写入的 blob，产生不必要的 I/O。

**核心挑战**：
- 全局去重 vs 安全和隔离：文件 hash 碰撞风险、隐私隔离要求（用户 A 不应通过 hash 获得用户 B 的文件访问权）
- 引用计数的原子性：`Increment` 在 `fileService.Create` 中是 best-effort 的，`refs.Increment` 失败被注释为"no worse than the legacy heuristic"

**预期架构变更**：

```
storage/ 接口扩展:
  Put 增加选项: WithDedup(bool) / WithChecksum(string) — 存储层优先检查已有文件

文件表扩展:
  content_hash TEXT NOT NULL UNIQUE (全局内容寻址，而非 owner-level)
  ref_count INT NOT NULL DEFAULT 1 (存储层管理，而非应用层)
  
删除路径简化:
  Delete → decrement ref_count → ref_count == 0 才删除 blob
```

**权衡**：
- 全局去重提升存储效率，但增加了安全考量（需要 content hash 无法被用于快速枚举或访问绕过）
- 存储结构变更需要迁移脚本和双写兼容（旧文件没有 content_hash）
- 建议延期至存储成本成为可见问题后再实施

---

### 方向 E：领域事件版本化与 Schema Registry（P2）

**为什么需要**：当前 outbox event 的 payload 是无版本的 JSON，`messageSentPayload`、`messageReadPayload` 等结构体在多个地方重复定义。随着系统演进：
- 事件结构变化（字段改名/新增/删除）无从感知
- 新旧 dispatcher 版本同时存在时，旧版本读不懂新格式的 payload
- webhook 外部消费者依赖事件格式的稳定性

**核心挑战**：
- 版本策略：兼容（新增字段）还是断裂（v1→v2）？
- 事件注册：如何让消费者知道自己消费的事件有新版可用？
- 跨服务事件契约：当前 `outbox` 进程内消费，但如果未来拆成独立服务（MQ），事件格式的稳定性会变成服务质量承诺

**建议**：
1. **立即做**：将事件 payload 类型定义统一提取到 `domain/event_types.go`，消除 `internal/notification/dispatcher.go` 和 `internal/service/notification.go` 中的定义重复
2. **短期**：在每个 event payload 结构体中加 `Version` 字段（int），初始为 1
3. **中期**：如果引入跨进程事件投递（MQ），考虑 protobuf 做 event schema + `buf`/`protoc` 做 schema registry

---

## 三、接口设计建议

### 3.1 核心原则

1. **service 接口是契约**：当前 `service.FileService`、`service.MessageService` 等接口是 aero-id 最核心的架构契约。任何新能力的加入应该反映在这里。接口方法的命名应统一为动词 + 名词（`Create`、`Get`、`List`、`Delete`、`UpdateMetadata`），而非 HTTP 动词（`PostFile`、`GetFile`）。

2. **`With*` 选项模式应继续**：`service.FileOption`、`service.DispatcherOption` 这种函数式选项模式让可选依赖的注入不破坏构造函数签名，是新能力加入的标准方式。

3. **新增 `BatchHandler` 接口**：群组场景需要批处理能力

```go
// outbox/batch.go
type BatchHandler interface {
    HandleBatch(ctx context.Context, events []*Event) error
}

// relay 中检测
if bh, ok := r.handler.(BatchHandler); ok {
    bh.HandleBatch(ctx, events)
} else {
    for _, e := range events {
        r.handler.Handle(ctx, e)
    }
}
```

### 3.2 需要新增的抽象层

**推送渠道抽象**（方向 C）：

```go
// internal/service/notifier.go
type Notifier interface {
    // Deliver attempts to deliver a notification through this channel.
    // Returns nil on success; returns an error when delivery is known to
    // have permanently failed (e.g., invalid device token).
    // Returns ErrWillRetry for transient failures.
    Deliver(ctx context.Context, n *domain.Notification) error
}

type NotifierFunc func(ctx context.Context, n *domain.Notification) error

func (f NotifierFunc) Deliver(ctx context.Context, n *domain.Notification) error {
    return f(ctx, n)
}

// FanOutNotifier delivers to multiple notifiers concurrently.
// Each notifier's failure is independent; FanOutNotifier aggregates
// errors but does not stop other notifiers from running.
type FanOutNotifier struct {
    notifiers []Notifier
}

func (f *FanOutNotifier) Deliver(ctx context.Context, n *domain.Notification) error {
    var g errgroup.Group
    for _, nf := range f.notifiers {
        nf := nf
        g.Go(func() error { return nf.Deliver(ctx, n) })
    }
    return g.Wait() // returns first error; non-critical channels may log and swallow
}
```

### 3.3 向后兼容策略

1. **接口扩展用选项模式**：`With*` 注入的新功能不改变现有接口签名
2. **新增字段用指针（`*string`、`*int64`）**：DTO 中的可选字段用指针，nil=未设置，保持零值有效
3. **事件 payload 加 `version` 字段**：初始版本为 1，新增字段不改变版本号，破坏性变更升版本号
4. **HTTP API 加版本前缀**：当前是 `/v1/`，如果新增配额配额返回 402，属于语义兼容变更，不需要升版本

---

## 四、技术选型评估

### 4.1 当前技术栈评估

| 选型 | 评估 | 建议 |
|---|---|---|
| **gin** | 稳定，well-tested，性能足够 | ✅ 保持 |
| **gorm** | 当前工作良好，但 group 查询/批量 insert/原始 SQL 不如 sqlx 灵活 | ⚠️ 监控性能瓶颈，必要时局部使用 sqlx/sql.DB |
| **zap** | ✅ 性能最优 | 保持 |
| **viper** | ✅ 标准选择 | 保持 |
| **testcontainers** | ✅ 集成测试必备 | 保持 |
| **golang-migrate** | ✅ 简单可靠 | 保持 |

### 4.2 是否需要引入新技术

**短期（P0-P1，无需引入新依赖）：**
- 存储配额：纯 SQL + service 层逻辑
- 群组消息：扩展现有 outbox + repository 层
- 推送渠道解耦：纯接口设计，无需新依赖

**中期（需引入）：**
- **NATS / Redis Streams**【看情况】：如果需要多实例水平扩容 outbox relay。当前单进程轮询满足需求，多实例时需避免重复消费
- **FCM/APNs 推送库**【方向四需要】：`firebase.google.com/go/v4/messaging` 或 apple 的推送库
- **protobuf event schema**【方向 E】：已在使用 protobuf（`aeropb`、`ssopb`），outbox event payload 迁移到 protobuf 是自然扩展

**不建议引入：**
- **外部 MQ（如 Kafka/RabbitMQ）**：当前架构的 outbox + relay 模式足以应对预期负载。引入 MQ 增加运维复杂度，且需要处理 at-least-once 投递语义与当前 outbox 重试逻辑的冲突。
- **Redis 做通知缓存**：当前通知不存在读取瓶颈。如果监控显示 `notification.ListByRecipient` 成为瓶颈，再引入 read-through cache 不迟。
- **分布式配额服务**：单个 aero-id 实例的配额检查不需要分布式协调。`UPDATE … SET used_bytes = used_bytes + ? WHERE used_bytes + ? <= quota` 是原子的。

### 4.3 自建 vs 集成

| 能力 | 选型 | 理由 |
|---|---|---|
| 配额限制 | 自建（service + SQL） | 业务逻辑简单，与文件服务的生命周期绑定，不值得拆出独立服务 |
| 推送通知 | 集成（firebase/APNs） | 系统通知通过 FCM/APNs 投递，复用基础设施，不需要自建推送通道 |
| 事件 schema registry | 自建（Go struct + version） | 当前所有消费者在同一进程中，不需要分布式 registry。如果拆分 event 投递到 MQ，再引入 protobuf + schema registry |

---

## 五、实施路线图

### P0 —— 当前版本（立即做）

| 项目 | 预估工作量 | 依赖 |
|---|---|---|
| **1. 清理新旧 dispatcher 共存** | 1d | 无 |
| 删除 `internal/notification/dispatcher.go`，确认 `internal/service/notification.go` 中的 `NotificationDispatcher` 为唯一实现 |  | |
| **2. 存储配额** | 3-5d | 新增迁移脚本 |
| `storage_quotas` 表 + `QuotaRepository` + `fileService.WithQuotaChecker` + 配额超出错误码（HTTP 402 / gRPC ResourceExhausted） |  | |
| **3. SSE 失败隔离语义化** | 1d | 无 |
| `NotificationDispatcher` 中 SSE push 失败不返回 error（当前行为已正确但未显式注释）。添加 `WithPusher` 的文档说明"pusher 失败不影响通知持久化" |  | |

**验收标准**：
- `make test && make lint` 通过
- 新建配额表迁移脚本（.up.sql + .down.sql）
- `./hack/integration-test.sh` 通过（配额场景）
- 文件上传超配额时返回 `402 Payment Required`

### P1 —— 下一迭代

| 项目 | 预估工作量 | 依赖 |
|---|---|---|
| **4. event payload 类型统一** | 1d | 无（可先于群组做） |
| 提取 `domain/event_types.go`，消除 payload 定义重复，加 `Version` 字段 |  | |
| **5. `BatchHandler` 接口** | 2d | P0-1 |
| `outbox` 包新增 `BatchHandler` 接口 + relay 检测 + 测试 |  | |
| **6. 群组消息基础能力** | 8-10d | 4, 5 |
| `domain.Group`、`service.GroupService`、`repository.GroupRepository`、群组消息 outbox event、`group.message.sent` 的批量通知扇出 |  | |
| **7. 推送渠道抽象** | 3-5d | 6 |
| `Notifier` 接口 + `FanOutNotifier` + dispatcher 改为 publish-to-channel + 独立 SSE/push goroutine 消费 |  | |

**验收标准**：
- 群组创建/成员管理/群组消息发送可运行
- 100 人群发扇出 100 条通知在 2 秒内完成
- SSE 推送失败不影响 notification row 创建
- `make check-file-sizes` 无违规（注意群组服务文件大小限制）

### P2 —— 储备

| 项目 | 预估工作量 | 依赖 |
|---|---|---|
| **8. 内容寻址 + 全局去重** | 5d | 无（但需验证存储 ROI） |
| content_hash 唯一约束 + 跨用户去重 + 引用计数重构 |  | |
| **9. 事件版本与 schema 管理** | 3d | P1-4 |
| protobuf event payload + registry |  | |
| **10. 多实例水平扩容准备** | 5d | P1-7 |
| outbox relay 的分布式协调（postgres advisory lock / listen-notify）+ SSE 粘性会话路由 |  | |

### 风险点与缓解策略

| 风险 | 可能性 | 影响 | 缓解 |
|---|---|---|---|
| **配额原子性理解错误** | 低 | 高 | 集成测试覆盖并发上传场景（testcontainers + goroutine 并发） |
| **群组扇出导致 DB 写入放大** | 中 | 高 | 流量预估：100 人群消息每秒 10 条 → 1000 notification rows/s。监控写入延迟。预研 COPY 协议批量 insert |
| **SSE Hub 成为单点瓶颈** | 低（当前）→ 高（多实例）| 中 | 多实例时需引入外部 pub/sub（NATS/Redis）。当前单实例足够 |
| **事件版本不兼容导致 consumer 沉默** | 中（长期）| 中 | 尽早建立 event payload 版本化规范，protobuf 兼容性检测 |
| **群组隐私泄露** | 中 | 高 | code review 重点：群组成员列表的可见性控制、通知中的群组名称暴露 |

---

## 总结

当前 aero-id 的架构是一个**严格遵守分层纪律的 monolith**，其 outbox + relay + dispatcher 的异步通知链路是架构中最具前瞻性的部分。五个扩展方向中：

- **P0**（存储配额 + SSE 语义化）是安全基线，应该立即做
- **P1**（群组 + 推送渠道解耦）是产品演进的核心路径，但要谨慎处理扇出放大的性能影响
- **P2**（内容寻址 + 事件 schema + 多实例）是系统成熟后的规模化准备，不必过早优化

关键的架构原则始终是：**保持 outbox 作为异步副作用的单一入口**，所有的新能力（配额、群组、新推送渠道）要么作为 service 层的选项注入，要么作为 outbox handler chain 的一环。不破坏当前的单向依赖链和分层契约。
