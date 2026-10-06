package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type AuditLedgerService interface {
	Record(context.Context, AuditFactInput) error
	Log(context.Context, *domain.AuditEvent) error
	List(context.Context, domain.AuditFactFilter) ([]*domain.AuditFact, string, error)
	VerifyChain(context.Context, string, string) error
}

type AuditPublisher interface {
	Publish(context.Context, *domain.AuditFact) error
}

type AuditFactInput struct {
	Source        string
	SourceRegion  string
	PartitionKey  string
	EventID       domain.ID
	OperationID   domain.ID
	CorrelationID domain.ID
	CausationID   domain.ID
	TraceID       string
	RequestID     string
	ActorUID      string
	AccountID     domain.ID
	TenantID      string
	Action        string
	ResourceType  string
	ResourceID    string
	OccurredAt    time.Time
	Payload       map[string]any
}

type auditLedgerService struct {
	repo    repository.AuditLedgerRepository
	metrics ControlPlaneMetrics
}

// NewAuditLedgerService builds the standalone-append audit service. Delivery
// of audit facts is exclusively the drain worker's job (drainPublications on
// audit_publications rows); Record only appends, it never publishes.
func NewAuditLedgerService(repo repository.AuditLedgerRepository) AuditLedgerService {
	s := &auditLedgerService{repo: repo, metrics: noopControlPlaneMetrics{}}
	return s
}

// SetControlPlaneMetrics attaches the observability sink (composition root).
func (s *auditLedgerService) SetControlPlaneMetrics(metrics ControlPlaneMetrics) {
	if metrics != nil {
		s.metrics = metrics
	}
}

func (s *auditLedgerService) Record(ctx context.Context, input AuditFactInput) error {
	fact := buildAuditFact(ctx, input)
	if err := s.repo.Append(ctx, fact); err != nil {
		if errors.Is(err, repository.ErrAuditDuplicate) {
			// The standalone path completed: the append folded on the dedup key,
			// which is a successful execution of the out-of-tx surface (the
			// retry re-derived the identical fact). Count it like a success —
			// failures keep observing ObserveAuditIngestFailure instead.
			if s.metrics != nil {
				s.metrics.ObserveAuditStandaloneRecorded(strings.TrimSpace(input.Action))
			}
			return nil
		}
		if s.metrics != nil {
			s.metrics.ObserveAuditIngestFailure(strings.TrimSpace(input.Source))
		}
		return apperr.Unavailable("audit.ingest_failed", "audit ledger append failed").Wrap(err)
	}
	if s.metrics != nil {
		s.metrics.ObserveAuditStandaloneRecorded(strings.TrimSpace(input.Action))
	}
	return nil
}

func buildAuditFact(ctx context.Context, input AuditFactInput) *domain.AuditFact {
	input = bindAuditContext(ctx, input)
	input = normalizeAuditInput(input)
	fact := &domain.AuditFact{ID: idgen.NewID(), Source: input.Source, SourceRegion: input.SourceRegion,
		PartitionKey: input.PartitionKey, EventID: input.EventID, OperationID: input.OperationID,
		CorrelationID: input.CorrelationID, CausationID: input.CausationID, TraceID: input.TraceID, RequestID: input.RequestID,
		ActorUID: input.ActorUID, AccountID: input.AccountID, TenantID: input.TenantID, Action: input.Action,
		ResourceType: input.ResourceType, ResourceID: input.ResourceID, OccurredAt: input.OccurredAt,
		Payload: redactAuditPayload(input.Payload)}
	if fact.RequestID == "" {
		fact.RequestID = RequestIDFrom(ctx)
	}
	return fact
}

func bindAuditContext(ctx context.Context, input AuditFactInput) AuditFactInput {
	if tenantID := TenantFrom(ctx); tenantID != "" {
		input.TenantID = tenantID
	}
	if input.ActorUID == "" {
		input.ActorUID = actorFromContext(ctx)
	}
	if input.TraceID == "" {
		input.TraceID = TraceIDFrom(ctx)
	}
	return input
}

func normalizeAuditInput(input AuditFactInput) AuditFactInput {
	if input.Source == "" {
		input.Source = "aero-id"
	}
	if input.SourceRegion == "" {
		input.SourceRegion = "local"
	}
	if input.PartitionKey == "" {
		input.PartitionKey = input.AccountID.String()
		if input.PartitionKey == "00000000-0000-0000-0000-000000000000" {
			input.PartitionKey = input.ActorUID
		}
		if input.PartitionKey == "" {
			input.PartitionKey = "system"
		}
	}
	if input.TenantID != "" {
		input.PartitionKey = auditTenantPartition(input.TenantID, input.PartitionKey)
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = time.Now().UTC()
	}
	return input
}

