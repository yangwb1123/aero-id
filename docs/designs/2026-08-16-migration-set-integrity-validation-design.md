# Design — Migration-Set Integrity Validation (strict `Discover` + `validate` + CI-safe gate)

Status: **Proposed** (implementation-ready)
Date: 2026-08-16
Module: `internal/pkg/migrate`, `cmd/migrate`, `migrations/`, `Makefile`, `.github/workflows/ci.yml`, `scripts/`
Spec: Requirements spec `docs/auto/runs/migration-set-integrity-validation-in-internal-p-baa9fb0c/artifacts/requirements-10762e10/requirements.md`
Baseline verified at: HEAD `b1942fb` — every evidence claim re-verified against the live tree on 2026-08-16 (symbols, line ranges, counts, and behaviors all match; see §1).

---

## 1. Verification verdict (untrusted evidence → tree)

| # | Claim (evidence) | Verdict | Tree evidence |
|---|---|---|---|
| E1 | `Discover()` merges duplicate versions via a map; second file silently wins | **Confirmed** | `internal/pkg/migrate/migrate.go:207-244` — `byVersion map[uint64]*Migration`; `mig.UpFile = full` / `mig.DownFile = full` overwrite; name kept from first file. `parseFilename` at 245-283 (line numbers exact). |
| E2 | Malformed names dropped silently (`ok=false -> continue`) | **Confirmed** | `migrate.go:217-218` — `if !ok { continue }`. `parseFilename` returns `ok=false` for `000060_x.UP.sql`, `000060_x.sql`, `abc_bad.up.sql` (case-sensitive suffix check). |
| E3 | `Up()` never enforces the up/down pair; errors only when the UP file is missing | **Confirmed** | `migrate.go:95-131` — `ensureTable()` first (96-98), then `Discover` (99), then per-migration `if mig.UpFile == ""` (~line 108); no `DownFile` check. |
| E4 | `Down()` fails late with "has no .down.sql file" | **Confirmed** | `migrate.go:133-185` — `if mig.DownFile == ""` (~line 157), reached only for applied versions. |
| E5 | `Version()` reads only `MAX(version)`, never diffs disk vs applied | **Confirmed** | `migrate.go:79-93` — `ensureTable()` then `SELECT MAX(version)`; `Discover` not called. |
| E6 | `cmd/migrate` has up/down/version only, no validate/status | **Confirmed** | `cmd/migrate/main.go` (138 lines) — `usage()` lists `up`, `down [N]`, `version`; switch cases in `run()`. |
| E7 | Pair invariant enforced only by the Docker-gated integration test | **Confirmed** | `migrations/migrations_test.go:1` `//go:build integration`; `discoverMigrations` at 76-113. `make test` = `go test ./... -count=1` (no tag); `check-integration-compile` compiles tagged files, never runs them (Makefile:35-39). |
| E8 | `migrate_test.go` has no pair/duplicate cases; `TestDiscover` **asserts an unpaired migration is discoverable** | **Confirmed** | `internal/pkg/migrate/migrate_test.go` (116 lines) — `TestParseDownArg`, `TestParseFilename`, `TestDiscover` (fixture includes `000003_c.up.sql` "intentionally missing its down" and asserts 3 migrations, `migs[2].DownFile == ""`), `TestDiscoverMissingDir`. |
| E9 | Makefile has no migration-set gate | **Confirmed** | `.PHONY` at line 6; `check-load-scenarios` target at 50-52; migration tooling `migrate-up/down/version` at 143-148 only. |
| E10 | `scripts/check-load-scenarios.sh` R3 covers only bench-table existence | **Confirmed** | `rule_r3()` (line 180) — bench SQL relations must have `CREATE TABLE` in `migrations/*.up.sql`; no pair/duplicate/malformed logic. |
| E11 | Tree currently 59/59 pairs | **Confirmed** | `ls migrations/*.up.sql | wc -l` = 59; `*.down.sql` = 59; `comm -3` on version sets = empty. |
| E12 | `Discover` callers = `Up`/`Down` only (via `cmd/migrate` and `internal/app/new.go` auto-migrate) | **Confirmed** | `grep -rn "Discover("` → `migrate.go:99` (`Up`), `migrate.go:140` (`Down`) only in non-test Go. `internal/app/new.go:48-66` auto-migrate path (`cfg.Database.AutoMigrate`, off by default) calls `migrate.New(...).Up()`. `internal/repository/testpg/testpg.go` and the `migrations_test.go` loop apply SQL directly, never call `Discover`. |
| E13 | `migrations_test.go` comment documents name-mismatch as a `Discover()`-time error — currently unimplemented | **Confirmed** | `migrations_test.go:73-75` — "a version whose name differs between up and down is a Discover()-time error in migrate.go"; the map keeps the first file's name, so no such check exists today. |
| E14 | `schema_migrations` DDL has no content hash | **Confirmed** | `migrate.go:37-46` — `version BIGINT PRIMARY KEY, name TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now()`. |
| E15 | House style for self-testing gates: header = canonical rules, fixtures under `scripts/testdata/<gate>/`, positive must fire designated rule only, negative must be clean, missing fixtures fail the gate, Makefile-recipe tripwire | **Confirmed** | `scripts/check-load-scenarios.sh:222-264` (missing tree, per-positive designated-rule sweep, negative clean sweep); `scripts/check-audit-swallow.sh:409-416` (anchored recipe-line tripwire grepping the Makefile). `scripts/testdata/` holds per-gate fixture dirs; no `migration-set/` yet. |
| E16 | CI `check-gates` runs on every PR/push with no paths filter | **Confirmed** | `.github/workflows/ci.yml:92-108` — checkout + `check-audit-swallow` + `check-architecture` + `check-load-scenarios`; comment states "No paths filter: a filter would recreate the blind spot this gate closes." |
| E17 | Lint-strict limits: funlen 50 / cyclop 10, tests included | **Confirmed** | `.golangci.yml` — `cyclop.max-complexity: 10`, `funlen.lines: 50`, `funlen.statements: 40`, `tests: true`. `cmd/migrate/main.go` `run()` already carries `//nolint:cyclop,funlen` (pre-existing). |

