# Design — AuditLedgerService.Record: single delivery path + AppendInTx consolidation

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-06-audit-record-single-delivery-path-and-appendintx-consolidation.md` (REQ-1..REQ-6) |
| Module (batch label) | `cmd/migrate` (effective scope `internal/service` + `internal/repository` + Makefile gate) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery path; gate G3: T-5, T-10.2) |
| Status | Design — evidence re-verified against live tree on 2026-08-06 |

## 1. Evidence verification result (untrusted claims re-checked)

Every symbol in the requirements evidence was re-checked against the live tree. All substantive claims
hold. Two additional corrections beyond the spec's own C1/C2, and one incompleteness, are folded in:

| # | Claim | Verdict |
|---|---|---|
| E1 / C1 | Inline loop `for _, publisher := range s.publishers` with `_ = publisher.Publish(ctx, fact)` at `audit_ledger.go:77-81`, swallow at `:79`, `Record` at `:66` | ✅ exact |
| E2 | `drainPublications` at `callback_audit.go:47`; `finishPublicationAttempt` `:73-82`; `publicationOutcome` `:84-95`; `publishAuditFact` `:97-113` | ✅ exact |
| E3 | `s.drainPublications(workerCtx)` in `Start` ticker at `callback.go:267` | ✅ exact |
| E4 | `wireAuditServices` at `app/audit.go:17`; `buildAuditPublishers` at `:38` | ✅ exact |
| A1 | `new.go:146` constructs ledger with **zero** publishers; `audit_integration_test.go:19`, `audit_ledger_test.go:43,56` also zero | ✅ exact |
| A2 | `new.go:150` hands publishers to `callbackSvc.SetAuditPublishers` only; `new.go:179` starts the callback worker | ✅ exact, **except C3**: the `if s.publications != nil { return nil }` no-op guard in `callbackService.Publish` is at `callback_audit.go:23-25`, not `:28-31` as cited |
| E5 | `AppendInTx` at `audit_ledger.go:123`; `appendInTx` shared funnel; `createAuditRows` `:189-203` creates ledger row **and** `status='pending'` publication row in one tx | ✅ exact |
| E6 / C2 | `checkAuditDuplicate` at `audit_ledger.go:174` (not `:203`); dedupe key `(tenant_id, source, event_id)` | ✅ exact |
| E7 | `operation.go:178`, `event_receipt.go:180`, `sync_job.go:111`, `account_profile.go:101` call `r.audit.AppendInTx(ctx, tx, fact)` inside the repo's own `Transaction` | ✅ verbatim, **but incomplete**: there are **6** call sites, not 4 — `operation_audit.go:125` and `sync_job_audit.go:43` are additional in-tx users. No requirement impact: all six are already transactional |
| E8 | `migrations/000045_audit_publications.up.sql`: `UNIQUE (fact_id)`, FK `ON DELETE CASCADE`, due/lease indexes | ✅ exact |
| E9 | Seven standalone `_ = s.audit.Record(...)` write sites: `callback.go:160/221/247`, `audit_hold.go:59/74`, `account_provision.go:168`, `legacy_cutover.go:88` | ✅ exact. Note: the E9 table lists 9 `Record` sites total; the two read-path sites `account.go:155/184` (`account.viewed/listed`) stay on `Record` per contract §B2-1. REQ-3's 7-row conversion table is the correct scope |
| A3 | `ClaimPublication` (`FOR UPDATE SKIP LOCKED`, pending+due or expired-lease delivering), `FinishPublication` (guarded by `status='delivering' AND lease_owner=?`), `publicationOutcome` terminal states | ✅ exact |
| REQ-5 | Makefile precedent `check-architecture` at `Makefile:27-28`, `scripts/check-architecture.sh` | ✅ exact |
| REQ-6 | `TestAuditWiringHappyPath` at `audit_wiring_integration_test.go:51`, `TestAuditWiringRollbackAtomicity` at `:163`, `countAuditLedgerRows` at `:39`; `requireAuditLedger` at `audit_wiring.go:14-19` | ✅ exact |
| Constructors | `NewCallbackRepository(db)` at `repository/callback.go:36`; `NewAuditHoldRepository(db)` at `repository/audit_hold.go:66`; `NewLegacyCutoverRepository(db)` at `repository/legacy_cutover.go:80` — none take a ledger today | ✅ exact |

Additional facts verified and used below:

- **F1 — created-flag is derivable for provisioning.** `ensureAccountIdentityTx` →
  `bindAccountIdentity` (`account_identity.go`) returns `createdID` only when the identity insert's
  `RowsAffected == 1`; otherwise `useExistingAccountIdentity` returns the pre-existing account. So an
  "account was created by this call" boolean can be plumbed without new SQL.
- **F2 — dedupe backstop.** `ErrAuditDuplicate` (ledger dedupe on `EventID`) already makes repeat
  `account.provisioned` writes no-ops today (`Record` swallows it at `audit_ledger.go:69-72`);
  `event_receipt.go:180` shows the in-tx precedent for **ignoring** `ErrAuditDuplicate` so idempotent
  replays never fail the transaction.
- **F3 — repo-generated ID.** `CreateSubscription` assigns `value.ID = idgen.NewID()` only when empty
  (`repository/callback.go`), so the service may pre-assign the ID to build the fact, or the repo binds
  `fact.EventID` after creation.
- **F4 — test fakes to update.** `callbackFakeRepo` (`callback_delivery_test.go:18`),
  `fakeHoldRepo` (`audit_hold_test.go:13`), `fakeCutoverRepo` (`legacy_cutover_test.go:11`) implement the
  repo interfaces; constructor call sites: `NewAuditHoldService` at `app/audit.go:110` and
  `audit_hold_test.go:69,94`; `NewLegacyCutoverService` at `new.go:241,411` and `legacy_cutover_test.go:41`.
- **F5 — file-size headroom.** `internal/service/callback.go` is 465 lines (limit 500) — the design only
  removes lines there. `repository/callback.go` (257) has headroom for three new methods.
- **F6 — deploy prerequisites are config-visible.** `configs/config.yaml:172-173` carries the
  `audit_ledger.signing_key` placeholder (`AppendInTx` fails closed when empty); `audit_governance.enabled:
  false` by default, so `buildAuditPublishers` returns nil today (the inline loop is doubly dead).

## 2. Goals and non-goals

**Goals.** (G1) Delete the dead inline publisher loop in `Record` (REQ-1) and prove with a gate (REQ-5)
that `drainPublications` is the sole delivery path (REQ-2). (G2) Convert the seven standalone-audit write
paths to the proven in-tx `WithAudit`/`AppendInTx` pattern so the audit fact (and its pending publication
row) commits atomically with the business mutation (REQ-3), fails the mutation on append failure (REQ-4),
and is covered by the four acceptance checks made testable (REQ-6).

**Non-goals.** Direction-2 swallow conversion (`_ = s.audit.Record` → observable failure) and the
`check-audit-swallow` gate; direction-3 governance-token invalidation; `account.viewed/listed` read-path
audits (stay on standalone `Record`); changes to `drainPublications`/`ClaimPublication`/`FinishPublication`
or migration 000045 semantics; backfilling facts for mutations that committed before this change.

## 3. Design overview

Two independent layers land in this order:

1. **Delivery layer (REQ-1/2/5)** — pure deletion plus a grep gate. `Record` stops invoking publishers;
   the `publishers` field and variadic constructor parameter disappear (compile-checked removal; all four
   call sites verified zero-publisher). Delivery remains: `Start` ticker → `drainPublications` →
   `ClaimPublication` → `publishAuditFact` (callback-delivery enqueue + tenant-bound governance publish) →
   `FinishPublication` (`completed | pending | dead_letter`).
2. **Consolidation layer (REQ-3/4)** — seven write paths move their audit append inside the business
   transaction. Repository constructors gain the fail-closed `requireAuditLedger` ledger dependency and
   `WithAudit` method variants; services build the fact, call the variant unconditionally, and drop the
   post-commit `s.audit.Record` calls (and the now-dead `s.audit` fields/params in the converted services).

**Design decision D1 — unconditional `WithAudit` on the main repo interfaces (no standalone-Record
fallback).** The shipped `account_profile.go` precedent type-asserts a separate
`AccountProfileAuditRepository` and falls back to plain-write + standalone `Record`. This design deviates
for the seven new paths: the `WithAudit` methods go **on the main repository interfaces** and the services
call them unconditionally. Justification: (a) production repos are fail-closed by construction, so the
capability is guaranteed; (b) a live fallback keeps a second, divergent write path — exactly what REQ-4
("no fact, rollback") and the campaign's single-delivery intent forbid; (c) all fakes are test-local and
enumerated (F4). Migrating `account_profile` itself to this strict pattern is out of scope.

## 4. API changes

### 4.1 `internal/service/audit_ledger.go` (REQ-1)

- Delete the loop at `:77-81` from `Record` (lines `:66-83` become append → duplicate-nil → metric → err).
- Delete the `publishers []AuditPublisher` field (`:44`) and the variadic parameter from
  `NewAuditLedgerService` (`:53-57`): `NewAuditLedgerService(repo repository.AuditLedgerRepository)
  AuditLedgerService`.
- **Keep** the `AuditPublisher` interface (`:41-43`), `callbackService.Publish` (`callback_audit.go:19-27`,
  with its no-op guard at `:23-25`), and `SetAuditPublishers` — they serve `publishAuditFact` on the drain
  path. `Record`, `Log`, `List`, `VerifyChain`, `AuditFactInput`, `buildAuditFact` unchanged.

Call-site impact: `new.go:146`, `app/audit_integration_test.go:19`, `audit_ledger_test.go:43,56` — all
already zero-publisher; compile passes with no edits.

### 4.2 Repository layer (REQ-3) — `internal/repository/`

All new methods take `fact *domain.AuditFact` (nil-safe: append skipped when nil, matching the
`account_profile.go:95-97` precedent) and run the existing business write plus
`r.audit.AppendInTx(ctx, tx, fact)` inside one `Transaction(...)`.

| File | Constructor change | New interface methods |
|---|---|---|
| `callback.go` | `NewCallbackRepository(db, audit AuditLedgerTransactionalRepository)` with `requireAuditLedger` | on `CallbackRepository`: `CreateSubscriptionWithAudit(ctx, sub, fact) error` (repo binds `fact.EventID = sub.ID` after creation, F3); `DeleteSubscriptionWithAudit(ctx, owner, tenantID, id, fact) error` (preserves `ErrNotFound` when `RowsAffected == 0`); `ResetDeliveryWithAudit(ctx, id, fact) error` |
| `audit_hold.go` | `NewAuditHoldRepository(db, audit)` with `requireAuditLedger` | on `AuditHoldRepository`: `PutHoldWithAudit(ctx, hold, fact) error` (upsert on the unique triple, as today); `RemoveHoldWithAudit(ctx, tenantID, scopeType, scopeID, fact) error` |
| `legacy_cutover.go` | `NewLegacyCutoverRepository(db, audit)` with `requireAuditLedger` | on `LegacyCutoverRepository`: `SetCutoverWithAudit(ctx, entry, fact) error` (same `OnConflict` upsert) |
| `account_identity.go` + `account_quota.go` | no constructor change (`accountRepo` already holds the fail-closed ledger, `new.go:162`) | on `AccountRepository`: `EnsureByIdentityWithAudit(ctx, tenantID, issuer, subject, canonicalUID, homeRegion, fact) (*domain.Account, error)`; on `AccountQuotaRepository`: `EnsureByIdentityWithQuotaWithAudit(ctx, ..., reservationID, fact) (*domain.Account, error)` |

**Provisioning created-only rule (D2).** `ensureAccountIdentityTx` changes its return to
`(*domain.Account, bool, error)` where the bool is "account created by this call" (F1). The plain
`EnsureByIdentity`/`EnsureByIdentityWithQuota` discard the bool (no signature change for existing
callers). The `WithAudit` variants append the fact **only when created**, and ignore
`ErrAuditDuplicate` defensively (F2, `event_receipt.go:180` precedent), so a repeat ensure can never fail
the transaction. This is stricter than today's behavior for pre-ledger accounts (today a repeat ensure
writes a spurious fact because dedupe finds nothing; after this change it writes none).

### 4.3 Service layer (REQ-3/4) — `internal/service/`

| Path | Service change | Fact preserved (action, event_id) |
|---|---|---|
| `callback.go:160` Register | `s.repo.CreateSubscriptionWithAudit(ctx, subscription, fact)`; pre-assign `subscription.ID = idgen.NewID()` so the fact carries `EventID: subscription.ID`; delete `:159-165` | `callback.registered`, `EventID: subscription.ID`, payload `endpoint_sha256` + `event_types` |
| `callback.go:221` Delete | `s.repo.DeleteSubscriptionWithAudit(ctx, owner, tenantID, id, fact)`; delete `:220-223`; keep `ErrNotFound → apperr.NotFound("callback.not_found", ...)` mapping | `callback.deleted`, `EventID: id` |
| `callback.go:247` ReplayDelivery | `s.repo.ResetDeliveryWithAudit(ctx, id, fact)`; delete `:246-250`; `GetDelivery` read stays outside the tx | `callback.delivery.replayed`, `EventID: id`, payload `{"action": delivery.Action}` |
| `audit_hold.go:59` Put | `s.holds.PutHoldWithAudit(ctx, hold, fact)` | `audit_hold.set`, `EventID: idgen.NewID()`, payload `reason`/`expires_at` |
| `audit_hold.go:74` Remove | `s.holds.RemoveHoldWithAudit(ctx, tenantID, scopeType, scopeID, fact)` | `audit_hold.removed`, `EventID: idgen.NewID()` |
| `legacy_cutover.go:88` Set | `s.cutover.SetCutoverWithAudit(ctx, entry, fact)`; the `Get`/`ValidCutoverTransition` pre-check stays | `legacy.cutover.set`, `EventID: idgen.NewID()`, payload `status`/`previous`/`note` |
| `account_provision.go:168` | delete `recordAccountProvisioned`; `provisionAccount`/`provisionUnmeteredAccount` build the fact up front and call the quota/unmetered `WithAudit` variants (D2: fact appended only when created) | `account.provisioned`, `EventID: account.ID`, payload `home_region` |

Fact construction moves from the post-write block to before the repo call; every field (source
`"aero-id"`, actor, tenant, resource type/id, payload) is preserved verbatim.

**Dead-code removal following from D1.** `callbackService.audit` field (`callback.go:92` region),
`SetAuditLogger` (`:97-99`), and the `SetAuditLogger` branch of `wireAuditServices` (`app/audit.go:19-23`)
are deleted — nothing else calls `SetAuditLogger`. `NewAuditHoldService(holds)` (drop `audit` param;
callers F4) and `NewLegacyCutoverService(cutover, cleanup)` (drop `audit`; callers F4). `accountService`
**keeps** its `audit` field: `account.viewed/listed` (`account.go:155,184`) and the profile fallback path
still use it (out of scope).

**Error mapping (REQ-4).** Add one small shared helper, e.g.
`mapAuditAppendError(err error, code string) *apperr.AppError` in `internal/service/audit_append.go`
(or beside the converted services): `errors.Is(err, repository.ErrAuditDuplicate)` →
`apperr.Conflict(code+".duplicate", "audit fact already recorded")`; otherwise
`apperr.Internal(code, "...")` wrapping the raw error. Each converted path maps through it using its
existing code prefix (`callback.create_failed`, `callback.delete_failed`,
`callback.delivery_replay_failed`, `audit_hold.save_failed`, `audit_hold.remove_failed`,
`cutover.save_failed`, `account.ensure_failed`). The `ErrAuditDuplicate` branch is defensive (fresh IDs
make it unreachable on register/hold/cutover; the ensure variants ignore it before it reaches the
service); its testable path is T-5's forced-duplicate scenario.

### 4.4 Wiring — `internal/app/new.go`, `internal/app/audit.go`

- `new.go:135` → `repository.NewCallbackRepository(gdb, auditLedgerRepo)` (auditLedgerRepo is built at
  `:134`, above the callback repo — already ordered correctly).
- `new.go:241,411` → pass `auditLedgerRepo` into `NewLegacyCutoverRepository`; drop `auditLedger` from
  `NewLegacyCutoverService` calls.
- `app/audit.go:110` → `service.NewAuditHoldService(repository.NewAuditHoldRepository(gdb,
  auditLedgerRepo))`; `wireAuditServices` loses the `SetAuditLogger` branch but keeps
  `SetAuditPublicationRepository` and `SetAuditPublishers`.
- `new.go:146` → `service.NewAuditLedgerService(auditLedgerRepo)` (unchanged line, now the only legal form).

### 4.5 Gate (REQ-5)

- `scripts/check-single-delivery-path.sh` (pattern: `scripts/check-architecture.sh`, `set -euo pipefail`):
  1. **G-1 (REQ-1, spec-mandated):** fail if any `.go` file under `internal/service/` (tests included, so
     the REQ-6 fixture trips it) matches `_ = publisher.Publish`. Zero matches after REQ-1.
  2. **G-2 (REQ-3 completeness, extension):** fail if `internal/service/callback.go`,
     `audit_hold.go`, `legacy_cutover.go`, or `account_provision.go` contains `.audit.Record` or
     `s.audit` (after conversion none remain; `account.go` is deliberately excluded because
     viewed/listed stay on `Record`). This makes the consolidation machine-checkable; it does not
     overlap direction-2's `check-audit-swallow` (different files/patterns).
- `Makefile` target `check-single-delivery-path` (`.PHONY`, same shape as `check-architecture` at
  `Makefile:27-28`), and add the target to the done-gate line in `AGENTS.md`.

## 5. Compatibility constraints

1. **No schema change, no data migration.** `audit_publications` already exists (000045). Rollback is a
   binary revert. **Deploy prerequisite:** `cmd/migrate` must have applied 000045 and the
   `audit_ledger.signing_key` must be non-empty (`configs/config.yaml:172-173` / `AERO_ID_AUDIT_LEDGER_SIGNING_KEY`)
   **before** this build rolls out — see failure mode F7.
2. **Behavior flip: seven paths go from silent-swallow to fail-closed.** Today a failed audit append is
   swallowed (`_ =`) and the mutation commits. After this change the same failure rolls the mutation back
   and returns an `AppError`. This is the intended REQ-4 semantics; operators must treat it as an
   availability-relevant change for those seven endpoints.
3. **Constructor/signature churn is compile-checked and fully enumerated:** `NewAuditLedgerService` (4
   call sites), `NewCallbackRepository` (1), `NewAuditHoldRepository` (1), `NewLegacyCutoverRepository`
   (2), `NewAuditHoldService` (3), `NewLegacyCutoverService` (3), `SetAuditLogger`/`wireAuditServices`
   (1 + internal). No external API surface (HTTP/gRPC/protobuf) changes; handler signatures unchanged.
4. **Interface growth breaks test fakes** (F4): `callbackFakeRepo`, `fakeHoldRepo`, `fakeCutoverRepo`,
   and any account-repo fakes must implement the new `WithAudit` variants (returning nil/forced errors).
   This is required by D1 and is the only test-only churn.
5. **Idempotency preserved.** Register/delete/replay/hold/cutover facts keep their exact `event_id`
   semantics; repeat `ensure` writes no fact (D2), which is a strict improvement over today's behavior
   for pre-ledger accounts (F2) and identical for post-change accounts.
6. **Delivery semantics unchanged.** Facts already committed via standalone `Append` already got a
   `pending` publication row (`createAuditRows`); the drain worker picks them up identically. No fact
   that exists today lacks a publication row, so no backfill is needed (verified: `Append` and
   `AppendInTx` share `appendInTx`/`createAuditRows`).
7. **500-line gate respected.** Only removals in `service/callback.go` (465 lines); additions in
   `repository/callback.go` (257) and siblings stay under 500; `scripts/` files are not Go.

## 6. Failure modes

| # | Failure | Behavior after change | Handling |
|---|---|---|---|
| F1 | `AppendInTx` returns `ErrAuditDuplicate` | Whole tx rolls back (business write undone); service maps to `Conflict` (`*.duplicate`); no fact, no publication row | Defensive on fresh-ID paths; forced in T-5; ensure variants ignore it (D2) |
| F2 | `AppendInTx` fails (signing key empty, DB down, advisory-lock wait/error, chain read error) | Whole tx rolls back; service maps to `Internal` with path-specific code; client sees the error; nothing persisted | `ObserveAuditIngestFailure` is not re-fired here (it stays on `Record`'s append error); rely on tx rollback + client retry (HTTP idempotency middleware covers POST) |
| F3 | Worker crash between `ClaimPublication` and `FinishPublication` | Row stays `delivering` with a lease; reclaimed after lease expiry by the next `ClaimPublication` (`audit_ledger.go` lease branch) | Existing A3 machinery; covered by T-10.2 |
| F4 | Governance publisher fails / tenant binding unavailable | `publicationOutcome` → `pending` + backoff, or `dead_letter` at `attempt >= MaxAttempts`; callback-delivery enqueue already happened (at-least-once) | Existing A3 machinery; unchanged |
| F5 | Missing `WithAudit` implementation (fake or future repo) | Compile error (methods on the main interface) — fail-closed by construction | D1; no runtime fallback to forget |
| F6 | Wiring passes nil ledger | `requireAuditLedger` panics at startup (already the shipped pattern for account/operation/receipt/sync) | Startup fail-fast; covered by existing wiring tests |
| F7 | Deploy with 000045 not applied or signing key empty | First mutation on a converted path fails (`Internal`, rollback) instead of silently committing without audit | Deploy prerequisite (compat §1); migration runner `cmd/migrate` is the mandated production path; optional readiness check for table presence is the separate activation-gate direction |
| F8 | Gate regression (someone re-adds an inline publish or a post-commit `Record` in the four files) | `make check-single-delivery-path` fails CI | G-1/G-2 |

## 7. Migration steps (ordered, done-gate after each)

**Phase A — delivery layer (REQ-1/2/5).** Delete loop/field/param from `audit_ledger.go`; add
`scripts/check-single-delivery-path.sh` + Makefile target; add G-2 checks; update AGENTS.md done-gate
line; add `TestRecordAppendsWithoutPublishing` (spy asserts `repo.Append` called, no publisher path
exists by construction). Gate: `make test && make lint && make lint-strict && make check-file-sizes &&
make check-single-delivery-path && make build`. Zero behavior change in the running binary.

**Phase B — repository layer (REQ-3).** Add the six `WithAudit` methods and four constructor ledger
params (F4 fakes updated in the same commit so `make test` stays green); plumb the created-flag through
`ensureAccountIdentityTx`; update `new.go`/`app/audit.go` wiring to the new constructors. Unit tests for
fact-binding rules (register binds `fact.EventID = sub.ID`; ensure created-only). Integration tests
(`//go:build integration`): one happy-path per converted repo asserting domain row + `audit_ledger` row +
`audit_publications` row commit together with preserved action/event_id (extend
`audit_wiring_integration_test.go`; reuse `testpg` harness and the `countAuditLedgerRows` pattern with a
new `countAuditPublicationRows` helper).

