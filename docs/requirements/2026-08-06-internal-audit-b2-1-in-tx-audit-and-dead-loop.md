# Requirements — B2-1/B2-2 joint: route all audit writes into the business transaction and delete the dead inline publisher loop (T-5, T-4)

| | |
|---|---|
| Module (batch label) | `internal/audit` (live machinery in `internal/service` + `internal/repository`; the `internal/audit` package itself is the legacy unwired tree) |
| Source analysis | `docs/auto/analyses/internal-audit-9b2d0397.json` (direction 1 of 3: `b2-1-in-tx-audit-and-dead-loop`) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery path; gate G3: T-5, T-4 joint) |
| Contract | `docs/proposals/audit-contract-batch-aero-id.md` (§B2-1, §B2-2) |
| Date | 2026-08-06 |
| Status | Draft — every cited file/symbol re-verified against the repository at head `13e4676` **including the uncommitted working tree** (the dead-loop removal, `buildAuditStack` rewiring, `check-audit-swallow` gate, anchored EventIDs, and the callback/provisioning conversions have already landed in the working tree and are part of the contract to keep, not re-do) |

## 1. Evidence verification status

The analysis snapshot predates the current working tree. All eleven cited symbols were re-checked; verdicts
are against the working tree at head `13e4676`.

| # | Claim (direction evidence) | Verification (current working tree) |
|---|---|---|
| E1 | `internal/service/audit_ledger.go:75-82` — `Record` runs a dead inline `_ = publisher.Publish(ctx, fact)` loop | ⚠️ **C1 — already removed.** `auditLedgerService` (`audit_ledger.go:48`) has no `publishers` field; `NewAuditLedgerService(repo)` is single-argument (`:56`); `Record` (`:68-82`) calls `s.repo.Append(ctx, fact)` only and never publishes; the doc comment (`:52-53`) states "Record only appends, it never publishes". `grep -rn "_ = publisher.Publish" internal/` → 0 hits. Requirement becomes keep-enforced (REQ-1) |
| E2 | `internal/service/audit_ledger.go:62` — `Record` → `repo.Append` opens its own GORM transaction | ✅ stands: `repo.Append` (`internal/repository/audit_ledger.go:90`) wraps `r.db.WithContext(ctx).Transaction(...)`; `AppendInTx` (`:106`) exists for the in-tx path. Both funnel into `appendInTx` (`:123`), which dedups (`checkAuditDuplicate` `:157`, key `(tenant_id, source, event_id)`), takes the partition advisory lock, chains, and creates the ledger row **and** the `status='pending'` `audit_publications` row atomically (`createAuditRows`, UNIQUE `(fact_id)` per `migrations/000045_audit_publications.up.sql:12`) |
| E3 | 9 swallowed `_ = s.audit.Record(...)` sites: `account.go:155,184`; `callback.go:160,221,247`; `audit_hold.go:59,74`; `legacy_cutover.go:88`; `account_provision.go:168` | ⚠️ **C2 — resolved or moved, with drift.** `grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service/*.go` → **0 hits**. Current state per site: (a) `callback.go` → in-tx `CreateSubscriptionWithAudit`/`DeleteSubscriptionWithAudit`/`ResetDeliveryWithAudit` (`repository/callback_audit.go:31,60,85`, all `AppendInTx` in the domain tx); (b) `account_provision.go:156-169` → in-tx `EnsureByIdentityWithAudit`/`EnsureByIdentityWithQuotaWithAudit` (`repository/account_provision_audit.go:45,74`); (c) `audit_hold.go:69,96` → **still out-of-tx**: `PutHold`/`RemoveHold` commit, then checked `s.audit.Record` in its own tx (error → `apperr.Internal("audit_hold.audit_failed")`); (d) `legacy_cutover.go:97` → **still out-of-tx**: `SetCutover` commits, then checked `s.audit.Record` (error → `cutover.audit_failed`); (e) `account.go:155,186` (`account.viewed`/`account.listed`) → **read-path** sensitive-read audits, checked (`apperr.Unavailable("audit.ingest_failed")`), no business transaction exists — correctly out-of-tx. **A1 — additional 10 sites** remain swallowed but invisible to the gate: `_ = s.audit.Log(...)` at `admin.go:93,120`, `ban.go:83,119`, `block.go:54,77`, `comment.go:415`, `file_share.go:114,146`, `message.go:313` — `Log` adapts to `Record`→`Append` (`audit_ledger.go:136-141`), so these swallow durable-ledger writes |
| E4 | Composition root never wires publishers: `internal/app/new.go:146` `NewAuditLedgerService(auditLedgerRepo)` with no publishers and no setter | ✅ holds, relocated: `new.go:134` calls `buildAuditStack(cfg, gdb, controlMetrics)`; `buildAuditStack` (`internal/app/audit.go:34-57`) calls `service.NewAuditLedgerService(auditLedgerRepo)` at `:51` with zero publishers, then `wireAuditServices` (`:59-75`) hands the governance publishers and the publication repo to `callbackSvc` only (`SetAuditPublicationRepository` `callback.go:100`, `SetAuditPublishers` `:104`) |
| E5 | Sole live delivery path is `drainPublications` (`callback_audit.go:47`), ticker loop `callback.go:262-267` | ✅ exact: `drainPublications` at `callback_audit.go:47`; `Start` at `callback.go:269`, ticker body calls `s.drainPublications(workerCtx)` at `:282` — the only call site in the tree. `publishAuditFact` (`callback_audit.go:102-113`) is the only `AuditPublisher.Publish` call (`:113`); `callbackService.Publish` (`:19-27`) no-ops when the publication repo is wired (single-delivery contract). `projection_replication.go:82` uses `event.EventPublisher` (projection envelopes), not `AuditPublisher` — out of scope |
| E6 | `AppendInTx`/`XxxWithAudit` pattern to copy: `account_profile.go:101`, `operation.go:178`, `sync_job.go:111`, `event_receipt.go:180`, `operation_audit.go:125`, `sync_job_audit.go:43` | ✅ with drift: interface `repository/audit_ledger.go:39`, impl `:106`; users verified verbatim: `account_profile.go:101` (UpdateProfileWithAudit `:67`), `event_receipt.go:180` (ApplySnapshotEventWithAudit `:89`), `sync_job_audit.go:55` (file is `sync_job_audit.go`, not `sync_audit.go`), `operation_audit.go:136` (appendAudit, `:119`), plus `callback_audit.go:49,73,101` and `account_provision_audit.go:45,74`. Fail-closed precedent `requireAuditLedger` (`repository/audit_wiring.go:14-28`, panic on nil/typed-nil) |
| E7 | `make check-audit-swallow` gate greps `_ =` against audit calls | ✅ exists and is **green**: `Makefile:52-54` → `scripts/check-audit-swallow.sh` (P1 `_ = ….(Record|Publish|FinishPublication|ClaimPublication)`, P2 bare-call `Record|FinishPublication|ClaimPublication`; self-test fixtures under `scripts/testdata/audit-swallow/`), wired into the done-gate (`AGENTS.md:52`). **C3 — known hole**: `Log(` is deliberately excluded from both patterns (documented at script header; negative fixture `scripts/testdata/audit-swallow/negative/fixture.go:15`), so the 10 `_ = s.audit.Log` sites (A1) are invisible — the gate is green despite 10 live swallows. The sibling spec `docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md` REQ-2/REQ-3 owns closing the hole and converting the 10 sites; this spec owns the module-side gate contract (REQ-3) |
| E8 | `TestAuditWiringRollbackAtomicity` at `internal/repository/audit_wiring_integration_test.go:163` | ✅ exists, relocated: `:175` (**C4**: cited `:163` stale). Duplicate-append injection (`ErrAuditDuplicate`) rolls the whole tx back — profile keeps first values, ledger keeps exactly one row. Sibling tests: `TestAuditWiringHappyPath` (`:60`), `TestAuditWiringPublicationInsertRollsBackLedgerRow` (`:268`, trigger-injected publication insert failure → 0 ledger + 0 publication + 0 domain rows) |
| E9 | T-4: remaining `idgen.NewID()` event_ids at `audit_hold.go:59,74`; `legacy_cutover.go:88`; `operation_audit.go:49` | ⚠️ **C5 — already anchored.** `audit_hold.go:69` `idgen.Anchor("audit_hold.set", tenant, scopeType, scopeID, reason, idgen.RenderAnchorTime(expiresAt))`, `audit_hold.go:96` `idgen.Anchor("audit_hold.removed", tenant, scopeType, scopeID)`; `legacy_cutover.go:97` `idgen.Anchor("legacy.cutover.set", tenant, status, previous)`; `internal/service/operation_audit.go:65` `idgen.Anchor(action, tenant, operationID, step, status)`. `idgen.Anchor`/`RenderAnchorTime` at `internal/pkg/idgen/idgen.go:35,54`. **A2 — new gate** `scripts/check-anchored-event-ids.sh` (R-1 bans `EventID: idgen.NewID()` outside the sole C2 allow-list `internal/service/callback.go:261` — the intentional fresh-ID-per-replay-intent site; R-2 bans derivation-source escape hatches) wired into the done-gate (`AGENTS.md:52`). Only audit `EventID` still minted randomly: `callback.go:261` (C2, documented) and the `Log`-adapter facts of A1 |
| E10 | Duplicate-append contract on `(tenant_id, source, event_id)` (`repository/audit_ledger.go:161-170`) | ✅ `checkAuditDuplicate` (`:157-171`) pre-check + `uq_audit_ledger_tenant_event` final guard (`migrations/000050_scope_events_and_audit_by_tenant.up.sql:15`), 23505 translated to `ErrAuditDuplicate` (`:49`). Covered by `TestAppendConcurrentDuplicateAcrossPartitions` (`audit_consolidation_integration_test.go:48`), `TestAppendConcurrentDuplicateBarrier` (`:129`), `TestAnchoredEventIDAppendsOnce` (`anchor_dedup_integration_test.go:35`) |
| E11 | `internal/audit` module itself | ✅ as analyzed: legacy unwired tree — only `internal/audit/repository.go` (`Repository.Append/Log` on `audit_events`), no wiring, not part of the live runtime. No work in this package |

