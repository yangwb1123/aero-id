package http

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/metrics"
)

// RouteRegistrar lets each feature handler attach its own routes without the
// router importing the concrete handler types — keeps the package decoupled and
// lets handlers be added task-by-task.
type RouteRegistrar interface {
	RegisterRoutes(r Routes)
}

// Routes carries the groups handed to each registrar.
type Routes struct {
	Auth   *gin.RouterGroup // mounted at /v1, requires a valid SSO token
	Public *gin.RouterGroup // anonymous, e.g. signature-verified file downloads
}

// RouterConfig holds cross-cutting router settings.
type RouterConfig struct {
	CORSOrigins []string
	Metrics     *metrics.Registry // when set, records RED metrics and serves /metrics
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
	e.Use(middleware.Recovery(log))
	if cfg.Metrics != nil {
		e.Use(middleware.Metrics(cfg.Metrics))
	}
	e.Use(middleware.AccessLog(log))
	e.Use(middleware.CORS(cfg.CORSOrigins))

	e.GET("/healthz", health.Live)
	e.GET("/readyz", health.Ready)
	if cfg.Metrics != nil {
		e.GET("/metrics", gin.WrapH(cfg.Metrics.Handler()))
	}

	routes := Routes{
		Public: e.Group(""),
		Auth:   e.Group("/v1", middleware.Auth(validator)),
	}
	for _, r := range registrars {
		r.RegisterRoutes(routes)
	}
	return e
}
