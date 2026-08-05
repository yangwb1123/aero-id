package app

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// warmup runs a set of non-critical startup probes in the background to
// pre-warm connections and caches before the first real request arrives.
// Failures are logged but never fail startup.
func (a *App) warmup(ctx context.Context) {
	go func() {
		// 1. Database: establish a pooled connection with a trivial query.
		if a.sqlDB != nil {
			warnCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var n int
			if err := a.sqlDB.QueryRowContext(warnCtx, "SELECT 1").Scan(&n); err != nil {
				a.log.Warn("warmup: db ping failed", zap.Error(err))
			} else {
				a.log.Debug("warmup: db ping ok")
			}
			cancel()
		}

		// 2. SSO cache: warmup already runs synchronously in Run(), but we
		// log a summary here for consistency.
		a.log.Debug("warmup: sso probe completed earlier")

		a.log.Info("cold-start warmup complete")
	}()
}
