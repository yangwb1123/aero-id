# Requirements — module `cmd/migrate`: fail-closed schema-version gate for the B2 operational surface (`RequireVersion` + `cmd/migrate verify` + audit-replay/boot checks)

| | |
|---|---|
| Module (batch label) | `cmd/migrate` — the schema-state owner (`internal/pkg/migrate/migrate.go:23` `migrationsTable`, `:79` `Version()`). The gate lands in `internal/pkg/migrate` (shared helper + constant), `cmd/audit-replay` (T-5), `internal/app` (T-10.1), and `cmd/migrate` (the `verify` subcommand from the direction title). `cmd/migrate` gains **no audit code** — the gate is a read-only version check plus one bookkeeping-table DDL |
| Source analysis | `docs/auto/analyses/cmd-migrate-c1b3771d.json` (direction 1 of 3: fail-closed schema-version gate). Sibling directions 2 (CLI test coverage for `cmd/migrate`) and 3 (DDL-shape pins in the migration sweep) are **out of scope here**; referenced only where their tests would overlap |
| Campaign | `aero-id-b2-in-tx-audit` (B2 fail-closed doctrine). The gate is the deployment-order enforcement half of the B2 surface: the replay tool (B2-5) and the drain worker already assume the schema is present |
| Design | None yet — this spec is the design source. The direction's evidence states the gate design is proposed, not implemented ("The gate design is proposed, not yet implemented") |
| Date | 2026-08-08 |
| Status | **Proposed — implementation pending.** Evidence re-verified against head `b005409` + working tree. Every citation in the direction's evidence holds (§2), with one cosmetic line-number drift (A1) and one un-enumerated load-bearing interaction the acceptance is only satisfiable through: `testpg.Migrate` never records `schema_migrations`, so the shared harness must learn version bookkeeping (REQ-4, A4) |

## 1. Problem statement (direction, preserved)

B2-5's replay tool and the drain worker assume the B2 schema is present but never verify it: `cmd/audit-replay/main.go`'s `replay()` opens the DB and immediately queries `audit_publications`/`audit_ledger`, and `internal/app/audit.go` `buildAuditStack` wires the publication repo the same way — grep confirms zero references to `schema_migrations` outside `internal/pkg/migrate`. On a deployment where `make migrate-up` was skipped, audit-replay dies with raw `relation audit_publications does not exist` SQL errors (no actionable guidance, no dry-run counts) and the server drain fails every tick while facts accumulate as pending backlog — the silent-loss shape B2's fail-closed doctrine exists to prevent. `cmd/migrate` is the schema-state owner (`internal/pkg/migrate/migrate.go:23` `migrationsTable`, `:79` `Version()`) and the natural place to expose the gate.

**Verified failure shape (§2.2):** with `audit_ledger` present (000034) but `audit_publications` absent (000045 never applied), the in-tx append path (`createAuditRows`, `internal/repository/audit_ledger.go`) still commits ledger facts; the drain worker (`internal/service/callback_worker.go:34` `Start` → per-tick `drainPublications`) fails its claim query every tick, observes only the failure metric, and retries forever — facts accepted into a schema that cannot deliver them. The audit-replay CLI fails with the raw Postgres error text and no operator guidance.

## 2. Verified evidence (head `b005409` + working tree; line numbers live)

