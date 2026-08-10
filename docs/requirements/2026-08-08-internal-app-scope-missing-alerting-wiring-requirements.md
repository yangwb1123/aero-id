# Requirements Specification — Composition-root scope-missing alerting wiring (OnScopeRejected → shared registry), proven with app-level tests

Module: `internal/app` (touches `internal/connector/auditgovernance`, `internal/service`, `internal/pkg/metrics` only for the minimal provenance distinction the pinned contract requires)

Direction (selected, verbatim title): **Prove the composition-root scope-missing alerting wiring (OnScopeRejected → shared registry) with app-level tests**

Status: **evidence verified against the repository** (every cited file/symbol checked; four stale line ranges corrected, one new double-count finding added — substance of the direction confirmed).

---

## 1. Evidence verification

| Cited | Verified state |
|---|---|
| `internal/app/audit.go:104-145` — `buildAuditPublishers` | ⚠️ Line range stale. Function is at **line 135** (`func buildAuditPublishers(cfg *config.Config, db *gorm.DB, controlMetrics service.ControlPlaneMetrics)`). Substance exact: flag-gated (`Enabled`), source-prefix guard, `auditOAuthBindings`, `validateAuditBindingCoverage`, `auditgovernance.NewOAuthTokenSource`, `auditgovernance.NewPublisher`, returns `[]service.AuditPublisher`. |
| `internal/app/audit.go:126-133` — OnScopeRejected closure | ⚠️ Stale. Closure is at **lines 163-165**: `OnScopeRejected: func(string) { controlMetrics.ObserveAuditScopeMissing(strings.TrimSpace(settings.SourcePrefix)) }`. Exact. The C-C no-double-count comment is at lines 160-162. |
| `internal/app/audit.go:106-111` — source-prefix guard | ⚠️ Stale. Guard is at **lines 145-147**: `if strings.TrimSpace(settings.SourcePrefix) != "aero-id"` → `fmt.Errorf("audit governance source_prefix must be %q, got %q", "aero-id", ...)`. Exact. |
| `internal/app/audit.go:60-66` — F-1 guard (drain metrics) | ⚠️ Stale. Comment block at **lines 96-100**, `setter.SetControlPlaneMetrics(controlMetrics)` on the callback service at **line 101** (the ledger setter is line 93). Substance exact: without it the drain's `aero_control_audit_publication_*` counters fall back to `noopControlPlaneMetrics`. |
| `internal/app/audit_integration_test.go:317` — `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain` | ✅ Function at **line 318** (doc comment 310-317). Integration-tagged (`//go:build integration`). Asserts only `aero_control_audit_publication_outcomes_total{status="completed"} 1` — **drain counters only, no scope hook** (no reference to `scope_missing` in the whole file). |
| "no reference in `internal/app/*_test.go`" — `buildAuditPublishers` / hook closure untested at app level | ✅ Confirmed: `grep buildAuditPublishers internal/app` hits only `audit.go:87,135` plus two comment mentions in integration tests (`audit_integration_test.go:478`, `composition_root_integration_test.go:126`). No test calls it or the closure. |
| `internal/pkg/metrics/control_plane.go:110-116` — `ObserveAuditScopeMissing` | ✅ Exact: doc comment 110-114, `func (r *Registry) ObserveAuditScopeMissing(source string)` at 116, increments `auditScopeMissing` counterMap keyed by source. |
| `internal/pkg/metrics/control_plane.go:227` — `aero_control_audit_scope_missing_total` | ✅ Exact: `fmt.Fprintf(w, "aero_control_audit_scope_missing_total{source=%q} %d\n", key, value)`. |
| `internal/service/control_plane_metrics.go:43` — noop fallback | ✅ Exact: line 43 is precisely `func (noopControlPlaneMetrics) ObserveAuditScopeMissing(string) {}`. Noop type at 37-38; `NewRegistryControlPlaneMetrics(nil)` returns the noop at 63-67 — a dropped wiring silently sinks every increment. |
| `internal/connector/auditgovernance/token_invalidation_test.go:525, :644` — hook fires exactly once (connector package only) | ⚠️ Line numbers stale by ~5. `TestEvents401WithScopeBodyIsScopeMissingAlertedOnce` (T-10.2.3) at **:519/:525** — events-401 → `ErrTokenScopeMissing`, hook stays silent (`hookCalls == 0`), status-driven `Invalidate` evicts the cached token. `TestTokenEndpointInvalidScopeFiresHookOnce` (REQ-4) at **:640/:644** — token-endpoint 400+invalid_scope → `Publish` returns `ErrTokenScopeMissing` (**"C-B propagation past authorization"**), hook fires **exactly once** with the tenant ID, nothing cached. |
| `internal/service/callback_audit.go:187` — drain-side classification counter | ✅ Exact: line 187 is `s.metrics.ObserveAuditScopeMissing(strings.TrimSpace(fact.Source))`, fired when `errors.Is(err, auditgovernance.ErrTokenScopeMissing)` after `publisher.Publish` (lines 182-188). `publicationOutcome` settles the row `pending`/`audit.binding_unavailable` via `errAuditTenantUnbound` (lines 15, 148-149). |

