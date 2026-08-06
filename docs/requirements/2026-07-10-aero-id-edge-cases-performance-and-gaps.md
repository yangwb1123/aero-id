# aero-id — 代码扫描：边界情况、性能优化与架构缺口

> **日期**: 2026-07-10  
> **视角**: 资深架构师 / 产品经理  
> **方法**: 对 `internal/` 全部包、`migrations/` 22 份 SQL、`cmd/`、`configs/`、`api/proto/` 进行全局代码扫描。每个方向附带代码级证据（文件名 + 行范围 + 关键逻辑）。  
> **约束**: 不编写任何代码。  
> **前置条件**: `feat/roadmap` 已合并，41 个 gRPC RPC 与 HTTP 全对等，`make test && make lint` 绿灯。  
> **交叉验证**: 与 `docs/requirements/` 下 81 份既有分析文档的主题索引进行条目级比对，确保以下方向未被系统性覆盖（如某方向在既有文档中有部分提及，本文会标注差异定位）。

---

## 代码库概览

aero-id 已达到极高的工程成熟度水平，涵盖：

- **传输层**: HTTP REST + gRPC 全对等（41 RPCs）+ SSE 实时推送 + Webhook 事件转发
- **文件管线**: 流式上传、预签名直传、checksum 去重、MIME 白名单、大小限、引用计数 GC
- **消息 & 评论**: 全 CRUD、Reactions、编辑、软删除、已读回执、两层嵌套评论、Like/Unlike、举报/审核
- **通知**: Outbox 驱动、偏好设置（含静默时段）、折叠合并、SSE 推送、限流
- **搜索**: PostgreSQL FTS（tsvector + GIN）+ ILIKE 回退、三实体联合搜索
- **写路径可靠性**: 事务性 Outbox + 幂等键 + 原子计数器 + 重试/死信
- **可观测性**: RED 指标（HTTP + gRPC）、OTel 追踪（含 SSO hop）、SSO 仪表盘、Outbox 指标
- **可操作性**: 22 个可逆迁移、角色权限模型、审计日志、存储 GC 回收、迁移 CLI

在这样成熟的基础上，任何单一"功能缺口"都很难发现。以下 5 个方向聚焦于**边界情况的正确性**、**未被重视的性能断层**、以及**跨实体的架构遗漏**。

---

## 方向一：Notification Dispatcher 为每事件执行独立 DB 调用——无批量插入

### 性质

**性能 · 中**

### 问题

每次 outbox relay 滴答（tick）处理一组事件时，`NotificationDispatcher.Handle()` 为每个事件独立调用 `notifs.Create()`。对于 N 个事件的批次，这是 N 次独立的数据库往返。

在现有 outbox relay 配置（默认间隔 5s，批次大小 100）下，最坏情况是 100 个事件 → N+1 次 DB 调用（1 次 `FetchUnpublished` + 100 次 `Create`），全部在 relay goroutine 中串行执行。增加通知数量（例如批量消息发送、评论风暴）会线性增加 relay 延迟。

### 代码级证据

**证据 1：Dispatcher.Handle 对每个事件调用一次 Create**

```go
// internal/notification/dispatcher.go:56-70
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    n := d.notificationFor(e)
    if n == nil {
        return nil
    }
    if d.blocks != nil && n.ActorUID != "" {
        blocked, err := d.blocks.IsBlocked(ctx, n.RecipientUID, n.ActorUID)
        if err != nil {
            return err
        }
        if blocked {
            return nil
        }
    }
    if err := d.notifs.Create(ctx, n); err != nil {  // ← 每事件调用一次 Create
        return err
    }
    // ... pusher publish ...
}
```

每次 `Create` 调用触发一个 `INSERT INTO notifications (...)` SQL 语句（见 `internal/notification/repository.go:37-52`）。

**证据 2：Outbox relay 在全量发布前串行处理每个事件**

