package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/region"
	"github.com/aero/aero-id/internal/repository"
)

// fakeBackfillRepo implements both AccountRepository and
// LegacyBackfillRepository for backfill tests.
type fakeBackfillRepo struct {
	uids      []string // legacy uids returned by ListLegacyUIDs
	identities map[string]bool // "tenant|issuer|subject" already mapped
	created   map[string]string // subject -> home region of created accounts
}

func (f *fakeBackfillRepo) EnsureByIdentity(ctx context.Context, tenantID, issuer, subject, canonicalUID, homeRegion string) (*domain.Account, error) {
	key := tenantID + "|" + issuer + "|" + subject
	if f.identities[key] {
		return &domain.Account{ID: idgen.NewID(), TenantID: tenantID, CanonicalUID: canonicalUID, HomeRegion: homeRegion}, nil
	}
	f.identities[key] = true
	f.created[subject] = homeRegion
	return &domain.Account{ID: idgen.NewID(), TenantID: tenantID, CanonicalUID: canonicalUID, HomeRegion: homeRegion}, nil
}

func (f *fakeBackfillRepo) FindByIdentity(ctx context.Context, tenantID, issuer, subject string) (*domain.Account, error) {
	key := tenantID + "|" + issuer + "|" + subject
	if f.identities[key] {
		return &domain.Account{ID: idgen.NewID(), TenantID: tenantID, CanonicalUID: subject}, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeBackfillRepo) ListLegacyUIDs(ctx context.Context, tenantID, issuer string, limit int) ([]string, error) {
	if limit <= 0 || len(f.uids) <= limit {
		return f.uids, nil
	}
	return f.uids[:limit], nil
}

func (f *fakeBackfillRepo) CountLegacyUIDs(ctx context.Context) (int64, error) {
	return int64(len(f.uids)), nil
}

// --- AccountRepository methods unused by the backfill path ---

func (f *fakeBackfillRepo) GetByID(ctx context.Context, tenantID string, id domain.ID) (*domain.Account, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeBackfillRepo) GetProfile(ctx context.Context, id domain.ID) (*domain.AccountProfile, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeBackfillRepo) EraseAccount(ctx context.Context, tenantID string, id domain.ID) error { return nil }
func (f *fakeBackfillRepo) UpdateProfile(ctx context.Context, p *domain.AccountProfile) error     { return nil }
func (f *fakeBackfillRepo) ListSources(ctx context.Context, accountID domain.ID, page domain.Page) ([]*domain.AccountSource, string, error) {
	return nil, "", nil
}
func (f *fakeBackfillRepo) ListMemberships(ctx context.Context, accountID domain.ID, page domain.Page) ([]*domain.AccountMembership, string, error) {
	return nil, "", nil
}
func (f *fakeBackfillRepo) UpsertSource(ctx context.Context, s *domain.AccountSource) error   { return nil }
func (f *fakeBackfillRepo) UpsertMembership(ctx context.Context, m *domain.AccountMembership) error {
	return nil
}
func (f *fakeBackfillRepo) ListAccounts(ctx context.Context, filter repository.AccountListFilter) ([]*domain.Account, string, error) {
	return nil, "", nil
}

func newBackfillService(f *fakeBackfillRepo) AccountService {
	return NewAccountService(f,
		WithAccountIssuer("snaplink"),
		WithAccountRegion("local"),
		WithAccountHomeRegionResolver(region.NewResolver("v1", []string{"local", "us-east", "eu-west"})),
	)
}

func TestBackfillLegacyIdentities_CreatesShadowAccounts(t *testing.T) {
	f := &fakeBackfillRepo{
		uids:       []string{"u1", "u2", "u1", ""},
		identities: map[string]bool{},
		created:    map[string]string{},
	}
	svc := newBackfillService(f)
	result, err := svc.BackfillLegacyIdentities(context.Background(), 0)
	if err != nil {
		t.Fatalf("backfill failed: %v", err)
	}
	if result.Scanned != 4 {
		t.Errorf("scanned = %d, want 4", result.Scanned)
	}
	if result.Created != 2 {
		t.Errorf("created = %d, want 2 (u1, u2; duplicate u1 skipped by repo dedup)", result.Created)
	}
	if result.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (blank uid)", result.Skipped)
	}
	if !result.Completed {
		t.Error("completed = false, want true (4 uids < default limit 500)")
	}
	if result.Tenant != LegacyBackfillTenant || result.Issuer != "snaplink" {
		t.Errorf("tenant/issuer = %s/%s, want legacy/snaplink", result.Tenant, result.Issuer)
	}
	// Home region resolved deterministically by (issuer, subject).
	if f.created["u1"] == "" || f.created["u2"] == "" {
		t.Fatalf("created map incomplete: %v", f.created)
	}
	if f.created["u1"] == f.created["u2"] {
		t.Logf("note: subjects hashed to same region (%s)", f.created["u1"])
	}
}

