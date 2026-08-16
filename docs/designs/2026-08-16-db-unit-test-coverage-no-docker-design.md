# Design — Deterministic Unit-Test Coverage for `internal/pkg/db` (no Docker)

Status: **Proposed** (implementation-ready)
Date: 2026-08-16
Module: `internal/pkg/db` (test-only change set)
Spec: Requirements spec `docs/auto/runs/deterministic-unit-test-coverage-for-internal-pk-d5b01057/artifacts/requirements-10762e10/requirements.md`
Baseline verified at: HEAD — every evidence claim re-verified against the live tree and by direct probe on 2026-08-16. Two evidence claims (E5, E10) are **materially wrong** and corrected in §1 deltas; the change set itself survives both corrections.

---

## 1. Verification verdict (untrusted evidence → tree)

| # | Claim (evidence) | Verdict | Tree evidence |
|---|---|---|---|
| E1 | `db_test.go` is `//go:build integration`, `package db_test`, exactly one test `TestPing` calling `testpg.Start(t)` then `db.Ping(g)`; no untagged test file exists in the package | **Confirmed** | `internal/pkg/db/db_test.go` (283 bytes) verbatim; `ls internal/pkg/db/` = exactly `db.go`, `db_test.go`. |
| E2 | `db.go` (claimed 66 lines): `Open` wraps `"gorm open: %w"`, `"get sql.DB: %w"`; `logLevel` switch `silent|error|info → *`, default `Warn`; `configurePool` anonymous-interface with `> 0` guards on all three setters | **Confirmed** (line count off by 3) | `db.go` is **69** lines (`wc -l`). Symbols, wrap strings, switch branches, and `> 0` guards match verbatim. |
| E3 | `test:` = `go test ./... -count=1`, no `-tags integration`; package executes zero tests under the default gate | **Confirmed** | `Makefile:14` exact. `go test -count=1 ./internal/pkg/db/` → `[no test files]` reproduced. |
| E4 | `test-integration:` = `go test -tags=integration -p 1 ./... -count=1`, Docker-backed | **Confirmed** | `Makefile:15` exact. |
| E5 | Lint gates: `lint` = `go vet ./...`; `lint-strict` = `golangci-lint run ./...`; `.golangci.yml` "sets `cyclop: tests: true` and `funlen: skip-tests: false` → limits apply to test functions" | **Partially wrong — see Delta 1** | `Makefile:16` `lint` ✓; `Makefile` `lint-strict` ✓. But `.golangci.yml` has `run.tests: true` (line 5 — *analyze* test files) and `skip-tests: false` (lines 11, 15) **overridden** by `issues.exclude-rules` (lines 41–50) which excludes `.*_test\.go$` from **funlen, cyclop, and lll**. Net effect: lint-strict **cannot** fail on test-function length/complexity. `check-file-sizes` excludes `*_test.go` ✓ (`Makefile:78`). |
| E6 | `check-architecture.sh` rules (a)–(f); `internal/pkg/db` appears in no rule; test-file-scoped | **Confirmed** | Script header rules (a)–(f) verbatim; (b)–(f) explicitly exclude `*_test.go`; `internal/pkg/db` in no banned list. |
| E7 | `go.mod` pins pgx v5.5.5, gorm postgres v1.5.9, gorm v1.25.11 | **Confirmed** | `go.mod:14,33,34` exact. |
| E8 | gorm logger constants: `Silent = iota + 1` = 1, `Error` = 2, `Warn` = 3, `Info` = 4 | **Confirmed** | `gorm.io/gorm@v1.25.11/logger/logger.go:38–45`. |
| E9 | Probe: `gorm.Open(postgres.Open("host=localhost sslmode=bogus"))` fails **synchronously at DSN parse, before any network dial**, with `cannot parse \`host=localhost sslmode=bogus\`: failed to configure TLS (sslmode is invalid)` | **Confirmed** | Probe re-run on this tree reproduces byte-for-byte. `pgx.ParseConfig` returns `&ParseConfigError{...}` before any `net.Dial`; gorm then **skips the automatic ping** (`gorm.go:203` `if err == nil && !config.DisableAutomaticPing`) because `Initialize` already failed — double-guaranteed offline. |
| E10 | "gorm wraps the pgx parse error with `%v` (`failed to initialize database, got error %v`), not `%w` — the wrap chain from pgx is broken. `errors.Is`/`errors.As` against pgx errors will **not** match" | **Wrong — see Delta 2** | The `failed to initialize database, got error %v` text is passed to `config.Logger.Error` (`gorm.go:214`) — a **log line, not an error wrap**. The postgres driver `Initialize` returns the raw `pgx.ParseConfig` error via bare `return` (no wrap, `driver/postgres@v1.5.9/postgres.go`), `gorm.Open` returns it raw, and `db.Open` wraps it with `%w`. Probe: `errors.As(err, new(*pgconn.ParseConfigError))` → **true**. |
| E11 | Internal (`package db`) + external (`package db_test`) test files may coexist in one directory; untagged internal file compiles/ runs under `go test ./...`; both compile under `-tags integration` | **Confirmed** | Reproduced in a scratch module: no-tags → only the untagged `package a` test runs; `-tags integration` → both run. |

