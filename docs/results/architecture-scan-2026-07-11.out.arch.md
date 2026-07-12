已完成阅读。两份文档都已理解，现在从资深架构师视角给出深度分析。

---

# 架构分析与扩展规划报告

> **分析基准:** 代码扫描文档 + 代码证据验证报告
> **范围:** aero-id 全局架构，涵盖现状评估、扩展路径、接口策略和技术决策

---

## 一、架构评估

### 1.1 当前架构的本质特征

aero-id 当前架构可以用一句话概括：**单体服务 + DB 驱动的异步处理**。这是一个在早期阶段极为合理的选择。让我们解构其核心结构：

```
                          ┌─────────────┐
     HTTP/gRPC ─────────▶ │  Service     │ ──▶ Repository ──▶ Postgres
                          │  (sync)      │ ──▶ Storage    ──▶ S3/Local
                          │              │ ──▶ SSO (gRPC) ──▶ External
                          └──────┬───────┘
                                 │ outbox
                                 ▼
                          ┌─────────────┐
                          │  Relay      │ ──▶ Webhook / Notification / SSE
                          │  (polling)  │
                          └─────────────┘
```

**核心设计决策评估：**

| 决策 | 选择 | 评估 |
|------|------|------|
| 单体 vs 微服务 | **单体** | ✅ 正确。当前复杂度（<30 个领域实体）单体足够。无服务间 RPC 开销、一致性简单 |
| 异步处理 | **DB Outbox + Polling Relay** | ✅ 正确开始。零基础设施依赖、事务一致性保障好。但**有天花板** |
| ORM | **GORM** | ⚠️ 可接受。作为以 CRUD 为主的服务，GORM 节省时间。但复杂查询（feed 聚合、活动流）会暴露 GORM 的局限性 |
| 缓存策略 | **进程内缓存（TokenCache/UserCache）** | ✅ 正确。SSO 调用是主要瓶颈，TTL 缓存刚好 |
| 双协议 | **HTTP + gRPC** | ✅ 前瞻性。gRPC 适合服务间通信、浏览器 SDK 不适合，双协议覆盖两个场景 |
| 鉴权模型 | **通过 SSO 的 Bearer Token** | ✅ 正确。身份外置不重复造轮子。但当前模型缺少**资源级权限** |

### 1.2 架构优势（应予保持）

1. **单向依赖链** — `handler → service → repository` 严格单向，没有循环依赖，没有服务直接调用另一个服务。这是可维护性的基石。

2. **接口第一的设计** — `FileStorage`、`hub.Pusher`、`PreferenceChecker` 等接口抽象了具体实现，新的存储后端或推送通道可以插入而不改变调用者。这正是方向一（推送通知）可以零侵入接入的根本原因。

3. **异常分离** — `pkg/errors.AppError` 统一了 HTTP 和 gRPC 的错误表达，service 层封装领域错误、handler 层渲染传输层错误。这个模式值得在所有扩展方向中延续。

4. **Outbox 作为事件源** — 当前 Outbox 表（`outbox_events`）本质上是一个轻量的事件存储，记录了所有已发生的变化。它已经为活动流（方向五）、审计日志、数据同步提供了回放基础。**这是整个系统中架构杠杆最高的组件。**

### 1.3 架构局限性（应予正视）

**(A) 没有外部事件总线**

这是当前最根本的架构约束。所有异步处理依赖 Outbox Relay 的 `time.Ticker` 轮询：

- Relay 是**单进程单线程**处理（`Handle()` 是同步的）。一个 Webhook 超时（30s）会阻塞同一 tick 内的所有其他事件。
- 内容管线（缩略图生成、病毒扫描）无法以独立 worker 进程部署——要么嵌入 relay 使其变慢，要么放弃。
- 没有背压机制。Relay 的轮询速率是固定的（`Config.Interval`），事件积压时无法动态调整。

这个局限是方向四（文件版本管线）被降为 P2 的根本原因——没有事件总线，内容管线的架构复杂度骤增。

