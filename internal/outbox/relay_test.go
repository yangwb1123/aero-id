package outbox

import (
	"context"
	stderrors "errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// recordingSink captures relay observations for assertions. It satisfies MetricsSink.
type recordingSink struct {
	mu          sync.Mutex
	published   map[string]int
	failed      map[string]int
	lastPending int
	lastOldest  time.Duration
	drainPasses int
}

func newRecordingSink() *recordingSink {
	return &recordingSink{published: map[string]int{}, failed: map[string]int{}}
}

func (s *recordingSink) ObserveOutboxPublished(eventType string) {
	s.mu.Lock()
	s.published[eventType]++
	s.mu.Unlock()
}

func (s *recordingSink) ObserveOutboxFailed(eventType string) {
	s.mu.Lock()
	s.failed[eventType]++
	s.mu.Unlock()
}

func (s *recordingSink) ObserveOutboxDrain(pending int, oldestAge, _ time.Duration) {
	s.mu.Lock()
	s.lastPending = pending
	s.lastOldest = oldestAge
	s.drainPasses++
	s.mu.Unlock()
}

// fakeRepo is an in-memory Repository for relay unit tests. It models durable
// claims: ClaimBatch mints a token per row, MarkFailed/MarkPublished/Release
// honor tokens, and MarkFailed clears the claim (matching the SQL semantics).
// Append is unused here (the relay never appends), so it is a no-op.
type fakeRepo struct {
	mu        sync.Mutex
	events    []*Event
	published map[uuid.UUID]bool
	claims    map[uuid.UUID]uuid.UUID // eventID -> token
	renews    []uuid.UUID             // event IDs renewed, in order
	releases  []uuid.UUID             // event IDs released, in order
}

func newFakeRepo(events ...*Event) *fakeRepo {
	return &fakeRepo{events: events, published: map[uuid.UUID]bool{}, claims: map[uuid.UUID]uuid.UUID{}}
}

func (r *fakeRepo) Append(context.Context, *gorm.DB, *Event) error { return nil }

func (r *fakeRepo) ClaimBatch(_ context.Context, limit int) ([]*ClaimedEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*ClaimedEvent
	for _, e := range r.events {
		if r.published[e.ID] {
			continue
		}
		if e.DeadLetter {
			continue // SQL ClaimBatch contract: only unpublished, non-dead-letter rows
		}
		if _, held := r.claims[e.ID]; held {
			continue // claimed but not yet finalized (lease never expires in-memory)
		}
		token := uuid.New()
		r.claims[e.ID] = token
		out = append(out, &ClaimedEvent{Event: e, Claim: Claim{EventID: e.ID, Token: token}})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *fakeRepo) MarkPublished(_ context.Context, claims []Claim) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lost int
	for _, c := range claims {
		token, held := r.claims[c.EventID]
		if !held || token != c.Token {
			lost++
			continue
		}
		r.published[c.EventID] = true
		delete(r.claims, c.EventID)
	}
	if lost > 0 {
		return fmt.Errorf("mark published: %w (%d of %d claims stale)", ErrClaimLost, lost, len(claims))
	}
	return nil
}

func (r *fakeRepo) MarkFailed(_ context.Context, c Claim, retryCount, maxRetries int, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	token, held := r.claims[c.EventID]
	if !held || token != c.Token {
		return fmt.Errorf("mark failed: %w", ErrClaimLost)
	}
	// Preserve retry state like the SQL path: clear the claim, keep the event
	// unpublished and track retry state so backoff skips work across drains.
	for _, e := range r.events {
		if e.ID == c.EventID {
			e.RetryCount = retryCount
			now := time.Now()
			e.LastRetryAt = &now
			if retryCount >= maxRetries {
				e.DeadLetter = true
			}
		}
	}
	delete(r.claims, c.EventID)
	return nil
}

func (r *fakeRepo) Release(_ context.Context, claims []Claim) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range claims {
		token, held := r.claims[c.EventID]
		if !held || token != c.Token {
			continue
		}
		delete(r.claims, c.EventID)
		r.releases = append(r.releases, c.EventID)
	}
	return nil
}

func (r *fakeRepo) RenewClaims(_ context.Context, claims []Claim) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range claims {
		token, held := r.claims[c.EventID]
		if !held || token != c.Token {
			continue
		}
		r.renews = append(r.renews, c.EventID)
	}
	return nil
}