### Deltas (content-affecting corrections adopted into this design)

1. **Delta 1 (E5): lint-strict does not bind test-file size/complexity.** The spec's §5/§6 claim that "`.golangci.yml` applies `cyclop` (`tests: true`) and `funlen` (`skip-tests: false`) to test files" is wrong on both counts: `tests: true` is `run.tests` (include test files in analysis), and the `issues.exclude-rules` block for `.*_test\.go$` (funlen, cyclop, lll) wins over the linter `skip-tests` settings. The non-exempt linters — `govet`, `staticcheck`, `gosec`, `errorlint`, `nilerr`, `noctx`, `bodyclose`, `unused` — still analyze test files, so the new file must be lint-clean in those respects (no unused imports, no bare `err == nil` comparisons against wrapped errors, etc.). The table-driven design remains the right choice (house style, per-case diagnostics, readable ≤ 40-line funcs) but is a **norm**, not a gate requirement. The done-gate in §9 therefore does not claim lint-strict protects the tests; it claims lint-strict stays green.
2. **Delta 2 (E10): the error chain survives; `errors.As` is available.** gorm only *logs* the `%v` text; it returns the raw `*pgconn.ParseConfigError`, and `db.Open`'s `fmt.Errorf("gorm open: %w", err)` preserves the chain. The spec's mandated `strings.Contains(err.Error(), "gorm open:")` assertion is correct and remains the primary pin (it asserts `db.Open`'s public wrap independent of pgx type drift). Optionally the test may **strengthen** R3 with `errors.As(err, new(*pgconn.ParseConfigError))`; this design makes the `errors.As` check an explicit, documented secondary assertion so the pin is both exact and future-proof. `errors.Is` against a sentinel is still not meaningful (pgx's `"sslmode is invalid"` is an inline `errors.New`, not a sentinel).
3. **Delta 3 (E2):** `db.go` is 69 lines, not 66. Cosmetic; no design impact.
4. Makefile line citations (14/15/78) verified exact; no delta.

---

## 2. Problem statement

`internal/pkg/db` is the connection foundation for the live composition roots (`internal/app/new.go`, `cmd/migrate`, `cmd/audit-replay`) and `internal/repository/testpg`. Its only test, `TestPing`, is integration-tagged and testcontainers-backed, so the default no-Docker gate `make test` executes **zero tests** in this package (`[no test files]` reproduced). The package's entire decision logic is unguarded:

- `logLevel(value string) logger.LogLevel` — `silent|error|info → *`, anything else (including `""` and `"warn"`) → `Warn` (`db.go:24–31`).
- `configurePool(...)` — `MaxOpenConns`, `MaxIdleConns`, `ConnMaxLifetime` applied only when strictly `> 0`; zero/negative skipped (`db.go:33–44`).
- `Open` error contract — DSN-parse failures surface as `gorm open: <pgx parse error>` (`db.go:18`); `get sql.DB: %w` for the pool path.
- `Ping` — used only by the integration test.

AGENTS.md requires "Unit tests live beside code and use deterministic fakes; they make no real cross-data-center calls." This package has none, and no regression baseline exists for the three behaviors above. Because this module underpins the other analyzed directions, its unit coverage is the precondition that makes those directions gate-verifiable.

---

## 3. Design overview

