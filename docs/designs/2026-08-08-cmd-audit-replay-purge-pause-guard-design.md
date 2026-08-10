# Design — Mutating-mode purge-pause guard in `cmd/audit-replay` (fail-closed purge race guard)

Direction: **Make cmd/audit-replay fail closed unless `audit_governance.purge_pause` is set** — replay
cannot race the drain loop's dead-letter purge scan.

Requirements: `docs/requirements/2026-08-08-cmd-server-audit-replay-purge-pause-guard.md` (REQ-1…REQ-5,
§8 acceptance mapping — preserved verbatim).

Module: `cmd/audit-replay` (+ test-only changes in `internal/app/audit_integration_test.go`).

Stage: design (aggregating the requirements spec; nothing landed yet — the spec's §"Done-gate relevance"
confirms no production code exists for this direction).

---

## 0. Evidence verification — untrusted claims → verified facts

Every claim in the supplied evidence and in the requirements spec was re-checked against the working
tree with `grep`/`sed`/`read`. **All hold.** Two internal inconsistencies in the requirements spec were
found while designing the test legs and are resolved in §3 (D-7, D-8); one stale line-count figure is
corrected (§8).

| Evidence claim | Verification (working tree, 2026-08-08) |
|---|---|
| Requirements doc exists at `docs/requirements/2026-08-08-cmd-server-audit-replay-purge-pause-guard.md`, 279 lines | ✅ Exact path and length |
| E1 — `run()` at `cmd/audit-replay/main.go:76-134` never reads `PurgePause`; zero `purge` hits in `cmd/audit-replay/` | ✅ `run()` at :76; flag parse :77-95 → `-actor` fail-closed :96-104 → `config.Load` :106-109 → signing-key preflight :111-115 → `db.Open` :117-131 → `replay(...)` :133. `grep -rn "PurgePause\|purge" cmd/audit-replay/` → zero hits across all four files |
| E2 — `internal/config/audit_governance.go:35-40`: doc promises a coordination guarantee the tool doesn't enforce | ✅ Doc comment :37-39 ("…so maintenance flows such as cmd/audit-replay cannot race a purge. Static per boot (no hot reload)."), field `PurgePause bool` at :40 |
| E3 — `internal/service/callback_worker.go` purge no-op guard at :65 | ✅ `purgeDeadLetters` at :58-83; `if s.cfg.DeadLetterRetention <= 0 \|\| s.cfg.PurgePause { return }` at :65; 50/kind per tick (`PurgeDeadLetterPublications(ctx, cutoff, 50)` :68, `PurgeDeadLetterDeliveries(ctx, cutoff, 50)` :78); ticker wiring :34-36 (`drainPublications` → `drain` → `purgeDeadLetters` per tick) |
| E4 — metrics at :71-82 (A1 drift in the direction's :95-102 citation) | ✅ `ObserveAuditPublicationPurgeFailed`/`ObserveAuditPublicationPurged` at :71-82; interface at `internal/service/control_plane_metrics.go:27-28` |
| E5 — `backlog_and_partial_test.go` six tests, zero purge mentions | ✅ Tests at :22/:51/:116/:226/:276/:298; `grep -c "urge"` → 0 in all four `cmd/audit-replay` files |
| E6 — `deadLetterRowSnapshot` at :700-707, `TestBuildAuditStackHonorsPurgeKnobs` at :743-811, fixture discards the replay-capable repo via `_` at :494 | ✅ Type :700-707; `deadLetterSnapshot` :709-727; test :743-811 (leg 1 `after != snapshot` :773; leg 2 boundary purge :798/:807); fixture `buildStack` :490-503 with `callbackSvc, auditLedger, _, err := buildAuditStack(...)` — the `_` is the `repository.FullAuditLedgerRepository` third return |
| E7 — composition-root copy `internal/app/audit.go:52` | ✅ `PurgePause: cfg.AuditGovernance.PurgePause` at app/audit.go:52 (with `DeadLetterRetention` :53); `CallbackConfig.PurgePause` at service/callback.go:50-52 |
| E8 — harness: `startReplayEnv` :30-59 (zero-value `AuditGovernance` → `PurgePause=false`), `runCapture` :74-94, `parseFlags` :96-134, `replayConfigYAML` :300-321, `writeReplayConfig` :323-331, `TestAuditReplaySigningKeyPreFlight` :337-390 (dead-DSN run-level proof), `TestAuditReplayDBOutageFailsClosed` :392-441 | ✅ All line numbers current. `replayConfigYAML` has **no** `audit_governance` section today |
| E9 — `validateAuditGovernance` has no `purge_pause` rule | ✅ `internal/config/validate.go:215-227`: only rejects negative `dead_letter_retention`; `purge_pause: true` with retention 0 passes `config.Load` (inert for the drain, legal for the tool) |
| E10 — replay surface: `RequeuePublication` at `audit_ledger.go:45-54`, `service.BuildAuditReplayIntent` at `audit_replay.go:32`, `FullAuditLedgerRepository` at `audit_wiring.go:9-13`; type-assert precedent at `main_test.go:45-49` | ✅ All live. `buildAuditStack` returns `(service.CallbackService, service.AuditLedgerService, repository.FullAuditLedgerRepository, error)` — the third return is exactly what the fixture discards |
| E11 — shipped config `configs/config.yaml:188-189` `purge_pause: false`, `dead_letter_retention: 0` | ✅ |
| E12 — prior design D-3 (`docs/designs/2026-08-07-audit-publication-dead-letter-replay-design.md:70-79`) planned a `-purge-pause` tool flag that never landed | ✅ D-3 section present with the flag plan; landed tool has no flag; config-side guard (`callback.go:305-311` era) is now `callback_worker.go:58-83` |

**Conclusion:** the requirements spec's evidence is accurate and current. The direction's core claim —
the tool neither reads `PurgePause` nor coordinates with the purge in any way while the drain honors it
— holds verbatim. The implementation has not landed; this design implements it.

---

## 1. API changes

No wire/proto/HTTP/gRPC/DB-schema API changes. The only behavioral surface is the CLI's exit
semantics in mutating mode, plus test fixtures.

### 1.1 `cmd/audit-replay/main.go` — the guard (production surface, ~12 lines)

New package-level helper, placed above `run()`:

```go
// errPurgePauseRequired is the stable fail-closed refusal for a mutating
// replay run against a config that does not pause the server's dead-letter
// purge scan. The text names the config key (one-line fix in the same file
// the server boots from) and is asserted by tests at both entry points.
var errPurgePauseRequired = errors.New(
	"audit_governance.purge_pause must be set for a mutating replay run (the server drain purge must be paused so replay cannot race it; -dry-run is unaffected)")

// checkPurgePause refuses a mutating replay unless the config pauses the
// drain purge (callback_worker.purgeDeadLetters honors purge_pause; the tool
// must too, or a long-running recovery races the per-tick DELETE). Dry runs
// build no intent, mutate nothing, and stay allowed.
func checkPurgePause(cfg *config.Config, dryRun bool) error {
	if dryRun || cfg.AuditGovernance.PurgePause {
		return nil
	}
	return errPurgePauseRequired
}
```

`run()` — insert between the signing-key preflight (`:113-115`) and `db.Open` (`:117`), fail-closed
before any connection:

```go
	if strings.TrimSpace(cfg.AuditLedger.SigningKey) == "" {
		return fmt.Errorf("audit ledger signing key is not configured")
	}
	// Fail closed before any DB connection: a mutating replay must not race the
	// server drain loop's dead-letter purge scan (purgeDeadLetters honors
	// audit_governance.purge_pause; the tool must too). Dry runs are unaffected.
	if err := checkPurgePause(&cfg, *dryRun); err != nil {
		return err
	}

	gdb, err := db.Open(db.Options{
```

`replay()` — the guard as the **first statement**, before the `limit <= 0` normalization, so the
extracted core (what the integration harness drives as "the exact CLI semantics") refuses before any
intent/query:

```go
func replay(gdb *gorm.DB, cfg *config.Config, ids []domain.ID, kind string, limit int,
	tenant, actor string, dryRun bool, reportPath string) error {
	if err := checkPurgePause(cfg, dryRun); err != nil {
		return err
	}
	if limit <= 0 {
```

`usage()` — one appended paragraph (and a matching line in the package-doc "Usage:" block):

```go
		"  -dry-run  print the bounded count and affected tenants of the next mutating\n" +
		"            run (capped at -limit); mutate nothing, mint nothing\n\n" +
		"mutating mode requires audit_governance.purge_pause: true in the loaded config\n" +
		"  (the server drain purge must be paused so replay cannot race it; -dry-run is\n" +
		"  unaffected). See internal/config/audit_governance.go."
```

Imports: add `errors` to the existing import block. **No new flags** (D1 in the spec; the acceptance's
primary disjunct).

### 1.2 `cmd/audit-replay/main_test.go` — fixture and new tests (test surface)

`startReplayEnv` gains one line (REQ-5 — the harness models the post-guard legal posture):

```go
	cfg := &config.Config{}
	cfg.AuditLedger.SigningKey = auditReplayTestKey
	// Post-guard posture: mutating replay is only legal while the drain purge
	// is paused (checkPurgePause). Tests that exercise the refusal reset this
	// to false explicitly.
	cfg.AuditGovernance.PurgePause = true
```

`replayConfigYAML` gains a `purgePause bool` parameter and emits the minimal governance block that
passes `config.Load` (E9 — no validation rule touches `purge_pause`):

```go
func replayConfigYAML(host string, port int, signingKey string, purgePause bool) string {
	return fmt.Sprintf(`
...
audit_ledger: {signing_key: %s}
audit_governance: {purge_pause: %t}
signing_key: testkey
...
`, host, port, strconv.Quote(signingKey), purgePause)
}
```

Call-site updates (behavior-preserving per test intent):
- `TestAuditReplaySigningKeyPreFlight` (3 calls): pass `false`. The guard sits **after** `config.Load`,
  so the key errors still fire first — this strengthens REQ-1.4's ordering proof (bad key never
  reaches the guard, let alone the DB).
