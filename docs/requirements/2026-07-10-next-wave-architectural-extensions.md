# aero-id — Next-Wave Architectural Extensions (2026-07-10)

> **Context** — Full codebase scan of `main` (source tree under `/home/u1/aero-id`).
> All five ROADMAP directions and the post-ROADMAP production hardening wave are
> delivered. This document identifies **genuinely uncovered** high-value directions
> that the current architecture does **not** yet anticipate, backed by specific code
> evidence. No code was changed to produce this.

---

## Current state summary (evidence base)

The service is production-ready across every dimension the original ROADMAP called for:

| Dimension | What exists |
|---|---|
| **Observability** | RED metrics per route/method, OTel tracing (incl. SSO hop), per-dependency `/readyz`, SSO health gauges |
| **SSO resilience** | Circuit breaker, bounded retries+backoff, serve-stale-on-failure, singleflight, TLS/mTLS, keepalive, cert-pinning |
| **File pipeline** | Streaming uploads, size cap, MIME allow-list, content-checksum dedup, presigned direct-to-S3, GC sweep (purge + orphan), storage ref counting |
| **Write-path** | Atomic transactions, idempotency keys, transactional outbox (retry + backoff + dead-letter) |
| **Product surface** | Comment reactions, reactions on messages, moderation/report queue, explicit file sharing, typed/system messages, outbox-driven notifications with SSE push, block-aware suppression, preference-aware dispatch, notification collapse + rate limiting |
| **Admin/ops** | Role-based permissions (moderator/admin), audit log query, migration CLI, auto-migrate, gRPC health + reflection, graceful shutdown, cold-start warmup |
| **Search** | Full-text search via `tsvector` + `GIN` on files, messages, comments |
| **Transport** | 38 gRPC RPCs across 5 services, full HTTP↔gRPC parity, SSE events with last_event_id replay |

**The five directions below are not feature wishlist items.** They are architectural gaps
that exist in the **current code** today — each represents a concrete limitation, a
production risk, or an implicit design constraint that the next wave of growth will
collide with.

---

## Direction 1 — Cross-Instance Pub/Sub for SSE Notifications

### Why it's needed

The SSE hub (`internal/hub/hub.go`) is an **in-memory, per-instance** subscriber
registry. In a single-instance deployment this is correct and efficient. But as soon as
the service scales horizontally (≥2 replicas), SSE subscribers are **affinitized to one
instance** by the load balancer's best-effort stickiness. When the outbox relay on
instance A dispatches a notification for uid `x`, only instance A's SSE subscribers
receive it — if `x`'s browser is connected to instance B, the notification is silently
dropped.

```go
// internal/hub/hub.go
// Locking is per-uid (sharded) so publishing to alice never blocks bob.
type Hub struct {
    subs   sync.Map // uid → *uidSubs   ← purely local; no cross-instance fan-out
    nextID atomic.Int64
    closed atomic.Bool
}
```

The `Publish` method (via `adapter.go`) calls `h.pub(uid, ...)` which only walks the
local `sync.Map`. There is **no channel, no Redis pub/sub, no NATS, no shared bus**.

**What fails in production:** Rolling deploy with zero-downtime (each instance restarted
in turn) loses all SSE connections during the restart window. The `last_event_id` replay
mechanism recovers missed notifications on reconnect, but after a full deploy cycle every
connected client experiences a gap + a burst of replay traffic — and during the intermediate
window where some instances are up and some are draining, events are lost silently.

**Proposed:**
- Introduce a **cross-instance event bus** abstraction (`PublishBus`) that the Hub and/or
  the notification dispatcher fan out to.
- Local implementation = current no-op passthrough (same-instance only).
- Redis pub/sub implementation = publishes to a Redis channel keyed by uid shard; every
  instance subscribes to the channels for uids whose SSE connections it holds.
- Keep the local `subs sync.Map` as the receiver side; add a relay goroutine per instance
  that reads from the bus and calls `pub()` locally.

**Impact / effort:** Medium. The Hub interface and dispatcher integration are clean; adding
a pluggable bus behind a small interface touches ~3 files. The Redis dependency would be new
(not in go.mod today). Off-by-default so existing single-instance deployments are unaffected.

