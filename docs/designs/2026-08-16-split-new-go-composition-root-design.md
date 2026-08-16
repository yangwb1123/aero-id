# Design: Split the new.go composition root into sibling builders with a pinned per-file budget

- **Date:** 2026-08-16
- **Direction:** Split the new.go composition root into sibling builders with a pinned per-file budget
- **Spec:** `internal-app-73e67e01` (analysis `docs/auto/analyses/internal-app-73e67e01.json`, approved)
- **Status:** Design proposal — no repository code changed

---

## 0. Verification verdict on the supplied evidence

Every claim in the evidence was re-verified against the tree. All are confirmed; two are corrected in the
evidence itself and one citation in it is self-correcting. No claim materially changes the direction.

| # | Claim (abridged) | Verdict | Measured |
|---|---|---|---|
| E1 | new.go is 499 lines, headroom 1 vs the 500 gate | ✅ | `wc -l internal/app/new.go` → 499 (analysis's 497 is superseded; evidence's correction confirmed) |
| E2 | `New(ctx, cfg, log) (*App, error)` at :32 is the single exported entry point | ✅ | `func New(` at :32; `cmd/server/main.go:38` has exactly one `app.New(ctx, cfg, log)` |
| E3 | C-pins: C-1 :131, NewFileRepository :140 (order ok), C-2 drain :170, C-6 :339, zero bare | ✅ | `:131` `:140` `:170` `:339`; `grep -c 'repository.RunAuditCleaner('` → 0 |
| E4 | Service block :184–260 (13 `service.New*` sites incl. control plane), `assertAuditWiring` calls :261–283, shared state :284–337, cleaners/degradation/health/compliance :338–380, router :381–423, `buildHTTPServer` :424, `buildGRPCServer` :426, failure path :437–472, `return &App{…}` :474–499 | ✅ | All anchors confirmed by `grep -n`/`sed`; the `assertAuditWiring`/`auditWiredService`/`assertComplianceAuditWiring` **definitions** live in `internal/app/audit.go` (:261/:275/:308) — only their call sites and the `auditWiredService` literal sit in new.go |
| E5 | `Makefile:65` `MAX_FILE_LINES ?= 500`; `check-file-sizes` recipe :66–81 inline bash, no self-test fixtures | ✅ | Confirmed |
| E6 | `Makefile` `check-composition-root` target (:122–124) → `@bash scripts/check-composition-root.sh` | ✅ | Confirmed |
| E7 | Script pins C-1/C-2/C-3/C-6 (`scan_new_go`), C-2b (`scan_audit_go`), C-4 (`scan_main_go`), C-5 (`scan_migrate_main_go`); self-tests ≈:119–141; live scan :143–150; missing file fails loud; fixtures at `scripts/testdata/composition-root/{positive,negative}` | ✅ | Confirmed (script is 156 lines; `SELF-TEST FAIL` strings at :126–128; fixtures list matches) |
| E8 | Extraction-note pattern: outbox.go:13–15 (`:14` "…from new.go so the composition root stays under the file-size gate (P1-08)"), storage_gc_audit.go:57–58 | ✅ | Confirmed; the analysis's `storage_gc_audit.go:71` citation is an offset — actual note at :57–58 (evidence self-corrects) |
| E9 | Sibling builders all package `app` (connectors/replication/eventbus/audit/auth/billing/grpc/http/health/ratelimit/outbox/storage_gc_audit); `outboxDependencies` outbox.go:18, `grpcDependencies` grpc.go:19 | ✅ | `grep -n '^func build'` confirms all; structs at :18/:19 |
| E10 | No gate other than `check-composition-root` greps new.go | ✅ | Only `scripts/check-composition-root.sh:146` + header comments + Makefile comment |
| E11 | `composition_root_integration_test.go:121,148,157` carry stale "new.go:168" refs to the drain (currently :170) | ✅ | Confirmed verbatim |
| E12 | No `internal/app/*_test.go` structural pins on new.go layout | ✅ | Only the E11 string refs mention new.go |
| R-4 prerequisites | `check-file-sizes` has no fixture infra; budget must live in the script | ✅ | Makefile recipe is inline bash; `scripts/` is the only fixture home |

