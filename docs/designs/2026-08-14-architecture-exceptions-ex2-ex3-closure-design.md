# Design — Close EX-2 and EX-3 Architecture-Gate Exceptions

**Date:** 2026-08-14
**Status:** Approved for implementation
**Module labels:** `scripts/check-architecture.sh`, `internal/domain`
**Baseline:** HEAD `aa42385` (clean tree, pre-closure state)
**Inputs:** requirements spec from the EX-2/EX-3 closure campaign (untrusted evidence,
independently re-verified against the tree at HEAD; see §2 for the verdict and the
one evidence gap found).

## 1. Problem statement

`scripts/check-architecture.sh` currently carries two time-boxed exceptions:

- **EX-2** — handlers may import `internal/repository` for exactly two enum
  type-casts (`LegacyCutoverStatus` in `http/account.go`, `AuditHoldScope` in
  `http/audit_ledger.go`), enforced by two allow-lists (`ALLOWED_HANDLER_FILES`,
  `ALLOWED_REPO_IDENTS`) and a second rule (e2) that scans `repository.<Ident>`
  usage.
- **EX-3** — `internal/domain/audit.go` imports `github.com/google/uuid` for the
  live `AuditEvent.ID` field, enforced by an exact-string exception in
  `classify_domain_import`.

Both have script-header TODOs to relocate the enum types into `internal/domain`
and switch `AuditEvent.ID` to the in-domain ID alias, then delete the exceptions.
This design closes both: `internal/domain` becomes pure stdlib + `internal/pkg/*`
with zero exceptions, and `internal/handler/` gets an absolute repository import
ban (rule e2 deleted — vacuous once e1 is absolute).

## 2. Verification verdict on the input evidence

All claims in the requirements spec were re-verified against the tree at HEAD
`aa42385`. Verdict: **Verified with one gap** (see below).

| Claim | Check | Result |
|---|---|---|
| Rule (e1) allow-list is exactly `http/account.go` + `http/audit_ledger.go` | `scripts/check-architecture.sh:36-39,84-87,234-240` | ✅ |
| Rule (e2) ident allow-list `LegacyCutoverStatus`/`AuditHoldScope` | `:40-44,88,248-271` | ✅ |
| EX-2/EX-3 header blocks with relocate TODOs | `:50-63` | ✅ |
| `domain.ID = idgen.ID = uuid.UUID` (alias chain) — `AuditEvent.ID uuid.UUID → ID` is type-identical | `internal/domain/shared.go:6`, `internal/pkg/idgen/idgen.go:8` | ✅ |
| Enum definitions + constant string values (`"legacy"/"shadow"/"cut_over"/"rolled_back"`, `"tenant"/"account"/"actor"`) | `internal/repository/legacy_cutover.go:16-37`, `audit_hold.go:16-22` | ✅ |
| Handler cast sites `account.go:121`, `audit_ledger.go:298,309`; both already import `domain` | read + grep | ✅ |
| Service users and test users; all six test files already import `internal/domain` | grep | ✅ |
| `repository/audit.go:43-44` uses `uuid.UUID{}`/`uuid.New()` against `AuditEvent.ID` (compiles via alias) | read | ✅ |
| Fixture topology: `negative/enum_cast` (green today), `negative/domain_imports_uuid`, `positive/{handler_calls_repo_ctor,domain_imports_gorm}`, `negative/{chain,hub_assertion,service_connector}` all as claimed | read + baseline script run | ✅ |
| Baseline script: 6 self-test rules (a,b,c,d,e1,e2), all green | `bash scripts/check-architecture.sh` | ✅ |
| HARNESS.md:27-28 and AGENTS.md:351 open-state wording; Makefile `check-architecture` target | read | ✅ |
| Done-gate targets all exist in Makefile | grep | ✅ |

**Gap found (evidence incomplete):** `internal/repository/audit.go:168` uses the
unqualified same-package identifier `AuditHoldScopeActor`
(`exempt, _ = holds.HoldScopeIDs(ctx, AuditHoldScopeActor)`). The evidence's
REQ-3 self-user list covers only `legacy_cutover.go` and `audit_hold.go`, and its
acceptance greps (`repository\.(LegacyCutoverStatus|AuditHoldScope|...)`) cannot
detect unqualified same-package uses — only `go build` catches the miss. This
site must be rewritten to `domain.AuditHoldScopeActor` (the file already imports
`internal/domain` at :12), and the implementation must add a completeness sweep
(§5 step 0) for *all* unqualified uses, not only the qualified ones the evidence
lists.

