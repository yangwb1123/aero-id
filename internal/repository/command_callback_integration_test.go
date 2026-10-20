//go:build integration

package repository_test

import (
	"context"
	"errors"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/pkg/migrate"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
	"strings"
	"sync"
	"testing"
	"time"
)

func callbackIntentFixture(t *testing.T, db *gorm.DB, subject string) (repository.CommandIntentRepository, *domain.CommandIntent) {
	t.Helper()
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "callback-intent-test-key"})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "callback-tenant", "https://snaplink.example", subject, subject, "local")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.NewRegionDirectoryRepository(db, ledger).EnsureDirectory(t.Context(), account.TenantID, account.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	callbacks := repository.NewCallbackRepository(db, ledger)
	sub := &domain.CallbackSubscription{ID: idgen.NewID(), OwnerUID: subject, TenantID: account.TenantID, Endpoint: "https://example.test/do-not-read", SecretCiphertext: "do-not-read", EventTypes: []string{"test.event"}, Enabled: true, Verified: true}
	if err := callbacks.CreateSubscription(t.Context(), sub); err != nil {
		t.Fatal(err)
	}
	delivery := &domain.CallbackDelivery{ID: idgen.NewID(), SubscriptionID: sub.ID, EventID: idgen.NewID(), Action: "test.event", Attempt: 8, Payload: map[string]any{"secret": "do-not-read"}}
	if err := callbacks.EnqueueDelivery(t.Context(), delivery); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE callback_deliveries SET status='dead_letter', response_body='preserve-original-diagnostic' WHERE id=?", delivery.ID).Error; err != nil {
		t.Fatal(err)
	}
	intent := &domain.CommandIntent{ID: idgen.NewID(), SchemaVersion: 1, AccountID: account.ID, Version: 1, Status: "prepared", AuthorizationVersion: "unsupported", ResourceAuthorization: "unknown", Destructive: true,
		Principal:      domain.CommandPrincipal{ActorUID: subject, TenantID: account.TenantID, Issuer: "https://snaplink.example", Subject: subject, ClientID: "console"},
		IdempotencyKey: subject, RequestFingerprint: "v1:" + strings.Repeat("a", 64), ConfirmationDigest: "v1:" + strings.Repeat("b", 64),
		Plan:      domain.CommandPlan{Kind: "callback_replay", Datasets: []domain.AccountDataset{}, Callback: &domain.CommandCallbackTarget{SubscriptionID: sub.ID, DeliveryID: delivery.ID, EventID: delivery.EventID, Revision: 2}},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute)}
	repo := repository.NewCommandIntentRepository(db, ledger)
	intent.Preconditions, err = repo.(repository.CommandCallbackCaptureRepository).CaptureCallbackReplay(t.Context(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateWithAudit(t.Context(), intent, intentFact(intent, intent.ID, "command.intent.prepared")); err != nil {
		t.Fatal(err)
	}
	return repo, intent
}

func callbackIntentExecution(intent *domain.CommandIntent) repository.CommandIntentExecution {
	fact := intentFact(intent, idgen.NewID(), "callback.delivery.replayed")
	fact.EventID = fact.ID
	fact.ResourceType = "callback_delivery"
	fact.ResourceID = intent.Plan.Callback.DeliveryID.String()
	return repository.CommandIntentExecution{Intent: intent, ExpectedVersion: 1, ConfirmationDigest: intent.ConfirmationDigest, RequestedFact: fact, ExecutedFact: intentFact(intent, idgen.NewID(), "command.intent.executed")}
}

func TestCallbackCommandIntentPostgresCASAndDurableQueueReceipt(t *testing.T) {
	db, _, _, _ := newEventContentHarness(t)
	if err := migrate.VerifyCommandIntents(db); err != nil {
		t.Fatal(err)
	}
	t.Run("one concurrent confirmation and replay after worker change or parent deletion", func(t *testing.T) {
		repo, intent := callbackIntentFixture(t, db, "concurrent-callback")
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				result, err := repo.Execute(t.Context(), callbackIntentExecution(intent))
				if err != nil || result == nil || result.ReceiptID != intent.ID || result.ReceiptKind != "callback_replay" {
					t.Errorf("receipt=%+v err=%v", result, err)
				}
			})
		}
		wg.Wait()
		var row struct {
			Status                    string
			Attempt                   int
			Revision, LeaseGeneration int64
			ResponseBody              string
			EventID                   domain.ID
		}
		db.Table("callback_deliveries").Where("id=?", intent.Plan.Callback.DeliveryID).Take(&row)
		if row.Status != "pending" || row.Attempt != 8 || row.Revision != 3 || row.LeaseGeneration != 1 || row.EventID != intent.Plan.Callback.EventID || row.ResponseBody != "preserve-original-diagnostic" {
			t.Fatalf("queue mutated original evidence: %+v", row)
		}
		var count int64
		db.Table("audit_ledger").Where("account_id=? AND action='callback.delivery.replayed'", intent.AccountID).Count(&count)
		if count != 1 {
			t.Fatalf("replay facts=%d", count)
		}
		db.Exec("UPDATE callback_deliveries SET status='success' WHERE id=?", intent.Plan.Callback.DeliveryID)
		db.Exec("DELETE FROM callback_subscriptions WHERE id=?", intent.Plan.Callback.SubscriptionID)
		result, err := repo.Execute(t.Context(), callbackIntentExecution(intent))
		if err != nil || result.ReceiptID != intent.ID {
			t.Fatal("original receipt lost after delete", err)
		}
		found, err := repo.FindByKey(t.Context(), intent.Principal, intent.IdempotencyKey)
		if err != nil || found.ReceiptID != intent.ID {
			t.Fatal("original-key receipt lost", err)
		}
	})
	for _, change := range []string{"legacy-update", "pending", "success", "live-lease", "expired-lease-owner", "event-id"} {
		t.Run(change, func(t *testing.T) {
			repo, intent := callbackIntentFixture(t, db, "callback-"+change)
			sql := "UPDATE callback_deliveries SET updated_at=clock_timestamp() WHERE id=?"
			switch change {
			case "pending":
				sql = "UPDATE callback_deliveries SET status='pending' WHERE id=?"
			case "success":
				sql = "UPDATE callback_deliveries SET status='success' WHERE id=?"
			case "live-lease":
				sql = "UPDATE callback_deliveries SET lease_owner='worker',lease_until=clock_timestamp()+interval '1 minute' WHERE id=?"
			case "expired-lease-owner":
				sql = "UPDATE callback_deliveries SET lease_owner='worker',lease_until=clock_timestamp()-interval '1 minute' WHERE id=?"
			case "event-id":
				sql = "UPDATE callback_deliveries SET event_id=gen_random_uuid() WHERE id=?"
			}
			if err := db.Exec(sql, intent.Plan.Callback.DeliveryID).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := repo.Execute(t.Context(), callbackIntentExecution(intent)); !errors.Is(err, repository.ErrCommandIntentStale) {
				t.Fatal("stale callback accepted", err)
			}
			found, _ := repo.Get(t.Context(), intent.Principal, intent.ID)
			if found.Status != "prepared" {
				t.Fatal("stale intent acquired receipt")
			}
		})
	}
	t.Run("audit failure rolls back queue generation and intent", func(t *testing.T) {
		repo, intent := callbackIntentFixture(t, db, "callback-audit-failure")
		db.Exec(`CREATE FUNCTION reject_callback_replay_fact() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='callback.delivery.replayed' THEN RAISE EXCEPTION 'test ledger unavailable'; END IF; RETURN NEW; END; $$`)
		db.Exec(`CREATE TRIGGER reject_callback_replay_fact BEFORE INSERT ON audit_ledger FOR EACH ROW EXECUTE FUNCTION reject_callback_replay_fact()`)
		defer db.Exec(`DROP TRIGGER reject_callback_replay_fact ON audit_ledger; DROP FUNCTION reject_callback_replay_fact()`)
		if _, err := repo.Execute(t.Context(), callbackIntentExecution(intent)); err == nil {
			t.Fatal("failed audit accepted")
		}
		var revision int64
		db.Table("callback_deliveries").Select("revision").Where("id=?", intent.Plan.Callback.DeliveryID).Scan(&revision)
		found, _ := repo.Get(t.Context(), intent.Principal, intent.ID)
		if revision != 2 || found.Status != "prepared" {
			t.Fatal("audit failure changed authoritative state")
		}
	})
	t.Run("lock wait consumes preview TTL", func(t *testing.T) {
		repo, intent := callbackIntentFixture(t, db, "callback-lock-wait")
		db.Exec("UPDATE command_intents SET expires_at=clock_timestamp()+interval '100 milliseconds' WHERE id=?", intent.ID)
		tx := db.Begin()
		tx.Exec("SELECT id FROM callback_deliveries WHERE id=? FOR UPDATE", intent.Plan.Callback.DeliveryID)
		done := make(chan error, 1)
		go func() { _, err := repo.Execute(context.Background(), callbackIntentExecution(intent)); done <- err }()
		time.Sleep(180 * time.Millisecond)
		tx.Rollback()
		if err := <-done; !errors.Is(err, repository.ErrCommandIntentExpired) {
			t.Fatal("child wait ignored TTL", err)
		}
	})
	t.Run("schema trigger and receipt guard cannot be removed by bookkeeping", func(t *testing.T) {
		db.Exec("ALTER TABLE callback_deliveries DISABLE TRIGGER callback_delivery_revision_guard")
		if err := migrate.VerifyCommandIntents(db); err == nil {
			t.Fatal("disabled revision trigger accepted")
		}
		db.Exec("ALTER TABLE callback_deliveries ENABLE TRIGGER callback_delivery_revision_guard")
		if err := migrate.VerifyCommandIntents(db); err != nil {
			t.Fatal(err)
		}
		tx := db.Begin()
		defer tx.Rollback()
		tx.Exec("ALTER TABLE command_intents DROP CONSTRAINT command_intents_callback_receipt_check")
		tx.Exec("ALTER TABLE command_intents ADD CONSTRAINT command_intents_callback_receipt_check CHECK (receipt_kind <> 'callback_replay' OR true)")
		if err := migrate.VerifyCommandIntents(tx); err == nil {
			t.Fatal("weakened receipt guard accepted")
		}
	})
}
