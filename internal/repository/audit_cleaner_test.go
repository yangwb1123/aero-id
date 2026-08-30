package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/domain"
)

// recordingAuditRepo is a deterministic fake for the AuditRepository seam plus
// the exclusion-aware purge surface. It records every exemptActors slice it
// receives and every bare PurgeOlderThan call, so a test can prove exactly
// which purge path the cleaner routes through and what exemption set reaches
// it (the routing type-assert in purgeAuditWithExempt needs the excluding
// surface present; without it the cleaner would fall back to a full purge).
type recordingAuditRepo struct {
	mu              sync.Mutex
	excluding       []string
	barePurges      int
	excludingPurges int
}

func (r *recordingAuditRepo) Append(context.Context, *domain.AuditEvent) error { return nil }
func (r *recordingAuditRepo) Log(context.Context, *domain.AuditEvent) error    { return nil }
func (r *recordingAuditRepo) List(context.Context, string, string, domain.Page) ([]*domain.AuditEvent, string, error) {
	return nil, "", nil
}
func (r *recordingAuditRepo) PurgeOlderThan(context.Context, time.Time, int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.barePurges++
	return 0, nil
}

// PurgeOlderThanExcluding is the exemption-aware purge surface the cleaner's
// routing type-assert looks for (audit.go purgeAuditWithExempt).
func (r *recordingAuditRepo) PurgeOlderThanExcluding(_ context.Context, _ time.Time, _ int, exempt []string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.excluding = append([]string(nil), exempt...)
	r.excludingPurges++
	return 0, nil
}

// excludingCalls returns the recorded exemption list from the most recent
// exclusion-aware purge and the total number of such purges.
func (r *recordingAuditRepo) excludingCalls() ([]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.excluding...), r.excludingPurges
}

func (r *recordingAuditRepo) bareCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.barePurges
}

// recordingHoldRepo is a deterministic fake for the AuditHoldRepository seam.
// HoldScopeIDs records the requested scope and returns the held actor ids, so
// a test can prove the cleaner consults the registry with the actor scope.
type recordingHoldRepo struct {
	mu          sync.Mutex
	scopes      []domain.AuditHoldScope
	exemptActor string
}

func (r *recordingHoldRepo) PutHold(context.Context, *AuditHold) error { return nil }
func (r *recordingHoldRepo) RemoveHold(context.Context, string, domain.AuditHoldScope, string) error {
	return nil
}
func (r *recordingHoldRepo) ListActiveHolds(context.Context, string) ([]*AuditHold, error) {
	return nil, nil
}
func (r *recordingHoldRepo) HoldScopeIDs(_ context.Context, scopeType domain.AuditHoldScope) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scopes = append(r.scopes, scopeType)
	return []string{r.exemptActor}, nil
}

func (r *recordingHoldRepo) scopeCalls() []domain.AuditHoldScope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.AuditHoldScope(nil), r.scopes...)
}

// waitForPurge polls for the first exclusion-aware purge within a bounded
// deadline (pattern: TestBuildOutboxCleanerWiringPauseFlag) so the ticker
// timing never makes the test flaky.
func waitForPurge(t *testing.T, repo *recordingAuditRepo) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, n := repo.excludingCalls(); n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cleaner never purged within the bounded deadline")
}

// TestRunAuditCleanerWithHoldsExemptsHeldActors (P4-07, unit): the hold-aware
// cleaner consults the hold registry on every tick with the actor scope and
// routes the purge through the exclusion-aware surface, carrying the held
// actor ids as the exemption set — so the held actor's rows are never passed
// to the purge for deletion. Mirrors audit_hold_integration_test.go
// (TestLegalHoldExemptsActorsFromPurge) without Postgres.
func TestRunAuditCleanerWithHoldsExemptsHeldActors(t *testing.T) {
	audit := &recordingAuditRepo{}
	holds := &recordingHoldRepo{exemptActor: "u-held"}
	stop := RunAuditCleanerWithHolds(audit, holds, zap.NewNop(), 90*24*time.Hour, 10*time.Millisecond)
	waitForPurge(t, audit)

	scopes := holds.scopeCalls()
	if len(scopes) == 0 || scopes[0] != domain.AuditHoldScopeActor {
		t.Fatalf("HoldScopeIDs scope = %v, want first call with %q", scopes, domain.AuditHoldScopeActor)
	}
	if n := audit.bareCallCount(); n != 0 {
		t.Fatalf("bare PurgeOlderThan calls = %d, want 0 (exemption-aware purge must be used)", n)
	}
	exempt, n := audit.excludingCalls()
	if n == 0 {
		t.Fatal("no exclusion-aware purge observed")
	}
	if len(exempt) != 1 || exempt[0] != "u-held" {
		t.Fatalf("exempt list = %v, want [u-held] (held actor must reach the purge as the exclusion set)", exempt)
	}

	stop()
	stop() // second stop must not panic (idempotent stop, R-5)
}

// TestRunAuditCleanerNilHoldsFullPurges documents the exact bug this wiring
// direction fixes: with a nil hold registry the cleaner still routes through
// the exclusion-aware surface (when the repo supports it) but with an empty
// exemption list — the concrete repo's NOT IN guard then deletes every
// retention-expired row, held or not. The composition root must never run the
// cleaner this way (pinned by C-6 in check-composition-root.sh).
func TestRunAuditCleanerNilHoldsFullPurges(t *testing.T) {
	audit := &recordingAuditRepo{}
	stop := RunAuditCleaner(audit, zap.NewNop(), 90*24*time.Hour, 10*time.Millisecond)
	waitForPurge(t, audit)

	exempt, n := audit.excludingCalls()
	if n == 0 {
		t.Fatal("no purge observed")
	}
	if len(exempt) != 0 {
		t.Fatalf("nil-holds exempt list = %v, want empty (empty exemption means a full purge with no NOT IN clause)", exempt)
	}
	if audit.bareCallCount() != 0 {
		t.Fatalf("bare PurgeOlderThan calls = %d, want 0 (excluding surface exists, so routing goes through it)", audit.bareCallCount())
	}
	stop()
	stop()
}

// TestRunAuditCleanerDisabled: a non-positive interval returns an immediate
// no-op stop; no hold consultation and no purge happen over a bounded window.
func TestRunAuditCleanerDisabled(t *testing.T) {
	audit := &recordingAuditRepo{}
	holds := &recordingHoldRepo{exemptActor: "u-held"}
	stop := RunAuditCleanerWithHolds(audit, holds, zap.NewNop(), 90*24*time.Hour, 0)
	stop()
	stop() // second stop must not panic
	time.Sleep(40 * time.Millisecond)
	if n := len(holds.scopeCalls()); n != 0 {
		t.Fatalf("HoldScopeIDs calls = %d, want 0 (disabled cleaner must not consult the hold registry)", n)
	}
	if _, n := audit.excludingCalls(); n != 0 {
		t.Fatalf("purge calls = %d, want 0 (disabled cleaner must not purge)", n)
	}
	if n := audit.bareCallCount(); n != 0 {
		t.Fatalf("bare purge calls = %d, want 0", n)
	}
}
