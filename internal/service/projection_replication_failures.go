package service

import (
	"context"
	stderrors "errors"
	"fmt"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

func (s *projectionReplicationService) handleReplicationPublishError(ctx context.Context, target string,
	published int64, count int, item repository.ReplicableSnapshot, envelope *event.Envelope, publishErr error) (int, error) {
	if !terminalReplicationError(publishErr) {
		return count, fmt.Errorf("publish projection: %w", publishErr)
	}
	parkedFailure, parked, transitioned, recordErr := s.recordTerminalAttempt(ctx, target, envelope, item, publishErr)
	if recordErr != nil {
		return count, fmt.Errorf("record replication terminal failure: %w", recordErr)
	}
	if !parked {
		return count, fmt.Errorf("publish projection: %w", publishErr)
	}
	if transitioned {
		observeReplicationStall(s.metrics, "terminal_rejection")
	}
	if parkedFailure == nil {
		return count, fmt.Errorf("replication terminal failure disappeared after parking")
	}
	return s.finishParkedItem(ctx, target, published, count, item, envelope, *parkedFailure)
}

func terminalReplicationError(err error) bool {
	var appErr *apperr.AppError
	if !stderrors.As(err, &appErr) || appErr.Code != "event.remote_rejected" {
		return false
	}
	return appErr.HTTPStatus() != 408 && appErr.HTTPStatus() != 425 && appErr.HTTPStatus() != 429
}

func (s *projectionReplicationService) terminalFailure(ctx context.Context, target string,
	eventID domain.ID) (*repository.ReplicationTerminalFailure, error) {
	if s.terminalFailures == nil {
		return nil, nil
	}
	failure, err := s.terminalFailures.GetTerminalFailure(ctx, s.region, target, eventID)
	if stderrors.Is(err, repository.ErrReplicationTerminalFailureNotFound) || stderrors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}
	return failure, err
}

//nolint:cyclop // the atomic repository result has distinct fencing and retry outcomes.
func (s *projectionReplicationService) recordTerminalAttempt(ctx context.Context, target string,
	envelope *event.Envelope, item repository.ReplicableSnapshot, publishErr error) (*repository.ReplicationTerminalFailure, bool, bool, error) {
	if s.terminalFailures == nil {
		return nil, false, false, nil
	}
	var appErr *apperr.AppError
	if !stderrors.As(publishErr, &appErr) {
		return nil, false, false, publishErr
	}
	failure := repository.ReplicationTerminalFailure{SourceRegion: s.region, Target: target,
		EnvelopeEventID: envelope.EventID, SnapshotID: item.Snapshot.ID, TenantID: item.TenantID,
		AccountID: item.Snapshot.AccountID, Source: item.Snapshot.Source, Dataset: item.Snapshot.Dataset,
		SnapshotVersion: item.Snapshot.Version, RemoteStatus: appErr.HTTPStatus(), ReasonCode: appErr.Code,
		TerminalCode: appErr.Code, State: "retrying"}
	var result repository.ReplicationTerminalFailure
	var parked bool
	var err error
	if fenced, ok := s.terminalFailures.(repository.ReplicationTerminalFailureFencedRepository); ok && s.leaseGeneration > 0 {
		result, parked, err = fenced.RecordTerminalAttemptOwned(ctx, failure, s.maxTerminalAttempts, s.worker, s.leaseGeneration)
	} else {
		result, parked, err = s.terminalFailures.RecordTerminalAttempt(ctx, failure, s.maxTerminalAttempts)
	}
	if err != nil {
		return nil, false, false, err
	}
	transitioned := parked
	parked = parked || result.State == "parked"
	if !parked {
		return nil, false, false, nil
	}
	return &result, true, transitioned, nil
}

func (s *projectionReplicationService) finishParkedItem(ctx context.Context, target string, published int64,
	count int, item repository.ReplicableSnapshot, envelope *event.Envelope,
	failure repository.ReplicationTerminalFailure) (int, error) {
	if err := s.recordReplicationQuarantineAudit(ctx, target, envelope, item, failure); err != nil {
		return count, err
	}
	if err := s.saveReplicationItemCursor(ctx, target, item, published+int64(count)); err != nil {
		return count, err
	}
	return count, nil
}

func (s *projectionReplicationService) recordReplicationQuarantineAudit(ctx context.Context, target string,
	envelope *event.Envelope, item repository.ReplicableSnapshot, failure repository.ReplicationTerminalFailure) error {
	if s.audit == nil {
		return fmt.Errorf("replication quarantine audit is not configured")
	}
	auditID := idgen.Anchor("projection.replication.quarantine.v1", s.region, target, envelope.EventID.String())
	// audit-site: standalone-fact — durable quarantine fact; deterministic ID retries safely.
	if err := s.audit.Record(ctx, AuditFactInput{Source: "aero-id", SourceRegion: s.region,
		EventID: auditID, AccountID: item.Snapshot.AccountID, TenantID: item.TenantID, ActorUID: "system",
		Action: domain.AuditProjectionReplicationQuarantine, ResourceType: "projection_replication",
		ResourceID: envelope.EventID.String(), Payload: map[string]any{
			"source_region": s.region, "target": target, "source": item.Snapshot.Source,
			"dataset": string(item.Snapshot.Dataset), "snapshot_id": item.Snapshot.ID.String(),
			"account_id": item.Snapshot.AccountID.String(), "tenant_id": item.TenantID,
			"snapshot_version": item.Snapshot.Version, "envelope_event_id": envelope.EventID.String(),
			"remote_status": failure.RemoteStatus, "reason_code": failure.ReasonCode,
			"terminal_attempts": failure.TerminalAttempts,
		}}); err != nil {
		return fmt.Errorf("record replication quarantine audit: %w", err)
	}
	return nil
}

func replicationFailureClass(err error) string {
	var appErr *apperr.AppError
	if stderrors.As(err, &appErr) {
		if appErr.Code == "event.remote_rejected" {
			if appErr.HTTPStatus() == 408 || appErr.HTTPStatus() == 425 || appErr.HTTPStatus() == 429 {
				return "transient"
			}
			return "terminal"
		}
		if appErr.Code == "event.remote_unavailable" || appErr.Code == "event.in_flight" {
			return "transient"
		}
	}
	return "unknown"
}

func observeReplicationStall(metrics ControlPlaneMetrics, reason string) {
	if metrics != nil {
		metrics.ObserveReplicationStall(reason)
	}
}
func observeReplicationFailure(metrics ControlPlaneMetrics, class string) {
	if metrics != nil {
		metrics.ObserveReplicationFailure(class)
	}
}
