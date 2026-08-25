// Package app is aero-id's composition root: it wires config + logger into the
// repository, storage, SSO, service, and transport layers, then runs both the
// HTTP and gRPC servers with graceful shutdown.
package app

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"
	grpclib "google.golang.org/grpc"

	"github.com/aero/aero-id/internal/config"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/sso"
	"github.com/redis/rueidis"
)

// presignTTL is how long a generated download URL stays valid.
const presignTTL = 5 * time.Minute

type App struct {
	cfg             *config.Config
	log             *zap.Logger
	sqlDB           *sql.DB
	sso             *sso.Client
	redisClient     rueidis.Client
	httpServer      *http.Server
	grpcServer      *grpclib.Server
	grpcHealth      *grpcsrv.HealthServer
	httpHealth      interface{ SetReady(bool) }
	grpcLis         net.Listener
	stopRelay       func()
	stopGC          func()
	stopRateLimit   func()
	stopIdemClean   func()
	stopAuditClean  func()
	stopNotifClean  func()
	stopOutboxClean func()
	stopCallbacks   func()
	drainOperations func()
	stopOperations  func()
	stopSyncJobs    func()
	stopEventBus    func()
	stopReplication func()
	stopDegradation func()
	traceShutdown   func(context.Context) error
}

// rateLimitDefaults fill in sane values when an operator enables limiting but
// leaves the tuning knobs at zero.
const (
	defaultRateLimitIdleTTL      = 10 * time.Minute
	defaultRateLimitCleanupEvery = time.Minute
)

// Run starts both servers and blocks until ctx is cancelled or a server fails,
// then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	// SSO cold-start warmup: probe the SSO connection before serving real
	// requests so the circuit breaker starts from a known-good state rather
	// than fast-failing the first request while the connection is idle.
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if err := a.sso.HealthCheck(probeCtx); err != nil {
		a.log.Warn("sso warmup probe failed (requests will fall back to stale cache or breaker)", zap.String("endpoint", a.cfg.SSO.Endpoint), zap.Error(err))
	} else {
		a.log.Info("sso warmup probe ok")
	}
	cancel()

	errCh := make(chan error, 2)
	go func() {
		a.log.Info("grpc server starting", zap.String("addr", a.grpcLis.Addr().String()))
		if err := a.grpcServer.Serve(a.grpcLis); err != nil {
			errCh <- fmt.Errorf("grpc serve: %w", err)
		}
	}()
	go func() {
		a.log.Info("http server starting", zap.String("addr", a.httpServer.Addr))
		if err := a.httpServer.ListenAndServe(); err != nil && !stderrors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http serve: %w", err)
		}
	}()

	// Cold-start warmup: run lightweight probes in the background so the first
	// real requests don't pay the connection-establishment latency on every
	// dependency. This runs AFTER servers start listening so health checks and
	// early traffic are not delayed.
	a.warmup(context.Background())

	select {
	case <-ctx.Done():
		a.log.Info("shutdown signal received")
	case err := <-errCh:
		a.log.Error("server error, shutting down", zap.Error(err))
		a.shutdown()
		return err
	}
	a.shutdown()
	return nil
}

func (a *App) shutdown() {
	// Fence operation writers and mark both transports unready before stopping
	// workers. In-flight source calls receive cancellation, while their durable
	// finalization uses an independent persistence context.
	if a.drainOperations != nil {
		a.drainOperations()
	}
	if a.httpHealth != nil {
		a.httpHealth.SetReady(false)
	}
	if a.grpcHealth != nil {
		a.grpcHealth.SetServing(false)
	}
	if a.stopRelay != nil {
		a.stopRelay()
	}
	if a.stopGC != nil {
		a.stopGC()
	}
	if a.stopRateLimit != nil {
		a.stopRateLimit()
	}
	if a.stopIdemClean != nil {
		a.stopIdemClean()
	}
	if a.stopAuditClean != nil {
		a.stopAuditClean()
	}
	if a.stopNotifClean != nil {
		a.stopNotifClean()
	}
	if a.stopOutboxClean != nil {
		a.stopOutboxClean()
	}
	if a.stopCallbacks != nil {
		a.stopCallbacks()
	}
	if a.stopOperations != nil {
		a.stopOperations()
	}
	if a.stopSyncJobs != nil {
		a.stopSyncJobs()
	}
	if a.stopEventBus != nil {
		a.stopEventBus()
	}
	if a.stopReplication != nil {
		a.stopReplication()
	}
	if a.stopDegradation != nil {
		a.stopDegradation()
	}
	shCtx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.HTTP.ShutdownTimeout)
	defer cancel()
	if err := a.httpServer.Shutdown(shCtx); err != nil {
		a.log.Error("http shutdown error", zap.Error(err))
	}
	a.grpcServer.GracefulStop()
	if a.traceShutdown != nil {
		if err := a.traceShutdown(shCtx); err != nil {
			a.log.Error("tracer shutdown error", zap.Error(err))
		}
	}
}

// Close releases long-lived resources. Safe to call after Run returns.
func (a *App) Close() {
	if a.sso != nil {
		a.sso.Close()
	}
	if a.redisClient != nil {
		a.redisClient.Close()
	}
	if a.sqlDB != nil {
		_ = a.sqlDB.Close()
	}
}
