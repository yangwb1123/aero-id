package middleware_test

import (
	"context"
	stderrors "errors"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/middleware"
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
