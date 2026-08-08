# Design — internal/block dead-tree quarantine gap: delete, sever, tripwire (requirements closeout)

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-08-internal-block-dead-tree-quarantine-gap.md` (REQ-1..REQ-4) |
| Analysis | `docs/auto/analyses/internal-block-c9ab184b.json` (direction 2 of 3: "Close the dead-tree quarantine gap: eliminate the two `_ = s.audit.Log(` swallow sites in internal/block", value 8 / risk 6 / effort 3 / confidence 9) |
| Verified at | HEAD `cd65f57` (working tree dirty from parallel workstreams; `git diff` touches neither `internal/block/` nor `internal/notification/`, so this module's verification is unaffected) |
| Verdict | **Every evidence claim re-verified against the current tree — all hold. The D-4 repo-wide sweep corrected one of this design's own claims: the AGENTS.md gotcha bullet is at :333 (the requirements spec's :333 was right; this draft's ":332" was the drift), and the sweep ruling promotes that bullet from 'recommended, non-gated' to a mandatory REQ-4 item (AC-4.3). N-5 resolved with the full commit-boundary audit: the entire gate substrate is uncommitted, no parallel audit-gate commit exists, two hidden prerequisites found (`callback_worker.go` for the gate's own T-5(a) assertion; a two-line anchor for the note edit), and the sequence P (gate wave) → M (migration, one atomic commit) is pinned with a fresh-checkout gate verification as Step 5. The design is delete + consumer-side interface + gate tripwire + doc consistency: zero runtime effect, zero API change outside an unreachable package, eleven failure modes closed, ordered steps that stay buildable at every point.** |

## 1. Independent verification of the evidence

Every cited file/symbol was re-checked against the current working tree, not taken on trust.

| # | Claim | Independent check | Verdict |
|---|---|---|---|
| E1 | `internal/block/block.go:66,86` — `_ = s.audit.Log(` swallow sites | `sed -n '60,100p'` — :66 (Block, `domain.AuditUserBlock`) and :86 (Unblock, `domain.AuditUserUnblock`), both after the repo mutation, both discard the error. Repo-wide grep of the P1 shape: these are the only `_ =` audit shapes in the package | ✅ Confirmed |
| E2 | Six repo-wide swallow sites | `rg -n '_ = s\.audit\.Log\(' internal/ --glob '*.go'` → exactly `message/service.go:289`, `block/block.go:66,86`, `file/service_share.go:45,75`, `comment/service.go:431` | ✅ Confirmed |
| E3 | Quarantine note at `internal/domain/audit.go:12-19` | Note block is :15-19 (C3 in the spec); names `internal/block/block.go:66,86` at :18 and the six-site count at :17 | ✅ Confirmed (span corrected in spec) |
| E4 | Gate excludes the dead tree; roots at Makefile:67-69 | Root list is on Makefile:69: `internal/service internal/repository internal/app internal/storage cmd`; `.PHONY`+target span :67-69. `scripts/check-audit-swallow.sh:25-26` documents the `internal/{file,message,comment,block,...}` exclusion | ✅ Confirmed |
| E5 | Sole importer `internal/notification/dispatcher.go:8` | `rg -n 'internal/block' --glob '*.go'` → exactly dispatcher.go:8 (import), :17 (field `blocks block.BlockChecker`), :23 (`WithDispatcherBlockChecker(blocks block.BlockChecker)`). No test file imports the package | ✅ Confirmed |
| E6 | `internal/notification` has zero importers outside its package | `rg -ln 'internal/notification' --glob '*.go'` → no match outside `internal/notification/`. The live relay uses `service.NewNotificationDispatcher` (`internal/app/outbox.go:36`) with `service.WithDispatcherBlockChecker` | ✅ Confirmed |
| E7 | Live `service.BlockChecker` at :12-20 | Interface declared :14-16, identical method set `IsBlocked(ctx, blocker, blocked) (bool, error)`; doc comment :11-13 | ✅ Confirmed (span :11-16) |
| E8 | 08-06 req doc E9 | `docs/requirements/2026-08-06-internal-block-in-tx-audit-requirements.md:61` — exactly as quoted | ✅ Confirmed |
| E9 | Red-proof fixtures | `scripts/testdata/audit-swallow/positive/fixture.go:7` carries the `_ = s.audit.Log(ctx, event)` P1 shape; `negative/` and `cmd_positive/cmd/` present; script self-tests all three before scanning | ✅ Confirmed |
| E10 | Red-proof (pre-change) | **Ran live:** `bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd internal/block` → exit 1 listing exactly `internal/block/block.go:66` and `:86`. The tripwire is real | ✅ Confirmed |
| E11 | Baselines | `go build ./...` exit 0; `go vet ./...` exit 0; `go test ./internal/notification/...` ok (0.014 s); `repository_integration_test.go:1` is the package's only `//go:build integration` file | ✅ Confirmed |
| E12 | Direction 2 of 3 in analysis | `internal-block-c9ab184b.json` (markdown-fenced JSON array): 3 directions; direction 2 title/values/acceptance verbatim, including the "(or the gate roots extended to include internal/block with 0 matches)" branch, "go build ./... passes with internal/block removed", "no non-test package imports", and the T-10.2 red-proof clause | ✅ Confirmed |
| E13 | Sibling fail-closed spec covers the live pair | `docs/requirements/2026-08-08-internal-block-fail-closed-wiring-requirements.md` — verified header/scope: live `internal/service/block.go` + `internal/repository/block*.go`, `internal/app/new.go:197-198`, residual fail-open gate `WithBlockAuditLogger` | ✅ Confirmed |

**New findings this verification adds to the record (none contradict the spec):**

- **N-1 (citation drift — corrected by the D-4 sweep):** the AGENTS.md gotcha bullet naming `block` is at **:333**, exactly as the requirements spec stated (`awk 'NR>=328 && NR<=336'` pins the bullet at :333). This design's draft ":332" was itself a mis-correction. The sweep ruling promotes the bullet from 'recommended, non-gated' to a mandatory REQ-4 item (D-4, §6, AC-4.3).
- **N-2 (name-collision check):** the live tree has its own `NewBlockHandler`/`NewBlockServer` in `internal/handler/http/block.go` and `internal/handler/grpc/block.go`, wired at `internal/app/new.go:391` and `internal/app/grpc.go:68` over `service.BlockService`, plus the generated `aeropb.RegisterBlockServiceServer`. These are different symbols in different packages; deleting `internal/block` cannot affect them. The dead package's `BlockServer`/`BlockHandler` were never registered by any composition root.
- **N-3 (historical docs):** several older plan/design/result docs cite `internal/block` (`docs/plans/2026-08-06-internal-block-in-tx-audit-design.md`, `docs/plans/2026-08-06-internal-block-audit-publications-single-delivery-path-design.md`, `docs/plans/2026-08-07-outbox-audit-publications-single-delivery-path-design.md`, `docs/designs/2026-08-07-internal-pkg-bulkhead-drain-sole-delivery-design.md`, `docs/REVIEW_2026-06-30_ARCHITECTURE_DEEP.md`, plus results/requirements docs). These describe the tree as it was and are not load-bearing evidence of current state; they stay untouched (consistent with how the spec treats the 08-06 doc's stale :54/:76 citations as C4-cosmetic).
- **N-4 (gate semantics on a missing root):** the script scans `for root in "${ROOTS[@]}"` with `found="$(grep -rnE ... "$root" ... 2>/dev/null)"` under `set -u` (no `set -e`). A nonexistent `internal/block` root yields no matches and no failure — REQ-3 is therefore safe post-deletion by construction, not by coincidence.
- **N-5 (load-bearing, resolved — full commit-boundary audit in §4.2):** the gate target, quarantine note, script, and fixtures are **uncommitted working-tree content**, and **no parallel audit-gate commit exists anywhere** (`git log --all --grep audit-swallow` → empty; the script and `scripts/testdata/audit-swallow/` are absent from every ref). Verified against the actual git state at `cd65f57`: `git show HEAD:Makefile` has **no** `check-audit-swallow`/`check-anchored-event-ids`/`check-audit-manifest`/`check-integration-compile` targets and no `build-audit-replay`; `HEAD:internal/domain/audit.go` has **no** quarantine note (HEAD carries the pre-conversion "must not fail the primary operation" doc comment); `scripts/check-audit-swallow.sh`, all three `scripts/testdata/audit-swallow/` fixtures (plus the sibling gates' scripts+fixtures) are `??` untracked; the Makefile gate targets and the audit.go note are `M` uncommitted diffs. The resolution audit surfaced **two dependencies the original finding missed**:
  - **Hidden T-5 content dependency:** the gate's own T-5(a) assertion requires *exactly one* non-test `drainPublications(` call site in `internal/service`, located in `callback_worker.go` — and `internal/service/callback_worker.go` is **untracked** (`??`). A fresh checkout that has the script but not that file fails the gate: `drainPublications( has 0 non-test call site(s), want exactly 1`. (The definition in `internal/service/callback_audit.go` is tracked; the sole call site is `callback_worker.go:47`.)
  - **Anchor mismatch in the original Step 3.2:** the replacement string did not exist verbatim — the note wraps after "swallow sites" (working-file lines 18–19), so the edit must be a two-line anchor (corrected in §4).
  Also pinned: `cmd/audit-replay/` is untracked and required by the uncommitted `build` target's `build-audit-replay` (fresh-checkout `make build` fails without it — done-gate scope), and the sibling gates are fresh-checkout-safe *except* their own scripts+fixtures (their scans are existence-based, so missing untracked content can only remove violations; only check-audit-swallow's T-5(a) has an exact-count assertion).
  Consequence (pinned sequence in §4.2): the migration applies to the working tree and lands as commit **M** that must be **preceded by** (or atomically merged with) the parallel gate wave committed as a green commit **P** — otherwise the tripwire and the red-proof have no substrate, and a fresh checkout of M's HEAD must pass `make check-audit-swallow`.

## 2. Design

### D-1 — Delete the unreachable package outright (REQ-1)

Remove all 8 git-tracked files of `internal/block/` (`block.go`, `block_test.go`, `handler_grpc.go`, `handler_grpc_test.go`, `handler_http.go`, `handler_http_test.go`, `repository.go`, `repository_integration_test.go`) via `git rm -r internal/block`. No code is moved or folded:

- The package is provably unreachable: its only importer is `internal/notification` (E5), which itself has zero importers (E6), and neither is referenced from any `cmd/*` composition root or `internal/app/new.go` (E5/E6 scans).
- The capability it nominally provides already exists in the live layer: `service.BlockChecker` (:14-16), `service.blockService`, `repository.BlockRepository`, and the wired handlers `internal/handler/{http,grpc}/block.go` (N-2). Nothing needs folding in.
- The two swallow sites it contributes (`block.go:66,86`) disappear with it, removing 2 of the 6 repository-wide sites (E2) at zero runtime risk.

**Rejected alternatives:** (a) fail-closed conversion of the dead package — that is the sibling direction for the *live* pair (`2026-08-08-internal-block-fail-closed-wiring-requirements.md`) and would preserve unreachable code in exchange for gate greenness; (b) keeping the package as an empty stub — a stub would need to import nothing and serve nobody, and would leave the quarantine note's enumeration ambiguous.

### D-2 — Sever the import with a consumer-side interface (REQ-2)

In `internal/notification/dispatcher.go`:

- Drop the import at :8 (`"github.com/aero/aero-id/internal/block"`).
- Declare a package-local interface with the identical method set, placed next to `Dispatcher`:

```go
// BlockChecker is the narrow read-only view of the block store used by the
// notification guard. It duplicates the method set of the deleted dead-tree
// block.BlockChecker; the live service.BlockChecker satisfies it structurally.
type BlockChecker interface {
	IsBlocked(ctx context.Context, blocker, blocked string) (bool, error)
}
```

- Change the field :17 to `blocks BlockChecker` and the option :23 to `func WithDispatcherBlockChecker(blocks BlockChecker) DispatcherOption`.

`Handle` (:57-70) stays byte-identical: nil-guard (`if d.blocks != nil && n.ActorUID != ""`) then `IsBlocked(ctx, n.RecipientUID, n.ActorUID)` with error propagation, block → silent skip. The existing test double `mockBlockChecker` (`dispatcher_test.go:33-40`) satisfies the local interface structurally (Go interfaces are structural), so `dispatcher_test.go` needs **zero edits**; the block-skip cases at :158-161 and :196-200 pass unchanged. This is the minimal-diff form of the direction's "or its BlockChecker folded into the live service" alternative, which is already satisfied by `service.BlockChecker` (E7) — no cross-package folding is needed.

### D-3 — Gate tripwire root (REQ-3)

Append `internal/block` to the Makefile:69 root list:

```make
	@bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd internal/block
```

This implements the direction's "(or …)" acceptance branch as a **permanent tripwire**: post-deletion the root is a nonexistent path whose scan yields 0 matches (N-4 — safe by construction), and if the swallow shape is ever reintroduced at `internal/block/…`, the gate goes red. The alternative branch (repository-wide 0 matches over `internal/`) is *not* achievable by this direction alone — 4 swallow sites remain in `internal/file` (×2), `internal/comment`, `internal/message` (E2) — so it is recorded as the compound target of the sibling directions (§6), not this gate. Once those land, REQ-3's root already covers the former dead-tree path with no further gate work.

### D-4 — Canonical citations updated (REQ-4)

The D-4 repo-wide sweep (executed 2026-08-08: `rg -n 'internal/block'`, `block.go:66`, and the "Six"/"six-site" family over README.md, AGENTS.md, Makefile, scripts/, docs/) found **zero** hits in README.md and Makefile and adjudicated every hit as mandatory-update or historical-record. **Mandatory — working-tree files whose claims become false after D-1:**

- `internal/domain/audit.go:15-19` (quarantine note): drop `internal/block/block.go:66,86` from the site enumeration and change the count "Six" → "Four" (two-line anchor, §4 Step 3.2), **and drop `block` from the note's package brace list at :16-17** — the remaining unwired tree is `internal/{file,message,comment,notification,search,audit}`. The note must end up enumerating exactly `internal/file/service_share.go:45,75; internal/comment/service.go:431; internal/message/service.go:289`. (Sweep finding S-2: the brace form `internal/{file,message,comment,block,` is not matched by the AC-4.1 pattern `internal/block`, so the designed edit alone would have left this stale citation invisible to the gate; AC-4.1 is strengthened to cover both forms.)
- `scripts/check-audit-swallow.sh:25-26` (exclusion comment): drop `block` from `internal/{file,message,comment,block,notification,search,audit}` — the package no longer exists and is now a scanned root; keeping it in the exclusion list would be a self-contradictory doc (excluded yet scanned).
- `AGENTS.md:333` (gotcha bullet — **promoted from 'recommended, non-gated' to mandatory by the sweep ruling**, §6, AC-4.3): drop `block` from `internal/{file,message,comment,block,notification,search}`. AGENTS.md is living, undated current-state guidance, not a dated record; leaving the bullet would be exactly the "cited evidence stays false" condition REQ-4 exists to prevent, and the edit is the same class as the script-comment edit already accepted as mandatory.

**Historical-record (untouched):** this design doc and the requirements spec (pre-change evidence and red-proof baseline — their "Six"/block.go:66 citations are the Step 0 record and must stay); the sibling `2026-08-08-internal-block-fail-closed-wiring-requirements.md` (dated spec — its dead-tree statements are verification-time evidence; re-verify at sibling implementation, no edit from this direction); the 08-06 gate parent spec `cmd-migrate-check-audit-swallow-gate.md` (incl. its `block.go:66,86 → block.audit_failed` row); the superseded 08-06/08-07 requirements/plans/designs (in-tx-audit req+design, audit-publications req+design, outbox-publications req, bulkhead-drain design, connector-aeroim census, comment change-inventory); all 2026-07-10/11 requirements and `docs/results/` (44 files) and `docs/REVIEW_2026-06-30_ARCHITECTURE_DEEP.md` (dated scan records); `docs/auto/**` (immutable pipeline artifacts: analyses JSONs, SUMMARY.md, state.jsonl, runs/); `scripts/testdata/audit-swallow/positive/fixture.go:5` "P1: six-site shape" (gate self-test fixture — self-labeled "historical swallow shape" archive and AC-3.2(a)-anchored; the shape itself remains live through the four remaining sites); `.qwen/skills/auto-skill-go-architecture-import-check/SKILL.md:169` (generic extracted skill template, illustrative example, not a repo-state citation). The "six" enumerations in the 08-06 audit-record/outbox docs and the cmd-server hardening plan are a different family (live-layer `Record` sites, already converted) — historical. `internal/block/*_test.go` self-references and `internal/notification/dispatcher.go:8` are resolved by REQ-1/REQ-2 themselves.

Not touched: the historical plan/design docs (N-3), the superseded 08-06 requirements doc (its stale :54/:76 citations are historical record).

### D-5 — Ordering rule: REQ-1 and REQ-2 are one atomic unit

REQ-1 (delete package) without REQ-2 (sever import) breaks `go build ./...` — the dispatcher's `block.BlockChecker` reference dangles. REQ-2 without REQ-1 is buildable (a package with zero importers is legal Go). Therefore:

1. Apply REQ-2 first in the working tree (tree still builds; dead package now has zero importers).
2. Apply REQ-1 (tree builds — verified: no remaining references).
3. REQ-3 + REQ-4 (tree builds; gate green).
4. Land as **one commit** (or, at most, one commit per step in a single PR) so no intermediate state with a dangling import is ever committed.

## 2.1 API changes

| Surface | Shape | Change |
|---|---|---|
| `internal/block` (whole package): `BlockChecker`, `Repository`, `Service`, `BlockList`, `Option`, `WithAuditLogger`, `NewService`, `BlockServer`, `NewBlockServer`, `BlockHandler`, `NewBlockHandler`, `blockRepo`, `NewRepository` | Internal, unreachable from any composition root (E5, E6, N-2) | **Deleted** — no caller exists |
| `internal/notification.Dispatcher` | `blocks` field type `block.BlockChecker` → local `BlockChecker`; `WithDispatcherBlockChecker(blocks BlockChecker)` | **Changed signature** — zero external callers (E6); behavior byte-identical (:57-70) |
| `internal/notification.BlockChecker` | New package-local interface, same method set | **Added** — consumer-side interface |
| `service.BlockChecker` (`internal/service/block.go:14-16`), `service.blockService`, `repository.BlockRepository`, `internal/handler/{http,grpc}/block.go`, `aeropb.RegisterBlockServiceServer`, `internal/app/new.go:391`, `internal/app/grpc.go:68` | Live block surface | **Unchanged** — satisfies the local interface structurally; distinct symbols (N-2) |
| HTTP JSON + OpenAPI, gRPC+Protobuf, config, migrations, proto generation | — | **Unchanged** — nothing in this direction touches them |

**No API change is observable outside the repository**: the only signature change is inside a package with zero importers, and the deleted package's surface had no callers.

## 2.2 Compatibility constraints

- **Runtime:** zero. No composition root, worker, route, or gRPC registration references the deleted package (E5, E6, N-2). `cmd/server` and `internal/app/*` are untouched.
- **Build order:** REQ-2 precedes REQ-1 within the atomic unit (D-5); every intermediate state in the working tree compiles.
- **Tests:** `dispatcher_test.go` needs no edits (structural typing, D-2); the deleted package's 4 test files go with it; `make check-integration-compile` loses the package's only integration-tagged file (`repository_integration_test.go:1`) and nothing outside referenced it (E11).
- **Gate script:** a nonexistent root is scanned as an empty set (N-4); the self-test fixtures (`positive/`, `negative/`, `cmd_positive/`) are outside the scanned roots and unaffected.
- **Anchor edits, not line edits:** the working tree is dirty from parallel workstreams; the Makefile root list, the audit.go note, and the script comment are edited by *text anchor* (the exact strings in §4), never by absolute line number.
- **No schema, no data:** the `user_blocks` table remains owned by the live repository (`internal/repository/block.go`); the dead `blockRow` type was a second GORM model over the same table and its removal changes nothing in the DB.

## 3. Failure modes

| # | Failure | Trigger | Effect | Detection | Mitigation |
|---|---|---|---|---|---|
| FM-1 | Dangling import | REQ-1 applied without REQ-2 (delete before sever) | `go build ./...` fails: `undefined: block` | `go build ./...` after step 1 | D-5 ordering; atomic commit; step-by-step build verification |
| FM-2 | Tripwire missing | REQ-3 skipped | Gate stays green today but the "(or …)" acceptance branch is unfulfilled; a reintroduced swallow at `internal/block/` would be invisible forever | `grep -n 'check-audit-swallow.sh' Makefile` — root list must end with `internal/block` | REQ-3 in the same commit; AC-3.1/AC-3.2(b) in the done gate |
| FM-3 | Stale canonical citations | REQ-4 skipped | `domain/audit.go` note names nonexistent files (`block.go:66,86`) and counts "Six" instead of "Four"; script exclusion and AGENTS.md gotcha enumerate a nonexistent package; future work misreads the enumeration and may "fix" dead lines | `grep -rn 'internal/block\|block.go:66\|comment,block\|block,notification' internal/domain/audit.go scripts/check-audit-swallow.sh` → 0; `grep -n 'comment,block' AGENTS.md` → 0 | AC-4.1/AC-4.2/AC-4.3 in the done gate |
| FM-4 | Swallow reintroduced at `internal/block/` | A future change recreates the package with a `_ = s.audit.Log(` shape | Swallowed audit write ships unnoticed (the exact original gap) | `make check-audit-swallow` goes red on the tripwire root | REQ-3 permanent root; red-proof AC-3.2(b) |
| FM-5 | Import reintroduced | A future change re-adds `internal/block` to any package | Build failure (package gone) | `go build ./...`; AC-2.1 grep | Deletion is final; AC-2.1 in done gate |
| FM-6 | Live surface confused with dead | Same-named constructors (`NewBlockHandler`/`NewBlockServer`) in both trees (N-2) | A reviewer "fixes" the dead package or expects the live handlers to change | Diff review; E5/E6 scans | D-1 states explicitly: live handlers untouched |
| FM-7 | Gate broken by missing root | `grep` on nonexistent `internal/block` | **None** — stderr discarded, no matches, no `set -e` (N-4) | Gate prints `✓` with the root list echoed | Documented in D-3; AC-3.1 |
| FM-8 | Parallel-workstream conflict | Dirty tree (other modules modified) | Doc/gate edits at shifted line numbers | `git diff` review | Text-anchor edits (compat §2.2); no workstream touches `internal/block`/`internal/notification` (verified) |
| FM-9 | Compound-target misattribution | Someone claims "0 matches repo-wide" after this change | False claim — 4 swallow sites remain in file/comment/message (E2) | Repo-wide P1 grep still yields 4 sites | §6 records the compound dependency; spec §4/§6 same |
| FM-10 | Gate substrate missing at fresh checkout | M (deletion) lands before/without the gate wave P — Makefile target absent, script/fixtures uncommitted, or `callback_worker.go` uncommitted (T-5 hidden dependency) | Fresh checkout of M's HEAD fails `make check-audit-swallow`: `No rule to make target` (no target), `scripts/check-audit-swallow.sh: No such file or directory` (no script), or `drainPublications( has 0 non-test call site(s), want exactly 1` (T-5, no `callback_worker.go`) — the done gate is red at the post-commit HEAD even though the dirty working tree was green | Step 5 fresh-checkout verification (§4.2); pinned sequence P→M | Commit P (green at its own HEAD) must land before M; target+script+fixtures in the same commit (no split where one lands without the other) |
| FM-11 | Tripwire root added before the deletion | Step 3's Makefile root edit lands in P or any pre-M commit while `internal/block` still exists with the two swallow sites | Gate red at that commit's HEAD (the exact pre-change red state ships in history) | `make check-audit-swallow` at each intermediate HEAD; §4.2 invariant (c) | The root edit is an M-only change; P ships the root list unchanged (without `internal/block`) |

## 4. Migration steps

Every step leaves the tree buildable (`go build ./...` + `go vet ./...`). Steps 1+2 are atomic (D-5).

**Step 0 — baseline capture (already done, verified §1):** record the red-proof output of the extended-roots scan against the current tree (`exit 1`, exactly `internal/block/block.go:66,86`) and the green baselines (`go build`, `go vet`, `go test ./internal/notification/...`, `make check-audit-swallow`). This is the AC-3.2(b) pre-change fixture; if the apply-time tree diverges, re-capture.

**Step 1 — sever the import (REQ-2):**
1. In `internal/notification/dispatcher.go`: delete the import line (`"github.com/aero/aero-id/internal/block"`); add the local `BlockChecker` interface (D-2); change `blocks block.BlockChecker` → `blocks BlockChecker` (:17) and `WithDispatcherBlockChecker(blocks BlockChecker)` (:23).
2. Verify: `go build ./...` exit 0; `go test ./internal/notification/...` ok **with zero test-file edits**; `go vet ./internal/notification/...` exit 0.

**Step 2 — delete the package (REQ-1):**
1. `git rm -r internal/block` (8 tracked files, §1 list).
2. Verify: `go build ./...` exit 0; `go vet ./...` exit 0; `test ! -d internal/block`; `grep -rln "github.com/aero/aero-id/internal/block" --include='*.go' internal cmd` → 0 matches (AC-2.1).
3. Commit steps 1+2 together as commit **M** (single logical change; FM-1) — the commit-boundary invariants (P before M, one atomic M) are pinned in §4.2.

**Step 3 — gate tripwire + canonical docs (REQ-3 + REQ-4):**
1. Makefile: replace the exact string
   `@bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd`
   with
   `@bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd internal/block`.
2. `internal/domain/audit.go` note: the text wraps across two lines, so the edit is a **two-line text anchor** (verified against the current tree — the original single-line anchor does not exist):
   - line 18: `// audit}), reachable only from its own tests. Six `_ = s.audit.Log(` swallow sites` → `// audit}), reachable only from its own tests. Four `_ = s.audit.Log(` swallow sites`
   - line 19: `// remain there (internal/file/service_share.go:45,75; internal/block/block.go:66,86;` → `// remain there (internal/file/service_share.go:45,75;`
   Result reads: "Four `_ = s.audit.Log(` swallow sites remain there (internal/file/service_share.go:45,75; internal/comment/service.go:431; internal/message/service.go:289)."
3. `scripts/check-audit-swallow.sh:25-26`: replace `internal/{file,message,comment,block,` with `internal/{file,message,comment,` (line 26 continues `notification,search,audit}` — keep).
4. `internal/domain/audit.go:16-17` (package brace list — sweep finding S-2): replace `internal/{file,message,comment,block,notification,search,` with `internal/{file,message,comment,notification,search,` (same two-line anchor region; the brace line stays one line, so a single-line anchor suffices).
5. `AGENTS.md:333` (sweep ruling, AC-4.3): replace `internal/{file,message,comment,block,notification,search}` with `internal/{file,message,comment,notification,search}`.
6. Verify: `make check-audit-swallow` exit 0; `bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd internal/block` exit 0; AC-4.1/AC-4.2/AC-4.3 greps (strengthened pattern covers literal and brace forms).
7. Steps 1–6 of this section are part of commit **M**, atomic with steps 1+2 (D-5); the Makefile root edit must **not** land in P or any pre-M commit (FM-11).

**Step 4 — done gate (spec §8):**

```bash
make test && make lint && make lint-strict && make check-file-sizes && \
make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && \
make check-integration-compile && make build
```

plus the direction-specific assertions from the requirements doc §8 (`test ! -d internal/block`, the three greps incl. AC-4.3, the extended-roots gate run, `go test ./internal/notification/...`). The AGENTS.md:333 gotcha bullet edit is part of REQ-4 (Step 3.5, AC-4.3) per the D-4 sweep ruling.

At a **fresh checkout of M's HEAD** this gate additionally requires the full P closure (§4.2) — the sibling gate scripts+fixtures (`check-anchored-event-ids.sh`, `check-audit-manifest.sh`, their `scripts/testdata/` trees) and `cmd/audit-replay/` (for the `build` target's `build-audit-replay`). Run Step 5 (§4.2) as the last migration action.

### 4.2 Commit boundary (N-5 resolution): pinned sequence and fresh-checkout gate verification

Verified against the actual git state at `cd65f57` (evidence: `git show HEAD:Makefile`/`HEAD:internal/domain/audit.go`, `git ls-files`, `git status --porcelain --untracked-files=all`, `git log --all --grep audit-swallow`). The migration is executed in a dirty working tree whose gate substrate is **entirely uncommitted**; the exact commit boundary is therefore:

**Commit P — the parallel gate wave (prerequisite, must land before M, green at its own HEAD).** P is the current uncommitted audit-gate work committed as-is: the Makefile gate targets (`check-audit-swallow` at :69 with the root list **without** `internal/block`, plus `check-anchored-event-ids`, `check-audit-manifest`, `check-integration-compile`, `build-audit-replay`), `scripts/check-audit-swallow.sh` + `scripts/testdata/audit-swallow/{positive,negative,cmd_positive/cmd}/fixture.go`, the sibling gate scripts + their `scripts/testdata/` trees, `internal/domain/audit.go` note in its current **"Six"** form, `internal/service/callback_worker.go` (T-5(a) substrate), and `cmd/audit-replay/` (build substrate). P's HEAD must pass `make check-audit-swallow` (green because the roots exclude `internal/block`, which still holds the two swallow sites) and `make build`.

**Commit M — the migration (single atomic commit, must not precede P).** Steps 1+2+3 of §4: dispatcher sever, `git rm -r internal/block` (8 tracked files), Makefile root list + `internal/block`, audit.go note "Six"→"Four" (two-line anchor) **plus the audit.go package brace list (S-2)**, script exclusion comment, **AGENTS.md:333 gotcha bullet (sweep ruling, AC-4.3)**. M's HEAD must pass `make check-audit-swallow` (green because the tripwire root scans an empty/nonexistent path, N-4) and the Step 4 done gate.

**Pinned invariants (each is a fresh-checkout red-state if violated):**

(a) **Target+script+fixtures are one unit.** No commit may contain the Makefile `check-audit-swallow` target without `scripts/check-audit-swallow.sh` (`bash: No such file or directory` at that HEAD), nor the script without the target (`No rule to make target`), nor the script without its three self-test fixtures (`SELF-TEST FAIL`). They land together in P.
(b) **P precedes M.** A fresh checkout of M without P's content has no gate at all — the deletion commit must never be the commit that first introduces or omits the gate.
(c) **The `internal/block` root is M-only.** Adding the tripwire root while the package still exists (any pre-M commit, including P) ships the red state (FM-11); the root edit is applied only inside M, atomically with the deletion.
(d) **M is atomic.** No commit boundary between the dispatcher sever, the `git rm`, and the doc edits (D-5) — an intermediate "deleted but not severed" state must never be committed (FM-1).
(e) **The "Six" form of the note is P's; the "Four" form is M's.** P's note accurately enumerates the pre-deletion tree; M's edit has a committed substrate.

If P and M are squashed into one combined commit (allowed alternative), the Step 0 red-proof can only be captured from the dirty working tree; the two-commit form is preferred because the red-proof at P's HEAD (below) is reproducible from history.

**Step 5 — fresh-checkout gate verification (last migration action; the N-5 acceptance).** After both commits land, verify from history — never from the dirty working tree:

```bash
# P-side: pre-deletion state, green gate + reproducible red-proof
git worktree add /tmp/aero-fc-p <P-hash>
( cd /tmp/aero-fc-p && make check-audit-swallow )   # exit 0: roots exclude internal/block
( cd /tmp/aero-fc-p && bash scripts/check-audit-swallow.sh \
    internal/service internal/repository internal/app internal/storage cmd internal/block )
# exit 1, exactly: internal/block/block.go:66 and :86  (AC-3.2(b) red-proof at committed P)

# M-side: post-deletion HEAD passes the gate and the done gate
git worktree add /tmp/aero-fc-m <M-hash>
( cd /tmp/aero-fc-m && \
  make check-audit-swallow && \                                  # tripwire root scans empty
  go build ./... && go vet ./... && go test ./internal/notification/... && \
  test ! -d internal/block && \
  [ "$(grep -rln 'github.com/aero/aero-id/internal/block' --include='*.go' internal cmd | wc -l)" -eq 0 ] && \
  [ "$(grep -rn 'internal/block\|block.go:66\|comment,block\|block,notification' internal/domain/audit.go scripts/check-audit-swallow.sh | wc -l)" -eq 0 ] && \
  [ "$(grep -n 'comment,block' AGENTS.md | wc -l)" -eq 0 ] && \
  make test && make lint && make lint-strict && make check-file-sizes && \
  make check-anchored-event-ids && make check-audit-manifest && \
  make check-integration-compile && make build )
git worktree remove /tmp/aero-fc-p && git worktree remove /tmp/aero-fc-m
```

Every command in the M-side block must exit 0; this proves that a fresh checkout of the post-commit HEAD passes `make check-audit-swallow` (and the full done gate) with only committed content. Any red line means the P closure is incomplete or M violated an invariant (a)–(e) — fix by completing P, never by weakening the gate.

### 4.1 Empirical fold verification (this design, full migration applied to a scratch copy)

All steps 1–4 were applied to a tar-copy of the **dirty working tree** (`/tmp/aero-fold`, `.git` excluded; copy fidelity proven by re-running the red-proof in the copy first — exit 1 listing exactly `block.go:66,86`). Results, all on the folded tree:

| Check | Result |
|---|---|
| Step 0 red-proof in the fold (pre-change, extended roots) | exit 1, exactly `internal/block/block.go:66,86` — copy faithful |
| Step 1: `go build ./...` | exit 0 |
| Step 1: `go test ./internal/notification/...` (**zero test-file edits**) | ok (0.011 s) |
| Step 1: dispatcher diff | exactly the design's D-2 diff: import line removed, local `BlockChecker` added, field `:17` + option `:23` type changed; `Handle` byte-identical |
| Step 2: `go build ./...`, `go vet ./...` after `rm -r internal/block` | exit 0, exit 0 |
| Step 2: AC-2.1 grep | 0 matches |
| Step 3: `make check-audit-swallow` | ✓ green, root list includes `internal/block` |
| Step 3: extended-roots gate run | ✓ green (missing-dir root scanned as empty, N-4) |
| Step 3: AC-4.1 / AC-4.2 | 0 stale citations; `service_share.go:45`, `service.go:431`, `service.go:289` each exactly 1 |
| Step 3 (sweep follow-up, S-2 / AC-4.3) | The fold validated the pre-sweep AC-4.1; the strengthened AC-4.1 additionally requires the audit.go **package brace list** edit (Step 3.4) and AC-4.3 requires the AGENTS.md:333 edit (Step 3.5) — one line each, same text-anchor method |
| Step 4: `make test` | all packages ok |
| Step 4: `make lint` / `check-file-sizes` / `check-anchored-event-ids` / `check-audit-manifest` | all ✓ |
| Step 4: `make check-integration-compile` | exit 0 (integration-tagged tests compile; none reference the deleted package) |

The fold was removed after verification; the working tree is untouched (only this design doc was added).

## 5. Testable acceptance mapping

| Direction acceptance (verbatim, T-10.2) | Requirements AC | Testable form (this design) | Gate |
|---|---|---|---|
| repository-wide grep for `_ = .*\.(Log\|Record\|Publish\|FinishPublication\|ClaimPublication)(` over internal/ returns 0 matches | Not gateable by this direction — 4 sites remain (E2); compound target of sibling directions (§6) | Repo-wide P1 grep must yield exactly the 4 remaining sites after the change | AC-4.2 (enumeration) + §6 dependency |
| …or the gate roots extended to include internal/block with 0 matches | REQ-3: AC-3.1, AC-3.2 | `make check-audit-swallow` exit 0 with `internal/block` in the root list; pre-change red-proof (Step 0) lists exactly :66,:86 | AC-3.1/AC-3.2(b) |
| go build ./... passes with internal/block removed | REQ-1: AC-1.1..AC-1.3 | `test ! -d internal/block`; `go build ./...` exit 0; `go vet ./...` exit 0; `make check-integration-compile` green | AC-1.1/AC-1.2/AC-1.3 |
| no non-test package imports github.com/aero/aero-id/internal/block (grep -rln over internal/ cmd/) | REQ-2: AC-2.1..AC-2.3 (strengthened: no package of any kind) | `grep -rln "github.com/aero/aero-id/internal/block" --include='*.go' internal cmd` → 0 matches; `go test ./internal/notification/...` ok with zero test edits; `go vet ./internal/notification/...` exit 0 | AC-2.1/AC-2.2/AC-2.3 |
| T-10.2 red-proof: gate self-test fixtures still fail as expected | REQ-3: AC-3.2(a) | Script self-tests: `positive/fixture.go:7` fires P1, `negative/` fires nothing, `cmd_positive/cmd` fires under the `cmd/` root; extended-roots scan at pre-change commit **P** (committed gate wave, §4.2) exits 1 listing exactly :66,:86 (Step 0 capture re-taken at P's HEAD — reproducible from history, not the dirty tree) | AC-3.2(a)/AC-3.2(b) |
| (spec-added) no stale citations | REQ-4: AC-4.1..AC-4.3 | `grep -rn "internal/block\|block.go:66\|comment,block\|block,notification" internal/domain/audit.go scripts/check-audit-swallow.sh` → 0 (literal + brace forms); `grep -n "comment,block" AGENTS.md` → 0; note enumerates exactly `service_share.go:45`, `service.go:431`, `service.go:289` (one match each) | AC-4.1/AC-4.2/AC-4.3 |

## 6. Out of scope and dependencies

- **Compound gate dependency (not a gate here):** the 4 remaining swallow sites (`internal/file/service_share.go:45,75`, `internal/comment/service.go:431`, `internal/message/service.go:289`) are the subject of sibling directions; the repo-wide-zero branch of T-10.2 is their compound target. Until they land, the quarantine note must keep enumerating them — which is exactly why REQ-4 keeps the count at "Four" rather than deleting the note.
- **Sibling direction (independent):** `docs/requirements/2026-08-08-internal-block-fail-closed-wiring-requirements.md` covers the live block pair (fail-closed `NewBlockRepository` wiring, remove `WithBlockAuditLogger` gate). Neither direction depends on the other; this one cannot conflict (different files: `internal/service/block.go` vs the whole `internal/block/`).
- **Out of scope by design:** `internal/notification` deletion (also unwired, separate decision; this change only severs its import), migrations/proto/config/handlers (none exist for the dead package), historical docs (N-3 — dated records stay untouched per the D-4 adjudication in D-4). **AGENTS.md:333 is in scope** — the D-4 sweep ruling promotes it from 'recommended, non-gated' to a mandatory REQ-4 item (AC-4.3): AGENTS.md is living, undated guidance, so its `block` enumeration is a false citation post-deletion, and this design already treats the script's identical brace-list edit as mandatory. N-1 corrected: the bullet is at :333, not :332 — the draft's 'off-by-one' was itself a mis-correction.
