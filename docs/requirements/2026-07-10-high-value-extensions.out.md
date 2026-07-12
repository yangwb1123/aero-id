这份文档已经存在于仓库中（与磁盘上的内容完全匹配）。目前还没有对应的评审回复文件（`.out.md`）。看起来您是希望我对其进行评审——类似于该目录中其他 100+ 对文档的模式。

让我对其进行一份全面的架构评审，对照实际的代码库进行验证。

---

## 评审：aero-id 高价值扩展方向分析

我已仔细阅读了这份文档，并对照 `internal/` 下的实际源代码对其主张进行了交叉验证。以下是逐项评审。

---

### ✅ 总体评价

一份扎实的分析——五个方向均是真实的盲区，引用准确，建议务实。它不是一份"愿望清单"；每一条都有具体的代码引用和工程约束考量。文档存在一些差距，我会在下文指出。

---

### 方向一：Conversation 作为一级领域聚合

**验证**： ✅ 全部准确。

- `service/message.go` 中 `Conversation()` 确实只是 `WHERE (from_uid=? AND to_uid=?) OR ...` 的查询——无 `conversations` 表。
- 无 `conversation_participants` 表、无 `last_message_id`、无对话级元数据。
- `MarkConversationRead` 在域模型中定义为批量标记来自特定用户的所有消息，而非基于水位线的读取。

**补充发现**：

- `internal/domain/message.go` 中 `Message` 结构体没有 `ConversationID` 字段——增加此字段需要修改域模型、GORM 模型、迁移文件以及所有 service 层方法。
- 现有的 `Inbox` 实现（`service/message.go` 中）是全局按创建时间倒排，没有按对话分组——这与文档描述一致。
- Outbox `message.sent` payload（在 `internal/domain/event.go` 中）仅包含 `message_id`、`from_uid`、`to_uid`——引入群聊需要改为支持 `recipient_uids []string`。

**修正/补充说明**：

- 1对1对话的**反向兼容数据迁移**比文档所述更为复杂。对于有 `N` 个用户的 `M` 条消息，如果使用朴素方法（逐对 uid 扫描），会有 `O(N²)` 的潜在对话创建。实际上需要一条 SQL 实现：`INSERT INTO conversations ... SELECT DISTINCT LEAST(from_uid, to_uid), GREATEST(from_uid, to_uid) FROM messages`，然后回填。
- 文档中建议的 `messages.conversation_id` 可为 NULL 的兼容策略是合理的，但它意味着所有现有查询在迁移完成前都需要一个 `WHERE conversation_id IS NULL OR conversation_id = ?` 回退分支——这会增加短期运维复杂度。

**估计**：2–4 周——合理。

---

### 方向二：多副本部署

**验证**： ✅ 准确，且文档切实低估了影响。

- `internal/pkg/ratelimit/`——`TokenBucket` 确实是每实例的：`sync.Mutex` + `time.Time`，无共享状态。
- `internal/outbox/relay.go`——`relay.FetchUnpublished()` 确实使用了简单的 `WHERE published_at IS NULL ORDER BY created_at LIMIT ?`——无 `FOR UPDATE SKIP LOCKED`，无实例租约。
- `internal/hub/hub.go`——`Hub` 使用 `sync.Map` 将 `uid` 映射到 `chan<- []byte`——完全在内存中。
- `internal/sso/token_cache.go`——`tokenCache` 使用带有 TTL 的 `sync.Map`。
- `internal/sso/user_cache.go`——`UserCache` 相同情况。

**未提及但相关的额外问题**：

1. **Warmup 初始化**（`internal/app/warmup.go`）目前在一个 `sync.Once` 中运行，初始化 SSO 缓存、Hub 等。在多副本场景下，所有实例都会同时冷启动并穿透 SSO——而文档中提及的 SSO 缓存问题仍然存在。
2. **健康检查/就绪检查**不需要更改，但 `internal/handler/http/healthz.go` 中的 `GET /readyz` 检查数据库可达性——多个实例会同时命中数据库，这没问题，但不会检测到实例特定的故障（如 Hub 损坏）。
3. **优雅关闭**（`internal/app/graceful.go`）——多副本下每个实例独立处理 SIGTERM，这没问题，但关闭期间已建立的 SSE 连接丢失，且没有实例间协调。

**对"短期 workaround"的补充说明**：

文档建议使用 `ENABLE_OUTBOX=false` 让单个实例运行 outbox。这在实践中是可行的，但需要添加一个配置项来禁用 outbox relay（当前没有）。此外，SSE 断连通过客户端重连 + `last_event_id` 回放缓解——但当前 SSE 实现（`internal/hub/hub.go`）**没有原生支持** `last_event_id` 回放。`EventStream` 结构体不存储事件历史。需要新增一个 `last_event_id` 查询参数 + 事件 ID 追踪的回放缓冲区才能实现。

