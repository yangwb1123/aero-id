# Data Retention Policy

> Last updated: 2026-10-07
> Applies to: aero-id v0.1.x

This document defines the TTL-based cleanup strategy for every table that
grows unboundedly. Cleaners run as background goroutines inside the aero-id
process and are configured via `config.yaml` under `data_retention.*`.

---

## Current control-plane exports and lifecycle

The table index below primarily describes legacy compatibility cleaners; it is
not the complete control-plane retention policy. Canonical account links,
operations, command intents, event receipts and the immutable audit ledger have independent
retention/hold rules. Temporary export deletion does not authorize ledger
purging or source-system erasure.

Account and audit exports keep encrypted content in access-controlled,
TTL-bound storage. New job results and checkpoint continuations contain only
bounded metadata. Historical job reads omit embedded content, but this does
not clean previously persisted copies. With deterministic storage, the original
key, digest, size and TTL are checkpointed before Put; lost acknowledgements
remain unknown and recovery does not regenerate content or extend its TTL.

Publication and final download admission use the same local account lock as
erase (account → job). An erase that commits first rejects admission; one-time
consumption and successful-read audit are atomic. Bytes admitted/sent before
erase cannot be recalled and may arrive afterwards. Cross-region disposition,
stale replicas, old writers, late remote writes after disposal, historical
content retention and Legal Hold/source deletion acceptance remain separate
work—not claims closed by these local transactions.

Expiration filtering happens before the bounded cleanup limit. Unknown jobs,
consumed pointers awaiting confirmed deletion and lease-expired running
artifacts remain eligible through their internal checkpoint. Object deletion
runs outside database transactions; a checkpoint is removed only after deletion
is confirmed (including NotFound). Physical deletion timing still depends on
storage availability and deployment lifecycle/retention policies.

Implementation: `internal/service/sync_artifact_{prepare,resume,validation}.go`,
`internal/service/sync_export_download.go` and
`internal/repository/sync_job_export_{fence,download,cleanup}.go`.

Command intents contain bounded, normalized/redacted plan and receipt metadata,
not tokens or source content. Their five-minute `expires_at` limits new confirmed
receipt admission, not historical retrieval, retention or Legal Hold. No automatic
intent purge is enabled; migration `000072` refuses rollback while any intent
exists. Expiry must not discard an uncertain execution receipt or authorize a
second command under a new key. A hold-aware intent retention/disposition policy
and expired-preview operator UX remain separate work.

## Table Index

| Table | Retention | Clean Interval | Clean Condition | Business Justification |
|-------|-----------|---------------|-----------------|----------------------|
| `idempotency_keys` | 24h | 1h | `expires_at < NOW()` | Idempotent responses are safe to replay within TTL; beyond that the client must retry |
| `audit_events` | 90 days | 1h | `created_at < NOW() - 90d` | Audit logs are for incident response & compliance; 90d satisfies most audit requirements |
| `notifications` | 30d (read), 90d (unread) | 1h | `read_at < NOW() - 30d` OR `created_at < NOW() - 90d` | Read notifications older than 30d are stale; unread older than 90d come from inactive users |
| `outbox_events` | 7d (dead letter), 30d (published) | 1h | `dead_letter AND created_at < NOW() - 7d` OR `published_at IS NOT NULL AND created_at < NOW() - 30d` | Dead letters are diagnostic; published events are historical |
| `file_shares` | Permanent | N/A | N/A | Share links have indefinite business meaning |
| `user_blocks` | Permanent | N/A | N/A | Block relationships are indefinite |
| `storage_refs` | 90d (orphaned) | 1h | `file_id IS NULL AND created_at < NOW() - 90d` | Orphaned refs from failed uploads are safe to clean |
| `unread_counts` | Permanent | N/A | N/A | One row per active user; bounded by user count |

---

## Cleaner Configuration

All cleaners are **OPT-IN**: they only start when the corresponding
`clean_interval` and `retention` are both set to positive values in config.

### 1. Audit Events Cleaner

**File:** `internal/repository/audit.go` — `RunAuditCleaner`

```yaml
data_retention:
  audit_retention: 2160h        # 90 days; 0 = never purge
  audit_clean_interval: 1h      # 0 = disabled
```

**SQL (batched):**
```sql
DELETE FROM audit_events
WHERE id IN (
  SELECT id FROM audit_events
  WHERE created_at < NOW() - INTERVAL '90 days'
  ORDER BY created_at ASC, id ASC
  LIMIT 1000
);
```

**Batch size:** 1000 rows, 100ms pause between batches.

### 2. Notifications Cleaner

**File:** `internal/repository/notification.go` — `RunNotificationCleaner`

```yaml
data_retention:
  notification_retention: 720h          # 30 days for read notifications
  notification_unread_retention: 2160h  # 90 days for unread notifications
  notification_clean_interval: 1h
```

**SQL (batched):**
```sql
-- Read notifications past retention
DELETE FROM notifications
WHERE id IN (
  SELECT id FROM notifications
  WHERE read_at IS NOT NULL AND read_at < NOW() - INTERVAL '30 days'
  ORDER BY created_at ASC, id ASC
  LIMIT 500
);

-- Unread notifications past retention
DELETE FROM notifications
WHERE id IN (
  SELECT id FROM notifications
  WHERE read_at IS NULL AND created_at < NOW() - INTERVAL '90 days'
  ORDER BY created_at ASC, id ASC
  LIMIT 500
);
```

