package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

func TestBillingCurrentEntitlementMapsInFlightContextTermination(t *testing.T) {
	tests := []struct {
		name     string
		deadline bool
	}{
		{name: "cancellation"},
		{name: "deadline", deadline: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resourceStarted := make(chan struct{})
			resourceCanceled := make(chan struct{})
			release := make(chan struct{})
			var startOnce, cancelOnce sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/"+entitlementPath {
					http.NotFound(writer, request)
					return
				}
				startOnce.Do(func() { close(resourceStarted) })
				select {
				case <-request.Context().Done():
					cancelOnce.Do(func() { close(resourceCanceled) })
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)

			client := newBillingConformanceClient(t, server.URL, 1024, map[string]BindingConfig{
				"tenant-1": billingConformanceBinding(server.URL+"/token", "client", "secret"),
			})
			if _, ok := client.bindings["tenant-1"].cacheTokenAtEpoch(ScopeEntitlementRead,
				tokenWire{AccessToken: "cached-token", TokenType: "Bearer", ExpiresIn: 300}, 0); !ok {
				t.Fatal("failed to prime entitlement token")
			}

			var ctx context.Context
			var cancel context.CancelFunc
			if test.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := client.CurrentEntitlement(ctx, "tenant-1")
				result <- err
			}()
			waitBillingConformanceSignal(t, resourceStarted, "resource request")
			if !test.deadline {
				cancel()
			}
			waitBillingConformanceSignal(t, resourceCanceled, "server request cancellation")
			if test.deadline && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("context error = %v, want deadline exceeded", ctx.Err())
			}
			select {
			case err := <-result:
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("in-flight request error = %v, want ErrUnavailable", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for canceled entitlement request")
			}
		})
	}
}

func TestBillingCurrentEntitlementRejectsInvalidReplies(t *testing.T) {
	validBody, err := json.Marshal(map[string]any{"entitlement": billingConformanceEntitlement("tenant-1")})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "malformed JSON", contentType: "application/json", body: `{"entitlement":`},
		{name: "wrong Content-Type", contentType: "text/plain", body: string(validBody)},
		{name: "body exceeds configured limit", contentType: "application/json", body: strings.Repeat("x", 1025)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/token":
					writeBillingConformanceJSON(writer, http.StatusOK, billingConformanceTokenResponse("resource-token"))
				case "/" + entitlementPath:
					writer.Header().Set("Content-Type", test.contentType)
					writer.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(writer, test.body)
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()

			client := newBillingConformanceClient(t, server.URL, 1024, map[string]BindingConfig{
				"tenant-1": billingConformanceBinding(server.URL+"/token", "client", "secret"),
			})
			_, err := client.CurrentEntitlement(t.Context(), "tenant-1")
			if !errors.Is(err, ErrInvalidReply) {
				t.Fatalf("invalid reply error = %v, want ErrInvalidReply", err)
			}
		})
	}
}

