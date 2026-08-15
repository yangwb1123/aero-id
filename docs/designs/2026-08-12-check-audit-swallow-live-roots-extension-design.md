# Design — Extend `check-audit-swallow` to the Unscanned Live Directories (B3-2 Gap 2)

**Status:** Proposed (verification complete; implementation not yet landed)
**Date:** 2026-08-12 (doc), evidence re-verified 2026-08-15 against HEAD `5f914b7`
**Module:** `scripts/check-audit-swallow.sh` + `Makefile` target + gate fixtures under `scripts/testdata/audit-swallow/`
**Campaign linkage:** Gap 2 of `docs/campaigns/campaign-aero-id-b3.yaml`; pipeline `docs/campaigns/pipeline-gap2-audit-swallow-roots.yaml`
**Constraint set:** (a) narrow breaker exemption via `grep -v` line filter; (b) mechanical generated-stub exclusion; (c) Option B consistency for no-arg default + unconditional membership tripwires; (d) sso-shaped fixture proves both directions; (e) existing quarantine tripwires/self-tests/FM-16 pins stay byte-identical where possible; (f) full done-gate stays green.

---

## 0. Verification verdict on the evidence (untrusted claims → verified facts)

Every claim in the evidence was re-checked against the live tree at HEAD `5f914b7` (working tree clean). All are **VERIFIED**:

| # | Claim | Verification result |
|---|---|---|
| F1 | Makefile `check-audit-swallow` scans exactly the 11 roots; last four are quarantine tripwires | **VERIFIED.** `Makefile:76-78`: recipe is `@bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage internal/block internal/comment cmd internal/file internal/message internal/notification internal/audit`. `internal/block`, `internal/comment` are absent dirs (FM-12 guards + Makefile tripwire roots); the last four absent too. |
| F2 | The ten unscanned live directories exist | **VERIFIED.** `internal/{connector,outbox,event,hub,region,sso,idempotency,middleware,config,handler}` all `EXISTS`; all are wired from `internal/app/new.go` (control-plane tree). |
| F3 | Zero P1 hits over the ten dirs; exactly three P2 hits, all `c.breaker.Record(` at `internal/sso/client.go:299,306,310`; `breaker.Record` is void | **VERIFIED.** Ran the script's exact patterns (`P1_PATTERN`, `P2_PATTERN`) over all ten dirs excluding `*_test.go`: zero P1; exactly `client.go:299/306/310` bare `c.breaker.Record(false/true)` P2 hits. `internal/sso/breaker.go` `func (b *breaker) Record(success bool)` has no return value → the P1 `_ =` form cannot compile today. |
| F4 | Generated stubs: 10 files, all named `*.pb.go` (dot form); underscore glob `*_pb.go` matches nothing | **VERIFIED.** `find … -name '*.pb.go'` → exactly 10 files (`internal/sso/{auditpb,authzpb,ssopb}/*.pb.go` + `*_grpc.pb.go`, `internal/handler/grpc/aeropb/*.pb.go` + `*_grpc.pb.go`). `find internal -name '*_pb.go'` → **0 files**. The underscore glob is a silent vacuous no-op in this repo. |
| F5 | `internal/event/nats_publisher.go:37` matches neither pattern; no exemption needed | **VERIFIED.** Statement is `if _, err := js.Publish(...)` — begins with `if`, so neither statement-start-anchored pattern matches. |
| F6 | `internal/handler` audit references are read paths only | **VERIFIED.** Zero `.Record(`/`.Log(` sites in non-test, non-`.pb.go` handler files; `h.audit.List`, `h.audit.VerifyChain` are the only audit calls (read paths). |
| F7 | Existing script hardening present (401 lines) | **VERIFIED.** No-arg default ROOTS, UNCONDITIONAL membership tripwires (`internal/comment` + four quarantine roots, no `$# -gt 0` gate), line-anchored invocation pins, guard-PASS counter (4/4), adjacent-path guard (ADR-012), FM-16 pins, positive/cmd/comment/dead-tree/negative fixture self-tests, T-5 wiring assertions. All present. |
| F8 | No AGENTS.md edit required | **VERIFIED.** AGENTS.md mentions `check-audit-swallow` only in the done-gate line and bans statements; it does not enumerate scan roots. |
| — | Gate green at HEAD | **VERIFIED.** `bash scripts/check-audit-swallow.sh` → exit 0. |

