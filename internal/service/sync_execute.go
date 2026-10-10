package service

import (
	"context"
	"sync"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func (s *syncJobService) executeSync(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	return s.executeSyncForSources(ctx, job, nil)
}

//nolint:cyclop,funlen // sync execution aggregates concurrent source outcomes into a durable partial result.
func (s *syncJobService) executeSyncForSources(ctx context.Context, job *domain.SyncJob, skip map[connector.Source]bool) (*domain.SyncJob, error) {
	datasets, scopeErr := parsePersistedSyncDatasets(job.Request, true)
	if scopeErr != nil {
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "sync.invalid_dataset_scope",
			"sync job requires a non-empty valid dataset scope", nil, scopeErr)
	}
	required, scopeErr := validatedDatasetsBySource(datasets)
	if scopeErr != nil {
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "sync.invalid_dataset_scope",
			"sync job requires a non-empty valid dataset scope", nil, scopeErr)
	}
	if err := s.checkSyncExecution(ctx, job, false); err != nil {
		return s.pauseSyncExecution(ctx, job, err, true, nil)
	}
	account, err := s.accountRepo.GetByID(ctx, job.TenantID, job.AccountID)
	if err != nil {
		cause := apperr.Internal("sync.account_load_failed", "failed to load account").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "sync.account_load_failed", "failed to load account", nil, cause)
	}
	result := cloneSyncResult(job.Result)
	result["mode"] = string(job.JobType)
	var wait sync.WaitGroup
	var mu sync.Mutex
	var failures int
	var incomplete int
	var unknown int
	var missing int
	var blocked error
	for source, requested := range required {
		if skip[source] {
			continue
		}
		if _, configured := s.connectors[source]; configured {
			continue
		}
		missing++
		result["sources"].(map[string]any)[string(source)] = map[string]any{
			"status": "not_configured", "error_code": "source.not_configured", "datasets": requested,
		}
	}
	for source, client := range s.connectors {
		if skip[source] {
			continue
		}
		requested := required[source]
		if len(requested) == 0 {
			continue
		}
		wait.Add(1)
		go func(source connector.Source, client connector.Connector, requested []domain.AccountDataset) {
			defer wait.Done()
			if err := s.checkSyncExecution(ctx, job, false); err != nil {
				mu.Lock()
				blocked = err
				mu.Unlock()
				return
			}
			before, listErr := s.snapshots.ListSnapshots(ctx, account.ID, requested)
			if listErr != nil {
				mu.Lock()
				failures++
				result["sources"].(map[string]any)[string(source)] = map[string]any{"status": "projection_read_failed"}
				mu.Unlock()
				return
			}
			request, requestErr := syncSummaryRequest(ctx, s.accountRepo, account, job, source, requested)
			if requestErr != nil {
				mu.Lock()
				failures++
				result["sources"].(map[string]any)[string(source)] = map[string]any{
					"status": "invalid_request", "error_code": "source.target_binding_invalid",
				}
				mu.Unlock()
				return
			}
			if err := s.checkSyncExecution(ctx, job, false); err != nil {
				mu.Lock()
				blocked = err
				result["sources"].(map[string]any)[string(source)] = map[string]any{"status": "not_started"}
				mu.Unlock()
				return
			}
			remoteCtx, remoteCancel := syncRemoteContext(ctx)
			summary, callErr := client.Summary(remoteCtx, request)
			remoteCancel()
			if callErr != nil {
				mu.Lock()
				failures++
				result["sources"].(map[string]any)[string(source)] = map[string]any{
					"status": "unavailable", "error_code": sourceErrorCode(callErr),
				}
				mu.Unlock()
				return
			}
			if summary == nil {
				mu.Lock()
				failures++
				result["sources"].(map[string]any)[string(source)] = map[string]any{"status": "invalid_response", "error_code": "source.empty_summary"}
				mu.Unlock()
				return
			}
			persistCtx, persistCancel := syncPersistenceContext(ctx)
			persistErr := s.persistSyncSummary(persistCtx, job, account, source, requested, summary)
			persistCancel()
			if persistErr != nil {
				status, code, isUnknown := summaryPersistenceStatus(persistErr)
				mu.Lock()
				if isSyncExecutionBlocked(persistErr) {
					blocked = persistErr
					mu.Unlock()
					return
				}
				if isUnknown {
					unknown++
				} else {
					failures++
				}
				result["sources"].(map[string]any)[string(source)] = map[string]any{"status": status, "error_code": code}
				mu.Unlock()
				return
			}
			statusName, partial := syncSummaryStatus(summary)
			status := map[string]any{"status": statusName, "count": len(summary.Snapshots), "complete": summaryIsComplete(summary)}
			if partial {
				status["error_code"] = "source.summary_incomplete"
			}
			if len(summary.SkippedDatasets) > 0 {
				status["skipped_datasets"] = summary.SkippedDatasets
			}
			if job.JobType == domain.SyncJobReconcile {
				drift := snapshotDrift(before, summary.Snapshots)
				status["drift_count"] = drift
				if summaryIsComplete(summary) {
					status["repaired_count"] = drift
				} else {
					status["repaired_count"] = 0
				}
			}
			mu.Lock()
			if partial {
				incomplete++
			}
			result["sources"].(map[string]any)[string(source)] = status
			mu.Unlock()
		}(source, client, requested)
	}
	wait.Wait()
	if blocked != nil {
		return s.pauseSyncExecution(ctx, job, blocked, false, result)
	}
	status := domain.SyncJobCompleted
	if unknown > 0 {
		status = domain.SyncJobUnknown
	} else if missing > 0 || failures > 0 || incomplete > 0 {
		status = domain.SyncJobPartiallyComplete
	}
	return s.finishJobAndLoad(ctx, job, status, "", "", result)
}
