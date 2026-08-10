package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/connector/auditgovernance"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

func TestPublicationOutcomeKeepsUnboundTenantRecoverable(t *testing.T) {
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	delay := 15 * time.Minute

	status, code, next := publicationOutcome(errAuditTenantUnbound, 99, 8, now, delay)
	if status != "pending" || code != "audit.binding_unavailable" || !next.Equal(now.Add(delay)) {
		t.Fatalf("unexpected unbound outcome: status=%q code=%q next=%s", status, code, next)
	}
}

func TestPublicationOutcomeDeadLettersBoundPublisherFailure(t *testing.T) {
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	status, code, _ := publicationOutcome(errors.New("transport"), 8, 8, now, time.Minute)
	if status != "dead_letter" || code != "audit.publication_failed" {
		t.Fatalf("unexpected publisher failure outcome: status=%q code=%q", status, code)
	}
}

// failingPublicationRepo models a FinishPublication failure (lease lost / DB
// error): the row is re-claimed after lease expiry, at-least-once is preserved.
type failingPublicationRepo struct {
	fail error
}

func (f *failingPublicationRepo) ClaimPublication(ctx context.Context, worker string, lease time.Duration, limit int) ([]*domain.AuditPublication, error) {
	return nil, nil
}
func (f *failingPublicationRepo) FinishPublication(ctx context.Context, id domain.ID, worker string, attempt int, status, errorCode string, next time.Time) error {
	return f.fail
}
func (f *failingPublicationRepo) RequeuePublication(context.Context, []domain.ID, int, repository.ReplayIntent) (int, []*domain.AuditFact, error) {
	return 0, nil, errors.New("failingPublicationRepo.RequeuePublication invoked in service test")
}
func (f *failingPublicationRepo) PurgeDeadLetterPublications(context.Context, time.Time, int) (int64, error) {
	return 0, errors.New("failingPublicationRepo.PurgeDeadLetterPublications invoked in service test")
}
func (f *failingPublicationRepo) CountDeadLetterPublications(context.Context, []domain.ID, int, string) (int, error) {
	return 0, errors.New("failingPublicationRepo.CountDeadLetterPublications invoked in service test")
}
func (f *failingPublicationRepo) DeadLetterTenants(context.Context, []domain.ID, int, string) ([]string, error) {
	return nil, errors.New("failingPublicationRepo.DeadLetterTenants invoked in service test")
}

// settlingPublicationRepo accepts every finish: settlements commit and are
// counted on the outcome metric. It records the persisted attempt and error
// code so tests can pin the M-4 contract (the incremented attempt is persisted
// only at settlement, never at claim time) and the publicationOutcome code
// (audit.binding_unavailable vs audit.publication_failed).
type settlingPublicationRepo struct {
	finishes []string
	codes    []string
	attempts []int
}

func (s *settlingPublicationRepo) ClaimPublication(ctx context.Context, worker string, lease time.Duration, limit int) ([]*domain.AuditPublication, error) {
	return nil, nil
}
func (s *settlingPublicationRepo) FinishPublication(ctx context.Context, id domain.ID, worker string, attempt int, status, errorCode string, next time.Time) error {
	s.finishes = append(s.finishes, status)
	s.codes = append(s.codes, errorCode)
	s.attempts = append(s.attempts, attempt)
	return nil
}
func (s *settlingPublicationRepo) RequeuePublication(context.Context, []domain.ID, int, repository.ReplayIntent) (int, []*domain.AuditFact, error) {
	return 0, nil, errors.New("settlingPublicationRepo.RequeuePublication invoked in service test")
}
func (s *settlingPublicationRepo) PurgeDeadLetterPublications(context.Context, time.Time, int) (int64, error) {
	return 0, errors.New("settlingPublicationRepo.PurgeDeadLetterPublications invoked in service test")
}
func (s *settlingPublicationRepo) CountDeadLetterPublications(context.Context, []domain.ID, int, string) (int, error) {
	return 0, errors.New("settlingPublicationRepo.CountDeadLetterPublications invoked in service test")
}
func (s *settlingPublicationRepo) DeadLetterTenants(context.Context, []domain.ID, int, string) ([]string, error) {
	return nil, errors.New("settlingPublicationRepo.DeadLetterTenants invoked in service test")
}

