# Requirements specification — config-declared anchor scheme for deterministic audit event_id (B2 requirement 4)

> Module: `configs` (direction contribution: the anchor-contract declaration and Load()-time
> validation on `audit_governance`, so deterministic-event_id dedup stability is a load-time
> invariant rather than per-call luck).
> Source analysis: `docs/auto/analyses/configs-1fa2a926.json` (item 3).
> Date: 2026-08-08. Status: every cited file/symbol re-verified against the repository at HEAD.

## 1. Direction

Dedup infrastructure is verified: unique `(tenant_id, source, event_id)` constraint with
`checkAuditDuplicate` → `ErrAuditDuplicate`, plus anchored pre-assigned event_ids for account
provisioning (`event_id = accountID`) and report resolution (T-10.1-A). The general append path
still defaults `fact.ID` from a random UUIDv7 when unset, so the direction proposes a
deterministic anchor-derived event_id for all facts. The configs-module contribution: **document
and validate the anchor contract** — a config-declared anchor scheme/version alongside
`audit_governance.schema_id` — so dedup stability is a load-time invariant.

Scope is strictly the configs-module contribution: the anchor scheme/version declaration,
its Load()-time validation, the parity pins that tie it to the compiled-in derivation, and the
documentation of the contract and its site registry. The repo/service acceptance legs (T-10.1-A,
T-5, T-10.2) are preserved verbatim and decomposed into testable cases below, with the
configs-module owning the load-time-invariant leg of each.

## 2. Evidence verification

Every citation in the direction was checked against the tree. All symbols exist; two line
references needed correction and one factual claim is partially stale (marked). Nothing cited was
missing.

