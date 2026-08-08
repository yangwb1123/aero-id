# Design — internal/block: move Block/Unblock audit writes into the business transaction (live service/repository pair, fail-closed wired)

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-06-internal-block-in-tx-audit-requirements.md` (REQ-1..REQ-7, AC-1..AC-3) |
| Module | `internal/repository/block.go`, `internal/service/block.go`, `internal/app/new.go`, `internal/service/mocks/mocks.go`, repository/service tests |
| Head verified | `13e4676` + uncommitted working tree (39 modified files; block surface untouched by them: `NewBlockRepository(gdb)` `new.go:144`, `WithBlockAuditLogger` `new.go:206` still present) |
| Campaign | `aero-id-b2` (in-tx audit directions; this design covers only the block direction) |
| Date | 2026-08-06 |
| Status | Design — every evidence claim re-verified against the working tree; two material amplifications (A1: swallow gate excludes `Log`, A2: sibling direction conflicts on the block shape) are load-bearing for landing order |

## 1. Evidence verification (untrusted claims vs. repository)

All cited symbols were re-checked against the working tree. **All substantive claims hold.** Line
numbers drifted in five places (immaterial); two amplifications (A1, A2) constrain the design.

| # | Claim | Verdict (proof) |
|---|---|---|
| E1 | Unwired tree swallows audit error after repo mutation | ✅ Pattern confirmed at `internal/block/block.go:66,86` (spec said :54,:76 — drift; both sit after `s.blocks.Block/Unblock` and discard the error with `_ =`) |
| E2 | Live twin swallows at :57,:79 | ✅ Confirmed at `internal/service/block.go:54,77` (drift of 3), both inside `if s.audit != nil` after `s.blocks.Block`/`s.blocks.Unblock` (:50,:73) |
| E3 | `AuditLedgerTransactionalRepository.AppendInTx` exists | ✅ Interface `audit_ledger.go:38-41`; impl at `:106` (spec said :123 — drift). Guards: signing key configured, non-nil tx, `validateAuditFact` |
| E4 | `createAuditRows` writes ledger fact + pending `audit_publications` row in one tx | ✅ `createAuditRows` `:172-205`: `tx.Create(auditLedgerFactToRow(...))` then `tx.Create(&auditPublicationRow{Status:"pending", Attempt:0, ...})`. 23505 on `uq_audit_ledger_tenant_event` translated to `ErrAuditDuplicate`; chain-seq violation stays raw |
| E5 | Fail-closed `FullAuditLedgerRepository`/`requireAuditLedger` | ✅ `audit_wiring.go:11-15` interface; `requireAuditLedger` `:21-28` panics on nil and typed-nil (reflect check). Used by account/operation/sync-job/event-receipt/callback constructors |
| E6 | Reference `UpdateProfileWithAudit` | ✅ `account_profile.go:67-71` wrapper (nil-guard), `updateProfile` `:74-101`, `AppendInTx` `:101` inside `db.Transaction` — exact match to spec |
| E7 | Reference `CreateWithAudit` + `ErrAuditDuplicate` in-tx swallow | ✅ `operation.go:135-139` wrapper, `create` `:141-186`, `AppendInTx` `:182` with `!errors.Is(err, ErrAuditDuplicate)`; same swallow at `callback_audit.go:49,73,101`, `event_receipt.go:180`, `sync_job_audit.go:55` — all confirmed by grep. **Stale by two (post-design):** the sibling direction has since landed `operation_audit.go:136` and `sync_job.go:116`, bringing the in-tx swallow set to **seven** sites; all follow the identical contract (re-audited in §8.4) |
| E8 | Wiring at `new.go:205-206`; `NewBlockRepository` at :144 | ✅ `blockRepo := repository.NewBlockRepository(gdb)` `:144` (no audit param yet); `blockSvc := service.NewBlockService(blockRepo, service.WithBlockAuditLogger(auditLedger))` `:205-206`; `WithBlockChecker(blockRepo)` `:198` (spec said :197); `auditLedgerRepo` (type `repository.FullAuditLedgerRepository`) in scope from `buildAuditStack` at `:134` |
| E9 | `internal/block` has exactly one importer; that importer is unwired | ✅ `grep -rn "internal/block"` non-test match is only `internal/notification/dispatcher.go:8`; `internal/notification` has zero importers outside itself (live relay uses `service.NewNotificationDispatcher`, `internal/app/outbox.go:36`) |
| E10 | Test styles exist (`block_integration_test.go`, `audit_wiring_test.go`) | ✅ `block_integration_test.go` build tag `integration`, `testpg.Start`+`testpg.Migrate`; `audit_wiring_test.go` has `mustPanic`, `TestConstructorsRejectNilAudit` (5 entries), `TestConstructorsRejectTypedNilAudit` (2 entries + `auditLedgerStub`) |
| F1 | Tenant-less identity possible; fact rejected by `validateAuditFact` | ✅ `sso/client.go:196-202` and `jwks.go:93-99` `ValidateIdentity` return `TokenIdentity{UID, Subject}` with **empty `TenantID`**; `TenantFrom` (`service/service.go:78-83`) returns it; `validateAuditFact` (`audit_ledger.go:339-344`) requires `Source, SourceRegion, PartitionKey, TenantID, Action, OccurredAt`; today the rejection is swallowed (`_ =`) and the block still commits. **Config-dependent blast radius (F-A):** in the default no-JWKS deployment (`sso.jwks_url: ""`, `configs/config.yaml:100`) every request on both surfaces is tenant-less → the flip is a total feature outage there (FM-2); JWKS config (`RequireTenant: true`, `jwks.go:197`) 401s tenant-less tokens, making the flip unreachable |
| F2 | `buildAuditFact`/`bindAuditContext`/`normalizeAuditInput` helper chain | ✅ `service/audit_ledger.go:82-96`, `:97-108`, `:110-133`; `Log` adapter `:136-143` maps `SubjectType→ResourceType`, `SubjectID→ResourceID`, `Meta→Payload` (TargetUID dropped today — block's `ResourceID`=uid already carries it); `auditTenantPartition` `:161-163`; defaults `Source="aero-id"`, `SourceRegion="local"`, partition `accountID→actorUID→"system"` then `tenant:partition` |
| F3 | Error-code convention: in-tx failures surface via the domain code | ✅ `service/operation.go:133` `apperr.Internal("operation.create_failed",...).Wrap(err)` wraps `CreateWithAudit` failures; no `*.audit_failed` code exists for in-tx paths |
| F4 | Implementers of `BlockRepository`: `blockRepo` + `mocks.BlockRepo` only | ✅ `mocks.go:480-517` with compile-time assert `mocks.go:637`; `BlockChecker` consumers (`WithBlockChecker` new.go:198, `WithDispatcherBlockChecker` outbox.go:37) use only `IsBlocked` |

**Amplification A1 — the swallow gate does not currently cover `Log`.** `scripts/check-audit-swallow.sh`
P1/P2 method lists are `Record|Publish|FinishPublication|ClaimPublication`; `Log(` is an explicit
documented carve-out ("legacy diagnostics surface (AGENTS.md carve-out); not in either pattern").
`internal/service/block.go` is therefore **already compliant** with today's gate. The requirements'
claim ("this change makes `internal/service/block.go` compliant with it") is directionally right but
the real enforcement is REQ-2's grep-able invariant (zero `_ =` audit calls); the gate only bites
block once the sibling direction extends P1/P2 to `s.audit.*` including `Log` (see A2).

**Amplification A2 — the sibling direction conflicts on the block shape (landing order is load-bearing).**
`docs/plans/2026-08-06-cmd-server-complete-in-tx-audit-writes-design.md` §3.1-§3.3 covers the same
files with a different contract: separate `BlockAuditRepository` interface + service-side
`s.blocks.(repository.BlockAuditRepository)` type-assert, `WithBlockAuditLogger` **kept**, fresh
`EventID` per fact (`buildLegacyAuditFact`), new `block.audit_unavailable` code, and the
`s.audit == nil` fail-open tail preserved. This direction (requirements §6.3) explicitly rejects that
shape in favor of a widened `BlockRepository` + compile-time fail-closed constructor. Both agree on:
`NewBlockRepository(db, audit)`, one tx (domain row + ledger + publication), `ErrAuditDuplicate`
in-tx swallow, zero `_ = s.audit.Log` in the block service. They disagree on interface mechanics,
the option, the fact builder, and the error code. They touch identical lines (`block.go` repo+service,
`mocks.go`, `new.go:144,205-206`). **Reconciliation (this design):** land this direction first and
exclude block from the sibling's ten-site list; the sibling's §3.3 block row then reduces to a no-op
or is adopted as-is minus the type-assert. If the sibling lands first, this direction becomes a
refactor of the block slice: widened interface, constructor fail-closed, option removal. Either order
converges on the same final state; do not land both with the sibling's type-assert shape **and** the
widened interface, because the service would then double-dispatch (plain `Block` + `BlockWithAudit`
both live on the same struct and the sibling's `if s.audit != nil` tail would never fire in the wired
root — dead code, not a correctness bug).

## 2. Target architecture

```text
POST /blocks  /  DELETE /blocks/:uid          (handler/http/block.go — unchanged, still 204)
BlockService.Block/Unblock (gRPC)             (handler/grpc/block.go:24,31 — unchanged;
                                                registered app/grpc.go:68)
      │  apperr: 400 block.invalid · 401 · 500 block.block_failed / block.unblock_failed
      │  (gRPC: same AppError → codes.Internal via grpcError, server.go:412-421)
      ▼
blockService.Block/Unblock                    (internal/service/block.go)
      │  buildAuditFact(ctx, {Source:"aero-id", Action: user.block|user.unblock,
      │    ResourceType:"user", ResourceID:uid, ActorUID:actor})   — tenant/trace from ctx
      ▼
blockRepo.BlockWithAudit/UnblockWithAudit     (internal/repository/block.go)
      │  one gorm tx:
      │    1. user_blocks INSERT ON CONFLICT DO NOTHING  /  DELETE (idempotent, unchanged)
      │    2. auditLedgerRepo.AppendInTx(tx, fact)        — ledger row + pending
      │       audit_publications row; ErrAuditDuplicate swallowed in-tx (inert: EventID unset)
      ▼
audit_ledger (signed hash chain per (source, partition)) + audit_publications outbox → drain worker
```

Properties gained (both lost today): (1) **atomicity** — a block row can no longer commit without
its durable fact+publication; crash between commit and `Log` is structurally impossible; (2)
**fail-closed** — construction without a ledger panics (startup), an incomplete fact (tenant-less
identity) rolls back the block instead of being silently dropped.

## 3. API changes

### 3.1 Repository — `internal/repository/block.go`

```go
// interface gains:
BlockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error
UnblockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error

// struct gains an audit field; constructor signature changes (fail-closed):
type blockRepo struct { db *gorm.DB; audit AuditLedgerTransactionalRepository }
func NewBlockRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) BlockRepository
// → audit = requireAuditLedger("NewBlockRepository", audit)  // panics on nil/typed-nil
```

`BlockWithAudit`: `db.WithContext(ctx).Transaction(func(tx) error {` → (a) current INSERT with
`clause.OnConflict{DoNothing: true}` (idempotency preserved); (b) `if fact != nil { if err :=
r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) { return
fmt.Errorf("append block audit: %w", err) } }`. `UnblockWithAudit` mirrors with the DELETE and
`"delete user block with audit"`. Plain `Block`/`Unblock` stay unchanged (single-statement, no
audit) and stay on the interface — they serve the guard path (integration tests, future non-audited
callers). Note the deliberate `fact != nil` guard mirrors `operationRepo.create`; the service never
passes nil, so the wired path always appends.

### 3.2 Service — `internal/service/block.go`

- Delete the `audit AuditLogger` field, `BlockOption`, `WithBlockAuditLogger`, and the
  `if s.audit != nil { _ = s.audit.Log(...) }` tails (:52-63, :75-86).
- `NewBlockService(blocks repository.BlockRepository) BlockService` — no options remain (variadic
  removal is source-compatible for all existing callers that pass no options).
- `Block`: validation unchanged (`mustActor`, non-empty `uid`, `uid != actor`), then
  `s.blocks.BlockWithAudit(ctx, actor, uid, blockAuditFact(ctx, actor, uid, domain.AuditUserBlock))`;
  error → `apperr.Internal("block.block_failed", "failed to block user").Wrap(err)` (F3: in-tx
  failures surface through the domain code, not a new `block.audit_failed`).
- `Unblock`: same with `domain.AuditUserUnblock` → `block.unblock_failed`.
- Private helper (same package as `buildAuditFact`):

```go
func blockAuditFact(ctx context.Context, actor, uid, action string) *domain.AuditFact {
    return buildAuditFact(ctx, AuditFactInput{Source: "aero-id", Action: action,
        ResourceType: "user", ResourceID: uid, ActorUID: actor})
}
```

Tenant/trace bind from ctx via `bindAuditContext`; `OccurredAt` = now; partition becomes
`tenant:actor`; for the tenant-less case `normalizeAuditInput` falls back `accountID → actorUID → "system"` —
but the fact then fails `validateAuditFact` (empty `TenantID`) before any row write, so the fallback
partition never reaches the DB (corrected in §8.2); `EventID` stays unset (B2-4 out of scope; makes the `ErrAuditDuplicate`
swallow inert, matching `checkAuditDuplicate`'s zero-EventID short-circuit `audit_ledger.go:157-159`).

### 3.3 Composition root — `internal/app/new.go`

- `:144` → `blockRepo := repository.NewBlockRepository(gdb, auditLedgerRepo)` (already in scope at
  `:134`, satisfies `AuditLedgerTransactionalRepository`).
- `:205-206` → `blockSvc := service.NewBlockService(blockRepo)`.
- No other wiring change: `blockRepo` still satisfies `service.BlockChecker` (`:198`) and
  `repository.BlockRepository` in `outboxDependencies` (`internal/app/outbox.go:23,37`).

### 3.4 Test double — `internal/service/mocks/mocks.go` (`BlockRepo`, :480-517)

Add `BlockWithAuditFn`/`UnblockWithAuditFn` fields and methods whose nil-default returns `nil`
(mirroring `Block`/`Unblock` defaults), so unrelated message/notification/outbox tests using the
mock keep passing. The existing compile-time assert `mocks.go:637` then enforces the widened
interface — the mock cannot silently lag.

### 3.5 No schema, proto, or handler changes

`user_blocks`, `audit_ledger`, `audit_publications` tables and their migrations are untouched; block
serves over **HTTP + gRPC**. Both surfaces are live and funnel into the same
`blockService.Block/Unblock`: `BlockHandler` (`internal/handler/http/block.go`) and `BlockServer`
(`internal/handler/grpc/block.go:24,31`, registered at `internal/app/grpc.go:68`). Both gain the
new failure class unchanged in shape — HTTP `500 block.block_failed`/`block.unblock_failed`,
gRPC `codes.Internal` via `grpcError` (`internal/handler/grpc/server.go:412-421`) — and the HTTP
surface (`POST /blocks`, `DELETE /blocks/:uid` → 204) is otherwise unchanged (no schema, proto,
or handler changes; §5).

## 4. Compatibility constraints

1. **`BlockRepository` widened — compile-time break for implementers.** Known implementers are
   exactly `blockRepo` and `mocks.BlockRepo` (F4). Any external/private implementer must add the two
   methods; this is the intended fail-closed guarantee (there is no code path that can construct the
   service without the audit-capable repo).
2. **`NewBlockRepository` signature change.** Call sites: `new.go:144` and
   `block_integration_test.go:19` (`setupBlockRepo` — must construct a ledger, §7). The sibling
   direction expects the same signature — compatible.
3. **`NewBlockService` drops options.** `WithBlockAuditLogger` is deleted; only `new.go:205-206` and
   the two audit-emission service tests use it (verified by grep; `.claude/worktrees/` copies are
   stale artifacts, not part of the tree). Option-less call sites compile unchanged.
4. **`BlockChecker` consumers unaffected.** `IsBlocked` is unchanged; message service guard
   (`new.go:198`) and the outbox relay dispatcher (`outbox.go:37`) keep working off the same struct.
5. **Idempotency semantics preserved.** Re-block of an existing pair stays a no-op on
   `user_blocks` (`ON CONFLICT DO NOTHING`), unblock of a missing pair stays a no-op; each call
   still appends one fact (matching the "facts record even when the domain write is a 0-row no-op"
   convention of the sibling design and today's Log-after-call placement).
6. **Fact shape parity with today's `Log` mapping.** `SubjectType/SubjectID/TargetUID` →
   `ResourceType/ResourceID` (TargetUID is already dropped by `Log` at `audit_ledger.go:136-143`);
   `Payload` stays nil as today (`Log` passes `legacy.Meta`, which block never set). Audit-list
   consumers see the same fields they see today, now durable.
7. **Cross-direction reconciliation (A2).** Land order with the sibling direction must be agreed
   before either merges; both touch `internal/service/block.go`, `internal/repository/block.go`,
   `mocks.go`, and `new.go:144,205-206`. This design's shape (widened interface, fail-closed
   constructor, no option) is the requirements' selected direction and is a strict subset of the
   sibling's block slice; recommend block be removed from the sibling's ten-site list.
8. **`make check-anchored-event-ids` stays green.** The fact carries no `EventID` (no
   `idgen.NewID()` event anchor introduced); the gate's only random-EventID site (callback replay
   C2) is untouched.

## 5. Failure modes

| FM | Trigger | Behavior after change | Compared to today |
|---|---|---|---|
| FM-1 | Ledger DB error/unavailability during append (after `user_blocks` write inside tx) | Whole tx rolls back; Block/Unblock return `500 block.block_failed`/`block.unblock_failed`; zero orphan rows (REQ-5 Test 2 proves it) | Block committed; fact lost; 204 returned |
| FM-2 | **Tenant-less identity — config-dependent blast radius, and the default config makes it total.** In the shipped default (`sso.jwks_url: ""`, `configs/config.yaml:100`), `buildTokenValidator` returns `*sso.Client` and `ValidateIdentity` (`sso/client.go:196-202`) always yields empty `TenantID` — **every** Block/Unblock on **both** surfaces (HTTP + gRPC) is tenant-less; no Snaplink token can carry a tenant into this path. With JWKS configured, `RequireTenant: true` rejects tenant-less tokens at auth (401, `jwks.go:197-203`) and the flip is unreachable. `FallbackValidator` (`jwks.go:79-110`) is definition-only, not wired. `bindAuditContext` leaves `TenantID` empty → `validateAuditFact` fails | Append fails → tx rollback → 500 `block.block_failed`/`block.unblock_failed` (HTTP) / `codes.Internal` (gRPC). **Block can no longer commit without its audit fact** — accepted consequence (§6.1 of requirements); matches `mustTenant` fail-closed precedent in account/operation/callback services. **Landing precondition:** JWKS validation or an equivalent tenant-resolution policy must be deployed and verified **before** the flip ships (§6 step 1) — a deployment prerequisite, not a token-content question; without it the flip takes the block feature fully offline with deterministic 500s | Block committed; fact rejected and silently dropped |
| FM-3 | Signing key unset | `AppendInTx` returns "signing key is not configured" → 500. Practically unreachable in the wired root (`validateAuditLedgerConfig` fails startup, `app/audit.go:19-27`) | Block committed; 204 |
| FM-4 | Repeated call on same pair (concurrent or retried) | `user_blocks` stays one row (ON CONFLICT); each call appends one fact — chain grows per call, serialized per partition by `pg_advisory_xact_lock` (`audit_ledger.go:128`). No orphan and no duplicate domain row | Same domain behavior; facts were racy/dropped |
| FM-5 | Retry with identical anchored fact (future B2-4 only) | `ErrAuditDuplicate` swallowed in-tx after the domain write — same contract as operation/callback/sync-job/event-receipt; can never mask a failed domain write (swallow runs after the write inside the same tx) | n/a (inert today: `EventID` unset → `checkAuditDuplicate` short-circuits, and `uq_audit_ledger_tenant_event` ignores NULLs) |
| FM-6 | Deadlock | Impossible by construction: both `BlockWithAudit` and `UnblockWithAudit` take the `user_blocks` row lock **before** the audit partition advisory lock (consistent ordering; no other code path takes the advisory lock first) | n/a (no advisory lock in block path) |
| FM-7 | Nil/typed-nil ledger at construction | Panic at startup (`requireAuditLedger`) — the process fails closed before serving traffic (REQ-6 proves both nil shapes) | `WithBlockAuditLogger(nil)` silently ran audit-inactive |

## 6. Migration steps (each step leaves the tree green)

No SQL migration, no proto, no repository config change. Implementation order — **step 1 is a
landing precondition (deployment gate, not a code change) and must hold in every target
environment before the flip lands in steps 4-5**:

1. **Landing precondition — tenant resolution deployed and verified (FM-2/F-A).** The flip turns
   every tenant-less Block/Unblock into a deterministic 500/Internal, and in the shipped default
   config tenant-less is **every** request: `configs/config.yaml:100` ships `sso.jwks_url: ""`,
   so `buildTokenValidator` (`internal/app/auth.go:13-15`) returns the plain `*sso.Client`, whose
   `ValidateIdentity` (`internal/sso/client.go:196-202`) always returns `TokenIdentity{UID,
   Subject}` with empty `TenantID` on both the HTTP and gRPC surfaces. Without this step the flip
   takes the block feature fully offline (deterministic `500 block.block_failed` /
   `block.unblock_failed`, gRPC `codes.Internal`), not intermittently. **Do not ship the flip to an
   environment that has not passed this check.** Per target environment, verify and record in the
   release note for the change that lands steps 2-5: (a) `sso.jwks_url` is set (JWKS validation
   with `RequireTenant: true` → tenant-less tokens are rejected 401 at auth, `jwks.go:197-203`, so
   the flip is unreachable) or an equivalent tenant-resolution policy populates `TenantID` from
   `ValidateIdentity`; (b) probe with an authenticated request that a non-empty tenant resolves —
   e.g., any account/callback operation that already requires `mustTenant` succeeds instead of
   returning 403 `tenant.required`; (c) if the environment runs the JWKS config for the first
   time, that config change ships with or just before the flip and is live and probed before the
   flip serves traffic. This gate is deliberately not a startup failure in code: the ledger is
   wired in every default deployment, so a hard startup requirement would break the out-of-box
   config; the enforcement point is deployment sequencing. (This supersedes the requirements
   §6.1 wording — "confirm whether deployed Snaplink tokens always carry `tenant_id`" — which
   framed the issue as token content; the prerequisite is the deployment of JWKS validation or an
   equivalent tenant-resolution policy, not a claim about tokens.)
2. **Repository** (`internal/repository/block.go`): add `audit` field, `requireAuditLedger`
   constructor, widen interface with `BlockWithAudit`/`UnblockWithAudit` (one tx each, domain write
   first, `ErrAuditDuplicate` swallow). Plain methods untouched. Tree does not compile until step 3 —
   do 2+3 together (step 1 must already hold in the target environment before this change ships).
3. **Mock** (`internal/service/mocks/mocks.go`): add the two fns/methods to `BlockRepo` (nil-default
   nil). Compile-time assert at :637 proves the double is honest.
4. **Service** (`internal/service/block.go`): delete option/field/tails; call the `WithAudit`
   variants via `blockAuditFact`; keep error codes `block.block_failed`/`block.unblock_failed`.
   Invariant: `grep -n "_ =" internal/service/block.go` returns nothing. **This is the behavioral
   flip** — from this step on, tenant-less requests roll back and surface 500/Internal; it must
   not ship ahead of step 1.
5. **Wiring** (`internal/app/new.go`): `NewBlockRepository(gdb, auditLedgerRepo)` at :144;
   `NewBlockService(blockRepo)` at :205-206. Flip fully live.
6. **Service tests** (`internal/service/block_test.go`): rewrite
   `TestBlockService_BlockEmitsAuditEvent`/`UnblockEmitsAuditEvent` into fact-shape assertions
   (REQ-7 items 1-2); add the two error-surfacing tests (items 3-4). Validation/happy-path tests
   (`BlockFn`/`UnblockFn`-based) stay valid unchanged.
7. **Repository tests**: extend `audit_wiring_test.go` (REQ-6 entries) and
   `block_integration_test.go` (REQ-5, Test 1 atomic commit + Test 2 rollback injections).
8. **Gate run** (after 2-7): `make test && make lint && make lint-strict && make check-file-sizes &&
   make check-audit-swallow && make check-anchored-event-ids && make build && make test-integration`
   (integration mandatory: this change modifies repository SQL transaction boundaries).
9. **Cross-direction hook**: notify/coordinate the sibling
   `cmd-server-complete-in-tx-audit-writes` design (A2) — exclude block from its ten-site list; its
   `scripts/check-audit-swallow.sh` P1/P2 extension to `s.audit.*` then finds block already clean.

## 7. Testable acceptance mapping

| Acceptance (requirements) | Test (file) | Concrete assertions |
|---|---|---|
| AC-1a / T-5 atomic commit | `TestBlockRepo_BlockWithAuditCommitsAtomically` (`internal/repository/block_integration_test.go`, `integration` tag, `testpg.Start`+`testpg.Migrate`, ledger with `auditLedgerTestKey`-style signing key) | After `BlockWithAudit(ctx,"a","b",fact)`: exactly one `user_blocks(a,b)` row; one `audit_ledger` row (`action='user.block'`, `actor_uid='a'`, `resource_id='b'`, `partition_key='tenant-a:a'`, `chain_sequence=1`, non-empty `signature`); one `audit_publications` row (`status='pending'`, `fact_id` = ledger row id); `VerifyChain(ctx,"aero-id","tenant-a:a")` == nil. Then `UnblockWithAudit` → pair gone, `chain_sequence=2`, second pending publication, chain still valid; repeat `BlockWithAudit` → still one `user_blocks` row |
| AC-1b / T-5 rollback | `TestBlockRepo_BlockWithAuditRollsBackOnInjectedFailure` (same file) | Injection 1 (invalid fact `&domain.AuditFact{}` fails `validateAuditFact` after the INSERT executed in-tx): error returned; **zero** `user_blocks`, **zero** `audit_ledger`, **zero** `audit_publications`. Injection 2 (pre-seeded ledger row with explicit `fact.ID` → PK collision inside `createAuditRows`): error returned; zero new rows, pre-existing counts unchanged. Same two injections for `UnblockWithAudit` (seeded pair stays blocked). No fault-injection framework needed — both are deterministic |
| AC-2 / T-5 fail-closed constructors | entries in `TestConstructorsRejectNilAudit` / `TestConstructorsRejectTypedNilAudit` (`internal/repository/audit_wiring_test.go`) | `mustPanic(t, "NewBlockRepository", func(){ repository.NewBlockRepository(db, nil) })`; `var ledger repository.AuditLedgerTransactionalRepository = (*auditLedgerStub)(nil); mustPanic(t, "NewBlockRepository (typed nil)", ...)` — reused `db := &gorm.DB{}` and `mustPanic` helper |
| AC-3a / T-4 fact shape in-tx | `TestBlockService_BlockWritesAuditFactInTx` / `TestBlockService_UnblockWritesAuditFactInTx` (`internal/service/block_test.go`) | `mocks.BlockRepo{BlockWithAuditFn: capture}`; `svc := service.NewBlockService(repo)`; `ctx := service.WithActor(context.Background(),"a")`; assert exactly one call with `blocker="a"`, `blocked="b"`, `fact.Action==domain.AuditUserBlock`, `fact.ActorUID=="a"`, `fact.ResourceType=="user"`, `fact.ResourceID=="b"`, `fact.Source=="aero-id"` (unblock: `AuditUserUnblock`) |
| AC-3b / T-4 never swallowed | `TestBlockService_BlockAuditAppendErrorSurfaces` / `...UnblockAuditAppendErrorSurfaces` (same file) | `BlockWithAuditFn` returns `errors.New("append failed")` → non-nil error; `assertAppCode(t, err, 500)`; code `block.block_failed` / `block.unblock_failed` |
| Compliance | grep + gates | `grep -n "_ =" internal/service/block.go` empty; `make check-audit-swallow` (already green — A1 — stays green); `make check-anchored-event-ids` green (no EventID introduced); `make test-integration` green (tx boundary change) |

**Verification result:** the evidence is accurate on every load-bearing claim (swallow sites, in-tx
append + publication row, fail-closed wiring helper, both reference implementations, wiring
locations, importer graph, tenant-less identity gap, error-code convention, mock implementer set).
Five line-number drifts and two amplifications (A1 swallow-gate `Log` carve-out; A2 sibling-direction
conflict on the block shape) are recorded above; neither changes the requirements' decisions. The
design implements REQ-1..REQ-7 as specified, with the landing-order constraint on the sibling
direction as the only cross-direction dependency.

## 8. Hardening verification (post-design, 2026-08-06)

Four hardenings were verified against the working tree (sibling B2 landed minus block; block
surface unchanged). Two findings required code changes; both are in `internal/repository/`.

### 8.1 requireAuditLedger covers every live wiring path in new.go

`requireAuditLedger` (`audit_wiring.go:19-28`) panics on `nil` and on typed-nil. Every live
ledger-taking repository constructor in the composition root passes through it:

| Constructor | Wiring | Guard |
|---|---|---|
| `NewAccountRepository` | `new.go:149` | `requireAuditLedger` `account.go:49` |
| `NewEventReceiptRepositoryWithStore` | `new.go:155` | delegates to `NewEventReceiptRepository` (`event_receipt.go:59`) → guarded `:52` |
| `NewOperationRepository` | `new.go:157` | `requireAuditLedger` `operation.go:60` |
| `NewSyncJobRepository` | `new.go:158` | `requireAuditLedger` `sync_job.go:55` |
| `NewCallbackRepository` | `app/audit.go:40` (via `buildAuditStack`) | `requireAuditLedger` `callback.go:44` |
| `NewBlockRepository` | `new.go:144` | takes no ledger today — **this direction's step 5 wires it** (AC-2) |
| `NewAuditLedgerRepository` | concrete impl | no ledger parameter (nothing to require); can never be nil |

No bypass exists: `NewEventReceiptRepositoryWithStore` is the only delegating constructor and it
re-enters the guarded path. The inline `NewAuditLedgerRepository` in `new.go`'s
`WithSyncJobAuditLedger` is a fresh concrete instance, not a nil-able dependency.

**Hardening applied (H2):** the typed-nil reflect check previously covered only `reflect.Ptr`.
It now rejects every nil-able kind (`Chan, Func, Interface, Map, Ptr, Slice`), so a nil map/slice
implementation of `AuditLedgerTransactionalRepository` cannot smuggle a fail-open ledger past
wiring. `audit_wiring_test.go` now enumerates **all six** live constructors in both the nil and
typed-nil tests (NewCallbackRepository was missing), plus a nil-map-kind test
(`TestConstructorsRejectNilKindAudit`) proving the widened check.

### 8.2 F1 tenant-less identity → 500: no leak, bounded cost (not a DoS amplifier)

**Blast radius is config-dependent, and the default config makes it total (F-A).** `TenantFrom` is
empty for **every** request in the default no-JWKS deployment (`sso.jwks_url: ""`,
`configs/config.yaml:100`; `sso.Client.ValidateIdentity` `client.go:196-202` returns no TenantID),
so the flip turns every Block/Unblock into a deterministic 500/Internal on both the HTTP and gRPC
surfaces there — the feature goes fully offline, not intermittently (FM-2). The mitigation is the
§6 step-1 landing precondition: JWKS validation (or an equivalent tenant-resolution policy) must
be deployed and verified before the flip ships. The bounded-cost analysis below governs the
remaining tenant-less cases (precondition violated in a target environment, JWKS misconfiguration,
future tenant-less paths).

- **No internal leak.** `httpx.WriteError` (`httpx.go:30-40`) renders only
  `{code, message, request_id}`; the wrapped cause chain (`audit fact is incomplete`, DB detail)
  never reaches the client. The 500 body is
  `{"error":{"code":"block.block_failed","message":"failed to block user","request_id":"..."}}`.
  gRPC (`GRPCError`, `errors.go`) renders only `ae.Message`. Server-side logs may show the cause —
  that is not a client leak.
- **Bounded storm cost.** `AppendInTx` validates the fact **before** the advisory lock and any
  chain read (`audit_ledger.go`: `validateAuditFact` → `json.Marshal` → `appendInTx` →
  `pg_advisory_xact_lock`). A tenant-less block costs one `user_blocks` INSERT + rollback — no
  partition lock, no chain I/O, no row writes — strictly cheaper than a legitimate block. No state
  accumulates (all rollbacks), so a storm cannot grow memory, locks, or WAL.
- **No retry amplification.** The idempotency middleware releases 5xx reservations
  (`middleware/idempotency.go:42-43`), so retries re-execute — but each retry costs the same
  bounded INSERT+rollback, and Auth → Ban → RateLimit → Idempotency ordering gates every request.
  The per-actor rate limiter is opt-in (`config.yaml` `rate_limit.enabled: false` default) — a
  general ops note, not block-specific: any deployment that wants storm protection enables it.
- **Doc correction:** the §3.2 parenthetical ("partition becomes `system` for the tenant-less
  case") was imprecise. `normalizeAuditInput` falls back `accountID → actorUID → "system"`, so a
  tenant-less block with actor `a` would compute partition `"a"`, not `"system"`. Immaterial —
  `validateAuditFact` rejects the fact (empty `TenantID`) before any row write — but the fallback
  value never reaches the DB either way. Fixed in §3.2.

### 8.3 blockAuditFact parity with today's Log mapping is exact

Field-for-field comparison of today's durable fact (block's `Log` call → `audit_ledger.go:136-143`
mapping → `buildAuditFact`/`bindAuditContext`/`normalizeAuditInput`) versus the proposed
`blockAuditFact(ctx, actor, uid, action)`:

| Field | Today (`Log`) | Proposed (`blockAuditFact`) |
|---|---|---|
| `ActorUID` | actor | actor |
| `Action` | `user.block` / `user.unblock` | same |
| `ResourceType` | `SubjectType` = `"user"` | `"user"` |
| `ResourceID` | `SubjectID` = uid (`TargetUID` dropped by `Log`) | uid |
| `Payload` | `Meta` = nil → `redactAuditPayload(nil)` = `{}` | nil → `{}` |
| `Source` / `SourceRegion` | `"aero-id"` / `"local"` (defaults) | same |
| `TenantID` / partition | ctx tenant / `tenant:actor` | same (`bindAuditContext`) |
| `EventID` | unset | unset |
| `OccurredAt` | `CreatedAt` zero → now | now |
| `RequestID` | from ctx (`buildAuditFact`) | from ctx |

Identical. No credentials or request bodies can enter the fact in either path: the block HTTP
request body is only `{"uid": ...}` and the service never forwards it into the fact; `Payload` is
nil by construction. The only behavioral difference is intentional (FM-2): today a tenant-less
fact is rejected and swallowed with the block committing; after this direction it rolls back.

### 8.4 All seven in-tx swallow sites re-audited (no silent drop on commit)

The design's five-site list is stale by two: the sibling direction landed `operation_audit.go:136`
and `sync_job.go:116`, so the working tree has **seven** in-tx `ErrAuditDuplicate` swallow sites.
Every one was re-read in full:

`operation.go:182` · `operation_audit.go:136` · `callback_audit.go:49,73,101` ·
`event_receipt.go:180` · `sync_job.go:116` · `sync_job_audit.go:55`

All seven satisfy the invariant: (1) the domain write executes **first** inside the same tx; (2)
the swallow is strictly `err != nil && !errors.Is(err, ErrAuditDuplicate)`; (3) any non-duplicate
append failure returns an error and rolls back the whole tx — the domain row cannot commit without
its fact. `account_provision_audit.go` appends with **no** swallow (fail-closed; appended only when
the account row was actually created, so a duplicate is structurally impossible).

`ErrAuditDuplicate` production is precise: (a) `checkAuditDuplicate` pre-check (EventID set and a
row with the same `(tenant_id, source, event_id)` exists), or (b) a raw 23505 whose constraint
name is exactly `uq_audit_ledger_tenant_event`.

**Hardening applied (H1):** `createAuditRows` previously mapped `gorm.ErrDuplicatedKey` to
`ErrAuditDuplicate` as a second swallow path. It was dead today (`gorm.Config.TranslateError` is
off in both `db.Open` and `testpg`, and the postgres driver only translates when it is enabled),
but it was a silent-drop footgun: if TranslateError were ever enabled, **any** duplicate-key
failure — `audit_ledger` PK collision, `uq_audit_ledger_chain_seq` violation, publication PK —
would have been swallowed as `ErrAuditDuplicate` and the enclosing tx would commit the domain row
without its audit row. The broad branch is removed; only the constraint-precise 23505 mapping
remains, and every other failure stays raw and rolls back. Design AC-1b injection 2 (PK collision)
now returns the raw wrapped error rather than a swallowable one — the "error returned; zero new
rows" assertion is unchanged and stronger.

Remaining legacy `_ = s.audit.Log(...)` sites (`admin.go:93,120`, `message.go:313`, `ban.go:83,119`,
`comment.go:415`, `file_share.go:114,146`, unwired `internal/block/block.go:66,86`) are the
documented out-of-tx diagnostics carve-out (AGENTS.md; A1), not the in-tx regime — out of scope
for this contract, and `internal/service/block.go:54,77` is the pair this direction removes.
