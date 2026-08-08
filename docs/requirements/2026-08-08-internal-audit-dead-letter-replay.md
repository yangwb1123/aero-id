# Requirements — module `internal/audit`: audit publication dead-letter replay (`RequeuePublication` + `cmd/audit-replay` + purge-pause flag)

| | |
|---|---|
| Module (batch label) | `internal/audit` — the module under analysis is the **unwired stub tree** (`internal/audit/repository.go`, 69 L: `Repository` with `Append`/`Log`, random `uuid.New` IDs, no outbox; `repository_test.go` 64 L). This direction adds **nothing** to that stub (T-10.1); the stub's exclude-or-delete decision is sibling direction #3 (B2-6), out of scope here. The direction's change surface is `internal/repository` + `cmd/audit-replay` (+ verification in `internal/service`/`internal/app`/`scripts`) |
| Source analysis | `docs/auto/analyses/internal-audit-9b2d0397.json` (direction 1 of 3: B2-5 — audit publication dead-letter replay) |
| Campaign | `aero-id-b2-5-dead-letter-replay` (gate G3: T-4). Sibling specs for the same direction under other labels: `2026-08-08-internal-app-dead-letter-replay.md`, `2026-08-08-cmd-server-dead-letter-replay.md`, `2026-08-07-internal-connector-aeroim-b2-5-dead-letter-replay.md` (T-4.1/T-4.1(b)/T-4.2/T-4.3), `2026-08-06-dead-letter-replay-configs-requirements.md`, `2026-08-07-outbox-purge-pause-config-requirements.md` |
| Design | `docs/plans/2026-08-07-dead-letter-replay-requeuepublication-design.md` (rev 2, status line "pending implementation" — **stale**: landed); verified-design reconciliation: `docs/plans/2026-08-08-cmd-server-dead-letter-replay-verified-design.md`; regression/race design: `docs/plans/2026-08-08-deterministic-replay-regression-tests-design.md`; `docs/plans/2026-08-08-dead-letter-replay-remediation-plan.md` |
| Date | 2026-08-08 |
| Status | **Evidence re-verified against head `cd65f57` + the uncommitted working tree.** Every "absent" claim in the direction's evidence (zero `Requeue*` symbols; `cmd/` = migrate+server only; no `purge_pause` symbol) is **false in the current tree** — the implementation and its tests have landed uncommitted and evolved past the sibling specs (intent-based, tenant-scoped per-tenant facts; in-tx binding enforcement; `-actor` mandatory). Every supplied acceptance check (T-4 a–e, T-4 joint, T-10.1) maps to a landed, runnable test; all were executed green in this pass (§7). Residual delta is documentation-only (§3.3) |

## 1. Problem statement (direction, preserved)

`dead_letter` is a terminal audit-publication state with zero recovery surface: `ClaimPublication` excludes `dead_letter` rows and `publicationOutcome` settles them terminally, so every permanently failing governance fact (endpoint rejection after `MaxAttempts`, key misconfig) is unrecoverable compliance evidence without manual SQL. The direction proposed `RequeuePublication` + `cmd/audit-replay` + a purge-pause flag, with requeue idempotency guaranteed by the unique fact constraint, and an in-tx `audit.publication.requeued` fact with a fresh anchored `event_id` (C2 allowlist edit).

**Current-state correction (verified, §2):** the recovery path now exists in the working tree, wired through the composition root and pinned by tests: `RequeuePublication` (`internal/repository/audit_publication_replay.go:40`) and `RequeueDead` (`internal/repository/callback_audit.go:119`) move `dead_letter` rows back to `pending` with `attempt=0`, cleared lease, `error_code='manual_replay'`, one audit fact per distinct affected tenant in the same transaction; `cmd/audit-replay` (`cmd/audit-replay/main.go`, 357 L) drives both legs from the operator shell; the drain-loop purge scan is pause-gated (`audit_governance.purge_pause` + `dead_letter_retention`). The requirements below preserve the supplied acceptance checks and pin their landed tests.

## 2. Verified evidence (head `cd65f57` + working tree; line numbers live)

