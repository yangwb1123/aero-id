# aero-id — 高价值扩展方向分析

> 基于 `main` 分支完整代码扫描（2026-07-11）。本文档从架构师/产品经理角度，识别 5 个尚
> 未实现但具备高杠杆效应的扩展方向。每个方向均引用**当前已存在**的基础设施和代码，并说明
> "为什么是现在"——而非纯 wish list。

---

## 方向一：群组/多参与者会话（Group Conversations）

### 现状

`messages` 表是严格的 **1:1（FromUID → ToUID）** 模型。`idx_messages_inbox`、
`idx_messages_outbox`、`idx_messages_unread` 三个索引均以单个 `to_uid` 为检索键。
`Service.GetConversation` / `ListInbox` / `MarkConversationRead` 均硬编码为双人对话。
Bulk 通知（notification dispatcher）也只处理 `message.sent` 的单收件人事件。文件中无
"room"、"channel"或"group"的概念。

### 为什么需要

群组对话是消息系统的**范式缺失**。现有基础设施已具备：

- **`outbox.Event` + `NotificationDispatcher`**：多收件人通知分发逻辑可通过枚举组成员复用
  （当前 dispatcher 的 `message.sent` 事件只写一个 recipient，扩展为 N 个即可批量入队）。
- **`block.BlockChecker`**：群组场景中的禁言/踢出/屏蔽逻辑可直接复用。
- **`hub.Hub` + SSE `Publish`**：群组消息的实时推送只需对 N 个 uid 各调一次 `Publish`。
- **`idempotency.Store`**：群组消息的幂等投递（避免重发导致收件人多条）已有架构支撑。
- **`idgen`（UUIDv7）**：群组 ID 和成员关系 ID 可直接使用同一 ID 生成器。

### 建议的技术路径

1. 新增 `group_conversations` 表（ID, name, created_by, created_at, deleted_at）和
   `group_members` 表（group_id, uid, role, joined_at, left_at）——共 2 张表。
2. 服务层新增 `GroupService`（Create, Join, Leave, AddMember, RemoveMember, ListMembers）。
3. `Message` 模型新增 `conversation_id`（可空），允许一条消息属于一个群组。`kind` 列
   扩展 `MessageKindGroupJoin` / `MessageKindGroupLeave` / `MessageKindGroupSystem` 等
   系统类型（当前 `MessageKindSystem = 4` 已预留，`IsSystem()` 机制可直接复用）。
4. 通知层：`message.sent` 事件 payload 扩展 `to_uid` 为 `group_id` + `recipient_uids[]`，
   dispatcher 按数组逐个分发表决。
5. 索引策略：`(conversation_id, created_at DESC)` 部分索引用于会话消息列表。

### 边界情况

- **大规模群组**（>1000 人）：outbox 单事件产生数千条 notification row 可能导致 relay 积压。
  需在 dispatcher 中引入批量 insert + 限流。
- **离开群组后收到历史消息**：`MarkPublished` + `MessageRead` 语义需确保已退出成员不再收到通知。
- **群组 + block 交互**：A 被 B 拉入群组后 block B，A 在该群组的消息应继续可见，但 A 不应收到
  B 触发的通知（当前 `NotificationDispatcher` 已实现 block suppression，可直接复用）。

---

## 方向二：每用户存储配额 + 上传带宽管理

### 现状

文件上传有全局 `MaxUploadBytes`（配置项 `storage.max_upload_bytes`）和可选的 MIME 类型
白名单，但**没有按账户的存储配额**。`fileService.Create` 仅检查单文件大小，`files` 表不记录
已用空间。存储 GC 可以回收软删除的 blob，但不能阻止一个用户耗尽整个存储后端。全局
`rate.Limiter` 是 RPS 维度的，不限制**吞吐量（字节/秒）**。

### 为什么需要

这是一个**服务端的 DoS 与成本控制**问题：

- 在 `configs/config.yaml` 中 `storage.type: local`（本地磁盘）和 `s3`（按量计费）两种
  模式下，一个恶意用户或 bug 脚本可以在短时间内填满可用存储。
- `storage.MaxUploadBytes` 只限制单文件，无法阻止连续上传 N 个文件直到配额耗尽。
- `CreateFileInput.Size` 由客户端声明，未经服务端验证（`cappedReader` 是字节级别的上限，
  但无法区分用户 A 和用户 B）。
- SSO 缓存（`UserCache`）已经按 uid 存储用户信息，可以扩展一个 `storage_quota_bytes` 字段。

### 建议的技术路径

1. 新增 `storage_quotas` 表（uid PK, quota_bytes, used_bytes, updated_at），或在 SSO 协定中
   扩展 `user` 元数据（但 aero-id 不存储账户数据，所以独立表更合适）。