// failingAuditPublisher models a governance sink that rejects every event:
// the drain loop's only failure input on the dead-letter path.
type failingAuditPublisher struct {
	calls int
}

func (f *failingAuditPublisher) Publish(context.Context, *domain.AuditFact) error {
	f.calls++
	return errors.New("governance endpoint unavailable")
}

// scopeMissingPublisher models a governance sink that classifies scope-missing
// with a caller-chosen error: the marker-wrapped token-endpoint sentinel (hook
// path — the drain counter must stay silent because the composition-root hook
// already counted it) or the bare ErrTokenScopeMissing (events-endpoint path —
// the drain counter fires once per failed attempt).
type scopeMissingPublisher struct {
	calls int
	err   error
}

func (s *scopeMissingPublisher) Publish(context.Context, *domain.AuditFact) error {
	s.calls++
	return s.err
}

// claimFailure records one ObserveAuditPublicationClaimFailed observation with
// its phase (FM-1): "claim_tx" (nothing leased, next tick retries) vs
// "fact_hydration" (rows delivering, hydrated prefix delivered, remainder
// re-claims after lease expiry).
type claimFailure struct {
	phase string
	err   error
}

// metricSpy records ObserveAuditPublication* observations and the audit
// ingest-failure counter (REQ-7). Errors are recorded alongside the status so
// integration tests can pin which error kind reached the metric (raw DB error
// vs ErrAuditPublicationLeaseLost). scopeMissing captures the label, not just
// the count, so drain tests can pin the FM-14 label convention
// (strings.TrimSpace(fact.Source)).
type metricSpy struct {
	finishFailures    []string
	finishFailureErrs []error
	outcomes          []string
	callbackOutcomes  []string // ObserveCallbackOutcome labels (success|pending|dead_letter|mark_failed)
	drainSkipped      int
	claimFailures     []claimFailure
	ingestFailures    []string
	scopeMissing      []string
	standaloneActions []string
	purged        []string // kind labels observed on the dead-letter purge metric
	purgeFailures []string
}

func (m *metricSpy) ObserveEventApplied(string)     {}
func (m *metricSpy) ObserveEventQuarantined(string) {}
func (m *metricSpy) ObserveAuditIngestFailure(source string) {
	m.ingestFailures = append(m.ingestFailures, source)
}
func (m *metricSpy) ObserveAuditScopeMissing(source string) {
	m.scopeMissing = append(m.scopeMissing, source)
}
func (m *metricSpy) ObserveAuditStandaloneRecorded(action string) {
	m.standaloneActions = append(m.standaloneActions, action)
}
func (m *metricSpy) ObserveAuditPublicationFinishFailure(status string, err error) {
	m.finishFailures = append(m.finishFailures, status)
	m.finishFailureErrs = append(m.finishFailureErrs, err)
}
func (m *metricSpy) ObserveAuditPublicationOutcome(status string) {
	m.outcomes = append(m.outcomes, status)
}
func (m *metricSpy) ObserveAuditPublicationDrainSkipped() {
	m.drainSkipped++
}
func (m *metricSpy) ObserveAuditPublicationClaimFailed(phase string, err error) {
	m.claimFailures = append(m.claimFailures, claimFailure{phase: phase, err: err})
}
func (m *metricSpy) ObserveAuditPublicationPurged(kind string, count int64) {
	m.purged = append(m.purged, kind)
}
func (m *metricSpy) ObserveAuditPublicationPurgeFailed(kind string, err error) {
	m.purgeFailures = append(m.purgeFailures, kind)
}
func (m *metricSpy) ObserveOperationOutcome(string)                     {}
func (m *metricSpy) ObserveCallbackOutcome(status string) {
	m.callbackOutcomes = append(m.callbackOutcomes, status)
}
func (m *metricSpy) ObserveReconcileDrift(string)                       {}
func (m *metricSpy) ObserveProjectionLag(string, string, time.Duration) {}
func (m *metricSpy) ObserveConsumerLag(string, time.Duration)           {}
func (m *metricSpy) ObserveOperationAge(string, time.Duration)          {}

