# Design — B2-6 activation-gate completion: B2-5 knob gate matrix, first-event governance e2e, consolidated lease-locking tests (T-10.1, T-10.2, T-4)

| | |
|---|---|
| Module (batch label) | `configs` — **label only**. Change surface: `internal/config/commercial_test.go` (+ env legs in `internal/config/config_test.go`), `internal/app/audit_integration_test.go`, `internal/repository/audit_consolidation_integration_test.go`. Zero production source changes |
| Input evidence | `docs/requirements/2026-08-08-internal-config-b2-6-activation-gate-completion.md` (evidence-backed requirements; all claims independently re-verified for this design, see §0) |
| Predecessor | `docs/requirements/2026-08-07-internal-config-b2-6-activation-gate.md` (T-10.1.1–T-10.1.6, T-10.2a–d landed) |
| Date | 2026-08-08 |
| Status | **Draft design — amended after adversarial review (audit-gate contract / concurrency / test-design); all blocking deltas landed, see amendment register §7.4–§7.9.** All evidence claims verified on head `13e4676`; four minor line-anchor drifts found (none material, §0). No production API change; the design adds test pins only, per the requirement's explicit scope boundary (§5 of the requirements) |

## 0. Verification verdict (evidence claims, re-checked on head `13e4676`)

The evidence's nine claims (E1–E9) were treated as untrusted and re-checked by reading the cited files at
the pinned commit. **All nine hold.** Three line-anchor drifts in the evidence itself are confirmed accurate
(`validAuditTokenURL`, the matrix-vs-knobs gap, the service-level partial supersession of the e2e chain); four
additional anchor drifts were found that the evidence under-reported — all are comment/neighbor-line anchors,
none changes a symbol, a semantic, or an acceptance target.

