# Requirements specification — outbox purge-pause flag (`purge_paused`) with validation

> Module: `internal/config` (direction B2-5 contribution: the `purge_paused` field on
> `DataRetentionConfig`, its validation, the cleaner gate it feeds, and tests proving a paused
> cleaner never deletes dead-letter rows).
> Source analysis: `docs/auto/analyses/internal-config-a932ee1e.json` (item 2).
> Date: 2026-08-07. Status: every cited file/symbol re-verified against the repository at HEAD.

## 1. Direction

Dead-lettering is shipped (`publicationOutcome` → `dead_letter` terminal state) and outbox retention
config exists (`DataRetentionConfig.OutboxDeadLetterRetention`, `OutboxPublishedRetention`,
`OutboxCleanInterval`), but no purge-pause flag exists: a replay run (once a `RequeuePublication`
surface lands) would race the outbox cleaner, which purges dead-letter rows after
`OutboxDeadLetterRetention` (7d default) before they can be requeued — silently losing audit facts.
The config-module contribution is `purge_paused` on `DataRetentionConfig`, validation (pause only
meaningful when the cleaner is enabled; negative retention rejected), and tests proving a paused
cleaner never deletes dead-letter rows. Scope is limited to this direction; `Requeue*` APIs and
`cmd/audit-replay` are explicitly out of scope (sibling direction, see §6).

## 2. Evidence verification

Every citation in the direction was checked against the repository. Substance holds; two line
references and two factual claims needed correction (marked "corrected").

| # | Claim | Verified location | Status |
|---|-------|-------------------|--------|
| E1 | `DataRetentionConfig{OutboxDeadLetterRetention, OutboxPublishedRetention, OutboxCleanInterval}` exists | `internal/config/config.go:352-371` — struct at 352, fields at 366/369/371 (citation said 290-308; the struct moved) | Confirmed (line range corrected) |
| E2 | `OutboxConfig{PollInterval, MaxRetries}` has no pause flag | `internal/config/config.go:49-52` — fields `poll_interval`, `max_retries` (+ `Webhook`); no pause field anywhere in the struct | Confirmed |
| E3 | `publicationOutcome` produces the `dead_letter` terminal state | `internal/service/callback_audit.go:90-101` — `return "dead_letter", "audit.publication_failed", ...` at line 98 when `attempt >= maxAttempts` (citation said line 92) | Confirmed (line corrected) |
| E4 | Drain loop is the only delivery path | `internal/service/callback.go:268-289` — `Start` ticker calls `s.drainPublications(workerCtx)` at line 281 then `s.drain(workerCtx)`; no purge, requeue, or pause anywhere in the loop (citation said line 267) | Confirmed (line corrected) |
| E5 | No `Requeue*` / `purge_paused` / `cmd/audit-replay` symbols anywhere | Repo-wide `grep -rn "Requeue\|purge_paused\|PurgePaused"` over `internal/`, `cmd/`, `configs/` finds nothing (only `PermAuditReplay`, a permission constant at `internal/domain/admin.go:18`, and owner-scoped `POST /deliveries/:id/replay` at `internal/handler/http/replay.go` — neither is a requeue API); `cmd/` contains only `migrate` and `server` | Confirmed |
| E6 | The outbox cleaner purges dead-letter rows after `OutboxDeadLetterRetention` | `internal/outbox/repository.go:135-163` — `RunOutboxCleaner` tick computes `deadCutoff = now.Add(-deadLetterRetention)` and calls `PurgeDeadLetters`; `PurgeDeadLetters` at `repository.go:89-131` deletes `outbox_events` rows where `dead_letter = true AND created_at < cutoff` (line 97) in batches of 500 | Confirmed |
| E7 | The cleaner is OPT-IN, wired only when interval and dead-letter retention are positive | `internal/app/new.go:345-350` — `if cfg.DataRetention.OutboxCleanInterval > 0 && cfg.DataRetention.OutboxDeadLetterRetention > 0 { stopOutboxClean = outbox.RunOutboxCleaner(...) }` | Confirmed |
| E8 | **Corrected:** "negative `OutboxDeadLetterRetention` rejected as today" | **False.** `internal/config/validate.go` has **no** `validateDataRetention` and no retention validation at all; the only retention-style check is `validateStorageGC` (`validate.go:154-157`), which requires `interval > 0 && retention > 0` **when enabled**. Negative retention is silently accepted today (`now.Add(-negative)` would put the cutoff in the future and purge every dead letter). The rejection is **new validation** added by this direction | Corrected (acceptance check preserved; it is implemented by new code) |
| E9 | **Corrected:** scheduling test assertion table | The cleaner operates on `outbox_events` (`dead_letter` bool, `repository.go:97`), **not** on `audit_publications` (`status = 'dead_letter'` text, `internal/repository/audit_publication.go:17`). `audit_publications` has no purge/cleaner today (repo-wide grep for purge on that table: none). The paused-cleaner assertion must target `outbox_events` rows with `dead_letter = true` | Corrected (check intent preserved: dead-letter rows survive a paused cleaner) |
| E10 | Dead-letter rows are created by the relay after retries | `internal/outbox/claim.go:135` — `dead_letter = (retry_count + 1 >= ?)`; `internal/outbox/relay.go:202` — retry ceiling before dead-lettering | Confirmed |
| E11 | Config is loaded once at boot (no hot reload) | `internal/config/config.go:374-393` — `Load` reads file + `AERO_ID_*` env and validates; no reload mechanism exists. "Purge resumes after the flag is cleared" therefore means: next boot with `purge_paused: false` | Confirmed |
| E12 | `purge_paused` belongs on `DataRetentionConfig`, not `OutboxConfig` | `OutboxConfig` feeds the **relay** only (`internal/app/outbox.go:63-66` — `NewRelay(..., deps.cfg.Outbox.MaxRetries, ...)`, `PollInterval`); the **cleaner** is fed directly from `cfg.DataRetention.*` (`new.go:345-350`). The direction offers both homes; the evidence picks `DataRetentionConfig`, next to `OutboxCleanInterval` | Confirmed (placement justified) |

