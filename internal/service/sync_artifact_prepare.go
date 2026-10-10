package service

import (
	"context"
	"errors"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
)

func (s *syncJobService) prepareJobArtifact(ctx context.Context, job *domain.SyncJob, kind string,
	data []byte, manifest map[string]any) (string, error) {
	if _, ok := s.store.(storage.JobArtifactWriter); !ok {
		return "", nil // Legacy ports do not have a predictable object namespace.
	}
	key, err := storage.JobArtifactKey(job.LeaseOwner, job.ID, kind)
	if err != nil {
		return "", err
	}
	recovery, ok := s.repo.(repository.SyncJobRecoveryRepository)
	if !ok {
		return key, errors.New("artifact recovery persistence is unavailable")
	}
	// Persist the exact key/digest/size/TTL BEFORE the remote write. A crash or
	// lost Put response can only reconcile this object; never generate another.
	return key, s.checkpointJobArtifact(ctx, recovery, job, kind, key, int64(len(data)), manifest)
}

func artifactResultMetadata(kind domain.SyncJobType, manifest map[string]any) map[string]any {
	if kind == domain.SyncJobExport {
		return safeAccountExportResult(manifest)
	}
	result := auditExportContinuationResult(manifest)
	for _, key := range []string{"job_id", "download_url", "expires_at", "_storage_key", "artifact_deleted_at"} {
		if value, ok := manifest[key]; ok {
			result[key] = value
		}
	}
	return result
}

func shouldRecoverExport(job *domain.SyncJob) bool {
	key, _ := job.Result["_storage_key"].(string)
	return key != "" || job.LeaseKind == "reconcile" || job.ErrorCode == "export.artifact_checkpoint_unknown" ||
		job.ErrorCode == "audit_export.artifact_checkpoint_unknown"
}