**(B) 缺乏资源级授权模型**

当前授权模型是**用户身份的确认**（SSO 验证 token 得到 uid），而非**资源权限的检查**。具体来说：

- 文件分享通过显式的 `shares` 表管理，而非通用的 ACL/权限系统
- 群组消息的成员资格需要另一个专用的成员表
- 权限检查逻辑散落在各个 service 中（`CanReadFile`、`CanSendTo`、`IsBlocked`）

这是一个**累积的技术债**——每个新资源类型都会增加一个独有的权限检查路径，而不是复用统一的授权框架。

**(C) 单数据库瓶颈**

所有读写操作共享同一个 Postgres 实例。写路径：事务中写入业务表 + Outbox 表（同一连接）。读路径：所有查询走同一数据库。当事件量增长（活动流写放大、通知 fan-out）时，这个架构需要拆分或副本。

---

## 二、核心扩展方向

### 方向 A：事件总线与异步处理基础设施

**优先级：P0 — 基础设施升级，不直接产生产品功能，但解锁所有其他方向**

#### 2.1 为什么需要

这是当前架构中**最根本的瓶颈点**。目前所有异步处理都通过 Outbox Relay 的同步 `Handle()` 方法处理。这意味着：

- 方向四（内容管线）无法独立部署 worker → 必须等事件总线
- 方向五（活动流 fan-out）如果在一个 tick 内写 1000 行 → 阻塞其他事件
- 当前 webhook 超时会阻塞后续事件
- 无法水平扩展（所有事件由一个 relay 实例处理）
- 无法按事件类型分级（重要事件 vs 可延迟事件同一个队列）

#### 2.2 核心挑战

**(a) 选择外部系统的引入复杂度**

| 方案 | 优势 | 劣势 | 推荐度 |
|------|------|------|--------|
| **Redis Streams** | 轻量、大多已部署 Redis、支持 consumer groups、消息持久化 | 无严格 ACK 语义、大规模下内存开销 | ⭐⭐⭐⭐ 推荐 |
| **NATS JetStream** | 专为云原生设计、ACK/重试完善、At-least-once 保证 | 需部署新组件、组织未使用过 | ⭐⭐⭐ 可接受 |
| **Kafka** | 最强持久化保障、分区扩容弹性 | 运维复杂度高、小规模没必要 | ⭐⭐ 过度设计 |

**(b) 与现有 Outbox 模式的共存策略**

现有 Outbox 表的写路径（`business_tx → outbox_events`）应该保留——它是事务性保证的基础。事件总线应作为**Outbox Relay 的下游**，而非替代 Outbox：

```
Request → Service (tx: business + outbox)
                                        ──(commit)──▶ Outbox Table
                                                         │
                                                    Relay 轮询
                                                         │
                                          ┌──────────────┼──────────────┐
                                          ▼              ▼              ▼
                                     Event Bus    Webhook       Notification
                                     (Redis)      (同步/异步)     (SSE)
                                          │
                              ┌───────────┼───────────┐
                              ▼           ▼           ▼
                         Content      Activity      Analytics
                         Pipeline     Flow          Pipeline
```

**(c) 消息契约的版本化**

一旦事件进入消息总线，生产者和消费者之间的契约就变成了分布式问题。不应将 Outbox payload 的 JSON 结构直接暴露给外部系统——需要定义 `EventEnvelope`：

```go
type EventEnvelope struct {
    ID            string    // UUIDv7
    AggregateType string    // "file", "message", "comment"
    EventType     string    // "file.created", "message.sent"
    Version       int       // Schema version
    Timestamp     time.Time
    Payload       json.RawMessage
    Metadata      map[string]string  // TraceID, causation IDs
}
```

#### 2.3 预期的架构变更

