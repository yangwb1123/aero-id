# aero-id — Post-ROADMAP 高价值扩展方向分析

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全局代码扫描（`internal/` 全部 ~40 个包、22 份迁移、`cmd/`、`configs/`、`api/proto/`），
>   对照已交付的 38 gRPC RPC + 全 HTTP 对等 + 生产加固功能，聚焦于**代码级证据驱动**的扩展方向。
> **前置条件**: `feat/roadmap` 已合并；所有 ROADMAP 方向、P0/P1/P2、后 ROADMAP 加固轮全部交付。
> **原则**: 只分析真正高价值但尚未实现的方向；不重复已交付或已覆盖的内容。
> **无代码变更**

---

## 当前代码能力概览（与本文相关）

| 领域 | 已实现状态 |
|------|-----------|
| **消息** | 1-to-1 直聊、Reactions、编辑/软删、已读回执、全文本搜索、系统消息 |
| **文件** | 双后端（local/S3）、流式上传、预签名直传、内容去重、MIME 校验、GC 扫描 |
| **评论** | 两层嵌套、Like/Unlike、举报/审核/隐藏、编辑 |
| **通知** | Outbox 驱动、偏好设置（启禁 + 静默时段 + 时区）、合并去重、SSE 实时推送 |
| **安全** | 用户阻塞（双向抑制消息+通知）、权限角色模型（admin/moderator）、Rate Limiting |
| **集成** | 全局 outbox webhook（HMAC 签名）、gRPC 全传输对等 |
| **运维** | RED 指标、OTel 追踪、断路器、Serve-stale SSO 缓存、存储 GC、迁移 CLI |
| **基础设施** | Rate limiter: 内存单实例 · SSO 缓存: 内存单实例 · SSE Hub: 内存单实例 |

---

## 方向一：多人群组会话（Group Conversations）

### 为什么需要

当前消息模型是 **strictly 1-to-1**：`messages` 表仅有 `from_uid` 和 `to_uid`，没有任何群组/频道的概念。
对于一个用户数据服务而言，这是最显著的产品缺口。从架构角度，1-to-1 向多方的跃迁需要**重新设计
消息路由层**，且不能破坏现有的 1-to-1 语义。

### 代码级证据

- `internal/domain/message.go` / `internal/service/service.go` — `SendMessageInput` 只有 `ToUID string`，
  不支持群组标识。`Message` 实体无 `ConversationID` 字段。
- `internal/service/message.go` — `Inbox()` / `Conversation()` 均基于 `(from_uid, to_uid)` 二元组查询，
  无 `conversations` 表联接。
- `api/proto/aero_id/v1/aero_id.proto` — `SendMessageRequest` 只有 `to_uid`；
  `InboxRequest` / `ConversationRequest` 无 `conversation_id` 参数。
- `migrations/` — 无任何 `conversations` / `conversation_members` 表迁移。

### 设计要点

1. **引入 `conversations` 表** — 聚合根，持有类型（pair/group）、标题、头像、创建者、`last_message_at`。
2. **引入 `conversation_members` 表** — 成员关系（用户 → 会话），含角色（owner/admin/member）、加入时间、离开时间。
3. **消息模型进化** — `Message` 获得 `conversation_id`（可选；1-to-1 向后兼容）。路由层根据会话类型决定分发逻辑。
4. **现有 1-to-1 就地迁移** — 每对已有通信的用户自动获得一个 pair conversation（在首次查询时惰性创建，
   或迁移脚本批量创建），所有现存消息通过 `conversation_id` 归组。
5. **通知扩展** — `message.received` 通知需要携带会话上下文；可选的 `@mention` 通知。

### 影响评估

| 维度 | 评估 |
|------|------|
| 产品价值 | ★★★★★ — 直接解锁群聊场景，是 IM 类服务的核心能力 |
| 架构影响 | ★★★★☆ — 涉及消息层重构、新增迁移、通知层适配 |
| 风险 | 现有 1-to-1 消息的向后兼容；pair conversation 的惰性创建策略 |
| 估算 | 高（2-3 周），主要是消息路由层重构 + 迁移 + 适配通知/搜索 |