## 2. Problem statement (current state)

Audit durability is split. The dead inline publisher loop (E1) is gone and the single delivery path
(`drainPublications` via the `callbackService.Start` ticker, E5) is the only way facts reach governance
sinks; `Record` appends only, and the composition root wires publishers exclusively into the callback
service (E4). Error swallowing at the direction's nine sites is eliminated (C2) and the
`check-audit-swallow` gate exists and is green (E7).

Three gaps from the direction remain open:

1. **Two write sites still append audit outside the business transaction** (E3c/d): `auditHoldService.Put`
   /`Remove` and `legacyCutoverService.Set` commit the domain row, then call `Record` in a second
   transaction. A crash between the two commits silently loses the compliance fact; a forced audit failure
   cannot roll the domain write back; and the reverse (audit committed, domain rolled back) leaves a
   phantom ledger row. The `XxxWithAudit` + `AppendInTx` pattern that fixes this is proven (E6) and the
   anchored EventIDs (E9) already give the retry-fold semantics the in-tx duplicate swallow needs.
2. **The no-swallow gate has a hole** (C3): `Log(` is excluded, so ten swallowed durable-ledger writes
   (A1) are invisible and the gate is green despite them.
3. **Atomicity coverage does not yet include hold/cutover** (E8 covers profile/operation/event only): after
   (1) the rollback-injection test must cover `PutHoldWithAudit`/`RemoveHoldWithAudit`/`SetCutoverWithAudit`
   for the "zero `audit_ledger` rows after forced rollback" acceptance to hold for every migrated mutation.

