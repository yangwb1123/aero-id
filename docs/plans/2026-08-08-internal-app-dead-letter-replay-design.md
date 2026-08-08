# Design — module `internal/app`: audit dead-letter replay (composition-root surface of `RequeuePublication`/`RequeueDead`, `cmd/audit-replay`, purge-pause knobs)

| | |
|---|---|
| Module (batch label) | `internal/app` — the composition root |
| Input evidence | `docs/requirements/2026-08-08-internal-app-dead-letter-replay.md` (289 L, evidence re-verified against head `13e4676` + working tree); direction snapshot `docs/auto/analyses/internal-app-73e67e01.json` (direction 1: B2 item 5 — stale on all three "absent" claims) |
| Sibling design | `docs/plans/2026-08-08-cmd-server-dead-letter-replay-verified-design.md` (repository/CLI core; §1.2 blocker resolved 2026-08-08) |
| Original design | `docs/plans/2026-08-07-dead-letter-replay-requeuepublication-design.md` (rev 2, status line stale — §6 step 1) |
| Date | 2026-08-08 |
| Status | **Verified design — LANDED, all eight done-gate checks re-run green on this tree (§8).** Every claim in the input evidence independently re-verified (§1); no production code remains for this direction. Residual delta is documentation-only (§6.2) plus one optional test-only chain leg (§6.3) |

## 1. Verification verdict (input-evidence claims, independently re-checked on head `13e4676` + working tree)

| # | Claim (input evidence) | Verdict | Where verified |
|---|---|---|---|
| V1 | "0 `Requeue*` hits, no `cmd/audit-replay`, no pause flag" (analysis snapshot) | ❌ **all three false today.** `RequeuePublication` (`internal/repository/audit_publication_replay.go:40`), `CountDeadLetterPublications` (`:121`), `PurgeDeadLetterPublications` (`:143`); `RequeueDead` (`internal/repository/callback_audit.go:115`), `CountDeadLetterDeliveries` (`:161`), `PurgeDeadLetterDeliveries` (`:180`); interfaces `internal/repository/audit_ledger.go:48-54`, `callback.go:33-39`; `cmd/audit-replay/` (main.go 209 L + two integration-tagged test files) | grep + read |
| V2 | `callback_audit.go:45/:115/:157` pins hold; single `Publish` site `:179` | ✅ **exact match.** `drainPublications` `:45`, `finishPublicationAttempt` `:115`, `publishAuditFact` `:157`, sole `publisher.Publish(ctx, fact)` `:179`; reachable only via `publishAuditFact` ← `finishPublicationAttempt` ← `drainPublications` ← `Start` ticker | grep |
| V3 | `validateAuditLedgerConfig` `audit.go:23-28`; knob copy `:52-53`; `wireAuditServices` `:81-105`; binding coverage `dead_letter` `:173` | ✅ exact. Knob copy `PurgePause`/`DeadLetterRetention` → `service.CallbackConfig` at `audit.go:52-53`; coverage predicate `status IN ('pending','delivering','dead_letter')` at `:173` | read |
| V4 | `buildOutboxCleaner` pause passthrough `outbox.go:77-93`; per-tick `drainPublications → drain → purgeDeadLetters` `callback_worker.go:36-38` | ✅ exact. Wiring gate inside `outbox.go:83-87` (cleaner wired iff interval+retention positive — `purge_paused` never inert); passthrough `:91`; wired `new.go:357`. `Start` at `new.go:166` | read |
| V5 | Config keys `config.yaml:186-187,238`; `validate.go:211-216,222-233` | ⚠️ **functionally holds, line drift.** `audit_governance.purge_pause` `:188`, `dead_letter_retention` `:189`; `data_retention.outbox_dead_letter_retention` `:237`, `purge_paused` `:240`. `validateAuditGovernance` (negative retention rejected) `internal/config/validate.go:214-223`; `purge_paused` requires wired cleaner `:259-266`. Recorded in §9 | read |
| V6 | `check-anchored-event-ids.sh:31` C2 allowlist; Makefile chain | ✅ exact. Allowlist `internal/service/callback.go|cmd/audit-replay/main.go|internal/service/audit_fact.go` at `:31`; `make build` chains `build-audit-replay` (`Makefile:8-12`); gate roots `Makefile:69` (audit-swallow incl. `internal/app cmd`), `:77` (anchored-event-ids `internal cmd`) | read |
| V7 | Test pins: `TestBuildAuditStackHonorsPurgeKnobs` `audit_integration_test.go:743`, `TestFirstEventGovernanceE2E` `:563`, `TestBuildOutboxCleanerWiringPauseFlag` `outbox_wiring_test.go:61` | ✅ exact, plus `TestFirstEventGovernanceE2EConcurrentBatch` `:648`, `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain` `:317`, `TestBuildOutboxCleanerGateConditions` `:88`, `TestCompositionRootValidatesAuditSigningKey` `audit_config_test.go:13`, `TestWireAuditServicesFailClosedOnMissingSetters` `audit_wiring_test.go:31` | grep |
| V8 | Migration `000045_create_audit_publications.up.sql` | ⚠️ filename drift: `migrations/000045_audit_publications.up.sql`. Contents match: `uq_audit_publication_fact UNIQUE (fact_id)`, FK `fact_id → audit_ledger(id) ON DELETE CASCADE`, due/lease indexes (§4) | read |
| V9 | §7 gate table "all green on this tree" | ✅ **freshly re-verified 2026-08-08**: `make test`, `make lint`, `make lint-strict`, `make check-file-sizes`, `make check-audit-swallow`, `make check-anchored-event-ids`, `make build` (emits `bin/aero-id` + `bin/audit-replay`), `make check-integration-compile` — all green (§8) | ran gates |
| V10 | Residual delta: design-doc status update + `make test-integration` | ✅ open, documentation/verification only | §6.2 |