**Design-relevant discovery (not in the evidence):** the drain Start at `new.go:170` is *line-invariant* under
the proposed split. Every block that moves (service construction :184+, shared state :284+, cleaners :358+,
router :381+) lies **below** :170, and nothing above :170 changes. So the drain's line number stays :170 and
R-7's "if the line moves" trigger does not fire — the three "new.go:168" strings remain pre-existing stale
references. Fixing 168 → 170 is a permitted, zero-risk drive-by within R-7's scope (the strings are explicitly
in scope) but is not mandatory for the gate.

---

## 1. Problem statement

`internal/app/new.go` (499 lines, `wc -l`) is the server composition root. `check-file-sizes`
(`MAX_FILE_LINES ?= 500`) leaves **1 line** of headroom. AGENTS.md: "over 500, split before feature work";
the B3 control-plane wiring (account, region-fencing, legacy-cutover, aggregation, event-ingest, operation,
sync-job, replay, projection-replay — all already inline at new.go:210–257) will trip the gate on the next
wiring commit, forcing a mid-feature split that risks the byte-exact C-1/C-2/C-3/C-6 seam pins in
`scripts/check-composition-root.sh`.

The extraction pattern is established in-tree (outbox.go, storage_gc_audit.go, and 10 more sibling builders),
but the three largest blocks — service construction + audit-wiring asserts (:184–283), shared-state/worker
setup (:284–337 + non-pinned parts of :338–380), and router/handler assembly (:381–423) — remain inline.
The fix: move them to sibling package-`app` builders behind package-private `build*` constructors, and pin a
400-line budget on `new.go` with a self-testing, Docker-free gate so the next bloat reddens mechanically
*before* it reaches the 500-line global gate.

---

## 2. Constraints (frozen surfaces)

1. **C-pins stay byte-exact in new.go** — `check-composition-root` uses `grep -cF` fixed strings on single
   lines. Frozen, in relative order, exactly one each:
   - C-1: `callbackSvc, auditLedger, auditLedgerRepo, err := buildAuditStack(cfg, gdb, controlMetrics)` (new.go:131)
   - C-3: C-1 precedes the single `repository.NewFileRepository(` (new.go:140)
   - C-2: `stopCallbacks := callbackSvc.Start(context.Background(), cfg.Callback.WorkerInterval)` (new.go:170)
   - C-6: exactly one `repository.RunAuditCleanerWithHolds(` (new.go:339) and zero `repository.RunAuditCleaner(`
   - C-2b: `internal/app/audit.go` keeps zero `callbackSvc.Start(`
   - C-4/C-5: `cmd/server/main.go` (one `app.New(ctx, cfg, log)`, zero `AuditGovernance`) and
     `cmd/migrate/main.go` (one `config.Load(`, one `migrate.New(`) are untouched.
2. **No scan-pattern edits to the script** except the new C-7 size pin (R-4).
3. **No public API change** — `New(ctx, cfg, log) (*App, error)` and the `App` struct (app.go) are unchanged.
4. **Runtime behavior identical** — same construction order, same log lines, same workers, same OPT-IN
   conditionals. The grpc-listen failure path (new.go:437–472) keeps its exact set and order of **12** stop
   calls (relay, idem, GC, rate-limit, audit-clean, notif-clean, outbox-clean, callbacks, operations,
   sync-jobs, event-bus, degradation); the pre-existing omission of `stopReplication`/`traceShutdown` on
   startup failure is out of scope and must be preserved.
5. **No reordering inside moved blocks; no new dependencies; no new packages.** New symbols are lowercase and
   reachable only from `New` (composition-root reachability).
6. **Scope exclusions:** no changes to `api/proto/**`, `migrations/`, `configs/`, `cmd/*`; no changes to the
   global 500-line gate.

---

## 3. Target design

### 3.1 Block inventory (verified boundaries in current new.go)