**One constraint-vs-reality conflict (must be resolved in the design):** constraint (b) states the stub exclusion as `--exclude='*_pb.go'`. The literal underscore form is **vacuous** in this repository (F4: 0 matches — every committed stub is `name.pb.go` / `name_grpc.pb.go`), so implementing it verbatim would silently re-expose all 10 generated stubs to the P1/P2 scan — the exact hazard class the gate exists to prevent. The verified-correct form is the **dot glob `--exclude='*.pb.go'`**. The design therefore implements constraint (b)'s *intent* (mechanical, self-verifying exclusion of generated stubs) with the dot glob plus a liveness fixture named `generated.pb.go`, so any future regression to the vacuous underscore form reddens the gate. This is the only faithful reading of (b) that satisfies F4's empirically demonstrated naming reality.

**Baseline computed with the script's own patterns (post-change shape):** 0 P1, 3 P2 (breaker), minus the narrow exemption → **0 matches** across all 21 roots once the exclusion and filter are applied.

---

## 1. Problem statement

The gate bans swallowed (`_ = x.Record(`) and bare-call (`x.Record(`) audit/delivery writes via two statement-start-anchored patterns (P1/P2 over `.Record/.Log/.Publish/.FinishPublication/.ClaimPublication/.RequeuePublication/.RequeueDead/.MarkDelivery`). It currently scans 11 roots: the live layered runtime + `cmd` + five absent quarantine tripwire roots. The account/audit/operation control-plane tree — `internal/{connector,outbox,event,hub,region,sso,idempotency,middleware,config,handler}` — is live, wired from `internal/app/new.go`, and **unscanned**. A swallowed or bare-call audit write introduced in any of these ten directories would land green today. This is the B2-2 blind spot.

The extension is blocked by exactly two pre-verified false-positive/mechanical hazards:

1. **`internal/sso` circuit-breaker observations** — three bare `c.breaker.Record(...)` calls (`client.go:299,306,310`) match P2. They are breaker outcome recordings (void return, `false` = availability failure, `true` = healthy), not audit writes.
2. **Committed generated protobuf stubs** — 10 `*.pb.go` files under `internal/sso/{auditpb,authzpb,ssopb}` and `internal/handler/grpc/aeropb/` contain `Record(`-shaped generated code (and would be scanned if not mechanically excluded). They are drift-pinned by `make check-proto-sync` and never hand-edited.

The gate's established invariants must survive the extension unchanged: ADR-012 Option B (no-arg default == Makefile gate line, byte-equal), unconditional root-membership tripwires, line-anchored text-presence pins, fixture-mode totality, the FM-12 quarantine guards (guard-PASS 4/4), the adjacent-path guard, the FM-16 standing pin, and the T-5 wiring assertions.

---

## 2. Design

### 2.1 Gate "API" surface (what changes, what does not)

The gate has no shipped Go API. Its public contract is:

1. **CLI:** `scripts/check-audit-swallow.sh [ROOT...]` — no-arg default scans the full done-gate surface; explicit roots scan exactly those roots but membership tripwires fire **unconditionally** (ADR-012 Option B).
2. **Makefile target** `check-audit-swallow` — the recipe line is the authoritative root list; the script's no-arg default must equal it byte-for-byte.
3. **Exit contract:** exit 0 only when scans + every self-test/tripwire pass; any violation → exit 1 with a named violation line.

Changes are confined to: `Makefile` recipe line, `scripts/check-audit-swallow.sh` (usage header, no-arg default, one new filter variable, one functional change in the scan loop, new self-tests), and five new fixture files. **No Go code, no protobuf, no AGENTS.md changes.**

### 2.2 Extended scan surface (REQ-G2-1)

The 21-root list, in this exact order (existing 11 in historical order, then the ten new roots appended):