**Conclusion:** the input evidence is accurate. The direction is fully landed and correctly pinned; the only open items are the stale status line of the original design doc, the AGENTS.md `make test-integration` run (testcontainers/Docker, never executed), and the optional single-chain test leg.

## 2. Design decisions (concrete; as-landed, with the alternatives that were rejected)

| # | Decision | Rationale / rejected alternative |
|---|---|---|
| D1 | **Requeue is a repository-level, status-scoped, one-tx operation** (`RequeuePublication`/`RequeueDead`): SELECT `status='dead_letter'` (exact `ids` or oldest `limit` by `(next_attempt_at, id)`, default 100) → one UPDATE (`status='pending'`, `attempt=0`, `next_attempt_at=now` Go clock, lease cleared, `error_code='manual_replay'`) → optional in-tx audit fact appended **only when rows were affected**. Rejected: a service-level orchestration with per-row txs (loses atomicity of fact+requeue) and a single generic SQL helper across both kinds (rejected because the row types differ; the selection helper is shared per kind) | `audit_publication_replay.go:40-72`, `callback_audit.go:115-143`; `TestRequeuePublicationFactAppendsInTx` |
| D2 | **Operator surface is a sibling CLI binary, not HTTP/gRPC.** `cmd/audit-replay` is a second composition root: config load → fail-closed signing-key preflight → DB open → dispatch. Rejected: `POST /v1/...` admin endpoint (would need authz, rate-limit, idempotency, and OpenAPI surface for a maintenance action; also expands the live HTTP API for zero product value) and a `cmd/server` subcommand (would couple the operator path to the serving binary's lifecycle) | `cmd/audit-replay/main.go:57-113`; `Makefile:8-12` |
| D3 | **Dry-run is a shape-identical re-expressed count, not a shared helper.** `CountDeadLetterPublications`/`CountDeadLetterDeliveries` rebuild the same predicate/bounds/order inline; the mutation surface never calls the count path. Rejected: extracting `deadLetterSelection` as a single helper (typing forces an `any` model; the behavior is pinned e2e instead — dry-run count == seeded == requeued) | `audit_publication_replay.go:121-141`, `callback_audit.go:161-176`; drift recorded in requirements §3.2 |
| D4 | **Purge is the pause-gated drain-loop scan — no `-purge` CLI flag.** The server's per-tick `purgeDeadLetters` (batch 50, keyset `(next_attempt_at, id)`, `updated_at < cutoff`, lease-excluding predicate at the SQL boundary) is a no-op when `DeadLetterRetention <= 0 || PurgePause`; `data_retention.purge_paused` gates `RunOutboxCleaner` the same way. Rejected: a `-purge` flag on the CLI (the acceptance's intent — replay cannot race purge — is realized by pause + interleaving/race tests, and a standalone purge tool duplicates server logic with no ownership benefit) | `callback_worker.go:58-80` (no-op guard `:65`), `outbox/repository.go:180-189`; `TestRequeueVsPurgeInterleaving`, `TestConcurrentPurgeAndRequeueRace`, `TestBuildAuditStackHonorsPurgeKnobs` |
| D5 | **Config knobs are static per boot, `0 = disabled`.** `audit_governance.dead_letter_retention: 0` = never purge; negative rejected at validation. `purge_pause`/`purge_paused` are inert when the corresponding worker cannot run (governance relay disabled; outbox cleaner unwired — the `buildOutboxCleaner` gate and `validate.go` agree). Rejected: hot reload (the drain worker reads `CallbackConfig` captured at composition; hot reload would add a new consistency surface for zero benefit) | `internal/app/audit.go:52-53`, `outbox.go:83-87`, `validate.go:214-223,259-266` |
| D6 | **Composition root fails closed.** `wireAuditServices` panics unless the ledger repo satisfies `AuditPublicationRepository` and the callback service accepts it; `assertAuditWiring` panics on any service constructed without a non-nil audit logger. The requeue-capable interface is therefore load-bearing at boot. Rejected: graceful degradation (a silently un-attachable publication repo would pend facts with no delivery path — the FM-8 contract forbids it) | `internal/app/audit.go:81-105`, `new.go` wiring; `TestWireAuditServicesFailClosedOnMissingSetters` |
| D7 | **Fact minting lives only at the CLI, never in the repository.** `requeueFact` (`cmd/audit-replay/main.go:197-209`) mints a fresh UUIDv7 per intent; `-tenant` empty → no fact (documented un-audited default). The C2 allowlist carries `cmd/audit-replay/main.go` so the anchored-event-id gate stays meaningful (the repository never derives event IDs). Rejected: minting in the repository (would force every caller to supply tenant/actor context and would widen the C2 mint surface) | `check-anchored-event-ids.sh:31`; `TestAuditReplayCLIRequeueFactWithTenant`, `TestRequeuePublicationSecondRequeueMintsDistinctFact` |

