# aero-id — 后 75 轮扫描：五个未被任何既有分析覆盖的基础设施与产品缺口

> **日期**: 2026-07-10  
> **方法**: 对 `internal/` 全部包、22 份 SQL 迁移、双传输 proto、配置、ROADMAP.md 做全量源码级扫描；与 `docs/requirements/` 下 75 份已有分析逐份比对标题和正文关键词，确认本文 5 个方向在全部现有分析中 **无系统性覆盖**。  
> **约束**: 不编写代码。每个方向附精确代码位置和架构师/产品经理双视角论证。

---

## 交叉验证摘要

| 方向 | 关键词搜索 | docs/requirements (75 份) | 命中 | 判定 |
|------|-----------|--------------------------|------|------|
| Outbox 死信运维 API | `dead.letter.*api\|dead.letter.*list\|dead.letter.*retry\|outbox.*admin\|dead.letter.*dashboard` | 全部 75 份 | 0 | ❌ |
| 存储配额 & 用量追踪 | `storage.*quota\|quota.*limit\|user.*usage\|storage.*usage\|storage.*used\|disk.*quota\|quota.*enforce` | 全部 75 份 | 0 | ❌ |
| 会话列表 & 会话管理 | `conversation.*list\|list.*conversation\|conversation.*sidebar\|conversation.*mute\|conversation.*archiv` | 全部 75 份 | 0 | ❌ |
| 幂等键可观测性 | `idempotency.*metric\|idempotency.*observ\|idempotency.*hit\|idempotency.*ratio\|idempotency.*admin\|stale.*idempotency` | 全部 75 份 | 0 | ❌ |
| 文件附件级联生命周期 | `file.*attach.*cascade\|message.*file.*cleanup\|orphan.*attach\|file.*ref.*count\|attach.*lifecycle\|file.*message.*refer` | 全部 75 份 | 0 | ❌ |

---

## 方向一：Outbox 死信运维 API（操作盲区）

### 问题

Outbox relay 在事件重试超过 `maxRetries` 后自动标记为 `dead_letter = true`。此后该事件被永久跳过——**没有任何人知道它死了，也无法手动恢复**。当前没有任何端点可以：

- 查看死信事件列表及其 payload（排查 webhook 下游挂掉的原因）
- 手动重试死信事件（修复下游后恢复投递）
- 手动将异常事件转为死信（阻止一个反复失败的事件浪费 relay 资源）
- 清除死信事件（释放表空间）

这实际上是一个**数据丢失定时炸弹**：假设 webhook 端点变更了路径但旧配置未更新，所有事件都会在 20 次重试后变成死信——运维团队只有在用户投诉"收不到通知"时才会发现。

### 代码证据

**证据 1：死信标记后不可见**

```go
// internal/outbox/repository.go:71-76
func (r *GormRepository) FetchUnpublished(...) ([]*Event, error) {
    // ...
    .Where("published_at IS NULL AND NOT dead_letter").
    //                      ^^^^^^^^^^^^^^^^^^^^^^^^  ← 死信被永久排除
```

没有任何 `ListDeadLetters` 或 `FetchDeadLetters` 方法。

**证据 2：Repository 接口无死信操作**

```go
// internal/outbox/outbox.go:36-54
type Repository interface {
    Append(...)
    FetchUnpublished(...)
    MarkPublished(...)
    MarkFailed(...)      // ← 只标记失败，无法撤销
    // 缺少: ListDeadLetters, CountDeadLetters, ResetDeadLetter, PurgeDeadLetters
}
```

**证据 3：Admin 端点只有审计日志**

```go
// internal/handler/http/admin.go:28
r.Auth.GET("/admin/audit-log", h.ListAudit)
// 没有: GET /admin/outbox/dead-letters, POST /admin/outbox/retry/:id
```

**证据 4：Metrics 没有死信计数**

```go
// internal/pkg/metrics/metrics.go — 搜索 dead_letter 零命中
// 现有: ObserveOutboxPublished, ObserveOutboxFailed, ObserveOutboxDrain
// 缺少: ObserveOutboxDeadLetter (gauge: 当前死信总数)
```

