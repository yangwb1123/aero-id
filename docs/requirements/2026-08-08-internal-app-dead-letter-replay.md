# Requirements — module `internal/app`: audit publication dead-letter replay (`RequeuePublication` + `cmd/audit-replay` + purge pause flag)

| | |
|---|---|
| Module (batch label) | `internal/app` — the composition root. Direction's change surface spans `internal/repository` + `cmd/audit-replay` (+ verification in `internal/service`/`internal/app`); this spec pins the **internal/app** obligations: config-knob surface and validation, composition-root copy into the drain worker, outbox-cleaner pause wiring, fail-closed wiring, binding-coverage inclusion of `dead_letter`, the operator binary's build chain, gate integration, and the app-level e2e tests |
| Source analysis | `docs/auto/analyses/internal-app-73e67e01.json` (direction 1 of 3: B2 item 5 — audit publication dead-letter replay) |
| Campaign | `aero-id-b2-5-dead-letter-replay`. Sibling specs for the same direction under other labels: `2026-08-08-cmd-server-dead-letter-replay.md` (repository/CLI core), `2026-08-07-internal-connector-aeroim-b2-5-dead-letter-replay.md` (audit-publication core), `2026-08-06-dead-letter-replay-configs-requirements.md` (configs), `2026-08-07-outbox-purge-pause-config-requirements.md` (`data_retention.purge_paused`) |
| Design | `docs/plans/2026-08-07-dead-letter-replay-requeuepublication-design.md` (rev 2, status line "pending implementation" — **stale**: landed with documented divergences, §3.2); verified-design reconciliation: `docs/plans/2026-08-08-cmd-server-dead-letter-replay-verified-design.md` |
| Date | 2026-08-08 |
| Status | **Evidence re-verified against head `13e4676` + the uncommitted working tree.** The direction's three "absent" claims (no `Requeue*` symbol, no `cmd/audit-replay`, no pause flag) are **all false in the working tree** — the implementation and its tests have landed uncommitted. Every supplied acceptance check (T-4 a–e) maps to a landed, runnable test; two mechanism drifts are recorded (§3.2): the dry-run count is shape-identical but not literally a shared helper, and the purge surface is a pause-gated drain-loop scan (no `-purge` CLI flag). Residual delta: design-doc status update + the AGENTS.md `make test-integration` run (§8). All local done-gate checks were re-run green on this tree (§7) |

## 1. Problem statement (direction, preserved)

Facts stranded in `dead_letter` are permanent audit loss for the governance sink. `drainPublications`
dead-letters rows after `MaxAttempts` (`publicationOutcome` returns `dead_letter`/
`audit.publication_failed` at `internal/service/callback_audit.go:144-155`) and, at analysis time, there
was no operator recovery path: no `RequeueDead`/`RequeuePublication` symbol anywhere (repo grep: 0),
`cmd/` contained only `migrate/` and `server/` (no `cmd/audit-replay`), and no pause flag gated the
audit/outbox TTL cleaners, so a replay could race a purge. The design was complete but pending
implementation; the requeue API shape and replay-tool flags were proposed until the design landed.

**Current-state correction (verified, §2):** the recovery path now exists in the working tree, wired
through the `internal/app` composition root: `RequeuePublication`
(`internal/repository/audit_publication_replay.go:40`) and `RequeueDead`
(`internal/repository/callback_audit.go:115`) move `dead_letter` rows back to `pending` with
`attempt=0`, cleared lease, `error_code='manual_replay'`, idempotent by the `dead_letter` status
predicate; `cmd/audit-replay` (`cmd/audit-replay/main.go`, 209 lines) drives both kinds from the
operator shell; the purge scans are pause-gated (`audit_governance.purge_pause`,
`data_retention.purge_paused`). The requirements below preserve the supplied acceptance checks (T-4
a–e) and pin their landed tests at the composition-root level.

## 2. Verified evidence (head `13e4676` + working tree; line numbers live)

