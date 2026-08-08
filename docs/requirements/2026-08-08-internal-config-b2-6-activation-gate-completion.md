# Requirements — B2-6 activation-gate completion: B2-5 knob gate matrix, first-event governance e2e, and consolidated lease-locking tests (T-10.1, T-10.2, T-4)

| | |
|---|---|
| Module (batch label) | `configs` (T-10.1 in `internal/config`; T-10.2/T-4 reach `internal/app` + `internal/service` + `internal/repository`) |
| Source analysis | `docs/auto/analyses/configs-1fa2a926.json` (direction 2 of 3: "Complete the activation gate (B2 requirement 6) with a first-event e2e and consolidated lease-locking tests") |
| Predecessor (implemented) | `docs/requirements/2026-08-07-internal-config-b2-6-activation-gate.md` — T-10.1.1–T-10.1.6 and T-10.2a–d landed; this spec closes its remaining half and the B2-5 knob gap |
| Campaign | `aero-id-b2-in-tx-audit` |
| Date | 2026-08-08 |
| Status | Draft — every cited file/symbol re-verified against the working tree at head `13e4676` (uncommitted B2-1/B2-2/B2-5 rewiring is present and is the contract to keep). `go test ./internal/config/...` green at verification time (measured baseline). No `httptest` usage exists anywhere in `internal/app/*_test.go` (verified gap) |

## 1. Evidence verification status

All direction citations were re-checked against the working tree. Three citations are exact; two drift
(noted); one gap claim is confirmed but is partially superseded by service-package tests that landed after
the analysis — the exact chain the direction names still does not exist in one flow (E6).

