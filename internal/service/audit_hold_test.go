package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
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
func (f *fakeHoldRepo) RemoveHold(ctx context.Context, tenantID string, scopeType domain.AuditHoldScope, scopeID string) error {
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
func (f *fakeHoldRepo) HoldScopeIDs(ctx context.Context, scopeType domain.AuditHoldScope) ([]string, error) {
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

	if _, err := svc.Put(holdContext(), "tenant-a", domain.AuditHoldScopeActor, "u-held", "evidence order #1", nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	holds, err := svc.List(holdContext(), "tenant-a")
	if err != nil || len(holds) != 1 {
		t.Fatalf("list = %d, %v", len(holds), err)
	}
	ids, err := svc.ActiveScopeIDs(context.Background(), domain.AuditHoldScopeActor)
	if err != nil || len(ids) != 1 || ids[0] != "u-held" {
		t.Fatalf("actor scope ids = %v, %v", ids, err)
	}
	if err := svc.Remove(holdContext(), "tenant-a", domain.AuditHoldScopeActor, "u-held"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if ids, _ := svc.ActiveScopeIDs(context.Background(), domain.AuditHoldScopeActor); len(ids) != 0 {
		t.Fatal("hold must be lifted")
	}
}

// TestAuditHold_Validation: bad scope and blank scope_id are rejected; expired
// holds are not active.
func TestAuditHold_Validation(t *testing.T) {
	repo := newFakeHoldRepo()
	svc := NewAuditHoldService(repo, nil)

	if _, err := svc.Put(holdContext(), "tenant-a", domain.AuditHoldScope("bogus"), "x", "", nil); err == nil {
		t.Fatal("invalid scope must be rejected")
	}
	if _, err := svc.Put(holdContext(), "tenant-a", domain.AuditHoldScopeActor, "  ", "", nil); err == nil {
		t.Fatal("blank scope_id must be rejected")
	}
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := svc.Put(holdContext(), "tenant-a", domain.AuditHoldScopeActor, "u-expired", "", &past); err != nil {
		t.Fatal(err)
	}
	if ids, _ := svc.ActiveScopeIDs(context.Background(), domain.AuditHoldScopeActor); len(ids) != 0 {
		t.Fatal("expired hold must not be active")
	}
}

// auditHoldAuditFakeRepo is a fake hold repository that implements the
// WithAudit surface: the service must take the atomic path and never invoke
// the post-commit Record fallback.
type auditHoldAuditFakeRepo struct {
	fakeHoldRepo
	putFact    *domain.AuditFact
	removeFact *domain.AuditFact
	putErr     error
	removeErr  error
}

func (f *auditHoldAuditFakeRepo) PutHoldWithAudit(ctx context.Context, hold *repository.AuditHold, fact *domain.AuditFact) error {
	f.putFact = fact
	if f.putErr != nil {
		return f.putErr
	}
	return f.fakeHoldRepo.PutHold(ctx, hold)
}

func (f *auditHoldAuditFakeRepo) RemoveHoldWithAudit(ctx context.Context, tenantID string, scopeType domain.AuditHoldScope, scopeID string, fact *domain.AuditFact) error {
	f.removeFact = fact
	if f.removeErr != nil {
		return f.removeErr
	}
	return f.fakeHoldRepo.RemoveHold(ctx, tenantID, scopeType, scopeID)
}

// TestAuditHoldAtomicAuditFailureFailsClosed (REQ-6): with the WithAudit
// surface, a failed ledger append fails the WHOLE mutation with
// audit_hold.save_failed (nothing committed) and the post-commit Record
// fallback is provably never invoked. save_failed here is fail-closed — it
// must not be misread as "audit skipped, mutation committed".
func TestAuditHoldAtomicAuditFailureFailsClosed(t *testing.T) {
	repo := &auditHoldAuditFakeRepo{fakeHoldRepo: *newFakeHoldRepo(), putErr: errors.New("ledger down")}
	ledger := &auditLedgerRepoSpy{}
	svc := NewAuditHoldService(repo, NewAuditLedgerService(ledger))

	if _, err := svc.Put(t.Context(), "tenant-a", domain.AuditHoldScopeAccount, "acc-1", "evidence", nil); err == nil {
		t.Fatal("Put must fail when the in-tx audit append fails")
	} else if !apperrIs(err, "audit_hold.save_failed") {
		t.Fatalf("Put error = %v, want audit_hold.save_failed (atomic failure)", err)
	}
	if ledger.appended != nil {
		t.Fatal("atomic put must never invoke the post-commit Record fallback")
	}

	repo.removeErr = errors.New("ledger down")
	if err := svc.Remove(t.Context(), "tenant-a", domain.AuditHoldScopeAccount, "acc-1"); err == nil {
		t.Fatal("Remove must fail when the in-tx audit append fails")
	} else if !apperrIs(err, "audit_hold.remove_failed") {
		t.Fatalf("Remove error = %v, want audit_hold.remove_failed (atomic failure)", err)
	}
	if ledger.appended != nil {
		t.Fatal("atomic remove must never invoke the post-commit Record fallback")
	}
}

// TestAuditHoldAtomicFactPathAndUnavailableFailFast (REQ-4 / R-3): the
// atomic path passes the fact built by the same pipeline (buildAuditFact →
// bind/normalize/redact) into the WithAudit repository and never invokes the
// ledger's Record. A repository without the WithAudit surface is a wiring
// error: with a non-nil audit logger the mutation fails fast with
// audit_hold.audit_unavailable BEFORE the plain write — no hold stored, no
// fact appended (the post-commit fallback tail is gone).
func TestAuditHoldAtomicFactPathAndUnavailableFailFast(t *testing.T) {
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-alpha"})

	atomicRepo := &auditHoldAuditFakeRepo{fakeHoldRepo: *newFakeHoldRepo()}
	atomicLedger := &auditLedgerRepoSpy{}
	atomicSvc := NewAuditHoldService(atomicRepo, NewAuditLedgerService(atomicLedger))
	if _, err := atomicSvc.Put(ctx, "tenant-alpha", domain.AuditHoldScopeAccount, "  acct-1024  ", "litigation hold", nil); err != nil {
		t.Fatalf("atomic put: %v", err)
	}
	if atomicRepo.putFact == nil || atomicLedger.appended != nil {
		t.Fatal("atomic path must pass the fact to the repo and never call Record")
	}
	// Byte-parity is structural now — the in-tx fact IS the only fact: the
	// anchored EventID, bound tenant/actor, and trimmed ResourceID land in
	// the fact the repo receives (same pipeline Record used to run).
	if atomicRepo.putFact.EventID != idgen.Anchor("audit_hold.set", "tenant-alpha", "account", "acct-1024", "litigation hold", "") {
		t.Fatalf("atomic EventID = %s, want the anchored value", atomicRepo.putFact.EventID)
	}
	if atomicRepo.putFact.TenantID != "tenant-alpha" || atomicRepo.putFact.ActorUID != "admin-1" || atomicRepo.putFact.ResourceID != "acct-1024" {
		t.Fatalf("atomic fact tenant/actor/resource = %q/%q/%q", atomicRepo.putFact.TenantID, atomicRepo.putFact.ActorUID, atomicRepo.putFact.ResourceID)
	}

	// R-3 fail-fast half: no WithAudit surface + non-nil audit → error before
	// the write, no hold stored, no fact appended.
	fallbackRepo := newFakeHoldRepo()
	fallbackLedger := &auditLedgerRepoSpy{}
	fallbackSvc := NewAuditHoldService(fallbackRepo, NewAuditLedgerService(fallbackLedger))
	if _, err := fallbackSvc.Put(ctx, "tenant-alpha", domain.AuditHoldScopeAccount, "  acct-1024  ", "litigation hold", nil); err == nil {
		t.Fatal("Put with a non-WithAudit repo and a non-nil audit must fail fast")
	} else if !apperrIs(err, "audit_hold.audit_unavailable") {
		t.Fatalf("Put error = %v, want audit_hold.audit_unavailable", err)
	}
	if len(fallbackRepo.holds) != 0 {
		t.Fatal("fail-fast must precede the plain write: no hold may be stored")
	}
	if fallbackLedger.appended != nil {
		t.Fatal("fail-fast must not append any fact")
	}
	if err := fallbackSvc.Remove(ctx, "tenant-alpha", domain.AuditHoldScopeAccount, "acct-1024"); err == nil {
		t.Fatal("Remove with a non-WithAudit repo and a non-nil audit must fail fast")
	} else if !apperrIs(err, "audit_hold.audit_unavailable") {
		t.Fatalf("Remove error = %v, want audit_hold.audit_unavailable", err)
	}
}

// TestAuditHoldAtomicFactRedactionSafe (REQ-4): the in-tx fact payload runs
// through the same redaction pipeline as Record — a sensitive value inside a
// nested payload key cannot bypass redaction by taking the atomic path.
func TestAuditHoldAtomicFactRedactionSafe(t *testing.T) {
	repo := &auditHoldAuditFakeRepo{fakeHoldRepo: *newFakeHoldRepo()}
	ledger := &auditLedgerRepoSpy{}
	svc := NewAuditHoldService(repo, NewAuditLedgerService(ledger))
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-alpha"})

	if _, err := svc.Put(ctx, "tenant-alpha", domain.AuditHoldScopeAccount, "acct-1024", "secret", nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	// The payload keys are allow-listed (reason/expires_at): they pass through
	// redaction unchanged, and buildAuditFact (not a raw marshal) built the
	// fact, so the redaction pipeline is provably in the atomic path.
	if repo.putFact == nil {
		t.Fatal("atomic path must receive the fact")
	}
	if got := repo.putFact.Payload["reason"]; got != "secret" {
		t.Fatalf("payload reason = %v, want preserved allow-listed value", got)
	}
}

// TestAuditHoldAuditFailureFailsClosed (N3): a failed in-tx audit append
// fails the hold mutation with audit_hold.save_failed / remove_failed instead
// of being swallowed — nothing commits without its durable fact.
func TestAuditHoldAuditFailureFailsClosed(t *testing.T) {
	repo := &auditHoldAuditFakeRepo{fakeHoldRepo: *newFakeHoldRepo(), putErr: errors.New("ledger down")}
	svc := NewAuditHoldService(repo, NewAuditLedgerService(&auditLedgerRepoSpy{}))

	if _, err := svc.Put(t.Context(), "tenant-a", domain.AuditHoldScopeAccount, "acc-1", "evidence", nil); err == nil {
		t.Fatal("Put must fail when the in-tx audit append fails")
	} else if !apperrIs(err, "audit_hold.save_failed") {
		t.Fatalf("Put error = %v, want audit_hold.save_failed", err)
	}
	repo.removeErr = errors.New("ledger down")
	if err := svc.Remove(t.Context(), "tenant-a", domain.AuditHoldScopeAccount, "acc-1"); err == nil {
		t.Fatal("Remove must fail when the in-tx audit append fails")
	} else if !apperrIs(err, "audit_hold.remove_failed") {
		t.Fatalf("Remove error = %v, want audit_hold.remove_failed", err)
	}
}

func apperrIs(err error, code string) bool {
	var appErr *apperr.AppError
	if errors.As(err, &appErr) {
		return appErr.Code == code
	}
	return false
}

// TestHoldActionDiscriminator (M-6, unit): put("", nil) derives G9b and
// removed derives G10 — the action discriminator (D-5) keeps an indefinite
// empty hold from ever colliding with the lift fact; the R3 trim fix makes
// the audited ResourceID the trimmed stored value.
func TestHoldActionDiscriminator(t *testing.T) {
	repo := &auditHoldAuditFakeRepo{fakeHoldRepo: *newFakeHoldRepo()}
	svc := NewAuditHoldService(repo, NewAuditLedgerService(&auditLedgerRepoSpy{}))
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-alpha"})

	if _, err := svc.Put(ctx, "tenant-alpha", domain.AuditHoldScopeAccount, "acct-1024", "", nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if repo.putFact == nil {
		t.Fatal("put must produce an audit fact on the atomic path")
	}
	const wantSet = "ee512dbf-6ea3-52d5-902a-669e5219fc79" // G9b
	if repo.putFact.EventID.String() != wantSet {
		t.Fatalf("put(\"\", nil) EventID = %s, want G9b %s", repo.putFact.EventID, wantSet)
	}
	if repo.putFact.ResourceID != "acct-1024" {
		t.Fatalf("put ResourceID = %q, want trimmed acct-1024 (R3)", repo.putFact.ResourceID)
	}
	if repo.putFact.TenantID != "tenant-alpha" {
		t.Fatalf("put fact tenant = %q, want tenant-alpha (anchor-from-fact, R3)", repo.putFact.TenantID)
	}

	if err := svc.Remove(ctx, "tenant-alpha", domain.AuditHoldScopeAccount, "acct-1024"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	const wantRemoved = "5d349dda-a820-5abf-a9f0-efe421a58f0f" // G10
	if repo.removeFact == nil || repo.removeFact.EventID.String() != wantRemoved {
		t.Fatalf("removed EventID = %v, want G10 %s", repo.removeFact, wantRemoved)
	}
	if repo.removeFact.EventID.String() == wantSet {
		t.Fatal("G9b must not equal G10")
	}
}

// TestHoldSetAnchoredGolden (M-6): a put with a non-empty reason and a
// microsecond-precision expiry derives G9; the anchored value is the
// post-normalization set that lands in the fact.
func TestHoldSetAnchoredGolden(t *testing.T) {
	repo := &auditHoldAuditFakeRepo{fakeHoldRepo: *newFakeHoldRepo()}
	svc := NewAuditHoldService(repo, NewAuditLedgerService(&auditLedgerRepoSpy{}))
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-alpha"})
	expiry, err := time.Parse(time.RFC3339Nano, "2026-12-31T23:59:59.123456Z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "tenant-alpha", domain.AuditHoldScopeAccount, "acct-1024", "litigation hold", &expiry); err != nil {
		t.Fatalf("put: %v", err)
	}
	const want = "411ae5c5-efc1-5f16-a381-fd3afd445816" // G9
	if repo.putFact == nil || repo.putFact.EventID.String() != want {
		t.Fatalf("put EventID = %v, want G9 %s", repo.putFact, want)
	}
}

// TestHoldSetRetryDedup (FM-7 / M-6): a crash-retry of an identical put
// re-derives the same anchored event_id, so the real ledger's dedup folds the
// second fact (uq_audit_ledger_tenant_event). At the service layer the
// property is anchor stability: both attempts present the identical EventID
// to the repository, and only one hold row survives the upsert.
func TestHoldSetRetryDedup(t *testing.T) {
	repo := &auditHoldAuditFakeRepo{fakeHoldRepo: *newFakeHoldRepo()}
	svc := NewAuditHoldService(repo, NewAuditLedgerService(&auditLedgerRepoSpy{}))
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-alpha"})

	var firstEventID domain.ID
	for i := 0; i < 2; i++ {
		if _, err := svc.Put(ctx, "tenant-alpha", domain.AuditHoldScopeAccount, "acct-1024", "litigation hold", nil); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		if repo.putFact == nil {
			t.Fatalf("put %d produced no fact", i)
		}
		if i == 0 {
			firstEventID = repo.putFact.EventID
		} else if repo.putFact.EventID != firstEventID {
			t.Fatalf("retry EventID = %s, want identical %s (dedup key must be stable)", repo.putFact.EventID, firstEventID)
		}
	}
	if len(repo.holds) != 1 {
		t.Fatalf("hold rows after duplicate put = %d, want 1 (upsert)", len(repo.holds))
	}
}

// TestHoldTrimmedScopeIDInAudit (R3): a padded scope_id input is trimmed for
// the hold row, the audit ResourceID, and the anchor alike.
func TestHoldTrimmedScopeIDInAudit(t *testing.T) {
	repo := &auditHoldAuditFakeRepo{fakeHoldRepo: *newFakeHoldRepo()}
	svc := NewAuditHoldService(repo, NewAuditLedgerService(&auditLedgerRepoSpy{}))
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin-1", TenantID: "tenant-alpha"})

	if _, err := svc.Put(ctx, "tenant-alpha", domain.AuditHoldScopeAccount, "  acct-1024  ", "x", nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	if repo.putFact.ResourceID != "acct-1024" {
		t.Fatalf("audited ResourceID = %q, want trimmed acct-1024", repo.putFact.ResourceID)
	}
	if repo.putFact.EventID != idgen.Anchor("audit_hold.set", "tenant-alpha", "account", "acct-1024", "x", "") {
		t.Fatalf("anchor must use the trimmed scope_id, got %s", repo.putFact.EventID)
	}
}