func (r *fakeRepo) PurgeDeadLetters(_ context.Context, _, _ time.Time, _ int) (int64, error) {
	return 0, nil
}

func (r *fakeRepo) isPublished(id uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.published[id]
}

func (r *fakeRepo) claimsHeld() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.claims)
}

func TestRelay_DrainPublishesEvents(t *testing.T) {
	e1 := &Event{ID: uuid.New(), EventType: "message.sent"}
	e2 := &Event{ID: uuid.New(), EventType: "comment.replied"}
	repo := newFakeRepo(e1, e2)

	var mu sync.Mutex
	var handled []uuid.UUID
	relay := NewRelay(repo, HandlerFunc(func(_ context.Context, e *Event) error {
		mu.Lock()
		handled = append(handled, e.ID)
		mu.Unlock()
		return nil
	}), 0, nil)

	if n := relay.drain(context.Background()); n != 2 {
		t.Fatalf("drain handled %d, want 2", n)
	}
	if len(handled) != 2 {
		t.Fatalf("handler called %d times, want 2", len(handled))
	}
	if !repo.isPublished(e1.ID) || !repo.isPublished(e2.ID) {
		t.Fatal("both events should be marked published")
	}
	// A second tick has nothing left to drain.
	if n := relay.drain(context.Background()); n != 0 {
		t.Fatalf("second drain handled %d, want 0", n)
	}
}

func TestRelay_HandlerErrorLeavesEventUnpublished(t *testing.T) {
	good := &Event{ID: uuid.New(), EventType: "message.sent"}
	bad := &Event{ID: uuid.New(), EventType: "comment.replied"}
	repo := newFakeRepo(good, bad)

	fail := true
	relay := NewRelay(repo, HandlerFunc(func(_ context.Context, e *Event) error {
		if e.ID == bad.ID && fail {
			return stderrors.New("boom")
		}
		return nil
	}), 0, nil)

	// First tick: the failing event stays unpublished; the good one publishes.
	if n := relay.drain(context.Background()); n != 1 {
		t.Fatalf("first drain published %d, want 1", n)
	}
	if !repo.isPublished(good.ID) {
		t.Fatal("good event should be published")
	}
	if repo.isPublished(bad.ID) {
		t.Fatal("failing event must remain unpublished for retry")
	}

	// Next tick with the handler recovered: the previously-failed event drains.
	// Backdate the failure so the event is outside its backoff window.
	due := time.Now().Add(-time.Hour)
	repo.mu.Lock()
	bad.LastRetryAt = &due
	repo.mu.Unlock()
	fail = false
	if n := relay.drain(context.Background()); n != 1 {
		t.Fatalf("retry drain published %d, want 1", n)
	}
	if !repo.isPublished(bad.ID) {
		t.Fatal("event should be published on retry")
	}
}

func TestRelay_MetricsSinkRecordsPublishedFailedAndLag(t *testing.T) {
	// K published + J failed in a single drain pass; the lag gauge reflects the
	// oldest unpublished event's age and pending reflects the batch size.
	oldest := time.Now().Add(-90 * time.Second)
	e1 := &Event{ID: uuid.New(), EventType: "message.sent", CreatedAt: oldest}
	e2 := &Event{ID: uuid.New(), EventType: "message.sent", CreatedAt: time.Now().Add(-30 * time.Second)}
	bad := &Event{ID: uuid.New(), EventType: "comment.replied", CreatedAt: time.Now()}
	repo := newFakeRepo(e1, e2, bad)

	sink := newRecordingSink()
	relay := NewRelay(repo, HandlerFunc(func(_ context.Context, e *Event) error {
		if e.ID == bad.ID {
			return stderrors.New("boom")
		}
		return nil
	}), 0, nil, sink)

	if n := relay.drain(context.Background()); n != 2 {
		t.Fatalf("drain published %d, want 2", n)
	}

	if got := sink.published["message.sent"]; got != 2 {
		t.Fatalf("published[message.sent] = %d, want 2", got)
	}
	if got := sink.failed["comment.replied"]; got != 1 {
		t.Fatalf("failed[comment.replied] = %d, want 1", got)
	}
	if sink.lastPending != 3 {
		t.Fatalf("lastPending = %d, want 3 (batch had 3 unpublished)", sink.lastPending)
	}
	// Oldest event is ~90s old; allow generous slack for test execution time.
	if sink.lastOldest < 80*time.Second {
		t.Fatalf("lastOldest = %v, want >= ~90s", sink.lastOldest)
	}
	if sink.drainPasses != 1 {
		t.Fatalf("drainPasses = %d, want 1", sink.drainPasses)
	}
}

