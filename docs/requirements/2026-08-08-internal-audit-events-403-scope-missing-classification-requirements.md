# Requirements — internal/audit: implement the events-endpoint 403 → `ErrTokenScopeMissing` classification and alert (REQ-5 'events 403' claim is code-unverified)

> **Module:** `internal/audit` (analysis module name; the work lands in the live runtime: `internal/connector/auditgovernance` + `internal/service` — the dead `internal/audit` tree is unwired per AGENTS.md and is not touched)
> **Date:** 2026-08-08
> **Direction (selected, value 8 / risk 7 / effort 2 / confidence 9):** "Implement the missing events-endpoint 403 -> ErrTokenScopeMissing classification and alert (REQ-5 'events 403' claim is code-unverified)"
> **Source analysis:** `docs/auto/analyses/internal-audit-9b2d0397.json` (direction 2)
> **Design authority:** `docs/plans/2026-08-07-auditgovernance-tokensource-merged-interface.md` §3 events-403 row and §4 error-routing composition; `docs/plans/2026-08-07-internal-outbox-tokensource-invalidate-on-401-design.md` §2 `StatusForbidden` branch
> **Method:** every cited file/symbol re-verified against the working tree; line numbers below are from the current tree.

---

## 1. Problem statement (verified against the working tree)

The merged TokenSource plan routes an events-endpoint **403** to the scope-missing classification: drain the bounded body, `MarkBindingUnhealthy(binding)` with the token cache retained, return `ErrTokenScopeMissing: HTTP 403`, and let the service settle the publication row `pending`/`audit.binding_unavailable` with `ObserveAuditScopeMissing` firing once per network-403 — **never** `dead_letter`. The tree implements every other cell of that routing table **except the events-403 branch**:

**Current behavior chain (all verified):**

1. `internal/connector/auditgovernance/publisher.go` `Publish` special-cases only `response.StatusCode == http.StatusUnauthorized` (:100-115: bounded body drain, status-driven `Invalidate`, scope-body classification, `ErrTokenRejected`/`ErrTokenScopeMissing`). **A 403 falls through to `return validateReceipt(response, fact)` (:116).**
2. `validateReceipt` (:246-250) rejects every non-202 with the generic error `fmt.Errorf("audit governance: HTTP %d", response.StatusCode)` — no scope classification, no binding side effect, no alert.
3. `internal/service/callback_audit.go` `publicationOutcome` (:144-155) treats that generic error as an ordinary publisher failure: `pending`/`audit.publication_failed` until `attempt >= maxAttempts`, then `dead_letter`/`audit.publication_failed` (:151-153). **Zero `ObserveAuditScopeMissing` observations.**
4. `internal/connector/auditgovernance/oauth.go:145-150`: `BindingUnhealthy`/`MarkBindingUnhealthy` are no-op stubs with the explicit deferral comment "the 403 path keeps validateReceipt's generic classification".

**Consequence:** a permanent tenant misconfiguration (client-credentials grant that succeeds but lacks `audit:event:write` on the resource, or the wrong resource granted) dead-letters the tenant's whole audit queue after `MaxAttempts` retries — the alertable signal is a generic `audit.publication_failed` DLQ entry, not the scope-missing alert, and the row never parks at `audit.binding_unavailable` for a targeted operator fix. The REQ-5 contract (merged plan §4: "`ErrTokenScopeMissing` (events-403 or token-endpoint scope rejection) → `errAuditTenantUnbound` → `pending`/`audit.binding_unavailable`, **never dead-letters**"; tenantid design §2 `StatusForbidden` branch) is comment/doc-vs-code mismatch: the service-side routing for the classification already exists (`classifyScopeMissing`, callback_audit.go:186-206) but the connector never produces the error on the 403 path.

**No service-code change is required** — the fix is the connector 403 branch; the service tests pin the existing routing against the new error shape (see §4, REQ-2).

---

## 2. Verification of the source analysis

