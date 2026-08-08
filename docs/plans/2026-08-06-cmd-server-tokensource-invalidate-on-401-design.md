# Design — cmd/server: TokenSource invalidation on 401 with epoch single-flight backfill, 403 binding-unhealthy short-circuit, and scope-missing alerting

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-06-cmd-server-tokensource-invalidate-on-401.md` (REQ-1..REQ-6, AT-1..AT-10) |
| Module (campaign label) | `cmd/server` (effective scope `internal/connector/auditgovernance`, `internal/service`, `internal/pkg/metrics`, `internal/app`) |
| Source analysis | `docs/auto/analyses/cmd-server-7a3bfea7.json` (direction #2) |
| Date | 2026-08-06 |
| Status | Design — every evidence claim re-verified against the live tree; four design-gate corrections (D1–D4) folded in; no scope change |

## 1. Evidence verification verdict

Every cited symbol was re-checked against the live tree (`go build ./...` clean, `git log` head `13e4676`).
**All eight evidence claims (E1–E8) are true as stated, including the new wiring-gap finding (E7).** Two
immaterial line drifts (noted below) and four design-relevant corrections (D1–D4, §2) were found.

| # | Claim | Verdict (actual current lines) |
|---|---|---|
| E1 | `cacheToken` caches until `refreshAt`, no invalidation | ✅ **Exact.** Struct oauth.go:30-41 (`token` :38, `refreshAt` :39, `fetches` :40); `cachedToken` :147-151 returns `("", false)` unless `token != "" && now().Before(refreshAt)`; `cacheToken` :210-217 caps at `min(expires_in, 86400s)` − `min(refreshSkew, lifetime/2)`. `Invalidate` grep across `internal/` = 0 hits; no `epoch` field anywhere. Drift: `now` injection is at :79-80 (inside `newTenantTokenSource`), not :63-64 |
| E2 | singleflight exists but can't backfill | ✅ **Exact.** `fetches singleflight.Group` :40; `accessToken` :122-145 with `DoChan("client", …)` :126 (constant key, no generation); closure only re-checks `cachedToken` (:128-129). `fetchToken` :153-173 has exactly one caller (the closure, :130); `cacheToken` has exactly one caller (`fetchToken` :171) |
| E3 | `validateReceipt` → generic HTTP 401, no hook | ✅ **Exact.** `Publish` :83-97; `validateReceipt` :220-237 accepts only 202, discards body :223, `fmt.Errorf("audit governance: HTTP %d", …)` :224 |
| E4 | 403 indistinguishable; scope never runtime-verified | ✅ **Exact.** `validOAuthBinding` oauth.go:86-89 pins `Scope == "audit:event:write"` (:21) at config time only; `tokenRequest` :175-187 sends the scope; `decodeTokenResponse` :189-208 reads only `access_token`/`token_type`/`expires_in` |
| E5 | Dead-letter storm via `publicationOutcome` | ✅ **Exact.** callback_audit.go:84-94: only `errAuditTenantUnbound` → `pending`/`audit.binding_unavailable` (:88-90); everything else → `pending`/`audit.publication_failed`, `dead_letter` at `attempt >= maxAttempts` (default 8, callback.go:76-78); `publishAuditFact` :97-112 (pre-check :105-107); `drainPublications` :44-65, `finishPublicationAttempt` :67-82, `Start` ticker callback.go:254-274 (call at :266). Drift: drain is at :44-65, not :47-89 |
| E6 | `ObserveAuditIngestFailure` hook | ✅ **Exact.** control_plane.go:97-99 increments `auditIngestFail` (:78), rendered :139-140 as `aero_control_audit_ingest_failures_total{source=%q}`; interface/noop/adapter at control_plane_metrics.go:13-24/:26-40/:53-80; fires only on ledger-append failure (audit_ledger.go:73); inline `_ = publisher.Publish` loop :77-80 swallows publish errors |
| E7 | **Wiring gap (new):** `callbackSvc.SetControlPlaneMetrics` exists but is never called | ✅ **Exact.** `callbackService.metrics` field callback.go:66, default `noopControlPlaneMetrics{}` :90, setter :101-104. new.go:96 builds `controlMetrics`; :148 wires it **only** to `auditLedger` via the `ControlPlaneMetricsSetter` assertion; `wireAuditServices` at :150; `callbackSvc.Start` at :179. No other `SetControlPlaneMetrics` call in the tree |
| E8 | `TokenSource` single implementer | ✅ **Exact.** types.go:58-61 (`AccessToken` + `HasBinding`); implemented only by `OAuthTokenSource` (oauth.go:25); `Publisher.tokens TokenSource` (publisher.go:28); no fakes in tests (publisher_test.go uses `NewOAuthTokenSource`) |
| E9 | Composition root constructs the publisher | ✅ **Exact.** app/audit.go:38-68 `buildAuditPublishers` (`NewOAuthTokenSource` :50-54, `NewPublisher` :58-63), gated on `cfg.AuditGovernance.Enabled` :39-41; `wireAuditServices` :17-36 |
| E10 | Test precedent | ✅ **Exact.** publisher_test.go: 169 lines, 5 tests, `httptest` with `/token` + `/api/v1/events`, `auditBinding` helper :164-169, in-package ⇒ `tokens.bindings[tenantID].cachedToken()` directly assertable; `now` injectable in-package (E1 drift); callback_audit_test.go already asserts `publicationOutcome` branches |

### Design-gate corrections (D1–D4) — must be folded into implementation

**D1 — `internal/app/new.go` is at the 500-line gate; AT-10's literal one-liner would fail `check-file-sizes`.**
`MAX_FILE_LINES ?= 500` with `-gt` (Makefile:31-37); `wc -l internal/app/new.go` = 500. Adding
`callbackSvc.SetControlPlaneMetrics(controlMetrics)` anywhere in new.go pushes it to 501 and fails the done
gate. **Required adjustment:** move both setter calls into `wireAuditServices` (app/audit.go:17-36), which
already receives `callbackSvc` and `auditLedger`; new.go drops the 3-line ledger block (new.go:147-149) and
passes `controlMetrics` as an extra argument — net 500 → 497 lines. AT-10's inspection point moves from
new.go to `wireAuditServices` in `internal/app/audit.go`; the acceptance intent (counter live in `cmd/server`)
is preserved. Rejected alternative: trimming unrelated lines from new.go to keep the literal call — churn
without benefit.

**D2 — `service.CallbackService` (callback.go:31-38) does not expose `SetControlPlaneMetrics`.**
The composition root cannot call `callbackSvc.SetControlPlaneMetrics(...)` directly on the interface value;
it must use the existing `service.ControlPlaneMetricsSetter` type assertion, exactly as new.go:147-148 does
for `auditLedger`. The requirement's REQ-6 wording "call `callbackSvc.SetControlPlaneMetrics(controlMetrics)`"
is realized as `if setter, ok := svc.(service.ControlPlaneMetricsSetter); ok { setter.SetControlPlaneMetrics(m) }`
inside `wireAuditServices`.

**D3 — sibling-campaign signature collision must be reconciled before merge.**
The sibling `cmd/migrate` design (`docs/plans/2026-08-06-tokensource-invalidate-on-401-epoch-singleflight.md`)
adds **`Invalidate(SourceBinding)` (void)** to the same `TokenSource` interface and **`ObserveTokenScopeMissing`**
to the same `ControlPlaneMetrics` interface. This spec's `Invalidate(SourceBinding) error` cannot coexist
with the void form (same method name, different signature = compile error), and two differently-named Go
methods for the same rendered family (`aero_control_audit_scope_missing_total`) would force both noop and
adapter implementations to carry both. **Resolution:** implement this spec's contract (error-returning
`Invalidate`, `ObserveAuditScopeMissing`); when the sibling branch merges, rename its methods to match. The
rendered metric family is identical either way, so no ops contract changes.

**D4 — the unhealthy short-circuit must precede the token fetch; `authorization` folds into `Publish`.**
REQ-3 requires "no token fetch, no event POST" while unhealthy, but `authorization` (publisher.go:103-120,
single caller: `Publish`) performs the fetch. Design: replace `authorization` with a `bindingFor(fact)`
helper that validates the fact and derives `SourceBinding{TenantID, SourceSystem}`; `Publish` checks
`BindingUnhealthy` immediately after, before `AccessToken`.

## 2. API changes

### 2.1 `internal/connector/auditgovernance/types.go` — sentinels + interface (REQ-1, REQ-3)

```go
var (
    // existing ErrInvalidConfig / ErrInvalidEvent / ErrInvalidReceipt /
    // ErrTokenUnavailable / ErrTenantUnbound (types.go:11-17) unchanged
    ErrTokenRejected     = errors.New("audit governance: token rejected")
    ErrTokenScopeMissing = errors.New("audit governance: token scope missing")
)

