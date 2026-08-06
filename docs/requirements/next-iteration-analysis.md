# aero-id — 下一轮扩展方向分析

> 撰写日期：2026-07-11  
> 作者：架构/产品联合评审  
> 范围：全代码库扫描后，基于**已交付功能**识别下一阶段高价值扩展方向、边界缺口和性能优化机会。  
> 约束：不写代码，不重述 `ROADMAP.md` 中已关闭的条目。

## 当前状态摘要

代码库已经从最初的 MVP CRUD 演进为一个功能丰富的生产级服务：

| 领域 | 已交付能力 |
|------|-----------|
| 文件 | CRUD、S3/local 双后端、流式上传、大小/MIME 限制、content-dedup（checksum + storage_refs）、预签名上传/下载、显式分享、元数据更新、GC 回收 |
| 消息 | CRUD、收件箱/发件箱、react/unreact、已读回执（outbox 驱动）、会话视图、编辑、软删除、block 感知 |
| 评论 | 双层嵌套、点赞/取消赞、编辑、举报/审核队列、软删除 |
| 通知 | 基于 outbox 异步派发、首选项（启用/关闭/免打扰时段）、折叠合并、SSE 实时推送、last-event-id 断线续连 |
| 搜索 | PostgreSQL FTS（tsvector + GIN）、messages/comments/files 三合一 |
| 审核 | admin 角色（moderator/admin）、permission 中间件、评论隐藏/取消隐藏、审计日志查询 |
| SSO 弹性 | 熔断、singleflight、serve-stale、重试退避、keepalive、TLS/mTLS、证书锁定 |
| 运维 | RED metrics、OTel tracing、健康检查（per-dependency）、优雅关停、速率限制（per-actor/IP）、幂等性、outbox 死信 + webhook 投递、存储 GC |
| 管道 | 事务型 outbox、storage_refs 引用计数、idempotency key 清理、后台预热 |

**基础设施矩阵完善度极高**——速率限制、追踪、熔断、outbox、preference、block、role 体系均已落地。下一轮应聚焦于**产品体验的完整性和边界情况**，而非继续堆基础设施。

---

## 方向一：多人群聊与会话管理

### 当前状态

消息模型是严格的 1:1 私信（`messages.from_uid` ↔ `messages.to_uid`）。`messages.kind` 预留了 `1=文本, 2=图片, 3=文件附件, 4=系统通知`，但没有**群组**概念。`/v1/conversations/:uid` 只是与某人的消息列表，不是真正的会话实体（无会话元数据、无成员列表、无标记状态）。

### 为什么需要

- **产品层面**：群聊是即时通讯的标配。不支持群聊意味着锁定在"一对一"场景，无法覆盖团队协作、兴趣小组、公告频道等高频用例。
- **架构层面**：当前模型扩展起来相对直接——`messages` 表已经支持 `to_uid` 存储目标，但群聊需要引入 `conversations` 实体表（含 `type`、`title`、`avatar`）、`conversation_members` 关联表（含 `joined_at`、`role`）、以及群组维度的通知折叠和已读跟踪。outbox 和 hub/SSE 已经为这种异步 + 实时的事件模式做好准备。

### 建议的切入点

1. **会话实体化**——`conversations` 表（UUID PK、type enum 1:1/group、title、avatar、created_at） + `conversation_members` 表（conversation_id、uid、role、joined_at、muted_until）
2. **群组消息路由**——`messages.conversation_id` 替代 `to_uid` 作为归属，`from_uid` 保留作为发送者
3. **通知适配**——`message.received` 事件按 conversation member 列表广播，而不是单收件人
4. **SSE 实时推送**——现有 hub 已支持多订阅者，按 conversation_id 订阅即可

### 边界 / 性能

- 大群（10K+ 成员）的消息广播需要避免 fan-out N 条 notification 行——应使用一次写入 + 读时过滤
- 成员离开/被移除后的历史消息可见性策略
- 群组管理操作的审计事件（创建、改名、成员变更）