| # | Cited claim | Verified? | Finding (current tree) |
|---|---|---|---|
| E1 | publisher.go:100-119: only 401 branch; 403 → `validateReceipt` | ✅ | `Publish` 401 branch at :100-115; `return validateReceipt(response, fact)` at :116. No `StatusForbidden` branch anywhere in the package (grep: zero hits). |
| E2 | `validateReceipt` generic non-202 error (cited :121-128) | ✅ (line drift) | Function is at :246; generic error `audit governance: HTTP %d` at :248-250. Cited lines are stale but substance verified. |
| E3 | oauth.go `BindingUnhealthy`/`MarkBindingUnhealthy` no-op stubs deferring "the 403 path keeps validateReceipt's generic classification" | ✅ | oauth.go:145-150, deferral comment verbatim at :145-148. Interface members at types.go:87-88. |
| E4 | callback_audit.go:157-175: only `ErrTokenScopeMissing` routes to `errAuditTenantUnbound` + `ObserveAuditScopeMissing` (cited range) | ✅ (line drift) | `classifyScopeMissing` at :195-206 (comment :186-194); `errAuditTenantUnbound` at :15; `publicationOutcome` at :144-155, dead_letter at :151-153. Cited lines are stale but substance verified. |
| E5 | `publicationOutcome` dead-letters at MaxAttempts | ✅ | callback_audit.go:151-153. Unbound branch (:148-150) precedes the cap check — the parking behavior the 403 classification needs already exists. |
| E6 | audit_standalone_sites_integration_test.go lacks a 403-scope fixture | ✅ | File (215 lines) has 4 tests, all standalone-site fact harnesses; zero 403/publisher/drain references. Grep for `StatusForbidden`/`HTTP 403` across all of `internal/service/*_test.go` and `internal/connector/auditgovernance/*_test.go`: zero hits — the claim holds repo-wide; the drain-loop integration harness lives in `internal/service/callback_integration_test.go` (see §4 T-10.1.3 for placement). |
| E7 | REQ-5 'events 403' is comment-vs-code mismatch | ✅ | callback_audit.go:187-189 comment lists "events 401-with-scope-body or a token-endpoint scope rejection" (403 omitted — predates the branch); the events-403 contract lives in the merged plan §3/§4 + tenantid design §2, which the code does not implement. |

All six evidence pillars of the direction hold. The two line-number drifts (E2, E4) are stale citations of functions that moved; neither changes the substance.

---

## 3. Requirements

### REQ-1 — Connector: events-endpoint 403 classification (`internal/connector/auditgovernance/publisher.go`, `Publish`)

Add a `response.StatusCode == http.StatusForbidden` branch between the 401 branch (:100-115) and `return validateReceipt(response, fact)` (:116), mirroring the 401 branch's structure:

- **REQ-1.1** Bounded body drain: `defer response.Body.Close()`; `body, _ := io.ReadAll(io.LimitReader(response.Body, maxReceiptBytes))` — identical to the 401 branch (:101-102). The body is only for classification; drain failure is non-fatal.
- **REQ-1.2** Scope-body classification via the **single shared classifier pair** `tokenErrorCode`/`scopeRejectedCode` (oauth.go:317-331 — one signature set per merged plan §3 pin): a body that best-effort parses to `{"error":"invalid_scope"}` or `{"error":"insufficient_scope"}` yields `fmt.Errorf("%w: HTTP %d", ErrTokenScopeMissing, http.StatusForbidden)`. Body-parse failure is non-fatal: the plain path applies.
- **REQ-1.3** Non-scope 403 (empty, unparseable, or any other `error` code) yields `fmt.Errorf("%w: HTTP %d", ErrTokenRejected, http.StatusForbidden)`. Mutual exclusivity both directions: scope-body 403 must not match `ErrTokenRejected`; non-scope 403 must not match `ErrTokenScopeMissing` (mirrors the 401 branch's contract, publisher.go:111-114).
- **REQ-1.4** The branch must never emit `ErrTokenEndpointScopeMissing` (types.go:66-75: marker is token-endpoint-only; the events-endpoint site must stay marker-free so the drain counter fires — C-C exactly-one-increment contract).
- **REQ-1.5** Status-driven `_ = p.tokens.MarkBindingUnhealthy(SourceBinding{TenantID: fact.TenantID, SourceSystem: source})`, running regardless of whether the body parses (mirrors the 401 branch's status-driven `Invalidate`, :105-108). This is the merged plan §3 events-403 row's side effect; today's `OAuthTokenSource` implementation is a documented no-op stub (oauth.go:149-150), so the call is behaviorally inert. **Cooldown predicate semantics (consulting `BindingUnhealthy` in the publish flow) are out of scope** (§5).
- **REQ-1.6** The branch must **not** call `Invalidate` (merged plan §2.3: a 403 means the grant lacks `audit:event:write`; the token itself is valid — invalidate-on-403 would storm identical refetches). Cache retained; epoch unchanged.
- **REQ-1.7** A 403 never falls through to `validateReceipt`'s generic `audit governance: HTTP %d` classification (publisher.go:248-250) — that generic bucket is what drives `audit.publication_failed` → `dead_letter` with no alerting.

### REQ-2 — Service: routing and alert already exist; pin them to the new error shape (no production change)

The 403 branch's output — `ErrTokenScopeMissing` wrapped only with the HTTP status (no `ErrTokenEndpointScopeMissing` marker) — must traverse the existing service path unchanged:

- **REQ-2.1** `classifyScopeMissing` (callback_audit.go:195-206) routes it via `errors.Is(err, auditgovernance.ErrTokenScopeMissing)` to `errAuditTenantUnbound`, firing `ObserveAuditScopeMissing(strings.TrimSpace(fact.Source))` **exactly once per failed publish attempt** (the bare, marker-free error takes the counter branch at :201-203, not the A3 skip).
- **REQ-2.2** `publicationOutcome` (:144-155) settles the row `pending`/`audit.binding_unavailable` with `next = now.Add(retryDelay)`; because the unbound branch (:148-150) precedes the `attempt >= maxAttempts` check (:151-153), the row **never** dead-letters, even at the MaxAttempts edge, and re-probes on the backoff schedule.
- **REQ-2.3** No change to `service.AuditPublisher` (audit_ledger.go:24-26), `tenantBoundAuditPublisher` (callback_audit.go:21-22), or `publicationOutcome`.

### REQ-3 — Drain-loop terminal behavior

Repeated events-403-scope responses must never transition the publication row to `dead_letter`: every attempt settles `pending`/`audit.binding_unavailable`, `attempt` increments (and may exceed `MaxAttempts` — the row is parked, not terminated), the scope-missing counter increments once per claimed attempt, and the row remains re-claimable after its `next_attempt_at` (the automatic re-probe path).

### REQ-4 — No-regression invariants

- **REQ-4.1** The 401 branch (publisher.go:100-115: status-driven `Invalidate` + scope-body classification) is byte-unchanged.
- **REQ-4.2** Token-endpoint paths are unchanged: 400-gate and scope-claim classification in `fetchToken` (oauth.go:215-262), `OnScopeRejected` hook, and the `ErrTokenEndpointScopeMissing` marker sites stay as-is (token-endpoint 401/403 remain `ErrTokenUnavailable` — FM-4 boundary).
- **REQ-4.3** `validateReceipt` (publisher.go:246-289) is untouched (202-only, conflict-as-idempotent-success, REQ-5 202/receipt contract byte-unchanged).
- **REQ-4.4** No signature changes: the five-method `TokenSource` interface (types.go:82-89) is fixed exactly once per merged plan §2/§6 — this direction adds no interface edits, no new constructors, no new config keys, no migrations, no proto.
- **REQ-4.5** Engineering gates: `publisher.go` is 298 lines today; the branch adds ~15 → well under the 500-line cap; `Publish` stays ≤ 50 lines and cyclomatic ≤ 10. Connector imports remain stdlib + `golang.org/x/sync` + `internal/domain`.

---

## 4. Acceptance (supplied T-10.1, made testable)

### T-10.1.1 — Connector contract test (unit, deterministic fake server; `internal/connector/auditgovernance/publisher_test.go`)

**Fixture:** `httptest` server with `/token` → 200 (`{"access_token":...,"token_type":"Bearer","expires_in":300}`) and `/api/v1/events` → configurable status/body; real `OAuthTokenSource` (via the existing `auditBinding` helper, publisher_test.go:266-272) wrapped in a **recording decorator** `recordingTokenSource` (test type embedding the real `*OAuthTokenSource`; overrides only `MarkBindingUnhealthy`/`Invalidate` to record the `SourceBinding` then delegate to the real implementation) + real `Publisher` — the pattern of `TestEvents401WithScopeBodyIsScopeMissingAlertedOnce` (token_invalidation_test.go:525-603). The decorator is required for the call-site assertion to be observable: the real `OAuthTokenSource.MarkBindingUnhealthy` is a documented no-op (oauth.go:149-150), and a full replacement fake would orphan the cache-retention assertions that read the real source's `bindings[tenantID]` internals. Keep both handles: decorator for call-site counts, embedded real source for cache/epoch. Prime with one successful 202 publish first (primed cache, epoch 0). Fixture guard (FM-3): `NewPublisher(config, nil, nil)` → `ErrInvalidConfig`.

**Cases (table-driven over the events response; each row runs on a fresh fixture, so side-effect counts are per-row):**

| events response | required assertion |
|---|---|
| `403` + `{"error":"invalid_scope"}` | `errors.Is(err, ErrTokenScopeMissing)` **and** `!errors.Is(err, ErrTokenRejected)`; error text contains `HTTP 403` (REQ-1.2/1.7) |
| `403` + `{"error":"insufficient_scope"}` | identical (REQ-1.2) |
| `403` + `{"error":"access_denied"}` (non-scope) | `errors.Is(err, ErrTokenRejected)` **and** `!errors.Is(err, ErrTokenScopeMissing)` (REQ-1.3) |
| `403` + empty body | `ErrTokenRejected` — body-parse failure is non-fatal, plain path applies (REQ-1.3) |
| `403` + non-JSON body (`text/plain` `Forbidden`) | `ErrTokenRejected` — non-empty unparseable body (REQ-1.3) |
| `403` + **oversized scope body** (>64 KiB): `{"error":"invalid_scope","padding":"` + 64 KiB of `x` + `"}` — valid JSON only when read unbounded | `errors.Is(err, ErrTokenRejected)` **and** `!errors.Is(err, ErrTokenScopeMissing)` — the `io.LimitReader` cap truncates mid-string, `tokenErrorCode` returns `""`, plain path applies (REQ-1.1, FM-1/FM-2). The row is discriminating: an unbounded drain would parse `invalid_scope` and flip the assertion, so removing/raising the bound fails the test. Handler writes the full ~65.5 KiB body in one `w.Write`, ignoring the write error. |

**Side-effect assertions (REQ-1.5/1.6, C-C):** with a primed token (one successful 202 publish first, per the 401-test pattern):
- cache retained: `tokens.bindings[tenantID].cachedToken()` still returns the primed token and `epoch` is unchanged after the 403 (REQ-1.6);
- no refetch storm: the `/token` endpoint is hit exactly once total across prime + 403 (REQ-1.6);
- hook silence: a counting `OnScopeRejected` hook records zero calls for the events-endpoint 403 (C-C: the hook is the token-endpoint seam only);
- status-driven `MarkBindingUnhealthy`: the recording decorator sees **exactly one** `MarkBindingUnhealthy` call carrying `SourceBinding{TenantID: fact.TenantID, SourceSystem: mustSource(t, tenantID)}` (helper, token_invalidation_test.go:261-266) and **zero** `Invalidate` calls, on both a scope-body row and a non-scope-body row (REQ-1.5: the call runs regardless of whether the body parses). Delegation to the real no-op keeps the cache-retention assertions meaningful — they double as the pin that the real stub is inert (FM-4). No assertion on cooldown state — the stub is a documented no-op (out of scope, §5).

### T-10.1.2 — Service test (unit, no Docker; `internal/service/callback_audit_test.go`)

**Fixture:** `settlingPublicationRepo` + a publisher fake returning the connector's exact 403 classification `fmt.Errorf("%w: HTTP %d", auditgovernance.ErrTokenScopeMissing, http.StatusForbidden)` (pattern: `scopeMissingPublisher`, callback_audit_test.go:96-105; the wrapped shape is the new pin).

**Assertions, per `finishPublicationAttempt` — run at `Attempt: 0` and `Attempt: 7` (nextAttempt 8 == MaxAttempts, the dead-letter edge):**
- `FinishPublication` settled `"pending"` with code `"audit.binding_unavailable"`; persisted attempt = item.Attempt + 1 (REQ-2.2);
- `metrics.scopeMissing` gains **exactly one** entry per attempt with label `"aero-id"` (== `strings.TrimSpace(fact.Source)`) — the counter fires because the wrapped 403 error does **not** match `ErrTokenEndpointScopeMissing` (A3 skip must not trigger); (REQ-2.1)
- `metrics.outcomes` records `"pending"` — never `"dead_letter"`, including at the MaxAttempts edge (REQ-2.2, REQ-3).

This is the no-Docker fast detector: it fails if a regression drops the `errors.Is` traversal through the `%w` wrap, re-introduces the marker on the events-endpoint path, or reorders `publicationOutcome`'s branches.

### T-10.1.3 — Drain-loop integration test (integration tag; `internal/service/callback_integration_test.go`)

**Fixture:** the harness of `TestDrainExhaustsAttemptsIntoDeadLetter` (callback_integration_test.go:207-296): `testpg` real Postgres + migrations, real `AuditLedgerRepository` as `AuditPublicationRepository`, `NewCallbackService` with `MaxAttempts: 3`, `metricSpy`, one enabled subscription, one fact via `ledger.Append` (`Source: "aero-id"`, `TenantID: "tenant-a"`). Wire the **real** `auditgovernance.Publisher` + real `OAuthTokenSource` (binding config inlined per `auditBinding`) against an `httptest` server: `/token` → 200 (count hits); `/api/v1/events` → `403` + `{"error":"invalid_scope"}` on every post. `svc.SetAuditPublishers(publisher)`; `svc.metrics = metrics`.

**Procedure:** drive `MaxAttempts + 2` = 5 drain cycles, backdating `next_attempt_at` between cycles (the existing test's pattern).

**Assertions after every cycle and at the end (REQ-3):**
- row `status == "pending"`, `error_code == "audit.binding_unavailable"`, `attempt == <cycles run>` (increments **past** MaxAttempts — parked, never `dead_letter`);
- `metrics.scopeMissing` length == cycles run, every label `"aero-id"` (exactly once per claimed attempt);
- `metrics.outcomes` all `"pending"`;
- `/token` hit exactly once across the whole loop (cache retained end-to-end — no invalidate/refetch storm, REQ-1.6);
- a final direct `ClaimPublication` re-claims the row (the re-probe path is alive; the row is parked, not dead); then flip `/api/v1/events` to `202` (grant fixed), backdate `next_attempt_at`, run one more drain cycle, and assert the row settles `status == "completed"` / `error_code == ""` while `/token` hits stay exactly 1 (FM-7: self-healing uses the retained cached token, no refetch).

---

## 5. Out of scope (deliberately, per the direction)

- **Cooldown predicate semantics** — `BindingUnhealthy` being consulted by `publishAuditFact`/`tenantBoundAuditPublisher` to skip publish attempts (merged plan §4 delta) is the sibling campaign's remaining work; the interface stays untouched and the no-op stubs stay no-ops.
- Token-endpoint 401/403 handling (remains `ErrTokenUnavailable`, FM-4 boundary) and any 403 retry/backoff-policy change (the existing `errAuditTenantUnbound` backoff applies unchanged).
- The analysis's other directions: dead-tree `internal/audit` quarantine gate (T-5) and `assertAuditWiring` extension to operation/sync/eventIngest (T-10.2).
- No config, migration, proto, metrics-interface, or composition-root changes.

---

## 6. Verification gate

```bash
make test                       # T-10.1.1 + T-10.1.2 (unit, no Docker)
make lint && make lint-strict   # funlen/cyclop on the new branch
make check-file-sizes           # publisher.go stays < 500 lines
make check-integration-compile  # staticTokenSource already implements the 5-method interface; must stay green
make test-integration           # T-10.1.3 (Postgres via testcontainers)
```

The full AGENTS.md done gate must stay green; no existing test may change behavior (REQ-4.1-4.4).
