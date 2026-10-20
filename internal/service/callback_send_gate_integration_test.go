//go:build integration

package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func seedCallbackSendClaim(t *testing.T, db *gorm.DB, svc *callbackService, repo repository.CallbackRepository) *domain.CallbackDelivery {
	t.Helper()
	ciphertext, err := svc.encrypt([]byte("unchanged-original-subscription-secret"))
	if err != nil {
		t.Fatal(err)
	}
	sub := &domain.CallbackSubscription{ID: idgen.NewID(), OwnerUID: "u-1", TenantID: "tenant-a",
		Endpoint: callbackTestEndpoint, EventTypes: []string{"account.updated"}, SecretCiphertext: ciphertext, Enabled: true, Verified: true}
	if err := repo.CreateSubscription(t.Context(), sub); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := db.WithContext(ctx).Exec("DELETE FROM callback_subscriptions WHERE id = ?", sub.ID).Error; err != nil {
			t.Errorf("delete send-gate fixture subscription: %v", err)
		}
	})
	eventID := idgen.NewID()
	delivery := &domain.CallbackDelivery{ID: idgen.NewID(), SubscriptionID: sub.ID, EventID: eventID, Action: "account.updated",
		Attempt: 7, Payload: map[string]any{"schema_version": 1, "event_id": eventID.String(), "content": "must-not-select-in-gate"}}
	if err := repo.EnqueueDelivery(t.Context(), delivery); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.FetchDueDeliveries(t.Context(), 1, svc.worker, 5*time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != delivery.ID {
		t.Fatalf("send-gate fixture claim = %v, error=%v", claimed, err)
	}
	return claimed[0]
}

func TestCallbackSendAdmissionPostgresChecksExactLeaseAndCurrentParent(t *testing.T) {
	db, service, repo := startCallbackService(t, "integration-callback-send-gate-ledger-key")
	svc := service.(*callbackService)
	checker := repo.(repository.CallbackDeliverySendRepository)
	for _, test := range []struct {
		name  string
		query string
	}{
		{"live", ""},
		{"expired", "UPDATE callback_deliveries SET lease_until = clock_timestamp() - INTERVAL '1 second' WHERE id = ?"},
		{"same worker new generation", "UPDATE callback_deliveries SET lease_generation = lease_generation + 1 WHERE id = ?"},
		{"different worker", "UPDATE callback_deliveries SET lease_owner = 'another-worker' WHERE id = ?"},
		{"changed deadline", "UPDATE callback_deliveries SET lease_until = lease_until + INTERVAL '1 minute' WHERE id = ?"},
		{"reset pending", "UPDATE callback_deliveries SET status = 'pending', lease_owner = '', lease_until = NULL WHERE id = ?"},
		{"changed event", "UPDATE callback_deliveries SET event_id = gen_random_uuid() WHERE id = ?"},
		{"changed action", "UPDATE callback_deliveries SET action = 'account.erased' WHERE id = ?"},
		{"changed attempt", "UPDATE callback_deliveries SET attempt = attempt + 1 WHERE id = ?"},
		{"disabled", "UPDATE callback_subscriptions SET enabled = false WHERE id = (SELECT subscription_id FROM callback_deliveries WHERE id = ?)"},
		{"unverified", "UPDATE callback_subscriptions SET verified = false WHERE id = (SELECT subscription_id FROM callback_deliveries WHERE id = ?)"},
		{"owner changed", "UPDATE callback_subscriptions SET owner_uid = 'other-owner' WHERE id = (SELECT subscription_id FROM callback_deliveries WHERE id = ?)"},
		{"tenant changed", "UPDATE callback_subscriptions SET tenant_id = 'tenant-b' WHERE id = (SELECT subscription_id FROM callback_deliveries WHERE id = ?)"},
		{"deleted parent", "DELETE FROM callback_subscriptions WHERE id = (SELECT subscription_id FROM callback_deliveries WHERE id = ?)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			delivery := seedCallbackSendClaim(t, db, svc, repo)
			if test.query != "" {
				if err := db.Exec(test.query, delivery.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			err := checker.CheckDeliverySend(t.Context(), delivery, svc.worker)
			if test.query == "" {
				if err != nil {
					t.Fatalf("live exact claim refused: %v", err)
				}
			} else if !errors.Is(err, repository.ErrCallbackDeliverySendIneligible) {
				t.Fatalf("changed claim/parent admission = %v", err)
			}
		})
	}
	t.Run("metadata only", func(t *testing.T) {
		delivery := seedCallbackSendClaim(t, db, svc, repo)
		recorder := &callbackSendSQLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
		ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-callback-send-gate-ledger-key"})
		recordedRepo := repository.NewCallbackRepository(db.Session(&gorm.Session{Logger: recorder}), ledger)
		if err := recordedRepo.(repository.CallbackDeliverySendRepository).CheckDeliverySend(t.Context(), delivery, svc.worker); err != nil {
			t.Fatal(err)
		}
		queries := recorder.snapshot()
		if len(queries) != 1 || !strings.Contains(strings.ToLower(queries[0]), "select exists") {
			t.Fatalf("send gate query is not a single metadata existence check: %v", queries)
		}
		for _, forbidden := range []string{"payload", "response_body", "secret_ciphertext", "endpoint", "select *", delivery.SecretCiphertext} {
			if strings.Contains(strings.ToLower(queries[0]), strings.ToLower(forbidden)) {
				t.Fatalf("send gate touched sensitive content column/value: %q", forbidden)
			}
		}
		row, err := repo.GetDelivery(t.Context(), delivery.ID)
		if err != nil || row.Attempt != delivery.Attempt || row.LeaseGeneration != delivery.LeaseGeneration || row.Status != "delivering" {
			t.Fatalf("read-only admission mutated claim: row=%+v error=%v", row, err)
		}
	})
}

type callbackSendSQLRecorder struct {
	logger.Interface
	mu      sync.Mutex
	queries []string
}

func (r *callbackSendSQLRecorder) Trace(_ context.Context, _ time.Time, query func() (string, int64), _ error) {
	sql, _ := query()
	r.mu.Lock()
	r.queries = append(r.queries, sql)
	r.mu.Unlock()
}

func (r *callbackSendSQLRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.queries...)
}

