# 架构分析报告：aero-id 高价值扩展方向

> **分析对象**：`docs/requirements/architecture-scan-2026-07-11.md`
> **分析日期**：2026-07-12
> **分析角色**：资深架构师
> **基准版本**：基于当前代码库 `f0a1b2c`（假设 commit），Go 1.26

---

## 1. 架构评估

### 1.1 当前架构的优势

aero-id 的架构设计展现了几个经过深思熟虑的决策，值得肯定：

| 优势 | 具体体现 | 架构价值 |
|------|---------|---------|
| **严格单向依赖** | `handler → service → repository/storage/sso` | 防止循环依赖，测试友好，可独立替换后端 |
| **事务性 Outbox 模式** | 完整实现：同一事务写入领域行 + 事件，Relay 异步 drain | 保证最终一致性，不丢失通知/副作用 |
| **接口驱动适配器** | `FileStorage`、`hub.Pusher`、`outbox.Handler`、`block.BlockChecker` | 每个扩展点都有明确的接口，新实现无需修改调用方 |
| **Options 模式构建** | `DispatcherOption`、`NewRelay(..., metrics ...MetricsSink)` | 非侵入式扩展，零配置安全默认值 |
| **自包含指标** | 零依赖 Prometheus Registry，无 client_golang | 构建简单，部署无额外依赖 |
| **幂等设计** | outbox 事件按 ID 去重，Reaction 幂等，Block 幂等 | 天然支持重试和最终一致性 |
| **双协议共享 service** | HTTP + gRPC handler 调用同一 service 层 | 行为一致，无需重复编码业务逻辑 |

### 1.2 已识别但未解决的架构债务

扫描文档附录中标注了 7 项技术债务，我将其按影响面重新分类：

**需要立即处理（严重度：中-高）**

1. **`actorKey`/`WithActor`/`ActorFrom` 重复** — 同时存在于 `internal/service/service.go` 和 `internal/domain/actor.go`。这是直接的复制粘贴式债务，在后续代码变更中可能导致一处修改另一处遗漏的 bug。**建议**：统一到 `domain` 包中，`service` 包引用 `domain.ActorFrom`。

2. **`outbox.Relay` 无死信 gauge** — 仅有事件计数器，无当前死信积压量的 gauge。运维无法 Dashboard 上看到死信水位。**建议**：在 `MetricsSink` 中增加 `ObserveDeadLetterCount(count int)`，Relay 启动时 + 每个 tick 后记录。

**需要排期处理（严重度：低-中）**

3. **`OutboxConfig.Webhook.Timeout` 是 string 而非 time.Duration** — 类型选择不当，每次访问需手动 parse。**建议**：改用 `time.Duration` + viper 的 `DecodeHook` 或自定义 UnmarshalText。

4. **hub 每 uid 缓冲区固定为 16，无配置** — 慢消费者静默丢包，且运维无法调优。**建议**：暴露 `HubOption` 配置，或从 `HubConfig` 注入。

5. **设备注册端点无独立速率限制** — 全局限流已兜底，但 `POST /v1/devices` 可被单客户端打爆 token 表。**建议**：在端点级别叠加较紧的速率限制（如 10/min/uid）。

### 1.3 设计文档（Out of Scope）重评估

当前 `docs/specs/aero-id-design.md` 的 out-of-scope 声明中需要重新审视两项：

- **"不追求 90%+ 测试覆盖率"** — 随着系统复杂度上升，死信运维、推送通知、群组消息等新增功能若缺乏集成测试覆盖，将显著增加线上故障风险。建议**将集成测试覆盖率阈值提高到核心路径（消息发送、文件上传、通知派发）的 80%**。
- **"不为未来'拆微服务'做过度抽象"** — 这是正确的决策，但当前 `hub.Pusher`、`outbox.Handler`、`FileStorage` 的接口抽象天然具备了拆微服务的边界。只需确保新扩展（推送通知、内容管道）也沿用同一抽象模式，无需额外铺垫。

---

## 2. 扩展方向：高价值架构变更

### 2.1 方向一：推送通知通道（P0 · 建议 2-3 周）

#### 为什么需要

移动端覆盖是当前架构最显著的缺口。SSE 通道仅服务保持长连接的浏览器客户端；移动端用户只能降级到轮询未读数。从**用户感知的实时性**和**留存率**角度看，这是最高优先级的产品缺口。

