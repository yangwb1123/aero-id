//go:build integration

package repository_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

func TestErasedAccountRejectsProfileSummaryAndDirectProjectionWrites(t *testing.T) {
	db, account, _, _ := newEventContentHarness(t)
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	snapshots := repository.NewAccountSnapshotRepository(db)
	application := repository.NewAccountSummaryApplication(db)
	request := summaryRequest(account.ID, 1, true, false, []domain.AccountDataset{datasetTwo},
		[]*domain.AccountSnapshot{summarySnapshot(account.ID, datasetTwo, 1, "fresh", "initial")},
		[]*domain.AccountSource{summarySourceRow(account.ID, "native-account", 1, "active", "initial")},
		[]*domain.AccountMembership{summaryMembership(account.ID, "scope", "native-member", 1, "active", "initial")})
	if _, err := application.ApplySummary(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := accounts.UpdateProfile(t.Context(), &domain.AccountProfile{AccountID: account.ID,
		DisplayName: "Before erase", Timezone: "Asia/Shanghai", Preferences: map[string]any{"locale": "zh"}}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.EraseAccount(t.Context(), account.TenantID, account.ID); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name  string
		write func() error
	}{
		{"profile", func() error {
			return accounts.UpdateProfile(t.Context(), &domain.AccountProfile{AccountID: account.ID, DisplayName: "Restore"})
		}},
		{"snapshot", func() error {
			return snapshots.UpsertSnapshot(t.Context(), summarySnapshot(account.ID, datasetTwo, 999, "fresh", "restore"))
		}},
		{"source", func() error {
			return accounts.UpsertSource(t.Context(), summarySourceRow(account.ID, "new-native", 999, "active", "restore"))
		}},
		{"membership", func() error {
			return accounts.UpsertMembership(t.Context(), summaryMembership(account.ID, "new-scope", "new-member", 999, "active", "restore"))
		}},
		{"summary", func() error {
			_, err := application.ApplySummary(t.Context(), summaryRequest(account.ID, 999, true, true,
				[]domain.AccountDataset{datasetTwo}, []*domain.AccountSnapshot{summarySnapshot(account.ID, datasetTwo, 999, "fresh", "restore")}, nil, nil))
			return err
		}},
		{"omission", func() error {
			_, err := snapshots.(repository.SnapshotReconciler).MarkUnavailable(t.Context(), account.ID,
				summarySource, []domain.AccountDataset{datasetTwo}, 999)
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.write(); !errors.Is(err, repository.ErrAccountErased) {
				t.Fatalf("write = %v, want durable account disposition rejection", err)
			}
		})
	}
	profile, err := accounts.GetProfile(t.Context(), account.ID)
	if err != nil || profile.DisplayName != "" || profile.Timezone != "" || len(profile.Preferences) != 0 {
		t.Fatalf("erased profile = %v/%v", profile, err)
	}
	sources, _, err := accounts.ListSources(t.Context(), account.ID, domain.Page{})
	if err != nil || len(sources) != 1 || len(sources[0].Data) != 0 || sources[0].Status != "unavailable" {
		t.Fatalf("erased references = %v/%v", sources, err)
	}
	memberships, _, err := accounts.ListMemberships(t.Context(), account.ID, domain.Page{})
	if err != nil || len(memberships) != 1 || len(memberships[0].Data) != 0 || memberships[0].Role != "" {
		t.Fatalf("erased memberships = %v/%v", memberships, err)
	}
	ensured, err := accounts.EnsureByIdentity(t.Context(), account.TenantID, "snaplink", "event-content", "event-content", "local")
	if err != nil || ensured.ID != account.ID || ensured.Status != domain.AccountErased {
		t.Fatalf("ensured tombstone = %v/%v", ensured, err)
	}
	var uid string
	if err := db.Table("account_identities").Select("canonical_uid").Where("account_id = ?", account.ID).Scan(&uid).Error; err != nil {
		t.Fatal(err)
	}
	if uid != "erased:"+account.ID.String() {
		t.Fatalf("identity canonical uid was rehydrated: %q", uid)
	}
	changes, err := snapshots.(repository.ReplicationChangeRepository).ListReplicationChangesAfter(t.Context(), "local", 0, 10)
	if err != nil || len(changes) != 2 || changes[1].Snapshot.Status != domain.SnapshotUnavailable || len(changes[1].Snapshot.Data) != 0 {
		t.Fatalf("erasure invalidation log = %v/%v", changes, err)
	}
	if err := accounts.EraseAccount(t.Context(), account.TenantID, account.ID); err != nil {
		t.Fatal(err)
	}
	changes, err = snapshots.(repository.ReplicationChangeRepository).ListReplicationChangesAfter(t.Context(), "local", 0, 10)
	if err != nil || len(changes) != 2 {
		t.Fatalf("repeated erase emitted another invalidation: %v/%v", changes, err)
	}
}

func TestLateEventAfterEraseIsDurablyQuarantinedWithoutContent(t *testing.T) {
	db, account, store, receipts := newEventContentHarness(t)
	accounts := repository.NewAccountRepository(db, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{}))
	if err := accounts.EraseAccount(t.Context(), account.TenantID, account.ID); err != nil {
		t.Fatal(err)
	}
	envelope := eventStoreEnvelope(account.ID, string(datasetTwo), 999, time.Now().UTC())
	envelope.Payload["data"] = map[string]any{"usage_bytes": 999, "name": "late-private-presentation"}
	mutation, err := envelope.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := receipts.(repository.EventSnapshotAuditRepository).ApplySnapshotEventWithAudit(
			t.Context(), envelope, mutation, eventContentAuditFact(envelope))
		if err != nil || result.Status != repository.EventQuarantined || result.ErrorCode != "event.account_erased" {
			t.Fatalf("late event/retry = %+v/%v", result, err)
		}
	}
	var payload string
	if err := db.Table("event_receipts").Select("payload::text").Where("event_id = ?", envelope.EventID).Scan(&payload).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, "lifecycle") || strings.Contains(payload, "usage_bytes") || strings.Contains(payload, "late-private") {
		t.Fatalf("quarantine retained source content: %s", payload)
	}
	if count := countEventContentRows(t, db, "event_store", envelope); count != 0 {
		t.Fatalf("late event entered the replay store: %d", count)
	}
	var auditCount int64
	if err := db.Table("audit_ledger").Where("event_id = ? AND payload->>'status' = 'quarantined'", envelope.EventID).Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("late-event audit facts = %d, want one durable fact", auditCount)
	}
	if _, _, err := store.ListByAccount(t.Context(), "tenant-a", "aero-vault", account.ID.String(), domain.ID{}, 100); err != nil {
		t.Fatal(err)
	}
}

func TestProfileWriteWaitsForConcurrentEraseAndThenRejects(t *testing.T) {
	db, account, _, _ := newEventContentHarness(t)
	accounts := repository.NewAccountRepository(db, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{}))
	holding := db.Begin()
	if holding.Error != nil {
		t.Fatal(holding.Error)
	}
	defer holding.Rollback()
	if err := holding.Exec("UPDATE accounts SET status = 'erased' WHERE id = ?", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- accounts.UpdateProfile(ctx, &domain.AccountProfile{AccountID: account.ID, DisplayName: "must not return"})
	}()
	waitForAccountLock(t, db)
	select {
	case err := <-finished:
		t.Fatalf("profile writer bypassed uncommitted disposition: %v", err)
	default:
	}
	if err := holding.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, repository.ErrAccountErased) {
			t.Fatalf("post-commit write = %v, want erased", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer failed to resume after disposition commit")
	}
}

func waitForAccountLock(t *testing.T, db *gorm.DB) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int64
		if err := db.Raw(`SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%accounts%' AND pid <> pg_backend_pid()`).Scan(&waiting).Error; err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("writer never waited on the authoritative account lock")
}
