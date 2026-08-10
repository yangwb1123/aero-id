# Requirements Specification — internal/comment: close the last swallowed, fail-open, out-of-transaction audit write and extend check-audit-swallow scope to the tree

| | |
|---|---|
| Module | `internal/comment` (unwired parallel tree; live twin `internal/service/comment.go` + `internal/repository/comment_audit.go` already converted) |
| Direction | Close the last swallowed, fail-open, out-of-transaction audit write in internal/comment and extend check-audit-swallow scope to the tree (value 10, effort 4) |
| Baseline | HEAD `b005409`; working tree carries an unrelated in-flight campaign (MarkDelivery/composition-root, per `git status`). `make check-audit-swallow` green on the live roots today. |
| Source analysis | `docs/auto/analyses/internal-comment-a2d7c3b3.json` (direction 1) |

## 1. Evidence verification (every cited symbol checked against the working tree)

| # | Direction citation | Verdict |
|---|---|---|
| 1 | `internal/comment/service.go:307-356` — ResolveReport commits resolve + conditional hide as two repo calls, then audits after commit | ✅ **Verified with drift correction C1.** `ResolveReport` is at `service.go:371-402`: `s.comments.ResolveReport` (:387), conditional `s.comments.SetStatus` auto-hide (:391-399), then `s.logResolveAudit` (:401) — three separate non-transactional operations; a crash between them loses the fact, a SetStatus failure leaves the report resolved with zero audit. |
| 2 | `internal/comment/service.go:456-467` — `logResolveAudit` `_ = s.audit.Log`, `if s.audit == nil` fail-open | ✅ **Verified with drift correction C2.** `logResolveAudit` is at `service.go:423-432`; `if s.audit == nil { return }` at :424-426 (fail-open); `_ = s.audit.Log(ctx, &domain.AuditEvent{...})` at :431 (P1 swallow). `audit domain.AuditLogger` field at :20; `WithAuditLogger` option at :37-40. `internal/domain/audit.go:14-19` explicitly names `internal/comment/service.go:431` as one of the four remaining dead-tree swallow sites. |
| 3 | `internal/comment/service_test.go:657-678` — mockAuditor LogFn pins the legacy path | ✅ **Verified.** `TestCommentService_ResolveReportEmitsAuditEvent` at :652-695; `mockAuditor{LogFn: ...}` at :657; asserts exactly 1 Log call, `Action`, `ActorUID`, `SubjectID`, **`ID == reportID` (anchor)**, `SubjectType`, `Meta{comment_id,status}`. `mockAuditor` type at :718-726 returns nil when `LogFn` is nil (fail-open default). No test asserts a Log error propagates. |
| 4 | `scripts/check-audit-swallow.sh` — documented exclusion of internal/comment | ✅ **Verified.** Header (:34-39) documents the exclusion: "the unwired parallel tree internal/{file,message,comment,notification,search,audit}". Makefile `check-audit-swallow` (:67-69) scans `internal/service internal/repository internal/app internal/storage internal/block cmd` — `internal/comment` is not a root. P1/P2 patterns at :42-43 already contain `Log` in both forms. |
| 5 | `internal/repository/comment_audit.go:31-52` — `ResolveReportWithAudit` target pattern, `AppendInTx` | ✅ **Verified.** Interface at :11-20; impl at :22-52: single `r.db.WithContext(ctx).Transaction(...)` — report update (0 rows → `ErrNotFound`, no fact), conditional hide via `UpdateColumn`, fact appended last via `r.audit.AppendInTx(ctx, tx, fact)` with `ErrAuditDuplicate` folded in-tx. |
| 6 | `internal/service/comment.go:367-421` — live fail-closed ResolveReport | ✅ **Verified.** Live `ResolveReport` at :367-421: when `s.audit != nil`, type-asserts `repository.CommentAuditRepository` and returns `apperr.Internal("comment.audit_unavailable", ...)` when the surface is missing (:403-405); the anchored fact (`EventID = reportID`, retry-dedup) is built via `buildLegacyAuditFact(..., reportID)` (:407-410). The `s.audit == nil` skip guard (:402) is the FM-8 legacy guard policed by `assertAuditWiring` at the composition root — the dead tree has no such wiring assertion. |
| 7 | `internal/app/new.go:144,193,263` — composition root wires only the live comment service | ✅ **Verified.** `commentRepo := repository.NewCommentRepository(gdb, outboxRepo, auditLedgerRepo)` (:144); `commentSvc := service.NewCommentService(commentRepo, fileRepo, msgRepo, ssoClient, service.WithCommentAuditLogger(auditLedger))` (:193); `assertAuditWiring(... name: "comment", svc: commentSvc ...)` (:263). Zero construction sites for the dead tree's `comment.NewService`/`comment.NewRepository` outside the package. |
| 8 | "its only consumer is the equally unwired internal/search" | ✅ **Verified.** Repo-wide grep: `internal/comment` is imported only by `internal/search/{search.go,handler_grpc.go,service_test.go}`. `internal/search` itself has zero importers (non-test and test). Neither is reachable from any `cmd/*` or `internal/app` composition root. |
| 9 | Gate baseline probe | ✅ **Verified.** `bash scripts/check-audit-swallow.sh internal/comment` → exit 1, sole violation `internal/comment/service.go:431: _ = s.audit.Log(...)` — the exact P1 shape, invisible today only because the Makefile root list omits the module. |

