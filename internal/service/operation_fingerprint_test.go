package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

func fingerprintContext(t *testing.T, client string) context.Context {
	t.Helper()
	return WithIdentity(t.Context(), &domain.TokenIdentity{Verified: true, UID: "actor-1",
		Issuer: "https://snaplink.example", Subject: "actor-1", TenantID: "tenant-a", ClientID: client})
}

func fingerprintOperationInput() OperationInput {
	return OperationInput{AccountID: domain.ID{1}, OperationType: "sync", IdempotencyKey: "intent-1",
		RequestPayload: map[string]any{"note": "refresh"}, Steps: []OperationStepInput{
			{StepKey: "vault", Source: "aero-vault", Action: "summary",
				RequestPayload: map[string]any{"datasets": []string{"aero-vault.tenants", "aero-vault.usage"}}},
			{StepKey: "im", Source: "aero-im", Action: "summary",
				RequestPayload: map[string]any{"datasets": []string{"aero-im.profile"}}},
		}}
}

func assertFingerprintConflict(t *testing.T, err error, kind string) {
	t.Helper()
	var appError *apperr.AppError
	if !errors.As(err, &appError) || appError.Code != kind+".idempotency_conflict" || appError.HTTP != 409 {
		t.Fatalf("error = %v, want %s.idempotency_conflict/409", err, kind)
	}
}

func TestOperationRequestFingerprintCanonicalRetry(t *testing.T) {
	repo := &operationScopeCreateRepo{}
	svc := NewOperationService(repo)
	ctx := fingerprintContext(t, "console")
	original, err := svc.Create(ctx, fingerprintOperationInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(original.RequestFingerprint) != 67 {
		t.Fatalf("fingerprint = %q, want versioned SHA-256", original.RequestFingerprint)
	}
	repo.existing = original
	input := fingerprintOperationInput()
	input.OperationType, input.IdempotencyKey = " SYNC ", " intent-1 "
	input.Steps[0].Source, input.Steps[0].Action = " aero-vault ", " SUMMARY "
	input.Steps[0].RequestPayload["datasets"] = []any{"aero-vault.usage", "aero-vault.tenants"}
	retry, err := svc.Create(WithRequestID(ctx, "different-request-id"), input)
	if err != nil || retry != original {
		t.Fatalf("retry = %v/%v, want original operation", retry, err)
	}
}

func TestOperationRequestFingerprintRejectsChangedIntent(t *testing.T) {
	changes := []struct {
		name   string
		client string
		change func(*OperationInput)
	}{
		{"account", "console", func(in *OperationInput) { in.AccountID = domain.ID{2} }},
		{"type", "console", func(in *OperationInput) { in.OperationType = "source.command" }},
		{"dataset", "console", func(in *OperationInput) {
			in.Steps[0].RequestPayload["datasets"] = []string{"aero-vault.tenants"}
		}},
		{"payload", "console", func(in *OperationInput) { in.RequestPayload["note"] = "changed" }},
		{"step order", "console", func(in *OperationInput) { in.Steps[0], in.Steps[1] = in.Steps[1], in.Steps[0] }},
		{"step key", "console", func(in *OperationInput) { in.Steps[0].StepKey = "other" }},
		{"compensation", "console", func(in *OperationInput) { in.Steps[0].CompensationAction = "summary" }},
		{"client", "other-console", func(*OperationInput) {}},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			repo := &operationScopeCreateRepo{}
			svc := NewOperationService(repo, WithOperationPermission(&operationAuthorizationPermission{allowed: true}))
			original, err := svc.Create(fingerprintContext(t, "console"), fingerprintOperationInput())
			if err != nil {
				t.Fatal(err)
			}
			repo.existing = original
			input := fingerprintOperationInput()
			tc.change(&input)
			result, err := svc.Create(fingerprintContext(t, tc.client), input)
			assertFingerprintConflict(t, err, "operation")
			if result != nil || repo.created != original {
				t.Fatal("changed intent returned or created an operation")
			}
		})
	}
}

func TestOperationLegacyRequestReceiptFailsClosed(t *testing.T) {
	repo := &operationScopeCreateRepo{operationAuthorizationRepo: operationAuthorizationRepo{
		existing: &domain.Operation{AccountID: domain.ID{1}}}}
	result, err := NewOperationService(repo).Create(fingerprintContext(t, "console"), fingerprintOperationInput())
	assertFingerprintConflict(t, err, "operation")
	if result != nil || repo.created != nil {
		t.Fatal("unbound receipt was returned or a second command was created")
	}
}

type fingerprintRaceOperationRepo struct {
	repository.OperationRepository
	winner  *domain.Operation
	lookup  int
	matched bool
}

func (r *fingerprintRaceOperationRepo) FindByIdempotency(context.Context, string, string, string) (*domain.Operation, error) {
	r.lookup++
	if r.lookup == 1 {
		return nil, repository.ErrNotFound
	}
	return r.winner, nil
}

func (r *fingerprintRaceOperationRepo) Create(_ context.Context, operation *domain.Operation) error {
	winner := *operation
	if !r.matched {
		winner.RequestFingerprint = "v1:" + strings.Repeat("0", 64)
	}
	r.winner = &winner
	return gorm.ErrDuplicatedKey
}

func TestOperationUniqueInsertRaceStillChecksFingerprint(t *testing.T) {
	for _, matched := range []bool{true, false} {
		repo := &fingerprintRaceOperationRepo{matched: matched}
		result, err := NewOperationService(repo).Create(fingerprintContext(t, "console"), fingerprintOperationInput())
		if matched {
			if err != nil || result != repo.winner {
				t.Fatalf("matching race = %v/%v", result, err)
			}
		} else {
			assertFingerprintConflict(t, err, "operation")
			if result != nil {
				t.Fatal("conflicting race returned the winner's operation")
			}
		}
	}
}
