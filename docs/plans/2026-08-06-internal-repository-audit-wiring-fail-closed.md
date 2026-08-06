# Design — internal/repository: make audit wiring fail-closed

| | |
|---|---|
| Module | `internal/repository` |
| Input spec | `docs/requirements/2026-08-06-internal-repository-audit-wiring-fail-closed.md` (AC1..AC4) |
| Date | 2026-08-06 |
| Status | **Implemented** |

## 0. Evidence verification status

The evidence summary and the requirements spec were re-checked against the live tree. The direction is
accurate. Four corrections are folded into this design (C1–C5):

| # | Claim | Verification |
|---|---|---|
| E1–E4 | Four constructors accept `audit ...AuditLedgerRepository` and silently type-assert (`transactional, _ = audit[0].(AuditLedgerTransactionalRepository)`) | ✅ `account.go:48-53`, `operation.go:59-63`, `event_receipt.go:51-55` (+ `WithStore` delegation at `:62-64`), `sync_job.go:54-58` |
| E5 | `AppendInTx` lives only on `AuditLedgerTransactionalRepository`, not the base interface | ✅ `audit_ledger.go:29-31` vs base `:19-26` (Append/List/VerifyChain/ChainPartitions/ChainHead) |
| E6 | `NewAuditLedgerRepository` returns the **base** interface | ✅ Function is at `audit_ledger.go:57`, not `:47` (**C1**); substance unchanged |
| E7 | 11 runtime-only "not configured" guards | ✅ `account_profile.go:69,99`; `operation.go:141,424,437,455`; `operation_audit.go:123` (`appendAudit`); `sync_job.go:92,294`; `sync_job_audit.go:41`; `event_receipt.go:96` — all verified verbatim |
| E8 | Live wiring passes the real ledger (hazard is latent) | ✅ `new.go:134` → `:162` (`NewAccountRepository`), `:168-170` (`WithStore`, `NewOperationRepository`), `:171` (`NewSyncJobRepository`), `:273` (second ledger into `WithSyncJobAuditLedger`); `wireAuditServices` (`internal/app/audit.go:17`) takes `repository.AuditLedgerRepository` |
| E9 | `service.NewAuditLedgerService` consumes the base interface | ✅ `internal/service/audit_ledger.go:54` |
| E10 | Zero-arg construction exercised in test wiring | ✅ 8 test files, **18** call sites, not ~16 (**C2**): `internal/app/audit_integration_test.go:24`; `internal/repository/account_backfill_integration_test.go:26,37`; `event_order_integration_test.go:26,32,101,106`; `event_store_integration_test.go:107,113`; `legacy_cutover_integration_test.go:38`; `region_directory_integration_test.go:20,86`; `tenant_billing_integration_test.go:19,61`; `internal/event/nats_integration_test.go:66,72,218,224` |
| E11 | The four structs are constructed only inside their own constructors | ✅ grep of `&accountRepo{`/`&operationRepo{`/`&eventReceiptRepo{`/`&syncJobRepo{` finds exactly the 4 constructor sites |
| E12 | Baseline `go build ./...` passes | ✅ verified locally |

Additional verified facts that shape the design:

- `createAuditRows` signs only `if len(r.signingKey) > 0` (`audit_ledger.go:185`) → test wiring may use
  `AuditLedgerConfig{}` with an empty key; `Append`/`AppendInTx` never panic or fail on it.
- `checkAuditDuplicate` keys on `(tenant_id, source, event_id)` and runs **inside** `appendInTx`
  (`audit_ledger.go:168-180`, `ErrAuditDuplicate` raised at `:175`) → `ErrAuditDuplicate` is a real, externally injectable post-mutation
  failure inside every `*WithAudit` transaction, which is what makes the AC3 atomicity test possible.
- None of the E10 test files call any `*WithAudit` method → the 18 call-site updates are functionally
  inert, compile-only churn.
