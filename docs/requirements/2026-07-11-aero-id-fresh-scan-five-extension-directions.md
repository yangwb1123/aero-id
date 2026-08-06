# aero-id — 最新代码扫描：五个高价值扩展方向

> **日期**: 2026-07-11  
> **视角**: 产品经理 / 资深架构师  
> **方法**: 全局扫描当前 `internal/` 及 `migrations/` 所有源码（不含 `.claude/worktrees`），逐一确认已实现与未覆盖的领域。  
> **原则**: 只列代码中 **不存在、无预留、无占位** 的真实缺口。已设计但未实现的不计（例如已有列、已有 proto 字段、已有 interface 定义），因为这些只需收官而非从零开始。

---

## 前置总结：项目现状

当前 aero-id 是一个 **极其成熟** 的生产级服务：

| 领域 | 覆盖度 |
|------|--------|
| 文件生命周期 | CRUD + 流式上传 + 大小/MIME 校验 + 哈希去重 + presigned S3 + 显式分享 + 软删除 + GC 回收 + 引用计数 |
| 消息系统 | 发送/编辑/删除/回复/附件/表情反应/已读回执/未读数/block 抑制/出站事件 |
| 评论系统 | 两级嵌套/编辑/点赞/举报（呈报/处理/驳回）/回复计数原子事务 |
| 通知 | 收件箱/已读/全读/实时 SSE/折叠/每类型限速/preference（开关+免打扰时段）/block 感知 |
| 搜索 | Postgres tsvector + GIN 全文搜索（消息/评论/文件） |
| 审计 | audit_events 表 + 关键操作写入/管理员查询 + 权限门禁 |
| 运维 | OTel 追踪 + RED 指标 + 熔断 + singleflight + 限速（HTTP+gRPC）/ 优雅关闭 |
| 单测 | 27 包 + 21 包集成测试 |
| 迁移 | 22 个可逆迁移 + 自动迁移 CLI |

**这是 38 个 gRPC RPC + 等量 HTTP 路由的体量。** 以下方向是在这个基础上从产品 & 架构纬度看到的 **全新** 增量，而非补全。

---

## 方向一：推送通知基础设施（FCM / APNs / Web Push）

### 为什么需要

当前通知的实时投递依赖 WebSocket 替代方案 **SSE**（`/v1/events`）。SSE 的前提是客户端维持一个长连接——用户在浏览器标签页开着时工作，**一旦关闭页面、切换到移动端、或处于弱网环境，实时通知归零**。用户只能通过轮询 `/v1/notifications` 获知新消息，这在移动端不可接受。

产品上，这会直接导致：
- 新消息到达后用户在数小时甚至数天后才察觉
- 评论回复的即时性归零
- 已读回执的通知闭环断裂
- 与市面上任何即时通讯产品的体验差距巨大

### 代码证据

- `hub.Hub`（`internal/hub/hub.go`）：纯内存的 SSE 订阅注册中心，**无任何推送到外部设备的能力**
- `NotificationPusher` interface（`internal/service/notification.go`）：只定义了 `Publish(uid, eventType, data string, eventID int64)`，目前唯一实现是 `*hub.Hub` 的 SSE 推送
- `notification/dispatcher.go` 中 dispatcher 的 `pusher` 字段：只接受 `hub.Pusher` 接口，**没有 FCM/APNs/Web Push 的分支**
- `outbox/webhook.go`：webhook 机制用于**服务端到服务端**的事件转发（给第三方集成），**不是终端设备推送**

### 建议方向

1. **引入 Web Push API**（Vapid）—— 浏览器后台推送，与 SSE 互补。用户关闭页面后仍能收到系统通知
2. **FCM / APNs 适配器** —— 为 Android / iOS 提供原生推送。需要一个设备注册端点（`POST /v1/devices`），以及一个在 NotificationDispatcher 中根据 `(uid → devices)` 映射推送的 `PushSender`
3. **统一推送路由** —— 在 dispatcher 中新增一个 `PushSender` interface，集成到 `Handle()` 的收尾阶段：先写 notification row + 推 SSE，再根据用户注册的设备列表并发推送外部渠道

### 增量估算

- 设备注册表 migration + repository：低
- Web Push adapter：中（浏览器 Push API 有标准 Go 库）
- FCM adapter：中（Google Firebase Admin SDK）
- 统一推送路由修改（dispatcher 新增一个 pusher chain）：低

---

## 方向二：群组/多人群聊（Group Conversation）

### 为什么需要

