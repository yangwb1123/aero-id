//go:build integration

package repository_test

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"gorm.io/gorm"
)

const (
	summarySource = "aero-vault"
	summaryRegion = "west"
	datasetOne    = domain.AccountDataset("aero-vault.buckets")
	datasetTwo    = domain.AccountDataset("aero-vault.usage")
)

type summaryProjectionState struct {
	snapshotOne *domain.AccountSnapshot
	snapshotTwo *domain.AccountSnapshot
	sources     map[string]*domain.AccountSource
	memberships map[string]*domain.AccountMembership
}

func TestSummaryApplicationFencesUpsertsAndReconcilesOmissions(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	app, accountID := summaryApplicationFixture(t, db)

	if !membershipErrorCodeColumnExists(t, db) {
		t.Fatal("account_memberships.error_code column is missing after migrations")
	}
	initial := summaryRequest(accountID, 10, true, false,
		[]domain.AccountDataset{datasetOne, datasetTwo},
		[]*domain.AccountSnapshot{summarySnapshot(accountID, datasetOne, 10, "fresh", "old.snapshot"), summarySnapshot(accountID, datasetTwo, 10, "fresh", "old.snapshot-two")},
		[]*domain.AccountSource{summarySourceRow(accountID, "source-one", 10, "active", "old.source"), summarySourceRow(accountID, "source-two", 10, "active", "old.source-two")},
		[]*domain.AccountMembership{summaryMembership(accountID, "scope-one", "member-one", 10, "active", "old.member"), summaryMembership(accountID, "scope-two", "member-two", 10, "active", "old.member-two")})
	if _, err := app.ApplySummary(t.Context(), initial); err != nil {
		t.Fatalf("initial summary: %v", err)
	}
	before := loadSummaryState(t, db, accountID)

	lower := summaryRequest(accountID, 9, false, false,
		[]domain.AccountDataset{datasetOne},
		[]*domain.AccountSnapshot{summarySnapshot(accountID, datasetOne, 9, "unavailable", "lower.snapshot")},
		[]*domain.AccountSource{summarySourceRow(accountID, "source-one", 9, "unavailable", "lower.source")},
		[]*domain.AccountMembership{summaryMembership(accountID, "scope-one", "member-one", 9, "unavailable", "lower.member")})
	if _, err := app.ApplySummary(t.Context(), lower); err != nil {
		t.Fatalf("lower summary: %v", err)
	}
	if after := loadSummaryState(t, db, accountID); !reflect.DeepEqual(before, after) {
		t.Fatalf("lower version changed projection state:\nbefore=%+v\nafter=%+v", before, after)
	}

	refresh := summaryRequest(accountID, 11, true, false,
		[]domain.AccountDataset{datasetOne, datasetTwo},
		[]*domain.AccountSnapshot{summarySnapshot(accountID, datasetOne, 11, "fresh", "new.snapshot")},
		[]*domain.AccountSource{summarySourceRow(accountID, "source-one", 11, "active", "new.source")},
		[]*domain.AccountMembership{summaryMembership(accountID, "scope-one", "member-one", 11, "active", "new.member")})
	if _, err := app.ApplySummary(t.Context(), refresh); err != nil {
		t.Fatalf("refresh summary: %v", err)
	}
	refreshed := loadSummaryState(t, db, accountID)
	if refreshed.snapshotOne.Version != 11 || refreshed.snapshotOne.Data["value"] != "new.snapshot" {
		t.Fatalf("snapshot refresh = %+v", refreshed.snapshotOne)
	}
	if refreshed.sources["source-one"].Version != 11 || refreshed.sources["source-one"].Data["value"] != "new.source" {
		t.Fatalf("source refresh = %+v", refreshed.sources["source-one"])
	}
	if refreshed.memberships["scope-one\x00member-one"].Version != 11 ||
		refreshed.memberships["scope-one\x00member-one"].Data["value"] != "new.member" {
		t.Fatalf("membership refresh = %+v", refreshed.memberships["scope-one\x00member-one"])
	}
	if refreshed.snapshotTwo == nil || refreshed.snapshotTwo.Version != 10 || refreshed.snapshotTwo.Status != domain.SnapshotFresh {
		t.Fatalf("normal refresh omission changed snapshot-two: %+v", refreshed.snapshotTwo)
	}
	if len(refreshed.sources) != 2 || len(refreshed.memberships) != 2 ||
		refreshed.sources["source-two"].Version != 10 || refreshed.memberships["scope-two\x00member-two"].Version != 10 {
		t.Fatalf("normal refresh omission changed references: sources=%+v memberships=%+v", refreshed.sources, refreshed.memberships)
	}

	second := summaryRequest(accountID, 12, true, true,
		[]domain.AccountDataset{datasetOne, datasetTwo},
		[]*domain.AccountSnapshot{summarySnapshot(accountID, datasetOne, 12, "fresh", "reconciled.snapshot")},
		[]*domain.AccountSource{summarySourceRow(accountID, "source-one", 12, "active", "reconciled.source")},
		[]*domain.AccountMembership{summaryMembership(accountID, "scope-one", "member-one", 12, "active", "reconciled.member")})
	if _, err := app.ApplySummary(t.Context(), second); err != nil {
		t.Fatalf("reconciled summary: %v", err)
	}
	state := loadSummaryState(t, db, accountID)
	if state.snapshotTwo == nil || state.snapshotTwo.Status != domain.SnapshotUnavailable ||
		state.snapshotTwo.Version != 12 || state.snapshotTwo.ErrorCode != "source.reconciled_absent" {
		t.Fatalf("omitted snapshot = %+v", state.snapshotTwo)
	}
	if omitted := state.sources["source-two"]; omitted == nil || omitted.Status != "unavailable" ||
		omitted.Version != 12 || omitted.ErrorCode != "source.reconciled_absent" {
		t.Fatalf("omitted source = %+v", omitted)
	}
	if omitted := state.memberships["scope-two\x00member-two"]; omitted == nil || omitted.Status != "unavailable" ||
		omitted.Version != 12 || omitted.ErrorCode != "source.reconciled_absent" {
		t.Fatalf("omitted membership = %+v", omitted)
	}
}

