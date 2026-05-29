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
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/idempotency"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/db"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/pkg/tracing"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
	"github.com/aero/aero-id/internal/storage"
	storagegc "github.com/aero/aero-id/internal/storage/gc"
)

// presignTTL is how long a generated download URL stays valid.
const presignTTL = 5 * time.Minute

// outboxPollInterval is how often the relay drains unpublished domain events.
const outboxPollInterval = 5 * time.Second

type App struct {
	cfg           *config.Config
	log           *zap.Logger
	sqlDB         *sql.DB
	sso           *sso.Client
	httpServer    *http.Server
	grpcServer    *grpclib.Server
	grpcLis       net.Listener
	stopRelay     func()
	stopGC        func()
	traceShutdown func(context.Context) error
}

// New builds the fully wired application. It opens the DB, storage, and SSO
// connections; constructs repositories, services, and both transports.
func New(ctx context.Context, cfg *config.Config, log *zap.Logger) (*App, error) {
	gdb, err := db.Open(db.Options{
		DSN:             cfg.Database.DSN(),
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		LogLevel:        cfg.Database.LogLevel,
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("sql db handle: %w", err)
	}

	store, downloader, err := buildStorage(ctx, cfg)
	if err != nil {
		return nil, err
	}

	// Tracing first so the global tracer is installed before the SSO client (whose
	// client spans bind a tracer at construction). No-op unless tracing is configured.
	traceShutdown, err := tracing.Init(ctx, tracing.Config{
		Enabled:        cfg.Tracing.Enabled,
		Endpoint:       cfg.Tracing.Endpoint,
		Insecure:       cfg.Tracing.Insecure,
		Timeout:        cfg.Tracing.Timeout,
		SampleRatio:    cfg.Tracing.SampleRatio,
		ServiceName:    cfg.App.Name,
		ServiceVersion: cfg.App.Version,
		Environment:    cfg.App.Env,
	})
	if err != nil {
		return nil, fmt.Errorf("tracing init: %w", err)
	}

	// One metrics registry, shared by HTTP/gRPC RED metrics and SSO health gauges.
	metricsReg := metrics.NewRegistry()

	ssoClient, err := sso.NewClient(ctx, cfg.SSO.Endpoint, sso.TLSConfig{
		Insecure:   cfg.SSO.Insecure,
		CAFile:     cfg.SSO.TLSCAFile,
		CertFile:   cfg.SSO.TLSCertFile,
		KeyFile:    cfg.SSO.TLSKeyFile,
		ServerName: cfg.SSO.TLSServerName,
	}, sso.ClientConfig{
		TokenCacheTTL:    cfg.SSO.TokenCacheTTL,
		UserCacheTTL:     cfg.SSO.UserCacheTTL,
		StaleTTL:         cfg.SSO.StaleTTL,
		Timeout:          cfg.SSO.Timeout,
		BreakerThreshold: cfg.SSO.BreakerThreshold,
		BreakerCooldown:  cfg.SSO.BreakerCooldown,
		MaxAttempts:      cfg.SSO.MaxAttempts,
		RetryBackoff:     cfg.SSO.RetryBackoff,
		KeepaliveTime:    cfg.SSO.KeepaliveTime,
		KeepaliveTimeout: cfg.SSO.KeepaliveTimeout,
		Metrics:          metricsReg,
	})
	if err != nil {
		return nil, fmt.Errorf("sso client: %w", err)
	}

	fileRepo := repository.NewFileRepository(gdb)
	fileShareRepo := repository.NewFileShareRepository(gdb)
	outboxRepo := outbox.NewGormRepository(gdb)
	msgRepo := repository.NewMessageRepository(gdb, outboxRepo)
	commentRepo := repository.NewCommentRepository(gdb, outboxRepo)
	notifRepo := repository.NewNotificationRepository(gdb)

	// The relay drains domain events written transactionally with their rows and
	// fans them out to per-recipient notification rows (Direction 5).
	dispatcher := service.NewNotificationDispatcher(notifRepo)
	relay := outbox.NewRelay(outboxRepo, dispatcher, log)
	stopRelay := relay.Start(outboxPollInterval)

	// Storage GC reconciliation sweep. OPT-IN and disabled by default: it only starts
	// when storage.gc.enabled, so an operator must explicitly turn it on. It purges
	// soft-deleted file rows past their retention (deleting unreferenced blobs) and,
	// optionally, sweeps orphaned blobs the create path may have leaked.
	var stopGC func()
	if cfg.Storage.GC.Enabled {
		sweeper := storagegc.New(fileRepo, store, storagegc.Config{
			Retention:    cfg.Storage.GC.Retention,
			MinOrphanAge: cfg.Storage.GC.MinOrphanAge,
			PurgeBatch:   cfg.Storage.GC.PurgeBatch,
			SweepOrphans: cfg.Storage.GC.SweepOrphans,
		}, log)
		stopGC = sweeper.Start(cfg.Storage.GC.Interval)
		log.Info("storage gc sweep enabled",
			zap.Duration("interval", cfg.Storage.GC.Interval),
			zap.Duration("retention", cfg.Storage.GC.Retention),
			zap.Bool("sweep_orphans", cfg.Storage.GC.SweepOrphans))
	}

	fileSvc := service.NewFileService(fileRepo, store, cfg.Storage.Type,
		service.WithMaxUploadBytes(cfg.Storage.MaxUploadBytes),
		service.WithAllowedMimeTypes(cfg.Storage.AllowedMimeTypes),
		service.WithFileShareRepo(fileShareRepo),
	)
	msgSvc := service.NewMessageService(msgRepo, fileRepo, ssoClient)
	commentSvc := service.NewCommentService(commentRepo, fileRepo, msgRepo, ssoClient)
	notifSvc := service.NewNotificationService(notifRepo, ssoClient)

	// HTTP transport. Readiness probes the DB (critical) and SSO (criticality is
	// configurable so an SSO-less dev stack stays "ready" rather than 503-ing).
	idemStore := idempotency.NewGormStore(gdb)
	health := httpapi.NewHealthHandler(
		httpapi.Check{Name: "database", Critical: true, Probe: sqlDB.PingContext},
		httpapi.Check{Name: "sso", Critical: cfg.SSO.ReadinessCritical, Probe: ssoClient.HealthCheck},
	)
	router := httpapi.NewRouter(
		log,
		httpapi.RouterConfig{
			Metrics:        metricsReg,
			Idempotency:    idemStore,
			IdempotencyTTL: cfg.Server.HTTP.IdempotencyTTL,
		},
		ssoClient,
		health,
		httpapi.NewFileHandler(fileSvc, downloader, presignTTL),
		httpapi.NewMessageHandler(msgSvc),
		httpapi.NewCommentHandler(commentSvc),
		httpapi.NewNotificationHandler(notifSvc),
	)
	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.HTTP.Host, cfg.Server.HTTP.Port),
		Handler:      router,
		ReadTimeout:  cfg.Server.HTTP.ReadTimeout,
		WriteTimeout: cfg.Server.HTTP.WriteTimeout,
	}

	// gRPC transport
	grpcServer := grpcsrv.NewServer(
		ssoClient, log, metricsReg,
		grpcsrv.NewFileServer(fileSvc),
		grpcsrv.NewMessageServer(msgSvc),
		grpcsrv.NewCommentServer(commentSvc),
	)
	grpcAddr := fmt.Sprintf("%s:%d", cfg.Server.GRPC.Host, cfg.Server.GRPC.Port)
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		stopRelay()
		if stopGC != nil {
			stopGC()
		}
		return nil, fmt.Errorf("grpc listen on %s: %w", grpcAddr, err)
	}

	return &App{
		cfg:           cfg,
		log:           log,
		sqlDB:         sqlDB,
		sso:           ssoClient,
		httpServer:    httpServer,
		grpcServer:    grpcServer,
		grpcLis:       grpcLis,
		stopRelay:     stopRelay,
		stopGC:        stopGC,
		traceShutdown: traceShutdown,
	}, nil
}

