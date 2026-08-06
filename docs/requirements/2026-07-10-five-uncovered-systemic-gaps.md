# aero-id — 五组代码级证据驱动的系统性缺口（未被任何现有文档覆盖）

> **日期**: 2026-07-10
> **视角**: 资深架构师 / 产品经理
> **方法**: 全量代码扫描（`internal/` 全部 ~45 个包、22 份迁移 SQL、`cmd/`、`configs/`、`api/proto/`），与 `docs/requirements/` 下 29 份、`docs/REVIEW_*` 下 8 份现有分析文档逐主题交叉比对
> **承诺**: 以下 5 个方向在现有全部 37 份分析文档中 **未以任何形式被系统性覆盖**（零命中）
> **约束**: 每个方向必须满足 ① 有明确代码级证据（文件名 + 行号 + 逻辑），② 在全部现有分析中零系统性覆盖，③ 从产品经理 + 架构师双视角论证产出价值

---

## 前置：本文的独特定位

已有 37 份分析文档（累计覆盖 70+ 个方向）覆盖了以下类别：

| 类别 | 覆盖方向数 | 代表文档 |
|------|-----------|---------|
| **新功能 / 产品特性** | ~28 | 群聊、文件版本、@提及、移动端 SDK、WebSocket…… |
| **架构演进 / 基础设施** | ~22 | 多租户、分布式限流、消息归档、多区域存储…… |
| **运维成熟度 / 可观测性** | ~12 | SLO 框架、混沌测试、配置漂移检测、零停机迁移…… |
| **安全 / 合规** | ~6 | 静态加密、SPKI 证书固定、内容安全检测…… |

本文关注的 5 个方向在以上所有分析中 **未以任何形式被系统覆盖**——不是产品 wishlist，而是代码级证明的工程缺口。

---

## 方向一：Notification 表无限增长与数据生命周期缺失

### 严重性

**运维 · 中**

### 问题描述

`notifications` 表是整个平台的核心写入路径之一——每次"消息已发送"、"评论已回复"、"已读回执"都会写入一条记录。但当前 **没有任何机制可以限制这张表的无限增长**。

### 代码级证据

**证据 1：Notification 领域模型无 TTL / 生命周期字段**

```go
// internal/domain/notification.go:16-28
type Notification struct {
    ID             ID
    RecipientUID   string
    Type           string
    ActorUID       string
    SubjectType    string
    SubjectID      string
    ReadAt         *time.Time
    CollapsedCount int
    CreatedAt      time.Time
}
```

没有 `ExpiresAt`、`ArchivedAt`、`TTL`、`RetentionPolicy` 等字段。`ReadAt` 是唯一的生命周期标记，但它只表示"用户已读"而非"可删除"。

**证据 2：Repository 层无任何硬删除或批量清理方法**

```go
// internal/repository/notification.go — 全接口
interface NotificationRepository {
    Create,
    ListByRecipient,
    ListSince,
    FindRecentByTypeAndActor,
    IncrementCollapsedCount,
    MarkRead,
    MarkAllRead,
    CountUnread,
    // ← 没有 HardDeleteBefore, DeleteByRecipient, ArchiveByAge 等方法
}
```

整个接口没有提供任何数据清理手段。`MarkRead` 仅设置 `read_at` 时间戳，`MarkAllRead` 同理。

**证据 3：Notification 迁移无过期索引**

```sql
-- migrations/000009_create_notifications.up.sql
CREATE TABLE notifications (
    id            UUID PRIMARY KEY,
    recipient_uid TEXT NOT NULL,
    type          TEXT NOT NULL,
    ...
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_notifications_recipient ON notifications(recipient_uid, created_at DESC);
CREATE INDEX idx_notifications_unread    ON notifications(recipient_uid) WHERE read_at IS NULL;
-- 没有基于 created_at 的分区或清理索引
```

后续的性能索引迁移（000015, 000017）只关注了查询性能，没有建立任何 TTL 分区或过期清理机制。

**证据 4：Dispatcher 只创建，永不删除**

```go
// internal/notification/dispatcher.go:60-65
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    n := d.notificationFor(e)
    // ...
    if err := d.notifs.Create(ctx, n); err != nil {
        return err
    }
    // ...
}
```