| # | Claim (direction evidence) | Verification result | Current location |
|---|---|---|---|
| E1 | Unique `(tenant_id, source, event_id)` constraint with `checkAuditDuplicate` → `ErrAuditDuplicate` | ✅ Confirmed (line drift). `checkAuditDuplicate` spans `:174-191` (direction cited :165-187); zero-`EventID` short-circuit at `:176-178`; triple query `tenant_id = ? AND source = ? AND event_id = ?` at `:180-186`. `ErrAuditDuplicate` declared at `:59` (`"audit ledger: duplicate source event"`). The INSERT-side 23505 → `ErrAuditDuplicate` mapping is constraint-precise on `uq_audit_ledger_tenant_event` (`:204-210`) — any other 23505 stays raw so the tx rolls back. | `internal/repository/audit_ledger.go:59,174-191,204-210`; `migrations/000050_scope_events_and_audit_by_tenant.up.sql:15-16` |
| E2 | General append path derives event_id from a random UUIDv7 (`fact.ID = idgen.NewID()`) | ✅ Confirmed with a precision correction. The assignment is at `:161` (direction cited :151). **Precision:** this is the **row PK** (`fact.ID`), not `EventID`; `EventID` stays zero and `checkAuditDuplicate` short-circuits (`:176-178`), so a retry appends a new row with a new PK, a bumped `chain_sequence` (`:164-166`), and a new `audit_publications` row (`:217`). The direction's substance — the unanchored path cannot dedup — holds exactly. | `internal/repository/audit_ledger.go:140,161,164-166,176-178,217` |
| E3 | Anchored pre-assigned event_ids: account provisioning and report resolution; "a deterministic anchor-derived event_id for all facts is proposed, not verified" | ⚠️ **Partially stale — the derivation is already implemented at 12+ sites and gate-enforced; only the config declaration is missing.** Verified anchored sites: pre-assigned v7 receipts — `account.provisioned` (`EventID: accountID`, `internal/service/account_provision.go:157`; doc `internal/repository/account_provision_audit.go:15-21`), `account.backfill` (`account_backfill.go:121`), `operation.requested` (`operation_audit.go:14`), `sync.job.requested` (`sync_audit.go:13`), `callback.registered`/`callback.deleted` (`callback.go:169,226`), `comment.report.resolve` (via the Log adapter `audit_ledger.go:151` `EventID: legacy.ID`, set to `reportID` by the comment service), event ingest (`event_ingest.go:160`, `event_receipt.go:190` `EventID: envelope.EventID`), replay (`replay.go:147,244`); `idgen.Anchor`-derived v5 — `operation.step.finished`/`operation.finished`/`compensated`/`reconciled` (uniform 5-tuple, `operation_audit.go:60-68`), `sync.job.finished` (`sync_audit.go:44`), `audit_hold.set`/`audit_hold.removed` (`audit_hold.go:164,184`, incl. `RenderAnchorTime` R7), `legacy.cutover.set` (`legacy_cutover.go:143`). Derivation primitive: `idgen.Anchor` (UUIDv5, fixed namespace `38248e3a-32f0-4929-9b56-b2dbd75aa849`, NUL-separated parts, arity ≥ 4, panic on NUL/zero parts) — `internal/pkg/idgen/idgen.go:12-44`, contract reference "derive-stable-deterministic-event-id-from-an-anc §2.1/§2.2 R1–R7". Gate: `scripts/check-anchored-event-ids.sh` (R-1: `EventID: idgen.NewID()` banned outside the C2 allowlist — `internal/service/callback.go` ReplayDelivery, `cmd/audit-replay/main.go` requeueFact, `internal/service/audit_fact.go` buildLegacyAuditFact; R-2: namespace defined exactly once). **Gate verified green at HEAD.** What is genuinely unverified/unpinned: no config-declared anchor scheme/version exists, and no Load()-time check ties the declared scheme to the compiled-in derivation. | registry above; `scripts/check-anchored-event-ids.sh:1-22`; gate run `✓` at HEAD |
| E4 | `internal/service/audit_anchor.go` — WORM anchor over `record_hash`, not event_id | ✅ Confirmed. The WORM anchor documents `AnchorKey` + `RecordHash` (`:41,:78`); the chain links via `PreviousHash`/`ChainSequence` (`audit_ledger.go:164-166`). `event_id` is the **dedup key**; `record_hash` is the **tamper-evidence chain** — two orthogonal identifiers, exactly as the direction states. | `internal/service/audit_anchor.go:41,78,105,122`; `internal/repository/audit_ledger.go:164-166` |
| E5 | `configs/config.yaml` `audit_governance.schema_id` | ✅ Confirmed. `schema_id: aero.id.audit-fact`, `schema_version: 1` at `configs/config.yaml:179-180`. **Gap verified:** `AuditGovernanceConfig` (`internal/config/audit_governance.go:8-28`) has `SchemaID`/`SchemaVersion` but **no anchor scheme/version field**; `validateAuditGovernance` (`internal/config/validate.go:211-214`) validates **only** `dead_letter_retention ≥ 0` — no schema or anchor validation at Load(); connector-side defaults silently fill empties (`internal/connector/auditgovernance/publisher.go:55-77` `defaultConfig`: `schema_id`→`"aero.id.audit-fact"`, `schema_version`→1, `validConfig` at `:79-81`). | `configs/config.yaml:174-186`; `internal/config/audit_governance.go:8-28`; `internal/config/validate.go:207-214`; `publisher.go:55-81` |
| E6 | Retried governance publish carries identical `event_id`/`idempotency_key` (AT-4) | ✅ Confirmed. Governance payload `event_id: fact.ID.String()` (`internal/service/callback_audit.go:32`); AT-4/C-4: `IdempotencyKey = fact.ID.String()` (`docs/plans/2026-08-06-cmd-server-tokensource-invalidate-on-401-design.md:355,429`); pinned by `TestRetryAfter401CompletesWithRefetchedToken` (`internal/connector/auditgovernance/publisher_test.go`). **Nuance (documented, not changed):** the governance `event_id` is the ledger row PK (`fact.ID`), not the anchored `EventID`; the anchored `EventID` is the *ledger* dedup key. Because a retried command dedups at the ledger (one row, one publication — `comment_report_anchor_integration_test.go` asserts exactly 1 publication row), the single publication's `event_id`/`idempotency_key` are stable across retries. A scheme change between boots would mint a different `EventID` for the same logical fact, append a second row with a **new** `fact.ID`, and break AT-4 stability across restarts — the exact hazard the Load()-time scheme validation closes. | `callback_audit.go:32`; 401 design `:355,:429`; `comment_report_anchor_integration_test.go:22-60` |
| E7 | T-10.1-A precedent `TestReportResolveAnchoredEventIDAppendsOnce` | ✅ Confirmed. `internal/repository/comment_report_anchor_integration_test.go:22` — two `Append`s with identical anchored `EventID` → second returns `ErrAuditDuplicate`, exactly 1 `audit_ledger` row, 1 `audit_publications` row, `chain_sequence` unchanged. Sibling precedent `TestAnchoredEventIDAppendsOnce` (`anchor_dedup_integration_test.go:32-53`, T-5.2) uses the same pattern with `idgen.Anchor`. | `comment_report_anchor_integration_test.go:22-60`; `anchor_dedup_integration_test.go:32-53` |
| E8 | T-5 precedent `TestAuditLedgerDuplicateMapsToSuccess` | ✅ Confirmed. `internal/service/audit_ledger_test.go:91-104` — `ErrAuditDuplicate` maps to `nil` at the service layer; every other error propagates. `Record` mapping at `internal/service/audit_ledger.go:68-79`. Rollback-atomicity twin: `TestAuditWiringRollbackAtomicity` (`internal/repository/audit_wiring_integration_test.go:175`) forces a duplicate `event_id` → `ErrAuditDuplicate` → whole tx rollback with domain row unchanged. Service duplicate contract: `TestRecordAnchoredEventID_Dedup` (T-5.1, `audit_ledger_test.go:127-131`). | `audit_ledger_test.go:91-131`; `audit_wiring_integration_test.go:175`; `audit_ledger.go:68-79` |
| E9 | Config-module test infrastructure for the parity pattern | ✅ Confirmed. `TestAuditGovernanceGateMatrix` (`internal/config/commercial_test.go:77`), `TestAuditGovernanceKnobGateMatrix` (T-10.1.7, `:123` — pins the entry-point split: knob validation lives in `validateRuntimeLimits` at `validate.go:125-138`, **not** `validateCommercialIntegrations`), `TestLoadAuditGovernanceTokenURLGate` (T-10.1.4, `config_test.go:151`), `TestAuditGovernanceDeadLetterRetentionValidation` (`config_test.go:279`), env-legs `TestLoadAuditGovernanceKnobEnvLegs` (T-10.1.9, `:312`), YAML fixture `auditGovernanceBadYAML` (`config_test.go:115-138`), parity-pin precedent `validAuditTokenURL` (T-10.1.6, `commercial.go:87`, no-echo rule `:85-87`). | `commercial.go:56-107`; `commercial_test.go:77,123`; `config_test.go:115-138,151,279,312` |
| E10 | C2 fresh-mint sites are by contract, not accidents | ✅ Confirmed. `internal/service/audit_fact.go:22-45` (`buildLegacyAuditFact`: the eight legacy-Log admin/ban/block/file-share sites mint a fresh v7 per call by B2-1 REQ-3.3 — "a stable reused ID across distinct intents would let ErrAuditDuplicate roll back a legitimate domain write"), `internal/service/callback.go:263` (`callback.delivery.replayed`), `cmd/audit-replay/main.go:204` (requeueFact). `internal/config` has zero anchor symbols today (grep: no hit). | `audit_fact.go:22-45`; `callback.go:263`; `cmd/audit-replay/main.go:204` |

