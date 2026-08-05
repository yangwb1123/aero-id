// Package hub provides a thread-safe SSE connection registry that allows
// publishing events to all subscribers for a given uid.
package hub

import (
	"sync"
	"sync/atomic"
)

// SSEEvent is a single server-sent event payload.
type SSEEvent struct {
	// ID is an opaque identifier sent as the SSE id: field. Clients echo it
	// back in Last-Event-ID to resume from the last received event after a
	// disconnect. For real-time pushes it is the creating notification's
	// created_at unix-millis; for replayed missed events it is the same.
	ID   int64
	Type string
	Data string
}

// uidSubs holds all subscriber channels for a single uid, protected by its
// own mutex so that operations targeting different uids never contend.
type uidSubs struct {
	mu   sync.Mutex
	subs map[int64]chan SSEEvent
}

// Hub is a thread-safe registry of subscriber channels keyed by uid. Multiple
// subscribers per uid are supported (e.g. same user on multiple devices).
// Locking is per-uid (sharded) so that publishing to alice never blocks
// subscribing or publishing to bob — only operations on the same uid serialise.
type Hub struct {
	subs   sync.Map // uid → *uidSubs
	nextID atomic.Int64
	closed atomic.Bool
}

// New returns an initialised Hub.
func New() *Hub {
	return &Hub{}
}

// Subscribe registers a new subscriber for uid and returns a read-only event
// channel plus an unsubscribe function. The caller must invoke the unsubscribe
// function (typically via defer) when the connection closes. The channel has a
// buffer of 16 events; slow consumers will silently drop events rather than
// block Publish.
//
// If the Hub has been closed, Subscribe returns an already-closed channel so
// the caller's select loop exits immediately.
func (h *Hub) Subscribe(uid string) (<-chan SSEEvent, func()) {
	ch := make(chan SSEEvent, 16)

	if h.closed.Load() {
		close(ch)
		return ch, func() {}
	}

	// LoadOrStore the per-uid subscriber map.
	entry, _ := h.subs.LoadOrStore(uid, &uidSubs{subs: make(map[int64]chan SSEEvent)})
	us := entry.(*uidSubs)

	id := h.nextID.Add(1)

	us.mu.Lock()
	if h.closed.Load() {
		us.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	us.subs[id] = ch
	us.mu.Unlock()

	unsub := func() {
		us.mu.Lock()
		if c, ok := us.subs[id]; ok {
			delete(us.subs, id)
			close(c)
		}
		// If this was the last subscriber for uid, remove the entry to avoid
		// leaking the *uidSubs object. The map entry itself is tiny, but a
		// churn of short-lived connections with unique uids would accumulate
		// them. This Delete is safe: a concurrent pub will either see the
		// entry (empty) or not; either way the loop body doesn't execute.
		if len(us.subs) == 0 {
			h.subs.Delete(uid)
		}
		us.mu.Unlock()
	}
	return ch, unsub
}

// pub delivers an event to all current subscribers for uid. Sends are
// non-blocking: if a subscriber's channel is full the event is dropped for
// that subscriber only.
func (h *Hub) pub(uid string, evt SSEEvent) {
	raw, ok := h.subs.Load(uid)
	if !ok {
		return
	}
	us := raw.(*uidSubs)
	us.mu.Lock()
	defer us.mu.Unlock()
	for _, ch := range us.subs {
		select {
		case ch <- evt:
		default:
			// subscriber is too slow; drop rather than block
		}
	}
}

// Disconnect closes every subscriber channel for the given uid and removes the
// uid from the registry. This is used to force-close SSE connections when a user
// is banned. Safe to call on a uid with no active subscribers (no-op).
func (h *Hub) Disconnect(uid string) {
	raw, ok := h.subs.Load(uid)
	if !ok {
		return
	}
	us := raw.(*uidSubs)
	us.mu.Lock()
	for id, ch := range us.subs {
		close(ch)
		delete(us.subs, id)
	}
	us.mu.Unlock()
	h.subs.Delete(uid)
}

// Close closes every subscriber channel and clears the registry. Any
// subsequent call to Subscribe will return a pre-closed channel.
func (h *Hub) Close() {
	h.closed.Store(true)
	h.subs.Range(func(key, value any) bool {
		us := value.(*uidSubs)
		us.mu.Lock()
		for id, ch := range us.subs {
			close(ch)
			delete(us.subs, id)
		}
		us.mu.Unlock()
		h.subs.Delete(key)
		return true
	})
}

// Pusher publishes an event to all subscribers for a given uid. Implemented by
// *Hub (in adapter.go) so that notification/dispatcher can be decoupled from the
// concrete Hub type.
type Pusher interface {
	Publish(uid, eventType, data string)
}