**Minor wording nit:** REQ-9's header says "all five files" but lists six test
files; the enumerated list itself is complete and correct. No action needed
beyond counting correctly in the implementation checklist.

## 3. Goals and non-goals

**Goals**

- `internal/domain` imports only stdlib + `internal/pkg/*`; delete the exact-string
  `google/uuid` exception (rule (b) stays byte-identical in mechanics).
- `internal/handler/` may not import `internal/repository` at all; delete the
  allow-lists and rule e2.
- Relocated domain types are pure (zero imports): `LegacyCutoverStatus` (+4
  constants + `ValidCutoverTransition`) and `AuditHoldScope` (+3 constants).
- `AuditEvent.ID` becomes the in-package `ID` alias (`ID = idgen.ID = uuid.UUID`)
  — type-identical; zero behavioral change (DB columns, JSON, ledger signing,
  anchored event IDs).
- Fixtures reworked so the self-test sweep still proves every surviving rule
  non-vacuous (positive ≥1 hit) and not over-broad (negative 0 hits).
- Full done-gate green.

**Non-goals / out of scope**

- Rules (a)/(c)/(d) and their fixtures; EX-1 (`hub_assertion`); the `hub`
  exclusion — byte-identical.
- The `check-audit-swallow` gate, Makefile tripwires, FM-12 guards —
  byte-identical.
- Protobuf, migrations, GORM models, anchored-ID derivations — no change.
- `internal/repository/audit.go`'s `uuid` import (remains valid via the alias).
- `docs/designs/2026-08-10-*-quarantine-design.md` (historical record; the
  evidence correctly excludes it from rewording).
- No new packages, binaries, or public API surfaces.

## 4. API changes and compatibility constraints

There is **no wire-level or storage-level API change**; this is a Go symbol
relocation. Compatibility analysis per surface:

| Surface | Impact | Why |
|---|---|---|
| DB schema / GORM | None | `LegacyCutover.Status` and `AuditHold.ScopeType` keep the same string-based named types (now `domain.*`); GORM stores text columns identically. No migration. |
| HTTP/JSON | None | Named string types marshal identically; `AuditEvent.ID` is still `uuid.UUID` after alias resolution (marshals as the same string). |
| Ledger signing / hashing | None | Byte content of `AuditEvent` unchanged; `uuid.UUID{}`/`uuid.New()` in `repository/audit.go:43-44` compile unchanged against the alias. |
| Anchored event IDs | None | `string(domain.AuditHoldScopeTenant)` == `"tenant"` — the `idgen.Anchor` derivation input is value-identical, so `check-anchored-event-ids` stays green. |
| Go package surface | `repository.LegacyCutoverStatus`, `repository.AuditHoldScope`, `repository.ValidCutoverTransition` disappear from the `repository` package | Internal monorepo; no external consumers. In-package `internal/` callers are all migrated. No deprecation shim (a shim would violate the REQ-3 acceptance `grep 'type ... string' internal/repository/*.go` → empty). |
| `domain` package surface | Adds `LegacyCutoverStatus` (+4 consts + `ValidCutoverTransition`) and `AuditHoldScope` (+3 consts); `AuditEvent.ID` type changes from `uuid.UUID` to `ID` (identical type) | Pure additions; the `ID` field is an in-package alias, matching the `comment.go`/`export.go`/`shared.go` pattern. |

Constraints the implementation must honor:

1. **Purity (constraint a):** both new `internal/domain` files must have zero
   imports — no gorm, no uuid, no internal imports. `ValidCutoverTransition`
   moves with the type, verbatim.
2. **Type identity (constraint b):** `AuditEvent.ID uuid.UUID → ID` is an alias
   swap, not a new type. Every construction site is unaffected:
   `repository/db.go:286`, `service/admin.go:68`, `service/message.go:373`,
   `handler/grpc/admin.go:106` (+ the protobuf echo type `aeropb.AuditEvent`
   which is unrelated). Verified: none of these sites name the field type.
3. **Constants keep their exact string values** — they are load-bearing for DB
   rows and anchored-ID derivation.
4. **Interface conformance:** service interfaces and test fakes must switch to
   `domain.AuditHoldScope`/`domain.LegacyCutoverStatus` in the same change;
   `repository.AuditHoldRepository`/`AuditHoldAuditRepository` reference the
   domain types after REQ-3, so any fake left on the old symbol stops satisfying
   the interface (compile error — good).

## 5. Detailed change list (migration steps)

Order matters: relocate domain types first, then re-point consumers, then tests,
then the script + fixtures together (they are co-dependent), then docs, then gate.

