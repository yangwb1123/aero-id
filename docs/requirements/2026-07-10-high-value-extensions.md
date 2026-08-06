# aero-id 高价值扩展方向分析

> 日期：2026-07-10
> 范围：全局代码扫描（`internal/`, `migrations/`, `docs/`, `cmd/`）
> 视角：资深架构师 / 产品经理
> 前提：ROADMAP 所有方向（P0–P2）均已交付，22 个迁移全部落地，双传输（HTTP + gRPC）完全对等。

---

## 概览

当前代码库是一个非常成熟的双传输（HTTP + gRPC）微服务，覆盖了：

| 领域 | 现状 |
|---|---|
| 文件 | CRUD、流式上传、分块断点（presigned S3）、内容去重、显式分享、GC 回收、元数据 PATCH |
| 消息 | 1 对 1 发送/收件箱/对话、已读回执、系统消息、reactions、软删除、编辑、block 抑制 |
| 评论 | 双层嵌套、点赞、举报/审核/隐藏、编辑、CJK 全文搜索 |
| 通知 | 事务 outbox 驱动、SSE 实时推送、偏好（含静默时段）、条数折叠、block 感知、rate limit |
| 运维 | RED metrics、OTel tracing、circuit breaker、rate limiter、idempotency、审计日志、管理角色权限 |
| 基础设施 | 事务 outbox relay、存储 GC 回收、冷启动 warmup、优雅关闭、TLS/mTLS、证书固定 |

**以下分析不是愿望清单，而是基于代码现状识别出的真实盲区。** 每个方向均标注了：为什么需要、现有代码的阻碍、建议的切入点和粗略量级。

---

## 方向一：Conversation 作为一级领域聚合（Group Chat 基础设施）

### 现状

消息模型目前是一个扁平的 `messages` 表：`(from_uid, to_uid, content, ...)`。所谓的"对话"是 service 层的一个查询方法：

```go
// service/message.go
Conversation(ctx context.Context, withUID string, page domain.Page) (*MessageList, error)
```

其实现仅是 `WHERE (from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?) ORDER BY created_at DESC`。**不存在 `conversations` 表、不存在 participants 表、不存在 last_message_id、不存在对话元数据。**

已有但暴露出的问题：

- **无对话列表 API**：你无法请求"我所有的对话，每个对话显示最后一条消息预览和未读数"。`Inbox` 是全局按时间倒排的消息列表，不是对话维度的。
- **无已读水位线**：`MarkConversationRead` 是批量标记某个对方发来的所有消息为已读，而不是记录"用户 A 在对话 X 中读到了哪条消息为止"。这意味着重新加入对话后无法精确恢复未读位置。
- **无多人参与**：`to_uid` 是单一字符串。没有 Group Chat（群组）的概念，没有 `conversation_participants` 表。
- **无对话级操作**：不能 mute、archive、pin、leave、rename 一个对话。这些是即时通讯类产品的标配能力。
- **outbox 事件丢失上下文**：当前 `message.sent` 事件的 payload 只有 `message_id`、`from_uid`、`to_uid`。如果引入群聊，接收方是一个集合而非单用户，事件结构需要变更。

### 为什么需要

1. **产品差异化**：如果 aero-id 是 SSO 体系下的数据层，那么对话（Conversation）是比消息更自然的消费者交互单元。Web 端/移动端需要一个对话列表来导航。
2. **已读回执精度**：现有的"标记与某人的所有消息为已读"是粗粒度的。逐消息 `read_at` 已经存在，但如果用户在对话 A 读到消息 5，在对话 B 读到消息 3，当前无法表达"每个对话最后读取位置"。
3. **群聊是自然需求**：一旦一个 SSO 生态中有团队协作场景，1 对 1 消息 + 文件分享是不够的。群聊、频道、话题讨论是自然的扩展方向。
4. **权限模型自然延伸**：现有 Block 是 `(blocker, blocked)` 二元方向抑制。群聊需要 `(conversation, participant, role)` 模型 + `invite`/`kick`/`leave` 操作。这是现有 block 体系无法覆盖的。

### 建议切入点（最小可行）

1. 新增 `conversations` 表：`id, type(1on1|group), title, created_by, created_at, updated_at`
2. 新增 `conversation_participants` 表：`conversation_id, uid, role(member|admin), joined_at, last_read_at`
3. 修改 `messages` 表：增加 `conversation_id`（可为 NULL 兼容旧数据，新消息强制关联）
4. 迁移旧数据：为每对 `(from_uid, to_uid)` 自动创建一个 1on1 对话并回填
5. 新增 API：`GET /v1/conversations`（对话列表 + last_message + unread_count）、`POST /v1/conversations`（创建群聊）、`POST /v1/conversations/:id/members`（邀请）、`DELETE /v1/conversations/:id/members/:uid`（踢出/离开）
6. 调整 outbox：`message.sent` 事件 payload 增加 `conversation_id`，通知分发逻辑支持多个 recipient

