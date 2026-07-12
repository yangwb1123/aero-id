Good — zero live tests import the dead packages. Safe to delete in one batch.

---

### ⑤ Soft Delete Cascade — Important Nuance

The analysis claims "no cascade strategy defined" and "no ON DELETE CASCADE". The reality is **more nuanced and arguably worse**:

| Table | FK | ON DELETE Clause | Effect |
|-------|----|-----------------|--------|
| `comment_likes` | `comment_id → comments(id)` | **None** (defaults to NO ACTION) | **Blocks deletion** of a commented-on comment |
| `comment_reports` | `comment_id → comments(id)` | **None** | **Blocks deletion** |
| `comments.parent_id` | `parent_id → comments(id)` | **None** | **Blocks deletion** of a parent comment |
| `messages.file_id` | `file_id → files(id)` | **None** | **Blocks deletion** of a referenced file |
| `message_reactions` | `message_id → messages(id)` | **ON DELETE CASCADE** | ✅ Reactions auto-delete with message |
| `file_shares` | *(no FK at all)* | N/A | Shares silently orphaned |

This means the current state is **inconsistent**:
- Try to delete a file that has messages referencing it → **SQL error** (FK violation)
- Try to delete a comment with likes → **SQL error**
- Try to delete a message → reactions cascade (good) but comments targeting it via `target_id` have **no FK** and silently orphan
- File shares have **no FK** and silently orphan

The analysis is right about the core problem (orphans/permission confusion), but the **real immediate danger is that deletions can fail** with unexpected 500 errors due to FK constraints — worse than just orphaned data.

---

## Calibrated Execution Plans

### P0: Notification Event Coverage

**Code changes needed** (verified, concrete):

1. **In `internal/service/comment.go`, `Like` method** (around line 260): Write outbox event after successful like:

```go
// Inside s.react() or the Like() method, after successful DB insert:
evt := outbox.NewEvent("comment", c.ID.String(), "comment.liked", map[string]any{
    "comment_id":    c.ID.String(),
    "comment_author": c.OwnerUID,
    "liker_uid":     actor,
})
// Need to pass outboxRepo to commentService for this
```

2. **In `internal/service/file_share.go`, `ShareFile`** (around line 65): Write event for each grantee (or one bulk event):

```go
evt := outbox.NewEvent("file_share", id.String(), "file.shared", map[string]any{
    "file_id":  fileID,
    "owner_uid": actor,
    "grantee_uid": uid,  // per-recipient or batch
})
```

3. **In `internal/service/message.go`, `ReactToMessage`** (around line 180) — currently doesn't exist in the live codebase (the dead `internal/message/service.go` has it). Need to add to the live `internal/service/message.go`.

4. **Add cases to `notificationFor`** in `internal/service/notification.go`:

```go
case "comment.liked":
    // ...
case "file.shared":
    // ...
case "message.reacted":
    // ...
case "report.resolved":
    // ...
```

### P0: Audit Log Reliability

**The fastest path** that respects the "audit can't block main operation" constraint:

1. **Add Prometheus counters** (in `internal/repository/audit.go`):

```go
func (r *auditRepo) Log(ctx context.Context, e *domain.AuditEvent) error {
    err := r.Append(ctx, e)
    if err != nil {
        metrics.AuditLogFailures.WithLabelValues(e.Action).Inc()
    }
    metrics.AuditLogTotal.WithLabelValues(e.Action, fmt.Sprint(err == nil)).Inc()
    return err  // still don't propagate to caller
}
```

2. **Add composite index** for the List query pattern — the existing `idx_audit_events_actor` only covers `(actor_uid, created_at DESC)` but the List query also filters by `action`:

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_audit_events_action
    ON audit_events(action, created_at DESC);