当前消息模型是严格的 **一对一私信**（`messages.from_uid` + `messages.to_uid`）。即使部分场景下 `messages.kind` 预留了 `MessageKindSystem`，系统消息也只能发给单个 uid。**不存在"群组"、"房间"、"频道"或"话题"这样的第一类实体。**

从产品来看：
- 项目协作场景天然需要 N:N 沟通（团队群聊、项目讨论）
- 文件分享目前是 O→G（owner→grantee），没有"分享到某个群"
- 评论系统是"对文件的讨论"，但没有永久的主题空间
- 群组是推送通知、未读数聚合、消息历史查询的**自然容器**

### 代码证据

- `domain.Message`（`internal/domain/message.go`）—— 只有 `FromUID` / `ToUID`，**没有 ConversationID / RoomID / ChannelID**。私信就是最高级抽象
- `repository.MessageRepository`（`internal/repository/message.go`）—— 查询按 `(from_uid, to_uid)` 组合；无按 conversation 分组的查询
- `message.Service`（`internal/service/message.go`）—— `List` / `ListConversations` 接口均面向一对一
- `notification/dispatcher.go` 的消息事件 —— `messageSentPayload` 只有 `message_id` / `from_uid` / `to_uid`，无法扩充到群组
- 迁移文件列表：**没有 `create_conversations` / `create_conversation_members` 相关的 migration**

### 建议方向

1. **Conversations（会话）作为第一类实体** —— 新表 `conversations`（id, type=one_on_one|group|channel, title, avatar, created_at），成员表 `conversation_members`（conversation_id, uid, role=admin|member, joined_at, last_read_at, muted_until）
2. **Message 模型扩展** —— `message.conversation_id`（FK to conversations, nullable 兼容私信）、`messages.mentions`（JSON array of uid）
3. **API 扩展** —— `POST /v1/conversations`（创建+邀请成员）、`POST /v1/conversations/:id/messages`（群发消息）、`GET /v1/conversations`（会话列表+最后一条消息预览+未读数）
4. **通知适配** —— 群消息通知聚合到 conversation 级别，支持免打扰

### 增量估算

- 数据模型 + migration：中
- Repository + Service：高（需重写/扩展消息路由）
- HTTP + gRPC handler：中高
- 通知 + SSE 适配：中

> **注意**: 这是**扩展**而非重构。私信架构完全保留，群组作为上层叠加。消息的 `to_uid` 在群组场景下可以留空或用 conversation_id 承载。

---

## 方向三：文件处理管线（缩略图 / 转码 / 病毒扫描）

### 为什么需要

当前文件上传管线是纯 **存储透传**：校验 → 哈希 → 存 blob → 写 row。服务器对文件内容**不做任何后续处理**。

具体缺失去：
- **图片**：上传后无缩略图生成，聊天中发一张 4000x3000 的照片只能全尺寸加载
- **视频**：无转码、无关键帧截图，无法在消息流中预览
- **文档**：无法生成 PDF 预览 / 转为 HTML 展示
- **病毒/恶意软件扫描**：用户之间可互发任意文件（即使 MIME 在 allow-list 内），平台无安全扫描

### 代码证据

- `file.Service.Create()`（`internal/service/file.go`）—— 从 reader 流式写入 `store.Put()` + 哈希去重，**完成后直接返回，没有触发任何异步任务**
- `internal/outbox/` —— outbox 模式已就绪（事务性事件），但 **File 垂直未使用 outbox**。`file.Service.Create()` 没有 `outbox.Append()` 调用
- `domain.File` 模型 —— 没有 `thumbnail_key`、`processing_status`、`virus_scan_at` 等字段
- `storage.FileStorage` interface —— 只有 `Put` / `Get` / `Delete` / `PresignUpload`，没有缩略图/转码方法
- `migrations/` —— 无 `file_processing_jobs` 或 `file_versions` 表

### 建议方向

1. **异步文件处理管线** —— 在 `file.Service.Create()`（和 `CompleteUpload()`）成功后通过 outbox 写入 `file.process` 事件
2. **Processor 组件** —— 新包 `internal/fileproc/`，订阅 outbox 事件，执行：
   - 图片缩略图生成（`thumbnail_{sm,md,lg}.jpg` 存储在同一个 backend，key 关联原图）
   - 病毒扫描接口（ClamAV / 第三方 API）
   - 媒体信息提取（exif → 存入 file.meta）
   - PDF 预览图
3. **File 模型扩展** —— `processing_status`（pending/processing/done/failed）、`thumbnail_keys`（JSON）、`virus_scan_result`
4. **API 扩展** —— `GET /files/:id/thumbnail?size=sm` 返回缩略图（公开或按权限）

