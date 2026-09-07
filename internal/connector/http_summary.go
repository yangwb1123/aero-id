package connector

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

type HTTPConfig struct {
	Endpoint              string
	AllowInsecureLoopback bool
	Environment           string
	SummaryPath           string
	ErasePath             string
	OperationStatusPath   string
	Region                string
	BearerToken           string
	OAuth                 OAuthClientCredentialsConfig
	OAuthAllowedHosts     []string
	Timeout               time.Duration
	MaxBodyBytes          int64
	// MaxConcurrency is the legacy aggregate fallback. It is partitioned into
	// query, command, and health pools so background work cannot consume the
	// interactive summary pool.
	MaxConcurrency        int
	QueryMaxConcurrency   int
	CommandMaxConcurrency int
	HealthMaxConcurrency  int
	MaxAttempts           int
	RetryBackoff          time.Duration
	TLSCAFile             string
	TLSCertFile           string
	TLSKeyFile            string
	TLSServerName         string
	// OAuth TLS settings are intentionally independent from the source
	// transport trust profile. The token authority and Vault are different
	// security boundaries and must not inherit one another's CA, SNI, or
	// client certificate.
	OAuthTLSCAFile     string
	OAuthTLSCertFile   string
	OAuthTLSKeyFile    string
	OAuthTLSServerName string
	OAuthTenantID      string
	// Account-summary target assertions are used only by the Aero IM
	// connector. The key is loaded from one of the two secret references;
	// non-Aero-IM connectors ignore these fields.
	TargetAssertionKeyFile     string
	TargetAssertionKeyEnv      string
	TargetAssertionJWKSURL     string
	TargetAssertionTrustDomain string
	TargetAssertionKeyID       string
	TargetAssertionIssuer      string
	TargetAssertionAudience    string
	TargetAssertionSubject     string
	TargetAssertionClientID    string
	TargetAssertionLifetime    time.Duration
	Client                     *http.Client
	Metrics                    *metrics.Registry

	// These are unexported test seams. Production composition always uses
	// TargetAssertionKeyFile/TargetAssertionKeyEnv above and cannot inject a
	// request rewriter from another package.
	targetAssertionSigner *accountSummaryTargetSigner
	testTargetRequest     func(SummaryRequest) SummaryRequest
}

// JSONSummaryConnector implements the small, versioned summary seam expected
// from a source service. Source-specific clients can replace it without
// changing aggregation services or public APIs.
type JSONSummaryConnector struct {
	source                Source
	cfg                   HTTPConfig
	client                *http.Client
	endpointURL           *url.URL
	tokens                *oauthClientCredentials
	queryPolicy           *sourceWorkloadPolicy
	commandPolicy         *sourceWorkloadPolicy
	healthPolicy          *sourceWorkloadPolicy
	targetAssertionSigner *accountSummaryTargetSigner
	testTargetRequest     func(SummaryRequest) SummaryRequest
}

const (
	maxSourceBodyBytes    int64 = 16 << 20
	maxSourceConcurrency        = 4096
	maxSourceAttempts           = 10
	maxSourceTimeout            = 5 * time.Minute
	maxSourceRetryBackoff       = time.Minute
)

func NewJSONSummaryConnector(source Source, cfg HTTPConfig) (*JSONSummaryConnector, error) {
	if !validTrustedSummarySource(source) {
		return nil, fmt.Errorf("source connector is unsupported")
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, fmt.Errorf("%s connector endpoint is required", source)
	}
	cfg, endpoint, decision, err := prepareSourceHTTPConfig(source, cfg)
	if err != nil {
		return nil, err
	}
	targetAssertionSigner, err := targetAssertionSignerForSource(source, cfg)
	if err != nil {
		return nil, err
	}
	client, tokens, err := sourceClientAndOAuth(source, cfg)
	if err != nil {
		return nil, err
	}
	if decision.Compatibility && cfg.Metrics != nil {
		cfg.Metrics.ObserveSourceAuthCompatibility(source.String(), string(decision.Mode))
	}
	limits := workloadLimits(cfg)
	return &JSONSummaryConnector{source: source, cfg: cfg, client: client, endpointURL: cloneURL(endpoint), tokens: tokens,
		queryPolicy: newSourceWorkloadPolicy(limits.query), commandPolicy: newSourceWorkloadPolicy(limits.command),
		healthPolicy: newSourceWorkloadPolicy(limits.health), targetAssertionSigner: targetAssertionSigner,
		testTargetRequest: cfg.testTargetRequest,
	}, nil
}