### 2.1 Direction-evidence claims (re-checked)

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `grep -rn "RequeueDead\|RequeuePublication" --include=*.go internal/ cmd/` yields 0 hits | ❌ **false today.** `RequeuePublication` (`audit_publication_replay.go:40-72`, interface `internal/repository/audit_ledger.go:48`), `RequeueDead` (`callback_audit.go:115-143`, interface `internal/repository/callback.go:33`), plus `CountDeadLetterPublications` (`audit_publication_replay.go:121-141`, iface `audit_ledger.go:51`), `PurgeDeadLetterPublications` (`:143-156`, iface `:54`), `CountDeadLetterDeliveries` (`callback_audit.go:161-176`, iface `callback.go:36`), `PurgeDeadLetterDeliveries` (`:180-194`, iface `:39`) |
| E2 | `cmd/` contains only `migrate/` and `server/` (no `cmd/audit-replay`) | ❌ **false today.** `cmd/audit-replay/` — `main.go` (209 L), `main_test.go` (integration-tagged), `i7_boot_hazard_test.go` (integration-tagged). `Makefile:10-12` `build-audit-replay` chained into `make build` (`Makefile:9`). Flags final: `-config/-kind publication\|delivery\|both/-limit 100/-id/-tenant/-actor audit-replay/-dry-run` (`main.go:57-73`); `replay` dispatch `:107`; `requeueFact` mint `:197-209` |
| E3 | `internal/service/callback_audit.go:45 drainPublications`, `:115 finishPublicationAttempt`, `:157 publishAuditFact` | ✅ **exact match** in the current tree (`:45`, `:115`, `:157`). Delivery is still drain-only: the single `publisher.Publish(ctx, fact)` call site is `callback_audit.go:179`, reachable only via `publishAuditFact` ← `finishPublicationAttempt` ← `drainPublications` ← the `Start` ticker (`callback_worker.go:36-38`) |
| E4 | `internal/repository/audit_publication.go` (no requeue symbol) | ✅ holds **for that file** (88 L → `ClaimPublication` at `:46-87` with the `dead_letter`-excluding predicate at `:42`, lease-fenced `FinishPublication` at `:111-122`); the requeue surface landed in the sibling `audit_publication_replay.go` |
| E5 | "No purge path or drain-pause flag" | ⚠️ **superseded.** Purge + pause landed: drain-loop scan `purgeDeadLetters` (`callback_worker.go:58-80`, no-op guard `:65` when `DeadLetterRetention <= 0 || PurgePause`; Go-clock cutoff `:67`; batch 50); outbox cleaner `data_retention.purge_paused` (`app/outbox.go:77-93` → `outbox.RunOutboxCleaner(..., purgePaused)` `internal/outbox/repository.go:180-189`, paused `tickOnce` no-op `:145`); config keys `audit_governance.purge_pause`/`dead_letter_retention` (`internal/config/audit_governance.go:25-32`, `configs/config.yaml:186-187`) and `data_retention.purge_paused` (`configs/config.yaml:238`); validation `internal/config/validate.go:211-216` (negative retention rejected) and `:222-233` (`purge_paused` requires a wired cleaner — never inert) |

### 2.2 `internal/app` composition-root surface (this spec's scope; line numbers live)

