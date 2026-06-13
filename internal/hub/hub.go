// Package hub provides a thread-safe SSE connection registry that allows
// publishing events to all subscribers for a given uid.
package hub

import (
	"sync"
	"sync/atomic"
)

// SSEEvent is a single server-sent event payload.
type SSEEvent struct {
	Type string
	Data string
}

// Hub is a thread-safe registry of subscriber channels keyed by uid. Multiple
// subscribers per uid are supported (e.g. same user on multiple devices).
type Hub struct {
	mu      sync.RWMutex
	subs    map[string]map[int64]chan SSEEvent
	nextID  atomic.Int64
	closed  bool
}

// New returns an initialised Hub.
func New() *Hub {
	return &Hub{
		subs: make(map[string]map[int64]chan SSEEvent),
	}
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
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan SSEEvent, 16)

	if h.closed {
		close(ch)
		return ch, func() {}
	}

	id := h.nextID.Add(1)
	if h.subs[uid] == nil {
		h.subs[uid] = make(map[int64]chan SSEEvent)
	}
	h.subs[uid][id] = ch

	unsub := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if m, ok := h.subs[uid]; ok {
			if c, ok2 := m[id]; ok2 {
				delete(m, id)
				close(c)
			}
			if len(m) == 0 {
				delete(h.subs, uid)
			}
		}
	}
	return ch, unsub
}

// pub delivers an event to all current subscribers for uid. Sends are
// non-blocking: if a subscriber's channel is full the event is dropped for
// that subscriber only.
func (h *Hub) pub(uid string, evt SSEEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.subs[uid] {
		select {
		case ch <- evt:
		default:
			// subscriber is too slow; drop rather than block
		}
	}
}

// Close closes every subscriber channel and clears the registry. Any
// subsequent call to Subscribe will return a pre-closed channel.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for uid, m := range h.subs {
		for id, ch := range m {
			close(ch)
			delete(m, id)
		}
		delete(h.subs, uid)
	}
}
