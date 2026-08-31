package service

import (
	"context"
	"errors"
	"reflect"
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

type recoveryProjectionRepo struct {
	repository.SyncJobRepository
	saved   []repository.SummaryReplayInput
	saveErr error
}

func (f *recoveryProjectionRepo) SaveSummaryReplay(_ context.Context, input repository.SummaryReplayInput) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, input)
	return nil
}
func (f *recoveryProjectionRepo) ListSummaryReplays(context.Context, domain.ID) ([]repository.SummaryReplay, error) {
	return nil, nil
}
func (f *recoveryProjectionRepo) ResolveSummaryReplay(context.Context, domain.ID, string, int, string) error {
	return nil
}
func (f *recoveryProjectionRepo) EnsureEraseOutcomes(context.Context, domain.ID, string, []repository.EraseOutcomeInput) error {
	return nil
}
func (f *recoveryProjectionRepo) ListEraseOutcomes(context.Context, domain.ID) ([]repository.EraseOutcome, error) {
	return nil, nil
}
func (f *recoveryProjectionRepo) CheckpointEraseOutcome(context.Context, repository.EraseOutcomeCheckpoint) error {
	return nil
}
func (f *recoveryProjectionRepo) CheckpointArtifact(context.Context, repository.ArtifactCheckpoint) error {
	return nil
}

type fakeSummaryApplication struct {
	requests []domain.SummaryApplicationRequest
}

func (f *fakeSummaryApplication) ApplySummary(_ context.Context, request domain.SummaryApplicationRequest) (domain.SummaryApplicationResult, error) {
	f.requests = append(f.requests, request)
	return domain.SummaryApplicationResult{Outcome: domain.SummaryApplied, Applied: true}, nil
}

func newSyncProjectionHarness() (*syncJobService, *fakeSnapshotRepo, *fakeAccountRepo, *fakeSummaryApplication) {
	snapshots := &fakeSnapshotRepo{}
	accountRepo := &fakeAccountRepo{}
	application := &fakeSummaryApplication{}
	svc := &syncJobService{
		accounts: fakeAccountService{}, accountRepo: accountRepo, snapshots: snapshots, application: application,
	}
	return svc, snapshots, accountRepo, application
}

var vaultDatasets = []domain.AccountDataset{
	domain.AccountDataset("aero-vault.tenants"),
	domain.AccountDataset("aero-vault.buckets"),
	domain.AccountDataset("aero-vault.usage"),
}

func TestPersistSyncSummaryPersistsExactRequestBeforeApplying(t *testing.T) {
	recovery := &recoveryProjectionRepo{}
	application := &fakeSummaryApplication{}
	svc := &syncJobService{repo: recovery, application: application}
	job := &domain.SyncJob{ID: idgen.NewID(), AccountID: idgen.NewID(), Attempt: 4, LeaseOwner: "worker"}
	summary := &connector.Summary{Source: connector.SourceAeroIM, SourceRegion: "local", Version: 9, Complete: true,
		Coverage: domain.SummaryCoverage{Datasets: []domain.AccountDataset{"aero-im.profile"}, Explicit: true}}
	if err := svc.persistSyncSummary(t.Context(), job, nil, connector.SourceAeroIM, []domain.AccountDataset{"aero-im.profile"}, summary); err != nil {
		t.Fatal(err)
	}
	if len(recovery.saved) != 1 || len(application.requests) != 1 {
		t.Fatalf("saved/applied = %d/%d, want one each", len(recovery.saved), len(application.requests))
	}
	if !reflect.DeepEqual(recovery.saved[0].Request, application.requests[0]) {
		t.Fatal("persisted summary request differs from applied request")
	}
}