**Missing coverage found (gaps this spec closes):** no test anywhere (live or dead tree) proves `commentRepo.ResolveReportWithAudit`'s transactional rollback — a fact-append failure rolling back the report status and the comment hide. Live tests cover routing/fail-closed (`internal/service/comment_test.go:449-620`), anchored dedup (`internal/repository/comment_report_anchor_integration_test.go`), and the wiring matrix happy path (`internal/repository/audit_wiring_matrix_integration_test.go:95-107`), but not the atomicity failure leg.

## 2. Baseline facts

- `internal/comment` = 9 files, 3,157 lines total; only audit-touching code is the field/option/guard/swallow in `service.go`. Its only consumer is `internal/search` (6 files, 699 lines), itself unwired with zero importers.
- Live twin already implements the target contract: one gorm transaction (resolve + conditional hide + anchored fact), fail-closed `comment.audit_unavailable`, `ErrAuditDuplicate` folded in-tx, `ErrNotFound` on 0 rows with no fact appended.
- The gate's P1/P2 pattern list already matches the dead-tree swallow verbatim; only the scan root is missing (probe #9).
- Precedent: `internal/block` dead tree was deleted in the 2026-08-08 quarantine (commit `b005409`); `check-audit-swallow.sh` carries the standing "QUARANTINE GUARD (FM-12): internal/block must not exist" tripwire and scans `internal/block` as a regression root.
- Sequencing hazard: extending the scan root before removing the site turns the gate red immediately (probe #9). The fix and the scope extension must land in the same change (or fix first).

## 3. Requirements

### REQ-1 — No comment audit path may swallow, fail open, or lag the transaction (T-10.2, second clause)

The fail-open guard (`if s.audit == nil { return }`, `service.go:424`), the discarded error (`_ = s.audit.Log(...)`, `service.go:431`), and the post-commit audit call (`service.go:401`) must not exist on any comment audit path. Two admissible implementations, chosen by REQ-5:

- **Option A — Quarantine (recommended).** Delete `internal/comment` (9 files) and, mechanically, its only consumer `internal/search` (6 files; both are unwired, zero composition-root reachability — verified in §1 row 8). Add a standing FM-12-style quarantine guard to `check-audit-swallow.sh` (mirroring the `internal/block` block, `:47-55`) so any reintroduction of the directory is a gate violation even without a swallow shape.
- **Option B — Port to the WithAudit/AppendInTx surface.** The dead-tree `repo` (gorm-only today, `repository.go:106-110`) gains an `internal/repository.AuditLedgerRepository` dependency (`AppendInTx` at `internal/repository/audit_ledger.go:39,129`; the `internal/comment → internal/repository` import is already permitted by `check-architecture.sh` ALLOWED_SHARED) and a `ResolveReportWithAudit` method on the `Repository` interface mirroring `repository/comment_audit.go:22-52`; `serviceImpl.ResolveReport` routes through it fail-closed; `logResolveAudit`, the `audit` field, and `WithAuditLogger` are deleted; `service_test.go:652-695,718-726` are rewritten to pin the fail-closed WithAudit path (preserving the `ID == reportID` anchor assertion byte-identical).

**Acceptance (both options):**
- A-1.1: `grep -rn "audit == nil\|_ = s.audit.Log(\|s.audit.Log(" internal/comment` yields zero matches (Option A: directory absent; the quarantine guard enforces it).
- A-1.2: `make check-audit-swallow` is green and the Makefile root list includes `internal/comment` (see REQ-2).
- A-1.3 (Option B only): the ported service's ResolveReport returns `comment.audit_unavailable`-equivalent when the WithAudit surface is missing, and a unit test pins it.

### REQ-2 — Extend check-audit-swallow scope to internal/comment; pattern list unchanged (T-10.1)

- Add `internal/comment` to the Makefile `check-audit-swallow` root list (`Makefile:69`).
- Add a self-test fixture that mechanically pins the extended scope, mirroring the existing I6 `cmd_positive` pattern (`scripts/testdata/audit-swallow/cmd_positive/cmd/fixture.go`): `scripts/testdata/audit-swallow/comment_positive/internal/comment/fixture.go` containing both the P1 shape (`_ = s.audit.Log(ctx, e)`) and the P2 shape (bare `s.audit.Log(ctx, e)`), with a self-test assertion in the script that greps the fixture directory with the exact P1/P2 patterns and requires ≥1 hit — so a future removal of the root or a pattern regression turns the gate red on the self-test, never silently green.
- Do **not** change P1/P2 patterns and do **not** add method names (the existing `Log` membership in both patterns already covers the comment site).
- Under Option A, also add the quarantine guard (REQ-1) so a reintroduced `internal/comment` fails the gate even with zero swallow shapes.

**Acceptance (T-10.1, testable):**
- A-2.1: a mutant probe reintroducing `_ = s.audit.Log(` or bare `s.audit.Log(` under `internal/comment` (scratch branch or the `comment_positive` fixture) makes `make check-audit-swallow` exit 1. The `comment_positive` self-test assertion makes this permanent.
- A-2.2: `make check-audit-swallow` on the delivered tree exits 0 (self-test + scan + quarantine guard all green).

### REQ-3 — Prove resolve + hide + audit fact commit in one transaction (T-10.2, first clause)

New integration test `internal/repository/comment_audit_integration_test.go` (`//go:build integration`, real Postgres via testcontainers, alongside `comment_report_anchor_integration_test.go`), targeting the authoritative `repository.commentRepo.ResolveReportWithAudit`:

- **Failure leg:** seed a normal comment + open report; call `ResolveReportWithAudit` with a fact whose append fails hard (e.g., a ledger with an empty signing key — `AppendInTx` errors "audit ledger signing key is not configured", `audit_ledger.go:131-133`; a validation failure is equally admissible, but not `ErrAuditDuplicate`, which is deliberately folded in-tx). Assert final rows unchanged: report still open, comment still visible, zero `audit_ledger` rows, zero `audit_publications` rows.
- **Happy leg:** same seed with a valid fact; assert report terminal, comment hidden, exactly one ledger row + one publication row with `EventID == reportID` (anchor preserved).
- **Not-found leg:** unknown reportID with a valid fact; assert `ErrNotFound`, no fact appended, no hide.

**Acceptance:**
- A-3.1: `make test-integration` green including the new test; the failure leg's "final rows unchanged" assertions are explicit `SELECT`-based checks, not just error propagation.
- A-3.2: the test file is the only place these three legs are asserted (no duplication in the dead tree; under Option A the dead tree no longer exists).

### REQ-4 — Doc-consistency sweep

Every document that names `internal/comment` as a scanned-but-excluded tree or as a live consumer must be updated in the same change (mirrors the internal/audit quarantine precedent):

| File | Today | After |
|---|---|---|
| `scripts/check-audit-swallow.sh:34-39` | exclusion list names `internal/{file,message,comment,...}` | remove `comment` (Option A: also `search`); note the quarantine/tripwire |
| `internal/domain/audit.go:14-19` | names `internal/comment/service.go:431` among the four remaining swallow sites | update the count/list (Option A: comment+search deleted) |
| `AGENTS.md:333` | "`internal/{file,message,comment,notification,search}` and `internal/audit` are an unwired parallel tree" | reflect the quarantine (Option A) or the converted tree (Option B) |
| `scripts/check-architecture.sh:12,38` | `FEATURES` lists `comment`; exception `search:comment` | `[ -d "$dir" ] || continue` tolerates absence; delete stale entries (Option A) |
| `docs/requirements/2026-08-08-internal-comment-resolve-report-with-audit.md` | predecessor spec (in-flight edits) | verify it contains no claim that the dead tree is wired or that the swallow is acceptable |

**Acceptance:** `grep -rn "internal/comment" scripts/check-audit-swallow.sh AGENTS.md internal/domain/audit.go scripts/check-architecture.sh` returns only entries consistent with the delivered state (quarantined-and-guarded or converted-and-scanned).

### REQ-5 — Decision rule between Option A and Option B

Choose **Option A (quarantine)** unless a reviewer demonstrates a runtime or near-term consumer of `internal/comment`/`internal/search` outside their own tests. Rationale: the live twin already implements the identical contract with the identical anchored EventID; a port creates a second, unwired, duplicative implementation of the same authority surface with permanent drift risk; Option A follows the established `internal/block` precedent (`b005409`) and costs less (deletion + guard + fixture vs. new ledger dependency, interface change, and test rewrite). The delivered acceptance set (A-1.x, A-2.x, A-3.x, A-4.x) is option-independent.

**Acceptance:** the implementation commit states the chosen option and the decision inputs; no acceptance check is skipped or weakened by the choice.

## 4. Sequencing and coordination constraints

- The site removal (REQ-1) and the root extension (REQ-2) must land in the same change — extending the root first turns the gate red on `service.go:431` (probe #9).
- The working tree carries an in-flight campaign touching `Makefile` (new `check-composition-root` target) and `internal/app/audit.go`. The `check-audit-swallow` target edit does not overlap those hunks, but rebase/merge coordination is required; do not revert the in-flight changes.
- `check-file-sizes` (500-line cap): Option A shrinks the tree; Option B grows `internal/comment/repository.go` — keep the merged repo file under the cap (split the audit surface into `comment_audit.go` in the dead tree, mirroring the live layout).
- Option B must preserve the load-bearing `EventID == reportID` anchor assertion (`service_test.go:672-674`) byte-identical; the live anchor contract (`comment_report_anchor_integration_test.go`) is unaffected by either option.

## 5. Out of scope (explicitly not required by this direction)

- Anchor-based `event_id` for comment outbox events (analysis direction 2 — fresh-mint `idgen.NewID` in `internal/service/message.go:152-162` and zero-ID `outbox.NewEvent` in the dead tree) is a separate direction and must not be folded in.
- Governance first-event e2e, drain/claim locking tests, publisher `Invalidate` de-swallow (analysis direction 3) are separate directions.
- The remaining dead-tree swallow sites `internal/file/service_share.go:45,75` and `internal/message/service.go:289` (`domain/audit.go:14-19`) are out of scope; the doc sweep (REQ-4) only renumbers/re-describes them.
- No change to the P1/P2 pattern list; no new audit methods; no behavioral change to the live comment service.

## 6. Done-gate mapping

```bash
make test && make lint && make lint-strict && make check-file-sizes \
  && make check-audit-swallow && make check-anchored-event-ids \
  && make check-audit-manifest && make check-composition-root \
  && make check-integration-compile && make build
make test-integration   # after the REQ-3 test lands
```

Specific mapping: `make check-audit-swallow` → REQ-1/REQ-2 (green scan + self-test + guard); `make test-integration` → REQ-3; `make test` → REQ-1 (Option B unit pins) and no dead-tree test breakage (Option A deletes them); `make check-integration-compile` → REQ-3 file compiles under the integration tag.
