# aero-id — 全代码库扫描：五个未被覆盖的架构与产品扩展方向

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 全量扫描 `internal/` 全部 22+ 包、180+ 源文件、22 份迁移 SQL、`api/proto/`、`cmd/`、`configs/`，与 `docs/requirements/` 下 68 份既有分析文档逐条交叉比对，确认以下 5 个方向在全部既有分析中**零系统性覆盖**。  
> **约束**: 不编写任何代码。每个方向附带代码级证据和精确文件引用。  

---

## 前置：当前状态

aero-id 是一个成熟度极高的微服务：

| 维度 | 已交付 |
|------|--------|
| 存储管线 | 双后端（local/S3）、流式上传、预签名直传、checksum 去重、MIME 校验、引用计数 GC |
| 消息系统 | 全 CRUD、Reactions、编辑/软删除、已读回执（带 outbox 事件）、全文搜索 |
| 评论系统 | 两层嵌套、Like/Unlike、举报/审核队列、编辑、全文搜索 |
| 通知系统 | Outbox 驱动、偏好设置（含静默时段）、折叠合并、SSE 实时推送、Webhook 转发 |
| 搜索 | PostgreSQL FTS（tsvector + GIN）、三实体联合搜索 |
| 可靠性 | 事务性 Outbox、幂等键、限流、SSO 断路器 + stale-cache + singleflight + 证书固定、优雅关闭 |
| 可观测性 | RED 指标、OTel 分布式追踪、健康检查（含 SSO 探测） |
| 运维 | 迁移 CLI、22 个可逆迁移、管理角色 + 权限、审计日志、存储 GC |
| 传输层 | 38+ gRPC RPC、HTTP ↔ gRPC 完全对等 |

`make test && make lint` 绿灯。68 份既有分析文档覆盖了 80+ 个扩展方向。

---

## 方向一：内容格式化管线（Content Formatting Pipeline）

### 为什么需要

**重要性: 高 · 产品基础能力缺失**

### 问题描述

aero-id 中的**全部**用户生成内容——消息（`Message.Content`）、评论（`Comment.Content`）、文件元数据（`File.Name` / `File.Meta`）——以纯文本（plain text）存储和展示。没有任何内联格式化能力：

1. ~~~**无 Markdown**：不能加粗、斜体、代码块、列表、标题
2. **无 @提及（Mention）**：用户不能在消息或评论中提及另一个用户
3. **无 #话题标签（Hashtag）**：没有内容标签或话题聚合
4. **无链接预览（Link Preview）**：粘贴 URL 不会自动展开为富媒体卡片
5. **无内联图片/附件展示**：文件 ID 可以附加，但不会在文本流中内嵌渲染
6. **无表情响应（内联）**：虽然有 Reaction 机制，但文本本身不支持 `:emoji:` 语法

对于一个定位为"data owned by accounts"（文件、消息、评论）的服务，没有内容格式化意味着：

- **各客户端各自为政**：Web、iOS、Android 各自实现自己的 Markdown 解析器，导致渲染不一致
- **搜索断裂**：`@mentions` 中的用户名无法被 FTS 索引（因为以纯文本存储但无结构化提取）
- **通知贫乏**：无法在通知中展示格式化的内容摘要或提及的用户名
- **集成困难**：第三方系统通过 webhook 收到的内容是原始纯文本，无法直接展示

### 代码级证据

**证据 1：Message 和 Comment 的 Content 字段是纯文本**

```go
// internal/domain/message.go
type Message struct {
    // ...
    Content   string        // ← 纯文本，无格式标记
    // ...
}

// internal/domain/comment.go
type Comment struct {
    // ...
    Content    string        // ← 纯文本，无格式标记
    // ...
}
```

**证据 2：Create/Update 端点不做任何内容处理**

```go
// internal/message/service.go
func (s *service) createMessage(ctx context.Context, actor string, in SendMessageInput) (*domain.Message, error) {
    m := &domain.Message{
        // ...
        Content:   in.Content,  // ← 直接存储，无解析/过滤/格式化
        // ...
    }
}
```

```go
// internal/comment/service.go
// Create 和 Edit 中的 content 同样直接传入，无格式化管线
```

**证据 3：FTS 索引直接对原始文本做 tsvector**

