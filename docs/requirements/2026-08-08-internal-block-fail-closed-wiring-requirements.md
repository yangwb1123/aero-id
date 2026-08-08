# Requirements — internal/block: finish fail-closed Block/Unblock audit wiring (remove the WithBlockAuditLogger gate)

> **Module:** `internal/block` (live `internal/service/block.go` + `internal/repository/block*.go`)
> **Date:** 2026-08-08
> **Direction (selected):** "Move live Block/Unblock audit writes into the business transaction: BlockWithAudit/UnblockWithAudit via AppendInTx (B2-1, B2-2)" — value 10 / risk 9 / effort 6 / confidence 9.
> **Source analysis:** `docs/auto/analyses/internal-block-c9ab184b.json`
> **Supersedes for this module:** `docs/requirements/2026-08-06-internal-block-in-tx-audit-requirements.md` (REQ-1..REQ-7). That spec described the pre-B2-1 tree (`_ = s.audit.Log(...)` after the repo mutation). The repo half of it has since shipped; this spec re-verifies every citation against the current tree, pins the residual work, and preserves the direction's acceptance checks in testable form.

---

## 1. Current state vs. the direction's problem statement (verified delta)

The direction's problem statement describes the tree as it was when the source analysis ran: `internal/service/block.go` Block/Unblock committing `user_blocks` via `s.blocks.Block/Unblock` and then appending the audit fact out-of-transaction via `_ = s.audit.Log(...)` (`auditLedgerService.Log` → `Record` → `repo.Append` opening its own transaction, `internal/repository/audit_ledger.go:113-127`; Log adapter at `internal/service/audit_ledger.go:145-150`). **That out-of-tx shape is gone from the current tree.** What shipped since:

| Piece | Current tree | Status |
|---|---|---|
| In-tx repo methods | `internal/repository/block_audit.go:25,39` — `BlockWithAudit`/`UnblockWithAudit` wrap the `user_blocks` INSERT (`ON CONFLICT DO NOTHING`) / DELETE and `r.audit.AppendInTx(ctx, tx, fact)` in one `db.Transaction`; `ErrAuditDuplicate` swallowed in-tx (`!errors.Is(err, ErrAuditDuplicate)`), matching `operation.go:182` | ✅ shipped |
| Fail-closed repo constructor | `internal/repository/block.go:45` — `NewBlockRepository(db, audit)` passes through `requireAuditLedger("NewBlockRepository", audit)` (`internal/repository/audit_wiring.go:21-28`; nil and typed-nil panic) | ✅ shipped |
| Test double | `internal/service/mocks/mocks.go:533-534,567-579` — `BlockRepo` has `BlockWithAuditFn`/`UnblockWithAuditFn` and methods | ✅ shipped |
| Matrix integration coverage | `internal/repository/audit_wiring_matrix_integration_test.go:25-41,88-96` — block/unblock commit side 1 ledger + 1 pending publication row; crash side (publication INSERT blocked) rolls both back | ✅ shipped |
| Service fail-open gate | `internal/service/block.go:20` (`audit AuditLogger` field), `:24` (`BlockOption`), `:27` (`WithBlockAuditLogger`), `:31` (`NewBlockService(blocks, opts...)`); `:50-59` (Block) / `:75-84` (Unblock) — `if s.audit != nil { repo, ok := s.blocks.(repository.BlockAuditRepository); ... } else { s.blocks.Block/Unblock }` (`:61` / `:86`); `:53,78` — `block.audit_unavailable` | ❌ **residual** |
| Wiring | `internal/app/new.go:197-198` — `service.NewBlockService(blockRepo, service.WithBlockAuditLogger(auditLedger))` | ❌ **residual** |
| Fail-closed wiring tests | `internal/repository/audit_wiring_test.go:29-66` — enumerates every live ledger-taking constructor **except `NewBlockRepository`** | ❌ **residual** |
| Repo integration tests for the WithAudit contract (chain_sequence, signature, VerifyChain, post-INSERT rollback) | `internal/repository/block_integration_test.go` covers only plain `Block`/`Unblock`/`IsBlocked`/`ListBlocked` (`:15-20` setup uses `AuditLedgerConfig{}` — no signing key) | ❌ **residual** |
| Service unit tests independent of the gate | `internal/service/block_test.go:69-153` construct the service with `service.WithBlockAuditLogger(&mocks.Auditor{})`; `:34-67` happy paths exercise the plain `BlockFn`/`UnblockFn` path that this change deletes; failure tests (`:133-154`) assert only HTTP 500, not the code string | ❌ **residual** |