Dispatcher 是 notification 的唯一写入入口。它只创建，不触发任何清理。每次活跃交互都会持久化一条记录。

### 增长推算

| 场景 | 每日估算（1000 活跃用户） | 年增长 |
|------|--------------------------|--------|
| 发送消息（每用户 5 条/天 → 2 条通知） | 10,000 条 | 3,650,000 条 |
| 评论回复（每用户 3 条/天 → 1 条通知） | 3,000 条 | 1,095,000 条 |
| 已读回执（每用户 5 条/天） | 5,000 条 | 1,825,000 条 |
| **总计** | **18,000 条/天** | **~6,570,000 条/年** |

对一个千万级通知的表，`CountUnread` 的 `WHERE read_at IS NULL` 索引扫描仍然高效，但全表 VACUUM、备份、恢复时间都会线性增长。

### 为什么现在就应该关注

1. **运维成本线性增长** — 日常 VACUUM、pg_dump、DDL 变更的时间随表大小线性增长。1000 万行 → 1 亿行的迁移窗口完全不同。
2. **GDPR/合规风险** — 用户的"right to erasure"需要能够在合理时间内删除所有个人通知记录。当前只能逐条软标记。
3. **SSE 重放性能下降** — `ListSince` 查询需要扫描越来越多已读通知。即使有 `(recipient_uid, created_at DESC)` 索引，表基数的增长最终会推高 P99 延迟。

### 建议方向

- **引入 TTL 分区** — 按月/季度对 `notifications` 做表分区，旧分区自动 DROP 或 TOAST 到归档存储。
- **提供硬删除 API** — 为 GDPR 合规暴露 `DELETE /v1/notifications/expired` 或批量清理端点。
- **自动清理已读超期通知** — 在后台协程中配置 `read_retention`（如 30 天），到期后批量硬删除已读通知。
- **归档策略** — 将超过 6 个月的通知导出到冷存储（如 S3 Parquet），数据库中只保留摘要。

---

## 方向二：跨实体一致性缺口 — 软删除与悬挂引用

### 严重性

**正确性 · 中高**

### 问题描述

aero-id 的实体间存在大量关联查询：评论挂在文件/消息上、文件被消息引用、文件被共享给其他用户。但这些关联查询在处理 **软删除的目标** 时，缺乏正确处理策略，可能导致：

- 搜索结果返回已删除的内容
- 评论列表显示指向已删除文件的评论
- 已删除文件的共享记录继续存在

### 代码级证据

**证据 1：Search 服务不对搜索结果做软删除过滤**

```go
// internal/search/search.go:43-60
func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error) {
    actor, err := domain.MustActor(ctx)
    // ...
    for _, kind := range kinds {
        hits, err = s.searchKind(ctx, actor, query, limit, kind, hits)
        // 不对 hits 做任何"目标是否已软删除"的二次校验
    }
    return hits, nil
}
```

搜索消息、评论、文件时，`SearchMessages`/`SearchComments`/`SearchFiles` 在 repo 层使用了 `WHERE deleted_at IS NULL`，但搜索结果中的 `Comment.TargetID` 对应的文件/消息 **可能已经被软删除**。SearchHit 不提供目标当前状态的指示。

**证据 2：评论列表不验证目标实体存在性**

```go
// internal/comment/service.go — List 方法（简化）
func (s *commentService) List(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) (*CommentList, error) {
    actor, err := domain.MustActor(ctx)
    // ...
    comments, next, err := s.comments.ListByTarget(ctx, t, target, page)
    // 没有验证 target 对应的文件/消息是否存在且未删除
    // 也没有检查 target 的 owner 是否已屏蔽当前用户
    // ...
}
```

当 `target_id` 对应的文件被硬删除后（GC sweep），评论列表尝试按 `target_type + target_id` 索引扫描——索引本身仍然有效，但返回的数据已无实际含义。

**证据 3：文件共享记录无级联清理**

```go
// internal/file/repository_share.go — 共享接口
type FileShareRepository interface {
    Create(ctx context.Context, fileID domain.ID, granteeUID string) error
    Delete(ctx context.Context, fileID domain.ID, granteeUID string) error
    ListByGrantee(ctx context.Context, granteeUID string, page domain.Page) ([]*domain.File, string, error)
    // ← 没有 OnFileDelete 级联方法
}
```

