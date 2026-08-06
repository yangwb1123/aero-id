# aero-id 扩展方向分析报告

> **作者**：架构师 / PM review  
> **日期**：2026-07-10  
> **范围**：全代码库全局扫描（`internal/`、`migrations/`、`configs/`、`cmd/`）  
> **方法**：扫描现有功能后，识别架构层面与产品层面的高价值扩展点、边界情况（edge case）以及性能瓶颈。仅分析，不写入代码。

---

## 当前功能全景（简短摘要）

在提出扩展之前，先回顾项目当前已交付的能力——以下全部已完成：

| 领域 | 已实现 |
|---|---|
| **文件** | 上传（直接 + S3 presigned）、下载（流式 + 预签名 URL + HMAC 匿名）、内容去重（sha256）、MIME 白名单、大小上限、显式共享（授权/撤销）、元数据 PATCH、软删除 + GC 回收（过期行清理 + 孤儿 blob 扫描）、存储引用计数 |
| **消息** | 1:1 私信、发送/接收/对话视图、已读/未读、`mark-all-read` 按对话、系统消息、编辑、软删除、表情反应（like/unlike） |
| **评论** | 两层嵌套（楼 + 楼中楼）、CRUD、编辑、软删除、点赞、举报/审核（状态机：举报→resolve/dismiss→hide/unhide） |
| **用户屏蔽** | 方向性屏蔽（blocker→blocked）、拦截消息发送与通知分发、屏蔽列表分页 |
| **通知** | Outbox 驱动：`message.received`、`message.read_receipt`、`comment.reply`；分发器含屏蔽过滤、偏好过滤（静音时段）、速率限制、折叠去重、SSE 实时推送（含 `last_event_id` 断线重播） |
| **SSO 集成** | gRPC 客户端 + 双缓存（token/user）TTL + serve-stale 降级、circuit breaker、singleflight、带退避的重试、keepalive、TLS/mTLS、证书固定 |
| **事务性 Outbox** | 写路径事件原子落库、Relay 轮询递送、指数退避、死信队列、Webhook 签名转发 |
| **全文本搜索** | PostgreSQL tsvector + GIN 索引 + CJK 回落 ILIKE；跨消息/评论/文件 |
| **审计日志** | 敏感操作记录（文件共享、屏蔽、举报处理、消息删除） |
| **双协议** | HTTP REST + gRPC 全端点对等覆盖（38 个 RPC），共享 Service 层 |
| **运维** | OTel 链路追踪、RED 指标（速率/错误/延迟）、Per-dependency readiness、限流（per-actor token bucket）、幂等键、迁移 CLI、优雅关机 |

这是一个**极其成熟**的 MVP+ 代码库，大多数基础能力已经到位。因此以下扩展方向不是"补基础功能"，而是**为下一阶段增长和规模化设计的战略投资**。

---

## 扩展方向一：水平扩展与分布式状态

### 为什么需要

当前架构中存在三处实例本地（in-memory）状态，使得无法启动第二个副本：

1. **SSE Hub** — `internal/hub/hub.go` 的 `Hub` 使用 `sync.Map` 维护 uid → channel 映射。如果用户连到实例 A，通知事件经由 outbox relay 在实例 A 上通过 dispatcher 推送，那么连到实例 B 的同用户收不到实时推送。
2. **Outbox relay** — `internal/outbox/relay.go` 每个实例启动独立的轮询 goroutine。多实例下所有实例同时轮询同一张 `outbox_events` 表，造成冗余处理与重复推送。
3. **Rate limiter** — `internal/pkg/ratelimit/ratelimit.go` 使用内存 token bucket。实例间不共享限流状态，恶意用户可在 N 个实例上各获得满额配额，绕开全局限制。

这意味着当前无法在负载均衡器后部署超过一个 aero-id 实例——这是生产就绪的首要架构瓶颈。

### 边界情况与风险