- **C3 (design flaw in spec AC3):** `finishStep` guards with
  `WHERE id = ? AND status = 'running' AND lease_owner = ?` (`operation_audit.go:13-31`) and returns
  `ErrOperationStepLeaseLost` on miss. Re-issuing `FinishStepWithAudit` on the **same** step (as AC3
  literally words it) fails at the lease guard **before** the audit append is reached, so
  `ErrAuditDuplicate` is unreachable that way. The rollback test must inject the duplicate via a fresh
  mutation whose fact duplicates an already-committed `event_id` (see §5).
- **C4 (spec/implementation mismatch):** testify is not in `go.mod` and `assert.Panics` is used nowhere
  in the tree. AC1's panic tests must use a stdlib `recover()` helper.
- **C5 (unsound "provably unreachable" claim):** a **typed-nil** interface value (`var r
  AuditLedgerTransactionalRepository = (*auditLedgerRepo)(nil)`) is `!= nil`, so `if audit == nil` alone
  does not make the 11 guards provably unreachable. The design closes this with a typed-nil check in the
  shared constructor helper (§1.1).

## 1. Design

### 1.1 Constructor hardening (D1)

All four constructors take a required, non-variadic `AuditLedgerTransactionalRepository` and delegate
the nil/typed-nil check to one shared helper in `audit_ledger.go`:

```go
// FullAuditLedgerRepository is implemented by the durable ledger repo: the
// read/verify surface plus the in-transaction append. NewAuditLedgerRepository
// returns this type so wiring the ledger into account/operation/receipt/sync-job
// repositories is checked at compile time (D2).
type FullAuditLedgerRepository interface {
    AuditLedgerRepository
    AuditLedgerTransactionalRepository
}

// requireAuditLedger fails closed at wiring time. A nil or typed-nil ledger would
// silently disable the transactional audit path for account, operation,
// event-receipt, and sync-job mutations.
func requireAuditLedger(name string, audit AuditLedgerTransactionalRepository) AuditLedgerTransactionalRepository {
    if audit == nil {
        panic("repository: " + name + " requires a non-nil AuditLedgerTransactionalRepository")
    }
    v := reflect.ValueOf(audit)
    if v.Kind() == reflect.Ptr && v.IsNil() {
        panic("repository: " + name + " requires a non-nil AuditLedgerTransactionalRepository")
    }
    return audit
}
```

Constructor shapes (per file):

```go
// account.go
func NewAccountRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) AccountRepository {
    return &accountRepo{db: db, audit: requireAuditLedger("NewAccountRepository", audit)}
}
// operation.go, sync_job.go — identical shape with their own names.
// event_receipt.go
func NewEventReceiptRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) EventReceiptRepository {
    return &eventReceiptRepo{db: db, audit: requireAuditLedger("NewEventReceiptRepository", audit)}
}
func NewEventReceiptRepositoryWithStore(db *gorm.DB, events EventStoreRepository, audit AuditLedgerTransactionalRepository) EventReceiptRepository {
    repo := NewEventReceiptRepository(db, audit).(*eventReceiptRepo)
    repo.events = events
    return repo
}
```

The silent `transactional, _ = audit[0].(...)` type assertions are deleted — a value that does not
implement `AppendInTx` can no longer be passed at all. The constructors never touch `db` before the
check, so AC1's `&gorm.DB{}` argument is valid.

Panic over `(T, error)`: the constructors return a single value today; a two-value return would ripple
through every call site without adding a reachable error state. A nil dependency is a programmer error
and must stop the process at startup (the fail-closed position for a compliance ledger).

### 1.2 Companion change: combined ledger interface (D2)

```go
func NewAuditLedgerRepository(db *gorm.DB, cfg AuditLedgerConfig) FullAuditLedgerRepository {
    return &auditLedgerRepo{db: db, signingKey: []byte(cfg.SigningKey)}
}
```

Every existing consumer of the return value takes the base interface, so widening is a drop-in:

- `service.NewAuditLedgerService(repo repository.AuditLedgerRepository, ...)` — `service/audit_ledger.go:54`
- `wireAuditServices(..., auditLedgerRepo repository.AuditLedgerRepository, ...)` — `app/audit.go:17`
- `service.WithSyncJobAuditLedger(ledger repository.AuditLedgerRepository)` — `service/sync_job.go:85`
- `repository/audit_hold_integration_test.go:78`, `internal/app/audit_integration_test.go:18` (method
  calls on the value, no interface assignment)

`internal/app/new.go` therefore needs **zero edits**: the same four call sites now get the
compile-time guarantee for free, and a startup nil-panic if anyone later deletes the ledger argument.

### 1.3 Call-site migration (D3)

The 18 zero-arg test call sites (E10) are updated mechanically to

```go
ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
accounts := repository.NewAccountRepository(db, ledger)
// ... NewEventReceiptRepository(db, ledger) / NewEventReceiptRepositoryWithStore(db, store, ledger)
```

Each site already has a real `testpg` DB handle in scope and none exercises audit behavior (verified),
so an empty signing key is safe. A shared `ledger :=` line per test function is preferred over inline
construction where the file has several call sites (e.g. `event_order_integration_test.go`).

### 1.4 Runtime guards retained (D4)

All 11 "transactional audit repository is not configured" guards stay byte-for-byte unchanged. With D1
they are unreachable from every construction path (E11 + typed-nil check), but they preserve the
fail-closed error contract for any future in-package struct literal.

### 1.5 Explicitly out of scope

- No change to `AuditLedgerRepository`/`AuditLedgerTransactionalRepository` method sets.
- No change to hashing, verification, anchoring, publication, or replay behavior.
- No change to any `*WithAudit` method signature, behavior, or error text.
- No config knobs, no DB migrations, no protobuf changes, no service/handler changes.

## 2. API changes

| Symbol | Before | After |
|---|---|---|
| `NewAccountRepository(db, audit ...AuditLedgerRepository)` | variadic, silent drop | `(db *gorm.DB, audit AuditLedgerTransactionalRepository)`; panics on nil/typed-nil |
| `NewOperationRepository` | same | same shape |
| `NewSyncJobRepository` | same | same shape |
| `NewEventReceiptRepository` | same | same shape |
| `NewEventReceiptRepositoryWithStore(db, events, audit ...AuditLedgerRepository)` | variadic | `(db, events, audit AuditLedgerTransactionalRepository)`; delegates, no own check |
| `NewAuditLedgerRepository(db, cfg)` | returns `AuditLedgerRepository` | returns `FullAuditLedgerRepository` (new exported combined interface) |
| 11 runtime guards | reachable | unreachable, retained unchanged |
| All repository *interfaces* (`AccountRepository`, `OperationRepository`, `SyncJobRepository`, `EventReceiptRepository`, `*AuditRepository`) | — | **unchanged** |

Service-layer contract is untouched: `internal/service/{operation_audit,sync_audit,event_ingest,account_profile,sync_job}.go` type-assert `s.repo.(repository.XxxAuditRepository)` on the interface, not the constructor, so they compile and behave identically.

## 3. Compatibility constraints

1. **Source compatibility is intentionally broken for internal wiring callers only:** any call passing
   zero audit args or a non-transactional implementation fails to compile. The full set of affected
   callers is known: `new.go` (4 sites, stay compiling via D2) + 18 test sites (updated by D3). No
   third-party code can be affected (`internal/` packages, module-internal).
2. **Behavioral compatibility:** every shipped `*WithAudit` method keeps its signature, error text, and
   transaction structure. Callers observe no difference for correctly wired repositories.
3. **Binary/rolling deploy compatibility:** no schema, config, or wire changes; old and new binaries
   interop freely during rollout. The only new startup behavior is a panic if an instance is wired
   without a ledger — which cannot happen with the shipped `new.go`.