---

## 方向二：资源级联生命周期（Resource Lifecycle Cascading）

### 为什么需要

当前系统广泛使用**软删除**（`gorm.DeletedAt`），但当一个根实体被删除时，其从属资源没有明确的
可见性/生命周期策略。这既是一个数据一致性问题，也是一个用户可见的 UX 问题：删除一条消息后，
该消息的 Reactions 仍然存在（作为孤立行）；删除一个文件后，其评论仍然显示（但无法访问目标）；
分享授权在文件删除后仍保留在 `file_shares` 表中。

### 代码级证据

- `internal/message/service.go` — `Delete()` 只设置 `deleted_at`，**不清理** `message_reactions`。
  查询 `Get()` / `Inbox()` 时通过 `Where("deleted_at IS NULL")` 过滤，但 reactions 表无对应过滤。
- `internal/comment/service.go` — 评论删除后 `like_count` / `reply_count` **不递减**。
  评论的 `status` 设为 `CommentStatusDeleted`，但目标实体（文件/消息）删除后评论仍显示在 `List()` 中。
- `internal/file/service_crud.go` — 文件删除后，对应的 `file_shares` 行未清理（孤立授权）。
  文件的评论未被标记或隐藏。
- `internal/repository/comment.go` — `ListByTarget()` 仅按 `target_type + target_id` 查询，
  不验证目标实体是否存在或已被删除。
- `internal/repository/storage_ref.go` — `storage_refs` 表在硬删除/软删除时递减引用计数，
  但 `file_shares` 没有类似的引用清理机制。

### 设计要点

1. **定义级联策略** — 每个 1:N 关系标注一种策略：
   - `CASCADE_HIDE` — 父实体删除后，子实体对非管理员不可见（评论 → 文件/消息）
   - `CASCADE_CLEAN` — 父实体删除后，子关联行清理（reactions → 消息；shares → 文件）
   - `CASCADE_NOOP` — 不做处理（保留审计记录；由 GC 后期清理）
2. **实现方式** — 在 service 层 `Delete()` 方法中同步执行级联操作（通过 repository 接口），
   或通过 outbox 事件异步处理（对于大规模级联）。
3. **查询层适配** — `ListByTarget()` 在返回前检查目标实体状态；隐藏的评论在 `List()` 中跳过。
4. **GC 集成** — 对清理类级联，可在存储 GC sweep 中批量执行（与现有 sweeper 机制一致）。

### 影响评估

| 维度 | 评估 |
|------|------|
| 产品价值 | ★★★★☆ — 用户可见的数据一致性问题，影响信任度 |
| 架构影响 | ★★★☆☆ — 仅限于 service + repository 层，不涉及传输层变更 |
| 风险 | 低 — 现有数据不动；级联是增量策略，逐步覆盖即可 |
| 估算 | 中（1-2 周） |

---

## 方向三：可编程 Webhook 订阅平台（Programmable Webhook Platform）

### 为什么需要

当前 outbox webhook 是**全局静态配置**（`configs/config.yaml` → `outbox.webhook.url`），
只支持一个外部端点，且所有事件类型都发往同一 URL。对于平台型服务来说，外部集成是一次主要需求：
不同客户端/服务需要订阅不同的事件子集，并且拥有各自的回调 URL、签名密钥和重试策略。
将 outbox 从"固定出口"升级为"可编程事件总线"是 aero-id 从单体服务向平台演进的关键一步。

### 代码级证据

- `internal/outbox/webhook.go` — `WebhookHandler` 从单一 `WebhookConfig` 结构体读取配置，
  无多订阅者概念。`NewWebhookHandler(cfg, log)` 静态构造。
- `internal/outbox/chain.go` — `Chain` 支持多 handler 串联，但目前最多两个（dispatcher +
  一个 webhook handler）。
