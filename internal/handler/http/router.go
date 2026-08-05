package http

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/idempotency"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/pkg/ratelimit"
)

// RouteRegistrar lets each feature handler attach its own routes without the
// router importing the concrete handler types — keeps the package decoupled and
// lets handlers be added task-by-task.
type RouteRegistrar interface {
	RegisterRoutes(r Routes)
}

// Routes carries the groups handed to each registrar.
type Routes struct {
	Auth         *gin.RouterGroup // mounted at /v1, requires a valid SSO token
	InternalAuth *gin.RouterGroup // mounted at /, requires the same valid SSO token
	Public       *gin.RouterGroup // anonymous, e.g. signature-verified file downloads
}

// RouterConfig holds cross-cutting router settings.
type RouterConfig struct {
	CORSOrigins            []string
	Metrics                *metrics.Registry // when set, records RED metrics and serves /metrics
	Idempotency            idempotency.Store // when set, makes keyed POSTs under /v1 safe to retry
	IdempotencyTTL         time.Duration
	RateLimiter            *ratelimit.Limiter           // when set & enabled, throttles per actor uid (IP for anon)
	DistributedRateLimiter ratelimit.DistributedLimiter // optional Redis/global limiter
	RequestTimeout         time.Duration                // when > 0, sets a context deadline on every request
	SampleRatio            float64                      // 0..1 access log sample ratio; <=0 disables sampling, >=1 logs all
	BanChecker             middleware.BanChecker        // when non-nil, inserts Ban middleware between Auth and RateLimit
	Degradation            *middleware.DegradationState // when set, writes X-Aero-Degraded response header
	LegacySunset           string                       // when set, legacy compat routes carry RFC 8594 Deprecation+Sunset headers
	RegionEpoch            *middleware.RegionEpochHeader // when set, emits X-Aero-Write-Epoch and flags stale reads (P6-05)
	CutoverChecker         middleware.CutoverChecker     // when set, cut-over tenants get 410 on the legacy surface (P8-04)
}

// NewRouter assembles the gin engine: global middleware, health probes, and the
// authenticated /v1 group, then lets every registrar attach its routes.
func NewRouter(
	log *zap.Logger,
	cfg RouterConfig,
	validator middleware.TokenValidator,
	health *HealthHandler,
	registrars ...RouteRegistrar,
) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()
	e.Use(middleware.RequestID())
	e.Use(middleware.Tracing())
	e.Use(middleware.Recovery(log))
	if cfg.Metrics != nil {
		e.Use(middleware.Metrics(cfg.Metrics))
	}
	// Access log sampling: the Sample middleware (when ratio < 1) marks requests
	// so that AccessLog only emits lines for a fraction of info-level calls.
	// Error responses (status >= 400) are never sampled away.
	e.Use(middleware.Sample(cfg.SampleRatio))
	e.Use(middleware.AccessLog(log))
	e.Use(middleware.CORS(cfg.CORSOrigins))
	if cfg.Degradation != nil {
		e.Use(middleware.DegradationHeader(cfg.Degradation))
	}
	e.Use(middleware.Timeout(cfg.RequestTimeout))

	e.GET("/healthz", health.Live)
	e.GET("/readyz", health.Ready)
	if cfg.Metrics != nil {
		e.GET("/metrics", gin.WrapH(cfg.Metrics.Handler()))
	}

	authMW := authenticatedMiddleware(cfg, validator)
	if cfg.LegacySunset != "" {
		// ADR-011: files/messages/comments stay a compatibility source; mark them
		// without breaking current clients. Path matching happens inside the middleware.
		authMW = append(authMW, middleware.LegacyDeprecation(cfg.LegacySunset))
	}
	if cfg.RegionEpoch != nil {
		// P6-05 read-your-writes token: tag successful writes with the writer
		// generation; flag reads pinned to a stale generation.
		authMW = append(authMW, cfg.RegionEpoch.Middleware())
	}
	if cfg.CutoverChecker != nil {
		// P8-04: cut-over tenants get an explicit 410 on the legacy surface.
		authMW = append(authMW, middleware.LegacyCutoverGuard(cfg.CutoverChecker))
	}
	publicMW := publicMiddleware(cfg)
	routes := Routes{
		Public:       e.Group("", publicMW...),
		Auth:         e.Group("/v1", authMW...),
		InternalAuth: e.Group("", authMW...),
	}
	for _, r := range registrars {
		r.RegisterRoutes(routes)
	}
	return e
}

func authenticatedMiddleware(cfg RouterConfig, validator middleware.TokenValidator) []gin.HandlerFunc {
	// Ban runs after auth and before rate limiting so rejected users do not consume
	// a rate-limit slot. Idempotency keys are scoped by the authenticated actor.
	result := []gin.HandlerFunc{middleware.Auth(validator)}
	if cfg.BanChecker != nil {
		result = append(result, middleware.Ban(cfg.BanChecker))
	}
	result = append(result, rateLimitMiddleware(cfg)...)
	if cfg.Idempotency != nil {
		result = append(result, middleware.Idempotency(cfg.Idempotency, cfg.IdempotencyTTL))
	}
	return result
}

func publicMiddleware(cfg RouterConfig) []gin.HandlerFunc { return rateLimitMiddleware(cfg) }

func rateLimitMiddleware(cfg RouterConfig) []gin.HandlerFunc {
	if cfg.RateLimiter != nil {
		return []gin.HandlerFunc{middleware.RateLimit(cfg.RateLimiter, cfg.DistributedRateLimiter)}
	}
	if cfg.DistributedRateLimiter != nil {
		return []gin.HandlerFunc{middleware.RateLimit(nil, cfg.DistributedRateLimiter)}
	}
	return nil
}
