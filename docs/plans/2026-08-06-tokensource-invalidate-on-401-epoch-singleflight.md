# Design — audit governance TokenSource: invalidate-on-401 with epoch single-flight and scope-missing alerting

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-06-cmd-migrate-tokensource-invalidate-on-401.md` (REQ-1..REQ-6) |
| Module (batch label) | `cmd/migrate` (effective scope `internal/connector/auditgovernance` + `internal/pkg/metrics` + `internal/service`) |
| Source analysis | `docs/auto/analyses/cmd-migrate-c1b3771d.json` (direction #3; directions #1/#2 are separate campaigns) |
| Status | Design — evidence re-verified against live tree on 2026-08-06 |

## 1. Evidence verification result (untrusted claims re-checked)

Every symbol in the requirements evidence was re-checked against the live tree. **All substantive claims
hold.** Two minor line-number drifts and two additional facts are folded in; none change the design.

| # | Cited claim | Verdict |
|---|---|---|
| E1 | `tenantTokenSource` struct at oauth.go:34-42 (`token`, `refreshAt`, `mu`, `fetches`); `accessToken` :122-145 with `s.fetches.DoChan("client", …)` at :126 whose closure only re-checks `cachedToken`; `cachedToken` :147-151; `fetchToken` :153-173; `cacheToken` :210-217 caps at `min(expires_in, 86400)` minus `min(refreshSkew, lifetime/2)`; **zero `Invalidate` matches** in `internal/connector/` | ✅ exact. `wc -l` = 217 as cited. Grep `Invalidate` across `internal/connector/` = 0 hits. No `epoch` field exists |
| E2 | `Publish` publisher.go:83-101 → `authorization` :103-120 → `client.Do` → `validateReceipt` :220-237; only 202 accepted; :224 `fmt.Errorf("audit governance: HTTP %d", …)`; body discarded :223 (`io.Copy(io.Discard, io.LimitReader(…, maxReceiptBytes))`); `IdempotencyKey: fact.ID.String()` at :185; `maxReceiptBytes` at :20; `Publisher.tokens` typed `TokenSource` | ✅ exact |
| E3 | Billing precedent client.go:277-282: `invalid_token`/`insufficient_scope`/`metering_source_unauthorized` → `ErrUnauthorized`, and `status == 401 \|\| status == 403` → `ErrUnauthorized`; billing's own oauth.go has **no** invalidation (precedent is classification only) | ✅ exact. `decodeBillingError` spans :270-282; classification at :277-282 as cited. Spec does not overclaim the precedent |
| E4 | `Registry.ObserveAuditIngestFailure` at control_plane.go:97-99; rendered :141 as `aero_control_audit_ingest_failures_total{source=%q}`; `counterMap` machinery at :23-45 | ✅ exact |
| E5 | Service seam control_plane_metrics.go: interface at :15 (includes `ObserveAuditIngestFailure`), `noopControlPlaneMetrics` at :29, `RegistryControlPlaneMetrics.ObserveAuditIngestFailure` at :55-56 | ✅ exact |
| E6 | `Record` at audit_ledger.go:68-80; `ObserveAuditIngestFailure` fires only on `repo.Append` error at :73; inline `_ = publisher.Publish(ctx, fact)` loop at :79 swallows publish errors | ✅ exact |
| E7 | Durable drain: `drainPublications` callback_audit.go:47-71, `finishPublicationAttempt` :73-82, `publicationOutcome` :84-95, `publishAuditFact` :97-112; `callbackService.metrics` field at callback.go:66, `SetControlPlaneMetrics` :99-103, ticker calls `s.drainPublications(workerCtx)` at :266 inside the :261-267 loop; wired at new.go:148 | ✅ exact (spec cited the ticker at :267; the call sits at :266 within the loop — immaterial) |
| E8 | `buildAuditPublishers` app/audit.go:38-67 constructs `NewOAuthTokenSource` (:50) and `NewPublisher` (:58); `SetAuditPublishers` :26-35; same publisher slice goes to ledger and callback service; no metrics injected into the connector | ✅ exact |
| E9 | Test precedent: `TestPublisherUsesDerivedSourceAndMatchingLedgerReceipt` at publisher_test.go:15 with `/token` + `/api/v1/events` httptest handlers; `auditBinding` helper :164-169; tests in-package | ✅ exact |
| — | `TokenSource` interface cited at types.go:75-79 | ✅ content exact (only `AccessToken` + `HasBinding`), **drift**: actually at types.go:58-61 |
| — | `domain.AuditFact.Source` exists for the REQ-5 emission key | ✅ exact (audit_ledger.go:9) |

**Additional facts verified and used below:**

- **F1 — single implementer.** `OAuthTokenSource` is the only implementer of `TokenSource`; no fakes in
  tests (`publisher_test.go` uses the real source via `NewOAuthTokenSource`). Adding a method to the
  interface breaks nothing in-repo.
- **F2 — no test asserts the generic 401 error text.** `"audit governance: HTTP %d"` is asserted nowhere
  outside publisher.go, so replacing the 401 variant with `ErrTokenRejected` breaks no existing test.
- **F3 — no test fakes implement `ControlPlaneMetrics`.** Only `internal/pkg/metrics/metrics_test.go`
  references it (against the real `Registry`); adding `ObserveTokenScopeMissing` to the interface breaks no
  fakes.
- **F4 — service→connector import precedent exists.** `internal/service` already imports
  `internal/connector/billing` (billing.go:11) and `internal/connector` (aggregation.go:9,
  legacy_migration.go:8); REQ-5's new `auditgovernance` import in `callback_audit.go` follows that pattern.
- **F5 — metrics render precedent.** `metrics_test.go:133-144` asserts
  `aero_control_audit_ingest_failures_total{source="aero-id"} 1` in `WriteText` output; the new counter
  follows the same assertion style.
- **F6 — no `check-audit-swallow` Makefile target** (direction #2 still absent, as claimed); the done gate
  for this design is unchanged from §6 of the spec.
- **F7 — body close ownership.** `validateReceipt` owns `defer response.Body.Close()` today (publisher.go:221)
  and is called only from `Publish`. The design moves close ownership to `Publish` (see §2.3) so the new
  classification path cannot leak response bodies.

## 2. API changes

### 2.1 `internal/connector/auditgovernance/types.go` — interface + sentinels (REQ-1, REQ-3, REQ-4)

```go
var (
    // existing ErrInvalidConfig / ErrInvalidEvent / ErrInvalidReceipt /
    // ErrTokenUnavailable / ErrTenantUnbound unchanged
    ErrTokenRejected     = errors.New("audit governance: token rejected")
    ErrTokenScopeMissing = errors.New("audit governance: token scope missing")
)