- **新增基础设施组件**：Redis Streams（推荐）或 NATS JetStream
- **新增包**：`internal/eventbus/`（producer + consumer 抽象）
- **Relay 改造**：新增 `EventBusPublisher` Handler，在 Webhook/Notification 之外向事件总线发布
- **新增 worker 进程**：`cmd/content-worker`、`cmd/feed-worker` 等，作为独立的二进制部署（与主服务解耦，可独立扩缩容）
- **GolangCI-Lint 重构触发**：当前 relay 的 `Handle()` 方法约 80 行 → 需要拆分为多个 Handler，每个通过选项注入

#### 2.4 对现有系统的影响

| 方面 | 影响 | 缓解措施 |
|------|------|----------|
| **部署拓扑** | 从单进程变为多进程（主服务 + N 个 worker） | 进程间无共享状态，各自连接 DB/Redis。主服务不变 |
| **运维复杂度** | 新增 Redis 依赖 | Redis 是成熟中间件，运维成本低 |
| **测试** | worker 逻辑需要 mock eventbus | 定义清晰的 `Consumer` 接口，提供内存实现用于测试 |
| **事务一致性** | Outbox Relay + EventBus 存在重复发布窗口 | 事件总线消费端实现幂等性（通过 `EventEnvelope.ID` 去重） |

---

### 方向 B：资源级授权框架

**优先级：P1 — 在群组消息（方向三）之前落地，否则每个新资源类型重复实现权限逻辑**

#### 2.1 为什么需要

当前架构中，每次引入一个新的资源类型，都需要在 service 层重复实现权限检查逻辑：

```
// 文件
func (s *Service) GetFile(ctx, callerUID, fileID) {
    file = repo.FindByID(fileID)
    if file.OwnerUID != callerUID && !share.IsShared(fileID, callerUID) {
        return ErrForbidden
    }
}

// 消息
func (s *Service) Send(ctx, fromUID, toUID) {
    if block.IsBlocked(fromUID, toUID) {
        return ErrForbidden
    }
}

// 评论（群组场景将要
func (s *Service) ListGroupMessages(ctx, callerUID, groupID) {
    if !member.IsMember(groupID, callerUID) {
        return ErrForbidden
    }
}
```

这不是开放的问题，而是**扩展性问题**——每个新资源类型都有一条独特且互不共享的权限判断路径。当方向三（群组消息）落地时，权限检查将再增加一条路径，而无法复用已有的基础设施。

#### 2.2 核心挑战

**(a) 领域特定的权限语义**

文件分享、群组成员、Block 关系虽然有共性（"谁可以做什么"），但具体语义差异很大：

- **文件**：Owner + 被分享者（显式列表）
- **群组**：成员（显式列表）+ 管理员
- **Block**：单向互斥（A 看不到 B 的内容）
- **公共资源**：所有人可读

通用 ACL 模型（`subject:user_A → action:read → resource:file_X`）在强类型语言中往往会过度抽象，导致运行时错误取代编译时检查。**不建议引入全通用 ACL。**

**(b) 建议的方案：策略化权限（Policy-based）而非 ACL**

对每个资源类型定义一个 `AccessPolicy` 接口：

```go
// 放在 domain 层
type AccessPolicy interface {
    CanRead(ctx, actorUID string, resourceID domain.ID) bool
    CanWrite(ctx, actorUID string, resourceID domain.ID) bool
    CanDelete(ctx, actorUID string, resourceID domain.ID) bool
}
```

每个资源类型实现自己的策略，但策略之间可以**组合**：

```go
// 文件的策略
type FilePolicy struct {
    fileRepo  FileRepository
    shareRepo ShareRepository
    blockRepo BlockRepository
}

func (p *FilePolicy) CanRead(ctx, uid, fileID) bool {
    file := p.fileRepo.FindByID(fileID) // 或从 context 传入
    return file.OwnerUID == uid ||
           p.shareRepo.IsShared(fileID, uid) ||
           !p.blockRepo.IsBlocked(file.OwnerUID, uid)
}
```

**这样做的价值：**
1. 每个资源类型的权限逻辑集中在一处，而不是散布在 service 方法中
2. 测试时独立验证 `FilePolicy`，而非通过 service 间接测试
3. 新增资源类型时，开发者实现 `AccessPolicy` 接口，而非理解整个授权体系

