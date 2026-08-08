# Requirements — Consolidate audit write paths into the business transaction; make drainPublications the sole delivery path (B2 items 1+2)

| | |
|---|---|
| Module (batch label) | `configs` (direction scope: `internal/service` + `internal/repository` audit write/delivery path; Makefile gate; no new config keys) |
| Source analysis | `docs/auto/analyses/configs-1fa2a926.json` (direction 1 of 3) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery path + swallowed-call ban) |
| Related artifact | `docs/requirements/2026-08-06-audit-record-single-delivery-path-and-appendintx-consolidation.md` (sibling `cmd/migrate` batch; splits the swallow-gate into a separate direction — this spec keeps the gate inside T-10.2 as supplied) |
| Date | 2026-08-06 |
| Status | Draft — every cited symbol re-verified against the live tree |

## 1. Evidence verification status

All claims from the direction were re-checked against the repository. **Every cited symbol exists**; two
nuances (N1, N2) and one incompleteness (N3) are folded in and shape the requirements below.

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `internal/service/audit_ledger.go:66-80` — `Record` appends via `repo.Append` in a standalone tx, then runs an inline `for _, publisher := range s.publishers { _ = publisher.Publish(ctx, fact) }` loop | ✅ exact. `Record` at `:66`; `s.repo.Append(ctx, fact)` at `:67`; `ErrAuditDuplicate → return nil` at `:70-72`; `ObserveAuditIngestFailure` at `:73-75`; loop `for _, publisher := range s.publishers` at `:77-78`; swallow `_ = publisher.Publish(ctx, fact)` at `:79` |
| E2 | `internal/service/callback_audit.go:47` — `drainPublications` worker claims and publishes `audit_publications` rows | ✅ exact. `drainPublications` at `:47`; claims via `ClaimPublication(ctx, worker, 5m, 50)` at `:52-54`; P6-06 bulkhead fan-out; `finishPublicationAttempt` at `:61-77`; `publishAuditFact` (enqueue deliveries + per-tenant-bound publisher) at `:97-113` |
| E3 | `internal/service/callback.go:267` — worker ticker invokes `drainPublications` | ✅ exact. `s.drainPublications(workerCtx)` inside `Start` ticker at `:267`, immediately followed by `s.drain(workerCtx)` |
| E4 | Six swallowed account-domain sites: `account.go:155,184`; `account_provision.go:168`; `callback.go:160,221,247` | ✅ all six exact, all of the form `_ = s.audit.Record(ctx, ...)`. Facts: `account.viewed` (`:155`), `account.listed` (`:184`), `account.provisioned` (`:168`, invoked at `account_provision.go:123,133` after the repo write), `callback.registered` (`:160`), `callback.deleted` (`:221`), `callback.delivery.replayed` (`:247`) |
| E5 | `internal/repository/audit_ledger.go:39,123` — `AppendInTx` exists | ✅ exact. `AuditLedgerTransactionalRepository` interface at `:38-40`; `func (r *auditLedgerRepo) AppendInTx` at `:123`; `Append` (standalone `Transaction`) at `:107-121`; both funnel into `appendInTx` (`:140`) → `createAuditRows` (`:189-201`) which creates **both** the `audit_ledger` row and a `status='pending'` `audit_publications` row in the same tx |
| E6 | `internal/repository/event_receipt.go:180`, `account_profile.go:101` — in-tx append precedent | ✅ verbatim. `event_receipt.go:180`: `r.audit.AppendInTx(ctx, tx, fact)` ignoring `ErrAuditDuplicate`; `account_profile.go:101`: `return r.audit.AppendInTx(ctx, tx, fact)` at the end of the profile-update `Transaction` |
| E7 | Operation service falls back to `Record` when the repo lacks the audit interface (`operation_audit.go:44-47, 74-78, 119-123`) | ✅ present at `operation_audit.go:42` (`finishStep`), `:71` (`setOperationStatus`), `:86` (`finishCompensationStep`) → `recordOperationAudit` (`:89-95`) → `return s.audit.Record(...)` at `:94`. **Note:** `operationRepo` already implements both audit interfaces (`operation.go:135` `CreateWithAudit`, `:417` `FinishStepWithAudit`, `:430` `FinishCompensationStepWithAudit`, `:448` `SetStatusWithAudit`), so in the production wiring the fallback is dead; it remains a latent second write path for any repo/fake that lacks the interfaces |
| E8 | Makefile has no `check-audit-swallow` target | ✅ exact. The only `audit` lines in the Makefile are the proto enumeration (`:58`) and a soak comment (`:114`); no audit gate exists |
| E9 | One `audit_publications` row per fact; ledger and publication commit atomically | ✅ `createAuditRows` (`audit_ledger.go:189-201`) plus `migrations/000045_audit_publications.up.sql`: `fact_id uuid NOT NULL REFERENCES audit_ledger(id) ON DELETE CASCADE`, `CONSTRAINT uq_audit_publication_fact UNIQUE (fact_id)`. A rolled-back ledger row therefore leaves no orphan publication row |

