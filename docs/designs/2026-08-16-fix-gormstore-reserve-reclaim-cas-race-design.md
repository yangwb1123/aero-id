# Design — Fix `GormStore.Reserve` reclaim CAS race that double-executes idempotent mutations

| | |
|---|---|
| Date | 2026-08-16 |
| Module | `internal/idempotency` (fix: `internal/idempotency/store.go`; regression test: `internal/idempotency/store_test.go`) |
| Direction | Make the stale-reservation reclaim in `GormStore.Reserve` a single-winner compare-and-swap on the observed `expires_at`, so concurrent retries of a crashed leader's reservation can never both receive `reserved=true` (which would double-run the handler) |
| Effort | 3 (one `WHERE` predicate change in `Reserve`; one integration regression test; no schema, wiring, middleware, or Redis changes) |
| Status | Proposed — no repository code modified by this document |

## 0. Verification verdict on the evidence

The evidence was treated as untrusted and every load-bearing citation was re-checked against the current
tree. **Verdict: verified.** The defect is real, reproducible as described, and the proposed fix is minimal
and correct. Discrepancies found are exactly the minor line-number drift the evidence itself flags; nothing
substantive fails:

| # | Claim | Verified location | Status |
|---|---|---|---|
| E1 | Reclaim `UPDATE` predicates only on `status = 0`, mutates only `created_at`/`expires_at`, `RowsAffected == 1 → (nil, true, nil)` | `internal/idempotency/store.go:81-88` (claim `:82-89`): guard `:82`, `Where("id = ? AND status = 0", existing.ID)` `:84`, `Updates({created_at, expires_at})` `:85`, `RowsAffected == 1 → reserved=true` `:86-88`. `status` is **never written by the reclaim** → double-claim is possible | ✅ real |
| E2 | `reservationTTL` doc: stale reservation "may be reclaimed by a retry"; `const reservationTTL = 2 * time.Minute` | `store.go:16-18` | ✅ |
| E3 | `Store.Reserve` contract: first caller gets `reserved=true` and must run handler + `Complete`; subsequent callers get `reserved=false` | `store.go:31-34` | ✅ |
| E4 | `RedisStore` claims atomically via `SET NX EX` — single winner, cannot double-claim | `store_redis.go:75-76` (`Set().Key(...).Nx().Ex(redisReservationTTL)` at `:76`) | ✅ |
| E5 | Middleware runs the handler iff `reserved == true`; in-progress → `409 idempotency.in_progress` | `internal/middleware/idempotency.go:38-41` (`case reserved: … c.Next()`); 409 mapping at `:53-56` | ✅ |
| E6 | Only conflict guard for a `(uid, endpoint, idem_key)` replay is `UNIQUE (uid, endpoint, idem_key)`; `status INT NOT NULL DEFAULT 0` (0 = in progress) | `migrations/000004_create_idempotency_keys.up.sql` | ✅ |
| E7 | No service-level dedup: `/v1` mutations rely solely on the reservation row; double `reserved=true` double-executes the handler | `store.Reserve` has exactly one caller, `internal/middleware/idempotency.go:34` (the `Reserve` at `internal/service/billing.go:112` is a different gateway type); middleware runs `c.Next()` iff reserved (E5); the unique constraint (E6) is never exercised because both retries operate on the same pre-existing row | ✅ |
| E8 | Integration test pattern: build tag `integration`, `package idempotency_test`, `testpg.Start`/`testpg.Migrate`/`testpg.RepoRoot`; pool `MaxOpenConns: 5` | `internal/idempotency/store_test.go`; `internal/repository/testpg/testpg.go:42` (`MaxOpenConns: 5`), `Start`/`Migrate`/`RepoRoot` | ✅ |
| E9 | Gate definitions: `test-integration` = `go test -tags=integration -p 1 ./... -count=1`; `check-integration-compile` = `go test -tags integration -run '^$' ./...` + `go vet -tags integration ./...`; `check-file-sizes` exempts `*_test.go` | `Makefile:15`, `Makefile:35-38`, `Makefile:69` | ✅ |

Additional confirmations beyond the evidence:

- **Both backends are live and the divergence matters.** `internal/app/workers.go:116-123` selects
  `RedisStore` when Redis is configured and `GormStore` (with `RunCleaner(time.Hour)`) otherwise. The
  GormStore path is therefore a real deployment configuration, and the race is reachable there.
