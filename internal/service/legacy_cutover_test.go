package service

import (
	"context"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/repository"
)

type fakeCutoverRepo struct {
	entries map[string]*repository.LegacyCutover
}

func newFakeCutoverRepo() *fakeCutoverRepo {
	return &fakeCutoverRepo{entries: map[string]*repository.LegacyCutover{}}
}
func (f *fakeCutoverRepo) GetCutover(ctx context.Context, tenantID string) (*repository.LegacyCutover, error) {
	entry, ok := f.entries[tenantID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return entry, nil
}
func (f *fakeCutoverRepo) SetCutover(ctx context.Context, cutover *repository.LegacyCutover) error {
	now := time.Now().UTC()
	f.entries[cutover.TenantID] = &repository.LegacyCutover{TenantID: cutover.TenantID, Status: cutover.Status,
		DecidedAt: now, DecidedBy: cutover.DecidedBy, Note: cutover.Note, UpdatedAt: now}
	return nil
}

type fakeCleanupRepo struct {
	items []repository.CleanupReadinessItem
}

func (f *fakeCleanupRepo) CleanupReadiness(ctx context.Context, tenantID string) ([]repository.CleanupReadinessItem, error) {
	return f.items, nil
}

func cutoverTestService(cutover *fakeCutoverRepo, cleanup repository.LegacyCleanupRepository) *legacyCutoverService {
	svc := NewLegacyCutoverService(cutover, cleanup, nil).(*legacyCutoverService)
	svc.actor = func(ctx context.Context) string { return "admin-1" }
	return svc
}

// TestCutoverTransitions: shadow -> cut_over -> rolled_back is legal; jumping
// straight to cut_over from legacy or re-cutting over after rollback is not.
func TestCutoverTransitions(t *testing.T) {
	ctx := context.Background()
	svc := cutoverTestService(newFakeCutoverRepo(), &fakeCleanupRepo{})

	if status, err := svc.Get(ctx, "tenant-a"); err != nil || status != repository.LegacyCutoverStatusLegacy {
		t.Fatalf("default status = %s, %v; want legacy", status, err)
	}
	if _, err := svc.Set(ctx, "tenant-a", repository.LegacyCutoverStatusCutOver, "skip shadow"); err == nil {
		t.Fatal("legacy -> cut_over must be rejected (shadow is mandatory)")
	}
	if _, err := svc.Set(ctx, "tenant-a", repository.LegacyCutoverStatusShadow, "dual read"); err != nil {
		t.Fatalf("legacy -> shadow: %v", err)
	}
	if _, err := svc.Set(ctx, "tenant-a", repository.LegacyCutoverStatusCutOver, "moved"); err != nil {
		t.Fatalf("shadow -> cut_over: %v", err)
	}
	cutOver, err := svc.IsCutOver(ctx, "tenant-a")
	if err != nil || !cutOver {
		t.Fatalf("IsCutOver = %v, %v; want true", cutOver, err)
	}
	if _, err := svc.Set(ctx, "tenant-a", repository.LegacyCutoverStatusRolledBack, "rollback"); err != nil {
		t.Fatalf("cut_over -> rolled_back: %v", err)
	}
	if cutOver, _ := svc.IsCutOver(ctx, "tenant-a"); cutOver {
		t.Fatal("rolled-back tenant must serve legacy again")
	}
	// Rolled back -> shadow again is legal (retry), cut_over again is not.
	if _, err := svc.Set(ctx, "tenant-a", repository.LegacyCutoverStatusCutOver, ""); err == nil {
		t.Fatal("rolled_back -> cut_over must be rejected")
	}
	if _, err := svc.Set(ctx, "tenant-a", repository.LegacyCutoverStatusShadow, "retry"); err != nil {
		t.Fatalf("rolled_back -> shadow: %v", err)
	}
}

// TestCleanupReadiness: all checks ready => ready; any failure => not ready.
func TestCleanupReadiness(t *testing.T) {
	ctx := context.Background()
	cutover := newFakeCutoverRepo()
	svc := cutoverTestService(cutover, &fakeCleanupRepo{items: []repository.CleanupReadinessItem{
		{Check: "legacy_rows_drained", Ready: true},
		{Check: "jobs_drained", Ready: true},
		{Check: "audit_retained", Ready: true},
	}})
	svc.rollbackWindow = 24 * time.Hour
	// Complete a cutover and age it past the rollback window.
	_, _ = svc.Set(ctx, "tenant-a", repository.LegacyCutoverStatusShadow, "")
	_, _ = svc.Set(ctx, "tenant-a", repository.LegacyCutoverStatusCutOver, "")
	cutover.entries["tenant-a"].DecidedAt = time.Now().UTC().Add(-48 * time.Hour)
	items, ready, err := svc.CleanupReadiness(ctx, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Errorf("all checks ready but overall = false: %+v", items)
	}

	blocked := cutoverTestService(cutover, &fakeCleanupRepo{items: []repository.CleanupReadinessItem{
		{Check: "legacy_rows_drained", Ready: false, Details: "42 rows"},
	}})
	blocked.rollbackWindow = 24 * time.Hour
	_, ready, err = blocked.CleanupReadiness(ctx, "tenant-a")
	if err != nil || ready {
		t.Errorf("un-drained rows must block cleanup: ready=%v err=%v", ready, err)
	}
}

// TestCleanupReadinessRollbackWindow: a fresh cut_over decision keeps the
// rollback window open; an old one satisfies it.
func TestCleanupReadinessRollbackWindow(t *testing.T) {
	ctx := context.Background()
	cutover := newFakeCutoverRepo()
	svc := cutoverTestService(cutover, &fakeCleanupRepo{items: []repository.CleanupReadinessItem{
		{Check: "legacy_rows_drained", Ready: true},
		{Check: "jobs_drained", Ready: true},
		{Check: "audit_retained", Ready: true},
	}})
	svc.rollbackWindow = 24 * time.Hour
	if _, err := svc.Set(ctx, "tenant-a", repository.LegacyCutoverStatusShadow, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Set(ctx, "tenant-a", repository.LegacyCutoverStatusCutOver, ""); err != nil {
		t.Fatal(err)
	}
	items, ready, err := svc.CleanupReadiness(ctx, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Error("fresh cut_over decision must keep the rollback window open")
	}
	// Age the decision past the window.
	cutover.entries["tenant-a"].DecidedAt = time.Now().UTC().Add(-48 * time.Hour)
	items, ready, err = svc.CleanupReadiness(ctx, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Errorf("rollback window elapsed but not ready: %+v", items)
	}
}