**Conclusion:** the direction's problem statement is accurate. The outbox cleaner races any future
dead-letter replay, and the pause flag + validation + cleaner gate are the config-module fix. The two
corrections (E8: retention validation is new, not existing; E9: the purged table is `outbox_events`)
change the test targets but not the acceptance checks' intent.

## 3. Gap summary

| Capability | Today | Required |
|---|---|---|
| Pause outbox retention purge during dead-letter replay | Not possible — `RunOutboxCleaner` ticks unconditionally (`repository.go:135-163`) | `data_retention.purge_paused` honored by the cleaner |
| Reject nonsensical pause config | No validation of `DataRetentionConfig` at all (E8) | `purge_paused=true` accepted only when the cleaner is enabled (interval and dead-letter retention > 0, matching the wiring gate at `new.go:345`) |
| Reject negative retention | Silently accepted — a negative `OutboxDeadLetterRetention` would put `deadCutoff` in the future and purge all dead letters on the next tick (E6, E8) | Negative outbox retentions rejected at `validate()` |
| Prove a paused cleaner never deletes dead-letter rows | No `RunOutboxCleaner` test exists (`internal/outbox` has only relay/claim/webhook tests; `relay_test.go:158` stubs `PurgeDeadLetters` for the fake repo) | Deterministic scheduling/skip test (T-5.2) |
| Requeue dead-letter rows | No `Requeue*` API (E5) | **Out of scope** — sibling direction; this module's flag is its precondition |

## 4. Requirements

### 4.1 Config field

**CFG-1 — `purge_paused` flag on `DataRetentionConfig`.**
Add to `internal/config/config.go` (after `OutboxCleanInterval`, line 371):

```go
// PurgePaused suspends the outbox retention cleaner (dead-letter and published
// purges) so maintenance flows such as dead-letter replay cannot race a purge.
// Meaningful only while the cleaner is enabled (outbox_clean_interval > 0 and
// outbox_dead_letter_retention > 0); see validateDataRetention.
PurgePaused bool `mapstructure:"purge_paused"`
```