- Redis 依赖的引入增加了部署复杂度和延迟（每个请求增加一次 RTT）。不能简单地附加 Redis——需要兜底降级（Redis 宕机时回落本地模式）。
- SSE 跨实例推送需要 pub/sub（Redis Pub/Sub 或类似），连接亲和性（sticky session）可作为短期方案但不推荐。
- Outbox relay 的分布式互斥：引入 leader election（pg_advisory_lock 或外部协调）确保同一时刻仅一个实例负责 drain。

### 建议方向

| 步骤 | 内容 | 估算 |
|---|---|---|
| 1 | **Sticky session fallback + 通知混合推送**：短期方案，允许部署多实例但 SSO 不做粘性保证。通知同时推送 to 所有实例的本地 SSE hub（通过 Redis Pub/Sub 广播）。 | 中 |
| 2 | **分布式限流**：将 `ratelimit.Limiter` 改为 sliding-window + Redis（或可插拔后端），提供降级路径。 | 中 |
| 3 | **Outbox leader election**：使用 PostgreSQL 咨询锁或 `outbox_events` 表的租约行实现单实例轮询。 | 中 |

---

## 扩展方向二：文件后处理管线（转码、缩略图、内容分析）

### 为什么需要

aero-id 定义自己的**文件上传能力是核心资产**，但上传后管线完全空白：

- 上传一张 12MP 照片后，客户端只能下载原图（~10MB），没有缩略图、没有 WebP 转码、没有裁切版本。
- 用户上传一个 PDF，除了存储之外没有任何内容提取（文本检索靠 `filename` + `meta`，没有 PDF 全文索引）。
- 没有病毒扫描——一个恶意文件一旦上传即可被分享。
- 没有图片/视频审核——不支持自动 NSFW 检测或敏感内容标记。

在 `ROADMAP.md` 的 Phase 1 规划中，outbox + 异步 worker 被列为下一个里程碑。当前的 outbox 基础设施已经做好——欠缺的是 **worker 定义和任务编排**。

### 现有基础

| 存在什么 | 缺失什么 |
|---|---|
| `outbox` 事务性事件写入 + relay + backoff + 死信 | 没有 `worker` 抽象层（将 outbox 事件映射为可执行的异步任务） |
| `storage` 接口（FileStorage Put/Get/Delete） | 没有临时工作目录 / 流式转换管线 |
| `file.Checksum`（sha256）存于 `files` 表 | 没有 thumbhash / blurhash、没有 embedding |
| MIME 类型校验 | 没有基于 MIME 的路由到不同处理器 |

### 建议方向

| 步骤 | 内容 | 估算 |
|---|---|---|
| 1 | **异步任务定义**：创建 `internal/processor/` 包，定义 `Task` 接口（`Process(ctx, *domain.File) error`），按 MIME 类型路由（图片→缩略图、文档→文本提取、视频→关键帧）。每个 task 通过 outbox 事件触发。 | 中 |
| 2 | **缩略图生成**：集成 `imaging` / `libvips` 或类似库，生成固定尺寸缩略图，存入 `files` 表关联的 `file_derivatives`（新迁移），并在文件元数据中暴露。 | 中 |
| 3 | **病毒扫描 / 内容审核**：可选集成 ClamAV 或外部审核 API，结果标记到文件状态字段中（`files.status`）。 | 低-中 |
| 4 | **全文内容索引**：从 PDF/Office 文档中提取文本，写入 `files.extracted_text` 列（已有 FTS 索引可复用），使文件内容可检索。 | 中-高 |

### 边界情况

- **大文件**：缩略图生成需要下载整个 blob，对超大文件（> 几百 MB）需要超时和内存保护。
- **失败处理**：转码失败不应阻止文件原始访问——失败应记录但文件状态保持"已上传"。
- **认证与授权**：processing pipeline 需要 actor context（但不来自 HTTP 请求）——需要系统 actor 传播。

---

## 扩展方向三：多人群组会话（Group Conversations / Channels）

### 为什么需要

当前 `messages` 模型的参与者严格限制为 2 人（`from_uid` + `to_uid`）。这是最显著的产品功能缺口：

- 用户无法创建团队频道、项目群聊或多人讨论组。
- 所有消息的发送者就是 `from_uid`，没有"频道匿名发言"或"频道广播"的概念。
- 通知系统（`message.received`）基于 `to_uid` 触发——群聊下需要 N 个通知行，当前模型不支持。

