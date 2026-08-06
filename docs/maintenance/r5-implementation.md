Everything verified. The full done gate passes. Final summary of what was verified and the state:

**Implementation state:** The scoped changes were already present in the working tree from an earlier timed-out attempt of this task. I verified every element line-by-line against the plan (all claims re-checked against actual code), and ran the complete gate. No further edits were needed — zero files touched this session.

**Verification against plan:**

- **Fix 1 (F5 fail-open):** `validateAuditLedger` in `internal/config/validate.go` (required after `TrimSpace`, ≥32 bytes) wired into `validateRuntimeLimits`; `configs/config.yaml` placeholder `change-me-please-very-long-audit-key` (35 B); `deployments/k8s/regional-cell.yaml` placeholder ≥32 B; empty-key guards in `Append`/`AppendInTx`/`VerifyChain` (`internal/repository/audit_ledger.go:105-126, 331-334`); HMAC check conditioned on `signature_version >= 1` (legacy unsigned rows still bound by the hash chain).
- **Fix 2 (float64 round-trip):** new `internal/repository/audit_payload.go` with `canonicalAuditPayload` + `normalizeAuditNumbers` (`UseNumber`, int64 → uint64 → float64 normalization); `VerifyChain` now hashes the canonical re-serialization of the raw stored bytes, not the float64-decoded map. Append path unchanged.
- **Tests:** config rejection table; no-DB fail-closed guard tests (zero-value `*gorm.DB` proves no DB touch); canonicalizer table fed jsonb-canonical bytes (2⁵³+1 ints, 1e21/1e20/1e19/1e-7 expansions, length-first key order, nested, uint64 max, 1e100); integration tests against real PostgreSQL 17 (adversarial round-trip, payload/chain/signature/rehash tamper detection, legacy unsigned rows, fail-closed); test key supplied in `internal/app/audit_integration_test.go`.
- **Necessary extra (not in plan's file list):** `internal/service/audit_ledger_test.go` spy gained `ChainPartitions`/`ChainHead` — the interface at HEAD already declared them while the committed spy lacked them (compile break from the plan-stage commit); test-only fix required for the build.
- **Not touched:** the many unrelated modified files in this shared campaign working tree (region_epoch, legacy_cutover, event_store, account_*, etc.) belong to other tasks; left as-is. No migrations, no proto, no API/handler/service changes.

**Gate results:** `make test` exit 0 · `go vet ./...` exit 0 · `make lint` exit 0 · golangci-lint (strict) exit 0 · `make check-file-sizes` exit 0 (audit_ledger.go 483/500 lines) · `make build` exit 0 · `make test-integration` exit 0 (37 packages, incl. all 6 new ledger integration tests + app binding test).

```yaml
completion_report:
  summary: >-
    Audit chain integrity hardening implemented per plan 2026-08-05. Fix 1 (F5
    fail-open): configs/config.yaml now ships a >=32-byte audit_ledger.signing_key
    placeholder; internal/config/validate.go adds validateAuditLedger (required,
    >=32 bytes, TrimSpace) wired into validateRuntimeLimits; internal/repository/
    audit_ledger.go adds empty-key guards to Append/AppendInTx/VerifyChain and
    checks HMAC only for signature_version >= 1 rows so legacy unsigned chains
    still verify via the hash chain. Fix 2 (float64 round-trip): new
    internal/repository/audit_payload.go canonicalAuditPayload + normalizeAuditNumbers
    (json.Decoder.UseNumber, int64->uint64->float64 normalization) makes VerifyChain
    byte-faithful against jsonb-canonical stored text (>2^53 integers, exponent
    expansions, length-first key order); append path unchanged. Tests: config
    rejection table, no-DB fail-closed guard tests, canonicalizer table, new
    integration tests (adversarial round-trip, payload/chain/signature/rehash
    tamper, legacy rows, fail-closed), app integration test key supplied, k8s
    placeholder lengthened. No migrations, no proto, no API changes, no commits.
    Implementation was present from an earlier timed-out attempt; verified
    line-by-line against the plan and the full done gate was run.
  changed_files:
    - configs/config.yaml
    - deployments/k8s/regional-cell.yaml
    - internal/app/audit_integration_test.go
    - internal/config/config_test.go
    - internal/config/validate.go
    - internal/repository/audit_ledger.go
    - internal/repository/audit_ledger_integration_test.go
    - internal/repository/audit_ledger_test.go
    - internal/repository/audit_payload.go
    - internal/repository/audit_payload_test.go
    - internal/service/audit_ledger_test.go
  commands_executed:
    - command: "go build ./..."
      result: passed
    - command: "go vet ./..."
      result: passed
    - command: "make test"
      result: passed
    - command: "make lint"
      result: passed
    - command: "golangci-lint run ./... (make lint-strict)"
      result: passed
    - command: "make check-file-sizes"
      result: passed
    - command: "make build"
      result: passed
    - command: "go test -tags=integration -count=1 -run TestAuditLedger ./internal/repository/"
      result: passed
    - command: "make test-integration"
      result: passed
  not_executed: []
  residual_risks:
    - >-
      Legacy rows written before a signing key (signature_version = 0) are
      verified by hash chain only, HMAC skipped; intentional rollout
      compatibility, documented in plan as R1.
    - >-
      Payload values float64(-0) (Go writes -0, jsonb normalizes to 0) and
      uint64 > 2^64 cannot round-trip byte-faithfully; pre-existing, out of
      scope (plan R2).
    - >-
      No key rotation/versioning; rotating the key invalidates existing
      signatures (pre-existing limitation, plan R3).
    - >-
      Working tree contains unrelated in-flight changes from other campaign
      tasks (region_epoch, legacy_cutover, event_store, account_*, etc.); left
      untouched per scope, but they are uncommitted and mixed into the same
      tree.
  assumptions:
    - >-
      The scoped implementation found in the working tree (left by an earlier
      timed-out attempt of this same task) was reviewed hunk-by-hunk against
      the approved plan and accepted; this session made no further edits.
    - >-
      golangci-lint is not on PATH but is installed at GOPATH/bin; make
      lint-strict's own command -v check would fail in a bare shell, so it was
      run directly with the same binary and config the Makefile target uses.
    - >-
      The ChainPartitions/ChainHead additions to the auditLedgerRepoSpy in
      internal/service/audit_ledger_test.go are a test-only compile fix: the
      interface at HEAD already declared those methods while the committed spy
      lacked them.
    - >-
      make test-integration was run with Docker available (postgres images
      pre-pulled); it passed in full, including the new ledger integration
      tests against real PostgreSQL.
</｜｜DSML｜｜parameter>
</｜｜DSML｜｜invoke>
</｜｜DSML｜｜tool_calls>
