package service

import (
	"context"
	"errors"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

//nolint:cyclop,funlen // recovery validates, replays, and dispatches only missing source work.
func (s *syncJobService) reconcileSync(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	datasets, scopeErr := parsePersistedSyncDatasets(job.Request, true)
	if scopeErr != nil {
		return s.recoveryFailure(ctx, job, "sync.invalid_dataset_scope", "sync job dataset scope is invalid", scopeErr)
	}
	required, scopeErr := validatedDatasetsBySource(datasets)
	if scopeErr != nil {
		return s.recoveryFailure(ctx, job, "sync.invalid_dataset_scope", "sync job dataset scope is invalid", scopeErr)
	}
	if err := s.checkSyncExecution(ctx, job, false); err != nil {
		return s.pauseSyncExecution(ctx, job, err, false, nil)
	}
	recovery, ok := s.repo.(repository.SyncJobRecoveryRepository)
	if !ok {
		cause := apperr.Unavailable("sync.recovery_unavailable", "stored summary recovery is unavailable")
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobUnknown, cause.Code, cause.Message, job.Result, cause)
	}
	replays, err := recovery.ListSummaryReplays(ctx, job.ID)
	if err != nil {
		return s.recoveryFailure(ctx, job, "sync.replay_state_invalid", "summary replay state could not be read", err)
	}
	if len(replays) == 0 && syncResultHasCommitUnknown(job.Result) {
		return s.recoveryFailure(ctx, job, "sync.summary_replay_missing", "commit-unknown summary has no recovery record", repository.ErrSyncReplayStateInvalid)
	}
	reconciler, canReconcile := s.application.(repository.SummaryApplicationReconciler)
	skip := make(map[connector.Source]bool)
	result := cloneSyncResult(job.Result)
	result["mode"] = string(job.JobType)
	for _, replay := range replays {
		if replay.State != "pending" && replay.State != "unknown" && replay.State != "resolved" {
			return s.recoveryFailure(ctx, job, "sync.replay_state_invalid", "summary replay state is invalid", repository.ErrSyncReplayStateInvalid)
		}
		if replay.JobID != job.ID || replay.Request.AccountID != job.AccountID || replay.Request.Source != replay.Source {
			return s.recoveryFailure(ctx, job, "sync.replay_state_invalid", "summary replay binding is invalid", repository.ErrSyncReplayStateInvalid)
		}
		if _, requested := required[connector.Source(replay.Source)]; !requested {
			return s.recoveryFailure(ctx, job, "sync.replay_state_invalid",
				"summary replay source is outside the job scope", repository.ErrSyncReplayStateInvalid)
		}
		if !sameIntentDatasets(replay.Request.RequestedDatasets, required[connector.Source(replay.Source)]) {
			return s.recoveryFailure(ctx, job, "sync.replay_state_invalid",
				"summary replay datasets differ from the job scope", repository.ErrSyncReplayStateInvalid)
		}
		if replay.State == "resolved" {
			skip[connector.Source(replay.Source)] = true
			continue
		}
		if !canReconcile {
			return s.recoveryFailure(ctx, job, "sync.replay_unsupported",
				"summary application cannot reconcile a stored request", repository.ErrSummaryApplicationUnavailable)
		}
		if err := s.checkSyncExecution(ctx, job, false); err != nil {
			return s.pauseSyncExecution(ctx, job, err, false, nil)
		}
		applyResult, reconcileErr := reconciler.ReconcileSummary(ctx, replay.Request)
		if reconcileErr != nil {
			var validationErr *repository.SummaryValidationError
			if errors.As(reconcileErr, &validationErr) {
				return s.recoveryFailure(ctx, job, "sync.replay_state_invalid", "stored summary replay is invalid", reconcileErr)
			}
			return s.finishJobAndLoad(ctx, job, domain.SyncJobUnknown, "projection.commit_unknown", "summary reconciliation remains unresolved", job.Result)
		}
		_, digest, digestErr := summaryRequestDigest(replay.Request)
		if digestErr != nil {
			return s.recoveryFailure(ctx, job, "sync.replay_state_invalid", "stored summary replay is invalid", digestErr)
		}
		if err := recovery.ResolveSummaryReplay(syncRecoveryContext(ctx, job.LeaseOwner), job.ID,
			replay.Source, replay.ExecutionAttempt, digest); err != nil {
			return s.finishJobAndLoad(ctx, job, domain.SyncJobUnknown, "projection.commit_unknown",
				"summary replay resolution could not be recorded", job.Result)
		}
		skip[connector.Source(replay.Source)] = true
		result["sources"].(map[string]any)[replay.Source] = map[string]any{"status": "reconciled", "outcome": applyResult.Outcome}
	}
	allSourcesRecovered := len(skip) == len(required)
	for source := range required {
		if !skip[source] {
			allSourcesRecovered = false
			break
		}
	}
	if len(skip) != 0 && allSourcesRecovered {
		// A recovery job with only stored requests is complete without contacting
		// any source. This is the important ambiguity boundary.
		return s.finishJobAndLoad(ctx, job, domain.SyncJobCompleted, "", "", result)
	}
	return s.executeSyncForSources(ctx, job, skip)
}

func (s *syncJobService) recoveryFailure(ctx context.Context, job *domain.SyncJob, code, message string, cause error) (*domain.SyncJob, error) {
	return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, code, message, job.Result, apperr.Internal(code, message).Wrap(cause))
}

func syncResultHasCommitUnknown(result map[string]any) bool {
	sources, _ := result["sources"].(map[string]any)
	for _, raw := range sources {
		value, _ := raw.(map[string]any)
		if code, _ := value["error_code"].(string); code == "projection.commit_unknown" {
			return true
		}
	}
	return false
}
