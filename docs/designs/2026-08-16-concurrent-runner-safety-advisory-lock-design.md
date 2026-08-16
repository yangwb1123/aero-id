# Design — Concurrent-Runner Safety for `internal/pkg/migrate` (session-level advisory lock around `Up()`/`Down()`)

Status: **Proposed** (implementation-ready)
Date: 2026-08-16
Module: `internal/pkg/migrate` (+ new sibling `advisory_lock.go`, + tests)
Spec: Requirements spec (evidence-backed) — "Concurrent-runner safety: advisory lock around Up/Down"
Baseline verified at: HEAD — every evidence claim re-verified against the live tree on 2026-08-16 (symbols, line ranges, key precedents, and gate constraints; see §1).

---

## 1. Verification verdict (untrusted evidence → tree)

| # | Claim (evidence) | Verdict | Tree evidence |
|---|---|---|---|
| E1 | `Up()` (~L112), `Down(n)` (~L155), `applyTx` (~L192) exist with the described behavior | **Confirmed** (minor line drift) | `internal/pkg/migrate/migrate.go:114` (`Up`), `:152` (`Down`), `:206` (`applyTx`). `Up()` calls `Discover` → `ensureTable` → `appliedVersions` → per-migration `applyTx`; `Down(n)` validates `n`, `Discover`, `ensureTable`, `appliedVersions`, rolls back highest-first; `applyTx` runs SQL + `schema_migrations` write in one `m.db.Transaction`. |
| E2 | No `pg_advisory_lock` anywhere in the package | **Confirmed** | `rg -n "advisory" internal/pkg/migrate/` → no matches. No serialization between the applied-set read and the DDL apply. |
| E3 | `internal/app/new.go:48-66` auto-migrate startup path runs `m.Up()` with no mutual exclusion | **Confirmed** (line drift) | `internal/app/new.go:44-52` — `if cfg.Database.AutoMigrate { … m := migrate.New(gdb, dir, …); applied, err := m.Up() … }`. A second stateless instance with `auto_migrate: true` races the same Postgres. |
| E4 | `internal/pkg/db/db.go` uses `gorm.io/driver/postgres`; advisory functions are available on the live handle | **Confirmed** | `internal/pkg/db/db.go:7` imports `gorm.io/driver/postgres`; `gorm.Open(postgres.Open(opts.DSN), …)`. |
| E5 | `internal/app/audit_integration_test.go:107-135` `pg_advisory_lock` precedent; comment documents the session-lock-leak hazard | **Confirmed** | `startBlockedAuditWriter` takes `SELECT pg_advisory_lock(hashtext('coverage-gate-writer'))` on a dedicated `*sql.Conn` (~L121), releases with `pg_advisory_unlock` (~L135); L110-111 documents: *"a session lock taken inside a transaction would survive COMMIT and leak"*. |
| E6 | Two-runner race: both `ensureTable()` idempotent, both read empty applied set, both apply the same migration → duplicate-object error | **Confirmed** | Code structure: `ensureTable` is `CREATE TABLE IF NOT EXISTS` (no serialization), `appliedVersions` reads committed state only; two interleaved `Up()` runs both see the same pending set and both `applyTx` the same DDL. |
| E7 | 59 up/down pairs in `migrations/`; concurrent-apply regression should use a small fixture dir | **Confirmed** | `ls migrations/*.up.sql | wc -l` → 59 (downs also 59). Fixture-dir approach is the only way to make "one applies N, other applies 0" exact. |
| E8 | `migrate_test.go` is `package migrate` (internal test package) → can assert unexported lock-key constant | **Confirmed** | `internal/pkg/migrate/migrate_test.go:1` `package migrate`; contains `TestParseDownArg`, `TestParseFilename`, `TestDiscover`, `TestDiscoverMissingDir` + `writeFiles` fixture helper. |
| E9 | `migrate_integration_test.go` is `//go:build integration` and boots real Postgres via `testpg.Start(t)` | **Confirmed** (package note) | `internal/pkg/migrate/migrate_integration_test.go:1-3` `//go:build integration` / `package migrate_test` (external test package); uses `testpg.Start(t)`, `testpg.RepoRoot(t)`, `migrate.Discover`. Integration tests therefore exercise only the **exported** API + SQL literals — compatible with A1–A3 (they never need the unexported key; they write the key literal directly into coordinator/try-lock SQL). |
| E10 | `cmd/migrate/main.go` dispatches `up`/`down` through the same `Migrator` → package-level lock covers operator/CI with no CLI change | **Confirmed** | `dispatch()` → `runUp`/`runDown` → `m.Up()`/`m.Down()`. |
| E11 | Advisory-key hygiene precedents: `'coverage-gate-writer'`, `'p-2'`, `hashtext(tenant/partition)` | **Confirmed** | `'coverage-gate-writer'` (`internal/app/audit_integration_test.go:121`); `'p-2'` (`internal/repository/audit_consolidation_integration_test.go:65`, session-lock coordinator in the duplication test); `hashtext(fact.PartitionKey)` (`internal/repository/audit_ledger.go:151`); `hashtext(projection.TenantID)` (`internal/repository/entitlement.go:67`). Migrator key must be namespaced — `"aero-id:migrate"` satisfies this. |
| E12 | `pg_locks` polling precedent (`locktype='advisory' AND granted=false AND pid <> pg_backend_pid()`) | **Confirmed** | `internal/repository/barrier_helpers_test.go:41-48` (`lockProbeQuery` case `"advisory"`). The migrate integration test needs its own local poll helper (that helper lives in `internal/repository`, not importable without a cross-package test dependency). |
| E13 | Test harness: `testpg.Start(t)` (testcontainers Postgres), `testpg.RepoRoot(t)`; `testpg.Migrate` exists and is out of scope | **Confirmed** | `internal/repository/testpg/testpg.go` — `Start` (L21), `Migrate` (L53), `RepoRoot` (L79). |
| E14 | `auto_migrate` default off in production config → production applies migrations explicitly via `cmd/migrate` | **Confirmed** | `configs/config.yaml:96` `auto_migrate: false` ("default off; explicit in prod"). |