| # | Surface | Verified location |
|---|---|---|
| A1 | Signing-key preflight at composition root | `validateAuditLedgerConfig` `internal/app/audit.go:23-28`; app unit pin `internal/app/audit_config_test.go:13` (`TestCompositionRootValidatesAuditSigningKey`); the CLI mirrors it before any DB mutation (`cmd/audit-replay/main.go:83-89`) |
| A2 | Knob copy: `buildAuditStack` passes `PurgePause`/`DeadLetterRetention` into `service.CallbackConfig` | `internal/app/audit.go:41-54` (copy at `:52-53`, comment B2-5 WORKER-1 at `:50-51`) → consumed by the drain-loop purge scan `callback_worker.go:64-80` |
| A3 | Fail-closed audit wiring | `wireAuditServices` `internal/app/audit.go:81-105` — panics unless the ledger repo satisfies `AuditPublicationRepository` (the requeue-capable interface, `:48-54`); app unit pins `internal/app/audit_wiring_test.go:31` (`TestWireAuditServicesFailClosedOnMissingSetters`); the app test fake `wiringTestRepo` implements the new delivery-leg methods `RequeueDead`/`PurgeDeadLetterDeliveries`/`CountDeadLetterDeliveries` (`audit_wiring_test.go:72-78`) |
| A4 | Binding coverage includes `dead_letter` backlog | `validateAuditBindingCoverage` `internal/app/audit.go:165-180`, predicate `status IN ('pending','delivering','dead_letter')` at `:173` — a requeued/unrequeued tenant's durable backlog still fails the next boot if unbound; app integration pin `internal/app/audit_integration_test.go:29` (`TestAuditBindingCoverageIncludesAccountsAndDurableBacklog`) |
| A5 | Drain worker + purge scan start | `callbackSvc.Start` at `internal/app/new.go:166`; per-tick order `drainPublications → drain → purgeDeadLetters` `internal/service/callback_worker.go:36-38` |
| A6 | Outbox cleaner pause wiring | `buildOutboxCleaner` `internal/app/outbox.go:77-93` — wiring gate inside (`:83-87`, mirror of the `new.go` guard) so `purge_paused=true` is never inert; passthrough at `:91`; wired at `new.go:357`; app unit pins `internal/app/outbox_wiring_test.go:61` (`TestBuildOutboxCleanerWiringPauseFlag`) and `:101` (`TestBuildOutboxCleanerGateConditions`) |
| A7 | `RunAuditCleaner` untouched by this direction | `new.go:334-341` — TTLs the legacy `audit_events` table only; never `audit_publications`/`audit_ledger` (no replay-race surface) |
| A8 | App-level e2e (integration-tagged `internal/app/audit_integration_test.go`) | `:317` `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain`; `:563` `TestFirstEventGovernanceE2E` (Record → drain ticker → OAuth → 202+receipt → `completed`; zero POSTs before `Start`; conflict-replay leg settles `completed`, never `dead_letter`, ledger stays 1 row); `:648` `TestFirstEventGovernanceE2EConcurrentBatch` (per-`event_id` exactly-once POST counters); `:743` `TestBuildAuditStackHonorsPurgeKnobs` (paused: past-retention dead-letter row preserved **bit-for-bit** while a pending fact completes on the same ticks; unpaused second stack: exactly the past-retention row purged, inside-retention row and ledger fact survive) |
| A9 | Operator binary as sibling composition root | `cmd/audit-replay/main.go` — `run(args) error` shape (`:57`), fail-closed preflight `:83-89`, `-dry-run` via the `Count*` estimates (`:157-166` publications, `:175-183` deliveries), exit 0 on 0-row re-runs; built by `make build` (`Makefile:9-12`) |
| A10 | Gate integration | `make check-audit-swallow` scans `internal/service internal/repository internal/app cmd` (`Makefile:69`); `make check-anchored-event-ids` scans `internal cmd` (`Makefile:77`) with `cmd/audit-replay/main.go` on the C2 allowlist (`scripts/check-anchored-event-ids.sh:31`); file-size/cyclomatic gates: `cmd/audit-replay/main.go` 209 L, `audit_publication_replay.go` 157 L, `callback_audit.go` (repo) 194 L — all ≤ 500 |
| A11 | Schema support | Migration `000045_create_audit_publications.up.sql` — `uq_audit_publication_fact UNIQUE (fact_id)` + `fact_id → audit_ledger(id) ON DELETE CASCADE` (ledger fact survives any publication purge); due/lease indexes ride the replay selection |

## 3. Current state and residual delta

### 3.1 Landed in the working tree (keep-enforced; do not re-do)

- **Requeue core** (repository): status-scoped (`dead_letter` only), bounded (`-id` exact set or oldest
  `limit` by `(next_attempt_at, id)`, default 100), one tx with the reset UPDATE
  (`status='pending'`, `attempt=0`, `next_attempt_at=now` Go clock, lease cleared,
  `error_code='manual_replay'`) and the optional in-tx audit fact appended only when rows were
  affected (`ErrAuditDuplicate` folded in-tx, defense-only). Idempotent by the status predicate +
  `uq_audit_publication_fact`.
- **CLI** (`cmd/audit-replay`): operator recovery surface, no HTTP/gRPC/proto change; `-dry-run`
  mutates nothing; re-runs print `requeued 0` and exit 0.
- **Purge pause** (composition root): `audit_governance.purge_pause` + `dead_letter_retention` gate the
  drain-loop scan; `data_retention.purge_paused` gates the outbox cleaner. Paused ticks perform no
  SELECT and no DELETE.
- **App-level e2e**: `TestBuildAuditStackHonorsPurgeKnobs`, `TestFirstEventGovernanceE2E`,
  `TestFirstEventGovernanceE2EConcurrentBatch`, `TestBuildOutboxCleanerWiringPauseFlag`,
  `TestBuildOutboxCleanerGateConditions`, `TestCompositionRootValidatesAuditSigningKey`,
  `TestWireAuditServicesFailClosedOnMissingSetters`, `TestAuditBindingCoverageIncludesAccountsAndDurableBacklog`.

