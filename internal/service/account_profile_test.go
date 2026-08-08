package service

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

func TestValidateAccountPreferencesRejectsCredentialFields(t *testing.T) {
	if err := validateAccountPreferences(map[string]any{"theme": "dark", "access_token": "secret"}); err == nil {
		t.Fatal("credential preference was accepted")
	}
}

func TestValidateAccountPreferencesAcceptsPresentationValues(t *testing.T) {
	if err := validateAccountPreferences(map[string]any{"theme": "dark", "sidebar": map[string]any{"collapsed": true}}); err != nil {
		t.Fatal(err)
	}
}

// profileRepoWithoutAudit implements the minimal account surface the profile
// update path touches (ensure → GetProfile → UpdateProfile) without
// AccountProfileAuditRepository methods — the opRepoWithoutAudit shape.
type profileRepoWithoutAudit struct {
	repository.AccountRepository
	updateCalls int
}

func (f *profileRepoWithoutAudit) EnsureByIdentityWithAudit(context.Context, string, string, string, string, string,
	domain.ID, *domain.AuditFact) (*domain.Account, error) {
	return &domain.Account{ID: idgen.NewID(), TenantID: "tenant-a", CanonicalUID: "u-1", HomeRegion: "local"}, nil
}

func (f *profileRepoWithoutAudit) FindByIdentity(context.Context, string, string, string) (*domain.Account, error) {
	return nil, repository.ErrNotFound
}

func (f *profileRepoWithoutAudit) EnsureByIdentityWithQuotaWithAudit(context.Context, string, string, string, string, string,
	string, domain.ID, *domain.AuditFact) (*domain.Account, error) {
	return nil, nil
}

func (f *profileRepoWithoutAudit) GetProfile(ctx context.Context, id domain.ID) (*domain.AccountProfile, error) {
	return &domain.AccountProfile{AccountID: id}, nil
}

func (f *profileRepoWithoutAudit) UpdateProfile(context.Context, *domain.AccountProfile) error {
	f.updateCalls++
	return nil
}

// profileRepoWithAudit implements AccountProfileAuditRepository and captures
// the fact argument.
type profileRepoWithAudit struct {
	repository.AccountRepository
	updateCalls int
	fact        *domain.AuditFact
}

func (f *profileRepoWithAudit) EnsureByIdentityWithAudit(context.Context, string, string, string, string, string,
	domain.ID, *domain.AuditFact) (*domain.Account, error) {
	return &domain.Account{ID: idgen.NewID(), TenantID: "tenant-a", CanonicalUID: "u-1", HomeRegion: "local"}, nil
}

func (f *profileRepoWithAudit) FindByIdentity(context.Context, string, string, string) (*domain.Account, error) {
	return nil, repository.ErrNotFound
}

func (f *profileRepoWithAudit) EnsureByIdentityWithQuotaWithAudit(context.Context, string, string, string, string, string,
	string, domain.ID, *domain.AuditFact) (*domain.Account, error) {
	return nil, nil
}

func (f *profileRepoWithAudit) GetProfile(ctx context.Context, id domain.ID) (*domain.AccountProfile, error) {
	return &domain.AccountProfile{AccountID: id}, nil
}

func (f *profileRepoWithAudit) UpdateProfile(context.Context, *domain.AccountProfile) error {
	f.updateCalls++
	return nil
}

func (f *profileRepoWithAudit) UpdateProfileWithAudit(ctx context.Context, p *domain.AccountProfile, fact *domain.AuditFact) error {
	f.updateCalls++
	f.fact = fact
	return nil
}

func profileUpdateInput() UpdateAccountProfileInput {
	displayName := "New Name"
	return UpdateAccountProfileInput{DisplayName: &displayName}
}

func profileUpdateCtx() context.Context {
	return WithIdentity(context.Background(), &domain.TokenIdentity{UID: "u-1", Issuer: "snaplink", Subject: "u-1", TenantID: "tenant-a"})
}

// TestProfileUpdateAuditUnavailableWhenRepoLacksWithAudit (REQ-1 / T-5): a
// wired service whose repo lacks AccountProfileAuditRepository fails closed
// with account.profile_audit_unavailable BEFORE any repository mutation — the
// plain UpdateProfile fallback is gone (fail-closed, B2-1 REQ-4 + REQ-2).
func TestProfileUpdateAuditUnavailableWhenRepoLacksWithAudit(t *testing.T) {
	repo := &profileRepoWithoutAudit{}
	audit := &auditRecordSpy{}
	svc := &accountService{repo: repo, audit: audit, issuer: "snaplink", region: "local", permission: allowAllPermission{}}

	_, err := svc.UpdateProfile(profileUpdateCtx(), profileUpdateInput())
	var appErr *apperr.AppError
	if !stderrors.As(err, &appErr) || appErr.Code != "account.profile_audit_unavailable" {
		t.Fatalf("err = %v, want AppError account.profile_audit_unavailable", err)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("plain UpdateProfile calls = %d, want 0 (fail closed before any mutation)", repo.updateCalls)
	}
	if audit.records != 0 {
		t.Fatalf("standalone Record calls = %d, want 0 (single in-tx write path)", audit.records)
	}
}

// TestProfileUpdateWithAuditWiredPath (REQ-1): the repo implementing
// AccountProfileAuditRepository receives the fact with the expected shape and
// the standalone surface is never touched.
func TestProfileUpdateWithAuditWiredPath(t *testing.T) {
	repo := &profileRepoWithAudit{}
	audit := &auditRecordSpy{}
	svc := &accountService{repo: repo, audit: audit, issuer: "snaplink", region: "local", permission: allowAllPermission{}}

	if _, err := svc.UpdateProfile(profileUpdateCtx(), profileUpdateInput()); err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("UpdateProfileWithAudit calls = %d, want 1", repo.updateCalls)
	}
	if repo.fact == nil {
		t.Fatal("no fact captured")
	}
	if repo.fact.Action != "account.profile.updated" || repo.fact.ResourceType != "account_profile" {
		t.Fatalf("fact = %s/%s, want account.profile.updated/account_profile",
			repo.fact.Action, repo.fact.ResourceType)
	}
	if repo.fact.ResourceID == "" {
		t.Fatal("fact ResourceID is empty, want the updated account's id")
	}
	if _, err := idgen.Parse(repo.fact.ResourceID); err != nil {
		t.Fatalf("fact ResourceID = %q is not a valid id: %v", repo.fact.ResourceID, err)
	}
	if audit.records != 0 {
		t.Fatalf("standalone Record calls = %d, want 0 (single write path)", audit.records)
	}
}
