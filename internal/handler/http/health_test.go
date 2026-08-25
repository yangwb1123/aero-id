package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	httpapi "github.com/aero/aero-id/internal/handler/http"
)

func TestHealthHandler_Ready(t *testing.T) {
	ok := func(context.Context) error { return nil }
	down := func(context.Context) error { return errors.New("down") }

	cases := []struct {
		name       string
		checks     []httpapi.Check
		wantCode   int
		wantStatus string
	}{
		{"no checks", nil, http.StatusOK, "ready"},
		{"all ok", []httpapi.Check{{Name: "database", Critical: true, Probe: ok}}, http.StatusOK, "ready"},
		{"critical down", []httpapi.Check{{Name: "database", Critical: true, Probe: down}}, http.StatusServiceUnavailable, "unavailable"},
		{"optional down still serves", []httpapi.Check{
			{Name: "database", Critical: true, Probe: ok},
			{Name: "sso", Critical: false, Probe: down},
		}, http.StatusOK, "degraded"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := httpapi.NewHealthHandler(tc.checks...)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/readyz", nil)

			h.Ready(c)

			if w.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d", w.Code, tc.wantCode)
			}
			var body struct {
				Status string            `json:"status"`
				Checks map[string]string `json:"checks"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", body.Status, tc.wantStatus)
			}
		})
	}
}

func TestHealthHandler_SetReadyRejectsTrafficDuringDrain(t *testing.T) {
	h := httpapi.NewHealthHandler()
	h.SetReady(false)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/readyz", nil)

	h.Ready(c)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(w.Body.String(), "draining") {
		t.Fatalf("body = %s, want draining reason", w.Body.String())
	}

	h.SetReady(true)
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.Ready(c)
	if w.Code != http.StatusOK {
		t.Fatalf("restored code = %d, want %d", w.Code, http.StatusOK)
	}
}
