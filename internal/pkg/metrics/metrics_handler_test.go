package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/pkg/metrics"
)

func TestRegistry_Handler(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.ObserveHTTP("GET", "/v1/files", 200, 10*time.Millisecond)
	reg.ObserveSSOCache("token", true)

	h := reg.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if w.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("content-type = %q", w.Header().Get("Content-Type"))
	}
	if !strings.Contains(body, "aeroid_requests_total") {
		t.Error("expected aeroid_requests_total in output")
	}
	if !strings.Contains(body, "aeroid_sso_cache_hits_total") {
		t.Error("expected aeroid_sso_cache_hits_total in output")
	}
}

func TestRegistry_Handler_Empty(t *testing.T) {
	reg := metrics.NewRegistry()
	h := reg.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "# HELP aeroid_requests_total") {
		t.Error("expected help header even with no data")
	}
}
