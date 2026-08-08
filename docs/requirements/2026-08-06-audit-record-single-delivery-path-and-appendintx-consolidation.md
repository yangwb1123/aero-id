# Requirements — AuditLedgerService.Record: remove the dead inline publisher loop, make drainPublications the sole delivery path, finish AppendInTx consolidation

| | |
|---|---|
| Module (batch label) | `cmd/migrate` (direction scope: `internal/service` + `internal/repository` audit delivery path) |
| Source analysis | `docs/auto/analyses/cmd-migrate-c1b3771d.json` (direction 1 of 3) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery path; gate G3: T-5, T-10.2) |
| Contract | `docs/proposals/audit-contract-batch-aero-id.md` (§B2-1, §B2-2) |
| Date | 2026-08-06 |
| Status | Draft — evidence verified against live tree |

## 1. Evidence verification status

Every cited file/symbol was re-checked against the repository. Two line-number corrections (C1, C2) and
three additional facts (A1–A3) that shape the requirements are folded in.

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `internal/service/audit_ledger.go:68-79` — `Record` appends via `repo.Append` then runs an inline `_ = publisher.Publish(ctx, fact)` loop | ✅ `Record` at `audit_ledger.go:66`; `repo.Append` at `:68`; `ErrAuditDuplicate → return nil` at `:70-72`; `ObserveAuditIngestFailure` at `:73-75`; inline loop `for _, publisher := range s.publishers` with `_ = publisher.Publish(ctx, fact)` at `:77-81` (**C1**: swallow is at `:79`, not `:68-79` as cited) |
| E2 | `internal/service/callback_audit.go:47` — `drainPublications` is the durable outbox drain | ✅ `func (s *callbackService) drainPublications` at `callback_audit.go:47`; claims via `ClaimPublication` (5 min lease, limit 50), fan-out under the P6-06 bulkhead, `finishPublicationAttempt` at `:60-77`; `publishAuditFact` at `:97-113` |
| E3 | `internal/service/callback.go:267` — worker ticker calls `drainPublications` | ✅ `s.drainPublications(workerCtx)` inside `Start` ticker at `callback.go:267`, immediately followed by `s.drain(workerCtx)` |
| E4 | `internal/app/audit.go` — `buildAuditPublishers`/`wireAuditServices` | ✅ `buildAuditPublishers` at `audit.go:38` (constructs the auditgovernance publisher when `cfg.AuditGovernance.Enabled`); `wireAuditServices` at `audit.go:17` hands publishers to `callbackSvc.SetAuditPublishers` only |
| E5 | `internal/repository/audit_ledger.go:123` — `AppendInTx` | ✅ `func (r *auditLedgerRepo) AppendInTx` at `audit_ledger.go:123`; both `Append` (`:76`) and `AppendInTx` funnel into `appendInTx` (`:140`), which calls `checkAuditDuplicate` twice (`:142`, `:148`), takes the partition advisory lock, appends to the chain, and creates **both** the `audit_ledger` row and a `status='pending'` `audit_publications` row in the same transaction (`createAuditRows` at `:189-203`) |
| E6 | `internal/repository/audit_ledger.go:203` — `checkAuditDuplicate` | ✅ Function exists at `audit_ledger.go:174` (**C2**: cited line `:203` is stale; dedupe key is `(tenant_id, source, event_id)`, `ErrAuditDuplicate` at `:182`) |
| E7 | Existing `AppendInTx` users: `operation.go:178`, `event_receipt.go:180`, `sync_job.go:111`, `account_profile.go:101` | ✅ All four verified verbatim: `operation.go:178`, `event_receipt.go:180`, `sync_job.go:111`, `account_profile.go:101` — each `r.audit.AppendInTx(ctx, tx, fact)` inside the repo's own `Transaction(...)` |
| E8 | `migrations/000045_audit_publications.up.sql` | ✅ `fact_id uuid NOT NULL REFERENCES audit_ledger(id) ON DELETE CASCADE`, `CONSTRAINT uq_audit_publication_fact UNIQUE (fact_id)`, due/lease indexes. Exactly one publication row per fact, so a rolled-back fact cannot leave an orphan publication |
| E9 | Live write paths still go through standalone `Append` (account view/list, holds, provisioning, callback mutations) | ✅ All seven named sites verified: `account.go:155` (`account.viewed`), `account.go:184` (`account.listed`), `audit_hold.go:59` (`audit_hold.set`), `audit_hold.go:74` (`audit_hold.removed`), `account_provision.go:168` (`account.provisioned`), `callback.go:160` (`callback.registered`), `callback.go:221` (`callback.deleted`), `callback.go:247` (`callback.delivery.replayed`), plus `legacy_cutover.go:88` (`legacy.cutover.set`) — all `_ = s.audit.Record(...)` after a separate repo write, i.e. audit append in a standalone transaction |

