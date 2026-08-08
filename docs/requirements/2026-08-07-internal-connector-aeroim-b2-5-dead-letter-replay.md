# Requirements — B2-5 (module label `internal/connector/aeroim`): dead-letter replay for audit publications — `RequeuePublication`, `cmd/audit-replay`, lease/purge safety (T-4)

| | |
|---|---|
| Module (batch label) | `internal/connector/aeroim` — **label only**. The direction's change surface is `internal/repository` + `cmd/` (+ drain-verification in `internal/service` + `internal/app`). `internal/connector/aeroim/` is a 7-line JSON-summary connector adapter with no audit code; it is untouched by this direction. The analysis file is a campaign-batch replication of the audit-governance directions across module labels (same quirk documented by the sibling spec `2026-08-07-internal-connector-aeroim-b2-1-b2-2-in-tx-audit-single-delivery.md`) |
| Source analysis | `docs/auto/analyses/internal-connector-aeroim-087e0c23.json` (direction 3 of 3: B2-5) |
| Campaign | `aero-id-b2-5-dead-letter-replay` (gate G3: T-4). Coordinated sibling specs for the same direction under other labels: `2026-08-06-dead-letter-replay-configs-requirements.md` (full B2-5: publications + deliveries + `audit_governance.purge_pause`/`dead_letter_retention` + drain-loop purge scan WORKER-1 + metrics MET-1), `2026-08-07-outbox-purge-pause-config-requirements.md` (`data_retention.purge_paused` + outbox-cleaner pause + the T-5.4 SQL-race handoff) |
| Date | 2026-08-07 |
| Status | Evidence re-verified against head `13e4676` **plus the uncommitted working tree** (128 modified files; the B2-1+B2-2 migration has progressed past every sibling spec). The direction's gap claims all hold with line drift (A1–A7, §9). This spec owns the **audit-publication replay core**: `RequeuePublication`, `cmd/audit-replay`, and the lease/purge invariants that make acceptance (d) testable at the SQL boundary. The purge-scan and pause-flag *implementation* is sibling-owned (§3.3); this spec's acceptance (d) is a landing-gated combined check plus invariants testable today |

## 1. Problem statement (direction)

`dead_letter` is terminal for `audit_publications`: `ClaimPublication` selects only `pending`/`delivering`
rows (`internal/repository/audit_publication.go:42`) while `publicationOutcome` writes `dead_letter`
after max attempts (`internal/service/callback_audit.go:98`); there is no `RequeueDead`/
`RequeuePublication` method anywhere (repo-wide grep: zero hits), no `cmd/audit-replay` binary
(`cmd/` contains only `migrate` and `server`), and no purge-pause flag, so a dead-lettered audit fact
is never delivered to governance without manual SQL. Migration 000045
(`migrations/000045_audit_publications.up.sql`) has status text without a check constraint and
`uq_audit_publication_fact UNIQUE (fact_id)` (`:12`), which makes requeue idempotent by construction —
a requeue is an UPDATE of the single existing row per fact, never an INSERT. Constraint hazard:
`internal/repository/audit_ledger.go` sits near the 500-line gate (currently **442**), so new replay
methods must land in a sibling file (acceptance (e) fixes the name:
`internal/repository/audit_publication_replay.go`).

