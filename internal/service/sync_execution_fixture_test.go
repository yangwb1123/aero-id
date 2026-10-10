package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

// Positive-control adapters for old projection/export fixtures. Negative
// lease/authority cases use a stateful repository, and PostgreSQL verifies the
// actual database-clock/directory/epoch predicates.
type syncTestExecutionPorts struct{}

func (syncTestExecutionPorts) CheckExecutionLease(context.Context, *domain.SyncJob, string, bool) error {
	return nil
}
func (syncTestExecutionPorts) DeferExecutionWithAudit(context.Context, repository.SyncJobExecutionDeferral, *domain.AuditFact) error {
	return repository.ErrSyncJobLeaseLost // unexpected blocking is a fixture error
}

type syncFixtureAccount struct {
	repository.AccountRepository
	region string
}

func (r syncFixtureAccount) GetByID(_ context.Context, tenant string, id domain.ID) (*domain.Account, error) {
	return &domain.Account{ID: id, TenantID: tenant, HomeRegion: r.region, Status: domain.AccountActive}, nil
}
