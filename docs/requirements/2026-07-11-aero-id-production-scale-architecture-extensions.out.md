Your analysis is exceptionally thorough — you've clearly done the cross-referencing work. Let me provide a targeted assessment on each direction, grounded in what I've just verified against the code.

---

## Verification Results Against Live Codebase

### Direction 1: Distributed Rate Limiting — **Confirmed, with a refinement**

You correctly identified the core issue. `internal/pkg/ratelimit/ratelimit.go` is purely in-process:

```go
type bucket struct {
    tokens   float64
    last     time.Time     // no instance ID, no coordination
    lastSeen time.Time
}
type Limiter struct {
    mu      sync.Mutex
    buckets map[string]*bucket  // per-instance only
}
```

And `config.go` explicitly documents the gap:
```go
// Limiting is PER-INSTANCE; distributed limiting would need
// shared state (e.g. Redis) and is out of scope.
```

**Refinement:** The analysis implies this is a single limiter shared between HTTP and gRPC. That's correct — `app.go:184-210` shows one `limiter` injected into both. But there's a subtlety: the gRPC interceptor uses `Allow` per-RPC, while HTTP middleware uses it per-request. In multi-replica, an attacker sending 50 RPCs via gRPC + 50 via HTTP to different instances would each pass their local bucket. The gap is even wider than stated.

**One detail the analysis misses:** the `Allow()` method has a **fail-open** path:
```go
func (l *Limiter) Allow(key string) bool {
    if l == nil || l.rate <= 0 || l.burst <= 0 { return true }
```
If a nil limiter is mistakenly injected (e.g. due to config race during rolling restart), **all rate limiting disappears** silently. No panic, no log. This is a safe default for dev but dangerous in multi-replica prod where configuration drift between instances could leave some replicas unlimited.

---

### Direction 2: Multi-Replica SSE — **Confirmed, with a caveat**

`internal/hub/hub.go` is pure `sync.Map`:
```go
type Hub struct {
    subs   sync.Map // uid → *uidSubs — same-instance only
}
```

And `adapter.go`:
```go
func (h *Hub) Publish(uid, eventType, data string, eventID int64) {
    h.pub(uid, SSEEvent{ID: eventID, Type: eventType, Data: data})
}
```

The `pub` method:
```go
func (h *Hub) pub(uid string, evt SSEEvent) {
    raw, ok := h.subs.Load(uid)
    if !ok { return }  // <-- silent no-op for cross-instance subscribers
```

**Caveat:** The analysis references `internal/notification/dispatcher.go` in the old path. That file still exists but the current codebase has been refactored — the dispatcher now lives in `internal/service/notification.go` as `NotificationDispatcher`. The evidence holds in both versions; the current `service.NotificationDispatcher.Handle()` does:

```go
if d.pusher != nil {
    payload, _ := json.Marshal(n)
    d.pusher.Publish(n.RecipientUID, n.Type, string(payload), n.CreatedAt.UnixMilli())
}
```

No cross-instance awareness. Same conclusion.

**PG LISTEN/NOTIFY recommendation is solid** — it uses zero new dependencies (PostgreSQL is already mandatory). One implementation detail to add: the NOTIFY payload is limited to 8000 bytes by PostgreSQL, so large notification payloads would need a lookup path through the database rather than inline delivery. The design should account for this.

---

### Direction 3: Storage Quota — **Confirmed**

The evidence is clean:

`file.go`'s `Create` method:
```go
if s.maxBytes > 0 && in.Size > s.maxBytes {
    return nil, apperr.BadRequest("file.too_large", ...)
}
// No total-usage check follows
```

`repository/file.go` has no `SUM(size)` method. The closest thing is `CountByStorageKey` (ref count) and `ListByOwner` (paginated — useless for aggregation).

**Nuance the analysis could add:** There's an existing `storage_refs` table (used via `storageRefRepo = repository.NewStorageRefRepository(gdb)` in `app.go:110`). This table tracks reference counts per storage key. A quota system could repurpose or parallel this pattern — `user_storage_usage(owner_uid TEXT PRIMARY KEY, total_bytes BIGINT)` kept in sync via the same outbox events. The infrastructure for event-driven aggregation already exists.

---

### Direction 4: DDoS/Abuse Protection — **Mostly confirmed, but some claims need moderation**

**Evidence fully supports:**

1. **No SSE connection limits:**
```go
// hub.go Subscribe
ch := make(chan SSEEvent, 16) // 16-buffer per connection, no per-uid limit
```

2. **No IP-level limiting for authenticated paths** — the ratelimit key is `uid`, not `ip`.

