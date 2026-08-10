# Requirements — Close the last fail-late governance gate: endpoint URL + `source_prefix` fail at `Load()`, not boot-wiring (T-10.1.10–T-10.1.13)

| | |
|---|---|
| Module (batch label) | `internal/config` (parity reaches `internal/connector/auditgovernance`; regression-proof is `internal/app` T-10.2e) |
| Source analysis | `docs/auto/analyses/internal-config-a932ee1e.json` (direction 1 of 3: governance endpoint URL + `source_prefix` seam must fail at `Load()`) |
| Predecessors | `docs/requirements/2026-08-07-internal-config-b2-6-activation-gate.md` (REQ-1–REQ-4, T-10.1.1–T-10.1.6 landed); `docs/plans/2026-08-08-internal-config-b2-6-activation-gate-completion-design.md` (T-10.1.7–T-10.1.9, T-10.2e/f, T-4 landed) |
| Date | 2026-08-08 |
| Status | Draft — every cited file/symbol re-verified against the repository working tree; unit baseline `go test ./internal/config/...` → `ok` at verification time |

## 1. Evidence verification status

All direction citations were re-checked against the working tree. Two line-number drifts found; both symbols
and all behavioral claims verified. The existing parity pattern to extend (T-10.1.6) is measured green.

| # | Claim (direction evidence) | Verification (working tree) |
|---|---|---|
| E1 | `internal/config/validate.go:149-162` — `validateAuditGovernance` gate checks only retention/bindings/anchor | ✅ symbol verified, **line drift → `validate.go:227-244`** (comment block `:215-226`; cited range `:149-162` is `validateRateLimit`/`validateStorageGC`). The validator reads `DeadLetterRetention` (`:228`), `Enabled`+`Bindings` (`:231-233`), and `validateAuditAnchor` (`:235-237`, defined `:256-273`). It **never reads `Endpoint` or `SourcePrefix`** — a malformed endpoint or non-`aero-id` prefix passes. Partially Enabled-*independent* by design (`:231` comment: the ledger dedup guard runs regardless of relay state) |
| E2 | `internal/config/commercial.go:60-62` — `validateAuditBindings` checks endpoint non-emptiness only | ✅ exact. `validateAuditBindings` at `commercial.go:56-80`; `if strings.TrimSpace(value.Endpoint) == "" \|\| len(value.Bindings) == 0` at `:60-62` (error `"audit governance requires an endpoint and tenant bindings"`). Per-binding checks: identity/scope/resources `:63-65`, `validAuditTokenURL` `:67-69`, `registerCredential` `:70-72` (D2 ordering: URL error precedes credential resolution). No endpoint-URL-shape check, no `SourcePrefix` check. `validAuditTokenURL` at `:82-97` is the twin gate that **already** fails at Load with connector parity (T-10.1.6) |
| E3 | `internal/connector/auditgovernance/publisher.go:33-38` — `NewPublisher`'s `secureEndpoint`/`validConfig` reject at construction | ✅ exact. `NewPublisher` at `publisher.go:33-38`: `secureEndpoint(config.Endpoint, config.AllowInsecureLoopback)` plus `tokens == nil`, `validConfig`, and `base.RawQuery != ""` → `ErrInvalidConfig`. `secureEndpoint` at `types.go:117-124` (parse err / empty host / userinfo / fragment → reject; scheme must be `https` unless `allowedLoopbackHTTP`), `allowedLoopbackHTTP` `:128-136` (`http` only when allowed ∧ hostname `localhost` or loopback IP), `validSourcePrefix` `:99-106` (non-empty, ≤64 chars, canonical, `[A-Za-z0-9._-]`, alnum first/last). `defaultConfig` (`publisher.go:41-72`) defaults ActorID/SchemaID/SchemaVersion/DataClassification/RetentionClass/Timeout — but **never** `SourcePrefix` |
| E4 | `internal/app/audit.go:110-114` — `source_prefix` seam fails boot | ⚠️ **line drift → `app/audit.go:180-186`** inside `buildAuditPublishers` (`:169`); cited range `:110-114` is `wireAuditServices`' panic block. The label-seam guard at `:180-186`: `if strings.TrimSpace(settings.SourcePrefix) != "aero-id" { return nil, fmt.Errorf("audit governance source_prefix must be %q, got %q", "aero-id", settings.SourcePrefix) }` — a boot-time hard failure, reachable only when `Enabled` (`:170` early return). No test references this guard today (`grep -rn "source_prefix must be" internal/ --include='*_test.go'` → empty) |
| E5 | `internal/config/commercial_test.go:301` — `TestAuditTokenURLGateParityWithConnector` (parity pattern to extend) | ✅ exact `:301-323`. Drives `auditgovernance.NewOAuthTokenSource` per cell of shared `auditTokenURLCases()` (`:229-257`), asserts accept/reject agreement with `validateCommercialIntegrations`. Config test package already imports the connector (`:12`) with no cycle — the same import is available for the new parity pin |
| E6 | `internal/config/commercial_test.go:79` — `TestAuditGovernanceGateMatrix` (matrix to extend) | ✅ exact `:79-132`. Table cells: enabled+empty bindings, enabled+empty/whitespace endpoint, disabled+empty, disabled+invalid token URL; each mutates `commercialTestConfig()` and calls `validateCommercialIntegrations()`. **Fixture gap:** `commercialTestConfig()` (`:341-345`) sets `Enabled: true, Endpoint: "https://audit.example"` and **does not set `SourcePrefix`** — an enabled-gated `source_prefix` rule requires adding `SourcePrefix: "aero-id"` to this fixture (see §5) |
| E7 | `internal/config/config_test.go:146-149` — pinned philosophy "the gate fails at Load(), before boot-wiring" | ✅ exact. Comment `:146-147` ("the gate fails at Load(), before boot-wiring"); `TestLoadAuditGovernanceTokenURLGate` (T-10.1.4) at `:151-183` — file-enabled bad TokenURL fails `Load`, env-enable cannot bypass, env loopback flag lands in the same merged struct the gate reads. Env plumbing verified at `config.go:384-402` (viper, prefix `AERO_ID`, `.`→`_`, `AutomaticEnv`, unmarshal then `validate()`), so scalar keys `endpoint`, `source_prefix`, `enabled`, `allow_insecure_loopback` are env-addressable |
| E8 | `internal/app/audit_integration_test.go:563` — `newAuditGovernanceFixture` (T-10.2e) | ⚠️ **line drift → `internal/app/audit_integration_test.go:507`** (uses at `:595/:714/:809/:947`). The fixture config `auditGovernanceTestConfig` (`:465-493`) is the canonical Load()-valid shape: `SourcePrefix: "aero-id"`, `AllowInsecureLoopback: true`, `Endpoint: <httptest http://127.0.0.1:port>`, one valid binding. T-10.2e (`TestFirstEventGovernanceE2E`, `:595`) drives `buildAuditStack` on real Postgres + real HTTP. This is the "REQ-2.1 fixture" the completion design (`:181`) guards only at e2e level — exactly the guard this direction moves to Load |
| E9 | `AERO_ID_AUDIT_GOVERNANCE_*` env cannot bypass | ✅ verified for the twin gate (E7) and for knobs (`TestLoadAuditGovernanceKnobEnvLegs`, `config_test.go:312-342`); the new gates must add the same legs for `ENDPOINT` and `SOURCE_PREFIX` (both scalars → env-addressable, unlike map-typed `bindings`) |