**Step 0 — Completeness sweep (evidence gap fix).** Before/after editing, sweep
for *all* enum users, qualified or not:

```bash
grep -rn 'LegacyCutoverStatus\|AuditHoldScope\|ValidCutoverTransition' internal/ scripts/testdata/architecture/
```

This catches the unqualified site the evidence missed:
`internal/repository/audit.go:168` → `domain.AuditHoldScopeActor`.

**Step 1 — New domain files (REQ-1).** Create `internal/domain/legacy_cutover.go`
and `internal/domain/audit_hold.go`, moving the types, constants, and
`ValidCutoverTransition` verbatim from the repository files. Zero imports.
Constant values unchanged.

**Step 2 — `internal/domain/audit.go` (REQ-2).** Delete the `google/uuid`
import; change `ID uuid.UUID` → `ID ID`.

**Step 3 — Repository (REQ-3 + gap fix).**
- `internal/repository/legacy_cutover.go`: delete type + 4 consts +
  `ValidCutoverTransition` (:16-37); `LegacyCutover.Status` →
  `domain.LegacyCutoverStatus`; `legacyCutoverRowToDomain` (:155) →
  `Status: domain.LegacyCutoverStatus(row.Status)`. `string(cutover.Status)`
  in `setCutoverInTx` compiles unchanged.
- `internal/repository/audit_hold.go`: delete type + 3 consts (:16-22);
  `AuditHold.ScopeType` and all interface signatures
  (`PutHold`/`RemoveHold`/`HoldScopeIDs`/`RemoveHoldWithAudit`) →
  `domain.AuditHoldScope`; `auditHoldRowToDomain` (:190) →
  `ScopeType: domain.AuditHoldScope(row.ScopeType)`.
