# Design — Emit operation outcome and age metrics from the live worker

**Date:** 2026-08-16
**Module:** `internal/service` (emission sites) + `internal/pkg/metrics` (existing registry, untouched)
**Direction id:** `internal-pkg-metrics-63990fa1`
**Status:** Design (evidence treated as untrusted; all claims re-verified against repository HEAD; two substantive corrections)

## 1. Verification verdict

Every load-bearing claim in the source evidence was re-checked against the live tree. All structural
claims are confirmed. Two substantive corrections are required, both verified by tracing the actual
status-computation code:

| # | Claim | Verified state |
|---|---|---|
| 1 | `operation_state.go:175` `finishOperation` is the sole terminal-transition choke point; computes `progress := inspectOperationProgress(operation.Steps)` then `status := progress.status()`; persists via `setOperationStatus(..., "operation.finished", ...)` | ✅ exact. Callers are only `runClaimed` (`operation_worker.go:46`) and `reconcileClaimed` (`operation_state.go:47`); the only other `setOperationStatus` callers set `running` (`operation_worker.go:27`) / `compensating` (`operation_compensation.go:28`) |
| 2 | `operation_worker.go:99` `Start`, `:121` `processRunnable` (drains `ListRunnable(16)` then reconcile-runnable `ListReconcileRunnable(16)`), `:142` `processOperations`, `:165` `operationInRegion` | ✅ exact. `processRunnable` at 121, not 139 (139 is inside the cited span; substance holds) |
| 3 | `operation.go` — metrics field, `WithOperationMetrics` at 87-90 (nil-guarded), `NewOperationService` defaults `noopControlPlaneMetrics{}` (never nil) | ✅ exact |
| 4 | Seam wired in composition root | ✅ substance true, stale file/line. Wiring is `internal/app/services.go:150-156` (`NewOperationService` :150, `WithOperationMetrics(deps.controlMetrics)` :156); worker start assertion at **158-159** (evidence said 160-162 — trivial drift); `controlMetrics := service.NewRegistryControlPlaneMetrics(metricsReg)` at `new.go:88` |
| 5 | `control_plane_metrics.go:29,34` seam declares both observers; noop stubs :51,:56; adapter :103-104,:118-119 | ✅ exact |
| 6 | Registry implements both; exposition `aero_control_operations_total{status=...}` / `aero_control_operation_age_seconds{status=...}`; gauge staleness 5 min | ✅ `ObserveOperationOutcome` at `control_plane.go:186-187`, `ObserveOperationAge` at :211-212 (evidence's 206-222 range is loose but point-accurate); exposition confirmed in `writeOperationAndCallbackCounters` (:309) and `writeControlPlaneText` (:248); `gaugeMap.snapshot()` drops entries stale > 5 min |
| 7 | Zero live callers; only `metricSpy` no-op stubs at `callback_audit_test.go:177` (outcome) and :184 (age); spy has no recording fields for these two | ✅ exact |
| 8 | File sizes 193/171; all named gates exist in Makefile; `check-composition-root` pins only `new.go`/`cmd/*` lines | ✅ `operation_state.go` = 193, `operation_worker.go` = 171; gates at Makefile :24-25/:35-36/:69/:126; composition-root script pins C-1/C-2/C-3/C-6/C-7 on `new.go`, C-4/C-5 on `cmd/*` — no `services.go` pin |
| 9 | `domain.Operation.CreatedAt time.Time` (:43), `CompletedAt *time.Time` (:45); status vocabulary `requested\|running\|waiting\|unknown\|partially_completed\|completed\|failed\|compensating` | ✅ exact |
| 10 | **AC-1 `partially_completed` fixture** `[{Failed}, {Succeeded, CompensationAction:"undo", CompensationStatus:Pending}]` | ❌ **WRONG — produces `unknown`, not `partially_completed`.** `observeCompensation` sets `compensationUnknown=true` for `CompensationStatus=Pending`, and `status()` returns `OperationUnknown` when `p.unknown || p.compensationUnknown` *before* consulting `failedStatus()`. Correct fixture: `[{Failed, CompensationAction:"undo", CompensationStatus:Failed}]` → `compensationFailed=true` → `failedStatus()` → `OperationPartiallyCompleted`. See §3 |
| 11 | **R-1/R-4 unconditional outcome emission** | ❌ **Design gap.** (a) `finishOperation` can persist a *non-terminal* `waiting` status in the two-worker race (worker B claims a step between `ListRunnable` and `ClaimNextStep` → `ErrOperationStepUnknown` → break → open steps → `progress.status()` = `waiting`). Unconditional emission would put a `waiting` label on the counter the registry documents as "terminal operation outcomes". (b) A still-`unknown` operation is re-finished on every reconcile pass (paced ≥ 1 min by `ReconcileUnknownRetryDelay`), so unconditional emission re-counts the same outcome repeatedly. Design adopts a **terminal + transition guard** (§3). |
| 12 | Test scaffolding | ✅ exists but with gaps the design must fill: `crashTestRepo.ListRunnable` always returns `nil` (Case A needs an override); `crashTestRepo.ListReconcileRunnable` always returns the op id (Case A must override to `nil` or it double-emits); `crashTestService` returns the `OperationService` **interface** (white-box access needs `.(*operationService)`); `crashedOperation` sets **no `CreatedAt`** (tests must set it). Both test files are `package service` (white-box compatible). |

Additional verified facts relied on by this design:

- **The two list feeds are disjoint by operation status**: `ListRunnable` selects `status IN (requested, running, waiting)`; `ListReconcileRunnable` selects `status IN (unknown, compensating)` (`internal/repository/operation_worker.go:11,37`). An operation cannot appear in both lists in one pass, so a single emission point in `processOperations` yields exactly one age observation per open operation per pass.
- **Emission ordering is provable**: `setOperationStatus` returns an error only on persist failure (`operation_audit.go:73-88`; audit path uses `SetStatusWithAudit`), so a successful return means the terminal fact is durable. Placing the outcome emission after that return satisfies "emit only after a durable terminal fact".
- **The registry methods cannot fail**: counter/gauge map operations under mutex, no error returns, no panic paths. Emission cannot crash the worker.
- **No new imports needed**: `operation_state.go` imports `time` and `domain`; `operation_worker.go` imports `time` and `domain`.
- **Emission branches stay within gates**: `finishOperation` cyclop 3→4 (≤ 10), funlen ~19→~27 (≤ 50); `processOperations` cyclop 6→7 (≤ 10), funlen ~25→~29 (≤ 50). File sizes 193→~200 and 171→~176 (< 500).

## 2. Problem statement (as verified)

The operation control plane is the one shipped target-architecture feature and is live end-to-end:
`NewOperationService` is wired with `WithOperationMetrics(deps.controlMetrics)` at
`internal/app/services.go:150-156`, and the worker runs every second (`worker.Start(...)` at
`services.go:158-159`). The metrics seam (`internal/service/control_plane_metrics.go:29,34`) and the
registry (`internal/pkg/metrics/control_plane.go:186-187,211-212`) both implement the two observers, and
the registry already exposes them as `aero_control_operations_total{status=...}` and
`aero_control_operation_age_seconds{status=...}` with regression-tested exposition
(`internal/pkg/metrics/metrics_test.go:147,158`).

Yet there are **zero live (non-test) emit sites**: grep across `internal/` matches only the seam
definition, noop stubs, registry, and the no-op test stubs at `callback_audit_test.go:177,184`.
Consequently the running server never produces either family. Operators cannot see operation storms,
unknown-outcome drift, or stuck operations — the exact dark observability AGENTS.md requires
("operation age, unknown outcomes" must be among required metrics).

## 3. API changes

No external API changes: no proto, HTTP, gRPC, config, or schema surface changes. The Go seam
`ControlPlaneMetrics`, the registry, and the composition root are all byte-identical after this change.

The observable API delta is that two existing metric families now appear in `/metrics` exposition on
live deployments:

| Family | Labels | Semantics (as designed here) |
|---|---|---|
| `aero_control_operations_total` | `status ∈ {completed, failed, partially_completed, unknown}` | Counter of **genuine transitions into a terminal status** (per `finishOperation` execution that persists a terminal status different from the loaded one) |
| `aero_control_operation_age_seconds` | `status ∈ {requested, running, waiting, unknown, compensating}` | Gauge of the most recently observed open operation's age per status (`time.Since(CreatedAt)`); entries stale > 5 min drop from exposition (existing registry semantics, unchanged) |

### Change 1 — `internal/service/operation_state.go`, `finishOperation`

Insert after the successful `setOperationStatus` block (after line 191) and before
`return s.repo.Get(ctx, id)`:

```go
	// Metrics: one terminal-outcome observation per genuine transition into a
	// terminal status. The terminalOperationStatus guard keeps a two-worker
	// race that re-finishes an open operation as "waiting" out of the
	// terminal-outcome counter; the status != operation.Status guard stops a
	// reconcile pass on an already-unknown operation from re-counting the same
	// outcome. Emission follows a durable setOperationStatus commit, so an
	// error path emits nothing.
	if terminalOperationStatus(status) && status != operation.Status {
		s.metrics.ObserveOperationOutcome(string(status))
	}
```

`terminalOperationStatus` already exists (`operation_worker.go:49`) and covers exactly
`completed | failed | partially_completed | unknown`.

Why the transition guard (`status != operation.Status`), deviating from the evidence's literal R-1/R-4:

- The registry documents the counter as "status -> terminal operation outcomes" (`control_plane.go`),
  and the exposition suffix is `_total`. A counter of outcomes should count distinct terminal
  transitions, not every re-visit of an already-terminal state.
- `finishOperation` is reachable with loaded `unknown` (via `reconcileClaimed`, which treats `unknown`
  as non-terminal — `operation_state.go:53-55`) and persists `unknown` again when the source still
  cannot confirm. Without the guard, each reconcile pass (paced ≥ 1 min by
  `repository.ReconcileUnknownRetryDelay = time.Minute`) appends a duplicate `unknown` observation
  forever for a stuck operation — indistinguishable from new operations entering `unknown`.
- The first time an operation enters `unknown`, the loaded status is non-terminal (`requested`/`running`/
  `waiting`/`compensating`), so the transition guard still emits that first observation. Stuck-ness
  remains continuously visible through the age gauge.

### Change 2 — `internal/service/operation_worker.go`, `processOperations`

Insert after the load-guard block (after line 147) and before the `if reconcile` branch:

```go
		// Metrics: one age observation per open operation that passed the load
		// guards (Get success, non-nil, in-region). The zero-CreatedAt skip is
		// defensive — time.Since(zero) would expose an ~5.4e13s gauge value.
		if !operation.CreatedAt.IsZero() {
			s.metrics.ObserveOperationAge(string(operation.Status), time.Since(operation.CreatedAt))
		}
```

This is the single choke point for both the runnable and reconcile-runnable feeds (verified disjoint by
status, §1), so each open operation is observed exactly once per worker pass with its loaded status label
and `CreatedAt`-based age. The status label is the *loaded* status (`operation.Status`), which is the
correct "open operation" view for the age gauge.

Accepted semantics (documented limitation): the registry gauge is last-write-wins per tick. With N open
operations of one status, the exposed value is the last-observed age in the pass, not strictly the oldest.
For the typical single-stuck-operation case both coincide; for N > 1 the value is still a live "open
operation age" signal. Preserving the registry's "oldest" comment would require per-pass max tracking,
which adds a branch and a data structure for no operational difference — explicitly out of scope.

## 4. Compatibility constraints

1. **Seam stability**: `ControlPlaneMetrics` interface is unchanged; every implementer (registry adapter,
   `noopControlPlaneMetrics`, test `metricSpy`) compiles without modification. `make check-integration-compile`
   unaffected.
2. **Unwired deployments (R-3)**: a service built without `WithOperationMetrics` keeps the
   `noopControlPlaneMetrics` default; both new emission lines are no-ops. Behavior identical.
3. **Composition root untouched**: no `internal/app` changes; `check-composition-root` pins `new.go` /
   `cmd/*` lines only (verified) — unaffected.
4. **Registry untouched**: exposition names, label formats, counter/gauge types, 5-min staleness, and
   `metrics_test.go:147,158` assertions all unchanged.
5. **No new imports** in either production file (`time` and `domain` already imported).
6. **Gates**: file sizes stay < 500 (`check-file-sizes`); funlen/cyclop within limits (`lint-strict`);
   no new audit sites (metrics-only — `check-audit-swallow`, `check-audit-manifest`,
   `check-anchored-event-ids` unaffected); no proto/migration/load/Makefile changes.
7. **Diff footprint**: exactly `internal/service/operation_state.go`, `internal/service/operation_worker.go`,
   `internal/service/callback_audit_test.go` (metricSpy extension), and a new
   `internal/service/operation_state_test.go`.

## 5. Failure modes

| # | Failure | Behavior | Why it is safe |
|---|---|---|---|
| 1 | `SetStatus`/`SetStatusWithAudit` fails in `finishOperation` | No outcome emission; error propagates as today | Emission is ordered after the durable commit; the metric reflects only durable terminal facts. The worker's next pass retries. Under-count vs. reality is intentional (the fact did not happen) |
| 2 | `Get` errors / returns nil / out-of-region in `processOperations` | No age emission for that operation; loop `continue`s | Guard precedes emission; no panic (`operation` nil-checked) |
| 3 | Zero `CreatedAt` on a loaded operation | Age emission skipped | `time.Since(zero)` ≈ 5.4e13 s would poison the gauge. A zero `CreatedAt` in production is itself a data-integrity anomaly to investigate separately |
| 4 | Reconcile pass on a still-`unknown` operation | No repeated outcome observation (transition guard); age keeps updating | `operations_total{status="unknown"}` counts transitions into unknown; stuck-ness is visible via age |
| 5 | Two workers race on one operation | Loser breaks on `ErrOperationStepUnknown`; `finishOperation` may persist `waiting` → no outcome (terminal guard); age emitted once for its own pass | No non-terminal labels in the terminal-outcome counter; no counter corruption (registry map ops are mutex-guarded) |
| 6 | Worker process down | Age family disappears from exposition after the 5-min staleness window; outcome counter freezes | The family's absence is itself the liveness alert for the worker |
| 7 | Emission itself fails | Cannot | `noop`/registry observers never return errors and have no panic paths |
| 8 | Clock skew between app and DB | Ages skewed by the skew | Known, standard limitation of wall-clock age metrics; `time.Since` uses the monotonic clock within a process, so intra-process ages are skew-free |

## 6. Migration steps

No schema, data, proto, or config migration exists — this is a behavioral rollout of two metric families:

1. **Code**: apply Change 1 and Change 2, extend `metricSpy`, add `internal/service/operation_state_test.go`
   (§7). Run the full done-gate:
   `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow &&
   make check-anchored-event-ids && make check-audit-manifest && make check-load-scenarios &&
   make check-migration-set && make check-composition-root && make check-proto-sync &&
   make check-integration-compile && make build`.
2. **Deploy**: rolling deploy of the API/worker binary. No data migration runs; the families appear on
   `/metrics` within the first worker tick (1 s) once an operation transitions or is open.
3. **Observe/alert**: after confirming a baseline, wire alerts on
   `rate(aero_control_operations_total{status="unknown"}[5m]) > 0`,
   `aero_control_operation_age_seconds{status="unknown"|"running"|"waiting"} > <SLO threshold>`, and
   `absent_over_time(aero_control_operation_age_seconds[6m])` as the worker-down signal.
4. **Rollback**: revert the two emission lines; the families stop being produced. No state to unwind,
   no config flag to toggle, no compatibility risk (unwired deployments already run the noop default).

## 7. Testable acceptance mapping

### Test-only scaffolding (extend `internal/service/callback_audit_test.go`)

Extend the `metricSpy` struct (line 133) with two additive fields and an observation record type;
convert the no-op stubs at lines 177 and 184 to recording methods:

```go
type operationAgeObservation struct {
	status string
	age    time.Duration
}
// fields on metricSpy:
//	operationOutcomes []string
//	operationAges     []operationAgeObservation
func (m *metricSpy) ObserveOperationOutcome(status string) {
	m.operationOutcomes = append(m.operationOutcomes, status)
}
func (m *metricSpy) ObserveOperationAge(status string, age time.Duration) {
	m.operationAges = append(m.operationAges, operationAgeObservation{status: status, age: age})
}
```

Existing `&metricSpy{}` users (18+ sites in `internal/service`) are unaffected (additive fields).

New file `internal/service/operation_state_test.go` (package `service`, white-box). Fixtures reuse
`crashTestRepo`/`crashRunner` where possible and add two small wrappers:

```go
// runnableListRepo overrides the two list feeds on top of crashTestRepo so
// each test drives the runnable vs reconcile-runnable path independently.
type runnableListRepo struct {
	*crashTestRepo
	runnableIDs  []domain.ID
	reconcileIDs []domain.ID
}
func (f *runnableListRepo) ListRunnable(ctx context.Context, limit int) ([]domain.ID, error) {
	return f.runnableIDs, nil
}
func (f *runnableListRepo) ListReconcileRunnable(ctx context.Context, limit int) ([]domain.ID, error) {
	return f.reconcileIDs, nil
}

// setStatusErroringRepo fails the terminal persist to prove the outcome
// observation only follows a durable commit.
type setStatusErroringRepo struct{ *crashTestRepo }
func (f *setStatusErroringRepo) SetStatus(context.Context, domain.ID, domain.OperationStatus,
	string, string, map[string]any) error { return errors.New("persist failed") }
```

White-box access: `crashTestService` returns the `OperationService` interface, so tests use
`svc.(*operationService)` to call `finishOperation`/`processRunnable` directly. All fixtures set
`CreatedAt` explicitly (the existing `crashedOperation` helper does not).

### AC-1 — outcome emitted once per genuine terminal transition, labeled correctly

`TestFinishOperationEmitsOutcomeOncePerTerminalStatus` (table-driven). Per case: build an operation with
`Status: domain.OperationRunning` (non-terminal loaded status, so the transition guard fires) and steps
mapping to the expected terminal status via `inspectOperationProgress`/`progress.status()`:

| Expected label | Steps fixture (verified against the actual progress logic) |
|---|---|
| `completed` | `[{Status: StepSucceeded}]` |
| `failed` | `[{Status: StepFailed}]` |
| `partially_completed` | **`[{Status: StepFailed, CompensationAction: "undo", CompensationStatus: StepFailed}]`** — the evidence's `{Succeeded, CompensationAction:"undo", CompensationStatus:Pending}` pair is a verified bug: it yields `unknown` (pending compensation ⇒ `compensationUnknown` ⇒ `status()` returns `OperationUnknown` before `failedStatus()`) |
| `unknown` | `[{Status: StepUnknown}]` |

Repo = `&crashTestRepo{operation: op, step: op.Steps[0]}`; service = `NewOperationService(repo,
WithOperationMetrics(spy))`; call `svc.finishOperation(context.Background(), op.ID)`.
**Assert:** `len(spy.operationOutcomes) == 1 && spy.operationOutcomes[0] == string(expected)`.

`TestFinishOperationTransitionGuard`: loaded `unknown` + step `Unknown` → `finishOperation` →
**zero** outcome observations (re-entry on unchanged status); loaded `unknown` + step `Succeeded` →
exactly one `"completed"` observation (genuine unknown→completed transition).

### AC-2 — worker pass emits one age observation per open operation

`TestProcessRunnableEmitsOperationAgeForOpenOperations`:
- **Case A (runnable path):** `runnableListRepo{runnableIDs: [id], reconcileIDs: nil}`; op
  `{Status: OperationWaiting, CreatedAt: now-1h}` with one pending step; runner `crashRunner`.
  `svc.processRunnable(ctx)` → **assert** exactly one age observation, `status == "waiting"`, `age` within
  5 s of 1 h. (The `reconcileIDs: nil` override is mandatory — `crashTestRepo.ListReconcileRunnable`
  otherwise returns the id and double-emits.)
- **Case B (reconcile-runnable path):** `runnableListRepo{runnableIDs: nil, reconcileIDs: [id]}`; op
  `{Status: OperationUnknown, CreatedAt: now-1h}` with one `Unknown` step (lease expired);
  runner `crashRunner` (implements `Reconcile`/`ReconcileCompensation`). → **assert** exactly one age
  observation, `status == "unknown"`. Age is emitted before the `reconcile` branch, so the observation
  holds regardless of reconcile outcome; the transition guard keeps `operationOutcomes` empty.
- **Case C (noop default):** same repo/op as Case A, service built **without** `WithOperationMetrics` →
  pass completes with no panic, no records to inspect.
- **Case D (defensive):** op with zero `CreatedAt` → **assert** zero age observations.

### AC-3 — no emission on error paths

`TestFinishOperationNoEmissionOnErrorPaths`:
- `setStatusErroringRepo` → `finishOperation` returns the persist error; **assert** `spy.operationOutcomes`
  is empty.
- `Get`-erroring wrapper (embed `crashTestRepo`, override `Get` to return an error) → `finishOperation`
  returns the reload error; no panic; `spy.operationOutcomes` empty.

### AC-4 — gates stay green

The §6 done-gate passes unchanged. Specifically: `make test`, `make lint`, `make lint-strict`,
`make check-file-sizes` (193→~200, 171→~176, both < 500), `make check-composition-root` (no
`internal/app` change), `make check-integration-compile` (interface untouched).

### AC-5 — no interface or registry drift

`ControlPlaneMetrics` (`control_plane_metrics.go:13-35`), the noop stubs, the
`RegistryControlPlaneMetrics` adapter, and `internal/pkg/metrics/control_plane.go` are byte-identical
before/after. The diff contains only `operation_state.go`, `operation_worker.go`, `callback_audit_test.go`,
and the new `operation_state_test.go`. Registry-level exposition stays covered by
`internal/pkg/metrics/metrics_test.go:147,158`.

## 8. Out of scope

- No `internal/pkg/metrics` changes (registry, exposition names, staleness semantics, adapter).
- No new gates or Makefile changes (unselected direction: metrics-contract gate).
- No regional-freshness metric family (unselected direction).
- No composition-root changes; no audit/proto/migration/config changes.
- No per-pass max-tracking for "oldest open operation" semantics (documented limitation, §3).
