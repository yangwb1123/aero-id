# Requirements — internal/block: close the dead-tree quarantine gap (delete the unwired package; T-10.2 gate completeness)

> **Module:** `internal/block` (unwired parallel tree — deletion direction; the live `internal/service/block.go` + `internal/repository/block*.go` pair is NOT touched)
> **Date:** 2026-08-08
> **Direction (selected, value 8 / risk 6 / effort 3 / confidence 9):** "Close the dead-tree quarantine gap: eliminate the two `_ = s.audit.Log(` swallow sites in internal/block (B2-2, T-10.2 gate completeness)"
> **Source analysis:** `docs/auto/analyses/internal-block-c9ab184b.json` (direction 2 of 3)
> **Method:** every cited file/symbol re-verified against the working tree at HEAD `cd65f57` (line numbers below are from the current tree). Corrections to the direction's citations are recorded in §5 (C1–C4). The sibling direction for the LIVE block pair is specified in `docs/requirements/2026-08-08-internal-block-fail-closed-wiring-requirements.md` and is out of scope here.

---

## 1. Problem statement (verified)

`internal/block/block.go` still ships two swallowed audit writes, and the T-10.2 gate cannot see them:

1. **Two swallow sites, exactly as cited:** `_ = s.audit.Log(ctx, &domain.AuditEvent{...})` at `internal/block/block.go:66` (Block, action `user.block`, `domain.AuditUserBlock`) and `:86` (Unblock, action `user.unblock`, `domain.AuditUserUnblock`). Both are the dead-tree `domain.AuditLogger` shape: the error is discarded, so a failed audit write is indistinguishable from a successful one. These are 2 of the 6 repository-wide sites enumerated in the quarantine note.
2. **Quarantine marker names them:** `internal/domain/audit.go:15-19` — "QUARANTINED DEAD TREE — do not wire as-is … Six `_ = s.audit.Log(` swallow sites remain there (internal/file/service_share.go:45,75; internal/block/block.go:66,86; internal/comment/service.go:431; internal/message/service.go:289)." The other four sites are verified still present at exactly those lines in the current tree.
3. **The gate excludes the package by design:** `make check-audit-swallow` runs `scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd` (Makefile:67-69). The script's own comment documents the exclusion: "the unwired parallel tree internal/{file,message,comment,block,notification,search,audit} — only the live layer is scanned". A repository-wide grep for the P1 swallow shape over `internal/` today yields exactly the six sites — two of which the gate cannot see because their package is excluded.
4. **The package is provably unreachable from any composition root:** its only importer in the whole repository (test or non-test) is `internal/notification/dispatcher.go:8` (import of `block.BlockChecker`, used at `:17` field and `:23` `WithDispatcherBlockChecker`), and `internal/notification` itself has **zero importers outside its own package** (repository-wide grep over `internal/`, `cmd/`, `internal/app/`). Nothing in `cmd/*` or `internal/app/new.go` wires either package. Deleting `internal/block` therefore has no runtime effect.
5. **The live layer already owns the equivalent capability:** `internal/service/block.go:14-16` declares `service.BlockChecker` with the identical method set (`IsBlocked(ctx, blocker, blocked) (bool, error)`); the doc comment at `:11-13` states it is "the narrow read-only view of the block store used by the message and notification guards". Nothing from the dead tree needs folding in; the direction's "or its BlockChecker folded into the live service" alternative is already satisfied by the existing live interface.

Consequence: deleting `internal/block/` (8 files, see §2) removes 2 of the 6 swallow sites with zero runtime effect, makes the gate's "0 swallowed audit calls" claim true for the whole repository **as far as `internal/block` is concerned**, and turns the extended-gate branch of the direction's acceptance into a regression tripwire that goes red if the swallow shape is ever reintroduced there. The remaining 4 sites (file ×2, comment ×1, message ×1) stay quarantined under their sibling directions (§4, AC-1 note).

---

## 2. Scope

**In scope**

