# Requirements — internal/block: move Block/Unblock audit writes into the business transaction (live service/repository pair, fail-closed wired)

> **Module:** `internal/block`
> **Date:** 2026-08-06
> **Direction (selected, value 9 / risk 9 / effort 6 / confidence 9):** "Move block audit writes into the business transaction: add BlockWithAudit/UnblockWithAudit via AppendInTx in the LIVE service/repository pair, fail-closed wired"
> **Source analysis:** `docs/auto/analyses/internal-block-c9ab184b.json`
> **Method:** every cited file/symbol re-verified against the working tree (line numbers below are from the current tree). The previous run of this direction (`docs/auto/runs/move-block-audit-into-the-business-transaction-o-5e9708f1/`) failed at the requirements stage for infrastructure reasons ("agent binary not found", `DECISIONS.md`), not content; no requirements artifact exists to supersede.

---

## 1. Problem statement (verified)

Block/Unblock audit facts are written **out-of-transaction** through the legacy fire-and-forget `AuditLogger`, after the repository mutation, with the error swallowed:

- `internal/block/block.go` (unwired parallel tree): `_ = s.audit.Log(...)` at :54 (Block) and :76 (Unblock), inside the cited ranges :52-60 and :74-82, both AFTER `s.blocks.Block/Unblock`.
- `internal/service/block.go` (live twin): `_ = s.audit.Log(...)` at :57 (Block) and :79 (Unblock), both AFTER `s.blocks.Block/Unblock` (lines 55-56 and 77-78 respectively).

Consequences, both real at shipped HEAD:

1. **Lost facts (B2 item 1):** a crash or audit failure between the repo commit and the `Log` call leaves a `user_blocks` row with no durable `audit_ledger` fact — exactly the "row without evidence" gap. The `Log` call is a separate round-trip (`auditLedgerService.Log` → `Record` → `repo.Append` opens its own transaction, `internal/repository/audit_ledger.go:90-103`), so the two writes are not atomic by construction.
2. **Swallowed errors (B2 item 2):** `_ =` discards every audit failure; Block/Unblock report success even when the compliance fact was never persisted. Additionally, because `sso.Client.ValidateIdentity` (`internal/sso/client.go:196-202`, `internal/sso/jwks.go:93-99`) can return an identity with an empty `TenantID`, and `validateAuditFact` (`internal/repository/audit_ledger.go:166-172`) requires a non-empty `TenantID`, the common-case fact is currently *rejected by validation* and silently dropped — the swallowed error hides a systematic audit gap, not just a rare crash window.

The in-transaction pattern this direction must reuse is already proven in the live repository layer (see §3, evidence rows E1-E8): `AuditLedgerTransactionalRepository.AppendInTx` inserts the ledger fact **and** the `audit_publications` outbox row in the same tx, with fail-closed wiring via `FullAuditLedgerRepository`/`requireAuditLedger`.

**Placement constraint (verified):** the work lands in `internal/repository/block.go` + `internal/service/block.go` + `internal/app/new.go`. `internal/block/` is an unwired parallel tree: its only importer in the whole repository is `internal/notification/dispatcher.go:8` (for the `block.BlockChecker` interface), and `internal/notification` itself is imported by nothing outside its own package (the live outbox relay uses `service.NewNotificationDispatcher`, `internal/app/outbox.go:36`). Implementing `WithAudit` inside `internal/block/` would be dead code (AGENTS.md constraint).

---

## 2. Scope

**In scope**

- `internal/repository/block.go`: in-transaction `BlockWithAudit`/`UnblockWithAudit` on the live `blockRepo` + `BlockRepository` interface, wired fail-closed with the transactional audit ledger.
- `internal/service/block.go`: Block/Unblock call the `WithAudit` repository methods with a durable `*domain.AuditFact`; the swallowed `_ = s.audit.Log(...)` calls and the now-obsolete `WithBlockAuditLogger` option are removed.
- `internal/app/new.go`: composition-root wiring (`NewBlockRepository(gdb, auditLedgerRepo)`; `NewBlockService(blockRepo)`).
- `internal/service/mocks/mocks.go`: `BlockRepo` test double extended to satisfy the widened interface.
- Tests: `internal/repository/block_integration_test.go` (testcontainers), `internal/repository/audit_wiring_test.go` (fail-closed constructors), `internal/service/block_test.go` (error surfacing + fact shape).

