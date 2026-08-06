# Requirements Specification — Make audit wiring fail-closed (internal/repository)

> **状态：已实现**（2026-08-06）— 见 `docs/plans/2026-08-06-*` 实现记录；门禁全绿。

- Module: `internal/repository`
- Source analysis: `docs/auto/analyses/internal-repository-0c7531e4.json` (direction 2 of 3)
- Direction: **Make audit wiring fail-closed: constructors silently discard the audit ledger when the passed `AuditLedgerRepository` lacks `AppendInTx`**
- Value: 8 · Risk reduction: 8 · Effort: 3 · Confidence: 9
- Status of this document: evidence re-verified against the repository at commit time; all acceptance checks preserved and made testable; scope limited to this direction.
- Corrections: this spec is amended to fold in design-review corrections C1–C5 (line citations, call-site count, AC1/AC3 recipes). §7 is the amendment register; where this document and the design (`docs/plans/2026-08-06-internal-repository-audit-wiring-fail-closed.md`) disagree, the design's §6 acceptance mapping and the corrected text below govern.

## 1. Problem statement

Four constructors accept an optional `...AuditLedgerRepository` and silently type-assert to
`AuditLedgerTransactionalRepository` (`transactional, _ = audit[0].(...)`). A repository implementation
that does not implement `AppendInTx` — a mock, a read-only wrapper, a future refactor of
`auditLedgerRepo`, or simply zero arguments — quietly disables the durable audit path for account,
operation, event-receipt, and sync-job mutations. The failure surfaces only later as a runtime error in
each `*WithAudit` method ("transactional audit repository is not configured"), never at wiring/startup.
This is a fail-open wiring hazard for a compliance ledger that AGENTS.md treats as authoritative evidence
("the compliance audit ledger is append-only and durable").

## 2. Verified evidence

Every cited symbol was re-checked against the current tree. Line numbers below are the live ones; where
the source analysis's line numbers drifted, the current location is given.

| # | Claim | Verified location (current tree) |
|---|---|---|
| E1 | `NewAccountRepository` accepts `audit ...AuditLedgerRepository` and silently type-asserts | `internal/repository/account.go:48-53` (`transactional, _ = audit[0].(AuditLedgerTransactionalRepository)` at :51) |
| E2 | `NewOperationRepository` — same pattern | `internal/repository/operation.go:59-63` (assert at :62) |
| E3 | `NewEventReceiptRepository` and `NewEventReceiptRepositoryWithStore` — same pattern; WithStore delegates through the base constructor | `internal/repository/event_receipt.go:51-55` (assert at :54), `:62-64` (`NewEventReceiptRepository(db, audit...)` at :63) |
| E4 | `NewSyncJobRepository` — same pattern | `internal/repository/sync_job.go:54-58` (assert at :57) |
| E5 | Interface split: `AppendInTx` is not part of the base `AuditLedgerRepository`; it lives only on `AuditLedgerTransactionalRepository` | `internal/repository/audit_ledger.go:19-26` (base: Append/List/VerifyChain/ChainPartitions/ChainHead), `:29-31` (transactional: AppendInTx only) — line-corrected with C1 |
| E6 | `NewAuditLedgerRepository` returns the **base** interface `AuditLedgerRepository`, so its static type cannot be passed to a non-variadic `AuditLedgerTransactionalRepository` parameter without a companion change | `internal/repository/audit_ledger.go:57` (**C1**: the function is at :57, not :47-48) |
| E7 | Runtime-only "not configured" errors — 11 guards, all reachable only at first `*WithAudit` call | `internal/repository/account_profile.go:69` and `:99` (analysis cited :53; file has drifted — same two guards, same error text), `operation.go:141,424,437,455`, `operation_audit.go:123` (`appendAudit` helper), `sync_job.go:92,294`, `sync_job_audit.go:41`, `event_receipt.go:96` |
| E8 | All live wiring passes the real repo → the hazard is latent, not an active bug | `internal/app/new.go:134` (`auditLedgerRepo := repository.NewAuditLedgerRepository(...)`), `:162` (`NewAccountRepository(gdb, auditLedgerRepo)`), `:168-170` (`NewEventReceiptRepositoryWithStore(gdb, store, auditLedgerRepo)`, `NewOperationRepository(gdb, auditLedgerRepo)`), `:171` (`NewSyncJobRepository(gdb, auditLedgerRepo)`), `:273` (second `NewAuditLedgerRepository` instance into `wireAuditServices`, which takes `repository.AuditLedgerRepository`) |
| E9 | `service.NewAuditLedgerService` consumes the base interface, so a combined interface remains assignable at the service boundary | `internal/service/audit_ledger.go:54` |
| E10 | Zero-arg construction is exercised today by 8 test files — **18** call sites, not ~16 (**C2**); these compile today and prove the fail-open behavior is reachable in test wiring | `internal/app/audit_integration_test.go:24`; `internal/repository/account_backfill_integration_test.go:26,37`; `event_order_integration_test.go:26,32,101,106`; `event_store_integration_test.go:107,113`; `legacy_cutover_integration_test.go:38`; `region_directory_integration_test.go:20,86`; `tenant_billing_integration_test.go:19,61`; `internal/event/nats_integration_test.go:66,72,218,224` (`NewAccountRepository(db)` / `NewEventReceiptRepository(db)` / `NewEventReceiptRepositoryWithStore(db, store)` with no audit arg) |
| E11 | The four structs are constructed only inside their own constructors (no direct struct literals elsewhere), so a constructor-level nil check makes `r.audit` provably non-nil | `account.go:53`, `operation.go:64`, `event_receipt.go:56`, `sync_job.go:59` (grep of `&accountRepo{` / `&operationRepo{` / `&eventReceiptRepo{` / `&syncJobRepo{` finds no other construction sites) |
| E12 | Baseline `go build ./...` passes before any change | verified locally |