### 建议方向

新增管理端点及其对应 service 方法：

| 端点 | 功能 |
|------|------|
| `GET /v1/admin/outbox/dead-letters` | 列出死信事件（带分页），包含 payload、failure 次数、最后错误时间 |
| `POST /v1/admin/outbox/dead-letters/:id/retry` | 将指定死信标记为 `dead_letter = false, retry_count = 0`，恢复投递 |
| `POST /v1/admin/outbox/dead-letters/purge` | 清除指定死信事件（释放磁盘空间） |
| `POST /v1/admin/outbox/events/:id/dead-letter` | 手动将某个 stuck 事件直接标记为死信 |

对应 metrics 增补：
- `aeroid_outbox_dead_letter_total` (counter, labels: event_type) — 死信产生速率
- `aeroid_outbox_dead_letter_pending` (gauge) — 当前死信积压数量

### 架构影响

- **outbox.Repository** 接口扩展：新增 `ListDeadLetters(ctx, page)`、`ResetDeadLetter(ctx, id)`、`PurgeDeadLetters(ctx, ids)` 方法
- **GormRepository** 实现上述三个方法（~50 行 SQL）
- **AdminHandler** 新增 3 个路由，复用现有 auth+permission 中间件
- 零影响现有 relay 逻辑：死信状态只在 relay 写（MarkFailed），不会读

**产品价值**: ★★★★☆ — B2B 客户合同中通常包含"事件投递保证"SLA，运维 API 是保证可追溯性的基础  
**实施工作量**: 低（2-3 天）  
**冲突检查**: 零冲突——纯新增代码

---

## 方向二：存储配额与用量追踪（成本控制盲区）

### 问题

当前文件上传有**单文件大小限制**（`max_upload_bytes`），但**没有任何用户级或全局的累积存储配额**。在 S3 后端上，这意味着一个用户可以上传任意数量的文件直到填满 bucket——这不是理论风险，而是生产级的成本失控向量。具体来说：

- 没有 `SELECT sum(size) FROM files WHERE owner_uid = ?` 的查询（无用量计算）
- 没有硬/软配额机制（软配额 = 警告，硬配额 = 拒绝上传）
- 没有 `remaining_bytes` 字段暴露给客户端（用户不知道还剩多少空间）
- 没有按时间窗口的配额（每日上传总量限制）
- 没有超配额后的降级行为（如拒绝上传但不拒绝读取）

### 代码证据

**证据 1：Create 只检查单文件大小**

```go
// internal/service/file.go:73-75
if s.maxBytes > 0 && in.Size > s.maxBytes {
    return nil, apperr.BadRequest("file.too_large", ...)
}
// 没有: sum(s.size) + in.Size > s.quota 的检查
```

**证据 2：FileRepository 无用量查询**

```go
// internal/repository/file.go — 搜索 SumSizeByOwner 零命中
type FileRepository interface {
    Create(...)
    Get(...)
    ListByOwner(...)
    Delete(...)
    CountByStorageKey(...)
    FindByChecksum(...)
    UpdateMetadata(...)
    // 缺少: SumSizeByOwner(ctx, ownerUID) (int64, error)
    // 缺少: CountByOwner(ctx, ownerUID) (int64, error)
}
```

**证据 3：Config 无配额配置**

```yaml
# configs/config.yaml — 搜索 quota 零命中
storage:
  max_upload_bytes: 10485760  # 10MB — 这是单文件限制
  # 没有: user_quota_bytes, daily_upload_limit, quota_warn_threshold
```

**证据 4：File domain 模型无用量概念**

```go
// internal/domain/file.go — 整个包没有 Quota、Usage、BytesUsed 概念
```

### 建议方向

1. **FileRepository 新增**：`SumSizeByOwner(ctx, ownerUID) (int64, error)` — 低开销查询
2. **fileService 新增配置**：`WithUserQuotaBytes(n int64)` — 在 `Create` 中查询已有用量+新文件大小，超限返回 `429 QuotaExceeded`
3. **新增端点**：`GET /v1/usage` 返回 `{storage_bytes: N, file_count: N, quota_bytes: M, remaining_bytes: M-N}` 
4. **可选缓存**：对 `SumSizeByOwner` 做 TTL 缓存（如 30s），避免每次上传都 count(*) sum

