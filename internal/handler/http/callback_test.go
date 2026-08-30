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
	"github.com/aero/aero-id/internal/idempotency"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type callbackServiceFake struct {
	input    service.CallbackInput
	register int
	replays  int
}

type callbackIdempotencyStore struct {
	records map[string]*idempotency.Record
}

func newCallbackIdempotencyStore() *callbackIdempotencyStore {
	return &callbackIdempotencyStore{records: make(map[string]*idempotency.Record)}
}

func (s *callbackIdempotencyStore) key(uid, endpoint, value string) string {
	return uid + "|" + endpoint + "|" + value
}

func (s *callbackIdempotencyStore) Reserve(_ context.Context, uid, endpoint, key string) (*idempotency.Record, bool, error) {
	if record, ok := s.records[s.key(uid, endpoint, key)]; ok {
		return record, false, nil
	}
	s.records[s.key(uid, endpoint, key)] = &idempotency.Record{}
	return nil, true, nil
}

func (s *callbackIdempotencyStore) Complete(_ context.Context, uid, endpoint, key string,
	record idempotency.Record, _ time.Duration) error {
	record.Completed = true
	record.Body = append([]byte(nil), record.Body...)
	s.records[s.key(uid, endpoint, key)] = &record
	return nil
}

func (s *callbackIdempotencyStore) Release(_ context.Context, uid, endpoint, key string) error {
	delete(s.records, s.key(uid, endpoint, key))
	return nil
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

func (f *callbackServiceFake) ReplayDelivery(context.Context, domain.ID) error {
	f.replays++
	return nil
}

func (*callbackServiceFake) Start(context.Context, time.Duration) func() { return func() {} }

func TestCallbackReplayRequiresIdempotencyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &callbackServiceFake{}
	engine := gin.New()
	httpapi.NewCallbackHandler(fake, nil).RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})

	request := httptest.NewRequest(http.MethodPost, "/v1/admin/callback-subscriptions/deliveries/"+idgen.NewID().String()+"/replay", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || fake.replays != 0 {
		t.Fatalf("status/replays = %d/%d, want 400/0; body=%s", response.Code, fake.replays, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("callback.replay_idempotency_key_required")) {
		t.Fatalf("body = %s, want idempotency-key error", response.Body.String())
	}
}

func TestCallbackReplayRetriesStoredResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &callbackServiceFake{}
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("actor_uid", "u-1"); c.Next() })
	engine.Use(middleware.Idempotency(newCallbackIdempotencyStore(), time.Hour, nil))
	httpapi.NewCallbackHandler(fake, nil).RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
	path := "/v1/admin/callback-subscriptions/deliveries/" + idgen.NewID().String() + "/replay"

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, path, nil)
	firstReq.Header.Set("Idempotency-Key", "replay-command-1")
	engine.ServeHTTP(first, firstReq)
	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, path, nil)
	secondReq.Header.Set("Idempotency-Key", "replay-command-1")
	engine.ServeHTTP(second, secondReq)

	if first.Code != http.StatusOK || second.Code != http.StatusOK || fake.replays != 1 {
		t.Fatalf("status/status/replays = %d/%d/%d, want 200/200/1", first.Code, second.Code, fake.replays)
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("retry did not replay the stored response")
	}
}

func TestCallbackReplayKeyIsScopedToConcreteDelivery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &callbackServiceFake{}
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("actor_uid", "u-1"); c.Next() })
	engine.Use(middleware.Idempotency(newCallbackIdempotencyStore(), time.Hour, nil))
	httpapi.NewCallbackHandler(fake, nil).RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
	key := "replay-command-1"
	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, "/v1/admin/callback-subscriptions/deliveries/"+idgen.NewID().String()+"/replay", nil)
	firstReq.Header.Set("Idempotency-Key", key)
	engine.ServeHTTP(first, firstReq)
	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, "/v1/admin/callback-subscriptions/deliveries/"+idgen.NewID().String()+"/replay", nil)
	secondReq.Header.Set("Idempotency-Key", key)
	engine.ServeHTTP(second, secondReq)

	if first.Code != http.StatusOK || second.Code != http.StatusOK || fake.replays != 2 {
		t.Fatalf("status/status/replays = %d/%d/%d, want 200/200/2", first.Code, second.Code, fake.replays)
	}
	if second.Header().Get("Idempotent-Replayed") == "true" {
		t.Fatal("same key for a different delivery must not replay delivery one")
	}
}

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
