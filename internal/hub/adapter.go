package hub

import "github.com/aero/aero-id/internal/service"

// Publish satisfies service.NotificationPusher. It converts the service-layer
// arguments to an SSEEvent and delivers it to all current subscribers for uid.
// hub may import service; service must NOT import hub.
func (h *Hub) Publish(uid, eventType, data string, eventID int64) {
	h.pub(uid, SSEEvent{ID: eventID, Type: eventType, Data: data})
}

// compile-time assertion that *Hub satisfies service.NotificationPusher.
var _ service.NotificationPusher = (*Hub)(nil)
