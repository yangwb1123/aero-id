# Requirements Specification — internal/outbox: claim-based dispatch for multi-instance at-least-once delivery

> **状态：已实现**（2026-08-06）— 见 `docs/plans/2026-08-06-*` 实现记录；门禁全绿。

| | |
|---|---|
| Module | `internal/outbox` |
| Direction | "Claim-based dispatch to make multi-instance at-least-once delivery actually safe (SKIP LOCKED does not span handle->mark)" |
| Source analysis | `docs/auto/analyses/internal-outbox-f6bb61c6.json` (direction #1) |
| Date | 2026-08-06 |
| Scope | Claim acquisition, conditional finalization, lease recovery, schema, and the three acceptance checks — nothing else |

## 1. Problem statement

`FetchUnpublished` uses `FOR UPDATE SKIP LOCKED` on a bare autocommit `SELECT`
(no surrounding transaction), so the row lock is released when the SELECT
statement ends. The relay then runs the handler and only afterwards calls
`MarkPublished`/`MarkFailed`. A second relay instance polling between another
instance's SELECT and its `MarkPublished` sees the same event still
unpublished and dispatches it concurrently. The module's own comment claiming
SKIP LOCKED prevents duplicate processing in multi-instance deployments does
not hold; `MarkFailed` additionally becomes a lost update on `retry_count`.

Consequence: with more than one relay process (AGENTS.md explicitly anticipates
background consumers with "leases/fencing"; only one active attempt may mutate
a given operation step), at-least-once degrades to **duplicate side effects**.
In the current composition root (`internal/app/outbox.go:63-68`) the relay
handler is `NotificationDispatcher` (duplicate notification rows) optionally
chained with `WebhookHandler` (duplicate signed HTTP POSTs).

## 2. Evidence verification (all citations re-checked against the repository on 2026-08-06)

| # | Cited evidence | Verification result |
|---|---|---|
| E1 | `internal/outbox/repository.go:78-88` FetchUnpublished: `clause.Locking{...SkipLocked}` on a bare `Find` with no transaction wrapper; lock lifetime ends at statement end | **Verified.** Function spans `repository.go:78-91`. The `Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked})` call at line 82 is chained on `r.db.WithContext(ctx)` directly (`repository.go:81-88`); there is no `Transaction(...)` wrapper anywhere in the file, so the statement runs in autocommit and the row lock expires at statement end. |
| E2 | `internal/outbox/relay.go:96-172` drain: fetch → `handler.Handle` → `MarkPublished` with zero `Transaction` usage | **Verified.** `drain` spans `relay.go:95-168`; the sequence is `FetchUnpublished` (line 97) → per-event `r.handler.Handle` (line 128) → `MarkPublished` (line 163). No `Transaction` usage exists in `relay.go` (grep confirmed). The claim-less gap between lines 97 and 163 is the duplicate-dispatch window. |
| E3 | `internal/outbox/repository.go:76-77` comment claiming SKIP LOCKED prevents duplicate processing in multi-instance deployments | **Verified verbatim.** `repository.go:76-77`: "Uses FOR UPDATE SKIP LOCKED so concurrent instances each obtain a disjoint set of events — preventing duplicate processing in multi-instance deployments." Contradicted by E1/E2: the lock never covers the handler execution window. |
| E4 | `internal/outbox/relay_test.go:55-101` fakeRepo returns events by in-memory copy; no test exercises two concurrent relay instances | **Verified.** `fakeRepo` spans `relay_test.go:56-101`: `FetchUnpublished` (line 71) copies from an in-memory slice filtered by a `published` map; `MarkFailed` (line 58) explicitly does not preserve retry state. All five relay tests drive a single relay instance; no concurrency test exists. |
| E5 | `migrations/000008_create_outbox_events.up.sql`, `migrations/000018_outbox_retry.up.sql`: no `claimed_at`/lease/attempt column | **Verified.** 000008 defines `id, aggregate_type, aggregate_id, event_type, payload, created_at, published_at`; 000018 adds `retry_count, last_retry_at, dead_letter` and recreates the partial index `idx_outbox_unpublished (created_at, id) WHERE published_at IS NULL AND NOT dead_letter`. No claim/lease column exists. Highest applied migration is `000057`; a new migration is `000058`. |
| E6 | Production wiring (supporting) | `internal/app/outbox.go:63-68`: `outbox.NewRelay(...)` + `relay.Start(pollInterval)` (default `outbox.poll_interval: 5s`, `configs/config.yaml:138-146`). Relay `Start` (relay.go:180-197) and the drain-on-stop path both call `drain`; both must claim. |

## 3. Requirements

### REQ-1 — Durable, exclusive claim acquisition

`FetchUnpublished` (or a replacement claim operation on `Repository`) must
persist a claim on every returned row **in the same atomic SQL statement that
selects the batch** (e.g., a single `UPDATE ... WHERE id IN (SELECT ... FOR
UPDATE SKIP LOCKED) RETURNING`), so the claim survives the statement end and
is durable immediately.

- A claim consists of a unique `claim_token` and a `claimed_at` timestamp,
  persisted in new `outbox_events` columns (REQ-5).
- Rows carrying an **unexpired** claim are invisible to all other claim
  attempts (exclusive), including rows whose original row lock was released.
- The claim query may keep `SKIP LOCKED` disjointness to avoid lock
  contention, but SKIP LOCKED alone must no longer be the correctness
  mechanism (E3 comment must be rewritten or removed).
- No distributed transaction is introduced; single-statement atomicity in the
  source database is sufficient.

### REQ-2 — Claim covers the handle window

The relay must hold the claim for the entire span from fetch through
`handler.Handle`, and consume/release it only via `MarkPublished` /
`MarkFailed`. After this change, the module may not rely on any mechanism
whose scope ends before `MarkPublished`/`MarkFailed` completes.

### REQ-3 — Conditional finalization

`MarkPublished` and `MarkFailed` must apply **only when the caller still
holds an unexpired claim with the matching `claim_token`**:

- A finalize call with a missing, foreign, or expired claim is a no-op (no
  row change) and returns a distinguishable, documented result (typed
  sentinel error or boolean/rows-affected), so the relay can log it without
  treating it as success.
- A stale holder must never overwrite state written by a newer claim holder
  (e.g., a delayed `MarkPublished` from a crashed-then-revived instance).
- Claim tokens must not be reusable across claims: re-claiming mints a new
  token, which invalidates every prior holder's finalize.

### REQ-4 — No lost update on `retry_count`

`MarkFailed` must be conditional on the claim (REQ-3). Concurrent or stale
`MarkFailed` attempts must produce the same final `retry_count` /
`last_retry_at` / `dead_letter` state as a serialized sequence. In
particular, two instances both failing an event must not both write
`retry_count = 1` from a stale snapshot; the holder's write must be relative
to the current row value (or the stale attempt must be rejected by the claim
check). The current unconditional `UPDATE ... WHERE id = ?` (`repository.go:119-120`,
inside `MarkFailed` at `repository.go:111`) is the defect.

### REQ-5 — Lease expiry and re-claim (crash recovery)

- Claims expire after a bounded lease duration; an expired claim is
  re-claimable (this is the recovery path when a relay crashes between fetch
  and finalize).
- Lease comparisons use the **database clock** (`now()`), never application
  clocks, so instances with skewed clocks cannot extend or shorten leases.
- The lease duration is a single fixed constant (code or config) whose value
  must exceed the longest plausible handler duration, and the partial-index
  predicate (REQ-7) must use the same constant so the index stays usable.
  Default suggestion: 30s.
- Re-claiming an expired row assigns a fresh `claim_token` and resets
  `claimed_at`; prior tokens are permanently invalid (REQ-3).

### REQ-6 — Backoff interplay preserved

Events skipped by the relay's per-event backoff check (`relay.go:121-125`,
`LastRetryAt + backoff(RetryCount) > now`) must **not hold a claim** — either
the claim query excludes them (backoff predicate expressed in SQL) or the
relay releases their claims immediately after the skip decision. A skipped
event must remain immediately claimable by any instance, and skipping must
not delay delivery of other events in the same or later batches.

