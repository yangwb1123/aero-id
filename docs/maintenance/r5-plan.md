Plan written to `docs/plans/2026-08-05-audit-chain-integrity-hardening.md`.

## Summary

**Assessment (run first):** standard → demo (S), risk low, workflow **L0_direct**, product level **L0_local_feature**, 0 prescription rules — restraint applies: fix only the two defects, no new APIs or structure.

**Both claims verified against live code — TRUE, with one critical correction:**

1. **F5 fail-open** — `configs/config.yaml:173` ships `audit_ledger.signing_key: ""`; `audit_ledger.go:342` (`VerifyChain`) and `:185-187` (append) skip HMAC when the key is empty; `validate.go` has zero `audit_ledger` validation. Precedents confirmed (`validateRequired` top-level key, `validateEventIngest` ≥32 bytes, `config.yaml:225` placeholder).
2. **float64 round-trip** — `VerifyChain` decodes stored payloads into `map[string]any` (float64) and re-marshals; `9007199254740993` becomes `9007199254740992` → digest mismatch → false rejection of unmodified chains. **But** "hash raw stored bytes" is unsafe: I empirically verified against the local PostgreSQL 17 that jsonb canonicalizes keys length-first (`{"b":1,"aa":2}` vs Go's `{"aa":2,"b":1}`) and expands literals (`1e21`→`1000000000000000000000`, `1e-7`→`0.0000001`). So the fix uses the task's sanctioned alternative — a **canonical form**: `json.Decoder.UseNumber` + number-type normalization (int64 → uint64 → Go 'g' float), which provably reproduces the append-side Go serialization byte-for-byte regardless of jsonb's literal form.

**Key design points:**
- Fix 1: `validateAuditLedger` (required, ≥32 bytes, fail-startup) + placeholder in `config.yaml` (top-level precedent) + fail-closed repo guards (empty-key Append/VerifyChain error before any DB touch) + HMAC checked only for `signature_version >= 1` rows so enabling a key doesn't false-alarm on legacy unsigned chains (hash-chain linkage still binds all rows).
- Fix 2: new `internal/repository/audit_payload.go` canonicalizer (~45 lines); append unchanged; `audit_ledger.go` stays ~482 lines (under the 500-line gate).
- **Zero migrations, zero API/proto changes**, legacy `audit_events` untouched; one-line key supply in `internal/app/audit_integration_test.go:18` (task-sanctioned: "test configs supply a key").
- Tests: config rejection table (deterministic), no-DB guard tests, canonicalizer table fed jsonb-canonical bytes, and a new integration test (round-trip with adversarial payloads against real jsonb, tamper detection, legacy-row semantics) — full done gate + `make test-integration`.
- Deliberately excluded: the sibling unlanded constructor-fail-closed campaign (separate direction), key rotation versioning (pre-existing, noted as R3).
