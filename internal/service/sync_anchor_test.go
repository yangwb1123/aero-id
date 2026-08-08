package service

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// syncFinishRepo records Finish / FinishWithAudit calls on an embedded
// SyncJobRepository surface (unimplemented methods panic if touched).
type syncFinishRepo struct {
	repository.SyncJobRepository
	withAudit  bool
	finished   int
	fact       *domain.AuditFact
	createFact *domain.AuditFact
}

func (r *syncFinishRepo) Finish(_ context.Context, _ domain.ID, _ string, _ domain.SyncJobStatus, _, _ string, _ map[string]any) error {
	r.finished++
	return nil
}

func (r *syncFinishRepo) FinishWithAudit(_ context.Context, _ domain.ID, _ string, _ domain.SyncJobStatus, _, _ string,
	_ map[string]any, fact *domain.AuditFact) error {
	r.finished++
	r.fact = fact
	return nil
}

func (r *syncFinishRepo) CreateWithAudit(_ context.Context, _ *domain.SyncJob, fact *domain.AuditFact) error {
	r.createFact = fact
	return nil
}

// syncFinishPlainRepo implements ONLY the plain Finish surface — the fallback
// shape that must record sync.job.finished standalone. It must NOT embed
// syncFinishRepo (embedding would promote the WithAudit methods and satisfy
// SyncJobAuditRepository).
type syncFinishPlainRepo struct {
	repository.SyncJobRepository
	finished int
}

func (r *syncFinishPlainRepo) Finish(_ context.Context, _ domain.ID, _ string, _ domain.SyncJobStatus, _, _ string, _ map[string]any) error {
	r.finished++
	return nil
}

// syncCreatePlainRepo implements ONLY the plain Create surface — a repo that
// lacks SyncJobAuditRepository. createWithAudit must fail closed on it.
type syncCreatePlainRepo struct {
	repository.SyncJobRepository
	creates int
}

func (r *syncCreatePlainRepo) Create(_ context.Context, _ *domain.SyncJob) error {
	r.creates++
	return nil
}

// TestSyncJobCreateWithAuditFailsClosedWhenRepoLacksWithAudit (FM-9): with
// audit wired and a repo lacking SyncJobAuditRepository, createWithAudit must
// fail closed with sync.audit_unavailable and never fall back to a plain
// Create that drops the sync.job.requested fact.
func TestSyncJobCreateWithAuditFailsClosedWhenRepoLacksWithAudit(t *testing.T) {
	repo := &syncCreatePlainRepo{}
	svc := &syncJobService{repo: repo, audit: &auditCapture{}}
	if err := svc.createWithAudit(t.Context(), goldenSyncJob()); err == nil {
		t.Fatal("createWithAudit must fail closed when the repo lacks SyncJobAuditRepository")
	}
	if repo.creates != 0 {
		t.Fatalf("plain Create calls = %d, want 0 (no silent fallback)", repo.creates)
	}
}

// TestSyncJobCreateWithAuditWiredPath: with audit wired and a repo
// implementing SyncJobAuditRepository, the sync.job.requested fact rides the
// create transaction.
func TestSyncJobCreateWithAuditWiredPath(t *testing.T) {
	repo := &syncFinishRepo{}
	capture := &auditCapture{}
	svc := &syncJobService{repo: repo, audit: capture}
	if err := svc.createWithAudit(t.Context(), goldenSyncJob()); err != nil {
		t.Fatalf("createWithAudit: %v", err)
	}
	if repo.createFact == nil {
		t.Fatal("CreateWithAudit did not receive the sync.job.requested fact")
	}
	if repo.createFact.Action != "sync.job.requested" {
		t.Fatalf("fact action = %q, want sync.job.requested", repo.createFact.Action)
	}
	if len(capture.inputs) != 0 {
		t.Fatalf("standalone Record calls = %d, want 0 (single write path)", len(capture.inputs))
	}
}

// TestSyncJobCreateWithAuditDisabledPlainPath: with s.audit == nil (unit-test
// construction only) createWithAudit uses the plain Create surface.
func TestSyncJobCreateWithAuditDisabledPlainPath(t *testing.T) {
	repo := &syncCreatePlainRepo{}
	svc := &syncJobService{repo: repo, audit: nil}
	if err := svc.createWithAudit(t.Context(), goldenSyncJob()); err != nil {
		t.Fatalf("createWithAudit: %v", err)
	}
	if repo.creates != 1 {
		t.Fatalf("plain Create calls = %d, want 1", repo.creates)
	}
}

var _ repository.SyncJobAuditRepository = (*syncFinishRepo)(nil)

// auditCapture records every standalone Record input.
type auditCapture struct {
	inputs []AuditFactInput
}

func (s *auditCapture) Record(_ context.Context, input AuditFactInput) error {
	s.inputs = append(s.inputs, input)
	return nil
}
func (s *auditCapture) Log(ctx context.Context, legacy *domain.AuditEvent) error { return nil }
func (s *auditCapture) List(ctx context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	return nil, "", nil
}
func (s *auditCapture) VerifyChain(ctx context.Context, source, partition string) error { return nil }

