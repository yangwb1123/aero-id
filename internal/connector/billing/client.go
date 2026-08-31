// Package billing calls Snaplink's tenantless M2M entitlement and usage API.
// The verified tenant selects one server-configured OAuth client binding.
package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/metrics"
)

const (
	entitlementPath      = "api/v1/metering/entitlement"
	reservationPath      = "api/v1/metering/reservations"
	ScopeEntitlementRead = "billing:entitlement:read"
	ScopeMeteringWrite   = "metering:write"
	billingResource      = "billing-api"
)

var (
	ErrInvalidConfig      = errors.New("billing: invalid configuration")
	ErrUnavailable        = errors.New("billing: unavailable")
	ErrUnauthorized       = errors.New("billing: source unauthorized")
	ErrQuotaExceeded      = errors.New("billing: quota exceeded")
	ErrEntitlement        = errors.New("billing: entitlement missing")
	ErrReservationExpired = errors.New("billing: reservation expired")
	ErrInvalidReply       = errors.New("billing: invalid response")
)

type BindingConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string
	Resources    []string
}

type Config struct {
	Endpoint              string
	Timeout               time.Duration
	MaxBodyBytes          int64
	AllowInsecureLoopback bool
	Bindings              map[string]BindingConfig
	// Metrics receives bounded shared OAuth-flight observations. It never
	// receives tokens, request IDs, tenant IDs, or trace IDs as labels.
	Metrics *metrics.Registry
}

type Client struct {
	base      *url.URL
	client    *http.Client
	maxBody   int64
	bindings  map[string]*tokenSource
	tenantIDs []string
}

type Reservation struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id"`
	Dimension string `json:"dimension"`
	Quantity  int64  `json:"quantity"`
	Status    string `json:"status"`
}

type ReserveCommand struct {
	ID             string `json:"id,omitempty"`
	Dimension      string `json:"dimension"`
	Quantity       int64  `json:"quantity"`
	TTLSeconds     int64  `json:"ttl_seconds"`
	IdempotencyKey string `json:"-"`
}

// NewClient constructs the Billing client. The standard net/http transport
// honors request cancellation during both RoundTrip and response-body reads,
// which is required for the configured OAuth flight bound. A caller-supplied
// RoundTripper is a trusted transport seam and must provide the same contract;
// net/http cannot forcibly stop a RoundTripper or Body that ignores context.
func NewClient(config Config, client *http.Client) (*Client, error) {
	if config.Timeout <= 0 {
		config.Timeout = defaultBillingTimeout
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = 256 << 10
	}
	base, err := secureEndpoint(config.Endpoint, config.AllowInsecureLoopback)
	if err != nil || base.RawQuery != "" || len(config.Bindings) == 0 {
		return nil, ErrInvalidConfig
	}
	result := &Client{base: base, client: noRedirectClient(client, config.Timeout), maxBody: config.MaxBodyBytes,
		bindings: make(map[string]*tokenSource, len(config.Bindings))}
	clients, secrets := map[string]struct{}{}, map[string]struct{}{}
	for tenantID, binding := range config.Bindings {
		if !validTenantID(tenantID) {
			return nil, ErrInvalidConfig
		}
		tokens, tokenErr := newTokenSource(oauthConfig{TokenURL: binding.TokenURL, ClientID: binding.ClientID,
			ClientSecret: binding.ClientSecret, Scope: binding.Scope, Resources: binding.Resources,
			Timeout: config.Timeout, AllowHTTP: config.AllowInsecureLoopback, metrics: billingTokenFlightMetrics(config.Metrics)}, client)
		if tokenErr != nil {
			return nil, tokenErr
		}
		if duplicateCredential(tokens, clients, secrets) {
			return nil, ErrInvalidConfig
		}
		result.bindings[tenantID] = tokens
		result.tenantIDs = append(result.tenantIDs, tenantID)
	}
	return result, nil
}

func billingTokenFlightMetrics(registry *metrics.Registry) tokenFlightMetrics {
	if registry == nil {
		return nil
	}
	return registry
}