## 3. API changes (as-landed contract surface)

### 3.1 Repository interfaces (the only Go API additions)

```go
// internal/repository/audit_ledger.go:42-54 (AuditPublicationRepository)
RequeuePublication(ctx context.Context, ids []domain.ID, limit int, fact *domain.AuditFact) (int, error)
CountDeadLetterPublications(ctx context.Context, ids []domain.ID, limit int) (int, error)
PurgeDeadLetterPublications(ctx context.Context, cutoff time.Time, limit int) (int64, error)

// internal/repository/callback.go:19-39 (CallbackRepository, delivery-leg twins)
RequeueDead(ctx context.Context, ids []domain.ID, limit int, fact *domain.AuditFact) (int, error)
CountDeadLetterDeliveries(ctx context.Context, ids []domain.ID, limit int) (int, error)
PurgeDeadLetterDeliveries(ctx context.Context, cutoff time.Time, limit int) (int64, error)
```

Contract: `Requeue*` returns the count of rows transitioned (0 on re-run, exit 0); `Count*` is a point-in-time estimate (dry-run only); `Purge*` deletes only past-cutoff, lease-free `dead_letter` rows and returns rows affected. All requeue/purge predicates are SQL-side; no app-level lease checks.

### 3.2 `service.CallbackConfig` gains two knobs (composition-root copy)

```go
// internal/app/audit.go:52-53 (copy from AuditGovernanceConfig)
PurgePause:          cfg.AuditGovernance.PurgePause,
DeadLetterRetention: cfg.AuditGovernance.DeadLetterRetention,
```

Consumed by the drain-loop purge scan (`callback_worker.go:64-80`). `data_retention.purge_paused` reaches `outbox.RunOutboxCleaner` through `buildOutboxCleaner` (`outbox.go:91`).

### 3.3 Config schema (static per boot)

```yaml
# configs/config.yaml:188-189
audit_governance:
  purge_pause: false              # maintenance: pause dead-letter purge/dead-letter scanning in the drain loop
  dead_letter_retention: 0        # how long dead-lettered publications/deliveries survive before purge (0 = never purge)
# configs/config.yaml:240
data_retention:
  purge_paused: false             # maintenance: suspend outbox retention purge so dead-letter replay cannot race it
```