func prepareSourceHTTPConfig(source Source, cfg HTTPConfig) (HTTPConfig, *url.URL, sourceauth.Decision, error) {
	cfg = defaultHTTPConfig(cfg)
	endpoint, err := sourceauth.ParseEndpoint(cfg.Endpoint)
	if err != nil {
		return HTTPConfig{}, nil, sourceauth.Decision{}, fmt.Errorf("%s connector endpoint is invalid", source)
	}
	cfg.Endpoint = endpoint.String()
	cfg.Region = strings.TrimSpace(cfg.Region)
	if source == SourceAeroVault {
		cfg.OAuthTenantID = strings.TrimSpace(cfg.OAuthTenantID)
	}
	if err := validateHTTPConfigBounds(cfg); err != nil {
		return HTTPConfig{}, nil, sourceauth.Decision{}, err
	}
	if err := validateHTTPConfigPaths(cfg); err != nil {
		return HTTPConfig{}, nil, sourceauth.Decision{}, err
	}
	decision, err := sourceAuthDecision(source, cfg)
	if err != nil {
		return HTTPConfig{}, nil, sourceauth.Decision{}, err
	}
	if source == SourceAeroVault {
		if err := validateVaultPrincipalConfig(cfg, decision); err != nil {
			return HTTPConfig{}, nil, sourceauth.Decision{}, err
		}
	}
	return applySourceAuth(source, cfg, decision), endpoint, decision, nil
}

func targetAssertionSignerForSource(source Source, cfg HTTPConfig) (*accountSummaryTargetSigner, error) {
	if source != SourceAeroIM {
		return nil, nil
	}
	if cfg.targetAssertionSigner != nil {
		if err := validateTargetAssertionEndpoint(cfg); err != nil {
			return nil, err
		}
		return cfg.targetAssertionSigner, nil
	}
	signer, err := configuredAccountSummaryTargetSigner(cfg)
	if err != nil {
		return nil, fmt.Errorf("%s target assertion: %w", source, err)
	}
	if signer != nil {
		if err := validateTargetAssertionEndpoint(cfg); err != nil {
			return nil, err
		}
	}
	if signer == nil && !sourceauth.IsExplicitNonProductionLoopback(cfg.Endpoint, cfg.Environment, cfg.AllowInsecureLoopback) {
		return nil, fmt.Errorf("sources.aero_im target assertion is required for an enabled non-loopback or production-like endpoint")
	}
	return signer, nil
}

func sourceAuthDecision(source Source, cfg HTTPConfig) (sourceauth.Decision, error) {
	allowedHosts := cfg.OAuthAllowedHosts
	if len(allowedHosts) == 0 {
		allowedHosts = cfg.OAuth.AllowedHosts
	}
	decision, err := sourceauth.Evaluate(sourceauth.Config{
		Source: source.String(), Label: sourceLabel(source), Endpoint: cfg.Endpoint, Environment: cfg.Environment,
		AllowInsecureLoopback: cfg.AllowInsecureLoopback, BearerToken: cfg.BearerToken,
		OAuth: sourceauth.OAuthConfig{TokenURL: cfg.OAuth.TokenURL, ClientID: cfg.OAuth.ClientID,
			ClientSecret: cfg.OAuth.ClientSecret, Scope: cfg.OAuth.Scope, Resource: cfg.OAuth.Resource,
			AllowedHosts: allowedHosts},
		TLSCertFile: cfg.TLSCertFile, TLSKeyFile: cfg.TLSKeyFile,
		OAuthTLSCertFile: cfg.OAuthTLSCertFile, OAuthTLSKeyFile: cfg.OAuthTLSKeyFile,
	})
	if err != nil {
		return sourceauth.Decision{}, fmt.Errorf("%s connector authentication: %w", source, err)
	}
	return decision, nil
}