| # | Claim (direction evidence) | Verification (working tree @ `13e4676`) |
|---|---|---|
| E1 | `internal/config/commercial.go:56` — `validateAuditBindings` fails Load() on `enabled` + empty endpoint/bindings | ✅ exact (`func validateAuditBindings` at `:56`; endpoint/bindings gate `:60-62`, early return on `!Enabled` `:57-59`). Called from `validateCommercialIntegrations` (`:20`, billing first `:22-25`, audit `:27-29`), reached from `Config.validate` → `validateRequired` chain |
| E2 | `internal/config/commercial_test.go:77` — `TestAuditGovernanceGateMatrix` (T-10.1.1) | ✅ exact (`:77`). Matrix covers enabled+empty bindings / enabled+empty+whitespace endpoint / disabled legs / TokenURL via sibling tests; `TestAuditTokenURLGateParityWithConnector` (T-10.1.6) at `:237`. The direction's `validAuditTokenURL` cite `commercial.go:87` drifts: the function is at `:82-97` (the `:87` anchor sits inside its body) |
| E3 | `internal/repository/audit_consolidation_integration_test.go:174/237/300` — drain cycle, dead-letter, lease reclaim | ✅ exact: `TestAuditPublicationDrainCycleSingleDelivery` `:174`, `TestDeadLetterPublicationNeverReclaimed` `:237`, `TestExpiredLeasePublicationReclaimedByAnotherWorker` `:300`. All claim tests are **sequential** (worker-1 claims → expiry backdate → worker-2 re-claims); no concurrent claim test exists anywhere (E8) |
| E4 | `internal/service/callback_audit.go:45` — `drainPublications` | ✅ exact (`func (s *callbackService) drainPublications` at `:45`). Sole non-test call site is the `Start` ticker (`internal/service/callback_worker.go:47`); `publishAuditFact` (`:157`) is the only `Publish` path; `enqueueFact` (`:27`) is reachable only from it |
| E5 | `internal/app/audit_integration_test.go:26-314` — coverage + wiring only, no first-event delivery e2e | ✅ confirmed for `internal/app`: five tests at `:26` (binding coverage), `:60` (MVCC snapshot), `:191` (concurrent provisioning stress), `:278` (enqueue-only), `:314` (`TestBuildAuditStackWiresControlMetricsIntoCallbackDrain`). The `:314` test is the only app test that drives Record → drain → completed, and it wires **zero publishers** (`AuditGovernance.Enabled` false → `buildAuditPublishers` returns nil, `internal/app/audit.go:104-105`), so `publishAuditFact` completes vacuously — it proves wiring/metrics, never delivery. No `httptest` import in any `internal/app/*_test.go` |
| E6 | First-event e2e gap: no test drives `AppendInTx` → `audit_publications` → `drainPublications` → governance endpoint 202 with dedup | ⚠️ Partially superseded at service level, gap confirmed at the composition root: (a) `TestFirstFactDrainDeliversExactlyOnce` (`internal/service/callback_integration_test.go:392`) drives Record → 1 ledger + 1 pending → `drainPublications` → exactly 1 publish → completed → second drain no-op, but with the `recordingAuditPublisher` **fake** (`:310`) — no HTTP, no 202, no receipt contract; (b) `TestAuditPublicationDuplicateConflictReceiptSettlesCompleted` (`:95`) drives a **real** httptest `/api/v1/events` (202 + receipt incl. `conflict` flag) and pins duplicate → idempotent success, never dead_letter — but seeds via `ledger.Append` (not Record) and delivers via direct `publishAuditFact` calls (not `drainPublications`). The exact chain the direction names — first fact via Record/AppendInTx → pending row → **drain worker** → **governance endpoint 202+receipt** → dedup on duplicate replay — does not exist in any single test, and no `internal/app` test exercises a real endpoint at all |
| E7 | New B2 config knobs: purge-pause/replay flags | ✅ `AuditGovernanceConfig.PurgePause bool` (`internal/config/audit_governance.go:24`), `DeadLetterRetention time.Duration` (`:27-28`); `configs/config.yaml:186-187` (`purge_pause: false`, `dead_letter_retention: 0`, "0 = never purge"); `validateAuditGovernance` rejects negative retention regardless of `Enabled` (`internal/config/validate.go:210-216`), Load-level-pinned by `TestAuditGovernanceDeadLetterRetentionValidation` (CFG-2, `config_test.go:279`). Wiring: `buildAuditStack` copies both into `CallbackConfig` (`internal/app/audit.go:52-53`, B2-5 WORKER-1); the drain purge scan honors them (`purgeDeadLetters`, `internal/service/callback_worker.go:64-67`), behavior pinned by `TestPurgeScanSkipsWhenGovernanceDisabled` (`callback_integration_test.go:957`). **Gap:** `TestAuditGovernanceGateMatrix` (E2) has no cells for these knobs; replay protection is `purge_pause` only — `cmd/audit-replay` exists (`cmd/audit-replay/main.go`, flags `-kind/-limit/-id/-tenant/-actor/-dry-run`) and adds **no config keys** (design D11), so "replay flags" in T-10.1 means the pause/retention knobs, not new keys |
| E8 | Concurrent `ClaimPublication` never double-finish: no test | ✅ confirmed gap. `ClaimPublication` uses `FOR UPDATE SKIP LOCKED` inside one tx (`internal/repository/audit_publication.go:68-71`), `FinishPublication` fences on `(id, status='delivering', lease_owner)` → `ErrAuditPublicationLeaseLost` (`:111-119`). Concurrent-append precedent `TestAppendConcurrentDuplicateBarrier` (`audit_consolidation_integration_test.go:131`) and concurrent purge-vs-requeue `TestConcurrentPurgeAndRequeueRace` (`audit_publication_replay_integration_test.go:328`) exist; no test runs two claimers against one due batch |
| E9 | Env reachability for the knob legs | ✅ `Load` uses `AERO_ID_` prefix + `.`→`_` replacer (`internal/config/config.go:374-380`); `AERO_ID_AUDIT_GOVERNANCE_PURGE_PAUSE` and `AERO_ID_AUDIT_GOVERNANCE_DEAD_LETTER_RETENTION` reach the scalars (bindings remains a map and is not env-addressable) |

Baseline: `go test ./internal/config/...` → `ok` (10 top-level tests, all T-10.1.1–T-10.1.6 tests green).

## 2. Problem statement

The activation gate (B2 requirement 6) is implemented and its original acceptance is green, but three
verified gaps remain — the "remaining half of requirement 6 as stated" plus the two knobs the B2-5 campaign
added to this module:

1. **The gate matrix has no cells for the B2-5 knobs (T-10.1).** `TestAuditGovernanceGateMatrix` pins only
   the endpoint/bindings cells and the TokenURL cells (E1/E2). `PurgePause`/`DeadLetterRetention` are
   scalars with a negative-retention rejection (E7), but that rejection is pinned only in isolation
   (CFG-2), not as matrix cells (enabled/disabled legs, defaults, env legs), and no parity pin proves a
   Load()-passing knob config is honored end-to-end by the drain purge scan (the T-10.1.6 pattern applied
   to the knob dimension).
2. **No first-event governance e2e (T-10.2).** The chain `Record`/`AppendInTx` → exactly 1 `audit_ledger`
   row + 1 `pending` `audit_publications` row → `drainPublications` → governance endpoint **202 + receipt**
   → dedup on duplicate replay is split across three tests (E5/E6), each missing at least one link, and
   `internal/app` — the package that proves "the config-gated relay actually delivers" from a real
   gate-passing `Config` — has no endpoint test at all.
