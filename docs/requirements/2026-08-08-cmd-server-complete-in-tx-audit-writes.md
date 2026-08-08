# Requirements — cmd/server: complete the in-tx audit migration (ten post-commit Log sites + hold/cutover → WithAudit, atomicAudit fallback tails deleted)

| | |
|---|---|
| Module (batch label) | `cmd/server` (composition root `internal/app/new.go`; spans `internal/service`, `internal/repository`, `scripts/check-audit-swallow.sh`, `scripts/check-anchored-event-ids.sh`) |
| Source analysis | `docs/auto/analyses/cmd-server-7a3bfea7.json` (direction 1 of 3) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery; gate G3: T-4/T-5) |
| Contract | `docs/proposals/audit-contract-batch-aero-id.md` (§B2-1, §B2-2) |
| Design | `docs/plans/2026-08-06-cmd-server-complete-in-tx-audit-writes-design.md` + hardening; locked by the campaign design gate |
| Date | 2026-08-08 |
| Status | Requirements — every citation re-verified against the repository at head `13e4676` + working tree (2026-08-08). Five corrections (C-1…C-5) amend the direction's citations; C-2 changes the EventID policy for two of the ten sites. |

## 1. Evidence verification (direction citations vs. repository)

All citations in the direction were re-checked against the current tree. Verdicts:

| # | Direction claim | Verification |
|---|---|---|
| 1 | Ten legacy audit write paths append standalone AFTER the business commit via `service.audit.Log` (admin grant/revoke, ban/unban, block/unblock, comment-report resolve, message delete, file-share grant/revoke) | ✅ Exact. 10 `s.audit.Log` sites, all `if err :=`-checked (none `_ =`), all after the domain repo call committed: `admin.go:93` (GrantRole), `admin.go:122` (RevokeRole), `ban.go:84` (Ban), `ban.go:122` (Unban), `block.go:54` (Block), `block.go:79` (Unblock), `comment.go:415` (ResolveReport), `file_share.go:114` (ShareFile grant loop), `file_share.go:148` (UnshareFile), `message.go:321`/`:345` via the helper `auditMessageDelete` (`message.go:354-363`, cited as `:352-364` in the direction — 2-line drift). Facts and payloads per site: admin `{role}`; ban `{reason, expiresAt}`; block/unblock/admin/ban/unban/fshare `TargetUID = uid/grantee`; comment `{comment_id, status}`; file share one fact per grantee |
| 2 | The backing repositories have no ledger field; constructors in `new.go` still take only `(gdb[, outbox, counter])` | ✅ For **six** repos: `adminRepo{db}` (`repository/admin.go:31-33`), `banRepo{db}` (`ban.go:36-38`), `blockRepo{db}` (`block.go:40`), `commentRepo{db, outbox}` (`comment.go:64-66`), `messageRepo{db, outbox, counter}` (`message.go:70-72`), `fileShareRepo{db}` (`file_share.go:32`). Constructors without ledger at `new.go:132` (fileShare), `:141` (msg), `:142` (comment), `:144` (block), `:145` (ban), `:148` (admin) — direction's `:141-148` holds with fileShare one line earlier. **C-3:** the hold/cutover repos are ALREADY converted — `NewAuditHoldRepository(db, audit)` (`audit_hold.go:82`), `NewLegacyCutoverRepository(db, audit)` (`legacy_cutover.go:97`), both `requireAuditLedger`-guarded; wired at `new.go:228` and `app/audit.go:221,223`. The design's §3.1 "eight signatures change" is stale; **six** remain |
| 3 | Four checked-but-post-commit tails remain (operation.create, sync-job create/finish, account-profile update) guarded by the `atomicAudit` bool | ✅ Three of four are `atomicAudit`-bool-shaped: `operation.go:127-142` (tail `:135-142`; cited `:128-142`), `sync_job.go:155-169` (tail `:162-169`), `account_profile.go:44-68` (tail `:61-68`). **C-6:** the fourth — sync-job finish (`sync_job.go:454-472`, cited `:466-471`) — is the type-assert-else shape (`repo.(SyncJobAuditRepository)` → `FinishWithAudit`, else `Finish` + standalone `Record`), not an `atomicAudit` bool; same deletion applies. **C-5 (residual, out of scope):** `legacy_cutover.go:85-107` still carries a fifth `atomicAudit` fallback tail (`:96-107`); it is provably unreachable in the wired root (concrete repo always satisfies `LegacyCutoverAuditRepository` ⇒ `setWithAudit` returns `atomicAudit=true`; `s.audit == nil` fails the guard). The direction and the locked design enumerate four; the cutover tail is **not** added to scope |
| 4 | `internal/app/new.go:141-148` repo constructors without ledger | ✅ See row 2 |
| 5 | Design ready at `docs/plans/2026-08-06-cmd-server-complete-in-tx-audit-writes-design.md` (REQ-3/4/5, constructor-API changes §3.1, per-mutation semantics §3.3); implemented: gate Log-inclusion `scripts/check-audit-swallow.sh`; unstarted: §3.1-§3.3 conversions, §6 step 2-4 | ✅ Design + hardening docs exist; the 210-line requirements mirror `docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md` exists. **C-4:** the gate portion has LANDED further than the design's §3.4 snapshot: `scripts/check-audit-swallow.sh:60-61` already includes `Log` in P1 and P2 (plus `RequeuePublication/RequeueDead` from the B2-5 direction), the negative fixture already keeps `if err := s.audit.Log(` / `return s.audit.Log(` and the positive fixture already carries `_ = s.audit.Log(` + `_ = publisher.Publish(`; Makefile:68-69 scans `internal/service internal/repository internal/app cmd`. Verified green at 0 hits today. **Not yet present:** the mechanical wiring assertions of design §3.4 (drainPublications single call site; `NewAuditLedgerService` single-arg) |
| 6 | T-4 test lines: `TestAuditWiringHappyPath :60`, `TestAuditWiringRollbackAtomicity :175`, `TestAuditWiringPublicationInsertRollsBackLedgerRow :294` | ✅ Exact in `internal/repository/audit_wiring_integration_test.go`. Trigger mechanism `installFailPublicationInsertTrigger` at `:264-285` (`BEFORE INSERT ON audit_publications` raises after the ledger row is staged — the last statement of every WithAudit tx). Coverage today: profile/operation/event only |
| 7 | T-5 wiring: gate asserts `drainPublications` has exactly one call site (callback.go:286 inside Start) and `NewAuditLedgerService` is single-arg | ⚠️ **C-1 (location drift):** `drainPublications` is defined at `internal/service/callback_audit.go:45`; its **only non-test call site is `internal/service/callback_worker.go:47`, inside `callbackService.Start`'s ticker loop (`callback_worker.go:29-51`)** — the worker loop moved out of `callback.go` (file header: "callback_worker.go owns the delivery/publication worker loop (Start)"). `callback.go:286` does not exist today. Test-only call sites exist (`callback_audit_test.go:187,242,408`, `callback_integration_test.go` ×11) and must be excluded from the mechanical assertion. `NewAuditLedgerService` is single-arg today (`audit_ledger.go:56`; sole composition call `app/audit.go:59`) |
| 8 | Anchored EventIDs preserved byte-identical for hold/cutover (`idgen.Anchor`, `scripts/check-anchored-event-ids.sh` stays green) | ✅ Hold anchors at `audit_hold.go:164` (`idgen.Anchor("audit_hold.set", tenant, scopeType, scopeID, reason, RenderAnchorTime(expiresAt))`) and `:184` (`"audit_hold.removed"`, 4-part); cutover anchor in `cutoverSetFactInput`. Gate `scripts/check-anchored-event-ids.sh` R-1 bans `EventID: idgen.NewID()` in non-test Go except the C2 allowlist (`internal/service/callback.go` ReplayDelivery, `cmd/audit-replay/main.go`). Verified green today. **C-2 (material):** two of the ten Log sites ALREADY carry anchored EventIDs — message delete (`ID: id` = message ID; the retry-dedup contract is documented at `message.go:351-353` and exercised by the `deleteAlreadyGone` path `:337-349`) and comment resolve (`ID: reportID` at `comment.go:419`). The Log adapter maps `legacy.ID → EventID` (`audit_ledger.go:141`), so those rows are NOT NULL-event_id today. The design's §4.3 "fresh NewID for all ten; today NULL" premise is false for these two; see REQ-3.3 |
| 9 | Backing facts: `AppendInTx` interface `repository/audit_ledger.go:39`, impl `:123`, `createAuditRows` (ledger + pending publication) `:189`, `ErrAuditDuplicate` `:59`, duplicate check `checkAuditDuplicate` `:174-181`/`:213`, `requireAuditLedger` `repository/audit_wiring.go:19-28`, `buildAuditFact` `service/audit_ledger.go:82`, Log adapter mapping `:136-141` (ActorUID, Action, SubjectType→ResourceType, SubjectID→ResourceID, Payload=Meta; **TargetUID dropped**) | ✅ All exact (minor drift vs. the design's `:106`/`:172-200`/`:147-159`/`:183-193`) |
| 10 | Unit-test fakes assert `Log` via `mocks.Auditor` + repo fakes lacking WithAudit fns (`block_test.go:74,102`, `comment_test.go:559,622`, `message_test.go:571,599`, `file_share_test.go:42`) | ✅ `internal/service/mocks.Auditor` (`mocks/*.go:593-601`) has `LogFn` only; no `*WithAuditFn` fields anywhere in `internal/service/mocks`. No admin/ban repo integration tests exist today (F3) — the T-4 matrix creates them. Constructor call sites to update: `repository/comment_test.go:22`, `repository/message_test.go:22`, `repository/block_integration_test.go:19`, `repository/file_share_integration_test.go:21` |

## 2. Problem statement (current state)

Audit durability is split. Operation/sync/profile/event-ingest/callback/provisioning/hold/cutover mutations already
append facts in-tx via `XxxWithAudit` + `AppendInTx` (`requireAuditLedger`-guarded). Ten live service mutations still
append **after** commit via `service.audit.Log` (admin×2, ban×2, block×2, comment×1, message×1, file_share×2), and four
out-of-tx fallback tails keep a post-commit `s.audit.Record` shape alive (operation.create, sync create, sync finish,
profile update) behind `atomicAudit`/type-assert plumbing that is provably unreachable in the wired root. Every one of
these sites keeps the crash window: the domain row commits and the fact is lost. The six backing repos have no ledger
field, so the WithAudit surface that would make each mutation + `ledger.AppendInTx` one gorm tx does not exist.

Gates today: `make check-audit-swallow` is green at 0 hits (the ten sites pass because they are `if err :=`-checked);
`make check-anchored-event-ids` is green.

## 3. Requirements

### REQ-1 — Repository constructor API: the six mutation repos gain the ledger (fail-closed)

Change exactly six constructors (hold/cutover already done — C-3):

| Constructor | Old | New |
|---|---|---|
| `NewAdminRepository` | `(db)` | `(db, audit AuditLedgerTransactionalRepository)` |
| `NewBanRepository` | `(db)` | `(db, audit)` |
| `NewBlockRepository` | `(db)` | `(db, audit)` |
| `NewCommentRepository` | `(db, ob outbox.Repository)` | `(db, ob, audit)` |
| `NewMessageRepository` | `(db, ob, counterOpt ...*UnreadCounter)` | `(db, ob, audit, counterOpt ...*UnreadCounter)` |
| `NewFileShareRepository` | `(db)` | `(db, audit)` |

Each struct gains `audit AuditLedgerTransactionalRepository`; each constructor calls
`requireAuditLedger("NewXxxRepository", audit)` (panic on nil = fail at startup; precedent `repository/audit_wiring.go:19-28`).
Plain methods (`GrantRole`, `Ban`, `Block`, `ResolveReport`+`SetStatus`, `SoftDeleteWithEvent`, `Grant`/`Revoke`) stay;
`XxxWithAudit` variants are additive. Update every call site: `internal/app/new.go:141,142,144,145,148` (pass
`auditLedgerRepo`, in scope from `buildAuditStack` at `:134`) — **F-1:** the `fileShareRepo` construction at
`new.go:132` must be moved below `buildAuditStack` (`:134`) in the same step, since `auditLedgerRepo` is not in
scope at `:132` — and the test constructors of §1 row 10 (pass a real `NewAuditLedgerRepository(db, cfg)`).

### REQ-2 — New repository audit interfaces (sibling `*_audit.go` files; one gorm tx each: domain write → `AppendInTx`)

| Interface (new file) | Methods |
|---|---|
| `AdminAuditRepository` (`admin_audit.go`) | `GrantRoleWithAudit(ctx, uid, roleCode, grantedBy string, fact *domain.AuditFact) error` · `RevokeRoleWithAudit(ctx, uid, roleCode string, fact) error` |
| `BanAuditRepository` (`ban_audit.go`) | `BanWithAudit(ctx, uid, actorUID, reason string, expiresAt *time.Time, fact) error` · `UnbanWithAudit(ctx, uid string, fact) error` |
| `BlockAuditRepository` (`block_audit.go`) | `BlockWithAudit(ctx, blocker, blocked string, fact) error` · `UnblockWithAudit(ctx, blocker, blocked string, fact) error` |
| `CommentAuditRepository` (`comment_audit.go`) | `ResolveReportWithAudit(ctx, reportID domain.ID, status domain.ReportStatus, resolverUID string, at time.Time, hideCommentID domain.ID, fact) error` — merges today's two-call service flow (report update + conditional comment hide) into one tx; `hideCommentID == zero` skips the hide; report-update `RowsAffected == 0` returns `ErrNotFound` (same as plain `ResolveReport`) |
| `MessageAuditRepository` (`message_audit.go`) | `SoftDeleteWithEventAudit(ctx, id domain.ID, evt *outbox.Event, fact) error` — one tx: soft delete + outbox event (when `evt != nil && r.outbox != nil`) + fact |
| `FileShareAuditRepository` (`file_share_audit.go`) | `GrantWithAudit(ctx, fileID domain.ID, ownerUID string, granteeUIDs []string, facts []*domain.AuditFact) error` — N grant rows + N facts, one tx, `len(facts) == len(granteeUIDs)` (service guarantees) · `RevokeWithAudit(ctx, fileID domain.ID, granteeUID string, fact) error` |

Unconditional fact append matches today's Log-after-call placement (facts record even when the domain write was a 0-row
no-op: revoke of absent role, unban of unbanned, unshare of unshared, resolve with no status change). Short-circuits that
today return before the Log site stay short-circuits (message delete of a missing message, file share with zero
grantees). `AuditFact` is appended exactly as built by the service — the repo never re-derives anchors. `ErrAuditDuplicate`
is swallowed in-tx **after** the domain write (Class B; never masks a failed domain write).