### REQ-7 — Schema migration

New reversible migration `000058_claim_outbox_events` (never edit applied
migrations):

- `ALTER TABLE outbox_events ADD COLUMN claim_token UUID, ADD COLUMN claimed_at TIMESTAMPTZ;`
- Drop and recreate the partial index `idx_outbox_unpublished` so its
  predicate matches the new claim predicate (e.g., `WHERE published_at IS
  NULL AND NOT dead_letter AND (claimed_at IS NULL OR claimed_at < now() -
  interval ...)`), keeping the fetch index-served.
- `.down.sql` removes the columns and restores the current index predicate.
- `Append` must not set claim columns (new rows start unclaimed).

### REQ-8 — Interface, implementations, and contract propagation

- `Repository` (`outbox.go:34-52`) changes to carry claims: `FetchUnpublished`
  results include the claim token, and `MarkPublished`/`MarkFailed` accept the
  token. The relay passes the token it received from the fetch.
- `GormRepository` and every test fake (`fakeRepo` in `relay_test.go`) are
  updated together; the relay unit tests keep passing with the updated fake
  preserving claim semantics (unlike today, where `MarkFailed` drops state,
  E4).
- `Append`, `PurgeDeadLetters`, metrics, and backoff formula are unchanged.
- Delivery contract stays **at-least-once**: AC-1 guarantees no concurrent
  overlap, but lease recovery (AC-3) may legitimately re-deliver after a
  crash; downstream handlers must remain idempotent, and no stronger
  guarantee may be claimed.
