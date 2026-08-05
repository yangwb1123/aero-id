package auditgovernance

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
	"unicode"

	"golang.org/x/sync/singleflight"
)

const (
	maxTokenBodyBytes = 64 << 10
	auditScope        = "audit:event:write"
	auditResource     = "audit-governance"
)

type OAuthTokenSource struct {
	sourcePrefix string
	bindings     map[string]*tenantTokenSource
}

type tenantTokenSource struct {
	endpoint     *url.URL
	clientID     string
	clientSecret string
	client       *http.Client
	refreshSkew  time.Duration
	now          func() time.Time
	mu           sync.RWMutex
	token        string
	refreshAt    time.Time
	fetches      singleflight.Group
}

func NewOAuthTokenSource(config OAuthConfig, client *http.Client) (*OAuthTokenSource, error) {
	config = defaultOAuthConfig(config)
	if !validSourcePrefix(config.SourcePrefix) || len(config.Bindings) == 0 {
		return nil, ErrInvalidConfig
	}
	result := &OAuthTokenSource{sourcePrefix: config.SourcePrefix, bindings: make(map[string]*tenantTokenSource)}
	clients, secrets := map[string]struct{}{}, map[string]struct{}{}
	for tenantID, binding := range config.Bindings {
		tokens, err := newTenantTokenSource(binding, config, client)
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

func newTenantTokenSource(binding OAuthBindingConfig, config OAuthConfig, client *http.Client) (*tenantTokenSource, error) {
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
		now: func() time.Time { return time.Now().UTC() },
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

func (s *tenantTokenSource) accessToken(ctx context.Context) (string, error) {
	if token, ok := s.cachedToken(); ok {
		return token, nil
	}
	result := s.fetches.DoChan("client", func() (any, error) {
		if token, ok := s.cachedToken(); ok {
			return token, nil
		}
		return s.fetchToken(ctx)
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
			return "", ErrTokenUnavailable
		}
		return token, nil
	}
}

func (s *tenantTokenSource) cachedToken() (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token, s.token != "" && s.now().Before(s.refreshAt)
}

func (s *tenantTokenSource) fetchToken(ctx context.Context) (string, error) {
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
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxTokenBodyBytes))
		return "", fmt.Errorf("%w: HTTP %d", ErrTokenUnavailable, response.StatusCode)
	}
	wire, err := decodeTokenResponse(response)
	if err != nil {
		return "", err
	}
	s.cacheToken(wire)
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
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
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

func (s *tenantTokenSource) cacheToken(wire tokenResponse) {
	lifetime := time.Duration(min(wire.ExpiresIn, int64((24*time.Hour)/time.Second))) * time.Second
	skew := min(s.refreshSkew, lifetime/2)
	s.mu.Lock()
	s.token = wire.AccessToken
	s.refreshAt = s.now().Add(lifetime - skew)
	s.mu.Unlock()
}
