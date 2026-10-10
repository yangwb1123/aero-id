package service

import (
	"context"

	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/repository"
)

func (s *eventIngestService) observeEventOutcome(envelope *event.Envelope,
	mutation *event.SnapshotMutation, result repository.EventApplyResult) {
	if result.Status == repository.EventQuarantined {
		s.observeQuarantine(envelope, quarantineLifecycle, result.ErrorCode)
		return
	}
	s.metrics.ObserveEventApplied(string(mutation.Dataset))
}

func (s *eventIngestService) recordEventOutcomeAudit(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation, result repository.EventApplyResult) error {
	if result.Status != repository.EventQuarantined {
		return s.recordAudit(ctx, envelope, mutation, string(result.Status))
	}
	details := repository.EventQuarantineDetails{Stage: quarantineLifecycle}
	return s.recordAuditInput(ctx, eventAuditInputWithDetails(envelope, mutation, string(result.Status),
		result.ErrorCode, quarantineLifecycle, details))
}