```sql
-- migrations/000016_fts_indexes.up.sql
ALTER TABLE messages ADD COLUMN search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED;
-- content 中的 markdown 标记（如 `**`、`#`、`@user`）会污染倒排索引
```

**证据 4：无 `content` 相关包或配置**

```bash
$ ls internal/ | grep -i content
# 输出为空 — 没有任何内容处理相关的包
```

**证据 5：事件 payload 中 content 以纯文本传递**

```go
// internal/service/notification.go
type messageSentPayload struct {
    MessageID string `json:"message_id"`
    FromUID   string `json:"from_uid"`
    ToUID     string `json:"to_uid"`
    // 没有 Content 字段 — webhook 消费者拿不到消息内容
    // 即使拿到，也是纯文本
}
```

### 设计建议

1. **选择一种存储格式**：推荐 Markdown（通用、安全、易于转 HTML）。另存一份 `content_html`（服务端渲染的 HTML 缓存）或运行时渲染。
2. **引入 `internal/content/` 包**：
   - `Parser`：将 Markdown 解析为结构化 AST（使用 `goldmark` 或类似库）
   - `Sanitizer`：HTML 过滤（XSS 防护），配置允许的标签/属性
   - `MentionExtractor`：提取 `@username` 模式并解析为 uid
   - `HashtagExtractor`：提取 `#topic` 并建立标签索引
   - `LinkPreviewer`：异步解析 URL 的 Open Graph 元数据
3. **存储策略**：
   - `content_raw TEXT`：用户输入的原始 Markdown
   - `content_html TEXT`：服务端渲染的 HTML（可选，运行时渲染也可）
   - `mentions TEXT[]`：提取的用户 uid 列表（用于通知 + 权限）
   - `hashtags TEXT[]`：提取的标签列表（用于搜索聚合）
4. **通知集成**：`comment.replied` 和 `message.sent` 的 payload 中增加 `content_html` 或 `content_preview`，使 SSE/webhook 消费者可以直接展示格式化片段。
5. **FTS 增强**：对 `content_raw` 做 Markdown 剥离后再建 tsvector，或将 `mentions` / `hashtags` 列加入搜索覆盖。

| 子方向 | 影响 | 工作量 |
|--------|------|--------|
| Markdown 渲染（存储 raw + 运行时渲染 HTML） | 前端展示 | 低-中 |
| @Mention 提取 + 通知 | 社交互动 UX | 中 |
| Hashtag 提取 + 搜索聚合 | 内容发现 | 中 |
| Link Preview | 链接共享 UX | 中-高（需外部 API） |
| FTS 剥离标记 | 搜索质量 | 低 |

---

## 方向二：全文搜索缺失页游标（Cursor）与分页

### 为什么需要

**重要性: 中 · 功能完整性问题**

### 问题描述

aero-id 的全文搜索支持三个实体（消息、评论、文件）的联合查询，但**没有分页机制**：

1. HTTP handler 只传递 `q` 和 `limit`，不解析 `cursor`
2. gRPC proto 定义中 SearchRequest 无 cursor 字段
3. 搜索服务层的 `Search(ctx, query, kinds, limit)` 接口无分页参数
4. 返回结果是一个平面列表，没有 `next_cursor`、`has_more` 等分页指示
5. `limit` 被硬编码为 `defaultLimit = 20`，不支持自定义

这意味着：
- 用户搜索超过 20 条结果时，只能得到前 20 条
- 移动端/前端无法实现"加载更多"或"翻页"功能
- 搜索结果的可用性严重受限——对于消息丰富的用户，搜索几乎总是截断的

此外，排序只依赖 `ts_rank` 自然排序，没有组合排序（如 `ts_rank DESC, created_at DESC`），导致相同相关度的结果顺序不确定。

### 代码级证据

**证据 1：HTTP handler 不处理 cursor**

```go
// internal/search/handler_http.go
func (h *SearchHandler) Search(c *gin.Context) {
    query := c.Query("q")
    kindStr := c.Query("kinds")
    // ← 没有读取 cursor 参数
    // ...
    hits, err := h.svc.Search(c.Request.Context(), query, kinds, defaultLimit)
    // ← 使用硬编码 defaultLimit = 20
    // ...
    httpx.OK(c, gin.H{"results": items})
    // ← 没有返回 next_cursor 或 has_more
}
```