**Conclusion:** the direction's problem statement is accurate, with one material update — the
deterministic derivation is **already implemented and gate-enforced** at 12+ sites (E3); the
genuinely missing piece is exactly the configs-module contribution: a declared anchor
scheme/version validated at Load() so the dedup-stability contract is a boot-time invariant
(E5). The acceptance checks' substance is fully testable against existing infrastructure
(E7/E8/E9).

## 3. Gap summary

| Capability | Today | Required |
|---|---|---|
| Deployment declares which event_id derivation contract audit facts use | No — the scheme is implied by `idgen.AnchorNamespace` + the static gate; `audit_governance` declares only `schema_id`/`schema_version` (E5) | `audit_governance.anchor_scheme` + `anchor_scheme_version` declared in config and yaml |
| Reject a config that contradicts the compiled-in derivation | No validation — a future namespace/derivation change would silently invalidate every historical derived event_id and stop ledger dedup across a rollout (E3: the namespace is "constant forever" by idgen contract) | `validateAuditAnchor` at Load(), Enabled-independent, stable error substrings, no raw-value echo (E9 parity-pin pattern) |
| Tie dedup stability to a load-time invariant | Per-call luck: each site derives correctly because the gate greps code; nothing checks a deployment's config against the derivation | Parity pin: the accepted scheme constant equals the scheme the `idgen.Anchor` implementation realizes (namespace + v5 encoding + NUL separation) |
| Document the anchor contract on the config surface | `configs/config.yaml` documents schema_id but nothing about event_id derivation (E5) | yaml + struct doc comments carrying the load-bearing rules (scheme constant forever, retry-dedup semantics, EventID vs fact.ID vs record_hash separation) |

