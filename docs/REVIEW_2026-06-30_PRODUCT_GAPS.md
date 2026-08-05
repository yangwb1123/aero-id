# aero-id — 产品缺口与平台演进分析

> 审查日期: 2026-06-30
> 视角: 资深架构师 + 产品经理 + 平台工程
> 方法: 全局代码扫描（`internal/` 全量 + `migrations/` + `configs/` + `docs/`）
> 前提: 代码库处于 `feat/roadmap` 完全合并状态（17 个 migration，41 个 gRPC RPCs，完整双传输对等）

---

## 目录

1. [项目当前定位](#1-项目当前定位)
2. [边界情况与防御性编程缺口](#2-边界情况与防御性编程缺口)
3. [性能瓶颈与可扩展性限制](#3-性能瓶颈与可扩展性限制)
4. [高价值扩展方向（5 个）](#4-高价值扩展方向-5-个)
5. [技术债速览](#5-技术债速览)
6. [平台演进路线图](#6-平台演进路线图)

---

## 1. 项目当前定位

aero-id 是一个**以 SSO 为身份源的用户自有数据服务**，三驾马车：

| 数据域 | 用户场景 | 传输 |
|--------|----------|------|
| **文件** | 上传/存储/分享/预览 | HTTP multipart + S3 presigned |
| **消息** | 1:1 私信 + 系统通知 | HTTP + gRPC |
| **评论** | 文件/消息上的两级嵌套讨论 | HTTP + gRPC |

横切能力：全文搜索、用户拦截、通知推送（SSE + Webhook）、审计日志、审核自管。

**一句话总结：** aero-id 是一个精心设计的**单实例可生产部署**的用户内容服务。写完 17 个 migration 后，它在"做正确的事"上已经非常完整（事务、幂等、弹性、可观测）。它面临的瓶颈不在正确性，而在**可扩展的产品形态**和**大规模部署**方向。

---

## 2. 边界情况与防御性编程缺口

### 2.1 多级审核：resolve report + hide 的竞态

**文件：** `internal/service/comment.go` — `ResolveReport()`

**场景：** 作者 A 有评论 C，收到举报 R1、R2（两个不同用户）。管理员审核 R1 时触发 `ResolveReport(R1, resolved)` → 调用 `SetStatus(hidden)`。此时 R2 尚未处理。片刻后另一审核员处理 R2 → `ResolveReport(R2, dismissed)` → `SetStatus(normal)`，覆盖了之前 R1 的隐藏操作。

**问题：** 审核操作的最终状态取决于**最后一个被 resolve 的报告**，而非最严格的裁决（隐藏 > 正常）。两个审核员可以互相抵消对方的审核决定。

**建议：** 引入审核操作的优先级语义：
- `Resolved`（认定违规）→ 设置 `status = hidden`，且只允许从 `hidden → more_severe`，不允许 `hidden → normal`
- `Dismissed`（不违规）→ 仅当当前状态是 `normal` 时才跳过，如果已经是 `hidden` 则维持隐藏
- 或者引入 `moderation_actions` 表记录每条操作，最终的 `status` 是所有 action 的聚合（any Resolved → hidden）

### 2.2 通知与消息的"幽灵已读"

**文件：** `internal/service/message.go` — `MarkRead()`

**场景：** `MarkRead` 在消息已读时同时 emit `message.read` outbox event。如果 `MarkReadWithEvent` 事务成功但 `message.read` 的 outbox event 因某种原因在 relay 层消费失败（handler panic、数据库故障），则：
- 消息行已标记 `read_at`（不可撤销）
- 发送方收不到 read receipt 通知
- 阅读者看到"已读"，发送方看到"未读"——数据不一致

**建议：** 将 read receipt 通知改为**最终一致但幂等**的方式：使 `MarkReadWithEvent` 失败时不回滚 `read_at`（当前行为），但增加一个**补偿任务**——relay 层加一个周期性检查 `read_at IS NOT NULL` 但 `published_at` 不存在的 outbox 事件。或者，更简单：read receipt 改为在 `MarkRead` 后**异步检查**——relay 额外查询最近已读但未发通知的消息。

### 2.3 文件去重 + 共享的交互陷阱

**文件：** `internal/service/file.go` — `Create()`

**场景：** 用户 A 上传文件 F（checksum X）→ 存储 `key = A/uuid-f`。用户 A 再次上传相同文件 → 去重命中，`key` 改为 `existing.StorageKey`（`A/uuid-f`），原本新上传的 blob `A/uuid-g` 被删除。正确。

但如果用户 A 共享文件 F 给用户 B，然后用户 A 删除了文件 F（软删除），文件指向的 blob 因为 `CountByStorageKey == 0` 被删除（因为软删除行不再被 `CountByStorageKey` 计入）。但**共享记录**（`file_shares` 表）仍然存在。用户 B 看到的共享条目指向一个幽灵文件——GET 返回 404。

**建议：**
- `files` 软删除时**级联删除所有 `file_shares`**（或在 soft delete 时标记 `shared_at` 失效）
- 共享列表 API（`ListSharedWithMe`）应该过滤已删除的文件（`WHERE deleted_at IS NOT NULL`）
- 或者共享表本身也软删除

### 2.4 SSE 推送：channel 容量选择与慢消费者

**文件：** `internal/hub/hub.go` — `Subscribe()` 创建 `make(chan SSEEvent, 16)`

**场景：** 16 的 buffer 在以下场景下太浅：
- 客户端断网重连间隙（例如手机进电梯 30 秒），服务端产生了 20 个通知，channel 满后最早的 4 个被 silent drop
- 客户端消费能力弱（如老旧浏览器、被限速的网络），即使连接还在，`Pub` 的非阻塞 send 也会丢弃事件

**后果：** 通知可靠交付（靠 persistence + outbox）与实时推送（靠 SSE）之间出现差距——客户端以为实时推送是可靠的，但实际上 16 的 buffer 随时可能溢出。**目前的 overrun 是静默的，无计数无日志。**

**建议：**
- 增加 channel cap 到 128 或 256（16 对于生产环境太保守）
- 增加一个 Hub 的 `DroppedEvents` 计数器（atomic），在 `select default:` 分支 `atomic.AddUint64(&h.dropped, 1)`
- 定期 log 丢弃率，暴露为 Prometheus 指标 `sse_dropped_events_total`
- 考虑用有界 ring buffer 替代 channel，让慢消费者可以追赶

### 2.5 搜索结果的可见性二次过滤

**文件：** `internal/repository/message.go` — `SearchMessages()`

**场景：** 搜索使用 `WHERE (from_uid = ? OR to_uid = ?)` 来隔离用户。但对于**评论搜索**，可见性更复杂：

```sql
-- SearchComments 的查询（推断）
SELECT ... FROM comments
WHERE (target_type = 'message' AND target_id IN (SELECT id FROM messages WHERE from_uid = ? OR to_uid = ?))
   OR (target_type = 'file' AND target_id IN (SELECT id FROM files WHERE owner_uid = ? OR visibility = 'public'))
```

但这不覆盖**文件共享**的场景——用户 B 通过 `ShareFile` 获得了文件 F 的读取权。B 搜索评论时，目标为 F 的评论应该出现在结果中，但当前 SQL 不检查 `file_shares` 表。

**建议：** SearchComments/ListByTarget 需要扩展可见性检查，包含 `file_shares` 的访问权限。这会将一个简单的 `WHERE` 查询变成带子查询或 JOIN 的复杂查询——但数据正确性高于查询复杂度。

### 2.6 文件引用计数反向依赖的死锁风险

**文件：** `internal/service/file.go` — `Delete()` 调用 `CountByStorageKey`；GC sweeper 也调用 `CountAnyByStorageKey`

**场景：** 如果 `files` 表有大量并发操作（上传 + 删除不同文件但相同 storage_key 的场景——同内容去重导致），`CountByStorageKey` 的 `SELECT COUNT(*) FROM files WHERE storage_key = ?` 在默认的 Read Committed 隔离级别下可能读到过时的结果。

**建议：** 用引用计数列（`storage_ref_count`）代替查询计数，`INSERT` 时 +1，软删除时 -1，用原子 UPDATE 保证一致性。GC sweeper 直接读该列，无需 COUNT 查询。

### 2.7 消息编辑与已读回执的交互

**文件：** `internal/service/message.go` — `Edit()` 和 `MarkRead()`

**场景：** 用户 A 发送消息 M。用户 B 读取 M（`read_at` 已设置）。用户 A 编辑 M。用户 B 不知道 M 已被编辑——对比 Slack/微信的"消息已编辑"提示。

**建议：**
- `MarkRead` 后编辑消息时，emit `message.edited` outbox event → 通知接收方
- 或者在消息列表/会话中显示 `edited_at` 标记，客户端自行识别

---

## 3. 性能瓶颈与可扩展性限制

### 3.1 1:1 消息模型的架构天花板

**当前设计：** `messages` 表用 `(from_uid, to_uid)` 表示会话。`ListInbox` 按 `to_uid` 查询并 `ORDER BY created_at DESC`。`ListConversation` 用 `(from_uid=x AND to_uid=y) OR (from_uid=y AND to_uid=x)`。

**瓶颈：**
- **群聊不可行。** 无法用 `(from_uid, to_uid)` 表达三人以上的对话。如果改 `to_uid` 为 `conversation_id`，整个 inbox 查询逻辑、未读计数、通知路由都要重写。
- **大会话的查询性能。** 两人长聊 10,000 条消息后，`ORDER BY created_at DESC LIMIT 21` 的 cursor 翻页仍需扫描大量行。
- **写放大。** 群聊场景（虽然当前不支持）下，N 个成员写 1 条消息需要 N 个 `message.received` 通知。如果 N=1000，一次发送产生 1000 条通知行 + 1000 次 SSE push。

**建议方向（属于扩展方向 1）：** 引入 `conversations` 表，`messages` 加 `conversation_id`。`ListInbox` 改为按 `conversation_members` JOIN 查询。未读计数改为 per-conversation 缓存。

### 3.2 通知系统的写放大与批量优化

**当前设计：** `NotificationDispatcher.Handle()` 每条事件调用一次 `notifs.Create()`。群消息（如果实现）需要 N 次 Create。

**瓶颈（假设群聊实现后）：**
- 群聊消息发送 → outbox event "message.sent"（1 行 outbox + 1 行 message）
- Relay 消费 → `Handle()` 被调用 → 检查 block × N → 写 N 行 notification
- 每行 notify 独立 INSERT，N=1000 时产生 1000 个小事务

**建议：**
- `NotificationRepository` 增加批量 Create 方法（`BulkCreate(ctx, []*domain.Notification)`）
- `Handle()` 改为分组批量：一次性收集同一事件的所有收件人，一次批量写入
- SSE push 也批量：每个收件人一次 push，但所有 push 共享一次事件解析

### 3.3 rate limiter 的 per-instance 限制

**当前设计：** `internal/pkg/ratelimit` 是 process-local in-memory token bucket。注释明确说"Distributed/global rate-limiting is intentionally out of scope"。

**生产场景：** 4 个 aero-id 实例（K8s replicas=4）behind a load balancer。limit 配置为 `rps: 20, burst: 40`。实际容量 = 4 × 20 = 80 RPS。单个用户发 60 RPS，4 个实例各自看到 ~15 RPS，全部通过。

**副作用：** `idle_ttl: 10m` + `cleanup_every: 1m` 的 eviction 策略在每实例独立运行，同一用户在 4 个实例上有 4 个 bucket，各自独立 refill。

**建议：**
- 引入可选的 Redis-backed limiter（`internal/pkg/ratelimit/redis.go`），与 in-memory 版本共享同一 `Limiter` 接口
- 配置项 `rate_limit.driver: memory | redis`，默认 `memory` 保持向后兼容
- 如果使用 Redis，key 命名 `aeroid:ratelimit:{key}`，用 `CL.THROTTLE` 或 Lua INCR + EXPIRE

### 3.4 SSE Hub 的内存模型

**当前设计：** 每个 SSE 连接创建一个 cap=16 的 channel。每个 uid 有 `map[int64]chan SSEEvent`。`Publish` 遍历所有 channel 非阻塞 send。

**瓶颈：**
- 如果 10,000 用户在线，10,000 个 channel × 16 = 160K 事件槽，加上 map overhead 约每个连接 2KB → ~20MB 堆内存，可控。
- 但每条通知要遍历所有 channel（可能 10-100 个/uid），`RLock` 保护的时间与设备数成正比。
- 无上限控制：一个 uid 可以开无限个 SSE 连接（每次 `GET /v1/events` 创建一个新的 subscription），恶意用户可以耗尽服务端内存。

**建议：**
- 对每个 uid 设置最大并发连接数（`max_sse_connections_per_uid: 10`），超出的 `Subscribe()` 返回 `http.StatusTooManyRequests`
- 考虑用 `sync.Pool` 复用 channel 内存分配
- 用 per-uid 的 `atomic.Int64` 跟踪连接数

### 3.5 全文搜索的 tsvector 更新实时性

**当前设计：** `search_vector` 列通过 trigger 在 INSERT/UPDATE 时自动更新（migration 000016）。

**场景：** 消息编辑后 tsvector 自动更新。但编辑频率高时（如 bot 反复修正消息），每次 UPDATE 都触发 tsvector 重建，对高频编辑场景有写放大。

**建议：**
- 考虑延迟更新策略：消息编辑超过 30 秒内的连续编辑合并为一次 tsvector 更新
- 使用 PostgreSQL 的 `tsvector_update_trigger()` 函数时注意性能，在大表上批量 UPDATE 会重建整个索引

---

## 4. 高价值扩展方向（5 个）

### 🚀 方向 1：多租户 / 组织工作空间

**概念：** 在当前"用户"层之上增加"组织 / 工作空间 / 团队"层。每个组织拥有独立的文件、消息、评论空间。用户可属于多个组织。

**为什么需要它：**
1. **产品形态从"个人工具"到"协作平台"的必由之路。** 当前所有数据按 uid 归属。组织场景需要：组织空间内的文件库、团队成员间消息隔离、组织级权限（谁可以看到哪些文件）。
2. **多租户是 SaaS 化的基础。** 如果一个组织需要独立的数据隔离（不能看到其他组织的数据），单靠 uid 前缀隔离不够安全——需要显式的 `tenant_id` 列。多租户设计有两条路径：**共享表 + 行级隔离**（Row-Level Security）和 **schema 级隔离**。后者适合合规敏感场景。
3. **与现有架构兼容度高。** 所有表已有 `owner_uid` / `from_uid` 等用户标识。加 `tenant_id` 列并建复合索引，现有查询基本不需要重写。
4. **SSO 可以是组织感知的。** SSO 已经提供了 uid → user 的映射；扩展协议使 SSO 能返回 `default_tenant_id` 或 `tenant_ids[]`，aero-id 使用它进行路由。

**实现要点：**
- 新增 `tenants` + `tenant_members` 表（role: owner/admin/member）
- 所有核心表加 `tenant_id` 列（可为 NULL 表示个人空间，向下兼容）
- 新增中间件 `TenantInterceptor` / `TenantMiddleware`，从请求头 `X-Tenant-ID` 或 token claim 提取
- Repository 层加 `WithTenant(ctx, tenantID)` scope 方法，所有查询自动加 `tenant_id = ?`
- 列表查询（ListByOwner、ListInbox）优先使用 `(tenant_id, uid)` 复合索引
- 注意：**这是有史以来最大的架构变更**——影响所有 17 个表、所有 repository、所有 service 的 actor 获取方式。建议渐进式：先加列不做强制约束，再开新端点

### 🚀 方向 2：消息会话管理（归档/静音/置顶/离开）

**概念：** 在 1:1 消息之上增加成熟的会话管理能力，让用户可以：
- **归档会话**（从 inbox 隐藏但不删除）
- **静音会话**（不接收新消息通知）
- **置顶会话**（固定在 inbox 顶部）
- **离开/删除会话**（删除自己的会话视图，对方不受影响）

**为什么需要它：**
1. **当前 inbox 是平坦的。** 所有会话按最后消息时间排序——没有优先级、没有分类。10 个活跃会话 + 50 个一周前结束的会话混在一起。
2. **Slack/Telegram/微信的基本功能。** 用户期望这些功能。没有它们，inbox 在长期使用后变得不可管理。
3. **实现结构清晰。** 只需要一张新表 `conversation_settings（uid, peer_uid, muted, archived, pinned_at, deleted_at）`。现有的 `ListInbox` 查询 JOIN 这张表做过滤和排序。不需要动 `messages` 表。
4. **与现有通知系统集成。** `NotificationDispatcher.Handle()` 中检查静音→跳过通知写入。`MarkAllRead` 改为 per-conversation。

**实现要点：**
- `conversation_settings` 表：`(uid, peer_uid)` 复合主键 + `muted_at`, `archived_at`, `pinned_at`, `deleted_at`
- `ListInbox` 增加 `filter` 参数：`active`（默认，排除 archived/deleted）、`archived`、`all`
- `GET /v1/conversations/:peer/settings` + `PATCH /v1/conversations/:peer/settings`
- 通知静音：`Dispatcher` 新增 `ConversationSettingsChecker`，类似 `BlockChecker` 模式
- 置顶：`ListInbox` 增加 `ORDER BY pinned_at DESC NULLS LAST, last_message_at DESC`

### 🚀 方向 3：数据导出 / 合规（GDPR / 数据可移植性）

**概念：** 允许用户通过自助服务导出自己的全部数据（文件、消息、评论），格式为标准化的 JSON + 文件归档。

**为什么需要它：**
1. **法律合规要求（GDPR 第 20 条）。** 如果面向欧洲用户，数据可移植性（data portability）是法定权利。即使不面向欧洲，越来越多 jurisdictions（加州 CCPA/CPRA、巴西 LGPD）要求相同的能力。
2. **用户信任的关键信号。** "我随时可以带着我的数据离开"是用户决定是否深度使用一个平台的重要心理因素。没有导出功能，用户数据被"锁定"。
3. **产品差异化。** 很多竞品不提供便捷的导出——这是一个低投入、高信任收益的功能。
4. **技术基础已存在。** Outbox 机制（事件溯源的基础） + 审计日志（操作记录）+ 全表可查询（repository 层）——导出本质上是"读取全量数据 + 打包"。

**实现要点：**
- `POST /v1/export` → 创建导出任务（async），返回 `task_id`
- `GET /v1/export/:task_id/status` → 查询进度
- `GET /v1/export/:task_id/download` → 下载 zip 归档（含 JSON 清单 + 原始文件）
- 导出范围可配置（`file: true, message: true, comment: true`）
- 文件导出：不包含非 owner 文件（仅 owner 的），但包含共享给该用户的文件元数据（不含 blob）
- 后台用 Job Queue（同方向 2 的缩略图管道）执行导出：查询数据 → 写临时文件 → 签名 URL 供下载
- TTL：导出归档 7 天后自动删除
- 注意：大用户的数据导出（10GB+ 文件）需要流式 zip 生成，不能全量在内存中

### 🚀 方向 4：消息全文搜索增强（语义搜索 + 搜索建议 + 过滤）

**概念：** 当前全文搜索是基础的 PostgreSQL tsvector。增强为：
- **搜索建议**：输入时返回补全建议（从消息历史提取的热门查询）
- **过滤条件**：按日期范围、发送方、是否有附件、会话过滤
- **语义相关性排序**：结合 `ts_rank` 加上 recency boost（近期消息排名更高）
- **搜索结果高亮**：`ts_headline()` 生成带 `<mark>` 的摘要

**为什么需要它：**
1. **搜索是消息系统的核心功能。** 一旦用户积累了上千条消息，搜索效率直接决定产品可用性。当前 `LIMIT 50` + `ts_rank` 在小规模下够用，但 10 万条消息后用户会感到搜索结果不精确。
2. **中文搜索仍然有问题。** `simple` 字典不切分中文（前文已述）。这是实际可用性问题，不是性能问题。
3. **搜索结果缺少上下文。** 当前只返回命中的消息本身，不返回前后的上下文（类似 Slack 的"显示周围消息"）。用户搜到一个词但不知道它来自什么对话。
4. **竞争产品对标。** Slack 的搜索、Discord 的搜索、Gmail 的搜索——这些产品在搜索体验上投入巨大。aero-id 目前是"能用"，不是"好用"。

**实现要点：**
- 增加 `ts_headline()` 支持 → 返回带高亮的摘要片段
- 增加 `search_suggestions` 表：从消息内容提取的 n-gram 或前缀索引，支持 `LIKE 'prefix%'` 快速补全
- 搜索 API 增加 `date_from`, `date_to`, `from_uid`, `conversation_with`, `has_file` 等过滤参数
- 排序算法：`ts_rank * 0.7 + recency_score * 0.3`，recency_score = `1.0 / (1.0 + days_ago / 30.0)`
- 上下文查询：搜索命中消息的同时，返回该消息前后的各 3 条消息（通过 `created_at` 范围查询）

### 🚀 方向 5：Webhook 事件平台的深化（事件订阅 + 重试治理 + 事件回放）

**概念：** 当前 webhook（outbox 链）已经支持发送事件到外部 endpoint。扩展为一个完整的事件平台：
- **事件订阅管理**：第三方开发者可以通过 API 注册 webhook URL + 选择事件类型
- **签名版本化**：当前是 HMAC-SHA256，增加签名版本号支持轮转
- **死信队列**：超过重试上限的事件进入 DLQ（dead letter queue），可查询和手动重放
- **事件回放**：按时间范围重新投递已发布的事件（恢复、迁移、测试场景）
- **速率限制**：per-endpoint 的投递速率控制，保护接收方不被淹没

**为什么需要它：**
1. **事件驱动架构的平台杠杆。** aero-id 已经构建了可靠的事务性 outbox 基础设施——这是最难的 80%。剩下的 20%（订阅管理、死信、回放）把这个基础设施从"内部集成管道"变成"面向开发者的平台产品"。
2. **生态系统的入口。** 外部开发者通过 webhook 可以构建：消息存档机器人、评论审核工作流、文件自动备份、数据同步到外部分析系统。这些都不需要改动 aero-id 代码。
3. **当前 webhook 的最简实现有缺口。** 无订阅管理（硬编码配置）、无投递保障（重试上限后永久丢弃）、无监控。
4. **与审计日志互补。** 审计日志记录"谁做了什么"，事件平台记录"系统发生了什么"——两者共同构成完整的操作可观测性。

**实现要点：**
- `webhook_subscriptions` 表：`(id, owner_uid, url, event_types[], signing_key, headers JSONB, active, created_at)`
- `CRUD API`：`POST /v1/webhook-subscriptions`、`GET /v1/webhook-subscriptions`、`PATCH /v1/webhook-subscriptions/:id`、`DELETE /v1/webhook-subscriptions/:id`
- 死信队列：`outbox_events` 加 `dead_letter_at` 列，`FetchUnpublished` 排除 `dead_letter_at IS NOT NULL`
- 重试策略：`max_retries: 10`，超过后设置 `dead_letter_at = now()`，人工介入
- 事件回放：`POST /v1/admin/replay-events`（from, to, event_type filter），写入一个新的 outbox 事件（`event_type: replay.batch`）触发回放 worker
- 速率限制：每个 webhook URL 独立 token bucket，默认 `rps: 10, burst: 20`

---

## 5. 技术债速览

| 等级 | 问题 | 影响面 | 文件 |
|------|------|--------|------|
| High | `like_count` UPDATE 无锁，高并发漂移 | 数据正确性 | `internal/repository/comment.go` |
| High | `idempotency_keys` 无 TTL 清理 | 存储无限增长 | `internal/idempotency/store.go` |
| High | FTS 中文不可用（`simple` 字典） | 产品可用性 | `internal/repository/*_search.go` |
| Medium | cache 全无，每个 `Get` 都穿透 DB | P99 延迟 | 全 repository 层 |
| Medium | SSE 无 `last_event_id`，重连丢通知 | 可靠性 | `internal/handler/http/events.go` |
| Medium | Outbox handler 失败无 backoff | 资源浪费 | `internal/outbox/relay.go` |
| Medium | 搜索无 `statement_timeout` | 连接池安全 | `internal/repository/*_search.go` |
| Low | 通知洪泛无防护 | DoS | `internal/service/notification.go` |
| Low | GC 默认关闭，生产易忘 | 存储成本 | `configs/config.yaml` |
| Low | SSE 无连接数上限 | 内存 DoS | `internal/hub/hub.go` |

---

## 6. 平台演进路线图

```
Time ────────────────────────────────────────────────────────────────>
     │
     │  Q3 2026                           Q4 2026                       Q1 2027
     │
     ├── 生产加固                         ├── 产品能力扩张                 ├── 平台化
     │   ├─ like_count 原子化              │   ├─ 多租户（方向1 Phase1）     │   ├─ 事件平台（方向5）
     │   ├─ idempotency TTL 清理           │   │  └─ tenant 列 + RLS        │   ├─ 多租户（方向1 Phase2）
     │   ├─ SSE last_event_id              │   ├─ 消息归档/静音（方向2）    │   │  └─ 组织级权限
     │   └─ outbox backoff                 │   ├─ 数据导出（方向3）         │   ├─ 第三方应用集成
     │                                     │   └─ 搜索增强（方向4）         │   └─ 数据合规报告
     │
     └── 基础设施                           └── 运维体系                    └── 生态
         ├─ FTS 中文分词支持                  ├─ 分布式限流（Redis）           ├─ 开发者文档
         ├─ 通知批量写入                      ├─ 实体缓存层                    ├─ SDK / Client Libs
         ├─ 通知洪泛门限                      ├─ 自动扩缩容压测               └─ 社区案例
         └─ SSE 连接数上限                    └─ runbook + 告警规则
```

**决策原则：**
1. **先修正确性，再谈性能。** High 级的技术债（like_count、idempotency TTL）在任何新功能之前修复。
2. **产品缺口优先于架构改造。** 方向 2（归档/静音）和方向 3（数据导出）可以在不改变核心数据模型的情况下实现——2-4 周交付。
3. **架构级方向需要 EPIC 拆分。** 方向 1（多租户）和方向 5（事件平台）影响所有层面。建议先做设计文档（RFC），再做 Phase 1 最小可行。

---

## 附录：关键数据流审查速查

| 数据流 | 有事务？ | 有幂等？ | 有超时？ | 有重试？ | 可观测？ |
|--------|---------|---------|---------|---------|---------|
| 文件上传 | ✅ | ✅ idempotency key | ✅ request_timeout | ❌ 上传失败需重传 | ✅ metrics |
| 消息发送 | ✅ outbox | ✅ | ✅ | ✅ relay retry | ✅ |
| 评论创建 | ✅ atomic+evt | ✅ | ✅ | ✅ | ✅ |
| 通知推送 | ✅ | N/A（create 幂等） | N/A | ✅ relay retry | ✅ metrics+log |
| 全文搜索 | N/A（只读） | N/A | ❌ 无 statement_timeout | N/A | ✅ 慢查询待加 |
| 文件 GC | ❌ 非事务 | N/A | N/A | ✅ 下一 tick | ✅ metrics+log |
| 文件分享 | ✅ | ✅ upsert | ✅ | N/A | ✅ audit log |
| 用户拦截 | ✅ | ✅ upsert | ✅ | N/A | ✅ audit log |