| Block | Lines | Content | Destination |
|---|---|---|---|
| S | :184–283 | `fileSvc`…`banSvc` (13 `service.New*` incl. `buildEventBusConsumer`, `buildProjectionReplication`, `stopOperations`, `stopSyncJobs`), `permissionChecker` selection, `assertAuditWiring` call + `auditWiredService` literal, conditional projection-replication assert | `services.go` |
| W | :284–337 | Redis client, edge rate limiter + `stopRateLimit`, `buildDistributedRateLimiter`, idempotency store + `stopIdemClean` | `workers.go` |
| C6 | :338–357 | audit TTL cleaner conditional (`stopAuditClean`, C-6) — **stays in new.go** | new.go |
| W2 | :358–380 | notif cleaner, outbox cleaner, degradation state + `stopDegradation`, `buildHealthHandler`, `buildAuditComplianceServices` + `assertComplianceAuditWiring` | `workers.go` |
| R | :381–423 | `buildAuditLedgerHandler`, `router := httpapi.NewRouter(…)` with all `httpapi.New*Handler` calls (incl. inline `service.NewLegacyMigrationService(repository.NewLegacyMigrationRepository(gdb), …)`) | `handlers.go` |
| pinned | :32–183, :424, :426–436, :437–472, :474–499 | DB/storage/tracing/metrics/SSO/token-validator/hub/outbox-relay/GC/audit-stack/repos/drain; `buildHTTPServer`; `buildGRPCServer` + health/reflection; failure path; `return &App{…}` | stays in new.go |

### 3.2 `internal/app/services.go` — builder `buildServices`

```go
// serviceDependencies carries the pieces service construction needs; extracted
// from new.go so the composition root stays under the file-size gate (C-7).
// Mirrors the outboxDependencies/grpcDependencies struct pattern.
type serviceDependencies struct {
    cfg              *config.Config
    log              *zap.Logger
    gdb              *gorm.DB
    store            storage.FileStorage
    ssoClient        *sso.Client
    sourceConnectors []connector.Connector
    controlMetrics   service.ControlPlaneMetrics
    metricsReg       *metrics.Registry
    billingRuntime   billingRuntime
    auditLedger      service.AuditLedgerService
    auditLedgerRepo  *repository.AuditLedgerRepository
    // repos constructed in new.go :141–169:
    fileRepo, msgRepo, commentRepo, notifRepo, blockRepo, prefRepo,
    adminRepo, banRepo, accountRepo, snapshotRepo, eventReceiptRepo,
    operationRepo, syncJobRepo repository…
}

// serviceBundle carries every service + worker-stop the rest of New needs.
type serviceBundle struct {
    fileSvc, msgSvc, commentSvc, notifSvc, blockSvc, prefSvc, searchSvc, adminSvc,
    accountSvc, regionFencingSvc, cutoverSvc, aggregationSvc, eventIngestSvc,
    operationSvc, replaySvc, projectionReplaySvc, syncJobSvc, banSvc, replicationSvc …
    permissionChecker middleware.PermissionChecker
    stopEventBus, stopReplication, stopOperations, stopSyncJobs func()
}

func buildServices(deps serviceDependencies) (serviceBundle, error)
```

Body: the verbatim statements from :184–283 with the `deps.*` substitutions; the `assertAuditWiring` call
and the projection-replication assert run inside the builder (all asserted services are in the bundle), so
the fail-closed audit-wiring insurance is unchanged. The two already-extracted builders
(`buildEventBusConsumer`, `buildProjectionReplication`) are invoked from here in their current relative
order; their `err` returns propagate through `(serviceBundle, error)`.

### 3.3 `internal/app/workers.go` — builder `buildWorkers`

```go
type workerDependencies struct {
    cfg *config.Config; log *zap.Logger; gdb *gorm.DB; sqlDB *sql.DB
    ssoClient *sso.Client; redisClient rueidis.Client
    notifRepo, outboxRepo, holdRepo repository…
    auditLedger service.AuditLedgerService; auditLedgerRepo *repository.AuditLedgerRepository
    store storage.FileStorage            // for buildAuditComplianceServices
    billingRuntime billingRuntime        // billingRuntime.probe
    cutoverSvc service.LegacyCutoverService // from buildServices (assertComplianceAuditWiring input)
}

type workerBundle struct {
    stopRateLimit, stopIdemClean, stopNotifClean, stopOutboxClean, stopDegradation func()
    degradationState *middleware.DegradationState
    health *grpcsrv.HealthServer        // buildHealthHandler result
    holdSvc, anchorSvc, cutoverFacadeSvc service… // consumed by buildRouter's audit handler
}

func buildWorkers(deps workerDependencies) workerBundle
```

