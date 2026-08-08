# Resolution — process-crash hazard in `OnScopeRejected` (singleflight panic re-raise + hook self-join)

| | |
|---|---|
| Hazard | The `OnScopeRejected` hook runs inside the singleflight flight fn (`fetchToken`). x/sync v0.20.0 re-raises a flight panic in a fresh goroutine — an **unrecoverable process crash** — and never delivers a result to coalesced waiters. A hook that calls `AccessToken` for the same tenant **self-joins the flight forever**. |
| Fix | In-package **recover-wrap of the hook invocation** inside the flight (`notifyScopeRejected`) + **per-tenant re-entrancy guard** (`hookRunning atomic.Bool`) checked before `DoChan`. |
| Verified against | `golang.org/x/sync@v0.20.0` module cache (`singleflight.go`), repo head `13e4676`; fix + tests compiled and run in a scratch module (`go test ./auditgovernance/ -count=1` — **no `-race`**): 10/10 green (7 existing + 3 new), mutation-tested both failure directions. |
| Applies to | tenantid campaign design `docs/auto/runs/add-tokensource-invalidate-tenantid-driven-by-go-077b85ef/artifacts/design-a77de8a6/task-1-design.md` and the merged plan `docs/plans/2026-08-07-auditgovernance-tokensource-merged-interface.md` (commit C2) |

## 1. Hazard verification (x/sync v0.20.0, exact)

The flight fn is invoked via `Group.DoChan` (oauth.go:126-130). In v0.20.0's `doCall` (singleflight.go:184-219):

