# Design — Billing integration Load gate: TokenURL/Endpoint parity with `billingconnector` (mirror T-10.1.6 audit pattern)

Status: **Proposed** (implementation-ready)
Date: 2026-08-16
Module: `internal/config/commercial.go` (billing gate) + `internal/config/commercial_test.go` / `internal/config/config_test.go` (regression pins)
Scope: No connector, app, or integration-tagged code changed.

---

## 1. Verification verdict (untrusted evidence → tree)

Every claim in the requirements spec was re-verified against the live tree on 2026-08-16. **All citations confirm; zero content deltas.** The only deviation is the line drift the evidence itself already flags (`app/billing.go:36-41` ≈ actual `NewClient` block `:35-44`).

| # | Claim (evidence) | Verdict | Tree evidence |
|---|---|---|---|
| E1 | `validateBillingBindings` at `commercial.go:36-53`; checks only `strings.TrimSpace(value.Endpoint) == ""` and per-binding tenant/scope/resource + `registerCredential`; **no TokenURL check, no Endpoint shape check** | **Confirmed** | `internal/config/commercial.go:36-53` — exact; `validBillingTokenURL` does not exist anywhere in `internal/` (grep: NOT FOUND). |
| E2 | Audit Load-gate precedent: `validateAuditBindings` :56, `validAuditTokenURL` :82 (parity comment verbatim), `allowedLoopbackTokenURL` :96 | **Confirmed** | `commercial.go:56`, `:82`, `:96` — comment text matches the spec's quote exactly; file is 195 lines (`wc -l`). |
| E3 | `newTokenSource` authoritative rules: `oauth.go:44-53`, `secureEndpoint(config.TokenURL, config.AllowHTTP)` at :45, rejects `endpoint.RawQuery != ""` at :48 | **Confirmed** | `internal/connector/billing/oauth.go:44`, `:45`, `:48` — exact. |
| E4 | `secureEndpoint` at `client.go:287-299`; `validAbsoluteEndpoint` :301-303 (Host≠"" ∧ User==nil ∧ Fragment==""); `isLoopbackHTTP` :305-310; `NewClient` rejects `base.RawQuery != ""` :88 and `len(config.Bindings)==0` :88-89; `ErrInvalidConfig` :32 | **Confirmed** | `internal/connector/billing/client.go` — all exact (the RawQuery and len(Bindings) guards share line 88; line 89 returns). |
| E5 | Audit connector already mirrors: `auditgovernance/oauth.go:81-82` `secureEndpoint(binding.TokenURL, …)` + `RawQuery != ""` | **Confirmed** (Exact) | `internal/connector/auditgovernance/oauth.go:81-82`. |
| E6 | Parity pin + shared matrix: `auditTokenURLCases` :245-276 (16 cells incl. bare-`?` and trimmed rows), `TestAuditTokenURLGateParityWithConnector` :301-326, `TestAuditGovernanceTokenURLErrorDoesNotDiscloseValue` :226-239, `TestAuditGovernanceRejectsInvalidTokenURLBeforeCredentials` :190-222, `commercialTestConfig` :334, `setCommercialSecrets` :328 | **Confirmed** | `internal/config/commercial_test.go` — all at the cited lines; matrix cells read verbatim as listed (16 rows, `"https bare query"` + `"trimmed https"` included). |
| E7 | Boot-wiring failure point: `buildBillingRuntime` :20, `billingconnector.NewClient` :35-41, `"billing client: %w"` :43, called from `app/new.go:147` | **Confirmed** (minor line drift, self-flagged) | `internal/app/billing.go` — `buildBillingRuntime` at :20, `NewClient` block :35-44, wrap :43; `internal/app/new.go:147` `billingRuntime, err := buildBillingRuntime(cfg, gdb)`. |
| E8 | Load path: `Load` (`config.go:384`) → `cfg.validate()` (`validate.go:10`) → `validateRuntimeLimits` (:134) → `validateCommercialIntegrations` (:140) | **Confirmed** | `internal/config/config.go:384`; `internal/config/validate.go:10`, `:18`, `:134`, `:140`. |
| E9 | Shipped file billing block at `configs/config.yaml:191-201`: `enabled: false`, `endpoint: ""`, `bindings: {}` | **Confirmed** | `configs/config.yaml` `billing:` block reads exactly that; `TestLoadShippedConfigFileAndEnvGate` at `config_test.go:511` (existing `gov.Enabled`/`gov.Bindings` assertions at :519-524). |
| E10 | Constants parity: config-package `billingEntitlementScope`/`billingMeteringScope`/`billingResource` equal connector `ScopeEntitlementRead`/`ScopeMeteringWrite`/`"billing-api"` | **Confirmed** | `commercial.go:12-14` vs `client.go:26-28` — identical string literals. |
| E11 | Baseline green (pre-change) | **Confirmed** | `go build ./...` clean; `go test ./internal/config/ ./internal/connector/billing/ ./internal/connector/auditgovernance/ -count=1` all `ok` on 2026-08-16. |

