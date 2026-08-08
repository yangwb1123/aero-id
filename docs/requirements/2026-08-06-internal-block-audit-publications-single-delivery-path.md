# Requirements — internal/block: make audit_publications outbox the only audit delivery path (delete dead inline publisher loop in auditLedgerService.Record and the bypass branch in callbackService.Publish)

> **Module:** `internal/block` (direction scope: `internal/service` audit delivery path; `internal/block/` itself is untouched, see §2)
> **Date:** 2026-08-06
> **Direction (selected, value 7 / risk 7 / effort 3 / confidence 9):** "Make audit_publications outbox the only delivery path: delete the dead inline publisher loop in auditLedgerService.Record and the bypass branch in callbackService.Publish" — direction 3 of 3 in `docs/auto/analyses/internal-block-c9ab184b.json`
> **Method:** every cited file/symbol re-verified against the working tree (line numbers below are from the current tree, which contains uncommitted in-flight audit-consolidation work; committed-HEAD differences noted). A prior requirements doc for the same direction exists from another batch (`docs/requirements/2026-08-06-audit-record-single-delivery-path-and-appendintx-consolidation.md`, module label `cmd/migrate`); its scope was broader (AppendInTx consolidation of seven live write paths). This spec is strictly the delivery-path narrowing of the selected direction.

---

## 1. Problem statement (verified)

Two inline delivery paths exist alongside the durable `audit_publications` outbox, and both are dead code that still looks live:

1. **Inline publisher loop in `auditLedgerService.Record`.** At committed HEAD, `Record` appended the fact and then ran `for _, publisher := range s.publishers { if publisher != nil { _ = publisher.Publish(ctx, fact) } }` (`internal/service/audit_ledger.go:77-81`): synchronous, after `repo.Append`'s own transaction, with the error swallowed — no bulkhead, no backoff, no DLQ. **In the current working tree this loop is already deleted** (uncommitted): `Record` (:68-76) is append-only, the `publishers []AuditPublisher` field is gone from `auditLedgerService`, and `NewAuditLedgerService(repo)` (:56) takes a single argument with a comment stating delivery is exclusively the drain worker's job. The deletion must land and be regression-guarded.
2. **Bypass branch in `callbackService.Publish`.** `Publish` (`internal/service/callback_audit.go:19-27`) short-circuits `if s.publications != nil { return nil }` (:23-25), i.e. it is a silent no-op whenever the outbox repository is wired — which is always in the live app (`wireAuditServices`, `internal/app/audit.go:59-73`, unconditionally calls `SetAuditPublicationRepository`). Repository-wide grep finds **zero callers** of `callbackService.Publish` in production or test code (the only `CallbackService` consumers are `internal/app/audit.go:35,59`, `internal/app/grpc.go:33`, `internal/handler/http/callback.go:13`; none invoke `Publish`). The method, its interface member (`CallbackService` at `internal/service/callback.go:33-41`, `Publish` at :35), and its bypass branch are pure dead weight. Deleting only the branch and keeping the method would re-create a *live* inline enqueue path (unconditional `enqueueFact`) — the opposite of this direction — so the whole dead entry point must go.

The only real delivery path is `drainPublications` (`callback_audit.go:47-70`), driven by the worker loop in `callbackService.Start` (`internal/service/callback.go:282`; cited as `:267` at committed HEAD), which claims `pending` rows, fans out under the P6-06 bulkhead, and settles each row via `finishPublicationAttempt`/`publicationOutcome` (`callback_audit.go:73-97`; already implements retry with backoff, attempt-free `audit.binding_unavailable` recovery, and `dead_letter` at `attempt >= MaxAttempts`). Keeping the two legacy paths alongside the outbox invites double delivery or divergent behavior on any future wiring change; the working tree has already made part of the change and the remaining part must be completed coherently.

---

## 2. Scope

**In scope**