### 现有基础

| 存在什么 | 缺失什么 |
|---|---|
| `messages` 表 + CRUD 完整 | 没有 `conversations` / `groups` / `memberships` 表 |
| `outbox` + notification dispatcher | 群聊消息通知需要广播到 N 个成员 |
| Block checker | 群聊中 A 屏蔽 B 后 A 应当看不到 B 在群里的消息 |
| 消息 reactions | 群聊 reactions 同理 |
| `file_shares` 表（显式授权） | 可以借鉴其模式（资源 + grantee）为群组成员建 membership |

### 建议方向

| 步骤 | 内容 | 估算 |
|---|---|---|
| 1 | **群组数据模型**：`conversations`（id, type, title, created_at） + `conversation_members`（conversation_id, member_uid, role, joined_at） + `conversation_messages`（宽表或引用现有 messages 表的结构） | 高 |
| 2 | **发送 API 扩展**：`POST /v1/conversations/:id/messages`（替代当前 1:1 `Send`），服务层自动向所有成员分发通知。 | 中 |
| 3 | **成员管理**：邀请、离开、踢出、角色（管理员/普通成员）。 | 中 |
| 4 | **通知适配**：群聊消息的通知应去重（相同消息多个收件人）并支持 `@mention` 标记。 | 中 |

### 数据迁移策略

由于现有 `messages` 表不支持群聊，建议新建 `conversation_messages` 独立表，1:1 消息保持现有模式不变。这避免了破坏性迁移。

### 边界情况

- 群聊中 A 屏蔽 B → A 看不到 B 的消息，但 B 看到 A 的。需要读写路径都检查。
- 成员离开后能否访问历史消息？（典型设计：能看到离开前的消息，不能发新消息）
- 群聊文件共享：成员自动获得群聊中分享的文件的读权限。

---

## 扩展方向四：文件版本控制与修订历史

### 为什么需要

当前文件是不可变的——上传后 content 不能更改。`PATCH /files/:id` 只改元数据（name/visibility/meta）。这对协作场景是个硬限制：

- 用户误操作上传错误版本，只能删除再上传，丢失原来的文件 ID 和分享关系。
- 无法实现"另存为新版本"（保留历史版本，显示最新）。
- 分享链接在内容更新后仍然有效，但指向旧 blob。

### 现有基础

| 存在什么 | 缺失什么 |
|---|---|
| `files` 表，`storage_key` 指向唯一 blob | 没有 `version_number`、`previous_version_id`、文件级修订链 |
| `storage_refs` 引用计数 | 如果同一 blob 被多个版本引用，引用计数可以复用 |
| `checksum` 去重 | 版本创建时可以复用已有 blob |
| 软删除 + GC | 版本需要自己的 GC 策略（保留最后 N 个版本或 M 天） |

### 建议方向

| 步骤 | 内容 | 估算 |
|---|---|---|
| 1 | **扩展数据模型**：`files` 表新增 `version_number`（递增整数）和 `replaces_file_id`（指向被替换版本）；或者新建 `file_versions` 表。 | 中 |
| 2 | **上传新版本**：`POST /files/:id/versions` 上传新 blob，创建新的文件行（same owner_uid, same name, version_number+1, previous_version_id = old row）。 | 中 |
| 3 | **版本列举与回退**：`GET /files/:id/versions` → 修订列表；`POST /files/:id/versions/:vid/restore` → 创建一个内容来自旧版本的新版本。 | 低-中 |
| 4 | **版本保留策略**：GC 增强，按版本数量或时间保留，超过阈值时自动清理旧版本。 | 低 |

### 边界情况

- 显式共享关系：共享是针对"文件实体"还是"文件版本"？（建议：共享作用于文件根，新版本自动继承共享权限。）
- 评论与消息引用：某条消息引用了 `file_id = X`，如果 X 被新版本替代，消息引用的 blob ID 不变——需要决定是否指向最新版本。
- 存储空间放大：频繁更新大文件会迅速消耗存储。需要清晰地告知用户或启用配额。