func TestSummaryApplicationRollsBackEveryProjectionOnTriggerFailure(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	app, accountID := summaryApplicationFixture(t, db)
	seed := summaryRequest(accountID, 10, true, false,
		[]domain.AccountDataset{datasetOne, datasetTwo},
		[]*domain.AccountSnapshot{
			summarySnapshot(accountID, datasetOne, 10, "fresh", "snapshot-one"),
			summarySnapshot(accountID, datasetTwo, 10, "fresh", "snapshot-two"),
		},
		[]*domain.AccountSource{
			summarySourceRow(accountID, "source-one", 10, "active", "source-one"),
			summarySourceRow(accountID, "source-two", 10, "active", "source-two"),
		},
		[]*domain.AccountMembership{
			summaryMembership(accountID, "scope-one", "member-one", 10, "active", "member-one"),
			summaryMembership(accountID, "scope-two", "member-two", 10, "active", "member-two"),
		})
	if _, err := app.ApplySummary(t.Context(), seed); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	before := loadSummaryState(t, db, accountID)
	installSummaryFailureTrigger(t, db)

	failed := summaryRequest(accountID, 99, true, true,
		[]domain.AccountDataset{datasetOne, datasetTwo},
		[]*domain.AccountSnapshot{summarySnapshot(accountID, datasetOne, 99, "fresh", "changed.snapshot")},
		[]*domain.AccountSource{summarySourceRow(accountID, "source-one", 99, "active", "changed.source")},
		[]*domain.AccountMembership{summaryMembership(accountID, "scope-one", "member-one", 99, "active", "changed.member")})
	if _, err := app.ApplySummary(t.Context(), failed); err == nil {
		t.Fatal("trigger failure was not returned")
	}
	if after := loadSummaryState(t, db, accountID); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed summary partially committed:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestSummaryApplicationIncompleteAndConcurrentSummariesAreSafe(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	app, accountID := summaryApplicationFixture(t, db)
	seed := summaryRequest(accountID, 1, true, false,
		[]domain.AccountDataset{datasetOne, datasetTwo},
		[]*domain.AccountSnapshot{summarySnapshot(accountID, datasetOne, 1, "fresh", "one"), summarySnapshot(accountID, datasetTwo, 1, "fresh", "two")},
		[]*domain.AccountSource{summarySourceRow(accountID, "source-one", 1, "active", "one"), summarySourceRow(accountID, "source-two", 1, "active", "two")},
		[]*domain.AccountMembership{summaryMembership(accountID, "scope-one", "member-one", 1, "active", "one"), summaryMembership(accountID, "scope-two", "member-two", 1, "active", "two")})
	if _, err := app.ApplySummary(t.Context(), seed); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	before := loadSummaryState(t, db, accountID)
	incomplete := summaryRequest(accountID, 2, false, true, []domain.AccountDataset{datasetOne, datasetTwo},
		[]*domain.AccountSnapshot{summarySnapshot(accountID, datasetOne, 2, "fresh", "must-not-write")}, nil, nil)
	incomplete.Complete = false
	incomplete.SkippedDatasets = []string{"aero-vault.future"}
	if _, err := app.ApplySummary(t.Context(), incomplete); err != nil {
		t.Fatalf("incomplete summary: %v", err)
	}
	if after := loadSummaryState(t, db, accountID); !reflect.DeepEqual(before, after) {
		t.Fatalf("incomplete summary changed state:\nbefore=%+v\nafter=%+v", before, after)
	}

	old := summaryRequest(accountID, 20, true, true, []domain.AccountDataset{datasetOne, datasetTwo},
		[]*domain.AccountSnapshot{summarySnapshot(accountID, datasetOne, 20, "fresh", "old")},
		[]*domain.AccountSource{summarySourceRow(accountID, "source-one", 20, "active", "old")},
		[]*domain.AccountMembership{summaryMembership(accountID, "scope-one", "member-one", 20, "active", "old")})
	newer := summaryRequest(accountID, 21, true, true, []domain.AccountDataset{datasetOne, datasetTwo},
		[]*domain.AccountSnapshot{summarySnapshot(accountID, datasetTwo, 21, "fresh", "new")},
		[]*domain.AccountSource{summarySourceRow(accountID, "source-two", 21, "active", "new")},
		[]*domain.AccountMembership{summaryMembership(accountID, "scope-two", "member-two", 21, "active", "new")})
	var wait sync.WaitGroup
	errCh := make(chan error, 2)
	wait.Add(2)
	go func() { defer wait.Done(); _, err := app.ApplySummary(t.Context(), old); errCh <- err }()
	go func() { defer wait.Done(); _, err := app.ApplySummary(t.Context(), newer); errCh <- err }()
	wait.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent summary: %v", err)
		}
	}
	state := loadSummaryState(t, db, accountID)
	if state.snapshotTwo == nil || state.snapshotTwo.Version != 21 || state.snapshotTwo.Status != domain.SnapshotFresh {
		t.Fatalf("newer snapshot did not win: %+v", state.snapshotTwo)
	}
	if state.snapshotOne == nil || state.snapshotOne.Version != 21 || state.snapshotOne.Status != domain.SnapshotUnavailable {
		t.Fatalf("newer omission did not fence older summary: %+v", state.snapshotOne)
	}
	if state.sources["source-two"] == nil || state.sources["source-two"].Version != 21 ||
		state.sources["source-one"] == nil || state.sources["source-one"].Version != 21 {
		t.Fatalf("source versions after concurrency = %+v", state.sources)
	}
	if state.memberships["scope-two\x00member-two"] == nil || state.memberships["scope-two\x00member-two"].Version != 21 {
		t.Fatalf("membership versions after concurrency = %+v", state.memberships)
	}
}