- Production wiring (`internal/app/outbox.go`) needs no behavioral change
  beyond compiling against the new signatures; relay `Start` and the
  drain-on-stop path both go through the claim flow (E6).

### REQ-9 — Non-regression constraints

- Existing single-instance behavior is unchanged: oldest-first ordering,
  `limit` batching, backoff skipping, dead-letter transition at
  `maxRetries`, drain summary log and metrics, drain-on-stop.
- Repository file-size gate: `repository.go` is currently 208 lines; claim
  logic may push it toward the 400-line planning mark — split into sibling
  files within `internal/outbox` if needed (AGENTS.md engineering
  constraints), keeping the package cohesive.
- No new configuration keys are required for the fix (lease constant only);
  `outbox.poll_interval`/`max_retries` semantics are unchanged.

## 4. Acceptance criteria

The three checks from the direction are preserved below, restated as
deterministic, runnable tests. AC-1 and AC-2 are integration-tagged tests
against real Postgres (`testpg.Start` + `testpg.Migrate`, mirroring
`repository_integration_test.go:13-19`); AC-3 may be integration or, with an
injectable lease, a deterministic repository test against real Postgres.

### AC-1 — Two concurrent relays never dispatch the same event (integration; red before fix)

> Source check: "two Relays sharing one GormRepository drain concurrently for N seconds; assert every event is handled exactly once (handler counts invocations per event ID) — currently fails due to overlap."

Testable form (deterministic, not wall-clock-flaky):

1. Seed `N = 20` events via `Append` inside a caller transaction.
2. Create **two `Relay` instances sharing one `GormRepository`**. Each runs a
   loop calling `relay.drain(ctx)` until a shared deadline (safety ceiling
   only, e.g. 10s) or until every event is published.
3. Handler: counts invocations per event ID under a mutex, sleeps ~50-100ms
   per invocation to widen the fetch→mark window, and uses a **per-event
   barrier**: the first concurrent invocation for an event ID blocks until a
   second invocation for the same ID arrives (or a 5s barrier timeout), then
   both proceed. The barrier forces the overlap scenario — instance A has
   fetched and is handling while instance B fetches — so the test does not
   depend on timing luck.
4. Assertions:
   - Every event ID is handled **exactly once** (`invocations[id] == 1` for
     all 20).
   - After the run, `FetchUnpublished` returns empty (all events published).
5. Red/green: against current code the barrier guarantees both relays fetch
   the same events, so `invocations[id] >= 2` for overlapped events and the
   test fails; with REQ-1..REQ-3 implemented, instance B's fetch excludes
   claimed rows and the test passes.

### AC-2 — Claim exclusivity and conditional finalization (integration; red before fix)

