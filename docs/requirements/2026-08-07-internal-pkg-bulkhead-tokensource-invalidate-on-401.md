# Requirements Specification — internal/pkg/bulkhead: TokenSource 401 invalidation with epoch single-flight backfill and scope-missing alerting (B2 item 3)

> Effective code scope is `internal/connector/auditgovernance` (`types.go`, `oauth.go`, `publisher.go`,
> `publisher_test.go`) plus the drain's publisher-error routing slice in `internal/service/callback_audit.go`
> and the `ControlPlaneMetrics` alert surface (`internal/service/control_plane_metrics.go`,
> `internal/pkg/metrics/control_plane.go`). The token source serves the audit-publication outbox drain
> (`callbackService.drainPublications`), whose fan-out is bounded by `internal/pkg/bulkhead` — the campaign
> label. This is the `internal/pkg/bulkhead` campaign spec for the direction that
> `2026-08-07-internal-outbox-tokensource-invalidate-on-401.md` covered under the `internal/outbox` label;
> both are subsumed by the interface resolution in
> `docs/plans/2026-08-07-auditgovernance-tokensource-merged-interface.md` and the hook-safety companion
> `docs/plans/2026-08-07-auditgovernance-hook-hazard-resolution.md`. This spec adopts the merged interface
> verbatim and adds the direction's own delta: a 401 whose body evidences a scope-missing condition fires the
> alert surface while the publication stays pending.

