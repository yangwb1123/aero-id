package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/hub"
)

func TestEventsHandler_NewAndRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := hub.New()
	defer h.Close()

	handler := httpapi.NewEventsHandler(h, nil)
	if handler == nil {
		t.Fatal("handler should not be nil")
	}

	log, _ := zap.NewDevelopment()
	health := httpapi.NewHealthHandler()
	router := httpapi.NewRouter(log, httpapi.RouterConfig{}, nil, health, handler)

	routes := router.Routes()
	var eventsRouteFound bool
	for _, rt := range routes {
		// The route is mounted under /v1 group (auth group).
		if rt.Path == "/v1/events" && rt.Method == "GET" {
			eventsRouteFound = true
			break
		}
	}
	if !eventsRouteFound {
		t.Errorf("expected GET /v1/events route; got %v", routes)
	}
}

func TestEventsHandler_Stream_Unauthenticated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := hub.New()
	defer h.Close()

	handler := httpapi.NewEventsHandler(h, nil)
	log, _ := zap.NewDevelopment()
	health := httpapi.NewHealthHandler()
	router := httpapi.NewRouter(log, httpapi.RouterConfig{}, nil, health, handler)

	// /events is under /v1 auth group, so the path is /v1/events.
	req := httptest.NewRequest("GET", "/v1/events", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