func validTenantID(value string) bool {
	return value != "" && len(value) <= 256 && value == strings.TrimSpace(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func duplicateCredential(source *tokenSource, clients, secrets map[string]struct{}) bool {
	_, duplicateClient := clients[source.clientID]
	_, duplicateSecret := secrets[source.clientSecret]
	clients[source.clientID] = struct{}{}
	secrets[source.clientSecret] = struct{}{}
	return duplicateClient || duplicateSecret
}

func (client *Client) CurrentEntitlement(ctx context.Context, tenantID string) (*domain.EntitlementProjection, error) {
	var envelope struct {
		Entitlement *domain.EntitlementProjection `json:"entitlement"`
	}
	if err := client.request(ctx, tenantID, ScopeEntitlementRead, http.MethodGet, entitlementPath, "", nil, &envelope); err != nil {
		return nil, err
	}
	if envelope.Entitlement == nil || envelope.Entitlement.Validate(tenantID) != nil {
		return nil, ErrInvalidReply
	}
	envelope.Entitlement.FetchedAt = time.Now().UTC()
	return envelope.Entitlement, nil
}

func (client *Client) Reserve(ctx context.Context, tenantID string, command ReserveCommand) (*Reservation, error) {
	if !validReserveCommand(command) {
		return nil, ErrInvalidConfig
	}
	var envelope struct {
		Reservation *Reservation `json:"reservation"`
	}
	if err := client.request(ctx, tenantID, ScopeMeteringWrite, http.MethodPost, reservationPath, command.IdempotencyKey, command, &envelope); err != nil {
		return nil, err
	}
	reservation := envelope.Reservation
	if !reservationMatches(reservation, tenantID, command) {
		return nil, ErrInvalidReply
	}
	return reservation, nil
}

func validReserveCommand(command ReserveCommand) bool {
	return command.ID != "" && command.Dimension != "" && command.Quantity > 0 &&
		command.TTLSeconds > 0 && command.IdempotencyKey != ""
}

func reservationMatches(reservation *Reservation, tenantID string, command ReserveCommand) bool {
	return reservation != nil && reservation.ID == command.ID && reservation.TenantID == tenantID &&
		reservation.Dimension == command.Dimension && reservation.Quantity == command.Quantity
}

func (client *Client) Commit(ctx context.Context, tenantID, reservationID, key, factID string, metadata map[string]string) error {
	body := map[string]any{"fact_id": factID, "metadata": metadata}
	path := reservationPath + "/" + url.PathEscape(reservationID) + "/commit"
	var envelope struct {
		Reservation *Reservation `json:"reservation"`
	}
	if err := client.request(ctx, tenantID, ScopeMeteringWrite, http.MethodPost, path, key, body, &envelope); err != nil {
		return err
	}
	if envelope.Reservation == nil || envelope.Reservation.ID != reservationID ||
		envelope.Reservation.TenantID != tenantID || envelope.Reservation.Status != "committed" {
		return ErrInvalidReply
	}
	return nil
}

func (client *Client) Release(ctx context.Context, tenantID, reservationID, idempotencyKey string) error {
	path := reservationPath + "/" + url.PathEscape(reservationID)
	var envelope struct {
		Reservation *Reservation `json:"reservation"`
	}
	if !validReleaseRequest(reservationID, idempotencyKey) {
		return ErrInvalidConfig
	}
	if err := client.request(ctx, tenantID, ScopeMeteringWrite, http.MethodDelete, path, idempotencyKey, nil, &envelope); err != nil {
		return err
	}
	if envelope.Reservation == nil || envelope.Reservation.ID != reservationID ||
		envelope.Reservation.TenantID != tenantID || !releaseState(envelope.Reservation.Status) {
		return ErrInvalidReply
	}
	return nil
}

func validReleaseRequest(reservationID, idempotencyKey string) bool {
	return strings.HasPrefix(reservationID, "usr-res-") && reservationID != "usr-res-" &&
		len(reservationID) <= 256 && reservationID == strings.TrimSpace(reservationID) &&
		strings.IndexFunc(reservationID, unicode.IsControl) < 0 && idempotencyKey != "" &&
		len(idempotencyKey) <= 256 && idempotencyKey == strings.TrimSpace(idempotencyKey) &&
		strings.IndexFunc(idempotencyKey, unicode.IsControl) < 0
}

func releaseState(status string) bool {
	return status == "released" || status == "expired"
}

func (client *Client) HealthCheck(ctx context.Context) error {
	if len(client.tenantIDs) == 0 {
		return ErrUnavailable
	}
	_, err := client.CurrentEntitlement(ctx, client.tenantIDs[0])
	return err
}

func (client *Client) request(ctx context.Context, tenantID, scope, method, path, key string, input, output any) error {
	tokens := client.bindings[tenantID]
	if tokens == nil {
		return ErrUnauthorized
	}
	snapshot, err := tokens.accessTokenSnapshot(ctx, scope)
	if err != nil {
		return err
	}
	request, err := client.newRequest(ctx, method, path, key, snapshot.value, input)
	if err != nil {
		return err
	}
	response, err := client.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: transport", ErrUnavailable)
	}
	if response.StatusCode == http.StatusUnauthorized {
		return client.retryUnauthorized(ctx, tokens, scope, method, path, key, input, output, snapshot, response)
	}
	return client.decode(response, output)
}

func (client *Client) retryUnauthorized(ctx context.Context, tokens *tokenSource, scope, method, path, key string,
	input, output any, snapshot billingTokenSnapshot, response *http.Response) error {
	if !client.refreshableUnauthorized(response) {
		return ErrUnauthorized
	}
	tokens.invalidateIf(snapshot)
	replacement, err := tokens.accessTokenSnapshot(ctx, scope)
	if err != nil {
		return err
	}
	retry, err := client.newRequest(ctx, method, path, key, replacement.value, input)
	if err != nil {
		return err
	}
	response, err = client.client.Do(retry)
	if err != nil {
		return fmt.Errorf("%w: transport", ErrUnavailable)
	}
	if response.StatusCode == http.StatusUnauthorized {
		body := readBillingErrorBody(response, client.maxBody)
		if billingErrorCode(body) != "insufficient_scope" {
			tokens.invalidateIf(replacement)
		}
		return ErrUnauthorized
	}
	return client.decode(response, output)
}

func (client *Client) refreshableUnauthorized(response *http.Response) bool {
	return billingErrorCode(readBillingErrorBody(response, client.maxBody)) != "insufficient_scope"
}

func readBillingErrorBody(response *http.Response, limit int64) []byte {
	if response == nil || response.Body == nil {
		return nil
	}
	if limit < 0 {
		limit = 0
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil
	}
	return body
}

func (client *Client) newRequest(ctx context.Context, method, path, key, token string, input any) (*http.Request, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, ErrInvalidConfig
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.base.JoinPath(path).String(), body)
	if err != nil {
		return nil, ErrInvalidConfig
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	return request, nil
}

func (client *Client) decode(response *http.Response, output any) error {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, client.maxBody+1))
	if err != nil || int64(len(body)) > client.maxBody {
		return ErrInvalidReply
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return decodeBillingError(response.StatusCode, body)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return ErrInvalidReply
	}
	if json.Unmarshal(body, output) != nil {
		return ErrInvalidReply
	}
	return nil
}

