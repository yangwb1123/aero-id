# Design — deterministic regression tests for the dead-letter replay interleavings (EPQ purge race, migration sweep, backlog > limit, cross-kind kill, crash windows)

> **Status: IMPLEMENTED (2026-08-08) as part of the remediation plan landing.** B1/B2 barrier
> primitives (§2) are used by `internal/repository/barrier_helpers_test.go`, the EPQ tests
> (`internal/repository/audit_publication_epq_integration_test.go`, `internal/outbox/purge_epq_integration_test.go`),
> the migration sweep (`migrations/migrations_test.go`), and the backlog/partial/crash CLI tests
> (`cmd/audit-replay/backlog_and_partial_test.go`). Superseded by the remediation plan: §6 T-4's
> "dry-run = full-backlog estimate" contract became the BOUNDED count (plan T3 — dry-run prints
> exactly the next run's prediction); the purge subqueries gained `FOR UPDATE SKIP LOCKED` (plan
> T4.2), which changes T-1's interleaving from "DELETE blocks on transactionid" to "DELETE skips
> the locked row" — the outer EPQ guard remains and is pinned by the same test; the migration
> sweep covers 59 pairs (000059 added).

## 0. Scope and validation note

This design specifies the regression tests that force the interleavings the current suite
passes probabilistically or never reaches. It covers the five requested deliverables:

1. **EPQ purge race**: purge `DELETE` in flight while a concurrent requeue tx holds the row
   lock — a barrier-driven EvalPlanQual recheck test proving a concurrently requeued row
   survives; the identical flaw in the outbox dead-letter leg.
2. **Migration sweep**: `migrations_test.go` beyond 000001–000003, so 000045's down is
   actually executed.
3. **Backlog > `-limit`**: dry-run count vs actual requeue count divergence and the
   second-run `requeued 100` case.
4. **Kill between `-kind both` legs**: cross-kind partial state invisible to single-kind
   checks.
5. **Crash windows**: mid-tx, after-commit-before-report, requeued-0 ambiguity.

Every barrier is a held transaction or a lock-state observation — no probabilistic sleeps.
Two of the mechanisms were **probed live against `postgres:16-alpine` on this tree before
writing this design** (probe files removed after validation; results in §2.4). The probes
confirmed both the red state (row deleted on current unguarded code) and the green state
(row survives as `pending` with the `callback_audit.go:191`-style outer guard), plus the
full 58-pair migration down-sweep passes in 5.3s.

## 1. What the current suite misses (gap map, all pins verified on head `13e4676`)

| # | Interleaving | Current coverage | Gap |
|---|---|---|---|
| 1a | Purge DELETE in flight while requeue commits | `TestRequeueVsPurgeInterleaving` (`audit_publication_replay_integration_test.go:509`) is strictly **sequential** (requeue-then-purge, purge-then-requeue); `TestConcurrentPurgeAndRequeueRace` (`:328`) races loops but the EPQ window (DELETE of ≤4 rows in microseconds vs a requeue commit with an in-tx fact append) is crossed only probabilistically | No test holds the requeue tx open across a purge DELETE; the guard at `callback_audit.go:189` is untested-by-necessity; the unguarded legs at `audit_publication_replay.go:152` and `outbox/repository.go:102-103` have **no** forcing test |
| 1b | Same flaw, outbox dead-letter leg | `internal/outbox` has **no purge race test at all** (only fake-repo cleaner tests in `repository_test.go` + 3 integration tests) | New integration test required; no live writer flips `dead_letter` today (see §4.1), so the concurrent writer is synthetic |
| 2 | Down migrations untested | `migrations/migrations_test.go:17-32` hardcodes only 000001–000003 up **and** down; `testpg.Migrate` applies ups only | 000045's down (and 53 other downs) never executed; probe: full sweep works, 58 pairs, 5.3s |
| 3 | Backlog > limit | `TestAuditReplayCLIRunTwiceIsIdempotent` (`cmd/audit-replay/main_test.go:133`) seeds 3 rows with `-limit 10` | Dry-run/run divergence (GORM `Count` ignores `LIMIT`; the CLI never loops) and the second-run `requeued 100` case unpinned |
| 4 | Kill between `-kind both` legs | `replay()` (`cmd/audit-replay/main.go:107-125`) commits publication and delivery legs in two transactions; no test covers a crash between them | Cross-kind partial state invisible to single-kind dry-runs unpinned |
| 5 | Crash windows | Rollback-on-append-failure is pinned (`TestRequeuePublicationFactAppendsInTx` unkeyed leg, `TestRequeueDeadRollsBackWithoutSigningKey`) but the **no-error abort** (process dies mid-tx), **after-commit-before-report**, and **requeued-0 ambiguity** are unpinned | §8 |