type callbackSendChangingRepo struct {
	repository.CallbackRepository
	checker repository.CallbackDeliverySendRepository
	calls   int
	after   func() error
}

func (r *callbackSendChangingRepo) CheckDeliverySend(ctx context.Context, d *domain.CallbackDelivery, worker string) error {
	r.calls++
	if err := r.checker.CheckDeliverySend(ctx, d, worker); err != nil {
		return err
	}
	if r.calls == 1 {
		return r.after()
	}
	return nil
}

func TestCallbackSendAdmissionPostgresRechecksImmediatelyBeforeTransport(t *testing.T) {
	db, service, repo := startCallbackService(t, "integration-callback-transport-gate-ledger-key")
	svc := service.(*callbackService)
	svc.cfg.AllowPrivateTarget = true
	delivery := seedCallbackSendClaim(t, db, svc, repo)
	changing := &callbackSendChangingRepo{CallbackRepository: repo, checker: repo.(repository.CallbackDeliverySendRepository),
		after: func() error {
			return db.Exec("UPDATE callback_subscriptions SET enabled = false WHERE id = ?", delivery.SubscriptionID).Error
		}}
	svc.repo = changing
	transport := &cannedTransport{status: http.StatusAccepted}
	svc.roundTripper = transport
	metrics := &metricSpy{}
	svc.metrics = metrics
	svc.markDelivery(t.Context(), delivery)
	calls, _ := transport.snapshot()
	row, err := repo.GetDelivery(t.Context(), delivery.ID)
	if err != nil || calls != 0 || changing.calls != 2 || row.Attempt != 7 || row.Status != "delivering" {
		t.Fatalf("disabled-between-checks send: calls=%d checks=%d row=%+v error=%v", calls, changing.calls, row, err)
	}
	if len(metrics.callbackOutcomes) != 1 || metrics.callbackOutcomes[0] != "send_ineligible" {
		t.Fatalf("disabled-between-checks outcome = %v", metrics.callbackOutcomes)
	}
}

type callbackAdmittedTransport struct {
	db       *gorm.DB
	delivery domain.ID
	calls    int
}

func (t *callbackAdmittedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls++
	if err := t.db.Exec("UPDATE callback_deliveries SET lease_generation = lease_generation + 1 WHERE id = ?", t.delivery).Error; err != nil {
		return nil, err
	}
	return (&cannedTransport{status: http.StatusAccepted}).RoundTrip(request)
}

func TestCallbackSendAdmissionPostgresCannotRecallAnAdmittedCall(t *testing.T) {
	db, service, repo := startCallbackService(t, "integration-callback-admitted-gate-ledger-key")
	svc := service.(*callbackService)
	svc.cfg.AllowPrivateTarget = true
	delivery := seedCallbackSendClaim(t, db, svc, repo)
	transport := &callbackAdmittedTransport{db: db, delivery: delivery.ID}
	svc.roundTripper = transport
	metrics := &metricSpy{}
	svc.metrics = metrics
	svc.markDelivery(t.Context(), delivery)
	row, err := repo.GetDelivery(t.Context(), delivery.ID)
	if err != nil || transport.calls != 1 || row.Status != "delivering" || row.Attempt != 7 || row.EventID != delivery.EventID {
		t.Fatalf("admitted call was mislabeled as canceled/settled: calls=%d row=%+v error=%v", transport.calls, row, err)
	}
	if len(metrics.callbackOutcomes) != 1 || metrics.callbackOutcomes[0] != "mark_failed" {
		t.Fatalf("admitted lease-lost outcome = %v", metrics.callbackOutcomes)
	}
}
