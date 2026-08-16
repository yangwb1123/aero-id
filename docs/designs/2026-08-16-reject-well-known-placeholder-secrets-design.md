# Design — Reject Well-Known Placeholder Secrets at `Load()` + Regenerate Shipped Dev Keys

- **Date:** 2026-08-16
- **Module:** `configs` (`internal/config`, `configs/config.yaml`)
- **Direction:** Reject well-known placeholder secrets at `Load()`; regenerate shipped dev keys (audit-chain HMAC forgery)
- **Status:** Design (spec-implementation-ready; no repository modification in this document)
- **Evidence run:** `docs/auto/analyses/configs-1fa2a926.json` (verified independently below)

---

## 1. Verification verdict (untrusted evidence → repository facts)

Every cited claim was re-checked against the working tree. **Verdict: Verified** with two minor line-number drifts that do not change behavior (noted inline).

| # | Claim | Verification result |
|---|---|---|
| E1 | `configs/config.yaml:173` — `audit_ledger.signing_key: change-me-please-very-long-audit-key` (36 bytes, passes the 32-byte gate) | **Confirmed** (`sed -n '165,180p'`). Comment "local-dev placeholder; override with `AERO_ID_AUDIT_LEDGER_SIGNING_KEY`". |
| E2 | `configs/config.yaml:230` — root `signing_key: change-me-please-very-long-random` (33 bytes) | **Confirmed** (`sed -n '224,235p'`). No length check exists for the root key. |
| E3 | `validateAuditLedger` (`validate.go:205-213`) requires non-empty then `len < 32`; root `validateRequired` (`:55-57`) is `!= ""` only | **Confirmed**. `wc -l internal/config/validate.go` = 427. `validateRuntimeLimits` (`:135`) invokes `validateAuditLedger` through the combined validator list. |
| E4 | `internal/repository/audit_ledger.go` — `sign` is HMAC-SHA256 over `record_hash`, hex-encoded; `verifyAuditRow` rejects mismatch via `hmac.Equal` inside the chain-verify loop | **Confirmed**. `createAuditRows` (`:196-199`) sets `RecordHash` then `Signature = sign(...)`; `verifyAuditRow` (`:325-341`) checks linkage/hash/signature-version/`hmac.Equal`. Public key ⇒ whole tamper-evidence chain forgeable. |
| E5 | FM-8 `validateAuditLedgerConfig` (`internal/app/audit.go:19-27`) rejects only `nil`/empty; `buildAuditStack` calls it first and passes `cfg.AuditLedger.SigningKey` verbatim | **Confirmed**. Non-empty placeholder boots. |
| E6 | `internal/app/audit_config_test.go:22,:83` use the literal as an accepted fixture | **Confirmed** (2 occurrences). These exercise FM-8 (non-empty contract), not `Load()`. |
| E7 | `internal/config/config_test.go` literal locations `:32,:93,:215,:282,:315,:351,:417,:450`; shipped-file test at `:511` pins governance knobs only | **Confirmed** — 8 occurrences exactly as cited; `TestLoadShippedConfigFileAndEnvGate` asserts `source_prefix/actor_id/schema_id/data_classification/retention_class/anchor_scheme/schema_version` + enabled/bindings, **no signing-key assertion**. |
| E8 | `internal/config/concurrency_test.go:33` carries the literal; package sequential | **Confirmed**. `grep -rn t.Parallel internal/config/` returns nothing (only a comment "package stays sequential (no t.Parallel)"); concurrency_test header documents the same. Env legs are race-safe. |
| E9 | `Load()` (`config.go:384-402`): `SetEnvPrefix("AERO_ID")`, replacer `.`→`_`, `AutomaticEnv`, `ReadInConfig` → `Unmarshal` → `cfg.validate()` | **Confirmed**. Env merges into the struct *before* `validate`, so any validation (including a new deny check) sees merged env values — env cannot bypass `Load()`-time validation. |
| E10 | Root `signing_key` consumers: `internal/app/storage.go:20` → `internal/storage/local.go:114,120` presign/verify download URLs | **Confirmed**. Public key ⇒ forged presigned download URLs within validity windows. |
| E11 | No gate pins the literals; integration boots load the shipped file but only require non-empty | **Confirmed**. `grep -rn change-me Makefile scripts/` → no matches (exit 1). Other occurrences are `.claude/worktrees/*` copies and `docs/auto/runs/*/pipeline.yaml` (campaign artifacts) — out of scope. |

Net assessment: the requirements spec is grounded; the threat model (forgeable audit-chain HMAC + forged download URLs from committed public secrets) is real and worth closing. The design below is the concrete change plan.