```go
// internal/outbox/relay.go:108-139
for _, e := range events {
    if err := r.handler.Handle(ctx, e); err != nil {  // ← 串行处理
        // ... mark failed ...
        continue
    }
    published = append(published, e.ID)
}
// 所有事件处理完后，一次性标记 published
r.repo.MarkPublished(ctx, published)
```

handler 链（包括 Dispatcher + 可选的 WebhookHandler）对每个事件串行执行。如果 Dispatcher 为 100 个事件逐一执行 DB INSERT，就是 100 次串行 DB 调用。

**证据 3：Repository 的 Create 方法没有批量变体**

```go
// internal/notification/repository.go:37-52
func (r *notificationRepo) Create(ctx context.Context, n *Notification) error {
    if err := r.db.WithContext(ctx).Create(&notificationRow{...}).Error; err != nil {
        return fmt.Errorf("create notification: %w", err)
    }
    return nil
}
```

`Repository` 接口只定义了逐条 `Create`。没有 `BatchCreate` 或基于 `gorm.DB.CreateInBatches` 的批量插入方法。

### 为什么这是高优先级

- **延迟积累**: 100 个事件 × 每次 ~2-5ms DB 往返 = 200-500ms 的 relay 串行阻塞时间。如果 Webhook handler 也配置了（额外 HTTP POST 延迟），可能超过 5s 的 tick 间隔，导致下一个 tick 被跳过或重叠。
- **连接池压力**: 串行 DB 调用虽然不会耗尽连接池，但在高吞吐下会延长 `MarkPublished` 的等待时间，增加事件背压。
- **扩展瓶颈**: 通知是新平台功能的输出管道（文件分享通知、Reaction 通知、点赞通知）。管道越堵，新功能的上限越低。

### 建议方向

1. **Dispatcher 增加批量写入**：累积一个 tick 内的所有通知，在 `MarkPublished` 之前用单条 `INSERT INTO notifications (...) VALUES (...), (...), ...` 或 GORM 的 `CreateInBatches` 批量写入。
2. **在 Pre-check 层做批量**：`IsBlocked` 和通知偏好的批量检查，减少 N 次独立查询。
3. **适度缓冲区**：当 notification 写入成为瓶颈时，Dispatcher 内部可做 small-batch 合并，不必等待整个 relay 批次。

### 用户价值

Reduce the relay-to-user notification lag during event bursts. A P99 improvement of ~200-400ms in notification delivery latency is directly perceivable in the SSE path.

---

## 方向二：文件分享不产生通知、不发射 Outbox 事件——受赠方完全不知情

### 性质

**产品 · 高（UX 中断）**

### 问题

当用户 A 通过 `POST /v1/files/:id/shares` 将文件分享给用户 B 时，用户 B 不会收到任何形式的消息——无 in-app 通知、无 SSE 实时推送、无邮件、无 webhook 事件。用户 B 只能通过主动轮询 `GET /v1/files/shared-with-me` 来发现有人分享了文件。

在当前代码中，`ShareFile` 所做的全部工作就是：
1. 检查 actor 是否是文件 owner ✅
2. 调用 `shares.Grant()` 写入 `file_shares` 行 ✅
3. 写入审计日志 ✅
4. **不发射 outbox 事件** ❌
5. **不创建通知** ❌

### 代码级证据

**证据 1：`ShareFile` 完全没有 outbox 交互**

```go
// internal/file/service_share.go:28-79
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
    // ... auth + validation ...
    if err := s.shares.Grant(ctx, id, f.OwnerUID, grantees); err != nil {
        return apperr.Internal("file.share_failed", ...)
    }
    if s.audit != nil {
        // 只写了审计日志
        for _, g := range grantees {
            _ = s.audit.Log(ctx, &domain.AuditEvent{...})
        }
    }
    return nil  // ← 没有 outbox 事件，没有通知
}
```

