package hub_test

import (
	"testing"
	"time"

	"github.com/aero/aero-id/internal/hub"
)

func TestHub_PubSub(t *testing.T) {
	h := hub.New()
	ch, unsub := h.Subscribe("alice")
	defer unsub()

	h.Publish("alice", "msg", "hello")

	select {
	case evt := <-ch:
		if evt.Type != "msg" || evt.Data != "hello" {
			t.Fatalf("unexpected event: %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestHub_MultipleSubscribers(t *testing.T) {
	h := hub.New()
	ch1, unsub1 := h.Subscribe("bob")
	defer unsub1()
	ch2, unsub2 := h.Subscribe("bob")
	defer unsub2()

	h.Publish("bob", "ping", "data1")

	recv := func(ch <-chan hub.SSEEvent, name string) {
		select {
		case evt := <-ch:
			if evt.Type != "ping" {
				t.Errorf("%s: unexpected type %q", name, evt.Type)
			}
		case <-time.After(time.Second):
			t.Errorf("%s: timed out waiting for event", name)
		}
	}
	recv(ch1, "ch1")
	recv(ch2, "ch2")
}

func TestHub_Unsubscribe(t *testing.T) {
	h := hub.New()
	ch, unsub := h.Subscribe("carol")

	unsub()

	// Channel should be closed; a receive should return the zero value with ok==false.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed, but received a value")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel to close")
	}
}

func TestHub_PublishNoSubscriber(t *testing.T) {
	h := hub.New()
	// Must not panic.
	h.Publish("nobody", "evt", "data")
}

// bufSize is the per-subscriber channel buffer used by Subscribe (see hub.go).
const bufSize = 16

func TestHub_PublishSlowConsumerDrops(t *testing.T) {
	h := hub.New()
	ch, unsub := h.Subscribe("eve")
	defer unsub()

	// Publish far more events than the channel buffer without draining. A slow
	// consumer must never block Publish: the hub drops excess events instead.
	const total = bufSize * 3

	done := make(chan struct{})
	go func() {
		for i := 0; i < total; i++ {
			// Must not panic and must not block even though nobody is reading.
			h.Publish("eve", "flood", "data")
		}
		close(done)
	}()

	select {
	case <-done:
		// All publishes returned promptly without blocking.
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full slow-consumer channel")
	}

	// Drain the channel. We must be able to read at most the buffer's capacity
	// of retained events, and the excess must have been silently dropped (i.e.
	// we do NOT receive all `total` events).
	var got int
drain:
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				break drain
			}
			got++
		default:
			break drain
		}
	}

	if got != bufSize {
		t.Fatalf("expected exactly %d retained events (buffer capacity), got %d", bufSize, got)
	}
	if got >= total {
		t.Fatalf("expected excess events to be dropped, but received all %d", got)
	}
}

func TestHub_Close(t *testing.T) {
	h := hub.New()

	// Subscribe before closing.
	ch, unsub := h.Subscribe("dave")
	defer unsub()

	h.Close()

	// The pre-existing subscriber channel should be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected pre-close channel to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pre-close channel to close")
	}

	// A new subscriber after Close should also get a closed channel.
	ch2, unsub2 := h.Subscribe("dave")
	defer unsub2()
	select {
	case _, ok := <-ch2:
		if ok {
			t.Fatal("expected post-close channel to be closed immediately")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for post-close channel")
	}
}