- `internal/app/app.go` — webhook 配置直接从 `cfg.Outbox.Webhook` 读取，
  无运行时添加/移除订阅者的机制。
- `internal/config/config.go` — `WebhookConfig` 是 YAML 静态结构，`url` 为空字符串时禁用。
- `api/proto/aero_id/v1/aero_id.proto` / HTTP routes — 没有任何 webhook 订阅管理的 API 端点。
- `migrations/` — 无 `webhook_subscriptions` 表。

### 设计要点

1. **引入 `webhook_subscriptions` 表** — 持有订阅者 ID、回调 URL、签名密钥、订阅的事件类型列表（
   `[]string`）、启用状态、重试策略等。
2. **引入 CRUD API** — HTTP + gRPC 端点：`POST /v1/webhooks`（注册）、`GET /v1/webhooks`（列表）、
   `PUT /v1/webhooks/:id`（更新）、`DELETE /v1/webhooks/:id`（删除）、`POST /v1/webhooks/:id/test`
   （发送测试事件）。
3. **运行时订阅管理器** — 取代静态 `WebhookHandler`，动态加载（或通过 chan 推送）订阅变更到 relay。
   每个订阅持有自己的 `rate limiter` / `retry budget` / `circuit breaker`。
4. **基于现有 outbox 机制** — 在 `Chain` 中插入 `SubscriptionDispatcher`（或替代 `WebhookHandler`），
   对每个事件遍历匹配的订阅并分发。已有 HMAC 签名、重试、dead-letter 等逻辑可直接复用。
5. **管理限流** — 每个订阅的 event delivery 需要有独立的限流（保护外部服务不被淹没），
   且有总量控制（保护 aero-id 自身 outbox relay 不被过多订阅拖慢）。

### 影响评估

| 维度 | 评估 |
|------|------|
| 产品价值 | ★★★★☆ — 直接解锁第三方集成场景，是平台化能力的基础 |
| 架构影响 | ★★★★☆ — 新增订阅管理层 + 持久化 + API；对 relay 路由有影响 |
| 风险 | 中 — 现有全局 webhook 配置需保持向后兼容（或视为"默认订阅"） |
| 估算 | 高（2-3 周），主要是订阅管理器 + 持久化 + CRUD API |

---

## 方向四：文件变换管道与缩略图生成（File Transformation Pipeline）

### 为什么需要

aero-id 定位为"数据 owned by accounts 的服务"，文件是核心资产之一。但目前系统仅存储原始文件
字节，不做任何**服务端变换**。对于一个存在 `MessageKindImage`（消息中包含图片）和文件上传的
服务而言，缺少缩略图意味着：列表页面需要加载原图（带宽浪费、加载慢）、不支持图片预览的常见 UI
模式、无法适配不同客户端（Web 需要 WebP、iOS 需要 HEIC）。从架构角度，变换管道的设计（同步 vs
异步、缓存策略、CDN 集成）是一个有深度的系统工程问题。

### 代码级证据

- `internal/file/service_upload.go` / `internal/file/service_crud.go` — `Create()` / `CompleteUpload()`
  仅存储原始数据，不做任何处理。无 image resize / thumbnail / format conversion 逻辑。
- `internal/domain/file.go` — `File` 实体无 `thumbnail_storage_key` 或 `derivatives` 字段。
- `internal/storage/storage.go` — `FileStorage` 接口只有 `Save` / `Get` / `Delete` / `PresignUpload`，
  无 `Transform` 或任何处理接口。
- `internal/handler/http/file.go` — `GET /v1/files/:id` 返回文件元数据，无缩略图 URL 字段。
  `GET /v1/files/:id/url` 返回原图 presigned URL。
- `api/proto/aero_id/v1/aero_id.proto` — `File` 消息无 `thumbnail_url` 字段。
- `internal/config/config.go` — 无任何图像处理相关的配置项。
- `migrations/` — 无 `file_thumbnails` 或 `file_derivatives` 表。

### 设计要点