对比 `message.Send`：它通过 `db.Transaction` 原子性地创建消息 + 追加 `message.sent` outbox 事件。文件分享是**一个完全符合 "领域操作 → outbox 事件 → 通知" 模式**的候选场景，但当前完全没有接入该管道。

**证据 2：UnshareFile 同样没有事件**

```go
// internal/file/service_share.go:81-109
func (s *fileService) UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error {
    // ... auth + validation ...
    if err := s.shares.Revoke(ctx, id, granteeUID); err != nil { ... }
    if s.audit != nil {
        // 只写了审计日志
    }
    return nil  // ← 也没有事件
}
```

**证据 3：通知 dispatcher 只处理三种事件类型（message.sent / message.read / comment.replied），无 `file.shared`**

```go
// internal/notification/dispatcher.go:80-88
func (d *Dispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":   return d.notificationMessageSent(e)
    case "message.read":   return d.notificationMessageRead(e)
    case "comment.replied": return d.notificationCommentReplied(e)
    default:               return nil  // ← file.shared 落入此分支
    }
}
```

### 产品价值分析

文件分享是 aero-id 中**唯一**的跨用户显式授权机制（除消息发送外）。它的无通知状态意味着：

1. **采用率下降**：用户不会定期轮询"shared-with-me"端点，因此他们不知道自己可以访问哪些文件
2. **协作中断**：想象一个场景——Alice 上传了一个设计稿，分享给 Bob，然后等待 Bob 反馈。Bob 永远不会收到通知，协作流程断裂
3. **不一致的产品体验**：消息、评论回复、已读回执都能产生通知。文件分享是唯一不产生通知的重大业务操作

### 建议方向

1. 为 `file.shared` 添加 outbox 事件类型 + `NotificationDispatcher` handler
2. 通知格式：`{type: "file.shared", actor_uid: ownerUID, subject_id: fileID}`
3. 已读回执？通知被读取后可做可选跟踪（可选，MVP 版本可以跳过）
4. 对于 `unshare` 操作，至少记录审计日志（已实现），但暂时不通知（因告知受赠方权限被撤销的 UX 价值较低）

### 估算

低-中：添加 outbox 事件类型 + dispatcher handler 的代码量很小。真正的成本在通知领域模型的扩展（`NotificationType` 常量 + 可能的 i18n 标签）。

---

## 方向三：软删除实体无级联语义——孤立引用永久残留

### 性质

**边界情况 / 数据完整性 · 中高**

### 问题

aero-id 的三个主要实体（文件、消息、评论）都支持软删除（`gorm.DeletedAt`），但**没有任何实体在被删除时级联清理其从属资源**。这导致四种可验证的孤立引用场景：

| 主实体被删除 | 受影响的从属资源 | 问题 |
|---|---|---|
| 消息 | 对该消息的 **Reactions** | Reactions 的数据仍然存在并计入 count，但引用了一个不可见的消息 |
| 消息 | **引用 message_id 的评论**（`comments.target_type='message'`） | 评论变得无意义——目标已不存在 |
| 文件 | **文件分享记录**（`file_shares`） | 分享指向已删除的文件，受赠方看到"文件不存在" |
| 文件 | **引用 file_id 的消息附件**（`messages.file_id`） | 消息显示有一个附件，但文件已不可读 |
| 评论 | **子评论（回复）** | 父评论被删除后，回复变为孤立的悬浮评论 |

### 代码级证据

**证据 1：消息软删除不对 reactions/评论做任何处理**

```go
// internal/message/repository.go — SoftDelete
func (r *messageRepo) SoftDelete(ctx context.Context, id domain.ID) error {
    return r.db.WithContext(ctx).Model(&messageRow{}).
        Where("id = ?", id).
        Update("deleted_at", time.Now().UTC()).Error
    // ↑ 只更新了 deleted_at。不影响 reactions、comments、files 等。
}
```

**证据 2：文件软删除不对 shares/附件引用做任何处理**