Semantics: when `true`, the outbox cleaner performs **no purge work** — no SELECT, no DELETE, no row
mutation; every `outbox_events` row (dead-letter or published) is preserved bit-for-bit. `false`
(default) is the current behavior. Environment override `AERO_ID_DATA_RETENTION_PURGE_PAUSED`
follows the existing viper mechanism (`config.go:378-384`). Document the field in the
`data_retention` block of `configs/config.yaml` (block at lines 227-236, e.g.
`purge_paused: false  # maintenance: suspend outbox retention purge so dead-letter replay cannot race it`)
and add one paragraph to `docs/DATA_RETENTION.md` §3 (Outbox Events Cleaner, lines 87-110).

The flag is static per process: `Load` runs once at boot (E11), so "purge resumes only after the flag
is cleared" means a restart/rollout with `purge_paused: false`. No hot reload.

*Acceptance: T-5.1.*

### 4.2 Validation

**CFG-2 — `validateDataRetention`, hooked into the existing validation chain.**
Add `validateDataRetention(value DataRetentionConfig) error` in `internal/config/validate.go` and
append it to the checks list in `validateRuntimeLimits` (`validate.go:125-139`). Rules (error
substrings are the testable contract):

1. `PurgePaused && OutboxCleanInterval <= 0` → error
   `"data_retention.purge_paused requires data_retention.outbox_clean_interval > 0"` (pause is
   meaningless when the cleaner never runs; this is the acceptance's "error otherwise" case).
2. `PurgePaused && OutboxDeadLetterRetention <= 0` → error
   `"data_retention.purge_paused requires data_retention.outbox_dead_letter_retention > 0"` (the
   cleaner is wired only when both are positive — `new.go:345`; a flag that does not gate a running
   cleaner would be silently inert).
3. `OutboxDeadLetterRetention < 0` → error
   `"data_retention.outbox_dead_letter_retention must not be negative"` (NEW validation, E8; prevents
   the future-cutoff purge that would delete every dead letter).
4. `OutboxPublishedRetention < 0` → error
   `"data_retention.outbox_published_retention must not be negative"` (same hazard for published rows).
5. `PurgePaused = false` with any zero/positive values → no error; `0 = disabled` convention and all
   existing configs stay accepted (including `configs/config.yaml` and the `config_test.go` sample).

No other retention fields are validated (audit/notification negative retentions remain unchanged —
non-goal, §6).

*Acceptance: T-5.1.*

### 4.3 Cleaner gate

**CLEAN-1 — `RunOutboxCleaner` honors the pause flag; tick and loop are directly testable
(determinism contract).**
In `internal/outbox/repository.go`:

- Change the signature to
  `RunOutboxCleaner(repo Repository, log *zap.Logger, deadLetterRetention, publishedRetention, interval time.Duration, purgePaused bool) func()`.
  When `purgePaused` is true, each tick performs no purge work (the purge call is skipped entirely;
  the goroutine and stop function behave exactly as today; `interval <= 0` still returns a no-op stop).
- Extract the per-tick purge body into a package-level function with an **injected clock**:
  `tickOnce(ctx context.Context, repo Repository, deadLetterRetention, publishedRetention time.Duration, purgePaused bool, now time.Time) (int64, error)`
  (or equivalently a method on a small `outboxCleaner` struct carrying repo/retentions/pause).
  When `purgePaused`, it returns `0, nil` **without touching the repository** (no SELECT, no DELETE).
  Cutoffs are derived from the injected `now` (`now.Add(-deadLetterRetention)`,
  `now.Add(-publishedRetention)`), never from a wall clock inside the function, so tests assert the
  exact cutoffs passed to `PurgeDeadLetters`.
- Extract the ticker loop into a package-level driver with an **injected tick source**:
  `runCleanerLoop(ctx context.Context, ticks <-chan time.Time, c *outboxCleaner)` (unexported).
  `RunOutboxCleaner` becomes a thin wrapper: construct the cleaner, `time.NewTicker(interval)`,
  `runCleanerLoop(ctx, ticker.C, c)`. Tests feed synthetic tick values through the channel and cancel
  `ctx`; **no test sleeps, no short intervals, no "wait for N ticks" polling** — call counts are exact
  (`N` ticks → `N` calls, or `0` calls when paused).
- The existing `PurgeDeadLetters` SQL (`repository.go:89-131`) is unchanged — pause is a caller-side
  gate, not a SQL change. No migration.

*Acceptance: T-5.2, T-5.3.*

### 4.4 Wiring

**WIRE-1 — composition root passes the flag.**
In `internal/app/new.go:346-350`, pass `cfg.DataRetention.PurgePaused` as the new last argument to
`outbox.RunOutboxCleaner`. Because CFG-2 rule 2 guarantees `OutboxDeadLetterRetention > 0` whenever
`purge_paused=true`, a validated config with the flag set always has the cleaner wired
(`new.go:345`), so the flag is never inert in production.

*Acceptance: T-5.3.*

## 5. Acceptance

The supplied acceptance check is preserved verbatim, then decomposed into deterministic, testable
cases:

> **T-5 (supplied):** config unit checks — `purge_paused=true` accepted only when a retention cleaner
> interval > 0 is set (error otherwise); negative `OutboxDeadLetterRetention` rejected as today;
> scheduling test with `purge_paused=true` => dead-letter purge step skipped while `audit_publications`
> rows with status='dead_letter' remain; e2e: enqueue publication to dead_letter, set purge_paused,
> requeue via RequeuePublication (once it lands), assert delivery completes and the row leaves
> dead_letter; purge resumes only after the flag is cleared.

**T-5.1 — config unit checks (`internal/config/config_test.go`, table-driven `Load` + direct
`validateDataRetention` calls).** Follow the existing `writeTemp`/yaml-fixture pattern
(`config_test.go:37-46`) and the substring-error assertion pattern (`config_test.go:164-185`):

| Case | Config | Expected |
|---|---|---|
| pause with cleaner disabled | `purge_paused: true`, `outbox_clean_interval: 0` | `Load` error containing `outbox_clean_interval` |
| pause with cleaner enabled | `purge_paused: true`, `outbox_clean_interval: 1h`, `outbox_dead_letter_retention: 168h` | `Load` succeeds; `cfg.DataRetention.PurgePaused == true` |
| pause with no dead-letter retention | `purge_paused: true`, `outbox_clean_interval: 1h`, `outbox_dead_letter_retention: 0` | `Load` error containing `outbox_dead_letter_retention` |
| negative dead-letter retention | `outbox_dead_letter_retention: -1h` | `Load` error containing `must not be negative` (rejection preserved from the supplied check; implemented by new CFG-2 validation — E8) |
| negative published retention | `outbox_published_retention: -1h` | `Load` error containing `must not be negative` |
| defaults unchanged | flag absent, `outbox_clean_interval: 0` | `Load` succeeds, `PurgePaused == false` |
| env override | `t.Setenv("AERO_ID_DATA_RETENTION_PURGE_PAUSED", "true")` + file with `outbox_clean_interval: 1h`, `outbox_dead_letter_retention: 168h` | `Load` succeeds; `PurgePaused == true` (env cannot bypass validation: same env with interval 0 still errors) |

Existing config tests (sample fixture, audit-ledger, event-ingest, audit-governance gate) stay green —
the new validation must not reject any currently accepted config.

**T-5.2 — scheduling tests: paused cleaner never deletes dead-letter rows; unpaused cleaner
demonstrably purges (`internal/outbox/repository_test.go`, unit, recording fake).**
Use a recording fake implementing `Repository` (precedent: `relay_test.go:158` stubs
`PurgeDeadLetters` for `fakeRepo`). The cleaner fake records **every** `PurgeDeadLetters` call
(`cutoff`, `publishedCutoff`, `batchSize`) and simulates deletion with the **exact SQL predicate**
(`dead_letter = true AND created_at < cutoff` OR `published_at IS NOT NULL AND created_at < publishedCutoff`),
so a recorded call has observable row consequences and a broken recorder fails the positive branch first.

**Rule: absence-assertions are permitted only paired with a positive control in the same test** (same
fake, same seed). A zero-call assertion is meaningful only because a sibling branch proves the
recorder and the loop are live. The five test cases below implement this rule (item 6 is the E9
table-correction note, not a test):

1. `TestCleanerTick_PausedSkips` — seed a dead-letter row older than `deadLetterRetention`, a published
   row older than `publishedRetention`, a dead-letter row inside retention, and a live unpublished row.
   - `tickOnce(..., purgePaused=true, now=fixed)` → returns `0, nil`; **`PurgeDeadLetters` never called**
     (`len(calls) == 0`); every row remains bit-for-bit (`dead_letter`, `created_at`, `retry_count`
     unchanged) — nothing lost.
   - **Positive control (same fake, same seed, immediately after):** `tickOnce(..., purgePaused=false,
     now=fixed)` → `len(calls) == 1`; the recorded cutoffs equal **exactly**
     `fixed.Add(-deadLetterRetention)` / `fixed.Add(-publishedRetention)`; rows past the cutoffs are
     deleted (2 rows), in-retention and live rows survive. A recorder that never records fails here
     first, so the paused branch's `len(calls) == 0` cannot pass vacuously.
2. `TestCleanerLoop_FlagOffPurgesEveryTick` — deterministic loop test, **no ticker**: feed 3 synthetic
   ticks into `runCleanerLoop` (buffered channel, then cancel `ctx`); assert **exactly 3** recorded
   calls in order, each with the injected-`now`-derived cutoffs; cutoff-eligible rows are gone after
   the run. This is the "purge still fires when the flag is off" control and the gate positive control
   (cleaner constructed with `interval > 0 && deadLetterRetention > 0`, i.e. gate conditions hold).
3. `TestCleanerLoop_PausedSkipsEveryTick` — same structure with `purgePaused=true`: 3 ticks → **0**
   recorded calls, all rows unchanged. Paired with case 2 in the same test file (shared fake type),
   so the absence assertion is only passable when the loop demonstrably records. This is the "purge
   resumes only after the flag is cleared" case: a config without the flag (or a restart with it
   cleared) purges on the next tick.
