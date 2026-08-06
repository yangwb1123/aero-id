# aero-id — Fifth-Wave Global Scan: Uncovered Architectural Extension Directions

**Date**: 2026-07-10
**Author**: Senior Architect / PM
**Methodology**: Full-codebase scan (~180 Go source files, 22 migrations, dual-transport parity, full outbox pipeline, SSE hub, SSO resilience stack, admin RBAC, content search, notification dispatcher, storage GC, rate limiting, metrics/tracing, idempotency, presigned uploads, user blocking, comment moderation). Cross-referenced against all ~70+ existing `docs/requirements/*.md` analyses, `ROADMAP.md`, and `docs/specs/aero-id-design.md` to ensure zero duplication.
**Constraint**: No code written. Every direction cites exact code-level evidence.

---

## Executive Summary

The codebase has been exceptionally well-analyzed. Over 70 prior documents in `docs/requirements/` cover feature gaps, production hardening, performance optimization, and architectural evolution. This document identifies **five directions that remain genuinely uncovered** in all prior analyses — each grounded in structural gaps visible at the code level, not subjective opinions.

These directions share a common character: they are **cross-cutting architectural concerns** rather than feature requests. They sit at the intersection of data lifecycle, operational resilience, and system evolution — the kinds of gaps that only become visible when you step back from individual features and examine how the system behaves over time.

| # | Direction | Priority | Category |
|---|-----------|----------|----------|
| 1 | **Outbox Event Schema Versioning & Lifecycle** | P1 | Data Integrity / Evolution |
| 2 | **Read-Side Data Isolation (CQRS-lite)** | P1 | Scalability / Performance |
| 3 | **Domain Event Type Safety** | P2 | Maintainability / Correctness |
| 4 | **Outbox Relay At-Least-Once → Exactly-Once Semantics** | P1 | Data Correctness |
| 5 | **Business Continuity & Data Portability** | P2 | Operational Resilience |

---

## Direction 1 — Outbox Event Schema Versioning & Lifecycle Management

### Why This Is Genuinely New

Every prior analysis treats the outbox system as a completed, mature component. It has retry, backoff, dead-lettering, chain handlers, webhook delivery, metrics — all well-implemented. **But none examine what happens when events in the queue have a different schema than the current code expects.**

