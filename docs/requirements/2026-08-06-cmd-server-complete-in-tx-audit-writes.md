# Requirements — Complete the in-transaction audit migration: all audit writes in AppendInTx/WithAudit, dead inline publisher loop deleted, no-swallow gate meaningful

| | |
|---|---|
| Module (batch label) | `internal/app` (composition root `buildAuditStack`/`wireAuditServices`, `internal/app` wiring tests; the migration spans `internal/service` + `internal/repository` + Makefile/scripts gate) |
| Source analysis | `docs/auto/analyses/internal-app-73e67e01.json` (direction 1 of 3) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery path; gate G3: T-4/T-5) |
| Contract | `docs/proposals/audit-contract-batch-aero-id.md` (§B2-1, §B2-2) |
| Date | 2026-08-06 |
| Status | Draft — every cited file/symbol re-verified against the repository at head `13e4676` **including the uncommitted working tree** (39 modified files; the dead-loop removal, `buildAuditStack` rewiring, gate script, and callback/provisioning/operation/sync conversions have already landed and are part of the contract to keep, not re-do) |

## 1. Evidence verification status

All ten evidence claims (E1–E10) were re-checked against the live tree (working-tree state, not the stale
pre-implementation snapshot the analysis was written against). Line numbers are current. Corrections C1–C6
amend the source analysis.

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `auditLedgerService.Record` runs a dead inline publisher loop (`_ = publisher.Publish`, `service/audit_ledger.go:88-101`) | ⚠️ **C1 — already removed in the working tree.** `auditLedgerService` (`internal/service/audit_ledger.go:48`) has no `publishers` field; `NewAuditLedgerService(repo)` is single-arg (`:56`); `Record` (`:68-82`) calls `repo.Append` only and never publishes. Zero `_ = publisher.Publish` remains anywhere in the tree. The requirement is now to **keep it removed and make the removal provable** (REQ-1, REQ-7) |
| E2 | `internal/app/new.go:146` constructs `NewAuditLedgerService(auditLedgerRepo)` with no publishers | ✅ holds, relocated: `buildAuditStack` (`internal/app/audit.go:23-57`) calls `service.NewAuditLedgerService(auditLedgerRepo)` at `:51` with no publisher argument; `wireAuditServices` (`audit.go:59-75`) hands the governance publishers and the publication repo to `callbackSvc` only (`SetAuditPublicationRepository`, `SetAuditPublishers`). All 9 test call sites are single-arg |
| E3 | `drainPublications` (`service/callback_audit.go:47`) is the sole delivery path, driven by `callbackService.Start` (`service/callback.go:267`) | ✅ exact: `Start` at `callback.go:269`; the ticker body calls `s.drainPublications(workerCtx)` at `:282` — the **only** call site in the tree; `drainPublications` defined at `callback_audit.go:47`. `callbackService.Publish` (`callback_audit.go:19-27`) no-ops when the publication repo is wired (single-delivery contract) |
| E4 | Existing `AppendInTx` pattern at `repository/operation_audit.go:125`, `sync_audit.go:27`, `account_profile.go:101`, `event_receipt.go:180` | ✅ with drift (**C2**): `AppendInTx` interface `repository/audit_ledger.go:39`, impl `:106`, `createAuditRows` (ledger row + pending publication row) `:172`; `operation_audit.go` appendAudit→`AppendInTx` at `:136` (not `:125`); the sync-job repo file is `repository/sync_job_audit.go` (appendFinishedAudit→`AppendInTx` at `:55`; there is no `repository/sync_audit.go`); `account_profile.go:101` exact; `event_receipt.go:180` exact. UNIQUE `(fact_id)` on `audit_publications` in `migrations/000045_audit_publications.up.sql:12` |
| E5 | `XxxWithAudit` variants exist for operation/sync/profile/event-ingest | ✅ extended beyond the analysis: `account_profile.go:67` (`UpdateProfileWithAudit`), `operation.go:36-43,135` (`CreateWithAudit`/`FinishStepWithAudit`/`FinishCompensationStepWithAudit`/`SetStatusWithAudit` at `:452`), `sync_job_audit.go` (create/finish), `callback_audit.go:22-24` (`CreateSubscriptionWithAudit`/`DeleteSubscriptionWithAudit`/`ResetDeliveryWithAudit`), `account_provision_audit.go:22-24` (`EnsureByIdentityWithAudit`/`EnsureByIdentityWithQuotaWithAudit`). All append via `AppendInTx` inside the business tx; `requireAuditLedger` fail-closed precedent (`audit_wiring.go:14-28`) |
| E6 | ~15 services still record post-commit with `_ = s.audit.Record/Log` | ⚠️ **C3 — 10 sites remain, all `_ = s.audit.Log`** (post-commit, swallowed): `admin.go:93,120` (GrantRole/RevokeRole), `ban.go:83,119` (Ban/Unban), `block.go:54,77` (Block/Unblock), `comment.go:415` (ResolveReport), `file_share.go:114,146` (ShareFile/UnshareFile), `message.go:313` (DeleteMessage). **C4 — 2 sites are checked but still post-commit**: `legacy_cutover.go:97` (SetCutover) and `audit_hold.go:69,96` (PutHold/RemoveHold) — anchored EventIDs, error checked, but the append still commits in a separate transaction after the domain commit. **C5 — 4 provably-unreachable out-of-tx fallback tails remain**: `operation.go:135-142`, `sync_job.go:162-169`, `sync_job.go:466-471` (finish fallback), `account_profile.go:61-68` — all behind `!atomicAudit` after the audit-repo assertion always succeeds for the concrete repos. The direction's other cited sites are converted: `callback.go` (in-tx WithAudit), `account_provision.go` (in-tx WithAudit, `:168` site replaced), `account.go:155,186` (read-path `account.viewed`/`account.listed` — checked, no business transaction exists, correctly out-of-tx) |
| E7 | No-swallow gate | ⚠️ **C6 — the gate exists but has a hole.** `Makefile:52-54` + `scripts/check-audit-swallow.sh` (in AGENTS.md done-gate at `:52`), with self-test fixtures under `scripts/testdata/audit-swallow/`. But P1/P2 deliberately exclude `Log(` — `scripts/testdata/audit-swallow/negative/fixture.go:15` documents `_ = s.audit.Log(...)` as "excluded". The gate is **green today while 10 swallowed durable-ledger writes remain** (`s.audit` is the durable `AuditLedgerService` in every wired service; `auditLedgerService.Log` → `Record` → `Append`). The supplied acceptance requires the gate to fail on any `_ = s.audit.` hit; the `Log` exclusion must be removed |
| E8 | Repo-level crash-injection test (T-4) | Partially exists: `internal/repository/audit_wiring_integration_test.go` — `TestAuditWiringHappyPath` (`:60`, profile/op-create/step/event commit 1 ledger + 1 publication row with the domain row), `TestAuditWiringRollbackAtomicity` (`:175`, duplicate append rolls the whole tx back), `TestAuditWiringPublicationInsertRollsBackLedgerRow` (`:268`, trigger-injected publication-insert failure → 0 ledger + 0 publication + 0 domain rows survive). **Coverage is profile/operation/event only** — not "every migrated mutation" |
| E9 | Wiring test (drainPublications only from `Start`; `NewAuditLedgerService` no publishers) | Partially exists: `internal/app/audit_integration_test.go:49` `TestAuditLedgerRecordEnqueuesOnlyIntegration` — one `Record` → exactly 1 `audit_ledger` row + 1 `pending` `audit_publications` row, no delivery. **Missing**: a test proving `drainPublications` is invoked only from `callbackService.Start` |
| E10 | Post-commit crash window rationale | ✅ stands: with `_ = s.audit.Log` after the domain commit, a crash between commit and append loses compliance facts silently; with checked post-commit `Record`, the same window exists but the error is surfaced. In-tx append closes both |

