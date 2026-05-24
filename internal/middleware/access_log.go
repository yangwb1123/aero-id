package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// AccessLog emits one structured log line per request after it completes.
func AccessLog(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info("http_request",
			zap.String("method", c.Request.Method),
			zap.String("path", c.FullPath()),
			zap.String("uri", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("latency", time.Since(start)),
			zap.String("request_id", c.GetString("request_id")),
			zap.String("actor", c.GetString("actor_uid")),
		)
	}
}
