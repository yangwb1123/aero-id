# Design — Canonicalize the bounded-concurrency primitive: adopt `internal/pkg/bulkhead` in the connector (retire the private semaphore)

| | |
|---|---|
| Direction | **Canonicalize the bounded-concurrency primitive: retire the connector's private semaphore and adopt `internal/pkg/bulkhead`** (analysis `docs/auto/analyses/internal-pkg-bulkhead-942e0fa7.json`, item 1 of 3) |
| Requirements | `docs/auto/runs/canonicalize-the-bounded-concurrency-primitive-r-5fe8573e/artifacts/requirements-10762e10/requirements.md` (spec verified below) |
| Scope | Strictly item 1. Analysis items 2 (`Limit()`/cap in module) and 3 (high-contention `Run` test) are **not** in scope. |
| Verified at | HEAD of the working tree, 2026-08-16 |
| Verdict | **Evidence verified: all 13 cited claims hold against the current tree.** Two trivial line drifts confirmed (http_policy.go `begin` is at :11 not :14; `new.go` calls `buildSourceConnectors` at :114 not :121 — substance identical). One evidence-internal correction (rule label (a)→(d)) is itself confirmed accurate by the `check-architecture.sh` header. Design below is additive and backwards-compatible; no shipped API, config key, migration, or proto changes. |

## 1. Independent verification of the evidence

The requirements spec's 13 evidence claims were re-checked against the working tree at HEAD, not taken on trust. All hold.

| # | Claim | Independent check (current tree) | Verdict |
|---|---|---|---|
| E1 | `bulkhead.go` `Acquire`/`Release`; bare `Release()` is `<-b.sem`, misuse wedges | `internal/pkg/bulkhead/bulkhead.go` — `New` :14-19 (`limit <= 0 → 1`), `Acquire` :22-28 (`select` on `b.sem <-` vs `ctx.Done()`), `Release` :31-32 (`<-b.sem`, no idempotency/guard), `Run` :36-47. File is 49 lines | Confirmed |
| E2 | Connector private `sem chan struct{}`, default 32, `acquire` byte-identical to `Acquire` | `internal/connector/http_summary.go` — `sem chan struct{}` field :46; `if cfg.MaxConcurrency <= 0 { cfg.MaxConcurrency = 32 }` :65-66; `sem: make(chan struct{}, cfg.MaxConcurrency)` :82; `acquire` :305-312 (select body byte-identical) | Confirmed |
| E3 | `begin()` returns release token; maps acquire error to `RemoteError{Kind: ErrorUnavailable, Code: "source.bulkhead", Temporary: true}` | `internal/connector/http_policy.go` — `begin` at :11 (spec cites :14; 3-line drift, substance identical); breaker check :13-20; error mapping :22 exactly as cited; success returns `func() { <-c.sem }` :27 | Confirmed (line drift) |
| E4 | Token used at Summary/Health/Erase/OperationStatus | `begin()` call sites: http_summary.go :137 (Summary), :175 (Health), :212 (Erase); http_operation.go :21 (OperationStatus). All four `defer release()` | Confirmed (exact) |
| E5 | Sole 64 cap lives outside the module | `internal/service/callback.go` — `if cfg.WorkerConcurrency > 64 { cfg.WorkerConcurrency = 64 }` :86-87; `bulkhead.New(cfg.WorkerConcurrency)` :91 | Confirmed |
| E6 | Drain sites use bare `Acquire`/`Release` | `callback_audit.go` `drainPublications` :77 (`Acquire`), `break` on error, `defer s.bulkhead.Release()` :83 in goroutine; `callback_worker.go` `drain` :95, `break`, :101 | Confirmed |
| E7 | Connector wiring via composition root | `internal/app/connectors.go` :14-43 `buildSourceConnectors`; `internal/app/new.go:114` calls it (spec cites :121 — drift). Wrappers `internal/connector/{snaplink,aeroim,aerovault}/<name>.go` all delegate to `NewJSONSummaryConnector` | Confirmed (line drift) |
| E8 | Per-source fan-out in aggregation | `internal/service/aggregation.go` `refresh` :99-118 — one goroutine per source calling `client.Summary` :116 | Confirmed |
| E9 | Operation worker exercises the connector | `operation_worker.go:105` ticker goroutine → `processRunnable` :112; `operation_runner.go` `OperationStatus` :92, `Erase` :122, `Summary` :141 — all under connector `begin()` | Confirmed (exact) |
| E10 | Existing drain saturation test | `internal/service/callback_delivery_test.go:316` (`TestCallbackDelivery_ConcurrentDrainRespectsBulkhead`, doc :313) — max in-flight ≤ 2 via `slowTransport` | Confirmed |
| E11 | `internal/pkg` already importable by connector; rule (d) is operative | `http_summary.go:16` imports `internal/pkg/metrics`. `scripts/check-architecture.sh` header: rule (a) is the dead-tree guard; rule (d) bans only `internal/service`/`internal/repository` from `internal/{connector,event,outbox,region,sso}`; `internal/pkg/*` is not restricted | Confirmed (rule-label correction validated) |
| E12 | Connector default 32 in config; validation `>= 0` | `configs/config.yaml:13,29,45` `max_concurrency: 32`; `internal/config/config.go:173` `MaxConcurrency int`; `internal/config/validate.go:449` `source.MaxConcurrency >= 0` | Confirmed |
| E13 | No connector concurrency test exists today | All `internal/connector/*_test.go` construct via `newTestConnector` (fault_injection_test.go:173) with `MaxConcurrency` unset (→ 32 default); grep for `MaxConcurrency` in connector tests: **0 matches**. `internal/pkg/bulkhead/bulkhead_test.go` has exactly 4 tests | Confirmed — new regression tests are genuinely new |

