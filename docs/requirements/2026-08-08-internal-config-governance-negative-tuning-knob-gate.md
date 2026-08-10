# Requirements — Governance B2-3 tuning-knob gate: negative `audit_governance.timeout` / `refresh_skew` fail at `Load()` (T-10.1, T-5)

| | |
|---|---|
| Module (batch label) | `internal/config` (validation + Load-level unit tests only; no connector changes) |
| Source analysis | `docs/auto/analyses/internal-config-a932ee1e.json` (direction 2 of 3: `reject-negative-audit-governance-tuning-knobs`) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-3 token-source fetch/refresh surface; follows the landed B2-4 anchor, B2-5 knob, and B2-6 activation-gate work) |
| Predecessor | `docs/requirements/2026-08-07-internal-config-b2-6-activation-gate.md`, `docs/plans/2026-08-08-internal-config-b2-6-activation-gate-completion-design.md` (T-10.1 gate family), `docs/plans/2026-08-08-internal-config-governance-load-time-endpoint-source-prefix-gates-design.md` (sibling direction 1 — endpoint/source_prefix gates; disjoint surface, no overlap) |
| Date | 2026-08-08 |
| Status | Draft — every cited file/symbol re-verified against the working tree at `b005409` |

## 1. Evidence verification status

All direction citations were re-checked against the working tree. The gap exists exactly as claimed; three
line citations drifted (noted below). Baseline: `go test ./internal/config/...` → `ok` (cached, green) at
verification time, so "existing cases stay green" has a measured baseline.

| # | Claim (direction evidence) | Verification (working tree @ `b005409`) |
|---|---|---|
| E1 | `internal/config/validate.go:149-162` — `validateAuditGovernance` never inspects `Timeout` or `RefreshSkew` | ✅ stands, line drift (`:227-240`). The function checks only: `DeadLetterRetention < 0` (`:228-230`), the activation gate `Enabled && len(Bindings) == 0` (`:231-233`), and `validateAuditAnchor` (`:234-237`). `Timeout`/`RefreshSkew` are never read. It is called from the `validateRuntimeLimits` checks list (`:136`) — before `c.validateCommercialIntegrations()` (last in the same list), so a knob rejection fires ahead of binding-content validation |
| E2 | `internal/config/audit_governance.go:22,25` — field definitions | ✅ stands, line drift (`:34-35`): `Timeout time.Duration \`mapstructure:"timeout"\``, `RefreshSkew time.Duration \`mapstructure:"refresh_skew"\`` |
| E3 | `internal/connector/auditgovernance/oauth.go:71-72` — `RefreshSkew <= 0` → 30s silent default | ✅ exact. `defaultOAuthConfig` (`:66-72`) replaces `Timeout <= 0` with 5s (`:68-69`) and `RefreshSkew <= 0` with 30s (`:71-72`); it runs inside `NewOAuthTokenSource` (`:50`) before any source is built. Both replace **negative** values silently |
| E4 | `internal/connector/auditgovernance/publisher.go:75` — `Timeout <= 0` → 5s silent default | ✅ stands, line drift (`:73-74`). `defaultConfig` (`:51`) replaces `Timeout <= 0` with 5s; runs inside `NewPublisher` (`:34`). Negative values are silently replaced |
| E5 | `internal/config/validate.go:262-264` — `validSourceLimits` rejects negatives (precedent) | ✅ stands, line drift (`:407-409`). `validSourceLimits` returns `Timeout >= 0 && MaxBodyBytes >= 0 && MaxConcurrency >= 0 && MaxAttempts >= 0 && RetryBackoff >= 0`; `validateSourceEndpoint` rejects negatives with `"sources.%s timeout, limits, and retry values must not be negative"` (`:384-386`). The sources block already closes this class; governance is the odd block out on the B2-3 surface |
| E6 | Runtime consumers of the two knobs | ✅ verified wiring: `internal/app/audit.go:81` passes `PublicationTimeout: cfg.AuditGovernance.Timeout` to the callback service (lease math defaults `<= 0` → 5s at `internal/service/callback_audit.go:100`); `internal/app/audit.go:197` passes `Timeout: settings.Timeout, RefreshSkew: settings.RefreshSkew` into `auditgovernance.NewOAuthTokenSource`; `buildAuditPublishers` early-returns when `!cfg.AuditGovernance.Enabled` (`:165`) — the knobs are **inert when the relay is disabled** (the Enabled-gating of the new check is therefore safe) |
| E7 | Test patterns cited by the acceptance | ✅ all exist: `TestAuditGovernanceDeadLetterRetentionValidation` `config_test.go:279` (Load-level YAML + substring error assert + valid-leg assert); `TestLoadAuditGovernanceKnobEnvLegs` `config_test.go:312` (env-wins-over-file + env-cannot-bypass legs, incl. the lazy-viper-merge rule: the file MUST carry the default-valued key); `TestAuditGovernanceKnobGateMatrix` `commercial_test.go:134` (incl. defaults cell pinning `false`/`0`); helpers `writeTemp` `config_test.go:37`, `setCommercialSecrets` `commercial_test.go:328`, `commercialTestConfig` `commercial_test.go:334` |
| E8 | Shipped config unaffected | ✅ `configs/config.yaml:185-186` carries `timeout: 5s` / `refresh_skew: 30s` — positive, so `TestLoadShippedConfigFileAndEnvGate` (`config_test.go:511`) and every real-file Load stay green without editing the shipped file |
| E9 | AGENTS.md zero-value convention | ✅ "A `0` value means code default unless the key explicitly documents `0 = disabled`." A **negative** is never a documented default; the runtime `<= 0` replacements (E3/E4/E6) silently erase operator error with no log and no error |