type TokenSource interface {
    AccessToken(context.Context, SourceBinding) (string, error)
    HasBinding(string) bool
    Invalidate(SourceBinding) // void: best-effort, never fails a publish path
}
```

`Invalidate` is void by design — an invalidation is a cache-side cleanup that must never turn a publish
failure into a different failure. The publisher already has the binding (`authorization` computes
`SourceBinding{TenantID, SourceSystem: source}`); it is passed through unchanged.

### 2.2 `internal/connector/auditgovernance/oauth.go` — epoch field, invalidate, epoch-guarded fetch (REQ-1, REQ-2)

```go
type tenantTokenSource struct {
    // …existing fields unchanged…
    epoch uint64 // bumped on every Invalidate; guards stale in-flight fetches
}

// OAuthTokenSource-level: mirrors AccessToken's binding validation; unbound
// tenant or source mismatch is a no-op (REQ-1).
func (s *OAuthTokenSource) Invalidate(binding SourceBinding) {
    expected, err := TenantSourceID(s.sourcePrefix, binding.TenantID)
    tokens := s.bindings[binding.TenantID]
    if err != nil || binding.SourceSystem != expected || tokens == nil {
        return
    }
    tokens.invalidate()
}

func (s *tenantTokenSource) invalidate() {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.token = ""
    s.refreshAt = time.Time{} // zero time ⇒ cachedToken() = ("", false)
    s.epoch++
}

func (s *tenantTokenSource) accessToken(ctx context.Context) (string, error) {
    if token, ok := s.cachedToken(); ok {
        return token, nil
    }
    s.mu.RLock()
    epoch := s.epoch
    s.mu.RUnlock()
    result := s.fetches.DoChan(fmt.Sprintf("client-%d", epoch), func() (any, error) {
        if token, ok := s.cachedToken(); ok {
            return token, nil
        }
        return s.fetchToken(ctx, epoch)
    })
    // …existing select unchanged…
}

