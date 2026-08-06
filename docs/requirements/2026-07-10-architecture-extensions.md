# aero-id — 架构扩展方向分析

> 日期: 2026-07-10
> 视角: 资深架构师 + 产品经理
> 方法: 全局代码扫描（`internal/` 全量 + `migrations/` 22 个 + `configs/` + `docs/` 已有 8 轮审查）
> 前置条件: `feat/roadmap` 完全合并，已交付 38 个 gRPC RPC + 全 HTTP 端点对等 + 生产加固

---

## 项目当前状态

aero-id 已经是一个**生产就绪的双传输用户数据服务**，已完成的核心能力包括：

| 领域 | 已实现 |
|------|--------|
| **文件** | 上传/流式/去重/预签名直传/分享/元数据更新/GC 清理 |
| **消息** | 1:1 私信/系统消息/已读回执/消息反应/编辑/软删 |
| **评论** | 2 级嵌套/回复计数/点赞/举报/隐藏/编辑/编辑历史 |
| **通知** | Outbox 驱动/SSE 实时推送/未读计数/偏好设置（静音时段）/折叠 |
| **用户阻断** | 方向性阻断/审计/收件箱过滤 |
| **搜索** | PostgreSQL tsvector 全文搜索/跨领域搜索 |
| **基础设施** | 幂等性/限流（per-actor）/事务 Outbox/Circuit Breaker/OpenTelemetry/证书固定/存储 GC/审计日志/管理角色 |

这不是一个"缺功能"的项目——它缺的不是基础能力，而是**产品形态的升级**和**平台化演进**。

以下 5 个方向按**用户价值 × 架构杠杆**排序，每个都利用了现有基础设施（outbox、通知、SSO、存储）中已投入的 80% 的努力。

---

## 方向 1：群聊与频道（Group Chat / Channels）

### 概念

在当前的 1:1 消息模型之上，增加 N:N 群组对话能力。每个群组是一个 `channel`，拥有成员列表、消息流、独立的通知路由和权限模型。

### 为什么需要它

**1. 消息系统的架构天花板。** 当前 `messages` 表用 `(from_uid, to_uid)` 表示对话。这本质上是 1:1 模型——无法表达 3 人以上的群聊。表结构如下：

```sql
-- 当前 messages 表（简化）
messages: (id, from_uid, to_uid, kind, content, read_at, ...)
```

群聊需要的是：

```sql
-- 群聊模型（新增 + 改造）
channels:    (id, type[group|1:1], name, avatar, created_by, created_at, deleted_at)
channel_members: (channel_id, uid, role[owner|admin|member], joined_at, notified_at)
messages:    (id, channel_id, from_uid, content, ...)  -- 加 channel_id，to_uid 变为可选
```

**2. 产品形态从"工具"到"平台"的跨越。** 1:1 消息适合私信，但团队协作、项目讨论、兴趣群组都需要群聊。这是 Telegram/Discord/Slack 的基础能力，也是 aero-id 从"用户数据存储"到"协作平台"的最大一跳。

**3. 现有基础设施可复用。** Outbox 通知系统（`NotificationDispatcher` + `Hub.SSE`）已经支持消息通知推送。群聊的消息路由只是将 `to_uid` 替换为 `channel_members` 查询。限流、幂等、审计、搜索都可以直接在 channel 模型上工作。

**4. 与现有 1:1 兼容。** 1:1 对话可以建模为 `channel` 的一种特例（`type='direct'`，两成员），使 inbox 查询统一为 `SELECT ... FROM messages WHERE channel_id IN (SELECT channel_id FROM channel_members WHERE uid = ?)`。

### 架构影响

| 层面 | 影响范围 |
|------|---------|
 | **Schema** | 新增 `channels` + `channel_members` 表；`messages` 加 `channel_id`（可为 NULL 兼容旧数据）|
 | **Repository** | 新增 `channel` + `channel_member` repo；改造 `message` repo 的 inbox/conversation 查询 |
 | **Service** | 新增 `CreateChannel`/`JoinChannel`/`LeaveChannel`/`ListChannels`；`Send` 增加 `channel_id` 参数 |
 | **Handler** | 新增 HTTP + gRPC 端点（约 8 个 RPC）|
 | **Notification** | `message.sent` 事件增加 member 列表推送（从查 `to_uid` 改为查 `channel_members`）|
 | **Search** | 增加 `?in_channel_id=` 过滤参数；搜索结果中显示 channel 上下文 |
 | **SSE** | 事件 payload 携带 `channel_id`，客户端按 channel 分组显示 |

