# Design — T-4 matrix: per-mutation crash-injection before/after the audit append (12 mutation classes, rollback-verified), service unit tests rewritten to fact-level assertions, new admin/ban repo integration tests

| | |
|---|---|
| Umbrella plan | `docs/plans/2026-08-06-cmd-server-complete-in-tx-audit-writes-design.md` (migration steps 1–6; this doc fills step 3's unit-test detail and step 5's T-4 detail) |
| Requirements | `docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md` (REQ-6 T-4 matrix, REQ-7.2 wiring test) |
| Module | `internal/service` (unit tests + `mocks`), `internal/repository` (integration tests), `internal/repository/testpg` |
| Head verified | `13e4676` + uncommitted working tree (all line proofs below re-checked live) |
| Campaign | `aero-id-b2-in-tx-audit` (gate G3: T-4) |
| Date | 2026-08-06 |
| Status | Design — the missing T-4 deliverables specified to test level |

## 1. Current-state verification (what the T-4 deliverables must close)

| # | Claim | Verified proof |
|---|---|---|
| 1 | Crash-injection integration suite covers profile/operation/event only, not the 12 converted mutations | `internal/repository/audit_wiring_integration_test.go` (316 lines): `TestAuditWiringHappyPath` `:60` (profile, op create, step finish, event apply), `TestAuditWiringRollbackAtomicity` `:175` (duplicate-injection, profile + step), `TestAuditWiringPublicationInsertRollsBackLedgerRow` `:268` (trigger-injected, profile only). No admin/ban/block/comment/message/file_share/hold/cutover WithAudit coverage exists |
| 2 | Injection mechanism precedent: `BEFORE INSERT ON audit_publications` trigger, `t.Cleanup` drop, counts by `event_id` | `audit_wiring_integration_test.go:268-310`; helpers `countAuditLedgerRows`/`countAuditPublicationRows` `:31-59`; `testpg.Start(t)` = one fresh Postgres 16 container per test (`testpg/testpg.go:23-50`) |
| 3 | Append statement order inside `AppendInTx` (defines the injection points) | `repository/audit_ledger.go:106-135` `AppendInTx` → `appendInTx` `:135`: dup-check → `pg_advisory_xact_lock(hashtext(partition))` → dup-check → chain-head read → `createAuditRows` `:172`: **ledger INSERT first, then publication INSERT** → caller's COMMIT |
| 4 | Service unit tests assert audit via `mocks.Auditor.LogFn`, i.e. on the *logger*, not the persisted fact | `block_test.go:68,96` (`TestBlockService_BlockEmitsAuditEvent`/`_UnblockEmitsAuditEvent`), `comment_test.go:544` (`_ResolveReportEmitsAuditEvent`), `message_test.go:560,595` (`_DeleteEmitsAuditEvent`, `_DeleteMissingDoesNotAudit`). `file_test.go` has ShareFile behavior tests (`:282`) but **no audit assertion**; admin/ban have **no service unit tests at all** |
| 5 | Hold/cutover service tests capture facts at the ledger-repo spy, not the mutation repo | `audit_hold_test.go` / `legacy_cutover_test.go` use `auditLedgerRepoSpy`/`dedupAuditRepoSpy` (`audit_ledger_test.go:12,84`) — spies on `AuditLedgerRepository.Append`; the service will no longer call `Append` directly after conversion |
| 6 | No admin or ban repository integration tests exist today | `ls internal/repository/*integration_test.go` — no `admin_*`, no `ban_*`. `comment_test.go:22`/`message_test.go:22` are integration-tagged and construct the repos with `(db, outbox)` — they gain the ledger param; `block_integration_test.go:19`, `file_share_integration_test.go:21`, `audit_hold_integration_test.go`, `legacy_cutover_integration_test.go` same |
| 7 | Fail-closed constructor precedent + unit gate test | `requireAuditLedger` (`repository/audit_wiring.go:19-28`, typed-nil aware); `TestConstructorsRejectNilAudit` (`audit_wiring_test.go:23-36`) must gain the 8 new constructors |
| 8 | Domain-write semantics per table (define "row absent" vs "prior state preserved" per class) | `admin.go` `GrantRole` = INSERT `user_roles` ON CONFLICT DO NOTHING, `RevokeRole` = DELETE; `ban.go` `Ban` = upsert `user_bans`, `Unban` = soft delete (`deleted_at`); `block.go:45-58` `Block` = INSERT `user_blocks` ON CONFLICT DO NOTHING, `Unblock` = DELETE; `audit_hold.go:80-96` `PutHold` = upsert `audit_holds`, `RemoveHold` = DELETE; `legacy_cutover.go:99-103` `SetCutover` = upsert `legacy_cutover`; `file_share.go:43-56` `Grant` = N-row INSERT DO NOTHING, `Revoke` = DELETE; outbox events land in `outbox_events` |
| 9 | A fake `AdminService` precedent exists for the new ban unit tests | `account_test.go:46` (`allowAllChecker`), `tenant_boundary_test.go:34`, `operation_worker_crash_test.go:151` — local minimal `HasPermission` fakes |