## 2. Problem statement (current state)

Audit durability is split. Operation/sync/profile/event-ingest/callback/provisioning mutations already append
facts via `AppendInTx` inside the business transaction. Ten live service mutations still append **after**
commit with **swallowed** `_ = s.audit.Log` calls (admin×2, ban×2, block×2, comment×1, message×1,
file_share×2), two append after commit with **checked** calls (audit hold×2, legacy cutover×1), and four
provably-unreachable out-of-tx fallback tails keep a post-commit `s.audit.Record` shape alive. A crash
between the business commit and the audit append silently loses compliance facts. Separately, the dead
inline publisher loop in `auditLedgerService.Record` has already been removed (C1) and must stay removed,
with the single-delivery path (`drainPublications` via `callbackService.Start`) and the no-swallow gate made
mechanically provable — the gate currently excludes `Log(` and is green despite ten swallows.

## 3. Requirements

### REQ-1 — Single delivery path stays: `Record` appends only, `drainPublications` delivers only (keep-enforced)

1. `NewAuditLedgerService` must remain single-argument `(repo repository.AuditLedgerRepository)`; the
   `publishers` field and inline publish loop must not return. `auditLedgerService.Record` must contain no
   publish call of any form. Enforced by: the constructor signature (compile), the gate (REQ-2), and the
   enqueue-only integration test (REQ-7.1).
