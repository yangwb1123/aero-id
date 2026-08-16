# Design — Close the snaplink PII redaction allow-list hole (jwt/passcode/otp/verification_code)

- **Date:** 2026-08-16
- **Module:** `internal/connector` (shared redaction boundary serving `internal/connector/snaplink`)
- **Direction ID:** Close the PII redaction allow-list hole (value 9 / risk_reduction 9 / effort 2 / confidence 9)
- **Status:** Proposed — this document is the design deliverable. Implementation and acceptance tests are
  deliberately NOT included in this change set.

---

## 1. Verification verdict

The requirements specification (`docs/auto/runs/close-the-pii-redaction-allow-list-hole-api-key--19ff0b26/artifacts/requirements-10762e10/requirements.md`)
was treated as untrusted evidence and checked claim-by-claim against the live tree at HEAD (`eb9d5ef`).
Every cited location was read directly; no in-package probes were needed. **All substantive claims are
confirmed**, including the tree-moved caveat: `daa5294` already added `api_key`/`signing_key` to the
allow-list and `redact_test.go` already pins those shapes — the remaining hole is exactly the four
credential/verification shapes plus two unpinned behaviors.

| # | Claim | Verified location | Verification result |
|---|---|---|---|
| V1 | `sensitiveSummaryParts` allow-list (`redact.go:32-38`) contains `password, token, secret, cookie, mfa, recovery, credential, private_key, access_key, refresh, authorization, object_bytes, blob, message_body, api_key, signing_key, message_text, latest_message, recent_message, message_preview, chat_history, conversation` and **lacks** `jwt`/`passcode`/`otp`/`verification_code` | `internal/connector/redact.go:32-38` | ✅ **Confirmed.** `grep -n "jwt\|passcode\|otp\|verification_code" internal/connector/redact.go internal/connector/*_test.go` → zero hits (exit 1). |
| V2 | Matcher is substring over canonical parts plus exact-key fallback `body\|content\|raw\|messages\|message`; `jwt/passcode/otp/verificationcode` satisfy neither | `redact.go` `sensitiveSummaryKey` (~:88-96): `strings.Contains(canonical, part)` loop, then `canonical == "body" \|\| "content" \|\| "raw" \|\| "messages" \|\| "message"` | ✅ **Confirmed.** By construction: `canonicalSummaryKey("verification_code") = "verificationcode"` contains no part and matches no exact word; same for `jwt`, `passcode`, `otp`. These keys survive `redactSummaryMap` (skipped set at `redact.go:57`). |
| V3 | Canonicalization lowercases and strips `-`, `_`, `.`, space | `redact.go:84-86` (`canonicalSummaryKey`) | ✅ **Confirmed.** |
| V4 | Truncation fallback: after redaction, if `len(marshal) > maxSummaryDataBytes` (64KB), return `{"truncated": true, "sha256": hex}`; zero test coverage of this branch | `redact.go:12-21` (branch at :15-19; evidence cited :16-18 — same function, off by one on comment lines, not material); `maxSummaryDataBytes` at :23. Grep for `truncated`/`maxSummaryDataBytes` in `internal/connector/*_test.go`: only `fault_injection_test.go:72` writes a *malformed JSON body* labeled "truncated JSON" — unrelated to the redaction branch. No test references the fallback map or constant. | ✅ **Confirmed.** |
| V5 | `snaplink.identity` and `snaplink.security` are `PII: true`, source `snaplink` | `internal/domain/aggregation.go:34,37` (dataset constants), `:49` (`PII` field), `:53,:56` (registry entries `PII: true`, `Source: "snaplink"`) | ✅ **Confirmed.** |
| V6 | Redaction applied at the snaplink summary boundary before persistence: `appendDatasetSnapshots` builds `AccountSnapshot{... Data: RedactSummaryData(values)}`; dataset admission filter skips non-`snaplink` datasets; source refs and memberships also redacted | `internal/connector/http_summary_request.go` — admission filter `if !ok \|\| descriptor.Source != string(c.source) { result.SkippedDatasets = append(...); continue }`; snapshot line `Data: RedactSummaryData(values)`; `sourceReferences` and `membershipReferences` both call `RedactSummaryData` | ✅ **Confirmed.** |
| V7 | Redaction reused on durable export paths | `internal/service/sync_export_manifest.go:18` (preferences), `:29` (sources `data`), `:43` (memberships `data`), `:51` (snapshots `data`) — all `connector.RedactSummaryData` | ✅ **Confirmed.** |
| V8 | Existing redaction coverage pins `apiKey`/`api_key`/`signing_key`/`access_token`/`mfa`, separator variants, nested+array, `key`-substring guard, IM message-content shapes — **none** cover the four missing shapes, standalone `api`/`code`/`session`, or truncation | `internal/connector/redact_test.go` — `containsSensitiveSummaryKey` helper (:5-20), `TestRedactSummaryDataDropsCamelCaseCredentialKeys` (:26), `TestRedactSummaryDataCanonicalizesSeparatorVariants` (:45), `TestRedactSummaryDataNestedAndArrayCredentials` (:55), `TestRedactSummaryDataDoesNotBroadenGenericKey` (:93), message-content tests (:106+), counter-key test (:138). `http_summary_test.go` — token/body connector test (:12-37), camelCase connector test (:39-65) | ✅ **Confirmed.** |
| V9 | Gates exist and baseline is green; `redact.go` is 96 lines | `Makefile:42` (`check-architecture`), `:69` (`check-file-sizes`), `:99-100` (`check-audit-swallow` — scans `internal/connector`). `wc -l internal/connector/redact.go` = 96 | ✅ **Confirmed.** |
| V10 | Constructor/request shape needed for the REQ-2 regression test exists | `internal/connector/connector.go:15` (`SourceSnaplink`), `:20` (`SummaryRequest`), `http_summary.go:52` (`NewJSONSummaryConnector(source, cfg)`) | ✅ **Confirmed.** The REQ-2 test sketch mirrors the live `TestJSONSummaryConnectorRedactsSensitiveFields` pattern exactly. |
| V11 | Over-redaction blast radius is minimal: no in-repo code reads keys that would newly be dropped | `grep -rn "totp\|hotp\|passcode\|verification_code\|jwt"` over non-test Go: only `internal/sso/jwks.go` JWT-verification error strings (unrelated to summary-data keys). `RedactSummaryData` call sites are only `redact.go`, `http_summary_request.go`, `sync_export_manifest.go` | ✅ **Confirmed.** No consumer depends on `otp_enabled`/`totp_enabled`/`jwt_claims`/`passcode_*` projection keys. |

