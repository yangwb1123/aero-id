package sso

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJWKSUnknownKIDRefreshIsBounded(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "kid": "known",
			"x": base64.RawURLEncoding.EncodeToString(public),
		}}})
	}))
	defer server.Close()
	validator, err := NewJWKSValidator(JWKSConfig{
		URL: server.URL, Issuer: "issuer", Audience: "audience",
		RefreshMinInterval: time.Hour, UnknownKIDCacheTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": "issuer", "sub": "subject", "aud": "audience",
		"tenant_id": "tenant", "client_id": "client", "exp": time.Now().Add(time.Minute).Unix()}
	for _, kid := range []string{"random-one", "random-one", "random-two"} {
		if _, err := validator.ValidateControlPlaneIdentity(t.Context(), signJWTWithKid(t, private, kid, claims)); err == nil {
			t.Fatalf("unknown kid %q was accepted", kid)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("JWKS requests = %d, want 1", got)
	}
}

func TestJWKSControlPlaneRejectsMalformedBoundedClaims(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	server := testJWKSServer(t, public)
	validator, err := NewJWKSValidator(JWKSConfig{URL: server.URL, Issuer: "snaplink", Audience: "aero-id"})
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"iss": "snaplink", "sub": "user-1", "aud": "aero-id",
		"tenant_id": "tenant-1", "client_id": "client-1", "exp": time.Now().Add(time.Minute).Unix()}
	cases := []struct {
		name  string
		claim string
		value any
	}{
		{name: "mixed audience types", claim: "aud", value: []any{"aero-id", 7}},
		{name: "malformed nbf", claim: "nbf", value: "later"},
		{name: "oversized subject", claim: "sub", value: strings.Repeat("x", maxIdentityClaimBytes+1)},
		{name: "control subject", claim: "sub", value: "user\n1"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			claims := make(map[string]any, len(base)+1)
			for key, value := range base {
				claims[key] = value
			}
			claims[test.claim] = test.value
			if _, err := validator.ValidateControlPlaneIdentity(t.Context(), signTestJWT(t, private, claims)); err == nil {
				t.Fatal("malformed claim was accepted")
			}
		})
	}
}

func signJWTWithKid(t *testing.T, key ed25519.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","kid":"` + kid + `","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	return signed + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(signed)))
}