**Independent spot checks (this session):**

- `begin`'s failure path is the only producer of `source.bulkhead`; `RemoteError` fields (`Kind/Code/Temporary/Cause`) and `Unwrap` at `internal/connector/connector.go:110-128`; `ErrorUnavailable` :102. Mapping is byte-identical to the spec's R3 quote.
- `newTestConnector` sets `cfg.Client = http.DefaultClient`, `SummaryPath = "/summary"`, default `MaxBodyBytes` — so AC-2 tests get real HTTP against `httptest` servers with no production wiring.
- `slowTransport` (`callback_delivery_test.go:345`) provides the mutex-guarded in-flight counter pattern AC-2 references.
- **Extra finding (affects R2.4):** `http_summary.go` imports `sync` for the `breakerMu sync.Mutex` struct field (:49), *not* for the semaphore. The `sync` import must **not** be removed when `acquire` is deleted; only the `acquire` method, the `sem` field, and its constructor initializer are deleted.
- **Extra finding (affects R2.2):** `NewJSONSummaryConnector` already mutates `cfg.MaxConcurrency` to 32 *before* creating the semaphore (:65-66), so today the bulkhead-equivalent always receives an already-clamped value. The explicit `clampedSummaryConcurrency` clamp is therefore defense-in-depth against future edits reordering or deleting the `<= 0 → 32` mutation (which would silently fall into `bulkhead.New(0)` serial), plus it gives AC-3 a unit-testable surface.

## 2. Problem statement

Two live, load-bearing bounded-concurrency implementations coexist with divergent contracts:

1. `internal/pkg/bulkhead` — `New`/`Acquire`/`Release`/`Run`; `New(0)` → 1 (serial); release is a bare `Release()` (`<-b.sem`); the only cap (64) lives at the caller (`callback.go:86-87`). Guards callback delivery fan-out and audit publication drain.
2. `JSONSummaryConnector` — private `sem chan struct{}` with a byte-identical acquire; `<= 0 → 32` default; no cap; token-safe release (`begin()` returns `func() { <-c.sem }`). Guards per-source outbound HTTP fan-out on `Summary`/`Health`/`Erase`/`OperationStatus` from the composition root.

They disagree on every contract axis (serial vs 32 default; caller cap vs no cap; bare release vs token release). The connector's token pattern is exactly the API the module lacks. The duplication forces every future contract fix to be applied twice, and future bulkhead users (per-source pools; AGENTS.md "Apply bulkheads per source and workload class") silently inherit one of two divergent semantics. The module's bare `Release()` is a misuse hazard: `defer b.Release()` before the `Acquire` error check blocks forever on the empty channel (slot never acquired → `<-b.sem` never completes) — the connector avoids this only by construction.

