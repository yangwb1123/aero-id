# Design — cmd/migrate parity: production migration binary test coverage + C-5 composition seam pin

**Date:** 2026-08-16
**Source analysis:** `docs/auto/analyses/cmd-server-7a3bfea7.json` (direction 1 of 3)
**Module scope boundary:** this design covers **only** the selected direction — `cmd/migrate` parity. The two sibling directions (cmd/server success-boot SIGTERM e2e; cmd/server fail-fast byte-exactness) are out of scope.

---

## 1. Verification verdict (evidence checked against the live tree)

The requirements evidence is **verified with two precision corrections and one empirical confirmation**. Every cited file/symbol was checked; no substantive claim was wrong.

| # | Claim | Verdict | Live-tree location |
|---|---|---|---|
| V1 | `cmd/migrate` has zero test files | **VERIFIED** | `cmd/migrate/` contains only `main.go`; `go test ./cmd/migrate/...` → `[no test files]` |
| V2 | `cmd/server` has 1 binary test | **VERIFIED** | `cmd/server/main_test.go:70` `TestServerBinaryActivationGateBoot` (only test; helpers `repoRoot`, `scrubAuditGovernanceEnv`, `buildServerBinary`) |
| V3 | `cmd/audit-replay` has 12+ integration tests | **VERIFIED** (15) | 3 files, all `//go:build integration`: `main_test.go` (5), `backlog_and_partial_test.go` (6), `i7_boot_hazard_test.go` (4) |
| V4 | `run()` ordering | **VERIFIED** | `flag.Parse` :52-58; missing-command :60; `config.Load` :64; `db.Open` :69; `migrate.New` :86; `switch` :88; `ParseDownArg` :106 (**inside** `down`, after `db.Open`); `unknown command` :141 (**inside** `default`, after `db.Open`) |
| V5 | Makefile migrate targets use `go run ./cmd/migrate` | **VERIFIED** (line drift noted) | `Makefile:146-161`: `MIGRATIONS_DIR` :146, `CONFIG` :147, `N` :149, targets :154-161 |
| V6 | C-1…C-4 cover only new.go/audit.go/cmd-server main; no migrate pin | **VERIFIED** | `scan_new_go` (C-1/C-2/C-3), `scan_audit_go` (C-2b), `scan_main_go` (C-4); live scans only `internal/app/new.go`, `internal/app/audit.go`, `cmd/server/main.go`; zero migrate references |
| V7 | audit-replay pre-flights before db.Open | **VERIFIED** (precision per spec) | `parseIDs` :90, `-actor` :99 → precede `config.Load` :106; signing-key check :113 sits **between** Load and `db.Open` :117 |
| V8 | `ParseDownArg` unit-tested; do not duplicate | **VERIFIED** (line drift) | `migrate.go:444`; error strings :450/:453; `TestParseDownArg` at `migrate_test.go:11` |
| V9 | `config.Load` is the shared validation seam | **VERIFIED** | `internal/config/config.go:384`; `read config: %w` wrap :393; governance gate in `validate.go` |
| V10 | `db.Open` / `migrate.New` signatures | **VERIFIED** | `internal/pkg/db/db.go:20`; `internal/pkg/migrate/migrate.go:62` |
| V11 | AGENTS.md: production uses `cmd/migrate` | **VERIFIED** | AGENTS.md "Current repository gotchas" |
| V12 | Supporting plans treat migrate's `config.Load` as load-bearing | **VERIFIED** | anchor-scheme design :31 (F3 boot coverage, cites `cmd/migrate/main.go:62`); operator-runbook :29 ("exactly once per binary… `cmd/migrate/main.go:62`"); activation-gate design :96 ("migrate is the first new-binary gate") |
| V13 | Missing-config byte-exact stderr | **VERIFIED EMPIRICALLY** | Built the binary; `-config <missing> up` → `migrate: load config: read config: open <path>: no such file or directory\n`, exit 1 — byte-exact match |
| V14 | `cmd/migrate/main.go` = 163 lines | **VERIFIED** | `wc -l` = 163 |
| V15 | Fixture layout + doctrine | **PARTIAL — see correction C-1** | `positive/{new,audit,main}.go` and `negative/{new,audit,main}.go` exist. **`positive/missing.go` is NOT a fixture file** — it is an intentionally nonexistent path the existing self-test feeds to `scan_new_go` to prove fail-loud scanning (`[ ! -f "$file" ]` branch). Implementers must not create a `missing.go` file |
| V16 | `nolint:cyclop,funlen` :48; package-doc `//nolint:cyclop` :18 | **VERIFIED** | both present with the quoted justifications |