| # | Evidence claim | Verdict | Verified at |
|---|---|---|---|
| E1 | `validateAuditBindings` at `commercial.go:56`; endpoint/bindings gate `:60-62`; early return `!Enabled` `:57-59` | ✅ exact | `internal/config/commercial.go` |
| E2 | `TestAuditGovernanceGateMatrix` at `commercial_test.go:77`; T-10.1.6 parity test at `:237`; `validAuditTokenURL` drift (`:82-97`, not `:87`) | ✅ exact, incl. the drift note | `commercial_test.go:77,237`; func at `:82` |
| E3 | Drain cycle / dead-letter / lease reclaim at `audit_consolidation_integration_test.go:174/:237/:300`; all sequential | ✅ exact (read all three; worker-1 claims → DB-clock backdate → worker-2 re-claims) | grep + read |
| E4 | `drainPublications` at `callback_audit.go:45`; sole non-test call site is the `Start` ticker (`callback_worker.go:47`); `publishAuditFact` `:157`; `enqueueFact` `:27` | ✅ exact | grep + read |
| E5 | App tests at `audit_integration_test.go:26/:60/:191/:278/:314`; `:314` wires zero publishers (`AuditGovernance.Enabled` false → `buildAuditPublishers` nil at `app/audit.go:104`); no `httptest` anywhere in `internal/app/*_test.go` | ✅ exact (`grep -c httptest internal/app/*_test.go` → zero) | read + grep |
| E6 | Service-level partial supersession: `TestFirstFactDrainDeliversExactlyOnce` `:392` (fake `recordingAuditPublisher` `:310`); `TestAuditPublicationDuplicateConflictReceiptSettlesCompleted` `:95` (real httptest 202 + conflict receipt, seeded via `ledger.Append` `:145`, delivered via direct `publishAuditFact` `:154/:172` — never `drainPublications`); exact chain absent in one flow | ✅ exact | read `:95-200`, `:392-440` |
| E7 | Knobs `PurgePause`/`DeadLetterRetention`; wiring `app/audit.go:52-53`; `purgeDeadLetters` guard `callback_worker.go:64-67`; CFG-2 `config_test.go:279`; `cmd/audit-replay` CLI-only flags | ✅ (minor drift: fields at `audit_governance.go:23/:26`, evidence cited `:24-28` — comment lines) | read |
| E8 | No concurrent-claim test; `ClaimPublication` SKIP LOCKED in one tx; `FinishPublication` fence `(id, status='delivering', lease_owner)` → `ErrAuditPublicationLeaseLost` | ✅ (anchor drift: the `clause.Locking{...SkipLocked}` select is `audit_publication.go:55-56`, not `:68-71`; `:68-71` anchors the same tx's `lease_until` update expr; fence at `:111-122`) | read |
| E9 | Env reachability: `AERO_ID_` prefix + `.`→`_` replacer | ✅ (anchor drift: `SetEnvKeyReplacer` at `config.go:385`, evidence cited `:374-380` — struct fields) | grep |

Supporting facts re-verified: `configs/config.yaml:186-187` (`purge_pause: false`, `dead_letter_retention: 0`,
"0 = never purge") ✅; DB-clock lease backdate `now() - interval '1 minute'` at
`audit_consolidation_integration_test.go:347` ✅; `TestConcurrentPurgeAndRequeueRace` at
`audit_publication_replay_integration_test.go:328` ✅; E11 httptest precedent
(`internal/connector/auditgovernance/publisher_test.go:40-70`: `/token` client-credentials form, `/api/v1/events`
202 + receipt with `event_id`/`tenant_id`/`status:"ledgered"`/`accepted_at`) ✅; matrix cells today cover only
endpoint/bindings/disabled legs — **no knob cells** (read `:77-112`) ✅; `validateAuditGovernance` is
`Enabled`-independent and reachable only via `validateRuntimeLimits` (`validate.go:130`, func `:211-215`), i.e.
**not** through `validateCommercialIntegrations` — this drives the matrix cell entry-point split in §1.1.

**Conclusion:** the requirements spec is trustworthy as-is. The four additional drifts are recorded in §1.1
where they affect implementation detail (matrix entry points) and in the amendment register §7.

## 1. Design

### 1.1 API changes

**Production API: none.** No new config keys, no behavior changes to `validateAuditBindings`,
`ClaimPublication`, `FinishPublication`, `drainPublications`, `purgeDeadLetters`, `buildAuditStack`,
`cmd/audit-replay` (design D11: `-config/-kind/-limit/-id/-tenant/-actor/-dry-run` stay CLI-only), no
migrations, no protobuf, no new packages, no new HTTP/gRPC surface. This is a test-pin-only campaign.

**Test-surface deltas** (the only "API" additions):

1. **Knob matrix cells** — `internal/config/commercial_test.go` (extend `TestAuditGovernanceGateMatrix` at
   `:77`, ≤ 40 lines; or a sibling `commercial_knob_test.go` if the file approaches 500 lines — it is at 282
   today). Entry-point split is load-bearing:
   - *Nil cells* (enabled+`PurgePause:true`, enabled+`DeadLetterRetention:168h`, disabled+`PurgePause:true`)
     call `cfg.validateCommercialIntegrations()` on a mutated `commercialTestConfig()` (`:270`) — a pause or
     positive retention must never trip the commercial gate.
   - *Rejection cells* (enabled+`-1h`, disabled+`-1h`) call `validateAuditGovernance(cfg.AuditGovernance)`
     directly: `validateAuditGovernance` is not in the `validateCommercialIntegrations` chain (it sits in
     `validateRuntimeLimits`, `validate.go:130`) and does not read `Enabled` — the cells pin exactly that
     Enabled-independence and re-pin the CFG-2 contract (`config_test.go:279`) in matrix shape.
   - *Defaults cell* asserts absent knobs → `false`/`0` on the unmutated fixture.
2. **Env legs** — `internal/config/config_test.go`, sibling of `TestLoad_envOverride` (`:59`): write a
   minimal file-default YAML that **must carry `audit_governance: {purge_pause: false, dead_letter_retention: 0}`
   — the default-valued keys, mirroring `configs/config.yaml:174` — and must never carry the env values
   themselves** (`true`/`1h`). viper `AutomaticEnv` is lazy (T-5.1's own comment at `config_test.go:212`
   documents it: "the env value wins when the key exists in the file"), so a file key is the precondition
   for the env merge; a file carrying the env values would be the one wrong-reason pass (assertion green
   without any env plumbing). Then `t.Setenv("AERO_ID_AUDIT_GOVERNANCE_PURGE_PAUSE", "true")` /
   `AERO_ID_AUDIT_GOVERNANCE_DEAD_LETTER_RETENTION=1h`, `Load` → merged struct carries both; env `-1h` →
   Load error containing `audit_governance.dead_letter_retention must not be negative` (env cannot bypass
   validation; a duration-parse failure yields a different error, so the substring assertion prevents
   wrong-reason passes). Uses the existing replacer (`config.go:385`); bindings remain non-env-addressable
   — no leg for them.
3. **App e2e fixture** — `internal/app/audit_integration_test.go` (no `httptest` exists there today; the
   package gains its first). Shared helper `newAuditGovernanceFixture(t)` per REQ-2.1:
   - httptest server: `/token` (client_credentials form → `{"access_token","token_type","expires_in"}`) and
     `/api/v1/events` (202 + receipt `{event_id, tenant_id, status:"ledgered", accepted_at}`; `conflict`
     set when the event_id was already accepted) — the E11 precedent shape (`publisher_test.go:40-70`).
   - `config.Config` with `AuditGovernance{Enabled:true, Endpoint: server.URL, AllowInsecureLoopback:true,
     SourcePrefix:"aero-id", Bindings:{tenant-one:{TokenURL: server.URL+"/token", ClientID, ClientSecretEnv
     (t.Setenv, ≥32 chars), Scope:"audit:event:write", Resources:["audit-governance"]}}}`, plus the
     `AuditLedger`/`Callback{MasterKey, WorkerConcurrency}` fields the `:314` test proves sufficient.
   - `buildAuditStack` → real connector publishers via `buildAuditPublishers` (`app/audit.go:103-148`) →
     coverage gate (`validateAuditBindingCoverage` called at `app/audit.go:120`, func at `:165`) passes
     trivially: `testpg.Start` gives a fresh throwaway container per test, so the coverage UNION query
     sees **zero tenants and returns nil before any `Record`**. The fixture therefore follows the `:314`
     order — `buildAuditStack` first, `Record` second, `Start` third — documented so nobody reorders to
     "Record first" or believes the gate needs the first fact. The `:26` test pattern is the UNION's
     *positive* leg (rows present → covered), not the fixture's gate-pass mechanism.
4. **Repository concurrent harness** — `internal/repository/audit_consolidation_integration_test.go` beside
   the E3 lease tests: seed N=20 due pending rows through `Append` (public surface), spawn W=4 workers each
   looping `ClaimPublication(ctx, worker-i, lease, limit)` until empty. Harness shape (all items mandatory
   after review):
   - **Overlap is asserted by multiplicity, not set union.** Workers record claims in a mutex-guarded
     `map[id]int` (counts) plus an error channel; the test goroutine asserts after `wg.Wait()`:
     `len(map) == N`, every entry == 1, `sum(map) == N`. A set union cannot see a double claim (two
     workers claiming the same row still yields a set of size N); the multiplicity map can.
   - **Start barrier + claim `limit < N`** (e.g. `limit=4` with N=20): all workers block on a closed
     `start` channel (the `TestAppendConcurrentDuplicateBarrier` pattern, repo `:131`) so the first claim
     wave is simultaneous, and the bounded limit forces ≥ 2 waves (with the default `limit=50` one worker
     could drain the whole seed before the others' first claims).
   - **Per-worker `claimed >= 1`** — deterministic under barrier + `limit < N` (4 workers × limit 4 → all
     four claim in wave 1). A test comment states that the assertions are the deterministic contract and
     overlap is best-effort exercise, so nobody later "fixes" the harness with sleeps.
   - **Workers never call `t.Fatalf`** (FailNow from a non-test goroutine is misuse/panic risk): workers
     record errors on the channel; every assertion runs in the test goroutine after `wg.Wait()`.
   - **Finishes happen inside the worker loop** (claim → finish by the claiming worker), which yields
     claim-vs-finish interleavings for free; the duplicate-finish fence probe (`FinishPublication` by a
     different worker → `ErrAuditPublicationLeaseLost`) runs **post-phase**, after all workers exited, so
     it cannot race a legitimate owner.
   - **`-race` is hygiene, not mechanism**: the partition proof is Postgres row locks (SKIP LOCKED claim
     tx + `(id, 'delivering', lease_owner)` fence), not the harness mutexes; one sentence in the test
     comment prevents conflating them.
   - **SKIP LOCKED is behavior-pinned, not clause-pinned** (documented limitation): replacing `SKIP LOCKED`
     with plain `FOR UPDATE` still partitions (transactions serialize) — T-4.1 stays green and the
     regression surfaces as head-of-line blocking. A clause-presence pin would need timing probes and be
     flaky; behavior-pinning is the deliberate trade, recorded as a decision, not a hole.

### 1.2 Production symbols the pins contract (reference)

| Symbol | Location | Pinned property |
|---|---|---|
| `validateAuditBindings` | `commercial.go:56-80` | enabled + empty endpoint/bindings fails; TokenURL/credential rules unchanged (E1) |
| `validAuditTokenURL` | `commercial.go:82-97` | parity with connector stays cross-layer (T-10.1.6, unchanged) |
| `validateAuditGovernance` | `validate.go:211-215` | negative retention rejected regardless of `Enabled` |
| `ClaimPublication` | `audit_publication.go:54-76` (`clause.Locking{...SkipLocked}` at `:55-56`) | row partition under concurrency (T-4.1) |
| `FinishPublication` | `audit_publication.go:111-122` | `(id, 'delivering', lease_owner)` fence → `ErrAuditPublicationLeaseLost` |
| `drainPublications` | `callback_audit.go:45-97` | ticker-only entry; claim→finish fan-out (`:72-91`) |
| `purgeDeadLetters` | `callback_worker.go:64-67` | pause/retention guard runs before any SELECT/DELETE |
| `buildAuditStack`/`buildAuditPublishers` | `app/audit.go:34-101`/`:103-148` | knob copy `:52-53`; gate-passing config constructs publishers |
| env replacer | `config.go:385` | `AERO_ID_AUDIT_GOVERNANCE_*` reach the scalars |

## 2. Compatibility constraints

- **Zero production delta** — the done gate cannot regress on file-size/cyclomatic (`check-file-sizes`
  exempts tests; funlen/cyclop still apply — helpers stay ≤ 50 lines), `check-audit-swallow` (no new
  production audit sites), or `check-anchored-event-ids` (no new event-ID mint sites).
- **Keep-enforced greps unchanged** (requirements §6): `publisher.Publish` reachable only from
  `publishAuditFact` (`callback_audit.go:157`); `drainPublications` callable outside tests only from the
  `Start` ticker (`callback_worker.go:47`). The new app e2e must not call either directly — it drives the
  real ticker only.
- **Existing tests stay green unchanged** (requirements §4 keep-enforced list, completed after review):
  T-10.1.1–T-10.1.6, CFG-2 (`TestAuditGovernanceDeadLetterRetentionValidation`, `config_test.go:279`),
  `TestLoad_envOverride` (`config_test.go:59`), **T-5.1 `TestDataRetentionPurgePausedValidation`
  (`config_test.go:212` — requirements cite `:208`, pre-`TestLoadAuditGovernanceTokenURLGate` numbering;
  its lazy-env comment is the mechanism §1.1.2 depends on)**, `TestAuditLedgerRecordEnqueuesOnlyIntegration`
  (`app:278`), `TestFirstFactDrainDeliversExactlyOnce` (`service:392`),
  `TestAuditPublicationDuplicateConflictReceiptSettlesCompleted` (`service:95`),
  `TestPurgeScanSkipsWhenGovernanceDisabled` (`service:957`), and **all three E3 sequential claim siblings**
  `TestAuditPublicationDrainCycleSingleDelivery` (`repo:174`), `TestDeadLetterPublicationNeverReclaimed`
  (`repo:237`), `TestExpiredLeasePublicationReclaimedByAnotherWorker` (`repo:300`),
  `TestRequeuedPublicationDrainsExactlyOnce` (`service:878`, B2-5 campaign, used as fixture only).
  `TestConcurrentPurgeAndRequeueRace` (`audit_publication_replay_integration_test.go:328`, B2-5) is the
  status-transition partition precedent the T-4.1 harness parallels; the two stay separate regression
  classes — the T-4.1 harness never calls purge/requeue.
- **Determinism**: phase-2 lease expiry uses the DB-clock backdate pattern (`audit_consolidation_integration_test.go:347`);
  drain settling uses the deadline-poll pattern (`app/audit_integration_test.go:334`); no sleeps.
- **Integration tag** on all new PostgreSQL/HTTP tests; `make check-integration-compile` must compile them.
- **Scope**: requeue/purge-scan contracts (B2-5) are referenced as fixtures, not re-specified (requirements §5).

## 3. Failure modes (each new pin's catch)

| Failure (future regression) | Catch | Symptom the pin makes red |
|---|---|---|
| `PurgePause` becomes a gate rejection | T-10.1.7 nil cells | enabled+`PurgePause:true` returns an error |
| Negative-retention rejection relaxed or made `Enabled`-gated | T-10.1.7 rejection cells (incl. disabled leg) | `validateAuditGovernance` accepts `-1h` |
| Knob defaults change | T-10.1.7 defaults cell | absent knobs no longer `false`/`0` |
| `buildAuditStack` stops copying knobs (`app/audit.go:52-53`) | T-10.1.8 parity pin | positive retention no longer purges (falls back to default 0 = never purge) or pause no longer suppresses |
| `purgeDeadLetters` guard removed (scan runs while paused) | T-10.1.8 parity pin | paused tick deletes past-retention dead-letter rows |
| Mapstructure/env key path breaks | T-10.1.9 | env legs fail to carry or reject |
| Record double-appends / inline publish / idempotency-key drift | T-10.2e | >1 ledger row, POST before drain, body `event_id`/`idempotency_key` != ledger row id |
| Hardcoded-positive retention copy in `buildAuditStack` (e.g. `time.Hour`) | T-10.1.8 leg 2 boundary | inside-retention row purged or past-retention row kept (boundary breaks) |
| Conflict receipt mapped to failure (dead-letter on duplicate) | T-10.2f | row settles `dead_letter` after conflict reply |
| Re-append on replay (ledger row minted again) | T-10.2f | `audit_ledger` count > 1 after phase 2 |
| Claim overlap / lost row / double-finish / stuck `delivering` | T-4.1 | multiplicity map: double claim → entry 2 / `sum > N`; lost row → `len(map) < N`; second finish not fenced; `delivering` remains |
| Double delivery per event_id at the composition root | T-4.2 | POST count > N or a non-202 reply |
| Gate-passing config fails connector construction | REQ-2.1 (fixture) | `buildAuditPublishers` returns error on the `Load()`-valid config |

## 4. Testable acceptance mapping (requirements acceptance → concrete pins)

| ID | Requirements pin | New test | Executable assertions |
|---|---|---|---|
| **T-10.1.7** | matrix cells per REQ-1.1 | `TestAuditGovernanceGateMatrix` extension (or `commercial_knob_test.go`) | 6 cells: enabled+`PurgePause:true` → `validateCommercialIntegrations` nil; enabled+`DeadLetterRetention:168h` → nil; enabled+`-1h` → `validateAuditGovernance` error contains `audit_governance.dead_letter_retention must not be negative`; disabled+`PurgePause:true` → nil; disabled+`-1h` → same error; defaults → `false`/`0`. **Precision (review):** only the **disabled** `-1h` cell pins Enabled-independence — it errors even under an `Enabled`-gated regression; the enabled `-1h` cell pins only "rejection exists at all". Keep both cells; drop neither |
| **T-10.1.8** | parity, composition-root knob honor (REQ-1.2) | `TestBuildAuditStackHonorsPurgeKnobs` (internal/app, ticker-driven) | **Leg 1 (paused):** fixture with `PurgePause:true, DeadLetterRetention:1h`; seed **one pending row** plus a past-retention dead-letter row via SQL backdating `updated_at` (the repo predicate is `updated_at < cutoff`, `audit_publication_replay.go:148`; the service precedent backdates `updated_at`, `callback_integration_test.go:960`). **Tick-progress proof:** deadline-poll until the pending row reaches `completed` (proves ≥ 1 tick ran; the purge metric is unusable — it stays 0 while paused; no sleeps), *only then* assert the dead-letter row is preserved bit-for-bit (id/status/attempt/`next_attempt_at`/`updated_at` unchanged, no DELETE). **Leg 2 (unpaused boundary value):** a **second `buildAuditStack` + `Start` from a `PurgePause:false` config** on the same fixture DB (the leg-1 dead-letter row is still dead_letter and still past retention) — never mutate the running service's `cfg`, which would bypass the composition-root copy at `app/audit.go:52-53`; seed **two** dead-letter rows, one at `now()-10 days` (past retention) and one at `now()-10 min` (inside retention); assert exactly the past row is purged and the `audit_ledger` fact survives. The boundary value catches a hardcoded *positive* retention that both single-row legs would green-light; the two-leg shape distinguishes knob plumbing from default-0 inertness |
| **T-10.1.9** | env legs (REQ-1.3) | `TestLoadAuditGovernanceKnobEnvLegs` (internal/config) | File-default YAML carries `audit_governance.purge_pause: false` / `dead_letter_retention: 0` (never the env values — §1.1.2); `AERO_ID_AUDIT_GOVERNANCE_PURGE_PAUSE=true` + `AERO_ID_AUDIT_GOVERNANCE_DEAD_LETTER_RETENTION=1h` over those file defaults → merged struct carries both (`true`/`1h` prove env-wins-over-file); env `-1h` → Load error containing the negative-retention message |
| **T-10.2e** | first-event e2e phase 1 (REQ-2.1/2.2) | `TestFirstEventGovernanceE2E` (internal/app) | `Record` known `EventID` → exactly 1 `audit_ledger` row (`WHERE event_id = ?`) + exactly 1 `audit_publications` row (`pending`, `attempt=0`, `lease_owner=''`), zero POSTs; one drain tick → exactly 1 POST 202 to `/api/v1/events`. **Pin target = the ledger row id, not the known `EventID`:** `SELECT id FROM audit_ledger WHERE event_id = <known>`; assert body `event_id == idempotency_key == <row id>` (AT-4). The caller-known `EventID` is the dedup key in the `event_id` column and is *never* what the body carries (`buildAuditFact` mints the row id; `publisher.go:204/:211` emit `fact.ID.String()`; service precedent `callback_integration_test.go:195`) — pinning the known `EventID` instead would be red under current production and invite dropping the assertion; row `completed`, `error_code=''` |
| **T-10.2f** | duplicate replay dedup (REQ-2.3) | same test, phase 2 | **Explicitly reset the row to `status='delivering'` AND backdate `lease_until` to `now() - interval '1 minute'`** (DB clock, `audit_consolidation_integration_test.go:347`; no `FinishPublication`) — after phase 1 the row is `completed`, so backdating the lease alone matches nothing; the reset simulates crash-after-remote-success (owner value irrelevant — the claim predicate's delivering leg does not check it). `attempt` becomes 2 on re-settlement; only never-`dead_letter` is asserted. Next tick re-claims, re-POSTs; endpoint replies conflict receipt → row `completed` (never `dead_letter`), exactly 1 conflict reply, POST count == 2 then stable, `audit_ledger` count still 1 (re-append red) |
| **T-4.1** | concurrent claim partition (REQ-3.1) | `TestConcurrentClaimPublicationPartition` (internal/repository) | N=20 via `Append`, W=4 claim-loops with `limit=4` (< N) behind a start barrier (repo `:131` pattern): **multiplicity map** — every claimed id counted exactly once, `sum(map) == N` (set union is insufficient: it cannot see double claims); per-worker `claimed >= 1` (deterministic under barrier + `limit < N`); each row finished once by its claimer (`completed`, `attempt=1`); duplicate finish **post-phase** by another worker → `ErrAuditPublicationLeaseLost`; no `delivering` rows; workers never call `t.Fatalf` (errors via channel, assertions in the test goroutine); `-race` clean as hygiene only (§1.1.4) |
| **T-4.2** | e2e concurrent fan-out (REQ-3.2) | `TestFirstEventGovernanceE2EConcurrentBatch` (internal/app, same fixture) | N first facts, `WorkerConcurrency:2` → N rows `completed`; per-`event_id` exactly one accepted POST; total POSTs == N |

## 5. Migration steps (implementation order; each step keeps the tree green)

1. **Unit cells (no Docker):** extend the matrix in `internal/config/commercial_test.go` (6 cells, §1.1.1);
   run `make test` — `go test ./internal/config/...` must stay green.
2. **Env legs:** `TestLoadAuditGovernanceKnobEnvLegs` in `internal/config/config_test.go` (file-default YAML carries `audit_governance.purge_pause: false` / `dead_letter_retention: 0`, never the env values — §1.1.2); `make test`.
3. **Repository concurrency:** `TestConcurrentClaimPublicationPartition` beside E3, harness shape per
   §1.1.4 (multiplicity map, start barrier, `limit=4 < N`, per-worker `claimed >= 1`, finishes in-worker,
   fence probe post-phase, no `t.Fatalf` in workers, SKIP LOCKED behavior-pinning note); run with
   `go test -tags integration -race -run TestConcurrentClaimPublicationPartition ./internal/repository/`
   (Docker required).
4. **App fixture + T-10.2e:** `newAuditGovernanceFixture` + `TestFirstEventGovernanceE2E` phase 1 in
   `internal/app/audit_integration_test.go`; fixture order `buildAuditStack` → `Record` → `Start` matches
   `:314` (coverage gate passes on the fresh empty container); keep
   `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain` (`:314`) untouched. Integration run.
5. **T-10.2f:** phase 2 (explicit `status='delivering'` reset + DB-clock lease backdate + conflict receipt
   leg) in the same test.
6. **T-4.2:** `TestFirstEventGovernanceE2EConcurrentBatch` on the shared fixture (REQ-3.2).
7. **T-10.1.8:** `TestBuildAuditStackHonorsPurgeKnobs` (ticker-driven parity per §4: leg 1 paused with
   tick-progress proof via pending-row completion; leg 2 = second `buildAuditStack`/`Start` from a
   `PurgePause:false` config with boundary-value seeding — one past-retention + one inside-retention
   dead-letter row via `updated_at` backdating, assert only the past row purged with ledger survival).
8. **Done gate:** §6 commands, including `make test-integration` and `make check-integration-compile`.

No database migrations, no config changes, no wire changes — nothing to roll back. If any step's pin
exposes a production regression, the failing assertion names the contract in §1.2; fix forward in the owning
campaign, do not relax the pin.

## 6. Engineering gates

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow \
  && make check-anchored-event-ids && make check-integration-compile && make build
make test-integration   # required: T-10.2e/f, T-4.1, T-4.2, T-10.1.8 drive real Postgres + real HTTP
```

- All new tests carry the `integration` build tag (except the pure-unit matrix/env legs, which do not).
- `go test ./internal/config/...` measured green at verification time (requirements baseline); steps 1–2 must
  preserve it.
- File-size/cyclomatic gates are unaffected (no production source changes; test files are exempt from
  `check-file-sizes`, and helper functions stay under funlen 50 / cyclop 10).

## 7. Amendment register (relative to the evidence)

1. **Matrix entry points** (new, affects implementation): `validateAuditGovernance` is reachable from
   `validateRuntimeLimits` (`validate.go:130`), not from `validateCommercialIntegrations` — the T-10.1.7
   rejection cells therefore call `validateAuditGovernance` directly rather than the commercial gate
   (matches the evidence's citation of the CFG-2 contract).
2. **Anchor drifts under-reported by the evidence** (recorded, no design impact): SKIP LOCKED select at
   `audit_publication.go:55-56` (cited `:68-71`); env replacer at `config.go:385` (cited `:374-380`);
   knob fields at `audit_governance.go:23/:26` (cited `:24-28`); `validateAuditGovernance` body at
   `validate.go:211-215` (cited `:210-216`).
3. **Parity pin leg shape** (new detail): T-10.1.8 needs both the paused-preservation leg *and* an
   unpaused-positive-retention purge leg to distinguish knob plumbing from the default `0 = never purge`
   no-op (the service-level precedent `TestPurgeScanSkipsWhenGovernanceDisabled` already proves the scan
   semantics; the new leg proves the `buildAuditStack` copy).
4. **T-4.1 harness shape** (new, mandatory after concurrency + test-design review): overlap is asserted by
   `map[id]int` multiplicity (`len == N`, every entry == 1, `sum == N`), never set union; start barrier +
   claim `limit < N` (4 with N=20) + deterministic per-worker `claimed >= 1`; workers never call
   `t.Fatalf` (error channel; assertions after `wg.Wait()`); finishes in-worker, duplicate-finish fence
   probe post-phase; `-race` documented as hygiene, not mechanism; SKIP LOCKED deliberately
   behavior-pinned — plain `FOR UPDATE` still partitions (regression surfaces as head-of-line blocking),
   a clause-presence pin would be flaky.
5. **T-10.1.8 leg-2 shape** (new, mandatory): boundary-value seeding — two dead-letter rows via
   `updated_at` backdating (`now()-10 days` past / `now()-10 min` inside retention), assert exactly the
   past row purged (catches a hardcoded-positive retention that both single-row legs green-light); leg-1
   tick-progress proof via pending-row completion, never the purge metric (0 while paused) and no sleeps;
   leg 2 runs from a **second `buildAuditStack`/`Start` with `PurgePause:false`**, never `cfg` mutation
   (which would bypass the `app/audit.go:52-53` copy and silently degrade the pin to a re-run of the
   service-level test).
6. **T-10.2e pin target** (new, mandatory): the body's `event_id == idempotency_key` must equal the
   **ledger row id** (`SELECT id FROM audit_ledger WHERE event_id = <known>`), not the caller-known
   `EventID` — the known `EventID` lives only in the dedup column and is never the payload
   (`publisher.go:204/:211`; service precedent `callback_integration_test.go:195`); pinning the wrong
   target is red under current production and invites dropping the assertion.
7. **Env-leg YAML precondition** (new, mandatory): the file-default YAML must carry
   `audit_governance.purge_pause: false` / `dead_letter_retention: 0` and never the env values — viper
   `AutomaticEnv` is lazy (T-5.1 comment, `config_test.go:212`); a file carrying the env values is the
   one wrong-reason pass.
8. **T-10.2f phase-2 reset** (new, mandatory): after phase 1 the row is `completed`; the replay leg must
   explicitly `UPDATE … SET status='delivering', lease_until = now() - interval '1 minute'` — backdating
   the lease alone matches nothing. `attempt` becomes 2; only never-`dead_letter` is asserted.
9. **Keep-enforced completion + anchor fixes** (new): §2 now lists T-5.1
   (`TestDataRetentionPurgePausedValidation`; working tree `config_test.go:212`, requirements cite `:208`
   — pre-`TestLoadAuditGovernanceTokenURLGate` numbering) and the two E3 siblings
   `TestAuditPublicationDrainCycleSingleDelivery` (`repo:174`) / `TestDeadLetterPublicationNeverReclaimed`
   (`repo:237`); `TestConcurrentPurgeAndRequeueRace` (`audit_publication_replay_integration_test.go:328`)
   recorded as the separate partition precedent; §1.1.3 coverage-gate justification corrected — the gate
   passes on the fresh empty container before any `Record` (zero tenants in the UNION), fixture order
   `buildAuditStack` → `Record` → `Start` matches `:314`.