从技术角度看，插入点已经就绪：
- `hub.Pusher` 接口已定义了 `Publish(uid, eventType, data)` 方法
- `notification/dispatcher.go` 在 SSE 推送前/后可插入额外推送
- 不修改任何现有 handler/service 的业务逻辑

#### 核心挑战

| 挑战 | 说明 | 架构应对 |
|------|------|---------|
| **设备 token 生命周期管理** | Token 会过期、更新、被应用卸载后失效 | 增 `device_tokens` 表，注册/注销端点，FCM/APNs 返回 `NotRegistered` 时静默清理 |
| **FCM 限速** | 600 QPS / project 的软限制 | 在 `Adapter.Send` 中引入令牌桶（`golang.org/x/time/rate`）或基于 channel 的背压 |
| **静默时段重复判断** | 通知已通过 `preference.IsAllowed` 过滤，推送不应重复判断 | 沿用 `WithDispatcherPreferenceChecker` 链路，推送适配器只做传输 |
| **批量推送性能** | 群组消息通知 N 个成员 → N 次推送 → QPS 放大 | 实现 batched send（FCM 的 `SendEach` 支持一次调用发多设备 token） |
| **payload 大小限制** | FCM 最大 4KB，APNs 最大 4KB | 通知 dispatcher 创建时确保序列化后不超限，超限则降级为"您有新消息" |

#### 预期架构变更

```
新增:
  internal/push/
    ├── adapter.go          # Adapter 接口: Send(ctx, uid, Notification) error
    ├── fcm.go              # FCM 实现 (firebase admin SDK 或 HTTP v1 API)
    ├── apns.go             # APNs 实现 (http2 + JWT 令牌)
    ├── adapter_test.go
    └── device.go           # DeviceRepository (GORM)

  migrations/*_device_tokens.up.sql
  internal/handler/http/device.go    # POST/DELETE /v1/devices
  internal/handler/grpc/device.go    # gRPC 等价端点

修改:
  internal/notification/dispatcher.go
    └── 新增 WithDispatcherPushAdapter(adapter) 选项
    └── Handle() 中 SSE 推送后同步调用 adapter.Send()

  internal/app/app.go
    └── 条件性组装 FCM/APNs adapter（由配置控制）
```

#### 对现有系统的影响

- **零侵入性**：推送适配器通过 `DispatcherOption` 注入，不修改现有 dispatcher 的构造签名
- **设备表隔离**：`device_tokens` 独立于 users 表（技术上的外键不存在，仅逻辑关联 `uid`）
- **新依赖**：需要引入 Firebase Admin SDK（或直接调用 FCM HTTP v1 API）和 APNs JWT 签名库。**建议**：优先使用 FCM HTTP v1 API（无需额外 SDK，仅需 HTTP 客户端 + JWT 库），避免第三方 SDK 的版本锁定风险。

#### 技术选型建议

| 选项 | 评估 |
|------|------|
| firebase-admin-go SDK | 功能完整但依赖较重 (~2MB) |
| 原生 FCM HTTP v1 API + golang-jwt | 更轻量，无 SDK 版本锁定，推荐 |
| 推送中间件如 gorush/pusher | 引入额外运维组件，暂不推荐——当前规模直接调用即可 |

### 2.2 方向二：死信队列运维（P0 · 建议 1 周，含 4 个快速获胜项中的 3 个）

#### 为什么需要

这是**运维成熟度的最低要求**。当前系统的死信是静默发生的——生产环境中当 webhook 端点故障或通知 handler 因数据异常 panic 时，事件进入死信但无人知晓。

**业务价值**：减少 MTTR，降低数据丢失风险。
**技术价值**：极低成本（2-3 个 repository 方法 + 2 个 handler 文件），杠杆率极高。

#### 核心挑战

| 挑战 | 说明 | 应对 |
|------|------|------|
| 并发重放同一事件 | 两个 admin 同时点击 replay → 两次 `dead_letter=false` 更新 | 幂等设计：每次 `MarkPublished` 按 ID 列表操作，重复 publish 无害 |
| 死信再失败的处理 | Replay 后的二次失败应继续走退避流程 | 不走特殊路径，重放仅重置状态到"未发布"，后续由 relay 按标准退避 |
| 自动清理策略 | 死信无限堆积 → 表膨胀 | 默认 retention 90 天，admin 可通过 API 配置清理 |

#### 预期架构变更

