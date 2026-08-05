package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/hub"
	"github.com/aero/aero-id/internal/service"
)

// MissedNotifFetcher fetches notifications created strictly after a timestamp
// for a given recipient. Used by the SSE handler to replay missed events on
// reconnection (SSE last_event_id recovery).
type MissedNotifFetcher interface {
	ListSince(ctx context.Context, recipientUID string, since time.Time, limit int) ([]*domain.Notification, error)
}

// EventsHandler serves GET /v1/events as an SSE (Server-Sent Events) stream.
// Each authenticated client receives real-time pushes for notifications addressed
// to their uid. Heartbeat pings are sent every 30s to keep proxies from timing out.
// Supports last_event_id reconnection: when a client reconnects with the
// Last-Event-ID header (or ?last_event_id query param), missed notifications are
// replayed before switching to real-time streaming.
type EventsHandler struct {
	hub     *hub.Hub
	fetcher MissedNotifFetcher
}

func NewEventsHandler(h *hub.Hub, fetcher MissedNotifFetcher) *EventsHandler {
	return &EventsHandler{hub: h, fetcher: fetcher}
}

func (h *EventsHandler) RegisterRoutes(r Routes) {
	r.Auth.GET("/events", h.Stream)
}

func (h *EventsHandler) Stream(c *gin.Context) {
	uid, ok := service.ActorFrom(c.Request.Context())
	if !ok {
		c.Status(http.StatusUnauthorized)
		return
	}

	sub, unsub := h.hub.Subscribe(uid)
	defer unsub()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // disable nginx proxy buffering
	c.Status(http.StatusOK)

	// Replay missed notifications based on last_event_id.
	lastEventID := c.Request.Header.Get("Last-Event-ID")
	if lastEventID == "" {
		lastEventID = c.Query("last_event_id")
	}
	if lastEventID != "" && h.fetcher != nil {
		h.replayMissed(c, uid, lastEventID)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case evt, ok := <-sub:
			if !ok {
				return
			}
			// Include an id: line so the browser tracks the most recent event
			// and reconnects with Last-Event-ID after a disconnect.
			fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", evt.ID, evt.Type, evt.Data)
			c.Writer.Flush()
		case <-ticker.C:
			fmt.Fprintf(c.Writer, ": ping\n\n")
			c.Writer.Flush()
		}
	}
}

// replayMissed queries notifications created after the given lastEventID
// (a unix-millis timestamp) and writes them as SSE events.
func (h *EventsHandler) replayMissed(c *gin.Context, uid, lastEventID string) {
	millis, err := strconv.ParseInt(lastEventID, 10, 64)
	if err != nil || millis <= 0 {
		return // invalid or missing id; skip replay
	}
	since := time.UnixMilli(millis)
	missed, err := h.fetcher.ListSince(c.Request.Context(), uid, since, 50)
	if err != nil {
		// Transient error — log but don't disrupt the connection.
		c.Error(err)
		return
	}
	for _, n := range missed {
		data, _ := json.Marshal(n)
		// Use the notification's created_at millis as the event id.
		eventMillis := n.CreatedAt.UnixMilli()
		fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", eventMillis, n.Type, string(data))
	}
	if len(missed) > 0 {
		c.Writer.Flush()
	}
}