```

3. **Outbox-based audit** is a larger change: you'd need to add `AuditEvent` as an outbox event type and have a consumer that writes to `audit_events`. This gives you transactional guarantees but adds complexity. My recommendation: start with metrics + index (easy, high impact), then plan outbox for the next sprint.

### P1: Dead Code Cleanup

**One-shot deletion script**:

```bash
rm -rf internal/comment/ internal/file/ internal/message/ internal/block/ internal/audit/ internal/search/ internal/notification/
```

Then update `.skills/refactor-large-file.md` if it references these. Run `make test && make lint` — if anything breaks, it's a signal that something DID depend on them.

**Add CI guard** (in a `Makefile` target or GitHub Actions):

```bash
# Fail if any .go file outside of app.go imports a non-imported package
go list -f '{{.ImportPath}} {{.Imports}}' ./internal/... | \
  grep -v 'internal/app\|internal/service\|internal/handler' | \
  grep 'github.com/aero/aero-id/internal' && exit 1
```

### P1: SSO User Cache Invalidation

The `UserCache` already has `SetLookupObserver` (line 43 in `user_cache.go`). Add:

```go
func (c *UserCache) Invalidate(ctx context.Context, uids ...string) {
    c.mu.Lock()
    for _, uid := range uids {
        delete(c.items, uid)
    }
    c.mu.Unlock()
}

func (c *UserCache) InvalidateAll(ctx context.Context) {
    c.mu.Lock()
    c.items = make(map[string]userEntry)
    c.mu.Unlock()
}
```

Then expose via `sso.Client` and wire it to an admin endpoint or webhook. But the **real question** is: how does aero-id learn about SSO-side changes? Options:
- **Polling**: every N minutes, batch-check recently-modified UIDs
- **Webhook**: SSO calls aero-id's `/admin/sso-invalidate` endpoint
- **gRPC streaming**: SSO pushes invalidation events

This requires SSO integration — it's not purely an aero-id change.

### P2: Soft Delete Cascade

The **staging approach** I'd recommend:

1. **Immediate**: Document which FK constraints block deletions today (the `comment_likes → comments`, `messages.file_id → files`, `comments.parent_id → comments` ones will cause SQL errors). Ensure all Delete methods handle `gorm.ErrForeignKeyViolation` gracefully.

2. **Short-term**: Add a `status` or `hidden_at` column approach instead of FK cascade — when a parent is deleted, set child status to `hidden`. This is what the analysis suggests and is the most robust approach for a soft-delete system.

3. **Long-term**: Remove `ON DELETE CASCADE` from `message_reactions` (it's inconsistent) and replace with the same soft-cascade pattern.

---

## One Missed Finding

Your analysis missed a **moderate-severity issue** in the audit log query path. The `List` method in `internal/repository/audit.go` does:

```go
q := r.db.WithContext(ctx).Model(&auditEventRow{}).Order("created_at DESC, id DESC")
```

But the existing index `idx_audit_events_actor` is on `(actor_uid, created_at DESC)` — note **no `id` column**. The `ORDER BY id DESC` will require an additional sort pass when multiple rows share the same `created_at` timestamp (which is `NOW()` resolution, so very possible under load). A covered index `(actor_uid, created_at DESC, id DESC)` would eliminate that sort. This is a minor perf issue but worth noting.

---

## Recommended Next Actions

| # | Action | Effort | Risk | Owner |
|---|--------|--------|------|-------|
| 1 | Delete the 7 dead packages in one commit | 15 min | Low (verified no live deps) | Any engineer |
| 2 | Add Prometheus counters to audit Log | 30 min | Low | DevOps-capable backend |
| 3 | Add comment.liked + message.reacted events | 2-3 hours | Low (follows existing pattern) | Backend |
| 4 | Add audit action composite index migration | 15 min | Low | Any engineer |
| 5 | Add UserCache.Invalidate | 1 hour | Low | Backend |
| 6 | Document FK constraint conflicts on delete | 1 hour | None | Design doc |

Items 1-3 can be done in parallel. Item 4 (SSO invalidation) requires cross-team coordination.

The analysis is exceptionally well-researched. The only correction I'd suggest is on the soft delete cascade: the problem is not just "orphaned data," it's **SQL failures on delete** — which is actually more urgent than the document implies. I'd bump it from P2 to at least P1 with the immediate mitigation of proper error handling for FK violations.
