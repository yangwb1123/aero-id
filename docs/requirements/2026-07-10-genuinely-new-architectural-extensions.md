# aero-id — 尚未被覆盖的高价值扩展方向

> 日期: 2026-07-10
> 方法: 全局代码扫描 + 与 docs/ 下全部 7 轮审查、35+ 扩展方向进行逐项比对，确保本分析不重复已有内容。
> 基线: 迁移 000001–000022，41+ gRPC RPCs，完整双传输对等，所有 ROADMAP 项目已交付。

---

## 零、前提——已覆盖的 35+ 方向（不重复列表）

7 轮审查（2026-06-30）和此前多篇分析已覆盖的方向包括（按主题分组）：

| 领域 | 已覆盖的方向 |
|------|------------|
| **消息** | 群聊、会话归档/静音/置顶、消息草稿、定时/延迟发送、引用回复/转发、Reactions（表情反应）、已读回执历史、送达确认、在线状态/输入指示器、会话内搜索 |
| **文件** | 版本控制、文件标签、病毒扫描/内容安全、EXIF 数据剥离、文件预览管道（缩略图/转码） |
| **通知/实时** | 通知偏好设置、免打扰、SSE last_event_id、推送通知（移动端） |
| **审核/管理** | 管理员角色系统、全局内容审查台、用户封禁、审计日志查询 API、死信队列 UI |
| **多租户/组织** | 多租户/工作空间、可插拔认证后端（JWT/Proxy/Dev）、API Keys/服务账号 |
| **搜索** | FTS 中文分词、搜索结果上下文、搜索建议/过滤 |
| **基础设施** | 分布式限流（Redis）、实体缓存层、数据生命周期管理、跨区域部署、SLO/SLA 框架、OpenAPI/Swagger、gRPC-Web、自诊断仪表盘、冷启动预热 |
| **数据/合规** | 数据导出/GDPR、会话导出、批量操作、分析管线/使用统计 |
| **事件平台** | Webhook 事件订阅管理、事件回放、签名轮转 |

**本文件的目标：** 找出上述广谱覆盖之外的、真正未被触及的高价值方向。

---

## 方向一：移动端优先的消息同步协议（Offline-First / Sync API）

### 当前状态

所有 API 设计假设客户端**始终在线**。移动端（或 Web 端断网恢复后）需要：

1. 全量拉取 inbox 列表（分页遍历所有会话）
2. 每个会话逐条拉取消息（再次分页）
3. 比对本地缓存判断哪些是新消息

这导致断线重连时的**数据风暴**——如果用户有 50 个会话、每个会话 200 条消息，重连后需要发出 50+ 个 HTTP 请求才能恢复 UI 状态。带宽浪费严重，用户体验差。

### 为什么现有分析没覆盖

已有方向覆盖了通知推送（SSE）、搜索增强、会话管理——但**都假设客户端在线**。离线-在线状态转换时的**数据差异同步**问题完全未触及。

### 缺失的核心能力

**1. 时间戳驱动的增量同步**

```http
GET /v1/messages/sync?since=2026-07-09T10:00:00Z
```

返回 `since` 时间戳之后发生变化的所有消息（新建、编辑、软删除、已读状态变更）。客户端本地维护 `last_sync_at`，每次重连只拉增量。

**2. 批量状态同步**

```http
POST /v1/messages/sync-state
{ "conversations": [
    {"with_uid": "abc", "last_seen_at": "2026-07-09T12:00:00Z", "last_read_at": "2026-07-09T11:30:00Z"},
    ...
  ]
}
```

服务端返回每个会话在 `last_seen_at` 之后的新消息计数和摘要。客户端无需拉取全量 inbox 即可显示未读角标。

**3. 冲突标记**

消息编辑和软删除在服务端是最终决定，但客户端离线期间可能产生**乐观 UI 更新**（如用户离线时删除了一条消息，但服务端稍后通过 SSE 收到了编辑通知）。需要 `conflict_version` 或 `updated_at` 字段让客户端能识别和解决冲突。

### 实现影响

| 组件 | 改动 | 复杂度 |
|------|------|--------|
| `messages` 表 | 已满足（已有 `created_at`/`updated_at`/`deleted_at`） | 无 |
| `inbox` 查询 | 新增 `sync` 端点，使用 `WHERE created_at > $since OR updated_at > $since OR deleted_at > $since` | 低 |
| `files` 表 | 同理，新增 `synced_at` 标记 | 低 |
| notifications | 同步端点可以复用现有 `ListSince` 逻辑（已有 SSE reconnection 的 `last_event_id` 机制） | 低 |
| 客户端 SDK | 需要实现增量同步状态机（首次全量 → 后续增量） | 中 |

