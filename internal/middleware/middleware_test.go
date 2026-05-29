package middleware_test

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/ratelimit"
	"github.com/aero/aero-id/internal/service"
)

type fakeValidator struct {
	uid string
	err error
}

func (f fakeValidator) ValidateToken(context.Context, string) (string, error) {
	return f.uid, f.err
}

func newEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	return gin.New()
}

func TestRequestID_setsHeader(t *testing.T) {
	e := newEngine()
	e.Use(middleware.RequestID())
	e.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Header().Get(middleware.RequestIDHeader) == "" {
		t.Fatal("expected a request id response header")
	}
}

func TestRecovery_returns500(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Recovery(zap.NewNop()))
	e.GET("/boom", func(*gin.Context) { panic("kaboom") })
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not valid JSON: %v (body=%s)", err, w.Body.String())
	}
	if body.Error.Code != "internal.panic" {
		t.Fatalf("expected error code %q, got %q (body=%s)", "internal.panic", body.Error.Code, w.Body.String())
	}
}

func TestAuth_missingToken401(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Auth(fakeValidator{uid: "u1"}))
	e.GET("/p", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/p", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAuth_invalidToken401(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Auth(fakeValidator{err: stderrors.New("bad")}))
	e.GET("/p", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer xyz")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRateLimit_blocksOverLimitWithRetryAfter(t *testing.T) {
	e := newEngine()
	// 1 req/sec sustained, burst 3: a quick burst yields some 200s then 429s.
	l := ratelimit.New(1, 3, time.Minute)
	e.Use(middleware.RateLimit(l))
	e.GET("/p", func(c *gin.Context) { c.Status(http.StatusOK) })

	var ok, limited int
	var retryAfter string
	for i := 0; i < 10; i++ {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/p", nil))
		switch w.Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
			retryAfter = w.Header().Get("Retry-After")
		default:
			t.Fatalf("unexpected status %d", w.Code)
		}
	}
	if ok == 0 {
		t.Fatal("expected some requests under the burst to pass")
	}
	if limited == 0 {
		t.Fatal("expected some requests over the limit to be 429'd")
	}
	if retryAfter == "" {
		t.Fatal("429 must carry a Retry-After header")
	}
}

func TestRateLimit_nilLimiterIsNoop(t *testing.T) {
	e := newEngine()
	e.Use(middleware.RateLimit(nil))
	e.GET("/p", func(c *gin.Context) { c.Status(http.StatusOK) })
	for i := 0; i < 50; i++ {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/p", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("nil limiter should pass everything, got %d", w.Code)
		}
	}
}

func TestRateLimit_distinctActorsIndependent(t *testing.T) {
	e := newEngine()
	l := ratelimit.New(1, 1, time.Minute)
	// Inject distinct actors via a stub before the limiter, then assert each
	// actor gets its own bucket.
	e.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithActor(c.Request.Context(), c.GetHeader("X-Actor")))
		c.Next()
	})
	e.Use(middleware.RateLimit(l))
	e.GET("/p", func(c *gin.Context) { c.Status(http.StatusOK) })

	call := func(actor string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/p", nil)
		req.Header.Set("X-Actor", actor)
		e.ServeHTTP(w, req)
		return w.Code
	}
	if call("a") != http.StatusOK {
		t.Fatal("actor a first call should pass")
	}
	if call("b") != http.StatusOK {
		t.Fatal("actor b has its own bucket and should pass")
	}
	if call("a") != http.StatusTooManyRequests {
		t.Fatal("actor a is now exhausted")
	}
}

func TestAuth_injectsActor(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Auth(fakeValidator{uid: "u1"}))
	var got string
	e.GET("/p", func(c *gin.Context) {
		got, _ = service.ActorFrom(c.Request.Context())
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer xyz")
	e.ServeHTTP(w, req)
	if w.Code != http.StatusOK || got != "u1" {
		t.Fatalf("status=%d actor=%q", w.Code, got)
	}
}
