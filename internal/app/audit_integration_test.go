//go:build integration

package app

import (
	"path/filepath"
	"testing"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"github.com/aero/aero-id/internal/service"
)

func TestAuditBindingCoverageIncludesAccountsAndDurableBacklog(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := service.NewAuditLedgerService(repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "app-integration-audit-ledger-key-0123456789"}))
	if err := ledger.Record(t.Context(), service.AuditFactInput{
		TenantID: "tenant-publication", Action: "account.created", ActorUID: "actor-one",
	}); err != nil {
		t.Fatal(err)
	}
	accounts := repository.NewAccountRepository(db)
	if _, err := accounts.EnsureByIdentity(
		t.Context(), "tenant-account", "snaplink", "subject-one", "actor-two", "local",
	); err != nil {
		t.Fatal(err)
	}

	bindings := map[string]config.AuditGovernanceBindingConfig{
		"tenant-publication": {},
	}
	if err := validateAuditBindingCoverage(db, bindings); err == nil {
		t.Fatal("account tenant without an audit binding passed startup coverage")
	}
	bindings["tenant-account"] = config.AuditGovernanceBindingConfig{}
	if err := validateAuditBindingCoverage(db, bindings); err != nil {
		t.Fatalf("complete durable tenant coverage failed: %v", err)
	}
}