- **Analysis source exists.** `docs/auto/analyses/internal-idempotency-732ffbb3.json` contains finding #1
  (the reclaim CAS race), finding #2 (expired-*completed* records replay stale until the hourly purge,
  vs. Redis auto-expiry), and finding #3 (middleware retry-contract paths untested). The spec's "Out of
  scope" section correctly defers #2 and #3. Note: the file is a text/JSON hybrid, not strict JSON —
  cosmetic only.
- **The dangerous window is real under the current code.** Both concurrent reclaimers read the same stale
  row (`Status == 0`, `ExpiresAt` past), both pass the guard `existing.Status == 0 && now.After(existing.ExpiresAt)`
  (`store.go:82`), and because the reclaim never changes `status`, both `UPDATE ... WHERE id = ? AND status = 0`
  statements match the row → both `RowsAffected == 1` → both return `reserved=true` → the middleware
  executes the handler twice for one `Idempotency-Key` → duplicate side effects (e.g. two messages/files).

## 1. Problem statement

`internal/idempotency` gives mutating HTTP requests carrying an `Idempotency-Key` safe-retry semantics:
the first caller of `Store.Reserve` receives `reserved=true` and must run the handler, then `Complete`
(store the response) or `Release` (on 5xx). Retries replay the stored response; concurrent duplicates get
`409 idempotency.in_progress`. Two storage backends exist: `GormStore` (PostgreSQL) and `RedisStore`
(SET NX).

In the stale-reservation reclaim path (`internal/idempotency/store.go:81-88`), the reclaim `UPDATE`:

```go
Where("id = ? AND status = 0", existing.ID).
Updates(map[string]any{"created_at": now, "expires_at": now.Add(reservationTTL)})
```

matches on `status = 0` — a column the `UPDATE` never changes — and bumps only `created_at`/`expires_at`.
Consider two concurrent retries A and B of a reservation whose leader crashed:

1. A and B both read the row with `Status == 0` and `ExpiresAt` in the past (both pass the guard at
   `store.go:82`; neither has written yet).
2. A executes the `UPDATE`; the row still has `status = 0` → `RowsAffected = 1` → A returns `reserved=true`.
3. B executes the same `UPDATE`; the row still has `status = 0` (A never changed it) → `RowsAffected = 1` →
   B also returns `reserved=true`.
4. The middleware (`internal/middleware/idempotency.go:38-41`) runs the handler for both A and B under the
   same `(uid, endpoint, key)` → duplicate side effects.

This violates the `Store.Reserve` contract (E3) and diverges from `RedisStore`, whose atomic `SET NX`
(E4) cannot double-claim. The `UNIQUE (uid, endpoint, idem_key)` constraint (E6) does not help: both
retries operate on the same pre-existing row, so the constraint is never exercised.

## 2. Design — single-winner reclaim CAS (REQ-1)

### 2.1 The change

In `GormStore.Reserve`, predicate the reclaim `UPDATE` on the **observed stale `expires_at` value** in
addition to `id` and `status`:

```go
	// Reclaim a stale in-progress reservation whose leader never completed.
	if existing.Status == 0 && now.After(existing.ExpiresAt) {
		reclaim := s.db.WithContext(ctx).Model(&idempotencyRow{}).
			Where("id = ? AND status = 0 AND expires_at = ?", existing.ID, existing.ExpiresAt).
			Updates(map[string]any{"created_at": now, "expires_at": now.Add(reservationTTL)})
		if reclaim.Error == nil && reclaim.RowsAffected == 1 {
			return nil, true, nil
		}
	}
	return &Record{
		Status:      existing.Status,
		ContentType: existing.ContentType,
		Body:        existing.Response,
		Completed:   existing.Status > 0,
	}, false, nil
```

**Why this works.** The winner's `UPDATE` atomically replaces `expires_at` with a fresh future value
(`now.Add(reservationTTL)`). Every concurrent loser's `WHERE … AND expires_at = <old value>` then matches
zero rows → `RowsAffected = 0` → the loser falls through to the unchanged existing-record return with
`reserved=false, Completed=false` → the middleware maps that to `409 idempotency.in_progress`
(`internal/middleware/idempotency.go:53-56`). The winner alone runs the handler; after `Complete`, retries
replay.

**Single-winner proof sketch.** All reclaimers that enter the reclaim branch share the same `existing` row
and therefore the same observed `existing.ExpiresAt`. The UPDATE statement is atomic in PostgreSQL: of all
concurrent statements matching `expires_at = old`, exactly one commits first and replaces the value;
every later statement re-evaluates the predicate against the committed row and finds a different
`expires_at` → 0 rows. Postgres UPDATE counts **matched** rows (unlike MySQL's "changed rows" semantics),
so the winner reliably sees `RowsAffected == 1`, and the winner's `SET` always changes the values (fresh
`now`), so there is no ambiguity.