1. **异步变换管道** — 文件上传后通过 outbox 发送 `file.uploaded` 事件，一个专门的变换 worker
   监听并处理：图片→生成多尺寸缩略图（WebP/AVIF）；视频→首帧截图（可选）。
2. **衍生文件管理** — 新增 `file_derivatives` 表（file_id, variant（如 "thumb_256"）, storage_key,
   mime_type, width, height, size）。或直接在 `files` 表加 `meta` 字段中记录衍生信息。
3. **访问路径扩展** — `GET /v1/files/:id/thumbnail?size=256` 返回缩略图；`File` API 响应中增加
   `thumbnails` 字段列出可用尺寸。
4. **缓存策略** — 变换结果可配置 CDN 缓存 TTL；缩略图的生成是幂等的，已有文件不需要重新生成。
5. **后端适配** — 本地存储模式使用本地图片处理库（如 `github.com/disintegration/imaging`）；
   S3 模式可对接 AWS Lambda + S3 事件触发，或使用 `libvips` 在 worker 内处理。

### 影响评估

| 维度 | 评估 |
|------|------|
| 产品价值 | ★★★★☆ — 直接改善文件/消息列表的加载体验，降低带宽消耗 |
| 架构影响 | ★★★☆☆ — 新增 worker + 衍生表 + API 扩展；不涉及核心路由变更 |
| 风险 | 低 — 变换是纯增量；原始文件路径不变，缩略图是附加的 |
| 估算 | 中（1-2 周），主要是 worker + 存储 + API 扩展 |

---

## 方向五：跨实例分布式基础设施层（Distributed Infrastructure Primitives）

### 为什么需要

当前所有**有状态的中间件组件**都是 in-memory + per-instance 的：

- Rate limiter（`internal/pkg/ratelimit/`）— 内存 token bucket，每个实例独立
- SSO Token/User cache（`internal/sso/token_cache.go`, `user_cache.go`）— 内存 TTL map
- SSE Hub（`internal/hub/hub.go`）— 内存 subscriber registry
- Idempotency store（`internal/idempotency/store.go`）— GORM / PostgreSQL（**这个是唯一不走内存的**）

在单实例部署时这不是问题。但一旦需要**水平扩展**（2+ 实例），每个实例的 rate limiter 独立计数、
SSO 缓存各自为政（miss 放大、SSO 负载翻倍）、SSE 推送无法跨实例路由（用户连接到实例 A 但通知
写入了实例 B）。这不是一个"将来考虑"的问题，而是水平扩展**必须解决**的架构瓶颈。

### 代码级证据

- `internal/pkg/ratelimit/ratelimit.go` — `Limiter` 使用 `sync.Map` 存 token buckets，
  无分布式后端选项。`Allow(key)` 只查本地内存。
- `internal/middleware/ratelimit.go` — `RateLimit()` 中间件不做任何分布式协调。
- `internal/sso/token_cache.go` — `TokenCache` 使用 `map[string]*cacheEntry` + `sync.RWMutex`，
  无 Redis 后备。`Get()` 只查本地；`staleTTL` 失效后必须回源 SSO。
- `internal/sso/user_cache.go` — 同上，`map[string]*cacheEntry`。
- `internal/hub/hub.go` — `Hub.subs` 是 `sync.Map`（`map[string]*uidSubs`），
  无法感知其他实例的 subscriber。Publish 只在当前实例生效。
- `internal/app/app.go` — 构造时没有传入 Redis 或其他共享存储地址；所有组件实例化时使用零值/默认值。
- `internal/config/config.go` — 无 `redis` / `shared_cache` / `distributed` 配置段。

### 设计要点

1. **提取抽象层** — 定义 `CacheBackend` 接口（`Get` / `Set` / `Delete` / `Incr`），
   当前实现为内存（保留作为退化/开发模式可用），新增 Redis 实现。
2. **Rate limiter → 分布式** — 改用滑动窗口的 Redis Sorted Set 或 `INCR` + `EXPIRE` 实现，
   确保跨实例的全局限额一致性（最终一致性即可，不需要强一致）。
