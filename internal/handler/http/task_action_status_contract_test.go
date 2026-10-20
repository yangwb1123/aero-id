package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
)

func taskFamilyStatuses() map[string][]string {
	return map[string][]string{
		"SyncJobEnvelope": {
			string(domain.SyncJobPending), string(domain.SyncJobRunning), string(domain.SyncJobCompleted),
			string(domain.SyncJobPartiallyComplete), string(domain.SyncJobFailed), string(domain.SyncJobUnknown),
		},
		"OperationEnvelope": {
			string(domain.OperationRequested), string(domain.OperationRunning), string(domain.OperationWaiting),
			string(domain.OperationUnknown), string(domain.OperationPartiallyCompleted), string(domain.OperationCompleted),
			string(domain.OperationFailed), string(domain.OperationCompensating),
		},
	}
}

func TestTaskActionHTTPPreservesFamilyStatusStrings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, action := range []string{"operation run", "operation reconcile", "sync reconcile"} {
		schema := "OperationEnvelope"
		if action == "sync reconcile" {
			schema = "SyncJobEnvelope"
		}
		for _, status := range taskFamilyStatuses()[schema] {
			t.Run(action+"/"+status, func(t *testing.T) {
				assertTaskActionStatus(t, action, status)
			})
		}
	}
}

func TestTaskDetailHTTPPreservesFamilyStatusStrings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, item := range []struct{ action, schema string }{
		{"operation get", "OperationEnvelope"}, {"sync get", "SyncJobEnvelope"},
	} {
		for _, status := range taskFamilyStatuses()[item.schema] {
			t.Run(item.action+"/"+status, func(t *testing.T) {
				assertTaskActionStatus(t, item.action, status)
			})
		}
	}
}

func assertTaskActionStatus(t *testing.T, action, status string) {
	t.Helper()
	id := uuid.MustParse("01800000-0000-7000-8000-000000000003")
	operations := &operationActionContractStub{value: &domain.Operation{ID: id, Status: domain.OperationStatus(status)}}
	jobs := &syncActionContractStub{value: &domain.SyncJob{ID: id, Status: domain.SyncJobStatus(status)}}
	engine := gin.New()
	routes := httpapi.Routes{Auth: engine.Group("/v1")}
	httpapi.NewOperationHandler(operations).RegisterRoutes(routes)
	httpapi.NewSyncJobHandler(jobs, nil).RegisterRoutes(routes)
	path, method := "/v1/operations/", "reconcile"
	if action == "operation run" {
		method = "run"
	} else if action == "sync reconcile" || action == "sync get" {
		path = "/v1/sync/jobs/"
	}
	requestMethod, target := http.MethodPost, path+id.String()+"/"+method
	if action == "operation get" || action == "sync get" {
		method, requestMethod, target = "get", http.MethodGet, path+id.String()
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(requestMethod, target, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d", recorder.Code)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	got, ok := envelope.Data["status"].(string)
	if !ok || got != status {
		t.Errorf("root status = %#v, want exact string %q", envelope.Data["status"], status)
	}
	calls := append(operations.calls, jobs.calls...)
	if len(calls) != 1 || calls[0] != (taskActionCall{method, id, ""}) {
		t.Fatalf("task calls = %+v", calls)
	}
}

func TestOpenAPITaskEnvelopesRequireFamilyStatusStrings(t *testing.T) {
	spec := loadOpenAPISpec(t)
	for name, statuses := range taskFamilyStatuses() {
		t.Run(name, func(t *testing.T) {
			var envelope struct {
				Properties map[string]struct {
					Required   []string `yaml:"required"`
					Properties map[string]struct {
						Type string   `yaml:"type"`
						Enum []string `yaml:"enum"`
					} `yaml:"properties"`
				} `yaml:"properties"`
			}
			decodeAuditContract(t, spec.Components.Schemas[name], &envelope)
			data := envelope.Properties["data"]
			status := data.Properties["status"]
			if !slices.Contains(data.Required, "status") || status.Type != "string" || !slices.Equal(status.Enum, statuses) {
				t.Errorf("root status must be required with the exact domain family enum: %+v", status)
			}
		})
	}
}
