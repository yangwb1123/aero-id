# Requirements — (module label `cmd/server`): cmd/audit-replay fails closed unless `audit_governance.purge_pause` is set — purge-race guard

| | |
|---|---|
| Module (batch label) | `cmd/server` — **label only**. The direction's change surface is `cmd/audit-replay` (+ verification in `internal/app`). `cmd/server/main.go` is not touched; the guard closes a hazard in the sibling operator binary against a drain-loop behavior owned by the `cmd/server` composition root |
| Source analysis | `docs/auto/analyses/cmd-server-7a3bfea7.json` (direction 2 of 3: replay/purge race) |
| Campaign | `aero-id-b2` (batch). Sibling spec for the same analysis file: `2026-08-08-cmd-server-dead-letter-replay.md` (direction 2 of the prior analysis, landed); this direction is the follow-on: the pause flag exists and gates the drain purge, but the replay tool never reads it |
| Design | `docs/designs/2026-08-07-audit-publication-dead-letter-replay-design.md` D-3 (`:70-79`) planned a `-purge-pause` tool flag that **never landed** — the tool has no purge guard at all (verified, §2). This direction implements the D-3 intent by the accepted design (config-driven fail-closed, §3.2) |
| Date | 2026-08-08 |
| Status | **Evidence re-verified against the working tree (all cited symbols live, line numbers current).** The direction's core claim — `cmd/audit-replay` never reads `PurgePause` while the drain purge honors it — holds verbatim. The implementation is **not yet landed**; §4 defines the requirements and their testable acceptance checks |

## 1. Problem statement (direction, preserved)

`internal/config/audit_governance.go` documents `PurgePause` as existing "so maintenance flows such as
cmd/audit-replay cannot race a purge", but the tool never reads it: grep of `cmd/audit-replay/main.go`
and its tests for `PurgePause`/`purge` returns zero hits (verified). `run()` loads the config and
proceeds to requeue dead-letter rows even when the live server's drain loop
(`internal/service/callback_worker.go` `purgeDeadLetters`) will keep DELETE-ing `dead_letter` rows
past `DeadLetterRetention` every tick (bounded to 50/kind, but unbounded across ticks), so a
long-running recovery can race a purge mid-leg — exactly the hazard the pause flag was built for, with
no enforcement. The `i7_boot_hazard` and backlog tests cover `-actor`, tenant filters, and partial
legs, but none cover the purge race.