// Log adapts the legacy fire-and-forget audit interface to the durable ledger.
func (s *auditLedgerService) Log(ctx context.Context, legacy *domain.AuditEvent) error {
	if legacy == nil {
		return nil
	}
	return s.Record(ctx, AuditFactInput{ActorUID: legacy.ActorUID, Action: legacy.Action,
		ResourceType: legacy.SubjectType, ResourceID: legacy.SubjectID, EventID: legacy.ID, Payload: legacy.Meta, OccurredAt: legacy.CreatedAt})
}

func (s *auditLedgerService) List(ctx context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, "", err
	}
	filter.TenantID = tenantID
	facts, next, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, "", apperr.Unavailable("audit.list_failed", "audit ledger query failed").Wrap(err)
	}
	for i, fact := range facts {
		facts[i] = redactedAuditFact(fact)
	}
	if err := s.Record(ctx, auditReadFactInput(tenantID, filter)); err != nil {
		return nil, "", err
	}
	return facts, next, nil
}

func (s *auditLedgerService) VerifyChain(ctx context.Context, source, partition string) error {
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return err
	}
	if err := s.repo.VerifyChain(ctx, source, auditTenantPartition(tenantID, partition)); err != nil {
		return apperr.Unavailable("audit.verify_failed", "audit chain verification failed").Wrap(err)
	}
	if err := s.Record(ctx, AuditFactInput{Source: "aero-id", TenantID: tenantID, Action: "audit.chain.verified",
		ResourceType: "audit_chain", ResourceID: auditTenantPartition(tenantID, partition),
		Payload: map[string]any{"source": source}}); err != nil {
		return err
	}
	return nil
}

func auditReadFactInput(tenantID string, filter domain.AuditFactFilter) AuditFactInput {
	return AuditFactInput{Source: "aero-id", TenantID: tenantID, Action: "audit.viewed",
		ResourceType: "audit_ledger", ResourceID: "tenant:" + tenantID,
		Payload: map[string]any{
			"action_filter":            filter.Action,
			"source_filter":            filter.Source,
			"resource_type_filter":     filter.ResourceType,
			"actor_filter_present":     strings.TrimSpace(filter.ActorUID) != "",
			"request_filter_present":   strings.TrimSpace(filter.RequestID) != "",
			"account_filter_present":   filter.AccountID != (domain.ID{}),
			"operation_filter_present": filter.OperationID != (domain.ID{}),
			"from_present":             filter.From != nil,
			"to_present":               filter.To != nil,
			"cursor_present":           strings.TrimSpace(filter.Page.Cursor) != "",
			"page_limit":               filter.Page.Limit,
		}}
}

func auditTenantPartition(tenantID, partition string) string {
	return strings.TrimSpace(tenantID) + ":" + strings.TrimSpace(partition)
}

const maxAuditPayloadBytes = 64 * 1024

func redactedAuditFact(fact *domain.AuditFact) *domain.AuditFact {
	if fact == nil {
		return nil
	}
	copy := *fact
	copy.Payload = redactAuditPayload(fact.Payload)
	return &copy
}

func redactAuditPayload(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	result := redactAuditMap(input, 0)
	encoded, err := json.Marshal(result)
	if err != nil {
		return map[string]any{"truncated": true, "reason": "unserializable"}
	}
	if len(encoded) > maxAuditPayloadBytes {
		return map[string]any{"truncated": true, "sha256": hashAuditBytes(encoded)}
	}
	return result
}

func redactAuditMap(input map[string]any, depth int) map[string]any {
	if depth >= 4 {
		return map[string]any{"truncated": true}
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		if !auditPayloadKeyAllowed(key) {
			continue
		}
		if sensitiveAuditKey(key) {
			result[key] = "[REDACTED]"
			continue
		}
		result[key] = redactAuditValue(value, depth+1)
	}
	return result
}

func redactAuditValue(value any, depth int) any {
	if depth >= 4 {
		return "[TRUNCATED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		return redactAuditMap(typed, depth)
	case []any:
		return redactAuditArray(typed, depth)
	default:
		return redactAuditScalar(typed)
	}
}

func redactAuditArray(values []any, depth int) any {
	result := make([]any, 0, len(values))
	for _, item := range values {
		result = append(result, redactAuditValue(item, depth+1))
	}
	return result
}