1. A panic inside `fn` is captured by the inner defer as `*panicError` and stored on `c.err` (singleflight.go:192-193).
2. The cleanup defer then runs: `c.wg.Done()`, the key is deleted from the map, and — because `len(c.chans) > 0` (DoChan always appends the caller's channel at :172-178) — it executes `go panic(e); select {}` (singleflight.go:201-207): the panic is **re-raised in a fresh goroutine that blocks forever**. This is an unrecoverable process crash by design ("ensure that this panic cannot be recovered").
3. The waiter loop `ch <- Result{...}` (singleflight.go:210-212) sits in the `else` branch — **it never runs**. Every coalesced waiter's channel receives nothing; waiters block until their own ctx deadline (or forever, with a non-cancellable ctx). The flight goroutine stays alive in `select {}`, so it is also unrecoverable by the runtime.

Consequences for the design's `OnScopeRejected`:

- **Panic crash + waiter starvation.** The design's failure-mode table ("a panicking hook propagates into `fetchToken`… acceptable… documented, not recovered") is materially wrong: the hook runs inside the flight fn, so a panicking composition-root hook kills the whole aero-id process and starves every coalesced waiter.
- **Re-entrancy self-join.** `DoChan` for the in-flight key appends the waiter's channel and returns without starting work (:172-178). A hook calling `AccessToken` for the same tenant parks on the very flight it is executing inside. The flight never completes, the key is never deleted, and **every subsequent same-tenant fetch parks forever** — a permanent tenant outage, not a transient hang.

Both failure directions were confirmed by mutation testing (see §6).

## 2. The fix — recover-wrap the hook invocation inside the flight

Chosen over "invoke it outside the flight" for three reasons:

1. The scope classification is only observable inside the flight fn. Hoisting the hook out requires the flight to return classification + status to all waiters, **exactly-once leader election** among them (`outcome.Shared`), and a post-flight invocation point.
2. Hoisting does **not** remove the panic hazard — a panicking hook would then crash the leader's caller unless recovered at the `Publish` boundary anyway. Recovery is required in both designs.
3. Recover-wrapping in place is strictly simpler, keeps the hook firing at classification time, and fully contains both the crash and the starvation: the flight completes, `ErrTokenScopeMissing` is delivered to every waiter, the process survives.

**Deliberate boundary:** only composition-root-injected code (the hook) is recover-wrapped. Package-owned flight code (`fetchToken`, `cachedToken`, `cacheToken`) stays panic-transparent so genuine bugs remain loud (fail-closed, visible in crash dumps) rather than being masked as `ErrTokenUnavailable`.

### 2.1 Code delta (all in `internal/connector/auditgovernance`)

**types.go** — `OAuthConfig` gains the hook field with the safety contract documented on it:

```go
type OAuthConfig struct {
    SourcePrefix          string
    Timeout               time.Duration
    RefreshSkew           time.Duration
    AllowInsecureLoopback bool
    Bindings              map[string]OAuthBindingConfig
    // OnScopeRejected is invoked exactly once per token-endpoint scope
    // rejection (400 + {"error":"invalid_scope"|"insufficient_scope"}). It
    // runs on the singleflight flight goroutine: it is recover-wrapped and
    // re-entrancy-guarded in-package, so it may panic safely, but it must not
    // call back into the same tenant's AccessToken (the guard returns
    // ErrTokenUnavailable instead of self-joining).
    OnScopeRejected func(tenantID string)
}
```

**oauth.go** — `tenantTokenSource` gains three fields; `accessToken` gains one guard; `fetchToken` routes the classification through the new `notifyScopeRejected`; the flight keeps completing normally in every case:

```go
type tenantTokenSource struct {
    endpoint        *url.URL
    clientID        string
    clientSecret    string
    client          *http.Client
    refreshSkew     time.Duration
    now             func() time.Time
    tenantID        string
    onScopeRejected func(string)
    hookRunning     atomic.Bool // re-entrancy guard; see accessToken
    mu              sync.RWMutex
    token           string
    refreshAt       time.Time
    fetches         singleflight.Group
}
```

`newTenantTokenSource` gains the `tenantID` parameter (the per-tenant source previously did not know its own tenant ID — the design's "plumb the hook through" needs this) and sets `tenantID`/`onScopeRejected` from it and from `config.OnScopeRejected`.

Guard in `accessToken` — placed after the cache fast path, immediately before the only join point:

```go
func (s *tenantTokenSource) accessToken(ctx context.Context) (string, error) {
    if token, ok := s.cachedToken(); ok {
        return token, nil
    }
    if s.hookRunning.Load() {
        // The OnScopeRejected hook is executing on this source. Joining the
        // flight would self-join the in-flight singleflight call (x/sync has
        // no cancellation) and deadlock forever; fail fast instead.
        return "", ErrTokenUnavailable
    }
    result := s.fetches.DoChan(key, func() (any, error) { ... s.fetchToken(ctx, epoch) })
    ...
}
```

Classification + recover + guard in `fetchToken`'s non-200 branch and its helper (the epoch machinery from the design is orthogonal and unchanged):

```go
// in fetchToken, non-200 branch — the body was drained bounded by
// maxTokenBodyBytes at the call site; classification gate pinned to the
// merged plan §3 table: 400 + parseable body only; any other token-endpoint
// status, incl. 401/403, stays ErrTokenUnavailable — sibling FM-4 boundary,
// no invalidate, no mark, no hook:
if response.StatusCode == http.StatusBadRequest && scopeRejectedCode(tokenErrorCode(body)) {
    s.notifyScopeRejected()
    return "", fmt.Errorf("%w: HTTP %d", ErrTokenScopeMissing, response.StatusCode)
}
return "", fmt.Errorf("%w: HTTP %d", ErrTokenUnavailable, response.StatusCode)

// notifyScopeRejected invokes the composition-root hook exactly once per
// classification, contained in-package. The hook runs inside the singleflight
// flight fn; x/sync v0.20.0 re-raises a flight panic as an unrecoverable
// process crash (go panic(e); select {}) and never delivers to coalesced
// waiters, so a panicking hook must never escape: the deferred recover
// converts it into a classified ErrTokenScopeMissing for every waiter instead.
// The re-entrancy guard (hookRunning) is armed for the hook's duration so a
// hook that calls back into this source fails fast instead of self-joining
// the in-flight flight.
func (s *tenantTokenSource) notifyScopeRejected() {
    hook := s.onScopeRejected
    if hook == nil {
        return
    }
    s.hookRunning.Store(true)
    defer s.hookRunning.Store(false)
    defer func() { _ = recover() }()
    hook(s.tenantID)
}

// tokenErrorCode is the SINGLE classifier helper for both classification
// sites (one signature set, bulkhead design §2.2 — the earlier
// classifyTokenError(status, body) form is dropped): it best-effort parses
// {"error":"<code>"} from an already-drained, bounded body; any parse
// failure yields "" (non-fatal: classification never blocks the publish
// path). Status gates stay at the call sites (token endpoint: 400; events
// endpoint: 401). The token-endpoint site drains bounded by maxTokenBodyBytes;
// the events-endpoint site (Publish's 401 branch) drains bounded by
// maxReceiptBytes and passes the bytes in — no site constructs a synthetic
// *http.Response.
func tokenErrorCode(body []byte) string {
    var envelope struct {
        Error string `json:"error"`
    }
    if json.Unmarshal(body, &envelope) != nil {
        return ""
    }
    return envelope.Error
}

func scopeRejectedCode(code string) bool {
    return code == "invalid_scope" || code == "insufficient_scope"
}
```

### 2.2 Required companion delta (discovered by the regression tests)

**`Publisher.authorization` must propagate the classified error.** As shipped, `authorization` collapses every `AccessToken` error into `ErrTokenUnavailable` (publisher.go:103-120; the collapse is at :113-117). Without a change, `ErrTokenScopeMissing` can never reach `Publish`'s caller — the service routing in the merged plan §4 (`errors.Is(err, auditgovernance.ErrTokenScopeMissing)` → `errAuditTenantUnbound`) could never fire for token-endpoint rejections, and T-10.1c's central assertion is unsatisfiable. Verified: all three new tests failed with `ErrTokenUnavailable` until this delta was added. Minimal delta, preserving the historical collapse for everything else:

```go
token, err := p.tokens.AccessToken(ctx, SourceBinding{TenantID: fact.TenantID, SourceSystem: source})
// The scope classification must escape to the caller (the service routes it
// to audit.binding_unavailable); every other token-source failure keeps the
// historical collapse to ErrTokenUnavailable.
if errors.Is(err, ErrTokenScopeMissing) {
    return "", "", err
}
if err != nil || token == "" || strings.ContainsAny(token, "\r\n") {
    return "", "", ErrTokenUnavailable
}
```

## 3. Re-entrancy guard specification

- **Mechanism:** `hookRunning atomic.Bool` on the per-tenant `tenantTokenSource`. Armed immediately before the hook call, cleared by `defer` (also on panic) — the guard window is exactly the hook's execution.
- **Check point:** in `accessToken`, after the cache fast path, before `DoChan` — the only join point. Cache hits remain available during a hook; only flight joins are refused. Refused calls return `ErrTokenUnavailable` (fail fast, no join, no deadlock). Hooks are composition-root code and must tolerate an error from `AccessToken`.
- **Why `atomic.Bool`, not a plain field:** the hook may spawn goroutines that call back; a plain bool would be a data race the tests would not detect (the done gate runs `go test` without `-race`). Atomic is race-free by construction and stdlib-only (the package's import budget is untouched).
- **Scoping:** the guard lives on the per-tenant source, so a hook may call `AccessToken` for a *different* tenant (a different singleflight group — no self-join possible). Chained cross-tenant re-entry (A's hook → B's source → B's hook → A's source) is documented out of scope: it is pathological composition-root code and would require a global re-entrancy token to break.
- **Composition with the recover:** the guard and the recover are complementary, not alternatives — the recover contains the panic, the guard prevents the deadlock. Worst case: a re-entrant call errors, the hook panics, the recover swallows it, the flight completes with `ErrTokenScopeMissing`.
- **The guard does not replace documentation.** The `OAuthConfig.OnScopeRejected` doc comment (2.1) states the contract; the in-package guard enforces it defensively.

## 4. Corrected failure-mode table

The design's §2.3 row is replaced by two rows:

| Failure mode | Behavior |
|---|---|
| Panicking `OnScopeRejected` | **Recover-wrapped in-package** (`notifyScopeRejected`'s deferred recover inside the flight fn): the flight completes, every coalesced waiter receives `ErrTokenScopeMissing: HTTP 400` (the classification gate is 400 + parseable body, merged plan §3), the process survives. Without it, x/sync v0.20.0 `doCall` re-raises the flight panic in a fresh goroutine (`go panic(e); select {}`) — unrecoverable crash — and never delivers to the waiters (they starve until ctx deadline or forever). Hook panics are swallowed by design (composition-root bug); the classification is preserved. Mutation-verified: removing the recover crashes the test binary at singleflight.go:193. |
| Re-entrant `OnScopeRejected` (calls `AccessToken` for the same tenant) | **Guarded:** `hookRunning atomic.Bool` armed for the hook's duration; `accessToken` returns `ErrTokenUnavailable` before joining the flight. Without it the re-entrant call `DoChan`s the in-flight flight and self-joins forever (singleflight has no cancellation); the flight, the epoch key, and every subsequent same-tenant fetch hang permanently. Guard is per-tenant-source: cross-tenant `AccessToken` from a hook is permitted (separate flight, no self-join). Mutation-verified: removing the guard yields `context deadline exceeded` after the bounded wait. |

The merged plan doc's routing table row ("nothing cached; nil-checked `OnScopeRejected(tenantID)` hook") is amended to "nothing cached; **recover-wrapped, re-entrancy-guarded** `OnScopeRejected(tenantID)` hook".

## 5. Regression tests (verified, run without `-race`)

All three tests ship in the package as `scope_hook_test.go` (in-package, `httptest` loopback + `AllowInsecureLoopback`, `sync`/`sync/atomic` — no new imports beyond what the package tests already use). Verified in the scratch harness: 10/10 green (`7` existing + `3` new), 5 repeated runs stable, and each failure direction mutation-tested (§6).

### 5.1 T-10.1e — recorder hook that panics → `Publish` returns `ErrTokenScopeMissing`, process survives, coalesced waiters served

```go
// TestPublisherSurvivesPanickingScopeHook (T-10.1e): the OnScopeRejected hook
// runs inside the singleflight flight fn. x/sync v0.20.0 re-raises a flight
// panic in a fresh goroutine (go panic(e); select {}) — an unrecoverable
// process crash — and never delivers a result to coalesced waiters. The
// in-package recover must convert the hook panic into the classified
// ErrTokenScopeMissing for every waiter, and the process must survive.
//
// Determinism (go test runs without -race): the token handler blocks until
// release, so the flight is provably in flight when the grace window opens;
// all waits time out (failures depend on time, passes depend on rendezvous
// except the coalescing grace). The single residual timing element is the
// grace window: all N goroutines are released by one barrier and the handler
// is still blocked, so a waiter not yet parked inside the window is
// negligible but not provably impossible; the post-join assertions
// (all ErrTokenScopeMissing, hook exactly once) hold either way.
func TestPublisherSurvivesPanickingScopeHook(t *testing.T) {
    const (
        tenantID = "tenant-a"
        workers  = 4
    )
    fact := &domain.AuditFact{
        ID: idgen.NewID(), TenantID: tenantID, ActorUID: "user-1", Action: "account.profile.updated",
        ResourceType: "account_profile", ResourceID: "account-1", OccurredAt: time.Now().UTC(),
        Payload: map[string]any{"changed_fields": []string{"locale"}},
    }
    var (
        arrivals      atomic.Int64
        hookCalls     atomic.Int64
        hookPanicked  atomic.Bool
        firstArrival  = make(chan struct{})
        secondArrival = make(chan struct{}, workers)
        release       = make(chan struct{})
        releaseOnce   sync.Once
    )
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/api/v1/events" {
            http.NotFound(w, r)
            return
        }
        if arrivals.Add(1) == 1 {
            close(firstArrival)
        } else {
            select {
            case secondArrival <- struct{}{}:
            default:
            }
        }
        <-release
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusBadRequest)
        _ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_scope"})
    }))
    defer server.Close()
    // Release must be idempotent and deferred: httptest.Server.Close blocks
    // until in-flight requests finish, so an early t.Fatal must never leave
    // the handler blocked.
    defer releaseOnce.Do(func() { close(release) })

    tokens, err := NewOAuthTokenSource(OAuthConfig{
        SourcePrefix: "aero-id", AllowInsecureLoopback: true,
        Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
        OnScopeRejected: func(gotTenant string) {
            hookCalls.Add(1)
            if gotTenant != tenantID {
                t.Errorf("hook tenant = %q, want %q", gotTenant, tenantID)
            }
            hookPanicked.Store(true)
            panic("recorder hook boom")
        },
    }, nil)
    if err != nil {
        t.Fatal(err)
    }
    publisher, err := NewPublisher(Config{
        Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
    }, tokens, nil)
    if err != nil {
        t.Fatal(err)
    }

    start := make(chan struct{})
    results := make([]error, workers)
    var wg sync.WaitGroup
    for i := range workers {
        wg.Add(1)
        go func() {
            defer wg.Done()
            <-start
            ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            defer cancel()
            results[i] = publisher.Publish(ctx, fact)
        }()
    }
    close(start)

    select {
    case <-firstArrival:
    case <-time.After(5 * time.Second):
        t.Fatal("no token fetch arrived")
    }
    // The flight is in flight and the cache is empty; every waiter that
    // reaches accessToken must join it. A second arrival inside the grace
    // window means coalescing is broken (or the epoch key changed).
    select {
    case <-secondArrival:
        t.Error("second token fetch while the first flight is still in flight — coalescing broken")
    case <-time.After(250 * time.Millisecond):
    }
    if got := hookCalls.Load(); got != 0 {
        t.Errorf("hook fired %d time(s) before any response was processed", got)
    }

    releaseOnce.Do(func() { close(release) })
    wg.Wait()

    for i, publishErr := range results {
        if !errors.Is(publishErr, ErrTokenScopeMissing) {
            t.Errorf("publish %d error = %v, want ErrTokenScopeMissing", i, publishErr)
        }
        if errors.Is(publishErr, ErrTokenUnavailable) || errors.Is(publishErr, ErrTokenRejected) {
            t.Errorf("publish %d misclassified: %v", i, publishErr)
        }
    }
    if got := hookCalls.Load(); got != 1 {
        t.Errorf("hook calls = %d, want exactly 1", got)
    }
    if !hookPanicked.Load() {
        t.Error("recorder hook did not panic — the panic path was not exercised")
    }
    if got := arrivals.Load(); got != 1 {
        t.Errorf("token fetches = %d, want 1 (single coalesced flight)", got)
    }
    // The source must remain fully functional after the swallowed panic.
    if err := publisher.Publish(t.Context(), fact); !errors.Is(err, ErrTokenScopeMissing) {
        t.Fatalf("post-panic publish error = %v, want ErrTokenScopeMissing", err)
    }
    if got := hookCalls.Load(); got != 2 {
        t.Errorf("hook calls after follow-up publish = %d, want 2", got)
    }
}
```

### 5.2 T-10.1f — re-entrant hook fails fast; guard is scoped per tenant

```go
// TestScopeHookReentrantAccessFailsFast (T-10.1f): a hook that calls
// AccessToken for the same tenant must fail fast with ErrTokenUnavailable
// instead of DoChan-ing the in-flight flight and self-joining forever
// (singleflight has no cancellation; the flight, the epoch key, and every
// future same-tenant fetch would hang). Fully deterministic: the assertions
// compare error values, not timing; the re-entrant call's 2 s context only
// bounds the runtime of a regression (a broken guard yields a deadline
// error, which fails errors.Is(ErrTokenUnavailable)).
func TestScopeHookReentrantAccessFailsFast(t *testing.T) {
    const tenantID = "tenant-a"
    fact := &domain.AuditFact{
        ID: idgen.NewID(), TenantID: tenantID, ActorUID: "user-1", Action: "account.profile.updated",
        ResourceType: "account_profile", ResourceID: "account-1", OccurredAt: time.Now().UTC(),
        Payload: map[string]any{"changed_fields": []string{"locale"}},
    }
    expectedSource, err := TenantSourceID("aero-id", tenantID)
    if err != nil {
        t.Fatal(err)
    }
    var (
        tokenArrivals atomic.Int64
        hookCalls     atomic.Int64
        hookErr       atomic.Value
        // tokens is captured by the hook closure; declare before use.
        tokens *OAuthTokenSource
    )
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/token":
            w.Header().Set("Content-Type", "application/json")
            if tokenArrivals.Add(1) == 1 {
                w.WriteHeader(http.StatusBadRequest)
                _ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_scope"})
                return
            }
            _ = json.NewEncoder(w).Encode(map[string]any{
                "access_token": "service-token-2", "token_type": "Bearer", "expires_in": 300,
            })
        case "/api/v1/events":
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(http.StatusAccepted)
            _ = json.NewEncoder(w).Encode(map[string]any{"receipt": map[string]any{
                "event_id": fact.ID.String(), "tenant_id": tenantID, "status": "ledgered",
                "accepted_at": time.Now().UTC(),
            }})
        default:
            http.NotFound(w, r)
        }
    }))
    defer server.Close()
    tokens, err = NewOAuthTokenSource(OAuthConfig{
        SourcePrefix: "aero-id", AllowInsecureLoopback: true,
        Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
        OnScopeRejected: func(gotTenant string) {
            hookCalls.Add(1)
            ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
            defer cancel()
            _, callErr := tokens.AccessToken(ctx, SourceBinding{TenantID: gotTenant, SourceSystem: expectedSource})
            hookErr.Store(callErr)
        },
    }, nil)
    if err != nil {
        t.Fatal(err)
    }
    publisher, err := NewPublisher(Config{
        Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
    }, tokens, nil)
    if err != nil {
        t.Fatal(err)
    }

    if err := publisher.Publish(t.Context(), fact); !errors.Is(err, ErrTokenScopeMissing) {
        t.Fatalf("publish error = %v, want ErrTokenScopeMissing", err)
    }
    if got := hookCalls.Load(); got != 1 {
        t.Fatalf("hook calls = %d, want 1", got)
    }
    callErr, _ := hookErr.Load().(error)
    if !errors.Is(callErr, ErrTokenUnavailable) {
        t.Fatalf("re-entrant AccessToken error = %v, want ErrTokenUnavailable (fail fast, no self-join)", callErr)
    }
    // The guard must clear after the hook returns: the next publish fetches
    // normally and succeeds end to end.
    if err := publisher.Publish(t.Context(), fact); err != nil {
        t.Fatalf("post-hook publish error = %v, want nil", err)
    }
    if got := tokenArrivals.Load(); got != 2 {
        t.Fatalf("token arrivals = %d, want 2 (one classified fetch + one normal fetch)", got)
    }
}

// TestScopeHookCrossTenantAccessIsNotBlocked pins the guard's scope: it
// lives on the per-tenant source, so a hook may call AccessToken for a
// *different* tenant (a different singleflight group — no self-join) without
// tripping it.
func TestScopeHookCrossTenantAccessIsNotBlocked(t *testing.T) {
    const tenantA, tenantB = "tenant-a", "tenant-b"
    fact := &domain.AuditFact{
        ID: idgen.NewID(), TenantID: tenantA, ActorUID: "user-1", Action: "account.profile.updated",
        ResourceType: "account_profile", ResourceID: "account-1", OccurredAt: time.Now().UTC(),
        Payload: map[string]any{"changed_fields": []string{"locale"}},
    }
    sourceB, err := TenantSourceID("aero-id", tenantB)
    if err != nil {
        t.Fatal(err)
    }
    var (
        hookCalls atomic.Int64
        crossErr  atomic.Value
        tokens    *OAuthTokenSource
    )
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/token":
            clientID, _, _ := r.BasicAuth()
            w.Header().Set("Content-Type", "application/json")
            if clientID == "client-a" {
                w.WriteHeader(http.StatusBadRequest)
                _ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_scope"})
                return
            }
            _ = json.NewEncoder(w).Encode(map[string]any{
                "access_token": "token-for-" + clientID, "token_type": "Bearer", "expires_in": 300,
            })
        case "/api/v1/events":
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(http.StatusAccepted)
            _ = json.NewEncoder(w).Encode(map[string]any{"receipt": map[string]any{
                "event_id": fact.ID.String(), "tenant_id": tenantA, "status": "ledgered",
                "accepted_at": time.Now().UTC(),
            }})
        default:
            http.NotFound(w, r)
        }
    }))
    defer server.Close()
    tokens, err = NewOAuthTokenSource(OAuthConfig{
        SourcePrefix: "aero-id", AllowInsecureLoopback: true,
        Bindings: map[string]OAuthBindingConfig{
            tenantA: auditBinding(server.URL, "client-a"),
            tenantB: auditBinding(server.URL, "client-b"),
        },
        OnScopeRejected: func(string) {
            hookCalls.Add(1)
            token, callErr := tokens.AccessToken(t.Context(), SourceBinding{TenantID: tenantB, SourceSystem: sourceB})
            if callErr != nil {
                crossErr.Store(callErr)
                return
            }
            crossErr.Store(errTokenOK{ok: true, token: token})
        },
    }, nil)
    if err != nil {
        t.Fatal(err)
    }
    publisher, err := NewPublisher(Config{
        Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
    }, tokens, nil)
    if err != nil {
        t.Fatal(err)
    }

    if err := publisher.Publish(t.Context(), fact); !errors.Is(err, ErrTokenScopeMissing) {
        t.Fatalf("publish error = %v, want ErrTokenScopeMissing", err)
    }
    if got := hookCalls.Load(); got != 1 {
        t.Fatalf("hook calls = %d, want 1", got)
    }
    value, _ := crossErr.Load().(errTokenOK)
    if !value.ok || value.token != "token-for-client-b" {
        t.Fatalf("cross-tenant AccessToken = %q, %v; want token-for-client-b, nil", value.token, crossErr.Load())
    }
}

type errTokenOK struct {
    ok    bool
    token string
}
```

### 5.3 Why these tests work without `-race`

- **No assertion reads shared state while other goroutines write it.** All counters/flags are `atomic`; result slices are written by the worker goroutine before `wg.Wait()` returns and read only after the join (join-before-assert). `t.Errorf` from the handler/flight goroutines is safe (only `FailNow`-family is test-goroutine-only).
- **The failure directions are error-value-determined, not timing-determined.** Mutation 1 (recover removed): the test binary dies with the re-raised panic — loud red. Mutation 2 (guard removed): the re-entrant call parks until its 2 s ctx, then returns `context deadline exceeded`, which fails `errors.Is(callErr, ErrTokenUnavailable)` — deterministic red in bounded time; without the ctx bound the outer publish's 10 s/5 s ctx still bounds it. The only timing element is the 250 ms coalescing grace in the **pass** path of T-10.1e (all N goroutines barrier-released while the handler is provably blocked; the residual straggler false-pass is negligible and documented in the test comment).
- **Hang-safety:** every channel wait has a timeout; `release` is idempotent-and-deferred (`sync.Once` + `defer`), so an early `t.Fatal` can never leave the httptest handler blocked (`Server.Close` waits for in-flight requests).
- **Distinct token values** (`service-token-2`, `token-for-<client>`) keep the bearer assertions non-vacuous; the T-10.1e/f fixtures are single-tenant, so no cross-tenant interference.

## 6. Mutation verification (scratch harness, no `-race`)

The fix + tests were built in a scratch module against HEAD-pinned package sources (nested module `github.com/aero/aero-id/hazardcheck` with `replace => repo root`, so the real `internal/domain`/`internal/pkg/idgen` were used; the working tree's unrelated uncommitted publisher.go changes were excluded by copying `git show HEAD:...`):

| Run | Result |
|---|---|
| 7 existing tests + 3 new, `-count=1` | `ok`, 10/10 green, 0.256 s; stable across 5 repeats |
| Mutation: delete `defer func() { _ = recover() }()` | Test binary **crashes**: `panic: recorder hook boom` re-raised from `singleflight.(*Group).doCall.func2.1` (singleflight.go:193) — the exact hazard, loud red |
| Mutation: delete the `hookRunning` guard in `accessToken` | `re-entrant AccessToken error = context deadline exceeded, want ErrTokenUnavailable` after 2.005 s — deterministic red, bounded runtime |
| Mutation: remove the `authorization` propagation (§2.2) | All three tests fail with `ErrTokenUnavailable` — the companion delta is load-bearing for T-10.1c too |

## 7. Delta to the campaign deliverables

- **task-1-design.md**: §2.1 item 3 and the `fetchToken`/`newTenantTokenSource` bullets gain the recover-wrap + guard + `tenantID` plumbing; §2.1 `publisher.go` bullet gains the `authorization` propagation; §2.3 table rows replaced per §4; §2.4 step 2 gains `notifyScopeRejected` (recover + guard), step 3 gains `authorization` propagation; §2.5 gains T-10.1e/f rows.
- **Merged plan doc**: routing-table row and the C2 commit description gain the recover-wrap + guard + `authorization` propagation + T-10.1e/f. C2 remains purely additive (one optional struct field, one `fetchToken` branch, one `authorization` branch, new tests); the hazard fix is epoch-orthogonal and does not change C1's interface commit.
- **Sentinel naming and classification gate (2026-08-07 sweep):** all `ErrScopeRejected` references above are renamed to `ErrTokenScopeMissing` — the merged plan §3 single sentinel set; the old name is dropped and nothing outside the package referenced it. `fetchToken`'s classification is gated on `status == 400` + parseable `invalid_scope`/`insufficient_scope` per the merged plan §3 table (REQ-4's looser "non-200" wording is superseded): token-endpoint 401/403 and 400 with unparseable or unknown bodies stay `ErrTokenUnavailable` (sibling FM-4 boundary); all fixtures and tests in §5 already use 400, and the design's `TestScopeClassificationNegatives` (bulkhead design §6, token-endpoint classification row) adds the token-401-with-invalid_scope-body negative that pins the gate.
- **Composition root (app/audit.go:111-115)**: the hook binding is the same closure and the guard/recover are invisible to the caller; per design C-3 the enclosing `buildAuditPublishers` gains a `controlMetrics service.ControlPlaneMetrics` parameter (call site `buildAuditStack` :51) so the closure can reach the sink, and the closure labels `strings.TrimSpace(settings.SourcePrefix)` (label-seam pin, bulkhead design §2.6 / merged plan §3). The doc comment on `OnScopeRejected` is the contract the composition root must honor (tolerate errors from `AccessToken`, keep the hook short).
- **Gate**: `make test && make lint && make lint-strict && make check-file-sizes && make build`; `notifyScopeRejected`/`tokenErrorCode`/`scopeRejectedCode` are ≤ 50 lines / cyclomatic ≤ 10 each; oauth.go stays ~285 lines ≪ 500. No `test-integration` (httptest loopback only, per the existing pattern).
