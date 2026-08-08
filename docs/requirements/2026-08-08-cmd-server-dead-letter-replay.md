# Requirements — (module label `cmd/server`): dead-letter replay for audit publications and callback deliveries — `RequeuePublication`/`RequeueDead`, `cmd/audit-replay`, purge-pause flags

| | |
|---|---|
| Module (batch label) | `cmd/server` — **label only**. The direction's change surface is `internal/repository` + `cmd/audit-replay` (+ verification in `internal/service`/`internal/app`). `cmd/server/main.go` itself is touched only incidentally (fail-closed config-load exit, already present in the working tree); this direction adds a **sibling** binary, not a `cmd/server` feature |
| Source analysis | `docs/auto/analyses/cmd-server-7a3bfea7.json` (direction 2 of 3: B2-5 dead-letter replay) |
| Campaign | `aero-id-b2-5-dead-letter-replay`. Sibling specs for the same direction under other labels: `2026-08-07-internal-connector-aeroim-b2-5-dead-letter-replay.md` (audit-publication core), `2026-08-06-dead-letter-replay-configs-requirements.md` (configs: purge scan, `audit_governance.purge_pause`, metrics), `2026-08-07-outbox-purge-pause-config-requirements.md` (`data_retention.purge_paused`) |
| Design | `docs/plans/2026-08-07-dead-letter-replay-requeuepublication-design.md` (Status line: "Design rev 2, pending implementation" — **stale**: the working tree implements the direction with documented divergences, §3.2) |
| Date | 2026-08-08 |
| Status | **Evidence re-verified against head `13e4676` + the uncommitted working tree.** The direction's three "absent" claims (no `Requeue*` symbol, no `cmd/audit-replay`, no pause flag) are **all false in the working tree** — the implementation and its tests have landed uncommitted (untracked files, §2). Every acceptance check maps to a landed test; the residual delta is documentation staleness plus one optional verification leg (§8 note). All local done-gate checks were re-run green on this tree (§7) |

## 1. Problem statement (direction, preserved)

Facts stranded in `dead_letter` are permanent audit loss for the governance sink. `drainPublications`
dead-letters rows after `MaxAttempts` (`publicationOutcome` returns `dead_letter`/
`audit.publication_failed`) and, at analysis time, there was no operator recovery path: no
`Requeue*` symbol anywhere (repo grep: 0), `cmd/` contained only `migrate/` and `server/` (no
`cmd/audit-replay`), and no pause flag gated the audit/outbox TTL cleaners, so a replay could race a
purge. The design was complete but pending implementation; the requeue API shape and replay-tool
flags were proposed until the design landed.

**Current-state correction (verified, §2):** the recovery path now exists in the working tree —
`RequeuePublication` (`internal/repository/audit_publication_replay.go:40`) and `RequeueDead`
(`internal/repository/callback_audit.go:115`) move `dead_letter` rows back to `pending` with
`attempt=0`, cleared lease, `error_code='manual_replay'`, idempotent by the `dead_letter` status
predicate; `cmd/audit-replay` (`cmd/audit-replay/main.go`) drives both kinds from the operator shell;
the purge scans are pause-gated (`audit_governance.purge_pause`, `data_retention.purge_paused`). The
requirements below preserve the supplied acceptance checks and pin their landed tests.