func applySourceAuth(source Source, cfg HTTPConfig, decision sourceauth.Decision) HTTPConfig {
	if source == SourceAeroIM || source == SourceSnaplink {
		cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	}
	if decision.Mode == sourceauth.ModeStaticBearer {
		cfg.BearerToken = decision.BearerToken
	} else {
		// Do not let whitespace-only or stale bearer configuration survive a
		// non-bearer decision. In particular, mTLS-only and loopback requests
		// must not acquire an Authorization header from raw config text.
		cfg.BearerToken = ""
	}
	return cfg
}

func sourceClientAndOAuth(source Source, cfg HTTPConfig) (*http.Client, *oauthClientCredentials, error) {
	client, err := sourceClient(cfg)
	if err != nil {
		return nil, nil, err
	}
	oauthConfig := cfg.OAuth
	oauthConfig.Environment = cfg.Environment
	oauthConfig.AllowLoopback = cfg.AllowInsecureLoopback
	oauthConfig.SourceEndpoint = cfg.Endpoint
	oauthConfig.TLSCAFile = cfg.OAuthTLSCAFile
	oauthConfig.TLSCertFile = cfg.OAuthTLSCertFile
	oauthConfig.TLSKeyFile = cfg.OAuthTLSKeyFile
	oauthConfig.TLSServerName = cfg.OAuthTLSServerName
	if len(cfg.OAuthAllowedHosts) > 0 {
		oauthConfig.AllowedHosts = cfg.OAuthAllowedHosts
	}
	if source == SourceAeroVault {
		oauthConfig.RequiredScope = sourceauth.VaultOAuthScope
		oauthConfig.RequiredAudience = sourceauth.VaultOAuthAudience
		oauthConfig.RequireJWT = true
	}
	// Pass the original injected client only as a test seam. A fresh hardened
	// client is built for the token authority, so source TLS roots/SNI/client
	// certificates cannot bleed into OAuth traffic.
	tokens, err := newOAuthClientCredentials(oauthConfig, cfg.Client, cfg.Timeout)
	if err != nil {
		return nil, nil, fmt.Errorf("%s connector OAuth: %w", source, err)
	}
	if tokens != nil && strings.TrimSpace(cfg.BearerToken) != "" {
		return nil, nil, fmt.Errorf("%s connector must configure either bearer token or OAuth client credentials", source)
	}
	return client, tokens, nil
}

func sourceLabel(source Source) string {
	if source == SourceAeroIM {
		return "sources.aero-im"
	}
	return string(source)
}

func validateHTTPConfigPaths(cfg HTTPConfig) error {
	paths := []string{cfg.SummaryPath, cfg.ErasePath, cfg.OperationStatusPath}
	for _, path := range paths {
		if !sourceauth.ValidAbsolutePath(path) {
			return fmt.Errorf("source connector paths must be absolute and free of control characters")
		}
	}
	return nil
}

func validateTargetAssertionEndpoint(cfg HTTPConfig) error {
	endpoint, err := sourceauth.ParseEndpoint(cfg.Endpoint)
	if err != nil || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawPath != "" {
		return fmt.Errorf("aero-im target assertion requires a root endpoint")
	}
	if strings.TrimSpace(cfg.Environment) != "" && sourceauth.ProductionLikeEnvironment(cfg.Environment) &&
		strings.TrimSpace(cfg.TargetAssertionJWKSURL) == "" {
		return fmt.Errorf("aero-im target assertion JWKS trust URL is required in production-like environments")
	}
	return nil
}

