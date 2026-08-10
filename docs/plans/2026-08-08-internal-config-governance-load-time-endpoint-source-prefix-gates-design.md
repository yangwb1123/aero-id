# Design — Close the last fail-late gate: governance `endpoint` URL + `source_prefix` fail at `Load()`, not boot-wiring (T-10.1.10–T-10.1.13)

| | |
|---|---|
| Module (batch label) | `internal/config` (parity reaches `internal/connector/auditgovernance`; regression-proof is `internal/app` T-10.2e) |
| Input evidence | `docs/requirements/2026-08-08-internal-config-governance-load-time-endpoint-source-prefix-gates.md` (claims re-verified for this design, §0) |
| Predecessors | `docs/requirements/2026-08-07-internal-config-b2-6-activation-gate.md` (T-10.1.1–T-10.1.6 landed); `docs/plans/2026-08-08-internal-config-b2-6-activation-gate-completion-design.md` (T-10.1.7–T-10.1.9, T-10.2e/f, T-4 landed) |
| Date | 2026-08-08 |
| Status | **Draft design — evidence re-verified against the working tree; one material mechanism claim corrected (C1/C2, §0); rule set, error texts, and all acceptance cells preserved unchanged.** No DB migration, no connector change, no new config keys |

## 0. Verification verdict (evidence claims re-checked, untrusted → verified)

Baseline re-run at verification time: `go test ./internal/config/... -count=1` → `ok`. All symbols and
behaviors check out; the two line drifts the requirements already disclosed are confirmed; **one material
mechanism claim is wrong and is corrected in §1.2 before it reaches implementation.**

| # | Claim (evidence) | Verdict | Verified at |
|---|---|---|---|
| E1 | `validateAuditGovernance` checks only retention/bindings/anchor, never `Endpoint`/`SourcePrefix` | ✅ exact (drift confirmed: `validate.go:227-244`; `:149-162` is `validateStorage`/`validateRateLimit`/`validateStorageGC` at `:147/:154/:161`) | `validate.go:227-244` |
| E2 | `validateAuditBindings` endpoint non-empty only; `validAuditTokenURL` `:82-97`; URL check precedes `registerCredential` | ✅ exact | `commercial.go:56-80`, `:82-97` |
| E3 | `NewPublisher` → `secureEndpoint`/`validConfig`/`RawQuery`/nil-tokens → `ErrInvalidConfig`; `secureEndpoint` `types.go:117-126`; `allowedLoopbackHTTP` `:128-137`; `validSourcePrefix` `:99-111`; `defaultConfig` defaults never fill `SourcePrefix` | ✅ exact (it **trims** it at `publisher.go:53` — see C1) | `publisher.go:33-49`, `types.go:99-137` |
| E4 | `source_prefix` seam at `app/audit.go:180-186` (guard at `:185-187`), Enabled-gated (`:170`), **zero test pins** | ✅ exact (`grep -rn "source_prefix must be" internal/ --include='*_test.go'` → 0) | `app/audit.go:185-187` |
| E5 | `TestAuditTokenURLGateParityWithConnector` at `commercial_test.go:301`; shared `auditTokenURLCases()` `:229-257`; config test pkg already imports the connector (no cycle) | ✅ exact (import at `:10`; spec cited `:12` — closing paren line, immaterial) | read `commercial_test.go` |
| E6 | `TestAuditGovernanceGateMatrix` at `:79`; fixture `commercialTestConfig()` `:341-345` sets `Enabled:true, Endpoint:"https://audit.example"` and **no `SourcePrefix`** | ✅ exact (func at `:86`, comment `:79`; fixture gap confirmed) | `commercial_test.go:79-132`, `:341-345` |
| E7 | Fail-at-Load philosophy pinned at `config_test.go:146-149`; T-10.1.4 at `:151-178`; viper env plumbing `config.go:384-402`; knob env legs `:312-342` | ✅ exact | read `config_test.go`, `config.go:384-403` |
| E8 | `newAuditGovernanceFixture` at `audit_integration_test.go:507` (uses `:595/:714/:809/:947`); `auditGovernanceTestConfig` `:466-490` is the Load()-valid shape (`source_prefix: aero-id`, loopback flag, loopback endpoint) | ✅ exact | read `audit_integration_test.go:459-510` |
| E9 | `AERO_ID_AUDIT_GOVERNANCE_*` env reaches the merged struct (scalars) | ✅ exact — `endpoint`/`source_prefix`/`enabled`/`allow_insecure_loopback` all scalar `mapstructure` keys (`audit_governance.go:10/:11/:6/:36`) | read |