## 2. Problem statement (current state)

The two B2-3 token-source tuning knobs — `audit_governance.timeout` (bounds each governance token fetch
and events-endpoint publish HTTP client; also seeds the drain's `publicationLease`) and
`audit_governance.refresh_skew` (pre-expiry refresh window pacing 401-invalidation refetches and
scope-missing backoff) — are the only negative-valued configuration accepted by `Load()` on the
enabled-governance surface:

1. **Negative values pass `Load()`.** `validateAuditGovernance` (E1) reads neither field, so
   `timeout: -1s` or `refresh_skew: -1s` (file or `AERO_ID_AUDIT_GOVERNANCE_*` env) validates cleanly.
2. **They are silently replaced at runtime.** All three consumer sites use `<= 0` defaults:
   `defaultOAuthConfig` (E3), `defaultConfig` (E4), `publicationLease` (E6). The operator's negative value
   disappears with no error, no log, no signal — exactly the silent-misconfig class the B2-6 gate family
   exists to prevent, and a direct violation of the AGENTS.md zero-value convention (E9): `0` is the
   documented "code default" sentinel, a negative is never one.
3. **The sources block already rejects this class** (E5): `sources.*.timeout` etc. fail `Load()` when
   negative. Governance is the odd block out on the token-source surface (B2-3), the knobs that pace
   `401`-invalidation refetches and scope-missing backoff.

Because the knobs are inert when the relay is disabled (E6 — `buildAuditPublishers` early-returns), the
new check is `Enabled`-gated: a disabled kill-switch config with negative knobs still loads, and only an
**enabled** relay (where the values actually reach the connector) rejects them. This mirrors the
direction's pinned acceptance and keeps the kill switch intact.

## 3. Requirements

### REQ-1 — An enabled governance relay rejects negative tuning knobs at `Load()`

**REQ-1.1** `validateAuditGovernance` (`internal/config/validate.go:227`) rejects
`Timeout < 0` with the stable dotted-path message `audit_governance.timeout must not be negative`
(message convention identical to `audit_governance.dead_letter_retention must not be negative`, `:229`).

**REQ-1.2** The same function rejects `RefreshSkew < 0` with
`audit_governance.refresh_skew must not be negative`.

**REQ-1.3** Both checks are **`Enabled`-gated** (only when `value.Enabled`): when the relay is disabled
(the kill switch), negative knob values still load — the knobs are inert (E6). `0` keeps loading and
means code default in both states, per the AGENTS.md zero-value convention; the connector defaults
(E3/E4/E6) are unchanged.

**REQ-1.4** The checks live inside `validateAuditGovernance`, which runs in the `validateRuntimeLimits`
list (`validate.go:136`) for every binary that calls `Load()`, **before**
`validateCommercialIntegrations` — so the knob error is not masked by binding-content errors, and viper's
env merge (which happens inside `Load`, before `validate`) cannot bypass it:
`AERO_ID_AUDIT_GOVERNANCE_TIMEOUT=-1s` / `AERO_ID_AUDIT_GOVERNANCE_REFRESH_SKEW=-1s` over a valid file
fail Load. Insertion point: after the activation-gate check (`:231-233`), before `validateAuditAnchor` —
existing error precedence for unbound enabled configs is preserved unchanged.

**REQ-1.5** Errors never echo raw values (consistent with the module's non-disclosure convention; the
messages above contain no value interpolation). No new config keys, no change to `configs/config.yaml`
(`timeout: 5s` / `refresh_skew: 30s` are already positive, E8), no change to the connector's
`<= 0` default semantics.

## 4. Acceptance criteria (Load-level unit tests, no Docker)

All new tests live in `internal/config/config_test.go` beside the cited patterns. Fixtures reuse the
`writeTemp` helper (`:37`) and the `setCommercialSecrets` helper for enabled cells; an enabled fixture
carries `enabled: true`, a non-empty `endpoint`, and one fully valid binding
(`token_url: https://sso.example/token`, `client_id`, `client_secret_env: TEST_AUDIT_SECRET`,
`scope: audit:event:write`, `resources: [audit-governance]`) so the activation gate and commercial
validation are satisfied and the **only** possible failure is the knob error (wrong-reason-pass
protection). Every error cell asserts the exact pinned message via `strings.Contains` (wrong-reason
protection identical to `TestAuditGovernanceDeadLetterRetentionValidation` `:279`).

| # | Cell | Pin |
|---|---|---|
| A1 | enabled + `timeout: -1s` | `Load` error contains `audit_governance.timeout must not be negative` |
| A2 | enabled + `refresh_skew: -1s` | `Load` error contains `audit_governance.refresh_skew must not be negative` |
| A3 | disabled + `timeout: -1s` / `refresh_skew: -1s` | `Load` OK (kill switch); assert the parsed struct carries `-1s` for both fields — proves the negative landed and was ignored by design, not silently dropped |
| A4 | env cannot bypass | File carries a valid enabled config with the default-valued key `timeout: 5s` (per the lazy-viper rule documented at `config_test.go:312-320`, the file MUST carry the key, never the env value); `AERO_ID_AUDIT_GOVERNANCE_TIMEOUT=-1s` → `Load` error contains the A1 message |
| A5 | defaults cell | `timeout: 0` / `refresh_skew: 0` (absent or explicit zero) on an enabled config → `Load` OK and merged struct has `Timeout == 0`, `RefreshSkew == 0` (0 = code default, pattern of the `TestAuditGovernanceKnobGateMatrix` defaults cell, `commercial_test.go:134`); extend that matrix's defaults cell to also assert `Timeout == 0 && RefreshSkew == 0` on `commercialTestConfig()` |
| A6 | regression pins | `go test ./internal/config/...` green incl. `TestLoadShippedConfigFileAndEnvGate` (`:511`), `TestAuditGovernanceDeadLetterRetentionValidation`, `TestLoadAuditGovernanceKnobEnvLegs`, `TestAuditGovernanceKnobGateMatrix`, `TestAuditGovernanceActivationGate` |

Maps to T-10.1 (config-layer gate family) and T-5 (env-leg family) in the predecessor design.

## 5. Non-goals

- **No connector changes**: the `<= 0` → 5s/30s defaults (E3/E4/E6) are the documented code-default
  contract and stay; the parity surface they form with `0` is pinned by A5, not by a new parity test.
- **No change to the `Enabled`-independent `dead_letter_retention` check** (`:228-230`): its
  Enabled-independence is deliberate and pinned by the existing knob-gate matrix; the new tuning-knob
  check is `Enabled`-gated because the knobs are inert when disabled (E6).
- **No new config keys, no env-var renames, no shipped-file edits**, and no runtime behavior change for
  any non-negative (or disabled) config.
- **No endpoint/source_prefix Load-gate work**: that is the sibling direction's surface
  (`docs/plans/2026-08-08-internal-config-governance-load-time-endpoint-source-prefix-gates-design.md`).
- **No integration tests**: the change is pure validation; unit tests only.

## 6. Implementation sketch (for the implementer, not normative)

In `validateAuditGovernance`, after the activation gate (`:233`) and before `validateAuditAnchor`:

```go
	// B2-3 tuning knobs: a negative value is never a documented default (the
	// AGENTS.md zero-value convention) and would be silently replaced at
	// runtime by the connector defaults (oauth.go:68-72, publisher.go:73-74)
	// and the drain lease (callback_audit.go:100). Gated on Enabled: when
	// the relay is off the knobs are inert (buildAuditPublishers early-
	// returns, app/audit.go:165), so the disabled kill switch still loads.
	if value.Enabled && value.Timeout < 0 {
		return fmt.Errorf("audit_governance.timeout must not be negative")
	}
	if value.Enabled && value.RefreshSkew < 0 {
		return fmt.Errorf("audit_governance.refresh_skew must not be negative")
	}
```

`validate.go` is 427 lines today; the addition keeps it under the 500-line gate.

## 7. Verification commands

```bash
make test              # includes the new internal/config unit tests — no Docker
make lint && make lint-strict && make check-file-sizes && make check-audit-swallow
make check-anchored-event-ids && make check-audit-manifest && make check-composition-root
make check-integration-compile && make build
```

No `make test-integration` requirement: the change touches no SQL, migrations, storage, connectors,
event ingestion, callback delivery, or transaction boundaries (all-new behavior is config validation
with unit tests).