### 3.2 Implementation divergences from the design / supplied acceptance (recorded, pinned by tests)

| Design / acceptance wording | Landed implementation | Pinned by |
|---|---|---|
| Dry-run count "shares the exact selection helper" (`deadLetterSelection`, D4) | Shape-identical but **re-expressed**: `RequeuePublication` uses `selectDeadLetterRows` (`audit_publication_replay.go:74-86`); `CountDeadLetterPublications` (`:121-141`) rebuilds the same predicate/bounds/order inline (delivery leg: `RequeueDead` → `selectDeadLetterDeliveryRows` `callback_audit.go:145-157`, count inline `:161-176`). Behavioral equivalence pinned e2e: dry-run count == seeded == requeued | `TestAuditReplayCLIRunTwiceIsIdempotent` (`cmd/audit-replay/main_test.go:133`) |
| Purge "keyset `(updated_at, id)`" literal (D9) | Purge orders by `(next_attempt_at, id)` with `updated_at < cutoff` (Go-clock cutoff) and a lease-excluding predicate at the SQL boundary | `audit_publication_replay.go:143-156`; `TestPurgeDeadLetterPublicationsPredicate` (`audit_publication_replay_integration_test.go:557`) |
| Acceptance (d) "`cmd/audit-replay -dry-run/-purge` keyset-cursor e2e" | **No `-purge` CLI flag exists.** The purge surface is the server drain-loop scan (pause-gated) + repository purge methods; the CLI's `-dry-run` is the operator estimate. The acceptance's intent — replay cannot race purge — is realized by the pause flag + interleaving/race tests | `TestBuildAuditStackHonorsPurgeKnobs` (`internal/app/audit_integration_test.go:743`), `TestOutboxRepo_PurgeDeadLettersPauseGate` (`internal/outbox/purge_pause_integration_test.go:20`), `TestRequeueVsPurgeInterleaving` (`audit_publication_replay_integration_test.go:509`), `TestConcurrentPurgeAndRequeueRace` (`:328`) |
| Design §2.2a: fact mint at `internal/service/audit_replay.go` | Mint landed in `cmd/audit-replay/main.go:197-209` (`requeueFact`, fresh v7 per intent; `nil` when `-tenant` empty → un-audited default, documented) — the C2 allowlist carries `cmd/audit-replay/main.go` instead (`check-anchored-event-ids.sh:31`) | `TestAuditReplayCLIRequeueFactWithTenant` (`main_test.go:226`), `i7_boot_hazard_test.go:87/:115/:149` |
| Design §2.1: `DeadLetterTenants` + tenant-binding pre-flight | Not landed: no `DeadLetterTenants`, no binding pre-flight; an unbound-tenant requeue fact commits today and fails the next governance-enabled boot coverage gate — pinned verbatim | `i7_boot_hazard_test.go` (`TestAuditReplayUnboundTenantEntersBootCoverage` `:115`, `TestAuditReplayDeliveryFirstIntroducesUnboundTenant` `:149`) |
| `-actor` required in mutating mode (D5) | `-actor` default `audit-replay` | `main.go:70` |

### 3.3 Residual delta (verification/documentation only — no production code)

1. **Design-doc status line** (`docs/plans/2026-08-07-dead-letter-replay-requeuepublication-design.md`):
   "pending implementation" is stale; the §5 step-1 baseline greps no longer hold. Update the status
   block to the landed state and point at this spec's §3.2 divergence table (same action as the
   `cmd/server` sibling spec §3.3.1).
2. **`make test-integration`** (testcontainers/Docker) is required by AGENTS.md after repository SQL
   work and covers the integration-tagged tests cited in §4; not run in any verification pass to
   date — run once with Docker.
3. **Optional single-chain leg (acceptance (d)/(e) wording):** no one integration test runs the
   **tool** and then drives the **real publisher** against an **httptest governance endpoint** in a
   single chain. Satisfied across levels today (CLI e2e re-enters the drain — `main_test.go` tail;
   drain delivers exactly once to a recording publisher — `TestRequeuedPublicationDrainsExactlyOnce`;
   publisher↔httptest contract — `internal/connector/auditgovernance/publisher_test.go`). Test-only;
   no production delta.

