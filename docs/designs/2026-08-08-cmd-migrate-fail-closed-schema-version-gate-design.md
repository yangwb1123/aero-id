# Design — module `cmd/migrate`: fail-closed schema-version gate for the B2 operational surface

| | |
|---|---|
| Source spec | `docs/requirements/2026-08-08-cmd-migrate-fail-closed-schema-version-gate.md` (REQ-1..REQ-5, acceptance T-5/T-10.1/T-4) |
| Design status | **Proposed** — concrete API, call sites, tests, rollout |
| Evidence status | Every claim in the requirements doc re-verified against head `b005409` this pass; two counting nuances recorded below (§0.1) |
| File-size budget | `internal/pkg/migrate/migrate.go` 283→~330; `cmd/migrate/main.go` 138→~150; `cmd/audit-replay/main.go` 357→~361; `internal/app/audit.go` 245→~250 — all ≪ 500 (`make check-file-sizes`) |

## 0. Evidence verification (untrusted claims → verified facts)

### 0.1 Claims verified exactly

| Claim | Result |
|---|---|
| `schema_migrations` referenced only in `cmd/migrate/main.go` + `internal/pkg/migrate/migrate.go` (`migrationsTable` :23, `Version()` :79) | ✅ exact (repo-wide `rg -l schema_migrations --type go` = those two files) |
| `cmd/audit-replay/main.go` `replay()` :141-177 queries B2 tables immediately, no version check | ✅ exact (`func replay` at :141; `run()` at :76; B2 queries `CountDeadLetterPublications` :197, `RequeuePublication` :215, `CountDeadLetterDeliveries` :230, `RequeueDead` :240) |
| `internal/app/audit.go` `buildAuditStack` :34-66 no version check; `validateAuditLedgerConfig` :23-30; `validateAuditBindingCoverage` :165 | ✅ exact |
| Composition root `new.go:131` → `buildAuditStack`; drain `callbackSvc.Start` at `new.go:168` | ✅ exact |
| Migrations 000045/000059 exist; 000059 latest on-disk; 000059 adds `idx_audit_publications_purge` + `idx_callback_deliveries_purge` | ✅ exact |
| T-4 tests exist: `migrations/migrations_test.go:34` `TestMigrationsUpAndDown` (raw-SQL sweep, never reads `schema_migrations`); `cmd/audit-replay/main_test.go:137` `TestAuditReplayCLIRunTwiceIsIdempotent` (via `runCapture` harness :74, which drives `replay()` directly) | ✅ exact |
| `check-audit-swallow` scans `cmd/` | ✅ `Makefile:67-69` — roots `internal/service internal/repository internal/app internal/storage internal/block cmd` |
| A1 drift: `cmd/migrate/main.go:71-79` is the `db.Open` block, not the version command; `case "version":` at :118-125 | ✅ confirmed: `db.Open` at :67, `case "version":` at :123 |
| A4: `testpg.Migrate` (`internal/repository/testpg/testpg.go:53-74`) executes raw `*.up.sql` lexically, never records `schema_migrations` | ✅ exact (function :53; pure `db.Exec` loop, no bookkeeping) |
| No test combines `testpg.Migrate` with the `Migrator` | ✅ only `migrate.New` outside `cmd/migrate` is the composition root `internal/app/new.go:56` (auto-migrate), not a test |
| Integration precedents: `internal/pkg/migrate/migrate_integration_test.go:16` `TestMigratorUpAndDown`; `internal/app/audit_integration_test.go:317` `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain`, `:743` `TestBuildAuditStackHonorsPurgeKnobs` | ✅ exact |
| Existing outage precedent `TestAuditReplayDBOutageFailsClosed` (`cmd/audit-replay/main_test.go:392`) asserts only non-nil errors | ✅ exact — the gate changes the error *text* (see §4.6) but cannot break the assertion |
| Line counts 138 / 283 / 357 / 245 | ✅ all exact |

### 0.2 Nuances found (doc wording, not substance)