> Source check: "FetchUnpublished followed by MarkPublished/MarkFailed conditional on a durable claim (e.g. UPDATE ... SET claimed_at WHERE id = ANY(...) returning rows) so a second instance cannot claim the same event."

Testable form:

1. **Claim exclusivity**: append event E. Instance 1 calls
   `FetchUnpublished` → E returned carrying token `T1`. Instance 2 calls
   `FetchUnpublished` → E **not** returned (and E is not returned by any
   later fetch while the claim is unexpired).
2. **Conditional MarkPublished**: a finalize with a foreign/stale token
   (e.g., a random token, or `T1` after a simulated re-claim) → no-op: E
   still has `published_at IS NULL`, claim unchanged, and the call returns
   the documented stale-claim result. Finalize with `T1` → `published_at`
   set, E leaves the unpublished set.
3. **No lost update on MarkFailed**: append E2. Instance 1 claims (`T1`) and
   calls `MarkFailed(retryCount=1, T1)` → accepted (`retry_count=1`).
   Simulate a second, stale holder (token `T1` replayed, or a token from a
   claim superseded by re-claim) calling `MarkFailed(retryCount=1, ...)` →
   rejected, row unchanged. Current holder re-claims (`T2`) and calls
   `MarkFailed(retryCount=2, T2)` → accepted; assert final `retry_count == 2`
   (not 1 — this is the lost-update regression the direction identifies) and
   `last_retry_at` reflects the last call.

### AC-3 — Expired claims are re-claimable after a crash before MarkPublished (recovery path)

> Source check: "Test that a claim older than a lease timeout is re-claimable after a simulated crash before MarkPublished (recovery path)."

Testable form (lease injectable — e.g., constructor/field with default 30s;
test uses ~100ms):

1. Append E. Instance 1 claims E and then **crashes**: no `MarkPublished`/`MarkFailed` is ever called.
2. Negative check: while the lease is unexpired, instance 2's
   `FetchUnpublished` does not return E (overlap exclusion from AC-2 holds
   across the crash).
3. Wait past the lease (sleep ≥ 2× lease, or set `claimed_at` to the past
   directly for an exact, sleep-free test).
4. Instance 2's `FetchUnpublished` returns E with a **new** token `T2 ≠ T1`.
5. Instance 2 handles E and calls `MarkPublished(T2)` → `published_at` set,
   `FetchUnpublished` empty.
6. Assert the handler saw E exactly **twice in total** (the crashed attempt
   plus the recovery attempt) — demonstrating the intended at-least-once
   recovery semantics, not a third delivery.
7. Assert a delayed finalize with the dead token `T1` is rejected (no
   overwrite of the recovery result).

## 5. Testability requirements

- AC-1 must not assert on elapsed time; the barrier makes the race
  deterministic and the deadline is only a safety ceiling.
- AC-3's lease must be injectable so the test does not wait 30s; the negative
  (unexpired) check must run before expiry, so the test needs both a short
  lease and a fast assertion order.
- Claim/finalize comparisons must be DB-clock based (REQ-5), so tests may
  manipulate `claimed_at` directly via SQL without app-clock coupling.
- All three ACs must fail (red) against the current implementation: AC-1 via
  the barrier, AC-2 via the missing claim columns/conditions, AC-3 via the
  unconditional fetch (a "crash" never blocks a second instance at all).

## 6. Out of scope

Explicitly **not** part of this direction (tracked as separate directions in
the source analysis and rejected from this spec):

- Poison-event/quarantine handling and dead-letter observability (analysis
  direction #3: `ErrMalformedPayload`, dead-letter metrics, drain deadline).
- `NewEvent` zero-ID trap and backoff/dead-letter test coverage (analysis
  direction #2).
- Changes to `WebhookHandler`, `NotificationDispatcher`, metrics, config
  surface, or other modules.

## 7. Done gate

```bash
make test && make lint && make lint-strict && make check-file-sizes && make build
make test-integration   # AC-1, AC-2, AC-3 are integration-tagged; run after SQL/repository changes
```

AC-1..AC-3 must be green; AC-1 and AC-2 must first be demonstrated red
against the pre-fix code (or reproduced via the barrier on a stash) to prove
they exercise the defect.
