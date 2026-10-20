package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
)

func (s *syncJobService) loadExportDownload(ctx context.Context, id domain.ID) (*domain.SyncJob, error) {
	if _, err := verifiedControlPlaneIdentity(ctx); err != nil {
		return nil, err
	}
	job, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.recheckExportDownloadPermission(ctx, job); err != nil {
		return nil, err
	}
	if err := s.recheckLegacyExportDownload(ctx, job); err != nil {
		return nil, err
	}
	if job.Status != domain.SyncJobCompleted || len(s.exportKey) == 0 {
		return nil, apperr.Conflict("export.not_available", "export artifact is not available")
	}
	expires, ok := exportExpiry(job.Result["expires_at"])
	if !ok || !time.Now().UTC().Before(expires) {
		s.deleteExpiredExport(ctx, job)
		return nil, apperr.Conflict("export.expired", "export artifact has expired")
	}
	return job, nil
}

func (s *syncJobService) burnOneTimeExport(ctx context.Context, job *domain.SyncJob, key string, oneTime bool) error {
	if !oneTime {
		return nil
	}
	return s.burnExportArtifact(ctx, job, key)
}

func (s *syncJobService) decodeExportArtifact(data []byte) ([]byte, map[string]any, error) {
	plain, err := decryptExport(data, s.exportKey)
	if err != nil {
		return nil, nil, apperr.Internal("export.decrypt_failed", "failed to decrypt export artifact").Wrap(err)
	}
	if int64(len(plain)) > s.maxExport {
		return nil, nil, apperr.Internal("export.read_failed", "export artifact is too large")
	}
	var manifest map[string]any
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.UseNumber()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, nil, apperr.Internal("export.invalid_artifact", "export artifact is invalid").Wrap(err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, nil, apperr.Internal("export.invalid_artifact", "export artifact is invalid")
	}
	return plain, manifest, nil
}

func (s *syncJobService) admitExportDownload(ctx context.Context, job *domain.SyncJob, key string,
	oneTime bool, size int) (bool, error) {
	if err := s.recheckExportDownloadPermission(ctx, job); err != nil {
		return true, err
	}
	repo, ok := s.repo.(repository.SyncJobExportDownloadRepository)
	if !ok {
		return false, nil // Legacy ports retain the audited fallback below.
	}
	if s.audit == nil {
		return true, apperr.Unavailable("audit.ingest_failed", "audit record unavailable")
	}
	fact := buildAuditFact(ctx, exportDownloadAudit(ctx, job, oneTime, size))
	if err := repo.AdmitExportDownload(ctx, job.ID, string(job.JobType), key, oneTime, fact); err != nil {
		if errors.Is(err, repository.ErrAccountErased) {
			return true, apperr.Forbidden("export.account_erased", "export is unavailable for an erased account")
		}
		if errors.Is(err, repository.ErrNotFound) {
			return true, apperr.NotFound("export.not_found", "export artifact not found")
		}
		return true, apperr.Unavailable("export.admission_failed", "export download admission is unavailable").Wrap(err)
	}
	if oneTime {
		return true, s.completeDownloadDisposal(ctx, job, key)
	}
	return true, nil
}

func (s *syncJobService) completeDownloadDisposal(ctx context.Context, job *domain.SyncJob, key string) error {
	cleanupCtx, cancel := syncPersistenceContext(ctx)
	defer cancel()
	if err := s.store.Delete(cleanupCtx, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return apperr.Internal("export.burn_failed", "failed to finalize one-time export").Wrap(err)
	}
	if err := s.repo.ClearExportArtifact(cleanupCtx, job.ID, key); err != nil {
		return apperr.Internal("export.burn_failed", "failed to finalize one-time export").Wrap(err)
	}
	return nil
}

// The current reader, not the durable initiator/owner, needs direct authority
// before object IO and again before final local admission. A generated artifact
// or legacy permission checker cannot renew a sensitive-read grant.
func (s *syncJobService) recheckExportDownloadPermission(ctx context.Context, job *domain.SyncJob) error {
	identity, err := verifiedControlPlaneIdentity(ctx)
	if err != nil {
		return err
	}
	if job == nil || job.TenantID != identity.TenantID {
		return apperr.NotFound("export.not_found", "export artifact not found")
	}
	permissions := exportDownloadPermissions(job.JobType)
	if len(permissions) == 0 {
		return apperr.Conflict("export.not_available", "export artifact is not available")
	}
	return s.checkExportDownloadGrants(ctx, identity.UID, permissions)
}

func exportDownloadPermissions(kind domain.SyncJobType) []string {
	switch kind {
	case domain.SyncJobExport:
		return []string{domain.PermAccountExport}
	case domain.SyncJobAuditExport:
		return []string{domain.PermAuditExport, domain.PermAuditRead}
	default:
		return nil
	}
}

func (s *syncJobService) checkExportDownloadGrants(ctx context.Context, actor string, permissions []string) error {
	ctx, cancel := context.WithTimeout(ctx, syncAuthorizationBudget)
	defer cancel()
	if isNilExecutionDependency(s.executionPermission) || ctx.Err() != nil {
		return apperr.Unavailable("export.permission_check_failed", "current export permissions cannot be verified")
	}
	for _, permission := range permissions {
		allowed, err := s.executionPermission.HasPermission(ctx, actor, permission)
		if err != nil || ctx.Err() != nil {
			return apperr.Unavailable("export.permission_check_failed", "current export permissions cannot be verified")
		}
		if !allowed {
			return apperr.Forbidden("export.forbidden", "current export and required read permissions are required")
		}
	}
	return nil
}

func (s *syncJobService) recheckLegacyExportDownload(ctx context.Context, job *domain.SyncJob) error {
	if s.accountRepo == nil {
		return nil
	}
	return s.recheckExportAccount(ctx, job)
}