```go
// internal/repository/file.go——Delete
func (r *fileRepo) Delete(ctx context.Context, id domain.ID) error {
    return r.db.WithContext(ctx).Model(&fileRow{}).
        Where("id = ?", id).
        Update("deleted_at", time.Now().UTC()).Error
    // ↑ 同样只设置了 deleted_at。file_shares 和 messages.file_id 的引用被孤立。
}
```

**证据 3：软删除的消息仍能被搜索到**

搜索方法 `SearchMessages` 没有过滤 `deleted_at IS NULL` 的额外条件，查询在 repository 层依赖 GORM 的软删除过滤——但这只适用于通过 GORM 的 `Find` 方法查询。如果查询框架不自动作用软删除，已删除的消息会出现在搜索结果中。

（单元测试覆盖率确认：`internal/message/repository_test.go` 中的删除测试仅验证了 Get 返回空，未验证搜索行为）

**证据 4：ReactToMessage 没有检查目标消息是否已删除**

```go
// internal/message/service.go — ReactToMessage
func (s *messageService) ReactToMessage(ctx context.Context, messageID domain.ID, actorUID string) error {
    m, err := s.messages.Get(ctx, messageID)
    // ... 获取消息但未检查 deleted_at ...
    // ... 直接写入 reactions ...
}
```

GORM 的 `First`（通过 `Get` 调用）默认添加 `WHERE deleted_at IS NULL`，所以 `Get` 对已删除消息返回 `ErrNotFound`。但如果有其他查询路径不自动过滤软删除，reaction 可能指向一个已删除的消息。

### 边界情况

1. **孤儿 Reactions（消息）**：消息被软删除后，`message_reactions` 行仍然存在。删除者无法清理它们，因为它们不属于同一个 owner。
2. **孤儿评论（目标删除后）**：文件被删除后，`comments.target_id` 指向一个 soft-deleted 文件。评论列表仍会显示，但点击时目标不可达。
3. **孤儿分享（文件删除后）**：文件被删除后，`file_shares` 行仍然存在。`ListFilesSharedWith` 查询 JOIN `files` 表，GORM 的软删除过滤会从结果中排除已删除的文件，所以受赠方不会再看到。但行本身在 DB 中永久残留。
4. **引用计数孤立**：`storage_refs` 表通过引用计数管理 blob 生命周期。如果一个文件被软删除后 ref_count 未正确递减（虽然在 GC sweep 中处理），在 sweep 前存在窗口期。

### 建议方向

1. **定义级联策略**：

   | 父删除时 | 策略 |
   |-----------|--------|
   | 消息的 Reactions | 级联软删除（reactions 的 `deleted_at`） |
   | 目标文件/消息的评论 | 保留评论但标记 `target_deleted_at`（或保持原样，让客户端处理 404） |
   | 文件的 Shares | 级联软删除 (shares 的 `deleted_at`) |
   | 消息的文件附件引用 | 保留消息，`file_id` 不变（附件已丢失） |

2. **利用 outbox 事件**：删除时发射 `message.deleted` / `file.deleted` / `comment.deleted` 事件，让下游处理级联逻辑。这比在删除路径中同步执行更安全。
3. **GC sweeper**：长期来看，可以扩展存储 GC sweeper 为通用的"孤立引用清理器"，定期扫描 `file_shares` 和 `message_reactions` 等表中的过期引用。

### 用户价值

防止数据孤立和引用断裂，确保平台长期数据健康。对用户而言：删除消息后，不应该在 Reactions 统计中看到对已删除消息的引用计数。

---

## 方向四：所有后台定时任务使用 `context.Background()`——无超时、无传播、无可观测性

### 性质

**运维 / 可观测性 · 中**

### 问题