### 为什么这值得做

1. **移动端是即时通讯的主要战场。** 没有离线支持和增量同步，移动端体验无法与竞品（Telegram/WhatsApp/Signal）对标。
2. **复用现有 schema。** `deleted_at`、`edited_at`、`read_at` 列已存在，增量同步不需要新迁移。
3. **SSE 基础设施的补全。** SSE 提供实时推送，增量同步提供断线恢复——两者结合才是完整的消息同步协议。
4. **数据竞争 TCO 降低。** 减少重连后的 N+1 请求风暴，直接降低带宽成本和后端负载。

---

## 方向二：存储成本治理与生命周期自动化

### 当前状态

现有能力：
- 文件 GC sweeper：清理过期软删除文件，回收孤儿 blob（**默认关闭**）
- 幂等键 TTL：24h 后过期（**cleaner 已启动**）
- 审计日志：append-only，**无任何 TTL 或归档策略**
- 通知：**无 TTL**

17 张表中，只有 `idempotency_keys` 有 TTL 机制。其余数据表只会增长——`files`（包含实际 blob 存储成本）、`audit_events`、`notifications`、`outbox_events`、`messages` 都没有数据生命周期策略。

### 为什么现有分析没覆盖

已有分析覆盖了"数据生命周期管理"（方向 γ/第 4 轮）和"文件 GC"，但视角是**基础设施层**（冷热分离/归档到 S3 Glacier）。从未从**成本治理**角度审视——即：谁为存储付费？如何控制存储成本增长？如何为不同数据类别设置不同的保留策略？

### 缺失的核心能力

**1. 按数据类别的分层保留策略**

| 数据类别 | 当前 | 建议热保留 | 建议温保留（低成本存储） | 建议可删除 |
|---------|------|-----------|----------------------|-----------|
| `messages` | 永久 | 90 天 | 1 年（保留元数据，blob 去重） | 1 年后用户可主动删除 |
| `notifications` | 永久 | 30 天 | 90 天 | 90 天后自动清理 |
| `audit_events` | 永久 | 90 天 | 3 年（可导出归档） | 7 年（合规保留底线） |
| `outbox_events` | 永久 | 7 天（发布后） | — | 7 天后自动删除已发布事件 |
| `idempotency_keys` | 24h（已有） | 24h | — | 24h（已满足） |
| `comment_likes` | 永久 | 永久 | 永久（数据量极小） | 用户删评论时级联 |
| `files`（blob） | 随用户删除 | 用户主动删除前 | — | 用户可设置自动清理>N 年的文件 |

**2. 存储配额与用量告警**

- 用户级配额：`max_storage_bytes` 配置项，`Create` 时校验 `SUM(size) WHERE owner_uid = ? AND deleted_at IS NULL`
- 用量 API：`GET /v1/me/storage-usage` → `{used_bytes, quota_bytes, usage_percent}`
- 告警阈值：`used > 80%` → 通知用户，`used > 100%` → 拒绝新上传
- 计费对接：预留 `billing_metadata` 列，可对接外部计费系统

**3. 自动归档 Job（复用 outbox 基础设施）**

- 新建 `internal/lifecycle` 包，定义 `Archiver` 接口
- outbox 事件类型 `lifecycle.archive` → 触发归档 worker
- 归档目标：CSV/Parquet 格式写入 S3 → 从主表 `DELETE`
- 配置驱动：`data_retention.messages: 90d`, `data_retention.audit: 365d`

### 实现影响

| 组件 | 改动 | 复杂度 |
|------|------|--------|
| `config.yaml` | 新增 `data_retention.*` 和 `storage.quota.*` 配置段 | 低 |
| `file/service_crud.go` | `Create` 中增加配额校验 | 低 |
| 新建 `GET /v1/me/storage-usage` | 聚合查询 `SUM(size)` | 低 |
| 新建 `internal/lifecycle` | 归档 worker（复用 outbox relay 模式） | 中 |
| 迁移 | 已有表加注释/索引即可，无需改 schema | 无 |

### 为什么这值得做

