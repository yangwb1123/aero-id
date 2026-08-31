package billing

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
	"time"

	"golang.org/x/sync/singleflight"
)

type oauthConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string
	Resources    []string
	Timeout      time.Duration
	AllowHTTP    bool
	metrics      tokenFlightMetrics
}

type tokenSource struct {
	endpoint      *url.URL
	clientID      string
	clientSecret  string
	resource      string
	client        *http.Client
	flightTimeout time.Duration
	metrics       tokenFlightMetrics
	mu            sync.RWMutex
	tokens        map[string]cachedToken
	epochs        map[string]uint64
	generations   map[string]uint64
	flight        singleflight.Group
}

type cachedToken struct {
	value      string
	refreshAt  time.Time
	epoch      uint64
	generation uint64
}

type billingTokenSnapshot struct {
	scope      string
	value      string
	epoch      uint64
	generation uint64
}

var (
	errEpochDiscarded     = errors.New("billing: token epoch discarded")
	errTokenFlightExpired = errors.New("billing: token flight expired")
)

const (
	defaultBillingTimeout = 5 * time.Second
	maxEpochReentries     = 1
)

func newTokenSource(config oauthConfig, client *http.Client) (*tokenSource, error) {
	if config.Timeout <= 0 {
		config.Timeout = defaultBillingTimeout
	}
	endpoint, err := secureEndpoint(config.TokenURL, config.AllowHTTP)
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.Scope = strings.Join(strings.Fields(config.Scope), " ")
	if err != nil || endpoint.RawQuery != "" || config.ClientID == "" || config.ClientSecret == "" ||
		!validBillingScopes(config.Scope) || len(config.Resources) != 1 || strings.TrimSpace(config.Resources[0]) != billingResource {
		return nil, ErrInvalidConfig
	}
	return &tokenSource{endpoint: endpoint, clientID: config.ClientID, clientSecret: config.ClientSecret,
		resource: billingResource, client: noRedirectClient(client, config.Timeout), flightTimeout: config.Timeout,
		metrics: config.metrics, tokens: make(map[string]cachedToken), epochs: make(map[string]uint64),
		generations: make(map[string]uint64)}, nil
}

func validBillingScopes(value string) bool {
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return false
	}
	return fields[0] != fields[1] &&
		(fields[0] == ScopeEntitlementRead || fields[1] == ScopeEntitlementRead) &&
		(fields[0] == ScopeMeteringWrite || fields[1] == ScopeMeteringWrite)
}

func (source *tokenSource) accessToken(ctx context.Context, scope string) (string, error) {
	snapshot, err := source.accessTokenSnapshot(ctx, scope)
	return snapshot.value, err
}

func (source *tokenSource) accessTokenSnapshot(ctx context.Context, scope string) (billingTokenSnapshot, error) {
	if scope != ScopeEntitlementRead && scope != ScopeMeteringWrite {
		return billingTokenSnapshot{}, ErrInvalidConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for reentry := 0; reentry <= maxEpochReentries; reentry++ {
		snapshot, err := source.accessTokenAttempt(ctx, scope)
		if !errors.Is(err, errEpochDiscarded) {
			return snapshot, err
		}
		if reentry == maxEpochReentries {
			return billingTokenSnapshot{}, ErrUnavailable
		}
	}
	return billingTokenSnapshot{}, ErrUnavailable
}

func (source *tokenSource) accessTokenAttempt(ctx context.Context, scope string) (billingTokenSnapshot, error) {
	if snapshot, ok := source.cachedSnapshot(scope); ok {
		return snapshot, nil
	}
	epoch := source.currentEpoch(scope)
	correlation := tokenFlightCorrelationFromContext(ctx)
	result := source.flight.DoChan(tokenFlightKey(scope, epoch), func() (any, error) {
		return source.runTokenFlightWithCorrelation(scope, epoch, correlation)
	})
	return source.awaitTokenForFlight(ctx, result, scope, epoch)
}

// runTokenFlight owns the shared request lifetime. Background is deliberate:
// no caller owns a coalesced flight, so caller cancellation, deadlines, and
// request-scoped values must not leak into the credential request. The timeout
// bounds context-compliant transport and response-body work.
// runTokenFlight is retained as the context-free private entry point used by
// package tests and helpers. Normal callers use runTokenFlightWithCorrelation so
// the shared flight can be traced without retaining their context.
//
//nolint:unused // retained as a context-free package test/helper entry point.
func (source *tokenSource) runTokenFlight(scope string, epoch uint64) (any, error) {
	value, err := source.runTokenFlightWithCorrelation(scope, epoch, tokenFlightCorrelation{})
	if flight, ok := tokenFlightResultFromValue(value); ok {
		return flight.snapshot, err
	}
	return value, err
}

func tokenFlightContextError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return errTokenFlightExpired
	}
	return nil
}

