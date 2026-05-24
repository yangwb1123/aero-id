package middleware

import (
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
)

// Recovery converts a panic in any downstream handler into a logged 500 response.
func Recovery(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic recovered",
					zap.Any("panic", r),
					zap.String("path", c.FullPath()),
					zap.ByteString("stack", debug.Stack()),
				)
				httpx.WriteError(c, apperr.Internal("internal.panic", "internal server error"))
				c.Abort()
			}
		}()
		c.Next()
	}
}