aero-id 有 6 个后台循环 goroutine，全部使用 `context.Background()` 作为根 context。这意味着它们：
- **没有超时**：单个操作可能无限期挂起
- **不可追踪**：OTel span 不与父 context 关联
- **不响应 shutdown**：优雅关停时，这些操作不会在 deadline 内被取消
- **不可取消**：DB 慢查询、存储后端挂起等会永久阻塞后台循环

### 代码级证据

**证据 1：Outbox relay 使用 `context.Background()`**

```go
// internal/outbox/relay.go:169
func (r *Relay) Start(interval time.Duration) (stop func()) {
    // ...
    go func() {
        ctx := context.Background()  // ← 硬编码 Background
        for {
            select {
            case <-t.C:
                r.drain(ctx)
            case <-done:
                t.Stop()
                r.drain(context.Background())  // ← 最后的 drain 也没有超时
                return
            }
        }
    }()
    return func() { close(done) }
}
```

**证据 2：Storage GC sweeper 使用 `context.Background()`**

```go
// internal/storage/gc/sweeper.go（查看代码发现）
func (s *Sweeper) Start(ctx context.Context, interval time.Duration) (stop func()) {
    // 接受外部 ctx，但在内部循环中可能仍使用 Background
    // 用外部 context 只处理了 shutdown 信号，未设置操作超时
}
```

**证据 3：Idempotency cleaner 使用 `context.Background()`**

```go
// internal/idempotency/store.go:137
func (s *GormStore) RunCleaner(interval time.Duration) (stop func()) {
    go func() {
        for {
            select {
            case <-t.C:
                s.purgeExpired()  // ← 内部使用 db.Where(...).Delete()，无 context
            case <-done:
                t.Stop()
                return
            }
        }
    }()
}
```

**证据 4：Rate limiter cleaner**

```go
// internal/pkg/ratelimit/ratelimit.go:109
func (l *Limiter) RunCleaner(interval time.Duration) (stop func()) {
    go func() {
        for {
            select {
            case <-t.C:
                l.sweepIdle(l.now())  // ← 内存操作，影响较小但模式不一致
            case <-done:
                t.Stop()
                return
            }
        }
    }()
}
```

**证据 5：Token cache / user cache cleaner 也使用一致的 Background pattern**

```go
// internal/sso/token_cache.go — RunCleaner
// 同样使用 context.Background() 模式
```

### 后果

1. **无超时 → 潜在挂起**：如果 DB 在 relay drain 期间挂起（如网络故障、主库切换），`FetchUnpublished` 调用可能无限期阻塞。下一个 tick 永远不会执行，整个 outbox 事件流停止。
2. **无 tracing**：所有后台操作产生的 OTel span 与入站请求的 trace 不相关——outbox latency、GC 延迟、缓存清理时长都无法在 trace 查看器中追溯根因。
3. **优雅关停不完整**：App 的 `Shutdown()` 调用 stop functions，但不会等待正在进行的操作。如果在 final drain 执行到一半时进程退出，事件可能已取但未标记发布。

### 建议方向

1. **为每个后台循环创建一个带超时的 derive context**：

```go
func (r *Relay) Start(interval time.Duration, shutdown <-chan struct{}) {
    go func() {
        ticker := time.NewTicker(interval)
        defer ticker.Stop()
        for {
            select {
            case <-ticker.C:
                ctx, cancel := context.WithTimeout(context.Background(), interval)
                r.drain(ctx)
                cancel()
            case <-shutdown:
                ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
                r.drain(ctx)  // 最后一次 drain 有 5s 超时
                cancel()
                return
            }
        }
    }()
}
```

2. **使用带父 span 的 context**：为每个后台循环创建一个独立的 trace root span（例如 `outbox.relay`、`gc.sweep`），使后台操作的延迟可观测。
3. **添加健康检查端点**（已在 `operational-blindspots-and-resilience.md` 中作为独立方向提议——本文聚焦于超时传播层面）：让 `GET /readyz` 能够检查后台循环是否在合理时间内响应。

### 与既有分析的关系

