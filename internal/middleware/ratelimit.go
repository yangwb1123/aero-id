package middleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// rateLimitSensitivePrefixes are paths whose requests must not proceed without
// a rate-limit decision (P6-07 risk-classified failure policy): erase, export,
// sync, audit, admin, operations and source event ingestion are security- or
// cost-sensitive workloads. Interactive reads fail open on limiter outage;
// these fail closed (503) so an outage cannot silently bypass throttles on
// sensitive writes.
var rateLimitSensitivePrefixes = []string{
	"/v1/me/erase",
	"/v1/me/export",
	"/v1/me/sync",
	"/v1/sync/jobs",
	"/v1/audit",
	"/v1/admin",
	"/v1/operations",
	"/internal/v1/events",
}

// RateLimit throttles requests using an in-memory per-key token bucket. It keys
// on the authenticated actor uid when present (so it must be placed AFTER Auth),
// falling back to the client IP for unauthenticated / public routes. Over-limit
// requests get a 429 with a Retry-After header; everything else passes through.
//
// A nil limiter (or one configured with a non-positive rate/burst) is a no-op,
// so wiring it in without enabling it costs nothing.
//
// Failure policy (P6-07): when the distributed limiter errors, interactive
// paths fall back to the local bucket (and ultimately fail open), while
// sensitive paths fail closed with 503 — no rate decision means no sensitive
// write proceeds.
func RateLimit(l *ratelimit.Limiter, distributed ...ratelimit.DistributedLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := rateLimitKey(c)
		allowed, retry, unavailable := allowRateLimit(c.Request.Context(), key, l, firstDistributed(distributed), isRateLimitSensitive(c.FullPath()))
		if allowed {
			c.Next()
			return
		}
		if unavailable {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, rateLimitErrorBody{
				Error: rateLimitErrorDetail{
					Code:      "rate.limiter_unavailable",
					Message:   "rate limiter unavailable; sensitive request refused",
					RequestID: c.GetString("request_id"),
				},
			})
			return
		}
		if retry <= 0 {
			retry = l.RetryAfter()
		}
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

func firstDistributed(values []ratelimit.DistributedLimiter) ratelimit.DistributedLimiter {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

func rateLimitKey(c *gin.Context) string {
	if uid, ok := service.ActorFrom(c.Request.Context()); ok {
		return "uid:" + uid
	}
	return "ip:" + c.ClientIP()
}

func isRateLimitSensitive(path string) bool {
	for _, prefix := range rateLimitSensitivePrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// allowRateLimit returns (allowed, retry, unavailable). unavailable is only
// true for sensitive paths when no rate decision could be made at all.
func allowRateLimit(ctx context.Context, key string, local *ratelimit.Limiter,
	distributed ratelimit.DistributedLimiter, sensitive bool) (bool, time.Duration, bool) {
	if distributed != nil {
		allowed, retry, err := distributed.Allow(ctx, key)
		if err == nil {
			return allowed, retry, false
		}
		// Distributed limiter is down. Interactive paths continue with the
		// local bucket; sensitive paths only proceed if the local bucket can
		// still make a decision.
		if sensitive {
			if local == nil {
				return false, 0, true // no decision possible -> fail closed
			}
			return local.Allow(key), local.RetryAfter(), false
		}
	}
	if local == nil {
		return true, 0, false // fail open for interactive traffic
	}
	return local.Allow(key), local.RetryAfter(), false
}
