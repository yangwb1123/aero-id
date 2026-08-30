package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRegionEpochWriter_ComposesWithCaptureWriter(t *testing.T) {
	var captured *captureWriter
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 4}, "us-east")
	router := gin.New()
	router.Use(func(c *gin.Context) {
		captured = &captureWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = captured
		c.Next()
	})
	router.Use(header.Middleware())
	router.PATCH("/v1/profile", func(c *gin.Context) {
		c.Status(http.StatusCreated)
		_, _ = c.Writer.WriteString("created")
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/v1/profile", nil))
	if rec.Code != http.StatusCreated || rec.Body.String() != "created" {
		t.Fatalf("response = %d/%q, want 201/created", rec.Code, rec.Body.String())
	}
	if got := rec.Result().Header.Get("X-Aero-Write-Epoch"); got != "us-east:4" {
		t.Fatalf("committed epoch = %q, want us-east:4", got)
	}
	if captured == nil || captured.body.String() != "created" {
		t.Fatalf("capture body = %q, want created", captured.body.String())
	}
}

func TestRegionEpochWriter_PreservesGinOptionalInterfaces(t *testing.T) {
	header := NewRegionEpochHeader(&fakeEpochProvider{epoch: 4}, "us-east")
	router := gin.New()
	router.Use(header.Middleware())
	router.PATCH("/v1/flush", func(c *gin.Context) {
		if _, ok := c.Writer.(http.Hijacker); !ok {
			t.Error("wrapped writer lost http.Hijacker")
		}
		if _, ok := c.Writer.(http.Flusher); !ok {
			t.Error("wrapped writer lost http.Flusher")
		}
		//nolint:staticcheck // Gin's ResponseWriter intentionally preserves this legacy interface.
		if _, ok := c.Writer.(http.CloseNotifier); !ok {
			t.Error("wrapped writer lost http.CloseNotifier")
		}
		// Gin exposes HTTP/2 server push through its Pusher accessor.
		if c.Writer.Pusher() != nil {
			t.Error("unexpected server pusher for httptest recorder")
		}
		if err := http.NewResponseController(c.Writer).Flush(); err != nil {
			t.Errorf("response controller flush = %v", err)
		}
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/v1/flush", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Result().Header.Get("X-Aero-Write-Epoch"); got != "us-east:4" {
		t.Fatalf("flushed epoch = %q, want us-east:4", got)
	}
}
