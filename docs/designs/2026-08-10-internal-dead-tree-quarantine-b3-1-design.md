# Design — Quarantine `internal/{file,message,notification,audit}` dead trees with FM-12 tripwires (B3-1)

Date: 2026-08-10 · Module: `cmd/audit-replay` (composition-root context) · Direction: B3-1
Source analysis: `docs/auto/analyses/cmd-audit-replay-bb2403a6.json`
Status: **Verified with corrections** (C1 material, C2 minor, C3 spec defect, C4 environment note)

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
- **C3 (spec defect, self-defeating)** — REQ-3.4 self-test A greps `$0` for `QUARANTINE VIOLATION: internal/$d must not exist` **per directory**. But REQ-3.1 defines a single parameterized guard whose message text is `QUARANTINE VIOLATION: internal/$name must not exist` — the literal per-dir sentence never appears in the file, so self-test A would *always* fail and the gate would be permanently red as specified. **Fix (adopted below)**: split A into (A1) one template grep for `QUARANTINE VIOLATION: internal/\$name must not exist` (proves the message exists) plus (A2) per-dir greps for the invocation `quarantine_guard "\$SELF_DIR/../internal/$d"` (proves each guard call exists; deleting the `QUARANTINED` array entry also removes the invocation and is therefore caught transitively). Tripwire semantics are preserved: deleting a guard call, the message, or the array entry turns the gate red.
- **C4 (environment)** — Guard/empty-array behavior verified on bash 5.2.21: `"${FEATURES[@]}"` with an empty array under `set -euo pipefail` is safe (bash ≥ 4.4); the parameterized guard + subshell trip proof was executed and behaves as specified. If CI ever pins bash < 4.4, use `${FEATURES[@]+"${FEATURES[@]}"}`; not needed today.

## 1. Problem statement (confirmed)

Four unwired trees (34 files) remain after the 2026-08-08 block/comment/search quarantine. They are provably unreachable (zero importers, absent from `go list -deps ./cmd/...`, unwired in `internal/app/new.go`) yet they (1) keep AGENTS.md's "unwired parallel tree" gotcha alive, (2) make `check-architecture` police dead FEATURES, and (3) remain the sole audit-gate blind spot: `check-audit-swallow.sh:33–34` explicitly excludes them, and they still hold three `_ = s.audit.Log(` swallow sites that the gate refuses to scan.

## 2. Design decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | Delete the four trees with **no shims** (no replacement packages, forwarding files, or empty dirs) | They are unreachable; a shim would itself trip the REQ-3 guards. Precedent: 2026-08-08 quarantine |
| D2 | Parameterized `quarantine_guard <path> <name>` for the **four new** paths only; the existing block/comment/search inline guards remain **byte-identical** | REQ-6.1 gate integrity: self-test greps and pinned texts of the existing guards must not move. A follow-up may absorb all seven into the function |
| D3 | One atomic commit: deletion + guards + fixtures + roots + comment updates | A quarantine that lands without its tripwires recreates the FM-12 gap it closes (block/comment precedent, audit.go note line 90) |
| D4 | Self-test A split into A1 (message template) + A2 (per-dir invocation), per C3 | Makes REQ-3 satisfiable while preserving every tripwire property |
| D5 | `FEATURES=()` with an explanatory header comment; `internal/domain` self-containment rule keeps running | REQ-2 scope: dropping dead entries only; live-flow rules are direction-3 territory. Empty-FEATURES "gate removed" optics are mitigated by the comment and by the fact that check-audit-swallow's dir guards are now the *total* reintroduction tripwire for all four paths |
| D6 | Fixtures under `scripts/testdata/audit-swallow/quarantine_positive/internal/{file,message,notification,audit}/fixture.go` | `testdata` is ignored by go tooling (build-safe precedent); `scripts/` is never a scan ROOT so P1/P2 match count stays 0; `fixture.go` exists so git tracks the dirs (git does not track empty dirs) |
| D7 | Append the four roots to the Makefile:78 invocation; membership tripwire (REQ-4.1) validates them when `$# -gt 0` | Absent-path roots scan to 0 matches (verified `2>/dev/null` in scan loop); dropping a root from Makefile is then a gate violation — same property as `internal/comment` |

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
   # Deliberately lifting the quarantine = deleting the array entry AND the
   # guard call AND self-tests A/B AND the Makefile tripwire roots AND the
   # audit.go note AND the exclusion comment + AGENTS.md, in one commit.
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
   for d in "${QUARANTINED[@]}"; do
     quarantine_guard "$SELF_DIR/../internal/$d" "$d"
   done
   ```
4. **Guard self-test A** (C3-fixed):
   ```bash
   for d in "${QUARANTINED[@]}"; do
     grep -q "quarantine_guard \"\$SELF_DIR/../internal/$d\"" "$0" \
       || { echo "SELF-TEST FAIL: quarantine guard for internal/$d missing"; fail=1; }
   done
   grep -q 'QUARANTINE VIOLATION: internal/\$name must not exist' "$0" \
     || { echo "SELF-TEST FAIL: quarantine guard message template missing"; fail=1; }
   ```
5. **Guard self-test B** (executable trip proof against stub dirs):
   ```bash
   for d in "${QUARANTINED[@]}"; do
     if ! ( fail=0; quarantine_guard "$FIXTURES/quarantine_positive/internal/$d" "$d"; [ "$fail" -ne 0 ] ); then
       echo "SELF-TEST FAIL: stub dir for internal/$d did not trip the quarantine guard"
       fail=1
     fi
   done
   ```
   (Verified executable under `set -u`: absent path → no trip → subshell returns 1 → `!` → fail; present stub → trip → pass.)
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
   FEATURES=()
   ```
