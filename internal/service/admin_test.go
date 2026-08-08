package service_test

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
)

// adminRepoStub implements the AdminRepository surface used by GrantRole and
// RevokeRole; the embedded interface satisfies the untouched methods. The
// WithAudit methods are the in-tx path the service uses when an auditor is
// wired (B2-1); they record the fact and the failure injection.
type adminRepoStub struct {
	repository.AdminRepository
	grantCalls   int
	revokeCalls  int
	auditErr     error
	grantedFacts []*domain.AuditFact
	revokedFacts []*domain.AuditFact
}

func (f *adminRepoStub) GrantRole(ctx context.Context, uid, roleCode, grantedBy string) error {
	f.grantCalls++
	return nil
}

func (f *adminRepoStub) RevokeRole(ctx context.Context, uid, roleCode string) error {
	f.revokeCalls++
	return nil
}

func (f *adminRepoStub) GrantRoleWithAudit(ctx context.Context, uid, roleCode, grantedBy string, fact *domain.AuditFact) error {
	if f.auditErr != nil {
		return f.auditErr
	}
	f.grantCalls++
	f.grantedFacts = append(f.grantedFacts, fact)
	return nil
}

func (f *adminRepoStub) RevokeRoleWithAudit(ctx context.Context, uid, roleCode string, fact *domain.AuditFact) error {
	if f.auditErr != nil {
		return f.auditErr
	}
	f.revokeCalls++
	f.revokedFacts = append(f.revokedFacts, fact)
	return nil
}

// TestAdminGrantRoleAuditFailureFailsClosed (B2-1): the in-tx WithAudit path
// fails closed — the request fails with admin.audit_unavailable and the
// mutation never commits (same transaction as the fact).
func TestAdminGrantRoleAuditFailureFailsClosed(t *testing.T) {
	repo := &adminRepoStub{auditErr: stderrors.New("ledger down")}
	svc := service.NewAdminService(repo, service.WithAdminAuditLogger(&mocks.Auditor{}))
	ctx := service.WithActor(context.Background(), "admin-1")

	err := svc.GrantRole(ctx, "u-2", "admin")
	assertAppCode(t, err, 500)
	if repo.grantCalls != 0 {
		t.Fatalf("grant repo calls = %d, want 0 (mutation rolled back with the failed fact)", repo.grantCalls)
	}
	if len(repo.grantedFacts) != 0 {
		t.Fatal("no fact may be recorded when the WithAudit call fails")
	}
}

// TestAdminRevokeRoleAuditFailureFailsClosed (B2-1): same fail-closed contract
// for role revocation — the revoke and its fact share one transaction.
func TestAdminRevokeRoleAuditFailureFailsClosed(t *testing.T) {
	repo := &adminRepoStub{auditErr: stderrors.New("ledger down")}
	svc := service.NewAdminService(repo, service.WithAdminAuditLogger(&mocks.Auditor{}))
	ctx := service.WithActor(context.Background(), "admin-1")

	err := svc.RevokeRole(ctx, "u-2", "admin")
	assertAppCode(t, err, 500)
	if repo.revokeCalls != 0 {
		t.Fatalf("revoke repo calls = %d, want 0 (mutation rolled back with the failed fact)", repo.revokeCalls)
	}
}
