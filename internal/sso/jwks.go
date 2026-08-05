package sso

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"golang.org/x/sync/singleflight"
)

var (
	ErrNotJWT          = errors.New("token is not a compact jwt")
	ErrInvalidJWT      = errors.New("token is not a valid jwt")
	ErrJWKSUnavailable = errors.New("jwks is unavailable")
)

type JWKSConfig struct {
	URL           string
	Issuer        string
	Audience      string
	RequireTenant bool
	CacheTTL      time.Duration
	MaxBodyBytes  int64
	HTTPClient    *http.Client
}

type JWKSValidator struct {
	cfg     JWKSConfig
	client  *http.Client
	mu      sync.RWMutex
	keys    map[string]jwkKey
	expires time.Time
	flight  singleflight.Group
}

type FallbackValidator struct {
	local    *JWKSValidator
	upstream interface {
		ValidateToken(context.Context, string) (string, error)
	}
}

func NewJWKSValidator(cfg JWKSConfig) (*JWKSValidator, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("jwks url is required")
	}
	if err := validateJWKSURL(cfg.URL); err != nil {
		return nil, err
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &JWKSValidator{cfg: cfg, client: client, keys: make(map[string]jwkKey)}, nil
}

func NewFallbackValidator(local *JWKSValidator, upstream interface {
	ValidateToken(context.Context, string) (string, error)
}) *FallbackValidator {
	return &FallbackValidator{local: local, upstream: upstream}
}

func (v *FallbackValidator) ValidateToken(ctx context.Context, token string) (string, error) {
	identity, err := v.ValidateIdentity(ctx, token)
	if identity == nil {
		return "", err
	}
	return identity.UID, err
}

func (v *FallbackValidator) ValidateIdentity(ctx context.Context, token string) (*domain.TokenIdentity, error) {
	if v.local == nil || !looksLikeJWT(token) {
		uid, err := v.upstream.ValidateToken(ctx, token)
		if err != nil {
			return nil, err
		}
		return &domain.TokenIdentity{UID: uid, Subject: uid}, nil
	}
	identity, err := v.local.ValidateIdentity(ctx, token)
	if err == nil {
		return identity, nil
	}
	if errors.Is(err, ErrJWKSUnavailable) {
		uid, upstreamErr := v.upstream.ValidateToken(ctx, token)
		if upstreamErr != nil {
			return nil, upstreamErr
		}
		return &domain.TokenIdentity{UID: uid, Subject: uid}, nil
	}
	return nil, err
}

func (v *JWKSValidator) ValidateToken(ctx context.Context, token string) (string, error) {
	identity, err := v.ValidateIdentity(ctx, token)
	if identity == nil {
		return "", err
	}
	return identity.UID, err
}

func (v *JWKSValidator) ValidateIdentity(ctx context.Context, token string) (*domain.TokenIdentity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrNotJWT
	}
	header, err := decodeJWTHeader(parts[0])
	if err != nil {
		return nil, err
	}
	if err := v.verifySignature(ctx, parts, header); err != nil {
		return nil, err
	}
	claims, err := v.validatedClaims(parts[1])
	if err != nil {
		return nil, err
	}
	return v.identityFromClaims(claims)
}

type jwtHeader struct {
	algorithm string
	keyID     string
}

func decodeJWTHeader(encoded string) (jwtHeader, error) {
	header, err := decodeJSONObject(encoded)
	if err != nil {
		return jwtHeader{}, fmt.Errorf("%w: header", ErrInvalidJWT)
	}
	alg, _ := header["alg"].(string)
	kid, _ := header["kid"].(string)
	if alg == "" || kid == "" || !supportedAlgorithm(alg) {
		return jwtHeader{}, fmt.Errorf("%w: unsupported header", ErrInvalidJWT)
	}
	return jwtHeader{algorithm: alg, keyID: kid}, nil
}

