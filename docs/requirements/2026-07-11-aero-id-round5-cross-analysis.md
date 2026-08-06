# aero-id — 第五轮交叉分析：未被覆盖的高价值方向

> **日期**: 2026-07-10
> **视角**: 资深架构师 + 产品经理
> **方法**: 全文扫描 `docs/requirements/` 下 9 份文档的所有方向标题（累计 50+ 方向），逐条交叉比对，
>   再全量扫描 `internal/` 代码验证代码级缺口。定位**在所有现有文档中零覆盖或仅有技术性提及**的方向。
> **前置条件**: `feat/roadmap` 已合并，38 个 gRPC RPC + 全 HTTP 端点对等 + 生产加固已交付
> **无代码变更**

---

## 交叉分析覆盖

已有 9 份 `docs/requirements/*.md` + 8 份 `docs/REVIEW_*.md` 共计 17 份审查文档，累计覆盖 **50+** 扩展方向：

| 文档 | 覆盖方向 |
|------|---------|
| `architecture-extensions.md` | 群聊/频道、统一活动流、内容生命周期策略、协作审核工作台、文件预览管线 |
| `edge-cases-and-performance.md` | 分布式限流、多地域部署、Webhook 事件平台、Schema 版本化、19 项 Edge Case |
| `product-directions.md` | 文件版本管理、活动流时间线、定时动作引擎、自适应限流、数据平台/嵌入式分析 |
| `untapped-directions.md` | 移动推送网关、SDK/开发者平台、OAuth 2.0、数据导入迁移、多区域部署 |
| `expansion-directions.md` | 群组对话、用户 Webhook、多副本扩展、WebSocket、存储配额 |
| `remaining-high-value-directions.md` | 会话线程与消息组织、记录管理与合规治理 |
| `final-blindspots.md` | 文件存储加密、文件分享权限细化、API 生命周期管理、异步管线可观测、供应链安全 |
| `deep-extension-analysis.md` | 多表情反应、会话模型统一、自动内容安全、文件预处理管线、审计告警/异常检测 |
| `architecture-migration-and-systemic-gaps.md` | 领域驱动重构、跨域关注点治理、依赖注入框架、统一错误处理 |

---

## 核心发现

尽管 50+ 方向覆盖了广泛的产品和架构领域，以下 **5 个方向在所有现有文档中零覆盖或仅有最小提及**。
每个方向均附代码级证据说明"为什么代码已暗示此能力存在，但从未建立为功能"。

---

## 方向一：内容草稿与自动保存系统（Content Draft & Auto-Save）

### 为什么它被遗漏