4. `TestRunOutboxCleaner_PublicAPI` — smoke at the public entry; the only wall-clock-touching test
   (bounded polling precedent: `relay_test.go` `TestRelay_DrainOnStop`).
   `RunOutboxCleaner(..., interval = time.Millisecond, purgePaused=false)` → poll (≤ 2s) until
   `len(calls) >= 1`; then `stop()` returns promptly and a second `stop()` does not panic. **The same
   test then runs the `purgePaused=true` twin and asserts `len(calls) == 0` over the same bounded
   window** — valid only because the unpaused twin already proved the goroutine starts and the
   recorder works. The paused branch asserts no lower-bound timing, so it cannot flake.
5. `TestCleanerLoop_ReplayVsPurge` — **explicit race test (F3)**, deterministic forced interleaving,
   additionally exercised under the race detector by CI (`ci.yml` runs `go test ./... -race`):
   a simulated replay worker (in-test goroutine) snapshots the dead-letter set, then "requeues"
   (`dead_letter = false` on the snapshot IDs), mirroring the sibling `RequeuePublication` shape.
   - **Unpaused — hazard demonstration:** worker snapshots; the test drives one tick (purge deletes
     the snapshot rows); the worker requeues → 0 rows affected, rows gone. Asserts the exact
     replay-vs-purge row loss the flag exists to prevent.
   - **Paused — protection invariant:** repeat the snapshot→tick→requeue cycle N ≥ 50 times with
     `purgePaused=true`; every snapshot row is requeued (affected == snapshot size), zero deletions.
   Interleaving is enforced with channels (no sleeps); the worker is a real goroutine so `-race`
   covers the concurrent access. The SQL-level interleaving test against real Postgres is the
   sibling's obligation once `RequeuePublication` lands (T-5.4 handoff).