func TestPersistSyncSummaryDoesNotCallApplicationWhenReplayPersistenceFails(t *testing.T) {
	recovery := &recoveryProjectionRepo{saveErr: errors.New("database unavailable")}
	application := &fakeSummaryApplication{}
	svc := &syncJobService{repo: recovery, application: application}
	job := &domain.SyncJob{ID: idgen.NewID(), AccountID: idgen.NewID(), Attempt: 1, LeaseOwner: "worker"}
	summary := &connector.Summary{Source: connector.SourceAeroIM, Version: 1, Complete: true}
	if err := svc.persistSyncSummary(t.Context(), job, nil, connector.SourceAeroIM, nil, summary); err == nil {
		t.Fatal("persistence failure was ignored")
	}
	if len(application.requests) != 0 {
		t.Fatal("application was called after replay persistence failed")
	}
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
	svc, snapshots, accountRepo, application := newSyncProjectionHarness()
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
	if snapshots.markUnavailableCalls != 0 || accountRepo.markMissingCalls != 0 {
		t.Errorf("legacy reconciliation calls = snapshot:%d references:%d, want 0", snapshots.markUnavailableCalls, accountRepo.markMissingCalls)
	}
	if len(snapshots.upserted) != 0 {
		t.Errorf("direct snapshot upserts = %d, want 0", len(snapshots.upserted))
	}
	if len(application.requests) != 1 || !application.requests[0].Reconcile ||
		len(application.requests[0].SkippedDatasets) != 1 {
		t.Fatalf("summary application requests = %+v, want one reconciled incomplete request", application)
	}
}

// TestPersistSyncSummary_GateIsOnCompleteNotOnSkips: an incomplete summary
// caused by the source's own flag (no skips) must equally suppress reconcile
// marking, pinning that the gate is summary.Complete, not SkippedDatasets.
func TestPersistSyncSummary_GateIsOnCompleteNotOnSkips(t *testing.T) {
	svc, snapshots, accountRepo, application := newSyncProjectionHarness()
	accountID := idgen.NewID()
	job := &domain.SyncJob{AccountID: accountID, JobType: domain.SyncJobReconcile}
	account := &domain.Account{ID: accountID}
	summary := &connector.Summary{Source: connector.SourceAeroVault, Complete: false, Version: 5}

	if err := svc.persistSyncSummary(context.Background(), job, account, connector.SourceAeroVault, vaultDatasets, summary); err != nil {
		t.Fatalf("persistSyncSummary: %v", err)
	}
	if snapshots.markUnavailableCalls != 0 || accountRepo.markMissingCalls != 0 {
		t.Errorf("legacy reconciliation calls = snapshot:%d references:%d, want 0", snapshots.markUnavailableCalls, accountRepo.markMissingCalls)
	}
	if len(application.requests) != 1 || !application.requests[0].Reconcile {
		t.Fatalf("summary application requests = %+v, want one reconciled incomplete request", application)
	}
}

// TestPersistSyncSummary_CompleteSummaryRunsReconcileMarking: positive
// control. A genuinely complete summary must run both reconcile seams; the
// requested-but-absent datasets (buckets, usage) exercise MarkUnavailable.
// This proves the fakes can observe reconcile marking and only the Complete
// gate suppressed it in the negative cases above.
func TestPersistSyncSummary_CompleteSummaryRunsReconcileMarking(t *testing.T) {
	svc, snapshots, accountRepo, application := newSyncProjectionHarness()
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
	if snapshots.markUnavailableCalls != 0 || accountRepo.markMissingCalls != 0 {
		t.Errorf("legacy reconciliation calls = snapshot:%d references:%d, want 0", snapshots.markUnavailableCalls, accountRepo.markMissingCalls)
	}
	if len(snapshots.upserted) != 0 {
		t.Errorf("direct snapshot upserts = %d, want 0", len(snapshots.upserted))
	}
	if len(application.requests) != 1 || !application.requests[0].Reconcile ||
		len(application.requests[0].Snapshots) != 1 {
		t.Fatalf("summary application requests = %+v, want one reconciled request", application)
	}
}