The remaining gap is exactly the direction's B2-1/B2-2 intent: **the fail-open `WithBlockAuditLogger` gate.** Today, `NewBlockService(blocks)` with no option still constructs successfully and silently blocks-without-audit (the else-branch at `block.go:61-63` / `:87-89`). The `block.audit_unavailable` path (`:53,:78`) is the same fail-open shape in disguise: it is only reachable when a repo does not implement the audit surface, and it reports a runtime error instead of making the absence unrepresentable. The direction's stated acceptance includes "removing the fail-open WithBlockAuditLogger gate"; this spec makes that the core requirement.

---

## 2. Evidence verification (every direction citation re-checked on 2026-08-08)

| # | Direction's claim | Verification result (current tree) |
|---|---|---|
| E1 | `internal/service/block.go` Block/Unblock: `_ = s.audit.Log(...)` after the repo mutation | ⚠️ **Stale — superseded.** No `_ =` anywhere in `internal/service/block.go` (grep verified, 0 matches). The service now has the gated `BlockAuditRepository` type-assertion path (`:50-59`, `:75-84`). The direction's *goal* (in-tx, never-swallowed audit) is half-achieved; the gate is the residual (see §1). |
| E2 | `auditLedgerService.Log` → `Record` → `repo.Append` opens its own transaction (`internal/repository/audit_ledger.go:90-103` as cited) | ✅ Confirmed at current lines `audit_ledger.go:113-127` (`Append` opens its own `db.Transaction`; `:129-139` `AppendInTx` requires a caller-supplied tx). The out-of-tx surface still exists for legacy `Log`/`Record` callers but is no longer used by the block path. |
| E3 | `AppendInTx` (`audit_ledger.go:123` as cited) | ✅ Confirmed at `:129`. Signature: `AppendInTx(ctx, tx *gorm.DB, fact *domain.AuditFact) error`; rejects nil tx; validates the fact; serializes the payload; appends via `appendInTx` (`:141-193`). |
| E4 | `createAuditRows` inserts ledger fact AND `audit_publications` pending row in the same tx (`:195-203` as cited) | ✅ Confirmed at `:195-232`: `tx.Create(auditLedgerFactToRow(fact, payload))` then `tx.Create(&auditPublicationRow{ID: idgen.NewID(), FactID: fact.ID, Status: "pending", NextAttemptAt: now, ...})` (`:226-231`). `Attempt` is omitted → column default 0. PK collision on the ledger INSERT stays raw so the enclosing tx rolls back (`:214-222` comment); only the `uq_audit_ledger_tenant_event` 23505 maps to `ErrAuditDuplicate`. |
| E5 | Fail-closed `requireAuditLedger` (`internal/repository/audit_wiring.go:13-28` as cited) | ✅ Confirmed: `FullAuditLedgerRepository` at `:13-19`; `requireAuditLedger` at `:21-28` panics on nil and on typed-nil for every nil-able reflect kind (Chan/Func/Interface/Map/Ptr/Slice). |
| E6 | Reference impl `UpdateProfileWithAudit` (`account_profile.go:67-101` as cited) | ✅ Confirmed at `:67`; in-tx pattern identical to `block_audit.go`. |
| E7 | Reference impl `CreateWithAudit` (`operation.go:135-178` as cited) | ✅ Confirmed at `:135`; in-tx `AppendInTx` at `:182` with the documented `ErrAuditDuplicate` swallow (`!errors.Is(err, ErrAuditDuplicate)`). |
| E8 | Wiring at `internal/app/new.go:144,205-206` as cited | ⚠️ **Line numbers moved.** Current: `:146` `blockRepo := repository.NewBlockRepository(gdb, auditLedgerRepo)` (fail-closed constructor already wired; `auditLedgerRepo` comes from `buildAuditStack` at `:131`); `:197-198` `blockSvc := service.NewBlockService(blockRepo, service.WithBlockAuditLogger(auditLedger))` — **the option is still passed and must be removed**. |
| E9 | Tenant-less SSO identities (`internal/sso/client.go:196-202`, `jwks.go:93-99`) rejected by `validateAuditFact` | ✅ Confirmed: `Client.ValidateIdentity` (`client.go:196-202`) returns `&domain.TokenIdentity{UID: uid, Subject: uid}` — no `TenantID`; `FallbackValidator.ValidateIdentity` (`jwks.go:93-99`) likewise. `validateAuditFact` (`audit_ledger.go:365-369`) requires non-empty `Source, SourceRegion, PartitionKey, TenantID, Action, OccurredAt`. Consequence (§6.1) unchanged by the gate removal — the fact was already built from ctx (`buildAuditFact` at `service/audit_ledger.go:92` → `bindAuditContext` at `:107`; `TenantFrom` at `service/service.go:78-82`) and already rejected inside `AppendInTx` on the WithAudit path. |
| E10 | `internal/block/` is an unwired parallel tree; only importer is `internal/notification/dispatcher.go:8` (for `block.BlockChecker`) | ✅ Confirmed by repository-wide import scan: single match `internal/notification/dispatcher.go:8`. Out of scope (§4). |
| E11 | Fact constants `domain.AuditUserBlock`/`AuditUserUnblock` | ✅ Confirmed at `internal/domain/audit.go:31-32` (`"user.block"` / `"user.unblock"`). |
| E12 | Fact construction helper | ✅ Confirmed: `buildLegacyAuditFact` (`internal/service/audit_fact.go:27-50`) — already used by the block service (`block.go:55-56,:80-81`) and the other B2-1 conversions (admin/ban/file-share/message/comment). Sets `Source: "aero-id"`, `ResourceType`/`ResourceID` from subject args, `Payload["target_uid"]`, and mints a fresh v7 `EventID` per call (allowlisted in `scripts/check-anchored-event-ids.sh`; the C2 rationale comment is in `audit_fact.go:14-25`). The block service keeps using this helper unchanged; no new EventID policy work (B2-4 is out of scope). |
| E13 | `internal/repository/block.go` — plain `Block`/`Unblock` with `ON CONFLICT DO NOTHING` | ✅ Confirmed: `Block` at `:49-57` (idempotent via `clause.OnConflict{DoNothing: true}`), `Unblock` at `:59-66` (no-op delete). `BlockRepository` interface at `:27-43` does **not** include the WithAudit methods. |
| E14 | `mocks.BlockRepo` (`internal/service/mocks/mocks.go:480-517` as cited) | ✅ Confirmed at current `:533-579` (fields `:533-534`, methods `:567-579`, nil-defaults return nil). Already satisfies both `BlockRepository` and `BlockAuditRepository`. |
| E15 | Existing integration/wiring test styles | ✅ Confirmed: `internal/repository/block_integration_test.go` (build tag `integration`, `testpg.Start`/`testpg.Migrate`); `audit_wiring_test.go` (`mustPanic` at `:15-24`, nil test `:29-48`, typed-nil test `:50-66` with `auditLedgerStub`, nil-map-kind test `:72-77`); `audit_wiring_matrix_integration_test.go` crash injection via `installFailPublicationInsertTrigger` (`audit_wiring_integration_test.go:271`, used at `:146,:190`). |