**Out of scope (deliberately, per the direction)**

- Any change to `internal/block/` (dead tree; its `BlockChecker` consumption is unaffected).
- Anchor-derived/deterministic `event_id` for block facts (separate B2-4 direction; facts in this direction carry no `EventID`, matching the current legacy event shape).
- The `make check-audit-swallow` gate (separate direction; this change makes `internal/service/block.go` compliant with it).
- Deletion of the inline publisher loop in `auditLedgerService.Record` (separate direction).
- No migrations, no proto changes (block is HTTP-only: `internal/handler/http/block.go`), no handler changes.

---

## 3. Evidence verification

| # | Direction's claim | Verification result |
|---|---|---|
| E1 | `internal/block/block.go:52-60,74-82` — `_ = s.audit.Log` after the repo mutation | ✅ Confirmed at :54 (Block) and :76 (Unblock); both sit after `s.blocks.Block/Unblock` and swallow the error. |
| E2 | `internal/service/block.go:57,79` — live twin swallows | ✅ Confirmed exactly at :57 (Block) and :79 (Unblock). |
| E3 | `AuditLedgerTransactionalRepository.AppendInTx` at `internal/repository/audit_ledger.go:39,123` | ✅ Confirmed: interface at :38-41, `AppendInTx` at :123. |
| E4 | `createAuditRows` inserts ledger fact AND `audit_publications` pending row in the same tx (`audit_ledger.go:195-203`) | ✅ Confirmed: `tx.Create(auditLedgerFactToRow(...))` then `tx.Create(&auditPublicationRow{..., Status: "pending", ...})` (:199-203). |
| E5 | Fail-closed wiring `FullAuditLedgerRepository`/`requireAuditLedger` at `internal/repository/audit_wiring.go:13-28` | ✅ Confirmed: interface :13-19; `requireAuditLedger` :21-28 panics on nil and typed-nil. |
| E6 | Reference impl `UpdateProfileWithAudit` at `internal/repository/account_profile.go:67-101` | ✅ Confirmed: nil-guard wrapper :67-71; `updateProfile` :73-101 wraps the mutation and `r.audit.AppendInTx(ctx, tx, fact)` (:101) in one `db.Transaction`. |
| E7 | Reference impl `CreateWithAudit` at `internal/repository/operation.go:135-178` | ✅ Confirmed: wrapper :135-139; in-tx `create` :141-186 with `r.audit.AppendInTx` at :182 and the documented `ErrAuditDuplicate` swallow (`!errors.Is(err, ErrAuditDuplicate)`). Same in-tx swallow pattern exists in `callback_audit.go:49,73,101`, `event_receipt.go:180`, `sync_job_audit.go:55`. |
| E8 | Wired at `internal/app/new.go:218-219` | ⚠️ **Line numbers corrected:** the block wiring is at `new.go:205-206` (`blockSvc := service.NewBlockService(blockRepo, service.WithBlockAuditLogger(auditLedger))`); `NewBlockRepository(gdb)` is at `:144`; :218-219 is the operation service. Symbols as cited. `auditLedgerRepo` (type `repository.FullAuditLedgerRepository`, from `buildAuditStack`, `internal/app/audit.go:34-37`) is in scope at :134. |
| E9 | `internal/notification/dispatcher.go:8` is the only consumer of `internal/block` | ✅ Confirmed by repository-wide import scan: only match is `dispatcher.go:8` (for `block.BlockChecker`). Additionally, `internal/notification` itself has no importers outside its package, so even that interface use is in an unwired dispatcher. |
| E10 | Acceptance-test styles exist: `block_integration_test.go` (testcontainers) and `audit_wiring_test.go` (panic-on-nil) | ✅ Confirmed: `internal/repository/block_integration_test.go` uses `testpg.Start`/`testpg.Migrate` (`internal/repository/testpg/testpg.go:21,53`, postgres:16-alpine via testcontainers); `audit_wiring_test.go` has `mustPanic`, `TestConstructorsRejectNilAudit`, `TestConstructorsRejectTypedNilAudit` with entries for NewAccountRepository/NewOperationRepository/NewSyncJobRepository/NewEventReceiptRepository. |