### 2.1 Direction-evidence claims (re-checked)

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `grep -rn 'Requeue' internal/ cmd/ scripts/` yields **0 hits** | ❌ **false today.** `RequeuePublication` (`audit_publication_replay.go:40`, iface `internal/repository/audit_ledger.go`), `RequeueDead` (`callback_audit.go:119`, iface `internal/repository/callback.go`), `CountDeadLetterPublications`/`DeadLetterTenants`/`PurgeDeadLetterPublications` (`audit_publication_replay.go`), `CountDeadLetterDeliveries`/`DeadLetterTenants`/`PurgeDeadLetterDeliveries` (`callback_audit.go:241/:256/:281`). The only `scripts/` hits are the **check-audit-swallow self-test fixtures** (`scripts/testdata/audit-swallow/positive/fixture.go:13-14`, `cmd_positive/cmd/fixture.go:9-10`) — intentional; P1/P2 patterns at `scripts/check-audit-swallow.sh:60-61` |
| E2 | `cmd/` contains only `migrate` and `server` | ❌ **false today.** `cmd/audit-replay/` — `main.go` (357 L) + three integration-tagged test files (`main_test.go` 439 L, `backlog_and_partial_test.go` 327 L, `i7_boot_hazard_test.go` 241 L). `Makefile:9-12` `build-audit-replay` chained into `make build`; `bin/audit-replay` built in this pass |
| E3 | No `purge_pause` symbol in code, config, or SQL | ❌ **false today.** `audit_governance.purge_pause` + `dead_letter_retention` (`configs/config.yaml:188-189`), `storage.gc.purge_paused` (`:229`), `data_retention.purge_paused` (`:241`); struct fields `internal/config/config.go:308/:381`; validation `internal/config/validate.go:165-166/:272-276`; drain-loop guard `internal/service/callback_worker.go:65`; composition copy `internal/app/audit.go:52` |
| E4 | `ClaimPublication` predicate excludes `dead_letter`; lease/dead_letter handling at `:85/:101` | ✅ holds: predicate `((status = 'pending' AND next_attempt_at <= now()) OR (status = 'delivering' AND lease_until < now()))` at `audit_publication.go:56`; lease/dead_letter notes at `:85/:101`; lease-guarded `FinishPublication` (`id = ? AND status = 'delivering' AND lease_owner = ?`) `:111-120` |
| E5 | `publicationOutcome` settles `dead_letter` (cited `callback_audit.go:98`) | ✅ holds with drift: `publicationOutcome` `callback_audit.go:144-157`; terminal `return "dead_letter", "audit.publication_failed", ...` at `:152` when `attempt >= maxAttempts` |
| E6 | `:45 drainPublications` sole drain; `callback.go:286 Start` ticker | ✅ `drainPublications` at `callback_audit.go:45` (exact); **drift**: `Start` now lives in `internal/service/callback_worker.go:34-52`, per-tick `drainPublications` → `drain` → `purgeDeadLetters` at `:47-49`. Single `publisher.Publish(ctx, fact)` site: `callback_audit.go:179` |
| E7 | Design + requirements docs exist | ✅ `docs/plans/2026-08-07-dead-letter-replay-requeuepublication-design.md` (rev 2); verified-design reconciliation `docs/plans/2026-08-08-cmd-server-dead-letter-replay-verified-design.md`; `docs/requirements/2026-08-07-internal-connector-aeroim-b2-5-dead-letter-replay.md` §8 (T-4.1, T-4.1(b), T-4.2, T-4.3) |
| E8 | In-tx precedents: `audit_hold.go`; `audit_ledger.go:172-208` (ledger + publication row in one tx, 23505 mapping) | ✅ with drift: `createAuditRows` now `audit_ledger.go:195-227` — ledger INSERT + `status='pending'` publication INSERT in one tx (`:223`); constraint-precise 23505 → `ErrAuditDuplicate` mapping (`:219-220`, only `uq_audit_ledger_tenant_event`; other 23505s stay raw). `audit_hold.go:127-141` `PutHoldWithAudit` appends via `AppendInTx` in-tx |
| E9 | `validateAuditBindingCoverage` covers `dead_letter` (cited `app/audit.go:161`) | ✅ drift +4: `internal/app/audit.go:165`; `status IN ('pending','delivering','dead_letter')` at `:173` |
| E10 | Proposed: requeue appends `audit.publication.requeued` fact, fresh anchored `event_id` in the same tx; needs C2 allowlist edit | ✅ **LANDED**: action/resource pair `{"audit.publication.requeued","audit_publication"}` in `internal/service/audit_replay.go:14-18`; repository mints a fresh v7 `EventID` per (run, leg, tenant) inside the requeue tx (`appendRequeueFacts`, `audit_publication_replay.go`) — the C2 doctrine comment is at that site; C2 allowlist edit landed at `scripts/check-anchored-event-ids.sh:30-31` (`internal/repository/audit_publication_replay.go|internal/repository/callback_audit.go`) |