```
新增:
  internal/handler/http/admin_deadletter.go   # GET /v1/admin/dead-letters
                                               # POST /v1/admin/dead-letters/:id/replay
                                               # DELETE /v1/admin/dead-letters
  internal/handler/grpc/ops_deadletter.go     # gRPC 等价

修改:
  internal/outbox/repository.go → GormRepository:
    + FetchDeadLetters(ctx, page) ([]*Event, string, error)
    + ReplayEvent(ctx, id) error
    + PurgeDeadLetters(ctx, before time.Time) (int64, error)
  internal/outbox/outbox.go → Repository 接口:
    + 同上三个方法

  internal/pkg/metrics/metrics.go:
    + ObserveDeadLetterCount(eventType string, count int)  # gauge
```

#### 快速获胜项的推进策略

扫描文档列出了 4 个快速获胜项。我的执行建议：

| 天 | 项目 | 说明 |
|----|------|------|
| 第 1 天 | 死信查询 API + Replay | DB 查询 + admin 端点，无新依赖 |
| 第 2 天 | Outbox 延迟仪表化 | `metrics.ObserveDeadLetterCount` + alert rules |
| 第 2-3 天 | 设备注册端点（占位） | 仅 `device_tokens` 表 + CRUD，后续接推送引擎 |
| 第 3-4 天 | 文件版本 schema（占位） | 仅表 + 迁移，留 version 1 写入代码 |

### 2.3 方向三：群组消息（P1 · 建议 4-6 周）

#### 为什么需要

这是**产品能力的重大升级**。1:1 私信模式限制了消息模块在团队协作、客户服务、频道广播等场景的应用。

从架构视角看，消息系统的演进路径是自然且可分步实施的：

1. **第一步**（1-2 周）：创建 `conversations` + `conversation_members` 表结构，无业务变更
2. **第二步**（2-3 周）：扩展 `messages` 表加 `conversation_id`，实现群组发信，message service 感知群组
3. **第三步**（1 周）：通知 fan-out（群消息通知所有成员），block 语义适配

#### 核心挑战

| 挑战 | 说明 | 架构应对 |
|------|------|---------|
| **大群 fan-out 性能** | 1000 人群发消息约产生 999 条通知行写入 | 批量 insert（`INSERT INTO notifications (...) VALUES (...), (...), (...)`）+ 异步批处理队列 |
| **Block 语义冲突** | A block B 但同在群 — A 不应看到 B 的消息 | 在消息查询层（`repository.ListMessages` 的 SQL）中 LEFT JOIN block 表过滤被 block uid 的消息行，而非在 service 层逐条检查 |
| **退出后历史可见** | Slack 允许（退出后可查历史），Telegram 不允许 | 建议默认**允许**（更符合用户预期），通过 `conversations.archived_at` 控制归档后不可见 |
| **消息 notification 的变化** | 当前通知是单点 `ToUID`，群组需要通知 N-1 个成员 | `dispatcher.notificationMessageSent` 在 `conversation_id` 非空时，为每个成员写入通知行 |
| **ACL 交叉检查** | 文件分享、评论引用消息等跨域操作需要验证群组成员关系 | 新增 `conversation.ConversationChecker` 接口（`IsMember(ctx, conversationID, uid)`），被 file/message 服务引用 |

#### 预期的表设计

```
conversations:
  id            UUIDv7 PK
  type          VARCHAR(10)  -- '1:1' | 'group'
  title         VARCHAR(256) -- nullable for 1:1 (auto-derived from participants)
  owner_uid     VARCHAR(64)  -- creator / admin
  created_at    TIMESTAMPTZ
  archived_at   TIMESTAMPTZ  -- nullable

conversation_members:
  conversation_id UUID  FK → conversations.id
  uid             VARCHAR(64)
  role            VARCHAR(16)  -- 'owner' | 'admin' | 'member'
  joined_at       TIMESTAMPTZ
  last_read_at    TIMESTAMPTZ

  PRIMARY KEY (conversation_id, uid)

messages: 新增
  conversation_id  UUID  FK → conversations.id  -- nullable (1:1 = NULL)
```

**设计决策 1：1:1 会话是否也进 `conversations` 表？**