**Evidence deltas found: none.** All cited symbols, line ranges, error strings, matrix cells, and behaviors reproduce exactly. The ACC set is adopted as specified.

---

## 2. Problem statement

`validateBillingBindings` (`commercial.go:36-53`) validates the activation envelope (`value.Enabled`, non-empty `Endpoint`, non-empty `Bindings`) and, per binding, `canonicalIdentity(tenant)`, `exactScopes` against `billingEntitlementScope`/`billingMeteringScope`, `exactResources` against `billingResource`, and `registerCredential(ClientID, ClientSecretEnv)`. **It never validates `binding.TokenURL`, and only checks `Endpoint` for emptiness — never its URL shape.**

Its audit twin `validateAuditBindings` → `validAuditTokenURL` (`commercial.go:56-105`) carries the explicit parity comment: a misconfigured enabled gate fails at `Load()` instead of connector construction at boot-wiring, with the error never echoing the raw value. The parity is pinned by `TestAuditTokenURLGateParityWithConnector` (`commercial_test.go:301-326`) over the shared `auditTokenURLCases` matrix (`:245-276`). **Billing has no such gate.**

Consequence (verified): an enabled billing block with `token_url: http://sso.example/token` (loopback off), `https://u:p@sso.example/token`, `https://sso.example/token?x=1`, or a malformed `endpoint` passes `Load()` and dies only at boot-wiring: `buildBillingRuntime` (`internal/app/billing.go:20`) → `billingconnector.NewClient` (`:35-44`) → `newTokenSource` (`internal/connector/billing/oauth.go:44`) → `secureEndpoint` (`internal/connector/billing/client.go:287`), surfacing as opaque `billing client: billing: invalid configuration` — the exact Load-vs-boot asymmetry the audit gate exists to prevent.

**Authoritative connector URL rules** (identical composite for `TokenURL` and `Endpoint`, from `secureEndpoint` + the `RawQuery != ""` guards at `oauth.go:45/48` and `client.go:87-88`): after `strings.TrimSpace`, `url.Parse` must succeed; then `Host != ""` (absolute), `User == nil` (no userinfo), `Fragment == ""` (no fragment), `RawQuery == ""` (no raw query; a bare trailing `?` with `ForceQuery=true`/`RawQuery==""` is accepted); scheme `https` **or** (`AllowInsecureLoopback` ∧ scheme `http` ∧ hostname `localhost` or loopback IP). `validAuditTokenURL` implements exactly this composite and is the behavioral template; `allowedLoopbackTokenURL` (`commercial.go:96-105`) is already generic and reusable.

---

## 3. Design

### 3.1 API changes

**No public shape change.** `BillingConfig`, `BillingBindingConfig`, `config.yaml` keys, `app/billing.go`, and `connector/billing/*` are untouched. The only change is behavioral: `validateBillingBindings` gains two URL-shape gates, so a previously boot-doomed config now fails at `Load()` with a stable, non-disclosing message.

