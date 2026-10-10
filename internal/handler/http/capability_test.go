package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type capabilityHandlerService struct{ calls int }

func (f *capabilityHandlerService) GetMeCapabilities(ctx context.Context) (*domain.CapabilityReport, error) {
	f.calls++
	identity, ok := service.IdentityFrom(ctx)
	if !ok {
		panic("capability handler omitted verified context")
	}
	return &domain.CapabilityReport{SchemaVersion: 1, AccountID: idgen.NewID(), TenantID: identity.TenantID,
		ClientID: identity.ClientID, Capabilities: []domain.Capability{}, Datasets: []domain.DatasetCapability{}}, nil
}

func TestCapabilitiesRouteRequiresClaimsAndUsesDataEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claims bool
		token  bool
		want   int
	}{
		{"missing token", true, false, 401}, {"UID-only", false, true, 401}, {"claims", true, true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &capabilityHandlerService{}
			var validator interface {
				ValidateToken(context.Context, string) (string, error)
			} = okValidator{}
			if tc.claims {
				validator = routerClaimsValidator{}
			}
			router := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, validator, httpapi.NewHealthHandler(), httpapi.NewCapabilityHandler(svc))
			req := httptest.NewRequest(http.MethodGet, "/v1/me/capabilities", nil)
			if tc.token {
				req.Header.Set("Authorization", "Bearer token")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			if tc.want != 200 {
				if svc.calls != 0 {
					t.Fatal("failed auth reached service")
				}
				return
			}
			var body struct {
				Data domain.CapabilityReport `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Data.ClientID != "console" || body.Data.TenantID != "tenant-1" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("report = %+v", body.Data)
			}
		})
	}
}