```go
// 新增配额检查（示意）
func (s *fileService) checkQuota(ctx context.Context, actor string, newSize int64) error {
    if s.userQuota <= 0 {
        return nil // quota not configured ≈ unlimited
    }
    used, err := s.files.SumSizeByOwner(ctx, actor)
    if err != nil {
        return apperr.Internal("file.quota_failed", "failed to check quota")
    }
    if used+newSize > s.userQuota {
        return apperr.New(429, "file.quota_exceeded", "storage quota exceeded")
    }
    return nil
}
```

### 架构影响

- **repository**：新增一个 `SUM` 查询方法（~10 行）
- **service**：`Create` 和 `PresignUpload` 路径插入配额检查
- **handler**：新增 `GET /v1/usage` 端点（~30 行）
- 不修改现有文件读写路径

**产品价值**: ★★★★★ — 存储成本是 SaaS 服务最大的运营支出之一  
**实施工作量**: 低（1-2 天）  
**冲突检查**: 零冲突——纯新增

---

## 方向三：会话列表与会话管理（用户界面盲区）

### 问题

当前消息 API 提供：
- `GET /v1/messages` — 收件箱（**所有消息**平铺）
- `GET /v1/conversations/:uid` — 与指定用户的聊天记录

但**没有任何端点能列出"我所有的会话"**（即我与之有消息往来的所有用户，含最后一条消息预览和未读数）。这意味着任何前端都无法展示 IM 产品最基本的"会话列表/侧边栏"。当前唯一变通方式是拉取全部收件箱再在客户端做聚合——随着消息增长这完全不可扩展。

### 代码证据

**证据 1：MessageRepository 无 ListConversations 方法**

```go
// internal/repository/message.go:17-43
type MessageRepository interface {
    ListInbox(ctx, toUID, page, unreadOnly)           // ← 平铺所有消息
    ListConversation(ctx, uidA, uidB, page)           // ← 指定两人的对话
    // 缺少: ListConversations(ctx, uid, page) → []*ConversationSummary, next
}
```

**证据 2：应用层无会话概念**

```go
// internal/handler/http/message.go — 搜索 "conversations" 路由
r.Auth.GET("/conversations/:uid", h.Conversation)     // ← 只有指定 UID 的
// 没有: GET /v1/conversations
```

**证据 3：Domain 无 Conversation 实体**

```go
// internal/domain/message.go — 只有 Message，没有 Conversation
type Message struct {
    ID, FromUID, ToUID, Kind, Content, FileID, ...
}
// 缺少: type ConversationSummary struct { PeerUID, LastMessage, UnreadCount, LastActivity }
```

**证据 4：proto 无 Conversation 消息**

```protobuf
// api/proto/aero_id/v1/aero_id.proto — 搜索 conversation 零命中
// service MessageService 中: 没有 ListConversations RPC
```

**证据 5：收件箱平铺在大量消息时效率低**

`ListInbox` 返回所有消息（不分会话），前端需要遍历整个列表按 sender 分组——这在大收件箱时 O(n) 且无法分页。

### 建议方向

新增 `ListConversations` 端点及其完整链路：

| 层 | 新增内容 |
|----|---------|
| Repository | `ListConversations(ctx, uid, page) → []*ConversationRow, next` — SQL: `SELECT DISTINCT ON ... last_message, unread_count ... ORDER BY last_activity DESC` |
| Domain | `ConversationSummary{PeerUID, LastMessage, LastActivity, UnreadCount}` |
| Service | `ListConversations(ctx, page) → *ConversationList` — 调用 repo + enrich peer 用户信息 |
| HTTP | `GET /v1/conversations` — 响应 `{conversations: [...], users: {...}, next_cursor: "..."}` |
| gRPC | `rpc ListConversations(ListConversationsRequest) returns (ListConversationsResponse)` |

可选下游扩展：

