package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
)

type artifactCheckpointError struct{ cause error }

func (e *artifactCheckpointError) Error() string { return "artifact metadata checkpoint failed" }
func (e *artifactCheckpointError) Unwrap() error { return e.cause }

func (s *syncJobService) putSyncArtifact(ctx context.Context, job *domain.SyncJob, kind string,
	data []byte, manifest map[string]any, legacyName string) (string, int64, error) {
	key, size, err := s.writeJobArtifact(ctx, job, kind, data, legacyName)
	if err != nil {
		return "", 0, err
	}
	if err := validateJobArtifactKey(job, kind, key, s.store); err != nil {
		return "", 0, err
	}
	if recovery, ok := s.repo.(repository.SyncJobRecoveryRepository); ok {
		if err := s.checkpointJobArtifact(ctx, recovery, job, kind, key, size, manifest); err != nil {
			return "", 0, &artifactCheckpointError{cause: err}
		}
	}
	return key, size, nil
}

func (s *syncJobService) writeJobArtifact(ctx context.Context, job *domain.SyncJob, kind string,
	data []byte, legacyName string) (string, int64, error) {
	if writer, ok := s.store.(storage.JobArtifactWriter); ok {
		// The claim token, not the stable actor UID, is part of the object
		// namespace. A stale attempt can at worst leave an orphan; it cannot
		// overwrite the replacement attempt's artifact.
		return writer.PutJobArtifact(ctx, job.LeaseOwner, job.ID, kind, bytes.NewReader(data))
	}
	// Compatibility backends retain the old Put seam. Production Local and S3
	// implement JobArtifactWriter and therefore use the deterministic key.
	return s.store.Put(ctx, job.ActorUID, legacyName, bytes.NewReader(data))
}

func validateJobArtifactKey(job *domain.SyncJob, kind, key string, store storage.FileStorage) error {
	if _, ok := store.(storage.JobArtifactWriter); !ok {
		return nil
	}
	expected, err := storage.JobArtifactKey(job.LeaseOwner, job.ID, kind)
	if err != nil || key != expected {
		return fmt.Errorf("artifact writer returned an invalid job key")
	}
	return nil
}

func (s *syncJobService) checkpointJobArtifact(ctx context.Context, recovery repository.SyncJobRecoveryRepository,
	job *domain.SyncJob, kind, key string, size int64, manifest map[string]any) error {
	continuation := make(map[string]any)
	for name, value := range manifest {
		if len(name) > 0 && name[0] == '_' {
			continuation[name] = value
		}
	}
	persistCtx, cancel := syncPersistenceContext(ctx)
	defer cancel()
	return recovery.CheckpointArtifact(syncRecoveryContext(persistCtx, job.LeaseOwner), repository.ArtifactCheckpoint{
		JobID: job.ID, Kind: kind, Worker: job.LeaseOwner, StorageKey: key,
		ManifestSHA256: artifactManifestDigest(manifest), SizeBytes: size, Continuation: continuation,
	})
}

func (s *syncJobService) validateDownloadedArtifact(ctx context.Context, job *domain.SyncJob, key string,
	size int, manifest map[string]any) error {
	expectedType, err := validateArtifactIdentity(job, manifest)
	if err != nil {
		return err
	}
	if err := validateArtifactExpiry(job, manifest); err != nil {
		return err
	}
	if job.JobType == domain.SyncJobAuditExport {
		if err := validateAuditArtifactManifest(manifest); err != nil {
			return err
		}
	}
	return s.validateArtifactMetadata(ctx, job, expectedType, key, size, manifest)
}

func validateArtifactIdentity(job *domain.SyncJob, manifest map[string]any) (string, error) {
	if job == nil || manifest == nil {
		return "", apperr.Internal("export.invalid_artifact", "export artifact is invalid")
	}
	expectedType, err := expectedArtifactType(job.JobType)
	if err != nil {
		return "", err
	}
	accountMatches := job.AccountID == (domain.ID{}) || manifest["account_id"] == job.AccountID.String()
	jobMatches := job.ID == (domain.ID{}) || manifest["job_id"] == job.ID.String()
	if manifest["type"] != expectedType || manifest["tenant_id"] != job.TenantID || !accountMatches || !jobMatches {
		return "", apperr.Internal("export.invalid_artifact", "export artifact identity does not match its job")
	}
	return expectedType, nil
}

func expectedArtifactType(jobType domain.SyncJobType) (string, error) {
	if jobType != domain.SyncJobExport && jobType != domain.SyncJobAuditExport {
		return "", apperr.Internal("export.invalid_artifact", "export artifact type is invalid")
	}
	return string(jobType), nil
}

func validateArtifactExpiry(job *domain.SyncJob, manifest map[string]any) error {
	expires, artifactOK := exportExpiry(manifest["expires_at"])
	_, jobOK := exportExpiry(job.Result["expires_at"])
	if !artifactOK || !jobOK || !time.Now().UTC().Before(expires) {
		return apperr.Internal("export.invalid_artifact", "export artifact expiry is invalid")
	}
	return nil
}

func validateAuditArtifactManifest(manifest map[string]any) error {
	digest, valid := manifest["manifest_digest"].(string)
	if !valid || len(digest) != sha256.Size*2 || digest != auditExportDigest(manifest) {
		return apperr.Internal("audit_export.integrity_failed", "audit export manifest integrity check failed")
	}
	return nil
}

func (s *syncJobService) validateArtifactMetadata(ctx context.Context, job *domain.SyncJob, kind, key string,
	size int, manifest map[string]any) error {
	recovery, ok := s.repo.(repository.SyncJobArtifactRepository)
	if !ok {
		return nil
	}
	metadata, err := recovery.GetArtifact(ctx, job.ID, kind)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return apperr.Internal("export.metadata_read_failed", "export artifact metadata is unavailable").Wrap(err)
	}
	if err == nil && (metadata == nil || metadata.StorageKey != key || metadata.SizeBytes != int64(size) ||
		metadata.ManifestSHA256 != artifactManifestDigest(manifest)) {
		return apperr.Internal("export.integrity_failed", "export artifact metadata does not match its content")
	}
	return nil
}

func artifactManifestDigest(manifest map[string]any) string {
	filtered := make(map[string]any, len(manifest))
	for key, value := range manifest {
		if key != "_storage_key" {
			filtered[key] = value
		}
	}
	data, _ := json.Marshal(filtered)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
