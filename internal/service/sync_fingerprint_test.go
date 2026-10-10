package service

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

type fingerprintSyncRepo struct {
	repository.SyncJobRepository
	job     *domain.SyncJob
	creates int
	race    bool
	matched bool
}

func (r *fingerprintSyncRepo) FindByIdempotency(_ context.Context, actor, tenant, key string) (*domain.SyncJob, error) {
	if r.job != nil && r.job.ActorUID == actor && r.job.TenantID == tenant && r.job.IdempotencyKey == key {
		return r.job, nil
	}
	return nil, repository.ErrNotFound
}

func (r *fingerprintSyncRepo) Create(_ context.Context, job *domain.SyncJob) error {
	r.creates++
	r.job = job
	if r.race {
		if !r.matched {
			r.job.RequestFingerprint = "v1:" + strings.Repeat("0", 64)
		}
		return gorm.ErrDuplicatedKey
	}
	return nil
}

func fingerprintSyncInput() SyncJobInput {
	return SyncJobInput{AccountID: domain.ID{1}, JobType: domain.SyncJobSync, IdempotencyKey: "sync-intent-1",
		Datasets: []domain.AccountDataset{"aero-vault.tenants", "aero-vault.usage"}}
}

func TestSyncJobRequestFingerprintCanonicalRetry(t *testing.T) {
	repo := &fingerprintSyncRepo{}
	svc := NewSyncJobServiceWithOptions(repo, nil, nil, nil, nil, nil, nil)
	ctx := fingerprintContext(t, "console")
	original, err := svc.Create(ctx, fingerprintSyncInput())
	if err != nil {
		t.Fatal(err)
	}
	input := fingerprintSyncInput()
	input.IdempotencyKey = " sync-intent-1 "
	input.Datasets[0], input.Datasets[1] = input.Datasets[1], input.Datasets[0]
	retry, err := svc.Create(WithRequestID(ctx, "new-request-id"), input)
	if err != nil || retry != original || repo.creates != 1 {
		t.Fatalf("retry = %v/%v, creates = %d", retry, err, repo.creates)
	}
}

func TestSyncJobRequestFingerprintRejectsChangedIntent(t *testing.T) {
	changes := []struct {
		name   string
		client string
		change func(*SyncJobInput)
	}{
		{"account", "console", func(in *SyncJobInput) { in.AccountID = domain.ID{2} }},
		{"implicit target", "console", func(in *SyncJobInput) { in.AccountID = domain.ID{} }},
		{"type", "console", func(in *SyncJobInput) { in.JobType = domain.SyncJobExport }},
		{"datasets", "console", func(in *SyncJobInput) { in.Datasets = []domain.AccountDataset{"aero-vault.tenants"} }},
		{"client", "other-client", func(*SyncJobInput) {}},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fingerprintSyncRepo{}
			svc := NewSyncJobServiceWithOptions(repo, nil, nil, nil, nil, nil, nil)
			if _, err := svc.Create(fingerprintContext(t, "console"), fingerprintSyncInput()); err != nil {
				t.Fatal(err)
			}
			input := fingerprintSyncInput()
			tc.change(&input)
			result, err := svc.Create(fingerprintContext(t, tc.client), input)
			assertFingerprintConflict(t, err, "sync")
			if result != nil || repo.creates != 1 {
				t.Fatal("changed intent was returned or persisted")
			}
		})
	}
}

func TestSyncJobLegacyReceiptFailsClosed(t *testing.T) {
	repo := &fingerprintSyncRepo{job: &domain.SyncJob{ActorUID: "actor-1", TenantID: "tenant-a",
		IdempotencyKey: "sync-intent-1"}}
	result, err := NewSyncJobServiceWithOptions(repo, nil, nil, nil, nil, nil, nil).Create(
		fingerprintContext(t, "console"), fingerprintSyncInput())
	assertFingerprintConflict(t, err, "sync")
	if result != nil || repo.creates != 0 {
		t.Fatal("historical receipt accepted or duplicate job persisted")
	}
}

func TestSyncJobUniqueInsertRaceChecksRequestFingerprint(t *testing.T) {
	for _, matched := range []bool{true, false} {
		repo := &fingerprintSyncRepo{race: true, matched: matched}
		result, err := NewSyncJobServiceWithOptions(repo, nil, nil, nil, nil, nil, nil).Create(
			fingerprintContext(t, "console"), fingerprintSyncInput())
		if matched {
			if err != nil || result != repo.job {
				t.Fatalf("matching race = %v/%v", result, err)
			}
		} else {
			assertFingerprintConflict(t, err, "sync")
			if result != nil {
				t.Fatal("conflicting winner returned")
			}
		}
	}
}
