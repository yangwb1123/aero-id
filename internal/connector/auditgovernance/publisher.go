package auditgovernance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

const maxReceiptBytes = 64 << 10

type Publisher struct {
	endpoint           *url.URL
	sourcePrefix       string
	actorID            string
	schemaID           string
	schemaVersion      int
	dataClassification string
	retentionClass     string
	tokens             TokenSource
	client             *http.Client
}

func NewPublisher(config Config, tokens TokenSource, client *http.Client) (*Publisher, error) {
	config = defaultConfig(config)
	base, err := secureEndpoint(config.Endpoint, config.AllowInsecureLoopback)
	if err != nil || tokens == nil || !validConfig(config) || base.RawQuery != "" {
		return nil, ErrInvalidConfig
	}
	endpoint := base.JoinPath("api/v1/events")
	query := endpoint.Query()
	query.Set("wait_for", "ledgered")
	endpoint.RawQuery = query.Encode()
	return &Publisher{
		endpoint: endpoint, sourcePrefix: config.SourcePrefix, actorID: config.ActorID,
		schemaID: config.SchemaID, schemaVersion: config.SchemaVersion,
		dataClassification: config.DataClassification, retentionClass: config.RetentionClass,
		tokens: tokens, client: noRedirectClient(client, config.Timeout),
	}, nil
}

func defaultConfig(config Config) Config {
	config.Endpoint = strings.TrimSpace(config.Endpoint)
	config.SourcePrefix = strings.TrimSpace(config.SourcePrefix)
	config.ActorID = strings.TrimSpace(config.ActorID)
	config.SchemaID = strings.TrimSpace(config.SchemaID)
	config.DataClassification = strings.TrimSpace(config.DataClassification)
	config.RetentionClass = strings.TrimSpace(config.RetentionClass)
	if config.ActorID == "" {
		config.ActorID = "aero-id"
	}
	if config.SchemaID == "" {
		config.SchemaID = "aero.id.audit-fact"
	}
	if config.SchemaVersion <= 0 {
		config.SchemaVersion = 1
	}
	if config.DataClassification == "" {
		config.DataClassification = "personal"
	}
	if config.RetentionClass == "" {
		config.RetentionClass = "account_7y"
	}
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}
	return config
}

func validConfig(config Config) bool {
	return validSourcePrefix(config.SourcePrefix) && config.ActorID != "" && config.SchemaID != "" &&
		config.SchemaVersion > 0 && config.DataClassification != "" && config.RetentionClass != ""
}

func (p *Publisher) Publish(ctx context.Context, fact *domain.AuditFact) error {
	binding, err := p.bindingFor(fact)
	if err != nil {
		return err
	}
	// A scope failure is a grant/configuration problem. Probe at most once per
	// cooldown rather than refetching the same client-credentials grant on
	// every publication attempt.
	if p.tokens.BindingUnhealthy(binding) {
		return fmt.Errorf("%w: binding unhealthy", ErrTokenScopeMissing)
	}
	source, token, err := p.authorizationForBinding(ctx, binding)
	if err != nil {
		return err
	}
	request, err := p.newRequest(ctx, fact, source, token)
	if err != nil {
		return err
	}
	response, err := p.client.Do(request)
	if err != nil {
		// F1: the raw client.Do error is a *url.Error embedding the full
		// events-endpoint URL; wrap it like the sibling token path so no
		// internal endpoint information can leak through any error.
		return fmt.Errorf("%w: transport", ErrTokenUnavailable)
	}
	if response.StatusCode == http.StatusUnauthorized {
		return p.handleUnauthorized(response, binding)
	}
	if response.StatusCode == http.StatusForbidden {
		return p.handleForbidden(response, binding)
	}
	return validateReceipt(response, fact)
}

func (p *Publisher) CanPublishTenant(tenantID string) bool {
	return p.tokens != nil && p.tokens.HasBinding(tenantID)
}