func TestRelay_NilMetricsSinkIsSafe(t *testing.T) {
	e := &Event{ID: uuid.New(), EventType: "message.sent", CreatedAt: time.Now()}
	repo := newFakeRepo(e)
	// No sink passed: drain must not panic and must still publish.
	relay := NewRelay(repo, HandlerFunc(func(context.Context, *Event) error { return nil }), 0, nil)
	if n := relay.drain(context.Background()); n != 1 {
		t.Fatalf("drain published %d, want 1", n)
	}
	// Empty outbox pass is also safe with no sink.
	if n := relay.drain(context.Background()); n != 0 {
		t.Fatalf("empty drain published %d, want 0", n)
	}
}

func TestRelay_DrainOnStop(t *testing.T) {
	e1 := &Event{ID: uuid.New(), EventType: "message.sent"}
	e2 := &Event{ID: uuid.New(), EventType: "message.sent"}
	e3 := &Event{ID: uuid.New(), EventType: "comment.replied"}
	repo := newFakeRepo(e1, e2, e3)

	var mu sync.Mutex
	var handled []uuid.UUID
	relay := NewRelay(repo, HandlerFunc(func(_ context.Context, e *Event) error {
		mu.Lock()
		handled = append(handled, e.ID)
		mu.Unlock()
		return nil
	}), 0, nil)

	stop := relay.Start(24 * time.Hour)
	stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(handled)
		mu.Unlock()
		if n == 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	got := len(handled)
	mu.Unlock()
	if got != 3 {
		t.Fatalf("handler called %d times after stop, want 3 (all pending events drained)", got)
	}
	if !repo.isPublished(e1.ID) || !repo.isPublished(e2.ID) || !repo.isPublished(e3.ID) {
		t.Fatal("all events must be marked published after Stop")
	}
}

// TestRelay_BackstopRelease guards REQ-6/D4: an event inside its backoff window
// is claimed (backoff exclusion is Go-side), the relay's backstop skips it
// without invoking the handler, and Release clears the claim so a later pass
// can pick it up once due.
func TestRelay_BackstopRelease(t *testing.T) {
	due := time.Now().Add(-time.Minute)
	past := time.Now().Add(-time.Second) // inside backoff(1) = 5s window
	dueEvent := &Event{ID: uuid.New(), EventType: "message.sent", CreatedAt: due}
	backoffEvent := &Event{ID: uuid.New(), EventType: "comment.replied", CreatedAt: past, RetryCount: 1, LastRetryAt: &past}
	repo := newFakeRepo(dueEvent, backoffEvent)

	var handled int
	relay := NewRelay(repo, HandlerFunc(func(context.Context, *Event) error {
		handled++
		return nil
	}), 0, nil)

	if n := relay.drain(context.Background()); n != 1 {
		t.Fatalf("drain handled %d, want 1 (backoff event skipped)", n)
	}
	if handled != 1 {
		t.Fatalf("handler invoked %d times, want 1 (backoff event must not be handled)", handled)
	}
	if !repo.isPublished(dueEvent.ID) {
		t.Fatal("due event should be published")
	}
	if repo.isPublished(backoffEvent.ID) {
		t.Fatal("backoff event must stay unpublished")
	}
	if len(repo.releases) != 1 || repo.releases[0] != backoffEvent.ID {
		t.Fatalf("releases = %v, want exactly the backoff event's claim released", repo.releases)
	}
	// The skipped event must not hold a claim: a second drain pass sees it again
	// (and skips it again), proving no lease-induced invisibility.
	repo.releases = nil
	if n := relay.drain(context.Background()); n != 0 {
		t.Fatalf("second drain handled %d, want 0", n)
	}
	if len(repo.releases) != 1 || repo.releases[0] != backoffEvent.ID {
		t.Fatalf("second drain releases = %v, want the backoff event re-released", repo.releases)
	}
	if repo.claimsHeld() != 0 {
		t.Fatal("no claims may remain held after the drain")
	}
}