## 2. Scope: the 12 mutation classes (13 `WithAudit` entry points)

The "12 mutations" = the 10 converted `_ = s.audit.Log` sites (REQ-3) + the hold pair and cutover set (REQ-4), with hold put/remove counted as one class pair — 13 method-level entry points:

| # | Class | Service method | Repo `WithAudit` method | Domain write (rollback-verification shape) | Facts | EventID |
|---|---|---|---|---|---|---|
| 1 | admin grant | `GrantRole` | `GrantRoleWithAudit` | INSERT `user_roles` (absent → present) | 1 | fresh |
| 2 | admin revoke | `RevokeRole` | `RevokeRoleWithAudit` | DELETE `user_roles` (present → absent) | 1 | fresh |
| 3 | ban | `Ban` | `BanWithAudit` | upsert `user_bans` (absent → present) | 1 | fresh |
| 4 | unban | `Unban` | `UnbanWithAudit` | soft-delete `user_bans` (active → deleted_at set) | 1 | fresh |
| 5 | block | `Block` | `BlockWithAudit` | INSERT `user_blocks` (absent → present) | 1 | fresh |
| 6 | unblock | `Unblock` | `UnblockWithAudit` | DELETE `user_blocks` (present → absent) | 1 | fresh |
| 7 | comment resolve | `ResolveReport` | `ResolveReportWithAudit` | UPDATE `comment_reports` status + conditional UPDATE `comments` → hidden (unchanged → changed) | 1 | fresh |
| 8 | message delete | `Delete` | `SoftDeleteWithEventAudit` | UPDATE `messages` deleted_at + INSERT `outbox_events` (both absent → present) | 1 | fresh |
| 9 | file share grant | `ShareFile` | `GrantWithAudit` | INSERT `file_shares` × N (N=2 in the matrix) | N | fresh ×N |
| 10 | file share revoke | `UnshareFile` | `RevokeWithAudit` | DELETE `file_shares` (present → absent) | 1 | fresh |
| 11 | audit hold put | `Put` | `PutHoldWithAudit` | upsert `audit_holds` (absent → present) | 1 | anchored 6-part |
| 12 | audit hold remove | `Remove` | `RemoveHoldWithAudit` | DELETE `audit_holds` (present → absent) | 1 | anchored 4-part |
| 13 | cutover set | `Set` | `SetCutoverWithAudit` | upsert `legacy_cutover` (absent → present) | 1 | anchored 4-part |

The already-migrated classes (profile, op create, op step finish, event apply, callback register/delete/replay-reset, provisioning ×2, sync create/finish) are *not* re-listed — they are covered by the existing suite (umbrella §7 counted them in its 16-class figure); this matrix adds the missing 12.

Class-2 semantic note: revoke/unban/unblock/unshare/resolve-without-status-change are **0-row or no-op domain writes** today and still append a fact (unconditional fact, umbrella §3.2). The crash-side verification for these asserts *prior state preserved* (the pre-existing row still present), not "row absent" — the table's `verifyGone` closure encodes the per-class shape.

## 3. T-4 crash-injection matrix (REQ-6)

### 3.1 Injection points — "before" and "after" the audit append

Every `WithAudit` tx executes: domain write(s) → `AppendInTx` → COMMIT, with `AppendInTx` writing ledger row then publication row. Three deterministic injection points, all using the established trigger mechanism (a raised exception aborts the tx exactly like a process crash before COMMIT — the rollback is identical, which is the property under test):

