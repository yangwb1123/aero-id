package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/service"
)

type routerClaimsValidator struct{}

func (routerClaimsValidator) ValidateToken(context.Context, string) (string, error) { return "u1", nil }
func (routerClaimsValidator) ValidateControlPlaneIdentity(context.Context, string) (*domain.TokenIdentity, error) {
	return &domain.TokenIdentity{UID: "u1", Issuer: "snaplink", Subject: "u1", TenantID: "tenant-1", ClientID: "console"}, nil
}

type splitRouteRegistrar struct{}

func (splitRouteRegistrar) RegisterRoutes(r httpapi.Routes) {
	r.Auth.GET("/legacy", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ControlPlane.GET("/control-plane", func(c *gin.Context) {
		if _, ok := service.IdentityFrom(c.Request.Context()); !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
	})
}

func TestRouterSeparatesControlPlaneAndLegacyAuthentication(t *testing.T) {
	router := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, routerClaimsValidator{},
		httpapi.NewHealthHandler(), splitRouteRegistrar{})

	request := httptest.NewRequest(http.MethodGet, "/v1/legacy", nil)
	request.Header.Set("Authorization", "Bearer token")
	legacy := httptest.NewRecorder()
	router.ServeHTTP(legacy, request)
	if legacy.Code != http.StatusOK {
		t.Fatalf("legacy route status = %d, want 200", legacy.Code)
	}

	controlPlane := httptest.NewRecorder()
	router.ServeHTTP(controlPlane, httptest.NewRequest(http.MethodGet, "/v1/control-plane", nil))
	if controlPlane.Code != http.StatusUnauthorized {
		t.Fatalf("control-plane route without token = %d, want 401", controlPlane.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/control-plane", nil)
	request.Header.Set("Authorization", "Bearer token")
	controlPlane = httptest.NewRecorder()
	router.ServeHTTP(controlPlane, request)
	if controlPlane.Code != http.StatusOK {
		t.Fatalf("control-plane route status = %d, want 200", controlPlane.Code)
	}
}