## 3. Required change (scope boundary)

All changes are confined to `internal/repository` constructor wiring, the ledger constructor return type,
and mechanical call-site updates. No `*WithAudit` method signature, behavior, or error text changes.

### 3.1 Compile-time guarantee via non-variadic constructors

Change the four constructors to require `AuditLedgerTransactionalRepository` and reject nil/typed-nil
through one shared helper (C5):

```go
// requireAuditLedger fails closed at wiring time. A nil or typed-nil ledger
// would silently disable the transactional audit path for account, operation,
// event-receipt, and sync-job mutations.
func requireAuditLedger(name string, audit AuditLedgerTransactionalRepository) AuditLedgerTransactionalRepository {
    if audit == nil {
        panic("repository: " + name + " requires a non-nil AuditLedgerTransactionalRepository")
    }
    if v := reflect.ValueOf(audit); v.Kind() == reflect.Ptr && v.IsNil() {
        panic("repository: " + name + " requires a non-nil AuditLedgerTransactionalRepository")
    }
    return audit
}

func NewAccountRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) AccountRepository {
    return &accountRepo{db: db, audit: requireAuditLedger("NewAccountRepository", audit)}
}
```

Same shape for `NewOperationRepository` (operation.go), `NewSyncJobRepository` (sync_job.go), and
`NewEventReceiptRepository` (event_receipt.go). `NewEventReceiptRepositoryWithStore` keeps delegating to
`NewEventReceiptRepository` and needs no independent check. A typed-nil interface value (`var r
AuditLedgerTransactionalRepository = (*auditLedgerRepo)(nil)`) is `!= nil`, so the plain `audit == nil`
check alone would leave a typed-nil path that deep-panics inside the first `AppendInTx`; the helper's
`reflect` check closes it at wiring time (C5). A nil argument is the only remaining way to
mis-wire (a non-transactional implementation cannot be passed at all), and it must fail at construction
— the panic is raised during `new.go` wiring, i.e., at startup, before any handler or mutation.

Rationale for panic over `(T, error)`: the constructors return a single value and changing to a two-value
return would ripple through every call site without adding a reachable error state; the direction
explicitly permits "error/panic". The panic message must name the repository and the requirement so a
startup crash is self-explanatory.

### 3.2 Companion change: combined ledger interface (required for 3.1 to compile)

`NewAuditLedgerRepository` currently returns the base `AuditLedgerRepository` (E6), whose static type
cannot be assigned to the new non-variadic parameter. Add a combined interface and return it:

```go
// FullAuditLedgerRepository is implemented by the durable ledger repo: the
// read/verify surface plus the in-transaction append. NewAuditLedgerRepository
// returns this type so wiring the ledger into account/operation/receipt/sync-job
// repositories is checked at compile time.
type FullAuditLedgerRepository interface {
    AuditLedgerRepository
    AuditLedgerTransactionalRepository
}

func NewAuditLedgerRepository(db *gorm.DB, cfg AuditLedgerConfig) FullAuditLedgerRepository
```

This keeps every existing consumer compiling unchanged: `service.NewAuditLedgerService` (E9) and
`wireAuditServices` (E8) accept the base interface, and a value of the combined type is assignable to it.
`internal/app/new.go` therefore needs **no edits** — the same four call sites now get the compile-time
guarantee for free. `audit_ledger.go` is 472 lines today; this adds ~18 lines (combined interface +
`requireAuditLedger` helper, including the C5 typed-nil check and one stdlib `reflect` import) →
~490, still under the 500-line gate (AGENTS.md `make check-file-sizes`).

### 3.3 Call-site updates (mechanical, forced by the compiler)

The 18 zero-arg call sites in E10 (C2) must pass a real ledger (`repository.NewAuditLedgerRepository(db,
repository.AuditLedgerConfig{})`) or a minimal test fake implementing `AppendInTx`. These tests do not
exercise audit behavior and are unaffected functionally; the update is compile-only.

### 3.4 Runtime guards retained as defense-in-depth

The 11 "transactional audit repository is not configured" guards (E7) become unreachable once every
construction path rejects nil (E11) but are retained unchanged: they preserve the fail-closed error
contract for any future in-package construction path and cost nothing. No guard is removed, no error
text changes.

### 3.5 Explicitly out of scope

- No changes to `AuditLedgerRepository` / `AuditLedgerTransactionalRepository` method sets (the split
  itself is intentional and stays).
- No changes to hashing, verification, anchoring, publication, or replay behavior (separate direction in
  the same analysis).
- No new config knobs, no changes to `*WithAudit` method signatures, service layer, or handlers.

## 4. Acceptance criteria (preserved, made testable)

### AC1 — Construction fails on nil audit (unit test; fails today)

New unit tests (plain `go test`, no build tag), e.g. `internal/repository/audit_wiring_test.go`
(`package repository_test`):

- `NewAccountRepository(&gorm.DB{}, nil)`, `NewOperationRepository(&gorm.DB{}, nil)`,
  `NewEventReceiptRepository(&gorm.DB{}, nil)`, `NewSyncJobRepository(&gorm.DB{}, nil)`, and
  `NewEventReceiptRepositoryWithStore(&gorm.DB{}, nil, nil)` each panic with the message from §3.1.
- testify is **not** a module dependency (zero `testify` entries in `go.mod`; `assert.Panics` is used
  nowhere in the tree — C4), so the assertions use a stdlib `recover()` helper:

  ```go
  func mustPanic(t *testing.T, name string, fn func()) {
      t.Helper()
      defer func() {
          if recover() == nil {
              t.Fatalf("%s: expected panic on nil audit, construction succeeded", name)
          }
      }()
      fn()
  }
  ```

  `mustPanic` is sound here: the panic originates inside the constructor called within the closure,
  propagates to the deferred func, and `recover()` catches it; the no-panic path hits `t.Fatalf`.
- The constructor never touches `db` before the panic, so `&gorm.DB{}` is a valid argument.
- **Fails today:** currently all five calls silently succeed (E1-E4), so the tests are red before the
  change and green after. This is the "fails today / passes after" unit proof.
- Zero-argument construction is subsumed by AC2: post-change it is a compile error, so it cannot be
  expressed in a runtime test. Its fail-open reachability today is proven by E10 (the 8 test files
  compile with zero args today and will not compile after the change).

### AC2 — Compile-time check (signature change; verifiable by the done gate)

- The four constructors require `AuditLedgerTransactionalRepository` (non-variadic); passing any
  implementation that lacks `AppendInTx` (mock of the base interface, read-only wrapper, or a future
  refactor of `auditLedgerRepo`) is unrepresentable — a compile error, not a runtime hazard.
- `NewAuditLedgerRepository` returns `FullAuditLedgerRepository` so `new.go` and all tests keep
  compiling with the real ledger.
