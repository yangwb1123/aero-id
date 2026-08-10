# Design — Governance B2-3 tuning-knob gate: negative `audit_governance.timeout` / `refresh_skew` fail at `Load()` (T-10.1, T-5)

| | |
|---|---|
| Module (batch label) | `internal/config` (validation + Load-level unit tests only; no connector changes) |
| Input evidence | `docs/requirements/2026-08-08-internal-config-governance-negative-tuning-knob-gate.md` (claims re-verified against the working tree @ `b005409` for this design, §0) |
| Predecessors | `docs/requirements/2026-08-07-internal-config-b2-6-activation-gate.md`, `docs/plans/2026-08-08-internal-config-b2-6-activation-gate-completion-design.md` (T-10.1 gate family), `docs/plans/2026-08-08-internal-config-governance-load-time-endpoint-source-prefix-gates-design.md` (sibling direction 1 — disjoint surface) |
| Date | 2026-08-08 |
| Status | **Draft design — evidence re-verified; all claims stand.** No DB migration, no connector change, no new config keys, no shipped-file edit |

## 0. Verification verdict (evidence claims re-checked, untrusted → verified)

Baseline re-run at verification time: `go test ./internal/config/... -count=1` → `ok` (cached, green). Every
claim in the requirements spec was re-checked against the working tree at `b005409` (HEAD matches). No claim
was wrong; one line citation was already disclosed as drifted and confirmed.

| # | Claim (evidence) | Verdict | Verified at |
|---|---|---|---|
| E1 | `validateAuditGovernance` reads only `DeadLetterRetention`, the activation gate, and the anchor — never `Timeout`/`RefreshSkew` | ✅ exact (`:227-240`; the disclosed `:149-162` drift confirmed — those lines are `validateRateLimit`/`validateStorageGC`) | `validate.go:227-240` |
| E2 | Runtime silently replaces negatives at all three consumer sites | ✅ exact — `defaultOAuthConfig` `<= 0` → 5s/30s (`internal/connector/auditgovernance/oauth.go:66-72`); `defaultConfig` `<= 0` → 5s (`internal/connector/auditgovernance/publisher.go:73-74`); drain lease `<= 0` → 5s (`internal/service/callback_audit.go:100-102`) | read all three |
| E3 | `validSourceLimits` precedent rejects negatives on the sources block | ✅ exact (`validate.go:407-409`; message at `:384-386`) | `validate.go:384-386,407-409` |
| E4 | Wiring: `app/audit.go:81` (`PublicationTimeout`), `:197` (`Timeout`/`RefreshSkew` → `OAuthConfig`), `:165` `buildAuditPublishers` early-return when disabled | ✅ exact — knobs inert when relay disabled, so `Enabled`-gating the check is safe | `app/audit.go:81,165,197,215` |
| E5 | All cited test patterns and helpers exist | ✅ exact — `TestAuditGovernanceDeadLetterRetentionValidation` (`config_test.go:279`), `TestLoadAuditGovernanceKnobEnvLegs` (`:312`), `TestAuditGovernanceKnobGateMatrix` (`commercial_test.go:134`, defaults cell at `:176`), `TestAuditGovernanceActivationGate` (`config_test.go:414`), `TestLoadShippedConfigFileAndEnvGate` (`:511`), helpers `writeTemp` (`config_test.go:37`), `setCommercialSecrets` (`commercial_test.go:328`, sets `TEST_AUDIT_SECRET`), `commercialTestConfig` (`commercial_test.go:334`, `Enabled:true`, valid binding) | read all |
| E6 | Shipped `configs/config.yaml:185-186` already positive (`5s`/`30s`) | ✅ exact — no file edit needed; shipped-file Load test stays green | `configs/config.yaml:185-186` |
| E7 | Viper env merge happens inside `Load()` before `validate()`; `AutomaticEnv` lazy (env wins only when the file carries the key) | ✅ exact — `config.go:388-399` (`SetEnvPrefix` → `AutomaticEnv` → `ReadInConfig` → `Unmarshal` → `validate()`); the file-must-carry-the-key rule is documented at `config_test.go:312-320` | `config.go:384-403` |

## 1. API changes

### 1.1 The change, precisely