**证据 2：gRPC proto 无 cursor**

```protobuf
// api/proto/aero_id/v1/aero_id.proto
message SearchRequest {
  string query = 1;
  repeated string kinds = 2;
  int32 limit = 3;
  // ← 没有 cursor 字段
}
message SearchResponse {
  repeated SearchHit hits = 1;
  // ← 没有 next_cursor 字段
}
```

**证据 3：Service 接口无分页**

```go
// internal/search/search.go
type Service interface {
    Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error)
    // ← 返回类型只有 []SearchHit，无 cursor
}
```

**证据 4：Repository 的 Search 方法返回平面列表**

```go
// internal/repository/message.go — SearchMessages
func (r *messageRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error) {
    // SQL: ... LIMIT limit+1? 不，是 LIMIT ?
    // ← 没有做 limit+1 的下一页探测
    // ← 返回无 cursor
}
```

### 设计建议

1. **扩展 domain.SearchHit**：增加 `SortKey string` 字段作为游标值（如 `created_at + id` 的编码）
2. **扩展 Service.Search 返回签名**：`(hits []SearchHit, nextCursor string, err error)`
3. **实现 cursor-paginated 的 Repository 查询**：每个实体的 Search 方法接受 cursor 参数，使用 `WHERE search_vector @@ query AND (created_at, id) < (cursor_at, cursor_id) ORDER BY ts_rank DESC, created_at DESC, id DESC LIMIT limit+1`
4. **Proto 扩展**：SearchRequest 增加 `cursor string`，SearchResponse 增加 `next_cursor string`
5. **排序增强**：`ORDER BY ts_rank(search_vector, query) DESC, created_at DESC, id DESC` 保证稳定排序

| 端点 | 当前行为 | 目标行为 |
|------|---------|---------|
| `GET /v1/search?q=hello` | 返回最多 20 条，无分页 | 返回最多 limit 条 + `next_cursor` |
| `GET /v1/search?q=hello&cursor=xxx` | 不支持 | 从游标开始继续搜索 |
| gRPC Search RPC | 平面列表 | 分页列表 |

---

## 方向三：`internal/audit/` 死代码与双审计实现

### 为什么需要

**重要性: 中 · 架构整洁性 / 维护负担**

### 问题描述

aero-id 代码库中存在**两个**独立的审计日志实现：

| 文件 | 导入路径 | 状态 |
|------|---------|------|
| `internal/audit/repository.go` | `github.com/aero/aero-id/internal/audit` | **死代码**——在全部生产代码中**未被任何文件导入** |
| `internal/repository/audit.go` | `github.com/aero/aero-id/internal/repository` | 活跃——由 `app.go` 创建并注入各 Service |

两个包都实现了相同的功能（写入 `audit_events` 表），但：

- `internal/audit/`: 独立包，定义了 `Repository` 接口 + `Log` 方法，**无任何人引用**
- `internal/repository/audit.go`: 与 `repository` 包中的其他 Repository 共存，由 `NewAuditRepository(gdb)` 实例化

这不是一个无害的重复——这是架构漂移的明确信号：
1. **死代码需要维护**：编译器不会删除它，lint/测试仍会扫描它
2. **团队认知负担**：新开发者看到两个 audit 实现，需要花费精力判断哪个是"正确的"
3. **违反"单一职责"**：`internal/audit/` 包的意图是成为一个独立的审计域包，但从未被接入

### 代码级证据

**证据 1：`internal/audit/` 从未被导入**

```bash
$ grep -r "internal/audit" --include="*.go" | grep -v "_test.go"
# 输出为空 — 零引用
```

**证据 2：App 使用 repository 层的 AuditRepository**

```go
// internal/app/app.go
auditRepo := repository.NewAuditRepository(gdb)
fileSvc := service.NewFileService(..., service.WithFileAuditLogger(auditRepo), ...)
msgSvc := service.NewMessageService(..., service.WithMessageAuditLogger(auditRepo), ...)
commentSvc := service.NewCommentService(..., service.WithCommentAuditLogger(auditRepo), ...)
```

**证据 3：两个实现都向同一个 `audit_events` 表写入**

