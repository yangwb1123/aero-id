现在我的分析已完整，以下是逐方向验证：

---

## 逐方向代码级验证

### 方向 1：群组会话 + 多参与者

| 文档中的断言 | 代码证据 | 验证 |
|---|---|---|
| 当前消息严格 1:1 | `domain/message.go:5` — 只有 `FromUID`/`ToUID`，无 `ConversationID` | ✅ |
| `messages.kind` 已预留 `system` | `domain/shared.go:23` — `MessageKindSystem = 4`，`IsValid()` 已包括 | ✅ 但确认：只是枚举值，没有 `MessageKindGroup` |
| outbox 可生成 `message.sent` 事件 | `internal/notification/dispatcher.go:82` — switch on `e.EventType` 已有 `message.sent` case | ✅ |
| 参与者列表模型可复用 `user_roles` 模式 | `domain/admin.go:22` — `UserRole` 结构体是 `(uid, role)` 模式 | ⚠️ 部分——`UserRole` 是全局角色，群组参与者是 (group_id, uid, role) 需要新实体 |

**补充发现：** 当前 `idx_messages_inbox` / `idx_messages_outbox` 索引是 `WHERE deleted_at IS NULL` 的部分索引。群组迁移后需要新索引覆盖 `conversation_id + created_at DESC`。而且 `outbox.Chain` 需要确认是否支持并行 handler——目前是顺序链，群组事件扇出到 N 个参与者需要确保 dispatcher 的 `Handle` 方法不会因为 N 大而阻塞太久。

---

### 方向 2：移动推送通知

| 文档中的断言 | 代码证据 | 验证 |
|---|---|---|
| 通知通过 SSE 传递，断开连接收不到 | `hub/hub.go:130` — SSE Hub 需要 WebSocket 或长轮询等效连接 | ✅ |
| `NotificationDispatcher` 写入通知行 | `service/notification.go:113-114` — `NewNotificationDispatcher` 接收 `NotificationRepository` | ✅ |
| `hub.Pusher` 接口已定义 | `hub/hub.go:133` — `Publish(uid, eventType, data string)` | ✅ |
| `NotificationPreference` 有 `QuietHoursStart/End` | `domain/notification.go:35` — `NotificationPreference` 结构体 | ✅ |
| 度量标准已跟踪出站事件 | `pkg/metrics/metrics.go:42-43` — `outboxPublished`/`outboxFailed` 按 event_type 统计 | ✅ |

**补充发现：** 代码中完全不存在 `device_registrations`、`push_token`、`FCM`、`APNs` 的任何引用——方向 2 确实是崭新的。而且 `outbox.Chain` 的架构使得以**独立 handler** 方式实现推送非常干净（如你所说），只需在 `app.go:185-201` 的 Chains 中加入：

```go
relayHandler = outbox.NewChain(dispatcher, webhookHandler, pushHandler)
```

但**关键验证点**：需要确认 `NewChain` 是否在某个 handler 出错时中断后续 handler。如果中断，推送 handler 应放在 dispatcher **之前**（先推送后持久化，推送失败不阻止通知落地）或 Chain 内部需要变为 fan-out。建议先读 `internal/outbox/chain.go` 或 `NewChain` 的实现。

---

### 方向 3：资源配额与使用治理

| 文档中的断言 | 代码证据 | 验证 |
|---|---|---|
| 只有 `storage.max_upload_bytes` 限制 | `config/config.go:18` — 无其他配额字段 | ✅ |
| `CountByStorageKey` 等聚合查询已存在 | `repository/file.go:130` — `CountByStorageKey`；`repository/message.go:263` — `CountUnread` | ✅ |
| 速率限制器扩展（`internal/pkg/ratelimit`） | `config/config.go:24-30` — `RateLimitConfig` 定义了 `RPS`/`Burst` | ✅ |
| audit 记录敏感操作 | `domain/audit.go:29` — `AuditEvent` 结构体 | ✅ |