当文件被软删除后，`file_shares` 表中该文件的共享记录仍然存在。`ListSharedWithMe` 查询返回的 `files` 包含已删除文件。service 层在返回前会补充一次 `Get` 检查来过滤已删除文件，但这产生了 N+1 查询。

**证据 4：Message 的 file_id 引用无一致性保障**

```go
// internal/domain/message.go
type Message struct {
    // ...
    FileID *ID  // 引用文件——但文件可能已被删除
    // ...
}
```

`messages.file_id` 有显式的 `REFERENCES files(id)` 外键（迁移 000002），但外键约束 **在软删除的上下文中不做语义验证**。文件被软删除后，`file_id` 仍然有效（外键只检查存在性），但引用的文件实际已标记为删除。

### 产品影响

| 场景 | 当前表现 | 期望表现 |
|------|---------|---------|
| 用户搜索并找到一篇文章的评论，但文章已被删除 | 评论显示，但点击后 404 | 搜索结果中标注"内容已被删除"，或自动过滤 |
| 用户查看共享给我的文件列表，其中有已删除的文件 | 文件显示但访问时报 404 | 文件从共享列表中自动隐藏 |
| 用户打开历史消息，消息引用的附件已被发送者删除 | 消息中显示文件 ID，但下载失败 | 显示"附件已被删除"的友好提示 |

### 建议方向

- **引入"目标状态"缓存** — 在 list/comment/search 路径中，批量预查目标实体的 `deleted_at` 状态（用 `WHERE id IN (...) SELECT id, deleted_at`），在展示层做标注而非静默过滤。
- **软删除级联** — 在文件/消息删除时，自动标记关联的共享记录和消息引用的 `deleted_at`（不是硬删除，但使查询可以自然过滤）。
- **SearchHit 增加 target_status 字段** — 让客户端知道引用目标是否有效，避免 404 体验。
- **Materialized View 或宽表** — 为评论列表创建包含 `target_deleted_at` 的物化视图，零开销过滤已删除目标的评论。

---

## 方向三：Outbox 事件处理缺少聚合内顺序保证（Per-Aggregate FIFO）

### 严重性

**正确性 · 中**

### 问题描述

Transactional Outbox 是 aero-id 写路径的核心支柱。但当前的 Relay 实现保证的是 **全局最老优先** 处理顺序，而非 **按聚合类型分组的有序处理**。这意味着同一实体的连续事件（如同一文件的两条标签更新、同一消息的两次编辑）可能被乱序处理。

### 代码级证据

**证据 1：Relay 只做全局排序，不按 AggregateType 分组**

```go
// internal/outbox/repository.go — FetchUnpublished
func (r *gormRepo) FetchUnpublished(ctx context.Context, limit int) ([]*outbox.Event, error) {
    // ...
    .Where("published_at IS NULL AND (dead_letter IS NULL OR dead_letter = ?)", false).
    .Where("(last_retry_at IS NULL OR last_retry_at < ?)", backoffCutoff()).
    .Order("created_at ASC, id ASC").
    .Limit(limit)
    // ↑ 全局按 created_at 排序，没有按 aggregate_type + aggregate_id 分组
}
```

这意味着一批（默认 100 条）事件可能包含：

```
事件 1: aggregate=message, id=A, type=message.sent
事件 2: aggregate=file,    id=B, type=comment.replied  ← 不同聚合
事件 3: aggregate=message, id=A, type=message.read     ← 与事件1同聚合
```

如果事件 1 先处理成功，事件 3 处理失败（重试）——此时事件 3 被标记为 failed，下次 tick 在 backoff 后才能处理。在此期间，外部消费者看不到对 message A 的"已读"通知。

**证据 2：Handler 链无聚合级别的事务或隔离**

```go
// internal/outbox/chain.go — Chain 只是顺序调用
type Chain struct {
    handlers []Handler
}

func (c *Chain) Handle(ctx context.Context, e *Event) error {
    for _, h := range c.handlers {
        if err := h.Handle(ctx, e); err != nil {
            return err  // 一旦失败，后续 handler 都不执行
        }
    }
    return nil
}
```

