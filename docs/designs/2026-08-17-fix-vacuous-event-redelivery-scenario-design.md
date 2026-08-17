# Design — Fix the vacuous redelivery scenario in `event-soak.js`: per-scenario exec wiring, non-vacuity thresholds, and a whole-tree R5 gate rule

- **Date:** 2026-08-17
- **Module:** `scripts/load/k6` (`event-soak.js`, `account.js`, `callback-soak.js`), `scripts/check-load-scenarios.sh`, `scripts/testdata/load-scenarios/`, `scripts/load/README.md`, `Makefile` (no change)
- **Direction:** fix vacuous redelivery scenario in `event-soak.js` — the at-least-once/no-double-apply assertion never executes; defect fix + threshold non-vacuity + whole-tree gate rule with regression fixtures, no product-behavior surface change
- **Status:** verified (all evidence claims re-verified against HEAD `ce43cc2`); no repository changes in this document — implement per the migration steps below

---

## 1. Verification verdict

Every load-bearing claim in the incoming requirements was re-verified against the current tree (HEAD `ce43cc2`). **All claims confirmed.** Two implementation-relevant refinements found beyond the cited evidence: (R1) `postEvents`/`get` pass tags as a **4th/3rd positional argument** to `http.post`/`http.get`, which k6 ignores (tags belong in `params.tags`), so *request-level* metric thresholds are also vacuous today; (R2) `callback-soak.js` has a second latent vacuity — if subscription registration fails, its `delivery_verified`/`signed_delivery` rates get **zero samples** and pass green. Both are absorbed by the same acceptance contract (Section 8, ACC-2).

