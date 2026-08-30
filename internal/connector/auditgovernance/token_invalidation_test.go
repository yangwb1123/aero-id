package auditgovernance

// Acceptance suite for the TokenSource 401-invalidation direction (design
// docs/plans/2026-08-07-internal-pkg-bulkhead-tokensource-invalidate-on-401-design.md
// §6): epoch-fenced singleflight refetch (T-10.1, T-10.2.1, T-10.2.2),
// events-401 scope classification (T-10.2.3), token-endpoint classification
// and scope-claim validation (REQ-4), hook safety (T-10.1e/f — panic
// containment + re-entrancy guard, scope_hook_test.go),
// Invalidate contract (FM-8), C-B propagation, and transport sanitization (F1).
// All tests are in-package httptest loopback fixtures with counting handlers
// (atomic.Int32 — handlers run on server goroutines).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// scopeFact returns a valid governance fact for the fixture tenant.
func scopeFact(tenantID string) *domain.AuditFact {
	return &domain.AuditFact{
		ID: idgen.NewID(), TenantID: tenantID, ActorUID: "user-1", Action: "account.profile.updated",
		ResourceType: "account_profile", ResourceID: "account-1", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"changed_fields": []string{"locale"}},
	}
}

// tokenHandler writes a JSON token response with the given scope claim.
func tokenHandler(w http.ResponseWriter, accessToken, scope string) {
	w.Header().Set("Content-Type", "application/json")
	if scope == "" {
		_, _ = w.Write([]byte(`{"access_token":"` + accessToken + `","token_type":"Bearer","expires_in":300}`))
		return
	}
	_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":300,"scope":%q}`, accessToken, scope)
}

func acceptedReceiptHandler(w http.ResponseWriter, fact *domain.AuditFact) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"receipt": map[string]any{
		"event_id": fact.ID.String(), "tenant_id": fact.TenantID, "status": "ledgered", "accepted_at": time.Now().UTC(),
	}})
}