**Edge case:** Subscriber migration on reconnect — if a client reconnects to a different
instance after a deploy, `last_event_id` replay on the new instance covers the gap but the
old instance's subscriber slot must timeout. The current unsubscribe-on-disconnect pattern
(HTTP handler's `defer unsub()`) handles this correctly as long as the old request is
cancelled (Gin's `c.Request.Context().Done()`).

---

## Direction 2 — Token Revocation Webhook (SSO → aero-id)

### Why it's needed

`internal/sso/token_cache.go` implements a TTL-based cache:

```go
// internal/sso/token_cache.go: Get returns a cached token if it exists AND is
// within its TTL window. Misses fall through to an SSO ValidateToken gRPC call.
// The stale-grace window (serve-stale) is bounded by StaleTTL config.
```

There is **no mechanism for SSO to push a revocation signal** to aero-id. If a user's
session is invalidated at SSO (password change, admin logout, account suspension), aero-id
will continue to honor that user's token for up to `TokenCacheTTL + StaleTTL` (default
60s + 300s = **6 minutes** of unauthorized access). In a security-conscious deployment
this window is unacceptable.

```go
// internal/sso/client.go: ValidateToken is cache-first
func (c *Client) ValidateToken(ctx context.Context, token string) (string, error) {
    // 1. token_cache.Get → hit → return uid
    // 2. singleflight + gRPC call → cache.Set → return uid
    // 3. on gRPC error → token_cache.GetStale (serve-stale window) → return uid
    // NO: push-based invalidation, NO: callback endpoint
}
```

The service has no HTTP endpoint or gRPC RPC that SSO can call to say "forget token X"
or "invalidate all tokens for uid Y". Adding one is trivial but architecturally important:
it changes the security model from "eventually consistent" to "near-instant revocation".

**Proposed:**
- Add an unauthenticated (or SSO-SSO-mTLS-authenticated) HTTP endpoint
  `POST /v1/admin/invalidate-token` (or a gRPC RPC on the ops service) that evicts a
  specific token from the cache.
- Add `POST /v1/admin/invalidate-user` that evicts *all* tokens for a uid.
- SSO calls these endpoints when it revokes a session or suspends an account.
- The token cache gains an `Evict(tokenHash string)` and `EvictUser(uid string)` method.
- These operations are **audit-logged** (the audit.Logger already exists at
  `internal/repository/audit.go` and is wired in the app composition root).

**Impact / effort:** Low. Two new cache methods, one new handler. The webhook target URL
and shared HMAC secret go in config (reusing `outbox.webhook` patterns). The endpoint
should be rate-limited and authenticated via mTLS or a shared bearer token.

**Edge case:** Race between concurrent eviction and validation — a token being validated
at the exact moment SSO revokes it. This is inherent in distributed systems; the design
should document that the revocation is "best effort within RTT + cache lookup" and that
SSO is the authoritative source of truth. Aero-id's cache is a performance optimization,
not an authorization decision.

---

## Direction 3 — Multi-Party Conversations / Group Messaging

### Why it's needed

The message model (`internal/domain/message.go`) is **strictly 1-to-1**:

```go
type Message struct {
    ID        ID
    FromUID   string
    ToUID     string
    Kind      MessageKind
    Content   string
    FileID    *ID
    ReadAt    *time.Time
    EditedAt  *time.Time
    Meta      map[string]any
    CreatedAt time.Time
}
```

Every query path is built around this binary contract — `Inbox` filters by `to_uid`,
`Conversation` queries `(from_uid, to_uid)`, `MarkRead` sets `read_at` on a single
recipient row. There is **no `conversation_id`**, no membership table, no notion of
N>2 participants.

```sql
-- migrations/000002_create_messages.up.sql
CREATE TABLE messages (
    id         UUID PRIMARY KEY,
    from_uid   TEXT NOT NULL,      ← exactly one sender
    to_uid     TEXT NOT NULL,      ← exactly one recipient
    ...
);
```

This is a **data model constraint**, not a missing feature — changing it requires a
migration, a new `conversations` table, and a fundamental rework of the message
repository queries. But the product value is immense: group chat is the #1 request on
any messaging platform.

**Proposed:**
- New `conversations` table: `id UUID PK, name TEXT, created_by TEXT, created_at, archived_at`.
- New `conversation_members` table: `conversation_id UUID, uid TEXT, joined_at, role TEXT`.
- Add a `conversation_id` nullable FK column to `messages` (1-to-1 messages get NULL;
  group messages reference the conversation).
- New endpoints: `POST /v1/conversations` (create), `POST /v1/conversations/:id/members`
  (invite), `DELETE /v1/conversations/:id/members/:uid` (kick/leave), `GET /v1/conversations`
  (list mine), `GET /v1/conversations/:id` (messages in a group).
- Reuse notification/outbox: sending a group message creates one outbox event; the
  dispatcher fans it out to N notification rows (one per non-sender member). SSE pusher
  already handles multi-recipient per the Direction 1 fix.
- Block-aware: if A blocked B, B's messages in shared groups are suppressed for A.

**Impact / effort:** High. This is a significant data model + repository + service +
handler addition across both transports. It's the most expensive direction on this list.
Sequence it after Direction 1 (cross-instance pub/sub) because group conversations
amplify the multi-instance SSE problem.

**Edge case:** "Someone left the group" — the outbox currently has no `conversation`
event types. Add `conversation.member_added`, `conversation.member_removed`,
`conversation.member_left` to the outbox event taxonomy and handle them in the
NotificationDispatcher. The `SystemSenderUID` constant already exists for system messages.

---

## Direction 4 — Async Job Queue (beyond the outbox)

### Why it's needed

The outbox relay (`internal/outbox/relay.go`) is excellent for its purpose: fire-and-forget
domain events with retry and dead-letter. But it is **not a general-purpose job queue**:

```go
// internal/outbox/relay.go
// drain handles one batch of unpublished events.
func (r *Relay) drain(ctx context.Context) int {
    events, err := r.repo.FetchUnpublished(ctx, r.batch)
    // ... handle each event, mark published or failed ...
}
```

Limitations relative to a job queue:

| Property | Outbox today | Would need for jobs |
|---|---|---|
| Schedule | Poll-based (fixed interval) | Cron-like "run at" / "run after" |
| Concurrency | Single-threaded drain | Configurable worker pool |
| Visibility | Binary published/unpublished | "claimed" + ACK timeout for re-delivery |
| Priority | Oldest-first | Priority queue |
| Event types | Only domain events via handler | Plugins per job type |

Code evidence — there is no scheduler, no delayed execution, no cron expression anywhere
in the codebase. The `go.mod` has no job-queue dependency. Every asynchronous operation
is either inline (SSO warmup in `app.warmup()`), in a ticker goroutine (relay, GC, idempotent
cleanup), or pushed through the outbox.

**What this blocks:**
- **Scheduled message sending** ("send this message tomorrow at 9am").
- **Delayed moderation actions** ("auto-hide after 3 unreviewed reports").
- **Periodic data exports** ("email me my data every month").
- **Content expiry** ("delete messages older than N days" — currently only file GC exists).
- **Webhook retry with exponential backoff across restarts** — the outbox dead-letter
  window is bounded by `MaxRetries` and retries are in-memory tick intervals; a restart
  resets the timer. A proper job queue persists the retry schedule.

**Proposed:**
- Introduce a lightweight job abstraction (e.g., `river` or `machinery` or a thin PostgreSQL
  advisory-lock based scheduler). Do **not** add Redis/Celery — stay Postgres-native.
- New `scheduled_jobs` table: `id UUID PK, type TEXT, payload JSONB, run_at TIMESTAMPTZ,
  priority INT, status TEXT, attempts INT, max_attempts INT, last_error TEXT,
  scheduled_at TIMESTAMPTZ, started_at TIMESTAMPTZ, completed_at TIMESTAMPTZ`.
- Worker pool goroutine that claims available jobs via `SELECT ... FOR UPDATE SKIP LOCKED`
  with a visibility timeout, executes the handler, and records completion or schedules retry.
- Start with one built-in job handler: `message.send_scheduled` — a system message posted
  at a future time.
- Keep the outbox for transactional domain events; use the job queue for deferred/external
  work. The two coexist: an outbox handler can **enqueue a job**.

**Impact / effort:** Medium-high. The key insight is to stay in PostgreSQL (reuse the
existing DB connection pool) and avoid adding a new infrastructure dependency. The
outbox's `FetchUnpublished` pattern already proves the polling approach works; a job
queue just extends it with a visibility timeout + worker pool + cron scheduling.

**Edge case:** A job's `run_at` has passed but the server was down — the scheduler must
pick up missed jobs on startup. The `scheduled_jobs` table includes a `status` column so
startup can query `WHERE status = 'pending' AND run_at <= now()` and claim them.

---

## Direction 5 — Per-User Storage Quotas & Usage Tracking

### Why it's needed

The file pipeline has per-request limits (`max_upload_bytes` in config, enforced in
`internal/service/file.go:Creater`'s `cappedReader`), but there is **no per-user
aggregate quota**:

```go
// internal/service/file.go
type fileService struct {
    files       repository.FileRepository
    store       storage.FileStorage
    maxBytes    int64  // ← per-file limit only
    allowedMime map[string]bool
    // No: userStorageQuota, userCurrentUsage
}
```

The `files` table records `size` per row, so computing an owner's total usage is a simple
`SELECT SUM(size) FROM files WHERE owner_uid = ? AND deleted_at IS NULL`. But this query
doesn't exist — neither the repository nor the service layer exposes a usage endpoint.

In production this matters for:
1. **Abuse prevention** — a single user uploading 10TB of files fills the storage backend.
2. **Billing readiness** — if the product ever tiers storage by plan, quotas are required.
3. **Capacity planning** — operators cannot answer "who are the top N storage consumers?"
4. **Hard enforcement** — a soft quota warns; a hard quota rejects uploads.

**What exists today** that would make this cheap to implement:
- The repository already has `CountByStorageKey` and `ListByOwner` methods.
- The Redis-less design means we stay entirely in PostgreSQL.
- The admin role system (`internal/domain/admin.go`) already defines `PermSystemConfig`;
  a quota-override endpoint would naturally land behind a `system.config` permission check.
- The `UpdateMetadata` endpoint already handles `PATCH /v1/files/:id` — adding a
  `validate usage` check there shares the same quota logic.

**Proposed:**
- Add `Usage(ctx context.Context, ownerUID string) (bytes int64, count int64, err error)`
  to `repository.FileRepository` (a two-column `SELECT` aggregate).
- Add `Quota(ctx context.Context) (maxBytes int64)` to a new `QuotaStore` (config-backed
  default, with optional per-user overrides table: `user_quotas(uid, max_bytes)`).
- In `fileService.Create`, before writing: `SELECT SUM(size)...` + check against the quota.
  Reject with `file.quota_exceeded` if exceeded.
- In `fileService.UpdateMetadata` (which only changes metadata, not size), no quota check
  needed — only content-mutating paths check.
- Expose `GET /v1/files/usage` returning `{bytes, count, quota_bytes}`.
- Admin endpoint `POST /v1/admin/quotas` to set per-user overrides.

**Impact / effort:** Low. This is ~1 new repository method, ~2 new handler endpoints,
and a config key `storage.quota_bytes` (0 = unlimited, backward compatible). The quota
check is a single `SELECT SUM` before the upload write transaction begins.

**Edge case:** Concurrent uploads — a user at 99.9% of quota could have two parallel
uploads both pass the check and collectively exceed the limit. Mitigation: use a
`SELECT ... FOR UPDATE` on a `user_quotas` row (or advisory lock) to serialize quota
checks for the same uid, or accept the slight overage and enforce hard at the blob level
by checking before the row insert. Given the outbox-based GC sweep can reclaim overage
in the next pass, a ±1-file tolerance is acceptable.

---

## Summary: Sequencing & Dependencies

| # | Direction | Effort | Risk | Depends on | Why now |
|---|---|---|---|---|---|
| 1 | Cross-instance pub/sub | Medium | Operational | None | Multi-replica deploy is implicit in any production rollout; this is the first limit you hit. |
| 2 | Token revocation webhook | Low | Security | None | 6-minute stale-token window is a compliance risk for production. |
| 3 | Group conversations | High | Product/Model | Direction 1 (amplifies SSE) | Most user-visible gap in the messaging model; data model change means "now or never". |
| 4 | Async job queue | Medium-High | Infrastructure | None | Unlocks scheduled messaging, auto-moderate, content expiry — product accelerators. |
| 5 | Storage quotas | Low | Abuse | None | Storage is the only unbounded resource with no per-user cap. Cheap to add, painful to retrofit. |

**Suggested order:** 2 → 5 → 1 → 4 → 3. Low-friction hardening first (token revocation +
quotas), then operational scale (pub/sub), then product expansion (job queue + groups).

---

## Appendix: Edge cases verified to be handled by existing code

These were checked during the scan and are already correct:

| Edge case | Where handled |
|---|---|
| Concurrent comment likes (unique constraint) | `comment_likes` PK `(comment_id, uid)` prevents duplicates |
| Concurrent content-dedup races | `storage_refs` atomic ref counting + sweeper uses `GetCount` |
| Outbox event loss on restart | Last drain before shutdown in `Relay.Start` stop func |
| SSE disconnect + reconnect | `last_event_id` query param + `ListSince` fetcher replay |
| Block race (concurrent block + message send) | Block check is inside the message send transaction |
| Notification collapse (duplicate suppression) | `WithDispatcherCollapse(5m)` merges same-type/actor events |
| Dead-letter outbox events | `MarkFailed` sets `dead_letter = true` after `maxRetries` |
| Oversized file rejected early | `cappedReader` limits before storage write |
| MIME type bypass via extension mismatch | `mime.ParseMediaType` on declared `Content-Type` |
| Rate limiter memory leak | `idleTTL` eviction + periodic cleanup goroutine |
| Graceful shutdown drain order | Health → NOT_SERVING → stop relay/GC → HTTP shutdown → gRPC GracefulStop |
