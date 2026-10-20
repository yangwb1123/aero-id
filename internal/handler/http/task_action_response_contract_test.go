package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/service"
)

type taskActionCall struct {
	action string
	id     domain.ID
	worker string
}

type operationActionContractStub struct {
	service.OperationService
	value *domain.Operation
	calls []taskActionCall
}

func (s *operationActionContractStub) Get(_ context.Context, id domain.ID) (*domain.Operation, error) {
	s.calls = append(s.calls, taskActionCall{"get", id, ""})
	return s.value, nil
}

func (s *operationActionContractStub) Run(_ context.Context, id domain.ID, worker string) (*domain.Operation, error) {
	s.calls = append(s.calls, taskActionCall{"run", id, worker})
	return s.value, nil
}

func (s *operationActionContractStub) Reconcile(_ context.Context, id domain.ID, worker string) (*domain.Operation, error) {
	s.calls = append(s.calls, taskActionCall{"reconcile", id, worker})
	return s.value, nil
}

type syncActionContractStub struct {
	service.SyncJobService
	value *domain.SyncJob
	calls []taskActionCall
}

func (s *syncActionContractStub) Get(_ context.Context, id domain.ID) (*domain.SyncJob, error) {
	s.calls = append(s.calls, taskActionCall{"get", id, ""})
	return s.value, nil
}

func (s *syncActionContractStub) Reconcile(_ context.Context, id domain.ID, worker string) (*domain.SyncJob, error) {
	s.calls = append(s.calls, taskActionCall{"reconcile", id, worker})
	return s.value, nil
}

func TestTaskActionHTTPReturnsCanonicalRootReceiptID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, action := range []string{"operation run", "operation reconcile", "sync reconcile"} {
		t.Run(action, func(t *testing.T) {
			id := uuid.MustParse("01800000-0000-7000-8000-000000000003")
			accountID := uuid.MustParse("01800000-0000-7000-8000-000000000002")
			operations := &operationActionContractStub{value: &domain.Operation{
				ID: id, AccountID: accountID, TenantID: "tenant-a", ActorUID: "original-initiator",
				OperationType: "sync", Status: domain.OperationUnknown,
			}}
			jobs := &syncActionContractStub{value: &domain.SyncJob{
				ID: id, AccountID: accountID, TenantID: "tenant-a", ActorUID: "original-initiator",
				JobType: domain.SyncJobExport, Status: domain.SyncJobUnknown,
			}}
			engine := gin.New()
			routes := httpapi.Routes{Auth: engine.Group("/v1")}
			httpapi.NewOperationHandler(operations).RegisterRoutes(routes)
			httpapi.NewSyncJobHandler(jobs, nil).RegisterRoutes(routes)
			path, field, other, method := "/v1/operations/", "operation_id", "job_id", "reconcile"
			if action == "operation run" {
				method = "run"
			} else if action == "sync reconcile" {
				path, field, other = "/v1/sync/jobs/", "job_id", "operation_id"
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, path+id.String()+"/"+method, nil)
			request.Header.Set("X-Worker-ID", "original-worker")
			engine.ServeHTTP(recorder, request)
			assertTaskActionReceipt(t, recorder, field, other, id.String(), accountID.String())
			calls := append(operations.calls, jobs.calls...)
			if len(calls) != 1 || calls[0] != (taskActionCall{method, id, "original-worker"}) {
				t.Fatalf("action calls = %+v", calls)
			}
		})
	}
}

func assertTaskActionReceipt(t *testing.T, recorder *httptest.ResponseRecorder, field, other, id, accountID string) {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d", recorder.Code)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope.Data
	if data[field] != id || data["account_id"] != accountID || data["status"] != "unknown" ||
		data["actor_uid"] != "original-initiator" || data["tenant_id"] != "tenant-a" {
		t.Fatal("action receipt lost canonical root identity or original scope")
	}
	for _, alias := range []string{"id", other} {
		if _, exists := data[alias]; exists {
			t.Errorf("action receipt unexpectedly returned root %s alias", alias)
		}
	}
}

func TestOpenAPITaskEnvelopesRequireCanonicalRootReceiptID(t *testing.T) {
	spec := loadOpenAPISpec(t)
	for _, item := range []struct{ schema, field string }{
		{"SyncJobEnvelope", "job_id"}, {"OperationEnvelope", "operation_id"},
	} {
		t.Run(item.schema, func(t *testing.T) {
			var envelope auditContractSchema
			decodeAuditContract(t, spec.Components.Schemas[item.schema], &envelope)
			data := envelope.Properties["data"]
			if !slices.Contains(envelope.Required, "data") || data.Type != "object" ||
				!slices.Contains(data.Required, item.field) {
				t.Fatal("task envelope must require data and its canonical root ID")
			}
			id := data.Properties[item.field]
			if id.Type != "string" || id.Format != "uuid" {
				t.Errorf("canonical task ID shape = %+v", id)
			}
		})
	}
}