| Point | Trigger | Fires | What it proves |
|---|---|---|---|
| **BP — before the append** | `BEFORE INSERT ON audit_ledger` | at the append's first write, after the domain write already executed in-tx | the domain write has no commit window before the append starts |
| **MP — mid-append** | `BEFORE INSERT ON audit_publications` | after ledger INSERT, before publication INSERT (existing FM-2 mechanism) | a publication can never exist without its ledger row |
| **AP — after the append** | `AFTER INSERT ON audit_publications` | after both append rows inserted, before COMMIT | **the append completed yet still nothing survives** — the exact guarantee the pre-migration post-commit code violated |

The supplied acceptance ("crash-injection before and after the audit append … mutation and fact both absent") maps to **BP + AP**; MP already exists (FM-2) and is kept as the mid-point regression. All three assert the same triple: call returns error ∧ 0 `audit_ledger` rows ∧ 0 `audit_publications` rows ∧ domain state per class.

### 3.2 Harness (new file `internal/repository/audit_wiring_crash_test.go`, `//go:build integration`)

```go
type appendFailurePoint int
const (
    failBeforeLedgerInsert appendFailurePoint = iota // BP
    failAfterPublicationInsert                        // AP
)

// installAuditAppendFailure installs one raising trigger on the shared
// connection, registered for t.Cleanup. One trigger per subtest; the shared
// function name is fine because each test function owns a fresh container.
func installAuditAppendFailure(t *testing.T, db *gorm.DB, point appendFailurePoint)
```

- The raising function is created once per test function: `CREATE FUNCTION raise_audit_append_failure() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'audit append failure injected (test)'; END; $$ LANGUAGE plpgsql`; the two triggers reference it; `DROP TRIGGER`/`DROP FUNCTION` in `t.Cleanup` (mirrors `audit_wiring_integration_test.go:272-285`).
- **Refactor FM-2 to use the helper** (`TestAuditWiringPublicationInsertRollsBackLedgerRow` keeps its assertions but drops its inline SQL) so there is exactly one copy of the trigger SQL.
- Move/extend the counters into this file: `countAuditLedgerRows`, `countAuditPublicationRows` (unchanged semantics), plus `assertFactRow(t, db, eventID, want *domain.AuditFact)` (SELECT `event_id, source, source_region, partition_key, tenant_id, actor_uid, action, resource_type, resource_id, payload` and compare — proves the repo wrote exactly the fact it was handed) and `assertNoAuditRows(t, db, eventID)` (both counts == 0).

### 3.3 Table engine (new file `internal/repository/audit_wiring_matrix_integration_test.go`)

```go
type mutationClass struct {
    name    string
    setup   func(t *testing.T, db *gorm.DB, ledger repository.FullAuditLedgerRepository) (*mutationProbe, error)
}
type mutationProbe struct {
    fact     *domain.AuditFact // built exactly as the service would (buildLegacyAuditFact rule / idgen.Anchor)
    verifyOK func(t *testing.T, db *gorm.DB)   // commit side: domain state changed + assertFactRow
    verifyGone func(t *testing.T, db *gorm.DB) // crash side: domain state per class (absent or prior state)
}

func migratedMutationClasses() []mutationClass // 13 rows, one per §2 entry point
```

Engine tests (each `t.Run` over `migratedMutationClasses()`, subtests sequential on the one shared container; per-class unique tenant/uids/EventIDs keep rows disjoint; each crash subtest installs its trigger inside the subtest and drops it via `t.Cleanup`):