**Minor deltas vs. the evidence (none material):**

- V4's line citation (`:16-18`) is inside the same `RedactSummaryData` function; the branch actually spans
  `redact.go:15-19`. Same behavior, same constant.
- The evidence's "single token/body test" superseded claim is confirmed: `http_summary_test.go` now has two
  connector redaction tests plus the unit suite in `redact_test.go`, exactly as the caveat states.
- The `fault_injection_test.go:72` "truncated JSON" hit is a malformed-body fault-injection fixture, not the
  sha256 truncation branch; implementers should not mistake it for coverage of V4.

---

## 2. Problem statement

`RedactSummaryData` is the last line of defense before a source response becomes a durable snapshot
projection (`internal/connector/http_summary_request.go` snapshot/source-ref/membership paths) or an export
payload (`internal/service/sync_export_manifest.go:18,29,43,51`). The allow-list `sensitiveSummaryParts`
(`redact.go:32-38`) matched against canonicalized keys leaves four real credential/verification shapes
unmatched:

| Key shape | Canonical form | Contains a part? | Equals `body\|content\|raw\|messages\|message`? | Result |
|---|---|---|---|---|
| `jwt` | `jwt` | no | no | **leaks** |
| `passcode` | `passcode` | no | no | **leaks** |
| `otp` | `otp` | no | no | **leaks** |
| `verification_code` | `verificationcode` | no | no | **leaks** |