1. **"36 call sites" is loose.** Real `testpg.Migrate(...)` invocations: **66 across 36 test files**. The 37th grep hit (`migrations/migrations_test.go:52`) is a *comment* ("same order as testpg.Migrate and migrate.Up"). The requirements doc's "36 test files" is accurate; the harness change scope (REQ-4.1) is 36 files / 66 invocations. The load-bearing conclusion — a version-based gate reads version 0 on every harness-migrated DB — is unaffected.
2. **A1's `:118-125`**: the `case "version":` body is `:123-130`; the switch begins ~:117. Cosmetic; the amendment register's correction direction is right.

### 0.3 Accepted design decisions inherited from the spec (re-stated, load-bearing)

- **A3**: `(dry-run prints 0 counts)` is pinned as **abort-before-counting** — the gate fires in `-dry-run` mode before any count query; count lines never print; zero rows/facts by construction. The literal "proceed and print 0" reading is rejected (it would count on a schema that cannot be counted = the fail-open shape this direction closes).
- **Gate semantics**: version-based, not existence-based (`schema_migrations` is the source of truth, not table presence); read-only (never applies migrations); fail-closed on missing bookkeeping (missing table ⇒ version 0 ⇒ fires).
- **Scope boundary**: sibling direction 2 (comprehensive `cmd/migrate` CLI test coverage) and direction 3 (DDL-shape pins) remain out of scope. Only the `verify` path gets a pin.

## 1. API changes

### 1.1 `internal/pkg/migrate` — new exported surface (the gate core)

```go
// MinB2SurfaceVersion is the schema version at which the B2 operational
// surface is complete: migrations/000059_add_dead_letter_purge_indexes.
// Bump to the newest B2 migration version whenever a later B2 migration
// lands (maintenance contract, §6).
const MinB2SurfaceVersion uint64 = 59

// b2SurfaceVersionMessage is the single actionable-gate message formatter.
// Pure (unit-testable without a DB); renders zero-padded versions and keeps
// the remediation literal stable so both gate sites and operator tooling can
// grep for it. Contract: contains "migrate up" and the %06d threshold.
func b2SurfaceVersionMessage(got, min uint64) string {
    return fmt.Sprintf("database schema at version %06d is below the required "+
        "B2 surface version %06d — run 'migrate up' (make migrate-up) before starting",
        got, min)
}

// RequireVersion fails closed when the applied schema version is below min.
// It reads schema_migrations through the same path as Migrator.Version()
// (idempotent CREATE TABLE IF NOT EXISTS + MAX(version)) and never applies
// migrations. A database with no bookkeeping rows reads version 0 and fails.
func RequireVersion(db *gorm.DB, min uint64) error {
    v, err := currentVersion(db)
    if err != nil {
        return fmt.Errorf("check schema version: %w", err) // fail-closed wrap
    }
    if v < min {
        return errors.New(b2SurfaceVersionMessage(v, min))
    }
    return nil
}

// currentVersion is Version()'s read extracted so Migrator.Version(),
// RequireVersion, and cmd/migrate verify share one implementation.
func currentVersion(db *gorm.DB) (uint64, error)

// EnsureSchemaMigrations is ensureTable()'s public form (same idempotent DDL)
// so the integration-test harness records bookkeeping without duplicating DDL.
func EnsureSchemaMigrations(db *gorm.DB) error
```

Internal refactor: `Migrator.Version()` (:79-91) becomes a one-line delegate to `currentVersion(m.db)`; `ensureTable` becomes a wrapper over `EnsureSchemaMigrations`. **No behavior change** to `Version()`, `Up()`, `Down()`.

The returned error type is a plain `error` (no sentinel needed — every call site fails closed on non-nil; the message is the contract). `RequireVersion` returns only `error`; `verify` prints via `Migrator.Version()` first (§1.4) — two trivial indexed MAX reads, no API awkwardness.

### 1.2 `cmd/audit-replay/main.go` — gate first in `replay()`

`replay()` (:141) gains, as its first statements after the `limit` default:

```go
if err := migrate.RequireVersion(gdb, migrate.MinB2SurfaceVersion); err != nil {
    return err
}
```

- Runs **before** ledger construction, `buildReplayIntents`, and every count/requeue query.
- Covers `-dry-run` and mutating modes alike (the gate precedes the dry-run branch).
- Covers both CLI paths: `run()` → `replay()` (exit 1 via the `audit-replay: ` prefix) and the `runCapture` test harness (drives `replay()` directly).
- New import: `github.com/aero/aero-id/internal/pkg/migrate` (no cycle: migrate imports only gorm + stdlib; audit-replay already imports `internal/pkg/db`, `internal/pkg/idgen`).

### 1.3 `internal/app/audit.go` — boot gate in `buildAuditStack`

`buildAuditStack` (:34-66) gains, immediately after `validateAuditLedgerConfig` and **before** `buildAuditPublishers`:

```go
if err := validateAuditLedgerConfig(cfg); err != nil {
    return nil, nil, nil, err
}
if err := migrate.RequireVersion(gdb, migrate.MinB2SurfaceVersion); err != nil {
    return nil, nil, nil, err
}
```

- Ordering is load-bearing: `buildAuditPublishers` → `validateAuditBindingCoverage` (:120, :165) queries `accounts`/`audit_publications`/`audit_ledger` and fails with a **raw SQL error** on an unmigrated DB; the gate must run first so the actionable message wins (REQ-3.3).
- Placement makes the failure structural: the composition root `internal/app/new.go:131` returns the error, so `callbackSvc.Start` (:168) is never reached — no drain tick, no fact accepted.
- The `internal/app/new.go:56` auto-migrate block runs earlier in `New()`; with `AutoMigrate=true` the schema is already current when the gate runs (no interaction); with the default `false`, the gate enforces the operator ran `make migrate-up`.
- New import: `internal/pkg/migrate` — no cycle (`internal/pkg/migrate` is leaf; `internal/app` already imports leaf packages `internal/config`, `internal/pkg/db`).

### 1.4 `cmd/migrate/main.go` — new `verify` subcommand

In `run()`'s dispatch switch, after `case "version":`:

```go
case "verify":
    v, err := m.Version()
    if err != nil {
        return err
    }
    if v == 0 {
        fmt.Println("no migrations applied (version 0)")
    } else {
        fmt.Printf("%06d\n", v)
    }
    if err := migrate.RequireVersion(gdb, migrate.MinB2SurfaceVersion); err != nil {
        return err
    }
    return nil
```

