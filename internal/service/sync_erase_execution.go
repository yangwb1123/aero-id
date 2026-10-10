package service

import (
	"context"
	"sync"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

//nolint:cyclop,funlen // erase execution preserves unknown remote outcomes before local deletion.
func (s *syncJobService) executeErase(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	datasets, scopeErr := parsePersistedDatasets(job.Request, true)
	if scopeErr != nil {
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "erase.invalid_dataset_scope",
			"erase job requires a non-empty valid dataset scope", nil, scopeErr)
	}
	required, scopeErr := validatedDatasetsBySource(datasets)
	if scopeErr != nil {
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "erase.invalid_dataset_scope",
			"erase job requires a non-empty valid dataset scope", nil, scopeErr)
	}
	if err := s.checkSyncExecution(ctx, job, false); err != nil {
		return s.pauseSyncExecution(ctx, job, err, true, nil)
	}
	account, err := s.accountRepo.GetByID(ctx, job.TenantID, job.AccountID)
	if err != nil {
		cause := apperr.Internal("erase.account_load_failed", "failed to load account").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "erase.account_load_failed", "failed to load account", nil, cause)
	}
	result := map[string]any{"sources": map[string]any{}}
	statuses := result["sources"].(map[string]any)
	if err := s.ensureEraseOutcomeRows(ctx, job, required); err != nil {
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobUnknown, "erase.outcome_checkpoint_failed",
			"source erase outcome could not be checkpointed", result, err)
	}
	unknown := false
	var blocked error
	failures := 0
	var wait sync.WaitGroup
	var mu sync.Mutex
	commands := make(map[connector.Source]connector.CommandConnector, len(required))
	for source := range required {
		client, configured := s.connectors[source]
		if !configured {
			statuses[string(source)] = map[string]any{"status": "failed", "error_code": "source.not_configured"}
			if err := s.checkpointEraseOutcome(ctx, job, source, "failed", "", "source.not_configured", nil); err != nil {
				statuses[string(source)].(map[string]any)["status"] = "unknown"
				statuses[string(source)].(map[string]any)["error_code"] = eraseCheckpointCode(err)
				unknown = true
			} else {
				failures++
			}
			continue
		}
		command, ok := client.(connector.CommandConnector)
		if !ok {
			statuses[string(source)] = map[string]any{"status": "failed", "error_code": "source.operation_not_configured"}
			if err := s.checkpointEraseOutcome(ctx, job, source, "failed", "", "source.operation_not_configured", nil); err != nil {
				statuses[string(source)].(map[string]any)["status"] = "unknown"
				statuses[string(source)].(map[string]any)["error_code"] = eraseCheckpointCode(err)
				unknown = true
			} else {
				failures++
			}
			continue
		}
		commands[source] = command
	}
	for source, command := range commands {
		wait.Add(1)
		go func(source connector.Source, command connector.CommandConnector, requested []domain.AccountDataset) {
			defer wait.Done()
			if err := s.checkSyncExecution(ctx, job, false); err != nil {
				mu.Lock()
				blocked = err
				statuses[string(source)] = map[string]any{"status": "pending"}
				mu.Unlock()
				return
			}
			remoteCtx, remoteCancel := syncRemoteContext(ctx)
			commandResult, callErr := command.Erase(remoteCtx, eraseRequestForJob(account, job, source, requested))
			remoteCancel()
			mu.Lock()
			defer mu.Unlock()
			if callErr != nil {
				status, outcomeUnknown, outcomeFailed := eraseCallOutcome(callErr)
				code := remoteErrorCode(callErr)
				sourceOperationID, correlated := trustedSyncEraseSourceOperationID(source, job.ID.String(), "", commandResult)
				if !correlated {
					status, outcomeUnknown, outcomeFailed, code = "unknown", true, false, "source.source_operation_id_mismatch"
				}
				var sourceResult map[string]any
				if commandResult != nil {
					sourceResult = connector.RedactSummaryData(commandResult.Result)
				}
				checkpointErr := s.checkpointEraseOutcome(ctx, job, source, eraseOutcomeStatus(status),
					sourceOperationID, code, sourceResult)
				if checkpointErr != nil {
					status, outcomeUnknown, outcomeFailed, code, sourceOperationID = "unknown", true, false, eraseCheckpointCode(checkpointErr), ""
				}
				if outcomeUnknown {
					unknown = true
				}
				if outcomeFailed {
					failures++
				}
				entry := map[string]any{"status": status, "error_code": code}
				if sourceOperationID != "" {
					entry["source_operation_id"] = sourceOperationID
				}
				statuses[string(source)] = entry
				return
			}
			if commandResult == nil {
				unknown = true
				code := "source.empty_command_result"
				if checkpointErr := s.checkpointEraseOutcome(ctx, job, source, "unknown", "", code, nil); checkpointErr != nil {
					code = eraseCheckpointCode(checkpointErr)
				}
				statuses[string(source)] = map[string]any{"status": "unknown", "error_code": code}
				return
			}
			sourceOperationID, correlated := trustedSyncEraseSourceOperationID(source, job.ID.String(), "", commandResult)
			commandStatus := normalizedCommandStatus(commandResult.Status)
			outcomeStatus := eraseOutcomeStatus(commandStatus)
			code := ""
			if !correlated {
				outcomeStatus, code, unknown = "unknown", "source.source_operation_id_mismatch", true
			} else if !commandSucceeded(commandStatus) {
				code = "source.command_failed"
			}
			if checkpointErr := s.checkpointEraseOutcome(ctx, job, source, outcomeStatus,
				sourceOperationID, code, commandResult.Result); checkpointErr != nil {
				outcomeStatus, code, unknown, sourceOperationID = "unknown", eraseCheckpointCode(checkpointErr), true, ""
			}
			entry := map[string]any{"status": outcomeStatus}
			if sourceOperationID != "" {
				entry["source_operation_id"] = sourceOperationID
			}
			if code != "" {
				entry["error_code"] = code
			}
			statuses[string(source)] = entry
			if outcomeStatus == "failed" {
				failures++
			} else if outcomeStatus == "unknown" {
				unknown = true
			}
		}(source, command, required[source])
	}
	wait.Wait()
	if blocked != nil {
		return s.pauseSyncExecution(ctx, job, blocked, false, result)
	}
	status := domain.SyncJobCompleted
	code, message := "", ""
	if unknown {
		status = domain.SyncJobUnknown
		code, message = "erase.unknown_outcome", "one or more source deletion outcomes require reconciliation"
	} else if failures > 0 {
		status = domain.SyncJobPartiallyComplete
		code, message = "erase.partial_failure", "one or more source deletion commands failed"
	} else {
		if err := s.checkSyncExecution(ctx, job, true); err != nil {
			return s.pauseSyncExecution(ctx, job, err, false, result)
		}
		persistCtx, persistCancel := syncPersistenceContext(ctx)
		err := s.eraseAccountWithLease(persistCtx, job)
		persistCancel()
		if err != nil {
			status = domain.SyncJobPartiallyComplete
			code, message = "erase.local_projection_failed", "source deletion completed but local erasure failed"
		}
	}
	return s.finishJobAndLoad(ctx, job, status, code, message, result)
}