Additional verified facts that shape the requirements:

- **A1 (the inline loop is unwired in production).** `internal/app/new.go:146` constructs
  `service.NewAuditLedgerService(auditLedgerRepo)` with **zero** publishers; no other caller passes any
  (`internal/app/audit_integration_test.go:19`, `internal/service/audit_ledger_test.go:43,56`). The loop is
  dead today, but if a publisher were ever wired it would double-deliver against `drainPublications` and
  swallow errors. Removing it is pure deletion with no live behavior change.
- **A2 (the only live delivery path is already the outbox).** `new.go:150` wires publishers into the
  callback service only; `new.go:179` starts `callbackSvc.Start(...)` so the `drainPublications` ticker runs
  in the live app. `callbackService.Publish` (`callback_audit.go:19`, no-op guard `if s.publications != nil` at `:28-31`) already no-ops when the publication
  repository is wired, confirming the design intent that delivery is outbox-driven.
- **A3 (publication lifecycle already exists).** `ClaimPublication` (`audit_ledger.go:205-249`, `FOR UPDATE
  SKIP LOCKED`, claims `pending`+due or expired-lease `delivering` rows) and `FinishPublication`
  (`:251-264`, guarded by `WHERE status='delivering' AND lease_owner=?`, `ErrAuditPublicationLeaseLost` on
  miss) plus `publicationOutcome` (`callback_audit.go:84-95`: `completed` / recoverable `pending`
  (`audit.binding_unavailable` is attempt-free) / `pending` with backoff / `dead_letter` at
  `attempt >= MaxAttempts`) give T-10.2 its reclaim machinery with no new mechanism required.

## 2. Problem statement

`AuditLedgerService.Record` appends the fact in a standalone transaction (`repo.Append`) and then runs an
inline `_ = publisher.Publish(ctx, fact)` loop (E1) that duplicates the durable outbox delivery path (A2).
Publish failures inside that loop are silently dropped — no retry, no dead-letter — so governance events
can be lost even though the durable `audit_publications` mechanism exists and is already the live delivery
path. Separately, several live write paths still append their audit fact after the domain write in a
separate transaction (E9), so the fact (and its pending publication) can diverge from the business
mutation: the mutation can commit without its audit fact, and a forced audit failure cannot roll the
mutation back. The transactional pattern that fixes this already exists and is proven
(E7: `XxxWithAudit` + `AppendInTx`, fail-closed wiring via `requireAuditLedger`).

## 3. Requirements

### REQ-1 — `Record` performs no publisher delivery; the inline loop is deleted

`auditLedgerService.Record` (audit_ledger.go:66) must:
- keep appending via `repo.Append` (fact + `status='pending'` publication row commit atomically, E5);
- keep the `ErrAuditDuplicate → return nil` idempotency behavior and the
  `ObserveAuditIngestFailure` metric on non-duplicate append errors;
- **not** invoke any publisher. The `for _, publisher := range s.publishers` loop (audit_ledger.go:77-81)
  is removed;
- drop the now-unused `publishers []AuditPublisher` field and the variadic `publishers ...AuditPublisher`
  parameter of `NewAuditLedgerService` (audit_ledger.go:54). All current call sites pass zero publishers
  (A1), so this is a compile-checked removal, not a signature churn. The `AuditPublisher` interface and
  `callbackService.Publish` remain: they serve `publishAuditFact` on the drain path (callback_audit.go:97).

### REQ-2 — `drainPublications` is the sole delivery path

