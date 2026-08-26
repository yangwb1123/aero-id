package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

type durableScopeJobRepo struct {
	repository.SyncJobRepository
	job          *domain.SyncJob
	finishCalls  int
	finishErr    error
	finishCtxErr error
}

func (r *durableScopeJobRepo) Get(context.Context, domain.ID) (*domain.SyncJob, error) {
	return r.job, nil
}

func (r *durableScopeJobRepo) Claim(_ context.Context, _ domain.ID, worker string, lease time.Duration) (*domain.SyncJob, error) {
	claimed := *r.job
	claimed.Status = domain.SyncJobRunning
	claimed.LeaseOwner = worker
	until := time.Now().UTC().Add(lease)
	claimed.LeaseUntil = &until
	return &claimed, nil
}

func (r *durableScopeJobRepo) ClaimUnknown(_ context.Context, _ domain.ID, worker string, lease time.Duration) (*domain.SyncJob, error) {
	return r.Claim(context.Background(), domain.ID{}, worker, lease)
}

func (r *durableScopeJobRepo) Finish(ctx context.Context, _ domain.ID, _ string, status domain.SyncJobStatus,
	_, _ string, _ map[string]any) error {
	r.finishCalls++
	r.finishCtxErr = ctx.Err()
	r.job.Status = status
	return r.finishErr
}

func TestSyncRunMalformedPendingScopeFailsBeforeAccountLookupAndSurvivesCancellation(t *testing.T) {
	repo := &durableScopeJobRepo{job: &domain.SyncJob{ID: domain.ID{1}, AccountID: domain.ID{2},
		ActorUID: "actor-1", TenantID: "tenant-a", JobType: domain.SyncJobSync, Status: domain.SyncJobPending,
		Request: map[string]any{"datasets": []any{}}, Result: map[string]any{}}}
	accounts := &countingSyncAccountRepo{}
	svc := NewSyncJobServiceWithOptions(repo, nil, accounts, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(WithIdentity(context.Background(), &domain.TokenIdentity{UID: "actor-1", TenantID: "tenant-a"}))
	cancel()

	_, err := svc.Run(ctx, repo.job.ID, "worker-1")
	if err == nil {
		t.Fatal("Run succeeded for malformed persisted scope")
	}
	if accounts.getCalls != 0 {
		t.Fatalf("account lookups = %d, want 0", accounts.getCalls)
	}
	if repo.finishCalls != 1 || repo.finishCtxErr != nil {
		t.Fatalf("finish calls/context = %d/%v, want 1/nil", repo.finishCalls, repo.finishCtxErr)
	}
	if repo.job.Status != domain.SyncJobFailed {
		t.Fatalf("durable status = %s, want failed", repo.job.Status)
	}
}

func TestSyncReconcileMalformedEraseScopeDoesNotLoadAccount(t *testing.T) {
	repo := &durableScopeJobRepo{job: &domain.SyncJob{ID: domain.ID{3}, AccountID: domain.ID{4},
		ActorUID: "actor-1", TenantID: "tenant-a", JobType: domain.SyncJobErase, Status: domain.SyncJobUnknown,
		Request: map[string]any{"datasets": []any{"aero-vault.tenants", "aero-vault.tenants"}}, Result: map[string]any{}}}
	accounts := &countingSyncAccountRepo{}
	svc := NewSyncJobServiceWithOptions(repo, nil, accounts, nil, nil, nil, nil)
	ctx := WithIdentity(context.Background(), &domain.TokenIdentity{UID: "actor-1", TenantID: "tenant-a"})

	_, err := svc.Reconcile(ctx, repo.job.ID, "reconciler-1")
	if err == nil {
		t.Fatal("Reconcile succeeded for malformed persisted scope")
	}
	if accounts.getCalls != 0 {
		t.Fatalf("account lookups = %d, want 0", accounts.getCalls)
	}
	if repo.finishCalls != 1 || repo.finishCtxErr != nil {
		t.Fatalf("finish calls/context = %d/%v, want 1/nil", repo.finishCalls, repo.finishCtxErr)
	}
	if repo.job.Status != domain.SyncJobFailed {
		t.Fatalf("durable status = %s, want failed", repo.job.Status)
	}
}

type countingSyncAccountRepo struct {
	repository.AccountRepository
	getCalls int
}

func (r *countingSyncAccountRepo) GetByID(context.Context, string, domain.ID) (*domain.Account, error) {
	r.getCalls++
	return nil, errors.New("account lookup must not run")
}