| 选项 | 优点 | 缺点 |
|------|------|------|
| **A：1:1 也进** | 统一查询（`SELECT * FROM conversations WHERE member = uid`）；现有 1:1 迁移到新模型时无缝过渡 | 需对已有的 `messages` 行做回填（迁移成本）；每个 1:1 对需要确定性 ID |
| **B：只在 group 场景进** | 无迁移成本；1:1 聊天保持现有查询 | 查询"所有会话"需要 UNION（`conversations` + `messages` 的隐式会话）；两个数据路径不一致 |

**推荐：选项 A**。迁移成本一次性地，但未来代码的一致性远大于初始成本。1:1 会话的 ID 可以通过确定性 UUIDv5（`uuid.NewSHA1(namespace, sender+receiver sorted)`）推导，避免重复创建。

#### 对现有系统的影响

- **消息 service**：`Send` 签名扩展 `ConversationID *domain.ID`，向后兼容（nil = 1:1）
- **通知 dispatcher**：`messageSentPayload` 增加 `conversation_id` 字段
- **文件分享**：需要支持"分享到群组"（即所有群成员获得文件读取权限）
- **block 模块**：新增双向 block-aware 查询（`repository.ListConversationMessages` 的 SQL 中 filter blocked）
- **认证模式**：群组消息的"参与者"概念从 2 方扩展到 N 方

### 2.4 方向四：文件版本与内容管道（P1 · 建议 3-4 周）

#### 为什么需要

当前文件系统是"写后即忘"的——无版本历史意味用户无法回滚；无缩略图生成意味消息列表中的图片预览必须加载全尺寸原图；无病毒扫描意味用户可能通过文件上传传播恶意内容。

#### 核心挑战

| 挑战 | 说明 | 架构应对 |
|------|------|---------|
| **异步处理可靠性** | 缩略图生成成功后需要更新 `file_thumbnails` 表，失败后重试 | 使用 outbox 事件（`file.version.created`）驱动 pipeline worker |
| **跨版本去重** | 同一文件上传多个版本 → 存储相同 blob 多次 | `content.checksum` + 引用计数（gc 时检查所有版本的 storage_key 引用） |
| **缩略图延迟** | GET thumbnail 时可能还未就绪 | 返回 404 或占位图 + 可选 `?wait=ms` 参数（等待生成完成） |
| **病毒扫描结果** | 命中病毒的文件应被隔离 | 更新 `file.status` 为 `quarantined`，DownloadURL/Get 拒绝返回 quarantined 文件 |
| **版本上限** | 无限版本 → 存储成本失控 | 默认为 50 版本上限，超限后自动删除最旧版本（以 `version_number` 升序 FIFO） |

#### Timeline 分解

```
第 1 周: 表结构 (file_versions, file_thumbnails) + 迁移 + 上传时写入 version 1
第 2 周: content-pipeline worker 骨架 + 缩略图生成（使用 imaging 库）
第 3 周: 病毒扫描集成 (ClamAV socket) + 文件隔离逻辑 + 版本 API
第 4 周: GC 扩展 + 去重 + 版本上限 + 集成测试
```

#### 对现有系统的影响

- **file service**：`Create` 内部创建 version 1 + 发布 `file.version.created` 事件；`Delete` 需要标记所有版本为待删除
- **storage GC**：`sweeper.go` 的 "orphan blob" 检测需考虑版本的 `storage_key`（非仅 `files.storage_key`）
- **新包**：`internal/content/`（pipeline worker）+ 可能的 ClamAV 客户端
- **新依赖**：图片处理库（建议 `github.com/disintegration/imaging`，纯 Go，无 CGO）

#### 第三方依赖评估

| 依赖 | 用途 | 推荐层级 |
|------|------|---------|
| `disintegration/imaging` | 图片缩略图生成 | 推荐（纯 Go，MIT，成熟） |
| `camlistore/go4` / `pkg/image` | 尺寸获取 | 可选（标准库 `image` 包通常足够） |
| ClamAV socket (c-icap) | 病毒扫描 | 推荐（自建或外部服务，Unix socket 通信轻量） |
| NSFW 检测 (如 `antimachine/go-nsfw`) | 内容审核 | 可选，精度低，建议用外部 API |

### 2.5 方向五：活动流（P2 · 建议 4-6 周，依赖于方向三/四）

#### 为什么需要

活动流是社交/协作平台的基石功能。当前用户需要轮询文件、消息、评论三个独立模块来感知平台动态——这造成了断层的用户体验。