// TestRelay_RenewsClaimsDuringLongDrain guards D12: while a drain holds pending
// claims across a long handler window, the relay extends them via RenewClaims
// so the lease cannot expire mid-drain for a live holder.
func TestRelay_RenewsClaimsDuringLongDrain(t *testing.T) {
	now := time.Now()
	var clockMu sync.Mutex
	// A batch of two events where the first handler blocks past the cadence:
	// the relay must renew the pending claim before the second handler call.
	repo := newFakeRepo(
		&Event{ID: uuid.New(), EventType: "a", CreatedAt: time.Now()},
		&Event{ID: uuid.New(), EventType: "b", CreatedAt: time.Now()},
	)
	entered := make(chan uuid.UUID, 2)
	release := make(chan struct{})
	relay := NewRelay(repo, HandlerFunc(func(_ context.Context, ev *Event) error {
		entered <- ev.ID
		if ev.EventType == "a" {
			<-release
		}
		return nil
	}), 0, nil)
	// Inject a fake clock and a tiny renewal cadence so the test is
	// deterministic without sleeping for real time.
	relay.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	relay.renewEvery = time.Second

	go relay.drain(context.Background())
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never entered")
	}
	// Handler for "a" is blocked; advance past the cadence, then unblock.
	clockMu.Lock()
	now = now.Add(2 * time.Second)
	clockMu.Unlock()
	close(release)
	select {
	case <-entered: // "b" handled after the renewal point
	case <-time.After(2 * time.Second):
		t.Fatal("second event never handled after unblocking")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		n := len(repo.renews)
		repo.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	repo.mu.Lock()
	renewed := len(repo.renews)
	repo.mu.Unlock()
	if renewed == 0 {
		t.Fatal("relay never renewed the pending claim across the long handler")
	}
	if !repo.isPublished(repo.events[0].ID) || !repo.isPublished(repo.events[1].ID) {
		t.Fatal("both events must be published after the long drain")
	}
}