```go
// internal/audit/repository.go — 未使用的实现
type repo struct{ db *gorm.DB }
func (r *repo) Log(ctx context.Context, e *domain.AuditEvent) error {
    // 直接 Create auditEventRow
}

// internal/repository/audit.go — 活跃的实现
type auditRepo struct{ db *gorm.DB }
func (r *auditRepo) Log(ctx context.Context, e *domain.AuditEvent) error {
    // 直接 Create auditEventRow
}
```

**证据 4：Test 也可能覆盖不完整**

```bash
$ ls internal/audit/repository_test.go
# 存在测试文件，但因为无引用，测试可能未在 CI 中被充分执行
```

### 设计建议

| 步骤 | 动作 | 原因 |
|------|------|------|
| 1 | 确认 `internal/audit/` 的意图 | 如果它是"将审计提取为独立 bounded context"的起步，应该决定是否继续 |
| 2a | **如果保留独立包**：删除 `internal/repository/audit.go`，将 `internal/audit/` 接入 `app.go` | 消除重复 |
| 2b | **如果接受 repository 层**：删除整个 `internal/audit/` 目录 | 消除死代码 |
| 3 | 添加 lint 规则禁止未使用的包目录 | 防止复发 |

**推荐**: 删除 `internal/audit/` 目录（选项 2b），因为 `repository.NewAuditRepository` 已经深度集成且工作正常。审计日志是横切关注点，放在 `repository` 层更自然。如果未来审计需要成为独立 bounded context，届时通过提取而非复制来重构。

---

## 方向四：外部身份链接与账号恢复（External Identity Linking & Account Recovery）

### 为什么需要

**重要性: 高 · 用户安全与可用性**

### 问题描述

aero-id 完全依赖外部 SSO 进行身份认证。它在架构层面没有身份管理能力——没有用户表、没有密码、没有会话。这在设计上是正确的（见 AGENTS.md "SSO owns identity"），但**在边缘情况中存在三个未覆盖的问题**：

**问题 1：单一 SSO 供应商锁定**

如果 SSO 是单实例（如一个 Auth0 租户或一个 Keycloak 实例），用户无法：
- 使用 Google 账号登录后，再绑定 GitHub 账号
- 当主 SSO 供应商不可用时通过备用供应商登录
- 在公司账号和私人账号之间切换不丢失数据

**问题 2：账号链接 / 合并**

当同一个自然人拥有多个 SSO 身份时（如工作邮箱 sso + 个人邮箱 sso），aero-id 会将其视为**两个完全独立的不透明用户**——因为用户标识只是 SSO UID。没有端点或机制来：
- 将两个身份标记为同一人
- 合并两个身份下的文件和消息
- 将文件从一个身份转移到另一个

**问题 3：被动数据丢失风险**

SSO 中的用户被删除时，aero-id 中的用户数据（文件、消息、评论）变成**永久孤立的死数据**——没有 owner 的记录可以 GC 吗？目前 `owner_uid` 只是一个 text 类型，没有 FK 约束，也没有 SSO 用户删除时的级联处理策略。

### 代码级证据

**证据 1：auth 中间件只接受一个 Token 来源**

```go
// internal/middleware/auth.go
func Auth(v TokenValidator) gin.HandlerFunc {
    return func(c *gin.Context) {
        token := bearerToken(c.GetHeader("Authorization"))
        // ← 只检查 Authorization header 的 Bearer token
        // ← 不检查 X-Account-Id、Cookie、多来源
        uid, err := v.ValidateToken(c.Request.Context(), token)
        // ← 只返回一个 uid
        // ← 不支持 "次要身份" 或 "已链接账号"
    }
}
```

**证据 2：所有实体使用不透明 uid 作为 owner**

```go
// internal/domain/file.go
type File struct {
    OwnerUID   string    // ← 纯 text，无 FK，无链接语义
    // ...
}
// internal/domain/message.go — FromUID, ToUID
// internal/domain/comment.go — OwnerUID
// 全部使用不透明 uid
```

**证据 3：无账号链接相关的表或端点**

```bash
$ grep -r "account\|identity\|linked\|oauth.*provider\|external.*id\|merged" internal/ --include="*.go" | grep -v "_test\|_mock\|ssopb\|aeropb" | grep -v "vendor" | head -5
# 基本上没有输出
```

