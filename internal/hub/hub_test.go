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

	h.Publish("alice", "msg", "hello", 0)

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

	h.Publish("bob", "ping", "data1", 0)

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
	h.Publish("nobody", "evt", "data", 0)
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
			h.Publish("eve", "flood", "data", 0)
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

func TestHub_Disconnect(t *testing.T) {
	h := hub.New()
	ch, unsub := h.Subscribe("alice")

	// Disconnect force-closes every subscriber channel for the uid and removes
	// the uid from the registry (the admin ban path).
	h.Disconnect("alice")

	// The pre-disconnect channel must be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected pre-disconnect channel to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pre-disconnect channel to close")
	}

	// The pre-disconnect unsubscribe must be safe: the map-membership guard
	// skips the already-closed channel, so there is no double-close panic.
	unsub()

	// Disconnect deleted the uid entry, so a fresh Subscribe must yield an
	// open channel that delivers events (a closed-but-present entry could not).
	ch2, unsub2 := h.Subscribe("alice")
	defer unsub2()

	h.Publish("alice", "msg", "hello", 0)

	select {
	case evt := <-ch2:
		if evt.Type != "msg" || evt.Data != "hello" {
			t.Fatalf("unexpected event: %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event on post-Disconnect channel")
	}

	h.Close()
}

func TestHub_DisconnectNoSubscriber(t *testing.T) {
	h := hub.New()

	// Disconnecting a uid with no active subscribers is a no-op (no panic).
	h.Disconnect("nobody")
	h.Disconnect("")

	// The registry must still function afterwards.
	h.Publish("nobody", "evt", "data", 0)
}

func TestHub_CloseTwice(t *testing.T) {
	h := hub.New()
	ch, unsub := h.Subscribe("dave")
	defer unsub()

	h.Close()

	// The pre-close channel must be closed by the first Close.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected pre-close channel to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pre-close channel to close")
	}

	// Close is idempotent: the registry is already drained and closed is
	// already true, so the second call must not panic.
	h.Close()
}

func TestHub_UnsubAfterClose(t *testing.T) {
	h := hub.New()
	_, unsub := h.Subscribe("dave")

	h.Close()

	// Unsubscribing after Close must not double-close the channel.
	unsub()
}

func TestHub_PublishAfterClose(t *testing.T) {
	h := hub.New()
	ch, _ := h.Subscribe("frank")

	h.Close()

	// Publishing after Close must not panic (the registry is drained, so pub
	// iterates nothing) and must deliver to no one.
	h.Publish("frank", "evt", "data", 1)
	h.Publish("other-uid", "evt", "data", 1)

	// The pre-close channel is closed: a receive yields the zero value with
	// ok == false, never a live event.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel, but received a live event after Close")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for channel close")
	}
}
