# Resolution — merged `TokenSource` interface and unified 401/403/scope-routing for the two audit-governance campaigns

| | |
|---|---|
| Conflicts | `docs/plans/2026-08-06-cmd-server-tokensource-invalidate-on-401-design.md` (sibling, binding-typed `Invalidate`) vs the `add-tokensource-invalidate-tenantid` campaign (`docs/auto/runs/add-tokensource-invalidate-tenantid-driven-by-go-077b85ef`, tenantID-typed `Invalidate`) |
| Status | **Resolved** — single merged interface, unified routing, ordering constraint stated. Both campaigns land green. |
| Verified against | `git head 13e4676`, `go build ./...` clean |

## 1. Verification of both specs against the current repository

### 1.1 Shared ground (both specs' claims that check out)

| Claim | Verified |
|---|---|
| `TokenSource` at types.go:58-61 is `AccessToken(context.Context, SourceBinding) (string, error)` + `HasBinding(string) bool` | ✓ exact |
| `SourceBinding{TenantID, SourceSystem}` (types.go:53-56) and `TenantSourceID(prefix, tenantID)` (types.go:63-69) already exist — the binding-typed parameter costs nothing new | ✓ exact |
| Zero `Invalidate`/`epoch`/`OnScopeRejected`/`ErrTokenRejected`/`ErrScopeRejected`/`ErrTokenScopeMissing`/`BindingUnhealthy`/`MarkBindingUnhealthy`/`ObserveAuditScopeMissing` in `internal/` | ✓ exact (rg across `internal/connector/auditgovernance` and the tree) |
| One production implementor: `OAuthTokenSource` (oauth.go:25), constructed only at `internal/app/audit.go:99` | ✓ exact |
| `cachedToken` (:147-151), constant-key `DoChan("client", …)` (:126-130), 24 h cap + skew (:210-217), `fetchToken` (:153-173) with exactly one caller | ✓ exact |
| `validateReceipt` non-202 → opaque `"audit governance: HTTP %d"` (publisher.go:222-225); 202-only acceptance, conflict-as-success, `IdempotencyKey = fact.ID.String()` (:185) | ✓ exact |
| `publicationOutcome` (callback_audit.go:90-101 — HEAD layout; the working-tree M0 layout is §4's :142-153) special-cases only `errAuditTenantUnbound` → `("pending","audit.binding_unavailable")`, never dead-letters; everything else → `("pending","audit.publication_failed")` → `dead_letter` at `attempt >= maxAttempts` | ✓ exact |
| Metrics seam: `ControlPlaneMetrics` interface + noop + adapter (control_plane_metrics.go:13-80); `auditIngestFail` counterMap + render `aero_control_audit_ingest_failures_total{source=%q}` (control_plane.go:78, render :185-186) | ✓ exact |
| `go build ./...` clean at head; publisher_test.go 7 tests, 272 lines, all construct the real `NewOAuthTokenSource` | ✓ exact |

### 1.2 Corrections to the sibling spec (its D1/D2 anchors are stale)

| Sibling claim | Current tree |
|---|---|
| "new.go:147-148 wires `controlMetrics` only to `auditLedger`; adding the callback setter pushes new.go to 501" (D1) | **Stale.** `new.go` is 500 lines but contains **no** `SetControlPlaneMetrics` block; the ledger setter already lives in `buildAuditStack` (app/audit.go:52-54). The merged metrics wiring extends that existing assertion — **new.go needs zero changes and stays at 500** |
| "composition root cannot call `callbackSvc.SetControlPlaneMetrics` directly; use the `ControlPlaneMetricsSetter` assertion" (D2) | Still true in substance; the assertion loop to extend is app/audit.go:52-54 (not new.go) |
| "no in-repo fakes implement `TokenSource`" (C-2/E8) | **Wrong for the integration tree.** `staticTokenSource` (internal/service/callback_integration_test.go:64-73, `//go:build integration`) implements both current methods. The interface commit must extend it or `make test-integration` fails to compile (default `make test` is unaffected) |

### 1.3 The conflict, precisely

Both specs change the **same interface in the same file with the same method name and incompatible signatures**:

| | This campaign (tenantid) | Sibling (cmd-server) |
|---|---|---|
| `Invalidate` | `Invalidate(tenantID string)` — void | `Invalidate(SourceBinding) error` |
| 403 handling | 403 → `Invalidate` (clears cache) + `ErrTokenRejected` | 403 → `MarkBindingUnhealthy` (cache retained, cooldown) + `ErrTokenScopeMissing` |
| Scope seam | `OAuthConfig.OnScopeRejected func(tenantID string)` (token-endpoint body classification) | `ControlPlaneMetrics.ObserveAuditScopeMissing(source string)` (events-endpoint 403) |
| Scope sentinel | `ErrScopeRejected` | `ErrTokenScopeMissing` |
| Service routing | both new sentinels → generic ladder | scope-missing → `errAuditTenantUnbound` branch (pending/`audit.binding_unavailable`) |

This is a compile-level conflict, not a merge note: the two `Invalidate` forms cannot coexist on one interface, and the tenantid spec's "403 → invalidate" is behaviorally inferior to the sibling's cooldown (refetching a client-credentials token with the same grant reproduces the same 403 — invalidate-on-403 would storm). The 401 machinery (epoch, epoch-keyed singleflight, `cacheToken` discard guard) is identical in both specs and needs no reconciliation.

## 2. The single merged interface

```go
type TokenSource interface {
    AccessToken(context.Context, SourceBinding) (string, error)
    HasBinding(string) bool
    Invalidate(SourceBinding) error        // 401: clear cached token, bump epoch; best-effort, always nil today
    BindingUnhealthy(SourceBinding) bool   // 403 cooldown predicate (zero = healthy)
    MarkBindingUnhealthy(SourceBinding)    // 403: set cooldown; never clears the token cache
}
```

This is exactly the sibling's shape. Rationale:

1. **`SourceBinding` over `tenantID`** — it is the parameter type of the sibling method `AccessToken`; the publisher derives it (`bindingFor`) before any HTTP; the unbound/mismatched-binding no-op mirrors `AccessToken`'s validation exactly (`SourceSystem` check via `TenantSourceID`). A bare `tenantID` forces the source to re-derive or ignore the source system, splitting the interface's own vocabulary. The tenantid campaign's intent ("the publisher invalidates the binding it just used") is fully preserved: the binding carries the tenant ID.
2. **`error` return over void** — a superset: a void call site (`_ = p.tokens.Invalidate(binding)`) compiles against the error-returning form; the reverse is false. Required by the sibling's REQ-1, always nil today, reserved for a future distributed cache.
3. **`MarkBindingUnhealthy` is the merged 403 mechanism** — it supersedes the tenantid spec's "403 → `Invalidate`". 403 means the grant lacks `audit:event:write`; the token itself is valid, so clearing and refetching is both useless and storm-prone. The sibling's `scopeMissingCooldown` + short-circuit is the correct unified behavior.

Compile blast radius of the interface change (verified complete): `OAuthTokenSource` (oauth.go:25), `staticTokenSource` (integration-tagged fake, must gain the three methods), `Publisher.tokens` field + `NewPublisher` parameter (publisher.go:28/:32). `internal/connector/billing`'s `tokenSource` is an unrelated unexported type. No default-gate test fakes implement the interface.

## 3. Unified 401 / 403 / scope-rejection routing

Single sentinel set (both campaigns adopt these names):

```go
ErrTokenRejected     = errors.New("audit governance: token rejected")      // 401, events endpoint
ErrTokenScopeMissing = errors.New("audit governance: token scope missing") // 403 + token-endpoint scope rejection
```

(The tenantid spec's `ErrScopeRejected` name is dropped; nothing outside the package references either sentinel yet.)

| Surface | Condition | Action | Error | Alert seam |
|---|---|---|---|---|
| Events endpoint | 401 | drain body (bounded) → `Invalidate(binding)` → refetch on next attempt. **C2 delta (C-4, additive):** scope body (`invalid_scope`/`insufficient_scope` via the single classifier, see pin below) → `ErrTokenScopeMissing` instead; `Invalidate` stays status-driven | `ErrTokenRejected: HTTP 401` (plain/empty body); `ErrTokenScopeMissing: HTTP 401` (scope body — **lands in C2**, not C1) | none — refetch self-heals (plain); service `ObserveAuditScopeMissing` exactly once per failed attempt (scope body, from C2) |
| Events endpoint | 403 | drain body (bounded) → `MarkBindingUnhealthy(binding)`; cache **retained** | `ErrTokenScopeMissing: HTTP 403` | service: `ObserveAuditScopeMissing(source)` exactly once per network-403 |
| Events endpoint | other status | `validateReceipt` byte-unchanged (202-only, conflict-as-success, idempotency key) | as today | none |
| Pre-check (unhealthy) | `BindingUnhealthy` true | no token fetch, no HTTP (connector check in `Publish` + service pre-check) | `ErrTokenScopeMissing` | none — suppressed attempts un-counted |
| Token endpoint | 400 + parseable `{"error":"invalid_scope"\|"insufficient_scope"}` | nothing cached; **recover-wrapped, re-entrancy-guarded** `OnScopeRejected(tenantID)` hook (in-package; see `docs/plans/2026-08-07-auditgovernance-hook-hazard-resolution.md` — x/sync v0.20.0 re-raises flight panics as process crashes) | `ErrTokenScopeMissing: HTTP 400` | composition-root hook → same `aero_control_audit_scope_missing_total` family |
| Token endpoint | any other non-200 (incl. 401/403) | unchanged | `ErrTokenUnavailable: HTTP %d` | none — sibling FM-4 boundary preserved (no invalidate, no mark) |

**One classifier signature set (all three docs agree):** `tokenErrorCode(body []byte) string` + `scopeRejectedCode(code string) bool` in the connector package — best-effort parse of `{"error":"<code>"}` from an already-drained, bounded body, plus the `invalid_scope`/`insufficient_scope` predicate. Status gates are **caller-side policy** (token endpoint: `status == 400`; events endpoint: the 401 branch) and never enter the helper. The design doc's earlier `classifyTokenError(status, body)` and any `tokenErrorCode(*http.Response)` form are **dropped** — implementers must not produce two shapes (bulkhead design §2.2, hook-hazard companion §2.1).

Two seams, one metric family, **one label set**: `OnScopeRejected` stays the connector's layering-preserving sink (auditgovernance imports only `internal/domain`; no connector→metrics import), bound at the composition root to `ObserveAuditScopeMissing` on the same `aero_control_audit_scope_missing_total{source=...}` family the service emits for network-403s. **Both seams emit the same normalized label `strings.TrimSpace(...)`**: the service labels `strings.TrimSpace(fact.Source)`, the hook closure labels `strings.TrimSpace(settings.SourcePrefix)` (the connector trims the prefix internally, oauth.go:61, so the closure must match), and the composition root fail-fast-pins `strings.TrimSpace(settings.SourcePrefix) == "aero-id"` (governance facts are hardcoded `Source: "aero-id"`, audit_hold.go:163/:183) so an operator-set prefix can never split one condition into two label sets. No double counting: a 403 fires only the service-side counter; a token-endpoint scope rejection fires only the hook.

## 4. Error-routing composition — callback_audit.go:43-182 (confirmed)

Current verified composition (working tree — the M0 claim-based-dispatch tree, not HEAD): `drainPublications` (:43-93, claim + bulkhead fan-out, `Acquire`/`Release` :75-81) → `finishPublicationAttempt` (:113-139, computes outcome, settles row) → `publicationOutcome` (:142-153, the only routing: `errAuditTenantUnbound` → `pending`/`audit.binding_unavailable` forever; else `pending`/`audit.publication_failed` → `dead_letter` at cap) → `publishAuditFact` (:155-182, enqueue :170-172 + duck-typed `tenantBoundAuditPublisher` pre-check :162-169 + `Publish` error propagation :177-179).

Merged delta — **only `publishAuditFact`'s publish loop (:173-180) changes**:

```go
type tenantBoundAuditPublisher interface {
    CanPublishTenant(string) bool
    BindingUnhealthy(string) bool // new
}

// in publishAuditFact:
if bound, ok := publisher.(tenantBoundAuditPublisher); ok &&
    (!bound.CanPublishTenant(fact.TenantID) || bound.BindingUnhealthy(fact.TenantID)) {
    return errAuditTenantUnbound
}
if err := publisher.Publish(ctx, fact); err != nil {
    if errors.Is(err, auditgovernance.ErrTokenScopeMissing) {
        if s.metrics != nil {
            s.metrics.ObserveAuditScopeMissing(strings.TrimSpace(fact.Source))
        }
        return errAuditTenantUnbound
    }
    return err
}
```

- **`ErrTokenScopeMissing`** (events-403 or token-endpoint scope rejection) → `errAuditTenantUnbound` → `pending`/`audit.binding_unavailable`, **never dead-letters** (the branch precedes the `attempt >= maxAttempts` check), re-probes automatically after cooldown. This supersedes the tenantid spec's "both sentinels → generic ladder".
- **`ErrTokenRejected`** (plain/empty-body 401) → generic ladder (`pending`/`audit.publication_failed` → `dead_letter` at cap). Deliberately not routed to the unbound branch — the refetched token self-heals; DLQ is the correct terminal for a persistent 401. (The scope-body 401 variant is the C-4 C2 delta in §3: from C2 on it routes to the unbound branch like the other scope-missing sources.)
- `publicationOutcome` (:142-153) is byte-identical; `service.AuditPublisher` (audit_ledger.go:24-26) is untouched. The stale claim of an inline publish loop at audit_ledger.go:77-80 is dropped (no such loop exists; the only service `Publish` call site is callback_audit.go:177).

## 5. Composition-root wiring — internal/app/audit.go:99-129 (confirmed)

Current verified block: `buildAuditPublishers` (:99-129), flag-gated on `cfg.AuditGovernance.Enabled` (:100) → `NewOAuthTokenSource` (:111-115, `OAuthConfig{...}` literal :111-115) → `NewPublisher` (:119-124) → `return []service.AuditPublisher{publisher}, nil` (:128-129). All merged code is unreachable when the flag is off.

Merged deltas:

1. **audit.go:111-115** — `OAuthConfig` literal gains `OnScopeRejected: func(tenantID string) { controlMetrics.ObserveAuditScopeMissing(strings.TrimSpace(settings.SourcePrefix)) }` — the label is the **same normalized value** the service seam emits (`strings.TrimSpace(fact.Source)`), per the §3 label-seam pin. **C-3 (supersedes the earlier 'no signature changes' reading):** `buildAuditPublishers` **gains a `controlMetrics service.ControlPlaneMetrics` parameter** — it currently takes `(cfg *config.Config, db *gorm.DB)` (:99) and cannot see the sink the closure needs; the call site in `buildAuditStack` (:51) already has `controlMetrics` in scope. `OAuthConfig`/`NewPublisher` constructor signatures are otherwise unchanged.
2. **audit.go:52-66** — the existing `SetControlPlaneMetrics` assertion loop (setter calls at :57 for `auditLedger`, :65 for `callbackSvc`) is the wiring; the service-side scope counter reaches `callbackSvc` through the same `service.ControlPlaneMetricsSetter` assertion. **new.go is untouched and stays at 500 lines** (the sibling's D1 concern is already resolved by the current tree layout).
3. `wireAuditServices` signature unchanged; `SetAuditPublishers` wiring unchanged. `buildAuditPublishers` **does** gain the `controlMetrics` parameter (delta 1, C-3) — the earlier 'signatures unchanged' wording is superseded: without the parameter the `OAuthConfig` literal cannot reference the sink.
4. **Label-seam guard:** `buildAuditPublishers` fails fast unless `strings.TrimSpace(settings.SourcePrefix) == "aero-id"` — governance facts are hardcoded `Source: "aero-id"` (audit_hold.go:163/:183), so any other prefix would split `aero_control_audit_scope_missing_total` into two label sets for one condition (bulkhead design §2.6).

## 6. Ordering constraint — how both campaigns land with no broken intermediate commit

**Constraint: the interface shape is fixed by exactly one commit, and it must be the superset (sibling) form; the tenantid campaign then lands as a purely additive delta. The interface must never be touched again.**

- **Commit C1 (base — sibling campaign rebased onto head 13e4676, with the merge folded in):** merged interface (§2) + `OAuthTokenSource` implementing all three new methods + epoch/epoch-keyed singleflight/`cacheToken` discard guard + `bindingFor`/`Publish` 401/403/unhealthy switch + `BindingUnhealthy(tenantID)` service predicate + `ObserveAuditScopeMissing` across metrics interface/noop/adapter/counter/render + service routing (§4) + wiring (§5) + **`staticTokenSource` extended with the three methods** (or `make test-integration` breaks) + the sibling's AT-1..AT-10/B-1..B-3 tests. Green alone; `make test && make lint && make lint-strict && make check-file-sizes && make build` passes.
- **Commit C2 (delta — tenantid campaign rebased onto C1):** token-endpoint 400 + `invalid_scope`/`insufficient_scope` classification in `fetchToken` + `OAuthConfig.OnScopeRejected` field + hook plumbing through `newTenantTokenSource` (gains the `tenantID` parameter) + **in-package hook safety: `notifyScopeRejected` recover-wrap + `hookRunning` re-entrancy guard** + **`Publisher.authorization` propagation of `ErrTokenScopeMissing` (errors.Is)** + **events-401 scope-body classification in `Publish` (C-4 — the §3 events-401 row's C2 delta)** + its tests (T-10.1a/b/c adjusted per §3/§4, T-10.1d rewritten to assert cache retention + `BindingUnhealthy` + unchanged epoch, T-10.1e/f added for the panicking and re-entrant hook — see `docs/plans/2026-08-07-auditgovernance-hook-hazard-resolution.md`). **Purely additive** — one new optional struct field, one new branch in `fetchToken`, one new branch in `Publish`'s 401 path, one new branch in `authorization`, new tests; no `TokenSource` interface, constructor, or routing-table change (the one deliberate signature exception is `buildAuditPublishers` gaining its `controlMetrics` parameter — C-3, §5 delta 1). Green alone. **No regression window between C1 and C2:** between the two commits an events-401-with-scope-body behaves exactly as today (`ErrTokenRejected` → generic ladder — C1's 401 branch is the plain form, C-4), and a token-endpoint scope rejection cannot exist before C2 (C1 has no classification; non-200 → `ErrTokenUnavailable` as today). C2 only widens classifications that previously fell into the generic bucket.
- **Forbidden order:** tenantid-first is broken-by-construction — C1 would then have to delete/rename `Invalidate(tenantID string)`, flip the 403 branch semantics, and rewrite T-10.1d in the same commit that lands the interface, and any commit between the two campaigns would not compile. Sibling-first has no such intermediate: after C1 the tree compiles and runs with the full merged behavior; C2 only widens the token-endpoint classification.
- **Atomicity within C1:** interface change, the real implementor, the integration fake, the publisher switch, and the service routing must land in one commit — they are one compile unit. Note: the default gate does **not** prove this — `go build ./...`/`go vet ./...` never compile the integration-tagged `staticTokenSource` (verified: a partial subset with the fake forgotten passes the default gate silently, and `go build -tags integration` is equally silent because `go build` skips `_test.go`). The FM-11 gate is `make check-integration-compile` (`go test -tags integration -run '^$' ./... && go vet -tags integration ./...`), which must be in C1's gate and the AGENTS.md done gate.
- **Commit-boundary pin (with the tenantid design doc `docs/plans/2026-08-07-internal-outbox-tokensource-invalidate-on-401-design.md`):** the tenantid design's **M1 ≡ C1 — one joint commit** carrying the full five-method merged interface (§2) + the `ErrTokenRejected`/`ErrTokenScopeMissing` sentinels + the implementor + the integration fake; its M2 (epoch machinery) and M3 (Publish 401/403 switch) fold into C1 exactly as listed above, and its M4 ≡ C2 (purely additive: 400-gated token-endpoint classification + `OAuthConfig.OnScopeRejected` + hook safety + `authorization` propagation). The design's M6 is demoted to verification no-op. **The interface is touched exactly once, by both campaigns jointly.** Any other reading — M1 as a separate earlier or later interface commit, or "the sibling folds `MarkBindingUnhealthy`/`BindingUnhealthy` immediately after" — re-opens FM-12: the interface would be edited twice, and both branches adding `ErrTokenRejected`/`ErrTokenScopeMissing` produce a loud merge conflict, not a silent one.
- **Cross-branch note:** the sibling's own D3 previously pointed at a third variant (cmd/migrate, void `Invalidate(SourceBinding)` + `ObserveTokenScopeMissing`). That variant is superseded by this resolution: error-returning `Invalidate(SourceBinding)` and `ObserveAuditScopeMissing` are the single names; no third signature may be introduced.

## 7. Per-campaign delta summary

| Element | Tenantid spec (this campaign) | Sibling spec (cmd-server) |
|---|---|---|
| `Invalidate` | `(tenantID string)` void → **`(SourceBinding) error`**; publisher call site takes the binding | unchanged (is the reference) |
| 403 | `Invalidate` + `ErrTokenRejected` → **`MarkBindingUnhealthy` + `ErrTokenScopeMissing`**, cache retained, cooldown | unchanged |
| Scope sentinel | `ErrScopeRejected` → **`ErrTokenScopeMissing`** | unchanged |
| Token-endpoint classification | kept (`OnScopeRejected`, `ErrTokenScopeMissing: HTTP 400`; single `tokenErrorCode(body)`/`scopeRejectedCode` classifier, §3 pin) | **new** (was FM-4 out-of-scope; additive, does not violate its B-1 boundary: token-endpoint 401/403 still `ErrTokenUnavailable`) |
| Events-401 scope body (C-4) | **new (C2, additive):** scope-body 401 → `ErrTokenScopeMissing` + service counter (mutually exclusive with `ErrTokenRejected`); `Invalidate` stays status-driven | n/a — C1's 401 branch is plain `ErrTokenRejected` |
| Service routing | both sentinels → generic ladder → **scope-missing → `errAuditTenantUnbound` branch + `ObserveAuditScopeMissing`** | unchanged |
| Metrics seam | `OnScopeRejected` hook bound at composition root to the same family | `ObserveAuditScopeMissing` service seam (unchanged) |
| Wiring | hook closure in `OAuthConfig` literal (audit.go:99-115) | metrics setter loop extension at audit.go:52-54; **new.go untouched** (D1 anchors stale) |
| Fakes | n/a | `staticTokenSource` gains the three methods (its "no fakes" claim missed the integration tree) |

## 8. Test/acceptance impact

- Sibling AT-1..AT-10, B-1..B-3 land in C1 as written (the merged interface is its interface; T-10.1d-style 403 assertions are already AT-5/AT-6 — the bulkhead design's §6 lists them as the events-403 (sibling C1) row).
- Tenantid T-10.1a/b/c land in C2 (requirements numbering, per the **tenantid** design's §6 — the merged test matrix with the T-10.1/T-10.2.x/T-10.1d/T-5 names lives in the bulkhead design's §6, which is the implementation source of truth): T-10.1a (401 → invalidate → exactly one refetch, idempotency key preserved), T-10.1b (epoch race, coalescing, stale-result discard — with the two-stage-handler rewrite and the FM-3 terminal-bound negative in the design's §6), and T-10.1c (stateful 401 → refresh + redelivery) are unchanged in intent. **T-10.1d.1 is marked rewritten-by-C-C** in the design's acceptance mapping: events 403 → `ErrTokenScopeMissing`, `BindingUnhealthy(binding)` true, `cachedToken()` **retained**, epoch unchanged, **no connector hook** — the hook-on-403 assertion is superseded by the service-side `ObserveAuditScopeMissing` counter (merged plan §3/§4). T-10.1d.2 (`invalid_scope` → `ErrTokenScopeMissing` + hook exactly once; 500/nil-hook negatives) asserts the new sentinel name and the service routing through `errAuditTenantUnbound`; T-10.1e/f (panicking and re-entrant hook) land per the hazard-resolution companion.
- Gate for both commits: `make test && make lint && make lint-strict && make check-file-sizes && make build`; run `make test-integration` on C1 (interface + `staticTokenSource`) — plus the Docker-free `make check-integration-compile` (§6 Atomicity, supplied by the design §3.3, wired into M1/M5 and the AGENTS.md done gate for the interface window) and `go test -race ./internal/connector/auditgovernance/...` at the design's M5. No SQL/storage/migration change.
