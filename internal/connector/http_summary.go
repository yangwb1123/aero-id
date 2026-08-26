package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	Client                *http.Client
	Metrics               *metrics.Registry
}

// JSONSummaryConnector implements the small, versioned summary seam expected
// from a source service. Source-specific clients can replace it without
// changing aggregation services or public APIs.
type JSONSummaryConnector struct {
	source        Source
	cfg           HTTPConfig
	client        *http.Client
	tokens        *oauthClientCredentials
	queryPolicy   *sourceWorkloadPolicy
	commandPolicy *sourceWorkloadPolicy
	healthPolicy  *sourceWorkloadPolicy
}

const (
	maxSourceBodyBytes    int64 = 16 << 20
	maxSourceConcurrency        = 4096
	maxSourceAttempts           = 10
	maxSourceTimeout            = 5 * time.Minute
	maxSourceRetryBackoff       = time.Minute
)

func NewJSONSummaryConnector(source Source, cfg HTTPConfig) (*JSONSummaryConnector, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, fmt.Errorf("%s connector endpoint is required", source)
	}
	cfg = defaultHTTPConfig(cfg)
	if err := validateHTTPConfigBounds(cfg); err != nil {
		return nil, err
	}
	if err := validateHTTPConfigPaths(cfg); err != nil {
		return nil, err
	}
	decision, err := sourceAuthDecision(source, cfg)
	if err != nil {
		return nil, err
	}
	cfg = applySourceAuth(source, cfg, decision)
	client, tokens, err := sourceClientAndOAuth(source, cfg)
	if err != nil {
		return nil, err
	}
	if decision.Compatibility && cfg.Metrics != nil {
		cfg.Metrics.ObserveSourceAuthCompatibility(source.String(), string(decision.Mode))
	}
	limits := workloadLimits(cfg)
	return &JSONSummaryConnector{source: source, cfg: cfg, client: client, tokens: tokens,
		queryPolicy: newSourceWorkloadPolicy(limits.query), commandPolicy: newSourceWorkloadPolicy(limits.command),
		healthPolicy: newSourceWorkloadPolicy(limits.health)}, nil
}

func sourceAuthDecision(source Source, cfg HTTPConfig) (sourceauth.Decision, error) {
	decision, err := sourceauth.Evaluate(sourceauth.Config{
		Source: source.String(), Label: sourceLabel(source), Endpoint: cfg.Endpoint, Environment: cfg.Environment,
		AllowInsecureLoopback: cfg.AllowInsecureLoopback, BearerToken: cfg.BearerToken,
		OAuth: sourceauth.OAuthConfig{TokenURL: cfg.OAuth.TokenURL, ClientID: cfg.OAuth.ClientID,
			ClientSecret: cfg.OAuth.ClientSecret, Scope: cfg.OAuth.Scope, Resource: cfg.OAuth.Resource},
		TLSCertFile: cfg.TLSCertFile, TLSKeyFile: cfg.TLSKeyFile,
	})
	if err != nil {
		return sourceauth.Decision{}, fmt.Errorf("%s connector authentication: %w", source, err)
	}
	return decision, nil
}

func applySourceAuth(source Source, cfg HTTPConfig, decision sourceauth.Decision) HTTPConfig {
	if source == SourceAeroIM || source == SourceSnaplink {
		cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
		cfg.BearerToken = decision.BearerToken
	}
	return cfg
}

func sourceClientAndOAuth(source Source, cfg HTTPConfig) (*http.Client, *oauthClientCredentials, error) {
	client, err := sourceClient(cfg)
	if err != nil {
		return nil, nil, err
	}
	tokens, err := newOAuthClientCredentials(cfg.OAuth, client, cfg.Timeout)
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
		if path != "" && (!strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\r\n")) {
			return fmt.Errorf("source connector paths must be absolute and free of control characters")
		}
	}
	return nil
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
	if err := validateDatasetScope(c.source, req.Datasets); err != nil {
		return nil, err
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
	httpReq, err := c.newSummaryRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	response, err := c.doHTTP(ctx, func() (*http.Request, error) { return httpReq.Clone(ctx), nil })
	if err != nil {
		return nil, c.summaryRequestError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, remoteHTTPError(c.source, response.StatusCode)
	}
	result, err := c.decodeSummary(response.Body)
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
	if ctx.Err() != nil {
		kind = ErrorTimeout
	}
	return &RemoteError{Source: c.source, Kind: kind, Code: "source.request_failed", Temporary: true, Cause: err}
}