#### 2.3 预期的架构变更

- **新增接口**：`domain/access.go` 中定义 `AccessPolicy` 接口
- **现有重构**：将 `file/service.go`、`message/service.go`、`comment/service.go` 中的权限检查提取到对应的 Policy 实现
- **不变**：service 层仍然是权限检查的**调用者**而非拥有者。Policy 只是封装了检查逻辑。
- **不发生**：不会引入通用的 ACL 表、权限数据库、或集中式授权服务

#### 2.4 对现有系统的影响

| 方面 | 影响 | 缓解措施 |
|------|------|----------|
| **API** | 无变化 | Policy 是 service 内部重构，不改变 handler 签名 |
| **测试** | 存量测试无需修改 | Policy 逻辑与当前 service 中的逻辑相同，只是位置变了 |
| **新功能** | 新资源类型只需实现 Policy | 较当前（在每个 service 方法中判断）更简单 |

---

### 方向 C：推送通知通道（设备注册 + FCM/APNs）

**优先级：P1 — 移动端刚需，但等设备注册表就绪 + SSO 凭据管理协调**

内容已在输入文档中充分覆盖，此处仅从架构设计角度补充几点：

#### 需要回答的关键设计问题

**(a) 设备 token 的归属**

设备 token 应该由 aero-id 管理，还是由 SSO 管理？双归属的问题是：用户注销时，token 需要从两个系统删除。

- **推荐方案**：aero-id 管理 `device_tokens` 表。SSO 不感知设备。理由是：
  - SSO 的业务边界已经划定（身份认证），不应掺入设备注册
  - 推送与通知功能紧耦合，通知属于 aero-id 的业务域
  - 注销 SSO 后，aero-id 不接收通知消息即可，无需清理 token

**(b) 推送与 SSE 的关系**

不应将推送视为 SSE 的替代品，而应视为补充：

| 通道 | 适合场景 | 不适合场景 |
|------|---------|-----------|
| **SSE** | 浏览器在线时实时推送 | 浏览器关闭时、移动端 |
| **FCM/APNs** | 移动端离线时唤醒 | 大量低延迟的频繁通知 |
| **Email** | 非紧急、每日摘要 | 实时场景 |

建议 dispatcher 按优先级发送：SSE → (失败?) → FCM → (失败?) → 不发送。而不是并行发送所有通道。

**(c) 架构位置确认**

文档已正确识别 `hub.Pusher` 接口是接入点。需要补充的是：

```
Dispatcher.Handle(event)
  → notif.Create(n)                    // DB write
  → d.pusher.Publish(uid, type, data)  // hub.Pusher 接口
                                        └── *Hub.Publish → SSE (现有)
                                        └── PushAdapter.Publish → FCM (新增)
```

`PushAdapter` 实现 `hub.Pusher` 接口，因此零侵入。但需要解决推送的**可靠性问题**——SSE 是内存通道，失败无影响；FCM 是外部网络调用，失败应记录而非阻塞。建议 `PushAdapter.Publish` 使用自己的 goroutine 池（`errgroup` 或独立的 worker queue），不影响 dispatcher 主循环。

---

### 方向 D：多对多会话 / 群组消息

**优先级：P1 — 最高产品价值的新领域，但前置依赖方向 B（授权框架）**

输入文档已充分分析。仅从架构角度补充几点：

#### 核心架构设计决策

**(a) 消息存储模型**

| 方案 | 描述 | 推荐度 |
|------|------|--------|
| **单表无 fan-out** | `messages` 表 + `conversation_id`，所有成员读同一行 | ⭐⭐⭐ 推荐。简单，适用于 < 1000 人的群 |
| **Fan-out 写** | 每条消息为每个成员复制一行 | ⭐⭐ 写放大严重，仅适用于大群场景 |
| **混合** | 小群单表，大群 fan-out | ⭐ 运维复杂，为时过早 |