Baseline: `go test ./internal/config/...` → `ok` (all current commercial/config tests green — "existing cases stay green" is measured, not assumed).

## 2. Problem statement (current state)

The B2-6 gate family exists to make the fail-late misconfig class die at `Load()` — before any binary (server,
`cmd/migrate`, `cmd/audit-replay`) opens a DB handle. The activation gate (T-10.1.1), the TokenURL gate
(T-10.1.2–4) with cross-layer parity (T-10.1.6), and the knob matrix (T-10.1.7–9) are landed. Two enabled-gate
surfaces still fail **later**:

1. **`audit_governance.endpoint` shape is not validated at Load** (E1/E2). Only non-emptiness is checked
   (`commercial.go:60-62`). `enabled: true` + `endpoint: "not-a-url"`, or `http://audit-governance.internal`
   without `allow_insecure_loopback: true`, passes `Load()` and aborts boot at connector construction:
   `NewPublisher` → `secureEndpoint` → `ErrInvalidConfig` (`publisher.go:35-38`, `types.go:117-124`). A
   deploy-time boot failure on a Load()-valid config is exactly the silent-misconfig class the B2-6 gates
   exist to prevent.
2. **`audit_governance.source_prefix != "aero-id"` is not validated at Load** (E1/E2/E4). The label-seam
   guard (`app/audit.go:180-186`) hard-fails boot for any non-`aero-id` prefix (the metric label
   `aero_control_audit_scope_missing_total{source=...}` must never split), and the connector's
   `validSourcePrefix` (`types.go:99-106`) additionally rejects non-canonical values — note the seam
   *trims* while the connector requires canonicality, so `" aero-id "` passes the seam and then fails the
   connector. Nothing on this dimension fails at Load, and no test pins either rule.