// BindingUnhealthy exposes the per-tenant cooldown to the durable audit drain.
// Invalid or unbound tenants are treated as healthy here; CanPublishTenant is
// the separate binding predicate used by the same pre-filter.
func (p *Publisher) BindingUnhealthy(tenantID string) bool {
	if p.tokens == nil {
		return false
	}
	source, err := TenantSourceID(p.sourcePrefix, tenantID)
	if err != nil {
		return false
	}
	return p.tokens.BindingUnhealthy(SourceBinding{TenantID: tenantID, SourceSystem: source})
}

func (p *Publisher) authorizationForBinding(ctx context.Context, binding SourceBinding) (string, string, error) {
	token, err := p.tokens.AccessToken(ctx, binding)
	// C-B propagation: the token-endpoint scope classification must escape to
	// the caller (the service routes it to audit.binding_unavailable); every
	// other token-source failure keeps the historical collapse.
	if errors.Is(err, ErrTokenScopeMissing) {
		return "", "", err
	}
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n") {
		return "", "", ErrTokenUnavailable
	}
	return binding.SourceSystem, token, nil
}

func (p *Publisher) handleUnauthorized(response *http.Response, binding SourceBinding) error {
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxReceiptBytes))
	// A 401 rejects the bearer: invalidate it and fence any in-flight token
	// refresh before the next publication attempt.
	_ = p.tokens.Invalidate(binding)
	if scopeRejectedCode(tokenErrorCode(body)) {
		return fmt.Errorf("%w: HTTP %d", ErrTokenScopeMissing, response.StatusCode)
	}
	return fmt.Errorf("%w: HTTP %d", ErrTokenRejected, response.StatusCode)
}

func (p *Publisher) handleForbidden(response *http.Response, binding SourceBinding) error {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxReceiptBytes+1))
	if err == nil && len(body) <= maxReceiptBytes && scopeRejectedCode(tokenErrorCode(body)) {
		// A scope-coded 403 identifies an unhealthy grant, not a rejected
		// bearer. Keep the token and suppress repeated probes for a cooldown.
		p.tokens.MarkBindingUnhealthy(binding)
		return fmt.Errorf("%w: HTTP %d", ErrTokenScopeMissing, response.StatusCode)
	}
	// The body has already been bounded and closed; never expose it in the
	// generic non-accepted receipt classification.
	return fmt.Errorf("audit governance: HTTP %d", response.StatusCode)
}

func (p *Publisher) bindingFor(fact *domain.AuditFact) (SourceBinding, error) {
	if !validAuditFact(fact) {
		return SourceBinding{}, ErrInvalidEvent
	}
	source, err := TenantSourceID(p.sourcePrefix, fact.TenantID)
	if err != nil {
		return SourceBinding{}, ErrInvalidEvent
	}
	return SourceBinding{TenantID: fact.TenantID, SourceSystem: source}, nil
}

func validAuditFact(fact *domain.AuditFact) bool {
	return fact != nil && fact.ID != (domain.ID{}) && strings.TrimSpace(fact.TenantID) != "" && fact.Action != ""
}