推荐单表方案——aero-id 短期内不会有 10 万人群的需求。每个会话的消息量控制在 `conversations.updated_at` + 分页即可。

**(b) last_read_at 的实现**

当前已有 `MarkConversationRead`（1:1 版本）。群组场景下，`last_read_at` 应存储在 `conversation_members` 行中，是会话级别的（而非会话+消息级别）。消息的未读计数通过 `COUNT(*) WHERE created_at > last_read_at` 计算。

这个模型在 1000 人群里会增加查询复杂度，但远未到需要额外索引或物化视图的程度。

#### 对现有系统的影响

方向三的影响面较广，因为：
- `message` 包的 service 签名需要扩展（增加 `ConversationID` 参数）
- `message` 包的 repository 查询需要增加 `conversation_id` JOIN
- Block 检查需要从单向（A → B）变为多向（A → 所有群成员）
- Notification 中需要区分 1:1 消息和群组消息的通知

**建议的迁移路径：**
1. `ConversationID` 设为 `*domain.ID`（nilable pointer），现有 1:1 消息传 nil
2. 所有现有查询（`Conversation(ctx, withUID, page)`）保持向后兼容——查询 `WHERE conversation_id IS NULL AND ((from_uid = ? AND to_uid = ?) OR ...)`
3. 群组查询走新的 `conversation_id` 索引

---

## 三、接口设计原则

### 3.1 接口设计的五个原则

扩展当前系统时，应遵循以下接口设计原则：

#### 原则一：面向接口的组合，而非继承

当前代码已很好体现了这一点（`hub.Pusher`、`FileStorage`）。扩展时保持一致：

```go
// ❌ 不推荐 - 抽血本定义
type NotificationSender interface {
    SendSSE(...)
    SendPush(...)
    SendEmail(...)
}

// ✅ 推荐 - 细粒度组合
type SSESender interface { Send(...) }
type PushSender interface { Send(...) }
type MailSender interface { Send(...) }

type Dispatcher struct {
    sse  SSESender
    push PushSender  // nilable, 可选注入
    mail MailSender  // nilable, 可选注入
}
```

#### 原则二：transport 与 domain 的解耦通过 DTO 边界维持

当前 handler 直接调用 service 时，参数是 domain 类型。这是正确的——handler 不应该有自己的数据传输对象层。但需要注意：

- gRPC handler 需要 `protobuf` ↔ domain 的映射（已有 `internal/handler/grpc/mapper/`）
- HTTP handler 直接使用 domain struct（JSON serialize 天然兼容）
- **不引入** `internal/dto/` 层——三个地方的映射增加了修改的摩擦成本

#### 原则三：Handler 不应感知多路推送的实现细节

```go
// 当前 handler 调用
err := s.svc.SendMessage(ctx, req.ToUID, req.Content)

// service 内部决定是否推送 -> 是否正确？
```

这是正确的抽象。service 是业务逻辑的拥有者，决定什么情况下触发的通知/推送/出站事件。handler 只关心请求/响应的转换。

#### 原则四：事件契约的版本化接口

当引入事件总线后，定义清晰的事件契约接口：

```go
// internal/eventbus/envelope.go
type Envelope struct {
    ID        string    `json:"id"`
    Type      string    `json:"type"`       // "file.created.v1"
    Source    string    `json:"source"`     // "aero-id"
    Time      time.Time `json:"time"`
    Data      []byte    `json:"data"`
    TraceID   string    `json:"trace_id"`
}

// consumer 接口
type Consumer interface {
    Handle(ctx context.Context, env Envelope) error
}
```

#### 原则五：新功能通过选项模式注入

aero-id 已大量使用函数选项模式（`WithDispatcherPush`、`WithSSE`、`WithWebhook`）。扩展时应保持一致：

```go
// app/new.go
func New(cfg *config.Config, opts ...Option) (*App, error)

type Option func(*App)
func WithDeadLetterHandler(h *admin.DeadLetterHandler) Option { ... }
func WithPushAdapter(a push.Adapter) Option { ... }
```