//nolint:unused // retained as the context-free package test/helper entry point.
func (source *tokenSource) awaitToken(ctx context.Context, result <-chan singleflight.Result) (billingTokenSnapshot, error) {
	return source.awaitTokenForFlight(ctx, result, "", 0)
}

func (source *tokenSource) awaitTokenForFlight(ctx context.Context, result <-chan singleflight.Result,
	scope string, epoch uint64) (billingTokenSnapshot, error) {
	select {
	case <-ctx.Done():
		recordTokenWaitForFlight(ctx, scope, epoch, "canceled", tokenFlightResult{}, false)
		return billingTokenSnapshot{}, ctx.Err()
	case outcome := <-result:
		flight, hasFlight := tokenFlightResultFromValue(outcome.Val)
		if outcome.Err != nil {
			recordTokenWaitForFlight(ctx, scope, epoch, tokenWaitOutcome(outcome.Err), flight, hasFlight)
			return billingTokenSnapshot{}, outcome.Err
		}
		if !hasFlight || flight.snapshot.value == "" {
			recordTokenWaitForFlight(ctx, scope, epoch, "invalid_result", flight, hasFlight)
			return billingTokenSnapshot{}, ErrUnavailable
		}
		recordTokenWaitForFlight(ctx, scope, epoch, "success", flight, true)
		return flight.snapshot, nil
	}
}

func tokenFlightKey(scope string, epoch uint64) string {
	return fmt.Sprintf("%s\x00%d", scope, epoch)
}

func (source *tokenSource) cached(scope string) (string, bool) {
	snapshot, ok := source.cachedSnapshot(scope)
	return snapshot.value, ok
}

func (source *tokenSource) cachedSnapshot(scope string) (billingTokenSnapshot, bool) {
	source.mu.RLock()
	defer source.mu.RUnlock()
	token, found := source.tokens[scope]
	epoch := source.epochs[scope]
	if !found || token.value == "" || token.epoch != epoch || !time.Now().UTC().Before(token.refreshAt) {
		return billingTokenSnapshot{}, false
	}
	return billingTokenSnapshot{scope: scope, value: token.value, epoch: token.epoch, generation: token.generation}, true
}

func (source *tokenSource) currentEpoch(scope string) uint64 {
	source.mu.RLock()
	defer source.mu.RUnlock()
	return source.epochs[scope]
}

func (source *tokenSource) invalidateIf(snapshot billingTokenSnapshot) {
	if snapshot.scope == "" || snapshot.value == "" {
		return
	}
	source.mu.Lock()
	current, found := source.tokens[snapshot.scope]
	epoch := source.epochs[snapshot.scope]
	if found {
		if current.value != snapshot.value || current.epoch != snapshot.epoch || current.generation != snapshot.generation {
			source.mu.Unlock()
			return
		}
	} else if epoch != snapshot.epoch {
		source.mu.Unlock()
		return
	}
	delete(source.tokens, snapshot.scope)
	if source.epochs == nil {
		source.epochs = make(map[string]uint64)
	}
	source.epochs[snapshot.scope] = epoch + 1
	source.mu.Unlock()
}

