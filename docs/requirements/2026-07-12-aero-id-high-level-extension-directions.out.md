Thank you for sharing this comprehensive analysis document. I've read it in full and cross-verified every code reference against the current repository state. Below is my detailed assessment.

---

## ✅ 代码证据验证结果

I've traced every claim in the document to its actual source code. **All references are accurate.** Summary:

| 方向 | 关键证据 | 验证结果 |
|------|---------|---------|
| **群组消息** | `MessageKind` enum (`shared.go:17-23`) 已预留但无 Group kind；`Message` struct 无 `GroupID`；outbox events 无 `group.*` | ✅ 全部准确 |
| **Redis 共享状态** | `config.go:29-30`"per-instance" 注释；idempotency store 使用 GORM 无跨实例去重；relay 无 `FOR UPDATE SKIP LOCKED` | ✅ 全部准确 |
| **文件后处理** | `File.Meta` 为 `map[string]any`（`file.go:100`）；File 无 `status` 字段 | ✅ 全部准确 |
| **对话聚合列表** | `ListInbox` 返回平面时间线（`WHERE to_uid = ? ORDER BY created_at DESC`），无聚合 | ✅ 全部准确 |
| **Webhook 平台** | `WebhookHandler` 存在但配置写死（`webhook.go`无管理API）；`Chain` 支持多 handler 链式调用 | ✅ 全部准确 |

---

## 🔍 深度扩展分析

### 方向一：群组消息 — 额外的注意事项

- **Message 表索引重构**：现有索引是 `(from_uid, to_uid, created_at DESC)` 和 `(to_uid, created_at DESC)`。添加 `GroupID` 后需要新索引 `(group_id, created_at DESC)` 且需确保 `WHERE group_id IS NULL` 分支仍能用旧索引。
- **通知类型需注册**：当前 `dispatcher.go` 只 switch 3 种 outbox event type（`message.sent`, `message.read`, `comment.replied`）。新增 `group.*` 事件需要在 `NotifType` enum 和 dispatcher 中注册。
- **群组消息的去重/幂等**：当前 idempotency key 绑定 `(uid, endpoint)`。群组场景下，"向 N 人发消息"的逻辑可能会变成一个循环插入——需要在 service 层保证事务性写入 outbox events 和 group messages。
- **群组消息的搜索**：search service 当前按 `(from_uid, to_uid)` 过滤。群组消息需要 `group_id` 作为额外的 filter dimension。

### 方向二：Redis 共享状态 — 架构风险说明

- **Idempotency store 迁移** 是 P0 中最安全的起点：`SET resource:${uid}:${endpoint}:${key} NX EX ${ttl}` 原子性天然支持跨实例。GORM fallback 可以在 Redis 不可用时降级，但需要注意 **数据一致性**——如果 Redis 和 GORM 都写，两个实例可能各拿一个 reservation。
- **Outbox relay 的锁问题**：当前 `FetchUnpublished` 使用 `WHERE published_at IS NULL AND NOT dead_letter ORDER BY created_at ASC` 无锁。多实例时两个实例可能取到相同事件。建议方案：
  - **Phase D.1**（轻量）：加 `FOR UPDATE SKIP LOCKED`（PG 9.5+），GORM 通过 `.Clauses(clause.Locking{Strength: "UPDATE", SkipLocked: true})` 实现
  - **Phase D.2**（成熟）：引入 leader election（etcd/Redis SETNX 租约），仅 leader 轮询 outbox
- **SSO 缓存多级策略**：L1（内存，短 TTL 如 30s）+ L2（Redis，长 TTL 如 5min）的"缓存雪崩"风险——L1 全量过期后所有请求击穿到 Redis，需用 `singleflight` 或 `probabilistic early recomputation`。

### 方向三：文件后处理管线 — 一个被低估的复杂度

文档正确指出了管线方向，但有三个额外风险：

1. **GC 与管线的交互**：当前 GC sweep 在 `storage.GC` 中遍历 `storage_refs` 表删除 `ref_count = 0` 的 blob。如果文件正在处理中（`file.uploaded` 已发出但管线未完成），GC 可能误删原 blob。需要 `File` 的 `status` 字段或管线持有临时 ref。
2. **缩略图的 ref count 策略**：缩略图 blob 的引用计数需要与原文件独立还是共享？建议独立——原文件被删除时缩略图 blob 的 ref count 归零，GC 清理，逻辑清晰。
3. **管线超时的处理**：大视频文件的转码可能远超 outbox relay 的默认超时。建议管线 worker 独立于 relay 进程，relay 只负责触发，worker 负责执行并通过回调更新 `File.Meta`。