```
internal/service internal/repository internal/app internal/storage internal/block internal/comment cmd internal/file internal/message internal/notification internal/audit internal/connector internal/outbox internal/event internal/hub internal/region internal/sso internal/idempotency internal/middleware internal/config internal/handler
```

Applied in three places: the Makefile recipe line, the script's no-arg default `ROOTS=(…)`, and the usage header enumeration. ADR-012 Option B equivalence (recipe line == no-arg default, byte-equal) is machine-checked by the Makefile recipe-line pin (2.6).

### 2.3 Narrow breaker exemption (REQ-G2-2)

**Constraint (a) honored literally:** a post-filter `grep -vE` applied to the scan loop output, filtering **only lines containing the bare statement `c.breaker.Record(`** — never a blanket exclusion of `internal/sso`.

```bash
BREAKER_EXEMPT_PATTERN='^([^:]*:)?[0-9]*:[[:space:]]*c\.breaker\.Record\('
```

Mandated properties, each with its enforcement mechanism:

| Property | Enforcement |
|---|---|
| Statement-anchored | Pattern anchors at line start after optional `path:` / `line:` grep prefixes and whitespace. A mid-line occurrence (`x := foo(); c.breaker.Record(`) is not exempted. |
| Receiver-exact | Only the identifier chain `c.breaker` matches (`c\.breaker\.Record\(`). `d.breaker.Record(`, `c.breaker2.Record(` stay red — pinned by `near_miss.go` fixture. |
| P1 form not exempted | `_ = c.breaker.Record(` line does **not** match the exemption (pattern requires the receiver immediately after optional whitespace). Pinned by `p1_anchor.go` fixture (uncompilable today since `Record` is void; a future-proof pin if `Record` ever gains a return). |
| Dual grep output formats | `^([^:]*:)?[0-9]*:` tolerates both `path:line:content` (directory scans) and `line:content` (single-file fixture greps). |
| Post-filter, patterns untouched | The exemption is applied via `grep -vE "$BREAKER_EXEMPT_PATTERN"` in the scan loop only; `P1_PATTERN`/`P2_PATTERN` definitions are byte-identical. |

The definition site carries the documented rationale comment: breaker observation (not an audit write), void return, receiver-exact, P1 form not exempted, dual output format, and an explicit "do not loosen to a substring filter" warning (a substring `grep -v 'c.breaker.Record('` zeroes `p1_anchor.go` and reddens the gate — FM-3).

### 2.4 Mechanical generated-stub exclusion (REQ-G2-3, resolving constraint (b))

The scan-loop grep carries **both** exclusions:

```bash
found="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$root" --include='*.go' --exclude='*_test.go' --exclude='*.pb.go' 2>/dev/null | grep -vE "$BREAKER_EXEMPT_PATTERN")"
```

- `--exclude='*_test.go'` — unchanged (existing R-M scan-scope amendment).
- `--exclude='*.pb.go'` — **dot glob**, the only form that matches this repo's committed stub names (F4). The underscore form is documented in the script comment as the vacuous no-op to avoid; the `pb_exclude/generated.pb.go` liveness fixture (2.6) reddens the gate if the glob ever regresses to `*_pb.go` (FM-5).
- **Naming note for future protobuf additions:** any new generated stub must keep the dot-form `*.pb.go` suffix; a stub named `*_pb.go` (underscore) would silently fall back inside the scan. The liveness fixture cannot detect this naming drift, so the script comment records it as a convention alongside the `check-proto-sync` drift pin.

### 2.5 Unconditional membership tripwires for the ten new roots (REQ-G2-4)

Mirroring the existing `internal/comment` / quarantine-root loops exactly (no `$# -gt 0` gate — Option B requires the assertion to fire for no-arg **and** explicit-root invocations):

```bash
for root in connector outbox event hub region sso idempotency middleware config handler; do
  root_has=false
  for r in "${ROOTS[@]}"; do
    if [ "$r" = "internal/$root" ]; then root_has=true; fi
  done
  if [ "$root_has" = false ]; then
    echo "SELF-TEST FAIL: internal/$root missing from scan ROOTS (tripwire root removed)"
    fail=1
  fi
done
```