### REQ-3 — Service conversions: the ten post-commit `Log` sites become fail-closed WithAudit calls

New shared helper `internal/service/audit_fact.go`:

```go
// buildLegacyAuditFact reproduces the Log-adapter mapping (audit_ledger.go:136-141:
// ActorUID, Action, SubjectType→ResourceType, SubjectID→ResourceID, Meta→Payload)
// plus TargetUID→Payload["target_uid"] (REQ-3.2) and the EventID policy of REQ-3.3.
func buildLegacyAuditFact(ctx context.Context, actor, action, subjectType, subjectID, targetUID string,
	meta map[string]any) *domain.AuditFact
```

1. **Shape** (six services `admin.go`, `ban.go`, `block.go`, `comment.go`, `message.go`, `file_share.go`): replace each
   `if s.audit != nil { s.audit.Log(...) }` tail with the landed fail-closed shape:
   ```go
   if s.audit != nil {
       repo, ok := s.blocks.(repository.BlockAuditRepository)
       if !ok {
           return apperr.Internal("block.audit_unavailable", "block audit persistence is unavailable")
       }
       return repo.BlockWithAudit(ctx, actor, uid,
           buildLegacyAuditFact(ctx, actor, domain.AuditUserBlock, "user", uid, uid, nil))
   }
   return s.blocks.Block(ctx, actor, uid)
   ```
   New error codes: `ban.audit_unavailable`, `block.audit_unavailable`, `comment.audit_unavailable`,
   `message.audit_unavailable`, `file.audit_unavailable` (precedents:
   `admin.audit_unavailable` already in use at `admin.go:51`, `operation.audit_unavailable` `operation_audit.go:28`,
   `callback.audit_unavailable` `callback.go:167`,
   `account.audit_unavailable` `account_provision.go:154`). With `s.audit == nil` (unit tests) the plain repo path is
   byte-for-byte today's behavior.