## 3. Requirements

### REQ-1 — `Record` stays append-only; `drainPublications` stays the sole delivery path (keep-enforced)

1. `NewAuditLedgerService` must remain single-argument `(repo repository.AuditLedgerRepository)`; the
   `publishers` field and any inline publish loop must not return. `auditLedgerService.Record`
   (`internal/service/audit_ledger.go:68-82`) must contain no publish call of any form and must keep:
   `repo.Append` (fact + `pending` publication row commit atomically), the
   `ErrAuditDuplicate → return nil` fold (E10), and `ObserveAuditIngestFailure(source)` on non-duplicate
   append errors (`:75`).
2. Composition root (`internal/app/audit.go:34-57,59-75`) must keep wiring publishers exclusively into the callback
   service: `NewAuditLedgerService(auditLedgerRepo)` with no publisher argument + `wireAuditServices`
   handing `SetAuditPublicationRepository`/`SetAuditPublishers` only to `callbackSvc`.
   `drainPublications` (`callback_audit.go:47`) stays the only delivery path, driven by the `Start` ticker
   (`callback.go:282`).
3. **T-10.1-side mechanical proof**: `grep -rn "\.Publish(ctx, fact)" internal/service/` matches only
   `callback_audit.go:113` (inside `publishAuditFact`); `grep -rn "drainPublications(" internal/` matches
   only its definition (`callback_audit.go:47`) and the `Start` ticker call (`callback.go:282`);
   `NewAuditLedgerService(` is never called with a second argument in non-test Go.