Supporting facts re-verified: all three binaries `Load()` before DB use — `cmd/server/main.go:22`,
`cmd/migrate/main.go:62`, `cmd/audit-replay/main.go:106` ✅; `configs/config.yaml:174-177` is
`enabled: false`, `endpoint: ""`, `source_prefix: aero-id` ✅; `auditGovernanceBadYAML`
(`config_test.go:124-144`) already carries `source_prefix: aero-id` + a valid https endpoint — the T-10.1.4
legs are unaffected by the new gates ✅; `TestBuildAuditStackRefusesEnabledZeroBindings` FM-02
byte-identity pattern at `app/audit_config_test.go:83-93` ✅; `commercial.go` 195 lines / `commercial_test.go`
346 lines ✅.

### C1 (material) — `" aero-id "` does **not** fail the connector; it boots and runs today

The requirements' REQ-1.5 rationale claims the whitespace-wrapped prefix "passes the seam but fails the
connector" (`validSourcePrefix` canonicality). **Empirically disproven.** Both connector constructors trim
before validating: `defaultConfig` (`publisher.go:53`) and `defaultOAuthConfig` (`oauth.go:67`). A probe test
driving `NewPublisher` with `Endpoint: "https://audit.example"` returned `nil` for `"aero-id"`, `" aero-id "`,
`"x"`, `"AERO-ID"`, `"aero-id-extra"` — the connector rejects only `""` and format-invalid-after-trim values
(e.g. `"aero id"`, >64 chars). Every runtime consumer of the prefix trims (`publisher.go` field copy,
`oauth.go` copy, `buildAuditScopeMissingHook` `app/audit.go:165`), so `" aero-id "` is fully normalized
end-to-end: **no boot failure exists for this value today.**

Consequences, all of which the rule set already survives:

1. The real fail-late class on this dimension is `TrimSpace(prefix) != "aero-id"` (e.g. `"x"`, `""`,
   `"AERO-ID"`, `"aero-id-extra"`, `"aero id"`): Load passes, then `buildAuditPublishers`' seam
   (`app/audit.go:185-187`) hard-fails boot **after** the ledger and callback repositories were already
   constructed (`buildAuditStack` order: `NewAuditLedgerRepository` → `NewCallbackRepository` →
   `buildAuditPublishers`). That is the fail-late bug to fix, and only `cmd/server` ever trips it —
   `cmd/migrate`/`cmd/audit-replay` never construct publishers and today tolerate the bad prefix entirely.
2. The intersection of the two boot predicates is **not** byte-exact `"aero-id"`: it is the whitespace-closure
   of it (seam accepts any value that trims to `"aero-id"`; the connector accepts the same after its own
   trim). Byte-exact `== "aero-id"` is a *strict subset* — a deliberate canonicality policy, not the exact
   intersection. Justification: (a) it eliminates the trim-normalization reliance at every consumer (a future
   consumer that forgets the trim would silently split the metric label — the exact class the seam exists
   for), and (b) the shipped config and every fixture already use byte-exact `"aero-id"`, so nothing
   operational loses.
3. The parity formula in R-1.7.2 (`configOk == connectorOk ∧ prefix == "aero-id"`) is **already correct**
   under the real behavior and now carries an explicit asymmetry cell: `" aero-id "` is config-rejected ∧
   connector-accepted. The pin must assert that asymmetry, or a future "fix" that trims the Load gate (making
   it match boot) goes green instead of red.