// claimingPublicationRepo models a ClaimPublication that errors outright
// (FM-1): the claim transaction fails before any row is leased, so nothing is
// delivered and nothing is settled.
type claimingPublicationRepo struct {
	fail error
}

func (f *claimingPublicationRepo) ClaimPublication(ctx context.Context, worker string, lease time.Duration, limit int) ([]*domain.AuditPublication, error) {
	return nil, f.fail
}
func (f *claimingPublicationRepo) FinishPublication(ctx context.Context, id domain.ID, worker string, attempt int, status, errorCode string, next time.Time) error {
	return nil
}
func (f *claimingPublicationRepo) RequeuePublication(context.Context, []domain.ID, int, repository.ReplayIntent) (int, []*domain.AuditFact, error) {
	return 0, nil, errors.New("claimingPublicationRepo.RequeuePublication invoked in service test")
}
func (f *claimingPublicationRepo) PurgeDeadLetterPublications(context.Context, time.Time, int) (int64, error) {
	return 0, errors.New("claimingPublicationRepo.PurgeDeadLetterPublications invoked in service test")
}
func (f *claimingPublicationRepo) CountDeadLetterPublications(context.Context, []domain.ID, int, string) (int, error) {
	return 0, errors.New("claimingPublicationRepo.CountDeadLetterPublications invoked in service test")
}
func (f *claimingPublicationRepo) DeadLetterTenants(context.Context, []domain.ID, int, string) ([]string, error) {
	return nil, errors.New("claimingPublicationRepo.DeadLetterTenants invoked in service test")
}

// TestDrainPublicationsClaimFailureObservesMetric (FM-1, claim_tx phase): a
// ClaimPublication error is observed on the claim-failed metric with
// phase="claim_tx" — never a silent return — and nothing is delivered,
// finished, or settled (no row was leased).
func TestDrainPublicationsClaimFailureObservesMetric(t *testing.T) {
	metrics := &metricSpy{}
	svc := newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.publications = &claimingPublicationRepo{fail: errors.New("claim tx failed")}
	svc.metrics = metrics

	svc.drainPublications(t.Context())

	if len(metrics.claimFailures) != 1 {
		t.Fatalf("claim failures observed = %d, want 1", len(metrics.claimFailures))
	}
	if metrics.claimFailures[0].phase != "claim_tx" {
		t.Fatalf("claim failure phase = %q, want claim_tx (nothing was leased)", metrics.claimFailures[0].phase)
	}
	if len(metrics.outcomes) != 0 || len(metrics.finishFailures) != 0 || len(metrics.finishFailureErrs) != 0 {
		t.Fatalf("outcomes/finish failures = %v/%v, want none (nothing was claimed)", metrics.outcomes, metrics.finishFailures)
	}
}

// hydrationGapPublicationRepo models ClaimPublication whose tx committed but
// whose post-commit fact hydration failed partway (FM-12): the repo returns
// the hydrated prefix alongside the wrapped ErrAuditPublicationFactHydration.
type hydrationGapPublicationRepo struct {
	items []*domain.AuditPublication
	err   error
}

