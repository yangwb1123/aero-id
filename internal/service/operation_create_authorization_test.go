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

type operationAuthorizationRepo struct {
	repository.OperationRepository
	existing  *domain.Operation
	findCalls int
}

func (r *operationAuthorizationRepo) FindByIdempotency(context.Context,
	string, string, string) (*domain.Operation, error) {
	r.findCalls++
	if r.existing == nil {
		return nil, repository.ErrNotFound
	}
	return r.existing, nil
}

type operationAuthorizationPermission struct {
	allowed bool
	calls   int
}

func (p *operationAuthorizationPermission) HasPermission(context.Context,
	string, string) (bool, error) {
	p.calls++
	return p.allowed, nil
}

func replayOperationInput(accountID domain.ID) OperationInput {
	return OperationInput{AccountID: accountID, OperationType: "replay", IdempotencyKey: "replay-key-1",
		Steps: []OperationStepInput{{StepKey: "replay-01", Source: "aero-vault", Action: "summary"}}}
}

func TestOperationCreateReauthorizesBeforeIdempotencyLookup(t *testing.T) {
	accountID := idgen.NewID()
	repo := &operationAuthorizationRepo{existing: &domain.Operation{ID: idgen.NewID(), AccountID: accountID}}
	permission := &operationAuthorizationPermission{allowed: false}
	service := NewOperationService(repo, WithOperationPermission(permission))
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-a"})

	_, err := service.Create(ctx, replayOperationInput(accountID))
	var appError *apperr.AppError
	if !errors.As(err, &appError) || appError.Code != "operation.forbidden" {
		t.Fatalf("error = %v, want operation.forbidden", err)
	}
	if permission.calls != 1 || repo.findCalls != 0 {
		t.Fatalf("permission calls = %d, idempotency lookups = %d", permission.calls, repo.findCalls)
	}
}

func TestOperationCreateReturnsExistingAfterReauthorization(t *testing.T) {
	accountID := idgen.NewID()
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-a"})
	input := replayOperationInput(accountID)
	var err error
	input.Steps, err = normalizeOperationDatasetScopes(input.Steps)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := buildOperation(ctx, "admin-1", "tenant-a", input, "")
	if err != nil {
		t.Fatal(err)
	}
	repo := &operationAuthorizationRepo{existing: existing}
	permission := &operationAuthorizationPermission{allowed: true}
	service := NewOperationService(repo, WithOperationPermission(permission))

	result, err := service.Create(ctx, replayOperationInput(accountID))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result != existing || permission.calls != 2 || repo.findCalls != 1 {
		t.Fatalf("result = %#v, permission calls = %d, idempotency lookups = %d",
			result, permission.calls, repo.findCalls)
	}
}