Additional verified facts that shape the requirements:

- **N1 — the inline loop is unwired in production, so double delivery is latent, not live.** The composition
  root `internal/app/new.go:146` calls `service.NewAuditLedgerService(auditLedgerRepo)` with **zero**
  publishers; `internal/app/audit_integration_test.go:19` and `internal/service/audit_ledger_test.go:43,56`
  likewise. The governance publisher is constructed by `buildAuditPublishers` (`internal/app/audit.go:38`,
  gated on `cfg.AuditGovernance.Enabled` — `configs/config.yaml:174`, default `false`) and wired only into
  the callback service (`wireAuditServices` at `audit.go:17` → `callbackSvc.SetAuditPublishers`).
  `callbackService.Publish` already no-ops when the publication repo is wired
  (`callback_audit.go:19-25`, guard at `:23`). The defect is therefore: the dual path exists by design and
  any wiring that passes publishers to `NewAuditLedgerService` (tests, future composition roots) would
  deliver concurrently with `drainPublications` and swallow errors. The **live** defect is the standalone-tx
  append (E4): a rolled-back business mutation still leaves its `audit_ledger` + `audit_publications` rows.
- **N2 — `account.viewed`/`account.listed` are read-path facts with no business transaction.** `GetAccount`
  (`account.go:155`) and `ListAccounts` (`account.go:184`) perform no account-row write, so "commit in the
  same tx as their account row" is only realizable for `account.provisioned` (whose `EnsureByIdentity`
  write is at `repository/account_identity.go:20-44`). For the two read facts the testable obligations are:
  (a) they append through the sole append path (no inline publish), and (b) their append errors are not
  swallowed (the item-2 ban). This is the only place the supplied acceptance wording exceeds what the code
  can express; REQ-3 and T-10.1 below make each obligation concrete.
- **N3 — the gate's own pattern flags three additional same-class sites not enumerated in the direction.**
  A grep for `_ =` audit/publication calls in the live service layer also matches `audit_hold.go:59`
  (`audit_hold.set`), `audit_hold.go:74` (`audit_hold.removed`), `legacy_cutover.go:88`
  (`legacy.cutover.set`), and `callback_audit.go:81` (`_ = s.publications.FinishPublication(...)` — a
  worker status write whose lease-lost/error result is dropped). REQ-6 requires the gate to go green, so
  these four must be converted with the same mechanical change; they are gate-driven follow-through of the
  direction's own pattern, not new feature scope. Legacy `_ = s.audit.Log(...)` diagnostics swallows
  (`internal/service/{block,admin,message,comment,file_share,ban}.go` plus the unwired
  `internal/{file,block,comment,message}` trees) are deliberately excluded — `Log` is the legacy
  diagnostics surface per AGENTS.md, not the compliance ledger write path.
- **N4 — fail-closed wiring precedent exists.** `requireAuditLedger` (`internal/repository/audit_wiring.go`)
  panics on nil/typed-nil `AuditLedgerTransactionalRepository`; `NewAccountRepository` uses it
  (`repository/account.go:48-49`). `NewCallbackRepository` (`repository/callback.go:36`) and
  `NewAuditHoldRepository` (`repository/audit_hold.go:66`) take no ledger today.
- **N5 — existing test infrastructure for both acceptance checks.** `internal/repository/audit_wiring_integration_test.go`
  (`//go:build integration`) already has `countAuditLedgerRows` (`:39`), `TestAuditWiringHappyPath` (`:51`),
  and `TestAuditWiringRollbackAtomicity` (`:163`, forced rollback via duplicate `event_id` →
  `ErrAuditDuplicate`). `internal/repository/audit_ledger_integration_test.go` covers the publication
  lifecycle. No new mechanism is needed for T-10.1/T-10.2.

## 2. Problem statement