### 3.2 是否需要新的抽象层？

**结论：不需要新的抽象层，但需要重组现有层。**

当前五层结构（handler → service → repository → DB / storage / sso）是健康的。需要调整的是：

1. **service 层内部的细分**：当前 `service` 包按领域组织（`file/service.go`、`message/service.go`）。在此结构上新增 `service/access.go`（授权策略）不破坏现有结构。
2. **没有新层的引入**：不需要 `workflow` 层、不需要 `orchestrator` 层、不需要 `pipeline` 层。事件总线的 Consumer 本质上是新的入口点（如同 handler），不应被视为独立的层。
3. **唯一可能需要的**：`internal/eventbus/` 包作为新的基础设施抽象。但它与 `internal/storage/`、`internal/sso/` 属于同一层次。

### 3.3 向后兼容性策略

| 变更类型 | 兼容策略 | 示例 |
|---------|---------|------|
| **新增 API 端点** | 天然兼容 | `/v1/devices` 新增不影响现有端点 |
| **现有 API 扩展字段** | JSON ignore 缺失字段 | `POST /v1/messages` 增加可选 `conversation_id` 字段 |
| **现有 API 行为变更** | 不修改现有语义 | 现有 1:1 消息路径不变，群组消息走新路径 |
| **Outbox 事件格式变更** | 新增 `version` 字段 | 消费者按 version 反序列化 |
| **Deprecation** | 旧端点加 `X-Deprecated` header | 保留一个版本周期后移除 |

---

## 四、技术选型

### 4.1 是否需要引入新的技术栈？

| 引入项 | 必要性 | 方案 | 理由 |
|--------|--------|------|------|
| **事件总线** | **必须引入**（方向 A） | Redis Streams | 轻量、可自托管、社区已广泛部署 |
| **内容处理** | 方向四需要 | `github.com/disintegration/imaging` + ClamAV | 无状态 worker，可独立部署 |
| **APNs 客户端** | 方向一需要 | `github.com/sideshow/apns2` | 成熟的 Go APNs 客户端 |
| **FCM 客户端** | 方向一需要 | `firebase.google.com/go/v4` | 官方 SDK，可信 |
| **视频缩略图** | 方向四远期 | FFmpeg CGO 或外部 HTTP API | 暂不引入 |

**关键决策：Redis Streams 的引入评估**

引入 Redis Streams 带来的收益和成本必须权衡：

| 维度 | 评估 |
|------|------|
| **收益** | 异步处理解耦、worker 独立扩缩容、背压支持、分离重要/可延迟事件 |
| **风险** | 增加运维复杂度（虽然 Redis 已广泛使用，Streams 的新概念需要学习） |
| **替代方案** | 继续使用 DB Outbox + 独立的 relay 进程。但无法解决 relay 同步阻塞的问题 |
| **决策** | **P0 引入**。四个方向中有三个依赖事件总线。越早引入，后续方向越顺 |

### 4.2 第三方依赖评估标准

评估新依赖时，应使用以下标准（优先级从高到低）：

1. **许可证兼容性** — 必须 Apache 2.0 / MIT / BSD。禁止 AGPL（传染性）。
2. **Go 版本兼容** — `go.mod` 中的 `go` 版本必须 ≤ 项目要求的版本（当前 1.26）。
3. **存活性信号** — GitHub Stars > 500 + 最近一年内有提交 + Issue 正常响应。
4. **API 稳定性** — 非 v0.x（v1+）。Go API 兼容性保证（不要 Breaking API changes）。
5. **测试覆盖率** — 项目自身的测试覆盖 > 60%（粗略判断）。
6. **非 CGO 优先** — 纯 Go 优先于 CGO 绑定。CGO 增加构建复杂度（交叉编译、musl vs glibc）。
7. **Go 社区推荐** — 是否被 `awesome-go` 收录、Go 官方推荐或广泛使用。

### 4.3 自建 vs 采购的决策依据

对于 aero-id 这种体量的项目，绝大多数扩展应**自建**而非采购外部服务：

