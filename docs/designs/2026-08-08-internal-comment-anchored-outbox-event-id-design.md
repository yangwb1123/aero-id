# Design — Stable anchor-based `event_id` for comment outbox events

Module: live layered tree (`internal/service` + `internal/repository` + `internal/outbox`). Scope: comment outbox events only; `message.*` events and the unwired parallel trees (`internal/message`, `internal/notification`) are untouched.
Input: `docs/auto/requirements/2026-08-08-internal-comment-anchored-outbox-event-id-requirements.md` (treated as untrusted claims; all re-verified against the current tree at HEAD `b005409`).

## 0. Evidence verification verdict

Every cited symbol was re-checked. Substance of the requirements doc is confirmed with **two corrections and one unaddressed hazard**; none invalidate the direction, but the receipt contract (REQ-3) must be amended before implementation.

| Claim | Verdict |
|---|---|
| `internal/service/message.go:152–161` `newEvent`, `ID: idgen.NewID()` at 155; shared by comment sites `comment.go:123,137` and `comment_reactions.go:27` | ✅ Verified verbatim |
| `internal/outbox/outbox.go:118–130` `NewEvent` leaves zero ID; only live caller is the unwired `internal/message/service.go:95,122,228` | ✅ Verified. (Note: the zero-ID hazard is historical for comments; the live hazard is the fresh v7 mint at `newEvent`.) |
| Comment events: `comment.replied` (service/comment.go:123), `file.commented`/`message.commented` (137), `comment.liked` (comment_reactions.go:27) | ✅ Verified. The four sites are exactly `replied` + `created×2` + `liked`. |
| Anchored resolve fact `EventID = reportID` (service/comment.go:400–413), pinned by `comment_test.go:574` "deterministic dedup anchor" | ✅ Verified |
| `idgen.Anchor` at idgen.go:35–48, NUL-separated v5 in fixed `AnchorNamespace`; zero-part/NUL panics; "arity ≥ 4" contract | ✅ Verified |
| `AnchorSchemeUUIDv5NulSeparated` config contract (config/validate.go:241–243; doc cited 245–246 — trivial drift) | ✅ Verified, substance exact |
| Relay at-least-once (outbox/relay.go header "Delivery is at-least-once", `drain`, claim lease, `ErrClaimLost`) | ✅ Verified |
| Dispatcher dedup-free fan-out at `internal/service/notification.go:226–289` (`Handle`) + `341–381` (`notificationFor`); fresh `idgen.NewID()` per notification row; collapse (5 min, `app/outbox.go:40`) is a merge heuristic; rate limiter 10/min wired | ✅ Verified. `internal/service/notification.go` is 438 lines; `Handle` spans ~226–296, `notificationFor` ~300–436. ⚠️ Also noted: the parallel dead tree `internal/notification/dispatcher.go` (146 lines) has a second, narrower dispatcher — do not touch it; work lands in the live tree only. |
| `outbox_events.id` is the PK (migration 000008); no separate event-identity uniqueness | ✅ Verified. ⚠️ **Bounded**: `PurgeDeadLetters` deletes published rows past `outbox_published_retention` (default 720h, configs/config.yaml:239). The PK is a backstop only while the row exists; the receipt is the durable gate. |
| Idempotency store is PostgreSQL-backed (internal/idempotency/store.go + store_redis.go); retry replays, doesn't re-execute | ✅ Verified |
| Gate compatibility with `scripts/check-anchored-event-ids.sh` (R-1 scans `EventID: idgen.NewID()` on audit facts only; R-2 bans raw `uuid.NewSHA1`/namespace constants/zero-part `Anchor` outside idgen+projection_replication) | ✅ Verified — a comment outbox anchor via `idgen.Anchor` (arity 5) is fully gate-compatible; no gate change required |
| File-size caps: comment.go 496, message.go 477, service/notification.go 438, repository/notification.go 303 — anchored builder must be a sibling file | ✅ Verified |
| **Quarantine commit attribution**: doc/evidence says `internal/comment` was deleted "in the 2026-08-08 quarantine, commit b005409" | ❌ **Corrected**: `internal/comment` (and `internal/search`) were deleted in commit `3a2bf38` ("quarantine: delete comment+search", 2026-08-08 04:47). `b005409` (2026-08-07 22:17) deleted `internal/block`. Same batch, wrong commit. Substantive conclusion (tree deleted, live surface is `internal/service/comment*.go`) stands. |
| **REQ-3 receipt key**: "durable receipt in `event_receipts` keyed by `uq_event_receipt_source_event` … `tenant_id` empty — the unique key excludes tenant" | ❌ **Blocking correction — schema drift**: migration **000050** (`scope_events_and_audit_by_tenant`) dropped `uq_event_receipt_source_event` and replaced it with **`uq_event_receipt_tenant_source_event UNIQUE (tenant_id, source, event_id)`**; `tenant_id` is `NOT NULL` with `CHECK (btrim(tenant_id) <> '')`; pre-000050 rows backfilled to `'legacy'`. Comment events carry no tenant (comments table, 000003, has no tenant column), so the design must pick a **non-empty tenant sentinel**. The doc's "tenant_id empty" cannot be implemented. (The audit-ledger dedup key is likewise now `(tenant_id, source, event_id)`, 000050:14–15.) |
| §6.2 "The only 'collision' is the intended one" | ⚠️ **Unaddressed hazard**: `comment.liked` can occur **multiple times per comment** (like → unlike → relike). With slot 5 fixed `""`, both occurrences derive the identical anchor. The second like's outbox append either PK-conflicts (pre-purge, silently swallowed — `AppendEvent` is best-effort) or the relay redelivery hits the applied receipt (post-purge) → **the relike notification is permanently lost**. Today it produces a second notification (collapsed if within 5 min). Must be an explicit product decision (§4.3, §8 D-1). |