2. 在 `fileService.Create` 和 `fileService.CompleteUpload` 中增加配额检查：`used_bytes +
   current_file_size > quota_bytes` → `errInsufficientStorage`（HTTP 507）。
3. 在 `fileService.Delete` 中释放配额：`used_bytes -= file.Size`。
4. 支持配置项 `storage.quota_default`（默认每用户配额），和 `storage.quota_overrides`
   （按 uid 特殊分配，通过 admin 接口或 config 注入）。
5. 扩展 `AdminHandler` 或新增端点 `GET /v1/admin/quotas` / `PATCH /v1/admin/quotas/:uid`
   供运营人员管理。
6. 带宽限流：在 `rate.Limiter` 基础上，新增 `uploadBandwidthLimiter`（每用户滑动窗口字节数，
   不是请求次数），在 `fileService` 的 multipart reader 中注入 throttle.

### 边界情况

- **并发上传**：`used_bytes` 的原子更新需要 `UPDATE … SET used_bytes = used_bytes + ? WHERE …`
  （乐观锁或在事务中加锁行），避免 race condition。
- **PresignUpload 路径**：直接在 `CompleteUpload` 时扣减配额，但 `PresignUpload` 阶段不预留
  配额——这可能导致 `CompleteUpload` 因配额不足而失败，客户端已上传的 blob 变成孤儿。
  如果有 `refs` 表（当前 `WithStorageRefs` 已实现），GC 最终可回收，但 UX 较差。更优方案是
  在 `PresignUpload` 时预占配额、`CompleteUpload` 时转为实际占用、超时释放。
- **Quota 超过 int64**：使用 `bigint`（PostgreSQL INT8）支持大账户。

---

## 方向三：用户可见的活动日志（Activity Feed）

### 现状

`audit_events` 表完整记录了敏感操作（文件共享、评论举报、消息删除、用户屏蔽），但元数据
**仅对 admin 角色可见**（`GET /v1/admin/audit-log` 需要 `audit.read` 权限）。普通用户
无法查看"谁访问了我的文件？"、"谁与我共享了什么？"、"我的评论被谁举报了？"。`audit`
接口是 `domain.AuditLogger` interface，在 `fileService`、`messageService`、
`commentService`、`blockService` 中作为可选依赖注入，日志写入发生在 action 之后但不
阻塞主流程。

### 为什么需要

aero-id 的产品定位是 **"data owned by accounts"（属于账户的数据）**——用户需要透明地理解
其数据在系统中的流转。当前架构已经支付了 audit 写入的成本（I/O + 存储），只是不让用户读取。
将这个数据**按作用域返回给数据主人**是低成本、高信任收益的功能。

具体而言：
- 文件被共享给谁、谁取消共享？← 当前写入 `file.share.grant` / `file.share.revoke` audit event
- 谁屏蔽了我？← 写入 `user.block` audit event（但 block 本身就是方向性的，公开给被屏蔽者
  可能违背设计意图；需要仔细过滤）
- 我的评论被举报了（匿名举报者信息脱敏）← 写入 `comment.report.resolve` audit event
- 消息被谁删除了？← 写入 `message.delete` audit event

### 建议的技术路径

1. 新增 `GET /v1/me/activity` 端点（普通用户可访问，返回作用于该用户数据的 audit events）。
2. 在 `repository.AdminRepository`（或独立 `ActivityRepository`）中新增按 `TargetUID` +
   `ActorUID` 双重过滤的查询（用户能看到"我相关的活动"以及"我的资源被他人操作"）。
3. 过滤逻辑：
   - `TargetUID == actor` → 他人的操作影响了我（如别人与我共享文件）。
   - `ActorUID == actor` → 我的操作（如我删除了消息）。
   - 不应返回他人之间的操作（即使 audit row 存在）。
4. 在 SSE events handler（`GET /v1/events`）上扩展 activity event 推送，使用户打开网页后
   实时看到谁共享了文件、谁举报了评论等。
5. 新增 migration `000023_activity_indexes`：在 `audit_events` 上加
   `(target_uid, created_at DESC)` 和 `(actor_uid, created_at DESC)` 索引。

### 边界情况

- **隐私 vs 透明**：举报者身份必须匿名。在 activity feed 中，报告结果显示"有用户举报了你的
  评论"而非"张三举报了你"。当前 `CommentReport` 表已匿名字段。
- **屏蔽交互**：如果 A 屏蔽了 B，A 不应在 activity feed 中看到 B 的任何操作（即使操作涉及
  A 的数据）。需要在 activity 查询层再次调用 `BlockChecker.IsBlocked`。
