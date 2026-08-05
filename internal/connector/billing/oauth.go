package billing

import (
	"context"
	"encoding/json"
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
}

type tokenSource struct {
	endpoint     *url.URL
	clientID     string
	clientSecret string
	resource     string
	client       *http.Client
	mu           sync.RWMutex
	tokens       map[string]cachedToken
	flight       singleflight.Group
}

type cachedToken struct {
	value     string
	refreshAt time.Time
}

func newTokenSource(config oauthConfig, client *http.Client) (*tokenSource, error) {
	endpoint, err := secureEndpoint(config.TokenURL, config.AllowHTTP)
	config.ClientID = strings.TrimSpace(config.ClientID)
	config.Scope = strings.Join(strings.Fields(config.Scope), " ")
	if err != nil || endpoint.RawQuery != "" || config.ClientID == "" || config.ClientSecret == "" ||
		!validBillingScopes(config.Scope) || len(config.Resources) != 1 || strings.TrimSpace(config.Resources[0]) != billingResource {
		return nil, ErrInvalidConfig
	}
	return &tokenSource{endpoint: endpoint, clientID: config.ClientID, clientSecret: config.ClientSecret,
		resource: billingResource, client: noRedirectClient(client, config.Timeout), tokens: make(map[string]cachedToken)}, nil
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
	if scope != ScopeEntitlementRead && scope != ScopeMeteringWrite {
		return "", ErrInvalidConfig
	}
	if token, ok := source.cached(scope); ok {
		return token, nil
	}
	result := source.flight.DoChan(scope, func() (any, error) {
		if token, ok := source.cached(scope); ok {
			return token, nil
		}
		return source.fetch(ctx, scope)
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case outcome := <-result:
		if outcome.Err != nil {
			return "", outcome.Err
		}
		token, ok := outcome.Val.(string)
		if !ok || token == "" {
			return "", ErrUnavailable
		}
		return token, nil
	}
}

func (source *tokenSource) cached(scope string) (string, bool) {
	source.mu.RLock()
	defer source.mu.RUnlock()
	token, found := source.tokens[scope]
	return token.value, found && token.value != "" && time.Now().UTC().Before(token.refreshAt)
}

func (source *tokenSource) fetch(ctx context.Context, scope string) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {scope}, "resource": {source.resource}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, source.endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", ErrUnavailable
	}
	request.SetBasicAuth(source.clientID, source.clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := source.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("%w: token transport", ErrUnavailable)
	}
	return source.decode(response, scope)
}

func (source *tokenSource) decode(response *http.Response, scope string) (string, error) {
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return "", fmt.Errorf("%w: token HTTP %d", ErrUnavailable, response.StatusCode)
	}
	wire, err := decodeTokenWire(response)
	if err != nil {
		return "", ErrUnavailable
	}
	return source.cacheToken(scope, wire), nil
}

type tokenWire struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

func decodeTokenWire(response *http.Response) (tokenWire, error) {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return tokenWire{}, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
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

func (source *tokenSource) cacheToken(scope string, wire tokenWire) string {
	lifetime := time.Duration(min(wire.ExpiresIn, int64((24*time.Hour)/time.Second))) * time.Second
	source.mu.Lock()
	source.tokens[scope] = cachedToken{value: wire.AccessToken,
		refreshAt: time.Now().UTC().Add(lifetime - min(30*time.Second, lifetime/2))}
	source.mu.Unlock()
	return wire.AccessToken
}