## 2. Deterministic barrier primitives

Three primitives. The only polling is `waitForLockPoll` — which observes a *guaranteed* DB
lock state, never synchronizes on time. Every goroutine signals via channels/`errCh`; the
main test goroutine asserts only after channel receives or `wg.Wait` (the existing
`TestConcurrentClaimPublicationPartition` tail-assertion convention).

### 2.1 B1 — trigger-held requeue tx (primary mechanism; real repo code path)

An `AFTER UPDATE` trigger on the target table blocks the requeue transaction **after** its
UPDATE (row lock held) and **before** COMMIT, by taking `pg_advisory_xact_lock` on a
test-chosen key. The test holds the matching **session** advisory lock, releases it when the
purge DELETE is provably blocked, and the requeue commits. Precedent: the advisory-lock
handshake in `TestAppendConcurrentDuplicateAcrossPartitions`
(`audit_consolidation_integration_test.go:43-110`) and `waitForLockPoll`
(`audit_consolidation_integration_test.go:18-27`, copied at `internal/app/audit_integration_test.go:169`).

```sql
CREATE FUNCTION hold_requeue() RETURNS trigger LANGUAGE plpgsql AS $$
  BEGIN PERFORM pg_advisory_xact_lock(hashtext('replay-hold-pub')); RETURN NEW; END $$;
CREATE TRIGGER trg_hold_requeue AFTER UPDATE ON audit_publications
  FOR EACH ROW WHEN (NEW.status = 'pending' AND OLD.status = 'dead_letter')
  EXECUTE FUNCTION hold_requeue();
```

Handshake (each step deterministic; `waitForLockPoll` deadlines are fail-fast only):

1. Seed exactly one dead_letter row via the public surface (`drivePublicationDeadLetter`),
   **then** create the trigger (fixture writes never fire it).
2. Test session: `SELECT pg_advisory_lock(hashtext('replay-hold-pub'))` on a dedicated
   `*sql.Conn` (defer unlock + close).
3. Goroutine R calls the **real** `RequeuePublication(ctx, nil, 0, fact)`; the UPDATE fires
   the trigger and R blocks holding the row lock, pre-append/pre-commit.
4. `waitForLockPoll(t, db, "advisory", 10s)` — fires **iff** the trigger ran, i.e. the
   UPDATE executed and the row lock is held. (Probe-verified.)
5. Goroutine P calls `PurgeDeadLetterPublications(ctx, now+1h, 10)`; the DELETE's subquery
   evaluates at statement snapshot (row still `dead_letter` → id captured), then the DELETE
   blocks on R's row lock.
6. `waitForLockPoll(t, db, "transactionid", 10s)` — proves the DELETE is **in flight and
   blocked**, the exact state the suite cannot reach today. (Probe-verified.)
7. Release the session advisory lock → trigger returns → fact appends in-tx → R commits →
   the DELETE's EPQ recheck evaluates the outer quals against the new `pending` version.
8. Assert (green contract): requeue returned 1, purge returned 0, row exists and is
   `pending`/`manual_replay`/attempt 0, one requeue fact in `audit_ledger`.

