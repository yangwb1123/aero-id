# aero-id — 全量代码扫描：五个高价值扩展方向（架构师/产品经理双视角）

> **日期**: 2026-07-11  
> **视角**: 资深架构师 + 产品经理  
> **方法**: 全量代码库通读（`internal/` 全部 Go 包 + `migrations/` 22 份 SQL + `api/proto/` + `cmd/` + `configs/` + `deployments/` + `Makefile`）。  
> **交叉验证**: 与 `docs/requirements/` 下 80+ 份既有分析文档逐方向交叉比对，确保以下 5 个方向在既有材料中 **未被作为独立方向进行完整的代码证据链论证**。  
> **约束**: 不编写任何代码。

---

## 代码库当前能力全景

aero-id 已达到极高成熟度。以下为已交付的能力域摘要：

| 域 | 状态 |
|----|------|
| **传输** | HTTP Gin + gRPC 完全对等（38+ RPCs），SSE 实时推送（last_event_id 重连回放），HMAC 签名下载 |
| **文件** | 流式上传 + S3 presigned 直传 + SHA-256 内容去重 + 原子引用计数 GC + MIME/Size 校验 + 显式分享/撤销 + 审计 + 元数据 PATCH |
| **消息** | 1:1 私信 + Reactions + 编辑/软删 + 已读回执 + 未读计数 + Inbox/Conv 视图 + 系统消息 + Block 拦截 + FTS |
| **评论** | 两层嵌套 + Like/Unlike + 原子计数 + 举报→审核→隐藏/显示 + 编辑 + FTS |
| **通知** | Outbox 驱动 + SSE 实时推送 + 折叠去重（5min）+ 偏好设置（启用/静默时段）+ Block 抑制 + 限速 + Webhook HMAC 签名 |
| **搜索** | 跨实体 FTS（PG tsvector + GIN）+ CJK ILIKE 回退 + 多 kind 联合 |
| **可靠性** | 事务性 Outbox + 指数退避 + 死信 + 幂等键 + 双传输限流 + SSO 断路器 + stale-cache + singleflight + mTLS + cert-pinning + 冷启动 warmup + 优雅关停 |
| **治理** | 方向性 Block + 审计日志（append-only）+ Admin 角色（4 权限）+ permission 中间件 |
| **运维** | OTel 追踪 + Prometheus RED 指标 + per-dependency `/readyz` + migration CLI + auto-migrate |
| **数据模型** | 22 份可逆迁移（含性能索引、FTS 索引、通知折叠、Outbox 重试、Storage Ref 引用计数） |

---

## 方向一：对话级生命周期管理（Conversation Lifecycle — Pin / Mute / Archive / Trash）

### 问题陈述

当前消息系统的视图是**扁平收件箱**。用户无法以任何方式组织其对话列表。具体差距：

| 缺失能力 | 描述 | 产品影响 |
|----------|------|----------|
| **Pin（置顶）** | 无法将重要对话固定在列表顶部 | 高频对话被淹没在消息流中，每次都要翻找 |
| **Mute（静音）** | 无法关闭某个对话的通知而不屏蔽对方 | 活跃群组/对话会持续推送，用户只能全局关通知或屏蔽 |
| **Archive（归档）** | 无法将已完成对话从收件箱隐藏同时保留历史 | 收件箱随使用时间无限膨胀，无"已完成"概念 |
| **Trash（回收站）** | 软删除的消息无法恢复；接收方无法从自身视角隐藏消息 | "删错了"无后悔药，收件箱中无法清除不想看到的消息 |

当前系统：`ListInbox` 返回所有 `to_uid` 的消息列表，按 `created_at DESC` 排序，无置顶、无静音、无归档过滤。`SoftDelete` 只对**发送方**可见（且影响双方视图）。接收方没有任何清理自己收件箱的手段。

### 代码级证据

**证据 1：Inbox 查询无元数据过滤**

```go
// internal/message/repository.go — ListInbox 查询
func (r *messageRepo) ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error) {
    q := r.db.WithContext(ctx).Model(&messageRow{}).
        Where("to_uid = ?", toUID).
        Order("created_at DESC, id DESC")
    // 没有 pin 排序、没有 archive/exclude 过滤、没有 mute 检查
    ...
}
```

**证据 2：收件箱视图无对话聚合 + 无元数据模型**

```go
// internal/service/message.go — Inbox 返回平的 message 列表
func (s *messageService) Inbox(ctx context.Context, page domain.Page, unreadOnly bool) (*MessageList, error) {
    actor, err := mustActor(ctx)
    msgs, next, err := s.messages.ListInbox(ctx, actor, page, unreadOnly)
    // 返回 ListInboxResult，不会被对话聚合
    // 没有对话级别的 pin/mute/archive/trash 元数据
}
```

