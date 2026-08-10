# Design — Quarantine `internal/{file,message,notification,audit}` dead trees with FM-12 tripwires (B3-1)

Date: 2026-08-10 · Module: `cmd/audit-replay` (composition-root context) · Direction: B3-1
Source analysis: `docs/auto/analyses/cmd-audit-replay-bb2403a6.json`
Status: **Verified with corrections** (C1 material, C2 minor, C3+C6 spec defects, C4 environment, C5 CI gap — closed by D8/§3.8; reviewer-validated literal-invocation/A3/empty-array forms folded in)

## 0. Evidence audit (verified against HEAD `e791aa2`)

| # | Claim | Verdict |
|---|---|---|
| E1 | File counts 12/9/11/2 | ✅ exact (`ls` counts match, incl. integration-tagged tests in `file` and `notification`) |
| E2 | Zero importers outside each tree | ✅ repo-wide `grep -rl "aero-id/internal/\(file\|message\|notification\|audit\)" --include="*.go"` matches only the trees' own files; `.claude/worktrees/` copies contain **0** matches today |
| E3 | `internal/app/new.go` + `go list -deps ./cmd/...` clean | ✅ 0 occurrences of the four packages in `go list -deps ./cmd/...`; `new.go` wires `repository.NewFileRepository`/`NewMessageRepository` (live layer) |
| E4 | `internal/domain/audit.go:18` inside QUARANTINED comment; swallow sites `service_share.go:45,75`, `message/service.go:289` | ✅ exact; all three `_ = s.audit.Log(` sites confirmed |
| E5 | `check-architecture.sh:15` `FEATURES=(file message block notification)`; `:6` header; `:22` `ALLOWED_SHARED` audit; `:37` `notification:block` | ✅ all exact |
| E6 | Exclusion comment lines 33–34 | ✅ (`check-audit-swallow.sh` lines 33–41 exclusion block) |
| E7 | Makefile 76–78, roots line 78 | ✅ exact: `internal/service internal/repository internal/app internal/storage internal/block internal/comment cmd` |
| E8 | AGENTS.md:334 gotcha sentence | ✅ (bullet 333–335; adjacent bullet 337–338 also confirmed stale) |
| E9 | FM-12 guards at 86–122 | ✅ block 86–97, comment 99–113, search 115–122 (pattern matches evidence) |
| E10 | Comment membership tripwire 152–166 | ✅ 157–166, gated on `$# -gt 0`, no-arg default exempt |
| E11 | Fixture layout | ✅ `positive/`, `negative/`, `comment_positive/internal/comment/fixture.go`, `cmd_positive/cmd/fixture.go`; all under `testdata` (go tooling ignores `testdata` dirs — that is what makes the fixtures build-safe) |
| E12 | No non-Go references | ✅ `deployments/`, `configs/`, `.golangci.yml`, Makefile clean of the four paths |
| — | Scan loop tolerates absent roots | ✅ `grep ... 2>/dev/null` at the ROOTS scan (line ~181) suppresses "No such file" errors; absent roots are safe by construction (existing block/comment precedent) |

### Corrections to the evidence

- **C1 (material, factual)** — The evidence's "Additional verified facts" and the Non-goals claim that "`domain.AuditLogger`/`domain.AuditEvent` … are referenced only by the dead trees" and that "`AuditEvent` becomes dead after the deletion" are **false for `AuditEvent`**. `domain.AuditEvent` is **live**: used by `internal/repository/audit.go` (auditRepo `Log`/`Append`/`List`), `internal/repository/db.go` (`auditEventDomainToRow`), `internal/repository/admin.go`, `internal/service/admin.go` (`ListAudit`, `auditEventFromFact`), `internal/service/service.go`, `internal/service/message.go`, mocks, and multiple tests (47 refs outside the dead trees). Only `domain.AuditLogger` (the interface) is dead-tree-only (`internal/file/file.go`, `internal/message/message.go`).
  **Impact**: REQ-5.4's comment rewrite must state *AuditLogger* is dead and *AuditEvent* remains live; the "future direction: prune the dead `AuditLogger` interface **and** `AuditEvent` struct" note must be corrected to name only `AuditLogger`. Deleting `AuditEvent` would break the live ledger/admin read paths.