- **`TestAuditWiringCommitSide_AllMigratedMutations`** (T-4.1): per class — mutation succeeds; `countAuditLedgerRows==1` (N=2 for file-share grant), `countAuditPublicationRows==1` (N), `verifyOK`. Assert the pending publication row `status='pending'` joined by `fact_id`.
- **`TestAuditWiringCrashBeforeAppend_AllMigratedMutations`** (T-4.2, BP): per class — trigger installed, mutation returns error, 0 ledger rows, 0 publication rows, `verifyGone`.
- **`TestAuditWiringCrashAfterAppend_AllMigratedMutations`** (T-4.3, AP): per class — same triple. This is the acceptance's "fact both absent" even though the append completed.
- **`TestAuditWiringAnchoredRetryDedupInTx`** (T-4.4): hold put, hold remove, cutover set — run the mutation twice with the same anchored EventID (crash-retry re-derivation); second run commits the domain write, the in-tx `ErrAuditDuplicate` swallow (Class B) fires after the domain write, exactly 1 ledger row survives, and `verifyOK` reflects the *retried* domain state (upsert refresh / idempotent delete).
- **`TestAuditWiringConvertedSiteSwallowsDuplicateInTx`** (T-4.5): one converted fresh-ID class (choose **ban** — its upsert always writes, so "domain write committed" is observable: re-ban with a different reason reusing one EventID → `user_bans.reason` = new reason, 1 ledger row). Proves the umbrella §3.2/§3.3 Class-B defensive parity on a converted site. (Class A rollback remains proven by the unchanged `TestAuditWiringRollbackAtomicity` profile case — the duplicate check lives in the shared ledger code, not per mutation.)