func decodeBillingError(status int, body []byte) error {
	switch billingErrorCode(body) {
	case "metering_quota_exceeded":
		return ErrQuotaExceeded
	case "metering_entitlement_missing", "metering_dimension_not_allowed":
		return ErrEntitlement
	case "metering_reservation_expired":
		return ErrReservationExpired
	case "invalid_token", "insufficient_scope", "metering_source_unauthorized":
		return ErrUnauthorized
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ErrUnauthorized
	}
	return fmt.Errorf("%w: HTTP %d", ErrUnavailable, status)
}

func billingErrorCode(body []byte) string {
	var envelope struct {
		Code string `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	return envelope.Code
}

func secureEndpoint(raw string, allowLoopback bool) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !validAbsoluteEndpoint(endpoint) {
		return nil, ErrInvalidConfig
	}
	if endpoint.Scheme == "https" {
		return endpoint, nil
	}
	if !allowLoopback || !isLoopbackHTTP(endpoint) {
		return nil, ErrInvalidConfig
	}
	return endpoint, nil
}

func validAbsoluteEndpoint(endpoint *url.URL) bool {
	return endpoint.Host != "" && endpoint.User == nil && endpoint.Fragment == ""
}

func isLoopbackHTTP(endpoint *url.URL) bool {
	if endpoint.Scheme != "http" {
		return false
	}
	ip := net.ParseIP(endpoint.Hostname())
	return strings.EqualFold(endpoint.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
}

func noRedirectClient(client *http.Client, timeout time.Duration) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if clone.Timeout <= 0 {
		clone.Timeout = timeout
	}
	return &clone
}