- **分页与性能**：活动日志增长快，需要和评论/消息一致的 cursor 分页、TTL 归档策略。

---

## 方向四：移动设备推送通知（Push Notification Delivery）

### 现状

实时通知通过 **SSE（Server-Sent Events）** 投递给已连接的 Web 客户端。`hub.Hub` 是一个
进程内内存订阅管理器，`NotificationDispatcher` 在创建通知行后会调用 `pusher.Publish()`
推送 SSE 事件。用户关闭页面后推送达不到。**没有 APNS (iOS) 或 FCM (Android) 通道。**

### 为什么需要

这是一个**即时消息服务**，移动端通知是产品底线而非锦上添花。当前：

- `NotificationPreferences` 已支持按类型禁用、免打扰时段、时区等精细控制。
- `dispatcher` 的 block suppression、rate limiter、collapse 逻辑已经承受了核心复杂度。
- `hub.Pusher` interface 已被 `dispatcher` 调用，但当前只有一个 SSE 实现。
- 通知行在 `notifications` 表中永久存在——即使离线用户最终也能通过 `List` 拉取，但无推送
  意味着用户不会及时得知新消息。

### 建议的技术路径

1. 新增 `push_devices` 表（uid, device_token, platform, created_at, updated_at）。
2. 新增 `POST /v1/devices` 端点供客户端注册/更新设备 token（幂等）。
3. 新增 `DELETE /v1/devices/:token` 供注销设备。
4. 在 `NotificationDispatcher` 中扩展 `pusher` 为**接口链/组合**：
   - 一个 `SSEPusher`（现有实现）
   - 一个 `PushNotificationPusher`（调用 FCM/APNS HTTP API）
   - `outbox.NewChain` 模式（已有 `dispatcher → webhook` 链的实现）可直接复用为
     `dispatcher → [sse, push, webhook]` 扇出。
5. 新增 push 失败处理：`push_devices.mark_invalid`（FCM/APNS 返回 InvalidToken 时标记
   为失效并停止投递）。重试机制复用 outbox relay 的 `MaxRetries` + `DeadLetter` 模式。
6. 可考虑独立 `push-relay` worker（部署为独立进程或 goroutine）以解耦 push 延迟与
   dispatcher 吞吐。

### 边界情况

- **设备 token 过期**：APNS/FCM 会定期收回未使用的 token。需要 batch 清理任务（再次复用
  storage GC 的 `Start` 模式，新增 `device_sweeper`）。
- **多语言推送内容**：推送 payload 需要本地化（标题、正文）。Dispatcher 不产本地化文本；
  建议推送通知 payload 中携带 `event_type` + `actor_name` 等结构化字段，由客户端/推送
  服务商渲染。
- **用户屏蔽 + 推送**：block suppression 已经在 dispatcher 层做过了，push 通道不应重新
  评估——一旦 dispatcher 决定不创建通知行，push 也不会触发。这是正确的架构分层。
- **免打扰时段**：`NotificationPreferences.QuietHoursStart/End` + `Timezone` 已在
  `PreferenceChecker` 中实现，push 层只需在 dispatcher 判定跳过时不做推送。

---

## 方向五：安全传输与媒体处理管线（Secure Transfer & Media Pipeline）

### 现状

文件上传后的处理完全缺席：

- 无病毒扫描（上传的文件直接落盘/S3，无 hooks）。
- 无图片缩略图生成（`files.mime_type` 标明 `image/*` 但不产生预览）。
- 无视频转码（原始格式直接提供下载）。
- 无文件内容全文搜索（当前 `search` 只搜索 `files.name` 和元数据，未索引 `content`）。
- S3 后端 `PresignGet` 直接暴露原始对象 URL，无缓存/CDN 层。

当前 `storage.gc.sweeper` 的架构（`interface{ List(ctx, callback) }` + `Start(ticker)`）
证明了这种"后台 worker + 可插拔 backend"的模式在项目中是正确的。媒体处理管线可以
完全复用同一模式。

### 为什么需要

- **安全合规**：无病毒扫描的文件分享服务无法通过企业安全审计。
- **UX**：无缩略图的文件列表需要全部下载才能预览，带宽成本和等待时间都很高。
- **运营成本**：无 CDN 的架构下，每一次下载都命中源站，无法利用边缘缓存。对于频繁共享
  的文件（如团队文档），这浪费大量出口带宽。
- **端到端加密**：当前所有文件以明文存储。对于"账户数据"定位，用户可能期望端到端加密
  （客户端加密，服务端盲存储）。

### 建议的技术路径

