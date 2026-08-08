# Requirements Specification — internal/comment: transactional report-resolution audit

- **Module:** `internal/comment` (plus the live twin `internal/service/comment.go` + `internal/repository/comment.go` and the audit-swallow gate)
- **Direction (selected):** *Make comment report-resolution audit transactional (ResolveReportWithAudit via AppendInTx) and kill the swallowed-log + dead inline publisher path*
- **Source analysis:** `docs/auto/analyses/internal-comment-a2d7c3b3.json` (direction #1)
- **Status:** requirements (evidence verified against working tree `HEAD 13e4676`)

---

## 1. Evidence verification

Every symbol cited by the direction was checked against the repository. Two citations no longer
describe the current tree (items 8 and 10); the work plan below reflects the verified state.

| # | Citation | Verification result |
|---|---|---|
| 1 | `internal/comment/service.go:401` — `logResolveAudit` fires after the two commits | ✅ **Verified.** `ResolveReport` (service.go:371-404) commits `s.comments.ResolveReport` (line 387) in its own statement, conditionally commits `s.comments.SetStatus` (line 394), then calls `s.logResolveAudit` (line 401). No transaction spans them. |
| 2 | `internal/comment/service.go:423-435` — `_ = s.audit.Log(...)` swallow | ✅ **Verified.** `logResolveAudit` at service.go:423-436; `_ = s.audit.Log(ctx, &domain.AuditEvent{...})` at line 431. Error discarded; `s.audit` is the legacy `domain.AuditLogger` (field at service.go:22, `WithAuditLogger` option at service.go:30-33). |
| 3 | `internal/comment/repository.go` `ResolveReport`/`SetStatus` — no tx, no audit param | ✅ **Verified.** `SetStatus` at repository.go:320-329, `ResolveReport` at 373-387 — single `r.db.WithContext(ctx)` UPDATE each, no transaction, no audit dependency; `repo` struct (repository.go:59-63) holds only `db` + `outbox`. |
| 4 | `internal/repository/audit_ledger.go:39,123` — `AppendInTx` | ✅ **Verified.** Interface `AuditLedgerTransactionalRepository.AppendInTx(context.Context, *gorm.DB, *domain.AuditFact) error` at audit_ledger.go:39; impl at audit_ledger.go:106-114. (Line 123 is inside `appendInTx`; minor line drift only.) `AppendInTx` fails closed on empty signing key (line 107-108) and on nil tx (110-111), and appends the `audit_ledger` row **and** a pending `audit_publications` row in the caller's tx (`createAuditRows`, audit_ledger.go:190-207; table 000045). |
| 5 | Precedents: `account_profile.go:101`, `event_receipt.go:180`, `operation.go:178`, `sync_job_audit.go:43` | ✅ **Verified** (minor line drift): `account_profile.go:101` (`r.audit.AppendInTx(ctx, tx, fact)` with fail-closed nil-audit guard at 97-99); `event_receipt.go:180`; `operation.go:182` (call inside the create-tx, with in-tx `ErrAuditDuplicate` tolerance and the contract §4.2 comment at 178-184); `sync_job_audit.go:55` (`appendFinishedAudit`, def at 43). |
| 6 | `internal/comment/service_test.go:657-678` — mockAuditor test pins current swallow behavior | ✅ **Verified.** `TestCommentService_ResolveReportEmitsAuditEvent` (service_test.go:653-693) asserts `Log` was called exactly once with `Action == domain.AuditReportResolve`, actor, `SubjectID == reportID`, `SubjectType == "comment_report"`, `Meta{comment_id, status}`. The test pins the post-commit path and must be converted with the change. |
| 7 | `internal/comment` imported only by `internal/search/*` (unwired parallel tree) | ✅ **Verified.** Only `internal/search/{search.go,handler_grpc.go,service_test.go}` import it; no `cmd/*` composition root constructs `comment.NewRepository` (zero construction sites repo-wide). The running composition wires the live twin (`internal/app/new.go:142,201-202`). |
| 8 | `internal/service/audit_ledger.go:76-80` — dead inline publisher loop (`_ = publisher.Publish`) | ⚠️ **Already fixed by a prior campaign.** `auditLedgerService.Record` (audit_ledger.go:64-77) only calls `s.repo.Append`; no publisher field, no loop, no `_ = publisher.Publish` anywhere in `internal/service`. `NewAuditLedgerService` doc (audit_ledger.go:50-55) states delivery is exclusively `drainPublications`. The acceptance "unit test asserting `auditLedgerService.Record` has no inline publisher loop" is **already satisfied** by `TestAuditLedgerRecordEnqueuesOnly` (internal/service/audit_ledger_test.go:44-66) and `TestAuditLedgerRecordEnqueuesOnlyIntegration` (internal/app/audit_integration_test.go:49-80, "one ledger row plus one pending audit_publications row and performs no governance delivery"). No new code is required for this check; it becomes a regression-verification step (R5). |
| 9 | `internal/service/callback_audit.go:47-82` — `drainPublications` is the only live delivery path | ✅ **Verified.** `drainPublications` at callback_audit.go:38-84 (`ClaimPublication` → parallel `finishPublicationAttempt` → `FinishPublication`); driven by the callback worker loop (callback.go:281); no inline delivery from `Record`. Publishers are injected only via `SetAuditPublishers` (callback.go:103-105). |
| 10 | Makefile "no `check-audit-swallow` target" | ⚠️ **Gate now exists but is narrower than the acceptance.** Makefile:52-54 runs `scripts/check-audit-swallow.sh internal/service`. The script (74 lines) scans **only** `internal/service`, pattern `P1` covers `_ =`/`_, _ =` + `(Record|Publish|FinishPublication|ClaimPublication)(`, `P2` covers bare `Record|FinishPublication|ClaimPublication` calls, and **explicitly excludes the `Log(` form** ("legacy diagnostics surface (AGENTS.md carve-out)") and the parallel tree. Consequently it does not flag `internal/comment/service.go:431` nor `internal/service/comment.go:415` (both `_ = s.audit.Log(`). Gate is green today on `internal/service`. |
| 11 | `internal/service/comment.go:415` — live twin swallow | ✅ **Verified.** `commentService.ResolveReport` (comment.go:367) commits `s.comments.ResolveReport` (line 392) then `SetStatus` (line 398) separately, then `_ = s.audit.Log(...)` at line 415 (block 407-426). Wired in production with `service.WithCommentAuditLogger(auditLedger)` (internal/app/new.go:201-202); `audit` field at comment.go:20. |
| 12 | `internal/app/audit.go:41-46` — publishers wired only to callbackSvc | ✅ **Verified** (line drift): `wireAuditServices(callbackSvc, auditLedgerRepo, auditPublishers...)` at audit.go:83 sets publishers only on the callback service; `buildAuditPublishers` (audit.go:87-124) returns nil when `cfg.AuditGovernance.Enabled` is false (the flag-gated "activation gate"); `NewAuditLedgerService(auditLedgerRepo)` (audit.go:51) receives no publishers. |
| 13 | `checkAuditDuplicate` zero-EventID short-circuit / random fallback | ✅ **Context only.** `checkAuditDuplicate` returns nil for zero `EventID` (audit_ledger.go:157-159); `appendInTx` assigns a fresh `idgen.NewID()` when `fact.ID` is zero (line 145). Deterministic EventID anchoring is direction #2 of the analysis and is **out of scope** here; this spec keeps `EventID` unset (random per append). In-tx `ErrAuditDuplicate` tolerance (contract §4.2) is still required for parity with the shipped precedents. |
| 14 | `domain.AuditLogger` contract | ✅ **Constraint.** "Implementations must be non-blocking … a failed log must not fail the primary operation" (internal/domain/audit.go:10-11; internal/service/service.go:95-97). The `_ =` swallow on the legacy `Log` surface is contract-correct diagnostics; the fix is to **remove the post-commit `Log` path entirely** for comment report resolution (the fact becomes part of the business transaction), not to propagate `Log` errors. |

### Verified current-state delta vs. the direction's problem statement

1. The dead inline publisher loop is **already dead** (prior campaign *make-audit-publications-outbox-the-only-delivery*). Remaining work on that axis: regression protection only.
2. `make check-audit-swallow` **exists** but scans only `internal/service` and excludes the `Log` form; the parallel tree and both comment swallows are invisible to it. Remaining work: extend the gate (R4).
3. The core defect — post-commit, swallowed, non-atomic report-resolution audit — is **unchanged and live** in both trees (items 1, 2, 11): a crash or DB failure between the report/status commits and the audit append loses the compliance fact with no error surfaced.

---

## 2. Scope

**In scope (this direction):**

- `internal/comment` (parallel tree): transactional `ResolveReportWithAudit` (report UPDATE + optional hide `SetStatus` + `AppendInTx` in one tx); service builds the fact and drops `logResolveAudit`/`WithAuditLogger`.
- Live twin: `internal/repository/comment.go` + `internal/service/comment.go` + `internal/app/new.go` wiring (the only composition root that can run the e2e).
- `make check-audit-swallow` extension: scan `internal/comment` and fail on the `Log` swallow form in comment code.
- Tests: fault-injection rollback, service error mapping, e2e (single ledger row + single publication row, delivery only via `drainPublications`), gate fixtures.
- Verification that `auditLedgerService.Record` has no inline publisher loop (already satisfied; regression-guarded).

**Out of scope (explicitly):**

- Deterministic EventID anchoring for the fact (analysis direction #2).
- Dead-letter replay / `RequeueDead` / `cmd/audit-replay` / purge-pause (analysis direction #3).
- The nine pre-existing legacy `_ = s.audit.Log(` diagnostics sites in non-comment services (`block.go:54,77`, `admin.go:93,120`, `message.go:313`, `ban.go:83,119`, `file_share.go:114,146`). They are contract-compliant legacy diagnostics per AGENTS.md ("The current best-effort `AuditLogger` is suitable for legacy diagnostics only") and the `AuditLogger` interface contract; their conversion is separate directions.
- Any change to `auditLedgerService.Record`, `drainPublications`, `ClaimPublication`/`FinishPublication`, or `AppendInTx` semantics.
- Any change to HTTP/gRPC handler contracts, service method signatures, error codes, or migrations.

---

## 3. Requirements

### R1 — Parallel-tree repository: transactional resolution (`internal/comment/repository.go`)

1.1 `repo` gains an audit dependency typed `repository.AuditLedgerTransactionalRepository`
(`internal/repository/audit_ledger.go:39`); `NewRepository(db, outbox, audit)` becomes the constructor
(zero existing callers outside the package — verified — so no wiring impact). The dependency must be
fail-closed in the new method: a nil audit appender returns an error and rolls back
(precedent `internal/repository/account_profile.go:97-99`, `internal/repository/sync_job_audit.go:53-54`).
This requires `internal/comment` → `internal/repository` import; safe (nothing in `internal/repository`
imports `internal/comment`, so no cycle) and needed for the `repository.ErrAuditDuplicate` sentinel
identity returned by `AppendInTx`.

1.2 New method, modeled on `CreateWithReplyBump` (repository.go:91-119) and the shipped
`XxxWithAudit` precedents (`callback_audit.go:40-60`, `operation.go:172-186`):

```go
// ResolveReportWithAudit transitions the report to a terminal status, optionally
// hides the comment, and appends the audit fact — atomically, in one transaction.
// hide is nil when no comment status change is requested.
ResolveReportWithAudit(ctx context.Context, reportID domain.ID, status domain.ReportStatus,
    resolverUID string, at time.Time, hide *domain.CommentStatus, fact *domain.AuditFact) error
```

Single `r.db.WithContext(ctx).Transaction` containing, in order:
1. the report UPDATE (identical SQL to `ResolveReport`, repository.go:373-387); `RowsAffected == 0` → `domain.ErrNotFound` → rollback;
2. if `hide != nil`: the comment status UPDATE (identical to `SetStatus`, repository.go:320-329); `RowsAffected == 0` → `domain.ErrNotFound` → rollback;
3. `r.audit.AppendInTx(ctx, tx, fact)`, tolerating `repository.ErrAuditDuplicate` **only** after the domain writes inside the tx (contract §4.2; precedent `operation.go:182`, `callback_audit.go:49`); any other append error rolls back the whole tx.

1.3 Plain `ResolveReport`/`SetStatus` remain unchanged (AGENTS.md: preserve current APIs until an
explicit migration task removes them; `SetStatus` is still used by the service's `Hide`/`Unhide`).

### R2 — Parallel-tree service: build the fact, stop swallowing (`internal/comment/service.go`)

2.1 `ResolveReport` (service.go:371-404) keeps its reads, validation, and authorization, then:
- computes the hide intent exactly as today (`status == ReportStatusResolved && c.Status == CommentStatusNormal` → `hide = &CommentStatusHidden`; else nil);
- builds the `*domain.AuditFact` in the service (precedent: `internal/service/account_profile.go:48` builds the fact and passes it to the WithAudit repo method) with the same payload vocabulary as today's event:
  - `Source: "aero-id"`, `SourceRegion: "local"`,
  - `PartitionKey`: non-empty and stable for the hash chain — tenant from context when present, else the actor UID (the ledger chains by `(source, partition_key)`, audit_ledger.go:134-141; the live layer's `normalizeAuditInput` fallback is not available in this package, so the service must populate it),
  - `ActorUID: actor`, `Action: domain.AuditReportResolve` (`"comment.report.resolve"`), `ResourceType: "comment_report"`, `ResourceID: reportID.String()`, `OccurredAt: now`,
  - `Payload: {"comment_id": rep.CommentID.String(), "status": "resolved"|"dismissed"}` (exact keys/labels preserved from service.go:431-435),
  - `EventID` left zero (random UUIDv7 assigned by the ledger; anchoring is out of scope);
- calls `s.comments.ResolveReportWithAudit(ctx, reportID, status, actor, now, hide, fact)`;
- maps errors exactly as today: `domain.ErrNotFound` → `comment.report_not_found` / `comment.not_found` (per the failing write), append failure → `apperr.Internal("comment.resolve_failed")` wrapping the repo error. **The append failure now surfaces as a 500 and rolls back the moderation state — this is the intended behavior change** (no more silent compliance-fact loss).

2.2 Delete `logResolveAudit` (service.go:423-436), the `audit domain.AuditLogger` field (service.go:22),
and the `WithAuditLogger` option (service.go:30-33) — the swallow cannot regress if the surface is gone.
The package's only consumer, `internal/search`, does not use the option (verified).

### R3 — Live twin: same fix on the wired path (`internal/repository/comment.go`, `internal/service/comment.go`, `internal/app/new.go`)

3.1 `repository.CommentRepository` gains `ResolveReportWithAudit` (same signature/contract as R1.2);
`commentRepo` gains the audit dependency; `NewCommentRepository(db, outbox, audit)` (comment.go:72)
becomes the constructor. Update the three verified construction sites:
`internal/app/new.go:142` (pass `auditLedgerRepo`, already in scope from `buildAuditStack` at new.go:134 —
`FullAuditLedgerRepository` embeds `AuditLedgerTransactionalRepository`, audit_wiring.go:9-12),
`internal/repository/comment_test.go:22`, `internal/repository/search_integration_test.go:27`.

3.2 `commentService.ResolveReport` (internal/service/comment.go:367): after the existing
reads/validation, build the fact with the shared helper `buildAuditFact(ctx, AuditFactInput{...})`
(audit_ledger.go:80-95 — inherits `bindAuditContext`, `normalizeAuditInput`, payload redaction;
precedent account_profile.go:48), compute the hide intent, call
`s.comments.ResolveReportWithAudit(...)`, map errors with the existing codes. Delete the
`if s.audit != nil { _ = s.audit.Log(...) }` block (comment.go:407-426), the `audit AuditLogger`
field (comment.go:20), and `WithCommentAuditLogger` (comment.go:26-29). Update `internal/app/new.go:201-202`
(drop the option; the ledger repo is now the repo's dependency) and the mock
(`internal/service/mocks/mocks.go:271-370` gains `ResolveReportWithAuditFn` + method).

3.3 No handler, gRPC, or HTTP contract changes: `Service.ResolveReport` signature is unchanged.

### R4 — Gate: `make check-audit-swallow` covers both trees and the `Log` form in comment code

4.1 `scripts/check-audit-swallow.sh`:
- scan root becomes `internal/service internal/comment` (Makefile target updated to
  `bash scripts/check-audit-swallow.sh internal/service internal/comment`);
- existing P1/P2 patterns (Record/Publish/FinishPublication/ClaimPublication) apply tree-wide, unchanged;
- new P3 pattern for the swallowed `Log` form (`_ = ...Log(`), applied **only to the comment scope**:
  all of `internal/comment/**` and `internal/service/comment.go`. This scope restriction is deliberate
  and documented in the script header: the nine legacy `_ = s.audit.Log(` diagnostics sites in
  non-comment services are contract-compliant (AGENTS.md legacy carve-out; `AuditLogger` "must not
  fail the primary operation") and are converted by separate directions — a tree-wide `Log` ban would
  make the done-gate red for out-of-scope code;
- self-test fixtures: add a positive fixture containing `_ = s.audit.Log(` (comment-shaped code) that
  the gate must flag, and a negative fixture with `if err := ...Log(...)` that it must accept; the
  existing positive/negative fixture machinery stays.

4.2 After the R2/R3 code changes, the gate is green (0 matches) and fails (non-zero exit) if either
comment swallow is reintroduced. Regression scenario: re-adding `_ = s.audit.Log(` to
`internal/comment/service.go` or `internal/service/comment.go` → `make check-audit-swallow` exits non-zero.

### R5 — Tests (acceptance checks preserved and made testable)

| Supplied acceptance | Requirement ID | Testable specification |
|---|---|---|
| T-5: fault-injection unit test — inject `AppendInTx` failure inside the new `ResolveReportWithAudit` tx and assert the report-status UPDATE and hide SetStatus roll back atomically with the audit append | T-5.1 | **Repo-level rollback test (integration tag, real Postgres via `internal/repository/testpg`).** No sqlmock in `go.mod` (verified), and atomicity is not provable with fakes, so the assertion runs against a real tx. Inject a stub `AuditLedgerTransactionalRepository` whose `AppendInTx` returns a deterministic error (the keyless-ledger recipe of `TestCallbackAuditRollbackWithoutSigningKey`, callback_audit_integration_test.go:123-150, is the in-repo precedent). Seed a comment + open report; call `ResolveReportWithAudit(..., hide=&Hidden, fact)`; assert: (a) the call returns the injected error, (b) report row unchanged (`status = open`, `resolved_at IS NULL`), (c) comment row unchanged (`status = normal`), (d) zero `audit_ledger` rows and zero `audit_publications` rows for the fact. Mirrors `countCallbackAuditRows` row-count style. |
| T-5 (service mapping) | T-5.1b | **Unit test** (`internal/comment/service_test.go` and/or `internal/service/comment_test.go`): repo fake's `ResolveReportWithAuditFn` returns a wrapped append error → `svc.ResolveReport` returns `apperr.Internal` with code `comment.resolve_failed` (the error is surfaced, not swallowed); fake returning `domain.ErrNotFound` → 404 mapping preserved. |
| T-5: e2e — resolve a report with audit enabled and assert exactly one `audit_ledger` row plus one `audit_publications` row appear in the same tx and the first fact reaches the governance endpoint only via `drainPublications` (first-event e2e for the activation gate) | T-5.2 | **Integration e2e in `internal/service` (package `service`, tag `integration`)** — the only composition-root path; modeled on `TestAuditLedgerRecordEnqueuesOnlyIntegration` (internal/app/audit_integration_test.go:49-80) + the drain invocation pattern (callback_integration_test.go:235,285) + fake-publisher injection (callback_audit_test.go:155). Wire: `NewAuditLedgerRepository` (signing key set) → `NewCallbackService` + `SetAuditPublicationRepository` + `SetAuditPublishers(spyPublisher)` (the flag-gated activation wiring of `wireAuditServices`, simulated with a spy for `cfg.AuditGovernance.Enabled == true`) → `NewCommentRepository(db, outbox, ledgerRepo)` → `NewCommentService`. Seed comment + report via the repo. As the comment author, call `ResolveReport(..., Resolved)`. Assert: (a) exactly 1 `audit_ledger` row (`action = 'comment.report.resolve'`, `resource_id = <reportID>`, `actor_uid = <author>`, payload keys `comment_id`/`status = 'resolved'`); (b) exactly 1 `audit_publications` row joining that fact (`status = 'pending'`); (c) both rows appear together after commit (atomicity is additionally proven by T-5.1); (d) spy publisher received **0** facts immediately after resolve (no inline delivery — the "only via drainPublications" assertion); (e) after `svc.drainPublications(ctx)` the spy received exactly the one fact and the publication row is terminal (`status = 'completed'`, per `publicationOutcome`, callback_audit.go:90-98). |
| T-4: add `make check-audit-swallow` that fails on `_ =` immediately followed by audit Log/Record/Publish/FinishPublication calls across internal/comment and internal/service | T-4.1 | Gate extension per R4. Testable: (a) script self-tests flag the new positive `Log` fixture and accept the negative fixture; (b) `make check-audit-swallow` exits 0 on the post-change tree; (c) mutation check — reintroduce `_ = s.audit.Log(` in `internal/comment/service.go` (or `internal/service/comment.go`) and assert the target exits non-zero with the file:line listed. |
| T-4: unit test asserting `auditLedgerService.Record` has no inline publisher loop | T-4.2 | **Already satisfied** — `TestAuditLedgerRecordEnqueuesOnly` (internal/service/audit_ledger_test.go:44-66) plus `TestAuditLedgerRecordEnqueuesOnlyIntegration` (internal/app/audit_integration_test.go:49-80). Requirement is verification + regression guard: keep these tests green; the gate's P1 `Publish` pattern (R4) and T-5.2 assertion (d) prevent reintroduction of an inline loop. No new code expected. |

### R6 — Existing tests converted with the change

- `internal/comment/service_test.go:653-693` `TestCommentService_ResolveReportEmitsAuditEvent`:
  rewrite to capture the `*domain.AuditFact` argument of the repo fake's `ResolveReportWithAuditFn`
  and assert the same contract (action, actor, subject id/type, meta `comment_id`/`status`), plus
  assert the hide intent is passed for `Resolved`+`Normal` and nil for `Dismissed`. `mockAuditor`
  and `WithAuditLogger` usages in the package's tests are removed with the option.
- `internal/service/comment_test.go:559` (uses `WithCommentAuditLogger` + `mocks.CommentRepo`):
  same conversion against `ResolveReportWithAuditFn`.
- `internal/service/mocks/mocks.go` `CommentRepo`: add `ResolveReportWithAuditFn` + method
  (default no-op, mirroring `ResolveReport` at mocks.go:366-370).
- Repo unit tests (`internal/comment/repository_test.go` if present / `internal/repository/comment_test.go`):
  constructor signature updates only.

---

## 4. Contract and engineering constraints

- **Atomicity semantics:** the moderation state and the compliance fact commit or roll back together;
  a report resolution can no longer exist without its audit fact. A failed append surfaces as
  `500 comment.resolve_failed` and rolls back the hide — intended behavior change, to be called out
  in the PR description.
- **In-tx duplicate tolerance** (`ErrAuditDuplicate`) follows contract §4.2 exactly as shipped
  (operation.go:178-184, callback_audit.go:44-49): tolerated only after the domain writes inside the
  same tx, so it can never mask a failed domain write.
- **Legacy `AuditLogger` contract preserved** (domain/audit.go:10-11): the remaining nine diagnostics
  sites keep their swallow-by-design behavior; this direction removes the comment moderation `Log`
  paths entirely instead of changing their error handling.
- **Fail-closed wiring:** nil audit dependency in a comment repo → the WithAudit method errors and
  rolls back (precedent account_profile.go:97-99); the live composition root passes the ledger repo
  from `buildAuditStack`.
- **File limits** (done-gate): current sizes `internal/comment/service.go` 472, `internal/comment/repository.go` 411,
  `internal/service/comment.go` 483, `internal/repository/comment.go` 464 lines — all ≤ 500. The
  additions are roughly net-neutral (fact build + WithAudit call replace `logResolveAudit`/swallow
  block); if a file crosses 500 during implementation, extract a sibling (e.g., a fact-builder
  helper file) rather than growing the file. Keep the new repo method ≤ 50 lines (funlen) and ≤ 10
  cyclomatic (cyclop) — the service method keeps its existing `//nolint:cyclop` only if complexity
  is unchanged from today.
- **No migrations:** `audit_ledger` (000044) and `audit_publications` (000045) already exist; the
  direction only adds call sites.
- **No API changes:** service method signatures, HTTP/gRPC handlers, and error codes are unchanged.

## 5. Out-of-scope reminder

Direction #2 (deterministic EventID anchoring for retry dedup) and direction #3 (dead-letter replay,
`cmd/audit-replay`, purge-pause, 401 token invalidation) from `internal-comment-a2d7c3b3.json` are
**not** part of this specification. In particular: do not set `EventID` on the fact, do not add
requeue/CLI tooling, do not touch `TokenSource`.

## 6. Verification / done gate

```bash
make test && make lint && make lint-strict && make check-file-sizes \
  && make check-audit-swallow && make check-anchored-event-ids && make build
make test-integration   # required: repository SQL + tx boundary + audit append + drain delivery changed
```

Integration scope after this change: T-5.1 rollback test, T-5.2 e2e, and the existing
`TestAuditLedgerRecordEnqueuesOnlyIntegration`/`TestCallbackAuditRollbackWithoutSigningKey` must pass.