func TestBackfillLegacyIdentities_ExistingMapped(t *testing.T) {
	f := &fakeBackfillRepo{
		uids:       []string{"u1"},
		identities: map[string]bool{"legacy|snaplink|u1": true},
		created:    map[string]string{},
	}
	svc := newBackfillService(f)
	result, err := svc.BackfillLegacyIdentities(context.Background(), 100)
	if err != nil {
		t.Fatalf("backfill failed: %v", err)
	}
	if result.Existing != 1 || result.Created != 0 {
		t.Errorf("existing/created = %d/%d, want 1/0", result.Existing, result.Created)
	}
	if len(f.created) != 0 {
		t.Errorf("no account should be created, got %v", f.created)
	}
	if !result.Completed {
		t.Error("completed = false, want true (1 uid < limit 100)")
	}
}

func TestBackfillLegacyIdentities_LookupFailureSkipped(t *testing.T) {
	f := &fakeBackfillRepo{
		uids:       []string{"u1"},
		identities: map[string]bool{},
		created:    map[string]string{},
	}
	// Break FindByIdentity by using an invalid tenant marker: simulate lookup
	// error by making identities return an error for a poisoned subject.
	poisoned := &poisonBackfillRepo{fake: f}
	svc := NewAccountService(poisoned,
		WithAccountIssuer("snaplink"),
		WithAccountRegion("local"),
	)
	result, err := svc.BackfillLegacyIdentities(context.Background(), 10)
	if err != nil {
		t.Fatalf("backfill failed: %v", err)
	}
	if result.Skipped != 1 || result.Created != 0 {
		t.Errorf("skipped/created = %d/%d, want 1/0", result.Skipped, result.Created)
	}
}

func TestBackfillLegacyIdentities_BoundedBatch(t *testing.T) {
	f := &fakeBackfillRepo{
		uids:       []string{"u1", "u2", "u3", "u4", "u5"},
		identities: map[string]bool{},
		created:    map[string]string{},
	}
	svc := newBackfillService(f)
	result, err := svc.BackfillLegacyIdentities(context.Background(), 3)
	if err != nil {
		t.Fatalf("backfill failed: %v", err)
	}
	if result.Created != 3 {
		t.Errorf("created = %d, want 3 (bounded by limit)", result.Created)
	}
	if result.Completed {
		t.Error("completed = true, want false (5 uids > limit 3; another run needed)")
	}
}

type poisonBackfillRepo struct {
	fake *fakeBackfillRepo
}

func (p *poisonBackfillRepo) FindByIdentity(ctx context.Context, tenantID, issuer, subject string) (*domain.Account, error) {
	return nil, errors.New("lookup exploded")
}
func (p *poisonBackfillRepo) EnsureByIdentity(ctx context.Context, tenantID, issuer, subject, canonicalUID, homeRegion string) (*domain.Account, error) {
	return p.fake.EnsureByIdentity(ctx, tenantID, issuer, subject, canonicalUID, homeRegion)
}
func (p *poisonBackfillRepo) ListLegacyUIDs(ctx context.Context, tenantID, issuer string, limit int) ([]string, error) {
	return p.fake.ListLegacyUIDs(ctx, tenantID, issuer, limit)
}
func (p *poisonBackfillRepo) CountLegacyUIDs(ctx context.Context) (int64, error) {
	return p.fake.CountLegacyUIDs(ctx)
}
func (p *poisonBackfillRepo) GetByID(ctx context.Context, tenantID string, id domain.ID) (*domain.Account, error) {
	return nil, repository.ErrNotFound
}
func (p *poisonBackfillRepo) GetProfile(ctx context.Context, id domain.ID) (*domain.AccountProfile, error) {
	return nil, repository.ErrNotFound
}
func (p *poisonBackfillRepo) EraseAccount(ctx context.Context, tenantID string, id domain.ID) error { return nil }
func (p *poisonBackfillRepo) UpdateProfile(ctx context.Context, profile *domain.AccountProfile) error {
	return nil
}
func (p *poisonBackfillRepo) ListSources(ctx context.Context, accountID domain.ID, page domain.Page) ([]*domain.AccountSource, string, error) {
	return nil, "", nil
}
func (p *poisonBackfillRepo) ListMemberships(ctx context.Context, accountID domain.ID, page domain.Page) ([]*domain.AccountMembership, string, error) {
	return nil, "", nil
}
func (p *poisonBackfillRepo) UpsertSource(ctx context.Context, source *domain.AccountSource) error { return nil }
func (p *poisonBackfillRepo) UpsertMembership(ctx context.Context, membership *domain.AccountMembership) error {
	return nil
}
func (p *poisonBackfillRepo) ListAccounts(ctx context.Context, filter repository.AccountListFilter) ([]*domain.Account, string, error) {
	return nil, "", nil
}
