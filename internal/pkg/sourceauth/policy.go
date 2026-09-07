// Package sourceauth contains the shared, source-independent policy for
// authenticated source endpoints. It deliberately imports no connector or
// configuration package so configuration validation and runtime construction
// cannot drift apart.
package sourceauth

import (
	"errors"
	"strings"
	"unicode"
)

const (
	aeroIMSource    = "aero-im"
	aeroVaultSource = "aero-vault"
	snaplinkSource  = "snaplink"

	// Vault's account-summary handler resolves a PrincipalService from a
	// Snaplink client-credentials token. These values are part of that source
	// contract, not caller-selected permissions.
	VaultOAuthScope    = "read"
	VaultOAuthResource = "aero-vault"
	VaultOAuthAudience = "aero-vault"
)

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
	AllowedHosts []string
}

// Config is the minimal input needed by the shared source authentication
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
	OAuthConfigured  bool
	TLSCertFile      string
	TLSKeyFile       string
	OAuthTLSCertFile string
	OAuthTLSKeyFile  string
}

// Decision is the selected authentication mode. BearerToken is normalized
// only for static bearer mode; OAuth access tokens are acquired lazily by the
// connector and are never returned by this package.
type Decision struct {
	Mode          Mode
	BearerToken   string
	Compatibility bool
}

// Evaluate validates and classifies an endpoint. Snaplink, Aero IM, and Aero
// Vault are authenticated service-to-service sources; other source connectors
// retain their existing compatibility behavior until their contracts migrate.
func Evaluate(cfg Config) (Decision, error) {
	if !authenticatedSource(cfg.Source) {
		return Decision{Mode: ModeDisabled, BearerToken: cfg.BearerToken}, nil
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return Decision{Mode: ModeDisabled}, nil
	}
	endpoint, err := ParseEndpoint(cfg.Endpoint)
	if err != nil {
		return Decision{}, policyError(cfg, "endpoint is invalid")
	}
	if err := validateEndpointAuthentication(cfg, endpoint.Scheme, endpoint.Hostname()); err != nil {
		return Decision{}, err
	}
	return classifyDecision(cfg, endpoint.Scheme)
}

func authenticatedSource(source string) bool {
	switch source {
	case "", aeroIMSource, aeroVaultSource, snaplinkSource:
		return true
	default:
		return false
	}
}

func validateEndpointAuthentication(cfg Config, scheme, host string) error {
	if err := validateLoopbackOption(cfg, host); err != nil {
		return err
	}
	if err := validateCertificatePair(cfg.TLSCertFile, cfg.TLSKeyFile, "mTLS"); err != nil {
		return err
	}
	if err := validateCertificatePair(cfg.OAuthTLSCertFile, cfg.OAuthTLSKeyFile, "OAuth mTLS"); err != nil {
		return err
	}
	if completeMTLS(cfg) && scheme != "https" {
		return policyError(cfg, "mTLS requires an HTTPS endpoint")
	}
	if completeOAuthMTLS(cfg) && strings.TrimSpace(cfg.OAuth.TokenURL) == "" {
		return policyError(cfg, "OAuth mTLS requires OAuth client credentials")
	}
	return nil
}

func classifyDecision(cfg Config, scheme string) (Decision, error) {
	oauthConfigured, oauthPartial := classifyOAuth(cfg)
	if oauthPartial {
		return Decision{}, policyError(cfg, "OAuth token URL, client ID, client secret, scope, and resource are required together")
	}
	if oauthConfigured {
		if err := validateOAuthDecisionEndpoint(cfg); err != nil {
			return Decision{}, err
		}
	}
	token, err := NormalizeBearerToken(cfg.BearerToken)
	if err != nil {
		return Decision{}, policyError(cfg, "bearer token is invalid")
	}
	if oauthConfigured && token != "" {
		return Decision{}, policyError(cfg, "must configure either bearer token or OAuth client credentials")
	}
	if cfg.Source == aeroVaultSource {
		return classifyVaultDecision(cfg, oauthConfigured, token)
	}
	return classifyNonVaultDecision(cfg, scheme, oauthConfigured, token)
}

func validateOAuthDecisionEndpoint(cfg Config) error {
	tokenEndpoint, err := ParseOAuthTokenEndpoint(cfg.OAuth.TokenURL, cfg.Environment, cfg.AllowInsecureLoopback,
		cfg.OAuth.AllowedHosts, cfg.Endpoint)
	if err != nil {
		return policyError(cfg, "OAuth token URL is not approved")
	}
	if completeOAuthMTLS(cfg) && tokenEndpoint.Scheme != "https" {
		return policyError(cfg, "OAuth mTLS requires an HTTPS token endpoint")
	}
	return nil
}

func classifyNonVaultDecision(cfg Config, scheme string, oauthConfigured bool, token string) (Decision, error) {
	if oauthConfigured {
		return Decision{Mode: ModeOAuth}, nil
	}
	if token != "" {
		return Decision{Mode: ModeStaticBearer, BearerToken: token}, nil
	}
	return unauthenticatedDecision(cfg, scheme)
}

func classifyVaultDecision(cfg Config, oauthConfigured bool, token string) (Decision, error) {
	if !oauthConfigured {
		// Static API keys, local JWTs, mTLS-only, and unauthenticated loopback
		// requests resolve to the wrong PrincipalKind at the deployed Vault
		// endpoint. Do not advertise them as compatible authentication modes.
		return Decision{}, policyError(cfg, "service authentication is required: Vault requires Snaplink OAuth client credentials for PrincipalService")
	}
	if token != "" {
		return Decision{}, policyError(cfg, "Aero Vault requires OAuth client credentials, not a bearer token")
	}
	if strings.Join(strings.Fields(cfg.OAuth.Scope), " ") != VaultOAuthScope ||
		strings.TrimSpace(cfg.OAuth.Resource) != VaultOAuthResource {
		return Decision{}, policyError(cfg, "Aero Vault OAuth scope and resource are not approved")
	}
	return Decision{Mode: ModeOAuth}, nil
}

func unauthenticatedDecision(cfg Config, scheme string) (Decision, error) {
	if completeMTLS(cfg) {
		return Decision{Mode: ModeMTLS}, nil
	}
	if cfg.AllowInsecureLoopback {
		return Decision{Mode: ModeLoopback, Compatibility: true}, nil
	}
	return Decision{}, policyError(cfg, "service authentication is required")
}

// NormalizeBearerToken trims a static bearer token after rejecting all
// Unicode control characters. Trimming is allowed for ordinary surrounding
// whitespace, but a token containing a control character is never usable.
func NormalizeBearerToken(value string) (string, error) {
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", errors.New("bearer token contains control characters")
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	return trimmed, nil
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

func validateCertificatePair(certFile, keyFile, label string) error {
	certSet := strings.TrimSpace(certFile) != ""
	keySet := strings.TrimSpace(keyFile) != ""
	if certSet != keySet {
		return errors.New("source " + label + " requires both client certificate and key")
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

func completeOAuthMTLS(cfg Config) bool {
	return strings.TrimSpace(cfg.OAuthTLSCertFile) != "" && strings.TrimSpace(cfg.OAuthTLSKeyFile) != ""
}

func policyError(cfg Config, message string) error {
	label := safePolicyLabel(cfg.Label)
	if label == "" {
		label = safePolicyLabel(cfg.Source)
	}
	if label == "" {
		label = "source"
	}
	return errors.New(label + " " + message)
}

func safePolicyLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "snaplink", "aero-im", "aero-vault", "sources.snaplink", "sources.aero-im", "sources.aero-vault":
		return strings.TrimSpace(value)
	default:
		return ""
	}
}
