package service

import (
	"context"
	"reflect"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

func TestValidatedDatasetsBySourcePreservesOrderAndRejectsInvalidPartitions(t *testing.T) {
	input := []domain.AccountDataset{
		"aero-vault.usage", "aero-im.notifications", "aero-vault.tenants", "aero-im.profile",
	}
	got, err := validatedDatasetsBySource(input)
	if err != nil {
		t.Fatal(err)
	}
	if want := []domain.AccountDataset{"aero-vault.usage", "aero-vault.tenants"}; !reflect.DeepEqual(got[connector.SourceAeroVault], want) {
		t.Fatalf("Vault scope = %v, want %v", got[connector.SourceAeroVault], want)
	}
	if want := []domain.AccountDataset{"aero-im.notifications", "aero-im.profile"}; !reflect.DeepEqual(got[connector.SourceAeroIM], want) {
		t.Fatalf("IM scope = %v, want %v", got[connector.SourceAeroIM], want)
	}

	for _, invalid := range [][]domain.AccountDataset{
		{"aero-vault.tenants", "aero-vault.tenants"},
		{"unknown.dataset"},
	} {
		if _, err := validatedDatasetsBySource(invalid); err == nil {
			t.Fatalf("invalid input %v was accepted", invalid)
		}
	}
	if got, err := validatedDatasetsBySource(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty partition = %#v/%v, want empty map", got, err)
	}
}

type syncScopeCreateRepo struct {
	repository.SyncJobRepository
	findCalls int
	created   *domain.SyncJob
}

func (r *syncScopeCreateRepo) FindByIdempotency(context.Context, string, string, string) (*domain.SyncJob, error) {
	r.findCalls++
	return nil, repository.ErrNotFound
}

func (r *syncScopeCreateRepo) Create(_ context.Context, job *domain.SyncJob) error {
	r.created = job
	return nil
}

func TestEmptyEraseCreateExpandsEverySourceOwnedScope(t *testing.T) {
	repo := &syncScopeCreateRepo{}
	service := NewSyncJobServiceWithOptions(repo, nil, nil, nil, nil, nil, nil)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "actor-1", TenantID: "tenant-a"})
	job, err := service.Create(ctx, SyncJobInput{AccountID: domain.ID{1}, JobType: domain.SyncJobErase, IdempotencyKey: "erase-all"})
	if err != nil {
		t.Fatal(err)
	}
	if repo.created != job {
		t.Fatal("created job was not captured")
	}
	datasets, ok := job.Request["datasets"].([]domain.AccountDataset)
	if !ok || len(datasets) == 0 {
		t.Fatalf("persisted datasets = %#v, want full registry", job.Request["datasets"])
	}
	bySource, err := validatedDatasetsBySource(datasets)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []connector.Source{connector.SourceSnaplink, connector.SourceAeroIM, connector.SourceAeroVault} {
		if len(bySource[source]) == 0 {
			t.Fatalf("source %s received an empty full-erase scope", source)
		}
		for _, dataset := range bySource[source] {
			descriptor, _ := domain.DatasetDescriptorOf(dataset)
			if descriptor.Source != string(source) {
				t.Fatalf("dataset %s was assigned to %s", dataset, source)
			}
		}
	}
}

func TestEraseCreateRejectsDuplicateScopeBeforePersistence(t *testing.T) {
	repo := &syncScopeCreateRepo{}
	service := NewSyncJobServiceWithOptions(repo, nil, nil, nil, nil, nil, nil)
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "actor-1", TenantID: "tenant-a"})
	_, err := service.Create(ctx, SyncJobInput{AccountID: domain.ID{1}, JobType: domain.SyncJobErase,
		IdempotencyKey: "erase-duplicate", Datasets: []domain.AccountDataset{"aero-vault.tenants", "aero-vault.tenants"}})
	if err == nil {
		t.Fatal("duplicate erase scope was accepted")
	}
	if repo.findCalls != 0 {
		t.Fatalf("idempotency lookups = %d, want 0 for invalid scope", repo.findCalls)
	}
	if repo.created != nil {
		t.Fatal("duplicate scope was persisted")
	}
}