## 3. Goals and non-goals

**Goals**

- **G1.** Add a token-release API to `internal/pkg/bulkhead` making it the single canonical bounded-concurrency primitive.
- **G2.** Retire the connector's private `sem chan struct{}` + `acquire`; express all four connector surfaces through `bulkhead.AcquireToken`, preserving byte-identical error mapping (`source.bulkhead`) and the `<= 0 → 32` default.
- **G3.** Preserve every existing contract: `New/Acquire/Release/Run`, the two drain call sites, the connector public API, and all gates green.

**Non-goals (scope pins — do not implement)**

- **NG1.** No `Limit()` accessor, no cap enforcement inside `New`, no cap-contract doc change, no high-contention `Run` test (analysis items 2 and 3). `New(0) → 1` and the caller-owned 64 cap stay as shipped.
- **NG2.** No migration of the two drain sites to the token API (audit-critical; expanding the change set doesn't advance the direction). A follow-up may adopt tokens there under its own direction.
- **NG3.** No change to `HTTPConfig`, connector defaults beyond the internal semaphore swap, config keys, migrations, protos, or the operation/aggregation services.
- **NG4.** No new package, no new dependency, no change to `MaxIdleConns` transport sizing (`newSourceHTTPClient` keeps using `cfg.MaxConcurrency`).
- **NG5.** No global 64 cap for the connector: it accepts any `>= 0` concurrency with no cap today; adding one is direction-3 territory. Only `<= 0 → 32` is preserved.

## 4. Design

### 4.1 R1 — Token API on `internal/pkg/bulkhead` (additive)

Add to `internal/pkg/bulkhead/bulkhead.go` (49 → ~75 lines, ≪ 500 gate):

```go
// AcquireToken blocks until a slot is free or ctx is done. On success it
// returns a release token; the caller must invoke it exactly once to return
// the slot. On ctx cancellation it returns the context error and does NOT
// consume a slot (no token is returned). The token is safe to call
// multiple times (no-op after the first release).
func (b *Bulkhead) AcquireToken(ctx context.Context) (func(), error) {
	select {
	case b.sem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-b.sem }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
```

Properties, each pinned by an acceptance test (AC-1):

- **R1.1** Release-without-acquire is structurally impossible: a token exists only after a successful acquire (nil token on failure).
- **R1.2** Double-release is a no-op: `sync.Once`-guarded `<-b.sem`; no panic, no block, exactly one slot returned.
- **R1.3** Failed acquire consumes no slot: `select` on `ctx.Done()` never sends to `b.sem`; a fresh-context acquire succeeds immediately with no intervening release.
- **R1.4** `Acquire`, `Release`, `Run`, `New` keep exact signatures, semantics, and doc comments. `Run` may be left as-is (it is; re-expressing it via `AcquireToken` is optional and observably identical — keep the diff minimal and leave `Run` untouched).
- **R1.5** File-size gate: file stays ≪ 500 lines.

**Decision (pinned):** the direction offers either `AcquireToken(ctx) (func(), error)` or changing `Acquire` to return a release func. Changing `Acquire`'s signature would break both live drain call sites and shipped tests in an audit-critical path (scope creep, NG2). Additive `AcquireToken` is chosen; it is exactly the shape `begin()` already uses, so the connector migration is a straight swap.

### 4.2 R2 — Connector migration: retire the private semaphore

In `internal/connector`:

- **R2.1** `JSONSummaryConnector.sem chan struct{}` (http_summary.go:46) → `bulkhead *bulkhead.Bulkhead`.
- **R2.2** Constructor builds the bulkhead from an explicitly clamped limit:

```go
// clampedSummaryConcurrency preserves the connector's historical <= 0 -> 32
// default. It deliberately prevents bulkhead.New(0) (serial, by contract)
// from silently serializing the connector.
func clampedSummaryConcurrency(limit int) int {
	if limit <= 0 {
		return 32
	}
	return limit
}
```

Constructor: `bulkhead: bulkhead.New(clampedSummaryConcurrency(cfg.MaxConcurrency))`. Values `> 0` pass through unchanged (no cap — NG5). The clamp lives in `internal/connector` (package-level func, unit-testable; http_summary.go shrinks from 328 → ~320 lines after deleting `acquire`). Note the constructor's existing `<= 0 → 32` mutation (:65-66) stays and makes the clamp technically redundant today — the clamp is the testable, edit-proof form of that invariant (see FM-2).

- **R2.3** `begin()` (http_policy.go:11-27) replaces `c.acquire(ctx)` with `c.bulkhead.AcquireToken(ctx)`. Error mapping stays byte-identical: `&RemoteError{Source: c.source, Kind: ErrorUnavailable, Code: "source.bulkhead", Temporary: true, Cause: err}`. The returned token **is** the release func, so `begin`'s signature `(func(), error)` and all four call sites (Summary :137, Health :175, Erase :212, OperationStatus :21) are unchanged.
- **R2.4** Delete `acquire` (http_summary.go:305-312), the `sem` field (:46), and the constructor initializer (:82). **Do not remove the `sync` import** — `breakerMu sync.Mutex` (:49) still needs it (verified; this is the concrete resolution of the spec's "check before removing" caveat).
- **R2.5** Behavior parity: token release is the module's `AcquireToken` token — identical slot accounting, now with misuse protection (no more bare `<-c.sem` closure).

### 4.3 R3 — Error contract preserved

`source.bulkhead` with `Kind: ErrorUnavailable`, `Temporary: true`, and `Cause` set to the context error on any acquire failure, identically to today (http_policy.go:22). No new error codes, no leakage of internal state.

### 4.4 R4 — Defaults and compatibility preserved

- Connector `<= 0 → 32` preserved through the explicit clamp (R2.2).
- `bulkhead.New(0) → 1` serial contract unchanged (R1.4).
- Drain sites unchanged (NG2); callback 64 cap unchanged (NG1).

## 5. Compatibility constraints (explicit contract surface)

| Surface | Constraint | Enforced by |
|---|---|---|
| `bulkhead.New/Acquire/Release/Run` | signatures, semantics, doc comments unchanged | AC-1 (existing 4 tests unchanged) + R1.4 |
| `bulkhead.New(0) → 1` serial | unchanged | `TestBulkhead_NewDefaultsToSerial` (existing) |
| Callback 64 cap | stays at `callback.go:86-87`, outside the module | NG1; no code touched |
| Drain sites (`callback_worker.go`, `callback_audit.go`) | byte-identical | NG2; no code touched |
| Connector public API | `NewJSONSummaryConnector`, `Connector` interface, `HTTPConfig`, error codes (`source.bulkhead`, `source.request_failed`, …), `RemoteError` shape — all unchanged | R2.3; existing contract/fault-injection suites |
| Connector default | `<= 0 → 32`, no cap | R2.2 + `TestConnector_DefaultConcurrencyClamp` |
| Config | `max_concurrency` keys and `>= 0` validation unchanged | NG3 |
| Migrations / protos | none | NG3 |
| Transport sizing | `newSourceHTTPClient` continues to use `cfg.MaxConcurrency` for `MaxIdleConns` | NG4 |
| Imports / architecture gate | connector gains `internal/pkg/bulkhead`; rule (d) permits `internal/pkg/*` (precedent: `internal/pkg/metrics`) | `make check-architecture` |

## 6. Failure modes and mitigations

| # | Failure mode | Trigger | Mitigation |
|---|---|---|---|
| FM-1 | Token misuse reintroduces wedge (double-release on a channel-receive token would decrement twice) | A future caller calls the token twice | `sync.Once`-guarded token is a no-op on double release (R1.2, `TestBulkhead_AcquireTokenDoubleReleaseNoOp`). The connector's old `func() { <-c.sem }` closure is deleted, not copied |
| FM-2 | Connector silently serializes after clamp removal | Future edit deletes `clampedSummaryConcurrency` and passes raw `cfg.MaxConcurrency`, which `bulkhead.New` maps `<= 0 → 1` (serial) — 32x behavior change with zero error | Explicit clamp (R2.2) + `TestConnector_DefaultConcurrencyClamp` pins `0/-5 → 32`, `1/64/128` pass-through |
| FM-3 | Error-mapping drift on `source.bulkhead` | Refactor reorders `begin` failure handling; `Temporary` or `Cause` lost | R3 literal preserved; `TestConnector_CancelledAcquireMapsToSourceBulkhead` asserts `Kind`/`Code`/`Temporary`/`Cause == context.Canceled` |
| FM-4 | Release-without-acquire hangs (module `Release()` hazard) | Caller does `defer Release()` before checking `Acquire` error | Structurally impossible with tokens (R1.1); nil-token assertion pins it |
| FM-5 | Gate violation from new import | Misread of architecture rules | Rule (d) bans only `internal/service`/`internal/repository`; `internal/pkg/*` already imported (`metrics`); `make check-architecture` green in AC-4 |
| FM-6 | Slot held across retry loop starves siblings | `doHTTP` retries inside a held slot (existing behavior) | **Unchanged** — slot accounting identical to today; out of scope. Documented, not altered |
| FM-7 | Leaked slot if caller forgets `defer release()` | Programming error in a future caller of `begin`/`AcquireToken` | Token doc contract "invoke exactly once"; all four current call sites already `defer release()` (verified E4) |

## 7. Migration steps (ordered; each step compiles and keeps gates green)

1. **`internal/pkg/bulkhead/bulkhead.go`** — add `AcquireToken` (add `sync` to imports). No other change; `Run` untouched.
2. **`internal/pkg/bulkhead/bulkhead_test.go`** — add the four AC-1 tests. Run `go test ./internal/pkg/bulkhead/`.
3. **`internal/connector/http_summary.go`** — add `clampedSummaryConcurrency`; swap `sem` field → `bulkhead`; constructor initializer → `bulkhead.New(clampedSummaryConcurrency(cfg.MaxConcurrency))`; delete `acquire`. **Keep `sync` import** (`breakerMu`). Add import `github.com/aero/aero-id/internal/pkg/bulkhead`.
4. **`internal/connector/http_policy.go`** — `begin`: `token, err := c.bulkhead.AcquireToken(ctx)`; error path returns the identical `RemoteError` literal; success `return token, nil`.
5. **`internal/connector/` tests** — add `TestConnector_SummarySerializesAtMaxConcurrencyOne`, `TestConnector_CancelledAcquireMapsToSourceBulkhead`, `TestConnector_DefaultConcurrencyClamp`. Run full `go test ./internal/connector/`.
6. **Gate sweep (AC-4)** — `make test && make lint && make lint-strict && make check-file-sizes && make check-architecture && make build`; then the unaffected full done-gate.

**Rollback:** reverting steps 1-5 restores the private semaphore; there is no data migration, config change, or wire-format change, so rollback is a source revert + rebuild.

## 8. Testable acceptance mapping

| Acceptance | Test (new unless noted) | Assertions | Gate |
|---|---|---|---|
| AC-1a token API | `TestBulkhead_AcquireTokenReturnsReleaseFunc` (bulkhead_test.go) | `release != nil`, `err == nil`; after `release()` a blocked acquire succeeds | `go test ./internal/pkg/bulkhead/` |
| AC-1b double-release no-op | `TestBulkhead_AcquireTokenDoubleReleaseNoOp` | `release()` twice: no panic, no block; fresh acquire succeeds immediately (exactly one slot returned) | same |
| AC-1c release-without-acquire impossible | `TestBulkhead_AcquireTokenCancelledNoToken` | sole slot held + cancelled/deadline ctx → `token == nil`, `err != nil` (`context.DeadlineExceeded`) | same |
| AC-1d cancel consumes no slot | `TestBulkhead_AcquireTokenCancelledNoSlotConsumed` | failed acquire → `b.Acquire(context.Background())` succeeds with no intervening release | same |
| AC-1e existing contract intact | `TestBulkhead_LimitsConcurrency`, `TestBulkhead_AcquireHonorsContext`, `TestBulkhead_NewDefaultsToSerial`, `TestBulkhead_Run` (unchanged) | green | same |
| AC-2a serialization parity | `TestConnector_SummarySerializesAtMaxConcurrencyOne` (connector; `newTestConnector` + `httptest` server with mutex-guarded in-flight counter, `MaxConcurrency: 1, MaxAttempts: 1`) | two concurrent `Summary` calls → max in-flight == 1, both succeed | `go test ./internal/connector/` |
| AC-2b cancelled acquire maps identically | `TestConnector_CancelledAcquireMapsToSourceBulkhead` (`MaxConcurrency: 1`; slot held by blocked first `Summary`; second `Summary` with pre-cancelled ctx) | `errors.As` → `Kind == ErrorUnavailable`, `Code == "source.bulkhead"`, `Temporary == true`, `errors.Is(Cause, context.Canceled)` | same |
| AC-2c shared path + existing surfaces | `TestConnector_PropagatesBearerTokenOnEverySurface` (contract_test.go:24), `TestJSONSummaryConnectorEraseUsesIdempotency`, full connector suite | green through the new bulkhead | same |
| AC-3 clamp pinned | `TestConnector_DefaultConcurrencyClamp` | `clampedSummaryConcurrency(0) == 32`, `(-5) == 32`, `(1) == 1`, `(64) == 64`, `(128) == 128` (no cap — NG5). Optional behavioral cross-check: `MaxConcurrency: 0` concurrent calls do **not** serialize | same |
| AC-4 gates green | existing + new | `make test` (incl. `TestCallbackDelivery_ConcurrentDrainRespectsBulkhead` — untouched drains), `make lint`, `make lint-strict`, `make check-file-sizes` (bulkhead.go ~75, http_summary.go ~320, both ≪ 500), `make check-architecture`, `make build` | done-gate subset + unaffected gates (`check-audit-swallow`, `check-composition-root`, `check-migration-set`, `check-proto-sync`) |

## 9. Risks

| Risk | Mitigation |
|---|---|
| Token misuse reintroduces wedge | FM-1: `sync.Once` token; old closure deleted, not copied |
| Connector accidentally hits `New(0)` serial after clamp removal | FM-2: explicit clamp + `TestConnector_DefaultConcurrencyClamp` |
| Error-mapping drift on `source.bulkhead` | R3 + AC-2b asserting Kind/Code/Temporary/Cause |
| Gate violation via new import (rule (d)) | Precedent exists (`internal/pkg/metrics`); `make check-architecture` in AC-4 |
| Line-number drift in cited evidence | Verified at HEAD; corrected in §1 (E3, E7); substance identical |
| Scope creep into drains or `Limit()`/cap work | Non-goals NG1-NG5 are explicit and gated by review |

## 10. References

- `internal/pkg/bulkhead/bulkhead.go` (49 lines) and `bulkhead_test.go` (4 tests)
- `internal/connector/http_summary.go` (sem :46, default 32 :65-66, sem init :82, begin call sites :137/:175/:212, acquire :305-312), `http_policy.go` (begin :11-27; `source.bulkhead` mapping :22; token :27), `http_operation.go` (begin :21), `connector.go` (`RemoteError` :110-128, `ErrorUnavailable` :102)
- `internal/service/callback.go` (64 cap :86-87, `bulkhead.New` :91), `callback_worker.go` (:95-101), `callback_audit.go` (:77-83) — untouched drain sites
- `internal/app/connectors.go:14-43`, `internal/app/new.go:114`, `internal/service/aggregation.go:99-118`, `internal/service/operation_worker.go:105`, `internal/service/operation_runner.go:84-141`
- `internal/service/callback_delivery_test.go:316` (existing saturation test), `internal/connector/fault_injection_test.go:173` (`newTestConnector`), `internal/connector/contract_test.go:24`
- `scripts/check-architecture.sh` rules (a)-(f); rule (d) governs connector imports; `internal/pkg/*` permitted
- `configs/config.yaml:13,29,45` (`max_concurrency: 32`); `internal/config/config.go:173`; `internal/config/validate.go:449`
- Context only: `docs/designs/2026-08-07-internal-pkg-bulkhead-drain-sole-delivery-design.md` (documents the callback bulkhead "max 64" and the drain sites' contract)
- Analysis source: `docs/auto/analyses/internal-pkg-bulkhead-942e0fa7.json` (items 2 and 3 explicitly out of scope)

**Done gate (this direction):** `make test && make lint && make lint-strict && make check-file-sizes && make check-architecture && make build`, plus the unaffected full done-gate from AGENTS.md.
