# aero-id — 面向用户的即时通讯体验缺口

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全局代码扫描后，与 `docs/requirements/` 下 30+ 份分析文档及 `docs/REVIEW_*` 下 8 份审查文档进行逐主题交叉比对。定位在所有前述分析中**未被系统性覆盖**的 5 个高价值面向用户方向。
> **承诺**: 无新代码。每个方向均有精确的代码级证据，且与已有分析不重叠。

---

## 已有分析覆盖摘要

在撰写本文时，已有 ~30 份 `docs/requirements/*.md` 和 8 份 `docs/REVIEW_*.md` 覆盖了 **70+** 个扩展方向。类别如下：

| 类别 | 代表性方向 | 文档数 |
|------|-----------|--------|
| 产品功能 | 群聊、文件版本、@提及、表情反应、搜索、文件分享 | ~15 |
| 架构演进 | 多副本缓存、分布式限流、GraphQL/BFF、Schema 版本化 | ~20 |
| 运维成熟度 | SLO 框架、混沌测试、配置漂移、供应链安全 | ~15 |
| 正确性/Edge Case | 并发安全、FTS 写放大、Outbox 顺序、SSE 竞态 | ~12 |
| 安全合规 | E2EE、静态加密、证书固定、审计告警 | ~8 |
| 开发者体验 | SDK 生成、API 生命周期、OpenAPI 规范、开发者门户 | ~6 |

本文聚焦的方向全部属于 **面向最终用户的消息体验**，且在上述所有文档中**未被系统性分析**。

---

## 方向一：已读回执隐私控制（Read Receipt Privacy Control）

### 为什么需要

已读回执是即时通讯中最敏感的隐私信号之一。当前 aero-id **无条件** 向发送方暴露 `read_at` 和 `message.read` 事件——接收方无法选择关闭。这既不符合用户期望（许多用户希望"已读不回应"），也无法满足合规要求（GDPR 下的数据最小化原则）。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/message/message.go` — `Message.ReadAt` | `ReadAt *time.Time` 是 Message 模型的公开字段，没有任何权限守卫。任何人只要拥有消息 ID 就能知道对方是否已读 |
| `internal/message/service.go` — `MarkRead()` | 无条件的写路径：每次 `MarkRead` 调用，无论接收方偏好如何，都执行 UPDATE + 写入 `message.read` outbox 事件 |
| `internal/message/handler_grpc.go` — `MessageRead` RPC | gRPC handler 直接调用 `MarkRead`，无偏好检查 |
| `internal/message/handler_http.go` — `MarkRead` 端点 | HTTP 端同样 |
| `internal/message/repository.go` — `UpdateReadAt` | 直接设置 `read_at = now()` |
| `internal/outbox/dispatcher.go` — `notificationMessageRead` | 将 `message.read` 事件转化为 `message.read_receipt` 通知，推送给发送方 |
| `internal/domain/notification.go` — `NotificationPreference` | 已有 `NotificationPreference` 模型，其主键是 `(uid, notification_type)`——可以扩展但**没有 `message.read_receipt` 类型的开关**。当前只有 `message.received` / `comment.reply` 两种类型 |

当前流程：
```
接收方 MarkRead → UPDATE messages SET read_at = now()
                → Append outbox event "message.read"
                → Dispatcher 转换 → notification: "message.read_receipt"
                → SSE 推送 → 发送方实时看到已读回执