---

## 2. Problem and threat model

Two committed secrets are public knowledge for anyone with repo access:

1. **`audit_ledger.signing_key`** — HMAC-SHA256 key over every audit fact's `record_hash` (E4). `validateAuditLedger` accepts anything ≥ 32 bytes, and the 36-byte literal passes. A deployment booting the shipped file without `AERO_ID_AUDIT_LEDGER_SIGNING_KEY` runs a forgeable tamper-evidence chain: an attacker who can write `audit_ledger` rows (defense-in-depth failure elsewhere) recomputes valid signatures for fabricated facts, and `verifyAuditRow` accepts them.
2. **Root `signing_key`** — signs local-storage presigned download URLs (E10). Anyone can mint valid signed URLs within the expiry window. `validateRequired` only checks non-empty.

Both violate AGENTS.md ("Secrets come from environment or an approved secret provider, never committed config"). The FM-8 contract (E5) needs only non-empty, so regenerated random values keep dev/CI boots green.

---

## 3. Design decisions

### D1 — Enforcement point: reject at `config.Load` only (no FM-8 change)

`Load()` is the single choke point every binary shares (`cmd/server`, `cmd/migrate`, `cmd/audit-replay`, integration boots — all call `Load` before opening the DB, per the activation-gate comment at `internal/app/audit.go:31-35`). Because `validate` runs after viper env merge (E9), one deny check covers file and env values alike. Deny-list semantics are compiled-in and not environment-overridable.

- **Accepted consequence:** in-code `config.Config` construction (unit tests, `buildAuditStack` callers) bypasses the deny-list. FM-8 stays non-empty-only. This is deliberate: the deny-list guards the *shipped/deployed config surface*, not hand-built structs; adding it to FM-8 would break `internal/app/audit_config_test.go` fixtures out of scope and create two enforcement points with drift risk.
- **Optional future hardening (explicitly out of scope, recorded not implemented):** move denylist into a tiny shared function both `Load()` and FM-8 call. Deferred because the direction requires zero FM-8 change and the scope boundary is loadable config.

### D2 — Deny semantics (normalized, exact + prefix)

```text
normalize(key) = strings.ToLower(strings.TrimSpace(key))
exact-deny (permanent, public forever):
  "change-me-please-very-long-audit-key"
  "change-me-please-very-long-random"
prefix-deny (conventional placeholder families):
  "change-me", "changeme", "replace-me"
```

Rationale:
- Lowercase + trim: env/file whitespace and case variants of a public value are still the public value.
- Exact set: covers the two shipped literals specifically.
- Prefix set: covers the conventional families at a cost of false positives only for keys that are, by convention, placeholders. A legitimate random hex key can never start with `change-me`/`changeme`/`replace-me`.
- Not covered (accepted residual): whitespace-embedded variants (`change me please`), base64/base32 re-encodings of the same bytes, and translated spellings. These are lower-risk than the literal values: the control's primary job is (a) never let the *shipped* values boot, and (b) fail loudly when an operator re-commits a placeholder family. Documented residual risk in §9.

### D3 — Placement and error contract

New package-level helper in `internal/config` (name `rejectPlaceholderSecret(key, field string) error`) returning errors with the shared pinned fragment **`signing_key is a known placeholder`**:

- root: `signing_key is a known placeholder — secrets must come from environment or an approved secret provider`
- audit: `audit_ledger.signing_key is a known placeholder — secrets must come from environment or an approved secret provider` (the pinned fragment appears as a substring)

Wiring:
- `validateRequired` (`validate.go` after the `== ""` check): `rejectPlaceholderSecret(c.SigningKey, "signing_key")`.
- `validateAuditLedger` (`validate.go`, order **required-empty → deny → length**): deny precedes the length check so any deny-listed value reports the deny error deterministically regardless of length; non-deny short keys keep the pinned 32-byte message.

Why `validateRequired` (not a new function): it already runs first in `Config.validate()` (line 12), the root key has no other validator, and the change is a 2-line addition.

**Placement contingency (file-size gate):** `validate.go` is 427 lines. The helper + constants (~40 lines incl. doc comment) project to ~467–475, under the 500-line gate and funlen 50. If actual insertion pushes ≥ 480 lines, move the deny-set vars + helper to a sibling `internal/config/secret_validation.go` (same package; one cohesive concern/file per AGENTS.md). Both options satisfy `make check-file-sizes`; helper must stay ≤ 50 lines and cyclomatic ≤ 10 (it is a small map-lookup + ordered prefix loop).

### D4 — Regenerate shipped dev values