**估计**：1–2 周实现 Redis 限流 + outbox 排他锁是可行的。完整的 Redis Pub/Sub SSE 桥接需要再增加 1 周，且需要仔细设计序列化格式。

---

### 方向三：内容版本管理

**验证**： ✅ 整体描述准确，但过于乐观。

- `UpdateMetadata`（`service/file.go`）确实修改了 `name`/`visibility`/`meta`，内容不可变。
- `Edit`（`service/message.go`、`service/comment.go`）覆盖内容，仅设置 `edited_at`。
- `files.checksum` 用于内容寻址去重。

**文档中缺失的问题**：

1. **版本表的大小**：每个编辑操作都会向 `message_versions`/`comment_versions` 插入一行。对于高频编辑的消息（如群聊中的快速修正），版本表会迅速增长。文档没有提及数据保留策略——是否需要 TTL 清理旧版本？或仅保留最后 N 个版本？
2. **Outbox 中 `previous_content` 的存储**：建议在 `message.edited` 事件 payload 中包含 `previous_content`。但当前的 outbox 事件 payload 存储为 `json.RawMessage`，其大小计入 outbox 行。对于大的消息内容，这可能会膨胀 outbox 表。更好的做法是仅存储 `version_id` 引用，让消费者按需获取版本内容。
3. **文件版本 vs 新文件**：文档建议"相同 `owner_uid` + `name` 自动关联为同一文件的不同版本"。这与文档前面声称的"文件内容不可变（新上传 = 新 row）"相矛盾。如果想让文件版本正常工作，要么需要：（a）修改文件上传逻辑，在覆盖时创建新版本而非新行，或者（b）增加一个 `file_group_id` 将多个文件行关联到一个逻辑文件下。该文档没有协调这一矛盾。
4. **回滚的原子性**：`POST /v1/messages/:id/rollback?version=N` 需要在单个事务中复制版本内容到主表并递增 `edited_at`。如果消息已被软删除，回滚是否应当取消删除？文档未说明。

**估计**：1–2 周——假设仅限于消息/评论版本管理。文件版本管理因上述矛盾更为复杂，可能需要再增加 1 周。实际总计约为 2–3 周。

---

### 方向四：Webhook 基础设施

**验证**： ✅ 准确。

- `configs/config.yaml` 模式中确实有一个单一的 `outbox.webhook.url`。
- `internal/outbox/webhook.go`——`WebhookHandler` 从配置读取单个 URL，使用单一签名密钥。
- `internal/outbox/chain.go`——`Chain` 将 Webhook 和通知分发串联在一起：一个失败两者都重试。
- Outbox 事件类型定义（`internal/domain/event.go`）——`EventType` 是一个字符串常量，无版本号。

**重要的补充问题**：

1. **重试隔离**：将 Webhook 从 outbox 重试链中分离出来是本次变更最重要的部分。当前，如果 Webhook 端点返回 500，整个 outbox relay 循环会重试——这可能会延迟本地通知的分发。文档正确地识别了这一点，但没有强调**数据丢失风险**：如果 Webhook 持续失败并超过最大重试次数，则本地通知也会被丢弃。分离重试队列能解决这个问题。
2. **Webhook 订阅的去重和幂等性**：`webhook_deliveries` 表需要使用 outbox `event_id` + `subscriber_id` 上的唯一约束来防止重试期间的重复投递。文档提到"跟踪每次投递状态"，但未提及幂等性保证。
3. **管理 API 的鉴权**：`CRUD /v1/admin/webhooks` 需要管理员角色——目前使用 `middleware.RequireRole("admin")` 实现。管理 API 已在 `internal/handler/http/admin.go` 中存在模式，因此这是可行的。
4. **事件 Schema 版本化**：建议增加 `event_version` 字段——当前的 `internal/domain/event.go` 中 `Event` 结构体有一个 `EventType` 和一个 `json.RawMessage` payload。增加 `EventVersion` 很简单。更困难的是让生产者（文件、消息、评论服务）在事件结构体变化时正确递增版本。

**估计**：2–3 周——合理。Schema 版本化是最大的未知因素。

---

### 方向五：批量操作与数据可移植性

**验证**： ✅ 大部分准确，但遗漏了一个关键细节。

- 确认：所有写操作都是单一实体粒度。
- 确认：无 `GET /v1/me/data` 或 `DELETE /v1/me/account`。
- 确认：不存在 `async_jobs` 表或 Job 框架。
- 确认：软删除不会级联关联数据（reactions、likes、reports）。

