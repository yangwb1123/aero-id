package auditgovernance

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublish403ScopeFailureCooldownRetainsTokenAndRecovers(t *testing.T) {
	const tenantID = "tenant-a"
	fact := scopeFact(tenantID)
	var tokenPosts, eventPosts atomic.Int32
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenPosts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"service-token","token_type":"Bearer","expires_in":3600}`)
		case "/api/v1/events":
			eventPosts.Add(1)
			if mode.Load() < 2 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"error":"insufficient_scope"}`)
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
	clock := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	tokens.bindings[tenantID].now = func() time.Time { return clock }
	publisher, err := NewPublisher(Config{Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true}, tokens, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}

	if err := publisher.Publish(t.Context(), fact); !errors.Is(err, ErrTokenScopeMissing) || errors.Is(err, ErrEventRejected) {
		t.Fatalf("first 403 error = %v, want only ErrTokenScopeMissing", err)
	}
	if tokenPosts.Load() != 1 || eventPosts.Load() != 1 {
		t.Fatalf("first probe counts = token %d/events %d, want 1/1", tokenPosts.Load(), eventPosts.Load())
	}
	cached, ok := tokens.bindings[tenantID].cachedToken()
	if !ok || cached != "service-token" {
		t.Fatalf("cached token after 403 = %q/%v, want retained token", cached, ok)
	}
	if tokens.bindings[tenantID].epoch != 0 || !tokens.BindingUnhealthy(binding) {
		t.Fatalf("403 state = epoch %d, unhealthy %v, want 0/true", tokens.bindings[tenantID].epoch, tokens.BindingUnhealthy(binding))
	}

	if err := publisher.Publish(t.Context(), fact); !errors.Is(err, ErrTokenScopeMissing) {
		t.Fatalf("suppressed publish error = %v, want ErrTokenScopeMissing", err)
	}
	if tokenPosts.Load() != 1 || eventPosts.Load() != 1 {
		t.Fatalf("suppressed counts = token %d/events %d, want unchanged 1/1", tokenPosts.Load(), eventPosts.Load())
	}

	clock = clock.Add(scopeMissingCooldown + time.Nanosecond)
	if err := publisher.Publish(t.Context(), fact); !errors.Is(err, ErrTokenScopeMissing) {
		t.Fatalf("rejected recovery probe error = %v, want ErrTokenScopeMissing", err)
	}
	if tokenPosts.Load() != 1 || eventPosts.Load() != 2 {
		t.Fatalf("rejected recovery probe counts = token %d/events %d, want 1/2", tokenPosts.Load(), eventPosts.Load())
	}

	mode.Store(2)
	clock = clock.Add(scopeMissingCooldown + time.Nanosecond)
	if err := publisher.Publish(t.Context(), fact); err != nil {
		t.Fatalf("successful recovery probe error = %v", err)
	}
	if tokenPosts.Load() != 1 || eventPosts.Load() != 3 {
		t.Fatalf("successful recovery probe counts = token %d/events %d, want 1/3", tokenPosts.Load(), eventPosts.Load())
	}
	if tokens.BindingUnhealthy(binding) {
		t.Fatal("binding remained unhealthy after cooldown expiry and successful probe")
	}
}

func TestPublish403NonScopeResponsesAreRejectedAndHealthy(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "access denied", body: `{"error":"access_denied"}`},
		{name: "malformed", body: "not-json"},
		{name: "oversized", body: `{"error":"invalid_scope","detail":"` + strings.Repeat("x", maxReceiptBytes) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const tenantID = "tenant-a"
			fact := scopeFact(tenantID)
			var tokenPosts, eventPosts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					tokenPosts.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"access_token":"service-token","token_type":"Bearer","expires_in":3600}`)
					return
				}
				eventPosts.Add(1)
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			tokens, err := NewOAuthTokenSource(OAuthConfig{SourcePrefix: "aero-id", AllowInsecureLoopback: true,
				Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			publisher, err := NewPublisher(Config{Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true}, tokens, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = publisher.Publish(t.Context(), fact)
			if err == nil || !errors.Is(err, ErrEventRejected) || errors.Is(err, ErrTokenScopeMissing) {
				t.Fatalf("403 error = %v, want ErrEventRejected (not scope-missing)", err)
			}
			if strings.Contains(err.Error(), "access_denied") || strings.Contains(err.Error(), "invalid_scope") {
				t.Fatalf("403 error leaked response body: %v", err)
			}
			if tokenPosts.Load() != 1 || eventPosts.Load() != 1 {
				t.Fatalf("calls = token %d/events %d, want 1/1", tokenPosts.Load(), eventPosts.Load())
			}
			if tokens.BindingUnhealthy(SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}) {
				t.Fatal("generic 403 marked binding unhealthy")
			}
		})
	}
}

func TestOAuthBindingCooldownIsConcurrentSafe(t *testing.T) {
	const tenantID = "tenant-a"
	tokens, err := NewOAuthTokenSource(OAuthConfig{SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: {TokenURL: "http://127.0.0.1/token", ClientID: "client",
			ClientSecret: "secret", Scope: auditScope, Resources: []string{auditResource}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := SourceBinding{TenantID: tenantID, SourceSystem: mustSource(t, tenantID)}
	var wait sync.WaitGroup
	for i := 0; i < 32; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			tokens.MarkBindingUnhealthy(binding)
			_ = tokens.BindingUnhealthy(binding)
		}()
	}
	wait.Wait()
	if !tokens.BindingUnhealthy(binding) {
		t.Fatal("concurrent cooldown marks did not leave binding unhealthy")
	}
}