## 4. Requirements

### REQ-1 — CFG-1: anchor scheme declaration on `AuditGovernanceConfig`

Add to `internal/config/audit_governance.go` (next to `SchemaID`/`SchemaVersion`, lines 13-14):

```go
// AnchorScheme identifies the deterministic event_id derivation contract for
// audit facts (idgen.Anchor: RFC-4122 v5 UUID over NUL-separated identity
// parts in the fixed AnchorNamespace 38248e3a-32f0-4929-9b56-b2dbd75aa849;
// derivation contract derive-stable-deterministic-event-id-from-an-anc
// §2.1/§2.2 R1-R7). The scheme is load-bearing: every retried mutation relies
// on the ledger dedup key (tenant_id, source, event_id) being re-derivable,
// so the declared scheme must match the compiled-in derivation. The namespace
// and encoding are constant forever: never rotate the namespace and never
// change the derivation without a scheme-version bump that fails at Load().
AnchorScheme        string `mapstructure:"anchor_scheme"`
AnchorSchemeVersion int    `mapstructure:"anchor_scheme_version"`
```

Semantics:
- `anchor_scheme: uuidv5-nul-separated` (compiled-in default; `0`/empty = default per the
  AGENTS.md zero-value convention — a config that omits the field keeps today's behavior).
- `anchor_scheme_version: 1` (only version that exists; `<= 0` = default 1).
- The fields are **declarations of an existing contract, not switches**: validation accepts
  exactly the compiled-in value and rejects every other value (REQ-2). They exist so a rollout
  that changes the derivation cannot boot with a stale config, and so deployments document the
  contract explicitly next to `schema_id`.
- Environment overrides `AERO_ID_AUDIT_GOVERNANCE_ANCHOR_SCHEME` /
  `AERO_ID_AUDIT_GOVERNANCE_ANCHOR_SCHEME_VERSION` follow the existing viper mechanism
  (`config.go:378-384`) and merge into the same validated struct (env cannot bypass validation,
  mirroring T-10.1.4's env legs).

`configs/config.yaml` `audit_governance` block (lines 174-186) gains, next to `schema_id`:

```yaml
  anchor_scheme: uuidv5-nul-separated # deterministic event_id derivation for audit facts (idgen.Anchor v5 over NUL-separated parts, fixed namespace); must match the compiled-in contract
  anchor_scheme_version: 1            # derivation version; only 1 exists; a derivation change must bump and fail Load() on mismatch
```

*Acceptance: T-10.1-A (config leg), T-5 (regression cells).*

### REQ-2 — CFG-2: Load()-time validation, Enabled-independent

Add `validateAuditAnchor(value AuditGovernanceConfig) error` in `internal/config/validate.go`
and append it to the checks list in `validateRuntimeLimits` (`validate.go:125-138`) — the
runtime-limits chain, **not** `validateCommercialIntegrations`, mirroring the entry-point split
already pinned by `TestAuditGovernanceKnobGateMatrix` (T-10.1.7, `commercial_test.go:123`).
Rules (error substrings are the testable contract; error never echoes the raw value — the
`validAuditTokenURL` no-echo rule, `commercial.go:85-87`):

1. `AnchorScheme` empty → accepted, default `uuidv5-nul-separated` (zero-value convention).
2. `AnchorScheme` non-empty and != `uuidv5-nul-separated` → error
   `audit_governance.anchor_scheme must be "uuidv5-nul-separated"`.
3. `AnchorSchemeVersion <= 0` → accepted, default 1.
4. `AnchorSchemeVersion != 1` → error `audit_governance.anchor_scheme_version must be 1`.

**Enabled-independence is load-bearing:** the ledger dedup guard runs on every append
(`checkAuditDuplicate` in `appendInTx`, `audit_ledger.go:141-143`) regardless of whether the
governance relay is enabled, so the scheme check must reject a wrong scheme even when
`audit_governance.enabled: false` — exactly the property `TestAuditGovernanceKnobGateMatrix`'s
"disabled negative retention rejected" cell pins for `dead_letter_retention`. A disabled relay
must not silently accept a derivation contradiction.

*Acceptance: T-5 (validation matrix).*

### REQ-3 — CFG-3: parity pin — the declared scheme equals the compiled-in derivation

The load-time invariant is only real if the accepted string is tied to the implementation it
describes. Add a parity test in `internal/config` (test-only import of `internal/pkg/idgen` —
a leaf package with no cycle; the runtime validation stays dependency-free):

- `AnchorSchemeUUIDv5NulSeparated` (the accepted scheme constant) equals the validation's
  accepted string.
- `idgen.AnchorNamespace` equals the fixed namespace `38248e3a-32f0-4929-9b56-b2dbd75aa849`
  that the scheme name documents — a namespace rotation without a scheme bump fails this test.
- `idgen.Anchor` produces an RFC-4122 v5 UUID (version/variant bits set), is stable across
  calls, and changes when any part changes (`TestAnchor_StableAcrossCalls` precedent,
  `internal/pkg/idgen/idgen_test.go:47-52`).
- The parity test mirrors the `validAuditTokenURL` cross-layer parity precedent (T-10.1.6,
  `commercial.go:87`): the config layer's accepted value must match the layer that implements
  the contract.

This is the "load-time invariant rather than per-call luck" half: per-site correctness stays
enforced by `make check-anchored-event-ids` (R-1/R-2), and now the deployment's declaration is
checked against the same contract at boot.

*Acceptance: T-10.1-A (config leg).*

### REQ-4 — CFG-4: documentation of the contract on the config surface

- Struct doc comments as in REQ-1 (scheme constant forever; dedup-key semantics; three
  identifiers not to be conflated: anchored `EventID` = ledger dedup key, `fact.ID` = ledger
  row PK and governance `event_id`/`idempotency_key`, `record_hash` = WORM tamper-evidence
  chain — E4/E6).
- `configs/config.yaml` comments as in REQ-1.
- This spec's §5 registry table is the canonical site list for review; it must not drift from
  `make check-anchored-event-ids` (the gate is the enforcement, the table is the map).

*Acceptance: T-10.2 (documentation leg).*

### REQ-5 — Scope boundary: the ledger append default and the C2 sites are untouched

- The `fact.ID = idgen.NewID()` fallback (`audit_ledger.go:161`) stays: it is the row PK, and
  the zero-`EventID` short-circuit (`:176-178`) is the documented legacy path.
- The C2 fresh-mint sites (`audit_fact.go` eight legacy-Log sites, `callback.go:263`
  `callback.delivery.replayed`, `cmd/audit-replay/main.go:204` requeueFact) stay fresh-mint by
  B2-1 REQ-3.3 contract (E10) — the config declaration does not convert them, and no code in
  `internal/config` may mint or coerce EventIDs.
- No change to the governance payload contract (`callback_audit.go:32`), the publisher
  (`publisher.go`), the schema_id/schema_version defaulting (connector-owned, E5), the ledger
  schema, or `checkAuditDuplicate`.

*Acceptance: gate regression (T-5, T-10.2).*

## 5. Acceptance

The supplied acceptance checks are preserved verbatim, then decomposed into deterministic,
testable cases. Each check has a **configs-module leg** (this direction's deliverable) and a
**cross-layer leg** (pinned against existing tests; the config leg is what makes them
boot-time guarantees).

> **T-10.1-A (supplied):** generalize `TestReportResolveAnchoredEventIDAppendsOnce` to every
> converted mutation: retried command appends with an identical deterministic event_id and
> lands once.

**T-10.1-A-1 (configs leg, `internal/config`): the scheme declaration pins the derivation
contract for every converted mutation.** `TestAuditAnchorSchemeParity` (unit):
(a) the accepted scheme constant equals the string `validateAuditAnchor` accepts;
(b) `idgen.AnchorNamespace` == `38248e3a-32f0-4929-9b56-b2dbd75aa849`; (c) `idgen.Anchor(parts...)`
is a stable RFC-4122 v5 ID that changes when any part changes. Rationale: every converted
mutation in the registry below derives its `event_id` from this one primitive; a single parity
test at Load()-time-adjacent scope pins the whole registry.

**T-10.1-A-2 (cross-layer leg, preserved; `internal/repository`, integration tag):**
generalize the anchored-append-once pattern into a table-driven integration test over the
converted-mutation registry (precedent: `TestAnchoredEventIDAppendsOnce`,
`anchor_dedup_integration_test.go:32-53`, and `TestReportResolveAnchoredEventIDAppendsOnce`,
`comment_report_anchor_integration_test.go:22`). For each row — action, anchor parts (or
pre-assigned ID), tenant, source — two `Append`s with the identical fact must yield: second
append returns `repository.ErrAuditDuplicate`; exactly 1 `audit_ledger` row; exactly 1
`audit_publications` row; `chain_sequence` unchanged after the second attempt.

Registry (verified, E3; the "every converted mutation" set):

| # | Action | EventID derivation | Site |
|---|---|---|---|
| 1 | `account.provisioned` | pre-assigned `accountID` | `account_provision.go:157`, `account_provision_audit.go:15-21` |
| 2 | `account.backfill` | pre-assigned `accountID` | `account_backfill.go:121` |
| 3 | `comment.report.resolve` | pre-assigned `reportID` | Log adapter `audit_ledger.go:151` |
| 4 | `operation.requested` | pre-assigned `operation.ID` | `operation_audit.go:14` |
| 5 | `operation.step.finished`, `operation.finished`, `operation.compensated`, `operation.reconciled` | `idgen.Anchor(action, tenant, operation, step-or-"", status)` | `operation_audit.go:60-68` |
| 6 | `sync.job.requested` | pre-assigned `job.ID` | `sync_audit.go:13` |
| 7 | `sync.job.finished` | `idgen.Anchor("sync.job.finished", tenant, job.ID, status)` | `sync_audit.go:44` |
| 8 | `audit_hold.set` | `idgen.Anchor(..., RenderAnchorTime(expiresAt))` | `audit_hold.go:164` |
| 9 | `audit_hold.removed` | `idgen.Anchor(...)` | `audit_hold.go:184` |
| 10 | `legacy.cutover.set` | `idgen.Anchor("legacy.cutover.set", tenant, status, previous)` | `legacy_cutover.go:143` |
| 11 | `callback.registered` | pre-assigned `subscription.ID` | `callback.go:169` |
| 12 | `callback.deleted` | pre-assigned `id` | `callback.go:226` |
| 13 | event ingest receipt | source-native `envelope.EventID` | `event_ingest.go:160`, `event_receipt.go:190` |
| 14 | audit replay | pre-assigned `replayID` / `operation.ID` | `replay.go:147,244` |

Rows 1-14 are the *converted mutations*; rows 5/7/8/9/10 are the `idgen.Anchor` sites whose
derivation the config declaration names. `make check-anchored-event-ids` stays green and is the
regression gate for the registry (R-1/R-2, `scripts/check-anchored-event-ids.sh`).

> **T-5 (supplied):** forced duplicate event_id → `ErrAuditDuplicate` → whole tx rollback with
> domain row unchanged and service mapping to success (`TestAuditLedgerDuplicateMapsToSuccess`
> precedent).

**T-5-1 (configs leg, `internal/config`): the Load()-time validation matrix** (parity pattern of
`TestAuditGovernanceGateMatrix` `commercial_test.go:77` and `TestAuditGovernanceKnobGateMatrix`
`:123`; `writeTemp` YAML pattern `config_test.go:37-46`; substring-error assertions):

| Case | Config | Expected |
|---|---|---|
| defaults | scheme/version absent, `enabled: false` | `Load` succeeds; `AnchorScheme == "uuidv5-nul-separated"`, `AnchorSchemeVersion == 1` |
| defaults with relay enabled | scheme/version absent, valid enabled fixture (`auditGovernanceBadYAML` minus the token-URL defect) | `Load` succeeds (regression: every currently accepted config stays accepted) |
| explicit correct scheme | `anchor_scheme: uuidv5-nul-separated`, `anchor_scheme_version: 1` | `Load` succeeds |
| unknown scheme | `anchor_scheme: uuidv4-random` | `Load` error containing `anchor_scheme must be "uuidv5-nul-separated"` |
| unknown version | `anchor_scheme_version: 2` | `Load` error containing `anchor_scheme_version must be 1` |
| **wrong scheme, relay disabled** | `enabled: false`, `anchor_scheme: uuidv4-random` | `Load` error containing `anchor_scheme` (Enabled-independent — the ledger dedup guard runs without the relay, E2/E3) |
| env cannot bypass | `t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ANCHOR_SCHEME", "uuidv4-random")` + valid file | `Load` error (env merges into the same validated struct, T-10.1.4 precedent) |
| env correct | `t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ANCHOR_SCHEME", "uuidv5-nul-separated")` + file with `anchor_scheme: uuidv4-random` | `Load` error — the *merged* value must be the valid one for success; env override wins only when it is itself valid (mirror `TestLoadAuditGovernanceKnobEnvLegs`, `config_test.go:312`) |

Existing config tests stay green — the new validation must not reject any currently accepted
config (the "defaults with relay enabled" cell pins this).

**T-5-2 (cross-layer leg, preserved): the rollback/success mapping is already pinned and stays
green.** `TestAuditWiringRollbackAtomicity` (`audit_wiring_integration_test.go:175`: forced
duplicate `event_id` → `ErrAuditDuplicate` → whole tx rollback, domain row unchanged),
`TestAuditLedgerDuplicateMapsToSuccess` (`audit_ledger_test.go:91`: service maps
`ErrAuditDuplicate` to success, other errors propagate), `TestRecordAnchoredEventID_Dedup`
(T-5.1, `audit_ledger_test.go:127-131`: identical anchored `Record` appends once, second is
nil). No new behavior required; the config leg (T-5-1) is what makes this contract load-time
stable.

> **T-10.2 (supplied):** retried governance publish carries identical event_id/idempotency_key
> (AT-4) while the WORM anchor chain (record_hash) remains unchanged.

**T-10.2-1 (configs leg, `internal/config`): governance-contract defaults parity.** The
publisher's `defaultConfig` (`publisher.go:55-77`) defaults `schema_id` → `aero.id.audit-fact`,
`schema_version` → 1; a config-parity test asserts `Load`'s defaults for `schema_id`,
`schema_version`, `anchor_scheme`, `anchor_scheme_version` equal the connector's defaults —
mirroring the `validAuditTokenURL` cross-layer parity pin (T-10.1.6). Rationale: AT-4's
identical-`event_id` guarantee across retries is per-boot stable only if the declared contract
(scheme + schema identity) is constant across boots; the validation (T-5-1) rejects a changed
scheme at boot.

**T-10.2-2 (cross-layer legs, preserved):** (a) publisher level — `TestRetryAfter401CompletesWithRefetchedToken`
(`publisher_test.go`, AT-4): the same fact published twice carries identical
`event_id`/`idempotency_key` == `fact.ID.String()` (C-4, 401 design `:355`); (b) ledger level —
`TestReportResolveAnchoredEventIDAppendsOnce` (`comment_report_anchor_integration_test.go:22`)
already asserts exactly **one** `audit_publications` row per anchored fact, so a retried
command's publish is a single delivery whose `event_id`/`idempotency_key` cannot differ;
(c) WORM chain — `internal/service/audit_anchor.go` anchors over `record_hash` (E4), and the
ledger-level dedup (E1) guarantees the retry appends no second row, so `chain_sequence`/
`PreviousHash`/`RecordHash` are unchanged (asserted in T-10.1-A-2 via the `chain_sequence`
stability assertion). No new behavior required.

**Done gate:** `make test && make lint && make lint-strict && make check-file-sizes &&
make check-audit-swallow && make check-anchored-event-ids && make build` plus
`make test-integration` (T-10.1-A-2 touches the ledger append path and the registry table).

## 6. Non-goals (scope boundary)

- **No conversion of the remaining fresh-mint sites** (C2 allowlist: the eight legacy-Log
  admin/ban/block/file-share sites, `callback.delivery.replayed`, `cmd/audit-replay` requeueFact).
  They stay fresh-mint by B2-1 REQ-3.3 contract (E10); "generalize to every converted mutation"
  means every *converted* (deterministic-EventID) mutation, exactly as the acceptance states.
- No change to the ledger append path (`appendInTx`, `checkAuditDuplicate`,
  `fact.ID = idgen.NewID()` fallback), the `(tenant_id, source, event_id)` constraint, or the
  WORM chain (record_hash) computation.
- No change to the governance payload contract (`callback_audit.go:32`) or the publisher;
  no validation of `schema_id`/`schema_version` shape (connector-owned defaulting, E5 — the
  anchor scheme/version is the only new validated pair).
- No migration; no `.proto` changes; no new metrics; no hot reload (config is loaded once at
  boot — `config.go:380` `Load`; scheme changes apply at the next boot by construction).
- No cross-region/anchor-registry runtime enforcement beyond the existing static gate
  (`check-anchored-event-ids`) plus the new Load()-time declaration check.

## 7. Impact map

| File | Change |
|---|---|
| `internal/config/audit_governance.go:13-14` (after) | add `AnchorScheme string` + `AnchorSchemeVersion int` with doc comment (REQ-1) |
| `internal/config/validate.go:125-138` | append `validateAuditAnchor` to `validateRuntimeLimits` checks |
| `internal/config/validate.go` (new func) | `validateAuditAnchor` — REQ-2 rules 1-4 (Enabled-independent) |
| `internal/config/commercial_test.go` or `config_test.go` | T-5-1 validation matrix (defaults/regression/rejections/env legs); T-10.2-1 defaults parity |
| `internal/config` (new test) | T-10.1-A-1 `TestAuditAnchorSchemeParity` (test-only `internal/pkg/idgen` import; no cycle) |
| `configs/config.yaml:174-186` | `anchor_scheme`/`anchor_scheme_version` with comments (REQ-1/REQ-4) |
| `internal/repository/anchor_dedup_integration_test.go` (or new sibling file, integration tag) | T-10.1-A-2 table-driven registry generalization (rows 1-14; precedent `comment_report_anchor_integration_test.go:22`) |
| `scripts/check-anchored-event-ids.sh` | unchanged (registry enforcement already live); gate stays green |

## 8. Engineering constraints

- New/edited non-test files stay ≤ 500 lines, functions ≤ 50 lines, cyclomatic ≤ 10
  (`make check-file-sizes`, `make lint-strict`); `validateAuditAnchor` is a small
  table-free function.
- Unit tests use deterministic fakes and fixtures; the only cross-package import added is
  test-only (`internal/config` test → `internal/pkg/idgen`, a leaf package importing only
  stdlib + `github.com/google/uuid` — no cycle).
- Integration test follows the existing `//go:build integration` + `testpg` harness
  (`anchor_dedup_integration_test.go:22-29`); `make test-integration` once in the implement
  stage (T-10.1-A-2 is net-new integration coverage).
- Error messages are stable dotted config paths with testable substrings; no raw value echoed
  (REQ-2).
- `make check-anchored-event-ids` must stay green — the spec adds no new minting site.

## 9. Open decisions (low risk, implementer choice)

1. **RESOLVED:** field names `anchor_scheme` / `anchor_scheme_version` (yaml) ↔
   `AnchorScheme` / `AnchorSchemeVersion` (struct); scheme identifier string
   `uuidv5-nul-separated` (matches the encoding the `idgen.Anchor` contract realizes: v5 over
   NUL-separated parts).
2. Placement of `validateAuditAnchor`: in `validateRuntimeLimits` (validate.go) — not
   `validateCommercialIntegrations` — pinned by the T-10.1.7 entry-point-split precedent
   (E9). Confirmed by REQ-2's Enabled-independence requirement.
3. Where the parity test file lives: a new `internal/config/audit_governance_test.go` (or
   inside `commercial_test.go`); implementer choice, both satisfy T-5-1/T-10.1-A-1.
4. Whether `TestAuditAnchorSchemeParity` re-derives the namespace from the scheme name or
   compares constants: it must assert `idgen.AnchorNamespace` equals the documented namespace
   constant, and that `Anchor("a","b","c","d")` is a stable v5 ID — the two together pin the
   scheme's meaning.