### 量级

- **中-高（2–4 周）**。模型不复杂但迁移旧数据和保持向后兼容需要仔细设计。

---

## 方向二：多副本部署 —— 消除单实例瓶颈

### 现状

代码库中有多处**假设只有一个实例在运行**：

| 组件 | 问题 | 影响 |
|---|---|---|
| **Rate limiter**（`internal/pkg/ratelimit`） | 内存 token bucket，每实例独立。两个实例各允许 100 req/s → 对外表现为 200 req/s | 限流形同虚设，多副本下容量翻倍 |
| **Outbox relay**（`internal/outbox/relay.go`） | 每个实例独立轮询 `FetchUnpublished`。两个实例都会拿到同一批事件、同时处理、同时 `MarkPublished` | 重复处理（即使幂等也有额外开销）；如果 Webhook 不可幂等则产生重复投递 |
| **SSE Hub**（`internal/hub/hub.go`） | `sync.Map` 内存注册表。用户连到实例 A 时收不到实例 B 产生的事件 | SSE 实时推送多副本下完全失效 |
| **SSO token/user cache**（`internal/sso/token_cache.go`、`user_cache.go`） | 内存 TTL cache，每实例独立 | 缓存命中率随副本数增加而下降，每个实例都 cold start 穿透到 SSO |
| **Idempotency store**（`internal/idempotency/store.go`） | 基于 Postgres，✅ 多副本安全 | — |

**没有 leader election，没有 shared state (Redis/Memcached)，没有 instance affinity。**

### 为什么需要

1. **高可用（HA）部署**：任何生产系统至少需要 2 副本滚动更新。当前架构下滚动更新期间 SSE 断连、限流失效、outbox 可能重复处理。
2. **水平扩展**：读流量增长时只能垂直扩容。无法通过增加副本分摊读负载（因为 SSE 和 outbox 有状态）。
3. **这不是"未来问题"**：如果现在只有一个实例跑，outbox 和 SSE 工作正常。但只要启动第二个实例，上述问题立即暴露——没有渐变期。

### 建议方案

**不需要一步到位做到完全分布式。** 推荐渐进式：

1. **短期（无代码变更）**：部署时通过环境变量 `INSTANCE_ID` 或 `ENABLE_OUTBOX=false` 让**只有一个实例跑 outbox relay**，其他实例只服务 API + SSE。SSE 的断连由客户端重连 + `last_event_id` replay 缓解。
2. **中期（1–2 周）**：
   - Rate limiter 增加 Redis 后端（slot 可选：已有 `ratelimit.Limiter` 是接口良好，加一个 `RedisLimiter` 实现即可，接口无需改动）。
   - Outbox relay 增加 `SELECT ... FOR UPDATE SKIP LOCKED` 或 `pg_advisory_lock` 防止同一事件被多个 relay 处理。
   - Hub/Pusher 增加 Redis Pub/Sub 桥接：实例 A 的事件通过 Redis 广播给实例 B 的 SSE 连接。
3. **长期**：
   - SSO cache 替换为 shared Redis（或维持每实例 cache + 短 TTL 作为 L1，Redis 为 L2）。

### 量级

- **低-中（分布式限流 + outbox 排他 = 1 周；完整的 Redis pub/sub 桥接 = 2 周）**。每个子问题范围明确，可以分阶段交付。

---

## 方向三：内容版本管理（文件 + 消息的历史与回滚）

### 现状

目前的内容模型是**原地更新**：

- **文件**：`UpdateMetadata` 可以改 name/visibility/meta，但文件内容是不可变的（新上传 = 新 row）。没有版本号，没有 delta 存储，没有版本列表。
- **消息**：`Edit` 直接重写 `content`，`edited_at` 记录了编辑时间但**不保留编辑历史**。无法查看某个消息的上一个版本。
- **评论**：同消息——`Edit` 覆盖内容，`edited_at` 记录时间但无历史。

已有但不足以支撑版本管理：
- `files.checksum` 支持内容寻址去重，**但两个不同版本的上传如果内容相同则共用 blob**（好现象！意味着版本管理不需要重复存储）。
- 软删除（`deleted_at`）可以恢复已删除的实体，但不能恢复编辑前的内容。
- `audit_events` 表记录了**操作审计**（谁在何时做了什么），但不记录**内容审计**（改前是什么）。

### 为什么需要

