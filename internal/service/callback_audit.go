package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/aero/aero-id/internal/connector/auditgovernance"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

var errAuditTenantUnbound = errors.New("audit publisher tenant binding unavailable")

// auditPublicationClaimLimit bounds one claim batch; the lease sizing (M-5)
// and the bulkhead fan-out are derived from it.
const auditPublicationClaimLimit = 50

type tenantBoundAuditPublisher interface {
	CanPublishTenant(string) bool
}

type tenantUnhealthyAuditPublisher interface {
	BindingUnhealthy(string) bool
}

// enqueueFact is the drain-worker-only delivery entry point (REQ-2): after
// the Publish bypass was deleted, it is reachable solely from publishAuditFact.
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
		// FM-8: unwired outbox. Facts pend with no delivery attempt; the live
		// composition root fails closed at wiring time (app.wireAuditServices),
		// so this counter is the runtime signal if that guard is ever bypassed.
		s.metrics.ObserveAuditPublicationDrainSkipped()
		return
	}
	items, err := s.publications.ClaimPublication(ctx, s.worker, s.publicationLease(), auditPublicationClaimLimit)
	if err != nil {
		// FM-1: never silent. The claim-failed counter carries a phase label
		// splitting two distinct semantics:
		//   claim_tx — the claim tx rolled back atomically; nothing was leased,
		//     facts pend with no attempt, the next tick retries the claim.
		//   fact_hydration — the claim tx committed (every row delivering) but
		//     post-commit fact hydration failed partway (FM-12); the hydrated
		//     prefix is still delivered below, the remainder re-claims after
		//     lease expiry, attempt untouched (M-4).
		phase := "claim_tx"
		if errors.Is(err, repository.ErrAuditPublicationFactHydration) {
			phase = "fact_hydration"
		}
		s.metrics.ObserveAuditPublicationClaimFailed(phase, err)
		if phase == "claim_tx" {
			return
		}
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

// publicationLease sizes the claim lease so it always exceeds the worst-case
// batch latency of the shared bulkhead (M-5): auditPublicationClaimLimit items
// × the per-publish governance timeout, serialized through WorkerConcurrency
// slots, doubled for margin, floored at 5 minutes (the crash-recovery window
// for default configs). drainPublications runs synchronously inside the ticker
// (callback.go Start), so at most one batch is in flight per instance; a lease
// ≥ 2× the worst-case batch latency therefore also prevents another instance
// from re-claiming a still-in-flight batch. If a future publisher is allowed
// unbounded latency, per-item lease refresh becomes mandatory (design §8).
func (s *callbackService) publicationLease() time.Duration {
	timeout := s.cfg.PublicationTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	concurrency := s.cfg.WorkerConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	lease := 2 * time.Duration(auditPublicationClaimLimit) * timeout / time.Duration(concurrency)
	if lease < 5*time.Minute {
		lease = 5 * time.Minute
	}
	return lease
}

func (s *callbackService) finishPublicationAttempt(ctx context.Context, item *domain.AuditPublication) {
	if item == nil || item.Fact == nil {
		return
	}
	now := time.Now().UTC()
	// M-4: the attempt is incremented here, only when a publish outcome is
	// computed — never at claim time. Lease-expiry re-claims that never
	// published (crash after claim, saturated bulkhead, claim hydration
	// failure) must not inflate the counter toward a premature terminal
	// dead_letter. ClaimPublication now leaves attempt untouched; the value
	// persisted here is this committed settlement's number. A remote delivery
	// may therefore be repeated after a crash without consuming the counter.
	nextAttempt := item.Attempt + 1
	status, code, next := publicationOutcome(
		s.publishAuditFact(ctx, item.Fact), nextAttempt, s.cfg.MaxAttempts, now, s.retryDelay(nextAttempt),
	)
	// No retry and no swallow: on ErrAuditPublicationLeaseLost the row is
	// re-claimed after lease expiry; on a DB error the lease expires and the
	// row is re-claimed too. The lease-guarded UPDATE prevents double-finish.
	// Settlement observability (FM-5): count only committed settlements. On a
	// finish failure the row stays delivering and the decision is re-made after
	// lease-expiry re-claim, so the outcome is not counted — the failure is
	// observed separately above.
	if err := s.publications.FinishPublication(ctx, item.ID, s.worker, item.LeaseGeneration, nextAttempt, status, code, next); err != nil {
		s.metrics.ObserveAuditPublicationFinishFailure(status, err)
		return
	}
	s.metrics.ObserveAuditPublicationOutcome(status)
}

func publicationOutcome(err error, attempt, maxAttempts int, now time.Time, retryDelay time.Duration) (string, string, time.Time) {
	if err == nil {
		return "completed", "", now
	}
	if errors.Is(err, errAuditTenantUnbound) || errors.Is(err, auditgovernance.ErrTokenScopeMissing) {
		return "pending", "audit.binding_unavailable", now.Add(retryDelay)
	}
	if errors.Is(err, auditgovernance.ErrEventRejected) {
		return "dead_letter", "audit.event_rejected", now.Add(retryDelay)
	}
	if errors.Is(err, auditgovernance.ErrInvalidReceipt) {
		if attempt >= maxAttempts {
			return "dead_letter", "audit.invalid_receipt", now.Add(retryDelay)
		}
		return "pending", "audit.invalid_receipt", now.Add(retryDelay)
	}
	if attempt >= maxAttempts {
		return "dead_letter", "audit.publication_failed", now.Add(retryDelay)
	}
	return "pending", "audit.publication_failed", now.Add(retryDelay)
}

func (s *callbackService) publishAuditFact(ctx context.Context, fact *domain.AuditFact) error {
	// FM-3: the tenant-binding pre-filter runs before ANY publisher, so an
	// unbound or unhealthy tenant can never let earlier publishers already
	// publish. The check consumes no attempt and never dead-letters
	// (publicationOutcome handles errAuditTenantUnbound before MaxAttempts).
	if !auditPublishersAllowTenant(s.publishers, fact.TenantID) {
		return errAuditTenantUnbound
	}
	if err := s.enqueueFact(ctx, fact); err != nil {
		return err
	}
	for _, publisher := range s.publishers {
		if publisher == nil {
			continue
		}
		if err := publisher.Publish(ctx, fact); err != nil {
			return s.classifyScopeMissing(fact, err)
		}
	}
	return nil
}

func auditPublishersAllowTenant(publishers []AuditPublisher, tenant string) bool {
	for _, publisher := range publishers {
		if !auditPublisherAllowsTenant(publisher, tenant) {
			return false
		}
	}
	return true
}

func auditPublisherAllowsTenant(publisher AuditPublisher, tenant string) bool {
	if publisher == nil {
		return true
	}
	bound, ok := publisher.(tenantBoundAuditPublisher)
	if !ok || !bound.CanPublishTenant(tenant) {
		return !ok
	}
	unhealthy, ok := publisher.(tenantUnhealthyAuditPublisher)
	return !ok || !unhealthy.BindingUnhealthy(tenant)
}

// classifyScopeMissing maps a publisher error onto the drain settlement and
// the scope-missing alert (REQ-5): a scope-missing classification (events
// 401-with-scope-body or a token-endpoint scope rejection) settles the row
// pending at audit.binding_unavailable — never completed, never swallowed,
// never dead-lettered (publicationOutcome handles errAuditTenantUnbound
// before the MaxAttempts check). Alert semantics (C-C): a token-endpoint
// rejection was already counted once by the composition-root hook, so the
// marker-carrying error skips this counter; an events-endpoint classification
// fires it once per pended publication attempt.
func (s *callbackService) classifyScopeMissing(fact *domain.AuditFact, err error) error {
	if errors.Is(err, auditgovernance.ErrTokenScopeMissing) {
		// A3 skip: a token-endpoint rejection fires only the hook
		// (buildAuditScopeMissingHook); the drain counter covers only
		// events-endpoint classifications. Skipping here restores exactly
		// one increment per scope-missing condition (T-10.2.3).
		if !errors.Is(err, auditgovernance.ErrTokenEndpointScopeMissing) {
			s.metrics.ObserveAuditScopeMissing(strings.TrimSpace(fact.Source))
		}
		return errAuditTenantUnbound
	}
	return err
}
