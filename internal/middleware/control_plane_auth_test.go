package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/service"
)

type claimsValidator struct {
	identity *domain.TokenIdentity
	err      error
}

func (v claimsValidator) ValidateToken(context.Context, string) (string, error) {
	if v.identity == nil {
		return "", v.err
	}
	return v.identity.UID, v.err
}

func (v claimsValidator) ValidateControlPlaneIdentity(context.Context, string) (*domain.TokenIdentity, error) {
	return v.identity, v.err
}

func TestControlPlaneAuthRejectsUIDOnlyValidator(t *testing.T) {
	e := gin.New()
	e.Use(middleware.ControlPlaneAuth(fakeValidator{uid: "u1"}))
	e.GET("/protected", func(c *gin.Context) { c.Status(http.StatusOK) })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer token")
	e.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestControlPlaneAuthInjectsCompleteIdentity(t *testing.T) {
	identity := &domain.TokenIdentity{UID: "u1", Issuer: "snaplink", Subject: "u1", TenantID: "tenant-1", ClientID: "console"}
	e := gin.New()
	e.Use(middleware.ControlPlaneAuth(claimsValidator{identity: identity}))
	var got *domain.TokenIdentity
	e.GET("/protected", func(c *gin.Context) {
		got, _ = service.IdentityFrom(c.Request.Context())
		c.Status(http.StatusOK)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer token")
	e.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || got != identity {
		t.Fatalf("status = %d identity = %+v", recorder.Code, got)
	}
}

func TestControlPlaneAuthRejectsIncompleteIdentity(t *testing.T) {
	identity := &domain.TokenIdentity{UID: "u1", Issuer: "snaplink", Subject: "u1", ClientID: "console"}
	e := gin.New()
	e.Use(middleware.ControlPlaneAuth(claimsValidator{identity: identity}))
	e.GET("/protected", func(c *gin.Context) { c.Status(http.StatusOK) })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer token")
	e.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}
