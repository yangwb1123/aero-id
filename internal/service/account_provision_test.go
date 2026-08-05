package service

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type pendingAccountQuotaRepo struct {
	repository.AccountRepository
	account       *domain.Account
	reservationID string
	committed     bool
}

func (repo *pendingAccountQuotaRepo) FindByIdentity(context.Context, string, string, string) (*domain.Account, error) {
	return repo.account, nil
}

func (repo *pendingAccountQuotaRepo) EnsureByIdentityWithQuota(context.Context, string, string, string,
	string, string, string) (*domain.Account, error) {
	return repo.account, nil
}

func (repo *pendingAccountQuotaRepo) PendingAccountQuota(context.Context, string, domain.ID) (string, error) {
	if repo.committed {
		return "", repository.ErrNotFound
	}
	return repo.reservationID, nil
}

func (repo *pendingAccountQuotaRepo) MarkAccountQuotaCommitted(_ context.Context, _ string,
	_ domain.ID, reservationID string) error {
	if reservationID != repo.reservationID {
		return repository.ErrNotFound
	}
	repo.committed = true
	return nil
}

type accountQuotaSpy struct {
	commits int
}

func (*accountQuotaSpy) ReserveAccount(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (quota *accountQuotaSpy) CommitAccount(context.Context, string, string, domain.ID) error {
	quota.commits++
	return nil
}

func (*accountQuotaSpy) ReleaseAccount(context.Context, string, string) error { return nil }

func TestExistingAccountCompletesPendingQuotaSaga(t *testing.T) {
	account := &domain.Account{ID: idgen.NewID(), TenantID: "tenant-a", CanonicalUID: "user-1",
		HomeRegion: "local", Status: domain.AccountActive}
	repo := &pendingAccountQuotaRepo{account: account, reservationID: "usr-res-stable"}
	quota := &accountQuotaSpy{}
	service := &accountService{repo: repo, quota: quota, issuer: "snaplink", region: "local"}
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "user-1", Issuer: "snaplink",
		Subject: "user-1", TenantID: "tenant-a"})

	if _, err := service.ensure(ctx); err != nil {
		t.Fatalf("first ensure() error = %v", err)
	}
	if _, err := service.ensure(ctx); err != nil {
		t.Fatalf("second ensure() error = %v", err)
	}
	if quota.commits != 1 || !repo.committed {
		t.Fatalf("quota saga commits/marked = %d/%v", quota.commits, repo.committed)
	}
}
