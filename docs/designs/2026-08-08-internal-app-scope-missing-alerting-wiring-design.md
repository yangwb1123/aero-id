# Design — Composition-root scope-missing alerting wiring (OnScopeRejected → shared registry), proven with app-level tests

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-08-internal-app-scope-missing-alerting-wiring-requirements.md` (R1–R4; supplied acceptances T-10.1, T-10.2.3, wiring assertion — preserved verbatim) |
| Module | `internal/app` (+ bounded deltas in `internal/connector/auditgovernance` and `internal/service`) |
| Verified at | Working tree on HEAD `b005409` (uncommitted sibling-batch work present: activation gate `validateAuditGovernanceGate` at `internal/app/audit.go:33-57`, anchor-derivation e2e additions in `audit_integration_test.go` — all line citations below are against this tree) |
| Stage | design (aggregating the requirements spec; nothing landed for this direction) |

---

## 0. Evidence verification — untrusted claims → verified facts

Every claim in the supplied evidence and in the requirements spec was re-checked against the working
tree with `grep`/`sed`/`read`. **All substance confirmed.** Corrections found during verification:

1. The spec's `internal/domain/audit_fact.go:30` citation for the `Source: "aero-id"` default is the
   wrong file — the domain `AuditFact` lives in `internal/domain/audit_ledger.go:5` and the default is
   applied in **`internal/service/audit_ledger.go:121-122`** (`if input.Source == "" { input.Source = "aero-id" }`).
   The substance (every ledger fact carries `Source: "aero-id"` unless explicitly overridden) holds;
   the hold/replay fact builders confirm it (`audit_hold.go:163,183`, `audit_replay.go:49`).
2. `token_invalidation_test.go` — the two connector tests are at **:519** (`TestEvents401WithScopeBodyIsScopeMissingAlertedOnce`)
   and **:643** (`TestTokenEndpointInvalidScopeFiresHookOnce`), not :525/:644 (evidence) or :640/:644
   (spec). Contract assertions (hook-once at :679-680, hook-silent at :582-583) are exact.

| Evidence claim | Verification (working tree, 2026-08-08) |
|---|---|
| `internal/app/audit.go:135` — `buildAuditPublishers` | ✅ `func buildAuditPublishers(cfg *config.Config, db *gorm.DB, controlMetrics service.ControlPlaneMetrics)` at :135; single call site at :87 inside `buildAuditStack`. Flag-gated (`Enabled`), source-prefix guard, `auditOAuthBindings`, `validateAuditBindingCoverage`, `NewOAuthTokenSource`, `NewPublisher` |
| `internal/app/audit.go:163-165` — OnScopeRejected closure | ✅ `OnScopeRejected: func(string) { controlMetrics.ObserveAuditScopeMissing(strings.TrimSpace(settings.SourcePrefix)) }` at :163-165; C-C no-double-count comment at :160-162 |
| `internal/app/audit.go:145-147` — source-prefix guard | ✅ `if strings.TrimSpace(settings.SourcePrefix) != "aero-id"` → `fmt.Errorf("audit governance source_prefix must be %q, got %q", ...)` at :145-147 |
| `internal/app/audit.go:96-101` — F-1 guard | ✅ Comment :96-99; `if setter, ok := callbackSvc.(service.ControlPlaneMetricsSetter); ok { setter.SetControlPlaneMetrics(controlMetrics) }` at :100-101 (ledger setter at :93) |
| `internal/app/audit_integration_test.go:318` — `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain` | ✅ Function at :318; integration-tagged; asserts only `aero_control_audit_publication_outcomes_total{status="completed"} 1`; zero `scope_missing` references in the file |
| "No reference in `internal/app/*_test.go`" | ✅ `grep buildAuditPublishers` hits only `audit.go:87,135` + two comments (`audit_integration_test.go:478`, `composition_root_integration_test.go:126`). No test calls the function or the closure |
| `internal/pkg/metrics/control_plane.go:110-116` — `ObserveAuditScopeMissing` | ✅ Doc :110-114; `func (r *Registry) ObserveAuditScopeMissing(source string)` at :116; counterMap keyed by source |
| `internal/pkg/metrics/control_plane.go:227` — exposition | ✅ `fmt.Fprintf(w, "aero_control_audit_scope_missing_total{source=%q} %d\n", key, value)` at :227 |
| `internal/service/control_plane_metrics.go:43` — noop fallback | ✅ Line 43 is exactly `func (noopControlPlaneMetrics) ObserveAuditScopeMissing(string) {}`; `NewRegistryControlPlaneMetrics(nil)` returns the noop at :63-67 |
| `internal/service/callback_audit.go:187` — drain-side counter | ✅ Line 187 is `s.metrics.ObserveAuditScopeMissing(strings.TrimSpace(fact.Source))` inside `if errors.Is(err, auditgovernance.ErrTokenScopeMissing)` (:186-188); settlement via `errAuditTenantUnbound` → `publicationOutcome` `pending`/`audit.binding_unavailable` (:148-149), never dead_letter |
| Classification sites (mechanism of the double-count) | ✅ `oauth.go:230-232` (token-endpoint 400 + invalid_scope → `s.notifyScopeRejected()` **and** `ErrTokenScopeMissing`) and `oauth.go:240-242` (scope-claim validation → `notifyScopeRejected()` **and** `ErrTokenScopeMissing`); `publisher.go:112` (events-endpoint 401 + scope body → `ErrTokenScopeMissing` only, no hook); `publisher.go:135-136` C-B propagation (`errors.Is(err, ErrTokenScopeMissing)` escapes `authorization`); `notifyScopeRejected` hook-once/panic-containment at `oauth.go:309-322` |
| Double-count finding (new, load-bearing) | ✅ **Confirmed by direct read**: a token-endpoint rejection fires the hook (+1, audit.go:164) **and** propagates `ErrTokenScopeMissing` through C-B out of `Publish` into `publishAuditFact`, where :187 increments the same family (+1) — both on `{source="aero-id"}`. One rejection = **2 increments today**; the T-10.2.3 scenario (one token-endpoint rejection + one events-401 classification) = **3, not the pinned 2**. The audit.go:160-162 C-C comment is contradicted by the drain path, and no test detects it |
| No `==`/string comparisons on the sentinel | ✅ `grep` for `== ErrTokenScopeMissing` / `!= ErrTokenScopeMissing` / `.Error()` comparisons: zero hits in `internal/` — every consumer uses `errors.Is` (publisher.go:135, callback_audit.go:186, connector tests). A wrapping sentinel is therefore safe |
| Sibling direction 2 seam | ✅ Analysis direction 2 (`docs/auto/analyses/internal-app-73e67e01.json`) acceptance T-5 names the shared seam "fake binding-coverage querier (extracted interface defaulting to the real UNION query)"; no sibling requirements doc has landed yet — the seam must be shaped once for both |

**Conclusion:** the requirements spec is accurate and current against the working tree. The double-count
is real, the pinned acceptance (2 increments) is achievable only via the R3 provenance distinction, and
the new test fails red (3) against pre-fix code. This design implements R1–R4.

---

## 1. API changes

No wire/proto/HTTP/gRPC/DB-schema/config/metrics-family changes. Five production deltas (all
unexported or additive) plus two test surfaces.

### A1 — `internal/connector/auditgovernance/types.go` — provenance sentinel (additive, exported)

Next to `ErrTokenScopeMissing` (types.go:24):

```go
// ErrTokenEndpointScopeMissing marks a scope-missing classification that
// originated at the token endpoint (400-gate or scope-claim validation) as
// opposed to the events endpoint. It always wraps ErrTokenScopeMissing, so
// errors.Is(err, ErrTokenScopeMissing) stays true for every consumer. The
// drain uses the marker to skip its service-side counter for the token-
// endpoint path (the composition-root hook already counted it — C-C, exactly
// one increment per condition). It must NEVER be emitted at the events-
// endpoint site (publisher.go:112).
ErrTokenEndpointScopeMissing = errors.New("audit governance: token endpoint scope missing")
```

### A2 — `internal/connector/auditgovernance/oauth.go` — marker at the two token-source sites

The two sites inside `fetchToken` wrap the existing classification (multi-`%w`, Go 1.20+; both
`errors.Is` relations hold):

```go
// :230-232 (400-gate) — before:
return "", fmt.Errorf("%w: HTTP %d", ErrTokenScopeMissing, response.StatusCode)
// after:
return "", fmt.Errorf("%w: %w: HTTP %d", ErrTokenEndpointScopeMissing, ErrTokenScopeMissing, response.StatusCode)

// :240-242 (scope-claim validation) — before:
return "", ErrTokenScopeMissing
// after:
return "", fmt.Errorf("%w: %w", ErrTokenEndpointScopeMissing, ErrTokenScopeMissing)
```

`publisher.go:112` (events-endpoint site) and `publisher.go:135-136` (C-B propagation) are
**unchanged** — the marker passes through `authorization` untouched because `errors.Is(err,
ErrTokenScopeMissing)` remains true. `notifyScopeRejected` (oauth.go:309-322) is unchanged: the hook
still fires exactly once per token-endpoint classification (singleflight-coalesced, panic-contained),
never for events-endpoint classifications.

### A3 — `internal/service/callback_audit.go:186-188` — drain counter skip for the hook path

```go
if errors.Is(err, auditgovernance.ErrTokenScopeMissing) {
    // C-C: a token-endpoint rejection was already counted once by the
    // composition-root hook (buildAuditScopeMissingHook); the drain counter
    // covers only events-endpoint classifications. Skipping here restores
    // exactly one increment per scope-missing condition.
    if !errors.Is(err, auditgovernance.ErrTokenEndpointScopeMissing) {
        s.metrics.ObserveAuditScopeMissing(strings.TrimSpace(fact.Source))
    }
    return errAuditTenantUnbound
}
```

Row settlement is byte-identical for both paths: `errAuditTenantUnbound` → `publicationOutcome`
`pending`/`audit.binding_unavailable` (:148-149), never dead_letter.

### A4 — `internal/app/audit.go` — R1 hook-builder seam (pure extraction, no behavior change)

```go
// buildAuditScopeMissingHook returns the OnScopeRejected closure wired to the
// shared control-plane registry. The label is the normalized (TrimSpace'd)
// configured source prefix; the connector contract (token_invalidation_test.go
// :643 family) guarantees one invocation per token-endpoint scope rejection,
// so one hook call must produce exactly one increment.
func buildAuditScopeMissingHook(controlMetrics service.ControlPlaneMetrics, sourcePrefix string) func(string) {
	return func(string) {
		controlMetrics.ObserveAuditScopeMissing(strings.TrimSpace(sourcePrefix))
	}
}
```

`buildAuditPublishers` replaces the inline closure (:163-165) with
`OnScopeRejected: buildAuditScopeMissingHook(controlMetrics, settings.SourcePrefix)`. The
source-prefix guard (:145-147) is untouched and still runs before any connector construction.

### A5 — `internal/app/audit.go` — R2 binding-coverage seam (injectable, DB-free `buildAuditPublishers`)

```go
// auditBindingCoverage validates that every durable tenant with pending audit
// state has a governance binding. The gorm implementation issues the UNION
// query; tests inject a fake so buildAuditPublishers is unit-testable without
// a database. This is the single shared seam also consumed by the sibling
// direction's T-5 ("fake binding-coverage querier").
type auditBindingCoverage interface {
	CheckAuditBindingCoverage(bindings map[string]config.AuditGovernanceBindingConfig) error
}

// gormAuditBindingCoverage is the production implementation.
type gormAuditBindingCoverage struct{ db *gorm.DB }

func (g gormAuditBindingCoverage) CheckAuditBindingCoverage(bindings map[string]config.AuditGovernanceBindingConfig) error {
	return validateAuditBindingCoverage(g.db, bindings) // existing UNION query, exact error strings
}
```

- `buildAuditPublishers(cfg *config.Config, db *gorm.DB, controlMetrics service.ControlPlaneMetrics)`
  becomes `buildAuditPublishers(cfg *config.Config, controlMetrics service.ControlPlaneMetrics, coverage auditBindingCoverage)`
  — unexported, single call site (:87), so the signature change is internal. New fail-fast guard
  mirroring the `wireAuditServices` doctrine:
  `if coverage == nil { return nil, errors.New("audit governance binding coverage seam is not wired") }`
  (a nil seam would silently skip the coverage gate — the exact silent-loss class the outbox contract
  forbids).
- `buildAuditStack` passes `gormAuditBindingCoverage{gdb}` (audit.go:87). Production path issues the
  real UNION query with the existing error strings (`audit governance binding coverage check failed:
  ...`, `audit governance bindings do not cover durable tenant state` — audit.go:208/:212, function at :197).
- `validateAuditBindingCoverage` keeps its current name/body as the gorm-backed implementation.
- Keep everything in `internal/app/audit.go` (277 lines today → ≈310 after A4+A5; gate ≤ 500).
- **Sibling alignment (hard constraint):** this is the *same* seam the sibling direction 2 (T-5)
  consumes. Extract it once, shaped so both directions can call it; do not fork a second coverage
  mechanism. If the sibling lands first with a different method name, rename to the landed name — the
  interface shape (one method, `map[string]config.AuditGovernanceBindingConfig` → `error`) is the
  contract.

### A6 — New unit test file `internal/app/audit_scope_missing_test.go` (no build tag)

T-10.1a/b/c (details in §7). Uses `metrics.NewRegistry()` +
`service.NewRegistryControlPlaneMetrics(reg)` + `reg.WriteText` into a buffer for exact-series
assertions; a `httptest` token endpoint for T-10.1c; `t.Setenv` for the binding secret env (mirrors
`auditGovernanceTestConfig`, audit_integration_test.go:456-478).

### A7 — `internal/app/audit_integration_test.go` (integration-tagged) — T-10.2.3

`TestScopeMissingTokenEndpointPlusEvents401IncrementsExactlyOncePerCondition` (name per house
style) with a **stateful** governance endpoint (token-mode ↔ events-mode switch) and
stop/restart drain discipline, modeled on `auditGovernanceFixture` (audit_integration_test.go:365-497)
and the `stop := callbackSvc.Start(t.Context(), 50*time.Millisecond)` precedent (:336). Determinism
argument: `RetryBase: 1s` (fixture config) → after a failed attempt the row's `next_attempt_at` is
`now + retryDelay(attempt+1)` (≥ 1s + jitter, callback_worker.go:173-185); polling at ~50ms and
stopping the drain immediately upon observing `attempt = 1` / `attempt = 2` makes a second claim
within the phase impossible. A too-slow poll fails loudly (attempt > 1), never false-green.

---

## 2. Compatibility constraints

1. **`errors.Is(err, ErrTokenScopeMissing) == true` for both paths, forever.** Pinned by the connector
   tests (`token_invalidation_test.go:582-583`, `:679-680`) which stay byte-unchanged, and by the
   drain settlement (`callback_audit.go:186`). The marker only *adds* a second `Is` relation.
2. **Mutual exclusion with `ErrTokenRejected` preserved** (`token_invalidation_test.go:113`,
   `:582-583`): the marker never wraps `ErrTokenRejected`; `errors.Is(err, ErrTokenRejected)` stays
   false for both scope-missing paths.
3. **Hook semantics untouched:** exactly once per token-endpoint classification (singleflight
   coalescing, `notifyScopeRejected` panic containment, oauth.go:309-322), zero for events-endpoint
   classifications (`hookCalls == 0` pin at the :519 test). A1/A2 change only the *error value*, never
   hook invocation.
4. **No consumer uses `==` or string equality on the sentinel** (verified: zero hits in `internal/`),
   so the wrapper is invisible to every existing consumer.
5. **`service/callback_audit_test.go` (:122-134, metricSpy) stays green unchanged:** its fake
   publisher returns the bare `ErrTokenScopeMissing` sentinel (no marker) — the drain counter still
   fires, which is exactly right: the fake models the events-endpoint path. No interface change to
   `AuditPublisher` (a method-based marker would break the fake).
6. **Row terminality unchanged:** scope-missing → `pending`/`audit.binding_unavailable` via
   `errAuditTenantUnbound` before the `MaxAttempts` check (`callback_audit.go:148-149, 182-188`);
   never `completed`, never `dead_letter`. No change to retry/backoff/lease/MaxAttempts.
7. **Single label set:** only `{source="aero-id"}` may ever appear on
   `aero_control_audit_scope_missing_total` from either path — the hook normalizes the prefix
   (audit.go:164) and every ledger fact defaults `Source: "aero-id"` (audit_ledger.go:121-122;
   audit_hold.go:163,183; audit_replay.go:49).
8. **No new metric family, counter, config surface, or migration.** `buildAuditStack`'s signature is
   unchanged (A5 changes only `buildAuditPublishers`, unexported, one call site). `fact.Source`,
   `publicationOutcome`, drain worker, and connector classification semantics are untouched.
9. **Existing app-level tests stay green:** `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain`
   (:318) and the governance fixture tests use healthy endpoints (no scope-missing) and assert drain
   counters only — unaffected by A1-A5.

---

## 3. Design decisions and rejected alternatives

**D-1 — Marker shape: exported sentinel wrapping the existing sentinel via multi-`%w`.**
`fmt.Errorf("%w: %w", ErrTokenEndpointScopeMissing, ErrTokenScopeMissing)` gives both `errors.Is`
relations, single-line messages, and a two-line diff per site.
- *Rejected: `errors.Join(...)`* — functionally equivalent (both `Is` relations hold) but newline-
  joined messages in logs/`%v` output; no benefit.
- *Rejected: typed wrapper struct* (`type tokenEndpointScopeError struct{ ... }` with `Unwrap()`) —
  more code, and `errors.As`-based consumers would need new branches; the sentinel approach needs
  none.
- *Rejected: reusing `ErrTokenScopeMissing` plus a string prefix check in the drain* — fragile,
  stringly-typed, breaks `errors.Is` doctrine.

**D-2 — Marker site: the two token-source classification sites in `fetchToken` (oauth.go:230-232,
240-242), not the propagation site (publisher.go:135-136).** Both classification paths funnel through
`fetchToken`; the marker is semantically "this classification originated at the token endpoint" and is
correct for *any* caller of `AccessToken` (today only `authorization`, publisher.go:131). The
events-endpoint site (publisher.go:112) is structurally incapable of carrying the marker — the two
sites are the complete token-endpoint surface. Applying at the propagation site instead would miss a
future direct `AccessToken` caller and puts the marker one hop away from the classification.

**D-3 — R2 seam injection: explicit parameter on `buildAuditPublishers`, production default
constructed in `buildAuditStack`.** The function is unexported with one call site; a parameter is the
smallest testable surface. *Rejected: constructor-with-default/option pattern* — boilerplate for one
call site. The nil-seam fail-fast (A5) closes the silent-skip hole that a defaulted-constructor
approach would leave open.

**D-4 — R1 hook-builder: plain `func(string)` closure factory.** Identical shape to today's closure;
the label normalization (`strings.TrimSpace`) lives inside so T-10.1b can pin it. *Rejected: a struct
or method receiver* — no benefit for a two-line closure.

**D-5 — T-10.2.3 determinism: stateful endpoint + stop-drain-on-row-state, no production knob
changes.** The spec's discipline (stop the drain the moment `attempt` reaches the phase's pinned
value; `RetryBase: 1s` makes the next claim ≥ 1s + jitter away; tick at 50ms) is adopted as-is.
*Rejected: injectable clock or configurable retryDelay in the drain* — explicitly out of scope (no
retry/backoff changes) and would ripple into the connector's contract tests.

**D-6 — Drain skip condition: `!errors.Is(err, ErrTokenEndpointScopeMissing)`.** Mirrors the C-C
contract exactly (token-endpoint → hook only). *Rejected: switching on the wrapper via `errors.As` to
a struct type* — same effect, more code (see D-1). *Rejected: suppressing the counter for *all*
scope-missing errors* — would silently drop the events-endpoint count (FM-2).

---

## 4. Failure modes

| FM | Failure | Detection | Severity |
|---|---|---|---|
| FM-1 | Marker forgotten at one of the two token-source sites (A2 partial) → token-endpoint rejection double-counts again (hook + drain counter = 2) | T-10.2.3 Phase A shows `... 2` instead of 1; final total 3 instead of 2 → red. T-10.1c alone cannot see it (it exercises the seam, not the drain) — T-10.2.3 is the only detector | High (silent metric inflation today; the exact regression this direction exists to prevent) |
| FM-2 | Marker accidentally applied at the events-endpoint site (publisher.go:112) → events-401 classifications no longer increment the counter (silent undercount; hook correctly stays silent) | T-10.2.3 Phase B total stays 1 instead of 2 → red. Connector tests do not check the marker, so T-10.2.3 is the only detector | High |
| FM-3 | Hook unwired / `NewRegistryControlPlaneMetrics(nil)` / noop fallback (control_plane_metrics.go:63-67) → every scope-missing increment sinks; runbook alerts match nothing | T-10.1c (family stays 0) and T-10.2.3 Phase A (0 instead of 1) → red | High (the F-1-mirror acceptance) |
| FM-4 | R2 seam extraction bug: `buildAuditPublishers` stops calling coverage (or calls a nil seam) → coverage gate silently skipped (unbound tenants publish into the void per the FM-13 class) | Nil-seam fail-fast (A5) + T-10.1c (fake coverage driving the hook requires the seam to run) → red; existing integration fixture would not catch a skipped gate (empty container passes trivially) | Medium |
| FM-5 | Wrong wrap direction at a site (`fmt.Errorf("%w: %w", ErrTokenScopeMissing, ErrTokenEndpointScopeMissing)` is fine; a `%v` instead of `%w` is not) → one `errors.Is` relation breaks | Connector tests (`token_invalidation_test.go:582-583, :679-680`) and drain settlement fail immediately → red | Medium |
| FM-6 | T-10.2.3 timing: drain claims again within a phase (poll slower than backoff) → `attempt` exceeds the pinned value | Loud assertion failure (`attempt == 1` / `attempt == 2`), never a false green | Low (test-only) |
| FM-7 | Label drift: a future change sets `fact.Source` or `SourcePrefix` to anything but `"aero-id"` → two label sets on one family | T-10.2.3 "single label set `{source="aero-id"}`" assertion → red; source-prefix guard (audit.go:145-147) already fail-fasts config-side drift | Low |
| FM-8 | Sibling direction lands a *second* coverage mechanism (fork) → two UNION queries drift apart | Review gate; the A5 comment names the single shared seam; sibling T-5 must consume `auditBindingCoverage` | Medium (process) |
| FM-9 | `buildAuditStack` (or a future caller) passes `nil` coverage | A5 fail-fast error at startup, mirroring `wireAuditServices` panic doctrine → loud, not silent | Low |

---

## 5. Migration steps (change rollout)

No data migration, no config migration, no schema change, no wire change. Ordered code rollout with
the done gate at each step; the pipeline's `git_commit: true` commits the batch.

1. **Baseline** — confirm the working tree is green: `make test` on HEAD `b005409` + uncommitted
   sibling work (must already pass; sibling work is independent).
2. **R1 (A4) — hook-builder extraction.** Pure refactor; `make test` stays green. Add T-10.1a/T-10.1b
   (unit, no DB) — green immediately.
3. **R2 (A5) — coverage seam.** `buildAuditPublishers` signature change; `buildAuditStack` passes
   `gormAuditBindingCoverage{gdb}`; nil-seam fail-fast. Run `make test`, `make lint-strict`,
   `make check-integration-compile` (the signature change ripples into any integration-tagged caller —
   none exists beyond `buildAuditStack`, but the gate proves it). Add T-10.1c (fake coverage +
   httptest token endpoint) — green.
4. **R3 (A1+A2+A3) — provenance marker.** types.go sentinel; oauth.go two sites; callback_audit.go
   skip. **Red-run discipline:** before A3 lands (or by temporarily reverting the skip), run the new
   T-10.2.3 and record the expected red (3 increments) — this is the regression-detector proof; then
   land A3 and confirm green (2). All existing connector tests (`token_invalidation_test.go:519`,
   `:643` families) and `service/callback_audit_test.go` must stay green **unchanged** — this is the
   A1/A2 compatibility contract.
5. **T-10.2.3 (A7)** — stateful-endpoint integration test with stop/restart drain discipline
   (Phase A: token-endpoint rejection → exactly 1; Phase B: events-401 → total exactly 2; row
   `pending`/`audit.binding_unavailable` with `attempt` 1 then 2; never `completed`/`dead_letter`).
6. **Full done gate:**
   `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-composition-root && make check-integration-compile && make build`
   plus **`make test-integration`** (Postgres via testcontainers) for A7.
7. **Convention copies** — this design + the landed requirements doc
   (`docs/requirements/2026-08-08-internal-app-scope-missing-alerting-wiring-requirements.md`) are the
   batch record; commit with the stage artifact.

---

## 6. Maintenance contract

- The C-C contract is **test-enforced, not comment-only**: T-10.1c pins the hook path, T-10.2.3 pins
  the exactly-once family total, the :519/:643 connector tests pin hook silence/frequency. The
  audit.go:160-162 comment is updated to describe both paths (token-endpoint → hook only; events-
  endpoint → counter only) instead of the current one-sided claim.
- `ErrTokenEndpointScopeMissing` is documented in types.go with the "never at the events-endpoint
  site" constraint (A1). Any future classification site must deliberately choose the marker;
  T-10.2.3's total pins any drift.
- The seam (`auditBindingCoverage`) is the single shared binding-coverage mechanism for the
  composition root; the sibling direction's T-5 consumes the same interface (A5).
- No new metric family, no new config key, no new sentinel beyond the one provenance marker. The
  family's documented meaning (control_plane.go:110-114) is unchanged: one increment per scope-missing
  condition.

---

## 7. Testable acceptance mapping

Supplied acceptance checks preserved verbatim; each decomposes into runnable assertions.

| Supplied acceptance | Requirement | Tests / assertions | Gate |
|---|---|---|---|
| T-10.1: unit test of an extracted hook-builder/binding-coverage seam; one token-endpoint scope rejection → `aero_control_audit_scope_missing_total{source="aero-id"}` exactly once (not zero, not twice) | R1, R2 | **T-10.1a** (new, `internal/app/audit_scope_missing_test.go`): `buildAuditScopeMissingHook(service.NewRegistryControlPlaneMetrics(reg), "aero-id")` invoked once → `reg.WriteText` contains exactly `aero_control_audit_scope_missing_total{source="aero-id"} 1` and no other `scope_missing` series; second invocation → `... 2` (per-call exactly one). **T-10.1b**: hook with `" aero-id "` → still `{source="aero-id"}` (TrimSpace pin); noop argument → family stays zero. **T-10.1c**: fake `auditBindingCoverage` + httptest `/token → 400 {"error":"invalid_scope"}` + `t.Setenv` secret; `buildAuditPublishers(cfg, metrics, fakeCoverage)`; `Publish(scopeFact)` → exposition `... 1` (proves provided metrics reached the hook — non-noop wiring); second `Publish` → `... 2` (no per-publish double increment) | `make test` (no Docker) |
| T-10.2.3: integration test; token-endpoint rejection (hook path) + events-401 classification (drain path) → exactly 2 increments on the same registry family — pins the C-C no-double-count contract | R3 | **A7** (`audit_integration_test.go`, integration tag): Phase A — stateful endpoint `/token → 400 invalid_scope`, real `buildAuditStack` (testpg + migrations + registry-backed metrics), `Record` one fact, start drain, poll publication row to `pending`/`audit.binding_unavailable` `attempt = 1`, stop drain; assert family == exactly 1, single label set. Phase B — flip endpoint to `/token → 200` + `/api/v1/events → 401 invalid_scope`, restart drain; row attempt → 2, back to `pending`/`audit.binding_unavailable`; stop drain; assert family == exactly 2. Negative framing: pre-fix code shows 2 in Phase A / 3 total → red; unwired hook shows 0 → red | `make test-integration` |
| T-10.1 wiring assertion: `buildAuditStack` installs non-noop control metrics for the hook path, mirroring the F-1 guard for drain metrics (audit.go:96-101) | R4 | **Unit leg** = T-10.1c (a noop/`NewRegistryControlPlaneMetrics(nil)` regression leaves the family at 0 → red). **Integration leg (F-1 mirror)** = T-10.2.3 Phase A driving the *real* `buildAuditStack` (hook wired :87 → :163-165) with registry-backed metrics — the exact shape of `TestBuildAuditStackWiresControlMetricsIntoCallbackDrain` (:318) for the drain counters. Mirror complete only when both tests exist | `make test` + `make test-integration` |

**Red-run evidence to record in the change report:** T-10.2.3 against pre-R3 code yields 3 increments
(Phase A = 2, total = 3) — run once before landing A3 and document the expected red; after A3 it is
green with 2. This is the regression-detector proof the direction exists for.

---

## 8. Verification delta vs. the requirements spec

1. **`internal/domain/audit_fact.go:30` → `internal/service/audit_ledger.go:121-122`** — wrong file
   citation for the `Source: "aero-id"` default; substance confirmed at the correct location.
2. **Connector test lines** — `:525/:644` (evidence) and `:519/:525, :640/:644` (spec) both drift
   slightly; actual funcs at `token_invalidation_test.go:519` and `:643`. All contract assertions
   verified at their actual sites.
3. **Spec §6 decisions resolved:** marker shape = exported sentinel, multi-`%w` wrap (D-1); marker
   site = the two `fetchToken` classification sites (D-2); seam signature = explicit parameter,
   production default in `buildAuditStack` (D-3); T-10.2.3 determinism = stop-drain-on-row-state with
   the 50ms-tick / 1s-backoff argument (D-5).
4. **Added constraint beyond the spec:** A5 nil-seam fail-fast — the spec's "parameter defaulted by
   `buildAuditStack`" wording left a silent-skip hole; the fail-fast closes it at zero cost.
5. **Working-tree note:** uncommitted sibling-batch work (activation gate, anchor-derivation e2e) is
   present and independent; all line citations in this design are against the tree as read, and the
   rollout steps assume that tree stays green.
