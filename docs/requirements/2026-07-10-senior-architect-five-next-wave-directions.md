# Seniour Architect / PM Perspective: Five Next-Wave Extension Directions

> **Date:** 2026-07-10
> **Scope:** Full-codebase scan of `main` (22 migrations, 38 gRPC RPCs, dual-transport parity, outbox,
> SSE hub, circuit breaker, storage GC, admin RBAC, search, preferences, notification collapsing).
> **Goal:** Identify genuinely novel high-value directions not already covered by ROADMAP.md or any
> existing `docs/requirements/` analysis. No code was changed.

---

## Executive Summary

The codebase is remarkably mature for an MVP — the foundation layer (outbox, circuit breaker, SSE hub,
idempotency, rate limiting, metrics, tracing, storage GC, dedup, RBAC, webhook forwarding) is already
built and tested. The gaps that remain are **structural expansions** rather than incremental features.

Five directions emerge from the scan, each grounded in concrete code evidence. They are ordered by
strategic leverage (product impact × architectural necessity):

| # | Direction | Why now | Evidence gap |
|---|-----------|---------|-------------|
| 1 | **Group Conversations & Multi-party Channels** | Transforms messaging from DMs to real-time collaboration | `messages` schema has `FromUID`/`ToUID` only — no `channel_id`, no membership, no group metadata |
| 2 | **Distributed Coordination Layer (Shared State)** | Horizontal scaling breaks per-instance rate limiting, idempotency, dedup | Config declares "distributed limiting needs shared state — out of scope"; no Redis, no distributed lock primitive |
| 3 | **Push Notification Infrastructure (FCM/APNs)** | SSE is web-only; mobile delivery is zero | `hub.Hub` delivers only for connected SSE clients; no device-registry table, no push token storage, no platform dispatch |
| 4 | **Automated Content Moderation Pipeline** | Manual reporting is unsustainable at scale; liability grows | `comment_reports` + manual hide is the entire moderation surface — no auto-detection, no image scanning, no spam filter, no user reputation |
| 5 | **File Versioning & Content History** | Overwrite = data loss; enterprise compliance demands audit trail | `files` upsert replaces the row; no version table, no diff metadata, no `superseded_by` chain |

---

## Direction 1 — Group Conversations & Multi-party Channels

### Why this is genuinely new

The entire messaging subsystem is designed around **1:1 peer conversations**. Every message row has
`from_uid` and `to_uid`; the service layer validates that only those two participants can read.
There is no `channel_id` column, no membership table, no group metadata. The `ListConversation`
method always takes exactly two uids. The permission check `CanReadMessage` returns true iff the
actor is one of the two parties.

### What exists today

```go
// internal/domain/access.go — the full extent of message access control
func CanReadMessage(actor string, m *Message) bool {
    return m.FromUID == actor || m.ToUID == actor
}
```

```go
// internal/domain/shared.go — message kind enum has no "group" variant
const (
    MessageKindText   MessageKind = 1
    MessageKindImage  MessageKind = 2
    MessageKindFile   MessageKind = 3
    MessageKindSystem MessageKind = 4
)
```

```go
// internal/message/service.go — Send() validates exactly one recipient
func (s *service) createMessage(ctx, actor, SendMessageInput{ToUID: toUID, ...}) { ... }
```

**Evidence:** The `messages` table migration (000002) defines `from_uid` and `to_uid` as `TEXT NOT
NULL`. There is no `channel_id` column, no `message_participants` table, no `channels`/`groups`
table in any of the 22 migrations. The `ListConversation` repository method takes `(uidA, uidB)`
— it is fundamentally a pairwise query.

### Proposed scope

| Sub-direction | Description | Evidence |
|---|---|---|
| **Channels table** | New `channels` entity with id, type, name, avatar, created_at, owner_uid | No such table exists |
| **Membership table** | `channel_members` with uid, channel_id, role (admin/member), joined_at, notification_prefs override | No membership model at all |
| **Message schema migration** | Add nullable `channel_id` to messages; `from_uid` + `channel_id` for group messages; `to_uid` can be NULL in group context | Current schema enforces exactly one recipient |
| **Service layer** | `SendToChannel(ctx, channelID, content)`; `ListChannels(ctx)`; `JoinChannel`/`LeaveChannel`; permission checks via membership | Service only knows `Send(ctx, SendMessageInput{ToUID: ...})` |
| **Outbox integration** | System messages for member join/leave, channel rename (via `MessageKindSystem`); notifications scoped to channel participants | System messages exist but are 1:1 only |
| **SSE events** | Real-time channel updates pushed per-member via `hub.Hub` | SSE hub works per-uid; group push would iterate over member list |

