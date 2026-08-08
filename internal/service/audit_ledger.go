package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
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
		return err
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
	return s.repo.List(ctx, filter)
}

func (s *auditLedgerService) VerifyChain(ctx context.Context, source, partition string) error {
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return err
	}
	return s.repo.VerifyChain(ctx, source, auditTenantPartition(tenantID, partition))
}

func auditTenantPartition(tenantID, partition string) string {
	return strings.TrimSpace(tenantID) + ":" + strings.TrimSpace(partition)
}

func redactAuditPayload(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		if sensitiveAuditKey(key) {
			result[key] = "[REDACTED]"
			continue
		}
		result[key] = redactAuditValue(value, 0)
	}
	encoded, err := json.Marshal(result)
	if err == nil && len(encoded) > 64*1024 {
		return map[string]any{"truncated": true, "sha256": hashAuditBytes(encoded)}
	}
	return result
}

func redactAuditValue(value any, depth int) any {
	if depth >= 4 {
		return "[TRUNCATED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		return redactAuditPayload(typed)
	case []any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, redactAuditValue(item, depth+1))
		}
		return result
	default:
		return value
	}
}

func sensitiveAuditKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, part := range []string{"token", "cookie", "password", "secret", "mfa", "recovery", "object_bytes", "message_body", "bearer"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return key == "content" || key == "body" || key == "raw"
}

func hashAuditBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