## 2. Verified evidence (head `13e4676` + working tree; line numbers live)

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `ClaimPublication` selects only `pending`/`delivering` — `dead_letter` never re-claimed (cited: `audit_ledger.go:246`) | ✅ holds, corrected location (**A1**): interface `AuditPublicationRepository` at `internal/repository/audit_ledger.go:42-44`; implementation `internal/repository/audit_publication.go:31-75`; the claim filter at `:42` is `((status = 'pending' AND next_attempt_at <= ?) OR (status = 'delivering' AND lease_until < ?))` — `dead_letter` rows are structurally unreachable. Lease default 5m (`:36`), limit default 50 (`:33`) |
| E2 | `publicationOutcome` writes `dead_letter` after max attempts (cited: `callback_audit.go:84-97`) | ✅ holds with drift (**A5**): `internal/service/callback_audit.go:90-101`; terminal `return "dead_letter", "audit.publication_failed", now.Add(retryDelay)` at `:98` when `attempt >= maxAttempts`. `finishPublicationAttempt` (`:68-88`) settles via `FinishPublication`, which clears the lease (`audit_publication.go:77-87`, guard `id = ? AND status = 'delivering' AND lease_owner = ?`) |
| E3 | No `RequeueDead`/`RequeuePublication` anywhere | ✅ `grep -rn "RequeueDead\|RequeuePublication" --include="*.go" .` → **0 hits**. The interface exposes only `ClaimPublication`/`FinishPublication` (`audit_ledger.go:42-44`) |
| E4 | No `cmd/audit-replay` binary | ✅ `cmd/` contains only `migrate/` and `server/` |
| E5 | No purge-pause flag | ✅ no `purge_paused`/`PurgePause` symbol anywhere; and `audit_publications` has **no purge/cleaner at all** today (no DELETE on the table; the outbox cleaner at `internal/outbox/repository.go:89-163` targets `outbox_events`, not `audit_publications` — the config-sibling's E9 correction). The flag lives in two sibling specs: `audit_governance.purge_pause` + `dead_letter_retention` (2026-08-06 spec CFG-1/CFG-2/WORKER-1) and `data_retention.purge_paused` (2026-08-07 spec CFG-1) |
| E6 | Migration 000045: status text without constraint; `uq_audit_publication_fact` per `fact_id` | ✅ `migrations/000045_audit_publications.up.sql` — `status text NOT NULL DEFAULT 'pending'` (no CHECK), `CONSTRAINT uq_audit_publication_fact UNIQUE (fact_id)` at `:12`; `audit_publications.fact_id REFERENCES audit_ledger(id) ON DELETE CASCADE` (`:4`) — deleting a publication row never deletes the ledger fact |
| E7 | `audit_ledger.go` near the 500-line gate (cited: 499) | ⚠️ drift (**A2**): **442 lines** today. The hazard stands; acceptance (e) mandates the sibling file regardless |
| E8 | Atomic outbox insert on append | ✅ `createAuditRows` (`audit_ledger.go:172-208`) inserts the ledger row **and** the `status='pending'` publication row in one tx (`:200`); constraint-precise 23505 mapping: only `uq_audit_ledger_tenant_event` translates to `ErrAuditDuplicate` (`:178-196`), a publication-site violation stays raw so the tx rolls back |
| E9 | Duplicate sentinels exist for idempotency contracts | ✅ `ErrAuditDuplicate` (`audit_ledger.go:49`), `ErrAuditPublicationLeaseLost` (`:50`); in-tx duplicate-swallow precedent `repository/callback_audit.go:36-40` (append last, swallow in-tx only) |
| E10 | Wiring treats `dead_letter` as durable backlog | ✅ `validateAuditBindingCoverage` (`internal/app/audit.go:134-146`) queries publications with `status IN ('pending','delivering','dead_letter')` (`:142`) — dead-lettered facts count toward governance binding coverage; a requeue fact with an unbound `tenant_id` would fail the next boot (design hazard, §10) |
| E11 | Fake governance endpoint precedent | ✅ `internal/connector/auditgovernance/publisher_test.go` — `httptest` server with `/token` + `/api/v1/events`; `TestPublisherConflictReceiptIsIdempotentSuccess` proves a conflict receipt (echoed `event_id`, `conflict: true`) is treated as idempotent success, never `ErrInvalidReceipt` → never re-retries a duplicated success into `dead_letter` |
| E12 | Test infrastructure | ✅ repository integration tests run on real Postgres via `testpg` (`audit_ledger_integration_test.go:19-25`, signing key `auditLedgerTestKey`); service-level publication fakes exist (`settlingPublicationRepo`/`failingPublicationRepo`, `callback_audit_test.go:32-58`); drain ticker at `callback.go:281`; `CallbackConfig.MaxAttempts` default 8 (`callback.go:74-76`), config `callback.max_attempts: 8` (`configs/config.yaml:202`) |

**Conclusion:** the direction's problem statement is accurate with line drift. Requeue surface,
CLI, and pause flag are all absent; the schema (`uq_audit_publication_fact`, text statuses) supports
idempotent requeue as a pure UPDATE with no migration.

## 3. Current state and residual delta

### 3.1 Landed (keep-enforced, no re-do)

- Terminal dead-lettering: `publicationOutcome` (`callback_audit.go:98`), lease-clearing `FinishPublication` (`audit_publication.go:77-87`).
- Single delivery path: `drainPublications` (`callback_audit.go:38-66`) ← ticker (`callback.go:281`) is the only `AuditPublisher.Publish` reachability; `make check-audit-swallow` green (P1 covers `ClaimPublication`/`FinishPublication`).
- Atomic append + outbox row (E8); `ErrAuditDuplicate`/`ErrAuditPublicationLeaseLost` sentinels (E9).
- Conflict-receipt idempotency in the governance publisher (E11) — the "receipt/conflict honored" half of acceptance (c) is already shipped and tested at the publisher level.

### 3.2 This spec's actionable scope

1. **`RequeuePublication`** on `AuditPublicationRepository`, implemented in the mandated sibling file `internal/repository/audit_publication_replay.go` (REQ-1).
2. **`cmd/audit-replay`** composition root (REQ-2), with run-twice exactly-once semantics (acceptance c).
3. **Drain-after-requeue verification** (acceptance b): no drain code change expected — `ClaimPublication` already claims freshly requeued `pending` rows — but the end-to-end path must be pinned by tests (REQ-3).
4. **Lease/purge invariants** that make acceptance (d) testable at the SQL boundary today, plus the real-Postgres `RequeuePublication`-vs-purge interleaving test the sibling specs hand off to this direction (REQ-4).

### 3.3 Sibling-owned (do not re-specify)

- `audit_governance.purge_pause` + `dead_letter_retention` config, drain-loop purge scan, purge/requeue metrics: `2026-08-06-dead-letter-replay-configs-requirements.md` (CFG-1/CFG-2/WORKER-1/MET-1).
- `data_retention.purge_paused` outbox-cleaner pause: `2026-08-07-outbox-purge-pause-config-requirements.md`.
- Callback-delivery requeue (`RequeueDead` on `CallbackRepository`, REPO-2): configs sibling, **out of scope here** (see A4).
- Requeue audit facts (AUD-1) and their `-actor`/`-dry-run` CLI flags: configs sibling. This spec's repo method accepts an **optional** fact (nil = no fact) so AUD-1 can land without an interface break; the CLI in this spec passes nil (acceptance-compatible; avoids the unbound-tenant boot hazard, §10).

## 4. Requirements

### REQ-1 — `RequeuePublication` on `AuditPublicationRepository`, in `internal/repository/audit_publication_replay.go`

1. **Signature.** Extend the interface (`internal/repository/audit_ledger.go:42-44`) with
   `RequeuePublication(ctx context.Context, ids []domain.ID, limit int, fact *domain.AuditFact) (int, error)`.
   Implement on `auditLedgerRepo` in the **new sibling file** `internal/repository/audit_publication_replay.go`
   (acceptance e; `audit_ledger.go` stays at 442 lines, `audit_publication.go` stays at 88).
2. **Selection.** `SELECT` rows with `status = 'dead_letter'` only, ordered `next_attempt_at ASC, id ASC`,
   bounded: non-empty `ids` → exactly that set (limit ignored); else `limit` (≤ 0 → default 100).
   `dead_letter` rows carry no lease (`FinishPublication` clears `lease_owner`/`lease_until`), so the
   status filter is also the lease guard: a row a drain worker holds as `delivering` can never be requeued.
3. **Reset.** In one transaction: `UPDATE ... SET status='pending', attempt=0, next_attempt_at=now(),
   lease_owner='', lease_until=NULL, error_code='manual_replay', updated_at=now() WHERE status='dead_letter' AND id IN (...)`.
   - **Attempt reset fixed to `0`** (acceptance allows "preserved or reset per spec"; reset gives a full
     fresh retry budget and matches the configs sibling REPO-1).
   - **Idempotent by construction:** the `status='dead_letter'` predicate makes a second call over the
     same rows affect 0 rows; `uq_audit_publication_fact` (migration 000045 `:12`) guarantees at most
     one publication row per fact, so there is no INSERT path to conflict on (a future backfill that
     inserts a missing row must use `ON CONFLICT (fact_id) DO NOTHING` — spelled out as an invariant,
     not implemented here).
4. **Optional in-tx audit fact** (WithAudit shape, callback precedent `repository/callback_audit.go:36-40`):
   when `fact != nil`, append via `AppendInTx` **after** the UPDATE, in the same transaction; append
   failure rolls the requeue back (no requeue without its fact); `ErrAuditDuplicate` is swallowed
   **in-tx only** — this is the acceptance (c) "ErrAuditDuplicate path". When `fact == nil`, no fact,
   no append (this spec's CLI passes nil; AUD-1 in the configs sibling passes a fact).
5. **Return** the number of rows requeued (UPDATE rows affected). Propagate raw DB errors (service/CLI translate).
6. No schema change; migration 000045 untouched; `check-audit-swallow` unaffected (no swallowed call — the
   append error propagates).

**Test:** §8 T-4.1; file-size/cyclomatic gates (§5).

### REQ-2 — `cmd/audit-replay` composition root

1. **Shape.** `cmd/audit-replay/main.go` following `cmd/migrate/main.go`: `main()` → `run(args []string) error`
   (testable entry), `flag.NewFlagSet`, `-config` default `configs/config.yaml`. Load via
   `internal/config.Load`; open DB via `internal/pkg/db.Open`; construct
   `repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: cfg.AuditLedger.SigningKey})`.
   **Fail closed:** require a non-empty signing key before any mutation (mirror
   `validateAuditLedgerConfig`, `internal/app/audit.go:23-31`; `AppendInTx` is fail-closed without a key).
2. **Flags.** `-limit` (default 100, bounded per run), `-id` (comma-separated publication row IDs;
   overrides `-limit`), `-dry-run`. (The configs sibling owns `-kind`, `-tenant`, `-actor`.)
3. **Behavior.** Non-dry-run: call `RequeuePublication` once (nil fact); print `requeued N publication(s)`;
   exit 0. `-dry-run`: run the SELECT only (same bounds/order), print counts with a `(dry run)` marker,
   mutate nothing. Non-zero exit on config/DB errors only.
4. **Run-twice semantics (acceptance c).** A second run with identical arguments requeues **0** rows
   (REQ-1.3 idempotency) and exits 0. Delivery of the requeued fact is the server drain worker's job
   (`ClaimPublication` picks the row up on the next tick); the CLI never publishes. Exactly-once delivery
   rests on the single-row-per-fact constraint + the single-claim lease protocol — no new machinery.
5. **Event-ID gate.** The CLI mints no event IDs in this spec's scope (nil fact). When AUD-1 lands with
   a requeue fact, the fresh v7 per run must be minted in `cmd/audit-replay` (C2 replay-intent rationale,
   `internal/service/callback.go` allowlist precedent; `scripts/check-anchored-event-ids.sh` scans
   `internal/` only, so `cmd/` is outside its root — the rationale, not the scan boundary, is the
   justification).

**Test:** §8 T-4.2; `make build` includes the new binary (`go build ./...`).

### REQ-3 — Drain-after-requeue delivery (acceptance b)

No change to `drainPublications`/`publishAuditFact`/`finishPublicationAttempt` expected — `ClaimPublication`
already claims `pending` rows with `next_attempt_at <= now` (E1). The requirement is **verification**:
a requeued publication must be claimed, delivered to the governance endpoint exactly once, and settled by
`FinishPublication(..., "completed", ...)` with the lease cleared. Keep-enforced: `publicationOutcome`
stays terminal at max attempts (no auto-requeue — recovery is operator-initiated via REQ-1/REQ-2), and
`drainPublications` remains the only `AuditPublisher.Publish` reachability (`make check-audit-swallow` green).

**Test:** §8 T-4.1(b)/T-4.2(c).

### REQ-4 — Lease/purge safety for replay (acceptance d)

Testable at the SQL boundary today, with the purge-scan landing gated on the siblings:

1. **Requeue cannot touch leased rows.** `RequeuePublication` predicates on `status='dead_letter'`
   (REQ-1.2); `ClaimPublication` predicates on `pending`-due/`delivering`-expired (E1). A row a replay or
   drain worker holds as `delivering` with a live `lease_until` is unreachable by both. Asserted as an
   SQL-boundary invariant (T-4.3 case 1).
2. **Purge predicate must exclude leased rows (handoff contract).** The sibling purge scan
   (2026-08-06 WORKER-1) already guards `lease_owner = ''`; this spec fixes the invariant: any
   `audit_publications` purge DELETE must be `WHERE status='dead_letter' AND (lease_owner = '') AND
   (lease_until IS NULL OR lease_until < now()) AND updated_at < cutoff`, bounded and keyset-ordered.
3. **Race test (this spec's obligation; the outbox-pause sibling's T-5.4 handoff item 2).**
   Real-Postgres interleaving test `TestRequeueVsPurgeInterleaving`: two connections; seed dead-letter
   rows past the retention bound; force both orders — (a) requeue then purge → requeue affects N, purge
   deletes 0 (rows no longer `dead_letter`); (b) purge then requeue → purge deletes N, requeue affects 0
   and succeeds (no error). Assert the audit fact always survives in `audit_ledger` (publication delete
   cascades only to the delivery record, never the fact — E6). Deterministic interleaving via explicit
   barriers (precedent: `audit_wiring_integration_test.go` concurrency cases); run under `-race` in CI.
4. **Pause-flag e2e (landing-gated).** When the sibling purge scan + `audit_governance.purge_pause`
   (and `data_retention.purge_paused`) land, the combined check runs: with the flag set, a purge pass
   deletes zero dead-letter rows; a replay run then requeues and delivers them; purge resumes only after
   the flag is cleared (restart with the flag unset — config is loaded once at boot). Until then, the
   pause-half of acceptance (d) is **deferred** to the siblings' landing and marked so in §8.

**Test:** §8 T-4.3.

### REQ-5 — Interface/compile impact is contained

Adding `RequeuePublication` to `AuditPublicationRepository` breaks the service-test fakes
(`failingPublicationRepo`/`settlingPublicationRepo`, `callback_audit_test.go:32-58`): extend both with a
recording no-op implementation (fail the test if invoked). No other implementers exist (wiring assertion
`wireAuditServices`, `internal/app/audit.go:69-92`).

### REQ-6 — No schema, API, or config changes from this spec

No migration files, no HTTP/gRPC/proto surfaces, no new config keys (flags live in the siblings' specs),
no new metrics (MET-1 is the configs sibling's). The requeue `error_code='manual_replay'` is an existing
free-text column — no constraint change.

## 5. Compatibility constraints

- **Wire/proto/API:** none touched. `POST /v1/callback/deliveries/:id/replay` keeps its owner-scoped
  single-delivery behavior; the operator replay surface is `cmd/audit-replay` only.
- **Attempt accounting:** requeue resets `attempt` to 0 (REQ-1.3) — the only behavioral difference from
  the existing `ResetDeliveryWithAudit` (which preserves attempt, E12-adjacent); publication dead-letter
  thresholds and `ClaimPublication`'s `attempt+1` per claim are unchanged.
- **Idempotency:** `uq_audit_publication_fact` + the status predicate make re-runs no-ops; the conflict
  receipt path (`validateReceipt`, publisher) already treats a duplicate governance acceptance as
  success (E11) — no publisher change.
- **File-size/cyclomatic gates:** `audit_publication_replay.go` ≤ 500 lines (target ≈ 120), functions
  ≤ 50 lines, cyclomatic ≤ 10; `audit_ledger.go` stays at 442, `audit_publication.go` at 88.
- **`make check-audit-swallow`:** the optional in-tx append is a checked call; P1/P2 must stay green
  (no `_ = ...AppendInTx`, no bare `FinishPublication(`).
- **`make check-anchored-event-ids`:** no `idgen.NewID()` added inside `internal/` by this spec (nil fact
  in the CLI; the repo method never mints event IDs).
- **`0 = disabled` convention:** unchanged; `limit <= 0` defaults are code defaults, documented above.

## 6. Failure modes

| Failure | Current behavior | After REQ-1/REQ-2 |
|---|---|---|
| Governance endpoint permanently down | Fact dead-letters after `MaxAttempts`, unrecoverable except manual SQL | Operator runs `cmd/audit-replay`; row → `pending`, drain retries with a full fresh budget |
| Replay run twice (operator error) | — | Second run affects 0 rows, exit 0; fact delivered exactly once (unique `fact_id` + lease protocol) |
| Replay fact append fails (AUD-1, optional) | — | Whole requeue tx rolls back (append last); no requeue without its fact; `ErrAuditDuplicate` folds in-tx without rollback (REQ-1.4) |
| Purge races replay (post-sibling-landing) | Rows deleted between replay's SELECT and UPDATE; delivery record lost | Pause flag stops the purge; lease guard excludes `delivering`; the ledger fact itself survives any publication delete (E6) — recovery = a re-run of `cmd/audit-replay` (no backfill needed for the fact) |
| Signing key unconfigured at CLI | — | `cmd/audit-replay` fails closed before any mutation (REQ-2.1) |

## 7. Migration steps (ordered)

1. **Baseline proof:** `grep -rn "RequeueDead\|RequeuePublication" --include="*.go" .` → 0 hits; `ls cmd/` → `migrate server`; `wc -l internal/repository/audit_ledger.go` → 442.
2. **Repo (REQ-1):** extend the interface (`audit_ledger.go:42-44`); add `internal/repository/audit_publication_replay.go` (select/update/optional fact; §4 REQ-1). Update the two service-test fakes (REQ-5).
3. **CLI (REQ-2):** `cmd/audit-replay/main.go` (flags `-limit`, `-id`, `-dry-run`, `-config`; fail-closed key check; `run(args) error`).
4. **Tests:** T-4.1 repository integration (red before step 2: no symbol; green after); T-4.2 CLI e2e; T-4.3 invariants + race test.
5. **Gate:** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make build`; `make test-integration` **required** (repository SQL boundary + new race test).
6. **Handoff:** re-run T-4.3 case 4 when the sibling purge scan + pause flags land; the combined e2e from the outbox-pause spec's T-5.4 closes acceptance (d) end-to-end.

Rollback: revert steps 2-3 and the new tests; no data migration exists.

## 8. Testable acceptance mapping (supplied T-4, preserved verbatim)

> **T-4 (supplied):** (a) RequeueDead moves dead_letter rows to pending with next_attempt_at=now, attempt preserved or reset per spec, and is idempotent (unique fact_id constraint); (b) after requeue, drainPublications delivers the fact to a fake governance endpoint and FinishPublication completes; (c) cmd/audit-replay run twice publishes the fact exactly once (receipt/conflict honored, ErrAuditDuplicate path); (d) purge pause flag blocks purge of dead_letter rows while a replay worker holds the lease; (e) new file internal/repository/audit_publication_replay.go stays under 500 lines.

| Supplied acceptance | Requirement | Testable criteria (all runnable) | Status today |
|---|---|---|---|
| **(a)** "RequeueDead" moves `dead_letter` rows to `pending` with `next_attempt_at=now`, attempt preserved or reset per spec, idempotent (unique `fact_id`) | REQ-1 | Naming note (**A4**): the unique-`fact_id` constraint pins this check to `audit_publications`, so the implemented symbol is **`RequeuePublication`** (the callback-delivery `RequeueDead` is the configs sibling's REPO-2). **T-4.1** (`internal/repository/audit_publication_replay_integration_test.go`, testpg): drive a fact to `dead_letter` through the public surface only — `AppendInTx` → `ClaimPublication` → `FinishPublication(..., "dead_letter", "audit.publication_failed", ...)` with a failing publisher (max 3 attempts via `CallbackConfig{MaxAttempts: 3}`), then: (1) `RequeuePublication` → row `status='pending'`, `attempt=0` (**reset**, fixed per spec §4 REQ-1.3), `next_attempt_at` ≈ now, `lease_owner=''`, `lease_until=NULL`, `error_code='manual_replay'`; (2) second call over the same rows → returns **0**, row unchanged; (3) exactly **one** row per `fact_id` before and after (unique constraint; assert `count(*)` per fact = 1); (4) rows in `pending`/`delivering`/`completed` statuses untouched; (5) `-dry-run`-equivalent SELECT path mutates nothing | Red today (no symbol); green after REQ-1 |
| **(b)** after requeue, `drainPublications` delivers to a fake governance endpoint and `FinishPublication` completes | REQ-3 | **T-4.1(b)** (integration): continue the T-4.1 setup — after requeue, run `drainPublications` against a callback service wired with a governance publisher pointed at an `httptest` server (`/token` + `/api/v1/events`, publisher_test.go precedent E11): assert exactly **1** event POST carrying the fact's `event_id`; then `FinishPublication` settles the row to `completed` with `error_code=''`, lease cleared, `ObserveAuditPublicationOutcome("completed")` observed (metric spy, `callback_audit_test.go` precedent). Service-level complement with `settlingPublicationRepo`-style fakes for the claim→publish→finish ordering | Drain path already correct (E1); the requeue→deliver e2e is red today (no requeue); green after REQ-1/REQ-3 |
| **(c)** `cmd/audit-replay` run twice publishes the fact exactly once (receipt/conflict honored, `ErrAuditDuplicate` path) | REQ-1.4, REQ-2 | **T-4.2** (integration, `cmd/audit-replay/main_test.go` + testpg): seed N ≥ 1 dead-letter publications (public surface). (1) `run([]string{"-limit","K"})` with K ≥ N → prints `requeued N publication(s)`, exit 0; (2) re-run identical → `requeued 0`, exit 0; (3) one drain cycle → each fact delivered **exactly once** to the fake governance endpoint (per-fact POST counter = 1), rows `completed`. **Receipt/conflict honored:** a conflict receipt (`conflict: true`, echoed `event_id`) from the fake endpoint is an idempotent success — the drain settles `completed` without a retry and never re-marks `dead_letter` (publisher-level test E11 exists; assert at drain level here). **ErrAuditDuplicate path** (repo-level, T-4.1 case 6): `RequeuePublication` with a supplied fact whose `event_id` collides with an existing ledger row → in-tx swallow, no error, requeue still commits, exactly 1 ledger row for that event; with a colliding fact the second run produces no duplicate (dedup + unique `fact_id`) | Red today (no CLI); green after REQ-1/REQ-2 |
| **(d)** purge pause flag blocks purge of `dead_letter` rows while a replay worker holds the lease | REQ-4 | **T-4.3**: (1) **lease invariant (runnable now):** a `delivering` row with a live `lease_until` is unreachable by `RequeuePublication` (status predicate) and by `ClaimPublication`; asserted with a row mid-lease. (2) **race test (runnable now, this spec's obligation):** `TestRequeueVsPurgeInterleaving` — both orders of requeue/purge on the same rows; requeue never errors on already-purged rows; purge never deletes a requeued row; the `audit_ledger` fact always survives. (3) **pause-flag e2e (landing-gated on the siblings' purge scan + flags):** `purge_pause=true`/`purge_paused=true` → purge pass deletes **zero** dead-letter rows; replay then requeues and delivers; after the flag is cleared (restart), past-retention rows purge. Until the siblings land, case 3 is recorded as **deferred** (status column: deferred, contract fixed in REQ-4.2/REQ-4.4) | Invariants testable today; e2e deferred to combined landing |
| **(e)** new file `internal/repository/audit_publication_replay.go` stays under 500 lines | REQ-1 | `make check-file-sizes` green; target ≈ 120 lines; functions ≤ 50 lines, cyclomatic ≤ 10 | Red today (file absent); green after REQ-1 |

## 9. Amendment register (corrections to the source analysis)

| # | Stale wording (direction evidence) | Correction | Folded into |
|---|---|---|---|
| A1 | `ClaimPublication` filter at `internal/repository/audit_ledger.go:246` | Interface at `audit_ledger.go:42-44`; implementation + filter at `internal/repository/audit_publication.go:31-75` (`:42`). Substance holds: `dead_letter` excluded | §2 E1 |
| A2 | `audit_ledger.go` "already 499 lines" | **442 lines** at head + working tree; gate hazard stands, sibling file mandated by acceptance (e) | §2 E7 |
| A3 | "no purge-pause flag" | Confirmed absent; additionally `audit_publications` has **no purge at all** today — acceptance (d) is partly a landing-gated check; the flags are sibling-owned (two specs) | §2 E5, §3.3, REQ-4.4 |
| A4 | Acceptance (a) names `RequeueDead` | The unique-`fact_id` constraint pins (a) to `audit_publications`; the implemented symbol is `RequeuePublication`. Callback-delivery `RequeueDead` (REPO-2) is the configs sibling's scope | §8(a) |
| A5 | `publicationOutcome` at `callback_audit.go:84-97` | `:90-101`; terminal `dead_letter` return at `:98` | §2 E2 |
| A6 | Module label `internal/connector/aeroim` as change surface | Label-only replication; the aeroim connector (7-line JSON-summary adapter) is untouched; surface is `internal/repository` + `cmd/` (+ verification in `internal/service`/`internal/app`) | header, §1 |
| A7 | "attempt preserved or reset per spec" (open) | Fixed: **reset to 0** (full fresh retry budget; consistent with configs sibling REPO-1) | REQ-1.3, §8(a) |

## 10. Open decisions (low risk)

1. **RESOLVED:** attempt reset to 0 (A7).
2. **RESOLVED:** requeue fact optional (`fact *domain.AuditFact`); this spec's CLI passes nil. When the configs sibling's AUD-1 lands, the fact's `tenant_id` must be chosen so `validateAuditBindingCoverage` (`internal/app/audit.go:134-146`) does not fail the next server boot for an unbound tenant — flag the sibling: scope the replay fact's tenant to the requeued rows when they share one, or add a binding for the operator tenant; the `ErrAuditDuplicate` in-tx fold (REQ-1.4) already makes a deterministic-anchor variant safe.
3. **RESOLVED:** `-limit` default 100 per run (bounded; consistent with configs sibling CLI-1).

---

### Done-gate relevance

This spec touches repository SQL-adjacent code and a new `cmd/*` binary: the implement stage must run
`make test-integration` once (T-4.1/T-4.2/T-4.3 use real Postgres) and keep
`make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow &&
make check-anchored-event-ids && make build` green. Cross-data-center/load gates are unaffected.
