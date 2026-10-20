package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/service"
)

type operationScopeContractStub struct{ operationActionContractStub }

func (s *operationScopeContractStub) Create(context.Context, service.OperationInput) (*domain.Operation, error) {
	return s.value, nil
}

func (s *operationScopeContractStub) List(context.Context, domain.Page) ([]*domain.Operation, string, error) {
	return []*domain.Operation{s.value}, "", nil
}

func TestOperationHTTPExposesOnlyExplicitApprovedStepScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	id := uuid.MustParse("01800000-0000-7000-8000-000000000003")
	for _, action := range []string{"get", "list", "create", "run", "reconcile"} {
		t.Run(action, func(t *testing.T) {
			operation := &domain.Operation{ID: id, Status: domain.OperationRequested, Steps: []*domain.OperationStep{{
				StepKey: "vault", Source: "aero-vault", Action: "summary", CompensationAction: "erase",
				RequestPayload: map[string]any{"datasets": []any{"aero-vault.usage", "aero-vault.buckets"},
					"credentials": "SYNTHETIC-NOT-FOR-RESPONSE", "source_body": map[string]any{"text": "PRIVATE-SOURCE-CONTENT"}},
			}}}
			stub := &operationScopeContractStub{operationActionContractStub{value: operation}}
			engine := gin.New()
			httpapi.NewOperationHandler(stub).RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
			method, path, body := operationScopeRequest(action, id.String())
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
			if recorder.Code != http.StatusOK && recorder.Code != http.StatusCreated {
				t.Fatalf("status = %d", recorder.Code)
			}
			data := operationScopeData(t, recorder, action)
			steps := data["steps"].([]any)
			step := steps[0].(map[string]any)
			want := map[string]any{"status": "explicit", "datasets": []any{"aero-vault.usage", "aero-vault.buckets"}}
			if !reflect.DeepEqual(step["dataset_scope"], want) {
				t.Fatalf("step dataset_scope = %#v; want bounded, explicit approved metadata", step["dataset_scope"])
			}
			if _, leaked := step["request_payload"]; leaked || strings.Contains(recorder.Body.String(), "SYNTHETIC-NOT-FOR-RESPONSE") ||
				strings.Contains(recorder.Body.String(), "PRIVATE-SOURCE-CONTENT") {
				t.Fatal("step scope must not expose arbitrary request payload or source content")
			}
			if operation.Steps[0].RequestPayload["credentials"] != "SYNTHETIC-NOT-FOR-RESPONSE" {
				t.Fatal("read projection changed durable execution input")
			}
		})
	}
}

func TestOpenAPIOperationStepScopeContract(t *testing.T) {
	spec := loadOpenAPISpec(t)
	var envelope, step auditContractSchema
	decodeAuditContract(t, spec.Components.Schemas["OperationEnvelope"], &envelope)
	decodeAuditContract(t, spec.Components.Schemas["OperationStepView"], &step)
	steps := envelope.Properties["data"].Properties["steps"]
	if !slices.Contains(envelope.Properties["data"].Required, "steps") || steps.Items == nil ||
		steps.Items.Ref != "#/components/schemas/OperationStepView" ||
		step.Properties["dataset_scope"].Ref != "#/components/schemas/OperationStepScope" {
		t.Fatal("actual HTTP envelopes must document the per-step scope view")
	}
	if _, exposed := step.Properties["request_payload"]; exposed || !slices.Contains(step.Required, "dataset_scope") {
		t.Fatal("only safe scope metadata, never arbitrary step request payload")
	}
	var scope struct {
		Required   []string `yaml:"required"`
		Properties map[string]struct {
			Type        string   `yaml:"type"`
			MaxItems    int      `yaml:"maxItems"`
			UniqueItems bool     `yaml:"uniqueItems"`
			Enum        []string `yaml:"enum"`
			Items       struct {
				Enum []string `yaml:"enum"`
			} `yaml:"items"`
		} `yaml:"properties"`
	}
	decodeAuditContract(t, spec.Components.Schemas["OperationStepScope"], &scope)
	datasets := scope.Properties["datasets"]
	if !slices.Contains(scope.Required, "status") || !slices.Contains(scope.Required, "datasets") ||
		!slices.Equal(scope.Properties["status"].Enum, []string{"explicit", "missing", "invalid"}) ||
		datasets.Type != "array" || datasets.MaxItems != 11 || !datasets.UniqueItems {
		t.Fatal("scope metadata must be explicit, bounded and unique")
	}
	var names []string
	for _, descriptor := range domain.AccountDatasetRegistry() {
		names = append(names, string(descriptor.Name))
	}
	if !slices.Equal(datasets.Items.Enum, names) {
		t.Fatal("HTTP scope allow-list drifted from domain registry")
	}
}

func operationScopeRequest(action, id string) (method, path, body string) {
	method, path = http.MethodGet, "/v1/operations/"+id
	switch action {
	case "list":
		path = "/v1/operations"
	case "create":
		method, path, body = http.MethodPost, "/v1/operations", `{"operation_type":"sync","steps":[]}`
	case "run", "reconcile":
		method, path = http.MethodPost, path+"/"+action
	}
	return method, path, body
}

func operationScopeData(t *testing.T, recorder *httptest.ResponseRecorder, action string) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if action == "list" {
		return envelope.Data["operations"].([]any)[0].(map[string]any)
	}
	return envelope.Data
}

func TestOperationHTTPDoesNotInventMissingOrInvalidStepScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, item := range []struct {
		name, status string
		payload      map[string]any
	}{
		{"missing", "missing", nil},
		{"null", "invalid", map[string]any{"datasets": nil}},
		{"empty", "invalid", map[string]any{"datasets": []any{}}},
		{"wrong source", "invalid", map[string]any{"datasets": []any{"aero-im.workspaces"}}},
		{"unregistered", "invalid", map[string]any{"datasets": []any{"aero-vault.private"}}},
		{"mixed type", "invalid", map[string]any{"datasets": []any{"aero-vault.usage", 1}}},
		{"duplicate", "invalid", map[string]any{"datasets": []any{"aero-vault.usage", "aero-vault.usage"}}},
	} {
		t.Run(item.name, func(t *testing.T) {
			operation := &domain.Operation{Status: domain.OperationRequested,
				RequestPayload: map[string]any{"datasets": []any{"aero-vault.usage"}},
				Steps:          []*domain.OperationStep{{Source: "aero-vault", Action: "summary", RequestPayload: item.payload}}}
			stub := &operationScopeContractStub{operationActionContractStub{value: operation}}
			engine := gin.New()
			httpapi.NewOperationHandler(stub).RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/operations/01800000-0000-7000-8000-000000000003", nil))
			step := operationScopeData(t, recorder, "get")["steps"].([]any)[0].(map[string]any)
			want := map[string]any{"status": item.status, "datasets": []any{}}
			if !reflect.DeepEqual(step["dataset_scope"], want) {
				t.Fatalf("scope = %#v, want %#v (no top-level inference or read-time expansion)", step["dataset_scope"], want)
			}
		})
	}
}
