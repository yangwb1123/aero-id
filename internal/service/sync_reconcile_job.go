package service

import (
	"context"
	"errors"
	"strings"
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
	if job.Status != domain.SyncJobUnknown {
		return job, nil
	}
	// Validate erase scopes before home-region/account loading or a status
	// query. A malformed historical job is a durable local failure, not an
	// instruction to probe a source with an implicit scope.
	if job.JobType == domain.SyncJobErase {
		if scopeErr := validatePersistedSyncJobScope(job); scopeErr != nil {
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

func (s *syncJobService) reconcileClaimed(ctx context.Context, claimed *domain.SyncJob) (*domain.SyncJob, error) {
	if claimed.JobType != domain.SyncJobErase {
		return s.finishJobAndLoad(ctx, claimed, domain.SyncJobUnknown, "sync.reconcile_unsupported",
			"this sync job type has no outcome reconciler", claimed.Result)
	}
	return s.reconcileErase(ctx, claimed)
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
	result := cloneSyncResult(job.Result)
	sources := syncResultSources(result)
	unknown := len(sources) == 0
	for source, rawValue := range sources {
		if len(required[connector.Source(source)]) == 0 {
			return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "erase.invalid_dataset_scope",
				"erase job contains a source outcome outside its dataset scope", job.Result, invalidSyncDatasetScope())
		}
		if _, ok := rawValue.(map[string]any); !ok {
			return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, "erase.invalid_dataset_scope",
				"erase job contains an invalid source outcome", job.Result, invalidSyncDatasetScope())
		}
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
	failures := 0
	for source, rawValue := range sources {
		value := rawValue.(map[string]any)
		if !eraseStatusNeedsReconcile(value) {
			continue
		}
		status, sourceOperationID, sourceResult, reconcileErr := s.reconcileEraseSource(ctx, account, job, source, value)
		if reconcileErr != nil {
			if isUnknownRemote(reconcileErr) {
				unknown = true
				status = "unknown"
			} else {
				status = "failed"
			}
			sourceResult = map[string]any{"error_code": remoteErrorCode(reconcileErr)}
		}
		entry := map[string]any{"status": status}
		if sourceOperationID != "" {
			entry["source_operation_id"] = sourceOperationID
		}
		for key, item := range sourceResult {
			if key != "status" && key != "source_operation_id" {
				entry[key] = item
			}
		}
		sources[source] = entry
		if status == "unknown" {
			unknown = true
		}
		if eraseFailureStatus(status) {
			failures++
		}
	}
	result["reconciled_at"] = time.Now().UTC()
	status, code, message := eraseReconcileStatus(unknown, failures)
	if status == domain.SyncJobCompleted {
		persistCtx, persistCancel := syncPersistenceContext(ctx)
		err := s.accountRepo.EraseAccount(persistCtx, job.TenantID, job.AccountID)
		persistCancel()
		if err != nil {
			status = domain.SyncJobPartiallyComplete
			code, message = "erase.local_projection_failed", "source deletion completed but local erasure failed"
		}
	}
	return s.finishJobAndLoad(ctx, job, status, code, message, result)
}

func (s *syncJobService) reconcileEraseSource(ctx context.Context, account *domain.Account, job *domain.SyncJob,
	source string, value map[string]any) (string, string, map[string]any, error) {
	client, ok := s.connectors[connector.Source(source)]
	if !ok {
		return "unknown", sourceOperationID(value), nil, remoteUnknownError(source, "source.not_configured")
	}
	statusClient, ok := client.(connector.OperationStatusConnector)
	if !ok {
		return "unknown", sourceOperationID(value), nil, remoteUnknownError(source, "source.operation_status_not_configured")
	}
	remoteCtx, remoteCancel := syncRemoteContext(ctx)
	result, err := statusClient.OperationStatus(remoteCtx, connector.OperationStatusRequest{
		AccountID: account.ID, CanonicalUID: account.CanonicalUID, ActorUID: job.ActorUID, TenantID: job.TenantID,
		RequestID: job.RequestID, Region: account.HomeRegion, OperationID: job.ID.String(), SourceOperationID: sourceOperationID(value),
		IdempotencyKey: syncEraseIdempotencyKey(job, connector.Source(source)),
	})
	remoteCancel()
	if err != nil {
		return "unknown", sourceOperationID(value), nil, err
	}
	if result == nil {
		return "unknown", sourceOperationID(value), nil, remoteUnknownError(source, "source.empty_command_result")
	}
	result.Result = connector.RedactSummaryData(result.Result)
	status := normalizedCommandStatus(result.Status)
	operationID := strings.TrimSpace(result.SourceOperationID)
	if operationID == "" {
		operationID = sourceOperationID(value)
	}
	return eraseSourceOutcome(status), operationID, result.Result, nil
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

func eraseStatusNeedsReconcile(value map[string]any) bool {
	status, _ := value["status"].(string)
	status = normalizedCommandStatus(status)
	return !commandSucceeded(status) && !eraseFailureStatus(status)
}

func eraseFailureStatus(status string) bool {
	switch normalizedCommandStatus(status) {
	case "failed", "failure", "rejected", "cancelled", "canceled":
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
		result["sources"] = cloned
	} else {
		result["sources"] = map[string]any{}
	}
	return result
}