```

### 用户场景

1. **隐私至上用户**：Alice 只想在自己愿意的时候才暴露已读状态。当前她无法阻止系统在看到消息后自动发送已读回执。
2. **敏感关系管理**：Bob 收到前伴侣的消息，不想暴露"已读"事实。当前只能选择永远不打开消息——但这在 Inbox 列表中也暴露了未读状态。
3. **企业合规**：法律部门要求某些对话禁止读回执（如内部调查通知）。

### 建议范围

| 增量 | 描述 | 代码影响 | 优先级 |
|------|------|---------|--------|
| **RR-1** | **扩展 NotificationPreference**：添加 `message.read_receipt` 通知类型，默认 `enabled=true`（向后兼容）。如果用户设为 `false`，`MarkRead` 跳过 outbox 事件写入 | `internal/domain/notification.go` 加常量 + `internal/message/service.go` 加偏好查询 | **P0** |
| **RR-2** | **消息 API 屏蔽 `ReadAt`**：当已读回执关闭时，`GetMessage` / `ListMessages` 为发送方隐藏 `read_at` 字段（返回 nil）。这需要在 service 层做字段级权限过滤 | `internal/service/message.go` `GetMessage` / `ListMessages` | **P1** |
| **RR-3** | **批量关闭/开启**：设置端点 `POST /v1/preferences`，支持 `notification_type = "message.read_receipt"` | `internal/preference/` handler + service | **P1** |
| **RR-4** | **对话级覆盖**：某些用户可能希望全局开但特定对话关。考虑 `conversation_settings` 表（参见方向二） | 需方向二先落地 | **P2** |

### 工程估算

- RR-1: ~1 天（纯逻辑，无 schema 变更——`NotificationPreference` 表已按 `(uid, notification_type)` 索引）
- RR-2: ~1.5 天（service 层字段守卫 + 测试）
- RR-3: ~0.5 天（复用已有 preference handler 结构）
- **共约 3 天**

### 为什么不适用现有方案

- Block 系统（`internal/block/`）只控制用户间的可见性/通知产生，不控制已读回执
- "read_receipt"作为通知类型目前根本不存在——只暴露给发送方，不是通知中心

---

## 方向二：对话收件箱管理（Conversation Inbox Management）

### 为什么需要

当前 aero-id 的收件箱是一个平面列表——所有 1:1 对话都在同一个列表中按时间降序排列。用户无法：
- 归档不再活跃的对话（隐藏但不删除）
- 置顶重要对话
- 静音特定对话（与全局通知偏好不同）
- 隐藏不想看到的对话

这是几乎所有现代 IM（WhatsApp、Telegram、iMessage）的基础体验。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/message/message.go` — `Message` | `Message` 类型没有归档/置顶/静音状态。消息只有 `from_uid`+`to_uid`，无"对话"实体 |
| `internal/message/handler_http.go` | 当前端点：`GET /v1/conversations/:uid` 只接受一个参数 `uid`，不支持 `status=archived\|active\|pinned` |
| `internal/message/repository.go` — `ListConversation` | SQL 查询返回对话中所有消息排序。无 JOIN 过滤已归档对话 |
| `internal/service/message.go` — `Conversation()` | Service 方法直接透传 repo 结果，无收件箱状态管理 |
| `internal/handler/http/router.go` | 路由注册中无 `/v1/conversations/:uid/settings` 或类似端点 |
| `migrations/` (全部 22 个) | 没有任何 `conversation_settings` 或 `conversation_status` 表 |

**注意**：`REVIEW_2026-06-30_PRODUCT_GAPS.md` 的第 226-231 行确实提出了一张 `conversation_settings` 表的概念，但那只是该文档中 30+ 个 gap 中的一个 5 行提及，**未被作为独立方向系统性分析**。方向上二、三、四、五也零覆盖。

### 用户场景

1. **收件箱归零**：Alice 有 50 个对话，其中 40 个已结束。她想归档它们，只保留 10 个活跃对话在视图中。
2. **置顶重要对话**：Bob 每天和团队负责人沟通，希望该对话始终在收件箱最顶部。
3. **静音喧嚣群聊**（支持未来群聊功能）：Charlie 加入了一个大群但不希望每次消息都收到通知——只想手动查看。
4. **隐藏历史对话**：Diana 结束了与某个用户的所有沟通，不想再看到该对话出现，但保留数据以防未来需要。

### 建议范围

