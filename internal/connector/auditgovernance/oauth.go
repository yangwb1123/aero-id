package auditgovernance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"golang.org/x/sync/singleflight"
)

const (
	maxTokenBodyBytes    = 64 << 10
	auditScope           = "audit:event:write"
	auditResource        = "audit-governance"
	scopeMissingCooldown = 5 * time.Minute
)

type OAuthTokenSource struct {
	sourcePrefix string
	bindings     map[string]*tenantTokenSource
}

type tenantTokenSource struct {
	endpoint        *url.URL
	clientID        string
	clientSecret    string
	client          *http.Client
	refreshSkew     time.Duration
	now             func() time.Time
	tenantID        string
	onScopeRejected func(string)
	hookRunning     atomic.Bool // re-entrancy guard; see accessToken
	mu              sync.RWMutex
	token           string
	refreshAt       time.Time
	epoch           uint64 // bumped by Invalidate; fences stale singleflight results
	unhealthyUntil  time.Time
	fetches         singleflight.Group
}

func NewOAuthTokenSource(config OAuthConfig, client *http.Client) (*OAuthTokenSource, error) {
	config = defaultOAuthConfig(config)
	if !validSourcePrefix(config.SourcePrefix) || len(config.Bindings) == 0 {
		return nil, ErrInvalidConfig
	}
	result := &OAuthTokenSource{sourcePrefix: config.SourcePrefix, bindings: make(map[string]*tenantTokenSource)}
	clients, secrets := map[string]struct{}{}, map[string]struct{}{}
	for tenantID, binding := range config.Bindings {
		tokens, err := newTenantTokenSource(tenantID, binding, config, client)
		if err != nil || !validTenantID(tenantID) || duplicateCredential(tokens, clients, secrets) {
			return nil, ErrInvalidConfig
		}
		result.bindings[tenantID] = tokens
	}
	return result, nil
}

func defaultOAuthConfig(config OAuthConfig) OAuthConfig {
	config.SourcePrefix = strings.TrimSpace(config.SourcePrefix)
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}
	if config.RefreshSkew <= 0 {
		config.RefreshSkew = 30 * time.Second
	}
	return config
}

func newTenantTokenSource(tenantID string, binding OAuthBindingConfig, config OAuthConfig, client *http.Client) (*tenantTokenSource, error) {
	binding.TokenURL = strings.TrimSpace(binding.TokenURL)
	binding.ClientID = strings.TrimSpace(binding.ClientID)
	binding.Scope = strings.Join(strings.Fields(binding.Scope), " ")
	endpoint, err := secureEndpoint(binding.TokenURL, config.AllowInsecureLoopback)
	if err != nil || endpoint.RawQuery != "" || !validOAuthBinding(binding) {
		return nil, ErrInvalidConfig
	}
	return &tenantTokenSource{
		endpoint: endpoint, clientID: binding.ClientID, clientSecret: binding.ClientSecret,
		client: noRedirectClient(client, config.Timeout), refreshSkew: config.RefreshSkew,
		now: func() time.Time { return time.Now().UTC() }, tenantID: tenantID,
		onScopeRejected: config.OnScopeRejected,
	}, nil
}

func validOAuthBinding(binding OAuthBindingConfig) bool {
	return binding.ClientID != "" && binding.ClientSecret != "" &&
		binding.Scope == auditScope && len(binding.Resources) == 1 &&
		strings.TrimSpace(binding.Resources[0]) == auditResource
}

func duplicateCredential(source *tenantTokenSource, clients, secrets map[string]struct{}) bool {
	if source == nil {
		return true
	}
	_, duplicateClient := clients[source.clientID]
	_, duplicateSecret := secrets[source.clientSecret]
	clients[source.clientID] = struct{}{}
	secrets[source.clientSecret] = struct{}{}
	return duplicateClient || duplicateSecret
}

func validTenantID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 256 &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func (s *OAuthTokenSource) HasBinding(tenantID string) bool {
	_, found := s.bindings[tenantID]
	return found
}

func (s *OAuthTokenSource) AccessToken(ctx context.Context, binding SourceBinding) (string, error) {
	expected, err := TenantSourceID(s.sourcePrefix, binding.TenantID)
	tokens := s.bindings[binding.TenantID]
	if err != nil || binding.SourceSystem != expected || tokens == nil {
		return "", ErrTenantUnbound
	}
	return tokens.accessToken(ctx)
}

