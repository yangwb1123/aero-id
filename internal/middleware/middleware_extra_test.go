package middleware_test

// middleware_extra_test.go adds coverage for the cases not exercised by the
// existing middleware_test.go:
//
//   - Auth: malformed header (no "Bearer " prefix), empty-uid path
//   - RequestID: inbound X-Request-ID is reused; downstream reads from gin context
//   - Recovery: non-panicking handler passes through normally
//   - CORS: permissive (wildcard) mode; allowlisted origin; disallowed origin
//     preflight 403 + real request passes without CORS headers; OPTIONS 204
//   - AccessLog: does not panic and passes through to the handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/aero/aero-id/internal/middleware"
)

// ── Auth extras ──────────────────────────────────────────────────────────────

// bearerToken strips the prefix only when it matches case-insensitively and is
// followed by at least one character. A header of just "Bearer " (nothing after
// the space) must also be rejected.
func TestAuth_malformedHeader401(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"no scheme", "xyz-token"},
		{"only bearer prefix no token", "Bearer "},
		{"wrong scheme", "Token abc"},
		{"empty header", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEngine()
			e.Use(middleware.Auth(fakeValidator{uid: "u1"}))
			e.GET("/p", func(c *gin.Context) { c.Status(http.StatusOK) })
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/p", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			e.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("header=%q: status = %d, want 401", tc.header, w.Code)
			}
		})
	}
}

