package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type operationGetRepo struct {
	repository.OperationRepository
	operation *domain.Operation
}

func (repo *operationGetRepo) Get(context.Context, domain.ID) (*domain.Operation, error) {
	return repo.operation, nil
}

type syncJobGetRepo struct {
	repository.SyncJobRepository
	job *domain.SyncJob
}

func (repo *syncJobGetRepo) Get(context.Context, domain.ID) (*domain.SyncJob, error) {
	return repo.job, nil
}

type allowAllPermission struct{}

func (allowAllPermission) HasPermission(context.Context, string, string) (bool, error) {
	return true, nil
}

func TestPrivilegedOperationReadCannotCrossTenant(t *testing.T) {
	id := idgen.NewID()
	repo := &operationGetRepo{operation: &domain.Operation{ID: id, ActorUID: "other", TenantID: "tenant-b"}}
	operations := NewOperationService(repo, WithOperationPermission(allowAllPermission{}))
	assertTenantNotFound(t, func(ctx context.Context) error {
		_, err := operations.Get(ctx, id)
		return err
	}, "operation.not_found")
}

func TestPrivilegedSyncJobReadCannotCrossTenant(t *testing.T) {
	id := idgen.NewID()
	repo := &syncJobGetRepo{job: &domain.SyncJob{ID: id, ActorUID: "other", TenantID: "tenant-b"}}
	jobs := NewSyncJobServiceWithOptions(repo, nil, nil, nil, nil, nil, nil, WithSyncJobPermission(allowAllPermission{}))
	assertTenantNotFound(t, func(ctx context.Context) error {
		_, err := jobs.Get(ctx, id)
		return err
	}, "sync.not_found")
}

func assertTenantNotFound(t *testing.T, call func(context.Context) error, code string) {
	t.Helper()
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin", TenantID: "tenant-a"})
	err := call(ctx)
	var appError *apperr.AppError
	if !errors.As(err, &appError) || appError.Code != code {
		t.Fatalf("cross-tenant error = %v, want %s", err, code)
	}
}