// TestPublish401InvalidatesAndRefetchesExactlyOnce (T-10.1): the first Publish
// hits a 401 (empty body), the token is invalidated immediately (epoch +1, no
// time travel), and the very next Publish refetches — /token == 2 total —
// carrying the refetched token and the identical event_id/idempotency_key.
func TestPublish401InvalidatesAndRefetchesExactlyOnce(t *testing.T) {
	const tenantID = "tenant-a"
	fact := scopeFact(tenantID)
	var (
		tokenPosts  atomic.Int32
		eventsPosts atomic.Int32
		authHeaders = make(chan string, 4)
		eventBodies = make(chan map[string]any, 4)
		events401   atomic.Bool
	)
	events401.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			token := fmt.Sprintf("service-token-%d", tokenPosts.Add(1))
			tokenHandler(w, token, "")
		case "/api/v1/events":
			eventsPosts.Add(1)
			authHeaders <- r.Header.Get("Authorization")
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode event body: %v", err)
			}
			eventBodies <- body
			if events401.Load() {
				// First request only: empty-body 401 (plain rejection).
				events401.Store(false)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			acceptedReceiptHandler(w, fact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
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

	// 1. First publish: plain 401 → ErrTokenRejected, mutually exclusive with
	// the scope-missing sentinel.
	firstErr := publisher.Publish(t.Context(), fact)
	if !errors.Is(firstErr, ErrTokenRejected) || errors.Is(firstErr, ErrTokenScopeMissing) || errors.Is(firstErr, ErrEventRejected) {
		t.Fatalf("first publish error = %v, want ErrTokenRejected (not scope-missing)", firstErr)
	}
	if got := eventsPosts.Add(0); got != 1 {
		t.Fatalf("events posts = %d, want 1", got)
	}

	// 2. Immediately (no time advance): the invalidation, not the refresh
	// window, dropped the token — epoch bumped by exactly 1.
	if token, ok := tokens.bindings[tenantID].cachedToken(); ok || token != "" {
		t.Fatalf("cached token after 401 = %q, %v; want empty", token, ok)
	}
	if epoch := tokens.bindings[tenantID].epoch; epoch != 1 {
		t.Fatalf("epoch after 401 = %d, want 1", epoch)
	}

	// 3. Second publish (same fact): immediate refetch serves it.
	if err := publisher.Publish(t.Context(), fact); err != nil {
		t.Fatalf("second publish error = %v, want nil", err)
	}
	if got := tokenPosts.Load(); got != 2 {
		t.Fatalf("token posts = %d, want 2 (immediate refetch)", got)
	}
	firstAuth, secondAuth := <-authHeaders, <-authHeaders
	if firstAuth != "Bearer service-token-1" {
		t.Errorf("first events auth = %q, want the rejected token", firstAuth)
	}
	if secondAuth != "Bearer service-token-2" {
		t.Errorf("second events auth = %q, want the refetched token (never the rejected one)", secondAuth)
	}
	firstBody, secondBody := <-eventBodies, <-eventBodies
	if firstBody["event_id"] != fact.ID.String() || secondBody["event_id"] != fact.ID.String() {
		t.Fatalf("event_id must be identical across retries: %v / %v", firstBody["event_id"], secondBody["event_id"])
	}
	if firstBody["idempotency_key"] != fact.ID.String() || secondBody["idempotency_key"] != fact.ID.String() {
		t.Fatalf("idempotency_key must equal fact.ID.String(): %v / %v", firstBody["idempotency_key"], secondBody["idempotency_key"])
	}
}

// TestConcurrentPublishesAfter401CoalesceToOneFetch (T-10.2.1): N = 8
// concurrent publishes after an invalidation join exactly one epoch-keyed
// flight — /token == 2 total (prime + one coalesced refetch). Standalone
// fixture: never reuses T-10.1's shared state (there T2 is cached and the N
// callers would never reach DoChan, passing /token == 2 vacuously). The
// refetch POST is channel-gated so all N join the one in-flight flight; the
// wait-for-all-then-assert pin makes a descheduled straggler (which would
// start a third POST) fail loudly before returning.
func TestConcurrentPublishesAfter401CoalesceToOneFetch(t *testing.T) {
	const (
		tenantID = "tenant-a"
		workers  = 8
	)
	fact := scopeFact(tenantID)
	var (
		tokenPosts    atomic.Int32
		refetchArrive = make(chan struct{})
		refetchOnce   sync.Once
		release       = make(chan struct{})
		releaseOnce   sync.Once
		postPrimeAuth atomic.Int32 // events requests carrying the refetched token
	)
	defer releaseOnce.Do(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if tokenPosts.Add(1) == 1 {
				// Prime POST: serves the token that the explicit Invalidate
				// below then evicts.
				tokenHandler(w, "service-token-1", "")
				return
			}
			refetchOnce.Do(func() { close(refetchArrive) })
			<-release
			tokenHandler(w, "service-token-2", "")
		case "/api/v1/events":
			if r.Header.Get("Authorization") == "Bearer service-token-2" {
				postPrimeAuth.Add(1)
			}
			acceptedReceiptHandler(w, fact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
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

	// Prime + explicit Invalidate: pinned cache-empty precondition, epoch
	// bumped, so every concurrent caller genuinely misses the fast path.
	if err := publisher.Publish(t.Context(), fact); err != nil {
		t.Fatal(err)
	}
	if err := tokens.Invalidate(SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}); err != nil {
		t.Fatal(err)
	}
	if epoch := tokens.bindings[tenantID].epoch; epoch != 1 {
		t.Fatalf("epoch after prime + Invalidate = %d, want 1", epoch)
	}

	start := make(chan struct{})
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = publisher.Publish(t.Context(), fact)
		}()
	}
	close(start)
	select {
	case <-refetchArrive:
	case <-time.After(5 * time.Second):
		t.Fatal("no refetch POST arrived — the flight never started")
	}
	// Grace window: all N goroutines were barrier-released and the flight is
	// provably in flight (its POST is gated); give the remaining workers time
	// to join it before release (documented residual: a straggler that misses
	// the completed flight starts POST #3 and fails the /token pin below).
	time.Sleep(250 * time.Millisecond)
	releaseOnce.Do(func() { close(release) })
	wg.Wait()

	for i, publishErr := range errs {
		if publishErr != nil {
			t.Errorf("publish %d error = %v, want nil", i, publishErr)
		}
	}
	if got := tokenPosts.Load(); got != 2 {
		t.Fatalf("token posts = %d, want 2 (prime + exactly one coalesced refetch)", got)
	}
	if got := postPrimeAuth.Load(); got != workers {
		t.Fatalf("post-prime events requests with refetched token = %d, want %d", got, workers)
	}
}