- `TestAuditReplayDBOutageFailsClosed` leg 1: pass `true` — the leg's subject is the DB outage, so the
  config must satisfy the new precondition (REQ-2.4).

New tests (all integration-tagged, same `//go:build integration` header):

1. `TestAuditReplayPurgePauseGuardCLI` (REQ-2) — `run([]string{"-config", path, ...})` with a dead
   DSN (`127.0.0.1:1`), table-driven over three cases:
   - `purge_pause: false`, `-actor ops-bot` → error contains `purge_pause`; must **not** contain
     `open db` or `connection` (guard fired before `db.Open`).
   - `purge_pause: true`, `-actor ops-bot` → error must **not** contain `purge_pause` and **must**
     contain `open db` (guard passed; the tool proceeded to the DB — the pass proof).
   - `purge_pause: false`, `-dry-run` → error must **not** contain `purge_pause` and must contain
     `open db` (dry-run unaffected; the dead DSN is the failure).
2. `TestAuditReplayPurgePauseGuardCore` (REQ-1.2) — live testpg via `startReplayEnv`, one seeded
   dead-letter row, then:
   - `cfg.AuditGovernance.PurgePause = false` (explicit reset) → `runCapture(... -actor ops-bot)` →
     error contains `purge_pause`; `status='dead_letter'` count still 1; zero
     `audit.publication.requeued` facts.
   - `cfg.AuditGovernance.PurgePause = true` → same `runCapture` succeeds (`requeued 1 publication(s)`),
     row requeued, exactly one requeue fact — the core-level pass proof.