**Corrections:**
- **C-1 (V15):** do not create `scripts/testdata/composition-root/positive/missing.go` as a fixture. The C-5 missing-file self-test reuses the *nonexistent-path probe* `"$FIXTURES/positive/missing.go"` exactly as the existing C-1…C-4 self-tests do. If a real `missing.go` were ever created containing scan tokens, the existing fail-loud self-test would itself redden (self-protecting).
- **C-2 (meta):** `docs/auto/analyses/cmd-server-7a3bfea7.json` is markdown-fenced JSON, not a raw JSON document; the direction title "cmd/migrate parity: the production migration binary has zero tests and no composition seam pin" is present verbatim. No substance impact.

**Empirical confirmation of the core defect:** against the current tree,
`migrate -config <missing> frobnicate` and `migrate -config <missing> down abc` both
print `migrate: load config: read config: open <missing>: no such file or directory`
(exit 1) — the *config* error surfaces because dispatch happens after `config.Load`.
Only the bare `migrate` (no command) fails with the intended CLI error. This is exactly
the ordering defect R-1 removes.

---

## 2. Problem statement (confirmed)

1. **Zero test coverage on the production migration binary.** `go test ./cmd/migrate/...` reports `[no test files]`. All `run()` error paths — missing command, unknown command, malformed `down` argument, config-load failure — are unpinned at the binary level, including the byte-exact `migrate: …` stderr contract that `cmd/server` and `cmd/audit-replay` already pin.
2. **Dispatch error paths require a live database.** Unknown command and malformed `down` argument reach `config.Load` (which loads the shipped `configs/config.yaml` successfully) and then `db.Open`; the failure surfaces as a connection error (or a hang against an unreachable DSN), not the intended CLI error. `cmd/audit-replay` demonstrates the correct pattern: CLI-input validation precedes `config.Load` (V7).
3. **No composition seam pin for migrate.** `check-composition-root.sh` pins the server's `app.New` seam (C-4) but nothing pins that `cmd/migrate` still loads config through the shared `config.Load` (the path that fires the governance activation gate and the F3 anchor-scheme boot-coverage contract, V12) and dispatches through `migrate.New`. A change routing migrate around `config.Load` (e.g. "migrate before governance") would ship green.

---

## 3. Design overview

Four coordinated, independently testable changes:

- **R-1** refactors `cmd/migrate/main.go`: CLI-input validation (command whitelist + `down` argument) moves **before** `config.Load`/`db.Open`, mirroring audit-replay pre-flight; the command dispatch is split into small per-command functions so no `nolint` suppression remains and every gate stays green.
- **R-2** adds a Docker-free `cmd/migrate/main_test.go` (package `main`, no build tag) pinning every error path byte-exactly.
- **R-3** extends `scripts/check-composition-root.sh` with a C-5 seam pin (exactly one `config.Load(` and exactly one `migrate.New(` in `cmd/migrate/main.go`) plus positive/negative/missing self-test fixtures.
- **R-4** removes both `nolint` directives (`:18`, `:48`); the pre-flight logic is linted unsuppressed.

### 3.1 R-1 — CLI pre-flight ordering refactor (`cmd/migrate/main.go`)

Target structure (error strings preserved verbatim from today):

```go
func run(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	cfgPath := fs.String("config", "configs/config.yaml", "path to config file")
	migrationsDir := fs.String("migrations", "migrations", "path to migrations directory")
	fs.Usage = func() { fmt.Fprintln(os.Stderr, usage()) }
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Pre-flight: CLI-input validation precedes config.Load and db.Open,
	// mirroring cmd/audit-replay's parseIDs/-actor placement (V7). An unknown
	// command or a malformed down count must fail without opening a database
	// connection and without touching the config file.
	cmd, downN, err := preflight(fs.Args())
	if err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath) // C-5: exactly one — keep the literal call
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// db.Open / sqlDB / defer close: unchanged from today.
	// logf: unchanged.
	m := migrate.New(gdb, *migrationsDir, logf) // C-5: exactly one — keep the literal call

	return dispatch(m, cmd, downN)
}
```