### 2.2 Module `internal/audit` (module under analysis)

- The stub (`internal/audit/repository.go`, 69 L: `Append`/`Log`, random `uuid.New`, no outbox; `repository_test.go` 64 L) is the unwired dead tree AGENTS.md forbids for new audit work. **This direction adds no code to it** — verified: no `Requeue*`, no `.Publish`, no new write path in `internal/audit/` (grep in this pass).
- The stub stays out of the done-gate allowlists exactly as before; extending or deleting it is direction #3's (B2-6) scope decision, explicitly out of scope here.

## 3. Current state and residual delta

### 3.1 Landed in the working tree (keep-enforced; do not re-do)

- **Requeue core** (`internal/repository/audit_publication_replay.go`, 455 L — under the 500-line gate, T-4(e)): status-scoped (`dead_letter` only), bounded (`-id` exact set overrides `-limit`; else oldest `limit` by `(next_attempt_at, id)`, default 100); selection locked `FOR UPDATE` (two-statement shape — the locked statement joins nothing, so ledger rows are never locked); reset UPDATE (`status='pending'`, `attempt=0`, `next_attempt_at=now` app clock, lease cleared, `error_code='manual_replay'`) and one fact per **distinct affected tenant** commit in one tx; `RowsAffected == len(selection)` asserted in-tx; zero rows ⇒ no fact. Fail-closed sentinels `ErrAuditFactRequired`, `ErrAuditReplayUnattributableTenant`, `ErrAuditReplayTenantNotBound`.
- **CLI** (`cmd/audit-replay`): operator recovery surface; `-actor` **mandatory in mutating mode** (fail-closed, no un-audited default); `-dry-run` prints bounded count + affected tenants, mutates nothing; re-runs print `requeued 0` and exit 0; one fresh v7 `OperationID` per run shared across legs/tenants; optional fsynced `-report` journal; signing-key preflight before any DB access.
- **Purge pause**: `audit_governance.purge_pause` + `dead_letter_retention` gate the drain-loop scan (`purgeDeadLetters`, `callback_worker.go:58-80`; paused ticks do no SELECT and no DELETE); `PurgeDeadLetterPublications`/`PurgeDeadLetterDeliveries` exclude leased rows at the SQL boundary and use `FOR UPDATE SKIP LOCKED` in the subquery with the outer status qual re-checked by EPQ.
- **Binding enforcement**: in-tx `checkReplayTenants` on the actual locked selection (empty tenant ⇒ rollback; non-'legacy' tenant outside `AllowedTenants` ⇒ rollback); CLI preflight `replayTenantPreflight` is friendly-only; `allowedTenants` derives from `audit_governance.bindings` keys when governance is enabled.
- **Tests** (all executed green, §7): 18 repository replay/purge/EPQ integration tests; 14 CLI integration tests; `TestRequeuedPublicationDrainsExactlyOnce`; `TestDrainPublicationsOnlyFromStart`; 7 app-level governance e2e/wiring tests.

### 3.2 Implementation divergences from the supplied acceptance (recorded, pinned by tests)

