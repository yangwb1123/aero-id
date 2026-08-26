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

type continuationSyncRepo struct {
	repository.SyncJobRepository
	parent  *domain.SyncJob
	created *domain.SyncJob
}

func (r *continuationSyncRepo) Get(_ context.Context, id domain.ID) (*domain.SyncJob, error) {
	if r.parent != nil && r.parent.ID == id {
		return r.parent, nil
	}
	return nil, repository.ErrNotFound
}

func (r *continuationSyncRepo) FindByIdempotency(_ context.Context, _, _, key string) (*domain.SyncJob, error) {
	if r.created != nil && r.created.IdempotencyKey == key {
		return r.created, nil
	}
	return nil, repository.ErrNotFound
}

func (r *continuationSyncRepo) Create(_ context.Context, job *domain.SyncJob) error {
	r.created = job
	return nil
}

func TestSyncAuditExportContinuationBindsParentAndIsIdempotent(t *testing.T) {
	parent := &domain.SyncJob{ID: idgen.NewID(), AccountID: idgen.NewID(), ActorUID: "admin",
		TenantID: "tenant-a", JobType: domain.SyncJobAuditExport, Status: domain.SyncJobCompleted,
		Result: map[string]any{auditExportContinuationCursorKey: "cursor-1"}}
	repo := &continuationSyncRepo{parent: parent}
	service := NewSyncJobServiceWithOptions(repo, nil, nil, nil, nil, nil, nil)
	ctx := WithIdentity(context.Background(), &domain.TokenIdentity{UID: "admin", Subject: "admin", TenantID: "tenant-a"})

	job, err := service.Create(ctx, SyncJobInput{AccountID: parent.AccountID, JobType: domain.SyncJobAuditExport,
		AuditExportContinuationID: parent.ID})
	if err != nil {
		t.Fatalf("continuation create: %v", err)
	}
	if job != repo.created || job.IdempotencyKey != auditExportContinuationKey(parent.ID) {
		t.Fatalf("created job = %+v, want deterministic continuation key", job)
	}
	if got := job.Request[auditExportContinuationRequestKey]; got != parent.ID.String() {
		t.Fatalf("continuation request reference = %v, want %s", got, parent.ID)
	}

	retry, err := service.Create(ctx, SyncJobInput{AccountID: parent.AccountID, JobType: domain.SyncJobAuditExport,
		IdempotencyKey: "caller-key-must-not-change-derived-key", AuditExportContinuationID: parent.ID})
	if err != nil || retry != job {
		t.Fatalf("continuation retry = %v/%v, want original job", retry, err)
	}
}

func TestSyncAuditExportContinuationRejectsCrossScopeOrExhaustedParent(t *testing.T) {
	parent := &domain.SyncJob{ID: idgen.NewID(), AccountID: idgen.NewID(), ActorUID: "admin",
		TenantID: "tenant-a", JobType: domain.SyncJobAuditExport, Status: domain.SyncJobCompleted,
		Result: map[string]any{auditExportContinuationCursorKey: "cursor-1"}}
	service := NewSyncJobServiceWithOptions(&continuationSyncRepo{parent: parent}, nil, nil, nil, nil, nil, nil)
	cases := []struct {
		name string
		ctx  context.Context
		id   domain.ID
		want string
	}{
		{name: "tenant mismatch", ctx: WithIdentity(context.Background(), &domain.TokenIdentity{UID: "admin", TenantID: "tenant-b"}), id: parent.ID, want: "sync.invalid_continuation"},
		{name: "actor mismatch", ctx: WithIdentity(context.Background(), &domain.TokenIdentity{UID: "other", TenantID: "tenant-a"}), id: parent.ID, want: "sync.invalid_continuation"},
		{name: "missing parent", ctx: WithIdentity(context.Background(), &domain.TokenIdentity{UID: "admin", TenantID: "tenant-a"}), id: idgen.NewID(), want: "sync.invalid_continuation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.Create(tc.ctx, SyncJobInput{AccountID: parent.AccountID, JobType: domain.SyncJobAuditExport,
				AuditExportContinuationID: tc.id})
			var appErr *apperr.AppError
			if !errors.As(err, &appErr) || appErr.Code != tc.want {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
		})
	}

	exhausted := *parent
	exhausted.Result = map[string]any{}
	exhaustedRepo := &continuationSyncRepo{parent: &exhausted}
	exhaustedService := NewSyncJobServiceWithOptions(exhaustedRepo, nil, nil, nil, nil, nil, nil)
	ctx := WithIdentity(context.Background(), &domain.TokenIdentity{UID: "admin", TenantID: "tenant-a"})
	_, err := exhaustedService.Create(ctx, SyncJobInput{AccountID: parent.AccountID, JobType: domain.SyncJobAuditExport,
		AuditExportContinuationID: parent.ID})
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code != "sync.continuation_exhausted" {
		t.Fatalf("exhausted error = %v, want sync.continuation_exhausted", err)
	}
	if exhaustedRepo.created != nil {
		t.Fatal("exhausted continuation created a child job")
	}
}
