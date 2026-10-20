//go:build integration

package repository_test

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/pkg/migrate"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"gorm.io/gorm"
)

func intentAbandonment(intent *domain.CommandIntent) repository.CommandIntentAbandonment {
	return repository.CommandIntentAbandonment{Intent: intent, ExpectedVersion: 1, ConfirmationDigest: intent.ConfirmationDigest,
		Region: "local", Fact: intentFact(intent, idgen.NewID(), "command.intent.abandoned")}
}

func assertIntentCounts(t *testing.T, db *gorm.DB, intent *domain.CommandIntent, jobs, withdrawals int64) {
	t.Helper()
	var count int64
	if err := db.Table("sync_jobs").Where("account_id = ?", intent.AccountID).Count(&count).Error; err != nil || count != jobs {
		t.Fatalf("jobs=%d want=%d err=%v", count, jobs, err)
	}
	if err := db.Table("audit_ledger").Where("account_id = ? AND action = 'command.intent.abandoned'", intent.AccountID).Count(&count).Error; err != nil || count != withdrawals {
		t.Fatalf("withdrawals=%d want=%d err=%v", count, withdrawals, err)
	}
}

func TestCommandIntentPostgresDispositionAndLateExecution(t *testing.T) {
	db, _, _, _ := newEventContentHarness(t)
	if err := migrate.VerifyCommandIntents(db); err != nil {
		var definition string
		db.Raw("SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'command_intents_state_check'").Scan(&definition)
		t.Fatalf("schema verification=%v actual CHECK=%s", err, definition)
	}
	t.Run("withdrawal is permanent audited proof not expiry renewal", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "withdraw-first")
		original, err := repo.Get(t.Context(), intent.Principal, intent.ID)
		if err != nil {
			t.Fatal(err)
		}
		closed, err := repo.Abandon(t.Context(), intentAbandonment(intent))
		if err != nil || closed.Status != "abandoned" || closed.Version != 2 || closed.AbandonedAt == nil || closed.ReceiptID != (domain.ID{}) {
			t.Fatalf("closed=%+v err=%v", closed, err)
		}
		if !closed.ExpiresAt.Equal(original.ExpiresAt) || closed.ConfirmationDigest != original.ConfirmationDigest || closed.IdempotencyKey != original.IdempotencyKey {
			t.Fatal("withdrawal replaced the key, plan binding or TTL")
		}
		retry, err := repo.Abandon(t.Context(), intentAbandonment(intent))
		if err != nil || !retry.AbandonedAt.Equal(*closed.AbandonedAt) {
			t.Fatal("closure changed on retry", err)
		}
		for _, version := range []int64{1, 2} {
			late := intentExecution(intent)
			late.ExpectedVersion = version
			if _, err := repo.Execute(t.Context(), late); !errors.Is(err, repository.ErrCommandIntentAbandoned) {
				t.Fatalf("late execution=%v", err)
			}
		}
		if err := db.Exec("UPDATE command_intents SET status='executed', version=3, receipt_kind='sync_job', receipt_id=? WHERE id=?", idgen.NewID(), intent.ID).Error; err == nil {
			t.Fatal("old writer changed a terminal intent")
		}
		if err := db.Exec("UPDATE command_intents SET status='prepared', version=1, abandoned_at=NULL WHERE id=?", intent.ID).Error; err == nil {
			t.Fatal("closed intent was reopened")
		}
		assertIntentCounts(t, db, intent, 0, 1)
	})
	t.Run("execute wins and withdrawal returns original receipt", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "receipt-first")
		executed, err := repo.Execute(t.Context(), intentExecution(intent))
		if err != nil || executed.Version != 2 {
			t.Fatalf("execution=%+v err=%v", executed, err)
		}
		result, err := repo.Abandon(t.Context(), intentAbandonment(intent))
		if err != nil || result.Status != "executed" || result.ReceiptID != executed.ReceiptID || result.AbandonedAt != nil {
			t.Fatal("withdrawal canceled/replaced an existing receipt", err)
		}
		assertIntentCounts(t, db, intent, 1, 0)
	})
	t.Run("concurrent execute and withdrawal have exactly one terminal outcome", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "competing-commands")
		failures := make(chan error, 16)
		var workers sync.WaitGroup
		for range 8 {
			workers.Go(func() { _, err := repo.Execute(t.Context(), intentExecution(intent)); failures <- err })
			workers.Go(func() { _, err := repo.Abandon(t.Context(), intentAbandonment(intent)); failures <- err })
		}
		workers.Wait()
		close(failures)
		for err := range failures {
			if err != nil && !errors.Is(err, repository.ErrCommandIntentAbandoned) {
				t.Fatal(err)
			}
		}
		fresh, err := repo.Get(t.Context(), intent.Principal, intent.ID)
		if err != nil || fresh.Version != 2 {
			t.Fatal("missing terminal outcome", err)
		}
		if fresh.Status == "abandoned" {
			assertIntentCounts(t, db, intent, 0, 1)
		} else {
			assertIntentCounts(t, db, intent, 1, 0)
		}
	})
	t.Run("audit failure rolls withdrawal back before late execute", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "withdraw-audit-failed")
		if err := db.Exec(`CREATE FUNCTION refuse_abandon_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.action='command.intent.abandoned' THEN RAISE EXCEPTION 'refuse audit'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER refuse_abandon_audit BEFORE INSERT ON audit_ledger FOR EACH ROW EXECUTE FUNCTION refuse_abandon_audit()`).Error; err != nil {
			t.Fatal(err)
		}
		defer db.Exec("DROP TRIGGER refuse_abandon_audit ON audit_ledger; DROP FUNCTION refuse_abandon_audit()")
		if _, err := repo.Abandon(t.Context(), intentAbandonment(intent)); err == nil {
			t.Fatal("audit failure was ignored")
		}
		fresh, err := repo.Get(t.Context(), intent.Principal, intent.ID)
		if err != nil || fresh.Status != "prepared" || fresh.Version != 1 || fresh.AbandonedAt != nil {
			t.Fatal("failed audit left closure", err)
		}
		if _, err := repo.Execute(t.Context(), intentExecution(intent)); err != nil {
			t.Fatal(err)
		}
		assertIntentCounts(t, db, intent, 1, 0)
	})
	t.Run("expired stale erased preview may close without content capture", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "obsolete-preview")
		if err := db.Exec("UPDATE command_intents SET created_at=clock_timestamp()-interval '10 minutes', expires_at=clock_timestamp()-interval '5 minutes' WHERE id=?", intent.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("UPDATE accounts SET status='erased' WHERE id=?", intent.AccountID).Error; err != nil {
			t.Fatal(err)
		}
		closed, err := repo.Abandon(t.Context(), intentAbandonment(intent))
		if err != nil || closed.Status != "abandoned" {
			t.Fatal("obsolete preview could not be withdrawn", err)
		}
		assertIntentCounts(t, db, intent, 0, 1)
	})
	t.Run("caller tenant issuer subject client and actor remain isolated", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "isolated-withdrawal")
		for _, field := range []string{"tenant", "issuer", "subject", "client", "actor"} {
			other := *intent
			switch field {
			case "tenant":
				other.Principal.TenantID = "foreign"
			case "issuer":
				other.Principal.Issuer = "https://foreign.example"
			case "subject":
				other.Principal.Subject = "foreign"
			case "client":
				other.Principal.ClientID = "foreign"
			case "actor":
				other.Principal.ActorUID = "foreign"
			}
			if _, err := repo.Abandon(t.Context(), intentAbandonment(&other)); err == nil {
				t.Fatal("foreign caller closed intent", field)
			}
			if _, err := repo.FindByKey(t.Context(), other.Principal, intent.IdempotencyKey); err == nil {
				t.Fatal("foreign key lookup", field)
			}
		}
		assertIntentCounts(t, db, intent, 0, 0)
	})
	t.Run("current region and epoch fence withdrawal not obsolete preview metadata", func(t *testing.T) {
		repo, intent := intentPostgresFixture(t, db, "withdraw-writer-fence")
		wrong := intentAbandonment(intent)
		wrong.Region = "foreign"
		if _, err := repo.Abandon(t.Context(), wrong); !errors.Is(err, repository.ErrCommandIntentDispositionFenced) {
			t.Fatalf("writer=%v", err)
		}
		tx := db.Begin()
		defer tx.Rollback()
		if err := tx.Exec("UPDATE region_epochs SET epoch=epoch+1 WHERE region='local'").Error; err != nil {
			t.Fatal(err)
		}
		// Use a repository on the transaction to exercise actual locked metadata.
		ledger := repository.NewAuditLedgerRepository(tx, repository.AuditLedgerConfig{SigningKey: "intent-test-key"})
		if _, err := repository.NewCommandIntentRepository(tx, ledger).Abandon(t.Context(), intentAbandonment(intent)); !errors.Is(err, repository.ErrCommandIntentDispositionFenced) {
			t.Fatalf("epoch=%v", err)
		}
	})
	t.Run("schema watermark cannot hide disabled guard or weakened state", func(t *testing.T) {
		for _, statement := range []string{
			"ALTER TABLE command_intents DISABLE TRIGGER command_intents_terminal_guard",
			"ALTER TABLE command_intents DROP CONSTRAINT command_intents_state_check",
			"ALTER TABLE command_intents DROP COLUMN abandoned_at CASCADE",
		} {
			tx := db.Begin()
			if err := tx.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
			if err := migrate.VerifyCommandIntents(tx); err == nil {
				t.Fatal("forged compatible schema accepted", statement)
			}
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("down refuses to discard disposition proof even after expiry", func(t *testing.T) {
		// Pin the 000073 refusal, not the newest additive migration's Down.
		m := migrate.New(db, testpg.RepoRoot(t)+"/migrations", nil)
		version, err := m.CurrentVersion()
		if err != nil {
			t.Fatal(err)
		}
		if version > 73 {
			count, err := strconv.Atoi(strconv.FormatUint(version-73, 10))
			if err != nil {
				t.Fatalf("disposition fixture rollback count exceeds integer budget: %v", err)
			}
			if _, err := m.Down(count); err != nil {
				t.Fatalf("prepare disposition-version fixture: %v", err)
			}
		}
		_, err = m.Down(1)
		if err == nil || !strings.Contains(err.Error(), "abandoned intents") {
			t.Fatalf("down=%v", err)
		}
		current, err := m.CurrentVersion()
		if err != nil || current != 73 {
			t.Fatal("failed down mutated disposition watermark", current, err)
		}
		// Current startup also requires the newer callback CAS guard. Restore
		// additive migrations before verifying this release's complete schema.
		if _, err := m.Up(); err != nil {
			t.Fatal(err)
		}
		if err := migrate.VerifyCommandIntents(db); err != nil {
			t.Fatal("failed down mutated schema", err)
		}
	})
}