2. **Fact parity (T-5):** each converted fact equals the Log-adapter mapping plus `Payload["target_uid"]` (the adapter
   drops `TargetUID` today — `audit_ledger.go:136-141`; conversion is the moment to stop losing it). Per-mutation payloads
   unchanged: admin `Payload["role"]`, ban `{"reason","expiresAt"}`, comment `{"comment_id","status"}` (statusLabel
   `resolved`/`dismissed`), file-share `target_uid` = grantee (one fact per grantee).
3. **EventID policy (C-2 amendment to design REQ-3.3):**
   - **message delete and comment resolve keep their existing anchored EventIDs byte-identical** — message ID
     (`message.go:354-363` `ID: id`) and report ID (`comment.go:419` `ID: reportID`). Both anchors are load-bearing: the
     `deleteAlreadyGone` residual path (`message.go:337-349`) and resolve-retry re-derive the same `event_id`, so the
     in-tx `ErrAuditDuplicate` swallow commits the domain write / no-op without fabricating a second fact (contract:
     "a re-delete never fabricates a fresh audit fact", `message.go:353`). The `deleteAlreadyGone` fact-only append stays
     a checked out-of-tx residual (its domain row predates the request; there is no write to join) — it must remain
     `if err :=`-checked and is added to the REQ-5.3 residual set.
   - **the other eight sites** (admin×2, ban×2, block×2, file-share×2) mint a fresh `idgen.NewID()` per call (today
     NULL event_id; a stable reused ID across distinct intents would let `ErrAuditDuplicate` roll back a legitimate
     domain write — the D3 hazard). Because `scripts/check-anchored-event-ids.sh` R-1 bans
     `EventID: idgen.NewID()` outside the C2 allowlist, add `internal/service/audit_fact.go` to the C2 allowlist with a
     comment documenting the eight unanchored legacy-Log sites (the gate must stay green — supplied acceptance A-4; no
     grep-evasion shapes such as a deferred assignment).
