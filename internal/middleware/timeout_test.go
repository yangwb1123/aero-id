package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/middleware"
)

func TestTimeout_setsDeadlineWhenPositive(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Timeout(5 * time.Second))
	var hasDeadline bool
	e.GET("/", func(c *gin.Context) {
		_, hasDeadline = c.Request.Context().Deadline()
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if !hasDeadline {
		t.Fatal("expected context to have a deadline when d > 0")
	}
}

func TestTimeout_noDeadlineWhenZero(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Timeout(0))
	var hasDeadline bool
	e.GET("/", func(c *gin.Context) {
		_, hasDeadline = c.Request.Context().Deadline()
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if hasDeadline {
		t.Fatal("expected no deadline when d <= 0")
	}
}

func TestTimeout_contextCancelledAfterExpiry(t *testing.T) {
	e := newEngine()
	e.Use(middleware.Timeout(10 * time.Millisecond))
	var ctxErr error
	e.GET("/", func(c *gin.Context) {
		ctx := c.Request.Context()
		time.Sleep(20 * time.Millisecond)
		ctxErr = ctx.Err()
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if ctxErr == nil {
		t.Fatal("expected context to be cancelled after deadline exceeded")
	}
}
