package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

// recordingPurgePublicationRepo records purge scan calls; the drain-loop purge
// scan is the only caller in production.
type recordingPurgePublicationRepo struct {
	purges []time.Time
	fail   error
}

func (r *recordingPurgePublicationRepo) ClaimPublication(context.Context, string, time.Duration, int) ([]*domain.AuditPublication, error) {
	return nil, nil
}
func (r *recordingPurgePublicationRepo) FinishPublication(context.Context, domain.ID, string, int, string, string, time.Time) error {
	return nil
}
func (r *recordingPurgePublicationRepo) RequeuePublication(context.Context, []domain.ID, int, repository.ReplayIntent) (int, []*domain.AuditFact, error) {
	return 0, nil, nil
}
func (r *recordingPurgePublicationRepo) CountDeadLetterPublications(context.Context, []domain.ID, int, string) (int, error) {
	return 0, nil
}
func (r *recordingPurgePublicationRepo) DeadLetterTenants(context.Context, []domain.ID, int, string) ([]string, error) {
	return nil, nil
}
func (r *recordingPurgePublicationRepo) PurgeDeadLetterPublications(_ context.Context, cutoff time.Time, limit int) (int64, error) {
	if r.fail != nil {
		return 0, r.fail
	}
	r.purges = append(r.purges, cutoff)
	return 2, nil
}

// recordingPurgeCallbackRepo is a callbackFakeRepo whose purge methods record
// instead of failing.
type recordingPurgeCallbackRepo struct {
	callbackFakeRepo
	purges []time.Time
}

func (r *recordingPurgeCallbackRepo) PurgeDeadLetterDeliveries(_ context.Context, cutoff time.Time, limit int) (int64, error) {
	r.purges = append(r.purges, cutoff)
	return 1, nil
}

// TestPurgeDeadLettersScanSkipsWhenDisabled (T-4.3): retention <= 0 (default)
// and purge_pause both skip the scan entirely — no SELECT/DELETE, no metric.
func TestPurgeDeadLettersScanSkipsWhenDisabled(t *testing.T) {
	pubRepo := &recordingPurgePublicationRepo{}
	cbRepo := &recordingPurgeCallbackRepo{}
	metrics := &metricSpy{}

	svc := newCallbackTestService(cbRepo, CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.publications = pubRepo
	svc.metrics = metrics

	// Default: retention 0 => never purge.
	svc.purgeDeadLetters(t.Context())
	if len(pubRepo.purges) != 0 || len(cbRepo.purges) != 0 {
		t.Fatalf("purge scans with retention 0 = %d/%d, want none", len(pubRepo.purges), len(cbRepo.purges))
	}
	if len(metrics.purged) != 0 {
		t.Fatalf("purge metrics with retention 0 = %v, want none", metrics.purged)
	}

	// purge_pause: even with a positive retention the scan never runs.
	svc.cfg.DeadLetterRetention = 24 * time.Hour
	svc.cfg.PurgePause = true
	svc.purgeDeadLetters(t.Context())
	if len(pubRepo.purges) != 0 || len(cbRepo.purges) != 0 {
		t.Fatalf("purge scans while paused = %d/%d, want none", len(pubRepo.purges), len(cbRepo.purges))
	}
	if len(metrics.purged) != 0 {
		t.Fatalf("purge metrics while paused = %v, want none", metrics.purged)
	}
}

// TestPurgeDeadLettersScanPurgesAndObserves (T-4.3 / MET-1): with a positive
// retention and no pause, one bounded scan per kind runs and the purged
// counter is emitted per kind.
func TestPurgeDeadLettersScanPurgesAndObserves(t *testing.T) {
	pubRepo := &recordingPurgePublicationRepo{}
	cbRepo := &recordingPurgeCallbackRepo{}
	metrics := &metricSpy{}

	svc := newCallbackTestService(cbRepo, CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.publications = pubRepo
	svc.metrics = metrics
	svc.cfg.DeadLetterRetention = 24 * time.Hour

	svc.purgeDeadLetters(t.Context())

	if len(pubRepo.purges) != 1 || len(cbRepo.purges) != 1 {
		t.Fatalf("purge scans = %d/%d, want 1/1", len(pubRepo.purges), len(cbRepo.purges))
	}
	want := time.Now().UTC().Add(-24 * time.Hour)
	for name, got := range map[string]time.Time{"publication": pubRepo.purges[0], "delivery": cbRepo.purges[0]} {
		if delta := got.Sub(want); delta > time.Minute || delta < -time.Minute {
			t.Fatalf("%s cutoff = %v, want ≈ %v", name, got, want)
		}
	}
	if len(metrics.purged) != 2 {
		t.Fatalf("purge metrics = %v, want publication+delivery", metrics.purged)
	}
}

// TestPurgeDeadLettersScanFailureObserved (never silent): a purge scan error
// is observed on the purge-failure counter and does not abort the other kind.
func TestPurgeDeadLettersScanFailureObserved(t *testing.T) {
	pubRepo := &recordingPurgePublicationRepo{fail: errors.New("purge scan failed")}
	cbRepo := &recordingPurgeCallbackRepo{}
	metrics := &metricSpy{}

	svc := newCallbackTestService(cbRepo, CallbackConfig{MaxAttempts: 8, RetryBase: time.Second}, nil)
	svc.publications = pubRepo
	svc.metrics = metrics
	svc.cfg.DeadLetterRetention = 24 * time.Hour

	svc.purgeDeadLetters(t.Context())

	if len(metrics.purgeFailures) != 1 || metrics.purgeFailures[0] != "publication" {
		t.Fatalf("purge failures = %v, want [publication]", metrics.purgeFailures)
	}
	if len(cbRepo.purges) != 1 {
		t.Fatalf("delivery purge after publication failure = %d, want 1 (kinds are independent)", len(cbRepo.purges))
	}
}
