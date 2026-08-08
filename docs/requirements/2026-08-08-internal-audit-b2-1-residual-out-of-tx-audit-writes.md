# Requirements — internal/audit: B2-1 residual out-of-tx audit writes (verification and closure)

| | |
|---|---|
| Module (batch label) | `internal/audit` (spans `internal/service`, `internal/repository`, `scripts/check-audit-swallow.sh`, `scripts/check-audit-manifest.sh`, `scripts/check-anchored-event-ids.sh`, composition root `internal/app/new.go`) |
| Source analysis | `docs/auto/analyses/internal-audit-9b2d0397.json` (direction 2 of 3) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery; gate G3: T-4/T-5) |
| Contract | `docs/proposals/audit-contract-batch-aero-id.md` (§B2-1, §B2-2) |
| Sibling spec (ownership split) | `docs/requirements/2026-08-08-cmd-server-complete-in-tx-audit-writes.md` (REQ-3/REQ-5: the ten post-commit `Log` sites + fallback-tail deletion). Verified present at head; the direction's "proposed, not verified" caveat is resolved — the sibling spec exists, was re-verified 2026-08-08 at head `13e4676`+working tree, and records corrections C-1…C-5 to the same citations |
| Date | 2026-08-08 |
| Status | **Verification and closure.** The conversion this direction calls for is already landed in the tree (verified line-by-line at head `cd65f57` + working tree). Every cited call site, every cited precedent, and every supplied acceptance check maps to landed code or a landed test/gate. This spec preserves the supplied acceptance checks in testable form, corrects two stale citations (T-10.1 `callback.go:286` → `callback_worker.go:47`; T-4 fresh-v7 policy for six of the ten sites), and defines the regression gates that keep the landed state from regressing. No conversion code remains in scope. |

## 1. Executive verdict

The direction's problem statement ("these sites are the last B2-1 conversions outstanding in the tree") is **superseded**: all five cited files and all ten legacy post-commit `Log` sites were converted to the in-tx `XxxWithAudit` pattern before this spec's verification pass. The conversion is complete, fail-closed, tested at unit and integration level, and pinned by three mechanical gates. The residual bare `s.audit.Record`/`s.audit.Log` sites that remain are exactly the eight enumerated in `scripts/check-audit-manifest.sh` — seven documented **standalone-fact** sites (no domain write exists to join) and one **provably unreachable with-audit-fallback** tail (`legacy_cutover.go:103`, concrete repo always takes the atomic path in the composition root). None is a B2-1 conversion candidate by design.

## 2. Evidence verification (direction citations vs. repository at head `cd65f57`)

### 2.1 The five cited post-commit sites — all converted

The direction's exact grep (`if err := s.audit.Log(`) returns **0 matches** in `internal/service` today (verified 2026-08-08). Each cited site now routes through an in-tx `WithAudit` repository method guarded by the fail-closed `if s.audit != nil` + repository type-assert pattern:

| Direction citation | Current state (verified) |
|---|---|
| `block.go:54,79` (`s.audit.Log` after `Block`/`Unblock` commit) | `block.go:55` `repo.BlockWithAudit(...)` / `:80` `repo.UnblockWithAudit(...)` — one transaction: block row + ledger append + pending publication. `repository/block_audit.go` |
| `admin.go:93,122` (`GrantRole`/`RevokeRole`) | `admin.go:91` `repo.GrantRoleWithAudit(...)` / `:123` `repo.RevokeRoleWithAudit(...)`. `repository/admin_audit.go` |
| `ban.go:84,122` (`Ban`/`Unban`) | `ban.go:85` `repo.BanWithAudit(...)` / `:120` `repo.UnbanWithAudit(...)`. `repository/ban_audit.go` |
| `comment.go:415` (`ResolveReport` + separate conditional `SetStatus`) | `comment.go:412` `repo.ResolveReportWithAudit(...)` — the report update, the conditional comment hide, and the anchored fact commit in **one transaction**; code comment at `comment.go:401-404` cites "(B2-1)". `repository/comment_audit.go` |
| `file_share.go:114,148` (`ShareFile` grant loop / `UnshareFile`) | `file_share.go:119` `repo.GrantWithAudit(ctx, id, owner, grantees, facts)` (one call, N facts) / `:151` `repo.RevokeWithAudit(...)`. `repository/file_share_audit.go` |