All account-domain audit facts flow through `AuditLedgerService.Record`, which appends the fact in a
**standalone transaction** (`repo.Append`, E1) and then runs a synchronous inline publisher loop with
swallowed errors, while the `drainPublications` worker concurrently claims and delivers the same fact's
`audit_publications` row (E2/E3). Because the six named sites swallow the result of `Record` (E4), a
business mutation can commit while its audit append fails (fact lost), and an audit append can commit while
the business mutation rolls back (orphan `audit_ledger` + `audit_publications` rows, E9). Delivery can be
duplicated or dropped depending on wiring (N1). The in-transaction pattern that fixes the orphan hazard is
already proven (`AppendInTx` + `XxxWithAudit`, E5/E6, fail-closed at N4), but the account/callback surfaces
do not use it, the operation service keeps a `Record` fallback (E7), and neither the swallowed-call ban nor
single-delivery is enforced by any gate (E8).

## 3. Config surface (module `configs`)

No new configuration keys are required by this direction. Two existing keys govern the delivery path and
are referenced by tests/verification only:

- `audit_ledger.signing_key` (`configs/config.yaml:172`) — `Append`/`AppendInTx` fail closed when empty
  (`audit_ledger.go:108-110, 124-126`); the acceptance tests must set a key.
- `audit_governance.enabled` (`configs/config.yaml:174`, default `false`) — when `false`,
  `buildAuditPublishers` returns nil (N1), i.e. the governance publisher is absent and the inline loop is
  doubly dead; the T-10.2 "no governance HTTP call" property is independent of this flag once REQ-1 lands.

## 4. Requirements

### REQ-1 — `Record` performs no publisher delivery; the inline loop is deleted

`auditLedgerService.Record` (`internal/service/audit_ledger.go:66`) must:
- keep appending via `repo.Append` — the `audit_ledger` row and its `status='pending'` `audit_publications`
  row commit atomically (E9);
- keep the `ErrAuditDuplicate → return nil` idempotency (`:70-72`) and `ObserveAuditIngestFailure` (`:73-75`);
- **not invoke any publisher**: delete the `for _, publisher := range s.publishers` loop (`:77-78`), the
  `publishers []AuditPublisher` field, and the variadic `publishers ...AuditPublisher` parameter of
  `NewAuditLedgerService` (`:54`). All current call sites pass zero publishers (N1), so this is a
  compile-checked removal. The `AuditPublisher` interface and `callbackService.Publish` remain — they serve
  `publishAuditFact` on the drain path (`callback_audit.go:97-113`).

### REQ-2 — `drainPublications` is the sole delivery path

Delivery of a fact to callback deliveries and governance endpoints happens only through the outbox worker:
`Start` ticker → `drainPublications` (`callback.go:267` → `callback_audit.go:47`) → `ClaimPublication` →
`publishAuditFact` (enqueue callback deliveries + per-tenant-bound governance `Publish`) →
`FinishPublication` with `completed | pending | dead_letter`. No code path other than `drainPublications`
may deliver a fact that has a publication row. No behavioral change to `drainPublications` itself is
required; REQ-1 removes the only other delivery path and REQ-7/T-10.2 proves the invariant.

### REQ-3 — The six named write sites stop swallowing and join the in-tx pattern where a write exists

All six `_ = s.audit.Record(...)` sites (E4) must be converted so that audit append errors are handled
explicitly. For the four **write-path** facts the fact must commit in the same business transaction as the
domain mutation, using the proven pattern (E5/E6, fail-closed at N4):