One function changes in `internal/config`; the only other edit is the rollout tooling of §4 step 4
(`cmd/server -validate-only` — a thin exit-0 wrapper around the same `Load()`, no config-surface change).
`validateAuditGovernance` (`internal/config/validate.go:227`) gains two
`Enabled`-gated rejection checks inserted **after the activation gate (`:231-233`) and before
`validateAuditAnchor` (`:234-237`)**:

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

New error surface (stable dotted-path message convention, identical shape to the existing
`audit_governance.dead_letter_retention must not be negative`; no value interpolation, so no disclosure):

| Condition | Error |
|---|---|
| `enabled: true` and `timeout < 0` (file or env) | `audit_governance.timeout must not be negative` |
| `enabled: true` and `refresh_skew < 0` (file or env) | `audit_governance.refresh_skew must not be negative` |

### 1.2 What is NOT part of the API change

- **No new config keys, no schema change** — `AuditGovernanceConfig` (`audit_governance.go:34-35`) is untouched.
- **No connector behavior change** — the `<= 0` → 5s/30s code-default replacements stay; `0` remains the
  documented "code default" sentinel at every consumer site. The gate only makes **negatives** a Load error
  when the knobs are live.
- **No `dead_letter_retention` semantics change** — that check stays `Enabled`-independent (deliberate:
  the purge guard protects a ledger even when the relay is off; pinned by the existing knob-gate matrix).
- **No shipped-file edit** — `configs/config.yaml:185-186` already carries positive values.
- **No HTTP/gRPC/protobuf surface** — this is a boot-time configuration contract; there is no runtime API.
- **No change to error precedence for already-rejected configs** — insertion after the activation gate means
  an enabled config with zero bindings still fails with the binding message first (existing pinned behavior,
  `TestAuditGovernanceActivationGate`).

### 1.3 Effective contract

`Load()` accepts `timeout`/`refresh_skew` iff they are `>= 0` **or** the relay is disabled. The runtime
consumers' `<= 0` defaults are reachable only through `0`/absent (code default) — never through a negative
that the operator believes is meaningful.

## 2. Compatibility constraints

