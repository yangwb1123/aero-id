package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"gorm.io/gorm"
)

func (s *syncJobService) Reconcile(ctx context.Context, id domain.ID, worker string) (*domain.SyncJob, error) {
	job, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if job.Status != domain.SyncJobUnknown && !retryableEraseProjectionFailure(job) {
		return job, nil
	}
	// Validate erase scopes before home-region/account loading or a status
	// query. A malformed historical job is a durable local failure, not an
	// instruction to probe a source with an implicit scope.
	if job.JobType == domain.SyncJobErase {
		if scopeErr := validatePersistedSyncJobScope(job); scopeErr != nil {
			return s.failInvalidPersistedScope(ctx, id, worker, job, scopeErr)
		}
	}
	if err := requireHomeRegion(ctx, s.accountRepo, job.AccountID, s.region); err != nil {
		return nil, err
	}
	claimed, err := s.repo.ClaimUnknown(ctx, id, workerOrDefault(worker), syncLeaseDuration)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperr.Conflict("sync.not_reconcilable", "sync job is already being reconciled or is no longer unknown")
	}
	if err != nil {
		return nil, apperr.Internal("sync.reconcile_claim_failed", "failed to claim unknown sync job").Wrap(err)
	}
	return s.reconcileClaimed(ctx, claimed)
}

func (s *syncJobService) failInvalidPersistedScope(ctx context.Context, id domain.ID, worker string,
	job *domain.SyncJob, scopeErr error) (*domain.SyncJob, error) {
	claimCtx, claimCancel := syncPersistenceContext(ctx)
	claimed, claimErr := s.repo.ClaimUnknown(claimCtx, id, workerOrDefault(worker), syncLeaseDuration)
	claimCancel()
	if errors.Is(claimErr, gorm.ErrRecordNotFound) {
		return nil, apperr.Conflict("sync.not_reconcilable", "sync job is already being reconciled or is no longer unknown")
	}
	if claimErr != nil {
		return nil, apperr.Internal("sync.reconcile_claim_failed", "failed to claim unknown sync job").Wrap(claimErr)
	}
	code, message := syncJobScopeFailure(job.JobType)
	return nil, s.finishJobFailure(ctx, claimed, domain.SyncJobFailed, code, message, job.Result, scopeErr)
}

func (s *syncJobService) reconcileClaimed(ctx context.Context, claimed *domain.SyncJob) (*domain.SyncJob, error) {
	switch claimed.JobType {
	case domain.SyncJobSync, domain.SyncJobReconcile:
		return s.reconcileSync(ctx, claimed)
	case domain.SyncJobExport:
		return s.executeExport(ctx, claimed)
	case domain.SyncJobAuditExport:
		return s.executeAuditExport(ctx, claimed)
	case domain.SyncJobErase:
		return s.reconcileErase(ctx, claimed)
	default:
		return s.recoveryFailure(ctx, claimed, "sync.invalid_job_type", "sync job type cannot be recovered", errors.New("unsupported sync job type"))
	}
}