## 2. Verified evidence (working tree; line numbers live)

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `cmd/audit-replay/main.go` — `run()` never reads `PurgePause` | ✅ holds. `run()` at `main.go:76-134`: flag parse (`:77-95`) → `-actor` fail-closed check (`:96-104`) → `config.Load` (`:106-109`) → signing-key preflight (`:111-115`) → `db.Open` (`:117-131`) → `replay(...)` (`:133`). `replay()` at `:141-172` receives the config and builds intents (`buildReplayIntents` `:175-191`) but no branch reads `cfg.AuditGovernance.PurgePause`; grep confirms zero `PurgePause`/`purge` hits anywhere in `cmd/audit-replay/` (verified: `grep -rn "PurgePause\|purge" cmd/audit-replay/` → no output) |
| E2 | `internal/config/audit_governance.go:35-37` — `PurgePause` doc claim | ✅ holds with drift: doc comment at `:37-39` — "PurgePause suspends the dead-letter purge scan in the drain loop (audit_publications and callback_deliveries) so maintenance flows such as cmd/audit-replay cannot race a purge. Static per boot (no hot reload)."; field `PurgePause bool` at `:40`. The doc promises a coordination guarantee the tool does not enforce |
| E3 | `internal/service/callback_worker.go:48-67` — `purgeDeadLetters` no-op only when `PurgePause` set | ✅ holds: `purgeDeadLetters` at `:58-83`; `if s.cfg.DeadLetterRetention <= 0 || s.cfg.PurgePause { return }` at `:65` — paused ticks perform no SELECT and no DELETE; unpaused, the scan deletes up to 50/kind per tick (`PurgeDeadLetterPublications(ctx, cutoff, 50)` `:68`, `PurgeDeadLetterDeliveries(ctx, cutoff, 50)` `:78`) with no bound across ticks. Ticker wiring at `:34-36` (`drainPublications` → `drain` → `purgeDeadLetters` per tick) |
| E4 | `internal/service/callback_worker.go:95-102` — purge metrics | ✅ drift only in line numbers: `ObserveAuditPublicationPurgeFailed`/`ObserveAuditPublicationPurged` at `:71-82`; interface at `internal/service/control_plane_metrics.go:27-28`. Metric observability exists; no replay-side coordination exists |
| E5 | `cmd/audit-replay/backlog_and_partial_test.go` — no purge-pause case | ✅ holds: the file's six tests cover bounded dry-run (`:22`), backlog>limit loop (`:51`), leg resume after partial (`:116`), commit-survives-missing-report (`:226`), empty backlog (`:276`), unbound-tenant preflight (`:298`). Grep confirms zero `PurgePause`/`purge` hits in the file |
| E6 | `internal/app/audit_integration_test.go` `deadLetterRowSnapshot` — the bit-for-bit preservation contract | ✅ holds: type at `:700-707`, `deadLetterSnapshot` at `:709-727`, `TestBuildAuditStackHonorsPurgeKnobs` at `:743-811` (leg 1 paused preservation via the `after != snapshot` assertion; leg 2 unpaused boundary purge). Fixture: `auditGovernanceTestConfig` `:444-462` (takes `purgePause` + `retention`), `newAuditGovernanceFixture` `:477-487`, `buildStack` `:490-503` — **the fixture discards the third `buildAuditStack` return value (`repository.FullAuditLedgerRepository`)**; the extension leg needs it retained (§4 REQ-3) |
| E7 | Composition-root copy: `internal/app/audit.go:52` maps `cfg.AuditGovernance.PurgePause` → `service.CallbackConfig.PurgePause` (`:50-52` in `internal/service/callback.go`); `purgeDeadLetters` honors it (`:65`) | ✅ — the server side of the contract is fully wired and tested (E6); only the tool side is missing |
| E8 | Replay test harness | ✅ `cmd/audit-replay/main_test.go`: `startReplayEnv` `:30-59` builds a hand-made `*config.Config` with **`AuditGovernance` zero-value (`PurgePause=false`)**; `runCapture` `:74-94` drives `replay(...)` directly (not `run()`); `parseFlags` `:96-134`; `replayConfigYAML` `:300-321` + `writeReplayConfig` `:323-331` drive the full `run([]string{...})` CLI path; `TestAuditReplaySigningKeyPreFlight` `:337-390` (run-level, dead DSN — proves config-layer rejections fire before the DB); `TestAuditReplayDBOutageFailsClosed` `:392-441` (leg 1 run-level with a dead DSN — asserts failure only, not the failure's identity) |
| E9 | Guard insertion points | ✅ `run()` reads the config at `:106-109` and opens the DB at `:117` — the guard slots between the signing-key preflight (`:111-115`) and `db.Open`, fail-closed before any connection. `replay()` is the extracted testable core (doc comment `:34-38`: "extracted from run so integration tests can drive the exact CLI semantics") with the full `cfg` in scope at `:141`. `validateAuditGovernance` (`internal/config/validate.go:215-227`) has **no** `purge_pause` rule — a config with `audit_governance: {purge_pause: true}` passes `config.Load` (zero-value anchor/retention accepted; activation gate only fires when `enabled: true`) |
| E10 | Replay surface for the app-level extension | ✅ `repository.AuditPublicationRepository` exposes `RequeuePublication(ctx, ids, limit, ReplayIntent) (int, []*domain.AuditFact, error)` (`internal/repository/audit_ledger.go:45-54`); the concrete repo returned by `NewAuditLedgerRepository` implements it (type-assert, precedent: `cmd/audit-replay/main_test.go:45-49`). `service.BuildAuditReplayIntent(ctx, kind, actor, tenantFilter, allowed, operationID) (repository.ReplayIntent, error)` at `internal/service/audit_replay.go:32`. Note: `FullAuditLedgerRepository` (`internal/repository/audit_wiring.go:9-13`) is only the append/read surface — the app test must type-assert to `AuditPublicationRepository` |
| E11 | Shipped config | `configs/config.yaml:188-189`: `purge_pause: false`, `dead_letter_retention: 0` (0 = never purge). Consequence of the accepted fail-closed guard: mutating replay against the shipped default config is refused until the operator sets `purge_pause: true` — a deliberate, documented behavior change (§3.2 D2) |
| E12 | Prior design intent | `docs/designs/2026-08-07-audit-publication-dead-letter-replay-design.md` D-3 (`:70-79`) planned a `-purge-pause` tool flag ("the landed tool has none") with the refusal condition `dead_letter_retention > 0 && purge_pause == false && flag absent`; the tool side of D-3 never landed (the config side did, E3/E7). The accepted direction supersedes D-3's mechanism (§3.2 D2) |

**Conclusion:** the direction's claim is verified in full — the pause flag gates the drain purge
(server side, tested) while the replay tool neither reads it nor any other purge coordination
(tool side, zero hits), so a mutating replay can race a purge exactly as described. No test in
`cmd/audit-replay` exercises the purge-pause interaction; the app-level snapshot test proves only
that the *drain* preserves rows under pause, not that *replay* is coordinated with it.

## 3. Current state and residual delta

### 3.1 Landed (keep-enforced; do not re-do)

- **Purge pause (server side):** `audit_governance.purge_pause` + `dead_letter_retention` gate the
  drain-loop purge scan of `audit_publications`/`callback_deliveries` (`callback_worker.go:58-83`),
  copied from config at the composition root (`app/audit.go:52`), documented
  (`audit_governance.go:37-40`, `configs/config.yaml:188-189`), and tested bit-for-bit
  (`TestBuildAuditStackHonorsPurgeKnobs`, `internal/app/audit_integration_test.go:743-811`).
- **Replay tool:** `cmd/audit-replay` — `run()`/`replay()` with `-actor` mandatory in mutating mode,
  signing-key preflight before any DB mutation, `-dry-run` bounded counts, in-tx per-tenant requeue
  facts, `-report` journal, idempotent re-runs. Suite: `main_test.go`, `i7_boot_hazard_test.go`,
  `backlog_and_partial_test.go` (all integration-tagged).
- **Gates:** `check-audit-swallow` and `check-anchored-event-ids` cover the replay surface.

### 3.2 Design decisions for this direction (accepted; do not re-litigate)

- **D1 — Refusal, no override flag.** The acceptance allows "refuse (or require an explicit
  `-ignore-purge-pause` override)"; this spec takes the **primary option: plain refusal**. No new
  flag surface; the error names the config key and the fix is one line in the same config file the
  server boots from.
