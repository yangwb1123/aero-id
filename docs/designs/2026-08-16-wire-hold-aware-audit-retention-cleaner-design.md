# Design — Wire the hold-aware audit retention cleaner (`RunAuditCleanerWithHolds`) in the composition root so legal-hold facts are never purged

**Date:** 2026-08-16
**Source analysis:** `docs/auto/analyses/internal-app-73e67e01.json` (untrusted requirements evidence)
**Module scope boundary:** `internal/app` composition root (+ one script gate, `scripts/check-composition-root.sh`, and two new unit-test files). No repository purge/exemption behavior change, no config schema change, no migrations.

---

## 1. Verification verdict (untrusted evidence → tree)

Every substantive claim in the requirements evidence was re-checked against the live tree at HEAD (`f3c9584`). **One correction (C-1) is required — the evidence's R2.3 double-stop assertion cannot pass against the current repository code.**

| # | Claim | Verdict | Live-tree location |
|---|---|---|---|
| V1 | Composition root wires the plain cleaner, not the hold-aware one | **VERIFIED** | `internal/app/new.go:337` — `stopAuditClean = repository.RunAuditCleaner(` (bare form); OPT-IN gate `cfg.DataRetention.AuditCleanInterval > 0 && cfg.DataRetention.AuditRetention > 0` at `:336` |
| V2 | Plain cleaner delegates with a nil hold registry | **VERIFIED** | `internal/repository/audit.go:146-147` — `RunAuditCleaner(...) { return RunAuditCleanerWithHolds(repo, nil, log, retention, interval) }` |
| V3 | Hold-aware cleaner consults the registry only when non-nil | **VERIFIED** | `internal/repository/audit.go:166-168` — `if holds != nil { exempt, _ = holds.HoldScopeIDs(ctx, domain.AuditHoldScopeActor) }` |
| V4 | Nil holds ⇒ empty exemption ⇒ full purge (no `NOT IN`) | **VERIFIED** | `audit.go:105-106` (`PurgeOlderThan` → `PurgeOlderThanExcluding(…, nil)`); `:122-124` (`if len(exemptActors) > 0 { sub = sub.Where("actor_uid NOT IN ?", exemptActors) }`); `:187-194` (`purgeAuditWithExempt` type-asserts for the excluding surface, else falls back to bare `PurgeOlderThan` at `:193-194`) |
| V5 | `AuditHoldRepository` interface + `HoldScopeIDs` + `NewAuditHoldRepository` (mandatory ledger, fail-closed) | **VERIFIED** | `internal/repository/audit_hold.go:39` (interface: `PutHold`, `RemoveHold`, `ListActiveHolds`, `HoldScopeIDs`), `:74-76` (`NewAuditHoldRepository(db, audit)` with `requireAuditLedger` panic), `:174-177` (active-only, `expires_at`-aware query) |
| V6 | Hold repo built inline in `buildAuditComplianceServices`, never returned | **VERIFIED** | `internal/app/audit.go:288-296`; inline `repository.NewAuditHoldRepository(gdb, ledgerRepo)` at `:295`; builder called only from `new.go:375` |
| V7 | Cleaner constructed before the hold repo | **VERIFIED** | `new.go:337` (cleaner) vs `new.go:375` (`buildAuditComplianceServices`) — both a repo-exposure and an ordering change are required |
| V8 | Exemption path integration-tested | **VERIFIED** | `internal/repository/audit_hold_integration_test.go:21` (`TestLegalHoldExemptsActorsFromPurge`), hold at `:33`, `HoldScopeIDs` assertion at `:37`, purge with exemption at `:47`, purge after lifting hold at `:65` |
| V9 | `AuditHoldScopeActor` constant | **VERIFIED** | `internal/domain/audit_hold.go:9` — `AuditHoldScopeActor AuditHoldScope = "actor" // legacy audit_events exemption` |
| V10 | Gate config keys | **VERIFIED** | `internal/config/config.go:357-358` — `AuditRetention`, `AuditCleanInterval` (0 = disabled) |
| V11 | Composition-root script pins C-1..C-5; **C-5 already taken by `cmd/migrate`** — new pin must be C-6 | **VERIFIED** | `scripts/check-composition-root.sh` — C-1/C-2/C-3 in `scan_new_go`, C-2b in `scan_audit_go`, C-4 in `scan_main_go`, C-5 (`config.Load(` + `migrate.New(`) in `scan_migrate_main_go`; self-tests against `scripts/testdata/composition-root/{positive,negative}/`; `Makefile:122-124` runs it |
| V12 | `auditLedgerRepo` available before the cleaner block | **VERIFIED** | `new.go:131` — `callbackSvc, auditLedger, auditLedgerRepo, err := buildAuditStack(...)`; err check ends `:134`; type `repository.FullAuditLedgerRepository` is the same value passed to `NewAuditHoldRepository` today (`audit.go:295`), so it satisfies `AuditLedgerTransactionalRepository` |
| V13 | File-size headroom | **VERIFIED** | `new.go` = 497 lines (limit 500), `audit.go` = 319 lines; arithmetic below (§3.3) keeps both ≤ 500 |
| V14 | `AuditRepository` interface shape (for the R2 fake) | **VERIFIED** | `audit.go:16-27` — `Append`, `Log`, `List(ctx, actorUID, action, page domain.Page)`, `PurgeOlderThan`; the fake must add `PurgeOlderThanExcluding(context.Context, time.Time, int, []string)` for the routing type-assert at `:189-191` |
| V15 | `service.NewAuditHoldService(holds repository.AuditHoldRepository, audit AuditLedgerService)` | **VERIFIED** | `internal/service/audit_hold.go:40` — matches R1.4 verbatim |
| V16 | Test precedents (polling + gate patterns, mutex-guarded recorders) | **VERIFIED** | `internal/app/outbox_wiring_test.go:66-79` (bounded-deadline poll), `:83-115` (`TestBuildOutboxCleanerGateConditions`), `:16-46` (`recordingCleanerRepo` with `sync.Mutex`); `internal/app/audit_wiring_test.go` and `audit_config_test.go` exist |
| V17 | Baseline green | **VERIFIED EMPIRICALLY** | `go build ./...` OK; `go test ./internal/repository/ -count=1` and `go test ./internal/app/ -count=1` both `ok` at HEAD |
| V18 | No existing unit test for the audit cleaner (no duplication risk) | **VERIFIED** | no `RunAuditCleaner`/`purgeAuditWithExempt` reference in any `internal/repository/*_test.go` |
| V19 | Cross-references | **VERIFIED** | `docs/auto/analyses/internal-app-73e67e01.json` exists; `docs/TODO.md` (2026-08-05 round) and `docs/plans/2026-08-04-distributed-account-aggregation.md:423` both document P4-07 audit-retention/legal-hold |
| V20 | The audit cleaner's stop closure is **not** double-stop-safe | **CORRECTION C-1** | `internal/repository/audit.go:184` — `return func() { close(done) }`; a second `stop()` panics (`close of closed channel`). The evidence's R2.3 requires "calling the stop function twice does not panic", which is **unsatisfiable against the current code** and conflicts with the evidence's §6 ("no changes to `internal/repository/audit.go`"). The outbox precedent is double-stop-safe (`internal/outbox/repository.go:183+` returns `func() { cancel() }`; context cancel is idempotent). **Resolution:** R-5 below hardens the stop closure with `sync.Once` (or context-cancel), a purge-semantics-neutral one-line change; without it R2.3 cannot pass |