| Acceptance wording | Landed implementation | Pinned by |
|---|---|---|
| "idempotent via `uq_audit_publication_fact`" | Idempotency is realized by the **`dead_letter` status predicate** (second run selects 0 rows ⇒ 0 facts); `uq_audit_publication_fact UNIQUE (fact_id)` keeps exactly one publication row per ledger fact (`migrations/000045_audit_publications.up.sql:12`); fresh v7 EventIDs per (run, leg, tenant) keep the dedup key `(tenant_id, source, event_id)` collision-free; `ErrAuditDuplicate` folds in-tx as defense-only | `TestRequeuePublicationResetsDeadLetterRows` (second call → 0), `TestRequeuePublicationSecondRequeueMintsDistinctFact` |
| "`callback.go:286 Start` ticker unchanged" | `Start`/ticker moved to `internal/service/callback_worker.go:34-52`; the invariant that matters — `drainPublications` fires only from the ticker — is pinned statically and dynamically | `TestDrainPublicationsOnlyFromStart`; greps §REQ-6 |
| "conflict receipt honored" (`ErrAuditDuplicate`/conflict) | Conflict receipt idempotency is publisher-level, landed and tested (`TestPublisherConflictReceiptIsIdempotentSuccess`, `internal/connector/auditgovernance/publisher_test.go:102`); `ErrAuditDuplicate` fold is repo-level in-tx | `TestRequeuePublicationFactAppendsInTx`, publisher test |
| Acceptance (d) "purge pause flag blocks dead_letter purge while a replay worker holds the lease (FOR UPDATE locked-selection race test)" | Landed as two mechanisms: (1) the pause flag gates the scan tick (`PurgePause` guard before any SELECT/DELETE); (2) purge's SQL excludes leased rows and SKIP-LOCKED skips rows locked by an in-flight requeue, with EPQ re-check saving concurrently requeued rows | `TestBuildAuditStackHonorsPurgeKnobs`, `TestPurgeScanSkipsWhenGovernanceDisabled`, `TestConcurrentPurgeAndRequeueRace`, `TestPurgeDeleteBlockedByRequeueCommitEPQ` (+DeliveryLeg), `TestRequeueCrashMidTxRollsBackByteIdentical` |

### 3.3 Residual delta (verification/documentation only — no production code)

1. **Design-doc status line** (`docs/plans/2026-08-07-dead-letter-replay-requeuepublication-design.md`): "pending implementation" is stale; the §5 baseline greps no longer hold. Update the status block to the landed state and point at §3.2 (same action as the sibling specs).
2. **Full `make test-integration`**: required by AGENTS.md after repository SQL work. This pass ran the direction-relevant integration suites individually (all green, §7) with Docker/testpg; the full suite should be run once in the implement/verify stage.
3. **Sibling-spec supersession**: the `internal/app`/`cmd/server` sibling specs (§3.2) describe an earlier working-tree snapshot — fact mint has since moved from `cmd/audit-replay/main.go` into the repository (`appendRequeueFacts`), `DeadLetterTenants` + in-tx binding enforcement landed (they were recorded "not landed"), and `-actor` became mandatory. This spec is verified against the current tree and supersedes those rows.

## 4. Requirements (supplied acceptance checks preserved; testable criteria + landed pins)

### REQ-1 — T-4(a): RequeuePublication moves `dead_letter` rows to `pending` with attempt reset and `error_code='manual_replay'`; idempotent; new file under 500 lines (LANDED)

> "T-4(a) RequeuePublication moves dead_letter rows to pending with attempt reset and error_code='manual_replay', idempotent via uq_audit_publication_fact (go test -tags=integration; new internal/repository/audit_publication_replay.go under 500 lines per T-4(e))"

Testable criteria (integration-tagged, real Postgres via testpg):

1. Drive a fact to `dead_letter` through the public surface only (`Record`/`AppendInTx` → `ClaimPublication` → `FinishPublication(..., "dead_letter", "audit.publication_failed", ...)`); then `RequeuePublication(ctx, nil, limit, intent)` ⇒ row `status='pending'`, `attempt=0`, `lease_owner=''`, `lease_until=NULL`, `error_code='manual_replay'`, `next_attempt_at` ≈ now: **`TestRequeuePublicationResetsDeadLetterRows`** (`audit_publication_replay_integration_test.go:138`).
2. Idempotency: second call over the same rows returns **0** affected and **0** facts; exactly one publication row per `fact_id` before and after; rows in `pending`/`delivering`/`completed` untouched (a completed row stays `completed/1`): same test + **`TestRequeuePublicationSecondRequeueMintsDistinctFact`** (`:476`).
3. In-tx fact: one `audit.publication.requeued` ledger row per distinct affected tenant, same tx as the reset (assert `count(*)` in `audit_ledger` and rollback on injected failure): **`TestRequeuePublicationFactAppendsInTx`** (`:238`), **`TestRequeuePublicationPerTenantFacts`** (`:313`), **`TestRequeuePublicationEmptyTenantFailsClosed`** (`:367`), **`TestRequeuePublicationUnboundTenantRollsBack`** (`:414`).
4. Lease invariant: a `delivering` row with a live lease is unreachable by requeue (status predicate) and by purge: **`TestRequeuePublicationLeaseInvariant`** (`:648`).
5. File-size gate: `internal/repository/audit_publication_replay.go` = **455 lines** (< 500): `make check-file-sizes` green.

