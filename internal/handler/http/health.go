// Package http hosts aero-id's REST transport: a router plus thin handlers that
// delegate to the service layer.
package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Check is a named readiness probe. A failing Critical check takes the service
// out of rotation (HTTP 503); a failing non-critical check is reported but only
// marks the service "degraded", so an optional dependency cannot fail readiness.
type Check struct {
	Name     string
	Critical bool
	Probe    func(ctx context.Context) error
}

// HealthHandler serves liveness and readiness probes.
type HealthHandler struct {
	checks  []Check
	timeout time.Duration
}

// NewHealthHandler wires an ordered set of readiness checks. With no checks,
// readiness always succeeds.
func NewHealthHandler(checks ...Check) *HealthHandler {
	return &HealthHandler{checks: checks, timeout: 2 * time.Second}
}

// Live always reports OK as long as the process is serving.
func (h *HealthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready runs every check under a short deadline and reports per-dependency
// status. A failing critical check returns 503 ("unavailable"); a failing
// non-critical check still returns 200 but flags the overall status "degraded".
func (h *HealthHandler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.timeout)
	defer cancel()

	results := make(map[string]string, len(h.checks))
	code := http.StatusOK
	degraded := false
	for _, chk := range h.checks {
		if chk.Probe == nil {
			results[chk.Name] = "ok"
			continue
		}
		if err := chk.Probe(ctx); err != nil {
			// Readiness is an operational signal, not a dependency error dump.
			// Keep topology, SQL and upstream response details out of the public
			// probe; operators can use logs and metrics for the cause.
			results[chk.Name] = "unavailable"
			if chk.Critical {
				code = http.StatusServiceUnavailable
			} else {
				degraded = true
			}
			continue
		}
		results[chk.Name] = "ok"
	}

	status := "ready"
	switch {
	case code != http.StatusOK:
		status = "unavailable"
	case degraded:
		status = "degraded"
	}
	c.JSON(code, gin.H{"status": status, "checks": results})
}