func cloneURL(value *url.URL) *url.URL {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func defaultHTTPConfig(cfg HTTPConfig) HTTPConfig {
	if cfg.SummaryPath == "" {
		cfg.SummaryPath = "/internal/account-summary"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 32
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = 50 * time.Millisecond
	}
	return cfg
}

func validateHTTPConfigBounds(cfg HTTPConfig) error {
	if cfg.MaxBodyBytes > maxSourceBodyBytes {
		return fmt.Errorf("source connector max body bytes must not exceed %d", maxSourceBodyBytes)
	}
	for name, limit := range map[string]int{
		"max": cfg.MaxConcurrency, "query": cfg.QueryMaxConcurrency,
		"command": cfg.CommandMaxConcurrency, "health": cfg.HealthMaxConcurrency,
	} {
		if limit > maxSourceConcurrency {
			return fmt.Errorf("source connector %s max concurrency must not exceed %d", name, maxSourceConcurrency)
		}
	}
	if cfg.MaxAttempts > maxSourceAttempts {
		return fmt.Errorf("source connector max attempts must not exceed %d", maxSourceAttempts)
	}
	if cfg.Timeout > maxSourceTimeout {
		return fmt.Errorf("source connector timeout must not exceed %s", maxSourceTimeout)
	}
	if cfg.RetryBackoff > maxSourceRetryBackoff {
		return fmt.Errorf("source connector retry backoff must not exceed %s", maxSourceRetryBackoff)
	}
	return nil
}

// clampedSummaryConcurrency preserves the connector's historical default:
// MaxConcurrency <= 0 means 32, and any positive value passes through
// unclamped. The clamp must happen here so the bulkhead module's New(0) ->
// serial contract never silently serializes the connector.
func clampedSummaryConcurrency(limit int) int {
	if limit <= 0 {
		return 32
	}
	return limit
}

func (c *JSONSummaryConnector) Source() Source { return c.source }

func (c *JSONSummaryConnector) Summary(ctx context.Context, req SummaryRequest) (result *Summary, resultErr error) {
	resolved, err := ResolveDatasetScope(c.source, req.Datasets, DatasetScopeSummary)
	if err != nil {
		return nil, err
	}
	req.Datasets = resolved
	req.Region, err = c.vaultRequestRegion(req.Region)
	if err != nil {
		return nil, err
	}
	if c.source == SourceAeroIM && c.targetAssertionSigner == nil {
		return nil, &RemoteError{Source: c.source, Kind: ErrorUnavailable,
			Code: "source.target_assertion_not_configured", Temporary: false}
	}
	started := time.Now()
	release, err := c.begin(ctx, workloadQuery)
	if err != nil {
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceCall(string(c.source), "summary", time.Since(started), err)
		}
		return nil, err
	}
	defer func() {
		release()
		c.finish(workloadQuery, resultErr)
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceCall(string(c.source), "summary", time.Since(started), resultErr)
		}
	}()

	result, err = c.fetchSummary(ctx, req)
	return result, err
}

func (c *JSONSummaryConnector) fetchSummary(ctx context.Context, req SummaryRequest) (*Summary, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	response, err := c.doHTTP(ctx, func() (*http.Request, error) {
		// Build a new request for every attempt. The assertion carries a one-time
		// jti, so reusing a cloned request would make an otherwise safe retry a
		// replay at Aero IM.
		return c.newSummaryRequest(ctx, req)
	})
	if err != nil {
		return nil, c.summaryRequestError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, remoteHTTPError(c.source, response.StatusCode)
	}
	result, err := c.decodeSummary(ctx, response.Body)
	if err != nil {
		return nil, err
	}
	if expected := strings.TrimSpace(req.Region); expected != "" && result.SourceRegion != expected {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.region_mismatch"}
	}
	requested := requestedSummaryDatasets(c.source, req.Datasets)
	markMissingRequestedDatasets(result, requested)
	if err := normalizeSummaryCoverage(result, requested); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *JSONSummaryConnector) summaryRequestError(ctx context.Context, err error) error {
	if remote, ok := IsRemote(err); ok {
		return remote
	}
	kind := ErrorUnavailable
	if requestTimedOut(ctx, err) {
		kind = ErrorTimeout
	}
	return &RemoteError{Source: c.source, Kind: kind, Code: "source.request_failed", Temporary: true, Cause: redactedRemoteCause(err)}
}