- Delete the entire `internal/block/` package — all 8 files: `block.go`, `block_test.go`, `handler_grpc.go`, `handler_grpc_test.go`, `handler_http.go`, `handler_http_test.go`, `repository.go`, `repository_integration_test.go` (`repository_integration_test.go` is `//go:build integration`, line 1). No replacement code anywhere.
- Sever the single import site: `internal/notification/dispatcher.go` — replace `block.BlockChecker` (import `:8`, field `:17`, option `:23`) with a package-local interface of the same method set. The existing test double `mockBlockChecker` (`internal/notification/dispatcher_test.go:33-40`) satisfies it structurally, so **no test file changes** are required; `Dispatcher.Handle` behavior (nil-guard + `IsBlocked` filter at `dispatcher.go:62-70`) stays byte-identical.
- Extend the `check-audit-swallow` gate roots with `internal/block` (Makefile:69) — green after deletion, red if the swallow shape is re-introduced there (regression tripwire; AC-1).
- Update the quarantine documentation that names the deleted files, so no cited evidence stays false (REQ-4; full repo-wide sweep adjudication in §7):
  - `internal/domain/audit.go:15-19`: the site enumeration drops `internal/block/block.go:66,86` and the count changes "Six" → "Four"; **the note's package brace list (:16-17) also drops `block`** — the remaining unwired tree is `internal/{file,message,comment,notification,search,audit}`.
  - `scripts/check-audit-swallow.sh`: the exclusion comment drops `block` from `internal/{file,message,comment,block,notification,search,audit}`.
  - `AGENTS.md:333` (promoted to mandatory by the D-4 sweep ruling, §7): the gotcha bullet drops `block` from `internal/{file,message,comment,block,notification,search}`.

**Out of scope (deliberately, per the direction)**

- The remaining 4 swallow sites (`internal/file/service_share.go:45,75`; `internal/comment/service.go:431`; `internal/message/service.go:289`) — separate sibling directions; the "repository-wide 0 matches" branch of the direction's acceptance is their **compound** target, not this direction's gate (§4).
- The live block pair (`internal/service/block.go`, `internal/repository/block_audit.go`, `internal/app/new.go:197-198`) — covered by `docs/requirements/2026-08-08-internal-block-fail-closed-wiring-requirements.md`.
- Deletion of `internal/notification` (also unwired, but its removal is a separate decision; this direction only severs its `internal/block` import).
- Any migration, proto, handler, or config change (none exist for `internal/block` outside the package).

---

## 3. Evidence verification

| # | Direction's claim | Verification result |
|---|---|---|
| E1 | `internal/block/block.go:66` and `:86` — `_ = s.audit.Log(` swallow sites | ✅ Confirmed exactly at :66 (Block, `domain.AuditUserBlock`) and :86 (Unblock, `domain.AuditUserUnblock`); both after the repo mutation (`s.blocks.Block/Unblock`), both discard the error. These are the only `_ =` audit shapes in the package (grep of all non-test files). |
| E2 | Two of the six repository-wide swallow sites | ✅ All six verified at current HEAD: `internal/file/service_share.go:45,75`, `internal/block/block.go:66,86`, `internal/comment/service.go:431`, `internal/message/service.go:289`. |
| E3 | Quarantine note at `internal/domain/audit.go:12-19` naming block.go:66,86 | ✅ Note present at :15-19 (comment block :10-19); names `internal/block/block.go:66,86` at :18 and the six-site count at :17. (C3: exact span is :15-19, not :12-19.) |
| E4 | Gate excludes the dead tree; roots are internal/service internal/repository internal/app | ✅ Makefile:67-69 — roots are `internal/service internal/repository internal/app internal/storage cmd` (C1: current roots also include `internal/storage` and `cmd`; line span is :67-69, not :63-65). `scripts/check-audit-swallow.sh` documents the exclusion of `internal/{file,message,comment,block,notification,search,audit}` and never scans them. |
| E5 | Only non-test importer is `internal/notification/dispatcher.go:8` (for `block.BlockChecker`) | ✅ Repository-wide `grep -rln "aero-id/internal/block"` over `internal/` + `cmd/` matches exactly `internal/notification/dispatcher.go` (import :8; `block.BlockChecker` at :17, :23). No test file imports the package either. |
| E6 | `internal/notification` itself has zero importers outside its package | ✅ `grep -rln "aero-id/internal/notification"` over `internal/` and `cmd/` → 0 matches outside `internal/notification/`. The live outbox relay uses `service.NewNotificationDispatcher` (`internal/app/outbox.go:36`), not this package. |
| E7 | Live equivalent BlockChecker at `internal/service/block.go:12-20` | ✅ Interface declared at :14-16 with the identical method set; doc comment :11-13 ("narrow read-only view … used by the message and notification guards"). (C2: span is :11-16, not :12-20.) Nothing needs folding in. |
| E8 | Import-scan evidence in `docs/requirements/2026-08-06-internal-block-in-tx-audit-requirements.md` E9 | ✅ Verified at :61 — "E9 \| `internal/notification/dispatcher.go:8` is the only consumer of `internal/block` \| ✅ Confirmed by repository-wide import scan". |
| E9 | Acceptance red-proof fixtures exist | ✅ `scripts/testdata/audit-swallow/` contains `positive/fixture.go`, `negative/`, `cmd_positive/`; the gate self-tests them before scanning (positive must match, negative must not, cmd fixture must fire under a `cmd/` root). |