Delivery of a fact to callback deliveries and governance endpoints happens only through the outbox worker:
`Start` ticker → `drainPublications` (callback.go:267, callback_audit.go:47) → `ClaimPublication` →
`publishAuditFact` (enqueue callback deliveries + per-tenant-bound governance `Publish`) →
`FinishPublication` with `completed | pending | dead_letter` (A3). No code path other than
`drainPublications` may deliver a fact that has a publication row. No behavioral change to
`drainPublications` itself is required; REQ-6 proves the invariant.

### REQ-3 — Finish AppendInTx consolidation: the seven named write paths become transactional

Each of the following write paths must append its audit fact in the **same business transaction** as the
domain mutation, using the proven pattern (E7): repository constructor takes
`AuditLedgerTransactionalRepository` (fail-closed via `requireAuditLedger`), service builds the fact, repo
opens `Transaction(...)`, performs the domain write, then `AppendInTx(ctx, tx, fact)`:

| Path | Current site | Fact (action, event_id) to preserve |
|---|---|---|
| callback register | `callback.go:160` | `callback.registered`, `EventID: subscription.ID` |
| callback delete | `callback.go:221` | `callback.deleted`, `EventID: id` |
| callback delivery replay | `callback.go:247` | `callback.delivery.replayed`, `EventID: id` |
| legal hold set | `audit_hold.go:59` | `audit_hold.set`, `EventID: idgen.NewID()` |
| legal hold remove | `audit_hold.go:74` | `audit_hold.removed`, `EventID: idgen.NewID()` |
| legacy cutover decision | `legacy_cutover.go:88` | `legacy.cutover.set`, `EventID: idgen.NewID()` |
| account provisioning | `account_provision.go:168` | `account.provisioned`, `EventID: account.ID` (dedupe key) |

Existing fact content — source `"aero-id"`, actor, tenant, resource type/id, payload — is preserved.
`NewCallbackRepository` and `NewAuditHoldRepository` currently take no ledger
(`repository/callback.go:36`, `repository/audit_hold.go:66`); they gain the fail-closed ledger dependency
like `NewAccountRepository` (`repository/account.go:48-49`). For provisioning, the fact is written only
when the account is actually created (the `EventID: account.ID` dedupe already makes repeat `ensure`
calls no-ops); the repo-level `EnsureByIdentity`/creation write gains the audit variant.

### REQ-4 — AppendInTx failure fails the mutation

When the in-transaction audit append fails (e.g. `ErrAuditDuplicate` from `checkAuditDuplicate`, or a
ledger error), the whole business transaction rolls back: the domain mutation is not committed, no
`audit_ledger` row and no `audit_publications` row exist, and the service returns the mapped
`*pkg/errors.AppError` (repository returns raw errors; services translate, per AGENTS.md). This is the
already-proven AC3b behavior (audit_wiring_integration_test.go:163) extended to the newly converted paths
and to the publication table (REQ-6/T-5).

### REQ-5 — Gate: no `_ = publisher.Publish` in `internal/service`

