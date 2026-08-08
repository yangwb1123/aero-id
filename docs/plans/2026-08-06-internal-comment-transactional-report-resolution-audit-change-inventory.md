# Change inventory — internal/comment transactional report-resolution audit

- **Companion to:** `docs/plans/2026-08-06-internal-comment-transactional-report-resolution-audit-design.md`
- **Tree checked:** HEAD `13e4676` + uncommitted prior-campaign changes (Makefile, `internal/app/audit.go`, AGENTS.md, go.mod, ledger/operation/sync work)
- **Method:** every inventory row below was verified against the tree (grep/read), not copied from the design. `.claude/worktrees/*` is stale and excluded.
- **Verdict:** the design's unchanged-surface boundary holds (handlers, proto, config, migrations, error codes, `Record`/`drainPublications`/`AppendInTx`, legacy `Log` sites). **Two existing tests break that are outside the design's enumerated rewrite set** (`ResolveReportTransitionsAndHides` in both trees) and must be added; three amendments (G-1, G-5, G-4) and one migration-number drift are required in the design text (see §6).

---

## 1. Exported symbols that change

### 1.1 Live tree — `internal/repository` + `internal/service`

| Symbol | Kind | Location today | Change |
|---|---|---|---|
| `CommentRepository.ResolveReportWithAudit` | interface method (NEW) | — | added to `internal/repository/comment.go:17-48` (after `ResolveReport` :48). Signature: `ResolveReportWithAudit(ctx context.Context, reportID domain.ID, status domain.ReportStatus, resolverUID string, at time.Time, hide *domain.CommentStatus, fact *domain.AuditFact) error`. Contract: report UPDATE identical to `ResolveReport` (:370), then optional hide UPDATE identical to `SetStatus` (:311), then `audit.AppendInTx(ctx, tx, fact)`; `0 rows` → `ErrNotFound` (report leg) / `ErrCommentNotFound` (hide leg); append failure or `ErrAuditDuplicate`-tolerance only after the domain writes; any error rolls back the whole tx. |
| `commentRepo` struct | type (CHANGED) | `internal/repository/comment.go:63-65` (`db`, `outbox`) | gains `audit repository.AuditLedgerTransactionalRepository` |
| `NewCommentRepository` | constructor (CHANGED) | `internal/repository/comment.go:72-75` | `NewCommentRepository(db *gorm.DB, ob outbox.Repository, audit AuditLedgerTransactionalRepository) CommentRepository`; `nil` audit accepted (fail closed at call time, precedent `account_profile.go:97-99`) |
| `ErrCommentNotFound` | sentinel (NEW) | — | `var ErrCommentNotFound = errors.New(...)`; **must live in a new sibling `internal/repository/comment_audit.go`** (G-1 — file-size gate, see §6); mirrors `repository.ErrNotFound` (db.go:15) |
| `commentRepo.ResolveReportWithAudit` | method (NEW) | — | tx body per interface contract; ≤50 lines, cyclop ≤10 (extract `updateReportAndHide` if needed) |
| `WithCommentAuditLogger` | option (REMOVED) | `internal/service/comment.go:26-29`; sole consumer `internal/app/new.go:202` | deleted; the `CommentOption` type and `NewCommentService` variadic signature remain |
| `commentService.audit` | field (REMOVED) | `internal/service/comment.go:23` | deleted; swallow block :407-426 (the `_ = s.audit.Log(` at :415) deleted, not converted |
| `CommentService` interface / `commentService.ResolveReport` | unchanged | `internal/service/service.go:294`; `comment.go:367-433` | signature and HTTP/gRPC surface unchanged; body re-wired to `buildAuditFact` (audit_ledger.go:82) + `s.comments.ResolveReportWithAudit`, mapping: `repository.ErrCommentNotFound`→404 `comment.not_found`, `repository.ErrNotFound`→404 `comment.report_not_found`, else 500 `comment.resolve_failed` |

### 1.2 Parallel tree — `internal/comment` (unwired)