- **C2 (minor)** — `cmd/audit-replay` also imports `internal/service` (main.go) and `internal/service` + `internal/repository/testpg` (tests); "imports only five packages" is imprecise. Non-load-bearing: the claim that matters ("imports none of the four quarantined trees", P1/P2 `Requeue*`/`MarkDelivery` pinning) is verified.
- **C3 (spec defect, self-defeating)** — REQ-3.4 self-test A greps `$0` for `QUARANTINE VIOLATION: internal/$d must not exist` **per directory**. But REQ-3.1 defines a single parameterized guard whose message text is `QUARANTINE VIOLATION: internal/$name must not exist` — the literal per-dir sentence never appears in the file, so self-test A would *always* fail and the gate would be permanently red as specified. **Fix (adopted below)**: split A into (A1) one template grep for `QUARANTINE VIOLATION: internal/\$name must not exist` (proves the message exists) plus (A2) per-dir greps for the invocation `quarantine_guard "\$SELF_DIR/../internal/$d"` (proves each guard call exists; in the loop-form draft, deleting the `QUARANTINED` array entry also removed the invocation and was caught transitively — superseded by C6/A3 once invocations became literal). Tripwire semantics are preserved: deleting a guard call, the message, or the array entry turns the gate red.
- **C4 (environment)** — Guard/empty-array behavior verified on bash 5.2.21: `"${FEATURES[@]}"` with an empty array under `set -euo pipefail` is safe (bash ≥ 4.4). **Correction**: the `FEATURES=()`/`KNOWN_EXCEPTIONS=()` empty-array idiom `${arr[@]+"${arr[@]}"}` is **mandatory, not conditional** — verified on bash 3.2.0 (and macOS 3.2.57): a plain `"${FEATURES[@]}"` dies with `unbound variable` under `set -u`. Adopted in §3.5; no-op on bash ≥ 4.4 (ubuntu-latest CI runners unaffected either way).
- **C5 (material, CI gap)** — Zero `.github/workflows/*.yml` invoke `check-audit-swallow.sh` or `check-architecture.sh` (verified by grep across ci/docker/release workflows). The FM-12 tripwires therefore rest solely on the local done-gate: a PR reintroducing any of the four trees merges green — material to the campaign premise. **Fix**: wire both gates into `.github/workflows/ci.yml` as a dedicated pure-bash job `check-gates` (D8, §3.8), verified empirically in a throwaway worktree.
- **C6 (spec defect, residual of C3)** — Two independent reviewers confirmed the C3-fixed §3.2 is still self-defeating as written: §3.2.3's parameterized loop (`quarantine_guard "$SELF_DIR/../internal/$d" "$d"`) never contains the literal per-dir text that §3.2.4's A2 grep requires, so the gate is permanently red (`SELF-TEST FAIL` ×4 on a clean tree). **Fix (verified)**: four literal invocation lines (§3.2.3) plus an end-anchored A3 array pin (§3.2.4) that restores FM-5's red property, which literal invocations alone would lose.

## 1. Problem statement (confirmed)

Four unwired trees (34 files) remain after the 2026-08-08 block/comment/search quarantine. They are provably unreachable (zero importers, absent from `go list -deps ./cmd/...`, unwired in `internal/app/new.go`) yet they (1) keep AGENTS.md's "unwired parallel tree" gotcha alive, (2) make `check-architecture` police dead FEATURES, (3) remain the sole audit-gate blind spot: `check-audit-swallow.sh:33–34` explicitly excludes them, and they still hold three `_ = s.audit.Log(` swallow sites that the gate refuses to scan, and (4) **no CI workflow invokes either gate** (C5): a PR reintroducing any of the four trees merges green, so the quarantine tripwires rest solely on the local done-gate until D8 wires them into `.github/workflows/ci.yml`.