### REQ-2 — T-4(b): after requeue, `drainPublications` delivers to the fake governance endpoint and `FinishPublication` completes (LANDED)

> "T-4(b) after requeue, drainPublications delivers to the fake governance endpoint and FinishPublication completes (callback_integration_test.go precedent)"

Testable criteria (integration):

1. Seed one publication; drive to `dead_letter`; `RequeuePublication` with the intent surface; then one `drainPublications` cycle against a service wired with a governance publisher ⇒ the original fact POSTs to the governance endpoint **exactly once** (per-event_id counter = 1), the requeue fact (`audit.publication.requeued`) also drains exactly once, the row settles `completed`/`''` with lease cleared, `ObserveAuditPublicationOutcome("completed")` observed, and a second drain is a no-op: **`TestRequeuedPublicationDrainsExactlyOnce`** (`internal/service/callback_integration_test.go:878`).
2. Composition-root e2e: first fact through the full stack delivers exactly once and completes: **`TestFirstEventGovernanceE2E`** (`internal/app/audit_integration_test.go:563`) + **`TestFirstEventGovernanceE2EConcurrentBatch`** (`:648`).

### REQ-3 — T-4(c): `cmd/audit-replay` run twice publishes the fact exactly once; exit 0 for 0 rows; `-dry-run` mints no fact (LANDED)

> "T-4(c) cmd/audit-replay run twice publishes the fact exactly once (ErrAuditDuplicate/conflict receipt honored), exit 0 for 0 rows, -dry-run mints no fact"

Testable criteria (integration, `cmd/audit-replay`):

1. Mutating run with `-actor` ⇒ `requeued N`, exit 0; identical re-run ⇒ `requeued 0`, exit 0, no new fact (`audit.publication.requeued` count unchanged), dry-run count == seeded == requeued: **`TestAuditReplayCLIRunTwiceIsIdempotent`** (`main_test.go:137`), **`TestAuditReplayCLIRequeueFactWithTenant`** (`:248`).
2. Empty backlog ⇒ prints zero, exit 0, mints nothing: **`TestAuditReplayEmptyBacklogPrintsZeroWithoutFact`** (`backlog_and_partial_test.go:276`).
3. `-dry-run` mints no fact and mutates nothing: **`TestAuditReplayDryRunCountIsBounded`** (`:22`); bounded at `-limit`: **`TestAuditReplayCLIBacklogExceedsLimit`** (`:51`).
4. `ErrAuditDuplicate` path: duplicate append folds in-tx, requeue still commits, exactly 1 ledger row: **`TestRequeuePublicationFactAppendsInTx`**; conflict-receipt idempotency at the publisher: **`TestPublisherConflictReceiptIsIdempotentSuccess`** (`internal/connector/auditgovernance/publisher_test.go:102`).
5. Fail-closed CLI: mutating run without `-actor` errors (exit 1); missing signing key errors before any DB mutation: **`TestAuditReplayDefaultInvocationRequiresActor`** (`i7_boot_hazard_test.go:96`), **`TestAuditReplaySigningKeyPreFlight`** (`main_test.go:337`), **`TestAuditReplayDBOutageFailsClosed`** (`:392`).

### REQ-4 — T-4(d): purge-pause flag blocks `dead_letter` purge while a replay worker holds the lease (LANDED)

> "T-4(d) purge pause flag blocks dead_letter purge while a replay worker holds the lease (FOR UPDATE locked-selection race test)"

Testable criteria:

1. Pause gate: with `PurgePause=true` (or `DeadLetterRetention=0`), the drain-loop tick performs **no SELECT and no DELETE** — a past-retention `dead_letter` row is bit-for-bit identical after multiple ticks: **`TestBuildAuditStackHonorsPurgeKnobs`** (`internal/app/audit_integration_test.go:743`), **`TestPurgeScanSkipsWhenGovernanceDisabled`** (`internal/service/callback_integration_test.go`, drain-loop leg); guard at `callback_worker.go:65`.
2. Lease exclusion: the purge predicate excludes `lease_owner != ''` and unexpired `lease_until` at the SQL boundary; `FOR UPDATE SKIP LOCKED` makes purge skip rows locked by an in-flight requeue tx instead of blocking; the EPQ re-check keeps a concurrently requeued row alive: **`TestConcurrentPurgeAndRequeueRace`** (`audit_publication_replay_integration_test.go:566`), **`TestPurgeDeleteBlockedByRequeueCommitEPQ`** (`audit_publication_epq_integration_test.go:32`), **`TestPurgeDeleteBlockedByRequeueCommitEPQDeliveryLeg`** (`:127`), **`TestRequeueVsPurgeInterleaving`**, **`TestRequeueCrashMidTxRollsBackByteIdentical`** (`:226`).
3. Boundary purge (pause cleared): exactly the past-retention row purges, in-retention rows survive; the `audit_ledger` fact always survives (FK `ON DELETE CASCADE` on the publication row only): **`TestBuildAuditStackHonorsPurgeKnobs`** leg 2, **`TestPurgeDeadLetterPublicationsPredicate`** (`audit_publication_replay_integration_test.go`).

### REQ-5 — T-4 joint: requeue fact `event_id` passes `check-anchored-event-ids` after the C2 allowlist edit (LANDED)

> "T-4 joint: requeue fact event_id passes check-anchored-event-ids after C2 allowlist edit"

Testable criteria:

1. Static gate: `make check-anchored-event-ids` green with the C2 allowlist carrying `internal/repository/audit_publication_replay.go` and `internal/repository/callback_audit.go` (`scripts/check-anchored-event-ids.sh:30-31`); the R-1 self-test still flags the positive fixtures. **Ran green in this pass** (§7).
2. Behavioral pin: two distinct requeue runs over the same row mint **distinct** ledger facts with fresh v7 EventIDs (the dedup key can never collide across re-runs): **`TestRequeuePublicationSecondRequeueMintsDistinctFact`** (`:476`).

### REQ-6 — T-10.1: `drainPublications` remains the only audit delivery path (LANDED)

> "T-10.1: grep proves drainPublications remains the only audit delivery path (callback.go:286 Start ticker unchanged, no new .Publish in cmd/)"

Testable criteria:

1. Static greps (this pass): `drainPublications(` in `internal/` non-test = definition (`callback_audit.go:45`) + ticker call (`callback_worker.go:47`) only; `publisher.Publish(ctx, fact)` in `internal/`+`cmd/` non-test = `callback_audit.go:179` only; **no `.Publish(` in `cmd/`** (the CLI never delivers; the server drain re-claims requeued rows).
2. Dynamic pin: with a recording publication repo wired, `ClaimPublication` fires from the `Start` ticker and from **no other service entry point** (`Register`/`List`/`Delete`/`ReplayDelivery` leave the claim counter unchanged): **`TestDrainPublicationsOnlyFromStart`** (`internal/service/drain_entry_test.go:54`).
3. Mechanical gate: `check-audit-swallow` P1/P2 patterns include `ClaimPublication`/`FinishPublication`/`RequeuePublication`/`RequeueDead` (`scripts/check-audit-swallow.sh:60-61`), with `cmd` a scan root (`Makefile:69`). **Ran green in this pass.**

## 5. Compatibility constraints (module `internal/audit`-relevant)

- No change to the unwired `internal/audit` stub — this direction adds no code to it and no new audit write path anywhere except the C2 allow-listed requeue mint sites.
- No migration change: requeue is a pure UPDATE on existing rows; `uq_audit_publication_fact` (000045) is reused as-is.
- Delivery stays server-side: the CLI requeues; it never publishes (REQ-6). No HTTP/gRPC/proto surface changes.
- Live APIs keep existing behavior (AGENTS.md); the replay surface is additive (new repository methods + new binary).

## 6. Failure modes (closed by landed code; pinned by tests)