| Symbol | Kind | Location today | Change |
|---|---|---|---|
| `Repository.ResolveReportWithAudit` | interface method (NEW) | `internal/comment/comment.go:72-91` | added after `ResolveReport` :89; same signature/contract as live, returning `domain.ErrNotFound` / new `comment.ErrCommentNotFound` |
| `repo` struct | type (CHANGED) | `internal/comment/repository.go:108-110` | gains `audit repository.AuditLedgerTransactionalRepository` (new import of `internal/repository` — cycle-free, verified: nothing in `internal/repository` imports `internal/comment`) |
| `NewRepository` | constructor (CHANGED) | `internal/comment/repository.go:111-113` | gains the audit param — **zero call sites** (verified: no caller anywhere) |
| `ErrCommentNotFound` | sentinel (NEW) | — | `internal/comment/repository.go`; 411 lines + ~55 ≈ 466 ≤ 500, no sibling needed |
| `repo.ResolveReportWithAudit` | method (NEW) | — | same tx body as §1.1, single `r.db.WithContext(ctx).Transaction` |
| `WithAuditLogger` | option (REMOVED) | `internal/comment/service.go:37-39`; sole consumer `service_test.go:672` | deleted; `Option` type and `NewService` variadic remain |
| `serviceImpl.audit` / `logResolveAudit` | field/method (REMOVED) | `service.go:26` / :423-436 (swallow at :431) | deleted; swallow surface gone, not converted |
| `WithTenant`, `tenantFrom` | NEW file | — | `internal/comment/context.go` (new sibling — keeps service.go ≤500): `WithTenant(ctx, tenantID string) context.Context` (exported), `tenantFrom(ctx) string` (package-private), `ctxKey`/`tenantKey` locals. D-A resolution: parallel tree has no tenant-from-context (domain has only `WithActor`/`ActorFrom`/`MustActor`, actor.go:13-27; `TokenIdentity.TenantID` exists, nothing stores it in ctx outside `internal/service`) |
| `buildResolveFact` | func (NEW, package-local) | — | fact: `Source:"aero-id"`, `SourceRegion:"local"`, `TenantID: tenantFrom(ctx)`, `PartitionKey: auditTenantPartition(TenantID, actor)` (**G-5 correction** — base partition must be the **actor UID**, not tenant-else-actor: live `normalizeAuditInput` (audit_ledger.go:110-131) yields `AccountID==0 → ActorUID` then prefixes → `tenant:actor`), `ActorUID: actor`, `Action: domain.AuditReportResolve`, `ResourceType:"comment_report"`, `ResourceID: reportID.String()`, `OccurredAt: now`, `Payload: {"comment_id": rep.CommentID.String(), "status": "resolved"|"dismissed"}` (exact vocabulary of :431-435), `EventID` zero (D-F). Legacy `TargetUID` (comment owner) dropped — no fact field; acceptable per pinned acceptance contract |
| `serviceImpl.ResolveReport` | method (CHANGED) | `service.go:371-404` | keeps reads/validation/authz; computes `hide` exactly as today; calls `ResolveReportWithAudit`; mapping: `ErrCommentNotFound`→404 `comment.not_found`, `domain.ErrNotFound`→404 `comment.report_not_found`, else 500 `comment.resolve_failed`; **no new codes, none removed** |

### 1.3 Removed-option reachability (verified complete)

- `WithCommentAuditLogger`: exactly 2 references — definition `internal/service/comment.go:28`, wiring `internal/app/new.go:202`, test consumer `internal/service/comment_test.go:559`. Nothing else.
- `WithAuditLogger` (parallel): definition `internal/comment/service.go:37`, test consumer `internal/comment/service_test.go:672`. The `WithAuditLogger` in `internal/file/file.go:126` and `internal/block/block.go:39` are different packages — untouched.

---

## 2. Constructor sites (3 + 1)

| # | Site | File:line | Change |
|---|---|---|---|
| 1 | `internal/app/new.go:142` | `commentRepo := repository.NewCommentRepository(gdb, outboxRepo)` | pass `auditLedgerRepo` (in scope from `buildAuditStack` :134; typed `repository.FullAuditLedgerRepository` (app/audit.go:36), which embeds `AuditLedgerTransactionalRepository` (audit_wiring.go:9-12) → compiles) |
| 2 | `internal/repository/comment_test.go:22` | `setupCommentRepo`: `NewCommentRepository(db, outbox.NewGormRepository(db))` | pass `nil` (accept-nil contract; unit tests never append) |
| 3 | `internal/repository/search_integration_test.go:27` | `setupSearchRepos` | pass `nil` (search tests never resolve) |
| 4 | `internal/comment/repository.go:111` | `NewRepository(db, ob)` | signature gains the param; **no call sites** (verified zero) |

