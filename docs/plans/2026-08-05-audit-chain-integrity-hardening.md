# Implementation Plan — Audit chain integrity hardening

Status: planning · Module: `internal/config`, `internal/repository` (audit ledger) · No migrations

## 0. Requirement assessment (run first, per task instructions)

`python /home/u1/ai-batch-runner/pi-batch.py assess "audit chain integrity hardening, production Go backend"`

- Profile: analysis | match **standard → prescription tier demo (scale S)** | risk low
- Workflow: **L0_direct** — single-task direct modification + fast verification gate; no pipeline
- Product level: **L0_local_feature** — restraint rule applies: implement only the two defects; no
  productization structure, no new config knobs beyond the mandated validation, no new APIs
- Prescription rules: 0 (minimal rule set). The assessor's "suggested supplements" (goal/user_role/
  acceptance) are satisfied by the task statement itself, which carries explicit acceptance framing
  ("fails startup validation instead of silently disabling", "verification is byte-faithful").

## 1. Verified evidence (both task claims re-checked against live code)

| # | Claim | Verification result |
|---|---|---|
| E1 | `configs/config.yaml` ships `audit_ledger.signing_key: ""` | **TRUE** — `configs/config.yaml:173` |
| E2 | `VerifyChain` skips signature verification when key empty | **TRUE** — `internal/repository/audit_ledger.go:342`: `if len(r.signingKey) > 0 && !hmac.Equal(...)` |
| E3 | Append path also silently skips signing when key empty | **TRUE** — `audit_ledger.go:185-187`: `if len(r.signingKey) > 0 { SignatureVersion = 1; Signature = sign(...) }`; default-config chains are written **unsigned** with `signature_version = 0` |
| E4 | No config validation exists for `audit_ledger` | **TRUE** — `internal/config/validate.go` has zero `AuditLedger` references; `validateRuntimeLimits` checks storage/rate-limit/GC/export/webhook/event_ingest/event_bus/replication/commercial only |
| E5 | Strict-signing precedent exists | **TRUE** — top-level `signing_key` required (`validate.go:46-48`, `validateRequired`); `event_ingest.signing_key` ≥ 32 bytes when set (`validate.go:184-186`); `configs/config.yaml:225` ships top-level placeholder `change-me-please-very-long-random` |
| E6 | Float64 round-trip in chain verification | **TRUE** — append hashes `json.Marshal(fact.Payload)` (`audit_ledger.go:110-112`); `VerifyChain` re-derives payload bytes via `auditLedgerRowToFact` → `json.Unmarshal(row.Payload, &map[string]any)` (**float64 decode**) → `json.Marshal` (`audit_ledger.go:333-337`) |
| E7 | Bug class confirmed: integers > 2⁵³ | **TRUE by construction** — `json.Unmarshal` into `any` yields float64; `9007199254740993` (2⁵³+1) re-marshals as `9007199254740992` → `digest` mismatch → `VerifyChain` rejects an **unmodified** chain |
| E8 | Payload column is jsonb | **TRUE** — `migrations/000034_create_audit_ledger.up.sql:19` (`payload jsonb NOT NULL`) |
| E9 | **Critical nuance (empirically verified against local PostgreSQL 17, 2026-08-05)** | jsonb canonicalizes on write, so "hash the raw read-back bytes" alone is **unsafe**: `'{"aa":2,"b":1}'::jsonb::text` → `{"b": 1, "aa": 2}` (jsonb sorts keys **length-first**; Go `json.Marshal` sorts **byte-order** → `{"aa":2,"b":1}`); `'1e21'::jsonb::text` → `1000000000000000000000` (exponent expanded); `'1e-7'` → `0.0000001`; `'9007199254740993'` is preserved exactly; `'1.0'` → `1.0` |
| E10 | Domain `AuditFact.Payload` is `map[string]any` | **TRUE** — `internal/domain/audit_ledger.go:25`; append-side bytes are always Go map serialization (byte-order keys; int64/uint64/float64/string/bool/nil values) |
| E11 | Legacy `audit_events` surface is separate | **TRUE** — `internal/repository/audit.go` (legacy) is untouched by the ledger; no shared code path |