所有现有文档关注的是"已提交数据"的操作——创建、读取、更新、删除、搜索、分享。
无人思考"正在创建但未提交的数据"的管理。这在整个 17 份文档中**零覆盖**。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/service/message.go` — `Send()` | 要求 `content` 或 `file_id` 非空。无"草稿"参数，用户输入中途离开则内容丢失 |
| `internal/service/comment.go` — `Create()` | 同上。长评论编辑中浏览器崩溃 → 所有文本丢失 |
| `internal/service/file.go` — `Create()` | 文件上传仅支持 multipart 完整上传或预签名直传。无"分片上传 + 暂存"能力 |
| `internal/handler/http/events.go` — SSE | 实时事件推送存在，但无"暂存草稿同步"事件类型——不能在多设备间同步未提交的内容 |
| `internal/domain/message.go` | Message 模型仅有 `Content` 字段，无 `DraftStatus`/`ScheduledAt`/`ExpiresAt` |
| `configs/config.yaml` | 无任何 draft 相关配置节 |
| `internal/repository/message.go` | `Create` 方法无 draft flag |

### 用户场景

1. **长消息/长评论意外丢失**：用户在消息编辑器输入了 500 字的评论，浏览器标签页意外关闭 → 内容全部丢失。无自动保存草稿。
2. **分阶段消息撰写**：用户想先写一个消息草稿，稍后再审阅和发送。当前消息一旦创建就会发送出去（或至少出现在对方 inbox 中）。
3. **多设备草稿同步**：用户在手机端开始写一条带附件的消息，想转移到桌面端完成。没有草稿同步机制。
4. **定时发送**：用户想在未来某个时间自动发送消息（"明天上午 9 点发送"）。当前完全不支持——这是 `product-directions.md` 中"定时动作引擎"的子集，但即使动作引擎也不包含消息草稿暂存。

### 架构设计要点

#### 存储层

```sql
-- 统一草稿表，适用于所有实体类型
CREATE TABLE drafts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_uid   TEXT NOT NULL,
    draft_type  TEXT NOT NULL,        -- 'message' | 'comment' | 'file_metadata'
    subject_id  UUID,                 -- 可选：编辑已有资源时的资源 ID（编辑消息草稿）
    content     JSONB NOT NULL,       -- 完整的草稿内容（text、meta、file_refs 等）
    version     INT NOT NULL DEFAULT 1,  -- 乐观锁，防并发覆盖
    expires_at  TIMESTAMPTZ,          -- 草稿过期自动清理（默认 7 天）
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE(owner_uid, draft_type, COALESCE(subject_id, '00000000-0000-0000-0000-000000000000'))
);
```

#### 自动保存机制

- **客户端驱动**：前端每 N 秒（如 5s）或在内容变化时自动调 `PUT /v1/drafts?type=message&subject_id=xxx`
- **服务端合并**：服务端保留最新版本，支持 `version` 乐观锁防止旧版本覆盖新版本
- **过期清理**：`drafts.expires_at` 默认 7 天，周期性清理（可复用已有的 `idemStore.RunCleaner` 模式）

### 与现有基础设施的整合

| 组件 | 复用方式 |
|------|---------|
| `idempotency.Store` | 草稿保存的幂等性——客户端可用 `Idempotency-Key` 防重复保存 |
| `ratelimit.Limiter` | 草稿操作也受限流保护——防止暴力遍历草稿 ID |
| `repository.NotificationRepository` | 草稿过期前可发送"您有未完成的草稿"提醒通知 |
| `hub.SSE` | 草稿同步推送——A 设备保存草稿 → SSE 推送给 B 设备 |
| GC sweeper（`storage/gc/sweeper.go`） | 到期草稿的清理可复用 sweeper 的定时模式 |
| `outbox.Event` | 草稿操作可选择性地写入 outbox 用于审计 |

### 实施成本与影响

| 维度 | 评估 |
|------|------|
| 工程成本 | **低-中**。新增 `drafts` 表 + CRUD endpoints（HTTP + gRPC）+ 自动清理。不涉及复杂的搜索/通知/权限逻辑 |
| 用户影响 | **高**。所有写操作的用户体验显著提升。意外关闭标签页时不再丢失内容 |
| 架构风险 | **低**。独立的存储表 + 独立的 service/handler，不触及现有数据流 |
| 前提条件 | 无。现有基础设施全部可选（非必要） |

---

## 方向二：@提及与用户标记系统（@Mentions & User Tagging）

### 为什么它被遗漏

现有文档仅在 `architecture-extensions.md` 群聊通知级别中提到 `mentions` 一词（"仅 @提及"模式），但从未将 @提及本身作为一个独立的产品方向来设计。**没有文档探讨 @提及的用户体验、存储模型、通知行为、权限检查、自动补全**。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/message.go` — `Message` | 无 `Mentions []string` 字段。当前消息内容中的 `@user` 只是纯文本，无语义 |
| `internal/domain/comment.go` — `Comment` | 同上 |
| `internal/domain/notification.go` — `NotifTypeCommentReply` | 仅回复触发通知，@提及不触发通知 |
| `internal/notification/dispatcher.go` | 通知类型只有 `message.sent`、`message.read`、`comment.replied`。无 `mention` 通知类型 |
| `internal/service/service.go` — `SendMessageInput` | 无 `Mentions` 字段 |
| `internal/service/comment.go` — `CreateCommentInput` | 同上 |
| `internal/repository/message.go` | `Create` 方法无提及解析逻辑 |
| `internal/sso/client.go` — `GetUsers` | SSO 客户端已支持批量 uid 解析（`GetUsers`），可直接用于提及用户的身份解析 |
| `internal/service/service.go` — `UserDirectory` | 同一 `GetUsers` 接口可用于在输入时验证被提及的用户是否存在 |