func (h *hydrationGapPublicationRepo) ClaimPublication(ctx context.Context, worker string, lease time.Duration, limit int) ([]*domain.AuditPublication, error) {
	return h.items, h.err
}
func (h *hydrationGapPublicationRepo) FinishPublication(ctx context.Context, id domain.ID, worker string, attempt int, status, errorCode string, next time.Time) error {
	return nil
}
func (h *hydrationGapPublicationRepo) RequeuePublication(context.Context, []domain.ID, int, repository.ReplayIntent) (int, []*domain.AuditFact, error) {
	return 0, nil, errors.New("hydrationGapPublicationRepo.RequeuePublication invoked in service test")
}
func (h *hydrationGapPublicationRepo) PurgeDeadLetterPublications(context.Context, time.Time, int) (int64, error) {
	return 0, errors.New("hydrationGapPublicationRepo.PurgeDeadLetterPublications invoked in service test")
}
func (h *hydrationGapPublicationRepo) CountDeadLetterPublications(context.Context, []domain.ID, int, string) (int, error) {
	return 0, errors.New("hydrationGapPublicationRepo.CountDeadLetterPublications invoked in service test")
}
func (h *hydrationGapPublicationRepo) DeadLetterTenants(context.Context, []domain.ID, int, string) ([]string, error) {
	return nil, errors.New("hydrationGapPublicationRepo.DeadLetterTenants invoked in service test")
}

// TestDrainPublicationsFactHydrationGapObservesMetricAndDeliversPrefix
// (FM-12): the claim tx committed (rows delivering) but fact hydration failed
// partway. The claim-failed metric records phase="fact_hydration", the
// hydrated prefix is STILL delivered and settled this tick, and the remainder
// is left for lease-expiry re-claim — no attempt was consumed for the
// un-hydrated rows (M-4).
func TestDrainPublicationsFactHydrationGapObservesMetricAndDeliversPrefix(t *testing.T) {
	metrics := &metricSpy{}
	publisher := &boundPublisher{tenant: "tenant-a"}
	svc := newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	fact := &domain.AuditFact{ID: idgen.NewID(), TenantID: "tenant-a", Action: "account.provisioned"}
	svc.publications = &hydrationGapPublicationRepo{
		items: []*domain.AuditPublication{{ID: idgen.NewID(), Status: "delivering", Attempt: 0, Fact: fact}},
		err:   fmt.Errorf("%w: fact %s: connection reset", repository.ErrAuditPublicationFactHydration, idgen.NewID()),
	}
	svc.publishers = []AuditPublisher{publisher}
	svc.metrics = metrics

	svc.drainPublications(t.Context())

	if len(metrics.claimFailures) != 1 || metrics.claimFailures[0].phase != "fact_hydration" {
		t.Fatalf("claim failures = %+v, want exactly one with phase fact_hydration", metrics.claimFailures)
	}
	if !errors.Is(metrics.claimFailures[0].err, repository.ErrAuditPublicationFactHydration) {
		t.Fatalf("claim failure error = %v, want wrapped ErrAuditPublicationFactHydration", metrics.claimFailures[0].err)
	}
	// The hydrated prefix is delivered despite the error (not dropped).
	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1 (hydrated prefix must still be delivered)", publisher.calls)
	}
	if len(metrics.outcomes) != 1 || metrics.outcomes[0] != "completed" {
		t.Fatalf("outcome observations = %v, want [completed] for the prefix", metrics.outcomes)
	}
}

// boundPublisher models a tenant-bound governance publisher (FM-3).
type boundPublisher struct {
	tenant string
	calls  int
}

func (b *boundPublisher) CanPublishTenant(tenant string) bool { return tenant == b.tenant }
func (b *boundPublisher) Publish(context.Context, *domain.AuditFact) error {
	b.calls++
	return nil
}