Impact is snaplink-specific and load-bearing: `internal/domain/aggregation.go:53,56` marks
`snaplink.identity` / `snaplink.security` as `PII: true`, and every payload key admitted to those datasets
passes through `RedactSummaryData` before `AccountSnapshot.Data` is persisted. A snaplink response carrying
`{"identity":{"jwt":"…","passcode":"…"}}` is therefore persisted into projections **and** re-exported
verbatim, violating AGENTS.md invariants: "never record … MFA/recovery material" and the allow-listed,
redacted-payload rule for durable PII facts.

The design closes the write path by extending the shared allow-list — the same boundary already guards all
three snaplink summary surfaces and all four export call sites — and pins it with a snaplink-identity-shaped
regression test, a false-positive negative fixture, and a truncation-branch pin.

---

## 3. Design

### 3.1 Production change (REQ-1) — extend the allow-list

**File:** `internal/connector/redact.go`, `sensitiveSummaryParts` (:32-38).

Add one group line:

```go
// verification/OTP shapes: jwt/passcode/otp/verification_code (incl. totp*/hotp* spellings via the otp substring)
"jwt", "passcode", "otp", "verification_code",
```

placed after the `"api_key", "signing_key",` line and before the message-content group, with a comment
documenting the deliberate `otp`-subsumes-`totp`/`hotp` conservatism. No other production code changes:
the matcher (`sensitiveSummaryKey`), canonicalizer (`canonicalSummaryKey`), truncation constant
(`maxSummaryDataBytes`), exact-word fallback set, and call sites are untouched. `redact.go` grows from 96 to
≈101 lines — far below the 500-line `check-file-sizes` gate.

**How the four shapes resolve (all verified against the canonicalizer):**

| Source spelling | Canonical form | Matched part | Dropped? |
|---|---|---|---|
| `jwt`, `JWT` | `jwt` | `jwt` | ✅ |
| `jwt_claims`, `jwtAlgorithm` | `jwtclaims` / `jwtalgorithm` | `jwt` | ✅ |
| `passcode`, `passCode`, `pass-code` | `passcode` | `passcode` | ✅ |
| `passcode_attempts`, `passcodeExpiresAt` | `passcodeattempts` / `passcodeexpiresat` | `passcode` | ✅ |
| `otp`, `OTP_secret`, `otpSecret` | `otp` / `otpsecret` | `otp` | ✅ |
| `totp_secret`, `hotpSecret`, `totp_enabled` | `totpsecret` / `hotpsecret` / `totpenabled` | `otp` (substring) | ✅ |
| `verification_code`, `verificationCode`, `verification-code`, `verification.code`, `verification code` | `verificationcode` | `verificationcode` | ✅ |
| `verification_code_expires_at` | `verificationcodeexpiresat` | `verificationcode` | ✅ |

**Standalone-key negative guard (verified unchanged):** `api`→`api`, `code`→`code`, `session`→`session`,
`api_version`→`apiversion`, `session_id`→`sessionid` contain none of the (now longer) canonical parts and
match no exact word — all retained. In particular `code` is not a part, so `code` and `api_version` cannot
false-positive off `passcode`/`verificationcode` (the substring direction is key-contains-part, never
part-contains-key).

### 3.2 API changes

- **Public surface: none.** No exported symbols added, removed, or re-typed; no protobuf, OpenAPI, config,
  or database changes; no new package.
- **Behavioral surface: `RedactSummaryData` (and transitively the snaplink summary snapshots, source refs,
  memberships, and export manifest) now drops a strictly larger set of keys.** The output shape contract of
  `RedactSummaryData` — `map[string]any`, sensitive keys dropped at any depth ≤ 6, `{"truncated": true,
  "sha256": hex}` for >64KB redacted output — is unchanged.

