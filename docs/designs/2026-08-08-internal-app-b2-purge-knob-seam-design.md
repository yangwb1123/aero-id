# Design — App-level B2 invariants into the unit gate: purge-knob seam (R1), T-5 twin+cutoff pin (R2), wireAuditServices positive leg (R3)

| | |
|---|---|
| Requirements | `docs/auto/runs/move-app-level-b2-invariants-purge-pause-reachab-35df2702/artifacts/requirements-10762e10/requirements.md` (evidence-verified summary; supplied acceptances T-5, T-4/T-10.2 preserved) |
| Module | `internal/app` only — no service-package changes, no `buildAuditStack` signature change, no new config |
| Verified at | Working tree on HEAD `b005409` (one commit after the cited `de20d78`; the audited files — `internal/app/audit.go`, the app test files, `internal/service/drain_entry_test.go` — are byte-identical between the two commits, so the evidence's verification holds at HEAD) |
| Stage | design (aggregating the requirements spec; nothing landed for this direction) |

---

## 0. Evidence verification — untrusted claims → verified facts

Every claim in the supplied evidence was re-checked against the working tree with
`grep`/`sed`/`read`. **All substance confirmed.** The three "material state changes"
are real, and the "genuine remaining gap" is exactly one. Corrections found during
verification:

1. **The evidence's own line corrections are slightly off.** The knob copy
   (`PurgePause:`/`DeadLetterRetention:`) is at **`internal/app/audit.go:84-85`**, not
   :88-90 as the evidence claims (the direction's original :53-56 was stale by ~30).
   `buildAuditStack` spans :59-105 (evidence said :59-103 — closing brace off by 2).
   All substance claims hold regardless.
2. **HEAD is `b005409`, not the cited `de20d78`.** `git diff de20d78 HEAD -- internal/app/audit.go
   internal/app/audit_integration_test.go internal/app/audit_scope_missing_test.go
   internal/app/audit_config_test.go internal/app/outbox_wiring_test.go internal/service/drain_entry_test.go`
   is **0 lines** — the evidence's verification carries over verbatim. The only delta in a
   cited file is `scripts/check-audit-swallow.sh` (21 lines: the FM-12 block-quarantine
   guard, unrelated); the drain single-call-site gate sits at :133-145 at HEAD, exactly as
   the evidence claims.

| Evidence claim | Verification (working tree @ b005409, 2026-08-08) |
|---|---|
| `buildAuditStack` at audit.go:59-103; knob copy inlined | ✅ Function at :59, body ends :105; knob copy (`PurgePause` :84, `DeadLetterRetention` :85) is **inlined inside `buildAuditStack`** into the `service.CallbackConfig` literal (:80-97) |
| `validateAuditBindingCoverage` at :238-257 (not :150-171) | ✅ Exact: `func validateAuditBindingCoverage(db *gorm.DB, ...)` at :238, closing brace :257; the UNION query body at :240-249 |
| `TestBuildAuditStackHonorsPurgeKnobs` at audit_integration_test.go:808 (not :743) | ✅ Exact (func at :808, comment at :792). Postgres-backed: `newAuditGovernanceFixture` → `testpg.Start` + `testpg.Migrate` (:507-527); two-leg twin (paused preservation / unpaused boundary purge) via a **second `buildAuditStack`** on the same DB — the composition-root copy is the thing under test |
| Outbox pattern at outbox_wiring_test.go:61-86 (not :57-80) | ✅ Twin test `TestBuildOutboxCleanerWiringPauseFlag` at :61 (the evidence's :61-86 spans the twin test + gate test), `outboxCleanerConfig(paused)` at :48-59, `recordingCleanerRepo` methods at :25-46, gate-condition test `TestBuildOutboxCleanerGateConditions` at :80. The pattern (recording fake repo + paused/unpaused twins + bounded window) is the model for T-5 |
| Injectable binding-coverage seam **already exists** (audit.go:146-156, defaulted at :87, consumed by `buildAuditPublishers`) | ✅ `type auditBindingCoverage interface { CheckAuditBindingCoverage(...) error }` at :146-150; `gormAuditBindingCoverage` at :152-157 (default `gormAuditBindingCoverage{gdb}` at :87); `buildAuditPublishers(cfg, controlMetrics, coverage)` at :169; nil-seam fail-fast at :178-180 |
| `recordingBindingCoverage` fake at audit_scope_missing_test.go:87 | ✅ Exact (type at :87, method :91) |
| Activation-gate matrix already unit-gated: `TestValidateAuditGovernanceGateMatrix` at audit_config_test.go:33 | ✅ Exact. Full 2×2 + nil config, DB-free, byte-identical error pin |
| `TestBuildAuditStackRefusesEnabledZeroBindings` at :81 | ✅ Exact. **Nil `gdb` is load-bearing** — proves the gate fires before any DB/connector use |
| Publishers legs at audit_scope_missing_test.go:107/:164 | ✅ Exact: `TestBuildAuditPublishersWiresScopeHookThroughRealConnector` at :107 (httptest token endpoint, fake coverage, hook-once pin) and `TestBuildAuditPublishersNilCoverageSeamFailsClosed` at :164 |
| Drain-only delivery path already unit-gated: `TestDrainPublicationsOnlyFromStart` drain_entry_test.go:53 | ✅ Func at :54 (comment :53); recording publication repo proves the Start ticker is the sole `ClaimPublication` driver |
| Static single-call-site gate in check-audit-swallow.sh:133-145 | ✅ Exact at HEAD: section (a) asserts exactly one non-test `drainPublications(` call site, inside `callback_worker.go` |
| `wireAuditServices` panic tests cover only failure cells | ✅ `audit_wiring_test.go:33-35`: nil callback service panics; ledger repo without `AuditPublicationRepository` panics. **No positive leg exists anywhere** |
| `buildAuditCallbackService` does not exist | ✅ `rg` across `internal/` and `scripts/`: zero hits. R1's gap is genuine |
| `buildOutboxCleaner` analog | ✅ `internal/app/outbox.go:77`, sole call site `new.go:348`; repo parameter is the injection seam |
| Knob semantics live in the service | ✅ `purgeDeadLetters` (callback_worker.go:58-79): `if s.cfg.DeadLetterRetention <= 0 || s.cfg.PurgePause { return }`; then `s.publications.PurgeDeadLetterPublications(ctx, cutoff, 50)` (publications, nil-guarded :69) and `s.repo.PurgeDeadLetterDeliveries(ctx, cutoff, 50)` (:77) with `cutoff := now - DeadLetterRetention`. Service-level unit tests exist (`internal/service/audit_purge_test.go:57,90,118`) — the app-level pass-through does not |
| Config knobs exist and are validated | ✅ `internal/config/audit_governance.go:37-44` (`purge_pause`, `dead_letter_retention`); validation at `config_test.go:276-297` + `commercial_test.go:143-162` (negative retention rejected). **No new config needed** |
| Lint gate tolerates the seam shape | ✅ `.golangci.yml` enables cyclop/funlen/lll/govet/staticcheck/unused/bodyclose/errorlint/gosec/nilerr/noctx — **no `unparam`**; an unused-but-documented parameter passes `make lint-strict` |

**Conclusion:** the requirements summary is accurate and current. The direction's original
problem statement is superseded for the binding-coverage seam (landed, unit-gated), the
activation-gate matrix (landed, unit-gated), and the drain-only path (landed, unit-gated +
static gate). Exactly one gap remains: **the composition-root purge-knob pass-through
(audit.go:84-85) is provable only via Postgres** (`TestBuildAuditStackHonorsPurgeKnobs`,
integration tag). This design implements R1 (seam), R2 (T-5 unit twin + cutoff pin), and
R3 (wireAuditServices positive leg).

---

## 1. API changes

No wire/proto/HTTP/gRPC/DB-schema/config/metrics-family changes, no service-package
changes, no `buildAuditStack` signature change. One production delta (unexported, pure
extraction) plus two test surfaces.

### A1 — `internal/app/audit.go` — R1: `buildAuditCallbackService` seam (pure extraction)

Move the inlined `service.NewCallbackService(callbackRepo, service.CallbackConfig{...})`
literal (audit.go:80-97) verbatim into a named, unit-testable function — the audit
analog of `buildOutboxCleaner(cfg, log, outboxRepo)`'s repo-parameter seam
(outbox.go:77-92):

```go
// buildAuditCallbackService constructs the drain callback service from the
// loaded config (the outbox analog of buildOutboxCleaner's repo-parameter
// seam). The B2-5 knob pass-through lives HERE — PurgePause suspends the
// dead-letter purge scan and DeadLetterRetention bounds it — so a lost
// purge_pause or a hardcoded retention copy fails the unit gate (T-5), not
// only the Postgres integration test. The repo parameters are the seams:
// callbackRepo carries delivery state; ledgerRepo doubles as the
// AuditPublicationRepository attached by wireAuditServices, so the unit test
// injects recording fakes for both halves of the drain surface (the same
// ledger-repo argument the caller passes to wireAuditServices). The
// production call site passes the real repos built by buildAuditStack.
func buildAuditCallbackService(cfg *config.Config, callbackRepo repository.CallbackRepository,
	ledgerRepo repository.AuditLedgerRepository) service.CallbackService {
	return service.NewCallbackService(callbackRepo, service.CallbackConfig{
		MasterKey: cfg.Callback.MasterKey, RequestTimeout: cfg.Callback.RequestTimeout,
		MaxResponseBytes: cfg.Callback.MaxResponseBytes, MaxAttempts: cfg.Callback.MaxAttempts,
		RetryBase: cfg.Callback.RetryBase, AllowPrivateTarget: cfg.Callback.AllowPrivateTarget,
		WorkerConcurrency: cfg.Callback.WorkerConcurrency,
		// M-5: the drain lease must exceed the worst-case batch latency; the
		// per-publish bound is the governance publisher timeout (0 when the
		// flag-gated publisher is disabled — publicationLease defaults it).
		PublicationTimeout: cfg.AuditGovernance.Timeout,
		// B2-5 WORKER-1: the dead-letter purge scan in the drain loop is
		// bounded by dead_letter_retention and suspended by purge_pause.
		PurgePause:          cfg.AuditGovernance.PurgePause,
		DeadLetterRetention: cfg.AuditGovernance.DeadLetterRetention,
	})
}
```

`buildAuditStack` replaces the inlined construction (audit.go:80-97) with
`callbackSvc := buildAuditCallbackService(cfg, callbackRepo, auditLedgerRepo)` — one
line, same position in the dependency order (after the two repo constructions, before
`buildAuditPublishers`). Everything downstream (`NewAuditLedgerService`, metrics
setters, `wireAuditServices`) is untouched.

**On the `ledgerRepo` parameter:** it is the dependency-set/injection seam — the unit
test passes the same recording fake to the seam and to `wireAuditServices`, mirroring
the production call shape exactly. No enabled linter flags unused parameters
(verified: `.golangci.yml` has no `unparam`; `unused` covers variables, not params).
Do not add a `_ = ledgerRepo` noise line; the doc comment carries the contract.

### A2 — New unit test file `internal/app/audit_purge_wiring_test.go` (no build tag) — R2: T-5

Fakes (same package, modeled on `recordingCleanerRepo` + `wiringTestRepo`):

- `recordingPurgeCallbackRepo` — implements `repository.CallbackRepository`; records
  every `PurgeDeadLetterDeliveries(ctx, cutoff, limit)` call (count + cutoffs);
  `EnqueueDelivery` returns nil; all other methods mirror `wiringTestRepo`
  (type at audit_wiring_test.go:42, methods :44-95).
- `recordingPurgePublicationRepo` — implements `repository.AuditPublicationRepository`
  (interface at `repository/audit_ledger.go:42-61`); records `ClaimPublication` calls
  (tick non-vacuity) and `PurgeDeadLetterPublications(ctx, cutoff, limit)` calls
  (count + cutoffs); `FinishPublication` records statuses.
- `recordingPurgePublisher` — implements `service.AuditPublisher`; records `Publish`
  calls, returns nil (success) — used by the R3 positive leg.

Helper `auditPurgeTestConfig(paused bool, retention time.Duration) *config.Config` —
mirrors `outboxCleanerConfig` (outbox_wiring_test.go:61-79): `Callback{MasterKey: "k"}`,
`AuditGovernance{PurgePause: paused, DeadLetterRetention: retention, Timeout: time.Second}`.

**T-5 — `TestBuildAuditCallbackServiceHonorsPurgeKnobs`** (two legs, fresh fakes per leg):

- **Leg 1 (paused):** `paused=true, retention=1h`. Build `svc :=
  buildAuditCallbackService(cfg, callbackRepo, pubRepo)`, attach the publication repo
  via the production path `wireAuditServices(svc, pubRepo)`, `stop := svc.Start(ctx,
  10*time.Millisecond)`. **Non-vacuity first:** deadline-poll until `pubRepo.claims ≥ 1`
  (the Start ticker drives `ClaimPublication` every tick — proven at the service level
  by `TestDrainPublicationsOnlyFromStart`), which proves at least one full tick ran
  while paused. **Then assert:** zero `PurgeDeadLetterPublications` AND zero
  `PurgeDeadLetterDeliveries` calls. `stop()`.
- **Leg 2 (unpaused twin + cutoff pin):** fresh fakes, `paused=false, retention=1h`.
  Same construction + wiring; deadline-poll until `pubRepo.purges ≥ 1`; assert the
  delivery-side purge also fired (`callbackRepo.deliveryPurges ≥ 1`). **Cutoff pin:**
  every recorded cutoff must satisfy `now.Add(-1h)` within ±2s — a hardcoded retention
  copy (e.g., the 168h config default) records `cutoff ≈ now-168h` → red. `stop()`.

Determinism: 10ms tick, 2s deadline, hard failure on timeout — no sleeps-only
assertions, no false greens (mirrors the outbox twin test's bounded-window doctrine and
`TestBuildAuditStackHonorsPurgeKnobs`'s poll patterns). The paused leg's zero is
meaningful because claims ≥ 1 proves the purge scan *ran and chose not to act* — the
same non-vacuity argument the integration test makes with a completing pending row.

### A3 — `internal/app/audit_purge_wiring_test.go` — R3: wireAuditServices positive leg

`TestWireAuditServicesWiresDrainSurface` — the missing success cell of the FM-8 matrix
(the panic tests at audit_wiring_test.go:33-35 cover only failure cells):

1. Real callback service via the seam (ties R3 to R1): `svc :=
   buildAuditCallbackService(auditPurgeTestConfig(false, 0), callbackRepo, pubRepo)`.
2. `wireAuditServices(svc, pubRepo, spyPublisher)` — must not panic (both type
   assertions hold with the concrete service + fake).
3. Proof of attachment via a full drain cycle: seed `pubRepo.ClaimPublication` to return
   one `&domain.AuditPublication{ID: idgen.NewID(), Fact: &domain.AuditFact{...},
   Attempt: 0}`; `stop := svc.Start(ctx, 10*time.Millisecond)`; deadline-poll until
   `spyPublisher.received ≥ 1` (**proves `SetAuditPublishers` attached**) and
   `pubRepo.finishStatus == "completed"` (**proves `SetAuditPublicationRepository`
   attached** — the drain claimed through the attached repo and settled through it).

This closes the hole where a future regression makes the setters silent no-ops (e.g., a
renamed method in the assertion, a no-op setter): the panic tests stay green while the
drain is dead — the positive leg goes red.

### A4 — Unchanged: `TestBuildAuditStackHonorsPurgeKnobs`

The Postgres integration test (audit_integration_test.go:808) stays byte-identical and
remains the e2e leg (real repos, real drain, backdated SQL rows, boundary-value pin).

---

## 2. Compatibility constraints

1. **`buildAuditStack` signature and behavior unchanged** — still
   `(cfg, gdb, controlMetrics) → (CallbackService, AuditLedgerService, FullAuditLedgerRepository, error)`;
   all callers (composition root in `new.go`, `auditGovernanceFixture.buildStack` at
   audit_integration_test.go:520, `TestBuildAuditStackRefusesEnabledZeroBindings` at
   audit_config_test.go:81) are untouched. The knob copy moves *verbatim* — same fields,
   same order, same values, same `NewCallbackService` `<= 0` defaults
   (callback.go:70-82).
2. **Zero service-package changes.** `service.NewCallbackService`, `CallbackConfig`,
   `Start`, `purgeDeadLetters`, `wireAuditServices` semantics are byte-identical.
   `internal/service/audit_purge_test.go` (:57/:90/:118) and `drain_entry_test.go:54`
   stay green unchanged.
3. **`wireAuditServices` keeps both panic legs and its byte-identical panic messages**
   (audit_wiring_test.go:33-35 green unchanged); R3 only *adds* a test.
4. **No new config, metric family, migration, or wire change.** The knobs are already
   validated (`config_test.go:276-297`); no `.proto`, no SQL, no `configs/config.yaml`
   edit.
5. **The seam is unexported with a single call site** (`buildAuditStack`) — no external
   API surface; `make check-composition-root` and `make check-integration-compile` must
   stay green (the latter proves no integration-tagged caller of the old inlined shape
   exists).
6. **`check-audit-swallow.sh` untouched** — the drain single-call-site gate (:133-145)
   keeps pinning `drainPublications(`'s sole site; the seam adds no new audit-write
   sites.
7. **T-5 fakes satisfy the existing interfaces exactly** — `repository.CallbackRepository`
   and `repository.AuditPublicationRepository`; no new interface, no change to
   `wireAuditServices`' type assertions.
8. **File-size gate:** audit.go is 318 lines today; A1 adds ≈20 → ≈340 ≤ 500 ✓. The new
   test file ≈250 lines ≤ 500 ✓ (test files are also exempt from `funlen` per
   `.golangci.yml`).
9. **The integration twin stays the boundary detector:** T-5 pins the *pass-through*;
   `TestBuildAuditStackHonorsPurgeKnobs` pins the *end-to-end purge* (SQL predicates,
   lease guards, bit-for-bit preservation). Both must stay green; neither replaces the
   other.

---

## 3. Design decisions and rejected alternatives

**D-1 — Seam shape: pure constructor moving the entire inlined `NewCallbackService`
literal, not just the two knob fields.** The `CallbackConfig` literal is the unit of
pass-through; splitting it would leave a second half-literal construction site and a
drift risk (FM-7). The knob fields are where the regression risk lives, but the whole
literal moves so the seam is the *single* construction site.
*Rejected: a `callbackConfigFrom(cfg)` helper returning `CallbackConfig`* — leaves the
`NewCallbackService` call in `buildAuditStack` and splits the construction into two
sites. *Rejected: a parameter-struct/options pattern* — boilerplate for one call site.
*Rejected: seam returning `(service.CallbackService, error)`* — nothing in the
construction can fail (validation already ran in `buildAuditStack`); an error return
would be dead code.

**D-2 — `ledgerRepo` parameter role.** It is the publication-repo source: the caller
passes the same value to `wireAuditServices`, and T-5 injects the recording fake through
it — mirroring `buildOutboxCleaner`'s repo parameter, which exists solely so the wiring
test can inject a fake. The seam itself is a pure constructor; attachment stays in
`wireAuditServices` (moving it would fork the attachment into two sites and weaken the
panic doctrine). No enabled linter flags the unused parameter (verified §0).

**D-3 — T-5 non-vacuity: claims-counter proof instead of a completing row.** The unit
test has no DB; the Start ticker drives `ClaimPublication` every tick regardless of
publication availability (proven by `TestDrainPublicationsOnlyFromStart`), so
`claims ≥ 1` proves a tick ran while the purge counter stayed at zero — making the
paused leg's zero meaningful. This is the unit analog of the integration test's
pending-row-completion proof.
*Rejected: fixed sleeps* — flaky; the deadline-poll pattern is the house doctrine.

**D-4 — Cutoff pin tolerance ±2s with a 1h retention.** `cutoff = now - retention`
(callback_worker.go:68) computed inside `purgeDeadLetters`; ±2s swallows scheduler
jitter while making a 168h hardcode (the config default, and the obvious wrong literal)
fail by ~167 hours. The pin lands on *both* purge kinds; the publication side is the
B2-5 target, the delivery side catches a copy that only fixed the publications leg.

**D-5 — R3 positive leg lives with the shared fakes in the new test file.** The panic
tests stay byte-unchanged in audit_wiring_test.go; the positive leg needs the recording
repos + spy publisher, which T-5 also uses. Assertion via observable drain effects
(spy publish + `completed` finish), not reflection on unexported fields.

**D-6 — No production red-run needed for R1** (pure refactor: `make test` stays green at
the seam step). The red-run discipline applies to T-5: run it against a mutant seam
(dropped `PurgePause`, then hardcoded retention) and record the expected reds — the
regression-detector proof that the new unit gate actually detects what it claims.

---

## 4. Failure modes

| FM | Failure | Detection | Severity |
|---|---|---|---|
| FM-1 | `purge_pause` pass-through lost (seam omits `PurgePause`) → drain purges dead-letter rows while a replay run is recovering them — **silent destructive deletion** during maintenance | T-5 leg 1: paused twin records ≥ 1 purge call → red. Today only `TestBuildAuditStackHonorsPurgeKnobs` (Postgres) catches this | High — the direction's core regression |
| FM-2 | `dead_letter_retention` hardcoded (e.g., the 168h config default inlined) → rows purged too early or never | T-5 leg 2 cutoff pin: recorded cutoff ≈ now-168h ≠ now-1h ± 2s → red | High |
| FM-3 | `PurgePause` hardcoded true → purge never runs → unbounded dead-letter growth | T-5 leg 2: purge counts stay 0 → red (the unpaused twin is load-bearing) | Medium |
| FM-4 | `wireAuditServices` setter regression (assertion renamed, setter no-op) → drain dead but panic tests green | R3 positive leg: no claim / no spy publish / no `completed` finish → red | High — the FM-8 success-cell gap |
| FM-5 | Seam extraction drift: any other `CallbackConfig` field mis-copied or reordered | `make test` (service-level defaults + drain tests) and T-5 legs; behavior-identical refactor — a field-level diff fails loudly | Medium |
| FM-6 | T-5 timing flake: tick never fires within the bounded window | Deadline-poll fails loudly (claims or purges never reach the pin) — never a false green | Low (test-only) |
| FM-7 | A future dev adds a second `NewCallbackService` construction site with a divergent knob copy | Review + maintenance contract (§6); optionally extend the check-audit-swallow.sh-style static gate with a `NewCallbackService(` single-call-site pin in a later batch — not in this scope | Low (process) |

---

## 5. Migration steps (change rollout)

No data migration, no config migration, no schema change, no wire change. Ordered code
rollout with the done gate at each step; the pipeline's `git_commit: true` commits the
batch.

1. **Baseline** — `make test` green on the working tree (HEAD `b005409` + uncommitted
   sibling-batch work; sibling work is independent and must already pass).
2. **A1 (R1) — seam extraction.** Pure refactor: `buildAuditCallbackService` lands in
   audit.go; `buildAuditStack` calls it. Run `make test`, `make lint`,
   `make lint-strict`, `make check-integration-compile` — all green with zero test
   changes (proves the extraction is behavior-identical).
3. **A2+A3 (R2+R3) — new test file** `internal/app/audit_purge_wiring_test.go`
   (no build tag): T-5 twin + cutoff pin, positive leg. Green immediately.
4. **Red-run discipline (regression-detector proof):** temporarily mutate the seam —
   (a) drop `PurgePause` from the literal, run
   `go test ./internal/app -run TestBuildAuditCallbackServiceHonorsPurgeKnobs` →
   record expected red (leg 1 purges > 0); (b) hardcode
   `DeadLetterRetention: 168*time.Hour`, run again → record expected red (leg 2 cutoff
   pin); (c) revert both mutants → green. This proves the new unit gate detects the two
   regressions the direction exists to prevent, without Docker.
5. **Full done gate:**
   `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-composition-root && make check-integration-compile && make build`
   plus **`make test-integration`** — `TestBuildAuditStackHonorsPurgeKnobs` (:808) must
   stay green: it now shares the pass-through with the seam, and it remains the e2e
   boundary detector.
6. **Convention copy** — this design is the batch record; commit with the stage
   artifact.

---

## 6. Maintenance contract

- **The seam is the single composition-root construction site for the callback
  service.** Any future `CallbackConfig` field must be added there; fields affecting
  the purge scan (`PurgePause`, `DeadLetterRetention`) must extend T-5's pin.
- **T-5 is the Docker-free detector** for the B2-5 knob pass-through;
  `TestBuildAuditStackHonorsPurgeKnobs` remains the e2e leg (SQL predicates, lease
  guards, bit-for-bit preservation). Neither replaces the other.
- **The drain-only delivery invariant is pinned at three levels:** static
  (check-audit-swallow.sh:133-145 single call site), service
  (`TestDrainPublicationsOnlyFromStart`), and app (R3 positive leg). A new delivery
  path must fail at least one of them.
- **Scope discipline held:** no `buildAuditStack` signature change, no
  service-package change, no new config, no new interface, no migration. The
  binding-coverage seam (audit.go:146-156), activation-gate matrix
  (audit_config_test.go:33/:81), and drain-only gates are already landed and are
  *not* reworked by this direction.

---

## 7. Testable acceptance mapping

Supplied acceptance checks preserved verbatim; each decomposes into runnable
assertions. Status per the evidence verification (§0): two of three are already
satisfied with zero new code; the remaining one is implemented by R1+R2.

| Supplied acceptance | Status | Tests / assertions | Gate |
|---|---|---|---|
| **T-5:** unit test using a fake binding-coverage querier (extracted interface defaulting to the real UNION query) proving buildAuditStack passes PurgePause/DeadLetterRetention to the drain — a paused drain performs zero purge calls over a bounded window while its unpaused twin demonstrably purges (mirror outbox_wiring_test.go:57) | Partially superseded, remainder = R1+R2 | **Already landed (fake querier half):** `auditBindingCoverage` seam (audit.go:146-156) defaulted to the real UNION query via `gormAuditBindingCoverage{gdb}` (:87), unit-gated by `recordingBindingCoverage` (audit_scope_missing_test.go:87) in the publishers legs (:107/:164). **New (pass-through half):** R1 seam `buildAuditCallbackService` + **`TestBuildAuditCallbackServiceHonorsPurgeKnobs`** (A2): leg 1 paused → claims ≥ 1 (tick ran) and purge calls == 0 for both kinds; leg 2 unpaused twin → purges ≥ 1 for both kinds and every recorded cutoff == now-1h ± 2s (lost `purge_pause` or hardcoded retention → red) | `make test` (no Docker) |
| **T-4/T-10.2:** unit test asserting the worker tick has exactly one delivery path (drainPublications) and no inline publish loop, extending the existing panic-based drain-only wiring assertions (internal/app/audit_wiring_test.go:29) | Already satisfied; R3 adds the missing success cell | **Landed:** `TestDrainPublicationsOnlyFromStart` (drain_entry_test.go:54) + static single-call-site gate (check-audit-swallow.sh:133-145) + `wireAuditServices` panic tests (audit_wiring_test.go:33-35). **New:** `TestWireAuditServicesWiresDrainSurface` (A3): seam-built service + `wireAuditServices(svc, pubRepo, spyPublisher)` → no panic; drain cycle completes: spy receives the fact (publishers attached) and `FinishPublication("completed")` (publication repo attached) | `make test` |
| **T-4:** app-seam activation-gate matrix (Enabled + empty bindings → boot failure; Enabled + bindings → publishers built; disabled + empty → no publishers) exercised without a database, complementing the config-level test | Satisfied with zero new code | `TestValidateAuditGovernanceGateMatrix` (audit_config_test.go:33): full 2×2 + nil config, byte-identical error; `TestBuildAuditStackRefusesEnabledZeroBindings` (:81): nil-`gdb` proves gate-before-DB ordering; publishers legs (audit_scope_missing_test.go:107/:164): enabled+bindings → publisher built, disabled → none | `make test` |

**Red-run evidence to record in the change report:** T-5 against the two mutants
(dropped `PurgePause`; hardcoded 168h retention) yields red on leg 1 / leg 2
respectively — run once before reverting the mutants, and document the expected reds.
This is the regression-detector proof the direction exists for.

---

## 8. Verification delta vs. the requirements summary

1. **Knob-copy lines:** the summary's corrected :88-90 is itself off — the copy is at
   audit.go:84-85 (and `buildAuditStack` ends at :105, not :103). Substance unchanged.
2. **HEAD drift:** the summary cites commit `de20d78`; HEAD is `b005409`. All audited
   files are byte-identical between the two commits (`git diff` = 0 lines); the only
   delta in a cited file is the unrelated FM-12 quarantine guard in
   check-audit-swallow.sh. The drain gate (:133-145) matches at HEAD.
3. **R1 signature detail:** `ledgerRepo` is a dependency-set/injection seam parameter,
   not consumed inside the pure constructor (D-2). The evidence's signature is held
   verbatim; the role is documented in A1 and no enabled linter flags it.
4. **R3 scope:** the positive leg proves attachment by observable drain effects (spy
   publish + `completed` finish), which also covers the seam's publication-repo half —
   no reflection on unexported fields, no production change beyond A1.
5. **Working-tree note:** uncommitted sibling-batch work (scope-missing alerting,
   activation gate, anchor derivation) is present and independent; all line citations
   are against the tree as read, and the rollout steps assume that tree stays green.