func summaryApplicationFixture(t *testing.T, db *gorm.DB) (repository.SummaryApplication, domain.ID) {
	t.Helper()
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "summary-tenant", "snaplink", "summary-user", "summary-user", summaryRegion)
	if err != nil {
		t.Fatalf("ensure account: %v", err)
	}
	return repository.NewAccountSummaryApplication(db), account.ID
}

func summaryRequest(accountID domain.ID, version int64, complete, reconcile bool, requested []domain.AccountDataset,
	snapshots []*domain.AccountSnapshot, sources []*domain.AccountSource, memberships []*domain.AccountMembership) domain.SummaryApplicationRequest {
	return domain.SummaryApplicationRequest{AccountID: accountID, Source: summarySource, SourceRegion: summaryRegion,
		Version: version, RequestedDatasets: requested, Complete: complete, Reconcile: reconcile,
		Snapshots: snapshots, Sources: sources, Memberships: memberships}
}

func summarySnapshot(accountID domain.ID, dataset domain.AccountDataset, version int64, status, value string) *domain.AccountSnapshot {
	generated := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	synced := generated.Add(time.Hour)
	expires := generated.Add(24 * time.Hour)
	return &domain.AccountSnapshot{AccountID: accountID, Source: summarySource, SourceRegion: summaryRegion,
		Dataset: dataset, Status: domain.SnapshotStatus(status), Version: version, GeneratedAt: generated,
		LastSyncedAt: &synced, ExpiresAt: &expires, ErrorCode: "source." + value, Data: map[string]any{"value": value}}
}