**Evidence deltas found (content of the direction is unaffected):**

1. **`migrate.go` is 456 lines, not 283.** The spec's "283 lines today … must keep migrate.go ≤ 500 lines" premise is stale, but its conclusion is *stronger* than stated: `456 + ~35` (inline lock helper) would exceed 500, so the **sibling `advisory_lock.go` file is mandatory, not optional**. The ≤ 500 gate (`Makefile` `MAX_FILE_LINES=500`, `make check-file-sizes`) binds.
2. **Line-number drift, non-material:** `new.go` auto-migrate block is L44-52 (spec: 48-66); `applyTx` is L206 (spec: ~192). Behavior identical.
3. **Package of the integration test file** (spec §1 listed it only by tag/harness): it is `package migrate_test` (external). A1–A3 need no unexported access (they bind the key literal `'aero-id:migrate'` in SQL), so this is compatible; A4's unit test correctly belongs in `migrate_test.go` (`package migrate`), exactly as the spec prescribes.

**Overall verdict: all load-bearing claims verified; no material gap between the evidence and the tree.** The direction is implementable as specified.

---

## 2. Problem statement

`Up()` and `Down()` perform an unsynchronized read-modify-write cycle against `schema_migrations` plus DDL:

1. `ensureTable()` — idempotent `CREATE TABLE IF NOT EXISTS` (no serialization);
2. `appliedVersions()` — read of committed state;
3. per-migration `applyTx()` — DDL + `schema_migrations` write in one transaction.

Two stateless instances racing — two `auto_migrate` startups (`internal/app/new.go`), an operator plus CI (`cmd/migrate`), or a region cutover — both observe the same pending set and both apply the same migration. The loser hits a duplicate-object error on the second `CREATE TABLE`/`ALTER …` and reports a spurious failed run; DDL races the audit ledger and other tenants' schema objects. AGENTS.md's stateless/multi-instance contract ("only one active attempt may mutate a given operation step") applies, and the repository already uses `pg_advisory_lock` (`audit_integration_test.go:121`) and `pg_advisory_xact_lock(hashtext(…))` (`audit_ledger.go:151`, `entitlement.go:67`) elsewhere — the migrator itself has no lock.