type TokenSource interface {
    AccessToken(context.Context, SourceBinding) (string, error)
    HasBinding(string) bool
    Invalidate(SourceBinding) error        // best-effort; see 2.2
    BindingUnhealthy(SourceBinding) bool
    MarkBindingUnhealthy(SourceBinding)    // void: state set, never fails a publish
}
```

`Invalidate` returns `error` per REQ-1 but is **currently always nil** (unbound/mismatched binding is a
no-op, mirroring `AccessToken`'s validation); the publisher ignores it (`_ = p.tokens.Invalidate(binding)`)
so an invalidation failure can never change a publish outcome. The error return is reserved for a future
distributed cache.

### 2.2 `internal/connector/auditgovernance/oauth.go` — epoch, invalidation, unhealthy state, epoch-keyed singleflight (REQ-1, REQ-2)

```go
const scopeMissingCooldown = 5 * time.Minute // package constant; NOT a config key (REQ-1)

type tenantTokenSource struct {
    // existing fields (oauth.go:30-41) unchanged
    epoch          uint64      // bumped on every Invalidate; guards stale in-flight fetches
    unhealthyUntil time.Time   // zero = healthy; set by MarkBindingUnhealthy
}

// OAuthTokenSource-level: resolve the per-tenant source; unbound tenant or
// SourceSystem mismatch is a no-op (mirrors AccessToken's ErrTenantUnbound path).
func (s *OAuthTokenSource) Invalidate(binding SourceBinding) error {
    expected, err := TenantSourceID(s.sourcePrefix, binding.TenantID)
    tokens := s.bindings[binding.TenantID]
    if err != nil || binding.SourceSystem != expected || tokens == nil {
        return nil // no-op
    }
    tokens.invalidate()
    return nil
}