// Invalidate drops the cached token for the binding and bumps its epoch. An
// unbound or mismatched binding is a no-op returning nil (mirrors
// AccessToken's validation). The epoch bump is unconditional for a bound
// tenant — even with an empty cache — so a pre-invalidation in-flight fetch
// can never repopulate the cache (REQ-1/REQ-2).
//
//nolint:nilerr // an unbound or mismatched binding is a documented no-op (mirrors AccessToken's validation)
func (s *OAuthTokenSource) Invalidate(binding SourceBinding) error {
	expected, err := TenantSourceID(s.sourcePrefix, binding.TenantID)
	tokens := s.bindings[binding.TenantID]
	if err != nil || binding.SourceSystem != expected || tokens == nil {
		return nil
	}
	tokens.invalidate()
	return nil
}

func (s *OAuthTokenSource) BindingUnhealthy(binding SourceBinding) bool {
	expected, err := TenantSourceID(s.sourcePrefix, binding.TenantID)
	tokens := s.bindings[binding.TenantID]
	if err != nil || binding.SourceSystem != expected || tokens == nil {
		return false
	}
	return tokens.bindingUnhealthy()
}

func (s *OAuthTokenSource) MarkBindingUnhealthy(binding SourceBinding) {
	expected, err := TenantSourceID(s.sourcePrefix, binding.TenantID)
	tokens := s.bindings[binding.TenantID]
	if err != nil || binding.SourceSystem != expected || tokens == nil {
		return
	}
	tokens.markBindingUnhealthy()
}

func (s *tenantTokenSource) bindingUnhealthy() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.now().Before(s.unhealthyUntil)
}

func (s *tenantTokenSource) markBindingUnhealthy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A 403 scope failure identifies an unhealthy grant, not a rejected
	// bearer. Retain the cached token and only suppress probes for a bounded
	// cooldown; refetching the same grant would reproduce the failure.
	s.unhealthyUntil = s.now().Add(scopeMissingCooldown)
}

func (s *tenantTokenSource) invalidate() {
	s.mu.Lock()
	s.token = ""
	s.epoch++
	s.mu.Unlock()
}

//nolint:cyclop // bounded re-entry state machine (counter-based discard/re-entry loop, REQ-2)
func (s *tenantTokenSource) accessToken(ctx context.Context) (string, error) {
	// Counter-based re-entry, bounded (REQ-2 constraint 3): a stale in-flight
	// result (errEpochDiscarded) triggers at most one re-entry against the
	// current epoch; a second consecutive discard is terminal
	// ErrTokenUnavailable — no unbounded loop under a 401 storm.
	for attempt := 0; ; attempt++ {
		if token, ok := s.cachedToken(); ok {
			return token, nil
		}
		if s.hookRunning.Load() {
			// The OnScopeRejected hook is executing on this source. Joining the
			// flight would self-join the in-flight singleflight call (x/sync has
			// no cancellation) and deadlock forever; fail fast instead.
			return "", ErrTokenUnavailable
		}
		// s.mu is never held across DoChan/wait: the flight closure's
		// cacheToken takes Lock, and a held RLock would writer-starve it into
		// a deadlock. The closure captures the keyed epoch (never re-reads
		// s.epoch) — key/epoch identity is the anti-poisoning invariant.
		s.mu.RLock()
		epoch := s.epoch
		s.mu.RUnlock()
		result := s.fetches.DoChan(fmt.Sprintf("client-%d", epoch), func() (any, error) {
			if token, ok := s.cachedToken(); ok {
				return token, nil
			}
			return s.fetchToken(ctx, epoch)
		})
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case outcome := <-result:
			if outcome.Err != nil {
				if errors.Is(outcome.Err, errEpochDiscarded) && attempt == 0 {
					continue
				}
				return "", outcome.Err
			}
			token, ok := outcome.Val.(string)
			if !ok || token == "" {
				return "", ErrTokenUnavailable
			}
			return token, nil
		}
	}
}

func (s *tenantTokenSource) cachedToken() (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token, s.token != "" && s.now().Before(s.refreshAt)
}

// fetchToken runs inside the singleflight flight closure. The 400-gated scope
// classification (C-2: token-endpoint 401/403 with any body stay
// ErrTokenUnavailable — sibling FM-4 boundary), the scope-claim validation,
// and the epoch-fenced cache write all happen here so coalesced waiters share
// one classification and one fetch.
func (s *tenantTokenSource) fetchToken(ctx context.Context, epoch uint64) (string, error) {
	request, err := s.tokenRequest(ctx)
	if err != nil {
		return "", ErrTokenUnavailable
	}
	response, err := s.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("%w: transport", ErrTokenUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxTokenBodyBytes))
		if response.StatusCode == http.StatusBadRequest && scopeRejectedCode(tokenErrorCode(body)) {
			s.notifyScopeRejected()
			// A2: the marker records this classification's token-endpoint origin
			// (C-C — the composition-root hook already counted it, so the drain
			// skips its counter); errors.Is(err, ErrTokenScopeMissing) stays true.
			return "", fmt.Errorf("%w: %w: HTTP %d", ErrTokenEndpointScopeMissing, ErrTokenScopeMissing, response.StatusCode)
		}
		return "", fmt.Errorf("%w: HTTP %d", ErrTokenUnavailable, response.StatusCode)
	}
	wire, err := decodeTokenResponse(response)
	if err != nil {
		return "", err
	}
	if !validScopeClaim(wire) {
		s.notifyScopeRejected()
		// A2: same provenance marker as the 400-gate site (see above).
		return "", fmt.Errorf("%w: %w", ErrTokenEndpointScopeMissing, ErrTokenScopeMissing)
	}
	if !s.cacheToken(wire, epoch) {
		// A pre-invalidation fetch finishing after Invalidate bumped the
		// epoch: the result is stale and must never repopulate the cache.
		return "", errEpochDiscarded
	}
	return wire.AccessToken, nil
}