3. Line 22: remove `"$INTERNAL/audit"` from `ALLOWED_SHARED` (zero importers exist; allowance referred only to the deleted package).
4. Lines 36–38: remove the `"notification:block:…"` KNOWN_EXCEPTIONS entry (both packages deleted). `KNOWN_EXCEPTIONS=()` empty array is safe under `set -euo pipefail` on bash ≥ 4.4.
5. Loop behavior: `for feat in "${FEATURES[@]}"` no-ops; `internal/domain` self-containment rule still runs; "✓ Architecture check passed" exit path unchanged.

### 3.6 `AGENTS.md`

- **Bullet 333–335 (gotcha)**: rewrite to record the completed quarantine — the four trees were deleted 2026-08-10; absence pinned by `check-audit-swallow` FM-12 guards + Makefile tripwire roots; `check-architecture` FEATURES no longer enumerates them. **Preserve verbatim** the operative guidance: "The running application uses `internal/service` + `internal/repository`. Do not implement target account/audit work in an unwired tree."
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

## 4. API changes and compatibility constraints

**Public/shipped API: zero changes.** The four trees are unreachable (zero importers, 0 in `go list -deps ./cmd/...`), so their deletion breaks no compile, wire, migration, or config surface. Explicit constraints:

| Surface | Constraint |
|---|---|
| Handlers, services, repositories, app wiring, storage, outbox, hub, sso, region, event, idempotency, connector | **Untouched** — no byte changes |
| `cmd/audit-replay` | Untouched; `Requeue*`/`MarkDelivery` P1/P2 pinning intact; `cmd` root stays in the Makefile gate |
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
| FM-5 | Delete a `QUARANTINED` array entry | Loop loses the invocation → A2 fails for that d | Red |
| FM-6 | Remove a root from Makefile:78 | Membership tripwire (`$# -gt 0`) | Red: `SELF-TEST FAIL: internal/<d> missing from scan ROOTS` |
| FM-7 | Delete the membership tripwire | Not self-pinned (same gap class as comment's tripwire — accepted, mirrors precedent; the guard self-tests pin the guards, not the tripwire itself) | Documented residual |
| FM-8 | FEATURES resurrected / `internal/audit` re-added to ALLOWED_SHARED | check-architecture only polices listed FEATURES; reintroduced package is caught by check-audit-swallow dir guard (total tripwire) | No blind spot: the swallow gate is the backstop |
| FM-9 | bash < 4.4 empty-array expansion | `set -u` unbound-variable error | Not applicable (bash 5.2.21 verified; use `${FEATURES[@]+"${FEATURES[@]}"}` idiom if CI pins older bash) |
| FM-10 | Stale `.claude/worktrees/` copies | Outside gate scope (throwaway checkouts); verified 0 pattern matches today | Accepted, documented |
| FM-11 | Future direction prunes `AuditEvent` "because it's dead" | C1 correction embedded in the audit.go note prevents this | Live ledger/admin breakage avoided |
| FM-12 | Guard text greps become brittle | Self-test A1/A2 match stable sentences that are also the runtime messages; rewording fails A1 and forces coordinated update | Intended property (same as fixture-pattern self-tests) |

## 6. Migration steps (one atomic commit)

1. `git rm -r internal/file internal/message internal/notification internal/audit` (34 files).
2. Edit `scripts/check-audit-swallow.sh` (§3.2): usage header, exclusion block, guard fn + invocations, self-tests A/B, membership tripwire.
3. Add 4 stub fixtures under `scripts/testdata/audit-swallow/quarantine_positive/` (§3.3).
4. Edit `Makefile` line 78 (§3.4).
5. Edit `scripts/check-architecture.sh` (§3.5).
6. Edit `AGENTS.md` bullets (§3.6) and `internal/domain/audit.go` comment (§3.7).
7. Commit message must narrate the quarantine + tripwire landing (block/comment precedent: one commit).

Rollback: `git revert` of the single commit is total — guards, roots, fixtures, and comments revert together; no half-quarantined state is possible.

## 7. Testable acceptance mapping

| Requirement | Acceptance (testable) | Verification |
|---|---|---|
| REQ-1 deletion | AC-1 | `make test && make lint && make lint-strict && make check-file-sizes && make build` all exit 0; `grep -rl "aero-id/internal/\(file\|message\|notification\|audit\)" --include="*.go" .` → no output; `go list ./internal/{file,message,notification,audit}/...` → package not found |
| REQ-2 FEATURES retirement | AC-4 | `make check-architecture` exit 0; `grep -n "FEATURES=" scripts/check-architecture.sh` shows `FEATURES=()` + quarantine comment; no `internal/audit` in header/ALLOWED_SHARED; no `notification:block` |
| REQ-3 guards + self-tests | AC-2 | Baseline: gate exit 0 with four "✓ quarantine guard: internal/{file,message,notification,audit} absent (FM-12)" lines and A/B checks passing. Mutation probes (throwaway worktree, restore after): (a) `mkdir -p internal/file` → exit 1 + violation line; (b) delete one guard invocation → exit 1 + A2 message; (c) delete one stub dir → exit 1 + B message; (d) delete the violation-message line → exit 1 + A1 message |
| REQ-4 roots + tripwire | AC-3 | Remove one of the four roots from Makefile:78 → `make check-audit-swallow` exit 1 + "missing from scan ROOTS"; restore → exit 0; no-arg `bash scripts/check-audit-swallow.sh` stays green and exempt |
| REQ-5 comments | AC-5 | `grep -n "unwired parallel tree" AGENTS.md scripts/check-audit-swallow.sh internal/domain/audit.go` → only past-tense historical notes; no present-tense reference to the four paths; `grep -n "internal/{file,message,notification,audit}" scripts/check-audit-swallow.sh` → only the rewritten exclusion block |
| REQ-6 gate integrity | AC-4 | `make check-audit-manifest && make check-composition-root && make check-proto-sync` exit 0; `git diff --stat scripts/check-audit-manifest.sh scripts/check-composition-root.sh scripts/check-proto-sync.sh Makefile` → only Makefile line 78 differs |
| Full gate | — | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-composition-root && make check-proto-sync && make check-integration-compile && make build` all exit 0 |

`make test-integration` is **not** required: no SQL, migrations, storage, connectors, event ingestion, or callback-delivery logic changes (live surfaces untouched).

## 8. Risks

| Risk | Mitigation |
|---|---|
| Guard + self-test A as originally specified is unsatisfiable (permanent red) | C3 fix: A1 template grep + A2 per-dir invocation grep; semantics preserved |
| `domain.AuditEvent` wrongly pruned later (evidence's "becomes dead" claim) | C1 correction embedded in the rewritten audit.go note; `AuditEvent` stays live |
| Empty FEATURES reads as "check-architecture removed" | Header comment documents why; domain rule still runs; check-audit-swallow dir guards are the total reintroduction tripwire; direction 3 is the designated successor for live-flow rules |
| Deletion breaks `check-integration-compile` | Verified: no live file references the removed integration-tagged tests |
| One stale doc surface missed (usage header at check-audit-swallow:63–66) | DA-1 folds it into REQ-5's consistency sweep |
| Self-test greps brittle to rewording | Intended: rewording fails A1/A2 and forces a coordinated update, same as fixture self-tests |