```go
// preflight validates the CLI command before any config/DB work. Extra
// positional args keep today's ignore behavior; only "down" consumes rest[1].
func preflight(rest []string) (string, int, error) {
	if len(rest) == 0 {
		return "", 0, fmt.Errorf("a command is required\n\n%s", usage())
	}
	cmd := rest[0]
	switch cmd {
	case "up", "version", "validate":
		return cmd, 0, nil
	case "down":
		var arg string
		if len(rest) > 1 {
			arg = rest[1]
		}
		n, err := migrate.ParseDownArg(arg)
		if err != nil {
			return "", 0, err
		}
		return cmd, n, nil
	default:
		return "", 0, fmt.Errorf("unknown command %q\n\n%s", cmd, usage())
	}
}

// dispatch runs the (pre-validated) command. The default arm is defensive and
// unreachable from run(); it keeps the historical error for direct callers.
func dispatch(m *migrate.Migrator, cmd string, downN int) error {
	switch cmd {
	case "up":
		return runUp(m)
	case "down":
		return runDown(m, downN)
	case "version":
		return runVersion(m)
	case "validate":
		return validateCmd(m)
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage())
	}
}
```

`runUp`/`runDown`/`runVersion` copy the current switch bodies **byte-for-byte** —
`already up to date`; `applied %d migration(s); now at version %06d`; `nothing to roll
back`; `rolled back %d migration(s); now at version %06d`; `no migrations applied
(version 0)`; `%06d`. `validateCmd` is untouched. The `down` case uses the
pre-parsed `downN` and never re-parses.

**C-5-compatibility constraint:** `config.Load(*cfgPath)` and `migrate.New(gdb, *migrationsDir, logf)` must remain **direct literal calls in `run()`**. Do not wrap config loading in a helper — the C-5 grep pins the literal tokens (R-3).

**Lint budget (R-4):** after the split no function exceeds the gates
(`funlen` 50 lines/40 statements, `cyclop` max-complexity 10, package-average ≤ 5.0,
`.golangci.yml:8-17`): `run` ≈ 30 lines, `preflight` ≈ 20, `dispatch` ≈ 10,
per-command helpers ≤ 15 each. Both `//nolint:cyclop` (:18 package doc) and
`//nolint:cyclop,funlen` (:48) are **removed**; nothing in the file carries a
suppression. If lint-strict unexpectedly objects (e.g. a new cyclop interaction),
the fallback is a targeted, justified nolint on `dispatch` only — never on
`preflight`.

### 3.2 R-2 — Docker-free `cmd/migrate/main_test.go` (package `main`)

No `//go:build integration` tag; runs under `make test`. Local helpers
`repoRoot(t)` and `buildMigrateBinary(t, root)` mirror `cmd/server/main_test.go`
(helpers do not cross packages — copy the small `repoRoot` walker).
`missingConfig(t)` returns `filepath.Join(t.TempDir(), "missing-migrate.yaml")` —
guaranteed absent, deterministic, and independent of any ambient `AERO_ID_*` env
(a missing file fails `ReadInConfig` regardless of env overrides; no env scrubbing
needed, unlike the server gate test which toggles governance env).

| Test | Invocation | Byte-exact assertion |
|---|---|---|
| `TestMigrateMissingCommand` | `run(nil)` | `err.Error() == "a command is required\n\n" + usage()` |
| | `run([]string{"-config", missingConfig(t)})` | same error — proves missing-command precedes `config.Load` |
| `TestMigrateUnknownCommand` | `run([]string{"-config", missingConfig(t), "frobnicate"})` | `err.Error() == "unknown command \"frobnicate\"\n\n" + usage()` — proves whitelist precedes `config.Load` (today this returns the config error) |
| `TestMigrateDownArgValidationAtCLI` | `run([…, "down", "0"])` | `invalid down count 0: must be >= 1` |
| | `run([…, "down", "abc"])` | `invalid down count "abc": must be a positive integer` |
| | `run([…, "down", "3"])` | `strings.HasPrefix(err.Error(), "load config: read config: ")` — valid down proceeds past pre-flight to `config.Load` |
| `TestMigrateConfigLoadFailure` | real binary via `buildMigrateBinary`, `-config <abs-missing>` + `up` | `*exec.ExitError`, exit code **1**, `string(out)` **exactly** `"migrate: load config: read config: open " + path + ": no such file or directory\n"` (full-output equality — "any extra line is a boot side effect") |

