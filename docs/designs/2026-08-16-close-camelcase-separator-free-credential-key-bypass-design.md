# Design — Close camelCase/separator-free credential-key bypass in the source-boundary redaction allow-list

Status: **Proposed** (implementation-ready)
Date: 2026-08-16
Module: `internal/connector/redact.go` + `internal/connector/redact_test.go` (new) + `internal/connector/http_summary_test.go` (one regression)
Scope: No change to any other package, no public API change, no config, no migrations, no new imports.

---

## 1. Verification verdict (untrusted evidence → tree)

Every claim in the evidence was re-verified against the live tree on 2026-08-16. **All load-bearing citations confirm.** Two immaterial line drifts found (self-flagged below); zero content deltas.

| # | Claim (evidence) | Verdict | Tree evidence |
|---|---|---|---|
| E1 | `RedactSummaryData` at `redact.go:13-21`; 64 KiB `sha256` truncation; depth cap 6; array recursion at `redact.go:42-51` | **Confirmed (Exact)** | `internal/connector/redact.go` — `RedactSummaryData` :13-21, `maxSummaryDataBytes = 64 * 1024` :23, `depth >= 6` guard :33, array recursion :42-51. |
| E2 | Parts list at `redact.go:25-28` (14 snake_case entries, no `api_key`/`signing_key`); `sensitiveSummaryKey` at :59-66 canonicalizes only `ToLower` + `ReplaceAll("-", "_")`, then substring-matches | **Confirmed (Exact)** | `redact.go:25-28`, :59-66. `strings.ToLower(strings.ReplaceAll(key, "-", "_"))` at :61. |
| E3 | Leak table: `accessKey`/`privateKey`/`apiKey`/`signingKey`/`signing_key`/`api_key` all redacted=false; `secretAccessKey`/`secret_access_key` redacted=true only via `secret`; `display_name` passthrough | **Confirmed (reproduced)** | Algorithm reproduced verbatim (Python, no repo write): all six leak rows `False`, `secretAccessKey`/`secret_access_key` `True`, `display_name` `False` (safe). Exact match to the evidence table. |
| E4 | Redaction call sites at `http_summary_request.go:162/176/190`; descriptor gate at :151-154 | **Confirmed (Exact)** | `appendDatasetSnapshots` redacts at :162 after `DatasetDescriptorOf` + `descriptor.Source != string(c.source)` skip at :152-153; `sourceReferences` :176; `membershipReferences` :190. |
| E5 | Durable persistence: `aggregation.go:116,151`, `operation_runner.go:141,153`, `sync_projection.go:21` call `UpsertSnapshot`; `account_snapshot.go:19,41` | **Confirmed (minor drift)** | `aggregation.go:151` (UpsertSnapshot; :116 is a `client.Summary` call in `refreshSource` — not a persistence site), `operation_runner.go:153`, `sync_projection.go:21`, `account_snapshot.go:19` (interface) and :133 (implementation; :41 is the repo constructor). Symbol-and-behavior claims exact; two line numbers drifted, immaterial. |
| E6 | Export inputs: `sync_export_manifest.go:18,29,43,51` feed `RedactSummaryData` | **Confirmed (Exact)** | All four sites at :18, :29, :43, :51. |
| E7 | Existing test covers only `token`/`body` at depth 2 + `display_name` passthrough; starts `http_summary_test.go:12`; no `redact_test.go` exists; no Vault summary test (Vault tests cover only `Erase`) | **Confirmed (Exact)** | `TestJSONSummaryConnectorRedactsSensitiveFields` at `http_summary_test.go:12` (payload `aero-im.profile` with `token`/nested `body`); `ls internal/connector/redact_test.go` → absent; `SourceAeroVault` appears in tests only for `TestJSONSummaryConnectorEraseUsesIdempotency` (Erase path) and `contract_test.go`/`http_summary_request_test.go` Erase tests. |
| E8 | `aero-vault.tenants` is a descriptor registered to `aero-vault` | **Confirmed (Exact)** | `internal/domain/aggregation.go:41,60` — `DatasetAeroVaultTenants = "aero-vault.tenants"`, `Source: "aero-vault"`. `SourceAeroVault` at `connector.go:17`; `NewJSONSummaryConnector` at `http_summary.go:52`. |
| E9 | Gates: check-architecture rule (d) bans connector non-test imports of service/repository; check-audit-swallow roots include `internal/connector`; golangci excludes `_test.go` from funlen/cyclop/lll | **Confirmed (Exact)** | `scripts/check-architecture.sh` rule (d) over `INFRA_LAYERS=(connector event outbox region sso)`; Makefile :99-100 passes `internal/connector` to `check-audit-swallow.sh`; `.golangci.yml:41-47` excludes `.*_test\.go$` from funlen/cyclop/lll. |
| E10 | `redact.go` is 67 lines today | **Confirmed (Exact)** | `wc -l internal/connector/redact.go` → 67. |