Body: verbatim :284–337 then :358–380 (skipping the pinned :338–357 C-6 conditional, which stays in
new.go). `assertComplianceAuditWiring` runs here (cutoverSvc arrives via `workerDependencies`), preserving
the fail-closed wiring assert. `stopRateLimit`/`stopIdemClean` flow out of this bundle so the failure path
in new.go is unchanged.

### 3.4 `internal/app/handlers.go` — builder `buildRouter`

```go
type routerDependencies struct {
    cfg *config.Config; log *zap.Logger; gdb *gorm.DB
    metricsReg *metrics.Registry
    idemStore idempotency.Store; limiter *ratelimit.Limiter
    distributedRateLimiter ratelimit.DistributedLimiter
    degradationState *middleware.DegradationState
    tokenValidator grpcsrv.TokenValidator; health *grpcsrv.HealthServer
    sourceConnectors []connector.Connector; sseHub *hub.Hub; notifRepo repository…
    downloader httpapi.SignedDownloader
    callbackSvc service.CallbackService; auditLedger service.AuditLedgerService
    svcs serviceBundle; workers workerBundle   // bundles keep the arg count sane
}

func buildRouter(deps routerDependencies) http.Handler
```

Body: verbatim :381–423 — `buildAuditLedgerHandler` (from `auditLedger`, `workers.holdSvc/anchorSvc`,
`svcs.syncJobSvc`, `svcs.permissionChecker`), then the full `httpapi.NewRouter(…)` call including the inline
`service.NewLegacyMigrationService(repository.NewLegacyMigrationRepository(deps.gdb), deps.sourceConnectors…)`
and `buildEventSourceBindings(cfg.EventIngest.Bindings)` calls. Pure construction — no worker starts, so no
stop functions flow from here. `presignTTL` is a package const (`app.go:25`) and stays put.

### 3.5 `internal/app/new.go` after the split (what remains)

DB/storage/tracing/metrics/SSO/token-validator/hub/outbox-relay/GC-sweeper (:32–130), C-1 (:131),
repo construction (:140–169), C-2 drain (:170), the three builder calls + bundle destructuring
(services, workers, router), the C-6 conditional, `buildHTTPServer`, `buildGRPCServer` + health/reflection,
the 12-stop failure path, and `return &App{…}`.

**Line budget math:** 499 − (S:100) − (W:54) − (W2:23) − (R:43) + ~30 (builder-call struct literals and
destructuring) ≈ **~309**, plus closing/blank drift — expected ≈ 290–320. Requirement: **≤ 400** with
`-gt` semantics shared with `check-file-sizes` (400 passes, 401 trips). The evidence's ≈250–270 expectation
is optimistic (it under-counts the struct literals and the C-6/return/failure-path floor, which stay);
≈290–320 still leaves ~80–110 lines of headroom for future B3 wiring before the next split is forced.

### 3.6 C-7 gate and fixtures

**Makefile** (adjacent to `Makefile:65`):

```make
MAX_FILE_LINES ?= 500
MAX_COMPOSITION_ROOT_LINES ?= 400
```

and the target:

```make
check-composition-root:
	@MAX_COMPOSITION_ROOT_LINES=$(MAX_COMPOSITION_ROOT_LINES) bash scripts/check-composition-root.sh
```

(`?=` semantics preserved: `make MAX_COMPOSITION_ROOT_LINES=500 check-composition-root` overrides.)

**`scripts/check-composition-root.sh`** — add to `scan_new_go` (after the C-6b check; C-7 documented in the
header and success message):