| 增量 | 描述 | 代码影响 | 优先级 |
|------|------|---------|--------|
| **IM-1** | **新增 `conversation_settings` 表**：`(uid, conversation_partner_uid)` 复合主键 + `archived_at TIMESTAMPTZ` + `pinned_at TIMESTAMPTZ` + `muted_at TIMESTAMPTZ` + `deleted_at TIMESTAMPTZ` | `migrations/000023_conversation_settings.up.sql` + `internal/repository/conversation_settings.go` | **P0** |
| **IM-2** | **ListConversations 支持过滤**：`GET /v1/conversations?filter=active|archived|pinned`，JOIN `conversation_settings` 做过滤和排序。`pinned` 对话始终显示在列表顶部 | `internal/service/message.go` `ListConversations` | **P0** |
| **IM-3** | **Upsert settings 端点**：`PATCH /v1/conversations/:uid/settings` 支持 `{archived: true}`、`{pinned: true}`、`{muted: true}`、`{deleted: true}` | `internal/message/handler_http.go` + `handler_grpc.go` + proto | **P1** |
| **IM-4** | **Webhook/Outbox 事件**：对话归档/取消归档、置顶/取消置顶应作为审计事件写入 outbox | `internal/outbox/chain.go` 事件类型扩展 | **P1** |
| **IM-5** | **静音实现**：`muted` 对话的消息不触发 `message.received` 通知（Dispatcer 中过滤） | `internal/notification/dispatcher.go` 添加静音检查 | **P1** |

### 工程估算

- IM-1: ~0.5 天（迁移 + repository）
- IM-2: ~1 天（service 逻辑 + 查询修改 + 测试）
- IM-3: ~1 天（HTTP + gRPC handler + 测试）
- IM-4: ~0.5 天（审计事件注册）
- IM-5: ~0.5 天（dispatcher 新增一次 `conversation_settings` 查询）
- **共约 3.5 天**

### 数据模型提案

```sql
CREATE TABLE IF NOT EXISTS conversation_settings (
    uid                     TEXT NOT NULL,
    peer_uid                TEXT NOT NULL,
    archived_at             TIMESTAMPTZ,         -- non-nil => hidden from active inbox
    pinned_at               TIMESTAMPTZ,         -- non-nil => sorted to top
    muted_until             TIMESTAMPTZ,         -- non-nil & future => muted
    deleted_at              TIMESTAMPTZ,         -- soft-delete for "hide forever"
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (uid, peer_uid)
);
```

---

## 方向三：快捷回复与消息模板（Canned Replies / Message Templates）

### 为什么需要