Why a trigger and not a repo commit-delay seam or raw-SQL replica: zero production diff,
exercises the exact GORM `Transaction` path, and the fire condition is a SQL fact (AFTER
UPDATE fires iff the UPDATE ran), not a code path that can drift. Rejected: `pg_sleep`
triggers (time-based, not barrier-based), a `beforeCommit` field on the repo structs
(production change for testability; the seam sits outside the SQL being proven), raw-SQL
requeue replica as the *only* mechanism (does not exercise `RequeuePublication`; kept as
B2 for the outbox leg where no real requeue exists).

### 2.2 B2 — held raw-conn writer (outbox leg, crash windows)

`sqlDB.Conn(ctx)` + explicit `BEGIN` / UPDATE / `COMMIT|ROLLBACK` on a dedicated backend —
the deterministic form of a crash. Used where the concurrent writer is synthetic (outbox,
§4.1) or where the test must control commit-vs-rollback (§8.1). The requeue UPDATE text is
verbatim `requeueReset()` / the outbox flip shape.

### 2.3 B3 — channel handshakes and cleanup

- Every goroutine reports through `errCh`/result channels; the test goroutine never calls
  `t.Fatal` from workers.
- `t.Cleanup`/`defer` releases advisory locks and closes conns, so a failed assertion can
  never wedge the per-test container (which is terminated anyway).
- The only `time.Sleep` in the suite stays inside `waitForLockPoll`'s 10ms observe loop —
  observation of a deterministic state, not synchronization.

### 2.4 Probe results (live, this tree, `postgres:16-alpine`)

| Probe | Result |
|---|---|
| Unguarded publication DELETE (`WHERE id IN (sub)`) under the held-requeue interleaving | DELETE blocked on `transactionid`; after commit the **row was deleted** (`status` gone) — the EPQ hole reproduces deterministically |
| Guarded DELETE (`WHERE id IN (sub) AND status='dead_letter'`) | row survived as `pending` |
| B1 trigger-held **real** `RequeuePublication` + purge | advisory wait observed, transactionid wait observed, requeue committed n=1, purge returned 1 (row deleted) on current code — red state proven through the real repo path |
| Full migration sweep (58 up ascending, 58 down descending) | PASS in 5.3s — no pre-existing down break; the sweep test design is green on this tree |

## 3. T-1 — publication-leg EPQ regression test (red-first, then the fix)

**Test**: `TestPurgeDeleteBlockedByRequeueCommitEPQ` in a new file
`internal/repository/audit_publication_epq_integration_test.go` (package `repository_test`,
integration tag), using B1. Assertions as in §2.1 step 8 — including the fact-in-ledger
check (`action='audit.publication.requeued'`, exactly 1 row) proving the append committed
atomically with the reset under the interleaving.

**Red-green order**: the test lands first and fails on the current tree (purge returns 1,
row gone). The one-line fix mirrors the delivery twin `callback_audit.go:189`:

```go
// internal/repository/audit_publication_replay.go:152 (current)
result := r.db.WithContext(ctx).Where("id IN (?)", sub).Delete(&auditPublicationRow{})
// fix
result := r.db.WithContext(ctx).Where("id IN (?) AND status = 'dead_letter'", sub).Delete(&auditPublicationRow{})
```

Under EPQ the outer status qual is rechecked against the new row version, so the requeued
(pending) row survives; `RowsAffected` stays authoritative. The subquery keeps the lease
and cutoff predicates — unchanged.

**Positive twin** `TestPurgeDeleteBlockedByRequeueCommitEPQDeliveryLeg` pins the
**guarded** reference leg (`PurgeDeadLetterDeliveries` + real `RequeueDead` + trigger on
`callback_deliveries`, WHEN clause `NEW.status='pending' AND OLD.status='dead_letter'`): row
survives, `requeued=1, purged=0`. This is the behavior the publication leg is being brought
up to; pinning it prevents the reference leg from regressing into the unguarded shape.

## 4. T-2 — outbox dead-letter leg and the four-leg guard matrix

