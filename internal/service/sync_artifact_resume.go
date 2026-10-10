package service

import (
	"context"
	"errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

func (s *syncJobService) recoverExportArtifact(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	ctx, cancel := syncRemoteContext(ctx)
	defer cancel()
	recovered := *job
	recovered.Result = cloneSyncResult(job.Result)
	if err := s.loadArtifactRecoveryResult(ctx, &recovered); err != nil {
		return s.unresolvedArtifact(ctx, job, err)
	}
	key, _ := recovered.Result["_storage_key"].(string)
	if key == "" || s.store == nil || len(s.exportKey) == 0 {
		return s.unresolvedArtifact(ctx, &recovered, errors.New("artifact recovery is unavailable"))
	}
	if err := s.recheckExportAccount(ctx, &recovered); err != nil {
		return s.discardExportAfterDispositionChange(ctx, &recovered, recovered.Result, key, err)
	}
	expires, validExpiry := exportExpiry(recovered.Result["expires_at"])
	if !validExpiry {
		return s.unresolvedArtifact(ctx, &recovered, errors.New("artifact expiry is unavailable"))
	}
	if !time.Now().UTC().Before(expires) {
		cause := apperr.Conflict("export.expired", "export artifact has expired")
		return s.discardExportAfterDispositionChange(ctx, &recovered, recovered.Result, key, cause)
	}
	// Read only the checkpointed object. NotFound may race an old in-flight
	// Put, so it remains unknown until TTL disposal; it never authorizes a new Put.
	if err := s.checkSyncExecution(ctx, &recovered, false); err != nil {
		return s.pauseSyncExecution(ctx, &recovered, err, false, recovered.Result)
	}
	_, _, err := s.readExportArtifact(ctx, &recovered)
	if err != nil {
		return s.unresolvedArtifact(ctx, &recovered, err)
	}
	return s.finishExportArtifact(ctx, &recovered, recovered.Result, key)
}

func (s *syncJobService) loadArtifactRecoveryResult(ctx context.Context, job *domain.SyncJob) error {
	repo, ok := s.repo.(repository.SyncJobArtifactRepository)
	if !ok {
		return nil // Compatibility ports use the durable result pointer alone.
	}
	metadata, err := repo.GetArtifact(ctx, job.ID, string(job.JobType))
	if err != nil || metadata == nil {
		return errors.New("artifact checkpoint is unavailable")
	}
	if metadata.JobID != job.ID || metadata.Kind != string(job.JobType) {
		return errors.New("artifact checkpoint scope is invalid")
	}
	if key, _ := job.Result["_storage_key"].(string); key != "" && key != metadata.StorageKey {
		return errors.New("artifact checkpoint pointer differs")
	}
	if len(job.Result) == 0 {
		result, ok := metadata.Continuation["_artifact_result"].(map[string]any)
		if !ok {
			return errors.New("artifact recovery metadata is unavailable")
		}
		job.Result = artifactResultMetadata(job.JobType, result)
	}
	job.Result["_storage_key"] = metadata.StorageKey
	return nil
}

func (s *syncJobService) unresolvedArtifact(ctx context.Context, job *domain.SyncJob, _ error) (*domain.SyncJob, error) {
	code := string(job.JobType) + ".artifact_checkpoint_unknown"
	return s.finishJobAndLoad(ctx, job, domain.SyncJobUnknown, code,
		"export artifact outcome remains unknown; reconcile the original artifact", artifactResultMetadata(job.JobType, job.Result))
}

func (s *syncJobService) finishExportArtifact(ctx context.Context, job *domain.SyncJob,
	manifest map[string]any, key string) (*domain.SyncJob, error) {
	if err := s.recheckExportAccount(ctx, job); err != nil {
		return s.discardExportAfterDispositionChange(ctx, job, manifest, key, err)
	}
	if err := s.checkSyncExecution(ctx, job, false); err != nil {
		return s.pauseSyncExecution(ctx, job, err, false, manifest)
	}
	result, err := s.finishJobAndLoad(ctx, job, domain.SyncJobCompleted, "", "", artifactResultMetadata(job.JobType, manifest))
	if errors.Is(err, repository.ErrAccountErased) {
		cause := apperr.Forbidden("export.account_erased", "export is unavailable for an erased account")
		return s.discardExportAfterDispositionChange(ctx, job, manifest, key, cause)
	}
	return result, err
}
