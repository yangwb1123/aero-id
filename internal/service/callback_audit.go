package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

var errAuditTenantUnbound = errors.New("audit publisher tenant binding unavailable")

type tenantBoundAuditPublisher interface {
	CanPublishTenant(string) bool
}

func (s *callbackService) Publish(ctx context.Context, fact *domain.AuditFact) error {
	if fact == nil || strings.TrimSpace(fact.TenantID) == "" {
		return nil
	}
	if s.publications != nil {
		return nil
	}
	return s.enqueueFact(ctx, fact)
}

func (s *callbackService) enqueueFact(ctx context.Context, fact *domain.AuditFact) error {
	subscriptions, err := s.repo.ListEnabledByAction(ctx, fact.TenantID, fact.Action)
	if err != nil {
		return err
	}
	payload := map[string]any{"schema_version": 1, "event_id": fact.ID.String(), "action": fact.Action, "source": fact.Source,
		"source_region": fact.SourceRegion, "actor_uid": fact.ActorUID, "account_id": fact.AccountID.String(),
		"operation_id": fact.OperationID.String(), "occurred_at": fact.OccurredAt, "payload": fact.Payload,
		"record_hash": fact.RecordHash}
	for _, subscription := range subscriptions {
		if err := s.repo.EnqueueDelivery(ctx, &domain.CallbackDelivery{SubscriptionID: subscription.ID, EventID: fact.ID,
			Action: fact.Action, Payload: payload}); err != nil {
			return err
		}
	}
	return nil
}

func (s *callbackService) drainPublications(ctx context.Context) {
	if s.publications == nil {
		return
	}
	items, err := s.publications.ClaimPublication(ctx, s.worker, 5*time.Minute, 50)
	if err != nil {
		return
	}
	// P6-06 bulkhead: audit publication is an outbound HTTP workload; bound
	// in-flight publishes so a slow governance endpoint cannot starve callbacks.
	var wg sync.WaitGroup
	for _, item := range items {
		item := item
		if err := s.bulkhead.Acquire(ctx); err != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer s.bulkhead.Release()
			s.finishPublicationAttempt(ctx, item)
		}()
	}
	wg.Wait()
}

func (s *callbackService) finishPublicationAttempt(ctx context.Context, item *domain.AuditPublication) {
	if item == nil || item.Fact == nil {
		return
	}
	now := time.Now().UTC()
	status, code, next := publicationOutcome(
		s.publishAuditFact(ctx, item.Fact), item.Attempt, s.cfg.MaxAttempts, now, s.retryDelay(item.Attempt),
	)
	_ = s.publications.FinishPublication(ctx, item.ID, s.worker, status, code, next)
}

func publicationOutcome(err error, attempt, maxAttempts int, now time.Time, retryDelay time.Duration) (string, string, time.Time) {
	if err == nil {
		return "completed", "", now
	}
	if errors.Is(err, errAuditTenantUnbound) {
		return "pending", "audit.binding_unavailable", now.Add(retryDelay)
	}
	if attempt >= maxAttempts {
		return "dead_letter", "audit.publication_failed", now.Add(retryDelay)
	}
	return "pending", "audit.publication_failed", now.Add(retryDelay)
}

func (s *callbackService) publishAuditFact(ctx context.Context, fact *domain.AuditFact) error {
	if err := s.enqueueFact(ctx, fact); err != nil {
		return err
	}
	for _, publisher := range s.publishers {
		if publisher == nil {
			continue
		}
		if bound, ok := publisher.(tenantBoundAuditPublisher); ok && !bound.CanPublishTenant(fact.TenantID) {
			return errAuditTenantUnbound
		}
		if err := publisher.Publish(ctx, fact); err != nil {
			return err
		}
	}
	return nil
}
