// Package auditgovernance — acceptance suite for the OnScopeRejected
// composition-root hook safety contract (T-10.1e/f; design
// docs/plans/2026-08-07-auditgovernance-hook-hazard-resolution.md §5, mapped
// to acceptance in
// docs/designs/2026-08-16-internal-connector-auditgovernance-scope-hook-acceptance-design.md):
// T-10.1e pins panic containment (a panicking hook must not escape the
// singleflight flight fn — x/sync v0.20.0 re-raises an escaping flight panic
// as an unrecoverable process crash), T-10.1f pins the re-entrancy guard (a
// hook calling back into the same tenant's AccessToken must fail fast with
// ErrTokenUnavailable, never self-join the in-flight flight). Both fixtures
// are in-package httptest loopback (AllowInsecureLoopback: true) reusing the
// module's shared helpers (scopeFact, mustSource, auditBinding).
package auditgovernance

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestScopeRejectedHookPanicIsContained (T-10.1e): the OnScopeRejected hook
// runs inside the singleflight flight fn. x/sync v0.20.0 re-raises an
// escaping flight panic in a fresh goroutine (go panic(e); select {}) — an
// unrecoverable process crash — and never delivers a result to waiters. The
// in-package deferred recover in notifyScopeRejected must convert the hook
// panic into the classified ErrTokenScopeMissing for the caller, invoke the
// hook exactly once, and leave the token cache empty. Test completion IS the
// containment proof (a broken recover crashes the test binary at
// singleflight.doCall with "panic: recorder hook boom" — mutation-verified
// in the hazard-resolution plan §6).
func TestScopeRejectedHookPanicIsContained(t *testing.T) {
	const tenantID = "tenant-a"
	var (
		hookCalls atomic.Int32
		gotTenant atomic.Value // string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_scope"})
	}))
	defer server.Close()

	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
		OnScopeRejected: func(got string) {
			hookCalls.Add(1)
			gotTenant.Store(got)
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

	publishErr := publisher.Publish(t.Context(), scopeFact(tenantID))

	if !errors.Is(publishErr, ErrTokenScopeMissing) {
		t.Fatalf("Publish error = %v, want errors.Is(err, ErrTokenScopeMissing)", publishErr)
	}
	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("hook invocations = %d, want 1", got)
	}
	if got, ok := gotTenant.Load().(string); !ok || got != tenantID {
		t.Fatalf("hook tenant = %v, want %q", got, tenantID)
	}
	if cached, ok := tokens.bindings[tenantID].cachedToken(); ok && cached != "" {
		t.Fatalf("token cache non-empty after scope rejection: %q", cached)
	}
}

// TestScopeRejectedHookReentrancyFailsFast (T-10.1f): a hook that calls back
// into the same tenant's AccessToken must fail fast with ErrTokenUnavailable
// (hookRunning guard, checked before DoChan) instead of DoChan-ing the
// in-flight flight and self-joining forever — singleflight has no
// cancellation; the flight, the epoch key, and every future same-tenant fetch
// would hang permanently. The outer publish must still complete with
// ErrTokenScopeMissing, the hook must run exactly once, and nothing is
// cached. Pass criteria are exclusively the channel receipts; the watchdog
// only converts a regression hang into a bounded failure (mutation-verified
// in the hazard-resolution plan §6: removing the guard yields a bounded
// deadlock failure).
func TestScopeRejectedHookReentrancyFailsFast(t *testing.T) {
	const tenantID = "tenant-a"
	expectedSource := mustSource(t, tenantID)
	var (
		hookCalls atomic.Int32
		tokens    *OAuthTokenSource
		err       error
	)
	innerResult := make(chan error, 1)
	outerResult := make(chan error, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_scope"})
	}))
	defer server.Close()

	tokens, err = NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
		OnScopeRejected: func(gotTenant string) {
			hookCalls.Add(1)
			_, innerErr := tokens.AccessToken(t.Context(), SourceBinding{
				TenantID: gotTenant, SourceSystem: expectedSource,
			})
			innerResult <- innerErr
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
	go func() { outerResult <- publisher.Publish(t.Context(), scopeFact(tenantID)) }()

	// Inner result first: the re-entrant call runs on the flight goroutine
	// inside the hook and completes before the hook returns, which precedes
	// the flight's result delivery — program order, not timing.
	select {
	case innerErr := <-innerResult:
		if !errors.Is(innerErr, ErrTokenUnavailable) {
			t.Fatalf("re-entrant AccessToken error = %v, want errors.Is(err, ErrTokenUnavailable)", innerErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("re-entrant AccessToken from hook did not fail fast (deadlock regression: guard missing?)")
	}

	select {
	case outerErr := <-outerResult:
		if !errors.Is(outerErr, ErrTokenScopeMissing) {
			t.Fatalf("outer Publish error = %v, want errors.Is(err, ErrTokenScopeMissing)", outerErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("outer Publish did not complete (deadlock regression: guard missing?)")
	}

	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("hook invocations = %d, want 1", got)
	}
	if cached, ok := tokens.bindings[tenantID].cachedToken(); ok && cached != "" {
		t.Fatalf("token cache non-empty after scope rejection: %q", cached)
	}
}