## 2. Verified evidence (head `13e4676` + working tree; line numbers live)

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `publicationOutcome` terminal `dead_letter` path at `internal/service/callback_audit.go:146-155` | ✅ holds with drift: `publicationOutcome` at `:144-157`; terminal `return "dead_letter", "audit.publication_failed", now.Add(retryDelay)` at `:151-153` when `attempt >= maxAttempts` (`:151`). The finish path still propagates: `finishPublicationAttempt` `:127-142` calls `s.publications.FinishPublication(...)` at `:137` under `if err :=` and observes via `ObserveAuditPublicationFinishFailure` — no swallow (acceptance 4) |
| E2 | `internal/repository/audit_publication.go` had only `ClaimPublication`/`FinishPublication`; lease-fenced finish at `:121-135`; "no requeue methods" | ⚠️ **superseded**. `ClaimPublication` at `audit_publication.go:46-87` (claims `pending`-due + expired-lease `delivering` only — `dead_letter` structurally unreachable); lease-fenced `FinishPublication` at `:111-122` (guard `id = ? AND status = 'delivering' AND lease_owner = ?`, `RowsAffected != 1 → ErrAuditPublicationLeaseLost` at `:117-121`). **The "no requeue methods" half is false today:** `AuditPublicationRepository` (interface `internal/repository/audit_ledger.go:42-54`) now exposes `RequeuePublication` (`:48`, impl `audit_publication_replay.go:40-72`), `CountDeadLetterPublications` (`:51`, impl `:121-141`), `PurgeDeadLetterPublications` (`:54`, impl `:143-156`); `CallbackRepository` (interface `internal/repository/callback.go:19-39`) exposes `RequeueDead` (`:33`, impl `callback_audit.go:115-143`), `CountDeadLetterDeliveries` (`:36`, impl `:161-176`), `PurgeDeadLetterDeliveries` (`:39`, impl `:180-194`) |
| E3 | `cmd/` contained only `migrate/` and `server/` (no `cmd/audit-replay`) | ❌ **false today**: `cmd/audit-replay/` exists — `main.go` (209 lines), `main_test.go` (396, integration-tagged), `i7_boot_hazard_test.go` (167, integration-tagged). `Makefile:12` `build-audit-replay: ; go build -o bin/audit-replay ./cmd/audit-replay`, chained into `make build` (`Makefile:9`). Flags are final, not proposed: `-config` (`main.go:57`), `-kind publication\|delivery\|both` (default `both`), `-limit` (default 100), `-id`, `-tenant`, `-actor` (default `audit-replay`), `-dry-run` (`main.go:57-73`); `replay` dispatch `:107`, `requeueFact` mint `:197-209` |
| E4 | `internal/app/new.go:351-372` starts `RunAuditCleaner`/`outbox.RunOutboxCleaner` "without pause coordination" | ⚠️ **superseded in mechanism, not intent**: `RunAuditCleaner` (`new.go:337-341`) still has no pause — it TTLs the **legacy `audit_events` table** (not `audit_publications`/`audit_ledger`), so no replay race exists for it. The **outbox** cleaner is pause-gated: `new.go:360-361` → `buildOutboxCleaner` (`internal/app/outbox.go:77-93`) passes `cfg.DataRetention.PurgePaused` → `outbox.RunOutboxCleaner(..., purgePaused)` (`internal/outbox/repository.go:180-189`, `:145` no-op guard). The **audit dead-letter purge is a drain-loop scan**, not a TTL cleaner: `purgeDeadLetters` (`internal/service/callback_worker.go:58-80`) runs per tick (`:49`), gated by `cfg.DeadLetterRetention <= 0 || cfg.PurgePause` (`:65`) → `PurgeDeadLetterPublications`/`PurgeDeadLetterDeliveries`. Config: `audit_governance.purge_pause` + `dead_letter_retention` (`internal/config/audit_governance.go:25-32`, `configs/config.yaml:186-187`, validation `internal/config/validate.go:208-214`); `data_retention.purge_paused` (`configs/config.yaml:238`, validation `validate.go:218-227` — the flag is never inert: it requires a wired cleaner). `audit_ledger` is never deleted by any purge (FK cascade runs ledger→publications only, migration 000045 `:4`) |
| E5 | Design `docs/plans/2026-08-07-dead-letter-replay-requeuepublication-design.md` "pending implementation" | ⚠️ **status line stale**: implementation landed (E2-E4) with documented divergences from design rev 2 (§3.2). The design's §5 step-1 baseline proof (grep 0 hits, `ls cmd/` → `migrate server`) no longer holds |
| E6 | Gates | `scripts/check-audit-swallow.sh:60-61` — P1/P2 patterns include `RequeuePublication`/`RequeueDead` (their error covers the in-tx audit append); Makefile gate scans `internal/service internal/repository internal/app cmd` (`Makefile:69`) with a `cmd_positive` self-test fixture (I6). `scripts/check-anchored-event-ids.sh:31` — `C2_ALLOWLIST="internal/service/callback.go|cmd/audit-replay/main.go"` (the requeue-fact mint site); Makefile gate scans `internal cmd` (`Makefile:77`) |
| E7 | Test infrastructure | `testpg` (real Postgres, integration tag) used by `internal/repository/audit_publication_replay_integration_test.go` (586 lines), `cmd/audit-replay/main_test.go` (`testpg.Start` + migrations), `internal/service/callback_integration_test.go`; governance httptest precedent `internal/connector/auditgovernance/publisher_test.go` (`/token` + `/api/v1/events` loopback; conflict-receipt idempotent-success test) |

