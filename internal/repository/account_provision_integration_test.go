//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"gorm.io/gorm"
)

const provisionTestKey = "integration-provision-audit-key-0123456789abcdef"

func startProvisionLedger(t *testing.T, key string) (*gorm.DB, repository.AccountProvisionAuditRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: key})
	accountRepo := repository.NewAccountRepository(db, ledger)
	provRepo, ok := accountRepo.(repository.AccountProvisionAuditRepository)
	if !ok {
		t.Fatal("account repo must implement AccountProvisionAuditRepository")
	}
	return db, provRepo
}

func provisionFact(eventID, accountID domain.ID, tenantID string) *domain.AuditFact {
	return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: tenantID + ":" + accountID.String(),
		EventID: eventID, AccountID: accountID, TenantID: tenantID, ActorUID: "u-1", Action: "account.provisioned",
		ResourceType: "account", ResourceID: accountID.String(), OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"home_region": "local"}}
}

func countProvisionLedgerRows(t *testing.T, db *gorm.DB, accountID domain.ID) (int64, int64) {
	t.Helper()
	var ledgerRows, publicationRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE account_id = ?`, accountID).Scan(&ledgerRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.account_id = ?`, accountID).Scan(&publicationRows).Error; err != nil {
		t.Fatal(err)
	}
	return ledgerRows, publicationRows
}

