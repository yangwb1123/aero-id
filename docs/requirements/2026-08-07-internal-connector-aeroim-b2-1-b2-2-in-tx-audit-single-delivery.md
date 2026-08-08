# Requirements — B2-1+B2-2 (module label `internal/connector/aeroim`): in-transaction audit writes via `AppendInTx`/`XxxWithAudit`, outbox-only delivery, dead inline publisher loop stays deleted (T-5)

| | |
|---|---|
| Module (batch label) | `internal/connector/aeroim` — **label only**. The direction's change surface is `internal/service` + `internal/repository` (+ composition root `internal/app`). `internal/connector/aeroim/aeroim.go` is a 7-line adapter (`connector.NewJSONSummaryConnector(connector.SourceAeroIM, cfg)`) with no audit code; it is untouched by this direction. The analysis file is a campaign-batch replication of the audit-governance directions across module labels |
| Source analysis | `docs/auto/analyses/internal-connector-aeroim-087e0c23.json` (direction 2 of 3: B2-1+B2-2) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery path; gate G3: T-5). Sibling specs for the same direction under other labels: `2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop.md`, `2026-08-06-cmd-server-complete-in-tx-audit-writes.md`, `2026-08-06-internal-repository-audit-wiring-fail-closed.md`, `2026-08-06-cmd-migrate-check-audit-swallow-gate.md` |
| Date | 2026-08-07 |
| Status | Evidence re-verified against head `13e4676` **plus the uncommitted working tree** (128 modified files; the migration has progressed past every sibling spec). Two of the direction's three central claims are already landed; the residual delta is §3.2. Corrections C1–C6 in §10 amend the source analysis |

## 1. Problem statement (direction)

The direction asserts three things: (1) `AuditLedgerService.Record` runs a **dead inline publisher loop**
(`for _, publisher := range s.publishers { _ = publisher.Publish(ctx, fact) }`) duplicating outbox delivery;
(2) the composition root wires publishers only into the callback-service outbox worker, so the loop is dead
code that is itself a swallowed audit call; (3) 25 `_ = s.audit.Record/Log(...)` swallow sites bypass the
proven `AppendInTx` + atomic `audit_publications` outbox row pattern, so a business-transaction rollback can
leave a durable audit fact claiming an operation that never committed.

At head `13e4676` + working tree, (1) and (2) are **already resolved in committed history**
(§2 E1/E2/E4), and (3) is resolved for five of the six named write paths (§2 E5). The **residual open scope**
is: two write paths (`audit_hold` put/remove, `legacy_cutover` set) still append the audit fact **after** the
domain commit in a second transaction (§3.1), the T-5 rollback-injection tests do not yet cover those two
paths, and the read-path observability counter has no direct unit test (§3.2). This spec keeps the landed
state enforced (REQ-1/2/3/8) and completes the residual (REQ-4/5/6/7) without expanding scope.