3. `TestAuditReplayUsageDocumentsPurgePause` (REQ-4) — `strings.Contains(usage(), "purge_pause")` and
   the package-doc block contains `purge_pause` (read via `//go:embed`-free approach: the test asserts
   the `usage()` substring; the package-doc claim is covered by the same test reading `main.go` via
   `os.ReadFile` — see §7 REQ-4).

### 1.3 `internal/app/audit_integration_test.go` — fixture retention + replay-under-pause leg (test surface)

Fixture gains the discarded repository (REQ-3.3):

```go
type auditGovernanceFixture struct {
	db          *gorm.DB
	endpoint    *governanceTestEndpoint
	callbackSvc service.CallbackService
	auditLedger service.AuditLedgerService
	ledgerRepo  repository.FullAuditLedgerRepository // retained: replay-under-pause leg (REQ-3)
}
```

`buildStack` assigns the third `buildAuditStack` return instead of `_`:

```go
	callbackSvc, auditLedger, ledgerRepo, err := buildAuditStack(cfg, f.db, service.NewRegistryControlPlaneMetrics(metrics.NewRegistry()))
	...
	f.ledgerRepo = ledgerRepo
```

`TestBuildAuditStackHonorsPurgeKnobs` gains the replay-under-pause leg inside leg 1 (see §3 D-7/D-8 for
the two spec refinements). Seed order changes so the existing tick-proof is reused: the `replayID`
fact is recorded and backdated **alongside** `pastID`, before `Start`; the existing
`pollPublication(progressID, "completed")` then proves ticks ran with **both** dead-letter rows in
place. Leg structure after the change:

1. (existing) record `progressID`, `pastID`; backdate `pastID` 10 days; snapshot `pastID`.
2. (new) record `replayID`; backdate `replayID` 10 days; snapshot `replayID`.
3. (existing) `Start(50ms)`; `pollPublication(progressID, completed)`.
4. (existing) `pastID` snapshot unchanged.
5. (new) `replayID` snapshot unchanged — preserved bit-for-bit under pause while the drain ticked
   (same contract as the `pastID` assertion; the "before" value is captured before replay because
   replay legitimately changes it).
6. (new) `stop()` — deterministic window, see D-8.
7. (new) replay leg: type-assert `fixture.ledgerRepo.(repository.AuditPublicationRepository)`;
   `intent, err := service.BuildAuditReplayIntent(t.Context(), "publication", "ops-bot", "", nil, domain.ID{})`;
   `n, facts, err := pubs.RequeuePublication(t.Context(), []domain.ID{replayID}, 10, intent)` (exact-id
   selection, D-7). Assert `n == 1`; row `replayID` is `pending` with `error_code = 'manual_replay'`
   and `attempt = 0`; exactly one fact with `action = 'audit.publication.requeued'`,
   `tenant_id = 'tenant-one'` (tenant derived from the row, never the intent).
8. (new) `stop2 := fixture.callbackSvc.Start(t.Context(), 50*time.Millisecond)` — the same paused
   stack resumes; `pollPublication(replayID, "completed")` — the requeued row flows through the
   paused drain (attempt 0 → 1 per `finishPublicationAttempt`).
9. (new) `pastID` snapshot **still** bit-for-bit unchanged after the replay and the completion ticks
   (replay touched only the selected row; the paused purge still deletes nothing).
10. (new) `stop2()`; (existing) leg 2 proceeds unchanged — the completed `replayID` row is not
    dead-letter, so the unpaused boundary purge semantics are untouched.

---

## 2. Compatibility constraints

- **Config:** the guard reads the existing `audit_governance.purge_pause` key. No new YAML key, env
  var, flag, or validation rule. `purge_pause: true` with `dead_letter_retention: 0` remains legal
  (E9) — inert for the drain, required for the tool. `configs/config.yaml` is **not** edited (the key
  is already documented at :188-189).
- **CLI surface:** no new flags (spec D1). `-dry-run`, `-actor`, `-tenant`, `-id`, `-limit`, `-kind`,
  `-report` semantics unchanged. New failure mode only: mutating mode against
  `purge_pause: false` exits 1 before any DB connection.
- **Intentional behavior change (spec D2):** the shipped default config (`purge_pause: false`) now
  refuses mutating replay until the operator sets `purge_pause: true` — even when retention is 0 and
  the purge is inert. Documented in `usage()` and this design's failure table. Operators already
  following the pause-then-replay protocol (set `purge_pause: true`, restart the server, run the tool)
  see zero change.
- **Server:** untouched. The drain purge, its metrics, and `TestBuildAuditStackHonorsPurgeKnobs`
  legs 1-2 keep their exact behavior and assertions.
- **Module boundary:** `internal/app` never imports `cmd/audit-replay`; REQ-3 drives the repository
  surface (`RequeuePublication` + `service.BuildAuditReplayIntent`) directly. `service` is already a
  dependency of `internal/app` tests (the fixture calls `service.NewCallbackService`).
- **Gates:** `check-audit-swallow` — the guard is a bare `return errPurgePauseRequired`; no swallow
  shape (P1/P2 patterns untouched; the guard adds no audit-write site). `check-anchored-event-ids` —
  no EventID mint site added (the fresh-v7 per intent stays in `internal/service/audit_replay.go`
  and the repository). `check-file-sizes` — non-test files: `main.go` 357 → ~370 lines (guard + usage
  text, under 500 with margin); no other production file changes. `check-integration-compile` —
  covers the new integration-tagged tests via `go test -tags integration -run '^$' ./...` (Makefile
  :36-38).