Additional verified facts that constrain the design:

- **F1 — `NewBlockRepository` is missing from the fail-closed wiring tests.** `audit_wiring_test.go` enumerates "every live ledger-taking constructor in the composition root" (`:29-48`, `:50-66`) but has no `NewBlockRepository` entry even though the constructor is fail-closed. The direction's T-5 acceptance requires these entries; this is a pure test gap.
- **F2 — `setupBlockRepo` uses a keyless ledger.** `block_integration_test.go:15-20` builds `NewAuditLedgerRepository(db, AuditLedgerConfig{})` (`:19`). WithAudit tests need a signing key (`AppendInTx` fails with "audit ledger signing key is not configured" otherwise, `audit_ledger.go:130-132`); the matrix test's 33-char key (`audit_wiring_matrix_integration_test.go:30`) is the in-repo precedent.
- **F3 — `service.BlockService` interface and handler are untouched by this change.** `service.go:288-294` (Block/Unblock/ListBlocked) and `internal/handler/http/block.go` (`NewBlockHandler(blocks service.BlockService)`) need no edits; block is HTTP-only.
- **F4 — `WithBlockAuditLogger` has exactly one non-test caller** (`internal/app/new.go:198`; the `.claude/worktrees/` copies are historical). `block.audit_unavailable` appears only in `internal/service/block.go:53,78`.
- **F5 — baseline gates are green today**: `go test ./internal/service/... -run Block` and `./internal/repository/` pass; `make check-audit-swallow` passes (0 matches under `internal/service internal/repository internal/app internal/storage cmd`; Makefile `:67-69`). The `grep -n '_ =' internal/service/block.go` invariant already holds (0 matches) and must keep holding.