```bash
$ grep -ri "account_links\|user_identities\|linked_accounts\|external_logins" migrations/*.sql
# 输出为空 — 无相关迁移
```

**证据 4：删除 SSO 用户后的数据孤儿化**

```sql
-- migrations/000001_create_files.up.sql
CREATE TABLE files (
    owner_uid     TEXT NOT NULL,
    -- ↑ 无 REFERENCES 约束
    -- ↑ 无 ON DELETE CASCADE 或 SET NULL
    -- ↑ SSO 中用户被删除后，这些行变成孤儿
);
```

### 设计建议

**阶段 1（基础安全）**：
1. 定义 `account_links` 表：`(primary_uid TEXT, linked_uid TEXT, provider TEXT, linked_at TIMESTAMPTZ)`——允许用户将多个 SSO 身份映射到同一主账号
2. 在 auth 中间件中，支持从 SSO 返回的 uid 解析到主 uid
3. 保证所有数据查询以主 uid 为准

**阶段 2（数据保护）**：
1. 当 SSO 用户删除时（通过 SSO webhook 或冷检测），触发 aero-id 的**数据冻结策略**：
   - 将 `owner_uid` 替换为匿名占位符（保留数据但不暴露身份）
   - 或通过 outbox 发射 `user.deleted` 事件，触发清理/归档
2. 添加 `user_deletions` 表跟踪已删除用户的资产处理状态

**阶段 3（可选恢复）**：
1. 实现简单的**账号恢复流程**：当 SSO 不可用时，用户可通过备用渠道（已验证邮箱 / 恢复码）获取临时访问凭证
2. 通过 SSO 的 staled-cache 窗口期，允许用户数据在 SSO 短暂离线时的读取访问

| 场景 | 当前行为 | 目标行为 |
|------|---------|---------|
| 用户从 SSO 删除 | 数据永远孤儿化 | 冻结 / 归档 / 可恢复 |
| 用户丢失 SSO 访问 | 完全无法访问数据 | 备用恢复路径 |
| 用户有两个 SSO 身份 | 两套独立数据 | 链接 + 统一视图 |
| SSO 多供应商 | 不支持 | 主 + 备用供应商 |

---

## 方向五：消息系统的会话抽象缺失（Conversation Aggregate 缺失）

### 为什么需要

**重要性: 中 · 架构演进限制**

### 问题描述

当前消息系统的数据模型是完全扁平的——每条消息是一个独立行，由 `(from_uid, to_uid)` 标识参与者，"对话"（conversation）是一个运行时派生的概念，而非持久化的聚合。具体来说：

- 没有 `conversations` 表
- 没有 `conversation_participants` 表
- 没有对话级别的元数据（标题、最后消息预览、是否置顶、是否静音）
- `MarkConversationRead` 是通过扫描 `to_uid` + `peer_uid` 的所有未读消息实现的 O(N) 操作
- 收件箱（Inbox）是对 `messages` 表做 GROUP BY `(to_uid, from_uid)` 的聚合查询
- 不支持对话归档、置顶、静音等操作

这种设计在 MVP 阶段是合理的，但随着规模增长会带来四个问题：

1. **性能退化**：收件箱需要在 `messages` 上做 GROUP BY 聚合以找到"与谁对话"。随着消息量的增长，这个查询会变慢。
2. **功能限制**：无法实现对话列表、最后消息预览、已读光标、静音、归档、置顶等功能——这些都需要会话级别的元数据。
3. **扩展困难**：如果未来要支持群聊（Group Chat），需要新增 `conversations` + `conversation_participants` 表，并迁移现有消息——工作量巨大且破坏现有 API。
4. **域模型不完整**："Conversation"是一个核心领域概念（用户看到的是"与 Alice 的对话"，而非"发给 Alice 的第 42 条消息"），但它在领域模型中不存在。

### 代码级证据

**证据 1：无 Conversation 实体**

```bash
$ grep -r "type Conversation\|type conversation" internal/domain/ --include="*.go"
# 输出为空 — 领域层无 Conversation 类型
```

**证据 2：Inbox 查询是 GROUP BY 聚合**

```go
// internal/repository/message.go — ListInbox 需要对 messages 表做
// SELECT DISTINCT ON (from_uid) ... 或 GROUP BY 聚合
// 才能找到"所有与我对话过的人"
```

