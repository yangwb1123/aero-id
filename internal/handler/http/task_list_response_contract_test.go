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

type operationListContractStub struct {
	service.OperationService
	items []*domain.Operation
	next  string
	pages []domain.Page
}

func (s *operationListContractStub) List(_ context.Context, page domain.Page) ([]*domain.Operation, string, error) {
	s.pages = append(s.pages, page)
	return s.items, s.next, nil
}

type syncListContractStub struct {
	service.SyncJobService
	items []*domain.SyncJob
	next  string
	pages []domain.Page
}

func (s *syncListContractStub) List(_ context.Context, page domain.Page) ([]*domain.SyncJob, string, error) {
	s.pages = append(s.pages, page)
	return s.items, s.next, nil
}

func TestTaskListHTTPReturnsObjectArrayAndStringCursor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, family := range []string{"jobs", "operations"} {
		for _, shape := range []string{"nil", "empty", "rows"} {
			t.Run(family+"/"+shape, func(t *testing.T) {
				assertTaskListHTTPContract(t, family, shape)
			})
		}
	}
}

func assertTaskListHTTPContract(t *testing.T, family, shape string) {
	t.Helper()
	id := uuid.MustParse("01800000-0000-7000-8000-000000000003")
	operations, jobs := &operationListContractStub{}, &syncListContractStub{}
	count := 0
	if shape == "empty" {
		operations.items, jobs.items = []*domain.Operation{}, []*domain.SyncJob{}
	} else if shape == "rows" {
		count = 1
		operations.items = []*domain.Operation{{ID: id, Status: domain.OperationUnknown}}
		jobs.items = []*domain.SyncJob{{ID: id, Status: domain.SyncJobUnknown}}
		operations.next, jobs.next = "opaque:next/1", "opaque:next/1"
	}
	engine := gin.New()
	routes := httpapi.Routes{Auth: engine.Group("/v1")}
	httpapi.NewOperationHandler(operations).RegisterRoutes(routes)
	httpapi.NewSyncJobHandler(jobs, nil).RegisterRoutes(routes)
	path, field, next := "/v1/operations", "operation_id", operations.next
	if family == "jobs" {
		path, field, next = "/v1/sync/jobs", "job_id", jobs.next
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path+"?cursor=opaque%3Aoriginal%2F1&limit=50", nil))
	assertTaskListPage(t, recorder, family, field, id.String(), count, next)
	pages := append(operations.pages, jobs.pages...)
	if !slices.Equal(pages, []domain.Page{{Cursor: "opaque:original/1", Limit: 50}}) {
		t.Fatalf("list pages = %+v", pages)
	}
}

func assertTaskListPage(t *testing.T, recorder *httptest.ResponseRecorder, family, field, id string, count int, next string) {
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
	items, ok := envelope.Data[family].([]any)
	if !ok || len(items) != count {
		t.Fatalf("%s must be an array of %d rows, got %#v", family, count, envelope.Data[family])
	}
	cursor, ok := envelope.Data["next_cursor"].(string)
	if !ok || cursor != next {
		t.Fatalf("next_cursor = %#v, want string %q", envelope.Data["next_cursor"], next)
	}
	for _, value := range items {
		item, valid := value.(map[string]any)
		if !valid || item[field] != id || item["status"] != "unknown" {
			t.Fatalf("list row must retain its original ID and status: %#v", value)
		}
	}
}

func TestOpenAPITaskListsRequireObjectArrays(t *testing.T) {
	spec := loadOpenAPISpec(t)
	for _, item := range []struct{ schema, field string }{
		{"SyncJobListEnvelope", "jobs"}, {"OperationListEnvelope", "operations"},
	} {
		t.Run(item.schema, func(t *testing.T) {
			var envelope auditContractSchema
			decodeAuditContract(t, spec.Components.Schemas[item.schema], &envelope)
			data := envelope.Properties["data"]
			if envelope.Type != "object" || !slices.Contains(envelope.Required, "data") || data.Type != "object" ||
				!slices.Contains(data.Required, item.field) {
				t.Fatal("task list must require a data object and its family list")
			}
			list, cursor := data.Properties[item.field], data.Properties["next_cursor"]
			if list.Type != "array" || list.Items == nil || list.Items.Type != "object" || cursor.Type != "string" {
				t.Fatalf("task list must describe an object array and string cursor: list=%+v cursor=%+v", list, cursor)
			}
		})
	}
}