2. Composition root `internal/app` must keep wiring publishers exclusively into the callback service:
   `buildAuditStack` → `NewAuditLedgerService(auditLedgerRepo)` (no publishers) +
   `wireAuditServices(callbackSvc, auditLedgerRepo, auditPublishers...)` (publishers + publication repo via
   `SetAuditPublicationRepository`/`SetAuditPublishers`). `drainPublications` (`callback_audit.go:47`) stays
   the only delivery path, driven by the `Start` ticker (`callback.go:282`).

### REQ-2 — `make check-audit-swallow` fails on any `_ = s.audit.` / `_ = publisher.Publish` hit

1. Extend `scripts/check-audit-swallow.sh` so the P1 pattern matches **any** `_ = <receiver>.<method>(` on
   the audit receivers — mechanically: `_ = s.audit.` (all methods, **including `Log`**) and
   `_ = publisher.Publish(` — and fails the run on any hit under `internal/service` (live layer).
2. Remove the documented `Log` exclusion (negative fixture line `_ = s.audit.Log(ctx, event)` becomes a
   **positive** fixture). Rationale: in every wired service `s.audit` is the durable `AuditLedgerService`;
   `auditLedgerService.Log` (`audit_ledger.go:135-141`) adapts to `Record` → `Append`, so swallowing it
   swallows a durable-ledger write error. The AGENTS.md carve-out applies to the legacy best-effort
   `AuditLogger`, not to the ledger's `Log` adapter.
3. Keep the P2 bare-call pattern (add `Log` to its method list or keep it receiver-based on `s.audit.`).
   Keep the documented exclusions that are still correct: best-effort SSE `d.pusher.Publish`
   (`notification.go`, not an audit write), comment lines, `if err :=`/`return`/`err :=`/`defer`/`go` forms,
   and the legacy unwired tree (`internal/{file,message,comment,block,notification,search,audit}` — the gate
   scans `internal/service` only).
4. Self-tests must flag the positive fixtures and accept the negative fixtures before scanning; the target
   is **0 hits after REQ-3/REQ-5** (today: 10 `_ = s.audit.Log` hits are invisible to the gate; `_ =
   publisher.Publish` has 0). Update the stale "Baseline on today's tree" comment (it still names
   `audit_ledger.go:79`). The target stays wired into the done-gate (`AGENTS.md:52`).

### REQ-3 — Convert the ten swallowed post-commit `_ = s.audit.Log` sites to in-transaction appends

