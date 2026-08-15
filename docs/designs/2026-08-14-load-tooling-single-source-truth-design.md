# Design — Load-Tooling Single Source of Truth + Drift Gates (P6-08)

Status: **Proposed** (implementation-ready)
Date: 2026-08-14
Module: `scripts/load/`
Campaign: `docs/campaigns/pipeline-load-tooling-gates.yaml`
Spec: P6-08 "Load-Test Tooling Hardening" (REQ-1..8)
Baseline verified at: HEAD `0f65b3c` (one docs-only commit past the spec's `583a2ad`; all content claims below re-verified against the live tree).

---

## 1. Verification verdict (untrusted evidence → tree)

| # | Claim (evidence) | Verdict | Tree evidence |
|---|---|---|---|
| E1 | `scenarios.yaml:39` carries dead `aeropb.AuditLedgerService.ListEvents`; proto has no such RPC; `AuditLedgerService` (proto line 548) exposes only `ListAuditFacts` (549) | **Confirmed** | `grep -rn ListEvents` → only `scripts/load/ghz/scenarios.yaml:39` + README prose. Proto: `service AuditLedgerService {` @548, `rpc ListAuditFacts(ListAuditFactsRequest) returns (ListAuditFactsResponse);` @549. Other calls exist: `ListFiles` @68, `GetMe` @446, `ListSources` @449, `GetMeOverview` @496. `go_package` = `.../aeropb`; services formatted `service Name {`. |
| E2 | `run-ghz.sh` inline `SCENARIOS` array (audit entry already correct: `ListAuditFacts`); hardcoded `--concurrency 50 --duration 2m`; CLI `run-ghz.sh <bearer-token> [grpc-addr]` | **Confirmed** | Inline array with 5 entries; audit entry `audit-list-events|aeropb.AuditLedgerService|ListAuditFacts|{"page":{"limit":20}}`; `--concurrency 50 --duration 2m` hardcoded; `PROTO`, `OUT=scripts/load/results`, `mkdir -p`, `==> ghz:` progress line, `done; results in $OUT` trailer, `${1:?usage: run-ghz.sh <bearer-token> [grpc-addr]}` all present. YAML documents 100/50/50/20/10 @3m — **two divergent sources + parameter drift confirmed**. |
| E3 | ci.yml `check-gates` = checkout + `check-audit-swallow` + `check-architecture` only; no setup-go/network/Docker | **Confirmed** | `check-gates` job: `actions/checkout@v4` + two `make` steps. AGENTS.md done-gate (line 53) has no load gate. |
| E4 | Bench SQL references 7 user tables; `pg_stat_user_indexes` is the only system relation; `generate_series` sits inside a `--` comment | **Confirmed**, plus **2 extra traps found**: relations `audit_ledger` (36/44/52), `notifications` (60/67), `outbox_events` (73), `audit_publications` (82), `messages` (92), `files` (103), `account_snapshots` (111), `pg_stat_user_indexes` (117). `generate_series` only in the "Seed first" comment (18). **Traps:** `\echo '=== 6. Outbox drain (FOR UPDATE SKIP LOCKED worker) ==='` @70 contains `UPDATE`; standalone `FOR UPDATE SKIP LOCKED;` lines @77/@87 contain `UPDATE` followed by `SKIP` — naive `UPDATE\s+<ident>` extraction yields false relations. R3 extraction must neutralize both (see §5.3.3). |
| E5 | `Makefile:197` `PG_DSN_BENCH ?= $(PG_DSN)`; `load-test-db` @199–201 runs psql with `ON_ERROR_STOP=1` | **Confirmed** | Line numbers exact. Bench header also documents the `psql "$PG_DSN" -f ...` usage (line ~14) and the manual `generate_series(1, 1000000)` seed step. |
| E6 | k6 scripts + sink wired via Makefile | **Confirmed** | `load-test-http`→`account.js`, `load-test-event`→`event-soak.js`, `load-test-callback`→`callback-soak.js`, `go run ./scripts/load/sink`. |
| E7 | python3 available (3.10.7); jq must not be required | **Confirmed** | `python3 --version` → 3.10.7. `jq` exists locally (`/usr/bin/jq`) but is deliberately not used; AC-3 bans the tokens. |
| E8 | `check-architecture.sh` = bash+grep house style, header is canonical rules, `scripts/testdata/architecture` positive/negative fixture sweep on every run; missing fixture tree fails the gate | **Confirmed** | Header states "script header is the canonical rule definition"; `selftest_rule` semantics: **positive fixture must fire (≥1 violation), negative must be clean, missing tree → SELF-TEST FAIL**. Note: the script uses `awk`/`sed` for extraction (import blocks) — the isolation class is POSIX tools, not literally grep-only; the new gate mirrors this. |
| E9 | SLO calibration: plan §5 regional snapshot read p95 ≤ 100ms / p99 ≤ 250ms; README k6 thresholds p95 < 250ms / p99 < 1000ms | **Confirmed** | `docs/plans/2026-08-04-distributed-account-aggregation.md:170`; `scripts/load/README.md:28`. |
| REQ-8 | No pre-existing `check-load-scenarios`, `seed-bench.sql`, or `aero_load_bench_seed` anywhere | **Confirmed** | grep across `Makefile scripts/ .github/ AGENTS.md` → zero hits; `aero_load_bench_seed` absent from `migrations/` (119 migration files; `CREATE TABLE IF NOT EXISTS <name>` style, compatible with R3's `CREATE TABLE.*<name>` pattern). |

Minor evidence deltas (content unchanged): README's `psql "$PG_DSN"` alternative is line 76 (not 57); the bench SQL header additionally carries its own `psql "$PG_DSN" -f ...` usage line (also updated, §5.7.3).

---

## 2. Problem statement

Three independent drift hazards, all currently undetected by any deterministic check:

1. **Dead RPC in the canonical scenario file.** `scenarios.yaml` references `ListEvents`, which does not exist in the proto; the runner silently works around it with its own inline array, so the YAML drifts from both the proto *and* the runner. Nobody notices until a scenario 404s at runtime.
2. **Two divergent scenario sources.** The YAML (100/50/50/20/10 @3m) and the runner (50 @2m for everything) disagree about load parameters; whichever file you read, you get different behavior. No gate compares them.
3. **Silent bench-DB fallback.** `PG_DSN_BENCH ?= $(PG_DSN)` means `make load-test-db` with no arguments runs EXPLAIN ANALYZE over the **app database**; the README even documents the hazardous `psql "$PG_DSN"` alternative. Additionally, an empty/unseeded bench DB produces meaningless plans, and nothing verifies the bench SQL's table references against migrations.

Design goal: one canonical source (`scenarios.yaml`), a pure-executor runner with fail-closed SLO enforcement, a deterministic bash+grep drift gate with self-test fixtures (CI-safe: no Go/Docker/network/python), and an explicit, guarded bench-DB workflow — with zero changes to live Go code, migrations, k6, sink, or existing gates.

---

## 3. Design overview

```text
                 ┌──────────────────────────────┐
                 │ scripts/load/ghz/scenarios.yaml   (single canonical source)
                 │  name / call / data / concurrency / duration / p95 / p99
                 └──────────────┬───────────────┘
                                │ parsed by
                                ▼
        run-ghz.sh <bearer-token> [grpc-addr]   (bash; python3 allowed here)
          ├─ python3 heredoc: validate + emit TSV (fail fast on malformed YAML)
          ├─ ghz per scenario (per-scenario --concurrency/--duration from YAML)
          └─ python3 heredoc: SLO check on ghz JSON (fail-closed; GZ_SLO_*_MS overrides)
                                │
        ┌───────────────────────┴────────────────────────┐
        ▼                                                ▼
 scripts/check-load-scenarios.sh (bash+grep, CI-safe)   make load-test-db / load-test-db-seed
  R1 calls resolve in proto                             PG_DSN_BENCH explicit + app-DSN refusal
  R2 runner == YAML (no inline literals)                + seed-sanity guard (aero_load_bench_seed)
  R3 bench tables exist in migrations                   + idempotent seed target
  R4 k6/sink assets live                                (scripts/load/db/seed-bench.sql)
  + self-test fixture sweep (positive must fire, negative must be clean)
        │
        ▼
 ci.yml check-gates (one appended step)  +  AGENTS.md done-gate chain
```

---

## 4. Detailed design

### 4.1 REQ-1 — Canonical scenario schema (`scenarios.yaml`)

**Schema (flat list; every entry has exactly these 7 fields; 2-space dash, 4-space fields):**

```yaml
scenarios:
  - name: account-getme              # slug; results file ghz-<name>.json
    call: aeropb.AccountService.GetMe
    data: '{}'
    concurrency: 100                 # ghz --concurrency (integer)
    duration: 3m                     # ghz --duration (Go duration string)
    p95: 100ms                       # SLO: p95 latency target, [0-9]+(ms|s)
    p99: 250ms                       # SLO: p99 latency target, [0-9]+(ms|s)
```

**Decisions:**

- **D1 (rename):** `audit-list-events` → `audit-list-facts`, `call:` → `aeropb.AuditLedgerService.ListAuditFacts`, `data: '{"page":{"limit":20}}'` unchanged (page-shaped request, already correct in the runner). Result filename becomes `ghz-audit-list-facts.json`. The rename is accepted (accuracy; spec allows it) and documented in the README so stale references are caught.
- **D2 (SLO defaults, per E9):** control-plane reads `account-getme`, `aggregation-overview`, `account-list-sources`, `audit-list-facts` → `p95: 100ms, p99: 250ms` (plan §5 regional snapshot read); legacy `legacy-file-list` → `p95: 250ms, p99: 1000ms` (README global k6 thresholds).
- **D3:** concurrency/duration values stay 100/50/50/20/10 @3m — the YAML becomes authoritative; this is a canonical-source refactor, not a load-semantics change. The `--rps 500` fragment in the YAML *header comment* remains a comment (the runner never passed `--rps` and still does not).
- Comment lines (`#`) and blank lines are ignored by every consumer (runner parser and gate).

### 4.2 REQ-2 — `run-ghz.sh` consumes the YAML, enforces SLOs

**Public CLI preserved exactly:** `run-ghz.sh <bearer-token> [grpc-addr]`, default `localhost:9090`, `set -euo pipefail`, `==> ghz: <Service>/<Rpc>` progress line, `done; results in $OUT` trailer, `OUT=scripts/load/results` + `mkdir -p`. The header comment becomes the canonical "single source = scenarios.yaml, runner is a pure executor" note.

**D4 — YAML parser (inline python3 heredoc, dependency-free):** a hand-rolled parser over the fixed flat schema (no PyYAML assumption — PyYAML is not guaranteed in CI/local environments). Rules:

- Skip blank lines and `#` comments. Expect exactly one top-level `scenarios:` line, then entries starting `  - name:`.
- Each entry must contain exactly one of each field `name, call, data, concurrency, duration, p95, p99`, non-empty; **unknown fields are an error** (typo protection).
- Validation: `concurrency` = `[0-9]+`; `duration` = `[0-9]+(ms|s|m|h)`; `p95`/`p99` = `[0-9]+(ms|s)`; `call` = `aeropb.<Service>.<Method>` (exactly 3 dot parts, prefix `aeropb`); `name` = `[A-Za-z0-9-]+`.
- Emits one TSV record per scenario: `name<TAB>call<TAB>data<TAB>concurrency<TAB>duration<TAB>p95_ms<TAB>p99_ms` (p95/p99 normalized to **integer ms**: `100ms`→100, `1s`→1000).
- Any error → `ERROR: scenarios.yaml line N: <reason>` on stderr, **exit 1** (with `set -euo pipefail`, the runner aborts before the first ghz invocation — fail fast, no half-runs).

**Per-scenario ghz invocation** (replaces the fixed `--concurrency 50 --duration 2m`):

```bash
ghz --insecure --proto api/proto/aero_id/v1/aero_id.proto \
    --call "<call>" -d "<data>" --metadata "authorization=Bearer $TOKEN" \
    --concurrency <c> --duration <d> \
    --out "scripts/load/results/ghz-<name>.json" --format json "$ADDR"
```

The `==> ghz:` progress line derives `<Service>/<Rpc>` from the call string (`service="${call#*.}"; service="${service%.*}"; rpc="${call##*.}"`), preserving today's output shape.

**D5 — SLO enforcement (fail-closed, python3 on ghz JSON):** after each run, invoke an inline python3 heredoc with `(json_path, name, p95_ms, p99_ms)`. Expected ghz JSON shape: a `latencyDistribution` array of `{"percentile": <number>, "latency": <ns>}` entries (percentile is numeric — ghz emits 50/75/90/95/99/99.9/99.99/100; latency is **nanoseconds**). Parser behavior:

- Locate entries with `abs(percentile - 95) < 0.1` and `abs(percentile - 99) < 0.1`; `actual_ms = latency // 1_000_000` (floor).
- **Missing/unparsable JSON or missing percentile entries → FAIL** (never pass on missing data; an empty `latencyDistribution` almost always means zero completed requests — auth/config problem — and must not read as green).
- Breach → `SLO FAIL <name> p95=<actual>ms > target=<target>ms` (same for p99); pass → `SLO OK <name> p95=...ms p99=...ms`.
- The checker exits 1 on any failure; the bash loop wraps it in `if ! python3 ...; then slo_fail=1; fi` — **all scenarios still run and report**, and the script exits non-zero at the end (preserves `set -euo pipefail` semantics without aborting mid-campaign). One summary line at the end: `SLO summary: N scenarios, M failed`.
- **Open item (pinned before merge):** confirm the `latencyDistribution` field name and ns units against one real `ghz --format json` run (no result artifacts exist in-repo to inspect). The parser is written strictly against this shape and fails closed on any surprise; the verified shape is recorded in the script header.

**D6 — Env overrides:** `GZ_SLO_P95_MS` / `GZ_SLO_P99_MS` (integer ms) override the YAML targets for **all** scenarios: `P95_MS="${GZ_SLO_P95_MS:-$yaml_p95_ms}"`. Applied in bash before the checker so the override is visible in SLO output. Documented in README (`GZ_SLO_P95_MS=150 make load-test-grpc TOKEN=...`).

**Tooling constraints honored:** `python3` appears only in `run-ghz.sh` (runner), never in the gate; no `jq`/`yq` anywhere; the runner stays ≤ 500 lines (expected ~150). No new binaries.

### 4.3 REQ-3 — Gate `make check-load-scenarios` (pure bash + grep, self-testing)

New file `scripts/check-load-scenarios.sh` (+ `scripts/testdata/load-scenarios/` fixtures). Isolation class identical to `check-architecture.sh`: **bash + POSIX text tools only — no Go, no Docker, no network, no python, no jq, no new binaries**. (House precedent: `check-architecture.sh` uses `awk`/`sed` for extraction; the new gate uses the same tool set — `grep` for all rule scanning, `awk` for the one SQL-token normalization where pure grep is unsound, see D8.) The script header is the canonical rule definition, including the allowlist justification (house style per E8).

Rule functions take a root directory and scan relative paths, so the same code runs against the real tree and fixture trees.

**R1 — every `call:` resolves in the proto.** For each `call: <prefix>.<Service>.<Method>` in `<root>/scripts/load/ghz/scenarios.yaml`:
(a) prefix must be `aeropb` (grep-validated by pattern `^    call: aeropb\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`);
(b) `<root>/api/proto/aero_id/v1/aero_id.proto` must contain `service <Service> {` (grep -n);
(c) an `rpc <Method>(` line must exist with line number strictly between that service line and the next `service ` line (or EOF). Implemented with `grep -n` line numbers + bash arithmetic (`mapfile` of service line numbers; binary-search/linear next-boundary). Missing service, missing rpc, or rpc outside the block → `ERROR: R1 <call> does not resolve in <proto>`.
This is the check that turns the dead `ListEvents` red today and stays red on any future RPC death/rename.

**R2 — runner scenario set equals YAML set.**
(a) `run-ghz.sh` must reference the canonical path: `grep -q 'scripts/load/ghz/scenarios.yaml' "$root/scripts/load/run-ghz.sh"` (anti-regression: if the canonical link silently breaks, the gate reddens);
(b) every `aeropb\.[A-Za-z0-9_]+` call literal found in `run-ghz.sh` (grep -oE, comment lines excluded) must appear verbatim as a `call:` value in `scenarios.yaml`. After the refactor the runner has **zero** inline literals, so this is vacuous-but-load-bearing — the divergence tripwire (the spec's R2-vacuity note is recorded in the script header; the `runner_divergence` fixture keeps it meaningful);
(c) YAML completeness: `grep -cE '^  - name:'` ≥ 1, and for each of the 7 fields `grep -cE "^    <field>:"` must equal the scenario count (a missing field in any entry shifts the count → red), plus format checks: concurrency `[0-9]+`, duration `[0-9]+(ms|s|m|h)`, p95/p99 `[0-9]+(ms|s)`.

**R3 — bench tables exist in migrations.** Relation extraction from `<root>/scripts/load/db/partition-index-bench.sql`, keyword set `FROM|JOIN|INTO|UPDATE|TABLE`:

- **D8 (normalization — the trap the evidence missed):** two `grep -v` pre-filters + one awk normalizer:
  1. drop comment lines: `grep -vE '^\s*--'` (kills the `generate_series` hit @18);
  2. drop psql meta-command lines: `grep -vE '^\s*\\'` (kills `\set`/`\echo`; **`\echo '... FOR UPDATE SKIP LOCKED ...'` @70 contains a bare UPDATE**);
  3. neutralize the SKIP LOCKED idiom: `sed 's/FOR[[:space:]][[:space:]]*UPDATE//g'` (kills `UPDATE SKIP` captures from `FOR UPDATE SKIP LOCKED;` @77/@87 — pure-grep extraction is unsound here; `awk`/`sed` are the same POSIX tools `check-architecture.sh` already uses);
  4. `grep -oE '\b(FROM|JOIN|INTO|UPDATE|TABLE)\s+[A-Za-z_][A-Za-z0-9_]*'`, then take the identifier (last token), dropping an optional `schema.` prefix (future-proofing for qualified names).
- **System allowlist** (part of the gate; each entry justified in the script header): `pg_stat_user_indexes` — the only system relation, the index-health summary view @117. Future system relations added to the bench require an allowlist update; the header documents that procedure.
- For each remaining identifier: `grep -l "CREATE TABLE.*<name>" "$root"/migrations/*.up.sql` must match ≥ 1 file (migration style `CREATE TABLE IF NOT EXISTS <name>` matches). A renamed table or a new hot table added to the bench without a migration → red.

**R4 — liveness of Makefile-referenced load assets.** The four paths must exist and be non-empty (`test -s`): `scripts/load/k6/account.js`, `scripts/load/k6/event-soak.js`, `scripts/load/k6/callback-soak.js`, `scripts/load/sink/main.go`. Hardcoded (deterministic); a future Makefile reference change must update the gate — noted in README and script header.

**Self-testing (mandatory, E8 semantics).** Fixture trees under `scripts/testdata/load-scenarios/` mirror the real-tree relative layout (`api/proto/aero_id/v1/aero_id.proto`, `scripts/load/{ghz/scenarios.yaml, run-ghz.sh, db/partition-index-bench.sql, k6/*.js, sink/main.go}`, `migrations/*.up.sql`) with a tiny synthetic proto. The gate runs its fixture sweep on **every invocation**; a missing fixture tree **fails** the gate. Per rule: the designated positive fixture **must fire ≥1 violation** (anti-vacuity), the negative fixture must be clean (anti-over-breadth); mismatch → `SELF-TEST FAIL`, exit 1. Fixture map:

| Rule | Positive fixture (must fire) | Negative |
|---|---|---|
| R1 | `positive/dead_rpc/` — yaml call not in the synthetic proto | `negative/clean/` (all rules silent) |
| R2 | `positive/runner_divergence/` — inline `aeropb.AccountService.GetMe` literal in run-ghz.sh; `positive/malformed_yaml/` — scenario missing `p99` (R2c) | same |
| R3 | `positive/bench_table_missing/` — bench SQL references a table absent from fixture migrations | same |
| R4 | `positive/missing_k6/` — `k6/account.js` absent | same |

**Exit contract:** 0 on clean tree + all fixtures green; 1 on any rule violation or self-test failure. Violations print one `ERROR:`-prefixed line per class; a final summary line; `✓` lines on success (check-architecture house style). Makefile: `check-load-scenarios: @bash scripts/check-load-scenarios.sh` + `.PHONY` entry.

### 4.4 REQ-4 — `load-test-db` safety + seed target

**D9 — Makefile changes (the only Makefile edits):**

1. **Delete** `PG_DSN_BENCH ?= $(PG_DSN)` (line 197). No default, no fallback.
2. `load-test-db` guard chain (each a one-line `@test ... || { echo "ERROR: ..."; exit 1; }` recipe; all psql probes `ON_ERROR_STOP=1`, read-only):
   - psql present (existing check, kept);
   - `PG_DSN_BENCH` set, else: `ERROR: PG_DSN_BENCH must be set explicitly (bench DB; never the app DB). See make load-test-db-seed / scripts/load/README.md`;
   - **App-DSN refusal:** if `"$(PG_DSN_BENCH)"` string-equals `"$(PG_DSN)"` and `BENCH_ALLOW_APP_DSN` ≠ `1` → `ERROR: PG_DSN_BENCH equals the app DSN (PG_DSN) — refusing. Set BENCH_ALLOW_APP_DSN=1 only for a deliberately isolated app-schema bench cell.` (documented as dangerous; deliberate operator action only);
   - **Seed-sanity guard:** unless `BENCH_SKIP_SEED_CHECK=1`, probe `psql "$(PG_DSN_BENCH)" -tA -c "SELECT schema_version FROM aero_load_bench_seed LIMIT 1"`; missing marker → `ERROR: <dsn> is not a seeded bench database — run: make load-test-db-seed PG_DSN_BENCH=...`; marker version < 1 → **warning** (stale seed; suggest re-seeding), not failure;
   - then run the bench: `psql "$(PG_DSN_BENCH)" -v ON_ERROR_STOP=1 -f scripts/load/db/partition-index-bench.sql`.
3. **New target `load-test-db-seed`:** same psql-present + explicit-DSN + app-DSN-refusal guards (no seed check), then `psql "$(PG_DSN_BENCH)" -v ON_ERROR_STOP=1 -f scripts/load/db/seed-bench.sql`.
4. `.PHONY` (line 6) gains `load-test-db-seed check-load-scenarios`.

**D10 — `scripts/load/db/seed-bench.sql` (new file; not a migration):**
- Creates the marker: `CREATE TABLE IF NOT EXISTS aero_load_bench_seed (schema_version int PRIMARY KEY, seeded_at timestamptz NOT NULL DEFAULT now(), rows jsonb NOT NULL);` — name collision-checked against `migrations/*.up.sql` (zero hits; `aero_` prefix keeps it out of the app namespace).
- **Idempotent re-runnable seeding (guarded truncate):** one `TRUNCATE audit_ledger, notifications, outbox_events, audit_publications, messages, files, account_snapshots RESTART IDENTITY;` (single statement — Postgres allows truncating FK-related tables together without CASCADE), then deterministic `INSERT ... SELECT ... FROM generate_series(1, :N)` with stable derived IDs (`md5('aero-load-' || i)::uuid`), so re-seeding reproduces identical data. `TRUNCATE` chosen over `ON CONFLICT DO NOTHING` so counts stay exact on re-runs.
- **Volumes** (per-cell adjustable via psql variables, overridable on the command line like the bench file already does: `psql -v AUDIT_LEDGER_ROWS=2000000`): `audit_ledger` 1,000,000 (the count the existing bench header documents), `notifications` 200,000, `outbox_events` 100,000, `audit_publications` 100,000, `messages` 200,000, `files` 100,000, `account_snapshots` 50,000. Header documents the numbers and the override syntax.
- **Column lists mirror the migrations at implementation time** (audit_ledger @000034, audit_publications @000045, account_snapshots @000032, outbox_events @000008, notifications @000009, messages @000002, files @000001) — locked by a header note ("update when migrations change"); verified by actually running the seed against a bench DB (implementation step §6.10, not part of the gate).
- Ends by refreshing the marker row (version 1 + per-table row counts) so the guard's stale-seed warning has data.
- The bench header's manual "Seed first" comment and its `psql "$PG_DSN" -f ...` usage line are updated to point at `make load-test-db-seed PG_DSN_BENCH=...` (§5.7.3).

**Defense-in-depth rationale (documented in README):** the app-DSN refusal is string-equality — imperfect against alternate DSN spellings; the **seed marker is the substantive protection** (the app DB will never contain `aero_load_bench_seed`, so an accidental app-DSN run is refused by the seed guard even if the string check is bypassed). Both layers are documented so operators understand the marker is load-bearing.

### 4.5 REQ-5 — CI wiring (additive)

`.github/workflows/ci.yml` `check-gates` job (ubuntu-latest, `actions/checkout@v4` only — **no setup-go, no network, no Docker, preserved**) appends exactly one step after the architecture step:

```yaml
      - name: Load-scenario drift gate (single-source ghz scenarios, bench schema, load liveness)
        run: make check-load-scenarios
```

No paths filter (consistent with the other check-gates steps: a filter recreates the blind spot). The gate is drift/static-only — it never runs ghz, so CI stays network-free.

### 4.6 REQ-6 — Done-gate chain

`AGENTS.md` done-gate (line 53) gains `&& make check-load-scenarios` placed with the other pure-script gates (after `make check-audit-manifest`), and the surrounding prose gains one sentence noting the gate's isolation class: pure bash+grep, CI-safe (no Go/Docker/network), self-testing fixtures. (The verification chain in AC-8 additionally runs `make check-architecture` — already green — to prove script-layer changes don't disturb the existing gates.)

### 4.7 REQ-7 — Documentation

- **`scripts/load/README.md` gRPC section:** `scenarios.yaml` is the **single source of truth** consumed by `run-ghz.sh` (no inline arrays); replace the drifted "audit ListEvents" prose with `ListAuditFacts`; per-scenario `concurrency`/`duration` come from the YAML (100/50/50/20/10 @3m); document the SLO fields, `GZ_SLO_P95_MS`/`GZ_SLO_P99_MS` overrides, results files (`ghz-<name>.json`), and that an SLO breach fails `make load-test-grpc` with non-zero exit. Note the `audit-list-facts` filename.
- **DB section:** delete the `psql "$PG_DSN" -f ...` alternative (documents the hazard); require `PG_DSN_BENCH`; document `make load-test-db-seed`, the app-DSN refusal (`BENCH_ALLOW_APP_DSN=1`), the seed-sanity guard (`BENCH_SKIP_SEED_CHECK=1`), and the defense-in-depth rationale (§4.4).
- **New "Gates" note:** `make check-load-scenarios` runs in CI `check-gates` and the done-gate; what it guards (R1–R4); the hardcoded R4 path list caveat.

---

## 5. API / interface changes (complete inventory)

| Surface | Change | Compat |
|---|---|---|
| `scripts/load/ghz/scenarios.yaml` | `call:` fix (`ListAuditFacts`), rename entry, +`p95`/`p99` fields | Schema **additive** except the one corrected call; name rename changes one results filename |
| `scripts/load/run-ghz.sh` | Inline `SCENARIOS` array deleted; parses YAML via python3; per-scenario ghz flags; SLO enforcement; `GZ_SLO_P95_MS`/`GZ_SLO_P99_MS` | **Public CLI unchanged** (`run-ghz.sh <bearer-token> [grpc-addr]`); progress/trailer output unchanged; ghz, k6, psql, python3 remain the only deps |
| `scripts/check-load-scenarios.sh` (new) | Gate R1–R4 + fixture self-test; Makefile target `check-load-scenarios` | New; `.PHONY` addition |
| `scripts/testdata/load-scenarios/` (new) | Fixture trees (5 positive classes + clean negative) | New |
| `Makefile` | Delete `PG_DSN_BENCH ?=`; guards on `load-test-db`; new `load-test-db-seed`; `.PHONY` | **Breaking by design:** `make load-test-db` without `PG_DSN_BENCH` now errors (was: silent app-DSN fallback) |
| `scripts/load/db/seed-bench.sql` (new) | Seed target + `aero_load_bench_seed` marker | New; no migration files touched |
| `scripts/load/db/partition-index-bench.sql` | Header usage/seed comments only | No SQL semantics change |
| `.github/workflows/ci.yml` | +1 step in `check-gates` | Additive; job isolation preserved |
| `AGENTS.md` | done-gate chain + prose | Additive |
| `scripts/load/README.md` | gRPC/DB/Gates sections | Content fix + documentation |
| Live Go, migrations, k6, sink, existing gates | **untouched** | — |

---

## 6. Failure modes and mitigations

| # | Failure mode | Behavior | Mitigation |
|---|---|---|---|
| FM-1 | ghz JSON schema drift (field rename, µs units, missing percentiles) | SLO check fails closed → target exits non-zero | D5 strict parser; shape pinned from a real run before merge (open item); header comment records the verified shape |
| FM-2 | Malformed YAML (typo, missing/unknown field) | Runner aborts **before any ghz run** with `ERROR: scenarios.yaml line N`; gate R2(c) also reddens | D4 strict parser + R2(c) field-presence/format checks |
| FM-3 | New scenario added to YAML | Runner picks it up automatically; results file auto-named | Single-source design; R2(c) requires all 7 fields |
| FM-4 | Inline runner array reintroduced | Gate R2(b) reddens (inline literal not in YAML) | Tripwire + `runner_divergence` fixture keeps it non-vacuous |
| FM-5 | Dead/renamed RPC in YAML | Gate R1 reddens in CI (and ghz would 404 locally) | R1 block-scoped rpc resolution |
| FM-6 | Bench table renamed, or new hot table added to bench SQL | Gate R3 reddens (missing `CREATE TABLE.*` in migrations) | R3 + fixtures |
| FM-7 | New **system** relation in bench SQL | Gate R3 reddens (false positive) | Allowlist in script header, each entry justified; header documents the update procedure |
| FM-8 | `FOR UPDATE SKIP LOCKED` / `\echo`/comment lines pollute extraction | False-red or missed relations | D8 normalization (comment/meta-line filters + FOR-UPDATE neutralization) — **the two extra traps beyond the evidence** |
| FM-9 | `PG_DSN_BENCH` unset | `make load-test-db` fails with targeted error | D9 guard (no default remains) |
| FM-10 | `PG_DSN_BENCH` = app DSN | Refused unless `BENCH_ALLOW_APP_DSN=1`; even then seed guard refuses (app DB has no marker) | D9 dual-layer defense; string-equality weakness documented, marker is substantive |
| FM-11 | Unseeded/empty bench DB | Seed guard refuses with actionable message | D9 probe + README |
| FM-12 | Stale seed (schema/migration drift) | Warning (non-fatal) | Marker `schema_version` check; re-seed suggested |
| FM-13 | Fixture tree deleted / gate goes vacuous | Gate fails (`SELF-TEST FAIL`) | E8 semantics: fixtures are part of the gate; positive must fire, negative must be clean |
| FM-14 | SLO breach during a local load run | `SLO FAIL` per scenario + non-zero exit after all scenarios complete | D5 accumulator (no mid-campaign abort) |
| FM-15 | CI accidentally runs ghz | Not possible by construction | Gate is static drift-only; check-gates stays network-free (no ghz, no setup-go) |
| FM-16 | Seed column lists drift from migrations | Seed fails against bench DB or produces wrong shapes | D10 header note + column lists locked from migrations at implementation; manual re-verify on migration change (documented; out of gate scope by spec) |
| FM-17 | Gate token hygiene (`go`/`docker`/`python3`/`jq` words in code lines) | AC-4 grep would fail | Constraint noted in design: banned tokens only in full-line comments; wording of echo strings avoids standalone `go` |

---

## 7. Implementation steps (ordered)

1. **YAML** (§4.1): fix call, rename entry, add p95/p99 to all five entries.
2. **Runner** (§4.2): delete `SCENARIOS`; add python3 YAML→TSV parser heredoc; per-scenario ghz loop; python3 SLO checker heredoc; `GZ_SLO_*_MS` overrides; header note; keep CLI/trailer.
3. **Pin ghz JSON shape** (open item): one short real `ghz --format json` run against a live cell; record field name/units in the checker and header.
4. **Gate script** (§4.3): `scripts/check-load-scenarios.sh` with R1–R4 rule functions (root-parametrized), fixture sweep, exit contract.
5. **Fixtures** (§4.3): `scripts/testdata/load-scenarios/{positive/{dead_rpc,runner_divergence,bench_table_missing,missing_k6,malformed_yaml}, negative/clean}` mini-trees.
6. **Makefile** (§4.4): delete default, guards, `load-test-db-seed`, `check-load-scenarios` target, `.PHONY`.
7. **Seed SQL** (§4.4): `scripts/load/db/seed-bench.sql`; update bench header usage/seed comments.
8. **CI** (§4.5): append one step to `check-gates`.
9. **AGENTS.md** (§4.6) + **README** (§4.7).
10. **Verify** (§8): AC-1..AC-9, mutation probes, full done-gate, `make check-architecture`.

---

## 8. Testable acceptance mapping

| Acceptance | Design element | Verification |
|---|---|---|
| AC-1: 5 scenarios, `ListAuditFacts` ×1, zero `ListEvents` anywhere, 10 SLO lines | §4.1 | `grep -c '^  - name:'` → 5; `grep -c 'call: aeropb.AuditLedgerService.ListAuditFacts'` → 1; `grep -c 'ListEvents' {yaml,run-ghz.sh,README}` → 0; `grep -cE '^    (p95\|p99): [0-9]+(ms\|s)$'` → 10 |
| AC-2: no `SCENARIOS=`, ≥1 `scenarios.yaml` ref, CLI doc kept | §4.2 | grep checks on `run-ghz.sh` |
| AC-3: python3 in runner, no jq/yq, `GZ_SLO_*_MS` present | §4.2/D6 | grep checks |
| AC-4: gate green on clean tree; no uncommented `go\|docker\|python3\|jq` tokens | §4.3 | `make check-load-scenarios` → 0; AC-4 grep |
| AC-5: mutation probes redden: (1) `ListEvents` call in yaml; (2) inline `echo aeropb.AccountService.GetMe` in runner; (3) `audit_ledger`→`audit_ledgr` in bench SQL; (4) empty/removed `k6/account.js` | R1 / R2b / R3 / R4 | each `make check-load-scenarios` → ≠0; restore → 0 |
| AC-6: `PG_DSN_BENCH ?=` gone; unset → ERROR; app DSN → refused; `BENCH_ALLOW_APP_DSN=1` → still refused by seed guard; seed → 0 (×2 idempotent); bench with seed → 0; `BENCH_SKIP_SEED_CHECK=1` → 0 | §4.4/D9/D10 | Makefile greps + target runs (needs a local bench DB for the green paths; red paths need no DB) |
| AC-7: exactly one appended CI step; check-gates still checkout-only | §4.5 | grep ci.yml |
| AC-8: done-gate chain updated and fully green | §4.6 | `grep check-load-scenarios AGENTS.md`; full gate chain → exit 0 |
| AC-9: README has no `ListEvents`, no `psql "$PG_DSN"`, ≥3 of `load-test-db-seed\|single source of truth\|GZ_SLO_` | §4.7 | greps |

---

## 9. Out of scope (must not)

k6 scenario content/mix/endpoints/thresholds; sink behavior; ghz target addresses; load durations/concurrency beyond making the YAML authoritative; changes to existing gates (`check-architecture.sh`, `check-audit-swallow.sh`, `check-proto-sync.sh`, …) or their CI steps; python in the CI gate; any new binary/tool; live Go code, migrations, proto changes; distributed/multi-region load orchestration.

## 10. Risks / open items

1. **ghz JSON percentile field/units** — unobservable in-repo (no committed result artifacts). Must be pinned from a real run before merge (FM-1); the fail-closed parser covers surprises. Mitigation if the shape differs from expectation: adjust the checker's key/unit only, no contract change.
2. **R2 vacuity after refactor** — expected; the `runner_divergence` fixture + R2(a) path-reference check keep it load-bearing (documented in script header).
3. **DSN string-equality limitation** — accepted; the seed marker is the substantive guard; both layers documented (FM-10).
4. **Seed column-list drift** vs. migrations — managed by header note + manual re-verify; out of gate scope per spec (FM-16).
5. **`seed-bench.sql` runtime** — 1M-row audit_ledger seed must stay within a reasonable window; volumes are psql-variable overridable per cell.