### 关键设计决策

- **成员数量上限**：初始设为 500（Telegram 模型），可通过配置调整。后续可通过分页成员列表支持无限群组。
- **只读频道**（Announcement）：只有管理员可以发言，成员只能阅读——适用于公告频道。
- **历史消息可搜索性**：加入频道后，新成员可搜索加入前的历史消息（通过 channel_id 过滤 messages）。
- **通知级别**：每个成员可设置 `all`（每条消息）/ `mentions`（仅 @提及）/ `muted`（静音），沿用现有的 `NotificationPreference` 模型。
- **退出与清理**：成员退出后不再收到通知，但已有消息保留（消息的 `from_uid` 保留以便追溯）。

### 风险与缓解

| 风险 | 缓解措施 |
|------|---------|
| 写放大：500 人群每消息产生 500 条 notification | 批量 INSERT + 通知折叠（已有 `CollapsedCount` 支持）；对 `all` 通知级别的成员直接 SSE 推送，跳过 notification 表 |
| 未读计数复杂度上升 | Per-channel 未读计数用 channel_members 的 `last_read_at` 列维护，messages 用 `WHERE created_at > last_read_at` 快速计数 |
| 频道名搜索需要新索引 | `channels.name` 加 `GIN trgm` 索引，支持模糊匹配 |

### 估算工作量

- Schema 迁移：2 个新表 + 1 个 ALTER TABLE（`messages.channel_id`）：**低**
- Repository 层：新 channel/member repo + message repo 改造：**中**
- Service 层：群聊 CRUD + Send 改造：**中**
- Handler 层（HTTP + gRPC）：8-10 个新端点：**中**
- 通知系统适配：群消息路由 + 批量写入：**中**
- 测试：**中高**
- **总计：3-5 周（单人）**

---

## 方向 2：统一活动流与跨域时间线（Unified Activity Feed / Timeline）

### 概念

将文件、消息、评论、通知等所有用户域的数据，整合为一个统一的、按时间排序的活动流（Activity Feed）。用户打开一个视图，可以看到"张三评论了你的文件"、"李四回复了你的消息"、"系统通知：存储空间即将用满"。

### 为什么需要它

**1. 当前的数据是孤立的。** 文件有独立的列表，消息有独立的 inbox，通知有独立的 feed，评论依附于目标对象。用户需要切换 3-4 个页面才能拼凑出"我从上次登录到现在发生了什么"的全貌。这违背了"单一信息源"的设计原则。

**2. 现有数据已完备，缺的是聚合层。** 仔细分析现有模型：

```
files:        created_at（文件上传时间）
messages:     created_at（消息发送时间）
comments:     created_at（评论创建时间）
notifications: created_at（事件记录时间）
audit_events:  created_at（操作发生时间）
```

每张表都有 `created_at` 和 `owner_uid` / `to_uid`。一个时间线查询本质上就是：

```sql
SELECT 'file' AS domain, id, created_at, owner_uid AS actor_uid FROM files  WHERE owner_uid = ? OR ... -- 可见性判断
UNION ALL
SELECT 'message', id, created_at, from_uid FROM messages WHERE to_uid = ?
UNION ALL
SELECT 'comment', id, created_at, owner_uid FROM comments WHERE target_id IN (...)
UNION ALL
SELECT 'notification', id, created_at, actor_uid FROM notifications WHERE recipient_uid = ?
ORDER BY created_at DESC
LIMIT 21
```

**3. 这是"首页/仪表盘"的基础。** 任何协作平台的核心体验都是"打开应用，看到发生了什么"。没有统一时间线，用户每次都要主动去各个页面"检查"是否有新内容。

**4. 通知系统已经做了 80% 的工作。** `NotificationDispatcher` 已经在监听 outbox 事件并创建通知记录。唯一缺少的是将**非通知类事件**（如"你上传了一个文件"、"你修改了文件元数据"）也纳入时间线——这些事件不产生通知（因为是用户自己的操作），但应该出现在活动流中。

### 架构影响

