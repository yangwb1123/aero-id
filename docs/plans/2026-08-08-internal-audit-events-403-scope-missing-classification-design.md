# Design — internal/audit: events-endpoint 403 → `ErrTokenScopeMissing` classification and alert (REQ-1)

> **Requirements authority:** `docs/requirements/2026-08-08-internal-audit-events-403-scope-missing-classification-requirements.md`
> **Design authority:** `docs/plans/2026-08-07-auditgovernance-tokensource-merged-interface.md` §3 events-403 row (:81), §4 routing (:119); `docs/plans/2026-08-07-internal-outbox-tokensource-invalidate-on-401-design.md` §2.4 `StatusForbidden` branch (:153-155)
> **Date:** 2026-08-08
> **Module:** `internal/connector/auditgovernance` (connector) + `internal/service` (test-only pin). The dead `internal/audit` tree is not touched.

---

## 1. Evidence verification (untrusted claims re-checked against the working tree)

Every pillar of the supplied evidence was re-verified by reading the code and running the baselines. All hold; two citations had minor line drift (substance unchanged):

| # | Claim | Verdict | Verified finding |
|---|---|---|---|
| E1 | `Publish` has only a 401 branch; 403 → `validateReceipt` | ✅ | `publisher.go:84-115`; 401 branch :98-113 (cited :100-115), `return validateReceipt(response, fact)` at :114 (cited :116). `grep StatusForbidden` in the package: zero hits. |
| E2 | Generic non-202 `audit governance: HTTP %d` | ✅ | `validateReceipt` at :246-289; generic error at :248-250 exactly as cited. |
| E3 | `oauth.go` no-op `BindingUnhealthy`/`MarkBindingUnhealthy` stubs | ✅ | :142-150 (cited :145-150); deferral comment verbatim; interface members at `types.go:82-89` with the "no third signature" pin. |
| E4 | `classifyScopeMissing` routes bare `ErrTokenScopeMissing` → `errAuditTenantUnbound` + counter | ✅ | `callback_audit.go:195-206`; `errAuditTenantUnbound` :15; `errors.Is` routing :200-204; counter on the marker-free branch :201-203. |
| E5 | `publicationOutcome` dead-letters at MaxAttempts; unbound branch precedes cap | ✅ | :144-155; unbound :148-150, cap :151-153. |
| E6 | Zero 403 fixtures in service/connector tests | ✅ | Zero `StatusForbidden` in `internal/service/*_test.go` and `internal/connector/auditgovernance/*_test.go`. (The `403` hits in `comment_test.go`/`file_test.go`/`message_test.go` are unrelated `assertAppCode` AppError assertions.) |
| E7 | REQ-5 comment lists only 401-scope-body + token-endpoint | ✅ | `callback_audit.go:186-194` comment omits 403; the 403 contract lives only in the merged plan §3/§4 + tenantid design §2. |

Baselines: `go test ./internal/connector/auditgovernance/ -count=1` and `go test ./internal/service/ -count=1` both green before any change.

