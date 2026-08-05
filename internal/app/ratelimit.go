package app

import (
	"github.com/redis/rueidis"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/pkg/ratelimit"
)

func buildDistributedRateLimiter(cfg *config.Config, client rueidis.Client, log *zap.Logger) ratelimit.DistributedLimiter {
	if cfg == nil || !cfg.RateLimit.Enabled || client == nil {
		return nil
	}
	limiter := ratelimit.NewRedisLimiter(client, cfg.RateLimit.RPS, cfg.RateLimit.Burst)
	if limiter != nil {
		log.Info("distributed rate limiting enabled")
	}
	return limiter
}