```bash
# C-7  composition-root size budget: wc -l <file> must be ≤ MAX_COMPOSITION_ROOT_LINES
#      (default 400). Same wc -l counting and strict -gt semantics as
#      check-file-sizes so both gates agree on the boundary (400 passes, 401 trips).
local nlines budget
budget="${MAX_COMPOSITION_ROOT_LINES:-400}"
nlines="$(wc -l < "$file")"
[ "$nlines" -le "$budget" ] || violations="${violations}C-7 composition root size: $nlines lines > $budget (MAX_COMPOSITION_ROOT_LINES) — split into sibling builders
"
```

**Fixtures** (mirror existing doctrine; `wc -l` counts newlines, so files must end with a trailing newline):

- `scripts/testdata/composition-root/positive/size.go` — exactly 401 lines; otherwise satisfies every C-pin
  (one C-1 line, one C-2 line, `repository.NewFileRepository(` after the C-1 line, exactly one
  `repository.RunAuditCleanerWithHolds(`, zero bare form). Pad to 401 with inert comment lines that contain
  none of the pinned substrings. Reddens **only** via C-7.
- `scripts/testdata/composition-root/negative/size.go` — exactly 400 lines satisfying the same pins; passes.

Wire into the self-test block with explicit messages:

```bash
scan_new_go "$FIXTURES/positive/size.go" >/dev/null 2>&1; rsize=$?
[ "$rsize" = "0" ] && { echo "SELF-TEST FAIL: positive/size.go (401 lines) passed — C-7 size pin not reddening"; fail=1; }
scan_new_go "$FIXTURES/negative/size.go" >/dev/null 2>&1; rnsize=$?
[ "$rnsize" != "0" ] && { echo "SELF-TEST FAIL: negative/size.go (400 lines) reddened — C-7 boundary off by one"; fail=1; }
```

Because C-7 runs inside `scan_new_go`, it also applies to the existing fixtures (`positive/new.go`,
`positive/cleaner.go`, `negative/new.go`, `negative/cleaner.go`) — all are small (≪400) so no existing
fixture reddens; keep it that way.

---

## 4. API changes

**Public surface: none.**
- `New(ctx context.Context, cfg *config.Config, log *zap.Logger) (*App, error)` — unchanged signature,
  single exported entry point of package `app`.
- `App` struct fields (app.go:17–42) — unchanged.
- `cmd/server/main.go`, `cmd/migrate/main.go` — byte-unchanged.
- HTTP/gRPC/OpenAPI/protobuf/config/migration surfaces — untouched.

**Package-private surface (new, all lowercase, reachable only from `New`):**
- `services.go`: `serviceDependencies`, `serviceBundle`, `buildServices`
- `workers.go`: `workerDependencies`, `workerBundle`, `buildWorkers`
- `handlers.go`: `routerDependencies`, `buildRouter`

**Gate interface (tooling API):**
- New Makefile variable `MAX_COMPOSITION_ROOT_LINES ?= 400` (overridable).
- New environment input to `scripts/check-composition-root.sh` (`MAX_COMPOSITION_ROOT_LINES`, default 400).
- New diagnostic `C-7 composition root size: N lines > B` in scan output.
- New fixtures `positive/size.go` / `negative/size.go` in the self-test doctrine.

---

## 5. Compatibility constraints (checklist)

1. C-1/C-2/C-6 single-line, byte-identical, exactly one each; C-3 order preserved; all four in new.go.
2. `scripts/check-composition-root.sh` unchanged except the C-7 pin + self-test block (R-4 is the only
   permitted script change).
3. Statement order inside each moved block is preserved verbatim (no reordering, no reformatting that would
   alter observable startup logs).
4. The failure path keeps its 12-stop set and order; `stopReplication`/`traceShutdown` omission on startup
   failure is preserved (out of scope by direction).
5. No new inter-service coupling; bundles are the only new plumbing.
6. New files ≤ 500 lines; every function ≤ 50 lines; cyclomatic ≤ 10 (constructors trivially satisfy).
7. Extraction-note doc comments on all three builders, mirroring outbox.go:13–15 /
   storage_gc_audit.go:57–58.