**业务价值**：提升用户活跃度和留存，打开"关注"和"发现"的产品交互模式。
**技术价值**：复用 outbox 事件流，构建统一的时序数据管道。

#### 核心挑战 — Fan-out 策略选择

这是活动流系统最关键的架构决策。

| 策略 | 描述 | 适用场景 | 写入成本 | 读取成本 | 一致性 |
|------|------|---------|---------|---------|--------|
| **A: 写时 fan-out（Push）** | 事件发生时，为每个关注者写入一条 activity_entry | 关注者数 << 1000 的场景 | **O(N)** 关注者 | O(1) 直接读取 | 强最终 |
| **B: 读时 merge（Pull）** | 查询时取被关注者的 outbox 事件 + 关注关系实时合并 | 关注者数 >> 1000 的 influencer | O(1) 写入 | **O(M×N)** 查询 | 实时 |
| **C: 混合** | 普通用户 push，influencer (>1000 followers) pull | 用户数分布广泛的平台 | 可变 | 可控 | 两种模式需共存逻辑 |

**推荐：先实施策略 A（写时 fan-out），在关注者数达到 1000+ 的用户上触发切换策略 C。** 理由如下：

1. aero-id 初始阶段的用户规模不会出现大量百万级关注者的场景
2. 策略 A 的代码路径最直接：outbox relay 中新增 `fanout handler`，为被关注者的每个关注者插入 activity_entry
3. 当出现爆款账号导致写入放大时，增加 `isInfluencer(uid)` 检查切换策略 C

#### 关键设计决策：Follow 模型

```
follows:
  id              UUIDv7 PK
  follower_uid    VARCHAR(64)    -- 关注者
  followee_type   VARCHAR(16)    -- 'user' | 'file' | 'conversation'
  followee_id     VARCHAR(128)   -- followee_type 对应的 ID
  created_at      TIMESTAMPTZ

  UNIQUE (follower_uid, followee_type, followee_id)
```

**是否支持"关注文件/会话"而不只关注用户？**

- 支持文件关注可以实现"文件更新通知"（结合方向四的 file.version.created）
- 支持会话关注可以实现"群消息免打扰"（只关注不加入群组时不接收通知）
- **选项 A（仅用户关注）**：简单，初期产品快速上线
- **选项 B（多态关注）**：扩展性强，但复杂度和存储略增
- **建议：选项 B，但 P2 阶段先实现用户关注，预留 followee_type/followee_id 以备后续**

#### 对现有系统的影响

- **新包**：`internal/follow/` + `internal/timeline/`
- **outbox relay**：新增 `fanout handler`（可在 `chain.go` 中组合已有 handler）
- **block 集成**：feed 查询时需过滤 block 关系（`WHERE actor_uid NOT IN (SELECT blocked_uid FROM blocks WHERE blocker_uid = ?)`）
- **通知可复用**：活动流中的"XX 赞了你的评论"可同时作为通知和活动流条目

---

## 3. 接口设计建议

### 3.1 关键模块的接口设计原则

#### 原则 1：每个扩展点一个接口，参数单一职责

现有代码的 `hub.Pusher` 接口是好的范例——只做一件事（Publish）。新模块应沿用此风格：

```
// 推送适配器
type Adapter interface {
    Send(ctx context.Context, toUID string, n *domain.Notification) error
}

// 设备注册
type DeviceRepository interface {
    Register(ctx context.Context, uid, platform, token string) error
    Unregister(ctx context.Context, uid, token string) error
    ListByUID(ctx context.Context, uid string) ([]*DeviceToken, error)
    DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// 内容管道
type ContentPipeline interface {
    Process(ctx context.Context, versionID domain.ID) error
}

// 活动流 Fan-out
type FanOutHandler interface {
    Handle(ctx context.Context, e *outbox.Event) error
}
```

#### 原则 2：避免"上帝接口"

不要创建 `NotificationService` 这样的宽接口然后让推送、SSE、email 各自实现。路由 dispatcher → 推送适配器 → FCM，每层维持窄接口。

#### 原则 3：Options 模式作为编译时安全的扩展机制

已是现有实践的好选择。所有新扩展点应遵循 `WithXxx(...) XxxOption` 模式。

### 3.2 是否需要引入新的抽象层

