# Requirements Specification — internal/comment: anchored in-transaction audit coverage for every comment mutation (Create/Edit/Delete/Like/Unlike/Report/Hide/Unhide)

| | |
|---|---|
| Module | `internal/comment` (unwired parallel tree; the live twin `internal/service/comment.go` + `internal/repository/comment_audit.go` owns the equivalent moderation surface and is the model) |
| Direction (selected) | *Extend anchored in-transaction audit coverage to every comment mutation (Create/Edit/Delete/Like/Report/Hide/Unhide)* (analysis direction #2) |
| Source analysis | `docs/auto/analyses/internal-comment-a2d7c3b3.json` |
| Campaign | `aero-id-b2` (B2 item 1: in-tx audit; B2 item 2: no swallowed audit calls) |
| Related specs | `docs/requirements/2026-08-08-internal-comment-resolve-report-with-audit.md` (**predecessor, direction #1 — ResolveReport conversion; a hard dependency of T-10.2, see §3 R0**), `docs/requirements/2026-08-06-internal-comment-transactional-report-resolution-audit.md`, `docs/requirements/2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop.md` |
| Date | 2026-08-08 |
| Status | Requirements — every cited file/symbol re-verified against the working tree at HEAD `b005409` (only the predecessor requirements doc is dirty in `git status`; the predecessor's ResolveReport conversion has **not** landed — see §3 R0) |

## 1. Evidence verification

| # | Citation (direction) | Verification against current tree |
|---|---|---|
| 1 | `internal/comment/service.go` Create (:70-112), Edit (:151-184), Delete (:186-208), react (:246-278), Report (:280-317), setStatusAsAuthor/Hide/Unhide (:319-349) — no audit calls | ✅ **Verified with drift.** Actual locations: `Create` :87-132, `Edit` :179-211, `Delete` :213-231, `Like` :234-239 / `Unlike` :242-247 / `react` :250-275, `Report` :277-310, `Hide` :312-314 / `Unhide` :316-318 / `setStatusAsAuthor` :320-342, `ListReports` :344-369, `ResolveReport` :371-403. None of the mutation paths calls the audit logger — the module's only audit call is `_ = s.audit.Log(...)` inside `logResolveAudit` :423-437 (at :431). Audit wiring: `audit domain.AuditLogger` field :20, `WithAuditLogger` option :37-40. |
| 2 | `internal/comment/comment.go` — `Repository` interface, "no WithAudit methods beyond CreateWithReplyBump" | ✅ **Verified exact.** `Repository` interface :72-94 (16 methods); `CreateWithReplyBump` :74-77 is the only `*With*` method. `Service` interface :96-109. `ErrNestingTooDeep` :17, `NewComment` :24-39. |
| 3 | `internal/domain/audit.go:30` — single action constant (`AuditReportResolve`); new action vocabulary needed | ✅ **Verified exact.** `AuditReportResolve = "comment.report.resolve"` at :30; it is the only comment-family action among the 10 constants :27-36. Dead-tree `AuditLogger` interface :22-25 with the quarantine note; `AuditEvent` struct :41-50. |
| 4 | `scripts/check-anchored-event-ids.sh` — R-1/R-2 derivation contract | ✅ **Verified.** R-1 bans `EventID: idgen.NewID()` in non-test Go except the C2 allowlist (script :28-30); R-2 bans `uuid.NewSHA1`/`uuid.NewHash` outside `internal/pkg/idgen/idgen.go` + `internal/service/projection_replication.go`, zero-part `Anchor()` calls, raw digest casts, and namespace constants outside those two files; `AnchorNamespace` must be defined exactly once (script :63-69). Doctrine (idgen.go :15-25): every anchor site has fixed arity ≥ 4, NUL-separated parts, parts never reused across part contracts. Makefile `check-anchored-event-ids` (:76-77) runs roots `internal cmd` — **`internal/comment` is already scanned** (the script descends `grep -rn`), so new code in the module is gate-visible today. |
| 5 | `internal/pkg/idgen` — `Anchor`, `RenderAnchorTime` | ✅ **Verified.** `Anchor(parts ...string) ID` (idgen.go :28-37) = `uuid.NewSHA1(AnchorNamespace, []byte(strings.Join(parts, "\x00")))`, panics on zero parts or NUL bytes; `RenderAnchorTime(t *time.Time) string` (:40-46) — UTC, six fractional digits, `Z` (contract R7); `AnchorNamespace` :16-20. House part-contract precedents: `internal/service/legacy_cutover.go:143` (`("legacy.cutover.set", tenant, status, previous)` — arity 4), `internal/service/audit_hold.go:164` (`("audit_hold.set", tenant, scopeType, scopeID, reason, RenderAnchorTime(expiresAt))` — arity 6), `internal/service/sync_audit.go:44` (`("sync.job.finished", tenant, jobID, status)` — arity 4), `internal/service/operation_audit.go:68` (dynamic `parts...`). Tenant occupies part 2 in every house site. |
| 6 | `internal/repository/comment_report_anchor_integration_test.go` — live anchor precedent | ✅ **Verified.** `TestReportResolveAnchoredEventIDAppendsOnce` (:27-61): EventID = reportID; second `ledger.Append` returns `repository.ErrAuditDuplicate` (:51); exactly 1 `audit_ledger` row + 1 `audit_publications` row (:53-58); `chain_sequence` stable across the duplicate (:60-62). `TestReportResolveAnchoredFactListRoundTrip` (:64). Helper `startAnchorLedger` lives in `internal/repository/anchor_dedup_integration_test.go:25`. |
| 7 | `internal/repository/account_provision_audit.go:31-52` — `XxxWithAudit` repo-method precedent | ✅ **Verified.** Interface `AccountProvisionAuditRepository` :9-17; `EnsureByIdentityWithAudit` :31-58 — one `r.db.WithContext(ctx).Transaction(...)`, fact via `r.audit.AppendInTx(ctx, tx, fact)` **appended only when the call actually created the row** (`created` flag), with the exact rationale comment (:5-8: "a retried or racing provision can never roll back its own touch with ErrAuditDuplicate"). **This is the conditional-append model for Create/Report/Like.** |
| 8 | `internal/repository/callback_audit.go` — `XxxWithAudit` repo-method precedents | ✅ **Verified.** `CreateSubscriptionWithAudit` :33-64: domain write, then `r.audit.AppendInTx(ctx, tx, fact)` with in-tx `ErrAuditDuplicate` tolerance **after** the domain write (contract §4.2; the comment at :47-50 pins why the swallow "can never mask a failed domain write"). `DeleteSubscriptionWithAudit` :66+ appends only when a row was actually deleted. |
| 9 | `internal/repository/comment_audit.go` — live in-transaction comment audit surface | ✅ **Verified** (model for the repo-side shape, not a change target). `CommentAuditRepository` interface :10-24; `ResolveReportWithAudit` :31-56: report UPDATE (`RowsAffected == 0` → `ErrNotFound`, no fact), optional hide UPDATE (no RowsAffected check — 0-row match is a no-op), then `AppendInTx` with in-tx `ErrAuditDuplicate` tolerance (:51-54). |
| 10 | `internal/repository/audit_wiring_integration_test.go` — T-5 pattern | ✅ **Verified.** `TestAuditWiringHappyPath` (:60-173, AC3a): every `*WithAudit` mutation commits its fact in the SAME transaction — one ledger row + one publication row per call. `TestAuditWiringRollbackAtomicity` (:175+, AC3b): forced append failure → WHOLE mutation rolls back (domain row unchanged, exactly 1 ledger row). Helpers `auditWiringFact`, `countAuditLedgerRows`, `countAuditPublicationRows` (:23-58). |
| 11 | `internal/service/callback_audit.go:45` — `drainPublications` | ✅ **Verified exact.** `func (s *callbackService) drainPublications(ctx context.Context)` at :45; claim → bulkhead-bounded `finishPublicationAttempt` → `publishAuditFact` → terminal `completed`/`dead_letter` settlement. Test harness: `internal/service/callback_integration_test.go` (`TestFirstFactDrainDeliversExactlyOnce` :392, `TestDrainExhaustsAttemptsIntoDeadLetter` :207 — `NewCallbackService(...)` + `SetAuditPublicationRepository` + `SetAuditPublishers` + `svc.drainPublications(ctx)`, `recordingAuditPublisher` fake) and `internal/service/callback_audit_test.go:200,258` (unit). |
| 12 | `internal/comment/service_test.go` — `TestCommentService_ResolveReportEmitsAuditEvent` (`logged.ID == reportID`, "deterministic dedup anchor") | ✅ **Verified exact.** Test at :652-695; failure guard `logged.ID != reportID` with message "deterministic dedup anchor" at :690 (i.e., the assertion is `logged.ID == reportID`). `mockCommentRepo` (:18-103) implements `comment.Repository` — **must gain the 7 new WithAudit methods (compile ripple)**; `mockAuditor` (:718-725) is the `domain.AuditLogger` fake used as the audit enable flag. |
| 13 | Module wiring status (importers of `internal/comment`) | ✅ **Verified.** Non-test importers: only `internal/search/search.go:27,33` (holds `comment.Repository` as a field) and `internal/search/handler_grpc.go:8,41` (PB conversion). `internal/search/service_test.go:59-94` `mockCommentRepo` implements the interface — **second compile ripple**. Zero `comment.NewRepository`/`comment.NewService` construction outside the package (grep: 0 hits). `internal/repository` non-test files import neither `internal/comment` nor `internal/search` — no cycle for the new `internal/comment → internal/repository` import (predecessor §12 verified the same). |
| 14 | `internal/comment/repository.go` — dead-tree repo, `CreateWithReplyBump` in-tx precedent | ✅ **Verified.** `repo` struct :106-110 holds only `db *gorm.DB` + `outbox outbox.Repository` (**no audit ledger**); `NewRepository(db, ob)` :111. `CreateWithReplyBump` :122-147 (comment insert + reply_count bump + outbox append in one `Transaction`); `SoftDelete` :220-229, `UpdateContent` :231-246, `Like` :248-273 (already transactional, ON CONFLICT DO NOTHING), `Unlike` :275-299, `CreateReport` :301-318 (`domain.ErrDuplicate` on 0 rows), `SetStatus` :320-331 (`domain.ErrNotFound` on 0 rows), `ResolveReport` :373-388. Sentinels are `domain.ErrNotFound`/`domain.ErrDuplicate`. File is 411 lines — **the 7 new methods go in a sibling `repository_audit.go` (500-line gate)**. |
| 15 | AGENTS.md exemption ("until migration") | ✅ **Verified.** "The audit-coverage invariant covers every operation that emits an audit fact, not every mutation. These legacy compatibility surfaces currently emit zero audit facts … and are exempt only until migration: file create/delete/update, message send/edit/react, comment create/edit/report, preferences, region promotion, and projection replication." This direction is the comment-mutation leg of that migration. |

### Runtime evidence captured (2026-08-08)

- `bash scripts/check-audit-swallow.sh internal/comment` → **exit 1**, sole violation `internal/comment/service.go:431: _ = s.audit.Log(...)` (P1). The module is a documented exclusion from the Makefile roots (`check-audit-swallow` scans `internal/service internal/repository internal/app internal/storage internal/block cmd`, Makefile :67-69), so today's green gate does not see it.
- `bash scripts/check-anchored-event-ids.sh internal/comment` → **exit 0** (vacuously: the module contains no audit code). The Makefile root `internal` already covers the module; the gate is live for any new code.
- `go test ./internal/comment/ -count=1` → **ok** (baseline).
- `git status`: clean except `docs/requirements/2026-08-08-internal-comment-resolve-report-with-audit.md` (in-flight predecessor).

## 2. Problem statement (current state)

`ResolveReport` is the only state-changing operation in `internal/comment` that emits an audit fact (and its only audit write is the swallowed `_ =` at `service.go:431`). The module's other eight mutation paths — `Create`, `Edit`, `Delete`, `Like`/`Unlike`, `Report`, `Hide`/`Unhide` — perform durable domain writes with **zero audit events**, and the module is the documented exclusion from the swallowed-audit gate's scan roots. AGENTS.md's zero-audit exemption for "comment create/edit/report" holds only "until migration"; the B2 audit campaign is that migration, and this direction performs it for the comment mutation surface:

1. **Compliance gap (B2 item 1).** Every comment mutation commits a state change without a durable audit fact: content creation/editing, soft delete, reactions, reports (moderation evidence), and author visibility changes are all invisible to the compliance ledger. `ListReports` is a read and stays out of contract (§5).
2. **The migration shape is already proven in the live layer.** `internal/repository/comment_audit.go` (`ResolveReportWithAudit`), `account_provision_audit.go` (conditional append only-when-created), `callback_audit.go` (in-tx `ErrAuditDuplicate` tolerance, contract §4.2), `audit_wiring_integration_test.go` (atomicity proof), and `comment_report_anchor_integration_test.go` (anchored retry dedup) define every pattern this direction needs. The dead tree's own `CreateWithReplyBump` (`repository.go:122-147`) already owns the in-transaction shape.
3. **Anchored event IDs are mandatory and gate-enforced.** `scripts/check-anchored-event-ids.sh` R-1 bans random minting (`EventID: idgen.NewID()`) and R-2 pins derivation to `idgen.Anchor` (fixed arity ≥ 4, NUL-separated) or an "already-deterministic v7 receipt" (the script's own doctrine; precedents: account provision `EventID = accountID`, callback create/delete `EventID = subscription.ID`, report resolve `EventID = reportID`). The new sites must pick one of these two classes per mutation — a resource ID alone is **not** safe for multi-intent resources (Edit→Edit would collide with Create under the `(tenant_id, source, event_id)` dedup key and roll back legitimate writes), so the repeatable-intent mutations need action-scoped `idgen.Anchor` derivations.
4. **Action vocabulary is missing.** `internal/domain/audit.go:30` defines only `comment.report.resolve`; the eight new paths need eight new constants.

## 3. Requirements

### R0 — Dependency: ResolveReport conversion (predecessor, direction #1) is required for T-10.2

The supplied T-10.2 acceptance ("gate scan of internal/comment stays at 0 swallowed/bare audit calls") cannot pass on today's tree: `service.go:431` is a P1 match. The predecessor spec `2026-08-08-internal-comment-resolve-report-with-audit.md` (R2.3: delete `logResolveAudit`; R1: `ResolveReportWithAudit` on the dead-tree repo; R4: Makefile root + docs) is the designated fix and its campaign has passed design gate. **Implementation order: land the predecessor conversion first (or fold it into the same change); this spec builds on its base — the dead-tree repo's `audit repository.AuditLedgerTransactionalRepository` field (predecessor R1.2), the `if s.audit != nil` service branch pattern (predecessor R2.1), the `internal/comment/audit_fact.go` fact-builder file, and the Makefile root `internal/comment` (predecessor R4.1).** If the predecessor lands first, §3.2/§3.3 below extend the already-added field/pattern; nothing below duplicates its requirements.

### R1 — Action vocabulary: eight new audit action constants (`internal/domain/audit.go`)

The `const` block :27-36 gains, next to `AuditReportResolve`:

```go
AuditCommentCreate   = "comment.create"
AuditCommentEdit     = "comment.edit"
AuditCommentDelete   = "comment.delete"
AuditCommentLike     = "comment.like"
AuditCommentUnlike   = "comment.unlike"
AuditCommentReport   = "comment.report.create" // report submission; resolution stays comment.report.resolve
AuditCommentHide     = "comment.hide"
AuditCommentUnhide   = "comment.unhide"
```

House-style dotted noun.noun.verb vocabulary, consistent with `file.share.grant`, `comment.report.resolve`, `user.block`/`user.unblock`, `message.delete`.

### R2 — Dead-tree repository: seven `XxxWithAudit` methods in a new sibling file `internal/comment/repository_audit.go`

Model: live `comment_audit.go:31-56`, `account_provision_audit.go:31-58` (conditional append), `callback_audit.go:33-64` (in-tx `ErrAuditDuplicate` tolerance). `repository.go` is 411 lines; the new file keeps the 500-line/file gate.

2.1 The `Repository` interface (`comment.go:72-94`) gains seven methods (all `*domain.AuditFact`-carrying; `fact` is appended **last** in the same GORM transaction as the domain write):

```go
CreateWithReplyBumpWithAudit(ctx context.Context, c *Comment, evt *outbox.Event, fact *domain.AuditFact) error
UpdateContentWithAudit(ctx context.Context, id domain.ID, content string, at time.Time, fact *domain.AuditFact) error
SoftDeleteWithAudit(ctx context.Context, id domain.ID, at time.Time, fact *domain.AuditFact) error
LikeWithAudit(ctx context.Context, commentID domain.ID, uid string, at time.Time, fact *domain.AuditFact) (likeCount int, err error)
UnlikeWithAudit(ctx context.Context, commentID domain.ID, uid string, at time.Time, fact *domain.AuditFact) (likeCount int, err error)
CreateReportWithAudit(ctx context.Context, r *CommentReport, fact *domain.AuditFact) error
SetStatusWithAudit(ctx context.Context, commentID domain.ID, status domain.CommentStatus, at time.Time, fact *domain.AuditFact) error
```

2.2 `repo` (struct `repository.go:106-110`) gains `audit repository.AuditLedgerTransactionalRepository`; `NewRepository(db *gorm.DB, ob outbox.Repository, audit repository.AuditLedgerTransactionalRepository) Repository` (predecessor R1.2 made the same change — merge, do not duplicate). The import `github.com/aero/aero-id/internal/repository` is needed for the interface type and the `ErrAuditDuplicate` sentinel identity; no cycle (verified, §1 #13).

2.3 Shared in-method contract (each method, in order):
1. One `r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { ... })` containing the domain write(s) **verbatim from the plain sibling** (`repository.go` lines cited in §1 #14: `CreateWithReplyBump` :122-147, `UpdateContent` :231-246, `SoftDelete` :220-229, `Like` :248-273, `Unlike` :275-299, `CreateReport` :301-318, `SetStatus` :320-331), with the `at` timestamp threaded into the row where the plain method mints its own (`SoftDelete` uses `.UpdateColumn("deleted_at", at)` instead of GORM's `Delete`; `Like`/`Unlike` stamp `commentLikeRow.CreatedAt = at`).
2. **Append condition** (mirror of `account_provision_audit.go:5-8,39-44` — never append for a no-op):
   - `CreateWithReplyBumpWithAudit` — append always (insert always affects 1 row);
   - `CreateReportWithAudit` — append **only when** the insert affected 1 row; a repeat report (`RowsAffected == 0`) returns `domain.ErrDuplicate` with **no** fact (today's `:315-317` contract);
   - `LikeWithAudit`/`UnlikeWithAudit` — append **only when** the like-row insert/delete affected 1 row; a repeat like / non-liked unlike stays a silent no-op with **no** fact (today's semantics);
   - `UpdateContentWithAudit`/`SoftDeleteWithAudit`/`SetStatusWithAudit` — append only when `RowsAffected == 1`; `RowsAffected == 0` returns `domain.ErrNotFound` with **no** fact (today's contracts).
3. **Nil-audit guard before the append**: `if r.audit == nil { return fmt.Errorf("transactional audit repository is not configured") }` — the tx then rolls back (predecessor R1.3, precedent `internal/repository/account_profile.go:97-99`).
4. `r.audit.AppendInTx(ctx, tx, fact)` with in-tx `ErrAuditDuplicate` tolerance **only after the domain writes, inside the same tx** (contract §4.2; verbatim pattern `callback_audit.go:47-54` / `comment_audit.go:51-54`); any other append error wrapped (`fmt.Errorf("append comment audit: %w", err)`) rolls the whole tx back.

2.4 Plain methods stay byte-identical and are still used by the unconfigured service path (AGENTS.md: preserve current APIs).

### R3 — Dead-tree service: audit-configured branches, anchored facts, fail-closed (`internal/comment/service.go` + new `internal/comment/audit_fact.go`)

3.1 Each of the eight mutation paths (`Create` :87-132, `Edit` :179-211, `Delete` :213-231, `react` :250-275 (serves `Like`/`Unlike`), `Report` :277-310, `setStatusAsAuthor` :320-342 (serves `Hide`/`Unhide`)) gains the live-twin branch shape (`internal/service/comment.go:407-426` model): **`if s.audit != nil`** → build the fact (R3.2), call the `XxxWithAudit` repo method, map errors (R3.3), return; **else** → the existing plain call verbatim, zero facts. The `audit domain.AuditLogger` field (:20) and `WithAuditLogger` (:37-40) remain the enable flag exactly as the predecessor R2.1 established for `ResolveReport`; **no `s.audit.Log` call is added anywhere** (the ledger append happens in the repo; T-10.2's "every audit call propagates via `if err :=`" is satisfied by the checked WithAudit call).

3.2 Fact builders live in the new sibling `internal/comment/audit_fact.go` (predecessor's file; keeps `service.go` — 473 lines today — ≤ 500 and functions ≤ 50/funlen, cyclop ≤ 10; extract each branch into a helper there if a method exceeds limits). Shared `buildAuditFactBase(ctx, actor, action, resourceType, resourceID, at)` sets: `Source: "aero-id"`, `SourceRegion: "local"`, `ActorUID: actor`, `ResourceType`, `ResourceID`, `OccurredAt: at`, and `TenantID`/`PartitionKey` from a tenant bound in context when present, else the actor-UID fallback (predecessor R2.2 convention — the dead tree has no tenant helper; `validateAuditFact` requires non-empty `TenantID`/`PartitionKey`). Payloads are allow-listed and **never carry content**: `comment_id` (+ `target_type`/`target_id` for create; + `status` for hide/unhide) — AGENTS.md redaction ("never record … unrestricted message bodies").

**Per-mutation EventID policy (frozen part contracts; the two classes allowed by the R-1 doctrine):**

| Mutation | Action | EventID | Class |
|---|---|---|---|
| Create | `comment.create` | `c.ID` — the service pre-mints it at `service.go:117` before the repo call (receipt precedent: account provision / callback create) | deterministic v7 receipt |
| Edit | `comment.edit` | `idgen.Anchor("comment.edit", tenantID, commentID.String(), actor, idgen.RenderAnchorTime(&at))` — `at` is the same `now` threaded into `UpdateContent` (:200-203) | Anchor v5, arity 5 |
| Delete | `comment.delete` | `idgen.Anchor("comment.delete", tenantID, commentID.String(), actor, idgen.RenderAnchorTime(&at))` | Anchor v5, arity 5 |
| Like | `comment.like` | `idgen.Anchor("comment.like", tenantID, commentID.String(), uid, idgen.RenderAnchorTime(&at))` | Anchor v5, arity 5 |
| Unlike | `comment.unlike` | `idgen.Anchor("comment.unlike", tenantID, commentID.String(), uid, idgen.RenderAnchorTime(&at))` | Anchor v5, arity 5 |
| Report | `comment.report.create` | `report.ID` — the service pre-mints it at :297-302 and threads it into the row (receipt class) | deterministic v7 receipt |
| Hide | `comment.hide` | `idgen.Anchor("comment.hide", tenantID, commentID.String(), actor, idgen.RenderAnchorTime(&at))` | Anchor v5, arity 5 |
| Unhide | `comment.unhide` | `idgen.Anchor("comment.unhide", tenantID, commentID.String(), actor, idgen.RenderAnchorTime(&at))` | Anchor v5, arity 5 |

Rationale, load-bearing: (a) the receipt class is safe only where one intent per resource-ID exists — create (ID minted per attempt) and report (unique `(comment_id, reporter_uid)` makes a second intent fail with 409 before any append); (b) the six repeatable-intent mutations must encode action + resource + actor + the threaded intent timestamp so a retry of the **same** intent re-derives the same `event_id` (dedup fold) while a **different** intent (new `at`) cannot collide under `uq_audit_ledger_tenant_event` and roll back a legitimate write — the D3 hazard named in `internal/service/audit_fact.go:19-22`; (c) `tenantID` occupies part 2 per house convention (legacy_cutover/audit_hold/sync_audit); (d) part contracts are **frozen** — `idgen.Anchor` doctrine: "never reuse it for a derivation site with a different part contract".

3.3 Error mapping in the audit-configured branch (each mutation keeps its existing domain-sentinel codes; only the append path changes):
- `domain.ErrNotFound` → the mutation's existing 404 (`comment.not_found` / `comment.report_not_found`), **no fact was appended** (repo appended nothing);
- `domain.ErrDuplicate` (report) → the existing 409 `comment.already_reported`, no fact;
- any other error (the append failed → the whole tx rolled back) → **`apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)`** — the B2 fail-closed contract (HTTP 503), byte-identical phrasing to the live layer's audit sites (`internal/service/account.go:161,191`) and the predecessor T-5; the domain write provably did not commit.

3.4 `ListReports` (:344-369), `List` (:134-151), `Replies` (:153-177) — read paths, no changes, no facts (§5). `ResolveReport` — predecessor R0's conversion only.

3.5 Compile ripple (mechanical, test-only): `internal/comment/service_test.go` `mockCommentRepo` (:18-103) and `internal/search/service_test.go` `mockCommentRepo` (:59-94) gain the seven new methods (nil-default no-op style, mirroring `ResolveReportFn` at `service_test.go:97-101`).

### R4 — Tests (supplied acceptance checks preserved and made testable)

4.1 **T-4 unit mirrors (per mutation, `internal/comment/service_test.go`).** For each of the eight paths, new or extended tests follow `TestCommentService_ResolveReportEmitsAuditEvent` (:652-695) exactly: with `comment.WithAuditLogger(mockAuditor)` wired, assert exactly **one** WithAudit repo invocation, the action constant, `ActorUID`, `ResourceType`/`ResourceID`, and — the "deterministic dedup anchor" assertion — `fact.EventID` equals (receipt class) the pre-minted `c.ID`/`report.ID`, or (Anchor class) `idgen.Anchor(...)` recomputed in the test from the same five parts and the same `at` (`at` must be injectable/observable — thread it through the mock's captured arguments). Existing plain-path tests (construct the service without the audit option) pass unchanged.

4.2 **T-4 fail-closed unit (per mutation).** WithAudit repo fake returns a non-domain error → the service returns `*apperr.AppError{Code: "audit.ingest_failed", HTTP: 503}` and the plain repo method is **never** invoked (no fallback to a committing non-audited path when audit is configured) — mirror of the predecessor's fail-closed unit (predecessor R3.2).

4.3 **T-5 integration — atomic commit/rollback per mutation (new `internal/comment/repository_integration_test.go`, `//go:build integration`, `package comment`).** Harness: `testpg.Start`/`Migrate`/`RepoRoot`, real `repository.NewAuditLedgerRepository(db, AuditLedgerConfig{SigningKey: ...})`, `NewRepository(db, ob, ledger)`; package-local count helpers in the style of `audit_wiring_integration_test.go:23-58` (`auditWiringFact`, `countAuditLedgerRows`, `countAuditPublicationRows`). For **each** of the eight mutations:
1. **Commit side**: seed prerequisites (comment row; report needs `(comment_id, reporter_uid)` uniqueness); call the WithAudit method with a fact carrying the expected anchored EventID; assert the domain state changed **and** exactly 1 `audit_ledger` row **and** exactly 1 `audit_publications` row for that event_id (pending) — the audit_wiring AC3a shape.
2. **Rollback side**: same call against a keyless ledger (`AuditLedgerConfig{}` → `AppendInTx` fails closed, precedent `callback_audit_integration_test.go` keyless cases + predecessor R3.3 case 3) → the call returns an error **and** the domain row is unchanged (create: no comment row; edit: old content/`edited_at`; delete: `deleted_at` still NULL; like: no like row, `like_count` unchanged; report: no report row; hide/unhide: status unchanged) **and** 0 ledger + 0 publication rows — the audit_wiring AC3b shape: the domain write and the fact commit or roll back together.
3. **No-op condition** (conditional-append paths): repeat `LikeWithAudit` (same uid) / repeat `CreateReportWithAudit` / `SoftDeleteWithAudit` on a soft-deleted row / `UpdateContentWithAudit`-style 0-row → nil (or `ErrDuplicate`/`ErrNotFound` per §2.3) with **zero** new facts and 1 ledger row total.
4. **Anchored retry fold**: a second call with the identical fact (same EventID) → returns nil (in-tx `ErrAuditDuplicate` tolerance) with exactly 1 ledger row — the dedup contract.

4.4 **T-4/T-10.1 dedup receipt (new `internal/comment/anchor_dedup_integration_test.go`).** For each of the eight EventIDs (table-driven), mirror `TestReportResolveAnchoredEventIDAppendsOnce` (`comment_report_anchor_integration_test.go:27-61`): first `ledger.Append` succeeds; second `ledger.Append` with the same (tenant, source, event_id) returns `repository.ErrAuditDuplicate`; exactly 1 ledger row + 1 publication row; `chain_sequence` unchanged. This is the "duplicate append rejected via the ledger's unique receipt" leg of T-10.1.

4.5 **T-10.1 drain delivery (new `internal/service/comment_audit_drain_integration_test.go`, `//go:build integration`, `package service`).** Model: `TestFirstFactDrainDeliversExactlyOnce` (`callback_integration_test.go:392`) + `TestDrainExhaustsAttemptsIntoDeadLetter` harness (:207-232: `NewCallbackService(...)` + `SetAuditPublicationRepository` + `SetAuditPublishers(recordingAuditPublisher)` + `svc.drainPublications(ctx)`). Seed a comment and append one fact **through the module's own WithAudit surface** (e.g., `UpdateContentWithAudit` on the dead-tree repo — the end-to-end wiring proof), then drive `drainPublications` (`callback_audit.go:45`): assert the `audit_publications` row transitions to `completed`, the publisher received exactly one fact with the anchored EventID and action, and a second drain claims nothing. Optional: seed one enabled matching callback subscription to also assert exactly one `callback_deliveries` row (dedup point).

4.6 `make test-integration` (Makefile:15) picks up the new `//go:build integration` files automatically.

### R5 — Gates (T-4 and T-10.2, enforced)

5.1 **T-4**: `bash scripts/check-anchored-event-ids.sh internal/comment` exits 0 with zero violations (already wired via `make check-anchored-event-ids` roots `internal cmd` — the `internal` root descends into the module). New `internal/comment` non-test code contains no `EventID: idgen.NewID()`, no raw `uuid.NewSHA1`/`uuid.NewHash`, no zero-part `Anchor()`, no namespace constants — only the two allowed classes of §3.2.

5.2 **T-10.2**: `bash scripts/check-audit-swallow.sh internal/comment` exits 0 with 0 matches. This requires R0 (the predecessor's `logResolveAudit` deletion) — the only P1 match on today's tree — plus §3.1's "no `s.audit.Log` calls, checked WithAudit calls only". Enforcement durability: the Makefile `check-audit-swallow` root list gains `internal/comment` per the predecessor's R4.1 (shared change; the line already carries the quarantine campaign's `internal/block` root — merge, do not overwrite). Script header exclusion list and the `internal/domain/audit.go` quarantine note updated per predecessor R4.3 (the four-site swallow enumeration becomes three: `internal/file/service_share.go:45,75; internal/message/service.go:289`).

## 4. Acceptance mapping

| Supplied acceptance | Requirement | Testable pass criterion |
|---|---|---|
| **T-5**: for each mutation, an integration test asserts exactly one audit fact is appended in the same GORM transaction as the domain write and that rolling back the tx removes both the row and the fact (pattern of `internal/repository/audit_wiring_integration_test.go`) | R2.3, R4.3 | `go test -tags=integration ./internal/comment/ -count=1`: for all 8 mutations, (a) commit case = 1 `audit_ledger` row + 1 `audit_publications` row + domain state changed; (b) keyless-ledger case = error + domain row unchanged + 0/0 ledger/publication rows; (c) no-op/conditional-append cases append zero facts; (d) retry-fold = nil + exactly 1 ledger row |
| **T-4**: every new AuditEvent ID is anchor-derived (no `EventID: idgen.NewID()`); run `scripts/check-anchored-event-ids.sh internal/comment` — 0 violations, mirroring `TestCommentService_ResolveReportEmitsAuditEvent` (`logged.ID == reportID`, "deterministic dedup anchor") | R3.2, R4.1, R4.4, R5.1 | (a) `bash scripts/check-anchored-event-ids.sh internal/comment` exits 0; (b) `make check-anchored-event-ids` (roots `internal cmd`) exits 0; (c) `go test ./internal/comment/ -count=1` — per-mutation unit tests assert `fact.EventID` == pre-minted `c.ID`/`report.ID` or recomputed `idgen.Anchor(...)`, mirroring the :690 assertion; (d) table-driven dedup test: duplicate `ledger.Append` per EventID → `ErrAuditDuplicate`, 1 ledger + 1 publication row, stable `chain_sequence` |
| **T-10.1**: drainPublications-based delivery test — facts land in `audit_publications` and are drained by `internal/service/callback_audit.go:45`, with duplicate append rejected via the ledger's unique receipt (`ErrAuditDuplicate`) | R4.4, R4.5 | (a) `go test -tags=integration ./internal/service/ -run TestCommentMutationAuditFactDrains -count=1` — fact seeded via the module's WithAudit method, `drainPublications` settles the publication to `completed`, publisher observed the anchored EventID, second drain claims nothing; (b) §4.4 table-driven `ErrAuditDuplicate` rejection (8 EventIDs) |
| **T-10.2**: gate scan of `internal/comment` stays at 0 swallowed/bare audit calls | R0, R3.1, R5.2 | (a) `bash scripts/check-audit-swallow.sh internal/comment` exits 0 with 0 matches (requires the predecessor's `logResolveAudit` deletion); (b) `make check-audit-swallow` exits 0 with `internal/comment` in the root list (predecessor R4.1); (c) mutation check: reintroducing `_ = s.audit.Log(` anywhere in the module turns the target red listing the file:line |

## 5. Scope boundaries (explicit non-goals)

- **Read paths** — `List`, `Replies`, `ListReports`: no audit facts. The direction's problem sentence names `ListReports` as producing "zero audit events", but the title, the T-5 acceptance ("for each mutation"), and AGENTS.md ("routine high-volume reads may use metadata-only access audit") scope the migration to state-changing mutations. Access-audit mode for comment reads is a separate decision.
- **`ResolveReport`**: the predecessor (direction #1) owns its conversion; this direction adds/alters nothing about the resolve fact beyond R0's dependency. Its `comment.report.resolve` fact is untouched.
- **The live layer** (`internal/service/comment.go`, `internal/repository/comment.go`/`comment_audit.go`, `internal/service/mocks`): untouched. `CommentAuditRepository` keeps its single `ResolveReportWithAudit`; the live comment service's mutation methods (which today emit no facts either — `internal/service/comment.go:80,204,238,262,299,308`) are **out of scope**: the module under migration is `internal/comment`. The T-10.1 drain test imports the dead-tree repo for seeding; no live-layer behavior changes.
- **`internal/search`**: mechanical mock-completion only (R3.5); no behavior changes.
- **No migrations, no handler/gRPC/HTTP contract changes, no changes to `AppendInTx`, `drainPublications`, ledger semantics, or the audit-manifest gate** (it scans `internal/service` only — `check-audit-manifest.sh` :15,21-22).
- **No payload content fields** (edit content, report reason, message bodies are never persisted to the ledger — AGENTS.md redaction).
- **No service-level idempotency keys** for comment mutations (dead-tree surface); retry semantics ride the anchored EventID + in-tx fold as specified.

## 6. Contract and engineering constraints

- **Atomicity**: domain write and compliance fact commit or roll back together for all eight paths. A failed append surfaces as `503 audit.ingest_failed` — fail-closed; the write provably did not commit (repo-level rollback, R2.3.4).
- **In-tx duplicate tolerance** (`ErrAuditDuplicate`): only after the domain writes inside the same tx (contract §4.2), verbatim `callback_audit.go:47-54` — it can never mask a failed domain write, and with the action-scoped anchors it can only fire on a true same-intent retry.
- **Conditional append** (account-provision doctrine): no-op domain writes (repeat like/report, 0-row updates) append nothing — the ledger records state-changing facts only.
- **Anchored IDs**: two classes only (receipt / `idgen.Anchor` arity 5); part contracts frozen per the table in §3.2; `idgen.RenderAnchorTime` for the time part (R7 — byte-identical to the stored `timestamptz`); never `EventID: idgen.NewID()`.
- **Enable-flag semantics**: `s.audit != nil` selects the WithAudit path; unconfigured service keeps byte-identical legacy behavior (both plain repo calls and error codes). `domain.AuditLogger` is the flag type (predecessor R2.1); the module never calls `Log`.
- **File/function limits** (done gate): `service.go` 473 and `repository.go` 411 lines today — new WithAudit methods go to `internal/comment/repository_audit.go` and fact builders/branch helpers to `internal/comment/audit_fact.go`; every function ≤ 50 lines (funlen) and cyclop ≤ 10; extract branch helpers if `Create`/`react`/`setStatusAsAuthor` exceed limits after the `if s.audit != nil` additions.
- **Working-tree note**: implement on top of HEAD `b005409` (clean except the in-flight predecessor doc); the predecessor conversion (R0) is the first change to land — this spec's §3.2/§3.3 merge with its R1.2/R2.1 artifacts (repo audit field, audit_fact.go, service branch shape).
- **Effort/value** (as supplied): value 8, risk_reduction 8, effort 6, confidence 7.

## 7. Done gate

```bash
make test && make lint && make lint-strict && make check-file-sizes \
  && make check-audit-swallow && make check-anchored-event-ids && make build
bash scripts/check-audit-swallow.sh internal/comment      # exit 0, 0 matches (after R0)
bash scripts/check-anchored-event-ids.sh internal/comment # exit 0, 0 violations
make test-integration   # required: repository tx boundary + audit append changed
# integration runs:
go test -tags=integration ./internal/comment/ -count=1        # T-5 cases (a)-(d), T-4 dedup table
go test -tags=integration ./internal/service/ -run TestCommentMutationAuditFactDrains -count=1  # T-10.1
```
