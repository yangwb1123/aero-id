package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

type operationScopeReadRepo struct {
	repository.OperationRepository
	operation *domain.Operation
}

func (r *operationScopeReadRepo) Get(context.Context, domain.ID) (*domain.Operation, error) {
	return r.operation, nil
}

type operationScopeReadPermission struct {
	allowed bool
	calls   []string
}

func (p *operationScopeReadPermission) HasPermission(_ context.Context, actor, permission string) (bool, error) {
	p.calls = append(p.calls, actor+":"+permission)
	return p.allowed, nil
}

func TestOperationScopeMetadataRequiresExistingReadAuthority(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, item := range []struct {
		name, actor, tenant string
		allowed             bool
		status              int
	}{
		{"owner", "initiator", "tenant-a", false, http.StatusOK},
		{"current reader allowed", "reader", "tenant-a", true, http.StatusOK},
		{"current reader denied", "reader", "tenant-a", false, http.StatusForbidden},
		{"foreign tenant even if allowed", "reader", "tenant-b", true, http.StatusNotFound},
		{"missing tenant", "initiator", "", true, http.StatusForbidden},
		{"missing actor", "", "tenant-a", true, http.StatusUnauthorized},
	} {
		t.Run(item.name, func(t *testing.T) {
			id := uuid.MustParse("01800000-0000-7000-8000-000000000003")
			repo := &operationScopeReadRepo{operation: &domain.Operation{ID: id, TenantID: "tenant-a", ActorUID: "initiator",
				Status: domain.OperationRequested, Steps: []*domain.OperationStep{{StepKey: "im", Source: "aero-im", Action: "summary",
					RequestPayload: map[string]any{"datasets": []string{"aero-im.workspaces"}}}}}}
			permission := &operationScopeReadPermission{allowed: item.allowed}
			svc := service.NewOperationService(repo, service.WithOperationPermission(permission))
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				// Isolated service-context seam, not a JWT or production auth bypass.
				ctx := service.WithIdentity(c.Request.Context(), &domain.TokenIdentity{UID: item.actor, TenantID: item.tenant, Verified: true})
				c.Request = c.Request.WithContext(ctx)
				c.Next()
			})
			httpapi.NewOperationHandler(svc).RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/operations/"+id.String(), nil))
			if recorder.Code != item.status {
				t.Fatalf("status = %d, want %d", recorder.Code, item.status)
			}
			if item.status != http.StatusOK && strings.Contains(recorder.Body.String(), "aero-im.workspaces") {
				t.Fatal("denied read exposed step scope")
			}
			if item.status == http.StatusOK && !strings.Contains(recorder.Body.String(), `"dataset_scope":{"status":"explicit","datasets":["aero-im.workspaces"]}`) {
				t.Fatal("authorized service read lost its safe explicit scope metadata")
			}
			if item.actor == "reader" && item.tenant == "tenant-a" &&
				(len(permission.calls) != 1 || permission.calls[0] != "reader:"+domain.PermOperationRead) {
				t.Fatal("metadata must retain the existing current read check")
			}
		})
	}
}