`configs/config.yaml`:
- `:173` → a fresh 64-char lowercase hex string (recommend `openssl rand -hex 32`; 64 hex chars = 32 bytes ≥ the audit gate, and hex can never match a deny prefix).
- `:230` → a fresh 64-char lowercase hex string of the same form.

Keep both inline comments and the `AERO_ID_*` override guidance. **No other behavioral line changes** in the file; governance/billing/callback/export/event-ingest/outbox/database blocks keep current values. `TestLoadShippedConfigFileAndEnvGate` pins only governance knobs (E7), so it stays green unmodified; FM-8 needs only non-empty, so out-of-box boots remain green.

### D5 — Fixture migration to one fixed hex constant

Introduce **one fixed, committed 64-char lowercase hex literal** used by all Load-path fixtures (deterministic; a literal, never generated at runtime). Recommended value:

```text
a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90
```

Properties: 64 chars ≥ 32 bytes; hex-only so no `-`; does not start with `change-me`/`changeme`/`replace-me`.

Mandatory replacements (otherwise `make test` breaks the moment the deny-list lands):
- `internal/config/config_test.go`: `:32` (`sample` const), `:93` (`validLine` in `TestLoadAuditLedgerSigningKeyValidation`), `:215, :282, :315, :351, :417, :450` (inline audit lines of per-test base YAMLs) → replace the audit key literal with the hex constant (go string literal in YAML: `audit_ledger: {signing_key: a1b2...}`).
- `internal/config/concurrency_test.go:33` (`concurrencyBaseYAML`) → same.

Consistency replacements (these pass today — FM-8 is non-empty-only — but must not keep the deny-listed literal as an "accepted key" fixture in the same change set):
- `internal/app/audit_config_test.go:22, :83` → local const `testAuditLedgerSigningKey = "<hex>"` (per-package local const; the same literal, no cross-package test dependency). FM-8 semantics unchanged (any non-empty key accepted).

Post-change invariant: `grep -rn "change-me-please" internal/ configs/` returns **nothing**. `.claude/worktrees/*` copies and historical docs stay untouched (out of scope).

### D6 — Root-key length gate stays out of scope

The direction adds only the deny-list to the root key. We deliberately do **not** add a `>= 32 bytes` check for it in this change: it would alter existing accepted configs (any non-empty root key currently boots) and is a separate hardening decision. The regenerated shipped value is 64 hex chars as hygiene, but the validator contract for non-deny root keys remains "non-empty only." Existing pinned messages (`is required`, `must be at least 32 bytes`) are unchanged for non-deny values, preserving current negative rows of `TestLoadAuditLedgerSigningKeyValidation`.

---

## 4. API changes

| Surface | Change | Compatibility |
|---|---|---|
| `internal/config.Load(path)` behavior | After `Unmarshal`, validation now rejects deny-listed `signing_key` / `audit_ledger.signing_key` values with new error text | Signature and call semantics unchanged; only *new* rejection class. Existing valid configs unaffected. Existing root/audit validation messages preserved for non-deny values. |
| New unexported symbols (`internal/config` package) | `wellKnownPlaceholderSecrets map[string]struct{}`, `placeholderSecretPrefixes []string`, `rejectPlaceholderSecret(key, field string) error` | No public API change. If `validate.go` crosses file-size budget, these move verbatim to `secret_validation.go` (same package). |
| `configs/config.yaml` values | Two signing keys regenerated (D4) | Value-only change; all structural keys, comments, and other blocks unchanged. |
| Test fixtures | Deny-listed literal replaced by fixed hex const (D5) | Test-only changes; no production code signature changes. |
| Error contract (new) | Any deny match → `"<field> is a known placeholder — secrets must come from environment or an approved secret provider"`; shared pinned substring `signing_key is a known placeholder` on both fields; byte-exact — tests assert it, so rewording is a breaking test change. | New errors are additive; they only fire for values that previously booted unsafely. |

---

## 5. Compatibility constraints