4. **File-size gate:** `audit_ledger.go` grows ~+18 lines (472 → ~490) including the helper and
   combined interface; all other touched files shrink (assert blocks deleted). All stay ≤ 500
   (`make check-file-sizes`). `requireAuditLedger` is one small function (≤ 50 lines, cyclop-safe).
5. **Test-only churn is compiler-forced:** the done gate compiles all `_test.go`, so D3 cannot be
   forgotten — the build fails until every site is migrated.

## 4. Failure modes

| # | Mode | Before | After | Handling |
|---|---|---|---|---|
| F1 | Zero-arg or base-interface-only ledger passed at wiring | Silent `r.audit = nil`; first `*WithAudit` mutation returns "not configured" at runtime (possibly in production) | Compile error at every call site | D1 + D3; AC2 |
| F2 | `nil` passed explicitly | Silent success | Constructor panic during `new.go` wiring — process fails at startup with a self-explanatory message | D1; AC1, AC4 |
| F3 | Typed-nil passed (`var r T = (*T)(nil)`) | `audit == nil` false; first `AppendInTx` panics deep inside a mutation transaction | Constructor panic at startup | D1 typed-nil check (C5) |
| F4 | Future in-package struct literal without audit | nil audit, runtime guard error | Same (guards retained) | D4 — defense-in-depth |
| F5 | Ledger wired but internally misconfigured (wrong DB, empty signing key) | N/A | N/A (undetectable at compile time; out of scope) | Existing `ErrAuditDuplicate`, chain verification, integration coverage |
| F6 | A test passes nil to a constructor | Test silently runs audit-disabled | Panic (test fails loudly) | AC1 asserts exactly this |
| F7 | Rollout with mixed binaries | N/A | N/A — no wire/schema change; wiring is identical | §3.3 |

Residual accepted risk: F5 (a *valid* transactional implementation that is semantically wrong) is not
representable as a compile error and is outside this direction's scope.

## 5. Migration steps (no DB migration)

1. **`internal/repository/audit_ledger.go`** — add `FullAuditLedgerRepository`, add
   `requireAuditLedger` (with `reflect` import), change `NewAuditLedgerRepository` return type.
2. **`internal/repository/account.go:48-53`**, **`operation.go:59-63`**, **`sync_job.go:54-58`**,
   **`event_receipt.go:51-55,62-64`** — non-variadic parameter, delete the silent asserts, call
   `requireAuditLedger`.
3. **18 test call sites** (E10 list) — pass `NewAuditLedgerRepository(db, AuditLedgerConfig{})`.
   `go build ./...` and `go vet` guide this mechanically.
