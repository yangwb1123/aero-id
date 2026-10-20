package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/service"
)

type accountReferenceContractService struct {
	service.AccountService
	empty bool
	page  domain.Page
}

func (s *accountReferenceContractService) ListSources(_ context.Context, page domain.Page) (*service.AccountSourceList, error) {
	s.page = page
	rows := []*domain.AccountSource{}
	if !s.empty {
		now := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
		rows = append(rows, &domain.AccountSource{ID: uuid.MustParse("01800000-0000-7000-8000-000000000011"),
			Source: "aero-vault", SourceAccountID: "opaque-account", ScopeType: "tenant", ScopeID: "same-scope-text",
			SourceRegion: "local", Status: "unavailable", Version: 7, GeneratedAt: &now, LastSyncedAt: &now})
	}
	return &service.AccountSourceList{Sources: rows, NextCursor: "original-cursor"}, nil
}

func (s *accountReferenceContractService) ListMemberships(_ context.Context, page domain.Page) (*service.AccountMembershipList, error) {
	s.page = page
	rows := []*domain.AccountMembership{}
	if !s.empty {
		rows = append(rows, &domain.AccountMembership{ID: uuid.MustParse("01800000-0000-7000-8000-000000000012"),
			Source: "aero-im", SourceMemberID: "opaque-member", ScopeType: "workspace", ScopeID: "same-scope-text",
			SourceRegion: "local", Role: "owner", Status: "active", Version: 9})
	}
	return &service.AccountMembershipList{Memberships: rows, NextCursor: "original-cursor"}, nil
}

func TestAccountReferenceHTTPPreservesNativeScopeAndMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, family := range []string{"sources", "memberships"} {
		for _, empty := range []bool{true, false} {
			t.Run(family+map[bool]string{true: "/empty", false: "/row"}[empty], func(t *testing.T) {
				svc := &accountReferenceContractService{empty: empty}
				engine := gin.New()
				httpapi.NewAccountHandler(svc, nil).RegisterRoutes(httpapi.Routes{ControlPlane: engine.Group("/v1")})
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/me/"+family+"?cursor=opaque-input&limit=50&tenant_id=untrusted", nil))
				if recorder.Code != http.StatusOK || svc.page.Cursor != "opaque-input" || svc.page.Limit != 50 {
					t.Fatal("original list contract or paging changed")
				}
				assertReferenceHTTPPage(t, recorder, family, empty)
			})
		}
	}
}

func assertReferenceHTTPPage(t *testing.T, recorder *httptest.ResponseRecorder, family string, empty bool) {
	t.Helper()
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(envelope.Data[family], &rows); err != nil {
		t.Fatal(err)
	}
	if rows == nil || len(rows) != map[bool]int{true: 0, false: 1}[empty] {
		t.Fatal("list must be an explicit array")
	}
	if empty {
		return
	}
	row := rows[0]
	if row["scope_id"] != "same-scope-text" || row["source_region"] != "local" {
		t.Fatal("native scope or region changed")
	}
	if family == "sources" {
		if row["scope_type"] != "tenant" || row["source_account_id"] != "opaque-account" || row["version"] != float64(7) || row["generated_at"] != "2026-10-16T00:00:00Z" {
			t.Fatal("source field mapping changed")
		}
		return
	}
	if row["scope_type"] != "workspace" || row["source_member_id"] != "opaque-member" || row["role"] != "owner" || row["version"] != float64(9) {
		t.Fatal("membership field mapping changed")
	}
	if _, exists := row["generated_at"]; exists {
		t.Fatal("membership has no generated-at contract")
	}
}

func TestOpenAPIAccountReferencePagesDocumentExactNativeFields(t *testing.T) {
	spec := loadOpenAPISpec(t)
	for _, name := range []string{"SourceReference", "MembershipReference"} {
		encoded, err := yaml.Marshal(spec.Components.Schemas[name])
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Required   []string `yaml:"required"`
			Properties map[string]struct {
				Type string `yaml:"type"`
			} `yaml:"properties"`
		}
		if err := yaml.Unmarshal(encoded, &schema); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"id", "source", "source_region", "status", "version"} {
			if !slices.Contains(schema.Required, field) {
				t.Errorf("%s missing required %s", name, field)
			}
		}
		for _, field := range []string{"scope_type", "scope_id", "last_synced_at"} {
			if schema.Properties[field].Type != "string" {
				t.Errorf("%s missing string %s", name, field)
			}
		}
	}
}