// TestRelay_HandlerPanicDoesNotCrashLoop guards the crash-loop hardening: a
// handler panic is recovered per-event and routed into the existing failure
// path, so the poison event advances retry_count, backs off, and dead-letters
// at maxRetries instead of escaping drain and killing the process. Before the
// fix this test fails because the panic escapes drain and kills the test
// binary (the regression proof).
func TestRelay_HandlerPanicDoesNotCrashLoop(t *testing.T) {
	good := &Event{ID: uuid.New(), EventType: "message.sent", CreatedAt: time.Now()}
	bad := &Event{ID: uuid.New(), EventType: "comment.replied", CreatedAt: time.Now()}
	repo := newFakeRepo(good, bad)
	sink := newRecordingSink()

	var mu sync.Mutex
	var calls int
	relay := NewRelay(repo, HandlerFunc(func(_ context.Context, e *Event) error {
		mu.Lock()
		calls++
		mu.Unlock()
		if e.ID == bad.ID {
			panic("poison payload")
		}
		return nil
	}), 2, nil, sink)

	// AC-1 (R1, R3, R4): the panic must not crash the drain; the good event
	// still publishes in the same pass, the poison event stays unpublished with
	// its claim cleared, and the failure is observable via the failed gauge.
	if n := relay.drain(context.Background()); n != 1 {
		t.Fatalf("first drain handled %d, want 1 (good published, bad failed)", n)
	}
	if !repo.isPublished(good.ID) {
		t.Fatal("good event should be published")
	}
	if repo.isPublished(bad.ID) {
		t.Fatal("poison event must stay unpublished for retry")
	}
	if got := sink.failed[bad.EventType]; got != 1 {
		t.Fatalf("failed[%s] = %d, want 1 (panic routed through the failure metrics)", bad.EventType, got)
	}
	if repo.claimsHeld() != 0 {
		t.Fatal("poison event's claim must be cleared; no claims may remain held")
	}
	mu.Lock()
	if calls != 2 {
		t.Fatalf("handler called %d times, want 2 (good + poison)", calls)
	}
	mu.Unlock()
	repo.mu.Lock()
	if bad.RetryCount != 1 || bad.DeadLetter {
		t.Fatalf("after first panic: retry_count=%d dead_letter=%v, want 1/false", bad.RetryCount, bad.DeadLetter)
	}
	repo.mu.Unlock()

	// AC-3 (R1): a second drain must not re-panic. The poison event is inside
	// its backoff(1) = 5s window, so it is claimed, skipped, and released
	// without invoking the handler.
	if n := relay.drain(context.Background()); n != 0 {
		t.Fatalf("backoff drain handled %d, want 0", n)
	}
	mu.Lock()
	if calls != 2 {
		t.Fatalf("handler called %d times after backoff drain, want 2 (no re-delivery in window)", calls)
	}
	mu.Unlock()

	// AC-2 (R2): once due, the poison event is retried, panics again, and
	// dead-letters at maxRetries; the dead-lettered event is then no longer
	// claimable (ClaimBatch excludes dead letters), so the handler is frozen.
	due := time.Now().Add(-time.Hour)
	repo.mu.Lock()
	bad.LastRetryAt = &due
	repo.mu.Unlock()
	if n := relay.drain(context.Background()); n != 0 {
		t.Fatalf("dead-letter drain handled %d, want 0", n)
	}
	repo.mu.Lock()
	deadLettered := bad.DeadLetter && bad.RetryCount == 2
	repo.mu.Unlock()
	if !deadLettered {
		t.Fatalf("poison event must dead-letter at maxRetries: retry_count=%d dead_letter=%v", bad.RetryCount, bad.DeadLetter)
	}
	if n := relay.drain(context.Background()); n != 0 {
		t.Fatalf("post-dead-letter drain handled %d, want 0", n)
	}
	mu.Lock()
	frozen := calls
	mu.Unlock()
	if frozen != 3 {
		t.Fatalf("handler called %d times, want 3 (dead-lettered poison event never re-delivered)", frozen)
	}
	if repo.claimsHeld() != 0 {
		t.Fatal("no claims may remain held after the dead-letter drain")
	}

	// AC-3 (R5): Start/stop (drain-on-stop) must complete without panic and
	// without re-delivering the dead-lettered poison event.
	sink.mu.Lock()
	passes := sink.drainPasses
	sink.mu.Unlock()
	stop := relay.Start(24 * time.Hour)
	stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		n := sink.drainPasses
		sink.mu.Unlock()
		if n > passes {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	sink.mu.Lock()
	drainedOnStop := sink.drainPasses > passes
	sink.mu.Unlock()
	if !drainedOnStop {
		t.Fatal("drain-on-stop never completed")
	}
	mu.Lock()
	afterStop := calls
	mu.Unlock()
	if afterStop != frozen {
		t.Fatalf("handler called %d times after stop, want %d (no poison re-delivery)", afterStop, frozen)
	}
}

// TestRelay_HandlerErrorClearsClaimAndRetries guards the MarkFailed interplay
// (F4): a failed event's claim is cleared, so the next drain pass can claim and
// retry it once due, and a second failure advances retry state.
func TestRelay_HandlerErrorClearsClaimAndRetries(t *testing.T) {
	bad := &Event{ID: uuid.New(), EventType: "comment.replied", CreatedAt: time.Now()}
	repo := newFakeRepo(bad)

	fail := true
	relay := NewRelay(repo, HandlerFunc(func(_ context.Context, e *Event) error {
		if fail {
			return stderrors.New("boom")
		}
		return nil
	}), 3, nil)

	if n := relay.drain(context.Background()); n != 0 {
		t.Fatalf("first drain handled %d, want 0", n)
	}
	if repo.claimsHeld() != 0 {
		t.Fatal("failed event's claim must be cleared so it stays claimable")
	}
	if repo.isPublished(bad.ID) {
		t.Fatal("failed event must stay unpublished")
	}

	// The event is in backoff now: a drain skips and releases it (no retry yet).
	if n := relay.drain(context.Background()); n != 0 {
		t.Fatalf("backoff drain handled %d, want 0", n)
	}
	if repo.isPublished(bad.ID) {
		t.Fatal("event must not be published while in backoff")
	}

	// Once due, the retry publishes.
	fail = false
	repo.mu.Lock()
	due := time.Now().Add(-time.Hour)
	repo.events[0].LastRetryAt = &due
	repo.mu.Unlock()
	if n := relay.drain(context.Background()); n != 1 {
		t.Fatalf("retry drain handled %d, want 1", n)
	}
	if !repo.isPublished(bad.ID) {
		t.Fatal("event should be published on retry")
	}
}