1. **合规与信任**：在消息/评论场景中，编辑不保留历史是安全和合规隐患——用户可能发送不当内容后编辑掉，审核方无法追溯原文。GDPR/CCPA 要求记录数据处理历史。
2. **用户期望**：文件版本管理（Google Drive 风格 "查看历史版本"、回滚到上一版）、消息编辑历史查看——这些是成熟产品的标配。
3. **低增量成本**：由于 content-dedup 已实现、outbox 事件流已存在、审计表已落地，**版本管理的底层基础设施 80% 已就绪**。缺的是版本表和读取 API。

### 建议方案

1. 新增 `message_versions`、`comment_versions` 表（或用单一 `content_versions` 多态表），每次编辑时插入上一版本（outbox 事件驱动或同步写）。
2. 新增 `file_versions` 表：每次上传相同 `owner_uid` + `name` 时自动关联为同一文件的不同版本（类似 Google Drive 的逻辑，而非每次新建独立 row）。
3. 新增 API：
   - `GET /v1/messages/:id/versions`、`GET /v1/comments/:id/versions`、`GET /v1/files/:id/versions`
   - 回滚操作（`POST /v1/messages/:id/rollback?version=N`）
4. 利用 files.checksum 确保相同内容的版本不重复占用存储。

### 量级

- **中（1–2 周）**。表结构简单，主要是 service 层逻辑和 API 设计。outbox 侧可以在 `message.edited`、`comment.edited` 事件 payload 中附带 `previous_content`。

---

## 方向四：Webhook 基础设施 —— 从单端点出站到事件生态

### 现状

只有一个 `outbox.WebhookHandler`，指向单一 URL，配置在全局 config 中：

```yaml
outbox:
  webhook:
    url: "https://hooks.example.com/events"
    signing_key: "..."
    event_types: []
    headers: {}
```

这意味着：
- **不能注册多个 webhook**：不同的消费方（数据分析管道、消息推送服务、审核系统）都想接收事件，但只能指向一个端点。
- **不能按订阅方过滤**：所有订阅方收到全部事件（除非在端点侧自己做路由）。
- **不能管理 webhook**：没有 `POST /v1/webhooks`、`DELETE /v1/webhooks/:id`、`GET /v1/webhooks` 的管理 API。
- **没有重试队列**：`WebhookHandler.Handle` 返回 error → outbox relay 重试整个事件（包括本地通知分发）。这意味着第三方 webhook 失败会阻塞本地通知。
- **没有订阅方鉴权**：除了 HMAC 签名外，没有每个订阅方独立的 secret。一个泄漏的 `signing_key` 影响所有事件。
- **没有事件 Schema 契约**：事件 payload 是 `json.RawMessage`，没有版本号或 schema 注册表。消费方只能自己解析。

### 为什么需要

1. **平台化**：aero-id 是 SSO 生态中的数据核心，天然适合作为事件源。外部服务（推送服务、分析管道、审核管道）需要通过 webhook 消费内部事件，而非轮询 API。
2. **解耦 outbox retry**：当前 `WebhookHandler` 和 `NotificationDispatcher` 通过 `Chain` 串联，一个失败两者都重试。将 webhook 抽离为独立的重试队列可以隔离故障域。
3. **产品价值**：Webhook 管理 API + 事件目录 = 第三方开发者可以围绕 aero-id 构建集成。这是从"单服务"到"平台"的关键一步。

### 建议方案

1. 新增 `webhook_subscribers` 表：`id, url, secret, event_types[]`, `retry_config`, `disabled_at`, `created_at`
2. 将 `WebhookHandler` 重构为从 DB 读取订阅列表（而非从 config），每个订阅独立 sign。
3. 新增独立的重试队列（利用现有的 outbox 事件，但使用 `webhook_deliveries` 表跟踪每次投递的状态）。
4. 新增管理 API：`CRUD /v1/admin/webhooks`
5. 事件 Schema 注册：在 `EventType` 基础上增加 `event_version` 字段，发布 API 文档标明每个事件的 payload shape。

### 量级

- **中-高（2–3 周）**。DB schema 简单，但重试队列、管理 API、事件版本化需要仔细设计。可以与方向二的分布式 outbox 合并实施。

---

## 方向五：批量操作与数据可移植性（GDPR / 批量导出 / 级联删除）

### 现状

所有写操作都是**单一实体粒度**：

| 操作 | 当前方式 | 问题 |
|---|---|---|
| 删除文件 | `DELETE /v1/files/:id` | 删除 10000 个文件需要 10000 次 HTTP 调用 |
| 导出用户数据 | 不存在 | 无法满足数据可移植性请求 |
| 清理已读通知 | `MarkAllRead` 已实现 | 但没有"删除 30 天前的已读通知" |
| 批量分享 | `POST /v1/files/:id/shares` 接受 `{uids: [...]}` | 只能针对一个文件；无法一次分享多个文件 |
| 账户注销 | 不存在 | SSO 删除用户后，aero-id 的数据变成孤儿 |