| Failure | Closure | Pin |
|---|---|---|
| Requeue without operator intent (un-audited commit) | `ErrAuditFactRequired` before any DB access; `-actor` mandatory | `TestRequeuePublicationEmptyTenantFailsClosed` (repo), `TestAuditReplayDefaultInvocationRequiresActor` (CLI) |
| Rows without a tenant / unbound tenant | Whole batch rolls back in-tx (`ErrAuditReplayUnattributableTenant`/`ErrAuditReplayTenantNotBound`); friendly preflight first | `TestRequeuePublicationEmptyTenantFailsClosed`, `TestRequeuePublicationUnboundTenantRollsBack`, `TestAuditReplayPreflightRefusesUnboundTenant`, `TestAuditReplayUnboundTenantRowsRefused` |
| Crash mid-requeue | Atomic tx: byte-identical rollback, no half-reset, no orphan fact | `TestRequeueCrashMidTxRollsBackByteIdentical` |
| Replay racing purge | Lease-excluding purge predicate + SKIP LOCKED + EPQ re-check; pause flag | §REQ-4 pins |
| Worker crash after remote publish | Lease re-claim; `attempt` untouched at claim time (M-4) | `TestRequeuePublicationLeaseInvariant`, `TestDeadLetterPublicationNeverReclaimed` |
| Partial `-kind both` run | `-report` journal records committed legs; re-run completes the other leg idempotently | `TestAuditReplayLegResumeAfterPartial`, `TestAuditReplayCommitSurvivesMissingReport` |
| Governance disabled / unwired outbox | `drainPublications` drain-skipped counter; wiring fails closed at composition | `TestWireAuditServicesFailClosedOnMissingSetters`, `TestPurgeScanSkipsWhenGovernanceDisabled` |

## 7. Done-gate evidence (re-run on this tree, 2026-08-08)

```text
go build ./...                    → ok
make build                        → bin/aero-id + bin/audit-replay built
make lint                         → go vet ./... clean
make lint-strict                  → golangci-lint run ./... clean
make check-file-sizes             → "All files ≤ 500 lines" (audit_publication_replay.go 455 L)
make check-audit-swallow          → ✓ (roots include cmd; self-test ok)
make check-anchored-event-ids     → ✓ (C2 allowlist ok, self-test ok)
make check-integration-compile    → ok (integration-tagged tests compile; go vet -tags integration clean)
go test ./... -count=1            → all packages ok (unit)
go test -tags=integration ./internal/repository/ -run 'Requeue|Purge|EPQ|DeadLetter'
                                  → 18/18 PASS (incl. all §REQ-1/REQ-4 pins)
go test -tags=integration ./cmd/audit-replay/...  → 14/14 PASS (all §REQ-3 pins)
go test -tags=integration ./internal/service/ -run 'TestRequeuedPublicationDrainsExactlyOnce|TestPurgeScanSkipsWhenGovernanceDisabled'
                                  → PASS (REQ-2/REQ-4)
go test -tags=integration ./internal/app/ -run 'TestBuildAuditStackHonorsPurgeKnobs|TestFirstEventGovernanceE2E|TestWireAuditServicesFailClosed|TestAuditBindingCoverage'
                                  → 7/7 PASS
go test ./internal/service/ -run TestDrainPublicationsOnlyFromStart → PASS (REQ-6)
```

Full `make test-integration` was not run in this pass; the direction-relevant integration suites were run individually with Docker/testpg (all green). AGENTS.md requires one full run after repository SQL work in the implement/verify stage.

## 8. Testable acceptance mapping (supplied checks preserved verbatim)

