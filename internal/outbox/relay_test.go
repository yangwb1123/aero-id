package outbox

import (
	"context"
	stderrors "errors"
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

// fakeRepo is an in-memory Repository for relay unit tests. Append is unused here
// (the relay never appends), so it is a no-op.
type fakeRepo struct {
	mu        sync.Mutex
	events    []*Event
	published map[uuid.UUID]bool
}

func newFakeRepo(events ...*Event) *fakeRepo {
	return &fakeRepo{events: events, published: map[uuid.UUID]bool{}}
}

func (r *fakeRepo) Append(context.Context, *gorm.DB, *Event) error { return nil }

func (r *fakeRepo) FetchUnpublished(_ context.Context, limit int) ([]*Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Event
	for _, e := range r.events {
		if r.published[e.ID] {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *fakeRepo) MarkPublished(_ context.Context, ids []uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		r.published[id] = true
	}
	return nil
}

func (r *fakeRepo) isPublished(id uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.published[id]
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
	}), nil)

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
	}), nil)

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
	}), nil, sink)

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
	relay := NewRelay(repo, HandlerFunc(func(context.Context, *Event) error { return nil }), nil)
	if n := relay.drain(context.Background()); n != 1 {
		t.Fatalf("drain published %d, want 1", n)
	}
	// Empty outbox pass is also safe with no sink.
	if n := relay.drain(context.Background()); n != 0 {
		t.Fatalf("empty drain published %d, want 0", n)
	}
}