### 用户场景

1. **在评论/消息中 @提及用户**：用户在评论中输入 `@张三`，张三收到专门的 `mention` 通知，而非通用的 `comment.reply` 通知
2. **提及自动补全**：前端输入 `@` 时触发后端搜索建议 `GET /v1/users/suggest?q=zhang`（需要 SSO 支持用户搜索）
3. **提及权限控制**：在群聊/频道中，只有频道成员可以被 @提及；在文件评论中，只有文件可读用户可以被 @提及
4. **提及汇总**：独立端点 `GET /v1/mentions?since=...` 查看所有提到自己的内容，不受通知 MarkRead 影响

### 架构设计要点

#### 存储层

```sql
-- 提及索引表：从被提及者的角度记录所有提及
-- 查询 "谁提到过我" 直接从这里索引，无需全文搜索
CREATE TABLE mentions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    mentioned_uid   TEXT NOT NULL,            -- 被提及的用户
    actor_uid       TEXT NOT NULL,            -- 谁提及的
    subject_type    TEXT NOT NULL,            -- 'message' | 'comment'
    subject_id      UUID NOT NULL,            -- 被提及所在的消息/评论 ID
    mention_text    TEXT,                     -- @提及的原文片段（"@张三"）
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_mentions_mentioned ON mentions(mentioned_uid, created_at DESC);
```

#### 提及解析

- 解析时机：在 `Create` / `Edit` 消息或评论时，服务层解析 `content` 中的 `@` 模式
- 解析方式：正则匹配 `@([^\s,.;:!?，。；：！？]+)` → 调用 `UserDirectory.GetUsers` 验证存在 → 写入 `mentions` 表
- 通知触发：通过 outbox 写入 `mention.created` 事件 → NotificationDispatcher 创建通知 → SSE 推送

#### 与现有通知系统的关系

提及通知与现有通知（`comment.reply`、`message.received`）的关系应是**正交且可叠加**：

- 用户创建评论 → 触发 `comment.reply` 通知给父评论作者（现有行为）
- 如果评论中 @提及了 张三 → 额外触发 `mention` 通知给张三（新增行为）
- 被 @提及的用户收到的通知类型为 `mention`，便于偏好设置中单独控制

### 实施成本与影响

| 维度 | 评估 |
|------|------|
| 工程成本 | **中**。新增 `mentions` 表 + 内容解析逻辑 + 通知类型扩展 + 自动补全端点（依赖 SSO）。不复杂但涉及多处修改 |
| 用户影响 | **高**。协作场景的核心体验——"被提及时收到准确通知"是所有协作平台的基础设施 |
| 架构风险 | **低-中**。新表和通知类型扩展不影响现有数据模型。提及解析需要在写路径增加一个步骤，需注意性能 |
| 前提条件 | `sso.UserDirectory.GetUsers` 已存在；如果还需要用户搜索建议，SSO 需要新增 `SearchUsers` RPC |

---

## 方向三：内容引用与转发（Content Quoting & Forwarding）

### 为什么它被遗漏