- **Exactly one new file**: `internal/pkg/db/db_internal_test.go`, **untagged**, `package db` (white-box — `logLevel` and `configurePool` are unexported). Go permits `package db` and `package db_test` test files to coexist in one directory (E11, probe-verified); the existing integration file is untouched and remains the only integration test in the package.
- **No production change**: `db.go`, `db_test.go`, `Makefile`, all gate scripts remain byte-for-byte unchanged. `git diff --stat` on the landed change touches only the new file.
- **Three table-driven unit tests** (one per requirement), offline and deterministic: no testcontainers, no network, no timing. The only test that touches `Open` uses the parse-failing DSN, probe-verified to fail before any I/O (E9) with the automatic ping skipped (gorm.go:203).
- **Corrections applied from §1 deltas**: R3 gains the `errors.As(*pgconn.ParseConfigError)` secondary pin (Delta 2); the gate/complexity claims are restated honestly (Delta 1).

---

## 4. API changes

### 4.1 Production API — none

No exported symbols added or changed; no error messages added or changed; no behavior tightening; no config, schema, or migration changes. `Open`, `logLevel`, `configurePool`, `Ping`, and `Options` remain exactly as shipped.

### 4.2 Test surface — `internal/pkg/db/db_internal_test.go`

```go
package db // white-box; logLevel and configurePool are unexported

import (
    "errors"
    "strings"
    "testing"
    "time"

    "github.com/jackc/pgx/v5/pgconn" // Delta 2: exact-type pin
    "gorm.io/gorm/logger"
)
```

| Test | Behavior under test | Cases | Key assertions |
|---|---|---|---|
| `TestLogLevel` | `logLevel` switch mapping (R1) | `silent→Silent`, `error→Error`, `info→Info`, `""→Warn`, `warn→Warn`, `WARN→Warn`, `ERROR→Warn`, `bogus→Warn` | Exact `logger.LogLevel` equality (constants pinned at E8) |
| `TestConfigurePool` | `> 0`-only application of the three pool setters (R2) | `(10, 5, time.Minute)`, `(0,0,0)`, `(-1,-5,-time.Second)`, `(0,3,0)`, `(7,0,250ms)` | Recording fake; per-case set/unset boolean flags + exact recorded values |
| `TestOpenMalformedDSN` | `Open` DSN-parse failure wrap (R3) | `DSN: "host=localhost sslmode=bogus"` | `err != nil`; `strings.Contains(err.Error(), "gorm open:")` (primary); `errors.As(err, new(*pgconn.ParseConfigError))` (secondary, Delta 2); gorm stderr log line documented as expected noise |

`TestConfigurePool` uses the deterministic recording fake specified in the evidence — `SetMaxOpenConns(int)`, `SetMaxIdleConns(int)`, `SetConnMaxLifetime(time.Duration)` — with boolean `setX` flags because int setters cannot distinguish "set to 0" from "not set". The fake satisfies the exact anonymous interface in `db.go:34–38`, so a signature drift in the production interface fails the compile.

`TestLogLevel` deliberately includes `"warn"` → `Warn`: there is **no explicit `warn` case** in the switch (`db.go:24–31`), so it documents the default branch, not a dedicated case. `""`, `"WARN"`, `"ERROR"`, `"bogus"` all land on the same default branch.

`TestOpenMalformedDSN` constraints (probe-verified):
- The DSN **must** fail at `pgx.ParseConfig` (invalid `sslmode`). A parse-valid DSN would trigger the automatic ping path or a lazy dial at first use → network dependence. `sslmode=bogus` fails before any I/O, and gorm skips the automatic ping entirely because `Initialize` already errored.
- Primary assertion is on the message prefix `"gorm open:"`, which pins `db.Open`'s own `%w` wrap. The pinned inner text `sslmode is invalid` is intentionally **not** asserted as a hard string — the `errors.As` check (Delta 2) pins the error *type* instead, which is stable across pgx minor bumps; the message text is shown in the test comment as documentation.
- The gorm default logger prints `[error] failed to initialize database, got error ...` to stderr during this test (gorm.go:214 log line). Expected noise; not a failure signal.

---

## 5. Compatibility constraints

