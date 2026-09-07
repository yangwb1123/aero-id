package connector

import (
	"context"
	"encoding/base64"
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
	"unicode"

	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

const maxOAuthTokenResponseBytes int64 = 64 << 10

// OAuthClientCredentialsConfig configures a source-specific RFC 6749 client
// credentials grant. Resource is sent as RFC 8707's resource parameter so a
// Snaplink token cannot be replayed at a different platform service.
type OAuthClientCredentialsConfig struct {
	TokenURL      string
	ClientID      string
	ClientSecret  string
	Scope         string
	Resource      string
	Environment   string
	AllowLoopback bool
	AllowedHosts  []string
	// SourceEndpoint is set by the connector constructor so an HTTP OAuth
	// endpoint cannot be enabled independently of the source loopback policy.
	SourceEndpoint string
	// OAuth TLS settings are for the token authority only; they must not be
	// inherited from the source client's TLS profile.
	TLSCAFile        string
	TLSCertFile      string
	TLSKeyFile       string
	TLSServerName    string
	RequiredScope    string
	RequiredAudience string
	RequireJWT       bool
}

type oauthClientCredentials struct {
	endpoint         *url.URL
	clientID         string
	clientSecret     string
	scope            string
	resource         string
	client           *http.Client
	now              func() time.Time
	requiredScope    string
	requiredAudience string
	requireJWT       bool

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
	endpoint, err := sourceauth.ParseOAuthTokenEndpoint(cfg.TokenURL, cfg.Environment, cfg.AllowLoopback,
		cfg.AllowedHosts, cfg.SourceEndpoint)
	if err != nil {
		return nil, err
	}
	oauthHTTPConfig := HTTPConfig{Client: client, Timeout: timeout, TLSCAFile: cfg.TLSCAFile,
		TLSCertFile: cfg.TLSCertFile, TLSKeyFile: cfg.TLSKeyFile, TLSServerName: cfg.TLSServerName}
	client, err = oauthHTTPClient(oauthHTTPConfig)
	if err != nil {
		return nil, err
	}
	allowLoopback := sourceauth.IsExplicitNonProductionLoopback(cfg.TokenURL, cfg.Environment, cfg.AllowLoopback)
	if err := restrictOAuthDestination(client, endpoint, allowLoopback); err != nil {
		return nil, err
	}
	return &oauthClientCredentials{
		endpoint: endpoint, clientID: strings.TrimSpace(cfg.ClientID), clientSecret: cfg.ClientSecret,
		scope: scope, resource: resource, client: client, now: func() time.Time { return time.Now().UTC() },
		requiredScope: strings.TrimSpace(cfg.RequiredScope), requiredAudience: strings.TrimSpace(cfg.RequiredAudience),
		requireJWT: cfg.RequireJWT,
	}, nil
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
		return oauthTokenResponse{}, redactedRemoteCause(err)
	}
	request.SetBasicAuth(s.clientID, s.clientSecret)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(request)
	if err != nil {
		if requestTimedOut(ctx, err) {
			return oauthTokenResponse{}, context.DeadlineExceeded
		}
		return oauthTokenResponse{}, redactedRemoteCause(err)
	}
	defer response.Body.Close()
	if err := validateOAuthTokenResponse(response); err != nil {
		return oauthTokenResponse{}, err
	}
	wire, err := decodeOAuthTokenResponse(ctx, response.Body)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	if !validOAuthTokenContract(wire, s.requiredScope, s.requiredAudience, s.requireJWT, s.clientID) {
		return oauthTokenResponse{}, errors.New("token response does not satisfy the source contract")
	}
	return wire, nil
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

func decodeOAuthTokenResponse(ctx context.Context, body io.Reader) (oauthTokenResponse, error) {
	data, err := readOAuthTokenBody(ctx, body)
	if err != nil {
		return oauthTokenResponse{}, err
	}
	return parseOAuthTokenBody(data)
}

func readOAuthTokenBody(ctx context.Context, body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxOAuthTokenResponseBytes+1))
	if err == nil {
		if int64(len(data)) > maxOAuthTokenResponseBytes {
			return nil, errors.New("token response is too large")
		}
		return data, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		requestTimedOut(ctx, err) || (ctx != nil && ctx.Err() != nil) {
		return nil, err
	}
	return nil, errors.New("token response is unavailable")
}

func parseOAuthTokenBody(data []byte) (oauthTokenResponse, error) {
	var wire oauthTokenResponse
	if err := json.Unmarshal(data, &wire); err != nil || !validOAuthTokenWire(wire) {
		return oauthTokenResponse{}, errors.New("token response is invalid")
	}
	return wire, nil
}

func validOAuthTokenWire(wire oauthTokenResponse) bool {
	return validOAuthAccessToken(wire.AccessToken) && strings.EqualFold(wire.TokenType, "Bearer") && wire.ExpiresIn > 0
}

const maxOAuthAccessTokenBytes = 4096

func validOAuthAccessToken(value string) bool {
	return validOAuthAccessTokenText(value) && validOAuthAccessTokenCharacters(value)
}