| 层面 | 影响范围 |
|------|---------|
 | **领域模型** | 新增 `Activity` 实体（`type`, `actor`, `object`, `target`, `timestamp`, `summary`）|
 | **聚合查询** | 新增 `ActivityRepository`：使用 UNION ALL + cursor 分页跨表查询 |
 | **性能索引** | 每张表加 `(owner_uid, created_at DESC)` 覆盖索引（部分表已有）|
 | **Handler** | 新增 `GET /v1/feed?cursor=&limit=` 端点（HTTP + gRPC）|
 | **SSE 扩展** | 在现有 SSE 事件流中增加 `activity.new` 事件类型，使时间线实时更新 |

### 关键设计决策

- **可见性过滤**：时间线只显示用户有权查看的内容（文件公开/已分享、消息收件人、自己发的评论、通知接收者）。
- **去重**：同一事件既出现在通知表又出现在活动流？是的——通知是推送给用户的"告知"，活动流是用户主动拉取的"记录"。通知标记已读后不会消失，活动流中的记录始终存在。
- **折叠**：连续同类活动（如 5 分钟后 3 个文件被上传）折叠为一条"你上传了 3 个文件"。
- **事件回溯**：加入时间线之前的数据是否显示？可以选择"只显示加入后的活动"（适合新用户）或"显示全部已有数据"（适合导入历史）。

### 复用现有基础设施

| 现有组件 | 在方向 2 中的角色 |
|---------|------------------|
| Outbox | 补充 `activity.log` 事件类型，使第三方操作也能写入时间线 |
| Notification | 通知已是时间线的子集 —— 直接 UNION 进 feed 查询 |
| SSE Hub | 扩展事件类型 `activity.new`，实时推送新活动到客户端 |
| Cursor 分页 | 复用 `id < cursor ORDER BY id DESC LIMIT n+1` 模式 |
| UserCache | 时间线中的 `actor_uid` 需要用昵称/头像丰富 |

### 估算工作量

- Schema：无新表（仅可能需要 `activities` 物化表优化性能，可选）：**低**
- Repository：新增跨表 UNION 查询 + 分页：**中**
- Service：活动过滤 + 排序 + 折叠逻辑：**低中**
- Handler：1-2 个端点：**低**
- SSE 扩展：新增事件类型：**低**
- **总计：1-2 周（单人）**

---

## 方向 3：内容生命周期策略引擎（Content Lifecycle Policy Engine）

### 概念

一个可配置的策略系统，允许管理员定义数据保留规则、存储配额、自动过期策略。例如：
- "未读消息 90 天后自动删除"
- "每个用户存储配额 5GB"
- "已删除文件的硬删除宽限期 30 天"
- "审计日志保留 3 年"

### 为什么需要它

**1. 当前无任何自动数据淘汰机制，存储成本会无限增长。** 软删除的数据不会被自动清除（GC sweeper 默认禁用，且只清理 orphan blob）。一个活跃用户每天发送 50 条消息、上传 3 个文件，一年后产生 18,250 条消息 + 1,095 个文件。10,000 个活跃用户 → 1.8 亿条消息。Postgres 不会自动压缩这个数据量。

**2. 法律合规要求数据保留期限。** GDPR 要求"数据存储时间不得超过处理目的所需的时间"。没有保留策略，存储期限就是"永久"——这在合规审计中是一个直接发现项。

**3. 当前 GC sweeper 有架构但无策略。** 查看 `internal/storage/gc/sweeper.go`：

```go
type Sweeper struct {
    interval       time.Duration
    batchSize      int
    deleteAfter    time.Duration // 软删除后的宽限期
}
```

已经预留了 `deleteAfter` 参数，但没有任何**按内容类型、按用户、按存储使用量**的策略路由。策略引擎可以复用这个 sweeper 的执行框架，增加策略评估层。

**4. 用户可以感知到配额。** 存储配额是 SaaS 产品的标准功能。用户可以看到"已用 2.3GB / 总配额 5GB"并主动管理自己的文件。没有配额时，少数用户的上传行为可能耗尽共享存储。

### 架构影响