## 2. Verified evidence (head `13e4676` + working tree; line numbers live)

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `AppendInTx` exists with partition advisory lock + atomic `audit_publications` outbox row (`internal/repository/audit_ledger.go:110-200`, `createAuditRows:246`) | ✅ stands with drift (**C1**): interface `repository/audit_ledger.go:39`; impl `AppendInTx` `:106-124`; both `Append` (`:90-104`, own `db.Transaction`) and `AppendInTx` funnel into `appendInTx` (`:123-156`): `pg_advisory_xact_lock(hashtext(partition_key))` (`:128`), dedup `checkAuditDuplicate` (`:157-171`), chain append, then `createAuditRows` (`:172-208`) inserts the ledger row **and** the `status='pending'` `audit_publications` row in the same tx. File is **442 lines** (direction said 499). Signing-key fail-closed guard at `:92` (`Append`) and `:108` (`AppendInTx`) |
| E2 | Inline publisher loop at `internal/service/audit_ledger.go:76-81`; `Record` → `repo.Append` (separate tx) | ⚠️ **C2 — loop already deleted** (commit `90c4c24`, 2026-08-05). Current `auditLedgerService` has no `publishers` field; `NewAuditLedgerService(repo)` is single-argument (`:56-58`); doc comment `:54-55`: "Record only appends, it never publishes"; `Record` (`:68-79`) calls `s.repo.Append(ctx, fact)` only, returns nil on `ErrAuditDuplicate` (`:70-72`), observes `ObserveAuditIngestFailure` on non-duplicate failure (`:75`). `grep -rn '_ = publisher.Publish' internal/` → 0 hits |
| E3 | Composition root: `internal/app/new.go:142-150` constructs `NewAuditLedgerService(auditLedgerRepo)` with no publishers | ✅ holds, relocated: `new.go:134-138` calls `buildAuditStack`; `buildAuditStack` (`internal/app/audit.go:34-57`) calls `service.NewAuditLedgerService(auditLedgerRepo)` at `:51` with zero publishers, then `wireAuditServices` (`audit.go:65-85`) hands the governance publishers **and** the publication repo to `callbackSvc` only (`SetAuditPublicationRepository`/`SetAuditPublishers`), with fail-closed panics on any wiring regression |
| E4 | Only live delivery is the outbox worker `drainPublications` (`internal/service/callback_audit.go:50-118`), ticker at `internal/service/callback.go:267` | ✅ exact: `drainPublications` at `callback_audit.go:38-66`, `finishPublicationAttempt` `:68-88`, `publicationOutcome` `:90-101`, `publishAuditFact` `:103-117`; the **only** `AuditPublisher.Publish` call in the tree is `callback_audit.go:114`; `enqueueFact` (`:20-36`) doc comment: "reachable solely from publishAuditFact"; `callbackService.Start` (`callback.go:268-287`) calls `s.drainPublications(workerCtx)` at `:281` — the only call site. Other `.Publish` call sites are different interfaces: `projection_replication.go:82` (`event.EventPublisher`), `notification.go:281,294` (SSE hub pusher) — out of scope |
| E5 | 25 swallow sites, e.g. `account.go:155,184; callback.go:160,221; account_provision.go:168; sync_job.go:163,467; audit_hold.go:59,74; legacy_cutover.go:88` | ⚠️ **C3 — mostly resolved or never-swallows.** `grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service/*.go` → **0 hits**; the gate is green (§2 E7). Per site at head+working tree: **(a)** `callback.go` → in-tx `registerWithAudit` (`:160-173`) → `CreateSubscriptionWithAudit`, `Delete` (`:210-232`) → `DeleteSubscriptionWithAudit`, `ReplayDelivery` (`:236-266`) → `ResetDeliveryWithAudit`; **(b)** `account_provision.go` → `provisionAccountWithAudit` (`:140-178`) → `EnsureByIdentityWithAudit`/`EnsureByIdentityWithQuotaWithAudit`; **(c)** `sync_job.go:163,467` → **not swallows**: `createWithAudit` at `:155` (`service/sync_audit.go:20-32`) and `FinishWithAudit` at `:461-469` — both propagate; **(d)** `account.go:155,184` → read-path `account.viewed`/`account.listed` at `:154-161`/`:181-190`, **checked** (`apperr.Unavailable("audit.ingest_failed")`), no business transaction exists — correctly out-of-tx; **(e)** `audit_hold.go:59,74` → **still out-of-tx but checked** (RESIDUAL, §3.1): `Put` `:44-77` (`PutHold` `:55`, then `Record` `:69`), `Remove` `:79-117` (`RemoveHold` `:84`, then `Record` `:96`), error → `audit_hold.audit_failed`; **(f)** `legacy_cutover.go:88` → **still out-of-tx but checked** (RESIDUAL): `Set` `:70-131` (`SetCutover` `:84`, then `Record` `:97`), error → `cutover.audit_failed`. Additionally the 10 `_ = s.audit.Log(...)` sites noted by sibling docs (admin/ban/block/comment/file_share/message) are **converted to checked** `if err := s.audit.Log(...)` in the working tree (`admin.go:93,122`, `ban.go:84,122`, `block.go:54,79`, `comment.go:415`, `file_share.go:114,148`, `message.go:358`). Only the 6 legacy dead-tree swallows remain (`internal/file/service_share.go:45,75`, `internal/block/block.go:66,86`, `internal/comment/service.go:431`, `internal/message/service.go:289`) — unwired tree, excluded by the gate by design, out of scope for this direction |
| E6 | No `check-audit-swallow` Makefile target; requirement doc `docs/requirements/2026-08-06-cmd-migrate-check-audit-swallow-gate.md` | ⚠️ **C4 — gate landed.** `Makefile:52-54` → `scripts/check-audit-swallow.sh` (P1 bans `_ = <recv>.(Record\|Log\|Publish\|FinishPublication\|ClaimPublication)(`; P2 bans bare `Record|Log|FinishPublication|ClaimPublication(` calls; self-test fixtures under `scripts/testdata/audit-swallow/`; `Log` is a first-class citizen in both patterns; `Publish` excluded from P2 only, with the SSE/event-publisher false positives documented in the header). Wired into the AGENTS.md done gate. **Verified green by running `make check-audit-swallow` → exit 0, 0 matches** (self-test ok). The cited requirement doc exists and is current |
| E7 | `XxxWithAudit` + `AppendInTx` pattern proven in-tree | ✅ `UpdateProfileWithAudit` (`repository/account_profile.go:67-71`, append last statement before commit `:101`); `CreateSubscriptionWithAudit`/`DeleteSubscriptionWithAudit`/`ResetDeliveryWithAudit` (`repository/callback_audit.go:31,60,85`; `ErrAuditDuplicate` swallowed in-tx only, after the domain write, documented `:36-40`); `EnsureByIdentityWithAudit`/`EnsureByIdentityWithQuotaWithAudit` (`repository/account_provision_audit.go:31,56`); `CreateWithAudit`/`FinishWithAudit` (`repository/sync_job.go:86,292`; `repository/sync_job_audit.go:55`); operation `CreateWithAudit`/`FinishStepWithAudit`/`FinishCompensationStepWithAudit`/`SetStatusWithAudit` (`repository/operation.go:36-43,135,421,434,452`; `repository/operation_audit.go:136`); `ApplySnapshotEventWithAudit` (`repository/event_receipt.go:89`, `AppendInTx` `:180`). Fail-closed wiring precedent `requireAuditLedger` (`repository/audit_wiring.go:14-28`, panic on nil/typed-nil) |
| E8 | `internal/repository/audit_ledger.go` at the 500-line gate | ✅ not anymore: **442 lines** (**C5**); new replay/hold work must still prefer sibling files per the repo gate |
| E9 | No `PutHoldWithAudit`/`RemoveHoldWithAudit`/`SetCutoverWithAudit` anywhere | ✅ verified: `grep -rn 'WithAudit' internal/repository/audit_hold.go internal/repository/legacy_cutover.go` → 0 hits. `auditHoldRepo{db}` (`repository/audit_hold.go:62-68`) and `legacyCutoverRepo{db}` (`repository/legacy_cutover.go:76-82`) have no ledger field; constructors `NewAuditHoldRepository(gdb)` (`app/audit.go:187`) and `NewLegacyCutoverRepository(gdb)` (`app/new.go:228,411`) take none. Both services' post-commit `Record` calls use **anchored** EventIDs (`idgen.Anchor("audit_hold.set", tenant, scopeType, scopeID, reason, idgen.RenderAnchorTime(expiresAt))` `audit_hold.go:70-72`; `"audit_hold.removed"` `:97-99`; `"legacy.cutover.set"` `legacy_cutover.go:98-100`), so a retry re-derives the same event_id and the in-tx duplicate swallow folds it — the migration changes no fact content |
| E10 | T-5 rollback-injection coverage exists for migrated paths | ✅ for profile/operation/event: `TestAuditWiringHappyPath` (`audit_wiring_integration_test.go:60`), `TestAuditWiringRollbackAtomicity` (`:175`, duplicate-append → whole tx rolls back), `TestAuditWiringPublicationInsertRollsBackLedgerRow` (`:268`, trigger-injected publication-insert failure → 0 ledger + 0 publication + 0 domain rows). ✅ callback: `TestCallbackAuditHappyPath` (`callback_audit_integration_test.go:54`), `TestCallbackAuditRollbackWithoutSigningKey` (`:129`, keyless-ledger failure → 0 subscription + 0 ledger + 0 publication; reset leaves the delivery byte-identical; delete preserves the row). ✅ provision: `TestEnsureByIdentityWithAuditCreatesAccountAndFact` (`account_provision_integration_test.go:56`), `...RollbackWithoutSigningKey` (`:114`), `...RepeatAppendsNothing` (`:140`), `...ConcurrentProvision` (`:203`). ✅ sync finish parity: `TestSyncJobFinishedParity` (`anchor_dedup_integration_test.go:311-342`). ❌ **hold/cutover: no WithAudit exists, hence no such tests** (RESIDUAL) |
| E11 | Read-path counter `ObserveAuditIngestFailure` | ✅ wired: interface `service/control_plane_metrics.go:15`, noop `:32`, registry impl `:61-62`; fired from `Record` on non-duplicate failure (`service/audit_ledger.go:75`). ❌ **no unit test asserts the counter fires when `Record` fails** — `metricSpy` (`service/callback_audit_test.go:79`) implements the interface but is never asserted for ingest failures (RESIDUAL) |