6. **Table correction (E9):** the preserved assertion "dead-letter rows remain" is checked against
   `outbox_events` rows with `dead_letter = true` (the cleaner's table, `repository.go:97`).
   `audit_publications` rows with `status = 'dead_letter'` are untouched by this cleaner today — that
   table's purge/pause is the sibling direction's scope (§6); asserting it here would test a no-op.

**Integration purge test — MANDATORY, not optional** (`TestOutboxRepo_PurgeDeadLettersPauseGate` in
`internal/outbox/repository_integration_test.go`, existing `testpg` harness, `integration` build tag):
seed real rows (dead-letter past cutoff, dead-letter within retention, published past
`publishedCutoff`, live row); `tickOnce(..., purgePaused=true)` → **zero rows deleted**; then
`tickOnce(..., purgePaused=false)` → exactly the cutoff-eligible rows deleted, others intact.

**Decision rationale (destructive-deletion semantics):** (a) the acceptance's core claim is
*deletion* behavior on `outbox_events`; the ground truth for "deletes exactly the eligible rows and
nothing else" is the SQL boundary, and `PurgeDeadLetters` currently has **zero** integration coverage
(verified: no reference in `repository_integration_test.go`); (b) this direction gates the *only
caller* of that destructive DELETE, so the gate must be certified against the real path, not a fake's
simulation of it; (c) an optional integration test is itself the absence-assertion failure mode this
hardening pass eliminates — acceptance could pass with deletion semantics asserted only against the
fake's own (tautological) simulation; (d) cost is bounded: one test in the existing harness, one
`make test-integration` run in the implement stage. The repo gate ("integration after SQL changes")
is not violated — no SQL changes here — but net-new coverage of a destructive path is required by
this direction's own acceptance. The test stays behind the `integration` build tag (Docker-gated)
per repo convention.

**T-5.3 — wiring and regression (positive wiring control, not compile-only).** A compile check proves
only that an argument exists; it cannot prove the flag value reaches the cleaner. Therefore:

- The composition root is extracted to `buildOutboxCleaner` (in `internal/app/outbox.go`, per design)
  and tested in `internal/app/outbox_wiring_test.go` (precedent: `audit_wiring_test.go` fake-repo
  pattern): a recording app-level fake outbox repo + `Config{DataRetention: {OutboxCleanInterval: 1h,
  OutboxDeadLetterRetention: 168h, PurgePaused: true}}` → cleaner runs, **0 purge calls** over a
  bounded window; same config with `PurgePaused: false` → **≥ 1 purge call** (same-test pairing, so
  the zero is meaningful). **Gate positive controls:** `OutboxCleanInterval: 0` or
  `OutboxDeadLetterRetention: 0` (either) → no purge calls and a prompt, non-panicking stop — proving
  the `new.go:345` wiring gate is not inverted and the flag is never inert in production.
- `make build` remains a smoke check; `make test && make lint && make lint-strict &&
  make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make build`
  stay green.
- `make test-integration` is **required once** in the implement stage to run the mandatory
  integration purge test (T-5.2 decision); no other integration surface changes.

**T-5.4 — e2e (deferred, cross-direction dependency).** The supplied e2e — "enqueue publication to
dead_letter, set purge_paused, requeue via RequeuePublication (once it lands), assert delivery
completes and the row leaves dead_letter; purge resumes only after the flag is cleared" — depends on
`RequeuePublication`, which does not exist (E5) and is the sibling direction's deliverable. It is
preserved verbatim as the acceptance of the combined B2-5 work. This module's guarantee is its
precondition: with `purge_paused=true` the retention cleaner holds every dead-letter row indefinitely
(T-5.2), so no replay run can race a purge between requeue and drain. The handoff contract for the
sibling direction: (1) the flag is `data_retention.purge_paused` (static per boot, E11); "resumes" =
next boot with the flag cleared; (2) **sibling obligation — SQL-level race test:** when
`RequeuePublication` lands, the sibling must add a real-Postgres interleaving test (concurrent
`RequeuePublication` and `PurgeDeadLetters` on the same rows) proving the requeue cannot lose rows
that a purge deletes mid-flight; this direction's race test (T-5.2 case 5) covers only the scheduling
boundary with a simulated replay.

## 6. Non-goals (scope boundary)

- No `RequeueDead`/`RequeuePublication` APIs, no `cmd/audit-replay`, no purge scan for
  `audit_publications`/`callback_deliveries`, no `audit_governance.purge_pause` flag — all belong to
  the sibling direction (`docs/requirements/2026-08-06-dead-letter-replay-configs-requirements.md`).
  The two flags are distinct blocks (`data_retention.purge_paused` vs `audit_governance.purge_pause`)
  and coexist without collision.
- No hot reload of the flag; no dynamic toggle API (config is loaded once at boot, E11).
- No validation of `audit_retention`, `notification_retention`, `notification_unread_retention`
  negatives — out of scope for an outbox-purge direction; their behavior is unchanged.
- No changes to `PurgeDeadLetters` SQL, relay retry/dead-letter thresholds (`claim.go:135`,
  `relay.go:202`), `publicationOutcome` (`callback_audit.go:90-101`), or the drain loop
  (`callback.go:268-289`).
- No schema migration; no metrics changes (the cleaner's existing `log.Warn`/`log.Info` lines at
  `repository.go:157-162` are the observability surface).
- No changes to `OutboxConfig` (relay config, E12).

## 7. Impact map

| File | Change |
|---|---|
| `internal/config/config.go:371` (after) | add `PurgePaused bool` to `DataRetentionConfig` with doc comment |
| `internal/config/validate.go:125-139` | append `validateDataRetention` to the `validateRuntimeLimits` checks |
| `internal/config/validate.go` (new func) | `validateDataRetention` — CFG-2 rules 1-4 |
| `internal/config/config_test.go` | T-5.1 table-driven cases (yaml fixtures + env override) |
| `internal/outbox/repository.go:135-163` | `RunOutboxCleaner` gains `purgePaused bool`; tick body extracted to injected-clock `tickOnce`; loop extracted to injected-tick `runCleanerLoop`; skip purge when paused |
| `internal/outbox/repository_test.go` (new) | T-5.2 recording-fake scheduling tests (cases 1-4) + `TestCleanerLoop_ReplayVsPurge` race test (case 5) |
| `internal/outbox/repository_integration_test.go` | T-5.2 mandatory `TestOutboxRepo_PurgeDeadLettersPauseGate` (destructive SQL boundary, `integration` tag) |
| `internal/app/outbox.go` | extract `buildOutboxCleaner` (wiring gate + flag pass-through) |
| `internal/app/outbox_wiring_test.go` (new) | T-5.3 positive wiring control: flag value reaches the cleaner; gate conditions verified |
| `internal/app/new.go:346-350` | pass `cfg.DataRetention.PurgePaused` to `RunOutboxCleaner` |
| `configs/config.yaml:227-236` | add `purge_paused: false` with comment in `data_retention` |
| `docs/DATA_RETENTION.md:87-110` | §3 note: flag semantics and replay-race rationale |

## 8. Engineering constraints

- New/edited non-test files stay ≤ 500 lines (`repository.go` is 163 today; the additions fit),
  functions ≤ 50 lines, cyclomatic ≤ 10 (`make check-file-sizes`, `make lint-strict`).
- Unit tests use deterministic fakes — no real DB, no network; the scheduling tests drive `tickOnce`
  with an injected clock and `runCleanerLoop` with an injected tick channel (no ticker timing, no
  sleeps; only the public-API smoke test touches a real ticker, with bounded presence polling). One
  mandatory integration test (T-5.2) covers the destructive SQL boundary behind the existing
  `integration` build tag.
- `make test && make lint && make lint-strict && make check-file-sizes && make build` must stay green.
- No `.proto` changes; no migration files; generated code untouched.
- Error messages are stable dotted config paths with testable substrings (§4.2).

## 9. Open decisions (low risk, implementer choice)

1. **RESOLVED (T-5 hardening):** `tickOnce(ctx, repo, deadLetterRetention, publishedRetention,
   purgePaused, now)` free function (or `outboxCleaner.tickOnce(ctx, now)` method) with injected
   `now`; the loop is driven by `runCleanerLoop(ctx, ticks <-chan time.Time, c *outboxCleaner)` with
   injected ticks. The pause decision, the clock, and the tick source must all be injectable;
   wall-clock `time.Now()` and `time.NewTicker` appear only inside the `RunOutboxCleaner` wrapper.
2. Exact wording of the `configs/config.yaml` comment and the `docs/DATA_RETENTION.md` paragraph.
3. **RESOLVED (T-5 hardening):** logging stays in `runCleanerLoop` so `tickOnce` is pure; a single
   `log.Debug` per paused tick is acceptable; no new metrics per §6.