| 层面 | 影响范围 |
|------|---------|
 | **领域模型** | 新增 `RetentionPolicy`（domain, max_age, action[soft_delete|hard_delete|archive]）；`StorageQuota`（uid, max_bytes, used_bytes）|
 | **配置** | 新增 `policies.retention.default_max_age`、`policies.storage_quota.default_bytes` 等配置项 |
 | **Repository** | 新增配额查询/更新方法；改造删除查询以支持策略过滤 |
 | **Service** | 新增策略评估引擎（`PolicyEvaluator.Evaluate(ctx, resource) → Action`）；新增配额检查钩子（上传前检查）|
 | **后台任务** | 新增 `PolicyEnforcer` goroutine（cron-like 调度），遍历资源并执行策略 |
 | **Storage GC** | 扩展 sweeper 以接受策略参数；将 orphan sweep 改为按策略执行 |
 | **Handler** | 新增 `GET /v1/me/quota`（配额使用情况）、`GET/PUT /v1/admin/policies`（策略管理，admin 角色）|

### 策略模型设计

```go
type PolicyRule struct {
    ID            domain.ID
    Domain        string        // "file" | "message" | "comment" | "audit"
    Scope         string        // "global" | "user" | "channel"
    ScopeID       string        // uid 或 channel_id，scope=global 时为空
    MaxAge        time.Duration // 超过此期限的数据将被处理
    Action        string        // "soft_delete" | "hard_delete" | "archive" | "notify"
    Enabled       bool
    Priority      int           // 数字越小优先级越高
    CreatedAt     time.Time
}
```

策略评估流程：

```
定时触发（每小时）→ PolicyEnforcer.Tick()
  → 加载所有活跃的 PolicyRule
  → 对于每个 domain，按优先级排序
  → 执行查询：SELECT COUNT(*) FROM {domain} WHERE created_at < now() - max_age [AND scope conditions]
  → 对每个匹配资源执行 action：
     - soft_delete: 设置 deleted_at
     - hard_delete: 从 DB + 存储中彻底删除
     - archive: 导出到冷存储（S3 Glacier）后再删除
     - notify: 发送通知给 owner（"你的文件 X 将在 7 天后自动删除"）
  → 写入审计日志
```

### 关键设计决策

- **默认策略**：保守。不设置默认删除策略——运营者必须显式配置。仅在配额超限时阻止新上传（而不是自动删除旧数据）。
- **通知前置**：在硬删除前 N 天（可配置）向用户发送通知，给予备份窗口。
- **配额计算**：`used_bytes` 不实时计算（COUNT+SUM 查询开销大），改为在文件上传/删除时原子更新 `storage_quota.used_bytes`（`UPDATE ... SET used_bytes = used_bytes + $1 WHERE uid = $2`）。
- **冷归档**：如果配置了 cold storage（如 S3 Glacier），archive action 将文件从热存储迁移到冷存储，保留元数据行但标记 `storage_tier = 'archive'`。

### 估算工作量

- Schema：2-3 个新表（`retention_policies`、`storage_quotas`、`policy_audit`）：**低**
- Repository：策略 CRUD + 配额原子更新：**中**
- Service：PolicyEvaluator + QuotaChecker：**中**
- 后台任务：PolicyEnforcer goroutine（复用 sweeper 模式）：**中**
- Handler：3-5 个新端点：**低中**
- **总计：3-4 周（单人）**

---

## 方向 4：协作审核与社区管理后台（Collaborative Moderation Console）

### 概念

将当前的单管理员内容审核升级为**团队协作的审核工作台**，支持审核队列分配、优先级排序、审核日志、用户申诉、自动处置规则和审核效能看板。

### 为什么需要它

**1. 当前审核是单人模式，无法扩展。** 现有审核能力分析：

```
举报创建（report）→ 审核员查询（GET /comments/:id/reports）→ 逐条 resolve/dismiss
```

当前实现的问题：
- 没有审核任务分配——所有审核员看到所有举报，可能重复处理（review 已指出竞态风险）
- 没有优先级——严重举报（色情/暴力/诈骗）和轻微举报（广告）在同一个队列中
- 没有审核历史——`audit_events` 记录了 `comment.report.resolve`，但无法查询"某个审核员今天处理了多少举报"
- 没有自动规则——不能设置"包含链接的新用户评论自动标记待审"
- 没有申诉流程——用户无法对隐藏决定申诉

**2. 从"技术 demo"到"运营平台"的必经之路。** 任何面对用户的 UGC 平台在 DAU > 1000 后都需要至少 1 名专职审核员。审核工具的效率直接决定了：

| 指标 | 无协作审核 | 有协作审核 |
|------|-----------|-----------|
| 审核延迟（举报→处置） | 4-24 小时 | 5-30 分钟 |
| 审核员人均处理量 | 50 件/天 | 200+ 件/天 |
| 误判率 | ~15%（单人疲劳） | ~3%（peer review + 指南） |
| 用户满意度 | 低（无反馈、无申诉） | 高（透明流程） |