### 2.1 Direction-evidence claims (re-checked)

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `rg schema_migrations` in `internal/app`, `internal/service`, `cmd/audit-replay` returns nothing | ✅ **exactly**: repo-wide `rg -l schema_migrations --type go` = `cmd/migrate/main.go` + `internal/pkg/migrate/migrate.go` only (verified this pass) |
| E2 | `cmd/audit-replay/main.go:100-138` (replay opens DB, no version check) | ✅ holds with drift: `run()` `cmd/audit-replay/main.go:76-140` opens the DB and calls `replay()` with no version check; `replay()` `:141-177` constructs the ledger repo and queries `CountDeadLetterPublications`/`DeadLetterTenants`/`RequeuePublication`/`RequeueDead` immediately — all B2-table queries |
| E3 | `internal/app/audit.go:36-60` `buildAuditStack` (no version check) | ✅ holds with drift: `buildAuditStack` at `internal/app/audit.go:34-66`; validates only the signing key (`validateAuditLedgerConfig` `:23-30`) then wires repos that query `audit_publications`/`audit_ledger`/`callback_deliveries`. Called from the composition root `internal/app/new.go:131`, before the drain `callbackSvc.Start` at `new.go:168` |
| E4 | `internal/pkg/migrate/migrate.go:23` `migrationsTable`, `:79` `Version()` | ✅ exact (`:23`, `:79`); `ensureTable` `:53-64` (CREATE TABLE IF NOT EXISTS); `New` `:35-42`; `Up` `:95`; `Down` `:133`; `ParseDownArg` `:271`; file 283 lines (< 500) |
| E5 | `cmd/migrate/main.go:71-79` (version command wiring) | ⚠️ cosmetic drift: `:71-79` is the `db.Open` block; the `version` command case is `:118-125` (prints `%06d`, or `no migrations applied (version 0)` for 0). Commands today: `up`/`down [N]`/`version` only — no `verify` (A1) |
| E6 | `migrations/000045_audit_publications.up.sql`, `000059_add_dead_letter_purge_indexes.up.sql` (the B2 tables/columns the gate would require) | ✅ both present; 000045 creates `audit_publications` (+ `uq_audit_publication_fact`, `idx_audit_publications_due/lease`); 000059 adds `idx_audit_publications_purge`/`idx_callback_deliveries_purge`. B2 chain: 000034 `audit_ledger`, 000045 `audit_publications`, 000050 tenant-scoped dedup UNIQUE, 000057 `audit_holds`, 000058 claim-outbox columns, 000059 purge indexes. **Latest on-disk version = 000059** |
| E7 | `migrations/migrations_test.go` `TestMigrationsUpAndDown` and `cmd/audit-replay/main_test.go` `TestAuditReplayCLIRunTwiceIsIdempotent` exist and must stay green | ✅ both exist (`migrations/migrations_test.go:34`, `cmd/audit-replay/main_test.go:137`); `TestMigrationsUpAndDown` executes the raw SQL files directly (never `run()`, never `schema_migrations`) and is unaffected by the gate; `TestAuditReplayCLIRunTwiceIsIdempotent` drives `replay()` via the `runCapture` harness against `testpg.Start` + `testpg.Migrate` — **this is where the harness interaction bites** (A4) |
| E8 | `check-audit-swallow` scans `cmd/` | ✅ `Makefile:67-69` — roots `internal/service internal/repository internal/app internal/storage internal/block cmd`. The gate adds zero audit-write sites (read-only version check), so the gate stays 0 (REQ-4) |

### 2.2 The drain-fails-every-tick shape (problem confirmation)

- `internal/service/callback_worker.go:34-52` `Start`: per-tick `drainPublications` → `drain` → `purgeDeadLetters`; errors are observed via `ObserveAuditPublicationPurgeFailed`/`ObserveAuditPublicationClaimFailed`-class metrics and the loop **continues** — no boot abort, no operator-visible failure beyond metrics. `internal/repository/audit_publication.go:46` `ClaimPublication` (FOR UPDATE SKIP LOCKED) returns the raw `relation "audit_publications" does not exist` error when 000045 was never applied.
- Fact acceptance without deliverability: `internal/repository/audit_ledger.go` `createAuditRows` commits the ledger row + publication row in one tx; with `audit_publications` missing the whole append fails (fail-closed at the write site), but the drain-tick failure is the **silent backlog** shape when `audit_ledger` exists (000034) and the B2 tail (≥ 000045) does not — or when the operator runs `down` past 000045.
- `internal/app/audit.go:165` `validateAuditBindingCoverage` queries `accounts`/`audit_publications`/`audit_ledger` — on an unmigrated DB with governance enabled, boot already fails with a **raw SQL error**. The gate must run before `buildAuditPublishers` so the failure is the actionable message, not the raw one (REQ-3 ordering).