## 4. Requirements (supplied acceptance checks preserved; testable criteria + landed pins)

### REQ-1 — T-4(a): bounded status-predicate requeue; lease invariant (LANDED)

> "RequeuePublication moves bounded dead_letter rows to pending (status predicate; concurrent drain/lease test: cannot steal `delivering`)"

Testable criteria (integration-tagged, real Postgres via testpg):

1. `dead_letter` → `pending`, `attempt=0`, `lease_owner=''`, `lease_until=NULL`,
   `error_code='manual_replay'`, `next_attempt_at` ≈ now (Go clock — immediately claimable):
   **`TestRequeuePublicationResetsDeadLetterRows`** (`audit_publication_replay_integration_test.go:109`).
2. Bounded selection: `-id` exact set overrides `-limit`; else oldest `limit` by `(next_attempt_at,
   id)`; rows in `pending`/`delivering`/`completed` untouched (completed row stays `completed/1`):
   same test.
3. Status predicate at the claim boundary: `ClaimPublication` selects only `pending`-due +
   expired-lease `delivering` (`audit_publication.go:42`) — `dead_letter` rows are structurally
   unreachable by the drain, so requeue can never steal a live lease. Lease invariant pinned both
   ways (requeue **and** purge): **`TestRequeuePublicationLeaseInvariant`** (`:409`).
4. Concurrent drain/lease behavior under `-race`: **`TestConcurrentPurgeAndRequeueRace`** (`:328`) —
   exact partition `purged + requeued == seeded`, no row transitions twice, survivors `pending`
   with cleared lease; interleaving both orders: **`TestRequeueVsPurgeInterleaving`** (`:509`).

### REQ-2 — T-4(b): second run affects 0 rows and mints no fact (LANDED)

> "second run affects 0 rows and mints no fact"

Testable criteria:

1. Repo level: second `RequeuePublication` over the same rows → `(0, nil)`, row unchanged, no new
   ledger row (the `requeued > 0 && fact != nil` gate at `audit_publication_replay.go:60-65` is the
   mint guard): `TestRequeuePublicationResetsDeadLetterRows` (second requeue → 0);
   `TestRequeueDeadResetsDeliveries` (`:446`, delivery leg second run → 0).
2. CLI level: identical re-run prints `requeued 0 publication(s)`, exit 0, zero additional rows:
   **`TestAuditReplayCLIRunTwiceIsIdempotent`** (`cmd/audit-replay/main_test.go:133`).
3. Positive-path fact pin (one fact per kind, same tx): **`TestAuditReplayCLIRequeueFactWithTenant`**
   (`main_test.go:226`); fresh v7 per intent on a second requeue of a re-failed row:
   **`TestRequeuePublicationSecondRequeueMintsDistinctFact`** (`:256`).

### REQ-3 — T-4(c): dry-run count matches the requeue selection (LANDED, with §3.2 drift)

> "dry-run count shares the exact selection helper"

Testable criteria:

1. `CountDeadLetterPublications`/`CountDeadLetterDeliveries` return a point-in-time estimate of the
   same status predicate, bounds and ordering (`audit_publication_replay.go:121-141`,
   `callback_audit.go:161-176`); the mutation surface never calls the count path.
2. CLI `-dry-run` prints `publications to requeue: N (dry run)`, mutates nothing (rows stay
   `dead_letter`), mints no fact (ledger count unchanged), needs no `-tenant`:
   `TestAuditReplayCLIRunTwiceIsIdempotent` (dry-run leg: count == seeded == requeued of run 1).
3. App-level: the drain worker's purge scan is the only DELETE path; a paused scan performs no
   SELECT/DELETE (`callback_worker.go:65`) — the dry-run estimate cannot race a mutation from the
   same process.

### REQ-4 — T-4(d): `-dry-run` e2e + keyset purge with pause flag; drain holds during purge (LANDED, mechanism drift §3.2)

> "cmd/audit-replay -dry-run/-purge keyset-cursor e2e with purge pause flag (drain holds during purge)"

Testable criteria:

1. CLI `-dry-run` e2e over a DLQ seeded through the public surface only (`AppendInTx` →
   `ClaimPublication` → `FinishPublication(dead_letter)`, `seedDeadLetterPublication`
   `main_test.go:45`): `TestAuditReplayCLIRunTwiceIsIdempotent`.
