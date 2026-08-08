# Design — audit_publications outbox as the only audit delivery path (internal/block direction 3)

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-06-internal-block-audit-publications-single-delivery-path.md` (REQ-1..REQ-8) |
| Module (batch label) | `internal/block` (effective scope: `internal/service` audit delivery path; `internal/block/` untouched) |
| Direction | "Make audit_publications outbox the only delivery path: delete the dead inline publisher loop in `auditLedgerService.Record` and the bypass branch in `callbackService.Publish`" |
| Status | Design — evidence re-verified against the live working tree on 2026-08-06; FM-9/E8 interaction resolved in-direction on 2026-08-06 (§3.4, A-6); operational observability gap resolved in-direction on 2026-08-06 (FM-5 dead_letter and FM-8 drain early-return had no signal — §3.5, REQ-7/REQ-8, A-7/A-8) |

## 1. Evidence verification (untrusted claims re-checked)

Every symbol cited in the requirements evidence was re-checked against the working tree (`git status`
shows the audit-consolidation batch is uncommitted; committed-HEAD deltas noted). **All substantive
claims hold.** One correction and one scope observation are folded in below.

| # | Evidence claim | Verdict |
|---|---|---|
| E1 | Inline `for _, publisher := range s.publishers { _ = publisher.Publish(ctx, fact) }` in `Record` at HEAD :77-81; already deleted in the working tree (`Record` :68-76 append-only, single-arg `NewAuditLedgerService(repo)` :56) | ✅ Exact. `git diff HEAD -- internal/service/audit_ledger.go` shows the field, variadic constructor param, and loop all removed in the uncommitted tree. At HEAD the loop was already doubly dead: `internal/app/new.go:146` (HEAD and tree) passes zero publishers. |
| E2 | Bypass branch `if s.publications != nil { return nil }` at `callback_audit.go:23-25`; zero callers of `callbackService.Publish` | ✅ Exact. Method at :19-27, guard at :23-25. Repo-wide `grep -rn "\.Publish("` finds only unrelated types (notification pusher, hub, nats publisher, `auditgovernance.Publisher`). Consumers of `CallbackService` (`app/audit.go:41,59`, `app/grpc.go:33`, `handler/http/callback.go:13`, `handler/grpc/{callback,server}.go`) never invoke `Publish`. No fake implements `CallbackService` via `var _`. |
| E3 | `drainPublications` is the only real delivery path | ✅ Exact. `callback_audit.go:47-70`: `ClaimPublication(ctx, s.worker, 5*time.Minute, 50)`, P6-06 bulkhead-bounded fan-out, `finishPublicationAttempt` per item. |
| E4 | `publicationOutcome`/`finishPublicationAttempt` implement retry/DLQ; tree un-swallowed the finish error | ✅ Exact. `finishPublicationAttempt` :73-87, `publicationOutcome` :89-97 (`completed` / `pending`+`audit.binding_unavailable` attempt-free / `pending`+backoff / `dead_letter` at `attempt >= maxAttempts`). `FinishPublication` error is returned and observed via `ObserveAuditPublicationFinishFailure` (:84-86), never `_ =`. |
| E5 | Worker loop at `callback.go:267` | ✅ Symbol exact; line corrected to `Start` :269 with `s.drainPublications(workerCtx)` at :282. Ticker order: `drainPublications` then `drain`. |
| E6 | Pending row inserted in same tx at `audit_ledger.go:199-203`; `UNIQUE(fact_id)` in migration 000045 | ✅ Exact. `createAuditRows` :172, publication row `{Status: "pending", NextAttemptAt: now}` :200-202; both `Append` (:77-90) and `AppendInTx` (:123-135) funnel through `appendInTx`. `migrations/000045_audit_publications.up.sql` has `CONSTRAINT uq_audit_publication_fact UNIQUE (fact_id)` plus due/lease indexes. |
| E7 | `ClaimPublication` never re-publishes `dead_letter`/`completed` rows; lease-guarded finish | ✅ Exact. `internal/repository/audit_publication.go:36-44`: claims only `(status='pending' AND next_attempt_at <= now) OR (status='delivering' AND lease_until < now)` with `FOR UPDATE SKIP LOCKED`; `FinishPublication` :60-71 guarded by `status='delivering' AND lease_owner=?` → `ErrAuditPublicationLeaseLost` on miss. |
| E8 | Governance endpoint dedups on `event_id`; residual risk is duplication, not loss | ⚠️ Partially verified, client side only. `auditgovernance.Publisher.event()` sends `EventID: fact.ID.String()` **and** `IdempotencyKey: fact.ID.String()`; server-side dedup is unprovable from this repo. **Corrected and resolved (FM-9):** at verification time a *conflict* receipt (`Conflict: true`) failed `receiptMatches` (`!Conflict` clause) → `ErrInvalidReceipt` → retry → `dead_letter` — a duplicated successful event settled as a failure. That interaction is **resolved in this direction** (§3.4): a well-formed conflict receipt is an idempotent success, pinned by A-6 (red-green proven). Only the *server-side* duplicate-response contract remains unprovable from this repo and stays a follow-up (§8). |
| E9 | Working tree already carries part of the acceptance tests | ✅ Exact. `TestAuditLedgerRecordEnqueuesOnly` (`audit_ledger_test.go:46`), `TestAuditLedgerRecordEnqueuesOnlyIntegration` (`audit_integration_test.go:49`), `TestAuditPublicationDrainCycleSingleDelivery` (`audit_consolidation_integration_test.go:172`), `TestPublicationOutcomeDeadLettersBoundPublisherFailure` (`callback_audit_test.go:24`), `TestFinishPublicationFailureObservesMetricNoRetry` (:66), `TestAuditWiringHappyPath`/`RollbackAtomicity`/`PublicationInsertRollsBackLedgerRow` (`audit_wiring_integration_test.go:60,175,268`). |
| E10 | `check-audit-swallow` gate exists and bans the inline loop shape | ✅ Exact. `Makefile:52-54`, `scripts/check-audit-swallow.sh` — P1 bans `_ = ...(Record|Publish|FinishPublication|ClaimPublication)(`, P2 bans bare `Record|FinishPublication|ClaimPublication(` calls; self-tests against `scripts/testdata/audit-swallow/`. It is the regression guard for this direction, not a deliverable. |

**Gap confirmed by verification — the two missing acceptance legs are genuinely absent:**

- **A-4 (dead_letter terminality, integration):** the only dead-letter coverage is the pure-function unit
  `TestPublicationOutcomeDeadLettersBoundPublisherFailure`. No test proves end-to-end that a row finished
  as `dead_letter` is never re-claimed by `ClaimPublication` and that a full drain cycle (MaxAttempts
  failures) terminates in `dead_letter`. The claim-predicate exclusion rests on the WHERE clause alone.
- **A-5 (lease-expiry reclaim, integration):** the `status='delivering' AND lease_until < now` leg of
  `ClaimPublication` is unpinned. `callback_audit_test.go:66-75` covers only the *finish* failure path
  (`ErrAuditPublicationLeaseLost` → metric, no inline retry). `callback_audit_integration_test.go:82`
  exercises `callback_deliveries` (delivery replay), **not** `audit_publications`.

## 2. Goals and non-goals

**Goals.** (G1) Land REQ-1: `Record` stays append-only with no publisher reference — already true in the
working tree, must survive the commit. (G2) Complete REQ-2: delete `callbackService.Publish` and its
`CallbackService` interface member so no inline enqueue entry point can be re-created by deleting "just
the branch". (G3) Pin the two unpinned legs: dead-letter terminality (A-4, including the mandatory
end-to-end MaxAttempts leg A-4.3) and lease-expiry reclaim (A-5) as integration tests. (G4) Prove via
the existing gate (`check-audit-swallow`, build, tests) that the outbox drain worker is the only
delivery path. (G5) Resolve FM-9 (REQ-6): treat a well-formed governance conflict receipt as
idempotent success, so the duplicate delivery that A-5 pins can never settle a duplicated
*successful* event as `dead_letter`. (G6) Pin the FM-8 mis-wiring leg (REQ-7): the drain
early-return on nil publications is **metered** (drain-skipped counter, §3.5) and
`wireAuditServices` fails closed at composition time, replacing the previously mis-cited
wiring-test evidence. (G7) Make the terminal settlement observable (REQ-8): every committed
publication outcome — including `dead_letter` — is counted on
`aero_control_audit_publication_outcomes_total{status=...}`, so governance non-delivery is
alertable, not silently terminal.

**Non-goals.** No change to `internal/block/` (dead parallel tree; `notification/dispatcher.go:8`
unaffected). No change to `drainPublications`/`ClaimPublication`/`FinishPublication` semantics, migration
000045, config, proto, or handlers. No new Makefile gate. No change to the `AuditPublisher` port
(`audit_ledger.go:24-26`) — it is the drain worker's port (`callbackService.publishers`,
`SetAuditPublishers` `callback.go:104-105`, implemented by `auditgovernance.Publisher` and
`sso.Client.Publish`). The `AppendInTx` consolidation of the seven write paths is the sibling batch's
scope. The `sso.Client.Publish` adapter is untouched (gRPC AuditWriter; no conflict-receipt concept).
Only the *server-side* governance dedup contract (the deployed endpoint's duplicate-response shape)
cannot be verified from this repo and remains a follow-up (§8); the client-side conflict-receipt
handling is resolved in-direction (§3.4). **No manual replay API for dead-lettered
publications** — terminal `dead_letter` is explicitly accepted (ledger durability is the
authority; non-delivery is now metered, §3.5/REQ-8); a replay feature is a recorded follow-up
(§8).

## 3. API changes

### 3.1 Deleted API — `callbackService.Publish` (the only remaining production-code change)

`internal/service/callback_audit.go` — delete the whole method (:19-27):

```go
func (s *callbackService) Publish(ctx context.Context, fact *domain.AuditFact) error {
	if fact == nil || strings.TrimSpace(fact.TenantID) == "" {
		return nil
	}
	if s.publications != nil {
		return nil
	}
	return s.enqueueFact(ctx, fact)
}
```

`internal/service/callback.go` — delete the interface member (:35):

```go
type CallbackService interface {
	Register(context.Context, CallbackInput) (*domain.CallbackSubscription, string, error)
	List(context.Context, domain.Page) ([]*domain.CallbackSubscription, string, error)
	Delete(context.Context, domain.ID) error
	Publish(context.Context, *domain.AuditFact) error   // ← remove
	ReplayDelivery(context.Context, domain.ID) error
	Start(context.Context, time.Duration) func()
}
```

After deletion `enqueueFact` (:29-45) is reachable only from `publishAuditFact` (:102-114) — the
drain-worker path. Deleting the method rather than only the branch is mandatory: a branch-only deletion
would leave an unconditional `enqueueFact` — a live second delivery path, the opposite of the direction.

### 3.2 Already-landed API change (working tree, must survive the commit) — REQ-1

`internal/service/audit_ledger.go`: `auditLedgerService` has no `publishers` field;
`NewAuditLedgerService(repo repository.AuditLedgerRepository) AuditLedgerService` is single-arg; `Record`
performs exactly one outbound persistence call (`s.repo.Append`). `AuditPublisher` interface stays.

### 3.3 Unchanged API

- `AuditPublisher` port; `SetAuditPublicationRepository` / `SetAuditPublishers` / `Start` /
  `drainPublications` / `finishPublicationAttempt` / `publicationOutcome` / `publishAuditFact`.
- Repositories, migration 000045, HTTP/gRPC handlers, protobuf, config schema.
- `auditgovernance.Publisher.Publish` and `sso.Client.Publish` (implementers of `AuditPublisher`) —
  signatures and the publish call graph are unchanged; only receipt *validation semantics* change for
  `auditgovernance` (§3.4).

### 3.4 Receipt-semantics change — conflict receipt is idempotent success (FM-9 resolution, REQ-6)

`internal/connector/auditgovernance/publisher.go` — `validateReceipt` / `receiptMatches`, no signature
change:

- `receiptMatches` no longer requires `!Conflict`; it proves the receipt echoes the exact fact sent
  (`event_id`, `tenant_id`, non-zero `accepted_at`).
- In `validateReceipt`, a `Conflict: true` receipt that passed `receiptMatches` returns `nil`
  (idempotent success) instead of falling into the status switch. Status is not re-validated on the
  conflict path: a `202` conflict response only occurs for an event already ledgered under the
  `wait_for=ledgered` contract, and the echoed `accepted_at` is the original acceptance timestamp.
- Every other shape is byte-for-byte unchanged: non-`202`, malformed envelope, mismatched echo, and
  non-conflict receipts with unknown status still return `ErrInvalidReceipt`. A conflict echoing a
  *different* event/tenant (misrouted idempotency state) is never acknowledged as this fact's success.

Why in-direction: A-5 pins duplicate delivery as designed at-least-once behavior. Without this change
the endpoint's dedup acknowledgement is mispriced as a failure and the duplicated successful event
settles `dead_letter` (terminal, no replay surface). The change is guarded by the echo match, so if the
deployed endpoint's conflict shape differs (follow-up, §8), behavior degrades to today's
`ErrInvalidReceipt` — never a false success.

### 3.5 Observability additions — settlement outcomes, drain stall, fail-closed wiring (REQ-7/REQ-8)

Three small additions make the operational story of the single delivery path true; before them,
FM-5's `dead_letter` and FM-8's drain early-return had **no signal at all** (the design's earlier
"visible as unbounded pending" claim was false — pending rows were only discoverable by direct DB
query).

1. **`ObserveAuditPublicationOutcome(status)`** →
   `aero_control_audit_publication_outcomes_total{status="completed|pending|dead_letter"}`,
   observed in `finishPublicationAttempt` **only when `FinishPublication` commits**. A failed
   finish is counted on the existing `ObserveAuditPublicationFinishFailure`, not as an outcome:
   the row stays `delivering` and the decision is re-made after lease-expiry re-claim, so an
   outcome would double-count an unsettled decision. `status="dead_letter"` is the terminal
   settlement of the only delivery path — its rate is the governance non-delivery alarm.
2. **`ObserveAuditPublicationDrainSkipped()`** →
   `aero_control_audit_publication_drain_skipped_total`, observed at the `s.publications == nil`
   early return in `drainPublications`. A non-zero value means the outbox is unwired and every
   fact pends without an attempt.
3. **`wireAuditServices` fails closed** (`internal/app/audit.go`): the silent
   `if setter, ok := ...; ok` assertions become panics. With the shipped concrete types both
   assertions always hold — the panics are unreachable insurance, mirroring the
   `requireAuditLedger` precedent from the fail-closed wiring direction. Startup stops instead
   of pend-facts-silently.

Alerting (infra side, not code): page on
`rate(aero_control_audit_publication_outcomes_total{status="dead_letter"}[15m]) > 0` and on
`aero_control_audit_publication_drain_skipped_total > 0`.

## 4. Compatibility constraints

1. **Interface-member removal breaks callers, not implementers — zero callers exist.** Repo-wide grep
   finds no `callbackService.Publish` invocation in production or test code. `make build` + `go vet`
   prove it. The only `CallbackService` consumers (`app/audit.go`, `app/grpc.go:33`,
   `handler/http/callback.go:13`, `handler/grpc/*`) use other members; no test fake implements
   `CallbackService` (no `var _ service.CallbackService`); `callback_delivery_test.go` and
   `callback_integration_test.go` exercise the concrete `*callbackService`.
2. **No wire/behavioral compatibility impact.** This is Go-internal dead-code removal: `Publish` was
   never called, and in live wiring it was additionally a no-op via the bypass branch. `Record`'s loop at
   HEAD received zero publishers from every committed composition root (`new.go:146`, `app/audit.go`),
   so its removal changes no runtime delivery. Deploying this change is behavior-neutral; correctness
   comes from the tests pinning the single path, not from a runtime switch.
3. **The single-path invariant is load-bearing, not cosmetic.** After this change, the only way a fact
   reaches a publisher is: `Append`/`AppendInTx` inserts one `pending` row (same tx, `UNIQUE(fact_id)`)
   → `drainPublications` claims it → `publishAuditFact` → `enqueueFact` + `AuditPublisher.Publish` →
   `finishPublicationAttempt` settles it. Any future wiring change that re-adds an inline publish must
   trip the `check-audit-swallow` gate (P1 bans `_ = ...Publish(`) and the A-1.1 test (single-arg
   constructor as written). Precise guard semantics: a *swallowed* inline publish (`_ = ...Publish(`)
   trips P1; a *non-swallowed* inline publish trips neither P1 nor P2 (P2 deliberately excludes
   `Publish` — the notification SSE pusher), and re-introducing a variadic constructor is **not** a
   compile error (a single-argument call compiles against a variadic signature). The A-1.1 test pins
   the single-arg constructor as written; the rest of the guard is code reading + review, not the
   compiler (see §7 A-1.1).
4. **`enqueueFact` stays private and unreachable from handlers/services.** Its only caller after §3.1 is
   `publishAuditFact`. A future handler-side "publish this fact now" requirement must go through the
   outbox (`Append` → drain), not through a resurrected enqueue path.
5. **Deployment sequencing:** no prerequisite. The new tests are integration-tagged and run only
   where Postgres is available (`make test-integration`); they add no startup-time validation.
6. **Conflict-receipt semantics are additive and guarded (§3.4).** The change converts only
   well-formed conflict receipts from failure to success; every other response maps exactly as before,
   and `sso.Client.Publish` is untouched. No configuration, migration, or wire-format change; the
   rollback of §3.4 is a small revert inside the same commit.
7. **Observability additions are additive (§3.5).** Two new counter series are added; no existing
   series is renamed or removed. The `ControlPlaneMetrics` seam grows by two members; its only
   implementers are the no-op, the registry adapter, and in-tree test spies (all updated). The
   `wireAuditServices` panics are unreachable with the shipped concrete types (both assertions
   always hold), so no shipped composition root changes behavior.

## 5. Failure modes (post-change semantics)

| FM | Scenario | Behavior after change | Evidence/test |
|---|---|---|---|
| FM-1 | `Append` fails (non-duplicate) | `Record` returns error + `ObserveAuditIngestFailure`; no publish is attempted at all (previously a swallowed inline publish could follow). Fact is not persisted → no outbox row → nothing to deliver; ingestion must be retried by the caller. | `TestAuditLedgerDuplicateMapsToSuccess`, A-1.2 |
| FM-2 | `Append` succeeds; drain worker not running or crashes before claim | `pending` row durable in same tx; no delivery until worker claims. No loss — this is the point of the outbox. | A-3.1, E6 |
| FM-3 | Worker crashes after claim, before finish (remote publish may or may not have happened) | Row stays `delivering` with future `lease_until`; excluded from claims until lease expiry; re-claimed after `lease_until < now` → at-least-once re-delivery. Duplicate delivery is possible by design; dedup is the governance endpoint's job (E8). | **A-5 (new)** |
| FM-4 | Stale `FinishPublication` (lease lost to another worker) or DB error on finish | `ErrAuditPublicationLeaseLost` → `ObserveAuditPublicationFinishFailure(status, err)`; no inline retry; row re-claimed after lease expiry. Lease-guarded UPDATE prevents double-finish. | `TestFinishPublicationFailureObservesMetricNoRetry` (unit), **A-5 (new)** for the cross-worker leg |
| FM-5 | Publisher failure exhausts `MaxAttempts` | Row settles `dead_letter` + `audit.publication_failed`; claim predicate excludes `dead_letter` forever; terminal. No path re-publishes it (after §3.1 there is no inline path at all). The committed settlement is counted on `aero_control_audit_publication_outcomes_total{status="dead_letter"}` (§3.5) — the alertable signal for governance sink non-delivery. | `TestPublicationOutcomeDeadLettersBoundPublisherFailure` (unit), **A-4 (new, integration)**, **A-8 (new)** |
| FM-6 | Tenant binding unavailable | `pending` + `audit.binding_unavailable`, attempt not incremented → retried indefinitely until binding exists (by design, attempt-free). | `TestPublicationOutcomeKeepsUnboundTenantRecoverable` |
| FM-7 | Bulkhead saturated | `Acquire` fails → `break`; remaining claimed rows stay `delivering` with lease → re-claimed later. Interactive callback traffic and governance publishes share the P6-06 bulkhead. | `drainPublications` :60-68; no new test needed (covered by A-5 mechanics) |
| FM-8 | `publications == nil` (mis-wiring) | `drainPublications` returns early and emits `aero_control_audit_publication_drain_skipped_total` (§3.5); facts pend forever, but the stall is metered, not silent. Live wiring always sets it (`app/audit.go:59-73` unconditional) and now **fails closed** (§3.5): `wireAuditServices` panics if the outbox cannot be attached (unreachable with shipped types). **Evidence correction:** the previously cited "R11 fail-closed wiring tests" do not exist in this direction — `audit_wiring_test.go` (`TestConstructorsRejectNilAudit`) covers nil-*ledger* repository-constructor panics, not `publications == nil` and not the `wireAuditServices` setter assertions; zero tests referenced `wireAuditServices`/`SetAuditPublicationRepository`/`SetAuditPublishers`. The earlier claim that mis-wiring was "visible as unbounded pending" was observability that did not exist (DB query only); the drain-skipped counter is now the actual signal. | **A-7 (new, landed)**, `wireAuditServices` |
| FM-9 | Duplicate delivery of an already-accepted governance event | Server returns `202` + conflict receipt echoing the original acceptance → **idempotent success** (§3.4): `publishAuditFact` sees `nil` → row settles `completed`. A conflict echoing a different event/tenant still fails `ErrInvalidReceipt` (guard, §3.4). | **A-6 (new): connector unit + e2e, red-green proven** |
| FM-10 | Governance endpoint down | Publisher error → `pending` + backoff with jitter (`retryDelay`), bounded by `MaxAttempts` → `dead_letter`; row is not lost at any point and the terminal settlement is metered (§3.5). | `publicationOutcome`, A-4, A-8 |

Residual-risk statement: after this change the only delivery duplication vector is at-least-once
re-claim after lease expiry (FM-3). Duplicate delivery is handled client-side as an idempotent
success: the connector sends a stable `EventID`/`IdempotencyKey` and treats a well-formed conflict
receipt as accepted (FM-9 resolution, §3.4, pinned by A-6). The remaining unverifiable-from-this-repo
piece is the deployed endpoint's duplicate-response contract (§8). Fact *loss* requires losing
Postgres itself; no in-memory state is the correctness store. **Governance non-delivery
(`dead_letter`) is a detected, metered degradation — never a silent one:** the ledger remains the
durable authority, the dead-lettered row is durable and identifies its fact, and the settlement
outcome counter (§3.5) is the alarm. Manual replay of dead-lettered publications is an explicit
follow-up (§8), not a hidden gap.

## 6. Migration steps

No SQL migration, no config change, no proto change, no new gate. Steps:

1. **Land REQ-1 as-is** (working-tree state of `internal/service/audit_ledger.go`): single-arg
   constructor, append-only `Record`, doc comment "delivery is exclusively the drain worker's job".
   Do not re-introduce the variadic publishers parameter.
2. **Apply §3.1 deletions** in one commit: remove `callbackService.Publish` (`callback_audit.go:19-27`)
   and the `CallbackService.Publish` interface member (`callback.go:35`). Verify no other file references
   the member (`grep -rn "Publish" internal/service/callback.go` → only `publishers`/`publishAuditFact`
   remain; `make build` proves no caller breaks).
3. **Apply §3.4 (FM-9 resolution, REQ-6):** `validateReceipt`/`receiptMatches` treat a well-formed
   conflict receipt as idempotent success; add the A-6 tests
   (`internal/connector/auditgovernance/publisher_test.go`, `internal/service/callback_integration_test.go`).
   Red-green proven: the A-6.2 e2e fails with `audit governance: invalid receipt` without §3.4.
4. **Add the A-4/A-5/A-7/A-8 tests and the §3.5 observability code** — dead-letter terminality
   (A-4.2 + mandatory A-4.3 drain end-to-end), lease-expiry reclaim (A-5), the mis-wiring
   units (A-7: drain-skipped counter + fail-closed wiring), and the settlement-outcome
   units (A-8) alongside the two counters and the `wireAuditServices` panics.
5. **Run the local gate** (see §9): `make test && make lint && make lint-strict && make check-file-sizes
   && make check-audit-swallow && make check-anchored-event-ids && make build`.
6. **Run `make test-integration`** (Postgres via testcontainers) — required because repository/SQL,
   outbox semantics, and the connector are touched by the tests (A-3/A-4/A-5/A-6).
7. **Deploy:** behavior-neutral dead-code removal plus a guarded receipt-semantics change; no
   rolling-restart ordering constraints, no data backfill, no dual-write window. `audit_publications`
   rows already in `pending`/`dead_letter` states are unaffected.

Rollback: revert the single commit (pure deletion + tests); no schema or data migration is involved.

## 7. Testable acceptance mapping

| Acceptance (requirements §4) | Test | Location | Status |
|---|---|---|---|
| A-1.1 single-arg constructor + gate | `TestAuditLedgerRecordEnqueuesOnly` — `NewAuditLedgerService(repo)` single-arg. **Wording correction:** re-introducing a variadic `(repo, publishers ...AuditPublisher)` is *not* a compile error — the single-argument call compiles identically against it. Real guards: the single-arg constructor as written (pinned by this test compiling today + code reading) and `check-audit-swallow` P1 (bans `_ = ...Publish(` — swallowed form only; P2 deliberately excludes `Publish`, so a non-swallowed inline publish trips neither pattern) | `internal/service/audit_ledger_test.go:46`, `scripts/check-audit-swallow.sh` | ✅ exists |
| A-1.2 unit append-only | same test: spy repo sees exactly one `Append`, action preserved | above | ✅ exists |
| A-1.3 e2e one pending row | `TestAuditLedgerRecordEnqueuesOnlyIntegration` — 1 `Record` → 1 ledger row + 1 `pending` publication row | `internal/app/audit_integration_test.go:49` | ✅ exists |
| A-2.1 static deletion proof | `grep -n "func (s \*callbackService) Publish" internal/service/callback_audit.go` and `grep -n "Publish" internal/service/callback.go` → zero hits; `make build` | — | 🔨 code change §3.1 |
| A-2.2 unit drain-only enqueue | **partial** — the finish-failure metric leg exists (`TestFinishPublicationFailureObservesMetricNoRetry`); the one-`enqueueFact`-per-claimed-item delivery-exclusivity leg is **new**. The repo-level A-3 test (`TestAuditPublicationDrainCycleSingleDelivery`) never constructs the service and cannot invoke the private `enqueueFact` (row-count invariants only), and no test referenced `drainPublications`/`enqueueFact`. **Closed by A-4.3**: 3 claims → 3 publisher calls (the publisher is only reached after `enqueueFact` succeeds) and exactly one `callback_deliveries` row (`EnqueueDelivery` is `OnConflict DoNothing` — the enqueue layer, not the drain loop, is the dedup point) | `internal/service/callback_audit_test.go:66` (exists); **A-4.3 (landed)** | partial — drain unit leg new, closed by A-4.3 |
| A-3.1 one pending row per fact | `TestAuditPublicationDrainCycleSingleDelivery` (count==1 across claim/finish/re-claim), `TestAuditWiringHappyPath` (:60), `TestAuditWiringRollbackAtomicity` (:175), `TestAuditWiringPublicationInsertRollsBackLedgerRow` (:268) | `internal/repository/` integration tests | ✅ exists |
| **A-4.1 dead_letter terminality — unit** | `TestPublicationOutcomeDeadLettersBoundPublisherFailure` — `attempt >= maxAttempts` → `dead_letter`/`audit.publication_failed` | `internal/service/callback_audit_test.go:24` | ✅ exists |
| **A-4.2 dead_letter terminality — integration (NEW)** | `TestDeadLetterPublicationNeverReclaimed` (see below) | `internal/repository/audit_consolidation_integration_test.go` (new) | ❌ new |
| **A-4.3 MaxAttempts→dead_letter end-to-end — integration (NEW, mandatory)** | `TestDrainExhaustsAttemptsIntoDeadLetter` (see below) — service-level with a failing publisher fake, real Postgres, `MaxAttempts=3`; pins the end-to-end `drainPublications` transition (today only the pure `publicationOutcome` unit covers it) and, with the enqueue/publish-count legs, closes A-2.2's missing drain unit leg; also asserts the REQ-8 outcome sequence `[pending pending dead_letter]` | `internal/service/callback_integration_test.go` | ✅ landed with this resolution |
| **A-5 lease-expiry reclaim — integration (NEW)** | `TestExpiredLeasePublicationReclaimedByAnotherWorker` (see below) | `internal/repository/audit_consolidation_integration_test.go` (new) | ❌ new |
| **A-6.1 conflict receipt = idempotent success — connector unit (NEW)** | `TestPublisherConflictReceiptIsIdempotentSuccess` (full publish flow, `202` + conflict echo → `nil`) and `TestPublisherConflictReceiptMustEchoTheFact` (different event/tenant, zero `accepted_at`, rejected status → `ErrInvalidReceipt`) | `internal/connector/auditgovernance/publisher_test.go` | ✅ landed with this resolution |
| **A-6.2 duplicate delivery settles `completed`, not `dead_letter` — e2e (NEW)** | `TestAuditPublicationDuplicateConflictReceiptSettlesCompleted` — claim → publish accepted → crash (no finish) → lease backdated → re-claim (attempt 2) → duplicate publish answered with conflict receipt → row `completed`, claim empty, 2 deliveries with identical `event_id`/`idempotency_key`. Red-green proven: fails with `audit governance: invalid receipt` without §3.4 | `internal/service/callback_integration_test.go` | ✅ landed with this resolution |
| **A-7.1 drain early-return on nil publications — unit (NEW)** | `TestDrainPublicationsEarlyReturnObservesMetric` — a `callbackService` without a publication repository: `drainPublications` returns immediately, claims nothing, observes `ObserveAuditPublicationDrainSkipped` exactly once (no panic, no outcome) | `internal/service/callback_audit_test.go` | ✅ landed with this resolution |
| **A-7.2 `wireAuditServices` fail-closed wiring — unit (NEW)** | `TestWireAuditServicesFailClosedOnMissingSetters` — `wireAuditServices` panics on a nil callback service and on a ledger repository that cannot attach the publication repository (fail-closed composition root; corrects the mis-cited nil-ledger constructor tests) | `internal/app/audit_wiring_test.go` | ✅ landed with this resolution |
| **A-8.1 settlement outcome on committed finish — unit (NEW)** | `TestFinishPublicationSettlesObservesOutcome` (committed `completed` → one outcome, no finish-failure) + `TestFinishPublicationDeadLetterSettlesObservesOutcome` (MaxAttempts exhaustion → committed `dead_letter` → one outcome) + `TestFinishPublicationFailureObservesMetricNoRetry` extended to assert an unsettled finish observes **no** outcome | `internal/service/callback_audit_test.go` | ✅ landed with this resolution |
| **A-8.2 registry text output — unit (NEW)** | `TestControlPlaneMetrics_TextOutput` extended: `aero_control_audit_publication_outcomes_total{status="dead_letter|completed"}` and `aero_control_audit_publication_drain_skipped_total` | `internal/pkg/metrics/metrics_test.go` | ✅ landed with this resolution |
| Regression guard (gate) | `make check-audit-swallow` bans `_ = ...Publish(` (P1) and bare `Record|FinishPublication|ClaimPublication(` (P2) | `scripts/check-audit-swallow.sh` | ✅ exists |

### A-4.2 — `TestDeadLetterPublicationNeverReclaimed` (new integration test, repo level)

```text
1. startAuditLedger(t, key); repo must implement AuditPublicationRepository.
2. Append fact → exactly 1 audit_publications row (existing helper assertion).
3. ClaimPublication(worker-1, 1min, 50) → 1 item (attempt=1, delivering).
4. FinishPublication(id, worker-1, "dead_letter", "audit.publication_failed", now+1h).
5. ClaimPublication(worker-1, 1min, 50) → 0 items (dead_letter excluded from predicate).
6. ClaimPublication(worker-2, 1min, 50) → 0 items (no worker sees it).
7. SQL: status='dead_letter', error_code='audit.publication_failed', count(*) == 1.
```

### A-4.3 — `TestDrainExhaustsAttemptsIntoDeadLetter` (new integration test, service level — mandatory)

```text
1. Real Postgres; NewCallbackService with cfg.MaxAttempts = 3; set the real publication repo
   (SetAuditPublicationRepository) and a failingAuditPublisher fake (always errors, counts calls);
   attach a metricSpy; seed one enabled callback subscription for the fact's action.
2. Append an audit fact → exactly 1 pending audit_publications row (same-tx insert).
3. Run the real drainPublications() loop; between cycles the row is pending with a future
   next_attempt_at (backoff), so backdate it via SQL (same pattern as A-5's lease backdate);
   stop when status == 'dead_letter' (assert termination within a bounded cycle count).
4. Assert terminal state via SQL: status='dead_letter', error_code='audit.publication_failed',
   attempt == 3 (incremented on each of the 3 claims).
5. Assert a subsequent drainPublications() + direct ClaimPublication (any worker) return 0 items
   — dead_letter is terminal.
6. Assert one enqueueFact per claimed item: publisher.calls == 3 (the publisher is reached only
   after enqueueFact succeeds) and exactly 1 callback_deliveries row for the fact (EnqueueDelivery
   is OnConflict DoNothing — the enqueue layer is the dedup point). Closes A-2.2.
7. Assert the REQ-8 outcome metric sequence [pending, pending, dead_letter] on the metricSpy.
```

Mandatory acceptance (A-4.3): this is the only test pinning the *end-to-end* MaxAttempts→
dead_letter transition (the pure `publicationOutcome` unit covers only the decision function;
the repo-level A-4.2 calls `FinishPublication` directly, bypassing the service), the first
end-to-end pin of the real `drainPublications` loop against real Postgres, and the delivery of
A-2.2's missing drain unit leg.

### A-5 — `TestExpiredLeasePublicationReclaimedByAnotherWorker` (new integration test, repo level)

```text
1. Append fact; ClaimPublication(worker-1, 5min, 50) → 1 item, status=delivering, lease_until ≈ now+5min.
2. ClaimPublication(worker-2, 5min, 50) → 0 items (unexpired lease must block other workers).
3. SQL: UPDATE audit_publications SET lease_until = now() - interval '1 minute' WHERE id = <id>.
4. ClaimPublication(worker-2, 5min, 50) → 1 item, lease_owner=worker-2, attempt incremented (2).
5. Stale finish: FinishPublication(id, worker-1, "completed", "", now) → ErrAuditPublicationLeaseLost.
6. FinishPublication(id, worker-2, "completed", "", now) → nil; count(*) == 1.
```

This is the only test that pins the `status='delivering' AND lease_until < now` claim leg and the
cross-worker fencing behavior (FM-3/FM-4).

## 8. Out-of-scope follow-up (recorded, not fixed here)

- **Governance dedup server contract (remaining half of E8/FM-9):** client-side conflict receipts are
  now idempotent successes (§3.4, REQ-6). Unverified from this repo: that the *deployed* endpoint
  actually answers a duplicate `event_id` with `202` + a receipt carrying `conflict: true` and echoing
  `event_id`/`tenant_id`/`accepted_at`. Confirm with the governance endpoint owner (contract test or
  integration smoke test against the real endpoint). If the endpoint instead uses a different shape
  (e.g. `409`), §3.4's echo guard keeps behavior failing-safe (`ErrInvalidReceipt`), and the fix must
  be revisited against the real contract before relying on it.
- **Manual replay for dead-lettered `audit_publications` — decided: follow-up, with explicit
  acceptance.** `callback_deliveries` have `ReplayDelivery`; `audit_publications` have no replay
  API (ops can only re-enqueue by flipping status in SQL). This direction **accepts** terminal
  `dead_letter` as designed: the audit ledger is the durable, authoritative evidence store (the
  governance endpoint is a downstream sink), the dead-lettered row is durable and identifies its
  fact, and non-delivery is now metered and alertable (§3.5/REQ-8) — no fact is lost, and the
  degradation is never silent. If governance delivery is contractually mandatory, the follow-up
  feature should be: an admin `ReplayPublication` that resets `dead_letter` → `pending` (attempt
  reset, `audit.publication.replayed` audit fact, reauthorization per AGENTS.md replay rules). It
  must keep the SAME `event_id`/idempotency key (= ledger row id) so a previously accepted event
  is answered with a conflict receipt → idempotent success (§3.4) — the new-idempotency-key rule
  from AGENTS.md applies to controlled command re-execution with new user intents, not to
  re-delivering the same physical event to its sink.
- **Outbox pending-depth/age alert:** the outcome and drain-skipped counters (§3.5) signal
  settlement rates and stall; a pending-row age gauge (e.g. oldest `pending`/`dead_letter`
  `audit_publications` row, SELECT-driven) is a separate metric addition if ops want staleness
  without waiting for a settlement rate to cross an alert threshold.

## 9. Done gate

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make build
make test-integration   # Postgres via testcontainers — A-3/A-4/A-5/A-4.3 live here
```

A-4.3, A-6.2, A-7.1, A-8.1/A-8.2 are landed with this resolution (unit legs run in `make test`;
A-4.3 and A-6.2 in `make test-integration`). A-4.2 and A-5 (repo-level integration tests) remain
for the landing task.

File-size headroom: `internal/service/callback.go` (465 lines, limit 500) only loses lines; the new
tests are `_test.go` (exempt from the 500-line gate). No new packages, no new migrations, no `make
proto`.