- **`internal/repository/audit.go:168`** (not in the evidence's REQ-3):
  `AuditHoldScopeActor` → `domain.AuditHoldScopeActor` (file already imports
  `domain`).

**Step 4 — Services (REQ-4).** Add the `internal/domain` import to
`internal/service/legacy_cutover.go` and `internal/service/audit_hold.go`;
replace every `repository.LegacyCutoverStatus` /
`repository.AuditHoldScope` / `repository.ValidCutoverTransition` with the
`domain.` form. `repository.LegacyCutover`,
`repository.LegacyCleanupRepository`, `repository.CleanupReadinessItem`,
`repository.AuditHold` stay as-is.

**Step 5 — Handlers (REQ-5).** Drop the `internal/repository` import from
`internal/handler/http/account.go` (:11) and `audit_ledger.go` (:14); casts →
`domain.LegacyCutoverStatus(...)` / `domain.AuditHoldScope(...)`.

**Step 6 — Tests (REQ-9).** Symbol-only renames in:
`internal/app/audit_integration_test.go` (:950,:955 — the `string(...)` at :955
keeps value `"tenant"`), `internal/repository/audit_hold_integration_test.go`,
`internal/repository/legacy_cutover_integration_test.go`,
`internal/repository/anchor_dedup_integration_test.go` (:371,:384),
`internal/service/audit_hold_test.go` (fakes at :36,:50,:132 must keep
satisfying the interfaces), `internal/service/legacy_cutover_test.go`.

**Step 7 — Script (REQ-6).** In `scripts/check-architecture.sh`, atomically:
1. Delete `ALLOWED_HANDLER_FILES`, `ALLOWED_REPO_IDENTS`, `UUID_EXCEPTION`.
2. Header rule (e) → single absolute assertion; delete EX-2 and EX-3 header
   blocks; keep EX-1 byte-identical.
3. `classify_domain_import`: delete the `"$UUID_EXCEPTION"` arm; reword the
   order comment.
4. Rule (b) message: drop `; see EX-3`.
5. `scan_handler_repo_import` → absolute rule (any non-test handler file
   importing repository; keep `! -name '*_test.go'`); delete
   `scan_handler_repo_idents` entirely.
6. Self-test block: keep `(e1) handler repo import`, delete `(e2)`; real-tree
   scan drops `scan_handler_repo_idents internal cmd`.
7. Leave rules (a)/(c)/(d), variables, selftest lines, summary echo, and the
   fixture-missing guard untouched (byte-identity, REQ-8).

**Step 8 — Fixtures (REQ-7).** Do this *in the same change* as Step 7 —
the script and fixture topology are mutually validating:
- `git mv negative/enum_cast → positive/enum_cast`; rewrite body to a plain
  repository-import violation (`repository.NewAuditHoldRepository(nil, nil)` —
  symbol exists at `repository/audit_hold.go:82`) with the "was deleted" prose.
- `git mv negative/domain_imports_uuid → positive/domain_imports_uuid`; rewrite
  comment; `google/uuid` now trips rule (b) as an ordinary third-party import.
- `positive/{handler_calls_repo_ctor,domain_imports_gorm}` and
  `negative/{chain,hub_assertion,service_connector}` unchanged (verified
  non-firing under the closed rules).
- Resulting sweep: positive fires (a) dead_tree_import, (b)
  domain_imports_gorm + domain_imports_uuid, (c) repository_imports_service,
  (d) connector_imports_service, (e1) handler_calls_repo_ctor + enum_cast;
  negative 0 hits under every rule.

Run `bash scripts/check-architecture.sh` immediately after this step — the
self-test sweep validates the new topology before the done gate.

**Step 9 — Docs (REQ-10/REQ-11).** HARNESS.md:27-28 and AGENTS.md:351 reworded
to the closed state (drop the `google/uuid` exception phrase and the
enum-cast wording; delete the `TODO: relocate ...` sentence in AGENTS.md).

**Step 10 — Done gate (REQ-12).** Full gate, §7.

## 6. Failure modes

| # | Failure | Symptom | Detection / mitigation |
|---|---|---|---|
| F1 | Missed same-package enum user (the `repository/audit.go:168` class) | `go build` fails: `undefined: AuditHoldScopeActor` | Step 0 sweep for unqualified identifiers, not just `repository.`-qualified ones; `make build` after Step 3. |
| F2 | Script edited but fixtures not moved (or vice versa) | SELF-TEST FAIL: negative sweep hits (rule went over-broad) or positive fires nothing (gate vacuous) | Steps 7+8 are one atomic change; run the script immediately after. |
| F3 | Fixture moved but rule e1 rewritten first | `negative/` sweep hits → exit 1 | Same as F2; the script is the tripwire. |
| F4 | Constant string value accidentally changed during the move | DB rows/anchored IDs silently diverge; `check-anchored-event-ids` may fail on the `"tenant"` derivation | Acceptance greps pin `"legacy"`/`"cut_over"`/`"tenant"`/`"actor"` values; `make check-anchored-event-ids`; integration tests. |
| F5 | `AuditEvent.ID` changed to a *new* type instead of the alias (e.g. `type ID uuid.UUID`) | `repository/audit.go:43-44` (`e.ID == (uuid.UUID{})`, `uuid.New()`) stops compiling; DB/JSON behavior changes | Constraint (b): must be the alias `type ID = idgen.ID` chain; `make build` + integration suite. |
| F6 | Deprecation shim left in `repository` (`type LegacyCutoverStatus = domain.LegacyCutoverStatus`) | REQ-3 acceptance grep fails; EX-2 could be resurrected later | No shim; acceptance `grep -n 'type \(LegacyCutoverStatus\|AuditHoldScope\) string' internal/repository/*.go` → empty. |
| F7 | Test fakes not updated in the same change | Fakes stop satisfying `repository.AuditHoldRepository` (now domain-typed) → compile failure in unit tests | Step 6 before the gate; `make test`. |
| F8 | e2 selftest/real-tree line accidentally kept | Acceptance grep (`scan_handler_repo_idents`) fails; script would still be green but with a dead rule | Delete in Step 7; final grep sweep. |
| F9 | Header/comment still references EX-2/EX-3 | Acceptance greps over script + HARNESS.md + AGENTS.md fail | Step 9; final evidence sweep. |
| F10 | `enum_cast` fixture keeps a `repository.AuditHoldScope` reference in its comment/body | The "was deleted" prose is allowed, but a *live-exception claim* would fail the fixture grep | REQ-7 fixture acceptance: only "was deleted" prose, no live-exception claims. |

No runtime failure mode exists: this change is compile-time-only, and the full
done gate plus (when Docker is available) `make test-integration` exercises the
moved enums against real Postgres.

## 7. Testable acceptance mapping

| # | Acceptance (command) | Expected |
|---|---|---|
| A1 | `sed -n '/^import/,/^)/p' internal/domain/legacy_cutover.go internal/domain/audit_hold.go \| grep -c '"'` | `0` (no import blocks) |
| A2 | `grep -n 'LegacyCutoverStatusLegacy.*"legacy"\|AuditHoldScopeActor.*"actor"' internal/domain/legacy_cutover.go internal/domain/audit_hold.go` | both present, values pinned |
| A3 | `grep -n 'google/uuid' internal/domain/*.go` | no output |
| A4 | `grep -n 'ID\s*ID\b' internal/domain/audit.go` | field declaration present |
| A5 | `grep -n 'type \(LegacyCutoverStatus\|AuditHoldScope\) string' internal/repository/*.go` | no output |
| A6 | `grep -rn 'repository\.\(LegacyCutoverStatus\|AuditHoldScope\|ValidCutoverTransition\)' internal/` | no output (test + non-test) |
| A7 | `grep -rn 'internal/repository' internal/handler/` | no output |
| A8 | `grep -n 'EX-2\|EX-3\|ALLOWED_HANDLER\|ALLOWED_REPO\|UUID_EXCEPTION\|scan_handler_repo_idents' scripts/check-architecture.sh` | no output |
| A9 | `bash scripts/check-architecture.sh` | exit 0; exactly FIVE `✓ self-test rule` lines (a,b,c,d,e1); final pass echo |
| A10 | `[ -f scripts/testdata/architecture/positive/enum_cast/internal/handler/http/audit_ledger.go ]` and `[ -f .../positive/domain_imports_uuid/internal/domain/fixture.go ]`; `[ ! -d .../negative/enum_cast ]` and `[ ! -d .../negative/domain_imports_uuid ]` | all true |
| A11 | `grep -rn 'EX-2\|EX-3' scripts/testdata/architecture/positive/enum_cast/ scripts/testdata/architecture/positive/domain_imports_uuid/` | only "was deleted" prose |
| A12 | `grep -n 'EX-2\|google/uuid exception\|enum casts' HARNESS.md` | no output |
| A13 | `grep -n 'google/uuid exception\|enum-cast-only\|TODO: relocate' AGENTS.md`; `grep -n 'handler→repository import ban' AGENTS.md` | first empty; second present |
| A14 | `git diff scripts/check-architecture.sh \| grep -cE '^[+-].*(scan_dead_tree_imports\|scan_repository_service\|scan_infra_service_repo\|selftest_rule "\(a\)\|selftest_rule "\(c\)\|selftest_rule "\(d\)"\|DEAD_TREES\|INFRA_LAYERS)'` | `0` (rules a/c/d byte-identical) |
| A15 | `git status --porcelain \| grep -vE 'internal/(domain|repository|service|handler)\|scripts/(check-architecture\.sh\|testdata/architecture)\|HARNESS\.md\|AGENTS\.md'` | no output (no stray changes) |
| A16 | `make check-integration-compile` | green (integration-tagged fakes compile) |
| A17 | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-composition-root && make check-proto-sync && make check-integration-compile && make build && make check-architecture` | all green |
| A18 | `make test-integration` (if Docker available) | green — exercises moved enums against real Postgres; no SQL changes expected |
| A19 | Final evidence sweep: `grep -rn 'EX-2\|EX-3' scripts/check-architecture.sh HARNESS.md AGENTS.md`; `grep -rn 'google/uuid' internal/domain/`; `grep -c 'self-test rule' <(bash scripts/check-architecture.sh)` | empty; empty; `5` |

**Mapping to requirements:** A1–A2 → REQ-1; A3–A4 → REQ-2; A5 → REQ-3
(+ `grep -n 'AuditHoldScopeActor' internal/repository/audit.go` for the gap
site); A6 → REQ-4/REQ-9; A7 → REQ-5; A8–A9 → REQ-6; A10–A11 → REQ-7; A14–A15 →
REQ-8; A12–A13 → REQ-10/REQ-11; A16–A18 → REQ-12; A19 → evidence checklist.

## 8. Evidence checklist (final state)

```bash
grep -rn 'EX-2\|EX-3' scripts/check-architecture.sh HARNESS.md AGENTS.md        # empty
grep -rn 'repository\.\(LegacyCutoverStatus\|AuditHoldScope\|ValidCutoverTransition\)' internal/   # empty
grep -rn 'LegacyCutoverStatus\|AuditHoldScope' internal/repository/audit.go     # empty (gap site migrated)
grep -rn 'google/uuid' internal/domain/                                          # empty
grep -c 'self-test rule' <(bash scripts/check-architecture.sh)                   # 5 (a,b,c,d,e1)
bash scripts/check-architecture.sh && echo CLOSED                                 # exit 0, no SELF-TEST FAIL
```

End state: `internal/domain` is pure stdlib + `internal/pkg/*` with zero
exceptions; `internal/handler/` has an absolute repository import ban; the
script header documents only EX-1; the self-test suite proves rules (a)–(e1)
non-vacuous with the moved fixtures; all Go code, tests, and
integration-tagged fakes compile; the full done gate passes.