Chain 不感知事件间的聚合关系。没有"这个聚合的下一个事件必须在前一个完成后才能处理"的保证。

**证据 3：Dispatcher 写 notification 时不做聚合顺序校验**

```go
// internal/notification/dispatcher.go — 每个事件独立处理
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    n := d.notificationFor(e)
    // 不检查同聚合的前序事件是否已经被处理
    // 不携带顺序信息到 notification 记录中
}
```

Dispatcher 不关心事件的顺序——每个事件独立生成一条通知。但当两个事件与同一个主题相关时（如"用户 A 发送消息" + "用户 A 撤回消息"），外部消费者看到通知的顺序可能与业务发生顺序相反。

### 场景分析

| 场景 | 当前行为 | 问题 |
|------|---------|------|
| 文件被编辑两次，产生两个 `file.updated` 事件 | 乱序处理：先处理了第二次编辑的 webhook 通知，再处理第一次编辑 | 外部消费者看到先新后旧的状态 |
| 消息先发送后撤回，产生 `message.sent` + `message.deleted` | 如果 `deleted` 先于 `sent` 处理 | 外部消费者看到撤回事件但没有看到发送事件 |
| 评论被创建后立即编辑 | `comment.replied` 通知可能携带旧内容 | 收件人收到通知后看到的评论内容和发送时不同 |

### 建议方向

- **引入 aggregate-based partitioning** — 在 Relay 内部按 `(aggregate_type, aggregate_id)` 分桶，同一桶内的事件严格顺序处理。每个桶可以独立并行。
- **事件携带 sequence_number** — 每个事件增加 `aggregate_sequence` 字段（单调递增），Dispatcher/Webhook 在处理时验证不跳过中间序列。
- **Webhook Payload 携带顺序信息** — 让外部消费者能够检测乱序交付并做重排。
- **考虑 Process Manager / Saga** — 对于需要多事件协调的业务流程，引入显式的状态机模式。

---

## 方向四：Webhook 投递的幂等性缺失与"侧效先于提交"竞态

### 严重性

**正确性 · 中高**

### 问题描述

Outbox Webhook 是 aero-id 对外事件驱动集成的唯一通道。但当前架构存在一个关键的 **侧效先于提交**（side-effect before commit）问题：webhook 的 HTTP POST 在 `MarkPublished` 之前执行，如果 `MarkPublished` 失败，事件会被重新投递，但外部接收方 **没有幂等键来安全去重**。

### 代码级证据

**证据 1：Webhook 投递在 MarkPublished 之前完成**

```go
// internal/outbox/relay.go:108-135
for _, e := range events {
    // ...跳过 backoff 中的事件...
    if err := r.handler.Handle(ctx, e); err != nil {  // ← webhook POST 在这里（通过 Chain）
        // 处理失败：标记为失败
        r.repo.MarkFailed(ctx, e.ID, e.RetryCount+1, r.maxRetries, now)
        continue
    }
    published = append(published, e.ID)  // ← 成功的 + webhook 200 响应已发出
}
// ...
if err := r.repo.MarkPublished(ctx, published); err != nil {  // ← 提交标记在最后
    r.log.Warn("outbox: mark published failed; will retry", zap.Error(err))
    return 0  // ← 所有已成功投递的事件都会被重新抓取并再次投递
}
```

当 `MarkPublished` 失败（网络抖动、连接池耗尽、瞬时高负载）：

1. 事件 1-5 的 webhook POST 已经成功发出（外部接收方已收到 200）
2. `MarkPublished([1,2,3,4,5])` 失败
3. Relay 返回 0
4. 下一个 tick：`FetchUnpublished` 再次抓取事件 1-5
5. Handler 再次执行 → webhook POST 再次发出

**证据 2：Webhook Payload 无幂等键**

```go
// internal/outbox/webhook.go:37-45
type webhookPayload struct {
    ID            string          `json:"id"`
    EventType     string          `json:"event_type"`
    AggregateType string          `json:"aggregate_type"`
    AggregateID   string          `json:"aggregate_id"`
    Payload       json.RawMessage `json:"payload"`
    CreatedAt     time.Time       `json:"created_at"`
    // ← 没有 IdempotencyKey、DeliveryAttempt、Sequence 等字段
}
```

