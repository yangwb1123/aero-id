# Design — internal/outbox: claim-based dispatch

| | |
|---|---|
| Module | `internal/outbox` |
| Input spec | `docs/requirements/2026-08-06-internal-outbox-claim-based-dispatch.md` (REQ-1..REQ-9, AC-1..AC-3) |
| Date | 2026-08-06 |
| Status | **Implemented** |
| Revised | 2026-08-06 — H1 (claim lifetime) resolved via token-gated renewal (§1.2/D12); H5 (backoff scan) bounded via Go-side exclusion (§1.3/D4); H4 clock-skew text corrected (F6); H2 index rationale, H6 rollout order, and acceptance-mapping fixes F1–F3 folded in. Resolves `2026-08-06-internal-outbox-claim-based-dispatch.stress-test.md` H1/H4/H5 (+H2/H3/H6) |

## 0. Evidence verification status

All five cited evidence items from the requirements spec were re-checked against
the working tree and are **confirmed**:

| # | Claim | Verification |
|---|---|---|
| E1 | `repository.go:78-91` FetchUnpublished: `FOR UPDATE SKIP LOCKED` (line 81-82) on a bare `r.db` chain, no `Transaction` wrapper → lock dies at statement end | ✅ Confirmed. No `Transaction` usage exists in `repository.go`; the statement runs in autocommit |
| E2 | `relay.go:95-168` drain: fetch (97) → `handler.Handle` (128) → `MarkPublished` (163); zero transaction usage | ✅ Confirmed. The fetch→finalize gap is the duplicate-dispatch window |
| E3 | `repository.go:76-77` comment: SKIP LOCKED "preventing duplicate processing in multi-instance deployments" | ✅ Confirmed verbatim; contradicted by E1/E2 |
| E4 | `relay_test.go:50-98` fakeRepo: in-memory copy; `MarkFailed` (62-68) drops state; no two-relay concurrency test | ✅ Confirmed. All five relay tests drive a single relay |
| E5 | Migrations `000008`/`000018` have no claim/lease column; next slot `000058`; wiring `internal/app/outbox.go:63-68` (NewRelay 63, 5s poll 64, Start 68) | ✅ Confirmed. `000008` defines `id, aggregate_type, aggregate_id, event_type, payload, created_at, published_at`; `000018` adds `retry_count, last_retry_at, dead_letter` |

Supporting facts verified for this design:

- The unconditional lost-update defect is `repository.go:119-120`
  (`Where("id = ?")` inside `MarkFailed`, `repository.go:111-125`); `MarkPublished`
  (`repository.go:96-109`) is equally unconditional (`WHERE id IN ?`).
- Only two implementors of `outbox.Repository` exist: `GormRepository` and the
  test `fakeRepo` (`relay_test.go:50-98`). `Repository` interface is
  `outbox.go:36-48`. Change surface is contained.
- `NewRelay` is called only from `internal/app/outbox.go:63` and `relay_test.go`
  (5 sites); `NewGormRepository` from `internal/app/new.go:151`,
  `repository_integration_test.go:23`, and three repository tests. All call
  sites pass `(db)` positionally.
- Integration tests run against `postgres:16-alpine` (testpg); `gen_random_uuid()`
  is already used in migration `000014` — available without extensions.
- Handler timing for lease sizing: `NotificationDispatcher` (DB writes) chained
  with optional `WebhookHandler` (HTTP timeout default 10s in app wiring,
  `5s` in `configs/config.yaml:144`). GORM v1.25.11. Sizing rule amended in
  §1.2/D12 (renewal): the webhook timeout bounds a *single* handler call, not
  the lease.

## 1. Design overview

Replace "row lock as correctness" with **durable claims**: one atomic SQL
statement claims a batch (writes `claim_token` + `claimed_at`), the relay holds
the claim across the handler window, and finalization (`MarkPublished` /
`MarkFailed`) is conditional on the caller still holding an unexpired claim.
Recovery from a crash between claim and finalize is lease expiry: `claimed_at <
now() - lease` makes a row re-claimable, and re-claiming mints a fresh token
that invalidates every prior holder.

```text
ClaimBatch (1 statement: UPDATE ... WHERE id IN (SELECT ... FOR UPDATE SKIP LOCKED) RETURNING)
   │  rows now carry durable claim_token/claimed_at
   ▼
relay: backoff skip? ──(Go backstop, primary)──> Release(claim)   [REQ-6, H5]
   │
   ├─ every renewEvery (10s) during a long drain: RenewClaims(pending)   [H1/D12]
   ▼
handler.Handle(claimedEvent.Event)                          [REQ-2]
   │ success                                          │ error
   ▼                                                  ▼
MarkPublished(claims)              MarkFailed(claim, retryCount, maxRetries, at)
   │ conditional on (id, token, unexpired)            │ conditional on (id, token, unexpired)
   │ ErrClaimLost if stale                             │ SQL-side retry_count = retry_count + 1
   ▼                                                  ▼
published_at = now()                          retry_count++, last_retry_at, maybe dead_letter
```

### 1.1 Design decisions (with rationale)

