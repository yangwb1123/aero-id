package http

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/pkg/httpx"
)

// SourceHealthHandler reports source connectivity separately from readiness:
// a source outage must never make the regional read endpoints unready
// (AGENTS.md layered-health rule).
type SourceHealthHandler struct {
	connectors []connector.Connector
	timeout    time.Duration
}

func NewSourceHealthHandler(connectors []connector.Connector) *SourceHealthHandler {
	return &SourceHealthHandler{connectors: connectors, timeout: 3 * time.Second}
}

func (h *SourceHealthHandler) RegisterRoutes(r Routes) {
	r.Public.GET("/health/sources", h.List)
}

type sourceHealthView struct {
	Source string `json:"source"`
	Status string `json:"status"` // ok | degraded
	Error  string `json:"error,omitempty"`
}

// List probes every configured source connector with an independent short
// deadline. The endpoint is unauthenticated and only reports connectivity —
// no topology, URLs or credentials.
func (h *SourceHealthHandler) List(c *gin.Context) {
	views := make([]sourceHealthView, 0, len(h.connectors))
	for _, client := range h.connectors {
		if client == nil {
			continue
		}
		view := sourceHealthView{Source: string(client.Source()), Status: "ok"}
		ctx, cancel := context.WithTimeout(c.Request.Context(), h.timeout)
		if err := client.Health(ctx); err != nil {
			view.Status = "degraded"
			view.Error = "source unreachable" // no internals
		}
		cancel()
		views = append(views, view)
	}
	httpx.OK(c, gin.H{"sources": views})
}