### 增量估算

- 处理任务 outbox 事件 + Consumer：中
- 缩略图生成（go 图片处理库）：中
- 病毒扫描集成：低（ClamAV 有现成 Go 客户端）
- API 扩展：低
- 存储接口扩展：低

---

## 方向四：跨实例共享状态（分布式缓存 & 分布式限速）

### 为什么需要

当前三个延迟敏感的关键基础设施全部是 **单机 in-memory**：

| 组件 | 存储 | 多实例行为 |
|------|------|-----------|
| `sso.TokenCache` | `sync.Map` + TTL 清理 | 每实例独立缓存的 token，同一用户的请求打到不同实例 → 全部穿透到 SSO |
| `sso.UserCache` | `sync.Map` | 同上 |
| `ratelimit.Limiter` | `map[string]*bucket` + idle eviction | 每实例独立计数。攻击者可以轮流打不同实例突破速率上限 |
| `notification.notifRateLimiter` | `map[string][]time.Time` + inline 清理 | 每实例独立，多实例重复计数 |

在水平扩展（2+ 副本）的生产环境中：
- **SSO 雪崩** —— 冷启动 + 多实例 = 瞬时 SSO 请求率 * N，即使有 circuit breaker 也在实例级别独立触发
- **限速失效** —— 恶意用户在 10 个实例间轮询，实测限速上限被放大 10 倍
- **通知重复** —— 每实例独立的 rate limiter 导致重复通知

### 代码证据

- `sso.TokenCache`（`internal/sso/token_cache.go`）：使用 `sync.Map`，`RunCleaner` 清理过期条目
- `sso.UserCache`（`internal/sso/user_cache.go`）：同上
- `ratelimit.New()`（`internal/pkg/ratelimit/ratelimit.go`）：纯内存 token-bucket，`map[string]*bucket`
- `notifRateLimiter`（`internal/service/notification.go`）：`keys map[string][]time.Time`，实例本地

### 建议方向

1. **引入 Redis 中心缓存层** —— 可选且向后兼容：当 `cache.redis.endpoint` 配置时，TokenCache / UserCache 使用 Redis 而非 sync.Map；无配置时回退到 in-memory
2. **分布式限速** —— `ratelimit.Limiter` 改用 Redis + Lua 脚本（原子滑动窗口或 token-bucket），保持 API 不变
3. **通知限速合并** —— `notifRateLimiter` 实例级限速在分布式部署时改为 Redis 后端，确保全局一致
4. **启动预热** —— 新实例上线时不需要冷启动 cache；Redis 已经是热状态

### 增量估算

- Redis 客户端 + 配置：低
- TokenCache Redis 后端实现：中
- UserCache Redis 后端实现：低（复用模式）
- 分布式限速 Lua 脚本：中
- 通知限速迁移：低
- 本地回退 + 配置开关：低

> **设计原则**: 不可用 ≠ 宕机。Redis 不可达时退回到本地 in-memory（serve-local-stale），限速退回到 "fail open"（allow all）。不因为中心缓存而降低可用性。

---

## 方向五：自动化内容审核管线（Auto-Moderation）

### 为什么需要

`comment_reports` + `admin` 角色 + `audit.read` 权限链已经构成了一个**完整但纯人工的审核系统**。举报 → 管理员登录 → 查看 → 裁决。

在规模下：
- **时效性** —— 一条恶意评论可能在人工处理前存在数小时，被 N 个用户看到
- **覆盖率** —— 没有举报的违规内容永远不被发现
- **扩展性** —— 一个审核员每天处理 1000 条已经是极限，且标准不一致
- **文件安全** —— 用户之间互传的任何文件都缺乏安全扫描（与方向三重叠但独立：方向三聚焦技术处理，这里聚焦内容安全）

### 代码证据

- `comment.Service` + `comment_reports`（`internal/comment/service.go` + `migrations/000007`）—— 举报模型实现完整，但 `reports` 被写入后**没有任何自动化消费**（没有 auto-moderation worker，没有 outbox 事件）
- `internal/handler/http/admin.go` —— `GET /v1/admin/audit-log` 只有**事后查询**，没有审核队列端点（`GET /v1/admin/reports/pending`，`POST /v1/admin/reports/:id/resolve`）
- `domain.Comment` —— 没有 `auto_moderation_score`、`auto_moderation_label`、`needs_review` 字段；评论的 `status` 只有 Normal/Deleted/Hidden
- `outbox` —— 没有任何 "评论触发审核" 的事件生产者
- `notification` —— 无 "你的评论被举报/隐藏" 通知类型（`NotifTypeCommentFlagged`）