func mustSource(t *testing.T, tenantID string) string {
	t.Helper()
	source, err := TenantSourceID("aero-id", tenantID)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// TestStaleInflightFetchDiscardedByEpoch (T-10.2.2 Part 1): a fetch in flight
// when Invalidate bumps the epoch must be discarded (never repopulate the
// cache with the rejected token) and the waiter re-enters once against the
// fresh epoch. The prime is direct in-package cache injection — an HTTP prime
// would make /token == 3 and break the pin (concurrency review finding 2).
func TestStaleInflightFetchDiscardedByEpoch(t *testing.T) {
	const tenantID = "tenant-a"
	fact := scopeFact(tenantID)
	var (
		tokenPosts   atomic.Int32
		firstBlock   = make(chan struct{}) // gates the stale refetch POST
		secondBlock  = make(chan struct{}) // gates the re-entry POST
		firstArrive  = make(chan struct{})
		secondArrive = make(chan struct{})
		// One Once per channel: a shared Once would consume its Do on the
		// first arrival and never close the second channel (T-10.2.2 hang).
		firstOnce  sync.Once
		secondOnce sync.Once
	)
	release := func(ch chan struct{}) {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	defer release(firstBlock)
	defer release(secondBlock)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/events" {
			acceptedReceiptHandler(w, fact)
			return
		}
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		if tokenPosts.Add(1) == 1 {
			// Arrival is signaled BEFORE the gate so the test can observe the
			// POST in flight; the gate then blocks the response until release.
			firstOnce.Do(func() { close(firstArrive) })
			<-firstBlock
			tokenHandler(w, "service-token-stale", "")
			return
		}
		secondOnce.Do(func() { close(secondArrive) })
		<-secondBlock
		tokenHandler(w, "service-token-2", "")
	}))
	defer server.Close()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
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

	// Prime the cache directly (epoch 0), then invalidate (epoch 1).
	source := tokens.bindings[tenantID]
	source.mu.Lock()
	source.token = "service-token-1"
	source.refreshAt = time.Now().Add(time.Hour)
	source.mu.Unlock()

	if err := tokens.Invalidate(SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}); err != nil {
		t.Fatal(err)
	}
	if epoch := source.epoch; epoch != 1 {
		t.Fatalf("epoch after prime + Invalidate = %d, want 1", epoch)
	}

	// Start the publish: it joins flight client-1 and blocks in fetchToken.
	publishResult := make(chan error, 1)
	go func() {
		publishResult <- publisher.Publish(t.Context(), fact)
	}()
	select {
	case <-firstArrive:
	case <-time.After(5 * time.Second):
		t.Fatal("stale refetch POST never arrived")
	}
	// Mid-flight Invalidate: epoch 1 → 2. The stale result must be discarded.
	if err := tokens.Invalidate(SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}); err != nil {
		t.Fatal(err)
	}
	release(firstBlock)
	select {
	case <-secondArrive:
	case <-time.After(5 * time.Second):
		t.Fatal("re-entry POST never arrived — the discard sentinel was not honored")
	}
	// Deterministic quiescence: the stale discard completed before the
	// discard result was delivered (cacheToken runs inside the flight fn),
	// and nothing writes the cache while the re-entry POST is blocked.
	if token, ok := source.cachedToken(); ok || token != "" {
		t.Fatalf("cache after stale discard = %q, %v; want empty (no poisoned re-entry)", token, ok)
	}
	release(secondBlock)
	select {
	case publishErr := <-publishResult:
		if publishErr != nil {
			t.Fatalf("publish error = %v, want nil", publishErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publish never completed after re-entry")
	}
	if got := tokenPosts.Load(); got != 2 {
		t.Fatalf("token posts = %d, want 2 (stale + re-entry)", got)
	}
	source.mu.RLock()
	cached, ok := source.token, source.token != "" && time.Now().Before(source.refreshAt)
	epoch := source.epoch
	source.mu.RUnlock()
	if !ok || cached != "service-token-2" {
		t.Fatalf("final cache = %q, %v; want service-token-2 (never the stale token)", cached, ok)
	}
	if epoch != 2 {
		t.Fatalf("epoch = %d, want 2 (prime 0 → invalidate 1 → mid-flight invalidate 2)", epoch)
	}
}

// TestSecondConsecutiveDiscardIsTerminal (T-10.2.2 Part 2, FM-3): a second
// consecutive epoch discard exhausts the bounded re-entry — Publish returns
// ErrTokenUnavailable and no third POST is ever issued (no livelock under a
// 401 storm).
func TestSecondConsecutiveDiscardIsTerminal(t *testing.T) {
	const tenantID = "tenant-a"
	fact := scopeFact(tenantID)
	var (
		tokenPosts   atomic.Int32
		firstBlock   = make(chan struct{})
		secondBlock  = make(chan struct{})
		firstArrive  = make(chan struct{})
		secondArrive = make(chan struct{})
		// One Once per channel: a shared Once would consume its Do on the
		// first arrival and never close the second channel (T-10.2.2 hang).
		firstOnce  sync.Once
		secondOnce sync.Once
	)
	release := func(ch chan struct{}) {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	defer release(firstBlock)
	defer release(secondBlock)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/events" {
			acceptedReceiptHandler(w, fact)
			return
		}
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		if tokenPosts.Add(1) == 1 {
			// Arrival is signaled BEFORE the gate so the test can observe the
			// POST in flight; the gate then blocks the response until release.
			firstOnce.Do(func() { close(firstArrive) })
			<-firstBlock
			tokenHandler(w, "service-token-stale", "")
			return
		}
		secondOnce.Do(func() { close(secondArrive) })
		<-secondBlock
		tokenHandler(w, "service-token-2", "")
	}))
	defer server.Close()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
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
	source := tokens.bindings[tenantID]
	source.mu.Lock()
	source.token = "service-token-1"
	source.refreshAt = time.Now().Add(time.Hour)
	source.mu.Unlock()

	// Same trace as Part 1 up to the blocked re-entry POST: prime (epoch 0),
	// invalidate (1), publish → POST #1 blocked, mid-flight invalidate (2),
	// release → discard → re-entry POST #2 blocked.
	if err := tokens.Invalidate(SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}); err != nil {
		t.Fatal(err)
	}
	publishResult := make(chan error, 1)
	go func() {
		publishResult <- publisher.Publish(t.Context(), fact)
	}()
	select {
	case <-firstArrive:
	case <-time.After(5 * time.Second):
		t.Fatal("stale refetch POST never arrived")
	}
	if err := tokens.Invalidate(SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}); err != nil {
		t.Fatal(err)
	}
	release(firstBlock)
	select {
	case <-secondArrive:
	case <-time.After(5 * time.Second):
		t.Fatal("re-entry POST never arrived")
	}
	// Invalidate again (epoch 3) while the re-entry POST is blocked: the
	// second consecutive discard is terminal — no third POST.
	if err := tokens.Invalidate(SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}); err != nil {
		t.Fatal(err)
	}
	release(secondBlock)
	select {
	case publishErr := <-publishResult:
		if !errors.Is(publishErr, ErrTokenUnavailable) {
			t.Fatalf("publish error = %v, want ErrTokenUnavailable (re-entry bound exhausted)", publishErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publish never completed after the terminal discard")
	}
	if got := tokenPosts.Load(); got != 2 {
		t.Fatalf("token posts = %d, want 2 (no third POST under the storm bound)", got)
	}
	source.mu.RLock()
	cached := source.token
	epoch := source.epoch
	source.mu.RUnlock()
	if cached != "" {
		t.Fatalf("cache = %q, want empty after the terminal discard", cached)
	}
	if epoch != 3 {
		t.Fatalf("epoch = %d, want 3", epoch)
	}
}

// TestEvents401WithScopeBodyIsScopeMissingAlertedOnce (T-10.2.3): a 401 whose
// drained body parses to invalid_scope is ErrTokenScopeMissing (mutually
// exclusive with ErrTokenRejected), the status-driven Invalidate still evicts
// the primed token, and the connector hook stays silent (C-C: the hook is the
// token-endpoint seam only — the service-side counter covers events-endpoint
// classifications).
func TestEvents401WithScopeBodyIsScopeMissingAlertedOnce(t *testing.T) {
	const tenantID = "tenant-a"
	fact := scopeFact(tenantID)
	var (
		eventsPosts  atomic.Int32
		tokenPosts   atomic.Int32
		hookCalls    atomic.Int32
		events401set atomic.Bool
	)
	events401set.Store(false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenHandler(w, fmt.Sprintf("service-token-%d", tokenPosts.Add(1)), "")
		case "/api/v1/events":
			// Prime request: 202. After the flip: 401 with a scope body.
			if !events401set.Load() {
				events401set.Store(true)
				acceptedReceiptHandler(w, fact)
				return
			}
			eventsPosts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_scope"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
		OnScopeRejected: func(string) {
			hookCalls.Add(1)
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
	// Prime via a successful publish (T1 cached, epoch 0) — without the prime
	// the "cache empty and epoch bumped" assertions would be trivially true
	// and the status-driven Invalidate would never be demonstrably exercised.
	if err := publisher.Publish(t.Context(), fact); err != nil {
		t.Fatal(err)
	}
	if epoch := tokens.bindings[tenantID].epoch; epoch != 0 {
		t.Fatalf("epoch after prime = %d, want 0", epoch)
	}

	publishErr := publisher.Publish(t.Context(), fact)
	if !errors.Is(publishErr, ErrTokenScopeMissing) || errors.Is(publishErr, ErrTokenRejected) || errors.Is(publishErr, ErrEventRejected) {
		t.Fatalf("publish error = %v, want ErrTokenScopeMissing (mutually exclusive with ErrTokenRejected)", publishErr)
	}
	if got := eventsPosts.Load(); got != 1 {
		t.Fatalf("events posts = %d, want 1", got)
	}
	// Status-driven Invalidate ran: T1 evicted, epoch bumped by exactly 1.
	if token, ok := tokens.bindings[tenantID].cachedToken(); ok || token != "" {
		t.Fatalf("cached token after 401-with-scope-body = %q, %v; want empty", token, ok)
	}
	if epoch := tokens.bindings[tenantID].epoch; epoch != 1 {
		t.Fatalf("epoch after 401-with-scope-body = %d, want 1", epoch)
	}
	// C-C: the connector hook is token-endpoint-only; events-endpoint
	// classifications fire the service-side counter instead.
	if got := hookCalls.Load(); got != 0 {
		t.Fatalf("connector hook calls = %d, want 0 for an events-endpoint response", got)
	}
}

// TestEvents401ScopeBodyNilHookDoesNotPanic: the events-endpoint
// classification path (which never invokes the connector hook) must not panic
// when no OnScopeRejected hook is configured.
func TestEvents401ScopeBodyNilHookDoesNotPanic(t *testing.T) {
	const tenantID = "tenant-a"
	fact := scopeFact(tenantID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenHandler(w, "service-token-1", "")
		case "/api/v1/events":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "insufficient_scope"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
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
	publishErr := publisher.Publish(t.Context(), fact)
	if !errors.Is(publishErr, ErrTokenScopeMissing) {
		t.Fatalf("publish error = %v, want ErrTokenScopeMissing (no panic, no hook configured)", publishErr)
	}
}

// TestTokenEndpointInvalidScopeFiresHookOnce (REQ-4): a 400 + invalid_scope
// token-endpoint response classifies as ErrTokenScopeMissing (propagated past
// authorization's collapse), fires the hook exactly once with the tenant ID,
// and caches nothing.
func TestTokenEndpointInvalidScopeFiresHookOnce(t *testing.T) {
	const tenantID = "tenant-a"
	fact := scopeFact(tenantID)
	var hookCalls atomic.Int32
	var hookTenant atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_scope"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
		OnScopeRejected: func(got string) {
			hookCalls.Add(1)
			hookTenant.Store(got)
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

	publishErr := publisher.Publish(t.Context(), fact)
	if !errors.Is(publishErr, ErrTokenScopeMissing) {
		t.Fatalf("publish error = %v, want ErrTokenScopeMissing (C-B propagation past authorization)", publishErr)
	}
	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("hook calls = %d, want exactly 1", got)
	}
	if got, _ := hookTenant.Load().(string); got != tenantID {
		t.Fatalf("hook tenant = %q, want %q", got, tenantID)
	}
	if token, ok := tokens.bindings[tenantID].cachedToken(); ok || token != "" {
		t.Fatalf("cached token after token-endpoint rejection = %q, %v; want empty", token, ok)
	}
}

// TestTokenEndpointScopeClaimValidation (REQ-4/FM-13/F2): a successful token
// response's scope claim is validated as an exact space-delimited token.
// Absent and null claims are accepted (server may omit scope when identical
// to the request); a present claim must contain audit:event:write; an
// explicit empty claim fails closed.
func TestTokenEndpointScopeClaimValidation(t *testing.T) {
	const tenantID = "tenant-a"
	newFixture := func(t *testing.T, tokenBody string, hook func(string)) (*OAuthTokenSource, *Publisher, *domain.AuditFact) {
		t.Helper()
		fact := scopeFact(tenantID)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/token":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tokenBody))
			case "/api/v1/events":
				acceptedReceiptHandler(w, fact)
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		tokens, err := NewOAuthTokenSource(OAuthConfig{
			SourcePrefix: "aero-id", AllowInsecureLoopback: true,
			Bindings:        map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
			OnScopeRejected: hook,
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
		return tokens, publisher, fact
	}

	t.Run("missing write scope is rejected", func(t *testing.T) {
		var hookCalls atomic.Int32
		tokens, publisher, fact := newFixture(t,
			`{"access_token":"service-token","token_type":"Bearer","expires_in":300,"scope":"audit:event:read"}`,
			func(string) { hookCalls.Add(1) })
		publishErr := publisher.Publish(t.Context(), fact)
		if !errors.Is(publishErr, ErrTokenScopeMissing) {
			t.Fatalf("publish error = %v, want ErrTokenScopeMissing", publishErr)
		}
		if got := hookCalls.Load(); got != 1 {
			t.Fatalf("hook calls = %d, want 1", got)
		}
		if token, ok := tokens.bindings[tenantID].cachedToken(); ok || token != "" {
			t.Fatalf("token cached despite missing scope = %q, %v", token, ok)
		}
	})
	t.Run("write scope among others is accepted", func(t *testing.T) {
		tokens, publisher, fact := newFixture(t,
			`{"access_token":"service-token","token_type":"Bearer","expires_in":300,"scope":"audit:event:read audit:event:write"}`,
			nil)
		if err := publisher.Publish(t.Context(), fact); err != nil {
			t.Fatalf("publish error = %v, want nil", err)
		}
		if token, ok := tokens.bindings[tenantID].cachedToken(); !ok || token == "" {
			t.Fatalf("token not cached after valid scope claim")
		}
	})
	t.Run("absent claim is accepted", func(t *testing.T) {
		tokens, publisher, fact := newFixture(t,
			`{"access_token":"service-token","token_type":"Bearer","expires_in":300}`, nil)
		if err := publisher.Publish(t.Context(), fact); err != nil {
			t.Fatalf("publish error = %v, want nil (absent scope accepted)", err)
		}
		if _, ok := tokens.bindings[tenantID].cachedToken(); !ok {
			t.Fatal("token not cached when scope claim is absent")
		}
	})
	t.Run("explicit empty claim is rejected", func(t *testing.T) {
		var hookCalls atomic.Int32
		tokens, publisher, fact := newFixture(t,
			`{"access_token":"service-token","token_type":"Bearer","expires_in":300,"scope":""}`,
			func(string) { hookCalls.Add(1) })
		publishErr := publisher.Publish(t.Context(), fact)
		if !errors.Is(publishErr, ErrTokenScopeMissing) {
			t.Fatalf("publish error = %v, want ErrTokenScopeMissing (explicit empty is a rejection, not absence)", publishErr)
		}
		if got := hookCalls.Load(); got != 1 {
			t.Fatalf("hook calls = %d, want 1", got)
		}
		if token, ok := tokens.bindings[tenantID].cachedToken(); ok || token != "" {
			t.Fatalf("token cached despite empty scope claim = %q, %v", token, ok)
		}
	})
	t.Run("null claim is accepted", func(t *testing.T) {
		tokens, publisher, fact := newFixture(t,
			`{"access_token":"service-token","token_type":"Bearer","expires_in":300,"scope":null}`, nil)
		if err := publisher.Publish(t.Context(), fact); err != nil {
			t.Fatalf("publish error = %v, want nil (null == absent, documented boundary)", err)
		}
		if _, ok := tokens.bindings[tenantID].cachedToken(); !ok {
			t.Fatal("token not cached when scope claim is null")
		}
	})
	t.Run("lookalike scope tokens cannot pass", func(t *testing.T) {
		var hookCalls atomic.Int32
		tokens, publisher, fact := newFixture(t,
			`{"access_token":"service-token","token_type":"Bearer","expires_in":300,"scope":"xaudit:event:write audit:event:write-extra"}`,
			func(string) { hookCalls.Add(1) })
		publishErr := publisher.Publish(t.Context(), fact)
		if !errors.Is(publishErr, ErrTokenScopeMissing) {
			t.Fatalf("publish error = %v, want ErrTokenScopeMissing (exact token equality)", publishErr)
		}
		if got := hookCalls.Load(); got != 1 {
			t.Fatalf("hook calls = %d, want 1", got)
		}
		if token, ok := tokens.bindings[tenantID].cachedToken(); ok || token != "" {
			t.Fatalf("token cached despite lookalike scope = %q, %v", token, ok)
		}
	})
}

// TestScopeClassificationNegatives (FM-10, 400-gate): token-endpoint
// classifications are gated on status == 400 with a parseable scope code —
// 500, 401-with-scope-body, and 400-with-unknown-code all stay
// ErrTokenUnavailable with no hook; a nil hook never panics.
func TestScopeClassificationNegatives(t *testing.T) {
	const tenantID = "tenant-a"
	newFixture := func(t *testing.T, tokenStatus int, tokenBody string, hook func(string)) (*OAuthTokenSource, *Publisher, *domain.AuditFact) {
		t.Helper()
		fact := scopeFact(tenantID)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/token":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tokenStatus)
				_, _ = w.Write([]byte(tokenBody))
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		tokens, err := NewOAuthTokenSource(OAuthConfig{
			SourcePrefix: "aero-id", AllowInsecureLoopback: true,
			Bindings:        map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
			OnScopeRejected: hook,
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
		return tokens, publisher, fact
	}
	check := func(t *testing.T, name string, status int, body string, want error) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			var hookCalls atomic.Int32
			_, publisher, fact := newFixture(t, status, body, func(string) { hookCalls.Add(1) })
			publishErr := publisher.Publish(t.Context(), fact)
			if !errors.Is(publishErr, want) {
				t.Fatalf("publish error = %v, want %v", publishErr, want)
			}
			if got := hookCalls.Load(); got != 0 {
				t.Fatalf("hook calls = %d, want 0", got)
			}
		})
	}
	check(t, "token endpoint 500", http.StatusInternalServerError, `{}`, ErrTokenUnavailable)
	check(t, "token endpoint 401 with scope body", http.StatusUnauthorized,
		`{"error":"invalid_scope"}`, ErrTokenUnavailable)
	check(t, "token endpoint 400 unknown code", http.StatusBadRequest,
		`{"error":"unauthorized_client"}`, ErrTokenUnavailable)
	check(t, "token endpoint 400 unparseable body", http.StatusBadRequest, `not json`, ErrTokenUnavailable)

	t.Run("nil hook does not panic", func(t *testing.T) {
		_, publisher, fact := newFixture(t, http.StatusBadRequest, `{"error":"invalid_scope"}`, nil)
		publishErr := publisher.Publish(t.Context(), fact)
		if !errors.Is(publishErr, ErrTokenScopeMissing) {
			t.Fatalf("publish error = %v, want ErrTokenScopeMissing (no panic)", publishErr)
		}
	})
}

// TestInvalidateUnboundAndMismatchedBindingAreNoOps (FM-8): Invalidate for an
// unbound tenant or a mismatched SourceSystem is a no-op returning nil (token
// retained, epoch unchanged); a valid binding with an empty cache still bumps
// the epoch (guards the in-flight race, REQ-2).
func TestInvalidateUnboundAndMismatchedBindingAreNoOps(t *testing.T) {
	const tenantID = "tenant-a"
	fact := scopeFact(tenantID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenHandler(w, "service-token-1", "")
		case "/api/v1/events":
			acceptedReceiptHandler(w, fact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
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
	if err := publisher.Publish(t.Context(), fact); err != nil {
		t.Fatal(err)
	}
	source := tokens.bindings[tenantID]
	source.mu.RLock()
	epochBefore := source.epoch
	source.mu.RUnlock()

	// Unbound tenant: no-op, nil.
	unboundSource, _ := TenantSourceID("aero-id", "tenant-zzz")
	if err := tokens.Invalidate(SourceBinding{TenantID: "tenant-zzz", SourceSystem: unboundSource}); err != nil {
		t.Fatalf("unbound Invalidate error = %v, want nil", err)
	}
	// Mismatched SourceSystem: no-op, nil — token retained, epoch unchanged.
	if err := tokens.Invalidate(SourceBinding{TenantID: tenantID, SourceSystem: "wrong.source"}); err != nil {
		t.Fatalf("mismatched Invalidate error = %v, want nil", err)
	}
	if token, ok := source.cachedToken(); !ok || token != "service-token-1" {
		t.Fatalf("token after no-op invalidations = %q, %v; want retained", token, ok)
	}
	source.mu.RLock()
	epoch := source.epoch
	source.mu.RUnlock()
	if epoch != epochBefore {
		t.Fatalf("epoch after no-op invalidations = %d, want %d", epoch, epochBefore)
	}

	// Valid binding: clears and bumps.
	if err := tokens.Invalidate(SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}); err != nil {
		t.Fatal(err)
	}
	if token, ok := source.cachedToken(); ok || token != "" {
		t.Fatalf("token after valid Invalidate = %q, %v; want cleared", token, ok)
	}
	// Valid binding with empty cache still bumps (guards the in-flight race).
	if err := tokens.Invalidate(SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}); err != nil {
		t.Fatal(err)
	}
	source.mu.RLock()
	epoch = source.epoch
	source.mu.RUnlock()
	if epoch != epochBefore+2 {
		t.Fatalf("epoch = %d, want %d (both valid invalidations bumped)", epoch, epochBefore+2)
	}
}

// stubTokenSource models a TokenSource whose AccessToken fails with a pinned
// error, for the C-B propagation test.
type stubTokenSource struct {
	err error
}

func (s stubTokenSource) AccessToken(context.Context, SourceBinding) (string, error) {
	return "", s.err
}
func (s stubTokenSource) HasBinding(string) bool              { return true }
func (s stubTokenSource) Invalidate(SourceBinding) error      { return nil }
func (s stubTokenSource) BindingUnhealthy(SourceBinding) bool { return false }
func (s stubTokenSource) MarkBindingUnhealthy(SourceBinding)  {}

// TestAuthorizationPropagatesScopeMissing (C-B): authorization must let
// ErrTokenScopeMissing escape to the caller instead of collapsing it into
// ErrTokenUnavailable; the three sentinels stay errors.Is-distinct.
func TestAuthorizationPropagatesScopeMissing(t *testing.T) {
	fact := scopeFact("tenant-a")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	publisher, err := NewPublisher(Config{
		Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
	}, stubTokenSource{err: ErrTokenScopeMissing}, nil)
	if err != nil {
		t.Fatal(err)
	}
	publishErr := publisher.Publish(t.Context(), fact)
	if !errors.Is(publishErr, ErrTokenScopeMissing) || errors.Is(publishErr, ErrTokenUnavailable) {
		t.Fatalf("publish error = %v, want ErrTokenScopeMissing propagated (not collapsed)", publishErr)
	}

	for name, pair := range map[string][2]error{
		"rejected vs scope-missing":    {ErrTokenRejected, ErrTokenScopeMissing},
		"rejected vs unavailable":      {ErrTokenRejected, ErrTokenUnavailable},
		"scope-missing vs unavailable": {ErrTokenScopeMissing, ErrTokenUnavailable},
	} {
		if errors.Is(pair[0], pair[1]) || errors.Is(pair[1], pair[0]) {
			t.Errorf("%s: sentinels must be errors.Is-distinct", name)
		}
	}
}

// TestPublishTransportErrorNeverLeaksEndpointURL (F1): a client.Do transport
// failure on the events endpoint surfaces as ErrTokenUnavailable: transport —
// the *url.Error embedding the endpoint URL must never reach the error text.
func TestPublishTransportErrorNeverLeaksEndpointURL(t *testing.T) {
	const tenantID = "tenant-a"
	fact := scopeFact(tenantID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenHandler(w, "service-token-1", "")
		case "/api/v1/events":
			// Force a transport-level failure with the URL embedded in the
			// raw *url.Error.
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("handler is not a hijacker")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
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
	publishErr := publisher.Publish(t.Context(), fact)
	if !errors.Is(publishErr, ErrTokenUnavailable) {
		t.Fatalf("publish error = %v, want ErrTokenUnavailable", publishErr)
	}
	if !strings.Contains(publishErr.Error(), "transport") {
		t.Fatalf("error %q must carry the transport marker", publishErr)
	}
	if strings.Contains(publishErr.Error(), "api/v1/events") || strings.Contains(publishErr.Error(), "http://") {
		t.Fatalf("error %q leaks the events-endpoint URL (F1)", publishErr)
	}
}
