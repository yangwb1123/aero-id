# aero-id 全局架构扫描：高价值扩展方向分析

> **日期:** 2026-07-11
> **范围:** 全量代码扫描 `api/proto/` · `cmd/` · `internal/` · `migrations/` · `configs/`
> **方法:** 逐文件分析 domain 模型、service 接口、repository 实现、transport handler、基础组件以及配置骨架
> **前置阅读:** `AGENTS.md`（项目契约）、`README.md`（API 速查）、`docs/specs/aero-id-design.md`（设计文档）

---

## 现状摘要

aero-id 已经从一个三层 CRUD MVP 成长为一个具备生产级能力的服务平台。通过已完成的前后数轮迭代，以下基础设施和产品能力都已就位：

| 领域 | 已完成能力 |
|------|-----------|
| **基础设施** | 双协议 HTTP + gRPC、GORM + Postgres 持久化、本地/S3 存储后端、OpenTelemetry 链路追踪、RED 指标、启动预热、优雅关停 |
| **可靠性** | 幂等键、事务性 Outbox、出站事件 Relay（退避+死信）、SSO 断路器+serve-stale+重试、速率限制 |
| **文件** | CRUD、流式上传、MIME 校验、大小限制、内容去重（checksum）、预签名直传 S3、显式分享、GC 回收 |
| **消息** | 1:1 私信、已读回执、Reaction、编辑/删除、系统消息、Block 后自动拦截 |
| **评论** | 两层嵌套、Like/Unlike、编辑、软删除、举报/审核队列、隐藏/显示 |
| **通知** | Outbox 驱动的异步通知、SSE 实时推送、通知偏好（含静默时段）、已读管理 |
| **运营安全** | 操作审计日志、管理员角色/权限体系、证书锁定 |

然而，按产品演进和运维成熟度的标准衡量，仍然存在数个高价值、低耦合的扩展方向。以下分析基于对当前代码的逐层扫描，**聚焦于未在现有 `docs/requirements/` 文件中充分覆盖的方向**。

---

## 方向一：推送通知通道 — FCM / APNs 集成

### 现状

`internal/notification/dispatcher.go` 在 outbox 事件处理中创建通知行，并通过 `hub.Pusher` 接口向 SSE 连接推送实时事件。SSE 通道仅适用于**保持打开的浏览器连接**。

### 为什么需要

1. **移动端覆盖为零。** 无 FCM（Firebase Cloud Messaging）或 APNs（Apple Push Notification）适配器意味着移动用户永远收不到实时推送 — 只能轮询获得未读计数。
2. **架构位置完美。** `hub.Pusher` 是一个只有 `Publish(uid, eventType, data)` 方法的接口（`internal/hub/hub.go:151`）。当前只有 `*Hub` 实现它。添加一个 PushNotificationPusher 实现，`Handle` 中在 SSE 推送之外同步（或异步）调用 FCM/APNs，零侵入。
3. **设备注册缺失。** 当前无设备 token 管理端点（`POST /v1/devices` 注册 token 和平台类型），也无 token 过期/刷新机制。

### 建议方案

```
outbox.Handler (dispatcher)
  ├─→ notification.Repository.Create       (DB 行)
  ├─→ hub.Pusher.Publish → SSE             (web 实时)
  └─→ push.Adapter.Send                    (新: FCM / APNs)
```

- 新增 `internal/push/` package，含 `Adapter` 接口 + FCM/APNs 实现
- 新增 `device_tokens` 表（`uid`, `platform`, `token`, `created_at`, `updated_at`）
- HTTP/gRPC 端点：`POST /v1/devices`（注册）、`DELETE /v1/devices/:token`（注销）
- `dispatcher` 新增 `WithDispatcherPush(adapter)` 选项，创建通知后异步推送
- 推送 payload 使用通知已序列化的 JSON data，客户端解析后跳转

### 边界情况

- **Token 失效：** FCM/APNs 返回 `NotRegistered` 或 `Unregistered` → 静默删除该 token 并记日志
- **静默时段 + 免打扰：** `preference.IsAllowed` 已在 dispatcher 链路中 → service 层包装的 `WithDispatcherPreferenceChecker` 已在，推送适配器无需重复判断
- **批量推送限速：** FCM 有 600 QPS / project 的限流 → 需在 `Adapter.Send` 中实现令牌桶或背压

---

## 方向二：死信队列运维与 Outbox 可观测性

### 现状

