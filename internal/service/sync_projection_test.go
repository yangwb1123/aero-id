package service

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// fakeSnapshotRepo records projection upserts and reconcile marking. The nil
// embedded interface satisfies the remaining SnapshotRepository methods; only
// the seams exercised by persistSyncSummary are implemented.
type fakeSnapshotRepo struct {
	repository.SnapshotRepository
	upserted             []*domain.AccountSnapshot
	markUnavailableCalls int
	markedUnavailable    []domain.AccountDataset
}

func (f *fakeSnapshotRepo) UpsertSnapshot(_ context.Context, snapshot *domain.AccountSnapshot) error {
	f.upserted = append(f.upserted, snapshot)
	return nil
}

func (f *fakeSnapshotRepo) MarkUnavailable(_ context.Context, _ domain.ID, _ string,
	datasets []domain.AccountDataset, _ int64) (int, error) {
	f.markUnavailableCalls++
	f.markedUnavailable = append(f.markedUnavailable, datasets...)
	return len(datasets), nil
}

// fakeAccountRepo records reference reconcile marking; the nil embedded
// interface satisfies the rest of repository.AccountRepository.
type fakeAccountRepo struct {
	repository.AccountRepository
	markMissingCalls int
}

func (f *fakeAccountRepo) MarkMissingReferences(_ context.Context, _ domain.ID, _ string,
	_ []*domain.AccountSource, _ []*domain.AccountMembership, _ int64) error {
	f.markMissingCalls++
	return nil
}

// fakeAccountService exposes the two account seams persistSyncSummary uses.
type fakeAccountService struct {
	AccountService
}

func (fakeAccountService) UpsertSource(context.Context, *domain.AccountSource) error { return nil }
func (fakeAccountService) UpsertMembership(context.Context, *domain.AccountMembership) error {
	return nil
}

func newSyncProjectionHarness() (*syncJobService, *fakeSnapshotRepo, *fakeAccountRepo) {
	snapshots := &fakeSnapshotRepo{}
	accountRepo := &fakeAccountRepo{}
	svc := &syncJobService{
		accounts:    fakeAccountService{},
		accountRepo: accountRepo,
		snapshots:   snapshots,
	}
	return svc, snapshots, accountRepo
}

var vaultDatasets = []domain.AccountDataset{
	domain.AccountDataset("aero-vault.tenants"),
	domain.AccountDataset("aero-vault.buckets"),
	domain.AccountDataset("aero-vault.usage"),
}

func TestSyncSummaryStatusSurfacesIncompleteResults(t *testing.T) {
	if status, partial := syncSummaryStatus(&connector.Summary{Complete: false}); status != "partial" || !partial {
		t.Fatalf("incomplete summary = %q/%v, want partial/true", status, partial)
	}
	if status, partial := syncSummaryStatus(&connector.Summary{Complete: true}); status != "synced" || partial {
		t.Fatalf("complete summary = %q/%v, want synced/false", status, partial)
	}
}

func TestDatasetsBySourceKeepsMissingConnectorDetectionScoped(t *testing.T) {
	got := datasetsBySource([]domain.AccountDataset{
		domain.AccountDataset("aero-vault.buckets"),
		domain.AccountDataset("aero-vault.usage"),
		domain.AccountDataset("aero-im.profile"),
	})
	if len(got[connector.SourceAeroVault]) != 2 || len(got[connector.SourceAeroIM]) != 1 {
		t.Fatalf("datasets by source = %#v, want vault=2 and im=1", got)
	}
	if _, ok := got[connector.SourceSnaplink]; ok {
		t.Fatal("unrequested source should not be treated as a missing connector")
	}
}

