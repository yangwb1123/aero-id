package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	httpapi "github.com/aero/aero-id/internal/handler/http"
)

type okValidator struct{}

func (okValidator) ValidateToken(context.Context, string) (string, error) { return "u1", nil }

// pingRegistrar registers a protected /v1/ping route to exercise the auth chain.
type pingRegistrar struct{}

func (pingRegistrar) RegisterRoutes(r httpapi.Routes) {
	r.Auth.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
}

func TestRouter_HealthLive(t *testing.T) {
	e := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, httpapi.NewHealthHandler(nil))
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("healthz = %d", w.Code)
	}
}

func TestRouter_ReadyFails503(t *testing.T) {
	bad := httpapi.NewHealthHandler(func(context.Context) error { return context.DeadlineExceeded })
	e := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, bad)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503", w.Code)
	}
}

func TestRouter_AuthRequiredOnV1(t *testing.T) {
	e := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, httpapi.NewHealthHandler(nil), pingRegistrar{})

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
