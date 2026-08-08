# Design — Dead-Letter Replay Surface for `audit_publications` + `callback_deliveries` (B2-5)

Direction: **Implement the missing dead-letter replay surface for audit_publications (B2-5): RequeueDead/RequeuePublication + cmd/audit-replay + purge-pause flag**

Module: `cmd/migrate` (direction spans `cmd/audit-replay`, `internal/repository`, `internal/service`, `internal/config`, `scripts/check-audit-swallow.sh`, `scripts/check-anchored-event-ids.sh`, `Makefile`, `configs/config.yaml`)

Stage: design (aggregating `requirements-10762e10`).

---

## 0. Evidence verification — verdicts against the live tree

Every claim in the requirements evidence was re-checked against the working tree. **The requirements snapshot is stale: most of the surface has since landed in the uncommitted working tree, some of it with deliberate semantic divergences.** The design below reconciles the requirements with what actually exists.

| Requirements claim | Verdict (working tree, 2026-08-07) |
|---|---|
| `AuditPublicationRepository` (audit_ledger.go:42-46) has only Claim/Finish | ⚠️ **Stale — superseded.** Interface now also has `RequeuePublication`, `CountDeadLetterPublications`, `PurgeDeadLetterPublications` (audit_ledger.go:47-57). Implemented in sibling file `internal/repository/audit_publication_replay.go` (126 lines, untracked). |
| `audit_publication.go` (122 lines) has no Requeue/list/count | ✅ **Still true** — Claim/hydrate/Finish only; new methods correctly went to a sibling file (sibling-file constraint honored). |
| `publicationOutcome` dead-letter settle @ callback_audit.go:144-155 (terminal settle 152) | ✅ **Confirmed** — `internal/service/callback_audit.go:115-124`; `return "dead_letter", "audit.publication_failed", …` at line 121. Single terminal settle path. |
| Only delivery site @ callback_audit.go:157 (`publishAuditFact`; drain at 44, ticked from callback.go:286) | ✅ **Confirmed** — `publishAuditFact` at service/callback_audit.go:157; `drainPublications` at 45; ticker at service/callback.go:286-292 (`s.drainPublications(workerCtx); s.drain(workerCtx); s.purgeDeadLetters(workerCtx)`). |
| `TestDrainExhaustsAttemptsIntoDeadLetter` @ callback_integration_test.go:207; no replay test exists | ✅ **Confirmed** — line 207; dead_letter reachable, attempt=3, never re-claimed. Replay tests still do not exist. |
| `cmd/` = migrate+server only | ⚠️ **Stale.** `cmd/audit-replay/main.go` (199 lines, untracked) now exists — requeue-only tool (see D-1). |
| `AuditGovernanceConfig` (8-21) has no purge/pause | ⚠️ **Stale — superseded.** Now has `PurgePause bool` + `DeadLetterRetention time.Duration`; `validateAuditGovernance` rejects negative retention (validate.go:207-214); `configs/config.yaml:186-187` documents both. `CallbackConfig` gained the same two fields (service/callback.go:58-63); `buildAuditStack` maps them (app/audit.go:47-52). |
| `audit_ledger.go` 449 lines | ⚠️ **459 now** (interface additions) — still under the 500 gate. **New gate violation elsewhere: `internal/service/callback.go` is 518 lines — `make check-file-sizes` fails today** (see D-5). |
| `check-audit-swallow.sh` P1/P2 lack `RequeuePublication\|RequeueDead` | ✅ **Still true** — P1/P2 cover `Record|Log|Publish|FinishPublication|ClaimPublication` only. The script itself is untracked (added this campaign). Gate extension not done (D-4). |
| Requeue fact needs deterministic `EventID = idgen.Anchor(...)` (pattern at audit_hold.go:164,184); second requeue stopped by `uq_audit_ledger_tenant_event` → `ErrAuditDuplicate` conflict receipt | ⚠️ **Diverged deliberately.** Landed `requeueFact` (cmd/audit-replay/main.go:186-199) mints a **fresh v7 `EventID` per requeue run** (C2 doctrine: replay = new operator intent), and the repo methods **swallow `ErrAuditDuplicate` in-tx** (audit_publication_replay.go:64-66, callback_audit.go:138-140) instead of propagating/rolling back. Consequences: (a) idempotency comes from the `status='dead_letter'` predicate, not the ledger dedup; (b) a second requeue of a re-failed row appends a *second* fact; (c) the requirements' T-5 "rollback via pre-appended anchored event_id" recipe is **unimplementable** — the duplicate is swallowed and the reset commits. Restated in D-2/D-7. |
| 202-conflict idempotent-success @ publisher.go:290 | ✅ **Confirmed** — receipt `Conflict` handled as idempotent success at publisher.go:242-262; `receiptMatches` at 290. |
| Four test fakes in callback_audit_test.go must gain the methods | ✅ **Landed** — service fakes updated (files modified); `go build ./...`, `go vet`, and unit tests for repository/service/cmd pass. |