3. **Concurrent claim partition is unpinned (T-4).** `FOR UPDATE SKIP LOCKED` + the lease-guarded finish
   (E8) are implemented, but every claim test is sequential; nothing proves two workers on one due batch
   never claim the same row and never double-finish it.

## 3. Requirements

### REQ-1 — The gate matrix covers the B2-5 knobs with the parity pattern (T-10.1)

1. `TestAuditGovernanceGateMatrix` (or a sibling in `internal/config/commercial_test.go`) gains one cell per
   knob dimension, each varying exactly one dimension against the valid enabled fixture
   (`commercialTestConfig()`):
   - enabled + `PurgePause: true` → `validateCommercialIntegrations` `nil` (a pause is never a rejection);
   - enabled + `DeadLetterRetention: 168h` → `nil`;
   - enabled + `DeadLetterRetention: -1h` → error containing
     `audit_governance.dead_letter_retention must not be negative`;
   - disabled + `PurgePause: true` → `nil` (inert when the relay is disabled — the drain loop does not run);
   - disabled + `DeadLetterRetention: -1h` → same negative-retention error (pin the existing CFG-2 contract
     as a matrix cell: `validateAuditGovernance` runs regardless of `Enabled`, `validate.go:210-216`);
   - defaults: knobs absent → `PurgePause == false`, `DeadLetterRetention == 0` ("0 = never purge",
     `configs/config.yaml:187`).
2. **Parity, not approximation (T-10.1.6 pattern):** a knob config that passes `Load()` must be honored
   end-to-end: `buildAuditStack` copies `cfg.AuditGovernance.PurgePause`/`DeadLetterRetention` into
   `CallbackConfig` (`app/audit.go:52-53`), and a paused drain tick performs **no SELECT and no DELETE** in
   `purgeDeadLetters` (`callback_worker.go:64-67`). The service leg is already pinned by
   `TestPurgeScanSkipsWhenGovernanceDisabled`; the new pin is the config→wiring leg at the composition root:
   `buildAuditStack` with `PurgePause: true` + `DeadLetterRetention: 1h` yields a drain whose paused tick
   leaves dead-letter rows bit-for-bit (no repository call), matching the Load()-level contract.
3. **Env legs (T-10.1.4 pattern):** `AERO_ID_AUDIT_GOVERNANCE_PURGE_PAUSE=true` and
   `AERO_ID_AUDIT_GOVERNANCE_DEAD_LETTER_RETENTION=1h` land in the merged struct the gate and the drain
   read, and env cannot bypass the negative-retention rejection.
4. No new config keys: `cmd/audit-replay`'s flags (`-actor`, `-tenant`, `-kind`, …) are CLI flags, not
   config surface (design D11); `purge_pause` is the replay/purge race protection and the only knob this
   requirement adds cells for.

### REQ-2 — First-event e2e through the real governance endpoint (T-10.2)

A composition-root integration test in `internal/app/audit_integration_test.go` drives the config-gated
relay end-to-end from a Load()-valid `Config` (the E11 httptest precedent:
`internal/connector/auditgovernance/publisher_test.go:40-70`):

1. **Fixture:** an httptest server serving `/token` (client_credentials form → `{"access_token", "token_type",
   "expires_in"}`) and `/api/v1/events` (202 + receipt with `event_id`, `tenant_id`, `status: "ledgered"`,
   `accepted_at`, and `conflict` set when the event_id was already accepted). `Config` with
   `AuditGovernance{Enabled: true, Endpoint: server.URL, AllowInsecureLoopback: true, SourcePrefix: "aero-id",
   Bindings: {tenant-one: {TokenURL: server.URL+"/token", ClientID, ClientSecretEnv (t.Setenv, ≥32 chars),
   Scope: auditWriteScope, Resources: [audit-governance]}}}` — the gate (E1) and
   `validateAuditBindingCoverage` (`app/audit.go:120`, `:165`) must pass, so the test also proves a
   gate-passing config never fails at connector construction (`buildAuditPublishers`, `app/audit.go:103-148`).
2. **First event:** `Record` one fact with a known `EventID` → exactly **1** `audit_ledger` row and exactly
   **1** `audit_publications` row (`status='pending'`, `attempt=0`, `lease_owner=''`); no POST yet. One
   drain cycle (`callbackSvc.Start` ticker, the pattern proven by
   `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain`, `app/audit_integration_test.go:314`) → exactly
   **one** POST to `/api/v1/events` returning **202**; body `event_id == idempotency_key ==` the known
   fact ID (AT-4); the row settles `completed`, `error_code=''`.