The tenth site (message delete) is also converted: `message.go:325` `repo.SoftDeleteWithEventAudit(...)` in-tx; only the `deleteAlreadyGone` residual path (`message.go:356,369-380` — the domain row predates the request, so no write is left to join) retains a checked `s.audit.Log`, documented as a standalone-fact at `message.go:362-367` and covered by `TestStandaloneSiteMessageDeleteAlreadyGone` (`audit_standalone_sites_integration_test.go:147`).

### 2.2 Landed precedents cited in the evidence — all verified present

| Evidence citation | Verified |
|---|---|
| `internal/service/callback.go` `registerWithAudit` + `internal/repository/callback_audit.go:31-55` (`CreateSubscriptionWithAudit`, `ErrAuditDuplicate` fold) | ✅ `callback.go:163-174` (`registerWithAudit` → `CreateSubscriptionWithAudit`); `callback_audit.go:24,33`; `ErrAuditDuplicate` fold at `:46-51` (swallowed in-tx, contract §4.2) |
| `internal/service/audit_hold.go` `putWithAudit`/`removeWithAudit` + `internal/repository/audit_hold.go` | ✅ `audit_hold.go:88-96` (`putWithAudit` → `PutHoldWithAudit`), `:119-128` (`removeWithAudit` → `RemoveHoldWithAudit`); repo guarded by `requireAuditLedger` (`repository/audit_hold.go:81-83`) |
| `internal/repository/audit_wiring.go:14-28` `requireAuditLedger` fail-closed | ✅ `audit_wiring.go:14-31`; panics on nil and typed-nil (reflect kind check). Used by every WithAudit repo constructor: `account.go:49`, `admin.go:37`, `ban.go:43`, `block.go:46`, `callback.go:59`, `comment.go:74` |
| Rollback tests `TestAuditHoldPutWithAuditRollbackWithoutSigningKey` / `TestAuditHoldPutWithAuditPublicationInsertRollsBack` / `TestAuditWiringRollbackAtomicity` | ✅ `internal/repository/audit_hold_integration_test.go:133` and `:238`; `internal/repository/audit_wiring_integration_test.go:175`; plus `TestAuditWiringPublicationInsertRollsBackLedgerRow` at `:294` and `TestAuditWiringHappyPath` at `:60`. Trigger mechanism `installFailPublicationInsertTrigger` at `audit_wiring_integration_test.go:264-285` (`BEFORE INSERT ON audit_publications` raises after the ledger row is staged — the last statement of every WithAudit tx) |
| `TestAnchoredEventIDAppendsOnce` | ✅ `internal/repository/anchor_dedup_integration_test.go:35-49` (second append folds into `ErrAuditDuplicate`; exactly 1 ledger row) |

### 2.3 Supplied acceptance checks — all landed, with two citation corrections

