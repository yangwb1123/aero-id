package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

func TestOpenAPIAuditManifestRequiresRootTarget(t *testing.T) {
	spec := loadOpenAPISpec(t)
	var response struct {
		Content map[string]struct {
			Schema auditContractSchema `yaml:"schema"`
		} `yaml:"content"`
	}
	decodeAuditContract(t, spec.Paths["/admin/audit/anchor/manifest/{source}/{partition}"]["get"].Responses["200"], &response)
	if response.Content["application/json"].Schema.Ref != "#/components/schemas/AnchorManifestEnvelope" {
		t.Fatal("Manifest response must use its own target-bearing data envelope")
	}
	var envelope auditContractSchema
	decodeAuditContract(t, spec.Components.Schemas["AnchorManifestEnvelope"], &envelope)
	data := envelope.Properties["data"]
	if envelope.Type != "object" || !slices.Contains(envelope.Required, "data") || data.Type != "object" {
		t.Fatal("Manifest must require a data object")
	}
	for _, field := range []string{"source", "partition"} {
		if !slices.Contains(data.Required, field) || data.Properties[field].Type != "string" {
			t.Errorf("Manifest must require root string field %s", field)
		}
	}
}

type manifestContractService struct {
	service.AuditAnchorService
	seen [][2]string
	err  error
}

func (s *manifestContractService) AnchorManifest(_ context.Context, source, partition string) (map[string]any, error) {
	s.seen = append(s.seen, [2]string{source, partition})
	if s.err != nil {
		return nil, s.err
	}
	return map[string]any{"source": source, "partition": partition, "chain_sequence": 7,
		"anchor_hash": "original-anchor-hash"}, nil
}

func manifestContractRouter(svc service.AuditAnchorService, checker *anchorPermissionChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("actor_uid", "operator-1")
		c.Next()
	})
	httpapi.NewAuditLedgerHandlerWithExtensions(nil, nil, svc, nil, checker).
		RegisterRoutes(httpapi.Routes{Auth: engine.Group("/v1")})
	return engine
}

func TestAuditManifestHTTPReturnsExactRootTarget(t *testing.T) {
	for _, target := range [][2]string{{"aero-id", "tenant:a:system"}, {"Aero-ID", "tenant:b:system"}} {
		t.Run(target[0]+"/"+target[1], func(t *testing.T) {
			svc := &manifestContractService{}
			checker := &anchorPermissionChecker{allowed: map[string]bool{domain.PermAuditRead: true}}
			engine := manifestContractRouter(svc, checker)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
				"/v1/admin/audit/anchor/manifest/"+target[0]+"/"+target[1], nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("Manifest status = %d", recorder.Code)
			}
			var envelope struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Data["source"] != target[0] || envelope.Data["partition"] != target[1] ||
				envelope.Data["chain_sequence"] != float64(7) || envelope.Data["anchor_hash"] != "original-anchor-hash" {
				t.Fatal("Manifest response must preserve the exact root target and metadata")
			}
			if len(svc.seen) != 1 || svc.seen[0] != target || !slices.Equal(checker.seen, []string{domain.PermAuditRead}) {
				t.Fatal("Manifest must check read permission and forward the original target exactly once")
			}
		})
	}
}

func TestAuditManifestHTTPDenialAndAuditFailureExposeNoEvidence(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "read_denied", true: "audit_unavailable"}[allowed], func(t *testing.T) {
			svc := &manifestContractService{err: apperr.Unavailable("audit.ingest_failed", "Audit unavailable")}
			checker := &anchorPermissionChecker{allowed: map[string]bool{domain.PermAuditRead: allowed}}
			engine := manifestContractRouter(svc, checker)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
				"/v1/admin/audit/anchor/manifest/aero-id/tenant:a:system", nil))
			wantStatus, wantCalls := http.StatusForbidden, 0
			if allowed {
				wantStatus, wantCalls = http.StatusServiceUnavailable, 1
			}
			var envelope map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != wantStatus || len(svc.seen) != wantCalls || envelope["data"] != nil || envelope["error"] == nil {
				t.Fatal("Manifest must propagate denial/audit failure without returning evidence")
			}
		})
	}
}