### 方向四：对话聚合列表 — SQL 方案的问题

文档给出的 `DISTINCT ON` SQL 在用户有 1000+ 对话时性能会急剧下降，因为：
- 需要对所有 `(least, greatest)` 对做排序
- 无法利用现有索引（现有索引是 `(from_uid)` 和 `(to_uid)` 单向的）

**建议**：使用 **PostgreSQL lateral join** 或 **materialized view**：
```sql
-- 更适合的 lateral join 方案
SELECT DISTINCT ON (conversation_key)
    m.*,
    CASE WHEN m.from_uid = :actor THEN m.to_uid ELSE m.from_uid END AS peer_uid
FROM messages m
CROSS JOIN LATERAL (
    SELECT LEAST(m.from_uid, m.to_uid) || ':' || GREATEST(m.from_uid, m.to_uid) AS conversation_key
) ck
WHERE (:actor IN (m.from_uid, m.to_uid))
  AND m.deleted_at IS NULL
ORDER BY conversation_key, m.created_at DESC;
```
并在 `conversation_key` 上建立索引。

### 方向五：Webhook 平台 — 一个关键遗漏

文档提到了 subscription 管理、投递历史、签名轮换。但遗漏了一个 **关键生产问题**：

**背压时的数据丢失风险**：当前 `WebhookHandler.Handle` 返回 error 时 relay 会重试。但如果订阅者持续 5xx，事件堆积在 outbox 表中（`published_at IS NULL`），堆积到一定程度后会触发 DB 写放大——每次 relay tick 都需要扫描大量未投递事件。

**建议**：在 subscription 级别引入 `rate_limit` 和 `max_consecutive_failures`，达到阈值后自动暂停该 subscription（`enabled = false`）并发出告警。这是比"指数退避"更彻底的解决方案。

---

## 📋 对文档优先级建议的补充

文档的优先级策略总体合理，但有两点调整建议：

### ⚠️ 方向三（文件管线）的隐含依赖

文档列为 P1，但实际上需要**方向二的 Redis 或类似的外部队列**来驱动管线 worker。原因：
- Outbox relay 当前是单进程的，如果管线 worker 和 relay 在同一个进程中，管线阻塞会导致 outbox 事件投递延迟
- 真正可靠的管线架构需要一个外部消息队列（Redis Streams / RabbitMQ / NATS），而不是依赖 outbox 表的轮询

**建议**：方向三与方向二的 Redis 引入应合并规划，方向二的 Phase A/B 完成后方向三才能低风险落地。

### 🚩 方向四（对话列表）应提升至 P1.5

从产品角度看，对话聚合列表是"用户能感觉到的最小可用产品差距"。当前 inbox API 返回的平面消息列表在移动端每次进入应用时都需要前端做大量聚合逻辑，且无法支持：
- "标记整个对话已读"（当前只支持单条消息 mark_read 或 `MarkConversationRead` 但后者已存在）
- 未读 Badge 聚合

**建议**：优先级提升至 P1.5（介于 P1 和 P2 之间），因为它复用基础设施最多（无新依赖），ROI 最高。

---

## 📊 修正后优先级矩阵

| # | 方向 | 新建议相位 | 前置依赖 | 纯新代码量估计 | 风险等级 |
|---|------|-----------|---------|--------------|---------|
| 1 | **群组消息** | P1 | 无（纯新 entity + migration） | ~1500-2000 loc | 🟡 中 |
| 2 | **Redis 共享状态** | **P0.5**（非 P0 因为扩容并非迫在眉睫） | Redis 外部依赖 | ~800-1200 loc | 🟢 低（渐进式引入） |
| 3 | **文件后处理管线** | P1 **（与方向二联动）** | Redis/外部队列 | ~2000-3000 loc（含 worker 进程） | 🟠 中高 |
| 4 | **对话聚合列表** | **P1.5** | 无新依赖 | ~400-600 loc | 🟢 低 |
| 5 | **Webhook 平台** | P2 | 无（纯 CRUD + 表） | ~1200-1800 loc | 🟢 低 |

---

## 总结

这份文档是我在这个迭代周期中看到的**代码证据最扎实、边界情况考虑最全面的分析之一**。五个方向的识别准确，依赖分析和基础设施复用表都很到位。我的补充主要是：
1. 方向四的 SQL 方案优化（lateral join 替代 DISTINCT ON）
2. 方向三对方向二的隐含依赖
3. 方向五的背压问题补充
4. 方向四升到 P1.5

如果有具体方向你希望我进一步做代码级可行性分析（包括需要改动的文件清单、migration 设计、API 签名），随时告诉我。