| Supplied acceptance | Status at head `cd65f57` |
|---|---|
| T-5 per-site failure injection (failing audit repo spy) for each converted site — operation returns error or `ObserveAuditIngestFailure` increments, never both absent; `grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service` = 0 | ✅ **Landed.** Nine unit tests assert the 500 AppError on a failing `*WithAuditFn` spy: `TestBlockAuditFailureFailsClosed` (`block_test.go:133`), `TestUnblockAuditFailureFailsClosed` (`:145`), `TestBanAuditFailureFailsClosed` (`ban_test.go:68`), `TestUnbanAuditFailureFailsClosed` (`:81`), `TestAdminGrantRoleAuditFailureFailsClosed` (`admin_test.go:58`), `TestAdminRevokeRoleAuditFailureFailsClosed` (`:75`), `TestResolveReportAuditFailureFailsClosed` (`comment_test.go:593`), `TestShareFileAuditFailureFailsClosed` (`file_share_test.go:17`), `TestUnshareFileAuditFailureFailsClosed` (`:40`). The metric half is observed on ledger `Record` failures (`audit_ledger.go:82`) and asserted in `audit_ledger_test.go:206-217`. The swallow grep returns **0 matches** (verified). The `_ =`/bare-call ban is enforced by `make check-audit-swallow` (P1+P2 include `Log`) |
| T-5 forced-rollback table per migrated mutation: injected append failure after domain insert → 0 `audit_ledger` rows, 0 `audit_publications` rows, 0 domain rows; success → 1 ledger + 1 pending publication + domain row | ✅ **Landed.** `TestAuditWiringMatrixAdminBanBlockCommentMessageFileShare` (`audit_wiring_matrix_integration_test.go:70`) table-drives all ten mutation classes across admin/ban/block/comment/message/file-share × {commit → 1/1 + exact action, crash (blocked publication insert) → error + 0/0}; `TestAuditWiringMatrixFileShareGrantMulti` (`:162`) proves the N-grantee grant commits N domain + N ledger + N publication rows atomically and rolls all back on crash |
| T-4: converted facts keep anchored event_ids (re-execution folds into `ErrAuditDuplicate` → exactly 1 ledger row, `TestAnchoredEventIDAppendsOnce` extended) | ✅ **Landed, with a policy correction.** Comment resolve keeps `EventID = reportID` and message delete keeps `EventID = message ID` (both load-bearing retry-dedup anchors, `audit_fact.go:27-47`); the other six sites (admin×2, ban×2, block×2, file-share×2) **mint a fresh v7 per call by contract** (B2-1 REQ-3.3: a stable reused ID across distinct intents would let `ErrAuditDuplicate` roll back a legitimate domain write). The fold → exactly-1-row property is proven by `TestAnchoredEventIDAppendsOnce` and by `audit_consolidation_integration_test.go:131-152` (concurrent appends, each worker gets nil or `ErrAuditDuplicate`, exactly one ledger + one publication row). The matrix test asserts exactly 1 ledger row per `event_id` on the commit side. `make check-anchored-event-ids` enforces the allowlist (R-1), with `audit_fact.go`'s fresh mint listed as the sole C2 mint |
| T-10.1: mechanical greps stay green — `.Publish(ctx, fact)` only at `callback_audit.go:179`; `drainPublications` only at definition + `callback.go:286` Start ticker | ✅ **Landed with a location correction.** `.Publish(ctx, fact)` occurs only at `callback_audit.go:179` (verified). `drainPublications` is defined at `callback_audit.go:45`; its **only non-test call site is `callback_worker.go:47`** inside `callbackService.Start`'s ticker — the worker loop moved out of `callback.go` (the cited `callback.go:286` does not exist today; the sibling spec recorded the same drift as correction C-1). Pinned statically by `check-audit-swallow.sh` ("T-5 mechanical wiring assertions": exactly one non-test call site, must be `callback_worker.go`; `NewAuditLedgerService` single-arg) and dynamically by `TestDrainPublicationsOnlyFromStart` (`drain_entry_test.go`) |

### 2.4 The four fallback tails — re-verified this run

The direction's note said the 4-tail claim was "verified in a prior sibling analysis, not re-verified line-by-line in this run." Re-verified here:

| Tail | Current state |
|---|---|
| `operation.go` | ✅ Gone — `operation.go:128` calls `createWithAudit` only; no `atomicAudit` tail. `operation_audit.go:19-30` (`CreateWithAudit`) |
| `sync_job.go` | ✅ Gone — `sync_job.go:449-456` comment: "in-tx via FinishWithAudit; a repo without the WithAudit surface fails closed (B2-1 REQ-4) — no standalone fallback tail." `sync_audit.go:20-31` (`CreateWithAudit`) |
| `account_profile.go` | ✅ Gone — `account_profile.go:47-52` fails closed (B2-1 REQ-4): no WithAudit surface ⇒ `admin.audit_unavailable`-style error, no standalone `Record` |
| `legacy_cutover.go` | ✅ Tail exists (`legacy_cutover.go:103`) but is provably unreachable in the composition root: `new.go:220` wires `NewLegacyCutoverRepository(gdb, auditLedgerRepo)` which satisfies `LegacyCutoverAuditRepository`, so `setWithAudit` (`:115-125`) always returns `atomicAudit=true`; the `s.audit == nil` guard additionally blocks the tail. Pinned by `check-audit-manifest.sh` row `legacy.cutover.set|with-audit-fallback|SetCutoverWithAudit` (the gate fails if the sibling symbol is absent) |

## 3. Problem statement (current state)

No conversion work remains. The compliance-risk window the direction describes — a crash between the domain commit and the audit append silently losing the fact — is closed for every mutation path in the live layer: all ten legacy post-commit `Log` sites now append the ledger row and the pending publication row in the same transaction as the domain write, and the composition root wires `FullAuditLedgerRepository` into all six repo families (`internal/app/new.go:138-160`: file, fileShare, message, comment, block, ban, admin, account, event store, operation, sync-job; `:220` cutover; `:186-204` service options). `requireAuditLedger` panics at wiring time on nil/typed-nil, so the audit path cannot be silently disabled.

The only remaining bare audit-write sites are the eight enumerated in `check-audit-manifest.sh` (classes `standalone-fact` ×7, `with-audit-fallback` ×1): `account.go` (`account.viewed`, `account.listed` — sensitive-read access audit), `operation_state.go` (`operation.reconciled`), `replay.go` (`audit.replay.dry_run`, `audit.replay.requested`), `event_ingest.go` (quarantine/outcome), `message.go` (`auditMessageDelete` — deleteAlreadyGone residual), `legacy_cutover.go` (unreachable fallback). Each carries its `audit-site:` class marker and each standalone-fact class has an integration proof in `audit_standalone_sites_integration_test.go` (`:80, :103, :147, :174`). These are deliberately not B2-1 candidates: in-tx atomicity is only meaningful when a domain write exists to join.

## 4. Requirements (closure and regression protection)

The conversion is complete; the requirements below preserve the supplied acceptance checks as *state invariants* with testable verification commands, and protect them from regression. All are satisfied at head `cd65f57`; the done gate re-proves them.

### REQ-1 — Zero silent audit failure at the converted sites (supplied T-5, first clause)