For `adminService.GrantRole`/`RevokeRole` (`admin.go:93,120`), `banService.Ban`/`Unban` (`ban.go:83,119`),
`blockService.Block`/`Unblock` (`block.go:54,77`), `commentService.ResolveReport` (`comment.go:415`),
`messageService.DeleteMessage` (`message.go:313`), `fileService.ShareFile`/`UnshareFile`
(`file_share.go:114,146`):

1. Add repository `XxxWithAudit` variants on the owning repos (admin, ban, block, comment, message, file
   share) that perform the domain write and `r.audit.AppendInTx` in **one** gorm transaction, following the
   landed precedent (`repository/callback_audit.go:31-55`, `operation_audit.go:119-136`,
   `requireAuditLedger` fail-closed check). Services call the `WithAudit` variant when `s.audit != nil` and
   fail closed on a missing audit repository (no silent plain-write fallback), matching the landed
   `finishStep`/`setOperationStatus` shape.
2. Fact content must equal what the `Log` adapter produces today (`audit_ledger.go:135-141` mapping:
   `ActorUID`, `Action`, `SubjectType`→`ResourceType`, `SubjectID`→`ResourceID`, `Meta`→`Payload`), built
   with `buildAuditFact`; additionally preserve `TargetUID` in `Payload["target_uid"]` (the current adapter
   drops it — the conversion is the moment to stop losing it). Keep `OccurredAt`/context binding unchanged.
3. `EventID` policy: **fresh `idgen.NewID()` per call** for these ten mutations (precedent: the C2
   replay-reset fix in `callback.go:257-263`). These facts have no natural idempotent anchor, and a stable
   EventID reused across distinct intents would make `ErrAuditDuplicate` roll back a legitimate domain write
   (the D3 hazard resolved for callback delete/replay). With fresh IDs the ledger dedup
   `(tenant_id, source, event_id)` cannot fire; `ErrAuditDuplicate` is still swallowed **in-tx after the
   domain write** (contract §4.2 Class B, defensive parity) so it can never mask a failed domain write.
4. Per-mutation shape: message delete keeps the outbox event and the audit fact in the same transaction as
   the soft delete (one tx: soft delete + event + fact). Comment resolve appends the fact in the same
   transaction as the conditional status update; when no status write is needed the fact alone commits in
   its own transaction — never a separate post-commit call. File share keeps one fact per grantee (current
   behavior: `file_share.go:114` loops) with all facts appended in the same transaction as the grant/revoke
   rows.

### REQ-4 — Convert the two checked post-commit `Record` sites to in-transaction appends

1. `legacyCutoverService.SetCutover` (`legacy_cutover.go:97`) → `SetCutoverWithAudit` on the cutover repo;
   `auditHoldService.Set`/`Remove` (`audit_hold.go:69,96`) → `PutHoldWithAudit`/`RemoveHoldWithAudit` on the
   hold repo. Domain write + `AppendInTx` in one transaction; fail closed on a missing audit repo.
2. **Preserve the existing anchored EventIDs** (`idgen.Anchor(...)` in `legacy_cutover.go:97-105` and
   `audit_hold.go:69-97`): retries re-derive the same `event_id`, the in-tx duplicate swallow (Class B)
   commits the domain write, and distinct logical facts keep distinct anchors — exactly the semantics the
   current out-of-tx `Record`+`ErrAuditDuplicate→nil` path has today, with the crash window closed.

### REQ-5 — Delete the provably-unreachable out-of-tx fallback tails

1. Delete the `if s.audit != nil && !atomicAudit { s.audit.Record(...) }` tails at `operation.go:135-142`,
   `sync_job.go:162-169`, and `account_profile.go:61-68`, plus the sync-job finish fallback tail
   `sync_job.go:466-471` (plain `repo.Finish` + standalone `Record` when the repo lacks
   `SyncJobAuditRepository`).