**Evidence deltas found: two line-number drifts only (E5), both immaterial and already flagged by the evidence's own drift note.** The leak is real, reachable to durable state (E4+E5+E6), and unguarded by tests (E7). The ACC set is adopted as specified.

---

## 2. Problem statement

`internal/connector/redact.go` is the source-boundary last line of defense before source-response payloads become durable projections (`UpsertSnapshot` from `aggregation.go:151`, `operation_runner.go:153`, `sync_projection.go:21`) or export inputs (`sync_export_manifest.go:18,29,43,51`). AGENTS.md (Data ownership and minimization) forbids persisting source credentials; the redaction allow-list must therefore be **shape-complete** across every spelling a credential key can take.

`sensitiveSummaryKey` canonicalizes keys with only `strings.ToLower(strings.ReplaceAll(key, "-", "_"))` and then substring-matches against a fixed part list. camelCase boundaries (`accessKey` → `accesskey`) and bare underscores (`api_key` → `api_key`) are never normalized, so substring matching against `access_key`/`private_key`/… misses every separator-free and camelCase credential shape:

| Key | Current result | Root cause |
|---|---|---|
| `accessKey` | **LEAK** | canonical `accesskey` does not contain `access_key` |
| `privateKey` | **LEAK** | canonical `privatekey` does not contain `private_key` |
| `apiKey` | **LEAK** | no part matches `apikey` at all |
| `signingKey` | **LEAK** | no part matches `signingkey` at all |
| `signing_key` | **LEAK** | no part matches `signing_key` at all |
| `api_key` | **LEAK** | no part matches `api_key` at all |
| `secretAccessKey` | redacted (accidental) | survives only because it contains `secret` |
| `display_name` | passthrough (correct) | — |

These are exactly the shapes produced by the surfaces in scope: Vault S3 (`accessKey`/`secretAccessKey`), PEM/private keys (`privateKey`), and the signing-key config field (`signingKey`/`signing_key`). Leaked values reach `domain.AccountSnapshot.Data`, `AccountSource.Data`, `AccountMembership.Data`, are upserted durably, and feed export manifests — a direct violation of the data-minimization contract.

---

## 3. Design

### 3.1 API changes

**No public shape change.** `RedactSummaryData(value map[string]any) map[string]any` keeps its exact signature and semantics; the three call sites in `http_summary_request.go` (and the four export sites) are untouched. All changes are internal to `redact.go`:

1. **New helper `canonicalKey(key string) string`** — the single source of truth for both sides of matching. Lowercases and removes every separator/whitespace character: `-`, `_`, `.`, and space (REQ-1 minimum). Implemented with a package-level `strings.NewReplacer` so call-time allocation is zero and **no new import is added** (`strings` is already imported; `NewReplacer` is in `strings`):

   ```go
   var summaryKeyCanonicalizer = strings.NewReplacer("-", "", "_", "", ".", "", " ", "")

   func canonicalKey(key string) string {
       return summaryKeyCanonicalizer.Replace(strings.ToLower(key))
   }
   ```

   One expression; cyclomatic complexity 1 (≤ 2 per REQ-1). `accessKey`, `access_key`, `access-key`, `access.key`, and `access key` all canonicalize to `accesskey`.

2. **Canonicalize the parts list once** — precompute the canonical forms of `sensitiveSummaryParts` at package init with the same `canonicalKey`, so the semantic list stays human-readable snake_case and both sides are provably identical:

   ```go
   var sensitiveSummaryCanonicalParts = func() []string {
       parts := make([]string, 0, len(sensitiveSummaryParts))
       for _, part := range sensitiveSummaryParts {
           parts = append(parts, canonicalKey(part))
       }
       return parts
   }()
   ```

