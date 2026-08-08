# Requirements specification — dead-letter replay for audit publications and callback deliveries, with purge pause flag

> Module: `configs` (direction anchored in the `audit_governance` config block; spans
> `internal/config`, `internal/repository`, `internal/service`, `cmd/`).
> Source analysis: `docs/auto/analyses/configs-1fa2a926.json` (item 2, "Add dead-letter replay:
> RequeueDead/RequeuePublication, cmd/audit-replay tool, and purge pause flag (B2 item 5)").
> Date: 2026-08-06. Status: evidence verified against the repository at HEAD.

## 1. Direction

Dead-lettering exists only as a terminal status with no recovery path for audit publications and no
bulk/operator recovery path for callback deliveries; the drain loop cannot be paused during
maintenance. Required: `RequeueDead`/`RequeuePublication` repository methods, a `cmd/audit-replay`
composition root, and a purge pause flag in the `configs/config.yaml` `audit_governance` block.

## 2. Evidence verification

Every citation from the source analysis was re-checked against the repository. Two line numbers are
corrected (the cited substance holds; see rows marked "corrected").

| # | Claim | Verified location | Status |
|---|-------|-------------------|--------|
| E1 | `publicationOutcome` marks audit publications `dead_letter` after `MaxAttempts` | `internal/service/callback_audit.go:97` — `return "dead_letter", "audit.publication_failed", ...` inside `publicationOutcome` (function spans lines 86–101; citation said 85–91) | Confirmed (corrected) |
| E2 | Callback deliveries are marked `dead_letter` after `MaxAttempts` | `internal/service/callback.go:324` — `status = "dead_letter"` in `markDelivery` when `delivery.Attempt+1 >= s.cfg.MaxAttempts` (citation said 305–309) | Confirmed (corrected) |
| E3 | No `RequeuePublication` method anywhere in `internal/repository` | `internal/repository/audit_ledger.go:42-44` — `AuditPublicationRepository` interface exposes only `ClaimPublication` and `FinishPublication`. Row struct is `auditPublicationRow` at `internal/repository/audit_publication.go:14` (citation said audit_ledger.go:103). Repo-wide `rg "Requeue|requeue"` finds no method | Confirmed (corrected) |
| E4 | No `RequeueDead` method | `internal/repository/callback.go:19-30` — `CallbackRepository` exposes `CreateSubscription`, `GetSubscription`, `ListSubscriptions`, `DeleteSubscription`, `ListEnabledByAction`, `EnqueueDelivery`, `FetchDueDeliveries`, `MarkDelivery`, `ResetDelivery` (plain single-row reset), `GetDelivery` — no bulk requeue. `internal/repository/callback_audit.go:17-24` has `CreateSubscriptionWithAudit`, `DeleteSubscriptionWithAudit`, `ResetDeliveryWithAudit` — no bulk requeue | Confirmed |
| E5 | `cmd/` contains only `migrate` and `server` | `cmd/migrate/main.go`, `cmd/server/main.go` — no `cmd/audit-replay` | Confirmed |
| E6 | No purge/pause flag in `configs/config.yaml` `audit_governance` block | `configs/config.yaml:174-186` — block keys: `enabled, endpoint, source_prefix, actor_id, schema_id, schema_version, data_classification, retention_class, timeout, refresh_skew, allow_insecure_loopback, bindings`. No `purge_pause`. Struct `AuditGovernanceConfig` at `internal/config/audit_governance.go:7-20` has no `PurgePause` field | Confirmed |
| E7 | A DLQ'd audit publication is unrecoverable except by manual SQL | `internal/repository/audit_publication.go:31-44` — `ClaimPublication` claims only `(status='pending' AND next_attempt_at <= now) OR (status='delivering' AND lease_until < now)`; `dead_letter` rows are never re-claimed. No publication replay surface exists | Confirmed |
| E8 | A DLQ'd callback delivery is unrecoverable except by manual SQL (bulk/operator path) | `internal/repository/callback.go:171-190` — `FetchDueDeliveries` uses the same status filter. **Nuance:** a per-delivery, owner-scoped replay exists — `ReplayDelivery` (`internal/service/callback.go:237-259`) → `ResetDeliveryWithAudit` (`internal/repository/callback_audit.go:75-93`) via `POST /v1/callback/deliveries/:id/replay` (`internal/handler/http/callback.go:29`). It covers one delivery, requires actor/tenant context, and does **not** reset `attempt`; it has no publication equivalent and no bounded-batch operator surface | Confirmed with nuance |
| E9 | Replay cannot be paused during maintenance | `internal/service/callback.go:269-289` — `Start` ticker calls `drainPublications` + `drain` unconditionally; neither has a purge/dead-letter scan nor a pause switch. No `purge` symbol exists in `internal/service` (only the legacy `audit_events` retention purge referenced at `internal/service/audit_hold.go:15` and the outbox dead-letter retention worker at `internal/app/new.go:345-348`) | Confirmed |
| E10 | Idempotency backbone exists for exactly-once re-publish | `migrations/000045_audit_publications.up.sql:12` — `UNIQUE (fact_id)`; `migrations/000036_create_callbacks.up.sql` — `UNIQUE (subscription_id, event_id)`; `EnqueueDelivery` uses `clause.OnConflict{DoNothing: true}` (`internal/repository/callback.go:163`) | Confirmed |
| E11 | Wiring point for config → drain loop | `internal/app/audit.go:53-58` — `buildAuditStack` constructs `CallbackConfig` from `cfg.Callback.*`; `internal/app/audit.go:81-92` — `wireAuditServices` attaches the publication repo and publishers. `internal/config/config.go:27` — `AuditGovernance` field; `internal/config/config.go:374` — `Load` | Confirmed |
| E12 | Existing attempt semantics | Publications: `ClaimPublication` increments `attempt` per claim (`internal/repository/audit_publication.go:49`); deliveries: `MarkDelivery` increments `attempt` (`internal/repository/callback.go:221-226`), `ResetDeliveryWithAudit` does **not** reset it (`internal/repository/callback_audit.go:79-85`); `dead_letter` thresholds compare against `MaxAttempts` | Confirmed |

**Conclusion:** the direction's problem statement is accurate. Audit-publication DLQ is fully terminal
(E7); delivery DLQ is terminal for the drain loop and only owner-scoped single-row replay exists (E8);
no requeue repository methods, no replay CLI, and no pause flag exist (E3–E6).

## 3. Gap summary

| Capability | Today | Required |
|---|---|---|
| Recover DLQ'd audit publication | Manual SQL only | `RequeuePublication` repo method + `cmd/audit-replay` |
| Recover DLQ'd callback delivery (operator, bounded) | Owner-scoped single-row `ReplayDelivery` (no attempt reset) | `RequeueDead` repo method + `cmd/audit-replay` |
| Pause purge/dead-letter scanning during maintenance | Not possible | `audit_governance.purge_pause` flag honored by the drain loop |
| Dead-letter purge | None (drain loop has no purge scan) | Bounded retention-based purge scan in the drain loop, gated by `purge_pause` |
| Requeue auditability | Single-delivery replay writes a fact (`ResetDeliveryWithAudit`) | Bulk requeue appends facts in the same transaction (precedent-consistent) |

## 4. Requirements

### 4.1 Config (module anchor)

**CFG-1 — `purge_pause` flag.**
Add `purge_pause bool` to `AuditGovernanceConfig` (`internal/config/audit_governance.go:7-20`) with
`mapstructure:"purge_pause"`, default `false`, and document it in the `audit_governance` block of
`configs/config.yaml:174-186` (e.g. `purge_pause: false  # maintenance: pause dead-letter purge/dead-letter scanning in the drain loop`).
Semantics: when `true`, the background drain loop skips the dead-letter purge scan entirely — no
`dead_letter` row is selected, deleted, or re-marked. Rows are never lost by the paused scan. Environment
override follows the existing `AERO_ID_*` mechanism. *Acceptance: T-4.3.*

**CFG-2 — purge retention bound (supporting key for the purge half).**
Add `dead_letter_retention time.Duration` to `AuditGovernanceConfig`, default `0` meaning *never purge*
(repo convention: `0 = disabled`), documented in `configs/config.yaml`. The drain-loop purge scan deletes
`dead_letter` rows only when retention is `> 0` and the row's `updated_at`/`next_attempt_at` is older than
the bound. Add validation in `internal/config/validate.go` (a `validateAuditGovernance` function; none
exists today — only `validateAuditLedger` at `validate.go:196-205`): reject negative retention; no other
new validation. *Acceptance: T-4.3 (negative-retention rejection asserted by config unit test).*

### 4.2 Repository

**REPO-1 — `RequeuePublication`.**
Add to the `AuditPublicationRepository` interface (`internal/repository/audit_ledger.go:42-44`) and
implement on `auditLedgerRepo` (extend `internal/repository/audit_publication.go`). Contract:
- Selects only rows with `status = 'dead_letter'` (bounded set: caller-supplied IDs and/or `LIMIT`,
  ordered `next_attempt_at ASC, id ASC`); rows in any other status are untouched.
- Resets the row to `status = 'pending'`, `attempt = 0`, `next_attempt_at = now`, clears
  `lease_owner`/`lease_until`, sets `error_code = 'manual_replay'`.
- Idempotent: a second call over the same rows affects zero rows (status filter), so re-running the
  replay never double-requeues.
- Returns the number of rows requeued; the caller (CLI) prints it.
- Follows the `WithAudit` pattern (precedent: `ResetDeliveryWithAudit`,
  `internal/repository/callback_audit.go:75-93`): when a fact is supplied, the reset UPDATE and the
  ledger append commit in one transaction; an append failure rolls the reset back.
- No schema change: `dead_letter`/`pending` are existing text statuses; applied migrations are never
  edited.

*Acceptance: T-4.1, T-4.2.*

**REPO-2 — `RequeueDead`.**
Add to the `CallbackRepository` interface (`internal/repository/callback.go:19-30`) and implement on
`callbackRepo` (extend `internal/repository/callback.go` or a sibling file, ≤500 lines per gate).
Contract: identical to REPO-1 for `callback_deliveries` — `status='dead_letter'` → `pending`,
`attempt = 0`, fresh `next_attempt_at`, lease cleared, `error_code = 'manual_replay'`; idempotent;
bounded; optional in-tx audit fact (the repo already embeds the audit ledger and implements
`CallbackAuditRepository`). Unlike the existing `ResetDeliveryWithAudit`, this method **resets
`attempt`** so a requeued delivery gets a full fresh retry budget. *Acceptance: T-4.1, T-4.2.*

### 4.3 CLI composition root

**CLI-1 — `cmd/audit-replay` binary.**
New composition root `cmd/audit-replay/main.go` following the `cmd/migrate/main.go` shape (flags,
`run(args) error` entry point for testability, `-config` default `configs/config.yaml`, config via
`internal/config.Load` at `internal/config/config.go:374`, DB via `internal/pkg/db`, repository
constructed like `buildAuditStack` at `internal/app/audit.go:34-66`). Flags: `-kind` (`publication` |
`delivery` | `both`, default `both`), `-limit` (bounded set per kind, default 100), `-id`
(repeatable/comma-separated explicit row IDs; overrides the limit for that kind), `-tenant` (optional
filter), `-actor` (operator identity recorded in requeue facts, default `audit-replay`), `-dry-run`.
Exit 0 on success with a printed count of requeued rows per kind; non-zero on DB/config errors. This
makes the replay tool part of the wiring surface (only `cmd/migrate` and `cmd/server` exist today,
E5). *Acceptance: T-4.2.*

**CLI-2 — bounded, idempotent requeue; exactly one re-publish each.**
The tool requeues at most `-limit` rows per kind (or the exact `-id` set), ordered
`next_attempt_at ASC, id ASC`. Re-running the tool with the same IDs/limit requeues nothing new
(REPO-1/REPO-2 status filter). After requeue, each row is re-published exactly once by the drain loop:
guaranteed by the existing unique constraints (`uq_audit_publication_fact`,
`uq_callback_delivery_event`) and the single-claim lease protocol (`ClaimPublication`/
`FetchDueDeliveries`), not by new machinery. *Acceptance: T-4.2.*

**CLI-3 — dry-run mode (derived from the audit contract's replay modes).**
`-dry-run` lists and counts the rows that would be requeued without mutating rows and without appending
audit facts; it returns the same counts with a `(dry run)` marker. *Acceptance: T-4.2 (asserts rows
remain `dead_letter`).*

### 4.4 Drain-loop purge scan and pause

**WORKER-1 — purge/dead-letter scan in the drain loop, gated by `purge_pause`.**
Extend the drain loop (`Start` at `internal/service/callback.go:269-289`, per tick after
`drainPublications`/`drain`) with a bounded dead-letter purge scan:
- Scans `audit_publications` and `callback_deliveries` rows with `status = 'dead_letter'` whose age
  exceeds CFG-2's retention bound, in bounded batches (per-kind limit consistent with the existing
  claim limits of 50), ordered `next_attempt_at ASC, id ASC`, keyset-style; guards `lease_owner = ''`.
- When retention is `0` (default), the scan is a no-op — dead letters are only ever removed by an
  explicit purge bound, never by default.
- When `purge_pause` is `true`, the scan is skipped entirely (no SELECT, no DELETE, no row mutation);
  rows are preserved bit-for-bit (status, attempt, error_code, next_attempt_at unchanged).
- Delivery of the flag and bound: add `PurgePause bool` and `DeadLetterRetention time.Duration` to
  `CallbackConfig` (`internal/service/callback.go:50-53`) and populate them from `cfg.AuditGovernance`
  in `buildAuditStack` (`internal/app/audit.go:53-58`).

*Acceptance: T-4.3.*

**MET-1 — observability.**
Extend `ControlPlaneMetrics` (`internal/service/control_plane_metrics.go:12-23`) with purge/requeue
counters (e.g. dead-letter purged per kind and requeued per kind); update the noop and registry
implementations (`control_plane_metrics.go:31-33, 60-66`). The existing `ObserveCallbackOutcome("dead_letter")`
behavior is unchanged. *Acceptance: unit assertion that purge-skip produces no purge metric emission.*

### 4.5 Requeue auditability (derived from AGENTS.md, precedent-consistent)

**AUD-1 — requeue writes audit facts.**
AGENTS.md requires every replay/callback-management operation to produce a durable audit fact, and the
shipped single-delivery replay already does so (`ResetDeliveryWithAudit` appends
`callback.delivery.replayed`). The bulk requeue must match: each requeue run appends one fact per kind
in the same transaction as the resets (REPO-1/REPO-2), action `audit.publication.requeued` /
`callback.delivery.requeued`, payload listing requeued row IDs and count, `actor_uid` from `-actor`,
and a **fresh** `event_id` per requeue intent (`idgen.NewID()`) — the C2 precedent documented at
`internal/service/callback.go:249-256` (a deterministic anchor would collide on a second requeue of a
re-failed row and roll the reset back via `ErrAuditDuplicate`). *Acceptance: T-4.1 (ledger row
asserted).*

## 5. Acceptance

The supplied acceptance check is preserved verbatim and decomposed into testable cases:

> **T-4 (supplied):** integration test where a publication and a delivery reach `dead_letter`,
> `RequeueDead`/`RequeuePublication` resets them to pending with a fresh `next_attempt_at` and resets
> attempts, `cmd/audit-replay` requeues a bounded set by idempotency key producing exactly one
> re-publish each, and setting `purge_pause=true` in config makes the drain loop skip purge/dead-letter
> scanning without losing rows.

**T-4.1 — repository requeue semantics (integration, Postgres; `internal/repository/*_integration_test.go`
pattern).**
Setup: drive rows to `dead_letter` through the public surface only — publication: `ClaimPublication` +
`FinishPublication(..., "dead_letter", "audit.publication_failed", ...)`; delivery: `EnqueueDelivery` +
`FetchDueDeliveries` + `MarkDelivery(..., "dead_letter", ...)` (E2/E12 thresholds apply; no new
injection). Assert after `RequeuePublication`/`RequeueDead`:
- status = `pending`, `attempt` = 0, `next_attempt_at` ≈ now (fresh; allow clock skew), lease
  owner/until cleared, `error_code` = `manual_replay`;
- the requeued rows are claimable by `ClaimPublication`/`FetchDueDeliveries` in the next drain pass;
- a second call over the same rows returns 0 rows affected (idempotency key = row identity);
- a supplied audit fact was appended in the same transaction (ledger row exists with the requeue
  action; a forced append failure rolls the reset back — inject via the same mechanism as existing
  rollback tests, e.g. signing-key-unset, never duplicate-event-id injection per C2);
- rows in non-`dead_letter` statuses are untouched.

**T-4.2 — `cmd/audit-replay` end-to-end (integration).**
Setup: seed N `dead_letter` publications and M `dead_letter` deliveries (public surface, as T-4.1).
Run `run([]string{"-kind", "both", "-limit", "K", ...})` with K < N and K < M:
- exactly K publications and K deliveries requeued (bounded set), counts printed, exit 0;
- re-run with the same arguments: 0 additional rows requeued;
- run one drain cycle; assert each requeued publication is re-published exactly once (one
  `callback_deliveries` row per `(subscription_id, event_id)` via `EnqueueDelivery`'s
  `OnConflict{DoNothing}` and one publication row per `fact_id` via the unique constraint) and each
  requeued delivery is delivered exactly once (single claim per lease);
- `-id` with explicit IDs requeues exactly that set; `-dry-run` changes nothing (rows remain
  `dead_letter`, no audit facts appended).

**T-4.3 — purge pause flag (integration for the drain loop; unit for config).**
Setup: `dead_letter` rows with age exceeding the configured retention bound, `purge_pause: true` in the
loaded config; run drain-loop ticks:
- no purge scan runs: rows remain present with unchanged `status`, `attempt`, `error_code`,
  `next_attempt_at` (nothing lost);
- `purge_pause: false` with retention `> 0`: rows past the bound are purged in bounded batches; rows
  within the bound and non-`dead_letter` rows are kept;
- `purge_pause: false` with retention `0`: nothing is ever purged (default safety);
- config unit tests: `purge_pause` parses (default false), negative `dead_letter_retention` is rejected
  at validation.

**T-4.4 — regression (unit).**
`publicationOutcome` still returns `dead_letter` at `attempt >= maxAttempts` and `markDelivery` still
marks `dead_letter` at `delivery.Attempt+1 >= MaxAttempts`; `ReplayDelivery` behavior unchanged.

**T-4.5 — wiring (build/unit).**
`cmd/audit-replay` builds and is reachable from `make build` (`go build ./...`); `buildAuditStack`
populates `PurgePause`/`DeadLetterRetention` from the loaded config.

## 6. Non-goals (scope boundary)

- No new HTTP/admin API for requeue: `POST /v1/callback/deliveries/:id/replay` keeps its current
  owner-scoped behavior; the operator surface is `cmd/audit-replay` only.
- No changes to the live dead-lettering thresholds or attempt accounting (E12 unchanged).
- No changes to the legacy `audit_events` retention purge (`internal/service/audit_hold.go:15`) or the
  outbox dead-letter retention worker (`internal/app/new.go:345-348`).
- No schema migration: no new columns/tables; applied migrations are not edited.
- No changes to `EnqueueDelivery`/`publishAuditFact` dedup machinery (existing unique constraints
  suffice).
- No automatic requeue: recovery is operator-initiated (the direction's pause semantics forbid the
  drain loop from requeueing on its own).

## 7. Impact map

| File | Change |
|---|---|
| `configs/config.yaml:174-186` | add `purge_pause: false`, `dead_letter_retention: 0` to `audit_governance` |
| `internal/config/audit_governance.go` | add `PurgePause`, `DeadLetterRetention` fields |
| `internal/config/validate.go` | add `validateAuditGovernance` (negative retention rejected) |
| `internal/repository/audit_ledger.go:42-44` | extend `AuditPublicationRepository` with `RequeuePublication` |
| `internal/repository/audit_publication.go` | implement `RequeuePublication` (WithAudit, in-tx fact) |
| `internal/repository/callback.go:19-29` | extend `CallbackRepository` with `RequeueDead` |
| `internal/repository/callback_audit.go` (or sibling) | implement `RequeueDead` (WithAudit, attempt reset) |
| `internal/service/callback.go:50-53,269-289` | `CallbackConfig.PurgePause`/`DeadLetterRetention`; purge scan in `Start` tick |
| `internal/service/control_plane_metrics.go` | purge/requeue counters (noop + registry) |
| `internal/app/audit.go:53-58` | populate new `CallbackConfig` fields from `cfg.AuditGovernance` |
| `cmd/audit-replay/main.go` | new composition root |
| tests | `internal/repository/*_integration_test.go`, `internal/service/*_test.go`, `internal/config/*_test.go`, `cmd/audit-replay/*_test.go` |

## 8. Engineering constraints

- New non-test files ≤ 500 lines, functions ≤ 50 lines, cyclomatic ≤ 10, package average ≤ 5.0
  (`make lint-strict`, `make check-file-sizes`); split `cmd/audit-replay` logic into sibling files if
  needed.
- Unit tests use deterministic fakes (no network/DB); repository and drain-loop tests are integration-
  tagged against real PostgreSQL; run `make test-integration` (repository SQL, source connectors, and
  multi-service boundaries are touched).
- `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make build`
  must stay green; the requeue fact append must not use the swallowed-call pattern.
- Generated protobuf untouched; no `.proto` changes.

## 9. Open decisions (low risk, implementer choice)

1. Default `-limit` for `cmd/audit-replay` (spec suggests 100 per kind; must be bounded).
2. Exact metric names for purge/requeue counters (must be added to noop + registry implementations).
3. Whether the purge scan batches over both kinds per tick or alternates; either is acceptable as long
   as it is bounded and keyset-ordered.