`internal/outbox/relay.go` 实现了完整的重试退避逻辑：事件失败后递增 `retry_count`，应用指数退避（`5s × 2^(n-1)`，上限 1h），超出 `maxRetries`（默认 20）后标记 `dead_letter = true` 并停止拾取。

但当前完全缺失：
- 查询死信事件的 API
- 手动重放（replay）死信事件的操作路径
- 按事件类型查看延迟/失败率的仪表化
- 死信清理策略（自动过期或手动）

### 为什么需要

1. **运维黑洞。** 当生产中的 webhook 端点故障或通知处理出错时，事件沉默地进入死信，运维人员无从知晓。
2. **已有基础设施完备。** `outbox.Repository` 有 `FetchUnpublished`（跳过死信）、`MarkFailed`、`MarkPublished`。添加 `FetchDeadLetters` + `ReplayEvent` 只需要 2-3 个新方法。
3. **警报基础。** 可以基于死信计数触发告警（`len(dead_letters) > threshold → PagerDuty`）。

### 建议方案

- `outbox.Repository` 新增：
  - `FetchDeadLetters(ctx, page)` → `[]*Event`（创建时间倒序）
  - `ReplayEvent(ctx, id)` → 重置 `dead_letter = false`, `retry_count = 0`, `last_retry_at = null`
  - `PurgeDeadLetters` 已存在（`repository.go:127`）
- 新增 `internal/handler/grpc/ops_deadletter.go` + `internal/handler/http/admin_deadletter.go`（挂载在 `/v1/admin/dead-letters` 下，需要 `outbox.admin` 权限）
- `metrics.Registry` 新增 `ObserveOutboxDeadLetter(eventType)` 计数器，暴露在 `/metrics`
- 可选项：relay 启动时记录当前死信数量为 gauge

### 边界情况

- **Replay 后再次失败：** 允许反复重放，但每次重放后走标准退避流程
- **并发 replay 同一事件：** relay 的 `MarkPublished` 按 ID 列表一次更新，重放的事件会被 relay 的下一次 tick 拾取 — 幂等设计
- **死信堆积：** 默认无自动清理，admin 应通过 `PurgeDeadLetters` 定期清理或按 retention 策略配置

---

## 方向三：多对多会话 / 群组消息

### 现状

当前消息模型是严格的 **1:1 私信**（`messages` 表有 `from_uid` 和 `to_uid`，`internal/message/service.go` 的 `Send` 只接受单 `ToUID`）。会话通过 `Conversation(ctx, withUID, page)` 方法隐式重建 — 本质上是查询两 uid 之间的所有消息。无会话实体、无成员资格、无群组。

### 为什么需要

1. **产品天花板。** 消息服务不支持群聊的场景范围大幅受限（团队协作、客服会话、频道广播）。
2. **合理演进路径。** 现有的 `messages` 表可以兼容新模型 — 增加可选的 `conversation_id` 列，非群组消息该列为空（或等于 1:1 双方的确定性哈希）。
3. **组合重用。** 通知、block、文件分享等已建的跨领域能力可以直接作用于群组场景（例如：block 用户后自动从所有群组中移除该用户的消息）。

### 建议方案

```
新增表:
  conversations         (id, type[1:1|group], title, owner_uid, created_at, archived_at)
  conversation_members  (conversation_id, uid, role, joined_at, last_read_at)

扩展 messages 表:
  +conversation_id  (UUID, nullable FK)

新增端点:
  POST /v1/conversations           (创建群组)
  POST /v1/conversations/:id/members (添加成员)
  POST /v1/conversations/:id/messages (在群里发消息)
  GET  /v1/conversations           (列出我的会话)
  GET  /v1/conversations/:id/messages (会话消息历史)
```

- 新增 `internal/conversation/` 包（domain + service + repository + handlers）
- `message.Send` 新增 `ConversationID *domain.ID` 参数（为空 → 1:1；非空 → 群组消息）
- 权限模型：群组成员可读/写；非成员无法查看
- Block 检查：`message.send` 时检查发送者是否被任何群成员 block（或者发到群里，只对有 block 关系的成员不可见 — 类似 Slack 的 "user left channel"）
- 通知扩展：群组消息应通知所有其他成员，而非仅一个 `ToUID`

### 边界情况