### C2 (design consequence) — the seam drift-lock leg may only use prefixes the seam rejects

T-10.1.13 leg 3 (byte-identity between the Load error and the seam error) cannot use `" aero-id "` — the
seam trims and does **not** error on it. Use `"x"` (or `""`): both layers error, byte-identically.

## 1. Design

### 1.1 API changes

**External/production API: none.** No new config keys, no proto, no HTTP/gRPC surface, no connector change,
no seam change, no migration. The only production delta is two Enabled-gated checks in the existing
`validateAuditBindings` (`commercial.go:56-80`) plus doc comments, and one test-fixture field. All three
binaries inherit the gate through the existing `Load()` → `validate()` → `validateRuntimeLimits` →
`validateCommercialIntegrations` chain (`validate.go:130-150`).

Exact code shape (inserted in `validateAuditBindings`, between the requires-endpoint check `:60-62` and the
per-binding loop `:63`):

```go
	// R-1.4.1: endpoint URL-shape gate. validAuditTokenURL is byte-identical
	// to the connector rules (secureEndpoint + RawQuery rejection); the eager
	// boot consumer is auditgovernance.NewPublisher, so Load must accept
	// exactly the set the connector accepts. Runs before the binding loop
	// (and therefore before registerCredential): a malformed endpoint can
	// never be masked by a missing/short secret env (D2 ordering, T-10.1.2
	// precedent). Enabled-gated by the early return above; the relay is never
	// constructed when disabled (app/audit.go:170). The raw value is never
	// echoed.
	if !validAuditTokenURL(value.Endpoint, value.AllowInsecureLoopback) {
		return fmt.Errorf("audit governance endpoint must be a valid https URL")
	}
	// R-1.5.1: source_prefix label-seam gate. Byte-exact mirror of the boot
	// seam (app/audit.go:185-187) extended to canonicality: any value other
	// than the byte-exact "aero-id" is rejected, including whitespace-wrapped
	// forms that boot normalizes today (C1) and format-valid values that only
	// the seam rejects. Error text is the seam's format string verbatim so
	// the two layers cannot drift (FM-02).
	if value.SourcePrefix != "aero-id" {
		return fmt.Errorf("audit governance source_prefix must be %q, got %q", "aero-id", value.SourcePrefix)
	}
```

Placement discipline (per the requirements §5): the checks live in `validateAuditBindings` — Enabled-gated by
its existing early return — **not** in `validateAuditGovernance` (`validate.go:227-244`), whose anchor and
negative-retention checks are deliberately Enabled-independent (the ledger dedup guard and purge run
regardless of relay state). Moving either check there would fail disabled+malformed configs at Load and
corrupt the kill switch. Endpoint-vs-prefix order between the two new checks is cosmetic (both precede the
loop); keep endpoint first so a config violating both reports the non-disclosing error.

Line budget: `commercial.go` 195 → ≈212 lines (≤500 gate ✅). `commercial_test.go` 346 → ≈381 with the
matrix extension (under the 400-line plan-a-split threshold; all new test files are sibling files per the
requirements §4).

### 1.2 Rule-reuse proof (endpoint gate == token-URL gate == connector gate)

Reusing `validAuditTokenURL` (`commercial.go:82-97`) for the endpoint is sound because the three rule sets
agree cell-for-cell:

| Rule component | `validAuditTokenURL` | `secureEndpoint` (`types.go:117-126`) | `NewPublisher` adds |
|---|---|---|---|
| parse error / empty host / userinfo / fragment | reject | reject | — |
| `RawQuery != ""` | reject | accept | reject (`publisher.go:36`) |
| scheme | `https` or loopback-`http` | `https` or loopback-`http` | — |
| loopback | `localhost`/`::1`/loopback IP ∧ flag | same (`allowedLoopbackHTTP` `:128-137`) | — |
| trimming | trims input | trims input | trims via `defaultConfig` |

