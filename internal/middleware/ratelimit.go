package middleware

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/pkg/ratelimit"
	"github.com/aero/aero-id/internal/service"
)

// rateLimitErrorBody mirrors the httpx error envelope so a 429 looks like every
// other error response. It is defined here (not imported) because httpx's body
// type is unexported and the limiter middleware lives in the middleware package.
type rateLimitErrorBody struct {
	Error rateLimitErrorDetail `json:"error"`
}

type rateLimitErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// RateLimit throttles requests using an in-memory per-key token bucket. It keys
// on the authenticated actor uid when present (so it must be placed AFTER Auth),
// falling back to the client IP for unauthenticated / public routes. Over-limit
// requests get a 429 with a Retry-After header; everything else passes through.
//
// A nil limiter (or one configured with a non-positive rate/burst) is a no-op,
// so wiring it in without enabling it costs nothing. The limiter fails open on
// any internal issue by virtue of Allow returning true when disabled.
func RateLimit(l *ratelimit.Limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if l == nil {
			c.Next()
			return
		}
		key := rateLimitKey(c)
		if l.Allow(key) {
			c.Next()
			return
		}
		retry := l.RetryAfter()
		if secs := int(retry.Seconds()); secs > 0 {
			c.Header("Retry-After", strconv.Itoa(secs))
		} else {
			// Sub-second windows still owe the client a value; round up to 1s.
			c.Header("Retry-After", "1")
		}
		c.AbortWithStatusJSON(http.StatusTooManyRequests, rateLimitErrorBody{
			Error: rateLimitErrorDetail{
				Code:      "rate.limit_exceeded",
				Message:   "too many requests",
				RequestID: c.GetString("request_id"),
			},
		})
	}
}

// rateLimitKey prefers the authenticated actor uid (set by Auth) so a single
// user is limited consistently across IPs; it falls back to the client IP for
// anonymous / public requests.
func rateLimitKey(c *gin.Context) string {
	if uid, ok := service.ActorFrom(c.Request.Context()); ok {
		return "uid:" + uid
	}
	return "ip:" + c.ClientIP()
}