// buildStorage selects the configured backend. Only the local backend serves
// signature-verified downloads through aero-id; S3 presigned URLs target the
// object store directly, so its downloader is nil.
func buildStorage(ctx context.Context, cfg *config.Config) (storage.FileStorage, httpapi.SignedDownloader, error) {
	switch cfg.Storage.Type {
	case "local":
		local := storage.NewLocal(storage.LocalConfig{
			Root:          cfg.Storage.Local.Root,
			SigningKey:    cfg.SigningKey,
			PublicBaseURL: cfg.Storage.Local.PublicBaseURL,
		})
		return local, local, nil
	case "s3":
		s3, err := storage.NewS3(ctx, storage.S3Config{
			Region:       cfg.Storage.S3.Region,
			Bucket:       cfg.Storage.S3.Bucket,
			Endpoint:     cfg.Storage.S3.Endpoint,
			AccessKey:    cfg.Storage.S3.AccessKey,
			SecretKey:    cfg.Storage.S3.SecretKey,
			UsePathStyle: cfg.Storage.S3.UsePathStyle,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("s3 storage: %w", err)
		}
		return s3, nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown storage type %q", cfg.Storage.Type)
	}
}

// Run starts both servers and blocks until ctx is cancelled or a server fails,
// then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
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
	if a.stopRelay != nil {
		a.stopRelay()
	}
	if a.stopGC != nil {
		a.stopGC()
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
	if a.sqlDB != nil {
		_ = a.sqlDB.Close()
	}
}