| 抽象层 | 是否推荐 | 理由 |
|--------|---------|------|
| **Notification Router** | ✅ 是 | 当前 dispatcher 同时处理 SSE + DB 写入，未来再加推送、email，会过长。建议分解为 `NotificationRouter`（接收事件，路由到多个 Sink） |
| **Content Pipeline Orchestrator** | ✅ 是 | 缩略图生成、病毒扫描、OCR 等多个 stage 需要编排（每个 stage 独立失败/重试），提取 `Pipeline` 抽象 |
| **Event Bus 中间件** | ❌ 否 | 当前 outbox 模式已覆盖异步事件。引入独立消息队列（如 RabbitMQ/NATS）在当前规模下是过度设计 |
| **Repository vs DAO 分离** | ❌ 否 | 当前 GORM 直接用于 repository 层，无抽象泄漏，暂不需要 |

#### Notification Router 的接口设计

```
type NotificationRouter struct {
    sinks []Sink
}

type Sink interface {
    // Send delivers a notification to recipient. Returning an error causes
    // the router to log but not fail the overall dispatch (best-effort per sink).
    Send(ctx context.Context, n *domain.Notification) error
}
```

`dispatcher.Handle` 中的顺序逻辑（检查 block → 写 DB → push SSE → push FCM）改为：

```
router := notification.NewRouter()
router.AddSink(dbSink)         // 写 notification 表
router.AddSink(sseSink)        // Hub.Publish
router.AddSink(pushSink)       // FCM/APNs adapter
router.Handle(ctx, e)
```

这允许每个 sink 独立 fail/skip，且新增 sink 不需要修改 Handle 方法。

### 3.3 向后兼容性

所有方向均已设计为向后兼容：

| 变更 | 兼容策略 |
|------|---------|
| 消息发送增加 `conversation_id` | 参数可选，nil = 1:1 行为不变 |
| 文件版本 | 现有 API 不变，version 1 在后台写入，用户无感知 |
| 推送适配器 | 通过 Option 注入；无推送配置 = 无行为变更 |
| 活动流 | 全新 API，不影响现有通知/消息/文件端点 |
| 死信运维 | 新增 admin 端点，不影响现有 relay 逻辑 |
| 通知 Router 重构 | 保持 `Dispatcher` 现有构造函数签名兼容（内部实现改为 Router 模式） |

---

## 4. 技术选型

### 4.1 是否需要引入新的技术栈

| 方向 | 建议引入 | 评估 |
|------|---------|------|
| 推送通知 | 无独立中间件，直接调用 FCM/APNs API | 当前规模零运维成本；当推送 QPS > 1000/s 时考虑引入推送网关（如 gorush） |
| 活动流 Fan-out | 无，Postgres 原生写入即可 | 仅当关注者数 > 100 万/用户时考虑 Redis 时间线分片或专用 Feed 服务（如 Stream） |
| 内容管道 | 无，利用现 outbox + worker goroutine | 若缩略图生成成为 CPU 瓶颈，考虑独立 worker 容器 + 队列 |
| 群组消息 | 无新中间件 | 大群 > 1000 人时考虑消息表分片或单聊/群聊物理隔离 |

**关键决策：当前不需要引入消息队列（Kafka/RabbitMQ/NATS）。**

理由：
1. outbox 模式的异步可靠事件已满足需求
2. 新增队列意味着增加运维复杂度（消费者组、重平衡、持久化策略）
3. 当 outbox relay 成为性能瓶颈时，升级路线：多个 relay worker（用 `sync.Semaphore` 控制并发度）→ 出站 relay 容器水平扩展 → 最后才考虑独立消息队列

### 4.2 第三方依赖评估标准

对于所有新引入的依赖，应按以下维度评分（1-5）：

| 维度 | 权重 | 说明 |
|------|------|------|
| **维护状态** | 30% | 近 6 月有提交，Issue 响应活跃 |
| **Go 版本兼容** | 20% | 支持 Go 1.26 |
| **API 稳定性** | 20% | 不频繁 breaking change，v1 或稳定接口 |
| **无 CGO** | 15% | 可交叉编译，构建无平台限制 |
| **许可证** | 10% | MIT/BSD/Apache 2.0 |
| **大小** | 5% | 不影响二进制体积 |

### 4.3 自建 vs 采购决策矩阵