### 4.1 Why the outbox writer is synthetic

No live code flips an outbox row out of `dead_letter` (`MarkFailed` sets it
`dead_letter = (retry_count + 1 >= ?)` and clears the claim in the same statement;
`MarkPublished`/`Release`/`RenewClaims` all require a held claim, which dead rows never
have — `internal/outbox/claim.go`). So the outbox dead-letter EPQ hazard is latent
defense-in-depth for the moment a requeue surface lands. The guard must still exist and be
pinned, exactly as the codebase already patterns it: `callback_audit.go:189` (guarded),
`outbox/repository.go:116` (guarded), and the two unguarded legs.

### 4.2 The matrix (all four purge legs)

| Leg | Site | Outer guard | Test |
|---|---|---|---|
| Publication purge | `audit_publication_replay.go:152` | ❌ missing | T-1 (red-first) |
| Delivery purge | `callback_audit.go:189` | ✅ `AND status = 'dead_letter'` | T-1 positive twin |
| Outbox dead-letter | `outbox/repository.go:102-103` | ❌ missing | T-2 (red-first) |
| Outbox published | `outbox/repository.go:116-117` | ✅ `AND published_at IS NOT NULL` | T-2 positive twin |

### 4.3 The tests

`internal/outbox/purge_epq_integration_test.go` (package `outbox_test`, integration tag):

- `TestOutboxPurgeDeleteBlockedByDeadLetterFlip`: seed one `outbox_events` row via raw
  insert (final schema per 000008+000018+000058: `retry_count`, `last_retry_at`,
  `dead_letter`, `claim_token`, `claimed_at`), `created_at` far past cutoff, `dead_letter=true`.
  B2 writer holds `UPDATE outbox_events SET dead_letter = false WHERE id = $1 AND dead_letter = true`
  (the exact shape a future outbox requeue must use). Purge goroutine calls the real
  `PurgeDeadLetters(ctx, farPast, farPast, 500)`; `waitForLockPoll("transactionid")`;
  COMMIT; assert: with the guard the row survives (`dead_letter=false`), purge returned 0
  and did **not** loop (first iteration deletes 0; the 100ms inter-batch pause never runs).
  Red-first: on current code the row is deleted.
