Good, I've now verified every claim in your analysis against the actual code. The evidence is solid and well-targeted. Let me offer a few refinements and additional observations from reading the full codebase.

---

## Additional evidence & nuance

### 1. The outbox event gap is wider than stated (directions 2, 4)

You correctly found that `file.Create` produces no outbox events. But the gap extends further — here are the specific operations that write **audit logs** but produce **no outbox events** (and thus no notifications or post-processing triggers):

| Operation | Audit | Outbox event | Notification |
|---|---|---|---|
| `file.Create` | No | No | — |
| `file.CompleteUpload` | No | No | — |
| `ShareFile` (`service_share.go:53`) | `AuditFileShareGrant` | **No** | Recipient gets nothing |
| `UnshareFile` | `AuditFileShareRevoke` | **No** | — |
| `comment.Like`/`Unlike` (`comment.react()`:196) | No | **No** | Author gets nothing |
| `message.Delete` | `AuditMessageDelete` | **No** | — |

Only **three** outbox event types exist in the entire codebase: `message.sent`, `message.read`, `comment.replied`. This is visible in the dispatcher's `notificationFor` switch statement at `internal/service/notification.go:189-251` — the `default: return nil` branch silently swallows every other event type.

### 2. Direction 2 (media pipeline) is cheaper than the 2-3 sprint estimate

The `outbox.Chain` pattern is already wired and production-tested. In `app.go:158`:

```go
relayHandler = outbox.NewChain(dispatcher, webhookHandler)
```

Adding a third handler to this chain is a one-line change in the composition root. The actual cost is purely the handler implementation — the outbox relay infrastructure, polling, retry, backoff, dead-letter, and transaction safety are all done:

- **Sprint 1**: `file.process` event emission in `Create`/`CompleteUpload` (2 lines each) + `ProcessingHandler` that shells out to `ffmpeg`/`ImageMagick` via `exec.Command` (non-blocking, outbox retry handles failures)
- **Sprint 2**: store derivative files + expose via `?size=thumbnail` query param

This is closer to 1 sprint for image thumbnails alone (which the user flags as highest ROI).

### 3. Direction 3 has an existing pattern to follow

The `StorageRefRepository` (`internal/repository/storage_ref.go`) already demonstrates the materialized-counter pattern for storage keys. The `UnreadCounter` does the same for notifications. A `ResourceUsage` table would follow this exact blueprint:

```go
// Existing pattern — internal/repository/storage_ref.go
type StorageRefRepository interface {
    Increment(ctx, storageKey) (int, error)
    Decrement(ctx, storageKey) (int, error)
    GetCount(ctx, storageKey) (int, error)
}
```

A `ResourceMeter` would parallel this:
```go
type ResourceMeter interface {
    Increment(ctx, ownerUID, resourceType, delta) error  // on File.Create, Message.Send
    Decrement(ctx, ownerUID, resourceType, delta) error  // on File.Delete (soft), Message.Delete
    GetUsage(ctx, ownerUID, resourceType) (int64, error) // for GET /v1/usage
    Check(ctx, ownerUID, resourceType, delta) (bool, error) // pre-check before allocation
}
```

The key nuance you correctly identified — presigned upload paths need pre-reservation — is essential. The `PresignUpload` returns a `FileID` in advance; the quota should be reserved there and confirmed/released in `CompleteUpload`.

### 4. Direction 5 (SSE sticky routing) has a latent interface mismatch

The `hub.Pusher` interface (from `hub/adapter.go`) and the `service.NotificationPusher` interface (from `service/notification.go`) have different signatures:

```go
// hub.Pusher (older)
Publish(uid, eventType, data string)

// service.NotificationPusher (newer, production-wired)
Publish(uid, eventType, data string, eventID int64)
```

The adapter bridges this, but any Redis pub/sub replacement must satisfy the `service.NotificationPusher` interface (with `eventID`) — the older `hub.Pusher` is only used in tests. This means if you introduce a `RedisHub`, it must implement `Publish(uid, eventType, data string, eventID int64)`.

Also: the `MissedNotifFetcher` interface in `events.go` uses `ListSince(ctx, recipientUID, since time.Time, limit int)`. This is backed by the notification repository, which queries the `notifications` table. In a multi-instance scenario, `last_event_id` replay still works correctly — every instance reads from the same PG table. So `last_event_id` recovery is instance-agnostic and inherently correct. Only the real-time push path breaks.

### 5. Direction 4 (activity feed) can borrow FTS infrastructure

The search service (`internal/search/`) already has:
- Cross-entity FTS with `tsvector` + GIN index
- CJK ILIKE fallback
- Multi-kind queries (`messages/comments/files`)

An activity feed is essentially the same query plane with a different access pattern: `WHERE (target_owner_uid = ? OR actor_uid = ?) ORDER BY created_at DESC`. The `tsvector` indexing is overkill for structured queries, but the **cursor-based pagination** (`WHERE id < cursor ORDER BY id DESC LIMIT n+1`) pattern is already standardized across every list endpoint and would be reused directly.

### 6. Minor correction: app.go wires `service.NewFileService`, not `internal/file`

Your evidence in direction 2 (media pipeline) references `internal/file/service_crud.go` — but the actual production wiring in `app.go:162` uses `service.NewFileService` from `internal/service/file.go`. Both implementations are structurally identical (same flow, same missing outbox events), so the conclusion is unchanged. However, `internal/service/file.go` (204 lines) is the authoritative version; `internal/file/` appears to be an earlier extraction that isn't wired in `app.go`.

---

## Refined priority suggestion

Your P0/P1/P2 mapping is sound. One refinement to the *sequencing*:

| Sprint | Direction | Rationale |
|--------|-----------|-----------|
| **1** | Direction 3 (metering) + Direction 2 (thumbnails) | Metering closes the immediate ops gap; image thumbnails use the same file-vertical changes and share no outbox conflicts |
| **2** | Direction 2 continuation (video/doc previews) + Direction 1 (Storage Copy method) | Add `Copy` to `FileStorage` interface as foundation for tiering |
| **3** | Direction 1 (migration worker) | Now that `Copy` exists and metering identifies cold candidates |
| **4** | Direction 5 (sticky session first, Redis if needed) | Image thumbnails + quotas bought breathing room |
| **5+** | Direction 4 (activity feed) | Depends on the outbox events from sprints 1-4 |

The key insight: **directions 2 and 3 touch the same file-vertical code** (the `Create` method). Implementing them in the same sprint halves the per-direction overhead because the `Create` method is edited once — add both `resource_meter.Increment(...)` and `outbox.NewEvent("file.process", ...)` in the same patch. The engineering cost of (2+3 combined) is less than the sum of their individual costs.
