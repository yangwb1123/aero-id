package http_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
)

type realAccountRecorder struct{ identity *domain.TokenIdentity }

func (r *realAccountRecorder) GetMe(ctx context.Context) (*service.AccountView, error) {
	r.identity, _ = service.IdentityFrom(ctx)
	id := idgen.NewID()
	return &service.AccountView{Account: &domain.Account{ID: id, TenantID: "tenant-1", CanonicalUID: "user-1", Status: domain.AccountActive}, Profile: &domain.AccountProfile{AccountID: id}}, nil
}
func (*realAccountRecorder) GetProfile(context.Context) (*domain.AccountProfile, error) {
	return nil, nil
}
func (*realAccountRecorder) UpdateProfile(context.Context, service.UpdateAccountProfileInput) (*domain.AccountProfile, error) {
	return nil, nil
}
func (*realAccountRecorder) ListSources(context.Context, domain.Page) (*service.AccountSourceList, error) {
	return &service.AccountSourceList{}, nil
}
func (*realAccountRecorder) ListMemberships(context.Context, domain.Page) (*service.AccountMembershipList, error) {
	return &service.AccountMembershipList{}, nil
}
func (*realAccountRecorder) UpsertSource(context.Context, *domain.AccountSource) error { return nil }
func (*realAccountRecorder) UpsertMembership(context.Context, *domain.AccountMembership) error {
	return nil
}
func (*realAccountRecorder) GetAccount(context.Context, domain.ID) (*service.AccountView, error) {
	return nil, nil
}
func (*realAccountRecorder) ListAccounts(context.Context, service.ListAccountsInput) (*service.AccountList, error) {
	return &service.AccountList{}, nil
}
func (*realAccountRecorder) BackfillLegacyIdentities(context.Context, int) (*service.BackfillResult, error) {
	return nil, nil
}

type realEventRecorder struct{ tenant string }

func (r *realEventRecorder) Ingest(ctx context.Context, envelope *event.Envelope) (*service.EventIngestResult, error) {
	r.tenant = envelope.TenantID
	return &service.EventIngestResult{EventID: envelope.EventID.String(), Status: "applied"}, nil
}

func TestRealSignedJWTReachesControlPlaneAccountRoute(t *testing.T) {
	validator, private := realJWKSValidator(t)
	recorder := &realAccountRecorder{}
	router := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, validator, httpapi.NewHealthHandler(), httpapi.NewAccountHandler(recorder, nil))

	req := httptest.NewRequest(http.MethodGet, "/v1/me?tenant_id=attacker", nil)
	req.Header.Set("Authorization", "Bearer "+realSignedJWT(t, private, map[string]any{
		"iss": "snaplink", "sub": "user-1", "aud": "aero-id", "tenant_id": "tenant-1", "client_id": "console",
		"exp": time.Now().Add(time.Minute).Unix(),
	}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if recorder.identity == nil || recorder.identity.TenantID != "tenant-1" || recorder.identity.Subject != "user-1" {
		t.Fatalf("service saw identity %+v", recorder.identity)
	}
}

func TestRealSignedJWTEventIngestAppliesAuthenticatedTenant(t *testing.T) {
	validator, private := realJWKSValidator(t)
	recorder := &realEventRecorder{}
	handler := httpapi.NewEventIngestHandler(recorder, "", 1<<20, time.Minute,
		httpapi.WithEventSourceKeys(map[string]string{"aero-im": "event-secret"}),
		httpapi.WithEventTimestampRequired(true), httpapi.WithEventSourceBindings(event.SourceBindings{
			"source-client": {TenantID: "tenant-1", Source: "aero-im", SourceRegion: "west"},
		}))
	router := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, validator, httpapi.NewHealthHandler(), handler)
	body := realEventBody(t)
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+realSignedJWT(t, private, map[string]any{
		"iss": "snaplink", "sub": "source-client", "aud": "aero-id", "tenant_id": "tenant-1", "client_id": "source-client",
		"exp": time.Now().Add(time.Minute).Unix(),
	}))
	req.Header.Set("X-Aero-Event-Timestamp", timestamp)
	req.Header.Set("X-Aero-Event-Signature", realEventSignature("event-secret", timestamp, body))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)

	if response.Code != http.StatusOK || recorder.tenant != "tenant-1" {
		t.Fatalf("status = %d tenant = %q: %s", response.Code, recorder.tenant, response.Body.String())
	}
}

func realJWKSValidator(t *testing.T) (*sso.JWKSValidator, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig", "kid": "real-test",
			"x": base64.RawURLEncoding.EncodeToString(public),
		}}})
	}))
	t.Cleanup(server.Close)
	validator, err := sso.NewJWKSValidator(sso.JWKSConfig{URL: server.URL, Issuer: "snaplink", Audience: "aero-id", RequireTenant: true})
	if err != nil {
		t.Fatal(err)
	}
	return validator, private
}

func realSignedJWT(t *testing.T, private ed25519.PrivateKey, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","kid":"real-test","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	return signed + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(signed)))
}

func realEventBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(event.Envelope{EventID: idgen.NewID(), Source: "aero-im", SourceRegion: "west",
		EventType: "account.updated", SchemaVersion: 1, AggregateType: "account", AggregateID: "source-account",
		AggregateVersion: 1, OccurredAt: time.Now().UTC(), Payload: map[string]any{"account_id": idgen.NewID().String()}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func realEventSignature(key, timestamp string, body []byte) string {
	// The handler's signature format is HMAC-SHA256(timestamp + "." + body).
	return signEvent(key, timestamp, body)
}

type legacyOnlyRecorder struct{}

func (legacyOnlyRecorder) RegisterRoutes(r httpapi.Routes) {
	r.Auth.GET("/legacy-real", func(c *gin.Context) { c.Status(http.StatusOK) })
}

func TestUIDOnlyValidatorRemainsLegacyOnly(t *testing.T) {
	router := httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, httpapi.NewHealthHandler(), legacyOnlyRecorder{})
	legacy := httptest.NewRequest(http.MethodGet, "/v1/legacy-real", nil)
	legacy.Header.Set("Authorization", "Bearer opaque")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, legacy)
	if response.Code != http.StatusOK {
		t.Fatalf("legacy status = %d, want 200", response.Code)
	}
}
