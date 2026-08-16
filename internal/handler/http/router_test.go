package http_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/idempotency"
	"github.com/aero/aero-id/internal/pkg/metrics"
)

type okValidator struct{}

func (okValidator) ValidateToken(context.Context, string) (string, error) { return "u1", nil }

// pingRegistrar registers a protected /v1/ping route to exercise the auth chain.
type pingRegistrar struct{}

func (pingRegistrar) RegisterRoutes(r httpapi.Routes) {
	r.Auth.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
}

// postRegistrar registers a protected /v1/create POST route to exercise the
// idempotency middleware wiring.
type postRegistrar struct{}

func (postRegistrar) RegisterRoutes(r httpapi.Routes) {
	r.Auth.POST("/create", func(c *gin.Context) { c.Status(http.StatusCreated) })
}

// failingIdemStore reserves every key but always fails Complete, so a keyed
// POST exercises the idempotency observer's complete_failed path.
type failingIdemStore struct{}

func (failingIdemStore) Reserve(context.Context, string, string, string) (*idempotency.Record, bool, error) {
	return nil, true, nil
}

func (failingIdemStore) Complete(context.Context, string, string, string, idempotency.Record, time.Duration) error {
	return errors.New("complete boom")
}

func (failingIdemStore) Release(context.Context, string, string, string) error { return nil }

// TestRouter_IdempotencyFailureObservedInMetrics proves the metrics-backed
// observer passed at the router wiring site is live: a keyed POST whose
// store.Complete fails produces the complete_failed series in /metrics.
func TestRouter_IdempotencyFailureObservedInMetrics(t *testing.T) {
	reg := metrics.NewRegistry()
	e := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{
		Metrics:     reg,
		Idempotency: failingIdemStore{},
	}, okValidator{}, httpapi.NewHealthHandler(), postRegistrar{})

	req := httptest.NewRequest(http.MethodPost, "/v1/create", nil)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Idempotency-Key", "k1")
	e.ServeHTTP(httptest.NewRecorder(), req)

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/metrics = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `aeroid_idempotency_total{op="complete_failed"} 1`) {
		t.Errorf("/metrics missing the complete_failed series:\n%s", w.Body.String())
	}
}

func TestRouter_HealthLive(t *testing.T) {
	e := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, httpapi.NewHealthHandler())
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("healthz = %d", w.Code)
	}
}

func TestRouter_ReadyFails503(t *testing.T) {
	bad := httpapi.NewHealthHandler(httpapi.Check{Name: "db", Critical: true, Probe: func(context.Context) error { return context.DeadlineExceeded }})
	e := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, bad)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503", w.Code)
	}
}

func TestRouter_AuthRequiredOnV1(t *testing.T) {
	e := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, httpapi.NewHealthHandler(), pingRegistrar{})

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/ping", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no-token /v1/ping = %d, want 401", w.Code)
	}

	w2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer t")
	e.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("with-token /v1/ping = %d, want 200", w2.Code)
	}
}

func TestRouter_MetricsEndpoint(t *testing.T) {
	reg := metrics.NewRegistry()
	e := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{Metrics: reg}, okValidator{}, httpapi.NewHealthHandler(), pingRegistrar{})

	// Exercise an authenticated route so the metrics middleware records it.
	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	req.Header.Set("Authorization", "Bearer t")
	e.ServeHTTP(httptest.NewRecorder(), req)

	// Scrape /metrics and confirm the observation is exposed in Prometheus format.
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/metrics = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `aeroid_requests_total{transport="http",method="GET",route="/v1/ping"`) {
		t.Errorf("/metrics missing the /v1/ping observation:\n%s", w.Body.String())
	}
}