---

## 方向二：用户级别封禁与内容审核 Workflow

### 当前状态

`domain/admin.go` 定义了 `PermUserBan = "user.ban"` 和 `PermContentModerate = "content.moderate"`，角色表也 seed 了 `moderator` 和 `admin`。但实际执行：

- **评论审核**：存在 `comment_reports` + `HideComment`/`UnhideComment` + `ResolveReport`——基本完整
- **文件审核/消息审核**：完全缺失——无法举报一个文件或一条消息
- **用户封禁**：`user.ban` permission 定义在 domain 层，但**没有封禁表**、**没有封禁中间件**、**没有封禁 API**——封禁权限是一个空壳
- **自动审核钩子**：不存在——无法接入敏感词过滤、图片审核 API、或 ML 分类器

### 为什么需要

- **生产安全基线**：任何允许用户上传内容（文件、消息、评论）的服务都需要封禁能力和内容审核工作流。没有封禁，对抗性用户无法被有效阻止；没有文件/消息举报渠道，滥用只能通过评论举报处理。
- **合规风险**：缺少封禁能力 = 无法响应法律要求的用户停用。
- **架构协同**：outbox 已经可以驱动异步审核管道（上传后 `file.created` 事件触发审核），`audit_events` 可以记录每一次审核操作。基础设施已经到位，只差业务逻辑。

### 建议的切入点

1. **`user_bans` 表**（uid、banned_by、reason、created_at、expires_at——支持临时封禁）
2. **封禁中间件**——在所有写操作和 SSE 连接之前检查 `user_bans`，被封禁用户返回 403
3. **封禁管理 API**——`POST /v1/admin/bans`（授予/撤销，需 `user.ban` permission）
4. **文件举报**——`file_reports` 表 + `ReportFile`/`HideFile`/`UnhideFile` API
5. **消息举报**——`message_reports` 表 + `ReportMessage` API（消息举报后自动审查发送者）
6. **审核仪表盘端点**——聚合所有待处理的举报（评论 + 文件 + 消息），按时间/类型/举报者排序

### 边界情况

- 封禁遇到拥有大量内容的用户——是否需要批量内容隐藏？
- 临时封禁到期自动解封——`expires_at IS NOT NULL AND expires_at <= now()` 在中间件中自动失效
- 封禁通知——被封禁用户应在下次请求时得到明确反馈，而非静默限流

---

## 方向三：用户数据导出与账户删除级联

### 当前状态

系统中存储的数据按用户归属：`owner_uid`（文件/评论）或 `from_uid`/`to_uid`（消息）。软删除已全面实现。**但没有一个端点允许用户导出自己的全部数据**，也没有**账户删除 API** 来级联清理或匿名化该用户的所有记录。

### 为什么需要

- **合规要求**：GDPR（第 15 条——访问权、第 17 条——被遗忘权）、CCPA（删除权）、及其他隐私法规要求服务商提供数据导出和账户删除能力。缺失这是生产上线前的红线。
- **产品信任**：用户无法拿走自己的数据会显著降低使用信心。
- **技术复杂度**：导出和删除涉及跨表级联（文件→storage blob、消息→inbox/outbox 双方、评论→回复树→点赞→举报→通知→审计），且需要在写负载下保持一致性。越早设计越容易，数据库已有软删除和 outbox，实现起来成本可控。

### 建议的切入点

1. **`POST /v1/account/export`**——异步任务：收集 uid 相关的所有 records（文件元数据、消息、评论、通知、审计事件），打包为 JSON/CSV，生成预签名下载 URL（复用现有 `PresignGet`）。使用 outbox 或后台 goroutine 避免阻塞请求。
2. **`DELETE /v1/account`**——账户删除级联：
   - 软删除所有评论（保留内容但 `owner_uid` 匿名化或标记 deleted）
   - 软删除所有消息（保留对话连续性但发送者/接收者标记 deleted）
   - 软删除所有文件（触发 storage_refs 递减 + GC 后续清理 blob）
   - 清理通知、偏好、block 关系、role 分配
   - 记录审计事件