- Fix (one line, mirrors the published leg's own guard at `outbox/repository.go:116`):
  `Where("id IN (?)", sub)` → `Where("id IN (?) AND dead_letter = true", sub)` at
  `internal/outbox/repository.go:102`.
- Positive twin `TestOutboxPurgeDeleteBlockedByPublishedFlip`: writer holds
  `UPDATE outbox_events SET published_at = NULL WHERE id = $1 AND published_at IS NOT NULL`
  (the hypothetical replay shape for the published leg) — pins that the existing guard
  keeps the row. Same for the *live* published-leg writer direction
  (`MarkPublished`-shaped `SET published_at = now()` can never touch a purged row because
  of the claim predicate — asserted as a comment, not a test).

## 5. T-3 — migration up/down sweep (000045's down actually executed)

Rewrite `migrations/migrations_test.go` (same file, same integration tag):

1. Discover all `^\d{6}_.*\.(up|down)\.sql$` entries; build the version → {up, down} map.
2. Assert **every version has both files** (58 pairs; orphan detection) and pair count == 58.
3. Apply ups ascending (same order as `testpg.Migrate` and `migrate.Up`), downs descending
   (exactly mirroring `migrate.Down`'s ordering at `internal/pkg/migrate/migrate.go:158`,
   `versions[i] > versions[j]`).
4. Assertions, on the same container: after ups — `audit_publications` exists (000045),
   `audit_ledger` exists (000034), plus the original three tables (files/messages/comments)
   to preserve the test's original purpose. After downs — all five gone, and specifically
   that 000045's down ran **before** 000034's down (reverse order is the FK-dependency-safe
   order; any violation fails the sweep).
5. The sweep is the contract test for every down, not just 000045's: a down that references
   an object already dropped by a higher-numbered down fails the test.

Validated by probe: the full sweep passes on this tree in 5.3s (58 up + 58 down on one
`postgres:16-alpine` container) — there is no pre-existing down break to work around.

## 6. T-4 — backlog > `-limit` (dry-run divergence and second-run `requeued 100`)

### 6.1 Contract being pinned (as-landed behavior, not a change)

- `CountDeadLetterPublications`/`CountDeadLetterDeliveries`: GORM `Count` strips ORDER BY
  and ignores LIMIT on the aggregate (one row), so the dry-run prints the **full backlog**.
- `RequeuePublication`/`RequeueDead` requeue at most `limit` rows (single batch; the CLI
  never loops — `cmd/audit-replay/main.go:107-125` calls each leg exactly once); `-limit 0` falls back to
  `replayDefaultLimit` = 100; there is no unbounded escape hatch.
- Therefore: `-dry-run` = estimate, run = bounded batch. The runbook's "second run prints
  requeued 0" (`docs/plans/2026-08-08-operator-runbook-stress-test.md` R2) is **wrong** for
  backlog > limit and must read "loop until the run prints requeued 0".

### 6.2 Tests (new file `cmd/audit-replay/backlog_and_partial_test.go`, integration tag)

- `TestAuditReplayCLIBacklogExceedsLimit` (publication leg; symmetric delivery variant):
  seed **120** dead publications (reuse `seedDeadLetterPublication`), then with
  `-limit 100`:
  - `-dry-run` prints `120 (dry run)` (the estimate divergence is pinned, not hidden);
  - run 1 prints `requeued 100 publication(s)`;
  - run 2 prints `requeued 20 publication(s)` — the corrected second-run contract;
  - run 3 prints `requeued 0 publication(s)` — the loop-until-zero terminal state;
  - with `-tenant` set, runs 1 and 2 mint **exactly one fact each** (2 total) — the fact
    gate is per mutating run, not per row;
  - `-limit 0` on the residual set requeues `min(remaining, 100)` — the default fallback.
- `TestAuditReplayCLIDryRunIsEstimateNotCount` (repo level, in
  `audit_publication_replay_integration_test.go`): seed 120; assert
  `CountDeadLetterPublications(nil, 100) == 120` while `RequeuePublication(nil, 100, ...)`
  returns 100 — the divergence at the API surface, with a comment documenting it as
  intended estimate semantics (bounded-count query or CLI looping are rejected here: they
  change operator-visible behavior and belong to a product decision, not a test task).

## 7. T-5 — kill between `-kind both` legs (cross-kind partial state)

### 7.1 The state

`replay()` (`cmd/audit-replay/main.go:107-125`) runs `replayPublications` then
`replayDeliveries` — **two independent transactions**. A crash between the legs leaves
publications `pending`/`manual_replay` and deliveries still `dead_letter`. No single-kind
check reports it: `-dry-run -kind publication` prints 0 (looks done), `-dry-run -kind
delivery` prints N (looks untouched). There is no per-run journal or cross-kind marker.

### 7.2 Deterministic simulation (no process kill)

`replay` is a pure function of its arguments; a kill between legs is exactly
"leg 1 executed, leg 2 never started". So the crash is simulated by calling
`replayPublications(...)` directly — the identical call `replay` makes — and then stopping.
A real `os/exec` kill of the binary would be slow, platform-dependent, and add nothing the
pure-call simulation does not already pin.

- `TestAuditReplayCrossKindPartialStateAfterLeg1Kill`: seed 3 dead publications + 2 dead
  deliveries; call `replayPublications(publications, nil, 100, "tenant-a", "audit-replay", false)`
  (leg 1) and **do not** run the delivery leg. Assert:
  - publications all `pending`/`manual_replay`, deliveries all `dead_letter` (the partial
    state is durable and exactly reachable);
  - invisibility: `-dry-run -kind publication` → `0`, `-dry-run -kind delivery` → `2`;
  - convergence: `replay(gdb, cfg, nil, "both", 100, "tenant-a", "audit-replay", false)`
    prints `requeued 0 publication(s)` + `requeued 2 delivery(ies)`;
  - audit accounting: exactly 1 publication fact + 1 delivery fact, none double-minted;
  - a second full `both` run prints `0`/`0` and mints nothing.
- `TestAuditReplayCrossKindPartialStateAfterLeg2Kill` (symmetric: delivery leg first, kill
  before the publication leg) and `TestAuditReplayKillBeforeAnyLeg` (empty state: both
  kinds untouched; a later `both` run requeues everything — the recovery path from the
  earliest crash point).
- The tests pin the discovery procedure the runbook's completion check already uses
  (per-kind SQL status counts), so the partial state is *detectable* even though no
  single-kind CLI output says so.

## 8. T-6 — crash windows

### 8.1 Mid-tx crash (no-error abort) — `TestRequeueCrashMidTxRollsBackByteIdentical`

The append-failure rollbacks are already pinned (`TestRequeuePublicationFactAppendsInTx`
unkeyed leg, `TestRequeueDeadRollsBackWithoutSigningKey`); the missing window is the
**process dies mid-tx with no in-tx error**. Postgres guarantees an aborted connection
rolls back identically to an explicit `ROLLBACK`, so the deterministic equivalent is:
B2 held raw conn, requeue UPDATE verbatim (`requeueReset()`), assert the row lock is held
(`waitForLockPoll("transactionid")` from a second conn attempting the same UPDATE or via
the B1 trigger), then `ROLLBACK`. Assert: the row is byte-identical to the pre-crash state
(dead_letter, original attempt, original error_code, cleared lease), zero new
`audit_ledger` rows for the intent, zero `audit_publications` rows. Run for both the
publication and delivery legs (delivery via `RequeueDead`'s reset shape).

### 8.2 After-commit-before-report — `TestRequeueCommitSurvivesMissingReport`

The requeue commits (row `pending`, fact durable) and the CLI dies before printing
`requeued N`. Deterministic form: call `RequeuePublication` (commits), then **do not
print**; assert the durable state equals what the report would have claimed (row pending +
fact present), then re-run through `replay()` → `requeued 0` — the operator recovery
(re-run) is safe and idempotent. Pin the disambiguation queries the runbook uses:
`SELECT count(*) FROM audit_publications WHERE status='pending' AND error_code='manual_replay'`
and `SELECT count(*) FROM audit_ledger WHERE action='audit.publication.requeued'` — both
must be non-zero here, proving "already done" rather than "never happened".

### 8.3 Requeued-0 ambiguity — `TestAuditReplayEmptyBacklogPrintsZeroWithoutFact`

Empty DLQ (0 seeded): `-kind both` mutating run prints `requeued 0 publication(s)` /
`requeued 0 delivery(ies)`, returns success, mints **zero** facts. This is
indistinguishable on stdout from "crash after full replay" — the test pins that the CLI
*does not* disambiguate, and documents the ledger/manual_replay queries as the only
disambiguation. A `-report` journal (per-run op id + counts, from the stress-test §8
hardening list) remains out of scope for this task and is noted in the runbook amendment.

## 9. Existing tests: what needs the new mechanism, and what only needs comment surgery

| Test | Verdict | Action |
|---|---|---|
| `TestConcurrentPurgeAndRequeueRace` (`audit_publication_replay_integration_test.go:328`) | **Passes probabilistically.** The race's partition assertion (`pending == requeued`) *would* catch the EPQ hole, but only when the DELETE straddles a requeue commit — a microseconds-vs-milliseconds window; `-race` is hygiene here, not mechanism (DB-level interleaving) | Keep as the aggregate partition stress; add a comment cross-referencing T-1 as the deterministic proof; **correct its doc comment at `:322-327`** — "both sides re-check the dead_letter status in their write" is false for the purge leg (the DELETE re-checks only via the outer qual; the fix in §3 makes it true) |
| `TestRequeueVsPurgeInterleaving` (`:509`) | Sequential; the in-flight order (requeue holds lock while the DELETE blocks) is never reached | Keep orders (a)/(b); the in-flight order (c) **is T-1** — cross-reference instead of duplicating |
| `TestPurgeDeadLetterPublicationsPredicate` (`:557`) | Sequential; fine | None |
| `TestAuditReplayCLIRunTwiceIsIdempotent` (`cmd/audit-replay/main_test.go:133`) | Seeds 3 ≤ limit 10; correct for its scope | None; the >limit cases are T-4 |
| `TestMigrationsUpAndDown` (`migrations/migrations_test.go:17-32`) | Hardcodes 3 of 58 pairs | Replaced by the T-3 sweep |
| Outbox purge | No race test exists | T-2 |

## 10. Landing order, file placement, gates

**Red-green order**: T-1 (publication) and T-2 (outbox) land first and fail on the current
tree; then the two one-line guards (`audit_publication_replay.go:152`,
`outbox/repository.go:102`); then green. T-3…T-6 and both positive twins land green
(behavior-pinning). The design doc's failure table row "Replay races purge"
(`docs/plans/2026-08-08-internal-app-dead-letter-replay-design.md:113`) is amended: the
"no row transitions twice" claim becomes "…guaranteed by the outer status qual (T-1)",
and the runbook stress doc's R2 wording is corrected per §6.1.

**Files** (all integration-tagged; each ≤ 500 lines, helpers ≤ 50 lines, cyclop ≤ 10):

- `internal/repository/barrier_helpers_test.go` (new, package `repository_test`): B1
  trigger install/uninstall, B2 held-conn helper, `waitForLockPoll` copy (the existing
  copies live in packages `repository` and `app`; per-package duplication is the
  established convention).
- `internal/repository/audit_publication_epq_integration_test.go` (new): T-1, T-1 delivery
  twin, §8.1 publication leg, §8.2 repo-level half.
- `internal/outbox/purge_epq_integration_test.go` (new, package `outbox_test`): T-2 pair.
- `migrations/migrations_test.go` (rewrite): T-3.
- `cmd/audit-replay/backlog_and_partial_test.go` (new): T-4 CLI, T-5 triple, §8.2/§8.3 CLI
  halves.
- `internal/repository/audit_publication_replay_integration_test.go`: §6.2 repo-level
  count-vs-run test; comment surgery at `:322-327`.

**Gates**: `make test-integration` (all new tests are integration-tagged),
`make check-integration-compile` (they must compile under `-tags integration` without
running — this is the gate the `waitForLockPoll`-in-`repository_test` addition would
break if the helper package were wrong), `make lint` / `make lint-strict`,
`make check-file-sizes`. `check-audit-swallow` and `check-anchored-event-ids` are
unaffected: test facts mint fresh v7 IDs via `idgen.NewID()` (the allowlisted C2
doctrine), and no production audit site changes.

## 11. Amendment register

- Line pins verified on head `13e4676` + working tree during this design:
  `audit_publication_replay.go:152` (unguarded), `callback_audit.go:189` (guarded),
  `outbox/repository.go:102-103` (unguarded) / `:116-117` (guarded),
  `migrations/migrations_test.go:17-32` (hardcoded 3 pairs), `migrate.Down` descending
  order at `internal/pkg/migrate/migrate.go:158`, CLI single-batch loop absence at
  `cmd/audit-replay/main.go:165` (one `RequeuePublication` call per leg), `replay` leg
  order at `main.go:107-125`,
  race-test comment at `audit_publication_replay_integration_test.go:322-327`.
- Probe findings recorded in §2.4; probe files removed after validation; `go vet -tags
  integration` clean on the affected packages afterwards.
- Design intent recorded (not a claim of shipped behavior): T-1/T-2 are red-first; the
  two-line guard fix is part of this task's landing, not of this design document alone.