**Corrections to the direction:** four line ranges in `internal/app/audit.go` and one each in `audit_integration_test.go` / `token_invalidation_test.go` are stale; every cited symbol and claim is otherwise confirmed exactly.

**New finding (not in the analysis, load-bearing for the acceptance):** the current code **double-counts a token-endpoint scope rejection**. `TestTokenEndpointInvalidScopeFiresHookOnce` proves `Publish` propagates `ErrTokenScopeMissing` past `authorization` (publisher.go:135-136, oauth.go:231-232), so the drain's `publishAuditFact` (callback_audit.go:187) fires the service counter **in addition to** the hook (audit.go:163-165) for the same single rejection. Both land on the identical series: the hook emits `source="aero-id"` (audit.go:164) and `fact.Source` is `"aero-id"` for every ledger fact (audit_fact.go:30, audit_hold.go:163,183, audit_replay.go:49). One token-endpoint rejection therefore yields **2 increments today**; the T-10.2.3 scenario (token-endpoint rejection + events-401 classification) yields **3**, not the pinned 2. The audit.go:160-162 C-C comment is violated by the drain counter, and no test anywhere detects it. R3 below makes the pinned contract real with a minimal provenance distinction; without R3, T-10.2.3 fails red against the shipped code (which is exactly the regression the direction exists to prevent).

---

## 2. Problem statement (verified)

Scope-missing alerting on 401 (`aero_control_audit_scope_missing_total`) is proven today only in two places, neither of which exercises the composition root:

1. Connector package — `TestEvents401WithScopeBodyIsScopeMissingAlertedOnce` and `TestTokenEndpointInvalidScopeFiresHookOnce` count **hook invocations on a local `atomic.Int32`**, not the shared registry family; they cannot see a wiring regression that swaps the registry-backed metrics for `noopControlPlaneMetrics` (control_plane_metrics.go:43).
2. Service counter — `callback_audit.go:187` is unit-tested with `metricSpy` (callback_audit_test.go:122-134) against a failing **fake** publisher; it proves the counter fires, not that the composition root's hook and the counter are the two and only two paths onto one family.

The composition root wires the hook to `controlMetrics` at `internal/app/audit.go:163-165` (inside `buildAuditPublishers`, called from `buildAuditStack` at :87). **No test in `internal/app` calls `buildAuditPublishers` or that closure** (verified by grep). The C-C no-double-count contract is comment-only (audit.go:160-162 vs callback_audit.go:182-188 — and they contradict for the token-endpoint path, per the §1 finding). The only app-level metrics-wiring test (`TestBuildAuditStackWiresControlMetricsIntoCallbackDrain`, audit_integration_test.go:318) covers drain counters only. A wiring regression (dropped `controlMetrics` parameter, accidental `NewRegistryControlPlaneMetrics(nil)`) would silently fall back to the noop sink and every scope-missing runbook alert would match nothing.

---

## 3. Requirements

### R1 — Extract the hook-builder seam (`buildAuditScopeMissingHook`)

Extract the `OnScopeRejected` closure construction in `buildAuditPublishers` (audit.go:163-165) into a directly testable, DB-free function in `internal/app/audit.go`:

```go
// buildAuditScopeMissingHook returns the OnScopeRejected closure wired to the
// shared control-plane registry. The label is the normalized (TrimSpace'd)
// configured source prefix; the connector contract (token_invalidation_test.go)
// guarantees one invocation per token-endpoint scope rejection, so one hook
// call must produce exactly one increment.
func buildAuditScopeMissingHook(controlMetrics service.ControlPlaneMetrics, sourcePrefix string) func(string) {
	return func(string) {
		controlMetrics.ObserveAuditScopeMissing(strings.TrimSpace(sourcePrefix))
	}
}
```