3. **Duplicate drain replay (dedup by receipt contract):** simulate crash-after-remote-success — backdate
   `lease_until` (DB clock) without `FinishPublication`; the next drain re-claims and re-POSTs the same
   `event_id`; the endpoint answers with a **conflict receipt**; the row settles `completed` (never
   `dead_letter`), exactly one conflict reply, no further POSTs, and the ledger row count stays exactly 1
   (no re-append). At-least-once transport, exactly-once outcome per `event_id`.
4. Keep-enforced: `TestAuditLedgerRecordEnqueuesOnlyIntegration` (`app:278`) and
   `TestFirstFactDrainDeliversExactlyOnce` (`service:392`) stay green **unchanged** — this test adds the
   missing endpoint/receipt legs, it does not re-litigate the counts.

### REQ-3 — Concurrent claim workers on one due batch never double-finish (T-4)

1. **Repository leg (beside the lease tests, `internal/repository/audit_consolidation_integration_test.go`):**
   seed N ≥ 20 due pending rows via the public surface (`Append`); spawn W = 4 workers, each looping
   `ClaimPublication(ctx, worker-i, lease, limit)` until it returns 0; assert: the union of claimed row IDs
   across workers is exactly the seeded set (**each row claimed exactly once** — the `FOR UPDATE SKIP
   LOCKED` partition), then each row is finished once (`completed`, `attempt=1`), a second finish by a
   different worker is fenced with `ErrAuditPublicationLeaseLost`, no row remains `delivering`, total
   `completed` == N. Run with `-race`.
2. **E2e fixture leg (same fixture as REQ-2):** one due batch of N first facts drained with
   `WorkerConcurrency ≥ 2` (the claim→finish fan-out in `drainPublications`, `callback_audit.go:72-91`):
   per-row single settlement, per-`event_id` exactly one accepted POST (all 202), total POSTs == N — the
   lease pattern of E3 extended to the composition-root fixture.

## 4. Testable acceptance mapping

| ID | Direction acceptance (verbatim) | Test pin | Executable assertions |
|---|---|---|---|
| **T-10.1** | extend the gate matrix with the new B2 config knobs (purge-pause/replay flags) using the parity pattern of `validAuditTokenURL` (T-10.1.6) in `internal/config/commercial.go:87` | `internal/config/commercial_test.go` — extend `TestAuditGovernanceGateMatrix` (E2) or add sibling `commercial_knob_test.go`; parity pin at the composition root reusing `buildAuditStack` (`internal/app/audit_integration_test.go` or `audit_wiring_test.go`) | T-10.1.7 — matrix cells per REQ-1.1: enabled+`PurgePause:true` → `nil`; enabled+`DeadLetterRetention:168h` → `nil`; enabled+`DeadLetterRetention:-1h` → error containing `audit_governance.dead_letter_retention must not be negative`; disabled+`PurgePause:true` → `nil`; disabled+`DeadLetterRetention:-1h` → same error; absent knobs → `false`/`0` defaults. Fixture: mutate `commercialTestConfig()` (`commercial_test.go:270`); keep `setCommercialSecrets` for credential legs. T-10.1.8 — parity: `buildAuditStack` with `PurgePause:true` + `DeadLetterRetention:1h` (gate-passing) → paused tick performs no purge (dead-letter rows preserved bit-for-bit, no SELECT/DELETE — matches `TestPurgeScanSkipsWhenGovernanceDisabled` service leg); a config that passes `Load()` never diverges from the drain's honor of the knobs. T-10.1.9 — env legs: `AERO_ID_AUDIT_GOVERNANCE_PURGE_PAUSE=true` / `AERO_ID_AUDIT_GOVERNANCE_DEAD_LETTER_RETENTION=1h` over a file-default config → merged struct carries them; env `-1h` → Load error (env cannot bypass validation). Existing T-10.1.1–T-10.1.6 tests stay green unchanged |
| **T-10.2** | first-event e2e asserts exactly 1 `audit_ledger` row + 1 `audit_publications` row (status pending), exactly one governance POST returning 202, and a duplicate drain replay is deduplicated by the endpoint receipt contract | `internal/app/audit_integration_test.go` — new test (no `httptest` exists in the package today, E5); fixture per REQ-2.1 with the E11 httptest precedent | T-10.2e — phase 1: after `Record` of a first fact with known `EventID`: exactly 1 `audit_ledger` row (`WHERE event_id = ?`), exactly 1 `audit_publications` row joined on `fact_id` (`status='pending'`, `attempt=0`, `lease_owner=''`); zero POSTs before the drain; one drain cycle → exactly **1** POST to `/api/v1/events` with 202; request body `event_id == idempotency_key ==` fact ID (AT-4); row settles `completed`, `error_code=''`. T-10.2f — phase 2 (duplicate drain replay): backdate `lease_until` to `now() - interval '1 minute'` (DB-clock pattern, `audit_consolidation_integration_test.go:347`) with no `FinishPublication` (crash after remote success); next drain re-claims and re-POSTs; endpoint replies 202 + receipt with `conflict: true`; assert row `completed` (never `dead_letter`), exactly 1 conflict reply, POST count == 2 and no further POSTs, `audit_ledger` count still exactly 1. Existing `TestAuditLedgerRecordEnqueuesOnlyIntegration` (`app:278`) and `TestFirstFactDrainDeliversExactlyOnce` (`service:392`) stay green unchanged |
| **T-4** | concurrent `ClaimPublication` workers on one due batch never double-finish (existing lease test pattern extended to the e2e fixture) | Repository leg beside E3 (`internal/repository/audit_consolidation_integration_test.go`); e2e leg on the T-10.2e fixture | T-4.1 — N=20 due pending rows (public surface), W=4 concurrent workers claiming in a loop until empty: union of claimed IDs == seeded set with **no overlap** (each row claimed exactly once); every row finished exactly once (`completed`, `attempt=1`); a duplicate finish from a second worker → `ErrAuditPublicationLeaseLost` (fence, E8); zero rows left `delivering`; `-race` clean. T-4.2 — e2e leg: one due batch of N first facts drained with `WorkerConcurrency ≥ 2` on the T-10.2e fixture: N rows `completed`, per-`event_id` exactly one accepted POST (202), total POSTs == N (no double delivery, no double finish) |