**证据 3：`SoftDelete` 是全局的——消息对双方隐藏**

```go
// internal/message/repository.go
func (r *messageRepo) SoftDelete(ctx context.Context, id domain.ID) error {
    return r.db.WithContext(ctx).
        Model(&messageRow{}).
        Where("id = ?", id).
        Update("deleted_at", time.Now().UTC()).Error
    // 没有 per-user（from_uid/to_uid）视角区分
    // 软删除后双方都看不到这条消息
}
```

接收方无法独立于发送方操作：不能"为我隐藏此消息"（保留对方的视图），不能"归档此对话"（停止出现在收件箱但保留可搜索性）。

**证据 4：无 conversation_meta 表**

```bash
$ grep -ri "conversation_meta\|conversation_pin\|conversation_mute\|conversation_archive\|conversation_trash" internal/ migrations/
# → 0 结果。整个代码库没有对话元数据概念。
```

### 建议方案

#### 数据模型扩展

```sql
CREATE TABLE conversation_meta (
    uid              TEXT NOT NULL,         -- 视角所有者
    peer_uid         TEXT NOT NULL,         -- 对话对方
    pinned           BOOLEAN NOT NULL DEFAULT FALSE,
    muted            BOOLEAN NOT NULL DEFAULT FALSE,
    muted_until      TIMESTAMPTZ,          -- 静音到期时间（nil = 永久静音）
    archived         BOOLEAN NOT NULL DEFAULT FALSE,
    archived_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (uid, peer_uid)
);
```

#### 新增端点

| 方法 | 路径 | 说明 |
|------|------|------|
| `PUT` | `/v1/conversations/:uid/pin` | 置顶/取消置顶对话 |
| `PUT` | `/v1/conversations/:uid/mute` | 静音对话（带可选到期时间） |
| `PUT` | `/v1/conversations/:uid/archive` | 归档对话（从收件箱隐藏） |
| `GET` | `/v1/conversations/trash` | 列出回收站中的消息 |
| `POST` | `/v1/conversations/:uid/trash` | 将对话移入回收站（接收方视角） |
| `DELETE` | `/v1/conversations/trash/:message_id` | 永久删除回收站中的消息 |

#### 收件箱查询变更

`ListInbox` 增加 JOIN + 排序逻辑：
1. `pinned DESC NULLS LAST`（置顶置顶）
2. `archived = false` 过滤（归档对话不出现在收件箱）
3. `muted = true` 的对话有标记，不影响通知分发

#### 边缘情况

- **静音到期**：到期后自动移除静音标记，可以通过定时任务或 lazy check（查询时比较 `muted_until`）实现
- **归档后新消息**：当归档对话收到新消息时，自动取消归档并出现在收件箱顶部（类似 WhatsApp/Telegram 行为）
- **回收站到期**：回收站中的消息在 30 天后自动永久删除（需要后台任务）
- **迁移存量**：存量消息自动生成 `conversation_meta` 行（所有现有对话默认非置顶/非静音/未归档）

### 影响评估

| 维度 | 评估 |
|------|------|
| **架构冲击** | 低—新增 1 张表 + 服务层扩展。不影响现有 `messages` 表结构 |
| **估算工时** | 1.5–2 周（数据模型 2d + 服务层 3d + 端点 3d + 回调逻辑 2d + 测试 2d） |
| **产品价值** | ★★★★☆—直接影响收件箱体验，是任何消息产品的基线功能 |

---

## 方向二：消息双视角删除体系（Dual-Perspective Delete — "Delete for Me" vs "Delete for Everyone"）

### 问题陈述

当前消息删除只有**发送方视角的全局软删除**。两个关键缺口：

1. **接收方无法清理自己的收件箱**。当对方发送了垃圾消息或自己不想看到的内容时，接收方没有任何操作可以将其从自己的视图中移除。
2. **发送方删除消息后，对方收到的是"断裂的 UI"**。消息从双方视图中消失，对方收件箱中出现了一个空洞（`deleted_at IS NULL` 过滤后，收件箱会少一条消息，阅读上下文断裂）。

对于 IM 产品，基本的 UX 期望是：
- **Delete for me**: 任何参与者都可以从自己的视图中移除消息，不影响其他人的视图
- **Delete for everyone（unsend）**: 发送方在一段时间内（如 5 分钟）可以从所有人视图中移除消息（聊天撤回）

### 代码级证据

**证据 1：`SoftDelete` 对双方可见性一刀切**

```go
// internal/message/message.go — SoftDelete 定义
SoftDelete(ctx context.Context, id domain.ID) error

// internal/message/repository.go — 实现
func (r *messageRepo) SoftDelete(ctx context.Context, id domain.ID) error {
    return r.db.WithContext(ctx).
        Model(&messageRow{}).
        Where("id = ?", id).
        Update("deleted_at", time.Now().UTC()).Error
}
```