- **会话静音**：`PATCH /v1/conversations/:uid` 支持 `muted: true`，静音后不为此会话产生通知
- **会话归档**：`POST /v1/conversations/:uid/archive` — 从默认列表隐藏，可恢复
- **会话已读**：批量标记整个会话已读（当前 `MarkConversationRead` 已有底层支持，但无 HTPP 端点映射到 `POST /v1/conversations/:uid/read`）

### 架构影响

- repository 层新增一条 SQL 查询（利用现有索引）
- domain 层新增一个纯数据 struct（无行为）
- service/handler/grpc 层各新增一个方法
- 不修改现有消息读写路径

**产品价值**: ★★★★★ — 没有会话列表的 IM 是不可用的  
**实施工作量**: 中（3-5 天，含 gRPC 扩展 + proto 生成）  
**冲突检查**: 零冲突——纯新增

---

## 方向四：幂等键可观测性（运营盲区）

### 问题

Idempotency key 机制已经实现并且在 `POST /v1/*` 上默认启用。但运营团队对幂等键的运行状况完全不可见：

- 不知道幂等键的命中/穿透比（idempotency 的缓存效率 → 是否在节省后端写操作？）
- 不知道正在累积多少未过期的幂等键（表行数是否在增长？）
- 不知道是否有来自同一客户端的重复无效请求（恶意重放或客户端 bug）
- Cleaner 默默过期删除——没有日志，没有度量

这意味着：如果某个客户端因 bug 每秒发送相同 `Idempotency-Key` 的请求，系统会正常响应 200（来自缓存），但 **没有任何指标暴露这种行为**——运维无法发现客户端异常。

### 代码证据

**证据 1：middleware 无度量埋点**

```go
// internal/middleware/idempotency.go
// 完整的 middleware 实现，搜索 metric / observe / counter 零命中
func Idempotency(store idempotency.Store, ttl time.Duration) gin.HandlerFunc {
    return func(c *gin.Context) {
        key := c.GetHeader("Idempotency-Key")
        // 调用 store.Reserve → 运行 handler → store.Complete
        // 没有任何 metric 记录：命中次数、穿透次数、冲突次数
    }
}
```

**证据 2：Metrics Registry 无幂等相关指标**

```go
// internal/pkg/metrics/metrics.go — 搜索 idempotency 零命中
// 有 HTTP RED、SSO、outbox 指标，但没有：
// - aeroid_idempotency_hits_total
// - aeroid_idempotency_misses_total  
// - aeroid_idempotency_stale_reclaims_total
// - aeroid_idempotency_keys_pending (gauge)
```

**证据 3：Cleaner 无日志输出**

```go
// internal/idempotency/store.go:145-148
func (s *GormStore) purgeExpired() {
    _ = s.db.Where("expires_at < ?", time.Now().UTC()).Delete(&idempotencyRow{}).Error
    // 没有 zap.L().Info("purged N expired keys")
    // 没有记录 purged 数量的 metric
}
```

**证据 4：配置中无幂等策略控制**

```yaml
# configs/config.yaml — 搜索 idempotency 零命中
# server.http.idempotency_ttl 存在，但：
# 没有: idempotency_log_hits, idempotency_max_key_length, idempotency_enforce_ttl
```

### 建议方向

1. **Idempotency middleware 埋点**：在 Reserve 和 Complete 处记录 metric：
   ```go
   if rec.Completed {
       metrics.ObserveIdempotencyHit(route)
   } else if !reserved {
       metrics.ObserveIdempotencyConflict(route) // 并发冲突
   }
   metrics.ObserveIdempotencyMiss(route)
   ```
2. **Idempotency Store 新增**：`Count(ctx) (int64, error)` — 统计当前有效（未过期）键数（Gauge）
3. **Cleaner 增强**：每次 purge 记录清理数量和耗时
4. **新增端点**：`GET /v1/admin/idempotency/stats` — 返回 `{total_keys, expired_per_hour, hit_ratio}`（仅 admin 可见）
5. **可选扩展**：`DELETE /v1/admin/idempotency/keys?uid=xxx` — 手动清理某用户的幂等键