- **D2 — Guard condition is the pause flag alone** (`mutating && !cfg.AuditGovernance.PurgePause` →
  refuse), per the accepted direction. The design doc's narrower D-3 condition
  (`dead_letter_retention > 0 && !purge_pause`) is **superseded**: the tool cannot verify the
  server's boot-time purge state from its own copy of the config, and the flag is the operator's
  unambiguous acknowledgment. Documented consequence: a config with `dead_letter_retention: 0`
  (never purge — the shipped default) also requires `purge_pause: true` for mutating replay; this is
  inert for the drain (pause with retention 0 is already a no-op) and costs one config line. Stated
  in `usage()` and in this spec's failure-mode table.
- **D3 — Guard at both entry points.** `run()` (CLI boundary: after `config.Load` + signing-key
  preflight, before `db.Open` — fail-closed before any connection) **and** the top of `replay()`
  (the extracted core the integration harness drives as "the exact CLI semantics"). The harness
  fixture `startReplayEnv` sets `AuditGovernance.PurgePause = true` so every existing mutating leg
  runs under the now-legal posture (one-line fixture change; no assertion changes).
- **D4 — Stable error text** for testability, containing the config key: `"audit_governance.purge_pause
  must be set for a mutating replay run (the server drain purge must be paused so replay cannot race
  it; -dry-run is unaffected)"`.