func (s *tenantTokenSource) fetchToken(ctx context.Context, epoch uint64) (string, error) {
    // …existing fetch/decode unchanged…
    s.cacheToken(wire, epoch)
    return wire.AccessToken, nil
}

func (s *tenantTokenSource) cacheToken(wire tokenResponse, epoch uint64) {
    lifetime := time.Duration(min(wire.ExpiresIn, int64((24*time.Hour)/time.Second))) * time.Second
    skew := min(s.refreshSkew, lifetime/2)
    s.mu.Lock()
    defer s.mu.Unlock()
    if epoch != s.epoch {
        return // stale pre-invalidation fetch: never repopulate (REQ-2)
    }
    s.token = wire.AccessToken
    s.refreshAt = s.now().Add(lifetime - skew)
}
```

Invariant: **the `cacheToken` epoch check under `mu.Lock` is the authoritative anti-poisoning guard**; the
singleflight keying is the dedup mechanism. A stale fetch can deliver its result to its own (pre-invalidation)
waiter but can never repopulate the cache.

### 2.3 `internal/connector/auditgovernance/publisher.go` — status classification (REQ-3, REQ-4)

```go
func (p *Publisher) Publish(ctx context.Context, fact *domain.AuditFact) error {
    source, token, err := p.authorization(ctx, fact)
    if err != nil {
        return err
    }
    request, err := p.newRequest(ctx, fact, source, token)
    if err != nil {
        return err
    }
    response, err := p.client.Do(request)
    if err != nil {
        return err
    }
    defer response.Body.Close() // close ownership moves here (F7)
    if response.StatusCode == http.StatusUnauthorized {
        p.tokens.Invalidate(SourceBinding{TenantID: fact.TenantID, SourceSystem: source})
    }
    if err := classifyPublishError(response); err != nil {
        return err
    }
    return validateReceipt(response, fact)
}
```

New helper (≤ 50 lines, cyclomatic ≤ 10):

```go
// classifyPublishError maps non-202 statuses to sentinels. 401 is strictly
// status-driven (ErrTokenRejected) and outranks any body code; scope-missing
// codes classify only other non-202 statuses (typically 403). The body is read
// with the receipt bound; parse failure degrades to the generic error.
func classifyPublishError(response *http.Response) error {
    if response.StatusCode == http.StatusAccepted {
        return nil
    }
    body, _ := io.ReadAll(io.LimitReader(response.Body, maxReceiptBytes+1))
    if response.StatusCode == http.StatusUnauthorized {
        return fmt.Errorf("%w: HTTP 401", ErrTokenRejected)
    }
    var envelope struct {
        Code string `json:"error"`
    }
    if len(body) <= maxReceiptBytes && json.Unmarshal(body, &envelope) == nil {
        switch envelope.Code {
        case "insufficient_scope", "invalid_scope":
            return fmt.Errorf("%w: HTTP %d", ErrTokenScopeMissing, response.StatusCode)
        }
    }
    return fmt.Errorf("audit governance: HTTP %d", response.StatusCode)
}
```

`validateReceipt` drops its own `defer response.Body.Close()` (now owned by `Publish`); its 202-only
semantics, `decodeReceipt`, and `receiptMatches` are untouched. Precedence rule (design decision): **401 ⇒
`ErrTokenRejected` regardless of body**; scope codes are read only on other statuses. This keeps T-4's
`errors.Is(err, ErrTokenRejected)` assertion unambiguous for any 401, and matches realistic governance
behavior (401 = bad/revoked token, 403 = scope problem). `errors.Join` of both sentinels was considered and
rejected: it complicates every `errors.Is` call site for no operational gain.

### 2.4 `internal/pkg/metrics/control_plane.go` — distinct counter (REQ-5)

- `controlPlaneMetrics` gains `tokenScopeMissing counterMap` (comment: `// source -> scope-missing publish responses`).
- `Registry.ObserveTokenScopeMissing(source string)` — `r.control.tokenScopeMissing.add(source, 1)`.
- `writeControlPlaneText` gains the render loop following the :136-145 convention:

```go
for key, value := range r.control.tokenScopeMissing.snapshot() {
    fmt.Fprintf(w, "aero_control_audit_scope_missing_total{source=%q} %d\n", key, value)
}
```

Independent `counterMap` ⇒ zero interaction with `aero_control_audit_ingest_failures_total` (test-verifiable).

### 2.5 `internal/service/control_plane_metrics.go` — seam (REQ-5)