//nolint:cyclop,funlen // erase reconciliation aggregates per-source completed, failed, and unknown states.
func (s *syncJobService) reconcileErase(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	datasets, scopeErr := parsePersistedDatasets(job.Request, true)
	if scopeErr != nil {
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "erase.invalid_dataset_scope",
			"erase job requires a non-empty valid dataset scope", job.Result, scopeErr)
	}
	required, scopeErr := validatedDatasetsBySource(datasets)
	if scopeErr != nil {
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "erase.invalid_dataset_scope",
			"erase job requires a non-empty valid dataset scope", job.Result, scopeErr)
	}
	if err := s.checkSyncExecution(ctx, job, true); err != nil {
		return s.pauseSyncExecution(ctx, job, err, false, nil)
	}
	result := cloneSyncResult(job.Result)
	sources := syncResultSources(result)
	if durableSources, durableErr := s.durableEraseSources(ctx, job, required); durableErr != nil {
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobUnknown, "erase.outcome_checkpoint_failed",
			"source erase outcome could not be recovered", result, durableErr)
	} else if durableSources != nil {
		sources = durableSources
		result["sources"] = sources
	}
	unknown := len(sources) == 0
	failures := 0
	for source, rawValue := range sources {
		if len(required[connector.Source(source)]) == 0 {
			return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "erase.invalid_dataset_scope",
				"erase job contains a source outcome outside its dataset scope", job.Result, invalidSyncDatasetScope())
		}
		value, ok := rawValue.(map[string]any)
		if !ok {
			return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "erase.invalid_dataset_scope",
				"erase job contains an invalid source outcome", job.Result, invalidSyncDatasetScope())
		}
		status, _ := value["status"].(string)
		if eraseFailureStatus(status) {
			failures++
		}
		// A source that needs a status lookup is not itself an unresolved
		// outcome yet; the lookup below may prove success or failure. Only a
		// missing/unknown lookup result keeps the whole job unknown.
	}
	for source := range required {
		if _, present := sources[string(source)]; !present {
			unknown = true
		}
	}
	account, err := s.accountRepo.GetByID(ctx, job.TenantID, job.AccountID)
	if err != nil {
		// A reconciliation account-load failure says nothing about any remote
		// erase outcome. Keep the job unknown so a later pass can retry status
		// queries instead of converting an infrastructure timeout into failure.
		cause := apperr.Internal("erase.account_load_failed", "failed to load account").Wrap(err)
		return nil, s.finishJobFailure(ctx, job, domain.SyncJobUnknown, "erase.account_load_failed", "failed to load account", job.Result, cause)
	}
	type sourceReconcileResult struct {
		source            string
		status            string
		sourceOperationID string
		sourceResult      map[string]any
		reconcileErr      error
		checkpointErr     error
		blocked           bool
	}
	pending := make(chan sourceReconcileResult, len(sources))
	var wait sync.WaitGroup
	for source, rawValue := range sources {
		value := rawValue.(map[string]any)
		if !eraseStatusNeedsReconcile(source, value) {
			continue
		}
		wait.Add(1)
		go func(source string, value map[string]any) {
			defer wait.Done()
			status, sourceOperationID, sourceResult, reconcileErr := s.reconcileEraseSource(ctx, account, job, source, value)
			if isSyncExecutionBlocked(reconcileErr) {
				pending <- sourceReconcileResult{source: source, reconcileErr: reconcileErr, blocked: true}
				return
			}
			if reconcileErr != nil {
				if isUnknownRemote(reconcileErr) {
					status = "unknown"
				} else {
					status = "failed"
				}
				sourceResult = map[string]any{"error_code": remoteErrorCode(reconcileErr)}
			}
			checkpointCode := ""
			if reconcileErr != nil {
				checkpointCode = remoteErrorCode(reconcileErr)
			}
			checkpointErr := s.checkpointEraseOutcome(ctx, job, connector.Source(source), status,
				sourceOperationID, checkpointCode, sourceResult)
			pending <- sourceReconcileResult{source: source, status: status, sourceOperationID: sourceOperationID,
				sourceResult: sourceResult, reconcileErr: reconcileErr, checkpointErr: checkpointErr}
		}(source, value)
	}
	wait.Wait()
	close(pending)
	var blocked error
	for reconciled := range pending {
		if reconciled.blocked {
			blocked = reconciled.reconcileErr
			continue
		}
		if reconciled.checkpointErr != nil {
			return s.finishJobAndLoad(ctx, job, domain.SyncJobUnknown, "erase.outcome_checkpoint_failed", "source erase outcome could not be recorded", result)
		}
		entry := map[string]any{"status": reconciled.status}
		if reconciled.sourceOperationID != "" {
			entry["source_operation_id"] = reconciled.sourceOperationID
		}
		for key, item := range reconciled.sourceResult {
			if key != "status" && key != "source_operation_id" && key != "error_code" {
				entry[key] = item
			}
		}
		sources[reconciled.source] = entry
		if reconciled.status == "unknown" {
			unknown = true
		}
		if eraseFailureStatus(reconciled.status) {
			failures++
		}
	}
	if blocked != nil {
		return s.pauseSyncExecution(ctx, job, blocked, false, result)
	}
	result["reconciled_at"] = time.Now().UTC()
	status, code, message := eraseReconcileStatus(unknown, failures)
	if status == domain.SyncJobCompleted {
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

//nolint:cyclop,funlen // status reconciliation and explicit not-accepted retry are bounded remote states.
func (s *syncJobService) reconcileEraseSource(ctx context.Context, account *domain.Account, job *domain.SyncJob,
	source string, value map[string]any) (string, string, map[string]any, error) {
	sourceType := connector.Source(source)
	existingOperationID := sourceOperationID(value)
	client, ok := s.connectors[sourceType]
	if !ok {
		return "unknown", existingOperationID, nil, remoteUnknownError(source, "source.not_configured")
	}
	statusClient, ok := client.(connector.OperationStatusConnector)
	if !ok {
		return "unknown", existingOperationID, nil, remoteUnknownError(source, "source.operation_status_not_configured")
	}
	// A status lookup followed by a not-accepted retry shares one bounded
	// remote budget. Otherwise one source could consume two five-minute calls
	// while the six-minute job lease is being held.
	remoteCtx, remoteCancel := syncRemoteContext(ctx)
	defer remoteCancel()
	if err := s.checkSyncExecution(remoteCtx, job, false); err != nil {
		return "unknown", existingOperationID, nil, err
	}
	result, err := statusClient.OperationStatus(remoteCtx, connector.OperationStatusRequest{
		AccountID: account.ID, CanonicalUID: account.CanonicalUID, ActorUID: job.ActorUID, TenantID: job.TenantID,
		RequestID: job.RequestID, Region: account.HomeRegion, OperationID: job.ID.String(), SourceOperationID: existingOperationID,
		IdempotencyKey: syncEraseIdempotencyKey(job, sourceType),
	})
	if err != nil {
		operationID, correlated := trustedSyncEraseSourceOperationID(sourceType, job.ID.String(), existingOperationID, result)
		if !correlated {
			return "unknown", existingOperationID, nil, remoteUnknownError(source, "source.source_operation_id_mismatch")
		}
		var sourceResult map[string]any
		if result != nil {
			sourceResult = connector.RedactSummaryData(result.Result)
		}
		return "unknown", operationID, sourceResult, err
	}
	if result == nil {
		return "unknown", existingOperationID, nil, remoteUnknownError(source, "source.empty_command_result")
	}
	result.Result = connector.RedactSummaryData(result.Result)
	operationID, correlated := trustedSyncEraseSourceOperationID(sourceType, job.ID.String(), existingOperationID, result)
	if !correlated {
		return "unknown", existingOperationID, nil, remoteUnknownError(source, "source.source_operation_id_mismatch")
	}
	status := normalizedCommandStatus(result.Status)
	// An operation-status API without an operation identifier is only safe
	// when the source explicitly says the command was not accepted. For every
	// other status, retrying or guessing could erase the wrong command.
	if operationID == "" && !notAcceptedStatus(status) {
		return "unknown", "", nil, remoteUnknownError(source, "source.operation_id_missing")
	}
	if notAcceptedStatus(status) {
		command, ok := client.(connector.CommandConnector)
		if !ok {
			return "unknown", operationID, nil, remoteUnknownError(source, "source.command_not_configured")
		}
		if err := s.checkSyncExecution(remoteCtx, job, false); err != nil {
			return "unknown", operationID, result.Result, err
		}
		retried, retryErr := command.Erase(remoteCtx, eraseRequestForJob(account, job, sourceType, requiredDatasetsForOutcome(job, source)))
		retriedOperationID, retryCorrelated := trustedSyncEraseSourceOperationID(sourceType, job.ID.String(), operationID, retried)
		if !retryCorrelated {
			return "unknown", operationID, nil, remoteUnknownError(source, "source.source_operation_id_mismatch")
		}
		if retryErr != nil {
			var sourceResult map[string]any
			if retried != nil {
				sourceResult = connector.RedactSummaryData(retried.Result)
			}
			return "unknown", retriedOperationID, sourceResult, retryErr
		}
		if retried == nil {
			return "unknown", operationID, nil, remoteUnknownError(source, "source.empty_command_result")
		}
		status = normalizedCommandStatus(retried.Status)
		return eraseSourceOutcome(status), retriedOperationID, connector.RedactSummaryData(retried.Result), nil
	}
	return eraseSourceOutcome(status), operationID, result.Result, nil
}

func notAcceptedStatus(status string) bool {
	status = normalizedCommandStatus(status)
	return status == "not_accepted" || status == "not accepted" || status == "not-accepted"
}

func requiredDatasetsForOutcome(job *domain.SyncJob, source string) []domain.AccountDataset {
	datasets, _ := parsePersistedDatasets(job.Request, true)
	bySource, _ := validatedDatasetsBySource(datasets)
	return bySource[connector.Source(source)]
}

func remoteUnknownError(source, code string) error {
	return &connector.RemoteError{Source: connector.Source(source), Kind: connector.ErrorUnknown,
		Code: code, Temporary: true}
}

func eraseReconcileStatus(unknown bool, failures int) (domain.SyncJobStatus, string, string) {
	if unknown {
		return domain.SyncJobUnknown, "erase.unknown_outcome", "one or more source deletion outcomes remain unknown"
	}
	if failures > 0 {
		return domain.SyncJobPartiallyComplete, "erase.partial_failure", "one or more source deletion commands failed"
	}
	return domain.SyncJobCompleted, "", ""
}

func syncResultSources(result map[string]any) map[string]any {
	sources, ok := result["sources"].(map[string]any)
	if !ok {
		sources = map[string]any{}
		result["sources"] = sources
	}
	return sources
}

func eraseStatusNeedsReconcile(source string, value map[string]any) bool {
	status, _ := value["status"].(string)
	status = normalizedCommandStatus(status)
	if source == string(connector.SourceSnaplink) && commandSucceeded(status) && sourceOperationID(value) == "" {
		return true
	}
	return !commandSucceeded(status) && !eraseFailureStatus(status)
}

func eraseFailureStatus(status string) bool {
	switch normalizedCommandStatus(status) {
	case "failed", "failure", "rejected", "cancelled", "canceled", "not_configured":
		return true
	default:
		return false
	}
}

func eraseSourceOutcome(status string) string {
	status = normalizedCommandStatus(status)
	if commandSucceeded(status) {
		return "completed"
	}
	if eraseFailureStatus(status) {
		return "failed"
	}
	return "unknown"
}

func commandSucceeded(status string) bool {
	switch normalizedCommandStatus(status) {
	case "succeeded", "success", "completed", "complete", "done":
		return true
	default:
		return false
	}
}

func normalizedCommandStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func sourceOperationID(value map[string]any) string {
	raw, _ := value["source_operation_id"].(string)
	return strings.TrimSpace(raw)
}

func retryableEraseProjectionFailure(job *domain.SyncJob) bool {
	return job != nil && job.JobType == domain.SyncJobErase &&
		job.Status == domain.SyncJobPartiallyComplete && job.ErrorCode == "erase.local_projection_failed"
}

func cloneSyncResult(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	if sources, ok := result["sources"].(map[string]any); ok {
		cloned := make(map[string]any, len(sources))
		for source, entry := range sources {
			cloned[source] = entry
		}
		result["sources"] = connector.RedactSummaryData(cloned)
	} else {
		result["sources"] = map[string]any{}
	}
	return result
}