### REQ-2 — Route the remaining out-of-tx write sites into the business transaction

1. `auditHoldService.Put`/`Remove` (`internal/service/audit_hold.go:69,96`) → new repository variants
   `PutHoldWithAudit`/`RemoveHoldWithAudit` on `repository/audit_hold.go`; `legacyCutoverService.Set`
   (`internal/service/legacy_cutover.go:97`) → `SetCutoverWithAudit` on
   `repository/legacy_cutover.go`. Each performs the domain write and `r.audit.AppendInTx(ctx, tx, fact)`
   in **one** gorm transaction, following the landed precedents (`repository/callback_audit.go:31-55`,
   `operation_audit.go:119-136`, `requireAuditLedger` fail-closed check at
   `repository/audit_wiring.go:14-28`). Services fail closed on a missing audit repository — no plain-write
   fallback.
2. **Preserve the anchored EventIDs byte-identically** (`idgen.Anchor(...)` shapes at `audit_hold.go:69,96`
   and `legacy_cutover.go:97`, E9): a retry re-derives the same `event_id`; the in-tx duplicate swallow
   (contract §4.2 Class B — swallow only after the domain write succeeded inside the same tx) commits the
   domain write with one ledger row; distinct logical facts keep distinct anchors. `PutHoldWithAudit` must
   copy back the stored hold fields (ID/CreatedAt/ExpiresAt, per `TestPutHoldCopyBack`
   `anchor_dedup_integration_test.go:365`) before the fact is built, so the anchor inputs match the
   persisted values.
3. Read-path sensitive-read audits (`account.go:155,186`, `account.viewed`/`account.listed`) stay
   out-of-tx by design (no business write exists to join) but must remain error-checked — never `_ =`, and
   on failure the business operation must fail or `ObserveAuditIngestFailure` must increment (they do
   both today: `Record` metering + `apperr.Unavailable("audit.ingest_failed")`).

### REQ-3 — No-swallow gate meaningful and green