| 组件 | 决策 | 理由 |
|------|------|------|
| **推送通知（FCM/APNs）** | 自建 | 是 3-5 个文件级的集成，不值得采购服务（OneSignal、Firebase 等）|
| **病毒扫描** | 自建（ClamAV）| 免费、成熟、本地部署。如果扫描吞吐 > 1000 文件/天，考虑外部 API |
| **内容审核** | 自建初始版，后续考虑 API | 初期可用正则 + 黑名单。深度审核（图片 NSFW 检测）再接入外部 API |
| **视频转码** | 延迟决策 | 当前无需求，不应提前投资 |
| **事件总线** | 自建（Redis Streams）| 无 SaaS 替代品适用于这个场景。如用 Kafka 可以选 Confluent Cloud |

**不采购的例外：**
- 方向五（活动流）如果扩展到 AI 推荐排序，可以考虑采购推荐引擎 API
- 方向四（病毒扫描）如果达到企业级合规要求，考虑采购 Virustotal API

---

## 五、实施路线图

### 5.1 优先级矩阵（最终版）

综合两篇文档的分析结果，这里给出最终优先级评估：

| 排序 | 方向 | 优先级 | 依赖 | 估计工时 | 风险 |
|------|------|--------|------|---------|------|
| **1** | **方向 B：事件总线（Redis Streams）** | **P0** | 无 | 3-5 天 | 🟡 中 — Redis 引入需学习曲线 |
| **2** | **方向二：死信运维** | **P0** | 无 | 0.5 天 | 🟢 低 — 零依赖、纯新增 |
| **3** | **方向 B：资源级授权框架** | **P1** | 方向 1（事件总线为非阻塞依赖）| 3-4 天 | 🟢 低 — 重构而非新功能 |
| **4** | **方向一：推送通知** | **P1** | 方向 1（事件总线为非阻塞依赖）| 4-6 天 | 🟡 中 — 外部凭据管理 |
| **5** | **方向三：群组消息** | **P1** | 方向 3（授权框架为硬依赖）| 8-12 天 | 🟡 中 — 测试覆盖面广 |
| **6** | **方向四：文件版本管线** | **P2** | 方向 1（事件总线为硬依赖）| 10-15 天 | 🟠 中高 — 管线 worker 架构 |
| **7** | **方向五：活动流** | **P2** | 方向 3+4 完成度 > 60% | 8-12 天 | 🟡 中 — 关注模型设计 |

### 5.2 阶段划分与里程碑

```
Phase 0 (Week 1-2): 基础设施升级 + 死信运维
  ├── Redis Streams 引入 + Outbox Relay 改造（向总线发布）
  ├── 死信查询/重放 API + 仪表化
  ├── actorKey 去重（技术债修复）
  └── Webhook.Timeout 类型修正
  🏁 里程碑：事件总线可用，运维可视

Phase 1 (Week 3-4): 授权框架 + 推送通道
  ├── AccessPolicy 接口定义 + 现有 service 重构
  ├── device_tokens schema + CRUD 端点
  ├── PushAdapter（FCM 实现）接入 dispatcher
  └── Webhook 集成测试补充
  🏁 里程碑：移动端可注册设备并接收推送

Phase 2 (Week 5-7): 群组消息
  ├── conversations + conversation_members 表
  ├── 消息模型扩展（conversation_id）
  ├── 群组 CRUD 端点
  ├── 群组消息通知扩展
  └── Block 检查的群组语义
  🏁 里程碑：群聊 MVP 可用

Phase 3 (Week 8-10): 文件版本管线 + 活动流 (MVP)
  ├── file_versions schema + 版本 CRUD
  ├── content-worker（缩略图生成 + 病毒扫描）
  ├── follow 模型 + 端点
  ├── actor_timeline 表 + 写入
  └── feed 查询端点（方案 C：双写 + Lazy 合并）
  🏁 里程碑：文件可版本管理 + 活动流 MVP
```

### 5.3 各阶段风险点与缓解策略