Both contradictions are against the module's own pinned philosophy (`config_test.go:146-147`) and its own
twin gate: the TokenURL gate already fails at Load with a cross-layer parity pin (T-10.1.6). The completion
design's REQ-2.1 fixture only guards the happy path at e2e level (`docs/plans/2026-08-08-...-completion-design.md:181`:
"Gate-passing config fails connector construction | REQ-2.1 (fixture) | `buildAuditPublishers` returns error
on the Load()-valid config") — no Load-level guard exists.

**Scope discipline:** this spec adds two Load-time checks on the *already-existing* `endpoint`,
`source_prefix`, and `allow_insecure_loopback` fields, plus their test pins. No new config surface, no new
connector behavior, no change to `validateAuditGovernance`'s Enabled-independent anchor/retention semantics.
The rule is byte-simple: an enabled relay may not carry any config that boot would reject.

## 3. Requirements

### REQ-1.4 — Enabled governance `endpoint` is URL-validated at Load with `NewPublisher`-parity rules

**Rationale.** `NewPublisher` (`publisher.go:33-38`) is the eager boot consumer of `Endpoint` +
`AllowInsecureLoopback`; the config gate must accept exactly the set the connector accepts, so no
Load()-valid config can fail boot construction on this dimension.

**Rule (R-1.4.1).** In `validateAuditBindings` (`commercial.go:56`), after the existing non-empty check
(`:60-62`) and before the per-binding loop, reject when the endpoint fails the connector's URL rules. The
endpoint rules are byte-identical to the token-URL rules already implemented in `validAuditTokenURL`
(`commercial.go:82-97`) — both consumers call the same `secureEndpoint` and both additionally reject
`RawQuery != ""` (`oauth.go:81-83`, `publisher.go:35-38`). **Implementation: reuse `validAuditTokenURL`
for the endpoint** — one rule, one implementation, no drift surface between the two URL gates. Cells
(identical to the pinned T-10.1.3 matrix): `https` any host → valid; `http` loopback (`localhost` /
`127.0.0.1` / `::1`) → valid iff `AllowInsecureLoopback`; `http` non-loopback → invalid at both flag
values; `not-a-url`, `ftp://`, userinfo, fragment, `?x=1` → invalid; `https://host?` (ForceQuery, empty
`RawQuery`) → valid; surrounding whitespace trimmed → valid.

**Rule (R-1.4.2).** Error text is stable and non-disclosing: `"audit governance endpoint must be a valid
https URL"` — the raw endpoint value must never appear (same convention as the token-URL gate, T-10.1.2 F4).

**Rule (R-1.4.3, kill switch).** The check is Enabled-gated: `validateAuditBindings` already early-returns
on `!value.Enabled` (`commercial.go:57-59`), and the connector is never constructed when disabled
(`app/audit.go:170`). Disabled + malformed endpoint → Load OK.

**Rule (R-1.4.4, ordering).** The check runs before the per-binding loop, so it precedes
`registerCredential` — a malformed endpoint can never be masked by a missing/short secret env (D2 ordering
precedent: T-10.1.2).

### REQ-1.5 — Enabled governance `source_prefix` is seam-validated at Load

**Rationale.** Boot fails on this dimension in two layers with different predicates: the label-seam guard
(`app/audit.go:180-186`) requires `TrimSpace(source_prefix) == "aero-id"`; the connector (`validSourcePrefix`,
`types.go:99-106`) requires a canonical value. The Load gate must accept exactly the intersection — which is
the byte-exact value `"aero-id"` (canonical `"aero-id"` passes both layers; `" aero-id "` passes the seam but
fails the connector; `"x"` passes the connector format but fails the seam; empty fails both).

