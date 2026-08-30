package outbox

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// purgeRecordingRepo records every PurgeDeadLetters call and simulates deletion
// with the exact SQL predicate of the real cleaner (dead_letter past cutoff OR
// published past publishedCutoff), so a recorded call has observable row
// consequences and a broken recorder fails the positive branch first.
type purgeRecordingRepo struct {
	mu       sync.Mutex
	calls    []purgeCall
	deadRows []*Event // dead-letter rows (dead_letter = true)
	pubRows  []*Event // published rows (published_at set)
}

type purgeCall struct {
	deadCutoff      time.Time
	publishedCutoff time.Time
	batchSize       int
}

func newPurgeRecordingRepo(dead, published []*Event) *purgeRecordingRepo {
	return &purgeRecordingRepo{deadRows: dead, pubRows: published}
}

func (r *purgeRecordingRepo) Append(context.Context, *gorm.DB, *Event) error { return nil }

func (r *purgeRecordingRepo) ClaimBatch(context.Context, int) ([]*ClaimedEvent, error) {
	return nil, nil
}

func (r *purgeRecordingRepo) GetEligibilityStats(context.Context) (EligibilityStats, error) {
	return EligibilityStats{}, nil
}

func (r *purgeRecordingRepo) MarkPublished(context.Context, []Claim) error { return nil }

func (r *purgeRecordingRepo) MarkFailed(context.Context, Claim, int, int, time.Time) error {
	return nil
}

func (r *purgeRecordingRepo) Release(context.Context, []Claim) error { return nil }

func (r *purgeRecordingRepo) RenewClaims(context.Context, []Claim) error { return nil }

func (r *purgeRecordingRepo) PurgeDeadLetters(_ context.Context, deadCutoff, publishedCutoff time.Time, batchSize int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, purgeCall{deadCutoff: deadCutoff, publishedCutoff: publishedCutoff, batchSize: batchSize})
	var deleted int64
	keptDead := make([]*Event, 0, len(r.deadRows))
	for _, e := range r.deadRows {
		if e.CreatedAt.Before(deadCutoff) {
			deleted++
		} else {
			keptDead = append(keptDead, e)
		}
	}
	r.deadRows = keptDead
	keptPub := make([]*Event, 0, len(r.pubRows))
	for _, e := range r.pubRows {
		if e.CreatedAt.Before(publishedCutoff) {
			deleted++
		} else {
			keptPub = append(keptPub, e)
		}
	}
	r.pubRows = keptPub
	return deleted, nil
}

func (r *purgeRecordingRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *purgeRecordingRepo) deadLetterCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.deadRows)
}

func purgeEvent(dead bool, age time.Duration) *Event {
	e := &Event{ID: uuid.New(), EventType: "message.sent", CreatedAt: time.Now().UTC().Add(-age)}
	if dead {
		e.DeadLetter = true
	} else {
		published := time.Now().UTC().Add(-age)
		e.PublishedAt = &published
	}
	return e
}

func cleanerFor(t *testing.T, repo Repository, deadRet, pubRet time.Duration, paused bool) *outboxCleaner {
	t.Helper()
	return &outboxCleaner{repo: repo, deadLetterRetention: deadRet, publishedRetention: pubRet, purgePaused: paused}
}