2. Drop the now-unused `atomicAudit` bool plumbing in `createWithAudit` (operation and sync-job variants)
   and `updateAccountProfile`. Keep the fail-closed heads (`repo, ok := s.repo.(...AuditRepository); if !ok
   { return apperr.Internal(...) }`), matching the landed `finishStep`/`setOperationStatus`/
   `finishCompensationStep` shape. Zero behavior change: with `s.audit == nil` (unit tests) the plain repo
   call happens exactly as today; with `s.audit != nil` (production) the concrete repos always satisfy the
   assertion.
3. Residual out-of-tx `s.audit.Record` calls are then only the justified read-path/standalone set (all
   error-checked, none swallowed): `account.go:155,186` (sensitive reads `account.viewed`/`account.listed`
   — no business transaction exists), `operation_state.go:39` (`operation.reconciled` — standalone fact),
   `replay.go:145,240` (`audit.replay.dry_run`/`audit.replay.requested` — the replay audit trail itself),
   `event_ingest.go:154` (quarantine record). Keep them checked.

### REQ-6 — T-4: repo-level crash-injection proves atomicity for **every** migrated mutation

Extend the repo-level integration suite (`internal/repository/audit_wiring_integration_test.go` and/or the
per-repo siblings `callback_audit_integration_test.go`, `audit_hold_integration_test.go`,
`audit_consolidation_integration_test.go`) so each migrated mutation has both sides:

1. **Commit side** (mechanism of `TestAuditWiringHappyPath`): after a successful `XxxWithAudit` call, a fresh
   connection sees exactly 1 `audit_ledger` row (correct `event_id`/`action`) + 1 `pending`
   `audit_publications` row per fact, alongside the domain row.
2. **Crash side** (mechanism of `TestAuditWiringPublicationInsertRollsBackLedgerRow`): a failure-injection
   trigger on `audit_publications` `BEFORE INSERT` fires after the business row insert → the call returns an
   error, and **no `audit_ledger` row and no `audit_publications` row survive** — and the domain row is
   rolled back with them. Where the duplicate path is meaningful (Class A sites), keep the
   `TestAuditWiringRollbackAtomicity` duplicate-injection assertions as well.
3. **Mutation matrix** (all migrated mutations): profile update, operation create, operation step finish,
   event apply, callback register / delete / replay-reset, audit hold put / remove, legacy cutover set,
   account provisioning (both variants), sync job create / finish, admin grant role / revoke role, ban /
   unban, block / unblock, comment report resolve, message delete, file share grant (N grantees → N facts)
   / unshare.

### REQ-7 — Wiring acceptance for module `internal/app`

1. Keep `TestAuditLedgerRecordEnqueuesOnlyIntegration` (`internal/app/audit_integration_test.go:49`)
   green: one `Record` → exactly 1 `audit_ledger` row + 1 `pending` `audit_publications` row, no governance
   delivery — this behaviorally proves `NewAuditLedgerService` receives no publishers and
   `auditLedgerService.Record` contains no publish call.
2. Add a test proving `drainPublications` is invoked only from `callbackService.Start`: a fake
   `repository.AuditPublicationRepository` recording `ClaimPublication` calls; construct the callback
   service with the fake, run `Start` with a short interval, assert claims happen from the ticker and that
   no other service entry point triggers `ClaimPublication`.
3. Add the mechanical call-site assertion to `scripts/check-audit-swallow.sh` (or a sibling wiring gate):
   `drainPublications(` appears exactly once outside its definition, inside `callbackService.Start`
   (`callback.go:282`); and `NewAuditLedgerService(` is never called with a second argument in non-test Go
   (today: `audit.go:51` only).

## 4. Acceptance mapping (testable)