3. **可配置的保留策略**——保留某些记录（如审计日志、已发送消息的发送方信息）一段时间后再彻底清理，而非立即删除。

### 边界情况

- 跨表级联的一致性——需要一个事务或 outbox 确保删除操作的原子性
- 大用户导出超时——使用预签名 URL + 异步生成，而非同步流式
- 消息的"双向归属"——`from_uid` 和 `to_uid` 各有一份权益；一方删除后另一方应仍能看到消息（只是对方显示"已注销用户"）
- 文件的分享引用——删除文件时应检查是否有其他用户通过文件分享引用它，而非级联删除共享方的访问

---

## 方向四：分布式实时架构——SSE 水平扩展与连接管理

### 当前状态

SSE 实时推送基于单进程的 `hub.Hub`（`sync.Map` + 每个 uid 的 channel 池）。`replayMissed` 支持断线续连。当前架构对单实例部署完全够用。**但扩展到多实例时存在三个问题**：

1. **推送盲区**——通知由 outbox relay 在同进程内派发 → hub 推送。如果用户连接的实例 B 不处理该用户的 outbox 事件，用户就收不到推送。
2. **连接计数 / 通道泄漏**——没有 `/v1/admin/connections` 查看活跃 SSE 连接数，也没有机制检测僵尸连接（客户端优雅关闭时未必调用 unsubscribe）。
3. **广播放大**——群聊场景（方向一）下，一个消息需要推送给 N 个在线用户，单进程到多进程后需要一条共享 pub/sub 总线。

### 为什么需要

- **水平扩展前提**：任何需要跨实例通信的实时功能（群聊推送、多设备通知同步、管理员广播）都依赖这一基础设施。这是方向一（群聊）的架构前置。
- **运维可观测性**：不知道当前有多少活跃 SSE 连接、分布在哪些 uid、连接持续时间——就无法诊断推送延迟或泄漏。
- **成本控制**：僵尸连接占用 goroutine + channel 内存，累积上千个会显著增加实例内存压力。

### 建议的切入点

1. **轻量级跨实例总线**——引入 Redis Pub/Sub 或 PG `LISTEN/NOTIFY` 作为跨实例事件通道。不需要全功能消息队列（NSQ/Kafka），因为延迟敏感（SSE 推送）且吞吐中等。Hub 在本地监听一个订阅频道，收到消息后向本地连接推送。
2. **`/v1/admin/connections` 端点**——暴露当前实例的活跃 SSE 连接信息（uid、subscriber ID、连接时长、已推送事件数），方便 debug 和容量规划。
3. **连接健康探测**——在 SSE 心跳之外，增加应用层 ping/pong 机制检测僵尸连接并清理。
4. **hub 的优雅降级**——当跨实例总线不可用时，降级为"仅本实例推送 + 客户端轮询兜底"，避免硬依赖。

### 边界情况

- last-event-id 跨实例恢复——用户断线重连后可能连接到不同实例，replayMissed 查询数据库始终正确，但应确保 replay 不重复推送已通过总线投递的事件
- 实例缩容时的连接迁移——如果 SIGTERM 前主动 drain 连接，客户端可以在断开后马上重连到存活实例 → last-event-id 保障不丢事件

---

## 方向五：搜索排序、高亮与高级查询

### 当前状态

搜索基于 PostgreSQL `tsvector` + GIN 索引实现，支持对 messages/comments/files 全文检索。但体验非常基础：