The outbox relay processes events asynchronously. An event written today (with today's payload schema) may sit in the queue for minutes, hours, or — after a dead-letter-then-resolve cycle — **days**. If the code is deployed with a new payload schema during that window, the old events fail to deserialize silently, or worse, deserialize into a partially-populated struct with missing fields.

### Code-Level Evidence

**Evidence 1: Event payloads are raw JSON with zero version metadata**

```go
// internal/outbox/outbox.go:28-38
type Event struct {
    ID            uuid.UUID
    AggregateType string
    AggregateID   string
    EventType     string
    Payload       []byte         // ← raw JSON, no version tag
    CreatedAt     time.Time
    PublishedAt   *time.Time
    RetryCount    int
    LastRetryAt   *time.Time
    DeadLetter    bool
}
```

There is no `SchemaVersion`, `PayloadVersion`, or `ContentType` field. The database migration (`migrations/000008_create_outbox_events.up.sql`) reflects this — only a `JSONB` payload column with no version discriminator.

**Evidence 2: Dispatchers unmarshal into concrete Go structs — any unknown field or changed shape causes silent failure**

```go
// internal/notification/dispatcher.go:228-260
func (d *NotificationDispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":
        return d.notificationMessageSent(e)
    // ...
    }
}

func (d *NotificationDispatcher) notificationMessageSent(e *outbox.Event) *domain.Notification {
    var p messageSentPayload                    // ← fixed struct, no version
    if err := json.Unmarshal(e.Payload, &p); err != nil {
        return nil                              // ← silent drop
    }
    // ...
}
```

If `messageSentPayload` gains a field (e.g. `file_name` for attachment notifications), in-flight events written before the deploy will still unmarshal (missing the new field), producing partial notifications. If a field is renamed, all in-flight events silently produce `nil` notifications.

**Evidence 3: Webhook consumers face the same problem externally**

```go
// internal/outbox/webhook.go:38-45
type webhookPayload struct {
    ID            string          `json:"id"`
    EventType     string          `json:"event_type"`
    AggregateType string          `json:"aggregate_type"`
    AggregateID   string          `json:"aggregate_id"`
    Payload       json.RawMessage `json:"payload"`
    CreatedAt     time.Time       `json:"created_at"`
}
```

External webhook consumers have no way to determine the schema version of a delivered payload. An integrator who built a handler for today's schema will break when aero-id adds or renames fields.

**Evidence 4: No migration strategy exists for in-flight events**

When a migration's `.up.sql` adds a column or table, pending outbox events written with the old schema are never migrated. The only options are: (a) drain the queue before deploying, (b) accept silently dropped events, or (c) write backward-compatible deserialization code — none of which are documented or enforced.

### Impact Assessment

| Scenario | Current behavior | Desired behavior |
|----------|-----------------|------------------|
| Add field to `message.sent` payload | Old events unmarshal with zero value for new field | New code handles both old and new payload shapes |
| Rename field in `comment.replied` payload | All in-flight events silently fail to notify | Old events either rejected with a clear error or migrated |
| Webhook consumer relies on specific payload shape | Changes break integrations with zero warning | Webhook payload includes a `schema_version` field |
| Backfill old events with new data | No mechanism exists | Schema migration tool to re-publish events with updated payloads |

### Suggested Approach

| Sub-item | Description | Complexity |
|----------|-------------|------------|
| Add `schema_version` field to outbox events (int, default 1) | New migration; update `Event` struct and all JSON marshal/unmarshal points | Low |
| Add typed event constructors | Replace bare `outbox.NewEvent("message.sent", payload)` with `outbox.NewMessageSentEvent(...)` that bakes in the current schema version | Low |
| Backward-compatible deserialization | Update dispatchers to try multiple known schemas or use `json.RawMessage` with version-aware parsing | Medium |
| Webhook payload version stamp | Include `schema_version` in `webhookPayload` so external consumers can adapt | Low |
| Event schema registry | Document each event type's payload shape with version history in `docs/events/` | Low |

---

## Direction 2 — Read-Side Data Isolation (CQRS-lite)

### Why This Is Genuinely New

All existing analyses focus on write-path correctness (transactions, idempotency, outbox, dedup). **None examine the read path's scalability characteristics.** The current architecture uses a uniform CRUD model for both reads and writes:

- Notifications feed: `SELECT ... FROM notifications WHERE recipient_uid = ? ORDER BY created_at DESC` — scans the same table that receives unbounded inserts via the outbox relay
- Inbox listing: `SELECT ... FROM messages WHERE to_uid = ? ORDER BY created_at DESC` — competes with concurrent message sends
- Search: Three separate `tsvector` queries on three tables executed sequentially under the same connection
- File listing: Paginated scan of `files` table with the same connection pool as uploads

The service is designed around horizontal layers (handler → service → repository), but **at the data access level, reads and writes are mixed** in the same schema, same connection pool, and often same query pattern. This works for thousands of users but will exhibit contention patterns at tens of thousands.

### Code-Level Evidence

**Evidence 1: Notification feed queries share a table with unbounded outbox-driven inserts**

```sql
-- migrations/000009_create_notifications.up.sql
CREATE TABLE notifications (
    id            UUID PRIMARY KEY,
    recipient_uid TEXT NOT NULL,
    -- ...
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

The notification feed (`internal/notification/repository.go`'s `ListByRecipient`) queries:

```sql
SELECT * FROM notifications WHERE recipient_uid = ? AND NOT read_at IS NULL?
ORDER BY created_at DESC, id DESC LIMIT ?;
```

This query competes with concurrent inserts from the outbox relay (`NotificationDispatcher.Create`). Under heavy load (e.g., a system notification to all users), the notification table sees a write burst from the relay while dozens of users simultaneously query their feed — row-level contention on the same index leaf pages.

**Evidence 2: Full-text search queries each table sequentially, not in parallel**

```go
// internal/search/search.go:60-70
func (s *searchService) Search(ctx context.Context, query string, kinds []domain.SearchKind, limit int) ([]SearchHit, error) {
    // ...
    for _, kind := range kinds {
        var err error
        hits, err = s.searchKind(ctx, actor, query, limit, kind, hits)
        // ← sequential: messages, then comments, then files
    }
}
```

Each `searchKind` call runs a separate DB query:

```sql
-- messages: tsquery on the full messages table (not just user's messages)
SELECT * FROM messages WHERE search_vector @@ plainto_tsquery('simple', ?) AND deleted_at IS NULL ORDER BY created_at DESC LIMIT ?;

-- comments: same pattern
SELECT * FROM comments WHERE search_vector @@ plainto_tsquery('simple', ?) AND deleted_at IS NULL ORDER BY created_at DESC LIMIT ?;
```

These three queries run sequentially, each blocking on the DB connection. No concurrent fan-out, no timeout-per-query, no fallback if one query is slow due to a large table.

**Evidence 3: No connection pool read/write separation**

```go
// internal/app/app.go:74-82
gdb, err := db.Open(db.Options{
    DSN:             cfg.Database.DSN(),
    MaxOpenConns:    cfg.Database.MaxOpenConns,    // ← single pool for all traffic
    MaxIdleConns:    cfg.Database.MaxIdleConns,
    ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
    LogLevel:        cfg.Database.LogLevel,
})
```

A single `sql.DB` (via GORM) serves both OLTP writes (message send, file upload, comment create) and read-heavy listing queries. When the connection pool is saturated by slow read queries (e.g., a FTS scan on a growing messages table), write transactions queue behind them.

**Evidence 4: No read-side caching beyond SSO user cache**

The only cache in the system is the SSO token/user cache. There is no:
- Materialized notification feed (notifications are always computed from base rows)
- File metadata cache (every `GET /v1/files/:id` hits the DB)
- Message inbox cache (every inbox query scans the messages table)
- Comment count cache (every like/unlike triggers a read-committed count)

### Impact Assessment

| Workload | Current bottleneck | Projected behavior at 100K+ users |
|----------|-------------------|-----------------------------------|
| Notification feed | Full table scan per query, competing with inserts | Feed latency degrades linearly with notification volume |
| Full-text search | Three sequential FTS queries on growing tables | Search response time degrades with corpus size |
| Inbox listing | Index scan competes with concurrent sends | Users see increased P99 latency during peak messaging |
| File listing | Content with inserts on same table | Owner-scoped query performs well but mixed write load causes jitter |

### Suggested Approach

The simplest high-leverage step is not a full CQRS separation but targeted **read-side optimizations**:

| Phase | Sub-item | Complexity |
|-------|----------|------------|
| 1 | **Parallel search**: Fan-out the three `searchKind` calls via `errgroup` with a timeout per query so one slow FTS scan doesn't block the others | Low |
| 2 | **Separate read pool**: Add `database.reader_dsn` config, use a read-replica connection for `GET`/`LIST`/`SEARCH` endpoints; writes use the primary pool | Medium |
| 3 | **Notification feed caching**: Cache the last N unread notifications per user in memory (short TTL, invalidated by outbox relay after write), reducing repeated queries | Medium |
| 4 | **File metadata cache**: Lightweight in-memory cache (lru + TTL) for `Get` calls; invalidate on Update/Delete | Low |
| 5 | **Materialized search index**: For production scale, consider a dedicated search index (Meilisearch/Elasticsearch) fed by the outbox webhook, removing FTS queries from the primary DB | High |

---

## Direction 3 — Domain Event Type Safety

### Why This Is Genuinely New

Event types in the outbox system are **bare string literals** scattered across packages. A typo in the producer produces a silent no-op in the consumer because `notificationFor()` returns `nil` for unknown event types. There is:

- No shared enum/constant for event type values
- No compiler-verified producer-consumer contract
- No automated test ensuring every event type produced by service X is handled by dispatcher Y
- No registry that maps event types to their expected payload schema

This is a **maintainability landmine** as the system grows. Every new feature that adds an event type must manually coordinate between the producer (service layer) and consumer(s) (notification dispatcher, webhook, future handlers).

### Code-Level Evidence

**Evidence 1: Event types are bare strings in the outbox namespace**

```go
// internal/message/service.go — producer side
evt := outbox.NewEvent("message", m.ID.String(), "message.sent", payload)
```

```go
// internal/notification/dispatcher.go — consumer side
func (d *NotificationDispatcher) notificationFor(e *outbox.Event) *domain.Notification {
    switch e.EventType {
    case "message.sent":      // ← bare string, must manually match producer
    case "message.read":      // ← bare string
    case "comment.replied":   // ← bare string
    default:
        return nil            // ← unknown type → silent no-op
    }
}
```

There is no shared package where `EventTypeMessageSent = "message.sent"` is defined. If a producer writes `"messge.sent"` (typo), the event is recorded in the outbox, picked up by the relay, passed to the dispatcher, and silently dropped. No error, no log, no metric — the event becomes a zombie that is repeatedly retried until it reaches max retries and dead-letters.

**Evidence 2: Aggregate type values are also bare strings**

```go
// internal/message/service.go
evt := outbox.NewEvent("message", m.ID.String(), "message.sent", payload)

// internal/comment/service.go
evt := outbox.NewEvent("comment", c.ID.String(), "comment.replied", payload)
```

The `aggregateType` parameter is `"message"` vs `"comment"` — bare strings. If a handler ever needs to dispatch based on aggregate type, it must string-match.

**Evidence 3: No cross-package test validates producer-consumer contracts**

`internal/service/mocks/mocks.go` has excellent mock definitions for repo testing. But there is no test that:
- Enumerates all event types produced by the service layer
- Verifies each has a corresponding handler in the dispatcher
- Verifies each handler's expected payload struct matches what the producer actually sends

**Evidence 4: New dispatcher handlers must manually update the switch statement**

Adding a new event type (e.g., `file.shared` from Direction 1) requires touching:
1. The service call site (to produce the event)
2. The dispatcher's `notificationFor()` switch
3. The payload struct definition
4. The notification type constant

At no point does any compiler or test catch the mistake if step 2 or 3 is skipped.

### Impact Assessment

| Scenario | Current behavior | Risk |
|----------|-----------------|------|
| Typo in event type string("message.sent" → "messge.sent") | Silent event drop, retried until dead-letter | High — data loss invisible to operators |
| New event type added but dispatcher not updated | Events accumulate in outbox, retried forever | High — queue backs up, relay spends CPU on doomed retries |
| Payload struct changes without updating producer/consumer in lockstep | Partial unmarshal (extra fields ignored) or silent nil | Medium — hard-to-diagnose notification gaps |
| Webhook consumer expects event type X | No way to enumerate supported event types | Medium — integration fragility |

### Suggested Approach

| Sub-item | Description | Complexity |
|----------|-------------|------------|
| Event type constants | Define `const EventTypeMessageSent = "message.sent"` etc. in `internal/domain/events.go` or a dedicated `internal/eventtype` package. Service layer imports constants; dispatcher switches on constants | Low |
| Aggregate type constants | Same pattern: `const AggregateMessage = "message"`, `const AggregateComment = "comment"` | Low |
| Producer-consumer contract test | A Go test that enumerates all registered event types (from a central registry) and verifies each has a handler. Use `//go:generate` or a manual list that fails CI when out of sync | Medium |
| Dispatcher error visibility | When `notificationFor` returns `nil` for a non-empty event type, log a WARN with the event ID and type so operators can detect zombie events without waiting for dead-letter | Low |
| Event type registration pattern | Replace the switch statement with a registry map: `handlers["message.sent"] = handleMessageSent`. Register handlers at init time; missing handlers cause a startup panic (fail fast) | Medium |

---

## Direction 4 — Outbox Relay At-Least-Once → Exactly-Once Semantics

### Why This Is Genuinely New

The outbox relay operates with **at-least-once** delivery semantics: it fetches unpublished events, dispatches them to handlers, then marks them published. If the process crashes between a successful `handler.Handle()` call and the subsequent `MarkPublished()` database write, the event is redelivered on the next tick.

This is a documented design choice — acceptable for notification delivery (duplicate notifications are better than missed ones) — but it creates real problems for **non-idempotent handlers**. Currently, all notification creation is not idempotent: `dispatcher.Create(ctx, n)` has no unique constraint on `(recipient_uid, type, subject_id)`, so duplicate events produce duplicate notification rows.

### Code-Level Evidence

**Evidence 1: The relay processes events, then marks published — crash window exists**

```go
// internal/outbox/relay.go:95-135
func (r *Relay) drain(ctx context.Context) int {
    events, err := r.repo.FetchUnpublished(ctx, r.batch)
    // ...
    for _, e := range events {
        if err := r.handler.Handle(ctx, e); err != nil {
            r.repo.MarkFailed(...)
            continue
        }
        published = append(published, e.ID)
    }
    // ← CRASH WINDOW: Handle() succeeded for N events but
    //    MarkPublished() hasn't been called yet
    if err := r.repo.MarkPublished(ctx, published); err != nil {
        // ← OR this fails (network blip), leaving all N events unpublished
        return 0
    }
}
```

**Evidence 2: Notification creation has no idempotency guard**

```go
// internal/notification/dispatcher.go:161-170
func (d *Dispatcher) Handle(ctx context.Context, e *outbox.Event) error {
    n := d.notificationFor(e)
    if n == nil {
        return nil
    }
    if err := d.notifs.Create(ctx, n); err != nil {  // ← no unique constraint on (recipient, type, subject)
        return err
    }
    // ...
}
```

The repository's `Create` method (`internal/notification/repository.go`) inserts a new row with a fresh UUID:

```go
func (r *notificationRepo) Create(ctx context.Context, n *domain.Notification) error {
    return r.db.WithContext(ctx).Create(notificationRowFromDomain(n)).Error
}
```

No `ON CONFLICT DO NOTHING`, no unique constraint on natural key, no idempotency check. A duplicate event creates a duplicate notification row — the user sees the same notification twice.

**Evidence 3: SSE replay after crash would double-deliver**

The SSE handler's `replayMissed` (`internal/handler/http/events.go:93`) replays notifications created since `last_event_id`. If a crash+redelivery produced duplicate notification rows, both rows are replayed to the SSE client.

### Impact Assessment

| Scenario | Current behavior | Desired behavior |
|----------|-----------------|------------------|
| Relay crashes after Handle() before MarkPublished() | Duplicate notification created on restart | Duplicate detected and suppressed via `(recipient, type, subject_id)` uniqueness |
| MarkPublished() fails due to network blip | Same batch processed again on next tick | Exactly-once processing via idempotent handlers |
| Process restarted during high-throughput event burst | Potentially dozens of duplicate notifications | Zero duplicates; relay auto-recovers |

This is classified P1 because it's a **data correctness** issue that directly affects user experience — duplicate notifications erode trust in the system.

### Suggested Approach

| Sub-item | Description | Complexity |
|----------|-------------|------------|
| Unique constraint on notifications | Migration to add `UNIQUE (recipient_uid, type, subject_id, COALESCE(actor_uid, ''))` or similar natural key so duplicate inserts are silently no-oped | Low |
| `ON CONFLICT DO NOTHING` in Create | Change notification repo's `Create` to use `clause.OnConflict{DoNothing: true}` so duplicate events don't fail | Low |
| Outbox event dedup stamp | Add a `processed_hash` column to outbox events — set by handler on successful process, checked before processing — so even if `MarkPublished` fails, the handler won't re-process | Medium |
| Idempotent handler pattern | Document the expectation that all outbox handlers must be idempotent (or safe for at-least-once delivery). Apply the same pattern to future handlers (webhook already is; it POSTs to an external endpoint) | Low |

---

## Direction 5 — Business Continuity & Data Portability

### Why This Is Genuinely New

aero-id manages **user-owned data** — files, messages, comments — that represent real business value. Yet the codebase has:

- No data export mechanism (a user cannot download all their data as a zip/archive)
- No bulk import/restore tooling
- No automated backup verification
- No cross-region replication strategy
- No data retention policies enforced at the application layer (other than the soft-delete GC sweep)
- No export/import for the outbox event history

All prior analyses focus on runtime correctness, performance, and features. **None address what happens when the data needs to leave the system** — whether for user request (GDPR/CCPA data portability), disaster recovery, or migration to another infrastructure.

### Code-Level Evidence

**Evidence 1: No bulk data export endpoint exists**

There is no `GET /v1/export` or similar endpoint that produces a user's complete data. Each data type is individually accessible:

- `GET /v1/files` — lists files (metadata only, not content)
- `GET /v1/messages` — lists messages (paginated, limited)
- `GET /v1/comments` — lists comments per target

But there is no unified export that:
1. Collects all files + their content blobs
2. All sent and received messages
3. All comments written
4. All notification history
5. All sharing grants and blocks
6. All preferences

**Evidence 2: No database dump/restore integration**

The deployment setup (`deployments/docker-compose.yml`, `deployments/Dockerfile`, `cmd/migrate/main.go`) covers:
- Application build and run ✅
- Migration up/down ✅
- Health checking ✅

But there is no:
- `make backup` or `make restore` target in `Makefile`
- No scripts for `pg_dump` / `pg_restore` integration
- No documented recovery procedure
- No automated restore verification in CI

**Evidence 3: Outbox events are irrecoverable after MarkPublished**

```go
// internal/outbox/repository.go:90-97
func (r *GormRepository) MarkPublished(ctx context.Context, ids []uuid.UUID) error {
    return r.db.WithContext(ctx).Model(&eventRow{}).
        Where("id IN ?", ids).
        UpdateColumn("published_at", time.Now().UTC()).Error
}
```

Once marked published, events are effectively "done" — but there's no retention policy for how long they stay in the table, and no mechanism to replay them for a new subscriber that joined after the event was published.

**Evidence 4: Blob-level DR for file storage**

The storage backends (`internal/storage/local.go`, `internal/storage/s3.go`) store blobs at deterministic paths (`<owner_uid>/<yyyy>/<mm>/<uuid>`), but:

- Local storage has no built-in replication or backup — if the local disk dies, files are gone
- S3 storage relies on the S3 provider's durability, but the application has no cross-bucket or cross-region replication trigger
- The `storage_refs` table (migration 000021) provides atomic reference counting but has no backup integration
- No blob integrity verification exists — there is no periodic checksum re-computation vs. stored checksum check

### Impact Assessment

| Scenario | Current capability | Risk |
|----------|-------------------|------|
| User requests download of all personal data | Manual: developer must write SQL queries + S3 copy scripts | High — legal liability under GDPR/CCPA |
| Production database corrupted | Rely on infrastructure-level backups (if configured); no application-level consistency check | High — silent corruption possible |
| Need to migrate from local storage to S3 | No built-in migration tool; must write custom script | Medium — migration delay is acceptable but increases human error risk |
| Need to replay all events for a new analytics pipeline | No event streaming log; only current outbox queue | Medium — analytics pipeline must be built from scratch |
| Blob integrity verification | No periodic checksum re-check | Low-Medium — silent data corruption in storage backend goes undetected |

### Suggested Approach

| Phase | Sub-item | Description | Complexity |
|-------|----------|-------------|------------|
| 1 | **Data export endpoint**: `POST /v1/export` creates an async job that zips all user data (file metadata, messages, comments, notifications, preferences) and stores the archive as a file in the user's own storage. `GET /v1/export/:job_id/status` and `GET /v1/export/:job_id/download` for retrieval | Medium |
| 2 | **Backup/restore Make targets** | Add `make backup` (pg_dump + blob snapshot) and `make restore` (pg_restore + blob sync) with documentation | Low |
| 3 | **Blob integrity scan** | Extend the GC sweeper (`internal/storage/gc/sweeper.go`) with an optional integrity pass: re-compute SHA-256 of stored blobs and compare against `files.checksum`. Log mismatches at ERROR level | Medium |
| 4 | **Outbox event retention and replay** | Add a configurable retention period for published events (default 7 days) before they are purged. Add a `GET /v1/admin/events` endpoint for replaying historical events to new subscribers | Medium |
| 5 | **Storage migration tool** | `cmd/storage-migrate/main.go` that reads from one storage backend and writes to another, using the `files` table as the source of truth. Works with both local→S3 and S3→local directions | High |

The export endpoint (Phase 1) should be the immediate priority — it's a legal requirement under many data privacy regulations and has the most direct user impact. Phase 3 (blob integrity) is the highest operational risk reducer: currently there is no way to detect silent data corruption in stored files.

---

## Cross-Cutting Analysis

### Common Themes Across All Five Directions

| Theme | Directions affected |
|-------|-------------------|
| **Data longevity** — how the system behaves over weeks/months/years, not just requests per second | 1 (event schema evolution), 4 (duplicate events), 5 (backup & integrity) |
| **Testability of cross-cutting contracts** — type-safe event types, producer-consumer tests | 1, 3 |
| **Operational visibility into the async path** — how operators detect and diagnose issues in the outbox, notification pipeline, and storage | 1 (zombie events), 3 (unknown event types), 4 (duplicate notifications) |
| **Scale-readiness of read paths** — current architecture treats all reads as equal to writes | 2 |

### Suggested Sequencing

| Phase | Direction | Rationale |
|-------|-----------|-----------|
| **Sprint 1** | 3 (Event type safety) + 1 (schema versioning stubs) | Low effort, no migration, immediate maintainability improvement. Event type constants can be extracted from existing code in one session |
| **Sprint 2** | 4 (Exactly-once outbox) | Schema change for unique constraint, small code changes. Fixes a data correctness bug that silently produces duplicate notifications |
| **Sprint 3** | 2 (Read-side isolation) | Parallel search is low-hanging fruit; separate read pool requires infra but is high-leverage |
| **Sprint 4+** | 5 (Business continuity) | Data export endpoint is Phase 1 priority; backup/restore and blob integrity follow |

---

## Appendix: Cross-Reference with Existing Analyses

To ensure novelty, each direction was checked against all identified prior analyses:

| Direction | Prior analysis coverage | Verdict |
|-----------|----------------------|---------|
| Outbox event schema evolution | Zero hits across 70+ docs | ✅ Genuinely uncovered |
| Read-side isolation / CQRS-lite | Zero hits (all analyses focus on write-path) | ✅ Genuinely uncovered |
| Domain event type safety | Zero hits — event types treated as "already complete" | ✅ Genuinely uncovered |
| Outbox at-least-once semantics | Zero hits — relay design not analyzed for idempotency gaps | ✅ Genuinely uncovered |
| Business continuity / data portability | Zero hits — all analyses focus on runtime, not data lifecycle | ✅ Genuinely uncovered |