## 2. Design decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | Delete the four trees with **no shims** (no replacement packages, forwarding files, or empty dirs) | They are unreachable; a shim would itself trip the REQ-3 guards. Precedent: 2026-08-08 quarantine |
| D2 | Parameterized `quarantine_guard <path> <name>` for the **four new** paths only; the existing block/comment/search inline guards remain **byte-identical** | REQ-6.1 gate integrity: self-test greps and pinned texts of the existing guards must not move. A follow-up may absorb all seven into the function |
| D3 | One atomic commit: deletion + guards + fixtures + roots + **CI wiring** + comment updates | A quarantine that lands without its tripwires — locally or in CI — recreates the FM-12 gap it closes (block/comment precedent, audit.go note line 90) |
| D4 | Self-test A split into A1 (message template) + A2 (per-dir invocation), per C3; A3 (end-anchored array pin) added per C6 | Makes REQ-3 satisfiable while preserving every tripwire property — A3 restores FM-5's red property under literal invocations |
| D5 | `FEATURES=()` with an explanatory header comment; `internal/domain` self-containment rule keeps running | REQ-2 scope: dropping dead entries only; live-flow rules are direction-3 territory. Empty-FEATURES "gate removed" optics are mitigated by the comment and by the fact that check-audit-swallow's dir guards are now the *total* reintroduction tripwire for all four paths |
| D6 | Fixtures under `scripts/testdata/audit-swallow/quarantine_positive/internal/{file,message,notification,audit}/fixture.go` | `testdata` is ignored by go tooling (build-safe precedent); `scripts/` is never a scan ROOT so P1/P2 match count stays 0; `fixture.go` exists so git tracks the dirs (git does not track empty dirs) |
| D7 | Append the four roots to the Makefile:78 invocation; membership tripwire (REQ-4.1) validates them when `$# -gt 0` | Absent-path roots scan to 0 matches (verified `2>/dev/null` in scan loop); dropping a root from Makefile is then a gate violation — same property as `internal/comment` |
| D8 | Wire both gates into `.github/workflows/ci.yml` as **one dedicated job `check-gates`** (C5 closure): `make check-audit-swallow` + `make check-architecture`, no paths filter, no `setup-go` | The campaign premise is a PR that merges green; a tripwire enforced only by the local done-gate is not a distributed gate. Precedent: `check-proto-sync` runs as its own job. Pure bash + grep → sub-second runtime, no Go/Docker/network → zero interference with the Go jobs. The **Makefile invocation is load-bearing**: the four quarantine roots and the membership tripwires live in the Makefile gate line, so CI enforces the exact done-gate surface (FM-6/AC-3). No paths filter: a filter would recreate the blind spot (same rationale as the `check-proto-sync` job comment). Triggers: inherited `push`/`pull_request` on main — direct pushes cannot bypass |

## 3. Concrete change set

### 3.1 `git rm -r internal/file internal/message internal/notification internal/audit` (34 files)

Post-condition: `grep -rl "aero-id/internal/\(file\|message\|notification\|audit\)" --include="*.go" .` → no output (verified satisfiable today; `.claude/worktrees/` already has 0 matches). `go list ./internal/file/...` etc. → "no Go files"/package not found.

### 3.2 `scripts/check-audit-swallow.sh`