func TestAuth_emptyUID401(t *testing.T) {
	// Validator succeeds but returns an empty uid — must be rejected.
	e := newEngine()
	e.Use(middleware.Auth(fakeValidator{uid: ""})) // empty uid, nil error
	e.GET("/p", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAuth_actorUID_inGinContext(t *testing.T) {
	// The uid must be accessible via c.GetString("actor_uid"), not just via the
	// request context.  (This is distinct from TestAuth_injectsActor which checks
	// service.ActorFrom.)
	e := newEngine()
	e.Use(middleware.Auth(fakeValidator{uid: "alice"}))
	var gotUID string
	e.GET("/p", func(c *gin.Context) {
		gotUID = c.GetString("actor_uid")
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer tok")
	e.ServeHTTP(w, req)
	if gotUID != "alice" {
		t.Fatalf("actor_uid in gin context = %q, want %q", gotUID, "alice")
	}
}

// ── RequestID extras ─────────────────────────────────────────────────────────

func TestRequestID_reuseInboundID(t *testing.T) {
	e := newEngine()
	e.Use(middleware.RequestID())
	e.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	const want = "my-correlation-id"
	req.Header.Set(middleware.RequestIDHeader, want)
	e.ServeHTTP(w, req)
	if got := w.Header().Get(middleware.RequestIDHeader); got != want {
		t.Fatalf("response X-Request-ID = %q, want %q", got, want)
	}
}

func TestRequestID_availableInGinContext(t *testing.T) {
	e := newEngine()
	e.Use(middleware.RequestID())
	var ginRID string
	e.GET("/", func(c *gin.Context) {
		ginRID = c.GetString("request_id")
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	responseRID := w.Header().Get(middleware.RequestIDHeader)
	if responseRID == "" {
		t.Fatal("no X-Request-ID in response")
	}
	if ginRID != responseRID {
		t.Fatalf("gin context request_id=%q, response header=%q — must match", ginRID, responseRID)
	}
}

func TestRequestID_availableInRequestContext(t *testing.T) {
	e := newEngine()
	e.Use(middleware.RequestID())
	var ctxRID string
	e.GET("/", func(c *gin.Context) {
		ctxRID = middleware.RequestIDFrom(c.Request.Context())
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	const inbound = "req-ctx-check"
	req.Header.Set(middleware.RequestIDHeader, inbound)
	e.ServeHTTP(w, req)
	if ctxRID != inbound {
		t.Fatalf("RequestIDFrom(ctx) = %q, want %q", ctxRID, inbound)
	}
}

// RequestIDFrom on a plain background context returns "".
func TestRequestIDFrom_emptyForMissingKey(t *testing.T) {
	if got := middleware.RequestIDFrom(context.Background()); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

// ── Recovery extras ──────────────────────────────────────────────────────────

func TestRecovery_passThroughNoPanic(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Recovery(zap.NewNop()))
	e.GET("/ok", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}

// ── CORS ─────────────────────────────────────────────────────────────────────

func TestCORS_permissive_realRequest(t *testing.T) {
	// Empty origins slice → wildcard; real request gets Allow-Origin: *
	e := newEngine()
	e.Use(middleware.CORS(nil))
	e.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://any-site.example")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("ACAO = %q, want *", got)
	}
}

func TestCORS_permissive_preflight204(t *testing.T) {
	e := newEngine()
	e.Use(middleware.CORS(nil))
	e.OPTIONS("/", func(*gin.Context) {}) // gin requires a route to be registered
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://any-site.example")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("ACAO = %q, want *", got)
	}
}

func TestCORS_exposesAccountConsoleHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(middleware.CORS(nil))
	e.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("Origin", "https://accounts.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "authorization,idempotency-key,x-aero-write-epoch")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", w.Code)
	}
	allowed := w.Header().Get("Access-Control-Allow-Headers")
	for _, name := range []string{"Authorization", "Idempotency-Key", "X-Aero-Write-Epoch"} {
		if !strings.Contains(allowed, name) {
			t.Errorf("Access-Control-Allow-Headers = %q, missing %q", allowed, name)
		}
	}
	exposed := w.Header().Get("Access-Control-Expose-Headers")
	for _, name := range []string{"X-Request-ID", "X-Trace-ID", "X-Aero-Degraded", "X-Aero-Write-Epoch"} {
		if !strings.Contains(exposed, name) {
			t.Errorf("Access-Control-Expose-Headers = %q, missing %q", exposed, name)
		}
	}
}

func TestCORS_allowlisted_origin(t *testing.T) {
	const origin = "https://trusted.example"
	e := newEngine()
	e.Use(middleware.CORS([]string{origin}))
	e.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", origin)
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("ACAO = %q, want %q", got, origin)
	}
	if got := w.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
}

func TestCORS_allowlisted_preflight204(t *testing.T) {
	const origin = "https://trusted.example"
	e := newEngine()
	e.Use(middleware.CORS([]string{origin}))
	e.OPTIONS("/", func(*gin.Context) {})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", origin)
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}

func TestCORS_disallowed_preflight403(t *testing.T) {
	e := newEngine()
	e.Use(middleware.CORS([]string{"https://trusted.example"}))
	e.OPTIONS("/", func(*gin.Context) {})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestCORS_disallowed_realRequest_noCORSHeaders(t *testing.T) {
	// A real (non-preflight) request from a disallowed origin is let through but
	// receives no CORS headers (browser will block it, which is correct behavior).
	e := newEngine()
	e.Use(middleware.CORS([]string{"https://trusted.example"}))
	e.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected ACAO header %q for disallowed origin", got)
	}
}

func TestCORS_noOriginHeader_noCorHeaders(t *testing.T) {
	// Requests without an Origin header (same-origin, non-browser) should
	// receive no CORS headers and proceed normally.
	e := newEngine()
	e.Use(middleware.CORS(nil)) // permissive
	e.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected ACAO header %q when Origin is absent", got)
	}
}

// ── AccessLog ────────────────────────────────────────────────────────────────

func TestAccessLog_passThroughAndNoOp(t *testing.T) {
	// AccessLog must not interfere with the handler's response.
	e := newEngine()
	e.Use(middleware.AccessLog(zap.NewNop()))
	e.GET("/check", func(c *gin.Context) { c.Status(http.StatusAccepted) })
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/check", nil))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
}

// AccessLog should not panic even when request_id and actor_uid are absent.
func TestAccessLog_doesNotPanicWithoutContext(t *testing.T) {
	e := newEngine()
	e.Use(middleware.AccessLog(zap.NewNop()))
	e.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	// Verify no panic by the fact that ServeHTTP returns normally.
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
}

// Combined: AccessLog + RequestID expose request_id in the log field.
func TestAccessLog_withRequestID(t *testing.T) {
	e := newEngine()
	e.Use(middleware.RequestID())
	e.Use(middleware.AccessLog(zap.NewNop()))
	e.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(middleware.RequestIDHeader, "test-rid")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

// newCapturingLog builds a zap logger writing JSON to an in-memory buffer. Test
// capture only — no production WithWriter API is involved (out of scope).
func newCapturingLog(buf *bytes.Buffer) *zap.Logger {
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(buf),
		zapcore.DebugLevel,
	)
	return zap.New(core)
}

// AccessLog must emit trace_id and it must equal the X-Trace-ID response header
// (the RequestID -> Tracing -> AccessLog chain order guarantees both agree).
// The header is asserted non-empty before comparing so an empty==empty pass can
// never be vacuous.
func TestAccessLog_withTraceID(t *testing.T) {
	prev := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(t.Context())
		otel.SetTracerProvider(prev)
	})

	var buf bytes.Buffer
	e := newEngine()
	e.Use(middleware.RequestID())
	e.Use(middleware.Tracing())
	e.Use(middleware.AccessLog(newCapturingLog(&buf)))
	e.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(middleware.RequestIDHeader, "rid-1")
	e.ServeHTTP(w, req)

	traceHeader := w.Header().Get("X-Trace-ID")
	if traceHeader == "" {
		t.Fatal("expected a non-empty X-Trace-ID header (assert before comparing)")
	}
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("access log line is not valid JSON: %v (line=%q)", err, buf.String())
	}
	gotTrace, _ := line["trace_id"].(string)
	if gotTrace == "" {
		t.Fatalf("access log line missing trace_id: %v", line)
	}
	if gotTrace != traceHeader {
		t.Fatalf("trace_id = %q, X-Trace-ID header = %q — must match", gotTrace, traceHeader)
	}
	if got, _ := line["request_id"].(string); got != "rid-1" {
		t.Fatalf("request_id = %q, want %q", got, "rid-1")
	}
}

// With an authenticated request, AccessLog must keep the existing request_id and
// actor field keys/values unchanged and additionally carry trace_id. Guards
// against accidentally renaming `actor` or dropping fields during the edit.
func TestAccessLog_withActorCarrier(t *testing.T) {
	prev := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(t.Context())
		otel.SetTracerProvider(prev)
	})

	var buf bytes.Buffer
	e := newEngine()
	e.Use(middleware.RequestID())
	e.Use(middleware.Tracing())
	e.Use(middleware.Auth(fakeValidator{uid: "u1"}))
	e.Use(middleware.AccessLog(newCapturingLog(&buf)))
	e.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer tok")
	e.ServeHTTP(w, req)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("access log line is not valid JSON: %v (line=%q)", err, buf.String())
	}
	if got, _ := line["actor"].(string); got != "u1" {
		t.Fatalf("actor = %q, want %q (field key must stay 'actor')", got, "u1")
	}
	if got, _ := line["request_id"].(string); got == "" {
		t.Fatal("request_id field missing or empty")
	}
	if got, _ := line["trace_id"].(string); got == "" {
		t.Fatal("trace_id field missing or empty")
	}
	if got := w.Header().Get("X-Trace-ID"); got == "" {
		t.Fatal("expected an X-Trace-ID correlation header")
	}
}