### 3.3 Test changes (REQ-2..REQ-4)

All additions live in `internal/connector`; no integration build tag required.

1. **`http_summary_test.go`** — `TestJSONSummaryConnectorRedactsSnaplinkIdentityCredentials`: drives the
   real `NewJSONSummaryConnector(SourceSnaplink, …)` against an `httptest` server returning a
   `snaplink.identity` dataset carrying `api_key`, `apikey`, `jwt`, `passcode`, `otp`, `verification_code`,
   `access_token`, `mfa_secret` nested under `identity` plus safe `display_name`/`email`/`verified`; asserts
   exactly one snapshot, safe fields retained with original values, `containsSensitiveSummaryKey(Data) ==
   false`, and the nested `identity` map retained-but-empty (`map[string]any{}`, `len == 0`).
2. **`redact_test.go`** — `TestRedactSummaryDataDropsSnaplinkIdentityShapes`: table-driven direct
   `RedactSummaryData` subtests over `jwt`, `JWT`, `passCode`, `pass-code`, `OTP_secret`, `totp_secret`,
   `hotpSecret`, `verificationCode`, `verification-code`, `verification.code`, `verification code` (mirrors
   the existing `TestRedactSummaryDataDropsCamelCaseCredentialKeys` pattern), each with a companion safe key
   that must survive. Pins the canonicalization claim for the new parts.
3. **`redact_test.go`** — `TestRedactSummaryDataRetainsApiCodeSessionKeys`: feeds
   `{"api":"v1","code":"OK","session":"s1","api_version":"2","session_id":"s-42","display_name":"Ada"}` and
   asserts **all** keys retained with original values. Complements the existing `key`-substring guard
   (`redact_test.go:93`).
4. **`redact_test.go`** — `TestRedactSummaryDataTruncatesOversizedPayload`: input
   `{"token":"secret","body":"message-body","display_name": strings.Repeat("A", 70*1024)}` — size must come
   from the **safe** field because sensitive keys are dropped before the size check. Asserts exactly two keys,
   `truncated == true`, `sha256` is 64 lowercase hex chars, and the digest equals
   `sha256(json.Marshal({"display_name": <70KB "A"s}))` — proving credential keys never enter the digest
   (`encoding/json` sorts map keys, so the digest is deterministic). Also asserts
   `containsSensitiveSummaryKey(got) == false` on the fallback map.

### 3.4 Configuration / data model

None. No config keys, no migrations, no schema fields.

---

## 4. Change set

| File | Change | Approx. delta |
|---|---|---|
| `internal/connector/redact.go` | +1 allow-list group line (4 strings) + group comment | +5 lines (96 → ≈101) |
| `internal/connector/redact_test.go` | +3 unit tests (shapes table, negative fixture, truncation pin) | +80–100 lines |
| `internal/connector/http_summary_test.go` | +1 connector-level snaplink test | +35–45 lines |
| Makefile / proto / config / migrations | none | — |

Test-file additions are exempt from funlen/cyclop/lll per `.golangci.yml:41-44`; production delta stays well
under the 500-line `check-file-sizes` limit.

---

## 5. Compatibility constraints

1. **Stricter redaction is a deliberate, documented behavior tightening.** Previously-surviving keys that
   now drop: `jwt*`, `passcode*`, `otp*`/`totp*`/`hotp*` (including state flags like `otp_enabled`,
   `totp_enabled`), `verification_code*` and separator variants. Verified: no in-repo consumer reads these
   keys from snapshots or exports (V11), so no downstream breakage is expected inside the repo. External
   consumers of snaplink summary/export data must treat absent keys as unknown/not-present, per the existing
   per-source `status`/`version`/`generated_at`/`last_synced_at` freshness contract.
2. **The `otp` substring is intentionally broad.** It catches `totp_*`/`hotp_*` spellings fail-safe. This is
   the conservative direction; narrowing it later (e.g., to project `totp_enabled`) requires an explicit
   allow-list decision with a security review, not a silent matcher tweak. Documented in the code comment.