| Supplied acceptance | Requirement | Test artifact / pass criterion |
|---|---|---|
| T-4: repo-level crash-injection test (rollback after business row insert) proves no `audit_ledger` row and no `audit_publications` row survive **for every migrated mutation** | REQ-6 | `go test -tags=integration ./internal/repository/` — per-mutation table test: (a) trigger-injected append failure after domain insert → error, 0 ledger rows, 0 publication rows, 0 domain rows; (b) success → 1 ledger row + 1 pending publication row + domain row. Mutation matrix of REQ-6.3 fully enumerated; `TestAuditWiringPublicationInsertRollsBackLedgerRow` generalized or table-driven |
| Gate: `make check-audit-swallow` greps for `_ = s.audit.` / `_ = publisher.Publish` and fails on any hit, matching the no-swallow contract | REQ-2 (+REQ-3/REQ-5 conversion) | `make check-audit-swallow` — positive fixtures include `_ = s.audit.Log(` and `_ = publisher.Publish(` (self-test fails without them); live scan of `internal/service` reports 0 hits after the conversions; a reintroduced `_ = s.audit.Record/Log/...` or `_ = publisher.Publish` in any live service file turns the gate red; gate runs in the done-gate (`AGENTS.md:52`) |
| Wiring test: `drainPublications` is invoked only from `callbackService.Start`; `NewAuditLedgerService` receives no publishers (loop removal verified by absence of publish calls in `auditLedgerService.Record`) | REQ-1, REQ-7 | `go test -tags=integration ./internal/app/` — enqueue-only test stays green (1+1 rows, no delivery); new unit test in `internal/service` with a fake publication repo proves `Start` drives `ClaimPublication` and no other path does; gate asserts the single `drainPublications` call site and single-argument `NewAuditLedgerService`; `grep -rn "_ = publisher.Publish" internal/` = 0 |

## 5. Scope boundaries (explicit non-goals)

- **Legacy unwired tree** (`internal/{file,message,comment,block,notification,search}` and `internal/audit`):
  not migrated, not scanned by the gate (documented exclusion; AGENTS.md "unwired parallel tree").
- **Other directions in the same batch**: TokenSource 401 invalidation with epoch single-flight (B2-3),
  dead-letter replay/requeue (B2-5), audit-binding activation gate (B2-6) — out of scope, except that the
  deterministic-anchor EventIDs already landed for hold/cutover (B2-4) are preserved as compatibility
  constraints (REQ-4.2).
- **Best-effort SSE hub** `notification.go` bare `d.pusher.Publish` calls: not audit writes; remain excluded
  from P2 (documented).
- **`_ = s.receipts.QuarantineEvent`** (`event_ingest.go:120`): a swallowed non-audit repo call, not part of
  this direction; the quarantine **audit** record is already checked (residual list, REQ-5.3).
- **No new migrations**: `audit_ledger`/`audit_publications` + UNIQUE `(fact_id)` (000045) already exist;
  `AppendInTx` already creates the publication row.
- **Read-path sensitive-read audits** (`account.viewed`/`account.listed`) stay out-of-tx by design (no
  business write exists) but must remain error-checked — never `_ =`.

## 6. Compatibility and failure-mode notes

- **Fact content parity**: converted facts carry exactly the fields the `Log` adapter maps today plus
  `Payload["target_uid"]`; existing unit tests asserting audit events keep passing after adjusting for the
  added payload key. Anchored hold/cutover facts are byte-identical (same anchor inputs, same payload).
- **Fail-closed behavior change (intended)**: today a failed post-commit append is silently swallowed (ten
  sites) or surfaced as an error after the domain commit (hold/cutover). After conversion, a non-duplicate
  append failure fails the whole transaction: the API returns an error and no domain row survives without
  its fact. This is the direction's durability contract; unit tests with `s.audit == nil` keep the plain
  path for tests.
- **`ErrAuditDuplicate` in-tx**: swallowed only after the domain write succeeded inside the same tx
  (contract §4.2 Class B); it can never mask a failed domain write and never leaves a ledger row without
  its domain row.
- **Crash windows closed**: no migrated mutation has a commit-to-append gap; the outbox worker
  (`drainPublications`) remains the only delivery path, so a fact that committed is eventually delivered at
  least once (idempotent, dedup-keyed).