// TestPublishAuditFactTenantPreFilterSkipsAllPublishers (FM-3): the tenant
// binding check runs BEFORE the publisher loop, so an unbound tenant can never
// cause earlier publishers to have already published (duplicate publishes on
// every retry). With two publishers where only the second is bound, an unbound
// tenant must produce zero calls on both.
func TestPublishAuditFactTenantPreFilterSkipsAllPublishers(t *testing.T) {
	first := &boundPublisher{tenant: "tenant-a"}
	second := &boundPublisher{tenant: "tenant-a"}
	svc := newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.publishers = []AuditPublisher{first, second}

	err := svc.publishAuditFact(t.Context(), &domain.AuditFact{ID: idgen.NewID(), TenantID: "tenant-other", Action: "account.provisioned"})
	if !errors.Is(err, errAuditTenantUnbound) {
		t.Fatalf("publish error = %v, want errAuditTenantUnbound", err)
	}
	if first.calls != 0 || second.calls != 0 {
		t.Fatalf("publisher calls = %d/%d, want 0/0 (pre-filter precedes every publish)", first.calls, second.calls)
	}
}

// TestPublicationLeaseCoversWorstCaseBatch (M-5): the drain lease must exceed
// the worst-case batch latency (claim limit × per-publish timeout serialized
// through the shared bulkhead) with margin, and keep the 5-minute floor for
// crash recovery under default config.
func TestPublicationLeaseCoversWorstCaseBatch(t *testing.T) {
	// Defaults: 50 × 5s / 4 slots = 62.5s worst case; 2× margin < 5m floor.
	svc := newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second, WorkerConcurrency: 4}, nil)
	if lease := svc.publicationLease(); lease != 5*time.Minute {
		t.Fatalf("default lease = %v, want the 5m floor", lease)
	}
	// Serial drain with a raised timeout: 50 × 10s / 1 = 500s worst case;
	// the lease must grow past the floor to cover the batch (2× margin).
	svc = newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second,
		WorkerConcurrency: 1, PublicationTimeout: 10 * time.Second}, nil)
	if lease := svc.publicationLease(); lease != 1000*time.Second {
		t.Fatalf("serial lease = %v, want 1000s (2 × 50 × 10s / 1)", lease)
	}
}

// TestFinishPublicationFailureObservesMetricNoRetry (FM-4): a failed
// FinishPublication is observed on the finish-failure metric, never swallowed,
// and never retried inline (the lease-expiry reclaim is the retry mechanism).
// The settlement is not counted as an outcome: the row stays delivering and
// the decision is re-made after re-claim.
func TestFinishPublicationFailureObservesMetricNoRetry(t *testing.T) {
	metrics := &metricSpy{}
	svc := newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.publications = &failingPublicationRepo{fail: repository.ErrAuditPublicationLeaseLost}
	svc.metrics = metrics

	item := &domain.AuditPublication{ID: idgen.NewID(), Status: "delivering",
		Fact: &domain.AuditFact{ID: idgen.NewID(), TenantID: "tenant-a", Action: "account.provisioned"}}
	svc.finishPublicationAttempt(t.Context(), item)

	if len(metrics.finishFailures) != 1 {
		t.Fatalf("finish failures observed = %d, want 1", len(metrics.finishFailures))
	}
	if metrics.finishFailures[0] != "completed" {
		t.Fatalf("observed status = %q, want completed (publish succeeded, finish failed)", metrics.finishFailures[0])
	}
	if len(metrics.outcomes) != 0 {
		t.Fatalf("outcomes observed = %v, want none (unsettled finish is not a settlement)", metrics.outcomes)
	}
}

// TestFinishPublicationSettlesObservesOutcome (FM-5 observability): a committed
// settlement is counted on the outcome metric with the decided status.
func TestFinishPublicationSettlesObservesOutcome(t *testing.T) {
	metrics := &metricSpy{}
	repo := &settlingPublicationRepo{}
	svc := newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.publications = repo
	svc.metrics = metrics

	item := &domain.AuditPublication{ID: idgen.NewID(), Status: "delivering", Attempt: 1,
		Fact: &domain.AuditFact{ID: idgen.NewID(), TenantID: "tenant-a", Action: "account.provisioned"}}
	svc.finishPublicationAttempt(t.Context(), item)

	if len(metrics.outcomes) != 1 || metrics.outcomes[0] != "completed" {
		t.Fatalf("outcomes observed = %v, want [completed]", metrics.outcomes)
	}
	if len(repo.finishes) != 1 || repo.finishes[0] != "completed" {
		t.Fatalf("finishes = %v, want [completed]", repo.finishes)
	}
	// M-4: the attempt persisted at settlement is the claim-time value plus
	// one (this delivery attempt) — claim never increments it.
	if len(repo.attempts) != 1 || repo.attempts[0] != 2 {
		t.Fatalf("persisted attempts = %v, want [2] (item.Attempt 1 + this attempt)", repo.attempts)
	}
	if len(metrics.finishFailures) != 0 {
		t.Fatalf("finish failures = %v, want none", metrics.finishFailures)
	}
}

