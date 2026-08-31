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

var errTokenFlightExpired = errors.New("audit governance: token flight expired")

type OAuthTokenSource struct {
	sourcePrefix string
	bindings     map[string]*tenantTokenSource
}

type tenantTokenSource struct {
	endpoint        *url.URL
	clientID        string
	clientSecret    string
	client          *http.Client
	flightTimeout   time.Duration
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
		client: noRedirectClient(client, config.Timeout), flightTimeout: config.Timeout,
		refreshSkew: config.RefreshSkew, now: func() time.Time { return time.Now().UTC() },
		tenantID: tenantID, onScopeRejected: config.OnScopeRejected,
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
	s.refreshAt = time.Time{}
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
			return s.runTokenFlight(epoch)
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

// runTokenFlight owns the lifetime of a shared token request. It is detached
// from every caller so cancellation of one waiter cannot cancel the HTTP
// request or strand the other waiters. Background is intentional: caller
// cancellation and caller-derived values are not inherited by the shared
// credential flight. DoChan's result channel is buffered, so the flight can
// finish even after all callers have stopped waiting.
func (s *tenantTokenSource) runTokenFlight(epoch uint64) (any, error) {
	flightCtx, cancel := context.WithTimeout(context.Background(), s.flightTimeout)
	defer cancel()
	if token, ok := s.cachedToken(); ok {
		return token, nil
	}
	token, err := s.fetchToken(flightCtx, epoch)
	if errors.Is(err, errTokenFlightExpired) {
		return nil, fmt.Errorf("%w: transport", ErrTokenUnavailable)
	}
	return token, err
}

// fetchToken runs inside the singleflight flight closure. The 400-gated scope
// classification (C-2: token-endpoint 401/403 with any body stay
// ErrTokenUnavailable — sibling FM-4 boundary), the scope-claim validation,
// and the epoch-fenced cache write all happen here so coalesced waiters share
// one classification and one fetch.
func (s *tenantTokenSource) fetchToken(ctx context.Context, epoch uint64) (string, error) {
	if err := tokenFlightContextError(ctx); err != nil {
		return "", err
	}
	request, err := s.tokenRequest(ctx)
	if err != nil {
		return "", ErrTokenUnavailable
	}
	response, err := s.client.Do(request)
	if err != nil {
		return "", tokenTransportError(ctx)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return s.classifyTokenResponse(ctx, response)
	}
	return s.completeToken(ctx, response, epoch)
}

func tokenTransportError(ctx context.Context) error {
	if err := tokenFlightContextError(ctx); err != nil {
		return err
	}
	return fmt.Errorf("%w: transport", ErrTokenUnavailable)
}

func (s *tenantTokenSource) classifyTokenResponse(ctx context.Context, response *http.Response) (string, error) {
	if err := tokenFlightContextError(ctx); err != nil {
		return "", err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxTokenBodyBytes+1))
	if err := tokenFlightContextError(ctx); err != nil {
		return "", err
	}
	if readErr != nil || len(body) > maxTokenBodyBytes {
		return "", ErrTokenUnavailable
	}
	if response.StatusCode != http.StatusBadRequest || !scopeRejectedCode(tokenErrorCode(body)) {
		return "", fmt.Errorf("%w: HTTP %d", ErrTokenUnavailable, response.StatusCode)
	}
	if err := s.notifyScopeRejected(ctx); err != nil {
		return "", err
	}
	// A2: the marker records this classification's token-endpoint origin
	// (C-C — the composition-root hook already counted it, so the drain
	// skips its counter); errors.Is(err, ErrTokenScopeMissing) stays true.
	return "", fmt.Errorf("%w: %w: HTTP %d", ErrTokenEndpointScopeMissing, ErrTokenScopeMissing, response.StatusCode)
}

func (s *tenantTokenSource) completeToken(ctx context.Context, response *http.Response, epoch uint64) (string, error) {
	if err := tokenFlightContextError(ctx); err != nil {
		return "", err
	}
	wire, err := decodeTokenResponse(response)
	if contextErr := tokenFlightContextError(ctx); contextErr != nil {
		return "", contextErr
	}
	if err != nil {
		return "", err
	}
	if !validScopeClaim(wire) {
		if err := s.notifyScopeRejected(ctx); err != nil {
			return "", err
		}
		// A2: same provenance marker as the 400-gate site (see above).
		return "", fmt.Errorf("%w: %w", ErrTokenEndpointScopeMissing, ErrTokenScopeMissing)
	}
	if err := s.cacheToken(wire, epoch, ctx); err != nil {
		// A pre-invalidation fetch finishing after Invalidate bumped the
		// epoch: the result is stale and must never repopulate the cache.
		return "", err
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

// cacheToken commits a fetched token only when it belongs to the current
// epoch and the shared flight is still alive. Both checks, the assignment,
// and cancellation cleanup are one mutex-protected critical section. Epoch
// mismatch wins over flight expiry so stale-flight re-entry remains intact.
func (s *tenantTokenSource) cacheToken(wire tokenResponse, epoch uint64, flightCtx context.Context) error {
	lifetime := time.Duration(min(wire.ExpiresIn, int64((24*time.Hour)/time.Second))) * time.Second
	skew := min(s.refreshSkew, lifetime/2)
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch {
		return errEpochDiscarded
	}
	if err := tokenFlightContextError(flightCtx); err != nil {
		return err
	}
	refreshAt := s.now().Add(lifetime - skew)
	s.token = wire.AccessToken
	s.refreshAt = refreshAt
	if err := tokenFlightContextError(flightCtx); err != nil {
		// The lock prevents another flight from writing between the assignment
		// and this check. Clear only the value committed by this flight.
		if s.token == wire.AccessToken && s.refreshAt.Equal(refreshAt) {
			s.token = ""
			s.refreshAt = time.Time{}
		}
		return err
	}
	return nil
}

// notifyScopeRejected invokes the composition-root hook exactly once per
// classification, contained in-package. The hook runs synchronously inside
// the singleflight flight fn; it must return promptly because a function with
// no context parameter cannot be forcibly interrupted. The flight checks its
// detached context before and after the hook, so an expiry during the hook
// takes timeout precedence over scope classification. A synchronous panic is
// recovered here and does not escape to x/sync/singleflight. The
// re-entrancy guard is armed for the hook's duration so a hook that calls back
// into this source fails fast instead of self-joining the in-flight flight.
func (s *tenantTokenSource) notifyScopeRejected(ctx context.Context) (err error) {
	if err := tokenFlightContextError(ctx); err != nil {
		return err
	}
	hook := s.onScopeRejected
	if hook == nil {
		return tokenFlightContextError(ctx)
	}
	s.hookRunning.Store(true)
	defer s.hookRunning.Store(false)
	defer func() {
		if recover() != nil {
			err = tokenFlightContextError(ctx)
		}
	}()
	hook(s.tenantID)
	return tokenFlightContextError(ctx)
}

func tokenFlightContextError(ctx context.Context) error {
	if ctx.Err() != nil {
		return errTokenFlightExpired
	}
	return nil
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