| # | Decision | Rationale |
|---|---|---|
| D1 | Claim acquisition is one `UPDATE ... RETURNING` statement; row locks are still `SKIP LOCKED` but are no longer the correctness mechanism | Single-statement atomicity is sufficient (REQ-1); the claim columns survive the statement end, the locks do not |
| D2 | Claim token is a UUID minted by `gen_random_uuid()` in SQL, per row | Tokens must be unforgeable/unique per claim; no Go-side coordination; PG16 supports it (already used in 000014) |
| D3 | Lease comparisons use the DB clock (`now()`) on both claim and finalize | REQ-5: app-clock skew cannot extend/shrink leases |
| D4 | Backoff eligibility stays in the relay's **Go backstop** (`relay.go:121-125`), now the *primary* exclusion, with a prompt per-event conditional `Release` for skipped claims; the SQL backoff predicate of the earlier draft is **removed** | REQ-6 explicitly sanctions the alternative: "the relay releases their claims immediately after the skip decision". A SQL backoff predicate forces the claim scan to walk every backoff-excluded row before the `limit`-th passing row — O(backlog) per poll vs today's fixed 100-row scan (H5, §1.3). The Go path is bounded (≤ limit + 1 small statements per poll in the all-backoff-head case; 0 in normal ops), removes the SQL/Go formula duplication (and the H3 `POWER(2, …)` overflow hazard), and keeps the backoff formula in one place |
| D5 | **Deviation from REQ-7's index proposal:** the partial index predicate stays time-independent (`WHERE published_at IS NULL AND NOT dead_letter`); the lease term is a residual query filter, not an index predicate; backoff has no SQL term at all (D4) | Postgres **rejects** `now()`-relative index predicates outright ("functions in index predicate must be marked IMMUTABLE", verified on PG 16/17 — §4): REQ-7's predicate is uncreatable, so the deviation is mandatory, not a preference. Secondary: even if creatable, write-time predicate evaluation would eject claimed rows and never re-admit them at lease expiry (no write occurs). The lease residual is cheap because the claimed-but-unexpired set is bounded by batch size × instances (100·N); the backoff residual was removed because it is bounded by *nothing* (H5, §1.3). Migration 000058 therefore only adds columns; the index is untouched (see §4) |
| D6 | New repository method named `ClaimBatch` (renamed from `FetchUnpublished`) returning `[]*ClaimedEvent` | The operation now mutates state; a read-only-sounding name invites the same misunderstanding the E3 comment created. Interface has exactly two implementors, both in-repo |
| D7 | `MarkPublished(ctx, claims []Claim)` (batch) and `MarkFailed(ctx, c Claim, ...)` (single) return a documented sentinel `ErrClaimLost` when any claim is missing/foreign/expired; valid subset still applies | Distinguishable result per REQ-3; partial-batch staleness is normal after lease expiry, not an infra failure |
| D8 | `MarkFailed` writes `retry_count = retry_count + 1` in SQL (relative write); the caller's `retryCount` argument is kept for logging parity | REQ-4. Claim exclusivity already makes the snapshot authoritative, but a relative write is self-healing and costs nothing |
| D9 | `eventRow` claim fields are pointers (`*uuid.UUID`, `*time.Time`) | GORM `Create` with zero-value `uuid.UUID`/`time.Time` would insert `00000000-...`/`0001-01-01` instead of NULL, corrupting new rows' claim state. Pointers map zero → NULL in `Append` |
| D10 | `MetricsSink` interface unchanged; claim loss is logged at warn | Extending `MetricsSink` would force changes in `metrics.Registry` and the test sink for an edge signal; observable via logs today, metric can be added later without breaking |
| D11 | Lease is a `GormRepository` option (`WithClaimLease`), default `30s`; **no new config key** | REQ-9: no config surface change; tests need an injectable short lease (AC-3). `configs/config.yaml:138-146` untouched. Sizing rule amended in D12/§1.2: the lease no longer needs to cover a whole batch's handler time |
| D12 | **Token-gated claim renewal** (H1 fix): while a drain holds pending claims, the relay calls a new conditional `RenewClaims` every `renewEvery = 10s` (default, L/3; relay field), extending `claimed_at = now()` under the same conjunction as finalize — `(id, token)` pair, unexpired, unpublished | A batch claim lives for the whole sequential handler window (B × t — up to 450s with the 5s webhook chain) while the lease is 30s (§1.2). Renewal keeps the claim alive for arbitrarily long drains, restores exactly-once-within-window across the full handling span, and is fail-safe: it can only extend claims the caller still holds and that remain unexpired — a mispaired cadence (R ≥ L) or a superseded token simply no-ops (at-least-once preserved). Sizing rule: `lease > renewEvery + longest single handler call + finalize margin` — defaults 30s > 10s + 10s + margin ✓. No schema/config change |

### 1.2 Claim-lifetime problem (H1): quantification and fix

`ClaimBatch` stamps `claimed_at = now()` on all rows in **one statement**; the
relay then handles the batch sequentially and finalizes — `MarkFailed` per
event, `MarkPublished` once after the loop. The claim lifetime is therefore
the whole batch's sequential handler time `T_claim = B × t` (B = 100), not one
handler call; the original sizing rule ("lease must exceed handler p95", old
F1) applied a per-event bound to a batch-lifetime claim.

With the supported defaults (B = 100, lease L = 30s, webhook chain timeout 5s,
poll 5s) and a slow-but-successful endpoint at t = 4.5s:

| Quantity | Value |
|---|---|
| Claim lifetime T_claim = B × t | **450s = 15 × L** |
| Events whose claim survives to finalize per drain | ⌊L/t⌋ = **6** (per-event finalize); **0** with the design's batch-at-end `MarkPublished` — every row's `claimed_at` is ~450s old at the single finalize statement |
| Duplicate deliveries per event | **Unbounded** (batch finalize: nothing ever finalizes; every poll re-claims and re-handles all 100) — or worst ≈ ⌈B/⌊L/t⌋⌉ ≈ **17×** (avg ≈ 9×) with per-event finalize |
| Convergence | **Never** (batch finalize) — or ≈ (B/a)·B·t ≈ 17 drains ≈ **2.1h** with per-event finalize |
| Slow-but-failing endpoint | Only the first ⌊L/t⌋ = 6 `MarkFailed` calls land (call i is stale once i·t > L) → the tail's `retry_count` never advances → backoff(1) = 5s never starts and the dead-letter ceiling is never reached: **the backoff governor and DLQ are defeated exactly during handler storms** |
| Exactly-once-within-window | Holds only for the first ⌊L/t⌋ events per drain. The tail's claims expire mid-handling (age i·t > L) and are re-claimable by another instance's next poll (≤ 5s later) **while the original handler call is still running** (up to 450s) → even *concurrent* duplicate delivery of the same event — the exact defect being fixed |

(The stress-test audit's "~30×" estimate overstates the per-event-finalize
case; with the design's batch-at-end `MarkPublished` the true worst case is
strictly worse: zero admission, no convergence.)

**Fix (D12): token-gated claim renewal.** While a drain holds pending claims,
the relay calls `RenewClaims` every `renewEvery = 10s` (L/3), extending
`claimed_at = now()` under the same conjunction as finalize. Consequences:

- **Claim lifetime** becomes "as long as the holder is alive": a claim cannot
expire mid-drain unless renewal fails (crash, DB outage) — the documented
at-least-once recovery path. Recovery latency after a crash stays ≤ L = 30s
(the claim is at most one cadence old when the holder dies; renewal stops at
the crash).
- **Sizing rule** (amends F1/D11): `lease > renewEvery + longest single
handler call + finalize margin` — defaults 30s > 10s + 10s + margin
(app wiring webhook timeout 10s; config default 5s). Single calls are bounded
by the webhook timeout; the batch's total handler time is unbounded but
covered by renewal.
- **Why not "lease > batch × p99"**: L would need ≥ 500–600s (crash-recovery
latency of minutes), and it is still wrong: with per-event p99 and B = 100,
P(any event in a batch exceeds p99) ≈ 1 − 0.99¹⁰⁰ ≈ 63% — batch × p99
under-sizes with near certainty, because the batch *maximum*, not the
per-event quantile, is the relevant statistic.
- **Why not sub-batch claiming**: it bounds the claim lifetime without new
SQL, but the sub-batch size must be derived from a per-event latency estimate
(new config or adaptive logic; the relay does not know the lease), and an
over-optimistic estimate reproduces H1. Renewal needs no estimate, keeps the
single-claim-statement-per-drain shape, and is fail-safe in the wrong
direction (a renewal that cannot apply simply no-ops).
- **Impact on REQ-8/9**: `Repository` gains `RenewClaims` (both implementors,
§2.2); no handler/chain/metrics/config changes; fast drains (≤ 10s) never
renew — the single-instance fast path is unchanged. Exactly-once-within-window
now spans the full handling span: a duplicate requires a lease expiry
(liveness failure), never a long handler.
- **MarkFailed interplay (F4)**: `MarkFailed` clears the claim in the same
conditional UPDATE (§3.3) — failed rows become claimable at their backoff due
time (5/10/20/40s…, REQ-9's cadence) instead of at lease expiry, and the relay
drops failed claims from the renewal set; a stale renewal targeting a cleared
claim no-ops on the token check. `MarkPublished` clears claims too (H7, §3.2),
keeping published rows claim-free and renewal a no-op after finalize.

### 1.3 Backoff scan amplification (H5): quantification and bound

**Regression.** Today's fetch scans exactly `limit` = 100 index entries (plus
≤ 100 heap fetches for the Go backoff check): a fixed cost, independent of
backlog size. The earlier draft's SQL backoff predicate moved the skip into
the claim query, whose scan must walk **every backoff-excluded row** to reach
the `limit`-th passing row — O(backlog) per poll. A post-outage cohort of 1M
events at `retry_count ≥ 11` (1h backoff) costs ~1M index entries + ~1M heap
fetches (`claimed_at`/`last_retry_at`/`retry_count` are not index columns) per
poll per instance, for zero deliveries: at poll 5s × 2 instances that is
~400k heap fetches/s vs ~2.4k rows/min today — four orders of magnitude,
unbounded, and silent (drain summary unchanged).