- Gate: `make build`, `make lint` (go vet), `make lint-strict`, and `make test` (which compiles every
  `_test.go`) all pass with the updated call sites. This replaces the analysis's "go build ./... and
  make lint pass" with the repo's full done gate, plus `make test-integration` (see AC3).

### AC3 — Durable, same-transaction audit facts (integration test; `make test-integration`)

New integration test `internal/repository/audit_wiring_integration_test.go` (`//go:build integration`,
`testpg.Start` + `testpg.Migrate`, real `NewAuditLedgerRepository` with a test signing key), following
the recipes already used by `internal/repository/event_order_integration_test.go`.

Authoring notes (C3-corrected mechanics): the `*WithAudit` methods live on the exported audit
interfaces — `AccountProfileAuditRepository` (`account.go:39`), `OperationAuditRepository` /
`OperationStateAuditRepository` (`operation.go:35,39`), `EventSnapshotAuditRepository`
(`event_receipt.go:38`) — not on the base `AccountRepository`/`OperationRepository`; in `package
repository_test` the test must type-assert, e.g. `repository.NewAccountRepository(db,
ledger).(repository.AccountProfileAuditRepository)`. `UpdateProfileWithAudit` is FK-bound to
`account_profiles.account_id` (account_profile.go:67), so seed a real `accounts` row via
`EnsureByIdentity` (account_identity.go:20) and assert rollback on the profile's display name, not on
row absence (the seed row always exists).

1. **Happy path per method** — wire the real ledger through the new signatures and assert each call
   produces exactly one committed `audit_ledger` row whose `source`/`action`/`tenant_id`/`event_id`
   match the supplied `domain.AuditFact`:
   - `accountRepo.UpdateProfileWithAudit` (account mutation),
   - `operationRepo.CreateWithAudit` (operation mutation),
   - `operationRepo.FinishStepWithAudit` (operation-state mutation; requires a claimed step first,
     reusing the ClaimNextStep flow),
   - `eventReceiptRepo.ApplySnapshotEventWithAudit` (event-receipt mutation; build envelope +
     `SnapshotMutation` as in `event_order_integration_test.go`).