- Interface at :15 gains `ObserveTokenScopeMissing(source string)`.
- `noopControlPlaneMetrics` gains the no-op method (so unwired services stay silent, as today).
- `RegistryControlPlaneMetrics` gains the one-line adapter.

### 2.6 `internal/service/callback_audit.go` — emission on the durable drain path (REQ-5)

In `publishAuditFact` (:97-112), on publisher error:

```go
if err := publisher.Publish(ctx, fact); err != nil {
    if errors.Is(err, auditgovernance.ErrTokenScopeMissing) && s.metrics != nil {
        s.metrics.ObserveTokenScopeMissing(strings.TrimSpace(fact.Source))
    }
    return err
}
```

New import `github.com/aero/aero-id/internal/connector/auditgovernance` (precedent F4). Emission happens on
the retryable drain path only — not in `auditLedgerService.Record`'s inline loop (that loop is direction
#1's removal target; the same `Publish` still invalidates there, which is what fixes the silence root cause
on that legacy path).

## 3. Compatibility constraints

| # | Constraint | Consequence |
|---|---|---|
| C-1 | `TokenSource` gains a method; interface is `internal/` with exactly one implementer (F1) | No compile break anywhere; no external implementers possible |
| C-2 | 202/receipt contract untouched: `validateReceipt` acceptance, `decodeReceipt`, `receiptMatches`, `IdempotencyKey = fact.ID.String()` (publisher.go:185) | Retried publishes carry identical keys; governance-side dedup is the authority's contract (T-10.1) |
| C-3 | Non-401/non-scope error text `"audit governance: HTTP %d"` unchanged; no test asserts the 401 text (F2) | Only new sentinels added; no existing assertion breaks |
| C-4 | Body close ownership moves from `validateReceipt` to `Publish` | Internal only; no observable change; prevents body leaks on the new classification path |
| C-5 | No config keys, no migrations, no proto changes, no connector→metrics dependency | Connector stays dependency-free; metric emitted at the service seam only |
| C-6 | New `service → connector/auditgovernance` import | Follows existing precedent (F4) |
| C-7 | Token TTL policy unchanged (24 h cap, `min(refreshSkew, lifetime/2)`) | Epoch is an invalidation guard, not a new expiry mechanism |
| C-8 | Metric name follows `aero_control_*_total{source=…}` convention and is distinct from `aero_control_audit_ingest_failures_total` | Independent counterMap; render-distinctness is tested |
| C-9 | `Invalidate` is void and best-effort | Never turns a publish failure into a different failure; unbound/mismatched binding is a no-op |

## 4. Failure modes

