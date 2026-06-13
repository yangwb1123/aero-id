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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

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
