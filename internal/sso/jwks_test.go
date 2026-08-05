package sso

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestJWKSValidatorVerifiesSnaplinkEdDSA(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig", "kid": "kid-1",
			"x": base64.RawURLEncoding.EncodeToString(public),
		}}})
	}))
	defer server.Close()
	validator, err := NewJWKSValidator(JWKSConfig{URL: server.URL, Issuer: "snaplink", Audience: "aero-id"})
	if err != nil {
		t.Fatal(err)
	}
	token := signTestJWT(t, private, map[string]any{"iss": "snaplink", "sub": "user-1", "aud": "aero-id", "exp": time.Now().Add(time.Minute).Unix()})
	uid, err := validator.ValidateToken(t.Context(), token)
	if err != nil || uid != "user-1" {
		t.Fatalf("ValidateToken() = %q, %v", uid, err)
	}
}

func TestJWKSValidatorRejectsWrongAudience(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "kid": "kid-1", "x": base64.RawURLEncoding.EncodeToString(public),
		}}})
	}))
	defer server.Close()
	validator, _ := NewJWKSValidator(JWKSConfig{URL: server.URL, Issuer: "snaplink", Audience: "aero-id"})
	token := signTestJWT(t, private, map[string]any{"iss": "snaplink", "sub": "user-1", "aud": "other", "exp": time.Now().Add(time.Minute).Unix()})
	if _, err := validator.ValidateToken(t.Context(), token); err == nil {
		t.Fatal("wrong audience was accepted")
	}
}

func TestJWKSValidatorBindsTenantAndClientClaims(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	server := testJWKSServer(t, public)
	validator, err := NewJWKSValidator(JWKSConfig{
		URL: server.URL, Issuer: "snaplink", Audience: "aero-id", RequireTenant: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := signTestJWT(t, private, map[string]any{
		"iss": "snaplink", "sub": "user-1", "aud": "aero-id", "tenant_id": "tenant-1",
		"client_id": "console", "azp": "console", "exp": time.Now().Add(time.Minute).Unix(),
	})
	identity, err := validator.ValidateIdentity(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if identity.TenantID != "tenant-1" || identity.ClientID != "console" {
		t.Fatalf("identity binding = tenant %q client %q", identity.TenantID, identity.ClientID)
	}
}

func TestJWKSValidatorRejectsMissingOrConflictingBindings(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	server := testJWKSServer(t, public)
	validator, err := NewJWKSValidator(JWKSConfig{
		URL: server.URL, Issuer: "snaplink", Audience: "aero-id", RequireTenant: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"iss": "snaplink", "sub": "user-1", "aud": "aero-id", "exp": time.Now().Add(time.Minute).Unix()}
	if _, err := validator.ValidateIdentity(t.Context(), signTestJWT(t, private, base)); err == nil {
		t.Fatal("token without a tenant was accepted")
	}
	base["tenant_id"], base["tenant"] = "tenant-1", "tenant-2"
	if _, err := validator.ValidateIdentity(t.Context(), signTestJWT(t, private, base)); err == nil {
		t.Fatal("token with conflicting tenant claims was accepted")
	}
}

func TestNewJWKSValidatorRejectsPlainHTTPOutsideLoopback(t *testing.T) {
	if _, err := NewJWKSValidator(JWKSConfig{URL: "http://jwks.example.test/keys"}); err == nil {
		t.Fatal("plain HTTP JWKS URL was accepted")
	}
}

func testJWKSServer(t *testing.T, public ed25519.PublicKey) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig", "kid": "kid-1",
			"x": base64.RawURLEncoding.EncodeToString(public),
		}}})
	}))
	t.Cleanup(server.Close)
	return server
}

func signTestJWT(t *testing.T, key ed25519.PrivateKey, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","kid":"kid-1","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signed := header + "." + encoded
	return signed + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(signed)))
}