| # | Scenario | Behavior | Correctness argument |
|---|---|---|---|
| FM-1 | In-flight fetch (epoch e0) completes **after** `Invalidate` bumped to e1 | `cacheToken(wire, e0)` sees `e0 != s.epoch`, discards; the e0-group waiter still receives the token string (uncached) | No poisoning re-entry: cache stays empty; next `AccessToken` fetches fresh |
| FM-2 | `Invalidate` lands between the `cachedToken` miss and the epoch read (or between read and `DoChan`) | Caller joins the e0 group; its fetch result is discarded by the guard (FM-1) or, if it started a fresh e0 fetch, also discarded | At most one extra token-endpoint call; the drain's next tick converges. No correctness loss, only a wasted fetch in a rare window |
| FM-3 | Token **endpoint** returns 401/403 (rotated client secret, revoked client) | `fetchToken` → `ErrTokenUnavailable: HTTP …`; **no invalidation** (nothing was cached; the revoked artifact is the grant, not the token) | Correct: invalidation is keyed to the governance endpoint's 401 (REQ-3). Publication stays `pending`, retries with backoff, dead-letters after `MaxAttempts`; observable via `aero_control_callback_deliveries_total{status="dead_letter"}` |
| FM-4 | 401 response whose body also carries a scope code | `ErrTokenRejected` wins (precedence rule §2.3); scope codes classify only non-401 statuses | Keeps T-4's `errors.Is(err, ErrTokenRejected)` unambiguous; matches governance semantics |
| FM-5 | Non-202 body unreadable/oversized/non-JSON | Generic `audit governance: HTTP %d`; for 401 the invalidation already happened | Scope check never blocks invalidation (REQ-4); bounded read (`maxReceiptBytes`) prevents memory abuse |
| FM-6 | `Invalidate` for unbound tenant or mismatched `SourceSystem` | No-op (mirrors `AccessToken`'s `ErrTenantUnbound` validation) | The publisher only invalidates the binding it just used; no cross-tenant effects |
| FM-7 | Process restart | Epoch resets to 0; token cache is process-local and already empty | No cross-restart consistency requirement: epoch is an intra-process guard against stale in-flight fetches, not a fencing mechanism |
| FM-8 | `singleflight.Group` retains the last result per key | One retained result per distinct `"client-<epoch>"` key; epochs advance only on invalidation | Bounded, negligible memory; optional hygiene: `s.fetches.Forget("client-"+old)` inside `invalidate` (safe — in-flight waiters still receive their result). Not required for correctness |
| FM-9 | Scope-missing on the legacy inline path (`audit_ledger.go:79` `_ = publisher.Publish`) | Token is still invalidated (same `Publish` code) but no scope metric fires there | By design (REQ-5 emission is drain-path-only; the inline loop is direction #1's removal target). The 401 invalidation still fixes the silent-token-rot root cause on that path |
| FM-10 | Drain tick with N concurrent publications after a 401 | N concurrent `Publish` → N concurrent `AccessToken` → all join the post-invalidation epoch group → exactly one refetch; N concurrent `Invalidate` calls are idempotent | This is T-4's exact scenario at the service level; bulkhead bounds in-flight publishes (P6-06) |

## 5. Migration steps

No schema/data/config migration exists for this design (C-5). The sequence is implementation order, then
binary rollout.

1. **types.go**: add `ErrTokenRejected`, `ErrTokenScopeMissing`, and `Invalidate(SourceBinding)` to
   `TokenSource`. Compile breaks pinpoint the single implementer (F1).
2. **oauth.go**: add `epoch` field, `OAuthTokenSource.Invalidate` + `tenantTokenSource.invalidate`,
   epoch-keyed `accessToken`, epoch parameters on `fetchToken`/`cacheToken` with the discard guard.
3. **publisher.go**: add the 401 branch and `classifyPublishError`; move body-close ownership to `Publish`;
   strip the now-redundant defer from `validateReceipt`.
4. **metrics**: `control_plane.go` counter + observer + render; `control_plane_metrics.go` interface/noop/adapter.
5. **callback_audit.go**: emission in `publishAuditFact` with the new import.
6. **Tests** (§6), in the same change set: connector tests in `publisher_test.go`, registry render test in
   `metrics_test.go`, service metrics-spy test in `callback_audit_test.go`.
7. **Gate**: `make test && make lint && make lint-strict && make check-file-sizes && make build`. No
   `make test-integration` run required (no SQL/storage/connector-network contract change; all new tests are
   in-package `httptest`).
8. **Rollout**: single binary deploy; no config change; `audit_governance.enabled` untouched. The epoch and
   invalidation are process-local — no multi-instance coordination needed (each instance refetches on its
   own 401).
9. **Ops runbook**: alert on `aero_control_audit_scope_missing_total > 0` (governance client grant/scope
   drift — remediate the OAuth client's `audit:event:write` grant); a rise in
   `aero_control_callback_deliveries_total{status="dead_letter"}` **without** the scope-missing counter
   points at token-endpoint health (FM-3) rather than scope configuration. After a token rotation at the
   governance authority, expect a one-time 401 → refetch → recovery within one drain tick instead of the
   previous up-to-24 h dead-letter window.

## 6. Testable acceptance mapping

All tests are in-package unit tests (`httptest`, no network/Docker), following E9's fixture pattern
(`/token` + `/api/v1/events` handlers, `auditBinding` helper, `/token` hit counter).

| Spec acceptance | Test (file) | Concrete assertions | Proves |
|---|---|---|---|
| T-4 part 1 step 1 | `TestPublish401InvalidatesAndReturnsErrTokenRejected` (publisher_test.go) | Prime cache (1 publish, `/token`=1, 202+receipt); events → 401; assert `errors.Is(err, ErrTokenRejected)` | REQ-3 |
| T-4 part 1 step 2 | same test | Immediately after: `tokens.bindings[tenantID].cachedToken()` returns `("", false)` (in-package access) | REQ-1 |
| T-4 part 1 step 3 | `TestConcurrentPublishesTriggerExactlyOneRefetch` (publisher_test.go) | After the 401, events → 202; N=8 concurrent `Publish` (same fact); assert `/token` hit counter == **2** total and all N succeed | REQ-2 (epoch-keyed singleflight) |
| T-4 part 1 step 4 | same test | Capture every events request's `Authorization` header; assert all N == `"Bearer " + token2` (the post-invalidation token served by the 2nd `/token` hit), never the revoked token | REQ-2 (no poisoned reuse) |
| T-4 part 2 | `TestRetryAfter401CompletesWithRefetchedToken` (publisher_test.go) | 1st `Publish` → 401; 2nd `Publish` (same fact) → 202; assert 2nd request's `Authorization` == refetched token and `Publish` returns nil | REQ-1/REQ-3 (durable-drain retry succeeds) |
| T-10.1 | `Test401RetryPreservesIdempotencyKey` (publisher_test.go) | Server captures both JSON bodies; assert `body["idempotency_key"]` and `body["event_id"]` identical across the 401'd request and the successful retry, both == `fact.ID.String()` | REQ-6 |
| Scope-missing 1 | `TestPublishScopeMissingSentinels` (publisher_test.go) | Events → 403 with `{"error":"insufficient_scope"}` → `errors.Is(err, ErrTokenScopeMissing)`; repeat for `invalid_scope`; also assert a 403 with an unrelated code stays generic | REQ-4 |
| Scope-missing 2 | `TestPublishAuditFactEmitsScopeMissingMetric` (callback_audit_test.go) | `callbackService` with a fake `AuditPublisher` returning `ErrTokenScopeMissing` and a metrics spy implementing `ControlPlaneMetrics`; assert `ObserveTokenScopeMissing(fact.Source)` called exactly once | REQ-5 (service seam) |
| Scope-missing 3 | registry render test (metrics_test.go, following :133-144) | After `ObserveTokenScopeMissing("aero-id")`: `WriteText` output contains `aero_control_audit_scope_missing_total{source="aero-id"} 1`; `aero_control_audit_ingest_failures_total` line is absent/unchanged (zero interaction) | REQ-5 (distinct metric) |
| Scope-missing 4 (negative) | connector: 500 response → generic error, no sentinel; service: spy counter stays 0 for a non-scope error | `errors.Is(err, ErrTokenScopeMissing)` false; spy not called | REQ-4/REQ-5 (no false alerts) |
| REQ-1 no-op clause | `TestInvalidateUnboundTenantNoop` (publisher_test.go; may grow a sibling oauth_test.go) | `Invalidate` with unbound tenant and with wrong `SourceSystem` leaves `cachedToken()` untouched | REQ-1 |
| REQ-2 anti-poisoning (hardening) | `TestInvalidateDuringInFlightFetchDoesNotRepopulate` (publisher_test.go) | Slow `/token` (channel-gated): start `AccessToken`, `Invalidate` mid-flight, release; assert `cachedToken()` still `("", false)` and the returned token is not cached | REQ-2 guard under `cacheToken` |

**Requirement coverage:** REQ-1 (T-4 p1 s2 + no-op test), REQ-2 (T-4 p1 s3/s4 + hardening test), REQ-3
(T-4 p1 s1, T-4 p2), REQ-4 (scope-missing 1 + negative), REQ-5 (scope-missing 2/3/4), REQ-6 (T-10.1 +
existing receipt tests untouched). Existing `TestPublisherUsesDerivedSourceAndMatchingLedgerReceipt` and the
`ErrTenantUnbound`/`ErrInvalidConfig` tests continue to pass unmodified (C-2, C-3).

## 7. Engineering constraints

- File limits (non-test): oauth.go 217 → ~250; publisher.go 258 → ~285; control_plane.go 176 → ~188;
  control_plane_metrics.go 81 → ~95; callback_audit.go 113 → ~118; types.go 121 → ~130. All ≤ 500 with
  headroom; no split needed (revisit at 400 per AGENTS.md).
- Function limits: `Publish` stays ≤ 50 lines / cyclomatic ≤ 10 (11 → ~17 lines); `classifyPublishError`
  is a new helper ≤ 50 lines / cyclomatic ≤ 10; package average cyclomatic ≤ 5.0 enforced by
  `make lint-strict`.
- No repository errors cross into the connector; new sentinels live in types.go beside the existing `Err*` set.
- Done gate: `make test && make lint && make lint-strict && make check-file-sizes && make build`.
