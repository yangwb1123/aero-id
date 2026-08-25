package connector

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
)

const maxOAuthTokenResponseBytes int64 = 64 << 10

// OAuthClientCredentialsConfig configures a source-specific RFC 6749 client
// credentials grant. Resource is sent as RFC 8707's resource parameter so a
// Snaplink token cannot be replayed at a different platform service.
type OAuthClientCredentialsConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string
	Resource     string
}

type oauthClientCredentials struct {
	endpoint     *url.URL
	clientID     string
	clientSecret string
	scope        string
	resource     string
	client       *http.Client
	now          func() time.Time

	mu         sync.Mutex
	token      string
	refreshAt  time.Time
	generation uint64
	refresh    *oauthRefresh
}

type oauthRefresh struct {
	done chan struct{}
}

type oauthTokenSnapshot struct {
	value      string
	generation uint64
}

func newOAuthClientCredentials(cfg OAuthClientCredentialsConfig, client *http.Client, timeout time.Duration) (*oauthClientCredentials, error) {
	scope := strings.Join(strings.Fields(cfg.Scope), " ")
	resource := strings.TrimSpace(cfg.Resource)
	values := []string{cfg.TokenURL, cfg.ClientID, cfg.ClientSecret, scope, resource}
	configured := false
	complete := true
	for _, value := range values {
		configured = configured || strings.TrimSpace(value) != ""
		complete = complete && strings.TrimSpace(value) != ""
	}
	if !configured {
		return nil, nil
	}
	if !complete {
		return nil, errors.New("token URL, client ID, client secret, scope, and resource are required together")
	}
	endpoint, err := parseOAuthTokenEndpoint(cfg.TokenURL)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &oauthClientCredentials{
		endpoint: endpoint, clientID: strings.TrimSpace(cfg.ClientID), clientSecret: cfg.ClientSecret,
		scope: scope, resource: resource,
		client: client, now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func parseOAuthTokenEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("token URL must be an absolute URL without userinfo, query, or fragment")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && oauthLoopbackHost(endpoint.Hostname())) {
		return nil, errors.New("token URL must use HTTPS outside localhost")
	}
	return endpoint, nil
}

func oauthLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (s *oauthClientCredentials) accessToken(ctx context.Context) (string, error) {
	snapshot, err := s.accessTokenSnapshot(ctx)
	return snapshot.value, err
}

//nolint:cyclop // refresh ownership, waiter cancellation, and cache publication are one state machine.
func (s *oauthClientCredentials) accessTokenSnapshot(ctx context.Context) (oauthTokenSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.mu.Lock()
		if s.token != "" && s.now().Before(s.refreshAt) {
			snapshot := oauthTokenSnapshot{value: s.token, generation: s.generation}
			s.mu.Unlock()
			return snapshot, nil
		}
		if current := s.refresh; current != nil {
			done := current.done
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return oauthTokenSnapshot{}, ctx.Err()
			}
		}
		refresh := &oauthRefresh{done: make(chan struct{})}
		s.refresh = refresh
		s.mu.Unlock()

		wire, err := s.fetch(ctx)
		s.mu.Lock()
		if s.refresh == refresh {
			s.refresh = nil
			if err == nil {
				lifetime := time.Duration(min(wire.ExpiresIn, int64((24*time.Hour)/time.Second))) * time.Second
				skew := min(30*time.Second, lifetime/2)
				s.token = wire.AccessToken
				s.refreshAt = s.now().Add(lifetime - skew)
				s.generation++
			}
			close(refresh.done)
		}
		generation := s.generation
		s.mu.Unlock()
		if err != nil {
			return oauthTokenSnapshot{}, err
		}
		return oauthTokenSnapshot{value: wire.AccessToken, generation: generation}, nil
	}
}

func (s *oauthClientCredentials) invalidateIf(snapshot oauthTokenSnapshot) {
	if snapshot.value == "" {
		return
	}
	s.mu.Lock()
	if s.token == snapshot.value && s.generation == snapshot.generation {
		s.token = ""
		s.refreshAt = time.Time{}
		s.generation++
	}
	s.mu.Unlock()
}

func (s *oauthClientCredentials) fetch(ctx context.Context) (oauthTokenResponse, error) {
	form := url.Values{
		"grant_type": {"client_credentials"},
		"scope":      {s.scope},
		"resource":   {s.resource},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return oauthTokenResponse{}, err
	}
	request.SetBasicAuth(s.clientID, s.clientSecret)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(request)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	defer response.Body.Close()
	if err := validateOAuthTokenResponse(response); err != nil {
		return oauthTokenResponse{}, err
	}
	return decodeOAuthTokenResponse(response.Body)
}

func validateOAuthTokenResponse(response *http.Response) error {
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxOAuthTokenResponseBytes))
		return fmt.Errorf("token endpoint returned HTTP %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("token endpoint did not return application/json")
	}
	return nil
}

func decodeOAuthTokenResponse(body io.Reader) (oauthTokenResponse, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxOAuthTokenResponseBytes+1))
	if err != nil || int64(len(data)) > maxOAuthTokenResponseBytes {
		return oauthTokenResponse{}, errors.New("token response is unavailable or too large")
	}
	var wire oauthTokenResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return oauthTokenResponse{}, errors.New("token response is invalid")
	}
	if strings.TrimSpace(wire.AccessToken) == "" || strings.ContainsAny(wire.AccessToken, "\r\n") ||
		!strings.EqualFold(wire.TokenType, "Bearer") || wire.ExpiresIn <= 0 {
		return oauthTokenResponse{}, errors.New("token response is invalid")
	}
	return wire, nil
}

type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}
