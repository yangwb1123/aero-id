package service

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

func TestCallbackSendGateInvalidClaimNeverQueriesDatabase(t *testing.T) {
	// The nil database is deliberate: malformed claims must fail before SQL.
	ledger := repository.NewAuditLedgerRepository(nil, repository.AuditLedgerConfig{SigningKey: "unit-send-gate-key"})
	checker := repository.NewCallbackRepository(nil, ledger).(repository.CallbackDeliverySendRepository)
	for _, test := range []struct {
		name   string
		mutate func(*domain.CallbackDelivery)
	}{
		{"pending", func(d *domain.CallbackDelivery) { d.Status = "pending" }},
		{"foreign worker", func(d *domain.CallbackDelivery) { d.LeaseOwner = "another-worker" }},
		{"missing generation", func(d *domain.CallbackDelivery) { d.LeaseGeneration = 0 }},
		{"negative generation", func(d *domain.CallbackDelivery) { d.LeaseGeneration = -1 }},
		{"missing deadline", func(d *domain.CallbackDelivery) { d.LeaseUntil = nil }},
		{"missing delivery", func(d *domain.CallbackDelivery) { d.ID = domain.ID{} }},
		{"missing subscription", func(d *domain.CallbackDelivery) { d.SubscriptionID = domain.ID{} }},
		{"missing event", func(d *domain.CallbackDelivery) { d.EventID = domain.ID{} }},
		{"missing owner", func(d *domain.CallbackDelivery) { d.OwnerUID = "" }},
		{"missing tenant", func(d *domain.CallbackDelivery) { d.TenantID = "" }},
		{"missing action", func(d *domain.CallbackDelivery) { d.Action = "" }},
		{"negative attempt", func(d *domain.CallbackDelivery) { d.Attempt = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			delivery := callbackDelivery(callbackTestEndpoint, 7)
			until := time.Now().Add(time.Minute)
			delivery.Status, delivery.LeaseOwner, delivery.LeaseUntil, delivery.LeaseGeneration = "delivering", "worker", &until, 1
			test.mutate(delivery)
			if err := checker.CheckDeliverySend(t.Context(), delivery, "worker"); !errors.Is(err, repository.ErrCallbackDeliverySendIneligible) {
				t.Fatalf("invalid claim admission = %v", err)
			}
		})
	}
	if err := checker.CheckDeliverySend(t.Context(), nil, "worker"); !errors.Is(err, repository.ErrCallbackDeliverySendIneligible) {
		t.Fatalf("nil claim admission = %v", err)
	}
}

func TestCallbackSendGateCanceledCheckDoesNotCallPort(t *testing.T) {
	delivery := callbackDelivery(callbackTestEndpoint, 7)
	repo := &callbackSendProbeRepo{callbackFakeRepo: newCallbackFakeRepo(delivery)}
	svc := newCallbackTestService(repo, CallbackConfig{AllowPrivateTarget: true}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := svc.deliver(ctx, delivery); !errors.Is(err, errCallbackSendCheckFailed) || repo.count() != 0 {
		t.Fatalf("canceled check called the port: error=%v calls=%d", err, repo.count())
	}
	if _, _, err := svc.deliver(t.Context(), nil); !errors.Is(err, errCallbackSendIneligible) {
		t.Fatalf("nil delivery admission = %v", err)
	}
}

func TestCallbackSendGateAdmittedTimeoutStillConsumesRemoteAttempt(t *testing.T) {
	delivery := callbackDelivery(callbackTestEndpoint, 7)
	repo := newCallbackFakeRepo(delivery)
	transport := &callbackAdmittedTimeoutTransport{}
	svc := newCallbackTestService(repo, CallbackConfig{AllowPrivateTarget: true, RequestTimeout: 20 * time.Millisecond, MaxAttempts: 9}, transport)
	metrics := &metricSpy{}
	svc.metrics = metrics
	svc.drain(t.Context())
	if transport.calls.Load() != 1 || len(repo.marked) != 1 || repo.marked[0].status != "pending" {
		t.Fatalf("admitted timeout was mislabeled as pre-send refusal: calls=%d marks=%v", transport.calls.Load(), repo.marked)
	}
	if len(metrics.callbackOutcomes) != 1 || metrics.callbackOutcomes[0] != "pending" {
		t.Fatalf("admitted timeout observations = %v", metrics.callbackOutcomes)
	}
}

type callbackAdmittedTimeoutTransport struct{ calls atomic.Int32 }

func (t *callbackAdmittedTimeoutTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	<-request.Context().Done()
	return nil, request.Context().Err()
}
