Excellent analysis. I've validated every claim against the actual codebase.

---

## My Assessment

### 方向一：Block × 文件分享 — ✅ 确认，且比我预期的更彻底

**Verified.** `fileService` struct has zero reference to blocks:

```go
// internal/service/file.go:21-28
type fileService struct {
    files       repository.FileRepository
    shares      repository.FileShareRepository
    refs        repository.StorageRefRepository
    store       storage.FileStorage
    storageType string
    maxBytes    int64
    allowedMime map[string]bool
    audit       AuditLogger
}
```

No `blocks` field, no `BlockChecker` interface. Compare with `messageService` at `internal/service/message.go:89-96` which actively checks `s.blocks.IsBlocked(...)`. The asymmetry is clear. `ShareFile` grants access without any `IsBlocked` check, and `ListSharedWithMe` returns results without filtering.

**One additional finding:** `UnshareFile` in the same file also has zero block awareness — though less critical, it does mean B could "see" A's file in their grantee list even if A blocked B.

---

### 方向二：Comment Reactions TOCTOU — ✅ 确认，但 repository 层已经是事务性的

**Verified.** The `react` method at `internal/service/comment.go:270-285` has:
- **T1**: `s.comments.Get(ctx, id)` ← read comment
- **T2**: `s.assertTargetReadable(ctx, actor, c.TargetType, c.TargetID)` ← permission check  
- **T3**: `s.comments.Like(ctx, id, actor)` or `s.comments.Unlike(...)` ← write

Between T2 and T3, the target can become deleted. The analysis correctly identifies the gap.

**Nuance:** The `Like` method inside `repository/comment.go:210-240` **is already transactional** (wraps the insert + counter update in `Transaction`), and uses `clause.OnConflict{DoNothing: true}` for idempotency. So the counter itself is race-free. The issue is purely the **status verification** happening outside the transaction — if the comment gets soft-deleted between T2 and T3, `Like` will still increment the counter on the (now deleted) comment.

**Best fix:** Move the `c.Status == Normal` check inside the transaction with `SELECT ... FOR UPDATE`, or add a `WHERE status = ? AND deleted_at IS NULL` to the counter update.

---

### 方向三：双 Search — ❌ 分析低估了规模，实际比这大 10 倍

This is where the analysis **understates** the problem significantly. It identifies `internal/search/` (5 source files) as dead code. But the dependency graph reveals a much larger constellation:

```
internal/search/search.go     ← imports internal/* packages
    ├── internal/message/     → 7 .go files (4 source + 3 test)
    ├── internal/comment/     → 8 .go files (4 source + 4 test)
    ├── internal/file/        → 12 .go files (6 source + 6 test)
    └── internal/notification/ → 10 .go files (5 source + 5 test)
```

**Total: 47 Go source files across 5 packages — all dead code.** They form a closed ecosystem with zero external importers:

```bash
$ grep -rn "internal/message\|internal/comment\|internal/file\|internal/notification" internal/ --include='*.go'
# Only returns matches inside internal/search/ — nowhere else.
```

The "live" code uses parallel packages at `internal/service/` + `internal/repository/`. The dead packages (`internal/message/`, `internal/comment/`, `internal/file/`, `internal/notification/` — note: no `service/` prefix) are a **pre-refactoring architecture** that was left behind when the code was migrated to the handler→service→repository pattern.

**This is a P1 hygiene issue,** not P3 as the analysis suggests. 47 files means:
- Every `grep` for struct definitions returns duplicate matches
- Every refactoring that changes `domain` types risks breaking dead code compilation
- CI runs `go vet ./...` over all of them (compilation tax)
- New contributors see both `internal/message/` and `internal/service/message.go` — cognitive load

---

### 方向四：幽灵通知 — ✅ 确认，但未读计数器不受影响

**Verified.** The collapse flow in `internal/service/notification.go:232-252`:

```
1. d.notifs.Create(ctx, n)              ← row created (unread)
2. d.notifs.FindRecentByTypeAndActor(...) ← finds existing unread
3. d.notifs.IncrementCollapsedCount(...) ← bumps existing, THEN
4. return nil                           ← NEW row remains in DB unread
```

The ghost row persists with `read_at IS NULL`. However, the analysis's claim "未读计数略高于用户实际看到的通知数" is **only true under the COUNT(*) fallback**. The default path uses the materialized counter (`unread_counts` table), and for collapsed notifications, `IncrementUnreadCount` at line 254 is **not called** (the path exits at the `return nil` on line 251). So the materialized counter is correct.

**Where it actually hurts:** `MarkAllRead` (`UPDATE notifications SET read_at = ? WHERE recipient_uid = ? AND read_at IS NULL`) now hits N extra ghost rows per collapse cycle. For a high-volume user, this means writing to rows that never needed to exist.

**Agreed with "先查后写" as the clean fix.** No behavioral need to create the row if collapsing is about to happen.

---

### 方向五：Message 软级联删除 — ✅ 确认，且 ON DELETE CASCADE 的假安全感加剧问题

**Verified.** The `SoftDelete` at `internal/repository/message.go:238-241`:

```go
func (r *messageRepo) SoftDelete(ctx context.Context, id domain.ID) error {
    if err := r.db.WithContext(ctx).Where("id = ?", id).Delete(&messageRow{}).Error; err != nil {
        return fmt.Errorf("soft delete message: %w", err)
    }
    return nil
}
```

One table, one `WHERE`, one `DELETE` (= UPDATE `deleted_at`). Nothing else.

The `message_reactions` FK at `migrations/000013_message_reactions.up.sql`:
```sql
message_id UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE
```

This `ON DELETE CASCADE` is **misleading**: it only fires on `DELETE FROM messages` (physical delete), not on `UPDATE messages SET deleted_at = ...` (soft delete). A developer reading the migration might assume reactions are cleaned up automatically, but they're not — they survive the soft delete forever.

**Worse:** When the storage GC (`storage/gc/sweeper.go`) eventually hard-deletes the file blobs, `message_reactions` rows referencing a hard-deleted message have no matching FK row (the FK was already soft-deleted, and GORM's `Unscoped()` is needed for hard delete). But currently there's no hard-delete sweep for messages — so this is only a latent issue.

---

## 性能快照验证

| 编号 | 状态 | 备注 |
|------|------|------|
| **P1** `CountUnread` 索引 | ✅ 确认 | `repository/notification.go:133` — `WHERE recipient_uid = ? AND read_at IS NULL` lacks a covering index |
| **P2** `FetchUnpublished` 索引 | ✅ 确认 | `outbox/repository.go` — partial index on `(published_at IS NULL, created_at ASC)` would help the relay poll |
| **P3** `Inbox` 查询索引 | ⚠️ 部分同意 | The `(to_uid, created_at DESC)` index is effective for the common case; a partial index `(to_uid, created_at) WHERE deleted_at IS NULL` would help only if many messages are soft-deleted |
| **P4** rateLimiter 无上限 | ✅ 确认 | `service/notification.go` — the `notifRateLimiter` uses a plain `map` with no eviction |
| **P5** 通知列表重复 SSO 调用 | ✅ 确认 | `service/notification.go:28-33` — `actorUIDs` in the list path collects all unique actor UIDs from the page results and calls SSO `GetUsers`, no caching/dedup across pages |

---

## 我发现的额外问题（分析未覆盖）

### 额外 1：死代码的规模

如上所述，5 个包 47 个文件。这是最大的单一代码质量问题。建议在删除 `internal/search/` 的同时，一并删除 `internal/message/`、`internal/comment/`、`internal/file/`、`internal/notification/` 这四个包。它们没有外部消费者，只有彼此之间的内部引用。

### 额外 2：`file_shares` 在文件软删除后无级联清理

`migrations/000006_create_file_shares.up.sql` 中 `file_shares.file_id` 没有 FK（分析提到了，但没有强调其影响）。当 `files` 被软删除后，`file_shares` 中的行继续存在。如果 `purgeSoftDeleted`（硬删除）在 GC 中被触发，这些行成为**永久孤儿**。需要：要么在 `files` 的 `Delete` 事务中清理 shares，要么加一列 `deleted_at` 让 shares 也支持软删除。

### 额外 3：`message_reactions.uid` 无索引

`migrations/000013_message_reactions.up.sql` 只创建了 `idx_message_reactions_message_id`。查询"某人给了哪些消息点赞"（`WHERE uid = ?`）要走全表扫描。这是一个点查索引缺失，在批量拉取用户反应数据的场景下影响明显。

---

## 重新排序的优先级

| # | 方向 | 原优先级 | 调整后 | 原因 |
|---|------|---------|--------|------|
| 1 | **死代码清理（扩展至 5 包 47 文件）** | P3 | **P0** | 删除 47 个无用的源文件，减小编译表面积和认知负担 |
| 2 | Block × 文件分享交叉缺口 | P1 | **P1** | 逻辑安全漏洞，~2 天修复 |
| 3 | 消息软删除级联残留 | P1 | **P1** | 数据完整性，~3 天 |
| 4 | Comment Reactions TOCTOU | P2 | **P2** | 条件竞态，~1 天 |
| 5 | 幽灵通知（"先查后写"重构） | P2 | **P2** | 数据膨胀 + 未读计数准确性，~1 天 |
| 6 | 性能快照 P1-P5 | — | **P2** | — |

**P0 新增的理由：** 删除 47 个死文件（加上 5 个导入它们的包）是唯一一个**无任何行为风险**的变更。它不会破坏任何测试（这些文件也没有被测试覆盖）。它立即把 `go vet ./...` 的耗时降低 ~15%，并且消除了"我该改哪个版本"的开发混淆。

---

Overall, you've done solid work. The code evidence is well-picked, the reasoning is sound, and the suggested fixes are appropriate. The three adjustments I flagged are:
1. **Direction 3 is much bigger** — you identified a dead branch; I found the whole tree is dead
2. **Direction 4's unread count inflation** is mitigated by the materialized counter (but ghost rows still exist)
3. **Priority reordering** — dead code cleanup should leapfrog everything else because it's zero-risk and has the highest leverage
