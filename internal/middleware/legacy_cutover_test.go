package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/service"
)

type fakeCutoverChecker struct {
	cutOver bool
	err     error
}

func (f *fakeCutoverChecker) IsCutOver(ctx context.Context, tenantID string) (bool, error) {
	return f.cutOver, f.err
}

func runCutoverRequest(t *testing.T, checker CutoverChecker, path string, tenantID string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if tenantID != "" {
			c.Request = c.Request.WithContext(service.WithIdentity(c.Request.Context(), &domain.TokenIdentity{
				TenantID: tenantID, Subject: "u-1", UID: "u-1",
			}))
		}
		c.Next()
	})
	router.Use(LegacyCutoverGuard(checker))
	router.GET("/v1/files", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/v1/me", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestLegacyCutoverGuard_BlocksCutOverTenants: a cut-over tenant gets 410 on
// the legacy surface with a stable code; control-plane routes are untouched.
func TestLegacyCutoverGuard_BlocksCutOverTenants(t *testing.T) {
	checker := &fakeCutoverChecker{cutOver: true}
	if rec := runCutoverRequest(t, checker, "/v1/files", "tenant-a"); rec.Code != http.StatusGone {
		t.Errorf("legacy read = %d, want 410", rec.Code)
	}
	if rec := runCutoverRequest(t, checker, "/v1/me", "tenant-a"); rec.Code != http.StatusOK {
		t.Errorf("control-plane read = %d, want 200", rec.Code)
	}
}

// TestLegacyCutoverGuard_ServesLegacyTenants: default and rolled-back tenants
// keep serving the compatibility surface.
func TestLegacyCutoverGuard_ServesLegacyTenants(t *testing.T) {
	checker := &fakeCutoverChecker{cutOver: false}
	if rec := runCutoverRequest(t, checker, "/v1/files", "tenant-a"); rec.Code != http.StatusOK {
		t.Errorf("legacy read = %d, want 200", rec.Code)
	}
}

// TestLegacyCutoverGuard_FailsOpenOnCheckerOutage: the registry is an admin
// marker, not an authorization control — an outage must not break legacy reads.
func TestLegacyCutoverGuard_FailsOpenOnCheckerOutage(t *testing.T) {
	checker := &fakeCutoverChecker{err: errors.New("registry down")}
	if rec := runCutoverRequest(t, checker, "/v1/files", "tenant-a"); rec.Code != http.StatusOK {
		t.Errorf("legacy read during checker outage = %d, want 200", rec.Code)
	}
}

// TestLegacyCutoverGuard_NilCheckerIsNoop.
func TestLegacyCutoverGuard_NilCheckerIsNoop(t *testing.T) {
	if rec := runCutoverRequest(t, nil, "/v1/files", "tenant-a"); rec.Code != http.StatusOK {
		t.Errorf("nil checker = %d, want 200", rec.Code)
	}
}
