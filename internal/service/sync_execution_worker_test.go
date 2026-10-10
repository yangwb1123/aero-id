package service

import (
	"context"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

type batchSyncExecutionRepo struct {
	*syncGateRepo
	second   *syncGateRepo
	queue    []*domain.SyncJob
	finished chan struct{}
}

func (r *batchSyncExecutionRepo) target(id domain.ID) *syncGateRepo {
	if id == r.second.job.ID {
		return r.second
	}
	return r.syncGateRepo
}
func (r *batchSyncExecutionRepo) ClaimNext(context.Context, string, time.Duration) (*domain.SyncJob, error) {
	if len(r.queue) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	job := r.queue[0]
	r.queue = r.queue[1:]
	return job, nil
}
func (*batchSyncExecutionRepo) ClaimUnknownNext(context.Context, string, time.Duration) (*domain.SyncJob, error) {
	return nil, gorm.ErrRecordNotFound
}
func (r *batchSyncExecutionRepo) CheckExecutionLease(ctx context.Context, job *domain.SyncJob, region string, erased bool) error {
	return r.target(job.ID).CheckExecutionLease(ctx, job, region, erased)
}
func (r *batchSyncExecutionRepo) DeferExecutionWithAudit(ctx context.Context, input repository.SyncJobExecutionDeferral, fact *domain.AuditFact) error {
	return r.target(input.Job.ID).DeferExecutionWithAudit(ctx, input, fact)
}
func (r *batchSyncExecutionRepo) SaveSummaryReplay(ctx context.Context, input repository.SummaryReplayInput) error {
	return r.target(input.JobID).SaveSummaryReplay(ctx, input)
}
func (r *batchSyncExecutionRepo) ResolveSummaryReplay(ctx context.Context, id domain.ID, source string, attempt int, digest string) error {
	return r.target(id).ResolveSummaryReplay(ctx, id, source, attempt, digest)
}
func (r *batchSyncExecutionRepo) Finish(ctx context.Context, id domain.ID, owner string, status domain.SyncJobStatus, code, message string, result map[string]any) error {
	err := r.target(id).Finish(ctx, id, owner, status, code, message, result)
	if id == r.second.job.ID {
		close(r.finished)
	}
	return err
}
func (r *batchSyncExecutionRepo) Get(ctx context.Context, id domain.ID) (*domain.SyncJob, error) {
	return r.target(id).Get(ctx, id)
}
func TestSyncWorkerBlockedClaimDoesNotStarveFollowingEligibleWork(t *testing.T) {
	svc, first, firstRepo, source, _, _ := syncExecutionFixture(t, domain.SyncJobSync)
	second := *first
	second.ID = idgen.NewID()
	second.ActorUID = "eligible"
	second.IdempotencyKey = "another-intent"
	second.Result = nil
	secondRepo := &syncGateRepo{minimalSyncJobRepo: &minimalSyncJobRepo{}, job: &second}
	secondRepo.SeedJob(&second)
	claimedSecond := armSyncGateJob(&second, "execute")
	repo := &batchSyncExecutionRepo{syncGateRepo: firstRepo, second: secondRepo, queue: []*domain.SyncJob{first, claimedSecond}, finished: make(chan struct{})}
	svc.repo = repo
	svc.executionPermission = capabilityPermissions(func(_ context.Context, actor, _ string) (bool, error) { return actor == "eligible", nil })
	stop := svc.Start(t.Context(), time.Millisecond)
	defer stop()
	select {
	case <-repo.finished:
	case <-time.After(time.Second):
		t.Fatal("blocked claim starved eligible work")
	}
	stop()
	if source.summaries.Load() != 1 || firstRepo.job.Status != domain.SyncJobPending || second.Status != domain.SyncJobCompleted || len(firstRepo.facts) != 1 {
		t.Fatal("batch did not preserve both intents and independent outcomes")
	}
}

func TestSyncRecoveryWithoutDurablePortNeverReissuesSource(t *testing.T) {
	svc, job, repo, source, _, _ := syncExecutionFixture(t, domain.SyncJobSync)
	job.LeaseKind = "reconcile"
	repo.job.LeaseKind = "reconcile"
	svc.repo = repo.minimalSyncJobRepo
	_, err := svc.reconcileSync(t.Context(), job)
	if requireAppError(t, err).Code != "sync.recovery_unavailable" || source.summaries.Load() != 0 || repo.job.Status != domain.SyncJobUnknown {
		t.Fatal("missing recovery port was permission to start another source read", err)
	}
}