**文档未覆盖的关键问题**：

1. **级联删除与 outbox 的交互**：文档建议在消息软删除的 outbox handler 中级联删除 `message_reactions`。但级联操作发生在 outbox 事务的**外部**（outbox handler 在 `MarkPublished` 提交已发布事件之后运行）。这意味着级联删除本身不是事务性的——如果级联删除在 outbox handler 中运行但在 `MarkPublished` 提交之前崩溃，则消息被标记为已发布，但 reactions 不会被清理。更安全的做法是在**同一个数据库事务**中进行级联删除（在 service 层的 `DeleteMessage` 中），将 outbox 事件和级联删除放在一起，而不是在 outbox handler 中进行。

2. **GC 回收器交互**：文档提到"被删除用户的文件仍然有 owner_uid 引用所以不会被 GC 视为 orphan"。这是正确的——`internal/storage/gc.go` 中的 `GarbageCollector` 寻找的是没有匹配 `files` 行的 blob，而非没有匹配用户的 blob。账户级联删除逻辑**必须**在删除文件行之前先清理 blob，否则会导致 blob 泄漏。文中提到"先删除所有文件 blob → 软删除所有消息/评论/文件行"，顺序正确。

3. **SSO `user.deleted` 事件**：文档正确地将此标注为 SSO 侧的依赖。但还有一个更紧迫的问题：即使有 SSO 事件，aero-id 当前的 outbox 架构是**仅出站**的——它不会从外部来源（包括 SSO 的 gRPC stream）消费事件。监听 SSO 事件需要一个入站事件消费机制，该机制目前不存在。这超出了简单的 webhook 监听范围。

4. **批量操作的事务边界**：`POST /v1/messages/batch-delete` 接受 `{ids: [...]}`——如果 10000 个 ID 中有 5 个不属于当前用户，应该怎么办？整体失败还是部分成功？文档没有指定。根据现有惯例（`Forbidden` vs `NotFound`），一个混合所有权条件应当返回 403。但实际操作中，批量 API 通常采用"尽力而为"策略，在最终响应中返回失败项列表。

**估计**：1–3 周，假设以账户级联删除为最小可行版本。支持 SSO 事件消费会增加依赖项，可能使时间表延长至 3–4 周。

---

### 额外盲区：文档未覆盖

我在实际代码库中发现了文档未提及的 3 个额外问题：

1. **服务层事务边界**：`internal/service/message.go` 中的 `DeleteMessage` 通过 GORM 事务包裹消息软删除 + outbox 事件插入。但级联删除（reactions）发生在此事务之外（在当前实现中根本不发生）。文档正确地识别了级联缺失，但低估了将其移入事务所需的工作量。

2. **对话方向中的 SSE 粘连**：方向一（对话）与方向二（多副本）在 SSE 方面存在交互。如果方向一在方向二之前实施，群聊通知需要广播给多个用户——在每实例 Hub 模式下，如果收件人分散在多个实例上，这无法工作。**建议的实施顺序：方向二 → 方向一**，而非文档建议的"方向二 → 方向五 → 方向一"。

3. **Webhook 管理 API 配置覆盖**：方向四建议将 `webhook_subscribers` 存储在数据库中，但当前的 `outbox.webhook` 配置仍然存在。如果两者都活跃，应当忽略配置中的 webhook 而支持数据库中的 webhook，或者从数据库播种配置。文档没有提及迁移/弃用策略。

---

### 总体评估

| 维度 | 评价 |
|---|---|
| 代码证据 | ⭐⭐⭐⭐⭐——所有引用准确，大部分与当前行号相符 |
| 问题识别 | ⭐⭐⭐⭐——发现了正确的盲区，但低估了文件版本和方向二中的 SSE last_event_id |
| 建议方案 | ⭐⭐⭐⭐——务实、渐进式，具有合理的接入点 |
| 实施估算 | ⭐⭐⭐——偏乐观；文件版本的方向三和级联事务的方向五需要更多考虑 |
| 依赖项与顺序 | ⭐⭐⭐——方向一与方向二的 SSE 交互未考虑 |

**这份文档是可靠的起点。** 它识别出了正确的盲区，并在现有代码库基础上提供了扎实的证据，但其估算和交互风险值得审慎对待。我认为最应该从**方向二（多副本）** 开始，因为它是所有生产部署的前提条件，而且方向一和方向四的架构决策都依赖它。

---

是否需要我基于此评审创建一份 `.out.md` 摘要文件？或者您已有具体的下一步行动想法？
