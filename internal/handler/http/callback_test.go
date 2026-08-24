package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type callbackServiceFake struct {
	input    service.CallbackInput
	register int
}

func (f *callbackServiceFake) Register(_ context.Context, input service.CallbackInput) (*domain.CallbackSubscription, string, error) {
	f.input = input
	f.register++
	return &domain.CallbackSubscription{ID: idgen.NewID(), Endpoint: input.Endpoint,
		EventTypes: input.EventTypes, Enabled: true, Verified: true}, "secret", nil
}

func (*callbackServiceFake) List(context.Context, domain.Page) ([]*domain.CallbackSubscription, string, error) {
	return nil, "", nil
}

func (*callbackServiceFake) Delete(context.Context, domain.ID) error { return nil }

func (*callbackServiceFake) ReplayDelivery(context.Context, domain.ID) error { return nil }

func (*callbackServiceFake) Start(context.Context, time.Duration) func() { return func() {} }

func TestCallbackCreateDecodesPublishedSnakeCaseContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &callbackServiceFake{}
	engine := gin.New()
	httpapi.NewCallbackHandler(fake, nil).RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
	body := []byte(`{"endpoint":"https://callback.example.test/events","tenant_id":"tenant-a","event_types":["operation.completed","export.ready"]}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/callback-subscriptions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fake.input.TenantID != "tenant-a" {
		t.Fatalf("tenant_id = %q, want tenant-a", fake.input.TenantID)
	}
	if len(fake.input.EventTypes) != 2 || fake.input.EventTypes[0] != "operation.completed" || fake.input.EventTypes[1] != "export.ready" {
		t.Fatalf("event_types = %#v", fake.input.EventTypes)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
	var envelope struct {
		Data struct {
			Secret string `json:"secret"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.Secret != "secret" || fake.register != 1 {
		t.Fatalf("secret/register = %q/%d, want secret/1", envelope.Data.Secret, fake.register)
	}
}