3. **SSO Cache → Redis** — Token/user cache 后移 Redis，TTL + serve-stale 逻辑保持不变；
   冷启动时不再每个实例独立回源 SSO。
4. **SSE Hub → 消息总线** — Hub 注册到 Redis Pub/Sub 或独立的 NATS/RabbitMQ 通道：
   `Publish()` 将事件发到全局频道，每个实例的 subscriber 收到后匹配本地的 uid 订阅者。
   需要引入 subscriber 在全局频道中的唯一标识。
5. **优雅退化** — 所有使用 Redis 的组件在 Redis 不可用时降级为**独立的内存模式**
   （rate limiter 退化为不限制、cache 退化为 always-miss、SSE 退化为仅推送本地订阅者）。
   这与现有 serve-stale / breaker 策略一致。

### 影响评估

| 维度 | 评估 |
|------|------|
| 产品价值 | ★★★★☆ — 水平扩展是生产部署的硬性需求，直接影响可用性和成本 |
| 架构影响 | ★★★★★ — 涉及基础设施抽象层定义、每个有状态组件的改造 |
| 风险 | 高 — 分布式一致性边界复杂（尤其是 SSO cache 的 serve-stale 语义）；
     Redis 引入新的运维依赖 |
| 估算 | 高（3-4 周），各组件逐个迁移，带 feature flag 逐步上线 |

---

## 方向对比与建议顺序

| # | 方向 | 产品价值 | 架构影响 | 风险 | 估算 | 建议 |
|---|------|---------|---------|------|------|------|
| 1 | 多人群组会话 | ★★★★★ | ★★★★☆ | 中-高 | 2-3 周 | **P0** — 最显著的产品缺口 |
| 2 | 资源级联生命周期 | ★★★★☆ | ★★★☆☆ | 低 | 1-2 周 | **P1** — 低风险高收益数据一致性 |
| 3 | 可编程 Webhook 平台 | ★★★★☆ | ★★★★☆ | 中 | 2-3 周 | **P1** — 平台化基石，但需求场景明确 |
| 4 | 文件变换管道 | ★★★★☆ | ★★★☆☆ | 低 | 1-2 周 | **P2** — 增量优化，可后置 |
| 5 | 分布式基础设施 | ★★★★☆ | ★★★★★ | 高 | 3-4 周 | **P0** — 水平扩展前置条件，但风险高；
     建议**先做架构分析 + 原型验证**再全量实施 |

### 推荐实施序列

```
第一梯队（P0，并行的两个方向）
├── 方向一：多人群组会话      → 直接产品价值最高
└── 方向五：分布式基础设施     → 先完成架构分析 + Redis 抽象层原型
                                  （不阻塞方向一的开发；两个团队可并行）

第二梯队（P1，方向一稳定后启动）
├── 方向三：可编程 Webhook 平台  → 依赖方向五的分布式基础设施（Redis 用于事件路由）
└── 方向二：资源级联生命周期     → 独立，可随时启动

第三梯队（P2，持续优化）
└── 方向四：文件变换管道         → 独立，可在任意阶段增量交付
```

---

## 附录：交叉覆盖检查

本文五个方向与现有 `docs/requirements/` 文档主题的交叉验证（基于前述 30+ 份分析的标题扫描）：

| 方向 | 现有文档覆盖情况 |
|------|----------------|
| 多人群组会话 | 零覆盖 —— 现有消息相关的分析均假设 1-to-1 模型 |
| 资源级联生命周期 | 零覆盖 —— 现有分析未讨论实体间的删除/可见性级联 |
| 可编程 Webhook 平台 | 零覆盖 —— 现有 outbox 分析均聚焦于通知 deliver 而非外部订阅管理 |
| 文件变换管道 | 零覆盖 —— 现有文件相关分析停留在上传/存储/GC，无变换 |
| 分布式基础设施 | 部分提及 —— rate limit 和 cache 的分布式化在个别文档中有一句提及，
  但无系统分析或设计方案 |
