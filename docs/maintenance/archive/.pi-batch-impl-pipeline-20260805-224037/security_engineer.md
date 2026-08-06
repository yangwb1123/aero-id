Security review complete. Full report written to `docs/maintenance/r5-reviews/security_engineer.md` (scratch probes removed; no source files touched).

## Security Engineer Review — r5 Audit Chain Integrity Hardening

**Method:** re-verified both fixes line-by-line against the working tree, and executed two adversarial probes against real PostgreSQL via the integration harness (scratch tests run, then deleted).

### 1. Assets / boundaries / attackers

- **Assets:** `audit_ledger` rows (payload + hash chain + HMAC), the signing key (env `AERO_ID_AUDIT_LEDGER_SIGNING_KEY`, config-validated ≥32 B), WORM anchor objects, audit API responses.
- **Boundaries:** SSO token → context identity (facts bound from context, never client headers); tenant scoping via `mustTenant` + `tenant:` partition prefix; permission gate `PermAuditRead` (default `moderator`/`admin` roles, **global, not tenant-scoped**); append-only DB trigger as first line, hash chain + HMAC as second; ingest payloads are allow-listed (`eventAuditInput`).
- **Attacker classes:** authenticated tenant user (rate limit **disabled by default**), DB writer (can bypass trigger), misconfiguring operator.

### 2. Findings

| # | Sev | Finding | Evidence |
|---|---|---|---|
| **F1** | **Medium** | **HMAC strip via `signature_version` downgrade** — `record_hash` doesn't bind `SignatureVersion`; `VerifyChain` skips HMAC for version-0 rows anywhere in the chain with no monotonicity check. **Probe executed: downgrade alone passes; tamper+downgrade+re-chain passes; control tamper without downgrade is caught.** | `audit_ledger.go:331-334, 379-405` |
| **F2** | **Medium** | **Unbounded `VerifyChain`** — full-chain `Find(&rows)` per request; `/v1/audit/verify` reachable by any `audit.read` holder; rate limiter opt-in, off by default → authenticated memory/CPU DoS on large tenant chains. | `audit_ledger.go:322-325`; `config.yaml:133` |
| **F3** | Low | **`float64(-0)` false tamper alarm** (plan R2) — **probe executed: unmodified chain rejected** (`jsonb` stores `{"n": 0}` vs append-side `-0`). Latent: unreachable today (typed-scalar payloads), breaks any future user-JSON→payload path. | probe: `TestSecScratchNegativeZeroFalsePositive` |
| **F4** | Low | Anchor manifest route not tenant-scoped (ledger queries are) → cross-tenant anchor metadata for any `audit.read` holder. | `audit_anchor.go:176-206` |
| **F5** | Low | Placeholder keys (`change-me-please-very-long-audit-key`, `CHANGE_ME_...`) pass the ≥32 B check; a deployment on a repo-public key has forgeable signatures. | `validate.go:195-202` |
| F6/F7/F8 | Info | Malformed cursor → 500 (not 400); empty chain verifies as `true`; `cmd/migrate` now requires the key (fail-closed blast radius, needs upgrade note). | |

### 3. Abuse-case table (key rows)

- **Identity spoofing** — Not reachable: facts bound from verified context; ingest requires verified envelope.
- **Replay** — Not reachable: pre/post-lock dedupe + `UNIQUE (tenant_id, source, event_id)`.
- **Cross-tenant List/Verify** — Not reachable (tenant prefix + `mustTenant`); **anchor manifest — reachable (F4, metadata only)**.
- **Proxy/header forgery** — Not reachable (context-bound IDs; partition prefix prevents escape).
- **Resource exhaustion** — **Reachable via `/v1/audit/verify` (F2)**; payload paths bounded (64 KiB cap, depth-4 truncation).
- **Sensitive-data leakage** — Not reachable: redaction allow-list at write time; oracle-safe generic errors.
- **Chain forgery without key** — Not reachable (HMAC-SHA256, `hmac.Equal`, 32 B key min); **forgery with DB write = reachable via downgrade (F1, proven)**.

### 4. Positive controls verified / residuals / plan

- **Verified:** fail-closed empty-key guards before any DB touch (no-DB tests with zero-value `*gorm.DB`); config rejection table; byte-faithful canonicalizer (unit table + 10-row adversarial integration round-trip, all signed); tamper detection (payload/rehash/chain/signature — 4 integration tests); legacy unsigned rows verify by hash chain; constant-time HMAC; parameterized SQL throughout; full done gate green.
- **Residuals:** F1 (open, exploitable), F2 (open), R2 `-0`, R3 key rotation, F4/F5.
- **Validation plan:** **P0** monotone `signature_version` check + downgrade regression test (converts the proven bypass into a red test; `0,0,1,1` legacy-prefix must still verify). **P1** windowed `VerifyChain` + load test; `-0` normalization + canonicalizer property/fuzz test. **P2** tenant-scoped anchor manifest; placeholder-key refusal. **P3** cursor 400, empty-chain not-found.

**Verdict:** both planned fixes are correct and well-tested; no Critical/High in the changed surface. The two Medium findings (F1 signature downgrade, F2 unbounded verify) deserve action before this surface is advertised as tamper-evidence-grade — both have cheap, regression-testable remediations.