**Net delta — what is already landed (uncommitted) vs. what the design still must deliver:**

| Landed | Still missing (this design's scope) |
|---|---|
| Repo methods: `RequeuePublication`, `CountDeadLetterPublications`, `PurgeDeadLetterPublications`; `RequeueDead`, `CountDeadLetterDeliveries`, `PurgeDeadLetterDeliveries` (both interfaces + fakes) | `cmd/audit-replay`: `-purge-pause` flag, fail-closed `-tenant` handling + binding pre-flight, signal-aware context, testable core extraction |
| Config: `purge_pause` + `dead_letter_retention` (+ negative-retention guard), buildAuditStack mapping, config.yaml docs | Config parity rule: `purge_pause` must not be a no-op (retention > 0) — sibling-direction parity |
| `purgeDeadLetters` tick (callback.go:305-328) with `Purged`/`PurgeFailed` metrics | `callback.go` 518-line split (file-size gate is red) |
| `cmd/audit-replay` requeue-only tool (no tests) | `make build`/Makefile target for `bin/audit-replay` |
| Makefile gate targets (`check-integration-compile`, `check-audit-swallow`) | Gate pattern extension + fixtures (P1/P2 += Requeue*); anchored-event-ids C2 allow-list for the tool + `cmd/` scan |
| — | All replay acceptance tests (T-10.2, T-4, T-5 restated in §7) |

---

## 1. Design decisions

### D-1 — Tool scope: requeue-only; the server owns delivery (keep landed doctrine)

The requirements (R5) had the tool requeue **and** run `drainPublications` until empty. The landed tool deliberately never claims, drains, or publishes ("the CLI never publishes" — main.go:8-10). **Keep the landed doctrine.** Rationale:

- No second publish loop, no claim races between tool and server, no new `CallbackService` entry point (`DrainOnce` interface growth avoided).
- The tool is safe to run while the server is up; requeued rows are picked up by the server's next tick through the single delivery path (R4 preserved).
- The "requeue and wait" variant is covered by `-dry-run` + per-tenant runs; the operator observes delivery via the existing `ObserveAuditPublicationOutcome`/`Purged` metrics.

Rejected alternative: tool-driven drain. Requires exporting drain control, duplicates claim/finish coordination, and races the server's own tick. The requirements' acceptance "the runnable core accepts a recording publisher so T-4 can be asserted without a live governance endpoint" is still met — via the *server-side* drain driven by the test harness (same shape as `TestDrainExhaustsAttemptsIntoDeadLetter`), not by the tool.

### D-2 — Requeue audit fact: one fact per requeue run, fresh v7 (C2), tenant REQUIRED

Landed shape (`requeueFact`, main.go:186-199): one batch-level fact per run/kind — `Action: audit.publication.requeued | callback.delivery.requeued`, `EventID: idgen.NewID()` (fresh v7), `Source: "aero-id"`, `SourceRegion: "local"`, `PartitionKey: tenant + ":requeue"`, `Payload: {actor}` (allow-listed, redacted), `CausationID` omitted.

Divergences from R3, reconciled deliberately:

1. **Fresh v7 per run, not per-publication anchor.** C2 doctrine (`check-anchored-event-ids.sh` R-1: "a replay is a new user intent that must re-apply the reset, so it deliberately mints a fresh v7 per replay intent — sole allow-list entry") already governs `callback.delivery.replayed`. The requeue fact is the same intent class. An anchored per-publication event_id would make a second requeue of a re-failed row a *duplicate* (blocked or swallowed) — the second intent would be invisible in the ledger. Each requeue run is a distinct, auditable intent. **This requires extending the C2 allow-list to `cmd/audit-replay` and scanning `cmd/`** (D-4).
2. **Idempotency is the status predicate, not the ledger dedup.** `WHERE status = 'dead_letter'` scopes both the SELECT and the UPDATE (`… AND status = 'dead_letter' AND id IN (…)`), so a second run with identical arguments requeues 0 rows. `ErrAuditDuplicate` is swallowed in-tx (after the UPDATE) so a retried requeue can never fail a row that is already reset. This is the same contract as the governance endpoint's 202-conflict idempotent success — but delivered by the status predicate, not the anchor.
3. **`-tenant` becomes REQUIRED; empty tenant is rejected (fail closed).** The landed default (`-tenant ""` → `requeueFact` returns nil → **no audit fact appended**) violates the audit invariant ("every state-changing, administrative, … operation produces a durable audit fact") and the "no silent un-audited mutation" doctrine. The tool's own comment documents the reason for the nil-fact path — the `validateAuditBindingCoverage` boot gate (app/audit.go:165-184) requires every tenant with `pending|delivering|dead_letter` publications to have a governance binding, and a delivery-kind requeue fact can *introduce* a tenant into the coverage set for the first time (deliveries are not in the coverage query). Resolution:
   - `-tenant` required; empty → error before any mutation.
   - Pre-flight: when `cfg.AuditGovernance.Enabled`, verify `tenant ∈ cfg.AuditGovernance.Bindings`; if not, fail with the exact remediation (add the binding or replay under a covered tenant). When governance is disabled there is no boot hazard, but the fact is still appended (local ledger).
   - The fact's tenant is the *operator-chosen* tenant for the run; rows of other tenants are simply not selected by the operator (documented: per-tenant replay runs).

Rejected alternative: move per-row fact construction into the repository (repo builds anchored per-row facts from the loaded fact/row). Churns the landed signatures again, resurrects the anchor-vs-C2 conflict per row, and buys nothing the batch fact doesn't provide once `-tenant` is mandatory.

### D-3 — `-purge-pause` flag on the tool: replay-window guardrail (fail-closed)

The direction title requires a purge-pause flag; the landed tool has none. Landed config semantics (service/callback.go:305-311): `purgeDeadLetters` early-returns when `DeadLetterRetention <= 0 || PurgePause` — the pause suspends the **purge DELETE**, not drain claims. The requirements' alternative reading (pause = drain early-returns + dedicated `ObserveAuditPublicationDrainPaused` metric) is **superseded**: the tool doesn't drain, so a drain-pause is meaningless for it; the real race is the server's purge scan deleting rows the operator wants to replay.

Tool semantics: add `-purge-pause` bool flag, `flag wins over config file` (R6 preserved):

- The tool never purges, so the flag's enforceable meaning is a **race guard**: if the loaded config has `dead_letter_retention > 0` AND `purge_pause == false` AND the flag is absent → **refuse to run** (exit non-zero, message: purge scan is active; set `audit_governance.purge_pause: true` + restart the server, or pass `-purge-pause` to acknowledge the window). With the flag → proceed and print the operational reminder (the running server still purges until the config is changed and restarted; rows past the retention cutoff may still be deleted).
- The flag cannot hot-toggle a running server (config is static per boot — documented in usage text).
- **No new metric.** `ObserveAuditPublicationDrainPaused` (R6 recommendation) is dropped: no drain pause exists; the purge path is already observable via `ObserveAuditPublicationPurged`/`PurgeFailed`; the sibling outbox direction (`purge_paused`) set the precedent of no pause metric. `ObserveAuditPublicationDrainSkipped` stays exclusively the FM-8 unwired-outbox signal.
- Config parity rule (with the sibling): `validateAuditGovernance` additionally rejects `purge_pause == true && dead_letter_retention == 0` — the flag must never be a no-op (sibling precedent: "pause requires both gate fields positive"). Safe: `purge_pause` is unreleased; `configs/config.yaml` ships `false/0`.

### D-4 — Gate extension (R7, still missing)

- `scripts/check-audit-swallow.sh`: P1_PATTERN and P2_PATTERN gain `RequeuePublication|RequeueDead`. Fixtures: positive += `_ = s.publications.RequeuePublication(ctx, ids, 0, nil)` (P1 shape) and bare `s.publications.RequeueDead(ctx, ids, 0, nil)` (P2 shape); negative fixtures unchanged (verify zero false positives — negative fixtures contain no Requeue tokens today).
- Makefile `check-audit-swallow` roots: add `cmd` (currently `internal/service internal/repository internal/app`). Verified safe: P1/P2 match only the named audit methods; `_ = sqlDB.Close()` in cmd/audit-replay does not match. The tool and the new repo methods contain zero swallowed/bare audit writes by construction (D-1/D-2).
- `scripts/check-anchored-event-ids.sh`: add `cmd/audit-replay` as a second C2 allow-list entry (fresh-v7 `EventID` per replay intent — same doctrine as `internal/service/callback.go`), and scan `cmd/` (the script's default root is `internal`; the tool's `EventID: idgen.NewID()` currently escapes the gate). No `Anchor` sites are added anywhere (batch-fact design, D-2).
- `check-integration-compile` already exists in the Makefile (untracked) and covers the new integration tests.

### D-5 — File-size gate repair (blocking): split `internal/service/callback.go` (518 lines)

`make check-file-sizes` is red today. Split ~120 lines into `internal/service/callback_worker.go`: `drain` (deliveries drain, callback.go:328-…), `purgeDeadLetters` (305-328), and the delivery attempt helper(s) they own. Zero behavior change; pure move. `callback.go` lands ≤ 400 lines.

### D-6 — Build/deploy

- Makefile: `build` gains `go build -o bin/audit-replay ./cmd/audit-replay` (or a `build-audit-replay` target invoked by `build`). No binaries at repo root (AGENTS.md).
- `cmd/audit-replay/main.go`: extract a testable core `replayAll(ctx context.Context, cfg *config.Config, opts replayOptions) error` (pure — no flags, no stdout dependency beyond returned counts/errors); `run(args)` stays a thin flag/IO wrapper (precedent: `cmd/migrate` structure). Wire `signal.NotifyContext` for SIGINT/SIGTERM instead of `context.Background()`.
- Tool exit codes: 0 = completed (including "0 requeued"), 1 = any error, with the message on stderr.

---

## 2. API changes (final)

**Repository (already landed, ratified by this design):**

```go
// AuditPublicationRepository (internal/repository/audit_ledger.go)
ClaimPublication(ctx, worker string, lease time.Duration, limit int) ([]*domain.AuditPublication, error)
FinishPublication(ctx, id domain.ID, worker string, attempt int, status, errorCode string, next time.Time) error
RequeuePublication(ctx, ids []domain.ID, limit int, fact *domain.AuditFact) (int, error)          // landed
CountDeadLetterPublications(ctx, ids []domain.ID, limit int) (int, error)                          // landed (dry-run)
PurgeDeadLetterPublications(ctx, cutoff time.Time, limit int) (int64, error)                       // landed

// CallbackRepository (internal/repository/callback.go)
RequeueDead(ctx, ids []domain.ID, limit int, fact *domain.AuditFact) (int, error)                  // landed
CountDeadLetterDeliveries(ctx, ids []domain.ID, limit int) (int, error)                            // landed
PurgeDeadLetterDeliveries(ctx, cutoff time.Time, limit int) (int64, error)                         // landed
```

Semantics (ratified): status-scoped (`dead_letter` only); bounded (exact ids, else oldest `limit` by `(next_attempt_at, id)`); reset = `status pending, attempt 0, lease cleared, error_code manual_replay, next_attempt_at now()`; optional fact appended in-tx **only when rows were affected**; `ErrAuditDuplicate` swallowed in-tx (idempotent re-apply, never masks a failed reset); purge excludes leased rows, keyset-ordered, bounded. `requeueReset`/`replayDefaultLimit` shared via `audit_publication_replay.go`.

**Config (landed + one new rule):**

```yaml
audit_governance:
  purge_pause: false          # landed; suspend dead-letter purge scan in the drain loop
  dead_letter_retention: 0    # landed; 0 = never purge
```
Env: `AERO_ID_AUDIT_GOVERNANCE_PURGE_PAUSE`, `AERO_ID_AUDIT_GOVERNANCE_DEAD_LETTER_RETENTION` (SetEnvPrefix "AERO_ID", `.`→`_`). **New:** `validateAuditGovernance` rejects `purge_pause && dead_letter_retention == 0` (D-3).

**Tool flags (delta):**

```
audit-replay [-config path] [-kind publication|delivery|both] [-limit N] [-id id[,id…]]
             [-tenant tenant (REQUIRED)] [-actor actor] [-purge-pause] [-dry-run]
```
Changes from landed: `-tenant` required (error when empty — D-2); `-purge-pause` added (D-3); binding pre-flight when governance enabled (D-2); signal-aware context; testable core (D-6).

**No service-interface changes.** No new `CallbackService` methods (D-1). No HTTP/gRPC surface.

---

## 3. Compatibility constraints

- **No DB migration.** `000045_audit_publications` already models every field the reset touches (`status, attempt, lease_owner, lease_until, next_attempt_at, error_code`) and carries `uq_audit_publication_fact`; `000050` carries `uq_audit_ledger_tenant_event`. No new columns, status values, or indexes (existing `idx_audit_publications_due (status, next_attempt_at, id)` covers both requeue and purge predicates).
- Existing drain/claim/finish/`publicationOutcome` behavior unchanged; requeued rows are ordinary `pending` rows with zero special casing in the claim predicate.
- Purge scan default-off (`dead_letter_retention: 0`); `purge_pause: false` default = today's behavior byte-for-byte.
- Interface growth is compile-time only; all four service fakes + repo fakes updated (landed); `check-integration-compile` proves integration-tagged fakes too.
- `cmd/audit-replay` is additive; `cmd/server`, `cmd/migrate`, handlers, and the governance publisher protocol untouched.
- C2 allow-list extension is additive (tool's fresh-v7 `EventID` becomes *sanctioned* rather than escaping the gate by root-scan omission).
- Only newly-rejected configs: `purge_pause: true` with `dead_letter_retention: 0` (unreleased field — no existing deployment can break).
- Requeue of a publication that dead-letters again is *always* possible (fresh v7 fact per intent) — the requirements' documented limitation ("second dead-lettering blocked by dedup") is **lifted** by D-2; the residual limitation is that each requeue is a new intent fact, so the ledger records intents, not a per-publication requeue counter.

---

## 4. Failure modes

| # | Failure | Consequence | Mitigation |
|---|---|---|---|
| F1 | Server purge DELETE races the tool (retention > 0, purge_pause false) | Rows past cutoff deleted between count and requeue; replay silently misses them (requeued count lower than expected) | D-3 fail-closed guard + config `purge_pause` + ops sequence; `-dry-run` first; requeued count printed per run |
| F2 | Requeue fact tenant not in governance bindings | Next server boot fails `validateAuditBindingCoverage` (esp. `-kind delivery`, whose dead rows are not in the coverage query) | D-2: `-tenant` required + pre-flight binding check when enabled |
| F3 | Second requeue of a re-failed row | Second fact appended (fresh v7) — distinct intent | Intended (C2 doctrine); documented in tool doc comment; not a dedup violation |
| F4 | Tool runs concurrently with server drain | Requeue touches only `dead_letter` rows; server never claims them; per-call tx isolation | No coordination needed; lease/fencing unchanged |
| F5 | Tool crashes mid-batch | Per-call tx atomicity: each `RequeuePublication` commits or rolls back wholly; partial requeue possible across calls; server may deliver already-requeued rows (safe — single delivery path + dedup receipts) | Idempotent re-run completes the set; `-id` for precise resumption |
| F6 | DB outage during run | Non-zero exit, nothing mutated in the failing call | Re-run; per-call tx |
| F7 | Missing/too-short signing key | Tool fails before any mutation (pre-flight in `run()`); in-repo, keyless `AppendInTx` rolls the reset back | T-5 negative test proves rollback; startup validation parity |
| F8 | Malformed `-id` | Hard error before any mutation | `parseIDs` rejects |
| F9 | Dry-run count vs. actual divergence (rows change between count and requeue) | Advisory only | Printed counts; documented |
| F10 | `purge_pause: true` with `dead_letter_retention: 0` | Flag inert (no purge scan exists) | New validation rejects (D-3 parity rule) |
| F11 | `callback.go` at 518 lines | `make check-file-sizes` red — done-gate blocked | D-5 split (blocking item) |
| F12 | Tool's fresh-v7 `EventID` escapes anchored-event-ids gate (root scan = `internal`) | Gate blind spot | D-4: C2 allow-list + `cmd/` scan |

---

## 5. Migration steps

1. **Schema:** none. Confirm `000045`/`000050` present in target DBs (both predate this work).
2. **Code/config (one commit):** repo methods + config fields + `purgeDeadLetters` tick + tool (landed, uncommitted) **plus** this design's delta: `-tenant` required + binding pre-flight, `-purge-pause` guard, testable core + signal context, validation parity rule, gate extensions (D-4), `callback.go` split (D-5), Makefile `build` for `bin/audit-replay` (D-6), tests (§7).
3. **Deploy:** build `bin/aero-id` + `bin/audit-replay`; rollout servers (config adds two inert fields). The tool needs no server restart to run.
4. **Replay window ops sequence** (documented in tool usage and config comment):
   a. set `audit_governance.purge_pause: true` (and `dead_letter_retention` as configured), restart all server instances;
   b. verify purge went quiet (`ObserveAuditPublicationPurged` stops);
   c. `bin/audit-replay -dry-run -tenant <covered-tenant>`, then `bin/audit-replay -tenant <covered-tenant>` (repeat per tenant);
   d. observe delivery via `ObserveAuditPublicationOutcome`/`ObserveCallbackOutcome` (server drains on next ticks; no tool-side delivery);
   e. clear `purge_pause`, restart.
5. **Gate:** full done-gate (below) green before merge; `make test-integration` once (Postgres via testcontainers).

---

## 6. Testable acceptance mapping (restated against landed semantics)

### T-10.2 — Requeue + drain lifecycle (integration, real Postgres)
New: `internal/repository/audit_publication_replay_integration_test.go` (integration tag, `testpg` harness):

1. **Seed → dead_letter**: append fact, drive `drainPublications` with a failing publisher until the row settles `dead_letter` (reuse the backdate loop from `TestDrainExhaustsAttemptsIntoDeadLetter`).
2. **Requeue**: `RequeuePublication(ctx, []domain.ID{id}, 0, fact)` → row asserts `status=pending, attempt=0, lease_owner='', lease_until IS NULL, next_attempt_at <= now(), error_code='manual_replay'`; requeue fact row present with `action='audit.publication.requeued'`, the run's event_id, `tenant`, allow-listed `payload.actor` only, `partition_key='<tenant>:requeue'`.
3. **Deliver exactly once**: server-style drain with `recordingAuditPublisher` → `FinishPublication` settles `completed`; exactly one publish; exactly one `callback_deliveries` row (`uq_callback_delivery_event`).
4. **Double-run idempotency** (restatement of "requeue twice publishes exactly once"): a second `RequeuePublication` on the now-pending row returns **0 rows** (status-predicate no-op; *not* `ErrAuditDuplicate` — the R3 anchor-based conflict receipt was superseded by D-2); drain publishes nothing new.
5. **Re-dead-letter + second requeue** (replaces R3's "exactly one fact per publication"): re-fail the row to `dead_letter`, requeue again → row pending again; ledger holds **two** `audit.publication.requeued` facts with distinct v7 event_ids (one per intent — C2); second drain publishes exactly once more.

### T-4 — `cmd/audit-replay` idempotency, purge-pause, fail-closed tenant (integration)
New: `cmd/audit-replay/main_test.go` (integration tag) driving `replayAll(ctx, cfg, opts)` against `testpg` with a temp config (governance disabled or a recording-binding setup):

1. Seed dead rows (publication + delivery kinds); run `replayAll` **twice** → both exit 0; second run requeues 0; server-side drain (harness) publishes each fact exactly once; exactly one requeue fact per run/kind.
2. `-dry-run` → counts match, zero mutations (rows still dead_letter, zero facts).
3. `-purge-pause` guard: config with `dead_letter_retention > 0, purge_pause: false` and no flag → tool **refuses** (non-zero, clear message); with `-purge-pause` → proceeds; with config `purge_pause: true` → proceeds without flag.
4. `-tenant` fail-closed: empty `-tenant` → error, no mutation; tenant not in bindings (governance enabled) → error naming the remediation.
5. Gate greps: no `_ =`/bare-call `RequeuePublication`/`RequeueDead`/`Record`/`Log`/`Publish`/`FinishPublication`/`ClaimPublication` in the tool or new repo methods (`make check-audit-swallow` with extended patterns); anchored-event-ids gate green with the C2 allow-list.

### T-5 — In-tx requeue audit fact, rollback atomicity (integration)

1. **Positive**: requeue commits reset + fact together (both visible post-commit; no standalone `Record`/`Log` anywhere in the requeue path).
2. **Negative (restated — the "pre-appended anchored event_id" recipe is dead by D-2's in-tx swallow)**: construct the ledger repo **without a signing key** (`NewAuditLedgerRepository(db, AuditLedgerConfig{})`) → `RequeuePublication` returns the append error and the row **remains `dead_letter`** with its original attempt/lease/next_attempt_at — the reset rolled back with the fact append. (Delivery-side analogue: unit test with an `AppendInTx`-failing fake ledger → `RequeueDead` rolls back.)
3. **Purge boundary**: `PurgeDeadLetterPublications` with cutoff/lease guards — leased rows excluded; `purge_pause: true` → zero rows deleted; retention 0 → zero rows deleted.

### Unit (non-integration)
`cmd/audit-replay`: `parseIDs` (valid/malformed/empty), flag→`replayOptions` mapping, `-purge-pause`/`-tenant` validation, `requeueFact` field assertions (allow-listed payload, partition, source/region). Repository: `requeueReset` shape, `replayDefaultLimit` fallback. Config: `validateAuditGovernance` parity rule + env override tests.

---

## 7. Out of scope (explicit)

- No HTTP/gRPC handler surface for requeue (operator tool only).
- No tool-side delivery/drain; no new service methods.
- No auto-requeue/self-healing policy; no change to `publicationOutcome`, `MaxAttempts`, backoff, lease math.
- No change to the governance publisher protocol or conflict-receipt validation.
- No per-publication anchored facts or requeue counters (superseded by D-2; documented limitation: ledger records requeue *intents*).
- No dead-row listing/export beyond `-dry-run` counts.

## 8. Done-gate

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-integration-compile && make build
```
plus `make test-integration` once (new replay/purge/tool integration tests). `check-file-sizes` is currently red (`internal/service/callback.go`, 518 lines) — D-5 split is the first blocking item.