- **D5 — Documentation.** `usage()` and the package-doc "Usage:" block state the mutating-mode
  requirement and that `-dry-run` is unaffected.
- **D6 — Verification only in `internal/app`.** The replay-under-pause assertion extends the existing
  composition-root snapshot test (`TestBuildAuditStackHonorsPurgeKnobs`); it drives the repository
  replay surface with a `service.BuildAuditReplayIntent` intent — never the CLI (internal/app must
  not import cmd). The fixture retains the `repository.FullAuditLedgerRepository` returned by
  `buildAuditStack` and type-asserts it to `repository.AuditPublicationRepository` (E10).

### 3.3 Residual delta after implementation

1. `docs/designs/2026-08-07-audit-publication-dead-letter-replay-design.md` D-3 status: mark the
   tool-side guard as landed via this direction's config-driven design (D1/D2) instead of the
   planned `-purge-pause` flag. Documentation-only.

## 4. Requirements (supplied acceptance checks preserved; testable criteria)

### REQ-1 — Mutating-mode purge-pause guard in `cmd/audit-replay` (NEW)

> Acceptance 1 (first clause): "add a mutating-mode guard in cmd/audit-replay run() — refuse (or
> require an explicit -ignore-purge-pause override) when cfg.AuditGovernance.PurgePause is false,
> with dry-run unaffected"

Testable criteria:

1. **`run()` refuses before any DB connection.** With a config file that passes `config.Load`
   (valid `audit_ledger.signing_key`) and `audit_governance.purge_pause: false`, a mutating
   invocation (`-actor` set, no `-dry-run`) returns an error whose text contains `purge_pause` and
   does **not** contain `open db` or `connection` — the guard fires between the signing-key
   preflight and `db.Open` (dead-DSN technique of `TestAuditReplaySigningKeyPreFlight` proves the
   ordering).
2. **`replay()` refuses before any mutation.** With a live testpg DB, seeded dead-letter rows, and
   `cfg.AuditGovernance.PurgePause = false`, `replay(...)` (the `runCapture` path) returns the same
   guard error; dead-letter row count unchanged; zero ledger facts minted.
3. **Dry-run unaffected at both sites.** `-dry-run` with `purge_pause: false` does **not** hit the
   guard: at the `run()` level the failure (dead DSN) is the DB connection error, not the guard; at
   the `replay()` level the dry run succeeds, prints the bounded count, and mutates nothing.
4. Guard ordering keeps every existing fail-closed preflight intact: `-actor` required first,
   signing-key preflight second, purge guard third, DB open last — `TestAuditReplaySigningKeyPreFlight`
   and `TestAuditReplayDefaultInvocationRequiresActor` stay green unchanged.

### REQ-2 — CLI test: refusal for `purge_pause=false`, pass for `purge_pause=true` (NEW)

> Acceptance 1 (second clause): "add a CLI test asserting the refusal for purge_pause=false and a
> pass for purge_pause=true"

Testable criteria (`TestAuditReplayPurgePauseGuardCLI`, `cmd/audit-replay`, integration-tagged;
uses `run([]string{"-config", ...})` with `replayConfigYAML` extended by an
`audit_governance: {purge_pause: <bool>}` block — a minimal section that passes `config.Load`
per E9 — and a dead DSN):

1. `purge_pause: false`, `-actor` set → error contains `purge_pause`; error does **not** contain
   `open db`/`connection` (guard fired before the DB; exit 1 via `main`).
2. `purge_pause: true`, `-actor` set → error does **not** contain `purge_pause` and does indicate
   the DB connection failure (guard passed; the tool proceeded to `db.Open` — the pass proof).
