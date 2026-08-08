# Stress test — operator runbook for dead-letter replay (pause → dry-run → replay → verify → unpause)

| | |
|---|---|
| Under test | `docs/plans/2026-08-08-internal-app-dead-letter-replay-design.md` §6.3 runbook, against the as-landed code on head `13e4676` + working tree |
| Scope | `cmd/audit-replay` semantics, purge-pause knobs, drain-loop claim/lease behavior, control-plane metrics, crash windows |
| Date | 2026-08-08 |
| Status | **Findings accepted into the remediation plan (2026-08-08, implemented)** — R1 (runbook step 1 + restart confirmation), R2 (loop-until-zero + bounded counts), R3 (verify semantics), R4 (knob-role correction), R5 (control-not-atomicity wording), R6 (`-report` journal + leg-resume test). Optional hardening from §8 NOT in scope: paused-boot gauge, unbounded dry-run escape hatch, crash-window kill tests. The corrected runbook is remediation plan §5 |

Method: every claim below was checked against the live source (not the design doc's claims about it). Line numbers are for the working tree at the time of writing. Runbook steps are quoted from the design's §6.3.

## 1. Findings summary

| # | Runbook claim/step | Verdict | Severity |
|---|---|---|---|
| R1 | "set `purge_pause` … and restart — paused ticks preserve DLQ rows bit-for-bit" | ✅ static-per-boot is real; **restart is the only mechanism and is unverifiable at runtime** — a flip without restart is silent, and in a positive-retention deployment the server keeps purging. In the **default** config (`dead_letter_retention: 0`, `configs/config.yaml:189`) the drain purge is a no-op anyway, so the hazard only exists once a positive retention is configured | **High** (silent precondition failure) |
| R2 | Step 4 "second run prints `requeued 0`" | ❌ **only correct when the backlog ≤ `-limit` per kind.** With a backlog > 100 the second run requeues the *next* oldest 100 and prints `requeued 100`. The design's own failure-mode row ("Second run affects 0") silently assumes backlog ≤ limit; the pinned idempotency test seeds 3 rows with `-limit 10` (`main_test.go:133-225`) and never exercises the >limit case | **High** (verify step can lie both ways) |
| R3 | Step 4 "drain completes rows to `completed` (metrics/lag)" | ❌ **insufficient to detect partial replay across kinds.** There is no audit-publication lag metric at all (`projectionLag`/`consumerLag` are source-event gauges for the account-projection subsystem); the outcome counters are cumulative, global-per-status, and not correlated to any replay batch; the `manual_replay` error-code marker is overwritten on the first finish; `-kind both` commits the two legs in **two separate transactions**, so a kill between them leaves one kind requeued and the other untouched — invisible to any single-kind metric check | **High** |
| R4 | Step 1 also sets `data_retention.purge_paused: true` "so dead-letter replay cannot race it" | ⚠️ **redundant for this tool.** `purge_paused` gates only the outbox `eventRow` cleaner (`internal/outbox/repository.go:84-183`); `cmd/audit-replay` mutates only `audit_publications` and `callback_deliveries`. The outbox cleaner and the replay tool cannot race. Harmless, but it teaches operators a wrong mental model of which knob protects what | Low |
| R5 | Skipping the pause step (or forgetting the restart) | ❌ **the requeued rows become claimable by the live drain within one tick (~1 s, `worker_interval: 1s`, `configs/config.yaml:209`).** This is not data loss but a loss of *control*: uncontrolled delivery attempts, duplicate callback POSTs under at-least-once, premature re-dead-lettering after `MaxAttempts`, and — with a backlog > limit — the purge tick deleting un-replayed rows between CLI runs | High (operator error is unguarded) |
| R6 | Crash windows (CLI killed mid-tx / after commit / between legs) | ✅ mid-tx is safe by Postgres rollback; ✅ after-commit-before-report is safe but *ambiguous* (re-run prints `requeued 0` in both the "already done" and "nothing existed" cases); ⚠️ **kill between the two legs of `-kind both` leaves a partial cross-kind state with no CLI-side marker** — detectable only by verifying both kinds | Medium (no test pins the crash windows; safety rests on tx semantics + status predicate) |

No production-code defect was found: the atomicity/idempotency/partition properties claimed by the design hold in the code (§2–§3). The defects are in the runbook's wording and in the operator-verification surface, not in the mechanism.

## 2. Static-per-boot pause flags: restart sequencing (R1)

Evidence that the knobs are boot-captured and there is no reload path:

- `config.Load` is called exactly once per binary: `cmd/server/main.go:22`, `cmd/audit-replay/main.go:75`, `cmd/migrate/main.go:62`. No other call sites (grep over `cmd/` + `internal/app/`).
- `cmd/server/main.go:35`: `signal.NotifyContext(..., syscall.SIGINT, syscall.SIGTERM)` — no SIGHUP handler, no reload goroutine.
- `internal/app/audit.go:52-53`: `PurgePause`/`DeadLetterRetention` are copied from config into `service.CallbackConfig` **at composition**.
- `internal/app/outbox.go:91`: `PurgePaused` is passed into `outbox.RunOutboxCleaner` **at wiring**.
- `internal/service/callback_worker.go:64-80`: `purgeDeadLetters` re-checks `s.cfg.PurgePause`/`s.cfg.DeadLetterRetention` every tick — but `s.cfg` is the boot-captured struct; the check makes the flag *honored per tick*, not *reloadable*.

So the sequencing is exactly: flip flag → **restart** → operate → revert flag → restart. Two operator-visible consequences:

1. **Flip without restart is undetectable.** No metric, log line, or counter distinguishes "paused since boot" from "paused in file, running process still purging". If an operator edits `config.yaml` and the deploy tooling restarts the process, all is well — but if the config edit lands without a restart (config-map sync that does not roll pods, SIGHUP assumed, runbook skimmed), the purge scan keeps running against `dead_letter` rows while replay works them. The tested race property (below) keeps the *database* consistent, but rows purged between CLI runs are **silently never replayed** — the ledger fact survives (FK `fact_id → audit_ledger(id)` runs ledger→publication only; purge deletes publication rows only), so the compliance trail is intact while the delivery opportunity is lost.
2. **Default-config deployments are immune.** `dead_letter_retention: 0` (`configs/config.yaml:189`) makes `purgeDeadLetters` a per-tick no-op (`callback_worker.go:65`). The R1 hazard exists only in deployments that raised the retention. By contrast `outbox_dead_letter_retention: 168h` is the **default** (`configs/config.yaml:237`), so the outbox cleaner is active by default — but it cannot race this tool (§R4).

The exact-partition property that makes the un-paused race *non-corrupting* is real and pinned: `TestConcurrentPurgeAndRequeueRace` (`audit_publication_replay_integration_test.go:328-409`, `purged + requeued == seeded`, no row transitions twice) and `TestRequeueVsPurgeInterleaving` (`:509-570`, both orders safe, facts survive). The pause is therefore about **control and completeness, not atomicity** — which is exactly why the runbook must state the restart as load-bearing and give the operator a way to confirm it took effect (recommendation in §6).

## 3. Crash windows (R6)

`RequeuePublication`/`RequeueDead` run one `gorm.Transaction` per kind: plain SELECT (no `FOR UPDATE`) of `dead_letter` rows → UPDATE with the same status predicate → optional in-tx fact append. Windows:

| Window | Behavior | Verdict |
|---|---|---|
| Kill mid-tx (after SELECT, after UPDATE, after fact append — before COMMIT) | Connection drop → Postgres implicit rollback: 0 rows, 0 facts. Re-run selects the same rows again. Safe by tx atomicity | ✅ |
| Kill after COMMIT, before the `requeued N` line prints | Rows are `pending` + fact appended; operator saw nothing. Re-run → `requeued 0`, **no fact** (fact minted only when rows affected, `audit_publication_replay.go:60-65`) → exit 0. Idempotent, but the printed `0` is ambiguous: "already requeued by the killed run" vs "nothing was ever there". The runbook's own verify step (second run → 0) is the disambiguator, but it must be read as idempotency proof, not completion proof | ✅ safe, ⚠️ ambiguous output |
| Kill between the two legs of `-kind both` | Two separate transactions (`cmd/audit-replay/main.go:99-105`): publications committed, deliveries never ran (leg order: publication first). No partial marker exists — no journal, no summary row, exit status is the kill signal. Detectable only by verifying **both** kinds | ⚠️ partial cross-kind state, CLI-side invisible |
| Concurrent purge between the CLI's SELECT and UPDATE (same tx, no lock) | Rows deleted by the purge tick are skipped by the UPDATE's status predicate → `RowsAffected < selected` → report prints the true (lower) count. Honest count, but it differs from the dry-run estimate. Only reachable when the pause step was skipped | ⚠️ (R1/R5 territory) |

No test pins these windows (the integration suite covers run-twice idempotency, not process death); the safety rests on standard Postgres semantics plus the status-predicate idempotency that `TestAuditReplayCLIRunTwiceIsIdempotent` (`main_test.go:133`) and `TestRequeuePublicationResetsDeadLetterRows` pin.

## 4. `-limit 100` default vs `-id` override (R2)

As-landed semantics (`internal/repository/audit_publication_replay.go:40-100`, `callback_audit.go:115-143`):

- `-id` non-empty → **exact set, `-limit` ignored entirely** (the `if len(ids) > 0` branch skips both the ordering and the bound). Malformed IDs are a hard error (`parseIDs`, `main.go:113-131`) — good.
- No `-id` → oldest `limit` rows by `(next_attempt_at, id)`, per kind. `limit <= 0` → code default 100 (consistent with the AGENTS.md `0 = disabled` convention: `-limit 0` is *not* "unbounded").
- `-dry-run` uses the **same bound**: `CountDeadLetterPublications`/`CountDeadLetterDeliveries` apply `.Limit(limit)` too. With a backlog > 100, dry-run prints `100 (dry run)` — a capped estimate, not the backlog. There is no CLI way to get the unbounded count (limit 0 falls back to 100).

Consequences the runbook must encode:

1. **Step 4's "second run prints requeued 0" is wrong for backlog > limit.** The second run requeues the next-oldest 100 and prints `requeued 100` (or, for `-kind both`, two lines). An operator who sees a nonzero second run is *fine* (keep looping), but the runbook as written instructs them to expect 0 — and a single run of a >100 backlog followed by a premature "verify" concludes success while rows remain `dead_letter`. The design's failure-mode row "Tool run twice / 0 eligible rows → Second run affects 0" shares the same silent assumption; the pinned test seeds 3 rows with `-limit 10`.
2. **`-id` with stale IDs is silent.** IDs already requeued/completed/purged are skipped by the status predicate; the report shows only the affected count. An operator targeting exact rows cannot distinguish "row done" from "row gone" from the CLI output — for forensic precision a DB query is required.
3. **Limit is per kind.** `-kind both -limit 100` moves up to 200 rows per run (100 per leg).

## 5. Skipped pause step: requeued rows are claimable by the live drain (R5)

Verified claim predicates and timing:

- `ClaimPublication` (`internal/repository/audit_publication.go:42`): `(status='pending' AND next_attempt_at <= now()) OR (status='delivering' AND lease_until < now())` — `dead_letter` rows are structurally unreachable, and requeue sets exactly `status='pending'`, `next_attempt_at=now` (app clock), so **the first tick after the requeue commit claims the rows**.
- `FetchDueDeliveries` (`internal/repository/callback.go:181-215`): same shape for the delivery leg (`pending AND next_attempt_at <= now`).
- Drain ticker default: `worker_interval: 1s` (`configs/config.yaml:209`, `internal/app/new.go:166`). So the window between "CLI commits" and "live drain attempts delivery" is ≤ ~1 s.
- The CLI itself never publishes; delivery stays drain-only (`callback_audit.go:179` single `Publish` site, pinned).

So if the operator skips the pause step (or flips the flag without restart), the replay is not a controlled recovery — it is an *uncoordinated* one:

1. **Immediate uncontrolled attempts.** Rows are claimed within a tick and published to the governance endpoint (publications) / callback endpoints (deliveries). If the underlying outage persists, the fresh budget (`attempt` reset to 0) is consumed by `MaxAttempts` more automatic retries and the rows re-dead-letter with `error_code='audit.publication_failed'` — the `manual_replay` marker is overwritten by the first `FinishPublication`. The operator's verify step then shows dead_letter again and the whole runbook restarts; not data loss, but the maintenance window is consumed by uncoordinated retries.
2. **Duplicate callback POSTs are possible** under at-least-once semantics (deliveries leg) — the endpoint sees replays it may not expect.
3. **Purge races multi-run replay.** With backlog > limit, rows not yet selected by the CLI are still `dead_letter` and past cutoff — the running purge tick can delete them between CLI runs. The partition property guarantees no double-transition and no fact loss, but the rows are gone: **silent loss of the delivery opportunity**, which is precisely the failure the pause step exists to prevent.
4. **Verification is polluted.** Outcome counters (`ObserveAuditPublicationOutcome`, `ObserveCallbackOutcome`) record automatic drain attempts indistinguishably from replay-triggered ones, so "metrics show completed" can be satisfied by the drain alone even if the operator's replay never ran.

Note the asymmetry: the pause step is *not* needed for requeue atomicity (the tx + status predicate hold regardless); it is needed for **control and completeness**. The runbook must say so, because an operator who understands "the race is safe" (true, at the DB level) may reasonably conclude the pause is optional (false, at the operational level).

## 6. Verify-by-metrics/lag sufficiency (R3)

Available control-plane metrics (`internal/pkg/metrics/control_plane.go:20-110`):

| Metric | Shape | Replay-relevant? |
|---|---|---|
| `auditPubOutcomes{status}` | cumulative counter, keyed by status only | Corroborates ("completed delta grew"), cannot isolate a batch |
| `callbacks{status}` | cumulative counter, status only | Same; terminal label is `success` for deliveries vs `completed` for publications — cross-kind asymmetry |
| `auditPurged{kind}` / `auditPurgeFail{kind}` | cumulative purge counts | Signals purge activity, not backlog |
| `auditClaimFail{phase}`, `auditPubSkipped`, `auditScopeMissing` | counters | Alerts only |
| `projectionLag{source\|dataset}` / `consumerLag{source}` | source-event lag gauges | **Unrelated to audit publications** — these track the account-projection replication subsystem |

Why metrics/lag cannot verify a replay, let alone a partial one:

1. **No lag metric exists for audit publications or callback deliveries.** The runbook's "metrics/lag" borrows vocabulary from other subsystems; for this feature there is nothing to watch.
2. **No current-state gauge.** There is no "dead_letter backlog" gauge — only cumulative outcome counters. The pre-replay backlog is not observable through metrics at all (the dry-run CLI is the only counter).
3. **No batch correlation.** Counters are global per status; normal audit and callback traffic continues during maintenance, so `completed`/`success` deltas mix unrelated work. The only batch marker — the requeue ledger facts (`audit.publication.requeued` / `callback.delivery.requeued`, minted in-tx) — lives in the ledger, invisible to metrics.
4. **Cross-kind partial replay is invisible to single-kind checks.** The two legs commit in separate transactions (§3); a kill between them leaves one kind requeued and the other `dead_letter`. Verifying only publications (or only the counter the operator happens to watch) declares success while deliveries sit unreplayed. The `manual_replay` error_code discriminator is transient — overwritten on the first finish — so it cannot serve post-hoc verification either.

The runbook's *own* correct check is step 4's first half (second run → `requeued 0`), provided it is run as `-dry-run -kind both` and both lines print 0 — and provided the backlog ≤ limit (§4). The reliable completion evidence is row-level: terminal status per kind (DB query), or the requeue facts in the ledger. Metrics can only corroborate (no new `dead_letter` deltas after the replay window, `completed` delta ≥ requeued count) — and even that is a heuristic, not proof.

## 7. Corrected runbook (replaces design §6.3)

1. **Pause.** Set `audit_governance.purge_pause: true` and restart. The flag is boot-captured (`internal/app/audit.go:52-53`); there is no reload — a file edit without a process restart changes nothing. Confirm the restart took effect (process start time; purge counters frozen). `data_retention.purge_paused: true` is unnecessary for this tool (it gates only the outbox `eventRow` cleaner, which `cmd/audit-replay` cannot race); set it only if outbox dead-letter rows are being handled by another tool in the same window.
2. **Dry-run.** `bin/audit-replay -dry-run -kind both`. The printed numbers are **capped at `-limit` (default 100) per kind** — they are not the backlog. For the true backlog, count via SQL (`SELECT status, count(*) FROM audit_publications WHERE status='dead_letter'` and the `callback_deliveries` twin), or raise `-limit` accordingly.
3. **Replay.** `bin/audit-replay -kind both -tenant <T> -actor <operator>`. Loop while any dry-run line is nonzero (each run drains the next `limit` rows per kind; the CLI is idempotent, so re-runs are safe). A killed CLI may have committed — the two kinds commit in separate transactions, so after any crash, re-run `-kind both` and verify **both** lines. `requeued 0` means "nothing left in this set", which is idempotency proof, not delivery proof.
4. **Verify.** (a) `bin/audit-replay -dry-run -kind both` → both lines `0`. (b) Completion: query row-level terminal state per kind (publications `completed`, deliveries `success`), or ledger facts `action IN ('audit.publication.requeued','callback.delivery.requeued')` for the run window. Metrics can corroborate (`auditPubOutcomes{completed}`, `callbacks{success}` deltas, no new `dead_letter` deltas) but cannot prove batch or cross-kind completeness — there is no lag metric for this subsystem and no per-batch correlation.
5. **Unpause.** Revert the flags and restart. Until then the drain's purge scan is suspended; rows that re-dead-letter during the window are preserved.

## 8. Optional hardening (not required; no code changed)

| Item | Rationale |
|---|---|
| Startup log/metric when the purge scan is paused (e.g., a boot line "purge paused: dead_letter retention N" or a `purge_paused` gauge) | Gives the operator a runtime confirmation that the restart honored the flag (R1) |
| `-dry-run` unbounded count escape hatch (e.g., document `-limit 0` as unbounded for the count path, or print `≥ limit` when the count is capped) | Removes the R2 estimate trap without touching the mutation bound |
| CLI journal/summary mode (`-report file` writing per-kind counts + fact IDs after commit) | Closes the after-commit-before-report ambiguity and the cross-kind partial marker gap (R6) with zero schema change |
| Crash-window integration tests (kill after commit before print; kill between legs) | Pins R6 explicitly instead of relying on tx semantics |
