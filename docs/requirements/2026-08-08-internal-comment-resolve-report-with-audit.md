# Requirements Specification — internal/comment: convert ResolveReport to a single-transaction WithAudit write (fail-closed, anchored audit)

| | |
|---|---|
| Module | `internal/comment` (unwired parallel tree; the live twin `internal/service/comment.go` + `internal/repository/comment_audit.go` was already converted and is the model) |
| Direction (selected) | *Convert ResolveReport to a single-transaction WithAudit write with fail-closed, anchored audit (fix the swallowed audit call)* |
| Source analysis | `docs/auto/analyses/internal-comment-a2d7c3b3.json` (direction #1) |
| Campaign | `aero-id-b2` (B2 item 1: in-tx audit; B2 item 2: no swallowed audit calls; `docs/campaigns/campaign-aero-id-b2.yaml`) |
| Related specs | `docs/requirements/2026-08-06-internal-comment-transactional-report-resolution-audit.md` (predecessor; live-twin half landed), `docs/requirements/2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop.md` |
| Date | 2026-08-08 |
| Status | Requirements — every cited file/symbol re-verified against the working tree at HEAD `245555b` + in-flight quarantine-gap changes (30 modified files; see §6) |

## 1. Evidence verification

| # | Citation (direction) | Verification against current tree |
|---|---|---|
| 1 | `internal/comment/service.go:360-395` — ResolveReport | ✅ **Verified with drift.** `ResolveReport` is at `service.go:371-403` (the `//nolint:cyclop` marker at :370). It runs three separate non-transactional operations: `s.comments.ResolveReport` (:387), conditional `s.comments.SetStatus` auto-hide (:394), then `s.logResolveAudit` (:401). A crash between commits loses the audit fact; a SetStatus failure leaves the report resolved with zero audit; a Log failure is discarded. |
| 2 | `internal/comment/service.go:424-437` — `logResolveAudit`, `_ = s.audit.Log` at :431 | ✅ **Verified exact.** `logResolveAudit` at :423-437; `_ = s.audit.Log(ctx, &domain.AuditEvent{...})` at :431. Error discarded (fail-open). `audit domain.AuditLogger` field at :20; `WithAuditLogger` option at :37-40. |
| 3 | `internal/comment/repository.go:84-112` — `CreateWithReplyBump` in-tx outbox precedent | ✅ **Verified with drift.** `CreateWithReplyBump` at `repository.go:122-157`; single `r.db.WithContext(ctx).Transaction(...)` (comment insert + reply_count bump + `r.outbox.Append(ctx, tx, evt)`), the in-tx precedent to model `ResolveReportWithAudit` on. `repo` struct at :106-110 holds only `db` + `outbox`. |
| 4 | `internal/repository/audit_ledger.go:113` — `AppendInTx` | ✅ **Verified with drift.** Interface `AuditLedgerTransactionalRepository.AppendInTx(context.Context, *gorm.DB, *domain.AuditFact) error` at :39-41; impl at :129-137. Fails closed on empty signing key (:130-131) and nil tx (:132-133); `validateAuditFact` requires non-empty Source/SourceRegion/PartitionKey/TenantID/Action/OccurredAt (:342-346); appends the `audit_ledger` row **and** a `status='pending'` `audit_publications` row in the caller's tx (`createAuditRows` :190-210); duplicate pre-check `checkAuditDuplicate` keyed on `(tenant_id, source, event_id)` (:157-171) + final guard `uq_audit_ledger_tenant_event` mapped to `ErrAuditDuplicate` (:190-201). |
| 5 | `internal/repository/callback_audit.go:27-55` — `CreateSubscriptionWithAudit` pattern | ✅ **Verified with drift.** Interface at :17-35, impl at :33-64: domain write then `r.audit.AppendInTx(ctx, tx, fact)` with in-tx `ErrAuditDuplicate` tolerance **after** the domain write (contract §4.2, "can never mask a failed domain write"). |
| 6 | `internal/repository/legacy_cutover.go:137-147` — `SetCutoverWithAudit` with `ErrAuditDuplicate` | ✅ **Verified exact.** `SetCutoverWithAudit` at :137-153: decision upsert + `AppendInTx` last; `ErrAuditDuplicate` swallowed in-tx with the §4.2 rationale comment. |
| 7 | `scripts/check-audit-swallow.sh` — P1/P2 patterns + documented module exclusion | ✅ **Verified.** P1 (`_ = ...(Record\|Log\|Publish\|FinishPublication\|ClaimPublication\|RequeuePublication\|RequeueDead)(`) and P2 (bare `Record\|Log\|...(`) at :42-43 — `Log` IS a gate citizen (the script was hardened since the predecessor spec). Header documents the exclusion: "the unwired parallel tree internal/{file,message,comment,notification,search,audit}". Makefile `check-audit-swallow` (:67-69) scans `internal/service internal/repository internal/app internal/storage internal/block cmd` — `internal/comment` is not a root. |
| 8 | `internal/service/comment.go:415-424` — live fail-closed counterpart | ⚠️ **Stale citation; counterpart exists at :407-426.** The cited lines are the old `_ = s.audit.Log` site, since converted: `commentService.ResolveReport` (:367-440) now type-asserts `repository.CommentAuditRepository` and calls `ResolveReportWithAudit` (:408-425) with the anchored fact (`EventID = reportID` via `buildLegacyAuditFact(..., reportID)`); missing audit surface → fail-closed `comment.audit_unavailable` (:409-411). The `audit.ingest_failed` code family the direction's T-5 acceptance names is the live AuditLogger contract (`internal/service/service.go:96`) used at `internal/service/account.go:161,191`. |
| 9 | `internal/comment/service_test.go:~676` — `TestCommentService_ResolveReportEmitsAuditEvent` | ✅ **Verified with drift.** Test at :652-695; asserts `Log` called exactly once, `Action == domain.AuditReportResolve`, `ActorUID == "author"`, `SubjectID == reportID.String()`, **`ID == reportID` ("deterministic dedup anchor")**, `SubjectType == "comment_report"`, `Meta{comment_id, status}`. Passes today (`go test ./internal/comment/ -count=1` → ok). |
| 10 | `internal/repository/audit_wiring_integration_test.go` — integration mirror | ✅ **Verified.** `TestAuditWiringRollbackAtomicity` at :175 (forced rollback → zero ledger rows); `TestAuditWiringPublicationInsertRollsBackLedgerRow` at :268. The comment-leg matrix precedent is `internal/repository/audit_wiring_matrix_integration_test.go:42,95-110` (`ResolveReportWithAudit` commit side: 1 ledger row + 1 pending publication row). Anchor precedent: `internal/repository/comment_report_anchor_integration_test.go` (`TestReportResolveAnchoredEventIDAppendsOnce`, :35). |
| 11 | B2 items 1 and 2 | ✅ **Verified.** `docs/campaigns/campaign-aero-id-b2.yaml`: "(1) in-tx audit … (2) forbid swallowed audit calls (no '_ =' — a make check-audit-swallow gate pattern)". `docs/requirements/2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop.md` REQ-2/REQ-3 scoped the live layer only and left the unwired tree excluded — the gap this direction closes. |
| 12 | Module wiring status | ✅ **Verified.** `internal/comment` is imported only by the unwired `internal/search/*`; zero `comment.NewRepository`/`comment.NewService` construction sites outside the package (grep over non-`internal/comment` files: 0 hits). `internal/repository` does not import `internal/comment` (no cycle risk for the new `internal/comment → internal/repository` import, verified by grep over repository non-test files). |
| 13 | `internal/domain/audit.go` dead-tree note | ✅ **Verified.** Quarantine comment (:13-21) enumerates the four remaining swallow sites, including `internal/comment/service.go:431` — goes stale with this fix and must be updated (R4.3). |

### Runtime evidence captured (2026-08-08)

- `bash scripts/check-audit-swallow.sh internal/comment` → **exit 1**, sole violation `internal/comment/service.go:431: _ = s.audit.Log(...)` — the exact P1 shape banned by the gate, currently invisible because the Makefile root list omits the module.
- `go test ./internal/comment/ -count=1` → **ok** (baseline for T-4).
- Working tree: 30 modified files from the in-flight dead-tree quarantine campaign; the Makefile `check-audit-swallow` line is already dirty (the campaign added `internal/block`). The T-10.2 root addition lands on the same line — merge, do not overwrite.

## 2. Problem statement (current state)

`internal/comment` is the unwired parallel tree, but its only audit path is the module's only compliance surface for moderation, and it is broken in three stacked ways:

1. **Swallowed audit write (B2 item 2).** `service.go:431` discards the `Log` error (`_ =`), the exact P1 shape `scripts/check-audit-swallow.sh` bans — and the module is the documented exclusion from that gate's scan roots, so the violation is invisible and the gate stays green.
2. **Post-commit, non-atomic audit (B2 item 1).** `ResolveReport` (`service.go:371-403`) runs three separate non-transactional operations: report UPDATE (:387) commits, conditional auto-hide SetStatus (:394) commits, then `logResolveAudit` (:401) writes after both commits. A crash after commit loses the audit fact; a SetStatus failure leaves the report resolved with **zero** audit; a Log failure is discarded (fail-open).
3. **The fix pattern is proven and already shipped in the live twin.** `internal/repository/comment_audit.go:31-56` (`ResolveReportWithAudit`) + `internal/service/comment.go:407-426` (fail-closed WithAudit path, anchored `EventID = reportID`) is the identical conversion for the wired module. The dead tree lags behind; this direction brings it to parity and lifts its gate exclusion.

## 3. Requirements

### R1 — Parallel-tree repository: transactional `ResolveReportWithAudit` (`internal/comment/repository.go`)

1.1 The `Repository` interface (`internal/comment/comment.go:52-73`) gains, modeled on `CreateWithReplyBump` (`repository.go:122-157`) and the live twin `comment_audit.go:31-56`:

```go
// ResolveReportWithAudit transitions the report to a terminal status, optionally
// hides the comment (hideCommentID != zero), and appends the audit fact —
// atomically, in one transaction. The fact is appended LAST; any append error
// rolls the whole transaction back. RowsAffected == 0 on the report UPDATE
// returns domain.ErrNotFound and appends nothing.
ResolveReportWithAudit(ctx context.Context, reportID domain.ID, status domain.ReportStatus,
    resolverUID string, at time.Time, hideCommentID domain.ID, fact *domain.AuditFact) error
```

1.2 `repo` (struct at `repository.go:106-110`) gains `audit repository.AuditLedgerTransactionalRepository`; `NewRepository(db *gorm.DB, ob outbox.Repository, audit repository.AuditLedgerTransactionalRepository) Repository` becomes the constructor (zero external call sites — verified, no wiring impact). The import `github.com/aero/aero-id/internal/repository` is required for the interface type and the `ErrAuditDuplicate` sentinel identity; no cycle (verified: repository's non-test files import neither comment nor search).

1.3 `ResolveReportWithAudit` runs one `r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {...})` containing, in order:
1. the report UPDATE — SQL identical to `ResolveReport` (`repository.go:373-387`); `RowsAffected == 0` → `domain.ErrNotFound` (rollback);
2. if `hideCommentID != (domain.ID{})`: the comment status UPDATE to `CommentStatusHidden` — SQL identical to `SetStatus` (`repository.go:320-329`); `RowsAffected == 0` → `domain.ErrNotFound` (rollback; the hide is now atomic with the resolve — the intended atomicity change);
3. `r.audit.AppendInTx(ctx, tx, fact)`, with **nil-audit guard first**: `if r.audit == nil { return fmt.Errorf("transactional audit repository is not configured") }` (precedent `internal/repository/account_profile.go:97-99`) — the tx then rolls back; `ErrAuditDuplicate` tolerated **only here, after the domain writes, inside the same tx** (contract §4.2; verbatim pattern `comment_audit.go:51-55`); any other append error wrapped `fmt.Errorf("append comment audit: %w", err)` rolls the whole tx back.

1.4 Plain `ResolveReport`/`SetStatus` stay unchanged (AGENTS.md: preserve current APIs; `SetStatus` remains used by the service's `Hide`/`Unhide`).

### R2 — Parallel-tree service: build the anchored fact, fail closed, stop swallowing (`internal/comment/service.go`)

2.1 Keep the `audit domain.AuditLogger` field (:20) and `WithAuditLogger` (:37-40) as the **audit enable flag** — exact mirror of the live twin (`internal/service/comment.go:407`: `if s.audit != nil`). `ResolveReport` (:371-403) keeps its reads, validation, authorization, and hide-intent computation, then branches:

- **audit configured (`s.audit != nil`)**: build the fact (R2.2), call `s.comments.ResolveReportWithAudit(ctx, reportID, status, actor, now, hideCommentID, fact)`. Error mapping:
  - `domain.ErrNotFound` → `apperr.NotFound("comment.report_not_found", "report not found")` (the merged call can no longer distinguish the hide leg; both surface as 404, matching the live twin `internal/service/comment.go:413-416`);
  - any other error → **`apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)`** (the supplied T-5 acceptance, byte-identical phrasing to `internal/service/account.go:161,191`; HTTP 503, fail-closed — the moderation state never commits without its fact);
  - `return nil`.
- **audit unconfigured (`s.audit == nil`)**: the existing plain path verbatim (`ResolveReport` then conditional `SetStatus`), with **no audit call** — legacy behavior preserved.

2.2 Fact construction in a new sibling file `internal/comment/audit_fact.go` (keeps `service.go` under the 500-line cap; it is 473 lines today). `buildResolveReportAuditFact(ctx, actor, reportID, rep *CommentReport, status domain.ReportStatus, at time.Time) *domain.AuditFact` produces:
- `EventID: reportID` — **the deterministic dedup anchor** (T-4; retries re-derive the same `event_id`, so the in-tx `ErrAuditDuplicate` fold commits the domain write with exactly one ledger row);
- `Source: "aero-id"`, `SourceRegion: "local"`; `TenantID` and `PartitionKey` from a tenant bound in context when present, else fall back to the actor UID (documented in the file; the dead tree has no `TenantFrom` helper — that lives in `internal/service/service.go:78` — and `validateAuditFact` requires non-empty `TenantID`; `ActorUID` is always non-empty here because `MustActor` precedes);
- `ActorUID: actor`, `Action: domain.AuditReportResolve` (`"comment.report.resolve"`), `ResourceType: "comment_report"`, `ResourceID: reportID.String()`, `OccurredAt: at`;
- `Payload: map[string]any{"comment_id": rep.CommentID.String(), "status": "resolved"|"dismissed"}` — **byte-identical keys/labels to today's `Meta`** (`service.go:431-435`), preserving the T-4 contract.

2.3 Delete `logResolveAudit` (`service.go:423-437`) entirely — the `_ = s.audit.Log` swallow cannot regress if the call is gone. After this, `grep -n "audit.Log\|s.audit" internal/comment/service.go` matches only the enable-flag field/option. No `Log` call remains anywhere in the module (T-10.2's "every audit call propagates its error via `if err :=`" is then vacuously satisfied by the WithAudit call's checked error).

2.4 No handler, gRPC, or HTTP contract changes; `Service.ResolveReport` signature unchanged (`comment.go:87`).

### R3 — Tests (acceptance checks preserved and made testable)

3.1 **T-4 — contract-preserving conversion of `TestCommentService_ResolveReportEmitsAuditEvent`** (`service_test.go:652-695`). The mock `mockCommentRepo` gains `ResolveReportWithAuditFn func(ctx, reportID, status, resolverUID, at, hideCommentID, fact) error` + method (nil-default no-op, mirroring `ResolveReportFn` at :97-101). The test keeps its name and **every existing assertion, re-pointed at the fact argument**: exactly 1 invocation; `fact.Action == domain.AuditReportResolve`; `fact.ActorUID == "author"`; `fact.ResourceID == reportID.String()`; **`fact.EventID == reportID`** (the "deterministic dedup anchor" assertion, currently `logged.ID != reportID` at :687-689); `fact.ResourceType == "comment_report"`; `fact.Payload["comment_id"] == commentID.String()`; `fact.Payload["status"] == "resolved"`. Additionally assert `hideCommentID == commentID` was passed (Resolved + Normal comment). `mockAuditor` stays as the non-nil enable-flag stub (its `Log` is no longer called — the live twin's converted test does exactly this, `internal/service/comment_test.go:539-585`). The plain-path tests (`TestCommentService_ResolveReportTransitionsAndHides` :562, `...DismissLeavesCommentVisible` :598, `...ByNonAuthorIsForbidden` :618, `...InvalidStatusIsBadRequest` :645) construct the service **without** the audit option and must pass unchanged.

3.2 **T-5 unit — fail-closed mapping, no service-level commit.** New `TestCommentService_ResolveReportAuditFailureFailsClosed` (mirror of the live twin's `internal/service/comment_test.go:593-616` (`TestResolveReportAuditFailureFailsClosed`)): `ResolveReportWithAuditFn` returns `errors.New("ledger down")`; `svc.ResolveReport(...)` must return `*apperr.AppError` with `Code == "audit.ingest_failed"` and `HTTP == 503`; assert `ResolveReportFn` and `SetStatusFn` were **never** invoked (the service must not fall back to a committing plain path when audit is configured).

3.3 **T-5 integration — atomic commit/rollback over real Postgres.** New `internal/comment/repository_integration_test.go` (`//go:build integration`, `package comment`, using `internal/repository/testpg` `Start`/`Migrate`/`RepoRoot` and a real `repository.NewAuditLedgerRepository(db, AuditLedgerConfig{SigningKey: ...})`; model: `internal/repository/audit_wiring_matrix_integration_test.go:42,95-110` + `TestAuditWiringRollbackAtomicity` :175). Cases:
1. **Commit side**: seed comment (status normal) + open report; `ResolveReportWithAudit(resolved, hideCommentID=commentID, fact{EventID: reportID, TenantID: "tenant-a", ...})` → report `status=resolved` + `resolved_at` set, comment `status=hidden`, **exactly 1 `audit_ledger` row + 1 `pending` `audit_publications` row** keyed by `event_id = reportID` (count helpers in the style of `countMatrixRows`/`countCallbackAuditRows`).
2. **Dismiss, no hide**: `hideCommentID = domain.ID{}` → report dismissed, comment stays `normal`, 1 ledger row.
3. **Rollback side (keyless-ledger injection, precedent `TestCallbackAuditRollbackWithoutSigningKey` callback_audit_integration_test.go:123-150)**: ledger built with `AuditLedgerConfig{}` (empty signing key) → `AppendInTx` fails → the call returns an error **and** report row unchanged (`status=open`, `resolved_at IS NULL`), comment row unchanged (`status=normal`), **zero** `audit_ledger` and **zero** `audit_publications` rows — the report UPDATE, auto-hide, and ledger row commit or roll back atomically.
4. **Anchored retry fold (T-4 dedup)**: second `ResolveReportWithAudit` with the same `EventID = reportID` → returns nil (in-tx `ErrAuditDuplicate` tolerance), still exactly 1 ledger row.
5. **Not-found**: unknown `reportID` → `domain.ErrNotFound`, zero ledger rows.

3.4 `make test-integration` runs `go test -tags=integration -p 1 ./...` (Makefile:15) — the new file is picked up automatically.

### R4 — Gate: scan `internal/comment`, 0 matches (T-10.2)

4.1 Makefile `check-audit-swallow` target (:67-69) root list becomes `internal/service internal/repository internal/app internal/storage internal/block cmd internal/comment`. The line is already modified by the in-flight quarantine campaign (added `internal/block`); the comment root is appended to the same line.

4.2 Post-conversion, `bash scripts/check-audit-swallow.sh internal/comment` must report **0 matches** (exit 0). Regression check: re-adding `_ = s.audit.Log(` anywhere in `internal/comment` turns the target red (P1 already covers the `Log` form — the fixture machinery at script :47-74 is unchanged).

4.3 Documentation accuracy, in the same change: remove `internal/comment` from the documented exclusion list in the script header (:24-31) and update the `internal/domain/audit.go` quarantine note (:13-21) — the four-site swallow list becomes three (`internal/file/service_share.go:45,75; internal/message/service.go:289`).

## 4. Acceptance mapping

| Supplied acceptance | Requirement | Testable pass criterion |
|---|---|---|
| **T-5**: unit test — when the auditor returns an error, `ResolveReport` returns `apperr.Unavailable("audit.ingest_failed")` and the report-resolve does not commit; integration test against real Postgres proves report UPDATE, auto-hide, and `audit_ledger` row commit or roll back atomically (mirror `internal/repository/audit_wiring_integration_test.go`) | R2.1, R3.2, R3.3 | (a) `TestCommentService_ResolveReportAuditFailureFailsClosed`: repo fake returns an append error → `*AppError{Code: "audit.ingest_failed", HTTP: 503}`, and `ResolveReportFn`/`SetStatusFn` never called; (b) `go test -tags=integration ./internal/comment/` — commit case = 1 ledger row + 1 pending publication row + resolved report + hidden comment; keyless-ledger case = error + `status=open`/`resolved_at NULL` + `status=normal` + 0/0 ledger/publication rows |
| **T-10.2**: extend the check-audit-swallow Makefile gate roots to include `internal/comment`; `scripts/check-audit-swallow.sh internal/comment` reports 0 matches (the `_ =` at service.go:431 is gone; every audit call propagates its error) | R4.1, R4.2 | (a) Makefile:69 contains `internal/comment`; (b) `bash scripts/check-audit-swallow.sh internal/comment` exits 0 with `✓`; (c) `make check-audit-swallow` (full roots) exits 0; (d) mutation check: reintroducing `_ = s.audit.Log(` in `internal/comment/service.go` makes the target exit 1 listing the file:line |
| **T-4**: existing `TestCommentService_ResolveReportEmitsAuditEvent` (`internal/comment/service_test.go:~676`) keeps passing — exactly 1 event, `Action=AuditReportResolve`, EventID anchored to reportID for dedup | R3.1 (+ R3.3 case 4) | (a) `go test ./internal/comment/ -count=1` — the converted test passes with the assertion set of §3.1, including `fact.EventID == reportID`; (b) integration retry-fold case: second append with the same EventID → nil, exactly 1 `audit_ledger` row |

## 5. Scope boundaries (explicit non-goals)

- **Other swallow sites in the dead tree** (`internal/file/service_share.go:45,75`, `internal/message/service.go:289`): untouched — they are separate directions (the same B2 item 2 follow-up, per `internal/domain/audit.go:13-21`); this direction lifts the exclusion only for `internal/comment` and updates the enumeration comment.
- **The live twin** (`internal/service/comment.go`, `internal/repository/comment_audit.go`): already converted — keep-enforced, no changes. In particular, do not "unify" the dead tree's `audit.ingest_failed` mapping with the live twin's `comment.resolve_failed`; the supplied acceptance pins `audit.ingest_failed` for this module and is preserved as supplied.
- **`internal/search`** (the only importer of `internal/comment`): no changes; it uses neither `NewRepository` nor `WithAuditLogger` (verified).
- **EventID anchoring scheme changes**: `EventID = reportID` is the load-bearing anchor already asserted by the current test (`logged.ID != reportID`); no `idgen.Anchor` derivation is introduced (that scheme is the live layer's, `scripts/check-anchored-event-ids.sh` scans the live layer only).
- **No migrations, no handler/API/error-code changes for existing paths, no changes to `AppendInTx`, `drainPublications`, or ledger semantics.**
- **The in-flight quarantine-gap campaign's changes** (30 modified files, incl. Makefile/AGENTS.md): preserved; this direction's Makefile edit merges with the campaign's existing edit on the same line.

## 6. Contract and engineering constraints

- **Atomicity semantics**: the moderation state (report + auto-hide) and the compliance fact commit or roll back together. A failed append surfaces as `503 audit.ingest_failed` and rolls back the hide — the intended behavior change (today: report commits, audit silently lost).
- **In-tx duplicate tolerance** (`ErrAuditDuplicate`): only after the domain writes inside the same tx (contract §4.2), verbatim `internal/repository/comment_audit.go:51-55`.
- **Fail-closed wiring**: nil audit appender in the repo → `ResolveReportWithAudit` errors and rolls back (precedent `account_profile.go:97-99`); service with audit configured but failing append → 503, no plain-path fallback (R2.1 asserts this).
- **File limits** (done-gate): `internal/comment/service.go` 473 and `internal/comment/repository.go` 411 lines today; the fact builder goes to the new sibling `audit_fact.go` so service.go stays ≤ 500. New repo method ≤ 50 lines (funlen) and ≤ 10 cyclomatic (cyclop); if `ResolveReport` grows past funlen with the branch, extract the WithAudit branch into a helper in `audit_fact.go` rather than growing the function.
- **Tenant fallback**: the dead tree has no tenant context helper; the fact builder's `TenantID`/`PartitionKey` fall back to the actor UID with a documented note that the eventual composition root must bind a tenant (live-layer `mustTenant` semantics) before wiring.
- **Working-tree note**: implement on top of the current dirty tree; the Makefile `check-audit-swallow` line already carries the quarantine campaign's `internal/block` root.

## 7. Done gate

```bash
make test && make lint && make lint-strict && make check-file-sizes \
  && make check-audit-swallow && make check-anchored-event-ids && make build
bash scripts/check-audit-swallow.sh internal/comment   # must exit 0, "0 matches"
make test-integration   # required: repository tx boundary + audit append changed
```
