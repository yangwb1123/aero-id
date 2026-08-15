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

// auditLoggerStub satisfies service.AuditLogger for the internal-package
// region fencing tests (the mocks package cannot be imported here — it imports
// the service package).
type auditLoggerStub struct{}

func (auditLoggerStub) Log(context.Context, *domain.AuditEvent) error { return nil }

// fakeRegionDirectory is an in-memory RegionDirectoryRepository.
type fakeRegionDirectory struct {
	entries   map[domain.ID]*repository.RegionDirectory
	epochs    map[string]int64
	fact      *domain.AuditFact // last fact recorded via PromoteWithFencingWithAudit
	appendErr error             // injected in-tx append failure
}

func newFakeRegionDirectory() *fakeRegionDirectory {
	return &fakeRegionDirectory{entries: map[domain.ID]*repository.RegionDirectory{}, epochs: map[string]int64{}}
}

func (f *fakeRegionDirectory) EnsureDirectory(ctx context.Context, tenantID string, accountID domain.ID, homeRegion, resolverVersion string) error {
	if _, ok := f.entries[accountID]; ok {
		return nil
	}
	f.entries[accountID] = &repository.RegionDirectory{AccountID: accountID, TenantID: tenantID,
		HomeRegion: homeRegion, ResolverVersion: resolverVersion, FencedEpoch: f.epochs[homeRegion]}
	return nil
}
func (f *fakeRegionDirectory) GetDirectory(ctx context.Context, tenantID string, accountID domain.ID) (*repository.RegionDirectory, error) {
	entry, ok := f.entries[accountID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return entry, nil
}
func (f *fakeRegionDirectory) RegionEpoch(ctx context.Context, region string) (int64, error) {
	return f.epochs[region], nil
}
func (f *fakeRegionDirectory) PromoteWithFencing(ctx context.Context, tenantID string, accountID domain.ID, targetRegion string, expectedEpoch int64) (int64, error) {
	entry, ok := f.entries[accountID]
	if !ok {
		return 0, repository.ErrNotFound
	}
	if entry.FencedEpoch != expectedEpoch {
		return 0, repository.ErrRegionEpochConflict
	}
	newEpoch := expectedEpoch + 1
	f.epochs[targetRegion] = newEpoch
	entry.HomeRegion = targetRegion
	entry.FencedEpoch = newEpoch
	return newEpoch, nil
}

func fencingTestAccount(region string) *domain.Account {
	return &domain.Account{ID: idgen.NewID(), TenantID: "tenant-a", CanonicalUID: "u-1", HomeRegion: region, Status: domain.AccountActive}
}

func newFencedAccountService(f *fakeRegionDirectory) *accountService {
	return NewAccountService(nil,
		WithAccountIssuer("snaplink"), WithAccountRegion("local"), WithAccountHomeRegionVersion("v1"),
		WithAccountRegionDirectory(f, "v1")).(*accountService)
}

// WithAccountHomeRegionVersion is a small helper for tests.
func WithAccountHomeRegionVersion(version string) AccountOption {
	return func(s *accountService) {
		if version != "" {
			s.homeVersion = version
		}
	}
}

// TestVerifyWriteFence_AllowsCurrentWriter: the local region holding the fenced
// epoch may write.
func TestVerifyWriteFence_AllowsCurrentWriter(t *testing.T) {
	dir := newFakeRegionDirectory()
	svc := newFencedAccountService(dir)
	account := fencingTestAccount("local")
	if err := dir.EnsureDirectory(context.Background(), account.TenantID, account.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.verifyAccountWriteFence(context.Background(), account); err != nil {
		t.Fatalf("current writer must pass: %v", err)
	}
}

// TestVerifyWriteFence_RefusesAfterPromotion: after the writer lease moved to
// another region, the old region is fenced out (fail-closed, no split-brain).
func TestVerifyWriteFence_RefusesAfterPromotion(t *testing.T) {
	dir := newFakeRegionDirectory()
	svc := newFencedAccountService(dir)
	account := fencingTestAccount("local")
	if err := dir.EnsureDirectory(context.Background(), account.TenantID, account.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	newEpoch, err := dir.PromoteWithFencing(context.Background(), account.TenantID, account.ID, "us-east", 0)
	if err != nil || newEpoch != 1 {
		t.Fatalf("promote = %d, %v; want 1, nil", newEpoch, err)
	}
	err = svc.verifyAccountWriteFence(context.Background(), account)
	if err == nil {
		t.Fatal("old region must be fenced out after promotion")
	}
	// The new region passes.
	account.HomeRegion = "us-east"
	svc.region = "us-east"
	if err := svc.verifyAccountWriteFence(context.Background(), account); err != nil {
		t.Fatalf("promoted region must write: %v", err)
	}
}

// TestVerifyWriteFence_LazyEnsuresPreExistingAccounts: accounts without a
// directory row (created before fencing) are lazily recorded and may write in
// their home region — single-region compatibility.
func TestVerifyWriteFence_LazyEnsuresPreExistingAccounts(t *testing.T) {
	dir := newFakeRegionDirectory()
	svc := newFencedAccountService(dir)
	account := fencingTestAccount("local")
	if err := svc.verifyAccountWriteFence(context.Background(), account); err != nil {
		t.Fatalf("lazy ensure must pass in the home region: %v", err)
	}
	if _, err := dir.GetDirectory(context.Background(), account.TenantID, account.ID); err != nil {
		t.Fatalf("directory row must exist after lazy ensure: %v", err)
	}
	// A non-home region still refuses.
	other := fencingTestAccount("us-east")
	svc.region = "local"
	if err := svc.verifyAccountWriteFence(context.Background(), other); err == nil {
		t.Fatal("non-home region write must be refused")
	}
}

// TestVerifyWriteFence_DisabledWithoutRepository: no fencing wired => no-op.
func TestVerifyWriteFence_DisabledWithoutRepository(t *testing.T) {
	svc := NewAccountService(nil, WithAccountRegion("local")).(*accountService)
	account := fencingTestAccount("local")
	if err := svc.verifyAccountWriteFence(context.Background(), account); err != nil {
		t.Fatalf("fencing disabled must be a no-op: %v", err)
	}
}

// TestRegionFencingPromote_CASConflict: a stale expected epoch loses.
func TestRegionFencingPromote_CASConflict(t *testing.T) {
	dir := newFakeRegionDirectory()
	svc := NewRegionFencingService(dir)
	account := fencingTestAccount("local")
	if err := dir.EnsureDirectory(context.Background(), account.TenantID, account.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Promote(context.Background(), account.TenantID, account.ID, "us-east", 0); err != nil {
		t.Fatalf("first promote: %v", err)
	}
	if _, err := svc.Promote(context.Background(), account.TenantID, account.ID, "eu-west", 0); err == nil {
		t.Fatal("stale expected epoch must conflict")
	}
}

// ── B3 Gap-4 audit site (S3 in-tx promote) ──────────────────────────────────

func (f *fakeRegionDirectory) PromoteWithFencingWithAudit(ctx context.Context, tenantID string, accountID domain.ID,
	targetRegion string, expectedEpoch int64, fact *domain.AuditFact) (int64, error) {
	if f.appendErr != nil {
		// Models the in-tx rollback: the append failure aborts the promotion,
		// so the fake's state must NOT advance.
		return 0, f.appendErr
	}
	f.fact = fact
	return f.PromoteWithFencing(ctx, tenantID, accountID, targetRegion, expectedEpoch)
}

func TestRegionFencing_PromoteAuditFactCommitted(t *testing.T) {
	dir := newFakeRegionDirectory()
	account := fencingTestAccount("local")
	if err := dir.EnsureDirectory(t.Context(), account.TenantID, account.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	svc := NewRegionFencingService(dir, WithRegionFencingAuditLogger(auditLoggerStub{}))
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "u-1", Issuer: "snaplink", Subject: "u-1", TenantID: account.TenantID})
	newEpoch, err := svc.Promote(ctx, account.TenantID, account.ID, "us-east", 0)
	if err != nil {
		t.Fatal(err)
	}
	if newEpoch != 1 {
		t.Fatalf("epoch = %d, want 1", newEpoch)
	}
	if dir.fact == nil {
		t.Fatal("region.promote audit fact was not recorded")
	}
	if dir.fact.Action != domain.AuditRegionPromote {
		t.Fatalf("action = %q, want %q", dir.fact.Action, domain.AuditRegionPromote)
	}
	if dir.fact.TenantID != account.TenantID {
		t.Fatalf("tenant = %q, want %q", dir.fact.TenantID, account.TenantID)
	}
	if dir.fact.ResourceType != "region_directory" || dir.fact.ResourceID != account.ID.String() {
		t.Fatalf("resource = (%q,%q), want (region_directory,%s)", dir.fact.ResourceType, dir.fact.ResourceID, account.ID.String())
	}
	if dir.fact.Payload["target_region"] != "us-east" {
		t.Fatalf("payload target_region = %v, want us-east", dir.fact.Payload["target_region"])
	}
	if epoch, ok := dir.fact.Payload["expected_epoch"].(int64); !ok || epoch != 0 {
		t.Fatalf("payload expected_epoch = %v (%T), want int64(0)", dir.fact.Payload["expected_epoch"], dir.fact.Payload["expected_epoch"])
	}
	// The EventID is the deterministic per-attempt anchor (retry dedup).
	wantID := idgen.Anchor("audit", "region.promote", account.TenantID, account.ID.String(), "us-east", "0")
	if dir.fact.EventID != wantID {
		t.Fatalf("event ID = %v, want %v (deterministic per-attempt anchor)", dir.fact.EventID, wantID)
	}
	if dir.fact.ActorUID != "u-1" {
		t.Fatalf("actor = %q, want u-1 (ctx actor)", dir.fact.ActorUID)
	}
}

func TestRegionFencing_PromoteAuditFailureFailsClosed(t *testing.T) {
	dir := newFakeRegionDirectory()
	account := fencingTestAccount("local")
	if err := dir.EnsureDirectory(t.Context(), account.TenantID, account.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	dir.appendErr = errors.New("append region promotion audit: ledger down")
	svc := NewRegionFencingService(dir, WithRegionFencingAuditLogger(auditLoggerStub{}))
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "u-1", Issuer: "snaplink", Subject: "u-1", TenantID: account.TenantID})
	_, err := svc.Promote(ctx, account.TenantID, account.ID, "us-east", 0)
	var ae *apperr.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *AppError, got %v", err)
	}
	if ae.Code != "region.promotion_failed" {
		t.Fatalf("code = %q, want region.promotion_failed (in-tx rollback)", ae.Code)
	}
	// The promotion must NOT have committed: epoch unchanged, directory unmoved.
	entry, gerr := dir.GetDirectory(ctx, account.TenantID, account.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if entry.HomeRegion != "local" || entry.FencedEpoch != 0 {
		t.Fatalf("promotion committed despite the audit failure: %+v", entry)
	}
	if epoch, eerr := dir.RegionEpoch(ctx, "us-east"); eerr != nil || epoch != 0 {
		t.Fatalf("us-east epoch = %d, %v; want 0 (no bump)", epoch, eerr)
	}
}