// TestEnsureByIdentityWithAuditCreatesAccountAndFact (REQ-3 / T-10.1): the
// account.provisioned fact commits in the SAME transaction as the account row
// (event_id = pre-assigned accountID, partition tenant:accountID); a forced
// append failure (keyless ledger, FM-8) rolls the account creation back.
func TestEnsureByIdentityWithAuditCreatesAccountAndFact(t *testing.T) {
	ctx := context.Background()
	db, provRepo := startProvisionLedger(t, provisionTestKey)

	accountID := idgen.NewID()
	account, err := provRepo.EnsureByIdentityWithAudit(ctx, "tenant-a", "snaplink", "u-1", "u-1", "local",
		accountID, provisionFact(accountID, accountID, "tenant-a"))
	if err != nil {
		t.Fatalf("EnsureByIdentityWithAudit: %v", err)
	}
	if account.ID != accountID {
		t.Fatalf("account id = %s, want pre-assigned %s", account.ID, accountID)
	}
	ledgerRows, publicationRows := countProvisionLedgerRows(t, db, accountID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("ledger/publication rows = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
	var action string
	if err := db.Raw(`SELECT action FROM audit_ledger WHERE event_id = ?`, accountID).Scan(&action).Error; err != nil {
		t.Fatal(err)
	}
	if action != "account.provisioned" {
		t.Fatalf("fact action = %q, want account.provisioned", action)
	}
	var partition string
	if err := db.Raw(`SELECT partition_key FROM audit_ledger WHERE event_id = ?`, accountID).Scan(&partition).Error; err != nil {
		t.Fatal(err)
	}
	if partition != "tenant-a:"+accountID.String() {
		t.Fatalf("partition = %q, want tenant:accountID", partition)
	}

	// Metered leg (A3): quota variant appends in the same tx.
	quotaID := idgen.NewID()
	quotaAccount, err := provRepo.EnsureByIdentityWithQuotaWithAudit(ctx, "tenant-a", "snaplink", "u-quota", "u-quota", "local",
		"res-1", quotaID, provisionFact(quotaID, quotaID, "tenant-a"))
	if err != nil {
		t.Fatalf("EnsureByIdentityWithQuotaWithAudit: %v", err)
	}
	if quotaAccount.ID != quotaID {
		t.Fatalf("quota account id = %s, want %s", quotaAccount.ID, quotaID)
	}
	ledgerRows, publicationRows = countProvisionLedgerRows(t, db, quotaID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("quota ledger/publication rows = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
	var quotaRows int64
	if err := db.Raw(`SELECT count(*) FROM account_quota_reservations WHERE account_id = ?`, quotaID).Scan(&quotaRows).Error; err != nil {
		t.Fatal(err)
	}
	if quotaRows != 1 {
		t.Fatalf("quota reservation rows = %d, want 1", quotaRows)
	}
}

// TestEnsureByIdentityWithAuditRollbackWithoutSigningKey (FM-1/FM-8): with a
// keyless ledger the in-tx append fails and the whole account creation rolls
// back — no orphan account, no ledger rows, no publication rows.
func TestEnsureByIdentityWithAuditRollbackWithoutSigningKey(t *testing.T) {
	ctx := context.Background()
	db, provRepo := startProvisionLedger(t, "")

	accountID := idgen.NewID()
	_, err := provRepo.EnsureByIdentityWithAudit(ctx, "tenant-a", "snaplink", "u-rb", "u-rb", "local",
		accountID, provisionFact(accountID, accountID, "tenant-a"))
	if err == nil {
		t.Fatal("provision with keyless ledger must fail")
	}
	var accountRows int64
	if err := db.Raw(`SELECT count(*) FROM accounts WHERE id = ?`, accountID).Scan(&accountRows).Error; err != nil {
		t.Fatal(err)
	}
	if accountRows != 0 {
		t.Fatalf("account rows after rollback = %d, want 0", accountRows)
	}
	ledgerRows, publicationRows := countProvisionLedgerRows(t, db, accountID)
	if ledgerRows != 0 || publicationRows != 0 {
		t.Fatalf("ledger/publication rows after rollback = %d/%d, want 0/0", ledgerRows, publicationRows)
	}
}

// TestEnsureByIdentityWithAuditRepeatAppendsNothing (A4 / FM-5): a second
// ensure of the same identity returns the existing account, touches
// last_seen_at, and appends nothing — exactly one ledger + publication row.
func TestEnsureByIdentityWithAuditRepeatAppendsNothing(t *testing.T) {
	ctx := context.Background()
	db, provRepo := startProvisionLedger(t, provisionTestKey)

	firstID := idgen.NewID()
	first, err := provRepo.EnsureByIdentityWithAudit(ctx, "tenant-a", "snaplink", "u-repeat", "u-repeat", "local",
		firstID, provisionFact(firstID, firstID, "tenant-a"))
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	var lastSeen time.Time
	if err := db.Raw(`SELECT last_seen_at FROM account_identities WHERE tenant_id = 'tenant-a' AND issuer = 'snaplink' AND subject = 'u-repeat'`).Scan(&lastSeen).Error; err != nil {
		t.Fatal(err)
	}

	secondID := idgen.NewID()
	second, err := provRepo.EnsureByIdentityWithAudit(ctx, "tenant-a", "snaplink", "u-repeat", "u-repeat", "local",
		secondID, provisionFact(secondID, secondID, "tenant-a"))
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second account id = %s, want existing %s", second.ID, first.ID)
	}
	var touched time.Time
	if err := db.Raw(`SELECT last_seen_at FROM account_identities WHERE tenant_id = 'tenant-a' AND issuer = 'snaplink' AND subject = 'u-repeat'`).Scan(&touched).Error; err != nil {
		t.Fatal(err)
	}
	if !touched.After(lastSeen) {
		t.Fatalf("last_seen_at not touched: %v -> %v", lastSeen, touched)
	}
	ledgerRows, publicationRows := countProvisionLedgerRows(t, db, first.ID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("ledger/publication rows after repeat = %d/%d, want 1/1 (no duplicate append)", ledgerRows, publicationRows)
	}

	// Quota variant: same assertions.
	quotaID := idgen.NewID()
	if _, err := provRepo.EnsureByIdentityWithQuotaWithAudit(ctx, "tenant-a", "snaplink", "u-qrepeat", "u-qrepeat", "local",
		"res-r1", quotaID, provisionFact(quotaID, quotaID, "tenant-a")); err != nil {
		t.Fatalf("first quota ensure: %v", err)
	}
	quotaRepeatID := idgen.NewID()
	secondQuota, err := provRepo.EnsureByIdentityWithQuotaWithAudit(ctx, "tenant-a", "snaplink", "u-qrepeat", "u-qrepeat", "local",
		"res-r1", quotaRepeatID, provisionFact(quotaRepeatID, quotaRepeatID, "tenant-a"))
	if err != nil {
		t.Fatalf("second quota ensure: %v", err)
	}
	if secondQuota.ID != quotaID {
		t.Fatalf("second quota account = %s, want %s", secondQuota.ID, quotaID)
	}
	ledgerRows, publicationRows = countProvisionLedgerRows(t, db, quotaID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("quota ledger/publication rows after repeat = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
}

// TestEnsureByIdentityWithAuditConcurrentProvision (FM-6): concurrent
// provisioning of the same identity — unmetered and same-reservation metered
// losers commit factless; different-reservation metered losers fail in
// ensureAccountQuotaTx (pre-existing) and roll back without a fact. Exactly one
// account, one identity, one ledger row, one publication row in every case.
func TestEnsureByIdentityWithAuditConcurrentProvision(t *testing.T) {
	ctx := context.Background()
	db, provRepo := startProvisionLedger(t, provisionTestKey)

	run := func(subject, reservationA, reservationB string) ([]*domain.Account, []error) {
		t.Helper()
		const workers = 4
		start := make(chan struct{})
		type outcome struct {
			account *domain.Account
			err     error
		}
		results := make(chan outcome, workers)
		for i := 0; i < workers; i++ {
			go func(i int) {
				<-start
				accountID := idgen.NewID()
				reservation := reservationA
				if i%2 == 1 && reservationB != "" {
					reservation = reservationB
				}
				account, err := provRepo.EnsureByIdentityWithQuotaWithAudit(ctx, "tenant-a", "snaplink", subject, subject, "local",
					reservation, accountID, provisionFact(accountID, accountID, "tenant-a"))
				results <- outcome{account: account, err: err}
			}(i)
		}
		close(start)
		var accounts []*domain.Account
		var errs []error
		for i := 0; i < workers; i++ {
			out := <-results
			if out.err != nil {
				errs = append(errs, out.err)
				continue
			}
			if out.account != nil {
				accounts = append(accounts, out.account)
			}
		}
		return accounts, errs
	}

	// Unmetered: 4 concurrent ensures, all return the same account.
	var accounts []*domain.Account
	{
		start := make(chan struct{})
		results := make(chan *domain.Account, 4)
		for i := 0; i < 4; i++ {
			go func() {
				<-start
				accountID := idgen.NewID()
				account, err := provRepo.EnsureByIdentityWithAudit(ctx, "tenant-a", "snaplink", "u-conc", "u-conc", "local",
					accountID, provisionFact(accountID, accountID, "tenant-a"))
				if err != nil {
					t.Errorf("concurrent ensure error: %v", err)
					results <- nil
					return
				}
				results <- account
			}()
		}
		close(start)
		for i := 0; i < 4; i++ {
			if account := <-results; account != nil {
				accounts = append(accounts, account)
			}
		}
	}
	if len(accounts) != 4 {
		t.Fatalf("successful unmetered provisions = %d, want 4", len(accounts))
	}
	winner := accounts[0].ID
	for _, account := range accounts[1:] {
		if account.ID != winner {
			t.Fatalf("unmetered accounts diverged: %s vs %s", account.ID, winner)
		}
	}
	var accountRows, identityRows int64
	if err := db.Raw(`SELECT count(*) FROM accounts WHERE tenant_id = 'tenant-a' AND canonical_uid = 'u-conc'`).Scan(&accountRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM account_identities WHERE tenant_id = 'tenant-a' AND issuer = 'snaplink' AND subject = 'u-conc'`).Scan(&identityRows).Error; err != nil {
		t.Fatal(err)
	}
	if accountRows != 1 || identityRows != 1 {
		t.Fatalf("unmetered account/identity rows = %d/%d, want 1/1", accountRows, identityRows)
	}
	ledgerRows, publicationRows := countProvisionLedgerRows(t, db, winner)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("unmetered ledger/publication rows = %d/%d, want 1/1", ledgerRows, publicationRows)
	}

	// Metered, same reservation: identical outcome.
	mAccounts, mErrs := run("u-conc-meta", "res-meta", "")
	if len(mErrs) != 0 {
		t.Fatalf("same-reservation metered errors: %v", mErrs)
	}
	if len(mAccounts) != 4 {
		t.Fatalf("same-reservation metered successes = %d, want 4", len(mAccounts))
	}
	for _, account := range mAccounts[1:] {
		if account.ID != mAccounts[0].ID {
			t.Fatalf("same-reservation accounts diverged")
		}
	}
	ledgerRows, publicationRows = countProvisionLedgerRows(t, db, mAccounts[0].ID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("same-reservation ledger/publication rows = %d/%d, want 1/1", ledgerRows, publicationRows)
	}

	// Metered, different reservations: exactly one distinct account + one fact;
	// same-reservation losers commit factless, different-reservation losers fail
	// with the pre-existing reservation mismatch (tx fully rolled back).
	dAccounts, dErrs := run("u-conc-diff", "res-diff-a", "res-diff-b")
	if len(dErrs) == 0 {
		t.Fatal("different-reservation losers must fail (account quota reservation mismatch)")
	}
	if len(dAccounts) < 1 {
		t.Fatalf("different-reservation successful accounts = %d, want >= 1 (winner + same-reservation losers)", len(dAccounts))
	}
	distinct := map[domain.ID]struct{}{}
	for _, account := range dAccounts {
		distinct[account.ID] = struct{}{}
	}
	if len(distinct) != 1 {
		t.Fatalf("different-reservation distinct accounts = %d, want exactly 1", len(distinct))
	}
	ledgerRows, publicationRows = countProvisionLedgerRows(t, db, dAccounts[0].ID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("different-reservation ledger/publication rows = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
}
