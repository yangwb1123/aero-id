package event

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

// HTTPPublisher delivers canonical envelopes to a remote region's ingest
// endpoint using the same signature scheme the ingest handler verifies
// (X-Aero-Event-Signature with timestamp-prefixed body).
type HTTPPublisher struct {
	endpoint    string
	signingKey  []byte
	bearerToken string
	client      *http.Client
}

// NewHTTPPublisher builds a publisher for remote HTTP ingest. signingKey must
// match the remote event_ingest signing_key (or per-source key for "aero-id").
func NewHTTPPublisher(endpoint string, signingKey string, client *http.Client) (*HTTPPublisher, error) {
	return newHTTPPublisher(endpoint, signingKey, "", client)
}

// NewAuthenticatedHTTPPublisher builds the production replication publisher.
// The HMAC authenticates the event body while the bearer token authenticates
// the configured aero-id service identity at the remote ingest boundary.
func NewAuthenticatedHTTPPublisher(endpoint, signingKey, bearerToken string, client *http.Client) (*HTTPPublisher, error) {
	if !validHTTPBearerToken(bearerToken) {
		return nil, fmt.Errorf("http publisher bearer token is required and must not contain control characters")
	}
	return newHTTPPublisher(endpoint, signingKey, bearerToken, client)
}

func validHTTPBearerToken(value string) bool {
	return value != "" && len(value) <= 16<<10 && value == strings.TrimSpace(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func newHTTPPublisher(endpoint, signingKey, bearerToken string, client *http.Client) (*HTTPPublisher, error) {
	endpoint = strings.TrimSpace(endpoint)
	if err := validateHTTPPublisherEndpoint(endpoint); err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(signingKey)) < 32 || signingKey != strings.TrimSpace(signingKey) {
		return nil, fmt.Errorf("http publisher signing key must be at least 32 bytes and must not be padded")
	}
	if client == nil {
		client = &http.Client{}
	}
	clone := *client
	if clone.Timeout <= 0 || clone.Timeout > 5*time.Minute {
		clone.Timeout = 10 * time.Second
	}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPPublisher{endpoint: endpoint, signingKey: []byte(signingKey), bearerToken: bearerToken, client: &clone}, nil
}

func (p *HTTPPublisher) Target() string { return "http:" + p.endpoint }

func (p *HTTPPublisher) Publish(ctx context.Context, envelope *Envelope) error {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := marshalCanonicalEnvelope(envelope)
	if err != nil {
		return err
	}
	if len(body) > 1<<20 {
		return fmt.Errorf("canonical envelope is too large")
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	message := append([]byte(timestamp+"."), body...)
	mac := hmac.New(sha256.New, p.signingKey)
	_, _ = mac.Write(message)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build publish request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if p.bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+p.bearerToken)
	}
	request.Header.Set("X-Aero-Event-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-Aero-Event-Timestamp", timestamp)
	response, err := p.client.Do(request)
	if err != nil || response == nil {
		return apperr.RemoteUnavailable()
	}
	if response.Body == nil {
		response.Body = io.NopCloser(strings.NewReader(""))
	}
	defer response.Body.Close()
	// Drain every response, but never retain or expose more than the bounded
	// diagnostic prefix. Classification is independent of body read errors.
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return classifyRemoteResponse(response.StatusCode, responseBody)
}

func classifyRemoteResponse(status int, body []byte) error {
	if status == http.StatusOK || status == http.StatusAccepted {
		return nil
	}
	if status >= 500 && status <= 599 {
		return apperr.RemoteUnavailable()
	}
	if remoteInFlight(body) {
		return apperr.Conflict("event.in_flight", "remote event is already being processed")
	}
	return apperr.RemoteRejected(status)
}

// remoteInFlight recognizes the stable machine code used by the ingest API;
// response prose is intentionally ignored so secrets can never affect errors.
func remoteInFlight(body []byte) bool {
	var response struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil {
		return false
	}
	return response.Code == "event.in_flight" || response.Error.Code == "event.in_flight"
}

func validateHTTPPublisherEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("http publisher endpoint must be a valid URL without userinfo, query, or fragment")
	}
	if parsed.Scheme != "https" && !isLoopbackHost(parsed.Hostname()) {
		return fmt.Errorf("http publisher endpoint must use https outside localhost")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
