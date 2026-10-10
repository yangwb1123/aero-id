package service

import (
	"context"
	"errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/storage"
)

func (s *syncJobService) recheckExportAccount(ctx context.Context, job *domain.SyncJob) error {
	if s.accountRepo == nil {
		return apperr.Unavailable("export.account_unavailable", "export account context is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, job.TenantID, job.AccountID)
	if err != nil {
		return apperr.Internal("export.account_load_failed", "failed to load account").Wrap(err)
	}
	if account == nil || account.Status == domain.AccountErased {
		return apperr.Forbidden("export.account_erased", "export is unavailable for an erased account")
	}
	return nil
}

// A disposition observed during the object-store round trip invalidates the
// artifact. Preserve only TTL/key metadata on unknown cleanup so the existing
// retention worker can retry. Never turn cleanup uncertainty into completion.
func (s *syncJobService) discardExportAfterDispositionChange(ctx context.Context, job *domain.SyncJob,
	manifest map[string]any, key string, cause error) (*domain.SyncJob, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.Delete(cleanupCtx, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobUnknown, "export.cleanup_unknown",
			"export artifact cleanup outcome is unknown", safeAccountExportResult(manifest))
	}
	result := safeAccountExportResult(manifest)
	delete(result, "_storage_key")
	var appErr *apperr.AppError
	if !errors.As(cause, &appErr) {
		appErr = apperr.Forbidden("export.invalidated", "export artifact was invalidated")
	}
	return nil, s.finishJobFailure(ctx, job, domain.SyncJobFailed, appErr.Code, appErr.Message, result, cause)
}

func (s *syncJobService) reconcileInvalidatedExport(ctx context.Context, job *domain.SyncJob) (*domain.SyncJob, error) {
	key, _ := job.Result["_storage_key"].(string)
	if key == "" || s.store == nil {
		return s.finishJobAndLoad(ctx, job, domain.SyncJobUnknown, "export.cleanup_unknown",
			"export artifact cleanup metadata is unavailable", safeAccountExportResult(job.Result))
	}
	cause := apperr.Forbidden("export.invalidated", "export artifact was invalidated")
	return s.discardExportAfterDispositionChange(ctx, job, job.Result, key, cause)
}