1. **FM-8 contract unchanged** — `validateAuditLedgerConfig` (`internal/app/audit.go`) stays non-empty-only; `internal/app/audit_config_test.go` still exercises that contract (with the new hex fixture).
2. **Existing validation messages preserved** for non-deny values: `audit_ledger.signing_key is required`, `must be at least 32 bytes` (negative rows of `TestLoadAuditLedgerSigningKeyValidation` must still pass with original substrings).
3. **Deny before length** ordering so deny-listed ≥32-byte and <32-byte values both report the deny error deterministically.
4. **Env-merge precedence preserved** — viper env override continues to work for legitimate keys; the deny check runs post-merge so a placeholder env value over a valid file fails. No `SetDefault` interference (none exist for these keys).
5. **Sequential test package** — `internal/config` has no `t.Parallel`, so `t.Setenv` env legs are safe (mirrors `TestLoadAuditGovernanceKnobEnvLegs`).
6. **Integration-tagged boots** (`cmd/server/main_test.go:65`, `internal/app/composition_root_integration_test.go:81`) load the shipped file; after regeneration it Loads (non-empty satisfies FM-8) — no changes needed in those tests.
7. **File-size gates** — `validate.go` stays ≤ 500 lines (or helper siblings in `secret_validation.go`); helper ≤ 50 lines, cyclomatic ≤ 10.
8. **No Makefile/gate/script changes** — no existing gate references the literals (E11).
9. **No edits to historical docs or `.claude/worktrees/*`** — stale copies are explicitly out of scope.

---

## 6. Failure modes and mitigations

| Failure mode | Impact | Mitigation |
|---|---|---|
| Production deploys the deny-list while still running a config with the old literal | Hard boot failure at `Load()` (fail-closed — no forgeable chain boots) | **Migration ordering (critical): rotate secrets in production config first, then deploy the deny-list release.** Document this order in the release notes. A failed boot is the desired security behavior, not a bug. |
| Operator legitimately generates a key starting with `change-me`/`changeme`/`replace-me` | `Load()` rejects a non-placeholder key | By construction such a value *is* a placeholder-family name; the prefix set is intentionally conservative. Error text tells them the remedy (env/secret provider). Use hex/random keys instead. |
| Env supplies a placeholder over a valid file | Fails to boot | Intended (E9 execution order); covered by REQ-C2 env-leg tests. |
| Case/whitespace variant of a public literal (`CHANGE-ME-PLEASE...`, padded) | Boots unless it hits prefix/exact rules | Lowercase+trim normalization catches case and surrounding whitespace, which are the practical re-commit vectors. Embedded-whitespace and re-encoded forms remain residual risk (§9). |
| A teammate re-commits the old literal after regeneration | `make test`/`Load` fails locally with the pinned error | This is the permanent tripwire; the deny-set is compiled-in and not env-overridable. |
| `validate.go` grows past the size gate | `make check-file-sizes` fails | D3 contingency: sibling `secret_validation.go` (≤ 500-line guard confirmed at 427 pre-change). |
| Deny-list shipping while `.claude/worktrees/*` copies still contain the literal | None — those are offline worktree copies, not loaded configs | Out of scope (E11); a fresh worktree loses the committed secret anyway. |
| Audit key rotated in prod while old signed rows exist | Old rows fail chain verification? **No** — re-verifying an historical chain with a *rotated* key would fail | Not a new failure: rotation already has this property today (signature is key-bound, `signature_version` monotonic). The deny-list change does not alter repository behavior; operators rotating the key must either rewrite/verify with the old key or accept a chain-segment boundary under the existing signature-version rules. Documented here so release notes mention it. |

---

## 7. Migration steps (ordered)

1. **Generate secrets** (offline): `openssl rand -hex 32` twice → `NEW_AUDIT_KEY`, `NEW_ROOT_KEY` (both 64 lowercase hex chars).
2. **Rotate production config first** (pre-deploy): set `AERO_ID_AUDIT_LEDGER_SIGNING_KEY` and `AERO_ID_SIGNING_KEY` in production environment/secret provider. Verify a boot reads the env values and sign/verify paths still work (`make run` smoke + audit append + one presigned download).
3. **Edit `configs/config.yaml`** (`:173`, `:230`) to the two new hex values, keeping comments.
4. **Add deny helper + wiring** in `internal/config` per D2/D3 (validate.go or `secret_validation.go` if size contingency triggers).
5. **Migrate fixtures** per D5: 8 occurrences in `config_test.go`, 1 in `concurrency_test.go`, 2 in `internal/app/audit_config_test.go` → fixed hex const.
6. **Add regression tests** per §8 (acceptance 1 & 2).
7. **Verify invariant**: `grep -rn "change-me-please" internal/ configs/` → empty.
8. **Run the full gate**: `make test && make lint && make lint-strict && make check-file-sizes && make build` (+ `make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-load-scenarios && make check-migration-set && make check-composition-root && make check-proto-sync && make check-integration-compile` for the complete done gate; migrations are untouched but the gate is cheap). Optionally `go test -race ./internal/config/ -count=1` (package is sequential, so race mode is a formality).
9. **Deploy deny-list release** after step 2 lands in production (fail-closed ordering).

---

## 8. Testable acceptance mapping