| 方向 | 自建 | 采购/集成 | 推荐 |
|------|------|---------|------|
| 推送通知 | Go 直接调用 FCM/APNs HTTP API | Firebase Cloud Messaging（免费）或第三方推送服务 | **自建**（因为接口层只有 1 个 adapter，成本极低） |
| 病毒扫描 | ClamAV socket 集成 | 第三方 API（如 VirusTotal）| **自建 ClamAV**（socket 通信轻量，运行在容器中，无网络延迟） |
| 缩略图生成 | `imaging` 库 | 外部图像处理服务（如 Imgix） | **自建**（CPU 操作，库已成熟，无外部依赖） |
| 活动流 | 自建 | Stream API (getstream.io) | **先自建**（方案 A 的写时 fan-out 实现简单，后期再评估） |
| OCR / 文本分析 | Tesseract | AWS Textract / Google Vision | **初期不引入**（方向四 P2），后续评估外部 API |

---

## 5. 实施路线图

### 5.1 阶段划分

```
Phase 1: 运维基础 (Weeks 1-2)
  ├── 死信队列运维 API (P0 · 1周)
  │   ├── FetchDeadLetters / ReplayEvent / PurgeDeadLetters
  │   ├── Dead letter gauge metric + 告警规则
  │   └── Admin HTTP + gRPC 端点
  ├── 设备注册占位 (P0 · 2天)
  │   ├── device_tokens 表 + 迁移
  │   └── POST/DELETE /v1/devices 端点 (返回 501 Not Implemented)
  ├── Outbox 可观测性完善 (P0 · 1天)
  │   ├── 死信 gauge
  │   └── 延迟直方图
  └── 技术债务清理 (P1 · 2天)
      ├── actorKey 去重 (domain 优先)
      └── OutboxConfig.Webhook.Timeout 类型修正

Phase 2: 推送通知 + 文件版本占位 (Weeks 3-5)
  ├── 推送通知通道 (P0 · 2周)
  │   ├── Adapter 接口 + FCM 实现
  │   ├── 设备 token 注册/注销/列表端点
  │   ├── Dispatcher 集成（WithDispatcherPushAdapter）
  │   ├── 令牌桶限速
  │   └── 集成测试（FCM mock HTTP server）
  ├── 文件版本 schema + core (P1 · 1周)
  │   ├── file_versions / file_thumbnails 表 + 迁移
  │   ├── 上传时写入 version 1
  │   ├── 版本列表 API
  │   └── GC 扩展（引用计数）
  └── Notification Router 重构 (P1 · 1周)
      ├── Sink 接口 + Router 实现
      └── Dispatcher 内部改为 Router 模式

Phase 3: 群组消息 (Weeks 6-10)
  ├── 数据层 (P1 · 2周)
  │   ├── conversations + conversation_members 表 (含迁移)
  │   ├── messages 表扩展 conversation_id
  │   └── Repository CRUD
  ├── 服务层 (P1 · 1.5周)
  │   ├── Conversation Service + Create / List / AddMembers
  │   ├── message.Send 支持 conversation_id
  │   └── 成员 ACL 检查
  ├── Block 语义适配 (P1 · 0.5周)
  │   └── 群组消息 block 过滤
  └── 通知 Fan-out (P1 · 1周)
      ├── dispatcher 群组消息通知 N-1 成员
      └── 批量通知 insert 优化

Phase 4: 内容管道 (Weeks 11-13)
  ├── Content Pipeline Worker (P1 · 2周)
  │   ├── Pipeline 接口 + stage 编排
  │   ├── 缩略图生成 (imaging)
  │   └── 病毒扫描 (ClamAV)
  ├── 版本上传 API (P1 · 0.5周)
  │   └── POST /v1/files/:id/versions
  ├── 版本回滚 API (P1 · 0.5周)
  │   └── file 指向特定 version 的 storage_key
  └── 版本上限 + GC 扩展 (P1 · 0.5周)

Phase 5: 活动流 (Weeks 14-17)
  ├── Follow 系统 (P2 · 1周)
  │   ├── follows 表 + migration
  │   ├── POST/DELETE/GET /v1/follows
  │   └── Follow Repository
  ├── 写时 Fan-out (P2 · 2周)
  │   ├── activity_entries 表
  │   ├── outbox relay fanout handler
  │   └── 批量写入优化
  ├── Feed API (P2 · 1周)
  │   ├── GET /v1/feed (cursor pagination)
  │   └── Block 过滤 + ACL 检查
  └── 活动聚合/去重 (P2 · 1周)
      └── 类似 "张三和另外 5 人点赞了" 的聚合逻辑
```