Also `internal/app/new.go:202` drops `service.WithCommentAuditLogger(auditLedger)` (option removal, not a constructor site).

---

## 3. Interface implementers and mocks (complete set)

`comment.Repository` / `repository.CommentRepository` implementers that must gain `ResolveReportWithAudit`:

| # | Implementer | File:line | Kind | Impact |
|---|---|---|---|---|
| 1 | `commentRepo` | `internal/repository/comment.go:63` | production (live) | gains the method (sibling file per G-1) |
| 2 | `repo` | `internal/comment/repository.go:108` | production (parallel, unwired) | gains the method |
| 3 | `mocks.CommentRepo` | `internal/service/mocks/mocks.go:271` | live mock | gains `ResolveReportWithAuditFn` field + method (default no-op, mirroring `ResolveReport` :366-370). **Compile-forced** by assertion `_ repository.CommentRepository = (*CommentRepo)(nil)` at :635 |
| 4 | `mockCommentRepo` | `internal/comment/service_test.go:27-104` | parallel service-test mock | gains `ResolveReportWithAuditFn` + method (default no-op). **G-2** — in the reviewers' set but absent from the design's migration steps; must be added |
| 5 | `mockCommentRepo` | `internal/search/service_test.go:91` | search-test mock (implements parallel `comment.Repository`, used via `search.go:27,33`) | gains the method (compile fix only — search never resolves; default no-op). **G-2** |

**Not affected** (implement the *service* interfaces, which do not change): `fakeCommentService` `internal/handler/http/comment_test.go:77`, `editCommentSvc` `internal/handler/grpc/parity_edit_test.go:36`, `fakeCommentService` `internal/comment/handler_http_test.go:80`, `editCommentSvc` `internal/comment/handler_grpc_test.go:288`.

No other implementers exist: `SetStatus(ctx context.Context` on comment rows exists only in the 5 rows above plus `operationRepo` (`internal/repository/operation.go:448`, different interface).

---

## 4. Unchanged surfaces (hard boundary — verified untouched)

| Surface | Verification |
|---|---|
| HTTP handlers (both trees) | `internal/handler/http/comment.go:273-289` and `internal/comment/handler_http.go:253-269` call `Service.ResolveReport(ctx, reportID, status)` only; route `:29`/`:27` unchanged; no repo access |
| gRPC handlers (both trees) | `internal/handler/grpc/comment.go:187-199` and `internal/comment/handler_grpc.go:187-199` call `Service.ResolveReport`; proto types untouched |
| Proto | no `.proto` edits; no `make proto`; `aero_id.proto`/`sso.proto` untouched |
| Config | no keys added/removed; ledger config (`cfg.AuditLedger.SigningKey`, `cfg.AuditGovernance`) pre-exists |
| Migrations | no new migration; `audit_ledger` = 000034 (+hardening 000040, request_id 000043), `audit_publications` = 000045 already exist. **Design drift:** §3.5 cites "audit_ledger (000044)" — 000044 is `operation_compensation`; fix the citation, conclusion unchanged |
| Error-code set | `comment.not_found`, `comment.report_not_found`, `comment.status_failed`, `comment.resolve_failed` all pre-exist in both trees (verified in full). New `ErrCommentNotFound` sentinels are internal; they preserve existing 404 codes, never add or remove an `AppError` code |
| Repo methods `ResolveReport` / `SetStatus` | still used: `SetStatus` by `Hide`/`Unhide` (`setStatusAsAuthor` :325-327/:337-339); both kept per AGENTS.md compatibility rule |
| `auditLedgerService.Record`, `drainPublications` (callback_audit.go:38-84), `ClaimPublication`/`FinishPublication`, `AppendInTx` (audit_ledger.go:38-39, :106-114), `AuditLogger` contract (domain/audit.go:10-11), nine legacy `_ = s.audit.Log(` sites outside comment scope | untouched |
| `service.WithIdentity`/`TenantFrom`/`mustTenant`, `domain.WithActor`/`MustActor`, `domain.AuditReportResolve = "comment.report.resolve"` | untouched (new code calls them) |
| Search surface | `internal/search/search.go` unchanged; live search tests share `mocks.CommentRepo` (one fix covers both) |
| Gate script (modified, not a surface) | `Makefile:54` → roots `internal/service internal/comment`; script gains P3 (`^[[:space:]]*(_ = |_, _ = )[A-Za-z_][A-Za-z0-9_.]*\.Log\(`) applied **only** to the comment scope; **P3 must be added to both self-test greps** (positive and negative fixture scans) or the new positive fixture is dead; D-C fixture reshape: delete `_ = s.audit.Log(ctx, event) // legacy diagnostics surface: excluded` from `scripts/testdata/audit-swallow/negative/fixture.go`, add positive `log_fixture.go`, add `if err := s.audit.Log(...)` to the negative |

