package sso

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
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
	URL                string
	Issuer             string
	Audience           string
	RequireTenant      bool
	CacheTTL           time.Duration
	UnknownKIDCacheTTL time.Duration
	RefreshMinInterval time.Duration
	MaxBodyBytes       int64
	HTTPClient         *http.Client
}

type JWKSValidator struct {
	cfg              JWKSConfig
	client           *http.Client
	mu               sync.RWMutex
	keys             map[string]jwkKey
	expires          time.Time
	unknownKids      map[string]time.Time
	refreshNotBefore time.Time
	flight           singleflight.Group
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
	if cfg.UnknownKIDCacheTTL <= 0 {
		cfg.UnknownKIDCacheTTL = 30 * time.Second
	}
	if cfg.RefreshMinInterval <= 0 {
		cfg.RefreshMinInterval = time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &JWKSValidator{cfg: cfg, client: client, keys: make(map[string]jwkKey), unknownKids: make(map[string]time.Time)}, nil
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
	if len(token) == 0 || len(token) > maxJWTBytes {
		return nil, fmt.Errorf("%w: token size", ErrInvalidJWT)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || anyJWTPartTooLarge(parts) {
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

// ValidateControlPlaneIdentity is the claims-capable authentication seam for
// control-plane HTTP routes. It is intentionally implemented only by the local
// JWKS verifier; the fallback validator can produce UID-only identities.
func (v *JWKSValidator) ValidateControlPlaneIdentity(ctx context.Context, token string) (*domain.TokenIdentity, error) {
	if strings.TrimSpace(v.cfg.Issuer) == "" || strings.TrimSpace(v.cfg.Audience) == "" {
		return nil, fmt.Errorf("%w: control-plane claims are not configured", ErrInvalidJWT)
	}
	identity, err := v.ValidateIdentity(ctx, token)
	if err != nil {
		return nil, err
	}
	if identity == nil || validBoundedString(identity.UID, true) != nil ||
		validBoundedString(identity.Issuer, true) != nil || validBoundedString(identity.Subject, true) != nil ||
		validBoundedString(identity.TenantID, true) != nil || validBoundedString(identity.ClientID, true) != nil {
		return nil, fmt.Errorf("%w: incomplete identity", ErrInvalidJWT)
	}
	return identity, nil
}

type jwtHeader struct {
	algorithm string
	keyID     string
}

func decodeJWTHeader(encoded string) (jwtHeader, error) {
	if len(encoded) == 0 || len(encoded) > maxJWTHeaderPartBytes {
		return jwtHeader{}, fmt.Errorf("%w: header size", ErrInvalidJWT)
	}
	header, err := decodeJSONObject(encoded)
	if err != nil {
		return jwtHeader{}, fmt.Errorf("%w: header", ErrInvalidJWT)
	}
	alg, _ := header["alg"].(string)
	kid, _ := header["kid"].(string)
	if alg == "" || !validJWTHeaderKid(kid) || !supportedAlgorithm(alg) {
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
	identity := &domain.TokenIdentity{Verified: true, UID: subject, Subject: subject}
	if issuerValue, ok := claims["iss"].(string); ok {
		identity.Issuer = issuerValue
	}
	identity.Audience = claimAudience(claims["aud"])
	identity.Scopes = parseOAuthScopes(claims["scope"])
	tenant, err := consistentStringClaim(claims, "tenant_id", "tenant", "ten")
	if err != nil {
		return nil, fmt.Errorf("%w: tenant claim", ErrInvalidJWT)
	}
	allowedTenants, err := verifiedTenantDelegation(claims)
	if err != nil {
		return nil, fmt.Errorf("%w: tenant delegation claim", ErrInvalidJWT)
	}
	if v.cfg.RequireTenant && tenant == "" && len(allowedTenants) == 0 {
		return nil, fmt.Errorf("%w: tenant claim", ErrInvalidJWT)
	}
	identity.TenantID = tenant
	clientID, err := consistentStringClaim(claims, "client_id", "azp")
	if err != nil {
		return nil, fmt.Errorf("%w: client claim", ErrInvalidJWT)
	}
	identity.ClientID = clientID
	identity.AllowedTenants = allowedTenants
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

func verifiedTenantDelegation(claims map[string]any) ([]string, error) {
	value, ok := claims["allowed_tenants"]
	if !ok {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok || len(values) > 1024 {
		return nil, errors.New("allowed_tenants must be a bounded array")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, raw := range values {
		tenant, valid := delegatedTenant(raw)
		if !valid {
			return nil, errors.New("allowed_tenants contains an invalid tenant")
		}
		if seen[tenant] {
			continue
		}
		seen[tenant] = true
		result = append(result, tenant)
	}
	return result, nil
}

func delegatedTenant(value any) (string, bool) {
	tenant, ok := value.(string)
	if !ok {
		return "", false
	}
	if validBoundedString(tenant, true) != nil || tenant == "*" {
		return "", false
	}
	return tenant, true
}

func consistentStringClaim(claims map[string]any, names ...string) (string, error) {
	value := ""
	for _, name := range names {
		claim, ok := claims[name]
		if !ok {
			continue
		}
		text, ok := claim.(string)
		if !ok || validBoundedString(text, true) != nil {
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
	now := time.Now()
	v.mu.RLock()
	key, ok := v.keys[kid]
	fresh := now.Before(v.expires)
	negative := now.Before(v.unknownKids[kid])
	throttled := now.Before(v.refreshNotBefore)
	v.mu.RUnlock()
	if ok && fresh {
		return key, nil
	}
	if !ok && throttled {
		// This is a locally throttled unknown kid, not evidence that the
		// authority is unavailable. Keep legacy fallback validators from turning
		// refresh amplification into UID-only authentication.
		return jwkKey{}, fmt.Errorf("%w: unknown kid", ErrInvalidJWT)
	}
	// Do not refresh for every attacker-controlled unknown kid. A short-lived
	// negative cache handles retries for the same kid; refresh() also applies a
	// process-wide minimum interval for random kid amplification.
	if !ok && negative {
		return jwkKey{}, fmt.Errorf("%w: unknown kid", ErrInvalidJWT)
	}
	value, err, _ := v.flight.Do("jwks", func() (any, error) { return v.refresh(ctx) })
	if err != nil {
		return jwkKey{}, err
	}
	keys := value.(map[string]jwkKey)
	key, ok = keys[kid]
	if !ok {
		v.rememberUnknownKid(kid, time.Now())
		return jwkKey{}, fmt.Errorf("%w: unknown kid", ErrInvalidJWT)
	}
	return key, nil
}

func (v *JWKSValidator) refresh(ctx context.Context) (map[string]jwkKey, error) {
	now := time.Now()
	v.mu.Lock()
	if now.Before(v.refreshNotBefore) {
		v.mu.Unlock()
		return nil, fmt.Errorf("%w: refresh throttled", ErrJWKSUnavailable)
	}
	v.refreshNotBefore = now.Add(v.cfg.RefreshMinInterval)
	v.mu.Unlock()

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

func (v *JWKSValidator) rememberUnknownKid(kid string, now time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.unknownKids) >= maxUnknownKIDEntries {
		// The entries are only a negative cache. Dropping the whole bounded set
		// is preferable to letting attacker-controlled kid values grow memory.
		v.unknownKids = make(map[string]time.Time)
	}
	v.unknownKids[kid] = now.Add(v.cfg.UnknownKIDCacheTTL)
}