3. **Known accepted false positives.** Any key whose canonical form contains `otp` (e.g. the improbable
   `potpourri` → `potpourri` contains `otp` at index 1) or `jwt`/`passcode`/`verificationcode` substrings is
   dropped. In account-summary key space this is a non-issue; the conservative direction wins.
4. **No wire/proto/schema/config contract changes.** Snapshot `Data` and export payloads keep their shape;
   only membership of the dropped-key set changes.
5. **Public API compatibility:** zero. `RedactSummaryData` signature, `Summary`/`AccountSnapshot`/export
   JSON shapes, and all connector interfaces are untouched.
6. **The exact-word fallback set and `maxSummaryDataBytes` are unchanged** (explicitly out of scope), so
   `body`/`content`/`raw`/`messages`/`message` semantics and the 64KB threshold behavior are byte-for-byte
   identical.

---

## 6. Testable acceptance mapping

| Requirement | Test (file) | Pass criterion |
|---|---|---|
| REQ-1 allow-list extension | `TestRedactSummaryDataDropsSnaplinkIdentityShapes` (`redact_test.go`) | Every row of the variant table (incl. `JWT`, `passCode`, `pass-code`, `OTP_secret`, `totp_secret`, `hotpSecret`, `verificationCode`, `verification-code`, `verification.code`, `verification code`) is dropped; companion safe key `display_name` survives. |
| REQ-2 snaplink boundary pin | `TestJSONSummaryConnectorRedactsSnaplinkIdentityCredentials` (`http_summary_test.go`) | Exactly 1 snapshot; `display_name == "Ada"`, `email == "ada@example.com"`, `verified == true`; `containsSensitiveSummaryKey(Data) == false` at every depth; nested `identity` map present with `len == 0`. |
| REQ-3 negative fixture | `TestRedactSummaryDataRetainsApiCodeSessionKeys` (`redact_test.go`) | All of `api`, `code`, `session`, `api_version`, `session_id`, `display_name` retained with original values; no key dropped. |
| REQ-4 truncation pin | `TestRedactSummaryDataTruncatesOversizedPayload` (`redact_test.go`) | Exactly keys `{truncated, sha256}`; `truncated == true`; `sha256` is 64 lowercase hex chars and equals the digest of the redacted content (`{"display_name": <70KB "A"s>}`), proving `token`/`body` values never enter the digest; `containsSensitiveSummaryKey(got) == false`. |
| REQ-5 done gate | CI gates | `make test && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-architecture` all green. `make check-migration-set` green (no migration files touched). `make check-integration-compile` not required (no integration-tagged code added); run it anyway for the full pipeline. |

**Negative controls the suite already provides:** existing `TestRedactSummaryDataDropsCamelCaseCredentialKeys`
(`redact_test.go:26`) pins `apiKey`/`api_key`/`signing_key`/`access_token`/`mfa` (regression-guards `daa5294`);
`TestRedactSummaryDataDoesNotBroadenGenericKey` (:93) pins `keyboard`/`monkey`/`keynote`; the new REQ-3 test
extends that guard to `api`/`code`/`session`. A matcher that naively added bare `code`/`session`/`api` parts
would fail REQ-3; a matcher that only added `jwt`/`passcode` but forgot `otp`/`verification_code` would fail
REQ-2's payload.

---

## 7. Failure modes and operational notes