4. **`internal/repository/audit_wiring_test.go`** (new, `package repository_test`, no build tag) —
   AC1 panic tests, stdlib helper:

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

   func TestConstructorsRejectNilAudit(t *testing.T) {
       db := &gorm.DB{}
       mustPanic(t, "NewAccountRepository", func() { repository.NewAccountRepository(db, nil) })
       mustPanic(t, "NewOperationRepository", func() { repository.NewOperationRepository(db, nil) })
       mustPanic(t, "NewSyncJobRepository", func() { repository.NewSyncJobRepository(db, nil) })
       mustPanic(t, "NewEventReceiptRepository", func() { repository.NewEventReceiptRepository(db, nil) })
       // WithStore delegates; one panic path suffices.
       mustPanic(t, "NewEventReceiptRepositoryWithStore", func() {
           repository.NewEventReceiptRepositoryWithStore(db, nil, nil)
       })
   }
   ```

   Red today (all five silently succeed), green after D1.
5. **`internal/repository/audit_wiring_integration_test.go`** (new, `//go:build integration`,
   `testpg.Start` + `testpg.Migrate`, recipes from `event_order_integration_test.go`) — AC3, with the
   corrected duplicate-injection mechanics (C3):

   **5a. Happy path — exactly one committed `audit_ledger` row per `*WithAudit` method:**
   - `accountRepo.UpdateProfileWithAudit(profile, fact{EventID: e1, Source: "aero-im", TenantID: "t", ...})`
     → assert 1 row; `event_id = e1`, `action`/`tenant_id` match the fact.
   - `operationRepo.CreateWithAudit(op, fact{EventID: e2, ...})` → assert 1 row; operation + steps
     committed.
   - `operationRepo.FinishStepWithAudit` — claim step via `ClaimNextStep`, then finish with
     `fact{EventID: e3}` → assert 1 row and step `status`/`lease_owner` updated.
   - `eventReceiptRepo.ApplySnapshotEventWithAudit(envelope, mutation, fact{EventID: e4})` → assert
     1 row plus the receipt row committed (envelope/mutation built as in `event_order_integration_test.go`).
   - Query helper: `SELECT count(*) FROM audit_ledger WHERE event_id = ?`.

   **5b. Rollback atomicity — inject `ErrAuditDuplicate` via a fresh mutation, never a re-issue:**
   - *Profile:* `UpdateProfileWithAudit(profile, fact{EventID: e1})` succeeds; re-run
     `UpdateProfileWithAudit(profile2, fact{EventID: e1})` (same `(tenant, source, event_id)`, different
     display name) → error wrapping `repository.ErrAuditDuplicate`; assert the profile still has the
     first call's values and the ledger has exactly one `e1` row.
   - *Operation step:* claim step A, `FinishStepWithAudit(stepA, fact{EventID: e3})`; claim step B,
     `FinishStepWithAudit(stepB, fact{EventID: e3})` → error; assert step B is still `running` with its
     worker lease (the UPDATE rolled back), and the ledger has exactly one `e3` row.
   - Do **not** re-issue on the same step: it is intercepted by `ErrOperationStepLeaseLost` before the
     audit append (C3) and would test the wrong guard.
6. **Gates:** `make test && make lint && make lint-strict && make check-file-sizes && make build`,
   then `make test-integration` (repository wiring is in the integration gate per AGENTS.md).

## 6. Testable acceptance mapping

| Acceptance criterion (spec) | Concrete test | Gate | Red today? |
|---|---|---|---|
| AC1 — construction fails on nil audit | `audit_wiring_test.go` `TestConstructorsRejectNilAudit` (5 `mustPanic` cases, stdlib `recover()` — C4) | `make test` | ✅ red (all five silently succeed) |
| AC2 — compile-time check (non-variadic param + combined interface) | No runtime test possible by construction; proven by every E10 site and `new.go` compiling after D1/D2/D3 | full done gate (`make test && make lint && make lint-strict && make check-file-sizes && make build`) | ✅ today's zero-arg sites compile (fail-open) and will not after the change |
| AC3 — durable same-transaction facts + rollback atomicity | `audit_wiring_integration_test.go`: 4 happy-path row assertions (5a) + duplicate-injection rollback on profile and step (5b, corrected mechanics C3) | `make test-integration` | ✅ (construction with ledger works today; the test is new) |
| AC4 — mis-wiring fails fast at startup | Non-transactional impl: compile error (AC2). Nil/typed-nil: constructor panic inside `new.go` wiring (AC1 proves panic; `new.go` passes the real ledger at `:134`, so startup is unaffected). No config path can produce an audit-disabled repo | done gate + code inspection of `new.go` (zero edits) | ✅ today mis-wiring is a first-mutation runtime error |

## 7. Risks and notes

- **Behavior contract unchanged:** the "transactional audit repository is not configured" text remains
  (unreachable) and is not part of any public API contract.
- **D1 typed-nil check (C5) is the only deviation from the spec beyond line/count corrections:** it is
  required for the "guards provably unreachable" claim to be sound and costs 4 lines in one helper.
- **`reflect` import in `audit_ledger.go`:** the only new dependency; standard library, used solely in
  the wiring-time helper.
- **No config or migration changes.** `make run` behavior with correct wiring is byte-identical.
