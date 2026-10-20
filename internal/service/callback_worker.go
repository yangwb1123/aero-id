package service

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

// callback_worker.go owns the delivery/publication worker loop (Start), the
// dead-letter retention purge (purgeDeadLetters), the deliveries drain
// (drain), and every delivery-attempt helper (markDelivery, deliver,
// retryDelay). Network policy/client ownership lives in callback_network.go,
// callback_address_policy.go and callback_endpoint.go.
// The request path (Register/List/Delete/ReplayDelivery) lives in
// callback.go; the audit publication drain lives in callback_audit.go.

func (s *callbackService) Start(ctx context.Context, interval time.Duration) func() {
	s.callbackDeliveryPermission()
	if interval <= 0 {
		interval = time.Second
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-ticker.C:
				s.drainPublications(workerCtx)
				s.drain(workerCtx)
				s.purgeDeadLetters(workerCtx)
			case <-workerCtx.Done():
				return
			}
		}
	}()
	return func() { cancel(); <-done }
}

// purgeDeadLetters is the bounded dead-letter retention scan (B2-5 WORKER-1):
// one keyset-ordered batch per kind per tick. It is a no-op unless a positive
// retention is configured and purge_pause is clear; paused ticks perform no
// SELECT and no DELETE, so dead-letter rows are preserved bit-for-bit while a
// replay run (cmd/audit-replay) recovers them. The purge predicates exclude
// leased rows at the SQL boundary (lease_owner empty and lease expiry).
func (s *callbackService) purgeDeadLetters(ctx context.Context) {
	if s.cfg.DeadLetterRetention <= 0 || s.cfg.PurgePause {
		return
	}
	cutoff := time.Now().UTC().Add(-s.cfg.DeadLetterRetention)
	if s.publications != nil {
		purged, err := s.publications.PurgeDeadLetterPublications(ctx, cutoff, 50)
		if err != nil {
			s.metrics.ObserveAuditPublicationPurgeFailed("publication", err)
		} else if purged > 0 {
			s.metrics.ObserveAuditPublicationPurged("publication", purged)
		}
	}
	if s.cfg.DeliveryPaused || len(s.key) == 0 {
		return // Preserve callback deliveries during operator/key maintenance.
	}
	purged, err := s.repo.PurgeDeadLetterDeliveries(ctx, cutoff, 50)
	if err != nil {
		s.metrics.ObserveAuditPublicationPurgeFailed("delivery", err)
	} else if purged > 0 {
		s.metrics.ObserveAuditPublicationPurged("delivery", purged)
	}
}

func (s *callbackService) drain(ctx context.Context) {
	if s.cfg.DeliveryPaused {
		s.metrics.ObserveCallbackOutcome("paused")
		return
	}
	if len(s.key) == 0 {
		s.metrics.ObserveCallbackOutcome("key_unavailable")
		return
	}
	deliveries, err := s.repo.FetchDueDeliveries(ctx, 50, s.worker, 5*time.Minute)
	if err != nil {
		return
	}
	// P6-06 bulkhead: concurrent deliveries are bounded by WorkerConcurrency;
	// each delivery row is leased to this worker, so parallel marking is safe.
	var wg sync.WaitGroup
	for _, delivery := range deliveries {
		delivery := delivery
		if err := s.bulkhead.Acquire(ctx); err != nil {
			break // context cancelled; remaining rows stay due for the next tick
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer s.bulkhead.Release()
			s.markDelivery(ctx, delivery)
		}()
	}
	wg.Wait()
}

func (s *callbackService) markDelivery(ctx context.Context, delivery *domain.CallbackDelivery) {
	statusCode, response, deliverErr := s.deliver(ctx, delivery)
	if s.observeCallbackSendBlock(deliverErr) {
		return
	}
	if errors.Is(deliverErr, errCallbackSecretUnavailable) {
		s.deferSecretUnavailable(ctx, delivery)
		return
	}
	status := "success"
	errorCode := ""
	next := time.Now().UTC()
	if deliverErr != nil || statusCode < 200 || statusCode >= 300 {
		status = "pending"
		errorCode = "callback.delivery_failed"
		if delivery.Attempt+1 >= s.cfg.MaxAttempts {
			status = "dead_letter"
		}
		next = next.Add(s.retryDelay(delivery.Attempt + 1))
	}
	if err := s.repo.MarkDelivery(ctx, delivery.ID, s.worker, delivery.LeaseGeneration, status, statusCode, response, errorCode, next); err != nil {
		// Settlement failed: keep the row delivering+leased (the existing
		// 5-minute lease re-fetch re-claims it with attempt unchanged) and
		// observe the failure on the outcome counter — never report
		// success/pending/dead_letter for a settlement that did not happen.
		s.metrics.ObserveCallbackOutcome("mark_failed")
		return
	}
	s.metrics.ObserveCallbackOutcome(status)
}

func (s *callbackService) deliver(ctx context.Context, delivery *domain.CallbackDelivery) (int, string, error) {
	if delivery == nil {
		return 0, "", errCallbackSendIneligible
	}
	// Detect local key failures before endpoint validation can resolve DNS or
	// consume the remote retry budget. Egress policy still precedes any send.
	secret, err := s.decrypt(delivery.SecretCiphertext)
	if err != nil {
		return 0, "", err
	}
	requestCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()
	if err := s.checkCallbackSend(requestCtx, delivery); err != nil {
		return 0, "", err
	}
	request, err := callbackDeliveryRequest(requestCtx, delivery, secret)
	if err != nil {
		return 0, "", err
	}
	client, gate, err := s.callbackSendClient(requestCtx, delivery)
	if err != nil {
		return 0, "", err
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	// net/http can replace a gate error with its request deadline error. Keep
	// the explicit pre-transport refusal distinct from an attempted timeout.
	if blocked := gate.blockedError(); blocked != nil {
		return 0, "", blocked
	}
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	return callbackDeliveryResponse(requestCtx, response, s.cfg.MaxResponseBytes)
}

func (s *callbackService) retryDelay(attempt int) time.Duration {
	delay := s.cfg.RetryBase
	for i := 1; i < attempt && delay < time.Hour; i++ {
		if delay > time.Hour/2 {
			delay = time.Hour
			break
		}
		delay *= 2
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	// Add bounded cryptographic jitter so many callback workers do not retry
	// the same endpoint in lockstep after an outage. Failure to obtain one
	// random byte is harmless: the deterministic cap remains safe.
	var randomByte [1]byte
	if _, err := rand.Read(randomByte[:]); err != nil {
		return delay
	}
	jitter := delay / 4 * time.Duration(randomByte[0]) / 255
	if delay > time.Hour-jitter {
		return time.Hour
	}
	return delay + jitter
}

// deferSecretUnavailable leaves the remote-attempt budget untouched. The same
// lease owner/generation/expiry CAS as settlement fences old workers; no plain
// MarkDelivery fallback may consume an attempt when the deferrer is unwired.
func (s *callbackService) deferSecretUnavailable(ctx context.Context, delivery *domain.CallbackDelivery) {
	repo, ok := s.repo.(repository.CallbackDeliveryDeferrer)
	if !ok {
		s.metrics.ObserveCallbackOutcome("defer_failed")
		return
	}
	persistenceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := repo.DeferDelivery(persistenceCtx, delivery.ID, s.worker, delivery.LeaseGeneration,
		"callback.secret_unavailable", 5*time.Minute); err != nil {
		s.metrics.ObserveCallbackOutcome("defer_failed")
		return
	}
	s.metrics.ObserveCallbackOutcome("secret_unavailable")
}