### Impact

- **Product:** Transforms "DMs" into "real-time collaboration" — the single highest-leverage product
  expansion on this codebase.
- **Architecture:** Requires a new domain aggregate (Channel), new repositories, new permission model
  (membership-based instead of pairwise). The service layer grows but doesn't break — `SendToChannel`
  is additive alongside the existing `Send`.
- **Risk:** Low-medium. The outbox, notifications, SSE hub, and block system all operate per-uid and
  adapt naturally to channel membership via an additional lookup.

---

## Direction 2 — Distributed Coordination Layer (Shared State)

### Why this is genuinely new

The config explicitly documents that rate limiting is **per-instance**:

```go
// internal/config/config.go — RateLimitConfig docstring
// Limiting is PER-INSTANCE; distributed limiting would need shared state
// (e.g. Redis) and is out of scope.
```

With N replicas behind a load balancer, the effective rate limit is N × configured RPS — a real
production gap. But the gap runs deeper: the idempotency store (`GormStore`) is Postgres-backed,
so it's already shared (good), but the lock is at the row level with a 2-minute reservation TTL;
a crashed-leader retry can wait up to 2 minutes. The dedup path (`FindByChecksum`) has no
distributed lock at all — two concurrent uploads of the same file on different replicas both
compute the same checksum, and both may attempt to create a file row; the second insert fails
on checksum uniqueness, but the first may have already written the blob.

### Evidence gaps

| Component | Per-instance problem | Production impact |
|---|---|---|
| **Rate limiter** (`internal/pkg/ratelimit`) | In-memory token-bucket per replica | N × rate effective limit; one abusive uid can exhaust one bucket while other replicas accept at full speed |
| **Content dedup** (`internal/file/service_crud.go`) | `FindByChecksum` + `Create` race with no distributed lock | Two replicas uploading same file both compute checksum → both insert → one fails on unique constraint; orphaned blob on the loser |
| **Circuit breaker** (`internal/sso/breaker.go`) | Per-instance state | SSO outage trips breaker on replica A while replica B keeps hammering SSO |
| **Idempotency** (`internal/idempotency/store.go`) | Postgres row-lock with 2min reservation TTL | Fine for correctness but slow on leader crash; no shared in-memory fast path |

### Proposed scope

| Sub-direction | Description |
|---|---|
| **Redis-backed rate limiter** | Replace the in-memory `ratelimit.Limiter` with a sliding-window counter in Redis; fall back to the in-memory limiter when Redis is unreachable (fail-open) |
| **Distributed dedup lock** | Acquire a short-lived Redis lock on `checksum:{sha256}` before `FindByChecksum` → `Create`; release after commit. This closes the double-upload race without a table-level lock |
| **Shared circuit breaker** | Write breaker state to Redis so one replica's "SSO is down" signal is visible to all replicas |
| **Idempotency fast path** | Keep the Postgres authoritative store; add a Redis TTL cache as a read-through front so repeated retries return faster without hitting the DB |
| **Semantic: do not require Redis** | All Redis dependencies must degrade gracefully — the in-memory limiter, per-instance breaker, and Postgres idempotency remain as fallbacks. Redis is an optional production-hardening layer |

### Impact

- **Product:** Zero user-facing change, but enables horizontal scaling with predictable SLA.
- **Architecture:** Introduces a new `pkg/distributed` or `pkg/redis` package. The `storage.FileStorage`
  interface and `sso.Client` already abstract their backends; this follows the same pattern.
- **Risk:** Low. Every component already has a single-instance fallback; Redis is additive.

---

## Direction 3 — Push Notification Infrastructure (FCM / APNs)

### Why this is genuinely new

Real-time delivery is handled by the SSE hub (`internal/hub/hub.go` + `internal/handler/http/events.go`).
SSE works only when the client maintains an open HTTP connection. There is zero mobile push capability:

- No `device_registrations` table or migration
- No push token storage (FCM `registration_token` or APNs `device_token`)
- No platform dispatch logic (Android vs iOS vs Web Push)
- No push notification template system

### Evidence

```go
// internal/hub/hub.go — the only real-time delivery primitive
type Hub struct { subs sync.Map /* uid → *uidSubs */ }
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) { ... }
func (h *Hub) pub(uid string, evt SSEEvent) { ... }
```

