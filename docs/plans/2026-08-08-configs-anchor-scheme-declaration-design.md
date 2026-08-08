# Design — config-declared anchor scheme for deterministic audit event_id (B2 requirement 4)

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-08-configs-anchor-scheme-declaration.md` (REQ-1..REQ-5, T-10.1-A, T-5, T-10.2) |
| Module (batch label) | `configs` (deliverable: config surface + Load()-time validation + parity pins; cross-layer legs pinned to existing tests) |
| Direction | Generalize the anchor-derived stable event_id (B2 requirement 4): the derivation is already implemented at 12+ sites and gate-enforced; the missing piece is the config-declared anchor scheme/version so dedup stability is a load-time invariant |
| Status | Design — all requirement evidence re-verified against the working tree at head `13e4676` on 2026-08-08 (§1); production-code delta is two struct fields + one normalization call + one validation function in `internal/config` (§3) |

## 1. Evidence verification (untrusted claims re-checked)

Every citation in the requirements evidence was re-checked against the live working tree. **All 10 claims hold in substance.** Line drifts are re-confirmed; **one new material correction is added: the AT-4 test name cited in the requirements doc (E6, T-10.2-2a) does not exist** — the real test has a different name and file. Details below.

| # | Evidence claim | Verdict |
|---|---|---|
| E1 | Unique `(tenant_id, source, event_id)` constraint; `checkAuditDuplicate` → `ErrAuditDuplicate` | ✅ Confirmed. `checkAuditDuplicate` `internal/repository/audit_ledger.go:174-191`; zero-`EventID` short-circuit `:175-177` (doc cited `:176-178` — 1-line drift); triple query `:178-186`; `ErrAuditDuplicate` at `:59`; constraint-precise 23505→`ErrAuditDuplicate` mapping on `uq_audit_ledger_tenant_event` `:204-210`; constraint in `migrations/000050_scope_events_and_audit_by_tenant.up.sql:15-16` |
| E2 | General append path: `fact.ID = idgen.NewID()`; row PK ≠ EventID; zero-EventID short-circuits dedup | ✅ Confirmed exact. `internal/repository/audit_ledger.go:161` (inside `if fact.ID == (domain.ID{})` `:160-162`); `EventID: input.EventID` is a separate field (`buildAuditFact`, `internal/service/audit_ledger.go:95-96`) that stays zero on the unanchored path; retry appends a new PK row with bumped `chain_sequence` `:156` and a new publication row. Service layer always mints a PK (`audit_ledger.go:95`), so the repo `:160-162` guard is defensive |
| E3 | Derivation already implemented at 12+ sites; only the config declaration is missing | ✅ Confirmed, substance exact. `idgen.Anchor` v5 sites at the cited lines, byte-exact: `internal/service/operation_audit.go:68`, `sync_audit.go:44`, `audit_hold.go:164,184`, `legacy_cutover.go:143`. Pre-assigned v7 receipts spot-checked at cited lines: `account_provision.go:157`, `account_backfill.go:121`, `operation_audit.go:14`, `sync_audit.go:13`, `callback.go:169,226`, `event_ingest.go:160`, `replay.go:147,244`. Gate `make check-anchored-event-ids` exists with R-1/R-2 (`scripts/check-anchored-event-ids.sh:1-40`); C2 allowlist exactly `callback.go` / `cmd/audit-replay/main.go` / `audit_fact.go`. **Missing piece confirmed:** `grep -rn Anchor internal/config/` → zero hits (E10) |
| E4 | WORM anchor over `record_hash`, not `event_id` | ✅ Confirmed. `internal/service/audit_anchor.go:41-42` (`RecordHash`, `AnchorKey`); chain links `PreviousHash`/`ChainSequence` (`repository/audit_ledger.go:156-157`) |
| E5 | `validateAuditGovernance` validates only `dead_letter_retention`; connector silently defaults `schema_id` | ✅ Confirmed exact. `internal/config/validate.go:211-216` (negative-retention check only); `AuditGovernanceConfig` (`audit_governance.go:8-28`) has `SchemaID`/`SchemaVersion` but no anchor fields; connector `defaultConfig` `internal/connector/auditgovernance/publisher.go:51-77` silently fills `schema_id`→`"aero.id.audit-fact"`, `schema_version`→1; `configs/config.yaml:174-186` has no anchor keys |
| E6 | Governance `event_id` = `fact.ID.String()`; AT-4 idempotency_key stability | ✅ Confirmed substance; **test-name correction (new).** `callback_audit.go:32` `"event_id": fact.ID.String()`; C-4 `IdempotencyKey = fact.ID.String()` at `docs/plans/2026-08-06-cmd-server-tokensource-invalidate-on-401-design.md:429`. **Correction:** the cited pin `TestRetryAfter401CompletesWithRefetchedToken` does not exist in any `.go` file in the tree (repo-wide grep: only the 401 design doc and the requirements doc name it). The AT-4 substance is implemented by `TestPublish401InvalidatesAndRefetchesExactlyOnce` (`internal/connector/auditgovernance/token_invalidation_test.go:60`), which asserts both retry bodies' `event_id`/`idempotency_key` are identical == `fact.ID.String()` (`:142-147`). §7 pins the corrected name |
| E7 | `TestReportResolveAnchoredEventIDAppendsOnce` precedent | ✅ Confirmed. `internal/repository/comment_report_anchor_integration_test.go:22`; sibling `TestAnchoredEventIDAppendsOnce` `anchor_dedup_integration_test.go:35` (doc cited `:32-53` — comment line) |
| E8 | `TestAuditLedgerDuplicateMapsToSuccess`; `TestAuditWiringRollbackAtomicity` | ✅ Confirmed. `internal/service/audit_ledger_test.go:91` (`ErrAuditDuplicate`→nil mapping); `internal/repository/audit_wiring_integration_test.go:175` (forced duplicate → tx rollback); `TestRecordAnchoredEventID_Dedup` `audit_ledger_test.go:127` |
| E9 | Config test infrastructure: gate matrices, env legs, parity precedent | ✅ Confirmed with comment-line drift. `TestAuditGovernanceGateMatrix` `commercial_test.go:79`; `TestAuditGovernanceKnobGateMatrix` `:134` (doc cited doc-comment lines `:77`/`:123`); `validAuditTokenURL` `commercial.go:82-90` (no-echo rule `:85-87`); env-leg harness comment `config_test.go:303-312`; `auditGovernanceBadYAML` `config_test.go:123-138`; in-package tests (`package config`) already import `internal/connector/auditgovernance` (`commercial_test.go:9`, T-10.1.6 parity `:296-322`) — no cycle, so the same pattern is available for the new parity legs |
| E10 | C2 fresh-mint sites by contract | ✅ Confirmed. Gate allowlist (R-1): `callback.go` (ReplayDelivery), `cmd/audit-replay/main.go` (requeueFact), `audit_fact.go` (buildLegacyAuditFact — eight legacy-Log sites); `internal/config` has zero anchor symbols |

**Additional verified facts the design depends on (beyond the evidence):**

- **F1 — Zero-value convention:** `Load` normalizes no defaults today; knobs stay raw (`TestAuditGovernanceKnobGateMatrix` defaults cell, `commercial_test.go:157-165`). The new `applyAuditAnchorDefaults` (§3.2) is a narrow, deliberate exception required by T-5-1's defaults cell (effective values post-`Load`) and T-10.2-1 (defaults parity with the connector) — it touches only the two new fields.
- **F2 — Env semantics:** viper `AERO_ID` prefix + `"."→"_"` replacer + `AutomaticEnv` (`config.go:380-391`); env wins over file keys, but viper only consults env for keys that exist in the file (`config_test.go:303-312` harness comment) — the env legs MUST carry the anchor keys in the base YAML, exactly like the T-10.1.9 knob env legs.
- **F3 — Boot coverage:** `config.Load` is called by both `cmd/server/main.go:22` and `cmd/migrate/main.go:62`; a Load-time rejection fails both binaries at boot.
- **F4 — Leaf-package hygiene:** `internal/pkg/idgen` imports only stdlib + `github.com/google/uuid` — a test-only import from `internal/config` is cycle-free; `internal/config` production code stays dependency-free (no idgen import at runtime, per REQ-3).
- **F5 — Entry-point split is load-bearing and already pinned:** `validateAuditGovernance` sits in `validateRuntimeLimits` (`validate.go:125-138`, checks list at `:130`), not `validateCommercialIntegrations`; the knob matrix pins the split (`commercial_test.go:123-133` comment). The anchor check joins the same list.

## 2. Goals and non-goals

**Goals.** (G1) Declare the anchor contract on the config surface: `audit_governance.anchor_scheme` + `anchor_scheme_version`, documented in struct and yaml (REQ-1, REQ-4). (G2) Reject at `Load()` any config that contradicts the compiled-in derivation, Enabled-independently (REQ-2). (G3) Pin the declared scheme to the implementation with parity tests: namespace constant, v5 encoding, NUL separation, defaults parity with the connector (REQ-3). (G4) Keep every currently accepted config accepted (regression cells) and every existing gate green (REQ-5).

**Non-goals.** No conversion of C2 fresh-mint sites (`audit_fact.go` eight legacy-Log sites, `callback.go` ReplayDelivery, `cmd/audit-replay` requeueFact) — they stay fresh-mint by B2-1 REQ-3.3 contract (E10). No change to the ledger append path (`appendInTx`, `checkAuditDuplicate`, the `fact.ID = idgen.NewID()` fallback at `audit_ledger.go:161`, the `(tenant_id, source, event_id)` constraint, or the WORM chain). No change to the governance payload contract (`callback_audit.go:32`), the publisher, or the connector's `schema_id`/`schema_version` defaulting. No `schema_id`/`schema_version` shape validation (connector-owned). No migration, no protobuf, no metrics, no hot reload, no new Makefile gates. No runtime import of `idgen` from `internal/config` production code (F4).

## 3. API changes

No exported API changes to any package outside `internal/config`; no interface changes; no behavioral change to the ledger, publisher, or connectors. Inside `internal/config`: two new struct fields, one new validation function, one normalization step in `Load`, and the yaml surface. All additions are additive — configs that omit the new keys keep today's behavior (zero-value convention, §4).

### 3.1 New config fields — `internal/config/audit_governance.go`

Insert after `SchemaVersion` (line 14), before `DataClassification`:

```go
	// AnchorScheme identifies the deterministic event_id derivation contract
	// for audit facts: idgen.Anchor, an RFC-4122 v5 UUID over NUL-separated
	// identity parts in the fixed AnchorNamespace
	// 38248e3a-32f0-4929-9b56-b2dbd75aa849 (derivation contract
	// derive-stable-deterministic-event-id-from-an-anc §2.1/§2.2 R1-R7). The
	// scheme is load-bearing: ledger dedup keys on (tenant_id, source,
	// event_id), so a retried mutation must re-derive the same event_id. The
	// namespace and encoding are constant forever — never rotate the
	// namespace and never change the derivation without a scheme-version
	// bump that fails at Load(). Empty = uuidv5-nul-separated (the only
	// scheme that exists). This field declares the contract; it is not a
	// switch — validation accepts exactly the compiled-in scheme.
	AnchorScheme string `mapstructure:"anchor_scheme"`
	// AnchorSchemeVersion is the derivation version. Empty/0 = 1 (the only
	// version that exists); any other value fails at Load(). Same
	// never-change-without-a-bump contract as AnchorScheme.
	AnchorSchemeVersion int `mapstructure:"anchor_scheme_version"`