The `RawQuery` divergence is closed by `NewPublisher`'s own check, so the composed connector decision is
byte-identical to `validAuditTokenURL`. Verified cases (`https://host?` ForceQuery → both accept; `?x=1` →
both reject; uppercase scheme `HTTP://` → both reject — scheme comparison is case-sensitive in both).

### 1.3 Compatibility constraints

| Surface | Impact | Action |
|---|---|---|
| `commercialTestConfig()` (`commercial_test.go:341-345`) | Enabled without `SourcePrefix` → new R-1.5.1 check rejects it | **Required fixture update:** add `SourcePrefix: "aero-id"`. Without it, every existing enabled-cell test in the commercial suite reddens — a false failure, not a behavior change |
| `configs/config.yaml` (`:174-177`) | `enabled: false`, `endpoint: ""`, `source_prefix: aero-id` | Unaffected: disabled skips both checks |
| `auditGovernanceBadYAML` (`config_test.go:124-144`) | Already byte-exact `source_prefix: aero-id` + valid https endpoint; only the binding `token_url` is bad | T-10.1.4 legs unchanged and green |
| `auditGovernanceTestConfig` (`app/audit_integration_test.go:466-490`) | Already the canonical Load()-valid shape | No change; T-10.2e and the other `newAuditGovernanceFixture` users (`:595/:714/:809/:947`) stay green |
| `TestBuildAuditStackRefusesEnabledZeroBindings` (`app/audit_config_test.go:83-93`) | Already `SourcePrefix: "aero-id"` | No change |
| `cmd/server` / `cmd/migrate` / `cmd/audit-replay` | All `Load()` before DB use (`main.go:22/:62/:106`) | No code change; the gate now protects all three — `cmd/migrate` and `cmd/audit-replay` will refuse a bad prefix/endpoint that today only `cmd/server`'s boot would catch |
| `scripts/testdata/composition-root/positive/main.go` | Governance disabled | Unaffected |
| Env surface | `AERO_ID_AUDIT_GOVERNANCE_{ENDPOINT,SOURCE_PREFIX,ALLOW_INSECURE_LOOPBACK,ENABLED}` already exist via the viper replacer; gates read the merged struct | No new env keys; env cannot bypass (R-1.6) |

**One intentional strictness break** (the only cell where a previously-bootable config becomes Load-invalid):
an enabled config with a whitespace-wrapped `source_prefix` (e.g. `" aero-id "`) boots and runs correctly
today (C1) and will now fail `Load()`. No shipped artifact uses this shape; the break is the point of the
byte-exact rule and is pinned as an asymmetry (T-10.1.13 leg 2). Values the seam already rejects (`"x"`,
`""`, `"AERO-ID"`, `"aero-id-extra"`) change *where* they fail (boot-wiring mid-`buildAuditStack` → Load),
not *whether* — except in `cmd/migrate`/`cmd/audit-replay`, which previously tolerated them and now refuse.

### 1.4 Failure modes

| # | Failure mode | Detection | Mitigation |
|---|---|---|---|
| FM-1 | Check moved into `validateAuditGovernance` (Enabled-independent) → disabled + malformed endpoint/prefix fails Load | T-10.1.10 cells ⑤⑥⑦ and T-10.1.12 leg ⑨ assert `nil` for disabled+malformed | Placement pinned by the disabled cells; comment documents the asymmetry with the anchor/retention checks |
| FM-2 | Endpoint check placed after the binding loop → missing/short secret env masks the endpoint error | Ordering cell (T-10.1.10 cell ⑧, added below): bad endpoint + short secret → endpoint message, never the credential message | Placement before `registerCredential` (D2 precedent, T-10.1.2) |
| FM-3 | Prefix gate trims (`TrimSpace(value.SourcePrefix) != "aero-id"`) → `" aero-id "` accepted at Load | T-10.1.12 leg ④ (`" aero-id "` file → error) and T-10.1.13 leg 2 asymmetry cell (`" aero-id "`: config rejects ∧ connector accepts) | Byte-exact comparison; comment states the strictness |
| FM-4 | Load error text drifts from the seam error text | T-10.1.13 leg 3 byte-identity (prefix `"x"`) | Same format string verbatim; FM-02 pattern (`app/audit_config_test.go:83-93`) |
| FM-5 | Connector rules drift (`secureEndpoint`/`validConfig`/`RawQuery` change) | T-10.1.13 leg 1 (`NewPublisher` parity, 16 cells) and leg 2 (6 prefix cells) | Parity pins force a coordinated change; the connector stays the source of truth |
| FM-6 | A config passes Load but still fails connector construction on a *different* dimension (e.g. `validConfig` fields) | Not a failure of this design: `defaultConfig` fills all other fields, and the T-10.2e e2e proves the Load()-valid shape constructs publishers | Documented; out of scope (no new connector behavior) |
| FM-7 | Operational surprise for `cmd/migrate`/`cmd/audit-replay` runs with a bad governance block that previously Loaded | Load now fails before any DB handle — the intended contract extension | Release note; grep for non-byte-exact `source_prefix` before rollout (§1.5) |