---

## 3. Problem statement (residual, verified)

1. **Fail-open service gate (B2-2 residual):** `NewBlockService(blocks)` without `WithBlockAuditLogger` constructs successfully and `Block`/`Unblock` fall through to plain `s.blocks.Block/Unblock` (`internal/service/block.go:61-63,:87-89`) — a `user_blocks` row with no durable `audit_ledger` fact and no error. The gate is wired on in production today (`new.go:198`), but nothing enforces it: a future wiring edit drops audit silently. This contradicts the fail-closed position every other live mutation repo took (`requireAuditLedger`, `audit_wiring.go:21-28`).
2. **Runtime-assertion escape hatch:** the WithAudit path itself requires `s.blocks.(repository.BlockAuditRepository)` (`block.go:51,:76`); the `block.audit_unavailable` failure mode (`:53,:78`) proves a non-audit `BlockRepository` is a representable state. After this change it must be unrepresentable at compile time (REQ-2).
3. **Missing acceptance coverage:** (a) `audit_wiring_test.go` omits `NewBlockRepository` — the fail-closed constructor has no wiring test; (b) `block_integration_test.go` has no atomic-commit/rollback tests for the WithAudit contract (the matrix test covers commit/crash for ledger+publication counts but not `chain_sequence`, `signature`, `VerifyChain`, or post-INSERT injections); (c) `block_test.go` failure tests assert only HTTP 500 and never the `block.block_failed`/`block.unblock_failed` codes, and the fact-shape tests don't assert `Source == "aero-id"`.

---

## 4. Scope

**In scope**

- `internal/repository/block.go` + `internal/repository/block_audit.go`: fold the WithAudit surface into `BlockRepository` (REQ-1).
- `internal/service/block.go`: remove the `audit` field, `BlockOption`, `WithBlockAuditLogger`, both gate branches, the plain-path fallbacks, and `block.audit_unavailable`; call the WithAudit methods unconditionally (REQ-2).
- `internal/app/new.go`: drop `service.WithBlockAuditLogger(auditLedger)` from the `NewBlockService` call (REQ-3).
- Tests: `internal/repository/block_integration_test.go` (REQ-5), `internal/repository/audit_wiring_test.go` (REQ-6), `internal/service/block_test.go` (REQ-7), and the mechanical type-assertion fix in `internal/repository/audit_wiring_matrix_integration_test.go` (REQ-1).
- `internal/service/mocks/mocks.go`: **no change needed** (verify-only, REQ-4).

**Out of scope (deliberately, per the direction)**

- Any change to `internal/block/` (unwired dead tree; `internal/notification/dispatcher.go:8` consumes only its `BlockChecker`).
- The other five B2-1 conversions' service gates (`ban.go`, `admin.go`, `file_share.go`, `message.go`, `comment.go` still use `WithXxxAuditLogger` — separate directions; this spec touches only block).
- Deterministic/anchored `event_id` for block facts (separate B2-4 direction; `buildLegacyAuditFact` keeps its fresh-v7 policy — E12).
- Handler, proto, migration, and outbox changes (none needed; block is HTTP-only, F3).
- The `make check-audit-swallow` gate itself (already green, F5; this change keeps it green).

---

## 5. Requirements

### REQ-1 — Repository: WithAudit surface moves onto `BlockRepository` (compile-time fail-closed)

Files: `internal/repository/block.go`, `internal/repository/block_audit.go`.

1. Add to the `BlockRepository` interface (`block.go:27-43`):
   - `BlockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error`
   - `UnblockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error`
