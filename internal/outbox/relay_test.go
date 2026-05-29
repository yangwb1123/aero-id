package outbox

import (
	"context"
	stderrors "errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

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