1. **存储成本是不可忽视的生产开支。** 在 AWS S3 上，1TB 文件存储月费 ~$23。100 万用户 × 每人 100MB = 100TB → 月费 $2,300。没有生命周期管理，成本线性增长。
2. **合规驱动。** 审计日志保留 7 年（金融/医疗合规要求），通知不需要保留 7 年。没有分类策略，要么全保留（存储成本爆炸）、要么全删除（不合规）。
3. **为计费做准备。** 存储配额是 SaaS 变现的基础——免费用户 100MB、Pro 用户 10GB、企业用户不限量。当前无法实施。
4. **复用已有基础设施。** Outbox relay 模式已经成熟，`internal/lifecycle` 只是新的事件消费者。

---

## 方向三：对外集成网关（Webhook 平台 + 第三方 Channel 适配器）

### 当前状态

现有 outbox 链支持：
- NotificationDispatcher → 本地通知表 + SSE 推送
- WebhookHandler → POST 到外部 URL（HMAC-SHA256 签名）

当前 webhook 的局限：
1. **单端点** — 整个实例只有一个 webhook URL（`outbox.webhook.url`），所有事件类型都发到同一地址
2. **无订阅管理** — webhook URL 是配置项，无法通过 API 注册/管理
3. **无事件类型过滤** — 要么全发，要么不发
4. **无投递保障 UI** — 死信事件只能查数据库
5. **无第三方 Channel** — 只支持 HTTP POST，不支持 Slack Bot、Telegram Bot、Email、SMS 等渠道

### 为什么现有分析没覆盖

已有方向 5（第 2 轮）和方向 E（第 6 轮）覆盖了 webhook 事件平台（订阅管理 + 死信重试）。但方向是**纯技术平台**（给开发者用的事件管道）。从未覆盖**产品级集成**——即，终端用户如何通过第三方渠道（Telegram、Slack、Email）接收通知和操作数据。

### 缺失的核心能力

**1. Channel 适配器框架**

```go
type Channel interface {
    Name() string                                    // "telegram", "slack", "email"
    Send(ctx context.Context, user User, msg Notification) error
    // 可选：解析 inbound 消息（Telegram bot → 创建 aero-id 消息）
    HandleInbound(ctx context.Context, raw interface{}) (*InboundMessage, error)
}
```

开箱实现：
- `channel/slack` — Slack Webhook API（OAuth + 消息卡片）
- `channel/telegram` — Telegram Bot API（BotFather token + 键盘响应）
- `channel/email` — SMTP（HTML 模板 + 摘要模式）
- `channel/webhook` — 现有（自定义 JSON POST）

**2. 用户级 Channel 绑定**

- 新建 `user_channels` 表：`(uid, channel, channel_user_id, config JSONB, enabled, created_at)`
- 用户通过 OAuth 或配置绑定 Slack/Telegram/Email 到自己的 aero-id 账户
- API: `POST /v1/channels/slack/connect`（OAuth 跳转）→ `GET /v1/channels`（当前绑定列表）→ `DELETE /v1/channels/:id`

**3. 通知路由规则**

- 在通知偏好之上，允许用户配置：`文件分享通知 → Slack`、`新消息 → Telegram`、`每日摘要 → Email`
- `NotificationDispatcher` 在写入通知表后，除了 SSE 推送，还检查用户是否绑定了 channels
- 异步发送（走 outbox）：`notification.channel_delivery` 事件 → channel worker 消费

**4. Inbound 消息**

- Telegram Bot 可以接收用户回复，转发为 aero-id 消息
- 示例：用户在 Telegram 上回复 Bot 的消息 → Bot 收到 callback → `HandleInbound` → `service.SendMessage` → 消息进入 aero-id

### 实现影响

| 组件 | 改动 | 复杂度 |
|------|------|--------|
| 新建 `internal/channel` | Channel 接口 + 注册表 | 低 |
| 新建 `internal/channel/slack` | Slack Webhook 适配器 | 中（需 OAuth 集成） |
| 新建 `internal/channel/telegram` | Telegram Bot 适配器 | 中 |
| 新建 `internal/channel/email` | SMTP 发送（已有 `go.mod` 可能需加 `gomail`） | 低-中 |
| `user_channels` 表 | 新 migration | 低 |
| `NotificationDispatcher` | 扩展 Handle() → 调用 channel 分发 | 低 |
| SSO 集成 | 可选：通过 SSO 同步用户的 email/channel 绑定 | 低 |

### 为什么这值得做