## 5. Scope boundaries (explicit non-goals)

- **No new config keys.** `cmd/audit-replay` flags (`-actor`, `-tenant`, `-kind`, `-id`, `-limit`,
  `-dry-run`) are CLI surface, not config (design D11); the replay-protection knob is
  `audit_governance.purge_pause`. `data_retention.purge_paused` (outbox cleaner) is sibling surface,
  already pinned by T-5.1 (`config_test.go:208`) — untouched.
- **No behavior change** to `validateAuditBindings`, `ClaimPublication`, `FinishPublication`,
  `drainPublications`, `purgeDeadLetters`, or `buildAuditStack`; all REQ-2/REQ-3 code paths are shipped —
  this spec adds test pins and keep-enforced greps only. The negative-retention rejection (CFG-2) is
  existing behavior, re-pinned as matrix cells, not new validation.
- **TokenURL/endpoint/bindings dimensions**: already pinned by T-10.1.1–T-10.1.6 (predecessor spec) —
  no new cells there; billing bindings unchanged (predecessor §5).
- **No new migrations, no protobuf changes, no new packages** (test-only helpers allowed in
  `internal/app`, `internal/config`, and `internal/repository` test files).
- Requeued-publication delivery (`TestRequeuedPublicationDrainsExactlyOnce`, `service:878`) and the
  purge-scan contract (D9) belong to the B2-5 campaign; referenced only as fixtures, not re-specified.

## 6. Engineering gates

- Unit leg: `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow
  && make check-anchored-event-ids && make build`. T-10.1 adds ≤ 40 lines to
  `internal/config/commercial_test.go` (or a sibling under the 500-line rule); no non-test source changes,
  so file-size/cyclomatic gates are unaffected.
- Integration leg: `make test-integration` — required: T-10.2e/f and T-4.1/4.2 drive real PostgreSQL
  (testpg), the real publication repository, the real OAuth token source, and a real HTTP endpoint
  (AGENTS.md integration gate). All new tests carry the `integration` build tag and are compiled by
  `make check-integration-compile`.
- Keep-enforced greps (unchanged from predecessor T-10.2d): `publisher.Publish` reachable only from
  `publishAuditFact` (`callback_audit.go:157`); `drainPublications` callable outside tests only from the
  `Start` ticker (`callback_worker.go:47`); `make check-audit-swallow` green.
- Determinism: phase-2 lease expiry uses the DB clock backdate pattern already proven at
  `audit_consolidation_integration_test.go:347` (M-1); no sleeps beyond the existing
  `waitForLockPoll`/polling patterns.
