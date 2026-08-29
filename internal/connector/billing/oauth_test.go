package billing

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenSourceEpochFencesStaleFetchAndCoalescesConcurrentAccess(t *testing.T) {
	const scope = ScopeEntitlementRead
	var tokenCalls atomic.Int32
	staleArrive := make(chan struct{})
	replacementArrive := make(chan struct{})
	releaseStale := make(chan struct{})
	releaseReplacement := make(chan struct{})
	var staleSignalOnce, replacementSignalOnce sync.Once
	var staleReleaseOnce, replacementReleaseOnce sync.Once
	defer staleReleaseOnce.Do(func() { close(releaseStale) })
	defer replacementReleaseOnce.Do(func() { close(releaseReplacement) })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/token" {
			http.NotFound(w, request)
			return
		}
		switch call := tokenCalls.Add(1); call {
		case 1:
			writeJSON(t, w, http.StatusOK, billingTokenResponse("token-1"))
		case 2:
			staleSignalOnce.Do(func() { close(staleArrive) })
			<-releaseStale
			writeJSON(t, w, http.StatusOK, billingTokenResponse("token-1"))
		case 3:
			replacementSignalOnce.Do(func() { close(replacementArrive) })
			<-releaseReplacement
			writeJSON(t, w, http.StatusOK, billingTokenResponse("token-2"))
		default:
			t.Errorf("unexpected token fetch %d", call)
			writeJSON(t, w, http.StatusOK, billingTokenResponse("token-2"))
		}
	}))
	defer server.Close()

	source, err := newTokenSource(oauthConfig{TokenURL: server.URL + "/token", ClientID: "client", ClientSecret: "secret",
		Scope: ScopeEntitlementRead + " " + ScopeMeteringWrite, Resources: []string{billingResource},
		Timeout: time.Second, AllowHTTP: true}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.accessToken(t.Context(), scope)
	initial, cached := source.cachedSnapshot(scope)
	if err != nil || token != "token-1" || !cached {
		t.Fatalf("initial token = %q/%q, cached=%v, err=%v", token, initial.value, cached, err)
	}
	source.mu.Lock()
	entry := source.tokens[scope]
	entry.refreshAt = time.Now().UTC().Add(-time.Second)
	source.tokens[scope] = entry
	source.mu.Unlock()

	type tokenResult struct {
		snapshot billingTokenSnapshot
		err      error
	}
	staleResult := make(chan tokenResult, 1)
	go func() {
		snapshot, fetchErr := source.accessTokenSnapshot(t.Context(), scope)
		staleResult <- tokenResult{snapshot: snapshot, err: fetchErr}
	}()
	waitBillingSignal(t, staleArrive, "stale token fetch")
	source.invalidateIf(initial)
	staleReleaseOnce.Do(func() { close(releaseStale) })
	waitBillingSignal(t, replacementArrive, "replacement token fetch")
	if _, ok := source.cachedSnapshot(scope); ok {
		t.Fatal("stale token was cached while replacement fetch was in flight")
	}

	const workers = 8
	start := make(chan struct{})
	results := make(chan tokenResult, workers)
	var waitGroup sync.WaitGroup
	for range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			snapshot, fetchErr := source.accessTokenSnapshot(t.Context(), scope)
			results <- tokenResult{snapshot: snapshot, err: fetchErr}
		}()
	}
	close(start)
	time.Sleep(50 * time.Millisecond)
	replacementReleaseOnce.Do(func() { close(releaseReplacement) })
	waitGroup.Wait()

	if result := <-staleResult; result.err != nil || result.snapshot.value != "token-2" {
		t.Fatalf("stale fetch re-entry = %q, err=%v, want token-2", result.snapshot.value, result.err)
	}
	for range workers {
		result := <-results
		if result.err != nil || result.snapshot.value != "token-2" {
			t.Fatalf("concurrent token = %q, err=%v, want token-2", result.snapshot.value, result.err)
		}
	}
	if got := tokenCalls.Load(); got != 3 {
		t.Fatalf("token fetches = %d, want 3 (prime, stale, one replacement)", got)
	}
	if cached, ok := source.cached(scope); !ok || cached != "token-2" {
		t.Fatalf("final cached token = %q/%v, want token-2/true", cached, ok)
	}
}

func TestTokenSourceInvalidateIfDoesNotEvictNewerGeneration(t *testing.T) {
	const scope = ScopeEntitlementRead
	source := &tokenSource{tokens: make(map[string]cachedToken), epochs: make(map[string]uint64), generations: make(map[string]uint64)}
	if _, ok := source.cacheTokenAtEpoch(scope, tokenWire{AccessToken: "token-1", TokenType: "Bearer", ExpiresIn: 300}, 0); !ok {
		t.Fatal("failed to cache initial token")
	}
	old, ok := source.cachedSnapshot(scope)
	if !ok {
		t.Fatal("initial token was not cached")
	}
	if _, ok := source.cacheTokenAtEpoch(scope, tokenWire{AccessToken: "token-2", TokenType: "Bearer", ExpiresIn: 300}, 0); !ok {
		t.Fatal("failed to cache replacement token")
	}
	newer, ok := source.cachedSnapshot(scope)
	if !ok {
		t.Fatal("replacement token was not cached")
	}

	source.invalidateIf(old)
	if cached, ok := source.cached(scope); !ok || cached != "token-2" {
		t.Fatalf("stale invalidation changed cache to %q/%v, want token-2/true", cached, ok)
	}
	source.invalidateIf(newer)
	if _, ok := source.cached(scope); ok {
		t.Fatal("current token was not invalidated")
	}
}

func billingTokenResponse(token string) map[string]any {
	return map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300}
}

func waitBillingSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
