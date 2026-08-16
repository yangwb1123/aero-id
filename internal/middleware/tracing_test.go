package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/logger"
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
	var carrierFields []zap.Field
	e.GET("/p", func(c *gin.Context) {
		ranWithRequestID = c.GetString("request_id") != ""
		carrierFields = logger.CtxFields(c.Request.Context())
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
	// The logger carrier must hold exactly request_id + trace_id (no auth in
	// this chain), in stable order, with trace_id matching the header.
	traceHeader := w.Header().Get("X-Trace-ID")
	if len(carrierFields) != 2 {
		t.Fatalf("carrier fields = %+v, want exactly [trace_id request_id]", carrierFields)
	}
	if carrierFields[0].Key != "trace_id" || carrierFields[0].String != traceHeader {
		t.Fatalf("carrierFields[0] = %+v, want trace_id = header %q", carrierFields[0], traceHeader)
	}
	if carrierFields[1].Key != "request_id" || carrierFields[1].String == "" {
		t.Fatalf("carrierFields[1] = %+v, want non-empty request_id", carrierFields[1])
	}
	if carrierFields[1].String != w.Header().Get(middleware.RequestIDHeader) {
		t.Fatalf("carrier request_id = %q, X-Request-ID header = %q — must match", carrierFields[1].String, w.Header().Get(middleware.RequestIDHeader))
	}
}