func validOAuthAccessTokenText(value string) bool {
	return value != "" && len(value) <= maxOAuthAccessTokenBytes && strings.TrimSpace(value) == value
}

func validOAuthAccessTokenCharacters(value string) bool {
	padding := false
	baseCharacters := 0
	for _, character := range value {
		if character > 127 || unicode.IsSpace(character) || unicode.IsControl(character) {
			return false
		}
		if character == '=' {
			padding = true
			continue
		}
		if padding || !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~+/", character) {
			return false
		}
		baseCharacters++
	}
	return baseCharacters > 0
}

func validOAuthTokenContract(wire oauthTokenResponse, requiredScope, audience string,
	requireJWT bool, clientID string) bool {
	if requiredScope != "" && wire.Scope != nil && strings.Join(strings.Fields(*wire.Scope), " ") != requiredScope {
		return false
	}
	if !requireJWT {
		return true
	}
	return validOAuthJWT(wire.AccessToken, audience, clientID, requiredScope)
}

// validOAuthJWT checks the untrusted token shape that must be present before a
// Vault request is attempted. Vault performs the cryptographic verification;
// this adapter additionally pins the RFC 9068 service-principal shape so a
// user token cannot be fetched successfully and then fail as the wrong
// PrincipalKind at the source.
func validOAuthJWT(token, audience, clientID string, requiredScopes ...string) bool {
	parts, ok := splitOAuthJWT(token)
	if !ok || audience == "" || clientID == "" {
		return false
	}
	header, ok := decodeOAuthJWTHeader(parts[0])
	if !ok || !validOAuthJWTHeader(header.Algorithm, header.Type) {
		return false
	}
	claims, raw, ok := decodeOAuthJWTClaims(parts[1])
	if !ok || !validOAuthJWTClaims(claims, audience, clientID, requiredScopes...) {
		return false
	}
	return !hasOAuthHumanClaim(raw)
}

type oauthJWTClaims struct {
	Subject  string          `json:"sub"`
	ClientID string          `json:"client_id"`
	Issuer   string          `json:"iss"`
	JTI      string          `json:"jti"`
	Scope    string          `json:"scope"`
	Audience json.RawMessage `json:"aud"`
	Expires  int64           `json:"exp"`
}

type oauthJWTHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

func splitOAuthJWT(token string) ([]string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	for _, part := range parts {
		if part == "" {
			return nil, false
		}
	}
	return parts, true
}

func decodeOAuthJWTHeader(encoded string) (oauthJWTHeader, bool) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return oauthJWTHeader{}, false
	}
	var header oauthJWTHeader
	if json.Unmarshal(data, &header) != nil {
		return oauthJWTHeader{}, false
	}
	return header, true
}

func decodeOAuthJWTClaims(encoded string) (oauthJWTClaims, map[string]json.RawMessage, bool) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return oauthJWTClaims{}, nil, false
	}
	var claims oauthJWTClaims
	if json.Unmarshal(data, &claims) != nil {
		return oauthJWTClaims{}, nil, false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return oauthJWTClaims{}, nil, false
	}
	return claims, raw, true
}

func validOAuthJWTClaims(claims oauthJWTClaims, audience, clientID string, requiredScopes ...string) bool {
	if strings.TrimSpace(claims.Subject) != clientID {
		return false
	}
	if strings.TrimSpace(claims.ClientID) != clientID {
		return false
	}
	if strings.TrimSpace(claims.Issuer) == "" || strings.TrimSpace(claims.JTI) == "" {
		return false
	}
	if !oauthJWTNotExpired(claims.Expires) || !oauthAudienceContains(claims.Audience, audience) {
		return false
	}
	return requiredOAuthScopeMatches(claims.Scope, requiredScopes...)
}

func requiredOAuthScopeMatches(scope string, requiredScopes ...string) bool {
	if len(requiredScopes) == 0 || requiredScopes[0] == "" {
		return true
	}
	return strings.Join(strings.Fields(scope), " ") == requiredScopes[0]
}

func validOAuthJWTHeader(algorithm, typ string) bool {
	if !strings.EqualFold(typ, "at+jwt") && !strings.EqualFold(typ, "application/at+jwt") {
		return false
	}
	switch strings.ToUpper(algorithm) {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EDDSA":
		return true
	default:
		return false
	}
}

func oauthJWTNotExpired(expires int64) bool {
	return expires > 0 && time.Now().Unix() <= expires+int64((30*time.Second)/time.Second)
}

func hasOAuthHumanClaim(raw map[string]json.RawMessage) bool {
	for _, name := range []string{"auth_time", "sid", "act", "may_act"} {
		if _, present := raw[name]; present {
			return true
		}
	}
	return false
}

func oauthAudienceContains(raw json.RawMessage, expected string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == expected
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return false
	}
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type oauthTokenResponse struct {
	AccessToken string  `json:"access_token"`
	TokenType   string  `json:"token_type"`
	ExpiresIn   int64   `json:"expires_in"`
	Scope       *string `json:"scope"`
}