**3. 与现有 admin role 系统无缝集成。** `admin.go` 已经定义了 `PermContentModerate` 权限和 `UserRole` 模型。协作审核工作台可以重用这套角色系统，新增 `moderator` 角色。

**4. Outbox + 通知系统为审核流水线提供天然支撑。** 审核事件（分配、处置、申诉）都可以通过 outbox 事件驱动：
- `report.created` → 通知所有审核员（SSE）
- `report.assigned` → 通知被分配的审核员
- `report.resolved` → 通知举报人和被举报人
- `moderation.appeal` → 通知上级审核员

### 架构影响

| 层面 | 影响范围 |
|------|---------|
 | **领域模型** | 新增 `ReportAssignment`（report_id, moderator_uid, assigned_at）；`ModerationAction`（report_id, action[approve|reject|escalate], note, created_at）；`BanRecord`（uid, reason, duration, created_by, expires_at）|
 | **Schema** | 新增 3-4 个表；comments 加 `moderator_notes` JSONB 列 |
 | **Repository** | 新增审核队列查询（按状态/优先级/分配人/时间范围过滤）|
 | **Service** | 新增 `ModerationService`：AssignReport / BatchResolve / Escalate / Appeal / AutoModerate |
 | **Handler** | 新增约 10 个审核端点（HTTP + gRPC）|
 | **后台任务** | 新增 `AutoModerator`（规则引擎，消费 outbox 事件进行自动化处理）|
 | **通知** | 审核事件作为通知类型（`moderation.report_assigned`, `moderation.content_hidden`, `moderation.appeal_received`）|
 | **审计** | 所有审核操作写入 `audit_events`（已有 `AuditReportResolve`，扩展其他类型）|

### 审核工作台数据流

```
用户举报评论 → POST /v1/comments/:id/report
  → report 状态 = open，优先级 = medium
  → outbox 事件: moderation.report_created
    → 通知所有 moderator role 的用户（SSE 推送）
    → AutoModerator 检查规则:
        - 举报人是否可信（历史准确率高？）→ 高优先级
        - 被举报内容是否匹配已知违规模式 → 自动 hidden
        - 用户名是否在黑名单中 → 自动 escalated

审核员打开工作台 → GET /v1/admin/moderation/queue?status=open&assigned_to=me
  → 查看举报详情: 内容 + 举报原因 + 用户历史 + 相似举报
  → 操作: resolve（认定违规→hide）| dismiss（不违规）| escalate（升级给 senior）
  → 备注: "多次发送广告链接，已隐藏+警告"
  → outbox 事件: moderation.report_resolved
    → 通知被举报用户（"你的评论因违反规则被隐藏"）+ 申诉链接
    → 审计日志记录

用户申诉 → POST /v1/comments/:id/appeal?reason="我认为没有违规"
  → report 状态 = appealed，重新进入队列
  → 通知 senior moderator
```

### 关键设计决策

- **二阶段审核**：初次审核（单人判断）→ 申诉审核（不同审核员判断）。二次申诉入口在应用内。
- **临时封禁**：新增 `user_bans` 表，支持定时封禁（`expires_at`）。被封禁用户的消息/评论直接在发送时被拒绝（service 层检查），不经过审核队列。
- **审核效能看板**：`GET /v1/admin/moderation/stats?since=2026-01-01` → 返回审核员工作量、平均处理时间、准确率（申诉率/撤销率）、举报趋势。
- **自动处置规则**：基于配置的规则引擎（不在代码中硬编码规则），支持关键词匹配、链接检测、频率触发。规则格式为 JSON（可热加载）。

### 估算工作量

- Schema：3-4 个新表：**低**
- Repository：审核队列查询 + 统计：**中**
- Service：ModerationService + AutoModerator 规则引擎：**中高**
- Handler：8-10 个新端点：**中**
- 后台任务：AutoModerator（消费 outbox 事件）：**中**
- **总计：4-6 周（单人）**

---

## 方向 5：文件预览管道与内容加工管线（File Preview Pipeline & Content Processing Pipeline）

### 概念

构建一个可扩展的文件内容处理管线（Processing Pipeline），在上传完成后自动执行一系列加工任务：图片缩略图生成、文档预览（PDF/Office 转图片）、视频关键帧提取、EXIF 隐私剥离、恶意软件扫描。加工结果通过 outbox 事件通知客户端。