func summarySourceRow(accountID domain.ID, sourceID string, version int64, status, value string) *domain.AccountSource {
	generated := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	synced := generated.Add(time.Hour)
	return &domain.AccountSource{AccountID: accountID, Source: summarySource, SourceRegion: summaryRegion,
		SourceAccountID: sourceID, Status: status, Version: version, GeneratedAt: &generated,
		LastSyncedAt: &synced, ErrorCode: "source." + value, Data: map[string]any{"value": value}}
}

func summaryMembership(accountID domain.ID, scopeID, memberID string, version int64, status, value string) *domain.AccountMembership {
	synced := time.Date(2026, time.January, 1, 1, 0, 0, 0, time.UTC)
	return &domain.AccountMembership{AccountID: accountID, Source: summarySource, SourceRegion: summaryRegion,
		ScopeType: "workspace", ScopeID: scopeID, SourceMemberID: memberID, Role: "member", Status: status,
		Version: version, LastSyncedAt: &synced, ErrorCode: "source." + value, Data: map[string]any{"value": value}}
}

func loadSummaryState(t *testing.T, db *gorm.DB, accountID domain.ID) summaryProjectionState {
	t.Helper()
	snapshots := repository.NewAccountSnapshotRepository(db)
	accounts := repository.NewAccountRepository(db, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{}))
	state := summaryProjectionState{sources: map[string]*domain.AccountSource{}, memberships: map[string]*domain.AccountMembership{}}
	var err error
	state.snapshotOne, err = snapshots.GetSnapshot(t.Context(), accountID, summarySource, datasetOne)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		state.snapshotOne = nil
	} else if err != nil {
		t.Fatalf("load snapshot one: %v", err)
	}
	state.snapshotTwo, err = snapshots.GetSnapshot(t.Context(), accountID, summarySource, datasetTwo)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		state.snapshotTwo = nil
	} else if err != nil {
		t.Fatalf("load snapshot two: %v", err)
	}
	sources, _, err := accounts.ListSources(t.Context(), accountID, domain.Page{Limit: 100})
	if err != nil {
		t.Fatalf("load sources: %v", err)
	}
	for _, source := range sources {
		state.sources[source.SourceAccountID] = source
	}
	memberships, _, err := accounts.ListMemberships(t.Context(), accountID, domain.Page{Limit: 100})
	if err != nil {
		t.Fatalf("load memberships: %v", err)
	}
	for _, membership := range memberships {
		state.memberships[membership.ScopeID+"\x00"+membership.SourceMemberID] = membership
	}
	return state
}

func membershipErrorCodeColumnExists(t *testing.T, db *gorm.DB) bool {
	t.Helper()
	var exists bool
	if err := db.Raw(`SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'account_memberships' AND column_name = 'error_code'
	)`).Scan(&exists).Error; err != nil {
		t.Fatalf("check membership error_code: %v", err)
	}
	return exists
}

func installSummaryFailureTrigger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE OR REPLACE FUNCTION fail_summary_membership() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.version = 99 AND NEW.source_member_id = 'member-two' THEN
		RAISE EXCEPTION 'summary membership trigger failure';
	END IF;
	RETURN NEW;
END
$$`).Error; err != nil {
		t.Fatalf("create summary trigger function: %v", err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_summary_membership_trigger
	BEFORE INSERT OR UPDATE ON account_memberships
	FOR EACH ROW EXECUTE FUNCTION fail_summary_membership()`).Error; err != nil {
		t.Fatalf("create summary trigger: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DROP TRIGGER IF EXISTS fail_summary_membership_trigger ON account_memberships`)
		_ = db.Exec(`DROP FUNCTION IF EXISTS fail_summary_membership()`)
	})
}