Additional verified facts that constrain the design (not in the direction, found during verification):

- **F1 — Tenant dependency:** `validateAuditFact` requires non-empty `Source, SourceRegion, PartitionKey, TenantID, Action, OccurredAt` (`audit_ledger.go:166-172`). The live SSO identity may carry an empty `TenantID` (`sso/client.go:196-202`, `jwks.go:93-99`; `TenantFrom` at `internal/service/service.go:78-83`). Today that means the fact is rejected and the error is swallowed (block still commits). After this change the append failure propagates and the whole tx rolls back: **a block never commits without its audit fact** (§6.1).
- **F2 — Fact construction helper:** `buildAuditFact(ctx, AuditFactInput{...})` (`internal/service/audit_ledger.go:82-96`) + `bindAuditContext`/`normalizeAuditInput` (:97-142) already bind tenant/actor/trace from ctx and default `Source="aero-id"`, `SourceRegion="local"`, partition = tenant-prefixed account/actor UID (or `"system"`), `OccurredAt=now`. Used by the live account/callback/operation services (`account_profile.go:48`, `callback.go:224,261`, `operation_audit.go:19-26`). The block service reuses it.
- **F3 — Error-code convention:** in-tx append failures surface through the domain write code (`operation.create_failed` wraps `createWithAudit`'s error, `operation.go:132-135`); distinct `*.audit_failed` codes are only used for non-atomic out-of-tx Record paths. Block keeps `block.block_failed` / `block.unblock_failed`.
- **F4 — Interface implementers:** `repository.BlockRepository` is implemented only by `blockRepo` (`internal/repository/block.go`) and `mocks.BlockRepo` (`internal/service/mocks/mocks.go:480-517`). `service.BlockChecker` (used by `WithBlockChecker`, `new.go:197`) requires only `IsBlocked` — widening `BlockRepository` does not disturb that consumer.

---

## 4. Requirements

### REQ-1 — Repository: in-transaction `BlockWithAudit` / `UnblockWithAudit` (live tree)

File: `internal/repository/block.go`.

1. `blockRepo` gains an `audit AuditLedgerTransactionalRepository` field.
2. `NewBlockRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) BlockRepository` — constructor passes the audit through `requireAuditLedger("NewBlockRepository", audit)` (fail-closed at construction; nil or typed-nil panics, per `internal/repository/audit_wiring.go:21-28`).
3. `BlockRepository` interface gains:
   - `BlockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error`
   - `UnblockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error`
4. `BlockWithAudit` runs one `db.Transaction` containing: (a) the current `user_blocks` INSERT with `ON CONFLICT DO NOTHING` (idempotency preserved); (b) if `fact != nil`, `r.audit.AppendInTx(ctx, tx, fact)` with the established in-tx error handling:
   ```go
   if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
       return fmt.Errorf("append block audit: %w", err)
   }
   ```
   `ErrAuditDuplicate` is swallowed in-tx exactly as in `operation.go:182`, `callback_audit.go:49`, `event_receipt.go:180`, `sync_job_audit.go:55`: a duplicate append implies the retried operation already committed the identical fact; the swallow runs after the domain write inside the same tx and can never mask a failed domain write. (Inert while `EventID` is unset — the current scope's fact shape.)
5. `UnblockWithAudit` mirrors 4 with the current DELETE (no-op on missing pair) and wraps errors as `delete user block with audit`.
6. Plain `Block`/`Unblock` remain unchanged (single-statement, no audit) and stay on the interface; they serve the guard path, tests, and future non-audited callers.

*Rationale:* the service will exclusively call the `WithAudit` variants, so widening the single interface (rather than a separate `BlockAuditRepository` type-assertion surface) makes the fail-closed property **compile-time**: there is no code path that can construct a block service without the audit-capable repository. (The separate-interface convention exists in this codebase for services with an *optional* audit gate — `OperationAuditRepository`, `AccountProfileAuditRepository` — which block no longer has.)

### REQ-2 — Service: Block/Unblock write the fact in the business transaction

File: `internal/service/block.go`.

1. Delete both `_ = s.audit.Log(...)` blocks (:57-63 Block, :79-85 Unblock).
2. Delete the `audit AuditLogger` field, the `WithBlockAuditLogger` option, and the `Option`/`BlockOption` plumbing. `NewBlockService(blocks repository.BlockRepository) BlockService` (no options remain).
3. `Block`: after the existing validation (`mustActor`, non-empty `uid`, `uid != actor`), call
   `s.blocks.BlockWithAudit(ctx, actor, uid, blockAuditFact(ctx, actor, uid, domain.AuditUserBlock))` and translate any error to `apperr.Internal("block.block_failed", "failed to block user").Wrap(err)`.
4. `Unblock`: same with `domain.AuditUserUnblock` and `block.unblock_failed`.
5. Private helper:
   ```go
   func blockAuditFact(ctx context.Context, actor, uid, action string) *domain.AuditFact {
       return buildAuditFact(ctx, AuditFactInput{Source: "aero-id", Action: action,
           ResourceType: "user", ResourceID: uid, ActorUID: actor})
   }
   ```
   Tenant/trace bind from ctx via `bindAuditContext`; `OccurredAt` defaults to now; `EventID` stays unset (dedup anchoring is a separate direction). The fact's action constants are `domain.AuditUserBlock = "user.block"` / `domain.AuditUserUnblock = "user.unblock"` (`internal/domain/audit.go:21-22`); the legacy `SubjectType/SubjectID/TargetUID` fields have no equivalent in the ledger fact — `ResourceType: "user"`, `ResourceID: uid` carry the same meaning (this is what `auditLedgerService.Log` already mapped, `internal/service/audit_ledger.go:136-143`).
6. After this change `internal/service/block.go` contains no `_ =` audit call (grep-able invariant; also required by the sibling `make check-audit-swallow` gate when it lands).

### REQ-3 — Composition root wiring

File: `internal/app/new.go`.

1. `:144` → `blockRepo := repository.NewBlockRepository(gdb, auditLedgerRepo)` (`auditLedgerRepo` is `repository.FullAuditLedgerRepository` from `buildAuditStack` at `:134`, which satisfies `AuditLedgerTransactionalRepository`).
2. `:205-206` → `blockSvc := service.NewBlockService(blockRepo)` (drop `service.WithBlockAuditLogger(auditLedger)`).
3. No other wiring change: `blockRepo` continues to satisfy `service.BlockChecker` for `WithBlockChecker` (`new.go:197`) and `repository.BlockRepository` for `outboxDependencies.blockRepo` (`internal/app/outbox.go:23,37`).

### REQ-4 — Test double

File: `internal/service/mocks/mocks.go` (`BlockRepo`, :480-517).

1. Add `BlockWithAuditFn func(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error` and `UnblockWithAuditFn ...`.
2. Add methods `BlockWithAudit`/`UnblockWithAudit` whose nil-default returns `nil` (mirroring the existing `Block`/`Unblock` defaults) so unrelated message/notification tests using `mocks.BlockRepo` keep passing.
3. Compile-time requirement: the mock must satisfy the widened `repository.BlockRepository` (the package tests fail to compile otherwise — this is the guard that keeps the double honest).

### REQ-5 — Integration test: atomic commit and rollback (AC-1, T-5)

File: `internal/repository/block_integration_test.go` (build tag `integration`, testcontainers via `testpg.Start` + `testpg.Migrate`, ledger constructed like `startAuditLedger` in `audit_ledger_integration_test.go:19-25` with a test signing key, e.g. the existing `auditLedgerTestKey`).

Test 1 — `TestBlockRepo_BlockWithAuditCommitsAtomically`:

1. Setup: `db := testpg.Start(t); testpg.Migrate(...); ledger := NewAuditLedgerRepository(db, AuditLedgerConfig{SigningKey: ...}); blocks := NewBlockRepository(db, ledger)`.
2. `BlockWithAudit(ctx, "a", "b", fact)` with a fully populated fact: `Source: "aero-id", SourceRegion: "local", PartitionKey: "tenant-a:a", TenantID: "tenant-a", ActorUID: "a", Action: "user.block", ResourceType: "user", ResourceID: "b", OccurredAt: now`.
3. Assert all three committed rows: (a) exactly one `user_blocks` row `(a, b)`; (b) one `audit_ledger` row with `action='user.block'`, `actor_uid='a'`, `resource_id='b'`, `partition_key='tenant-a:a'`, `chain_sequence=1`, non-empty `signature`; (c) one `audit_publications` row with `status='pending'`, `attempt=0`, `fact_id` = the ledger row's id.
4. `VerifyChain(ctx, "aero-id", "tenant-a:a")` returns nil (in-tx append produced a signed, linked chain).
5. `UnblockWithAudit(ctx, "a", "b", fact2)` with `Action: "user.unblock"`: `user_blocks` row gone; ledger has a second row `chain_sequence=2`; a second pending publication; `VerifyChain` still nil.
6. Repeat `BlockWithAudit(ctx, "a", "b", ...)` on the same pair (idempotent `ON CONFLICT DO NOTHING`): still exactly one `user_blocks` row (the re-block path must not duplicate the domain row; fact append behavior on the repeated call is covered by the duplicate-swallow contract, inert with unset `EventID`).

Test 2 — `TestBlockRepo_BlockWithAuditRollsBackOnInjectedFailure` (the direction's "injected tx failure after insert leaves zero orphan facts/publications"):

1. *Failure before/at append — invalid fact:* `BlockWithAudit(ctx, "a", "b", &domain.AuditFact{})` fails `validateAuditFact` **after the `user_blocks` INSERT has executed inside the tx** → error returned; assert **zero** `user_blocks` rows, **zero** `audit_ledger` rows for the partition, **zero** `audit_publications` rows.
2. *Failure inside `createAuditRows` — ID collision:* pre-append a ledger row with an explicit ID (e.g. `fact.ID = idgen.NewID()` set before `ledger.Append`), then `BlockWithAudit` with a fact carrying the same ID → the ledger INSERT raises its PK constraint inside the tx → error returned; assert **zero** new `user_blocks` rows and the pre-existing ledger/publication counts unchanged (no orphan fact, no orphan publication, no block row without a fact).
3. Same two injections for `UnblockWithAudit` (seeded pair): error returned, pair still blocked, counts unchanged.

*Testability note:* both injections are deterministic and need no fault-injection framework; they exercise the rollback of the domain write together with the ledger+publication writes, which is precisely the property the current out-of-tx code lacks.

### REQ-6 — Wiring test: fail-closed constructors (AC-2, T-5)

File: `internal/repository/audit_wiring_test.go` (mirror the existing entries):

1. Add to `TestConstructorsRejectNilAudit`: `mustPanic(t, "NewBlockRepository", func() { repository.NewBlockRepository(db, nil) })`.
2. Add to `TestConstructorsRejectTypedNilAudit`: `var ledger repository.AuditLedgerTransactionalRepository = (*auditLedgerStub)(nil); mustPanic(t, "NewBlockRepository (typed nil)", func() { repository.NewBlockRepository(db, ledger) })`.
3. The existing `db := &gorm.DB{}` and `mustPanic` helper are reused unchanged.

### REQ-7 — Service unit tests: error surfacing and fact shape (AC-3, T-4)

File: `internal/service/block_test.go`.

1. Rewrite `TestBlockService_BlockEmitsAuditEvent` → `TestBlockService_BlockWritesAuditFactInTx`: `repo := &mocks.BlockRepo{BlockWithAuditFn: func(_ context.Context, blocker, blocked string, fact *domain.AuditFact) error { capture; return nil }}`; `svc := service.NewBlockService(repo)`; `ctx := service.WithActor(context.Background(), "a")`; after `svc.Block(ctx, "b")` assert: called exactly once with `blocker="a"`, `blocked="b"`, and `fact.Action == domain.AuditUserBlock`, `fact.ActorUID == "a"`, `fact.ResourceType == "user"`, `fact.ResourceID == "b"`, `fact.Source == "aero-id"`.
2. Same for `TestBlockService_UnblockWritesAuditFactInTx` with `UnblockWithAuditFn` and `domain.AuditUserUnblock`.
3. New `TestBlockService_BlockAuditAppendErrorSurfaces`: `BlockWithAuditFn` returns `errors.New("append failed")` → `svc.Block(ctx, "b")` returns a non-nil error; `assertAppCode(t, err, 500)` and the code is `block.block_failed`. The error is **never swallowed**.
4. New `TestBlockService_UnblockAuditAppendErrorSurfaces`: same with `UnblockWithAuditFn` → code `block.unblock_failed`.
5. Existing validation tests (`TestBlockService_BlockSelfBadRequest`, `TestBlockService_BlockBlankUIDBadRequest`, `TestBlockService_BlockRequiresActor`, happy paths via `BlockFn`/`UnblockFn`, `TestBlockService_ListBlockedHappyPath`) remain valid — the mock's `BlockWithAudit`/`UnblockWithAudit` defaults return nil — but `BlockFn`-based tests are not required to change; the audit-emission tests (:74-126) are the ones rewritten because `WithBlockAuditLogger` is removed.
6. Grep-able invariant: `grep -n "_ =" internal/service/block.go` returns nothing audit-related.

---

## 5. Acceptance checks (preserved from the direction, made testable)

| ID | Direction's check | Testable form (above) |
|---|---|---|
| AC-1 / T-5 | Integration test (testcontainers, `internal/repository/block_integration_test.go` style): Block commits `user_blocks` + `audit_ledger` fact + `audit_publications` pending row atomically; injected tx failure after insert leaves zero orphan facts/publications | REQ-5, Test 1 (three-row atomic commit + chain verification + unblock) and Test 2 (two deterministic post-insert failure injections, both directions) |
| AC-2 / T-5 | Wiring test mirrors `audit_wiring_test.go`: nil/typed-nil ledger panics at `NewRepository` time (fail-closed) | REQ-6 (entries added to both existing constructor tests) |
| AC-3 / T-4 | Unit test: audit append failure surfaces as AppError from Block/Unblock, never `_ =`-swallowed | REQ-7 items 1-4 (fact shape asserted in-tx; both failure paths assert `500` + `block.block_failed`/`block.unblock_failed`) |

**Done gate:** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make build`, plus `make test-integration` (this change modifies repository SQL transaction boundaries). No new files exceed the 500-line limit (`block.go` stays well under; `block_integration_test.go` and `block_test.go` additions are small).

---

## 6. Documented behavioral consequences and decisions

### 6.1 Tenant-less identities now fail closed (accepted consequence)

Today, an identity with empty `TenantID` (possible via `sso.Client.ValidateIdentity`, F1) commits the block and silently drops the fact at `validateAuditFact`. After this change, `AppendInTx` rejects the incomplete fact, the tx rolls back, and Block/Unblock return `500 block.block_failed`/`block.unblock_failed`. This is the intended fail-closed contract — a block row must never exist without its durable audit fact — and it matches the account/operation/callback services, which already require a verified tenant (`mustTenant`, `internal/service/service.go:86-91`). Implementation must confirm whether deployed Snaplink tokens always carry `tenant_id`; if product policy requires tenant-less blocking to succeed, that is a separate decision outside this direction (it would require a tenant-resolution policy, not a swallow).

### 6.2 Error surface change (accepted consequence)

`POST /blocks` and `DELETE /blocks/:uid` can now return `500` on ledger unavailability where they previously always succeeded (204). This is precisely the direction's intent ("never `_ =`-swallowed") and is covered by REQ-7.

### 6.3 Rejected alternatives

- **Implement `WithAudit` in `internal/block/`** — rejected: dead code (E9); violates AGENTS.md.
- **Keep `WithBlockAuditLogger` as an optional gate** — rejected: it is a fail-open path (nil audit ⇒ block without audit) and contradicts "fail-closed wired"; with the repository fail-closed at construction, the option has no remaining legitimate value.
- **Separate `BlockAuditRepository` interface + service type-assertion** — rejected: the convention exists to support services with optional audit gates; block has none, so the compile-time guarantee of REQ-1 is strictly stronger.
- **Introduce a new error code (e.g. `block.audit_failed`)** — rejected: F3 — the codebase reserves distinct audit codes for out-of-tx Record paths; in-tx append failures surface through the domain write code.

---

## 7. Cross-direction dependencies

- This change is a prerequisite for the sibling `make check-audit-swallow` gate direction to pass on `internal/service/block.go` (the file will contain zero `_ =` audit calls after REQ-2).
- It does not depend on the deterministic `event_id` direction (B2-4); facts here carry no `EventID`, and the `ErrAuditDuplicate` in-tx swallow (REQ-1.4) follows the existing contract so a future anchored-ID retry behaves identically to operation/callback/sync-job/event-receipt.
