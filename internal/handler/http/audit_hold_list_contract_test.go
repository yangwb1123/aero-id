package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

func TestOpenAPIAuditHoldListRequiresObjectArray(t *testing.T) {
	spec := loadOpenAPISpec(t)
	var response struct {
		Content map[string]struct {
			Schema auditContractSchema `yaml:"schema"`
		} `yaml:"content"`
	}
	decodeAuditContract(t, spec.Paths["/admin/audit/holds"]["get"].Responses["200"], &response)
	if response.Content["application/json"].Schema.Ref != "#/components/schemas/HoldListEnvelope" {
		t.Fatal("Legal Hold list must use its published data envelope")
	}
	var envelope auditContractSchema
	decodeAuditContract(t, spec.Components.Schemas["HoldListEnvelope"], &envelope)
	data := envelope.Properties["data"]
	if envelope.Type != "object" || !slices.Contains(envelope.Required, "data") || data.Type != "object" {
		t.Fatal("Legal Hold list must require a data object")
	}
	holds := data.Properties["holds"]
	if !slices.Contains(data.Required, "holds") || holds.Type != "array" || holds.Items == nil || holds.Items.Type != "object" {
		t.Fatal("Legal Hold list must require a root object array, including empty results")
	}
}

type holdListContractService struct {
	service.AuditHoldService
	holds []*repository.AuditHold
	seen  []string
	err   error
}

func (s *holdListContractService) List(_ context.Context, tenant string) ([]*repository.AuditHold, error) {
	s.seen = append(s.seen, tenant)
	return s.holds, s.err
}

func holdListContractRouter(svc service.AuditHoldService, checker *anchorPermissionChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("actor_uid", "operator-1")
		ctx := service.WithIdentity(c.Request.Context(), &domain.TokenIdentity{UID: "operator-1", TenantID: "tenant-a"})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	httpapi.NewAuditLedgerHandlerWithExtensions(nil, svc, nil, nil, checker).
		RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
	return engine
}

func TestAuditHoldListHTTPAlwaysReturnsObjectArray(t *testing.T) {
	at := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	expiry := at.Add(24 * time.Hour)
	rows := []*repository.AuditHold{
		{ScopeType: domain.AuditHoldScopeAccount, ScopeID: "account-1", Reason: "case-1", CreatedBy: "operator-1", CreatedAt: at},
		{ScopeType: domain.AuditHoldScopeActor, ScopeID: "actor-1", Reason: "case-2", CreatedBy: "operator-2", CreatedAt: at, ExpiresAt: &expiry},
	}
	for _, name := range []string{"nil", "empty", "rows"} {
		t.Run(name, func(t *testing.T) {
			svc := &holdListContractService{}
			if name == "empty" {
				svc.holds = []*repository.AuditHold{}
			} else if name == "rows" {
				svc.holds = rows
			}
			checker := &anchorPermissionChecker{allowed: map[string]bool{domain.PermAuditRead: true}}
			engine := holdListContractRouter(svc, checker)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
				"/v1/admin/audit/holds?tenant_id=untrusted-tenant", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("Legal Hold list status = %d", recorder.Code)
			}
			assertHoldListContract(t, recorder, svc.holds)
			if !slices.Equal(svc.seen, []string{"tenant-a"}) || !slices.Equal(checker.seen, []string{domain.PermAuditRead}) {
				t.Fatal("Hold list must use the context tenant and check read permission exactly once")
			}
		})
	}
}

func assertHoldListContract(t *testing.T, recorder *httptest.ResponseRecorder, want []*repository.AuditHold) {
	t.Helper()
	var envelope struct {
		Data struct {
			Holds []map[string]any `json:"holds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Holds == nil || len(envelope.Data.Holds) != len(want) {
		t.Fatal("Legal Hold list must return an array, not null or an omitted field")
	}
	for i, hold := range envelope.Data.Holds {
		expected := want[i]
		if len(hold) != 6 || hold["scope_type"] != string(expected.ScopeType) || hold["scope_id"] != expected.ScopeID ||
			hold["reason"] != expected.Reason || hold["created_by"] != expected.CreatedBy ||
			hold["created_at"] != expected.CreatedAt.Format(time.RFC3339Nano) {
			t.Fatal("Legal Hold list must preserve row order and the published fields")
		}
		var expiry any
		if expected.ExpiresAt != nil {
			expiry = expected.ExpiresAt.Format(time.RFC3339Nano)
		}
		if hold["expires_at"] != expiry {
			t.Fatal("Legal Hold expiry must preserve the original nullable timestamp")
		}
	}
}

func TestAuditHoldListHTTPDenialAndReadErrorExposeNoRows(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "read_denied", true: "query_failed"}[allowed], func(t *testing.T) {
			svc := &holdListContractService{err: apperr.Internal("audit_hold.list_failed", "Hold query unavailable")}
			checker := &anchorPermissionChecker{allowed: map[string]bool{domain.PermAuditRead: allowed}}
			engine := holdListContractRouter(svc, checker)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/admin/audit/holds", nil))
			wantStatus, wantCalls := http.StatusForbidden, 0
			if allowed {
				wantStatus, wantCalls = http.StatusInternalServerError, 1
			}
			var envelope map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != wantStatus || len(svc.seen) != wantCalls || envelope["data"] != nil || envelope["error"] == nil {
				t.Fatal("Hold list must propagate denial/read failure without returning an empty success")
			}
		})
	}
}