**Consequence of E9:** the task's "hash the raw stored payload bytes" option is rejected for the jsonb
column — it would create *new* false failures for any payload whose Go key order differs from jsonb's
length-first order (e.g. `{"b":1,"aa":2}`) and for exponent-form floats. The sanctioned alternative —
"or a canonical form" — is the correct mechanism (see §3.2).

## 2. Module boundaries & data ownership (AGENTS.md authority contract)

- **aero-id owns** the audit ledger facts, `payload_hash`, `record_hash`, `signature` (`audit_ledger`
  table, migration 000034 + 000040 harden). This plan only changes how the ledger *signs and verifies
  its own rows*; no new data is owned, no source authority is touched.
- **Snaplink / Aero IM / Aero Vault** — untouched. No connector, no source database access.
- **Config** (`internal/config`) owns startup validation of the signing key; **repository**
  (`internal/repository/audit_ledger.go`) owns hashing/signing/verification mechanics. The service and
  handler layers keep their current contracts (`AuditLedgerService.Record/List/VerifyChain`).
- **Not in scope (deliberately):** the sibling fail-closed campaign
  (`docs/requirements/2026-08-06-internal-repository-audit-wiring-fail-closed.md` — constructor
  wiring/`FullAuditLedgerRepository`) is a separate direction, currently unlanded, and is **not**
  folded into this plan (restraint rule).

## 3. Design

### 3.1 Fix 1 — Fail-closed signing (F5)

Three layers, smallest possible radius:

1. **Startup validation** (`internal/config/validate.go`): add `validateAuditLedger(c.AuditLedger)` to
   the `validateRuntimeLimits` checks slice (same list as `validateEventIngest`):

   ```go
   func validateAuditLedger(value AuditLedgerConfig) error {
       key := strings.TrimSpace(value.SigningKey)
       if key == "" {
           return fmt.Errorf("audit_ledger.signing_key is required")
       }
       if len(key) < 32 {
           return fmt.Errorf("audit_ledger.signing_key must be at least 32 bytes")
       }
       return nil
   }
   ```

   Unlike `event_ingest` (where empty = documented "disables ingestion"), the audit ledger has **no
   legitimate empty state**: empty silently disables tamper evidence. Hence empty → hard startup error.
   Length rule matches the `event_ingest` precedent (≥ 32 bytes).

2. **Explicit placeholder default** (`configs/config.yaml:173`): replace `""` with
   `change-me-please-very-long-audit-key` (≥ 32 bytes), with the comment updated to "placeholder for
   local dev only; override via `AERO_ID_AUDIT_LEDGER_SIGNING_KEY` / secret provider in production".
   This follows the committed top-level placeholder precedent (`config.yaml:225`). The default config
   now boots, but *signs* — the failure mode moves from "silently unsigned" to "signed with a known
   dev key", which is the accepted dev posture of this repository (AGENTS.md: secrets from env/secret
   provider in production; `make run` is a dev path).

3. **Fail-closed repository guards** (defense in depth; the config gate is the primary fix, but a
   repository constructed directly — tests, future wiring — must not silently disable evidence):
   - `Append` / `AppendInTx`: return `errors.New("audit ledger signing key is not configured")` before
     touching the DB when `len(r.signingKey) == 0`.
   - `VerifyChain`: same guard at function entry — an empty key now **errors** instead of skipping the
     signature check (`audit_ledger.go:342` condition becomes unreachable for empty keys).
   - `createAuditRows` keeps signing unconditionally reachable (the `len(r.signingKey) > 0` conditional
     becomes dead but is left in place for minimal diff).
   - **Rollout compatibility:** `VerifyChain` checks the HMAC only for rows with
     `signature_version >= 1`. Rows written under the broken default (`signature_version = 0`,
     empty `signature`) still pass **hash-chain** verification (record hash + previous-hash linkage
     always apply); they simply carry no HMAC evidence. Without this, enabling a key on a deployment
     that already wrote unsigned rows would make every existing chain "fail verification" — an
     operational break on top of a security fix. `signature_version` is not inside `auditRecordHash`,
     so this skip cannot be used to launder modified rows: the hash chain still detects any field
     tampering (see §6 risk R4).

### 3.2 Fix 2 — Byte-faithful payload verification (float64 round-trip)