**补充边界发现：** 每日计数器的查询 `WHERE owner_uid = ? AND created_at > now() - interval '1 day'` 在消息表上可以被 `idx_messages_outbox (from_uid, created_at DESC) WHERE deleted_at IS NULL` 覆盖。但在评论表上，当前索引结构可能不充分——需要验证 `idx_comments_target` 或类似索引是否有 `(owner_uid)` 前缀。

另外，"哨兵值"方案你提到了 `max_storage_bytes = 0`（无限）或 `-1`（阻止）——通常更安全的做法是用两个独立字段：`quota_override_enabled bool` + `quota_value`，避免与默认配置混淆。

关于你提到的 **HTTP 429 / gRPC `ResourceExhausted`** 的 map：当前 `pkg/errors` 已有 `BadRequest`/`Forbidden` 等，确认需要新增 `ErrQuotaExceeded`，其 HTTP 映射为 `429`，gRPC 映射为 `ResourceExhausted`。这与方向 5 的速率限制共享同一个错误码空间。

---

### 方向 4：数据可移植性——GDPR

| 文档中的断言 | 代码证据 | 验证 |
|---|---|---|
| 软删除（`DeletedAt`）在所有实体上 | `domain/message.go`、`domain/file.go`、`domain/comment.go` 均有 `DeletedAt` | ✅ |
| `fileService.Delete` 处理 blob | `file/service_crud.go:146` — `CountByStorageKey` 确认引用计数 | ✅ |
| storage GC 清理孤立 blob | 文档路线图中提到已实现 | ✅ |
| outbox 和通知引用用户 ID | `domain/notification.go:19` — `Notification.RecipientUID` | ✅ |

**补充发现：** 这个方向在所有现有文档（包括 `docs/requirements/` 中的其余 2 份分析）中**均未提出**。你的版本是目前唯一将 GDPR 合规作为独立方向的。这是一个显著差异点，也是高价值点——文件服务存储用户数据却无"删除我的所有数据"的一键操作，在生产环境中确实是一个合规风险。

需要注意的工程细节：

1. **出口文件 blob 的导出策略**：你文档中写"文件列表（元数据，而非 blob）"——这是正确的决定。blob 数据量可能很大，导出归档应包含下载链接（presigned URL，带过期时间），而非直接包含二进制数据。

2. **消息的"双向删除"**：`from_uid = ? OR to_uid = ?` 目前是合理的。但需要确认业务语义——当你"删除我的数据"时，收件人是否也应丢失该消息？GDPR 的"被遗忘权"要求你自己的数据被删除，但不要求你发出的消息从别人的会话中消失。一个可接受的折中是：消息在你这边变为"已删除用户"，但文本内容仍在对方的会话中保留（类似 WhatsApp 的"此消息的发送者已删除其账户"）。

---

### 方向 5：分布式速率限制与多实例协调

| 文档中的断言 | 代码证据 | 验证 |
|---|---|---|
| 当前限制器是进程内、仅内存 | `config/config.go:222-227` — 注释标注了 per-instance | ✅ |
| `ratelimit.Limiter` 有干净接口 | `Allow(key) bool` 模式的存在 | ✅（通过注释推断，**建议确认接口签名**） |
| HTTP 和 gRPC 通过 `WithRateLimit` 注入 | `middleware/ratelimit.go` 的存在 | ✅ |
| 幂等性键存储使用 Postgres | `internal/pkg/idempotency/store.go` — GORM 表 | ✅ |
| 出站 relay 竞争事件 | `outbox/relay.go` — 无 leader election | ⚠️ 确认 relay 本身是单 pod 设计，但多 relay 确实会竞争 |

**补充发现：** 你的选项 B（Redis）描述中提到：

> Redis INCR + EXPIRE（滑动窗口算法，或用于固定窗口的 SETEX）