**Secondary precision notes (no substance impact):**

- **P-1 (R2.4 mechanics):** with a fake implementing `PurgeOlderThanExcluding`, the routing type-assert at `audit.go:189-191` *always* succeeds, so the nil-holds cell observes the **excluding surface receiving a nil/empty exempt list** — never the bare method. That is exactly the "full purge" precondition (the concrete repo's `NOT IN` guard at `:122-124` then deletes everything); the unit cell asserts the nil list and the existing integration test already proves the concrete consequence.
- **P-2 (R3.3 mechanics):** `RunAuditCleanerWithHolds` gates only on `interval <= 0` (`audit.go:158-160`); the `&& retention > 0` half of the composition-root gate lives only in `new.go:336`. Unlike the outbox cleaner (whose gate moved into the `buildOutboxCleaner` helper precisely so tests could drive the real gate), the audit cleaner has **no app-level helper** — R3.3 must therefore replicate the `new.go` `&&` condition in the test. This is a coverage gap worth noting (§6.4): the gate in `new.go` itself has no dynamic pin; C-6 pins the call form, not the gate. A follow-up `buildAuditCleaner` extraction (mirroring `buildOutboxCleaner`) is the stronger seam; explicitly out of scope here per the evidence's minimal-change prescription.

---

## 2. Problem statement (confirmed)

1. `new.go:337` calls `repository.RunAuditCleaner(auditRepo, log, …)`.
2. `RunAuditCleaner` (`audit.go:146-147`) delegates to `RunAuditCleanerWithHolds(repo, nil, …)` — **nil** hold registry.
3. With `holds == nil`, the tick body (`audit.go:166-168`) leaves `exempt` empty; `purgeAuditWithExempt` (`audit.go:187-194`) routes to `PurgeOlderThanExcluding(…, nil)`.
4. The `if len(exemptActors) > 0` guard (`audit.go:122-124`) is false, so no `NOT IN` clause is applied — **every** `audit_events` row older than retention is deleted, including rows whose `actor_uid` is under an active legal hold. Held actors' evidence is destroyed silently.
5. The registry that would fix this (`NewAuditHoldRepository`, `audit_hold.go:74-76`) is constructed only inside `buildAuditComplianceServices` (`audit.go:295`) at `new.go:375` — 38 lines *after* the cleaner — and is never returned from that builder, so the cleaner cannot see it.

The repository-level exemption behavior is correct and integration-tested (`audit_hold_integration_test.go:21-69`); only the server wiring is wrong. Verified by V1–V19; V20 (C-1) adds one required hardening so the new unit test can assert double-stop safety.

---

## 3. Design overview

Five coordinated changes (R-1…R-5), each independently testable:

- **R-1** — Composition-root wiring (`internal/app/new.go`): construct the hold repo **once** before the cleaner block; swap the bare call for `repository.RunAuditCleanerWithHolds(auditRepo, holdRepo, log, …)`; pass the same `holdRepo` into `buildAuditComplianceServices`.
- **R-2** — `internal/app/audit.go`: `buildAuditComplianceServices` accepts the hold repo as a parameter; the inline `NewAuditHoldRepository` construction is deleted (exactly one construction remains, in `new.go`).
- **R-3** — Repository-level unit regression test (`internal/repository/audit_cleaner_test.go`, new, no build tag, no Docker): recording fakes prove held actors' ids reach `PurgeOlderThanExcluding` as the exemption set; nil-holds cell documents the bug; disabled cell pins the interval gate.
- **R-4** — App-level wiring test (`internal/app/audit_cleaner_wiring_test.go`, new, package `app`, local fakes): proves the exact composition-root call shape consults the hold registry per tick and the gate conditions start nothing.
- **R-5** — Stop-closure hardening in `internal/repository/audit.go` (correction C-1): make the stop function idempotent (`sync.Once` wrapping the `close(done)`, or context-cancel as in `RunOutboxCleaner`) so R-3's double-stop assertion is satisfiable. No purge/exemption semantics change.
- **R-6** — Static pin C-6 in `scripts/check-composition-root.sh` + positive/negative fixtures (`positive/cleaner.go`, `negative/cleaner.go` new; `negative/new.go` updated) + self-test wiring + header/echo documentation.

### 3.1 Target wiring shape (`internal/app/new.go`)

```go
// after the buildAuditStack error check (new.go:134):
// P4-07: one shared hold registry for the cleaner and the hold service.
holdRepo := repository.NewAuditHoldRepository(gdb, auditLedgerRepo)

// cleaner block (new.go:336-343, gate unchanged):
if cfg.DataRetention.AuditCleanInterval > 0 && cfg.DataRetention.AuditRetention > 0 {
    stopAuditClean = repository.RunAuditCleanerWithHolds(
        auditRepo, holdRepo, log,
        cfg.DataRetention.AuditRetention,
        cfg.DataRetention.AuditCleanInterval,
    )
}

// later (new.go:375): same instance into the builder
holdSvc, anchorSvc, cutoverFacadeSvc := buildAuditComplianceServices(gdb, auditLedger, auditLedgerRepo, store, holdRepo)
```

### 3.2 Target builder shape (`internal/app/audit.go`)

```go
func buildAuditComplianceServices(gdb *gorm.DB, auditLedger service.AuditLedgerService,
    ledgerRepo repository.FullAuditLedgerRepository, store storage.FileStorage,
    holds repository.AuditHoldRepository) (
    service.AuditHoldService, service.AuditAnchorService, service.LegacyCutoverService) {
    holdSvc := service.NewAuditHoldService(holds, auditLedger)
    return holdSvc, service.NewAuditAnchorService(ledgerRepo, store),
        service.NewLegacyCutoverService(repository.NewLegacyCutoverRepository(gdb, ledgerRepo),
            repository.NewLegacyCleanupRepository(gdb), auditLedger)
}
```

`gdb` stays a parameter (still used by the two legacy repositories). `assertComplianceAuditWiring(holdSvc, cutoverSvc, cutoverFacadeSvc)` and the return signature are unchanged. The mandatory-ledger fail-closed guarantee (`requireAuditLedger`) is preserved: the same non-nil `auditLedgerRepo` value that feeds the builder today now feeds the `new.go` construction site.

### 3.3 Line-budget arithmetic (gate: non-test files ≤ 500 lines)

- `internal/app/new.go`: 497 → **499** (+2: `holdRepo :=` line + one-line P4-07 comment). The cleaner call and the `buildAuditComplianceServices` call keep their current line counts. **Only one line of headroom remains — the comment must stay on a single line; do not expand adjacent comments.**
- `internal/app/audit.go`: 319 → **320** (+1: the `holds repository.AuditHoldRepository` parameter line; `holdSvc :=` stays one line).
- `internal/repository/audit.go`: 195 → **196** (+1 for the `sync.Once` guard in the stop closure, R-5; a `sync` import may already exist — check; if not, the import line is inside the 196).
- New test files are exempt from the file-size limit (non-test rule).

---

## 4. API changes

**No production/public API changes.** No handler, gRPC, HTTP, config-schema, or interface-contract changes. The changes are internal wiring and tests:

| Surface | Change | Kind |
|---|---|---|
| `internal/app/new.go` | +`holdRepo := repository.NewAuditHoldRepository(gdb, auditLedgerRepo)` after `:134`; cleaner call → `repository.RunAuditCleanerWithHolds(auditRepo, holdRepo, log, …)`; `buildAuditComplianceServices(...)` gains 5th arg `holdRepo` | internal wiring |
| `internal/app/audit.go` | `buildAuditComplianceServices` gains param `holds repository.AuditHoldRepository`; inline `NewAuditHoldRepository` deleted; `service.NewAuditHoldService(holds, auditLedger)` | internal signature (unexported builder) |
| `internal/repository/audit.go` | R-5 only: stop closure becomes idempotent (`sync.Once`); exported `RunAuditCleaner` and `RunAuditCleanerWithHolds` signatures unchanged | internal hardening |
| `internal/repository/audit_hold.go` | **unchanged** | — |
| `internal/config/config.go` | **unchanged** (no new flags; `AuditRetention`/`AuditCleanInterval` semantics identical) | — |
| `internal/repository/audit_cleaner_test.go` | **new** (unit, no build tag) | test |
| `internal/app/audit_cleaner_wiring_test.go` | **new** (unit, package `app`) | test |
| `scripts/check-composition-root.sh` | **C-6 pin** added to `scan_new_go`; header + final echo updated; self-test wiring for the two new fixtures | gate |
| `scripts/testdata/composition-root/positive/cleaner.go` | **new** (bare-form fixture, must redden C-6) | test fixture |
| `scripts/testdata/composition-root/negative/cleaner.go` | **new** (WithHolds-form fixture, must pass) | test fixture |
| `scripts/testdata/composition-root/negative/new.go` | gains the WithHolds block (keeps it green under C-6) | test fixture |

`AuditRepository`, `AuditHoldRepository`, `AuditHoldService` interfaces: **unchanged** — no consumer (service, handler, gRPC) recompiles with a different contract.

---

## 5. Compatibility constraints

1. **Preserve the exported `repository.RunAuditCleaner`** (AGENTS.md: preserve current APIs). It keeps its documented nil-holds semantics and its one production call site is removed; C-6 bans the bare call form **only in `internal/app/new.go`**. No other caller exists today (V18).
2. **Single-construction invariant.** After R-1/R-2 there is exactly one `NewAuditHoldRepository` call in the production tree (`new.go`); the builder consumes it. The `requireAuditLedger` fail-closed panic guarantee is preserved because the identical `auditLedgerRepo` value (non-nil by construction, V12) feeds the new site.
3. **Ordering within `New()`.** Two prescribed moves only: hold-repo construction must sit between the `buildAuditStack` error check (`:134`) and the cleaner block (`:336`); everything else keeps its current relative order. Placing `holdRepo` after the cleaner block is a compile error (undefined at `:337`), so the ordering is self-enforcing.
4. **Gate semantics unchanged.** `AuditCleanInterval > 0 && AuditRetention > 0` OPT-IN gate and the `var stopAuditClean func()` / shutdown handling (`new.go:447-448`, `app.go:125`) are untouched.
5. **Existing pins byte-for-byte.** C-1..C-5 in `check-composition-root.sh` and all existing fixtures/self-tests remain effective; C-6 is additive and lives inside `scan_new_go` (so it applies to both the live `new.go` and every `scan_new_go` fixture — which is why `negative/new.go` must gain the WithHolds block). Do not renumber C-5.
6. **File-size gate.** new.go ≤ 500 (499 target, §3.3), audit.go ≤ 500 (320), repository/audit.go ≤ 500 (196).
7. **No Docker/integration additions.** Both new test files are unit tests with deterministic fakes; no build tag, no Postgres, no network.
8. **Substring safety.** `grep -cF 'repository.RunAuditCleaner('` does not match `repository.RunAuditCleanerWithHolds(` (the character after `RunAuditCleaner` is `W`, not `(`); the fixture pair (positive must redden, negative must stay green) mechanically proves both directions.

---

## 6. Failure modes

| # | Failure mode | Behavior | Mitigation / coverage |
|---|---|---|---|
| F1 | Regression to the bare `repository.RunAuditCleaner(` in `new.go` | nil holds ⇒ empty exemption ⇒ **full purge including held actors** (the exact bug) | **C-6 static pin reddens** (`grep -cF` zero-bare check); R-4 dynamic test reddens if it still drives the real call shape; revert drill in acceptance A-3 |
| F2 | `RunAuditCleanerWithHolds(auditRepo, nil, …)` (nil literal) | Identical full-purge bug, but passes the C-6 count-based pin | **Blind spot** (evidence §7, confirmed): covered by R-4 (wiring test drives `holdRepo`) and review; optional future exact-prefix pin (`repository.RunAuditCleanerWithHolds(auditRepo, holdRepo, log,`), explicitly out of scope |
| F3 | `holdRepo` constructed twice (builder re-adds inline construction) | Two instances can drift apart (hold service vs cleaner consult different repos) | R-2 deletes the inline construction; R-6 does not pin this — covered by review + the `requireAuditLedger` panic on a bad ledger; a future duplicate would be visible in diff |
| F4 | Wrong placement of `holdRepo :=` (after the cleaner block) | Compile error (`undefined: holdRepo` at `:337`) — self-enforcing | placement pinned by the compiler; R-1 prescribes the slot after `:134` |
| F5 | Cleaner disabled (`interval ≤ 0` or `retention ≤ 0` via the gate) | No purge at all — by design; held facts survive, but so do stale rows | R-3 (`TestRunAuditCleanerDisabled`), R-4 (`TestAuditCleanerWiringGateConditions`) pin non-inversion; note P-2: the retention half of the gate is replicated in the test, not dynamically pinned in `new.go` (§6.4) |
| F6 | Second `stop()` call | **Panics today** (`close(done)` at `audit.go:184`) — R-3 asserts safety | **R-5** makes the stop idempotent (`sync.Once` / cancel), matching the `RunOutboxCleaner` precedent |
| F7 | `HoldScopeIDs` query error mid-tick | `exempt, _ =` swallows the error ⇒ exempt stays nil ⇒ **full purge despite a hold-registry outage** (fail-open on the exemption query) | **Pre-existing latent risk, out of scope** (evidence §6 forbids repository behavior changes beyond R-5). Documented here for the follow-up queue: recommend fail-closed-on-query-error (skip the purge cycle on `HoldScopeIDs` error) in a separate direction |
| F8 | Ticker goroutine panic/crash | Purge silently stops; next boot restarts the cleaner | Pre-existing; no change |
| F9 | `new.go` line budget exceeded (comment expansion) | `make check-file-sizes` reddens | §3.3 arithmetic (499/500); keep the P4-07 comment on one line |

### 6.1 Gate-coverage gap (P-2, explicit)

The outbox cleaner's gate lives inside `buildOutboxCleaner` so `TestBuildOutboxCleanerGateConditions` drives the *real* gate. The audit cleaner's `&& retention > 0` half lives only inline in `new.go:336`; C-6 pins the call form, not the gate, and R-4's gate test replicates the condition. The risk is a future edit weakening the `&&` in `new.go` (e.g., dropping `retention > 0`) that ships green. Mitigation: the evidence's prescribed minimal change keeps the gate inline; a follow-up `buildAuditCleaner(cfg, log, auditRepo, holdRepo)` extraction (mirroring `buildOutboxCleaner`, gate hosted in the helper) would close this class within the same change pattern — **recommended follow-up, out of scope here**.

---

## 7. Migration steps (implementation sequence)

Ordered landing plan; each step keeps the tree compiling and the existing gates green.

1. **R-5 first (correction C-1):** `internal/repository/audit.go` — harden the stop closure of `RunAuditCleanerWithHolds` (and `RunAuditCleaner` inherits it): `var once sync.Once; return func() { once.Do(func() { close(done) }) }` (or replace `done` with a `context.WithCancel` and `return func() { cancel() }`, matching `RunOutboxCleaner`). No purge/exemption behavior change.
2. **R-2:** `internal/app/audit.go` — add `holds repository.AuditHoldRepository` parameter to `buildAuditComplianceServices`; `holdSvc := service.NewAuditHoldService(holds, auditLedger)`; delete the inline `NewAuditHoldRepository` call. Compiles standalone (caller in `new.go` updated in step 3 in the same commit, or temporarily via a `nil`-argument stub — prefer one commit).
3. **R-1:** `internal/app/new.go` — add the one-line P4-07 comment + `holdRepo := repository.NewAuditHoldRepository(gdb, auditLedgerRepo)` after `:134`; swap the cleaner call to `repository.RunAuditCleanerWithHolds(auditRepo, holdRepo, log, …)`; add `holdRepo` as the 5th argument to `buildAuditComplianceServices(...)` at `:375`. Verify `make build`, `go vet ./...`.
4. **R-3:** new `internal/repository/audit_cleaner_test.go` (§8, A-1) — `recordingAuditRepo` (full `AuditRepository` + `PurgeOlderThanExcluding`, mutex-guarded, records exempt slices and bare-call count), `recordingHoldRepo` (`PutHold`/`RemoveHold`/`ListActiveHolds`/`HoldScopeIDs`; `HoldScopeIDs` records the scope and returns `["u-held"]`). Three tests: held-exemption, nil-holds contrast, disabled.
5. **R-4:** new `internal/app/audit_cleaner_wiring_test.go` (§8, A-2) — local `appAuditRecorder` / `appHoldRecorder` (same two interfaces); the composition-root call shape test and the gate-conditions test (gate replicated per P-2).
6. **R-6:** `scripts/check-composition-root.sh` — C-6 inside `scan_new_go` (exactly one `repository.RunAuditCleanerWithHolds(`, zero `repository.RunAuditCleaner(`), header doc line, self-test invocations for `positive/cleaner.go` (expect fail) and `negative/cleaner.go` (expect pass), final echo mentions C-6. Fixtures: add the two `cleaner.go` files; update `negative/new.go` with the WithHolds block (see §4/§8 fixture shapes).
7. **Gate runs:** full done gate (§8, A-4); `make check-composition-root` revert drill (A-3). `make test-integration` optional (no SQL/storage/connector change; the exemption path is already covered by the existing Postgres-backed test, V8).

Fixture shapes (grep-scanned only; they need not compile — mirror existing fixtures' style):

- `scripts/testdata/composition-root/negative/cleaner.go` — satisfies C-1 (`buildAuditStack(cfg, gdb, controlMetrics)` line before `repository.NewFileRepository(`), C-2 (`stopCallbacks := callbackSvc.Start(context.Background(), cfg.Callback.WorkerInterval)`), and contains exactly the `repository.RunAuditCleanerWithHolds(` block with `holdRepo`, zero bare forms → **passes**.
- `scripts/testdata/composition-root/positive/cleaner.go` — same shape but with the bare `repository.RunAuditCleaner(` call → **fails** (C-6 zero-bare check).
- `scripts/testdata/composition-root/negative/new.go` — append the WithHolds block so it stays green under C-6 (it is scanned by `scan_new_go` in the self-test block). `positive/new.go` already trips C-1 and stays red; no change.

---

## 8. Testable acceptance mapping

| # | Acceptance | Testable mechanism | Gate command |
|---|---|---|---|
| A-1 | Repository regression test: held actors' rows never passed to the purge | `internal/repository/audit_cleaner_test.go`: `TestRunAuditCleanerWithHoldsExemptsHeldActors` — poll for first purge (bounded deadline ≤ 2s, interval 10ms), assert `HoldScopeIDs` called with `domain.AuditHoldScopeActor`, purge routed to `PurgeOlderThanExcluding` with exempt == `["u-held"]`, **zero** bare `PurgeOlderThan` calls, double `stop()` safe (R-5). `TestRunAuditCleanerNilHoldsFullPurges` — `repository.RunAuditCleaner(...)` ⇒ nil exempt list reaches the excluding call, `HoldScopeIDs` never called (the bug's precondition). `TestRunAuditCleanerDisabled` — `interval ≤ 0` ⇒ immediate no-op stop, zero `HoldScopeIDs`, zero purges over a bounded window. No build tag, no Docker | `go test ./internal/repository/ -count=1` |
| A-2 | Composition root wires the hold-aware cleaner; app-level proof the closure receives the hold repo; gate non-inverted | `internal/app/audit_cleaner_wiring_test.go`: `TestAuditCleanerWiringReceivesHoldRepo` — drive `repository.RunAuditCleanerWithHolds(auditRepo, holdRepo, zap.NewNop(), cfg.DataRetention.AuditRetention, cfg.DataRetention.AuditCleanInterval)` with both > 0; over a bounded window assert `HoldScopeIDs` recorded per tick (scope `AuditHoldScopeActor`) and the exempt list reaches `PurgeOlderThanExcluding` with zero bare calls. `TestAuditCleanerWiringGateConditions` — with `AuditCleanInterval == 0` or `AuditRetention == 0`, the replicated composition-root `&&` gate starts nothing: zero `HoldScopeIDs`, zero purges (P-2 note applies) | `go test ./internal/app/ -count=1` |
| A-3 | Static gate: exactly one `repository.RunAuditCleanerWithHolds(`, zero bare `repository.RunAuditCleaner(` in `new.go`; fixtures prove the checks are live | **C-6** in `scan_new_go`; self-tests: `positive/cleaner.go` must redden, `negative/cleaner.go` must pass, updated `negative/new.go` must pass, all existing self-tests intact. Revert drill on the live tree: temporarily restore `repository.RunAuditCleaner(` at `:337` ⇒ red; remove the WithHolds call entirely ⇒ red; restore ⇒ green | `bash scripts/check-composition-root.sh` |
| A-4 | Full done gate green; untouched gates stay green as no-op regression | new.go = 499 ≤ 500, audit.go = 320, repository/audit.go = 196; no migrations/load-assets/proto/architecture changes | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-audit-manifest && make check-anchored-event-ids && make check-composition-root && make check-integration-compile && make build` (+ no-op regression: `make check-migration-set && make check-load-scenarios && make check-proto-sync && make check-architecture`) |
| A-5 | Fail-closed ledger guarantee preserved | `requireAuditLedger` still panics on nil/typed-nil ledger; the value fed to `NewAuditHoldRepository` is the identical non-nil `auditLedgerRepo` (V12); single construction site | `go build ./...`; review + `make check-composition-root` (the single `NewAuditHoldRepository` in the production tree is not statically pinned — covered by review/diff) |

Verification order: (1) `make test`; (2) `make check-composition-root` + revert drill; (3) `make check-file-sizes`; (4) full done gate; `make test-integration` optional.

---

## 9. Out of scope (explicit non-goals)

- **No purge/exemption behavior changes** in `internal/repository/audit.go` or `internal/repository/audit_hold.go` — the exemption path is already correct and integration-tested; **exception:** R-5's stop-closure idempotency (required by correction C-1, purge-semantics-neutral).
- **No removal of the exported `repository.RunAuditCleaner`** (AGENTS.md: preserve current APIs; nil-holds semantics documented; C-6 bans the bare form only in `new.go`).
- **No hold CRUD service/handler changes**, no ledger/WORM/anchoring changes, no per-tenant retention policies, no config schema change, no new flags.
- **No migrations, no Docker/integration test additions, no new packages.**
- **No reordering of `New()`** beyond the two prescribed moves.
- **Out of scope by explicit note:** F7 fail-closed-on-`HoldScopeIDs`-error, §6.1 `buildAuditCleaner` helper extraction, §4 F2 exact-prefix pin — recommended follow-ups, deliberately not part of this change set.

---

## 10. References

- `docs/auto/analyses/internal-app-73e67e01.json` — source analysis (requirements evidence, verified in §1).
- `internal/app/new.go:131,134,336-343,375,447-448`; `internal/app/audit.go:288-296`; `internal/app/app.go:125`.
- `internal/repository/audit.go:105-106,122-124,146-147,158-160,166-168,174,184,187-194`; `internal/repository/audit_hold.go:39,71-77,174-177`.
- `internal/repository/audit_hold_integration_test.go:21-69`; `internal/domain/audit_hold.go:9`; `internal/config/config.go:357-358`.
- `scripts/check-composition-root.sh` (C-1..C-5, self-tests, fixtures); `Makefile:122-124`; `internal/app/outbox_wiring_test.go:16-46,66-79,83-115`; `internal/outbox/repository.go:183+` (double-stop-safe precedent).
- `docs/plans/2026-08-04-distributed-account-aggregation.md:423` (P4-07 audit retention); `docs/TODO.md` 2026-08-05 round (legal hold + WORM anchoring, P4-03/P4-07).
- AGENTS.md — authority contract, done gate, engineering constraints (500-line gate, API preservation, fail-closed audit contract).