**Empirically captured during this verification (baseline at HEAD `cd65f57`):**

- `go build ./...` → exit 0; `go vet ./...` → exit 0; `make check-integration-compile` → green (the package's only integration-tagged file, `repository_integration_test.go`, has no external importers, so deletion cannot break the compile gate).
- **Red-proof (pre-change, live):** `bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd internal/block` fails with exactly:
  ```
  AUDIT SWALLOW VIOLATIONS (must be 0):
  internal/block/block.go:66:		_ = s.audit.Log(ctx, &domain.AuditEvent{
  internal/block/block.go:86:		_ = s.audit.Log(ctx, &domain.AuditEvent{
  ```
  i.e., the extended roots **do** detect the two sites the direction removes — the tripwire is real, not nominal.

---

## 4. Requirements and acceptance

### REQ-1 — Delete the `internal/block` package outright

Remove all 8 files under `internal/block/` (4 non-test, 4 test). No code is moved, folded, or replaced in any other package; the live `service.BlockChecker` (`internal/service/block.go:14-16`) already covers the capability (E7).

**Acceptance**

- AC-1.1: `ls internal/block` fails / the directory does not exist in `git status` after the change.
- AC-1.2: `go build ./...` exits 0 with the package removed (the direction's acceptance "go build ./... passes with internal/block removed").
- AC-1.3: `go vet ./...` exits 0, and `make check-integration-compile` stays green (the package's only integration-tagged test is deleted with it; nothing outside the package referenced it).

### REQ-2 — Sever the import at `internal/notification/dispatcher.go`

In `internal/notification/dispatcher.go`: drop the `"github.com/aero/aero-id/internal/block"` import (:8), declare a package-local `BlockChecker` interface with the same method set (`IsBlocked(ctx context.Context, blocker, blocked string) (bool, error)`), and use it for the `blocks` field (:17) and `WithDispatcherBlockChecker` (:23). `Handle` behavior is unchanged (nil-guard + `IsBlocked` filter, `dispatcher.go:62-70`). `dispatcher_test.go` needs no edits: its `mockBlockChecker` (:33-40) satisfies the local interface structurally.

**Acceptance**

- AC-2.1: `grep -rln "github.com/aero/aero-id/internal/block" --include='*.go' internal cmd` returns 0 matches — stronger than the direction's "no non-test package imports": there are no test importers either (E5).
  **Standing-gap note (FM-12):** this grep is a one-time done-gate assertion, not a standing check — nothing re-runs it on later builds. The reintroduction it cannot see (a benign recreated package, where the import compiles again and the audit error is propagated) is closed by the standing quarantine guard of AC-3.3; code review remains the last line for a deliberate policy lift (§7).
- AC-2.2: `go test ./internal/notification/...` exits 0 (existing `dispatcher_test.go` cases at :158-161 and :196-200 pass unchanged).
- AC-2.3: `go vet ./internal/notification/...` exits 0.

### REQ-3 — Extend the `check-audit-swallow` gate roots with `internal/block`

Append `internal/block` to the root list on Makefile:69 (`@bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd internal/block`). After deletion the scan of the (empty) path trivially returns 0 matches; if the swallow shape is ever reintroduced into the package, the gate goes red. This is the direction's "(or the gate roots extended to include internal/block with 0 matches)" branch, made into a permanent tripwire.

**Boundary closure (FM-12):** the roots tripwire only sees swallow *shapes*. A benign full reintroduction — compilable package + working import + **propagated** audit error — defeats it and the deletion/compile tripwire together (empirically proven; design §4.3 probe 5). REQ-3 therefore also carries a **standing existence assertion** — the operational form of `test ! -d internal/block`: a `QUARANTINE VIOLATION` check on `$SELF_DIR/../internal/block` inside `scripts/check-audit-swallow.sh` (design Step 3.6), with a `✓ quarantine guard: internal/block absent` echo so green provably means the guard ran (FM-2-style visibility). **M-only**: the guard lands in M with the deletion — at P it reds because the package legitimately exists (design §4.3 probe 3). It is script-body policy, not a Makefile line, so direct script invocations (red-proof, CI) enforce it too.

**Acceptance**

- AC-3.1: `make check-audit-swallow` exits 0 and prints `✓ check-audit-swallow: no swallowed or bare-call audit writes` (including the `internal/block` root).
- AC-3.2 (**red-proof, both parts**):
  - (a) The gate's self-test fixtures still behave as expected — `scripts/testdata/audit-swallow/positive/fixture.go` still produces ≥ 1 match (P1 shapes including the `_ = s.audit.Log(ctx, event)` six-site shape at `positive/fixture.go:7`), `negative/` still matches 0, `cmd_positive/cmd` still fires under a `cmd/` root; the script's own self-test block exits 0.
  - (b) Pre-change red-proof is captured in §3: the extended roots against the current HEAD fail listing exactly `internal/block/block.go:66,86`. Regression check of the same fact: on a scratch copy at pre-change HEAD, `bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd internal/block` must exit 1 and list both lines. (P-HEAD `4476c3a` — the committed gate wave — reproduces this from history: verified exit 1, exactly :66,:86; design §4.3 probe 2.)
- AC-3.3 (**standing quarantine guard, FM-12**): (i) the guard is present — `grep -q "internal/block must not exist" scripts/check-audit-swallow.sh` exits 0; (ii) at M, `make check-audit-swallow` exits 0 and prints `✓ quarantine guard: internal/block absent (FM-12)` alongside the root list (green provably includes the guard); (iii) red-test (scratch copy of M): recreate `internal/block` as a compilable package with a working import and a propagated audit error → `go build ./...` exits 0 but `make check-audit-swallow` exits 1 with `QUARANTINE VIOLATION` (design §4.3 probe 5).

### REQ-4 — Update the quarantine documentation that names the deleted files

- `internal/domain/audit.go:15-19`: the note's enumeration drops `internal/block/block.go:66,86` and the count changes "Six" → "Four" — leaving exactly `internal/file/service_share.go:45,75; internal/comment/service.go:431; internal/message/service.go:289`.
- `internal/domain/audit.go:16-17` (sweep finding S-2): the note's **package brace list** drops `block` from `internal/{file,message,comment,block,notification,search,audit}` — the remaining unwired tree is `internal/{file,message,comment,notification,search,audit}`. The original AC-4.1 pattern (`internal/block`) cannot match the brace form, so this citation would have survived the designed edit unnoticed.
- `scripts/check-audit-swallow.sh`: the exclusion comment drops `block` from `internal/{file,message,comment,block,notification,search,audit}` — the package is no longer quarantined because it no longer exists, and it is now a scanned root.
- `AGENTS.md:333` (sweep ruling — promoted to mandatory): the "Current repository gotchas" bullet drops `block` from `internal/{file,message,comment,block,notification,search}`. AGENTS.md is living, undated current-state guidance — not a dated record — so leaving the bullet is precisely the "cited evidence stays false" condition REQ-4 exists to prevent.

**Acceptance**

- AC-4.1 (**refined for the FM-12 guard**): `grep -rn "block.go:66\|comment,block\|block,notification" internal/domain/audit.go scripts/check-audit-swallow.sh` returns 0 matches (stale-citation forms, incl. the brace form the original pattern could not see), **and** `grep -n "internal/block" internal/domain/audit.go` returns 0 matches (the note names no package in any form). The script's literal `internal/block` occurrences are the FM-12 guard's own prescriptive text (violation message, comments) plus the root-list comment — intentional, pinned by AC-3.3(i), not stale citations.
- AC-4.2: the audit.go note enumerates exactly the four remaining sites (grep for `service_share.go:45` and `service.go:431` and `service.go:289` each matches once in the note).
- AC-4.3: `grep -n "comment,block" AGENTS.md` returns 0 matches and the :333 gotcha bullet still enumerates the remaining unwired packages (`internal/{file,message,comment,notification,search}` and `internal/audit`).

---

## 5. Corrections to the direction's citations (verified against the tree)

| # | Direction citation | Current tree | Impact |
|---|---|---|---|
| C1 | Makefile:63-65 gate roots "internal/service internal/repository internal/app" | Makefile:67-69; roots are `internal/service internal/repository internal/app internal/storage cmd` | Cosmetic — the gate still excludes `internal/block`; REQ-3 appends it to the current root list. |
| C2 | `internal/service/block.go:12-20` (live BlockChecker) | Interface at :14-16; doc comment :11-13 | Cosmetic — symbol and method set as cited. |
| C3 | `internal/domain/audit.go:12-19` (quarantine note) | Note at :15-19; site enumeration at :17-18 | Cosmetic — names `block.go:66,86` exactly as cited. |
| C4 | (implicit) dead-tree block.go swallow lines at :54/:76 in the 2026-08-06 requirements doc E1 | Current tree :66/:86, matching the domain/audit.go quarantine note | The 08-06 doc described an older tree; the quarantine note's numbers govern. |

No citation was found to be materially wrong; none changes the direction's conclusion.

---

## 6. Supplied acceptance checks, preserved and made testable

The direction's acceptance, verbatim:

> T-10.2: repository-wide grep for `'^[[:space:]]*_ = .*\\.(Log|Record|Publish|FinishPublication|ClaimPublication)\\('` over internal/ returns 0 matches (or the gate roots extended to include internal/block with 0 matches); go build ./... passes with internal/block removed; no non-test package imports github.com/aero/aero-id/internal/block (grep -rln over internal/ cmd/). T-10.2 red-proof: the gate self-test fixtures (scripts/testdata/audit-swallow/positive) still fail as expected.

Mapping to this direction (the `(or …)` branch is the applicable one — see below):

| Supplied check | Testable form in this spec | Where |
|---|---|---|
| Repo-wide grep returns 0 matches | **Not achievable by this direction alone** — 4 swallow sites remain in `internal/file` (×2), `internal/comment`, `internal/message` (E2). This is the compound T-10.2 target of the sibling directions; recorded here as a dependency, not a gate. | §4 note, §7 |
| …or gate roots extended to include internal/block with 0 matches | REQ-3: roots extended; `make check-audit-swallow` green; AC-3.1/AC-3.2 | §4 REQ-3 |
| go build ./... passes with internal/block removed | AC-1.2 (+ AC-1.3: vet + check-integration-compile) | §4 REQ-1 |
| no non-test package imports internal/block | AC-2.1 — strengthened to *no package of any kind* imports it (grep over `internal/` + `cmd/`, `--include='*.go'`) | §4 REQ-2 |
| red-proof: positive fixtures still fail as expected | AC-3.2(a) fixture self-tests; AC-3.2(b) pre-change extended-roots scan already fails on block.go:66,86 (captured in §3) | §4 REQ-3 |

**Why branch (a) cannot be this direction's gate:** the supplied grep pattern over `internal/` today matches the six sites (E2). This direction removes exactly the two in `internal/block`. Claiming "0 matches over internal/" after this change would be false; the direction's own "(or …)" alternative is therefore the branch this spec gates on, and the repo-wide-zero branch is stated as the compound target that sibling file/comment/message directions complete. Once those land, REQ-3's extended roots already cover the whole repository with no further gate work.

---

## 7. Constraints and dependencies

- **No runtime effect:** `internal/block` is unreachable from any composition root (E5, E6); the change is delete + import-sever + gate-tripwire + doc consistency. `cmd/server` and `internal/app/new.go` are untouched.
- **Sibling directions (out of scope, but the compound gate depends on them):** the remaining four swallow sites in `internal/file/service_share.go:45,75`, `internal/comment/service.go:431`, `internal/message/service.go:289` are quarantined exactly as today; converting or deleting them is the remaining work to make the repo-wide-zero branch of the T-10.2 acceptance true.
- **Sibling direction for the live block pair:** `docs/requirements/2026-08-08-internal-block-fail-closed-wiring-requirements.md` (in-tx BlockWithAudit/UnblockWithAudit, fail-closed `NewBlockRepository`). Independent of this direction; neither depends on the other.
- **Required by acceptance (AC-4.3) — D-4 sweep ruling:** `AGENTS.md:333` drops `block` from the unwired-tree sentence. Rationale: AGENTS.md is living, undated current-state guidance (unlike the dated records below), so its enumeration of `internal/block` as an existing package is exactly the "cited evidence stays false" condition REQ-4 targets; the historical-record carve-out applies only to dated records. The edit is a one-token change of the same class already accepted as mandatory in `scripts/check-audit-swallow.sh`.

**D-4 sweep adjudication (executed 2026-08-08):** `rg 'internal/block'` over the repo → 0 hits in README.md, Makefile, and all scripts except the files below. **Mandatory-update** (working-tree files whose claims become false): the four REQ-4 sites above — audit.go site enumeration + count, audit.go package brace list, script exclusion comment, AGENTS.md:333. **Historical-record (no edit):** `docs/requirements/2026-08-08-internal-block-dead-tree-quarantine-gap.md` + `docs/designs/2026-08-08-internal-block-dead-tree-quarantine-gap-design.md` (this direction's own pre-change evidence and red-proof baseline — the "Six"/block.go:66 citations are the Step 0 record); `docs/requirements/2026-08-08-internal-block-fail-closed-wiring-requirements.md` (sibling spec — its dead-tree statements are verification-time evidence; re-verify at sibling implementation, no edit from here); `docs/requirements/2026-08-06-cmd-migrate-check-audit-swallow-gate.md` (parent spec of the gate, incl. the `block.go:66,86 → block.audit_failed` row); all remaining 08-06/08-07 requirements/plans/designs (in-tx-audit req+design, audit-publications req+design, outbox-publications req, bulkhead-drain design, connector-aeroim census, comment change-inventory); all 2026-07-10/11 requirements and `docs/results/` (44 files) and `docs/REVIEW_2026-06-30_ARCHITECTURE_DEEP.md` (dated scan records); `docs/auto/**` (immutable pipeline artifacts: analyses JSONs, SUMMARY.md, state.jsonl, runs/); `scripts/testdata/audit-swallow/positive/fixture.go:5` "P1: six-site shape" (gate self-test fixture — self-labeled "historical swallow shape" archive, AC-3.2(a)-anchored; the shape itself remains live through the four remaining sites); `.qwen/skills/auto-skill-go-architecture-import-check/SKILL.md:169` (generic extracted skill template, illustrative example, not a repo-state citation). The "six" enumerations in the 08-06 audit-record/outbox docs and the cmd-server hardening plan are a *different* family (live-layer `Record` sites, already converted) — historical. `internal/block/*_test.go` self-references and `internal/notification/dispatcher.go:8` are resolved by REQ-1/REQ-2 themselves. Line-number note: the AGENTS.md bullet is at **:333**; the design draft's claimed ":332" was itself a mis-correction — this spec's original :333 was correct.
- **FM-12 boundary adjudication (adopted, 2026-08-08):** the empirical tripwire test proved the one state that defeats both standing tripwires is benign full reintroduction (compilable package + working import + propagated audit error) — build and gate both green, invisible to every standing check (AC-2.1 is a one-time done-gate grep). It is **not** a swallow-safety gap (a propagated audit write is the fail-closed form the live layer requires; the security claim holds unconditionally) but **is** a dead-tree hygiene gap. Ruling: **adopt** the standing `test ! -d internal/block` guard, **in the script body** (not the Makefile target — the script already owns dead-tree policy; direct script invocations must not bypass it; `$SELF_DIR`-relative keeps it cwd-independent), **M-only** (at P the package legitimately exists, so the guard reds — it cannot precede the deletion; this also self-enforces M's atomicity, design invariant (d)). Zero false positives (the path must never exist); the only escape is a deliberate quarantine lift in one commit (delete the guard + update the audit.go note and the exclusion comment).
- **No migrations, no proto, no config, no handlers, no new packages.** `make proto` is not involved. `make test` (unit) is unaffected beyond the deleted package's own tests; `make test-integration` loses one integration file (`internal/block/repository_integration_test.go`) and no other integration test referenced it.

---

## 8. Done gate for this direction

```bash
make test && make lint && make lint-strict && make check-file-sizes && \
make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && \
make check-integration-compile && make build
```

plus the direction-specific assertions:

```bash
test ! -d internal/block
grep -rln "github.com/aero/aero-id/internal/block" --include='*.go' internal cmd   # → 0 matches
grep -rn "block.go:66\|comment,block\|block,notification" internal/domain/audit.go scripts/check-audit-swallow.sh  # → 0 matches (AC-4.1 citation forms, incl. brace form)
grep -n "internal/block" internal/domain/audit.go  # → 0 matches (AC-4.1 literal form; script literals are the FM-12 guard's own text — AC-3.3.i)
grep -n "comment,block" AGENTS.md  # → 0 matches (AC-4.3)
bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd internal/block  # → ✓ green, incl. ✓ quarantine guard echo (AC-3.3.ii)
grep -q "internal/block must not exist" scripts/check-audit-swallow.sh  # → exit 0: FM-12 guard present (AC-3.3.i)
go test ./internal/notification/...   # → ok
```