8. `check-audit-swallow` / `check-audit-manifest` scan `internal/app` — moved audit-option call sites keep
   their exact shape (pure code motion), so their greps stay green.

---

## 6. Failure modes and mitigations

| # | Failure mode | Mitigation |
|---|---|---|
| F1 | **Fixture coupling under C-7:** C-7 applies to every file `scan_new_go` scans; a malformed fixture could redden on a C-pin rather than size, or the existing fixtures could start reddening on size, voiding the self-test's proof value | Fixtures are deliberately constructed to differ *only* in the 401-vs-400 dimension and to avoid all pinned substrings in comments/padding; keep all existing fixtures ≪400; the self-test asserts both directions on every run |
| F2 | **Boundary disagreement between gates:** `wc -l` counts trailing newlines; a 400-line file without a trailing newline reports 399 | Use identical `wc -l < f` + strict `-gt` semantics in both gates; fixtures carry trailing newlines; boundary is 400-passes/401-trips everywhere |
| F3 | **Fixed-string grep sensitivity:** a moved/reformatted C-1/C-2/C-6 line, or a stray comment containing a pinned substring anywhere in the new files, reddens the gate (count 2) | Treat the pinned lines as frozen; run `make check-composition-root` after each extraction step, not just at the end; the gate's own fixtures already prove the substring semantics |
| F4 | **Stop-flow regression:** a stop function created inside a builder not flowing back to `New` would silently change shutdown/failure behavior | All four service stops and five worker stops flow out through the bundles; the failure path and `return &App{…}` use every stop, so a missing flow is a compile error (nil-checks and struct fields force it) |
| F5 | **Order/behavior drift during code motion:** `err` shadowing, log-line reordering, or conditional changes inside moved blocks alter observable behavior | Pure copy-paste motion (no logic edits); `git diff` review must show only code motion; boot e2e (`composition_root_integration_test.go`) + `make test` are the behavioral backstop |
| F6 | **Gate reddens before the split lands** (live new.go is 499 > 400 the moment C-7 ships) | Land script + fixtures + split in the same change so CI never sees a red live scan; if split separately, accept the intentional reddening as the C-7 proof step |
| F7 | **R-7 staleness:** the drain Start stays at :170, so the three "new.go:168" strings stay stale; a future move would leave the suite pointing at the wrong line | Fix 168 → 170 in `composition_root_integration_test.go` as a permitted drive-by (R-7 scope explicitly covers these strings; string-only change, no behavioral change) |
| F8 | **Post-split regrowth:** next B3 wiring commit pushes new.go past 400 | That is the gate's purpose: C-7 reddens at 401, forcing the next deliberate split before the 500-line global gate trips mid-feature |
| F9 | **Bundle struct bloat:** `serviceDependencies`/`routerDependencies` are large structs | This is the documented `outboxDependencies`/`grpcDependencies` pattern (E9); the structs are package-private, one-file, and keep builder signatures under ~5 args; split a bundle further only if a future file exceeds 500 lines |
| F10 | **Composition-root reachability:** a moved symbol accidentally exported or called elsewhere | All new symbols are lowercase; the only callers are inside `New`; `go vet` + `golangci-lint` (unused/exported checks) enforce |

---

## 7. Migration steps (ordered, gate-first)

1. **Gate first (same change as the split):** add `MAX_COMPOSITION_ROOT_LINES ?= 400` next to
   `Makefile:65`; change the `check-composition-root` target to export it.
2. **C-7 pin:** add the size check inside `scan_new_go` (§3.6) and document C-7 in the header + success
   message.
3. **Fixtures + self-test:** create `scripts/testdata/composition-root/{positive,negative}/size.go`;
   wire both into the self-test block with `SELF-TEST FAIL` messages.
4. **Prove the fixture directions:** run `bash scripts/check-composition-root.sh` — self-tests pass, live
   scan reddens on C-7 (499 > 400). This is the expected red state *before* step 5.
5. **Extract `services.go`:** move :184–283 verbatim into `buildServices`; define the two structs; replace
   the block in new.go with `svcs, err := buildServices(serviceDependencies{…})`; use the compiler to
   enumerate missing struct fields; run `make build` + `make test`.
