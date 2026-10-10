//go:build integration

package repository_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/pkg/migrate"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

func intentFact(intent *domain.CommandIntent, event domain.ID, action string) *domain.AuditFact {
	return &domain.AuditFact{ID: idgen.NewID(), EventID: event, Source: "aero-id", SourceRegion: "local",
		PartitionKey: intent.Principal.TenantID + ":" + intent.AccountID.String(), TenantID: intent.Principal.TenantID,
		ActorUID: intent.Principal.ActorUID, AccountID: intent.AccountID, Action: action, ResourceType: "command_intent",
		ResourceID: intent.ID.String(), OccurredAt: time.Now().UTC(), Payload: map[string]any{"version": intent.Version}}
}
func intentPostgresFixture(t *testing.T, db *gorm.DB, subject string) (repository.CommandIntentRepository, *domain.CommandIntent) {
	t.Helper()
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "intent-test-key"})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "intent-tenant", "https://snaplink.example", subject, subject, "local")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.NewRegionDirectoryRepository(db, ledger).EnsureDirectory(t.Context(), account.TenantID, account.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	repo := repository.NewCommandIntentRepository(db, ledger)
	pre, err := repo.Capture(t.Context(), account.TenantID, account.ID, []domain.AccountDataset{"aero-vault.usage"})
	if err != nil {
		t.Fatal(err)
	}
	intent := &domain.CommandIntent{ID: idgen.NewID(), SchemaVersion: 1, AccountID: account.ID, Version: 1, Status: "prepared",
		Principal:      domain.CommandPrincipal{TenantID: account.TenantID, ActorUID: subject, Issuer: "https://snaplink.example", Subject: subject, ClientID: "console"},
		IdempotencyKey: subject, RequestFingerprint: "v1:" + strings.Repeat("a", 64), ConfirmationDigest: "v1:" + strings.Repeat("b", 64),
		Plan: domain.CommandPlan{Kind: "export", Datasets: []domain.AccountDataset{"aero-vault.usage"}}, Preconditions: pre,
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute), AuthorizationVersion: "unsupported", ResourceAuthorization: "unknown"}
	if err := repo.CreateWithAudit(t.Context(), intent, intentFact(intent, intent.ID, "command.intent.prepared")); err != nil {
		t.Fatal(err)
	}
	return repo, intent
}
func intentExecution(intent *domain.CommandIntent) repository.CommandIntentExecution {
	job := &domain.SyncJob{ID: idgen.NewID(), AccountID: intent.AccountID, TenantID: intent.Principal.TenantID, ActorUID: intent.Principal.ActorUID,
		JobType: domain.SyncJobExport, Status: domain.SyncJobPending, IdempotencyKey: "command-intent:" + intent.ID.String(),
		RequestFingerprint: "v1:" + strings.Repeat("a", 64), Request: map[string]any{"datasets": intent.Plan.Datasets}}
	return repository.CommandIntentExecution{Intent: intent, ExpectedVersion: 1, ConfirmationDigest: intent.ConfirmationDigest, Job: job,
		RequestedFact: intentFact(intent, job.ID, "account.export.requested"), ExecutedFact: intentFact(intent, idgen.NewID(), "command.intent.executed")}
}