现有文档中，"转发"仅以安全角度出现（`final-blindspots.md` 中"防转发水印"），"引用"从未被讨论。
消息与消息之间、评论与评论之间的内容引用关系，在所有 17 份文档中**零覆盖**。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/message.go` — `Message` | 无 `QuotedMessageID` 或 `ForwardedFromID` 字段。消息无法引用其他消息 |
| `internal/domain/comment.go` — `Comment` | 无 `QuotedContent` 字段。评论无法引用其他评论的内容 |
| `internal/service/message.go` — `SendMessageInput` | 输入 DTO 无引用/转发支持 |
| `internal/service/message.go` — `Send()` | 无 `quoted_message_id` 参数。无法将引用消息包含在发送请求中 |
| `internal/handler/grpc/aeropb/aero_id.proto` | 需检查 proto 是否有 `quoted_message` 或 `forwarded_from` 字段 |
| `internal/handler/grpc/message.go` | gRPC handler 无引用/转发 RPC |
| `internal/handler/http/message.go` | HTTP handler 无引用/转发端点 |
| `internal/repository/message.go` | `Create` 方法无引用字段 |

### 用户场景

1. **消息引用回复**：用户在对话中回复某条消息时，自动引用原文。类似 Telegram 的"回复"功能——在输入框上方显示被引用的消息片段
2. **跨对话转发**：用户将一条消息（附带上下文）转发给另一个用户或对话。转发消息显示"转自 ..."的源头信息
3. **评论引用**：用户在回复评论时引用被回复评论的内容片段，尤其在楼中楼场景中帮助上下文理解
4. **文件上下文引用**：在消息/评论中引用某个文件的特定版本或片段

### 架构设计要点

#### 存储模型

```sql
-- 消息模型扩展：被引用的消息信息作为 JSONB 存储在 messages.meta 中
-- 不需要新建表——messages.meta 已有 JSONB 字段

-- messages.meta 扩展示例：
{
  "quoted_message": {
    "id": "uuid-of-original-message",
    "from_uid": "sender-uid",
    "content_snippet": "原文的前 100 字...",
    "created_at": "2026-07-10T00:00:00Z"
  },
  "forwarded_from": {
    "original_message_id": "uuid",
    "original_conversation_uid": "other-user-uid",
    "forwarded_by": "forwarder-uid",
    "forwarded_at": "2026-07-10T00:00:00Z"
  }
}
```

不使用外键 `quoted_message_id` 的原因：
- 被引用的消息可能被软删除，删除引用消息不应级联删除引用方
- 引用关系不需要强制一致性——引用是一条消息的"元数据"，而非关系
- JSONB 存储在 `meta` 中已足够，无需额外表

#### 引用权限

- **引用可见性**：引用者只能引用自己有权限查看的消息/评论。`canReadMessage` / `canReadFile` 检查
- **引用后删除**：被引用的消息被删除后，引用方显示"原文已被删除"而非断开引用
- **转发控制**：文件分享已支持 ACL，但消息转发需要额外的隐私考虑——`forwarded_from` 是否包含原始发送者的 uid？

### 实施成本与影响

| 维度 | 评估 |
|------|------|
| 工程成本 | **低**。纯前端 + 后端 `meta` 扩展。不需要新表、新迁移。只需在 service 层验证引用消息的存在性和可见性 |
| 用户影响 | **中-高**。显著改善消息对话的上下文理解。但现有 1:1 消息格式可能使引用 UI 受限 |
| 架构风险 | **低**。仅扩展已有 `meta` JSONB 字段。不破坏现有 API |
| 前提条件 | 需要先实现/规划群聊（方向 1），因为引用的最大价值在群聊场景中体现。但 1:1 对话也已受益 |

---

## 方向四：通用批量操作 API（General-Purpose Batch Operations）

### 为什么它被遗漏

现有文档在审核工作台（`architecture-extensions.md`）和内容制裁（`deep-extension-analysis.md`）中提到
了管理员的"批量操作"需求，但**从未将批量操作 API 作为面向所有用户的通用能力来设计**。
用户不能批量删除文件、批量标记消息已读、批量分享文件、批量取消分享。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/service/file.go` — `FileService` | 所有操作都是单实体：`Get`、`Delete`、`ShareFile`、`UpdateMetadata` |
| `internal/service/message.go` — `MessageService` | 只有 `MarkConversationRead` 是"批量"（针对一个对话），无通用 `BatchMarkRead` |
| `internal/service/comment.go` — `CommentService` | 无批量操作。不能批量删除、批量隐藏评论 |
| `internal/handler/http/file.go` — `Routes` | 无 `POST /v1/files/batch-delete` 或 `POST /v1/files/batch-share` |
| `internal/repository/file.go` | 所有 repo 方法均为单实体操作 |
| `internal/repository/message.go` — `MarkConversationRead` | 这是唯一一个有批量语义的 repo 方法，"批量"的概念只存在于"一个对话" |
| `internal/handler/http/router.go` | 中间件链（auth + rate limit + idempotency）按请求级别应用。批量请求中的部分失败处理不存在 |