- **无排序**——`SearchMessages`/`SearchComments`/`SearchFiles` 使用 `tsquery @@ tsvector` 过滤，但结果按 `created_at DESC` 排序，而非按**相关性**（`ts_rank`）。这意味着搜索"hello"时结果顺序与键入查询词无关。
- **无关键词高亮**——用户不知道匹配在文本的哪个位置。
- **无分页**——`SearchRequest` 只有 `limit`，无 `cursor` 或 `offset`。大数据集下结果截断不透明。
- **无词组搜索 / 前缀匹配**——`plainto_tsquery`/`websearch_to_tsquery` 没有使用，不支持 `"exact phrase"` 或 `prefix*` 语法。
- **scope 固定**——搜索范围限制为用户"拥有访问权限"的实体（自己 & 公开的），但无法精细指定：搜索某段日期范围、某人的消息、或某标签。

### 为什么需要

- **用户体验差距**：相关性排序和高亮是现代搜索的基线期望。无排序的"全文搜索"实际价值有限——用户需要翻页找想要的。
- **架构可演进性**：ts_rank 和 GIN 索引已经就位（`migrations/000016_fts_indexes`），sql 只需要调整 `ORDER BY` 子句。投入产出比极高。
- **为 AI 检索打基础**：后续引入 embedding 时（设计文档 Phase 2），一个干净的高级查询层（query parser + 排序器 + 分页器）让混合搜索的接入成本更低。

### 建议的切入点

1. **相关性排序**——在 repository 层增加 `ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC` 选项，与现有的 `created_at DESC` 并存（由 service 层按是否有 query 切换）。
2. **关键词高亮**——`ts_headline('simple', content, query)` 在 SQL 层返回匹配片段的 `<b>...</b>` 标签。
3. **游标分页**——在 `SearchHit` 中嵌入 `next_cursor`，支持 `search_after` 语义（基于 `(rank, id)` 或 `(created_at, id)` 的游标）。
4. **查询语法增强**——使用 `websearch_to_tsquery`（PostgreSQL 14+）支持引号词组和 `-` 排除词。
5. **按日期/作者筛选**——`SearchRequest` 增加 `sender_uid`、`created_since`、`created_until` 字段。

### 性能考虑

- GIN 索引对文本排名（ts_rank）需要 `tsvector` 参与排序，GIN 索引本身不直接提供排序顺序，需要额外的 Sort 节点——数据集 > 10K 行时可能需要覆盖索引或物化视图
- 对于混合 `ts_rank` + `created_at` 排序可以考虑 `experimental_strategies`（CASE 权重）
- 如果搜索负载高，可以考虑在 `search_vector` 列上建 `GIN` 的 `tsvector_ops` 并关闭 `fastupdate` 以减少插入抖动

---

## 边界情况一览（现有缺口）

以下是与方向正交但值得记录的边界和异常路径，散布在现有代码中尚未专门处理：

| # | 边界/缺口 | 影响面 | 说明 |
|---|-----------|--------|------|
| 1 | **storage_refs 并发竞态** | 文件 dedup 正确性 | storage_refs 引用计数大幅降低了竞态窗口，但极端并发下 Create + Delete 仍可能让 blob 被 GC 误删。现在尚无事务级锁保护。 |
| 2 | **outbox 死信不可见** | 运维可观测性 | 死信事件写入 outbox_events 后无告警、无管理端点、无法重新入队。运维人员无法发现或重试。 |
| 3 | **SSO 完全不可用时的新用户** | SSO 降级策略 | serve-stale 仅在 SSO 故障时返回**已缓存**的 token 结果。从未登录过的用户（或 token 已彻底过期）在 SSO 宕机期间完全无法使用服务——这是设计的。但可以记录成已知限制，并考虑是否提供离线白名单机制。 |
| 4 | **SSE 大规模重连风暴** | 实时推送可靠性 | N 个客户端同时断线重连 → 每个客户端执行 replayMissed 查询。当 N≥1000 时数据库负载尖刺。可考虑共享缓存或 dedup。 |
| 5 | **通知折叠的副作用** | 通知准确性 | 同一用户在同一 collapse window 内多次触发同一事件类型，折叠会把它们合并为一条 `collapsed_count > 1` 的通知。但用户标记其中一条为已读时，会折叠整个组——这可能不是用户意图。 |
| 6 | **block 驱逐顺序** | Block 服务正确性 | 用户的 block 列表用于屏蔽消息和通知，但 block 是**追加写入**的（无事务包裹）。如果用户同时 blocking A 和 unblocking B，可能出现短暂的不一致中间态。 |
| 7 | **大文件上传的 MIME 校验窗口** | 安全 | MIME 类型由客户端宣称（`file` handler 读取 `Content-Type`），服务端不做 `mime sniff`。恶意客户端可以宣称 `image/png` 但上传 `script.exe`。 |
| 8 | **presign upload 的孤儿 blob** | 存储成本 | `PresignUpload` 分配 storage_key 但只有 `CompleteUpload` 才写行。如果客户端拿到 URL 后不 PUT 也不 complete，storage_key 成为匿名字段但无数据占用。现有的 GC `min_orphan_age` 可以通过时间戳近似清理，但无法区分"已分配但未上传"和"上传失败"。 |