**Rejected:** hashing raw read-back bytes (see E9). **Chosen: canonical re-serialization** that
reproduces, byte-for-byte, the serialization the append path hashed — Go `json.Marshal` of the
original `map[string]any` — from jsonb's value-preserving text, independent of jsonb's literal
canonicalization.

New helper in a new sibling file `internal/repository/audit_payload.go` (keeps `audit_ledger.go`
well under the 500-line gate; it is at 472 today):

```go
// canonicalAuditPayload re-serializes stored payload bytes (jsonb text) into the
// canonical Go form that Append hashed (json.Marshal of map[string]any):
// byte-order-sorted keys, exact integer digits, Go 'g'-format floats.
// jsonb canonicalizes keys (length-first) and number literals (1e21 -> 1000...,
// 1e-7 -> 0.0000001), so a naive re-marshal of a float64 decode is not byte-faithful.
func canonicalAuditPayload(raw []byte) ([]byte, error) {
    dec := json.NewDecoder(bytes.NewReader(raw))
    dec.UseNumber()
    var value any
    if err := dec.Decode(&value); err != nil {
        return nil, err
    }
    return json.Marshal(normalizeNumbers(value))
}

// normalizeNumbers walks decoded JSON and rewrites json.Number literals to the
// Go numeric type that reproduces Append-side bytes: int64 when the literal is an
// exact integer, else uint64, else the shortest-round-trip float64 form.
func normalizeNumbers(v any) any { ... }
```

Number normalization (per `json.Number` literal, decoded with `UseNumber`):
1. `Int64()` succeeds → keep as int64 (exact digits; jsonb preserves > 2⁵³ integers exactly — E9).
2. else `Uint64()` succeeds → uint64 (covers `10000000000000000000`-class literals from float64(1e19),
   whose Go append-side bytes are the plain digits).
3. else `ParseFloat` → float64; `json.Marshal(float64)` emits Go's shortest 'g' form — byte-identical
   to the append-side bytes for every float64-originated value regardless of jsonb's literal form
   (`1e21` → jsonb `1000000000000000000000` → float64 → Go `1e+21` ✓; `1e-7` → jsonb `0.0000001` →
   float64 → Go `1e-07` ✓; `1.0` → jsonb `1.0` → float64(1) → Go `1` ✓).

`VerifyChain` payload check becomes:

```go
payload, err := canonicalAuditPayload(row.Payload)
if err != nil || fact.PayloadHash != digest(payload) {
    return fmt.Errorf("audit payload verification failed at %s", row.ID)
}
```

**Append is unchanged** — `json.Marshal(fact.Payload)` is already the canonical form. Proof of
byte-faithfulness: append-side bytes = Go marshal of map with int64/uint64/float64/string/bool/nil
values; the verifier reproduces exactly those bytes from the (value-preserving) jsonb text via the
normalizer above; key order is re-derived from a map on both sides (Go byte-order sort).

## 4. API / contract changes and compatibility constraints

| Surface | Change |
|---|---|
| HTTP `/v1/audit/ledger/verify`, gRPC verify, `AuditLedgerService`, `AuditLedgerRepository` interfaces | **None** — signatures and wire contracts unchanged |
| `VerifyChain` semantics | Empty key: error instead of silent pass (was: success without any check). Signed rows (`signature_version >= 1`): unchanged. Legacy unsigned rows (`signature_version = 0`): hash-chain check only, HMAC skipped (new, required for rollout) |
| `Append`/`AppendInTx` semantics | Empty key: error instead of writing unsigned rows |
| Config | `audit_ledger.signing_key` becomes **required, ≥ 32 bytes** at startup. Shipped `config.yaml` placeholder keeps `make run` working |
| Legacy `audit_events` surface | Untouched (E11) |
| Migration / protobuf | **None** — no schema change, no `.proto` change, no `make proto` |

## 5. Error model and failure modes

- **Startup:** `config.Load` returns `audit_ledger.signing_key is required` / `must be at least 32
  bytes` — plain error, same style as all existing config validation; process refuses to start
  (fail closed). Distinct from `event_ingest` (where empty is a documented disable switch).