- `internal/service/audit_ledger.go`: the removal of the inline publisher loop/field/constructor-param stays landed (already done in the working tree); no publisher reference may exist on `auditLedgerService` or in `Record`'s call graph (single-path proof: single-arg constructor as written + `check-audit-swallow`; see A-1.1 — this is gate + code-reading, not a compiler-enforced property). The `AuditPublisher` interface (:24-26) itself stays — it is the outbox worker's port (`callbackService.publishers`/`SetAuditPublishers`, `internal/service/callback.go:62,104-105`; consumed by `internal/app/audit.go:60-71` and implemented by `auditgovernance.Publisher` and `sso.Client.Publish`) and must not be confused with `Record`'s deleted dependency.
- `internal/service/callback_audit.go` + `internal/service/callback.go`: delete `callbackService.Publish` (bypass branch included) and the `Publish` member of the `CallbackService` interface; `enqueueFact` remains reachable only from `publishAuditFact` (the drain-worker path).
- Tests: unit + integration coverage for the acceptance checks (§4, §5) — the A-4.2 dead-letter
  terminality integration test, the **mandatory A-4.3 drain end-to-end test** (service-level,
  failing publisher fake, real Postgres), the A-5.2 lease-expiry reclaim integration test, and the
  A-7 mis-wiring units; existing tests are kept/asserted.
- `internal/pkg/metrics/control_plane.go` + `internal/service/control_plane_metrics.go` (REQ-7/REQ-8):
  two new counter series — `aero_control_audit_publication_outcomes_total{status=...}` (settlements,
  incl. terminal `dead_letter`) and `aero_control_audit_publication_drain_skipped_total` (unwired
  outbox early return). Additive: no existing series renamed or removed.
- `internal/app/audit.go` (REQ-7): `wireAuditServices` fails closed — the silent setter-assertion
  no-ops become panics; with the shipped concrete types both assertions always hold, so the panics
  are unreachable insurance (mirrors the `requireAuditLedger` precedent).
- `internal/connector/auditgovernance/publisher.go` (+ `publisher_test.go`; REQ-6): FM-9 resolution —
  treat a well-formed conflict receipt (`Conflict: true` echoing this fact's `event_id`/`tenant_id`
  with non-zero `accepted_at`) as idempotent success in `validateReceipt`/`receiptMatches`, so the
  duplicate delivery guaranteed by lease-expiry re-claim (REQ-5/A-5) can never settle a duplicated
  *successful* event as `dead_letter`. Guarded: mismatched echoes and every non-conflict path map
exactly as before.

**Out of scope (deliberately, per the direction)**

