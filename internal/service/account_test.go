package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// accountViewRepo implements the AccountRepository surface used by
// GetAccount/ListAccounts (permission checker + repo). The embedded interface
// satisfies the untouched AccountRepository methods.
type accountViewRepo struct {
	repository.AccountRepository
	account *domain.Account
	rows    []*domain.Account
}

func (f *accountViewRepo) GetByID(ctx context.Context, tenantID string, id domain.ID) (*domain.Account, error) {
	if f.account == nil {
		return nil, repository.ErrNotFound
	}
	return f.account, nil
}
func (f *accountViewRepo) ListAccounts(ctx context.Context, filter repository.AccountListFilter) ([]*domain.Account, string, error) {
	return f.rows, "", nil
}
func (f *accountViewRepo) GetProfile(ctx context.Context, id domain.ID) (*domain.AccountProfile, error) {
	return nil, repository.ErrNotFound
}
func (f *accountViewRepo) ListSources(ctx context.Context, accountID domain.ID, page domain.Page) ([]*domain.AccountSource, string, error) {
	return nil, "", nil
}
func (f *accountViewRepo) ListMemberships(ctx context.Context, accountID domain.ID, page domain.Page) ([]*domain.AccountMembership, string, error) {
	return nil, "", nil
}

// allowAllChecker grants every permission.
type allowAllChecker struct{}

func (allowAllChecker) HasPermission(ctx context.Context, uid, permission string) (bool, error) { return true, nil }

func newAccountViewService(repo repository.AccountRepository, audit AuditLedgerService) *accountService {
	return NewAccountService(repo,
		WithAccountPermissionChecker(allowAllChecker{}),
		WithAccountIssuer("snaplink"),
		WithAccountRegion("local"),
		WithAccountAudit(audit),
	).(*accountService)
}

func accountViewCtx(t *testing.T) context.Context {
	t.Helper()
	return WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", Issuer: "snaplink",
		Subject: "admin-1", TenantID: "tenant-a"})
}

// TestAccountViewedListedFailClosedOnLedgerOutage (FM-10 / N2 decomposition):
// the account.viewed/account.listed read facts have no account-row write to
// join, so the service must fail the admin read with 503 audit.ingest_failed
// when the ledger append fails — never a silent success without the fact.
func TestAccountViewedListedFailClosedOnLedgerOutage(t *testing.T) {
	ctx := accountViewCtx(t)
	account := &domain.Account{ID: idgen.NewID(), TenantID: "tenant-a", CanonicalUID: "u-1",
		HomeRegion: "local", Status: domain.AccountActive}
	failing := &auditLedgerRepoSpy{appendErr: errors.New("ledger down")}
	svc := newAccountViewService(&accountViewRepo{account: account}, NewAuditLedgerService(failing))

	if _, err := svc.GetAccount(ctx, account.ID); err == nil {
		t.Fatal("GetAccount must fail when the audit append fails")
	} else if !isAuditIngestFailed(err) {
		t.Fatalf("GetAccount error = %v, want audit.ingest_failed (503)", err)
	}
	if _, err := svc.ListAccounts(ctx, ListAccountsInput{Page: domain.Page{Limit: 10}}); err == nil {
		t.Fatal("ListAccounts must fail when the audit append fails")
	} else if !isAuditIngestFailed(err) {
		t.Fatalf("ListAccounts error = %v, want audit.ingest_failed (503)", err)
	}
}

// TestAccountViewedListedSucceedWhenAuditOk: the same reads succeed when the
// ledger accepts the facts.
func TestAccountViewedListedSucceedWhenAuditOk(t *testing.T) {
	ctx := accountViewCtx(t)
	account := &domain.Account{ID: idgen.NewID(), TenantID: "tenant-a", CanonicalUID: "u-1",
		HomeRegion: "local", Status: domain.AccountActive}
	repo := &accountViewRepo{account: account, rows: []*domain.Account{account}}
	svc := newAccountViewService(repo, NewAuditLedgerService(&auditLedgerRepoSpy{}))

	if _, err := svc.GetAccount(ctx, account.ID); err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if _, err := svc.ListAccounts(ctx, ListAccountsInput{Page: domain.Page{Limit: 10}}); err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
}

func isAuditIngestFailed(err error) bool {
	var appErr *apperr.AppError
	if errors.As(err, &appErr) {
		return appErr.Code == "audit.ingest_failed" && appErr.HTTP == http.StatusServiceUnavailable
	}
	return false
}