func (v *JWKSValidator) verifySignature(ctx context.Context, parts []string, header jwtHeader) error {
	key, err := v.key(ctx, header.keyID)
	if err != nil {
		return err
	}
	if key.alg != "" && key.alg != header.algorithm {
		return fmt.Errorf("%w: key algorithm mismatch", ErrInvalidJWT)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("%w: signature encoding", ErrInvalidJWT)
	}
	if err := verifyJWTSignature(header.algorithm, key.public, []byte(parts[0]+"."+parts[1]), signature); err != nil {
		return fmt.Errorf("%w: signature", ErrInvalidJWT)
	}
	return nil
}

func (v *JWKSValidator) validatedClaims(encoded string) (map[string]any, error) {
	claims, err := decodeJSONObject(encoded)
	if err != nil {
		return nil, fmt.Errorf("%w: claims", ErrInvalidJWT)
	}
	if err := validateClaims(claims, v.cfg.Issuer, splitAudience(v.cfg.Audience), time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("%w: claims", ErrInvalidJWT)
	}
	return claims, nil
}

func (v *JWKSValidator) identityFromClaims(claims map[string]any) (*domain.TokenIdentity, error) {
	subject, _ := claims["sub"].(string)
	identity := &domain.TokenIdentity{UID: subject, Subject: subject}
	if issuerValue, ok := claims["iss"].(string); ok {
		identity.Issuer = issuerValue
	}
	identity.Audience = claimAudience(claims["aud"])
	tenant, err := consistentStringClaim(claims, "tenant_id", "tenant", "ten")
	if err != nil || (v.cfg.RequireTenant && tenant == "") {
		return nil, fmt.Errorf("%w: tenant claim", ErrInvalidJWT)
	}
	identity.TenantID = tenant
	clientID, err := consistentStringClaim(claims, "client_id", "azp")
	if err != nil {
		return nil, fmt.Errorf("%w: client claim", ErrInvalidJWT)
	}
	identity.ClientID = clientID
	return identity, nil
}

func validateJWKSURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("jwks url must be an absolute URL without userinfo")
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme != "https" && host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return errors.New("jwks url must use https outside loopback")
	}
	return nil
}

func consistentStringClaim(claims map[string]any, names ...string) (string, error) {
	value := ""
	for _, name := range names {
		claim, ok := claims[name]
		if !ok {
			continue
		}
		text, ok := claim.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return "", errors.New("invalid string claim")
		}
		if value != "" && value != text {
			return "", errors.New("conflicting claims")
		}
		value = text
	}
	return value, nil
}

func (v *JWKSValidator) key(ctx context.Context, kid string) (jwkKey, error) {
	v.mu.RLock()
	key, ok := v.keys[kid]
	fresh := time.Now().Before(v.expires)
	v.mu.RUnlock()
	if ok && fresh {
		return key, nil
	}
	value, err, _ := v.flight.Do("jwks", func() (any, error) { return v.refresh(ctx) })
	if err != nil {
		return jwkKey{}, err
	}
	keys := value.(map[string]jwkKey)
	key, ok = keys[kid]
	if !ok {
		return jwkKey{}, fmt.Errorf("%w: unknown kid", ErrInvalidJWT)
	}
	return key, nil
}

func (v *JWKSValidator) refresh(ctx context.Context) (map[string]jwkKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: create request", ErrJWKSUnavailable)
	}
	request.Header.Set("Accept", "application/json")
	response, err := v.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch", ErrJWKSUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: status %d", ErrJWKSUnavailable, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, v.cfg.MaxBodyBytes+1))
	if err != nil || int64(len(body)) > v.cfg.MaxBodyBytes {
		return nil, fmt.Errorf("%w: response body", ErrJWKSUnavailable)
	}
	keys, err := decodeJWKS(body)
	if err != nil {
		return nil, fmt.Errorf("%w: document", ErrJWKSUnavailable)
	}
	v.mu.Lock()
	v.keys = keys
	v.expires = time.Now().Add(v.cfg.CacheTTL)
	v.mu.Unlock()
	return keys, nil
}