The `NotificationDispatcher` pushes to the hub after creating a notification row:

```go
// internal/service/notification.go — the only "push" in the system
if d.pusher != nil {
    payload, _ := json.Marshal(n)
    d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
}
```

There are zero references to `firebase`, `fcm`, `apns`, `push`, `device_token`, or `registration`
anywhere in the codebase. The outbox webhook (`internal/outbox/webhook.go`) ships events to an
external HTTP endpoint, but that's for server-to-server integration, not end-user push.

### Proposed scope

| Sub-direction | Description |
|---|---|
| **Device registration table** | New migration: `device_registrations` with uid, platform (ios/android/web), device_token, locale, created_at, updated_at |
| **Register/Unregister API** | HTTP + gRPC endpoints: `POST /v1/devices` (body: platform, token), `DELETE /v1/devices/:token` |
| **Push dispatcher** | New `internal/push/` package with a `PushDispatcher` that implements `outbox.Handler` (like `NotificationDispatcher`) — intercepts outbox events and dispatches via FCM/APNs for each device of the recipient |
| **Platform clients** | HTTP clients for FCM (v1 HTTP API) and APNs (HTTP/2). Lightweight, with configurable credentials via env (`AERO_ID_FCM_CREDENTIALS`, `AERO_ID_APNS_KEY`) |
| **Outbox chain extension** | Wire the push dispatcher into the existing outbox `Chain` alongside `NotificationDispatcher` and (optional) webhook handler |
| **Preference-aware** | Respect `notification_preferences` (enabled, quiet hours, timezone) before dispatching — reuse the existing `PreferenceChecker` |

### Impact

- **Product:** Enables mobile clients. Without this, any mobile app built on aero-id is polling-only.
  This is the single biggest UX gap for non-web clients.
- **Architecture:** Additive. The outbox `Chain` pattern (`internal/outbox/chain.go`) was explicitly
  designed for this — handlers compose linearly. `NotificationDispatcher` already does the block check,
  preference check, rate limit, and collapse; push can compose after it.
- **Risk:** Low. FCM/APNs client libraries are thin HTTP wrappers; the integration surface is small.

---

## Direction 4 — Automated Content Moderation Pipeline

### Why this is genuinely new

The current moderation surface is entirely manual:

- Comment reporting (`comment_reports` table, `Report` / `ResolveReport` APIs)
- Author-only hide/unhide (`SetStatus(CommentStatusHidden)`)
- Role-based permission check (`PermContentModerate`)

There is **no automated detection of any kind** — no spam filter, no profanity/toxicity check,
no image moderation (NSFW detection), no user reputation scoring, no automatic action on
threshold breach (e.g., 3 reports in 10 minutes → auto-hide).

### Evidence

```go
// internal/domain/admin.go — the two moderation-related permissions
const (
    PermContentModerate = "content.moderate"
    PermAuditRead       = "audit.read"
    PermUserBan         = "user.ban"
    PermSystemConfig    = "system.config"
)
```

```go
// internal/comment/service.go — Report() creates a row and stops.
// No side-effect beyond the report row itself.
func (s *serviceImpl) Report(ctx context.Context, id ID, reason string) error {
    // ... validates, creates report, returns ...
}
```

```go
// Migration 000007 — comment_reports table has no trigger, no counter, no auto-action
CREATE TABLE comment_reports (
    id           BYTEA PRIMARY KEY,
    comment_id   BYTEA NOT NULL REFERENCES comments(id),
    reporter_uid TEXT NOT NULL,
    reason       TEXT NOT NULL DEFAULT '',
    status       SMALLINT NOT NULL DEFAULT 1,  -- 1=open
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at  TIMESTAMPTZ,
    resolved_by  TEXT NOT NULL DEFAULT '',
    UNIQUE(comment_id, reporter_uid)
);
```

No column for `auto_resolved`, `auto_hidden`, `moderation_confidence`, `scan_status` (pending/scanned/errored),
no `moderation_queue` table priority. No event in the outbox for "new report" — the action is purely
synchronous and silent.

### Proposed scope