### 1.5 Migration steps

Config-only; **no database migration, no deploy ordering constraint.**

1. **Code.** Add the two checks to `validateAuditBindings` (`commercial.go`) exactly as in §1.1.
2. **Fixture.** Add `SourcePrefix: "aero-id"` to `commercialTestConfig()` (`commercial_test.go:341-345`).
3. **Tests.** Extend `TestAuditGovernanceGateMatrix` with the T-10.1.10 cells (incl. the FM-2 ordering cell);
   add `internal/config/audit_governance_gate_test.go` with T-10.1.11/T-10.1.12/T-10.1.13 legs 1-2;
   extend `internal/app/audit_config_test.go` with T-10.1.13 leg 3 (§2).
4. **Local gates.** `make test && make lint && make lint-strict && make check-file-sizes &&
   make check-audit-swallow && make build`; then `make test-integration` (T-10.2e/f + fixture users on real
   Postgres + real HTTP).
5. **Pre-rollout sweep.** `grep -rn "source_prefix" configs/ deployments/ 2>/dev/null` and visually confirm
   byte-exact `aero-id` (no surrounding whitespace, correct case) in every enabled block. The shipped
   `configs/config.yaml` is already exact; the sweep is for downstream config overlays.
6. **Release note.** One behavior-change cell: enabled governance configs with non-byte-exact
   `source_prefix` or a URL-invalid/`RawQuery`-carrying endpoint now fail `Load()` in all three binaries;
   previously `" aero-id "` booted (normalized at every consumer) and `"x"`-class prefixes failed only
   `cmd/server` boot-wiring.

## 2. Testable acceptance mapping (requirements acceptance → concrete pins)

All requirements acceptance cells are preserved; one ordering cell (T-10.1.10 ⑧) is added to pin FM-2, and
the parity formula is retained with the C1 asymmetry made explicit. Baseline before-state:
`go test ./internal/config/...` → `ok`.