4. **Per-mutation semantics (design §3.3):** message delete keeps one tx (soft delete + outbox event + fact) via
   `SoftDeleteWithEventAudit`; comment resolve passes `hideCommentID = rep.CommentID` when
   `status == Resolved && c.Status == Normal` (intended atomicity change: a failing conditional hide now rolls the
   resolve back with it — report-not-found still wins when both fail); file share builds one fact per grantee and calls
   `GrantWithAudit`. The `audit` field stays (nil gate); `With*AuditLogger` options and wiring unchanged. No
   `s.audit.Log` call remains for these mutations (the `Log` adapter itself stays: `List`-adjacent legacy surface).

### REQ-4 — Fallback-tail deletion: four tails, `atomicAudit` plumbing dropped

1. Delete the post-commit tails at `operation.go:135-142`, `sync_job.go:162-169` (create), `sync_job.go:454-472`
   (finish — C-6 shape), `account_profile.go:61-68`.
2. Drop the `atomicAudit` bool from `operationService.createWithAudit` (`operation_audit.go:19-28`),
   `syncJobService.createWithAudit` (`sync_audit.go:19-28`), and `updateAccountProfile` — they become error-returning
   with fail-closed heads (`if !ok { return apperr.Internal(...) }`; `operation.audit_unavailable` and
   `sync.audit_unavailable` already exist at `operation_audit.go:28`/`sync_audit.go:29`, new
   `account.profile_audit_unavailable` — today's tail error is `account.profile_audit_failed`). The sync-job finish
   path becomes
   `s.audit != nil → repo.(SyncJobAuditRepository) else fail-closed`; plain `repo.Finish` only when `s.audit == nil`.
3. Residual out-of-tx `s.audit.Record` calls are then only the justified, always-checked set: `account.go:155,186`
   (sensitive reads), `operation_state.go:39`, `replay.go:145,240`, `event_ingest.go:154` (quarantine), plus the
   message `deleteAlreadyGone` fact-only append (REQ-3.3). Never `_ =` — `make check-audit-swallow` enforces.

### REQ-5 — T-4: crash-injection matrix over every migrated mutation

Extend `internal/repository/audit_wiring_integration_test.go` (or per-repo siblings, creating admin/ban suites where
none exist) with a table-driven matrix using the existing `BEFORE INSERT ON audit_publications` trigger mechanism
(`installFailPublicationInsertTrigger :264-285`), covering the 16 mutation classes of REQ-6.3 (enumeration of design
§7; file-share grant/unshare counted separately): profile update · operation create · operation step finish · event
apply · callback register / delete / replay-reset · audit hold put / remove · legacy cutover set · account
provisioning (both variants) · sync job create / finish · admin grant role / revoke role · ban / unban · block /
unblock · comment report resolve · message delete · file share grant (N grantees → N facts) / unshare.

- **Crash side** (trigger installed): `XxxWithAudit` returns an error; a fresh connection sees **0 `audit_ledger`
  rows + 0 `audit_publications` rows + 0 domain rows**.
- **Commit side** (no trigger): **1 ledger row** (correct `event_id`/`action`) **+ 1 `pending` publication row +
  domain row**; file-share grant with N grantees → N/N/N.
- Where the duplicate path is meaningful (Class A), keep the `TestAuditWiringRollbackAtomicity` duplicate-injection
  assertions.
- Run: `make test-integration` / `go test -tags=integration ./internal/repository/ -run AuditWiring`.

### REQ-6 — T-5 wiring: single delivery path and gate (keep + prove)

1. `make check-audit-swallow` stays green at 0 hits after REQ-3/REQ-4 — converted sites keep the `if err :=` /
   `return` shape (P1/P2 already include `Log`; fixtures already carry `_ = s.audit.Log(` and `_ = publisher.Publish(`).
   A reintroduced `_ = s.audit.`/`_ = publisher.Publish` anywhere in `internal/service internal/repository internal/app
   cmd` turns the gate red (Makefile:68-69).
2. Add the mechanical wiring assertions to `scripts/check-audit-swallow.sh` (design §3.4): (a) `drainPublications(`
   occurs exactly **once outside its definition and outside `_test.go` files, and that call site is inside
   `callbackService.Start`** — current location `internal/service/callback_worker.go:47` within the ticker loop
   `:29-51` (C-1; assert location, not the stale `callback.go:286`); (b) `NewAuditLedgerService(` is never called with
   a second argument in non-test Go; (c) `_ = publisher.Publish` remains 0.
3. New unit test with a fake `repository.AuditPublicationRepository` recording `ClaimPublication` calls
   (`internal/service/callback_audit_test.go`): construct the callback service with the fake, run `Start` with a short
   interval — claims happen from the ticker, and **no other service entry point triggers `ClaimPublication`** (the
   direct `drainPublications` calls in `callback_audit_test.go:187,242,408` and `callback_integration_test.go` are
   test-only and must not be the proof).
4. `TestAuditLedgerRecordEnqueuesOnlyIntegration` (`internal/app/audit_integration_test.go:278`) stays green: one
   `Record` → exactly 1 ledger row + 1 pending publication row, no governance delivery.

### REQ-7 — Anchored EventIDs preserved (keep-green)

`scripts/check-anchored-event-ids.sh` stays green: hold/cutover anchors byte-identical (same anchor inputs → same
`event_id`; crash-retry re-derivation → in-tx duplicate swallowed, domain write commits, exactly one fact); message/report
anchors preserved (REQ-3.3); the fresh-ID minting for the eight unanchored sites is confined to the allowlisted helper.

## 4. Acceptance mapping (supplied acceptance, made testable)

| Supplied acceptance | Requirement | Test artifact / pass criterion |
|---|---|---|
| T-4: extend the `audit_wiring_integration_test.go` trigger-injection pattern (currently profile/operation/event only: `TestAuditWiringHappyPath :60`, `TestAuditWiringRollbackAtomicity :175`, `TestAuditWiringPublicationInsertRollsBackLedgerRow :294`) to a table over the 16 mutation classes of REQ-6.3; crash side → error + 0 `audit_ledger` + 0 `audit_publications` + 0 domain rows; commit side → 1 ledger row + 1 pending publication row + domain row | REQ-5 | `go test -tags=integration ./internal/repository/` — table-driven test enumerating all 16 classes (REQ-5 list) × {crash, commit}; crash rows assert error non-nil and three zero-counts from a fresh connection; commit rows assert 1/1/1 (N/N/N for N-grantee file share) with correct `event_id`/`action`; admin/ban suites created where none exist today; `TestAuditWiringPublicationInsertRollsBackLedgerRow` generalized or kept as the trigger mechanism's carrier |
| T-5: `make check-audit-swallow` stays green at 0 hits (gate already includes `Log` in P1/P2; converted sites keep `if err :=` shape), and the ten converted facts carry adapter-parity fields plus `target_uid` | REQ-3.2, REQ-6.1 | `make check-audit-swallow` green (self-test + live scan, 0 hits); unit tests rewritten from `mocks.Auditor.LogFn` assertions (`block_test.go:74,102`, `comment_test.go:559,622`, `message_test.go:571,599`, `file_share_test.go:42`, admin/ban) to fact-level assertions on the fake repo's captured `*WithAuditFn` argument: `ActorUID`, `Action`, `ResourceType`=SubjectType, `ResourceID`=SubjectID, `Payload`=Meta fields + `Payload["target_uid"]` present and equal to the legacy `TargetUID`; a reintroduced swallow in any live service file turns the gate red |
| T-5 wiring: gate asserts `drainPublications` has exactly one call site (inside `Start` — current `callback_worker.go:47`, not the stale `callback.go:286`) and `NewAuditLedgerService` is single-arg; new unit test with fake `AuditPublicationRepository` proves `Start` is the only `ClaimPublication` entry point | REQ-6.2, REQ-6.3 | Gate mechanical section added to `scripts/check-audit-swallow.sh` (counts `drainPublications(` = 1 outside definition + `_test.go`; asserts that call site lies inside the `Start` function; greps `NewAuditLedgerService(` with a second argument in non-test Go = 0); unit test `TestDrainPublicationsOnlyFromStart` with recording fake passes; enqueue-only integration test stays green |
| Anchored EventIDs preserved byte-identical for hold/cutover (`idgen.Anchor`, `scripts/check-anchored-event-ids.sh` stays green) | REQ-7 (+REQ-3.3) | `make check-anchored-event-ids` green; hold/cutover integration tests keep asserting same anchor inputs → same `event_id`, crash-retry → in-tx duplicate swallowed with exactly one fact; new assertions: message-delete and comment-resolve ledger rows carry the message-ID/report-ID `event_id` byte-identical to today; the only `EventID: idgen.NewID()` outside the C2 allowlist is none — the helper `internal/service/audit_fact.go` is on the allowlist |

## 5. Scope boundaries (explicit non-goals)

- **Legacy unwired tree** (`internal/{file,message,comment,block,notification,search}` + `internal/audit`): not
  migrated, not scanned (documented gate exclusion).
- **`legacy_cutover.go:96-107` fallback tail (C-5 residual):** provably unreachable in the wired root; the direction
  and the locked design enumerate four tails; **not** added to this scope. If it is later removed, it is a one-line
  mechanical cleanup — do it in a separate task.
- **Other directions in the same batch:** dead-letter replay/requeue (B2-5), TokenSource 401 invalidation (B2-3),
  activation gate first-event e2e (B2-6) — out of scope (their gate additions — `Requeue*` in P1/P2, `purgeDeadLetters`
  — are already landed and kept).
- **Best-effort SSE hub** (`notification.go` bare `d.pusher.Publish`): not audit; remains excluded from P2.
- **`_ = s.receipts.QuarantineEvent`** (`event_ingest.go:120`): swallowed non-audit repo call; not part of this
  direction.
- **No new migrations, no protobuf changes, no HTTP/gRPC API changes.** `audit_ledger`/`audit_publications` + UNIQUE
  `(fact_id)` (migration 000045) already exist; `AppendInTx` already creates the pending publication row.

## 6. Compatibility and failure-mode notes

- **Fail-closed behavior change (intended):** a non-duplicate append failure now fails the whole mutation (API error,
  no domain row without its fact) instead of committing the domain write and losing the fact (swallowed at 10 sites
  today). This is the direction's durability contract; unit tests with `s.audit == nil` keep the plain path.
- **Comment-resolve atomicity change (intended, edge case):** today a failing conditional comment hide surfaces after
  the report resolve committed; in-tx the resolve rolls back with it. Report-not-found still wins when both fail.
- **`ErrAuditDuplicate` in-tx:** swallowed only after the domain write succeeded inside the same tx; never masks a
  failed domain write; never leaves a ledger row without its domain row.
- **Crash windows closed:** no migrated mutation has a commit-to-append gap; `drainPublications` remains the only
  delivery path, so a committed fact is eventually delivered at least once (idempotent, dedup-keyed).
- **Engineering gates:** new repo methods live in sibling `*_audit.go` files; each new function ≤50 lines / cyclop ≤10
  (straight-line tx wrappers); `make check-file-sizes`, `make lint-strict` must stay green.
- **Done gate:** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow &&
  make check-anchored-event-ids && make check-integration-compile && make build` + `make test-integration`
  (repository SQL, outbox, and callback delivery are touched).
