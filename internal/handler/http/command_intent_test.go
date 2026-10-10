package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"go.uber.org/zap"
)

type intentHTTPValidator struct{ routerClaimsValidator }

func (intentHTTPValidator) ValidateControlPlaneIdentity(context.Context, string) (*domain.TokenIdentity, error) {
	return &domain.TokenIdentity{Verified: true, UID: "u1", Issuer: "snaplink", Subject: "u1", TenantID: "tenant-1", ClientID: "console"}, nil
}

type intentHTTPService struct {
	creates, executions int
	key                 string
}

func (f *intentHTTPService) Create(ctx context.Context, input service.CommandIntentInput) (*domain.CommandIntent, error) {
	f.creates++
	f.key = input.IdempotencyKey
	identity, _ := service.IdentityFrom(ctx)
	if identity == nil || !identity.Verified {
		panic("unverified command context")
	}
	return &domain.CommandIntent{ID: idgen.NewID(), Version: 1, Status: "prepared", Principal: domain.CommandPrincipal{
		TenantID: identity.TenantID, ActorUID: identity.UID, Subject: identity.Subject, Issuer: identity.Issuer, ClientID: identity.ClientID}}, nil
}
func (f *intentHTTPService) Get(context.Context, domain.ID) (*domain.CommandIntent, error) {
	return nil, nil
}
func (f *intentHTTPService) Execute(context.Context, domain.ID, int64, string) (*domain.CommandIntent, error) {
	f.executions++
	return nil, nil
}

func TestCommandIntentHTTPClaimsBoundsAndStrictExecutionEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		claims     bool
		want       int
	}{
		{"UID-only", `{"plan":{"kind":"sync","datasets":["aero-vault.usage"]}}`, false, 401},
		{"verified preview", `{"plan":{"kind":"sync","datasets":["aero-vault.usage"]}}`, true, 201},
		{"tenant override", `{"tenant_id":"foreign","plan":{"kind":"sync","datasets":[]}}`, true, 400},
		{"trailing JSON", `{"plan":{}} {}`, true, 400},
		{"oversized", `{"plan":{"request_payload":{"note":"` + strings.Repeat("a", 256*1024) + `"}}}`, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &intentHTTPService{}
			var validator interface {
				ValidateToken(context.Context, string) (string, error)
			} = okValidator{}
			if tc.claims {
				validator = intentHTTPValidator{}
			}
			router := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, validator, httpapi.NewHealthHandler(), httpapi.NewCommandIntentHandler(svc))
			req := httptest.NewRequest(http.MethodPost, "/v1/command-intents", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer token")
			req.Header.Set("Idempotency-Key", "stable-preview")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.want != 201 {
				if svc.creates != 0 || svc.executions != 0 {
					t.Fatal("invalid input reached service")
				}
				return
			}
			var body struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Data["tenant_id"] != "tenant-1" || body.Data["client_id"] != "console" || body.Data["receipt_id"] != nil ||
				response.Header().Get("Cache-Control") != "no-store" || svc.key != "stable-preview" || svc.executions != 0 {
				t.Fatal("preview leaked a receipt, executed or lost caller binding")
			}
			req = httptest.NewRequest(http.MethodPost, "/v1/command-intents/"+idgen.NewID().String()+"/execute", strings.NewReader(`{"version":1,"confirmation_digest":"digest","plan":{}}`))
			req.Header.Set("Authorization", "Bearer token")
			response = httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != 400 || svc.executions != 0 {
				t.Fatal("client could override server execution plan")
			}
		})
	}
}