// TestCleanerTick_PausedSkips (T-5.2 case 1): a paused tick performs no purge
// work (no SELECT/DELETE) and preserves every row; the unpaused twin on the
// same fake proves the recorder is live and the cutoffs are exact.
func TestCleanerTick_PausedSkips(t *testing.T) {
	fixed := time.Now().UTC().Truncate(time.Second)
	repo := newPurgeRecordingRepo(
		[]*Event{
			purgeEvent(true, 10*24*time.Hour), // dead letter past retention
			purgeEvent(true, time.Hour),       // dead letter inside retention
		},
		[]*Event{
			purgeEvent(false, 40*24*time.Hour), // published past retention
			purgeEvent(false, time.Hour),       // published inside retention
		},
	)
	before := repo.deadLetterCount()

	// Paused: no purge work at all.
	deleted, err := tickOnce(context.Background(), cleanerFor(t, repo, 7*24*time.Hour, 30*24*time.Hour, true), fixed)
	if err != nil || deleted != 0 {
		t.Fatalf("paused tick = %d, %v; want 0, nil", deleted, err)
	}
	if n := repo.callCount(); n != 0 {
		t.Fatalf("purge calls while paused = %d, want 0", n)
	}
	if repo.deadLetterCount() != before {
		t.Fatal("paused tick mutated rows")
	}

	// Positive control: same fake, unpaused, same seed — exactly one call with
	// the injected-now-derived cutoffs; the two past-retention rows are gone.
	deleted, err = tickOnce(context.Background(), cleanerFor(t, repo, 7*24*time.Hour, 30*24*time.Hour, false), fixed)
	if err != nil || deleted != 2 {
		t.Fatalf("unpaused tick = %d, %v; want 2, nil", deleted, err)
	}
	if n := repo.callCount(); n != 1 {
		t.Fatalf("purge calls = %d, want 1", n)
	}
	wantDead := fixed.Add(-7 * 24 * time.Hour)
	wantPub := fixed.Add(-30 * 24 * time.Hour)
	call := repo.calls[0]
	if !call.deadCutoff.Equal(wantDead) || !call.publishedCutoff.Equal(wantPub) {
		t.Fatalf("cutoffs = %v/%v, want %v/%v", call.deadCutoff, call.publishedCutoff, wantDead, wantPub)
	}
	if repo.deadLetterCount() != 1 {
		t.Fatalf("dead-letter rows after unpaused tick = %d, want 1 (in-retention kept)", repo.deadLetterCount())
	}
}

func TestCleanerTick_ZeroPublishedRetentionDisablesOnlyPublishedLeg(t *testing.T) {
	fixed := time.Now().UTC().Truncate(time.Second)
	repo := newPurgeRecordingRepo(
		[]*Event{purgeEvent(true, 10*24*time.Hour)},
		[]*Event{purgeEvent(false, 40*24*time.Hour)},
	)
	deleted, err := tickOnce(context.Background(), cleanerFor(t, repo, 7*24*time.Hour, 0, false), fixed)
	if err != nil || deleted != 1 {
		t.Fatalf("zero published retention tick = %d, %v; want one dead-letter deletion", deleted, err)
	}
	if len(repo.pubRows) != 1 || repo.callCount() != 1 {
		t.Fatalf("published rows/calls = %d/%d, want 1/1", len(repo.pubRows), repo.callCount())
	}
	if !repo.calls[0].publishedCutoff.IsZero() {
		t.Fatalf("published cutoff = %v, want zero never-purge sentinel", repo.calls[0].publishedCutoff)
	}
}

// TestCleanerLoop_FlagOffPurgesEveryTick (T-5.2 case 2): 3 synthetic ticks
// drive exactly 3 purge calls in order; this is the positive control proving
// the loop is live when the flag is off. The loop runs on a goroutine and is
// stopped by cancelling ctx after the calls are observed (bounded presence
// polling — no sleeps in the assertion path).
func TestCleanerLoop_FlagOffPurgesEveryTick(t *testing.T) {
	repo := newPurgeRecordingRepo(
		[]*Event{purgeEvent(true, 10*24*time.Hour), purgeEvent(true, 10*24*time.Hour)},
		nil,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		runCleanerLoop(ctx, ticks, zap.NewNop(), cleanerFor(t, repo, 7*24*time.Hour, 30*24*time.Hour, false))
		close(done)
	}()
	for i := 0; i < 3; i++ {
		select {
		case ticks <- time.Now().UTC():
		case <-done:
			t.Fatal("cleaner loop exited before all ticks were consumed")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for repo.callCount() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if n := repo.callCount(); n != 3 {
		t.Fatalf("purge calls = %d, want 3", n)
	}
	if repo.deadLetterCount() != 0 {
		t.Fatalf("dead-letter rows after 3 unpaused ticks = %d, want 0", repo.deadLetterCount())
	}
}

// TestCleanerLoop_PausedSkipsEveryTick (T-5.2 case 3): the paused twin of case
// 2 — 3 ticks, 0 purge calls, rows bit-for-bit preserved. The pairing makes
// the zero meaningful (the loop demonstrably records in the sibling test).
func TestCleanerLoop_PausedSkipsEveryTick(t *testing.T) {
	repo := newPurgeRecordingRepo(
		[]*Event{purgeEvent(true, 10*24*time.Hour), purgeEvent(true, 10*24*time.Hour)},
		nil,
	)
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		runCleanerLoop(ctx, ticks, zap.NewNop(), cleanerFor(t, repo, 7*24*time.Hour, 30*24*time.Hour, true))
		close(done)
	}()
	for i := 0; i < 3; i++ {
		select {
		case ticks <- time.Now().UTC():
		case <-done:
			t.Fatal("cleaner loop exited before all ticks were consumed")
		}
	}
	// Give the loop a bounded window to (wrongly) purge, then stop it.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if n := repo.callCount(); n != 0 {
		t.Fatalf("purge calls while paused = %d, want 0", n)
	}
	if repo.deadLetterCount() != 2 {
		t.Fatalf("dead-letter rows after paused ticks = %d, want 2 (nothing lost)", repo.deadLetterCount())
	}
}

