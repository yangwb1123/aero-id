package middleware

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

// RequestIDHeader is the canonical header carrying the per-request correlation id.
const RequestIDHeader = "X-Request-ID"

type ctxKey int

const requestIDKey ctxKey = iota

// RequestID reuses an inbound X-Request-ID or mints a new one, exposing it on the
// response header, the gin context ("request_id"), and the request context.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader(RequestIDHeader)
		if rid == "" {
			rid = idgen.NewID().String()
		}
		c.Writer.Header().Set(RequestIDHeader, rid)
		c.Set("request_id", rid)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), requestIDKey, rid))
		c.Next()
	}
}

// RequestIDFrom returns the request id stored in ctx, or "".
func RequestIDFrom(ctx context.Context) string {
	rid, _ := ctx.Value(requestIDKey).(string)
	return rid
}