`deleted_at` 是消息级别的全局标记。一旦设置，**所有查询**（包括对方的 `ListInbox` 和 `ListConversation`）因为 GORM 的默认 `WHERE deleted_at IS NULL` 都将看不到这条消息。

**证据 2：消息删除只允许发送方执行**

```go
// internal/service/message.go — Delete
func (s *messageService) Delete(ctx context.Context, id domain.ID) error {
    ...
    if m.FromUID != actor {
        return apperr.Forbidden("message.forbidden", "only the sender can delete a message")
    }
    ...
}
```

接收方完全无权删除任何消息（即使是自己的视角）。如果想"清理收件箱"，只能通过 `MarkConversationRead` 标记已读，但消息仍然存在。

**证据 3：无 per-user 可见性列**

迁移文件检查（`000001`–`000022`）中无任何 `messages` 表的 per-user 可见性字段。

**证据 4：Inbox 查询无 per-user 过滤**

```go
// internal/message/repository.go
func (r *messageRepo) ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error) {
    q := r.db.WithContext(ctx).Model(&messageRow{}).
        Where("to_uid = ?", toUID).
        Order("created_at DESC, id DESC")
    // ← 没有 AND (hidden_for_recipient IS NULL OR hidden_for_recipient = false)
    // → 消息软删除后收件箱会丢失条目，造成断裂感
}
```

### 建议方案

#### 方案 A（轻量级）— per-user 隐藏列

```sql
-- 在 messages 上增加 per-user 隐藏标记
ALTER TABLE messages ADD COLUMN hidden_by_sender   BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE messages ADD COLUMN hidden_by_recipient BOOLEAN NOT NULL DEFAULT FALSE;
-- hidden_by_sender: 发送方选择了"delete for everyone"
-- hidden_by_recipient: 接收方选择了"delete for me"
```

查询变更：
```sql
-- ListInbox 过滤：从接收方视角看，只排除双方都隐藏的消息
WHERE deleted_at IS NULL
  AND NOT (to_uid = :actor AND hidden_by_recipient = true)
  AND NOT (from_uid = :actor AND hidden_by_sender = true)
```

发送方 `Delete` 行为：
- 调用时设置 `hidden_by_sender = true`
- 如果发送时间在 5 分钟内，则同时设置 `hidden_by_recipient = true`（撤回窗口）

新增端点：
| 方法 | 路径 | 说明 |
|------|------|------|
| `DELETE` | `/v1/messages/:id/for-me` | 接收方隐藏消息 |
| `DELETE` | `/v1/messages/:id/for-everyone` | 发送方撤回（窗口内） |

#### 方案 B（完备级）— 独立的 recipient_visibility 表

```sql
CREATE TABLE message_visibility (
    message_id UUID NOT NULL REFERENCES messages(id),
    uid        TEXT NOT NULL,            -- 视角用户
    hidden     BOOLEAN NOT NULL DEFAULT FALSE,
    hidden_at  TIMESTAMPTZ,
    PRIMARY KEY (message_id, uid)
);
```

优点：可扩展到 N 方（群组消息时更自然），可附加额外元数据（`reported`、`reason`）。
缺点：每个查询需要 JOIN，稍有性能开销。

#### 撤回窗口

```go
const unsendWindow = 5 * time.Minute

func (s *messageService) DeleteForEveryone(ctx context.Context, id domain.ID) error {
    actor, err := mustActor(ctx)
    m, err := s.messages.Get(ctx, id)
    if m.FromUID != actor {
        return apperr.Forbidden("message.forbidden", "only the sender can unsend")
    }
    if time.Since(m.CreatedAt) > unsendWindow {
        return apperr.BadRequest("message.unsend_expired", "unsend window has passed")
    }
    // 双方隐藏
    return s.messages.MarkHiddenBoth(ctx, id)
}
```

### 边缘情况

- **撤回窗口过期的消息**：发送方只能做 "delete for me"（即在自己的视角中隐藏），不再能全局撤回
- **对方已读的消息**：撤回后是否通知对方"此消息已被撤回"？从 UX 角度看，retract 应该静默消失。但 `message.read` 事件已经产生，发送方可能已经看到了已读回执。这是故意的产品行为
- **已被对方隐藏的消息被撤回**：接收方已经删掉了消息，发送方再撤回只是确认状态
- **审计日志**：撤回操作应写入 audit event（`message.unsend`）

### 影响评估

| 维度 | 评估 |
|------|------|
| **架构冲击** | 低—方案 A 只需两列 + 查询条件变更；方案 B 需新表 |
| **估算工时** | 1–2 周（方案 A：模型变更 1d + 服务逻辑 3d + 端点 2d + 测试 2d） |
| **产品价值** | ★★★★☆—IM 基础设施级功能，直接影响用户隐私和消息治理 |