**Phase C — service layer (REQ-3/4).** Convert the seven paths per §4.3; delete `recordAccountProvisioned`
and the `s.audit` fields/setters; add `mapAuditAppendError`; update the three service-constructor call
sites. `callback_delivery_test.go`/`audit_hold_test.go`/`legacy_cutover_test.go` fakes gain forced-error
hooks for the T-5 service tests.

**Phase D — acceptance tests (REQ-6).**
- **T-5 atomicity (both tables):** extend `TestAuditWiringHappyPath` (assert one `audit_publications` row
  per committed fact) and `TestAuditWiringRollbackAtomicity` (forced duplicate-`event_id` rollback:
  zero `audit_ledger` rows **and** zero `audit_publications` rows for that event_id).
- **T-5 service error, no fact:** repository integration (forced `ErrAuditDuplicate` in a converted path:
  domain row unchanged, zero rows in both tables, service returns mapped `Conflict`) + service unit
  (fake repo WithAudit variant returns a forced error; service returns `AppError`, does not swallow).
- **T-10.2 reclaim + exactly one delivery:** repository integration
  (`audit_ledger_integration_test.go`): Append → Claim → `FinishPublication(pending, next=past)` →
  reclaim (attempt incremented) → `FinishPublication(completed)` → empty claim; one publication row per
  fact throughout (UNIQUE `fact_id`). Service unit (`callback_audit_test.go`): `callbackService` with a
  fake `AuditPublicationRepository` + fake `CallbackRepository` records exactly one `enqueueFact` per
  claim; `Record` performs zero publisher invocations.