**Bound (D4 amendment).** Backoff exclusion returns to the relay's Go
backstop — the alternative REQ-6 explicitly sanctions — with a prompt
per-event `Release` for skipped claims (per-event, not batched-at-end:
skipped rows must stay immediately claimable, REQ-6). `ClaimBatch` keeps only
the lease residual, whose scan is bounded: the claimed-but-unexpired head is
at most batch × instances = 100·N rows, so the index walk is ≤ limit + 100·N,
backlog-independent:

| Per poll, per instance | Today | Earlier-draft SQL backoff | Bound applied |
|---|---|---|---|
| Index rows walked | 100 (fixed) | O(backlog), unbounded | ≤ 100 + 100·N |
| Heap fetches | ≤ 100 | O(backlog) | ≤ 100 + 100·N |
| Row writes | 0 | 0 | ≤ 1 claim + ≤ limit per-event releases in the all-backoff-head case; 0 in normal ops |

The churn the SQL predicate bought is bounded and self-limiting (skipped rows
sit at the batch head, so the backstop claims-and-releases them within the
first loop iterations — claims held for milliseconds; released rows stay
claimable); the unbounded scan it traded for is not. Side benefits: the
SQL/Go formula duplication (and its parity test) disappears, and the H3
`POWER(2, …)` overflow hazard vanishes with it. Monitoring: drain duration
(already logged) is the scan-bound signal; `TestClaimScanBoundedUnderBacklog`
(§8) locks the structural bound.

## 2. API changes

All changes are in `internal/outbox`. No handler, webhook, chain, metrics, or
app-config surface changes.

### 2.1 New types (`outbox.go`)

```go
// Claim identifies one event currently held by a relay instance. Finalization
// (MarkPublished/MarkFailed) and Release apply only while the claim is held
// and unexpired; Token is minted per claim and is never reusable.
type Claim struct {
    EventID uuid.UUID
    Token   uuid.UUID
}

// ClaimedEvent is an Event plus the durable claim under which it was fetched.
// The relay passes e.Event to the Handler and keeps the Claim for finalization.
type ClaimedEvent struct {
    Event     *Event
    Claim     Claim
    ClaimedAt time.Time // DB clock at claim time (observability)
}
```

### 2.2 `Repository` interface (`outbox.go:36-48`)

```go
type Repository interface {
    // Append writes an event using the caller-supplied transaction ... (unchanged)

    // ClaimBatch atomically claims up to limit unpublished, non-dead-letter
    // events (backoff eligibility is applied by the relay after claiming, D4),
    // returning them oldest-first with a fresh durable claim on each row. Rows
    // with an unexpired claim are invisible to all other ClaimBatch calls.
    // Claims expire after the repository's lease; expired rows are
    // re-claimable with a new token.
    ClaimBatch(ctx context.Context, limit int) ([]*ClaimedEvent, error)

    // MarkPublished stamps published_at only on claims the caller still holds
    // (matching token, unexpired). Valid claims are applied; if any claim is
    // missing, foreign, or expired the returned error wraps ErrClaimLost.
    MarkPublished(ctx context.Context, claims []Claim) error

    // MarkFailed increments retry_count and sets last_retry_at only while the
    // caller holds the claim; when the new retry_count reaches maxRetries the
    // event becomes a dead letter. Stale claims are a no-op returning
    // ErrClaimLost. The write is relative (retry_count = retry_count + 1);
    // retryCount is used for relay logging and must equal the current
    // retry_count + 1 when the claim is held.
    MarkFailed(ctx context.Context, c Claim, retryCount, maxRetries int, at time.Time) error

    // Release clears the claim on rows the caller holds without changing
    // retry state. Used for events skipped by the relay's Go backoff check
    // (the primary backoff exclusion, D4). Stale claims are a no-op.
    Release(ctx context.Context, claims []Claim) error

    // RenewClaims extends claimed_at to now() on claims the caller still
    // holds and that remain unexpired, unpublished, and non-dead-letter. The
    // conjunction is the same as finalization — (id, token) pair + lease — so
    // renewal can never extend a superseded or expired claim. Stale claims
    // are silently skipped (finalization reports them); DB errors propagate.
    // The relay uses this to keep batch claims alive across long handler
    // chains (D12).
    RenewClaims(ctx context.Context, claims []Claim) error

    // PurgeDeadLetters ... (unchanged)
}
```

New exported sentinel in `outbox.go`:

```go
// ErrClaimLost is returned by MarkPublished/MarkFailed when the caller no
// longer holds an unexpired claim for an event (lost to lease expiry, a newer
// re-claim, or never held). It is a normal at-least-once condition, not an
// infrastructure error: the event will be re-claimed and re-delivered.
var ErrClaimLost = errors.New("outbox: claim lost or expired")
```

### 2.3 `GormRepository` constructor (`repository.go` / new `claim.go`)

```go
const defaultClaimLease = 30 * time.Second

type Option func(*GormRepository)

// WithClaimLease overrides the claim lease duration (default 30s). The lease
// must exceed the relay's renewal cadence plus the longest single handler
// call plus finalize margin (D12: default 30s > 10s + 10s + margin);
// non-positive leases are rejected (d <= 0 → default).
func WithClaimLease(d time.Duration) Option

func NewGormRepository(db *gorm.DB, opts ...Option) *GormRepository
```

`NewGormRepository(db)` call sites keep compiling — variadic options are
backward compatible. All six call sites (`internal/app/new.go:151`,
`repository_integration_test.go:23`, `internal/repository/{message,comment,
reaction_audit_integration,search_integration}_test.go`) are unchanged.

### 2.4 `Relay` (internal only; `NewRelay` signature unchanged)

