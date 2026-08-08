# Requirements Specification — internal/outbox: TokenSource Invalidate on 401 with epoch-guarded single-flight backfill and scope-missing alerting

> Effective code scope is `internal/connector/auditgovernance` (`types.go`, `oauth.go`, `publisher.go`,
> `publisher_test.go`): the token source that the audit-publication outbox drain (`internal/service/callback_audit.go`)
> uses to deliver `audit_publications` rows to the governance endpoint. This is the `internal/outbox` campaign spec
> for the direction that sibling specs `2026-08-06-cmd-server-tokensource-invalidate-on-401.md`,
> `2026-08-06-cmd-migrate-tokensource-invalidate-on-401.md`, and `2026-08-07-internal-connector-tokensource-invalidate-on-401.md`
> cover under their own campaign labels. It is deliberately narrower: no service-layer routing, no
> `internal/pkg/metrics` change, no composition-root wiring, no drain-loop changes.

| | |
|---|---|
| Module | `internal/outbox` (effective scope `internal/connector/auditgovernance`) |
| Direction | "TokenSource Invalidate on 401 with epoch-guarded single-flight backfill and scope-missing alerting" |
| Source analysis | `docs/auto/analyses/internal-outbox-f6bb61c6.json` (direction #2) |
| Date | 2026-08-07 |
| Scope | (1) `TokenSource.Invalidate(SourceBinding)` + per-tenant token epoch; (2) epoch-fenced singleflight backfill (stale in-flight result discarded, bounded re-entry); (3) `Publisher.Publish` classifies governance 401 → `Invalidate` + distinguishable `ErrTokenRejected`; (4) scope-missing responses (`invalid_scope` from the token endpoint, 403 from the events endpoint) → distinguishable `ErrTokenScopeMissing` + a nil-safe `OnScopeMissing` alert hook; (5) tests for all three supplied acceptance checks. Nothing else. |

## 1. Problem statement

`tenantTokenSource` caches a client-credentials bearer token until `refreshAt` (`cachedToken`, oauth.go:147-151;
lifetime capped at `min(expires_in, 86400s)` minus `refreshSkew`, `cacheToken` oauth.go:210-217) and has no
invalidation path. When the governance events endpoint rejects a publish with 401, `Publisher.validateReceipt`
turns it into an opaque `audit governance: HTTP 401` error (publisher.go:220-224), the drain re-queues the
publication (`publicationOutcome`, internal/service/callback_audit.go:96-107), and the next attempt reuses the
same rejected cached token until the refresh window — a stall that can persist up to the full token lifetime
(24 h cap). A concurrent refetch race can additionally re-cache the same bad token (an in-flight fetch started
before invalidation completes after it and repopulates the cache via `fetchToken` → `cacheToken`, oauth.go:171).
There is no epoch to fence stale singleflight results and no classification of scope-missing responses
(`invalid_scope` / 403) into a distinct alertable error. The composition root validates bindings at boot
(internal/app/audit.go:34-57, 87-155) but nothing reacts to 401 at runtime.

Four concrete gaps, all verified against the repository (§2):

1. **No `Invalidate`/epoch mechanism.** `TokenSource` (types.go:58-61) exposes only `AccessToken`/`HasBinding`.
   `grep Invalidate internal/` → zero matches. `tenantTokenSource` has no epoch; `s.fetches` (singleflight.Group,
   oauth.go:40) dedupes via `DoChan("client", ...)` (oauth.go:126-130) with a constant key, and a pre-invalidation
   in-flight fetch can repopulate the cache after an invalidation — the rejected token re-enters service.
2. **401 is indistinguishable from any other failure.** `validateReceipt` (publisher.go:220-245) accepts only 202;
   every other status becomes `fmt.Errorf("audit governance: HTTP %d", ...)` (publisher.go:224) with the body
   discarded (publisher.go:223). Nothing signals the token source, so the stale token keeps being served until
   expiry — up to 24 h per tenant with no operator signal.
3. **Scope-missing responses are silent.** A token-endpoint 400 `{"error":"invalid_scope"}` is mapped to the same
   `ErrTokenUnavailable: HTTP 400` as any transient failure (`fetchToken`, oauth.go:164-168; body discarded,
   oauth.go:166) and an events-endpoint 403 is indistinguishable from the generic error — misconfigured OAuth
   grants fail without a distinct alertable signal.
4. **No runtime reaction to 401.** Boot-time binding coverage exists (`validateAuditBindingCoverage`,
   internal/app/audit.go:134-155) but nothing observes rejections at runtime.

## 2. Evidence verification (all citations re-checked against the repository on 2026-08-07, head `13e4676`)

The analysis's line ranges are from an older revision; every cited symbol is present. Corrected current lines:

| # | Cited evidence | Verification result |
|---|---|---|
| E1 | `oauth.go:112-215`: `accessToken` singleflight `DoChan`, `cachedToken`, `fetchToken`, `cacheToken` — no `Invalidate`, no epoch field | **Symbol verified; line range corrected.** `tenantTokenSource` struct oauth.go:30-41 (`token` :38, `refreshAt` :39, `fetches` :40); `accessToken` **oauth.go:122-145** with `DoChan("client", ...)` :126 (constant key); `cachedToken` **:147-151** — `("", false)` unless `token != "" && now().Before(refreshAt)`, the only drop path is expiry; `fetchToken` **:153-173**, exactly one caller (the flight closure, :130); `cacheToken` **:210-217** — `lifetime = min(expires_in, 86400s)`, `refreshAt = now + lifetime − min(refreshSkew, lifetime/2)`; `fetchToken` → `cacheToken` at :171 caches unconditionally. `grep Invalidate internal/` → zero matches; no epoch field exists anywhere in the connector. |
| E2 | `publisher.go:106-108`: `validateReceipt` non-202 → generic `'audit governance: HTTP %d'`, no 401/scope classification | **Verified; line range corrected.** `Publish` at **publisher.go:83-97**; `validateReceipt` at **publisher.go:220-245** — 202-only, body drained to `io.Discard` :223, `fmt.Errorf("audit governance: HTTP %d", response.StatusCode)` :224. `Publisher.tokens TokenSource` :28; `authorization` :103-120 (single caller: `Publish`). |
| E3 | `publisher.go:42-48`: `CanPublishTenant`/`HasBinding` seam | **Verified; line range corrected.** `CanPublishTenant` at **publisher.go:99-100** (`p.tokens.HasBinding(tenantID)`); `HasBinding` on `OAuthTokenSource` at oauth.go:108-110; `TokenSource` interface types.go:58-61. |
| E4 | `publisher_test.go:118,154`: token tests cover credentials only, no 401/revoke | **Verified; line range corrected.** Token tests are at **publisher_test.go:221-276** (`TestOAuthTokenSourceUsesPerTenantCredentialsAndRejectsUnboundTenant`, `TestOAuthTokenSourceRejectsCredentialReuse`) — credentials/unbound coverage only. The file has 7 tests total (:17, :102, :152, :198, :214, :221, :257); none exercises 401/403, invalidation, epoch, or scope. Tests are in-package, use `httptest` loopback servers with `/token` + `/api/v1/events` handlers (`AllowInsecureLoopback: true`), and `tenantTokenSource.now` is injected (oauth.go:79-84) — epoch/cache assertions are directly reachable. |
| E5 | `internal/app/audit.go:38-82`: composition root validates bindings at boot | **Verified; line range corrected.** `buildAuditStack` at **app/audit.go:34-57** calls `buildAuditPublishers` :47; `wireAuditServices` :65-86; `buildAuditPublishers` **:87-118** (`NewOAuthTokenSource` :105-112, `NewPublisher` :114-125); `auditOAuthBindings` :119-133; `validateAuditBindingCoverage` **:134-155** (durable-tenant coverage check). All binding validation is boot-time; nothing observes rejections at runtime (E1: zero `Invalidate` symbols). |
| E6 | Drain consumes `Publisher.Publish`; retry ladder; **the dead inline publisher loop cited by the analysis no longer exists** | **Verified with a tree change.** `publishAuditFact` internal/service/callback_audit.go:109-125 is the sole caller of `publisher.Publish` (:120); `publicationOutcome` :96-107 — only `errAuditTenantUnbound` → `pending`/`audit.binding_unavailable` (:100-101), everything else → `pending`/`audit.publication_failed`, `dead_letter` at `attempt >= maxAttempts` (:103-106); drain ticker `callbackService.Start`, internal/service/callback.go:268-282. The analysis's "legacy inline `_ = publisher.Publish` loop (audit_ledger.go:77-80)" is **gone in the current tree**: `auditLedgerService.Record` (audit_ledger.go:66-82) is repo.Append + `ObserveAuditIngestFailure` only, and `service.AuditPublisher` (audit_ledger.go:24-25) is consumed only via `SetAuditPublishers` (app/audit.go:79-83). Direction #1's loop removal landed before this spec; the remaining #1 items (AppendInTx consolidation, check-audit-swallow gate) stay out of scope (§5). |
| E7 | Single `TokenSource` implementer; billing is unrelated | **Verified.** Implemented only by `OAuthTokenSource` (oauth.go:25-27); no test fakes implement it in-repo. `internal/connector/billing/oauth.go:28` has its own unexported `tokenSource` — unrelated, untouched. New sentinels break nothing: `ErrTokenUnavailable`/`ErrInvalidReceipt` are referenced nowhere outside the package. |
| E8 | No scope-missing metric family exists | **Verified.** `ControlPlaneMetrics` (internal/service/control_plane_metrics.go:13-24) and `metrics.Registry` (internal/pkg/metrics/control_plane.go:77-89, render at :174-246) expose audit ingest/publication counters only; no scope-missing symbol exists (`grep` → zero hits). The connector imports no metrics package today. |

## 3. Requirements

### REQ-1 — `TokenSource.Invalidate(SourceBinding)` with per-tenant token epoch (types.go, oauth.go)

- Add `Invalidate(SourceBinding)` to the `TokenSource` interface (types.go:58-61), alongside the existing
  `AccessToken(context.Context, SourceBinding)` shape. The acceptance text's "Invalidate(token)" means
  invalidating the cached token of the binding whose token was rejected; the behavior contract is call-count
  observable (§4), not signature-observable. Unbound tenant or mismatched `SourceSystem` is a no-op (mirrors
  `AccessToken`'s validation, oauth.go:113-120).
- `tenantTokenSource` (oauth.go:30-41) gains `epoch uint64`. `OAuthTokenSource.Invalidate` resolves
  `s.bindings[tenantID]` and calls the per-tenant `invalidate()`.
- `tenantTokenSource.invalidate()`, under `mu.Lock`: set `s.token = ""` and `s.epoch++`. `refreshAt` may be left
  as-is — `cachedToken`'s `token != ""` conjunct (oauth.go:150) is the authority, so immediately after
  `Invalidate` every `cachedToken()` returns `("", false)` and the very next `AccessToken` performs a fresh
  fetch, bypassing the refresh window (the up-to-lifetime stall).
- `Invalidate` on a tenant with no cached token must still bump the epoch (guards the in-flight race, REQ-2).
- Token TTL policy (24 h cap, `refreshSkew`) is unchanged; the epoch is an invalidation guard, not a new expiry.

### REQ-2 — Epoch-fenced single-flight backfill (oauth.go)

- `accessToken` (oauth.go:122-145) reads `s.epoch` under `RLock` and keys the singleflight group by
  `fmt.Sprintf("client-%d", epoch)` instead of the constant `"client"` (:126). Callers that start after
  `Invalidate` join a **new** flight and coalesce into exactly one refetch; a pre-invalidation in-flight fetch
  is never deduped against a post-invalidation one.
- The flight closure captures its epoch and passes it to `fetchToken(ctx, epoch)` (sole caller, :130 — signature
  change is safe), which passes it to `cacheToken(wire, epoch)` (oauth.go:210-217). `cacheToken` discards the
  result under `Lock` when `epoch != s.epoch` — a stale pre-invalidation fetch can never repopulate the cache
  after `Invalidate` (no poisoned re-entry of the rejected token). On discard, `fetchToken` returns an
  unexported sentinel (e.g. `errEpochDiscarded`) instead of the token.
- `accessToken`, on receiving the discard sentinel from the flight, re-enters the access path against the
  current epoch (joining any fresh in-flight flight) **at most once**; a second consecutive discard returns
  `ErrTokenUnavailable` (bounded work, no livelock under repeated invalidations).
- After `Invalidate`, no code path may return the rejected token: it is stored only in `s.token`, which
  `invalidate()` cleared.

### REQ-3 — Governance 401 → `Invalidate` + distinguishable `ErrTokenRejected` (publisher.go, types.go)

In `Publisher.Publish` (publisher.go:83-97), after `client.Do` and before `validateReceipt`:

- On `response.StatusCode == http.StatusUnauthorized`: drain the body with the existing bounded limit
  (`maxReceiptBytes`, publisher.go:18) as today, call `p.tokens.Invalidate(binding)` for the fact's binding, and
  return a distinguishable sentinel — new `ErrTokenRejected` in types.go (message `"audit governance: token
  rejected"`), wrapped as `fmt.Errorf("%w: HTTP %d", ErrTokenRejected, response.StatusCode)` — replacing the
  generic `audit governance: HTTP 401` (publisher.go:224). The drain's existing retry ladder
  (callback_audit.go:96-107) then re-attempts with a freshly fetched token on the next attempt.
- Classification is **status-driven** (401), independent of whether the response body parses.
- No retry loop inside `Publish`: the redelivery that benefits from the refetch is the caller's durable
  mechanism (outbox drain, callback_audit.go:109-125 — the only `Publish` caller in the current tree).
- All other statuses keep the existing `validateReceipt` path (publisher.go:220-245) unchanged, including the
  opaque error for non-202/403.

### REQ-4 — Scope-missing (`invalid_scope` / 403) → distinguishable `ErrTokenScopeMissing` + alert hook (types.go, oauth.go, publisher.go)

- New sentinel `ErrTokenScopeMissing` in types.go (message `"audit governance: token scope missing"`), in the
  existing `Err*` block (types.go:18-22). Distinct from `ErrTokenRejected` and from `ErrTokenUnavailable`
  (`errors.Is` distinguishable; E7: no external consumers of these identities).
- **Token endpoint `invalid_scope`** (oauth.go): in `fetchToken` (oauth.go:153-173), on a non-200 response, read
  the error body with the existing bounded limit (`maxTokenBodyBytes`, oauth.go:20) instead of discarding it
  (oauth.go:166) and best-effort parse an OAuth2 error envelope (`{"error": "<code>", ...}`). If the code is
  `invalid_scope` (also accept `insufficient_scope`), return `ErrTokenScopeMissing` wrapped with the HTTP status
  and fire the alert hook (below) exactly once with the tenant ID. Body-parse failure is non-fatal: the existing
  `ErrTokenUnavailable: HTTP %d` error is returned as today.
- **Events endpoint 403** (publisher.go): in `Publish`, on `response.StatusCode == http.StatusForbidden`: drain
  the body with the bounded limit, return `ErrTokenScopeMissing` wrapped with the status, and fire the alert hook
  exactly once with the tenant ID. **No `Invalidate` on 403**: a 403 is a scope/grant problem, not a token
  problem — refetching cannot fix it (the direction ties `Invalidate` to 401), and the cached token stays
  usable for the healthy-binding case.
- **Alert hook (the metric callback):** optional, nil-safe field `OnScopeMissing func(tenantID string)` carried
  on `OAuthConfig` (types.go:37-44, reached via `newTenantTokenSource` oauth.go:71-84) for the token-endpoint
  classification and on `Config` for the events-endpoint classification; `newTenantTokenSource` and
  `NewPublisher` copy it onto their structs. Invoked exactly once per classified response; a nil hook is a no-op.
  This is the connector's "raise alert" surface: the package stays dependency-free (no `internal/pkg/metrics`
  import). Binding it to a counter family at the composition root is out of scope here (§5); the recommended
  family, consistent with the sibling specs, is `aero_control_audit_scope_missing_total{source=...}`, distinct
  from `aero_control_audit_ingest_failures_total`.

### REQ-5 — No contract changes to the 202/receipt path (publisher.go)

`validateReceipt`'s 202-only acceptance, receipt envelope validation (`receiptMatches`), conflict-as-success
semantics, and `IdempotencyKey = fact.ID.String()` (publisher.go:185) are unchanged. Retried publishes carry the
identical idempotency key; governance-side dedup on that key is undisturbed by invalidation/refetch.

### REQ-6 — Engineering constraints (all files)

- File limits (non-test): oauth.go 217 lines, publisher.go 272, types.go 121 — headroom under 500, but the
  REQ-1/REQ-2/REQ-4 additions must keep oauth.go ≤ 500 (split into a sibling file such as `oauth_errors.go` if
  needed, per AGENTS.md "at 400 lines, plan a split").
- Function limits: `accessToken`, `fetchToken`, `Publish` stay ≤ 50 lines and cyclomatic ≤ 10; the new
  `invalidate`, epoch plumbing, and error-body classification live in small helpers (≤ 50 lines, cyclomatic
  ≤ 10); package average cyclomatic ≤ 5.0 (`make lint-strict`).
- New sentinels live in types.go alongside the existing `Err*` set; the connector continues to import only
  stdlib + `golang.org/x/sync/singleflight` + `internal/domain` (no service/repository/metrics imports).
- No new configuration keys, no migrations, no proto changes.

## 4. Acceptance checks (supplied checks preserved; made testable)

All tests are in-package unit tests (`package auditgovernance`) using `httptest` loopback servers
(`AllowInsecureLoopback: true`, per the existing pattern, publisher_test.go:36-60) with **counting** `/token`
and `/api/v1/events` handlers (hit counters mutex-guarded or `atomic.Int32` — handlers run on server goroutines
and plain ints race under N ≥ 8 concurrency); assertions on cache/epoch use in-package access
(`tokens.bindings[tenantID]`). No real network, no Docker, stdlib only. Existing 7 tests keep passing unchanged.

**T-10.1a (unit, supplied: "cached token + Invalidate(token) => next AccessToken triggers exactly one new
fetch (call-count assertion)").** Fixture: `/token` returns a valid token and counts POSTs; `/api/v1/events`
counts requests, returns 401 once, then 202 with a matching receipt. Assert:

1. First `Publish` fails with `errors.Is(err, ErrTokenRejected)`; `/api/v1/events` counter == 1.
2. Immediately after, without advancing time, in-package `tokens.bindings[tenantID].cachedToken()` returns
   `("", false)` and `s.epoch` incremented by exactly 1 — the invalidation, not the refresh window, dropped the
   token (proves the up-to-lifetime stall is eliminated).
3. Next `AccessToken` (or second `Publish`, events now 202) returns nil; `/token` counter == **2** total —
   exactly one new fetch; the governance request's `Authorization` header is `Bearer <refetched-token>` (never
   the rejected token); `idempotency_key`/`event_id` still equal `fact.ID.String()`.

**T-10.1b (unit, supplied: "N concurrent callers after invalidation still cause exactly one fetch
(epoch-fenced singleflight)").** Two parts:

1. **Coalescing:** after the 401 of T-10.1a, run N ≥ 8 concurrent `Publish` calls (same fact) against a now-202
   events endpoint. Assert `/token` counter == **2** total (one refetch serving all N), all N publishes succeed,
   and all N governance requests carry `Authorization: Bearer <refetched-token>`.
2. **Epoch race:** prime the cache with a successful publish. Make the `/token` handler block on a channel;
   start one `Publish` (it joins a flight and blocks in `fetchToken`); while blocked, call
   `Invalidate(binding)` directly; release the handler with a token. Assert: `cachedToken()` still returns
   `("", false)` (the stale in-flight result was discarded — no poisoned re-entry of the rejected token), and
   the next `AccessToken` performs exactly one fresh `/token` POST whose token is the one subsequently served.

**T-10.1c (integration/httptest, supplied: "governance 401 on publish causes immediate token refresh and
redelivery succeeds, eliminating the up-to-lifetime stall").** Stateful events handler: 401 for the first
request (stale cached token), 202 with a matching receipt for the second (refetched token). Assert:

1. First `Publish` → `errors.Is(err, ErrTokenRejected)`; `cachedToken()` empty immediately (no time advance).
2. Second `Publish` (the drain's redelivery attempt, same fact) → nil; `/token` counter == 2 total — the
   refresh was immediate (next attempt), not deferred to the refresh window; second request's
   `Authorization` == `Bearer <refetched-token>`; both requests carry identical `event_id`/`idempotency_key`
   (governance dedup contract, REQ-5).
3. The whole exchange completes without any time-travel or `refreshAt` manipulation — the stall that previously
   persisted up to the full token lifetime is gone.

**T-10.1d (unit, supplied: "scope-missing (invalid_scope/403) mapped to a distinct error code plus an
alert/metric callback, verified by test").** Assert:

1. Events endpoint 403 → `errors.Is(err, ErrTokenScopeMissing)`, `!errors.Is(err, ErrTokenRejected)`; with
   `Config.OnScopeMissing` set to a recorder, the hook fired exactly once with the tenant ID; `cachedToken()`
   still returns the token and `s.epoch` unchanged (403 does not invalidate, REQ-4).
2. Token endpoint 400 `{"error":"invalid_scope"}` → `errors.Is(err, ErrTokenScopeMissing)`,
   `!errors.Is(err, ErrTokenUnavailable)` / `!errors.Is(err, ErrTokenRejected)`; with
   `OAuthConfig.OnScopeMissing` set, the hook fired exactly once with the tenant ID.
3. Negatives: `/token` 500 keeps `ErrTokenUnavailable: HTTP 500` and does **not** fire the hook; a nil hook does
   not panic; events 401 in the same run still returns `ErrTokenRejected` and fires no scope hook (the two
   classifications are independent).

**Regression guards:** `var _ TokenSource = (*OAuthTokenSource)(nil)` continues to compile (covers `Invalidate`
presence); all 7 existing tests in publisher_test.go pass unchanged; `make test` is clean.

## 5. Out of scope (explicit)

- **Service-layer routing and metrics wiring** (sibling spec `2026-08-06-cmd-server-tokensource-invalidate-on-401.md`
  and its design `docs/plans/2026-08-06-cmd-server-tokensource-invalidate-on-401-design.md`): the
  `errAuditTenantUnbound`/`audit.binding_unavailable` branch, `ControlPlaneMetrics` interface changes, and the
  `internal/app` composition-root binding of `OnScopeMissing` to a Registry counter family are **not** part of
  this spec. Recommended counter family for that follow-up:
  `aero_control_audit_scope_missing_total{source=...}`.
- **Binding-unhealthy short-circuit / cooldown** (`MarkBindingUnhealthy`/`BindingUnhealthy` in the cmd/server
  design): not in this direction.
- **Direction #1 of the same analysis** (make `drainPublications` the sole audit delivery path: AppendInTx
  consolidation, `make check-audit-swallow`; the dead inline publisher loop was already removed before this
  spec — E6) and **direction #3** (anchored event_id, DLQ replay tooling): not touched.
- No retry loop inside `Publisher.Publish`; no changes to the 202/receipt contract, token TTL policy, or
  `validateReceipt` semantics beyond the 401/403 branch in `Publish`.
- **Sibling signature reconciliation (merge note):** sibling specs specify `Invalidate(tenantID string)`
  (2026-08-07-internal-connector) and `Invalidate(SourceBinding) error` (cmd-server design, D3). This spec's
  `Invalidate(SourceBinding)` (void) cannot coexist with either (same method name, different signature =
  compile error). Reconcile to one signature before merging; behavior and tests in §4 are signature-independent.
- No changes to `internal/connector/billing` (its 401 classification is precedent only).

## 6. Done gate

`make test && make lint && make lint-strict && make check-file-sizes && make build`. No `make test-integration`
run is required (no SQL/storage/migration change; all new logic is in-package `httptest`-testable per E4's
fixture pattern). All changed and existing `auditgovernance` tests must pass under `make test`.
