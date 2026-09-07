package connector

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

const maxTargetAssertionJWKSBytes int64 = 256 << 10

// VerifyAeroIMTargetAssertionTrust performs the release-time trust check for
// the already configured signer. It is deliberately not called by normal
// connector construction: application startup must not depend on a remote
// JWKS, while the deployment preflight must prove that the configured kid and
// public key are registered in the dedicated Aero IM trust domain.
func VerifyAeroIMTargetAssertionTrust(cfg HTTPConfig) error {
	if strings.TrimSpace(cfg.TargetAssertionJWKSURL) == "" {
		return errors.New("target assertion trust verification requires a JWKS URL")
	}
	signer, err := configuredPreflightSigner(cfg)
	if err != nil {
		return err
	}
	endpoint, err := parseTargetAssertionJWKSURL(cfg.TargetAssertionJWKSURL)
	if err != nil {
		return err
	}
	data, err := fetchTargetAssertionJWKS(cfg, endpoint)
	if err != nil {
		return err
	}
	if !targetAssertionJWKSMatches(data, signer) {
		return errors.New("target assertion trust document does not contain the configured key")
	}
	return nil
}

func configuredPreflightSigner(cfg HTTPConfig) (*accountSummaryTargetSigner, error) {
	signer, err := configuredAccountSummaryTargetSigner(cfg)
	if err != nil || signer == nil {
		return nil, errors.New("target assertion trust verification could not load the signer")
	}
	return signer, nil
}

func fetchTargetAssertionJWKS(cfg HTTPConfig, endpoint *url.URL) ([]byte, error) {
	client, err := sourceClient(HTTPConfig{Endpoint: endpoint.String(), Environment: cfg.Environment,
		Timeout: targetAssertionPreflightTimeout(cfg.Timeout), TLSCAFile: cfg.TLSCAFile, TLSServerName: cfg.TLSServerName})
	if err != nil {
		return nil, errors.New("target assertion trust verification client is unavailable")
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("target assertion trust verification request is invalid")
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("target assertion trust verification endpoint is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("target assertion trust verification endpoint rejected the request")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxTargetAssertionJWKSBytes+1))
	if err != nil || int64(len(data)) > maxTargetAssertionJWKSBytes {
		return nil, errors.New("target assertion trust document is unavailable")
	}
	return data, nil
}

func parseTargetAssertionJWKSURL(raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" || strings.IndexAny(value, "?#") >= 0 {
		return nil, errors.New("target assertion JWKS URL must be HTTPS without query or fragment")
	}
	parsed, err := sourceauth.ParseEndpoint(value)
	if err != nil || parsed.Scheme != "https" {
		return nil, errors.New("target assertion JWKS URL must be HTTPS without query or fragment")
	}
	return parsed, nil
}

type targetAssertionJWKSet struct {
	Keys []targetAssertionJWK `json:"keys"`
}

type targetAssertionJWK struct {
	KTY    string   `json:"kty"`
	CRV    string   `json:"crv"`
	X      string   `json:"x"`
	KID    string   `json:"kid"`
	Use    string   `json:"use"`
	KeyOps []string `json:"key_ops"`
	Alg    string   `json:"alg"`
}

func targetAssertionJWKSMatches(data []byte, signer *accountSummaryTargetSigner) bool {
	document, ok := decodeTargetAssertionJWKSet(data)
	if !ok {
		return false
	}
	public, ok := targetAssertionPublicKey(signer)
	if !ok {
		return false
	}
	return targetAssertionKeyMatches(document.Keys, signer.keyID, public)
}

func decodeTargetAssertionJWKSet(data []byte) (targetAssertionJWKSet, bool) {
	var document targetAssertionJWKSet
	if json.Unmarshal(data, &document) != nil || len(document.Keys) == 0 {
		return targetAssertionJWKSet{}, false
	}
	return document, true
}

func targetAssertionPublicKey(signer *accountSummaryTargetSigner) (ed25519.PublicKey, bool) {
	if signer == nil {
		return nil, false
	}
	public, ok := signer.privateKey.Public().(ed25519.PublicKey)
	return public, ok && len(public) == ed25519.PublicKeySize
}

func targetAssertionKeyMatches(keys []targetAssertionJWK, keyID string, public ed25519.PublicKey) bool {
	seen := make(map[string]struct{}, len(keys))
	matched := false
	for _, key := range keys {
		if !validTargetAssertionJWK(key) || strings.TrimSpace(key.KID) == "" {
			return false
		}
		if _, exists := seen[key.KID]; exists {
			return false
		}
		seen[key.KID] = struct{}{}
		if key.KID != keyID {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil || !bytes.Equal(decoded, public) {
			return false
		}
		matched = true
	}
	return matched
}

func validTargetAssertionJWK(key targetAssertionJWK) bool {
	if key.KTY != "OKP" || key.CRV != "Ed25519" || key.Alg != "EdDSA" || key.Use != "sig" {
		return false
	}
	if len(key.KeyOps) != 1 || key.KeyOps[0] != "verify" {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(key.X)
	return err == nil && len(decoded) == ed25519.PublicKeySize
}

// Keep the preflight timeout bounded even when a caller passes a zero config.
func targetAssertionPreflightTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return 5 * time.Second
	}
	return value
}
