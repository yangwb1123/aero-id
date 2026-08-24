// Package sourceauth contains the shared, source-independent policy for
// authenticated source endpoints. It deliberately imports no connector or
// configuration package so configuration validation and runtime construction
// cannot drift apart.
package sourceauth

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

const aeroIMSource = "aero-im"

// Mode identifies the authentication mode selected for a source connector.
type Mode string

const (
	ModeDisabled     Mode = "disabled"
	ModeOAuth        Mode = "oauth"
	ModeStaticBearer Mode = "static_bearer"
	// ModeBearer is retained as a concise alias for callers that do not need
	// to distinguish the transitional static bearer mechanism by name.
	ModeBearer   Mode = ModeStaticBearer
	ModeMTLS     Mode = "mtls"
	ModeLoopback Mode = "loopback"
)

// OAuthConfig contains only the fields needed to determine whether OAuth
// client credentials are complete. It is deliberately not used to emit any
// secret in an error.
type OAuthConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string
	Resource     string
}

// Config is the minimal input needed by the shared Aero IM authentication
// policy. It contains no secrets in errors returned by Evaluate.
type Config struct {
	Source                string
	Label                 string
	Endpoint              string
	Environment           string
	AllowInsecureLoopback bool
	BearerToken           string
	OAuth                 OAuthConfig
	// OAuthConfigured lets configuration validation reuse this policy after its
	// existing OAuth validator has run, without copying secret values here.
	OAuthConfigured bool
	TLSCertFile     string
	TLSKeyFile      string
}

// Decision is the selected authentication mode. BearerToken is normalized
// only for static bearer mode; OAuth access tokens are acquired lazily by the
// connector and are never returned by this package.
type Decision struct {
	Mode          Mode
	BearerToken   string
	Compatibility bool
}

// Evaluate validates and classifies an endpoint. The policy is intentionally
// scoped to Aero IM; other source connectors retain their existing behavior.
func Evaluate(cfg Config) (Decision, error) {
	if cfg.Source != "" && cfg.Source != aeroIMSource {
		return Decision{Mode: ModeDisabled, BearerToken: cfg.BearerToken}, nil
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return Decision{Mode: ModeDisabled}, nil
	}
	endpoint, err := ParseEndpoint(cfg.Endpoint)
	if err != nil {
		return Decision{}, policyError(cfg, "endpoint is invalid")
	}
	if err := validateLoopbackOption(cfg, endpoint.Hostname()); err != nil {
		return Decision{}, err
	}
	if err := validateCertificatePair(cfg); err != nil {
		return Decision{}, err
	}
	return classifyDecision(cfg, endpoint.Scheme)
}

func classifyDecision(cfg Config, scheme string) (Decision, error) {
	oauthConfigured, oauthPartial := classifyOAuth(cfg)
	if oauthPartial {
		return Decision{}, policyError(cfg, "OAuth token URL, client ID, client secret, scope, and resource are required together")
	}
	token, err := NormalizeBearerToken(cfg.BearerToken)
	if err != nil {
		return Decision{}, policyError(cfg, "bearer token is invalid")
	}
	if oauthConfigured && token != "" {
		return Decision{}, policyError(cfg, "must configure either bearer token or OAuth client credentials")
	}
	if oauthConfigured {
		return Decision{Mode: ModeOAuth}, nil
	}
	if token != "" {
		return Decision{Mode: ModeStaticBearer, BearerToken: token}, nil
	}
	return unauthenticatedDecision(cfg, scheme)
}

func unauthenticatedDecision(cfg Config, scheme string) (Decision, error) {
	if completeMTLS(cfg) {
		if scheme != "https" {
			return Decision{}, policyError(cfg, "mTLS requires an HTTPS endpoint")
		}
		return Decision{Mode: ModeMTLS}, nil
	}
	if cfg.AllowInsecureLoopback {
		return Decision{Mode: ModeLoopback, Compatibility: true}, nil
	}
	return Decision{}, policyError(cfg, "service authentication is required")
}

// ParseEndpoint accepts only the source endpoint shape shared by config and
// runtime construction. Errors intentionally do not include the raw value.
func ParseEndpoint(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("endpoint is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !validEndpointShape(parsed) {
		return nil, errors.New("endpoint must be an absolute URL without userinfo, query, or fragment")
	}
	if !validEndpointScheme(parsed.Scheme) {
		return nil, errors.New("endpoint must use HTTP or HTTPS")
	}
	if parsed.Scheme != "https" && !IsExactLoopbackHost(parsed.Hostname()) {
		return nil, errors.New("endpoint must use HTTPS outside loopback")
	}
	return parsed, nil
}

func validEndpointShape(parsed *url.URL) bool {
	return parsed != nil && parsed.Hostname() != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == ""
}

func validEndpointScheme(scheme string) bool {
	return scheme == "http" || scheme == "https"
}

// NormalizeBearerToken trims a static bearer token after rejecting all
// Unicode control characters. Trimming is allowed for ordinary surrounding
// whitespace, but a token containing a control character is never usable.
func NormalizeBearerToken(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", errors.New("bearer token contains control characters")
	}
	return trimmed, nil
}

// IsExactLoopbackHost is deliberately narrower than private-network checks:
// only these three spellings receive the unauthenticated compatibility mode.
func IsExactLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// ProductionLikeEnvironment is the single environment definition used by the
// loopback compatibility policy and configuration validation.
func ProductionLikeEnvironment(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "prod", "production", "staging":
		return true
	default:
		return false
	}
}

func validateLoopbackOption(cfg Config, host string) error {
	if !cfg.AllowInsecureLoopback {
		return nil
	}
	if strings.TrimSpace(cfg.Environment) == "" {
		return policyError(cfg, "insecure loopback compatibility requires an environment")
	}
	if ProductionLikeEnvironment(cfg.Environment) {
		return policyError(cfg, "insecure loopback compatibility is not allowed in production-like environments")
	}
	if !IsExactLoopbackHost(host) {
		return policyError(cfg, "insecure loopback compatibility requires an exact loopback endpoint")
	}
	return nil
}

func validateCertificatePair(cfg Config) error {
	certSet := strings.TrimSpace(cfg.TLSCertFile) != ""
	keySet := strings.TrimSpace(cfg.TLSKeyFile) != ""
	if certSet != keySet {
		return policyError(cfg, "mTLS requires both client certificate and key")
	}
	return nil
}

func classifyOAuth(cfg Config) (configured, partial bool) {
	if cfg.OAuthConfigured {
		return true, false
	}
	values := []string{cfg.OAuth.TokenURL, cfg.OAuth.ClientID, cfg.OAuth.ClientSecret,
		strings.Join(strings.Fields(cfg.OAuth.Scope), " "), cfg.OAuth.Resource}
	configured = false
	complete := true
	for _, value := range values {
		present := strings.TrimSpace(value) != ""
		configured = configured || present
		complete = complete && present
	}
	return configured && complete, configured && !complete
}

func completeMTLS(cfg Config) bool {
	return strings.TrimSpace(cfg.TLSCertFile) != "" && strings.TrimSpace(cfg.TLSKeyFile) != ""
}

func policyError(cfg Config, message string) error {
	label := strings.TrimSpace(cfg.Label)
	if label == "" {
		label = strings.TrimSpace(cfg.Source)
	}
	if label == "" {
		label = "source"
	}
	return errors.New(label + " " + message)
}