2. Move the implementations from `block_audit.go:25-49` into `block.go` (delete `block_audit.go` and the separate `BlockAuditRepository` interface at `block_audit.go:18-21`). Method bodies are unchanged: one `db.Transaction` containing the domain write (INSERT `ON CONFLICT DO NOTHING` / DELETE) then `r.audit.AppendInTx(ctx, tx, fact)` with the established in-tx swallow `if err != nil && !errors.Is(err, ErrAuditDuplicate)`; wrap as `"append block audit: %w"`. `ErrAuditDuplicate` (`audit_ledger.go:65`) stays swallowed in-tx exactly as `operation.go:182` — a duplicate implies the retried operation already committed the identical fact, and the swallow runs after the domain write inside the same tx (inert for fresh-v7 facts, E12).
3. `NewBlockRepository(db, audit)` (`block.go:45-47`) is unchanged: still fail-closed via `requireAuditLedger`. With the interface folded, **every** `BlockRepository` value — including `mocks.BlockRepo`, which already implements the methods (`mocks.go:567-579`) — is audit-capable; a block service without an audit path becomes unconstructible at compile time.
4. `Block`/`Unblock`/`IsBlocked`/`ListBlocked` remain on the interface and unchanged (single-statement, no audit); they serve the guard path (`WithBlockChecker`, `new.go:190`), tests, and the seeded-pair setup in REQ-5.
5. Mechanical consequence: `audit_wiring_matrix_integration_test.go:41` `repository.NewBlockRepository(db, ledger).(repository.BlockAuditRepository)` drops the assertion (field `matrixRepo.block` retyped to `repository.BlockRepository`; the `:88-96` cases compile unchanged).

*Decision (from the superseded spec §6.3, still binding):* a separate `BlockAuditRepository` + service type-assertion is the convention for services with an *optional* audit gate; block loses the gate in REQ-2, so the compile-time guarantee of the widened interface is strictly stronger and removes the `block.audit_unavailable` failure mode from the language.

### REQ-2 — Service: Block/Unblock write the fact in the business transaction, unconditionally

File: `internal/service/block.go`.

1. Delete: the `audit AuditLogger` field (`:20`), the `BlockOption` type (`:24`), `WithBlockAuditLogger` (`:27-29`), and the options loop in `NewBlockService` (`:31-37`) → `func NewBlockService(blocks repository.BlockRepository) BlockService`.
2. `Block` (`:39-64`): delete the entire `if s.audit != nil { ... }` branch (`:50-59`, including the `block.audit_unavailable` path at `:53`) and the else-branch plain call (`:61-63`). Keep validation (`mustActor`, non-empty `uid`, `uid != actor`), then:
   ```go
   if err := s.blocks.BlockWithAudit(ctx, actor, uid,
       buildLegacyAuditFact(ctx, actor, domain.AuditUserBlock, "user", uid, uid, nil, domain.ID{})); err != nil {
       return apperr.Internal("block.block_failed", "failed to block user").Wrap(err)
   }
   ```
3. `Unblock` (`:67-89`): same deletion (`:75-84`, `:86-88`) and the mirrored call with `domain.AuditUserUnblock` and `apperr.Internal("block.unblock_failed", "failed to unblock user")`.
4. Fact shape (asserted in REQ-7): `Action` = `domain.AuditUserBlock`/`domain.AuditUserUnblock` (`domain/audit.go:31-32`), `ResourceType` = `"user"`, `ResourceID` = `uid`, `Source` = `"aero-id"`, `ActorUID` = actor, `Payload["target_uid"]` = uid; tenant/trace bind from ctx via `buildAuditFact` → `bindAuditContext` (`service/audit_ledger.go:92-118`); `OccurredAt` defaults to now; fresh v7 `EventID` per call (E12, unchanged).
5. Invariants after this change: `grep -n '_ =' internal/service/block.go` matches nothing (already true, F5) and `block.audit_unavailable` / `WithBlockAuditLogger` / `BlockOption` do not exist anywhere in non-test Go.

### REQ-3 — Composition root wiring

File: `internal/app/new.go`.

1. `:197-198` →
   ```go
   blockSvc := service.NewBlockService(blockRepo)
   ```
   (drop `service.WithBlockAuditLogger(auditLedger)`).
2. No other wiring change: `:146` `NewBlockRepository(gdb, auditLedgerRepo)` already passes the transactional ledger; `blockRepo` continues to satisfy `service.BlockChecker` for `WithBlockChecker` (`:190`) and the widened `repository.BlockRepository` everywhere.

### REQ-4 — Test double (verify-only)

File: `internal/service/mocks/mocks.go`.

No edits. `BlockRepo` already implements the widened `BlockRepository` (fields `:533-534`, methods `:567-579`, nil-default success), so the package compiles without changes — that is the compile-time guard that keeps the double honest. Confirm in review that the mock still satisfies the interface after REQ-1 (a compile failure here would mean REQ-1 forgot an implementer).