1. `make check-audit-swallow` (`Makefile:52-54`, done-gate `AGENTS.md:52`) must fail on **any**
   `_ = <audit receiver>.<method>(` — mechanically `_ = s.audit.` (all methods, **including `Log`**) and
   `_ = publisher.Publish(` — plus the P2 bare-call pattern, under `internal/service`; the documented
   `Log(` exclusion (C3) is removed and the negative fixture `_ = s.audit.Log(...)` becomes a positive
   fixture.
2. Target: **0 hits** after REQ-2 and the sibling conversions
   (`docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md` REQ-3/REQ-5, which own the
   ten `Log` sites and the four unreachable fallback tails). A reintroduced `_ = s.audit.Record/Log/...`
   or `_ = publisher.Publish` in any live service file turns the gate red. Self-tests must flag positive
   fixtures and accept negative fixtures before scanning. Update the stale baseline comment in
   `scripts/check-audit-swallow.sh` (it still names `audit_ledger.go:79`).
3. Kept exclusions that are correct: best-effort SSE `d.pusher.Publish` (`notification.go`, not an audit
   write), comment lines, `if err :=`/`return`/`err :=`/`defer`/`go` forms, and the legacy unwired tree
   (`internal/{file,message,comment,block,notification,search,audit}` — the gate scans
   `internal/service` only).

### REQ-4 — T-4: anchored event_ids with dedup fold proven

1. No audit `EventID` may be minted with `idgen.NewID()` outside the sole C2 allow-list
   (`internal/service/callback.go:261`, `callback.delivery.replayed` — a replay is a new user intent that
   must re-apply the reset and record its own fact; rationale is already inline). Enforced by
   `scripts/check-anchored-event-ids.sh` (R-1/R-2) in the done-gate; `grep -rn "EventID: idgen.NewID()"`
   in non-test Go matches only the allow-listed line.
2. Re-execution of the same business action must fold into `ErrAuditDuplicate` and exactly **one**
   `audit_ledger` row: for `audit_hold.set`/`audit_hold.removed` and `legacy.cutover.set` the anchor
   inputs are the post-normalization values that land in the persisted fact (context-bound tenant,
   trimmed scope_id, canonical expiry rendering, previous status) — retries re-derive the identical
   `event_id`.
3. Duplicate-append tests on `(tenant_id, source, event_id)` stay green
   (`TestAppendConcurrentDuplicateAcrossPartitions`, `TestAnchoredEventIDAppendsOnce`) and are extended
   after REQ-2 with hold/cutover retry-fold cases (see REQ-5).

### REQ-5 — T-5: per-site failure injection and forced-rollback atomicity for every migrated mutation

