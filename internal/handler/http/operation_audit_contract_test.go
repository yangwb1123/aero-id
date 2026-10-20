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
	"gopkg.in/yaml.v3"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/service"
)

type auditContractSchema struct {
	Ref        string                         `yaml:"$ref"`
	Type       string                         `yaml:"type"`
	Format     string                         `yaml:"format"`
	Required   []string                       `yaml:"required"`
	Properties map[string]auditContractSchema `yaml:"properties"`
	Items      *auditContractSchema           `yaml:"items"`
}

func decodeAuditContract(t *testing.T, value, target any) {
	t.Helper()
	data, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAPIOperationAuditEnvelope(t *testing.T) {
	spec := loadOpenAPISpec(t)
	var response struct {
		Content map[string]struct {
			Schema auditContractSchema `yaml:"schema"`
		} `yaml:"content"`
	}
	decodeAuditContract(t, spec.Paths["/audit/operations/{id}"]["get"].Responses["200"], &response)
	if got := response.Content["application/json"].Schema.Ref; got != "#/components/schemas/OperationAuditEnvelope" {
		t.Fatalf("Operation audit response ref = %q, want its own paged fact envelope", got)
	}
	var envelope auditContractSchema
	decodeAuditContract(t, spec.Components.Schemas["OperationAuditEnvelope"], &envelope)
	data := envelope.Properties["data"]
	if envelope.Type != "object" || !slices.Contains(envelope.Required, "data") || data.Type != "object" {
		t.Fatal("Operation audit must require a data object")
	}
	for _, field := range []string{"operation_id", "events"} {
		if !slices.Contains(data.Required, field) {
			t.Errorf("Operation audit data must require %s", field)
		}
	}
	id, events, cursor := data.Properties["operation_id"], data.Properties["events"], data.Properties["next_cursor"]
	if id.Type != "string" || id.Format != "uuid" {
		t.Errorf("Operation audit root ID shape = %+v", id)
	}
	if events.Type != "array" || events.Items == nil || events.Items.Type != "object" {
		t.Errorf("Operation audit events shape = %+v", events)
	}
	if cursor.Type != "string" {
		t.Errorf("Operation audit cursor shape = %+v", cursor)
	}
}

type operationAuditReadStub struct {
	service.AuditLedgerService
	facts []*domain.AuditFact
	next  string
	seen  []domain.AuditFactFilter
}

func (s *operationAuditReadStub) List(_ context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	s.seen = append(s.seen, filter)
	return s.facts, s.next, nil
}

func operationAuditRouter(svc service.AuditLedgerService, checker *anchorPermissionChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("actor_uid", "operator-1")
		c.Next()
	})
	httpapi.NewAuditLedgerHandler(svc, checker).RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
	return engine
}

func TestOperationAuditHTTPResponseMatchesPageContract(t *testing.T) {
	id := uuid.MustParse("01800000-0000-7000-8000-000000000003")
	for _, empty := range []bool{true, false} {
		t.Run(map[bool]string{true: "empty", false: "facts"}[empty], func(t *testing.T) {
			svc := &operationAuditReadStub{}
			if !empty {
				svc.facts = []*domain.AuditFact{{ID: id, OperationID: id, Action: "operation.requested"}}
				svc.next = "opaque:next/1"
			}
			checker := &anchorPermissionChecker{allowed: map[string]bool{domain.PermAuditRead: true}}
			engine := operationAuditRouter(svc, checker)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
				"/v1/audit/operations/"+id.String()+"?cursor=opaque%3Aoriginal%2F1&limit=50", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("HTTP status = %d", recorder.Code)
			}
			assertOperationAuditResponse(t, recorder, id.String(), len(svc.facts), svc.next)
			if len(svc.seen) != 1 || svc.seen[0].OperationID != id ||
				svc.seen[0].Page.Cursor != "opaque:original/1" || svc.seen[0].Page.Limit != 50 {
				t.Fatalf("audit filters = %+v", svc.seen)
			}
			checker.allowed[domain.PermAuditRead] = false
			denied := httptest.NewRecorder()
			engine.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/v1/audit/operations/"+id.String(), nil))
			if denied.Code != http.StatusForbidden || len(svc.seen) != 1 {
				t.Fatalf("denied status/calls = %d/%d", denied.Code, len(svc.seen))
			}
		})
	}
}

func assertOperationAuditResponse(t *testing.T, recorder *httptest.ResponseRecorder, id string, count int, next string) {
	t.Helper()
	var envelope struct {
		Data struct {
			OperationID string           `json:"operation_id"`
			Events      []map[string]any `json:"events"`
			NextCursor  string           `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope.Data
	if data.OperationID != id || data.Events == nil || len(data.Events) != count || data.NextCursor != next {
		t.Fatalf("Operation audit page = %+v", data)
	}
	for _, event := range data.Events {
		if event["operation_id"] != id || event["action"] != "operation.requested" {
			t.Fatalf("Operation audit fact = %+v", event)
		}
	}
}