func (source *tokenSource) fetchSnapshotAtEpoch(ctx context.Context, scope string, epoch uint64) (billingTokenSnapshot, error) {
	if err := tokenFlightContextError(ctx); err != nil {
		return billingTokenSnapshot{}, err
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {scope}, "resource": {source.resource}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, source.endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return billingTokenSnapshot{}, ErrUnavailable
	}
	request.SetBasicAuth(source.clientID, source.clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	injectTokenFlightTraceContext(ctx, request)
	response, err := source.client.Do(request)
	if err != nil {
		if contextErr := tokenFlightContextError(ctx); contextErr != nil {
			return billingTokenSnapshot{}, contextErr
		}
		return billingTokenSnapshot{}, fmt.Errorf("%w: token transport", ErrUnavailable)
	}
	return source.decodeSnapshotAtEpoch(ctx, response, scope, epoch)
}

func (source *tokenSource) decodeSnapshotAtEpoch(ctx context.Context, response *http.Response,
	scope string, epoch uint64) (billingTokenSnapshot, error) {
	if response == nil || response.Body == nil {
		return billingTokenSnapshot{}, ErrUnavailable
	}
	defer response.Body.Close()
	if err := tokenFlightContextError(ctx); err != nil {
		return billingTokenSnapshot{}, err
	}
	if response.StatusCode != http.StatusOK {
		if err := drainTokenResponse(ctx, response.Body); err != nil {
			return billingTokenSnapshot{}, err
		}
		return billingTokenSnapshot{}, fmt.Errorf("%w: token HTTP %d", ErrUnavailable, response.StatusCode)
	}
	wire, err := decodeTokenWireWithContext(ctx, response)
	if err != nil {
		if errors.Is(err, errTokenFlightExpired) {
			return billingTokenSnapshot{}, err
		}
		return billingTokenSnapshot{}, ErrUnavailable
	}
	snapshot, err := source.cacheSnapshotAtEpoch(ctx, scope, wire, epoch)
	if err != nil {
		return billingTokenSnapshot{}, err
	}
	return snapshot, nil
}

type tokenWire struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

//nolint:unused // retained as the context-free parsing helper for package callers.
func decodeTokenWire(response *http.Response) (tokenWire, error) {
	return decodeTokenWireWithContext(context.Background(), response)
}

func drainTokenResponse(ctx context.Context, body io.Reader) error {
	_, readErr := io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	if err := tokenFlightContextError(ctx); err != nil {
		// A response can finish draining at the same instant the flight
		// deadline fires. The deadline wins over HTTP classification.
		return err
	}
	if readErr != nil {
		return fmt.Errorf("%w: token body", ErrUnavailable)
	}
	if err := tokenFlightContextError(ctx); err != nil {
		return err
	}
	return nil
}

func decodeTokenWireWithContext(ctx context.Context, response *http.Response) (tokenWire, error) {
	if err := tokenFlightContextError(ctx); err != nil {
		return tokenWire{}, err
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return tokenWire{}, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil {
		if contextErr := tokenFlightContextError(ctx); contextErr != nil {
			return tokenWire{}, contextErr
		}
		return tokenWire{}, ErrUnavailable
	}
	if err := tokenFlightContextError(ctx); err != nil {
		return tokenWire{}, err
	}
	if len(body) > 64<<10 {
		return tokenWire{}, ErrUnavailable
	}
	var wire tokenWire
	if json.Unmarshal(body, &wire) != nil || !validTokenWire(wire) {
		return tokenWire{}, ErrUnavailable
	}
	return wire, nil
}

func validTokenWire(wire tokenWire) bool {
	return wire.AccessToken != "" && !strings.ContainsAny(wire.AccessToken, "\r\n") &&
		strings.EqualFold(wire.TokenType, "Bearer") && wire.ExpiresIn > 0
}

func (source *tokenSource) cacheTokenAtEpoch(scope string, wire tokenWire, epoch uint64) (string, bool) {
	snapshot, err := source.cacheSnapshotAtEpoch(context.Background(), scope, wire, epoch)
	return snapshot.value, err == nil
}

func (source *tokenSource) cacheSnapshotAtEpoch(ctx context.Context, scope string, wire tokenWire,
	epoch uint64) (billingTokenSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lifetime := time.Duration(min(wire.ExpiresIn, int64((24*time.Hour)/time.Second))) * time.Second
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.epochs[scope] != epoch {
		return billingTokenSnapshot{}, errEpochDiscarded
	}
	if err := tokenFlightContextError(ctx); err != nil {
		return billingTokenSnapshot{}, err
	}
	if source.tokens == nil {
		source.tokens = make(map[string]cachedToken)
	}
	if source.generations == nil {
		source.generations = make(map[string]uint64)
	}
	source.generations[scope]++
	generation := source.generations[scope]
	entry := cachedToken{value: wire.AccessToken,
		refreshAt: time.Now().UTC().Add(lifetime - min(30*time.Second, lifetime/2)),
		epoch:     epoch, generation: generation}
	source.tokens[scope] = entry
	if err := tokenFlightContextError(ctx); err != nil {
		// The lock prevents another commit from interleaving between the
		// assignment and this check. Clear only the value written by this
		// commit; never evict a newer generation.
		if source.tokens[scope] == entry {
			delete(source.tokens, scope)
		}
		return billingTokenSnapshot{}, err
	}
	return billingTokenSnapshot{scope: scope, value: wire.AccessToken, epoch: epoch, generation: generation}, nil
}
