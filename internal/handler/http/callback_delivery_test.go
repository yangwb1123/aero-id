package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type callbackDeliveryHTTPFake struct {
	callbackServiceFake
	item     *domain.CallbackDeliveryMetadata
	filter   domain.CallbackDeliveryFilter
	sub      domain.ID
	delivery domain.ID
	fail     error
	calls    int
}

func (f *callbackDeliveryHTTPFake) ListDeliveries(_ context.Context, id domain.ID, filter domain.CallbackDeliveryFilter) ([]*domain.CallbackDeliveryMetadata, string, error) {
	f.calls++
	f.sub, f.filter = id, filter
	return []*domain.CallbackDeliveryMetadata{f.item}, f.item.ID.String(), f.fail
}
func (f *callbackDeliveryHTTPFake) GetDeliveryMetadata(_ context.Context, sub, id domain.ID) (*domain.CallbackDeliveryMetadata, error) {
	f.calls++
	f.sub, f.delivery = sub, id
	return f.item, f.fail
}
func TestCallbackDeliveryHTTPRoutesRenderOnlyMetadataNoStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()
	fake := &callbackDeliveryHTTPFake{item: &domain.CallbackDeliveryMetadata{ID: idgen.NewID(), SubscriptionID: idgen.NewID(),
		EventID: idgen.NewID(), TenantID: "tenant-a", Action: "account.provisioned", Status: "pending",
		CreatedAt: now, UpdatedAt: now, NextAttemptAt: now}}
	engine := gin.New()
	httpapi.NewCallbackHandler(fake, nil).RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
	path := "/v1/admin/callback-subscriptions/" + fake.item.SubscriptionID.String() + "/deliveries"
	request := httptest.NewRequest(http.MethodGet, path+"?status=pending&limit=7&cursor="+fake.item.ID.String()+"&tenant_id=tenant-b", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || fake.sub != fake.item.SubscriptionID || fake.filter.Status != "pending" || fake.filter.Page.Limit != 7 {
		t.Fatalf("list=%d %s filter=%+v", response.Code, response.Body, fake.filter)
	}
	for _, forbidden := range []string{"payload", "response_body", "secret", "endpoint", "lease_owner", "tenant-b"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatal("unsafe field emitted", forbidden)
		}
	}
	var body struct {
		Data struct {
			Deliveries []map[string]any `json:"deliveries"`
			Next       string           `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Data.Deliveries) != 1 || body.Data.Next != fake.item.ID.String() {
		t.Fatal("list envelope differs", err)
	}
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+"/"+fake.item.ID.String(), nil))
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || fake.delivery != fake.item.ID || !strings.Contains(response.Body.String(), `"delivery":`) {
		t.Fatal("detail differs", response.Code, response.Body)
	}
	before := fake.calls
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+"/not-an-id", nil))
	if response.Code != 400 || fake.calls != before {
		t.Fatal("invalid ID reached service")
	}
	fake.fail = apperr.NotFound("callback.delivery_not_found", "callback subscription or delivery not found")
	response = httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+"/"+fake.item.ID.String(), nil))
	if response.Code != 404 || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "event_id") {
		t.Fatal("failed read leaked metadata")
	}
}

func TestCallbackDeliveryHTTPUIDOnlyCannotReachNewReadSurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithActor(c.Request.Context(), "owner"))
		c.Next()
	})
	httpapi.NewCallbackHandler(service.NewCallbackService(nil, service.CallbackConfig{}), nil).RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
	sub, delivery := idgen.NewID().String(), idgen.NewID().String()
	for _, path := range []string{"/v1/admin/callback-subscriptions/" + sub + "/deliveries", "/v1/admin/callback-subscriptions/" + sub + "/deliveries/" + delivery} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != 401 || !strings.Contains(response.Body.String(), "auth.claims_required") {
			t.Fatal("UID-only read admitted", response.Code, response.Body)
		}
	}
}
