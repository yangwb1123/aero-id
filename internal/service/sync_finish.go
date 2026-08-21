package service

import (
	"context"
	"errors"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

// finishJobAndLoad makes the durable terminal transition and reload one
// service-level operation. A failed audit append or lease CAS must never be
// hidden by returning a stale in-memory job or a raw repository error.
func (s *syncJobService) finishJobAndLoad(ctx context.Context, job *domain.SyncJob,
	status domain.SyncJobStatus, code, message string, result map[string]any) (*domain.SyncJob, error) {
	if err := s.finishJob(ctx, job, status, code, message, result); err != nil {
		return nil, apperr.Internal("sync.finish_failed", "failed to finish sync job").Wrap(err)
	}
	return s.reloadSyncJob(ctx, job.ID)
}

func (s *syncJobService) reloadSyncJob(ctx context.Context, id domain.ID) (*domain.SyncJob, error) {
	job, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, apperr.Internal("sync.reload_failed", "failed to reload sync job").Wrap(err)
	}
	return job, nil
}

// finishJobFailure preserves the original service error when the terminal
// failure state is durably written. If that write (including its audit fact)
// fails, the persistence failure takes precedence and both causes remain
// attached for diagnostics.
func (s *syncJobService) finishJobFailure(ctx context.Context, job *domain.SyncJob,
	status domain.SyncJobStatus, code, message string, result map[string]any, cause error) error {
	if err := s.finishJob(ctx, job, status, code, message, result); err != nil {
		return apperr.Internal("sync.finish_failed", "failed to persist sync job failure").Wrap(errors.Join(cause, err))
	}
	return cause
}