3. **Two new semantic entries close the enumerated credential-key class (REQ-2):** append `"api_key"` and `"signing_key"` to `sensitiveSummaryParts` (kept in snake_case spelling per the evidence's design note; they canonicalize to `apikey`/`signingkey` at init). The list grows 14 → 16 entries. No generic bare `key` shape is added (REQ-3).

4. **`sensitiveSummaryKey` uses the canonical helper on both sides** and keeps its exact structure (substring loop + exact-match set):

   ```go
   func sensitiveSummaryKey(key string) bool {
       key = canonicalKey(key)
       for _, part := range sensitiveSummaryCanonicalParts {
           if strings.Contains(key, part) {
               return true
           }
       }
       return key == "body" || key == "content" || key == "raw" || key == "messages" || key == "message"
   }
   ```

   The exact-match set is now evaluated against the fully canonical key; for the five literal shapes (`body`/`content`/`raw`/`messages`/`message`), which contain no separators, results are bit-identical to today.

No change to: the depth cap (6), the 64 KiB `sha256` truncation (`RedactSummaryData` :13-21), array recursion (:42-51), or any other file. `redact.go` grows 67 → ≈77 lines (well under the 500-line gate) and the changed function stays at complexity 3 (base + loop + if), under the cyclop cap of 10.

### 3.2 Why this closes the class without the rejected alternatives

- **Not camelCase word-splitting:** stripping separators is strictly more complete (also closes `access-key`, `access.key`, `access key` in one rule) with strictly less code and lower cyclomatic risk.
- **Not a bare `key` shape:** `keyboard`/`monkey`/`keynote` remain safe (verified empirically — see §5.1), while the enumerated class (`access`/`private`/`api`/`signing` + key, plus the existing `secret`/`token`/… family) is fully closed.
- **Monotone widening:** if old canonical `K` contained part `P` (i.e. `K = X + P + Y`), then the stripped forms satisfy `strip(K) = strip(X) + strip(P) + strip(Y)`, so every previously redacted key still matches. The fix only *adds* redactions; it can never stop redacting a key.

---

## 4. Compatibility constraints

1. **Public API frozen:** `RedactSummaryData`, `Summary`, `Snapshot`, and connector constructors are untouched. No proto, config, or migration change.
2. **Every previously redacted key stays redacted (REQ-4):** monotone-widening proof above; empirically pinned by the §5.1 regression rows (`token`, `body`, `password`, `client_secret`, `secret`, `secretAccessKey`, `secret_access_key`, `access_token`, `authorization`, `mfa`, `recovery`, `object_bytes`, `blob`, `message_body`, `raw`, `content`, `messages`, `message`, and depth-2/array-nested occurrences).
3. **Over-redaction is bounded and intended:** the new drops are exactly the separator/case variants of the existing class plus the two new enumerated shapes (`apikey`, `signingkey`). A safe key is dropped only if its canonical form contains one of the 16 enumerated shapes. `display_name`, `source_region`, `generated_at`, `version`, `status`, `last_synced_at`, `truncated`, `sha256` all verified safe.
4. **Degenerate keys unchanged:** a separator-only or empty key canonicalizes to `""`; substring matching and the exact set both miss it, exactly as today.
5. **Import/lint invariants:** no new imports in connector non-test code (check-architecture rule (d) stays trivially green); no `Record`/`Log` audit sites added (check-audit-swallow green); new `_test.go` files are exempt from funlen/cyclop/lll per `.golangci.yml:41-47` but still run under vet/staticcheck.
6. **Durable rows already written are not retroactively scrubbed by this fix** — projections are disposable and replayable, so remediation is operational (see §6), not a schema migration.

---

## 5. Failure modes

| Failure mode | Probability / impact | Mitigation |
|---|---|---|
| **False positive — safe key dropped** (e.g. a future field whose canonical form contains `refresh`, `token`, `accesskey`, …) | Low impact: a dropped summary field (projection gap), never data corruption. Probability low: shapes are credential-specific; `keyboard`/`monkey`/`keynote`/`truncated`/`source_region` verified safe. | Enumerated shape set, not a generic `key`; unit pins on the safe set (§5.1); future credential classes require an explicit shape addition, documented in a comment above the parts list. |
| **Under-redaction — a novel credential shape leaks** (e.g. `passphrase`, `clientCert`) | Bounded: same enumerative class limitation exists today; the direction explicitly rejects the generic-`key` broadening that would close it. | The fix closes the *enumerated* class (REQ-2) with zero false-positive risk; the parts list is the extension point and is now canonicalized, so any future entry in any spelling works. Replay/`make check-*` gates catch regressions. |
| **Regression — a previously redacted key survives** | Near zero: canonicalization only widens matching (monotone-widening proof, §3.2). | REQ-4 regression rows in the unit suite run on every `make test`. |
| **Gate failure** | Negligible: ~10-line single-file change in an infra layer that is already lint-strict-clean; no new imports; no audit sites. | §5.3 full gate is the done gate. |
| **Leaked historical rows remain in durable snapshots after deploy** | Certain (pre-existing leaks), until remediated. | Operational re-sync/replay regenerates disposable projection rows (§6); exports regenerate on the next export job. |

---

## 6. Migration steps

No code migration, schema change, or config change is required — this is a behavior fix plus tests.

1. **Commit 1 (the fix):** edit `internal/connector/redact.go` per §3.1; add `internal/connector/redact_test.go`; add the httptest regression to `internal/connector/http_summary_test.go`. No other file changes.
2. **Green gate (evidence §5.3):** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-architecture && make build`.
3. **Post-deploy remediation (operational, no code):** trigger projection re-sync/replay for existing accounts so `UpsertSnapshot` rows are regenerated from freshly redacted source payloads. Because projections are disposable and replayable, this scrubs the leaked shapes from durable snapshot rows without touching account links, operation state, audit facts, or callback delivery state (authoritative, non-disposable data). Export manifests rebuild cleanly on the next export job since they re-apply `RedactSummaryData` at generation time.
4. **Optional verification sweep (read-only):** a one-off query over snapshot `data` JSONB for residual `accessKey`/`privateKey`/`apiKey`/`signingKey` keys before/after replay confirms remediation; not a code change.
5. **DoD check:** `git diff` after the commit touches exactly `redact.go`, `redact_test.go`, `http_summary_test.go`.

---

## 7. Testable acceptance mapping

| Requirement | Acceptance (test) | Location | Gate |
|---|---|---|---|
| REQ-1 — separator/case-insensitive canonical matching | Table rows: `accessKey`, `access_key`, `access-key`, `access.key`, `access key` → **all dropped** | `redact_test.go` `TestRedactSummaryDataDropsCamelCaseCredentialKeys` | `make test` |
| REQ-2 — shape set closes the class | Table rows: `apiKey`, `signingKey`, `signing_key`, `api_key` → **dropped**; canonical parts precomputed from the semantic list (identical helper) | same table | `make test` |
| REQ-2 — exact-match set preserved | `body`, `content`, `raw`, `messages`, `message` → **dropped** (exact, not substring) | same table | `make test` |
| REQ-3 — safe passthrough, no over-broadening | `display_name: "Ada"` and safe keys pass through **unchanged** (keys dropped, never rewritten; values untouched); `keyboard`, `monkey`, `keynote` → **not dropped** | same table + existing test's `display_name` assertion | `make test` |
| REQ-4 — no regression | Rows: `token`, `body`, `password`, `client_secret`, `secret`, `secretAccessKey`, `secret_access_key`, `access_token`, `authorization`, `mfa`, `recovery`, `object_bytes`, `blob`, `message_body`, `raw`, `content`, `messages`, `message` → **dropped**; nested depth-2 (`"tenant":{"owner":{…}}`) and `[]any` element containing a credential-key map → **dropped** | same table | `make test` |
| REQ-5 — scope/budgets | Only the three named files change; `redact.go` ≤ 500 lines; changed function cyclomatic ≤ 10, ≤ 50 lines/40 stmts, ≤ 160 cols; no new connector imports; no audit `Record`/`Log` sites | — | `make check-file-sizes`, `make check-architecture`, `make check-audit-swallow`, `make lint-strict` |
| §5.2 — end-to-end vault regression | `httptest.Server` returns `{"source_region":"west","version":1,"datasets":{"aero-vault.tenants":{"display_name":"Ada","owner":{"accessKey":"AKIA…","privateKey":"-----BEGIN…"}}}}`; `NewJSONSummaryConnector(SourceAeroVault, …)`; assert `len(summary.Snapshots)==1`, `Data["display_name"]=="Ada"`, and **zero leaks** anywhere under `Snapshots[0].Data` (walk finds no `accessKey`/`privateKey` at depth 1 or 2) | `http_summary_test.go` `TestJSONSummaryConnectorRedactsCamelCaseCredentials` | `make test` |
| Done gate (§5.3) | All commands green | — | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-architecture && make build` |
| DoD (e) — no out-of-scope modification | `git diff` touches exactly `redact.go`, `redact_test.go`, `http_summary_test.go` | — | review |

Empirical pre-change baseline (reproduced 2026-08-16, no repo writes): the current matcher leaks exactly `accessKey`, `privateKey`, `apiKey`, `signingKey`, `signing_key`, `api_key`; the proposed matcher (canonicalize both sides + two new shapes) drops all six, keeps all REQ-4 rows redacted, and leaves `display_name`, `keyboard`, `monkey`, `keynote`, `truncated`, `sha256`, `source_region`, `last_synced_at`, `owner`, `datasets` untouched.
