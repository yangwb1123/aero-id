// Package auditgovernance publishes Aero ID audit facts to Snaplink Audit
// Governance through a tenant-bound, redirect-safe OAuth client.
package auditgovernance

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrInvalidConfig     = errors.New("audit governance: invalid configuration")
	ErrInvalidEvent      = errors.New("audit governance: invalid event")
	ErrInvalidReceipt    = errors.New("audit governance: invalid receipt")
	ErrTokenUnavailable  = errors.New("audit governance: token unavailable")
	ErrTenantUnbound     = errors.New("audit governance: tenant binding unavailable")
	ErrTokenRejected     = errors.New("audit governance: token rejected")      // events-endpoint 401, non-scope body
	ErrTokenScopeMissing = errors.New("audit governance: token scope missing") // events 401-with-scope-body, token endpoint
	// ErrTokenEndpointScopeMissing marks a scope-missing classification that
	// originated at the token endpoint (400-gate or scope-claim validation) as
	// opposed to the events endpoint. It always wraps ErrTokenScopeMissing, so
	// errors.Is(err, ErrTokenScopeMissing) stays true for every consumer. The
	// drain uses the marker to skip its service-side counter for the token-
	// endpoint path (the composition-root hook already counted it — C-C, exactly
	// one increment per condition). It must NEVER be emitted at the events-
	// endpoint site (publisher.go:112).
	ErrTokenEndpointScopeMissing = errors.New("audit governance: token endpoint scope missing")
	errEpochDiscarded    = errors.New("audit governance: epoch discarded")     // stale in-flight fetch, internal only
)

type Config struct {
	Endpoint              string
	SourcePrefix          string
	ActorID               string
	SchemaID              string
	SchemaVersion         int
	DataClassification    string
	RetentionClass        string
	Timeout               time.Duration
	AllowInsecureLoopback bool
}

type OAuthConfig struct {
	SourcePrefix          string
	Timeout               time.Duration
	RefreshSkew           time.Duration
	AllowInsecureLoopback bool
	Bindings              map[string]OAuthBindingConfig
	// OnScopeRejected is invoked exactly once per token-endpoint scope
	// rejection (400 + {"error":"invalid_scope"|"insufficient_scope"}), and
	// once per successful token response whose scope claim omits
	// audit:event:write or explicitly empties it ("scope":""). It runs on
	// the singleflight flight goroutine: it is recover-wrapped and
	// re-entrancy-guarded in-package, so it may panic safely, but it must not
	// call back into the same tenant's AccessToken (the guard returns
	// ErrTokenUnavailable instead of self-joining). Keep the hook short.
	OnScopeRejected func(tenantID string)
}

type OAuthBindingConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string
	Resources    []string
}

type SourceBinding struct {
	TenantID     string
	SourceSystem string
}

// TokenSource is the merged interface pinned by
// docs/plans/2026-08-07-auditgovernance-tokensource-merged-interface.md (fixed
// exactly once; no third signature). BindingUnhealthy/MarkBindingUnhealthy are
// the sibling campaign's 403-cooldown scope and carry no behavior here.
type TokenSource interface {
	AccessToken(context.Context, SourceBinding) (string, error)
	HasBinding(string) bool
	Invalidate(SourceBinding) error      // 401: clear cached token, bump epoch; best-effort, always nil today
	BindingUnhealthy(SourceBinding) bool // 403 cooldown predicate (zero = healthy)
	MarkBindingUnhealthy(SourceBinding)  // 403: set cooldown; never clears the token cache
}

func TenantSourceID(prefix, tenantID string) (string, error) {
	if !validSourcePrefix(prefix) || tenantID == "" || tenantID != strings.TrimSpace(tenantID) {
		return "", ErrInvalidConfig
	}
	digest := sha256.Sum256([]byte(tenantID))
	return prefix + "." + base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func validSourcePrefix(prefix string) bool {
	if prefix == "" || len(prefix) > 64 || prefix != strings.TrimSpace(prefix) {
		return false
	}
	for index := range len(prefix) {
		value := prefix[index]
		if isAlphaNumeric(value) || value == '.' || value == '_' || value == '-' {
			continue
		}
		return false
	}
	return isAlphaNumeric(prefix[0]) && isAlphaNumeric(prefix[len(prefix)-1])
}

func isAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func secureEndpoint(raw string, allowLoopback bool) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return nil, ErrInvalidConfig
	}
	if endpoint.Scheme != "https" && !allowedLoopbackHTTP(endpoint, allowLoopback) {
		return nil, ErrInvalidConfig
	}
	return endpoint, nil
}

func allowedLoopbackHTTP(endpoint *url.URL, allowed bool) bool {
	if !allowed || endpoint.Scheme != "http" {
		return false
	}
	if strings.EqualFold(endpoint.Hostname(), "localhost") {
		return true
	}
	ip := net.ParseIP(endpoint.Hostname())
	return ip != nil && ip.IsLoopback()
}

func noRedirectClient(client *http.Client, timeout time.Duration) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if clone.Timeout <= 0 {
		clone.Timeout = timeout
	}
	return &clone
}