2. Paused purge holds: `audit_governance.purge_pause=true` + positive retention → the drain tick's
   purge scan is a no-op and the dead-letter row is preserved **bit-for-bit** while a pending fact
   completes on the same ticker (proving ticks ran): **`TestBuildAuditStackHonorsPurgeKnobs`**
   (`internal/app/audit_integration_test.go:743`, leg 1 — `deadLetterRowSnapshot` equality).
   Service-level: **`TestPurgeDeadLettersScanSkipsWhenDisabled`**
   (`internal/service/audit_purge_test.go:53`); drain-loop integration leg:
   **`TestPurgeScanSkipsWhenGovernanceDisabled`** (`internal/service/callback_integration_test.go:957`).
3. Unpaused boundary: a second `buildAuditStack` + `Start` from a `PurgePause:false` config on the
   same fixture DB purges exactly the past-retention row (`updated_at < cutoff`, Go-clock cutoff)
   and keeps the inside-retention row and the ledger fact: `TestBuildAuditStackHonorsPurgeKnobs`
   leg 2 (boundary value catches a hardcoded-positive retention copy).
4. Outbox cleaner pause: `data_retention.purge_paused=true` → zero `PurgeDeadLetters` calls over a
   bounded window while the unpaused twin purges (the zero is meaningful): **`TestBuildOutboxCleanerWiringPauseFlag`**
   (`internal/app/outbox_wiring_test.go:61`); gate conditions (interval/retention 0 → nothing
   starts): **`TestBuildOutboxCleanerGateConditions`** (`:88`); SQL boundary:
   **`TestOutboxRepo_PurgeDeadLettersPauseGate`** (`internal/outbox/purge_pause_integration_test.go:20`).
5. Validation: `dead_letter_retention < 0` rejected (`internal/config/validate.go:211-216`);
   `purge_paused=true` requires wired cleaner — never inert (`:222-233`).

### REQ-5 — T-4(e): requeue → drain → `completed`; delivery still only via `drainPublications` (LANDED)

> "requeue → drain completes → publication `completed` (T-10.1: delivery still only via drainPublications)"

Testable criteria:

1. After requeue, `ClaimPublication` claims all requeued rows (CLI test tail) and one drain cycle
   delivers each fact exactly once per `event_id` and settles `completed` with cleared lease; a
   second drain is a no-op: **`TestRequeuedPublicationDrainsExactlyOnce`**
   (`internal/service/callback_integration_test.go:878`).
2. App composition-root chain: `Record` → pending → drain ticker → OAuth client-credentials →
   202+receipt → `completed`; **zero events POSTs before `Start`** (Record never publishes inline);
   crash-after-remote-success replay settles `completed` via conflict receipt, never `dead_letter`,
   ledger stays exactly 1 row: **`TestFirstEventGovernanceE2E`** (`internal/app/audit_integration_test.go:563`).
3. Drain-only delivery invariant (T-10.1): the single `publisher.Publish(ctx, fact)` call site is
   `internal/service/callback_audit.go:179`, reachable only from `publishAuditFact` (`:157`) ←
   `finishPublicationAttempt` (`:115`) ← `drainPublications` (`:45`) ← the `Start` ticker
   (`callback_worker.go:34-46`); no other caller exists (`grep` verified). Fan-out at the root with
   per-`event_id` exactly-once counters: **`TestFirstEventGovernanceE2EConcurrentBatch`** (`:648`).
4. The requeue surface itself never publishes: `cmd/audit-replay` calls only the repository
   requeue/count methods (`main.go:135-170`).

## 5. Compatibility constraints (internal/app-relevant)

- **Wire/proto/API:** none. `POST /v1/callback/deliveries/:id/replay` keeps its owner-scoped
  single-delivery semantics; the operator replay surface is `cmd/audit-replay` only. No HTTP/gRPC
  handler change.
- **Composition-root invariants preserved:** `wireAuditServices` keeps failing closed on a repo that
  cannot satisfy `AuditPublicationRepository` (`audit.go:81-105`); `validateAuditBindingCoverage`
  keeps the `dead_letter` backlog in the boot coverage gate (`audit.go:173`).
- **Config contract:** `purge_pause`/`dead_letter_retention` are static per boot (no hot reload);
  `dead_letter_retention: 0` = never purge; `purge_paused` is never inert (validation requires a
  wired cleaner, matching the `buildOutboxCleaner` gate).