func (s *tenantTokenSource) tokenRequest(ctx context.Context) (*http.Request, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {auditScope}, "resource": {auditResource}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(s.clientID, s.clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	return request, nil
}

type tokenResponse struct {
	AccessToken string  `json:"access_token"`
	TokenType   string  `json:"token_type"`
	ExpiresIn   int64   `json:"expires_in"`
	Scope       *string `json:"scope"`
}

func decodeTokenResponse(response *http.Response) (tokenResponse, error) {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return tokenResponse{}, ErrTokenUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTokenBodyBytes+1))
	if err != nil || len(body) > maxTokenBodyBytes {
		return tokenResponse{}, ErrTokenUnavailable
	}
	var wire tokenResponse
	if json.Unmarshal(body, &wire) != nil || wire.AccessToken == "" || strings.ContainsAny(wire.AccessToken, "\r\n") ||
		!strings.EqualFold(wire.TokenType, "Bearer") || wire.ExpiresIn <= 0 {
		return tokenResponse{}, ErrTokenUnavailable
	}
	return wire, nil
}

// cacheToken persists a fetched token only when it belongs to the current
// epoch; a stale result (epoch mismatch) is discarded and reported so the
// caller can re-enter against the fresh epoch (REQ-2).
func (s *tenantTokenSource) cacheToken(wire tokenResponse, epoch uint64) bool {
	lifetime := time.Duration(min(wire.ExpiresIn, int64((24*time.Hour)/time.Second))) * time.Second
	skew := min(s.refreshSkew, lifetime/2)
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch {
		return false
	}
	s.token = wire.AccessToken
	s.refreshAt = s.now().Add(lifetime - skew)
	return true
}

// notifyScopeRejected invokes the composition-root hook exactly once per
// classification, contained in-package. The hook runs inside the singleflight
// flight fn; x/sync v0.20.0 re-raises a flight panic as an unrecoverable
// process crash (go panic(e); select {}) and never delivers to coalesced
// waiters, so a panicking hook must never escape: the deferred recover
// converts it into a classified ErrTokenScopeMissing for every waiter instead.
// The re-entrancy guard (hookRunning) is armed for the hook's duration so a
// hook that calls back into this source fails fast instead of self-joining
// the in-flight flight.
func (s *tenantTokenSource) notifyScopeRejected() {
	hook := s.onScopeRejected
	if hook == nil {
		return
	}
	s.hookRunning.Store(true)
	defer s.hookRunning.Store(false)
	defer func() { _ = recover() }()
	hook(s.tenantID)
}

// tokenErrorCode is the SINGLE classifier helper for both classification
// sites (one signature set, bulkhead design §2.2): it best-effort parses
// {"error":"<code>"} from an already-drained, bounded body; any parse failure
// yields "" (non-fatal — classification never blocks the publish path).
// Status gates stay at the call sites (token endpoint: 400; events endpoint:
// 401).
func tokenErrorCode(body []byte) string {
	var envelope struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	return envelope.Error
}

func scopeRejectedCode(code string) bool {
	return code == "invalid_scope" || code == "insufficient_scope"
}

// validScopeClaim reports whether a successful token response's scope claim
// grants the requested audit scope (RFC 6749 §3.3: exact space-delimited
// token). A nil claim (absent) is accepted — the server may omit scope when
// identical to the request. A present claim must contain auditScope as an
// exact token (strings.Fields + equality, so xaudit:event:write or
// audit:event:write-extra cannot pass); a present-but-empty claim
// ("scope":"") is a rejection, not an absence — fail closed (F2). JSON null
// unmarshals to nil and is indistinguishable from absent → accepted
// (documented boundary).
func validScopeClaim(wire tokenResponse) bool {
	if wire.Scope == nil {
		return true
	}
	for _, token := range strings.Fields(*wire.Scope) {
		if token == auditScope {
			return true
		}
	}
	return false
}