**Conclusion:** the direction's problem statement (terminal `dead_letter`, no recovery surface) was
accurate at analysis time; the working tree now implements the recovery surface. This spec's
requirements preserve the four supplied acceptance checks and pin each to a landed, runnable test,
with the residual delta in §3.3.

## 3. Current state and residual delta

### 3.1 Landed (keep-enforced; do not re-do)

- **Requeue core:** `RequeuePublication` (`audit_publication_replay.go:40-72`) and `RequeueDead`
  (`callback_audit.go:115-143`) — one tx: status-scoped selection (`status='dead_letter'` only;
  exact `ids` or oldest `limit` by `(next_attempt_at, id)`, default 100), reset UPDATE
  (`status='pending'`, `attempt=0`, `next_attempt_at=now` Go clock, lease cleared,
  `error_code='manual_replay'`), optional in-tx audit fact appended **only when rows were affected**
  with `ErrAuditDuplicate` folded in-tx (defense-only; fresh v7 per intent at the mint site).
  Idempotent by the status predicate + `uq_audit_publication_fact` (migration 000045 `:12`).
- **CLI:** `cmd/audit-replay` (`main.go:57-209`) — `run(args) error` shape, fail-closed signing-key
  preflight before any DB mutation (`:83-89`, mirrors `validateAuditLedgerConfig`
  `internal/app/audit.go:23-28`), `-dry-run` via the `Count*` estimates, exit 0 on 0-row re-runs.
- **Purge pause:** `audit_governance.purge_pause` + `dead_letter_retention` gate the drain-loop
  purge scan of `audit_publications`/`callback_deliveries` (`callback_worker.go:58-80`);
  `data_retention.purge_paused` gates `outbox.RunOutboxCleaner` (`app/outbox.go:77-93`). Paused ticks
  perform no SELECT and no DELETE.
- **Gates:** `check-audit-swallow` covers the replay surface (P1/P2 + `cmd` root + self-test
  fixtures); `check-anchored-event-ids` allowlists `cmd/audit-replay/main.go` as the sole new C2 mint
  site.

### 3.2 Implementation divergences from design rev 2 (pinned by landed tests; record, do not re-litigate)

> **Remediation (2026-08-08)**: the rows below marked ⚠️ were **closed by the remediation plan**
> (`docs/plans/2026-08-08-dead-letter-replay-remediation-plan.md`, implemented 2026-08-08): the
> un-audited default is deleted (`-actor` required), the fact is mandatory per affected tenant
> (derived from the rows, `ReplayIntent`), `FOR UPDATE` is restored, `-tenant` is a selection
> filter, the purge legs carry the outer EPQ guard + `SKIP LOCKED`, counts are bounded, and the
> C2 allowlist moved with the mint site into `internal/repository`. See also the design doc's
> §9b register.