### 建议方向

1. **审核队列 API 补齐** —— `GET /v1/admin/reports`（支持 status=open 过滤）+ `PUT /v1/admin/reports/:id/resolve` + `PUT /v1/admin/reports/:id/dismiss`。当前只能通过 `comment_reports` 表直接操作
2. **自动审核服务** —— 新包 `internal/moderation/`，包含：
   - **文本分类器 adapter**（keyword 规则 + 可选 NLP API / OpenAI moderation endpoint）
   - **图片检测 adapter**（NSFW / 暴力检测）
   - **阈值配置**（自动隐藏 vs 标记待审）
3. **审核触发管线** —— `comment.Create()` 完成后通过 outbox 发送 `comment.moderate` 事件，moderation worker 消费
4. **通知闭环** —— 新增 `comment.hidden`（因审核被隐藏通知作者）和 `comment.flagged`（举报已处理通知举报人）通知类型
5. **文件审核** —— 文件上传完成后通过 outbox 发送 `file.scan` 事件执行病毒/违规扫描（与方向三协同）

### 增量估算

- 审核队列 API：低（复用现有模型）
- 文本规则引擎：中
- NLP API 集成：中
- 图片检测集成：中
- 通知类型扩展：低
- 文件扫描触发：低

---

## 综合优先级建议

| 方向 | 产品影响 | 架构杠杆 | 估算成本 | 前置依赖 | 建议时序 |
|------|---------|---------|---------|---------|---------|
| 推送通知 | ★★★★★ | ★★★ | 中 | 方向四（分布式中较弱） | P0 |
| 群组会话 | ★★★★★ | ★★★★★ | 高 | 无 | P1 |
| 文件处理管线 | ★★★★ | ★★★ | 中高 | 无（outbox 已就绪） | P1 |
| 分布式缓存与限速 | ★★★ | ★★★★★ | 中 | 无（纯底层优化） | P0（多实例时） |
| 自动化审核 | ★★★★ | ★★★★ | 中高 | 无 | P2 |

> **P0 定义**: 不上线则显著影响产品可用性或生产可靠性  
> **P1 定义**: 高价值产品能力，可规划在下个版本  
> **P2 定义**: 重要但需 P0/P1 先行

---

## 附录：不列入本次分析的方向（已有或已有预留）

以下方向在之前的 analysis 中被多次提及，但 **本次代码扫描确认已实现** 或 **已有完整设计预留**，不再列为新方向：

| 方向 | 当前状态 |
|------|---------|
| 消息 Reactions | ✅ 已实现（`message_reactions`，`POST /v1/messages/:id/reactions`） |
| 全文搜索 | ✅ 已实现（tsvector+GIN + HTTP/gRPC 端点） |
| 审计日志 | ✅ 已实现（`audit_events` + admin 查询） |
| 管理员角色 | ✅ 已实现（`admin_roles` + `user_roles` + permission check） |
| 通知偏好 | ✅ 已实现（`notification_preferences` + 免打扰时段） |
| 通知折叠 | ✅ 已实现（`CollapsedCount` + `collapseWin`） |
| 通知限速 | ✅ 已实现（`notifRateLimiter`，dispatcher 侧） |
| 文件去重 | ✅ 已实现（`checksum` + `FindByChecksum`） |
| 文件大小/MIME 校验 | ✅ 已实现（`maxBytes` + `allowedMime` + `cappedReader`） |
| 文件分享 | ✅ 已实现（`file_shares` + `is_shared_with` + 审计日志） |
| 引用计数存储 | ✅ 已实现（`storage_refs` + `Increment`/`Decrement` + GC 整合） |
| 存储 GC | ✅ 已实现（row 清除 + orphan 扫描 + ref 检测） |
| Outbox Webhook | ✅ 已实现（HMAC-SHA256 + 事件过滤 + Chain） |
| SSE 实时推送 | ✅ 已实现（`/v1/events` + `last_event_id` 恢复） |
| 冷启动预热 | ✅ 已实现（SSO warmup probe + db ping warmup） |
| gRPC 反射/健康 | ✅ 已实现（config-gated reflection + health） |
| 幂等性写入 | ✅ 已实现（idempotency keys + GORM store + background cleanup） |
| OTel 追踪 | ✅ 已实现（HTTP + gRPC + SSO 跨服务追踪） |
