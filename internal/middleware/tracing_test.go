package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/aero/aero-id/internal/middleware"
)

// The tracing middleware must slot into the existing chain after RequestID without
// breaking it: the handler still runs, the request_id header is still set, and a
// trace id is surfaced for correlation.
func TestTracing_preservesChainAndCorrelates(t *testing.T) {
	// Install a real (in-memory) provider so a sampled span carries a trace id.
	prev := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(t.Context())
		otel.SetTracerProvider(prev)
	})

	e := newEngine()
	e.Use(middleware.RequestID())
	e.Use(middleware.Tracing())

	var ranWithRequestID bool
	e.GET("/p", func(c *gin.Context) {
		ranWithRequestID = c.GetString("request_id") != ""
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/p", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (chain must not break)", w.Code)
	}
	if !ranWithRequestID {
		t.Fatal("handler did not see a request_id; chain ordering broke")
	}
	if w.Header().Get(middleware.RequestIDHeader) == "" {
		t.Fatal("request id header missing; tracing middleware disrupted RequestID")
	}
	if w.Header().Get("X-Trace-ID") == "" {
		t.Fatal("expected an X-Trace-ID correlation header")
	}
}