- **大群（>1000 人）：** 入群/退群、消息 fan-out 的性能。可考虑通知行写为批量 insert。
- **成员退出后历史可见：** 设计决策 — Slack 允许退出后仍可查看历史；Telegram 不允许。建议默认允许，可通过 `conversations.archived_at` 控制。
- **Block 与群共存的语义：** 如果 A block B，但两人在同一个群 — A 不应看到 B 的消息。实现方式：在消息查询层对 blocked uid 做 filter（`internal/message/repository.go` 已做类似过滤）。

---

## 方向四：文件版本管理与内容管道

### 现状

文件 CRUD 是写后即忘的：`file.Create` 写入一个 `File` 行 + 存储 blob；`file.Delete` 软删除行（后续 GC 清理 blob）。`file.UpdateMetadata` 只修改 name/visibility/meta。**无版本历史、无缩略图生成、无病毒扫描、无 OCR / 内容分析。**

### 为什么需要

1. **版本丢失。** 用户上传新版本覆盖（同名文件重新 `Create`）后，旧版本仅靠软删除保留，且被 GC 定期清理。无版本历史意味着无法回滚。
2. **内容管道缺失。** 尽管 `StorageType` 和 `MimeType` 记录了媒体信息，但无后台处理。图片无缩略图（在消息列表中预览原图很重），文档无预览，恶意文件无扫描。
3. **Outbox 已经为内容管道提供了天然的触发机制：** `file.created` 事件可以在 outbox 中发布，一个 worker 订阅该事件并执行缩略图生成 / 病毒扫描 / 内容分析，结果存储在 `file_versions` 或 `file_metadata` 表中。

### 建议方案

```
新增表:
  file_versions  (id, file_id, version_number, storage_key, size, checksum, mime_type,
                  created_at, created_by)
  file_thumbnails (file_id, version_id, type[square|wide], width, height, storage_key, created_at)

扩展 file.created outbox 事件:
  触发 content-pipeline worker（新 internal/content/ 包）
    ├─ 缩略图生成（images → thumbnail, 使用 imaging 库或外部服务）
    ├─ 病毒扫描（ClamAV socket / external API）
    └─ 内容分析（NSFW detection for images, text moderation for docs）

端点:
  GET  /v1/files/:id/versions              (列出版本)
  POST /v1/files/:id/versions              (上传新版本)
  GET  /v1/files/:id/versions/:version/dl  (下载特定版本)
  GET  /v1/files/:id/thumbnail?type=square (获取缩略图)
```

- `file.service.go` 的 `Create` 逻辑：新上传 → 创建 `version 1` 行 + 发布 `file.version.created` 事件
- 内容处理 worker 独立于请求路径运行，失败不影响主流程
- GC 扩展：purge 时需检查所有版本的 `storage_key` 引用，无引用才删除 blob

### 边界情况

- **存储成本：** 每个版本都存储完整 blob → 可用 `content.checksum` 做跨版本去重
- **版本上限：** 建议设默认版本上限（如 50），超出后自动删除最旧版本（LRU 或 FIFO）
- **缩略图延迟：** 生成是异步的；GET thumbnail 在未就绪时返回 404 或占位图
- **病毒命中：** 扫描结果应更新 `file.status`（`flagged` / `quarantined`），被隔离的文件拒绝下载

---

## 方向五：跨资源统一活动流（Activity Feed）

### 现状

当前通知系统（`notification`）提供了每个用户触发的**与用户直接相关**的事件摘要（"有人回复了你的评论"、"你收到了新消息"）。但不存在一种**全局的、按时间序排列的跨资源活动流**——例如"我的好友群组中发生了什么"或"我关注的文件有什么更新"。

出站 Outbox 中的事件数据已经记录了所有活动，但未被重用于**订阅-推送的活动流**。

### 为什么需要

1. **产品刚需。** 所有社交/协作平台都有某形式的活动流（Facebook News Feed、GitHub Dashboard、Slack Activity）。缺少此功能意味着用户需分别轮询文件、消息、评论来感知平台动态。
2. **数据已存在。** Outbox 表记录了 `aggregate_type`（`file`/`message`/`comment`）、`event_type`（`file.created`/`message.sent`/`comment.replied`）和 `payload`（JSON）。一个轻量的物化视图或 fan-out 写入即可生成每个用户的活动流。
3. **与现有功能互补而非重叠。** `notification` 表是**面向被交互者**的（评论被回复者收到通知），而 activity feed 是**面向观察者**的（关注者看到被关注者的活动）。两者服务不同用例。

### 建议方案