### 用户场景

1. **批量删除文件**：用户整理存储空间时，选中 20 个文件 → "批量删除" → 一个请求删除所有选中文件
2. **批量分享/取消分享**：用户将 10 个文件同时分享给同一个协作者
3. **批量标记已读**：用户清空所有未读通知/消息
4. **批量编辑元数据**：用户将 50 个文件的 visibility 从 private 改为 public
5. **管理员批量操作**（补充现有方向）：管理员批量隐藏/删除违反政策的评论或文件

### 架构设计要点

#### API 设计

```http
POST /v1/files/batch-delete
Content-Type: application/json
Idempotency-Key: batch-abc-123

{
  "ids": ["uuid1", "uuid2", ...]
}

Response 200:
{
  "results": [
    {"id": "uuid1", "status": "deleted"},
    {"id": "uuid2", "status": "not_found"},
    {"id": "uuid3", "status": "forbidden"},
    ...
  ],
  "total": 20,
  "succeeded": 18,
  "failed": 2
}
```

#### 部分失败处理

批量操作面临的核心挑战是**部分失败**。设计原则：

1. **非事务性**：批量操作不应是 `ALL OR NOTHING` 事务。每个子操作独立提交。部分失败不影响已成功的操作
2. **详细结果**：返回每个子操作的状态码和错误信息，使客户端可以针对性重试失败的子集
3. **幂等性**：批量操作的 `Idempotency-Key` 作用于整个请求级别，而非每个子操作。相同的 key + 相同的请求体 → 返回相同结果（但已成功的子操作不会重复执行）
4. **批量大小限制**：单次请求最多 N 个 ID（可配置，默认 100），防止请求体过大
5. **限流适配**：批量请求的速率限制按**总子操作数**计算，而非请求数（`rate_limit.RPS * batch_size`）

#### 对现有基础设施的影响

| 组件 | 变更 |
|------|------|
| `middleware.RateLimit` | 需要为批量端点设置不同的限流权重 |
| `middleware.Idempotency` | 批量操作的幂等键需要映射到所有子操作的组合结果 |
| `repository/*.go` | 新增批量操作的 repo 方法（`DeleteMany`、`ShareMany`），用 `WHERE id IN (...)` 实现 |
| `audit.AuditLogger` | 批量操作的审计日志应记录所有受影响资源的 ID 列表 |
| `outbox.Event` | 批量操作可写入单个 outbox 事件（`files.batch_deleted`），包含所有 IDs |

### 实施成本与影响

| 维度 | 评估 |
|------|------|
| 工程成本 | **中**。每个域（file/message/comment）需要新增批量 repo 方法和 service 方法。但每个批量操作本质上是循环调用单实体操作——渐进式实现 |
| 用户影响 | **中-高**。高频用户（管理大量文件的用户）影响最大。但不是新用户获取的差异化功能 |
| 架构风险 | **中**。部分失败模型增加了错误处理的复杂度。需要谨慎设计响应格式和幂等性语义 |
| 前提条件 | 无。现有基础设施全部支持。但建议在幂等性和限流就绪后实施 |

---

## 方向五：跨域内容关系遍历 API（Cross-Entity Relationship Graph API）

### 为什么它被遗漏

现有文档讨论了"统一活动流"（跨域时间线），但活动流是**时间维度的聚合**，而非**关系维度的遍历**。
aero-id 的实体之间存在天然的引用关系网络——文件被消息引用，评论附着在文件/消息上，消息包含文件，
分享授予访问权限——但没有任何 API 可以让客户端**按关系遍历这个图**。