- **File-size/cyclomatic:** all new/changed files under the 500-line gate (209/157/194 L); `run`/
  `replay` in `cmd/audit-replay/main.go` carry the documented `//nolint:cyclop,funlen`.
- **Gates:** `check-audit-swallow` scans `internal/app` and `cmd` (`Makefile:69`) and its P1/P2
  patterns cover `RequeuePublication`/`RequeueDead`; `check-anchored-event-ids` allowlists
  `cmd/audit-replay/main.go` as the sole new C2 mint site (`check-anchored-event-ids.sh:31`).

## 6. Failure modes (closed by landed code at the composition root; pinned by tests)

| Failure | Behavior | Pinned by |
|---|---|---|
| Governance endpoint down; fact dead-letters | Operator runs `cmd/audit-replay`; row → `pending`, full fresh budget; next drain tick retries | `TestAuditReplayCLIRunTwiceIsIdempotent`, `TestRequeuedPublicationDrainsExactlyOnce` |
| Replay races purge | Both orders safe; ledger fact always survives (FK cascade runs ledger→publications only); exact partition under live `-race` | `TestRequeueVsPurgeInterleaving`, `TestConcurrentPurgeAndRequeueRace`, `TestBuildAuditStackHonorsPurgeKnobs` leg 2 |
| Purge paused mid-replay | Paused ticks do no SELECT/DELETE; dead-letter rows preserved bit-for-bit | `TestBuildAuditStackHonorsPurgeKnobs` leg 1, `TestPurgeDeadLettersScanSkipsWhenDisabled`, `TestOutboxRepo_PurgeDeadLettersPauseGate` |
| Signing key unconfigured | Server boot fails (`validateAuditLedgerConfig`); CLI exits 1 before any DB mutation | `TestCompositionRootValidatesAuditSigningKey` (`audit_config_test.go:13`), `TestAuditReplaySigningKeyPreFlight` (`main_test.go:294`) |
| DB unreachable | CLI exit 1, zero mutation | `TestAuditReplayDBOutageFailsClosed` (`main_test.go:349`) |
| Replay-fact append fails | Whole tx rolls back (no requeue without its fact); `ErrAuditDuplicate` folds in-tx only | `TestRequeuePublicationFactAppendsInTx` (`audit_publication_replay_integration_test.go:194`) |
| Unbound-tenant requeue fact | Commits today (documented contract, §3.2); next governance-enabled boot fails the coverage gate (dead_letter rows join the UNION) | `i7_boot_hazard_test.go:115/:149`, `TestAuditBindingCoverageIncludesAccountsAndDurableBacklog` |
| Delivery bypass regression | Fail-closed: `wireAuditServices` panics on an un-attachable publication repo; drain-only delivery pinned by the single `Publish` site | `TestWireAuditServicesFailClosedOnMissingSetters`, `TestFirstEventGovernanceE2E` (zero POSTs before Start) |

## 7. Done-gate evidence (re-run on this tree, 2026-08-08)

```text
make test                        → ok (all packages, incl. internal/app, internal/service, cmd/audit-replay)
make lint                        → go vet ./... clean
make lint-strict                 → golangci-lint run ./... clean
make check-file-sizes            → "All files ≤ 500 lines"
make check-audit-swallow         → ✓ (roots: internal/service internal/repository internal/app cmd; self-test ok)
make check-anchored-event-ids    → ✓ (C2 allowlist ok, self-test ok)
make build                       → bin/aero-id + bin/audit-replay built
make check-integration-compile   → ok (integration-tagged tests compile; go vet -tags integration clean)
```

`make test-integration` (testcontainers/Docker) was **not** run here; it is required by AGENTS.md
after repository SQL work and covers the integration-tagged tests cited in §4 (including the
`internal/app` e2e suite). The integration fixtures are already compiled green via
`check-integration-compile`.

## 8. Testable acceptance mapping (supplied checks preserved verbatim)

