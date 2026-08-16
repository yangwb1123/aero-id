# Design — Enforce the Secrets Contract at `Load()`: Prod Hardening Gate (`app.env=prod`)

- **Date:** 2026-08-15
- **Module:** `configs` (`internal/config`, `configs/config.yaml`)
- **Direction:** Enforce the secrets contract at `Load()`: reject committed placeholder secrets and dev-only security defaults when `app.env=prod`
- **Status:** Design (spec-implementation-ready; no repository modification in this document)
- **Evidence run:** requirements spec `CONFIG-PROD-HARDENING-01` (verified independently below)

---

## 1. Verification verdict (untrusted evidence → repository facts)

Every cited claim was re-checked against the working tree (HEAD `d51faa6`; the evidence's `dbbea33+` base has minor commit drift only — all cited lines match the current tree). **Verdict: Verified — all 14 evidence claims (E1–E14) confirmed.** Two additional repository facts beyond the evidence are recorded as F1–F2; neither changes the mechanism, both affect scope documentation.

| # | Claim | Verification result |
|---|---|---|
| E1 | `configs/config.yaml:230` — root `signing_key: change-me-please-very-long-random` | **Confirmed** (exact line match). |
| E2 | `configs/config.yaml:173` — `audit_ledger.signing_key: change-me-please-very-long-audit-key` with "local-dev placeholder; override with AERO_ID_AUDIT_LEDGER_SIGNING_KEY in production" comment | **Confirmed**. |
| E3 | `configs/config.yaml:89` `password: aero`; `:104` `authz_required: false` + "production should enable" comment; `:105` `authz_legacy_fallback: true`; `:109` `insecure: true` | **Confirmed** (line numbers exact). |
| E4 | `configs/config.yaml:1` — `app: { name: aero-id, env: dev, ... }` | **Confirmed**. |
| E5 | `validateRequired` (`validate.go:54-68`) non-empty/jwks checks only; `validateAuditLedger` (`:205-215`) length-only (≥ 32 bytes); no `prod` predicate anywhere in `internal/config` | **Confirmed** (427-line file; tree grep for `prod` finds no env predicate). |
| E6 | `validate()` order: `applyDefaults` → `validateRequired` → `validateSourcesAndRegions` → `validateRuntimeLimits` → `validateTransport` | **Confirmed** (`validate.go:10-21`). |
| E7 | `App.Env` sole non-test consumer is `internal/app/new.go:87` (`Environment: cfg.App.Env,`) | **Confirmed** (tree-wide grep: only occurrence). |
| E8 | `Load()` (`config.go:384-401`): `SetEnvPrefix("AERO_ID")`, replacer `.`→`_`, `AutomaticEnv`, then `ReadInConfig` → `Unmarshal` → `validate()` | **Confirmed**. Env merges into the struct before validation. |
| E9 | `TestLoadShippedConfigFileAndEnvGate` at `config_test.go:511` pins governance knobs + env-merge-before-validate semantics | **Confirmed** (also pins `AERO_ID_AUDIT_GOVERNANCE_ENABLED=false` first — the ambient-CI pattern to mirror). |
| E10 | Root `signing_key` signs local-storage download URLs (`storage/local.go:114` `presignHMAC(..., l.cfg.SigningKey)`, `:120` `verifyPresignedURL`) | **Confirmed** (lines 114/120 exact). |
| E11 | `audit_ledger.signing_key` underpins tamper evidence (`repository/audit_ledger.go:199` `fact.Signature = sign(r.signingKey, fact.RecordHash)`, `:310` `verifyAuditRow(r.signingKey, ...)`) | **Confirmed**. |
| E12 | `sso.insecure`/`authz_required`/`authz_legacy_fallback` live switches (`config.go:264-289` SSOConfig; consumed `new.go:97-108` TLS + `:205-206` `DelegatingPermissionChecker`) | **Confirmed**. |
| E13 | No fixture sets `env: prod`; shipped audit placeholder reused as a *valid* fixture in 8 config tests + 1 concurrency test | **Confirmed** — `config_test.go:32,93,215,282,315,351,417,450` and `concurrency_test.go:33`; all non-prod (`env: test` or none). |
| E14 | Patterns to mirror: `TestLoadAuditLedgerSigningKeyValidation` (`:92`, table-driven pinned-substring), `TestLoadAuditGovernanceKnobEnvLegs` (`:312`, env-leg), `writeTemp` (`:43`) | **Confirmed**. |

**Additional verified facts (F1–F2, beyond the evidence):**

- **F1 — k8s deployment manifest uses `env: production`, not `prod`:** `deployments/k8s/regional-cell.yaml:20` embeds a `config.yaml` with `app.env: production`, and its ConfigMap commits `CHANGE_ME_*` placeholder secrets (`signing_key: "CHANGE_ME_SIGNING_KEY_32_BYTES_MINIMUM"` at `:91`, `audit_ledger_signing_key: "CHANGE_ME_AUDIT_LEDGER_KEY_32_BYTES_MINIMUM"` at `:94`, etc.) wired via `secretKeyRef` from a `aero-id-secrets` Secret. Consequences documented in §9: (a) the literal `prod` predicate does **not** fire for the k8s path (`production` ≠ `prod`); (b) the `CHANGE_ME_*` literals are outside the exact-literal deny set for the two shipped values. Both are accepted residuals of the mechanism, recorded so the reader can make an informed scope call.
- **F2 — related but distinct prior design exists:** `docs/designs/2026-08-16-reject-well-known-placeholder-secrets-design.md` covers an always-on deny-list (any env) + shipped-key regeneration + prefix-family denial, and does **not** gate SSO booleans. This direction is the complementary env-scoped gate. Interplay in §9: the two designs are compatible as written (this gate's key checks become redundant-but-harmless if the always-on deny-list lands first); this design is additive and needs no change to that one.

---

## 2. Problem and threat model

1. **Publicly-known HMAC keys boot under `env: prod`.** The shipped file commits `signing_key: change-me-please-very-long-random` (E1) and `audit_ledger.signing_key: change-me-please-very-long-audit-key` (E2). The audit key HMAC-signs every ledger `record_hash` (E11); the root key signs local-storage download URLs (E10). Anyone with repo access can forge presigned download URLs within validity windows and fabricate ledger facts that pass chain verification. The yaml comment itself says the audit key is a "local-dev placeholder" that must be overridden "in production" — but nothing enforces it: `validateRequired` accepts any non-empty key (E5).
2. **Dev-only SSO posture boots under `env: prod`.** `sso.insecure: true` (no TLS verification of the Snaplink gRPC endpoint), `sso.authz_required: false` (Snaplink `Authorizer.Check` never invoked), `sso.authz_legacy_fallback: true` (local fallback authorization) — the exact fail-open posture the yaml comments call dev-only (E3, E12).
3. **`app.env` is inert.** The only consumer is tracing metadata (`new.go:87`, E7). No validation step reads it, so a deployment booting `env: prod` gets zero prod-specific scrutiny.

This design adds one deterministic gate at `Load()`: when the merged `app.env == "prod"`, reject the two shipped placeholder HMAC keys and require the production SSO posture. `Load()` merges `AERO_ID_*` env before validation (E8), so the gate reads the merged config: env cannot bypass it, and env overrides are the documented remediation path.

---

## 3. Design decisions

### D1 — Prod predicate: literal `prod`, no normalization

The gate activates iff `strings.TrimSpace(c.App.Env) == "prod"`. Any other value — empty, `dev`, `test`, `staging`, `production` — keeps current behavior. No env-value normalization is introduced; `prod` is matched literally, matching the shipped lowercase convention (E4). **F1 consequence (documented, accepted):** the k8s manifest's `app.env: production` does not activate the gate; operators who want it must set `app.env: prod` or `AERO_ID_APP_ENV=prod` (§9).

### D2 — Placeholder detection: exact-literal after trim, compiled-in constants

```go
const (
	// shippedSigningKeyPlaceholder is the root signing_key committed in the
	// shipped configs/config.yaml. It signs local-storage download URLs; the
	// value is public repository knowledge.
	shippedSigningKeyPlaceholder = "change-me-please-very-long-random"

	// shippedAuditLedgerKeyPlaceholder is the audit_ledger.signing_key
	// committed in the shipped configs/config.yaml. It HMAC-signs every
	// ledger record_hash; the value is public repository knowledge.
	shippedAuditLedgerKeyPlaceholder = "change-me-please-very-long-audit-key"
)
```

Both are package-private in `internal/config`. Matching is `strings.TrimSpace(key) == literal` — exact-literal, **not** prefix-based (`change-me…` families are explicitly out of scope per the accepted mechanism; §9). The constants may be referenced by both the validator and the regression test (same package).

### D3 — Check ordering: FR-2 → FR-3 → FR-4, after `validateRequired`

`validate()` gains exactly one step between `validateRequired` and `validateSourcesAndRegions`:

```go
func (c *Config) validate() error {
	c.applyDefaults()
	if err := c.validateRequired(); err != nil {
		return err
	}
	if err := c.validateEnvHardening(); err != nil {
		return err
	}
	if err := c.validateSourcesAndRegions(); err != nil {
		return err
	}
	// ... unchanged
}
```

Inside the step, checks run FR-2 (root key) → FR-3 (audit key) → FR-4 (SSO booleans), first failure wins. Running after `validateRequired` keeps the pinned `signing_key is required` precedence (a missing root key errors before the hardening gate, regardless of other placeholder values). The existing ≥ 32-byte audit-key check (`validateAuditLedger`) runs later in `validateRuntimeLimits` and is unchanged and independent.

### D4 — Mechanism and placement: sibling file `internal/config/validate_hardening.go`

New unexported method, ~35 lines including doc comment:

```go
// validateEnvHardening enforces the secrets contract when app.env=prod: the
// two shipped placeholder HMAC keys and the dev-only SSO posture are rejected
// so a prod boot can never run with publicly-known credentials or fail-open
// authorization. It runs after validateRequired (missing keys keep their
// pinned "required" errors) and reads the merged config, so AERO_ID_* env
// overrides are the remediation path and cannot bypass the gate. Errors name
// the field and its override env var; they never echo the secret value.
func (c *Config) validateEnvHardening() error {
	if strings.TrimSpace(c.App.Env) != "prod" {
		return nil
	}
	if strings.TrimSpace(c.SigningKey) == shippedSigningKeyPlaceholder {
		return fmt.Errorf("signing_key must not be the shipped placeholder when app.env=prod (override with AERO_ID_SIGNING_KEY)")
	}
	if strings.TrimSpace(c.AuditLedger.SigningKey) == shippedAuditLedgerKeyPlaceholder {
		return fmt.Errorf("audit_ledger.signing_key must not be the shipped placeholder when app.env=prod (override with AERO_ID_AUDIT_LEDGER_SIGNING_KEY)")
	}
	if c.SSO.Insecure {
		return fmt.Errorf("sso.insecure must be false when app.env=prod")
	}
	if !c.SSO.AuthZRequired {
		return fmt.Errorf("sso.authz_required must be true when app.env=prod")
	}
	return nil
}
```

Placement decision: `validate.go` is 427/500 lines (E5) and already past the "plan a split at 400 lines" guidance; the evidence's FR-8 makes the sibling file mandatory if `validate.go` would cross the cap. A sibling file is cleaner and keeps `validate.go` untouched. New file metrics: function ~35 lines (funlen ≤ 50), cyclomatic 5 (cyclop ≤ 10), file well under 500 — all gates satisfied.

### D5 — Error contract: field path + override env var, never the value

Errors name the config field path and the remediation env var; they never echo the secret value (consistent with the documented "Errors never echo the raw value" convention in `validateAuditAnchor`). The pinned substrings:

- `signing_key must not be the shipped placeholder when app.env=prod`
- `audit_ledger.signing_key must not be the shipped placeholder when app.env=prod`
- `sso.insecure must be false when app.env=prod`
- `sso.authz_required must be true when app.env=prod`

These are byte-exact test contracts (acceptance legs assert them as substrings, so rewording is a breaking test change — intentional).

### D6 — SSO gating scope

Under prod: `sso.insecure` must be `false`; `sso.authz_required` must be `true` (checked in that order). `sso.authz_legacy_fallback` is **not** gated — it is documented "compatibility only" with a deliberate migration path at `new.go:205-206` (§9). `database.password` is **not** gated (§9).

### D7 — Env posture

Because `Load()` runs the viper env merge before `validate()` (E8), the gate reads the merged struct:
- File `env: dev` + `AERO_ID_APP_ENV=prod` → predicate holds on merged config → placeholder keys fail (acceptance leg 6).
- `AERO_ID_SIGNING_KEY`, `AERO_ID_AUDIT_LEDGER_SIGNING_KEY`, `AERO_ID_SSO_INSECURE`, `AERO_ID_SSO_AUTHZ_REQUIRED` overrides land before validation and satisfy the gate — the intended remediation path.

### D8 — No shipped-file changes

The shipped `configs/config.yaml` stays byte-identical (`env: dev` → gate inactive, positive shipped-file leg E9 stays green). No regeneration, no new config keys, no schema change. Runtime (post-`Load`) enforcement is out of scope.

---

## 4. API changes

| Surface | Change | Compatibility |
|---|---|---|
| `internal/config.Load(path)` behavior | Under merged `app.env == "prod"`, rejects the two shipped placeholder HMAC keys and dev-only SSO posture with new pinned error texts | Signature and call semantics unchanged; only a *new* rejection class that fires for configs that today boot unsafely. All non-prod configs and all prod configs with real secrets/SSO posture are unaffected. |
| New unexported method | `func (c *Config) validateEnvHardening() error` in new sibling file `internal/config/validate_hardening.go` | No public API change. |
| New package constants | `shippedSigningKeyPlaceholder`, `shippedAuditLedgerKeyPlaceholder` (package-private, `internal/config`) | No public API change; shared by validator and tests. |
| `internal/config/validate.go` | One line: call `validateEnvHardening()` after `validateRequired()` | `validate.go` stays 427 lines; no existing validator logic touched. |
| Error contract | 4 new pinned error strings under prod only | Additive; existing pinned messages (`is required`, `must be at least 32 bytes`) unchanged for non-prod and non-placeholder values. |
| `configs/config.yaml` | **None** | Shipped file unchanged (D8). |
| Public/proto/HTTP/gRPC surfaces | **None** | No config schema, handler, protobuf, or API contract changes. |

---

## 5. Compatibility constraints

1. **Shipped-file positive leg stays green unchanged.** `TestLoadShippedConfigFileAndEnvGate` (`config_test.go:511`) loads `configs/config.yaml` (`env: dev`) — no gate leg fires (E4, E9). No edits to that test are required; optionally a defensive `t.Setenv("AERO_ID_APP_ENV", "dev")` may be added later if CI ever sets it, but nothing in the current gate requires touching it.
2. **All 9 placeholder-key fixtures stay green.** `config_test.go:32,93,215,282,315,351,417,450` and `concurrency_test.go:33` use `env: test`/none — gate inactive (E13).
3. **`TestLoadAuditLedgerSigningKeyValidation` negative rows unchanged.** The ≥ 32-byte check and its pinned messages live in `validateAuditLedger`, untouched; the hardening gate adds no length logic.
4. **Precedence preserved.** `env: prod` + missing root key still yields `signing_key is required` (validateRequired runs first; D3).
5. **Env-merge precedence preserved.** No `SetDefault` interference (none exist for these keys); the gate reads post-merge values (D7). Env-wins-over-file semantics of `TestLoadAuditGovernanceKnobEnvLegs` are untouched (file `env` is absent there → gate inactive).
6. **Sequential test package.** `internal/config` has no `t.Parallel` (documented in `concurrency_test.go`), so `t.Setenv` env legs are race-safe — mirroring the existing env-leg pattern (E14).
7. **Engineering gates.** `validate.go` stays 427 lines; new file is small (funlen ≤ 50, cyclop ≤ 10, file ≤ 500). No Makefile/script/gate changes.
8. **All `Load()` callers unaffected.** `cmd/server`, `cmd/migrate`, `cmd/audit-replay`, `scripts/testdata/composition-root/*` load the shipped (dev) file or dev configs — gate inactive.
9. **k8s manifest unaffected (F1).** `deployments/k8s/regional-cell.yaml` embeds `app.env: production` — the literal `prod` predicate does not fire. No deployment artifact in the repo currently trips the gate; the gate is future-facing plus a tripwire for explicit `prod` boots.

---

## 6. Failure modes and mitigations

| Failure mode | Impact | Mitigation |
|---|---|---|
| Prod deployment boots with the shipped placeholders or dev SSO posture | Hard `Load()` failure (fail-closed) — the forgeable-chain/fail-open posture cannot boot | **Migration ordering (critical): rotate secrets and SSO posture in production config first, then deploy the gate release (§7).** A failed boot is the desired security behavior, not a bug. |
| Operator sets `env: prod` with legitimate keys but forgets SSO posture | Boot fails on `sso.insecure`/`sso.authz_required` with a self-explanatory pinned error | Error names the field and remediation; operator sets `AERO_ID_SSO_INSECURE=false` / `AERO_ID_SSO_AUTHZ_REQUIRED=true` (or file values). |
| Env injection: `AERO_ID_APP_ENV=prod` over a file with placeholders | Fails on FR-2 (root placeholder first) — env cannot bypass | D7 execution order; acceptance leg 6 pins this. |
| Ambient CI env leaks `AERO_ID_APP_ENV` or `AERO_ID_SSO_*` into tests | Wrong-reason pass/fail of the new test | Every leg pins `AERO_ID_APP_ENV`, `AERO_ID_SSO_INSECURE`, `AERO_ID_SSO_AUTHZ_REQUIRED` (and key envs in the positive leg) to neutral values first, mirroring `TestLoadShippedConfigFileAndEnvGate`'s `AERO_ID_AUDIT_GOVERNANCE_ENABLED=false` pin. |
| False positive: a prod operator's key literally equals a shipped placeholder | `Load()` rejects | Impossible to be legitimate — the literals are committed public values; the error names the env override as the remedy. Exact-literal matching (D2) means no false positives for any real random key. |
| k8s path with `app.env: production` and `CHANGE_ME_*` secrets (F1) | Gate never fires — the k8s deployment keeps booting with committed placeholders | Accepted residual of the literal `prod` + exact-literal scope (§9). Documented so ops can set `app.env: prod` / `AERO_ID_APP_ENV=prod` and align the ConfigMap; extending the deny set is a separate direction. |
| A teammate re-commits the shipped literals into a prod config | `make test`/`Load` fails locally with the pinned error | Permanent tripwire; constants are compiled-in and not env-overridable. |
| `validate.go` grows past the size cap | `make check-file-sizes` fails | D4 places the code in a sibling file; `validate.go` is untouched. |

---

## 7. Migration steps (ordered)

1. **Ops pre-step — remediate production config (before deploying the gate):** set `AERO_ID_SIGNING_KEY` and `AERO_ID_AUDIT_LEDGER_SIGNING_KEY` to fresh random values (`openssl rand -hex 32` each), `AERO_ID_SSO_INSECURE=false`, `AERO_ID_SSO_AUTHZ_REQUIRED=true` in the production environment/secret provider. Smoke-verify: boot, one audit append, one presigned download.
2. **Add `internal/config/validate_hardening.go`** — the two placeholder constants (D2) and `validateEnvHardening()` (D4).
3. **Wire into `validate()`** — one call after `validateRequired()` (D3).
4. **Add `TestLoadProdHardeningGate`** in `internal/config/config_test.go` per §8 (6 legs + precedence + determinism), mirroring `TestLoadAuditLedgerSigningKeyValidation` (table-driven, `writeTemp`) and the ambient-CI env pins of `TestLoadShippedConfigFileAndEnvGate`.
5. **Run the full done-gate:** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-load-scenarios && make check-migration-set && make check-composition-root && make check-proto-sync && make check-integration-compile && make build`. Also run `go test -race ./internal/config/ -count=1` (formality; package is sequential).
6. **Deploy the gate release** only after step 1 lands in production (fail-closed ordering).

---

## 8. Testable acceptance mapping

New regression test `TestLoadProdHardeningGate` in `internal/config/config_test.go`. Base fixture: the existing `sample` const (E14; `env: test`, `signing_key: testkey`, `audit_ledger.signing_key: change-me-please-very-long-audit-key` at lines 33/32) with `app.env` replaced by `prod`. Legs run sequentially (no `t.Parallel`). **Every leg pins `AERO_ID_APP_ENV` and the SSO env vars to neutral values first** so ambient CI env cannot leak in (D7 pattern from E9); the positive leg additionally pins the key env vars to its strong values so a leaky CI `AERO_ID_SIGNING_KEY` cannot cause a wrong-reason failure.

| Leg | Input (on `sample` with `env: prod`) | Expected | Requirement |
|---|---|---|---|
| 1 | `signing_key: change-me-please-very-long-random` (shipped root placeholder) | `Load` error contains `signing_key must not be the shipped placeholder when app.env=prod` | FR-2 |
| 2 | `audit_ledger.signing_key: change-me-please-very-long-audit-key` (shipped audit placeholder; root key `testkey` non-placeholder) | `Load` error contains `audit_ledger.signing_key must not be the shipped placeholder when app.env=prod` | FR-3 |
| 3 | Strong keys (both `strings.Repeat("k", 64)`), `sso.insecure: false`, `sso.authz_required: true` | `Load` succeeds; assert `cfg.SigningKey == strings.Repeat("k", 64)`, `cfg.AuditLedger.SigningKey == strings.Repeat("k", 64)`, `!cfg.SSO.Insecure`, `cfg.SSO.AuthZRequired` on the returned config | FR-1…FR-4 positive (prevents wrong-reason passes) |
| 4 | Strong keys, `sso.insecure: true` (authz_required left false) | `Load` error contains `sso.insecure must be false when app.env=prod` (insecure fires before authz_required per D3) | FR-4 |
| 5 | `env: dev` (or `env: test`) + placeholder keys + dev SSO defaults | `Load` succeeds — permissive/shipped-file leg (shipped file additionally covered unchanged by `TestLoadShippedConfigFileAndEnvGate`) | FR-1 predicate inactive |
| 6 | File `env: dev` + `t.Setenv("AERO_ID_APP_ENV", "prod")` over the shipped `configs/config.yaml` (placeholders present) | `Load` error contains the FR-2 substring — env cannot bypass; also proves FR-2 fires before FR-3 (the shipped file also carries the audit placeholder) | FR-6, FR-5 determinism |

Additional required assertions:
- **Precedence:** `env: prod` + missing `signing_key` (root key line removed from `sample`) yields `signing_key is required` — FR-2 runs after `validateRequired`.
- **Negative guard for wrong-reason pass:** leg 3's returned-config assertions (above); leg 4 pins `AERO_ID_SSO_AUTHZ_REQUIRED=true` in env so the failure can only be `insecure`.
- **Regression legs:** `TestLoadShippedConfigFileAndEnvGate`, `TestLoadAuditLedgerSigningKeyValidation`, `TestLoadAuditGovernanceKnobEnvLegs`, `TestLoad_fromFile`/`TestLoad_missingSigningKey`, and the concurrency fixtures all stay green unchanged (none sets `env: prod`; E13).

Implementation-time verification commands (documented, not run now):

```bash
go test ./internal/config/ -count=1 -run 'TestLoadProdHardeningGate|TestLoadShippedConfigFileAndEnvGate|TestLoadAuditLedgerSigningKeyValidation|TestLoadAuditGovernanceKnobEnvLegs'
go test -race ./internal/config/ -count=1
make test && make lint && make lint-strict && make check-file-sizes && make build
# full done gate per §7 step 5
```

---

## 9. Scope boundaries and residual risk

**Implemented (this design):** prod-only gate over the two shipped placeholder HMAC keys (FR-2, FR-3) and the two SSO booleans `sso.insecure` / `sso.authz_required` (FR-4), activated by literal `app.env == "prod"` (FR-1), ordered FR-2 → FR-3 → FR-4 after `validateRequired` (FR-5, FR-8), reading the merged env (FR-6), with value-free error texts (FR-7).

**Explicitly out of scope (per the accepted mechanism):**
- `database.password: aero` (`configs/config.yaml:89`) — no acceptance leg exercises it; gating it would require a deny-list value for an otherwise-unvalidated field.
- `sso.authz_legacy_fallback: true` (`:105`) — documented "compatibility only" with a deliberate migration path at `new.go:205-206`.
- Prefix-family placeholder denial (`change-me…`, `changeme`, `replace-me`), env-value normalization (`production` ≠ `prod`), shipped-value regeneration, runtime (post-`Load`) enforcement — all explicitly excluded.
- F1: the k8s manifest's `CHANGE_ME_*` ConfigMap secrets and its `app.env: production` are outside the exact-literal deny set and the literal `prod` predicate. **Recorded residual:** the k8s deployment path can still boot with committed `CHANGE_ME_*` placeholders and never trip this gate. The design documents the remedy (set `app.env: prod`/`AERO_ID_APP_ENV=prod`, align the Secret) but does not implement it; extending the deny set to `CHANGE_ME_*` or matching `production` is a separate direction.
- F2 interplay: if the always-on deny-list design (`2026-08-16-reject-well-known-placeholder-secrets-design.md`) lands first, this gate's FR-2/FR-3 checks become redundant-but-harmless for the two shipped literals (the always-on gate rejects them under any env, so the prod-only legs simply never see them); FR-4 (SSO posture) remains uniquely valuable. If this gate lands first, the always-on design's fixture migration (D5 there) remains compatible — the `env: test` fixtures it rewrites never hit the prod predicate. No coordination change required in either direction.
- **Compatibility carve-out note:** no deployment artifact in the repo currently sets `app.env: prod` (shipped file is `dev`, k8s is `production`), so this gate breaks nothing today; it is a tripwire that activates the moment an operator or pipeline sets `prod` with the shipped placeholders/dev posture.