- Exit 0 at/above 59; exit 1 below with the actionable message (via `run()`'s existing `migrate: ` prefix + `os.Exit(1)` — no new exit machinery).
- Output mirrors `version` exactly, then gates. Reuses `m.Version()` (same query as `RequireVersion`'s — a concurrent `migrate up` between the two reads yields "print 58, gate passes at 59", which is correct: the gate is the last word).
- `usage()` gains `verify        check the schema meets the required B2 surface version (exit non-zero below it)`.
- New test file `cmd/migrate/verify_integration_test.go` — the module's first test file.

### 1.5 `internal/repository/testpg/testpg.go` — harness bookkeeping (REQ-4.1, enabling)

`testpg.Migrate` (:53-74) is reworked to stay execution-identical while recording versions:

```go
func Migrate(t *testing.T, db *gorm.DB, migrationsDir string) {
    t.Helper()
    if err := migrate.EnsureSchemaMigrations(db); err != nil {
        t.Fatalf("ensure schema_migrations: %v", err)
    }
    migrations, err := migrate.Discover(migrationsDir) // ascending version order
    if err != nil {
        t.Fatalf("discover migrations: %v", err)
    }
    // Naming-contract guard: today the raw sweep executes every *.up.sql;
    // Discover must recognize them all or the harness would silently skip.
    for _, mig := range migrations {
        if mig.UpFile == "" {
            t.Fatalf("migration %06d_%s has no .up.sql file", mig.Version, mig.Name)
        }
        b, err := os.ReadFile(mig.UpFile)
        if err != nil {
            t.Fatalf("read %s: %v", mig.UpFile, err)
        }
        if err := db.Exec(string(b)).Error; err != nil {
            t.Fatalf("exec %s: %v", mig.UpFile, err)
        }
        if err := db.Exec(
            "INSERT INTO schema_migrations (version, name) VALUES (?, ?) ON CONFLICT (version) DO NOTHING",
            mig.Version, mig.Name,
        ).Error; err != nil {
            t.Fatalf("record %s: %v", mig.UpFile, err)
        }
    }
}
```

- Execution order is provably identical: `Discover` sorts ascending by numeric version; the old harness sorted zero-padded filenames lexically — identical for the `NNNNNN_` contract (and `migrations_test.go:31` `migrationFileRE` already polices the contract).
- Raw `db.Exec` is retained (no behavior change to what SQL runs); only the bookkeeping insert is added, mirroring `Migrator.applyTx`'s record step (`ON CONFLICT DO NOTHING` for idempotence).
- New import: `internal/pkg/migrate` — no cycle (`testpg` is a test helper package; migrate is a leaf).
- Guard note: `Discover` ignores non-conforming files; the `UpFile == ""` check only catches missing up-files, not non-conforming extras. To keep "execute everything the old harness executed", the design adds one loud check: `*.up.sql` entries under `migrationsDir` that `Discover` did not return cause `t.Fatalf` (the naming contract is already load-bearing for `Migrator.Up`, so this cannot break a legitimate migration layout). All 66 invocations across 36 files then boot at version 59.
- No production code path depends on this change; it exists solely to keep acceptance T-4 honest.

## 2. Compatibility constraints

1. **No migration files change.** `migrations/*.sql` untouched; `TestMigrationsUpAndDown` (raw sweep) is structurally unaffected — it never reads `schema_migrations`.
2. **No behavior change on a migrated schema (≥ 59).** The gate is a no-op: audit-replay, boot, drain, `migrate up/down/version` behave exactly as today. Gate sites are 2-3 lines each.
3. **No new config keys, no HTTP/gRPC/proto surface.** The only CLI addition is the additive `verify` subcommand (unknown commands still error; existing commands byte-identical).
4. **No auto-migration, no fail-open.** `RequireVersion` never applies migrations; a missing `schema_migrations` table ⇒ `EnsureSchemaMigrations` recreates it empty ⇒ version 0 ⇒ gate fires. Dropping the bookkeeping table cannot bypass the gate (it self-heals to "unmigrated").
5. **Error-text contract is load-bearing.** One format function produces the message; tests grep for `migrate up` and `000059` and assert absence of `relation`/`does not exist`. Both gate sites share it ⇒ T-10.1's "the same actionable error" holds by construction.
6. **Import graph stays acyclic.** `internal/pkg/migrate` imports only gorm + stdlib; new edges `cmd/audit-replay → migrate`, `internal/app → migrate`, `internal/repository/testpg → migrate` introduce no cycles.
7. **Harness equivalence.** `testpg.Migrate` remains execution-identical; the bookkeeping insert cannot collide with any existing test (verified: no test combines `testpg.Migrate` with `Migrator.Up/Down`; the only other `migrate.New` is the `new.go:56` auto-migrate, which becomes a no-op instead of re-applying idempotent SQL — safe because today's double-application already proves every migration idempotent under test).
8. **File-size budgets** (§ header) all well under 500.
9. **Existing outage behavior preserved in kind.** `TestAuditReplayDBOutageFailsClosed` asserts only non-nil errors; the gate's `check schema version: …` wrap is still non-nil and still mutates nothing (§4.6).

## 3. Design decisions and rejected alternatives

| Decision | Choice | Rejected |
|---|---|---|
| Gate shape | Free function `migrate.RequireVersion(db, min)` + shared constant `MinB2SurfaceVersion` | Method on `*Migrator` (forces callers to construct a dir-carrying Migrator for a dir-less read) |
| Threshold | Single constant 59 (000059 = last B2 migration on disk) | Per-site literals (drift risk between replay tool and boot check) |
| Gate behavior | Read-only; fail-closed on missing/empty bookkeeping | Auto-migrate (violates AGENTS.md "not auto-applied by default"); fail-open skip (the exact shape this direction closes) |
| Message | One pure formatter, `%06d` versions, literals `migrate up` + `000059` | Inline `fmt.Errorf` at each site (drift between sites, untestable without DB) |
| audit-replay placement | First statement of `replay()` | In `run()` after `db.Open` (would not cover the `runCapture` harness; would duplicate the check for each leg) |
| Boot placement | `buildAuditStack`, after key validation, before `buildAuditPublishers` | In the drain `Start` (too late: a tick may already run; not structurally testable) |
| Harness bookkeeping | `testpg.Migrate` records via `migrate.Discover` + raw exec + `ON CONFLICT DO NOTHING` | Pointing 36 test files at `Migrator.Up()` (rewrites every harness call site; changes error surfaces); duplicating DDL text in testpg (drift risk) |
| `RequireVersion` return | `error` only; `verify` prints via `Version()` | Returning the version (awkward at the two gate sites) |
| Outage wrap | `check schema version: %w` | Raw DB error (loses context); custom sentinel (nothing distinguishes it) |

## 4. Failure modes

| # | Failure | Behavior after this design | Closed by |
|---|---|---|---|
| 4.1 | Deployment skipped `make migrate-up`; operator runs audit-replay (fresh DB) | `replay()` gate fires before any B2 query: exit 1, message names `migrate up` + `000059`, zero rows requeued, zero facts minted, no count lines, no raw `relation … does not exist` text. In `-dry-run` mode identically (A3 pin) | REQ-2.1 |
| 4.2 | Deployment skipped `make migrate-up`; server boot with ledger enabled | `buildAuditStack` fails with the same actionable message; `new.go:131` aborts; `callbackSvc.Start` (:168) never runs; no tick; no fact accepted | REQ-3.1–3.4 |
| 4.3 | Partial rollback (`down` past 000059, or past 000045) | Version-based gate fires although some B2 tables exist — `schema_migrations` is the source of truth, not table presence | REQ-2.2, REQ-3.2 |
| 4.4 | Governance enabled on an unmigrated DB | Ordering pin: version gate precedes `validateAuditBindingCoverage`'s raw-SQL failure; operator sees the actionable message | REQ-3.3 |
| 4.5 | `schema_migrations` dropped or truncated in prod | `EnsureSchemaMigrations` recreates it empty ⇒ version 0 ⇒ gate fires (fail-closed; cannot be bypassed by deleting the table) | REQ-1, §2.4 |
| 4.6 | DB unreachable / pool closed at replay or boot | `RequireVersion` returns `check schema version: <wrapped raw>` — non-nil, exit 1, nothing mutated. `TestAuditReplayDBOutageFailsClosed` (non-nil assertion) stays green; the error *text* changes from the count-query error to the gate wrap, which is strictly clearer | §0.1, REQ-2 |
| 4.7 | Concurrent `migrate up` while a gate reads | Transient spurious fire on a stale read; operator re-runs; read-only gate ⇒ no locks, no corruption, no split state | REQ-1 |
| 4.8 | Gate mis-fires on every integration test (harness records no versions) | Closed by REQ-4.1: 36 files / 66 invocations boot at version 59 | REQ-4.1 |
| 4.9 | Threshold drift (future migration 000060 lands; constant not bumped) | Gate stays at 59: weaker (surface beyond 59 unchecked) but never wrong; maintenance contract §6 makes the bump duty explicit | §6 |
| 4.10 | Message regression (raw SQL leak, lost literals) | Pinned: REQ-1.1 unit pins + REQ-2/REQ-3 negative assertions (`relation`/`does not exist` absent) | REQ-1.1, REQ-2.1, REQ-3.1 |
| 4.11 | Operator forges a version-59 bookkeeping row without applying SQL (bypass) | Gate passes; raw table errors return as today. **Accepted limitation**: this is a deployment-order check, not a tamper-proof integrity chain (that is the audit-chain-integrity hardening direction's scope). Documented, not silently claimed | §6 (documented) |

## 5. Migration steps (change rollout)

**No SQL migration is introduced.** The rollout is code + test-infra only:

1. **Phase 1 — gate core** (`internal/pkg/migrate/migrate.go`): add `MinB2SurfaceVersion`, `b2SurfaceVersionMessage`, `RequireVersion`, `currentVersion`, `EnsureSchemaMigrations`; refactor `Version()`/`ensureTable` to delegate. Unit tests: message pins (REQ-1.1). Integration test: 58/59 boundary (REQ-1.2).
2. **Phase 2 — harness bookkeeping** (`internal/repository/testpg/testpg.go`): REQ-4.1. Land in the same commit as Phase 1 (nothing reads the bookkeeping yet; `make test-integration` must be run because the shared harness changed — AGENTS.md).
3. **Phase 3 — replay gate** (`cmd/audit-replay/main.go`): wire `replay()`; add REQ-2 tests.
4. **Phase 4 — boot gate** (`internal/app/audit.go`): wire `buildAuditStack`; add REQ-3 tests.
5. **Phase 5 — CLI surface** (`cmd/migrate/main.go`): `verify` subcommand + usage line; add `cmd/migrate/verify_integration_test.go` (REQ-5).
6. **Phase 6 — full done-gate** (section 7 of the spec):

```text
make test && make lint && make lint-strict && make check-file-sizes \
  && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest \
  && make check-integration-compile && make build
make test-integration        # mandatory: shared harness changed (REQ-4.1)
```

**Deployment sequencing:** ship with the app binary; no DB migration, no config change. Fresh environments: `make migrate-up` before `cmd/server` or `audit-replay` — now enforced with an actionable message instead of raw SQL errors. Existing environments at ≥ 59: no-op. Rollback: revert the binary; no schema artifacts to un-migrate.

## 6. Maintenance contract

- **Bump duty:** the author of any future B2 migration (e.g. 000060) must bump `MinB2SurfaceVersion` to the new version in the same commit. The constant sits in the schema-state owner next to `migrationsTable`, and the message renders it via `%06d` so `000060` propagates automatically.
- **Message stability:** never edit `b2SurfaceVersionMessage` without updating the REQ-1.1 unit pins; `migrate up` and the threshold literal are operator-facing grep contracts.
- **Bypass limitation (4.11)** and **dry-run pin (A3)** are documented decisions, not silent behavior.

## 7. Testable acceptance mapping

Supplied acceptance (direction) → requirement → concrete test pins:

### T-5 → REQ-2 → new tests in `cmd/audit-replay/main_test.go`

| Criterion | Test (proposed name) | Assertions |
|---|---|---|
| Fresh-DB leg, dry-run | `TestAuditReplayGateFailsClosedFreshDB` | `testpg.Start` with **no** `testpg.Migrate`; `runCapture(t, db, cfg, "-kind", "publication", "-dry-run")` → err non-nil, text contains `migrate up` and `000059`; text does **not** contain `relation`/`does not exist`; captured stdout contains no `to requeue` count lines |
| Fresh-DB leg, mutating | same test, second leg (or `TestAuditReplayGateFailsClosedMutating`) | `runCapture(..., "-kind", "publication", "-limit", "10", "-actor", "ops-run")` → same error (actor validation passes first in `parseFlags`; gate fires in `replay`) |
| Version-58 leg (tables present) | `TestAuditReplayGateFailsClosedVersion58` | `testpg.Migrate` then `DELETE FROM schema_migrations WHERE version = 59`; seed one dead-letter publication via `seedDeadLetterPublication`; mutating `replay()` → err contains `migrate up`; seeded row still `status = 'dead_letter'`; `SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'` = 0. **Proves version-based, not existence-based; zero mutation; zero facts** |
| Version-59 control | existing `TestAuditReplayCLIRunTwiceIsIdempotent` (T-4.2) | unchanged behavior — green via REQ-4.1 (harness boots at 59) |

### T-10.1 → REQ-3 → new tests in `internal/app/audit_integration_test.go`

| Criterion | Test (proposed name) | Assertions |
|---|---|---|
| Unmigrated leg | `TestBuildAuditStackGateFailsClosedFreshDB` | `testpg.Start` no migrate; `buildAuditStack(cfg, db, metrics)` with signing key set → err non-nil whose text **equals** the REQ-2 message (same `RequireVersion` formatter — identical by construction), contains `migrate up`, no `relation`/`does not exist` |
| Version-58 leg | `TestBuildAuditStackGateFailsClosedVersion58` | full `testpg.Migrate` + delete row 59 → same error (version-based) |
| Ordering pin | `TestBuildAuditStackGatePrecedesBindingCoverage` | unmigrated DB, `AuditGovernance.Enabled = true` + binding map (forces the old `validateAuditBindingCoverage` raw-SQL path) → error is the version-gate message, not the raw SQL error |
| Structural no-drain pin | same tests by construction | gate error is returned from `buildAuditStack` (`new.go:131`); `callbackSvc.Start` (`new.go:168`) is unreachable; assert error-type/message, not worker state |
| Positive controls | existing `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain` (:317), `TestBuildAuditStackHonorsPurgeKnobs` (:743) | green on migrated DBs |

### T-4 → REQ-4

| Criterion | Pin |
|---|---|
| Harness bookkeeping (enabling) | `testpg.Migrate` records `(version, name)`; 36 files / 66 invocations boot at 59; full `make test-integration` green |
| Both named tests green | `TestMigrationsUpAndDown` (raw sweep, untouched) + `TestAuditReplayCLIRunTwiceIsIdempotent` (now version-59) |
| Zero audit-write sites | gate adds no `Record`/`Log`/`Publish`/`Claim*`/`Requeue*` call; `make check-audit-swallow` stays 0 (roots include `cmd/`) |
| Static gates | `make check-anchored-event-ids`, `make check-audit-manifest` clean (no anchored-event mints, no manifest rows) |

### REQ-5 → `cmd/migrate verify`

New `cmd/migrate/verify_integration_test.go` (the module's first test file): `writeReplayConfig`-style temp config against a testpg DSN; `run([]string{"-config", cfg, "verify"})` at version 58 → err contains `migrate up`; at version 59 (full `testpg.Migrate`) → nil; output prints the `%06d` version (or `no migrations applied (version 0)` on a fresh DB). Comprehensive CLI coverage (`up`/`down`/flags/config-load exits) remains direction 2's scope.

### REQ-1 → gate core

- Unit (`internal/pkg/migrate/migrate_test.go`, existing file + new cases): `b2SurfaceVersionMessage(0, 59)` and `(58, 59)` render `%06d` and contain `migrate up` + `000059`; `currentVersion` delegation is unchanged (`TestParseFilename`/`TestDiscover` untouched).
- Integration (`internal/pkg/migrate/migrate_integration_test.go`, new test): `RequireVersion(db, 59)` on fresh `testpg.Start` → err names `migrate up`; on `Migrator.Up()`-migrated DB → nil; boundary: delete row 59 → err again; re-insert → nil. Assert no `Up()`/`Down()` and no DML beyond `EnsureSchemaMigrations`' idempotent DDL (by test construction: bookkeeping contents unchanged apart from the ensure).

## 8. Verification delta vs. the requirements spec

| Spec item | Delta this pass |
|---|---|
| "36 call sites" (A4/§2.3) | Confirmed as 36 **files**; precise count is 66 invocation lines (the 37th file match is a comment). Scope and conclusion unchanged |
| A1 line numbers | Confirmed: `db.Open` :67; `case "version":` :123 (spec's `:118-125` is the case block region) |
| Everything else | No deltas — all file/line/test claims hold exactly at head `b005409` |