### 为什么需要它

**1. 文件服务的核心体验是"预览"而非"下载"。** 当前文件列表只返回文件名、大小和 MIME type——用户看不到内容。在照片分享、文档协作场景中，没有缩略图的文件列表几乎不可用。现有 API 只有 `GET /v1/files/:id/url`（下载链接），没有 `GET /v1/files/:id/thumbnail`。

**2. 上传管道已就绪，加工管道是自然延伸。** `CompleteUpload` 是文件上传的终点。之后可以触发 `file.uploaded` outbox 事件，由加工管道消费：

```
User → PresignUpload → PUT to S3 → CompleteUpload → file.created (outbox)
                                                       ↓
                                            ┌──────────────────┐
                                            │ Processing Pipeline │
                                            │  ┌──────────────┐ │
                                            │  │ Thumbnail    │ │ → 缩略图写入 S3
                                            │  │ Generator    │ │ → files.meta.thumbnails 更新
                                            │  ├──────────────┤ │
                                            │  │ EXIF Stripper│ │ → 隐私数据剥离
                                            │  ├──────────────┤ │
                                            │  │ Virus Scanner │ │ → 扫描结果写入 meta
                                            │  ├──────────────┤ │
                                            │  │ PDF Preview  │ │ → 第一页转图片
                                            │  └──────────────┘ │
                                            └──────────────────┘
                                               ↓ 完成后
                                            outbox: file.processed
                                               → SSE 通知客户端
                                               → 通知文件 owner
```

**3. 与方向 1（媒体频道）形成协同。** 如果方向 1（群聊）实现，用户在群聊中发送图片时自动生成缩略图嵌入消息预览，这是即时通讯的标准体验。

**4. 差异化竞争力。** 很多文件存储服务（MinIO、S3 本身）不提供内置预览。aero-id 如果能在服务端原生支持缩略图和预览，就从一个"文件存储 API"升级为"文件管理平台"。

### 架构影响

| 层面 | 影响范围 |
|------|---------|
 | **领域模型** | 新增 `ProcessingTask`（file_id, status, task_type, result JSONB）；`files.meta` 增加 `thumbnails`、`exif_stripped`、`scan_status` 等字段 |
 | **Service** | 新增 `ProcessingPipeline`：接收 `file.uploaded` 事件，编排加工任务，支持重试和超时 |
 | **存储** | 缩略图使用独立 key 空间（`{storage_key}/thumb_{size}.jpg`），与原始文件共用存储后端 |
 | **Handler** | 新增 `GET /v1/files/:id/thumbnail?size=small|medium|large`；`GET /v1/files/:id/process?type=regenerate`（重新生成缩略图）|
 | **后台任务** | 新增 `PipelineWorker` 池（基于 semaphore.Weighted 控制并发度，限制 CPU 密集型任务数量）|
 | **配置** | 新增 `processing.workers: 4`、`processing.thumbnail.sizes: [200,600,1200]`、`processing.virus_scan.enabled: false`、`processing.exif_strip.enabled: true` |

### 缩略图生成模型

```go
type ThumbnailSize struct {
    Name   string // "small" | "medium" | "large"
    Width  int
    Height int
    Quality int  // JPEG quality (1-100)
}

var DefaultThumbnailSizes = []ThumbnailSize{
    {Name: "small",  Width: 200,  Height: 200,  Quality: 80},
    {Name: "medium", Width: 600,  Height: 600,  Quality: 85},
    {Name: "large",  Width: 1200, Height: 1200, Quality: 90},
}
```

支持的输入格式：

| MIME 类型 | 处理方式 | 依赖 |
|-----------|---------|------|
| `image/jpeg`, `image/png`, `image/webp` | Go `image` 标准库 + `golang.org/x/image/draw` | 内置 |
| `image/gif` | 取第一帧 | 内置 |
| `image/svg+xml` | 保留原格式（或 use `librsvg` if available） | 可选 |
| `application/pdf` | `pdftoppm` 转第一页为图片 | 外部命令 |
| `video/mp4`, `video/webm` | `ffmpeg` 提取关键帧 | 外部命令 |
| `application/vnd.openxmlformats-officedocument.*` | `libreoffice --headless` 转 PDF → 再转图片 | 外部命令 |

### 关键设计决策