- **Gate fixture:** a fixture file under `internal/service/` containing `_ = publisher.Publish(` fails
  `make check-single-delivery-path`; tree passes.

**Deploy.** Apply migrations + confirm signing key (F7), roll out the binary, observe
`audit_publications` drain metrics and per-source publication latency; rollback is a binary revert
(compat §1). Run `make test-integration` before merge (repository SQL/wiring touched).

## 8. Testable acceptance mapping

| Acceptance check | Test | File |
|---|---|---|
| T-5: forced rollback removes `audit_ledger` **and** `audit_publications` rows together | `TestAuditWiringRollbackAtomicity` extended with `countAuditPublicationRows`; happy path asserts both = 1 | `internal/repository/audit_wiring_integration_test.go` |
| T-5: `AppendInTx` failure → mapped service error, no fact, domain row unchanged | Forced-duplicate repo integration + fake-repo service test asserting `AppError` propagation | `audit_wiring_integration_test.go`, `callback_delivery_test.go`/`audit_hold_test.go` |
| T-10.2: failing publisher → reclaim → completed → empty claim; exactly one delivery per fact; zero `Publish` from `Record` | Claim/finish/reclaim cycle integration; drain unit test with recording fakes; `TestRecordAppendsWithoutPublishing` | `internal/repository/audit_ledger_integration_test.go`, `internal/service/callback_audit_test.go`, `internal/service/audit_ledger_test.go` |
| Gate: zero `_ = publisher.Publish` (+ zero `s.audit` in the four converted files) | Fixture fails, tree passes | `scripts/check-single-delivery-path.sh`, `Makefile` |

## 9. Out of scope (unchanged from REQ-5 of the requirements)

Direction-2 swallow conversion and its gate; direction-3 TokenSource invalidation; `account.viewed` /
`account.listed` staying on `Record`; the ingest/replay/operation/sync `Record` sites already layered on
transactional `WithAudit` methods; `drainPublications`/claim/finish/migration-000045 semantics; the
account_profile dual-path migration to the strict D1 pattern.