`ID` 是事件的主键 UUID，对于幂等性来说可以作为去重依据——但当前没有通过 HTTP 头（如 `Idempotency-Key` 或 `X-Event-ID`）明确传达给接收方，也没有机制引导接收方如何使用它。

**证据 3：Dispatcher 同样存在侧效先于 MarkPublished 的问题**

```go
// internal/notification/dispatcher.go:60-75
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    n := d.notificationFor(e)
    // ...block check...
    if err := d.notifs.Create(ctx, n); err != nil {  // ← 通知行先写入
        return err
    }
    if d.pusher != nil {
        d.pusher.Publish(n.RecipientUID, n.Type, string(payload))  // ← SSE 推送也在 MarkPublished 之前
    }
    return nil
}
```

Dispatcher 在写入 notification 行并推送 SSE 后返回 nil——此时 Relay 的 `MarkPublished` 尚未执行。如果 `MarkPublished` 失败：

1. 通知行 **已经提交**（`notifs.Create` 在 Dispatcher 的事务中）
2. SSE 推送 **已经发出**
3. `MarkPublished` 失败
4. 下次 tick 重新分发事件 → Dispatcher 再次写入通知行
5. 虽然后续会被 `collapsed_count` 合并，但 SSE 推送会第二次发出相同事件

### 产品影响

| 场景 | 概率 | 影响 |
|------|------|------|
| Relay 执行 `MarkPublished` 时 DB 连接池满 | 低（生产环境有连接池） | Webhook 接收方收到重复事件 |
| 优雅关闭时最后一次 drain 的 `MarkPublished` 被中断 | 可能（stop → drain → 系统关闭） | 重启后事件重新投递，接收方收到重复 |
| Dispatcher 写入 + MarkPublished 失败 | 低 | 用户看到重复的 SSE 通知 |

### 建议方向

- **在 Webhook Payload 中添加 `X-Event-ID` HTTP 头** — 明确要求接收方以该 ID 作为幂等键，文档化建议接收方做去重。
- **引入投递确认机制** — webhook 响应返回 `200` + `{"accepted": true, "idempotency_key": "..."}`，由 aero-id 记录确认号。如果确认号不匹配，重试。
- **改变处理顺序** — 先 `MarkPublished`（标记为"投递中"），再执行 webhook，在成功响应后标记为"已完成"。这可避免重试导致重复，但需要额外的投递中状态。
- **考虑 at-least-once 为默认语义** — 明确文档化 webhook 的交付语义为 at-least-once，提供幂等键支持，由接收方负责去重。

---

## 方向五：客户端声明的 MIME 类型缺乏服务端验证

### 严重性

**安全 · 中**

### 问题描述

文件上传时，`mime_type` 完全来自客户端的 HTTP `Content-Type` 头或 multipart 表单项。服务端 **不做任何基于内容的 MIME 类型验证**（如 magic bytes 检测）。有 MIME allow-list 配置时，它只是检查客户端字符串，而不是实际文件内容。

### 代码级证据

**证据 1：MIME 类型完全信任客户端声明**

```go
// internal/service/file.go:264-270
func (s *fileService) mimeAllowed(contentType string) bool {
    if len(s.allowedMime) == 0 {
        return true  // 无白名单 → 所有类型放行
    }
    mt, _, err := mime.ParseMediaType(contentType)
    if err != nil {
        return false
    }
    return s.allowedMime[strings.ToLower(mt)]
    // ↑ 只检查客户端传入的字符串，不读取实际内容
}
```

当 `allowed_mime_types` 配置为 `["image/png", "image/jpeg"]` 时，以下攻击是可能的：

1. 客户端发送 `Content-Type: image/png`
2. 服务端检查通过
3. 实际文件内容是 `application/octet-stream`（可执行文件）
4. 文件被存储且 mime_type = "image/png"

**证据 2：Upload handler 同样透传客户端声明的类型**

```go
// internal/file/service_crud.go:32-33（feature 包实现）
if !s.mimeAllowed(in.MimeType) {
    return nil, apperr.BadRequest("file.unsupported_type", "file content type is not allowed")
}
// ...
MimeType: in.MimeType,  // ← 最终写入数据库的 mime_type 就是客户端声明的
```

文件上传的完整路径中，没有一步对文件内容做 MIME 检测。`sha256` 校验和计算只用于去重，不用于类型验证。

