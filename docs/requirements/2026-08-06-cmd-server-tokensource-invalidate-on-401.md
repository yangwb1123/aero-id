# Requirements Specification — cmd/server: TokenSource invalidation on 401 with epoch single-flight backfill and scope-missing alerting

> Target code is `internal/connector/auditgovernance`, `internal/service`, `internal/pkg/metrics`, and the
> composition root `internal/app` (all reachable from `cmd/server/main.go`, module `github.com/aero/aero-id`).
> A sibling spec (`2026-08-06-cmd-migrate-tokensource-invalidate-on-401.md`) covers the same connector seam under
> the `cmd/migrate` campaign label; this spec is the `cmd/server` direction and additionally requires the
> **binding-unhealthy short-circuit + routing to the existing `audit.binding_unavailable` branch** (T-10.2), which
> the sibling spec does not include.

| | |
|---|---|
| Module | `cmd/server` (campaign label) → target: `internal/connector/auditgovernance`, `internal/service`, `internal/pkg/metrics`, `internal/app` |
| Direction | "TokenSource invalidation on 401 with epoch single-flight backfill and scope-missing alerting" |
| Source analysis | `docs/auto/analyses/cmd-server-7a3bfea7.json` (direction #2) |
| Date | 2026-08-06 |
| Scope | (1) `Invalidate` + token epoch on `tenantTokenSource`; (2) epoch-keyed singleflight refetch; (3) 401 → invalidate in `Publisher.Publish`; (4) 403 → binding-unhealthy mark + short-circuit + `ErrTokenScopeMissing` sentinel; (5) service routing to the existing `pending`/`audit.binding_unavailable` branch; (6) distinct scope-missing counter/alert metric + its wiring in `cmd/server`. Nothing else. |

## 1. Problem statement

`tenantTokenSource` (internal/connector/auditgovernance/oauth.go) caches a client-credentials bearer token until
`refreshAt` (`cacheToken` at oauth.go:210-218 caps the lifetime at `min(expires_in, 86400s)` minus `refreshSkew`).
There is no invalidation path. When the governance events endpoint rejects a publish with HTTP 401 (server-side
token revocation/rotation), `Publisher.Publish` (publisher.go:83-97) surfaces a generic `audit governance: HTTP 401`
error (`validateReceipt`, publisher.go:220-237, accepts only 202) and every subsequent drain attempt reuses the
same rejected cached token until expiry. The durable drain (`callbackService.drainPublications`,
internal/service/callback_audit.go:47-89) therefore retries with a dead token and dead-letters the backlog after
`MaxAttempts` (default 8, callback.go:76-78) via `publicationOutcome` (callback_audit.go:84-94) — compliance facts
can be lost for up to a day with no operator signal.

Separately, a 403/`insufficient_scope` (token issued without the pinned `audit:event:write` scope — scope is
enforced only at config time by `validOAuthBinding`, oauth.go:86-89, never verified at runtime) is
indistinguishable from a transient failure: it follows the same `pending`→`dead_letter` path and silently storms
retries with the same misconfigured token. No scope-missing alert exists (verified: no `Invalidate` and no alert
symbol anywhere in `internal/`).

Three concrete gaps, all verified against the repository (Section 2):

1. **No `Invalidate`/epoch mechanism.** `tenantTokenSource` exposes only `cachedToken`/`fetchToken`/`cacheToken`;
   the `singleflight.Group` at `s.fetches` (oauth.go:40, `DoChan` at :126) dedupes fetches but cannot force a
   backfill after invalidation, and a pre-invalidation in-flight fetch can repopulate the cache after invalidation.
2. **401 and 403 are indistinguishable from any other failure.** `validateReceipt` (publisher.go:220-237) accepts
   only 202; every other status becomes `fmt.Errorf("audit governance: HTTP %d", ...)` (publisher.go:224) with the
   response body discarded.
3. **No scope-missing alert and no unhealthy-binding state.** The existing `ObserveAuditIngestFailure` hook
   (control_plane.go:97-99) fires only on ledger-append failure (audit_ledger.go:73); publish failures are silent.
   The only path to `pending`/`audit.binding_unavailable` is the unbound-tenant sentinel `errAuditTenantUnbound`
   (callback_audit.go:13, :88-90), reachable only via `CanPublishTenant` (callback_audit.go:105-107).

## 2. Evidence verification (all citations re-checked against the repository on 2026-08-06)

The analysis's line numbers are from an older revision; symbols are all present. Actual current lines:

| # | Cited evidence | Verification result |
|---|---|---|
| E1 | `tenantTokenSource.cacheToken` caches until `refreshAt`; no invalidation hook | **Verified.** Struct at oauth.go:30-41 (`token string` :38, `refreshAt time.Time` :39, `fetches singleflight.Group` :40). `cachedToken` at :147-151 returns `(s.token, s.token != "" && s.now().Before(s.refreshAt))` — the only way to drop the token is expiry. `cacheToken` at :210-218: `lifetime = min(expires_in, 86400s)`, `refreshAt = now + lifetime - min(refreshSkew, lifetime/2)`. `Invalidate` grep across `internal/` → **zero matches**; no `epoch` field exists anywhere. |
| E2 | `s.fetches.DoChan` singleflight exists but cannot backfill after invalidation | **Verified.** `accessToken` at :122-145: `s.fetches.DoChan("client", ...)` (:126) with a closure that only re-checks `cachedToken` — dedup, no generation/epoch key. `fetchToken` at :153-173 maps any non-200 token response to `ErrTokenUnavailable: HTTP %d`; `cacheToken` is called only from `fetchToken` (:171). `OAuthTokenSource.AccessToken` at :113-120 resolves the per-tenant source. |
| E3 | `validateReceipt` returns `audit governance: HTTP 401` with no invalidation hook | **Verified.** `Publish` at publisher.go:83-97: `authorization` (:103-120) → `newRequest` (:122) → `client.Do` → `validateReceipt`. `validateReceipt` at :220-237: `if response.StatusCode != http.StatusAccepted` → `fmt.Errorf("audit governance: HTTP %d", response.StatusCode)` (:224) after discarding the body (:223). No status classification, no invalidation, no retry loop (retries are the caller's durable drain). |
| E4 | 403/insufficient_scope indistinguishable from transient failure | **Verified.** Same generic error path as E3. Scope is pinned only at config time: `validOAuthBinding` (oauth.go:86-89) requires `binding.Scope == auditScope` (`"audit:event:write"`, :21); `tokenRequest` sends `scope=auditScope` (:176-187) but the returned token's scope claim is never verified at runtime (`decodeTokenResponse` :189-208 reads only `access_token`/`token_type`/`expires_in`). |
| E5 | Dead-letter storm via `publicationOutcome` | **Verified.** `publicationOutcome` at callback_audit.go:84-94: only `errors.Is(err, errAuditTenantUnbound)` routes to `"pending"`/`"audit.binding_unavailable"` (:88-90); any other error retries as `pending`/`audit.publication_failed` until `attempt >= maxAttempts` (default 8, callback.go:76-78) then `"dead_letter"` (:92). `publishAuditFact` at :97-112 calls `publisher.Publish` (:108); the only unbound check is `CanPublishTenant` (:105-107) returning `errAuditTenantUnbound` (:106). Drain loop: `callbackService.Start` ticker (callback.go:254-274) → `drainPublications` (callback_audit.go:47-89) → `finishPublicationAttempt` (:67-82). |
| E6 | `ObserveAuditIngestFailure` metric hook exists but covers only append failures | **Verified.** Registry: control_plane.go:97-99 increments `control.auditIngestFail` (:78), rendered at :139-140 as `aero_control_audit_ingest_failures_total{source=%q}`. Service seam: `ControlPlaneMetrics` interface (control_plane_metrics.go:13-24), noop impl (:26-40), `RegistryControlPlaneMetrics` adapter (:53-80). Called only on `repo.Append` failure (audit_ledger.go:73); publish errors are swallowed by the inline loop (`_ = publisher.Publish`, audit_ledger.go:77-80) or returned to the drain without any metric. |
| E7 | **Wiring gap:** `callbackService` has the metrics seam but `cmd/server` never attaches it | **Verified.** `callbackService.metrics ControlPlaneMetrics` field (callback.go:66, default `noopControlPlaneMetrics{}` :90) and `SetControlPlaneMetrics` (:101-104) exist; `internal/app/new.go` wires `controlMetrics := service.NewRegistryControlPlaneMetrics(metricsReg)` (:96) but calls `SetControlPlaneMetrics` only on the ledger service (:148). **No call on `callbackSvc`** — a scope-missing counter emitted on the drain path would be a noop in production unless this is added. |
| E8 | `TokenSource` interface is the seam; single implementer | **Verified.** `TokenSource` (types.go:58-61): `AccessToken(context.Context, SourceBinding) (string, error)` + `HasBinding(string) bool`. Implemented only by `OAuthTokenSource` (oauth.go:25-27; verified: no other implementer in the module, no test fakes outside `publisher_test.go` which uses `NewOAuthTokenSource`). `Publisher.tokens TokenSource` (publisher.go:28). Extending the interface breaks nothing. |
| E9 | Composition root constructs the publisher | **Verified.** `internal/app/audit.go:38-68` `buildAuditPublishers`: `NewOAuthTokenSource` (:50-54) and `NewPublisher` (:58-63); `wireAuditServices` (app/audit.go:17-36) hands the publisher to `callbackSvc.SetAuditPublishers`. Enabled only when `cfg.AuditGovernance.Enabled` (:39-41); no metrics are injected into the connector today. |
| E10 | Test precedent | **Verified.** `publisher_test.go` (169 lines, 5 tests) uses `httptest` servers with `/token` + `/api/v1/events` handlers and `auditBinding` helper (:164-169); tests are in-package (`package auditgovernance`), so `tokens.bindings[tenantID].cachedToken()` is directly assertable. `tenantTokenSource.now` is injected (`now: func() time.Time { return time.Now().UTC() }`, oauth.go:63-64) — cooldown expiry is testable without sleeping. Service tests (`callback_audit_test.go`) already assert `publicationOutcome` branches directly. |

## 3. Requirements

### REQ-1 — Invalidation seam with token epoch (`internal/connector/auditgovernance`)

- `tenantTokenSource` gains an `epoch uint64` field (oauth.go:30-41).
- Add `Invalidate(binding SourceBinding) error`, `BindingUnhealthy(binding SourceBinding) bool`, and
  `MarkBindingUnhealthy(binding SourceBinding)` to the `TokenSource` interface (types.go:58-61); implement on
  `OAuthTokenSource` (resolves the per-tenant source; unbound tenant is a no-op) and `tenantTokenSource`.
- `Invalidate`, under `mu.Lock`: set `s.token = ""` and `s.epoch++`. After `Invalidate`, `cachedToken()` MUST
  return `("", false)` (oauth.go:150) — the very next `AccessToken` performs a fresh fetch. `refreshAt` may be
  left as-is; the `token != ""` conjunct is the authority.
- `MarkBindingUnhealthy` sets `s.unhealthyUntil = s.now().Add(scopeMissingCooldown)`; `BindingUnhealthy` returns
  `s.now().Before(s.unhealthyUntil)`. New package constant `scopeMissingCooldown = 5 * time.Minute` (recovery
  probe cadence; not a config key). The existing injected `now` (oauth.go:63-64) makes this testable.
- Token cache TTL policy (24 h cap, `refreshSkew`, oauth.go:210-218) is unchanged; the epoch is an invalidation
  guard, not a new expiry mechanism.

### REQ-2 — Epoch-keyed single-flight backfill (exactly one refetch under N concurrent callers)

- `accessToken` (oauth.go:122-145) reads `s.epoch` under `RLock` and keys the singleflight group by
  `"client-"+strconv.FormatUint(epoch, 10)` instead of the constant `"client"` (:126). Callers that start after
  `Invalidate` therefore join a **new** flight and coalesce into exactly one refetch; a pre-invalidation in-flight
  fetch is never deduped against a post-invalidation one.
- The flight closure captures the epoch and passes it to `fetchToken`; `cacheToken(wire, epoch)` (oauth.go:210-218)
  discards the result under `Lock` when `epoch != s.epoch`, so a stale pre-invalidation fetch can never repopulate
  the cache after `Invalidate` (no poisoned re-entry). `fetchToken` has exactly one caller (the closure, :128), so
  its signature change is safe.
- The rejected token is stored only in `s.token`; after `Invalidate` no code path can return it (guarantees
  "a stale-token retry never reuses the rejected token").

### REQ-3 — Publisher invalidates on 401 (`internal/connector/auditgovernance/publisher.go`)

In `Publisher.Publish` (publisher.go:83-97):

- Compute the `SourceBinding{TenantID: fact.TenantID, SourceSystem: source}` (thread it out of `authorization`,
  :103-120, which already computes `source`).
- **Before** the HTTP call: if `p.tokens.BindingUnhealthy(binding)` return
  `fmt.Errorf("%w: binding unhealthy", ErrTokenScopeMissing)` — no token fetch, no event POST (storm suppression,
  REQ-4).
- **After** `client.Do`: on `response.StatusCode == http.StatusUnauthorized` call
  `p.tokens.Invalidate(binding)` and return `fmt.Errorf("%w: HTTP %d", ErrTokenRejected, response.StatusCode)`;
  on `http.StatusForbidden` call `p.tokens.MarkBindingUnhealthy(binding)` and return
  `fmt.Errorf("%w: HTTP %d", ErrTokenScopeMissing, response.StatusCode)`.
- All other statuses keep the existing `validateReceipt` path (publisher.go:220-237) unchanged.
- New sentinels in types.go alongside the existing `Err*` set (types.go:11-17): `ErrTokenRejected`
  ("audit governance: token rejected") and `ErrTokenScopeMissing` ("audit governance: token scope missing").
- Invalidation is status-driven (any 401), independent of the response body. No retry loop is added inside
  `Publish`: the retry that benefits from the refetch is the caller's durable drain (E5).
- 401 and 403 are deliberately asymmetric: 401 clears the cache (self-healing refetch); 403 does **not** clear the
  cache (the token is valid but wrong-scoped — refetching cannot fix an issuer-side scope omission) and instead
  parks the binding in the unhealthy state with a bounded probe cadence.

### REQ-4 — 403/insufficient_scope marks the binding unhealthy and suppresses the retry storm

- On the first 403 the binding enters the unhealthy state for `scopeMissingCooldown` (REQ-1). While unhealthy,
  `Publisher.Publish` short-circuits (REQ-3) — a stuck binding produces at most one network probe per cooldown
  instead of a per-tick storm, and never refetches the token.
- After the cooldown expires, the next publish is a real probe: 202 → healthy (no re-mark, no counter); 403 →
  re-marked + sentinel + counter (alert re-fires). Recovery is automatic and requires no operator action and no
  restart (state is in-memory; a process restart also clears it).

### REQ-5 — Service routes scope-missing to the existing binding-unavailable branch (`internal/service/callback_audit.go`)

- Extend the `tenantBoundAuditPublisher` interface (callback_audit.go:15-17) with `BindingUnhealthy(string) bool`.
- In `publishAuditFact` (:97-112), extend the existing pre-check (:105-107):
  `if !bound.CanPublishTenant(fact.TenantID) || bound.BindingUnhealthy(fact.TenantID) { return errAuditTenantUnbound }`.
  Suppressed attempts therefore take the **existing** branch — `publicationOutcome` maps `errAuditTenantUnbound`
  to `"pending"`/`"audit.binding_unavailable"` (callback_audit.go:88-90) — with no network traffic and no counter
  inflation.
- After a fresh `publisher.Publish` error: if `errors.Is(err, auditgovernance.ErrTokenScopeMissing)`, call
  `s.metrics.ObserveAuditScopeMissing(strings.TrimSpace(fact.Source))` and return `errAuditTenantUnbound`
  (same branch). Importing the connector sentinel in the service layer is precedented (billing.go, aggregation.go,
  sync_job.go import `internal/connector/...`; no import cycle — auditgovernance imports only `internal/domain`).
- `publicationOutcome` (:84-94) is unchanged — the "existing branch" is reused verbatim.
- The dead inline loop in `auditLedgerService.Record` (`_ = publisher.Publish`, audit_ledger.go:77-80) is **not**
  touched (it is direction #1 of the same analysis, §5), but it calls the same `Publisher.Publish`, so 401/403
  handling and the short-circuit apply there too.

### REQ-6 — Distinct scope-missing counter/alert metric, live in `cmd/server`

- `internal/pkg/metrics/control_plane.go`: add `auditScopeMissing counterMap` (label `source`) to
  `controlPlaneMetrics` (:52-66), `Registry.ObserveAuditScopeMissing(source string)` following the
  `ObserveAuditIngestFailure` pattern (:97-99), and a render line in `writeControlPlaneText` (:139-140) —
  **`aero_control_audit_scope_missing_total{source=%q}`** — distinct from
  `aero_control_audit_ingest_failures_total`.
- `internal/service/control_plane_metrics.go`: add `ObserveAuditScopeMissing(source string)` to the
  `ControlPlaneMetrics` interface (:13-24), the noop impl (:26-40), and the `RegistryControlPlaneMetrics` adapter
  (:53-80).
- **Alert semantics:** the repository has no alerting subsystem (verified: zero `alert` symbols in `internal/`);
  the established alert surface is the control-plane metric families on `/metrics` (AGENTS.md required-metrics
  list). "Emits an alert" is therefore realized as this dedicated, monotonically increasing counter family — the
  alert rule target — incremented exactly once per network-detected 403 (per tenant per cooldown). It is distinct
  from the generic ingest-failure counter so a misconfiguration is immediately distinguishable.
- **Wiring fix (required for the counter to be live):** `internal/app/new.go` must call
  `callbackSvc.SetControlPlaneMetrics(controlMetrics)` (method exists at callback.go:101-104; verified currently
  never called — E7). Place it alongside the ledger wiring (:146-150), before `callbackSvc.Start` (:179).
- No config keys, no migrations, no proto changes; the connector stays dependency-free (no metrics import — the
  counter is emitted at the service seam, which already has the registry adapter).

## 4. Acceptance checks (supplied checks preserved; made testable)

All tests are in-package unit tests (`package auditgovernance` / `package service` / `package metrics`) using
`httptest` servers per E10 with a counting `/token` handler; no real network, no Docker, no testify (stdlib only).

**T-10.1 — 401 invalidates the cached token and bumps the epoch; N concurrent callers coalesce into exactly one
refetch; a stale-token retry never reuses the rejected token.**

- **AT-1 (invalidate + epoch bump):** prime the cache with one successful publish (server: `/token` → valid token,
  hit counter = 1; `/api/v1/events` → 202 with matching receipt). Then make `/api/v1/events` return 401 once.
  Assert: (a) `Publish` fails with `errors.Is(err, ErrTokenRejected)`; (b) in-package access:
  `tokens.bindings[tenantID].cachedToken()` returns `("", false)`; (c) `s.epoch` incremented by exactly 1.
- **AT-2 (singleflight coalescing, counting fake token server):** after the 401 above, run N ≥ 8 concurrent
  `AccessToken` calls (or `Publish` calls against a now-202 events endpoint). Assert: (a) `/token` hit counter ==
  **2** total (one refetch serving all N); (b) all N returned tokens are equal to the post-invalidation token and
  never equal to the rejected token string; (c) all N governance requests carry `Authorization: Bearer
  <refetched-token>`.
- **AT-3 (stale in-flight fetch cannot repopulate):** start a `Publish` while the `/token` handler blocks on a
  channel; from another goroutine trigger a 401 on a second publish (or call `Invalidate` directly), then release
  the blocked `/token` handler with a token. Assert `cachedToken()` stays `("", false)` (epoch mismatch discarded
  the stale fetch result, REQ-2).
- **AT-4 (stale-token retry):** same fact: first `Publish` → 401, second `Publish` (same fact, events now 202) →
  nil. Assert the second request's `Authorization` header equals the token served by `/token` after invalidation
  and that the rejected token string appears nowhere in the second exchange. The idempotency key
  (`IdempotencyKey = fact.ID.String()`, publisher.go:185) is unchanged across both attempts (governance-side dedup
  contract undisturbed).

**T-10.2 — 403/insufficient_scope marks the binding unhealthy, routes publications to `pending` with code
`audit.binding_unavailable` (existing branch), increments a scope-missing counter, and emits an alert while
suppressing the retry storm.**

- **AT-5 (403 detection):** events endpoint returns 403 with body `{"error":"insufficient_scope"}`. Assert:
  (a) `Publish` fails with `errors.Is(err, ErrTokenScopeMissing)`; (b) `tokens.bindings[tenantID].BindingUnhealthy(binding)`
  is true; (c) `cachedToken()` still returns the token (403 must NOT clear the cache — asymmetry with 401, REQ-3);
  (d) `s.epoch` unchanged.
- **AT-6 (storm suppression):** while the binding is unhealthy, repeat `Publish` (same fact) and assert both
  `/token` and `/api/v1/events` hit counters do not move and each call returns `ErrTokenScopeMissing`.
- **AT-7 (routing to the existing branch):** service-level test with a fake `AuditPublisher` implementing
  `tenantBoundAuditPublisher`: (a) `BindingUnhealthy()==true` → `publishAuditFact` returns an error satisfying
  `errors.Is(err, errAuditTenantUnbound)` and `publicationOutcome(err, attempt, 8, now, delay)` returns
  `("pending", "audit.binding_unavailable", now.Add(delay))` — the exact existing branch (callback_audit.go:88-90);
  (b) fresh 403 (`BindingUnhealthy()==false` pre-check, `Publish` returns `ErrTokenScopeMissing`) → same routing
  AND a metrics spy records exactly one `ObserveAuditScopeMissing(source)` call with the fact's source; suppressed
  (pre-checked) attempts record zero.
- **AT-8 (cooldown probe and recovery):** with the injected `now` (E10), advance past `scopeMissingCooldown`;
  assert the next `Publish` performs exactly one network probe (events hit counter +1) and: (a) events still 403 →
  `ErrTokenScopeMissing` again, binding re-marked, counter incremented again (alert re-fires); (b) events now 202 →
  `Publish` returns nil, no counter increment, binding left to expire naturally.
- **AT-9 (metric family):** registry test: after `ObserveAuditScopeMissing("aero-id")` the rendered text contains
  `aero_control_audit_scope_missing_total{source="aero-id"}`; the family is distinct from
  `aero_control_audit_ingest_failures_total` (observing one never affects the other). Negative: a non-403 failure
  (e.g. 500 on the events endpoint, transport error) neither marks the binding unhealthy nor increments the
  counter.
- **AT-10 (composition root):** `internal/app/new.go` calls `callbackSvc.SetControlPlaneMetrics(controlMetrics)`
  (verified by inspection; without it AT-7's counter is a noop in `cmd/server` — E7).

## 5. Out of scope (explicit)

- **Direction #1 of `cmd-server-7a3bfea7.json`** (complete in-tx audit writes: remove the dead inline
  `_ = publisher.Publish` loop at audit_ledger.go:77-80, AppendInTx/XxxWithAudit consolidation, `check-audit-swallow`
  gate): not touched; the inline loop is mentioned only because it shares `Publisher.Publish`.
- **Direction #3** (stable anchored `event_id` + dead-letter replay `RequeueDead`/`cmd/audit-replay`/purge pause):
  not touched. `IdempotencyKey = fact.ID.String()` stays exactly as-is (AT-4 asserts it).
- No retry loop inside `Publisher.Publish`; no changes to the 202/receipt contract, token TTL policy, or
  `validateReceipt` semantics beyond the 401/403 branch in `Publish`.
- No response-body parsing of governance error envelopes (the 401/403 classification is status-driven; a
  body-parse refinement is possible later and must not block invalidation — not required here).
- No new configuration keys (cooldown is a package constant), no migrations, no proto changes, no connector
  metrics import (counter emitted at the service seam), no changes to `internal/connector/billing` (its 401/403
  classification is precedent only).

## 6. Engineering constraints and done gate

- File limits (non-test): oauth.go 220 lines, publisher.go 258, types.go 116, callback_audit.go 113,
  control_plane.go 176, control_plane_metrics.go 81 — all have headroom; REQ-1/REQ-2 must keep oauth.go ≤ 500 and
  REQ-3 must keep publisher.go ≤ 500 (split into sibling files at ~400 lines per AGENTS.md if needed).
- Function limits: `Publish`, `accessToken`, `publishAuditFact`, and the new invalidate/mark helpers must stay
  ≤ 50 lines and cyclomatic ≤ 10; package average cyclomatic ≤ 5.0.
- New sentinels live in types.go alongside the existing `Err*` set; connector stays free of service/repository
  imports (imports `internal/domain` only).
- Done gate: `make test && make lint && make lint-strict && make check-file-sizes && make build`. No
  `make test-integration` run required (no SQL/storage/migration change, and connector tests use `httptest`
  loopback servers per the existing pattern); changed connector/service/metrics tests must pass under `make test`.