消息模板允许用户预写常用回复，在对话中快速选择发送。这是效率工具——对于客服场景尤其重要（Telegram 的"Quick Reply"、Slack 的"Snippets"、Discord 的"Reply Templates"）。当前用户每一次发消息都要手动输入相同的常用回复。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/message.go` — `Message` | 只有 `Content` / `FileID` / `Kind` 等字段，无模板 ID 或模板引用 |
| `internal/service/message.go` — `SendMessageInput` | 输入 DTO 无 `TemplateID` 或 `IsTemplate` 标记 |
| `internal/message/repository.go` — `Create` | 插入操作完全面向已发送消息，不支持"保存为模板" |
| `internal/handler/http/message.go` | 无 `/v1/templates` 端点群 |
| `internal/handler/grpc/aeropb/aero_id.proto` | gRPC service 定义中无 `MessageTemplate` 相关 RPC |
| `migrations/` (全部 22 个) | 没有任何 `message_templates` 表 |
| `docs/DEVELOPER_MANUAL.md` | 无模板功能描述 |

**重要区分**：已有文档中提到"模板"一词的上下文：
- `code-level-implementation-gaps.md` → 邮件通知模板引擎（完全不同的领域——SMTP 邮件格式）
- `deep-extension-analysis.md` → 审计告警规则模板（系统级规则，非用户生成内容）
- `product-directions.md` → 活动流摘要模板（`"{actor} uploaded {subject}"` 无用户交互）

**从未出现**：用户可创建、编辑、选择和发送的"快捷回复"功能。

### 用户场景

1. **客服场景**：客服 Alice 需要经常回复"我们会尽快处理您的问题，预计 24 小时内回复"。她可以保存为模板，一键发送。
2. **标准回复**：项目经理 Bob 每周末发送"请提交本周工作总结"。他可以为这个创建模板，下周直接使用。
3. **团队一致性**：团队共享模板确保对外沟通的口径一致。

### 建议范围

| 增量 | 描述 | 代码影响 | 优先级 |
|------|------|---------|--------|
| **CR-1** | **新增 `message_templates` 表**：`(uid, id)` + `title TEXT` + `content TEXT` + `shortcut TEXT`（可选，用于 `/` 快速调出） + `created_at/updated_at` | `migrations/000024_message_templates.up.sql` + `internal/repository/template.go` | **P0** |
| **CR-2** | **CRUD 端点**：`POST /v1/templates`、`GET /v1/templates`、`PATCH /v1/templates/:id`、`DELETE /v1/templates/:id` | `internal/template/` handler + service + gRPC | **P0** |
| **CR-3** | **发送时引用模板**：`POST /v1/messages` 可选支持 `template_id`，发送后自动填充 content。可选扩展：`POST /v1/messages/template/:id/send` | `internal/service/message.go` Send 方法扩展 | **P1** |
| **CR-4** | **快捷指令**：用户在消息输入框输入 `/` 触发模板列表，选择后自动填充。这属于客户端功能，服务端只需提供列表 API | 客户端实现 | **P2** |
| **CR-5** | **团队共享模板**（未来）：引入 `shared_templates` 表，通过组织/角色共享 | 需方向 "多租户/组织" 先落地 | **P3** |

### 工程估算

- CR-1: ~0.5 天（迁移 + repository）
- CR-2: ~1 天（完整 CRUD handler + 测试）
- CR-3: ~0.5 天（send 扩展）
- 不含客户端实现
- **共约 2 天**

### 数据模型提案

```sql
CREATE TABLE IF NOT EXISTS message_templates (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uid         TEXT NOT NULL,
    title       TEXT NOT NULL,
    content     TEXT NOT NULL,
    shortcut    TEXT,                  -- e.g. "greeting", "hours"
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_templates_uid ON message_templates(uid);
```

---

## 方向四：限定域搜索与结构化搜索表达式（Scoped & Fielded Search）

### 为什么需要

当前 aero-id 的搜索系统（`internal/search/`）支持跨实体的全文搜索——用户在搜索框输入一个关键词，系统从 `messages`、`comments`、`files` 三个表中查找匹配项。但是搜索是**全量无范围的**：

- 不能在**单个对话内**搜索消息
- 不能按**发送者**过滤  
- 不能按**日期范围**过滤
- 不能搜索**仅包含文件的消息**或**仅文本消息**
- 不能组合多个条件

这些是用户日常使用搜索的基本期望——与 Google 搜索的 `site:`、`filetype:` 运算符同理。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/search/search.go` — `Service` | `Search(ctx, actor, query, limit)` 接口只有 `query`（关键词字符串）和 `limit`（结果数）。无其他过滤器 |
| `internal/search/handler_http.go` — `Search` | HTTP handler 只解析 `q` 和 `limit` 参数。`?q=hello&limit=20` |
| `internal/search/handler_grpc.go` — `Search` RPC | gRPC proto 中 `SearchRequest` 只有 `query`、`limit`、`cursor` |
| `internal/search/service_test.go` | 测试中搜索只有关键词测试，没有带过滤条件的测试用例 |
| `internal/repository/comment.go` — `SearchComments` | SQL 过滤条件仅为 `owner_uid = ? AND search_vector @@ plainto_tsquery('simple', ?)`——只能搜 own 的评论 |
| `internal/repository/message.go` — `SearchMessages` | SQL 过滤条件仅为 `(from_uid = ? OR to_uid = ?) AND search_vector @@ ...`——只能搜参与的消息 |
| `internal/repository/file.go` — `SearchFiles` | 同上，只能搜 own 的文件 |
| `api/proto/aero_id/v1/aero_id.proto` — `SearchRequest` | proto 中仅有 `query`（string）和 `limit`（int32）以及 `cursor`（string） |

**相关但不同**：`REVIEW_2026-06-30_FLOW_CONCURRENCY_CACHE.md` 第 524 行提到 `GET /v1/search?query=hello&with_uid=abc123` 作为单行示例。这不是系统性分析——没有讨论过滤器语法、解析器设计、索引策略。

### 用户场景

1. **对话内搜索**：Alice 与 Bob 的对话已有 2000 条消息，她想找到 Bob 上周发的"合同"消息 → `in:uid-bob contract` 或 `?in=uid-bob&q=contract`
2. **按发送者搜索**：Charlie 记得消息来自 David，但不记得关键词 → `from:uid-david` 看到所有 David 的消息
3. **按日期范围搜索**：Eve 想找 6 月份所有包含"会议"的消息 → `since:2026-06-01 until:2026-06-30 会议`
4. **按类型过滤**：Frank 想找所有对话中分享过的文件（不含文本消息） → `has:file` 或 `kind:messages`

### 建议范围

| 增量 | 描述 | 代码影响 | 优先级 |
|------|------|---------|--------|
| **FS-1** | **扩展 `SearchRequest` proto**：在 `api/proto/aero_id/v1/aero_id.proto` 中添加可选字段 `from_uid`、`with_uid`、`date_from`、`date_to`、`kind`（messages\|comments\|files）、`has_file`、`has_attachment` | proto 文件 + 重新生成 | **P0** |
| **FS-2** | **HTTP 查询参数**：`GET /v1/search?q=hello&from=uid&in=uid2&since=2026-06-01&until=2026-06-30&kind=messages` | `internal/search/handler_http.go` | **P0** |
| **FS-3** | **Service 层条件构建**：`SearchInput` 结构体增加可选字段，`Search()` 方法根据输入动态构建带 WHERE 子句的 SQL | `internal/search/search.go` + `internal/repository/*` | **P1** |
| **FS-4** | **搜索语法解析器**（高级）：支持类似 Gmail 的 `from:uid content:"exact phrase"` 语法。包括词法分析 + 语法树 + SQL 生成 | 新增 `internal/search/parser/` | **P2** |
| **FS-5** | **分面搜索**（Faceted Search）：返回每个过滤维度的计数，帮助用户缩小范围 | 新增 `facet_count` SQL | **P2** |

### 工程估算

- FS-1: ~0.5 天（proto 修改）
- FS-2+3: ~2 天（handler + service + repository + 测试）
- FS-4: ~3 天（语法解析器 + 测试，可独立推进）
- FS-5: ~1 天（facet 查询 + 测试）
- **共约 6.5 天**（FS-4 可拆分）

### 搜索语法提案

```
简单的结构化参数方案（FS-1/2/3）:
  GET /v1/search?q=contract&from=uid-abc&in=uid-def&since=2026-06-01&until=2026-06-30&kind=messages&has=file

高级自然语言语法方案（FS-4）:
  query        → expression ["AND"] expression
  expression   → keyword | field:value | "quoted phrase"
  field        → "from" | "in" | "to" | "since" | "until" | "kind" | "has"
  keyword      → [a-zA-Z0-9_\-\u4e00-\u9fff]+

  示例: from:uid-abc "project update" kind:messages since:2026-06-01
```

---

## 方向五：未读对话聚合与已读状态同步（Unread Conversation Aggregation）

### 为什么需要

当前每个消息有独立的 `ReadAt` 字段。系统可以回答"某条消息是否已读"，但**无法回答以下用户体验最基本的问题**：

- 当前用户有多少个未读对话？
- 每个对话有多少条未读消息？
- 如何将整个对话重置为"已读"？
- 用户在设备 A 上已读，设备 B 如何同步知道？

这些缺失直接导致：
- 无法在收件箱列表显示未读计数
- 无法从收件箱界面做"全部标为已读"
- 无法支持多设备之间同步已读状态
- 重启或重新登录后丢失所有未读状态（因为未读状态是隐含的——`ReadAt < createdAt` 的消息就是"未读"）

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/message/message.go` — `Message.ReadAt` | 每条消息独立记录 `ReadAt`。无聚合视图 |
| `internal/message/repository.go` — `ListInbox` | 返回所有消息列表，无 `unread_count` 聚合字段 |
| `internal/message/handler_http.go` — `Inbox` | 返回 `{messages: [...], next_cursor}`，无 `unread_count`、`has_more` |
| `internal/message/repository.go` — `MarkRead` | 操作粒度是单条消息：`UPDATE messages SET read_at = now() WHERE id = ?`。无批量标为已读 |
| `internal/service/message.go` | 无 `MarkConversationRead`、`UnreadCount`、`UnreadConversations` 方法 |
| `internal/handler/http/router.go` | 无 `/v1/conversations/unread-count`、`/v1/conversations/read-all` 端点 |
| `internal/domain/message.go` | 领域模型中无 `UnreadConversation` 值对象 |
| `internal/sso/user_cache.go` | 用户缓存只缓存 SSO 标识，不缓存未读状态 |

**注意**：`expansion-directions.md` 第 48-53 行在群聊上下文中提到了 `conversation_members.last_read_at`——但那是在群聊架构中作为成员元数据讨论，不是针对 1:1 消息的未读聚合功能。方向一、二、三、四同样零覆盖。

### 用户场景

1. **收件箱未读徽标**：Alice 打开应用，看到应用图标右上角"5"——表示有 5 个未读对话。她需要知道不需要逐个打开对话计数。
2. **批量标为已读**：Bob 在网页端阅读了所有消息，但在手机端仍然是"未读"——因为消息是逐条标记的，且跨设备不同步。
3. **对话列表未读计数**：Charlie 的收件箱按时间显示对话，每个对话旁边显示"3"——表示该对话中有 3 条未读消息。
4. **跨设备同步**：Diana 在手机上读了所有消息。当她打开桌面端应用时，未读计数应该已更新为零，无需在桌面端重复标记。

### 建议范围

| 增量 | 描述 | 代码影响 | 优先级 |
|------|------|---------|--------|
| **UC-1** | **`UnreadCount` 查询**：新增 `SELECT COUNT(DISTINCT ...)` 查询按对话聚合未读消息数。端点 `GET /v1/conversations/unread?detail=true` 返回总数 + 每个对话的未读数 | `internal/message/repository.go` + handler | **P0** |
| **UC-2** | **`MarkConversationRead` 批量操作**：`POST /v1/conversations/:uid/read` → `UPDATE messages SET read_at = now() WHERE from_uid = :uid AND to_uid = :actor AND read_at IS NULL`。仍在事务中写入 outbox `message.read` 事件（每条消息一个？效率考量见下） | `internal/message/service.go` + handler | **P0** |
| **UC-3** | **高效批量已读确认**：批量 `MarkConversationRead` 不应为 N 条消息写入 N 个 outbox 事件。应引入 `conversation.read` 事件类型（单条事件，标记该对话已确认阅读到某个 `until` 时间戳），替代逐条 `message.read` 事件 | `internal/outbox/` 新增事件类型 + `internal/notification/dispatcher.go` 处理 | **P1** |
| **UC-4** | **`conversation_read_cursors` 表**：持久化每个对话的"已读直到"光标。`(uid, peer_uid, read_cursor_message_id, read_cursor_created_at)`。这使应用启动时无需扫描全表消息即可计算未读数 | `migrations/000025_conversation_read_cursors.up.sql` + `internal/repository/` | **P1** |
| **UC-5** | **SSE 推送未读更新**：当同一个用户在其他设备上标记已读时，通过 SSE 推送「未读计数已更新」事件，实现跨设备同步 | `internal/hub/hub.go` + `internal/notification/dispatcher.go` | **P2** |

### 工程估算

- UC-1: ~1 天（新查询 + handler + 测试）
- UC-2: ~0.5 天（batch UPDATE + handler）
- UC-3: ~1 天（新事件类型 + dispatcher 逻辑 + 测试）
- UC-4: ~1 天（新表 + repository + 读光标迁移逻辑）
- UC-5: ~1 天（SSE 事件 + 推送逻辑）
- **共约 4.5 天**

### 关键设计决策

**批量已读 vs 逐条已读**：
- 当前：逐条 `message.read` → 发送方收到 N 条 read_receipt 通知（N 条消息）。如果 N=50，用户收到 50 条通知，体验糟糕。
- 建议：引入 `conversation.read` 事件，payload 包含 `{conversation_with: "uid", read_until_id: "msg-uuidv7", read_until_at: "timestamp"}`。发送方收到一条通知："Alice 已阅读了所有消息直到 timestamp"。

**读光标（Read Cursor）**：
- 读光标表 `(uid, peer_uid, read_cursor_id, read_cursor_at)` 使得未读计数查询变为 O(1) 而不是 O(N)：
  ```sql
  -- O(1) 未读计数
  SELECT COUNT(*) FROM messages
  WHERE from_uid = $1 AND to_uid = $2
    AND created_at > (SELECT read_cursor_at FROM conversation_read_cursors WHERE uid = $2 AND peer_uid = $1);
  ```
- 不需要扫描全部未读消息，只需要与游标时间戳比较

---

## 交叉相关性矩阵

五个方向在架构上高度协同，部分之间存在依赖关系：

```
                     ┌──────────────────┐
                     │  方向二: 收件箱管理 │
                     │ (Archive/Pin/Mute)│
                     └────┬──────┬──────┘
                          │      │
            ┌─────────────┘      └─────────────┐
            ▼                                  ▼
   ┌──────────────────┐             ┌────────────────────┐
   │ 方向一: 已读回执  │◄────────────│  方向五: 未读聚合   │
   │ (隐私控制)       │             │ (Cursors & Counts) │
   └──────────────────┘             └─────────┬──────────┘
                                              │
                                    ┌─────────┘
                                    ▼
                           ┌──────────────────┐
                           │  方向四: 限定搜索 │
                           │ (Scoped Search)  │
                           └──────────────────┘
```

- **方向一 + 方向五**：当已读回执关闭时，未读计数应当向发送方隐藏"对方是否已读"的信息。方向一的 RR-2（隐藏 `ReadAt`）和方向五的 UC-3（`conversation.read` 事件）需要协调：已读回执关闭时不应产生 `conversation.read` 事件。
- **方向二 + 方向五**：置顶对话（方向二）和未读计数（方向五）在收件箱响应中需要一起返回。置顶对话始终有未读计数的显示。
- **方向四 + 全部**：限定域搜索的方向四可以为方向二的"归档对话"、方向五的"仅未读"提供搜索范围的过滤。

---

## 总结优先级

| 方向 | 用户影响 | 工程成本 | 基础依赖 | 建议推进次序 |
|------|---------|---------|---------|------------|
| 方向二：收件箱管理 | ⭐⭐⭐⭐⭐ | ~3.5 天 | 无 | **第 1 个** |
| 方向五：未读聚合 | ⭐⭐⭐⭐⭐ | ~4.5 天 | 方向二的部分设计 | **第 2 个** |
| 方向一：已读回执隐私 | ⭐⭐⭐⭐ | ~3 天 | 无 | **第 3 个** |
| 方向四：限定域搜索 | ⭐⭐⭐⭐ | ~3.5 天 (不含解析器) | 无 | **第 4 个** |
| 方向三：快捷回复 | ⭐⭐⭐ | ~2 天 | 无 | **第 5 个** |

**总体工程估算**: ~17 天（5 个方向，不含重叠收益）
**重叠收益**: 方向二和方向五共享 `conversation_settings` 的设计决策，合并推进可节省 ~1 天。