**Evidence deltas found (content of the direction is unaffected; one spec-internal gap):**

1. **§3 classification table is internally inconsistent for non-zero-padded but parseable files.** The table's "migration-like malformed" predicate requires `parseFilename` to *fail* AND a name-shape condition. But the "Decisions locked in" text requires a non-zero-padded file (e.g. `42_thing.up.sql`) to "still parse[] but [be] rejected by Discover". For such a file `parseFilename` *succeeds* (pinned by `TestParseFilename`), so the table's predicate classifies it as neither malformed nor valid — falling through to "non-migration" (silently ignored), which contradicts the locked-in decision. **Fix adopted in §3:** the malformed predicate is name-shape-based only (no `parseFilename` clause); anything that looks like a migration but fails the canonical form errors. This makes `42_thing.up.sql`, `_no_version.up.sql`, `000001.up.sql`, `000060_x.UP.sql`, `000060_x.sql`, `abc_bad.up.sql` all D4 errors, and `README.md`/`notes.txt`/`migrations_test.go`/dotfiles all ignored — matching every example in the spec.
2. Evidence line `cmd/migrate/main.go:84` as the `Up` caller is stale (it's `m.Up()` in the `up` case, ~line 101; `m := migrate.New(...)` ~line 90) — behavior identical; the spec already flagged stale line numbers.
3. The evidence's `validate` existence check says "in the public schema"; this design uses `table_schema = current_schema()` (§5.3) so the check follows the same search-path the DDL uses — a strict correctness improvement, no behavior change for the default DSN (public is first in search_path).

---

## 2. Problem statement

The production runner silently tolerates a broken migration set:

1. **Duplicate versions** — `Discover()` merges by version via a map; a second file with the same version silently overwrites the first (the map-merge at `migrate.go:222-232`). The runner applies whichever file was scanned last.
2. **Malformed names** — `parseFilename ok=false -> continue`; `000060_x.UP.sql`, `000060_x.sql`, `abc_bad.up.sql` are silently invisible to the runner, and so is the non-zero-padded `42_thing.up.sql`.
3. **Unpaired migrations** — the AGENTS.md invariant ("Every zero-padded `NNNNNN_name.up.sql` has a matching `.down.sql`") is not enforced by `Discover`/`Up`: `Up()` only checks `UpFile != ""`, so an up-without-down applies cleanly and fails only later at `down` time; a down-without-up is invisible until `Down` errors on a version that has files but no up.
4. **Disk-vs-applied drift invisible** — `Version()` reads only `MAX(version)`; an applied migration deleted from disk is never reported by any command.
5. **No mechanical pin** — the pair invariant is enforced only by the Docker-gated `migrations/migrations_test.go`, which `make test`/`check-integration-compile` never execute. A commit that breaks the invariant passes the default done-gate and surfaces in production. The tree is currently 59/59 pairs, but nothing mechanical pins that.

The requirements spec (REQ-1..REQ-8) resolves these with: a strict `Discover`, fail-fast `Up`/`Down`, a `validate` subcommand with a pure testable seam, a CI-safe bash gate with self-test fixtures, and a reserved (not implemented) `content_hash` phase.

---

## 3. Canonical validity contract (single source of truth)

One regular expression is the sole validity definition shared by the Go layer and the bash gate, so the two can never diverge:

```
^[0-9]{6}_.+\.(up|down)\.sql$
```

This is byte-compatible with `migrations_test.go`'s `migrationFileRE` (`^(\d{6})_.+\.(up|down)\.sql$`; `\d` ≡ `[0-9]` for ASCII) and enforces the zero-padded `NNNNNN` part of the AGENTS.md contract.

### 3.1 Classification (corrected; see §1 delta 1)

For a non-directory file `f` in a migrations directory, let `base` be the basename:

| Class | Predicate | Treatment |
|---|---|---|
| **Valid migration** | `base` matches the canonical regexp | parsed by `parseFilename`; merged into the version set |
| **Migration-like malformed** | `base` is migration-like AND does NOT match the canonical regexp | **error** (D4) |
| **Non-migration** | `base` is not migration-like | silently ignored (README.md, notes.txt, `migrations_test.go`, dotfiles, subdirectories — unchanged current behavior) |

where **migration-like** is a pure name-shape predicate (no `parseFilename` involved — this is the §1-delta-1 fix):

```
migration_like(base) := (base ends with .up.sql OR .down.sql, case-insensitive)
                     OR (base has a [0-9]{6}_ prefix AND base ends with .sql, case-insensitive)
```

Worked examples (all locked in):

| File | canonical? | migration-like? | Result |
|---|---|---|---|
| `000060_x.up.sql` | yes | — | valid |
| `000060_x.UP.sql` | no (case) | yes (ci `.up.sql`) | D4 error |
| `000060_x.sql` | no | yes (prefix + ci `.sql`) | D4 error |
| `abc_bad.up.sql` | no | yes (ci `.up.sql`) | D4 error |
| `42_thing.up.sql` | no (not zero-padded) | yes (ci `.up.sql`) | D4 error (spec gap closed) |
| `_no_version.up.sql`, `000001.up.sql` | no | yes (ci `.up.sql`) | D4 error |
| `README.md`, `notes.txt`, `migrations_test.go`, `.gitkeep` | no | no | ignored |

Deliberate consequences:
- `parseFilename` keeps its permissive numeric parsing (pinned by `TestParseFilename`: `42_thing.up.sql` → `ok=true`); canonical-form enforcement lives in `Discover` and the gate. The Go/bash asymmetry that a 7-digit or short version would otherwise create is removed because both layers reject at the same canonical form.
- Any file with a 6-digit numeric prefix and a `.sql` suffix is treated as migration-like even if it is not a `.up.sql`/`.down.sql` pair file — this is intentional (it looks like a migration, violates the naming contract) and must be documented in the script header so operators don't trip on it.
- Name consistency is part of "matching" in the quoted pair invariant and is already documented as a `Discover()`-time error by `migrations_test.go`; it becomes real behavior (D3).

---

## 4. API changes

### 4.1 Go — `internal/pkg/migrate`

**`Discover(dir string) ([]Migration, error)` — strict validation (REQ-1).**
Signature unchanged; the error contract extends from "read-dir failure only" to include D-rule violations. All existing callers already propagate errors (`Up`, `Down`; tests), so this is source-compatible. Behavior:

- **D1 Duplicate version:** more than one `.up.sql` file, or more than one `.down.sql` file, for the same version errors, naming **both** files: `duplicate migration version 000060: migrations/000060_x.up.sql and migrations/000060_y.up.sql`.
- **D2 Up/down pair invariant:** a version with an up file but no down errors `migration 000060 has no .down.sql file (migrations/000060_x.up.sql)`; symmetric message for down-without-up.
- **D3 Name mismatch:** up/down files of one version must carry identical names: `migration 000060 name mismatch: up name "x" vs down name "y"`. Detected at merge time regardless of scan order (up-first or down-first).
- **D4 Malformed migration-like filename:** any migration-like file failing the canonical contract errors `malformed migration filename "000060_x.UP.sql"` (predicate in §3.1).
- **D5 Non-migration files stay ignored** and never enter the result set.
- Preserve: ascending-version sort of the result.
- Fail-fast: first violation encountered wins (deterministic given `os.ReadDir` order is sorted — `ReadDir` returns entries sorted by filename, so error choice is stable for a given directory contents).

Implementation shape (extract helpers so every function stays within funlen 50 / cyclop 10 — **no new `nolint` exemptions**):
- `var canonicalMigrationRE = regexp.MustCompile(...)` — package-level; the one shared regexp.
- `func migrationLike(name string) bool` — pure predicate (§3.1).
- `func (m *Migrator) discoverStrict(dir string) ([]Migration, error)` or free function `discoverStrict` — scan loop building per-version file inventories (`upByVersion`, `downByVersion map[uint64][]string`, first-seen name map), erroring on D1/D4 inline; then a merge pass over the union of versions enforcing D2/D3 and emitting ascending-sorted `[]Migration`. `Discover` becomes a thin wrapper (read-dir error + `discoverStrict`), keeping `Discover`'s callers unchanged.

**`Up()` / `Down()` — fail-fast reorder (REQ-2).**
Move `Discover(m.dir)` to run **before** `ensureTable()` in both. Consequences:
1. A broken migration set errors before `schema_migrations` is created and before any SQL executes (fail-before-mutate — strictly safer than today, where the table is created and then the runner errors or, for up-without-down, applies cleanly).
2. The error path becomes DB-free: `migrate.New(nil, dir, nil).Up()` on a broken fixture returns an error without dereferencing the nil DB — this is what makes the "errors at Up" unit test possible under `make test` (no Docker).
3. The `Up` error for a broken set is the `Discover` error, propagated unchanged — satisfying the direction's "up-without-down fixture errors at Discover/Up".
- `Version()` semantics are **not** changed (no disk diff inside `Version`; drift reporting belongs to `validate`, REQ-6). `Version()` keeps calling `ensureTable()`.

**`ValidateSet` (REQ-6) — new pure, DB-free function:**
```go
type ValidateResult struct {
    Pairs          int      // complete up/down pairs on disk
    Pending        []uint64 // on disk, not applied (ascending)
    AppliedNoFiles []uint64 // recorded applied, missing from disk (ascending)
}

func ValidateSet(migrations []Migration, applied map[uint64]bool) ValidateResult
```
Pure function; the testable seam (unit tests run under `make test`, no DB, no Docker).

**`(*Migrator).Validate() (ValidateResult, error)` (REQ-6) — new method, non-mutating:**
1. `Discover(m.dir)` — strict; any D-rule violation errors and fails the command.
2. Read applied versions **without creating the table**: `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'schema_migrations')`. Absent → applied = empty (fresh DB, not an error). Present → reuse the existing `appliedVersions()` shape behind the existence gate.
3. `ValidateSet(migrations, applied)`.

Non-mutation contract: `Validate` must not call `ensureTable`, must not execute migration SQL, and must not write rows. Opening the DB connection (in `cmd/migrate`) is not a mutation.

### 4.2 CLI — `cmd/migrate`

New subcommand `validate` (REQ-6): `migrate [-config …] [-migrations …] validate` — "report migration-set integrity and disk-vs-applied drift without mutating".

- `usage()` gains a `validate` line.
- Dispatch: add a `case "validate":` in `run()`. The existing `run()` already carries `//nolint:cyclop,funlen` (pre-existing, not new). Keep the case body thin — delegate to a small helper (e.g. `validateCmd(m *migrate.Migrator) error`) that stays ≤50 lines, so no new nolint is needed and the switch stays readable.
- Output on healthy run (exit 0; pending is normal, not an error): `migration set: 59 up/down pairs; applied: 59; pending: 0; applied-without-files: 0`.
- Broken set (D-rule error), non-empty `AppliedNoFiles`, or DB failure: return error → existing `main()` contract prints `migrate: <err>` to stderr and exits 1. For `AppliedNoFiles`, print each offending version before returning the error.
- `Pending` is informational (printed in the summary), not an error — a fresh DB with zero applied migrations exits 0.
- The CLI opens the DB before dispatch, as today (DB-open is not a mutation); the `-config`/`-migrations` flags are reused unchanged.

### 4.3 Gate — `scripts/check-migration-set.sh` (new, REQ-4)

Isolation class: **bash + grep/sed/awk only** — no Go toolchain, no Docker, no network, no new binaries (runs in the `check-gates` CI job). **The script header is the canonical rule definition**, per house style.

Rules (each scans one root's `migrations/` tree — the real repo or a fixture; `ROOT` parameterized like `check-load-scenarios.sh`):

- **G1 Unpaired:** extract 6-digit version prefixes from the up glob and the down glob (`ls "$root"/migrations/*.up.sql 2>/dev/null | sed 's|.*/||' | grep -oE '^[0-9]{6}' | sort -u`, same for `.down.sql`), compare sets with `comm -3`; inequality reddens.
- **G2 Duplicate:** any version appearing in more than one up file or more than one down file (`… | sort | uniq -d`) reddens.
- **G3 Malformed migration-like:** for every non-directory file whose basename is migration-like (§3.1 predicate) but does not match the canonical regexp (`grep -qE '^[0-9]{6}_.+\.(up|down)\.sql$'`), reddens; non-migration files ignored.
- **G0 Makefile-recipe tripwire:** `grep -qE '^[[:space:]]*@bash scripts/check-migration-set\.sh[[:space:]]*$'` on the repo Makefile (anchored recipe line, mirroring `check-audit-swallow.sh`); removing the target reddens the gate.
- **G-LIVE fixture liveness:** the fixture tree `scripts/testdata/migration-set/` must exist; missing → gate fails.

Self-test doctrine (run on **every** invocation; fixtures are part of the gate, mirroring `check-load-scenarios.sh`):
- `scripts/testdata/migration-set/positive/unpaired/migrations/` — `000001_a.up.sql`, `000001_a.down.sql`, `000060_x.up.sql` (no down). Designated G1 must fire; G2, G3 silent.
- `scripts/testdata/migration-set/positive/duplicate/migrations/` — `000060_x.up.sql`, `000060_x.down.sql`, `000060_y.up.sql`. Designated G2 must fire; G1 silent (up-set `{60}` == down-set `{60}`), G3 silent (all canonical).
- `scripts/testdata/migration-set/positive/malformed/migrations/` — `000001_a.up.sql`, `000001_a.down.sql`, `000060_x.UP.sql` (may also add `000060_x.sql`). Designated G3 must fire; G1 silent (`*.up.sql` glob does not match `UP.sql`), G2 silent.
- `scripts/testdata/migration-set/negative/clean/migrations/` — `000001_a.up.sql`/`.down.sql`, `000002_b.up.sql`/`.down.sql`, plus `README.md` (non-migration must be ignored). All rules silent.
- Each positive fixture must redden **exactly** its designated rule (anti-vacuity + over-breadth sweep, mirroring `check-load-scenarios.sh:226-264`); the negative fixture must be clean for all rules.

Real-tree scan: run G1–G3 against `$ROOT/migrations` (fixtures are never scanned as the real tree). Success summary line: `✓ Migration-set check passed (G1: up/down pairs; G2: no duplicate versions; G3: no malformed migration files)`.

### 4.4 Wiring — Makefile, AGENTS.md, CI (REQ-5)

**Makefile:**
- Add `check-migration-set` to the `.PHONY` list at line 6.
- Add the target beside `check-load-scenarios` (~line 52):
  ```make
  .PHONY: check-migration-set
  check-migration-set:
  	@bash scripts/check-migration-set.sh
  ```
- Add `migrate-validate` to the migration tooling block (~lines 143-148):
  ```make
  .PHONY: migrate-up migrate-down migrate-version migrate-validate
  migrate-validate:
  	go run ./cmd/migrate -config $(CONFIG) -migrations $(MIGRATIONS_DIR) validate
  ```

**AGENTS.md:**
- Add `make check-migration-set` to the done-gate block (adjacent to the other `check-*` entries).
- Extend the "Migrations are not auto-applied…" gotcha bullet with one sentence: the zero-padded-pair / duplicate / malformed invariants are mechanically enforced by `make check-migration-set` and strict `Discover()`.

**`.github/workflows/ci.yml` `check-gates` job (lines 92-108):** append
```yaml
      # Migration-set integrity gate: G1 up/down pairs, G2 no duplicate
      # versions, G3 no malformed migration files (bash + grep only — no Go
      # toolchain, no Docker, no network). No paths filter: a filter would
      # recreate the blind spot this gate closes.
      - name: Migration-set integrity gate
        run: make check-migration-set
```

### 4.5 Tests (REQ-3, REQ-7)

All new unit tests are `t.TempDir()`-based — no DB, no Docker, run under `make test`:

1. `TestDiscoverDuplicateVersion` — `000060_x.up.sql` + `000060_x.down.sql` + `000060_y.up.sql` + `000060_y.down.sql` → error naming both files; same-direction-only `000060_x.up.sql` + `000060_y.up.sql` → error. (Map-merge regression pin; **fails on the pre-change tree**.)
2. `TestDiscoverUnpaired` — up-only → error; down-only → error.
3. `TestDiscoverNameMismatch` — `000060_x.up.sql` + `000060_y.down.sql` → error.
4. `TestDiscoverMalformed` — `000060_x.UP.sql`, `000060_x.sql`, `abc_bad.up.sql` → error; include `42_thing.up.sql` to pin the §3.1 fix.
5. `TestDiscoverIgnoresNonMigration` — `README.md`, `notes.txt`, `migrations_test.go` + two valid pairs → no error; result contains exactly the two valid migrations, ascending.
6. `TestUpUnpaired` — `migrate.New(nil, dir, nil).Up()` on the up-without-down fixture → error (proves REQ-2 fail-fast, DB-free).
7. `TestValidateSet` — pure-function coverage: all applied / some pending / applied-without-files / empty applied → asserts `Pending`, `AppliedNoFiles`, `Pairs`.
8. Update `TestDiscover`: remove the deliberately unpaired `000003_c.up.sql` case (now errors) into `TestDiscoverUnpaired`; preserve pairs + `notes.txt` ignored + ascending order.

Integration (Docker-gated, runs under `make test-integration` only): extend `internal/pkg/migrate/migrate_integration_test.go` with an up-then-validate roundtrip — `migrate.New(db, dir, nil).Up()` then `Validate()` → 59 pairs, 0 pending, 0 applied-without-files against the real `migrations/` tree.

Recommended (REQ-7.3, strengthens fixture liveness from the Go side): a small test resolving `../../scripts/testdata/migration-set/positive/{duplicate,unpaired}/migrations` via `runtime.Caller(0)` (same technique as `migrations_test.go`) asserting `Discover` errors — `make test` then also liveness-pins the bash fixtures.

---

## 5. Compatibility constraints

| Surface | Change | Compatibility |
|---|---|---|
| `migrate.Discover` | strict D1-D5 | Signature unchanged; new error paths. All callers already propagate errors. |
| `migrate.(*Migrator).Up/Down` | `Discover` before `ensureTable` | Signatures unchanged; fail-before-mutate. No test or production flow depends on the old order. |
| `migrate.ValidateResult`, `ValidateSet`, `(*Migrator).Validate` | new | Purely additive; no existing symbol changes. |
| `cmd/migrate` | `validate` subcommand + usage line | Additive; `up`/`down`/`version` behavior unchanged. |
| `scripts/check-migration-set.sh`, `scripts/testdata/migration-set/` | new | Additive. |
| `Makefile`, `AGENTS.md`, `ci.yml` | new target / docs / step | Additive. |
| `parseFilename` | unchanged (permissive) | Pinned by `TestParseFilename`; canonical enforcement is in `Discover`/gate only. |
| `Version()` | unchanged | No disk diff; drift is `validate`'s job. |
| `migrations/*.sql` naming contract | enforced, not changed | Current tree 59/59 passes untouched. |
| `schema_migrations` DDL, migrations SQL | untouched | No SQL migration needed for this change. |
| `testpg.Migrate` / `migrations_test.go` loops | untouched | They never call `Discover`; unaffected by strictness. |
| `internal/app/new.go` auto-migrate | unchanged | `Up()` now fail-before-mutate on broken sets — strictly safer. |
| go.mod | unchanged | stdlib `regexp` only; bash+grep gate. |

Constraint on future files: any new file placed in `migrations/` must match the canonical form and arrive as a pair — the gate and strict `Discover` will reject anything else at CI time and at `Up`/`Down`/`validate` time respectively. Files that are not migrations must not be named migration-like (§3.1) or they will be rejected as D4.

---

## 6. Failure modes and mitigations

| # | Failure mode | Behavior | Mitigation / residual risk |
|---|---|---|---|
| F1 | A repo with a tolerated-broken set (duplicate version, unpaired, malformed) runs `Up`/`Down` | Now errors **before** `schema_migrations` is created or any SQL runs (fail-before-mutate). Previously it applied the second file silently / created the table / failed late. | This is the intended contract change; current tree is 59/59 healthy, so nothing existing breaks. The one test asserting old tolerance (`TestDiscover`) is updated in the same change. Operator remedy on a real broken set: fix the offending file, then rerun. |
| F2 | Classification false positive — a legitimately non-migration file that is migration-like (e.g. `000123_notes.sql`, `backup.up.sql`) | D4 error / G3 redden | Intentional per contract; documented in the script header and this design. Non-migration files in `migrations/` must avoid migration-like names. Residual risk accepted. |
| F3 | Go layer and bash gate drift apart | Gate fixtures or Go tests eventually catch asymmetry; both implement the same §3.1 contract and the same canonical regexp. | Duplicate/unpaired/malformed fixtures reddened on every gate invocation; recommended Go-side fixture scan pins both sides under `make test`. |
| F4 | Gate goes vacuous (stops catching regressions) | Missing fixtures fail the gate; every positive fixture must redden exactly its designated rule; Makefile-recipe tripwire (G0) prevents target removal; CI `check-gates` runs on every PR/push with no paths filter. | Self-test doctrine is part of the gate, executed on every invocation. |
| F5 | `validate` against an unreachable DB | DB-open or existence-query error → `migrate: <err>` to stderr, exit 1. | Documented CLI contract; integrity-only (DB-free) checking is the gate's job, drift checking is `validate`'s. |
| F6 | Schema/search-path mismatch between the existence gate and `ensureTable` | `Validate()` uses `table_schema = current_schema()`, i.e. the first schema in the connection's search_path — the same schema the DDL's `CREATE TABLE IF NOT EXISTS schema_migrations` resolves to. | Consistent by construction; no behavior change for the default DSN (public first). |
| F7 | Concurrent `Up` and `validate` on the same DB | `validate` reads a point-in-time applied set; the report may be momentarily stale. | Read-only command; at-least-once semantics of the runner unchanged. Documented as a point-in-time snapshot. |
| F8 | An applied migration edited after apply | Not detected in this change (no content hash). | Accepted residual risk; REQ-8 reserves the `content_hash` phase (§8) to close it. |
| F9 | Function-size/complexity creep in `Discover`/`validate` path | lint-strict (funlen 50 / cyclop 10) enforced in acceptance gates. | Explicit requirement to extract helpers; no new `nolint` exemptions. |
| F10 | Version `0` (`000000_x.up.sql`) or applied-file-deleted edge | `000000` not rejected (out of scope, §9); deleted-applied-file surfaces as `AppliedNoFiles` in `validate` and as `Down`'s existing "has no migration files on disk" error. | Documented; drift is `validate`'s job, not `Version()`'s. |

---

## 7. Migration steps (landing plan)

No SQL migration is required — this is Go code, a bash gate, fixtures, and wiring. Landing order (each step keeps the tree green):

1. **Tests red (pre-change tree):** add `TestDiscoverDuplicateVersion`, `TestDiscoverUnpaired`, `TestDiscoverNameMismatch`, `TestDiscoverMalformed`, `TestDiscoverIgnoresNonMigration`, `TestUpUnpaired`, `TestValidateSet`; update `TestDiscover`. Run `go test ./internal/pkg/migrate -count=1 -run 'TestDiscoverDuplicateVersion' -v` → **fails** (red-before proof: the map-merge accepts the fixture).
2. **Implement strict `Discover` + helpers (§4.1), fail-fast reorder of `Up`/`Down`, `ValidateResult`/`ValidateSet`/`Validate`.** Run `make test` (all green, including the new tests), `make lint`, `make lint-strict`, `make check-file-sizes`, `make check-integration-compile`.
3. **Add `validate` subcommand + usage + helper (§4.2).** Run `make lint-strict` (no new nolint), `make build`.
4. **Add the gate script + fixtures (§4.3).** Run `make check-migration-set` → self-test sweep + real-tree scan green. Prove reddening: delete `scripts/testdata/migration-set/positive/unpaired/` → gate fails; remove the Makefile recipe line → gate fails; copy the real `migrations/` to a temp root, add `000060_x.up.sql` with no down → G1 reddens.
5. **Wire Makefile, AGENTS.md, CI (§4.4).** Run the full done-gate chain (now including `make check-migration-set`).
6. **Integration roundtrip (Docker):** extend `migrate_integration_test.go` (up → validate = 59/59, no drift); run `make test-integration`.
7. **Recommended:** Go-side fixture liveness test (REQ-7.3).
8. **Optional manual `validate` smoke:** `go run ./cmd/migrate -config configs/config.yaml -migrations migrations validate` against a healthy migrated DB → exit 0 with `pending: 0; applied-without-files: 0`; against a DB with no `schema_migrations` → exit 0, all pending, table still absent afterwards; against an unpaired fixture dir → non-zero with `migrate:` stderr.

---

## 8. Phase-2 reservation: `content_hash` (REQ-8 — designed, **not implemented**)

Closed in this change as a design reservation only:

- Opt-in (config key or CLI flag, e.g. `-content-hash`): `Up()` records `sha256` of the raw up-file bytes in a new `content_hash TEXT` column of `schema_migrations`.
- Idempotent provisioning: `content_hash` in the `ensureTable` DDL (CREATE TABLE IF NOT EXISTS) plus `ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS content_hash TEXT` on the ensure path for existing DBs.
- Backfill: existing rows keep `NULL` = **unverified**; `validate` reports `NULL` as informational and a non-NULL hash mismatch as "edited after apply" (error, exit 1) — closing "never edit an applied migration" as convention-only.
- This change delivers nothing for REQ-8 except this reservation; the DDL, `applyTx`, and `ValidateResult` are shaped so the later phase is additive (new field, new column, new validation branch).

---

## 9. Out of scope (explicit non-goals)

- **No content-hash implementation** (§8 is design-only).
- **No CLI dispatch test suite for `cmd/migrate` `run()`/flags** (separate selected direction, analysis item 3); only the `validate` path's pure logic is unit-tested here.
- **No delegation of `testpg.Migrate` / `migrations_test.go` loops to `migrate.Migrator`** (separate selected direction, analysis item 2). Those runners are unaffected by strict `Discover` since they don't call it.
- **No change to `Version()` semantics** (no disk diff inside `Version`); drift reporting belongs to `validate`.
- **No rejection of version `0`** (`000000_x.up.sql`) and **no gate on "edited applied migration"** beyond the reserved REQ-8 design.
- **No change to `parseFilename`'s permissive parsing contract** (pinned by `TestParseFilename`); canonical-form enforcement lives in `Discover` and the gate.

---

## 10. Testable acceptance mapping

| Acceptance criterion (spec §5) | Testable check | Where / how |
|---|---|---|
| AC-1: `make test` green with new DB-free tests; `TestDiscoverDuplicateVersion` fails pre-change | `go test ./internal/pkg/migrate -count=1 -run 'TestDiscoverDuplicateVersion\|TestDiscoverUnpaired\|TestDiscoverNameMismatch\|TestDiscoverMalformed\|TestDiscoverIgnoresNonMigration\|TestUpUnpaired\|TestValidateSet' -v` all pass on the changed tree; `TestDiscoverDuplicateVersion` fails on the pre-change tree (red-before/green-after) | `make test`; git stash round-trip for the red proof |
| AC-2: `make check-migration-set` green on current tree; reddening proven three ways | exit 0 + G1/G2/G3 summary + self-test sweep clean; (a) delete `positive/unpaired/` → fail; (b) remove Makefile recipe line → fail (G0 tripwire); (c) temp-root copy of `migrations/` + orphan `000060_x.up.sql` → G1 reddens; each positive fixture reddens exactly its designated rule; negative clean | `make check-migration-set`; manual fault injection per §7 step 4 |
| AC-3: duplicate-version regression pin | `TestDiscoverDuplicateVersion` asserts error and message names both files (`000060_x` and `000060_y`) | unit test; fails pre-change (map-merge) |
| AC-4: unpaired-migration gate pin | `scripts/testdata/migration-set/positive/unpaired/` present; G1 reddens during every gate invocation | gate self-test (on-every-run) + fixture-liveness check |
| AC-5: `migrate validate` behavior | healthy migrated DB → exit 0, `pending: 0; applied-without-files: 0`; DB without `schema_migrations` → exit 0, all pending, table absent afterwards (existence gate, no `ensureTable`); unpaired fixture dir → non-zero `migrate:` stderr; `usage()` lists `validate`; `ValidateSet` + strict-`Discover` paths covered DB-free under `make test`; up→validate roundtrip = 59/59 under `make test-integration` | `go run ./cmd/migrate -config configs/config.yaml -migrations migrations validate`; `go test ./internal/pkg/migrate`; `make test-integration` |
| AC-6: all gates green | `make lint`, `make lint-strict`, `make check-file-sizes` (`cmd/migrate/main.go` stays ~≤160 lines; no non-test file >500), `make check-integration-compile`, `make build`, full done-gate incl. `make check-migration-set` | full AGENTS.md done-gate chain |
| AC-7 (recommended): Go-side fixture liveness | test resolving `../../scripts/testdata/migration-set/positive/{duplicate,unpaired}/migrations` via `runtime.Caller(0)` asserts `Discover` errors | `go test ./internal/pkg/migrate` (no Docker) |

---

## 11. Risks

| Risk | Mitigation |
|---|---|
| Strict `Discover` is a behavior change at a shared chokepoint (`Up` via `cmd/migrate` and `internal/app` auto-migrate) | Current tree is 59/59 healthy, so nothing existing breaks; the only test asserting the old tolerance (`TestDiscover`) is updated in the same change; the gate and Go share one canonical regexp so there is no Go/bash divergence. |
| Go layer and bash gate drift apart over time | Both implement the same §3.1 contract; the duplicate/unpaired/malformed fixtures are reddened by the gate on every invocation and (recommended) also scanned by a Go test, pinning both sides. |
| Up/Down reorder changes DB side effects | Fail-before-mutate is strictly safer (no `schema_migrations` created on a broken set); no test or production flow depends on the old order. |
| Gate goes vacuous (stops catching real regressions) | Self-test doctrine: every positive fixture must redden its designated rule and only it; missing fixtures fail the gate; Makefile-recipe tripwire prevents target removal; CI `check-gates` runs it on every PR/push with no paths filter. |
| Function-size/complexity gates after adding validation | Explicit requirement to extract helpers; no new `nolint` exemptions; lint-strict is in the acceptance gates. |
| Classification predicate misread by future editors | The corrected §3.1 rule is documented in the script header (canonical rule definition), in this design, and pinned by fixtures + unit tests including the `42_thing.up.sql` case. |