对于生产级速率限制，推荐使用 **GCRA (Generic Cell Rate Algorithm)** 或 **Sliding Window Log**，单纯 `INCR` + `EXPIRE` 的固定窗口在窗口边界处允许 2x 的突发流量（trample effect）。不过对于 1.0 版本的简单限制，你说的滑动窗口日志更合适。

关于**出站 relay 协调**，你提到：

> 在 relay 之间添加 leader election（通过 Postgres 咨询锁或 leader_elections 表）使单个 relay 成为活跃的 relay；其他备用。

这是一个好思路。当前 relay 的 `FetchUnpublished` 查询没有 `FOR UPDATE SKIP LOCKED`——这意味着多实例确实会有**重复工作**（两个 relay 同时 fetch 相同的事件行，都尝试 mark_published，第二个会因为 `WHERE published_at IS NULL` 不命中而跳过，但浪费了 `Handle` 调用）。Postgres advisory lock 的方案较轻量：`SELECT pg_try_advisory_lock(123456)` 在 relay loop 开头检查。

---

## 建议排序的再评估

你文档中的排序是：

1. **方向 2（推送通知）** → 实际上与方向 4（GDPR 导出/删除）共享"设备注册"模型：`user_devices` 表。如果先做方向 2，方向 4 可以复用设备表来推送"导出就绪"的通知。

2. **方向 3（配额）** → 与方向 5 共享 `pkg/errors` 的错误码扩展。

3. **方向 1（群组聊天）** → 最大产品差异点。

4. **方向 5（分布式限制器）** → 与方向 3 共享相同的 `ratelimit.Limiter` 接口，但方向 3（配额检查）可以独立于速率限制实现。

5. **方向 4（GDPR 导出/删除）** → 合规。

**一个替代排序供参考：**

| 排序 | 方向 | 理由 |
|---|---|---|
| **Phase 1** | 方向 2（推送） | 同期可完成设备注册表，为方向 4 打好基础 |
| **Phase 1** | 方向 3（配额） | 独立实现，低成本高安全收益 |
| **Phase 2** | 方向 1（群组） | 架构影响大，需要谨慎的 schema 迁移设计 |
| **Phase 3** | 方向 4（导出/删除） | 复用了 Phase 1 的设备注册 + Phase 2 的 conversation 级联删除 |
| **Phase 3** | 方向 5（分布式限制器） | 在需要扩容前都是可选；Redis 依赖是新增 |

如果 Phase 1 并行进行方向 2 和 3，（方向 3 的导入路径不需要方向 2 的任何东西），你可以获得两个独立的增量交付物。

---

## 被拒绝方向附录的评估

你附录中的 5 个被拒绝方向——确认它们都不在现有 `docs/requirements/` 的任何分析中出现过，这是代码扫描得出的合理去重结果。其中：

- **文件版本控制**：你正确拒绝了。当前 `storage_refs` 表使用的是引用计数去重，而非版本链。
- **实时协作（文档编辑）**：超出 aero-id 范围，正确。
- **第三方 OAuth 作为 SSO 供应商**：SSO 接口是 gRPC，aero-id 不处理身份提供商逻辑，正确。
- **端到端加密**：与全文搜索冲突的且与去重冲突，正确。
- **WebSocket 替代 SSE**：`hub.SSE` 已实现 `last_event_id` 重放，在此阶段加 WebSocket 不会带来产品层面的增量。

这份拒绝列表很干净。

---

## 总结

这是一份与代码库现状高度吻合、可落地的扩展分析。五个方向都不与已有实现重叠，各自有清晰的"现有基础→新增内容→边界情况"结构。**方向 4（GDPR 导出/删除）** 是这份分析中相对最独特的增量——在 `docs/requirements/` 中没有任何其他文件提及过，如果 aero-id 要在欧洲运营，这将是合规基线。

唯一建议立即确认的是 `outbox.Chain` 的短路行为（方向 2 依赖此设计决策），以及 `ratelimit.Limiter` 接口的完整签名（方向 3 和 5 依赖）。
