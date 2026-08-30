package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/outbox"
)

// recordingCleanerRepo is an app-level recording fake for the outbox
// Repository seam: it records every PurgeDeadLetters call so a wiring test can
// prove the flag value reaches the cleaner (T-5.3 — compile checks prove
// nothing about the value).
type recordingCleanerRepo struct {
	mu     sync.Mutex
	purges int
}

func (r *recordingCleanerRepo) Append(context.Context, *gorm.DB, *outbox.Event) error { return nil }
func (r *recordingCleanerRepo) ClaimBatch(context.Context, int) ([]*outbox.ClaimedEvent, error) {
	return nil, nil
}
func (r *recordingCleanerRepo) GetEligibilityStats(context.Context) (outbox.EligibilityStats, error) {
	return outbox.EligibilityStats{}, nil
}
func (r *recordingCleanerRepo) MarkPublished(context.Context, []outbox.Claim) error { return nil }
func (r *recordingCleanerRepo) MarkFailed(context.Context, outbox.Claim, int, int, time.Time) error {
	return nil
}
func (r *recordingCleanerRepo) Release(context.Context, []outbox.Claim) error     { return nil }
func (r *recordingCleanerRepo) RenewClaims(context.Context, []outbox.Claim) error { return nil }
func (r *recordingCleanerRepo) PurgeDeadLetters(context.Context, time.Time, time.Time, int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purges++
	return 0, nil
}

func (r *recordingCleanerRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.purges
}

func outboxCleanerConfig(paused bool) *config.Config {
	cfg := &config.Config{}
	cfg.DataRetention.OutboxCleanInterval = 10 * time.Millisecond
	cfg.DataRetention.OutboxDeadLetterRetention = 168 * time.Hour
	cfg.DataRetention.OutboxPublishedRetention = 720 * time.Hour
	cfg.DataRetention.PurgePaused = paused
	return cfg
}

// TestBuildOutboxCleanerWiringPauseFlag (T-5.3): the purge_paused value
// reaches the cleaner — a paused cleaner performs zero purge calls over a
// bounded window while its unpaused twin (same fake, same config shape)
// demonstrably purges, so the zero is meaningful.
func TestBuildOutboxCleanerWiringPauseFlag(t *testing.T) {
	pausedRepo := &recordingCleanerRepo{}
	stop := buildOutboxCleaner(outboxCleanerConfig(true), zap.NewNop(), pausedRepo)
	time.Sleep(60 * time.Millisecond)
	stop()
	stop() // second stop must not panic
	if n := pausedRepo.callCount(); n != 0 {
		t.Fatalf("paused cleaner purge calls = %d, want 0", n)
	}

	unpausedRepo := &recordingCleanerRepo{}
	stop2 := buildOutboxCleaner(outboxCleanerConfig(false), zap.NewNop(), unpausedRepo)
	deadline := time.Now().Add(2 * time.Second)
	for unpausedRepo.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop2()
	if n := unpausedRepo.callCount(); n == 0 {
		t.Fatal("unpaused cleaner never purged — the recorder is live, so the paused zero above is meaningful")
	}
}

// TestBuildOutboxCleanerGateConditions (T-5.3): interval 0 disables the
// cleaner, while a zero dead-letter retention still wires a cleaner when the
// published-retention leg remains active.
func TestBuildOutboxCleanerGateConditions(t *testing.T) {
	repo := &recordingCleanerRepo{}
	cfg := outboxCleanerConfig(false)
	cfg.DataRetention.OutboxCleanInterval = 0
	stop := buildOutboxCleaner(cfg, zap.NewNop(), repo)
	time.Sleep(40 * time.Millisecond)
	stop()
	if n := repo.callCount(); n != 0 {
		t.Fatalf("cleaner with interval 0 purged %d times, want 0 (gate not inverted)", n)
	}

	cfg2 := outboxCleanerConfig(false)
	cfg2.DataRetention.OutboxDeadLetterRetention = 0
	publishedRepo := &recordingCleanerRepo{}
	stop2 := buildOutboxCleaner(cfg2, zap.NewNop(), publishedRepo)
	deadline := time.Now().Add(2 * time.Second)
	for publishedRepo.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop2()
	if n := publishedRepo.callCount(); n == 0 {
		t.Fatal("cleaner with dead-letter retention 0 did not run the published-retention leg")
	}
}