2. **Rollback atomicity** — inject `ErrAuditDuplicate` via a **fresh** mutation whose fact carries an
   `EventID` already committed in the ledger (C3). Re-issuing `FinishStepWithAudit` on the *same* step
   is NOT a valid injection path: `finishStep` guards with `WHERE id = ? AND status = 'running' AND
   lease_owner = ?` (`operation_audit.go:13-31`) and returns `ErrOperationStepLeaseLost` before the
   audit append is reached — the duplicate never gets to `appendInTx`. Corrected recipes:
   - *Profile duplicate:* `UpdateProfileWithAudit(profile1, fact{EventID: e1})` commits; a second
     `UpdateProfileWithAudit(profile2, fact{EventID: e1})` (same `(tenant_id, source, event_id)`,
     different display name) returns an error wrapping `ErrAuditDuplicate` (`audit_ledger.go`
     duplicate check inside `appendInTx`), which aborts the repository's transaction; assert the
     error, profile2's display name unchanged (the upsert rolled back), and exactly one `e1` row in
     the ledger.
   - *Step duplicate:* claim step A, `FinishStepWithAudit(stepA, fact{EventID: e3})` commits; claim
     step B (claimable once A is finished — `ClaimNextStep`'s NOT EXISTS predecessor guard), then
     `FinishStepWithAudit(stepB, fact{EventID: e3})` returns an error wrapping `ErrAuditDuplicate`;
     assert step B is still `running` with its worker lease (the UPDATE rolled back) and exactly one
     `e3` row in the ledger.
   This is the testable form of "rollback the tx and assert no fact remains" using only public API: the
   audit append is the last step in each `*WithAudit` transaction, so a failure *during* the append is
   the only externally injectable post-append failure, and it must leave no partial state — atomicity
   in both directions.

### AC4 — Mis-wiring fails fast at startup (no silent audit-off state)

- Non-transactional impl: compile error at every wiring site (AC2) — cannot reach `new.go`.
- Nil/absent ledger: constructor panic raised while `new.go:162,168-171` executes, i.e., during
  startup, before any handler serves or any mutation runs (AC1 proves the panic; `new.go` passes the
  real ledger from `:134`).
- There is no config path that can produce a silently audit-disabled repository: `make run`/`make
  build` with correct wiring is unchanged; any mis-wiring is rejected by the compiler or the
  constructor. The previous failure mode — first mutation returning "transactional audit repository is
  not configured" — becomes unreachable.

## 5. Implementation checklist

1. `internal/repository/audit_ledger.go` — add `FullAuditLedgerRepository`; change
   `NewAuditLedgerRepository` return type.
2. `internal/repository/account.go:48-53`, `operation.go:59-63`, `event_receipt.go:51-55`,
   `sync_job.go:54-58` — non-variadic parameter + nil panic; delete the `transactional, _ =` asserts.
3. Update the 18 zero-arg call sites (E10 list; C2) to pass `NewAuditLedgerRepository(db,
   AuditLedgerConfig{})` or a fake.
4. Add `internal/repository/audit_wiring_test.go` (AC1).
5. Add `internal/repository/audit_wiring_integration_test.go` (AC3).
6. Gates: `make test && make lint && make lint-strict && make check-file-sizes && make build`, then
   `make test-integration` (repository wiring is in the integration gate per AGENTS.md).

## 6. Risks and notes

- **Behavior contract unchanged:** the error text "transactional audit repository is not configured"
  remains in the code (now unreachable) and is not part of any public API contract; callers of
  `*WithAudit` methods observe no signature or behavior change.
- **Test-only churn is compiler-forced:** every E10 call site fails to compile after AC2 until updated;
  none of those tests exercise audit behavior, so the updates are strictly mechanical.
- **File-size gate:** `audit_ledger.go` grows ~18 lines (472 → ~490, combined interface +
  `requireAuditLedger` helper), safely under 500; no split needed.
- **No config or migration changes** are required by this direction.

## 7. Amendment register (corrections C1–C5, folded from design review)

This register records every correction between this spec and the design
(`docs/plans/2026-08-06-internal-repository-audit-wiring-fail-closed.md`). The original (stale)
wording is preserved here so no future implementation gate or design-review re-flags it as a
regression; the corrected text in the sections referenced below is authoritative.

| # | Stale wording (original) | Correction | Folded into |
|---|---|---|---|
| C1 | E5 cited `audit_ledger.go:36-44`; E6 cited `audit_ledger.go:47-48` | `NewAuditLedgerRepository` is at `audit_ledger.go:57`; base interface at `:19-26`, transactional `AppendInTx` at `:29-31` (all re-verified live) | §2 E5/E6 rows |
| C2 | E10, §3.3, and checklist item 3 said "~16 call sites" | Exactly **18** zero-arg call sites in 8 files (per-file: `audit_integration` 1, `account_backfill` 2, `event_order` 4, `event_store` 2, `legacy_cutover` 1, `region_directory` 2, `tenant_billing` 2, `nats` 4) | §2 E10, §3.3, §5.3 |
| C3 | AC3 re-issued `FinishStepWithAudit` on the *same* step to inject `ErrAuditDuplicate` | Unreachable: `finishStep`'s lease guard `WHERE id = ? AND status = 'running' AND lease_owner = ?` (`operation_audit.go:13-31`) returns `ErrOperationStepLeaseLost` before the audit append. Corrected testable recipes: (a) profile duplicate — second `UpdateProfileWithAudit` with an already-committed `event_id`; (b) step duplicate — claim step B after step A finishes, then `FinishStepWithAudit(stepB, same event_id)`. Both proven to reach `appendInTx` and roll back, empirically, against real Postgres | §4 AC3 (incl. interface type-assert + `EnsureByIdentity` seeding notes) |
| C4 | AC1 mandated `assert.Panics` | testify is not a module dependency (zero `testify` entries in `go.mod`; `assert.Panics` used nowhere in the tree). Use the stdlib `recover()` `mustPanic` helper | §4 AC1 |
| C5 | §3.1 rejected only `audit == nil` | A typed-nil interface value is `!= nil`; the shared `requireAuditLedger` helper adds a `reflect` typed-nil check so the "guards provably unreachable" claim is sound (the 11 runtime guards stay as defense-in-depth for future in-package literals) | §3.1, §3.4 |