### REQ-5 — Integration test: atomic commit and post-INSERT rollback (direction T-5)

File: `internal/repository/block_integration_test.go` (build tag `integration`, testcontainers via `testpg.Start`/`testpg.Migrate`).

Setup note: the existing `setupBlockRepo` (`:15-20`) uses `AuditLedgerConfig{}` (keyless, `:19`) and stays for the plain-path tests; the new tests build their own ledger with a signing key (precedent: `"matrix-audit-key-0123456789abcdef"`, `audit_wiring_matrix_integration_test.go:30` — ≥32 bytes per the F5 fail-closed key fix) and a `repository.NewAuditLedgerRepository` so `AppendInTx` does not fail on the key check (`audit_ledger.go:130-132`).

Test 1 — `TestBlockRepo_BlockWithAuditCommitsAtomically`:

1. `blocks := repository.NewBlockRepository(db, keyedLedger)`; fact fully populated: `Source: "aero-id"`, `SourceRegion: "local"`, `PartitionKey: "tenant-a:a"`, `TenantID: "tenant-a"`, `ActorUID: "a"`, `Action: "user.block"`, `ResourceType: "user"`, `ResourceID: "b"`, `OccurredAt: now`.
2. `BlockWithAudit(ctx, "a", "b", fact)` returns nil. Assert all three committed atomically:
   - exactly one `user_blocks` row `(a, b)`;
   - one `audit_ledger` row with `action='user.block'`, `actor_uid='a'`, `resource_id='b'`, `partition_key='tenant-a:a'`, `chain_sequence=1`, non-empty `signature`;
   - one `audit_publications` row with `status='pending'`, `attempt=0`, `fact_id` = the ledger row's `id`.
3. `ledger.VerifyChain(ctx, "aero-id", "tenant-a:a")` (`audit_ledger.go:299`) returns nil (in-tx append produced a signed, linked chain).
4. `UnblockWithAudit(ctx, "a", "b", fact2)` with `Action: "user.unblock"`: `user_blocks` row gone; ledger has a second row with `chain_sequence=2`; a second pending publication; `VerifyChain` still nil.
5. Repeat `BlockWithAudit(ctx, "a", "b", ...)`: still exactly one `user_blocks` row (idempotent `ON CONFLICT DO NOTHING` — the re-block path must not duplicate the domain row; the fact append on the repeated call is unconditional legacy-Log parity, documented at `block_audit.go:23-24` and now `block.go`).

Test 2 — `TestBlockRepo_BlockWithAuditRollsBackOnInjectedFailure` ("injected failure after the `user_blocks` INSERT rolls back all three; 0 orphan rows"):