```
新增表:
  activity_entries (
    id          UUIDv7 PK,
    owner_uid   string,        // 流的所有者（这条活动属于谁）
    actor_uid   string,        // 谁触发了活动
    event_type  string,        // 同 outbox 枚举
    summary     string,        // 渲染文本，如 "张三 评论了 文件.pdf"
    metadata    jsonb,         // 引用 ID 等结构化数据
    created_at  timestamptz
  )

写入路径（二选一）:
  方案 A（大型）: outbox relay 中新增 fan-out handler，为每位"关注者"写入 activity_entry
  方案 B（轻量）: 查询时通过 outbox 表 + 关注关系实时物化，配合缓存

读取端点:
  GET /v1/feed?cursor=&limit=     (当前用户的活动流)
  GET /v1/feed/:uid               (查看指定用户的活动流，需权限)

关注系统:
  POST /v1/follows         (关注用户 / 文件 / 评论)
  DELETE /v1/follows/:id   (取消关注)
```

- 关注（follow）模型新增 `internal/follow/` package
- 活动流的聚合需要 `timeline` 服务，从 `outbox` 获取事件并筛选出用户关注的对象的事件
- 高级功能：去重（同一用户连续点赞不重复显示）、聚合（"张三和另外 5 人赞了你的评论"）

### 边界情况

- **大量关注者（influencer 场景）：** 方案 A 的 fan-out write 对拥有 10k+ 关注者的用户会产生 10k+ 行写入 → 需使用异步批处理或先写一个 `actor_activity` 时间线，再让关注者查询时做 merge
- **已删除的内容：** 活动流中的引用内容可能已被删除或超出可见性 → 读取时需检查 ACL（`CanReadFile`/`CanReadMessage`），不可见时显示"内容已删除"
- **Block 互斥：** 如果 A block B，A 的活动中不应出现 B 的活动；B 的活动流中也不应出现 A → 在 feed 查询时做 block 过滤（已有 `block.Repository.IsBlocked`）

---

## 优先级建议

| 方向 | 产品价值 | 架构杠杆 | 实施成本 | 推荐时序 |
|------|---------|---------|---------|---------|
| **方向一：推送通知** | ★★★★★ | ★★★☆☆ | ★★☆☆☆ | **P0** — 移动端不可缺失 |
| **方向二：死信运维** | ★★★☆☆ | ★★★★★ | ★☆☆☆☆ | **P0** — 运维必备，成本极低 |
| **方向三：群组消息** | ★★★★★ | ★★★★☆ | ★★★★☆ | **P1** — 重大产品扩展 |
| **方向四：文件版本管线** | ★★★★☆ | ★★★☆☆ | ★★★☆☆ | **P1** — 文件服务深化 |
| **方向五：活动流** | ★★★★★ | ★★★★☆ | ★★★★☆ | **P2** — 依赖于方向三/四的完成度 |

### 快速获胜项（可在 1-2 天内完成的低成本子项）

1. **死信查询 API** — 仅需添加 DB 查询 + admin 端点，零新依赖
2. **设备注册端点** — `device_tokens` 表 + CRUD 端点，占位但不接推送引擎
3. **Outbox 延迟仪表化** — `metrics.ObserveOutboxDeadLetter` + 警报规则，开箱即用
4. **文件版本 schema** — 仅加表和迁移，留 version 1 的写入代码，后续接 content pipeline

---

## 附：扫描中发现的次级技术债务

| 分类 | 问题 | 严重度 |
|------|------|--------|
| **可观测性** | `outbox.Relay` 无 gauge 记录当前死信数量，仅有计数器 | 低 |
| **测试** | `internal/handler/grpc/ops.go` 的测试仅覆盖 health check，未覆盖 reflection | 低 |
| **测试** | `internal/outbox/webhook.go` 集成测试缺失（只有 unit mock 覆盖） | 中 |
| **配置** | `OutboxConfig.Webhook.Timeout` 是 `string` 而非 `time.Duration`（需手动 parse） | 低 |
| **可移除** | `internal/service/service.go` 中的 `actorKey`/`WithActor`/`ActorFrom` 与 `internal/domain/actor.go` 完全重复 | 中 |
| **安全性** | 设备注册端点当前无速率限制配置（全局限流已兜底但有局限性） | 低 |
| **扩展性** | `internal/hub/hub.go` 中每 uid 订阅 channel 缓冲区固定为 16，无配置 | 低 |
