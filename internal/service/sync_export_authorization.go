package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

// Every sensitive page gets current admission, not just the first page.
type authorizedSyncExportLedger struct {
	repository.AuditLedgerRepository
	service *syncJobService
	job     *domain.SyncJob
}

func (r *authorizedSyncExportLedger) List(ctx context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	if err := r.service.checkSyncExecution(ctx, r.job, false); err != nil {
		return nil, "", err
	}
	return r.AuditLedgerRepository.List(ctx, filter)
}