| Supplied acceptance (direction) | Status | Testable criteria (landed pins) |
|---|---|---|
| **T-4(a)** "RequeuePublication moves dead_letter rows to pending with attempt reset and error_code='manual_replay', idempotent via uq_audit_publication_fact (go test -tags=integration; new internal/repository/audit_publication_replay.go under 500 lines per T-4(e))" | **LANDED** | `TestRequeuePublicationResetsDeadLetterRows` (`audit_publication_replay_integration_test.go:138`), `TestRequeuePublicationSecondRequeueMintsDistinctFact` (`:476`), `TestRequeuePublicationLeaseInvariant` (`:648`); file 455 L — REQ-1 |
| **T-4(b)** "after requeue, drainPublications delivers to the fake governance endpoint and FinishPublication completes (callback_integration_test.go precedent)" | **LANDED** | `TestRequeuedPublicationDrainsExactlyOnce` (`internal/service/callback_integration_test.go:878`), `TestFirstEventGovernanceE2E` (+ConcurrentBatch) — REQ-2 |
| **T-4(c)** "cmd/audit-replay run twice publishes the fact exactly once (ErrAuditDuplicate/conflict receipt honored), exit 0 for 0 rows, -dry-run mints no fact" | **LANDED** | `TestAuditReplayCLIRunTwiceIsIdempotent` (`main_test.go:137`), `TestAuditReplayEmptyBacklogPrintsZeroWithoutFact`, `TestAuditReplayDryRunCountIsBounded`, `TestRequeuePublicationFactAppendsInTx`, `TestPublisherConflictReceiptIsIdempotentSuccess` — REQ-3 |
| **T-4(d)** "purge pause flag blocks dead_letter purge while a replay worker holds the lease (FOR UPDATE locked-selection race test)" | **LANDED** | `TestBuildAuditStackHonorsPurgeKnobs` (`internal/app/audit_integration_test.go:743`), `TestPurgeScanSkipsWhenGovernanceDisabled`, `TestConcurrentPurgeAndRequeueRace` (`:566`), `TestPurgeDeleteBlockedByRequeueCommitEPQ` (+DeliveryLeg), `TestRequeueCrashMidTxRollsBackByteIdentical` — REQ-4 |
| **T-4(e)** "new internal/repository/audit_publication_replay.go under 500 lines" | **LANDED** | 455 lines; `make check-file-sizes` green — REQ-1.5 |
| **T-4 joint** "requeue fact event_id passes check-anchored-event-ids after C2 allowlist edit" | **LANDED** | C2 allowlist at `check-anchored-event-ids.sh:30-31`; gate green; `TestRequeuePublicationSecondRequeueMintsDistinctFact` — REQ-5 |
| **T-10.1** "grep proves drainPublications remains the only audit delivery path (callback.go:286 Start ticker unchanged, no new .Publish in cmd/)" | **LANDED** (ticker moved to `callback_worker.go:34`; invariant intact) | `TestDrainPublicationsOnlyFromStart` (`drain_entry_test.go:54`); greps: `drainPublications` = def `callback_audit.go:45` + ticker `callback_worker.go:47`; single `Publish` site `callback_audit.go:179`; no `.Publish` in `cmd/` — REQ-6 |

## 9. Amendment register (stale wording in the direction / source analysis)

| # | Stale claim | Correction | Folded into |
|---|---|---|---|
| A1 | "grep for Requeue* across internal/, cmd/, scripts/ yields 0 hits" | False in the working tree: replay surface landed in `internal/repository` (`audit_publication_replay.go`, `callback_audit.go`) and `cmd/audit-replay`; `scripts/` hits are check-audit-swallow fixtures only | §2.1 E1, §3.1 |
| A2 | "cmd/ contains only migrate and server" | False: `cmd/audit-replay/` (main.go 357 L + 3 integration test files); `make build` emits `bin/audit-replay` | §2.1 E2 |
| A3 | "no purge_pause symbol exists in code, config, or SQL" | False: config keys + validation + drain-loop guard + composition copy all landed | §2.1 E3, REQ-4 |
| A4 | "callback.go:286 Start ticker" | `Start`/ticker moved to `internal/service/callback_worker.go:34-52`; `drainPublications` at `:47` | §2.1 E6, REQ-6 |
| A5 | "callback_audit.go:98 publicationOutcome settles dead_letter" | Holds with drift: `:144-157`, terminal `dead_letter` at `:152` | §2.1 E5 |
| A6 | "audit_ledger.go:172-208 (ledger+publication row in one tx, 23505 mapping)" | Holds with drift: `createAuditRows` `:195-227`; publication insert `:223`; 23505 mapping `:219-220` | §2.1 E8 |
| A7 | "app/audit.go:161 validateAuditBindingCoverage" | Holds with drift: `:165`; `dead_letter` in `status IN (...)` at `:173` | §2.1 E9 |
| A8 | "requeue idempotency … guaranteed by the unique fact constraint" | Mechanism drift: idempotency is the `dead_letter` status predicate; `uq_audit_publication_fact` pins one publication row per fact; fresh v7 EventIDs keep the ledger dedup key collision-free; `ErrAuditDuplicate` folds in-tx | §3.2, REQ-1 |
| A9 | Sibling-spec snapshot rows (fact mint at `cmd/audit-replay/main.go`, `-actor` default, "no binding pre-flight") | Superseded by the current tree: mint in-repo per (run, leg, tenant); `-actor` mandatory; `DeadLetterTenants` + in-tx `checkReplayTenants` landed | §3.3.3 |

---

### Done-gate relevance

No production code remains for this direction in this tree; residual delta is the design-doc status update (§3.3.1) and one full `make test-integration` run (§3.3.2). The implement/verify stages must keep the §7 gate set green — all direction-relevant integration suites were executed green in this pass with Docker/testpg.