## 3. Current state and residual delta

### 3.1 Landed (keep-enforced, no re-do)

- Dead inline publisher loop deleted (commit `90c4c24`); `NewAuditLedgerService` single-arg; `Record` appends only (E2).
- Outbox-only delivery: `drainPublications` ← `callbackService.Start` ticker is the only `AuditPublisher.Publish` reachability (E4).
- Composition root wires publishers + publication repo into the callback service only, fail-closed (E3).
- `make check-audit-swallow` gate exists, wired into the done gate, green at 0 (E6).
- In-tx `XxxWithAudit` conversions: account profile, callback register/delete/replay-reset, account provision (both variants), sync job create/finish, operations, event-ingest (E7).
- 10 live `Log` sites converted from `_ = s.audit.Log(...)` to checked calls (E5).
- Anchored EventIDs on all remaining post-commit sites (E9).

### 3.2 Residual (this spec's actionable scope)

1. **`auditHoldService.Put`/`Remove` append after commit** (`audit_hold.go:69,96`): the domain write (`PutHold`/`RemoveHold`) commits in its own transaction, then `Record` appends in a second one. A crash between the two silently loses the fact; a forced audit failure cannot roll the domain write back; a domain rollback after a committed fact leaves a phantom ledger row. No `PutHoldWithAudit`/`RemoveHoldWithAudit` exists (E9).
2. **`legacyCutoverService.Set` appends after commit** (`legacy_cutover.go:97`): same gap; no `SetCutoverWithAudit` (E9).
3. **T-5 rollback-injection tests missing for hold/cutover** (E10): acceptance (a) requires "for each migrated write path … assert neither `audit_ledger` nor `audit_publications` row exists (rollback), and assert ledger row + outbox row exist on success" — profile/operation/event/callback/provision have such tests; hold/cutover cannot until REQ-4/REQ-5 land.
4. **Read-path counter has no unit test** (E11): acceptance (d) is code-true but not test-asserted.