| # | Constraint | Enforcement |
|---|---|---|
| C1 | No production code change | `git diff HEAD -- internal/pkg/db/db.go internal/pkg/db/db_test.go` empty; `git status --porcelain` shows only the new file |
| C2 | Exactly one new file, untagged | `git diff --name-only HEAD` = `internal/pkg/db/db_internal_test.go` |
| C3 | `TestPing` remains the only integration test in the package; integration file byte-identical | E1 re-check after landing |
| C4 | New file compiles under **both** tag modes | `go test ./internal/pkg/db/` (no tags) and `go test -tags integration -run '^$' ./internal/pkg/db/` (compile-only, no Docker) |
| C5 | No new build tags, no `//nolint`, no testcontainers, no network, no timing in the new file | Review + gate §9 |
| C6 | Default gate must now **execute** tests in this package (no `[no test files]`) | `go test -count=1 -v ./internal/pkg/db/` lists exactly `TestLogLevel`, `TestConfigurePool`, `TestOpenMalformedDSN` |
| C7 | Non-exempt linters apply to test files (Delta 1): keep imports used, no `err == nil` against wrapped errors, no unused symbols | `make lint-strict` green |
| C8 | No `t.Parallel`, no shared state with `TestPing`; the package stays sequential | Test bodies are self-contained tables |

Compatibility with the wider repo is trivially preserved: `internal/pkg/db` is not mentioned by any `check-architecture` rule (E6), `check-file-sizes` exempts `*_test.go` (Makefile:78), `check-composition-root` covers `cmd/*`/`internal/app`, and `check-migration-set`/`check-audit-*` gates are untouched by a test file.

---

## 6. Failure modes (of the change set and its tests)

| # | Failure mode | Cause | Detection / mitigation |
|---|---|---|---|
| FM1 | `TestOpenMalformedDSN` becomes network-dependent | A maintainer swaps the DSN for a parse-valid one (lazy dial / automatic ping) | The file comment mandates the parse-failing DSN; acceptance A1 runs it offline. Any future flake (hang/refused connection) points here |
| FM2 | Secondary pin breaks on pgx bump | pgx renames `ParseConfigError` or changes parse behavior | The `errors.As` pin is type-based (Delta 2); bump review catches the compile-time reference. Primary `"gorm open:"` assertion is unaffected by pgx internals |
| FM3 | gorm bump changes `Open`'s wrap behavior | gorm returns a `%v`-wrapped error, `db.Open` re-wraps it | Primary assertion still holds (`gorm open:` is `db.Open`'s own prefix); secondary `errors.As` against `ParseConfigError` would then fail — a *desired* regression signal for the error contract |
| FM4 | Default gate silently drops the tests again | Someone tags the new file `//go:build integration` or renames it | C6 acceptance fails (`[no test files]` or missing tests); C4 compile check in both modes |
| FM5 | gorm stderr log line misread as failure | Expected logger noise during R3 | Documented in file comment and §4.2; Go tests do not fail on stderr |
| FM6 | `logLevel` default branch drifts (e.g., an explicit `"warn"` case added, or `""` treated as Silent) | Future refactor of `db.go` | `""`, `"WARN"`, `"ERROR"`, `"bogus"` cells pin the default branch; any case added for `warn` does not flip existing cells (deliberate: the cells pin *current* behavior, not aspirational behavior) |
| FM7 | lint-strict mis-reported as the test-size guard | Delta 1: funlen/cyclop exempt for test files | §1 Delta 1 restates the real binding constraints (C7); no reliance on lint-strict for test-function length |
| FM8 | Integration run interference | `TestPing` (testcontainers) and the unit tests run in the same package under `-tags integration` | Unit tests allocate no resources, touch no shared globals, no `t.Parallel` → no interference; `make test-integration -p 1` order is irrelevant |

---

## 7. Migration steps

This is an additive, test-only change: no data, config, schema, or production-behavior migration exists. The rollout sequence is:

1. **Add** `internal/pkg/db/db_internal_test.go` per §4.2 (package `db`, untagged, three tests).
2. **Verify locally offline**: `go test -count=1 -v ./internal/pkg/db/` → 3 tests pass, no Docker, no network; confirm the gorm stderr line in the R3 test is present but non-failing.
3. **Verify the default gate executes them**: `make test` (package no longer reports `[no test files]`).
4. **Verify tag-mode compatibility**: `go test -tags integration -run '^$' ./internal/pkg/db/` compiles both test files without running Docker; `go test -tags integration -p 1 -run TestLogLevel ./internal/pkg/db/` runs unit tests under the tag (no testcontainers started).
5. **Run the full Docker-free gate** (§9 list minus `test-integration`).
6. **Run `make test-integration`** (Docker) to confirm `TestPing` stays green against testcontainers Postgres and coexists with the unit tests.
7. **Rollback**: delete the single file; every gate returns to its current state. Nothing else needs undoing.