Validation: `dead_letter_retention < 0` → Load error (`validate.go:218-219`); `purge_paused=true` requires `outbox_clean_interval > 0` and `outbox_dead_letter_retention > 0` (`validate.go:259-266`).

### 3.4 CLI (new sibling binary; no HTTP/gRPC/proto change)

```
audit-replay [-config configs/config.yaml] [-kind publication|delivery|both]
             [-limit 100] [-id id[,id...]] [-tenant T] [-actor audit-replay] [-dry-run]
```

- `-id` (exact row set) overrides `-limit` (oldest-first bound).
- `-dry-run` prints `N (dry run)` via the `Count*` estimates, mutates nothing, mints nothing, needs no `-tenant`.
- Mutating mode: one tx per kind; the optional fact (fresh UUIDv7, `-tenant`/`-actor` stamped) is appended in-tx only when rows were affected; any append error rolls the whole requeue back.
- Fail-closed preflight: empty ledger signing key → exit 1 before any DB mutation; DB unreachable → exit 1, zero mutation.

## 4. Compatibility constraints

- **No wire change**: no HTTP/gRPC/proto; `POST /v1/callback/deliveries/:id/replay` keeps its owner-scoped single-delivery semantics (attempt preserved); the operator replay surface is `cmd/audit-replay` only.
- **Schema constraint (migration `000045_audit_publications`)**: `uq_audit_publication_fact UNIQUE (fact_id)` makes requeue re-runs idempotent at the row level; FK `fact_id → audit_ledger(id) ON DELETE CASCADE` runs ledger→publications only, so **the ledger fact survives any purge** in both replay/purge interleavings. Never edit an applied migration.
- **Indexes — amended by the 2026-08-08 remediation plan (§3.2/§7)**: the original claim that "requeue/purge ride the existing due/lease indexes" is **false for the purge cutoff**: `idx_audit_publications_due`/`_lease` carry no `updated_at`, so the steady-state purge tick walked the whole dead_letter index slice with a heap fetch per entry, every tick. Requeue still rides `idx_audit_publications_due` (locked selection, index-only); the purge `updated_at` cutoff is served by the NEW `idx_audit_publications_purge`/`idx_callback_deliveries_purge` (migration `000059_add_dead_letter_purge_indexes`, renumbered from the plan's 000046 which collided with landed migrations), with the purge scans reordered to `(updated_at ASC, id ASC)`.
- **Lease invariants**: `ClaimPublication` selects only `pending`-due + expired-lease `delivering` (`audit_publication.go:42`) — `dead_letter` rows are structurally unreachable by the drain, so requeue can never steal a live lease; purge excludes leased rows at the SQL boundary. Both invariants are predicates, not app-level checks.
- **Attempt accounting**: requeue resets `attempt=0` (fresh budget) — the only behavioral difference from `ResetDeliveryWithAudit` (preserves attempt). Claim never increments; the increment rides `FinishPublication`.
- **Idempotency**: `dead_letter` status predicate + `uq_audit_publication_fact` make re-runs no-ops; governance conflict receipts make duplicate deliveries idempotent success — no publisher change.
- **Binding coverage**: the boot gate includes `dead_letter` backlog (`audit.go:173`), so a requeue of an unbound tenant's rows commits today but fails the next governance-enabled boot — documented contract, pinned verbatim by the i7 boot-hazard tests.
- **Gate compatibility**: `check-audit-swallow` P1/P2 cover the bare-call forms of `RequeuePublication`/`RequeueDead`; `check-anchored-event-ids` allowlists `cmd/audit-replay/main.go` as the sole new C2 mint site; file-size/cyclomatic: `audit_publication_replay.go` 157 L, repo `callback_audit.go` 194 L, `cmd/audit-replay/main.go` 209 L — all under the 500-line gate, `run`/`replay` carry documented `//nolint:cyclop,funlen`.
- **`0 = disabled`**: `dead_letter_retention: 0` = never purge; `limit <= 0` → code default 100.

## 5. Failure modes (closed by landed code; pinned by tests)

| Failure | Behavior | Pinned by |
|---|---|---|
| Governance endpoint down; fact dead-letters | Operator runs `cmd/audit-replay`; row → `pending`, attempt 0, freshly due; next drain tick retries with full budget; repeated failure re-dead-letters after `MaxAttempts` | `TestAuditReplayCLIRunTwiceIsIdempotent`, `TestRequeuedPublicationDrainsExactlyOnce` |
| Tool run twice / 0 eligible rows | Second run affects 0, mints no fact, exit 0 | `TestAuditReplayCLIRunTwiceIsIdempotent` (CLI), `TestRequeuePublicationResetsDeadLetterRows`/`TestRequeueDeadResetsDeliveries` (repo) |
| Replay-fact append fails | Whole tx rolls back (no requeue without its fact); `ErrAuditDuplicate` folds in-tx only (defense against retried identical append) | `TestRequeuePublicationFactAppendsInTx` |
| Replay races purge | Both orders safe; ledger fact always survives; exact partition (`purged + requeued == seeded`, no row transitions twice) — **amended by the 2026-08-08 remediation plan (§3.1/§7)**: as landed, the purge DELETE re-checked only `id IN (subquery)` under EPQ and could delete a concurrently requeued row; the claim is guaranteed by code only after the outer status guard (`AND status = 'dead_letter'`) on both audit purge legs plus `FOR UPDATE SKIP LOCKED` in their subqueries (outbox dead-letter leg: guard only). Deterministic proof: `TestPurgeDeleteBlockedByRequeueCommitEPQ` (+ delivery twin); the race test remains the aggregate partition stress | `TestRequeueVsPurgeInterleaving`, `TestConcurrentPurgeAndRequeueRace`, `TestBuildAuditStackHonorsPurgeKnobs` leg 2 |
| Purge paused mid-replay | Paused ticks perform no SELECT and no DELETE; dead-letter rows preserved bit-for-bit while a pending fact completes on the same ticker | `TestBuildAuditStackHonorsPurgeKnobs` leg 1 (snapshot equality), `TestPurgeDeadLettersScanSkipsWhenDisabled`, `TestOutboxRepo_PurgeDeadLettersPauseGate` |
| Signing key unconfigured / short | Server boot fails (`validateAuditLedgerConfig`); CLI exits 1 before any DB mutation | `TestCompositionRootValidatesAuditSigningKey`, `TestAuditReplaySigningKeyPreFlight` |
| DB unreachable | CLI exit 1, zero mutation | `TestAuditReplayDBOutageFailsClosed` |
| Row mid-lease (`delivering`) | Structurally unreachable by requeue and purge (status predicates) | `TestRequeuePublicationLeaseInvariant` |
| Second requeue of a re-failed row | Commits with a **fresh** v7 fact — no C2 collision, no `uq_audit_publication_fact` violation | `TestRequeuePublicationSecondRequeueMintsDistinctFact` |
| Unbound-tenant requeue fact | Commits today (documented contract); next governance-enabled boot fails the coverage gate (`dead_letter` rows join the UNION) | `i7_boot_hazard_test.go` (`TestAuditReplayUnboundTenantEntersBootCoverage`, `TestAuditReplayDeliveryFirstIntroducesUnboundTenant`), `TestAuditBindingCoverageIncludesAccountsAndDurableBacklog` |
| Delivery-bypass regression | Fail-closed: `wireAuditServices` panics on an un-attachable publication repo; drain-only delivery pinned by the single `Publish` site | `TestWireAuditServicesFailClosedOnMissingSetters`, `TestFirstEventGovernanceE2E` (zero POSTs before `Start`) |
| Requeue error swallowed | Impossible by gate: P1/P2 patterns fire on `_ =` and bare calls; append failure rolls back | `check-audit-swallow.sh`; `TestRequeuePublicationFactAppendsInTx` |

## 6. Migration steps

### 6.1 Schema (applied via `cmd/migrate`, not auto-migration)

`migrations/000045_audit_publications.up.sql` (applied; `.down.sql` exists): `audit_publications` with `uq_audit_publication_fact UNIQUE (fact_id)`, FK `fact_id → audit_ledger(id) ON DELETE CASCADE`, `idx_audit_publications_due (status, next_attempt_at, id)`, `idx_audit_publications_lease (status, lease_until, next_attempt_at)`. No further migration is needed for this direction; the delivery-leg rows already exist (`callback_deliveries` carries `status`/`lease_*`/`error_code`).

### 6.2 Residual delta (documentation/verification only — no production code)

1. **Update the stale design status line**: `docs/plans/2026-08-07-dead-letter-replay-requeuepublication-design.md` — "pending implementation" → landed, pointing at the requirements §3.2 divergence table and this design's §2/§3 (as-landed surface). This is the only remaining artifact claim known to be stale.
2. **Run `make test-integration` once with Docker** (AGENTS.md requirement after repository SQL work; testcontainers/Postgres + MinIO). Never executed to date; `make check-integration-compile` is green, so the suite compiles. Covers the integration-tagged pins in §7 (incl. the `internal/app` e2e suite).
3. **Optional single-chain leg** (test-only, closes acceptance T-4(e) wording exactly): one `cmd/audit-replay` integration test that runs `replay(...)`, then one `callbackService.Start` tick with a real `auditgovernance.NewPublisher` pointed at an httptest `/token` + `/api/v1/events` server, asserting one POST per `event_id`. Satisfied across levels today (CLI e2e re-enters drain; drain delivers exactly once to a recording publisher — `TestRequeuedPublicationDrainsExactlyOnce`; publisher↔httptest contract — `internal/connector/auditgovernance/publisher_test.go`). No production delta.

### 6.3 Operational migration (runbook, not code)

1. Before any replay: set `audit_governance.purge_pause: true` (and `data_retention.purge_paused: true` for outbox dead letters) and restart — paused ticks preserve DLQ rows bit-for-bit.
2. `bin/audit-replay -dry-run -kind both` → confirm the estimate.
3. `bin/audit-replay -kind both -tenant <T> -actor <operator>` → rows → `pending`, facts minted.
4. Verify: second run prints `requeued 0`; drain completes rows to `completed` (metrics/lag); then clear the pause flags and restart.

## 7. Testable acceptance mapping (T-4 a–e → landed pins; all present and gate-green)

| Supplied acceptance | Status | Testable criteria (landed pins) |
|---|---|---|
| **(a)** RequeuePublication moves bounded `dead_letter` rows to `pending` (status predicate; concurrent drain/lease test: cannot steal `delivering`) | **LANDED** | `TestRequeuePublicationResetsDeadLetterRows` (`audit_publication_replay_integration_test.go:109`), `TestRequeuePublicationLeaseInvariant` (`:409`), `TestConcurrentPurgeAndRequeueRace` (`:328`) |
| **(b)** Second run affects 0 rows and mints no fact | **LANDED** | `TestRequeuePublicationResetsDeadLetterRows` (second → 0), `TestRequeueDeadResetsDeliveries` (`:446`), `TestAuditReplayCLIRunTwiceIsIdempotent` (`cmd/audit-replay/main_test.go:133`), mint gate `audit_publication_replay.go:60-65` |
| **(c)** Dry-run count shares the exact selection helper | **LANDED** (shape-identical; §2 D3 drift) | `Count*` same predicate/bounds/order; dry-run count == seeded == requeued in `TestAuditReplayCLIRunTwiceIsIdempotent` |
| **(d)** `-dry-run`/`-purge` keyset-cursor e2e with purge pause flag (drain holds during purge) | **LANDED** (no `-purge` flag; pause-gated drain-loop scan, §2 D4) | `TestAuditReplayCLIRunTwiceIsIdempotent` (`-dry-run`), `TestBuildAuditStackHonorsPurgeKnobs` (`internal/app/audit_integration_test.go:743`), `TestBuildOutboxCleanerWiringPauseFlag` (`outbox_wiring_test.go:61`), `TestOutboxRepo_PurgeDeadLettersPauseGate`, `TestRequeueVsPurgeInterleaving` (`:509`), `TestConcurrentPurgeAndRequeueRace` (`:328`) |
| **(e)** Requeue → drain completes → `completed`; delivery still only via `drainPublications` | **LANDED** | `TestRequeuedPublicationDrainsExactlyOnce` (`internal/service/callback_integration_test.go:878`), `TestFirstEventGovernanceE2E` + `TestFirstEventGovernanceE2EConcurrentBatch` (`internal/app/audit_integration_test.go:563/:648`), single `Publish` site `callback_audit.go:179` |

## 8. Done-gate evidence (freshly re-run on this tree, 2026-08-08)

```text
make test                        → ✓ ok (all packages)
make lint                        → ✓ go vet ./... clean
make lint-strict                 → ✓ golangci-lint run ./... clean
make check-file-sizes            → ✓ "All files ≤ 500 lines"
make check-audit-swallow         → ✓ (roots: internal/service internal/repository internal/app cmd; self-test ok)
make check-anchored-event-ids    → ✓ (C2 allowlist ok, self-test ok)
make build                       → ✓ bin/aero-id + bin/audit-replay built
make check-integration-compile   → ✓ (go test -tags integration -run '^$' ./... + go vet -tags integration ./...)
```

`make test-integration` (testcontainers/Docker) remains un-run (§6.2.2); integration-tagged tests compile green.

## 9. Amendment register (drifts found during this verification, relative to the input evidence)

| # | Evidence claim | Correction |
|---|---|---|
| A1 | Config keys at `configs/config.yaml:186-187,238` | Actual: `purge_pause` `:188`, `dead_letter_retention` `:189`, `outbox_dead_letter_retention` `:237`, `purge_paused` `:240` (comment/blank-line drift of ±2) |
| A2 | Validation at `internal/config/validate.go:211-216,222-233` | Actual: `validateAuditGovernance` `:217-224` (negative-retention check `:219`); `validateDataRetention` purge_paused-requires-cleaner `:260-263`. Function identity and behavior match |
| A3 | Migration `000045_create_audit_publications.up.sql` | Actual filename: `migrations/000045_audit_publications.up.sql`; schema contents match exactly |
| A4 | `selectDeadLetterRows` is shared across both kinds | Per-kind helpers: `selectDeadLetterRows` (`audit_publication_replay.go:74`) and `selectDeadLetterDeliveryRows` (`callback_audit.go:145-157`) — the shared helper is typed to `auditPublicationRow`; counts re-express the same predicate inline (D3) |
| A5 | Everything else (V1–V10) | Verified as cited, including all test pins, knob-copy lines, wiring gates, gate-script patterns, and the residual-delta list |

## 9b. Remediation amendments (2026-08-08 remediation plan, §7 — supersedes §2 D1/D7, §3.4, §4/§5 claims, §6.3)

| # | Register entry | Resolution in the implemented tree |
|---|---|---|
| A6 | Locked-selection restore (supersedes the unlocked drift) | `RequeuePublication`/`RequeueDead` select with `FOR UPDATE` (two-statement shape — the locked statement joins nothing, so ledger/subscription rows are never locked); the repo asserts `RowsAffected == len(locked selection)` in-tx and aborts on divergence |
| A7 | Per-tenant mandatory facts + `-actor` required (supersedes the optional-fact drift) | `ReplayIntent` (OperationID/ActorUID/TenantFilter/AllowedTenants/Envelope); zero/incomplete intent → `ErrAuditFactRequired` before any DB access; one fact per distinct affected tenant in-tx; `-actor` required in mutating mode; `TestAuditReplayDefaultInvocationRequiresActor` (inverted from the un-audited contract) |
| A8 | `-tenant` repurposed to selection filter | Facts derive tenant from the rows via `audit_ledger`/`callback_subscriptions` join; `-tenant tenant-unbound` over `tenant-a` rows selects nothing; boot-coverage backstop unreachable by construction (`TestAuditReplayUnboundTenantActsAsFilter`) |
| A9 | Purge outer guard + index + reorder (supersedes the §4/§5 claims) | Outer `AND status = 'dead_letter'` on both audit purge DELETEs + `FOR UPDATE SKIP LOCKED` subqueries + `(updated_at, id)` ordering; outbox dead-letter leg guard-only; migration `000059_add_dead_letter_purge_indexes` |
| A10 | Bounded dry-run count | `CountDeadLetterPublications`/`CountDeadLetterDeliveries` count the derived id-selection table (== next run's exact prediction); CLI prints `(dry run; capped at -limit N)`; `TestAuditReplayDryRunCountIsBounded` |
| A11 | C2 allowlist edit (mint moves into `internal/repository`) | Fresh v7 `EventID` mint per (run, leg, tenant) in `audit_publication_replay.go`/`callback_audit.go`; `C2_ALLOWLIST` updated; `check-anchored-event-ids` arg-loop bug fixed (every root argument scanned) |
| A12 | `-report` journal + stderr logfmt record restored | `writeJournal` (fsynced, per-leg) + `logReplayRun` (stderr logfmt); `TestAuditReplayLegResumeAfterPartial` |