func redactAuditScalar(value any) any {
	if value == nil {
		return nil
	}
	if typed, ok := value.(time.Time); ok {
		return typed.UTC().Format(time.RFC3339Nano)
	}
	if typed, ok := value.(*time.Time); ok {
		return redactAuditTimePointer(typed)
	}
	if typed, ok := value.([]string); ok {
		// Strings cannot contain nested keys, so preserve this typed slice.
		return typed
	}
	if isAuditPrimitive(value) {
		return value
	}
	return "[UNSUPPORTED]"
}

func redactAuditTimePointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func isAuditPrimitive(value any) bool {
	switch reflect.TypeOf(value).Kind() {
	case reflect.Bool, reflect.String, reflect.Float32, reflect.Float64,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		return false
	}
}

var auditPayloadAllowList = map[string]struct{}{
	"_quarantine": {}, "account_id": {}, "action": {}, "action_filter": {}, "actor_uid": {}, "actor_filter_present": {},
	"aggregate_version": {}, "anchored_at": {}, "application_outcome": {}, "attempt": {}, "audit": {}, "bytes": {}, "canceled": {},
	"chain_sequence": {}, "challenge": {}, "client_id": {}, "comment_count": {}, "comment_id": {}, "command_count": {}, "commands": {},
	"complete": {}, "completed": {}, "compensation_attempt": {}, "conflict": {}, "continuation_job_id": {},
	"created_at": {}, "current_state": {},
	"cursor": {}, "cursor_present": {}, "data": {}, "dataset": {}, "datasets": {}, "delivery": {}, "dimension": {},
	"drift_count": {}, "effective_at": {}, "eligible": {}, "envelope_event_id": {}, "erase": {}, "error_code": {}, "event_count": {},
	"event_id": {}, "event_type": {}, "event_types": {}, "expected_epoch": {}, "expired": {}, "expires_at": {},
	"failed": {}, "features": {}, "fields": {}, "from_present": {}, "from_uid": {}, "generated_at": {}, "hard": {},
	"historical_operation_id": {}, "home_region": {}, "identity_kind": {}, "issuer": {}, "job_type": {}, "k": {},
	"kind": {}, "last_synced_at": {}, "limit": {}, "locale": {}, "manifest_digest": {}, "matched": {},
	"membership_scope_id": {}, "membership_scope_type": {}, "membership_source": {}, "mode": {}, "n": {},
	"note": {}, "occurred_at": {}, "one_time": {}, "operation_filter_present": {}, "operation_id": {}, "outcome": {},
	"owner_uid": {}, "page_limit": {}, "partition": {}, "payload": {}, "peer_uid": {}, "plan": {}, "preferences": {},
	"previous": {}, "previous_anchor_hash": {}, "quantity": {}, "reason": {}, "reason_code": {}, "record_hash": {},
	"reconciled": {}, "recovery": {}, "replayed_events": {}, "request_filter_present": {}, "resource_id": {},
	"required_scope": {}, "resource_type": {}, "resource_type_filter": {}, "revision": {}, "role": {}, "schema_version": {}, "scope_id": {},
	"scope_type": {}, "sha256": {}, "sidebar": {}, "signature_version": {}, "snapshot_id": {}, "snapshot_version": {},
	"source": {}, "source_account_id": {}, "source_available": {}, "source_filter": {}, "source_member_id": {},
	"source_operation_id": {}, "source_region": {}, "sources": {}, "stage": {}, "status": {}, "step_count": {},
	"step_id": {}, "step_key": {}, "steps": {}, "storage_type": {}, "subscription_id": {}, "success": {}, "sync": {},
	"target": {}, "target_id": {}, "target_region": {}, "target_type": {}, "tenant_id": {}, "terminal_attempts": {},
	"to_present": {}, "to_uid": {}, "truncated": {}, "type": {}, "updated_at": {}, "usage_bytes": {}, "version": {},
	"worker": {}, "worker_role": {}, "would_execute": {}, "target_uid": {}, "wire_event_id": {}, "wire_fingerprint": {},
}

func auditPayloadKeyAllowed(key string) bool {
	_, ok := auditPayloadAllowList[strings.ToLower(strings.ReplaceAll(key, "-", "_"))]
	return ok
}

func sensitiveAuditKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, part := range []string{"token", "cookie", "password", "secret", "mfa", "recovery", "credential",
		"private_key", "privatekey", "access_key", "accesskey", "api_key", "apikey", "jwt", "bearer",
		"object_bytes", "object_data", "message_body", "message_content"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return key == "authorization" || key == "content" || key == "body" || key == "raw"
}

func hashAuditBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
