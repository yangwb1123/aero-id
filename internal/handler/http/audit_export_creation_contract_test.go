package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

type auditExportCreationStub struct {
	service.SyncJobService
	job     *domain.SyncJob
	err     error
	inputs  []service.SyncJobInput
	tenants []string
}

func (s *auditExportCreationStub) Create(ctx context.Context, input service.SyncJobInput) (*domain.SyncJob, error) {
	s.inputs = append(s.inputs, input)
	s.tenants = append(s.tenants, service.TenantFrom(ctx))
	return s.job, s.err
}

func auditExportCreationRouter(svc service.SyncJobService, checker *anchorPermissionChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("actor_uid", "operator-1")
		ctx := service.WithIdentity(c.Request.Context(), &domain.TokenIdentity{UID: "operator-1", TenantID: "tenant-a"})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	httpapi.NewAuditLedgerHandlerWithExtensions(nil, nil, nil, svc, checker).
		RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
	return engine
}

func TestAuditExportCreationHTTPReturnsRootJobUUID(t *testing.T) {
	for _, bodyKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "header_key", true: "body_key"}[bodyKey], func(t *testing.T) {
			id := uuid.MustParse("01800000-0000-7000-8000-000000000003")
			svc := &auditExportCreationStub{job: &domain.SyncJob{ID: id, Status: domain.SyncJobPending}}
			checker := &anchorPermissionChecker{allowed: map[string]bool{domain.PermAuditExport: true}}
			body, wantKey := `{}`, "original-header-key"
			if bodyKey {
				body, wantKey = `{"idempotency_key":"original-body-key"}`, "original-body-key"
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/audit/export?tenant_id=untrusted-tenant", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "original-header-key")
			auditExportCreationRouter(svc, checker).ServeHTTP(recorder, request)
			assertAuditExportCreationReceipt(t, recorder, id.String())
			if len(svc.inputs) != 1 || svc.inputs[0].JobType != domain.SyncJobAuditExport ||
				svc.inputs[0].IdempotencyKey != wantKey || svc.inputs[0].AccountID != (domain.ID{}) {
				t.Fatal("creation must delegate the original audit-export command and key once")
			}
			if !slices.Equal(svc.tenants, []string{"tenant-a"}) || !slices.Equal(checker.seen, []string{domain.PermAuditExport}) {
				t.Fatal("creation must retain the authenticated tenant and existing export permission")
			}
		})
	}
}

func assertAuditExportCreationReceipt(t *testing.T, recorder *httptest.ResponseRecorder, id string) {
	t.Helper()
	if recorder.Code != http.StatusCreated {
		t.Fatalf("audit-export creation HTTP status = %d", recorder.Code)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope.Data
	if len(data) != 3 || data["job_id"] != id || data["status"] != "pending" ||
		data["download_url"] != "/v1/sync/jobs/"+id+"/export/download" {
		t.Fatal("creation must return its root UUID, status and original download path without ID aliases")
	}
}

func TestAuditExportCreationHTTPDenialAndServiceErrorExposeNoReceipt(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "export_denied", true: "creation_error"}[allowed], func(t *testing.T) {
			svc := &auditExportCreationStub{err: apperr.Internal("sync.create_failed", "Failed to create sync job")}
			checker := &anchorPermissionChecker{allowed: map[string]bool{domain.PermAuditExport: allowed}}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/audit/export", strings.NewReader(`{}`))
			request.Header.Set("Idempotency-Key", "original-key")
			auditExportCreationRouter(svc, checker).ServeHTTP(recorder, request)
			wantStatus, wantCalls := http.StatusForbidden, 0
			if allowed {
				wantStatus, wantCalls = http.StatusInternalServerError, 1
			}
			var envelope map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != wantStatus || len(svc.inputs) != wantCalls || envelope["data"] != nil || envelope["error"] == nil {
				t.Fatal("creation must propagate denial/service uncertainty without a success receipt")
			}
		})
	}
}

func TestOpenAPIAuditExportCreationRequiresRootJobUUID(t *testing.T) {
	spec := loadOpenAPISpec(t)
	var response struct {
		Content map[string]struct {
			Schema auditContractSchema `yaml:"schema"`
		} `yaml:"content"`
	}
	decodeAuditContract(t, spec.Paths["/audit/export"]["post"].Responses["201"], &response)
	if response.Content["application/json"].Schema.Ref != "#/components/schemas/AuditExportEnvelope" {
		t.Fatal("audit-export creation must document its actual HTTP 201 receipt envelope")
	}
	var envelope auditContractSchema
	decodeAuditContract(t, spec.Components.Schemas["AuditExportEnvelope"], &envelope)
	data := envelope.Properties["data"]
	id := data.Properties["job_id"]
	if envelope.Type != "object" || !slices.Contains(envelope.Required, "data") || data.Type != "object" ||
		!slices.Contains(data.Required, "job_id") || id.Type != "string" || id.Format != "uuid" {
		t.Fatal("audit-export creation must require a root job UUID, not an ID alias")
	}
}