- Any change to `internal/block/` (dead parallel tree; its `BlockChecker` consumer at `internal/notification/dispatcher.go:8` is unaffected — this direction touches no block code).
- The `make check-audit-swallow` gate (separate direction 2 of the same analysis; already exists in the working tree as `scripts/check-audit-swallow.sh` + `Makefile:52-54` and bans `_ = publisher.Publish(` — it is the regression guard for this direction, not a deliverable here).
- The AppendInTx conversion of the seven live write paths (the other batch's consolidation scope; see the prior doc).
- `drainPublications`/`ClaimPublication`/`FinishPublication` semantics, migration 000045, config, proto, or handler changes.
- No new Makefile gate, no new migration, no change to the `sso.Client.Publish` adapter. Governance-publisher change is limited to the FM-9 conflict-receipt resolution (REQ-6); the *server-side* dedup contract (the deployed endpoint's duplicate-response shape) remains unverifiable from this repo (follow-up).
- No manual replay API for dead-lettered `audit_publications`. Terminal `dead_letter` is **explicitly accepted**: the audit ledger is the durable authority, the dead-lettered row is durable and identifies its fact, and non-delivery is metered (REQ-8). A replay feature is a recorded follow-up (design §8), not a deliverable of this direction.

---

## 3. Evidence verification

| # | Direction's claim | Verification result |
|---|---|---|
| E1 | `internal/service/audit_ledger.go:76-80` — `Record` calls `_ = publisher.Publish(ctx, fact)` synchronously after `Append` | ✅ True at committed HEAD (`Record` at :66, loop at :77-81). ⚠️ **Already removed in the working tree** (uncommitted): `Record` at :68-76 is append-only (`repo.Append` → `ErrAuditDuplicate`→nil → metric on other errors → nil); struct at :50-52 has no `publishers` field; `NewAuditLedgerService(repo)` single-arg at :56. The direction's described hazard is real at HEAD and half-resolved in the tree. |
| E2 | `internal/service/callback_audit.go:20-27` — `Publish` short-circuits `if s.publications != nil { return nil }` | ✅ Present: `Publish` at :19-27, guard at :23-25. ❗ **Zero callers** repo-wide (production and tests) — the method is dead beyond the bypass branch itself. Not yet deleted. |
| E3 | `internal/service/callback_audit.go:47-70` — `drainPublications` is the only real delivery path | ✅ Present: `ClaimPublication(ctx, s.worker, 5*time.Minute, 50)` at :53-55, bulkhead-bounded fan-out :60-68, `finishPublicationAttempt` per item. |
| E4 | `internal/service/callback_audit.go:73-90` — `publicationOutcome`/`finishPublicationAttempt` implement retry/DLQ | ✅ Present: `finishPublicationAttempt` :73-87, `publicationOutcome` :89-97 (`completed` / `pending`+`audit.binding_unavailable` attempt-free / `pending`+backoff / `dead_letter` at `attempt >= MaxAttempts`). Working tree additionally converted the swallowed `_ = s.publications.FinishPublication(...)` (HEAD :78) into a returned error + `ObserveAuditPublicationFinishFailure` (:84-86). |
| E5 | `internal/service/callback.go:267` — worker loop drives `drainPublications` | ✅ Symbol verified; **line corrected**: `Start` at :269, `s.drainPublications(workerCtx)` at :282 (HEAD :267; the tree removed ~15 earlier lines — `SetAuditLogger` + register refactor). Ticker order: `drainPublications` then `drain`. |
| E6 | `internal/repository/audit_ledger.go:199-203` — `createAuditRows` always inserts a pending `audit_publications` row in the same tx | ✅ Present at :200-202 (`publication := &auditPublicationRow{..., Status: "pending", NextAttemptAt: now, ...}`; `tx.Create` :201). Both `Append` (:77-90) and `AppendInTx` (:123-135) funnel into `appendInTx` → `createAuditRows`, so every fact gets exactly one pending publication row atomically. Migration 000045 enforces `UNIQUE (fact_id)`. |
| E7 | `ClaimPublication` never re-publishes `dead_letter`/`completed` rows | ✅ Verified (mechanism): `internal/repository/audit_publication.go:36-44` claims only `(status='pending' AND next_attempt_at <= now) OR (status='delivering' AND lease_until < now)`; `FinishPublication` (:60-71) is lease-guarded (`WHERE status='delivering' AND lease_owner=?`, `ErrAuditPublicationLeaseLost` on miss). |
| E8 | Proposed: governance endpoint dedups on `event_id`, so residual legacy-path risk is duplication, not loss | ⚠️ **Partially verified, client side only**: `auditgovernance.Publisher` sends `EventID: fact.ID.String()` **and** `IdempotencyKey: fact.ID.String()` (`internal/connector/auditgovernance/publisher.go:140,178-185`), and validates the receipt echoes `event_id`/`tenant_id` and is not `Conflict` (:256-259). Server-side dedup behavior is not provable from this repository. |
| E9 | Working tree already carries part of the acceptance tests | ✅ `TestAuditLedgerRecordEnqueuesOnly` (`internal/service/audit_ledger_test.go:44`), `TestAuditLedgerRecordEnqueuesOnlyIntegration` (`internal/app/audit_integration_test.go:46`), `TestAuditPublicationDrainCycleSingleDelivery` (`internal/repository/audit_consolidation_integration_test.go:172`), `TestPublicationOutcomeDeadLettersBoundPublisherFailure` (`internal/service/callback_audit_test.go:24`), `TestFinishPublicationFailureObservesMetricNoRetry` (:66), `TestAuditWiringHappyPath`/`RollbackAtomicity`/`PublicationInsertRollsBackLedgerRow` (`internal/repository/audit_wiring_integration_test.go:60,175,268`). |

---

## 4. Requirements

### REQ-1 — `Record` contains no publisher reference (single-path proof)

File: `internal/service/audit_ledger.go`.

1. `auditLedgerService` must not hold a publisher field, and `NewAuditLedgerService` must take only `repo repository.AuditLedgerRepository` (already true in the working tree at :48-56; must survive the commit).
2. `Record` must perform exactly one outbound persistence call: `s.repo.Append(ctx, fact)`; it must contain no loop, no `Publish` call, and no publisher-typed variable (interface narrowing: the publisher type is absent from the type's members and call graph).
3. The `AuditPublisher` interface (:24-26) stays, documented as the drain worker's port (consumed by `callbackService`, not by `auditLedgerService`).

**Testable acceptance (A-1):**

- A-1.1 — single-arg constructor + gate: `make build` passes and `internal/service/audit_ledger_test.go:44` (`TestAuditLedgerRecordEnqueuesOnly`) constructs `NewAuditLedgerService(repo)` with a single argument. **Wording correction:** re-introducing a variadic `(repo, publishers ...AuditPublisher)` is *not* a compile error — the single-argument call compiles identically against a variadic signature. The real guards are the single-arg constructor as written (code reading; the test pins the call compiling today) and `check-audit-swallow` P1, which bans the historical swallowed shape `_ = ...Publish(`; P2 deliberately excludes `Publish` (notification SSE pusher), so a *non-swallowed* inline publish trips neither pattern.
- A-1.2 — unit: `TestAuditLedgerRecordEnqueuesOnly` (exists): `Record` on the spy repo → `repo.appended` set, exactly one `Append` call, action preserved.
- A-1.3 — e2e: `TestAuditLedgerRecordEnqueuesOnlyIntegration` (exists, `internal/app/audit_integration_test.go:46`): one `Record` → exactly 1 `audit_ledger` row with the given `event_id` and exactly 1 `audit_publications` row `status='pending'` joined to it.

### REQ-2 — Delete the dead inline delivery entry point `callbackService.Publish` (bypass branch included)

Files: `internal/service/callback_audit.go`, `internal/service/callback.go`.

1. Delete the `Publish` method (`callback_audit.go:19-27`) including its bypass branch `if s.publications != nil { return nil }` (:23-25). Deleting only the branch would leave an unconditional inline `enqueueFact` path — a live second delivery path, contrary to the direction — so the method itself is removed.
2. Delete the `Publish` member from the `CallbackService` interface (`callback.go:31-41`, member at :35). No implementer or caller exists outside these two files (verified: consumers at `internal/app/audit.go:35,59`, `internal/app/grpc.go:33`, `internal/handler/http/callback.go:13` never call it), so this is pure deletion.
3. `enqueueFact` (:29-45) stays as a private helper; after the deletion it is reachable only from `publishAuditFact` (:102-114), i.e. only from the drain-worker path.
4. `sso.Client.Publish` (`internal/sso/client.go:228`) and `auditgovernance.Publisher.Publish` (`publisher.go:83`) are unaffected — they implement the `AuditPublisher` port consumed by the drain worker.

**Testable acceptance (A-2):**

- A-2.1 — static: `grep -n "func (s \*callbackService) Publish" internal/service/callback_audit.go` and `grep -n "Publish" internal/service/callback.go` return no hits; the `CallbackService` interface (callback.go:31-41) contains no `Publish` member; `make build` passes (no caller exists to break).
- A-2.2 — unit (partial — drain leg new): `TestFinishPublicationFailureObservesMetricNoRetry` (exists) covers the finish-failure metric leg only. The delivery-path-exclusivity leg is **new**: a drain test with a recording repo asserts the only enqueue happens through `finishPublicationAttempt`→`publishAuditFact`→`enqueueFact` (one `enqueueFact` per claimed item) — delivered by the mandatory A-4.3 (`TestDrainExhaustsAttemptsIntoDeadLetter`); the repo-level A-3 test cannot invoke the private `enqueueFact`. No test may invoke a `Publish` method on `callbackService` (it no longer compiles).

### REQ-3 — One pending publication row per fact, same transaction (regression-guarded)

File: `internal/repository/audit_ledger.go` (no change — verified behavior).

`createAuditRows` (:200-202) must keep inserting exactly one `pending` `audit_publications` row in the same tx as the ledger fact for both `Append` and `AppendInTx` (shared `appendInTx`); `UNIQUE (fact_id)` (migration 000045) keeps it at exactly one per fact.

**Testable acceptance (A-3):**

- A-3.1 — integration (exists): `TestAuditPublicationDrainCycleSingleDelivery` asserts `count(*) FROM audit_publications == 1` at every stage of the claim/finish/re-claim cycle; `TestAuditWiringHappyPath` (:60) asserts 1 publication per committed fact; `TestAuditWiringRollbackAtomicity` (:175) asserts zero rows in both tables after a forced rollback; `TestAuditPublicationInsertRollsBackLedgerRow` (:268) asserts a publication-insert failure rolls the ledger row back.

### REQ-4 — Exceeding `MaxAttempts` lands in `dead_letter` and is never re-published by any path

Files: `internal/service/callback_audit.go` (no change — verified behavior), tests.

`publicationOutcome` (:89-97) returns `dead_letter` when `attempt >= MaxAttempts`; `ClaimPublication` (`audit_publication.go:36-44`) never selects `dead_letter` rows; after REQ-1/REQ-2 no inline path exists that could re-publish.

**Testable acceptance (A-4):**

- A-4.1 — unit (exists): `TestPublicationOutcomeDeadLettersBoundPublisherFailure`: `publicationOutcome(errors.New("transport"), 8, 8, now, delay)` → `status == "dead_letter"`, `code == "audit.publication_failed"`.
- A-4.2 — integration (new): append a fact, drive the publication to `dead_letter` via `FinishPublication(ctx, id, worker, "dead_letter", "audit.publication_failed", future)`, then call `ClaimPublication` repeatedly (including with a past `next_attempt_at`) and assert the row is never returned. This is the missing terminality proof — the WHERE clause is the mechanism, but no test currently pins it.
- A-4.3 — drain end-to-end (new, **mandatory**): `TestDrainExhaustsAttemptsIntoDeadLetter` —
  service-level, failing publisher fake, real Postgres, `MaxAttempts = 3`: run the real
  `drainPublications` until the row settles; assert terminal `status == 'dead_letter'` with
  `error_code == 'audit.publication_failed'` after attempt 3 and that a subsequent
  `drainPublications`/`ClaimPublication` cycle claims zero items. One `enqueueFact` per claimed
  item (3 publisher calls; exactly one `callback_deliveries` row, because `EnqueueDelivery` is
  `OnConflict DoNothing` — the enqueue layer, not the drain loop, is the dedup point — closing
  A-2.2's missing leg), and the REQ-8 outcome sequence `[pending pending dead_letter]`. This is
  the only test pinning the end-to-end MaxAttempts→dead_letter transition (the
  `publicationOutcome` pure unit covers only the decision function; repo-level A-4.2 bypasses the
  service).

### REQ-5 — Worker kill mid-drain: row stays leased, is reclaimed after lease expiry, delivered exactly-once-effectively

Files: `internal/service/callback_audit.go`, `internal/repository/audit_publication.go` (no change — verified behavior), tests.

A worker killed between `ClaimPublication` and `FinishPublication` leaves the row `delivering` with a future `lease_until`; after expiry, the next `ClaimPublication` reclaims it (`audit_publication.go:36-44` lease branch) with `attempt+1`; a stale worker's `FinishPublication` misses the lease guard and returns `ErrAuditPublicationLeaseLost` (:60-71), preventing double-finish. Redelivery is idempotent at the endpoint contract: the governance event carries `IdempotencyKey == fact.ID` (E8), and the row itself is unique per fact (E6).

**Testable acceptance (A-5):**

- A-5.1 — integration (exists, partial): `TestAuditPublicationDrainCycleSingleDelivery` covers claim → `pending` with past `next_attempt_at` → re-claim (attempt 2) → `completed` → empty claim, exactly one row throughout.
- A-5.2 — integration (new, the missing worker-kill leg): claim a row (status `delivering`, `lease_until` in the future), perform **no** finish (simulate worker kill), then claim again with an expired lease (wait out a short lease or move `lease_until` into the past via SQL); assert the same row is returned with `attempt+1`, and the stale worker's `FinishPublication(..., "completed", ...)` returns `ErrAuditPublicationLeaseLost` and does not change the row.
- A-5.3 — unit (exists): `TestFinishPublicationFailureObservesMetricNoRetry`: `FinishPublication` failure (`ErrAuditPublicationLeaseLost`) is observed on `ObserveAuditPublicationFinishFailure`, never swallowed, never retried inline (lease-expiry reclaim is the retry mechanism).

### REQ-6 — Conflict receipts are idempotent successes (FM-9 resolution)

File: `internal/connector/auditgovernance/publisher.go` (+ `publisher_test.go`); e2e test in
`internal/service/callback_integration_test.go`.

The outbox delivers at-least-once (REQ-5): after lease-expiry re-claim the same fact is re-sent, and
the governance endpoint answers the duplicate with a conflict receipt. That dedup acknowledgement must
be a success — a duplicated *successful* event must never settle `dead_letter` (terminal, no replay
surface).

1. `receiptMatches` verifies `event_id`/`tenant_id` echo and non-zero `accepted_at`; it must not reject
   `Conflict: true` receipts.
2. `validateReceipt` returns `nil` for a conflict receipt that echoes this exact fact; all other shapes
   (non-`202`, malformed, mismatched echo, unknown status on non-conflict receipts) keep returning
   `ErrInvalidReceipt`. A conflict echoing a different event/tenant is never acknowledged as this
   fact's success.
3. `sso.Client.Publish` is untouched; no config, migration, or wire-format change.

**Testable acceptance (A-6):**

- A-6.1 — connector unit: `TestPublisherConflictReceiptIsIdempotentSuccess` (full publish flow, `202` +
  conflict echo → `nil`); `TestPublisherConflictReceiptMustEchoTheFact` (different event/tenant, zero
  `accepted_at`, rejected status → `ErrInvalidReceipt`).
- A-6.2 — e2e: `TestAuditPublicationDuplicateConflictReceiptSettlesCompleted` — claim → publish
  accepted → crash before finish → lease expired → re-claim (attempt 2) → duplicate publish answered
  with conflict receipt → row settles `completed`, claim empty; red-green proven (fails with
  `audit governance: invalid receipt` without the REQ-6 change).

### REQ-7 — Mis-wiring (`publications == nil`) is pinned, metered, and fail-closed

Files: `internal/service/callback_audit.go` (one observation on the early return),
`internal/app/audit.go` (fail-closed assertions), tests.

`drainPublications` early-returns when the publication repository is not wired
(`callback_audit.go:48`; previously no metric, no signal — facts pend forever, discoverable
only by querying `audit_publications` directly). `wireAuditServices` (`app/audit.go:59-73`)
sets it unconditionally in live composition. No test pinned either leg before this direction:
the `audit_wiring_test.go` constructors tests (`TestConstructorsRejectNilAudit`) cover
nil-*ledger* repository-constructor panics, not `publications == nil` and not the
`wireAuditServices` setter assertions; zero tests referenced `wireAuditServices` /
`SetAuditPublicationRepository` / `SetAuditPublishers`. FM-8's previous evidence ("R11 wiring
tests") was misattributed — no R11 exists in this direction.

1. The early return emits `aero_control_audit_publication_drain_skipped_total` — the stall is
   metered, not silent. A non-zero value means the outbox is unwired and every fact pends
   without an attempt.
2. `wireAuditServices` fails closed: each silent `if setter, ok := ...; ok` assertion becomes a
   panic when the setter surface or the `AuditPublicationRepository` implementation is missing.
   With the shipped concrete types both assertions always hold — the panics are unreachable
   insurance, mirroring `requireAuditLedger` (the fail-closed wiring direction). Startup stops
   instead of pend-facts-silently.

**Testable acceptance (A-7):**

- A-7.1 — unit (new): `TestDrainPublicationsEarlyReturnObservesMetric` — a `callbackService`
  without a publication repository: `drainPublications` returns immediately, claims nothing,
  and observes the drain-skipped counter exactly once (no panic, no outcome).
- A-7.2 — unit (new): `TestWireAuditServicesFailClosedOnMissingSetters` — `wireAuditServices`
  panics on a nil callback service and on a ledger repository that cannot attach the
  publication repository (fail-closed composition root, correcting the mis-cited nil-ledger
  constructor tests).

### REQ-8 — Settlement outcomes are observable: `dead_letter` is metered (FM-5)

Files: `internal/pkg/metrics/control_plane.go`, `internal/service/control_plane_metrics.go`,
`internal/service/callback_audit.go` (one observation), tests.

`dead_letter` is the terminal settlement of the only delivery path (REQ-4): the row is never
re-claimed and no inline path re-publishes it. With no settlement metric, governance
non-delivery was only discoverable by querying `audit_publications` directly — the design
claimed a delivery story with no terminal signal.

1. Every **committed** settlement is counted on
   `aero_control_audit_publication_outcomes_total{status="completed|pending|dead_letter"}`,
   observed in `finishPublicationAttempt` only when `FinishPublication` succeeds. A failed
   finish is counted on the existing finish-failure metric, not as an outcome: the row stays
   `delivering` and the decision is re-made after lease-expiry re-claim.
2. `status="dead_letter"` is the alertable signal for governance sink non-delivery (alert on
   `rate(...{status="dead_letter"}[15m]) > 0` — infra-side rule, not code).

**Testable acceptance (A-8):**

- A-8.1 — unit (new): `TestFinishPublicationSettlesObservesOutcome` (committed `completed` →
  one outcome, no finish-failure) and `TestFinishPublicationDeadLetterSettlesObservesOutcome`
  (MaxAttempts exhaustion → committed `dead_letter` → one outcome). `TestFinishPublicationFailureObservesMetricNoRetry`
  additionally asserts an unsettled finish observes the finish-failure metric and **no** outcome.
- A-8.2 — unit (new): registry text output contains both new series
  (`TestControlPlaneMetrics_TextOutput` extended).
- A-8.3 — integration (folded into A-4.3): the end-to-end MaxAttempts cycle observes
  `[pending pending dead_letter]`.

---

## 5. Testable acceptance mapping (direction's acceptance checks → concrete tests)

| Direction acceptance check | Test | Status |
|---|---|---|
| "After removal, Record contains no publisher reference (compile-time proof via interface narrowing)" | `TestAuditLedgerRecordEnqueuesOnly` (single-arg constructor + append-only spy assertion); `make build`; REQ-1 A-1.1/A-1.2 | Exists in working tree; must land |
| "e2e test: one Record → exactly one pending audit_publications row" | `TestAuditLedgerRecordEnqueuesOnlyIntegration` (1 ledger row + 1 `pending` publication row) | Exists in working tree; must land |
| "→ drainPublications delivers and marks completed" | `TestAuditPublicationDrainCycleSingleDelivery` (claim → re-claim → `completed` → empty claim) | Exists in working tree; must land |
| "worker kill mid-drain leaves the row pending/lease-expired and redelivered exactly-once-effectively via idempotent receipt (T-10.1/T-10.2)" | REQ-5 A-5.2 (delivering + expired lease → re-claim attempt+1; stale finish → `ErrAuditPublicationLeaseLost`) + A-5.3; idempotency contract pinned by E8 (`IdempotencyKey == fact.ID`) | **New test required** (mechanism verified, no test) |
| "a publication exceeding MaxAttempts lands in dead_letter and is NOT re-published by any inline path (T-10.2)" | `TestPublicationOutcomeDeadLettersBoundPublisherFailure` (unit) + REQ-4 A-4.2 (claim never returns `dead_letter`) + **A-4.3 `TestDrainExhaustsAttemptsIntoDeadLetter` (mandatory: service-level, `MaxAttempts=3`, failing publisher fake, real Postgres — terminal `dead_letter` after attempt 3, empty subsequent claim, one `enqueueFact` per claimed item)**; inline paths absent by REQ-1/REQ-2 deletion | Unit exists; **new integration tests required (A-4.3 mandatory)** |
| "a duplicated successful event must not settle as failure (FM-9)" | REQ-6 A-6.1 (connector unit: conflict → `nil`; mismatched echo → `ErrInvalidReceipt`) + A-6.2 (e2e: duplicate delivery settles `completed`) | **New** — resolved in-direction; A-5 alone cannot cover it (repo-level claim/finish test never invokes the connector) |
| "mis-wiring (`publications == nil`) must not be a silent stall" | REQ-7 A-7.1 (`TestDrainPublicationsEarlyReturnObservesMetric`: early return emits the drain-skipped counter) + A-7.2 (`TestWireAuditServicesFailClosedOnMissingSetters`: fail-closed panics) | **New** — in-direction; previous evidence mis-cited nil-ledger constructor tests |
| "dead_letter (the terminal settlement) must be observable" | REQ-8 A-8.1/A-8.2 (unit: outcome observed on committed settlement, incl. `dead_letter`; registry text) + A-8.3 (folded into A-4.3: `[pending pending dead_letter]`) | **New** — in-direction (FM-5) |

---

## 6. Failure modes considered

| # | Failure | Behavior after change | Handling |
|---|---|---|---|
| F1 | Future wiring re-introduces an inline publish (e.g., a new `_ = publisher.Publish(` in `internal/service`) | Code state: `auditLedgerService` has no publisher member today — re-adding one is **not** a compile error (A-1.1 pins the constructor as written; it cannot compile-pin arity). Runtime: `make check-audit-swallow` (already in the working tree, `Makefile:52-54`) fails CI on any *swallowed* `Publish`/`Record`/`FinishPublication` call (P1); a *non-swallowed* inline `Publish` trips neither P1 nor P2 (P2 deliberately excludes `Publish` — SSE pusher) and is a review-time guard | REQ-1 (constructor as written) + existing gate (direction 2); non-swallowed re-introduction caught by review, not CI |
| F2 | Someone deletes only the bypass branch and keeps `Publish` | Would re-create a live inline `enqueueFact` path — explicitly forbidden | REQ-2 removes the method and interface member; A-2.1 static check |
| F3 | Worker crash between claim and finish | Row stays `delivering`; reclaimed after lease expiry; stale finish loses the lease (`ErrAuditPublicationLeaseLost`) | REQ-5 A-5.2 (new test) |
| F4 | Governance endpoint is down / tenant binding unavailable | `publicationOutcome` → `pending` + backoff, or `dead_letter` at `attempt >= MaxAttempts`; never re-published after DLQ; the terminal settlement is metered on `aero_control_audit_publication_outcomes_total{status="dead_letter"}` (REQ-8) | Existing machinery; REQ-4 A-4.2/A-4.3 (new tests); REQ-8 A-8 |
| F7 | Outbox wiring regresses (`publications == nil` in a future composition root) | `drainPublications` early-returns and emits the drain-skipped counter; `wireAuditServices` panics at startup (fail-closed); facts pend durably with a non-zero metric instead of a silent stall | REQ-7 A-7.1/A-7.2 |
| F5 | Dead-lettered row re-published by a future code path | `ClaimPublication` WHERE clause excludes `dead_letter`; no inline path exists after REQ-1/REQ-2 | A-4.2 pins the exclusion; REQ-2 removes the alternative path |
| F6 | Duplicate delivery (lease-expiry re-claim) answered with a conflict receipt | Connector treats a well-formed conflict receipt as idempotent success (REQ-6) → row settles `completed`; a conflict for a different event stays `ErrInvalidReceipt` | REQ-6 A-6.1/A-6.2 |

## 7. Out of scope (unchanged from §2)

`internal/block/` code, the swallow gate deliverable, AppendInTx consolidation of other write paths, migration/config/proto/handler changes, any change to `drainPublications`/claim/finish/outbox semantics, and any change to the `sso.Client.Publish` adapter. Governance-publisher change is limited to the FM-9 conflict-receipt resolution (REQ-6, A-6). This direction is delivery-path narrowing plus that guarded receipt-semantics fix; per AGENTS.md, nothing in `internal/block/` is wired into the running application, and the live audit delivery path lives entirely in `internal/service` + `internal/repository`.