### 2.2 TIMESTAMPTZ precision note (design constraint — no code change)

Postgres stores `TIMESTAMPTZ` at microsecond precision while Go `time.Time` carries nanoseconds. The CAS
compares `existing.ExpiresAt`, which is read back from the DB and is therefore already microsecond-truncated
and UTC-normalized; round-tripping it through the `WHERE` clause is stable, so equality holds. The winner's
new `expires_at` is likewise truncated on write and re-read consistently by later reclaimers. Two rules
follow:

- **Only the observed `existing.ExpiresAt` may appear in the CAS predicate.** Substituting `time.Now()` or
  any freshly computed value breaks the CAS: two reclaimers compute different `now` values and a
  range-style predicate (`expires_at < now`) would still match the stale row for both → both win → the bug
  returns. Exact equality on the round-tripped value is the only safe predicate.
- Even in a hypothetical precision mismatch, the CAS cannot produce two winners — only the value actually
  stored in the row can satisfy the predicate, and the winner replaces it. The precision concern only
  affects whether *the winner's own* predicate matches, which it always does by construction.

### 2.3 Preserved unchanged

- Guard `existing.Status == 0 && now.After(existing.ExpiresAt)` (`store.go:82`) — stale **in-progress**
  rows only. Expired *completed* rows (`status > 0`) keep their current behavior (replay until the hourly
  `RunCleaner` purge); the TTL-parity divergence with Redis (analysis finding #2) is explicitly out of
  scope.
- `const reservationTTL = 2 * time.Minute` (`store.go:18`) and `redisReservationTTL` — unchanged.
- Loser fall-through return (`store.go:90-93`) — unchanged: `(record{Status: existing.Status,
  Completed: existing.Status > 0}, false, nil)`. For a CAS loser inside the guard this is
  `(Status: 0, Completed: false)` → middleware `409` — it must never return `reserved=true`.
- `Complete`, `Release`, `RunCleaner`, `purgeExpired` — unchanged.
- `RedisStore` (E4) — untouched.

### 2.4 Alternatives considered and rejected

| Alternative | Why rejected |
|---|---|
| Reclaim predicate `WHERE expires_at < now` (time-range) | `now` differs per caller; both concurrent reclaimers' predicates match the same stale row → both `RowsAffected == 1` → same bug. Only equality on the observed value is a CAS. |
| `SELECT … FOR UPDATE` around read + reclaim (transaction) | Works but adds a transaction, row-lock hold time, and abandoned-transaction failure modes; the single-statement CAS needs no transaction and holds no lock beyond the UPDATE itself. |
| Bump `status` to a sentinel "reclaiming" value in the reclaim `UPDATE` | `status` doubles as the stored response status code for completed records (replay path, `internal/middleware/idempotency.go:47-52`); a sentinel would need a distinct column or cleanup path and changes migration surface. Changes `status` semantics for no benefit over the `expires_at` CAS. |
| Add a `reclaimed_at`/version column (schema change) | Unnecessary: `expires_at` is already a natural optimistic version for this row's lifecycle and requires no migration. |

## 3. API changes and compatibility constraints

### 3.1 Public API: none

- `idempotency.Store`, `Record`, `NewGormStore`, `NewRedisStore` — unchanged.
- HTTP surface: status codes, error code `idempotency.in_progress`, `Idempotent-Replayed` header,
  fail-open-on-store-error behavior — unchanged. No OpenAPI change (no new endpoints, no response-shape
  change).
- `internal/app/workers.go:116-123` wiring — unchanged.

### 3.2 Behavioral contract

- The fix enforces the **already documented** contract (E3) in the stale-reclaim race: at most one caller
  receives `reserved=true`. It introduces no new contract; it removes a violation.
- Backend parity: after the fix, `GormStore` and `RedisStore` agree that a stale in-progress reservation
  can be claimed by exactly one retry. (TTL parity for *completed* records remains a separate open item —
  finding #2, out of scope.)
- All happy paths are byte-for-byte unchanged: reserve-create, in-flight 409, completed replay,
  `5xx → Release` (middleware `idempotency.go:43-45`), fail-open on Reserve error (`idempotency.go:36-37`).

### 3.3 Compatibility constraints checklist

1. Do not reclaim `status > 0` rows (preserves replay-until-purge for expired completed records; the
   `Status == 0` guard stays).
2. Keep `reservationTTL`/`redisReservationTTL`/`recordTTL` values.
3. Use only `existing.ExpiresAt` in the CAS predicate (Section 2.2).
4. No migration, no new packages, no middleware, no Redis changes.
5. No test, migration, or Makefile target modified — the regression test is additive.

## 4. Failure modes and race analysis

**Interleaving matrix for the reclaim path** (all verified safe with the fix):

| Interleaving | Result | Correct? |
|---|---|---|
| A wins CAS; B's predicate misses (A committed first) | A `reserved=true` runs handler; B returns stale snapshot `(Status: 0, Completed: false)` → `409` | ✅ — exactly one execution; B's 409 is transient, client retry replays |
| A wins CAS and `Complete`s before B's `First()` read | B reads `Status > 0` → guard skipped → completed replay | ✅ |
| B reads after A's reclaim `UPDATE` but before A's `Complete` | B sees fresh `ExpiresAt` → guard fails → `409` (in-flight) | ✅ |
| `Complete` commits between B's read and B's reclaim `UPDATE` | `status = 0` predicate fails → `RowsAffected = 0` → B returns snapshot → `409`/replay | ✅ — a completed record can never be reclaimed into a fresh execution |
| Reclaim `UPDATE` returns an error (DB down/conn error) | Falls through with snapshot, `err = nil` → `409` or replay based on snapshot `Status`; never `reserved=true` | ✅ — pre-existing behavior, preserved; the middleware's fail-open applies only when `Reserve` itself returns an error |
| Reclaimers run under REPEATABLE READ (not the Postgres/GORM default of READ COMMITTED) | Postgres `EvalPlanQual` re-evaluates the `WHERE` against the latest committed version of a candidate row; a loser either blocks then finds 0 rows, or gets a serialization error → falls through to `409` | ✅ — never a spurious win |

**Clock skew between app instances.** The reclaim guard uses the local clock (`now.After(existing.ExpiresAt)`).
Skew can shorten/lengthen the effective reservation TTL per instance but cannot create double execution: the
CAS still admits exactly one winner per row state, and any loser returns `409`. Pre-existing property,
unchanged.

**Stale 409 after a winner completes.** A loser that read the pre-reclaim snapshot may return `409` for a
brief window even after the winner completed; the client retries and gets the replay. This matches existing
in-flight semantics — idempotency keys are designed for client retry — and never duplicates a mutation.

**Postgres `RowsAffected` semantics.** Postgres counts matched rows (not changed values), so the winner
always sees `1`; the loser's `WHERE` misses entirely so it sees `0`. No "no-op update" ambiguity exists
(the winner's values always differ — fresh `now`).

## 5. Regression test design (REQ-2)

Add to `internal/idempotency/store_test.go` (which already carries the `//go:build integration` tag,
`package idempotency_test`, and the `testpg` pattern — E8):

`TestGormStore_Reserve_ConcurrentReclaimSingleWinner`:

1. **Setup**: `db := testpg.Start(t)`; `testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))`;
   `store := idempotency.NewGormStore(db)`.
2. **Reserve a fresh key** with a key unique to this invocation, e.g.
   `key := fmt.Sprintf("k-conc-%d", time.Now().UnixNano())`; assert `reserved == true`.
3. **Force-expire the row** via SQL, simulating a crashed leader:
   `db.Exec("UPDATE idempotency_keys SET expires_at = now() - interval '1 hour' WHERE uid = $1 AND endpoint = $2 AND idem_key = $3", uid, endpoint, key)`;
   assert no error and `RowsAffected == 1`.
4. **N = 8 concurrent `Reserve` calls** behind a start barrier (a `chan struct{}` all goroutines block on,
   then `close(start)`): N exceeds `testpg`'s `MaxOpenConns: 5`, so real read-then-reclaim interleaving is
   forced across pool slots. Each goroutine records `(rec, reserved, err)` into its own slice slot (safe:
   distinct indexes).
5. **Assert exactly one winner**: after `sync.WaitGroup` completion, count `reserved == true` calls and
   assert `winners == 1`; for every loser assert `err == nil`, `rec != nil`, and `rec.Completed == false`
   (in-flight semantics → middleware `409`).
6. **Winner reservation stays usable**: call `Complete` with a canned `201` response, then `Reserve` again
   and assert `reserved == false`, `rec.Completed == true`, `rec.Status == 201` (replay path intact).

**Determinism.** Under the fix, exactly-one winner holds for every interleaving (single-statement CAS, no
sleeps or production hooks). Under the buggy code, multiple winners occur whenever ≥ 2 reclaimers read the
stale row before either writes — with N = 8 behind a barrier and a 5-connection pool this is the expected
outcome. Optional hardening (if flakiness is ever suspected in a CI environment): run the scenario for ≥ 2
distinct keys and assert each round independently; per REQ-2's determinism note this must not involve sleeps
or production-code hooks. N ≥ 8, not the analysis draft's "≥ 2", per the normative spec.

**Gate self-check (must hold):** removing `AND expires_at = ?` from the reclaim `UPDATE` (reverting to the
pre-fix `Where("id = ? AND status = 0", …)`) makes the new test fail — proving the test pins the fix.

## 6. Migration and release steps

**No schema migration.** The fix uses only existing columns (`id`, `status`, `expires_at`, `created_at`);
`migrations/` is untouched and `make check-migration-set` is unaffected. `make migrate-up`/`cmd/migrate`
needs no change.

**Release steps:**

1. Implement Section 2.1 in `internal/idempotency/store.go`.
2. Add Section 5 test to `internal/idempotency/store_test.go` (additive; no existing test modified).
3. Run the gate list (Section 7).
4. Optional local sanity-revert of the CAS predicate to confirm the new test fails, then restore.

**Deployment.** API instances are stateless and horizontally scalable; deploy is rolling. During the overlap
window old binaries may still exhibit the double-claim race (pre-existing risk), while new binaries are
strictly more conservative (never return a spurious `reserved=true`). No cross-instance coordination is
required: the fix is purely a stricter single-statement CAS.

**Rollback.** Revert the single predicate change. No data migration, no schema drift: rows written under the
new code are indistinguishable from rows written under the old code (same columns, same lifecycle semantics).
In-flight reservations at the moment of deploy are unaffected — a stale `status = 0` row is reclaimed
correctly by either binary version.

**Observability.** No new metrics required. Reclaim outcomes are already observable through the middleware's
existing `409 idempotency.in_progress` responses; a reclaim win is the corresponding `reserved=true`
execution. Optionally, the 409 rate on a specific key can be monitored during an incident to confirm
single-winner reclaim behavior.

## 7. Testable acceptance mapping

| Requirement / acceptance criterion | Concrete testable check | Gate |
|---|---|---|
| REQ-1: reclaim is a single-winner CAS | `grep`/review: `store.go` reclaim `Where` contains `id = ? AND status = 0 AND expires_at = ?` with `existing.ExpiresAt`; guard `existing.Status == 0 && now.After(existing.ExpiresAt)` and `reservationTTL` unchanged; `store_redis.go` untouched | code review + `make lint` |
| REQ-2: regression test exists and is effective | `internal/idempotency/store_test.go` contains integration-tagged `TestGormStore_Reserve_ConcurrentReclaimSingleWinner` doing (a) reserve, (b) SQL force-expire (`expires_at = now() - interval '1 hour'`), (c) N = 8 concurrent `Reserve` behind a closed-channel barrier, (d) exactly-one `reserved=true`, losers `rec.Completed == false`, (e) winner reservation usable via `Complete` → replay | `make check-integration-compile` + `make test-integration` |
| Gate self-check | Removing `AND expires_at = ?` makes the new test fail (verify locally, then restore) | manual, pre-merge |
| Acceptance 3: gates green | `make test && make lint && make lint-strict && make check-file-sizes && make check-integration-compile` all pass; new file compiles under `go vet -tags integration` (part of `check-integration-compile`) | Makefile gates |
| Acceptance 4: real-Postgres gate green | `make test-integration` (`go test -tags=integration -p 1 ./... -count=1`, testcontainers Postgres) passes, including the new test | `make test-integration` (Docker) |
| No regressions to existing behavior | Existing `TestGormStore_ReserveCompleteReplayRelease` (and Redis tests) stay green; happy paths byte-for-byte unchanged | `make test` + `make test-integration` |
| Compatibility constraints (Section 3.3) | No migration file changed (`make check-migration-set`); no middleware/`internal/app` change; TTL constants unchanged | review + `make check-migration-set` |

## 8. Out of scope (explicitly deferred)

- **TTL divergence for expired completed records** (analysis finding #2): `GormStore` replays expired
  completed rows until the hourly `RunCleaner` purge while Redis auto-expires. The `Status == 0` guard is
  intentionally preserved; a follow-up direction may reclaim `status > 0` rows with the same CAS pattern.
- **Middleware retry-contract unit tests** (finding #3): `5xx → Release` and fail-open paths, with a
  scripted fake store.
- No changes to TTL constants, middleware, migrations, Redis backend, or `internal/app/new.go` wiring.
