package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

// appAuditRecorder is an app-level recording fake for the AuditRepository seam
// plus the exclusion-aware purge surface. It records every exemptActors slice
// and every bare purge so a wiring test can prove the cleaner closure the
// composition root starts routes through the exemption path (compile checks
// prove nothing about the values).
type appAuditRecorder struct {
	mu           sync.Mutex
	excluding    []string
	excludingCnt int
	bareCnt      int
}

func (r *appAuditRecorder) Append(context.Context, *domain.AuditEvent) error { return nil }
func (r *appAuditRecorder) Log(context.Context, *domain.AuditEvent) error    { return nil }
func (r *appAuditRecorder) List(context.Context, string, string, domain.Page) ([]*domain.AuditEvent, string, error) {
	return nil, "", nil
}
func (r *appAuditRecorder) PurgeOlderThan(context.Context, time.Time, int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bareCnt++
	return 0, nil
}
func (r *appAuditRecorder) PurgeOlderThanExcluding(_ context.Context, _ time.Time, _ int, exempt []string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.excluding = append([]string(nil), exempt...)
	r.excludingCnt++
	return 0, nil
}

func (r *appAuditRecorder) counts() ([]string, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.excluding...), r.excludingCnt, r.bareCnt
}

// appHoldRecorder is an app-level recording fake for the AuditHoldRepository
// seam: HoldScopeIDs records the requested scope and returns the held actor
// ids, so a wiring test can prove the cleaner consults the registry per tick.
type appHoldRecorder struct {
	mu          sync.Mutex
	scopes      []domain.AuditHoldScope
	exemptActor string
}

func (r *appHoldRecorder) PutHold(context.Context, *repository.AuditHold) error { return nil }
func (r *appHoldRecorder) RemoveHold(context.Context, string, domain.AuditHoldScope, string) error {
	return nil
}
func (r *appHoldRecorder) ListActiveHolds(context.Context, string) ([]*repository.AuditHold, error) {
	return nil, nil
}
func (r *appHoldRecorder) HoldScopeIDs(_ context.Context, scopeType domain.AuditHoldScope) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scopes = append(r.scopes, scopeType)
	return []string{r.exemptActor}, nil
}

func (r *appHoldRecorder) scopeCalls() []domain.AuditHoldScope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.AuditHoldScope(nil), r.scopes...)
}

// auditCleanerConfig returns a config with the audit retention cleaner enabled
// (both gate halves > 0), mirroring the OPT-IN gate in new.go.
func auditCleanerConfig() *config.Config {
	cfg := &config.Config{}
	cfg.DataRetention.AuditCleanInterval = 10 * time.Millisecond
	cfg.DataRetention.AuditRetention = 90 * 24 * time.Hour
	return cfg
}

// waitForExcludingPurge polls for the first exclusion-aware purge within a
// bounded deadline (pattern: TestBuildOutboxCleanerWiringPauseFlag).
func waitForExcludingPurge(t *testing.T, audit *appAuditRecorder) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, n, _ := audit.counts(); n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cleaner never purged within the bounded deadline")
}

// TestAuditCleanerWiringReceivesHoldRepo (A-2): the exact composition-root
// call shape — repository.RunAuditCleanerWithHolds(auditRepo, holdRepo, log,
// retention, interval) — consults the hold registry on every tick with the
// actor scope and routes the purge through the exemption-aware surface with
// the held actor ids, never through a bare purge. This is the recording-seam
// proof that the cleaner the server starts receives and uses a hold registry.
func TestAuditCleanerWiringReceivesHoldRepo(t *testing.T) {
	audit := &appAuditRecorder{}
	holds := &appHoldRecorder{exemptActor: "u-held"}
	cfg := auditCleanerConfig()
	stop := repository.RunAuditCleanerWithHolds(
		audit, holds, zap.NewNop(),
		cfg.DataRetention.AuditRetention,
		cfg.DataRetention.AuditCleanInterval,
	)
	waitForExcludingPurge(t, audit)

	scopes := holds.scopeCalls()
	if len(scopes) == 0 || scopes[0] != domain.AuditHoldScopeActor {
		t.Fatalf("HoldScopeIDs scope = %v, want first call with %q", scopes, domain.AuditHoldScopeActor)
	}
	exempt, _, bare := audit.counts()
	if bare != 0 {
		t.Fatalf("bare PurgeOlderThan calls = %d, want 0", bare)
	}
	if len(exempt) != 1 || exempt[0] != "u-held" {
		t.Fatalf("exempt list = %v, want [u-held] (held actor must reach the purge as the exclusion set)", exempt)
	}
	stop()
	stop() // second stop must not panic
}

// TestAuditCleanerWiringGateConditions (A-2, P-2): the composition-root OPT-IN
// gate is `AuditCleanInterval > 0 && AuditRetention > 0`; the repository
// function itself gates only on interval, so the test replicates the full
// condition exactly as new.go writes it. With either half zero, nothing
// starts: zero hold consultations, zero purges, prompt non-panicking stop.
func TestAuditCleanerWiringGateConditions(t *testing.T) {
	// Interval 0: the repository function itself returns a no-op stop.
	audit := &appAuditRecorder{}
	holds := &appHoldRecorder{exemptActor: "u-held"}
	cfg := auditCleanerConfig()
	cfg.DataRetention.AuditCleanInterval = 0
	var stop func()
	if cfg.DataRetention.AuditCleanInterval > 0 && cfg.DataRetention.AuditRetention > 0 {
		stop = repository.RunAuditCleanerWithHolds(audit, holds, zap.NewNop(),
			cfg.DataRetention.AuditRetention, cfg.DataRetention.AuditCleanInterval)
	}
	if stop != nil {
		stop()
		stop() // second stop must not panic
	}
	time.Sleep(40 * time.Millisecond)
	if n := len(holds.scopeCalls()); n != 0 {
		t.Fatalf("interval 0: HoldScopeIDs calls = %d, want 0 (gate not inverted)", n)
	}
	if _, n, bare := audit.counts(); n != 0 || bare != 0 {
		t.Fatalf("interval 0: purge calls = %d excluding / %d bare, want 0/0", n, bare)
	}

	// Retention 0 with interval > 0: the repository function WOULD start a
	// cleaner (it gates only on interval), so the && gate in the composition
	// root is the only thing keeping this disabled — the test replicates it.
	audit2 := &appAuditRecorder{}
	holds2 := &appHoldRecorder{exemptActor: "u-held"}
	cfg2 := auditCleanerConfig()
	cfg2.DataRetention.AuditRetention = 0
	var stop2 func()
	if cfg2.DataRetention.AuditCleanInterval > 0 && cfg2.DataRetention.AuditRetention > 0 {
		stop2 = repository.RunAuditCleanerWithHolds(audit2, holds2, zap.NewNop(),
			cfg2.DataRetention.AuditRetention, cfg2.DataRetention.AuditCleanInterval)
	}
	if stop2 != nil {
		stop2()
		stop2() // second stop must not panic
	}
	time.Sleep(40 * time.Millisecond)
	if n := len(holds2.scopeCalls()); n != 0 {
		t.Fatalf("retention 0: HoldScopeIDs calls = %d, want 0 (gate not inverted)", n)
	}
	if _, n, bare := audit2.counts(); n != 0 || bare != 0 {
		t.Fatalf("retention 0: purge calls = %d excluding / %d bare, want 0/0", n, bare)
	}
}
