package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

const agentMembershipPath = "/internal/v1/agent/membership-check"

type agentMembershipValidator struct{ identity *domain.TokenIdentity }

func (v agentMembershipValidator) ValidateToken(context.Context, string) (string, error) {
	return v.identity.UID, nil
}
func (v agentMembershipValidator) ValidateControlPlaneIdentity(context.Context, string) (*domain.TokenIdentity, error) {
	return v.identity, nil
}

type agentMembershipServiceFake struct {
	result service.AgentMembershipResult
	err    error
	calls  int
}

func (f *agentMembershipServiceFake) Check(_ context.Context, accountID domain.ID) (service.AgentMembershipResult, error) {
	f.calls++
	if f.result.AccountID == "" {
		f.result.AccountID = accountID.String()
	}
	return f.result, f.err
}

func agentMembershipRouter(checker service.AgentMembershipCheckService, validator middleware.TokenValidator) *gin.Engine {
	engine := gin.New()
	handler := NewAgentMembershipHandler(checker)
	handler.RegisterRoutes(Routes{InternalControlPlane: engine.Group("", middleware.ControlPlaneAuth(validator))})
	return engine
}

func TestAgentMembershipRouteUsesControlPlaneAuthAndNoStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accountID := idgen.NewID()
	identity := &domain.TokenIdentity{Verified: true, UID: "machine", Issuer: "issuer", Subject: "machine",
		TenantID: "tenant-a", ClientID: "hub"}
	checker := &agentMembershipServiceFake{result: service.AgentMembershipResult{Eligible: true, TenantID: "tenant-a"}}
	engine := agentMembershipRouter(checker, agentMembershipValidator{identity: identity})
	request := httptest.NewRequest(http.MethodPost, agentMembershipPath,
		strings.NewReader(`{"account_id":"`+accountID.String()+`"}`))
	request.Header.Set("Authorization", "Bearer verified")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}
	var response struct {
		Data service.AgentMembershipResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Data.Eligible || response.Data.TenantID != "tenant-a" || response.Data.AccountID != accountID.String() {
		t.Fatalf("response data=%+v", response.Data)
	}
	if checker.calls != 1 {
		t.Fatalf("service calls=%d", checker.calls)
	}
}

func TestAgentMembershipRouteRejectsMissingAuthAndNonExactRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	identity := &domain.TokenIdentity{Verified: true, UID: "machine", Issuer: "issuer", Subject: "machine",
		TenantID: "tenant-a", ClientID: "hub"}
	checker := &agentMembershipServiceFake{result: service.AgentMembershipResult{Eligible: true}}
	engine := agentMembershipRouter(checker, agentMembershipValidator{identity: identity})
	cases := []struct {
		name, body string
		withToken  bool
	}{
		{name: "missing token", body: `{}`},
		{name: "unknown field", body: `{"account_id":"00000000-0000-0000-0000-000000000001","tenant_id":"other"}`, withToken: true},
		{name: "duplicate field", body: `{"account_id":"00000000-0000-0000-0000-000000000001","account_id":"00000000-0000-0000-0000-000000000002"}`, withToken: true},
		{name: "invalid account", body: `{"account_id":"not-a-uuid"}`, withToken: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, agentMembershipPath, strings.NewReader(test.body))
			if test.withToken {
				request.Header.Set("Authorization", "Bearer verified")
			}
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			if recorder.Code < 400 || recorder.Code >= 500 {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if checker.calls != 0 {
		t.Fatalf("invalid requests reached service %d times", checker.calls)
	}
}

func TestAgentMembershipRouteIsAbsentWhenDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := agentMembershipRouter(nil, agentMembershipValidator{})
	request := httptest.NewRequest(http.MethodPost, agentMembershipPath, strings.NewReader(`{"account_id":"x"}`))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("disabled endpoint status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
