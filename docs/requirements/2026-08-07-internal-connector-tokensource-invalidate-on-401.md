# Requirements Specification — internal/connector: TokenSource.Invalidate(tenantID) on governance 401/403, epoch-guarded single-flight refetch, scope-missing alerting

> Target code is `internal/connector/auditgovernance` only (`types.go`, `oauth.go`, `publisher.go`,
> `publisher_test.go`). This is the `internal/connector` campaign spec for the direction that sibling specs
> `2026-08-06-cmd-server-tokensource-invalidate-on-401.md` and `2026-08-06-cmd-migrate-tokensource-invalidate-on-401.md`
> cover under their own campaign labels. It is deliberately narrower: no service-layer routing, no
> `internal/pkg/metrics` change, no composition-root wiring.

| | |
|---|---|
| Module | `internal/connector` |
| Direction | "Add TokenSource.Invalidate(tenantID) driven by governance 401/403 responses, with an epoch-guarded single-flight refetch and scope-missing alerting" |
| Source analysis | `docs/auto/analyses/internal-connector-25126edd.json` (direction #2) |
| Date | 2026-08-07 |
| Scope | (1) `TokenSource.Invalidate(tenantID)` + per-tenant token epoch; (2) epoch-guarded singleflight refetch (stale in-flight result discarded, bounded re-entry); (3) `Publisher.Publish` invalidates on governance 401/403 and returns a distinguishable `ErrTokenRejected`; (4) token-endpoint `invalid_scope` produces a distinguishable `ErrScopeRejected` and fires a scope-missing alert hook; (5) connector-only tests for all three supplied acceptance checks. Nothing else. |

## 1. Problem statement

`tenantTokenSource` caches a client-credentials bearer token until `refreshAt` (`cachedToken`, oauth.go:147-151;
lifetime capped at `min(expires_in, 86400s)` minus `refreshSkew`, `cacheToken` oauth.go:210-217). There is no
invalidation path. When the governance events endpoint rejects a publish with 401/403 (rotated or revoked token),
`Publisher.Publish` surfaces a generic `audit governance: HTTP 401` error (`validateReceipt`, publisher.go:220-245,
accepts only 202) and every subsequent publish reuses the same rejected cached token until expiry — audit
publication can fail for up to 24 hours per tenant with no operator signal.

Three concrete gaps, all verified against the repository (Section 2):

1. **No `Invalidate`/epoch mechanism.** `TokenSource` (types.go:58-61) exposes only `AccessToken`/`HasBinding`.
   `grep Invalidate internal/` → zero matches. `tenantTokenSource` has no epoch; `s.fetches` (singleflight.Group,
   oauth.go:40) dedupes via `DoChan("client", ...)` (oauth.go:126-130) but a pre-invalidation in-flight fetch can
   repopulate the cache after an invalidation (`fetchToken` → `cacheToken`, oauth.go:171), so a 401-racing fetch
   result can overwrite the invalidation.
2. **401/403 are indistinguishable from any other failure.** `validateReceipt` (publisher.go:220-245) accepts only
   202; every other status becomes `fmt.Errorf("audit governance: HTTP %d", ...)` (publisher.go:224) with the body
   discarded (publisher.go:223). Nothing signals the token source, so the stale token keeps being served.
3. **Token-endpoint scope rejection is silent.** The token request pins `scope=audit:event:write`
   (`tokenRequest`, oauth.go:175-187) and the binding scope is enforced only at config time (`validOAuthBinding`,
   oauth.go:86-89); a runtime 400 `invalid_scope` from the token endpoint is mapped to the same
   `ErrTokenUnavailable: HTTP 400` as any transient failure (`fetchToken`, oauth.go:164-168), the error body is
   discarded (oauth.go:166), and there is no alert — misconfigured bindings fail silently until boot-time coverage
   checks.

## 2. Evidence verification (all citations re-checked against the repository on 2026-08-07)

The analysis's line ranges are from an older revision; every symbol is present. Corrected current lines:

| # | Cited evidence | Verification result |
|---|---|---|
| E1 | `TokenSource` interface at `types.go:33-36` | **Symbol verified; line range corrected.** The interface is at **types.go:58-61**: `AccessToken(context.Context, SourceBinding) (string, error)` + `HasBinding(string) bool`. Lines 33-36 are the tail of the `Config` struct — no `Invalidate` anywhere (grep, zero matches). `SourceBinding` at types.go:53-56; existing `Err*` set at types.go:16-23. |
| E2 | `cachedToken` serves the cached token until `refreshAt` (`oauth.go:121-133`) | **Verified; line range corrected.** `accessToken` at oauth.go:122-145 consults `cachedToken()` at :123-125 before any fetch. `cachedToken` itself is at **oauth.go:147-151**: returns `(s.token, s.token != "" && s.now().Before(s.refreshAt))` — the only way to drop the token is expiry. |
| E3 | Lifetime capped at 24 h (`oauth.go:208-218`) | **Verified.** `cacheToken` at oauth.go:210-217: `lifetime = min(expires_in, 86400s)` (:211), `refreshAt = now + lifetime - min(refreshSkew, lifetime/2)` (:215). A rejected token therefore survives up to 24 h. |
| E4 | Singleflight dedupes fetches but has no epoch (`oauth.go:141-149`) | **Verified; line range corrected.** `fetches singleflight.Group` field at oauth.go:40; `DoChan("client", ...)` at **oauth.go:126-130** with a fixed key; the closure only re-checks `cachedToken` (:127). No epoch field exists (`grep epoch internal/connector/` → zero matches). `fetchToken` → `cacheToken` at oauth.go:171 caches unconditionally — a pre-invalidation result can repopulate the cache after an invalidation. `fetchToken` has exactly one caller (the closure, oauth.go:128), so its signature can change safely. |
| E5 | `validateReceipt` maps every non-202 to an opaque error (`publisher.go:151-160`) | **Verified; line range corrected.** `validateReceipt` is at **publisher.go:220-245** (lines 151-160 are `governanceEvent` struct fields). Non-202 → `fmt.Errorf("audit governance: HTTP %d", response.StatusCode)` at :221-224 after discarding the body (:223). `Publish` at publisher.go:83-97; `authorization` at :103-120; `Publisher.tokens TokenSource` at :28. |
| E6 | No 401-invalidation test in `publisher_test.go` | **Verified.** 272 lines, 7 tests: source/receipt matching (:17), conflict-receipt idempotency (:102), conflict-echo guard (:152), event-type invariance (:198), tenantless rejection (:214), per-tenant credentials (:221), credential reuse (:257). No 401/403, epoch, or scope test exists. Tests are in-package, use `httptest` loopback servers with `/token` + `/api/v1/events` handlers (`AllowInsecureLoopback: true`), and `tenantTokenSource.now` is injected (oauth.go:79-84) — epoch/cache assertions are directly reachable. |
| E7 | Single production implementor; no external sentinel consumers | **Verified.** `TokenSource` is implemented only by `OAuthTokenSource` (oauth.go:25-27), wired at internal/app/audit.go:99-113; no test fakes exist outside the package. `ErrTokenUnavailable`/`ErrInvalidReceipt` are referenced nowhere outside `internal/connector/auditgovernance` (grep) — new standalone sentinels break nothing. `internal/connector/billing` has its own unrelated `tokenSource` (billing/oauth.go:44). |
| E8 | Token endpoint rejects scope silently | **Verified.** `fetchToken` at oauth.go:153-173: non-200 → body drained to `io.Discard` (:166), return `fmt.Errorf("%w: HTTP %d", ErrTokenUnavailable, status)` (:167-168). `decodeTokenResponse` (oauth.go:193-208) reads only `access_token`/`token_type`/`expires_in`; `tokenRequest` hardcodes `scope=auditScope` (:178). No alert symbol exists in `internal/connector` (grep); `internal/connector/http_summary.go:16,36` shows the package-level precedent for a `*metrics.Registry` sink, but `auditgovernance` currently imports no metrics. |

## 3. Requirements

### REQ-1 — `TokenSource.Invalidate(tenantID)` with per-tenant token epoch (oauth.go, types.go)

- Add `Invalidate(tenantID string)` to the `TokenSource` interface (types.go:58-61). The argument is the tenant ID
  per the direction text, consistent with the existing `HasBinding(string)` shape (oauth.go:108-110); the
  per-tenant source is resolved by `s.bindings[tenantID]`. (Sibling campaign specs use a binding-typed variant;
  this spec follows the direction's stated signature — a merge note only, no behavior difference.)
- `tenantTokenSource` gains an `epoch uint64` field (oauth.go:30-41, alongside `token`/`refreshAt`).
- `OAuthTokenSource.Invalidate(tenantID)` (oauth.go:113-120 sibling method): unbound tenant is a no-op (the
  publisher only invalidates the binding it just used); otherwise call the per-tenant `invalidate()`.
- `tenantTokenSource.invalidate()`, under `mu.Lock`: set `s.token = ""` and `s.epoch++`. `refreshAt` may be left
  as-is — `cachedToken`'s `token != ""` conjunct (oauth.go:150) is the authority, so after `Invalidate` every
  `cachedToken()` returns `("", false)` and the very next `AccessToken` performs a fresh fetch.
- `Invalidate` on a tenant with no cached token is still required to bump the epoch (guards the in-flight race,
  REQ-2).
- Token cache TTL policy (24 h cap, `refreshSkew`) is unchanged; the epoch is an invalidation guard, not a new
  expiry mechanism.

### REQ-2 — Epoch-guarded single-flight refetch (oauth.go)

- `accessToken` (oauth.go:122-145) reads `s.epoch` under `RLock` and keys the singleflight group by
  `fmt.Sprintf("client-%d", epoch)` instead of the constant `"client"` (:126). Callers that start after
  `Invalidate` join a **new** flight and coalesce into exactly one refetch; a pre-invalidation in-flight fetch is
  never deduped against a post-invalidation one.
- The flight closure captures its epoch and passes it to `fetchToken`; `fetchToken(ctx, epoch)` passes it to
  `cacheToken(wire, epoch)` (oauth.go:210-217), which discards the result under `Lock` when `epoch != s.epoch` —
  a stale pre-invalidation fetch can never repopulate the cache after `Invalidate` (no poisoned re-entry). On
  discard, `fetchToken` returns an internal sentinel (unexported, e.g. `errEpochDiscarded`) instead of the token.
- `accessToken`, on receiving the discard sentinel from the flight, re-enters the access path against the current
  epoch (joining any fresh in-flight flight) **at most once**; a second consecutive discard returns
  `ErrTokenUnavailable` (bounded work, no livelock under repeated invalidations).
- After `Invalidate`, no code path may return the rejected token: it is stored only in `s.token`, which
  `invalidate()` cleared.

### REQ-3 — Publisher invalidates on governance 401/403 and returns a distinguishable error (publisher.go)

In `Publisher.Publish` (publisher.go:83-97), after `client.Do` and before `validateReceipt`:

- On `response.StatusCode == http.StatusUnauthorized` **or** `http.StatusForbidden`: call
  `p.tokens.Invalidate(fact.TenantID)` (the direction drives invalidation from both statuses) and return a
  distinguishable sentinel — new `ErrTokenRejected` in types.go (message "audit governance: token rejected"),
  wrapped as `fmt.Errorf("%w: HTTP %d", ErrTokenRejected, response.StatusCode)` — replacing the generic
  `audit governance: HTTP 401` (publisher.go:224). Response body handling: drain with the existing bounded limit
  (`maxReceiptBytes`, publisher.go:18) as today.
- All other statuses keep the existing `validateReceipt` path (publisher.go:220-245) unchanged, including the
  opaque error for non-202.
- Invalidation is **status-driven** (any 401/403), independent of whether the response body parses.
- No retry loop is added inside `Publish`: the retry that benefits from the refetch is the caller's durable
  mechanism (outbox drain, internal/service/callback_audit.go:47-112), which now succeeds because
  `cachedToken()` is empty. As a side effect, the legacy inline `_ = publisher.Publish` loop
  (internal/service/audit_ledger.go:77-80) also triggers the same invalidation on 401 — no code change there.
- A 403 with a valid-but-wrong-scoped token will be refetched once per rejection; the token-endpoint alert
  (REQ-4) is the operator signal for that misconfiguration. Binding-unhealthy short-circuiting is explicitly
  **not** part of this direction (see §5).

### REQ-4 — Token-endpoint `invalid_scope` → distinguishable error + scope-missing alert hook (oauth.go, types.go)

- In `fetchToken` (oauth.go:153-173), on a non-200 response, read the error body with the existing bounded limit
  (`maxTokenBodyBytes`, oauth.go:20) instead of discarding it (oauth.go:166) and best-effort parse an OAuth2
  error envelope (`{"error": "<code>", ...}`). If the code is `invalid_scope` (the OAuth2 client-credentials
  scope-rejection code; `insufficient_scope` on the token endpoint is also accepted), return a new distinguishable
  sentinel — `ErrScopeRejected` in types.go (message "audit governance: token scope rejected"), wrapped with the
  HTTP status — and fire the alert hook (below) exactly once with the tenant ID.
- Body-parse failure is non-fatal: the existing `ErrTokenUnavailable: HTTP %d` error is returned as today, and the
  scope check never blocks the REQ-3 invalidation path.
- **Alert hook (the connector's metric sink):** `OAuthConfig` (types.go:37-44) gains an optional, nil-safe field
  `OnScopeRejected func(tenantID string)`. `newTenantTokenSource` (oauth.go:71-84) carries it onto
  `tenantTokenSource`; `fetchToken` invokes it in a nil-checked call on `invalid_scope`. This is the connector's
  "raise alert" surface: the auditgovernance package stays dependency-free (no `internal/pkg/metrics` import),
  and the binding to a counter family at the composition root is out of scope here (see §5 — recommended family
  name, consistent with the sibling campaign specs: `aero_control_audit_scope_missing_total{source=...}`, distinct
  from `aero_control_audit_ingest_failures_total`).
- `ErrScopeRejected` and `ErrTokenRejected` are standalone sentinels in the `Err*` block (types.go:16-23); no
  existing code depends on `ErrTokenUnavailable` error identity outside the package (E7), so no wrapping chain is
  required.

### REQ-5 — No contract changes to the 202/receipt path (publisher.go)

`validateReceipt`'s 202-only acceptance, receipt envelope validation (`receiptMatches`), conflict-as-success
semantics, and `IdempotencyKey = fact.ID.String()` (publisher.go:185) are unchanged. Retried publishes carry the
identical idempotency key; governance-side dedup on that key is undisturbed by invalidation/refetch.

### REQ-6 — Engineering constraints (all files)

- File limits (non-test): oauth.go is 217 lines, publisher.go 272, types.go 121 — headroom under 500, but the
  REQ-1/REQ-2/REQ-4 additions must keep oauth.go ≤ 500 (split into a sibling file such as `oauth_errors.go` at
  ~400 lines per AGENTS.md if needed).
- Function limits: `accessToken`, `fetchToken`, `Publish` stay ≤ 50 lines and cyclomatic ≤ 10; the new
  `invalidate`, epoch plumbing, and error-body classification belong in small helpers (≤ 50 lines, cyclomatic
  ≤ 10); package average cyclomatic ≤ 5.0.
- New sentinels live in types.go alongside the existing `Err*` set; the connector continues to import only
  `internal/domain` plus stdlib/`singleflight` (no service/repository/metrics imports).

## 4. Acceptance checks (supplied checks preserved; made testable)

All tests are in-package unit tests (`package auditgovernance`) using `httptest` loopback servers
(`AllowInsecureLoopback: true`, per the existing pattern, publisher_test.go:36-60) with counting `/token` and
`/api/v1/events` handlers; assertions on cache/epoch use in-package access (`tokens.bindings[tenantID]`). No real
network, no Docker, stdlib only.

**T-10.1a — governance 401 → publisher calls Invalidate → next Publish triggers exactly one token refetch.**
Fixture: `/token` returns a valid token and counts POSTs; `/api/v1/events` counts requests, returns 401 once, then
202 with a matching receipt. Assert:

1. First `Publish` fails with `errors.Is(err, ErrTokenRejected)` and `/api/v1/events` counter == 1.
2. Immediately after, in-package `tokens.bindings[tenantID].cachedToken()` returns `("", false)` (proves the
   publisher called `Invalidate`), and `s.epoch` incremented by exactly 1.
3. Second `Publish` (same fact, events now 202) returns nil; `/token` counter == **2** total (exactly one refetch);
   the second governance request's `Authorization` header is `Bearer <refetched-token>` (never the rejected
   token), and its `idempotency_key`/`event_id` equal `fact.ID.String()` (unchanged across both attempts).

**T-10.1b — N concurrent publishes after 401 produce one token POST; epoch bump during in-flight fetch discards
the stale result.** Two parts:

1. **Coalescing:** after the 401 of T-10.1a, run N ≥ 8 concurrent `Publish` calls (same fact) against a now-202
   events endpoint. Assert `/token` counter == **2** total (one refetch serving all N), all N publishes succeed,
   and all N governance requests carry `Authorization: Bearer <refetched-token>`.
2. **Epoch race:** prime the cache with a successful publish. Then make the `/token` handler block on a channel;
   start one `Publish` (it joins a flight and blocks in `fetchToken`); while blocked, call `Invalidate(tenantID)`
   directly; release the handler with a token. Assert: `cachedToken()` still returns `("", false)` (the stale
   in-flight result was discarded — no poisoned re-entry), and the next `AccessToken` performs exactly one fresh
   `/token` POST whose token is the one subsequently served (the discarded token string appears nowhere in any
   governance `Authorization` header).

**T-10.1c — token endpoint returning `invalid_scope` raises a scope-missing alert and a distinguishable error.**
Fixture: `/token` returns 400 with body `{"error":"invalid_scope"}`. Assert:

1. `AccessToken` (via `Publish` or directly) fails with `errors.Is(err, ErrScopeRejected)` and
   `!errors.Is(err, ErrTokenRejected)` / `!errors.Is(err, ErrTokenUnavailable)` — distinguishable.
2. With `OAuthConfig.OnScopeRejected` set to a recorder, the hook fired exactly once with the tenant ID.
3. Negatives: `/token` returning 500 keeps the existing `ErrTokenUnavailable: HTTP 500` path and does **not** fire
   the hook; a nil hook does not panic; a governance-endpoint 401 in the same run still returns `ErrTokenRejected`
   (the two classifications are independent).

**T-10.1d — 403 also drives Invalidate (direction title: "401/403 responses").** Events endpoint returns 403:
`Publish` fails with `errors.Is(err, ErrTokenRejected)` and `cachedToken()` returns `("", false)` — same
invalidation contract as 401.

**Interface/regression guards:** `var _ TokenSource = (*OAuthTokenSource)(nil)` continues to compile (covers
`Invalidate` presence); all 7 existing tests in publisher_test.go keep passing unchanged.

## 5. Out of scope (explicit)

- **Service-layer routing and metrics wiring** (sibling spec `2026-08-06-cmd-server-tokensource-invalidate-on-401.md`
  T-10.2): the `errAuditTenantUnbound`/`audit.binding_unavailable` branch, `ControlPlaneMetrics` seam changes, and
  the `internal/app` composition-root binding of `OnScopeRejected` to a Registry counter are **not** part of this
  connector spec. The recommended counter family for that follow-up is
  `aero_control_audit_scope_missing_total{source=...}` (distinct from `aero_control_audit_ingest_failures_total`).
- **Binding-unhealthy short-circuit / cooldown** (`MarkBindingUnhealthy`/`BindingUnhealthy` in the cmd/server
  sibling): not in this direction; 403 is treated as an invalidation trigger here.
- **Direction #1 of the same analysis** (single delivery path / AppendInTx / check-audit-swallow gate): not touched.
- No retry loop inside `Publisher.Publish`; no changes to the 202/receipt contract, token TTL policy, or
  `validateReceipt` semantics beyond the 401/403 branch in `Publish`.
- No new configuration keys, no migrations, no proto changes, no `internal/pkg/metrics` import in the connector,
  no changes to `internal/connector/billing` (its 401 classification is precedent only).

## 6. Done gate

`make test && make lint && make lint-strict && make check-file-sizes && make build`. No `make test-integration`
run is required (no SQL/storage/migration change; connector tests use `httptest` loopback servers per the existing
pattern); all changed and existing `auditgovernance` tests must pass under `make test`.