1. **产品粘性的飞跃。** 用户绑定 Telegram Bot 后，即使不打开 aero-id 客户端也能接收消息和回复——这是从"一个 App"到"无处不在"的转变。
2. **推送通知的前置条件。** 移动端 Push Notification（APNs/FCM）是渠道适配器框架的一个实现。建好框架后，添加 Push 只是加一个 adapter。
3. **开发者生态。** 第三方开发者可以编写自定义 Channel（Webhook 已有），集成自己的系统。这是平台化的关键能力。
4. **与通知偏好方向互补。** 通知偏好解决"要不要发"，channel 适配器解决"往哪发"。两者结合构成完整的通知路由系统。

---

## 方向四：多租户数据隔离与行级安全（Row-Level Security）

### 当前状态

当前所有表通过 `owner_uid` / `from_uid` / `to_uid` 实现**用户级数据隔离**。从架构视角看，这是最薄的隔离层——数据只按用户划分，没有"组织/工作空间/项目"的层级概念。

`domain/admin.go` 中存在 `Role` 和 `UserRole` 结构体，但**未在 app.go 中 wired**——管理员角色系统有模型无实现。

### 为什么现有分析没覆盖

已有方向 1（第 2 轮）覆盖了"多租户/组织工作空间"——但那篇分析的切入点是**产品功能**（团队协作空间、组织级权限）。从未从**安全合规的数据隔离**角度分析过——即，PostgreSQL 行级安全策略（RLS）如何保证不同租户的数据即使在同一张表中也绝对不可见。

### 缺失的核心能力

**1. 租户（Tenant）模型**

```
tenants                  tenant_members               所有业务表的 tenant_id 列
┌──────────────┐        ┌──────────────────────┐      ┌───────────────────┐
│ id (UUIDv7)  │        │ tenant_id             │      │ files.tenant_id   │
│ name         │──1:N──→│ uid                   │      │ messages.tenant_id│
│ domain       │        │ role (owner/admin/...)│      │ comments.tenant_id│
│ plan (free/…)│        │ joined_at             │      │ notifications.tid │
│ created_at   │        │ invited_by            │      │ audit.tid         │
└──────────────┘        └──────────────────────┘      └───────────────────┘
```

**2. PostgreSQL Row-Level Security（RLS）**

```sql
-- 为每个业务表启用 RLS
ALTER TABLE files ENABLE ROW LEVEL SECURITY;
ALTER TABLE messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE comments ENABLE ROW LEVEL SECURITY;

-- 每表一个策略：用户只能看到自己租户的数据，且必须是该租户的成员
CREATE POLICY tenant_isolation ON files
  USING (tenant_id = current_setting('app.tenant_id')::UUID);

-- 个人数据（无 tenant_id）向后兼容：
CREATE POLICY personal_data ON files
  USING (tenant_id IS NULL AND owner_uid = current_setting('app.uid')::TEXT);
```

**3. aero-id 层的 Tenant 中间件**

- `middleware/tenant.go`：从请求头 `X-Tenant-ID` 或 token claim 中提取，注入 context
- 通过 `SET LOCAL app.tenant_id = '...'` 传递给 PostgreSQL 会话
- 所有查询自动受 RLS 约束——repository 层不需要改任何 `WHERE` 子句

**4. 个人空间与组织空间的共存策略**

- 用户默认在"个人空间"（tenant_id IS NULL），行为与当前完全一致（向后兼容）
- 加入组织后，用户可以选择"使用组织身份"操作数据（X-Tenant-ID 头）
- 同一用户的数据可以分布在个人空间和多个组织中——但互不可见（RLS 保证）

### 实现影响

| 组件 | 改动 | 复杂度 |
|------|------|--------|
| 迁移 | 新建 `tenants`、`tenant_members` 表；为每个业务表加 `tenant_id` 列 + 索引 | 高（22 张表） |
| `middleware/tenant.go` | 新建中间件（~50 行） | 低 |
| `internal/app/app.go` | 注册 tenant 中间件 | 低 |
| 所有 repository | **需要确认：** 使用 GORM 时，RLS 是否透明工作？GORM 的 `db.Create` 会插入 RLS 策略不阻塞的行——除非策略也限制 INSERT。需要场景测试。 | 中 |
| `domain/admin.go` | 现有 `Role`/`UserRole` 可复用，加 `tenant_id` 列 | 低 |
| 现有 API | 个人空间场景完全向后兼容，组织空间场景客户端传 `X-Tenant-ID` | 低 |

### 为什么这值得做