### 代码级证据

| 位置 | 发现 |
|------|------|
| `internal/domain/file.go` — `File` | 无 `ReferencedByMessages`、`CommentsCount` 等关系字段 |
| `internal/domain/message.go` — `Message` | 有 `FileID` 指向文件，但无法从文件端查询"哪些消息引用了我" |
| `internal/domain/comment.go` — `Comment` | 有 `TargetID` 指向文件/消息，但无法从目标端查询"哪些评论附着在我身上" |
| `internal/domain/access.go` — `CanReadFile` | 不检查 `file_shares`，所以无法枚举"哪些用户对我有访问权限" |
| `internal/service/file.go` — `FileService` | `ListSharedWithMe` 是从用户角度查询，但无法从文件角度回答"这个文件分享给了谁" |
| `internal/service/message.go` — `Conversation()` | 返回两个用户之间的消息列表，但无法查询"这条消息所在的完整对话信息" |
| `internal/repository/file_share.go` | `ListSharedWithMe` 是单向的；缺少 `ListGrants(fileID)`（文件的所有授权接收者） |
| `internal/repository/comment.go` | `ListByTarget` 存在（按目标查询评论），但缺少 `CountByTarget`、`ListByOwner`（用户的所有评论） |
| `internal/handler/http/file.go` | 无 `GET /v1/files/:id/related` 端点 |
| `internal/handler/grpc/file.go` | 无 `GetRelatedContent` RPC |

### 用户场景

1. **"这个文件被用在了哪里？"**：用户在决定删除一个文件前，想知道哪些消息、评论、分享引用了这个文件
2. **"这个对话里有哪些文件？"**：用户想查看与某人的对话中所有分享过的文件列表
3. **"谁有权访问这个文件？"**：文件所有者想查看当前文件的所有授权用户列表（不仅限于分享——还包括通过群聊/频道获得的权限）
4. **"这个评论下的所有回复"**：尽管 `Replies` 端点存在，但没有"计数"信息——用户在展开前不知道有多少回复
5. **"这个用户的全部活动"**：查看特定用户的所有文件、评论、消息的汇总（管理员/审计用途）

### 架构设计要点

#### API 设计

```http
# 1. 文件的关系遍历
GET /v1/files/:id/related
→ {
  "referencing_messages": [...],  // 引用此文件的消息
  "comments": [...],              // 此文件上的评论（顶级 + count）
  "shares": [...],                // 此文件的分享授权
  "versions": [...]               // 文件版本历史（功能实现后）
}

# 2. 消息的关系遍历
GET /v1/messages/:id/related
→ {
  "replies": [...],               // 此消息上的评论
  "reactions": [...],             // 此消息的反应
  "referenced_file": {...},       // 消息引用的文件
  "quoted_messages": [...]        // 此消息引用的其他消息（方向三实现后）
}

# 3. 用户的关系遍历（需管理员/本人）
GET /v1/users/:uid/related?types=files,messages,comments
→ {
  "files": [...],                 // 用户拥有的文件（分页）
  "messages": [...],              // 用户发送/接收的消息（最近）
  "comments": [...],              // 用户发表的评论（分页）
  "notifications": [...]          // 用户的未读通知（最近）
}
```

#### 实现策略

跨域关系遍历 API 不应通过 N+1 查询实现。推荐策略：

1. **按需加载**：`/v1/files/:id/related` 每次查询所有相关域（消息、评论、分享），但每个域使用独立的、受限制的查询
2. **可选的查询参数**：`/v1/files/:id/related?include=messages,comments` 让客户端选择需要的关系类型
3. **基于已有索引的查询**：
   - 文件→消息：`SELECT ... FROM messages WHERE file_id = ?`（无需新索引）
   - 文件→评论：`SELECT ... FROM comments WHERE target_type='file' AND target_id = ?`（已有索引）
   - 文件→分享：`SELECT ... FROM file_shares WHERE file_id = ?`（已有索引）
