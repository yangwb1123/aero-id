//go:build integration

package repository_test

import (
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestAgentMembershipProjectionRequiresExactFreshActiveTenantRows(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "hub-user", "hub-user", "local")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	syncedAt := now.Add(-time.Minute)
	membership := &domain.AccountMembership{
		AccountID: account.ID, Source: "snaplink", ScopeType: "tenant", ScopeID: "scope-a",
		SourceMemberID: "member-a", Status: "active", Version: 1, LastSyncedAt: &syncedAt,
	}
	if err := accounts.UpsertMembership(t.Context(), membership); err != nil {
		t.Fatal(err)
	}
	reader := accounts.(repository.AgentMembershipProjectionRepository)
	check := func(tenant string, at time.Time) bool {
		t.Helper()
		eligible, checkErr := reader.HasFreshActiveMembership(t.Context(), tenant, account.ID,
			"snaplink", "tenant", "scope-a", at.Add(-5*time.Minute), at)
		if checkErr != nil {
			t.Fatalf("membership projection query: %v", checkErr)
		}
		return eligible
	}
	if !check("tenant-a", now) {
		t.Fatal("fresh active membership should be eligible")
	}
	if check("tenant-b", now) {
		t.Fatal("account was visible through a different tenant")
	}

	updates := []struct {
		name   string
		values map[string]any
	}{
		{name: "inactive", values: map[string]any{"status": "unavailable", "error_code": ""}},
		{name: "error", values: map[string]any{"status": "active", "error_code": "source.failed"}},
		{name: "stale", values: map[string]any{"status": "active", "error_code": "", "last_synced_at": now.Add(-6 * time.Minute)}},
		{name: "future", values: map[string]any{"status": "active", "error_code": "", "last_synced_at": now.Add(time.Minute)}},
	}
	for _, test := range updates {
		t.Run(test.name, func(t *testing.T) {
			if err := db.Table("account_memberships").Where("account_id = ?", account.ID).Updates(test.values).Error; err != nil {
				t.Fatal(err)
			}
			if check("tenant-a", now) {
				t.Fatal("non-active, erroneous, stale, or future projection was eligible")
			}
			if err := db.Table("account_memberships").Where("account_id = ?", account.ID).Updates(map[string]any{
				"status": "active", "error_code": "", "last_synced_at": syncedAt,
			}).Error; err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := db.Table("accounts").Where("id = ?", account.ID).Update("status", "suspended").Error; err != nil {
		t.Fatal(err)
	}
	if check("tenant-a", now) {
		t.Fatal("suspended account was eligible")
	}
}