1. **Usage header (lines 63–66)** — extend the documented root list with the four new tripwire roots (evidence omitted this; stale docs would contradict the Makefile gate — DA-1).
2. **Exclusion block (33–41)** — rewrite to past tense: the four trees were deleted 2026-08-10; they are no longer excluded surfaces but absent-path tripwire roots under the FM-12 guards, so a reintroduced swallowed write there turns the gate red. Adjust the dead-type note (~51–52: "AGENTS.md's 'AuditLogger … legacy diagnostics only' carve-out refers to the dead-tree internal/audit type…") to a past-tense historical reference.
3. **Guard function + invocations** — after the existing search guard (line ~124), before the positive-fixture self-test:
   ```bash
   # ── quarantine guard (FM-12): internal/{file,message,notification,audit} ──
   # must never exist. Deleted in the 2026-08-10 dead-tree quarantine; the
   # four trees carried the last excluded swallow sites (three `_ = s.audit.Log(`
   # at internal/file/service_share.go:45,75 and internal/message/service.go:289).
   # The guard is total — even a benign reintroduction is a gate violation.
   # Deliberately lifting the quarantine = deleting the QUARANTINED entry AND
   # the guard calls AND self-tests A1/A2/A3/B AND the Makefile tripwire roots
   # AND the audit.go note AND the exclusion comment + AGENTS.md, in one commit.
   QUARANTINED=("file" "message" "notification" "audit")
   quarantine_guard() {  # usage: quarantine_guard <path> <name>
     local path="$1" name="$2"
     if [ -d "$path" ]; then
       echo "QUARANTINE VIOLATION: internal/$name must not exist (dead-tree quarantine, FM-12)"
       fail=1
     else
       echo "✓ quarantine guard: internal/$name absent (FM-12)"
     fi
   }
   # Four LITERAL invocations (C6 fix, verified): the A2 self-test greps for
   # the literal per-dir text `quarantine_guard "$SELF_DIR/../internal/<d>"`;
   # a `for d in "${QUARANTINED[@]}"` loop line contains only `$d` and would
   # make the gate permanently red. Names are [a-z]+ (A2 interpolates them
   # into a grep pattern).
   quarantine_guard "$SELF_DIR/../internal/file" "file"
   quarantine_guard "$SELF_DIR/../internal/message" "message"
   quarantine_guard "$SELF_DIR/../internal/notification" "notification"
   quarantine_guard "$SELF_DIR/../internal/audit" "audit"
   ```
4. **Guard self-test A** (C3-fixed):
   ```bash
   for d in "${QUARANTINED[@]}"; do
     grep -q "quarantine_guard \"\$SELF_DIR/../internal/$d\"" "$0" \
       || { echo "SELF-TEST FAIL: quarantine guard for internal/$d missing"; fail=1; }
   done
   grep -q 'QUARANTINE VIOLATION: internal/\$name must not exist' "$0" \
     || { echo "SELF-TEST FAIL: quarantine guard message template missing"; fail=1; }
   # A3 (C6 fix, verified): pins the QUARANTINED array itself. Literal
   # invocations are not array-driven, so A2 alone cannot catch an entry
   # deletion. The `$` end-anchor is load-bearing: an unanchored hardcoded
   # pin matches its own grep source line (vacuous green, empirically
   # proven); the anchored form matches only the array line.
   grep -q 'QUARANTINED=("file" "message" "notification" "audit")$' "$0" \
     || { echo "SELF-TEST FAIL: QUARANTINED array must list file message notification audit"; fail=1; }
   ```
5. **Guard self-test B** (executable trip proof against stub dirs):
   ```bash
   for d in "${QUARANTINED[@]}"; do
     if ! ( fail=0; quarantine_guard "$FIXTURES/quarantine_positive/internal/$d" "$d" >/dev/null; [ "$fail" -ne 0 ] ); then
       echo "SELF-TEST FAIL: stub dir for internal/$d did not trip the quarantine guard"
       fail=1
     fi
   done
   ```
   (Verified executable under `set -u`: absent path → no trip → subshell returns 1 → `!` → fail; present stub → trip → pass. The `>/dev/null` silences the expected-violation echo so green runs (and CI logs) stay clean; semantics unchanged.)
6. **Membership tripwire** (mirror comment check at 157–166), inside the existing `if [ "$#" -gt 0 ]` block:
   ```bash
   for d in "${QUARANTINED[@]}"; do
     root_has=false
     for root in "${ROOTS[@]}"; do
       [ "$root" = "internal/$d" ] && root_has=true
     done
     if [ "$root_has" = false ]; then
       echo "SELF-TEST FAIL: internal/$d missing from scan ROOTS (tripwire root removed)"
       fail=1
     fi
   done
   ```
   No-arg default (`internal/service` only) stays exempt, exactly as for `internal/comment`.
7. **Untouched**: P1/P2 patterns, T-5 assertions, positive/negative/comment_positive/cmd_positive self-tests, block/comment/search guards, final pass message.

### 3.3 `scripts/testdata/audit-swallow/quarantine_positive/internal/{file,message,notification,audit}/fixture.go`

Each: `package <name>` (one line, matching the `comment_positive` precedent). Lives under `testdata` → ignored by go tooling; never scanned (not a ROOT); zero P1/P2 matches.

### 3.4 `Makefile` (line 78)

```make
check-audit-swallow:
	@bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage internal/block internal/comment cmd internal/file internal/message internal/notification internal/audit
```

### 3.5 `scripts/check-architecture.sh`

1. Line 6 header: drop `, internal/audit` from the allowed-import list.
2. Lines 10–12: extend the deletion note:
   ```bash
   # file, message, notification, block and audit were deleted in the
   # 2026-08-10 dead-tree quarantine; the FEATURES list is intentionally empty.
   # Reintroducing a feature package requires re-adding it here (and the
   # check-audit-swallow FM-12 guards will trip first).
   # The ${arr[@]+...} idiom is MANDATORY (C4, verified on bash 3.2/5.2): a
   # plain "${FEATURES[@]}" on an empty array dies with 'unbound variable'
   # under set -u on bash 3.2/macOS; it is a no-op on bash >= 4.4 (ubuntu CI).
   FEATURES=()
   ```
3. Line 22: remove `"$INTERNAL/audit"` from `ALLOWED_SHARED` (zero importers exist; allowance referred only to the deleted package).
4. Lines 36–38: remove the `"notification:block:…"` KNOWN_EXCEPTIONS entry (both packages deleted). `KNOWN_EXCEPTIONS=()` empty array plus the `${arr[@]+...}` loop idiom (see step 2) is safe on bash 3.2 and ≥ 4.4 — verified.
5. Loop behavior: `for feat in ${FEATURES[@]+"${FEATURES[@]}"}` no-ops on the empty array (idiom per step 2; same for the KNOWN_EXCEPTIONS loop); `internal/domain` self-containment rule still runs; "✓ Architecture check passed" exit path unchanged.

### 3.6 `AGENTS.md`

- **Bullet 333–335 (gotcha)**: rewrite to record the completed quarantine — the four trees were deleted 2026-08-10; absence pinned by `check-audit-swallow` FM-12 guards + Makefile tripwire roots, enforced locally by the done-gate and in CI by the `check-gates` job (`.github/workflows/ci.yml`); `check-architecture` FEATURES no longer enumerates them. **Preserve verbatim** the operative guidance: "The running application uses `internal/service` + `internal/repository`. Do not implement target account/audit work in an unwired tree."
- **Bullet 337–338**: rewrite to: the FEATURES list is intentionally empty after the 2026-08-10 quarantine, so a green `check-architecture` result does not prove runtime wiring (warning value retained).
- Line 266 (dead-tree AuditLogger carve-out) and the platform-contract bullets: **untouched**.

### 3.7 `internal/domain/audit.go` (comment lines 15–21 only — C1-corrected text)

```
// QUARANTINED DEAD TREE — deleted 2026-08-10: internal/{file,message,notification,audit}
// were removed in the dead-tree quarantine; their three `_ = s.audit.Log(` swallow
// sites (internal/file/service_share.go:45,75; internal/message/service.go:289) no
// longer exist, and their absence is pinned by the check-audit-swallow FM-12 guards
// + Makefile tripwire roots. This AuditLogger interface is now dead (zero references)
// and may be pruned in a future change; AuditEvent below remains LIVE (used by
// internal/repository/audit.go and internal/service/admin.go). The live layer uses
// service.AuditLogger and is policed by make check-audit-swallow.
```

Retain the fail-closed contract text (lines 12–14) verbatim. **Do not touch** `AuditLogger`/`AuditEvent`/`AuditFact` types or the action constants (C1: `AuditEvent` is live).

### 3.8 `.github/workflows/ci.yml` — new job `check-gates` (C5 closure, D8)

Append one dedicated job after `check-proto-sync`; all existing jobs byte-identical (verified: the ci.yml diff has 0 removed lines):

```yaml
  check-gates:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Quarantine, audit-swallow and architecture gates
        run: |
          make check-audit-swallow
          make check-architecture
```

- **Which targets/events**: inherited `push: branches [main]` + `pull_request: branches [main]` — no new triggers, no paths filter. A paths filter would recreate the blind spot this gate closes (a tree-reintroducing or guard-weakening PR must fail CI regardless of which paths it touches — same rationale as the `check-proto-sync` job comment, which documents why it too has no filter). Push coverage matters: direct pushes / squash merges to main cannot bypass the quarantine.
- **Dedicated job, not an extension of `test`/`vet`**: precedent `check-proto-sync` (gates run as their own jobs). Pure bash + grep means no `setup-go`, so the job is independent of the Go toolchain/cache and cannot interfere with the Go jobs; failure attribution stays clean. Both scripts share one job (same characteristics; two jobs would double runner overhead for no isolation gain).
- **The Makefile invocation is load-bearing**: the four quarantine tripwire roots and the membership tripwires live in the Makefile gate line, so CI runs the exact done-gate surface — FM-6 (root removal) turns CI red, not just local. The script's no-arg default stays a documented single-service shortcut.
- **Why not docker.yml / release.yml**: ci.yml already triggers on the same push/PR events and is the enforcement workflow; docker/release exist to build/push artifacts. No need to duplicate the gates.
- **Runner**: `ubuntu-latest` (bash 5.x, GNU grep with `--include`) matches the other jobs; the `ubuntu-24.04` pin exists only for `check-proto-sync`'s protoc 3.21.12.

## 4. API changes and compatibility constraints

**Public/shipped API: zero changes.** The four trees are unreachable (zero importers, 0 in `go list -deps ./cmd/...`), so their deletion breaks no compile, wire, migration, or config surface. Explicit constraints:

| Surface | Constraint |
|---|---|
| Handlers, services, repositories, app wiring, storage, outbox, hub, sso, region, event, idempotency, connector | **Untouched** — no byte changes |
| `cmd/audit-replay` | Untouched; `Requeue*`/`MarkDelivery` P1/P2 pinning intact; `cmd` root stays in the Makefile gate |
| `.github/workflows/ci.yml` | Adds job `check-gates` (two `make` lines, pure bash — §3.8); existing jobs, triggers, env, and `check-proto-sync` byte-identical (verified 0 removed lines in the diff) |
| Protobuf, migrations, config | Untouched; no SQL/storage/connector change → `make test-integration` not required |
| Gate baseline | Stays green at **0 P1/P2 matches**; no new exclusions; P1/P2 patterns, T-5, and existing self-tests unchanged |
| `domain.AuditLogger` | Becomes fully dead (interface, zero refs) — harmless to the compiler; prune is a follow-up, **not** part of this change (C1: do not prune `AuditEvent`) |
| `check-architecture` | FEATURES loop no-ops; domain rule still enforced; exit path unchanged |
| Removed integration-tagged tests (`file/repository_integration_test.go`, `notification/repository_integration_test.go`) | No live code references them → `check-integration-compile` stays green |
| `docs/designs/` historical records, AGENTS.md:266 | Untouched (history/normative text remains true) |

## 5. Failure modes and tripwire matrix

| # | Failure | Detection | Consequence |
|---|---|---|---|
| FM-1 | Reintroduce `internal/file` (benign or swallow-bearing) | Guard `[ -d ]` → `QUARANTINE VIOLATION: internal/file must not exist` | Gate red (exit 1) |
| FM-2 | Delete one guard invocation | Self-test A2 grep fails | Red: `SELF-TEST FAIL: quarantine guard for internal/<d> missing` |
| FM-3 | Reword/delete the violation message | Self-test A1 template grep fails | Red (forces coordinated update — intended) |
| FM-4 | Delete a stub dir | Self-test B execution fails | Red: `SELF-TEST FAIL: stub dir … did not trip` |
| FM-5 | Delete a `QUARANTINED` array entry | A3 end-anchored pin fails (literal invocations are not array-driven, so A2 alone cannot catch it) | Red: `SELF-TEST FAIL: QUARANTINED array must list file message notification audit` (verified with a targeted line edit). Documented residual: a *global* multi-line edit that rewrites both the array line and the A3 pattern text stays green — same coordinated-change class as the deliberate-lift path and FM-3's message rewording |
| FM-6 | Remove a root from Makefile:78 | Membership tripwire (`$# -gt 0`); the CI `check-gates` job runs the Makefile invocation on every push/PR, so removal is caught there too | Red locally and in CI: `SELF-TEST FAIL: internal/<d> missing from scan ROOTS` (verified via `make`, exit 2) |
| FM-7 | Delete the membership tripwire | Not self-pinned (same gap class as comment's tripwire — accepted, mirrors precedent; the guard self-tests pin the guards, not the tripwire itself) | Documented residual |
| FM-8 | FEATURES resurrected / `internal/audit` re-added to ALLOWED_SHARED | check-architecture only polices listed FEATURES; reintroduced package is caught by check-audit-swallow dir guard (total tripwire), which now runs in CI on every push/PR | No blind spot — locally and in CI (verified: `mkdir -p internal/audit` → `make check-audit-swallow` exit 2, `QUARANTINE VIOLATION`) |
| FM-9 | bash < 4.4 empty-array expansion | `set -u` unbound-variable error | Not applicable: §3.5 adopts the `${arr[@]+"${arr[@]}"}` idiom unconditionally — verified green on bash 3.2.0 and 5.2.21 |
| FM-10 | Stale `.claude/worktrees/` copies | Outside gate scope (throwaway checkouts); verified 0 pattern matches today | Accepted, documented |
| FM-11 | Future direction prunes `AuditEvent` "because it's dead" | C1 correction embedded in the audit.go note prevents this | Live ledger/admin breakage avoided |
| FM-12 | Guard text greps become brittle | Self-test A1/A2 match stable sentences that are also the runtime messages; rewording fails A1 and forces coordinated update | Intended property (same as fixture-pattern self-tests) |
| FM-13 | `check-gates` job removed or the guard lines dropped from `.github/workflows/ci.yml` | Nothing self-pins a workflow file (same gap class as FM-7) | Accepted residual, documented: workflow diffs are human-reviewed; AC-6 grep records the expected surface; script-level tripwires and the local done-gate remain |

## 6. Migration steps (one atomic commit)

1. `git rm -r internal/file internal/message internal/notification internal/audit` (34 files).
2. Edit `scripts/check-audit-swallow.sh` (§3.2): usage header, exclusion block, guard fn + invocations, self-tests A/B, membership tripwire.
3. Add 4 stub fixtures under `scripts/testdata/audit-swallow/quarantine_positive/` (§3.3).
4. Edit `Makefile` line 78 (§3.4).
5. Edit `scripts/check-architecture.sh` (§3.5).
6. Edit `AGENTS.md` bullets (§3.6), `internal/domain/audit.go` comment (§3.7), and `.github/workflows/ci.yml` (§3.8 — append the `check-gates` job).
7. Commit message must narrate the quarantine + tripwire landing (block/comment precedent: one commit).

Rollback: `git revert` of the single commit is total — guards, roots, fixtures, comments, and the CI job revert together; no half-quarantined state is possible.

## 7. Testable acceptance mapping

| Requirement | Acceptance (testable) | Verification |
|---|---|---|
| REQ-1 deletion | AC-1 | `make test && make lint && make lint-strict && make check-file-sizes && make build` all exit 0; `grep -rl "aero-id/internal/\(file\|message\|notification\|audit\)" --include="*.go" .` → no output; `go list ./internal/{file,message,notification,audit}/...` → package not found |
| REQ-2 FEATURES retirement | AC-4 | `make check-architecture` exit 0; `grep -n "FEATURES=" scripts/check-architecture.sh` shows `FEATURES=()` + quarantine comment; no `internal/audit` in header/ALLOWED_SHARED; no `notification:block` |
| REQ-3 guards + self-tests | AC-2 | Baseline: gate exit 0 with four "✓ quarantine guard: internal/{file,message,notification,audit} absent (FM-12)" lines and A/B checks passing. Mutation probes (throwaway worktree, restore after): (a) `mkdir -p internal/file` → exit 1 + violation line; (b) delete one guard invocation → exit 1 + A2 message; (c) delete one stub dir → exit 1 + B message; (d) delete the violation-message line → exit 1 + A1 message; (e) delete one `QUARANTINED` array entry (targeted line edit) → exit 1 + A3 message. All mutations also fail the CI job (its first command is `make check-audit-swallow`) — verified in the throwaway worktree |
| REQ-4 roots + tripwire | AC-3 | Remove one of the four roots from Makefile:78 → `make check-audit-swallow` exit 1 + "missing from scan ROOTS"; restore → exit 0; no-arg `bash scripts/check-audit-swallow.sh` stays green and exempt. CI enforcement: the `check-gates` job runs the same Makefile invocation, so the removal also fails CI (verified) |
| REQ-5 comments | AC-5 | `grep -n "unwired parallel tree" AGENTS.md scripts/check-audit-swallow.sh internal/domain/audit.go` → only past-tense historical notes; no present-tense reference to the four paths; `grep -n "internal/{file,message,notification,audit}" scripts/check-audit-swallow.sh` → only the rewritten exclusion block |
| REQ-6 gate integrity | AC-4 | `make check-audit-manifest && make check-composition-root && make check-proto-sync` exit 0; `git diff --stat scripts/check-audit-manifest.sh scripts/check-composition-root.sh scripts/check-proto-sync.sh Makefile` → only Makefile line 78 differs (ci.yml's appended job is intended; all existing CI jobs byte-identical) |
| REQ-7 CI enforcement | AC-6 | `.github/workflows/ci.yml` contains the `check-gates` job whose run block is exactly `make check-audit-swallow` + `make check-architecture` (grep-verifiable); `git diff` of ci.yml shows 0 removed lines (existing jobs byte-identical); the job's exact steps exit 0 on the post-quarantine tree in a throwaway worktree, each in < 1 s, with no `setup-go` step, no network, and no Docker (pure bash + grep) |
| Full gate | — | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-composition-root && make check-proto-sync && make check-integration-compile && make build` all exit 0; the CI job's two commands (`make check-audit-swallow && make check-architecture`) exit 0 on the post-quarantine tree in a throwaway worktree (verified, ~0.04 s total warm) |

`make test-integration` is **not** required: no SQL, migrations, storage, connectors, event ingestion, or callback-delivery logic changes (live surfaces untouched).

## 8. Risks

| Risk | Mitigation |
|---|---|
| Guard + self-test A as originally specified is unsatisfiable (permanent red) | C3+C6 fixes: A1 template grep + A2 per-dir invocation grep + A3 array pin; literal invocations (§3.2.3) — all verified green at baseline, red for every FM-1…FM-6 probe |
| `domain.AuditEvent` wrongly pruned later (evidence's "becomes dead" claim) | C1 correction embedded in the rewritten audit.go note; `AuditEvent` stays live |
| Empty FEATURES reads as "check-architecture removed" | Header comment documents why; domain rule still runs; check-audit-swallow dir guards are the total reintroduction tripwire; direction 3 is the designated successor for live-flow rules |
| Deletion breaks `check-integration-compile` | Verified: no live file references the removed integration-tagged tests |
| One stale doc surface missed (usage header at check-audit-swallow:63–66) | DA-1 folds it into REQ-5's consistency sweep |
| Self-test greps brittle to rewording | Intended: rewording fails A1/A2 and forces a coordinated update, same as fixture self-tests |
| CI job removed / guard lines dropped from ci.yml (FM-13) | Accepted residual (workflow files are not self-pinned, same class as FM-7); mitigated by human review of workflow diffs, the AC-6 grep, and the remaining script-level + local done-gate enforcement |