// TestFinishPublicationDeadLetterSettlesObservesOutcome (FM-5): the terminal
// MaxAttempts exhaustion settles dead_letter and is counted on the outcome
// metric — the alertable signal for governance sink non-delivery.
func TestFinishPublicationDeadLetterSettlesObservesOutcome(t *testing.T) {
	metrics := &metricSpy{}
	repo := &settlingPublicationRepo{}
	publisher := &failingAuditPublisher{}
	svc := newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.publications = repo
	svc.publishers = []AuditPublisher{publisher}
	svc.metrics = metrics

	item := &domain.AuditPublication{ID: idgen.NewID(), Status: "delivering", Attempt: 7,
		Fact: &domain.AuditFact{ID: idgen.NewID(), TenantID: "tenant-a", Action: "account.provisioned"}}
	svc.finishPublicationAttempt(t.Context(), item)

	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1", publisher.calls)
	}
	if len(metrics.outcomes) != 1 || metrics.outcomes[0] != "dead_letter" {
		t.Fatalf("outcomes observed = %v, want [dead_letter]", metrics.outcomes)
	}
	if len(repo.finishes) != 1 || repo.finishes[0] != "dead_letter" {
		t.Fatalf("finishes = %v, want [dead_letter]", repo.finishes)
	}
	// M-4: this is the 8th delivery attempt (7 prior + 1), which exhausts
	// MaxAttempts 8 — the persisted attempt is the incremented value.
	if len(repo.attempts) != 1 || repo.attempts[0] != 8 {
		t.Fatalf("persisted attempts = %v, want [8]", repo.attempts)
	}
}

// TestPublishAuditFactTokenEndpointScopeMissingSkipsDrainCounter (A3, C-C): a
// token-endpoint scope-missing error carries the ErrTokenEndpointScopeMissing
// marker (multi-%w wrap over ErrTokenScopeMissing, connector oauth.go fetchToken
// sites). The composition-root hook already counted it, so the drain counter
// must stay silent — while the row still settles pending at
// audit.binding_unavailable, even at the MaxAttempts edge (the unbound
// classification precedes the dead-letter check). This is the no-Docker fast
// detector for the A3 skip: a regression that drops the skip fires the counter
// for the wrapped error and fails here.
func TestPublishAuditFactTokenEndpointScopeMissingSkipsDrainCounter(t *testing.T) {
	metrics := &metricSpy{}
	repo := &settlingPublicationRepo{}
	publisher := &scopeMissingPublisher{err: fmt.Errorf("%w: %w",
		auditgovernance.ErrTokenEndpointScopeMissing, auditgovernance.ErrTokenScopeMissing)}
	svc := newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.publications = repo
	svc.publishers = []AuditPublisher{publisher}
	svc.metrics = metrics

	fact := &domain.AuditFact{ID: idgen.NewID(), Source: "aero-id", TenantID: "tenant-a", Action: "account.provisioned"}
	for _, attempt := range []int{0, 7} { // 7 → nextAttempt 8 == MaxAttempts: the unbound classification must still win
		item := &domain.AuditPublication{ID: idgen.NewID(), Status: "delivering", Attempt: attempt, Fact: fact}
		svc.finishPublicationAttempt(t.Context(), item)
	}

	if publisher.calls != 2 {
		t.Fatalf("publisher calls = %d, want 2 (both attempts reached the sink)", publisher.calls)
	}
	if len(metrics.scopeMissing) != 0 {
		t.Fatalf("scope-missing counter observations = %v, want none (token-endpoint path is hook-counted; A3 must skip the drain counter)", metrics.scopeMissing)
	}
	if len(repo.finishes) != 2 || repo.finishes[0] != "pending" || repo.finishes[1] != "pending" {
		t.Fatalf("finishes = %v, want [pending pending] (never dead_letter, even at MaxAttempts)", repo.finishes)
	}
	if len(repo.codes) != 2 || repo.codes[0] != "audit.binding_unavailable" || repo.codes[1] != "audit.binding_unavailable" {
		t.Fatalf("error codes = %v, want [audit.binding_unavailable audit.binding_unavailable]", repo.codes)
	}
	if len(repo.attempts) != 2 || repo.attempts[0] != 1 || repo.attempts[1] != 8 {
		t.Fatalf("persisted attempts = %v, want [1 8]", repo.attempts)
	}
}

