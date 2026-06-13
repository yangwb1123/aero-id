package http

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/hub"
	"github.com/aero/aero-id/internal/service"
)

// EventsHandler serves GET /v1/events as an SSE (Server-Sent Events) stream.
// Each authenticated client receives real-time pushes for notifications addressed
// to their uid. Heartbeat pings are sent every 30s to keep proxies from timing out.
type EventsHandler struct {
	hub *hub.Hub
}

func NewEventsHandler(h *hub.Hub) *EventsHandler { return &EventsHandler{hub: h} }

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
			fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", evt.Type, evt.Data)
			c.Writer.Flush()
		case <-ticker.C:
			fmt.Fprintf(c.Writer, ": ping\n\n")
			c.Writer.Flush()
		}
	}
}