// goldenSyncJob rebuilds the §3 fixture job (G8: ["sync.job.finished",
// "tenant-alpha", jobID, "completed"]).
func goldenSyncJob() *domain.SyncJob {
	return &domain.SyncJob{ID: uuid.MustParse("0195b2c6-9f40-7c03-8000-000000000001"),
		AccountID: uuid.MustParse("33333333-3333-4333-8333-333333333333"), ActorUID: "actor-1", TenantID: "tenant-alpha",
		RequestID: "req-1", JobType: domain.SyncJobSync, Status: domain.SyncJobRunning}
}

// TestSyncJobFinishedAnchored (M-5, unit): syncJobFinishedAudit derives G8 from
// the §2.3 parts, and both finish paths — the in-tx FinishWithAudit route and
// the fallback standalone Record — mint the SAME event_id (single minting
// point, D-7).
func TestSyncJobFinishedAnchored(t *testing.T) {
	job := goldenSyncJob()
	fact := syncJobFinishedAudit(t.Context(), "local", job, domain.SyncJobCompleted, "", nil)
	const want = "5d165a06-322e-5b95-beb1-07ea5df9db47" // G8
	if fact.EventID.String() != want {
		t.Fatalf("sync.job.finished EventID = %s, want golden G8 %s", fact.EventID, want)
	}
	if retry := syncJobFinishedAudit(t.Context(), "local", job, domain.SyncJobCompleted, "", nil).EventID; retry != fact.EventID {
		t.Fatalf("identical retry re-derived %s, want %s", retry, fact.EventID)
	}
	if other := syncJobFinishedAudit(t.Context(), "local", job, domain.SyncJobFailed, "", nil).EventID; other == fact.EventID {
		t.Fatal("status must be part of the anchor (out-of-order invariant §4.4.3)")
	}
}

// TestSyncJobFinishedParity (M-5, unit): finishJob passes the SAME anchored
// fact to the in-tx path (repo implements SyncJobAuditRepository) and records
// the same event_id standalone when the repo lacks the WithAudit surface —
// the fallback can never diverge from the in-tx derivation.
func TestSyncJobFinishedParity(t *testing.T) {
	job := goldenSyncJob()
	job.LeaseOwner = "sync-worker"

	// Fail-closed path (B2-1 REQ-4): a repo without SyncJobAuditRepository
	// fails with sync.audit_unavailable — the standalone Record fallback tail
	// was deleted; there is no second delivery path to diverge from.
	fallbackAudit := &auditCapture{}
	fallback := &syncJobService{repo: &syncFinishPlainRepo{}, audit: fallbackAudit, region: "local"}
	if err := fallback.finishJob(t.Context(), job, domain.SyncJobCompleted, "", "", map[string]any{}); err == nil {
		t.Fatal("finishJob must fail closed when the repo lacks SyncJobAuditRepository")
	}
	if len(fallbackAudit.inputs) != 0 {
		t.Fatalf("standalone Records = %d, want 0 (tail deleted)", len(fallbackAudit.inputs))
	}

	// In-tx path: repo implements SyncJobAuditRepository -> fact to FinishWithAudit.
	const want = "5d165a06-322e-5b95-beb1-07ea5df9db47" // G8
	inTxRepo := &syncFinishRepo{withAudit: true}
	inTxAudit := &auditCapture{}
	inTx := &syncJobService{repo: inTxRepo, audit: inTxAudit, region: "local"}
	if err := inTx.finishJob(t.Context(), job, domain.SyncJobCompleted, "", "", map[string]any{}); err != nil {
		t.Fatalf("in-tx finishJob: %v", err)
	}
	if inTxRepo.finished != 1 || inTxRepo.fact == nil {
		t.Fatalf("FinishWithAudit calls = %d fact=%v, want 1 with a fact", inTxRepo.finished, inTxRepo.fact)
	}
	if got := inTxRepo.fact.EventID.String(); got != want {
		t.Fatalf("in-tx fact EventID = %s, want G8 %s", got, want)
	}
	if len(inTxAudit.inputs) != 0 {
		t.Fatalf("in-tx path must not fall back to Record, got %d", len(inTxAudit.inputs))
	}

	// No audit wired: plain Finish only.
	plainRepo := &syncFinishRepo{}
	plain := &syncJobService{repo: plainRepo, audit: nil, region: "local"}
	if err := plain.finishJob(t.Context(), job, domain.SyncJobCompleted, "", "", map[string]any{}); err != nil {
		t.Fatalf("plain finishJob: %v", err)
	}
	if plainRepo.finished != 1 {
		t.Fatalf("plain Finish calls = %d, want 1", plainRepo.finished)
	}
}

var _ = idgen.NewID // keep idgen import for fixture parity