func (s *tenantTokenSource) invalidate() {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.token = ""
    s.epoch++
    // refreshAt deliberately untouched: cachedToken()'s `token != ""` conjunct
    // (oauth.go:150) is the authority, so ("", false) holds immediately.
}

func (s *OAuthTokenSource) BindingUnhealthy(binding SourceBinding) bool {
    expected, err := TenantSourceID(s.sourcePrefix, binding.TenantID)
    tokens := s.bindings[binding.TenantID]
    if err != nil || binding.SourceSystem != expected || tokens == nil {
        return false // unbound is not "unhealthy"; CanPublishTenant covers it
    }
    return tokens.bindingUnhealthy()
}

func (s *tenantTokenSource) bindingUnhealthy() bool {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.now().Before(s.unhealthyUntil) // injected now (E1 drift: :79-80) makes cooldown testable
}

func (s *OAuthTokenSource) MarkBindingUnhealthy(binding SourceBinding) {
    expected, err := TenantSourceID(s.sourcePrefix, binding.TenantID)
    tokens := s.bindings[binding.TenantID]
    if err != nil || binding.SourceSystem != expected || tokens == nil {
        return
    }
    tokens.markBindingUnhealthy()
}

func (s *tenantTokenSource) markBindingUnhealthy() {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.unhealthyUntil = s.now().Add(scopeMissingCooldown)
    // token cache NOT cleared: 403 means wrong scope, not a bad token (REQ-3 asymmetry)
}
```

`accessToken` — epoch-keyed singleflight (REQ-2):

```go
func (s *tenantTokenSource) accessToken(ctx context.Context) (string, error) {
    if token, ok := s.cachedToken(); ok {
        return token, nil
    }
    s.mu.RLock()
    epoch := s.epoch
    s.mu.RUnlock()
    result := s.fetches.DoChan("client-"+strconv.FormatUint(epoch, 10), func() (any, error) {
        if token, ok := s.cachedToken(); ok {
            return token, nil
        }
        return s.fetchToken(ctx, epoch)
    })
    // existing select (ctx.Done / outcome) unchanged
}
```

`fetchToken(ctx, epoch)` gains the epoch parameter (only caller: the closure); `cacheToken` becomes the
authoritative anti-poisoning guard:

```go
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

Invariant: the epoch check under `mu.Lock` is the correctness guard; the singleflight key is the dedup
mechanism. A stale fetch may deliver its result to its own pre-invalidation waiter but can never repopulate
the cache. New import: `strconv`. Token TTL policy unchanged (C-7).

### 2.3 `internal/connector/auditgovernance/publisher.go` — status-driven 401/403 branches + short-circuit (REQ-3, REQ-4)

`authorization` is folded into `Publish` (D4); `validateReceipt` (publisher.go:220-237) is untouched and
keeps its `defer response.Body.Close()`.

```go
func (p *Publisher) Publish(ctx context.Context, fact *domain.AuditFact) error {
    binding, err := p.bindingFor(fact)
    if err != nil {
        return err
    }
    if p.tokens.BindingUnhealthy(binding) { // REQ-3: before token fetch, before HTTP
        return fmt.Errorf("%w: binding unhealthy", ErrTokenScopeMissing)
    }
    token, err := p.tokens.AccessToken(ctx, binding)
    if err != nil || token == "" || strings.ContainsAny(token, "\r\n") {
        return ErrTokenUnavailable
    }
    request, err := p.newRequest(ctx, fact, binding.SourceSystem, token)
    if err != nil {
        return err
    }
    response, err := p.client.Do(request)
    if err != nil {
        return err
    }
    switch response.StatusCode {
    case http.StatusUnauthorized: // status-driven, body-independent (REQ-3)
        _, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxReceiptBytes))
        response.Body.Close()
        _ = p.tokens.Invalidate(binding)
        return fmt.Errorf("%w: HTTP %d", ErrTokenRejected, response.StatusCode)
    case http.StatusForbidden:
        _, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxReceiptBytes))
        response.Body.Close()
        p.tokens.MarkBindingUnhealthy(binding)
        return fmt.Errorf("%w: HTTP %d", ErrTokenScopeMissing, response.StatusCode)
    default:
        return validateReceipt(response, fact) // 202 + receipt path byte-for-byte unchanged
    }
}

// bindingFor validates the fact and derives the governance binding (D4).
func (p *Publisher) bindingFor(fact *domain.AuditFact) (SourceBinding, error) {
    if !validAuditFact(fact) {
        return SourceBinding{}, ErrInvalidEvent
    }
    source, err := TenantSourceID(p.sourcePrefix, fact.TenantID)
    if err != nil {
        return SourceBinding{}, ErrInvalidEvent
    }
    return SourceBinding{TenantID: fact.TenantID, SourceSystem: source}, nil
}

// BindingUnhealthy is the service-facing predicate for the REQ-5 pre-check
// (service.tenantBoundAuditPublisher). Publish's own check above is the
// connector-side guarantee (also covers the inline ledger loop, REQ-5).
func (p *Publisher) BindingUnhealthy(tenantID string) bool {
    if p.tokens == nil {
        return false
    }
    source, err := TenantSourceID(p.sourcePrefix, tenantID)
    if err != nil {
        return false
    }
    return p.tokens.BindingUnhealthy(SourceBinding{TenantID: tenantID, SourceSystem: source})
}
```