func (p *Publisher) newRequest(
	ctx context.Context, fact *domain.AuditFact, source, token string,
) (*http.Request, error) {
	body, err := json.Marshal(p.event(fact, source))
	if err != nil {
		return nil, ErrInvalidEvent
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, ErrInvalidConfig
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	return request, nil
}

type governanceEvent struct {
	EventID            string             `json:"event_id"`
	SourceSystem       string             `json:"source_system"`
	EventType          string             `json:"event_type"`
	SchemaID           string             `json:"schema_id"`
	SchemaVersion      int                `json:"schema_version"`
	OccurredAt         time.Time          `json:"occurred_at"`
	OperationID        string             `json:"operation_id,omitempty"`
	CorrelationID      string             `json:"correlation_id,omitempty"`
	CausationID        string             `json:"causation_id,omitempty"`
	TraceID            string             `json:"trace_id,omitempty"`
	Actor              governanceActor    `json:"actor"`
	Targets            []governanceTarget `json:"targets,omitempty"`
	AggregateType      string             `json:"aggregate_type,omitempty"`
	AggregateID        string             `json:"aggregate_id,omitempty"`
	Action             string             `json:"action"`
	Outcome            string             `json:"outcome"`
	Payload            map[string]any     `json:"payload"`
	DataClassification string             `json:"data_classification"`
	RetentionClass     string             `json:"retention_class"`
	IdempotencyKey     string             `json:"idempotency_key"`
}

type governanceActor struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type governanceTarget struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (p *Publisher) event(fact *domain.AuditFact, source string) governanceEvent {
	actor := fact.ActorUID
	if actor == "" {
		actor = p.actorID
	}
	event := governanceEvent{
		EventID: fact.ID.String(), SourceSystem: source, EventType: p.schemaID,
		SchemaID: p.schemaID, SchemaVersion: p.schemaVersion, OccurredAt: fact.OccurredAt.UTC(),
		OperationID: optionalID(fact.OperationID), CorrelationID: optionalID(fact.CorrelationID),
		CausationID: optionalID(fact.CausationID), TraceID: fact.TraceID,
		Actor: governanceActor{ID: actor, Type: "principal"}, AggregateType: fact.ResourceType,
		AggregateID: fact.ResourceID, Action: fact.Action, Outcome: auditOutcome(fact.Payload),
		Payload: fact.Payload, DataClassification: p.dataClassification, RetentionClass: p.retentionClass,
		IdempotencyKey: fact.ID.String(),
	}
	if fact.ResourceType != "" && fact.ResourceID != "" {
		event.Targets = []governanceTarget{{Type: fact.ResourceType, ID: fact.ResourceID}}
	}
	return event
}

func optionalID(value domain.ID) string {
	if value == (domain.ID{}) {
		return ""
	}
	return value.String()
}

func auditOutcome(payload map[string]any) string {
	if value, ok := payload["outcome"].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	if value, ok := payload["status"].(string); ok && (strings.Contains(value, "fail") || strings.Contains(value, "error")) {
		return "failure"
	}
	return "success"
}

type receiptEnvelope struct {
	Receipt struct {
		EventID    string    `json:"event_id"`
		TenantID   string    `json:"tenant_id"`
		Status     string    `json:"status"`
		AcceptedAt time.Time `json:"accepted_at"`
		Conflict   bool      `json:"conflict"`
	} `json:"receipt"`
}

func validateReceipt(response *http.Response, fact *domain.AuditFact) error {
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxReceiptBytes))
		return fmt.Errorf("audit governance: HTTP %d", response.StatusCode)
	}
	envelope, err := decodeReceipt(response)
	if err != nil || !receiptMatches(envelope, fact) {
		return ErrInvalidReceipt
	}
	// Conflict receipts are idempotent successes: the endpoint already
	// accepted this event under the same event_id/idempotency_key. Duplicate
	// delivery is by design (outbox lease-expiry re-claim, FM-3) or follows a
	// lost response; receiptMatches already proved the conflict echoes this
	// exact fact, so acking it as success prevents a duplicated successful
	// event from settling as dead_letter.
	if envelope.Receipt.Conflict {
		return nil
	}
	switch envelope.Receipt.Status {
	case "ledgered", "indexed", "archived":
		return nil
	default:
		return ErrInvalidReceipt
	}
}

func decodeReceipt(response *http.Response) (receiptEnvelope, error) {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return receiptEnvelope{}, ErrInvalidReceipt
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxReceiptBytes+1))
	if err != nil || len(body) > maxReceiptBytes {
		return receiptEnvelope{}, ErrInvalidReceipt
	}
	var envelope receiptEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return receiptEnvelope{}, ErrInvalidReceipt
	}
	return envelope, nil
}

// receiptMatches verifies the receipt echoes the exact fact that was sent.
// Conflict is deliberately not part of the match: a conflict receipt for this
// event_id/tenant_id is the endpoint's dedup acknowledgement and is validated
// as idempotent success in validateReceipt; a conflict for any other event is
// still rejected as invalid.
func receiptMatches(envelope receiptEnvelope, fact *domain.AuditFact) bool {
	receipt := envelope.Receipt
	return receipt.EventID == fact.ID.String() && receipt.TenantID == fact.TenantID &&
		!receipt.AcceptedAt.IsZero()
}