---

## 性能优化机会

| # | 项目 | 当前 | 优化方向 |
|---|------|------|---------|
| 1 | **通知列表查询** | `idx_notifications_recipient_created` 覆盖了基础排序，但 `ListUnread` + 游标分页需要执行 plan 检查是否触发排序 | 确认 PG 使用了 `(recipient_uid, read_at IS NULL, created_at DESC)` 理想索引路径 |
| 2 | **SSE 断线续连查询** | `LastSince` 每次重连扫全表匹配 `recipient_uid` + `created_at > since` | 加 `(recipient_uid, created_at DESC)` 索引即可（可能已有——检查 `idx_notifications_recipient_created` 是否覆盖） |
| 3 | **搜索结果的 sort 开销** | GIN 索引过滤快，但 `ORDER BY created_at DESC` 对于大结果集需要显式 Sort | 考虑 `CREATE INDEX ON messages(created_at DESC) WHERE search_vector @@ ...` 或使用 `ts_rank` 排名后 LIMIT，避免全量排序 |
| 4 | **用户解析缓存跨请求** | `Inbox` / `ListComments` 每次请求独立解析用户（user_cache 在单请求内 batch，但不同请求间复用有限） | 考虑在 response 层做 `UserCache.GetMany` 的跨请求亲和性，或利用 HTTP 304 Not Modified |
| 5 | **outbox relay 吞吐** | relay 单 goroutine 轮询，`FetchUnpublished` 步长固定（默认 ?） | 考虑变长 batch（根据 pending 积压量自适应调整）或并行 drain（不同类型事件并行） |
| 6 | **storage GC 全量扫描** | GC 每轮扫描全表找 soft-deleted + orphaned | 加 `idx_files_deleted_at` 部分索引 `WHERE deleted_at IS NOT NULL`，避免扫全表 |

---

## 建议执行顺序

| 优先级 | 方向 | 前置依赖 | 价值 | 估算 |
|--------|------|---------|------|------|
| **P0** | 用户数据导出 + 账户删除级联（方向三） | 无 | 合规红线 | 中 |
| **P0** | 用户封禁 + 审核完整化（方向二） | 角色系统已就绪 | 生产安全基线 | 中 |
| **P1** | 搜索排序 + 高亮（方向五） | `migrations/000016` 已就位 | 高用户体验杠杆 | 低-中 |
| **P1** | 群聊与会话管理（方向一） | 方向四（跨实例总线）非硬依赖（群聊可在单实例先落地） | 大产品增量 | 高 |
| **P2** | SSE 水平扩展（方向四） | 群聊/P0-1 需求推动 | 架构演进 | 高 |

### 快速短期修补（可选）

- 添加 outbox 死信管理 API（列出死信 + 重新入队）
- `user_bans` 表 + 基础封禁中间件（不实现管理端点，仅允许手动 SQL + 中间件自动拒绝被封禁用户的写操作）
- 搜索结果的 `ts_headline` 高亮——只需要改 repository 层 SQL 和 proto 字段
- 添加 `idx_files_deleted_at` 部分索引优化 GC 扫表