func TestBillingConstructorsRejectUnsafeURLShapes(t *testing.T) {
	validAuthEndpoint := "https://oauth.example.test/token"
	endpointCases := []struct {
		name      string
		endpoint  string
		allowHTTP bool
	}{
		{name: "non-loopback HTTP", endpoint: "http://billing.example.test", allowHTTP: true},
		{name: "loopback HTTP when disabled", endpoint: "http://127.0.0.1:8080", allowHTTP: false},
		{name: "endpoint credentials", endpoint: "https://user:password@billing.example.test", allowHTTP: true},
		{name: "endpoint query", endpoint: "https://billing.example.test?redirect_uri=https%3A%2F%2Fevil.example", allowHTTP: true},
		{name: "endpoint fragment", endpoint: "https://billing.example.test/#redirect", allowHTTP: true},
	}
	for _, test := range endpointCases {
		t.Run("endpoint/"+test.name, func(t *testing.T) {
			_, err := NewClient(Config{Endpoint: test.endpoint, AllowInsecureLoopback: test.allowHTTP,
				Bindings: map[string]BindingConfig{
					"tenant-1": billingConformanceBinding(validAuthEndpoint, "client", "secret"),
				}}, nil)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("constructor error = %v, want ErrInvalidConfig", err)
			}
		})
	}

	tokenCases := []struct {
		name     string
		tokenURL string
	}{
		{name: "non-loopback HTTP", tokenURL: "http://oauth.example.test/token"},
		{name: "token credentials", tokenURL: "https://user:password@oauth.example.test/token"},
		{name: "token query", tokenURL: "https://oauth.example.test/token?audience=other"},
		{name: "token fragment", tokenURL: "https://oauth.example.test/token#redirect"},
	}
	for _, test := range tokenCases {
		t.Run("token/"+test.name, func(t *testing.T) {
			_, err := NewClient(Config{Endpoint: "https://billing.example.test", AllowInsecureLoopback: true,
				Bindings: map[string]BindingConfig{
					"tenant-1": billingConformanceBinding(test.tokenURL, "client", "secret"),
				}}, nil)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("constructor error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestBillingClientsDoNotFollowRedirectResponses(t *testing.T) {
	for _, redirectToken := range []bool{true, false} {
		name := "resource"
		if redirectToken {
			name = "token"
		}
		t.Run(name, func(t *testing.T) {
			const target = "/redirect-target"
			var targetCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case target:
					targetCalls.Add(1)
					if redirectToken {
						writeBillingConformanceJSON(writer, http.StatusOK, billingConformanceTokenResponse("redirect-token"))
					} else {
						writeBillingConformanceJSON(writer, http.StatusOK, map[string]any{
							"entitlement": billingConformanceEntitlement("tenant-1"),
						})
					}
				case "/token":
					if redirectToken {
						writer.Header().Set("Location", target)
						writer.WriteHeader(http.StatusFound)
					} else {
						writeBillingConformanceJSON(writer, http.StatusOK, billingConformanceTokenResponse("resource-token"))
					}
				case "/" + entitlementPath:
					writer.Header().Set("Location", target)
					writer.WriteHeader(http.StatusFound)
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()

			client := newBillingConformanceClient(t, server.URL, 1024, map[string]BindingConfig{
				"tenant-1": billingConformanceBinding(server.URL+"/token", "client", "secret"),
			})
			_, err := client.CurrentEntitlement(t.Context(), "tenant-1")
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("redirect response error = %v, want ErrUnavailable", err)
			}
			if targetCalls.Load() != 0 {
				t.Fatalf("redirect target calls = %d, want 0", targetCalls.Load())
			}
		})
	}
}

func TestBillingTokenFailuresAreUnavailableWithoutResponseDetails(t *testing.T) {
	const detail = "oauth-response-detail-never-return-this"
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{name: "HTTP 400", status: http.StatusBadRequest, contentType: "application/json", body: `{"error":"` + detail + `"}`},
		{name: "HTTP 503", status: http.StatusServiceUnavailable, contentType: "application/json", body: `{"error":"` + detail + `"}`},
		{name: "malformed JSON", status: http.StatusOK, contentType: "application/json", body: `{"access_token":"` + detail},
		{name: "wrong Content-Type", status: http.StatusOK, contentType: "text/plain", body: `{"access_token":"` + detail + `","token_type":"Bearer","expires_in":300}`},
		{name: "missing access token", status: http.StatusOK, contentType: "application/json", body: `{"token_type":"Bearer","expires_in":300,"error_description":"` + detail + `"}`},
		{name: "wrong token type", status: http.StatusOK, contentType: "application/json", body: `{"access_token":"` + detail + `","token_type":"Basic","expires_in":300}`},
		{name: "invalid expiry", status: http.StatusOK, contentType: "application/json", body: `{"access_token":"` + detail + `","token_type":"Bearer","expires_in":0}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var resourceCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/token":
					writer.Header().Set("Content-Type", test.contentType)
					writer.WriteHeader(test.status)
					_, _ = io.WriteString(writer, test.body)
				case "/" + entitlementPath:
					resourceCalls.Add(1)
					http.Error(writer, "unexpected resource request", http.StatusInternalServerError)
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()

			client := newBillingConformanceClient(t, server.URL, 1024, map[string]BindingConfig{
				"tenant-1": billingConformanceBinding(server.URL+"/token", "client", "secret"),
			})
			_, err := client.CurrentEntitlement(t.Context(), "tenant-1")
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("token failure error = %v, want ErrUnavailable", err)
			}
			if strings.Contains(err.Error(), detail) {
				t.Fatalf("token failure leaked response detail: %v", err)
			}
			if resourceCalls.Load() != 0 {
				t.Fatalf("resource calls = %d, want 0 after token failure", resourceCalls.Load())
			}
		})
	}
}

func TestBillingConcurrentRequestsCoalesceTokensPerScope(t *testing.T) {
	const workersPerScope = 6
	var entitlementTokenCalls, meteringTokenCalls atomic.Int32
	var entitlementCalls, reservationCalls atomic.Int32
	var serverFailures atomic.Int32
	entitlementStarted := make(chan struct{})
	meteringStarted := make(chan struct{})
	entitlementRelease := make(chan struct{})
	meteringRelease := make(chan struct{})
	var entitlementStartOnce, meteringStartOnce sync.Once
	var entitlementReleaseOnce, meteringReleaseOnce sync.Once
	releaseEntitlement := func() { entitlementReleaseOnce.Do(func() { close(entitlementRelease) }) }
	releaseMetering := func() { meteringReleaseOnce.Do(func() { close(meteringRelease) }) }
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if request.Method != http.MethodPost || request.FormValue("resource") != billingResource {
				serverFailures.Add(1)
			}
			switch request.FormValue("scope") {
			case ScopeEntitlementRead:
				if entitlementTokenCalls.Add(1) == 1 {
					entitlementStartOnce.Do(func() { close(entitlementStarted) })
					<-entitlementRelease
				}
				writeBillingConformanceJSON(writer, http.StatusOK, billingConformanceTokenResponse("entitlement-token"))
			case ScopeMeteringWrite:
				if meteringTokenCalls.Add(1) == 1 {
					meteringStartOnce.Do(func() { close(meteringStarted) })
					<-meteringRelease
				}
				writeBillingConformanceJSON(writer, http.StatusOK, billingConformanceTokenResponse("metering-token"))
			default:
				serverFailures.Add(1)
				http.Error(writer, "unexpected scope", http.StatusBadRequest)
			}
		case "/" + entitlementPath:
			entitlementCalls.Add(1)
			if request.Method != http.MethodGet || request.URL.RawQuery != "" ||
				request.Header.Get("Authorization") != "Bearer entitlement-token" {
				serverFailures.Add(1)
			}
			writeBillingConformanceJSON(writer, http.StatusOK, map[string]any{
				"entitlement": billingConformanceEntitlement("tenant-1"),
			})
		case "/" + reservationPath:
			reservationCalls.Add(1)
			if request.Method != http.MethodPost || request.URL.RawQuery != "" ||
				request.Header.Get("Authorization") != "Bearer metering-token" ||
				request.Header.Get("Idempotency-Key") == "" {
				serverFailures.Add(1)
			}
			var wire map[string]any
			if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
				serverFailures.Add(1)
			} else if _, found := wire["tenant_id"]; found {
				serverFailures.Add(1)
			}
			id, _ := wire["id"].(string)
			dimension, _ := wire["dimension"].(string)
			quantity, _ := wire["quantity"].(float64)
			writeBillingConformanceJSON(writer, http.StatusCreated, map[string]any{
				"reservation": map[string]any{"id": id, "tenant_id": "tenant-1", "dimension": dimension,
					"quantity": int64(quantity), "status": "pending"},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	defer releaseEntitlement()
	defer releaseMetering()

	client := newBillingConformanceClient(t, server.URL, 1024, map[string]BindingConfig{
		"tenant-1": billingConformanceBinding(server.URL+"/token", "client", "secret"),
	})
	start := make(chan struct{})
	ready := make(chan struct{}, workersPerScope*2)
	results := make(chan error, workersPerScope*2)
	var waitGroup sync.WaitGroup
	for index := range workersPerScope {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			ready <- struct{}{}
			<-start
			_, err := client.CurrentEntitlement(t.Context(), "tenant-1")
			results <- err
		}()
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			ready <- struct{}{}
			<-start
			_, err := client.Reserve(t.Context(), "tenant-1", ReserveCommand{
				ID: fmt.Sprintf("reservation-%d", index), Dimension: "users", Quantity: 1,
				TTLSeconds: 300, IdempotencyKey: fmt.Sprintf("reservation-key-%d", index),
			})
			results <- err
		}(index)
	}
	for range workersPerScope * 2 {
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent billing callers")
		}
	}
	close(start)
	waitBillingConformanceSignal(t, entitlementStarted, "entitlement token fetch")
	waitBillingConformanceSignal(t, meteringStarted, "metering token fetch")
	releaseEntitlement()
	releaseMetering()
	finished := make(chan struct{})
	go func() {
		waitGroup.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for concurrent billing calls")
	}
	for range workersPerScope * 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent billing request error = %v", err)
		}
	}
	if got := entitlementTokenCalls.Load(); got != 1 {
		t.Fatalf("entitlement token calls = %d, want 1", got)
	}
	if got := meteringTokenCalls.Load(); got != 1 {
		t.Fatalf("metering token calls = %d, want 1", got)
	}
	if got := entitlementCalls.Load(); got != workersPerScope {
		t.Fatalf("entitlement calls = %d, want %d", got, workersPerScope)
	}
	if got := reservationCalls.Load(); got != workersPerScope {
		t.Fatalf("reservation calls = %d, want %d", got, workersPerScope)
	}
	if serverFailures.Load() != 0 {
		t.Fatalf("server observed %d malformed billing requests", serverFailures.Load())
	}
}

func TestBillingUnknownTenantDoesNotMakeHTTPRequest(t *testing.T) {
	var requests atomic.Int32
	var tenantOneTokens, tenantTwoTokens atomic.Int32
	var wrongBinding atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		switch request.URL.Path {
		case "/token-one", "/token-two":
			clientID, secret, ok := request.BasicAuth()
			if request.URL.Path == "/token-one" {
				if !ok || clientID != "tenant-one-client" || secret != "secret-one" {
					wrongBinding.Add(1)
				}
				tenantOneTokens.Add(1)
				writeBillingConformanceJSON(writer, http.StatusOK, billingConformanceTokenResponse("tenant-one-token"))
				return
			}
			if !ok || clientID != "tenant-two-client" || secret != "secret-two" {
				wrongBinding.Add(1)
			}
			tenantTwoTokens.Add(1)
			writeBillingConformanceJSON(writer, http.StatusOK, billingConformanceTokenResponse("tenant-two-token"))
		case "/" + entitlementPath:
			if request.Method != http.MethodGet || request.URL.RawQuery != "" ||
				request.Header.Get("Authorization") != "Bearer tenant-two-token" {
				wrongBinding.Add(1)
			}
			writeBillingConformanceJSON(writer, http.StatusOK, map[string]any{
				"entitlement": billingConformanceEntitlement("tenant-two"),
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := newBillingConformanceClient(t, server.URL, 1024, map[string]BindingConfig{
		"tenant-one": billingConformanceBinding(server.URL+"/token-one", "tenant-one-client", "secret-one"),
		"tenant-two": billingConformanceBinding(server.URL+"/token-two", "tenant-two-client", "secret-two"),
	})
	beforeUnknown := requests.Load()
	if _, err := client.CurrentEntitlement(t.Context(), "unknown-tenant"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown tenant error = %v, want ErrUnauthorized", err)
	}
	if got := requests.Load(); got != beforeUnknown {
		t.Fatalf("unknown tenant made %d HTTP requests", got-beforeUnknown)
	}
	if _, err := client.CurrentEntitlement(t.Context(), "tenant-two"); err != nil {
		t.Fatalf("configured tenant request error = %v", err)
	}
	if tenantOneTokens.Load() != 0 || tenantTwoTokens.Load() != 1 {
		t.Fatalf("token calls = tenant-one:%d tenant-two:%d, want 0/1", tenantOneTokens.Load(), tenantTwoTokens.Load())
	}
	if wrongBinding.Load() != 0 {
		t.Fatalf("server observed %d wrong tenant binding requests", wrongBinding.Load())
	}
}

func newBillingConformanceClient(t *testing.T, endpoint string, maxBodyBytes int64, bindings map[string]BindingConfig) *Client {
	t.Helper()
	client, err := NewClient(Config{Endpoint: endpoint, Timeout: time.Second, MaxBodyBytes: maxBodyBytes,
		AllowInsecureLoopback: true, Bindings: bindings}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func billingConformanceBinding(tokenURL, clientID, clientSecret string) BindingConfig {
	return BindingConfig{TokenURL: tokenURL, ClientID: clientID, ClientSecret: clientSecret,
		Scope: ScopeEntitlementRead + " " + ScopeMeteringWrite, Resources: []string{billingResource}}
}

func billingConformanceTokenResponse(token string) map[string]any {
	return map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300}
}

func billingConformanceEntitlement(tenantID string) domain.EntitlementProjection {
	at := time.Unix(1_700_000_000, 0).UTC()
	return domain.EntitlementProjection{TenantID: tenantID, SubscriptionID: "subscription-" + tenantID,
		Plan: domain.PlanRef{ID: "plan", Version: 1}, Revision: 1, Active: true,
		Limits: map[string]domain.LimitGrant{"users": {Hard: 10}}, EffectiveAt: at, GeneratedAt: at}
}

func writeBillingConformanceJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func waitBillingConformanceSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", name)
	}
}