func decodeJSONObject(encoded string) (map[string]any, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func looksLikeJWT(value string) bool { return strings.Count(value, ".") == 2 }

func supportedAlgorithm(alg string) bool {
	switch alg {
	case "EdDSA", "RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512":
		return true
	default:
		return false
	}
}

func validateClaims(claims map[string]any, issuer string, audiences []string, now time.Time) error {
	subject, _ := claims["sub"].(string)
	if strings.TrimSpace(subject) == "" {
		return errors.New("missing subject")
	}
	if issuer != "" && claims["iss"] != issuer {
		return errors.New("issuer mismatch")
	}
	if !audienceMatches(claims["aud"], audiences) {
		return errors.New("audience mismatch")
	}
	exp, ok := numericClaim(claims["exp"])
	if !ok || now.Unix() >= int64(exp) {
		return errors.New("expired token")
	}
	if nbf, ok := numericClaim(claims["nbf"]); ok && now.Unix()+60 < int64(nbf) {
		return errors.New("token is not active")
	}
	return nil
}

func splitAudience(value string) []string {
	result := make([]string, 0, 2)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func audienceMatches(raw any, expected []string) bool {
	if len(expected) == 0 {
		return true
	}
	values := []string{}
	switch value := raw.(type) {
	case string:
		values = append(values, value)
	case []any:
		for _, item := range value {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	}
	for _, want := range expected {
		for _, value := range values {
			if want == value {
				return true
			}
		}
	}
	return false
}

func claimAudience(raw any) []string {
	switch value := raw.(type) {
	case string:
		return []string{value}
	case []any:
		result := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func numericClaim(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok
}

func verifyJWTSignature(alg string, public crypto.PublicKey, signed, signature []byte) error {
	if alg == "EdDSA" {
		return verifyEdDSASignature(public, signed, signature)
	}
	hashFunc, hashSize, err := jwtHash(alg)
	if err != nil {
		return err
	}
	digest := hashFunc(signed)
	switch public.(type) {
	case *rsa.PublicKey:
		return verifyRSASignature(alg, public, hashSize, digest, signature)
	case *ecdsa.PublicKey:
		return verifyECDSASignature(public, digest, signature)
	default:
		return errors.New("unsupported public key")
	}
}

func verifyEdDSASignature(public crypto.PublicKey, signed, signature []byte) error {
	key, ok := public.(ed25519.PublicKey)
	if !ok || !ed25519.Verify(key, signed, signature) {
		return errors.New("invalid ed25519 signature")
	}
	return nil
}

func verifyRSASignature(alg string, public crypto.PublicKey, hash crypto.Hash, digest, signature []byte) error {
	key, ok := public.(*rsa.PublicKey)
	if !ok {
		return errors.New("invalid rsa public key")
	}
	if strings.HasPrefix(alg, "PS") {
		return rsa.VerifyPSS(key, hash, digest, signature, &rsa.PSSOptions{SaltLength: hash.Size(), Hash: hash})
	}
	return rsa.VerifyPKCS1v15(key, hash, digest, signature)
}

func verifyECDSASignature(public crypto.PublicKey, digest, signature []byte) error {
	key, ok := public.(*ecdsa.PublicKey)
	if !ok || len(signature)%2 != 0 {
		return errors.New("invalid ecdsa signature")
	}
	half := len(signature) / 2
	r := new(big.Int).SetBytes(signature[:half])
	s := new(big.Int).SetBytes(signature[half:])
	if !ecdsa.Verify(key, digest, r, s) {
		return errors.New("invalid ecdsa signature")
	}
	return nil
}

func jwtHash(alg string) (func([]byte) []byte, crypto.Hash, error) {
	switch alg {
	case "RS256", "PS256", "ES256":
		return func(value []byte) []byte { sum := sha256.Sum256(value); return sum[:] }, crypto.SHA256, nil
	case "RS384", "PS384", "ES384":
		return func(value []byte) []byte { sum := sha512.Sum384(value); return sum[:] }, crypto.SHA384, nil
	case "RS512", "PS512", "ES512":
		return func(value []byte) []byte { sum := sha512.Sum512(value); return sum[:] }, crypto.SHA512, nil
	default:
		return nil, 0, errors.New("unsupported jwt hash")
	}
}