```

### 3.2 Load-time normalization — `internal/config/audit_governance.go` + `config.go`

New unexported method (same file, next to the struct), so the effective values are present on the loaded config for documentation, parity tests, and downstream consumers:

```go
// applyAuditAnchorDefaults fills the anchor contract with its effective
// values (zero-value convention: empty/0 = default). Unlike the B2-5 knobs,
// which stay raw zero, these two fields are normalized so the loaded config
// always carries the contract a deployment is actually running under.
func (c *Config) applyAuditAnchorDefaults() {
	if c.AuditGovernance.AnchorScheme == "" {
		c.AuditGovernance.AnchorScheme = anchorSchemeUUIDv5NulSeparated
	}
	if c.AuditGovernance.AnchorSchemeVersion <= 0 {
		c.AuditGovernance.AnchorSchemeVersion = anchorSchemeVersionDefault
	}
}
```

Called from `Load` (`config.go`) between `Unmarshal` (line 390) and `cfg.validate()` (line 395):

```go
	cfg.applyAuditAnchorDefaults()
	if err := cfg.validate(); err != nil {
```

Validation then always sees effective values, while direct `validateAuditAnchor` calls (the matrix tests) must still accept empty/0 (rule 1/3, §3.3) so the two layers agree. F1 documents why this normalization is a deliberate exception to the raw-knob convention.

### 3.3 New validation — `internal/config/validate.go`

New package-level constants and function (≤50 lines, cyclop ≤10; in-package tests reference the unexported constant directly — no new exported API):

```go
const (
	// anchorSchemeUUIDv5NulSeparated is the only scheme the compiled-in
	// derivation realizes (idgen.Anchor: v5, NUL-separated parts, fixed
	// namespace). The T-10.1-A-1 parity test pins it to idgen.
	anchorSchemeUUIDv5NulSeparated = "uuidv5-nul-separated"
	anchorSchemeVersionDefault     = 1
)

// validateAuditAnchor rejects a config that contradicts the compiled-in
// event_id derivation contract. Enabled-independent: the ledger dedup guard
// (checkAuditDuplicate) runs on every append whether or not the governance
// relay is enabled, so a wrong scheme must fail even when audit_governance
// is disabled. Error substrings are the testable contract; the raw value is
// never echoed (validAuditTokenURL no-echo rule, commercial.go:85-87).
func validateAuditAnchor(value AuditGovernanceConfig) error {
	if value.AnchorScheme != "" && value.AnchorScheme != anchorSchemeUUIDv5NulSeparated {
		return fmt.Errorf("audit_governance.anchor_scheme must be %q", anchorSchemeUUIDv5NulSeparated)
	}
	if value.AnchorSchemeVersion != 0 && value.AnchorSchemeVersion != anchorSchemeVersionDefault {
		return fmt.Errorf("audit_governance.anchor_scheme_version must be %d", anchorSchemeVersionDefault)
	}
	return nil
}
```

Exact error substrings (the testable contract): `audit_governance.anchor_scheme must be "uuidv5-nul-separated"` and `audit_governance.anchor_scheme_version must be 1`.

Append to the `validateRuntimeLimits` checks list (`validate.go:130`), after `validateAuditGovernance`:

```go
		validateDataRetention(c.DataRetention), validateAuditGovernance(c.AuditGovernance),
		validateAuditAnchor(c.AuditGovernance),
		c.validateCommercialIntegrations(),
```

Entry-point rationale (F5): the runtime-limits chain, not `validateCommercialIntegrations` — the knob-matrix precedent pins the split, and the anchor check is Enabled-independent exactly like `dead_letter_retention`.

### 3.4 YAML surface — `configs/config.yaml:174-186`

Insert into the `audit_governance` block, after `schema_version: 1`:

```yaml
  anchor_scheme: uuidv5-nul-separated  # deterministic event_id derivation for audit facts (idgen.Anchor: RFC-4122 v5 over NUL-separated identity parts, fixed namespace 38248e3a-32f0-4929-9b56-b2dbd75aa849); constant forever — must match the compiled-in contract, validated at Load()
  anchor_scheme_version: 1             # derivation version; only 1 exists; a derivation change must bump this and fail Load() on mismatch
```

### 3.5 No other production changes

`scripts/check-anchored-event-ids.sh`, the publisher, `callback_audit.go`, the ledger, and all connectors are untouched (REQ-5). The 14-row registry (T-10.1-A-2) is enforced by the existing gate; this design adds no new minting site.

## 4. Compatibility constraints

1. **Zero-value convention (AGENTS.md):** `anchor_scheme` empty and `anchor_scheme_version <= 0` are accepted and normalize to `uuidv5-nul-separated` / `1`. Every existing config — including `configs/config.yaml` and every test fixture — omits both keys and boots unchanged. The T-5-1 "defaults with relay enabled" cell pins this.
2. **Declaration, not switch:** validation accepts exactly the compiled-in value. There is no "future scheme" support path; a derivation change is a code change plus a config change in the same release. This is deliberate — it is what turns "silently invalidate every historical derived event_id across a rollout" into a boot-time error.
3. **Enabled-independence:** the check fires even with `audit_governance.enabled: false`, because the ledger dedup guard runs on every append regardless of the relay (E2). A disabled relay must not silently accept a derivation contradiction.
4. **Env cannot bypass:** `AERO_ID_AUDIT_GOVERNANCE_ANCHOR_SCHEME` / `AERO_ID_AUDIT_GOVERNANCE_ANCHOR_SCHEME_VERSION` merge via viper into the same struct and hit the same validation (F2). Env wins over file values (viper precedence), so a bad env value cannot be masked by the file.
5. **No raw-value echo:** errors carry stable dotted paths and quoted accepted values, never the offending input (no-echo rule).
6. **Cross-layer parity is test-only:** `internal/config` production code stays free of `idgen` and connector imports; parity pins live in the in-package tests (`package config`), where importing `internal/pkg/idgen` (F4) and `internal/connector/auditgovernance` (precedent, `commercial_test.go:9`) is cycle-free.
7. **Error substrings are stable contract:** tests assert `strings.Contains` on the exact strings above; renaming the scheme string requires touching the constant, both error strings, the yaml comment, and the parity test — all in one commit.
8. **Config loaded once per boot** (`config.go:380`); scheme changes apply at next boot by construction. No hot reload.

## 5. Failure modes

| # | Failure mode | Behavior | Design response |
|---|---|---|---|
| FM-1 | Config declares `anchor_scheme: uuidv4-random` (or any non-`uuidv5-nul-separated`) | `Load` fails; `cmd/server` and `cmd/migrate` refuse to boot (F3) with `audit_governance.anchor_scheme must be "uuidv5-nul-separated"` | Intended contract: a deployment whose config contradicts the compiled-in derivation cannot start and cannot silently break ledger dedup across a rollout (E6 hazard) |
| FM-2 | Config declares `anchor_scheme_version: 2` | `Load` fails with `audit_governance.anchor_scheme_version must be 1` | Version is part of the contract; a derivation change must bump code + config together (compat §2) |
| FM-3 | Env override typo (`AERO_ID_AUDIT_GOVERNANCE_ANCHOR_SCHEME=uuidv5`) | Env wins over the file; merged value invalid → `Load` fails | Documented accepted value; the error names the accepted scheme. Env cannot bypass validation (compat §4) |
| FM-4 | Future derivation change (new namespace or encoding) shipped without a scheme bump | CI: parity test fails (T-10.1-A-1: namespace ≠ documented constant, or `Anchor` no longer v5); deployments: existing configs fail at boot if the accepted string changed | The load-time invariant: no deployment can boot half-converted. Rollback: old binary + old config agree again — no data repair needed because no second row was ever minted (boot refused) |
| FM-5 | Namespace rotation attempt | Gate R-2 bans redefinition (`check-anchored-event-ids.sh`); parity test T-10.1-A-1(b) pins the constant | Defense in depth: static gate at CI, Load-time check at deployment |
| FM-6 | Wrong scheme with relay disabled | Still rejected (Enabled-independent, compat §3) | Ledger dedup runs without the relay; the check protects the ledger itself |
| FM-7 | Typo'd key name (e.g. `anchor_schem_version`) | viper ignores unknown keys → defaults apply, `Load` succeeds | Known viper-wide limitation shared by every existing key; not fixable without strict-unmarshal, which is out of scope. Mitigation: yaml docs + the pinned env legs. Documented residual risk |
| FM-8 | Existing deployments / test fixtures that omit the keys | Accepted; effective defaults `uuidv5-nul-separated`/`1` applied at Load | Zero-config-change rollout; T-5-1 defaults cells + existing `config_test.go` suite stay green pin this |

## 6. Migration steps

No database migration, no data migration, no proto changes. Steps in order:

1. **Fields + normalization** (§3.1, §3.2): add the two struct fields with doc comments; add `applyAuditAnchorDefaults`; wire it into `Load` between `Unmarshal` and `validate`.
2. **Validation** (§3.3): add constants + `validateAuditAnchor`; append to `validateRuntimeLimits`.
3. **YAML docs** (§3.4): update `configs/config.yaml`.
4. **Configs-module tests**: T-5-1 validation matrix, T-10.1-A-1 parity test, T-10.2-1 defaults parity (new `internal/config/audit_governance_test.go` or `config_test.go` additions; in-package).
5. **Integration leg**: T-10.1-A-2 table-driven anchored-append-once registry test (`internal/repository`, `//go:build integration`, `testpg` harness per `anchor_dedup_integration_test.go:22-29`).
6. **Gate run**: `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-integration-compile && make build` plus `make test-integration` (T-10.1-A-2 touches the ledger append path).
7. **Rollout**: no coordination required for existing deployments — absent keys default to the compiled-in scheme (FM-8). A future derivation change must ship code + config in one release and deploy config first or atomically (FM-4); a failed boot is the safe failure.

## 7. Testable acceptance mapping

Acceptance is mapped one-to-one; each check has a configs-module leg (new, this deliverable) and a cross-layer leg (pinned to existing tests — with the corrected AT-4 pin).

| Acceptance (supplied) | Configs leg (new) | Cross-layer leg (pinned) |
|---|---|---|
| **T-10.1-A**: generalize `TestReportResolveAnchoredEventIDAppendsOnce` to every converted mutation | **T-10.1-A-1** `TestAuditAnchorSchemeParity` (in-package, test-only `internal/pkg/idgen` import): (a) `anchorSchemeUUIDv5NulSeparated == "uuidv5-nul-separated"` and equals the string `validateAuditAnchor` accepts; (b) `idgen.AnchorNamespace == "38248e3a-32f0-4929-9b56-b2dbd75aa849"`; (c) `idgen.Anchor` is a stable RFC-4122 v5 ID (version 5 + RFC variant bits), identical across calls, different when any part changes, and NUL-separation is real: `Anchor("ab","c","x","y") != Anchor("a","bc","x","y")` (precedent `TestAnchor_StableAcrossCalls`, `idgen_test.go:47-52`) | **T-10.1-A-2** table-driven integration test over the 14-row registry (requirements §5): two `Append`s with the identical fact → second returns `repository.ErrAuditDuplicate`; exactly 1 `audit_ledger` row; exactly 1 `audit_publications` row; `chain_sequence` unchanged. Precedents: `TestAnchoredEventIDAppendsOnce` (`anchor_dedup_integration_test.go:35`), `TestReportResolveAnchoredEventIDAppendsOnce` (`comment_report_anchor_integration_test.go:22`) |
| **T-5**: forced duplicate event_id → `ErrAuditDuplicate` → whole tx rollback + service maps to success | **T-5-1** `TestAuditAnchorLoadMatrix` (`writeTemp` pattern `config_test.go:37-46`, substring assertions): defaults (scheme/version absent, `enabled: false`) → `Load` OK, effective `AnchorScheme == "uuidv5-nul-separated"`, `AnchorSchemeVersion == 1`; defaults with relay enabled (valid fixture) → `Load` OK (regression); explicit correct scheme → OK; `anchor_scheme: uuidv4-random` → error containing `anchor_scheme must be "uuidv5-nul-separated"`; `anchor_scheme_version: 2` → error containing `anchor_scheme_version must be 1`; **wrong scheme + `enabled: false` → still error** (Enabled-independent); env cannot bypass (`AERO_ID_AUDIT_GOVERNANCE_ANCHOR_SCHEME=uuidv4-random` + file carrying `anchor_scheme: uuidv5-nul-separated` → error); env correct (env `uuidv5-nul-separated` + file `uuidv4-random` → `Load` OK; merged value wins only when valid). Env legs follow the F2 harness rule: base YAML must carry the anchor keys | **T-5-2** (no new behavior): `TestAuditWiringRollbackAtomicity` (`audit_wiring_integration_test.go:175`), `TestAuditLedgerDuplicateMapsToSuccess` (`audit_ledger_test.go:91`), `TestRecordAnchoredEventID_Dedup` (`audit_ledger_test.go:127`) stay green |
| **T-10.2**: retried governance publish carries identical event_id/idempotency_key (AT-4) while the WORM chain is unchanged | **T-10.2-1** `TestAuditAnchorDefaultsParity`: `Load`'s effective defaults equal the connector's `defaultConfig` defaults (`publisher.go:51-77`): `schema_id == "aero.id.audit-fact"`, `schema_version == 1`, `anchor_scheme == "uuidv5-nul-separated"`, `anchor_scheme_version == 1`. Literal-constant parity with a connector-pinning comment (the connector's defaults are unexported; constructing a `Publisher` cannot read them back — importing the connector for behavior parity, as `commercial_test.go:296-322` does, is not possible for unexported defaulting, so literal pinning is the T-10.1.6-style mirror; rejected alternative: exporting `DefaultConfig()` from the connector — unnecessary connector API change) | **T-10.2-2** (corrected pins): (a) **`TestPublish401InvalidatesAndRefetchesExactlyOnce`** (`internal/connector/auditgovernance/token_invalidation_test.go:60` — the AT-4 test; **the name `TestRetryAfter401CompletesWithRefetchedToken` cited in the requirements doc and 401 design doc `:429` does not exist**): same fact published twice across a 401/refetch carries identical `event_id`/`idempotency_key` == `fact.ID.String()` (`:142-147`); (b) `TestReportResolveAnchoredEventIDAppendsOnce` asserts exactly 1 `audit_publications` row per anchored fact → retried command's publish is a single delivery; (c) WORM chain: `audit_anchor.go:41-42` anchors over `record_hash`; ledger dedup prevents a second row, so `chain_sequence`/`PreviousHash`/`RecordHash` are unchanged (asserted in T-10.1-A-2) |

**Gate regression (REQ-5):** `make check-anchored-event-ids` stays green (no new minting site); `make check-audit-swallow` stays green (no audit-path change); `make check-integration-compile` covers the new integration test.

## 8. Engineering constraints

- New/edited non-test files stay ≤ 500 lines, functions ≤ 50 lines, cyclomatic ≤ 10: `validateAuditAnchor` is ~12 lines; `applyAuditAnchorDefaults` ~8; `audit_governance.go` grows by ~35 lines (currently 34) — still far under limits.
- Tests are in-package (`package config`, matching all existing config tests); the only new cross-package import is test-only (`internal/pkg/idgen`, leaf — F4) plus the already-precedented `internal/connector/auditgovernance` for behavior-parity cells (`commercial_test.go:9`).
- Integration test follows the existing `//go:build integration` + `testpg` harness (`anchor_dedup_integration_test.go:22-29`); `make test-integration` once (T-10.1-A-2 is net-new integration coverage).
- Error messages are stable dotted config paths with testable substrings; no raw value echoed (§3.3, compat §5).
- No new packages, no new Makefile gates, no proto, no migration (requirements §6 non-goals).

## 9. Open decisions (resolved with rationale)

1. **Field names** `anchor_scheme` / `anchor_scheme_version` ↔ `AnchorScheme` / `AnchorSchemeVersion`; scheme identifier `uuidv5-nul-separated` — matches the encoding the `idgen.Anchor` contract realizes (v5 over NUL-separated parts). **RESOLVED** (requirements §9.1).
2. **Constant visibility:** unexported `anchorSchemeUUIDv5NulSeparated` in `validate.go` — all tests are in-package, so no exported API surface is needed. **RESOLVED** (adds nothing to the module's public API; the requirements doc's `AnchorSchemeUUIDv5NulSeparated` name is realized unexported).
3. **Default normalization:** `applyAuditAnchorDefaults` in `Load` — required by T-5-1's defaults cell and T-10.2-1; documented as a narrow exception to the raw-knob convention (F1). **RESOLVED** — without it, "effective value on the loaded config" is untestable and the defaults-parity leg degrades to doc-comment.
4. **AT-4 pin name:** use the real test `TestPublish401InvalidatesAndRefetchesExactlyOnce` (token_invalidation_test.go:60); optionally update the 401 design doc's AT-4 row (`docs/plans/2026-08-06-cmd-server-tokensource-invalidate-on-401-design.md:429`) to the real name in the same commit. **RESOLVED** — the requirements doc's cited name does not exist (E6 correction).
5. **T-10.2-1 parity style:** literal-constant assertion in the config test with a connector-pinning comment (not a connector import at runtime, not an exported `DefaultConfig()`); the connector's defaulting is unexported and unreadable post-construction. **RESOLVED**.
6. **Test file placement:** new `internal/config/audit_governance_test.go` for T-5-1/T-10.1-A-1/T-10.2-1 (sibling of `audit_governance.go`, matching the per-file test convention); implementer may instead extend `config_test.go`. **RESOLVED** (either satisfies acceptance).