| # | Claimed evidence | Repository truth (verified this session) | Verdict |
|---|---|---|---|
| E1 | Vacuity: both scenarios bind to the implicit `default` exec, `__ENV.SCENARIO` gates all VUs identically, `make load-test-event` passes no `SCENARIO` | ✅ Exact. `event-soak.js` `options.scenarios` has `fresh` (vus `VUS-1`) and `redelivery` (vus 1), **neither** declares `exec:` → k6 binds both to the implicit `export default function ()`. The default body branches on `const scenario = __ENV.SCENARIO \|\| ''` (line 61); k6 `__ENV` is process-global, so **every** VU in **both** scenarios sees the same value. `Makefile:194-204` `load-test-event` passes only `-e BASE_URL -e TOKEN -e SOURCE -e SOURCE_KEY -e BATCH -e VUS -e DURATION` — no `-e SCENARIO`. All VUs therefore take the fresh branch; the redelivery branch (`receiptStable.add(...)`, `event-soak.js:72-80`) never executes → `receipt_stable{scenario:redelivery}` has zero samples → the k6 tag-filtered threshold can never fail (vacuously green). **Stronger finding:** even an operator-set `-e SCENARIO=redelivery` cannot rescue it — the pool is seeded only by the fresh branch, so the redelivery path would `sleep(2); return` forever with zero samples. The assertion is structurally unreachable for either env value | ✅ Exact |
| E2 | `EventApplied`/`EventDuplicate` sit at `event_receipt.go:23-24` | ✅ Exact. Line 23 `EventApplied EventApplyStatus = "applied"`; line 24 `EventDuplicate EventApplyStatus = "duplicate"`. These string values are exactly what the ingest API echoes as `data.events[].status` and what the soak compares (`statuses[0].status === 'duplicate' \|\| 'applied'`, `event-soak.js:70`) — the assertion strings are grounded in the live constants | ✅ Exact |
| E3 | Dedup unique constraint in migrations | ✅ Exact. `migrations/000033_create_event_receipts.up.sql` → `CONSTRAINT uq_event_receipt_source_event UNIQUE (source, event_id)`; `000050_scope_events_and_audit_by_tenant.up.sql:6-8` drops it and adds `CONSTRAINT uq_event_receipt_tenant_source_event UNIQUE (tenant_id, source, event_id)`. Combined with `claimEventReceipt`'s `OnConflict{DoNothing: true}` (`event_receipt.go:229-243`) and the terminal `duplicate` status, this is the at-least-once/no-double-apply mechanism the soak is designed to exercise | ✅ Exact |
| E4 | Gate passes today | ✅ Exact. `make check-load-scenarios` → exit 0, `✓ Load-scenario check passed (R1: calls resolve in the proto; R2: runner == scenarios.yaml; R3: bench tables exist in migrations; R4: load assets live)`. The gate today covers ghz/YAML/bench/k6-*liveness* only — **no rule over k6 scenario/exec wiring**, which is exactly the gap R5 fills | ✅ Exact |
| E5 | Same shared-default anti-pattern exists in `account.js` and `callback-soak.js`; the new whole-tree rule must conform or exclude them | ✅ With nuance. `account.js`: scenarios `control_plane` + `legacy_compat`, both implicit `default`, gated by `__ENV.SCENARIO` with an `__ITER % 5 === 4` fallback (line 58) — same anti-pattern shape; additionally its `http_req_duration{scenario:control_plane}` threshold is vacuous because `get()` passes the tag object as the 3rd positional arg to `http.get` (ignored). `callback-soak.js`: single scenario bound to implicit default — the *mechanical* whole-tree rule "every scenario declares explicit `exec`" would flag it (conform = trivial rename), and it carries R2 (registration-failure vacuity). **Design choice: conform all three files; no exclusion allowlist** (mirrors R2(b)'s zero-carve-out house style) | ✅ Confirmed |
| — | `make load-test-event` env contract | `VUS ?= 50`, `DURATION ?= 3m`, `BATCH ?= 10` (Makefile:170-181, 194-204). `SCENARIO` appears nowhere in the Makefile or README — zero external consumers of the removed env var | ✅ Confirmed |
| — | Fixture k6 scaffold | Every fixture's `scripts/load/k6/*.js` is a minimal stub (`export default function () {}`) for R4 liveness; only `positive/missing_k6` omits `account.js`. Stubs contain no `options.scenarios` and no `__ENV.SCENARIO` → trivially R5-clean (Section 4.3) | ✅ Confirmed |
| — | Gate isolation contract | Clean header: "bash + POSIX text tools (grep, sed, awk) only — no compiled toolchain, no container runtime, no network, no interpreter, no JSON preprocessor, no new binaries". R5 must stay grep/awk-level | ✅ Confirmed |

**Defect confirmed:** `event-soak.js`'s redelivery scenario never executes its assertion under the documented `make load-test-event` invocation, so the at-least-once/no-double-apply contract has **zero test coverage** in the soak while its threshold reports green. `account.js`'s control-plane SLO tag filter and `callback-soak.js`'s delivery rates carry adjacent vacuity of the same class (R1/R2).

---

## 2. Problem statement

`event-soak.js` defines two k6 scenarios (`fresh` @ `VUS-1` VUs, `redelivery` @ 1 VU) but wires neither to an explicit `exec`. k6 therefore runs the implicit `export default` for **both**, and the default body branches on the **process-global** `__ENV.SCENARIO`. Because `make load-test-event` never sets that variable, every VU executes the fresh batch path; the one-VU redelivery scenario silently runs fresh work too, and `receiptStable` — the rate that asserts redelivered events are receipted `duplicate` and never double-apply — is never sampled. k6 thresholds over a tag-filtered metric with no matching samples cannot fail, so the soak reports a pass while the very assertion it claims to verify never executes.

The same structural pattern (scenarios bound to the implicit default + `__ENV.SCENARIO` gating every VU identically) exists in `account.js`, and the mechanical whole-tree rule that fixes the class would also touch `callback-soak.js`'s single-scenario file. The fix therefore has three parts, all inside the load-tooling surface:

1. **Per-scenario exec wiring** for all three k6 files: every scenario declares an `exec:` naming an exported function; scenario-specific behavior lives in that function; `__ENV.SCENARIO` is deleted outright (no global gate remains).
2. **Non-vacuity thresholds**: every SLO-critical rate/Trend threshold gains a `count>0` floor, so "the assertion never ran" reddens instead of passing silently. Request tags move into `params.tags` (k6 contract) so tag-filtered thresholds match real samples.
3. **Whole-tree gate rule R5** in `scripts/check-load-scenarios.sh` (bash + POSIX text tools, self-testing fixtures, same isolation class as R1-R4): every scenario in every `scripts/load/k6/*.js` must declare an explicit `exec` that resolves to an exported function, and no k6 file may reference `__ENV.SCENARIO`. Positive fixtures prove the rule fires; `negative/clean` stays clean.

---

## 3. Design overview

Contract after the change (per layer):

```text
event-soak.js                          scenario fresh      → exec: 'freshExec'      (VUS-1 VUs)
                                       scenario redelivery → exec: 'redeliveryExec' (1 VU)
   └─ both exported functions; shared module-level redeliveryPool (same k6 process)
   └─ no __ENV.SCENARIO anywhere; tags passed via params.tags
   └─ thresholds: 'receipt_stable{scenario:redelivery}': ['rate>0.999', 'count>0']

account.js                             scenario control_plane → exec: 'controlPlaneExec' (60% VUs)
                                       scenario legacy_compat → exec: 'legacyExec'       (40% VUs)
   └─ __ITER%5 heuristic deleted; tags via params.tags → tagged SLO threshold now real
   └─ thresholds: tagged p95 + 'count>0'

callback-soak.js                       scenario callback_soak → exec: 'callbackSoak'   (all VUs)
   └─ default renamed to exported callbackSoak; no other behavior change
   └─ thresholds: 'delivery_verified{scenario:callback_soak}': ['rate>0.99', 'count>0'] (same for signed_delivery)

scripts/check-load-scenarios.sh        + R5 (scenario wiring + no __ENV.SCENARIO), header enumeration
scripts/testdata/load-scenarios/       + positive/scenario_shared_default, positive/scenario_env_gate
   └─ existing stub k6 files stay R5-clean (no options.scenarios, no SCENARIO token)
```

Untouched: `Makefile` (env pass-through identical; `SCENARIO` never existed), ingest API, migration set, `event_receipt.go` constants, R1-R4 semantics.

---

## 4. API changes

**No product-API changes** — no handlers, protobuf, HTTP/gRPC surfaces, domain types, or configuration change. The "API" affected is the internal k6-script contract (`options.scenarios.exec`, env vars, threshold semantics, gate rules):

| Contract point | Before | After |
|---|---|---|
| `options.scenarios.*.exec` (all 3 files) | absent → implicit `default` | explicit `exec:'<exportedFn>'` on every scenario |
| `__ENV.SCENARIO` gating | branches in `default` (event-soak, account) | **deleted outright**; per-scenario behavior lives in the exec function |
| request tags (`http.post`/`http.get`) | 4th/3rd positional arg (ignored by k6) | `params.tags` (honored) |
| `receipt_stable{scenario:redelivery}` threshold | `['rate>0.999']` (vacuous: 0 samples) | `['rate>0.999', 'count>0']` |
| `http_req_duration{scenario:control_plane}` (account) | `['p(95)<200']` (vacuous: no tagged samples) | `['p(95)<200', 'count>0']` |
| `delivery_verified` / `signed_delivery` (callback) | `['rate>0.99']` (vacuous on register failure) | `['rate>0.99', 'count>0']` |
| Gate `scripts/check-load-scenarios.sh` | R1-R4 | + R5 (scenario wiring contract) |
| Fixture sweep class map | 6 positive classes | + `scenario_shared_default` + `scenario_env_gate` (both → `rule_r5`) |
| Makefile `load-test-event` | `-e BASE_URL TOKEN SOURCE SOURCE_KEY BATCH VUS DURATION` | **unchanged** (both scenarios always run; no $()/SCENARIO additions) |

### 4.1 FIX-1 — `event-soak.js`: per-scenario exec + no env gate + tags fix + non-vacuity floors

```
export function freshExec() { … }        // exactly the current fresh branch body
export function redeliveryExec() { … }   // exactly the current redelivery branch body
// `export default function () { __ENV.SCENARIO … }` REMOVED — no default branch remains
options.scenarios.fresh.exec = 'freshExec'
options.scenarios.redelivery.exec = 'redeliveryExec'
thresholds['receipt_stable{scenario:redelivery}'] = ['rate>0.999', 'count>0']
```

- **Pool hand-off:** `redeliveryPool` is module-level, and k6 runs all VUs/scenarios in one process, so fresh execs seed and the single redelivery VU consumes exactly as today. The redelivery exec keeps its `sleep(2); return` empty-pool spin — each iteration re-checks, and fresh VUs seed within the first batch (sub-second against localhost). If the pool ever fails to fill, `count>0` now **reddens** the run instead of green-silence.
- **Reachability nuance (from E1):** with `VUS=1`, fresh gets `Math.max(1, VUS-1)` = 1 VU and redelivery 1 VU — pre-existing behavior, kept and documented.
- **`postEvents`:** `http.post(url, body, { headers: {…}, tags }, /*tags*/)` → drop the 4th arg; fold into `params.tags`. Unlocks `http_req_duration{scenario:…}`-style thresholds and makes the soak's tags honest.
- **Assertion string grounding (E2):** the `'duplicate'`/`'applied'` comparisons map directly to `EventDuplicate`/`EventApplied` (`event_receipt.go:23-24`) — unchanged; the tolerance for `applied` covers the (effectively impossible) race where a redelivery lands before its fresh copy commits; `duplicate` is deterministic for pool-seeded events (pool is populated only after the fresh response).
- **k6 version compatibility:** only `exec` (per-scenario function selection, supported since the k6 scenario API's early days) is used. Per-scenario `env:` (k6 ≥ 0.49) is **deliberately not used** — distinct exec functions make it unnecessary and avoiding it keeps the toolchain floor unchanged.

### 4.2 FIX-2 — `account.js`: real scenario split + honest tags

```
export function controlPlaneExec() { … }   // /v1/me, /v1/me/overview, /v1/me/sources,
                                           // /v1/me/memberships, /v1/account-datasets, /v1/me/data
export function legacyExec() { … }         // /v1/files, /v1/messages, /v1/comments + Deprecation-header check
options.scenarios.control_plane.exec = 'controlPlaneExec'   // vus = ceil(VUS*0.6)
options.scenarios.legacy_compat.exec = 'legacyExec'         // vus = floor(VUS*0.4)
// `__ENV.SCENARIO` and `__ITER % 5 === 4` deleted — the heuristic existed only to
// compensate for the broken scenario wiring
thresholds['http_req_duration{scenario:control_plane}'] = ['p(95)<200', 'count>0']
```

- `get(path, tags)` → `http.get(url, { …params, tags }, )` — tags into `params.tags` (same fix as 4.1).
- **Load-profile delta (documented, intended):** previously every VU ran `default`, with ~20% of *iterations* (via `__ITER % 5 === 4`) taking the legacy path and diluting the control-plane measurement. After the fix, 60% of VUs run strictly control plane, 40% strictly legacy — the annotated design of the scenario structure, now actually wired.

### 4.3 FIX-3 — `callback-soak.js`: conform (trivial), close the R2 vacuity

```
export function callbackSoak() { … }       // rename of export default function ()
options.scenarios.callback_soak.exec = 'callbackSoak'
thresholds['delivery_verified{scenario:callback_soak}'] = ['rate>0.99', 'count>0']
thresholds['signed_delivery{scenario:callback_soak}'] = ['rate>0.99', 'count>0']
```

- No other behavior change. The `count>0` floors close the latent vacuity: today, if `registerSubscription()` fails every time, the default body returns before any `.add()`, producing zero samples and a green run despite 100% register failure.

### 4.4 REQ-1 — gate R5: whole-tree scenario-wiring contract (bash + POSIX text tools only)

Inside `scripts/check-load-scenarios.sh`, new `rule_r5` scanning `scripts/load/k6/*.js` in the real tree (and fixture roots):

```
R5 — k6 scenario wiring contract: every scenario in every k6 file must be
     explicitly executed.
   (a) every file that declares `options.scenarios` must carry one `exec:`
       line per scenario key (1:1), each naming an exported function in the
       same file (`export function <name>(`), or the literal `default`;
   (b) no k6 file may reference `__ENV.SCENARIO` — with explicit per-scenario
       exec there is no legitimate use, and the token is the tripwire for the
       shared-default/environment-gate anti-pattern (vacuous-scenario class:
       one scenario's assertions execute with zero samples and green
       thresholds).
```

Mechanical formulation (mirroring R2(c)'s count semantics and R2(b)'s literal ban):

```bash
# (a) exec coverage
for f in "$root"/scripts/load/k6/*.js; do
  [ -f "$f" ] || continue
  grep -q 'options.scenarios' "$f" || continue
  scen=$(grep -cE '^    [a-z_]+: \{' "$f" || true)          # scenario keys (4-space indent)
  execs=$(grep -cE "[[:space:]]+exec: '" "$f" || true)      # explicit exec lines
  if [ "$execs" -ne "$scen" ]; then
    echo "ERROR: R5 $f declares $scen scenario(s) but $execs exec line(s) — every scenario needs an explicit exec:"
    violations=$((violations + 1))
  fi
  # (b) exec targets resolve to exported functions
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    if [ "$name" != "default" ] && ! grep -qE "^export function $name\(" "$f"; then
      echo "ERROR: R5 $f exec '$name' does not resolve to an exported function in the same file"
      violations=$((violations + 1))
    fi
  done < <(grep -oE "exec: '[A-Za-z_][A-Za-z0-9_]*'" "$f" | sed "s/exec: '//;s/'//" || true)
done
# (c) no global scenario env gate
if grep -q '__ENV.SCENARIO' "$root"/scripts/load/k6/*.js 2>/dev/null; then
  echo "ERROR: R5 __ENV.SCENARIO referenced in scripts/load/k6/*.js — global env gating is the vacuous-scenario anti-pattern (use explicit exec:)"
  violations=$((violations + 1))
fi
```

Notes: (a) counts are 1:1 with scenario keys, so a scenario *without* exec reddens and an exec *without* a scenario reddens (over-wiring is as suspicious as under-wiring); (b) catches typos both at the gate and at k6 validation time (k6 aborts on an unresolvable exec — fail-fast bonus); (c) is a bare-token ban, the same tripwire philosophy as R2(b)'s inline-literal ban. Fixture stubs (`export default function () {}`, no `options.scenarios`, no token) are trivially clean — verified (Section 1 evidence row 8).

### 4.5 REQ-2 — fixtures + sweep wiring

1. **`positive/scenario_shared_default`** — copy `negative/clean`'s layout; replace `scripts/load/k6/event-soak.js` with a violation: two scenarios, **no** `exec` lines (trips R5(a)); other rules stay silent (valid proto copy, migrations, bench SQL, stub `await`-free k6 siblings, 7-field scenarios.yaml, guard-token runner stub — all cloned from `negative/clean`).
2. **`positive/scenario_env_gate`** — copy as above; replace `scripts/load/k6/event-soak.js` with a version that has an `exec:` on every scenario**and** a stray `__ENV.SCENARIO` token (trips R5(c) while passing R5(a), proving (c) is load-bearing rather than redundant with (a)).
3. **Sweep case map**: add `scenario_shared_default|scenario_env_gate) want=rule_r5 ;;`. Designated-rule-fires / others-silent apply automatically (existing sweep machinery). Stub k6 files in the *other* 7 fixture trees contain neither `options.scenarios` violations nor the token → unchanged and clean.
4. **Anti-vacuity**: with R5 present but both fixtures wired, the sweep self-test proves the rule is load-bearing; with R5 stubbed out, `SELF-TEST FAIL: positive fixture … matched nothing` (captured once during implementation, then continuous in CI).

### 4.6 REQ-3 — doc sync (text-only)

- `scripts/check-load-scenarios.sh` header: enumerate R5 under the rules list, document the two new fixture classes and the token ban.
- `scripts/load/README.md`: (a) event-soak section — note both scenarios run on every invocation, the redelivery VU asserts `duplicate` receipts, and the `receipt_stable`/`count>0` floor means a run where redelivery never executed fails; (b) add an R5 bullet to the R1-R4 rule list (lines 132-151 area); (c) account-soak section — if present, note the strict 60/40 exec split replaces the old iteration heuristic; (d) note `SCENARIO` is not a supported variable anywhere.
- No AGENTS.md change (it does not enumerate sub-rules; house precedent, see the 2026-08-17 run-ghz design §4.5).

---

## 5. Compatibility constraints

- **Env-var contract unchanged**: `BASE_URL`, `TOKEN`, `SOURCE`, `SOURCE_KEY`, `BATCH`, `VUS`, `DURATION` keep exact semantics for all three k6 files; `make load-test-event` / `load-test-http` / `load-test-callback` pass-through untouched. `SCENARIO` had zero consumers (not in Makefile, not in README) — deleting it is a non-breaking removal.
- **k6 toolchain floor unchanged**: only `exec` is used; per-scenario `env` (k6 ≥ 0.49) is avoided. The gate never invokes k6 (CI-safe; E2E k6 runs stay manual where k6 + live server + token exist).
- **Threshold values**: every existing SLO number (`rate>0.999`, `rate>0.99`, `p(95)<…`, `p(99)<…`) is preserved verbatim; only `count>0` floors are added (ANDed — a floor cannot loosen an existing bound).
- **R1-R4 semantics untouched**: the real tree and all existing fixture trees stay clean under R5 (stubs verified — Section 1); `negative/clean` stays negative; `missing_k6` still trips exactly R4.
- **k6-process state**: pool hand-off relies on k6's single-process multi-VU model — already the model the current script depends on; no new cross-process assumption.
- **File-size caps**: Go-only gate (`make check-file-sizes`) unaffected; k6 files grow by a few lines; gate `~288 → ~330` lines (no cap applies to bash).
- **Load-profile delta** (account.js): strict 60/40 VU split replaces the `__ITER % 5 === 4` heuristic — intended, documented, and the scenario names/annotations already advertised this split.

---

## 6. Failure modes

| # | Failure mode | Behavior | Mitigation |
|---|---|---|---|
| FM-1 | Redelivery pool never fills (fresh seeding broken, server down, auth rejects all fresh posts) | Old: `receipt_stable` 0 samples, **green pass**. New: `count>0` floor **reddens** the run | FIX-1 floors + `ingest_ok{scenario:fresh}` rate already reddens on fresh failures |
| FM-2 | Future refactor reintroduces the shared-default/`__ENV.SCENARIO` pattern (any k6 file) | R5(a)/(c) reddens in CI | REQ-1 token ban + 1:1 exec count + positive fixtures |
| FM-3 | `exec` typos / target renamed but scenario not updated | k6 aborts at validation ("unable to find exported function") before a single request; R5(b) reddens in CI | Gate + k6 fail-fast, layered |
| FM-4 | Redelivery lands before its fresh copy commits (pool-seeded race) | Receipt status `applied` instead of `duplicate` | Existing tolerance (`applied \|\| duplicate`) retained; the receipt count assertion (length === 1) still forbids double-apply |
| FM-5 | Request tags regress to the ignored positional form in `account.js` | `http_req_duration{scenario:control_plane}` loses samples → `count>0` **reddens** (no longer silent) | FIX-2 + REQ-1 (no rule change needed; the floor makes the vacuity visible) |
| FM-6 | Callback register endpoint fails for the whole run (`subscriptionID` empty) | Old: zero samples, green. New: `count>0` reddens | FIX-3 floors |
| FM-7 | Fixture sweep contamination (new fixtures trip another rule, or updated stub breaks an existing fixture's isolation) | `SELF-TEST FAIL: … also trips rule_…` | REQ-2 mandates cloning `negative/clean` (proto/migrations/bench/yaml/runner-guard stubs) and stub k6 files kept minimal |
| FM-8 | k6 absent locally (this environment) | Gate is shell-only (no k6 invocation) — stays green/meaningful in CI; E2E k6 evidence captured only where k6 + infra exist | REQ-1 design; ACC-7 marked manual |
| FM-9 | Edge `VUS=1` / `BATCH=1` | fresh = 1 VU + redelivery = 1 VU (pre-existing); fresh floor `applied >= floor(1*0.9)=1` fine; pool push `events[0]` fine | Documented in FIX-1; no behavior change |
| FM-10 | `count>0` misread as "must run forever" | `count` = sample count of the Rate/Trend; any single executed assertion yields ≥1 | Floor semantics documented in FIX-1/3 and README |

---

## 7. Migration steps

No data/DB/product migration exists — this is purely CI/load-tooling. "Migration" is the implementation sequence with evidence capture (each step independently revertible; the tree is gate-green today, so the change set is revert-safe as a whole).

1. **Baseline capture (pre-change):** `make check-load-scenarios` → exit 0 (E4 evidence); `grep -n 'SCENARIO' Makefile scripts/load/README.md scripts/load/k6/*.js` → shows the anti-pattern only in `event-soak.js`/`account.js`; `grep -n 'receiptStable.add' scripts/load/k6/event-soak.js` → the sole producer sits under the dead `SCENARIO === 'redelivery'` branch (E1 evidence).
2. **FIX-1 (`event-soak.js`):** split `default` into `export function freshExec()` / `export function redeliveryExec()`; add `exec:` to both scenarios; delete the `__ENV.SCENARIO` branch; fix `postEvents` tags into `params.tags`; add `count>0` to the `receipt_stable` threshold. Verify: `grep -cE '^    [a-z_]+: \{' < file` == `grep -cE 'exec: '…` == 2; zero `__ENV.SCENARIO`; both `export function <name>(` lines present.
3. **FIX-2 (`account.js`) + FIX-3 (`callback-soak.js`):** same pattern; verify the 1:1 exec counts per file and the deleted heuristic/env-gate tokens; callback-soak keeps every other line byte-identical.
4. **REQ-1 (gate `rule_r5`):** add `rule_r5` + header enumeration; verify real tree green and each sub-rule (a)/(b)/(c) catches its flavor on throwaway copies.
5. **REQ-2 (fixtures):** clone `negative/clean` → `positive/scenario_shared_default` (k6 event-soak.js without `exec`) and `positive/scenario_env_gate` (with `exec` + stray token); add the two arms to the sweep case map.
6. **Anti-vacuity proof (once):** run the sweep with the new fixtures wired but `rule_r5` stubbed → `SELF-TEST FAIL: positive fixture scenario_shared_default matched nothing`; then with R5 live → pass, only `rule_r5` fires for each fixture.
7. **REQ-3 (docs):** gate header (step 4), README (event-soak section, R-rule list, `SCENARIO` non-support note).
8. **Done gate:** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-load-scenarios && make check-composition-root && make check-proto-sync && make check-integration-compile && make build`.
9. **Rollback:** revert the single change-set commit. Gate was green pre-change; the revert restores a known-good (defective-but-green) state; the reproduction evidence is in §1.

---

## 8. Acceptance mapping (testable)

| Req | Acceptance criterion (made executable) | Evidence |
|---|---|---|
| ACC-1 | `event-soak.js`: every scenario declares `exec:` resolving to an exported function in the same file; zero `__ENV.SCENARIO` references anywhere in `scripts/load/k6/`; the `receipt_stable` producer is reachable (its exec is wired to the redelivery scenario) | §7 steps 2-4 static greps; R5(a)/(b)/(c) green on the real tree |
| ACC-2 | Non-vacuity floors: `receipt_stable{scenario:redelivery}`, `http_req_duration{scenario:control_plane}`, `delivery_verified{scenario:callback_soak}`, `signed_delivery{scenario:callback_soak}` thresholds each include `count>0`; request tags are passed via `params.tags`, not positional args | §7 steps 2-3 |
| ACC-3 | Whole-tree rule conforms (not excludes) `account.js` and `callback-soak.js`: both gain explicit `exec`; no exclusion allowlist exists anywhere in the gate | §7 step 3; R5 green |
| ACC-4 | Gate: `make check-load-scenarios` exits 0; `positive/scenario_shared_default` fires **only** `rule_r5` (R5(a)); `positive/scenario_env_gate` fires **only** `rule_r5` (R5(c)); `negative/clean` clean; all 7 existing fixture trees still single-rule-isolated (stub k6 files R5-clean) | §7 steps 4-6 |
| ACC-5 | Makefile env contract unchanged: `load-test-event` passes the same 7 `-e` variables; no `SCENARIO` ($() or `-e`) introduced; `VUS=1`/`BATCH=1` edge semantics preserved | §5; diff review |
| ACC-6 | Done gate green | §7 step 8 |
| ACC-7 (manual E2E, where k6 + live server + token + sink exist) | `make load-test-event TOKEN=… SOURCE=… SOURCE_KEY=…` → results JSON contains `receipt_stable` samples tagged `scenario=redelivery` with count ≥ 1; redelivered IDs return exactly one receipt, status `duplicate` (tolerance: `applied`); fresh IDs never receipted twice across fresh+redelivery. `make load-test-http TOKEN=…` → `http_req_duration{scenario:control_plane}` has samples. `make load-test-callback TOKEN=… RECEIVER_URL=…` → `delivery_verified`/`signed_delivery` counts ≥ 1 | Optional/recorded evidence; gate stays CI-safe without it (FM-8) |

The assertion strings the soak compares (`duplicate`/`applied`) remain grounded in the live failure vocabulary: `EventDuplicate`/`EventApplied` (`internal/repository/event_receipt.go:23-24`) and the dedup unique constraint `uq_event_receipt_tenant_source_event (tenant_id, source, event_id)` (migration 000050) — the at-least-once/no-double-apply invariant ACC-7 exercises.

---

## 9. Verification gates

```bash
make check-load-scenarios                                            # ACC-1/3/4 (R5 + fixtures run every invocation)
# static ACC-1 greps:
grep -rn '__ENV.SCENARIO' scripts/load/k6/     || echo "clean"        # zero references
for f in scripts/load/k6/*.js; do grep -cE '^    [a-z_]+: \{' "$f"; grep -cE "exec: '" "$f"; done  # 1:1 per file
grep -nE 'export function (freshExec|redeliveryExec|controlPlaneExec|legacyExec|callbackSoak)\(' scripts/load/k6/*.js
grep -n 'count>0' scripts/load/k6/*.js                                # ACC-2 floors present
# anti-vacuity (one-time): fixtures wired + rule_r5 stubbed → SELF-TEST FAIL: … matched nothing; then live → pass (ACC-4)
make test && make lint && make lint-strict && make check-file-sizes && \
make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && \
make check-load-scenarios && make check-composition-root && make check-proto-sync && \
make check-integration-compile && make build                          # ACC-6
# optional manual E2E where infra exists: make load-test-event / load-test-http / load-test-callback with k6 (ACC-7)
```

CI note: this is a `check-gates`-job-and-done-gate change only; no Docker, network, or new binaries anywhere in the diff (fixture k6/other assets are copies; the gate edit is bash+grep+awk only).

---

## 10. Out of scope (explicitly preserved from the direction)

- **No change to the ingest product surface**: handlers, protobuf, `event_receipt.go` constants (lines 23-24), migrations (000033/000050 unique constraints), or config.
- **No Makefile change**: env pass-through is already sufficient; `SCENARIO` is declared an unsupported variable, not a new feature.
- **No exclusion allowlist in R5** — the whole-tree rule conforms all three k6 files, mirroring R2(b)'s zero-carve-out philosophy. (This is where the evidence-named tension "conform or exclude" is resolved: **conform**.)
- **No k6 per-scenario `env`** (k6 ≥ 0.49 dependency deliberately avoided; `exec` suffices and keeps the toolchain floor).
- **No change to R1-R4 semantics**, no change to ghz/YAML/bench assets.
- **No E2E k6 run as a gate** (k6 not installed in this environment; requires live server + token + sink) — static + gate evidence is the CI-safe proof; ACC-7 is manual.
- **No change to the dedup acceptance semantics** (`applied \|\| duplicate` tolerance retained; deterministic `duplicate` for pool-seeded events).
- **No product-behavior surface change**: no handlers, services, repositories, migrations, or README API surface touched; `make check-composition-root` and `make check-proto-sync` unaffected.