### 5.2 优先级矩阵（面向决策者）

| 方向 | 产品价值 | 架构杠杆 | 实施成本 | 风险 | 决策 |
|------|---------|---------|---------|------|------|
| **推送通知** | ★★★★★ | ★★★☆☆ | 2-3 周 | 低（适配器模式零侵入） | **立即执行 Phase 1** |
| **死信运维** | ★★★☆☆ | ★★★★★ | 1 周 | 非常低（2-3 个方法） | **本周执行** |
| **群组消息** | ★★★★★ | ★★★★☆ | 4-6 周 | 中（block 语义歧义风险） | Phase 3（依赖 Phase 1-2） |
| **文件版本管线** | ★★★★☆ | ★★★☆☆ | 3-4 周 | 中（ClamAV 引入外部进程依赖） | Phase 2 占位，Phase 4 实施 |
| **活动流** | ★★★★★ | ★★★★☆ | 4-6 周 | 中（fan-out 性能模型需验证） | Phase 5（依赖群组/文件完成） |

### 5.3 风险点和缓解策略

| 风险 | 影响 | 概率 | 缓解策略 |
|------|------|------|---------|
| FCM 限速导致推送延迟 | 消息推送延迟从秒级到分钟级 | 中 | 令牌桶 + 背压 + 监控告警；告警后自动降级为只发送 "有新消息" 简化 payload |
| 群组 block 语义引发用户投诉 | 被 block 用户仍看到对方消息 | 中 | 测试覆盖所有 block 组合（A→B block, B→A block, 互不 block）；上线前盲测 |
| 活动流 fan-out 写入放大 | 大 V 发条消息 → 10k+ 行写入 | 低（初期） | 监控 follower 数，超过 1000 自动切换 Pull 模式（方案 C） |
| ClamAV Unix socket 故障 | 内容管道中断，文件无法完成上传 | 中 | `file.status = 'pending_scan'` 允许不上传阻塞；ClamAV 恢复后自动重扫 |
| 设备 token 表膨胀 | 无效 token 占用存储 | 低 | FCM/APNs 返回 `NotRegistered` 后静默清除；定期 `DELETE expired_tokens` |
| 大群消息通知所有成员的 CPU/IO | 1000 人群 → 999 条通知行 insert | 中-高 | 批量 insert（`VALUES (...), (...)...
）代替逐条 insert；观察是否需要异步批量提交 |

### 5.4 关键执行建议

1. **Phase 1 在两周内完成**，包含死信运维 + 设备注册占位 + 技术债务清理。这是最低成本提升运维成熟度和可观测性的工作。

2. **Phase 2（推送通知）紧接着 Phase 1 执行**，不要等群组消息。移动端覆盖是最紧迫的产品缺口，推送通道建立后群组消息的 fan-out 通知（通知 N-1 个成员）可以直接复用。

3. **文件版本 schema 占位在 Phase 2 内插队**，仅 2-3 天的工作量，为 Phase 4 的内容管道铺路。

4. **群组消息的 block 语义需要产品确认后实施**。扫描文档提到了 Slack 和 Telegram 的不同做法，这个产品决策会直接影响 `repository.ListConversationMessages` 的 SQL WHERE 子句。建议在 Phase 3 开始前完成产品评审。

5. **活动流放在最后**不是因为不重要，而是因为它天然依赖于群组消息和文件版本的内容管道就绪后，才有"有意义的"活动数据可供聚合。过早搭建活动流会导致 feed 里只有 1:1 私信和评论回复，内容稀疏、用户无感。

---

## 总结

aero-id 当前的架构是一个健康的三层设计，具备良好的扩展点。五个扩展方向中：

- **死信运维**是**最短运维负债**——成本极低，收益立竿见影
- **推送通知**是**最大产品价值**——填补移动端实时空缺
- **群组消息**是**最复杂产品升级**——需谨慎处理 block 语义和 fan-out 性能
- **文件版本管线**是**技术深度最高**的扩展——引入异步处理编排和外部依赖
- **活动流**是**长期持久价值**——但依赖于前述几个方向的完成度

**推荐第一阶段（2 周）立即实施死信运维 + 设备注册占位 + 技术债务清理，然后推进推送通知。** 每完成一个 phase 应运行一次 `make test && make lint` 并评估文件大小约束（`make check-file-sizes`），确保不突破 500 行/文件的工程纪律。