**Conclusion (agrees with the requirements doc's key finding):** the service-side routing is complete and correct — a bare, marker-free `ErrTokenScopeMissing` from the connector traverses `classifyScopeMissing` → `errAuditTenantUnbound` → `pending`/`audit.binding_unavailable` with `ObserveAuditScopeMissing` once per claimed attempt, and `publicationOutcome`'s unbound branch (:148-150) precedes the MaxAttempts cap (:151-153), so the row parks and re-probes forever. The **only** production change is the connector's missing 403 branch in `Publisher.Publish`.

---

## 2. Design summary

Insert one `http.StatusForbidden` branch in `Publisher.Publish` (publisher.go) between the 401 branch (:98-113) and `return validateReceipt(...)` (:114), mirroring the 401 branch's structure with four deliberate deltas:

1. Side effect is **`MarkBindingUnhealthy`** (not `Invalidate`) — cache retained, epoch unchanged.
2. Scope-body classification reuses the **same shared classifier pair** `tokenErrorCode`/`scopeRejectedCode` (oauth.go:317-331).
3. The error is **never** wrapped with `ErrTokenEndpointScopeMissing` (marker is token-endpoint-only; the drain counter must fire).
4. A 403 never reaches `validateReceipt`'s generic bucket.

### 2.1 Exact code change (publisher.go, `Publish`)

```go
	if response.StatusCode == http.StatusForbidden {
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxReceiptBytes))
		// Status-driven binding health (merged plan §3 events-403 row): a 403
		// means the grant lacks audit:event:write; the token itself is valid,
		// so the cache is retained and the epoch is unchanged (no Invalidate —
		// invalidate-on-403 would storm identical refetches). Runs regardless
		// of whether the body parses.
		_ = p.tokens.MarkBindingUnhealthy(SourceBinding{TenantID: fact.TenantID, SourceSystem: source})
		// Scope-body classification via the single shared classifier pair
		// (oauth.go tokenErrorCode/scopeRejectedCode): a drained body that
		// best-effort parses to invalid_scope/insufficient_scope is
		// ErrTokenScopeMissing — mutually exclusive with ErrTokenRejected.
		// Body-parse failure is non-fatal: the plain path applies. Never
		// ErrTokenEndpointScopeMissing (token-endpoint-only marker, C-C).
		if scopeRejectedCode(tokenErrorCode(body)) {
			return fmt.Errorf("%w: HTTP %d", ErrTokenScopeMissing, response.StatusCode)
		}
		return fmt.Errorf("%w: HTTP %d", ErrTokenRejected, response.StatusCode)
	}
```

**Lint math (verified against `.golangci.yml`):** funlen 50 lines/40 statements with `ignore-comments: true`; cyclop 10. `Publish` is 32 lines / ~17 statements today → ~40 lines / ~25 statements after (comments excluded from the line count); cyclomatic 6 → 8. File 298 → ~317 lines (< 500 cap). The 401 branch stays byte-identical.

### 2.2 Error shapes produced (the connector contract)

| events response | returned error | `errors.Is` matches | text |
|---|---|---|---|
| 403 + `{"error":"invalid_scope"}` | `%w`-wrap of `ErrTokenScopeMissing` | `ErrTokenScopeMissing` ✓, `ErrTokenRejected` ✗, `ErrTokenEndpointScopeMissing` ✗ | `audit governance: token scope missing: HTTP 403` |
| 403 + `{"error":"insufficient_scope"}` | identical | identical | identical |
| 403 + `{"error":"access_denied"}` or any other code | `%w`-wrap of `ErrTokenRejected` | `ErrTokenRejected` ✓, `ErrTokenScopeMissing` ✗ | `audit governance: token rejected: HTTP 403` |
| 403 + empty/unparseable body | `%w`-wrap of `ErrTokenRejected` (non-fatal parse) | `ErrTokenRejected` ✓ | `audit governance: token rejected: HTTP 403` |

The drain loop then behaves as (no service change): scope-body → `classifyScopeMissing` fires `ObserveAuditScopeMissing(strings.TrimSpace(fact.Source))` once and returns `errAuditTenantUnbound` → `publicationOutcome` settles `pending`/`audit.binding_unavailable`, `next = now.Add(retryDelay)` (exponential, `RetryBase` doubling, 1h cap — callback_worker.go:173-181), never `dead_letter` even at the MaxAttempts edge; non-scope body → ordinary failure ladder (`pending`/`audit.publication_failed` → `dead_letter` at MaxAttempts), exactly the pre-existing contract for a genuinely rejected request.

---

## 3. API changes

| Surface | Change |
|---|---|
| `TokenSource` interface (types.go:82-89) | **None.** Five methods, pinned once by the merged plan; `BindingUnhealthy`/`MarkBindingUnhealthy` remain documented no-op stubs on `OAuthTokenSource` (oauth.go:142-150). |
| `Config`, `OAuthConfig`, `SourceBinding`, error sentinels | **None.** `ErrTokenScopeMissing`/`ErrTokenRejected`/`ErrTokenEndpointScopeMissing` already exist (types.go:23-33). |
| `Publisher.Publish` signature | **None.** One new status branch; signature, return type, and wrapping style unchanged. |
| `service.AuditPublisher`, `tenantBoundAuditPublisher`, `publicationOutcome`, `classifyScopeMissing`, metrics interface | **None** (REQ-2.3). |
| Wire-format / error-text | Events-403 error text changes from `audit governance: HTTP 403` to the classified forms above. The only consumers are `classifyScopeMissing` via `errors.Is` (callback_audit.go:200) and `authorization`'s C-B propagation (publisher.go:135) — no text matching anywhere. |
| Config keys, migrations, protobuf, composition root, metrics labels | **None** (REQ-4.4). |

---

## 4. Compatibility constraints (no-regression pins)

1. **REQ-4.1 — 401 branch byte-unchanged** (publisher.go:98-113): status-driven `Invalidate`, scope-body classification, `ErrTokenRejected` fallback all preserved. The 403 branch is inserted *after* it, so 401 handling is untouched.
2. **REQ-4.2 — token-endpoint paths unchanged**: `fetchToken` 400-gate and scope-claim classification (oauth.go:215-262), `OnScopeRejected` hook, and the `ErrTokenEndpointScopeMissing` marker sites stay as-is. Token-endpoint 401/403 remain `ErrTokenUnavailable` (FM-4 boundary).
3. **REQ-4.3 — `validateReceipt` untouched** (:246-289): 202-only acceptance, conflict-as-idempotent-success, bounded receipt decode. Only 403s are diverted from it; every other non-202 status keeps the generic classification.
4. **REQ-4.4 — no signature/config/migration/proto changes.** `staticTokenSource` (callback_integration_test.go) already implements the five-method interface, so `make check-integration-compile` stays green.
5. **REQ-4.5 — engineering gates**: funlen/cyclop/file-size math in §2.1; connector imports remain stdlib + `x/sync` + `internal/domain`.
6. **Service behavior is a pin, not a change**: `classifyScopeMissing`'s A3 skip (marker → counter silent) and counter (marker-free → exactly one per failed attempt) and `publicationOutcome`'s branch order are the existing contracts the new branch must satisfy — the tests in §7 lock them.

---

## 5. Failure modes

| # | Mode | Behavior | Analysis |
|---|---|---|---|
| FM-1 | 403 body drain failure (read error / >64 KiB) | Non-fatal: `body, _ :=` ignores the error; `io.LimitReader` bounds at `maxReceiptBytes`; `tokenErrorCode("")` → `ErrTokenRejected` | Mirrors the 401 branch's contract (REQ-1.1/1.3). Bounded drain can never OOM or block the publish path. |
| FM-2 | 403 + scope body misclassified as `ErrTokenRejected` (e.g., truncated body) | `pending`/`audit.publication_failed` → `dead_letter` at MaxAttempts | Accepted tradeoff, identical to the 401 path: classification is best-effort over a bounded drain. No alert regression vs. today (today every 403 is generic-failure). |
| FM-3 | `MarkBindingUnhealthy` on nil `tokens` | Impossible: `NewPublisher` rejects nil `tokens` (publisher.go:47), and `Publish` already calls `p.tokens.Invalidate` unconditionally on 401 | Same guard covers the 403 branch. |
| FM-4 | `MarkBindingUnhealthy` no-op stub | The call is behaviorally inert today (oauth.go:149-150); the recording-decorator test still proves the call site (REQ-1.5) | Deliberate: cooldown predicate semantics are the sibling campaign's scope (out of scope, §8). Future implementer must mirror `Invalidate`'s unbound/mismatch no-op and mutex-safety (the drain fan-out can publish the same tenant concurrently through the bulkhead). |
| FM-5 | 403 storm (persistent misconfiguration) | Each claimed attempt = one HTTP call + one `ObserveAuditScopeMissing`; row parks `pending`/`audit.binding_unavailable` on the backoff ladder; cache retained so no refetch storm; row never dead-letters | This is the target behavior (REQ-3). Alert fires once per claimed attempt — bounded by the backoff ladder, not by queue exhaustion. |
| FM-6 | Pre-deploy rows already `dead_letter` | Unchanged; the fix affects only new attempts | Operators re-drive via the existing manual replay path (`RequeuePublication`); no backfill migration. |
| FM-7 | 403 with valid token, later scope fix | `AccessToken` returns the retained cached token; next claim re-probes and succeeds once the grant is fixed | Self-healing: no cache eviction means the fixed grant works on the next backoff tick without a refetch. |

---

## 6. Migration steps

**No data migration, no config change, no rollout sequencing** — the branch is additive and fail-safe:

1. **Code:** add the §2.1 branch to `Publisher.Publish` (publisher.go). Single-file edit.
2. **Tests (same change set):**
   - T-10.1.1 connector contract test — `internal/connector/auditgovernance/publisher_test.go` (or `token_invalidation_test.go`, next to the 401 twin).
   - T-10.1.2 service pin — `internal/service/callback_audit_test.go`.
   - T-10.1.3 drain-loop integration — `internal/service/callback_integration_test.go` (integration tag).
3. **Gate:** `make test && make lint && make lint-strict && make check-file-sizes && make check-integration-compile && make build`, then `make test-integration` for T-10.1.3 (Postgres via testcontainers).
4. **Deploy:** routine. After rollout, any *new* 403-scope condition parks at `audit.binding_unavailable` instead of dead-lettering; already-dead-lettered rows stay dead-lettered until an operator replays them (existing manual replay path). No backfill, no rollback hazard (reverting the commit restores the generic classification exactly).

---

## 7. Testable acceptance mapping

### T-10.1.1 — Connector contract (unit, no Docker; `internal/connector/auditgovernance/publisher_test.go`)

Fixture (existing pattern, `TestEvents401WithScopeBodyIsScopeMissingAlertedOnce`, token_invalidation_test.go:525-603): `httptest` server — `/token` → 200 with a counted `tokenHandler`, `/api/v1/events` → 202 for the prime, then configurable status/body; **real `OAuthTokenSource`** via `auditBinding` (publisher_test.go:266-272) wrapped in a new **recording decorator** (`recordingTokenSource`, same test file) + real `Publisher`. The decorator embeds the real `*OAuthTokenSource` as an anonymous field (promoting `AccessToken`/`HasBinding`/`BindingUnhealthy` unchanged) and overrides only the two call sites under test: `MarkBindingUnhealthy` appends the `SourceBinding` to a `markUnhealthy []SourceBinding` slice **then delegates to the real no-op**; `Invalidate` appends to `invalidations` then delegates. This disambiguates the fixture: with a bare real source the "exactly once" assertion is unobservable (`OAuthTokenSource.MarkBindingUnhealthy` is a documented no-op, oauth.go:149-150), while a full `stubTokenSource`-style replacement would orphan the cache-retention assertions that read `bindings[tenantID]` internals. The test therefore keeps **both** handles: the decorator for the call-site assertions, the embedded real source for the cache/epoch assertions. Prime with one successful publish first (primed cache, epoch 0) so cache-retention assertions are non-trivial. Fixture guard (FM-3): `NewPublisher(config, nil, nil)` must return `ErrInvalidConfig` (publisher.go:47) — the nil-tokens rejection the 403 branch's `MarkBindingUnhealthy` relies on has no existing test.

| events response | assertion (per row, table-driven; each row runs on a **fresh** fixture: server + source + decorator + prime, so side-effect counts are per-row) |
|---|---|
| `403` + `{"error":"invalid_scope"}` | `errors.Is(err, ErrTokenScopeMissing)` ∧ `!errors.Is(err, ErrTokenRejected)` (REQ-1.2) |
| `403` + `{"error":"insufficient_scope"}` | identical (REQ-1.2) |
| `403` + `{"error":"access_denied"}` | `errors.Is(err, ErrTokenRejected)` ∧ `!errors.Is(err, ErrTokenScopeMissing)` (REQ-1.3) |
| `403` + empty body | `ErrTokenRejected` — non-fatal parse, EOF leg (REQ-1.3) |
| `403` + non-JSON body (`text/plain` `Forbidden`, the realistic HTML/text error page) | `ErrTokenRejected` — non-empty unparseable-body leg (REQ-1.3) |
| `403` + **oversized scope body** (>64 KiB): `{"error":"invalid_scope","padding":"` + 64 KiB of `x` + `"}` — valid JSON only when read unbounded | `errors.Is(err, ErrTokenRejected)` ∧ `!errors.Is(err, ErrTokenScopeMissing)` (REQ-1.1, FM-1/FM-2) |

All six rows additionally assert the error text contains `HTTP 403` (REQ-1.7: never the generic `audit governance: HTTP %d` text).

**Oversized-row construction (discriminating by design):** `io.LimitReader(response.Body, maxReceiptBytes)` caps the drain at exactly 64 KiB; the 33-byte JSON prefix plus 64 KiB of padding guarantees `io.ReadAll` stops mid-padding-string, so `json.Unmarshal` fails, `tokenErrorCode` returns `""`, and the plain path applies — **but** an unbounded drain would parse the full body as `invalid_scope` and return `ErrTokenScopeMissing`, flipping the assertion. The row therefore fails if anyone removes or raises the bound: it pins FM-1 (bounded drain — `Publish` returns a classified error promptly, no OOM, no hang) and FM-2 (truncated scope body → `ErrTokenRejected`) in one assertion. The handler writes the full ~65.5 KiB body in a single `w.Write` and ignores the write error (the client stops reading at the cap; whether the tail lands in the kernel send buffer or hits a closed connection, the test is deterministic).

Side-effect assertions (after the 403, via the recording decorator per REQ-1.5; decisive on both a scope-body row and a non-scope-body row — REQ-1.5 requires the call regardless of parse):
- `decorator.markUnhealthy` has **exactly one** entry: `SourceBinding{TenantID: fact.TenantID, SourceSystem: mustSource(t, tenantID)}` (`mustSource`, token_invalidation_test.go:261-266) — and `decorator.invalidations` is **empty** (REQ-1.5/1.6). These are observable call-site pins; delegation means the real no-op still runs underneath.
- cache retained on the embedded real source: `bindings[tenantID].cachedToken()` still returns the primed token and `epoch` is unchanged — which simultaneously proves the real `MarkBindingUnhealthy` stub is inert (FM-4);
- `/token` hit exactly **once** total (prime + 403 — no refetch storm);
- counting `OnScopeRejected` hook records **zero** calls (token-endpoint seam only).
- No cooldown-state assertion (stub is a documented no-op, §8).

### T-10.1.2 — Service pin (unit, no Docker; `internal/service/callback_audit_test.go`)

Fixture: `settlingPublicationRepo` + `scopeMissingPublisher` (callback_audit_test.go:108-115) returning the connector's exact wrapped shape `fmt.Errorf("%w: HTTP %d", auditgovernance.ErrTokenScopeMissing, http.StatusForbidden)` — the new pin (existing test at :483 uses the bare sentinel; keep it, add the wrapped variant). Run `finishPublicationAttempt` at `Attempt: 0` and `Attempt: 7` (nextAttempt 8 == MaxAttempts, the dead-letter edge, `MaxAttempts: 8`):

- `FinishPublication` settled `"pending"` / `"audit.binding_unavailable"`; persisted attempt == `item.Attempt + 1`;
- `metrics.scopeMissing` gains exactly one `"aero-id"` entry per attempt (counter fires — the `%w` wrap must not match `ErrTokenEndpointScopeMissing`, so the A3 skip cannot trigger);
- `metrics.outcomes` records `"pending"` — never `"dead_letter"`, including at the MaxAttempts edge (REQ-2.2, REQ-3).

This is the fast no-Docker detector for regressions in the `errors.Is` traversal, marker re-introduction, or `publicationOutcome` branch order.

### T-10.1.3 — Drain-loop integration (integration tag; `internal/service/callback_integration_test.go`)

Fixture (harness of `TestDrainExhaustsAttemptsIntoDeadLetter`, :207-296): `testpg` real Postgres + migrations, real `AuditLedgerRepository` as `AuditPublicationRepository`, `NewCallbackService` with `MaxAttempts: 3`, `metricSpy`, one enabled subscription, one fact via `ledger.Append` (`Source: "aero-id"`, `TenantID: "tenant-a"`). Wire the **real** `auditgovernance.Publisher` + real `OAuthTokenSource` (binding inlined per `auditBinding`) against an `httptest` server: `/token` → 200 (counted), `/api/v1/events` → always `403` + `{"error":"invalid_scope"}`. `svc.SetAuditPublishers(publisher)`.

Procedure: `MaxAttempts + 2` = 5 drain cycles, backdating `next_attempt_at` between cycles (existing pattern).

Assertions after every cycle and at the end:
- row `status == "pending"`, `error_code == "audit.binding_unavailable"`, `attempt == <cycles run>` (increments **past** MaxAttempts — parked, never `dead_letter`);
- `metrics.scopeMissing` length == cycles run, every label `"aero-id"` (exactly once per claimed attempt);
- `metrics.outcomes` all `"pending"`;
- `/token` hit exactly once across the whole loop (cache retained end-to-end);
- a final direct `ClaimPublication` re-claims the row (re-probe path alive); then flip `/api/v1/events` to `202` (grant fixed), backdate `next_attempt_at`, run one more drain cycle, and assert the row settles `status == "completed"` / `error_code == ""` while `/token` hits stay exactly **1** — self-healing uses the retained cached token, no refetch (FM-7).

### Requirements → test map

| Requirement | Pinned by |
|---|---|
| REQ-1.1 bounded drain | T-10.1.1 oversized-body row (empty-body row pins only the EOF leg) |
| REQ-1.2 scope-body → `ErrTokenScopeMissing` | T-10.1.1 rows 1-2 |
| REQ-1.3 non-scope → `ErrTokenRejected`, mutual exclusivity | T-10.1.1 rows 3-6 |
| REQ-1.4 no `ErrTokenEndpointScopeMissing` marker | T-10.1.2 (counter fires) |
| REQ-1.5 `MarkBindingUnhealthy` regardless of body | T-10.1.1 recording-decorator assertions |
| REQ-1.6 no `Invalidate`, cache retained, no refetch storm | T-10.1.1 (epoch/cachedToken/token-hit) + T-10.1.3 (one token hit) |
| REQ-1.7 no `validateReceipt` fall-through | T-10.1.1 (classified errors, never `audit governance: HTTP 403` text) |
| REQ-2.1 routing + counter | T-10.1.2 |
| REQ-2.2 never dead-letters at edge | T-10.1.2 (Attempt 7) + T-10.1.3 (5 cycles > MaxAttempts 3) |
| REQ-2.3 no service production change | diff review + T-10.1.2 passing unchanged service code |
| REQ-3 drain-loop terminal behavior | T-10.1.3 |
| REQ-4.1-4.5 no-regression | existing suites green + lint/file-size/integration-compile gates |

### Failure-mode → pinning assertion map (audit of §5; every FM row must be pinned)

| FM | Pinned by |
|---|---|
| FM-1 (drain failure / >64 KiB; non-fatal, bounded) | T-10.1.1 oversized-body row: `Publish` returns classified `ErrTokenRejected` — no OOM/hang, and the 64 KiB cap is the *discriminating* assertion (an unbounded drain would return `ErrTokenScopeMissing` and fail the row) |
| FM-2 (truncated scope body misclassified as `ErrTokenRejected`) | classification half: T-10.1.1 oversized-body row; ladder half (`pending`/`audit.publication_failed` → `dead_letter` at MaxAttempts): existing `TestDrainExhaustsAttemptsIntoDeadLetter` (callback_integration_test.go:207-296), unchanged pre-existing `ErrTokenRejected` contract |
| FM-3 (nil `tokens` impossible) | T-10.1.1 fixture guard: `NewPublisher(config, nil, nil)` → `ErrInvalidConfig` (publisher.go:47) — no existing test covered this guard |
| FM-4 (`MarkBindingUnhealthy` no-op stub) | T-10.1.1 recording decorator: exactly one call-site entry, zero `Invalidate`; delegation to the real no-op plus unchanged `epoch`/`cachedToken()` on the embedded real source proves the stub is inert |
| FM-5 (403 storm) | T-10.1.3: 5 cycles > MaxAttempts 3 — parked `pending`/`audit.binding_unavailable`, `scopeMissing` once per attempt, outcomes all `"pending"`, `/token` hit exactly once; T-10.1.1 per-row single 403 with one token hit |
| FM-6 (pre-deploy `dead_letter` rows) | No-change pin via the existing replay suite: `TestRequeuedPublicationDrainsExactlyOnce` (callback_integration_test.go:874, T-4.1(b)) + `TestDrainExhaustsAttemptsIntoDeadLetter`'s never-re-claimed ending — pre-fix rows stay dead until manual `RequeuePublication` |
| FM-7 (self-heal after grant fix) | T-10.1.3 final step: 202 flip → row settles `"completed"` with `error_code == ""`, `/token` hits stay 1 (retained cache, no refetch) |

---

## 8. Out of scope (unchanged from the requirements)

- `BindingUnhealthy` cooldown predicate semantics (sibling campaign; stubs stay no-ops; interface untouched).
- Token-endpoint 401/403 (stays `ErrTokenUnavailable`); 403 retry/backoff policy (existing `errAuditTenantUnbound` backoff applies unchanged).
- Dead-tree `internal/audit` quarantine (T-5) and `assertAuditWiring` extension (T-10.2) — the analysis's other directions.
- No config, migration, proto, metrics-interface, or composition-root changes.

## 9. Verification gate

```bash
make test                       # T-10.1.1 + T-10.1.2 (unit, no Docker)
make lint && make lint-strict   # funlen/cyclop on the new branch (math in §2.1)
make check-file-sizes           # publisher.go < 500 lines
make check-integration-compile  # 5-method TokenSource fakes stay complete
make test-integration           # T-10.1.3 (Postgres via testcontainers)
make build
```

Full AGENTS.md done gate must stay green; no existing test changes behavior (REQ-4.1-4.4).