| Design rev 2 (§) | Landed implementation | Pinned by |
|---|---|---|
| Return `(int, []domain.ID, error)` + `DeadLetterTenants` (§2.1, R2) | ⚠️ **Remediated**: `(int, []*domain.AuditFact, error)` (affected ids ride the facts' allow-listed payload) + `DeadLetterTenants` restored | `audit_ledger.go`/`callback.go` interfaces; `TestDeadLetterTenantsReport` |
| `fact` **required**; nil → `ErrAuditFactRequired` before DB (D7) | ⚠️ **Remediated**: zero/incomplete `ReplayIntent` → `ErrAuditFactRequired` before any DB access; one fact per affected tenant, in-tx; `-actor` required in mutating mode | `TestAuditReplayDefaultInvocationRequiresActor` (inverted), `TestRequeuePublicationPerTenantFacts`, `TestRequeuePublicationEmptyTenantFailsClosed` |
| Locked `FOR UPDATE` selection inside the tx (D1/R5) | ⚠️ **Remediated**: locked selection restored (two-statement shape, no join inside the lock); `RowsAffected == len(selection)` asserted in-tx | `TestRequeuePublicationFactAppendsInTx`; `TestPurgeDeleteBlockedByRequeueCommitEPQ` |
| `-actor` required in mutating mode (D5) | ⚠️ **Remediated**: `-actor` required (trimmed, ≤ 256 bytes), no default | `cmd/audit-replay/main.go`; `TestAuditReplayDefaultInvocationRequiresActor` |
| Purge scan sibling-owned (REQ-4, §7.2) | Landed here: drain-loop `purgeDeadLetters` + repository purge methods + `purge_pause` config | §2 E4 |
| `-kind`/delivery leg sibling-owned (REPO-2, A4) | Landed here: `-kind publication\|delivery\|both`, `RequeueDead` | `main.go`; `TestRequeueDeadResetsDeliveries` |
| `(updated_at, id)` keyset purge literal (D9) | ⚠️ **Remediated**: purge orders by `(updated_at, id)` (migration `000059` indexes); `-tenant` filter + bounded counts added | `TestPurgeDeadLetterPublicationsPredicate`; `TestAuditReplayDryRunCountIsBounded` |

### 3.3 Residual delta (verification/documentation only — no production code)

1. **Design-doc status line** (`docs/plans/2026-08-07-...design.md`): "pending implementation" is
   stale; the §5 step-1 baseline greps no longer hold. Update the status block to reflect the landed
   state and point at this spec's §3.2 divergence table.
2. **Optional single-chain verification leg (acceptance 2 wording):** no one integration test runs
   the **tool** and then drives the **real publisher** against an **httptest governance endpoint**.
   The intent is satisfied across levels today (CLI e2e re-enters the drain; drain delivers exactly
   once to a recording publisher; the publisher↔httptest contract is tested in the connector
   package). A `cmd/audit-replay` integration leg that runs `replay(...)`, then one
   `callbackService.Start` tick with a real `auditgovernance.NewPublisher` pointed at an httptest
   `/token` + `/api/v1/events` server, asserting one POST per `event_id`, would close the wording
   exactly. **Test-only; no production delta.**

## 4. Requirements (supplied acceptance checks preserved; testable criteria + landed status)

### REQ-1 — Acceptance 1: requeue semantics, idempotency, lease guard (LANDED)

> "Integration test: dead_letter row → RequeueDead/RequeuePublication → status pending, attempt reset, next_attempt_at backdated, fact intact; requeue is idempotent and lease-guarded"

Testable criteria and landed pins (all integration-tagged, real Postgres via `testpg`):

1. `dead_letter` → `pending`, `attempt=0`, `error_code='manual_replay'`, lease cleared
   (`lease_owner=''`, `lease_until=NULL`), `next_attempt_at` ≈ now (Go clock; "backdated" = freshly
   due, immediately claimable): **`TestRequeuePublicationResetsDeadLetterRows`**
   (`internal/repository/audit_publication_replay_integration_test.go:109`).
2. Fact intact: the `audit_ledger` fact survives requeue and any publication purge — exactly one
   publication row per `fact_id` before and after (unique constraint):
   `TestRequeuePublicationResetsDeadLetterRows` per-fact count; ledger survival across purge:
   `TestRequeueVsPurgeInterleaving` (facts=4 after both orders).
3. Idempotent: second call over the same rows returns 0, row unchanged, no new ledger row:
   `TestRequeuePublicationResetsDeadLetterRows` (second requeue → 0);
   `TestRequeueDeadResetsDeliveries` (delivery leg).
4. Lease-guarded: a `delivering` row with a live lease is unreachable by `RequeuePublication` **and**
   by the purge scan: **`TestRequeuePublicationLeaseInvariant`** (`:409`).
5. Other statuses untouched (`pending`/`delivering`/`completed`): `TestRequeuePublicationResetsDeadLetterRows`
   (completed row kept `completed/1`).
6. In-tx optional fact: append commits with the reset; `ErrAuditDuplicate` folds without rollback;
   append failure (unkeyed repo) rolls the whole requeue back: **`TestRequeuePublicationFactAppendsInTx`**
   (`:194`). Fresh-v7-per-intent: **`TestRequeuePublicationSecondRequeueMintsDistinctFact`** (`:256`).
7. `-id` overrides `-limit`: `TestRequeuePublicationResetsDeadLetterRows` (id-scoped run → 1).

### REQ-2 — Acceptance 2: `cmd/audit-replay` e2e; row re-enters drain; governance endpoint receives the fact (LANDED + one optional leg)

> "cmd/audit-replay e2e: seed DLQ via test fixture, run tool (flags per pending design — proposed), verify row re-enters drain and a governance httptest endpoint receives the fact"

Testable criteria and landed pins (all integration-tagged):

1. Seed DLQ via the public surface only (`AppendInTx` → `ClaimPublication` →
   `FinishPublication(dead_letter)`): `seedDeadLetterPublication`
   (`cmd/audit-replay/main_test.go:45`).
2. Run the tool core (`replay`, `main.go:107`): `-dry-run` prints `N (dry run)` and mutates nothing
   (rows stay `dead_letter`, zero new ledger rows); run 1 prints `requeued N publication(s)`; run 2
   identical prints `requeued 0`; `-id` requeues exactly that set; `-limit` bounds: **`TestAuditReplayCLIRunTwiceIsIdempotent`** (`main_test.go:133`).
3. Row re-enters the drain: after replay, `ClaimPublication` claims all requeued rows
   (`TestAuditReplayCLIRunTwiceIsIdempotent` tail); one drain cycle delivers each fact exactly once
   per `event_id` and settles `completed` with cleared lease, second drain a no-op:
   **`TestRequeuedPublicationDrainsExactlyOnce`** (`internal/service/callback_integration_test.go:878`,
   T-4.1(b)) — per-`event_id` counters, the requeue fact's own publication delivered too.
4. Governance endpoint receives the fact: publisher↔httptest (`/token` + `/api/v1/events`) contract
   at `internal/connector/auditgovernance/publisher_test.go`, including conflict-receipt idempotent
   success. **Optional leg (§3.3.2):** one tool→drain→real-publisher→httptest chain test. Until it
   lands, the acceptance is satisfied across the two existing levels; this leg is test-only.
5. Fail-closed flags: missing/short signing key → error before any DB touch (config-layer rejections
   fire before the DB is reached): **`TestAuditReplaySigningKeyPreFlight`** (`main_test.go:294`);
   DB outage → error, zero mutation: **`TestAuditReplayDBOutageFailsClosed`** (`main_test.go:349`).
6. Requeue facts with `-tenant`/`-actor` append in the same tx: **`TestAuditReplayCLIRequeueFactWithTenant`** (`main_test.go:226`).

### REQ-3 — Acceptance 3: purge pause flag; replay cannot race purge (LANDED; mechanism drift recorded)

> "Purge pause flag: with pause set, RunAuditCleaner/outbox cleaner skip deleted rows in audit_publications/audit_ledger; replay completes before purge resumes (unit test with fake clock)"

Testable criteria and landed pins:

1. Paused drain-loop purge: with `purge_pause` set and a positive retention, the scan performs **no
   SELECT and no DELETE** for `audit_publications` and `callback_deliveries`:
   **`TestPurgeDeadLettersScanSkipsWhenDisabled`** (`internal/service/audit_purge_test.go:53`); drain-loop
   integration leg: **`TestPurgeScanSkipsWhenGovernanceDisabled`** (`internal/service/callback_integration_test.go`).
2. Paused outbox cleaner: `purge_paused=true` → zero purge calls over a bounded window while the
   unpaused twin demonstrably purges: **`TestBuildOutboxCleanerWiringPauseFlag`**
   (`internal/app/outbox_wiring_test.go:61`); SQL boundary: **`TestOutboxRepo_PurgeDeadLettersPauseGate`**
   (`internal/outbox/purge_pause_integration_test.go`).
3. Replay completes before purge resumes: both interleavings safe — requeue-then-purge (purge deletes
   0), purge-then-requeue (requeue affects 0, no error), ledger fact always survives:
   **`TestRequeueVsPurgeInterleaving`** (`audit_publication_replay_integration_test.go:509`); live
   race under `-race`: **`TestConcurrentPurgeAndRequeueRace`** (`:328`) — `purged + requeued == seeded`,
   no row transitions twice.
4. Mechanism drift (recorded, not a gap): (a) the pause gates the **drain-loop purge scan** and the
   **outbox cleaner**, not `RunAuditCleaner` — that TTL targets the legacy `audit_events` table,
   never `audit_publications`/`audit_ledger`; (b) `audit_ledger` is never purged (publication DELETE
   cascades ledger→publications only, so the fact always survives); (c) unit tests use the **real
   clock with a ±1-minute tolerance window**, not a fake clock (`audit_purge_test.go:99-106`) —
   the pause semantics (zero scan calls) do not depend on clock control.
5. Validation: `dead_letter_retention < 0` rejected (`validate.go:208-214`); `purge_paused` requires
   a wired cleaner (`validate.go:218-227`) — the flag is never inert.

### REQ-4 — Acceptance 4: T-5 regression — replay paths propagate errors; gate green (LANDED, re-run green)

> "T-5 regression: replay paths still propagate FinishPublication errors (no new _ = swallow); make check-audit-swallow stays green"

1. `finishPublicationAttempt` keeps the `if err := s.publications.FinishPublication(...)` shape and
   observes failures (`callback_audit.go:137-141`); the replay surface never swallows:
   `RequeuePublication`/`RequeueDead` append errors propagate (non-`ErrAuditDuplicate`) and roll the
   tx back — pinned by `TestRequeuePublicationFactAppendsInTx` (unkeyed append → rollback).
2. `scripts/check-audit-swallow.sh:60-61` P1/P2 include `RequeuePublication`/`RequeueDead`; the
   Makefile gate scans `internal/service internal/repository internal/app cmd` (`Makefile:69`) with
   positive fixtures under `cmd/` (`scripts/testdata/audit-swallow/cmd_positive/cmd/fixture.go`) so a
   green result means "scanned", never "never scanned" (I6 self-test).
3. Re-run on this tree: **green** (§7).

## 5. Compatibility constraints

- **Wire/proto/API:** none. `POST /v1/callback/deliveries/:id/replay` keeps its owner-scoped
  single-delivery semantics; the operator replay surface is `cmd/audit-replay` only.
- **Attempt accounting:** requeue resets `attempt` to 0 — the only behavioral difference from
  `ResetDeliveryWithAudit` (preserves attempt). `ClaimPublication` never increments attempt
  (`audit_publication.go:46-87`); the increment rides `FinishPublication` (`callback_audit.go:127-132`,
  M-4).
- **Idempotency:** `uq_audit_publication_fact` + the `status='dead_letter'` predicate make re-runs
  no-ops; governance conflict receipts make duplicate deliveries idempotent success — no publisher
  change.
- **File-size/cyclomatic:** `audit_publication_replay.go` = 156 lines, `audit_publication.go` = 122,
  `audit_ledger.go` = 459, `callback_audit.go` (repo) = 194, `callback_audit.go` (service) = 194,
  `cmd/audit-replay/main.go` = 209 — all under the 500-line gate; `main.go` `run`/`replay` carry the
  documented `//nolint:cyclop,funlen` exemption.
- **`make check-anchored-event-ids`:** `cmd/audit-replay/main.go` is the single new C2 mint site
  (`check-anchored-event-ids.sh:31`); the repository never mints event IDs.
- **`0 = disabled`:** `dead_letter_retention: 0` = never purge (`configs/config.yaml:187`);
  `limit <= 0` → code default 100.

## 6. Failure modes (all closed by landed code; see tests)

| Failure | Behavior | Pinned by |
|---|---|---|
| Endpoint down; fact dead-letters | Operator runs `cmd/audit-replay`; row → `pending`, full fresh budget; next drain tick retries | `TestAuditReplayCLIRunTwiceIsIdempotent`, `TestRequeuedPublicationDrainsExactlyOnce` |
| Tool run twice | Second run affects 0, exit 0, no new fact | `TestAuditReplayCLIRunTwiceIsIdempotent` |
| Replay fact append fails | Whole tx rolls back (no requeue without its fact); `ErrAuditDuplicate` folds in-tx only | `TestRequeuePublicationFactAppendsInTx` |
| Purge races replay | Both orders safe; ledger fact always survives; partition property under live race | `TestRequeueVsPurgeInterleaving`, `TestConcurrentPurgeAndRequeueRace` |
| Signing key unconfigured | Exit 1 before any DB mutation | `TestAuditReplaySigningKeyPreFlight` |
| DB unreachable | Exit 1, zero mutation | `TestAuditReplayDBOutageFailsClosed` |
| Row mid-lease (`delivering`) | Unreachable by requeue and purge | `TestRequeuePublicationLeaseInvariant` |
| Second requeue of a re-failed row | Commits with a **fresh** v7 fact — no C2 collision | `TestRequeuePublicationSecondRequeueMintsDistinctFact` |
| `-tenant` requeue fact with unbound tenant | Commits today (documented contract); next governance-enabled boot fails the coverage gate — pinned verbatim | `i7_boot_hazard_test.go` (`TestAuditReplayUnboundTenantEntersBootCoverage`, `TestAuditReplayDeliveryFirstIntroducesUnboundTenant`) |

## 7. Done-gate evidence (re-run on this tree, 2026-08-08)

```text
make test                        → ok (all packages, incl. internal/service, cmd/audit-replay unit-visible paths)
make lint                        → go vet ./... clean
make lint-strict                 → golangci-lint run ./... clean
make check-file-sizes            → "All files ≤ 500 lines"
make check-audit-swallow         → ✓ (roots: internal/service internal/repository internal/app cmd; self-test ok)
make check-anchored-event-ids    → ✓ (C2 allowlist ok, self-test ok)
make build                       → bin/aero-id + bin/audit-replay built
make check-integration-compile   → ok (integration-tagged tests compile; go vet -tags integration clean)
```

`make test-integration` (testcontainers/Docker) was **not** run here; it is required by AGENTS.md
after repository SQL work and covers the integration-tagged tests cited in §4. The integration
fixtures are already compiled green via `check-integration-compile`.

## 8. Testable acceptance mapping (supplied checks preserved verbatim)

| Supplied acceptance | Status | Testable criteria (landed tests) |
|---|---|---|
| **(1)** "dead_letter row → RequeueDead/RequeuePublication → status pending, attempt reset, next_attempt_at backdated, fact intact; requeue is idempotent and lease-guarded" | **LANDED** | `TestRequeuePublicationResetsDeadLetterRows`, `TestRequeueDeadResetsDeliveries`, `TestRequeuePublicationLeaseInvariant`, `TestRequeueVsPurgeInterleaving` (fact survival), `TestRequeuePublicationFactAppendsInTx` — REQ-1 |
| **(2)** "cmd/audit-replay e2e: seed DLQ via test fixture, run tool (flags per pending design — proposed), verify row re-enters drain and a governance httptest endpoint receives the fact" | **LANDED** (one optional leg) | `TestAuditReplayCLIRunTwiceIsIdempotent` (+`-dry-run`, `-id`), `TestAuditReplayCLIRequeueFactWithTenant`, `TestRequeuedPublicationDrainsExactlyOnce`, connector httptest suite — REQ-2; optional §3.3.2 chain leg |
| **(3)** "Purge pause flag: with pause set, RunAuditCleaner/outbox cleaner skip deleted rows in audit_publications/audit_ledger; replay completes before purge resumes (unit test with fake clock)" | **LANDED** (mechanism drift: drain-loop scan + outbox cleaner; real clock ±1 min) | `TestPurgeDeadLettersScanSkipsWhenDisabled`, `TestPurgeScanSkipsWhenGovernanceDisabled`, `TestBuildOutboxCleanerWiringPauseFlag`, `TestOutboxRepo_PurgeDeadLettersPauseGate`, `TestRequeueVsPurgeInterleaving`, `TestConcurrentPurgeAndRequeueRace` — REQ-3 |
| **(4)** "T-5 regression: replay paths still propagate FinishPublication errors (no new _ = swallow); make check-audit-swallow stays green" | **LANDED** | `callback_audit.go:137-141`; P1/P2 + `cmd` scan root + `cmd_positive` fixture; gate re-run green — REQ-4 |

## 9. Amendment register (stale wording in the direction / source analysis)

| # | Stale claim | Correction | Folded into |
|---|---|---|---|
| A1 | "no Requeue* symbol exists anywhere (repo grep: 0)" | False in the working tree: `RequeuePublication`/`CountDeadLetterPublications`/`PurgeDeadLetterPublications` (`audit_ledger.go:48-54`), `RequeueDead`/`CountDeadLetterDeliveries`/`PurgeDeadLetterDeliveries` (`callback.go:33-39`) | §2 E2, §3.1 |
| A2 | "cmd/ contains only migrate/ and server/ (no cmd/audit-replay)" | False: `cmd/audit-replay/` with `main.go` + two integration test files; `make build` emits `bin/audit-replay` | §2 E3 |
| A3 | "no pause flag gates the audit/outbox TTL cleaners (RunAuditCleaner, outbox.RunOutboxCleaner)" | Outbox cleaner **is** pause-gated (`data_retention.purge_paused`); the audit dead-letter purge is a pause-gated drain-loop scan (`audit_governance.purge_pause`); `RunAuditCleaner` TTLs the legacy `audit_events` table and never touches `audit_publications`/`audit_ledger` | §2 E4, REQ-3.4 |
| A4 | "Requeue API shape and replay-tool flags are proposed until the design lands" | Final: `(int, error)` signature, optional fact; flags `-config/-kind/-limit/-id/-tenant/-actor/-dry-run` | §2 E3, §3.2 |
| A5 | Design status "pending implementation" | Stale; landed with divergences (§3.2). Update the design's status block | §3.3.1 |
| A6 | Acceptance (3) "unit test with fake clock" | Unit tests use the real clock with a ±1-minute tolerance window; the pause semantics asserted are clock-independent (zero scan calls) | REQ-3.4 |
| A7 | Acceptance (2) "governance httptest endpoint receives the fact" | Satisfied across levels (drain→recording publisher exactly once; publisher↔httptest in the connector package); single-chain leg optional | REQ-2.4, §3.3.2 |

---

### Done-gate relevance

No production code remains for this direction in this tree; the residual delta is the design-doc
status update (§3.3.1) and the optional test-only chain leg (§3.3.2). The implement/verify stages
must keep the §7 gate set green (`make test-integration` included — the cited tests are
integration-tagged and use real Postgres; run once with Docker).
