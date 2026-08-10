# Requirements — internal/audit: quarantine the dead-tree module and gate live roots against importing it

> **Module:** `internal/audit` (unwired parallel tree — deletion direction; the live audit machinery in `internal/service` + `internal/repository` is NOT touched)
> **Date:** 2026-08-08
> **Direction (selected, value 9 / risk 8 / effort 2 / confidence 9):** "Quarantine the dead-tree internal/audit module and gate live roots against importing it"
> **Source analysis:** `docs/auto/analyses/internal-audit-9b2d0397.json` (direction 1 of 3)
> **Method:** every cited file/symbol re-verified against the working tree at HEAD `b005409` (line numbers below are from the current tree; corrections to the direction's citations are recorded in §4, C1–C3). The sibling directions (events-endpoint 403 classification; `assertAuditWiring` coverage of operation/syncJob/eventIngest) are out of scope here. Precedent for this quarantine shape: `docs/requirements/2026-08-08-internal-block-dead-tree-quarantine-gap.md` (deletion + gate-tripwire + FM-12 standing existence guard).

---

## 1. Problem statement (verified)

`internal/audit` is the last unwired package in the audit family: a second, bare audit write path that nothing prevents from being wired tomorrow.

1. **The package is a bare second write path.** `internal/audit/repository.go` defines `Repository{Append, Log}` (`:15-19`) and `Append` (`:57-69`) inserts exactly one `audit_events` row (`auditEventRow`, `TableName` = `"audit_events"` at `:27`) via `r.db.WithContext(ctx).Create(row)` (`:65-67`). There is **no `audit_publications` outbox row, no hash chain, no signing** — contrast the live `repository.NewAuditLedgerRepository` (`internal/app/audit.go:71`), whose `appendInTx`/`createAuditRows` commit the ledger fact AND a `status='pending'` `audit_publications` row atomically. If any composition root ever wired `internal/audit`, facts would be appended outside the `drainPublications`-only delivery contract (B2-1): they would never reach governance sinks and would be invisible to every publication/replay mechanism.
2. **Nothing enforces the unwiring today.** (a) `scripts/check-audit-swallow.sh` documents the exclusion: "the unwired parallel tree internal/{file,message,comment,notification,search,audit} — only the live layer is scanned" (`:34-39`); the Makefile gate roots (`Makefile:67-69`) are `internal/service internal/repository internal/app internal/storage internal/block cmd` — a swallowed or bare `Log`/`Record` reintroduced in `internal/audit` would be gate-invisible (B2-2 violation). (b) `make check-architecture` polices only the dead tree (`FEATURES=(file message comment block notification search)`, `scripts/check-architecture.sh:12`; the deleted `internal/block` entry survives only via the `[ -d "$dir" ] || continue` guard) and its `ALLOWED_SHARED` list **explicitly permits** `"$INTERNAL/audit"` (`:25`) — dead-tree features may import it without a violation; live roots are not scanned at all. (c) AGENTS.md documents both facts verbatim (`:333` gotcha: "unwired parallel tree"; the check-architecture policing caveat).
3. **Today the package is provably dead code.** Repository-wide `grep -rln '"github.com/aero/aero-id/internal/audit"' --include='*.go' .` → **0 matches** (test and non-test). The only files in the package are `repository.go` (69 lines) and `repository_test.go` (64 lines, `package audit`, self-contained `domainToRow` test). The live composition root wires `repository.NewAuditRepository` (legacy read/verify + purge surface, `new.go:130`, `:326`) and `repository.NewAuditLedgerRepository` (durable ledger, `internal/app/audit.go:71` via `buildAuditStack` at `new.go:131`) — never `internal/audit`.
4. **Residual risk is a standing hole, not a current defect.** Zero importers are verified today (§2 E4), but the exclusion in (2) means a future wiring regression — a swallowed or bare audit write in a recreated/re-wired `internal/audit` — would be gate-invisible (violating B2-2) and the facts would never reach governance (violating B2-1's only-delivery-path invariant). The `internal/block` dead-tree quarantine established the closing pattern (delete + extend the gate + FM-12 standing existence guard); this direction applies it to the last remaining audit-family dead tree.

---

## 2. Evidence verification

| # | Direction's claim | Verification result (current tree) |
|---|---|---|
| E1 | `internal/audit/repository.go:53-61` — `Append` → `r.db.Create(row)`, no publication row | ⚠️ **C1 — span correction.** `Append` is at `:57-69`; the bare `r.db.WithContext(ctx).Create(row)` is at `:65-67`. Lines `:53-61` cover the `Log` error branch tail through `Append`'s ID/CreatedAt defaults. Content claim confirmed exactly: one `audit_events` insert, no `audit_publications` row, no hash chain, no signing. |
| E2 | `scripts/check-audit-swallow.sh` documents the exclusion of `internal/audit` | ✅ Confirmed at `:34-39` ("the unwired parallel tree internal/{file,message,comment,notification,search,audit} — only the live layer is scanned"); Makefile gate roots at `Makefile:67-69` exclude it. The script's `:44` sentence also names "the dead-tree internal/audit type". |
| E3 | AGENTS.md 'Current repository gotchas' — check-architecture polices the dead tree, not the live runtime | ✅ Confirmed at `AGENTS.md:333` ("`internal/{file,message,comment,notification,search}` and `internal/audit` are an unwired parallel tree …"); the check-architecture policing caveat is in the same gotcha bullet. `scripts/check-architecture.sh:12` scans only `FEATURES=(file message comment block notification search)`; `:25` allows `"$INTERNAL/audit"` imports from them. |
| E4 | Zero importers of `internal/audit` | ✅ Confirmed: `grep -rln '"github.com/aero/aero-id/internal/audit"' --include='*.go' .` → 0 matches across test and non-test code. The only dependent is `internal/audit/repository_test.go` itself (in-package, `package audit`). |
| E5 | `internal/app/new.go:131-160` — live stack wires `repository.NewAuditLedgerRepository`, never `internal/audit` | ⚠️ **C2 — nuance.** `new.go:131` calls `buildAuditStack(...)`; the `NewAuditLedgerRepository` call is inside it (`internal/app/audit.go:71`), and `auditLedgerRepo` is passed to file/message/comment/block/ban/admin/account/event-receipt/operation/sync-job repos at `new.go:138-160`. Additionally `new.go:130` wires `repository.NewAuditRepository` — the **live legacy** `audit_events` read/verify + TTL-purge surface (`internal/repository/audit.go`, used at `new.go:326`). Neither is `internal/audit`. |

Additional verified facts that constrain the design:

- **F1 — `domain.AuditEvent`/`AuditLogger` are shared, not dead-tree-only.** The quarantine note at `internal/domain/audit.go:15-19` ("QUARANTINED DEAD TREE — do not wire as-is … internal/{file,message,comment,notification,search,audit}") describes the interface's *users*, but the live layer also uses these types: `service.AuditLogger` (`internal/service/service.go:100`) and the `auditLedgerService.Log` adapter (`internal/service/audit_ledger.go:19,146`), plus `repository.AuditRepository` (`internal/repository/audit.go:18-19`). **Deleting the package must not delete the domain types**; only the package's brace-list mention (`internal/domain/audit.go:17`) is updated.
- **F2 — the dead `internal/audit.Repository` duplicates the live `repository.AuditRepository`.** Same `Append`/`Log` method set on `*domain.AuditEvent`; the live one additionally offers `List`/`PurgeOlderThan` and is wired (`new.go:130`, `:326`). No capability is lost by deletion.
- **F3 — no migration, proto, config, or handler surface exists for `internal/audit`.** The `audit_events` table is shared with the live legacy surface and is untouched. `make proto` is not involved.
- **F4 — `internal/handler` is NOT a current swallow-scan root** (`Makefile:69`: `internal/service internal/repository internal/app internal/storage internal/block cmd`), but the direction's import-ban root list includes it. The ban therefore needs its own fixed root list (§3 REQ-2), independent of the script's `ROOTS` argument — matching the FM-12 precedent of script-body policy that direct invocations cannot bypass.
- **F5 — baseline is green today (captured 2026-08-08):** `go build ./...` → exit 0; `go test ./internal/audit/... -count=1` → ok; `bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd` → exit 0 with the `✓ quarantine guard: internal/block absent (FM-12)` and `✓ check-audit-swallow: no swallowed or bare-call audit writes` echoes. The import ban over the six live roots matches **0 lines today** (verified), so the ban is green at P; its red-proof comes from the fixture self-test and the mutant probe (§3 AC-2.2).
- **F6 — in-flight working tree:** a separate campaign has uncommitted edits to `scripts/check-audit-swallow.sh` (MarkDelivery in P1/P2 patterns + fixtures) and `internal/app/audit.go`. The REQ-2 sections (import ban + existence guard) are new script blocks that do not overlap those patterns; implement on top of the merged working tree. Nothing else in this spec touches the in-flight files.

---

## 3. Requirements and acceptance

### REQ-1 — Delete the `internal/audit` package outright

Remove both files: `internal/audit/repository.go` and `internal/audit/repository_test.go`. No code is moved or folded into any other package — the live `repository.AuditRepository` (`internal/repository/audit.go`) and the durable `repository.AuditLedgerRepository` already cover both the legacy and the ledger surfaces (E5, F2). `domain.AuditEvent`/`domain.AuditLogger` stay (F1).

**Acceptance**

- AC-1.1: `test ! -d internal/audit` exits 0; `git status` shows no `internal/audit/` entries.
- AC-1.2: `go build ./...` exits 0 (the direction's "delete internal/audit … keep make test + make check-audit-swallow green").
- AC-1.3: `go test ./... -count=1` (unit) exits 0 — the only lost tests are the package's own `domainToRow` test; nothing outside the package referenced it (E4). `make lint`, `make lint-strict`, `make check-file-sizes`, `make check-integration-compile` stay green (the package has no integration-tagged files, so the compile gate is unaffected).

### REQ-2 — Extend `scripts/check-audit-swallow.sh` with an import ban and a standing existence guard

Three additions to the script (this is the direction's "extend check-audit-swallow (or a sibling gate)" branch):

1. **Import ban over the six live roots.** New section that greps for the literal import `"github.com/aero/aero-id/internal/audit"` over a fixed root list `internal/service internal/repository internal/app internal/handler internal/storage cmd` — the direction's list verbatim, including `internal/handler` (F4). The ban uses its own hardcoded roots, independent of the script's `ROOTS` argument, so direct invocations (default `internal/service`) cannot bypass it. Any match prints `QUARANTINE VIOLATION: internal/audit must not be imported from live roots:` + the matching lines and sets `fail=1`.
2. **Standing existence guard (FM-12 pattern).** `if [ -d "$SELF_DIR/../internal/audit" ]; then echo "QUARANTINE VIOLATION: internal/audit must not exist (dead-tree quarantine, FM-12)"; fail=1; else echo "✓ quarantine guard: internal/audit absent (FM-12)"; fi` — sibling to the existing `internal/block` guard. **M-only**: it lands in the same commit as REQ-1; at P it would red because the package legitimately exists. This closes the benign-reintroduction boundary proven in the block campaign (compilable package + working import + propagated audit error defeats the import ban and the compile tripwire together).
3. **Self-test fixture proving the ban pattern works.** New fixture `scripts/testdata/audit-swallow/import_positive/fixture.go` containing the literal banned import string (a non-compiling snippet, like the existing fixtures). The script's self-test block asserts `grep -rn '"github.com/aero/aero-id/internal/audit"' "$FIXTURES/import_positive"` matches ≥ 1, so green provably means the ban scanned something. The existing positive/negative/cmd_positive fixtures are untouched.

**Acceptance**

- AC-2.1: `make check-audit-swallow` exits 0 and prints `✓ quarantine guard: internal/audit absent (FM-12)` and `✓ import ban: no live-root imports of internal/audit` alongside the existing echoes.
- AC-2.2 (**red-proof, both parts**):
  - (a) fixture self-test: `grep -q '"github.com/aero/aero-id/internal/audit"' scripts/testdata/audit-swallow/import_positive/fixture.go` exits 0, and the script's self-test block exits 0 (ban pattern proven live).
  - (b) mutant probe (scratch copy of M): create `internal/audit` as a compilable package with a working import from `internal/service` and a propagated audit error → `go build ./...` exits 0 but `make check-audit-swallow` exits 1 listing both the import line and the `QUARANTINE VIOLATION: internal/audit must not exist` guard (design §4.3 probe-5 shape from the block precedent).
- AC-2.3 (**P-phase green and M-only guard**): at P (before REQ-1 lands), the ban section alone is green — `grep -rn '"github.com/aero/aero-id/internal/audit"' internal/service internal/repository internal/app internal/handler internal/storage cmd --include='*.go'` → 0 matches (verified, §2 F5) — but the existence guard must NOT be in the script yet (at P it reds; it lands atomically with REQ-1). After M, `grep -q "internal/audit must not exist" scripts/check-audit-swallow.sh` exits 0.

### REQ-3 — Repository-level zero-importer test (`go list` assertion)

Add `internal/repository/audit_quarantine_test.go` (package `repository`, unit tag) per the direction's "add a repository-level test asserting go list shows zero importers (proposed until the gate/delete lands)". The test runs:

```
go list -f '{{.ImportPath}}' -deps github.com/aero/aero-id/internal/service/... \
  github.com/aero/aero-id/internal/repository/... github.com/aero/aero-id/internal/app/... \
  github.com/aero/aero-id/internal/handler/... github.com/aero/aero-id/internal/storage/... \
  github.com/aero/aero-id/cmd/...
```

with `cmd.Dir` set to the repository root (derived from `runtime.Caller(0)` — the test lives at `internal/repository/`, so root is `../..`), and asserts no output line equals `github.com/aero/aero-id/internal/audit`.

**Landing order (mutant-first):** the test lands **before** REQ-1 (at P). It is green at P (0 importers today, E4), and it is the *only* check that goes red the moment a live package imports `internal/audit` while the package still exists — the transitional tripwire the direction asks for. After REQ-1 it stays green (the deleted path cannot appear in any live root's dep graph) and is retained as belt-and-suspenders coverage alongside the standing gate (REQ-2).

**Acceptance**

- AC-3.1: at P, `go test ./internal/repository/ -run TestNoLiveRootImportsInternalAudit -count=1` exits 0.
- AC-3.2 (**mutant red**): on a scratch copy at P, add one file under `internal/service/` importing `"github.com/aero/aero-id/internal/audit"` → the same test exits 1 and names the package. Remove the mutant → green again.
- AC-3.3: at M (after REQ-1), the test still exits 0, and `make test` is green with it in place.

### REQ-4 — Update the quarantine documentation that names the deleted package

Same class as the block quarantine's REQ-4 (D-4 sweep ruling: working-tree files whose claims become false must be updated; dated records stay historical):

- `scripts/check-audit-swallow.sh:34-35` — exclusion comment: `internal/{file,message,comment,notification,search,audit}` → `internal/{file,message,comment,notification,search}`; `:44` — the "carve-out refers to the dead-tree internal/audit type" sentence reworded to past tense (the type is deleted; the carve-out is historical).
- `internal/domain/audit.go:17` — quarantine note brace list: `internal/{file,message,comment,notification,search,audit}` → `internal/{file,message,comment,notification,search}`.
- `AGENTS.md:333` — gotcha bullet drops ` and internal/audit`: `internal/{file,message,comment,notification,search} are an unwired parallel tree (…)`.
- `scripts/check-architecture.sh:6` (header comment) and `:25` (`ALLOWED_SHARED` entry) — drop the `internal/audit` mention/entry: after deletion the allow-list entry is a dangling citation for a package that cannot exist (a dead-tree feature importing it would not compile regardless).

**Acceptance**

- AC-4.1: stale enumeration forms return 0 matches: `grep -n "search,audit" scripts/check-audit-swallow.sh internal/domain/audit.go` → 0; `grep -n "and internal/audit" AGENTS.md` → 0; `grep -n "internal/audit" scripts/check-architecture.sh` → 0 (both the header comment at `:6` and the `ALLOWED_SHARED` entry at `:25` are stale citations — no intentional text remains in that script, since REQ-2's guard lives in `check-audit-swallow.sh`). The script's remaining `internal/audit` literals are the REQ-2 guard's own prescriptive text (violation message, comments) plus the past-tense historical carve-out reference — intentional, pinned by AC-2.3.
- AC-4.2: `grep -n "internal/audit" scripts/check-audit-swallow.sh internal/domain/audit.go AGENTS.md scripts/check-architecture.sh` shows only the intentional guard/comment text (AC-4.1 forms absent).

---

## 4. Corrections to the direction's citations (verified against the tree)

| # | Direction citation | Current tree | Impact |
|---|---|---|---|
| C1 | `internal/audit/repository.go:53-61` (Append → Create) | `Append` at `:57-69`; bare `Create` at `:65-67`; `:53-61` is Log-tail + Append defaults | Cosmetic — the claim (bare `audit_events` insert, no publication row, no chain, no signing) is fully confirmed. |
| C2 | `internal/app/new.go:131-160` wires `repository.NewAuditLedgerRepository` | `new.go:131` calls `buildAuditStack`; the constructor is at `internal/app/audit.go:71`; `auditLedgerRepo` flows to repos at `new.go:138-160`. `new.go:130` additionally wires the live legacy `repository.NewAuditRepository` | Cosmetic — "never `internal/audit`" confirmed; the live legacy surface is a distinct package that stays. |
| C3 | (implicit) `internal/audit` is the only unwired audit-family package | Confirmed — `internal/block` was deleted at HEAD `b005409`; the remaining brace list is `internal/{file,message,comment,notification,search,audit}` | No change to the direction; REQ-4 drops `audit` from that list. |

No citation was found to be materially wrong; none changes the direction's conclusion.

---

## 5. Supplied acceptance checks, preserved and made testable

The direction's acceptance, verbatim:

> T-5: extend check-audit-swallow (or a sibling gate) to fail on any import of github.com/aero/aero-id/internal/audit from internal/{service,repository,app,handler,storage,cmd} — gate red on a reintroduced import; delete internal/audit (only repository_test.go depends on it) and keep make test + make check-audit-swallow green; add a repository-level test asserting go list shows zero importers (proposed until the gate/delete lands).

| Supplied check | Testable form in this spec | Where |
|---|---|---|
| extend check-audit-swallow (or a sibling gate) to fail on any import from the six live roots | REQ-2: import-ban section in `scripts/check-audit-swallow.sh` with fixed roots `internal/service internal/repository internal/app internal/handler internal/storage cmd` (the primary branch; the gate is the natural home — it already owns audit-write policy and self-test fixtures) | §3 REQ-2 |
| gate red on a reintroduced import | AC-2.2(b) mutant probe: compilable recreated package + working import + propagated error → `go build` green, gate exit 1 listing the import AND the existence-guard violation; AC-2.2(a) fixture self-test proves the ban pattern is live | §3 REQ-2 |
| delete internal/audit (only repository_test.go depends on it) | REQ-1 + AC-1.1 (dir absent), AC-1.2 (`go build ./...` green); the "only repository_test.go depends on it" claim verified as E4 (zero external importers; `repository_test.go` is in-package) | §3 REQ-1, §2 E4 |
| keep make test + make check-audit-swallow green | AC-1.3 (`go test ./... -count=1`, lint/lint-strict/check-file-sizes/check-integration-compile), AC-2.1 (gate exit 0 with both new echoes) | §3 REQ-1/REQ-2 |
| add a repository-level test asserting go list shows zero importers (proposed until the gate/delete lands) | REQ-3: `internal/repository/audit_quarantine_test.go` using `go list -f '{{.ImportPath}}' -deps` over the six roots; lands at P (transitional tripwire, mutant-first AC-3.2), retained at M (AC-3.3) | §3 REQ-3 |

**Why the go-list test alone is not the standing gate:** after REQ-1, a benign reintroduction (package recreated, import present, error propagated) is caught by the REQ-2 existence guard + import ban; the go-list test is the transitional pre-deletion tripwire the direction's parenthetical describes and remains as redundant-with-gate coverage. The direction's "(proposed until the gate/delete lands)" is honored by the landing order: test first (P), gate + deletion second (M).

---

## 6. Constraints and dependencies

- **No runtime effect.** `internal/audit` is unreachable from any composition root (E4). `cmd/server`, `internal/app/new.go`, and all live services/repositories are untouched. `domain.AuditEvent`/`domain.AuditLogger` stay (F1); the live `repository.AuditRepository` legacy surface (`new.go:130`) is untouched — its own bare `audit_events` writes are the documented legacy surface, out of scope for this direction.
- **No migrations, proto, config, or handlers.** The shared `audit_events` table is unaffected. `make proto` is not involved.
- **M-only guard.** The REQ-2 existence guard lands atomically with REQ-1 (at P it reds because the package legitimately exists — same rule as the block campaign's FM-12 guard). The import ban and REQ-3 test can land at P.
- **Sibling directions (out of scope):** the other two directions from `internal-audit-9b2d0397.json` (events-endpoint 403 classification; `assertAuditWiring` coverage for operation/syncJob/eventIngest) are independent; neither depends on this deletion. The remaining dead-tree swallow sites (`internal/file/service_share.go:45,75`, `internal/comment/service.go:431`, `internal/message/service.go:289`) are quarantined exactly as today (the `internal/domain/audit.go:15-19` note's "Four `_ = s.audit.Log(` swallow sites" stays true; only the brace list changes, REQ-4).
- **In-flight working tree (F6):** the uncommitted MarkDelivery gate work in `scripts/check-audit-swallow.sh` and `internal/app/audit.go` belongs to another campaign; REQ-2 adds new blocks (ban + guard + fixture) that do not overlap it. Implement on top of the merged tree; re-run `make check-audit-swallow` after merge.
- **Historical-record sweep (D-4):** no edit to `docs/auto/**` (immutable pipeline artifacts), dated requirements/designs that cite `internal/audit` as of 2026-07-10/11 or the 08-06 in-tx-audit spec (their "legacy unwired tree" statements are verification-time records), or `docs/results/`. The four REQ-4 sites are the mandatory updates.

---

## 7. Done gate for this direction

```bash
make test && make lint && make lint-strict && make check-file-sizes && \
make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && \
make check-integration-compile && make build
```

plus the direction-specific assertions:

```bash
test ! -d internal/audit                                              # AC-1.1
grep -rln '"github.com/aero/aero-id/internal/audit"' --include='*.go' internal/service internal/repository internal/app internal/handler internal/storage cmd   # → 0 matches (AC-2.3, REQ-2 ban)
grep -q '"github.com/aero/aero-id/internal/audit"' scripts/testdata/audit-swallow/import_positive/fixture.go   # → exit 0: ban fixture present (AC-2.2.a)
grep -q "internal/audit must not exist" scripts/check-audit-swallow.sh   # → exit 0: FM-12 guard present (AC-2.3)
bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage cmd   # → exit 0, incl. ✓ quarantine guard: internal/audit absent + ✓ import ban echoes (AC-2.1)
go test ./internal/repository/ -run TestNoLiveRootImportsInternalAudit -count=1   # → ok (AC-3.1/AC-3.3)
grep -n "search,audit" scripts/check-audit-swallow.sh internal/domain/audit.go    # → 0 matches (AC-4.1)
grep -n "and internal/audit" AGENTS.md                                            # → 0 matches (AC-4.1)
grep -n "internal/audit" scripts/check-architecture.sh                          # → 0 matches (AC-4.1)
```
