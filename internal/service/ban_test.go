package service_test

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/service/mocks"
)

// adminPermitter grants every permission; the embedded interface satisfies the
// untouched AdminService methods.
type adminPermitter struct {
	service.AdminService
}

func (adminPermitter) HasPermission(ctx context.Context, uid, perm string) (bool, error) {
	return true, nil
}

// banRepoStub implements the BanRepository surface used by Ban and Unban; the
// embedded interface satisfies the untouched methods. The WithAudit methods
// are the in-tx path the service uses when an auditor is wired (B2-1).
type banRepoStub struct {
	repository.BanRepository
	banCalls   int
	unbanCalls int
	auditErr   error
	banFacts   []*domain.AuditFact
	unbanFacts []*domain.AuditFact
}

func (f *banRepoStub) Ban(ctx context.Context, uid, actorUID, reason string, expiresAt *time.Time) error {
	f.banCalls++
	return nil
}

func (f *banRepoStub) Unban(ctx context.Context, uid string) error {
	f.unbanCalls++
	return nil
}

func (f *banRepoStub) BanWithAudit(ctx context.Context, uid, actorUID, reason string, expiresAt *time.Time, fact *domain.AuditFact) error {
	if f.auditErr != nil {
		return f.auditErr
	}
	f.banCalls++
	f.banFacts = append(f.banFacts, fact)
	return nil
}

func (f *banRepoStub) UnbanWithAudit(ctx context.Context, uid string, fact *domain.AuditFact) error {
	if f.auditErr != nil {
		return f.auditErr
	}
	f.unbanCalls++
	f.unbanFacts = append(f.unbanFacts, fact)
	return nil
}

// TestBanAuditFailureFailsClosed (B2-1): the in-tx WithAudit path fails
// closed — the request fails and the ban never commits (same transaction as
// the fact).
func TestBanAuditFailureFailsClosed(t *testing.T) {
	repo := &banRepoStub{auditErr: stderrors.New("ledger down")}
	svc := service.NewBanService(repo, adminPermitter{}, service.WithBanAuditLogger(&mocks.Auditor{}))
	ctx := service.WithActor(context.Background(), "admin-1")

	err := svc.Ban(ctx, "u-2", "spam", 0)
	assertAppCode(t, err, 500)
	if repo.banCalls != 0 {
		t.Fatalf("ban repo calls = %d, want 0 (mutation rolled back with the failed fact)", repo.banCalls)
	}
}

// TestUnbanAuditFailureFailsClosed (B2-1): same fail-closed contract for unban.
func TestUnbanAuditFailureFailsClosed(t *testing.T) {
	repo := &banRepoStub{auditErr: stderrors.New("ledger down")}
	svc := service.NewBanService(repo, adminPermitter{}, service.WithBanAuditLogger(&mocks.Auditor{}))
	ctx := service.WithActor(context.Background(), "admin-1")

	err := svc.Unban(ctx, "u-2")
	assertAppCode(t, err, 500)
	if repo.unbanCalls != 0 {
		t.Fatalf("unban repo calls = %d, want 0 (mutation rolled back with the failed fact)", repo.unbanCalls)
	}
}