| Sub-direction | Description |
|---|---|
| **Content scanning service** | New `internal/moderation/` package with a `Scanner` interface: `ScanText(ctx, content) → Score, Labels, Error` and `ScanImage(ctx, blob) → Score, Labels, Error`. Concrete implementations: (a) no-op (dev), (b) configurable regex/pattern-based filter, (c) optional integration with external API (e.g., Google Cloud Vision, Perspective API) |
| **Pipeline hook in comment Create** | After `CreateWithReplyBump` succeeds, write a `comment.sent` outbox event → the outbox relay picks it up, runs text scan, and auto-hides or flags for review based on threshold |
| **Auto-moderation action** | When scan score exceeds configurable thresholds: (a) `soft_block` — auto-hide the comment but send the author a notification, (b) `hard_block` — auto-hide + flag for moderator review, (c) `flag_only` — escalate to moderator queue with scan labels |
| **Moderation queue** | `GET /v1/admin/moderation-queue` (HTTP + gRPC) with filters: status (pending/reviewed/resolved), min_score, date range. Permissions-gated (`content.moderate`) |
| **User reputation** | Track `comment_reports` count per uid, auto-flag users with high report-to-comment ratio. Seed `user_notes`/`user_flags` for moderators |
| **Image moderation** | For comment attachments (future) and uploaded files: optional scan in the file `Create` / `CompleteUpload` path with a configurable `AERO_ID_MODERATION_SCAN_IMAGES=true` flag |
| **Spam detection** | Repeated identical content across comments → auto-flag; same URL posted N times in quick succession → auto-hide |

### Impact

- **Product:** Safety at scale. Without automation, a viral post with 10K comments generates 10K
  reports that a moderator must triage individually — impossible.
- **Architecture:** The outbox pattern is perfect here — `comment.sent` events are already written
  (for reply notifications); a `ModerationDispatcher` handler in the Chain adds scanning without
  slowing the write path. The scanner interface keeps the core testable with a no-op or fake.
- **Risk:** Medium. Image scanning adds latency; external API calls may fail (fail-open: let the
  comment through, flag for review). Text scanning must be careful about false positives.

---

## Direction 5 — File Versioning & Content History

### Why this is genuinely new

When a user uploads a file that content-dedup matches an existing one, the `Create` path reuses the
existing storage key — correct and space-efficient. But when a user uploads a **different** file
(perhaps an updated version of a document), the old blob is simply orphaned (if the old row is
soft-deleted → GC eventually purges it) or overwritten (if the new row references a new key).
There is:

- No concept of a "file version" — no `file_versions` table
- No `superseded_by` / `supersedes` column on the `files` table
- No endpoint to list previous versions
- No endpoint to restore a previous version
- No diff metadata (size diff, checksum diff)

### Evidence