| Supplied acceptance (T-4) | Status | Testable criteria (landed pins) |
|---|---|---|
| **(a)** "RequeuePublication moves bounded dead_letter rows to pending (status predicate; concurrent drain/lease test: cannot steal `delivering`)" | **LANDED** | `TestRequeuePublicationResetsDeadLetterRows` (`audit_publication_replay_integration_test.go:109`), `TestRequeuePublicationLeaseInvariant` (`:409`), `TestConcurrentPurgeAndRequeueRace` (`:328`) — REQ-1 |
| **(b)** "second run affects 0 rows and mints no fact" | **LANDED** | `TestRequeuePublicationResetsDeadLetterRows` (second → 0), `TestRequeueDeadResetsDeliveries` (`:446`), `TestAuditReplayCLIRunTwiceIsIdempotent` (`main_test.go:133`), mint gate `audit_publication_replay.go:60-65` — REQ-2 |
| **(c)** "dry-run count shares the exact selection helper" | **LANDED** (shape-identical; §3.2 drift) | `CountDeadLetterPublications`/`CountDeadLetterDeliveries` same predicate/bounds/order (`audit_publication_replay.go:121-141`, `callback_audit.go:161-176`); dry-run count == seeded == requeued in `TestAuditReplayCLIRunTwiceIsIdempotent` — REQ-3 |
| **(d)** "cmd/audit-replay -dry-run/-purge keyset-cursor e2e with purge pause flag (drain holds during purge)" | **LANDED** (no `-purge` flag; purge is the pause-gated drain-loop scan, §3.2) | `TestAuditReplayCLIRunTwiceIsIdempotent` (`-dry-run`), `TestBuildAuditStackHonorsPurgeKnobs` (`internal/app/audit_integration_test.go:743`), `TestPurgeDeadLettersScanSkipsWhenDisabled`, `TestBuildOutboxCleanerWiringPauseFlag`, `TestOutboxRepo_PurgeDeadLettersPauseGate`, `TestRequeueVsPurgeInterleaving`, `TestConcurrentPurgeAndRequeueRace` — REQ-4 |
| **(e)** "requeue → drain completes → publication `completed` (T-10.1: delivery still only via drainPublications)" | **LANDED** | `TestRequeuedPublicationDrainsExactlyOnce` (`callback_integration_test.go:878`), `TestFirstEventGovernanceE2E` + `TestFirstEventGovernanceE2EConcurrentBatch` (`internal/app/audit_integration_test.go:563/:648`), single `Publish` site `callback_audit.go:179` — REQ-5 |

## 9. Amendment register (stale wording in the direction / source analysis)

| # | Stale claim | Correction | Folded into |
|---|---|---|---|
| A1 | "`grep -rn 'RequeueDead\|RequeuePublication'` yields 0 hits" | False in the working tree: requeue/count/purge symbols landed in `internal/repository` (interfaces `audit_ledger.go:42-54`, `callback.go:19-39`) | §2.1 E1, §3.1 |
| A2 | "`cmd/` contains only `migrate` and `server` (no `cmd/audit-replay`)" | False: `cmd/audit-replay/` (main.go 209 L + two integration test files); `make build` emits `bin/audit-replay` | §2.1 E2 |
| A3 | "No purge path or drain-pause flag" | Superseded: `audit_governance.purge_pause` + `dead_letter_retention` gate the drain-loop scan; `data_retention.purge_paused` gates the outbox cleaner; validation `validate.go:211-216/:222-233` | §2.1 E5, REQ-4 |
| A4 | "`internal/service/callback_audit.go:45 drainPublications, :115 finishPublicationAttempt, :157 publishAuditFact`" | **Holds exactly** in the current tree; add the delivery-only pin `Publish` at `:179` | §2.1 E3, REQ-5 |
| A5 | "`internal/repository/audit_publication.go` (no requeue symbol)" | Still true for that file; the requeue surface landed in the sibling `audit_publication_replay.go` (157 L) | §2.1 E4 |
| A6 | Design status "pending implementation"; §5 step-1 baseline greps | Stale; landed with divergences (§3.2). Update the design's status block | §3.3.1 |
| A7 | Acceptance (d) "`-purge` keyset-cursor e2e" | No `-purge` flag; purge is the pause-gated drain-loop scan + repository methods; intent pinned by pause/interleaving/race tests | §3.2, REQ-4 |

---

### Done-gate relevance

No production code remains for this direction in this tree; the residual delta is the design-doc
status update (§3.3.1) and the AGENTS.md `make test-integration` run (§3.3.2). The implement/verify
stages must keep the §7 gate set green, `make test-integration` included — the `internal/app` e2e
suite (`audit_integration_test.go`) is integration-tagged and uses real Postgres via testpg; run
once with Docker.