`buildAuditPublishers` must call it with `(controlMetrics, settings.SourcePrefix)` — behavior byte-identical to today, including the `strings.TrimSpace` label normalization (audit.go:164) and the source-prefix guard at audit.go:145-147 (unchanged, still fail-fast before any connector construction). No production behavior change in this step.

### R2 — Extract the binding-coverage seam (injectable, DB-free `buildAuditPublishers`)

`buildAuditPublishers` currently requires a live `*gorm.DB` for `validateAuditBindingCoverage` (audit.go:149-151, UNION query at :155-166), which is the only DB dependency on the unit-testable path. Extract the coverage check behind an interface whose default implementation is the existing UNION query:

```go
// auditBindingCoverage validates that every durable tenant with pending
// audit state has a governance binding. The gorm implementation issues the
// UNION query; tests inject a fake so buildAuditPublishers is unit-testable
// without a database.
type auditBindingCoverage interface {
	CheckAuditBindingCoverage(bindings map[string]config.AuditGovernanceBindingConfig) error
}
```

- `buildAuditPublishers` accepts the seam (exact signature is the design's choice — e.g. a parameter defaulted by `buildAuditStack` to the gorm-backed implementation wrapping its `gdb`); the production call path through `buildAuditStack` (audit.go:87) must keep issuing the real UNION query and its exact error strings (`audit governance binding coverage check failed: ...`, `audit governance bindings do not cover durable tenant state`).
- Constraint: this is the **same seam** the sibling direction (analysis direction 2, acceptance T-5) names "extracted interface defaulting to the real UNION query". Extract it once, shaped so both directions consume it; do not fork a second coverage mechanism.
- Keep the seam and the default implementation in `internal/app/audit.go` (277 lines today; headroom to 500) or a sibling file — design choice.

### R3 — C-C contract: exactly one increment per scope-missing condition (the pinned observable)

The observable contract the acceptance pins:

> For one scope-missing condition the family `aero_control_audit_scope_missing_total` increments **exactly once**, on the single label set `{source="aero-id"}`. A token-endpoint rejection increments **only via the hook** (audit.go:163-165); an events-endpoint classification increments **only via the service counter** (callback_audit.go:187). Combined (one rejection + one classification): exactly **2** increments.

The current code violates this for the token-endpoint path (hook +1 and drain counter +1 per rejection, §1 finding). The minimal change, bounded to the two classification seams:

1. `internal/connector/auditgovernance` distinguishes token-endpoint-originated scope-missing from events-endpoint-originated in the error `Publish` returns — e.g. an exported sentinel `ErrTokenEndpointScopeMissing` wrapping `ErrTokenScopeMissing` (types.go:24), emitted at the **two token-source classification sites** (oauth.go:231-232 — 400-gate; oauth.go:241-242 — scope-claim validation), and **never** at the events-401 site (publisher.go:112).
2. `callback_audit.go:187` skips the counter when the error carries the token-endpoint marker; the row settlement (`pending`/`audit.binding_unavailable` via `errAuditTenantUnbound`, callback_audit.go:148-149, 182-188) is unchanged for both paths.

Hard constraints on the mechanism (the acceptance is the contract; the marker shape is the design's choice):

- `errors.Is(err, ErrTokenScopeMissing)` must remain **true for both paths** — the connector tests at token_invalidation_test.go:525/:644 assert it, and the drain settlement depends on it.
- The hook must still fire **exactly once** per token-endpoint classification (singleflight-coalesced, oauth.go:313-322) and **never** for events-endpoint classifications (connector test pins `hookCalls == 0`).
- The `{source="aero-id"}` label must be the only label set ever emitted for the family from either path (fact.Source is `"aero-id"` for all ledger facts; hook uses the normalized prefix).
- No new metric family, no new counter, no config surface, no migration, no change to retry/backoff/lease/MaxAttempts, no change to `publicationOutcome` terminality (a scope-missing row stays pending at `audit.binding_unavailable`, never dead_letter).

### R4 — App-level tests proving the wiring (acceptance checks, preserved verbatim)

All three supplied acceptance checks are preserved; each is decomposed into deterministic, runnable sub-checks. Unit tests go in a new unit-gated file in `internal/app` (e.g. `audit_scope_missing_test.go`, no build tag — `make test` must run them); the integration test extends `internal/app/audit_integration_test.go` (`//go:build integration`).

#### T-10.1 (unit) — "unit test of an extracted hook-builder/binding-coverage seam asserting one token-endpoint scope rejection increments `aero_control_audit_scope_missing_total{source="aero-id"}` exactly once (not zero, not twice)"

- **T-10.1a — direct seam, exactly-once semantics:** fresh `metrics.NewRegistry()` + `service.NewRegistryControlPlaneMetrics(reg)`; `hook := buildAuditScopeMissingHook(metrics, "aero-id")`; invoke once → `reg.WriteText` exposition contains exactly `aero_control_audit_scope_missing_total{source="aero-id"} 1` and no other `scope_missing` series; invoke a second time → `... 2` (per invocation exactly one increment — **not zero, not twice**).
- **T-10.1b — label normalization pin:** hook built with `" aero-id "` (whitespace-padded prefix) still emits `{source="aero-id"}` — the `TrimSpace` contract of audit.go:164 is preserved through the seam; a `noopControlPlaneMetrics` argument leaves the registry at zero (the noop fallback is explicit, never the hook's sink).
- **T-10.1c — wiring through `buildAuditPublishers`, no DB:** fake `auditBindingCoverage` returning nil; httptest server whose `/token` answers `400 {"error":"invalid_scope"}`; config with `Enabled: true, SourcePrefix: "aero-id", AllowInsecureLoopback: true`, one binding (`TokenURL: server.URL+"/token"`, secret env set via `t.Setenv`, mirroring `auditGovernanceTestConfig` in audit_integration_test.go:456-478); registry-backed metrics. Call `buildAuditPublishers`, drive the returned publisher's `Publish` with a valid `domain.AuditFact` (shape of `scopeFact`, token_invalidation_test.go:30-36) → exposition shows exactly `aero_control_audit_scope_missing_total{source="aero-id"} 1` (**not zero** — proves the provided `controlMetrics` reached the hook, i.e. non-noop wiring); a second `Publish` (second rejection) → `... 2` (**not twice per rejection** — no per-publish double increment at the seam).

#### T-10.2.3 (integration) — "integration test asserting a token-endpoint rejection (hook path) plus an events-401 classification (drain path) yield exactly 2 increments on the same registry family — pins the C-C no-double-count contract"

Integration-tagged, modeled on the existing fixture (`auditGovernanceFixture` / `governanceTestEndpoint`, audit_integration_test.go:365-497) with a **stateful endpoint** and stop/restart drain discipline for determinism:

1. **Phase A — token-endpoint rejection (hook path):** endpoint serves `/token → 400 {"error":"invalid_scope"}` (`/api/v1/events` unreachable while the token is rejected). Build the real stack via `buildAuditStack` (testpg + migrations + registry-backed metrics), `Record` one fact, start the drain, poll the publication row to `pending`/`audit.binding_unavailable` with `attempt = 1`, **stop the drain immediately** (bounded first backoff window — `RetryBase: 1s` in the fixture config — makes a second attempt impossible once stopped; the `attempt = 1` assertion makes any extra attempt a loud failure). Assert the registry exposition contains exactly `aero_control_audit_scope_missing_total{source="aero-id"} 1` and nothing else in that family.
2. **Phase B — events-401 classification (drain path):** flip the endpoint to `/token → 200` valid token and `/api/v1/events → 401 {"error":"invalid_scope"}`. Restart the drain; the row's next claim fetches a token successfully (no hook fire), the events POST classifies scope-missing, the row returns to `pending`/`audit.binding_unavailable` with `attempt = 2`. Stop the drain; assert the family total is **exactly 2** with the single label set `{source="aero-id"}`; row still `pending` at `audit.binding_unavailable` — never `completed`, never `dead_letter`.
3. **Negative framing baked in:** if the drain counter double-counted the token-endpoint path (shipped code today), Phase A alone would show 2 and the final total 3 → the test fails red. If the hook were dropped/unwired, Phase A shows 0 → fails red. The test is the first app-level detector for both regressions.

#### Wiring assertion (third acceptance, verbatim) — "wiring assertion that buildAuditStack installs non-noop control metrics for the hook path, mirroring the F-1 guard already applied to drain metrics (internal/app/audit.go:60-66)"

- **Unit leg:** T-10.1c **is** the non-noop wiring assertion at the seam — a regression that swaps the `controlMetrics` parameter for `NewRegistryControlPlaneMetrics(nil)`/noop leaves the family at 0 and fails T-10.1c.
- **Integration leg (F-1 mirror):** T-10.2.3 Phase A drives the **real `buildAuditStack`** (audit.go:59, hook path wired at :87 → :163-165) with a registry-backed metrics and asserts the family increments — exactly the shape of `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain` (audit_integration_test.go:318), which proves the same for the drain counters via `SetControlPlaneMetrics` (audit.go:101). The mirror is complete only when both tests exist: drain observability and hook observability each proven through the composition root.

### Acceptance mapping (supplied checks → requirements)

| Supplied acceptance check | Requirement | Runs under |
|---|---|---|
| T-10.1: unit test of an extracted hook-builder/binding-coverage seam; one token-endpoint scope rejection → `aero_control_audit_scope_missing_total{source="aero-id"}` exactly once (not zero, not twice) | R1 (seam), R2 (coverage seam), T-10.1a/b/c | `make test` (no Docker) |
| T-10.2.3: integration test; token-endpoint rejection (hook path) + events-401 classification (drain path) → exactly 2 increments on the same registry family — pins the C-C no-double-count contract | R3 (provenance distinction), T-10.2.3 | `make test-integration` |
| T-10.1: wiring assertion that buildAuditStack installs non-noop control metrics for the hook path, mirroring the F-1 guard for drain metrics | T-10.1c (unit leg) + T-10.2.3 Phase A (integration leg, F-1 mirror) | `make test` + `make test-integration` |

---

## 4. Done-gate

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-composition-root && make check-integration-compile && make build
```

plus `make test-integration` (Postgres via testcontainers) for the new integration test. `internal/app/audit.go` grows by the two seams (≤ 500-line file gate: 277 lines today); connector/service deltas are bounded to the provenance marker and the one skip in `callback_audit.go:187`.

## 5. Out of scope (explicit)

- No change to the connector's classification semantics (`ErrTokenScopeMissing` still classifies both paths; settlement to `pending`/`audit.binding_unavailable` unchanged; hook-once and hook-silent-on-events-401 connector contracts untouched).
- No change to drain retry/backoff/lease/`MaxAttempts`/`publicationOutcome` terminality; no new metric family or counter; no config surface; no migration; no change to the ledger fact `Source` value.
- No HTTP/gRPC surface, no runbook/alert definitions, no `cmd/*` changes.
- The sibling direction's app-level invariants (purge-pause knob pass-through, drain-only delivery path, activation gate at the app seam — analysis direction 2) are separate; only the shared binding-coverage seam extraction is in scope here, shaped for both.

## 6. Known limitations / decisions for the design stage

- **Provenance marker shape (R3):** exported sentinel `ErrTokenEndpointScopeMissing` wrapping `ErrTokenScopeMissing` (via `fmt.Errorf("%w: %w", ...)` or `errors.Join`) vs. a typed wrapper; marker applied at the token-source sites (oauth.go:231-232, 241-242) vs. at the authorization propagation site (publisher.go:135-136). All shapes must keep `errors.Is(err, ErrTokenScopeMissing) == true` for both paths and keep the two connector tests green unchanged.
- **T-10.2.3 determinism:** the stop-drain-on-row-state discipline bounds attempts; `attempt == 1` / `attempt == 2` pins single-fire per phase. If the drain's retryDelay ever shrinks below the poll interval, the phase-A assertion of exactly 1 fails loudly rather than silently — acceptable (noisy failure, no false green).
- **Seam signature (R2):** parameter injection vs. a constructor default is the design's call; the production path through `buildAuditStack` must issue the real UNION query with the existing error strings, and the sibling direction's T-5 must be able to consume the same interface.
- **Implementation order:** R1/R2 first (pure extraction, `make test` green), then R3 (provenance), then the tests; T-10.2.3 must be red against the pre-R3 code (3 increments) and green after (2) — run it once pre-fix to confirm the detector fires, or document the expected red in the change report.