**Rule (R-1.5.1).** In `validateAuditBindings`, after the endpoint check and before the per-binding loop,
reject when `value.SourcePrefix != "aero-id"` (byte-exact, no trimming). This subsumes both boot-layer
predicates: canonicality (connector) and seam equality (app). Enabled + `""`, `"x"`, `" aero-id "`,
`"AERO-ID"`, `"aero-id-extra"` → Load error; `"aero-id"` → Load OK.

**Rule (R-1.5.2).** Error text is byte-identical to the boot seam error
(`app/audit.go:186`): `"audit governance source_prefix must be \"aero-id\", got %q"` with the raw value —
the same format string, so the Load gate and the boot re-check cannot drift silently (FM-02 precedent: the
activation-gate error is byte-identical across layers, const at `app/audit.go:42` + pin at
`audit_config_test.go:89-93`).
The value is a config field, not a credential; echoing it matches the existing seam and keeps byte-identity
pinable.

**Rule (R-1.5.3, kill switch).** Enabled-gated, same as R-1.4.3: disabled + any `source_prefix` → Load OK.

### REQ-1.6 — Env overrides cannot bypass either gate

**Rule (R-1.6.1).** `AERO_ID_AUDIT_GOVERNANCE_ENDPOINT` and `AERO_ID_AUDIT_GOVERNANCE_SOURCE_PREFIX` are
scalar keys and land in the same viper-merged struct the gate reads (`config.go:384-402`). Env values that
violate R-1.4.1/R-1.5.1 over an otherwise valid file → Load error. Env-enable
(`AERO_ID_AUDIT_GOVERNANCE_ENABLED=true`) over a file-disabled config carrying a bad endpoint or bad
`source_prefix` → Load error (pattern: T-10.1.4 env-enable leg, `config_test.go:157-161`).

**Rule (R-1.6.2, flag parity leg).** `AERO_ID_AUDIT_GOVERNANCE_ALLOW_INSECURE_LOOPBACK=true` over a
file-enabled `http://localhost` endpoint → Load OK, exactly enabling what the connector enables (pattern:
T-10.1.4 loopback leg, `config_test.go:163-166`).

**Rule (R-1.6.3, kill switch via env).** File-disabled + bad endpoint/prefix + no env-enable → Load OK.

### REQ-1.7 — Cross-layer parity pins: config rules == connector construction rules

**Rule (R-1.7.1, endpoint parity).** For every cell of a shared endpoint URL matrix (the R-1.4.1 cell set),
the config gate decision equals `auditgovernance.NewPublisher`'s decision on the same
`(Endpoint, AllowInsecureLoopback)` pair with an otherwise-valid connector config — pattern
`TestAuditTokenURLGateParityWithConnector` (T-10.1.6), but driven against the **actual boot consumer**
(`NewPublisher`, which additionally enforces `validConfig` and `base.RawQuery == ""`) instead of
`NewOAuthTokenSource`.

**Rule (R-1.7.2, prefix parity).** For every cell of a source-prefix matrix (`"aero-id"`, `"x"`, `""`,
`" aero-id "`, `"AERO-ID"`, `"aero-id-extra"`), the config gate decision equals
`NewPublisher`-accepts ∧ prefix == `"aero-id"` (connector format rule ∧ seam policy). This pins that the
Load gate is exactly the intersection of the two boot layers.

**Rule (R-1.7.3).** T-10.2e (`TestFirstEventGovernanceE2E`) stays green unchanged: the fixture shape
(`auditGovernanceTestConfig`, `audit_integration_test.go:466-493` — `http://127.0.0.1` + loopback flag +
`source_prefix: aero-id`) is the canonical Load()-valid shape, and its `buildAuditStack` → `NewPublisher`
success proves Load()-valid config constructs publishers.

## 4. Test plan

All cells preserve the supplied acceptance checks. New tests live in a sibling file
`internal/config/audit_governance_gate_test.go` (commercial_test.go is at 346 lines; the matrix extension
alone would push it past the 400-line split threshold — the plan's own "or new sibling" option,
completion-design `:187`).