---

## 8. Testable acceptance mapping

| Acceptance (spec §6) | Requirement | Test (new file) | Verification command (offline unless noted) |
|---|---|---|---|
| A1: default gate executes the new tests with the specified assertions | R1 logLevel mapping | `TestLogLevel` (8-cell table, exact constant equality) | `go test -count=1 -v ./internal/pkg/db/ -run TestLogLevel` |
| A1: `configurePool` applies each setting only when `> 0`, skips at 0/negative | R2 pool semantics | `TestConfigurePool` (recording fake, 5 cases) | `go test -count=1 -v ./internal/pkg/db/ -run TestConfigurePool` |
| A1: `Open` with `sslmode=bogus` returns non-nil error containing `"gorm open:"`, parse failure precedes any I/O | R3 error wrap | `TestOpenMalformedDSN` (primary prefix + secondary `errors.As`) | `go test -count=1 -v ./internal/pkg/db/ -run TestOpenMalformedDSN` |
| A1 (whole): `make test` green | R1–R3 | all three | `make test` |
| A2: `TestPing` unchanged, sole integration test, green | R4 | (existing) | `make test-integration` (Docker) |
| A3: lint gates green; new funcs modest despite Delta 1 | R4 | all three | `make lint && make lint-strict && make check-file-sizes` |
| A4: architecture/composition/build untouched | R4 | — | `make check-architecture && make check-composition-root && make build` |
| C1/C2: single new file, zero existing-file edits | R4 | — | `git diff --stat HEAD` |

---

## 9. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Test accidentally requires a live DB (valid-DSN `Open` dials lazily / auto-pings) | R3 mandates the parse-failing `sslmode=bogus` DSN, probe-verified to fail at `pgx.ParseConfig` before any I/O; gorm additionally skips the automatic ping when `Initialize` fails. Parse-valid DSNs are explicitly forbidden in the test file comment (FM1) |
| Evidence's `errors.Is`/`errors.As` prohibition | Delta 2: the chain survives (`%w` reaches the raw `*pgconn.ParseConfigError`); R3's primary assertion stays on `strings.Contains(err.Error(), "gorm open:")` (stable against pgx internals) and adds the type-level `errors.As` pin as the strong check |
| lint-strict "rejects long test funcs" (evidence premise false) | Delta 1: test files are exempt from funlen/cyclop/lll; the real constraints are the non-exempt linters (C7) and house norms. Table-driven structure keeps every func ≤ ~40 lines anyway, preserving per-case diagnosability |
| Scope creep toward the other two directions (connectivity verification, pool hygiene/config validation) | This design pins only current behavior: the switch mapping, the `> 0` guards, and the `gorm open:` wrap. No new validators, no tightening, no `PingContext`/`WaitReady` work |
| gorm stderr log line mistaken for failure | Documented as expected noise (§4.2, FM5) |
| Default-gate silent regression (file re-tagged/renamed) | C6 acceptance + C4 compile in both tag modes |

**Done gate for this direction** (Docker-free): `make test && make lint && make lint-strict && make check-file-sizes && make check-architecture && make check-composition-root && make check-integration-compile && make build`, plus `make test-integration` (Docker) to confirm `TestPing` remains green and coexists with the unit tests. `git diff --stat HEAD` must show exactly one new file: `internal/pkg/db/db_internal_test.go`.

---

## 10. Traceability matrix

| Requirement | Test (new file) | Evidence |
|---|---|---|
| R1 `logLevel` mapping | `TestLogLevel` (table, exact equality) | E2 (switch), E8 (constants) |
| R2 `configurePool` `> 0`-only | `TestConfigurePool` (recording fake) | E2 (guards + interface), AGENTS.md "deterministic fakes" |
| R3 `Open` error wrap | `TestOpenMalformedDSN` | E9 (offline parse failure), Delta 2 (`%w` chain + `errors.As`), E7 (pinned pgx) |
| R4 no behavior change | absence of edits to existing files | E1, E2, E3, E5(partial), E6 |
| A1 default gate executes tests | all three | E3 (gate), E11 (package coexistence, probe-verified) |
| A2 integration gate | existing `TestPing` | E1, E4 |
| A3 lint gates | all three | E5 with Delta 1 |
| A4 architecture/composition/build | — | E6 |
