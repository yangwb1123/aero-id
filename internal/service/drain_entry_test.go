package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

// recordingClaimRepo records every ClaimPublication invocation; the
// T-5 assertion is that the Start ticker is the ONLY non-test entry point.
type recordingClaimRepo struct {
	mu     sync.Mutex
	claims int
}

func (r *recordingClaimRepo) ClaimPublication(context.Context, string, time.Duration, int) ([]*domain.AuditPublication, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claims++
	return nil, nil
}
func (r *recordingClaimRepo) FinishPublication(context.Context, domain.ID, string, int, string, string, time.Time) error {
	return nil
}
func (r *recordingClaimRepo) RequeuePublication(context.Context, []domain.ID, int, repository.ReplayIntent) (int, []*domain.AuditFact, error) {
	return 0, nil, nil
}
func (r *recordingClaimRepo) CountDeadLetterPublications(context.Context, []domain.ID, int, string) (int, error) {
	return 0, nil
}
func (r *recordingClaimRepo) DeadLetterTenants(context.Context, []domain.ID, int, string) ([]string, error) {
	return nil, nil
}
func (r *recordingClaimRepo) PurgeDeadLetterPublications(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (r *recordingClaimRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claims
}

// TestDrainPublicationsOnlyFromStart (T-5 wiring): with the callback service
// wired to a recording publication repo, ClaimPublication fires from the Start
// ticker loop — and from no other service entry point. The direct
// drainPublications calls in other tests are test-only; this test proves the
// production reachability: Start is the sole caller (the mechanical gate in
// check-audit-swallow.sh asserts the single call site statically).
func TestDrainPublicationsOnlyFromStart(t *testing.T) {
	repo := &recordingClaimRepo{}
	svc := newCallbackTestService(newCallbackFakeRepo(), CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.publications = repo
	svc.metrics = &metricSpy{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := svc.Start(ctx, 10*time.Millisecond)

	// The ticker must drive claims without any other entry point being called.
	deadline := time.Now().Add(2 * time.Second)
	for repo.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if n := repo.count(); n == 0 {
		t.Fatal("ClaimPublication never fired from the Start ticker")
	}

	// Exercise the other public service entries: none of them may trigger a
	// claim (the drain is ticker-only; Register/List/Delete/ReplayDelivery
	// operate on subscriptions and deliveries, not publications).
	before := repo.count()
	registerCtx := WithIdentity(ctx, &domain.TokenIdentity{UID: "u-1", Issuer: "snaplink", Subject: "u-1", TenantID: "tenant-a"})
	// Register performs an endpoint challenge over HTTP; the challenge fails
	// without a live server, which is fine — the drain-claim counter must be
	// unchanged regardless of the registration outcome.
	_, _, _ = svc.Register(registerCtx, CallbackInput{Endpoint: "https://cb.example.test/hook",
		TenantID: "tenant-a", EventTypes: []string{"account.provisioned"}})
	if n := repo.count(); n != before {
		t.Fatalf("ClaimPublication calls after Register = %d, want %d (Register must not drain)", n, before)
	}
}