Dropping any of the ten from the Makefile line **or** the no-arg default is red in both invocation modes (FM-6).

### 2.6 New self-tests and pins (all run on every invocation; `fail=1` on any failure)

| ID | Self-test | Failure mode caught |
|---|---|---|
| ST-G2-1 | Membership loop over the ten new roots (2.5) | FM-6 root dropped |
| ST-G2-2 | Makefile recipe-line pin: `grep -qE '^[[:space:]]*@bash scripts/check-audit-swallow\.sh internal/service … internal/config internal/handler[[:space:]]*$' Makefile` — anchored on the **recipe line**, not the target-name line | FM-7 shrunk Makefile line |
| ST-G2-3 | Exemption liveness: raw grep of `breaker_exempt/breaker_ok.go` ≥1 AND filtered pipeline (same grep `\| grep -vE "$BREAKER_EXEMPT_PATTERN"`) == 0; prints residual lines on failure | FM-4 filter removed from scan loop |
| ST-G2-4 | No-hiding + narrowness: for each of `audit_red.go`, `near_miss.go`, `p1_anchor.go` — file must exist (fixture-mode totality) and filtered pipeline ≥1 | FM-1 real swallow hidden; FM-2 receiver loosening; FM-3 substring loosening |
| ST-G2-5 | pb exclusion liveness: raw grep of `$FIXTURES/pb_exclude` ≥1; grep with `--include='*.go' --exclude='*.pb.go'` == 0 | FM-5 vacuous `*_pb.go` glob |
| ST-G2-6 | Text-presence pins (line-anchored, ADR-012 style): `^BREAKER_EXEMPT_PATTERN=` definition present; `grep -qF -- "--exclude='*.pb.go'"` present; the `grep -vE "$BREAKER_EXEMPT_PATTERN"` application present in the scan loop; the extended membership loop header line present | FM-4 definition deleted / scan-loop filter dropped |

### 2.7 New fixture files (never compiled, never root-scanned; text-scanned only by self-tests)

```
scripts/testdata/audit-swallow/breaker_exempt/breaker_ok.go
  package fixture
  // bare breaker observations — exempted shape
  func breakerObservations() { c.breaker.Record(false); c.breaker.Record(true) }
scripts/testdata/audit-swallow/breaker_exempt/audit_red.go
  func auditWrite() { s.audit.Log(ctx, event); s.audit.Record(ctx, f) }        // must stay red
scripts/testdata/audit-swallow/breaker_exempt/near_miss.go
  func nearMisses() { d.breaker.Record(false); c.breaker2.Record(true) }       // must stay red
scripts/testdata/audit-swallow/breaker_exempt/p1_anchor.go
  func anchorPin() { _ = c.breaker.Record(false) }                             // must stay red
scripts/testdata/audit-swallow/pb_exclude/generated.pb.go
  package generated
  func generatedStub() { stub.Record(ctx, f) }                                 // excluded by *.pb.go glob
```

The `near_miss.go` and `p1_anchor.go` lines are intentionally uncompilable/unreferenced Go — same convention as the existing `positive/` fixtures. `generated.pb.go`'s **filename** is the test: the dot glob must exclude it while the raw patterns match it.

---

## 3. Compatibility constraints

1. **Byte-identical preservation where possible (constraint (e)):** P1/P2 pattern definitions, FM-12 guards + guard-PASS counter (4/4), adjacent-path guard, FM-16 standing pin, existing fixture self-tests (positive/cmd/comment/dead-tree/negative/adjacent), T-5 assertions, and the quarantine Makefile tripwire roots are **not edited**. The only functional change to scanning is the added `--exclude='*.pb.go'` and the post-filter.
2. **No Go/protobuf changes:** `internal/sso/client.go` keeps its three breaker calls as-is; `make check-proto-sync` must stay green untouched; `make check-architecture` boundaries untouched (shell script + Makefile + testdata only).
3. **Option B equivalence (constraint (c)):** no-arg default == Makefile recipe line byte-for-byte; membership tripwires unconditional in both invocation modes.
4. **Fail-closed audit contract:** the extension *narrows* no existing scan coverage; it only adds coverage plus a receiver-exact line filter for a documented non-audit call shape. The documented residual (deliberate rename evasion, e.g. an audit write spelled `c.breaker.Record(`) is the same accepted review-level hazard class the script already documents; all canonical audit receivers (`s.audit`, `s.publications`, `s.repo`, `publisher`) remain fully scanned.
5. **Script size:** the gate script grows (401 → ~528 lines dry-run); exempt from `check-file-sizes` (non-Go). Keep new self-tests in the existing compact style; split the script only if it exceeds ~550 lines.
6. **Fixtures are gate parts:** missing/dropped fixture files or dirs redden the gate (fixture-mode totality), consistent with existing convention.