func TestCommandIntentPostgresAtomicReceiptAndFences(t *testing.T) {
	db, _, _, _ := newEventContentHarness(t)
	if err := migrate.VerifyCommandIntents(db); err != nil {
		t.Fatal(err)
	}
	t.Run("schema watermark alone cannot hide nullable metadata", func(t *testing.T) {
		if err := db.Exec("ALTER TABLE command_intents ALTER COLUMN document DROP NOT NULL").Error; err != nil {
			t.Fatal(err)
		}
		if err := migrate.VerifyCommandIntents(db); err == nil {
			t.Fatal("incompatible metadata accepted")
		}
		if err := db.Exec("ALTER TABLE command_intents ALTER COLUMN document SET NOT NULL").Error; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("same confirmation concurrent retries share one audited receipt", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "concurrent-intent")
		results := make(chan *domain.CommandIntent, 2)
		failures := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				result, err := repo.Execute(t.Context(), intentExecution(intent))
				results <- result
				failures <- err
			})
		}
		wg.Wait()
		close(results)
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		var receipt domain.ID
		for result := range results {
			if receipt == (domain.ID{}) {
				receipt = result.ReceiptID
			}
			if result.ReceiptID != receipt {
				t.Fatal("duplicate receipt")
			}
		}
		var count int64
		db.Table("sync_jobs").Where("account_id = ?", intent.AccountID).Count(&count)
		if count != 1 {
			t.Fatalf("jobs = %d", count)
		}
		db.Table("audit_ledger").Where("account_id = ? AND action = ?", intent.AccountID, "command.intent.executed").Count(&count)
		if count != 1 {
			t.Fatalf("execution facts = %d", count)
		}
		foreign := intent.Principal
		foreign.ClientID = "other-client"
		if _, err := repo.Get(t.Context(), foreign, intent.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("foreign client = %v", err)
		}
	})
	t.Run("erase commits before execution", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "erased-intent")
		if err := db.Exec("UPDATE accounts SET status = 'erased' WHERE id = ?", intent.AccountID).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Execute(t.Context(), intentExecution(intent)); !errors.Is(err, repository.ErrAccountErased) {
			t.Fatalf("erase fence = %v", err)
		}
	})
	t.Run("expiry checked after account lock wait", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "expired-intent")
		if err := db.Exec("UPDATE command_intents SET expires_at = clock_timestamp() + interval '100 milliseconds' WHERE id = ?", intent.ID).Error; err != nil {
			t.Fatal(err)
		}
		tx := db.Begin()
		defer tx.Rollback()
		if err := tx.Exec("UPDATE accounts SET updated_at=updated_at WHERE id = ?", intent.AccountID).Error; err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := repo.Execute(context.Background(), intentExecution(intent)); done <- err }()
		time.Sleep(150 * time.Millisecond)
		if err := tx.Commit().Error; err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, repository.ErrCommandIntentExpired) {
			t.Fatalf("late expiry = %v", err)
		}
	})
	t.Run("changed metadata refuses receipt", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "stale-intent")
		if err := db.Exec("UPDATE accounts SET status = 'suspended' WHERE id = ?", intent.AccountID).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Execute(t.Context(), intentExecution(intent)); !errors.Is(err, repository.ErrCommandIntentStale) {
			t.Fatalf("stale fence = %v", err)
		}
	})
	t.Run("regional epoch change fences a prepared preview", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "epoch-intent")
		if err := db.Exec("UPDATE region_epochs SET epoch = epoch + 1 WHERE region = 'local'").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Execute(t.Context(), intentExecution(intent)); !errors.Is(err, repository.ErrCommandIntentStale) {
			t.Fatalf("epoch fence = %v", err)
		}
	})
	t.Run("audit failure rolls back receipt and intent", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "audit-failed-intent")
		if err := db.Exec(`CREATE FUNCTION refuse_intent_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.action = 'command.intent.executed' THEN RAISE EXCEPTION 'refuse intent audit'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER refuse_intent_audit BEFORE INSERT ON audit_ledger FOR EACH ROW EXECUTE FUNCTION refuse_intent_audit()`).Error; err != nil {
			t.Fatal(err)
		}
		defer db.Exec("DROP TRIGGER refuse_intent_audit ON audit_ledger; DROP FUNCTION refuse_intent_audit()")
		if _, err := repo.Execute(t.Context(), intentExecution(intent)); err == nil {
			t.Fatal("audit failure accepted")
		}
		fresh, err := repo.Get(t.Context(), intent.Principal, intent.ID)
		if err != nil || fresh.Status != "prepared" {
			t.Fatal("intent consumed despite failed audit", err)
		}
		var count int64
		db.Table("sync_jobs").Where("account_id = ?", intent.AccountID).Count(&count)
		if count != 0 {
			t.Fatal("receipt survived rollback")
		}
	})
}