Bodies of 401/403 responses are drained (bounded, `maxReceiptBytes`) before close for connection reuse —
identical to today's non-202 behavior in `validateReceipt` (:223). No body parsing, no retry loop inside
`Publish` (retry is the durable drain's ladder). `IdempotencyKey = fact.ID.String()` (publisher.go:185)
unchanged.

### 2.4 `internal/service/callback_audit.go` — routing to the existing branch + metric emission (REQ-5)

```go
type tenantBoundAuditPublisher interface {
    CanPublishTenant(string) bool
    BindingUnhealthy(string) bool // new; no in-repo fakes exist (verified), real publisher implements it
}

func (s *callbackService) publishAuditFact(ctx context.Context, fact *domain.AuditFact) error {
    if err := s.enqueueFact(ctx, fact); err != nil {
        return err
    }
    for _, publisher := range s.publishers {
        if publisher == nil {
            continue
        }
        if bound, ok := publisher.(tenantBoundAuditPublisher); ok &&
            (!bound.CanPublishTenant(fact.TenantID) || bound.BindingUnhealthy(fact.TenantID)) {
            return errAuditTenantUnbound // existing branch: pending / audit.binding_unavailable
        }
        if err := publisher.Publish(ctx, fact); err != nil {
            if errors.Is(err, auditgovernance.ErrTokenScopeMissing) {
                if s.metrics != nil {
                    s.metrics.ObserveAuditScopeMissing(strings.TrimSpace(fact.Source))
                }
                return errAuditTenantUnbound // same existing branch; one counter per network-403
            }
            return err
        }
    }
    return nil
}
```

New import `github.com/aero/aero-id/internal/connector/auditgovernance` — precedented (billing.go:11
imports `internal/connector/billing`); no cycle (auditgovernance imports only `internal/domain`).
`publicationOutcome` (:84-94) is **unchanged** — the "existing branch" is reused verbatim. `ErrTokenRejected`
(401) intentionally does NOT route to the unbound branch: it stays on the generic ladder
(`pending`/`audit.publication_failed` → `dead_letter` at attempt ≥ 8) because the refetched token self-heals.
The dead inline loop in `auditLedgerService.Record` (`_ = publisher.Publish`, audit_ledger.go:77-80) is not
touched; it shares `Publisher.Publish`, so invalidation/short-circuit apply there too, but no metric is
emitted from it (out of scope, direction #1).

### 2.5 `internal/pkg/metrics/control_plane.go` + `internal/service/control_plane_metrics.go` — scope-missing counter (REQ-6)

- `controlPlaneMetrics` struct gains `auditScopeMissing counterMap // source -> scope-missing publish responses`.
- `Registry.ObserveAuditScopeMissing(source string)` — `r.control.auditScopeMissing.add(source, 1)`, following
  the `ObserveAuditIngestFailure` pattern (:97-99).
- `writeControlPlaneText` gains the render loop:
  `aero_control_audit_scope_missing_total{source=%q}` — distinct from
  `aero_control_audit_ingest_failures_total` (independent `counterMap`, render-distinctness tested, AT-9).
- `ControlPlaneMetrics` interface (:13-24), `noopControlPlaneMetrics` (:26-40), and
  `RegistryControlPlaneMetrics` (:53-80) each gain `ObserveAuditScopeMissing(source string)`.

This family **is** the alert surface: the repository has no alert subsystem (verified: zero `alert` symbols
in `internal/`), so "emits an alert" = this dedicated, monotonically increasing counter — the alert-rule
target — incremented once per network-403 (per tenant per cooldown).

### 2.6 `internal/app/audit.go` + `internal/app/new.go` — composition-root wiring (REQ-6, D1, D2)

```go
// app/audit.go — wireAuditServices (extended; currently :17-36)
func wireAuditServices(callbackSvc service.CallbackService, auditLedger service.AuditLedgerService,
    auditLedgerRepo repository.AuditLedgerRepository, metrics service.ControlPlaneMetrics,
    publishers ...service.AuditPublisher) {
    // existing SetAuditLogger / SetAuditPublicationRepository / SetAuditPublishers assertions unchanged
    for _, svc := range []any{auditLedger, callbackSvc} { // D2: ControlPlaneMetricsSetter assertion
        if setter, ok := svc.(service.ControlPlaneMetricsSetter); ok {
            setter.SetControlPlaneMetrics(metrics)
        }
    }
}
```

new.go: delete the ledger-only setter block (:147-149), pass `controlMetrics` at the `wireAuditServices` call
(:150). Net new.go line count 500 → 497 (D1). AT-10 inspection point: `wireAuditServices` in app/audit.go,
still `internal/app` (composition root), still before `callbackSvc.Start` (new.go:179).

## 3. Compatibility constraints

| # | Constraint | Consequence |
|---|---|---|
| C-1 | `TokenSource` gains 3 methods; interface is `internal/` with exactly one implementer (E8) | No compile break; no external implementers possible |
| C-2 | `service.AuditPublisher` (audit_ledger.go:24) unchanged (`Publish` only); `tenantBoundAuditPublisher` gains `BindingUnhealthy`; **no in-repo test fakes implement it** (verified) | Duck-typed pre-check in `publishAuditFact` keeps working; a future publisher without the method silently skips the pre-check — mitigated by the connector-side check in `Publish` itself |
| C-3 | `service.CallbackService` / `AuditLedgerService` interfaces unchanged | Wiring uses the existing `ControlPlaneMetricsSetter` assertion (D2) |
| C-4 | 202/receipt contract untouched: `validateReceipt`, `decodeReceipt`, `receiptMatches`, `IdempotencyKey = fact.ID.String()` | Retried publishes carry identical keys; governance-side dedup contract undisturbed (AT-4) |
| C-5 | 401 error text changes from `"audit governance: HTTP 401"` to `"audit governance: token rejected: HTTP 401"` | Nothing asserts the old text (verified, E10); persisted `error_code` on `audit_publications` stays `audit.publication_failed` (publicationOutcome unchanged) — no DB row change |
| C-6 | Body-close ownership: 401/403 branches close explicitly (bounded drain); `validateReceipt` unchanged | No leak on the new path; no observable change for the two error-only callers (drain + unwired inline loop) |
| C-7 | Token TTL policy unchanged (24 h cap, `min(refreshSkew, lifetime/2)`); epoch is an invalidation guard, not a new expiry | E1 semantics preserved; 403 does not clear the cache (asymmetry with 401) |
| C-8 | No config keys (cooldown is a package constant), no migrations, no proto, no connector→metrics import | Binary-only rollout; all new code unreachable when `audit_governance.enabled=false` (publisher never constructed, app/audit.go:39-41) |
| C-9 | Metric family is new, additive, and render-distinct from `aero_control_audit_ingest_failures_total` | Existing dashboards unchanged; independent `counterMap` |
| C-10 | **Sibling-campaign reconciliation (D3):** `TokenSource.Invalidate` and the metrics seam must not land with two signatures/names | Merge order decides; this spec's contract (error-returning `Invalidate`, `ObserveAuditScopeMissing`) is the reference |

## 4. Failure modes

| # | Scenario | Behavior | Correctness argument |
|---|---|---|---|
| FM-1 | In-flight fetch (epoch e0) completes **after** `Invalidate` bumped to e1 | `cacheToken(wire, e0)` sees `e0 != s.epoch`, discards; the e0-group waiter still receives the token string (uncached) | No poisoning re-entry: cache stays empty; next `AccessToken` fetches fresh (AT-3) |
| FM-2 | Race windows: (a) caller misses cache, then `Invalidate` lands before its epoch read → joins the e1 group; (b) caller read e0, `Invalidate` lands before `DoChan` → joins e0 group, result discarded | At most one wasted fetch per window; no correctness loss; drain's next tick converges | Epoch guard under lock (2.2) |
| FM-3 | Cache-hit caller holds the pre-invalidation token across `Invalidate` | Uses it once → 401 → invalidate again (idempotent, epoch already bumped) → converges; concurrent holders bounded by bulkhead (P6-06, ≤ 64) | Bounded by the drain's ladder and bulkhead |
| FM-4 | **Token endpoint** returns 401/403 (rotated secret, revoked client) | `fetchToken` → `ErrTokenUnavailable: HTTP …`; **no invalidation, no unhealthy mark** — nothing was cached and the revoked artifact is the grant, not the token | Boundary pinned by test (§6, B-2); publication stays on the generic ladder and dead-letters at the cap — pre-existing behavior, unchanged |
| FM-5 | Persistent events-endpoint 401 with a healthy token endpoint | Each fact: 401 → invalidate → refetch (fresh token) → still 401 → … bounded: ≤ 8 publishes + ≤ 8 refetches per fact over ~62 min (retryDelay 1 s → 1 h, callback.go:358-375) → `dead_letter` | Bounded by the existing ladder; no storm (one refetch per epoch group per tick, FM-10) |
| FM-6 | 403 storm | First 403 marks binding unhealthy; while unhealthy the service pre-check and the connector short-circuit both suppress network traffic; ≤ 1 real probe per tenant per `scopeMissingCooldown`; each probe-403 increments the counter once | Storm suppressed by construction (AT-6); alert rate bounded (≤ 1 per tenant per 5 min) |
| FM-7 | Cooldown-boundary race (service pre-check passes, connector check flips, or vice versa) | Rare double-count or one suppressed attempt counted | Benign, bounded; both checks read the same `unhealthyUntil`/`now` |
| FM-8 | Recovery after cooldown | Next publish is a real probe: 202 → nil, no re-mark, no counter, binding expires naturally; 403 → re-marked + sentinel + counter (alert re-fires) | Automatic, no operator action, no restart (AT-8) |
| FM-9 | 401 observed while binding is unhealthy | Impossible: no HTTP while unhealthy. After cooldown the probe order applies: 401 → invalidate + refetch (token corrected); 403 → re-mark; 202 → healthy | Probe cadence keeps convergence bounded |
| FM-10 | N concurrent publishes after a 401 (one drain tick) | N concurrent `Invalidate` calls (idempotent, one epoch bump); N `AccessToken` calls join the post-invalidation epoch group → exactly one refetch; stale holders per FM-3 | T-10.1's counting-`/token` assertion (AT-2) |
| FM-11 | Scope-missing rows on the drain ladder | `errAuditTenantUnbound` branch returns `pending`/`audit.binding_unavailable` **forever** (never dead-letters — the unbound branch precedes the `attempt >= maxAttempts` check, callback_audit.go:88-92); retries re-run the pre-check with zero network; recovery is automatic | No DLQ loss for scope-missing; rows stay recoverable; pre-existing branch semantics reused |
| FM-12 | Process restart | Epoch resets to 0, token cache and unhealthy state are process-local and cleared | No cross-restart consistency requirement: epoch is an intra-process guard, not a fencing mechanism |
| FM-13 | Multi-instance deployment | Each instance refetches/self-heals on its own 401; invalidation is per-process | ≤ instances × 1 refetch per wave; no shared state to corrupt |
| FM-14 | Spurious 401 (WAF/gateway false positive, IdP clock skew) | One wasted refetch, self-healing (the client-credentials refetch returns the same valid token) | Cost of a spurious invalidate (1 fetch) ≪ cost of a missed revocation (up-to-24 h dead-token window) |
| FM-15 | `Invalidate`/`MarkBindingUnhealthy` for unbound tenant or mismatched `SourceSystem` | No-op (mirrors `AccessToken`'s validation) | The publisher only mutates the binding it just used; no cross-tenant effects |
| FM-16 | 403 with `{"error":"invalid_token"}` body (authority expressing revocation as 403) | Status-driven classification: `ErrTokenScopeMissing` + unhealthy mark; no invalidation, no body parsing | **Explicitly rejected alternative** (billing precedent maps `invalid_token` → `ErrUnauthorized`, client.go:277-282): REQ-3 is status-driven by design and body parsing is out of scope; 401 remains the revocation signal. Consequence recorded: such deployments see the scope-missing counter rise and rows parked on `binding_unavailable` rather than a refetch — remediation is the runbook's grant-drift procedure. A future body-refinement must not block invalidation |
| FM-17 | Direction #1 later wires publishers into the ledger | The inline loop shares `Publisher.Publish`; invalidation/short-circuit become cross-path via the shared per-tenant cache | Intended mechanism; bounded by the same epoch group and ladder; not load-bearing today (ledger gets zero publishers, new.go:146) |

## 5. Migration steps

No schema, data, config, or proto migration exists for this design (C-8). Sequence = implementation order,
then binary rollout.

1. **types.go** — add `ErrTokenRejected`, `ErrTokenScopeMissing`, and the three `TokenSource` methods.
   Compile breaks pinpoint the single implementer (C-1) and force the sibling reconciliation (D3).
2. **oauth.go** — `scopeMissingCooldown` constant; `epoch` + `unhealthyUntil` fields; the three
   `OAuthTokenSource`/`tenantTokenSource` method pairs; epoch-keyed `accessToken` (`strconv` import);
   epoch parameters on `fetchToken`/`cacheToken` with the discard guard (2.2).
3. **publisher.go** — fold `authorization` into `Publish` via `bindingFor`; add the unhealthy short-circuit
   and the 401/403 switch branches; add `Publisher.BindingUnhealthy(string) bool` (2.3).
4. **metrics** — `control_plane.go`: counter field + observer + render line; `control_plane_metrics.go`:
   interface/noop/adapter methods (2.5).
5. **callback_audit.go** — extend `tenantBoundAuditPublisher`, extend the pre-check, add the
   `ErrTokenScopeMissing` → metric + `errAuditTenantUnbound` mapping, add the connector import (2.4).
6. **composition root** — extend `wireAuditServices` (app/audit.go) with the metrics loop; update the new.go
   call site and delete the ledger-only block (2.6, D1/D2).
7. **Tests** (§6) in the same change set.
8. **Gate**: `make test && make lint && make lint-strict && make check-file-sizes && make build`. No
   `make test-integration` run required (no SQL/storage/migration change; all new logic is in-package
   `httptest`-testable per E10).
9. **Rollout**: single binary deploy; no config change; behavior gated by the pre-existing
   `audit_governance.enabled` flag. Epoch/invalidation/unhealthy state are process-local — no multi-instance
   coordination. In `enabled=false` deployments every new symbol is unreachable.
10. **Runbook**: alert on `aero_control_audit_scope_missing_total > 0` (governance client grant drift —
    remediate the OAuth client's `audit:event:write` grant); after a governance-side token rotation expect a
    one-time 401 → refetch → recovery within one drain tick instead of the previous up-to-24 h dead-token
    window. Note the FM-16 spelling caveat (403+`invalid_token` counts as scope-missing).

## 6. Testable acceptance mapping

All tests are in-package unit tests (stdlib only, no Docker, no testify) using E10's fixture pattern
(`httptest` `/token` + `/api/v1/events`, `auditBinding` helper). **Test-server hit counters and captured
headers must be `atomic.Int32`/mutex-guarded** — handlers run on server goroutines and plain ints race under
N≥8 concurrency (go vet will not catch it).

| Spec acceptance | Test (file) | Concrete assertions | Proves |
|---|---|---|---|
| AT-1 | `TestPublish401InvalidatesAndBumpsEpoch` (publisher_test.go) | Prime cache (1 publish: `/token`=1, 202+receipt); events → 401; `errors.Is(err, ErrTokenRejected)`; in-package `tokens.bindings[tenantID].cachedToken()` == `("", false)`; `s.epoch == 1` | REQ-1/REQ-3 |
| AT-1 variant | same test, 401 body `{"error":"insufficient_scope"}` | Still `ErrTokenRejected` and `!errors.Is(err, ErrTokenScopeMissing)` | Status-driven precedence, body-independent (REQ-3; pins against a body-code-first regression) |
| AT-2 | `TestConcurrentPublishesTriggerExactlyOneRefetch` (publisher_test.go) | After the 401, events → 202; N=8 concurrent `Publish` (same fact); atomic `/token` counter == **2** total; all N `Authorization` headers == `"Bearer "+token2` (never the rejected token) | REQ-2 epoch-keyed singleflight |
| AT-2 extension (D-correction pin) | `TestPostInvalidateCallerDoesNotJoinStaleFlight` (publisher_test.go) | Channel-gated `/token`: start `AccessToken`, `Invalidate` mid-flight, start a **second** `AccessToken` before release, then release; second caller's returned token != stale token; `/token` counter == 2 | REQ-2's "never deduped against a post-invalidation fetch" (the only test that fails under a constant-`"client"`-key regression) |
| AT-3 | `TestStaleInflightFetchCannotRepopulate` (publisher_test.go) | Slow `/token` (channel-gated): start `Publish`, `Invalidate` from another goroutine, release with a token; `cachedToken()` stays `("", false)` | REQ-2 discard guard in `cacheToken` |
| AT-4 | `TestRetryAfter401CompletesWithRefetchedToken` (publisher_test.go) | Stateful events handler (401 once, then 202); same fact twice; second exchange's `Authorization` == refetched token; both JSON bodies' `idempotency_key` and `event_id` identical == `fact.ID.String()` | REQ-3 + governance dedup contract unchanged (C-4) |
| AT-5 | `TestPublish403MarksUnhealthyWithoutClearingCache` (publisher_test.go) | Events → 403 `{"error":"insufficient_scope"}`; `errors.Is(err, ErrTokenScopeMissing)`; `BindingUnhealthy(binding)` true; `cachedToken()` still returns the token; `s.epoch` unchanged (stays 0) | REQ-3 asymmetry (403 ≠ 401) |
| AT-6 | `TestUnhealthyBindingShortCircuitsWithoutNetwork` (publisher_test.go) | In the AT-5 state, repeat `Publish` (same fact): `/token` and `/api/v1/events` counters do not move; each call returns `ErrTokenScopeMissing` | REQ-4 storm suppression |
| AT-7 | `TestPublishAuditFactRoutesScopeMissingToUnboundBranch` (callback_audit_test.go) | Fake `AuditPublisher` implementing `tenantBoundAuditPublisher` + metrics spy implementing `ControlPlaneMetrics`: (a) `BindingUnhealthy()==true` → `errors.Is(err, errAuditTenantUnbound)` and `publicationOutcome(err, 0, 8, now, delay)` == `("pending","audit.binding_unavailable", now.Add(delay))`; (b) healthy pre-check + `Publish` returns `ErrTokenScopeMissing` → same routing + spy records exactly one `ObserveAuditScopeMissing(fact.Source)`; suppressed (pre-checked) attempts record zero | REQ-5 existing-branch routing + exactly-once-per-network-403 counting |
| AT-8 | `TestUnhealthyCooldownProbeAndRecovery` (publisher_test.go) | In-package: swap `tokens.bindings[tenantID].now` to a controllable fake (E1/E10); advance past `scopeMissingCooldown`; next `Publish` performs exactly one events POST (counter +1) and: (a) still 403 → `ErrTokenScopeMissing` again, re-marked, counter re-fired (service level); (b) events 202 → `Publish` nil, no counter, binding left to expire naturally | REQ-4 probe cadence + automatic recovery |
| AT-9 | `TestControlPlaneMetrics_ScopeMissingFamily` (metrics_test.go, following :122-144) | After `ObserveAuditScopeMissing("aero-id")`, `WriteText` contains `aero_control_audit_scope_missing_total{source="aero-id"} 1`; `aero_control_audit_ingest_failures_total` unaffected (independent counters). Negative: 500 on events (and a transport error) → generic error, no unhealthy mark, no counter (connector + service spy) | REQ-6 distinct family + no false alerts |
| AT-10 | Composition-root wiring — **inspection with D1/D2 adjustment** | `wireAuditServices` (app/audit.go) calls `SetControlPlaneMetrics(controlMetrics)` on both `auditLedger` and `callbackSvc` via the `ControlPlaneMetricsSetter` assertion; new.go passes `controlMetrics` at the `wireAuditServices` call and stays ≤ 500 lines | REQ-6 counter live in `cmd/server` (without it AT-7's counter is a noop — E7) |
| B-1 (boundary) | `TestTokenEndpoint401DoesNotInvalidate` (publisher_test.go) | `/token` → 401 ⇒ `errors.Is(err, ErrTokenUnavailable)`, `!errors.Is(err, ErrTokenRejected)`; `cachedToken()` empty but `epoch` unchanged; no unhealthy mark | REQ-3 boundary (invalidation is events-endpoint-keyed; pins against adding the branch to `fetchToken`) |
| B-2 (negative) | `TestNonScopeFailureLeavesBindingHealthy` (publisher_test.go) | 500 on events with valid token → generic `audit governance: HTTP 500`; `BindingUnhealthy` false; cache untouched | REQ-4 no false alarms |
| B-3 (optional) | `TestRecordInlineLoopDoesNotEmitScopeMetric` (audit_ledger_test.go) | `Record` with a fake repo + publisher returning `ErrTokenScopeMissing` + metrics spy ⇒ spy not called | REQ-5 drain-path-only emission boundary (inline loop untouched, direction #1) |

**Requirement coverage:** REQ-1 (AT-1, AT-5), REQ-2 (AT-2, AT-3 + extension), REQ-3 (AT-1, AT-4, B-1),
REQ-4 (AT-5, AT-6, AT-8, B-2), REQ-5 (AT-7, B-3), REQ-6 (AT-9, AT-10). Existing publisher tests (5),
`TestPublicationOutcome*` (callback_audit_test.go), and the metrics render test continue to pass unmodified
(C-4, C-5).

## 7. Engineering constraints

- Non-test file-size estimates: oauth.go 217 → ~275, publisher.go 258 → ~290 (authorization −18, +bindingFor,
  +switch, +BindingUnhealthy), types.go 116 → ~130, callback_audit.go 113 → ~128, control_plane.go 176 →
  ~190, control_plane_metrics.go 81 → ~93, app/audit.go ~110 → ~120 — all ≤ 500 with headroom.
  **new.go must end at ≤ 500** (D1): 500 → 497 via the wireAuditServices move.
- Function limits: `Publish` ~35 lines / cyclomatic ~7; `accessToken`, `publishAuditFact`, and all new
  helpers ≤ 50 lines / cyclomatic ≤ 10; package average ≤ 5.0 enforced by `make lint-strict`.
- Connector stays dependency-free (imports `internal/domain` only); sentinels live in types.go beside the
  existing `Err*` set; no repository errors cross into the connector.
- Done gate: `make test && make lint && make lint-strict && make check-file-sizes && make build`. No
  `make test-integration` (no SQL/storage/migration/connector-network contract change).

## 8. Explicitly out of scope (unchanged from REQ §5)

- Direction #1 (complete in-tx audit writes: dead inline loop removal, AppendInTx consolidation,
  check-audit-swallow gate) and direction #3 (anchored event_id, DLQ replay, cmd/audit-replay) — not touched.
- Body parsing of governance error envelopes (incl. the FM-16 `invalid_token`-on-403 mapping) — status-driven
  classification only; the rejection is recorded in FM-16.
- Drain-outcome metric (`finishPublicationAttempt` emission) recommended by the sibling blast-radius review —
  **not in this spec's scope**; noted for the runbook: scope-missing rows are observable via DB
  `status='pending', error_code='audit.binding_unavailable'`, and generic dead-letters via the same table.
- Refetch cooldown (sibling M1) — not load-bearing here: the epoch-keyed singleflight bounds refetches to one
  per tenant per drain tick, and the retry ladder (FM-5) bounds per-fact refetches.
- No config keys, no migrations, no proto changes, no changes to `internal/connector/billing`.