Add a Makefile gate (script precedent: `scripts/check-architecture.sh`, `Makefile:27-28`) that fails when
any `.go` file under `internal/service/` contains `_ = publisher.Publish`. Suggested target name:
`check-single-delivery-path` (distinct from the separate direction's `check-audit-swallow`). The gate must
pass with zero matches after REQ-1 and must be listed in the done-gate documentation (AGENTS.md
`make test && make lint && make lint-strict && make check-file-sizes && make build`).

### REQ-6 — Tests (acceptance checks from the direction, made testable)

- **T-5 (atomicity, both tables).** Extend the repository integration suite
  (`internal/repository/audit_wiring_integration_test.go`, `//go:build integration`, `testpg.Start` +
  `testpg.Migrate`): add a `countAuditPublicationRows(t, db, eventID)` helper (JOIN `audit_ledger` on
  `fact_id`). In `TestAuditWiringHappyPath`, assert exactly one `audit_publications` row per committed
  fact (currently only `audit_ledger` is counted). In `TestAuditWiringRollbackAtomicity` (forced rollback
  via a fresh mutation reusing an already-committed `event_id` → `ErrAuditDuplicate`), additionally assert
  zero `audit_publications` rows for the failed fact alongside the existing zero-ledger-row assertion —
  the two tables disappear together.
- **T-5 (service error, no fact).** For at least one converted path (e.g. `auditHoldService.Put` or
  `callbackService.Register`): a business mutation whose `AppendInTx` fails returns the service error
  (mapped AppError, e.g. `ErrAuditDuplicate` → conflict/duplicate code) and writes no fact — assert the
  domain row is unchanged and zero `audit_ledger`/`audit_publications` rows for that event_id. Repository
  integration test forces the failure; service-level test uses a fake repo and asserts the error is
  propagated rather than swallowed.
- **T-10.2 (reclaim + exactly one delivery).** Repository integration test: `Append` a fact →
  `ClaimPublication` returns the pending row → simulate a failing publisher with
  `FinishPublication(id, worker, "pending", "audit.publication_failed", next=past)` → the next
  `ClaimPublication` returns the same fact again (reclaimed, `attempt` incremented) →
  `FinishPublication(..., "completed", ...)` → a further `ClaimPublication` returns nothing. Assert
  exactly one publication row per fact across the whole cycle (UNIQUE `fact_id`). Service-level test:
  `Record` with a recording fake `AuditPublisher` performs **zero** `Publish` calls (inline loop gone —
  the "no double publish from a legacy inline path" half), and a `callbackService` with a fake publication
  repo delivers each claimed fact exactly once per claim via `publishAuditFact`.
- **Gate (REQ-5).** `make check-single-delivery-path` fails on a fixture file containing
  `_ = publisher.Publish(` under `internal/service/` and passes after REQ-1 lands.

## 4. Acceptance checks (as supplied, now testable)

| Check | Testable formulation | Where |
|---|---|---|
| T-5: mutation + forced rollback asserts both `audit_ledger` and `audit_publications` rows disappear together (atomic) | `TestAuditWiringRollbackAtomicity` extended: after a duplicate-`event_id` `WithAudit` mutation fails, `count(*) FROM audit_ledger WHERE event_id=?` **and** `count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id=ap.fact_id WHERE l.event_id=?` are both 0; happy path asserts both are 1 | `internal/repository/audit_wiring_integration_test.go` (integration tag) |
| T-5: a business mutation whose `AppendInTx` fails returns the service error and writes no fact | Forced `ErrAuditDuplicate` (or repo error) inside a converted path returns the mapped service error; domain row unchanged; zero rows in both tables for that event_id | repository integration test + service unit test with fake repo |
| T-10.2: failing publisher leaves `status='pending'` row reclaimed by `drainPublications` on the next tick and exactly one delivery per fact (no double publish from a legacy inline path) | Claim → finish `pending` (past `next_attempt_at`) → reclaim → finish `completed` → empty claim; one publication row per fact throughout; `Record` invokes zero publishers | `internal/repository/audit_ledger_integration_test.go` + `internal/service` unit tests |
| Gate: zero matches for `_ = publisher.Publish` in `internal/service` | `make check-single-delivery-path` fails on fixture, passes on tree | Makefile target + script |

## 5. Out of scope (explicitly not part of this direction)

- **`_ = s.audit.Record/Log` error-swallow conversion and the `check-audit-swallow` gate** — separate
  direction (direction 2 of the same analysis); this direction only removes the *publisher* swallow.
- **TokenSource 401 invalidation / scope-missing alerting** — separate direction (direction 3).
- **`account.viewed` / `account.listed`** (account.go:155,184) are sensitive-read audits with no business
  write transaction to join; per the contract they stay on `Record` (standalone append) with
  observability. REQ-3 covers only the seven named write paths.
- **`event_ingest.go:154`, `replay.go:145,240`, `operation.go:136`, `sync_job.go:163,467`** Record calls
  — these already sit on top of transactional `WithAudit` repository methods (E7) or are outbox/ingest
  paths owned by other B2 items; no change here unless a named path above requires it.
- No change to `drainPublications`, `ClaimPublication`, `FinishPublication`, or migration 000045 semantics.

## 6. Verification

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-single-delivery-path && make build
make test-integration   # after changing repository SQL/wiring (audit_wiring, audit_ledger integration tests)
```