---

## 扩展方向五：滥用防护与信誉系统（Anti-Abuse & Trust Scoring）

### 为什么需要

aero-id 提供了**消息、评论、文件分享、举报**等开放的社交原语，但缺少分层级的滥用防护：

1. **当前限流是全局均一**的——新用户和老用户共享相同的 `rps/burst`。恶意注册可以低成本刷屏。
2. **没有内容级别的滥用检测**——重复发送相同消息（spam）不做检测，虚假举报不做惩罚，批量注册后发送垃圾消息不触发警报。
3. **没有用户信誉**——没法给举报系统"权重"（一个被 20 人举报的用户 vs. 一个新用户的举报，应区别对待）。
4. **当前举报是 author-scoped**——评论作者可以 resolve/dismiss 举报，但没有全局管理员视角的举报队列和级联封禁。

### 现有基础

| 存在什么 | 缺失什么 |
|---|---|
| 全局 per-actor token bucket 限流 | 无按端点、按角色、按用户年龄的差异化限流 |
| 评论举报（`comment_reports` 表 + 状态机） | 举报者信誉、举报频率限制、虚假举报惩罚 |
| 用户屏蔽（block） | 自动屏蔽（大规模举报+验证后自动屏蔽） |
| 管理员角色（`admin_roles` + `permissions`） | 管理控制台、批量操作、用户封禁/解封 API |
| `idempotency` 幂等键 | 但不是用来防 spam 的 |

### 建议方向

| 步骤 | 内容 | 估算 |
|---|---|---|
| 1 | **分层限流**：在全局限流之上增加 per-endpoint 配额 + per-user-age 阶梯（新用户更严格）。限流命中后返回可读的错误码 + `Retry-After` 头部。 | 中 |
| 2 | **用户信誉（trust score）**：新增 `user_reputation` 表或字段，根据行为（举报 resolve 数、被举报数、账号年龄、消息被 block 数）计算分数，影响限流阈值、举报权重。 | 高 |
| 3 | **举报队列管控台**：管理员视角的全局举报队列，支持批量处理、按状态/时间/频率排序、自动封禁触发器（同用户被举报 N 次且多数 resolve → 自动临时屏蔽）。 | 中 |
| 4 | **内容级反垃圾**：对消息/评论做重复内容检测（相同文本短时间内多次发送 = spam）、对文件做 hash 级黑名单。这些可以作为 processor 管线的 task 集成（方向二）。 | 中-高 |

### 边界情况

- **误封**：所有自动动作必须有逆向路径（申诉 → 人工复审 → 恢复信誉分）。
- **隐私**：信誉系统实现时不能暴露内部分数给用户（导致 gaming）。
- **冷启动**：新系统的信誉分无法覆盖老用户——需要批量初始化（默认中位分，根据历史数据微调）。

---

## 总结与优先级建议

| # | 方向 | 产品价值 | 架构价值 | 风险降低 | 估算工作量 |
|---|------|---------|---------|---------|-----------|
| 1 | 水平扩展与分布式状态 | ★★★ | ★★★★★ | ★★★★★ | 中 |
| 2 | 文件后处理管线 | ★★★★★ | ★★★★ | ★★★ | 中-高 |
| 3 | 多人群组会话 | ★★★★★ | ★★★ | ★★ | 高 |
| 4 | 文件版本控制 | ★★★★ | ★★★ | ★★ | 中 |
| 5 | 滥用防护与信誉系统 | ★★★★ | ★★★ | ★★★★★ | 高 |

**建议启动顺序**：

1. **先做方向一（水平扩展）**——这是阻碍生产部署的硬约束，不做就无法 scale。
2. **然后做方向二（后处理管线）**——复用现有 outbox 基础设施，直接提升文件核心能力的价值。
3. **方向三（群聊）和方向四（文件版本）** 可并行，两者数据模型互不冲突。
4. **方向五（滥用防护）** 伴随整个生命周期持续迭代——不必一次做完，但新用户注册时立即启用分层限流（步骤 1）可以作为高优先级 quick win 单独拉出。