| Test | Maps to | Location | Cells / shape |
|---|---|---|---|
| **T-10.1.10** — matrix extension | REQ-1.4/1.5 (T-10.1 family) | extend `TestAuditGovernanceGateMatrix` (`commercial_test.go:79`) | Seven new cells over `commercialTestConfig()` (now carrying `SourcePrefix: "aero-id"`), each varying exactly one dimension, calling `validateCommercialIntegrations()`: ① enabled + `Endpoint: "not-a-url"` → error contains `audit governance endpoint must be a valid https URL`; ② enabled + `Endpoint: "http://audit.example"` (loopback flag false) → same error; ③ enabled + `Endpoint: "http://audit.example?x=1"` → same error (RawQuery, `NewPublisher` parity); ④ enabled + `SourcePrefix: "x"` → error contains `audit governance source_prefix must be "aero-id"`; ⑤ disabled + `Endpoint: "not-a-url"` → nil (kill switch); ⑥ disabled + `Endpoint: "http://audit.example"` → nil; ⑦ disabled + `SourcePrefix: "x"` → nil |
| **T-10.1.11** — endpoint scheme/loopback matrix | REQ-1.4 (T-10.1 family) | `internal/config/audit_governance_gate_test.go` | `auditEndpointCases()`: the T-10.1.3 cell set verbatim (16 cells incl. `https bare query` ForceQuery pin and `trimmed https`), asserted through `validateCommercialIntegrations` at both `AllowInsecureLoopback` values, plus a non-disclosure assertion that the error never contains the raw endpoint |
| **T-10.1.12** — Load-level + env legs | REQ-1.6 (T-5 env-legs family; pattern `TestLoadAuditGovernanceTokenURLGate`, T-10.1.4) | `internal/config/audit_governance_gate_test.go` (or extend `config_test.go`) | Full-YAML `Load()` legs: ① file-enabled + `endpoint: not-a-url` → error; ② file-enabled + `endpoint: http://audit.example` → error; ③ file-enabled + `source_prefix: x` → error; ④ file-enabled + `source_prefix: " aero-id "` → error (seam-trims/connector-canonical gap); ⑤ file-enabled + `endpoint: http://localhost` + env `AERO_ID_AUDIT_GOVERNANCE_ALLOW_INSECURE_LOOPBACK=true` → Load OK (flag parity); ⑥ env `AERO_ID_AUDIT_GOVERNANCE_ENDPOINT=http://audit.example` over a valid file → error; ⑦ env `AERO_ID_AUDIT_GOVERNANCE_SOURCE_PREFIX=x` over a valid file → error; ⑧ file-disabled bad endpoint + env `AERO_ID_AUDIT_GOVERNANCE_ENABLED=true` → error (env-enable cannot bypass); ⑨ file-disabled bad endpoint/prefix, no env → Load OK (kill switch); ⑩ **positive control**: YAML serializing the T-10.2e fixture shape (loopback http endpoint + `allow_insecure_loopback: true` + `source_prefix: aero-id` + one valid binding) → Load OK — makes "Load()-valid config constructs publishers" measured at Load, not just at e2e |
| **T-10.1.13** — cross-layer parity | REQ-1.7 (pattern `TestAuditTokenURLGateParityWithConnector`, T-10.1.6) | `internal/config/audit_governance_gate_test.go` (imports `auditgovernance`; no cycle — precedent `commercial_test.go:12`) | **Leg 1 (endpoint):** per `auditEndpointCases()` cell, assert `(validateCommercialIntegrations err == nil) == (NewPublisher err == nil)`, driving `NewPublisher` with `{Endpoint: cell, SourcePrefix: "aero-id", AllowInsecureLoopback: matched}` + a non-nil token source (minimal in-test fake implementing the 5-method `TokenSource` interface — `NewPublisher` rejects `tokens == nil` at `publisher.go:36`; no network occurs at construction). **Leg 2 (prefix):** per prefix cell, assert `configOk == (connectorOk && prefix == "aero-id")` where `connectorOk` = `NewPublisher` success on the same prefix. **Leg 3 (seam drift lock):** an `internal/app` test (extend `audit_config_test.go`) asserting that `config.Load`'s source_prefix error for a given bad prefix is **byte-identical** to the `buildAuditPublishers` seam error for the same prefix (FM-02 pattern, `audit_config_test.go:89-93`) — pins R-1.5.2 across the package boundary that neither package can import the other |
| **T-10.2e (regression)** | REQ-1.7.3 | `internal/app/audit_integration_test.go:595` (unchanged) | `TestFirstEventGovernanceE2E` + the other `newAuditGovernanceFixture` users (`:714/:809/:947`) stay green — the Load()-valid fixture shape still constructs publishers and delivers the first fact |