1. **数据安全合规的底线。** 如果没有 RLS，多租户的数据隔离完全依赖应用代码的正确性——一次 Bug（如忘记 `WHERE owner_uid = ?`）就可能导致租户 A 看到租户 B 的数据。RLS 是 PostgreSQL 层面的安全网，即使应用代码出错，数据也不会泄露（defense in depth）。
2. **RLS 性能优于应用层过滤。** `current_setting` 查询的开销是微秒级的——比应用层加 `WHERE tenant_id = ?` 和连接池上下文传播的开销更低。
3. **企业客户的进场门禁。** 几乎所有企业软件采购都要求 SOC 2 / ISO 27001 认证，其中"租户隔离"是核心控制点。没有多租户隔离——没有企业客户。
4. **向后兼容性。** `tenant_id IS NULL` 的个人空间模式与当前行为完全一致，部署零影响。组织功能可以渐进上线。

---

## 方向五：消息模态与内容结构化（Rich Content / Message Types）

### 当前状态

`messages.kind` 是 `SMALLINT`，映射到 Go 的 `int` 枚举：

| 值 | 当前用途 | 现状 |
|----|---------|------|
| 1 | 文本消息 | ✅ 默认类型，`content` 为纯文本 |
| 2 | 图片消息 | ✅ `file_id` 引用图片 |
| 3 | 文件附件 | ✅ `file_id` 引用文件 |
| 4 | 系统通知 | ✅ `from_uid = "system"`，走 `SendSystem` |

但**这些类型只是标记，不约束内容结构**。图片消息的 `content` 字段可能含有文本描述（希望如此），也可能为空（也行），还可能含有 HTML（危险但允许）。没有消息内容的 schema 校验。

### 为什么现有分析没覆盖

已有方向覆盖了消息 Reactions、引用回复/转发、已读回执、送达确认、草稿、定时发送——但**全是围绕纯文本/附件的增强**。从未从"消息本身应该支持富媒体结构化内容"的角度分析过。

### 缺失的核心能力

**1. 消息体 schema 演进**

为 `messages.body` 引入结构化 JSON schema（替代自由文本 `content`）：

```json
// 当前（自由文本）
{ "body": "Hello world" }

// 未来（结构化）
{
  "body": {
    "type": "doc",
    "content": [
      { "type": "text", "text": "Hello " },
      { "type": "text", "marks": ["bold"], "text": "world" },
      { "type": "mention", "uid": "user123" },
      { "type": "inline_image", "file_id": "file456", "alt": "screenshot" }
    ]
  }
}
```

这不是 Markdown 或 HTML——这是**ProseMirror 风格的 JSON 文档格式**（类似 Notion/Editor.js/Apache JIRA 的富文本格式）。

**2. 消息类型扩展**

| 新类型 | 值 | 结构化 body 示例 | 产品场景 |
|--------|-----|-----------------|----------|
| 投票 | 5 | `{type: "poll", question: "...", options: [...], expires_at: "..."}` | 团队决策 |
| 位置 | 6 | `{type: "location", lat: 31.23, lng: 121.47, name: "上海中心"}` | 共享位置 |
| 联系人/名片 | 7 | `{type: "contact", uid: "user789", name: "张三"}` | 分享联系信息 |
| 代码块 | 8 | `{type: "code", language: "go", code: "fmt.Println(...)"}` | 开发者分享代码 |
| 表单/审批 | 9 | `{type: "approval", title: "...", fields: [...], status: "pending"}` | 业务流程 |
| 日历事件 | 10 | `{type: "event", title: "...", start: "...", end: "..."}` | 日程协调 |

**3. 消息内容安全清洗**

- HTML 标签剥离（`<script>`、`<iframe>` 等 XSS 向量）
- Markdown-to-safe-HTML（仅允许 safe subset: bold/italic/link/list）
- 外部链接白名单或 rel="noopener noreferrer"
- 媒体 URL 强制使用 aero-id 的 proxy 路由（`/files/dl`），禁止直接引用外部图片

### 实现影响

| 组件 | 改动 | 复杂度 |
|------|------|--------|
| `messages.body` | 新增 `body JSONB` 列，`content TEXT` 保留作为降级兼容 | 低 |
| `domain/message.go` | 定义 `MessageBody` 接口 + 各类型实现 | 中 |
| `service/message.go` | `Send` 中校验 body schema（`jsonSchema` 验证） | 中 |
| `handler/grpc/validate.go` | gRPC 侧新类型校验 | 低 |
| 全文搜索 | `body` 中各 text 节点的内容，tsvector 索引覆盖 | 中 |
| 通知 | 通知消息摘要需要根据 body type 生成（"张三发起了投票"vs"张三分享了一个位置"） | 低 |