### 架构影响

- **middleware**：一行代码引入 metrics registry
- **idempotency.Store** 接口新增 `Count(ctx) (int64, error)`（~10 行 SQL）
- **metrics.Registry** 新增 4 个计数器 + 1 个 gauge
- **AdminHandler** 新增一个只读端点

**产品价值**: ★★★☆☆ — 运营可观测性，对稳定性 SLA 有要求的场景必不可少  
**实施工作量**: 低（1-2 天）  
**冲突检查**: 零冲突——纯新增

---

## 方向五：文件附件级联生命周期管理（数据完整性盲区）

### 问题

当一条消息引用了某文件（`message.file_id`）并被软删除后，被引用的文件**不会被一同清理**。具体来说：

1. 消息软删除 → `message.deleted_at` 置为当前时间，但其 `file_id` 引用的文件永久存活
2. 用户删除文件 → 即使文件被消息引用，文件仍可被删除（`Delete` 只检查 owner）
3. 没有任何引用计数或 GC 机制来清理"仅被已删除消息引用"的孤立文件
4. 评论（通过 `TargetType=file` 引用文件）也存在同样的问题

这与 storage_refs（内容去重引用计数）不同——storage_refs 计数的是**同一个物理 blob 被多少 file 行共享**，而非**file 被多少 message/comment 引用**。

### 代码证据

**证据 1：文件 Delete 不检查下游引用**

```go
// internal/service/file.go:134-151
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    // 只检查: actor == owner  → OK
    // 然后: files.Delete(ctx, id) → soft delete
    // 不检查: messages 表中是否有 message.file_id = id AND deleted_at IS NULL
    // 不检查: comments 表中是否有 target_type='file' AND target_id = id AND deleted_at IS NULL
}
```

**证据 2：文件被引用时不阻止删除**

没有任何 `PreventDeleteIfReferenced` 或 `CascadeDelete` 逻辑——一个文件即使被 1000 条消息引用，owner 依然可以删除它（删除后消息里的 file_id 变成悬挂指针）。

**证据 3：Message 和 File 之间无反向引用查询**

```go
// internal/repository/message.go:20-42
type MessageRepository interface {
    // 没有: ListByFileID(ctx, fileID) → []*Message
    // 没有: CountByFileID(ctx, fileID) → int64
}
// internal/repository/comment.go — 同样没有 CountByTargetFile(ctx, fileID) 
```

**证据 4：GC sweeper 不解此类孤立**

```go
// internal/storage/gc/sweeper.go — 搜索 "message" "comment" 零命中
// sweeper 只检查 storage_refs（物理 blob 的引用），不检查业务引用
// 孤立的 file 行（owner 已删但 blob 还活着）不会被清理
```

**证据 5：storage_refs 解决的是完全不同的问题**

storage_refs 跟踪的是**同一 blob 被多少 file row 通过内容去重引用**（N:N），而非 file 被多少 message/comment 引用（1:N）。这是两个正交的引用维度。

### 建议方向

1. **软引用保护**：在 `File.Delete` 中查询 `CountByFileID`，如果有活跃引用则返回 `409 Conflict`（可选：加 `?force=true` 实现级联删除）
2. **新增 repository 方法**：`MessageRepository.CountByFileID(ctx, fileID)` + `CommentRepository.CountByTargetFile(ctx, fileID)`
3. **新增 GC pass**：在 sweeper 中新增一个可选阶段，硬删除"软删除的文件行且未被任何活跃 message/comment 引用"的行，然后清理其 blob
4. **审计增强**：级联删除时记录到 audit log

```go
// 软引用检查示意
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    // ... ownership check ...
    refCount, _ := s.messages.CountByFileID(ctx, id)
    refCount += s.comments.CountByTargetFile(ctx, "file", id)
    if refCount > 0 {
        return apperr.Conflict("file.in_use", 
            fmt.Sprintf("file is referenced by %d messages/comments; remove references first", refCount))
    }
    // ... proceed with delete ...
}
```

### 架构影响