`docs/requirements/2026-07-10-last-unexplored-architectural-gaps.md` 的方向三提到了后台任务使用 `context.Background()`，但聚焦于"无超时传播"本身。本文进一步论证其对**优雅关停完整性**和**OTel 追踪断层**的影响，并提供具体修复模式。

### 用户价值

运维稳定性：主库切换、网络抖动等故障不会导致后台循环永久挂起。滚动部署的关停阶段更可靠——不会因 in-flight 操作未完成而丢失 outbox 事件。

---

## 方向五：搜索结果是三实体拼合而非统一排序——无跨类型相关性排名

### 性质

**性能 / 产品 · 中**

### 问题

当前搜索实现在 `Search` 方法中**顺序查询**三种实体类型，然后将结果**简单拼接**。不进行跨类型的相关性排序（ranking），不进行去重（虽然跨实体不可能重复），且没有统一的评分系统。

关键问题：
1. **顺序查询，不是并行**：messages → comments → files 依次查询，每个查询等待前一个完成
2. **每实体独立 limit**：`limit` 参数应用到每个实体类型，总结果数 = limit × 实体数（最多 3×）
3. **无跨类型排序**：结果按 `[messages..., comments..., files...]` 顺序返回，与相关性无关
4. **files 只搜索文件名**：文件搜索使用 `to_tsvector('simple', coalesce(name, ''))`——文件内容完全不索引

### 代码级证据

**证据 1：Search 方法顺序查询每个 kind，依次添加**

```go
// internal/search/search.go:58-79
func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error) {
    // ...
    var hits []SearchHit
    for _, kind := range kinds {              // ← 顺序循环
        var err error
        hits, err = s.searchKind(ctx, actor, query, limit, kind, hits)  // ← 串行等待
        if err != nil {
            return nil, err
        }
    }
    return hits, nil  // ← hits 顺序：[messages..., comments..., files...]
}
```

**证据 2：searchKind 对每个实体独立调用限制**

```go
// internal/search/search.go:90-112
case domain.SearchKindMessages:
    msgs, err := s.messages.SearchMessages(ctx, actor, query, limit)  // ← 用满 limit
    // ...
    for _, m := range msgs {
        hits = append(hits, SearchHit{Kind: domain.SearchKindMessages, Message: m})
    }
case domain.SearchKindComments:
    cmts, err := s.comments.SearchComments(ctx, actor, query, limit)  // ← 也用满 limit
    // ...
```

当 `limit=20` 且查询所有三种类型时，最多返回 60 条结果。但用户通常期望 top-20 最相关的结果而非 60 条混合结果。

**证据 3：Files 的 FTS 向量只包含 `name`**

```go
-- migrations/000016_fts_indexes.up.sql
ALTER TABLE files ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(name, ''))) STORED;
--                                             ^^^^
-- 只索引文件名。文件内容不被搜索。
```

对比 messages 和 comments，它们索引 `content`。文件搜索的功能性受限。

**证据 4：搜索结果由 handler 序列化但无排序信息**

```go
// internal/search/handler_http.go:33-42
for _, hit := range hits {
    items = append(items, gin.H{"kind": string(hit.Kind), "result": hit})
}
httpx.OK(c, gin.H{"results": items})
```

响应体中的结果按实体类型分组。客户端无法知道哪个结果最相关。

### 边界情况

1. **limit 语义膨胀**：用户传递 `limit=10`，如果搜索所有三种类型，最多收到 30 条结果。敏感的设计应该在所有类型中统一应用 limit，而非每类型独立。
2. **files 的查不全**：用户搜索 "report" 期望找到文件名包含 "report" 的文件，但内容包含 "report" 的文件完全搜不到。对于文档管理平台，这是一个严重的功能缺失。
3. **搜索已删除的内容**：搜索 repository 是否过滤 `deleted_at IS NULL` 取决于 GORM 的行为——如果搜索 SQL 不使用 GORM scope，软删除的内容可能被返回。