3. `purge_pause: false`, `-dry-run` → error does **not** contain `purge_pause` (dry-run unaffected;
   the failure is the dead-DSN connection).
4. Existing run-level tests keep their intent: `TestAuditReplayDBOutageFailsClosed` leg 1's config
   gains `purge_pause: true` so the leg still exercises the DB outage (the guard is a precondition,
   not the failure under test); `TestAuditReplaySigningKeyPreFlight` is unaffected — `config.Load`
   rejects the bad keys before the guard is reachable.

### REQ-3 — Bit-for-bit snapshot test stays green and gains a replay-under-pause leg (EXTEND)

> Acceptance 1 (third clause): "keep the existing bit-for-bit dead-letter snapshot test
> (internal/app/audit_integration_test.go deadLetterRowSnapshot) green and extend it to assert
> replay under pause preserves rows"

Testable criteria (`TestBuildAuditStackHonorsPurgeKnobs`, `internal/app/audit_integration_test.go:743`):

1. **Unchanged legs stay green:** leg 1 paused preservation (`after != snapshot` must hold for the
   past-retention row `pastID`) and leg 2 unpaused boundary purge (past-retention row purged,
   inside-retention row + ledger fact kept) — no existing assertion is altered.
2. **New replay-under-pause leg (inside leg 1, drain ticker running, `PurgePause=true`,
   `retention=1h`):**
   a. Seed a second past-retention dead-letter row (`replayID`, `updated_at` backdated 10 days via
      SQL — the exact row the unpaused purge would delete; strongest canary).
   b. Assert `replayID`'s snapshot is preserved bit-for-bit while the drain ticks (same contract as
      the `pastID` assertion).
   c. Replay it while the purge is paused: type-assert the fixture's retained
      `repository.FullAuditLedgerRepository` to `repository.AuditPublicationRepository`; build the
      intent via `service.BuildAuditReplayIntent(ctx, "publication", "ops-bot", "", nil, domain.ID{})`;
      call `RequeuePublication(ctx, nil, 10, intent)`.
   d. Assert: `n == 1`; exactly one `audit.publication.requeued` fact minted with `tenant_id =
      'tenant-one'` (tenant derived from the row, never the intent); the `replayID` publication row
      is `pending` with `error_code = 'manual_replay'` and `attempt = 0`.
   e. Assert the paused drain does not interfere: after the replay and at least one further tick,
      `pastID`'s snapshot is **still** bit-for-bit unchanged (replay touched only the selected row;
      the paused purge still deletes nothing), and the requeued `replayID` row flows to `completed`
      through the still-running paused drain (`pollPublication`, the existing helper) — the full
      recovery chain works under pause.
3. **Fixture change:** `auditGovernanceFixture` retains the `repository.FullAuditLedgerRepository`
   returned by `buildAuditStack` (currently discarded via `_` at `:494`); `buildStack` assigns it.

### REQ-4 — Document the requirement in `usage()` (NEW)

> Acceptance 1 (final clause): "document the flag in usage()"

Testable criteria:

1. `usage()` output (and the package-doc "Usage:" block) contains the literal substring
   `purge_pause` and states that mutating mode requires `audit_governance.purge_pause: true` in the
   loaded config while `-dry-run` is unaffected. (There is no new CLI flag under D1; "the flag" is
   the existing `audit_governance.purge_pause` config key.)
2. New test `TestAuditReplayUsageDocumentsPurgePause` (`cmd/audit-replay`, integration-tagged)
   asserts `strings.Contains(usage(), "purge_pause")`.

### REQ-5 — Existing `cmd/audit-replay` suite stays green under the new posture (KEEP)

Testable criteria:

1. `startReplayEnv` sets `cfg.AuditGovernance.PurgePause = true` (one line, commented: mutating
   replay is only legal under pause after REQ-1) — every existing mutating leg
   (`TestAuditReplayCLIRunTwiceIsIdempotent`, `TestAuditReplayCLIRequeueFactWithTenant`,
   `TestAuditReplayCLIBacklogExceedsLimit`, `TestAuditReplayLegResumeAfterPartial`,
   `TestAuditReplayCommitSurvivesMissingReport`, `TestAuditReplayEmptyBacklogPrintsZeroWithoutFact`,
   `TestAuditReplayPreflightRefusesUnboundTenant`, the i7 suite, signing-key and DB-outage core
   legs) keeps its assertions with zero changes.
2. Dry-run tests (no `-actor`) keep passing with `PurgePause` false or true — the guard skips
   dry-run (REQ-1.3).

## 5. Compatibility constraints

- **No wire/proto/API/config change.** The guard reads the existing `audit_governance.purge_pause`
  key; no new YAML key, env var, flag, or validation rule. `validateAuditGovernance` is untouched
  (E9) — `purge_pause: true` with `dead_letter_retention: 0` remains legal (it is inert for the
  drain and now the required posture for mutating replay).
- **CLI surface:** no new flags (D1). `-dry-run` semantics unchanged (still the safe pre-flight).
- **Behavioral change (intentional):** a mutating run against a config with `purge_pause: false`
  now exits 1 before any DB connection; previously it proceeded. Dry runs and the server are
  unaffected. Operators following the pause-then-replay protocol (set `purge_pause: true`, restart
  the server, run the tool) see no change.
- **File-size gates:** `cmd/audit-replay/main.go` is 209 lines; the guard (~8 lines) + usage text
  keep it under 500. `run()`/`replay()` already carry the documented `//nolint:cyclop,funlen`.
- **`check-anchored-event-ids`:** the guard adds no event-ID mint site (the requeue facts' mint
  stays in `internal/repository`; the C2 allowlist for `cmd/audit-replay/main.go` is untouched).
- **`check-audit-swallow`:** the guard is a plain `return fmt.Errorf(...)` — no new swallow shape;
  the gate's P1/P2 patterns are unaffected.
- **Module boundary:** `internal/app` must not import `cmd/audit-replay`; REQ-3 drives the
  repository surface directly (E10).

## 6. Failure modes

| Failure | Behavior after this direction | Pinned by |
|---|---|---|
| Server drain purge armed (`dead_letter_retention > 0`) and `purge_pause: false`; operator starts mutating replay | Tool exits 1 before any DB connection with the guard error naming `purge_pause` — the mid-leg purge race is structurally unreachable | REQ-1.1, REQ-2.1 |
| Operator config has `purge_pause: false` but retention 0 (never purge — shipped default) | Mutating replay refused until `purge_pause: true` (deliberate over-strictness, D2); documented in `usage()`; dry-run still works | REQ-2.1/3, REQ-4 |
| Operator sets `purge_pause: true` and runs mutating replay | Guard passes; replay proceeds exactly as before (bounded batches, in-tx per-tenant facts, idempotent re-runs) | REQ-2.2, existing suite (REQ-5) |
| Operator runs `-dry-run` with `purge_pause: false` | Unaffected: counts print, nothing mutates | REQ-1.3, REQ-2.3 |
| Purge paused, replay requeues a row, drain keeps ticking | Requeued row flows to `completed` through the paused drain; other preserved dead-letter rows stay bit-for-bit unchanged | REQ-3.2d/e |
| Purge resumes (second boot, `purge_pause: false`) | Unpaused purge deletes exactly the past-retention dead-letter rows and keeps the ledger facts — leg-2 behavior unchanged | REQ-3.1 (existing leg 2) |
| `replay()` called directly with `PurgePause=false` (harness or future caller) | Core refuses before any intent/query — the fail-closed contract holds at the extracted core, not just the flag parser | REQ-1.2 |

## 7. Done-gate evidence (expected after implementation)

The implementation must keep the full local gate set green:

```text
make test                       # unit tests
make lint && make lint-strict   # go vet + golangci-lint (funlen 50, cyclop 10)
make check-file-sizes           # main.go stays <= 500 lines
make check-audit-swallow        # no new swallow shape
make check-anchored-event-ids   # no new C2 mint site
make check-integration-compile  # new integration tests compile (go test -tags integration -run '^$' + vet)
make build                      # bin/aero-id + bin/audit-replay
make test-integration           # required: all new tests and the extended app test are integration-tagged
                                # (testcontainers/Docker); also re-run internal/app + cmd/audit-replay suites
```

## 8. Testable acceptance mapping (supplied checks preserved verbatim)

| Supplied acceptance | Status | Testable criteria |
|---|---|---|
| "add a mutating-mode guard in cmd/audit-replay run() — refuse (or require an explicit -ignore-purge-pause override) when cfg.AuditGovernance.PurgePause is false, with dry-run unaffected" | **NEW — REQ-1/REQ-2** | Guard in `run()` (after config load + signing-key preflight, before `db.Open`) and at the top of `replay()`; refusal only in mutating mode; error contains `purge_pause`; dry-run proves unaffected at both sites (D1: refusal-only, no override flag) |
| "add a CLI test asserting the refusal for purge_pause=false and a pass for purge_pause=true" | **NEW — REQ-2** | `TestAuditReplayPurgePauseGuardCLI`: false → guard error before DB (no `open db`/`connection`); true → DB connection error (guard passed); plus the `replay()`-level refusal/pass legs with a live DB and zero-mutation asserts |
| "keep the existing bit-for-bit dead-letter snapshot test (internal/app/audit_integration_test.go deadLetterRowSnapshot) green" | **KEEP — REQ-3.1** | `TestBuildAuditStackHonorsPurgeKnobs` legs 1 and 2 unchanged and green |
| "and extend it to assert replay under pause preserves rows" | **NEW — REQ-3.2** | Replay-under-pause leg: past-retention canary row preserved bit-for-bit while paused, requeued via `RequeuePublication` + `BuildAuditReplayIntent` → pending/`manual_replay`/attempt 0, one tenant-derived fact, other preserved row untouched, requeued row completes through the paused drain |
| "document the flag in usage()" | **NEW — REQ-4** | `usage()` and the package doc contain `purge_pause` and state the mutating-mode requirement (dry-run unaffected); `TestAuditReplayUsageDocumentsPurgePause` asserts the substring |

## 9. Amendment register (stale or superseded wording in the direction / source analysis)

| # | Claim | Correction | Folded into |
|---|---|---|---|
| A1 | Direction evidence cites `callback_worker.go:95-102` for purge metrics | Metrics are at `:71-82` (`ObserveAuditPublicationPurged`/`...PurgeFailed`); the cited range is the drain ticker region. Substance unchanged | §2 E4 |
| A2 | Direction's acceptance mentions an optional `-ignore-purge-pause` override | Not implemented (D1): plain refusal is the acceptance's primary disjunct; no new flag surface | §3.2 D1, §5 |
| A3 | Design doc D-3 planned a `-purge-pause` tool flag with condition `dead_letter_retention > 0 && !purge_pause` | Superseded by the accepted direction: refusal keys on `purge_pause` alone (D2); the tool-side guard lands config-driven instead of flag-driven | §3.2 D2, §3.3 |
| A4 | Direction's problem statement implies only `run()` needs the guard | The guard also lands at the top of `replay()` — the extracted core the integration harness drives as the CLI's semantics (D3) | §3.2 D3, REQ-1.2 |
| A5 | `startReplayEnv` (test harness) builds `PurgePause=false` configs | Becomes `PurgePause=true` (REQ-5.1): after the guard, mutating replay is only legal under pause, so the harness models the legal posture | REQ-5 |

---

### Done-gate relevance

No production code has been written for this direction yet; this spec defines it. The implement
stage must land §4 (guard in `main.go`, fixture/usage changes, three new/updated test files) and
keep the §7 gate set green, including `make test-integration` (all cited tests are
integration-tagged and use real Postgres via testpg).