## 4. Requirements

### REQ-1 — `Record` stays append-only (keep-enforced)

`NewAuditLedgerService` must remain single-argument; `auditLedgerService` must keep no `publishers` field and no publish call of any form inside `Record` (`service/audit_ledger.go:68-79`); `Record` must keep `ErrAuditDuplicate → nil` and `ObserveAuditIngestFailure` on non-duplicate failure. **Test:** `TestAuditLedgerRecordEnqueuesOnly` (`service/audit_ledger_test.go:47`) plus gate P1/P2 stay green (§9.2).

### REQ-2 — Outbox-only delivery (keep-enforced)

`drainPublications` (`callback_audit.go:38`) must remain the only delivery entry point; `publishAuditFact` (`:103-117`) the only `AuditPublisher.Publish` call; `callbackService.Start` ticker (`callback.go:281`) the only `drainPublications` caller; `enqueueFact` reachable solely from `publishAuditFact`. **Test:** §9.3 grep invariants + `TestDrainPublicationsEarlyReturnObservesMetric` (`callback_audit_test.go:176`) and `TestAuditLedgerRecordEnqueuesOnlyIntegration` (`app/audit_integration_test.go:276`) stay green.

### REQ-3 — `make check-audit-swallow` stays green at 0 (keep-enforced)

