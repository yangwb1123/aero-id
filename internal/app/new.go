package app

import (
	"context"
	"fmt"
	"net"
	"time"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/config"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/hub"
	"github.com/aero/aero-id/internal/idempotency"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/outbox"
	"github.com/aero/aero-id/internal/pkg/db"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/pkg/migrate"
	"github.com/aero/aero-id/internal/pkg/ratelimit"
	redislib "github.com/aero/aero-id/internal/pkg/redis"
	"github.com/aero/aero-id/internal/pkg/tracing"
	"github.com/aero/aero-id/internal/region"
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

	fileSvc := service.NewFileService(fileRepo, store, cfg.Storage.Type,
		service.WithMaxUploadBytes(cfg.Storage.MaxUploadBytes),
		service.WithAllowedMimeTypes(cfg.Storage.AllowedMimeTypes),
		service.WithFileShareRepo(fileShareRepo),
		service.WithFileAuditLogger(auditLedger),
		service.WithStorageRefs(storageRefRepo),
	)
	msgSvc := service.NewMessageService(msgRepo, fileRepo, ssoClient,
		service.WithBlockChecker(blockRepo),
		service.WithMessageAuditLogger(auditLedger),
	)
	commentSvc := service.NewCommentService(commentRepo, fileRepo, msgRepo, ssoClient,
		service.WithCommentAuditLogger(auditLedger),
	)
	notifSvc := service.NewNotificationService(notifRepo, ssoClient)
	blockSvc := service.NewBlockService(blockRepo)
	prefSvc := service.NewPreferenceService(prefRepo)
	searchSvc := service.NewSearchService(msgRepo, commentRepo, fileRepo)
	adminSvc := service.NewAdminService(adminRepo,
		service.WithAdminAuditLogger(auditLedger),
		service.WithAdminAuditLedger(auditLedger),
	)
	var permissionChecker middleware.PermissionChecker = adminSvc
	if cfg.SSO.AuthZRequired || cfg.SSO.AuthZLegacyFallback {
		permissionChecker = service.NewDelegatingPermissionChecker(ssoClient, adminSvc, cfg.SSO.AuthZLegacyFallback)
	}
	accountSvc := service.NewAccountService(accountRepo,
		service.WithAccountPermissionChecker(permissionChecker),
		service.WithAccountAudit(auditLedger),
		service.WithAccountQuota(billingRuntime.quota),
		service.WithAccountIssuer(cfg.Account.Issuer),
		service.WithAccountRegion(cfg.Account.Region),
		service.WithAccountHomeRegionResolver(region.NewResolver(cfg.Account.HomeRegionVersion, cfg.Account.Regions)),
		service.WithAccountRegionDirectory(repository.NewRegionDirectoryRepository(gdb), cfg.Account.HomeRegionVersion),
	)
	regionFencingSvc := service.NewRegionFencingService(repository.NewRegionDirectoryRepository(gdb))
	cutoverSvc := service.NewLegacyCutoverService(repository.NewLegacyCutoverRepository(gdb, auditLedgerRepo),
		repository.NewLegacyCleanupRepository(gdb), auditLedger)
	aggregationSvc := service.NewAggregationService(accountSvc, snapshotRepo, sourceConnectors...)
	eventIngestSvc := service.NewEventIngestService(eventReceiptRepo,
		service.WithEventAllowedRegions(cfg.EventIngest.AllowedRegions),
		service.WithEventIngestAudit(auditLedger),
		service.WithEventIngestMetrics(controlMetrics))

	// P3-04: optional NATS JetStream consumer feeding the same ingest service.
	stopEventBus, err := buildEventBusConsumer(cfg, log, eventIngestSvc)
	if err != nil {
		return nil, err
	}

	// P6-03: optional cross-region projection replication worker.
	stopReplication, err := buildProjectionReplication(cfg, gdb, log, metricsReg)
	if err != nil {
		return nil, err
	}
	operationRunner := service.NewConnectorOperationRunner(accountRepo, snapshotRepo, sourceConnectors...)
	operationSvc := service.NewOperationService(operationRepo, service.WithOperationAudit(auditLedger), service.WithOperationRegion(cfg.Account.Region),
		service.WithOperationRunner(operationRunner), service.WithOperationAccountRepository(accountRepo), service.WithOperationPermission(permissionChecker),
		service.WithOperationMetrics(controlMetrics))
	var stopOperations func()
	if worker, ok := operationSvc.(service.OperationWorkerService); ok {
		stopOperations = worker.Start(context.Background(), time.Second)
	}
	replaySvc := service.NewReplayService(auditLedger, operationSvc,
		service.WithReplayAccountRepository(accountRepo), service.WithReplaySnapshotRepository(snapshotRepo),
		service.WithReplayPermission(permissionChecker))
	syncJobSvc := service.NewSyncJobServiceWithOptions(syncJobRepo, accountSvc, accountRepo, snapshotRepo, store, sourceConnectors,
		service.WithSyncJobPermission(permissionChecker), service.WithSyncJobAudit(auditLedger),
		service.WithSyncJobAuditLedger(repository.NewAuditLedgerRepository(gdb, repository.AuditLedgerConfig{SigningKey: cfg.AuditLedger.SigningKey})),
		service.WithSyncExportConfig(cfg.Export.EncryptionKey, cfg.Export.MaxBytes, cfg.Export.ArtifactTTL),
		service.WithSyncJobRegion(cfg.Account.Region), service.WithSyncJobMetrics(controlMetrics))
	stopSyncJobs := syncJobSvc.Start(context.Background(), time.Second)
	banSvc := service.NewBanService(banRepo, adminSvc,
		service.WithBanAuditLogger(auditLedger),
	)

	// FM-8 wiring assertion: the six legacy services own the `if s.audit != nil`
	// skip guards at the 10 fail-closed Log sites; assert all six are wired.
	assertAuditWiring(
		auditWiredService{name: "file", svc: fileSvc},
		auditWiredService{name: "message", svc: msgSvc},
		auditWiredService{name: "comment", svc: commentSvc},
		auditWiredService{name: "block", svc: blockSvc},
		auditWiredService{name: "admin", svc: adminSvc},
		auditWiredService{name: "ban", svc: banSvc},
		auditWiredService{name: "account", svc: accountSvc},
	)

	// Redis client for shared state (idempotency store, SSO cache, leader election,
	// rate limiter back-end, etc.). OPT-IN: when redis.endpoint is empty every Redis
	// feature falls back to its GORM / in-memory alternative.
	redisClient, err := redislib.NewClient(&cfg.Redis)
	if err != nil {
		return nil, fmt.Errorf("redis client: %w", err)
	}
	if redisClient != nil {
		log.Info("redis client enabled", zap.String("endpoint", cfg.Redis.Endpoint))
	}

	// Edge rate limiter, shared by HTTP middleware and the gRPC interceptor. OPT-IN
	// and disabled by default; Redis provides the cross-instance decision while the
	// local bucket remains a bounded fallback. When on, a background cleaner evicts
	// idle local buckets to bound memory.
	var limiter *ratelimit.Limiter
	var stopRateLimit func()
	if cfg.RateLimit.Enabled {
		idleTTL := cfg.RateLimit.IdleTTL
		if idleTTL <= 0 {
			idleTTL = defaultRateLimitIdleTTL
		}
		cleanupEvery := cfg.RateLimit.CleanupEvery
		if cleanupEvery <= 0 {
			cleanupEvery = defaultRateLimitCleanupEvery
		}
		limiter = ratelimit.New(cfg.RateLimit.RPS, cfg.RateLimit.Burst, idleTTL)
		stopRateLimit = limiter.RunCleaner(cleanupEvery)
		log.Info("rate limiting enabled",
			zap.Float64("rps", cfg.RateLimit.RPS),
			zap.Int("burst", cfg.RateLimit.Burst),
			zap.Duration("idle_ttl", idleTTL))
	}
	distributedRateLimiter := buildDistributedRateLimiter(cfg, redisClient, log)

	// HTTP transport. Readiness probes the DB (critical) and SSO (criticality is
	// configurable so an SSO-less dev stack stays "ready" rather than 503-ing).
	// Idempotency store: use Redis when available, fall back to GORM.
	var idemStore idempotency.Store
	var stopIdemClean func()
	if redisClient != nil {
		idemStore = idempotency.NewRedisStore(redisClient)
		stopIdemClean = func() {} // Redis keys auto-expire; no background cleaner needed
		log.Info("idempotency store: redis")
	} else {
		gormStore := idempotency.NewGormStore(gdb)
		idemStore = gormStore
		stopIdemClean = gormStore.RunCleaner(time.Hour)
		log.Info("idempotency store: gorm (no redis configured)")
	}

	// Audit events TTL cleaner: prevents unbounded growth of the audit_events table.
	// OPT-IN: only runs when data_retention.audit_clean_interval > 0.
	var stopAuditClean func()
	if cfg.DataRetention.AuditCleanInterval > 0 && cfg.DataRetention.AuditRetention > 0 {
		stopAuditClean = repository.RunAuditCleaner(
			auditRepo, log,
			cfg.DataRetention.AuditRetention,
			cfg.DataRetention.AuditCleanInterval,
		)
	}

	// Notifications TTL cleaner: purges read notifications past retention and
	// unread notifications past unread retention. OPT-IN.
	var stopNotifClean func()
	if cfg.DataRetention.NotificationCleanInterval > 0 && cfg.DataRetention.NotificationRetention > 0 {
		stopNotifClean = repository.RunNotificationCleaner(
			notifRepo, log,
			cfg.DataRetention.NotificationRetention,
			cfg.DataRetention.NotificationUnreadRetention,
			cfg.DataRetention.NotificationCleanInterval,
		)
	}

	// Outbox events TTL cleaner: purges dead letters and published events past
	// their respective retentions. OPT-IN.
	var stopOutboxClean func()
	if cfg.DataRetention.OutboxCleanInterval > 0 && cfg.DataRetention.OutboxDeadLetterRetention > 0 {
		stopOutboxClean = buildOutboxCleaner(cfg, log, outboxRepo)
	}

	// Degradation state manager — tracks upstream dependency health for the
	// X-Aero-Degraded response header and handler adaptation.
	// Probes DB and SSO on a 30s interval, marking degraded after 3 consecutive
	// failures and restored after 2 consecutive successes.
	degradationState := middleware.NewDegradationState()
	degradationState.SetProber(middleware.DepDB, sqlDB.PingContext)
	if ssoClient != nil {
		degradationState.SetProber(middleware.DepSSO, ssoClient.HealthCheck)
	}
	stopDegradation := degradationState.StartProbing(context.Background(), 30*time.Second, 3, 2)

	health := buildHealthHandler(cfg, sqlDB, ssoClient, billingRuntime.probe)
	holdSvc, anchorSvc, cutoverFacadeSvc := buildAuditComplianceServices(gdb, auditLedger, auditLedgerRepo, store)
	// REQ-4/5 fail-closed wiring: hold/cutover services must not run with a nil audit service.
	assertComplianceAuditWiring(holdSvc, cutoverSvc, cutoverFacadeSvc)
	auditHandler := buildAuditLedgerHandler(auditLedger, holdSvc, anchorSvc, syncJobSvc, permissionChecker)
	router := httpapi.NewRouter(
		log,
		httpapi.RouterConfig{
			Metrics:                metricsReg,
			Idempotency:            idemStore,
			IdempotencyTTL:         cfg.Server.HTTP.IdempotencyTTL,
			RateLimiter:            limiter,
			DistributedRateLimiter: distributedRateLimiter,
			RequestTimeout:         cfg.Server.HTTP.RequestTimeout,
			SampleRatio:            cfg.Log.SampleRatio,
			BanChecker:             banRepo, // repository.BanRepository satisfies middleware.BanChecker
			Degradation:            degradationState,
			LegacySunset:           cfg.Server.HTTP.LegacySunset,
			RegionEpoch:            middleware.NewRegionEpochHeader(regionFencingSvc, cfg.Account.Region),
			CutoverChecker:         cutoverSvc,
		},
		tokenValidator,
		health,
		httpapi.NewSourceHealthHandler(sourceConnectors),
		httpapi.NewFileHandler(fileSvc, downloader, presignTTL),
		httpapi.NewMessageHandler(msgSvc),
		httpapi.NewCommentHandler(commentSvc),
		httpapi.NewNotificationHandler(notifSvc),
		httpapi.NewBlockHandler(blockSvc),
		httpapi.NewEventsHandler(sseHub, notifRepo),
		httpapi.NewPreferenceHandler(prefSvc),
		httpapi.NewAdminHandler(adminSvc, []service.BanService{banSvc}, []httpapi.HubDisconnector{sseHub}, permissionChecker),
		httpapi.NewSearchHandler(searchSvc),
		httpapi.NewAccountHandlerWithExtensions(accountSvc, regionFencingSvc,
			service.NewLegacyMigrationService(repository.NewLegacyMigrationRepository(gdb), sourceConnectors...),
			cutoverFacadeSvc,
			permissionChecker),
		httpapi.NewAggregationHandler(aggregationSvc, permissionChecker),
		httpapi.NewEventIngestHandler(eventIngestSvc, cfg.EventIngest.SigningKey, cfg.EventIngest.MaxBodyBytes, cfg.EventIngest.ClockSkew,
			httpapi.WithEventSourceKeys(cfg.EventIngest.SourceKeys), httpapi.WithEventTimestampRequired(cfg.EventIngest.RequireTimestamp),
			httpapi.WithEventSourceBindings(buildEventSourceBindings(cfg.EventIngest.Bindings)), httpapi.WithEventPermissionChecker(permissionChecker)),
		auditHandler,
		httpapi.NewCallbackHandler(callbackSvc, permissionChecker),
		httpapi.NewOperationHandler(operationSvc),
		httpapi.NewSyncJobHandler(syncJobSvc, permissionChecker),
		httpapi.NewReplayHandlerWithProjection(replaySvc,
			service.NewProjectionReplayService(repository.NewEventStoreRepository(gdb), snapshotRepo), permissionChecker),
	)
	httpServer := buildHTTPServer(cfg, router)

	grpcServer := buildGRPCServer(cfg, tokenValidator, log, metricsReg, limiter, distributedRateLimiter, permissionChecker, degradationState, grpcDependencies{
		Files: fileSvc, Messages: msgSvc, Comments: commentSvc, Notifications: notifSvc, Blocks: blockSvc,
		Search: searchSvc, Preferences: prefSvc, Admin: adminSvc, Bans: banSvc, Accounts: accountSvc,
		Aggregation: aggregationSvc, Audit: auditLedger, Operations: operationSvc, Callbacks: callbackSvc, SyncJobs: syncJobSvc,
		EventIngest: eventIngestSvc, EventBindings: buildEventSourceBindings(cfg.EventIngest.Bindings),
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
		stopIdemClean()
		if stopGC != nil {
			stopGC()
		}
		if stopRateLimit != nil {
			stopRateLimit()
		}
		if stopAuditClean != nil {
			stopAuditClean()
		}
		if stopNotifClean != nil {
			stopNotifClean()
		}
		if stopOutboxClean != nil {
			stopOutboxClean()
		}
		if stopCallbacks != nil {
			stopCallbacks()
		}
		if stopOperations != nil {
			stopOperations()
		}
		if stopSyncJobs != nil {
			stopSyncJobs()
		}
		if stopEventBus != nil {
			stopEventBus()
		}
		stopDegradation()
		return nil, fmt.Errorf("grpc listen on %s: %w", grpcAddr, err)
	}

	return &App{
		cfg:             cfg,
		log:             log,
		sqlDB:           sqlDB,
		sso:             ssoClient,
		redisClient:     redisClient,
		httpServer:      httpServer,
		grpcServer:      grpcServer,
		grpcHealth:      grpcHealth,
		grpcLis:         grpcLis,
		stopRelay:       stopRelay,
		stopGC:          stopGC,
		stopRateLimit:   stopRateLimit,
		stopIdemClean:   stopIdemClean,
		stopAuditClean:  stopAuditClean,
		stopNotifClean:  stopNotifClean,
		stopOutboxClean: stopOutboxClean,
		stopCallbacks:   stopCallbacks,
		stopOperations:  stopOperations,
		stopSyncJobs:    stopSyncJobs,
		stopEventBus:    stopEventBus,
		stopReplication: stopReplication,
		stopDegradation: stopDegradation,
		traceShutdown:   traceShutdown,
	}, nil
}