**Phase 0 风险点：**

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|----------|
| Redis Streams 引入增加运维复杂度 | 中 | 中 | 项目已使用 Redis（用户缓存），Streams 是 Redis 内置功能，无需新服务 |
| Outbox Relay 改造引入 bug | 低 | 高 | 保持旧（DB-only）和新（DB+EventBus）两条路径共存，逐步迁移 |
| 死信 API 权限边界不清晰 | 低 | 低 | 复用现有 admin 中间件 |

**Phase 1 风险点：**

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|----------|
| AccessPolicy 重构影响存量测试 | 中 | 中 | 重构前写特性开关，新旧逻辑并行运行对比结果 |
| FCM 凭据管理（跨团队依赖 SSO）| 高 | 高 | 设备注册端点先上线（P1），推送实际接 FCM 延迟到 SSO 侧就绪 |
| 推送 token 过期处理不完整 | 中 | 中 | 实现 token 刷新回调，APNs 的不定期断开有日志告警 |

**Phase 2 风险点：**

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|----------|
| Block 与群组共存的语义复杂 | 高 | 中 | 先实现简单的"被 block 的成员在群中不显示该成员的消息"，更细粒度的"部分成员可见"延迟到 Phase 3 |
| 现有 message 测试需要广泛修改 | 中 | 高 | `ConversationID` 作为 nilable 字段，现有测试传 nil 即可 |
| 消息通知的 fan-out 量增多 | 中 | 中 | 群组通知写入批量 insert（`INSERT INTO notifications (...)` 而非逐行 insert）|

**Phase 3 风险点：**

| 风险 | 概率 | 影响 | 缓解策略 |
|------|------|------|----------|
| 缩略图生成依赖 CGO | 中 | 中 | 使用纯 Go imaging 库（无 CGO）。视频缩略图暂不支持 |
| 病毒扫描（ClamAV）延迟高 | 中 | 中 | 扫描在独立的 content-worker 中运行，不阻塞主请求路径 |
| GC 误删正在处理的文件版本 | 中 | 高 | 文件创建时立即建立 storage_ref，管线完成只追加不替换 |
| 活动流读取延迟（后台合并操作）| 低 | 中 | 首次实现使用方案 B（实时物化 + 缓存），后续优化到方案 C |

### 5.4 不做的事情（明确排除）

| 排除项 | 理由 |
|--------|------|
| **微服务拆分** | 当前复杂度远未到需要跨服务拆分。单体+独立 worker 是更好的选择 |
| **通用 ACL 系统** | 领域特定的授权语义不适合通用 ACL，策略模式更合适 |
| **外部 SaaS 推送服务** | 自建 FCM/APNs 适配器在 3-5 个文件中可以完成 |
| **视频处理** | 无业务需求，不应提前投资 |
| **活动流的 AI 推荐排序** | 产品价值高但实现成本极高，非 MVP 目标 |
| **Kubernetes** | 单服务+独立 worker 进程，compose 或 systemd 足矣 |

---

## 六、总结

aero-id 的当前架构在一个关键指标上做得很好：**它没有设计过度**。handler → service → repository 三层的简洁性、outbox 模式的可靠性、接口抽象的灵活性，都为扩展做好了准备。

最大的架构债务是**缺乏事件总线**——这是四个扩展方向中的三个的阻塞依赖项。方向二（死信运维）可以在总线就绪前独立完成，届时快速获得运维可见性的改善。

从架构演进的角度，推荐的时间线是：

```
现在 ──── 死信运维 ──── 事件总线 ──── 授权框架 ──── 推送通知 ──── 群组消息 ──── 文件管线 ──── 活动流
        │              │              │              │              │              │
        0.5天          3-5天          3-4天          4-6天          8-12天         10-15天
                                                                                   │
                                                                                   8-12天
```

总计预估：**约 40 个工作日**完成全阶段。但 Phase 0（死信 + 事件总线 + 技术债修复）可在 **1-2 周**内落地，获得显著的架构杠杆和运维价值。