| Constraint | Binding | Enforcement / pin |
|---|---|---|
| C1: Kill switch intact | A disabled relay (`enabled: false` — the documented pre-provision/maintenance state) must keep loading with any knob values, including negatives. The knobs are inert when disabled (`app/audit.go:170` early-return), so rejection there would be a boot failure with no behavioral benefit. | A3 cell asserts Load **OK** and the parsed struct carries `-1s` for both fields — the negative landed and was ignored by design, not silently dropped. |
| C2: Zero-value convention (AGENTS.md) | `0` = code default in **both** states; `0` must never be rejected. The parity surface between `0` and the connector defaults (`<= 0` → 5s/30s) is pinned, not re-tested as new parity. | A5 cell + extended `TestAuditGovernanceKnobGateMatrix` defaults cell. |
| C3: Error precedence preserved | Existing rejection order inside `validateAuditGovernance` is load-bearing: `dead_letter_retention` → activation gate → (new knobs) → anchor. An enabled unbound config must still fail with the activation-gate message, never the knob message. | Insertion point fixed after `:233`; `TestAuditGovernanceActivationGate` (`config_test.go:414`) stays green. |
| C4: Env cannot bypass | `AERO_ID_AUDIT_GOVERNANCE_TIMEOUT=-1s` / `..._REFRESH_SKEW=-1s` over a valid enabled file must fail Load. Viper merges env inside `Load` before `validate` (`config.go:388-399`), so the check sees the merged struct. | A4 cell (with the lazy-viper file-must-carry-the-key rule). |
| C5: Non-disclosure | Errors never echo raw values (module convention; matches `TestAuditGovernanceRejectsInvalidTokenURLBeforeCredentials`'s disclosure pin). | Message strings contain no interpolation; test asserts via substring on the pinned message only. |
| C6: Shipped config and existing tests | `configs/config.yaml` (positive knobs, relay disabled) and every existing Load-level test keep passing. | A6 regression pins; `TestLoadShippedConfigFileAndEnvGate` (`:511`) green. |
| C7: File-size gate | `validate.go` is 427 lines; the 12-line insertion keeps it at 439, under the 500-line gate. No package split needed; the function stays one cohesive concern. | `make check-file-sizes`. |
| C8: No integration requirement | Pure validation; no SQL/migrations/storage/connectors/event ingestion touched. | Unit-only done gate (§6); `make test-integration` not required. |

## 3. Failure modes

### 3.1 Operational failure modes (config-time)

| Mode | Symptom without the gate (today) | With the gate |
|---|---|---|
| F-1: Operator typos a negative tuning knob (`timeout: -1s`, `refresh_skew: -1s`) in an **enabled** relay | Load passes; at runtime the connector silently substitutes 5s/30s (oauth.go:68-72, publisher.go:73-74) and the lease math substitutes 5s (callback_audit.go:100) — no log, no error, operator believes a negative bound is active | Load fails with the pinned message; boot stops before any DB/service wiring. Fail-closed, matching the audit-governance family's contract. |
| F-2: Same typo with the relay **disabled** (kill switch / pre-provision) | Load passes; knobs inert | Load still passes (C1). Values are inert by design; the struct carries them so a later `enabled: true` flip in the same file fails Load — the gate catches the flip, not the dormant state. |
| F-3: Env override carries the negative (`AERO_ID_AUDIT_GOVERNANCE_TIMEOUT=-1s`) over an enabled file | Load passes (env merge inside Load, `config.go:388-399`); silent runtime default | Load fails (C4) — env cannot bypass. |
| F-4: `0` used as "disable" by an operator expecting `0 = off` | Load passes; runtime treats `0` as code default (5s/30s) — this is the documented convention, unchanged | Same behavior, unchanged and pinned (C2/A5). Documented in config comments; not a regression. |

### 3.2 Implementation failure modes (what could make the gate wrong)

| Mode | How it would happen | Guard |
|---|---|---|
| F-5: Wrong-reason test pass (A1/A2 green without the check) | Test asserts only `err != nil`; any pre-existing error (e.g., activation gate firing because the fixture lacks a binding) would satisfy it | Enabled fixtures carry `enabled: true` + valid endpoint + one fully valid binding + `setCommercialSecrets`, so the **only** possible Load error is the knob error; tests assert the exact pinned substring (pattern of `:279`). |
| F-6: Wrong-reason test pass (A4 green without env plumbing) | File omits the key → viper's lazy `AutomaticEnv` never merges the env value → test passes even if env plumbing is broken | File MUST carry the default-valued key (`timeout: 5s`), never the env value; documented rule at `config_test.go:312-320`. A duration-parse failure would produce an unmarshal error, not the pinned message — substring assertion excludes it. |
| F-7: Wrong-reason test pass (A3 green without parsing) | Test asserts only Load OK; the YAML could have been dropped entirely | Assert the parsed struct carries `-1s` for **both** fields — proves the negative landed and was accepted-by-design (inert), distinguishing "gated correctly" from "value lost". |
| F-8: Regression of `Enabled`-independence of the retention check | Implementer folds the new check into the existing retention guard pattern and accidentally gates retention too, or vice versa | A6 pins the existing matrix cells (`enabled negative retention rejected`, `disabled negative retention rejected`); new cells are added alongside, not merged. |
| F-9: Error precedence flip | Insertion before the activation gate → enabled+unbound+negative config would report the knob error instead of the binding error, changing the pinned T-10.1.6 behavior | Insertion point fixed after `:233`; A6 runs `TestAuditGovernanceActivationGate`. |
| F-10: Value disclosure | Message interpolates the raw duration | Pinned messages are static strings; review checklist item in §5. |

## 4. Migration steps

No data migration, no schema change, no shipped-file change. Operational rollout is a config-contract
change and must be sequenced so no running deployment boots into the new rejection unexpectedly:

1. **Audit current configs (pre-merge).** Search all managed configs for negative
   `audit_governance.timeout` / `refresh_skew` values: `grep -rn "audit_governance" configs/ | grep -E "timeout|refresh_skew"`.
   The shipped file is positive (`:185-186`) and the relay is disabled (`:175`), so the default surface is
   unaffected. Any enabled config carrying a negative is already misconfigured (silently defaulted at
   runtime today) and must be corrected to a positive value or `0` (code default) **before** the gate lands.
2. **Land the code change with tests (single commit).** Validation + A1-A6 cells; `make test` green. No
   deployment ordering constraint within the repo — the gate is fail-closed at boot, so any config that
   passes Load today with non-negative (or disabled) values keeps passing (C6).
3. **Roll out per environment; disabled-relay environments first (they cannot block).** No inter-
   environment ordering constraint exists: the only new rejection surface is the `Enabled`-gated knob
   check, so any environment whose *effective* config (file + `AERO_ID_*` env overrides) has the relay
   disabled loads any knob values and can roll in any order, including first (C1). Boot wiring verified:
   `buildAuditPublishers` early-returns (`app/audit.go:169-171`), and the drain lease normalizes
   `<= 0` → 5s (`callback_audit.go:100`), so no negative ever reaches a live duration in the disabled
   state. The only load-bearing sequence: an environment whose effective config has `enabled: true` must
   have non-negative effective knobs before the new binary lands there — run the step-4 dry run against
   its real file + env first. A negative that survived step 1 (file) or arrived via env
   (`AERO_ID_AUDIT_GOVERNANCE_TIMEOUT=-1s`, or `..._ENABLED=true` flipping a disabled file) fails Load
   with the pinned message naming the exact key: the rollout is blocked explicitly, never silently
   misconfigured.
4. **Post-rollout verification (mechanism specified — none existed).** No validate-only surface exists
   today: `cmd/server` has only `-config` and proceeds to full app wiring (DB, storage, SSO, outbox, hub),
   `cmd/migrate`/`cmd/audit-replay` Load before DB use but are not validate-only, and no `cmd/config-check`
   exists. Specify a `-validate-only` flag on `cmd/server` (flag precedent: `cmd/audit-replay -dry-run`),
   inserted immediately after `config.Load` succeeds and before `logger.New`/`app.New`:

   ```go
   	cfg, err := config.Load(*cfgPath)
   	if err != nil {
   		fmt.Fprintln(os.Stderr, "load config: "+err.Error())
   		os.Exit(1)
   	}
   	if *validateOnly {
   		fmt.Fprintln(os.Stdout, "config valid: "+*cfgPath)
   		os.Exit(0)
   	}
   ```

   Contract: accepted config → exit 0 + `config valid: <path>` on stdout; rejected config → exit 1 + the
   existing byte-identical `load config: <pinned message>` line on stderr; no logger output, no DB/app
   wiring, no side effects. Because it runs the same `Load()` (viper env merge → Unmarshal → `validate()`),
   it verifies the *effective* per-environment config including `AERO_ID_*` overrides — closing the
   residual gap of step 1's file-only grep. Procedure per environment: `bin/aero-id -config <env config>
   -validate-only`; then confirm the connector still receives 5s/30s defaults when `0` is configured
   (A5 parity pin, unchanged). Pin the flag with one subprocess test reusing the `buildServerBinary`
   harness (`cmd/server/main_test.go:59`): good config → exit 0/`config valid:`; enabled config with
   `timeout: -1s` → exit 1 + stderr byte-identical to `load config: audit_governance.timeout must not be
   negative\n`. No Makefile or composition-root change (`make build` already produces `bin/aero-id`; no
   new package).
5. **Revert is a plain code revert.** The change touches only `internal/config/validate.go` (+12 lines,
   427 → 439) and two test files; no new files (no `testdata/` fixtures — all fixtures are inline YAML via
   `writeTemp`/`t.TempDir()` and `t.Setenv` env, both auto-scoped/cleaned), no `configs/config.yaml` edit,
   no schema/migration/data, no connector or `app/` edit. `git revert <commit>` restores all touched
   files; there is no leftover state, no fixture coupling (`commercialTestConfig`/`setCommercialSecrets`
   are used read-only), and the reverted matrix defaults cell restores its original assertion. The only
   theoretical conflict is textual, with the sibling endpoint/source-prefix gates design, which also
   edits `validate.go`/`config_test.go` but hooks `validateAuditBindings` (its §1.2: explicitly *not*
   `validateAuditGovernance`) — disjoint hunks, so a revert applies cleanly (or with trivially
   resolvable context). Configs remain valid either way: the gate only rejects negatives that are
   already operator errors.

## 5. Implementation checklist (review gates)

- [ ] Insertion point: after activation gate (`validate.go:233`), before `validateAuditAnchor` (`:234`).
- [ ] Both conditions gated on `value.Enabled`; both messages static, dotted-path, no interpolation.
- [ ] `validate.go` stays under 500 lines (427 + 12 = 439).
- [ ] No edits to `audit_governance.go`, `oauth.go`, `publisher.go`, `callback_audit.go`, `app/audit.go`,
      `configs/config.yaml`, or any `.proto`. The only permitted edit outside `internal/config` is
      `cmd/server/main.go` — the `-validate-only` flag per §4 step 4, plus one subprocess test in
      `cmd/server/main_test.go`.
- [ ] Enabled test fixtures use `setCommercialSecrets` + one fully valid binding (wrong-reason protection).
- [ ] Env-leg file carries the default-valued key per the lazy-viper rule.

## 6. Testable acceptance mapping (A1–A6 → concrete tests)

All new cells live in `internal/config/config_test.go` beside the cited patterns; matrix cells extend
`TestAuditGovernanceKnobGateMatrix` (`commercial_test.go:134`). Fixture: the `base` YAML from
`TestAuditGovernanceActivationGate` (`:415-421`) plus an enabled governance block
(`enabled: true`, `endpoint: https://audit.example`, one binding with
`token_url: https://sso.example/token`, `client_id`, `client_secret_env: TEST_AUDIT_SECRET`,
`scope: audit:event:write`, `resources: [audit-governance]`) and `setCommercialSecrets(t, ..., "audit-secret-value-000000000000000001")`.