- **管道失败不阻塞上传**：缩略图生成失败不会回滚文件上传。文件本身可正常下载，只是没有预览。失败的 task 会在 `processing_tasks` 中记录错误，留待重试。
- **并发控制**：CPU 密集型任务（ffmpeg、pdftoppm）限制并发数（worker pool），I/O 密集型任务（EXIF 剥离）可以高并发。
- **缩略图缓存**：生成的缩略图不重复生成。`GET /v1/files/:id/thumbnail` 先检查 `meta.thumbnails` 是否存在，不存在则同步生成（适用于小文件）或返回 202 Accepted 并异步生成。
- **恶意软件扫描需要外部服务**：ClamAV（`clamd`）通过 TCP 连接扫描，或通过 `exec("clamscan")` 调用。扫描结果写入 `meta.scan_result: "clean" | "infected" | "skipped"`。
- **隐私考量**：EXIF 剥离默认启用。用户可在 `UpdateMetadata` 中请求 `?include_exif=true` 获取原始元数据（存储在 `meta.original_exif` 中，仅 owner 可读）。

### 估算工作量

- Schema：1 个新表（`processing_tasks`）+ `files.meta` 扩展：**低**
- Service：ProcessingPipeline 编排逻辑：**中高**
- 后台任务：PipelineWorker 池 + goroutine 管理：**中**
- 具体处理实现：
  - 图片缩略图（Go image 库）：**低中**
  - EXIF 剥离（goexif 库）：**低**
  - PDF/视频预览（外部命令）：**中**
  - 病毒扫描（ClamAV 集成）：**中**
- Handler：2-3 个新端点：**低**
- **总计：4-6 周（单人）**

---

## 扩展方向对比总结

| # | 方向 | 用户价值 | 架构杠杆¹ | 实现成本 | 依赖前置 | 估算 |
|---|------|---------|----------|---------|---------|------|
| 1 | 群聊与频道 | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 中高 | 无 | 3-5 周 |
| 2 | 统一活动流 | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ | 低中 | 无 | 1-2 周 |
| 3 | 内容生命周期策略 | ⭐⭐⭐ | ⭐⭐⭐ | 中 | 无 | 3-4 周 |
| 4 | 协作审核工作台 | ⭐⭐⭐⭐ | ⭐⭐⭐ | 中高 | 管理角色已存在 | 4-6 周 |
| 5 | 文件预览加工管线 | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ | 中高 | 无 | 4-6 周 |

> ¹ 架构杠杆 = 对现有基础设施（outbox / notification / SSE / storage / audit）的复用程度

### 推荐执行顺序

```
Phase 1（短期，2 周）
  └── 方向 2（统一活动流）—— 成本最低，价值立现，为 Phase 2-3 提供"入口"

Phase 2（中期，6 周）
  ├── 方向 1（群聊与频道）—— 最大的产品形态提升
  └── 方向 3（内容生命周期策略）—— 生产运营刚需

Phase 3（中长期，8 周）
  ├── 方向 4（协作审核工作台）—— 社区运营刚需
  └── 方向 5（文件预览加工管线）—— 文件领域体验升级
```

方向 2 应该最先做，因为它：
1. 实现成本最低（1-2 周）
2. 不引入新领域概念（只是已有数据的聚合视图）
3. 为后续所有方向提供"统一入口"——群聊消息、审核通知、文件处理完成事件都可以出现在活动流中
4. 风险最低（无 schema 变更，纯查询逻辑）

---

## 附录：代码库关键发现速查

| 发现 | 严重性 | 涉及文件 |
|------|--------|---------|
| `idempotency.RunCleaner` 定义但未在 `app.go` 中启动 | Medium | `internal/idempotency/store.go`、`internal/app/app.go` |
| SSO Token/User 缓存无容量上限（可能 OOM） | Medium | `internal/sso/token_cache.go`、`internal/sso/user_cache.go` |
| 搜索查询无 `statement_timeout` | Medium | `internal/repository/*_search.go` |
| 文件上传不写审计日志 | Low-Medium | `internal/file/service_upload.go` |
| `like_count` UPDATE 无原子锁保护 | High | `internal/repository/comment.go` |
| SSE channel cap=16 在高速场景可能丢事件 | Low | `internal/hub/hub.go` |
| search 结果不检查 file_shares 权限 | Low | `internal/repository/comment.go`（SearchComments）|