| Acceptance (from direction) | Executable test / artifact | Requirement |
|---|---|---|
| 1. Table-driven rejections of the two shipped literals + `change-me`/`changeme`/`replace-me` prefixes with pinned substring; positive leg loads | New `func TestLoadRejectsPlaceholderSecrets(t *testing.T)` in `internal/config`, mirroring `TestLoadAuditLedgerSigningKeyValidation` (base YAML + per-row `strconv.Quote` + `strings.Replace`): rows (each asserted against **both** the audit line and the root line — shared helper): `change-me-please-very-long-audit-key`, `change-me-please-very-long-random`, `change-me-` + `strings.Repeat("x", 45)`, `changeme-` + `strings.Repeat("x", 45)`, `replace-me-` + `strings.Repeat("x", 45)`. Assert `err != nil && strings.Contains(err.Error(), "signing_key is a known placeholder")` (the ≥32-byte prefix rows guarantee deny-error, not length-error, firing). Positive leg: audit = fixed hex const, root = `testkey` → `Load` succeeds and `cfg.AuditLedger.SigningKey == <hex>`. | REQ-C1, REQ-C4, REQ-C5 |
| 2. Env cannot bypass deny-list | New `func TestLoadRejectsPlaceholderSecretEnvLeg(t *testing.T)` mirroring `TestLoadAuditGovernanceKnobEnvLegs` (`t.Setenv` before `Load`, `strings.Contains` on error): `AERO_ID_AUDIT_LEDGER_SIGNING_KEY=change-me-please-very-long-audit-key` over a valid file fails; symmetric `AERO_ID_SIGNING_KEY=change-me-please-very-long-random` leg fails identically. Package is sequential (no `t.Parallel`) so env legs are safe. | REQ-C2 |
| 3. Shipped values regenerated; shipped-file test green; out-of-box boots | Diff: `configs/config.yaml:173` and `:230` → two fresh 64-hex values, no other behavioral line changes. Existing `TestLoadShippedConfigFileAndEnvGate` passes **unmodified** (pins governance knobs only, E7). FM-8 (`internal/app/audit.go`) unchanged and satisfied by non-empty values; `cmd/server` compiles; integration-tagged boots load the regenerated file. | REQ-C3 |
| 4. Engineering gates green; file-size contingency | `make test && make lint && make lint-strict && make check-file-sizes && make build`; `wc -l internal/config/validate.go` ≤ 500 (or helper in `secret_validation.go`); helper ≤ 50 lines (funlen), cyclomatic ≤ 10 (cyclop); existing negative rows of `TestLoadAuditLedgerSigningKeyValidation` still pass with original substrings. | REQ-C6 |
| 5. No deny-listed literal remains in code/config | `grep -rn "change-me-please" internal/ configs/` → no matches (docs + `.claude/worktrees/*` excluded by scope). | REQ-C5 invariant |

Implementation-time verification commands (documented, not to be run now):

```bash
go test ./internal/config/ ./internal/app/ -count=1 \
  -run 'TestLoadRejectsPlaceholder|TestLoadAuditLedgerSigningKeyValidation|TestLoadShippedConfigFileAndEnvGate|TestCompositionRootValidatesAuditSigningKey'
go test -race ./internal/config/ -count=1
grep -rn "change-me-please" internal/ configs/   # must be empty
make test && make lint && make lint-strict && make check-file-sizes && make build
```

---

## 9. Scope boundaries and residual risk

**Non-goals (explicit):**
- No deny-list extension to `event_ingest.signing_key`, `outbox.webhook.signing_key`, `callback.master_key`, `export.encryption_key`, `database.password`, or `sso` credentials — the direction names exactly two keys and the shipped values elsewhere are empty/secret-provider-gated/dev-only.
- No change to FM-8 (`validateAuditLedgerConfig`).
- No new `>= 32 bytes` gate for the root key (D6).
- No edits to historical docs or `.claude/worktrees/*`.
- No Makefile/gate changes.
- No change to the two existing pinned error messages or their test rows.

**Residual risk (accepted, documented):**
1. Committed dev keys remain public by nature of the repo; the control is the permanent compiled-in deny-list + existing env/secret-provider override guidance. Removing committed secrets entirely is a config-distribution concern outside this module.
2. Normalized-deny misses embedded-whitespace and re-encoded (base64/hex-of-bytes) variants of the same public values; the practical re-commit vectors (exact literals, prefix families, case/trim variants) are covered.
3. The deny-list applies to `Load()`-loaded config only — in-code constructed configs (tests, embedding code) are not denied at FM-8. Accepted per D1.

**Traceability:** definitions of done map 1:1 to §8 rows 1–5; all map to REQ-C1…REQ-C6 of the requirements spec.