Everything else in the requirements doc (REQ-1/REQ-2/REQ-4/REQ-5, T-5.1/T-5.2/T-5.3/T-10.1, §5 non-goals, §6.1 boundary, §7 traceability) is adopted as-is with the amendments in §4 and §9.

## 1. Problem (as verified)

1. **No identity contract on the producer side.** Every comment outbox event mints a fresh UUIDv7 at append time (`newEvent`, service/message.go:155). The stored event ID is not derivable from the event's identity, so two deliveries of the same logical event cannot be proven equal, and the durable receipt key has nothing stable to gate on.
2. **Duplicate fan-out on at-least-once delivery.** The relay re-delivers on handler error / claim loss / crash-between-effect-and-`MarkPublished` (relay.go header; `ErrClaimLost`, outbox/claim.go:85–130). `NotificationDispatcher.Handle` applies every delivery: duplicate `notifications` row, double unread increment, double SSE push. The 5-minute collapse window (app/outbox.go:40) is a merge heuristic, not a dedup guarantee (service/notification.go:283–286: the extra row "stays in the DB").
3. **Precedent already compiled in.** The report-resolve audit fact uses `EventID = reportID` (service/comment.go:400–413) with load-bearing retry-dedup, pinned by Postgres-backed parity tests. The derivation primitive (`idgen.Anchor`) and scheme contract (`AnchorSchemeUUIDv5NulSeparated`) are enforced by gates. Comment outbox events are the remaining fresh-mint surface.

## 2. Design overview

Two independent changes on the live tree:

- **A — Producer (service layer):** comment outbox events get a deterministic `event_id` derived once at append time:
  `idgen.Anchor("comment.outbox", "comment", <commentID>, <event_type>, "")` — fixed arity 5, NUL-separated, in the fixed `AnchorNamespace`. No fallback to fresh v7 or zero ID on any comment path.
- **B — Consumer (repository + dispatcher):** the comment drain path becomes receipt-gated. One transaction commits `event_receipts` insert-or-ignore keyed by `(tenant_id, source, event_id)` = `("aero-id", "aero-id", derived ID)` **atomically with** the notification effect (row + collapse decision + unread counter). A duplicate delivery finds the receipt and is a no-op. SSE push happens after commit, only when the effect applied.

No wire/HTTP/gRPC/proto API changes. No new tables, no new migrations. `message.*` events and both unwired parallel trees are untouched.

## 3. Change A — anchored producer (API change: internal, Go-level)

**New file `internal/service/comment_events.go`** (~40 lines; keeps comment.go at 496 and message.go at 477 under the 500-line gate):

```go
// commentAnchorScope is the fixed first part of the comment outbox event-ID
// anchor. Constant forever: changing it silently invalidates every historical
// derived event_id and stops receipt dedup across a rollout (mirrors the
// AnchorNamespace doc contract). Never reuse it for a site with a different
// part contract.
const commentAnchorScope = "comment.outbox"

// newCommentEvent builds a comment outbox event whose ID is a pure function of
// its identity parts (fixed arity 5: scope, aggregate type, aggregate id,
// event type, empty operation slot). Identical parts -> byte-identical ID.
// Payload and CreatedAt are deliberately not parts: the fan-out payload may
// evolve without changing event identity. Part values are NUL-free constants
// and UUID strings, so the Anchor NUL guard can never fire.
func newCommentEvent(commentID domain.ID, eventType string, payload map[string]any) *outbox.Event {
    b, _ := json.Marshal(payload)
    return &outbox.Event{
        ID:            idgen.Anchor(commentAnchorScope, "comment", commentID.String(), eventType, ""),
        AggregateType: "comment",
        AggregateID:   commentID.String(),
        EventType:     eventType,
        Payload:       b,
        CreatedAt:     time.Now().UTC(),
    }
}
```