---

## 5. Observable behavior changes — documented and intended

| # | Change | Today | After | Documented at (design) | Intended |
|---|---|---|---|---|---|
| B-1 | **500 vs 200 on append failure** | post-commit fire-and-forget `Log` (swallow `_ = s.audit.Log` :415/:431); API returns success (200) even when the audit write fails; fact can be silently lost | append failure → tx rollback (report stays `open`, comment stays `normal`) → 500 `comment.resolve_failed`; no silent fact loss | §3.1, FM-1, FM-2, FM-4, §7 | yes — compliance fact is authoritative |
| B-2 | **Hide-leg race: rollback of the resolve** | resolve commits, then hide UPDATE hits a concurrently soft-deleted comment → 404 `comment.not_found`, **report left `resolved`** | hide leg `RowsAffected==0` → new `ErrCommentNotFound` → 404 `comment.not_found`, whole tx rolls back → **report still `open`** (state delta vs today) | FM-3 (rollback behavior); the *delta vs. today* needs one explicit line in §7 PR notes (currently only implied) | yes — atomicity is the point |
| B-3 | **`comment.status_failed` → `comment.resolve_failed` on the resolve path** | non-404 hide-leg DB errors on resolve → 500 `comment.status_failed` | all non-404 resolve-path failures → 500 `comment.resolve_failed` (else-bucket of the new mapping) | §2.1 mapping; §3.3 says "no observable code change in the non-race path" — the hide-leg error-code swap is a same-500 code change worth one explicit PR line | yes |
| B-4 | **Tenant precondition (wired path)** | resolve works for tenant-less identities (audit swallowed anyway) | empty `TenantID` → `validateAuditFact` (audit_ledger.go:342) rejects → 500 + rollback; loud, never silent. Live `bindAuditContext` (audit_ledger.go:97-102) fills only non-empty tenants, so a token without `tenant_id` reaches validation → reachable 500 in prod; consistent with the shipped account-profile WithAudit path | D-A, §3.2, FM-4 | yes |
| B-5 | **Retry duplicates facts** | client retry after a crash between commit and `Log` loses the fact; no dedup | retry appends a second fact (`EventID` zero → `checkAuditDuplicate` short-circuit :157-159, unique constraint can't fire) — parity with today's post-commit behavior | D-F, §3.4 | yes (dedup = direction #2, out of scope) |
| B-6 | **Gate becomes red on today's swallows** | `make check-audit-swallow` green with both swallows invisible (P1/P2 `Log`-blind, `internal/service`-only) | P3 + comment root → red on today's tree (migration step 1 proves it bites); green at 0 after | §2.4, FM-7 | yes |

Nil-audit (FM-2): call-time guard errors before any write → 500 + rollback. Note G-3: a *typed-nil* ledger passes `r.audit == nil` and panics inside the tx — GORM still rolls back (fail-closed holds, but as a panic; unreachable in prod because `buildAuditStack` never yields nil and `validateAuditLedgerConfig` blocks keyless startup). Document the typed-nil case in the method doc.

Ledger outage (FM-8): fail-closed verified end-to-end (key check → validation → advisory lock → chain-head read → INSERT → publication INSERT; every failure returns from the closure → rollback → 500). **G-4:** the FM-8 "Detection" row over-claims — `ObserveAuditIngestFailure` is emitted only in `auditLedgerService.Record` (audit_ledger.go:75); the comment path calls `AppendInTx` directly so no metric fires unless the live service's error mapping adds the one-line call. Either add it or downgrade the row.

---

## 6. Test impact — the complete breakage set (all verified)

### 6.1 Tests that must be rewritten (behavior change on the resolve path)

| Test | File:lines | Why it breaks / changes |
|---|---|---|
| `TestCommentService_ResolveReportEmitsAuditEvent` (pinned) | `internal/comment/service_test.go:652-699` (function opens 652; body 653-693) | enumerated rewrite (design R6): drops `comment.WithAuditLogger` (:672), wires `ResolveReportWithAuditFn`, asserts the fact contract (action/actor/resource/payload + hide intent) and uses `comment.WithTenant` |
| `TestCommentService_ResolveReportEmitsAuditEvent` (live twin) | `internal/service/comment_test.go:539-581` (`WithCommentAuditLogger` at :559) | enumerated rewrite: drops the option, wires `ResolveReportWithAuditFn` on `mocks.CommentRepo`, asserts fact + hide intent; ctx via `service.WithIdentity(&domain.TokenIdentity{TenantID: ..., UID: "author"})` (with `WithActor` only the fact carries an empty tenant — invisible on mocks but wrong contract) |
| **`TestCommentService_ResolveReportTransitionsAndHides` — BREAKS, not in the design's enumerated set** | `internal/comment/service_test.go:562-595` | asserts `SetStatusFn` fires (`hiddenComment` non-nil). New service calls `ResolveReportWithAudit` only; `SetStatusFn` never fires → `hiddenComment == nil` → **test fails**. Must be converted to assert `hide == &domain.CommentStatusHidden` on `ResolveReportWithAuditFn` |
| **`TestCommentService_ResolveReportTransitionsAndHides` — BREAKS, not in the design's enumerated set** | `internal/service/comment_test.go:449-480` | same failure mode (live twin) |

### 6.2 Tests that compile-break (mock interface growth — G-2)

| File | Fix |
|---|---|
| `internal/search/service_test.go:91` | `mockCommentRepo` gains `ResolveReportWithAudit` (no-op) |
| `internal/comment/service_test.go:27-104` | `mockCommentRepo` gains `ResolveReportWithAuditFn` + method |
| `internal/service/mocks/mocks.go:271` | `CommentRepo` gains `ResolveReportWithAuditFn` + method (forced by :635 assertion) |

### 6.3 Tests that stay green but go vacuous (recommend conversion per R6 "hide-intent nil/non-nil")

- `TestCommentService_ResolveReportDismissLeavesCommentVisible` — `internal/comment/service_test.go:598-616` and `internal/service/comment_test.go:485-502`: `SetStatusFn = t.Fatal` never fires (dismiss passes `hide=nil`, mock no-ops) → pass trivially, but the dismiss-hide-intent contract is no longer asserted. Convert to assert `hide == nil` on `ResolveReportWithAuditFn` (or rely on the new repo-level integration leg).

### 6.4 Resolve-path tests verified unaffected

- `ResolveReportByNonAuthorIsForbidden` (both trees), `ResolveReportInvalidStatusIsBadRequest` (both), `ResolveUnknownReportIsNotFound` (parallel :629, live :521) — all fail before any repo write; pass unchanged.
- Handler tests (both trees, http+grpc): fakes implement the unchanged `Service` interface — no edits.
- `TestCommentRepo_ListAndResolveReports` (`internal/repository/comment_test.go:278`) and all other repo tests: use the kept plain `ResolveReport`/`SetStatus` — no edits.
- `internal/repository/search_integration_test.go`: constructor gains `nil`; search behavior untouched.
- `TestAuditLedgerRecordEnqueuesOnly` + integration twin, `TestCallbackAuditRollbackWithoutSigningKey`, `reaction_audit_integration_test.go` (only a comment mentions the resolve shape at :112): untouched.
- No existing integration test wires a ledger into a comment repo (verified) → the B-4 tenant precondition cannot break any current test; it first bites in the new T-5.2 e2e (which uses `service.WithIdentity`) and in production.

### 6.5 New test files (migration step 5)

- `internal/repository/comment_audit_integration_test.go` — T-5.1 (keyless `NewAuditLedgerRepository` → deterministic `AppendInTx` failure at the signing-key guard (audit_ledger.go:107), before any ledger SQL; assertions (b)/(c) — report `open`/`resolved_at IS NULL`, comment `normal` — prove genuine rollback of the domain UPDATEs; (d) 0/0 ledger+publication rows). State in the test comment that the failure fires at the key guard, not mid-`createAuditRows`.
- T-5.2 e2e in `internal/service` (package `service`, tag `integration`) — spy publisher, worker not started, explicit `drainPublications`; (d) 0 facts pre-drain, (e) exactly 1 + terminal `completed` post-drain. Optional strengthening (recommended): a keyless negative leg asserting 500 + report still `open` on real Postgres (the 500 mapping is otherwise covered only by fake-based T-5.1b).
- T-5.1b additions in both service test files (mapping via fakes: append error → `comment.resolve_failed`, `ErrNotFound` → `comment.report_not_found`, `ErrCommentNotFound` → `comment.not_found`; empty-tenant ctx → 500).

---

## 7. Implementation constraints (gate safety)

| Constraint | Value | Status |
|---|---|---|
| `make check-file-sizes` (500) | `internal/repository/comment.go` = **464** lines; +~55-60 → ~519 → **must split** new method + sentinel into sibling `internal/repository/comment_audit.go` (precedent `sync_job_audit.go`) | **G-1 — gate-blocking if ignored** |
| | `internal/comment/repository.go` 411 (+~55 ≈ 466), `internal/comment/service.go` 472 (net ≈ 488), `internal/service/comment.go` 483 (net ≈ 470) | OK |
| funlen/cyclop | new repo method ≤50 lines / ≤10 (extract `updateReportAndHide`); keep `//nolint:cyclop,funlen` on live `ResolveReport` only if complexity unchanged | OK |
| Import direction | `internal/comment` → `internal/repository` acyclic (verified; nothing in `internal/repository` imports `internal/comment`); `internal/comment` must not import `internal/service` | OK |
| Integration gate | run `make test-integration` after repo/ledger/service changes (T-5.1, T-5.2, existing `TestCallbackAuditRollbackWithoutSigningKey`, `TestAuditLedgerRecordEnqueuesOnlyIntegration`) | required |

---

## 8. Design-text corrections (from this inventory + reviewer findings)

1. **G-6 (new, gate-blocking for "no test outside the enumerated set breaks"):** add both `ResolveReportTransitionsAndHides` twins to the R6 rewrite set (§6.1). The design's R6 row enumerates only the pinned tests + mocks.
2. **G-1:** §2.2/§7 "net-neutral file limits" holds only for the two service files; live repo needs the `comment_audit.go` sibling (or `make check-file-sizes` fails).
3. **G-5 (D-D formula):** parallel `buildResolveFact` must use `auditTenantPartition(TenantID, actor)` — base partition = actor UID — to match live `normalizeAuditInput`'s `tenant:actor` for the resolve path; "partition = tenant-else-actor" as written yields `tenant:tenant` and defeats D-D's stated goal.
4. **G-4 (FM-8 detection):** either add `ObserveAuditIngestFailure` in the live service's append-error mapping or downgrade the FM-8 detection row to "generic 500"; as written no metric fires on the comment path.
5. **G-3:** document the typed-nil-ledger case in the new method's doc (panics inside tx → GORM rollback → still fail-closed, but as a panic).
6. **Migration-number drift:** §3.5 "audit_ledger (000044)" → 000034 (+000040 hardened, +000043 request_id); `audit_publications` 000045 ✓. Conclusion unchanged.
7. **B-2/B-3 PR notes:** add the explicit hide-race state-delta line (report rolls back to `open` on the 404) and the `comment.status_failed`→`comment.resolve_failed` code swap on that leg to §7.
8. **Pinned-test citation:** function opens at 652 (not 653); body 653-693. Live twin opens at 539; option at 559.
9. **§2.4:** P3 must join `$P1_PATTERN|$P2_PATTERN` in **both** self-test greps (positive and negative) or the new positive fixture is inert.