1. **Per-site no-silent-path proof (the direction's nine sites)**. For each of the 9 named sites
   (`account.go:155,186` reads; `callback.go` register/delete/replay-reset; `audit_hold.go` put/remove;
   `legacy_cutover.go` set; `account_provision.go` provision), inject a repository append failure and
   assert: **either** the business operation returns an error, **or** `ObserveAuditIngestFailure` is
   observed — never both absent. Test artifacts: unit tests with a failing audit repo spy (precedent
   `internal/service/account_test.go:72`, `audit_hold_test.go:118`, `legacy_cutover_test.go:156` all
   already inject `NewAuditLedgerService(failing)` and assert the error — the same injection pattern
   applied to every site), plus the integration suite for in-tx sites
   (`internal/repository/audit_wiring_integration_test.go`).
2. **Forced-rollback integration test shows zero `audit_ledger` rows** (mirrors
   `TestAuditWiringRollbackAtomicity` at `audit_wiring_integration_test.go:175`): a duplicate-append or
   trigger-injected publication-insert failure after the domain insert → the call returns an error and
   **no `audit_ledger` row, no `audit_publications` row, and no domain row survive**. Extend the existing
   mechanisms (`TestAuditWiringRollbackAtomicity` `:175`, `TestAuditWiringPublicationInsertRollsBackLedgerRow`
   `:268`) to the REQ-2 mutations: hold put, hold remove, cutover set. Each migrated mutation then has
   both sides: commit side (1 ledger row + 1 `pending` publication row + domain row) and crash side (0/0/0).
3. **Duplicate retry-fold cases** (T-4 joint): re-running the same `Set`/`Put`/`Remove` business action
   with identical anchor inputs yields exactly one `audit_ledger` row (the second append folds into
   `ErrAuditDuplicate` — at the service boundary `Record` returns nil; in-tx the Class B swallow commits
   the domain write).

## 4. Acceptance mapping (testable)

| Supplied acceptance | Requirement | Test artifact / pass criterion |
|---|---|---|
| T-5: for each of the 9 sites, inject repo failure → either the business operation fails or `ObserveAuditIngestFailure` increments (no silent path) | REQ-2, REQ-5.1 | `go test ./internal/service/` — table of the 9 sites: failing audit repo spy → operation error returned (reads/hold/cutover: `account_test.go:72`, `audit_hold_test.go:118`, `legacy_cutover_test.go:156` pattern) and/or metric captured; **0 sites** where the operation succeeds with the failure unobserved. For the 3 in-tx sites (callback) + provisioning: repo-level `XxxWithAudit` failure → service error (integration). `grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service/*.go` → 0 |
| Forced rollback integration test shows zero `audit_ledger` rows (mirrors `TestAuditWiringRollbackAtomicity` at `audit_wiring_integration_test.go:163`) | REQ-5.2 | `go test -tags=integration ./internal/repository/` — `TestAuditWiringRollbackAtomicity` (`:175`) + `TestAuditWiringPublicationInsertRollsBackLedgerRow` (`:268`) stay green; new per-mutation cases for hold put/remove and cutover set: duplicate/trigger-injected append failure → `count(audit_ledger)=0`, `count(audit_publications)=0`, domain row absent |
| New `make check-audit-swallow` gate greps `_ =` against audit calls and is green repo-wide | REQ-3 | `make check-audit-swallow` — positive fixtures include `_ = s.audit.Log(` and `_ = publisher.Publish(` (self-test fails without them); live scan of `internal/service` = 0 hits; a reintroduced `_ = s.audit.Record/Log/...` or `_ = publisher.Publish` in any live service file turns the gate red; gate runs in the done-gate (`AGENTS.md:52`) alongside `make check-anchored-event-ids` |
| T-10.1-side: grep proves no `publisher.Publish` reachable outside `publishAuditFact` (`callback_audit.go:105`) | REQ-1.3 | `grep -rn "\.Publish(ctx, fact)" internal/service/` → only `callback_audit.go:113`; `grep -rn "drainPublications(" internal/` → definition (`callback_audit.go:47`) + `Start` ticker (`callback.go:282`) only; `NewAuditLedgerService(` has no second argument in non-test Go (today: `app/audit.go:51` only); `internal/app/audit_integration_test.go:49` `TestAuditLedgerRecordEnqueuesOnlyIntegration` stays green (1 `Record` → 1 ledger row + 1 `pending` publication row, no delivery) |
| T-4 joint: replace remaining `idgen.NewID()` event_ids (`audit_hold.go:59,74`; `legacy_cutover.go:88`; `operation_audit.go:49`) with anchor-derived IDs so re-execution folds into `ErrAuditDuplicate` and one ledger row; duplicate-append test on `(tenant_id, source, event_id)` | REQ-4, REQ-5.3 | Already landed (E9): `idgen.Anchor` shapes at `audit_hold.go:69,96`, `legacy_cutover.go:97`, `operation_audit.go:65`; gate `scripts/check-anchored-event-ids.sh` green (`make check-anchored-event-ids`); `grep -rn "EventID: idgen.NewID()"` in non-test Go matches only the C2 allow-list (`callback.go:261`). After REQ-2: `TestAnchoredEventIDAppendsOnce` + new hold/cutover retry-fold cases assert re-execution → exactly 1 `audit_ledger` row; `TestAppendConcurrentDuplicateAcrossPartitions`/`TestAppendConcurrentDuplicateBarrier` stay green |

## 5. Scope boundaries (explicit non-goals)

- **Legacy unwired tree** (`internal/audit` — only `Repository.Append/Log` on `audit_events` — and
  `internal/{file,message,comment,block,notification,search}`): not migrated, not scanned by the gate
  (documented exclusion; AGENTS.md "unwired parallel tree"). No work in the `internal/audit` package
  itself.
- **The ten `_ = s.audit.Log` conversion sites and the four unreachable out-of-tx fallback tails**:
  owned by the sibling spec `docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md`
  (REQ-2/REQ-3/REQ-5). This spec owns the gate closure (REQ-3.1) they depend on, and their completion is
  a precondition for the gate's 0-hit target.
- **Other directions in the same batch**: TokenSource 401 invalidation with epoch single-flight (B2-3),
  dead-letter replay/requeue (B2-5), audit-binding activation gate (B2-6) — out of scope.
- **`callbackService.Publish`** (`callback_audit.go:19-27`) and the C2 fresh-ID replay site
  (`callback.go:261`): retained as-is — the C2 exception is the sole allow-list entry in
  `check-anchored-event-ids.sh` (R-1).
- **`projection_replication.go:82`** `s.publisher.Publish`: `event.EventPublisher` (cross-region
  projection envelopes), not `AuditPublisher`; not an audit delivery path; excluded from REQ-1.3's grep
  by the `(ctx, fact)` signature.
- **No new migrations**: `audit_ledger`/`audit_publications` + UNIQUE `(fact_id)` (000045) and
  `uq_audit_ledger_tenant_event` (000050) already exist; `AppendInTx` already creates the publication row.
- **Read-path sensitive-read audits** (`account.viewed`/`account.listed`): stay out-of-tx by design (no
  business write exists) but remain error-checked — the T-5 disjunction applies via the operation-failure
  arm and the `Record`-path metric.

## 6. Compatibility and failure-mode notes

- **Working-tree delta**: the dead loop, composition-root rewiring, gate, and anchors are already in the
  uncommitted working tree (C1/C3/C5). The requirements above are keep-enforced for landed items and
  completion for the two remaining out-of-tx write sites (REQ-2) plus the gate hole (REQ-3). The audit
  `Record`/`Append`/`AppendInTx`/publication surface is unchanged — no API or schema break.
- **`ErrAuditDuplicate` semantics**: `Record` maps it to `nil` (idempotent fold, no metric, no error);
  in-tx (Class B) it is swallowed **after** the domain write inside the same tx (`operation_audit.go:119-136`
  precedent) so it can never mask a failed domain write and never leaves a ledger row without its domain
  row. REQ-2 conversions inherit this via the landed pattern.
- **Metric scope**: `ObserveAuditIngestFailure` increments on the `Record` path only
  (`audit_ledger.go:75`); in-tx `XxxWithAudit` failures surface as business-operation errors. The T-5
  acceptance is disjunctive ("either…or") precisely so both paths satisfy it; no metering change is
  required by this direction.
- **Fact parity**: hold/cutover facts keep byte-identical content and anchors when moved into the
  transaction (REQ-2.2) — the anchor inputs must be the post-normalization, post-copy-back values that
  land in the persisted fact, so the dedup fold is unchanged from today's out-of-tx behavior.
- **Crash windows closed by REQ-2**: after conversion, no remaining domain write (hold put/remove,
  cutover set) has a commit-to-append gap; delivery stays outbox-driven
  (`drainPublications`, at-least-once, dedup-keyed, lease-fenced
  `ErrAuditPublicationLeaseLost`), so a committed fact is eventually delivered exactly-once
  semantically.
- **Gate greenness today is not evidence of no-swallow**: the 10 `Log` sites (A1) are invisible until
  REQ-3.1 lands; the acceptance "green repo-wide" is meaningful only with the exclusion removed.