- `drain` claims a batch, then per `ClaimedEvent`:
  1. **Backoff skip check** (the primary exclusion, D4/H5; same formula as
     today, `relay.go:121-125`): if inside the backoff window →
     `repo.Release(ctx, []Claim{ce.Claim})`, log debug, continue. Skipped rows
     sit at the batch head (oldest-first), so they are claimed and released
     within the first loop iterations — bounded churn, no lease-induced
     invisibility (§1.3).
  2. **Renewal** (D12): if `now().Sub(lastRenew) >= r.renewEvery` (default
     10s) and pending claims exist → `repo.RenewClaims(ctx, pending)`; DB
     error → warn, continue (lease expiry is the at-least-once fallback).
     Runs at the top of each iteration so the claim is fresh when the next
     handler call starts. `pending` holds every claim from this drain except
     those already `MarkFailed` (whose claims were cleared, §3.3).
  3. `handler.Handle(ctx, ce.Event)`.
     - error → `repo.MarkFailed(ctx, ce.Claim, e.RetryCount+1, r.maxRetries, now)`;
       `errors.Is(err, ErrClaimLost)` → warn "claim lost; event will be
       re-claimed after lease expiry" (no failure metric — the event is not
       lost, only this attempt's write). The failed claim leaves `pending`.
     - success → append `ce.Claim` to the published batch.
  4. After the loop: `repo.MarkPublished(ctx, publishedClaims)` (also clears
     the claims of published rows, §3.2).
     - DB error → warn, return 0 (today's behavior, `relay.go:163-167`).
     - `ErrClaimLost` (some or all claims stale) → warn with count; return
       `len(publishedClaims)` (the events were handled; stale ones will be
       re-claimed).
- Ordering, batching, drain summary log, metrics, and drain-on-stop are
  unchanged (REQ-9).

### 2.5 `eventRow` (`repository.go`)

```go
type eventRow struct {
    // ...existing fields...
    ClaimToken *uuid.UUID `gorm:"column:claim_token"` // nil = unclaimed
    ClaimedAt  *time.Time `gorm:"column:claimed_at"`  // nil = unclaimed
}
```

`Append` unchanged (zero pointer fields → NULL, D9). `rowToEvent` untouched
(claim fields travel via `ClaimedEvent`, not `Event`).

## 3. Claim SQL (exact statements)

Executed via `r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows)` into
`[]eventRow` (GORM `Raw` + `Scan` reads `RETURNING` result rows).

### 3.1 ClaimBatch — one atomic statement

```sql
UPDATE outbox_events
SET claim_token = gen_random_uuid(),
    claimed_at  = now()
WHERE id IN (
    SELECT id
    FROM outbox_events
    WHERE published_at IS NULL
      AND NOT dead_letter
      AND (claimed_at IS NULL OR claimed_at < now() - ?::interval)                 -- lease (only residual filter; backoff is Go-side, D4)
    ORDER BY created_at ASC, id ASC
    LIMIT ?
    FOR UPDATE SKIP LOCKED
)
RETURNING id, claim_token, claimed_at,
          aggregate_type, aggregate_id, event_type, payload,
          created_at, published_at, retry_count, last_retry_at, dead_letter
```

- Args: `leaseInterval` (e.g. `30s` as a `time.Duration` formatted via
  `fmt.Sprintf("%s", d)` → `'30s'` interval literal), `limit`.
- No backoff term (D4/H5): in-backoff rows are claimed like any other and
  released by the relay's Go backstop within the same drain pass; the scan is
  bounded by `limit + 100·N` (§1.3).
- Properties: one statement ⇒ atomic claim; the inner `FOR UPDATE` locks rows
  for the statement, `SKIP LOCKED` keeps concurrent claimers disjoint without
  blocking; the claim columns are durable after commit. Ordering/limit match
  today's `Order("created_at ASC, id ASC")` / `Limit(limit)` (REQ-9).

### 3.2 MarkPublished — conditional on held, unexpired claim

```sql
UPDATE outbox_events o
SET published_at  = now(),
    claim_token   = NULL,
    claimed_at    = NULL
FROM (VALUES (?::uuid, ?::uuid), (?::uuid, ?::uuid), ...) AS c(id, token)   -- one pair per claim
WHERE o.id = c.id
  AND o.claim_token = c.token
  AND o.claimed_at  >= now() - ?::interval
  AND o.published_at IS NULL
```

- Executed with `db.Exec`; if `RowsAffected < len(claims)` → return
  `fmt.Errorf("mark published: %w (%d of %d claims stale)", ErrClaimLost, lost, len(claims))`.
- The `(id, token)` pair matching means a token collision between two rows can
  never cross-apply (both conditions must match the same row).
- Claim columns are cleared in the same statement (H7): published rows carry
  no dangling claim, a later renewal (D12) no-ops on the token check, and the
  token check running in the same statement makes the clear race-free.

### 3.3 MarkFailed — relative write, conditional on held claim

```sql
UPDATE outbox_events
SET retry_count   = retry_count + 1,
    last_retry_at = ?,
    dead_letter   = (retry_count + 1 >= ?),
    claim_token   = NULL,
    claimed_at    = NULL
WHERE id = ?
  AND claim_token = ?
  AND claimed_at  >= now() - ?::interval
```

- Args: `at` (as today), `maxRetries`, `c.EventID`, `c.Token`, lease.
- `RowsAffected == 0` → `fmt.Errorf("mark failed: %w", ErrClaimLost)`.
- Semantics identical to today's Go branch for a live claim:
  `retry_count+1`, `last_retry_at = at`, `dead_letter = retry_count+1 >= maxRetries`
  (REQ-4: two stale holders can no longer both write `retry_count = 1` — only
  the current holder writes, and the write is relative).
- **Claim-clear (F4, H1 interplay)**: the same conditional UPDATE clears
  `claim_token`/`claimed_at`. A failed row is then claimable at its backoff
  due time — the next poll claims it and the Go backstop (D4) releases it —
  instead of being claim-invisible until lease expiry. This keeps REQ-9's
  retry cadence (5/10/20/40s…) and the dead-letter ceiling intact during
  handler storms: without it, `backoff(1) = 5s` would silently become "lease
  (30s)" for the first three retries. The relay also drops failed claims from
  its renewal set; a stale renewal no-ops on the token check.

### 3.4 Release — conditional clear (REQ-6 backstop)

```sql
UPDATE outbox_events
SET claim_token = NULL,
    claimed_at  = NULL
WHERE id = ?
  AND claim_token = ?
```

- **No lease term**: if the claim expired and was re-claimed, the token no
  longer matches (no-op); if it expired and nobody re-claimed, clearing is
  correct — the event is being skipped and must stay immediately claimable.
- Per-event and prompt: skipped rows sit at the batch head (oldest-first),
  so the backstop claims-and-releases them within the first loop iterations,
  holding each claim for milliseconds — REQ-6's "immediately claimable".
  (A batched-at-end release would hold skipped claims for the whole drain;
  it is not used.) `Release` accepts `[]Claim` so callers may group when
  convenient; the all-backoff-head case costs ≤ limit small releases per
  poll — bounded (§1.3). DB error is logged by the relay; the lease will
  expire the claim anyway.

### 3.5 RenewClaims — token-gated extension (H1/D12)

```sql
UPDATE outbox_events o
SET claimed_at = now()
FROM (VALUES (?::uuid, ?::uuid), (?::uuid, ?::uuid), ...) AS c(id, token)   -- one pair per claim
WHERE o.id = c.id
  AND o.claim_token = c.token
  AND o.claimed_at  >= now() - ?::interval
  AND o.published_at IS NULL
  AND NOT o.dead_letter
```

- Args: claim pairs, lease.
- Same conjunction as finalization — `(id, token)` pair, unexpired,
  unpublished, non-dead-letter — so a renewal can never extend a claim the
  caller does not hold, a superseded token, or an expired claim; it only
  moves `claimed_at` forward for claims the caller still holds. Fail-safe by
  construction: with `renewEvery ≥ lease` renewals simply no-op and behavior
  degrades to the pre-renewal design (at-least-once, `ErrClaimLost` warns).
- Stale claims (RowsAffected < len) are **not** an error: finalization reports
  them; renewal is best-effort liveness. DB errors propagate and are logged
  as a warn by the relay (F12).
- Called by the relay at the top of the per-event loop when
  `now() − lastRenew ≥ renewEvery` and pending claims exist (§2.4). With
  defaults (cadence 10s, lease 30s) a claim is at most ~10s old at any
  finalize/renewal, so the lease cannot expire during a drain whose holder is
  alive; after a crash, recovery latency is ≤ L = 30s (the claim is at most
  one cadence old when the holder dies).

## 4. Index strategy (deviation from REQ-7, D5)

`idx_outbox_unpublished` **is not modified**. Its predicate stays
`WHERE published_at IS NULL AND NOT dead_letter` (as recreated by
`000018`). The lease term lives only in the query; the backoff term is
Go-side (D4/H5).

Why not adopt REQ-7's suggested predicate
`... AND (claimed_at IS NULL OR claimed_at < now() - interval ...)`:

1. **Postgres refuses to create it**: partial-index predicates must be
   IMMUTABLE, and `now()` is STABLE — verified on PG 16.14/17.10:
   `ERROR: functions in index predicate must be marked IMMUTABLE`. The
   deviation is not a judgment call: REQ-7's predicate is uncreatable on any
   supported Postgres, which is the decisive reason (stronger than the
   original D5 rationale).
2. Even if creatable, Postgres evaluates a partial-index predicate **at row
   write time**: the claim statement's `claimed_at = now()` would flip the
   predicate false and eject the row; lease expiry happens without any write,
   so the row would never be re-admitted — invisible to index scans until an
   unrelated write rewrites it.
3. Cost of the residual-filter approach is bounded: at any instant the
   claimed-but-unexpired set is at most `batch (100) × active instances`, so
   a `(created_at, id)` index scan skips at most `100·N` claimed rows before
   finding `limit` passing rows — O(100 + 100·N), backlog-independent (§1.3).
   (The draft's "backoff rows are bounded the same way" claim was false —
   backoff rows *are* the backlog — which is why the backoff term moved to
   the relay's Go backstop, D4/H5.)

REQ-7's schema requirement is otherwise honored: columns added in `000058`,
`.down.sql` restores prior state, `Append` starts rows unclaimed.

## 5. Compatibility constraints

| Constraint | Consequence |
|---|---|
| `NewRelay` signature unchanged | `internal/app/outbox.go:63` and all relay tests compile unchanged |
| `NewGormRepository` gains variadic options only | All six existing call sites unchanged |
| `Repository` interface is breaking (rename + new method + signature changes) | Only `GormRepository` + `fakeRepo` implement it; both in-repo, updated in the same change (REQ-8) |
| `Handler`, `Chain`, `WebhookHandler`, `Event` unchanged | Dispatcher/webhook code and payloads untouched; `ClaimedEvent.Event` is what handlers already receive |
| `MetricsSink` unchanged | `metrics.Registry` and `recordingSink` untouched |
| Migration `000058` is additive and reversible | No edit to applied migrations `000008`/`000018`; `000058.down.sql` restores the exact pre-migration schema |
| At-least-once preserved; exactly-once only within a claim window | The window spans claim → finalize, extended by token-gated renewal for arbitrarily long drains (D12) — a duplicate requires a lease expiry (holder crash/DB outage), never a long handler. Lease recovery re-delivers after crash (AC-3 is the intended proof); handlers must stay idempotent — no stronger guarantee is claimed (REQ-8) |
| Renewal pairing (D12) | No schema/config change. Defaults pair: `renewEvery` 10s (relay) with lease 30s (repository), satisfying `lease > renewEvery + single-call bound + margin`. A mispairing (cadence ≥ lease) is fail-safe: renewals no-op and behavior degrades to the pre-renewal design (at-least-once, `ErrClaimLost` warns) |
| Mixed-version rollout window | **The binding constraint is migration first, binaries second** (stress-test audit H6). New binaries against the pre-migration schema fail every `ClaimBatch` (`column claim_token does not exist`, SQLSTATE 42703) → total delivery stall — an availability regression (§5.1.2). The reverse overlap — old binaries against the post-migration schema — is **harmless and verified**: old code ignores the new columns, its unconditional finalizes produce only status-quo-level duplicates and transient retry skew that converges, and at-least-once holds in every traced interleaving (stress-test audit §2.7). Sequence: apply `000058` while old binaries run, then deploy new binaries; §5.1 defines the fail-safe order, stall detection, rollback paths, and post-deploy verification |
| No new config keys | `outbox.poll_interval`, `max_retries`, webhook keys unchanged; lease is code-level (`WithClaimLease`) per REQ-9 |
| File-size gate | see §9 split plan (`repository.go` 208 lines today; claim code moves to a new `claim.go`) |

### 5.1 Deploy sequencing: fail-safe order, stall detection, rollback, verification

The rollout hazard is directional (stress-test audit H6): **new binary + pre-migration
schema = total delivery stall**; old binary + post-migration schema = harmless. Every
procedure below exists to make the first state unreachable and, if it is ever reached,
instantly detected and cleanly reverted.

#### 5.1.1 Fail-safe deploy order

Staging proof → schema first (no code) → pre-flight guard → binaries (rolling) →
verification. Migration-first is fail-safe in **both** directions: if the binary deploy
fails or is rolled back, production is left with old binaries on the new schema — the
verified-harmless overlap — so there is no pressure to rush the code rollout and no
need to down-migrate.

1. **Staging proof.** Apply `000058` + the new binaries in staging; `make
test-integration` green (AC-1..AC-3, `TestMigrationReversible`).
2. **Schema first.** `cmd/migrate up` in production while every instance still runs the
   old binary. Additive columns, no backfill (NULL = unclaimed), index untouched — this
   step must not change any delivery behavior. Verify before proceeding: `cmd/migrate
   version` reports `000058` and `aeroid_outbox_published_total` keeps growing for ≥ 5
   poll intervals with zero new warn logs.
3. **Pre-flight guard (mechanical).** Deploy tooling must assert, before starting any
   new instance, that the production schema has both columns:

   ```sql
   SELECT count(*) FROM information_schema.columns
   WHERE table_name = 'outbox_events'
     AND column_name IN ('claim_token', 'claimed_at');
   ```

   Expect `2`; abort the rollout otherwise. The guard lives in the deploy pipeline, not
   the binary, so the approved code surface (D1–D12) is untouched. Optional hardening
   (separate change, not part of this design): a fail-fast startup probe in
   `NewGormRepository` (`SELECT claim_token, claimed_at FROM outbox_events LIMIT 0`)
   that aborts process start instead of stalling delivery at the first poll.
4. **Binaries, rolling.** Deploy new binaries canary-first, then the fleet. During the
   mixed window (old + new instances sharing the schema) **at-least-once holds in every
   traced interleaving**: old instances double-fetch rows new instances hold →
   duplicates at status-quo level (multiplicity = old + new instance count, no worse
   than today); an old unconditional `MarkPublished` landing mid-claim makes the new
   holder's finalize return `ErrClaimLost` (warn noise; the event is published once);
   an old absolute `MarkFailed` racing the new relative write causes transient
   `retry_count`/`last_retry_at` skew that converges, with the dead-letter boundary
   possibly shifting by one attempt. No loss path exists — any failed finalize leaves
   the row unpublished and re-claimable. Handlers must stay idempotent through the
   window; no stronger guarantee is claimed. **Abort and roll back on any stall signal
   (§5.1.2) rather than continuing the rollout.**
5. **Post-deploy verification** (§5.1.4) proving claims are active, then steady state:
   `ErrClaimLost` warns ~0 (only F2/F4 events).

#### 5.1.2 Stall detection and monitoring signals

The binary-first misorder is fail-stop and loud, but it is an availability regression
and must alarm within one poll, not one incident. Poll = 5s default; every new instance
repeats the signature on every poll. The drain error path returns **before** metric
observation and the summary log (relay.go:97-100), which is itself the fingerprint:

| Signal | Healthy steady state | Stall (new binary, pre-migration schema) |
|---|---|---|
| Drain warn log (claim failure; SQLSTATE 42703 `column ... does not exist`) | 0 | every poll, every instance — the loud fail-stop marker |
| `outbox drain` info summary line | every poll | absent (drain returns at claim, before the summary) |
| `aeroid_outbox_published_total` | grows | flat |
| `aeroid_outbox_failed_total` | grows on handler errors | flat |
| `aeroid_outbox_pending` / `aeroid_outbox_oldest_age_seconds` | track the backlog | **frozen** at last pre-stall values — the error path skips `observeDrain`, so gauges do not decay to 0 |
| `aeroid_outbox_drain_duration_seconds` | tracks handler work | ~0 (claim fails instantly) |

Alert rule: **any** drain claim-failure warn (rate > 0) — unambiguous and fail-stop.
Backstop: `aeroid_outbox_published_total` growth == 0 for > 60s while the backlog is
non-empty (frozen gauges + flat counters confirm stall rather than an empty outbox).
Dashboards keyed on `pending == 0` alone will **not** detect this stall — key the alert
on the counters and the log rate.

Not an alert: transient `ErrClaimLost` warns during the mixed window and the first
minutes after cutover (expected old-binary races, §5.1.1 step 4). Alert only if
`ErrClaimLost` warns persist in steady state after full cutover — that is lease
misconfiguration (F9/F10) or a forgotten old instance, not rollout noise.

#### 5.1.3 Rollback paths

| Situation | Action | Notes |
|---|---|---|
| New binaries accidentally started against pre-migration schema (binary-first) | Revert binaries to the old build immediately | Delivery resumes on the next poll (≤ 5s). The stall is **write-free**: the claim statement fails at analysis time, so no rows, columns, or tokens were mutated — no replay or data cleanup. Optionally down-migrate afterward (last row) |
| Binary deploy fails mid-rollout (safe order) | Roll binaries back; **keep `000058` applied** | Old binaries on the new schema are the verified-harmless overlap; no down-migration and no deadline pressure |
| Full release revert | Revert all binaries, then `cmd/migrate down 1` | Gating rule: down-migrate only after the **last** new binary is gone — any new binary against the reverted schema recreates the stall. Drop is cheap (no index or default depends on the columns; SQL review verified) |
| Schema applied, release never deployed | `cmd/migrate down 1` before any new binary starts | All rows are NULL (no backfill wrote anything), so the drop is a pure schema change |

#### 5.1.4 Post-deploy verification — proving claims are active

"No errors" is necessary but not sufficient: it only proves schema/binary
compatibility. Claims are proven active by the deterministic one-event round-trip
below plus the ambient signals. Run after full cutover (all instances new).

1. **Schema applied.** `cmd/migrate version` → `000058`; the §5.1.1 step-3 column query
   returns `2`.
2. **Claim path alive.** For ≥ 10 poll intervals (50s at 5s): zero claim-failure warns
   (SQLSTATE 42703) on all instances; `aeroid_outbox_published_total` increasing;
   `outbox drain` summary lines every poll. The new binary cannot silently run the old
   path — the interface rename means only `ClaimBatch` exists — so a clean poll
   history proves the binary is executing the claim statements.
3. **Deterministic active-claim proof** (exact production statements, one due
   low-urgency event; delays that one event by seconds — pick one whose delay is
   acceptable, and complete the steps within one lease or rerun):

   ```sql
   -- (a) claim exactly as the binary does; capture id + minted token from RETURNING
   --     (backoff exclusion is Go-side in the relay, D4 — the claim has no backoff term)
   UPDATE outbox_events
   SET claim_token = gen_random_uuid(), claimed_at = now()
   WHERE id IN (
       SELECT id FROM outbox_events
       WHERE published_at IS NULL AND NOT dead_letter
         AND (claimed_at IS NULL OR claimed_at < now() - interval '30 seconds')
       ORDER BY created_at ASC, id ASC
       LIMIT 1
       FOR UPDATE SKIP LOCKED
   )
   RETURNING id, claim_token;

   -- (b) exclusivity check — MUST return 0 rows while the claim is unexpired:
   --     every instance's ClaimBatch now excludes the held row
   SELECT id FROM outbox_events
   WHERE id = '<id>'
     AND published_at IS NULL AND NOT dead_letter
     AND (claimed_at IS NULL OR claimed_at < now() - interval '30 seconds');

   -- (c) release with the minted token — MUST affect exactly 1 row
   UPDATE outbox_events SET claim_token = NULL, claimed_at = NULL
   WHERE id = '<id>' AND claim_token = '<token>';
   ```

   Expected: (b) is empty while (a)'s claim is unexpired (lease exclusion live), and
   (c) succeeds only with the minted token (conditional release live). After (c), the
   relay claims the event on the next poll; if the row is in backoff the relay skips
   and releases it (backoff is Go-side, D4) and delivers the next eligible row —
   confirm via the drain summary or `SELECT published_at IS NOT NULL` on an eligible
   row. If the lease expired between (a) and (b), the check is void; rerun.
4. **Mixed-window evidence (bonus, race-dependent).** If old instances were present at
   cutover, at least one `ErrClaimLost` warn observed during the window is direct
   evidence of enforcement (a claim was held and lost to an old unconditional write).
   Not a hard gate — step 3 is the deterministic one.
5. **Claim visibility (optional).** During a real backlog drain,
   `SELECT count(*) FROM outbox_events WHERE claim_token IS NOT NULL AND claimed_at
   >= now() - interval '30 seconds'` is transiently > 0; 0 at steady state is
   expected, not a failure.
6. **Behavioral proof.** AC-1 (`TestTwoRelaysExactlyOnce`) green in staging against
   the same migration — already part of the done gate.

## 6. Failure modes

| # | Failure | Behavior after fix | User-visible contract |
|---|---|---|---|
| F1 | Handler exceeds lease (e.g. webhook chain stall > 30s) | With renewal (D12) a live handler never loses its claim: renewals extend `claimed_at` every 10s, and a single call is bounded by the webhook timeout (≤ 10s app default) ≪ L − R = 20s. Claim loss requires renewal failure (crash, DB outage) → finalize → `ErrClaimLost`, warn; row re-claimed → re-delivered | At-least-once; duplicate side effect possible, same as any crash recovery. Sizing rule (D12): `lease > renewEvery + longest single handler call + finalize margin` — defaults 30s > 10s + 10s + margin. The draft's "lease must exceed handler p95" was the H1 flaw (§1.2): the claim covers the whole sequential batch (up to 450s), not one call |
| F2 | Crash between claim and finalize | Lease expires → re-claim with new token → re-delivery; old token dead | Exactly the AC-3 path; no loss, no permanent stall |
| F3 | DB outage during claim | `ClaimBatch` errors → drain aborts, returns 0 (today's fetch-failure behavior); nothing claimed | Delivery pauses, resumes on recovery |
| F4 | DB outage during finalize | Claims held until lease expiry → re-claim → re-delivery after recovery | At-least-once; no loss, no double-write by the same holder |
| F5 | Partial batch stale (mixed handler durations, slow instance) | `MarkPublished` applies the valid subset and returns `ErrClaimLost`; stale events re-claimed | Handled events stay published; stale ones redelivered once |
| F6 | App/DB clock skew (H4-corrected) | Lease is DB-clock-pure on both sides — `claimed_at` written by DB `now()` at claim/renewal, compared against DB `now()` at finalize/renewal — so instance clock skew can neither extend nor shorten a lease. Backoff is Go-side (D4): `last_retry_at` and the backstop both use the app clock → self-consistent → instance skew has no effect on backoff. (The draft's SQL-predicate analysis had the directions swapped: with a DB-clock predicate over an app-clock `last_retry_at`, a DB clock *ahead* of the app admits events *early* → the Go backstop rejects → bounded claim+release churn on near-due rows, never early delivery; a DB clock *behind* delays admission by the skew.) DB server NTP steps (H9): forward step > lease → premature expiry → re-claims → duplicate deliveries (legal, at-least-once); backward step → claims outlive lease → delivery delay; no loss in either direction — mitigate with NTP discipline and lease ≫ poll interval (30s vs 5s) | No premature delivery, no loss; churn/delay bounded by skew |
| F7 | Claim token collision across rows | Pairs `(id, token)` in finalize WHERE make cross-row interference impossible even in the astronomically unlikely collision | Safe by construction |
| F8 | Purge races a claim | `PurgeDeadLetters` deletes only dead-letter/published rows past cutoff; a purged row's later finalize affects 0 rows → `ErrClaimLost`, harmless | No resurrection, no error propagation |
| F9 | Lease misconfigured too short | Frequent spurious re-claims → duplicate deliveries + `ErrClaimLost` warn spam | Signal: warn log rate; sizing rule documented on `WithClaimLease` |
| F10 | Slow-instance starvation (handler consistently > lease) | Instance repeatedly loses claims; events still delivered by whoever finalizes | At-least-once holds; operator signal = `ErrClaimLost` warns |
| F11 | Re-claim churn on drained batch (batch=100, all in backoff) | Backoff rows are claimed then released by the Go backstop within the same drain pass (they sit at the batch head, so claims are held for milliseconds) — ≤ limit per-event releases per poll in the pathological case, zero in normal ops | Bounded, self-limiting churn (§1.3); the draft's zero-write SQL predicate was dropped because it made the claim scan O(backlog) (H5) |
| F12 | Renewal fails mid-drain (DB outage, NTP step) | Claims age out; finalize → `ErrClaimLost`; rows re-claimed after recovery → re-delivered | At-least-once; the lease is the renewal backstop. Recovery latency ≤ L = 30s after the holder stops renewing (crash) |

## 7. Migration steps

1. **Add** `migrations/000058_claim_outbox_events.up.sql`:

   ```sql
   -- Durable claim columns for multi-instance outbox dispatch (REQ-1/REQ-5).
   -- NULL claim_token/claimed_at = unclaimed. The partial index is NOT changed:
   -- a now()-relative index predicate would eject claimed rows at write time
   -- and never re-admit them when the lease expires (see design D5).
   ALTER TABLE outbox_events
       ADD COLUMN claim_token UUID,
       ADD COLUMN claimed_at TIMESTAMPTZ;
   ```

2. **Add** `migrations/000058_claim_outbox_events.down.sql`:

   ```sql
   ALTER TABLE outbox_events
       DROP COLUMN claim_token,
       DROP COLUMN claimed_at;
   ```

   No backfill: NULL = unclaimed, so every existing row is immediately eligible.
   Index untouched, so no index rebuild cost.

3. **Apply — schema strictly before binaries**: migrations are not auto-applied
   (AGENTS.md) — run `cmd/migrate up` in staging, verify, then production **while
   every instance still runs the old binary** (additive columns must not change
   delivery behavior; §5.1.1 step 2). `testpg.Migrate` picks `000058` up
   automatically for integration tests.

4. **Deploy binaries after the schema**: run the §5.1.1 step-3 pre-flight guard, roll
   new binaries canary-first (at-least-once holds through the mixed window; §5.1.1
   step 4), then verify per §5.1.4. Steady state after cutover: drain warn logs for
   `ErrClaimLost` ~0 (only F2/F4 events); `outbox drain` summary unchanged; existing
   metrics unchanged.

## 8. Testable acceptance mapping

All new tests are integration-tagged (`//go:build integration`) in a new
`internal/outbox/claim_integration_test.go` — **package `outbox`** (internal
test: AC-1 drives the unexported `relay.drain`, which an external
`outbox_test` package cannot compile), `testpg` setup mirroring
`repository_integration_test.go:13-23` — except the updated `fakeRepo` unit
tests which stay in `relay_test.go`.

| Req | Acceptance test | Assertions | Red proof |
|---|---|---|---|
| REQ-1 | `TestClaimBatch_AtomicClaim` | Append N events; `ClaimBatch(10)` returns 10 `ClaimedEvent`s each with non-nil token and `ClaimedAt` set; a second `ClaimBatch` returns none while claims are unexpired; claims visible in DB (`claim_token`/`claimed_at` non-NULL) | Red today: no columns → statement errors (or, against a stash of the old API, second fetch returns the same rows) |
| REQ-2 | `TestTwoRelaysExactlyOnce` (AC-1) | 10 events; two `Relay`s share one `GormRepository`, loop `drain` until all published (10s safety ceiling); handler counts invocations per event ID under mutex with 50-100ms latency and a **per-event barrier** (first invocation blocks up to **250ms** for a second); assert every ID handled **exactly once**; assert the outbox is empty via raw SQL on the shared `*gorm.DB` (`SELECT count(*) FROM outbox_events WHERE published_at IS NULL` — compiles pre- and post-fix, unlike a `ClaimBatch` call) | **Red today**: barrier forces both relays to fetch the same rows (no claim predicate) → `invocations[id] ≥ 2`. Uses only the stable public `Relay` API, so the same test file compiles pre- and post-fix. Parameters tuned empirically: 250ms barrier × 10 events ≈ 3.0s green run (the original 5s barrier paid the full timeout per event ≈ 1m41s); red stays deterministic (5.25s) |
| REQ-3 | `TestConditionalFinalize` (AC-2) | (a) Claim E with `T1`; finalize with a foreign token → `ErrClaimLost`, row unchanged; (b) finalize with `T1` → `published_at` set; (c) after re-claim (`T2`), finalize with dead `T1` → no-op | Red today: `MarkPublished` is unconditional → foreign token still publishes |
| REQ-4 | `TestConditionalFinalize` sub-assertion (AC-2.3) | Claim `T1`, `MarkFailed(retryCount=1, T1)` → `retry_count=1`; replayed stale `T1` `MarkFailed` → rejected, unchanged; re-claim `T2`, `MarkFailed(retryCount=2, T2)` → final `retry_count == 2` (the lost-update regression) and `last_retry_at` updated | Red today: two instances both write `retry_count=1` from stale snapshots |
| REQ-5 | `TestLeaseRecovery` (AC-3) | `WithClaimLease(1s)` (≥1s lease so the negative check is not flaky, H10); claim E then crash (no finalize); negative: second `ClaimBatch` excludes E while unexpired; set `claimed_at = now() - 2s` via SQL (DB-clock, sleep-free); second `ClaimBatch` returns E with `T2 ≠ T1`; handle + `MarkPublished(T2)` → published, batch empty; handler saw E exactly **twice total**; delayed finalize with `T1` → `ErrClaimLost` | Red today: a "crash" never blocks a second instance (no claims exist at all) |
| REQ-6 | `TestBackoffGoSkipRelease` (replaces `TestBackoffSQLExclusion`) | Event inside its backoff window **is** returned by `ClaimBatch` (backoff exclusion is Go-side, D4); the relay's backstop skips it (handler never invoked) and `Release` clears the claim; a second instance's `ClaimBatch` returns it immediately (no lease-induced invisibility); a due event is claimed and handled normally | Guards the H5 bound + REQ-6 "skipped events must not hold a claim" |
| REQ-6 | `TestClaimScanBoundedUnderBacklog` (H5) | 100k rows at `retry_count ≥ 11` (1h backoff) ahead of the head; `ClaimBatch(100)` returns exactly 100 rows — the head, in-backoff rows included — with no O(backlog) walk (structural bound, §1.3) plus a generous latency smoke; the relay releases the skipped rows | Locks the H5 scan bound; the draft's SQL backoff predicate would scan the whole backlog (red) |
| REQ-6 | (removed) `TestBackoffSQLParity` | — | The SQL/Go formula duplication no longer exists (D4); the parity hazard is gone with the SQL predicate — as is the H3 `POWER(2, …)` overflow |
| REQ-7 | `TestMigrationReversible` | `testpg.Migrate` (includes 000058) → columns exist; exec `000058.down.sql` → columns gone; re-apply up → tests rerun clean | n/a |
| REQ-8 | Updated `fakeRepo` + existing five relay tests | fakeRepo models claims (token map; `MarkFailed` preserves state **and clears the claim**, `MarkPublished`/`MarkFailed`/`Release`/`RenewClaims` honor tokens); `TestRelay_DrainPublishesEvents`, `TestRelay_HandlerErrorLeavesEventUnpublished`, `TestRelay_MetricsSink...`, `TestRelay_NilMetricsSinkIsSafe`, `TestRelay_DrainOnStop` all green | n/a (non-regression) |
| REQ-8 (H1) | `TestRelay_RenewsClaimsDuringLongDrain` (unit) | fakeRepo + injected `renewEvery` and fake `now` (relay fields; `relay_test.go` is `package outbox`); handler blocks; advance the fake clock past the cadence; assert `RenewClaims` was called with the pending claims and fakeRepo extended `claimed_at`; after the handler returns, `MarkPublished` succeeds | Guards D12: a long drain must not lose its claims |
| REQ-2 (H1) | `TestTwoRelaysSlowHandlerRenewalExactlyOnce` (integration) | `WithClaimLease(3s)`, relay `renewEvery = 1s`, handler sleeps 1.5s; 8 events; two relays drain concurrently; assert every ID handled exactly once and all published | Red without renewal (H1): claims expire mid-batch (3s < 8 × 1.5s) → re-claims → duplicates; green with renewal |
| REQ-4 (H1) | `TestMarkFailedExpiredClaimLeavesRetryCount` | Claim E, backdate `claimed_at` past the lease via SQL (DB-clock, sleep-free); `MarkFailed(T1)` → `ErrClaimLost`, `retry_count` untouched | Guards H1's backoff-governor interaction: a stale finalize must never advance retry state |
| REQ-8 | `TestRelay_BackstopRelease` (unit) | fakeRepo with an event inside its backoff window returned by `ClaimBatch` (the normal Go-side exclusion path, D4) → relay calls `Release`, handler never invoked, event stays unpublished | n/a |
| REQ-9 | Non-regression | Ordering/limit/backoff/dead-letter/drain-log/metrics/drain-on-stop behavior asserted by the existing tests above plus `TestOutboxRepo_AppendFetchMarkPublished`/`TestOutboxRepo_FailedTxRollsBackEvent` (updated to `ClaimBatch` + `Release` between claim and finalize so they remain single-instance deterministic) | n/a |

## 9. Implementation plan (file-by-file)

| File | Change | Size after |
|---|---|---|
| `migrations/000058_claim_outbox_events.{up,down}.sql` | Add/drop two columns; index untouched (D5) | ~10 lines each |
| `internal/outbox/outbox.go` | `Claim`, `ClaimedEvent`, `ErrClaimLost`; interface: `ClaimBatch`, conditional `MarkPublished`/`MarkFailed`, `Release`, new `RenewClaims`; doc comments rewritten (E3-style misleading comments removed) | ~160 |
| `internal/outbox/claim.go` (new) | `defaultClaimLease`, `Option`/`WithClaimLease`, the five SQL statements, `GormRepository.ClaimBatch`/`MarkPublished`/`MarkFailed`/`Release`/`RenewClaims` | ~230 |
| `internal/outbox/repository.go` | Remove `FetchUnpublished`/`MarkPublished`/`MarkFailed` (moved to `claim.go`); add pointer claim fields to `eventRow`; keep `Append`/`PurgeDeadLetters`/cleaner | ~120 (from 208) |
| `internal/outbox/relay.go` | `drain` rework per §2.4 (Go backstop primary, renewal cadence, pending set); `NewRelay` unchanged; `renewEvery` field (default 10s) | ~280 (from 197) |
| `internal/outbox/relay_test.go` | fakeRepo implements new interface with claim semantics (incl. `RenewClaims`); backstop-release + renewal unit tests; existing five tests updated mechanically | ~390 |
| `internal/outbox/claim_integration_test.go` (new, integration tag) | AC-1..AC-3, go-skip-release, scan-bound backlog, renewal slow-handler, expired-claim `MarkFailed`, migration reversibility | ~420 |
| `internal/outbox/repository_integration_test.go` | Update to `ClaimBatch` + `Release` | ~90 |

Repository average cyclomatic ≤ 5.0, funlen 50, cyclop 10: `ClaimBatch` is one
`Raw().Scan` call; per-claim loop logic stays in `relay.drain` (already
`nolint`ed for the state machine, and grows only a release branch).

## 10. Out of scope (unchanged from the requirements spec)

Poison-event/quarantine and dead-letter observability; `NewEvent` zero-ID trap;
`WebhookHandler`/`NotificationDispatcher`/metrics/config changes; any stronger
than at-least-once delivery guarantee.

## 11. Done gate

```bash
make test && make lint && make lint-strict && make check-file-sizes && make build
make test-integration   # AC-1..AC-3 + backoff-skip/scan-bound/renewal/migration tests are integration-tagged
```

AC-1 must be demonstrated red against the pre-fix code (the barrier test uses
only the stable public API, so it runs unmodified on the current tree); AC-2/AC-3
are red by construction against the pre-migration schema (claim columns and
conditions do not exist). All three green after the change.
