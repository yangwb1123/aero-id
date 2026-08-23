package app

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"github.com/redis/rueidis"

	"github.com/aero/aero-id/internal/config"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/idempotency"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/ratelimit"
	redislib "github.com/aero/aero-id/internal/pkg/redis"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
	"github.com/aero/aero-id/internal/storage"
)

// workerDependencies carries the shared-state and worker pieces the
// composition needs; extracted from new.go so the composition root stays
// under the file-size gate (C-7). Mirrors the outboxDependencies/
// grpcDependencies struct pattern.
type workerDependencies struct {
	cfg         *config.Config
	log         *zap.Logger
	gdb         *gorm.DB
	sqlDB       *sql.DB
	ssoClient   *sso.Client
	notifRepo   repository.NotificationRepository
	outboxRepo  *outbox.GormRepository
	holdRepo    repository.AuditHoldRepository
	auditLedger     service.AuditLedgerService
	auditLedgerRepo repository.FullAuditLedgerRepository
	store       storage.FileStorage
	billingRuntime billingRuntime
	cutoverSvc  service.LegacyCutoverService
}

// workerBundle carries the shared state and worker-stop functions the rest
// of the composition root needs (App fields, the router, the gRPC server and
// the failure path), so builder signatures stay small.
type workerBundle struct {
	redisClient        rueidis.Client
	idemStore          idempotency.Store
	limiter            *ratelimit.Limiter
	distributedLimiter ratelimit.DistributedLimiter

	stopRateLimit   func()
	stopIdemClean   func()
	stopNotifClean  func()
	stopOutboxClean func()
	stopDegradation func()

	degradationState *middleware.DegradationState
	health           *httpapi.HealthHandler
	holdSvc          service.AuditHoldService
	anchorSvc        service.AuditAnchorService
	cutoverFacadeSvc service.LegacyCutoverService
}

// buildWorkers constructs the shared state (Redis, edge/distributed rate
// limiting, idempotency store), the OPT-IN TTL cleaners, the degradation
// state manager, the health handler, and the audit compliance services
// (extracted from new.go so the composition root stays under the file-size
// gate). The audit TTL cleaner (C-6) is deliberately NOT here: it stays in
// new.go, pinned by check-composition-root.
func buildWorkers(deps workerDependencies) (workerBundle, error) {
	// Redis client for shared state (idempotency store, SSO cache, leader election,
	// rate limiter back-end, etc.). OPT-IN: when redis.endpoint is empty every Redis
	// feature falls back to its GORM / in-memory alternative.
	redisClient, err := redislib.NewClient(&deps.cfg.Redis)
	if err != nil {
		return workerBundle{}, fmt.Errorf("redis client: %w", err)
	}
	if redisClient != nil {
		deps.log.Info("redis client enabled", zap.String("endpoint", deps.cfg.Redis.Endpoint))
	}

	// Edge rate limiter, shared by HTTP middleware and the gRPC interceptor. OPT-IN
	// and disabled by default; Redis provides the cross-instance decision while the
	// local bucket remains a bounded fallback. When on, a background cleaner evicts
	// idle local buckets to bound memory.
	var limiter *ratelimit.Limiter
	var stopRateLimit func()
	if deps.cfg.RateLimit.Enabled {
		idleTTL := deps.cfg.RateLimit.IdleTTL
		if idleTTL <= 0 {
			idleTTL = defaultRateLimitIdleTTL
		}
		cleanupEvery := deps.cfg.RateLimit.CleanupEvery
		if cleanupEvery <= 0 {
			cleanupEvery = defaultRateLimitCleanupEvery
		}
		limiter = ratelimit.New(deps.cfg.RateLimit.RPS, deps.cfg.RateLimit.Burst, idleTTL)
		stopRateLimit = limiter.RunCleaner(cleanupEvery)
		deps.log.Info("rate limiting enabled",
			zap.Float64("rps", deps.cfg.RateLimit.RPS),
			zap.Int("burst", deps.cfg.RateLimit.Burst),
			zap.Duration("idle_ttl", idleTTL))
	}
	distributedRateLimiter := buildDistributedRateLimiter(deps.cfg, redisClient, deps.log)

	// HTTP transport. Readiness probes the DB (critical) and SSO (criticality is
	// configurable so an SSO-less dev stack stays "ready" rather than 503-ing).
	// Idempotency store: use Redis when available, fall back to GORM.
	var idemStore idempotency.Store
	var stopIdemClean func()
	if redisClient != nil {
		idemStore = idempotency.NewRedisStore(redisClient)
		stopIdemClean = func() {} // Redis keys auto-expire; no background cleaner needed
		deps.log.Info("idempotency store: redis")
	} else {
		gormStore := idempotency.NewGormStore(deps.gdb)
		idemStore = gormStore
		stopIdemClean = gormStore.RunCleaner(time.Hour)
		deps.log.Info("idempotency store: gorm (no redis configured)")
	}

	// Notifications TTL cleaner: purges read notifications past retention and
	// unread notifications past unread retention. OPT-IN.
	var stopNotifClean func()
	if deps.cfg.DataRetention.NotificationCleanInterval > 0 && deps.cfg.DataRetention.NotificationRetention > 0 {
		stopNotifClean = repository.RunNotificationCleaner(
			deps.notifRepo, deps.log,
			deps.cfg.DataRetention.NotificationRetention,
			deps.cfg.DataRetention.NotificationUnreadRetention,
			deps.cfg.DataRetention.NotificationCleanInterval,
		)
	}

	// Outbox events TTL cleaner: purges dead letters and published events past
	// their respective retentions. OPT-IN.
	var stopOutboxClean func()
	if deps.cfg.DataRetention.OutboxCleanInterval > 0 && deps.cfg.DataRetention.OutboxDeadLetterRetention > 0 {
		stopOutboxClean = buildOutboxCleaner(deps.cfg, deps.log, deps.outboxRepo)
	}

	// Degradation state manager — tracks upstream dependency health for the
	// X-Aero-Degraded response header and handler adaptation.
	// Probes DB and SSO on a 30s interval, marking degraded after 3 consecutive
	// failures and restored after 2 consecutive successes.
	degradationState := middleware.NewDegradationState()
	degradationState.SetProber(middleware.DepDB, deps.sqlDB.PingContext)
	if deps.ssoClient != nil {
		degradationState.SetProber(middleware.DepSSO, deps.ssoClient.HealthCheck)
	}
	stopDegradation := degradationState.StartProbing(context.Background(), 30*time.Second, 3, 2)

	health := buildHealthHandler(deps.cfg, deps.sqlDB, deps.ssoClient, deps.billingRuntime.probe)
	holdSvc, anchorSvc, cutoverFacadeSvc := buildAuditComplianceServices(deps.gdb, deps.auditLedger, deps.auditLedgerRepo, deps.store, deps.holdRepo)
	// Fail-closed wiring: compliance services must not run with a nil audit service.
	assertComplianceAuditWiring(holdSvc, anchorSvc, deps.cutoverSvc, cutoverFacadeSvc)

	return workerBundle{
		redisClient: redisClient, idemStore: idemStore,
		limiter: limiter, distributedLimiter: distributedRateLimiter,
		stopRateLimit: stopRateLimit, stopIdemClean: stopIdemClean,
		stopNotifClean: stopNotifClean, stopOutboxClean: stopOutboxClean,
		stopDegradation: stopDegradation, degradationState: degradationState,
		health: health, holdSvc: holdSvc, anchorSvc: anchorSvc,
		cutoverFacadeSvc: cutoverFacadeSvc,
	}, nil
}