**Batch size:** 500 rows per sub-query, 100ms pause between batches.

### 3. Outbox Events Cleaner

**File:** `internal/outbox/repository.go` — `RunOutboxCleaner`

```yaml
data_retention:
  outbox_dead_letter_retention: 168h   # 7 days
  outbox_published_retention: 720h     # 30 days
  outbox_clean_interval: 1h
```

**SQL (batched):**
```sql
-- Dead letters past retention
DELETE FROM outbox_events
WHERE id IN (
  SELECT id FROM outbox_events
  WHERE dead_letter = true AND created_at < NOW() - INTERVAL '7 days'
  ORDER BY created_at ASC, id ASC
  LIMIT 500
);

-- Published events past retention
DELETE FROM outbox_events
WHERE id IN (
  SELECT id FROM outbox_events
  WHERE published_at IS NOT NULL AND created_at < NOW() - INTERVAL '30 days'
  ORDER BY created_at ASC, id ASC
  LIMIT 500
);
```

**Important:** Events that are still being retried (`published_at IS NULL AND
dead_letter = false`) are **never** touched by the cleaner.

**Maintenance pause (`data_retention.purge_paused`):** the cleaner can be
suspended for maintenance flows such as dead-letter replay
(`cmd/audit-replay`, which recovers dead-lettered `audit_publications` and
`callback_deliveries` rows). With `purge_paused: true` every tick performs **no
purge work** — no SELECT, no DELETE; every `outbox_events` row (dead-letter or
published) is preserved bit-for-bit. The flag is static per boot (config is
loaded once; "purge resumes" means the next restart with the flag cleared) and
is validated so it is accepted only while the cleaner is actually wired
(`outbox_clean_interval > 0` and `outbox_dead_letter_retention > 0`). Negative
retentions are rejected at load: a negative bound would put the cutoff in the
future and purge every eligible row on the next tick.

```yaml
data_retention:
  outbox_dead_letter_retention: 168h   # 7 days
  outbox_published_retention: 720h     # 30 days
  outbox_clean_interval: 1h
  purge_paused: false                  # maintenance: suspend retention purge
```

### 4. Idempotency Keys Cleaner

**File:** `internal/idempotency/store.go` — `RunCleaner`

```yaml
server:
  http:
    idempotency_ttl: 24h  # TTL is built-in; cleaner runs at 1h interval
```

The `idempotency_keys` table has a pre-existing TTL-based cleanup mechanism
(`RunCleaner(time.Hour)`) that deletes rows where `expires_at < NOW()`.

---

## Implementation Pattern

Every cleaner follows the same pattern established by `RunAuditCleaner`:

```go
func RunAuditCleaner(repo AuditRepository, log *zap.Logger,
    retention time.Duration, interval time.Duration) func() {
    if interval <= 0 {
        return func() {}
    }
    t := time.NewTicker(interval)
    done := make(chan struct{})
    go func() {
        ctx := context.Background()
        for {
            select {
            case <-t.C:
                cutoff := time.Now().UTC().Add(-retention)
                deleted, err := repo.PurgeOlderThan(ctx, cutoff, 0)
                if err != nil {
                    log.Warn("audit purge failed", zap.Error(err))
                } else if deleted > 0 {
                    log.Info("audit purge complete",
                        zap.Int64("deleted", deleted),
                        zap.Time("cutoff", cutoff))
                }
            case <-done:
                t.Stop()
                return
            }
        }
    }()
    return func() { close(done) }
}
```

Key invariants:
1. **OPT-IN:** Cleaners only start when `clean_interval > 0` **and** `retention > 0`
2. **Batched deletes:** Every purge uses `DELETE ... WHERE id IN (SELECT id ... LIMIT N)` with a pause between batches to avoid saturating the DB
3. **Graceful shutdown:** The stop function closes a channel to terminate the goroutine; `App.shutdown()` calls it before `http.Server.Shutdown`
4. **Observability:** Each successful purge logs the number of deleted rows at `info` level; errors are logged at `warn`

---

## Monitoring

| Metric | Type | Description | Labels |
|--------|------|-------------|--------|
| `aeroid_gc_rows_purged_total` | Counter | Rows purged by each cleaner | `table` (audit_events, notifications, outbox_events) |

**Recommended dashboard panels:**
- Per-table purge rate (rows/hour)
- Oldest row age per table (gauge)
- Cleaner error count

---

## Tables Excluded from Cleanup

The following tables never grow unboundedly and are excluded from TTL cleanup:

| Table | Reason |
|-------|--------|
| `files` | Soft-deleted; actual blob cleanup is handled by the Storage GC sweep (`storage.gc.*` config) |
| `file_shares` | Business semantic is permanent; bounded by number of active share links |
| `messages` | Business semantic is permanent; user-generated content should not be auto-deleted |
| `comments` | Same as messages |
| `user_blocks` | Block relationships are indefinite |
| `unread_counts` | One row per active user; bounded by user count |
| `users` | Legacy compatibility projection; human identity is Snaplink-owned. Canonical `accounts` links/tombstones are separate authoritative control-plane records, not covered by this legacy cleaner table. |
| `storage_refs` | Only orphaned refs are cleaned (see table above); active refs are permanent |
