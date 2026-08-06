package middleware

import (
	"context"
	"errors"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/service"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/pkg/ratelimit"
)

// failDistributedLimiter always errors, modeling a Redis outage.
type failDistributedLimiter struct{}

func (failDistributedLimiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	return false, 0, errors.New("redis down")
}

func runRateLimitRequest(t *testing.T, path string, distributed ratelimit.DistributedLimiter, local *ratelimit.Limiter) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	withActor := func(c *gin.Context) {
		c.Request = c.Request.WithContext(service.WithIdentity(c.Request.Context(), &domain.TokenIdentity{
			TenantID: "tenant-a", Subject: "u-1", UID: "u-1", Issuer: "snaplink",
		}))
		c.Next()
	}
	group := router.Group("/v1", withActor, RateLimit(local, distributed))
	for _, path := range []string{"/me", "/me/erase", "/admin/accounts", "/audit/events"} {
		group.GET(path, func(c *gin.Context) { c.Status(http.StatusOK) })
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestRateLimitSensitiveFailClosedOnLimiterOutage: with Redis down and no local
// bucket, sensitive writes are refused with 503 (no rate decision => no
// sensitive write), while interactive reads still pass (fail open).
func TestRateLimitSensitiveFailClosedOnLimiterOutage(t *testing.T) {
	if rec := runRateLimitRequest(t, "/v1/me/erase", failDistributedLimiter{}, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("sensitive erase = %d, want 503", rec.Code)
	}
	if rec := runRateLimitRequest(t, "/v1/admin/accounts", failDistributedLimiter{}, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("sensitive admin = %d, want 503", rec.Code)
	}
	if rec := runRateLimitRequest(t, "/v1/audit/events", failDistributedLimiter{}, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("sensitive audit = %d, want 503", rec.Code)
	}
	if rec := runRateLimitRequest(t, "/v1/me", failDistributedLimiter{}, nil); rec.Code != http.StatusOK {
		t.Errorf("interactive read = %d, want 200 (fail open)", rec.Code)
	}
}

// TestRateLimitSensitiveUsesLocalBucketWhenRedisDown: a local bucket still
// provides a decision, so sensitive paths are throttled (429), not 503.
func TestRateLimitSensitiveUsesLocalBucketWhenRedisDown(t *testing.T) {
	local := ratelimit.New(1, 1, time.Minute) // saturated after one request
	_ = local.Allow("uid:u-1")                // consume the only token (middleware keys on actor uid)
	if rec := runRateLimitRequest(t, "/v1/me/erase", failDistributedLimiter{}, local); rec.Code != http.StatusTooManyRequests {
		t.Errorf("sensitive erase with local = %d, want 429", rec.Code)
	}
}

// TestRateLimitSensitiveKeepsThrottlingWhenRedisHealthy: a Redis decision of
// "denied" still yields 429 on sensitive paths.
func TestRateLimitSensitiveKeepsThrottlingWhenRedisHealthy(t *testing.T) {
	deny := ratelimit.DenyAllLimiter{}
	if rec := runRateLimitRequest(t, "/v1/me/erase", deny, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("sensitive erase denied = %d, want 429", rec.Code)
	}
	if rec := runRateLimitRequest(t, "/v1/me", deny, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("interactive read denied = %d, want 429", rec.Code)
	}
}

func TestIsRateLimitSensitive(t *testing.T) {
	cases := map[string]bool{
		"/v1/me/erase":                true,
		"/v1/me/export":               true,
		"/v1/me/sync":                 true,
		"/v1/sync/jobs/123/reconcile": true,
		"/v1/audit/events":            true,
		"/v1/admin/accounts":          true,
		"/v1/operations/123/run":      true,
		"/internal/v1/events":         true,
		"/v1/me":                      false,
		"/v1/me/overview":             false,
		"/v1/files":                   false,
		"/v1/messages":                false,
		"/v1/notifications":           false,
	}
	for path, want := range cases {
		if got := isRateLimitSensitive(path); got != want {
			t.Errorf("isRateLimitSensitive(%q) = %v, want %v", path, got, want)
		}
	}
}