### 2.3 Load-bearing interaction the direction did not enumerate (A4)

`internal/repository/testpg/testpg.go:53-74` `testpg.Migrate` executes every `*.up.sql` file in lexical order and **never creates or records `schema_migrations`**. It is the shared integration harness for 36 test files (including `cmd/audit-replay/main_test.go` and `internal/app/audit_integration_test.go`). A version-based gate reads `schema_migrations` (via `Migrator.Version()`), so a DB migrated by `testpg.Migrate` reads version 0 — the gate would fire on every existing integration test, breaking the T-4 acceptance. The acceptance is only satisfiable if the harness records applied versions (REQ-4). Verified: **no existing test combines `testpg.Migrate` with `Migrator.Up`/`Down`** (grep for `testpg.Migrate` ∩ `migrate.New|Migrator` = ∅), so teaching the harness version bookkeeping cannot collide with any existing `Migrator` test.

## 3. Design decisions (bounded by the direction)

1. **One shared gate, one threshold.** `internal/pkg/migrate` exposes `RequireVersion(db, min)` (proposed name; exact shape is the implementer's) and `MinB2SurfaceVersion = 59` — the B2 operational-surface completeness marker (000059 = purge indexes; the last B2 migration on disk today). `cmd/audit-replay` and `internal/app` must use the **same** constant so the replay tool and the boot check can never drift apart on the threshold. `internal/pkg/migrate` imports only gorm + stdlib, so importing it from `cmd/audit-replay` and `internal/app` introduces no cycle.
2. **Read-only gate, no auto-migration.** `RequireVersion` reuses `Version()` (→ `ensureTable` `CREATE TABLE IF NOT EXISTS schema_migrations` — the same idempotent bookkeeping DDL `cmd/migrate version` performs today, and the same write the operator's `migrate up` expects to find) and **never applies migrations**. AGENTS.md: "Migrations are not auto-applied by default" — the gate fails with guidance; it does not heal.
3. **Stable, greppable, actionable message.** The error text is produced by one format function (unit-testable without a DB) and must contain the literal remediation `migrate up` and the threshold version `000059`, e.g. `database schema at version %06d is below the required B2 surface version 000059 — run 'migrate up' (make migrate-up) before starting`. Both gate sites surface this exact text (T-10.1's "the same actionable error").
4. **Gate placement.** `cmd/audit-replay`: first statement of `replay()` — before any ledger construction or count query — so `run()` (exit 1 via the `audit-replay: ` prefix) **and** the `runCapture` test harness (which drives `replay()` directly) both exercise it, in `-dry-run` and mutating modes alike. `internal/app`: first statement of `buildAuditStack` after `validateAuditLedgerConfig` and **before** `buildAuditPublishers` — so on an unmigrated DB the actionable message wins over `validateAuditBindingCoverage`'s raw SQL error, and boot aborts at `new.go:131` before the drain `Start` at `new.go:168` is ever reached (no fact can be accepted into a schema that cannot deliver it).
5. **`cmd/migrate verify` (direction title).** New `verify` subcommand: prints the current version and runs `RequireVersion` against `MinB2SurfaceVersion`; exit 0 at/above, non-zero with the actionable message below. Shares the helper — ~10 lines in `cmd/migrate/main.go` (138 → ≤ 150 lines). Comprehensive `cmd/migrate` CLI test coverage (flag errors, `down [N]` parsing, config-load exits) is sibling direction 2's scope and is **not** required here; only the `verify` path gets a pin.
6. **`(dry-run prints 0 counts)` reading (A3).** The supplied acceptance's parenthetical is pinned as: the gate fires identically in `-dry-run` mode, **before** any count query; the count lines are never printed; zero rows requeued and zero facts minted by construction. The literal reading ("proceed to print 0 counts on a schema that cannot be counted") would require counting on missing tables — the fail-open shape this direction exists to close — and is rejected.

## 4. Requirements (supplied acceptance checks preserved; testable criteria)

### REQ-1 — Gate core: `internal/pkg/migrate` gains `RequireVersion(db, min)` + `MinB2SurfaceVersion = 59` (proposed names), read-only, actionable message

Testable criteria:

1. **Unit (no DB):** the message format function is pure and pinned — `(version 0)` and `(version 58)` render `%06d` versions and contain the literals `migrate up` and `000059`; `version ≥ min` renders no error. (New `internal/pkg/migrate/migrate_test.go` cases; file stays under 500 lines — currently 283.)
2. **Integration (`go test -tags=integration ./internal/pkg/migrate/`):** `RequireVersion(db, 59)` against a fresh `testpg.Start` DB (no migrate) → error naming `migrate up`; against a DB migrated by `Migrator.Up()` to the top of the real `migrations/` dir → nil; boundary pin: delete `schema_migrations` row 59 → error again (58 < 59), re-insert → nil. (`TestMigratorUpAndDown` in `migrate_integration_test.go` already proves `Migrator.Up()` reaches the top version; the new test reuses its shape.)
3. The gate performs **no** `Up()`/`Down()` and no DML beyond `ensureTable`'s idempotent `CREATE TABLE IF NOT EXISTS` — asserted by test construction (migrations table contents unchanged apart from the ensure).
4. `make check-file-sizes` green (283 → ≤ ~310 lines).

### REQ-2 — T-5: `cmd/audit-replay` fails closed on `schema_migrations < 000059`, with the actionable message, zero mutation, zero facts

> Supplied acceptance (preserved): "T-5: cmd/audit-replay against a DB at schema_migrations < 000059 exits non-zero with a message naming `migrate up`, mints zero facts and requeues zero rows (dry-run prints 0 counts)."

Testable criteria (integration, `cmd/audit-replay/main_test.go` — new test; the existing `runCapture`/`parseFlags` harness drives `replay()` directly, so both CLI paths are covered):

1. **Fresh-DB leg:** `testpg.Start` with **no** `testpg.Migrate`; `replay()` in `-dry-run` mode → error non-nil whose text contains `migrate up` and **does not** contain `relation`/`does not exist` (no raw SQL leak); stdout contains no `to requeue` count lines. Mutating mode with `-actor` → same error (exit 1 via `run()`'s `audit-replay: ` prefix; the test asserts the error, the `run()` path is covered by the existing outage-test precedent `TestAuditReplayDBOutageFailsClosed` leg 1).
2. **Partial-version leg (version 58, tables present):** `testpg.Start` + `testpg.Migrate` + `DELETE FROM schema_migrations WHERE version = 59`; seed one `dead_letter` publication via the public surface (`seedDeadLetterPublication` precedent); mutating `replay()` with `-actor` → error naming `migrate up`; the seeded row is still `status = 'dead_letter'`; `SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'` = 0. This proves the gate is **version-based, not existence-based** (the B2 tables exist at 58; the tool still refuses) and pins zero-mutation/zero-fact on a schema where mutation would otherwise be possible.
3. **Version-59 control:** fully migrated DB → existing `TestAuditReplayCLIRunTwiceIsIdempotent` behavior unchanged (covered by REQ-4.2).
4. `cmd/audit-replay/main.go` stays ≤ 500 lines (357 → ~360) — `make check-file-sizes`.

### REQ-3 — T-10.1: boot gate — `buildAuditStack` on a schema below 000059 fails boot with the same actionable error; the drain never runs; no fact is accepted

> Supplied acceptance (preserved): "T-10.1: buildAuditStack (or drain Start) with ledger enabled on an unmigrated DB fails boot with the same actionable error instead of running drain with missing tables, so no fact is accepted into a schema that cannot deliver it."

Testable criteria (integration, `internal/app/audit_integration_test.go` — new test):

1. **Unmigrated leg:** `testpg.Start` with no `testpg.Migrate`; `buildAuditStack` with a signing key set ("ledger enabled" = the `validateAuditLedgerConfig` activation gate, which is the only ledger enablement switch) → error non-nil whose text **equals** the REQ-2 message (same `RequireVersion` text — "the same actionable error"), contains `migrate up`, and does not contain `relation`/`does not exist`.
2. **Partial-version leg:** full `testpg.Migrate` + `DELETE FROM schema_migrations WHERE version = 59` → same error (version-based).
3. **Ordering pin:** on the unmigrated DB, the returned error is the version-gate message, **not** the `validateAuditBindingCoverage` raw SQL error — i.e. the gate runs before `buildAuditPublishers` (governance enabled case; test with `AuditGovernance.Enabled = true` + a binding map to force the old failure path). This also covers the ordering decision §3.4.
4. **Structural no-drain pin:** boot aborts at `buildAuditStack` (`internal/app/new.go:131`), before `callbackSvc.Start` (`new.go:168`) — asserted by construction (composition root returns the error; no worker exists to start) plus the error-type assertion above.
5. **Positive controls:** migrated DB → `buildAuditStack` succeeds — existing `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain` (`:322`) and `TestBuildAuditStackHonorsPurgeKnobs` (`:743`) stay green.
6. `internal/app/audit.go` stays ≤ 500 lines (245 → ~250) — `make check-file-sizes`.

### REQ-4 — T-4: enabling harness change + no-regression (the direction's acceptance is only satisfiable via A4)

> Supplied acceptance (preserved): "T-4: existing TestMigrationsUpAndDown (migrations/migrations_test.go) and TestAuditReplayCLIRunTwiceIsIdempotent (cmd/audit-replay/main_test.go) stay green — no audit-write sites added, check-audit-swallow stays 0."

Testable criteria:

1. **Harness bookkeeping (enabling, test-infra only):** `testpg.Migrate` (`internal/repository/testpg/testpg.go:53`) creates `schema_migrations` (same DDL as `migrate.ensureTable`, `migrate.go:53-64`) and records each applied `(version, name)` with `ON CONFLICT DO NOTHING`, making the harness semantically equivalent to `Migrator.Up()` (the doc comment already claims the same ordering). Verified no existing test combines `testpg.Migrate` with `Migrator.Up/Down` (§2.3) → no collision. All 36 `testpg.Migrate` call sites then boot with version 59 and pass any gate.
2. **No-regression:** `make test-integration` green with `TestMigrationsUpAndDown` (raw-SQL sweep — untouched by the gate; it never reads `schema_migrations`) and `TestAuditReplayCLIRunTwiceIsIdempotent` (now version-59 via REQ-4.1) passing. Full-suite run is required by AGENTS.md because the shared harness changed.
3. **No audit-write sites:** the gate adds no `Record`/`Log`/`Publish`/`Claim*`/`Requeue*` call and no new audit write path anywhere — `make check-audit-swallow` stays 0 (roots include `cmd/`). The only new write is the idempotent `schema_migrations` bookkeeping DDL, which is not an audit site.
4. **Static gates:** `make check-anchored-event-ids` and `make check-audit-manifest` stay green (no anchored-event mints, no manifest rows added).

### REQ-5 — Direction-title surface: `cmd/migrate verify` subcommand

> Title scope: "…(`cmd/migrate verify` + audit-replay/boot checks)". Not covered by the T-5/T-10.1/T-4 acceptance texts, but named in the direction; implemented as thin sugar over REQ-1.

Testable criteria:

1. `migrate verify` prints the current version (`%06d`, or `no migrations applied (version 0)`) and runs `RequireVersion(db, MinB2SurfaceVersion)`; exit 0 at/above 59, non-zero with the actionable message below — via `run()`'s existing `migrate: ` prefix and exit 1 (no new exit machinery).
2. **One integration pin** (`cmd/migrate/verify_integration_test.go`, the module's first test file): drive `run([]string{"-config", cfg, "verify"})` against a testpg DSN (temp-config precedent: `writeReplayConfig` in `cmd/audit-replay/main_test.go`) at version 58 → error contains `migrate up`; at version 59 (full `testpg.Migrate`) → nil.
3. `cmd/migrate/main.go` stays ≤ 500 lines (138 → ≤ ~150). Comprehensive CLI-test coverage of `up`/`down`/flags/config-load exits is sibling direction 2's scope — explicitly not required here.

## 5. Compatibility constraints

- **No migration files change.** The gate reads `schema_migrations`; `migrations/*.sql` are untouched. `TestMigrationsUpAndDown` (raw-SQL sweep) is structurally unaffected.
- **No behavior change on a migrated schema.** Version ≥ 59 ⇒ gate is a no-op: audit-replay, drain, and boot behave exactly as today. Gate sites are two lines each; no HTTP/gRPC/proto surface, no config keys, no new flags except the `verify` subcommand.
- **No auto-migration, no fail-open.** The gate never applies migrations and never skips on a missing `schema_migrations` table (missing table ⇒ `Version()` ⇒ 0 ⇒ gate fires — the fresh-DB case is the primary target). `configs/config.yaml`'s dev signing-key placeholder means the out-of-box config still boots **only after** `make migrate-up`, which is the intended deployment order.
- **Message stability is a contract:** tests grep for `migrate up` and `000059`; the message text is produced by one format function (REQ-1.1).
- **The `testpg.Migrate` harness change is test-infra-only** — no production code path depends on it; it exists to keep the T-4 acceptance honest.

## 6. Failure modes (closed by this direction)

| Failure | Closure | Pin |
|---|---|---|
| Deployment skipped `make migrate-up`; audit-replay run | Gate fires before any B2 query: exit non-zero, message names `migrate up`, zero rows requeued, zero facts minted, no raw `relation … does not exist` text | REQ-2.1, REQ-2.2 |
| Deployment skipped `make migrate-up`; server boot with ledger enabled | `buildAuditStack` fails with the same actionable message before `buildAuditPublishers` and before drain `Start`; no tick ever runs; no fact accepted | REQ-3.1–REQ-3.4 |
| Partial rollback (`down` past 000059/000045) | Version-based gate fires although some B2 tables exist — schema_migrations is the source of truth, not table presence | REQ-2.2, REQ-3.2 |
| Governance enabled on an unmigrated DB | Ordering: version gate precedes `validateAuditBindingCoverage`'s raw-SQL failure; operator sees the actionable message | REQ-3.3 |
| Operator needs a check surface on the CLI | `migrate verify` exits 0/non-zero with the same message | REQ-5 |
| Gate mis-fires on every integration test (harness records no versions) | `testpg.Migrate` records applied versions; 36 call sites read version 59 | REQ-4.1 |
| Threshold drift between replay tool and boot check | One shared constant `MinB2SurfaceVersion` in `internal/pkg/migrate` | REQ-1, §3.1 |

## 7. Done-gate evidence (planned for the implement/verify stage)

```text
make test && make lint && make lint-strict && make check-file-sizes \
  && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest \
  && make check-integration-compile && make build
go test ./internal/pkg/migrate/ -count=1            # unit: message-format pins (REQ-1.1)
go test -tags=integration ./internal/pkg/migrate/   # RequireVersion boundary 58/59 (REQ-1.2)
go test -tags=integration ./cmd/audit-replay/       # REQ-2 incl. TestAuditReplayCLIRunTwiceIsIdempotent
go test -tags=integration ./internal/app/           # REQ-3 incl. existing buildAuditStack tests
go test -tags=integration ./cmd/migrate/            # REQ-5 verify pin
make test-integration                               # REQUIRED (AGENTS.md): shared harness changed (REQ-4.1)
```

## 8. Testable acceptance mapping (supplied checks preserved verbatim)

| Supplied acceptance (direction) | Status | Testable criteria (proposed pins) |
|---|---|---|
| **T-5** "cmd/audit-replay against a DB at schema_migrations < 000059 exits non-zero with a message naming `migrate up`, mints zero facts and requeues zero rows (dry-run prints 0 counts)" | **Proposed** | REQ-2: fresh-DB leg (no raw SQL leak; no count lines) + version-58 leg (seeded dead_letter row untouched; `audit.publication.requeued` = 0); `(dry-run prints 0 counts)` pinned as abort-before-counting with zero mutation by construction (A3) |
| **T-10.1** "buildAuditStack (or drain Start) with ledger enabled on an unmigrated DB fails boot with the same actionable error instead of running drain with missing tables, so no fact is accepted into a schema that cannot deliver it" | **Proposed** | REQ-3: `buildAuditStack` errors with the identical `RequireVersion` text on fresh and version-58 DBs; ordering pin vs `validateAuditBindingCoverage`; structural pin that `new.go:131` aborts before `new.go:168` `Start` |
| **T-4** "existing TestMigrationsUpAndDown (migrations/migrations_test.go) and TestAuditReplayCLIRunTwiceIsIdempotent (cmd/audit-replay/main_test.go) stay green — no audit-write sites added, check-audit-swallow stays 0" | **Proposed** | REQ-4: `testpg.Migrate` version bookkeeping (enabling, A4); both named tests green in `make test-integration`; gate adds no audit-write sites; `check-audit-swallow`/`check-anchored-event-ids`/`check-audit-manifest` stay 0/clean |

## 9. Amendment register (stale or un-enumerated wording in the direction / source analysis)

| # | Claim | Correction | Folded into |
|---|---|---|---|
| A1 | "`cmd/migrate/main.go:71-79` (version command wiring)" | Cosmetic drift: `:71-79` is the `db.Open` block; the `version` case is `:118-125`. No `verify` command exists today | §2.1 E5, REQ-5 |
| A2 | "`cmd/audit-replay/main.go:100-138` (replay opens DB, no version check)" | Holds with drift: `run()` DB-open at `:76-140`; `replay()` at `:141+` queries B2 tables immediately, no version check | §2.1 E2 |
| A3 | "(dry-run prints 0 counts)" | Pinned as abort-before-counting: the gate fires in `-dry-run` mode before any count query; count lines never print; zero rows/facts by construction. The literal "proceed and print 0" reading would count on a schema that cannot be counted (fail-open) and is rejected | §3.6, REQ-2, §8 |
| A4 | (un-enumerated) the direction's T-4 acceptance is satisfiable only if the shared integration harness records `schema_migrations` | `testpg.Migrate` (36 call sites) executes raw SQL and never records versions; a version-based gate reads version 0 there and would fire on every existing integration test. Enabling change: harness records `(version, name)` like `Migrator.Up`; verified no test combines `testpg.Migrate` with the `Migrator`, so no collision | §2.3, REQ-4.1 |

---

### Done-gate relevance

This direction is **production-code additive** (gate helper + two wiring lines + `verify` subcommand) with **one test-infra change** (`testpg.Migrate` bookkeeping). The full `make test-integration` run is mandatory in the implement stage because the shared harness changed (AGENTS.md). Sibling direction 2 owns the comprehensive `cmd/migrate` CLI test suite; direction 3 owns DDL-shape pins in the migration sweep — neither is required for this spec's acceptance, and no requirement above expands into them.