**证据 3：Presigned Upload 同样信任客户端**

```go
// internal/file/service_upload.go:30-32
func (s *fileService) PresignUpload(ctx context.Context, in PresignUploadInput, ttl time.Duration) (*PresignUploadResult, error) {
    // ...
    if !s.mimeAllowed(in.MimeType) {  // ← 还是只检查字符串
        return nil, apperr.BadRequest("file.unsupported_type", ...)
    }
```

对于预签名直传（S3 direct upload），更无服务端检测机会——客户端直接 PUT 到 S3，`CompleteUpload` 时只 `Stat` 文件大小，不做内容验证。

**证据 4：配置允许 MIME 空列表 = 完全无过滤**

```yaml
# configs/config.yaml
storage:
  allowed_mime_types: []  # 空 = 接受任意类型
```

在生产部署中，如果团队忘记配置这个列表，所有文件类型都会被接受。

### 攻击场景

| 场景 | 危害 |
|------|------|
| 攻击者上传 `.exe` 伪装为 `image/png` | CDN/浏览器可能根据 `Content-Type` 头将其作为图片执行（如果后续提供下载时透传 mime_type） |
| 上传 HTML 文件伪装为 `text/plain` | 如果文件通过 `/files/dl` 提供且浏览器嗅探，可能触发 XSS |
| 上传 SVG 文件（含 JavaScript）伪装为 `image/png` | SVG 在浏览器中渲染时可能执行内嵌脚本 |
| 绕过 MIME 白名单上传大型二进制文件 | 存储空间和带宽被滥用，而此类滥用可能被误判为"合法的图片上传" |

### 建议方向

- **引入服务端 MIME 检测** — 在 `Create` 路径中，使用 `net/http#DetectContentType`（基于前 512 字节 magic bytes）或集成 `xwb1989/tcnative`/`filetype` 库做内容检测。
- **双重验证策略** — 客户端声明的 MIME 用于快速过滤，服务端检测的类型作为最终写入值。两者不一致时：
  - 如果服务端类型不在 allow-list 中 → 拒绝
  - 如果服务端类型与客户端声明不匹配 → 记录告警日志
- **Presigned Upload 增加回调校验** — 在 `CompleteUpload` 时，从 S3 下载前 N 字节做 MIME 检测，而不是只校验大小。
- **提供类型覆盖白名单** — 明确文档化并启用 `allowed_mime_types` 为默认安全值（而非空列表）。

---

## 附录：交叉比对方法

### 搜索方法

对每个方向的候选关键词，在以下文档集合中执行 `grep -rl` 搜索：

```
docs/requirements/*.md         (29 份)
docs/REVIEW_2026-06-30_*.md    (8 份)
ROADMAP.md                     (1 份)
README.md                      (1 份)
docs/CHANGELOG.md              (1 份)
docs/TODO.md                   (1 份)
```

### 方向一搜索词

`notification.*lifecycle|notification.*retention|notification.*TTL|notification.*archive|notification.*hard.*delete|notification.*cleanup|notification.*purge|notification.*data.*growth|notif.*table.*growth|notification.*unbounded`

**结果**: 零命中。

### 方向二搜索词

`cross.*entity.*consist|soft.*delete.*search|search.*result.*stale|deleted.*content.*search|dangling.*ref.*comment|orphan.*comment.*target|comment.*target.*deleted`

**结果**: 零命中。

### 方向三搜索词

`outbox.*order|event.*ordering|FIFO.*outbox|outbox.*FIFO|event.*sequence|aggregate.*order|outbox.*sequence|per.*aggregate.*order`

**结果**: 零命中。

### 方向四搜索词

`webhook.*idempot|webhook.*dedup|webhook.*redeliver|webhook.*delivery.*guarantee|event.*redelivery|outbox.*duplicate|webhook.*duplicate|markPublished.*crash|side.effect.*before.*commit`

**结果**: 零命中。

### 方向五搜索词

`mime.*detect|mime.*magic|magic.*byte|content.*type.*verify|mime.*sniff|mime.*infer|mime.*validate.*content|client.*declared.*mime|mime.*bypass|file.*type.*detect|content.*inspection`

**结果**: 零命中。