func (c *JSONSummaryConnector) Health(ctx context.Context) (resultErr error) {
	started := time.Now()
	release, err := c.begin(ctx, workloadHealth)
	if err != nil {
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceCall(string(c.source), "health", time.Since(started), err)
		}
		return err
	}
	defer func() {
		release()
		c.finish(workloadHealth, resultErr)
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceCall(string(c.source), "health", time.Since(started), resultErr)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	urlValue := strings.TrimRight(c.cfg.Endpoint, "/") + "/healthz"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, urlValue, nil)
	if err != nil {
		return err
	}
	response, err := c.doHTTP(ctx, func() (*http.Request, error) { return request.Clone(ctx), nil })
	if err != nil {
		if remote, ok := IsRemote(err); ok {
			return remote
		}
		return &RemoteError{Source: c.source, Kind: ErrorUnavailable, Code: "source.health_failed", Temporary: true, Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return remoteHTTPError(c.source, response.StatusCode)
	}
	return nil
}

func (c *JSONSummaryConnector) Erase(ctx context.Context, req EraseRequest) (result *CommandResult, resultErr error) {
	if err := validateDatasetScope(c.source, req.Datasets); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.idempotency_key_missing", Temporary: false}
	}
	if strings.TrimSpace(c.cfg.ErasePath) == "" {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.erase_not_configured"}
	}
	started := time.Now()
	release, err := c.beginObserved(ctx, workloadCommand, "erase", started)
	if err != nil {
		return nil, err
	}
	defer func() {
		release()
		c.finish(workloadCommand, resultErr)
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.ObserveSourceCall(string(c.source), "erase", time.Since(started), resultErr)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	httpReq, body, err := c.newEraseRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	response, err := c.doHTTP(ctx, func() (*http.Request, error) {
		return cloneEraseRequest(ctx, httpReq, body)
	})
	if err != nil {
		return nil, c.eraseRequestError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, remoteHTTPError(c.source, response.StatusCode)
	}
	return c.decodeEraseResult(response.Body)
}

func (c *JSONSummaryConnector) eraseRequestError(ctx context.Context, err error) *RemoteError {
	if remote, ok := IsRemote(err); ok {
		return remote
	}
	kind := ErrorUnavailable
	if ctx.Err() != nil {
		kind = ErrorTimeout
	}
	return &RemoteError{Source: c.source, Kind: kind, Code: "source.erase_failed", Temporary: true, Cause: err}
}

func cloneEraseRequest(ctx context.Context, template *http.Request, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, template.URL.String(), strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	for key, values := range template.Header {
		request.Header[key] = append([]string(nil), values...)
	}
	return request, nil
}

func (c *JSONSummaryConnector) decodeSummary(body io.Reader) (*Summary, error) {
	limited := io.LimitReader(body, c.cfg.MaxBodyBytes+1)
	var payload summaryPayload
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, &RemoteError{Source: c.source, Kind: ErrorUnavailable, Code: "source.response_read_failed", Temporary: true, Cause: err}
	}
	if int64(len(data)) > c.cfg.MaxBodyBytes {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.response_too_large"}
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.response_invalid", Cause: err}
	}
	return c.summaryFromPayload(&payload)
}

type summaryPayload struct {
	SourceRegion string                     `json:"source_region"`
	Version      int64                      `json:"version"`
	GeneratedAt  time.Time                  `json:"generated_at"`
	Complete     bool                       `json:"complete"`
	Coverage     *summaryCoveragePayload    `json:"coverage"`
	Datasets     map[string]json.RawMessage `json:"datasets"`
	Sources      []summarySourcePayload     `json:"sources"`
	Memberships  []summaryMembershipPayload `json:"memberships"`
}

type summaryCoveragePayload struct {
	Datasets    []string `json:"datasets"`
	Sources     bool     `json:"sources"`
	Memberships bool     `json:"memberships"`
}

type summarySourcePayload struct {
	SourceAccountID string         `json:"source_account_id"`
	ScopeType       string         `json:"scope_type"`
	ScopeID         string         `json:"scope_id"`
	Status          string         `json:"status"`
	Data            map[string]any `json:"data"`
}

type summaryMembershipPayload struct {
	ScopeType      string         `json:"scope_type"`
	ScopeID        string         `json:"scope_id"`
	SourceMemberID string         `json:"source_member_id"`
	Role           string         `json:"role"`
	Status         string         `json:"status"`
	Data           map[string]any `json:"data"`
}

func (c Source) String() string { return string(c) }

func remoteHTTPError(source Source, code int) *RemoteError {
	kind := ErrorUnavailable
	temporary := code == http.StatusTooManyRequests || code >= 500
	switch code {
	case http.StatusUnauthorized:
		kind = ErrorUnauthorized
	case http.StatusForbidden:
		kind = ErrorForbidden
	case http.StatusBadRequest, http.StatusNotFound:
		kind = ErrorInvalid
	}
	return &RemoteError{Source: source, Kind: kind, Code: fmt.Sprintf("source.http_%d", code), Temporary: temporary}
}

func timePtr(value time.Time) *time.Time { return &value }
