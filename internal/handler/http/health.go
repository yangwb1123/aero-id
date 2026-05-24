// Package http hosts aero-id's REST transport: a router plus thin handlers that
// delegate to the service layer.
package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// HealthHandler serves liveness and readiness probes.
type HealthHandler struct {
	ready func(ctx context.Context) error
}

// NewHealthHandler wires a readiness checker (typically a DB ping). A nil checker
// makes readiness always succeed.
func NewHealthHandler(ready func(ctx context.Context) error) *HealthHandler {
	return &HealthHandler{ready: ready}
}

// Live always reports OK as long as the process is serving.
func (h *HealthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready reports 503 when the readiness checker fails (e.g. DB unreachable).
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if h.ready != nil {
		if err := h.ready(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable", "error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
