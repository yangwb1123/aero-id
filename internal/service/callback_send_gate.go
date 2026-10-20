package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

const callbackSendCheckBudget = 3 * time.Second

var (
	errCallbackSendIneligible  = errors.New("callback send admission is ineligible")
	errCallbackSendCheckFailed = errors.New("callback send admission cannot be verified")
)

// checkCallbackSend verifies metadata only. Missing ports, database failures and
// late success fail closed without converting an unattempted send into a failed
// remote attempt. Existing delivering state/lease is left for reconciliation.
func (s *callbackService) checkCallbackSend(ctx context.Context, delivery *domain.CallbackDelivery) error {
	if s.cfg.DeliveryPaused || delivery == nil {
		return errCallbackSendIneligible
	}
	checker, ok := s.repo.(repository.CallbackDeliverySendRepository)
	if !ok || isNilExecutionDependency(checker) {
		return errCallbackSendCheckFailed
	}
	checkCtx, cancel := context.WithTimeout(ctx, callbackSendCheckBudget)
	defer cancel()
	if checkCtx.Err() != nil {
		return errCallbackSendCheckFailed
	}
	err := checker.CheckDeliverySend(checkCtx, delivery, s.worker)
	if checkCtx.Err() != nil {
		return errCallbackSendCheckFailed
	}
	if errors.Is(err, repository.ErrCallbackDeliverySendIneligible) {
		return errCallbackSendIneligible
	}
	if err != nil {
		return errCallbackSendCheckFailed
	}
	return nil
}

type callbackSendTransport struct {
	service  *callbackService
	delivery *domain.CallbackDelivery
	next     http.RoundTripper
	mu       sync.Mutex
	blocked  error
}

func (t *callbackSendTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.service.checkCallbackSend(request.Context(), t.delivery); err != nil {
		t.mu.Lock()
		t.blocked = err
		t.mu.Unlock()
		return nil, err
	}
	return t.next.RoundTrip(request)
}

func (t *callbackSendTransport) blockedError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.blocked
}

func (t *callbackSendTransport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (s *callbackService) callbackSendClient(ctx context.Context, delivery *domain.CallbackDelivery) (*http.Client, *callbackSendTransport, error) {
	client, err := s.httpClient(ctx, delivery.Endpoint)
	if err != nil {
		return nil, nil, err
	}
	gate := &callbackSendTransport{service: s, delivery: delivery, next: client.Transport}
	client.Transport = gate
	return client, gate, nil
}

func (s *callbackService) observeCallbackSendBlock(err error) bool {
	switch {
	case errors.Is(err, errCallbackSendIneligible):
		s.metrics.ObserveCallbackOutcome("send_ineligible")
	case errors.Is(err, errCallbackSendCheckFailed):
		s.metrics.ObserveCallbackOutcome("send_check_failed")
	default:
		return false
	}
	return true
}