---

## 4. Failure modes and gate reactions

| FM | Attack / regression | Gate reaction | Mechanism |
|---|---|---|---|
| FM-1 | Real swallowed/bare audit write added in any of the ten new dirs (`_ = s.audit.Log(` / `s.audit.Record(`) | **Red** | P1/P2 match; exemption is receiver-exact and cannot match `s.audit.*`; ST-G2-4 keeps `audit_red.go` red |
| FM-2 | Breaker observation elsewhere (`d.breaker.Record(`, `c.breaker2.Record(`) | **Red** | `near_miss.go` self-test (ST-G2-4) |
| FM-3 | Exemption loosened to substring filter (`grep -v 'c.breaker.Record('`) | **Red** | `p1_anchor.go` zeroed → ST-G2-4 |
| FM-4 | Exemption filter removed from scan loop / definition line deleted | **Red** | ST-G2-3 (residual lines) + ST-G2-6 (text pins, undefined-variable semantics) |
| FM-5 | `*.pb.go` glob regressed to vacuous `*_pb.go` | **Red** | `pb_exclude` liveness (ST-G2-5) |
| FM-6 | New root dropped from Makefile line or no-arg default | **Red** | ST-G2-1 unconditional membership tripwire (both invocation modes) |
| FM-7 | Makefile recipe line shrunk (script default intact) | **Red** | ST-G2-2 recipe-line pin |
| FM-8 | G2 fixture dir/file deleted | **Red** | fixture-mode totality in ST-G2-3/4/5 |
| FM-9 | Quarantine root reintroduced (any of the seven) | **Red** | FM-12 guards + adjacent-path guard unchanged, still total |
| FM-10 | Legitimate breaker-shaped line added anywhere in the ten dirs | **Green** | exemption's intended use; filter only exempts `c.breaker.Record(` lines |
| FM-11 | P1 `_ = c.breaker.Record(` added (future, if `Record` gains a return) | **Red** | P1 not exempted (ST-G2-4 `p1_anchor.go`) |
| FM-12 | `_test.go` shapes / `*.pb.go` generated shapes | **Excluded** | `--exclude='*_test.go'` unchanged + `--exclude='*.pb.go'`; stubs remain pinned by `check-proto-sync` |

---

## 5. Migration steps (implementation order)

1. **Edit `Makefile`** — replace the recipe line (77-78) with the 21-root list (§2.2).
2. **Edit `scripts/check-audit-swallow.sh`:**
   a. Update the usage header: new root enumeration (both spots), the sso paragraph pointing at the implemented `BREAKER_EXEMPT_PATTERN` filter, and a sentence on the `*.pb.go` mechanical exclusion (dot form; underscore form documented as vacuous no-op).
   b. Replace the no-arg default `ROOTS=(…)` with the 21-root list (G2 comment: ADR-012 Option B).
   c. Insert `BREAKER_EXEMPT_PATTERN` definition immediately after `P2_PATTERN`, with the rationale comment (§2.3).
   d. Change the scan loop `found=` line to add `--exclude='*.pb.go'` and `| grep -vE "$BREAKER_EXEMPT_PATTERN"` (§2.4). This is the **only** functional change to scanning.
   e. Add self-tests ST-G2-1 … ST-G2-6 before the T-5 block (order-insensitive; keep existing blocks untouched).