- **Runtime (unreachable via config, reachable via direct repository construction):**
  - Append with empty key → `audit ledger signing key is not configured` (before any DB touch; no row
    written, nothing partial).
  - VerifyChain with empty key → same error; surfaces through the existing handler mapping as
    `audit.chain_invalid` conflict (unchanged mapping, now with a truthful cause).
  - VerifyChain on tampered payload/chain → unchanged `audit payload|chain|signature verification
    failed at <id>` errors.
- **Failure modes documented for operators:**
  - Deploy without key → startup fails with a self-explanatory message (was: silently unsigned).
  - Enable a key after unsigned rows exist → hash chain still verifies; HMAC only applies to rows
    written after the key (no false "tampered" alarms on pre-existing data).
  - Key rotation → pre-existing limitation (no key versioning; `signature_version` is not used for key
    selection). Out of scope; noted in risks.

## 6. Test plan (mapped to acceptance checks)

### AC-F5 — Empty/weak audit signing key fails startup; no silent verify-skip; tests deterministic

1. `internal/config/config_test.go` — extend the `sample` fixture with
   `audit_ledger: {signing_key: change-me-please-very-long-audit-key}` (deterministic; every `Load`
   test keeps passing). Add a rejection table calling `Load` on variants:
   - empty key → error contains `audit_ledger.signing_key is required`;
   - 31-byte key → error contains `must be at least 32 bytes`;
   - whitespace-padded key → rejected after `TrimSpace`;
   - placeholder (32+ bytes) → accepted.
   `TestLoad_missingSigningKey` keeps its meaning: `validateRequired` (top-level key) runs before
   `validateRuntimeLimits`, so its error still fires for the top-level key.
2. `internal/repository/audit_ledger_test.go` (pure, **no DB** — guards run before any DB access):
   - `NewAuditLedgerRepository(&gorm.DB{}, AuditLedgerConfig{})` → `Append(ctx, validFact)` errors
     with `signing key is not configured`; `VerifyChain(ctx, "", "")` errors with the same; nothing
     touches the DB.
   - `NewAuditLedgerRepository(&gorm.DB{}, AuditLedgerConfig{SigningKey: "test-key"})` → `Append` of
     a valid fact fails only on the DB (proving the guard is key-conditional), i.e. the error is not
     the key error.

### AC-FLOAT — Verification is byte-faithful; tamper still detected

3. `internal/repository/audit_payload_test.go` (pure): table test feeding **jsonb-canonical** bytes
   (as produced by PostgreSQL 17, per E9) and asserting `canonicalAuditPayload` equals the Go
   append-side bytes:
   - `{"n":9007199254740993}` (big int, int64 path) → unchanged;
   - `{"n":1000000000000000000000}` (jsonb form of 1e21, float64 path) → `{"n":1e+21}`;
   - `{"n":0.0000001}` (jsonb form of 1e-7) → `{"n":1e-07}`;
   - `{"b": 1, "aa": 2}` (jsonb length-first key order) → `{"aa":2,"b":1}` (Go byte order);
   - nested object/array, strings, bools, null, negative ints, `1.0` → `1`.
4. **Integration** — new `internal/repository/audit_ledger_integration_test.go`
   (`//go:build integration`, `testpg.Start` + `testpg.Migrate`, real ledger with a test key):
   - **Round-trip:** append facts whose payloads contain the adversarial values from E9 (big ints
     > 2⁵³, `1e21`, `1e-7`, `1e20`, non-sorted keys `{"b":1,"aa":2}`, nested) → `VerifyChain` passes
     (fails today for the big-int rows — red-before/green-after proof against real jsonb).
   - **Tamper detection:** UPDATE a stored payload (flip a value) → `VerifyChain` fails with
     `payload verification failed`; UPDATE `previous_hash` → fails with `chain verification failed`;
     UPDATE a signed row's payload **and** recompute its `payload_hash` only → fails on
     `record_hash`/signature.
   - **Signature semantics:** new rows carry `signature_version = 1` + valid HMAC; a row mutated to a
     wrong signature fails; a legacy-style row (`signature_version = 0`, empty signature, correct
     hashes, inserted via SQL) passes `VerifyChain` (hash chain intact, HMAC skipped).
   - **Fail-closed:** a repo constructed with an empty key fails append and fails VerifyChain (no DB
     dependence asserted by the error type).