Notes:

- The three `run()`-level tests exercise only `fs.Parse` (no flag errors → no
  `flag` package stderr noise) and the pre-flight/config.Load chain; they never
  reach `db.Open`. The valid-`down` leg stops at `config.Load` because the config
  path is absent.
- The byte-exact binary string embeds `os.PathError` text
  (`open <path>: no such file or directory`); CI is Linux — consistent with the
  existing `cmd/server` byte-exact doctrine.
- Do **not** duplicate `ParseDownArg` unit coverage (V8 — `migrate_test.go:11`).
  The CLI tests assert only that `run()` surfaces those errors before any
  config/DB touch.

### 3.3 R-3 — C-5 composition seam pin (`scripts/check-composition-root.sh`)

Header addition (after C-4):

```text
#   C-5  cmd/migrate/main.go must contain exactly one `config.Load(` — the
#        shared validation seam that fires the governance activation gate and
#        the F3 anchor-scheme boot coverage — and exactly one `migrate.New(`
#        (the dispatch seam). A drift that routes migrate around config.Load
#        (e.g. "migrate before governance") must redden here.
```

Scanner (mirrors `scan_main_go`'s shape):

```bash
scan_migrate_main_go() { # <path> — C-5
  local file="$1" violations=""
  if [ ! -f "$file" ]; then echo "cmd/migrate/main.go missing — re-anchor check-composition-root"; return 1; fi
  local n
  n="$(grep -cF -- 'config.Load(' "$file")"
  [ "$n" = "1" ] || violations="${violations}C-5 config.Load seam: found $n, want exactly 1 (migrate must keep the shared config validation gate)
"
  n="$(grep -cF -- 'migrate.New(' "$file")"
  [ "$n" = "1" ] || violations="${violations}C-5 migrate.New dispatch seam: found $n, want exactly 1
"
  if [ -n "$violations" ]; then printf '%s' "$violations"; return 1; fi
  return 0
}
```

Self-tests (same doctrine as C-1…C-4; wire the negative result into the existing
combined negative check or a C-5-specific message):

```bash
scan_migrate_main_go "$FIXTURES/positive/migrate_main.go" >/dev/null 2>&1; r5=$?
[ "$r5" = "0" ] && { echo "SELF-TEST FAIL: positive/migrate_main.go matched nothing"; fail=1; }
scan_migrate_main_go "$FIXTURES/negative/migrate_main.go" >/dev/null 2>&1; rm5=$?
[ "$rm5" != "0" ] && { echo "SELF-TEST FAIL: negative/migrate_main.go matched"; fail=1; }
scan_migrate_main_go "$FIXTURES/positive/missing.go" >/dev/null 2>&1; rmiss5=$?
[ "$rmiss5" = "0" ] && { echo "SELF-TEST FAIL: missing cmd/migrate/main.go did not fail loud"; fail=1; }
```

Live scan + summary:

```bash
scan_migrate_main_go "$ROOT/cmd/migrate/main.go" || fail=1
# final summary mentions the migrate seam, e.g.:
# "✓ check-composition-root: buildAuditStack + drain Start wired in the server
#    root; cmd/migrate keeps the shared config.Load + migrate.New seams (self-test ok)"
```

**Fixtures** (grep-only; never compiled — scripts/testdata is out of every Go tool
path and out of `check-file-sizes`, which scans only `internal/`):

- `scripts/testdata/composition-root/positive/migrate_main.go` — the **config.Load
  bypass** (0 `config.Load(` occurrences → C-5a violation → must redden):

  ```go
  // Positive fixture: config loaded through a bypass loader — C-5 must flag
  // (zero config.Load occurrences means the shared validation seam is gone).
  package main

  func main() {
  	cfg, err := loadConfigOverride(*cfgPath)
  	if err != nil {
  		panic(err)
  	}
  	m := migrate.New(gdb, *migrationsDir, logf)
  	_ = m
  	_ = cfg
  }
  ```

- `scripts/testdata/composition-root/negative/migrate_main.go` — exactly one
  `config.Load(` and exactly one `migrate.New(` (mirrors the live shape) → must pass:

  ```go
  // Negative fixture: mirrors the live seam shape — exactly one config.Load and
  // one migrate.New — C-5 must pass.
  package main

  func main() {
  	cfg, err := config.Load(*cfgPath)
  	if err != nil {
  		panic(err)
  	}
  	gdb := openDB(cfg)
  	m := migrate.New(gdb, *migrationsDir, logf)
  	_ = m
  	_ = cfg
  }
  ```

- **Missing-file probe:** pass the nonexistent path `"$FIXTURES/positive/missing.go"`
  exactly as the existing C-1…C-4 self-tests do. Do **not** create a `missing.go`
  fixture (correction C-1).

The live file has zero `config.Load(`/`migrate.New(` tokens in comments or usage
text (verified: doc comments and usage never contain the literals), so
`grep -cF` counts are unambiguous. Keep the fixtures token-clean in comments too.

### 3.4 R-4 — nolint re-scoping

- Remove `//nolint:cyclop` (main.go:18, package doc) and `//nolint:cyclop,funlen`
  (main.go:48).
- `preflight` (the new logic) is linted **without suppression**; `make lint-strict`
  is the gate. Fallback per §3.1: targeted nolint on `dispatch` only, never on
  `preflight`.

---

## 4. API changes

**Public/user-facing: none in the happy path.** `cmd/migrate` is a CLI binary; its
contract is the CLI surface and printed output, both frozen (below). No exported Go
API changes: `run(args []string) error` keeps its signature; new symbols are
unexported (`preflight`, `dispatch`, `runUp`, `runDown`, `runVersion`).

**Intentional observable change (error paths only):** unknown command and malformed
`down` argument now fail **before** `config.Load`/`db.Open` with the clean CLI error
instead of a DB-connection error (or a hang against an unreachable DSN). This is the
point of the change (G-1) and is strictly a failure-mode improvement: it is
Docker-free, deterministic, and removes the DB from the invalid-input path. Exit
codes are unchanged (1 on every error). `main()`'s `migrate: <err>` stderr wrap is
unchanged.

**New files:** `cmd/migrate/main_test.go`,
`scripts/testdata/composition-root/positive/migrate_main.go`,
`scripts/testdata/composition-root/negative/migrate_main.go`.

**Modified files:** `cmd/migrate/main.go`, `scripts/check-composition-root.sh`.
Everything else is untouched (see §5).

**Frozen strings** (byte-identical; do not edit):
- `usage()` — `usage: migrate [-config path] [-migrations dir] <command>\n\ncommands:\n  up            apply all pending migrations\n  down [N]      roll back the most recent migration (or the most recent N)\n  version       print the current schema version\n  validate      report migration-set integrity and disk-vs-applied drift without mutating` (no trailing newline).
- `run()` errors: `a command is required\n\n` + usage; `unknown command %q\n\n` + usage; ParseDownArg errors from `migrate.go:450/453`.
- Command output: `already up to date`; `applied %d migration(s); now at version %06d`; `nothing to roll back`; `rolled back %d migration(s); now at version %06d`; `no migrations applied (version 0)`; `%06d`; `validateCmd` summary.
- `main()` wrap: `migrate: ` prefix, stderr, exit 1.
- `config.Load` wrap chain: `read config: %w` (`config.go:393`) → `load config: %w` (`run()`).

---

## 5. Compatibility constraints

1. **Valid-command behavior is frozen.** `up`/`down`/`version`/`validate` output and
   exit semantics are byte-identical (per-command bodies copied verbatim;
   `validateCmd` untouched). The `internal/pkg/migrate` integration tests
   (`migrate_integration_test.go`) continue to cover real Migrator behavior.
2. **No changes to** `internal/pkg/migrate` (incl. `ParseDownArg` semantics),
   `internal/config`, `cmd/server`, `cmd/audit-replay`, Makefile targets,
   migrations, protobuf, configs, or audit code. `make check-migration-set`,
   `check-proto-sync`, `check-audit-*`, `check-anchored-event-ids`,
   `check-load-scenarios` are unaffected.
3. **C-5 literal-call constraint.** `cmd/migrate/main.go` must keep exactly one
   literal `config.Load(` and one literal `migrate.New(` — the R-1 refactor
   preserves them inline in `run()`; any future refactor that wraps either call in
   a helper reddens the gate by design.
4. **Makefile targets keep working.** `migrate-up/down/version/validate` invoke
   `go run ./cmd/migrate` with the same flags; `migrate-down` passes `down $(N)`.
   For valid `N` behavior is identical; for invalid `N` ("0", "abc") the error now
   surfaces fast, pre-DB (improvement, no contract break).
5. **Test file is untagged** → runs under `make test` (Docker-free, no DB).
   `make check-integration-compile` is unaffected (no signature changes; untagged
   test compiles in both modes).
6. **Do not duplicate** `ParseDownArg` unit tests (V8).
7. **Fixture hygiene:** `scripts/testdata/composition-root/` files are grep
   fixtures; keep their comments free of the pinned literals and never create a
   `positive/missing.go` file.
8. **`check-file-sizes`** scans only `internal/`; `cmd/migrate/main.go` stays at
   ~150 lines (163 today, ≤ 500 gate) and the fixtures are not scanned.

---

## 6. Failure modes

| Failure | Detection | Mitigation |
|---|---|---|
| Regression moving whitelist/down-arg checks after `config.Load` | `TestMigrateUnknownCommand` / `TestMigrateDownArgValidationAtCLI` redden (they pass a nonexistent `-config`, so a post-Load placement returns the config error instead of the CLI error) | R-1 ordering + these tests are the tripwire |
| Valid-command output drift | per-command helpers copy text verbatim; `internal/pkg/migrate` integration tests cover real Migrator behavior; valid-`down` leg of `TestMigrateDownArgValidationAtCLI` pins "reaches `config.Load`" | R-1 wording discipline; code review |
| C-5 false pass (0 or 2 `config.Load(`/`migrate.New(`) | positive fixture (bypass, 0 occurrences) must redden; live scan counts == 1 | grep `-cF` equality; self-test doctrine |
| C-5 false fail (comment/docblock token inflation) | verified zero literals in comments today; fixtures kept token-clean | documented hygiene rule |
| C-5 false fail from wrapping `config.Load` in a helper | gate reddens | documented constraint (§5.3) |
| Implementer creates `positive/missing.go` as a real file | the existing fail-loud self-tests (`scan_new_go … missing.go`) would then *not* fail loud → `SELF-TEST FAIL` | correction C-1 documented in this design |
| Non-Linux `os.PathError` text breaks byte-exact assertion | test fails on non-Linux | CI is Linux; same doctrine as `cmd/server` |
| `go build` in binary test is slow/flaky | 30s timeout pattern from `cmd/server` | reuse the precedent (`buildServerBinary`); temp dir per run |
| `make lint-strict` rejects new code | funlen/cyclop on unsuppressed `preflight` | keep functions small (§3.1 budget); fallback = targeted nolint on `dispatch` only |
| Ambient config env changes `run()`-level behavior | `AERO_ID_*` env cannot make a missing file load (`ReadInConfig` fails regardless); tests use `t.TempDir()` paths | deterministic by construction |

---

## 7. Migration steps (implementation sequence)

> Only the deliverable files listed here change. Run each gate after its step.

1. **R-1 refactor `cmd/migrate/main.go`** — introduce `preflight`, `dispatch`,
   `runUp`, `runDown`, `runVersion`; move the `down`-arg parse before `config.Load`;
   keep `config.Load`/`migrate.New` literal in `run()`; delete both `nolint`
   directives. *Gate:* `make lint` && `make lint-strict` && `make build`.
2. **R-2 add `cmd/migrate/main_test.go`** — four tests + `repoRoot`/
   `buildMigrateBinary`/`missingConfig` helpers. *Gate:* `make test`
   (`go test ./cmd/migrate/... -count=1` runs all four green). Sanity: the two
   regression tests fail against the pre-refactor binary ordering — run them
   against a stash of step 1 if a regression check is desired.
3. **R-3 extend `scripts/check-composition-root.sh`** — C-5 header entry,
   `scan_migrate_main_go`, three self-test probes, live scan, updated `✓` summary.
   Add the two fixture files. *Gate:* `make check-composition-root`.
4. **R-4 verify nolint removal** — `grep -n nolint cmd/migrate/main.go` returns
   nothing; `make lint-strict` green with `preflight` unsuppressed.
5. **Full gate** — `make test && make lint && make lint-strict && make
   check-composition-root && make check-integration-compile && make build`.
   The remaining done-gate items (`check-file-sizes`, `check-audit-swallow`,
   `check-anchored-event-ids`, `check-audit-manifest`, `check-load-scenarios`,
   `check-migration-set`, `check-proto-sync`) are untouched and stay green.
6. **Sanity probes (not committed):** temporarily delete the `config.Load(`
   line from `negative/migrate_main.go` → gate reddens; restore. Temporarily
   delete it from the live `cmd/migrate/main.go` → gate reddens; restore. Both
   prove the pin is live.

---

## 8. Testable acceptance mapping

| Requirement | Testable proof | Fails on |
|---|---|---|
| R-1 — pre-flight before `config.Load` | `TestMigrateMissingCommand` leg 2, `TestMigrateUnknownCommand`, `TestMigrateDownArgValidationAtCLI` legs 1–2 assert CLI errors with a nonexistent `-config`; leg 3 asserts a valid `down` reaches `config.Load` (`load config: read config: ` prefix) | pre-refactor binary ordering |
| R-1 — output preservation | per-command bodies byte-copied; `validateCmd` unchanged; `internal/pkg/migrate` integration tests green; valid-`down` leg pins progression to Load | drift in any helper |
| R-2 — binary-level coverage | `make test` includes the four new Docker-free tests; `TestMigrateConfigLoadFailure` asserts exit 1 + byte-exact stderr on the real binary | any extra stderr line, wrong exit code, or refactor drift |
| R-3 — C-5 pin | `make check-composition-root` green with C-5: positive fixture (bypass) reddens, negative fixture passes, missing path fails loud, live scan passes; sanity probes in step 6 redden on removal | removing either seam call |
| R-4 — lint integrity | `make lint-strict` green; `grep -n nolint cmd/migrate/main.go` empty | reintroducing blanket suppression |
| No collateral | `make check-integration-compile`, `make build`, and the untouched gates stay green; diff limited to the 5 listed files | — |

---

## 9. Out of scope

- Sibling directions from the same analysis file (cmd/server SIGTERM success-boot
  e2e; cmd/server fail-fast byte-exactness) — separate work items.
- Any change to `internal/pkg/migrate`, `internal/config`, `cmd/server`,
  `cmd/audit-replay`, Makefile targets, migrations, protobuf, or configs.
- Integration (testcontainers/testpg) tests for migrate — the four new tests are
  Docker-free by design.
- New audit/ledger behavior in `cmd/migrate` (2026-08-07 sweep C8 contract
  preserved — no audit code lands here).
- A generic loader abstraction or a second config mechanism — C-5 pins the existing
  single `config.Load` seam and nothing more.

---

## 10. References

- `cmd/migrate/main.go` (163 lines; `run()` :49, `//nolint:cyclop,funlen` :48,
  package-doc `//nolint:cyclop` :18, `config.Load` :64, `db.Open` :69,
  `migrate.New` :86, `ParseDownArg` :106, `unknown command` :141)
- `cmd/server/main_test.go` (:16 gate-error const, :70 `TestServerBinaryActivationGateBoot`, helpers `repoRoot`/`scrubAuditGovernanceEnv`/`buildServerBinary`)
- `cmd/audit-replay/main.go` (:76 run, :90 `parseIDs`, :99 `-actor`, :106 `config.Load`, :113 signing-key, :117 `db.Open`)
- `internal/pkg/migrate/migrate.go` (:62 New, :444 `ParseDownArg`, :450/:453 error strings), `migrate_test.go` (:11 `TestParseDownArg`), `migrate_integration_test.go`
- `internal/config/config.go` (:384 Load, :393 `read config: %w`), `internal/config/validate.go`, `internal/pkg/db/db.go` (:20 Open)
- `scripts/check-composition-root.sh` (C-1…C-4 header, self-tests, live scans :103-105), `scripts/testdata/composition-root/{positive,negative}/`
- `Makefile:146-161` (migrate tooling), `Makefile:122-124` (`check-composition-root` target), `Makefile:66-90` (`check-file-sizes` scans `internal/` only)
- `.golangci.yml` (funlen 50/40, cyclop 10/5.0, lll 160)
- `docs/plans/2026-08-08-configs-anchor-scheme-declaration-design.md:31` (F3),
  `docs/plans/2026-08-08-operator-runbook-stress-test.md:29`,
  `docs/plans/2026-08-07-internal-config-b2-6-activation-gate-design.md:96`,
  `docs/plans/2026-08-07-cmd-migrate-sweep-and-lock-in-tx-audit-conversion-design.md` (C8)
- `docs/auto/analyses/cmd-server-7a3bfea7.json` (direction 1; markdown-fenced JSON)