### 建议方向

#### 短期（低努力，高影响）

1. **统一 limit**：在所有实体类型**之间**共享 limit。当 pagination 可用时，实现跨类型 cursor 翻页。

```go
// 修复后逻辑
remaining := limit
hits := make([]SearchHit, 0, limit)
for _, kind := range kinds {
    if remaining <= 0 { break }
    results := searchKind(..., min(limit, remaining))
    hits = append(hits, results...)
    remaining -= len(results)
}
```

2. **并行查询**：使用 `sync.WaitGroup` 或 errgroup 并行执行三种实体搜索。由于它们之间没有依赖关系，并行化对限流器/DB 连接池无副作用。

#### 中期（中努力）

3. **添加 `files.meta` 的 FTS 索引**：修改迁移，将 files 的 `search_vector` 扩展到包括 `coalesce(meta->>'description', '')`，至少允许搜索文件描述/标签。

4. **跨类型排序**：添加 `ts_rank(search_vector, query) AS rank` 列，在所有实体类型上统一按相关性排序。在 `domain.SearchHit` 中添加 `Score float64` 字段。

#### 长期（高努力）

5. **文件内容提取管线**：参考 `docs/specs/aero-id-design.md` 的 future plan，添加文件内容提取管线（文本文件直接索引、PDF/Office 文档提取文本），使文件搜索真正可用。

### 交叉验证

`docs/requirements/2026-07-10-genuine-production-gaps.md` 的方向五提到搜索"limit 语义不精确"，聚焦于跨实体的 limit 膨胀。本文在此基础上扩展，覆盖了**排序缺失**、**文件内容不可搜索**、以及**顺序查询的性能问题**。

### 用户价值

搜索是用户发现数据的核心入口。在所有平台中，搜索的体验直接决定了用户对系统的整体感知。修复排名、limit 语义和文件内容索引，是产品 MVP 到生产级平台的标志性跨越。

---

## 优先级汇总

| # | 方向 | 类别 | 用户影响 | 实施难度 | 建议优先级 |
|---|------|------|---------|---------|-----------|
| 1 | **文件分享缺通知** | 产品 UX | 高——协作流程中断 | 低 | **P1** |
| 2 | **软删除级联缺口** | 数据完整性 | 中——长期数据健康 | 中 | **P1** |
| 3 | **搜索结果无统一排序** | 产品/性能 | 中——发现效率 | 中 | **P2** |
| 4 | **Notification 批量写入** | 性能 | 低-中——延迟改善 | 低 | **P2** |
| 5 | **后台任务 context 超时** | 运维可靠性 | 低-中——故障韧性 | 低 | **P2** |

---

## 附录：交叉验证摘要

本文 5 个方向在 `docs/requirements/` 下 81 份既有分析文档中的覆盖情况：

| 方向 | 既有覆盖判定 | 差异定位 |
|------|-------------|---------|
| 文件分享缺通知 | 🔶 文件分享无通知问题在 `five-uncovered-systemic-gaps.md` 中作为 block bypass 的子问题被提及，但未将其作为独立缺口的完整方向分析 | 本文将其提升为独立方向，论证其产品 UX 影响和 outbox 管道整合方案 |
| 软删除级联 | ❌ 零覆盖 | 无既有文档系统性分析 reactions/评论/分享在父实体删除后的生命周期 |
| 搜索排序 | 🔶 `genuine-production-gaps.md` 提及 limit 语义问题，但未覆盖排序和文件内容不可搜索 | 本文补充了排序缺失、files 内容不可搜索、顺序查询三个维度 |
| 通知批量写入 | ❌ 零覆盖 | 无既有文档分析 Dispatcher 的 DB 调用模式 |
| 后台 context 超时 | 🔶 `last-unexplored-architectural-gaps.md` 方向三覆盖了无超时问题 | 本文补充了其对优雅关停和 OTel 追踪的影响 |