// TestPersistSyncSummary_SkipsSuppressReconcileMarking: a summary downgraded
// to incomplete because datasets were skipped must not mark previously
// projected snapshots unavailable or references missing, even though the job
// is a reconcile job. This pins the fail-closed gate: a source that silently
// dropped data must never be acknowledged as reconciled.
func TestPersistSyncSummary_SkipsSuppressReconcileMarking(t *testing.T) {
	svc, snapshots, accountRepo := newSyncProjectionHarness()
	accountID := idgen.NewID()
	job := &domain.SyncJob{AccountID: accountID, JobType: domain.SyncJobReconcile}
	account := &domain.Account{ID: accountID}
	summary := &connector.Summary{
		Source:          connector.SourceAeroVault,
		Complete:        false, // downgraded by the connector because of the skip below
		SkippedDatasets: []string{"aero-vault.unknown"},
		Version:         5,
	}

	if err := svc.persistSyncSummary(context.Background(), job, account, connector.SourceAeroVault, vaultDatasets, summary); err != nil {
		t.Fatalf("persistSyncSummary: %v", err)
	}
	if snapshots.markUnavailableCalls != 0 {
		t.Errorf("MarkUnavailable calls = %d, want 0 (reconcile marking must be suppressed)", snapshots.markUnavailableCalls)
	}
	if accountRepo.markMissingCalls != 0 {
		t.Errorf("MarkMissingReferences calls = %d, want 0 (reconcile marking must be suppressed)", accountRepo.markMissingCalls)
	}
	if len(snapshots.upserted) != 0 {
		t.Errorf("upserted snapshots = %d, want 0", len(snapshots.upserted))
	}
}

// TestPersistSyncSummary_GateIsOnCompleteNotOnSkips: an incomplete summary
// caused by the source's own flag (no skips) must equally suppress reconcile
// marking, pinning that the gate is summary.Complete, not SkippedDatasets.
func TestPersistSyncSummary_GateIsOnCompleteNotOnSkips(t *testing.T) {
	svc, snapshots, accountRepo := newSyncProjectionHarness()
	accountID := idgen.NewID()
	job := &domain.SyncJob{AccountID: accountID, JobType: domain.SyncJobReconcile}
	account := &domain.Account{ID: accountID}
	summary := &connector.Summary{Source: connector.SourceAeroVault, Complete: false, Version: 5}

	if err := svc.persistSyncSummary(context.Background(), job, account, connector.SourceAeroVault, vaultDatasets, summary); err != nil {
		t.Fatalf("persistSyncSummary: %v", err)
	}
	if snapshots.markUnavailableCalls != 0 || accountRepo.markMissingCalls != 0 {
		t.Errorf("reconcile marking ran on an incomplete summary: MarkUnavailable=%d MarkMissingReferences=%d",
			snapshots.markUnavailableCalls, accountRepo.markMissingCalls)
	}
}

// TestPersistSyncSummary_CompleteSummaryRunsReconcileMarking: positive
// control. A genuinely complete summary must run both reconcile seams; the
// requested-but-absent datasets (buckets, usage) exercise MarkUnavailable.
// This proves the fakes can observe reconcile marking and only the Complete
// gate suppressed it in the negative cases above.
func TestPersistSyncSummary_CompleteSummaryRunsReconcileMarking(t *testing.T) {
	svc, snapshots, accountRepo := newSyncProjectionHarness()
	accountID := idgen.NewID()
	job := &domain.SyncJob{AccountID: accountID, JobType: domain.SyncJobReconcile}
	account := &domain.Account{ID: accountID}
	summary := &connector.Summary{
		Source:   connector.SourceAeroVault,
		Complete: true,
		Version:  5,
		Snapshots: []*domain.AccountSnapshot{
			{Dataset: domain.AccountDataset("aero-vault.tenants")},
		},
	}

	if err := svc.persistSyncSummary(context.Background(), job, account, connector.SourceAeroVault, vaultDatasets, summary); err != nil {
		t.Fatalf("persistSyncSummary: %v", err)
	}
	if snapshots.markUnavailableCalls != 1 {
		t.Errorf("MarkUnavailable calls = %d, want 1", snapshots.markUnavailableCalls)
	}
	if len(snapshots.markedUnavailable) != 2 {
		t.Errorf("marked unavailable datasets = %v, want the two requested-but-absent vault datasets",
			snapshots.markedUnavailable)
	}
	if accountRepo.markMissingCalls != 1 {
		t.Errorf("MarkMissingReferences calls = %d, want 1", accountRepo.markMissingCalls)
	}
	if len(snapshots.upserted) != 1 {
		t.Errorf("upserted snapshots = %d, want 1", len(snapshots.upserted))
	}
}