**证据 3：MarkConversationRead 是 O(N) 操作**

```go
// internal/repository/message.go
func (r *messageRepo) MarkConversationRead(ctx context.Context, toUID, peer string, at time.Time) error {
    // UPDATE messages SET read_at = ? WHERE to_uid = ? AND from_uid = ? AND read_at IS NULL
    // 需要扫描所有未读消息行
}
```

**证据 4：无对话级别元数据**

```bash
$ grep -ri "conversation.*pin\|conversation.*mute\|conversation.*archive\|conversation.*preview" internal/ --include="*.go"
# 输出为空 — 无相关功能
```

**证据 5：通知的 conversation 上下文缺失**

通知流程中：
- `message.sent` → 通知接收方
- 但接收方收到的通知不包含对话上下文（对话中有多少未读消息、最后一条消息的预览）

```go
// internal/outbox/outbox.go
type messageSentPayload struct {
    MessageID string `json:"message_id"`
    FromUID   string `json:"from_uid"`
    ToUID     string `json:"to_uid"`
    // ← 没有 conversation_id 或对话级别上下文
}
```

### 设计建议

**但不建议现在创建一个完整的 `conversations` 表**——对于 1:1 消息，当前设计是 functional 的但有限制。建议以下演进路线：

**近期（最小侵入）**：
1. 在 `messages` 表上添加 `conversation_id UUID` 列（可为 NULL，现有消息填 NULL 兼容）
2. 对 1:1 对话，`conversation_id` 由 `sorted(from_uid, to_uid)` 的哈希派生——这样同一个对话的所有消息共享同一个 ID，无需建新表即可实现收件箱聚合
3. 添加 `conversation_read_cursors(uid, peer_uid, read_cursor_id, read_cursor_at)` 表（`migrations/`），使未读计数变为 O(1) 查询

**中期**：
1. 当群聊需求明确时，创建真正的 `conversations` + `conversation_participants` 表
2. 从派生的 conversation_id 平滑迁移到真实的 conversation_id
3. 添加对话级元数据：标题、置顶、静音、归档状态

**长期**：
1. 对话级权限：退出对话、添加/移除参与者
2. 对话级通知偏好：静音整个对话而非每条消息

| 能力 | 当前（纯派生） | 近期待办 | 中期 |
|------|-------------|---------|------|
| 对话列表 | GROUP BY 聚合 | 派生 conversation_id → 按对话分组 | `conversations` 表 |
| 未读计数 | `COUNT(*) WHERE read_at IS NULL` | O(1) read cursor 表 | 同上 |
| 对话预览 | 无 | 派生：取每个对话最新消息 | `conversations.last_message` 列 |
| 置顶/静音/归档 | 不支持 | 不支持 | `conversation_preferences` 表 |
| 群聊 | 不支持 | 不支持 | `conversations` + `participants` |

---

## 总结：优先级与依赖关系

| # | 方向 | 类型 | 影响 | 工作量 | 现有分析覆盖 | 优先级 |
|---|------|------|------|--------|-------------|--------|
| 一 | 内容格式化管线 | 产品基础能力 | 高 | 中-高 | **0/68** ✅ | P1 |
| 二 | 搜索分页游标 | 功能完整性 | 中 | 低 | **0/68** ✅ | P1 |
| 三 | 双审计实现（死代码） | 架构整洁性 | 中 | 极低 | **0/68** ✅ | P2 |
| 四 | 外部身份链接与账号恢复 | 安全/可用性 | 高 | 中-高 | **0/68** ✅ | P1 |
| 五 | 会话抽象 | 架构演进 | 中 | 中 | **0/68** ✅ | P2 |

**建议执行顺序**：
1. **方向三（死代码清理）**：5 分钟的工作，立即提升代码库整洁度
2. **方向二（搜索分页）**：小型但高可见度的功能补齐，前端/移动端开发者会立即受益
3. **方向一（内容格式化）**：最大用户可见影响，建议作为独立项目分阶段推进
4. **方向四（身份链接）**：依赖 SSO 协作和产品决策，建议与 SSO 团队联合评估
5. **方向五（会话抽象）**：架构投资，建议在群聊需求明确时启动