| Failure mode | Detection | Impact | Mitigation / note |
|---|---|---|---|
| Over-redaction drops a legitimately-needed safe key (e.g. `totp_enabled` state flag) | REQ-3 negative fixture stays green; downstream consumer complains about a missing key | Projected state flag disappears; consumers must treat missing as unknown (already the freshness contract) | Documented conservative direction. No in-repo consumer reads these keys (V11). If a future requirement needs a specific flag projected, add it to the *explicit* safe set after security review — never narrow `otp`. |
| Newly-added part accidentally substring-matches a safe key (e.g. `potpourri`, `jwt` inside `jwthelper`) | Review of canonical-part set at code review; REQ-3 guard | Conservative drop of an unlikely key | Accepted, documented in the code comment. The four parts are short but high-signal; the negative fixture pins the exact guard boundary. |
| Existing durable rows already polluted by pre-fix leaks (jwt/passcode/otp/verification_code values persisted in `AccountSnapshot.Data`) | Data audit / reconciliation drift | Stale PII remains readable until overwritten | **The fix closes the write path only.** Snapshots are disposable, replayable projections (AGENTS.md); run a reconcile/re-sync pass (e.g. `SyncJobReconcile`) so fresh redacted snapshots overwrite polluted rows. Audit-fact and operation-state data were never fed by this boundary in a way that would contain these values; verify per deployment if any pre-fix export artifact requires lifecycle handling. |
| Already-delivered export artifacts containing leaked values | Post-fix export review | Exported PII is out of aero-id's control | Exports are delivered artifacts; per AGENTS.md they are encrypted, access-controlled, TTL-bound. Revoke/rotate affected artifact URLs and honor TTL/erase policy. The fix guarantees **future** exports are redacted. |
| Truncation ordering regression (someone moves the size check before redaction) | REQ-4 digest-equality assertion | Raw credentials could enter the sha256 digest or the fallback map | REQ-4 pins digest-over-redacted-content ordering. Keep `maxSummaryDataBytes` check after `redactSummaryMap`. |
| `make check-audit-swallow` trips | CI gate | — | No audit writes are added or removed; the change is pure data filtering. Gate stays green. |
| Misapplied fix lands in a wrapper instead of the shared boundary | Code review; REQ-2 uses `SourceSnaplink` connector directly | snaplink-only patch; IM/Vault and export paths stay vulnerable | The fix must land in `sensitiveSummaryParts` (shared). REQ-2's connector-level test and the existing IM/Vault connector tests guard the shared boundary. |

---

## 8. Migration steps

1. **Code change only.** No SQL migration, no config migration, no proto regeneration, no data backfill
   script. `make check-migration-set` is unaffected.
2. **Merge & deploy** the `redact.go` + test change as one commit. No feature flag is warranted: the change
   is a strict tightening of an existing defensive boundary with zero in-repo consumers of the newly-dropped
   keys.
3. **Purge stale projections (operational).** After deploy, trigger a reconcile/re-sync pass over snaplink
   snapshots so pre-fix polluted `AccountSnapshot.Data` rows are overwritten with freshly redacted data.
   Snapshots are disposable/replayable by contract; this is the sanctioned purge path. Verify a sample of
   `AccountSnapshot.Data` rows contains no `jwt`/`passcode`/`otp`/`verification_code` keys afterwards
   (e.g. via the same `containsSensitiveSummaryKey` matcher in a one-off check).
4. **Export artifacts (operational).** Enforce TTL/rotation on any export artifacts produced before the fix;
   future exports are redacted by the shared boundary.
5. **Verify gates:** `make test && make lint-strict && make check-file-sizes && make check-audit-swallow &&
   make check-architecture && make check-migration-set && make check-integration-compile && make build`.

---

## 9. Non-goals / out of scope

- The other two directions from the same analysis (snaplink adapter factory/filter/region-pinning coverage;
  the Health seam auth/header contract). No change to `internal/connector/snaplink`.
- Re-adding or re-testing `api_key`/`apikey`/`signing_key` (landed in `daa5294`, pinned in `redact_test.go`).
- Changing the exact-key fallback set, `maxSummaryDataBytes`, redaction depth, or the truncation map shape.
- Narrowing the `otp` substring to distinguish `totp_enabled` state flags — that is a future security-reviewed
  allow-list decision, explicitly deferred.
- Retroactive scrubbing of already-delivered export artifacts or already-persisted audit facts (covered
  operationally in §8; no new scrubber in this change).
- No new package, no new connector surface, no domain/model changes.
