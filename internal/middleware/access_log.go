package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// AccessLog emits one structured log line per request after it completes.
// When a Sample middleware runs before it, the "sampled" context flag controls
// whether info-level logs are emitted: false means skip the log line. Status >= 400
// (client/server errors) are always logged regardless of sampling.
func AccessLog(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		// Always log errors regardless of sampling.
		if c.Writer.Status() < 400 {
			sampled, ok := c.Get("sampled")
			if ok && !sampled.(bool) {
				return
			}
		}

		log.Info("http_request",
			zap.String("method", c.Request.Method),
			zap.String("path", c.FullPath()),
			zap.String("uri", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
			zap.String("request_id", c.GetString("request_id")),
			zap.String("trace_id", c.GetString("trace_id")),
			zap.String("actor", c.GetString("actor_uid")),
		)
	}
}