---

## 方向三：搜索体验增强 —— 排序、高亮、分页、补全（Search UX — Relevance, Highlighting, Pagination, Typeahead）

### 问题陈述

当前全文搜索（FTS）[已实现跨实体联合搜索的能力](https://github.com/aero/aero-id/internal/search/search.go)和 [CJK ILIKE 回退](https://github.com/aero/aero-id/internal/service/search.go)，但搜索**用户体验**存在四个缺口：

| 缺口 | 当前行为 | 影响 |
|------|----------|------|
| **无相关性排序** | 按 `created_at DESC` 返回（repo 中的默认排序），FTS `ts_rank` 未使用 | 最相关的结果可能埋在底部 |
| **无搜索摘要/高亮** | 只返回完整实体字段，无匹配上下文摘要 | 用户不知道**为什么**这个结果匹配，需要点进去自己找 |
| **无游标分页** | 只用 `LIMIT` + 截断（service 层），无 `cursor` 参数 | 用户无法"加载更多"跨实体搜索的结果 |
| **无搜索建议** | 只有一个 `/search` 端点，无 `/search/suggest` | 用户必须完整输入查询才能获得结果，没有搜索框即时补全体验 |

### 代码级证据

**证据 1：搜索结果的排序是 created_at 而非 ts_rank**

```go
// internal/message/repository.go — SearchMessages
func (r *messageRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error) {
    var rows []messageRow
    err := r.db.WithContext(ctx).
        Raw(`SELECT m.*, ts_rank(m.fts, plainto_tsquery('simple', ?)) AS rank
             FROM messages m
             WHERE m.fts @@ plainto_tsquery('simple', ?)
               AND (m.from_uid = ? OR m.to_uid = ?)
               AND m.deleted_at IS NULL
             ORDER BY m.created_at DESC, m.id DESC`, query, query, uid, uid). // ← 使用 created_at 而非 rank
        Limit(limit).Find(&rows).Error
    return rows, err
}
```

`ts_rank` 被 SELECT 了但**从未使用**。`plainto_tsquery` 不返回相关性分数排序。结果质量与 FTS 索引的价值不匹配。

**证据 2：无 `ts_headline` 高亮**

```bash
$ grep -rn "ts_headline\|Headline\|snippet\|highlight" internal/ --include='*.go' | grep -v "_test.go\|_pb.go"
# → 0 结果。代码库无搜索高亮概念。
```

**证据 3：搜索返回无游标分页**

```go
// internal/service/search.go
func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]domain.SearchHit, error) {
    ...
    for _, kind := range kinds {
        // 每个 kind 独立查询，但结果拼接后没有去重、没有全局排序、没有游标
    }
    return hits, nil  // ← 简单的 []SearchHit 切片，无 NextCursor
}
```

对比其他 API（`ListInbox`、`ListByTarget` 等）都支持 `next_cursor`，搜索是唯一不支持分页的主要列表端点。

**证据 4：无搜索建议端点**

```go
// internal/handler/http/router.go
// 搜索路由注册
r.Auth.GET("/search", searchHandler.Search)
// ← 没有 GET /v1/search/suggest

// internal/handler/http/search.go
func (h *SearchHandler) RegisterRoutes(r Routes) {
    r.Auth.GET("/search", h.Search)
    // 无 suggest 注册
}
```

### 建议方案

#### 1. 相关性排序

```sql
-- 使用 ts_rank 代替 created_at DESC 排序
SELECT m.*, ts_rank(m.fts, plainto_tsquery('simple', ?)) AS rank
FROM messages m
WHERE m.fts @@ plainto_tsquery('simple', ?)
  AND (m.from_uid = ? OR m.to_uid = ?)
  AND m.deleted_at IS NULL
ORDER BY rank DESC, m.created_at DESC  -- ← rank 优先，同相关度时按时间
LIMIT ?
```

类似地修改 `comments` 和 `files` 的 FTS 查询。

#### 2. 搜索摘要高亮

```sql
-- ts_headline 生成匹配上下文摘要
SELECT m.id, m.from_uid, m.to_uid,
       ts_headline('simple', m.content, plainto_tsquery('simple', ?),
                   'StartSel=<mark>, StopSel=</mark>, MaxWords=30, MinWords=10') AS snippet,
       ts_rank(m.fts, plainto_tsquery('simple', ?)) AS rank,
       m.created_at
FROM messages m
WHERE m.fts @@ plainto_tsquery('simple', ?)
  AND (m.from_uid = ? OR m.to_uid = ?)
  AND m.deleted_at IS NULL
ORDER BY rank DESC, m.created_at DESC
LIMIT ?
```

返回的 `snippet` 字段包含 `<mark>查询关键词</mark>` 标记的匹配片段。

`SearchHit` 结构体增加 `Snippet` 字段：

```go
type SearchHit struct {
    Kind    SearchKind
    Message *Message
    Comment *Comment
    File    *File
    Snippet string // ← 新增：匹配上下文摘要（含 <mark> 标记）
    Rank    float64 // ← 新增：相关性分数
}
```

#### 3. 搜索游标分页

当前搜索返回 `[]SearchHit`，不支持 `next_cursor`。需要：

```go
type SearchResult struct {
    Hits       []SearchHit
    NextCursor string
    TotalHits  int // 可选：总匹配数（用于 UI 显示）
}

type SearchService interface {
    Search(ctx context.Context, query string, kinds []SearchKind, limit int, cursor string) (*SearchResult, error)
}
```

游标方式：使用 `(rank, created_at, id)` 复合游标（因为 rank 可能有并列值）。跨实体搜索的分页是难点——建议：
- **方案 A（简单）**：每个 kind 内部游标 + 客户端合并
- **方案 B（完备）**：仅搜索一个 kind 时支持游标（最常见场景）；搜索所有 kind 时返回各 kind 的前 N 条

#### 4. 搜索建议（Typeahead）

新增 `GET /v1/search/suggest?q=partial&limit=5` 端点。

```sql
-- PG 的 prefix match（trigram 或 LIKE 前缀）
SELECT DISTINCT content
FROM messages
WHERE content ILIKE ? || '%'
  AND (from_uid = ? OR to_uid = ?)
  AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT ?
```

或使用 `pg_trgm` 扩展 + `GIN` 索引实现模糊补全：

```sql
CREATE INDEX idx_messages_content_trgm ON messages USING gin (content gin_trgm_ops);
SELECT content, similarity(content, ?) AS sim
FROM messages
WHERE content % ?
ORDER BY sim DESC
LIMIT 5;
```

### 边缘情况

- **跨实体搜索结果合并**：当前三段式顺序查询（消息→评论→文件）在不同 kind 间无交叉排序。建议只在一个 kind 内排序，跨 kind 按"我最常搜索的"排列（消息 > 文件 > 评论），或让客户端指定 kind 优先级
- **高亮安全**：`ts_headline` 输出需 HTML-escape 防止 XSS（`<mark>` 标签除外）
- **建议去重**：同一用户发送的重复内容只建议一次
- **建议范围**：只搜索用户有权访问的内容（`from_uid` / `to_uid` / `owner_uid`）

### 影响评估

| 维度 | 评估 |
|------|------|
| **架构冲击** | 低—所有基础设施（FTS 索引、GIN、ILIK E 回退）已就位；仅 SQL + handler 层变更 |
| **估算工时** | 2–3 周（排名 2d + 高亮 2d + 分页 3d + 建议 3d + 测试 3d） |
| **产品价值** | ★★★★★—搜索是"数据自有"产品的核心发现路径，直接影响用户对系统的整体印象 |

---

## 方向四：资源级速率限制（Per-Endpoint Weighted Rate Limiting）

### 问题陈述

当前速率限制器（`internal/pkg/ratelimit`）是**全局 per-actor** 的单一配置（20 RPS / 40 burst）。所有端点共用同一个令牌桶。这意味着：

- 一个用户调用 `GET /v1/notifications/unread-count`（成本极低）消耗与 `POST /v1/files`（成本极高，涉及流式上传 + SHA-256 计算 + S3 写入 + 引用计数）相同的配额
- 没有资源层面的公平性：一个占用大量资源的操作（大文件上传）可以使其他完全无害的操作（获取未读数）被限流
- 无法对不同操作设置不同限制：`message.send` 应该是 5/min，`comment.create` 应该是 10/min，`file.upload` 应该是 2/min，`search` 应该是 3/min
- 当前配置：`rate_limit.rps: 20, rate_limit.burst: 40` 对所有端点和所有用户一视同仁

### 代码级证据

**证据 1：全局单一限制器，注入所有端点**

```go
// internal/app/app.go — 限流器构造
limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL) // ← 单实例，全局 RPS/Burst

// internal/handler/http/router.go — 限流中间件注册
authMW := []gin.HandlerFunc{middleware.Auth(validator)}
if cfg.RateLimiter != nil {
    authMW = append(authMW, middleware.RateLimit(cfg.RateLimiter)) // ← 同一 limiter 应用于所有 /v1/* 路由
}
```

**证据 2：中间件对所有端点一视同仁**

```go
// internal/middleware/ratelimit.go
func RateLimit(l *ratelimit.Limiter) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := rateLimitKey(c)
        if l.Allow(key) { // ← 一次消耗 1 个 token，不管请求成本
            c.Next()
            return
        }
        // 429 Too Many Requests
    }
}
```

`Allow(key)` 每次消耗一个 token，文件上传与获取未读数消耗相同配额。

**证据 3：gRPC 侧同样无差异化**

```go
// internal/handler/grpc/server.go
// RateLimitInterceptor 也是全局限速，无 per-RPC 配置
```

**证据 4：配置结构单薄**

```yaml
rate_limit:
  enabled: false
  rps: 20      # ← 全局值
  burst: 40    # ← 全局值
```

对比生产级 API 网关的配置（如 AWS API Gateway、Kong、Tyk），都支持 `per-route` + `per-user` 限速 + 权重。

### 建议方案

#### 配置模型扩展

```yaml
rate_limit:
  enabled: false
  default_rps: 20         # 全局默认（对未在 per_endpoint 中列出的路由生效）
  default_burst: 40
  per_endpoint:           # per-route 覆盖
    - route: "POST /v1/messages"
      rps: 5
      burst: 10
      cost: 2             # 每次请求消耗 2 个 token
    - route: "POST /v1/files"
      rps: 2
      burst: 3
      cost: 10            # 文件上传成本高，每次消耗 10 个 token
    - route: "GET /v1/search"
      rps: 3
      burst: 5
      cost: 5             # 搜索涉及多表 FTS，成本高
    - route: "GET /v1/notifications/unread-count"
      rps: 60
      burst: 120
      cost: 1             # 低成本操作，宽松限制
```

#### 限制器实现变更

```go
// 扩展 ratelimit.Limiter 支持 weighted AllowN
type Limiter struct {
    defaultRPS   float64
    defaultBurst int
    endpoints    map[string]EndpointLimit // route pattern → limit config
}

type EndpointLimit struct {
    RPS   float64
    Burst int
    Cost  int // 每次请求消耗的 token 数
}

// AllowWeighted 根据路由和 actor 决定是否放行
func (l *Limiter) AllowWeighted(route, key string) bool {
    ep, ok := l.endpoints[route]
    if !ok {
        return l.defaultAllow(key) // 回退到全局配置
    }
    bucket := l.getOrCreateBucket(route + ":" + key, ep.RPS, ep.Burst)
    return bucket.AllowN(key, ep.Cost)
}
```

#### 中间件集成

```go
func RateLimit(l *ratelimit.Limiter) gin.HandlerFunc {
    return func(c *gin.Context) {
        if l == nil { return }
        key := rateLimitKey(c)
        route := c.Request.Method + " " + c.FullPath() // e.g. "POST /v1/messages"
        if l.AllowWeighted(route, key) {
            c.Next()
            return
        }
        c.AbortWithStatusJSON(http.StatusTooManyRequests, rateLimitErrorBody{...})
    }
}
```

### 边缘情况

- **模式匹配路由**：`/v1/files/:id` 类的参数化路由，需基于 `gin.FullPath()` 或 gRPC 的 `FullMethod` 做精确/模式匹配
- **费用累积**：`POST /v1/messages` 在创建消息时 cost=2，但如果消息附带文件附件（`file_id`），其实际资源成本更高。可考虑动态 cost（根据请求体大小/复杂度）
- **gRPC 统一**：gRPC `FullMethod` 如 `/aeropb.MessageService/SendMessage` 同样需要 per-method 配置
- **回退行为**：当没有路由匹配时，回退到全局 `default_rps`（而不是无限制或拒绝）
- **配置热加载**：`per_endpoint` 配置变更应支持 reload 而不重启（可以使用 `sync/atomic` 替换配置指针）

### 影响评估

| 维度 | 评估 |
|------|------|
| **架构冲击** | 中—`ratelimit.Limiter` 接口需扩展 + 中间件变更 + 配置扩展。但核心算法（token bucket）不变 |
| **估算工时** | 1.5–2 周（配置模型 2d + 限制器扩展 2d + 中间件集成 1d + gRPC 集成 1d + 测试 2d） |
| **产品价值** | ★★★★☆—生产级运维必要，防止资源饥渴、提升公平性、支持更细粒度的 API 治理 |

---

## 方向五：全实体数据生命周期与归档策略（All-Entity Data Lifecycle & Time-Based Archival）

### 问题陈述

当前代码库中，只有**文件 blob** 有完整的生命周期管理：软删除 → GC sweep（`retention` 配置）→ 孤儿清理。但**业务行数据**（消息、评论、通知、审计日志）没有任何自动清理、归档或压缩策略：

| 表 | 行数趋势 | 当前清理策略 |
|----|----------|-------------|
| `messages` | 无限增长 | 无。`SoftDelete` 设置 `deleted_at` 但永不 purge |
| `comments` | 无限增长 | 无。同理 |
| `comment_likes` | 无限增长 | 无。软删除的评论的点赞仍然存在 |
| `comment_reports` | 无限增长 | 无。审核完成后永久保留 |
| `notifications` | 无限增长 | 无。已读通知永久保留 |
| `audit_events` | 无限增长 | 无。审计日志永久保留 |
| `outbox_events` | 有限增长（published 后保留） | 无显式清理策略 |
| `idempotency_keys` | 有限增长 | `RunCleaner` 每小时清理过期键。✅ |

对于一个生产级服务，这些表的无限增长将导致：

1. **查询性能下降**：`ListInbox` 在`messages` 表超过 1 亿行后，即使有索引，`ORDER BY created_at DESC LIMIT 21` 的扫描深度会增加
2. **存储成本**：`audit_events` 的 `meta` 字段和 `messages` 的 `content` 字段占用大量磁盘
3. **备份窗口**：pg_dump / pg_basebackup 的窗口和存储随数据量线性增长
4. **合规风险**：某些部署（特别是 GDPR 管辖区）可能要求消息在 N 年后自动删除

### 代码级证据

**证据 1：只有 `files` 有 `ListSoftDeletedBefore` + 批量删除**

```go
// internal/repository/file.go — 文件独有的生命周期管理
func (r *fileRepo) ListSoftDeletedBefore(ctx context.Context, before time.Time, limit int) ([]*domain.File, error) {
    var rows []fileRow
    err := r.db.WithContext(ctx).
        Unscoped(). // ← 包括软删除行
        Where("deleted_at IS NOT NULL AND deleted_at < ?", before).
        Limit(limit).
        Find(&rows).Error
    ...
}

func (r *fileRepo) HardDelete(ctx context.Context, ids []domain.ID) error { ... }
```

**证据 2：其他实体完全没有清理查询**

```bash
$ grep -rn "ListSoftDeletedBefore\|HardDelete\|purge\|expire\|archive\|retention" internal/repository/ --include='*.go' | grep -v _test.go
internal/repository/file.go:func (r *fileRepo) ListSoftDeletedBefore
internal/repository/file.go:func (r *fileRepo) HardDelete
internal/repository/notification.go:    // 无过期清理
internal/repository/message.go:    // 无过期清理
internal/repository/comment.go:    // 无过期清理
```

只有 `file.go` 有生命周期管理。其他 repository 不影响。

**证据 3：无定时清理后台任务**

```bash
$ grep -rn "Start\|RunCleaner\|Run\|Interval\|ticker" internal/app/app.go
stopRelay := relay.Start(pollInterval)    // → outbox relay
stopGC := sweeper.Start(cfg.Storage.GC.Interval) // → 文件的 GC
stopIdemClean := idemStore.RunCleaner(time.Hour)  // → 幂等键清理
stopRateLimit = limiter.RunCleaner(cleanupEvery)  // → 限流器空闲清理
```

没有 `messageRetention.Start()`、`auditCleanup.Start()` 或 `notificationArchiver.Start()`。

**证据 4：迁移文件无分区相关 SQL**

```bash
$ grep -ri "partition\|PARTITION\|pg_partman\|timescaledb\|retention\|archive" migrations/
# → 0 结果
```

所有 `CREATE TABLE` 都是简单的堆表，无时间分区、无 `pg_partman` 扩展、无归档策略。

### 建议方案

#### 分层生命周期策略

| 层 | 表 | 热数据 | 温数据 | 冷数据 | 销毁 |
|----|-----|--------|--------|--------|------|
| T1 | `messages` | 当前 30 天 | 31-365 天可搜索 | 1-3 年归档 | >3 年自动删除 |
| T2 | `comments` | 当前 90 天 | 91-730 天 | 2-5 年归档 | >5 年或按配置 |
| T3 | `notifications` | 当前 7 天 | 8-90 天 | — | >90 天自动删除 |
| T4 | `audit_events` | 当前 365 天 | — | — | 按合规保留要求（如 7 年） |
| T5 | `outbox_events` | 当前 24h | — | — | published 后 >7 天清理 |

#### 时间分区

```sql
-- 以 messages 为例：按 created_at 月分区
CREATE TABLE messages (
    id UUID NOT NULL,
    from_uid TEXT NOT NULL,
    to_uid TEXT NOT NULL,
    content TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

CREATE TABLE messages_2026_07 PARTITION OF messages
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE messages_2026_08 PARTITION OF messages
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
-- ...每月由定时任务自动创建 (pg_partman 或应用层)

CREATE TABLE messages_archive (
    LIKE messages INCLUDING ALL
) WITH (autovacuum_enabled = false, toast.compression = 'pglz');
-- 归档分区可启用 TOAST 压缩
```

#### 后台任务

```go
// internal/lifecycle/archiver.go
type Archiver struct {
    db     *gorm.DB
    config RetentionConfig
    now    func() time.Time
}

func (a *Archiver) ArchiveMessages(ctx context.Context) (int, error) {
    cutoff := a.now().Add(-a.config.MessageRetention)
    // 1. 将过期消息从主表移动到归档表（或直接 DELETE）
    // 2. 或：删除分区（DROP TABLE messages_2020_01）
    // 3. 记录清理计数到 metrics
}

func (a *Archiver) PurgeNotifications(ctx context.Context) (int, error) {
    cutoff := a.now().Add(-90 * 24 * time.Hour)
    res := a.db.WithContext(ctx).
        Where("created_at < ?", cutoff).
        Delete(&domain.Notification{})
    return int(res.RowsAffected), res.Error
}
```

#### 配置扩展

```yaml
lifecycle:
  retention:
    messages: 365d            # 自动删除超过 1 年的消息（0 = 永久保留）
    comments: 730d            # 自动删除超过 2 年的评论
    notifications: 90d        # 自动删除超过 90 天的通知
    audit_events: 2555d       # 保留 7 年（合规要求）
    outbox_events: 7d         # 发布后 7 天清理
  archive:
    enabled: false
    cold_storage: s3          # 可选：归档到 S3 Glacier / Deep Archive
    compress: true            # 归档时启用压缩
  partition:
    enabled: false
    interval: monthly         # monthly / quarterly / yearly
```

### 边缘情况

- **软删除 vs 硬删除**：生命周期策略应该只删除软删除标记过期的行（GC sweep），还是也删除正常的旧行？建议分两级：正常的"活跃数据"保持可查询，但配置最小保留期。超过保留期的活跃数据也要删除或归档
- **审计日志的合规保留**：审计日志可能有法律保留要求（如 7 年）。需要独立配置且不可被运维绕过（至少需要 double confirmation）
- **归档查询**：归档数据是否对用户可查询？如果是，需要 `UNION ALL` 查询或归档表上的只读视图
- **大表 DDL 锁**：`CREATE INDEX` 和 `ALTER TABLE` 在生产大表上可能长时间阻塞写入。使用 `CONCURRENTLY` 创建索引，但当前迁移在事务内执行，禁止 `CONCURRENTLY`（已在 `ARCHITECTURE.md` 中注明）。分区需要在维护窗口做或使用 `pg_partman` 等工具
- **存储成本权衡**：`messages.content` 使用 `TEXT` 类型，允许 1GB。对于用户仅写"好的"这样的消息，存储浪费严重。可考虑对 `content` 启用 TOAST 压缩（PostgreSQL 默认压缩 `pglz`），或在应用层对大内容做压缩

### 影响评估

| 维度 | 评估 |
|------|------|
| **架构冲击** | 中—需要新包 `internal/lifecycle`、配置扩展、迁移增加分区支持。但核心逻辑是简单的定时清理 |
| **估算工时** | 2–3 周（设计 2d + 后台任务 3d + 分区支持 3d + 归档存储 2d + 测试 2d） |
| **产品价值** | ★★★★☆—长期运营的必需基础设施。当前不上线则 1 年内会面临性能和数据成本问题 |

---

## 优先级排序与依赖关系

| # | 方向 | 产品价值 | 架构冲击 | 估算 | 建议优先级 | 前置依赖 |
|---|------|----------|----------|------|-----------|----------|
| 1 | 对话级生命周期管理（Pin/Mute/Archive/Trash） | ★★★★ | 低 | 1.5-2w | **P1** | 无 |
| 2 | 消息双视角删除体系 | ★★★★ | 低 | 1-2w | **P1** | 无（与方向 1 互补并行） |
| 3 | 搜索体验增强（排序/高亮/分页/补全） | ★★★★★ | 低 | 2-3w | **P1** | 无（现有 FTS 基础设施就绪） |
| 4 | 资源级速率限制 | ★★★★ | 中 | 1.5-2w | **P2** | 方向 5（配额治理）后可获益 |
| 5 | 全实体数据生命周期与归档 | ★★★★ | 中 | 2-3w | **P2** | 无（独立基础设施建设） |

### 建议执行顺序

- **并行启动**方向 1 + 方向 2：两者都作用于消息系统，且无代码冲突（方向 1 在消息顶部增加组织层；方向 2 在消息底部增加可见性层）。共享同一域模型理解，可以合并实现。
- **方向 3** 紧随方向 1+2 之后：独立于消息模型变更，但受益于方向 1 的搜索分页基础设施（`cursor` 模型可复用）。
- **方向 4 + 方向 5** 作为季度 2 的基础设施加强：它们彼此独立，但都扩展了 `config.yaml`，需要注意配置模型冲突。

---

*扫描日期：2026-07-11 · 基于 `main` 分支全量代码库扫描。不包含任何代码变更。*
