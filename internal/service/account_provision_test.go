package service

import (
	"context"
	"errors"
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
	commits  int
	releases int
}

func (*accountQuotaSpy) ReserveAccount(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (quota *accountQuotaSpy) CommitAccount(context.Context, string, string, domain.ID) error {
	quota.commits++
	return nil
}

func (quota *accountQuotaSpy) ReleaseAccount(context.Context, string, string) error {
	quota.releases++
	return nil
}

func TestProvisionAccountReleasesReservationAfterPersistenceFailure(t *testing.T) {
	persistErr := errors.New("account persistence failed")
	repo := &provisionQuotaRepo{account: provisionedTestAccount(), ensureErr: persistErr}
	quota := &provisionQuotaSpy{reservationID: "usr-res-preserve"}
	svc := &accountService{repo: repo, quota: quota, audit: &auditRecordSpy{}, issuer: "snaplink", region: "local"}

	_, err := svc.provisionAccount(t.Context(), "tenant-a", "snaplink", "user-1", "local")
	if !errors.Is(err, persistErr) {
		t.Fatalf("provision error = %v, want persistence error", err)
	}
	if len(quota.releases) != 1 || quota.releases[0].reservationID != "usr-res-preserve" ||
		quota.releases[0].tenantID != "tenant-a" {
		t.Fatalf("release calls = %+v, want one matching reservation", quota.releases)
	}
	if repo.markCalls != 0 || quota.commits != 0 {
		t.Fatalf("post-persistence calls = mark:%d commit:%d, want zero", repo.markCalls, quota.commits)
	}
}

type provisioningContextKey string

func TestProvisionAccountCompensationSurvivesRequestCancellation(t *testing.T) {
	persistErr := errors.New("account persistence failed")
	repo := &provisionQuotaRepo{account: provisionedTestAccount(), ensureErr: persistErr}
	quota := &provisionQuotaSpy{reservationID: "usr-res-canceled"}
	const contextKey provisioningContextKey = "compensation-marker"
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey, "preserved"))
	cancel()
	svc := &accountService{repo: repo, quota: quota, audit: &auditRecordSpy{}, issuer: "snaplink", region: "local"}

	_, err := svc.provisionAccount(ctx, "tenant-a", "snaplink", "user-1", "local")
	if !errors.Is(err, persistErr) {
		t.Fatalf("provision error = %v, want persistence error", err)
	}
	if quota.releaseCtx == nil {
		t.Fatal("compensation context was not captured")
	}
	if quota.releaseCtx.Err() != nil || quota.releaseCtx.Value(contextKey) != "preserved" {
		t.Fatalf("compensation context error/value = %v/%v, want uncanceled context with request value", quota.releaseCtx.Err(), quota.releaseCtx.Value(contextKey))
	}
}

func TestProvisionAccountPreservesPersistenceErrorWhenReleaseFails(t *testing.T) {
	persistErr := errors.New("audit transaction failed")
	releaseErr := errors.New("release unavailable")
	repo := &provisionQuotaRepo{account: provisionedTestAccount(), ensureErr: persistErr}
	quota := &provisionQuotaSpy{reservationID: "usr-res-release-failure", releaseErr: releaseErr}
	svc := &accountService{repo: repo, quota: quota, audit: &auditRecordSpy{}, issuer: "snaplink", region: "local"}

	_, err := svc.provisionAccount(t.Context(), "tenant-a", "snaplink", "user-1", "local")
	if !errors.Is(err, persistErr) || errors.Is(err, releaseErr) {
		t.Fatalf("provision error = %v, want only persistence error", err)
	}
	if len(quota.releases) != 1 {
		t.Fatalf("release calls = %d, want one", len(quota.releases))
	}
}

func TestProvisionAccountDoesNotReleaseAfterPersistenceSucceeds(t *testing.T) {
	tests := []struct {
		name      string
		commitErr error
		markErr   error
	}{
		{name: "success"},
		{name: "remote commit failure", commitErr: errors.New("commit failed")},
		{name: "quota mark failure", markErr: errors.New("mark failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &provisionQuotaRepo{account: provisionedTestAccount(), markErr: test.markErr}
			quota := &provisionQuotaSpy{reservationID: "usr-res-committed", commitErr: test.commitErr}
			svc := &accountService{repo: repo, quota: quota, audit: &auditRecordSpy{}, issuer: "snaplink", region: "local"}

			account, err := svc.provisionAccount(t.Context(), "tenant-a", "snaplink", "user-1", "local")
			if test.commitErr != nil || test.markErr != nil {
				if err == nil {
					t.Fatal("expected quota completion error")
				}
			} else if err != nil || account == nil {
				t.Fatalf("successful provision = %v, %+v", err, account)
			}
			if len(quota.releases) != 0 {
				t.Fatalf("release calls = %d, want zero after persistence", len(quota.releases))
			}
		})
	}
}

type provisionQuotaRepo struct {
	repository.AccountRepository
	account   *domain.Account
	ensureErr error
	markErr   error
	markCalls int
}

func (repo *provisionQuotaRepo) EnsureByIdentityWithAudit(context.Context, string, string, string, string, string,
	domain.ID, *domain.AuditFact) (*domain.Account, error) {
	return repo.account, repo.ensureErr
}

func (repo *provisionQuotaRepo) EnsureByIdentityWithQuotaWithAudit(context.Context, string, string, string,
	string, string, string, domain.ID, *domain.AuditFact) (*domain.Account, error) {
	if repo.ensureErr != nil {
		return nil, repo.ensureErr
	}
	return repo.account, nil
}

func (repo *provisionQuotaRepo) EnsureByIdentityWithQuota(context.Context, string, string, string,
	string, string, string) (*domain.Account, error) {
	return repo.account, repo.ensureErr
}

func (repo *provisionQuotaRepo) PendingAccountQuota(context.Context, string, domain.ID) (string, error) {
	return "", repository.ErrNotFound
}

func (repo *provisionQuotaRepo) MarkAccountQuotaCommitted(context.Context, string, domain.ID, string) error {
	repo.markCalls++
	return repo.markErr
}

func (repo *provisionQuotaRepo) FindByIdentity(context.Context, string, string, string) (*domain.Account, error) {
	return nil, repository.ErrNotFound
}

func provisionedTestAccount() *domain.Account {
	return &domain.Account{ID: idgen.NewID(), TenantID: "tenant-a", CanonicalUID: "user-1", HomeRegion: "local",
		Status: domain.AccountActive}
}

type provisionQuotaRelease struct {
	tenantID      string
	reservationID string
}

type provisionQuotaSpy struct {
	reservationID string
	commitErr     error
	releaseErr    error
	commits       int
	releases      []provisionQuotaRelease
	releaseCtx    context.Context
}

func (quota *provisionQuotaSpy) ReserveAccount(context.Context, string, string, string) (string, error) {
	return quota.reservationID, nil
}

func (quota *provisionQuotaSpy) CommitAccount(context.Context, string, string, domain.ID) error {
	quota.commits++
	return quota.commitErr
}

func (quota *provisionQuotaSpy) ReleaseAccount(ctx context.Context, tenantID, reservationID string) error {
	quota.releaseCtx = ctx
	quota.releases = append(quota.releases, provisionQuotaRelease{tenantID: tenantID, reservationID: reservationID})
	return quota.releaseErr
}

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
	if quota.commits != 1 || quota.releases != 0 || !repo.committed {
		t.Fatalf("quota saga commits/releases/marked = %d/%d/%v", quota.commits, quota.releases, repo.committed)
	}
}