3. **SSE slow consumer:** `pub()` drops to `default:` silently:
```go
select {
case ch <- evt:
default: // drop silently
}
```

**Claim that needs moderation:** "CJK ILIKE fallback can trigger full table scans on files, messages, and comments."

I checked the actual queries:
- `SearchFilesLike`: `WHERE deleted_at IS NULL AND owner_uid = ? AND name ILIKE ?` — the `owner_uid` filter has an index (from `000015_perf_indexes.up.sql`). On `messages` and `comments`, the Like queries similarly filter by `(owner_uid` or `(from_uid, to_uid)` which are indexed. The scan is not a full table scan — it's an indexed scan on owner scope + ILIKE filter. For a single user's data, this is bounded to that user's rows. The risk is more about **CPU amplification on large per-user datasets** than DB-wide collapse.

**However**, the multi-kind search (up to 3 kinds × 5s timeout = 15s worst case) is a real concern for connection pool exhaustion: an attacker can open 50 concurrent search requests, occupying 50 DB connections for up to 15s each. With `MaxOpenConns` typically 25-50, this exhausts the pool for everyone.

---

### Direction 5: Data Lifecycle — **Confirmed, with a correction**

**Confirmed points:**

1. No cascading delete on file delete:
```go
func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
    // soft-deletes file row, decrements ref count, maybe deletes blob
    // No: file_shares cleanup, comment cleanup, outbox event cleanup
```

2. `file_shares` has no FK:
```sql
CREATE TABLE file_shares (
    file_id UUID NOT NULL,
    -- no FOREIGN KEY (file_id) REFERENCES files(id) ON DELETE CASCADE
```

3. No user data export endpoint — confirmed by scanning all registered handlers in `app.go`:
```go
httpapi.NewFileHandler(...)
httpapi.NewMessageHandler(...)
// ... no NewExportHandler
```

4. No SSO user-deletion callback — confirmed by scanning `sso/client.go` interface (only `ValidateToken`, `GetUsers`, `HealthCheck`).

**Correction needed:** The analysis says "soft-deleted file rows past their retention window are physically deleted" isn't there. But `StorageGCConfig` and `storage/gc` sweeper already exist:
```go
type StorageGCConfig struct {
    Enabled   bool
    Retention time.Duration // how long a soft-deleted row is kept before purge
    ...
}
```
The GC sweeper does purge soft-deleted rows past retention — it's opt-in (`Enabled: false` by default) but the infrastructure exists. The gap is that it only handles **file rows and blobs**, not messages, comments, notifications, audit logs, or file_shares.

---

## Cross-Direction Observations

### Overlap between Directions 1 and 4

Both point at the same root cause for different effects. Direction 1 is about **intentional bypass** (attacker exploiting N× capacity), while Direction 4 is about **resource exhaustion** (connection DoS, DB pool saturation). A unified solution could be a **distributed token bucket in Redis** that covers both rate limiting AND connection budget (each SSE connection consumes from a per-uid connection budget keyed in Redis).

### Direction 2's PG LISTEN/NOTIFY interacts with Direction 5

If you implement PG LISTEN/NOTIFY for cross-instance SSE delivery, the NOTIFY payload's 8000-byte limit means large notification payloads must be looked up from the DB. That naturally aligns with Direction 5's data retention — notifications older than the TTL would be cleaned up, and the SSE listener would simply get a 404-like "notification not found" and skip. This is actually a clean design forcing function.

### Missing from the analysis: idempotency key store as a multi-replica concern

`internal/idempotency/gorm_store.go` uses GORM-backed storage, which IS shared (same database). This is actually **already correct** for multi-replica. Worth noting as a contrast: the idempotency layer doesn't need fixing — it's a model for how other subsystems should be adapted.

---

## Verdict

All five directions are **valid, accurately evidenced, and not already covered by existing `docs/requirements/*` documents** (I checked the 292 files — none frame these as multi-replica scaling challenges with this code evidence structure).

Priority assessment: **Agreed**. Directions 1 and 2 are P0 because they are **silent failures** under replica count > 1. No crash, no error log, just wrong behavior.

The analysis would benefit from one addition: a **discovery protocol** section. Currently aero-id has no mechanism for instances to discover each other (no gossip, no registry, no shared state for peer enumeration). Any coordination solution (Redis, PG NOTIFY, gRPC broadcast) needs to either be dependency-based (Redis) or leverage existing infrastructure (PG). The analysis correctly recommends PG for Direction 2. For Direction 1, Redis is the right call because PG NOTIFY has no rate-limit semantics and polling the DB for rate state would defeat the purpose.

Are you looking for me to implement any of these directions, or is this analysis being reviewed before planning?
