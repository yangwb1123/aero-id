package service

import (
	"context"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type fakeHoldRepo struct {
	holds map[string]*repository.AuditHold // key: scope_type|scope_id
}

func newFakeHoldRepo() *fakeHoldRepo {
	return &fakeHoldRepo{holds: map[string]*repository.AuditHold{}}
}
func (f *fakeHoldRepo) PutHold(ctx context.Context, hold *repository.AuditHold) error {
	key := string(hold.ScopeType) + "|" + hold.ScopeID
	if existing, ok := f.holds[key]; ok {
		existing.Reason = hold.Reason
		existing.ExpiresAt = hold.ExpiresAt
		existing.CreatedBy = hold.CreatedBy
		existing.CreatedAt = time.Now().UTC()
		return nil
	}
	f.holds[key] = &repository.AuditHold{ID: idgen.NewID(), TenantID: hold.TenantID, ScopeType: hold.ScopeType,
		ScopeID: hold.ScopeID, Reason: hold.Reason, CreatedBy: hold.CreatedBy,
		CreatedAt: time.Now().UTC(), ExpiresAt: hold.ExpiresAt}
	return nil
}
func (f *fakeHoldRepo) RemoveHold(ctx context.Context, tenantID string, scopeType repository.AuditHoldScope, scopeID string) error {
	delete(f.holds, string(scopeType)+"|"+scopeID)
	return nil
}
func (f *fakeHoldRepo) ListActiveHolds(ctx context.Context, tenantID string) ([]*repository.AuditHold, error) {
	out := []*repository.AuditHold{}
	for _, hold := range f.holds {
		if hold.ExpiresAt != nil && hold.ExpiresAt.Before(time.Now().UTC()) {
			continue
		}
		out = append(out, hold)
	}
	return out, nil
}
func (f *fakeHoldRepo) HoldScopeIDs(ctx context.Context, scopeType repository.AuditHoldScope) ([]string, error) {
	ids := []string{}
	for _, hold := range f.holds {
		if hold.ScopeType != scopeType {
			continue
		}
		if hold.ExpiresAt != nil && hold.ExpiresAt.Before(time.Now().UTC()) {
			continue
		}
		ids = append(ids, hold.ScopeID)
	}
	return ids, nil
}

func holdContext() context.Context {
	return WithIdentity(context.Background(), &domain.TokenIdentity{TenantID: "tenant-a", Subject: "admin-1", UID: "admin-1"})
}

// TestAuditHold_Lifecycle: put, list, expiry, remove.
func TestAuditHold_Lifecycle(t *testing.T) {
	repo := newFakeHoldRepo()
	svc := NewAuditHoldService(repo, nil)

	if _, err := svc.Put(holdContext(), "tenant-a", repository.AuditHoldScopeActor, "u-held", "evidence order #1", nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	holds, err := svc.List(holdContext(), "tenant-a")
	if err != nil || len(holds) != 1 {
		t.Fatalf("list = %d, %v", len(holds), err)
	}
	ids, err := svc.ActiveScopeIDs(context.Background(), repository.AuditHoldScopeActor)
	if err != nil || len(ids) != 1 || ids[0] != "u-held" {
		t.Fatalf("actor scope ids = %v, %v", ids, err)
	}
	if err := svc.Remove(holdContext(), "tenant-a", repository.AuditHoldScopeActor, "u-held"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if ids, _ := svc.ActiveScopeIDs(context.Background(), repository.AuditHoldScopeActor); len(ids) != 0 {
		t.Fatal("hold must be lifted")
	}
}

// TestAuditHold_Validation: bad scope and blank scope_id are rejected; expired
// holds are not active.
func TestAuditHold_Validation(t *testing.T) {
	repo := newFakeHoldRepo()
	svc := NewAuditHoldService(repo, nil)

	if _, err := svc.Put(holdContext(), "tenant-a", repository.AuditHoldScope("bogus"), "x", "", nil); err == nil {
		t.Fatal("invalid scope must be rejected")
	}
	if _, err := svc.Put(holdContext(), "tenant-a", repository.AuditHoldScopeActor, "  ", "", nil); err == nil {
		t.Fatal("blank scope_id must be rejected")
	}
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := svc.Put(holdContext(), "tenant-a", repository.AuditHoldScopeActor, "u-expired", "", &past); err != nil {
		t.Fatal(err)
	}
	if ids, _ := svc.ActiveScopeIDs(context.Background(), repository.AuditHoldScopeActor); len(ids) != 0 {
		t.Fatal("expired hold must not be active")
	}
}