| Path | Current site | Fact to preserve (action, event_id) | Domain write to join |
|---|---|---|---|
| callback register | `callback.go:160` | `callback.registered`, `EventID: subscription.ID` | `repo.CreateSubscription` (`repository/callback.go:74`; pre-assign `subscription.ID` in the service so the fact's event_id is known before the tx, per the `CreateSubscription` ID-when-empty precedent) |
| callback delete | `callback.go:221` | `callback.deleted`, `EventID: id` | `repo.DeleteSubscription` (`repository/callback.go:116`) |
| callback delivery replay | `callback.go:247` | `callback.delivery.replayed`, `EventID: id` | `repo.ResetDelivery` (`repository/callback.go:222`) |
| account provisioning | `account_provision.go:168` | `account.provisioned`, `EventID: account.ID` (dedupe key) | the `EnsureByIdentity` creation write (`repository/account_identity.go:20`); the fact is appended only when this call actually creates the account (repeat `ensure` calls are deduped by `EventID: account.ID` → `ErrAuditDuplicate` no-op) |

Mechanically: the repository gains `XxxWithAudit` variants (or takes
`AuditLedgerTransactionalRepository` via `requireAuditLedger`, like `NewAccountRepository` at
`repository/account.go:48-49`), opens `Transaction(...)`, performs the domain write, then
`AppendInTx(ctx, tx, fact)` — mirroring `account_profile.go:67-103`. Fact content (source `"aero-id"`,
actor, tenant, resource type/id, payload) is preserved unchanged.

For the two **read-path** facts — `account.viewed` (`account.go:155`) and `account.listed` (`account.go:184`)
— there is no business transaction to join (N2). Their obligations are: (a) append through the sole append
path (REQ-1 makes this structural), and (b) stop swallowing: the append error is handled explicitly and
surfaces as a mapped `*pkg/errors.AppError` with a stable dotted code (e.g. `audit.ingest_failed`) instead
of `_ =`; sensitive-read facts remain durable per AGENTS.md (metadata-only access audit is allowed), so the
fact itself is not dropped.

### REQ-4 — `AppendInTx` failure fails the mutation

When the in-transaction audit append fails, the whole business transaction rolls back: the domain mutation
is not committed, no `audit_ledger` row and no `audit_publications` row exist (E9), and the service returns
the mapped `*pkg/errors.AppError` (repository returns raw errors; services translate). This is the proven
behavior of `TestAuditWiringRollbackAtomicity` (`audit_wiring_integration_test.go:163`) extended to the
newly converted paths (REQ-3) and to the publication table (REQ-7/T-10.1).

### REQ-5 — Operation service `Record` fallback is removed; the in-tx path is the only path

`internal/service/operation_audit.go` must no longer fall back to standalone `Record`: the type-assertion
fallbacks at `:42` (`finishStep`), `:71` (`setOperationStatus`), `:86` (`finishCompensationStep`) and the
`recordOperationAudit` helper (`:89-95`) are deleted. The operation service requires
`OperationAuditRepository`/`OperationStateAuditRepository` at wiring (fail closed, per N4); `operationRepo`
already implements all four `WithAudit` methods (`operation.go:135,417,430,448`), so production wiring is
unaffected and the change removes only the latent second write path (E7).

### REQ-6 — `make check-audit-swallow` gate

Add a Makefile gate (script precedent: `scripts/check-architecture.sh`, `Makefile:26-28`) that fails when
any `.go` file under `internal/service/` (live layered tree) contains an assignment-swallowed audit or
publication call, matching the direction's "`_ =` audit/publication calls" pattern:

- `_ = <expr>.Record(` (e.g. `_ = s.audit.Record(`)
- `_ = <expr>.Publish(`
- `_ = <expr>.FinishPublication(` / `_ = <expr>.ClaimPublication(`

Exclusions (documented in the script): `Log(` calls — the legacy diagnostics surface (N3); the unwired
parallel tree `internal/{file,message,comment,block,notification,search,audit}` (AGENTS.md: dead tree, not
runtime).

Run against today's tree the gate must fail, including on the six named sites (E4). Because the gate must
pass as part of the done-gate, the four additional same-class matches (N3: `audit_hold.go:59,74`,
`legacy_cutover.go:88`, `callback_audit.go:81`) are converted with the same mechanical change — the
`FinishPublication` swallow becomes an explicit error handling per REQ-4 semantics (its error is a
lease-loss/workflow signal, not a compliance fact loss). The gate is added to the done-gate line in
AGENTS.md.

### REQ-7 — Tests (acceptance checks T-10.1 and T-10.2, made testable)

- **T-10.1a — rollback atomicity, both tables (repository integration).** Extend
  `internal/repository/audit_wiring_integration_test.go`: add `countAuditPublicationRows(t, db, eventID)`
  (JOIN `audit_ledger` on `fact_id`). In `TestAuditWiringHappyPath` (`:51`) assert exactly one
  `audit_publications` row per committed fact (today only `audit_ledger` is counted). In
  `TestAuditWiringRollbackAtomicity` (`:163`, forced rollback via duplicate `event_id` →
  `ErrAuditDuplicate`) additionally assert **zero** `audit_publications` rows for the failed fact alongside
  the existing zero-ledger-row assertion — the two tables disappear together.
- **T-10.1b — service-level, same-tx for `account.provisioned`.** Unit/integration test: a provisioning
  call whose `AppendInTx` fails (forced via a repo-level failure after the identity insert) returns the
  mapped service error and leaves **no** account row, **no** `audit_ledger` row, and **no**
  `audit_publications` row; a successful provisioning call yields exactly one ledger row and one
  publication row with `event_id = account.ID`. For `account.viewed`/`account.listed` (no tx exists, N2):
  with a failing ledger repo, the read returns the mapped `audit.ingest_failed`-class error instead of
  succeeding silently (non-swallow proof), and with a recording fake publisher the read performs zero
  `Publish` calls (single-path proof).
- **T-10.2a — `Record` performs no governance HTTP call; only the two table rows.** Service unit test:
  after REQ-1, `Record` accepts no publishers (compile-time), so the observable effect is exactly one
  `audit_ledger` row and one `audit_publications` row (integration, `testpg` fixture with a configured
  `signing_key`, N4). Drain-path counterpart: `ClaimPublication` → failing publisher →
  `FinishPublication(..., "pending", next=past)` → re-claim (attempt incremented) →
  `FinishPublication(..., "completed")` → empty claim, with exactly one publication row per fact across the
  whole cycle (UNIQUE `fact_id`), proving `drainPublications` delivers exactly once per claim and there is
  no second (inline) delivery path.
- **T-10.2b — gate.** `make check-audit-swallow` fails on a fixture file reproducing the six sites
  (`_ = s.audit.Record(` under `internal/service/`) and passes on the tree after REQ-1, REQ-3, REQ-5, and
  the N3 conversions land.

## 5. Acceptance checks (as supplied, now testable)

| Supplied check | Testable formulation | Where |
|---|---|---|
| T-10.1: business mutation that fails after `AppendInTx` leaves zero `audit_ledger`/`audit_publications` rows (rollback atomicity) | `TestAuditWiringRollbackAtomicity` extended: after a duplicate-`event_id` `WithAudit` mutation fails, `count(*) FROM audit_ledger WHERE event_id=?` **and** `count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id=ap.fact_id WHERE l.event_id=?` are both 0; happy path asserts both are 1 | `internal/repository/audit_wiring_integration_test.go` (integration tag) |
| T-10.1: `account.provisioned`/`viewed`/`listed` facts commit in the same tx as their account row | `account.provisioned`: appended via `AppendInTx` in the account-creation tx (forced-failure rollback proves same-tx; `event_id = account.ID` dedupe preserved). `account.viewed`/`listed`: no account-row write exists (N2); the testable form is non-swallowed append (error surfaces as mapped `AppError`) + zero publisher invocations | repository integration + `internal/service/account_provision_test.go`, `account_test.go` |
| T-10.2: `Record` performs no governance HTTP call (only enqueues an `audit_publications` row) | `Record` takes no publishers after REQ-1 (compile-time); integration test asserts exactly one ledger row + one `audit_publications` row (`status='pending'`) per `Record` call, and no publisher of any kind is invoked | `internal/service/audit_ledger_test.go` + `internal/repository/audit_ledger_integration_test.go` |
| T-10.2: `make check-audit-swallow` greps `_ =` audit/publication calls and fails on the six verified sites | Gate script fails on a fixture with the six `_ = s.audit.Record(` patterns and on the current tree; passes on the converted tree (zero matches of the gate's pattern classes, N3 sites resolved) | Makefile target + `scripts/check-audit-swallow.sh` |

## 6. Out of scope (explicitly not part of this direction)

- **The other two directions in `configs-1fa2a926.json`**: dead-letter replay tooling (direction 2) and
  deterministic `event_id` anchoring (direction 3).
- **`audit_hold.go:59,74`, `legacy_cutover.go:88`, `callback_audit.go:81` swallows** are not enumerated in
  this direction's evidence; they are converted only because REQ-6's gate (the direction's own pattern)
  requires zero matches for the gate to pass (N3).
- **Legacy `_ = s.audit.Log(...)` diagnostics swallows** (`internal/service/{block,admin,message,comment,file_share,ban}.go`,
  unwired `internal/{file,block,comment,message}` trees) — legacy diagnostics surface, excluded from the
  gate pattern (N3).
- **`drainPublications`/`ClaimPublication`/`FinishPublication` semantics and migration 000045** — unchanged.
- **New config keys** — none required (§3).

## 7. Verification

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make build
make test-integration   # repository SQL/wiring changed (audit_wiring, audit_ledger, callback, account_identity)
```