| | |
|---|---|
| Module | `internal/pkg/bulkhead` (effective scope `internal/connector/auditgovernance` + drain error routing in `internal/service/callback_audit.go`) |
| Direction | "TokenSource 401 invalidation with epoch single-flight backfill and scope-missing alerting (B2 item 3)" (analysis direction #2) |
| Source analysis | `docs/auto/analyses/internal-pkg-bulkhead-942e0fa7.json` |
| Date | 2026-08-07 |
| Scope | (1) `TokenSource.Invalidate(SourceBinding) error` per the merged interface + per-tenant token epoch; (2) epoch-fenced singleflight backfill (stale in-flight results discarded, bounded re-entry); (3) events-endpoint 401 → `Invalidate` + distinguishable `ErrTokenRejected`, with 401-and-scope-missing-body → `ErrTokenScopeMissing` (alertable); (4) scope-missing alerting: token-endpoint `invalid_scope`/`insufficient_scope` classification, token-response scope-claim validation, `OnScopeRejected` hook (token-endpoint only), service-side `ObserveAuditScopeMissing` counter for events-endpoint classifications; (5) drain routing so a scope-missing publication stays `pending` (never completed, never swallowed, never prematurely dead-lettered) and the refetched token serves the retried attempt within the same worker lease window; (6) tests for all three supplied acceptance checks. Nothing else. |

## 1. Problem statement

`tenantTokenSource` caches a client-credentials bearer token until `refreshAt` and has no invalidation path.
`cachedToken` (oauth.go:147-151) serves `s.token` while `now().Before(s.refreshAt)`; `cacheToken`
(oauth.go:210-217) caps the lifetime at `min(expires_in, 86400s)` minus `refreshSkew` — a rejected token can
therefore poison every publication for a tenant for up to 24 h. When the governance events endpoint rejects a
publish with 401, `Publisher.validateReceipt` (publisher.go:220-245) collapses it into an opaque
`audit governance: HTTP 401` error (publisher.go:224, body discarded at :223). The drain re-queues the
publication (`publicationOutcome`, callback_audit.go:142-153: every non-unbound error → `pending`/
`audit.publication_failed`, `dead_letter` at `attempt >= maxAttempts`), and the next attempt reuses the same
rejected cached token until the refresh window — burning `MaxAttempts` (default 8, callback.go:80) into
`dead_letter` with no operator signal.

Four concrete gaps, all verified against the working tree (§2):

1. **No `Invalidate`/epoch mechanism.** `TokenSource` (types.go:58-61) exposes only `AccessToken`/`HasBinding`;
   `grep Invalidate internal/` → zero matches. `s.fetches` (singleflight.Group, oauth.go:40) dedupes via
   `DoChan("client", ...)` (oauth.go:126) with a constant key and is used only for expiry-driven refresh
   (the flight closure re-checks `cachedToken`, then `fetchToken`). A pre-invalidation in-flight fetch can
   repopulate the cache after an invalidation via `fetchToken` → `cacheToken` (oauth.go:171) — the rejected
   token re-enters service. There is no epoch to fence stale singleflight results.
2. **401 is indistinguishable from any other failure.** `validateReceipt` accepts only 202; every other status
   becomes `fmt.Errorf("audit governance: HTTP %d", ...)` with the body discarded. Nothing signals the token
   source, so the stale token keeps being served until expiry.
3. **Scope-missing conditions are silent and unchecked.** Token responses are never checked for the required
   scope: `tokenRequest` always sends `scope=audit:event:write` (oauth.go:176), but `tokenResponse`
   (oauth.go:187-191) has no `scope` field and `decodeTokenResponse` (oauth.go:193-208) never validates a
   scope claim; a token-endpoint `invalid_scope` error response is mapped to the same `ErrTokenUnavailable:
   HTTP %d` as any transient failure (`fetchToken`, oauth.go:163-168); an events-endpoint scope rejection is
   indistinguishable from the generic error. No scope-missing metric family or alert hook exists
   (`ControlPlaneMetrics`, control_plane_metrics.go:12-26, has no scope symbol; `grep ScopeMissing
   internal/` → zero matches).
4. **No runtime reaction to 401.** Binding coverage is validated at boot only
   (`validateAuditBindingCoverage`, internal/app/audit.go:146); nothing observes rejections at runtime.

Note on the direction's "noopControlPlaneMetrics is the production default" premise: **stale in the current
tree.** Production wires `RegistryControlPlaneMetrics` (internal/app/new.go:96 → `buildAuditStack`
internal/app/audit.go:34 → `SetControlPlaneMetrics` for both the ledger and the callback service,
app/audit.go:56-65; the F-1 fix landed). `noopControlPlaneMetrics` (callback.go:93) is only the constructor
fallback. The alerting gap itself is real: no scope-missing method exists on the interface, the noop, the
registry, or the render (`aero_control_audit_*` families, control_plane.go:186-232).

## 2. Evidence verification (all citations re-checked against the working tree on 2026-08-07)

The analysis's line ranges are from an older revision; every cited symbol is present. Corrected current lines:

| # | Cited evidence | Verification result |
|---|---|---|
| E1 | `oauth.go:40,97-101`: singleflight group, `accessToken` `DoChan` | **Symbol verified; lines corrected.** `fetches singleflight.Group` oauth.go:40; `accessToken` **oauth.go:122-145**, `DoChan("client", ...)` :126 (constant key), flight closure re-checks `cachedToken` :127-129; `fetchToken` :153-173 with exactly one caller (the flight closure, :130). |
| E2 | `oauth.go:117-120`: `cachedToken`, no `Invalidate` | **Verified.** `cachedToken` **oauth.go:147-151** — `("", false)` unless `token != "" && now().Before(refreshAt)`; the only drop path is expiry. `cacheToken` **oauth.go:210-217** — `lifetime = min(expires_in, 86400s)`, `refreshAt = now + lifetime − min(refreshSkew, lifetime/2)`; `fetchToken` → `cacheToken` at :171 caches unconditionally. `grep Invalidate internal/` → zero hits; no epoch field anywhere in the connector. |
| E3 | `oauth.go:176-181`: token request scope; `types.go:49` `Scope` | **Verified; scope claim check absent.** `tokenRequest` oauth.go:175-186 sends `"scope": {auditScope}` at :176; `OAuthBindingConfig.Scope` types.go:49 (validated equal to `auditScope` at oauth.go:88). `tokenResponse` oauth.go:187-191 has **no `scope` field**; `decodeTokenResponse` :193-208 validates only `access_token`/`token_type`/`expires_in`. |
| E4 | `publisher.go:196-199`: `validateReceipt` treats 401 as generic | **Verified; lines corrected.** `Publish` publisher.go:83-97; `validateReceipt` **publisher.go:220-245** — 202-only, body drained to `io.Discard` :223, `fmt.Errorf("audit governance: HTTP %d", response.StatusCode)` :224. `Publisher.tokens TokenSource` :28; `authorization` **publisher.go:103-120** (sole caller `Publish`; collapses every `AccessToken` error into `ErrTokenUnavailable` :113-117 — a token-endpoint scope classification must be propagated past this wrap, C-B). `maxReceiptBytes` :18. |
| E5 | `publisher.go:143-156`: authorization | **Verified; lines corrected.** `authorization` at publisher.go:103-120; `newRequest` :122-135; `IdempotencyKey = fact.ID.String()` :185 — retried publishes carry an identical idempotency key, so refetch/redelivery does not disturb governance dedup. |
| E6 | `internal/service/callback_audit.go:108-112`: publisher error path | **Verified; lines corrected.** `publishAuditFact` **callback_audit.go:155-180** is the sole `Publish` caller (:177); `finishPublicationAttempt` :110-139; `publicationOutcome` :142-153 — only `errAuditTenantUnbound` → `("pending","audit.binding_unavailable")` (:146-148, never dead-letters), everything else → `("pending","audit.publication_failed")` → `dead_letter` at `attempt >= maxAttempts` (:149-151). `drainPublications` :52-93 fans out through `s.bulkhead.Acquire/Release` (:75-81). `service.AuditPublisher` is `Publish` only (audit_ledger.go:24-26). |
| E7 | `internal/service/callback.go:66`: `ControlPlaneMetrics` surface | **Verified; lines corrected.** `metrics ControlPlaneMetrics` field at callback.go:69; constructor default `noopControlPlaneMetrics{}` :93; `SetControlPlaneMetrics` :98-101; interface control_plane_metrics.go:12-26, noop :28-42. Registry counter maps control_plane.go:76-88, render families :186-232 — **no scope-missing symbol** (E8). |
| E8 | No alerting hook for a scope-missing condition; noop is the production default | **Gap verified; premise corrected.** No `ObserveAuditScopeMissing`/`OnScopeRejected`/`ErrTokenScopeMissing` anywhere (`rg` → zero). The noop-default premise is **stale**: production wires `RegistryControlPlaneMetrics` (app/new.go:96 → app/audit.go:56-65); the noop is the constructor fallback only. The F-1 setter loop at app/audit.go:52-65 already attaches metrics to `callbackSvc` — the service-side scope counter needs no new wiring path. |
| E9 | Lease/retry mechanics for the T-5 "same worker lease window" claim | **Verified.** `ClaimPublication` (repository/audit_publication.go:46-70) claims `pending AND next_attempt_at <= now()` plus expired-lease `delivering` rows, `FOR UPDATE SKIP LOCKED`, `lease_until = now() + lease` in DB clock; `FinishPublication` :111-119 fences on `(id, status='delivering', lease_owner=worker)` → `ErrAuditPublicationLeaseLost`. `retryDelay` (callback.go:377-393): `RetryBase` default 1 s, exponential to 1 h + jitter; `publicationLease` (callback_audit.go:97-111) floors at 5 min. A `pending` settlement at `now + retryDelay(1)` therefore re-claims on the next drain tick **inside** the original lease window — the refetched token serves the retried attempt without lease expiry or worker change. |
| E10 | Test surface | **Verified.** `publisher_test.go` has 7 tests (:17, :102, :152, :198, :214, :221, :257) — none covers 401/invalidation/epoch/scope; in-package `httptest` loopback pattern with `AllowInsecureLoopback: true` and injected `tenantTokenSource.now` (oauth.go:82). `callback_audit_test.go` has 11 drain tests with fake repos (`settlingPublicationRepo`, `failingPublicationRepo`, `failingAuditPublisher`) — the T-5 fixture pattern exists. **Integration-tree constraint:** `staticTokenSource` (internal/service/callback_integration_test.go:64-73, `//go:build integration`) implements `TokenSource`; the interface change must extend it or `make check-integration-compile`/`make test-integration` break (merged plan §1.2). `internal/connector/billing/oauth.go:28` has an unrelated unexported `tokenSource` — untouched. |

## 3. Requirements

Interface shape is pinned by `docs/plans/2026-08-07-auditgovernance-tokensource-merged-interface.md` §2 (the
superset, fixed exactly once): `AccessToken(context.Context, SourceBinding) (string, error)`,
`HasBinding(string) bool`, `Invalidate(SourceBinding) error`, `BindingUnhealthy(SourceBinding) bool`,
`MarkBindingUnhealthy(SourceBinding)`. This spec specifies the `Invalidate`/epoch/scope-missing slice;
`BindingUnhealthy`/`MarkBindingUnhealthy` are the sibling's 403-cooldown scope and are present on the interface
only — this spec must not introduce a third signature or 403 behavior.

### REQ-1 — `TokenSource.Invalidate(SourceBinding) error` with per-tenant token epoch (types.go, oauth.go)

- Add `Invalidate(SourceBinding) error` to the `TokenSource` interface (types.go:58-61), exactly as pinned by
  the merged plan (error-returning superset; void call sites compile as `_ = p.tokens.Invalidate(binding)`).
  Unbound tenant or mismatched `SourceSystem` is a no-op returning nil (mirrors `AccessToken`'s validation,
  oauth.go:113-120).
- `tenantTokenSource` (oauth.go:30-41) gains `epoch uint64`. `OAuthTokenSource.Invalidate` resolves
  `s.bindings[binding.TenantID]` and calls the per-tenant `invalidate()`.
- `tenantTokenSource.invalidate()`, under `mu.Lock`: set `s.token = ""` and `s.epoch++`. `refreshAt` may be
  left as-is — `cachedToken`'s `token != ""` conjunct (oauth.go:150) is the authority, so immediately after
  `Invalidate` every `cachedToken()` returns `("", false)` and the very next `AccessToken` performs a fresh
  fetch, bypassing the refresh window (the up-to-lifetime stall).
- `Invalidate` on a tenant with no cached token must still bump the epoch (guards the in-flight race, REQ-2).
- Token TTL policy (24 h cap, `refreshSkew`) is unchanged; the epoch is an invalidation guard, not a new expiry.

### REQ-2 — Epoch-fenced single-flight backfill (oauth.go)

- `accessToken` (oauth.go:122-145) reads `s.epoch` under `RLock` and keys the singleflight group by
  `fmt.Sprintf("client-%d", epoch)` instead of the constant `"client"` (:126). Callers that start after
  `Invalidate` join a **new** flight and coalesce into exactly one refetch; a pre-invalidation in-flight fetch
  is never deduped against a post-invalidation one.
- The flight closure captures its epoch and passes it to `fetchToken(ctx, epoch)` (sole caller, :130), which
  passes it to `cacheToken(wire, epoch)` (oauth.go:210-217). `cacheToken` discards the result under `Lock`
  when `epoch != s.epoch` — a stale pre-invalidation fetch can never repopulate the cache after `Invalidate`
  (no poisoned re-entry of the rejected token). On discard, `fetchToken` returns an unexported sentinel
  (e.g. `errEpochDiscarded`) instead of the token.
- `accessToken`, on receiving the discard sentinel from the flight, re-enters the access path against the
  current epoch (joining any fresh in-flight flight) **at most once**; a second consecutive discard returns
  `ErrTokenUnavailable` (bounded work, no livelock under repeated invalidations).
- After `Invalidate`, no code path may return the rejected token: it is stored only in `s.token`, which
  `invalidate()` cleared.

### REQ-3 — Events-endpoint 401 → `Invalidate` + distinguishable `ErrTokenRejected`; 401-with-scope-missing-body → `ErrTokenScopeMissing` (publisher.go, types.go)

In `Publisher.Publish` (publisher.go:83-97), after `client.Do` and before `validateReceipt`:

- On `response.StatusCode == http.StatusUnauthorized`: drain the body with the existing bounded limit
  (`maxReceiptBytes`, publisher.go:18) as today, call `p.tokens.Invalidate(binding)` for the fact's binding,
  and return a distinguishable sentinel — new `ErrTokenRejected` in types.go (message `"audit governance:
  token rejected"`), wrapped as `fmt.Errorf("%w: HTTP %d", ErrTokenRejected, response.StatusCode)` —
  replacing the generic `audit governance: HTTP 401` (publisher.go:224). Invalidation is **status-driven**
  (401), independent of whether the body parses.
- **Direction delta over the merged table:** if the drained 401 body best-effort parses to an OAuth2 error
  code `invalid_scope` or `insufficient_scope`, return `ErrTokenScopeMissing` (message `"audit governance:
  token scope missing"`, wrapped with the HTTP status) **instead of** `ErrTokenRejected` — the two
  classifications are mutually exclusive and `errors.Is`-distinguishable. `Invalidate` still runs (status-
  driven). No connector hook fires for events-endpoint responses (C-C: the `OnScopeRejected` hook is the
  token-endpoint seam only; the alert for events-endpoint classifications is the service-side counter,
  REQ-4/REQ-5). Body-parse failure is non-fatal: the plain `ErrTokenRejected` path applies.
- No retry loop inside `Publish`: the redelivery that benefits from the refetch is the caller's durable
  mechanism (outbox drain, callback_audit.go:155-180 — the only `Publish` caller).
- All other statuses keep the existing `validateReceipt` path (publisher.go:220-245) byte-unchanged,
  including the opaque error for non-202/403.

### REQ-4 — Scope-missing alerting: token-endpoint classification, scope-claim validation, alert seams (types.go, oauth.go, publisher.go, control_plane_metrics.go)

- New sentinel `ErrTokenScopeMissing` in types.go (types.go:18-22 `Err*` block). Distinct from
  `ErrTokenRejected` and `ErrTokenUnavailable` (`errors.Is`-distinguishable; nothing outside the package
  references these identities yet).
- **Token endpoint `invalid_scope`/`insufficient_scope`** (oauth.go): in `fetchToken` (oauth.go:153-173), on
  a non-200 response, read the error body with the existing bounded limit (`maxTokenBodyBytes`, oauth.go:20)
  instead of discarding it and best-effort parse an OAuth2 error envelope (`{"error": "<code>", ...}`). If
  the code is `invalid_scope` or `insufficient_scope`, return `ErrTokenScopeMissing` wrapped with the HTTP
  status and fire the alert hook (below) exactly once with the tenant ID. Body-parse failure is non-fatal:
  the existing `ErrTokenUnavailable: HTTP %d` error is returned as today. Token-endpoint 401/403 without a
  scope code stay `ErrTokenUnavailable` (merged-plan FM-4 boundary preserved).
- **Token-response scope-claim validation** (oauth.go): `tokenResponse` gains
  `Scope string \`json:"scope,omitempty"\``. When the successful response carries a non-empty `scope` claim,
  validate it contains `auditScope` as a space-delimited token (RFC 6749 §3.3 form). A claim missing
  `audit:event:write` → do **not** cache the token; return `ErrTokenScopeMissing` and fire the hook exactly
  once. An absent claim is accepted as today (the server may omit `scope` when identical to the requested
  scope) — no new failure mode for well-behaved endpoints.
- **Alert hook:** optional, nil-safe field `OnScopeRejected func(tenantID string)` on `OAuthConfig`
  (types.go:37-44), reached via `newTenantTokenSource` (oauth.go:71-84, which gains a `tenantID` parameter —
  sole construction site is `NewOAuthTokenSource`). Invoked exactly once per classified token-endpoint
  response; a nil hook is a no-op. **Hook safety is mandatory** (hook-hazard plan): the classification runs
  inside the singleflight flight closure, so the hook must be recover-wrapped (x/sync v0.20.0 re-raises a
  flight panic as an unrecoverable process crash) and re-entrancy-guarded (a hook that self-joins the same
  tenant's flight must not park forever). Implement `notifyScopeRejected` with a `recover()` wrapper and a
  `hookRunning` guard.
- **Service-side counter (events-endpoint classifications):** `ControlPlaneMetrics`
  (control_plane_metrics.go:12-26) gains `ObserveAuditScopeMissing(source string)`; `noopControlPlaneMetrics`
  gains a no-op; `metrics.Registry` (control_plane.go:76-88) gains an `auditScopeMissing counterMap` rendered
  as `aero_control_audit_scope_missing_total{source=%q}` (distinct from
  `aero_control_audit_ingest_failures_total`). The composition root binds `OAuthConfig.OnScopeRejected` to
  the same counter family (label per the `fact.Source` convention) at app/audit.go:99-115; the
  `SetControlPlaneMetrics` wiring for `callbackSvc` already exists (app/audit.go:56-65) and needs no new path.
  No double counting per C-C: a token-endpoint scope rejection fires only the hook; an events-endpoint
  classification fires only the service counter.
- **Propagation (C-B):** `Publisher.authorization` (publisher.go:103-120) collapses every `AccessToken`
  error into `ErrTokenUnavailable` (:113-117); it must propagate `ErrTokenScopeMissing` by `errors.Is`
  before the generic wrap, or the token-endpoint classification is unreachable through `Publish`.

### REQ-5 — Drain routing: scope-missing stays pending; refetched token serves the retried attempt inside the lease window (callback_audit.go)

- In `publishAuditFact` (callback_audit.go:155-180), after `publisher.Publish` returns an error: if
  `errors.Is(err, auditgovernance.ErrTokenScopeMissing)` (events-401-with-scope-body, events-403, or
  token-endpoint scope rejection), call `s.metrics.ObserveAuditScopeMissing(strings.TrimSpace(fact.Source))`
  and return `errAuditTenantUnbound`. `publicationOutcome` (callback_audit.go:142-153) then settles
  `("pending", "audit.binding_unavailable")` — the row is **never completed, never swallowed, never
  dead-lettered** by this branch, and re-probes on the retry ladder (merged plan §4 routing). The branch
  precedes the `attempt >= maxAttempts` check, so a persistent scope-missing condition cannot burn
  `MaxAttempts` into `dead_letter`.
- A plain `ErrTokenRejected` (401 without scope-missing body) keeps the existing generic ladder
  (`pending`/`audit.publication_failed` → `dead_letter` at cap): the refetch self-heals a revoked token, and
  DLQ is the correct terminal for a persistent 401 (merged plan §4).
- **Same-lease-window refetch:** no code change to the lease machinery is required (E9): the 401 settlement
  is `pending` at `now + retryDelay(1)` (≈ 1 s default), far inside the 5-minute lease floor
  (callback_audit.go:97-111), so the next drain tick re-claims the row for the same worker and the refetched
  token (REQ-2) serves the retried `Publish` before the lease expires. Tests must pin this end-to-end (§4
  T-5). `FinishPublication` errors (lease lost) are returned, never `_ =`-swallowed (existing contract,
  callback_audit.go:135-139).
- `service.AuditPublisher` (audit_ledger.go:24-26) is untouched.

### REQ-6 — Engineering constraints (all files)

- File limits (non-test): oauth.go 217 lines, publisher.go 272, types.go 121 — headroom under 500, but the
  REQ-1/REQ-2/REQ-4 additions must keep each file ≤ 500; split into sibling files (e.g. `oauth_errors.go`)
  at 400 lines per AGENTS.md.
- Function limits: `accessToken`, `fetchToken`, `Publish` stay ≤ 50 lines and cyclomatic ≤ 10; new helpers
  (`invalidate`, epoch plumbing, error-body classification, `notifyScopeRejected`) are small (≤ 50 lines,
  cyclomatic ≤ 10); package average cyclomatic ≤ 5.0 (`make lint-strict`).
- New sentinels live in types.go alongside the existing `Err*` set; the connector continues to import only
  stdlib + `golang.org/x/sync/singleflight` + `internal/domain` (no service/repository/metrics imports).
- The merged-interface commit extends `staticTokenSource`
  (internal/service/callback_integration_test.go:64-73) with the new methods in the same commit
  (compile unit; `make check-integration-compile` is the only gate that proves it).
- No new configuration keys, no migrations, no proto changes.

## 4. Acceptance checks (supplied checks preserved; made testable)

Supplied: *"T-10.2/T-10.1: httptest connector test — first Publish returns 401, second Publish immediately
refetches (token endpoint hit >= 2) with exactly one in-flight fetch under N concurrent publishers (epoch
single-flight); a 401 with scope-missing body emits an alert metric and the publication stays pending (not
completed, not swallowed). T-5: the refetched token is used for the retried publication attempt within the
same worker lease window."*

All tests are in-package unit tests (`package auditgovernance` / `package service`) using `httptest` loopback
servers (`AllowInsecureLoopback: true`, per the existing pattern, publisher_test.go:54-64) with **counting** `/token` and
`/api/v1/events` handlers (hit counters via `atomic.Int32` — handlers run on server goroutines and plain ints
race under N ≥ 8 concurrency); cache/epoch assertions use in-package access
(`tokens.bindings[tenantID].epoch`, `cachedToken()`); the drain test uses the existing fake-repo pattern
(callback_audit_test.go) with the **real** connector against httptest. No real network, no Docker, stdlib
only. Existing 7 connector tests and 11 drain tests pass unchanged.

**T-10.1 (connector unit — "first Publish returns 401, second Publish immediately refetches, token endpoint
hit >= 2").** Fixture: `/token` returns a valid token and counts POSTs; `/api/v1/events` returns 401 once
(non-scope body, e.g. empty), then 202 with a matching receipt. Assert:

1. First `Publish` fails with `errors.Is(err, ErrTokenRejected)` and `!errors.Is(err, ErrTokenScopeMissing)`;
   `/api/v1/events` counter == 1.
2. Immediately after, without advancing time: in-package `cachedToken()` returns `("", false)` and
   `bindings[tenantID].epoch` incremented by exactly 1 — the invalidation, not the refresh window, dropped
   the token (proves the up-to-lifetime stall is eliminated).
3. Second `Publish` (same fact) succeeds; `/token` counter == **2** total — the refetch was immediate (next
   call), not deferred to `refreshAt`; the governance request's `Authorization` is `Bearer <refetched-token>`
   (never the rejected token); both requests carry identical `event_id`/`idempotency_key`
   (`fact.ID.String()`, publisher.go:185) — governance dedup undisturbed (REQ-5).

**T-10.2 (connector unit — "exactly one in-flight fetch under N concurrent publishers (epoch single-flight)"
and "a 401 with scope-missing body emits an alert metric").** Two parts:

1. **Coalescing:** after the 401 of T-10.1, run N ≥ 8 concurrent `Publish` calls (same fact) against a now-202
   events endpoint. Assert `/token` counter == **2** total (one refetch serving all N — the epoch key joined
   them into one flight), all N publishes succeed, all N governance requests carry
   `Authorization: Bearer <refetched-token>`.
2. **Epoch race:** prime the cache with a successful publish. Make the `/token` handler block on a channel;
   start one `Publish` (it joins a flight and blocks in `fetchToken`); while blocked, call
   `Invalidate(binding)` directly; release the handler with a token. Assert: `cachedToken()` still returns
   `("", false)` (the stale in-flight result was discarded by the `cacheToken` epoch guard — no poisoned
   re-entry of the rejected token), and the next `AccessToken` performs exactly one fresh `/token` POST whose
   token is the one subsequently served.
3. **Scope-missing 401 → alert + pending:** `/api/v1/events` returns 401 with body
   `{"error":"invalid_scope"}`. Assert `errors.Is(err, ErrTokenScopeMissing)` and
   `!errors.Is(err, ErrTokenRejected)`; the cached token is empty and the epoch bumped (status-driven
   `Invalidate` still ran); with a recording `ControlPlaneMetrics` on the service side (or, at connector
   level, the REQ-4 propagation through `authorization`), the alert is observed **exactly once** and no
   connector hook fires for the events-endpoint response (C-C). Negatives: a plain 401 (empty body) fires no
   scope alert; a nil hook does not panic.

**T-10.1d (drain-level leg of the supplied T-10.2/T-10.1 composite — "the publication stays pending (not
completed, not swallowed)"; service unit with fake repo + real connector).** Fixture: `settlingPublicationRepo` (records finish status/attempt);
`ClaimPublication` returns one `AuditPublication` whose Fact is valid; real `NewOAuthTokenSource` +
`NewPublisher` against the httptest pair above (events 401-with-scope-body first, then 202); recording
`ControlPlaneMetrics`. Run `drainPublications` once. Assert:

1. `ObserveAuditScopeMissing` observed exactly once.
2. `FinishPublication` called with status **"pending"** (not "completed", not "dead_letter") — the row was
   settled, not swallowed; `error_code` `audit.binding_unavailable`; attempt 1.
3. `ObserveAuditPublicationOutcome("pending")` observed (no finish failure, no drain skip).

**T-5 (drain-level — supplied "the refetched token is used for the retried publication attempt within the
same worker lease window").** Same fixture; run `drainPublications` twice (two ticks — the retry is due at
`now + retryDelay(1)` ≈ 1 s, far inside the 5-min lease floor; no lease expiry, no worker change, no time
manipulation of `refreshAt`). Assert:

1. Settlements are `[pending, completed]` with attempts `[1, 2]` — the first 401 did not burn the row to
   `dead_letter` and the second attempt completed.
2. `/token` counter == **2** total — exactly one refetch served both the N-way coalescing and the retried
   attempt; the second governance request carried `Authorization: Bearer <refetched-token>` and the same
   `event_id`/`idempotency_key` as the first.
3. Both settlements happened under the same worker id (`lease_owner`); no `ErrAuditPublicationLeaseLost`
   path was exercised (the row never needed re-claim after lease expiry).

**Regression guards:** `var _ TokenSource = (*OAuthTokenSource)(nil)` continues to compile (covers
`Invalidate` presence); `make check-integration-compile` passes (the interface change and the extended
`staticTokenSource` land together); all 7 existing connector tests and 11 existing drain tests pass
unchanged; `make test` clean.

## 5. Out of scope (explicit)

- **403/cooldown machinery** (`MarkBindingUnhealthy`/`BindingUnhealthy`, events-403 → cooldown): the
  sibling's scope per the merged plan; present on the interface only, no behavior specified here.
- **Direction #1 of the same analysis** (make `drainPublications` the sole audit delivery path: AppendInTx
  consolidation, `make check-audit-swallow` gate) and **direction #3** (anchored event_id, DLQ replay
  tooling): not touched.
- No retry loop inside `Publisher.Publish`; no changes to the 202/receipt contract, conflict-as-success
  semantics, token TTL policy, or `validateReceipt` beyond the 401 branch in `Publish`.
- No changes to `internal/connector/billing` (its 401 classification is precedent only).
- The existing `internal-outbox` campaign spec and its design doc remain valid; this spec is the
  `internal/pkg/bulkhead`-labeled instance of the same direction and does not re-litigate the merged
  interface resolution.

## 6. Done gate

`make test && make lint && make lint-strict && make check-file-sizes && make check-integration-compile &&
make build`. No `make test-integration` run is required for the connector slice (no SQL/storage/migration
change; all new logic is in-package `httptest`-testable per E10's fixture pattern), but `make
check-integration-compile` is mandatory because the interface change touches the integration-tagged
`staticTokenSource` (E10). Run `go test -race ./internal/connector/auditgovernance/...` for the epoch race
sub-case (T-10.2.2).