The gate (`Makefile:52-54` → `scripts/check-audit-swallow.sh`) must keep matching all three acceptance shapes — `_ = s.audit.*`, `_ = publisher.Publish`, `_ = s.publications.*` (P1 covers `Record|Log|Publish|FinishPublication|ClaimPublication`; P2 covers bare `Record|Log|FinishPublication|ClaimPublication`) — and must report 0 matches under `internal/service`. Self-test fixtures (`scripts/testdata/audit-swallow/`) must keep passing. **Test:** §9.2 red/green + negative control.

### REQ-4 — Migrate `audit_hold` put/remove into the business transaction

1. `repository/audit_hold.go`: add an audit-bearing surface, e.g. `AuditHoldAuditRepository` with `PutHoldWithAudit(ctx, hold, fact)` and `RemoveHoldWithAudit(ctx, tenantID, scopeType, scopeID, fact)`; give `auditHoldRepo` a `FullAuditLedgerRepository` field via `requireAuditLedger("audit_hold", audit)` (`repository/audit_wiring.go:14-28`); extend `NewAuditHoldRepository(db, audit)`.
2. Each method wraps the existing domain statement (upsert with `RETURNING` copy-back `repository/audit_hold.go:77-102`; delete `:94-98`) and `r.audit.AppendInTx(ctx, tx, fact)` in one `db.Transaction`, with the append as the last statement; `ErrAuditDuplicate` is swallowed **in-tx only** (callback precedent, `repository/callback_audit.go:36-40`: the swallow runs after the domain write inside the same tx and can never mask a failed domain write).
3. `service/audit_hold.go` `Put`/`Remove`: build the fact **before** the repo call from the same post-normalization values used today (trimmed `ScopeID`, raw `reason`, the caller's `expiresAt` rendered via `idgen.RenderAnchorTime`; the anchor inputs do not include the RETURNING copy-back values, so pre-building is byte-identical); call the WithAudit variant when the repo implements it (fail-closed `apperr.Internal("audit_hold.audit_unavailable", ...)` on assert failure — callback precedent `callback.go:162-167`), keeping the existing checked post-commit `Record` fallback only for non-audit repositories (sync precedent `sync_job.go:162-169`; unreachable for the concrete repo). Keep both error codes live: `audit_hold.save_failed` / `audit_hold.audit_failed`.
4. Preserve `Remove` parity: the fact records even when 0 rows are deleted (current behavior; a RowsAffected gate would be a behavior change outside this direction).
5. Composition root `app/audit.go:187`: `NewAuditHoldRepository(gdb, auditLedgerRepo)`.

### REQ-5 — Migrate `legacy_cutover` set into the business transaction

Mirror REQ-4 exactly: `LegacyCutoverAuditRepository.SetCutoverWithAudit(ctx, cutover, fact)` on `repository/legacy_cutover.go` (upsert `:96-107` + `AppendInTx` in one tx, append last, `ErrAuditDuplicate` in-tx only); `legacyCutoverRepo` gains the ledger via `requireAuditLedger`; `NewLegacyCutoverRepository(db, audit)`; service `Set` (`legacy_cutover.go:96-131`) builds the anchored fact pre-call (`idgen.Anchor("legacy.cutover.set", tenant, status, current)` — all inputs known pre-tx), calls `SetCutoverWithAudit` fail-closed (`cutover.audit_unavailable`), keeps the checked post-commit fallback for non-audit repos, keeps codes `cutover.save_failed` / `cutover.audit_failed`; composition root `app/new.go:228,411` pass `auditLedgerRepo`.

### REQ-6 — T-5 rollback-injection tests for hold and cutover

Repository-level integration tests (testpg, `//go:build integration`) mirroring `TestCallbackAuditRollbackWithoutSigningKey`:
1. **Happy path** (signed key): `PutHoldWithAudit`/`RemoveHoldWithAudit`/`SetCutoverWithAudit` commit exactly 1 `audit_ledger` row + 1 `pending` `audit_publications` row with the domain row.
2. **Rollback injection**: keyless ledger (fail-closed guard `repository/audit_ledger.go:112-113`) → the WithAudit call fails and **no** domain row, **no** ledger row, **no** publication row survives; for remove/reset, the pre-state row must be byte-identical after the failed call.
3. **In-tx duplicate swallow**: re-execution with the identical anchored fact (already-committed hold/cutover) succeeds via `ErrAuditDuplicate` and produces no second ledger row.

Service-level: with a fake repo **without** the audit surface, a failing audit spy makes `Put`/`Remove`/`Set` return `audit_hold.audit_failed` / `cutover.audit_failed` (extension of the existing patterns at `service/audit_hold_test.go:118`, `legacy_cutover_test.go:156` per sibling spec); with a fake repo **with** the audit surface, the fallback `Record` is never invoked (recording spy, zero calls).

### REQ-7 — Read-path counter test (acceptance d)

Unit test in `internal/service/audit_ledger_test.go`: a counting metrics spy + failing `auditLedgerRepoSpy` (`appendErr` set, `:13-45` shape) → `Record` returns the error **and** `ObserveAuditIngestFailure` was called exactly once with the fact's source; a duplicate `appendErr = repository.ErrAuditDuplicate` → returns nil **and** the counter is not incremented. This pins acceptance (d) for the read-path sites `account.go:154-161,181-190` (which surface `audit.ingest_failed` and rely on the counter).

### REQ-8 — Reachability keep-enforced (acceptance c)

No code change required; §9.3 turns the reachability claim into a checkable invariant: the only `AuditPublisher.Publish` call is `callback_audit.go:114`; the only `drainPublications` call is `callback.go:281`; `enqueueFact` is called only from `publishAuditFact`; the gate's P1 keeps `_ = publisher.Publish` red.

## 5. Compatibility constraints

- **Wire/proto/API:** no HTTP/gRPC/proto/migration changes; no SQL; no new error codes at the transport boundary — `audit_hold.audit_failed`, `audit_hold.save_failed`, `cutover.audit_failed`, `cutover.save_failed` keep their meanings and remain reachable (REQ-4.3/REQ-5).
- **Fact parity:** hold/cutover facts keep byte-identical content and anchored EventIDs when moved in-tx (E9; REQ-4.3) — retry-fold via `ErrAuditDuplicate` is preserved.
- **Semantics parity:** `Remove` records the fact even on 0 rows deleted (REQ-4.4); `Set` records after a validated transition only (unchanged).
- **Fallback tails:** the post-commit `Record` fallback survives only for non-audit repositories (test fakes); concrete repos always satisfy the audit surface, so the crash window is closed in the composition root. `requireAuditLedger` panics at wiring time if the ledger is missing.
- **File-size gate:** `repository/audit_hold.go` (~108 lines) and `repository/legacy_cutover.go` (~130 lines) absorb the WithAudit methods without approaching 500 lines; no splits required. New test files land beside the code.
- **Dead-tree swallows:** the 6 legacy `internal/{file,block,comment,message}` sites remain out of scope (unwired tree; gate scans `internal/service` only, documented in `scripts/check-audit-swallow.sh`).
- **`0 = disabled` convention:** unchanged; the `s.audit != nil` guards and the fallback branches keep unconfigured-audit unit tests working.

## 6. Failure modes

| Failure | Current behavior | After REQ-4/REQ-5 |
|---|---|---|
| Crash between domain commit and audit append (hold/cutover) | Fact silently lost; ledger has no record | Impossible: fact + outbox row commit with the domain row (or not at all) |
| Domain write fails after audit appended (hold/cutover) | Phantom durable fact claims an operation that never committed | Whole tx rolls back: 0 domain + 0 ledger + 0 publication rows |
| Ledger down / signing key unconfigured | Hold/cutover commit succeeds, `Record` error surfaced (checked) but domain write already durable | Mutation fails atomically; nothing commits; `audit_hold.audit_failed`/`cutover.audit_failed` still reachable via fallback path for fakes |
| Duplicate retry (same anchor) | `ErrAuditDuplicate` → nil, second `Record` dedups | Same, in-tx: duplicate swallow after the domain write, no second ledger row |
| Future wiring regression re-adds a publish/append bypass | Gate P1/P2 red; `TestAuditLedgerRecordEnqueuesOnly` red | Unchanged (REQ-1/2/3 keep-enforced) |

## 7. Migration steps (ordered)

1. **Baseline proof:** `make check-audit-swallow` → exit 0 (verified 2026-08-07); `grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service/*.go` → 0 hits; `grep -rn 'WithAudit' internal/repository/audit_hold.go internal/repository/legacy_cutover.go` → 0 hits.
2. **Repo surface (REQ-4/REQ-5):** add `AuditHoldAuditRepository` + `LegacyCutoverAuditRepository` with the WithAudit methods (append last, `ErrAuditDuplicate` in-tx only); extend constructors with `requireAuditLedger`.
3. **Service (REQ-4/REQ-5):** rework `audit_hold.go Put/Remove` and `legacy_cutover.go Set` to pre-built anchored facts + fail-closed audit-surface assertion + kept fallback; update the two error-code comments.
4. **Composition root:** `app/audit.go:187`, `app/new.go:228,411` pass `auditLedgerRepo`.
5. **Tests (REQ-6):** add the repository integration tests (happy path, keyless-ledger rollback, duplicate fold) and the service tests (fallback fires with non-audit fake; never fires with audit fake). They are **red before** steps 2-4 (no WithAudit symbols) and green after.
6. **Read-path counter test (REQ-7):** add the counting-spy unit test; red today (no assertion exists — the code path is live but unpinned), green after.
7. **Gate:** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make build`; `make test-integration` required (repository/SQL-boundary tests changed).

Rollback: revert steps 2-4 and the new tests; no data migration exists.

## 8. Testable acceptance mapping (supplied T-5, preserved verbatim)

| Supplied acceptance | Requirement | Testable criteria (all runnable) | Status today |
|---|---|---|---|
| **(a)** for each migrated write path (account profile, callback sub/delivery, account provision, sync job, audit hold, legacy cutover), inject a business-tx failure after the audit call — assert neither `audit_ledger` nor `audit_publications` row exists (rollback), and assert ledger row + outbox row exist on success | REQ-4, REQ-5, REQ-6 | Per path, integration tests with the in-tree injection mechanisms: keyless-ledger failure (fail-closed guard) and duplicate-append/trigger-injected failure → `count(audit_ledger)=0`, `count(audit_publications)=0`, domain row absent; signed-key happy path → domain row + exactly 1 ledger row + 1 `pending` publication row. Covered today: profile/operation/event (`audit_wiring_integration_test.go:60,175,268`), callback (`callback_audit_integration_test.go:54,129`), provision (`account_provision_integration_test.go:56,114,140`), sync (`anchor_dedup_integration_test.go:311`). **Delta: hold put/remove and cutover set cases (REQ-6)** | 5/7 paths covered; hold/cutover red (no symbols) |
| **(b)** grep / `make check-audit-swallow` gate: zero `_ = s.audit.*` / `_ = publisher.Publish` / `_ = s.publications.*` sites | REQ-3 | `make check-audit-swallow` exits 0 with 0 matches under `internal/service` (P1 covers all three shapes: `Record\|Log\|Publish\|FinishPublication\|ClaimPublication`); negative control: temporarily add `_ = s.audit.Record(...)` → red, remove → green; no false positives on `if err :=` / `return` / `err =` forms (P2 anchors at line start; fixtures in `scripts/testdata/audit-swallow/`) | ✅ green (verified exit 0) |
| **(c)** assert `publisher.Publish` is reachable only from `callback_audit.go` `publishAuditFact` (dead loop in `audit_ledger.go` removed) | REQ-2, REQ-8 | `grep -rn '\.Publish(' internal/service/*.go` (non-test): the only `AuditPublisher.Publish` call is `callback_audit.go:114`; the only `drainPublications` call is `callback.go:281`; `enqueueFact` called only from `publishAuditFact`; `audit_ledger.go` contains no publish symbol; gate P1 keeps `_ = publisher.Publish` red; `TestAuditLedgerRecordEnqueuesOnly` (`service/audit_ledger_test.go:47`) + `TestAuditLedgerRecordEnqueuesOnlyIntegration` (`app/audit_integration_test.go:276`) assert Record never delivers. Non-audit `.Publish` call sites (SSE hub `notification.go:281,294`, `event.EventPublisher` `projection_replication.go:82`) are different interfaces — out of scope, documented | ✅ holds; keep-enforced |
| **(d)** read-path `Record` sites keep an observability counter (`ObserveAuditIngestFailure`) on failure | REQ-7 | New unit test in `service/audit_ledger_test.go`: failing repo spy + counting metrics spy → `Record` returns error and the counter increments exactly once with the fact source; `ErrAuditDuplicate` → nil and counter not incremented. Read-path sites `account.go:154-161,181-190` surface `audit.ingest_failed` and rely on this counter | Code wired (`audit_ledger.go:75`, `control_plane_metrics.go:61-62`); **no assertion exists** (delta) |

## 9. Amendment register (corrections to the source analysis)

| # | Stale wording (direction evidence) | Correction | Folded into |
|---|---|---|---|
| C1 | `AppendInTx` at `repository/audit_ledger.go:110-200`, `createAuditRows:246`; file at the 499-line gate | `AppendInTx` at `:106-124`, `createAuditRows` at `:172-208`; file is 442 lines | §2 E1/E8 |
| C2 | Dead inline publisher loop at `service/audit_ledger.go:76-81` | Already deleted in commit `90c4c24` (2026-08-05); `NewAuditLedgerService` is single-arg; `Record` appends only; zero `_ = publisher.Publish` in the tree. Requirement is keep-enforced (REQ-1/REQ-2), not new work | §2 E2, §3.1 |
| C3 | "25 swallow sites … `sync_job.go:163,467`" | Zero `_ = s.audit.*` sites remain in `internal/service`; `sync_job.go:155,461` are propagation sites (never swallows); the listed `account.go`/`callback.go`/`account_provision.go` sites are converted to in-tx WithAudit; `audit_hold.go:69,96` and `legacy_cutover.go:97` are the only remaining out-of-tx sites and are **checked**, not swallowed. The 10 `_ = s.audit.Log` sites from sibling docs are converted to checked calls in the working tree; only the 6 legacy dead-tree swallows remain (out of scope) | §2 E5 |
| C4 | "No `check-audit-swallow` Makefile target exists" | Target exists (`Makefile:52-54`), script has P1+P2 with self-test fixtures, wired into the done gate, verified green; `Log` is now a first-class gate citizen in both patterns | §2 E6, §8(b) |
| C5 | `internal/connector/aeroim` as the change surface | The aeroim connector is a 7-line JSON-summary adapter with no audit code; the direction's surface is `internal/service` + `internal/repository` + `internal/app`; the analysis JSON replicates the audit-governance directions across module labels | header, §1 |
| C6 | (Sibling-doc drift) gate `Log` hole and 10 live `Log` swallows | Closed in the working tree: all 10 sites converted to checked `if err := s.audit.Log(...)`; the current script documents "No `Log(` exclusion exists" | §2 E5/E6 |