Ordering guard: T-10.1.12 legs ⑥⑦ must run in subtests with `t.Setenv` (no cross-leg leakage), following
`TestLoadAuditGovernanceKnobEnvLegs` (`config_test.go:312-342`).

## 5. Compatibility and fixture updates (measured)

| Surface | Impact | Action |
|---|---|---|
| `commercialTestConfig()` (`commercial_test.go:341-345`) | `Enabled: true` without `SourcePrefix` → new R-1.5.1 check rejects it | **Required fixture update:** add `SourcePrefix: "aero-id"` (in-scope: the fixture is the enabled-gate canonical shape). Without it, T-10.1.10 and every existing cell of T-10.1.1/T-10.1.7 redden — a false failure, not a behavior change |
| `configs/config.yaml` | `enabled: false`, `endpoint: ""`, `source_prefix: aero-id` | Unaffected: disabled skips both checks; even the enabled shape would be valid (https/non-loopback rule applies only to the *configured* endpoint — `""` is already rejected by the requires-endpoint gate when enabled) |
| `auditGovernanceTestConfig` (`app/audit_integration_test.go:466-493`) | Already `SourcePrefix: "aero-id"` + loopback flag + loopback endpoint | Load()-valid under the new gates; no change |
| `TestBuildAuditStackRefusesEnabledZeroBindings` (`app/audit_config_test.go:83-93`) | Already `SourcePrefix: "aero-id"` | No change |
| `TestAuditGovernanceKnobGateMatrix` / `TestAuditGovernanceTokenURLSchemeMatrix` / `TestAuditTokenURLGateParityWithConnector` / `TestAuditGovernanceRejectsInvalidTokenURLBeforeCredentials` | All mutate `commercialTestConfig()` | Green once the fixture gains `SourcePrefix`; the https endpoint in the fixture passes R-1.4 |
| `cmd/server`, `cmd/migrate`, `cmd/audit-replay` | All `Load()` before DB use | Gate now protects all three binaries at Load (the stated goal); no code change |
| `scripts/testdata/composition-root/positive/main.go` | Disables governance | Unaffected |
| `validateAuditGovernance` (`validate.go:227`) | Anchor + negative-retention checks stay Enabled-independent (documented `:231`); the two new checks are Enabled-gated | **Deliberate asymmetry**, documented in-code: endpoint/prefix are connector-construction concerns that do not exist when the relay is disabled (`app/audit.go:170`); anchor/retention affect the ledger dedup guard and purge, which run regardless |

Implementation placement: both new checks go in `validateAuditBindings` (`commercial.go:56-80`) — beside
`validAuditTokenURL`, Enabled-gated by the existing early return, and reached from `validate()` →
`validateRuntimeLimits` → `validateCommercialIntegrations` in every binary. Do **not** put them in
`validateAuditGovernance`: that validator is deliberately Enabled-independent and its tests
(`TestAuditGovernanceKnobGateMatrix` rejection cells) call it directly; an Enabled-gated check there would
corrupt the kill-switch semantics and the entry-point-split pin (completion-design `:239`). Endpoint
validation reuses `validAuditTokenURL` unchanged; the only new non-test code is one byte-exact
`SourcePrefix == "aero-id"` predicate plus its error, keeping `commercial.go` at ~200 lines (≤500 gate).

## 6. Out of scope (explicit)

- No new config fields (no `scope_missing_alert` knobs — that is direction 3 of the source analysis).
- No connector changes: `secureEndpoint`, `validSourcePrefix`, `defaultConfig` stay as-is; parity pins *to*
  them.
- No change to `validateAuditGovernance`'s anchor/retention Enabled-independence (T-10.1.7 cells keep their
  semantics).
- No change to the app seam guard (`app/audit.go:180-186`) — it remains as the composition-root second line
  for in-code configs; the Load gate mirrors it and the drift lock (T-10.1.13 leg 3) pins agreement.
- No gRPC/idempotency/notification surface, no runtime behavior of the drain worker.

## 7. Done gate

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make build   # config unit gates
make test-integration   # T-10.2e/f + T-10.1.8/9 + the new fixture users stay green on real Postgres + real HTTP
```

Green = T-10.1.10–13 pass, all pre-existing config/commercial tests pass with the one fixture update, and
T-10.2e (unchanged) proves the Load()-valid shape constructs publishers. The measured baseline
(`go test ./internal/config/...` → `ok`) is the before-state for the regression claim.