4. **权限检查**：用户只能看到自己有权访问的关联关系。`canReadFile` / `canReadMessage` 应用于每个关联实体

### 与现有基础设施的整合

| 组件 | 复用方式 |
|------|---------|
| 已有索引 | 所有关系查询都可基于现有 GORM 索引直接查询，无需新索引 |
| `enrichUsers` | 关系结果中的 uid 需要用户信息丰富，可复用 `UserDirectory` |
| 分页 | 每种关系的列表都使用已有 `domain.Page` + cursor 分页 |
| 权限检查 | `canReadFile` / `canReadMessage` 直接复用。但需注意 `canReadFile` 当前不检查 `file_shares`——这是修复前置条件 |

### 实施成本与影响

| 维度 | 评估 |
|------|------|
| 工程成本 | **低-中**。每个关系查询不超过 5 行 SQL。但需要为每个域编写对应的 service/repo 方法。主要是"胶水代码"——没有新的业务逻辑 |
| 用户影响 | **中**。普通用户可能不直接使用此 API，但构建在它之上的 UI（文件预览页面的"上下文"面板、用户资料页面的"活动"选项卡）将显著提升 UX |
| 架构风险 | **低**。纯读操作，不修改数据。权限检查确保安全 |
| 前提条件 | 修复 `canReadFile` 不检查 `file_shares` 的问题（已在 `edge-cases-and-performance.md` 中作为 EC-15 指出） |

---

## 方向优先级与依赖关系

| 优先级 | 方向 | 用户价值 | 工程成本 | 架构风险 | 依赖项 |
|--------|------|---------|---------|---------|--------|
| **P1** | 方向一：草稿与自动保存 | ⭐⭐⭐ | 低-中 | 低 | 无 |
| **P1** | 方向二：@提及与用户标记 | ⭐⭐⭐⭐ | 中 | 低-中 | SSO 搜索能力（可选） |
| **P2** | 方向三：内容引用与转发 | ⭐⭐⭐⭐ | 低 | 低 | 群聊（架构扩展方向一） |
| **P2** | 方向四：批量操作 API | ⭐⭐⭐ | 中 | 中 | 无 |
| **P1** | 方向五：关系遍历 API | ⭐⭐⭐ | 低-中 | 低 | 修复 `canReadFile` 权限检查 |

### 推荐实施顺序

```
Phase 1 (P1):
  ├── 方向一（草稿）— 用户写路径体验基线，零依赖
  └── 方向五（关系遍历）— 为所有 UI 构建关系面板，低风险，立即可见

Phase 2 (P2):
  ├── 方向二（@提及）— 提升协作体验，依赖 SSO 但不阻塞
  └── 方向四（批量操作）— 高频用户生产力提升，需设计部分失败模型

Phase 3 (P2):
  └── 方向三（引用/转发）— 最大价值在群聊上线后，依赖群聊基础设施
```

---

## 总结

本文件识别了 5 个在所有 17 份现有审查文档中**零覆盖**的高价值方向：

1. **草稿与自动保存** — 提升所有写操作的用户体验基础。最容易被忽视但用户触碰最频繁的能力
2. **@提及与用户标记** — 协作平台的社交基础设施。从评论回复扩展到定向通知的必由之路
3. **内容引用与转发** — 消息/评论之间的关系表达能力。与群聊（existing direction）组合后释放最大价值
4. **通用批量操作 API** — 高频用户的效率工具。弥补"单实体操作"到"批量管理"之间的鸿沟
5. **跨域关系遍历 API** — 连接孤立域之间的数据。使客户端能从"文件"导航到"引用了此文件的消息"再到"对此消息的评论"

这 5 个方向有一个共同特征：**它们不依赖未实现的基础设施**。每个方向都可以基于当前的代码库渐进实现，
不需要新的存储后端、新的中间件基础设施、或重大的架构变更。
它们在技术上是"低垂的果实"，在产品上是"已知但被遗忘的需求"——两相结合，使它们成为下一轮迭代的最佳候选。
