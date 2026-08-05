package middleware

import (
	"math/rand/v2"

	"github.com/gin-gonic/gin"
)

// Sample returns a middleware that probabilistically marks requests for access log
// sampling. It stores a boolean "sampled" in the gin context that AccessLog can
// check before emitting a log line.
//
// Only info-level logs are sampled; requests with status >= 400 (client or server
// error) are ALWAYS logged regardless of sample_ratio. The /healthz and /readyz
// paths are always sampled (i.e. never sampled away) because they are operational
// probes that should remain visible.
//
// A ratio <= 0 disables sampling entirely — every request is logged (backward
// compatible). A ratio >= 1 logs every request as well but skips the coin flip.
func Sample(ratio float64) gin.HandlerFunc {
	if ratio <= 0 {
		// Sampling disabled — pass through; AccessLog will log everything.
		return func(c *gin.Context) {
			c.Set("sampled", true)
			c.Next()
		}
	}
	if ratio >= 1 {
		return func(c *gin.Context) {
			c.Set("sampled", true)
			c.Next()
		}
	}
	return func(c *gin.Context) {
		// Health-check probes are always logged (never sampled away).
		p := c.Request.URL.Path
		if p == "/healthz" || p == "/readyz" {
			c.Set("sampled", true)
			c.Next()
			return
		}
		// Roll the dice.
		sampled := rand.Float64() < ratio //nolint:gosec // request sampling is intentionally non-cryptographic
		c.Set("sampled", sampled)
		c.Next()
	}
}
