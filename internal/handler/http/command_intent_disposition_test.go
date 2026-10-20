package http_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"go.uber.org/zap"
)

type intentDispositionHTTPService struct {
	intentHTTPService
	intent               *domain.CommandIntent
	lookups, withdrawals int
	version              int64
	digest               string
}

func (s *intentDispositionHTTPService) FindByKey(_ context.Context, key string) (*domain.CommandIntent, error) {
	s.lookups++
	if key != s.intent.IdempotencyKey {
		return nil, apperr.NotFound("command.not_found", "command intent not found")
	}
	return s.intent, nil
}
func (s *intentDispositionHTTPService) Abandon(_ context.Context, _ domain.ID, version int64, digest string) (*domain.CommandIntent, error) {
	s.withdrawals++
	s.version, s.digest = version, digest
	return s.intent, nil
}

func TestCommandIntentHTTPOriginalLookupAndStrictAbandonment(t *testing.T) {
	now := time.Now().UTC()
	svc := &intentDispositionHTTPService{intent: &domain.CommandIntent{ID: idgen.NewID(), SchemaVersion: 1, Version: 2,
		Status: "abandoned", AbandonedAt: &now, IdempotencyKey: "original-preview", Principal: domain.CommandPrincipal{TenantID: "tenant-1", ClientID: "console"}}}
	router := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, intentHTTPValidator{}, httpapi.NewHealthHandler(), httpapi.NewCommandIntentHandler(svc))
	request := func(path, body string) *httptest.ResponseRecorder {
		method := http.MethodPost
		if body == "" {
			method = http.MethodGet
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Idempotency-Key", "original-preview")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	lookup := request("/v1/command-intents/by-key", "")
	var body struct {
		Data struct {
			KeyDigest string         `json:"key_digest"`
			Intent    map[string]any `json:"intent"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lookup.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if lookup.Code != 200 || lookup.Header().Get("Cache-Control") != "no-store" ||
		body.Data.KeyDigest != fmt.Sprintf("v1:%x", sha256.Sum256([]byte("original-preview"))) ||
		body.Data.Intent["status"] != "abandoned" || body.Data.Intent["receipt_id"] != nil || strings.Contains(lookup.Body.String(), "original-preview") {
		t.Fatal("original-key lookup lost binding or exposed the key/receipt", lookup.Body.String())
	}
	path := "/v1/command-intents/" + svc.intent.ID.String() + "/abandon"
	for _, invalid := range []string{
		`{"version":1,"confirmation_digest":"digest","tenant_id":"foreign"}`,
		`{"version":1,"confirmation_digest":"digest","plan":{}}`,
		`{"version":1,"confirmation_digest":"digest"} {}`,
		`{"confirmation_digest":"` + strings.Repeat("a", 256*1024) + `"}`,
	} {
		if response := request(path, invalid); response.Code != 400 || svc.withdrawals != 0 {
			t.Fatal("invalid withdrawal reached service", response.Body.String())
		}
	}
	closed := request(path, `{"version":1,"confirmation_digest":"original-digest"}`)
	if closed.Code != 200 || closed.Header().Get("Cache-Control") != "no-store" || svc.version != 1 || svc.digest != "original-digest" || svc.executions != 0 || svc.creates != 0 {
		t.Fatal("withdrawal executed/prepared or changed confirmation", closed.Body.String())
	}
}
