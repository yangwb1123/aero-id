package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aero/aero-id/internal/event"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

type EventIngestService interface {
	Ingest(context.Context, *event.Envelope) (*EventIngestResult, error)
}

type EventIngestResult struct {
	EventID string `json:"event_id"`
	Status  string `json:"status"`
}

type eventIngestService struct {
	receipts       repository.EventReceiptRepository
	audit          AuditLedgerService
	allowedRegions map[string]bool
	metrics        ControlPlaneMetrics
}

// EventIngestOption configures the ingest service.
type EventIngestOption func(*eventIngestService)

// WithEventAllowedRegions sets the residency allow-list (P6-04). Events whose
// source_region is not listed are quarantined instead of applied. Empty set
// accepts any region (single-region compatibility).
func WithEventAllowedRegions(regions []string) EventIngestOption {
	return func(s *eventIngestService) {
		s.allowedRegions = make(map[string]bool, len(regions))
		for _, region := range regions {
			if strings.TrimSpace(region) != "" {
				s.allowedRegions[strings.TrimSpace(region)] = true
			}
		}
	}
}

// WithEventIngestAudit attaches the durable audit ledger.
func WithEventIngestAudit(audit AuditLedgerService) EventIngestOption {
	return func(s *eventIngestService) { s.audit = audit }
}

// WithEventIngestMetrics attaches the control-plane observability sink.
func WithEventIngestMetrics(metrics ControlPlaneMetrics) EventIngestOption {
	return func(s *eventIngestService) {
		if metrics != nil {
			s.metrics = metrics
		}
	}
}

func NewEventIngestService(receipts repository.EventReceiptRepository, opts ...EventIngestOption) EventIngestService {
	service := &eventIngestService{receipts: receipts, metrics: noopControlPlaneMetrics{}}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

func (s *eventIngestService) Ingest(ctx context.Context, envelope *event.Envelope) (*EventIngestResult, error) {
	if err := validateIngestEnvelope(envelope); err != nil {
		return nil, err
	}
	if !s.regionAllowed(envelope.SourceRegion) {
		// P6-04: residency policy — a disallowed source region is quarantined,
		// never applied to local projections (data stays in its authority region).
		_ = s.receipts.QuarantineEvent(ctx, envelope, "event.region_not_allowed")
		s.metrics.ObserveEventQuarantined(envelope.Source + "." + envelope.EventType)
		return nil, apperr.Forbidden("event.region_not_allowed",
			"event source region violates residency policy")
	}
	mutation, err := envelope.Snapshot()
	if err != nil {
		s.metrics.ObserveEventQuarantined(envelope.Source + "." + envelope.EventType)
		return nil, s.quarantineInvalidProjection(ctx, envelope, err)
	}
	result, atomicAudit, err := s.applySnapshot(ctx, envelope, mutation)
	if err != nil {
		return nil, mapEventApplyError(err)
	}
	s.metrics.ObserveEventApplied(string(mutation.Dataset))
	if result.Status == "" {
		return nil, fmt.Errorf("event apply returned empty status")
	}
	if !atomicAudit && s.audit != nil {
		if err := s.recordAudit(ctx, envelope, mutation, string(result.Status)); err != nil {
			return nil, apperr.Internal("event.audit_failed", "failed to record event audit").Wrap(err)
		}
	}
	return &EventIngestResult{EventID: envelope.EventID.String(), Status: string(result.Status)}, nil
}

func (s *eventIngestService) regionAllowed(sourceRegion string) bool {
	if len(s.allowedRegions) == 0 {
		return true // no residency policy configured
	}
	return s.allowedRegions[sourceRegion]
}

func validateIngestEnvelope(envelope *event.Envelope) error {
	if envelope == nil || strings.TrimSpace(envelope.TenantID) == "" {
		return apperr.Forbidden("event.tenant_required", "verified event tenant is required")
	}
	if err := envelope.Validate(); err != nil {
		return apperr.BadRequest("event.invalid", "event envelope is invalid").Wrap(err)
	}
	return nil
}

func (s *eventIngestService) quarantineInvalidProjection(ctx context.Context, envelope *event.Envelope, cause error) error {
	_ = s.receipts.QuarantineEvent(ctx, envelope, "event.invalid_projection")
	if err := s.recordAudit(ctx, envelope, nil, "quarantined"); err != nil {
		return apperr.Internal("event.audit_failed", "failed to record event audit").Wrap(err)
	}
	return apperr.BadRequest("event.invalid_projection", "event cannot update an approved projection").Wrap(cause)
}

func mapEventApplyError(err error) error {
	if errors.Is(err, repository.ErrEventReceiptBusy) {
		return apperr.Conflict("event.in_flight", "event is already being processed").Wrap(err)
	}
	if errors.Is(err, repository.ErrEventTenantMismatch) {
		return apperr.Forbidden("event.tenant_mismatch", "event target is not in the authenticated tenant")
	}
	return apperr.Internal("event.apply_failed", "failed to apply event").Wrap(err)
}

func (s *eventIngestService) applySnapshot(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation) (repository.EventApplyResult, bool, error) {
	if s.audit != nil {
		if applier, ok := s.receipts.(repository.EventSnapshotAuditRepository); ok {
			result, err := applier.ApplySnapshotEventWithAudit(ctx, envelope, mutation,
				buildAuditFact(ctx, eventAuditInput(envelope, mutation, "applied")))
			return result, true, err
		}
	}
	result, err := s.receipts.ApplySnapshotEvent(ctx, envelope, mutation)
	return result, false, err
}

func (s *eventIngestService) recordAudit(ctx context.Context, envelope *event.Envelope, mutation *event.SnapshotMutation, status string) error {
	if s.audit == nil {
		return nil
	}
	return s.audit.Record(ctx, eventAuditInput(envelope, mutation, status))
}

func eventAuditInput(envelope *event.Envelope, mutation *event.SnapshotMutation, status string) AuditFactInput {
	input := AuditFactInput{Source: envelope.Source, SourceRegion: envelope.SourceRegion, EventID: envelope.EventID,
		OperationID: envelope.OperationID, CorrelationID: envelope.CorrelationID, CausationID: envelope.CausationID,
		TraceID: envelope.TraceID, ActorUID: envelope.ActorUID, TenantID: envelope.TenantID, Action: "source.event",
		ResourceType: envelope.AggregateType, ResourceID: envelope.AggregateID, OccurredAt: envelope.OccurredAt,
		Payload: map[string]any{"event_type": envelope.EventType, "schema_version": envelope.SchemaVersion,
			"aggregate_version": envelope.AggregateVersion, "status": status}}
	if mutation != nil {
		input.AccountID = mutation.AccountID
	}
	return input
}