1. *Invalid fact (validation failure after the INSERT):* `BlockWithAudit(ctx, "a", "b", &domain.AuditFact{})` — the `user_blocks` INSERT executes first inside the tx, then `AppendInTx` → `validateAuditFact` fails (`audit_ledger.go:365-369`) → error returned. Assert **zero** `user_blocks` rows, **zero** `audit_ledger` rows for the partition, **zero** `audit_publications` rows.
2. *Ledger PK collision (failure inside `createAuditRows`):* pre-append a ledger row carrying an explicit ID via out-of-tx `ledger.Append(ctx, &domain.AuditFact{ID: id, ...})` (with an `EventID` distinct from the collision fact's, so `checkAuditDuplicate` passes — `audit_ledger.go:180-187` — and the INSERT hits the `audit_ledger` PK). Then `BlockWithAudit` with a fact carrying the same `ID`: the ledger INSERT raises 23505, which stays raw (constraint-precise mapping, `audit_ledger.go:214-222`) → error returned. Assert **zero** new `user_blocks` rows and the pre-existing ledger/publication counts unchanged (no orphan fact, no orphan publication, no block row without a fact).
3. *Unblock mirror:* seed a pair with plain `Block`; `UnblockWithAudit(ctx, "a", "b", &domain.AuditFact{})` → error; pair still blocked (one `user_blocks` row), zero new ledger/publication rows. PK-collision variant with the seeded pair behaves identically (counts unchanged).

*Testability notes:* both injections are deterministic and need no fault-injection framework (the matrix crash injection — `installFailPublicationInsertTrigger` at `audit_wiring_integration_test.go:271`, used by `audit_wiring_matrix_integration_test.go:146,190` — already covers the third failure point, publication INSERT blocked, and stays complementary). These tests exercise rollback of the domain write together with ledger+publication writes, which is precisely the property the pre-B2-1 out-of-tx code lacked.

### REQ-6 — Wiring test: fail-closed constructors (direction T-5)

File: `internal/repository/audit_wiring_test.go`.

1. Add to `TestConstructorsRejectNilAudit` (`:29-48`): `mustPanic(t, "NewBlockRepository", func() { repository.NewBlockRepository(db, nil) })`.
2. Add to `TestConstructorsRejectTypedNilAudit` (`:50-66`), reusing the existing `var ledger repository.AuditLedgerTransactionalRepository = (*auditLedgerStub)(nil)`: `mustPanic(t, "NewBlockRepository (typed nil)", func() { repository.NewBlockRepository(db, ledger) })`.
3. The nil-map-kind test (`:72-77`) may also list `NewBlockRepository` (optional; the shared `requireAuditLedger` is already covered by the three existing entries — one block entry per test class is the required minimum).

This closes the F1 gap: the constructor is fail-closed today but untested; the direction's acceptance requires the entries.

### REQ-7 — Service unit tests: unconditional WithAudit call, fact shape, error codes (direction T-4)

File: `internal/service/block_test.go` (package `service_test`; `assertAppCode` at `file_test.go:23` asserts HTTP only — add a local `assertAppCodeAndCode(t, err, wantHTTP, wantCode string)` helper using the existing `errors.As` pattern, or assert `ae.Code` inline).

1. `TestBlockService_BlockHappyPath` (`:34-48`): rewrite from `BlockFn` to `BlockWithAuditFn` — after REQ-2 the plain path does not exist, and leaving `BlockFn` in place would make the test silently assert nothing (mock default returns nil, captured args stay empty and the `gotBlocker != "a"` check fails). Assert `blocker="a"`, `blocked="b"`, called exactly once, error nil.
2. `TestBlockService_UnblockHappyPath` (`:50-67`): same conversion to `UnblockWithAuditFn`.
3. `TestBlockService_BlockEmitsAuditEvent` (`:69-99`): drop `service.WithBlockAuditLogger(&mocks.Auditor{})` → `service.NewBlockService(repo)` (the option no longer exists). Keep the call-count and fact assertions; **add** `fact.Source == "aero-id"` (the direction's fact-shape acceptance). Existing assertions already cover `Action == domain.AuditUserBlock`, `ActorUID == "a"`, `ResourceType == "user"`, `ResourceID == "b"`, `Payload["target_uid"] == "b"`.
4. `TestBlockService_UnblockEmitsAuditEvent` (`:101-131`): same changes with `domain.AuditUserUnblock`.
5. `TestBlockAuditFailureFailsClosed` (`:133-143`): drop the option; strengthen to `assertAppCodeAndCode(t, err, 500, "block.block_failed")` — the error is **never swallowed** and the code is the domain-write code (in-tx append failures surface through the domain write code, per the superseded spec's F3: distinct `*.audit_failed` codes exist only for out-of-tx `Record` paths).
6. `TestUnblockAuditFailureFailsClosed` (`:145-154`): same with `block.unblock_failed`.
7. Validation tests (`:13-32` self-block 400, blank-uid 400, requires-actor) and `TestBlockService_ListBlockedHappyPath` (`:156-173`) are unchanged — the mock's nil-default WithAudit methods return nil.

---

## 6. Acceptance checks (preserved from the direction, made testable)

| ID | Direction's check | Testable form |
|---|---|---|
| T-5 (repo) | Integration test (`block_integration_test.go`, testpg testcontainers): BlockWithAudit commits `user_blocks` + `audit_ledger` (`chain_sequence=1`, `signature`) + `audit_publications` (pending, attempt 0) atomically; VerifyChain nil; injected failure after the `user_blocks` INSERT (invalid fact, ledger PK collision) rolls back all three (0 orphan rows); UnblockWithAudit mirrors | REQ-5 Test 1 (three-row atomic commit, chain + signature + publication row, unblock to `chain_sequence=2`, idempotent re-block) and Test 2 (two deterministic post-INSERT failure injections, both directions, with and without a seeded pair) |
| T-5 (wiring) | `NewBlockRepository(db, nil)` and typed-nil panic (`audit_wiring_test.go` mustPanic style) | REQ-6: entries added to `TestConstructorsRejectNilAudit` and `TestConstructorsRejectTypedNilAudit` |
| T-4 (service unit) | BlockWithAuditFn/UnblockWithAuditFn failure surfaces as 500 `block.block_failed`/`block.unblock_failed` AppError, never swallowed; fact shape (Action `user.block`/`user.unblock`, ResourceType `user`, ResourceID uid, Source `aero-id`) | REQ-7 items 1-6: fact shape asserted in-tx (incl. `Source`), both failure paths assert HTTP 500 + exact code string |
| T-10.2 | `grep -n '_ =' internal/service/block.go` returns nothing audit-related and `make check-audit-swallow` stays green | REQ-2.5: zero `_ =` in the file (already true, F5) and the gate's P1/P2 patterns match no block site — verified by running `make check-audit-swallow` (Makefile `:67-69`) |

**Done gate:** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make build`, plus `make test-integration` (this change modifies repository transaction-surface tests). No new file exceeds the 500-line limit (`block.go` grows by ~25 lines; `block_audit.go` is deleted; test additions are small).

---

## 7. Documented behavioral consequences and decisions

### 7.1 Tenant-less identities fail closed (unchanged, re-affirmed)

`buildAuditFact` → `bindAuditContext` binds `TenantID` from ctx (`service/audit_ledger.go:92-118`; `TenantFrom`, `service/service.go:78-82`); identities without `tenant_id` (possible via `sso/client.go:196-202`, `jwks.go:93-99`) produce an incomplete fact, `validateAuditFact` rejects it inside the tx (`audit_ledger.go:365-369`), and Block/Unblock return `500 block.block_failed`/`block.unblock_failed` with the block rolled back. This is already the behavior on the gated WithAudit path (wired in production at `new.go:198`); REQ-2 makes it the only behavior. A block row never exists without its durable audit fact. If product policy ever requires tenant-less blocking to succeed, that is a separate decision requiring a tenant-resolution policy — not a swallow.

### 7.2 Error-surface change (unchanged, re-affirmed)

`POST /blocks` and `DELETE /blocks/:uid` return `500` on ledger unavailability instead of always succeeding (204). This is the direction's intent ("never `_ =`-swallowed") and is covered by REQ-7.5-6.

### 7.3 Decisions

- **Fold `BlockAuditRepository` into `BlockRepository`** (REQ-1) instead of keeping the separate interface + unconditional runtime assertion: compile-time fail-closed is strictly stronger, removes `block.audit_unavailable` as a reachable state, and matches the superseded spec's REQ-1.6/§6.3. Cost is one mechanical type-assertion removal in the matrix test.
- **Keep `buildLegacyAuditFact` as the fact builder** (not the superseded spec's proposed `blockAuditFact` wrapper): it is already shipped, already used by the block service and the sibling conversions, adds `Payload["target_uid"]` (asserted by existing tests), and preserves the fresh-v7 EventID policy (C2 allowlist; B2-4 is out of scope).
- **No new error code**: in-tx append failures surface through `block.block_failed`/`block.unblock_failed`; distinct `*.audit_failed` codes are reserved for out-of-tx `Record` paths.

### 7.4 Rejected alternatives

- **Keep `WithBlockAuditLogger` as an optional gate** — rejected: it is the fail-open path (nil option ⇒ block without audit) that the direction's acceptance explicitly removes; with REQ-1 the option has no remaining legitimate value.
- **Leave `BlockAuditRepository` separate and assert unconditionally in the service** — rejected (§7.3): runtime check instead of compile-time guarantee; the matrix-test churn is identical either way.
- **Reuse the matrix test as the only repo-level coverage** — rejected: it asserts ledger/publication counts only; it does not assert `chain_sequence`, `signature`, `VerifyChain`, idempotent re-block, or the post-INSERT invalid-fact/PK-collision injections the direction's T-5 requires.

---

## 8. Cross-direction dependencies

- This is the last fail-open gate among the B2-1 conversions in the *live block path*; the sibling services (ban/admin/file-share/message/comment) have their own gate-removal directions and are untouched here.
- No dependency on the deterministic `event_id` direction (B2-4): facts keep fresh-v7 IDs; the `ErrAuditDuplicate` in-tx swallow (REQ-1.2) follows the existing contract so a future anchored-ID retry behaves identically to operation/callback/sync-job/event-receipt.
- `make check-audit-swallow` and `make check-anchored-event-ids` remain green throughout (F5; `audit_fact.go` is already on the anchored-IDs allowlist).