```go
// internal/domain/file.go — File domain model has no version field
type File struct {
    ID          ID
    OwnerUID    string
    Name        string
    Size        int64
    MimeType    string
    StorageKey  string
    StorageType string
    Checksum    string
    Visibility  Visibility
    Meta        map[string]any
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

```go
// internal/file/service_crud.go — Create() dedups by checksum but only
// for the same owner — there is no "create new version" path.
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*File, error) {
    // ... computes checksum, dedups against existing ...
    // Reuses the existing StorageKey; no version chain created.
}
```

The dedup index is `idx_files_checksum ON files(owner_uid, checksum)`. A new upload with a
different checksum (e.g., updated document) creates a brand-new `File` row — the old row is still
there but invisible to the user unless they paginate through their file list.

### Proposed scope

| Sub-direction | Description |
|---|---|
| **file_versions table** | New migration: `file_versions(id, file_id, version_number, storage_key, checksum, size, mime_type, meta, created_at)` — each upload that changes the blob creates a new version row |
| **Versioning service** | `CreateWithVersion(ctx, ...)` — on content change, create a new `File` row (same `name`, new `id`) and link via a `file_group_id` or `canonical_id`; old row becomes a "version" (soft-deletable independently) |
| **Version list API** | `GET /v1/files/:id/versions` — returns version history with cursor pagination |
| **Version restore API** | `POST /v1/files/:id/versions/:version_id/restore` — creates a new current version from a historical blob |
| **Version pruning policy** | Configurable `storage.version_retention` — keep last N versions per file by age or count; older versions are soft-delete candidates for the GC sweep |
| **Content diff metadata** | Optional: store `sha256:previous → sha256:current` diff hint in version metadata for frontend "what changed" display |

### Impact

- **Product:** Enterprise-grade document management. For any B2B use case (collaboration, document
  sharing, content management), versioning is table stakes.
- **Architecture:** Versioning is almost entirely **additive** — the existing `File` CRUD is
  unchanged. A new `VersionRepository` plus an optional `fileService.CreateVersion` wrapper.
  Storage GC must be version-aware (don't GC a blob referenced by any version, even if the primary
  File row is soft-deleted) — but `CountAnyByStorageKey` already handles this (it counts all rows
  referencing a key, including soft-deleted ones).
- **Risk:** Low. The data model scales naturally. The only complexity is GC interaction (already
  handled by existing ref-counting logic).

---

## Cross-cutting Considerations

### Migration sequencing

All five directions are additive and non-breaking. The recommended order is:

| Phase | Direction | Rationale |
|---|---|---|
| **Phase 1** | Distributed Coordination (Dir 2) | Enables reliable horizontal scaling; no schema change; all-else benefits from shared state |
| **Phase 2** | Group Conversations (Dir 1) | Largest product impact; needs the rate-limiting foundation for channel spam protection |
| **Phase 3** | Push Notifications (Dir 3) | Unlocks mobile; needs the outbox chain that already exists |
| **Phase 4** | Content Moderation (Dir 4) | Risk multiplier at scale; best preceded by push (so moderators get real-time alerts) |
| **Phase 5** | File Versioning (Dir 5) | Purely additive; lowest urgency unless B2B requirements appear |

### Edge cases each direction surfaces

| Direction | Key edge cases |
|---|---|
| **Group Conversations** | Orphan channel (no members) on last-leave; channel deletion vs soft-delete; notification amplification (1 message → N push notifications per member); block semantics in group context (if A blocks B, does B see A's messages in the shared channel?) |
| **Distributed Coordination** | Redis outage → degrade to per-instance (no cascade failure); clock skew in Redis TTL expiry; eviction policy interaction with rate-limit keys |
| **Push Notifications** | Token expiry (FCM/APNs return `Unregistered` → must purge token); multi-device dedup (don't push twice to the same physical device); silent push vs alert push respects quiet hours; payload size limits (4KB FCM, 4KB APNs) |
| **Content Moderation** | False positives (innocent content flagged) → appeal flow needed; language/region-specific rules; scanning cost at scale (external API calls per upload); race between moderator action and auto-action; adversarial content designed to evade scanning |
| **File Versioning** | Dedup + version interaction: same checksum across versions should reuse storage key (cheap); version hard-delete must check ref counts before blob deletion; large-number-of-versions per file → pagination performance; version names differ from current name |

### Testability

Each direction follows the existing pattern: interface-separated service + repository, unit-testable
with mocks/fakes, integration-testable with testcontainers. The SSE hub, outbox relay, and GC
sweeper all have testable patterns to follow.

---

## Appendix: Code Evidence Index

| Direction | Key file(s) | Lines / schema evidence |
|---|---|---|
| Group Conversations | `migrations/000002_create_messages.up.sql` | `from_uid TEXT NOT NULL, to_uid TEXT NOT NULL` — no channel concept |
| Group Conversations | `internal/domain/access.go` | `CanReadMessage` checks `m.FromUID == actor || m.ToUID == actor` |
| Group Conversations | `internal/message/service.go` | `Send()` takes `SendMessageInput{ToUID: ...}` — single recipient |
| Distributed Coordination | `internal/config/config.go` | `Limiting is PER-INSTANCE; distributed limiting would need shared state...out of scope` |
| Distributed Coordination | `internal/file/service_crud.go` | `FindByChecksum` + `Create` race — no distributed lock |
| Distributed Coordination | `internal/sso/breaker.go` | Per-process `breaker.mu` — no shared state across replicas |
| Push Notifications | `internal/hub/hub.go` | Only SSE push exists — `pub(uid, evt)` iterates in-memory subscribers |
| Push Notifications | `internal/service/notification.go` | After notification row create: `pusher.Publish(uid, type, data, eventID)` — only SSE |
| Push Notifications | Codebase grep | Zero references to `fcm`, `apns`, `device_token`, `push` |
| Content Moderation | `internal/comment/service.go` | `Report()` — creates row, returns. No auto-action, no scan, no trigger |
| Content Moderation | `migrations/000007_create_comment_reports.up.sql` | No scan_status, no confidence, no auto_resolved columns |
| Content Moderation | `internal/domain/admin.go` | `PermContentModerate` — only human-initiated actions |
| File Versioning | `internal/domain/file.go` | `File` struct has no version_id, no superseded_by, no file_group_id |
| File Versioning | `internal/file/service_crud.go` | `Create` dedups but never creates a version chain |
| File Versioning | `internal/file/file.go` | `Repository` interface has no version-related methods |