5. `internal/app/audit_integration_test.go:18` — `AuditLedgerConfig{}` → supply the test key
   (mandated by the task: "test configs supply a key"; without it the fail-closed append guard makes
   this test red).

### AC-COMPAT — No legacy surface breakage; done gate

6. `make test && make lint && make lint-strict && make check-file-sizes && make build` (repository
   SQL/storage/connector code is not touched, but the audit ledger change still triggers the
   integration gate per AGENTS.md) and `make test-integration` (new ledger integration test).
7. Legacy `audit_events` tests (`internal/repository/audit_test.go` etc.) untouched and green;
   `git grep` shows no edits outside the file list in §8.

## 7. Change radius

| File | Change |
|---|---|
| `configs/config.yaml` | 1 line: `audit_ledger.signing_key` `""` → placeholder |
| `internal/config/validate.go` | +~12 lines: `validateAuditLedger` + wire into `validateRuntimeLimits` |
| `internal/config/config_test.go` | +~35 lines: sample fixture key + rejection table |
| `internal/repository/audit_ledger.go` | +~10 lines: two empty-key guards, `signature_version`-conditioned HMAC check, canonical payload call (472 → ~482, under the 500-line gate) |
| `internal/repository/audit_payload.go` | **NEW** ~45 lines: `canonicalAuditPayload` + `normalizeNumbers` |
| `internal/repository/audit_ledger_test.go` | +~40 lines: fail-closed guard tests (no DB) |
| `internal/repository/audit_payload_test.go` | **NEW** ~50 lines: canonicalizer table |
| `internal/repository/audit_ledger_integration_test.go` | **NEW** ~120 lines: round-trip / tamper / legacy-row / fail-closed |
| `internal/app/audit_integration_test.go` | 1 line: test key |
| `deployments/k8s/regional-cell.yaml` | 1 line (optional): placeholder `CHANGE_ME_AUDIT_LEDGER_KEY` (25 B < 32) → ≥ 32-byte placeholder so template validation matches the new rule |

**No migrations, no `.proto`, no `cmd/*` wiring changes, no service/handler edits.** Existing rows
are read-only; nothing is rewritten (rewriting `payload_hash`/`record_hash`/`signature` would itself
be tamper-evidence laundering and is explicitly rejected).

## 8. Risks and known limitations

- **R1 (legacy unsigned rows):** HMAC is skipped for `signature_version = 0` rows. Hash-chain
  verification still applies to them; an attacker with write access to the ledger table could strip
  signatures (set version 0) but cannot pass hash-chain verification for modified rows, since every
  hashed field is bound into `record_hash` (v1/v2) and chained via `previous_hash`. Residual gap
  versus a fully signed chain, accepted for rollout compatibility; closed for all rows written after
  this change.
- **R2 (residual literal edges):** payload values of `float64(-0)` (Go writes `-0`, jsonb reads `0`)
  and uint64 values > 2⁶⁴ cannot round-trip byte-faithfully; both are pre-existing, unreachable from
  normal handler payloads, and outside this defect's scope. Noted, not fixed.
- **R3 (key rotation):** rotating the key invalidates existing signatures; no key-versioning exists
  (`signature_version` is not a key selector). Pre-existing; a rotation procedure is a separate task.
- **R4 (jsonb divergence):** if a future PostgreSQL changes jsonb number/key canonicalization, the
  canonicalizer (which is jsonb-form-agnostic by design — it always re-derives Go-side bytes from
  values) remains correct; the integration test pins the current behavior.
- **R5 (file-size gate):** `audit_ledger.go` stays ~482 lines; the canonicalizer lives in a sibling
  file to keep headroom.

## 9. Done gate

```bash
make test && make lint && make lint-strict && make check-file-sizes && make build
make test-integration   # new audit ledger integration tests (jsonb round-trip, tamper, legacy rows)
```

Acceptance summary: (AC-F5) empty/weak audit key fails startup, default config signs, VerifyChain
never silently skips; (AC-FLOAT) unmodified chains with > 2⁵³ integers / exponent literals /
non-sorted keys verify, tampered chains still fail; (AC-COMPAT) legacy `audit_events` and all current
APIs unchanged, zero migrations.