1. **New sibling helper** `validBillingTokenURL(raw string, allowLoopback bool) bool` in `commercial.go`, mirroring `validAuditTokenURL` verbatim (trim → `url.Parse` → `Host != ""` ∧ `User == nil` ∧ `Fragment == ""` ∧ `RawQuery == ""` → `https` or `allowedLoopbackTokenURL`). Reuses the existing generic `allowedLoopbackTokenURL`. This mirrors the connector twin-package pattern (`auditgovernance` and `billing` each carry their own `secureEndpoint`) and deliberately does not refactor the audit side.
2. **REQ-1 — TokenURL gate:** inside the per-binding loop, after the tenant/scope/resource check and **before** `registerCredential` (D2 ordering), reject with `billing binding contains an invalid token URL` when `!validBillingTokenURL(binding.TokenURL, value.AllowInsecureLoopback)`. A missing/short secret env can never mask a URL error, mirroring `TestAuditGovernanceRejectsInvalidTokenURLBeforeCredentials` (`commercial_test.go:190-222`).
3. **REQ-2 — Endpoint gate:** after the existing `strings.TrimSpace(value.Endpoint) == ""` emptiness check, reject a non-empty-but-malformed Endpoint with `billing endpoint contains an invalid URL` when `!validBillingTokenURL(value.Endpoint, value.AllowInsecureLoopback)`. (The helper's `RawQuery == ""` rule closes the connector's `base.RawQuery != ""` guard at `client.go:88`; the `len(Bindings) == 0` guard at `client.go:88-89` is already mirrored by the existing `len(value.Bindings) == 0` check.)
4. **REQ-4 — Non-disclosure:** both messages are stable substrings that never echo the raw value (userinfo `u:p@…` never leaks), matching `TestAuditGovernanceTokenURLErrorDoesNotDiscloseValue`. Disabled billing (`Enabled == false`) short-circuits as today — no change.

`commercial.go` grows 195 → ≈220 lines, comfortably under the 500-line gate (`make check-file-sizes` applies to non-test files only).

### 3.2 Compatibility constraints