1. **病毒扫描 Hook**：新增 `internal/media` 包，实现 `ScanHook` interface。
   - `fileService.Create` / `CompleteUpload` 完成后，向 `media_queue` 表写入一条扫描任务
     （或直接通过 `outbox` 发 `file.uploaded` 事件）。
   - worker（再次复用 `outbox.Relay` 或新 `Start` ticker）消费任务，调用 ClamAV / S3
     Lambda 等外部扫描服务。
   - 扫描结果写回 `files.meta.scan_status` / `files.meta.scan_result`。
   - 受感染文件自动 visibility 设为 private，通知所有者。

2. **图片缩略图生成**：
   - 同上 worker，对 `mime_type LIKE 'image/%'` 的文件生成缩略图。
   - 缩略图另存为 `thumbnails/{storage_key}_thumb.jpg`（同一 bucket 或独立 prefix）。
   - 新增 `GET /v1/files/:id/thumbnail` 端点，返回缩略图 URL（或重定向）。
   - 无缩略图时返回 404，客户端降级为文件图标。

3. **CDN 集成**：
   - `storage.S3Config` 新增 `CDNBaseURL` 选项。
   - `PresignGet` 可以选择返回 `CDNBaseURL/storage_key` 而非 S3 原生 presigned URL，
     依赖 CDN 的 origin-pull 回源验证。
   - 本地 `local` 存储可通过 `nginx X-Accel-Redirect` 实现类似能力（当前
     `X-Accel-Buffering: no` header 已存在于 SSE handler 中，模式可用）。

4. **文件内容索引**（全文搜索的补全）：
   - 新增 `file_extracted_text` 表（file_id PK, plain_text TEXT, extracted_at）。
   - worker 提取 PDF/Word/纯文本文件内容（使用 `libreoffice` / `unrtf` / `pdftotext` 等
     外部工具或 Go 库 `github.com/ledongthuc/pdf`）。
   - 在 `search.Search` 中增加 `SearchKindFilesContent`，通过 PostgreSQL `tsvector` 索引
     `plain_text` 列进行搜索。当前 `000016_fts_indexes.up.sql` 已为 messages 和 comments
     建立了 `tsvector` 索引，files 表的 FTS 索引可直接复用该 migration 的架构。

### 边界情况

- **扫描延迟与异步性**：文件上传后立即可用，但扫描可能几分钟后才完成。前端应在文件列表中
  显示扫描状态（pending/safe/infected）。当前 `files.meta` 是 `JSONB`，可以随时写入
  `scan_status`。
- **视频转码的复杂性**：建议第一阶段只做图片缩略图 + ClamAV 扫描，视频转码列为 P2。
- **E2E 加密与搜索的冲突**：如果文件是端到端加密的，服务端无法提取内容做全文搜索和病毒扫描。
  这不是技术问题而是产品取舍——建议加密和服务器端处理互斥（用户选择加密则放弃搜索/扫描）。
- **缩略图的生命周期**：原始文件被删除时，缩略图也要被清理。GC sweeper 需要扩展 `ThumbnailRepo`。

---

## 总结：建议优先级

| # | 方向 | 产品价值 | 运营/安全价值 | 实现成本 | 推荐优先级 |
|---|------|---------|-------------|---------|-----------|
| 1 | 群组对话 | ⭐⭐⭐⭐⭐ | ⭐⭐ | 中 | **P1** |
| 2 | 存储配额+带宽管理 | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | 低 | **P0** |
| 3 | 活动日志 | ⭐⭐⭐⭐ | ⭐⭐⭐ | 中 | **P2** |
| 4 | 移动推送 | ⭐⭐⭐⭐⭐ | ⭐ | 中 | **P1** |
| 5 | 媒体处理管线 | ⭐⭐⭐ | ⭐⭐⭐⭐ | 高 | **P2** |

### 快速可摘的低挂果实（无需大架构变更）

- **存储配额硬上限**（方向二）——只需在 `fileService` 加一次配额查询 + 一个 `UPDATE` 语句，
  2-3 个文件变更即可上线。这是最紧迫的运营安全补丁。
- **群组对话的基础 CRUD**——`group_conversations` + `group_members` 两张表、一个
  `GroupService`、3 个 HTTP/gRPC 端点，不涉及消息路由变更。可以在不破坏现有 1:1 消息
  的情况下独立部署。
- **活动日志的用户端暴露**——`GET /v1/me/activity` 只需要在 `AdminRepository.ListAudit`
  上加一个 `target_uid` 或 `actor_uid` 过滤器，无需新建表。

---

*本文档不包含代码变更。以上分析的每个方向均可在现有架构约束内增量交付。*
