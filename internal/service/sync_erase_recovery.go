package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

func (s *syncJobService) eraseAccountWithLease(ctx context.Context, job *domain.SyncJob) error {
	eraser, ok := s.accountRepo.(repository.SyncJobLeaseAccountRepository)
	if !ok {
		return fmt.Errorf("lease-fenced account erasure is unavailable")
	}
	return eraser.EraseAccountWithSyncJobLease(ctx, job.TenantID, job.AccountID, job.ID, job.LeaseOwner)
}

func (s *syncJobService) ensureEraseOutcomeRows(ctx context.Context, job *domain.SyncJob,
	required map[connector.Source][]domain.AccountDataset) error {
	recovery, ok := s.repo.(repository.SyncJobRecoveryRepository)
	if !ok {
		return nil
	}
	inputs := make([]repository.EraseOutcomeInput, 0, len(required))
	for source, datasets := range required {
		inputs = append(inputs, repository.EraseOutcomeInput{Source: string(source), RequestedDatasets: append([]domain.AccountDataset(nil), datasets...)})
	}
	persistCtx, cancel := syncPersistenceContext(ctx)
	defer cancel()
	return recovery.EnsureEraseOutcomes(syncRecoveryContext(persistCtx, job.LeaseOwner), job.ID, job.LeaseOwner, inputs)
}

func (s *syncJobService) checkpointEraseOutcome(ctx context.Context, job *domain.SyncJob, source connector.Source,
	status, operationID, code string, result map[string]any) error {
	if source == connector.SourceSnaplink && status == "completed" && strings.TrimSpace(operationID) == "" {
		// A completed Snaplink erase must carry the source-owned operation ID;
		// otherwise a legacy or malformed response could be persisted as a
		// successful deletion without a safe reconciliation target.
		return remoteUnknownError(string(source), "source.source_operation_id_missing")
	}
	recovery, ok := s.repo.(repository.SyncJobRecoveryRepository)
	if !ok {
		return nil
	}
	if result == nil {
		result = map[string]any{}
	}
	persistCtx, cancel := syncPersistenceContext(ctx)
	defer cancel()
	return recovery.CheckpointEraseOutcome(syncRecoveryContext(persistCtx, job.LeaseOwner), repository.EraseOutcomeCheckpoint{
		JobID: job.ID, Source: string(source), Worker: job.LeaseOwner, Status: status,
		SourceOperationID: operationID, ErrorCode: code, Result: connector.RedactSummaryData(result),
	})
}

func eraseCheckpointCode(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("sync.outcome_checkpoint_failed: %v", err)
}

func eraseOutcomeStatus(status string) string {
	if normalizedCommandStatus(status) == "not_configured" {
		// This is a deterministic local configuration failure, not an
		// ambiguous remote outcome. It must not remain claimable forever.
		return "failed"
	}
	return eraseSourceOutcome(status)
}

// trustedSyncEraseSourceOperationID retains only a response identifier that
// belongs to this logical erase. A mismatched identifier must not become the
// next reconciliation target, even when the source also returned an error.
//
//nolint:cyclop,funlen // legacy result import and durable outcome conversion have explicit states.
func trustedSyncEraseSourceOperationID(source connector.Source, logicalOperationID, existing string,
	result *connector.CommandResult) (string, bool) {
	retained := strings.TrimSpace(existing)
	if result == nil {
		return retained, true
	}
	returned := strings.TrimSpace(result.SourceOperationID)
	if strings.TrimSpace(result.OperationID) != "" && result.OperationID != logicalOperationID {
		return retained, false
	}
	if source == connector.SourceSnaplink && (strings.TrimSpace(result.OperationID) == "" || returned == "") {
		return retained, false
	}
	if retained != "" && returned != "" && retained != returned {
		return retained, false
	}
	if returned != "" {
		return returned, true
	}
	return retained, true
}

func (s *syncJobService) durableEraseSources(ctx context.Context, job *domain.SyncJob,
	required map[connector.Source][]domain.AccountDataset) (map[string]any, error) {
	recovery, ok := s.repo.(repository.SyncJobRecoveryRepository)
	if !ok {
		return nil, nil
	}
	if err := s.ensureEraseOutcomeRows(ctx, job, required); err != nil {
		return nil, err
	}
	outcomes, err := recovery.ListEraseOutcomes(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	// Import only legacy fields for rows still pending. Existing durable
	// terminal/unknown outcomes are authoritative and must never be
	// downgraded by a stale or partial legacy result.
	legacy, _ := job.Result["sources"].(map[string]any)
	for _, outcome := range outcomes {
		if outcome.Status != "pending" {
			continue
		}
		value, _ := legacy[outcome.Source].(map[string]any)
		status, _ := value["status"].(string)
		operationID, _ := value["source_operation_id"].(string)
		code, _ := value["error_code"].(string)
		if status == "" {
			continue
		}
		if err := s.checkpointEraseOutcome(ctx, job, connector.Source(outcome.Source), eraseOutcomeStatus(status),
			operationID, code, connector.RedactSummaryData(value)); err != nil {
			return nil, err
		}
	}
	if len(outcomes) > 0 {
		outcomes, err = recovery.ListEraseOutcomes(ctx, job.ID)
		if err != nil {
			return nil, err
		}
	}
	sources := make(map[string]any, len(outcomes))
	for _, outcome := range outcomes {
		entry := map[string]any{"status": outcome.Status}
		if outcome.SourceOperationID != "" {
			entry["source_operation_id"] = outcome.SourceOperationID
		}
		if outcome.ErrorCode != "" {
			entry["error_code"] = outcome.ErrorCode
		}
		for key, value := range outcome.Result {
			// Outcome metadata is authoritative and must not be overridden by
			// an untrusted source result with reserved field names.
			if key == "status" || key == "source_operation_id" || key == "error_code" {
				continue
			}
			entry[key] = value
		}
		sources[outcome.Source] = entry
	}
	return sources, nil
}