- **File-size note (spec correction):** the requirements' §5 claims `main.go` is 209 lines; the live
  file is 357. The constraint (≤ 500) still holds with ~130 lines of headroom — no split needed.

---

## 3. Design decisions and rejected alternatives

- **D-1 — Refusal-only, no `-ignore-purge-pause` override** (spec D1, accepted). Acknowledgment flags
  rot; the config key is the operator's unambiguous statement, and the error names the exact fix.
- **D-2 — Guard keys on `purge_pause` alone**, not `dead_letter_retention > 0 && !purge_pause`
  (spec D2; supersedes design-doc D-3's narrower condition). The tool cannot observe the server's
  boot-time purge state from its own config copy; the flag is the acknowledgment.
- **D-3 — Guard at both entry points** (spec D3): `run()` before `db.Open` (fail-closed before any
  connection, dead-DSN-provable) and the top of `replay()` (the harness's "exact CLI semantics" core).
  The `replay()` site is technically redundant after `run()` but load-bearing for direct callers and
  future composition.
- **D-4 — Stable error text** (spec D4) as a package-level `var` so both entry points and all three
  test files assert the identical string.
- **D-5 — Dry-run exempt** at both sites by parameter, not by caller convention.
- **D-6 — Harness models the legal posture** (spec REQ-5): `startReplayEnv` flips to `PurgePause=true`
  so the seven existing mutating legs and the i7 suite keep their assertions with zero changes; the
  new refusal test resets to `false` explicitly, making the legal/illegal postures both visible.
- **D-7 — Replay-leg selection uses exact IDs, not `nil`** *(spec refinement)*. The spec's REQ-3.2c
  says `RequeuePublication(ctx, nil, 10, intent)` and asserts `n == 1`, but with `ids=nil` the
  selection is "oldest `limit` dead-letter rows by `(next_attempt_at, id)`" — at that point **both**
  `pastID` and `replayID` are dead-letter with the same tenant, so `n == 2` and `pastID` would be
  requeued, violating REQ-3.2e's untouched-canary assertion. The design passes
  `ids = []domain.ID{replayID}`: exact-id selection (limit ignored — `deadLetterPublicationSelection`
  `:185-190`), `n == 1` deterministically, and the canary row is structurally out of scope. This is
  the tool's documented `-id` semantics, exercised through the repository surface.
- **D-8 — Deterministic stop/replay/restart in the app leg** *(spec refinement)*. REQ-3.2d asserts
  the requeued row is `pending` with `attempt = 0` while REQ-3.2e requires the same drain to then
  complete the row. With the ticker live every 50ms, the claim → `finishPublicationAttempt`
  (`nextAttempt := item.Attempt + 1`, callback_audit.go:126) can flip the row to `delivering`/
  `completed` with attempt 1 inside the assert window — a flaky test. The design stops the ticker
  before the replay (asserting the exact pending/manual_replay/attempt-0 state), restarts it on the
  **same paused stack** (never lifting `PurgePause`), and polls to `completed`. Substance of REQ-3.2e
  is preserved: the requeued row completes through the paused drain, and `pastID` stays bit-for-bit
  unchanged after further ticks. Consistent with the suite's deterministic doctrine (deadline polls,
  no unconditional sleeps).
- **D-9 — `replayConfigYAML` gains the block via a new parameter**, not a second YAML helper. Four
  existing call sites and the new table all pass an explicit bool; the signing-key test passes
  `false` to keep proving the preflight ordering (bad key fails at `config.Load`, before the guard).

Rejected: a `-purge-pause` tool flag (spec D1/A3 — superseded by the config-driven design); an
`-ignore-purge-pause` override (spec A2 — the acceptance's secondary disjunct; refusal is primary);
a `validateAuditGovernance` parity rule rejecting `purge_pause: true && retention == 0` (the sibling
direction's rule; here it would block the shipped config's one-line fix path and buy nothing — the
guard's semantics do not depend on the drain state); `internal/app` driving the CLI (module boundary).

---

## 4. Failure modes

| # | Failure | Behavior after this design | Pinned by |
|---|---|---|---|
| F1 | Server purge armed (`dead_letter_retention > 0`), `purge_pause: false`; operator starts mutating replay | Exit 1 before any DB connection; error names `purge_pause`. The mid-leg purge race is structurally unreachable | REQ-1.1, REQ-2.1 |
| F2 | `purge_pause: false`, retention 0 (never purge — shipped default) | Mutating replay refused until `purge_pause: true` (deliberate over-strictness, D-2); documented in `usage()`; dry-run unaffected | REQ-2.1/3, REQ-4 |
| F3 | `purge_pause: true`; mutating replay runs | Guard passes; bounded batches, in-tx per-tenant facts, idempotent re-runs exactly as before | REQ-2.2, REQ-5 (existing suite) |
| F4 | `-dry-run` with `purge_pause: false` | Unaffected at both sites: bounded counts print, nothing mutates | REQ-1.3, REQ-2.3 |
| F5 | Purge paused; replay requeues a row; drain keeps ticking | Requeued row flows to `completed` through the paused drain; other preserved dead-letter rows stay bit-for-bit unchanged | REQ-3.2e |
| F6 | Purge resumes (second boot, `purge_pause: false`) | Unpaused purge deletes exactly the past-retention dead-letter rows; ledger facts and completed rows kept — leg-2 behavior unchanged | REQ-3.1 |
| F7 | `replay()` called directly with `PurgePause=false` (harness, future caller) | Core refuses before any intent/query — fail-closed at the extracted core, not just the flag parser | REQ-1.2 |
| F8 | Operator misses the config line and runs `-kind both` | Refusal fires before the DB: no partial cross-leg state, no journal line, no facts (the guard precedes both legs) | REQ-1.1 |
| F9 | Config has `purge_pause: true` but server was **not** restarted (flag set after boot) | Tool proceeds — the config is the operator's acknowledgment; the server's static-per-boot posture is documented in `audit_governance.go` and the usage text. Not a tool defect; runbook step | §2, REQ-4 |

---

## 5. Migration steps (change rollout)

No data migration, no schema change, no wire change. Rollout order:

1. **Land the production change** (§1.1): `checkPurgePause` + guard sites + usage text in
   `cmd/audit-replay/main.go`. `make build` produces `bin/audit-replay` (the Makefile `build-audit-replay`
   target exists — verify the new file compiles standalone).
2. **Update the test harness** (§1.2): `startReplayEnv` pause flip, `replayConfigYAML` parameter +
   four call-site updates. This step is a precondition for the green suite — without it every existing
   mutating leg fails the new guard.
3. **Add the new CLI/core/usage tests** (§1.2) and **the app-level replay-under-pause leg** (§1.3,
   fixture retention first, then the leg).
4. **Docs:** mark `docs/designs/2026-08-07-audit-publication-dead-letter-replay-design.md` D-3 as
   superseded-by-config (spec §3.3 — the tool-side guard landed via this direction's D-1/D-2 instead
   of the planned flag). No `configs/config.yaml` edit.
5. **Operator runbook consequence (documented, no code):** the recovery protocol becomes
   "set `audit_governance.purge_pause: true`, restart the server, run audit-replay (mutating), then
   restore the config and restart". The failure modes table F1/F2/F9 is the reference.
6. **Gate sweep** (§7) — full local done-gate, then `make test-integration`.

---

## 6. Maintenance contract

- The guard error text is a stable test contract: any wording change must update the three tests that
  assert `purge_pause` / the absence of `open db` / `connection`.
- The guard's position in `run()` (after `config.Load` + signing-key preflight, before `db.Open`) is
  load-bearing for REQ-1.1/REQ-2.1 and the dead-DSN proof technique; a new preflight must be ordered
  explicitly, not appended blindly.
- The fixture's retained `ledgerRepo` is the only replay-capable handle in `internal/app`; keep the
  type-assert to `AuditPublicationRepository` at the leg site (the composition-root return type is
  deliberately the narrow read/append surface, `audit_wiring.go:9-13`).
- The replay-under-pause leg's deterministic stop/replay/restart window (D-8) must be preserved by any
  future test edit — do not "simplify" it into a racy in-flight assertion.

---

## 7. Testable acceptance mapping (spec §8 preserved; test → assertion)

| Supplied acceptance | Status | Test (file) | Concrete assertions |
|---|---|---|---|
| "add a mutating-mode guard in cmd/audit-replay run() — refuse (or require an explicit -ignore-purge-pause override) when cfg.AuditGovernance.PurgePause is false, with dry-run unaffected" | NEW — REQ-1/REQ-2 | `TestAuditReplayPurgePauseGuardCLI` + `TestAuditReplayPurgePauseGuardCore` (cmd/audit-replay) | run-level: guard error before DB (no `open db`/`connection`); core-level: live DB, `purge_pause:false` → refusal with `status='dead_letter'` count unchanged and zero `audit.publication.requeued` facts; `purge_pause:true` → success at both sites; dry-run unaffected at both sites |
| "add a CLI test asserting the refusal for purge_pause=false and a pass for purge_pause=true" | NEW — REQ-2 | `TestAuditReplayPurgePauseGuardCLI` | table: false → error contains `purge_pause`, not `open db`/`connection`; true → error is the dead-DSN `open db` (pass proof); false+dry-run → `open db` (guard skipped) |
| "keep the existing bit-for-bit dead-letter snapshot test (internal/app/audit_integration_test.go deadLetterRowSnapshot) green" | KEEP — REQ-3.1 | `TestBuildAuditStackHonorsPurgeKnobs` legs 1-2 | zero edits to the existing assertions: `after != snapshot` for `pastID` (leg 1); past-retention purged, inside-retention kept, ledger fact kept (leg 2) |
| "and extend it to assert replay under pause preserves rows" | NEW — REQ-3.2 | same test, new leg | `replayID` backdated 10 days; snapshot unchanged while paused ticks ran; exact-id `RequeuePublication` → `n==1`; row `pending`/`manual_replay`/`attempt 0`; exactly one fact, `tenant_id='tenant-one'`; `pollPublication(replayID, completed)` through the paused drain; `pastID` snapshot still bit-for-bit unchanged afterward |
| "document the flag in usage()" | NEW — REQ-4 | `TestAuditReplayUsageDocumentsPurgePause` | `strings.Contains(usage(), "purge_pause")`; `os.ReadFile("main.go")` contains `purge_pause` in the package-doc "Usage:" block and the mutating-mode sentence |

**Done gate (unchanged from spec §7):** `make test && make lint && make lint-strict &&
make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids &&
make check-integration-compile && make build`, then `make test-integration` (all new tests are
integration-tagged; the app leg needs real Postgres + the governance fake, already in the fixture).

---

## 8. Verification delta vs. the requirements spec

| # | Spec statement | Delta |
|---|---|---|
| V1 | §5: "`cmd/audit-replay/main.go` is 209 lines" | Live file is 357 lines. Constraint unaffected (≤ 500 with ~130 lines of headroom after the guard); noted here so nobody "splits" on a stale figure |
| V2 | REQ-3.2c: `RequeuePublication(ctx, nil, 10, intent)` asserting `n == 1` | Unimplementable as written (both canary rows are dead-letter, same tenant; nil-id selection takes the oldest `limit` → `n == 2`, canary destroyed). Design uses exact-id selection (D-7) |
| V3 | REQ-3.2d/e: assert `pending`/`attempt 0` while the drain is live, then have the same drain complete the row | Racy as written (50ms tick vs. assert window; `finishPublicationAttempt` sets attempt+1). Design uses deterministic stop/replay/restart on the same paused stack (D-8) |
| V4 | REQ-3.2b: "replayID's snapshot is preserved bit-for-bit while the drain ticks" | Placement clarified: the preservation assertion must run **before** the replay (replay legitimately changes the row); the existing `pollPublication(progressID, completed)` is reused as the tick proof with both rows seeded pre-`Start` |
| V5 | REQ-2.4: "`TestAuditReplayDBOutageFailsClosed` leg 1's config gains `purge_pause: true`" | Implemented via the `replayConfigYAML` parameter (D-9); the signing-key test passes `false`, strengthening the preflight-ordering proof |

Everything else in the spec is implemented as written. The design introduces no config, proto, wire,
or server changes; the sole intentional behavioral consequence remains spec D2 (mutating replay
refused under the shipped `purge_pause: false` default).