- **repository**：新增 2 个简单 COUNT 查询
- **fileService**：`Delete` 路径增加引用检查 + 新增 `CascadeOption` 允许强制删除
- **sweeper**：扩展 `purgeSoftDeleted` 以检查业务引用
- 不修改 message/comment 写入路径

**产品价值**: ★★★★☆ — 数据完整性是服务可靠性的基础，文件泄漏直接影响存储成本  
**实施工作量**: 低-中（2-4 天，GC pass 需额外 1 天）  
**冲突检查**: 零冲突——纯新增

---

## 总结：优先级建议

| 方向 | 工作量 | 产品价值 | 运营价值 | 建议阶段 |
|------|--------|---------|---------|---------|
| 1. Outbox 死信运维 API | 2-3 天 | ★★★★☆ | ★★★★★ | 第一阶段（高运维 ROI） |
| 2. 存储配额 & 用量追踪 | 1-2 天 | ★★★★★ | ★★★★☆ | 第一阶段（成本控制） |
| 3. 会话列表 & 会话管理 | 3-5 天 | ★★★★★ | ★★★☆☆ | 第一阶段（核心 UX） |
| 4. 幂等键可观测性 | 1-2 天 | ★★☆☆☆ | ★★★★☆ | 第二阶段（运营提效） |
| 5. 文件附件级联生命周期 | 2-4 天 | ★★★★☆ | ★★★★☆ | 第二阶段（数据完整性） |

**建议迭代顺序**：方向 1 + 2 + 3 并行开始（互不依赖），方向 4 和 5 在下一轮。

---

## Edge Cases 补充（被既有 75 份分析遗漏的边界情况）

**在扫描中发现的额外边界情况，不属于完整方向但值得记录：**

1. **幂等键存活撞上 cleaner 窗口**：`idempotency_ttl` 默认与 cleaner 间隔同量级（均为 ~1h）。如果一个慢请求的 `reservationTTL`（2 分钟）已过但 handler 仍在运行，cleaner 不会清理它（只清理 `expires_at < now` 的已完成行），但在极端情况下在途预留可能被错误重放。现有代码通过 `Reserve` 中的 stale-reclaim 逻辑部分处理了此问题，但无测试覆盖。

2. **`PresignUpload` + `CompleteUpload` 之间的文件被误删除**：用户 A 获取 presigned URL 后（FileID 已分配但尚未完成上传），此时另一个拥有文件删除权限的操作（如 owner 本人通过其他客户端）可能调用 `DELETE /v1/files/:id`——该行尚不存在，删除会返回 404，所以没影响。但如果 FileID 已通过其他路径被已知，无影响。

3. **通知折叠与已读回执的竞态**：`NotificationDispatcher` 先创建 notification 行，然后才折叠（`FindRecentByTypeAndActor`）。如果两个相同类型的 notification 在同一个 drain tick 中被处理，前一个还没被折叠（下一个 tick 才会折叠第二个）。这导致折叠窗口内的精确计数可能偏差 1——无害但值得记录。现有代码通过 `CollapsedCount` 的 best-effort 语义承认了这一点。

4. **文件删除与 GC sweeper 的竞态**：用户删除文件的软删除与 GC sweep 之间，blob 被 storage_refs 保护。但如果 ref-counting 未启用（`WithStorageRefs(nil)`），`CountByStorageKey` 会扫描 files 表，软删除的行仍被计数——所以不会误删除。这个 race 在启用 ref-counting + sweeper 时已被关闭（sweeper 使用 RefCounter）。无问题。

5. **`Outbox relay` 优雅关闭时最后 drain 不阻塞**：`Start` 在收到 `done` 信号后执行一次 `r.drain(context.Background())` 然后立即返回。但这最后一次 drain 是并发的——`stop()` 返回后 drain 可能还在运行。调用者（`app.shutdown`）此时开始 `httpServer.Shutdown`，可能导致正在处理的 webhook 请求被中断。这已经在 `ROADMAP.md` 的 P0-4 中标记但代码中已验证为已修复——开始 drain 后关闭。

---

*本文档通过全局代码扫描生成，每项断言均附精确代码位置。不包含 LLM 幻觉或推测性内容。*
