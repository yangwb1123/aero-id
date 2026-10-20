package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

func (f *callbackFakeRepo) CheckDeliverySend(ctx context.Context, d *domain.CallbackDelivery, worker string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, current := range f.deliveries {
		if current.ID == d.ID && current.Status == "delivering" && current.LeaseOwner == worker &&
			current.LeaseGeneration == d.LeaseGeneration && current.LeaseUntil != nil && current.LeaseUntil.After(time.Now()) {
			return nil
		}
	}
	return repository.ErrCallbackDeliverySendIneligible
}

type callbackSendProbeRepo struct {
	*callbackFakeRepo
	gateMu sync.Mutex
	calls  int
	check  func(context.Context, *domain.CallbackDelivery, int) error
}

func (r *callbackSendProbeRepo) CheckDeliverySend(ctx context.Context, d *domain.CallbackDelivery, worker string) error {
	r.gateMu.Lock()
	r.calls++
	call := r.calls
	r.gateMu.Unlock()
	if r.check != nil {
		return r.check(ctx, d, call)
	}
	return r.callbackFakeRepo.CheckDeliverySend(ctx, d, worker)
}

func (r *callbackSendProbeRepo) count() int {
	r.gateMu.Lock()
	defer r.gateMu.Unlock()
	return r.calls
}

type callbackRepoWithoutSendGate struct{ repository.CallbackRepository }
type callbackNilSendGate struct{ repository.CallbackRepository }

func (*callbackNilSendGate) CheckDeliverySend(context.Context, *domain.CallbackDelivery, string) error {
	panic("typed nil callback send checker was called")
}

func TestCallbackSendGateDeniesBeforeEndpointAndNeverSpendsAttempt(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"ineligible", repository.ErrCallbackDeliverySendIneligible, "send_ineligible"},
		{"database failure", errors.New("private database details"), "send_check_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			delivery := callbackDelivery("http://127.0.0.1/invalid-policy", 7)
			base := newCallbackFakeRepo(delivery)
			repo := &callbackSendProbeRepo{callbackFakeRepo: base, check: func(ctx context.Context, _ *domain.CallbackDelivery, _ int) error {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > callbackSendCheckBudget {
					t.Error("callback send check lacks its bounded context")
				}
				return test.err
			}}
			transport := &cannedTransport{status: http.StatusAccepted}
			svc := newCallbackTestService(repo, CallbackConfig{MaxAttempts: 8}, transport)
			metrics := &metricSpy{}
			svc.metrics = metrics
			svc.drain(t.Context())
			calls, _ := transport.snapshot()
			if calls != 0 || len(base.marked) != 0 || repo.count() != 1 || delivery.Attempt != 7 || delivery.Status != "delivering" {
				t.Fatalf("blocked send changed attempts/settled/sent: calls=%d marks=%d checks=%d row=%+v", calls, len(base.marked), repo.count(), delivery)
			}
			if len(metrics.callbackOutcomes) != 1 || metrics.callbackOutcomes[0] != test.want {
				t.Fatalf("blocked send outcome = %v", metrics.callbackOutcomes)
			}
		})
	}
}

func TestCallbackSendGateRequiresPortAndRejectsTypedNil(t *testing.T) {
	var typedNil *callbackNilSendGate
	for _, repo := range []repository.CallbackRepository{
		callbackRepoWithoutSendGate{newCallbackFakeRepo()}, typedNil,
	} {
		delivery := callbackDelivery(callbackTestEndpoint, 7)
		transport := &cannedTransport{status: http.StatusAccepted}
		svc := newCallbackTestService(repo, CallbackConfig{AllowPrivateTarget: true}, transport)
		metrics := &metricSpy{}
		svc.metrics = metrics
		svc.markDelivery(t.Context(), delivery)
		calls, _ := transport.snapshot()
		if calls != 0 || len(metrics.callbackOutcomes) != 1 || metrics.callbackOutcomes[0] != "send_check_failed" {
			t.Fatalf("unwired send checker did not fail closed: calls=%d metrics=%v", calls, metrics.callbackOutcomes)
		}
	}
}

func TestCallbackSendGateRechecksAtTransportAndRejectsLateSuccess(t *testing.T) {
	for _, late := range []bool{false, true} {
		delivery := callbackDelivery(callbackTestEndpoint, 7)
		base := newCallbackFakeRepo(delivery)
		repo := &callbackSendProbeRepo{callbackFakeRepo: base, check: func(ctx context.Context, _ *domain.CallbackDelivery, call int) error {
			if call == 1 {
				return nil
			}
			if late {
				<-ctx.Done()
				return nil // Ignoring the deadline must not authorize a late send.
			}
			return repository.ErrCallbackDeliverySendIneligible
		}}
		transport := &cannedTransport{status: http.StatusAccepted}
		svc := newCallbackTestService(repo, CallbackConfig{AllowPrivateTarget: true, RequestTimeout: 20 * time.Millisecond}, transport)
		metrics := &metricSpy{}
		svc.metrics = metrics
		svc.drain(t.Context())
		calls, _ := transport.snapshot()
		if calls != 0 || len(base.marked) != 0 || repo.count() != 2 || delivery.Attempt != 7 {
			t.Fatalf("second check admitted remote work: calls=%d marks=%d checks=%d", calls, len(base.marked), repo.count())
		}
		want := "send_ineligible"
		if late {
			want = "send_check_failed"
		}
		if len(metrics.callbackOutcomes) != 1 || metrics.callbackOutcomes[0] != want {
			t.Fatalf("second check outcome = %v, want %s", metrics.callbackOutcomes, want)
		}
	}
}

func TestCallbackSendGateChecksQueuedClaimAfterBulkheadWait(t *testing.T) {
	first, queued := callbackDelivery(callbackTestEndpoint, 1), callbackDelivery(callbackTestEndpoint, 7)
	base := newCallbackFakeRepo(first, queued)
	var revoked atomic.Bool
	repo := &callbackSendProbeRepo{callbackFakeRepo: base, check: func(_ context.Context, d *domain.CallbackDelivery, _ int) error {
		if d.ID == queued.ID && revoked.Load() {
			return repository.ErrCallbackDeliverySendIneligible
		}
		return nil
	}}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseTransport := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseTransport)
	transport := &callbackGateBlockingTransport{started: started, release: release}
	svc := newCallbackTestService(repo, CallbackConfig{AllowPrivateTarget: true, WorkerConcurrency: 1}, transport)
	go func() { svc.drain(t.Context()); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first callback did not enter the transport")
	}
	revoked.Store(true)
	releaseTransport()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked queued callback did not finish")
	}
	if transport.calls.Load() != 1 || len(base.marked) != 1 || base.marked[0].id != first.ID || queued.Attempt != 7 {
		t.Fatal("queued stale claim sent or consumed a retry budget")
	}
}

type callbackGateBlockingTransport struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (t *callbackGateBlockingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.calls.Add(1) == 1 {
		close(t.started)
	}
	select {
	case <-t.release:
	case <-request.Context().Done():
		return nil, request.Context().Err()
	}
	return (&cannedTransport{status: http.StatusAccepted}).RoundTrip(request)
}