// TestPublishAuditFactEventsEndpointScopeMissingFiresDrainCounter (A3, C-C):
// an events-endpoint scope-missing error is the bare ErrTokenScopeMissing (no
// marker — the connector hook is structurally unreachable there), so the drain
// counter fires exactly once per failed attempt with the normalized
// fact.Source label, and the row settles pending at audit.binding_unavailable,
// never dead_letter.
func TestPublishAuditFactEventsEndpointScopeMissingFiresDrainCounter(t *testing.T) {
	metrics := &metricSpy{}
	repo := &settlingPublicationRepo{}
	publisher := &scopeMissingPublisher{err: auditgovernance.ErrTokenScopeMissing}
	svc := newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.publications = repo
	svc.publishers = []AuditPublisher{publisher}
	svc.metrics = metrics

	item := &domain.AuditPublication{ID: idgen.NewID(), Status: "delivering", Attempt: 0,
		Fact: &domain.AuditFact{ID: idgen.NewID(), Source: "aero-id", TenantID: "tenant-a", Action: "account.provisioned"}}
	svc.finishPublicationAttempt(t.Context(), item)

	if len(metrics.scopeMissing) != 1 || metrics.scopeMissing[0] != "aero-id" {
		t.Fatalf("scope-missing counter observations = %v, want exactly [aero-id] (one per failed attempt, normalized label)", metrics.scopeMissing)
	}
	if len(metrics.outcomes) != 1 || metrics.outcomes[0] != "pending" {
		t.Fatalf("outcome observations = %v, want [pending]", metrics.outcomes)
	}
	if len(repo.finishes) != 1 || repo.finishes[0] != "pending" {
		t.Fatalf("finishes = %v, want [pending]", repo.finishes)
	}
	if len(repo.codes) != 1 || repo.codes[0] != "audit.binding_unavailable" {
		t.Fatalf("error codes = %v, want [audit.binding_unavailable]", repo.codes)
	}
	if len(repo.attempts) != 1 || repo.attempts[0] != 1 {
		t.Fatalf("persisted attempts = %v, want [1]", repo.attempts)
	}
}

// TestDrainPublicationsEarlyReturnObservesMetric (FM-8): with the publication
// repository unwired, drainPublications returns early but now emits the
// drain-skipped counter — the stall is metered, not silent.
func TestDrainPublicationsEarlyReturnObservesMetric(t *testing.T) {
	metrics := &metricSpy{}
	svc := newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.metrics = metrics
	if svc.publications != nil {
		t.Fatal("test precondition: publications must be nil")
	}

	svc.drainPublications(t.Context())

	if metrics.drainSkipped != 1 {
		t.Fatalf("drain skipped observed = %d, want 1", metrics.drainSkipped)
	}
	if len(metrics.outcomes) != 0 {
		t.Fatalf("outcomes = %v, want none (nothing was claimed)", metrics.outcomes)
	}
}
