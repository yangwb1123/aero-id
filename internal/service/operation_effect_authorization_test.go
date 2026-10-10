package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

type intentStoredOperationRepo struct {
	repository.OperationRepository
	operation *domain.Operation
}

func (r intentStoredOperationRepo) Get(context.Context, domain.ID) (*domain.Operation, error) {
	return r.operation, nil
}

func TestOperationRunRechecksCompensationEffectPermissions(t *testing.T) {
	for _, outage := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "authority unavailable"}[outage], func(t *testing.T) {
			accounts := &intentAccountRepo{account: &domain.Account{ID: domain.ID{1}, TenantID: "tenant-a", CanonicalUID: "actor-1", HomeRegion: "local", Status: domain.AccountActive}}
			operation := &domain.Operation{ID: domain.ID{2}, AccountID: accounts.account.ID, TenantID: "tenant-a", ActorUID: "actor-1", HomeRegion: "local",
				OperationType: "sync", Steps: []*domain.OperationStep{{StepKey: "vault", Source: "aero-vault", Action: "summary", CompensationAction: "erase",
					RequestPayload: map[string]any{"datasets": []string{"aero-vault.usage"}}}}}
			checks := 0
			permission := capabilityPermissions(func(_ context.Context, _ string, value string) (bool, error) {
				if value != domain.PermAccountErase {
					return true, nil
				}
				checks++
				if outage {
					return false, errors.New("private authorizer endpoint")
				}
				return false, nil
			})
			svc := NewOperationService(intentStoredOperationRepo{operation: operation}, WithOperationPermission(permission),
				WithOperationAccountRepository(accounts), WithOperationRegion("local"))
			if _, err := svc.Run(fingerprintContext(t, "console"), operation.ID, "manual-worker"); err == nil || checks != 1 {
				t.Fatal("revoked compensation permission reached execution", err)
			}
		})
	}
}

func TestTypedSyncCannotSmuggleDestructiveCompensation(t *testing.T) {
	input := fingerprintOperationInput()
	input.Steps[0].CompensationAction = "erase"
	permissions, err := operationEffectPermissions(input)
	if err != nil || len(permissions) != 1 || permissions[0] != domain.PermAccountErase {
		t.Fatal("typed self sync hid destructive compensation", permissions, err)
	}
	if err := validateOperationCompensationScope(OperationStepInput{Source: "snaplink", Action: "summary", CompensationAction: "erase"}); err == nil {
		t.Fatal("destructive compensation expanded a missing scope")
	}
}