Class fixtures (inline in the matrix file, one clearly delimited section per class): seed rows via plain SQL/plain repo methods (e.g. roles row for admin, active ban for unban, block pair for unblock, comment+report for resolve, message for delete, file+existing share for revoke, hold row for remove, no row for cutover). Facts: the ten fresh-ID classes build `*domain.AuditFact` per the `buildLegacyAuditFact` rule (Action = `domain.AuditAdminGrantRole` etc., `ResourceType/ResourceID` from subject, `Payload["target_uid"]`, fresh `idgen.NewID()` EventID); hold/cutover build via `idgen.Anchor(...)` with the exact anchor inputs from `service/audit_hold.go`/`legacy_cutover.go` and the test re-derives the expected `event_id` in `verifyOK` — byte-identical anchor preservation is asserted at the repo level (the umbrella's B2-4 compatibility constraint).

### 3.4 Matrix sizing and hygiene

- 13 classes × 3 (commit/BP/AP) + 3 dedup + 1 Class-B = ~43 subtests over 4 test functions = 4 containers, each subtest a sub-second tx — well inside the existing integration budget.
- Partition discipline: per-class tenant prefix (`t-<class>`) → distinct `partition_key` → distinct advisory locks; chain growth in shared containers is fine because counts are `event_id`-scoped.
- `assertFactRow` compares `payload` by JSON round-trip (Postgres JSONB ordering) — assert exact equality for the ten classes (adapter-parity + `target_uid`) and for anchored classes (byte-identical payload incl. `note`).

## 4. Service unit-test rewrite: Auditor mocks → fact-level assertions (REQ-3/REQ-4 test surface)

### 4.1 New/updated test doubles (`internal/service/mocks/mocks.go` + test-local fakes)

- `mocks.BlockRepo`, `mocks.CommentRepo`, `mocks.MessageRepo`, `mocks.FileShareRepo` gain `*WithAuditFn` fields mirroring the §2 signatures (e.g. `BlockWithAuditFn func(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error`), with nil-fn ⇒ plain-path defaults preserved. Compile-time interface assertions gain `_ repository.BlockAuditRepository = (*BlockRepo)(nil)` etc.
- **New `mocks.AdminRepo`** (full `repository.AdminRepository` + `GrantRoleWithAuditFn`/`RevokeRoleWithAuditFn`) and **new `mocks.BanRepo`** (`repository.BanRepository` + `BanWithAuditFn`/`UnbanWithAuditFn`) — neither exists today; defaults model "no-op success / empty lists" per the established convention.
- `fakeHoldRepo` (`audit_hold_test.go:12`) and the cutover fakes (`legacy_cutover_test.go`) gain `PutHoldWithAuditFn`/`RemoveHoldWithAuditFn`/`SetCutoverWithAuditFn`; the WithAudit methods delegate to the Fn and return `ErrAuditDuplicate`-free plain behavior by default (the dedup swallow is repo-side, so the fake does not model it).
- `mocks.Auditor` **stays but is demoted**: after the conversion no live service calls `s.audit.Log` (the ten sites convert; the read-path residuals are `Record`-only and already checked), so `LogFn` assertions disappear. `Auditor` remains as the gate-flip dummy (a non-nil `s.audit` to enter the WithAudit branch) — tests pass `&mocks.Auditor{}` via the existing `With*AuditLogger` options, whose signatures stay (umbrella §4.2). Delete `Auditor` only if a final grep shows zero references (expected: it stays referenced as the dummy).

### 4.2 The fact-level assertion pattern (replaces every `LogFn` capture)

```go
var gotFact *domain.AuditFact
repo := &mocks.BlockRepo{
    BlockWithAuditFn: func(_ context.Context, blocker, blocked string, fact *domain.AuditFact) error {
        gotFact = fact
        return nil
    },
}
svc := service.NewBlockService(repo, service.WithBlockAuditLogger(&mocks.Auditor{}))
// ... svc.Block(ctx, "b")
// assert: gotFact.Action == domain.AuditUserBlock; ActorUID == "a";
// ResourceType == "user"; ResourceID == "b"; Payload["target_uid"] == "b";
// EventID != zero and != any EventID observed in a second call (fresh per call).
```

The audit assertions move from "the logger was handed an event" to "the repo was handed the durable fact inside the mutation tx" — which is the contract the migration establishes (the fact argument is what `AppendInTx` persists verbatim).

### 4.3 Rewritten and new unit tests (file by file)

| File | Today | After rewrite |
|---|---|---|
| `block_test.go` | `TestBlockService_BlockEmitsAuditEvent` `:65`, `_UnblockEmitsAuditEvent` `:93` (LogFn captures at `:68,96`) | Fact-level via `BlockWithAuditFn`/`UnblockWithAuditFn`: exactly 1 call; Action/ActorUID/ResourceType/ResourceID/`target_uid`; fresh non-zero EventID; plus new `TestBlockService_AuditUnavailableFailsClosed` (plain-interface-only fake + non-nil logger → `apperr.Internal("block.audit_unavailable")`) and a two-call test asserting distinct EventIDs |
| `comment_test.go` | `_ResolveReportEmitsAuditEvent` `:544` (LogFn + Meta) | Fact-level via `ResolveReportWithAuditFn` capturing `reportID`, `status`, `resolverUID`, `hideCommentID`, fact; two variants: resolved+normal → `hideCommentID == commentID` and fact `Payload["comment_id"]`/`Payload["status"]=="resolved"`; dismissed → `hideCommentID == zero` and `status=="dismissed"` (the hide is skipped, fact still appended) |
| `message_test.go` | `_DeleteEmitsAuditEvent` `:560`, `_DeleteMissingDoesNotAudit` `:595` | Fact-level via `SoftDeleteWithEventAuditFn`: asserts the outbox event (evt.Type `message.deleted`, `message_id`/`from_uid`/`to_uid` payload) travels with the soft delete + fact in one call; missing-message short-circuit → no repo call at all, 0 facts |
| `file_test.go` | no audit assertions | New `TestFileService_ShareFileEmitsOneFactPerGrantee` (`GrantWithAuditFn`: N grantees ⇒ N facts, `len(facts)==len(grantees)`, each `target_uid` == grantee, distinct EventIDs) and `_UnshareFileEmitsAuditEvent`; zero-grantees short-circuit → no WithAudit call (existing `:301` behavior test extended with a facts counter) |
| `admin_test.go` | **does not exist** | New: GrantRole/RevokeRole behavior (repo args incl. `grantedBy`; blank uid/role 400; self-grant 400; `ErrNotFound`→404; revoke no-op success), fact-level audit via `GrantRoleWithAuditFn`/`RevokeRoleWithAuditFn` (action, actor, `resource_type=="user"`, `resource_id==uid`, `Payload["role"]`, `target_uid`, fresh EventID), fail-closed `admin.audit_unavailable`, and `s.audit==nil` ⇒ plain `GrantRole`/`RevokeRole` called |
| `ban_test.go` | **does not exist** | New: Ban/Unban behavior (repo args: uid, actorUID, reason, expiresAt nil vs set; permission gate via a local `allowAllChecker`-style fake AdminService precedent `account_test.go:46`; self-ban 400; blank reason 400), fact-level audit via `BanWithAuditFn`/`UnbanWithAuditFn` (`Payload["reason"]`, `Payload["expiresAt"]`), fail-closed `ban.audit_unavailable`, plain-path (`s.audit==nil`) |
| `audit_hold_test.go` | `auditLedgerRepoSpy` captures (`:146,184,205,223`); fails-closed `:115` | `fakeHoldRepo` WithAudit fns capture the fact; golden-anchor tests (`G9/G9b/G10`, `:133-235`) now assert the fact captured by the repo fake — anchored `event_id` byte-identical, `ResourceID` trimmed; `TestAuditHoldAuditFailureFailsClosed` rewires: `PutHoldWithAuditFn`/`RemoveHoldWithAuditFn` return the injected error, service still maps to `audit_hold.audit_failed` (umbrella §4.1 same-code-for-same-cause); `TestHoldSetRetryDedup` becomes: retry ⇒ repo fake called with the identical anchored fact (the swallow itself is repo-level → T-4.4) |
| `legacy_cutover_test.go` | `auditLedgerRepoSpy` (`:173-226`); fails-closed `:151` | Same pattern via `SetCutoverWithAuditFn`; G11/G12/G13 golden assertions preserved; fails-closed rewired to `cutover.audit_failed` via the fake's WithAudit error |
| `audit_ledger_test.go` | spies `:12,84` | Unchanged — the spies are the `AuditLedgerService` unit-test doubles and the service keeps `Record` for the read-path residuals |

New error-code contract asserted in the fail-closed tests (umbrella §3.3): `admin.audit_unavailable`, `ban.audit_unavailable`, `block.audit_unavailable`, `comment.audit_unavailable`, `message.audit_unavailable`, `file.audit_unavailable`; hold/cutover keep `audit_hold.audit_failed`/`cutover.audit_failed` for in-tx append failures.

### 4.4 Fail-closed fakes

Because `*mocks.XxxRepo` will implement the audit interface after §4.1, the `*.audit_unavailable` tests need a fake that implements **only** the plain interface. Each affected test file defines one tiny local type (e.g. `plainBlockRepo struct { repository.BlockRepository }` embedding the interface — an embedded nil interface implements nothing extra; the type assertion `s.blocks.(repository.BlockAuditRepository)` fails). This mirrors how the production fail-closed branch is only reachable with a non-concrete repo.

## 5. New admin/ban repository integration tests (real PostgreSQL)

Both files are `//go:build integration`, package `repository_test`, using `testpg.Start` + `testpg.Migrate` (pattern: `block_integration_test.go:12-19`). They construct repos with the **new** signatures (umbrella §3.1) — they land in migration step 2, before the matrix (step 5) reuses their fixtures.

### 5.1 `internal/repository/admin_integration_test.go` (new)

- **Constructor + wiring**: `adminRepo := repository.NewAdminRepository(db, ledger)` with `ledger := repository.NewAuditLedgerRepository(db, AuditLedgerConfig{SigningKey: ...})`; nil-ledger panic is covered by extending `TestConstructorsRejectNilAudit` (`audit_wiring_test.go:23`) with `NewAdminRepository`, `NewBanRepository`, `NewBlockRepository`, `NewCommentRepository`, `NewMessageRepository`, `NewFileShareRepository`, `NewAuditHoldRepository`, `NewLegacyCutoverRepository`.
- **Seed**: roles have no repo writer in `AdminRepository`, so seed via SQL: `INSERT INTO roles (code, name, permissions) VALUES ('moderator', 'Moderator', '{user.ban}')` (permissions is `text[]`).
- **`TestAdminGrantRoleWithAuditCommitsFact`**: `GrantRoleWithAudit(ctx, "u-1", "moderator", "admin-1", fact)` → `user_roles` row (uid, role_code, granted_by) + exactly 1 `audit_ledger` row (assertFactRow: action `admin.grant_role`, actor `admin-1`, `resource_type=="user"`, `resource_id=="u-1"`, `payload["role"]=="moderator"`, `payload["target_uid"]=="u-1"`, event_id == the fresh EventID handed in) + 1 pending `audit_publications` row. Duplicate grant (same pair, second call) → still exactly 1 fact (fresh EventID each call; the domain write no-ops via ON CONFLICT).
- **`TestAdminRevokeRoleWithAuditCommitsFact`**: seed a role grant (plain `GrantRole`), `RevokeRoleWithAudit` → `user_roles` row gone + 1 fact (`admin.revoke_role`); revoke of an absent role still appends its fact (unconditional-fact parity, umbrella §3.2).
- **Plain-path smoke** (this repo had zero integration coverage): `HasPermission` true/false, `ListRoles`, `ListUserRoles` — brief, to anchor the file.
- **Matrix registration**: `adminGrantClass()`, `adminRevokeClass()` fixtures exported for `migratedMutationClasses()` (same package).

### 5.2 `internal/repository/ban_integration_test.go` (new)

- **`TestBanWithAuditCommitsFact`**: `BanWithAudit(ctx, "u-1", "admin-1", "spam", &expiresAt, fact)` → `user_bans` row (uid, actor_uid, reason, expires_at, deleted_at NULL) + 1 ledger row (`action "user.banned"`, `payload["reason"]=="spam"`, `payload["expiresAt"]` round-trips, `target_uid=="u-1"`) + 1 pending publication. Re-ban with a new reason → row refreshed, second fact appended (fresh EventID; no dedup collision — proves the fresh-ID policy on the same subject).
- **`TestUnbanWithAuditCommitsFact`**: seed an active ban, `UnbanWithAudit` → `deleted_at` set + 1 fact (`user.unbanned`); unban of a not-banned uid → no domain change, fact still appended.
- **Plain-path smoke**: `Ban`/`IsBanned` (incl. expiry semantics `ListExpired`), `ListBanned` cursor — today absent; minimal coverage.
- **Matrix registration**: `banClass()`, `unbanClass()`.

### 5.3 Constructor-call-site updates the matrix depends on (umbrella step 2, listed here for completeness)

`block_integration_test.go:19`, `comment_test.go:22`, `message_test.go:22`, `file_share_integration_test.go:21`, `audit_hold_integration_test.go` (`NewAuditHoldRepository(db)`), `legacy_cutover_integration_test.go` — each gains the ledger argument (`NewAuditLedgerRepository(db, cfg)`); `setup*Repo` helpers change signature to take the ledger. These compile breaks are the migration's mechanical surface (umbrella §6 step 2 enumerated the same sites).

## 6. Compatibility constraints

1. **No production-code change in this design** — it specifies tests only, plus the test doubles they require. All production changes remain governed by the umbrella plan (steps 2–4).
2. **Test doubles mirror the §2 signatures exactly**; `mocks` defaults keep every existing non-audit test green (nil-Fn ⇒ no-op success, the established convention).
3. **Anchored golden values stay byte-identical** (G9/G9b/G10/G11–13): the rewritten hold/cutover tests capture the fact at the repo fake and assert the same constants as today; the repo-level matrix re-derives anchors in-test and compares.
4. **Fresh-EventID assertions are structural, not golden**: non-zero, distinct across calls (the ten classes) — never a fixed UUID.
5. **Fact-level unit assertions equal the repo contract**: the unit tests assert the fact handed to the repo; the integration matrix asserts the fact persisted. The `Log`-adapter parity mapping (`ActorUID/Action/SubjectType→ResourceType/SubjectID→ResourceID/Meta→Payload` + added `target_uid`) is asserted at both levels so a drift in either direction fails loudly.
6. **No new migrations, no protobuf, no Makefile changes.** `go test -tags=integration ./internal/repository/` and `go test ./internal/service/` are the only gates touched.
7. **File-size/cyclop gates unaffected**: all new code is `_test.go` (exempt from `check-file-sizes`) or additions to `mocks.go` (test helper, exempt).
8. **Test determinism**: no wall-clock assertions except `expiresAt` JSON round-trip (ban matrix row uses a fixed UTC instant, asserted via JSONB equality, not time proximity); subtest order fixed (no `t.Parallel` on shared containers).

## 7. Failure modes (of the tests themselves — what each one guards)

| Risk | Guard |
|---|---|
| A converted mutation still appends post-commit (conversion regression) | T-4.2/T-4.3: with any append failure the whole mutation errors and 0 domain rows survive — a post-commit append would leave the domain row committed, failing `verifyGone` |
| Ledger row survives without its publication (or vice versa) | MP/AP: both rows must be 0 after injection; commit side requires exactly 1+1 |
| Fact content drifts from the `Log` adapter (missing `target_uid`, wrong action) | `assertFactRow` exact compare (integration) + §4.3 field assertions (unit) |
| Anchored EventIDs change (B2-4 break) | T-4.1 `verifyOK` re-derives the anchor in-test; §4.3 golden constants |
| Fresh-ID policy regressed to a stable EventID (D3 hazard) | §4.3 two-call distinct-EventID tests + T-4.5 (a stable ID would make the second ban a duplicate that still commits — the test asserts 2 facts for 2 bans) |
| Dedup swallow moved before the domain write (masks a failed domain write) | T-4.5 asserts the *new reason* committed with 1 fact; T-4.4 same for anchored retries |
| Fail-closed head lost (silent plain-write fallback) | §4.3 `*.audit_unavailable` tests with plain-interface-only fakes |
| Trigger SQL drift between FM-2 and the matrix | shared `installAuditAppendFailure` helper (FM-2 refactored onto it) |
| Matrix rows interfere (shared container) | per-class tenants/uids/EventIDs; `event_id`-scoped counts; sequential subtests |
| A mutation's unconditional fact disappears (0-row no-op still audited) | revoke/unban/unblock/unshare classes assert the fact appends even when the domain write is a no-op (commit side) |

## 8. Migration steps (lands inside umbrella steps 2, 3, 5; each leaves the tree green)

1. **Test doubles first (umbrella step 3 companion).** Add the `WithAuditFn` fields and the new `mocks.AdminRepo`/`mocks.BanRepo`; update the 8 service test files per §4.3 — this compiles only against the §2 interfaces, so it lands *with* umbrella step 3 (repo interfaces + service conversions in the same step). The rewritten tests fail (red) until the service conversion lands, proving they test the new contract.
2. **Admin/ban integration files + constructor call sites (umbrella step 2).** `admin_integration_test.go`, `ban_integration_test.go`, extended `TestConstructorsRejectNilAudit`, `setup*Repo` ledger params in the six existing repo integration files. Green against the step-2 repo changes.
3. **Crash harness + matrix (umbrella step 5).** `audit_wiring_crash_test.go`, `audit_wiring_matrix_integration_test.go` (FM-2 refactor included). Green once step-4 wiring delivers the full `WithAudit` surface.
4. **Full gate.** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make build` + `make test-integration` (repository SQL/migrations touched; Postgres required).

## 9. Testable acceptance mapping

| Supplied acceptance | Pass criterion |
|---|---|
| Per-mutation crash-injection **before** and **after** the audit append for all 12 mutations | `go test -tags=integration ./internal/repository/ -run 'TestAuditWiringCrash(Before|After)Append'` — 13 entry points × 2 points: call errors, 0 `audit_ledger` rows, 0 `audit_publications` rows, domain state per class (absent for INSERT classes, prior state preserved for DELETE/UPDATE classes, incl. message-delete's `outbox_events` row) |
| Rollback verification: mutation and fact both absent | the same two tests' `verifyGone` + `assertNoAuditRows` — the fact is absent even in the **after**-append case (append completed, tx aborted) |
| Service unit-test rewrite from Auditor mocks to fact-level assertions | `go test ./internal/service/` — zero `LogFn` captures remain (grep `LogFn` in `internal/service` = 0); every audit assertion reads the `*domain.AuditFact` captured by a `*WithAuditFn` (action/actor/resource/`target_uid`/payload/fresh-EventID); new `admin_test.go`/`ban_test.go` cover the two services that had none; fail-closed `*.audit_unavailable` tests pass |
| New admin/ban repository integration tests for audit writes against real PostgreSQL | `go test -tags=integration ./internal/repository/ -run 'Test(Admin|Ban).*WithAudit'` — commit-side: 1 ledger row (exact fact fields) + 1 pending publication + domain row, per GrantRole/RevokeRole/Ban/Unban; the classes are also inside `migratedMutationClasses()` for the crash tables |

## 10. Explicit non-goals

- No changes to the already-migrated classes' coverage (profile/op/event/callback/provisioning/sync) beyond what exists — the matrix's BP/AP tables could later absorb them, but the current suite's three tests already prove the shared append code.
- No service-level integration tests (the repo-level matrix plus unit-level fact assertions are the agreed split; service integration is covered by `internal/app` wiring tests from the umbrella).
- No load/parallelism testing of the partition advisory lock (covered by the existing ledger suite; not a T-4 acceptance).
- The gate/wiring assertions (REQ-2/REQ-7.3) remain umbrella step 1/4 work, not this design.