The lock must be **session-level**, not transaction-scoped: `Up()`/`Down()` span multiple transactions (one per migration via `applyTx`), so `pg_advisory_xact_lock` would be released at the first `COMMIT` and re-open the race for every subsequent migration.

---

## 3. Design

### 3.1 New sibling file `internal/pkg/migrate/advisory_lock.go` (mandatory: migrate.go is 456 lines)

```go
package migrate

import (
	"context"
	"fmt"
)

// advisoryLockKey is the single, namespaced advisory-lock key serializing all
// Up/Down runs against one database. It is pinned by TestAdvisoryLockKeyStable:
// revaluing it during a rolling deployment would split contention across two
// keys and re-open the race, so the value is load-bearing. The prefix avoids
// collision with existing advisory keys ('coverage-gate-writer', 'p-2',
// tenant/partition hashtexts).
const advisoryLockKey = "aero-id:migrate"

// advisoryLockQuery builds the session-level acquire/release SQL from key so
// Up and Down provably contend on the package constant (never a divergent
// hardcoded string). Session-level by construction: pg_advisory_lock (not
// pg_advisory_xact_lock) survives transaction boundaries, which is required
// because Up/Down span one transaction per migration.
func advisoryLockQuery(key string) (acquire, release string) {
	return "SELECT pg_advisory_lock(hashtext('" + key + "'))",
		"SELECT pg_advisory_unlock(hashtext('" + key + "'))"
}

// withSessionLock runs fn while holding the package advisory lock on a
// dedicated *sql.Conn obtained from the GORM pool. The lock must live on a
// dedicated connection, never via m.db.Exec on a pooled connection: a session
// lock taken on a pooled connection that GORM returns to the pool survives and
// leaks (documented hazard, audit_integration_test.go:110-111). The dedicated
// connection is closed on every path — including error paths — and closing it
// releases the session lock, which makes release-on-error real.
func (m *Migrator) withSessionLock(ctx context.Context, fn func() error) (retErr error) {
	sqlDB, err := m.db.DB()
	if err != nil {
		return fmt.Errorf("migrate lock: sql handle: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate lock: acquire conn: %w", err)
	}
	// Close on every path, before the lock is even taken: a failed acquire
	// must not strand the connection. Closing also releases the session lock.
	defer func() { _ = conn.Close() }()

	acquire, release := advisoryLockQuery(advisoryLockKey)
	if _, err := conn.ExecContext(ctx, acquire); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	// Best-effort release: unlock failure is logged via m.logf and never masks
	// or changes the migration result; the deferred Close above releases the
	// session lock regardless.
	defer func() {
		if _, err := conn.ExecContext(context.Background(), release); err != nil {
			m.logf("release migration lock: %v", err)
		}
	}()

	return fn()
}
```

Key properties, each mandated by the spec:

- **One key constant, one acquire helper** — `advisoryLockKey` is the only key; `withSessionLock` is the only lock path; both `Up()` and `Down()` route through it. All instances contend on one key per database (`pg_locks.database` scope).
- **Session-level** — `pg_advisory_lock`/`pg_advisory_unlock`, never the xact variants; the lock survives each `applyTx` `COMMIT`.
- **Dedicated connection** — `sqlDB.Conn(ctx)`; acquire and release both execute on that same conn; `defer conn.Close()` registered *before* the acquire so an acquire failure still closes the conn; closing releases the lock even if the explicit unlock Exec fails.
- **Blocking semantics** — `pg_advisory_lock` blocks until free; deliberate (serialize rather than race, per AGENTS.md). A crashed holder's connection death auto-releases; no `pg_try_advisory_lock`, no timeout, no cancellation plumbing (non-goals).
- **Error style** — acquire failure wraps as `fmt.Errorf("acquire migration lock: %w", err)` (spec's package convention); unlock failure goes to `m.logf` and is never returned in place of the migration result.
- **Import set** — stdlib (`context`, `fmt`) + existing `gorm.io/gorm` only; no new dependencies (A5 / `check-architecture` safe; `internal/pkg/migrate` is not under the rule-(b) domain-purity or rule-(d) infra lists, but the minimal import set keeps `check-architecture` trivially green and preserves the package's dependency-free ethos).

### 3.2 Wiring into `Up()` (migrate.go:114) and `Down()` (migrate.go:152)

Both methods keep their existing signature and their existing *pure, DB-free* preamble, then wrap every DB-touching step in `withSessionLock`:

```go
func (m *Migrator) Up() ([]uint64, error) {
	migrations, err := Discover(m.dir) // pure disk validation, unchanged, fail-before-mutate
	if err != nil {
		return nil, err
	}
	var done []uint64
	err = m.withSessionLock(context.Background(), func() error {
		if err := m.ensureTable(); err != nil {
			return err
		}
		applied, err := m.appliedVersions()
		if err != nil {
			return err
		}
		for _, mig := range migrations {
			// … existing per-migration loop (applyTx etc.), appending to done …
		}
		return nil
	})
	return done, err
}
```

`Down(n)` mirrors: keep the `n <= 0` validation and `Discover` outside the lock (both are local/pure; the spec explicitly allows `n <= 0` to precede acquisition), then wrap `ensureTable` → `appliedVersions` → rollback loop in `withSessionLock`.

**Placement rationale** — the lock is acquired after `Discover` but before `ensureTable`/`appliedVersions`/`applyTx`, exactly the spec's binding entry-placement ("before ensureTable()/appliedVersions()/applyTx"). Keeping `Discover` outside the lock preserves fail-fast on broken migration sets without acquiring or holding the lock (a broken set never blocks other runners; a valid set blocks only at the lock). The A1 blocking-handshake test still observes the blocked advisory wait promptly because `Discover` on a small fixture completes in microseconds.

**Coverage note** — `Version()` and `Validate()` are *not* locked (spec R1/non-goals). Nuance for the record: `Version()` calls `ensureTable()` (an idempotent `CREATE TABLE IF NOT EXISTS`), which is technically a mutation but is race-safe by construction (`IF NOT EXISTS`); locking `Version()` would expand scope, so it stays unlocked. `Validate()` is fully read-only.

---

## 4. API changes

| Surface | Change |
|---|---|
| `Migrator.Up() ([]uint64, error)` | **None (signature)**. Behavior: now serialized with every other `Up`/`Down` on the same database; may block until the key is free. Return values unchanged. |
| `Migrator.Down(n int) ([]uint64, error)` | **None (signature)**. Behavior: serialized; `n <= 0` still errors before any lock/DB access. |
| `Migrator.Version()`, `Migrator.Validate()`, `Discover`, `parseFilename`, `ParseDownArg`, `Migration`, `ValidateResult` | **None** — untouched, unlocked. |
| `cmd/migrate` CLI, `internal/app/new.go` auto-migrate path | **None** — they call the same `Migrator`; the lock is inherited with zero call-site changes. |
| New unexported symbols | `advisoryLockKey` (`const`), `advisoryLockQuery` (`func`), `withSessionLock` (`method`) — package-private, no public API surface. |
| New tests | `TestAdvisoryLockKeyStable` (unit, `package migrate`, Docker-free); `TestUpBlocksWhileKeyHeld`/`TestDownBlocksWhileKeyHeld`/`TestLockReleasedAfterSuccess`/`TestLockReleasedOnError`/`TestConcurrentUpAppliesOnce`/`TestConcurrentDownRollsBackOnce` (integration, `package migrate_test`). |

There is no public API change; this is a purely internal behavioral hardening.

---

## 5. Compatibility constraints

1. **No signature, CLI, config, or env changes.** `Up()`/`Down()` signatures, `cmd/migrate` commands/flags, `database.auto_migrate`, DSN semantics — all unchanged.
2. **Single-runner behavior is byte-for-byte identical** modulo one dedicated `*sql.Conn` per run and two extra `SELECT pg_advisory_*` round trips. No change to migration semantics, ordering, `applyTx` transactions, error text (except the new "acquire migration lock: …" prefix for a genuinely new failure mode).
3. **Per-database scope is correct.** Advisory locks are scoped by `pg_locks.database`; every migrator instance targeting the same database contends on one key, which is exactly the serialization required. Different databases (regions, environments) never contend — consistent with AGENTS.md's "regional PostgreSQL plus home-region writes" (each region serializes its own migrations independently).
4. **Key stability is load-bearing.** `advisoryLockKey` must never be revalued; a change during a rolling deployment would split contention across two keys and re-open the race. Pinned by `TestAdvisoryLockKeyStable`.
5. **`testpg.Migrate` and the migration-set direction are untouched** (non-goals).
6. **File-size gate.** `advisory_lock.go` keeps `migrate.go` at 456 lines (≤ 500). New test files are exempt from `check-file-sizes`.
7. **New integration files must keep `make check-integration-compile` green** (tagged test files compile without running) and must not introduce imports outside stdlib + existing deps (`testpg`, `migrate`, `gorm`, `testing`, `time`, `sync`, `os`, `path/filepath`, `fmt`, `context`, `database/sql`).
8. **Lock-only-during-run.** The lock serializes the migration run, not steady-state traffic; zero effect on post-migration serving paths. No interaction with existing trigger/tenant advisory keys (distinct, namespaced).

---

## 6. Failure modes

| # | Mode | Behavior | Mitigation |
|---|---|---|---|
| F1 | Lock acquisition fails (DB down, conn pool exhausted, ctx canceled) | `Up()`/`Down()` return `acquire migration lock: …` (wrapped); **no migration applied** — fail-closed, same spirit as today's other DB errors. | None needed; error text is stable and debuggable. |
| F2 | A live-but-stuck holder (hung DDL, deadlocked apply) | Blocking acquisition has no timeout; other runners wait indefinitely. | By design (serialize rather than race). Postgres auto-releases session locks when the holder's connection dies; operator can `pg_terminate_backend(holder_pid)` (visible in `pg_locks`). |
| F3 | Crashed holder (process kill, pod eviction) | Connection death releases the session lock automatically; the key cannot wedge permanently. | `defer conn.Close()` on every path in the holder is the second line of defense for orderly exits. |
| F4 | Explicit unlock fails (conn broken after fn) | Unlock error is logged via `m.logf`, never returned in place of the migration result; `defer conn.Close()` releases the session lock anyway. | Design guarantees release; logging preserves observability. |
| F5 | Key collision with tenant/partition hashtexts | Astronomically unlikely (hashtext is 32-bit; keys are namespaced `'aero-id:migrate'` vs tenant-derived strings). Worst case is a stall (one side blocks), never corruption: session vs xact lock semantics mean both are advisory coordination, and no tenant code holds the session form. | Namespaced literal satisfies the hygiene requirement; collision would be deliberate. |
| F6 | Mid-run migration failure inside `applyTx` | `applyTx` rolls back DDL + version row atomically (Postgres transactional DDL); the lock is released on the error return path; a retrying runner re-applies the failed migration cleanly — no duplicate-object error, because the failed version is never recorded. | Existing per-migration transaction semantics unchanged (spec non-goal). |
| F7 | Concurrent `Up` + `Down` on the same key | Serialized in lock-acquisition order; a `Down` that wins re-exposes the top version to a later `Up`, which re-applies it — coherent, deterministic ordering. | Single shared key (R1). |
| F8 | Rolling deploy mixes old (unlocked) and new (locked) binaries | Only matters if `auto_migrate: true`; production default is off and migrations are applied explicitly via a single `cmd/migrate` operator, so the deploy window is inert. If `auto_migrate` is enabled, run the migration as a standalone step or accept the tiny pre-cutover race window (forward-only: old binaries hold no key and cannot wedge new ones). | Documented in §7 rollout; no code needed. |

---

## 7. Migration steps

**No data migration, no schema change, no config change** — this is code-only hardening inside the package. Sequence:

1. **Add `internal/pkg/migrate/advisory_lock.go`** (§3.1): constant, query builder, `withSessionLock`.
2. **Wrap `Up()` and `Down()`** bodies (post-`Discover`) in `withSessionLock(context.Background(), …)`, preserving `done`/error return shape exactly (§3.2). Do not touch `Version`/`Validate`/`Discover`/`applyTx`.
3. **Add unit test** `TestAdvisoryLockKeyStable` in `migrate_test.go` (`package migrate`, Docker-free) — A4.
4. **Add integration tests** in `migrate_integration_test.go` (`package migrate_test`, `//go:build integration`, `testpg.Start(t)`, local `waitForLockPoll`-style helper) — A1/A2/A3.
5. **Gates:** `make test && make lint && make lint-strict && make check-file-sizes && make check-migration-set && make check-architecture && make check-integration-compile && make build && make test-integration` (Docker required for the last).
6. **Rollout:** ship the new binary; `cmd/migrate` `up`/`down` and any `auto_migrate` instance on the new binary take the lock automatically. No operator action. With `auto_migrate: false` (production default), the rolling deploy is fully inert w.r.t. this change; with `auto_migrate: true`, prefer applying migrations via a single explicit step at deploy time (the lock now makes even that safe against accidental overlap).

---

## 8. Testable acceptance mapping

Fixture convention (both test packages): a small temp dir (`t.TempDir()` + `os.WriteFile`) with `000001_a.{up,down}.sql`, `000002_b.{up,down}.sql`, `000003_c.{up,down}.sql` (N=3), each `up.sql` creating a **distinct** table (`CREATE TABLE t_a …`, `t_b`, `t_c`) so duplicate-object failure is detectable, each `down.sql` dropping it. The unit-test `writeFiles` helper already exists in `package migrate`; the integration package gets an equivalent local helper (contents `SELECT 1;` suffice for A4-style fixtures; real DDL for the concurrency fixtures).

### A1 — Session-level lock acquired at entry, released on all return paths

| Case | Test (integration, `package migrate_test`) | Pre-fix behavior | Post-fix assertion |
|---|---|---|---|
| A1.1 blocking handshake (`Up`) | Coordinator `*sql.Conn` takes `SELECT pg_advisory_lock(hashtext('aero-id:migrate'))`; goroutine calls `Up()` on the fixture; poll `pg_locks` (`locktype='advisory' AND granted=false AND pid <> pg_backend_pid()`) until the blocked wait appears (local poll helper mirroring `barrier_helpers_test.go:41-48`, ~10s timeout); assert `Up()` has **not** completed while blocked; coordinator releases via `pg_advisory_unlock` on the same conn + closes it; assert `Up()` completes with all N applied, no error. | No lock taken → poll times out (blocked wait never appears) → **test fails** (regression proof). | Poll succeeds; blocked-then-completes; `len(applied)==N`; all N tables exist. |
| A1.1 blocking handshake (`Down(1)`) | Same shape on a fully-applied fixture: hold key, goroutine `Down(1)`, poll blocked, release, assert rollback of exactly the top version. | Same as above. | Blocked-then-completes; `len(rolled)==1`; `Version()==top−1`. |
| A1.2 release on success | After a completed `Up()` and a completed `Down(1)`, a fresh session's `pg_try_advisory_lock(hashtext('aero-id:migrate'))` returns `true` immediately (then unlock to clean up). | Would also pass today (no lock ever taken) — guard against the pooled-conn leak regression specifically. | `try_lock` returns true — the lock was not leaked onto a pooled connection. |
| A1.3 release on error (`Up`) | Fixture where `000002_b.up.sql` contains invalid SQL (e.g., `SELECT * FROM nonexistent_table_xyz;`) — still passes `Discover` (content is not validated). `Up()` applies migration 1, errors on 2. Immediately, `pg_try_advisory_lock` on the key succeeds. | Passes trivially today (no lock) — guards the new error path. | Error returned; `try_lock` succeeds; only version 1 recorded. |
| A1.3 release on error (`Down`) | Apply valid fixture fully; replace top `down.sql` with invalid SQL; `Down(1)` errors; `try_lock` succeeds. | Same. | Error returned; `try_lock` succeeds. |

### A2 — Concurrent `Up()` applies each migration exactly once

| Case | Test (integration) | Pre-fix behavior | Post-fix assertion |
|---|---|---|---|
| A2.1 deterministic core | Fresh container; coordinator holds the key; two goroutines call `Up()` behind a start barrier (`sync.WaitGroup`-style channel); poll `pg_locks` until **two** ungranted advisory waits on the key are visible (`count(*) >= 2`, `pid <> pg_backend_pid()`); coordinator releases. | Neither goroutine takes an advisory lock → the two-blocked-waits poll times out → **test fails deterministically** (regression proof). | Neither goroutine errors; exactly one returns `len==N`, the other `len==0`; `schema_migrations` has exactly N rows; `Version()==top`; all three tables exist. |
| A2.2 plain race | Second fresh container, no coordinator; two goroutines `Up()` behind the same start barrier. | Nondeterministic (may pass or hit duplicate-object) — the literal acceptance shape. | Same assertions as A2.1, deterministic. |

### A3 — Concurrent `Down()` serialization

| Test (integration) | Pre-fix behavior | Post-fix assertion |
|---|---|---|
| Fresh container; fixture fully applied to top (N=3); two goroutines `Down(1)` behind a start barrier. | Nondeterministic race (both may roll back; second sees version already gone and returns 0, or both read the same applied set and double-roll-back — `Down`'s second `applyTx` `DELETE` on a missing row is a no-op so it may "pass" vacuously; the serialization assertion is what matters). | No error from either; exactly one returns `len==1`, the other `len==0`; `Version()==top−1`. |

### A4 — Lock key stable / package-constant (unit, Docker-free, `package migrate`)

`TestAdvisoryLockKeyStable`:
1. `advisoryLockKey == "aero-id:migrate"` and non-empty — an accidental revalue fails the test (revalue during rolling deploy splits contention; stability is load-bearing).
2. `acquire, release := advisoryLockQuery(advisoryLockKey)`; assert both strings contain the key literal (`"aero-id:migrate"`), contain `pg_advisory_lock` / `pg_advisory_unlock` (never `pg_advisory_xact_*` — pins session-level semantics at the unit level), and use `hashtext`. Because `Up()`/`Down()` both route through `withSessionLock`, which builds its SQL from this constant via `advisoryLockQuery`, this proves both provably contend on one key.

### A5 — Gates

`make test`, `make test-integration`, `make lint`, `make lint-strict`, `make check-file-sizes`, `make check-integration-compile`, `make build` — all green. Structural notes: (a) `migrate.go` stays at 456 lines (the helper lives in `advisory_lock.go`, which also keeps total package lines tidy); (b) `advisory_lock.go` imports only stdlib (`context`, `fmt`) + the package's existing `gorm.io/gorm`; (c) integration test files compile under `-tags integration` (`make check-integration-compile`); (d) no new dependencies anywhere.

---

## 9. Non-goals (locked from the spec)

- No lock on `Version()`/`Validate()` (read-only, non-mutating; `Version()`'s `ensureTable` is `IF NOT EXISTS` race-safe).
- No changes to `Discover`/`parseFilename` strictness, the migration filename contract, or `testpg.Migrate`.
- No checksum/content-integrity verification or `verify` subcommand.
- No `cmd/migrate` CLI, config key, env override, or `Up()`/`Down()` signature changes.
- No lock timeout, `pg_try_advisory_lock` fail-fast mode, or cancellation plumbing — blocking session-level lock with defer release only.
- No changes to `applyTx`'s per-migration transaction semantics.

## 10. Risks

- **Blocking-without-timeout** is deliberate but means a live-but-stuck holder stalls migration runs; connection death auto-releases, and `pg_locks`/`pg_terminate_backend` give the operator an escape hatch. Matches the spec's session-level mandate and AGENTS.md's pause-rather-than-split-brain stance.
- **Key stability** is the only correctness-adjacent constant; pinned by A4.
- **hashtext 32-bit collision space** is shared with existing tenant/partition keys; the namespaced literal makes meaningful collision require deliberate action, and a collision stalls rather than corrupts.
- The change is **forward-only** for rolling deploys: old binaries simply don't hold the key; production `auto_migrate: false` makes the window inert.
