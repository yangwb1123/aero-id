package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
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

	if status, err := svc.Get(ctx, "tenant-a"); err != nil || status != domain.LegacyCutoverStatusLegacy {
		t.Fatalf("default status = %s, %v; want legacy", status, err)
	}
	if _, err := svc.Set(ctx, "tenant-a", domain.LegacyCutoverStatusCutOver, "skip shadow"); err == nil {
		t.Fatal("legacy -> cut_over must be rejected (shadow is mandatory)")
	}
	if _, err := svc.Set(ctx, "tenant-a", domain.LegacyCutoverStatusShadow, "dual read"); err != nil {
		t.Fatalf("legacy -> shadow: %v", err)
	}
	if _, err := svc.Set(ctx, "tenant-a", domain.LegacyCutoverStatusCutOver, "moved"); err != nil {
		t.Fatalf("shadow -> cut_over: %v", err)
	}
	cutOver, err := svc.IsCutOver(ctx, "tenant-a")
	if err != nil || !cutOver {
		t.Fatalf("IsCutOver = %v, %v; want true", cutOver, err)
	}
	if _, err := svc.Set(ctx, "tenant-a", domain.LegacyCutoverStatusRolledBack, "rollback"); err != nil {
		t.Fatalf("cut_over -> rolled_back: %v", err)
	}
	if cutOver, _ := svc.IsCutOver(ctx, "tenant-a"); cutOver {
		t.Fatal("rolled-back tenant must serve legacy again")
	}
	// Rolled back -> shadow again is legal (retry), cut_over again is not.
	if _, err := svc.Set(ctx, "tenant-a", domain.LegacyCutoverStatusCutOver, ""); err == nil {
		t.Fatal("rolled_back -> cut_over must be rejected")
	}
	if _, err := svc.Set(ctx, "tenant-a", domain.LegacyCutoverStatusShadow, "retry"); err != nil {
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
	_, _ = svc.Set(ctx, "tenant-a", domain.LegacyCutoverStatusShadow, "")
	_, _ = svc.Set(ctx, "tenant-a", domain.LegacyCutoverStatusCutOver, "")
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
	if _, err := svc.Set(ctx, "tenant-a", domain.LegacyCutoverStatusShadow, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Set(ctx, "tenant-a", domain.LegacyCutoverStatusCutOver, ""); err != nil {
		t.Fatal(err)
	}
	_, ready, err := svc.CleanupReadiness(ctx, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Error("fresh cut_over decision must keep the rollback window open")
	}
	// Age the decision past the window.
	cutover.entries["tenant-a"].DecidedAt = time.Now().UTC().Add(-48 * time.Hour)
	items, ready, err := svc.CleanupReadiness(ctx, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Errorf("rollback window elapsed but not ready: %+v", items)
	}
}

// fakeCutoverAuditRepo is a fake cutover repository that implements the
// WithAudit surface: the service must take the atomic path and never invoke
// the post-commit Record fallback.
type fakeCutoverAuditRepo struct {
	fakeCutoverRepo
	setFact *domain.AuditFact
	setErr  error
}

func (f *fakeCutoverAuditRepo) SetCutoverWithAudit(ctx context.Context, cutover *repository.LegacyCutover, fact *domain.AuditFact) error {
	f.setFact = fact
	if f.setErr != nil {
		return f.setErr
	}
	return f.fakeCutoverRepo.SetCutover(ctx, cutover)
}

// TestCutoverAtomicAuditFailureFailsClosed (REQ-6): with the WithAudit
// surface, a failed ledger append fails the WHOLE decision with
// cutover.save_failed (nothing committed) and the post-commit Record fallback
// is provably never invoked. save_failed here is fail-closed — it must not be
// misread as "audit skipped, decision committed".
func TestCutoverAtomicAuditFailureFailsClosed(t *testing.T) {
	cutover := &fakeCutoverAuditRepo{fakeCutoverRepo: *newFakeCutoverRepo(), setErr: errors.New("ledger down")}
	ledger := &auditLedgerRepoSpy{}
	svc := NewLegacyCutoverService(cutover, &fakeCleanupRepo{}, NewAuditLedgerService(ledger)).(*legacyCutoverService)
	svc.actor = func(ctx context.Context) string { return "admin-1" }

	if _, err := svc.Set(t.Context(), "tenant-a", domain.LegacyCutoverStatusShadow, "dual read"); err == nil {
		t.Fatal("Set must fail when the in-tx audit append fails")
	} else if !apperrIs(err, "cutover.save_failed") {
		t.Fatalf("Set error = %v, want cutover.save_failed (atomic failure)", err)
	}
	if ledger.appended != nil {
		t.Fatal("atomic set must never invoke the post-commit Record fallback")
	}
}

// TestCutoverAtomicFactMatchesFallbackRecord (REQ-5 byte-parity): the fact
// appended in-transaction is built by the same pipeline as the post-commit
// Record fallback — same anchored EventID (status+previous), same
// tenant/actor/resource/partition, same redaction-safe payload.
func TestCutoverAtomicFactMatchesFallbackRecord(t *testing.T) {
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-alpha"})

	atomicRepo := &fakeCutoverAuditRepo{fakeCutoverRepo: *newFakeCutoverRepo()}
	atomicLedger := &auditLedgerRepoSpy{}
	atomicSvc := NewLegacyCutoverService(atomicRepo, &fakeCleanupRepo{}, NewAuditLedgerService(atomicLedger)).(*legacyCutoverService)
	atomicSvc.actor = func(ctx context.Context) string { return "admin-1" }
	if _, err := atomicSvc.Set(ctx, "tenant-alpha", domain.LegacyCutoverStatusShadow, "dual read"); err != nil {
		t.Fatalf("atomic set: %v", err)
	}
	if atomicRepo.setFact == nil || atomicLedger.appended != nil {
		t.Fatal("atomic path must pass the fact to the repo and never call Record")
	}

	fallbackRepo := newFakeCutoverRepo()
	fallbackLedger := &auditLedgerRepoSpy{}
	fallbackSvc := NewLegacyCutoverService(fallbackRepo, &fakeCleanupRepo{}, NewAuditLedgerService(fallbackLedger)).(*legacyCutoverService)
	fallbackSvc.actor = func(ctx context.Context) string { return "admin-1" }
	if _, err := fallbackSvc.Set(ctx, "tenant-alpha", domain.LegacyCutoverStatusShadow, "dual read"); err != nil {
		t.Fatalf("fallback set: %v", err)
	}
	if fallbackLedger.appended == nil {
		t.Fatal("fallback path must record the fact via Record")
	}

	atomicFact, fallbackFact := atomicRepo.setFact, fallbackLedger.appended
	if atomicFact.EventID != fallbackFact.EventID || atomicFact.TenantID != fallbackFact.TenantID ||
		atomicFact.ActorUID != fallbackFact.ActorUID || atomicFact.ResourceID != fallbackFact.ResourceID ||
		atomicFact.PartitionKey != fallbackFact.PartitionKey || atomicFact.Action != fallbackFact.Action {
		t.Fatalf("atomic fact %+v differs from fallback fact %+v", atomicFact, fallbackFact)
	}
	if !reflect.DeepEqual(atomicFact.Payload, fallbackFact.Payload) {
		t.Fatalf("atomic payload %v differs from fallback payload %v", atomicFact.Payload, fallbackFact.Payload)
	}
}

// TestCutoverAuditFailureFailsClosed (N3): a failed audit append fails the
// cutover decision with cutover.audit_failed instead of being swallowed.
func TestCutoverAuditFailureFailsClosed(t *testing.T) {
	cutover := newFakeCutoverRepo()
	failing := &auditLedgerRepoSpy{appendErr: errors.New("ledger down")}
	svc := NewLegacyCutoverService(cutover, &fakeCleanupRepo{}, NewAuditLedgerService(failing)).(*legacyCutoverService)
	svc.actor = func(ctx context.Context) string { return "admin-1" }

	if _, err := svc.Set(t.Context(), "tenant-a", domain.LegacyCutoverStatusShadow, "dual read"); err == nil {
		t.Fatal("Set must fail when the audit append fails")
	} else if !apperrIs(err, "cutover.audit_failed") {
		t.Fatalf("Set error = %v, want cutover.audit_failed", err)
	}
}

// TestCutoverAnchorTransitions (M-7): the anchor is (tenant, status, previous)
// — Legacy→Shadow (G11), same-status re-assertion (G12), and the cycle return
// rolled_back→legacy (G13) each record a DISTINCT fact; a racing duplicate
// execution that reads the same previous dedups exactly.
func TestCutoverAnchorTransitions(t *testing.T) {
	ctx := context.Background()
	cutover := newFakeCutoverRepo()
	audit := &auditLedgerRepoSpy{}
	svc := NewLegacyCutoverService(cutover, &fakeCleanupRepo{}, NewAuditLedgerService(audit)).(*legacyCutoverService)
	svc.actor = func(ctx context.Context) string { return "admin-1" }

	// legacy -> shadow (G11).
	if _, err := svc.Set(ctx, "tenant-alpha", domain.LegacyCutoverStatusShadow, "dual read"); err != nil {
		t.Fatalf("legacy->shadow: %v", err)
	}
	const g11 = "55090245-2231-5fc5-9671-5db846c5dd82"
	if audit.appended == nil || audit.appended.EventID.String() != g11 {
		t.Fatalf("legacy->shadow EventID = %v, want G11 %s", audit.appended, g11)
	}

	// Same-status re-assertion cut_over -> cut_over (note renewal): records a
	// distinct fact, never a swallowed transition. (G12's (shadow, shadow)
	// part list is a pure derivation fixture; shadow->shadow is rejected by
	// ValidCutoverTransition, so the reachable same-status flow is
	// cut_over->cut_over with golden 6854eb94-d40b-50fd-80b6-0f7dd4bca0a8.)
	if _, err := svc.Set(ctx, "tenant-alpha", domain.LegacyCutoverStatusCutOver, "moved"); err != nil {
		t.Fatalf("shadow->cut_over: %v", err)
	}
	if _, err := svc.Set(ctx, "tenant-alpha", domain.LegacyCutoverStatusCutOver, "renew"); err != nil {
		t.Fatalf("cut_over->cut_over: %v", err)
	}
	const g12Reachable = "6854eb94-d40b-50fd-80b6-0f7dd4bca0a8"
	if audit.appended == nil || audit.appended.EventID.String() != g12Reachable {
		t.Fatalf("cut_over->cut_over EventID = %v, want %s", audit.appended, g12Reachable)
	}
	if g12Reachable == g11 {
		t.Fatal("same-status re-assertion must differ from the transition fact")
	}

	// Cycle return: cut_over -> rolled_back -> legacy (G13).
	if _, err := svc.Set(ctx, "tenant-alpha", domain.LegacyCutoverStatusRolledBack, "rollback"); err != nil {
		t.Fatalf("cut_over->rolled_back: %v", err)
	}
	if _, err := svc.Set(ctx, "tenant-alpha", domain.LegacyCutoverStatusLegacy, "serve legacy"); err != nil {
		t.Fatalf("rolled_back->legacy: %v", err)
	}
	const g13 = "3c540abf-4417-56c2-b726-f26a89b0b157"
	if audit.appended == nil || audit.appended.EventID.String() != g13 {
		t.Fatalf("rolled_back->legacy EventID = %v, want G13 %s", audit.appended, g13)
	}
	if g13 == g11 || g13 == g12Reachable {
		t.Fatal("cycle return must record its own fact (G13 distinct)")
	}

	// Racing duplicate executions that both read the same previous dedup
	// exactly: two legacy->shadow sets against a dedup ledger append once. The
	// stale-read fake models both executions observing the pre-transition
	// state (previous=legacy) — the race window the anchor must dedup.
	racing := &staleCutoverRepo{fakeCutoverRepo: *newFakeCutoverRepo()}
	dupAudit := &dedupAuditRepoSpy{triples: map[string]bool{}}
	svcDup := NewLegacyCutoverService(racing, &fakeCleanupRepo{}, NewAuditLedgerService(dupAudit)).(*legacyCutoverService)
	svcDup.actor = func(ctx context.Context) string { return "admin-1" }
	for i := 0; i < 2; i++ {
		if _, err := svcDup.Set(ctx, "tenant-alpha", domain.LegacyCutoverStatusShadow, "dual read"); err != nil {
			t.Fatalf("racing set %d: %v", i, err)
		}
	}
	if dupAudit.appendedCount() != 1 {
		t.Fatalf("facts appended for racing duplicates = %d, want 1", dupAudit.appendedCount())
	}
}

// staleCutoverRepo models a racing duplicate execution: every read observes
// the pre-transition state (no decision -> legacy), so two racing executions
// both derive (status=shadow, previous=legacy) and must dedup at the ledger.
type staleCutoverRepo struct {
	fakeCutoverRepo
}

func (f *staleCutoverRepo) GetCutover(ctx context.Context, tenantID string) (*repository.LegacyCutover, error) {
	return nil, repository.ErrNotFound
}