| Requirement | Test (location) | Executable assertions |
|---|---|---|
| R-1.4.1 endpoint shape | **T-10.1.10** cells ①②③ (extend `TestAuditGovernanceGateMatrix`, `commercial_test.go:86`); **T-10.1.11** (`audit_governance_gate_test.go`, `auditEndpointCases()` = the T-10.1.3 16-cell set verbatim, both loopback flag values); **T-10.1.12** legs ①②⑤⑩ | enabled + `not-a-url` / `http://audit.example` (flag off) / `http://audit.example?x=1` → error contains `audit governance endpoint must be a valid https URL`; `https` any host, `https://host?` (ForceQuery), trimmed https, loopback http+flag → OK; positive control ⑩: YAML of the T-10.2e fixture shape → Load OK |
| R-1.4.2 non-disclosure | **T-10.1.11** non-disclosure assertion | for every rejecting cell, `!strings.Contains(err.Error(), rawEndpoint)` |
| R-1.4.3 kill switch | **T-10.1.10** ⑤⑥; **T-10.1.12** ⑨ | disabled + `not-a-url` / `http://audit.example` → nil (both through `validateCommercialIntegrations` and full `Load`) |
| R-1.4.4 ordering | **T-10.1.10** ⑧ (added): enabled + `Endpoint: "not-a-url"` + `TEST_AUDIT_SECRET=short` (secret env made invalid) | error contains the endpoint message, not `commercial OAuth binding contains invalid credentials` |
| R-1.5.1 prefix byte-exact | **T-10.1.10** ④⑦; **T-10.1.12** legs ③④⑩; **T-10.1.13** leg 2 | enabled + `"x"` → error contains `audit governance source_prefix must be "aero-id"`; file + `" aero-id "` → error (seam-trims/byte-exact gap); `"aero-id"` → OK; disabled + `"x"` → nil |
| R-1.5.2 error byte-identity | **T-10.1.13** leg 3 (extend `app/audit_config_test.go`, FM-02 pattern) | for prefix `"x"`: `config.Load` error string `==` `buildAuditPublishers` seam error string, byte-for-byte (C2: only seam-rejecting prefixes; `" aero-id "` must not be used — the seam trims and accepts it) |
| R-1.5.3 kill switch | **T-10.1.10** ⑦; **T-10.1.12** ⑨ | disabled + `"x"` → nil |
| R-1.6.1 env cannot bypass | **T-10.1.12** legs ⑥⑦⑧ (`t.Setenv` subtests, knob-env-leg pattern `config_test.go:312-342`) | env `ENDPOINT=http://audit.example` over valid file → error; env `SOURCE_PREFIX=x` over valid file → error; file-disabled bad endpoint + env `ENABLED=true` → error |
| R-1.6.2 flag parity | **T-10.1.12** leg ⑤ | file-enabled `endpoint: http://localhost` + env `ALLOW_INSECURE_LOOPBACK=true` → Load OK |
| R-1.6.3 env kill switch | **T-10.1.12** leg ⑨ | file-disabled bad endpoint/prefix, no env-enable → Load OK |
| R-1.7.1 endpoint parity | **T-10.1.13** leg 1 (`audit_governance_gate_test.go`; imports `auditgovernance`, precedent `commercial_test.go:10`) | per `auditEndpointCases()` cell: `(validateCommercialIntegrations err == nil) == (NewPublisher err == nil)` with `{Endpoint: cell, SourcePrefix: "aero-id", AllowInsecureLoopback: matched}` + non-nil in-test `TokenSource` fake (5 methods; `NewPublisher` rejects `tokens == nil` at `publisher.go:36`; no network at construction) |
| R-1.7.2 prefix parity | **T-10.1.13** leg 2 | per prefix cell (`"aero-id"`, `"x"`, `""`, `" aero-id "`, `"AERO-ID"`, `"aero-id-extra"`): `configOk == (connectorOk && prefix == "aero-id")`. **C1 asymmetry pinned explicitly:** `" aero-id "` must assert config-rejects ∧ connector-accepts (comment documents that boot normalizes it today and the gate is deliberately stricter) |
| R-1.7.3 T-10.2e regression | **T-10.2e** + `newAuditGovernanceFixture` users (`app/audit_integration_test.go:595/:714/:809/:947`, unchanged) | green — the Load()-valid fixture shape constructs publishers and delivers the first fact (integration tag) |

## 3. Done gate

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make build   # T-10.1.10-13 + all pre-existing config/commercial tests green with the one fixture update
make test-integration   # T-10.2e/f + fixture users green on real Postgres + real HTTP
```

Green = T-10.1.10–13 pass, the measured baseline (`go test ./internal/config/...` → `ok`) is preserved
modulo the intentional `commercialTestConfig()` fixture update, and T-10.2e (unchanged) proves the
Load()-valid shape constructs publishers. The corrected C1 rationale changes no acceptance cell; it changes
only the justification recorded in comments and in T-10.1.13 leg 2's explicit asymmetry assertion.
