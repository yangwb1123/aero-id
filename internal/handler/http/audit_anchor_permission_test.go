package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/service"
)

type anchorPermissionChecker struct {
	allowed map[string]bool
	seen    []string
}

func (c *anchorPermissionChecker) HasPermission(_ context.Context, _, permission string) (bool, error) {
	c.seen = append(c.seen, permission)
	return c.allowed[permission], nil
}

type anchorRouteService struct {
	runs      int
	manifests int
}

func (s *anchorRouteService) AnchorOnce(context.Context) (*service.AnchorRunResult, error) {
	s.runs++
	return &service.AnchorRunResult{}, nil
}

func (s *anchorRouteService) AnchorManifest(context.Context, string, string) (map[string]any, error) {
	s.manifests++
	return map[string]any{"kind": "audit_anchor"}, nil
}

func TestAuditAnchorRoutesSeparateReadAndWritePermissions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	checker := &anchorPermissionChecker{allowed: map[string]bool{domain.PermAuditRead: true}}
	anchors := &anchorRouteService{}
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("actor_uid", "operator-1")
		c.Next()
	})
	httpapi.NewAuditLedgerHandlerWithExtensions(nil, nil, anchors, nil, checker).
		RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})

	assertAnchorRoute(t, engine, http.MethodPost, "/v1/admin/audit/anchor", http.StatusForbidden)
	assertAnchorRoute(t, engine, http.MethodGet,
		"/v1/admin/audit/anchor/manifest/aero-id/tenant-a", http.StatusOK)
	if anchors.runs != 0 || anchors.manifests != 1 {
		t.Fatalf("anchor calls = runs:%d manifests:%d, want 0/1", anchors.runs, anchors.manifests)
	}
	if len(checker.seen) != 2 || checker.seen[0] != domain.PermSystemConfig ||
		checker.seen[1] != domain.PermAuditRead {
		t.Fatalf("checked permissions = %v", checker.seen)
	}

	checker.allowed = map[string]bool{domain.PermSystemConfig: true}
	assertAnchorRoute(t, engine, http.MethodPost, "/v1/admin/audit/anchor", http.StatusOK)
	assertAnchorRoute(t, engine, http.MethodGet,
		"/v1/admin/audit/anchor/manifest/aero-id/tenant-a", http.StatusForbidden)
	if anchors.runs != 1 || anchors.manifests != 1 {
		t.Fatalf("anchor calls = runs:%d manifests:%d, want 1/1", anchors.runs, anchors.manifests)
	}
}

func assertAnchorRoute(t *testing.T, engine *gin.Engine, method, path string, wantStatus int) {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body = %s",
			method, path, recorder.Code, wantStatus, recorder.Body.String())
	}
}