- **Parity is the compatibility contract (REQ-3).** The gate and `billingconnector.NewClient` must agree on every URL cell at both `AllowInsecureLoopback` values. Because both sides implement the identical composite (`secureEndpoint` semantics + `RawQuery`), the gate accepts **exactly** the URL set the connector accepts. Therefore **every currently-working deployment keeps working**: any config that boots today already has connector-valid URLs (the connector is a hard gate at boot), so the new Load gate rejects precisely the configs that were already doomed to fail at boot-wiring. The change is strictly failure-timing and failure-message quality.
- **No audit-side change:** `validateAuditBindings`/`validAuditTokenURL` and their pins (`TestAuditGovernance*`, `TestAuditTokenURLGateParityWithConnector`) remain verbatim.
- **Validation ordering inside `validateBillingBindings`** (all first-error-wins, as today): envelope → Endpoint shape → per binding: tenant/scope/resource → TokenURL shape → credentials. The parity pins (ACC-1/ACC-2) hold all non-URL dimensions constant-valid so only the URL dimension varies.
- **Env merge cannot bypass:** `AERO_ID_BILLING_*` env overrides merge into the same struct inside `Load` before `validate()` runs, so the gate reads the merged config; env-loopback enables exactly what the connector enables (pinned in ACC-1's matrix), and the existing audit test at `config_test.go:151-180` documents this property for the audit twin.
- **Constants:** the config-package `billingEntitlementScope`/`billingMeteringScope`/`billingResource` equal the connector's `ScopeEntitlementRead`/`ScopeMeteringWrite`/`"billing-api"` (E10) — the parity test's connector-side config uses the config constants directly, so no literal drift is possible.
- **No integration-tagged code touched:** the config package is unit-tested (`package config`), so `make check-integration-compile` is unaffected.

### 3.3 Failure modes

| Mode | Behavior | Guard |
|---|---|---|
| **Gate/connector drift (gate accepts, connector rejects)** | Config passes `Load()`, dies at boot as today — the original bug reappears | ACC-1/ACC-2 parity pins turn any drift red on any matrix cell; boot failure remains as backstop for programmatically constructed configs |
| **Gate too strict (gate rejects, connector accepts)** | Deployment fails to start on a connector-valid URL | Parity pins; failure is loud and actionable at Load with stable message |
| **Credential masking** | A bad URL hidden behind a missing/short secret env error | D2 ordering: URL check precedes `registerCredential` (ACC-3 asserts the URL message, never a credential message, even with secrets unset) |
| **Non-disclosure** | Raw URL (incl. userinfo password) echoed in error / logs | ACC-3 asserts `!strings.Contains(err.Error(), rawURL)`; messages are fixed substrings |
| **Env bypass attempt** | `AERO_ID_BILLING_ENABLED=true` + invalid URL in file | Gate reads the env-merged struct; env cannot bypass (mirrors `TestLoadAuditGovernanceTokenURLGate` env legs) |
| **Shipped-file regression** | A future file edit flips billing to enabled/non-empty | ACC-4 strengthens `TestLoadShippedConfigFileAndEnvGate` with `cfg.Billing.Enabled == false && len(cfg.Billing.Bindings) == 0` |
| **Boot-wiring regressions** | Connector relaxes/tightens URL rules independently of the gate | Parity pins import `billingconnector` and call `NewClient` directly, so a connector change without a gate change is red |

### 3.4 Migration steps

1. **Code:** add `validBillingTokenURL` and the two gate calls per §3.1; add the ACC-1…ACC-4 tests.
2. **Gates:** run `make test && make lint && make lint-strict && make check-file-sizes && make check-integration-compile && make build` (ACC-5). No schema, migration-set, or proto impact.
3. **Ops:** no action for currently-running valid configs (parity guarantees acceptance set unchanged). Runbook note: an enabled billing block with an invalid `token_url` or malformed `endpoint` now fails `Load()` with `billing binding contains an invalid token URL` / `billing endpoint contains an invalid URL` instead of an opaque `billing client: billing: invalid configuration` at boot-wiring. The shipped file (disabled, empty bindings) is unaffected.
4. **No data migration:** this is a pure load-time validation change; no repository, SQL, or event changes.

---

## 4. Testable acceptance mapping

### ACC-1 — Parity pin: TokenURL (supplied, preserved)

`TestBillingTokenURLGateParityWithConnector` in `internal/config/commercial_test.go`, modeled on `TestAuditTokenURLGateParityWithConnector` (:301-326) and reusing the shared `auditTokenURLCases()` matrix (:245-276) — all 16 cells, each at its matrix loopback value:

1. `setCommercialSecrets(t, "billing-secret-value-0000000000000001", "audit-secret-value-000000000000000001")` (two distinct ≥32-byte literals — Billing and Audit credentials stay globally distinct for the `registerCredential` duplicate guards).
2. Gate side: `cfg := commercialTestConfig()` (audit remains valid `https://sso.example/token`; `sameTenantSet` at `commercial.go:28` must not fire — both sides bind `tenant-one`); `cfg.Billing.AllowInsecureLoopback = tc.allowLoopback`; `cfg.Billing.Bindings["tenant-one"].TokenURL = tc.tokenURL`; `gateErr := cfg.validateCommercialIntegrations()`.
3. Connector side: `billingconnector.NewClient(billingconnector.Config{Endpoint: "https://billing.example", Timeout: 5*time.Second, MaxBodyBytes: 256<<10, AllowInsecureLoopback: tc.allowLoopback, Bindings: map[string]billingconnector.BindingConfig{"tenant-one": {TokenURL: tc.tokenURL, ClientID: "billing-client", ClientSecret: "billing-secret-value-0000000000000001", Scope: billingEntitlementScope+" "+billingMeteringScope, Resources: []string{billingResource}}}}, nil)` → `connectorErr`.
4. Assert `(gateErr == nil) == (connectorErr == nil)`; drift on any cell (including `https://…/token?` bare-query and `  https://…  ` trimmed rows) is red.

Import `billingconnector "github.com/aero/aero-id/internal/connector/billing"` — no cycle (the connector does not import `config`; same pattern as the audit pin's `auditgovernance` import at `commercial_test.go:9`).

### ACC-2 — Parity pin: Endpoint (from the direction's "TokenURL/**Endpoint**" scope)

`TestBillingEndpointGateParityWithConnector`: Endpoint sub-matrix of the same composed rule set applied to the Endpoint — cells: `https://billing.example` (valid), `http://billing.example/token` off/on, `http://localhost:9090` off/on, `https://u:p@billing.example`, `https://billing.example#frag`, `https://billing.example?x=1`, `https://billing.example?` (bare, valid), `""`, `"   "` (rejected by the existing emptiness check). For each cell at both loopback values: gate side sets `cfg.Billing.Endpoint` = cell (binding TokenURL stays `https://sso.example/token`); connector side sets `billingconnector.Config.Endpoint` = cell (valid binding); assert `(gateErr == nil) == (connectorErr == nil)`. This keeps the fix complete on both dimensions named in the direction and proves zero gate/connector drift without touching any other code.

### ACC-3 — Negative Load tests (supplied, preserved)

`TestLoadBillingTokenURLGate` in `internal/config/config_test.go`, modeled on `TestLoadAuditGovernanceTokenURLGate` (:151-180) and `writeTemp(t, yaml)` (:22-28). Base fixture = the `sample` constant plus an enabled billing block with valid endpoint/tenant/scope/resources and `allow_insecure_loopback: false` (mirroring the `auditGovernanceBadYAML` discipline at :124-141; secret env may be unset — the URL check precedes credentials, D2). For each of the three cells — `token_url: "http://sso.example/token"` (loopback off), `"https://u:p@sso.example/token"`, `"https://sso.example/token?x=1"` — assert **both**:
- `Load(path)` errors containing `billing binding contains an invalid token URL` (never a credential error — D2 ordering), and
- `!strings.Contains(err.Error(), rawURL)` — non-disclosure, matching `TestAuditGovernanceTokenURLErrorDoesNotDiscloseValue`.

The Endpoint dimension at Load level is covered by the ACC-2 parity pin; no separate endpoint Load cell is required (keeps scope minimal, matches the supplied acceptance list).

### ACC-4 — Positive leg / shipped file (supplied, preserved)

`TestLoadShippedConfigFileAndEnvGate` (`config_test.go:511`) stays green (the shipped billing block is disabled with empty bindings, so the gate early-returns). Strengthen it minimally by asserting `cfg.Billing.Enabled == false && len(cfg.Billing.Bindings) == 0`, mirroring the existing `gov.Enabled`/`gov.Bindings` assertions at :519-524, so a future file edit to a non-empty/disabled billing block fails loudly.

### ACC-5 — Done gate (supplied, preserved)

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-integration-compile && make build
```

- `commercial.go` ends ≈220 lines (well under 500; `check-file-sizes` applies to non-test files only).
- No integration-tagged code is added or touched.
- Existing tests stay green: `commercialTestConfig()` uses a valid `https://…/token`, so `TestCommercial*`, `TestAuditGovernance*`, and the audit parity pin are undisturbed (baseline re-run in §1 E11 confirms the pre-change state is green).

---

## 5. Out of scope (preserved)

No change to the audit side (`validateAuditBindings`/`validAuditTokenURL` and its pins remain verbatim — the direction is billing-only); no change to `BillingConfig`/`BillingBindingConfig` shape, `config.yaml`, `app/billing.go`, or `connector/billing/*`; no unknown-key strictness; no `CallbackConfig` validation; no integration-tagged code.
