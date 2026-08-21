package app

import (
	"context"
	"fmt"
	"net"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/config"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/hub"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/db"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/pkg/migrate"
	"github.com/aero/aero-id/internal/pkg/tracing"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
)

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

	// Optional startup auto-migrate. OFF by default so production applies
	// migrations explicitly (via cmd/migrate); when database.auto_migrate is
	// set we run all pending up-migrations before any server starts serving.
	if cfg.Database.AutoMigrate {
		dir := cfg.Database.MigrationsDir
		if dir == "" {
			dir = "migrations"
		}
		m := migrate.New(gdb, dir, func(format string, a ...any) {
			log.Info("auto-migrate: " + fmt.Sprintf(format, a...))
		})
		applied, err := m.Up()
		if err != nil {
			return nil, fmt.Errorf("auto-migrate: %w", err)
		}
		if len(applied) == 0 {
			log.Info("auto-migrate: schema already up to date")
		} else {
			log.Info("auto-migrate: applied pending migrations",
				zap.Int("count", len(applied)),
				zap.Uint64("version", applied[len(applied)-1]))
		}
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

	// One metrics registry (HTTP/gRPC RED + control-plane sink).
	metricsReg := metrics.NewRegistry()
	controlMetrics := service.NewRegistryControlPlaneMetrics(metricsReg)

	ssoClient, err := sso.NewClient(ctx, cfg.SSO.Endpoint, sso.TLSConfig{
		Insecure:   cfg.SSO.Insecure,
		CAFile:     cfg.SSO.TLSCAFile,
		CertFile:   cfg.SSO.TLSCertFile,
		KeyFile:    cfg.SSO.TLSKeyFile,
		ServerName: cfg.SSO.TLSServerName,
		CertPins:   cfg.SSO.TLSCertPins,
	}, sso.ClientConfig{
		AuthZClientID:    cfg.SSO.AuthZClientID,
		TokenCacheTTL:    cfg.SSO.TokenCacheTTL,
		UserCacheTTL:     cfg.SSO.UserCacheTTL,
		StaleTTL:         cfg.SSO.StaleTTL,
		Timeout:          cfg.SSO.Timeout,
		BreakerThreshold: cfg.SSO.BreakerThreshold,
		BreakerCooldown:  cfg.SSO.BreakerCooldown,
		MaxAttempts:      cfg.SSO.MaxAttempts,
		RetryBackoff:     cfg.SSO.RetryBackoff,
		MaxConcurrent:    cfg.SSO.MaxConcurrent,
		UserBatchSize:    cfg.SSO.UserBatchSize,
		KeepaliveTime:    cfg.SSO.KeepaliveTime,
		KeepaliveTimeout: cfg.SSO.KeepaliveTimeout,
		Metrics:          metricsReg,
	})
	if err != nil {
		return nil, fmt.Errorf("sso client: %w", err)
	}
	sourceConnectors, err := buildSourceConnectors(cfg, metricsReg)
	if err != nil {
		return nil, err
	}
	tokenValidator, err := buildTokenValidator(cfg, ssoClient, log)
	if err != nil {
		return nil, err
	}

	auditRepo := repository.NewAuditRepository(gdb)
	callbackSvc, auditLedger, auditLedgerRepo, err := buildAuditStack(cfg, gdb, controlMetrics)
	if err != nil {
		return nil, err
	}
	// P4-07: one shared hold registry for the audit TTL cleaner and the hold service.
	holdRepo := repository.NewAuditHoldRepository(gdb, auditLedgerRepo)
	// F-1 (storage GC audit): NewFileRepository gains the ledger for the
	// HardDeleteWithAudit sweep surface, so it is constructed AFTER the audit
	// stack (mirroring the fileShareRepo placement).
	fileRepo := repository.NewFileRepository(gdb, auditLedgerRepo)
	fileShareRepo := repository.NewFileShareRepository(gdb, auditLedgerRepo)
	outboxRepo := outbox.NewGormRepository(gdb)
	// Materialized unread counter: shared by message and notification repos.
	unreadCounter := repository.NewUnreadCounter(gdb)
	msgRepo := repository.NewMessageRepository(gdb, outboxRepo, auditLedgerRepo, unreadCounter)
	commentRepo := repository.NewCommentRepository(gdb, outboxRepo, auditLedgerRepo)
	notifRepo := repository.NewNotificationRepository(gdb, unreadCounter)
	blockRepo := repository.NewBlockRepository(gdb, auditLedgerRepo)
	banRepo := repository.NewBanRepository(gdb, auditLedgerRepo)
	prefRepo := repository.NewPreferenceRepository(gdb)
	storageRefRepo := repository.NewStorageRefRepository(gdb)
	adminRepo := repository.NewAdminRepository(gdb, auditLedgerRepo)
	accountRepo := repository.NewAccountRepository(gdb, auditLedgerRepo)
	billingRuntime, err := buildBillingRuntime(cfg, gdb)
	if err != nil {
		return nil, err
	}
	snapshotRepo := repository.NewAccountSnapshotRepository(gdb)
	eventReceiptRepo := repository.NewEventReceiptRepositoryWithStore(gdb,
		repository.NewEventStoreRepository(gdb), auditLedgerRepo)
	operationRepo := repository.NewOperationRepository(gdb, auditLedgerRepo)
	syncJobRepo := repository.NewSyncJobRepository(gdb, auditLedgerRepo)

	// SSE hub: delivers real-time notification pushes to connected clients.
	sseHub := hub.New()
	stopRelay := buildOutboxRelay(outboxDependencies{
		cfg: cfg, log: log, outboxRepo: outboxRepo, notifRepo: notifRepo,
		blockRepo: blockRepo, prefRepo: prefRepo, sseHub: sseHub, metricsReg: metricsReg,
	})
	stopCallbacks := callbackSvc.Start(context.Background(), cfg.Callback.WorkerInterval)

	// Storage GC reconciliation sweep. OPT-IN and disabled by default; purges
	// soft-deleted file rows past retention (deleting unreferenced blobs) and,
	// optionally, sweeps orphaned blobs. The destructive phases are audited
	// (in-tx row purge + checked reclamation observations).
	var stopGC func()
	if cfg.Storage.GC.Enabled {
		stopGC = buildStorageGCSweeper(cfg, log, fileRepo, storageRefRepo, store, auditLedger)
	} else if cfg.Storage.Type == "s3" {
		log.Warn("storage gc is DISABLED with s3 backend — stale blobs will accumulate until gc.enabled is true",
			zap.String("storage_type", cfg.Storage.Type))
	}

	// Service construction: every domain service, the permission checker
	// selection, and the fail-closed audit-wiring asserts. Extracted to
	// services.go so the composition root stays under the file-size gate (C-7).
	svcs, err := buildServices(serviceDependencies{
		cfg: cfg, log: log, gdb: gdb, store: store,
		ssoClient: ssoClient, sourceConnectors: sourceConnectors,
		controlMetrics: controlMetrics, metricsReg: metricsReg,
		billingRuntime: billingRuntime,
		auditLedger:    auditLedger, auditLedgerRepo: auditLedgerRepo,
		fileRepo: fileRepo, fileShareRepo: fileShareRepo,
		msgRepo: msgRepo, commentRepo: commentRepo, notifRepo: notifRepo,
		blockRepo: blockRepo, banRepo: banRepo, prefRepo: prefRepo,
		storageRefRepo: storageRefRepo, adminRepo: adminRepo, accountRepo: accountRepo,
		snapshotRepo: snapshotRepo, eventReceiptRepo: eventReceiptRepo,
		operationRepo: operationRepo, syncJobRepo: syncJobRepo,
	})
	if err != nil {
		return nil, err
	}

	// Shared state and worker setup: Redis, edge/distributed rate limiting,
	// idempotency store, notification/outbox TTL cleaners, degradation state,
	// health handler, and the audit compliance services. Extracted to
	// workers.go so the composition root stays under the file-size gate (C-7).
	workers, err := buildWorkers(workerDependencies{
		cfg: cfg, log: log, gdb: gdb, sqlDB: sqlDB,
		ssoClient: ssoClient, notifRepo: notifRepo,
		outboxRepo: outboxRepo, holdRepo: holdRepo,
		auditLedger: auditLedger, auditLedgerRepo: auditLedgerRepo,
		store: store, billingRuntime: billingRuntime,
		cutoverSvc: svcs.cutoverSvc,
	})
	if err != nil {
		return nil, err
	}

	// Audit events TTL cleaner: prevents unbounded growth of the audit_events table.
	// OPT-IN: only runs when data_retention.audit_clean_interval > 0.
	var stopAuditClean func()
	if cfg.DataRetention.AuditCleanInterval > 0 && cfg.DataRetention.AuditRetention > 0 {
		stopAuditClean = repository.RunAuditCleanerWithHolds(
			auditRepo, holdRepo, log,
			cfg.DataRetention.AuditRetention,
			cfg.DataRetention.AuditCleanInterval,
		)
	}

	router := buildRouter(routerDependencies{
		cfg: cfg, log: log, gdb: gdb, metricsReg: metricsReg,
		tokenValidator: tokenValidator, downloader: downloader,
		sseHub: sseHub, sourceConnectors: sourceConnectors,
		callbackSvc: callbackSvc, auditLedger: auditLedger,
		banRepo: banRepo, notifRepo: notifRepo,
		svcs: svcs, workers: workers,
	})
	httpServer := buildHTTPServer(cfg, router)

	grpcServer := buildGRPCServer(cfg, tokenValidator, log, metricsReg,
		workers.limiter, workers.distributedLimiter, svcs.permissionChecker, workers.degradationState, grpcDependencies{
			Files: svcs.fileSvc, Messages: svcs.msgSvc, Comments: svcs.commentSvc, Notifications: svcs.notifSvc, Blocks: svcs.blockSvc,
			Search: svcs.searchSvc, Preferences: svcs.prefSvc, Admin: svcs.adminSvc, Bans: svcs.banSvc, Accounts: svcs.accountSvc,
			Aggregation: svcs.aggregationSvc, Audit: auditLedger, Operations: svcs.operationSvc, Callbacks: callbackSvc, SyncJobs: svcs.syncJobSvc,
			EventIngest: svcs.eventIngestSvc, EventBindings: buildEventSourceBindings(cfg.EventIngest.Bindings),
		})
	// Standard ops surface: health (reported SERVING) and, when enabled, reflection.
	grpcHealth := grpcsrv.RegisterHealth(grpcServer)
	if cfg.Server.GRPC.ReflectionEnabled {
		grpcsrv.RegisterReflection(grpcServer)
		log.Info("grpc reflection enabled")
	}
	grpcAddr := fmt.Sprintf("%s:%d", cfg.Server.GRPC.Host, cfg.Server.GRPC.Port)
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		stopRelay()
		workers.stopIdemClean()
		if stopGC != nil {
			stopGC()
		}
		if workers.stopRateLimit != nil {
			workers.stopRateLimit()
		}
		if stopAuditClean != nil {
			stopAuditClean()
		}
		if workers.stopNotifClean != nil {
			workers.stopNotifClean()
		}
		if workers.stopOutboxClean != nil {
			workers.stopOutboxClean()
		}
		if stopCallbacks != nil {
			stopCallbacks()
		}
		if svcs.stopOperations != nil {
			svcs.stopOperations()
		}
		if svcs.stopSyncJobs != nil {
			svcs.stopSyncJobs()
		}
		if svcs.stopEventBus != nil {
			svcs.stopEventBus()
		}
		workers.stopDegradation()
		return nil, fmt.Errorf("grpc listen on %s: %w", grpcAddr, err)
	}

	return &App{
		cfg:             cfg,
		log:             log,
		sqlDB:           sqlDB,
		sso:             ssoClient,
		redisClient:     workers.redisClient,
		httpServer:      httpServer,
		grpcServer:      grpcServer,
		grpcHealth:      grpcHealth,
		grpcLis:         grpcLis,
		stopRelay:       stopRelay,
		stopGC:          stopGC,
		stopRateLimit:   workers.stopRateLimit,
		stopIdemClean:   workers.stopIdemClean,
		stopAuditClean:  stopAuditClean,
		stopNotifClean:  workers.stopNotifClean,
		stopOutboxClean: workers.stopOutboxClean,
		stopCallbacks:   stopCallbacks,
		stopOperations:  svcs.stopOperations,
		stopSyncJobs:    svcs.stopSyncJobs,
		stopEventBus:    svcs.stopEventBus,
		stopReplication: svcs.stopReplication,
		stopDegradation: workers.stopDegradation,
		traceShutdown:   traceShutdown,
	}, nil
}