| # | Cell | Concrete test | Pin (assertion) | Wrong-reason protection |
|---|---|---|---|---|
| A1 | enabled + `timeout: -1s` | `t.Run("enabled negative timeout rejected")`: Load(`base` + `audit_governance: {enabled: true, endpoint: ..., bindings: {...}, timeout: -1s}`) | `err != nil && strings.Contains(err, "audit_governance.timeout must not be negative")` | Fixture satisfies activation gate + commercial validation → only knob error possible; exact substring |
| A2 | enabled + `refresh_skew: -1s` | same shape with `refresh_skew: -1s` | `strings.Contains(err, "audit_governance.refresh_skew must not be negative")` | same |
| A3 | disabled + both negatives | Load(`base` + `audit_governance: {enabled: false, timeout: -1s, refresh_skew: -1s}`) — no bindings needed (kill switch accepts empty) | `err == nil`; then `cfg.AuditGovernance.Timeout == -time.Second && cfg.AuditGovernance.RefreshSkew == -time.Second` | struct-value assert proves parse-and-ignore (F-7); if the gate were ungated, this cell goes red |
| A4 | env cannot bypass | `t.Setenv("AERO_ID_AUDIT_GOVERNANCE_TIMEOUT", "-1s")`; file carries the **default-valued key** `timeout: 5s` (never `-1s`, never absent) in the A1-enabled fixture | `strings.Contains(err, "audit_governance.timeout must not be negative")` | lazy-viper rule (F-6): wrong-reason pass impossible; unmarshal-failure excluded by substring |
| A5 | `0` = code default | enabled fixture with `timeout: 0, refresh_skew: 0` (also one absent-key leg) | `err == nil`; merged struct `Timeout == 0 && RefreshSkew == 0`; **extend** `TestAuditGovernanceKnobGateMatrix` defaults cell (`commercial_test.go:176`) to also assert `Timeout == 0 && RefreshSkew == 0` on `commercialTestConfig()` | matrix defaults cell pins the direct-`validateAuditGovernance` path too |
| A6 | regression pins | `go test ./internal/config/... -count=1` green, including `TestLoadShippedConfigFileAndEnvGate`, `TestAuditGovernanceDeadLetterRetentionValidation`, `TestLoadAuditGovernanceKnobEnvLegs`, `TestAuditGovernanceKnobGateMatrix`, `TestAuditGovernanceActivationGate` | all pass | C3/C6/F-8/F-9 |

Maps to T-10.1 (config-layer gate family) and T-5 (env-leg family) in the predecessor design.

## 7. Verification commands (done gate)

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-composition-root && make check-integration-compile && make build
```

No `make test-integration`: the change is pure config validation; no SQL, migrations, storage, connectors,
event ingestion, callback delivery, or transaction boundaries are touched (per AGENTS.md integration-gate
trigger list).