账户注销（Account Deletion）尤其值得关注：aero-id 存储了用户产出的文件、消息、评论、通知偏好、block 列表等数据。当 SSO 通知"用户 X 已注销"时，aero-id 当前没有任何 handler 来处理——数据永远残留。

### 为什么需要

1. **合规要求**：GDPR 第 20 条（数据可移植性）、第 17 条（被遗忘权 / Right to erasure）要求平台能导出用户的所有数据，并在用户要求时删除。
2. **运营成本**：无批量删除意味着用户体内存泄漏。如果 SSO 删除了用户，aero-id 的行不会被清理，storage 中的 blob 也不会被清理。GC sweeper 只能清理软删除的行，但 orphan blobs（从未关联 row）才被清理——被删除用户的文件仍然有 owner_uid 引用所以不会被 GC 视为 orphan。
3. **产品体验**：用户期望能一键清空消息/通知/文件，而不是手动逐条删除。
4. **级联删除的必要性**：当一条消息被软删除后，它的 reactions (`message_reactions`)、关联的文件引用 (`file_id`)、通知 (`notifications.subject_id`) 等关联数据不会自动清理。这导致消息已删但 reactions 行仍然存在。

### 建议方案

1. **数据导出 Job 框架**：
   - 新增 `async_jobs` 表：`id, type(export|bulk_delete|account_wipe), owner_uid, status, progress, result_url, created_at`
   - 新增 `DELETE /v1/me/data`（异步，返回 job_id）→ 后台扫描 messages、comments、files、notifications，生成 JSON 或 CSV 导出文件上传到 storage
   - 新增 `DELETE /v1/me/account`（级联删除所有用户关联数据 + 通知 SSO）

2. **批量删除 API**：
   - `POST /v1/messages/batch-delete`（body: `{ids: [...]}`）
   - 或 `DELETE /v1/messages?before=2026-01-01`（按时间范围批量删除）

3. **级联清理**：
   - 在消息软删除的 outbox handler 中，级联删除 `message_reactions` 行
   - 在评论软删除时，级联处理 `comment_likes`、`comment_reports`
   - 在账户注销时：先删除所有文件 blob → 软删除所有消息/评论/文件行 → 删除通知/偏好/block → commit

4. **SSO 用户删除事件的监听**：
   - SSO 增加 `user.deleted` 事件（当前无此事件，需要 SSO 侧配合）
   - aero-id 的 outbox webhook 或 gRPC 流监听该事件 → 触发级联清理

### 量级

- **中（1–3 周）**。数据导出 + 级联删除是独立的功能。可以先做账户级联删除（合规优先级高），再考虑批量操作 API。需要 SSO 侧配合的部分是外部依赖，可先提供手动触发的 admin API。

---

## 总结 —— 优先级建议

| 方向 | 产品价值 | 架构价值 | 合规/运维 | 估算 | 建议 |
|---|---|---|---|---|---|
| **一：Conversation 聚合体** | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ | — | 2–4 周 | 追求产品增长时首位 |
| **二：多副本部署** | — | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 1–2 周 | 上生产 HA 前必须做 |
| **三：内容版本管理** | ⭐⭐⭐ | ⭐⭐ | ⭐⭐⭐⭐ | 1–2 周 | 合规驱动时可并行 |
| **四：Webhook 基础设施** | ⭐⭐⭐⭐ | ⭐⭐⭐ | — | 2–3 周 | 平台化路线图的关键 |
| **五：批量操作 & 可移植性** | ⭐⭐ | ⭐⭐ | ⭐⭐⭐⭐⭐ | 1–3 周 | GDPR 合规要求时最高优先级 |

**建议启动顺序**：

1. **方向二（多副本）**——无此能力无法 HA 部署，影响所有其他方向的线上稳定性。子任务可独立交付，短期 workaround（outbox leader-only）可立即实施。
2. **方向五的账户级联删除子集**——合规风险最高。可先做 `POST /v1/me/account` 级联清理 + admin 触发的用户数据擦除，再做批量导出。
3. **方向一（Conversation）**——产品增长的最大杠杆。如果 aero-id 是面向终端用户的产品而非纯 API 服务，Conversation 是最高价值的功能增量。
4. **方向四（Webhook）**——平台化基础设施，为生态扩展铺路。可以与方向二的分布式 outbox 合并设计。
5. **方向三（版本管理）**——合规增强 + 用户信任，但增量价值低于前四项，可放在后续迭代。

---

*本文档仅做分析，未修改任何代码。*