For every mutation converted by B2-1 (admin grant/revoke role, ban/unban, block/unblock, comment-report resolve, file-share grant/revoke, message delete), an injected audit-append failure must surface as a request error (site's `*.audit_unavailable`/`*_failed` AppError, HTTP 500) or a `ObserveAuditIngestFailure` metric increment — never both absent.

Verification:
- `grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service` → 0 matches.
- `go test ./internal/service/ -run 'FailsClosed' -count=1` → 9 tests pass (names in §2.3).
- `make check-audit-swallow` → green (P1+P2 patterns include `Record|Log|Publish|FinishPublication|ClaimPublication|RequeuePublication|RequeueDead`; scans `internal/service internal/repository internal/app internal/storage cmd`).

### REQ-2 — Forced-rollback atomicity per migrated mutation (supplied T-5, second clause)

Every migrated mutation keeps the property: injected append failure after the domain insert (publication insert blocked via `installFailPublicationInsertTrigger`) → error, 0 `audit_ledger` rows, 0 `audit_publications` rows, 0 domain rows; success side → exactly 1 ledger row + 1 pending publication row + the domain row.

Verification (`make test-integration`, Postgres + MinIO via testcontainers):
- `TestAuditWiringMatrixAdminBanBlockCommentMessageFileShare` — 10 mutation classes × {commit, crash}.
- `TestAuditWiringMatrixFileShareGrantMulti` — N-grantee atomicity.
- `TestAuditWiringRollbackAtomicity`, `TestAuditWiringPublicationInsertRollsBackLedgerRow`, `TestAuditHoldPutWithAuditRollbackWithoutSigningKey`, `TestAuditHoldPutWithAuditPublicationInsertRollsBack`.

### REQ-3 — Anchored event IDs preserved; duplicate fold yields exactly one row (supplied T-4)

Comment resolve (`EventID = reportID`) and message delete (`EventID = message ID`) keep their byte-identical anchors; the six fresh-mint sites (admin×2, ban×2, block×2, file-share×2) keep fresh-v7-per-call (B2-1 REQ-3.3 contract). Re-execution with the same `event_id` folds into `ErrAuditDuplicate` with exactly 1 ledger row and 1 publication row.

Verification:
- `go test -tags integration ./internal/repository/ -run 'TestAnchoredEventIDAppendsOnce|TestAuditConsolidation' -count=1` (names as in §2.2/§2.3) — plus the matrix test's commit side (exactly 1 ledger row per `event_id`, exact `action`).
- `make check-anchored-event-ids` → green; `audit_fact.go`'s single fresh mint remains on the C2 allowlist with the documented rationale; no new `EventID: idgen.NewID()` enters non-test Go outside the allowlist.

### REQ-4 — Single audit delivery path stays mechanical (supplied T-10.1)

`.Publish(ctx, fact)` occurs only at `callback_audit.go:179`; `drainPublications` has exactly one non-test call site, `callback_worker.go:47` inside `Start`'s ticker (corrected from the stale `callback.go:286`); `NewAuditLedgerService` is single-argument (ledger has no publisher surface).

Verification:
- `grep -rn '\.Publish(ctx, fact)' internal --include='*.go' | grep -v _test` → only `internal/service/callback_audit.go:179`.
- `grep -rn 'drainPublications(' internal/service --include='*.go' | grep -v _test` → definition + `callback_worker.go:47` only.
- `make check-audit-swallow` T-5 mechanical assertions (drain single site in `callback_worker.go`; no two-arg `NewAuditLedgerService`; no swallowed `Publish`).
- `go test ./internal/service/ -run TestDrainPublicationsOnlyFromStart -count=1` → the Start ticker is the only production claim driver.

### REQ-5 — The residual bare-site manifest is closed and pinned

The only bare `s.audit.Record`/`s.audit.Log` sites in `internal/service` are the eight enumerated rows of `scripts/check-audit-manifest.sh`, each carrying its `audit-site:` class marker; any new unmapped bare site fails the gate; the one `with-audit-fallback` row requires its `SetCutoverWithAudit` sibling in the same file.

Verification:
- `make check-audit-manifest` → green.
- `make check-integration-compile` → green (the standalone-site proofs compile under the integration tag).

### REQ-6 — Fail-closed wiring is permanent

Every `WithAudit` repository constructor keeps its `requireAuditLedger` guard; nil or typed-nil ledger input panics at wiring time; the composition root passes a single `FullAuditLedgerRepository` (`repository.NewAuditLedgerRepository`) to all mutation repos.

Verification:
- `go test ./internal/repository/ -run 'TestConstructorsRejectNilAudit|TestConstructorsRejectTypedNilAudit|TestConstructorsRejectNilKindAudit' -count=1` (`audit_wiring_test.go:29,50,72`).
- `make test && make lint && make lint-strict && make check-file-sizes && make build` (per AGENTS.md done gate).

## 5. Acceptance criteria (testable)

| ID | Criterion | Command / probe | Expected |
|---|---|---|---|
| A-1 | No silent audit writes; per-site fail-closed unit tests pass | `grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service`; `go test ./internal/service/ -run 'FailsClosed' -count=1` | 0 matches; 9/9 pass |
| A-2 | Forced-rollback table green for all ten mutation classes | `make test-integration` (runs `TestAuditWiringMatrix*`, `TestAuditWiringRollbackAtomicity`, `TestAuditWiringPublicationInsertRollsBackLedgerRow`, `TestAuditHoldPutWithAudit*`) | crash side 0/0/0; commit side 1/1 + domain row |
| A-3 | Anchored IDs and dedup fold proven | `go test -tags integration ./internal/repository/ -run 'TestAnchoredEventIDAppendsOnce|TestAuditConsolidation' -count=1`; `make check-anchored-event-ids` | exactly 1 ledger row after re-execution; gate green |
| A-4 | Single delivery path pinned | greps of §REQ-4; `go test ./internal/service/ -run TestDrainPublicationsOnlyFromStart -count=1`; `make check-audit-swallow` | exactly 1 production `Publish` site; exactly 1 production `drainPublications` site (`callback_worker.go:47`); gate green |
| A-5 | Manifest closed | `make check-audit-manifest` | green; 8 enumerated rows |
| A-6 | Wiring fails closed | `go test ./internal/repository/ -run TestConstructorsReject -count=1` | 3/3 pass (nil, typed-nil, nil-kind) |
| A-7 | Full done gate | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-integration-compile && make build` | all green |

## 6. Out of scope (unchanged from the direction; no expansion)

- The eight residual bare sites (§3) — documented standalone-fact / provably-unreachable fallback classes, manifest-pinned; converting them to `WithAudit` is not meaningful (no domain write to join) and would contradict B2-1 REQ-3.3 (a stable reused ID would roll back legitimate writes).
- `legacy_cutover.go:103` tail deletion — the direction and the locked sibling design explicitly leave the fifth tail out of scope; it is unreachable and sibling-pinned.
- B2-5 (dead-letter replay) and B2-6 (activation gate) — sibling directions in the same analysis file; their surfaces (`RequeuePublication`, `PurgeDeadLetterPublications`, activation policy) are already wired and gated separately.
- The dead-tree `internal/{file,message,comment,block,notification,search,audit}` parallel tree — never scanned by the gates and not a runtime surface (AGENTS.md).

## 7. Traceability

| Supplied acceptance | Requirement | Landed artifact |
|---|---|---|
| T-5 per-site failure injection, 0 swallow sites | REQ-1 | `block_test.go:133,145`; `ban_test.go:68,81`; `admin_test.go:58,75`; `comment_test.go:593`; `file_share_test.go:17,40`; `check-audit-swallow.sh` P1+P2; `audit_ledger.go:82` + `audit_ledger_test.go:206-217` |
| T-5 forced-rollback table | REQ-2 | `audit_wiring_matrix_integration_test.go:70,162`; `audit_wiring_integration_test.go:175,294`; `audit_hold_integration_test.go:133,238` |
| T-4 anchored event_ids, fold → 1 row | REQ-3 | `audit_fact.go:27-47`; `anchor_dedup_integration_test.go:35`; `audit_consolidation_integration_test.go:42-152`; `check-anchored-event-ids.sh` R-1 allowlist |
| T-10.1 mechanical greps | REQ-4 | `callback_audit.go:45,179`; `callback_worker.go:47`; `drain_entry_test.go`; `check-audit-swallow.sh` T-5 assertions |
| — (closure) | REQ-5 | `check-audit-manifest.sh` (8 rows); `audit_standalone_sites_integration_test.go:80,103,147,174` |
| — (wiring) | REQ-6 | `audit_wiring.go:14-31`; `audit_wiring_test.go:29,50,72`; `internal/app/new.go:138-160,220` |