### 为什么这值得做

1. **产品差异化。** 消息系统是红海功能。纯文本 + 附件勉强够用，但富文本 + 结构化消息是 Slack（消息卡片、Block Kit）和 Telegram（bot 键盘、inline 模式）等顶级产品的核心能力。没有这个，aero-id 永远靠不上"消息平台"的边。
2. **XSS 安全风险。** 当前 `content` 字段不加清洗存储。如果客户端渲染时信任服务端返回的 `content` 为安全内容，攻击者可以通过 `SendMessage` 注入 `<script>` 标签。结构化 body schema 天然防止这种注入——因为文本被编码为 `{type: "text", text: "..."}` 而不是原始字符串。
3. **搜索增强的基础。** 结构化 body 使得搜索可以针对特定类型（"只搜投票"、"只搜代码块"），也可以在搜索结果中展示类型特定摘要（代码高亮片段、地图缩略图）。
4. **渐进式。** 不需要一次性实现所有类型。先定义 schema 框架 + body 列，实现文本 + 富文本，然后逐个加新类型。每个新类型是一个独立 PR。

---

## 六、总结——本分析与已有 35+ 方向的核心差异

| 分析维度 | 已有七轮 35+ 方向 | **本文四个方向** |
|----------|------------------|-----------------|
| 视角 | 产品经理 / 资深架构师 | **运维治理 + 安全合规 + 平台生态** |
| 核心问题 | "用户还需要什么功能？" | **"如何让这个服务安全、可持续地运行到 100 万用户？"** |
| 覆盖面 | 前端用户体验（通知、搜索、消息） | **后端治理（数据生命周期、移动同步、集成网关、RLS、消息 schema）** |
| 工程挑战 | 新功能开发 | **增量演进 + 向后兼容 + 安全加固** |
| 时效性 | 即时可用 | **当用户量 > 10 万时必须实施** |

### 实施建议

```
按业务紧迫度排序（非技术难度）：

P1 — 移动端碎片级联（Q3 2026）
├── 增量同步 API（方向一，最小可行——一个 `sync` 端点）
├── 消息体 schema + JSONB（方向五 Phase 1——定义框架 + 文本安全清洗）
└── 存储配额校验（方向二 Phase 1——阻止超配额上传）

P2 — 平台化基础（Q4 2026）
├── Channel 适配器框架（方向三——Slack/Telegram/Email adapter）
├── 数据分层保留策略 + 归档 Job（方向二 Phase 2）
├── 多租户 RLS Phase 1（方向四——tenant 表 + 中间件，业务表不动）
└── 富文本消息（方向五 Phase 2——bold/italic/link/mention 支持）

P3 — 规模化治理（Q1 2027）
├── 多租户 RLS Phase 2（方向四——所有业务表加 tenant_id + 迁移现有数据）
├── 结构化消息类型扩展（方向五 Phase 3——投票、位置、代码块）
├── Inbound 消息集成（方向三——Telegram Bot 回复 → aero-id 消息）
└── 存储成本治理仪表盘（方向二 Phase 3——用量可视化 + 告警）
```

---

## 附录：关键代码引用

| 文件 | 与本分析的关系 |
|------|--------------|
| `internal/domain/message.go` | 方向五——`Message.Body` 需要扩展为 JSONB schema |
| `internal/domain/admin.go` | 方向四——`Role`/`UserRole` 已定义，加 `tenant_id` 即可复用 |
| `internal/service/file.go` | 方向二——`Create` 加配额校验、`Delete` 触发生命周期事件 |
| `internal/service/notification.go` | 方向三——`Handle()` 扩展为调用 channel 分发 |
| `internal/outbox/relay.go` | 方向二、三——新的事件消费者（归档 worker、channel worker） |
| `internal/hub/hub.go` | 方向一——增量同步的 SSE 补全（`last_event_id` → 同步标记） |
| `internal/handler/http/events.go` | 方向一——`replayMissed` 已有时间戳概念，可泛化为同步端点 |
| `internal/repository/db.go` | 方向四——所有 GORM row 结构体加 `TenantID` 字段 |
| `internal/middleware/auth.go` | 方向四——`Auth` 后增加 `Tenant` 中间件提取 `X-Tenant-ID` |
| `internal/pkg/ratelimit/ratelimit.go` | 方向四——限流 key 可扩展为 `tenant_id:uid` 组合 |