// TestRunOutboxCleaner_PublicAPI (T-5.2 case 4): the public entry starts the
// goroutine (bounded presence polling) and stop returns promptly; the paused
// twin asserts zero calls over the same window — valid only because the
// unpaused twin already proved the goroutine starts.
func TestRunOutboxCleaner_PublicAPI(t *testing.T) {
	repo := newPurgeRecordingRepo([]*Event{purgeEvent(true, 10*24*time.Hour)}, nil)
	stop := RunOutboxCleaner(repo, zap.NewNop(), 7*24*time.Hour, 30*24*time.Hour, time.Millisecond, false)
	deadline := time.Now().Add(2 * time.Second)
	for repo.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	stop() // second stop must not panic
	if repo.callCount() == 0 {
		t.Fatal("unpaused cleaner never purged")
	}

	paused := newPurgeRecordingRepo([]*Event{purgeEvent(true, 10*24*time.Hour)}, nil)
	stopPaused := RunOutboxCleaner(paused, zap.NewNop(), 7*24*time.Hour, 30*24*time.Hour, time.Millisecond, true)
	time.Sleep(50 * time.Millisecond)
	stopPaused()
	if paused.callCount() != 0 {
		t.Fatalf("paused cleaner purged %d times, want 0", paused.callCount())
	}
}

// TestCleanerLoop_ReplayVsPurge (T-5.2 case 5): the race the pause flag exists
// to prevent. An unpaused tick purges rows a replay worker snapshotted before
// it requeues them (hazard); with purgePaused=true the same interleaving
// requeues every snapshot row with zero deletions. Interleaving is enforced
// with channels (no sleeps); the worker is a real goroutine so -race covers it.
func TestCleanerLoop_ReplayVsPurge(t *testing.T) {
	run := func(paused bool) (survived int, purgeRan bool) {
		repo := newPurgeRecordingRepo(
			[]*Event{purgeEvent(true, 10*24*time.Hour), purgeEvent(true, 10*24*time.Hour)},
			nil,
		)
		ctx := context.Background()
		fixed := time.Now().UTC().Truncate(time.Second)
		workerDone := make(chan struct{})
		requeueReady := make(chan struct{})
		purgeDone := make(chan struct{})

		// Replay worker: snapshot the dead-letter set, signal, then requeue
		// (dead_letter = false on the snapshot IDs) once the purge tick ran.
		go func() {
			defer close(workerDone)
			repo.mu.Lock()
			snapshot := append([]*Event(nil), repo.deadRows...)
			repo.mu.Unlock()
			close(requeueReady)
			<-purgeDone
			repo.mu.Lock()
			kept := repo.deadRows[:0]
			for _, e := range snapshot {
				for _, row := range repo.deadRows {
					if row.ID == e.ID {
						kept = append(kept, e)
						break
					}
				}
			}
			repo.deadRows = kept
			repo.mu.Unlock()
		}()

		<-requeueReady
		_, _ = tickOnce(ctx, cleanerFor(t, repo, 7*24*time.Hour, 30*24*time.Hour, paused), fixed)
		close(purgeDone)
		<-workerDone
		return repo.deadLetterCount(), repo.callCount() > 0
	}

	// Unpaused — hazard demonstration: the purge deletes the snapshotted rows
	// before the worker requeues them; 0 rows survive.
	if survived, purgeRan := run(false); !purgeRan || survived != 0 {
		t.Fatalf("unpaused interleaving: survived=%d purgeRan=%v; want 0/true (hazard reproduced)", survived, purgeRan)
	}

	// Paused — protection invariant: no purge ran, every snapshot row survived.
	for i := 0; i < 50; i++ {
		survived, purgeRan := run(true)
		if purgeRan || survived != 2 {
			t.Fatalf("paused interleaving #%d: survived=%d purgeRan=%v; want 2/false", i, survived, purgeRan)
		}
	}
}
