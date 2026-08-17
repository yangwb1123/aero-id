# Design — Fix `run-ghz.sh` data pass-through (YAML quotes leak into `ghz -d`) and make the data contract gate-visible

- **Date:** 2026-08-17
- **Module:** `scripts/load/ghz` (`scripts/load/run-ghz.sh`, `scripts/load/ghz/scenarios.yaml`, `scripts/check-load-scenarios.sh`, `scripts/testdata/load-scenarios/`)
- **Direction:** fix runner data pass-through + gate-visible data contract (JSON validity + quoted/brace-balanced structural rule); defect fix + gate hardening + regression fixture, no product-behavior surface change
- **Status:** verified (all evidence claims re-verified against HEAD `748973e`); no repository changes in this document — implement per the migration steps below

---

## 1. Verification verdict

Every load-bearing claim in the incoming requirements was re-verified against the current tree (HEAD `748973e`; commit date 2026-08-16 15:08 -0800 = 2026-08-17 UTC, matching the direction's dating). **All claims confirmed.** One implementation-relevant refinement found (Section 4, REQ-2): the current PLAN parser prints TSV rows *inside* its validation loop, so a standalone heredoc dry-run only emits zero rows if the poisoned scenario is first; ACC-4's "zero TSV rows" requirement is made deterministic by a two-phase (validate-all-then-print) restructure, which I specify below.

| # | Claimed evidence | Repository truth (verified this session) | Verdict |
|---|---|---|---|
| E1 | Parser stores `data` verbatim via `m.group(2).strip()` (`run-ghz.sh:76`) | ✅ Exact. Line 76: `current[m.group(1)] = m.group(2).strip()`. Reproduced: parser heredoc run against the real YAML emits all 5 data values quote-wrapped — `'{}'`, `'{}'`, `'{"page":{"limit":50}}'`, `'{"page":{"limit":20}}'` ×2 — and `json.loads()` fails on all 5 (`Expecting value: line 1 column 1 (char 0)`) | ✅ Exact |
| E2 | `-d "$data"` at `run-ghz.sh:128` under `set -euo pipefail` (line 1); `load-test-grpc` aborts at scenario 1; ghz not installed locally | ✅ Exact. Line 1 `set -euo pipefail`; line 128 `-d "$data" --metadata ...`; Makefile:181-186 (`command -v ghz` guard, TOKEN guard, `bash scripts/load/run-ghz.sh "$(TOKEN)" "$(BASE_URL_GRPC)"`). `command -v ghz` → not installed in this environment, so E2E verification stays parser-level as claimed | ✅ Exact |
| E3 | `scenarios.yaml` data lines single-quoted | ✅ Exact. Data lines at 29 (`'{}'`), 37 (`'{}'`), 45 (`'{"page":{"limit":50}}'`), 53/61 (`'{"page":{"limit":20}}'`); schema-comment line for `data:` at line 13 (`#   data:         request body JSON`) | ✅ Exact |
| E4 | R2(c) checks `data` only for non-empty presence; no format rule; gate green on the defect | ✅ Exact. `rule_r2` presence loop covers `name call data concurrency duration p95 p99` via `^    $field: .+`; the format-check list covers only `name / concurrency / duration / p95 / p99`. `bash scripts/check-load-scenarios.sh` exits 0 with the single `✓ Load-scenario check passed` line | ✅ Exact |
| E5 | Pre-refactor `2c7a2a9` raw unquoted payloads; `5fc55eb` regressed | ✅ Exact. `git show 2c7a2a9:scripts/load/run-ghz.sh` — inline array `"...|{}"`, `"...|{\"page\":{\"limit\":50}}"` etc.; fifth entry named `audit-list-events`. `git diff 2c7a2a9 df4f3de --stat` shows the parser rewrite (172 insertions). HEAD YAML's fifth scenario is `audit-list-facts` (rename at 5fc55eb), payload unchanged | ✅ Exact |
| E6 | All 6 fixture YAMLs use single-quoted, brace-balanced `data` | ✅ Exact. All 6 fixture trees' `scenarios.yaml` data lines are `'{}'` and `'{"page":{"limit":20}}'` | ✅ Exact |
| E7 | None of the 6 fixture runners contains `json.loads`; all are 7-line `echo` stubs; `runner_divergence` carries an inline `aeropb` literal | ✅ Exact. All 6 stubs are `# Fixture runner (clean) ...` + `set -euo pipefail` + `YAML=...` + `echo "parsing $YAML"`; `runner_divergence` additionally has `echo aeropb.AggregationService.GetMeOverview` (R2(b) trigger). Zero `json.loads` in any stub. **Load-bearing:** every stub must gain R2(e) guard tokens or every fixture would redden R2(e) and break single-rule sweep isolation | ✅ Exact |
| E8 | Gate CI-green today | ✅ Exact (verified: exit 0, `✓ Load-scenario check passed`); part of the `check-gates` CI job (Makefile) and done-gate chain | ✅ Exact |
| — | Fixture proto surface (R1-safe copy source for the new fixture) | `negative/clean/.../aero_id.proto` declares exactly `AccountService` (GetMe, ListSources), `AggregationService` (GetMeOverview), `FileService` (ListFiles), `AuditLedgerService` (ListAuditFacts) — a superset of every fixture's calls | ✅ Confirmed |
| — | Gate isolation contract ("no interpreter, no JSON preprocessor") | ✅ Script header: "bash + POSIX text tools (grep, sed, awk) only — no compiled toolchain, no container runtime, no network, no interpreter, no JSON preprocessor, no new binaries". REQ-3's python-in-the-gate ban is load-bearing | ✅ Confirmed |

Minor non-substantive drift vs. the direction: README R2 bullet is at `scripts/load/README.md:138-139` (cited 138-141); nothing else deviates.

**Defect confirmed:** `run-ghz.sh:76` stores `data` verbatim including YAML single-quote delimiters; `run-ghz.sh:128` passes `'{}'`/`'{"page":{"limit":50}}'` (with quotes) to `ghz -d`, whose `-d` contract parses any non-`@`-prefixed string as raw request JSON → unmarshal failure → nonzero exit → `set -euo pipefail` aborts `make load-test-grpc` at scenario 1 of 5, zero results, while `make check-load-scenarios` stays green (E4/E8). The pre-refactor form (`2c7a2a9`) passed raw JSON and worked; `5fc55eb` regressed it.

---

## 2. Problem statement

The single-source refactor (`5fc55eb`) replaced the inline scenario array with a regex YAML parser, and added the CI gate (`R1`–`R4`). The refactor introduced a data pass-through regression: the parser stores the `data:` field verbatim (including the YAML single-quote delimiters), so `ghz -d` receives a literal `'{}'` string instead of `{}`. ghz's `-d` contract only treats a leading `@` as file-read; any other string is parsed as the raw request body and fails JSON unmarshal. The first campaign therefore fails, and `set -euo pipefail` aborts the entire load run (**zero results**). The gate cannot see the defect: R2(c) checks `data:` only for non-empty presence.

Three-part fix, all inside the existing load-tooling surface:
1. **Runner** — decode the YAML single-quoted scalar and `json.loads()`-validate each payload inside the PLAN parser, fail-fast **before any ghz invocation** (extends the documented malformed-YAML abort contract at `run-ghz.sh:40-44`).
2. **Gate** — R2(d): every `data:` value must be quoted and brace-balanced (structural tripwire, bash + POSIX text tools only); R2(e): the runner must contain the `json.loads(` guard + `not valid JSON` failure token (mirrors R2(a)'s canonical-link tripwire).
3. **Regression fixture** — positive fixture `positive/data_bad_json` under the existing self-test sweep, plus R2(e) guard-token lines in all 6 existing fixture runners (E7), so the sweep stays single-rule-isolated and load-bearing.

The canonical YAML keeps its quoted style (the gate now *enforces* it); the fix is the runner's decode step, not a YAML rewrite.

---

## 3. Design overview

Contract after the change (per layer):

```text
scenarios.yaml (canonical)               data: '{"page":{"limit":50}}'   # single-quoted JSON, R2(d)-enforced
        │
        │ PLAN="$(python3 ...)"  [run-ghz.sh:37-113 — the ONLY permitted json.loads home]
        ▼
parser: decode '…'→payload (strip delimiters, ''→' escape) → json.loads(payload)
        on decode/JSON failure → "ERROR: ... not valid JSON ..." + sys.exit(1)
        (two-phase: fully validate all n scenarios, then emit n TSV rows)
        │
        ▼
TSV row: name<TAB>call<TAB>{"page":{"limit":50}}<TAB>concurrency<TAB>duration<TAB>p95<TAB>p99
        │
        ▼
ghz loop [run-ghz.sh:118-183]           -d "$data"   # unchanged line; data is now raw JSON
```

Gate side (unchanged isolation class): `rule_r2` gains
- R2(d): count-based quoted+brace-balanced checks on `scenarios.yaml` (structure only — the gate cannot run python);
- R2(e): `grep -vE '^[[:space:]]*#'` on the runner must find `json.loads(`; the runner must contain `not valid JSON`.

Fixture side: new `positive/data_bad_json/` mirrors `negative/clean`'s layout with an R2(d)-violating `data:` line; all 7 fixture runner stubs (6 existing + 1 new) carry inert guard-token lines on non-comment lines; the sweep case map gains `data_bad_json) want=rule_r2 ;;`.

---

## 4. API changes

**No product-API changes** — no handlers, protobuf, HTTP/gRPC surfaces, domain types, or configuration change. The only "API" affected is the internal contract between `scenarios.yaml`, `run-ghz.sh`'s PLAN format, and the gate rules:

| Contract point | Before | After |
|---|---|---|
| `scenarios.yaml` `data:` scalar | single-quoted; meaning underspecified | single-quoted JSON payload (now **required** by R2(d)); `data` schema comment (line 13) documents decode+validate |
| PLAN TSV data column | `'{}'` (quote-wrapped, broken) | `{}` (raw JSON, identical to `2c7a2a9` payloads) |
| `-d "$data"` line (128) | — | **unchanged** (fix is at the parser; unquoting would be a new word-splitting defect) |
| `rule_r2` | (a), (b), (c) | +(d) quoted+brace-balanced data rule; +(e) runner data-guard tripwire |
| Fixture sweep class map | 5 positive classes | +`data_bad_json) want=rule_r2 ;;` |

### 4.1 REQ-1 + REQ-2 — runner decode + JSON guard (PLAN parser, `run-ghz.sh:37-113`)

Restructure the PLAN parser heredoc from validate-while-print to **two-phase**:

1. Parse-and-collect (existing regex/YAML logic unchanged), then per scenario, in order:
   - `raw = s["data"]` (as today, `m.group(2).strip()`);
   - **decode**: `m2 = re.fullmatch(r"'(.*)'", raw)` — if no match → `fail(-1, "scenario %s: data %r must be a single-quoted JSON payload" % (s["name"], raw))`. If match → `payload = m2.group(1).replace("''", "'")` (YAML `''`→`'` escape, pro-forma for the canonical dataset which contains no inner quotes);
   - **validate**: `try: json.loads(payload)` — on `(ValueError, TypeError)` → `fail(-1, "scenario %s: data %r is not valid JSON" % (s["name"], raw))` — the literal token `not valid JSON` lives here (R2(e) tripwire);
   - existing per-scenario validation (name, call, concurrency, duration, p95/p99 formats) unchanged and after the data guard (data fails first);
   - collect the validated 7-tuple into `rows`.
2. After all scenarios pass: `print("\t".join(row))` for each row.

Failure path: `fail()` writes `ERROR: <path> …` to stderr and `sys.exit(1)`; under `set -euo pipefail`, `PLAN="$(python3 …)"` aborts the script before the ghz loop (`run-ghz.sh:118`). The two-phase structure guarantees **zero TSV rows on stdout for any poisoned scenario position**, making ACC-4's isolation dry-run deterministic.

Rationale: `json.loads` stays exclusively in the runner (the gate's isolation contract forbids interpreters in the gate); the guard lives in the PLAN heredoc (37-113), not the SLO heredoc (132-179), so the SLO checker's `json.load(f)` (`run-ghz.sh:141`) never satisfies it.

### 4.2 REQ-3 — gate R2(d): quoted + brace-balanced data (bash + POSIX text tools only)

Inside `rule_r2`, after the R2(c) presence/format checks:

```bash
# (d) data contract: every data: line is single-quoted with a non-empty
# payload, and every payload has >= 1 '{' with balanced brace counts.
# Structural tripwire only (no interpreter/JSON tooling in the gate —
# isolation contract in the header); the runner's json.loads guard (REQ-2)
# is the accurate validator (R2(e) keeps that guard present).
dq=$(grep -cE "^    data: '.+'\$" "$yaml" || true)          # quoted, non-empty
if [ "$dq" -ne "$n" ]; then
  echo "ERROR: R2 $yaml data lines matching single-quoted non-empty form: $dq, expected $n (data must be '...' quoted with a non-empty payload)"
  violations=$((violations + 1))
  grep -nE '^    data:' "$yaml" | grep -vE "^[0-9]+:    data: '.+'\$" || true   # offending lines, named
fi
db=$(awk '/^    data: / { o = gsub(/{/, "{"); c = gsub(/}/, "}"); if (o >= 1 && o == c) n++ } END { print n+0 }' "$yaml")
if [ "$db" -ne "$n" ]; then
  echo "ERROR: R2 $yaml brace-balanced data lines (>= 1 '{', equal '{' and '}' counts): $db, expected $n"
  violations=$((violations + 1))
  awk '/^    data: / { o = gsub(/{/, "{"); c = gsub(/}/, "}"); if (o < 1 || o != c) print NR ": " $0 }' "$yaml" || true
fi
```

Notes: `gsub`'s return value is the substitution count (POSIX awk). Brace-counting is content-agnostic (nested braces, braces inside string literals all count), which is deliberately coarse — see §6 FM-4 for the documented tradeoff. The two sub-counts cover every violation flavor: unquoted (`data: {"page":…}`) fails `dq`; quoted-empty (`data: ''`) fails `dq`; quoted non-JSON (`data: 'not json'`) fails `db` (zero braces); quoted imbalanced (`data: '{"page":1'`) fails `db` (`{`≠`}`). Count semantics mirror R2(c) exactly (`$n` = scenario count).

### 4.3 REQ-4 — gate R2(e): runner data-guard tripwire

Inside `rule_r2`, after R2(d):

```bash
# (e) runner data-guard tripwire (mirror of (a)): the runner must contain
# the json.loads( guard on a code line and the 'not valid JSON' failure
# token; json.load(f) in the SLO checker is NOT a substitute.
if ! grep -vE '^[[:space:]]*#' "$runner" | grep -q 'json.loads('; then
  echo "ERROR: R2 $runner missing json.loads( data guard (data pass-through contract: YAML quotes must not reach ghz -d)"
  violations=$((violations + 1))
fi
if ! grep -q 'not valid JSON' "$runner"; then
  echo "ERROR: R2 $runner missing data-guard failure token 'not valid JSON'"
  violations=$((violations + 1))
fi
```

The SLO checker (`run-ghz.sh:141`) uses `json.load(f)` — the `json.loads(` grep cannot match it, and the sole `not valid JSON` occurrence comes from the REQ-2 guard. Two-token requirement defeats incidental satisfaction by a stray `json.loads(` elsewhere in the file.

### 4.4 REQ-5 — fixture sweep

1. **New positive fixture** `scripts/testdata/load-scenarios/positive/data_bad_json/` mirroring `negative/clean`'s layout exactly (proto, `migrations/*.up.sql` ×2, `partition-index-bench.sql`, `k6/{account,event-soak,callback-soak}.js`, `sink/main.go`, `run-ghz.sh`, `scenarios.yaml`). Proto = copy of `negative/clean`'s (already covers `AccountService.GetMe`/`ListSources`, `AggregationService.GetMeOverview`, `FileService.ListFiles`, `AuditLedgerService.ListAuditFacts` — R1-safe for any fixture call set). `scenarios.yaml` keeps 2 scenarios, all other fields/formats valid, with a **single** R2(d) violation on one `data:` line — recommended flavor set to cover both sub-counts: `data: 'not json'` (zero braces, fires `db`) on scenario 2 (scenario 1 stays `'{}'` so `dq` count still passes). All six other fields valid → R1, R2(a/b/c/e), R3, R4 silent.
2. **Existing 6 fixture runners + the new one**: append an inert guard-token line on a non-comment line, e.g.:
   ```bash
   data_guard='json.loads( not valid JSON )'   # R2(e) data-guard tripwire tokens (kept: the sweep needs single-rule isolation)
   ```
   This satisfies R2(e) (both greps) without adding `aeropb.*` literals (R2(b)-safe), without touching the runner behavior (stub is never executed), and `runner_divergence` still trips only R2(b) via its existing `echo aeropb.AggregationService.GetMeOverview`. **Mandatory for all 7 stubs** — otherwise every positive fixture trips R2(e) and the sweep's designated-rule isolation collapses (E7).
3. **Sweep wiring**: in the self-test case map add `data_bad_json) want=rule_r2 ;;` (next to the existing `runner_divergence|malformed_yaml) want=rule_r2 ;;` arm). No other sweep machinery changes; designated-rule-fires and other-rules-silent apply automatically.

### 4.5 REQ-6 — doc sync (text-only)

- `scripts/check-load-scenarios.sh` header: enumerate R2(d) and R2(e) under R2, and document the `data_bad_json` fixture class and the E7 guard-token rule.
- `scripts/load/README.md:138-139` R2 bullet: add "every `data:` is a single-quoted, brace-balanced JSON payload (R2(d)); the runner decodes and `json.loads`-validates it before ghz runs, and the gate trips if that guard is dropped (R2(e))".
- `scenarios.yaml:13` schema comment: note `data` is a single-quoted JSON payload, decoded + validated by the runner (`json.loads`) and structurally checked by the gate (R2(d)).
- No AGENTS.md change (it does not enumerate sub-rules).

---

## 5. Compatibility constraints

- **Canonical YAML quoted style is preserved and now enforced.** Rewriting `scenarios.yaml` to unquoted style is explicitly out of scope and would now be a gate violation.
- **`-d "$data"` line 128 unchanged.** The shell use stays quoted (unquoting would reintroduce word splitting); the fix is entirely in the parser's decode step.
- **PLAN TSV contract unchanged**: 7 columns, same order, same downstream loop (`name call data concurrency duration p95_ms p99_ms`); only the data column's content becomes raw JSON. Downstream consumers (ghz loop, results files) see byte-identical payloads to pre-refactor `2c7a2a9`.
- **R1 / R3 / R4 and R2(a)/(b)/(c) semantics untouched** — verified: real tree and all 6 existing fixture trees stay clean under the new sub-rules (E6), so no existing positive fixture becomes a multi-rule violation and `negative/clean` stays negative.
- **Gate isolation contract intact**: R2(d) is grep/awk/tr-level (POSIX awk `gsub` counting); no python, no JSON preprocessor, no new binaries in the gate; `json.loads` remains exclusively in the runner.
- **Runner toolchain unchanged**: `python3` is already a runner dependency (the PLAN parser); the decode/guard adds stdlib `json`/`re` only. Python 3.10.7 present locally; CI-safe (gate is shell-only and does not execute the runner).
- **SLO checker (`run-ghz.sh:132-179`) behaviorally untouched** and does not satisfy R2(e) (`json.load(f)` ≠ `json.loads(`).
- **File-size caps**: runner goes ~188 → ~205 lines; gate ~288 → ~310 lines — both far under the 500-line gate (Go-only anyway per `make check-file-sizes`).

---

## 6. Failure modes

| # | Failure mode | Behavior | Mitigation |
|---|---|---|---|
| FM-1 | Unquoted `data:` reaches runner (`data: {"page":…}`) | Parser `fail()` → `ERROR: … must be a single-quoted JSON payload`, `sys.exit(1)` → `set -euo pipefail` aborts before ghz; **zero campaigns** | REQ-2 decode guard; additionally R2(d)-`dq` reddens in CI |
| FM-2 | Quoted but invalid JSON (`data: 'not json'`) or imbalanced (`data: '{"page":1'`) | Parser `fail()` → `ERROR: … is not valid JSON`, exit 1, zero campaigns | REQ-2 json.loads guard; R2(d)-`db` reddens structurally |
| FM-3 | `data: ''` (quoted empty) | R2(c) presence passes; R2(d)-`dq` (`'.+'` requires ≥1 char) reddens; runner `json.loads('')` also fails | Layered gate+runner |
| FM-4 | Valid JSON whose *payload string content* has unbalanced brace counts (e.g. a regex containing `{`/`}` inside a JSON string) | R2(d)-`db` false-positives (structural proxy) | Documented tradeoff: brace-count is a coarse structural tripwire; the runner `json.loads` is the accurate validator and would accept it, but the gate would force the payload to be expressed differently (or R2(d) to be strengthened, out of scope). Canonical payloads are simple page-limit objects — no brace-in-string today; the schema comment forbids adding one without a gate extension |
| FM-5 | Future refactor drops the runner guard | R2(e) reddens (`json.loads(` and/or `not valid JSON` absent) — gate never goes vacuous about the runner side | REQ-4 two-token tripwire |
| FM-6 | R2(e) incidentally satisfied by stray code | Token pair (`json.loads(` on a code line AND `not valid JSON`) — today only the guard produces both; low pass-noise | Both tokens required |
| FM-7 | Fixture sweep contamination: the new `data_bad_json` trips another rule, or updated stub breaks an existing fixture's isolation | Sweep fails with `SELF-TEST FAIL: … also trips rule_…` or designated-rule mismatch | REQ-5 mandates valid proto/migrations/k6/sink copies and the E7 guard-token update for **all 7** stubs before the sweep passes |
| FM-8 | Parser partial output on late failure (old single-loop structure) | Under `set -euo pipefail`, `PLAN="$(…)"` failure discards the partial capture and aborts before the ghz loop, so no campaign runs; but a standalone heredoc dry-run would show partial rows | REQ-2 two-phase restructure makes zero-row emission deterministic for ACC-4 |
| FM-9 | ghz E2E still failing for an unrelated reason | Not covered by this change (runner passes correct JSON; ghz must still reach the server) | E2E designated manual-only where ghz + live server + token exist; Makefile already guards `command -v ghz` |
| FM-10 | YAML `''`→`'` escape edge cases | Decoder handles per YAML spec pro-forma; canonical payloads contain no inner quotes; any unhandled form reddens via REQ-2 json.loads failure | Covered |

---

## 7. Migration steps

No data/DB/**product** migration exists — this is purely CI/load-tooling. "Migration" here is the implementation sequence with evidence capture (each step is independently revertible; the tree is gate-green today, so the change set is revert-safe as a whole).

1. **Baseline capture (pre-change, already done this session):** `bash scripts/check-load-scenarios.sh` → exit 0; parser dry-run → 5/5 data values quote-wrapped, `json.loads` fails 5/5 (E1/E4/E8 evidence).
2. **Runner (REQ-1 + REQ-2):** edit `run-ghz.sh` PLAN heredoc → add decode + `json.loads` guard + two-phase emit. Verify: `bash -n scripts/load/run-ghz.sh`; parser dry-run on real YAML → 5 rows, data column `{}`, `{}`, `{"page":{"limit":50}}`, `{"page":{"limit":20}}`, `{"page":{"limit":20}}`; `json.loads` clean on each (ACC-3/ACC-5); poisoned-YAML dry-runs ×3 (`data: '{"page":1'`, `data: {"page":1}`, `data: 'not json'`) → exit 1, `ERROR:` naming the scenario, `not valid JSON` token for the JSON-parse failures, zero TSV rows (ACC-4).
3. **Gate (REQ-3 + REQ-4):** extend `rule_r2` with R2(d) + R2(e); update header enumeration (REQ-6 part 1). Verify real tree still green and each sub-rule catches its flavor on throwaway YAML copies.
4. **Fixtures (REQ-5):** copy `negative/clean` → `positive/data_bad_json`; introduce the R2(d) violation; add guard-token lines to all 7 fixture runners; add `data_bad_json) want=rule_r2 ;;` to the sweep case map.
5. **Anti-vacuity proof (ACC-2):** (a) run the sweep with the fixture+wiring in place but R2(d)/R2(e) stubbed out → expect `SELF-TEST FAIL: positive fixture data_bad_json matched nothing` (capture output); (b) after the rules land → gate passes, only `rule_r2` fires for the fixture.
6. **Docs (REQ-6):** README R2 bullet, YAML schema comment, gate header (header edit done in step 3).
7. **Done gate (ACC-6):** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-load-scenarios && make check-composition-root && make check-proto-sync && make check-integration-compile && make build`.
8. **Rollback:** revert the single change-set commit (runner + gate + fixtures + docs). The gate is green pre-change, so a revert restores a known-good (defective-but-green) state; the regression reproduction is documented in §1 for re-checking.

---

## 8. Acceptance mapping (testable)

| Req | Acceptance criterion (from the direction, made executable) | Evidence |
|---|---|---|
| REQ-1 | ACC-3: PLAN dry-run on real YAML → exactly 5 TSV rows, data column `{}`, `{}`, `{"page":{"limit":50}}`, `{"page":{"limit":20}}`, `{"page":{"limit":20}}` (no quote delimiters), `json.loads()` clean on every value. ACC-5: values equal `2c7a2a9` payloads exactly (modulo the 5fc55eb rename). ACC-4: `bash -n scripts/load/run-ghz.sh` exits 0 | §7 step 2 captured output |
| REQ-2 | ACC-4: poisoned-YAML dry-runs (`data: '{"page":1'`, `data: {"page":1}`, `data: 'not json'`) each → exit 1, `ERROR:` naming scenario, `not valid JSON` for JSON-parse failures, zero TSV rows (i.e. before any ghz invocation) | §7 step 2 |
| REQ-3 | ACC-1: real tree green under R2(d). ACC-2: `data_bad_json` fires `rule_r2` (R2(d)) and only it; `negative/clean` clean | §7 steps 3-5 |
| REQ-4 | ACC-1: real tree green under R2(e). ACC-4: `grep -vE '^[[:space:]]*#' scripts/load/run-ghz.sh \| grep -q 'json.loads('` succeeds and `grep -q 'not valid JSON' scripts/load/run-ghz.sh` succeeds. ACC-2: fixture sweep stays single-rule | §7 steps 3-5 |
| REQ-5 | ACC-2: fixture exists with mandated layout; sweep passes with it in place; `negative/clean` clean; anti-vacuity procedure (step before/after) reproduced | §7 steps 4-5 |
| REQ-6 | ACC-1 unchanged (gate passes); text-only diffs (header/README/YAML comment) are the evidence; no acceptance gate of its own | §7 step 6 |
| (gate chain) | ACC-6: full done-gate exits 0 | §7 step 7 |

Anti-vacuity framing kept from the direction: the E8 sweep runs on **every** gate invocation, so step 5's two-phase proof is a one-time implementation capture; thereafter the CI sweep continuously exercises `data_bad_json` (designated-rule-fires / others-silent).

---

## 9. Verification gates

```bash
bash -n scripts/load/run-ghz.sh                                   # ACC-4 static
bash scripts/check-load-scenarios.sh                              # ACC-1, ACC-2 (fixtures run every invocation)
# PLAN dry-run on real YAML → 5 rows, unquoted data, json.loads-clean  (ACC-3, ACC-5)
# poisoned dry-runs ×3 → exit 1 + ERROR + zero rows                  (ACC-4)
grep -vE '^[[:space:]]*#' scripts/load/run-ghz.sh | grep -q 'json.loads(' && \
  grep -q 'not valid JSON' scripts/load/run-ghz.sh                  # ACC-4 guard presence
# anti-vacuity: fixture+wiring without R2(d)/R2(e) → SELF-TEST FAIL: … matched nothing; then full change → passes  (ACC-2)
make test && make lint && make lint-strict && make check-file-sizes && \
make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && \
make check-load-scenarios && make check-composition-root && make check-proto-sync && \
make check-integration-compile && make build                        # ACC-6
# optional manual E2E where infra exists: make load-test-grpc (ghz + live server + TOKEN)
```

CI note: this is a `check-gates`-job-and-done-gate change only; no Docker, network, or new binaries anywhere in the diff (the fixture proto is a copy; the runner edit is python-stdlib-only).

---

## 10. Out of scope (explicitly preserved from the direction)

- **No change to `scenarios.yaml` scenario content** — quoted style preserved and now enforced.
- **No change to the `ghz` invocation line** (`-d "$data"`, line 128) — parser-level fix only.
- **No change to R1/R3/R4**, and no change to R2(a)/(b)/(c) semantics.
- **No python/JSON tooling in the gate** — isolation contract load-bearing; `json.loads` stays exclusively in the runner; R2(d) uses grep/awk counting.
- **Idempotency-key policy: no new enforcement.** All 5 scenarios invoke read RPCs (`GetMe`, `GetMeOverview`, `ListSources`, `ListFiles`, `ListAuditFacts`); AGENTS.md requires explicit idempotency/operation keys only for command RPCs (none today). Deferred by the direction's binding acceptance criteria.
- **No behavioral change to the SLO checker** (`run-ghz.sh:132-179`); `json.load(f)` must not satisfy and must not be affected by R2(e).
- **No E2E ghz run as a gate** (ghz not installed here; requires live server + token) — parser-level fail-fast proof is the CI-safe evidence.
- **No product-behavior surface change**: no handlers, services, repositories, migrations, or README API surface touched; `make check-composition-root` and `make check-proto-sync` are unaffected (no `.proto` edits).