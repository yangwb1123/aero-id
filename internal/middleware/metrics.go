package middleware

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/pkg/metrics"
)

// Metrics records one RED observation per request into reg, labelled by the matched
// route template (via c.FullPath, not the raw path) to keep label cardinality bounded.
func Metrics(reg *metrics.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		reg.ObserveHTTP(c.Request.Method, c.FullPath(), c.Writer.Status(), time.Since(start))
	}
}