**Call-site changes** (each is a one-line swap from `newEvent(...)` to `newCommentEvent(...)`):

| Site | Event type | Current |
|---|---|---|
| `internal/service/comment.go:123` | `comment.replied` | `newEvent("comment", c.ID.String(), …)` |
| `internal/service/comment.go:137` | `file.commented` / `message.commented` | `newEvent("comment", c.ID.String(), …)` |
| `internal/service/comment_reactions.go:27` | `comment.liked` | `newEvent("comment", id.String(), …)` |

`message.go`'s `newEvent` (fresh v7) stays for `message.*` events (message.go:106,138,238,313,451) — out of scope by REQ-2. The unwired `internal/message` tree (`outbox.NewEvent`, zero ID) is untouched.

**Derivation contract (fixed):**
- Slot 1 `"comment.outbox"` — constant forever.
- Slot 2 `"comment"` — must equal `outbox_events.aggregate_type` for these rows.
- Slot 3 — comment UUID string, byte-identical to `outbox_events.aggregate_id` and `comments.id`.
- Slot 4 — the event type string; separates distinct events on the same comment.
- Slot 5 `""` — reserved for a future stable mutation discriminator (operation ID / idempotency key). A future discriminator lands **in this slot only**, never by reordering parts. Today's `CreateCommentInput` and `Like` carry no such identity (service.go:177–183; comment_reactions.go:14–57).
- Derived **once at append time**; never re-minted, re-derived, or zeroed at delivery. `outbox_events.id` = derived ID (PK).

## 4. Change B — receipt-gated exactly-once comment dispatch

### 4.1 Receipt key contract (amended for migration 000050)

The requirements doc's REQ-3 ("`tenant_id` empty — the unique key excludes tenant") is **superseded by migration 000050**. Current schema:

```
event_receipts.tenant_id  TEXT NOT NULL  CHECK (btrim(tenant_id) <> '')   -- 000050:1-4
UNIQUE (tenant_id, source, event_id)                                      -- uq_event_receipt_tenant_source_event, 000050:6-7
```

Comment events have no tenant (comments table has no tenant column). The design therefore introduces a **reserved local tenant sentinel**:

```
const LocalEventReceiptTenant = "aero-id"   // internal/repository/notification_dispatch.go
const LocalEventReceiptSource = "aero-id"   // same label as audit facts (service/audit_fact.go:30)
```

Both constants are **exported** so the Docker-free acceptance tests can pin the amended contract by value (T-REC, §8): a regression to the obsolete empty-tenant key (`tenant_id = ''` or a `(source, event_id)`-only key) fails a test, not just a review.

- Comment receipts are written with `tenant_id = "aero-id"`, `source = "aero-id"` — the same local-source label already used for audit facts (`buildLegacyAuditFact`, service/audit_fact.go:30) and projection envelopes (service/projection_replication.go:138).
- The sentinel is a documented reservation in the `event_receipts` tenant namespace; real tenants are external identities (Snaplink tenant IDs) and never equal `"aero-id"`. The unique key `(tenant, source, event_id)` keeps the comment namespace disjoint from all snapshot-event receipts (real tenants, other sources, v7 or source-anchored event IDs).
- A pre-existing row with **any** status (including `legacy` backfill rows) makes the delivery a no-op: the key space is authoritative, not the status. The snapshot-path machinery (`claimEventReceipt` / `verifyEventAccountTenant`, repository/event_receipt.go:217–247, which require tenant + account and write leases) is **not** reused — a minimal insert-or-ignore is the contract, as the requirements doc correctly insisted.
- `source_region` is NOT NULL → write `""` (no CHECK on it). `aggregate_version` defaults to 0. `status` = `"applied"`. The NOT NULL `aggregate_type`/`aggregate_id` columns (000033:6–7) are filled from the event (`"comment"` + comment ID) — `ReceiptKey` therefore carries them (§4.2); without this the receipt insert itself fails, which T-REC-PG's raw-SQL assertion would catch.

### 4.2 Repository API (internal, Go-level)

**New file `internal/repository/notification_dispatch.go`** (keeps repository/notification.go at 303 under the cap). New method on `notificationRepo`:

```go
// ReceiptKey identifies one logical delivery on the comment drain path.
// Tenant is the reserved local sentinel (LocalEventReceiptTenant, "aero-id");
// Source is the local source label (LocalEventReceiptSource, "aero-id");
// EventID is the derived comment event ID; AggregateType/AggregateID feed the
// NOT NULL columns of the receipt row (000033:6-7).
type ReceiptKey struct {
	TenantID, Source string
	EventID          uuid.UUID
	AggregateType    string
	AggregateID      string
}

// DispatchComment applies the notification effect atomically with a durable
// (tenant, source, event_id) receipt. applied=false means a receipt already
// existed: the delivery is a no-op (the relay should mark the event
// published). surfaced is the notification that represents the effect (the
// new row, or the pre-existing row when collapsed) and is used for the
// real-time push after commit. Collapse behavior is preserved inside the
// same transaction (create row, then merge into a recent unread row).
DispatchComment(ctx context.Context, key ReceiptKey, n *domain.Notification,
    collapseWin time.Duration) (applied bool, surfaced *domain.Notification, err error)
```

One transaction:
1. Insert receipt row `ON CONFLICT (tenant_id, source, event_id) DO NOTHING` (`clause.OnConflict{Columns: …}`). RowsAffected == 0 → return `(false, nil, nil)` — no effect, no error.
2. Insert the notification row (same fields as today's `Create`).
3. Collapse check (extracted tx-scoped variant of `findRecentByTypeAndActor`); if a recent unread same-`(recipient, type, actor)` row exists and `collapseWin > 0`: `IncrementCollapsedCount` on it, `surfaced` = existing row. Else `IncrementUnreadCount` (+1) via `r.counter.IncrementNotif(ctx, tx, …)` and `surfaced` = new row.
4. Commit. Any error rolls back the receipt with the effect → the relay retry re-executes.

Behavior preservation notes:
- The current non-collapse effect order is `Create` → `IncrementUnreadCount`; the collapse path is `Create` → `FindRecent…` → `IncrementCollapsedCount` **and the extra row stays in the DB** (service/notification.go:283–286). Both are preserved inside the tx, byte-for-byte semantics.
- `r.counter == nil` → skip the counter step, exactly like today's `IncrementUnreadCount` (repository/notification.go:88–93).
- The collapse window value continues to come from `WithDispatcherCollapse(5*time.Minute)` (app/outbox.go:40).

### 4.3 Dispatcher change (`internal/service/notification.go`, live tree only)

`Handle` keeps suppression checks (prefs → rate → block) exactly as today. The effect section becomes:

```go
if e.AggregateType == "comment" {
    d, ok := d.notifs.(repository.NotificationDispatchRepository)   // extended interface, satisfied by the real repo
    if ok {
        applied, surfaced, err := d.DispatchComment(ctx,
            repository.ReceiptKey{
                TenantID:      repository.LocalEventReceiptTenant,
                Source:        repository.LocalEventReceiptSource,
                EventID:       e.ID,
                AggregateType: e.AggregateType, // "comment"
                AggregateID:   e.AggregateID,
            },
            n, d.collapseWin)
        if err != nil {
            return err // receipt+effect rolled back; relay retries
        }
        if !applied {
            return nil // duplicate delivery: exactly-once effect already happened
        }
        if d.pusher != nil {
            payload, _ := json.Marshal(surfaced)
            d.pusher.Publish(surfaced.RecipientUID, surfaced.Type, string(payload), surfaced.CreatedAt.UnixMilli())
        }
        return nil
    }
    // fallback: repository without the extended interface — reachable only from
    // unit fakes (mocks.NotificationRepo); prod wiring is pinned by T-WIRE-1/2/3
    // so the dedup-free path can never be silently active in production.
}
```

- The type assertion keeps every existing unit test (mocks without the new method) compiling and passing via the legacy path; production wiring (app/outbox.go → `repository.NewNotificationRepository`) always satisfies the extended interface. New tests use an extended mock or the real repo.
- If the file exceeds 500 lines during the edit, extract the receipt-gated comment branch into `internal/service/notification_dispatch.go` (sibling, same package) — do not grow `notification.go` past the cap.
- **Scope guard:** gating is keyed on `e.AggregateType == "comment"`. `message.*` events (fresh v7 IDs) keep the legacy path unchanged, per REQ-2/§5.

### 4.4 Decision D-1 — `comment.liked` repeat occurrences (must be made before implementation)

With slot 5 = `""`, like → unlike → relike on the same comment derives the **same** event ID for both occurrences. Consequences:
- Pre-purge: second append PK-conflicts on `outbox_events.id`; `AppendEvent` is best-effort (service/comment_reactions.go:29 `_ = …`) → silently dropped.
- Post-purge (published retention default 720h): second append succeeds; relay delivery hits the `applied` receipt → no-op.
- Net: **only the first like of a comment ever notifies its owner.** Today, relike produces a second notification (collapsed only within the 5-min window).

**Recommended resolution (adopt-and-pin):** accept the change as intentional. The notification states one fact ("X liked your comment"); the 5-minute collapse already treats repeated likes as mergeable, and the like itself is idempotent at the domain layer (repository/comment.go:231–247, `ON CONFLICT DO NOTHING`). Document in `docs/DECISIONS.md` and pin with a test (T-RELK, §9). The exactly-once guarantee applies to **deliveries of the same logical event**, which is the requirement's stated scope.

**Alternative (rejected for this direction, documented for a follow-up):** put a per-occurrence discriminator in slot 5 — the like row's stored `CreatedAt` rendered via `idgen.RenderAnchorTime` (stable: it is a stored attribute of the occurrence, not a derivation-time clock; byte-identical across idempotent retries because `Like` is `ON CONFLICT DO NOTHING`). This preserves today's relike notification at the cost of (a) `Like` returning the row's `CreatedAt`, (b) slot 5 no longer being empty (arity/part contract change is safe — the slot exists — but the "no clock in parts" purity rule is bent), and (c) more moving parts in the pinned formula test. If product requires per-occurrence like notifications, this is a small follow-up; it is **not** folded into this direction.

## 5. Compatibility constraints

1. **No wire API change.** No HTTP/gRPC/proto surface, request shape, response shape, or error code changes. This is producer/consumer internals.
2. **`message.*` events unchanged** (fresh v7 via `newEvent`), including their fan-out behavior — including today's duplicate-on-redelivery possibility. The unwired `internal/message` tree and `internal/notification` tree are untouched (dead trees; `check-audit-swallow` tripwires remain green).
3. **Report-resolve audit fact anchor unchanged** (`EventID = reportID`, service/comment.go:400–413) — byte-identical; the existing Postgres parity tests (comment_report_anchor_integration_test.go) must stay green.
4. **Dispatcher suppression semantics unchanged**: preference/rate/block suppression still returns nil with no receipt and no row; the relay marks the event published (a suppressed event is final — same as today).
5. **Collapse behavior unchanged** (row + collapsed count in the same tx; extra row retained; 5-min window from config wiring).
6. **SSE push semantics**: pushed only when `applied`; on collapse, pushes the collapsed row (as today). Best-effort as today (a crash after commit loses the push, never the row).
7. **Webhook chain unchanged** (app/outbox.go `outbox.NewChain(dispatcher, webhook)`): the dispatcher's duplicate no-op still lets the webhook handler fire on every delivery — the webhook is a separate delivery surface with its own retry/DLQ (out of scope; note in runbook that webhook receivers may see duplicate deliveries and already must be idempotent).
8. **Webhook `id` field format change**: `outbox.Event.ID` is serialized into webhook bodies (outbox/webhook.go:30,64,96). Comment event IDs change from fresh v7 to derived v5. IDs are opaque identifiers; consumers that only carry/echo them are unaffected. Document in the webhook contract notes.
9. **Receipt key space**: comment receipts occupy `("aero-id","aero-id",<v5 anchor>)`. Disjoint from snapshot receipts (real tenants, other sources, v7/source-anchored IDs). The sentinel `"aero-id"` is reserved; never assign a tenant literally named `"aero-id"` (add a config-validation note if tenant IDs are ever configurable).
10. **Gate compatibility**: `check-anchored-event-ids.sh` R-1 scans `EventID: idgen.NewID()` (audit facts) — the outbox builder sets `ID:`, not `EventID:`, and uses `idgen.Anchor` (arity 5 ≥ 4), so no gate change. `check-file-sizes`, `check-composition-root` (new files are reached from `internal/app/outbox.go` → dispatcher → repo), `check-integration-compile` all apply.
11. **T-WIRE wiring pin**: `scripts/check-composition-root.sh` gains self-tested clauses W-1/W-2/W-3 (see §8, T-WIRE-3). `make check-composition-root` then fails deterministically, Docker-free, if prod wiring ever loses the direct `NewNotificationDispatcher(deps.notifRepo, …)` seam and silently routes comment dispatch through the dedup-free fallback.

## 6. Failure modes and mitigations

| # | Failure mode | Behavior | Mitigation / property |
|---|---|---|---|
| F-1 | Relay redelivery (handler error, claim loss, crash between effect and `MarkPublished`) | Receipt exists → `DispatchComment` returns `applied=false` → no-op, relay publishes | Exactly-once effect; the receipt is the durable gate (outlives outbox purge, unlike the PK) |
| F-2 | Crash mid-dispatch | Tx rolls back receipt + notification + counter together | Relay retry re-executes the effect; no half-state, no duplicate |
| F-3 | Concurrent deliveries of the same event (claim race, multi-instance relay) | `ON CONFLICT DO NOTHING` under the unique constraint serializes; exactly one winner | Concurrency-safe by constraint, no app-level lock |
| F-4 | Re-append of the same anchored ID (idempotent retry of `Like`, or same comment row re-persisted) | PK conflict on `outbox_events.id` (pre-purge) fails the append transaction | For `liked` (best-effort `AppendEvent`) the error is swallowed → duplicate event never exists. For `replied`/`created` the anchor derives from a fresh comment ID → conflict is unreachable (only deterministic comment IDs could trigger it — out of scope, §6.1 of the requirements doc) |
| F-5 | Outbox purge removed the row (retention 720h default) | Re-append succeeds; delivery hits the applied receipt → no-op | Receipt, not the PK, is the correctness store (AGENTS.md: "Redis and process memory … never the only correctness store"; same doctrine for purge) |
| F-6 | like → unlike → relike (same anchor) | Second occurrence's notification lost (PK conflict or receipt no-op) | **Decision D-1**: accepted + pinned by T-RELK, or the slot-5 discriminator follow-up |
| F-7 | Duplicate delivery of a *suppressed* event (prefs/rate/block) | Suppression returns nil (no receipt); relay publishes; redelivery re-evaluates suppression → same nil | No double effect; suppression decisions remain final per outbox row, as today |
| F-8 | Receipt present with status from another path (snapshot `processing`/`quarantined` under the same key) | Any existing row → no-op | Key space is disjoint by construction (sentinel tenant + anchor namespace); a collision would be a derivation bug caught by T-5.1 parity tests |
| F-9 | `DispatchComment` not implemented by a repository (fake/mock) | Dispatcher falls back to today's path (dedup-free) | Explicit fallback keeps unit tests green; production wiring always satisfies the extended interface — pinned by T-WIRE-1 (compile-time concrete-type assert), T-WIRE-2 (behavioral unit pair), T-WIRE-3 (check-composition-root wiring clauses). A regression that routes prod through the fallback fails the done gate with no Docker |
| F-10 | Clock/region skew | Not applicable to the ID: parts contain no clock/randomness | Pure derivation; `CreatedAt` is metadata only |
| F-11 | Webhook delivery of a duplicate event | Dispatcher no-ops but chain continues to webhook | Webhook is its own at-least-once surface with signed deliveries (out of scope; receivers must be idempotent — already the contract) |

## 7. Migration steps

**No schema migration is required.** The design depends on the **current** receipt schema (000033 + 000046 + 000050); environments must be at 000050 before this code ships (it is part of normal `cmd/migrate`; the done gate's `make test-integration` runs the full migration set via testcontainers).

Rollout order:
1. Land Change A (anchored producer) + Change B (receipt-gated consumer) + tests + the T-WIRE wiring-gate clauses (check-composition-root W-1..W-3 with updated fixtures) in one change set; run the done gate plus `make test-integration`.
2. **No backfill**: historical comment outbox rows keep their v7 IDs; if any are redelivered post-deploy they behave exactly as today (possible duplicate fan-out — status quo ante, bounded by the relay retry ceiling). Their notifications are already delivered or dead-lettered; no receipts are fabricated for them.
3. **No dual-write window needed**: old rows (v7 IDs) and new rows (v5 anchors) coexist in the outbox and receipts table; the dispatcher gates only `aggregate_type == "comment"` and only cares about the ID currently on the row.
4. If 000050 is ever down-migrated (reverting to `(source, event_id)` + drop tenant), comment receipts keep working (`tenant_id` column disappears; dedup key widens — no correctness loss, only a slightly wider key space). If the tenant CHECK were ever relaxed, no impact. Do not down-migrate 000050 without auditing snapshot receipts.
5. **Decision D-1** is recorded in `docs/DECISIONS.md` (or the follow-up slot-5 discriminator is cut as a separate direction) before merge.

## 8. Testable acceptance mapping

Done gate (all Docker-free): `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-composition-root && make check-integration-compile && make build`. T-5.1/T-5.2/T-REC/T-RELK/T-10.1/T-WIRE-1/2/3 run inside `make test` or the static gates — no Docker. T-5.3, T-REC-PG, T-RELK-PG are Postgres-backed extras executed by `make test-integration` (testcontainers, Docker) and kept compiling in the done gate by `make check-integration-compile`.

Docker-free baseline verified green at HEAD `b005409` during this hardening pass (run 2026-08-08): `make test`, `make lint`, `make check-file-sizes`, `make check-audit-swallow`, `make check-anchored-event-ids`, `make check-composition-root`. The new tests below are additions to those same harnesses.

| ID | Acceptance (testable) | Location / harness | Maps to |
|---|---|---|---|
| T-5.1 | Parity formula: for each of `comment.replied`, `file.commented`, `message.commented`, `comment.liked`, the event captured by the service satisfies `evt.ID == idgen.Anchor("comment.outbox", "comment", evt.AggregateID, evt.EventType, "")`, `evt.ID != uuid.Nil`, and recomputation twice yields byte-identical IDs (pure function). Mock fields: `CreateWithReplyBumpFn`/`AppendEventFn` on `mocks.CommentRepo` at `internal/service/mocks/mocks.go:309–310` (struct at 307) | `internal/service/comment_test.go` (extend capturing fakes at lines 29–75; the file is 630 lines but the 500-line gate excludes `*_test.go` — add to a `comment_events_test.go` sibling if preferred) | REQ-1, REQ-4 |
| T-5.2 | Idempotent-retry parity: two executions of the same mutation resolving to the same comment ID append **byte-identical** event IDs; both equal the formula | `internal/service/comment_test.go` | REQ-1, REQ-4 |
| T-5.3 | Duplicate-drain exactly-once effect (Postgres; Docker): append one `comment.replied` event + comment row; drain through the dispatcher twice with the same `event_id` (relay semantics: re-claim after lost finalize). Assert: exactly one `notifications` row, unread counter +1 (not +2), exactly one `event_receipts` row with `status='applied'`; second delivery returns `applied=false` and no push | New `internal/repository/notification_dispatch_integration_test.go` following `notification_integration_test.go` + `startAnchorLedger` precedent (`internal/repository/anchor_dedup_integration_test.go:25`); drives `repository.NewNotificationRepository` + `service.NewNotificationDispatcher` | REQ-3 |
| **T-REC** | **Receipt key contract — amended tenant-scoped key, unit, Docker-free**: wire the dispatcher with an extended fake (embeds `*mocks.NotificationRepo`, adds `DispatchComment` recording the `ReceiptKey`); `Handle` one `comment.liked` event; assert the recorded key is exactly `{TenantID: "aero-id", Source: "aero-id", EventID: <derived anchor>, AggregateType: "comment", AggregateID: <comment ID>}` with `TenantID != ""`; assert `repository.LocalEventReceiptTenant == "aero-id"` and `LocalEventReceiptSource == "aero-id"`. **Fails deterministically** if the code reverts to the obsolete empty-tenant contract (`tenant_id = ""`, or a `(source, event_id)`-only key — the pre-000050 `uq_event_receipt_source_event` shape) or renames the sentinels | New `internal/service/notification_dispatch_test.go` (unit; runs in `make test`) | REQ-3 as amended for 000050 |
| T-REC-PG | Receipt row + constraint (Postgres; Docker — **extra layer, not part of the done gate**): raw SQL asserts the written row has `tenant_id='aero-id'`, `source='aero-id'`, `event_id` = derived anchor, `aggregate_type='comment'`, `aggregate_id` = comment ID; a second insert under the same key is ignored by `uq_event_receipt_tenant_source_event` even when `tenant_id`/`source` differ from snapshot rows | same integration file as T-5.3; compiled by `make check-integration-compile`, executed by `make test-integration` | REQ-3 as amended for 000050 |
| T-10.1 | No fallback: every comment event path (top-level created on file, top-level on message, reply, liked) appends an event with non-zero, formula-equal ID; a regression to `idgen.NewID()`/zero ID in the comment builders fails the test. (Optional non-blocking: extend `check-anchored-event-ids.sh` with an R-1-style scan of the comment builder file — not required, the unit test is the mandatory mechanism) | `internal/service/comment_test.go` (note: there is **no** `comment_reactions_test.go`; the like-path capturing fake lives in `TestCommentService_LikeAndUnlike` at comment_test.go:221–242) | REQ-2, REQ-5 |
| **T-RELK** | **Decision D-1 pin — full like → unlike → relike sequence through the relay, unit, Docker-free**: (1) drive the real `commentService.Like`/`Unlike`/`Like` with `mocks.CommentRepo` (`GetFn`/`LikeFn`/`UnlikeFn`; `AppendEventFn` appends into an in-memory `outbox.Repository` fake that rejects a duplicate `ID` — models the `outbox_events.id` PK, mirroring `comment_reactions.go`'s best-effort swallow). Assert evt1.ID == evt2.ID == `idgen.Anchor("comment.outbox", "comment", id, "comment.liked", "")` and exactly one outbox row survives. (2) Drain through the real relay: `outbox.NewRelay(outboxFake, service.NewNotificationDispatcher(extendedNotifFake), 0, nil)` + `relay.Start(2*time.Millisecond)`; deadline-bounded poll until published, then `stop()`. (3) Assert exactly **one** `DispatchComment` with applied=true, exactly one receipt key recorded, zero further effects, no second SSE push — the relike notification is suppressed. Post-purge variant: fresh outbox row carrying the same derived ID (the first row is gone — the 720h retention purge) with the receipt pre-seeded in the extended fake → drain through the same relay → the event is marked published with zero effects (the durable-gate path of D-1, F-5) | New `internal/service/comment_dispatch_relay_test.go` (package `service_test`; the relay unit harness `fakeRepo`/`drain` in `internal/outbox/relay_test.go` is package-private and importing `service` from `package outbox` is an import cycle, so the fake + `relay.Start` polling live here; polling precedent: `TestBuildOutboxCleanerWiringPauseFlag` in `internal/app/outbox_wiring_test.go`) | D-1 |
| T-RELK-PG | D-1 through the real repository (Postgres; Docker — extra layer): like → unlike → relike via the service + real `CommentRepository`; two drains with the identical derived event id; assert exactly one `notifications` row and exactly one `event_receipts` row | `notification_dispatch_integration_test.go` | D-1 |
| **T-WIRE-1** | Compile-time, Docker-free: `var _ NotificationDispatchRepository = (*notificationRepo)(nil)` in the new `internal/repository/notification_dispatch.go` — the build fails if the concrete repo loses `DispatchComment`. (A `var _ … = repository.NewNotificationRepository(nil)` in `internal/service` **cannot compile**: the constructor's static return type is the plain `NotificationRepository` interface, which does not satisfy the extended interface; the concrete-type assert is the correct pin — and `assertCompositionRoot` does not exist in this tree) | `internal/repository/notification_dispatch.go`; compiled by every gate step | F-9 |
| **T-WIRE-2** | Behavioral pair, unit, Docker-free: (a) extended fake → `Handle(comment event)` routes through `DispatchComment` and returns nil with no push when applied=false; (b) `mocks.NotificationRepo` (non-extended) → legacy dedup-free path still runs — proving the fallback is reachable only from unit fakes | `internal/service/notification_dispatch_test.go` (same file as T-REC) | F-9 |
| **T-WIRE-3** | Wiring pin, Docker-free, done gate: extend `scripts/check-composition-root.sh` (existing self-test doctrine) with W-1: `internal/app/outbox.go` contains exactly one `service.NewNotificationDispatcher(deps.notifRepo,` (repo passed directly — a decorator/wrapper seam fails the gate); W-2: `internal/app/new.go` contains exactly one `notifRepo := repository.NewNotificationRepository(` (the real constructor product, new.go:145); W-3: `internal/service/notification.go` still contains `.(repository.NotificationDispatchRepository)` (deleting the receipt branch fails the gate). Fixtures in `scripts/testdata/composition-root/` gain positive (violated) and negative (clean) cases per clause | `scripts/check-composition-root.sh` + `scripts/testdata/composition-root/`; runs in `make check-composition-root` | F-9 |
| Gate | `make check-anchored-event-ids` stays green with the new `idgen.Anchor` call (arity 5, in `internal/service/`); `make check-file-sizes` green with the two new sibling files; `make check-composition-root` green with W-1..W-3 | Makefile | REQ-1/REQ-2 constraints |

## 9. Amended requirements (deltas to the requirements doc)

- **REQ-3 (amended):** the durable receipt key is `uq_event_receipt_tenant_source_event UNIQUE (tenant_id, source, event_id)` (migration 000050:6–7), with `tenant_id` the reserved non-empty sentinel `"aero-id"` (000050:1–4 requires non-empty; the doc's "tenant_id empty" and "key excludes tenant" are obsolete). `source = "aero-id"`. The minimal insert-or-ignore and the no-reuse of `claimEventReceipt`/`verifyEventAccountTenant` clauses stand.
- **REQ-3 (amplified):** "atomically with the notification effect" includes the collapse decision and counter (not just the row insert), and the SSE push happens after commit, only when applied.
- **REQ-1 (clarified):** the `outbox_events.id` PK backstop is bounded by published retention (default 720h); the receipt is the durable gate. A future mutation discriminator goes in slot 5 only.
- **§6.2 (corrected):** the only collision is *not* exclusively "the intended one" — see D-1 for `comment.liked` repeat occurrences.
- **§1 (corrected):** `internal/comment` was deleted in commit `3a2bf38`; `b005409` deleted `internal/block`. Same 2026-08-08 quarantine batch; no impact on this work.

## 10. Non-goals (unchanged from the requirements doc §5)

No change to `message.*` event IDs or the unwired trees; no change to the report-resolve anchor; no new tables/migrations; no new anchor namespace or raw `uuid.NewSHA1`; no change to collapse/rate-limit/preference/block behavior; no request-level idempotency changes; no deterministic comment IDs (aggregate-level idempotency stays out of scope, requirements doc §6.1).