6. **Extract `workers.go`:** move :284–337 and :358–380 (skipping :338–357) into `buildWorkers`; the C-6
   conditional stays in new.go; flow `stopRateLimit`/`stopIdemClean`/`stopNotifClean`/`stopOutboxClean`/
   `stopDegradation` + `degradationState`/`health`/`holdSvc`/`anchorSvc`/`cutoverFacadeSvc` out.
7. **Extract `handlers.go`:** move :381–423 into `buildRouter`; replace with
   `router := buildRouter(routerDependencies{…})`.
8. **R-7 drive-by:** update the three "new.go:168" strings to "new.go:170" in
   `composition_root_integration_test.go` (drain Start stays at :170 — see §0).
9. **Verify purity:** `git diff` shows only code motion + new files; `wc -l internal/app/new.go` ≤ 400;
   `wc -l internal/app/{services,workers,handlers}.go` ≤ 500; every new symbol lowercase; extraction-note
   doc comments present.
10. **Done-gate:** run the full gate (§8) once; every command exits 0.

---

## 8. Testable acceptance mapping

| Acceptance | Testable form | Pass criterion |
|---|---|---|
| A1 (split + global gate) | `make check-file-sizes` | exit 0; `wc -l internal/app/services.go internal/app/handlers.go internal/app/workers.go` each ≤ 500 |
| A2 (seam pins) | `make check-composition-root` | exit 0 with default budget; self-tests pass; `grep -cF` shows C-1/C-2/C-6 each = 1 in new.go; C-3 order (C-1 line < NewFileRepository line); `cmd/server/main.go` still one `app.New(ctx, cfg, log)` + zero `AuditGovernance` |
| A3 (behavior) | `make test && make lint && make lint-strict && make build` | each exit 0; `composition_root_integration_test.go` passes (boot path, drain start, audit stack) |
| A4 (new regression gate) | `MAX_COMPOSITION_ROOT_LINES ?= 400` in Makefile + exported to the script; C-7 pin in `scan_new_go`; fixtures `positive/size.go` (401, reddens) / `negative/size.go` (400, passes) wired into self-test; `wc -l internal/app/new.go` ≤ 400 | script's self-test block proves both fixture directions on every invocation (green run is itself the proof); live new.go ≤ 400 |
| R-7 (test line refs) | `grep -n "new.go:" internal/app/*_test.go` | no reference to a line other than the drain Start's current line (:170) |
| R-3 (exported surface) | `git diff --stat cmd/`; `git diff` review | `cmd/` untouched; `New` signature and `App` struct unchanged; failure path still 12 stops in the same order |
| R-6 (conventions) | `git diff --stat` | changes confined to `internal/app/**`, `Makefile`, `scripts/check-composition-root.sh`, `scripts/testdata/composition-root/`, test-message strings |

---

## 9. Risks and open items

1. **Post-split line count** is estimated at ≈290–320 (evidence expects 250–270; the delta is builder
   struct literals and the pinned floor). If the measured count unexpectedly exceeds 400, fall back to
   moving the `buildHTTPServer`/`buildGRPCServer` call sites (each one line) into handlers.go — they are not
   pinned — before touching anything pinned.
2. **`buildAuditLedgerHandler` placement:** it sits at the head of the router block (:381). It could equally
   live in workers.go; keeping it with the router block preserves the evidence's block boundary and R-1's
   statement-order rule. Either is acceptable if statements are not reordered.
3. **Fixture padding hygiene:** the 401-line positive fixture is padding-heavy; a future editor adding a
   pinned substring in a comment would silently change which pin fires. Mitigate with a fixture-local
   comment block stating "do not add the pinned strings here".
4. **Overshoot of R-4's default:** the default budget (400) is an upper bound, not a target; the measured
   ≈290–320 leaves real headroom for the next B3 wiring batch, after which the next split is forced
   mechanically rather than mid-feature.
5. **The 499-line measurement** (evidence) supersedes the analysis's 497; both are above the intended
   post-split budget, so the direction is unaffected.
