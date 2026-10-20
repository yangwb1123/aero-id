//go:build integration

package auditintegration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type callbackReadSQL struct {
	logger.Interface
	mu         sync.Mutex
	statements []string
}

func (l *callbackReadSQL) Trace(_ context.Context, _ time.Time, query func() (string, int64), _ error) {
	sql, _ := query()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = append(l.statements, strings.ToLower(sql))
}

func deliveryReadFact(action string, resourceID domain.ID) *domain.AuditFact {
	fact := callbackFact(idgen.NewID(), "tenant-a", action)
	fact.ResourceID = resourceID.String()
	if action == "callback.delivery.read" {
		fact.ResourceType = "callback_delivery"
	}
	return fact
}

func TestCallbackDeliveryMetadataPostgresScopeKeysetAndAudit(t *testing.T) {
	db, base, _ := startCallbackAuditRepo(t, callbackAuditTestKey)
	ctx := t.Context()
	subs := make([]*domain.CallbackSubscription, 4)
	for i := range subs {
		subs[i] = &domain.CallbackSubscription{ID: idgen.NewID(), OwnerUID: "u-1", TenantID: "tenant-a",
			Endpoint: "https://secret.example/DO-NOT-READ-ENDPOINT", EventTypes: []string{}, SecretCiphertext: "DO-NOT-READ-CIPHERTEXT"}
	}
	subs[2].TenantID, subs[3].OwnerUID = "tenant-b", "u-2"
	for _, sub := range subs {
		if err := base.CreateSubscription(ctx, sub); err != nil {
			t.Fatal(err)
		}
	}
	ids := make([]domain.ID, 6)
	for i := range ids {
		id, err := idgen.Parse(fmt.Sprintf("01800000-0000-7000-8000-%012x", i+1))
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
		delivery := &domain.CallbackDelivery{ID: ids[i], SubscriptionID: subs[0].ID, EventID: idgen.NewID(),
			Action: "account.provisioned", Attempt: i, CreatedAt: time.Now().Add(time.Duration(6-i) * time.Hour),
			Payload: map[string]any{"secret": "DO-NOT-READ-PAYLOAD"}}
		if err := base.EnqueueDelivery(ctx, delivery); err != nil {
			t.Fatal(err)
		}
		status := "pending"
		if i%2 == 0 {
			status = "dead_letter"
		}
		if err := db.Exec("UPDATE callback_deliveries SET status = ?, response_body = ? WHERE id = ?", status, "DO-NOT-READ-RESPONSE", ids[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	trace := &callbackReadSQL{Interface: logger.Default}
	readDB := db.Session(&gorm.Session{Logger: trace})
	repo := repository.NewCallbackRepository(readDB, repository.NewAuditLedgerRepository(readDB, repository.AuditLedgerConfig{SigningKey: callbackAuditTestKey})).(repository.CallbackDeliveryReader)

	t.Run("uuid keysets ignore creation time and retain attempts", func(t *testing.T) {
		cursor := ""
		seen := []domain.ID{}
		for range 3 {
			items, next, err := repo.ListDeliveryMetadataWithAudit(ctx, "u-1", "tenant-a", subs[0].ID,
				domain.CallbackDeliveryFilter{Page: domain.Page{Limit: 2, Cursor: cursor}}, deliveryReadFact("callback.deliveries.read", subs[0].ID))
			if err != nil || len(items) != 2 {
				t.Fatalf("page = %v/%s, %v", items, next, err)
			}
			for _, item := range items {
				seen = append(seen, item.ID)
				if int(item.Attempt) != int(item.ID[15])-1 || item.TenantID != "tenant-a" {
					t.Fatalf("metadata = %+v", item)
				}
			}
			cursor = next
		}
		if cursor != "" || len(seen) != 6 {
			t.Fatalf("seen=%v next=%s", seen, cursor)
		}
		for i, id := range seen {
			if id != ids[len(ids)-1-i] {
				t.Fatal("timestamps changed descending keyset traversal", seen)
			}
		}
	})
	t.Run("status filter and owned empty subscription are audited", func(t *testing.T) {
		fact := deliveryReadFact("callback.deliveries.read", subs[0].ID)
		items, next, err := repo.ListDeliveryMetadataWithAudit(ctx, "u-1", "tenant-a", subs[0].ID,
			domain.CallbackDeliveryFilter{Status: "dead_letter"}, fact)
		if err != nil || len(items) != 3 || next != "" {
			t.Fatalf("filtered page=%v/%s %v", items, next, err)
		}
		for _, item := range items {
			if item.Status != "dead_letter" {
				t.Fatal(item.Status)
			}
		}
		ledger, publication := countCallbackAuditRows(t, db, fact.EventID)
		if ledger != 1 || publication != 1 {
			t.Fatal("read fact/publication missing", ledger, publication)
		}
		fact = deliveryReadFact("callback.deliveries.read", subs[1].ID)
		items, _, err = repo.ListDeliveryMetadataWithAudit(ctx, "u-1", "tenant-a", subs[1].ID, domain.CallbackDeliveryFilter{}, fact)
		if err != nil || items == nil || len(items) != 0 {
			t.Fatalf("empty owned subscription=%v/%v", items, err)
		}
		ledger, _ = countCallbackAuditRows(t, db, fact.EventID)
		if ledger != 1 {
			t.Fatal("empty sensitive read was unaudited")
		}
	})
	t.Run("owner tenant subscription and delivery are jointly isolated", func(t *testing.T) {
		for _, sub := range subs[2:] {
			fact := deliveryReadFact("callback.deliveries.read", sub.ID)
			items, next, err := repo.ListDeliveryMetadataWithAudit(ctx, "u-1", "tenant-a", sub.ID, domain.CallbackDeliveryFilter{}, fact)
			if !errors.Is(err, repository.ErrNotFound) || items != nil || next != "" {
				t.Fatalf("foreign scope=%v/%v", items, err)
			}
			ledger, _ := countCallbackAuditRows(t, db, fact.EventID)
			if ledger != 0 {
				t.Fatal("foreign read fact committed")
			}
		}
		fact := deliveryReadFact("callback.delivery.read", ids[0])
		item, err := repo.GetDeliveryMetadataWithAudit(ctx, "u-1", "tenant-a", subs[1].ID, ids[0], fact)
		if !errors.Is(err, repository.ErrNotFound) || item != nil {
			t.Fatal("delivery borrowed another subscription", item, err)
		}
		fact.ActorUID = "u-2"
		if _, err := repo.GetDeliveryMetadataWithAudit(ctx, "u-1", "tenant-a", subs[0].ID, ids[0], fact); err == nil {
			t.Fatal("audit principal differs")
		}
	})
	t.Run("detail is metadata only and audit failure exposes no result", func(t *testing.T) {
		fact := deliveryReadFact("callback.delivery.read", ids[0])
		item, err := repo.GetDeliveryMetadataWithAudit(ctx, "u-1", "tenant-a", subs[0].ID, ids[0], fact)
		if err != nil || item.ID != ids[0] || item.EventID == (domain.ID{}) {
			t.Fatalf("detail=%+v %v", item, err)
		}
		ledger, publication := countCallbackAuditRows(t, db, fact.EventID)
		if ledger != 1 || publication != 1 {
			t.Fatal("detail read not durably audited")
		}
		keyless := repository.NewCallbackRepository(db, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})).(repository.CallbackDeliveryReader)
		fact = deliveryReadFact("callback.delivery.read", ids[0])
		item, err = keyless.GetDeliveryMetadataWithAudit(ctx, "u-1", "tenant-a", subs[0].ID, ids[0], fact)
		if err == nil || item != nil {
			t.Fatal("failed detail audit returned data")
		}
		ledger, publication = countCallbackAuditRows(t, db, fact.EventID)
		if ledger != 0 || publication != 0 {
			t.Fatal("failed audit left rows")
		}
		items, next, err := keyless.ListDeliveryMetadataWithAudit(ctx, "u-1", "tenant-a", subs[0].ID, domain.CallbackDeliveryFilter{}, deliveryReadFact("callback.deliveries.read", subs[0].ID))
		if err == nil || items != nil || next != "" {
			t.Fatal("failed page audit returned data")
		}
	})
	t.Run("deletion wins before read and leaves no phantom read fact", func(t *testing.T) {
		tx := db.Begin()
		defer tx.Rollback()
		if err := tx.Exec("DELETE FROM callback_subscriptions WHERE id = ?", subs[1].ID).Error; err != nil {
			t.Fatal(err)
		}
		fact := deliveryReadFact("callback.deliveries.read", subs[1].ID)
		result := make(chan error, 1)
		go func() {
			_, _, err := repo.ListDeliveryMetadataWithAudit(ctx, "u-1", "tenant-a", subs[1].ID, domain.CallbackDeliveryFilter{}, fact)
			result <- err
		}()
		select {
		case err := <-result:
			t.Fatalf("read passed uncommitted deletion: %v", err)
		case <-time.After(30 * time.Millisecond):
		}
		if err := tx.Commit().Error; err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if !errors.Is(err, repository.ErrNotFound) {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("read did not resume")
		}
		ledger, _ := countCallbackAuditRows(t, db, fact.EventID)
		if ledger != 0 {
			t.Fatal("deleted subscription left read fact")
		}
	})
	t.Run("all callback SELECTs omit content credentials and worker IDs", func(t *testing.T) {
		trace.mu.Lock()
		defer trace.mu.Unlock()
		selects := 0
		for _, sql := range trace.statements {
			if !strings.HasPrefix(sql, "select ") || !(strings.Contains(sql, "from callback_deliveries as d") || strings.Contains(sql, `from "callback_subscriptions"`)) {
				continue
			}
			selects++
			for _, forbidden := range []string{"payload", "response_body", "secret_ciphertext", "endpoint", "lease_owner", "select *", "d.*"} {
				if strings.Contains(sql, forbidden) {
					t.Fatalf("callback read selected %s: %s", forbidden, sql)
				}
			}
		}
		if selects < 10 {
			t.Fatalf("metadata selection checks were vacuous: %d", selects)
		}
	})
	t.Run("new keyset indexes cover both list orders", func(t *testing.T) {
		var definitions []string
		if err := db.Raw("SELECT indexdef FROM pg_indexes WHERE indexname IN (?, ?) ORDER BY indexname",
			"idx_callback_deliveries_subscription_keyset", "idx_callback_deliveries_subscription_status_keyset").Scan(&definitions).Error; err != nil {
			t.Fatal(err)
		}
		if len(definitions) != 2 || !strings.Contains(definitions[0], "(subscription_id, id DESC)") || !strings.Contains(definitions[1], "(subscription_id, status, id DESC)") {
			t.Fatal("keyset index definitions differ", definitions)
		}
	})
}