3. **Add the five fixture files** (§2.7) under `scripts/testdata/audit-swallow/`.
4. **Do not touch:** `internal/`, `api/`, `cmd/`, `AGENTS.md`, existing fixtures, P1/P2 definitions, FM-12/adjacent/FM-16/T-5 blocks.
5. Run the acceptance mapping (§6); restore the tree after tamper checks.

---

## 6. Testable acceptance mapping

| # | Acceptance check | Command | Expected |
|---|---|---|---|
| A1 | Gate green via Makefile | `make check-audit-swallow; echo $?` | exit 0; final line `✓ check-audit-swallow: no swallowed or bare-call audit writes under internal/service … internal/handler (self-test ok)` |
| A2 | No-arg green + byte-equal surface | `bash scripts/check-audit-swallow.sh`; extract no-arg default vs Makefile recipe and `diff` | exit 0; diff empty (21 roots, same order) |
| A3 | Extended baseline is 0 | `bash scripts/check-audit-swallow.sh` | 0 matches; the three `c.breaker.Record(` lines not violations; 10 `*.pb.go` stubs not scanned |
| A4 | Mandatory no-hiding regression | filtered pipeline over `audit_red.go` vs `breaker_ok.go` | ≥1 vs 0; also `rm -rf scripts/testdata/audit-swallow/breaker_exempt && bash scripts/check-audit-swallow.sh` → exit 1 (then `git checkout`) |
| A5 | Root-removal tripwire | invoke with 20 roots omitting `internal/handler` | exit 1, `SELF-TEST FAIL: internal/handler missing from scan ROOTS` |
| A6 | Makefile shrink pin | `sed -i 's/ internal\/handler$//' Makefile` then no-arg run | exit 1, `SELF-TEST FAIL: Makefile check-audit-swallow recipe line missing or does not carry the full extended root list`; restore |
| A7 | Exemption tamper | (a) drop `grep -vE` from scan loop; (b) delete definition line; (c) loosen to substring | each exit 1 (breaker_ok residual / text pin / p1_anchor zeroed); restore after each |
| A8 | Glob regression | replace `*.pb.go` with `*_pb.go` | exit 1 (`pb_exclude` liveness); restore |
| A9 | Per-root coverage (green ⇒ scanned) | for each of the ten dirs, add `internal/<d>/zz_probe.go` with `\ts.audit.Log(ctx, event)` | exit 1 naming the probe; with `\tc.breaker.Record(false)` → exit 0 |
| A10 | Quarantine totality unchanged | `mkdir -p internal/block` then run | exit 1, `QUARANTINE VIOLATION: internal/block must not exist (FM-12)`; remove dir |
| A11 | Syntax + untouched invariants | `bash -n scripts/check-audit-swallow.sh`; `git status --short`; `git diff` | clean; only Makefile + script + five fixtures changed; no `internal/`, `api/`, `cmd/`, `AGENTS.md`, or existing fixture/tripwire changes |
| A12 | Full done-gate | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-composition-root && make check-proto-sync && make check-integration-compile && make build` + `make check-architecture` | all green (Go tree untouched → Go gates regression-only) |
| A13 | Script size hygiene | `wc -l scripts/check-audit-swallow.sh` | ≤ ~550 lines (non-Go, exempt from `check-file-sizes`); new self-tests in existing compact style |

---

## 7. Documented residuals (accepted hazard classes, unchanged by this design)

1. **Deliberate rename evasion** — an audit write whose receiver chain is literally spelled `c.breaker` would evade the filter. This is the same review-level residual the script already documents for near-variant quarantine names; the exemption cannot hide any canonical audit receiver (`s.audit`, `s.publications`, `s.repo`, `publisher`). Text-presence pins detect accidental edits; adversarial global rewrites of the whole script are outside the gate's threat model (shared with existing pins).
2. **Future stub naming drift** — a newly committed generated stub named `*_pb.go` (underscore) would not match the dot glob. Convention documented in the script comment; `check-proto-sync` continues to pin committed stubs to the proto sources.
3. **`Publish` bare calls** — unchanged from the current gate (P2 deliberately excludes bare `Publish` for the best-effort SSE hub; P1's `_ =` form still catches swallowed publishes).
