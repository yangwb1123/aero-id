package app

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/region"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
	"github.com/aero/aero-id/internal/storage"
)

// serviceDependencies carries the repositories and support objects the
// service-construction block needs; extracted from new.go so the composition
// root stays under the file-size gate (C-7). Mirrors the
// outboxDependencies/grpcDependencies struct pattern.
type serviceDependencies struct {
	workerContext      context.Context
	cfg                *config.Config
	log                *zap.Logger
	gdb                *gorm.DB
	store              storage.FileStorage
	ssoClient          *sso.Client
	sourceConnectors   []connector.Connector
	controlMetrics     service.ControlPlaneMetrics
	metricsReg         *metrics.Registry
	billingRuntime     billingRuntime
	summaryApplication repository.SummaryApplication
	auditLedger        service.AuditLedgerService
	auditLedgerRepo    repository.FullAuditLedgerRepository

	fileRepo         repository.FileRepository
	fileShareRepo    repository.FileShareRepository
	msgRepo          repository.MessageRepository
	commentRepo      repository.CommentRepository
	notifRepo        repository.NotificationRepository
	blockRepo        repository.BlockRepository
	banRepo          repository.BanRepository
	prefRepo         repository.PreferenceRepository
	storageRefRepo   repository.StorageRefRepository
	adminRepo        repository.AdminRepository
	accountRepo      repository.AccountRepository
	snapshotRepo     repository.SnapshotRepository
	eventReceiptRepo repository.EventReceiptRepository
	operationRepo    repository.OperationRepository
	syncJobRepo      repository.SyncJobRepository
}

// serviceBundle carries every service and worker-stop the rest of the
// composition root needs, so builder signatures stay small.
type serviceBundle struct {
	fileSvc             service.FileService
	msgSvc              service.MessageService
	commentSvc          service.CommentService
	notifSvc            service.NotificationService
	blockSvc            service.BlockService
	prefSvc             service.PreferenceService
	searchSvc           service.SearchService
	adminSvc            service.AdminService
	accountSvc          service.AccountService
	regionFencingSvc    service.RegionFencingService
	cutoverSvc          service.LegacyCutoverService
	legacyMigrationSvc  service.LegacyMigrationService
	aggregationSvc      service.AggregationService
	eventIngestSvc      service.EventIngestService
	operationSvc        service.OperationService
	replaySvc           service.ReplayService
	projectionReplaySvc service.ReplayProjectionService
	syncJobSvc          service.SyncJobService
	banSvc              service.BanService
	replicationSvc      service.ProjectionReplicationService
	permissionChecker   middleware.PermissionChecker

	stopEventBus    func()
	stopReplication func()
	drainOperations func()
	stopOperations  func()
	stopSyncJobs    func()
}

// buildServices constructs every domain service wired into both transports,
// selects the permission checker, and runs the fail-closed audit-wiring
// assertions (extracted from new.go so the composition root stays under the
// file-size gate). The optional event-bus consumer and projection-replication
// workers are started here in their current relative order; their stop
// functions flow back through the bundle so the failure path in new.go is
// unchanged.
func buildServices(deps serviceDependencies) (serviceBundle, error) {
	fileSvc := service.NewFileService(deps.fileRepo, deps.store, deps.cfg.Storage.Type,
		service.WithMaxUploadBytes(deps.cfg.Storage.MaxUploadBytes),
		service.WithAllowedMimeTypes(deps.cfg.Storage.AllowedMimeTypes),
		service.WithFileShareRepo(deps.fileShareRepo),
		service.WithFileAuditLogger(deps.auditLedger),
		service.WithStorageRefs(deps.storageRefRepo),
	)
	msgSvc := service.NewMessageService(deps.msgRepo, deps.fileRepo, deps.ssoClient,
		service.WithBlockChecker(deps.blockRepo),
		service.WithMessageAuditLogger(deps.auditLedger),
	)
	commentSvc := service.NewCommentService(deps.commentRepo, deps.fileRepo, deps.msgRepo, deps.ssoClient,
		service.WithCommentAuditLogger(deps.auditLedger),
	)
	notifSvc := service.NewNotificationService(deps.notifRepo, deps.ssoClient)
	blockSvc := service.NewBlockService(deps.blockRepo)
	prefSvc := service.NewPreferenceService(deps.prefRepo, service.WithPreferenceAuditLogger(deps.auditLedger))
	searchSvc := service.NewSearchService(deps.msgRepo, deps.commentRepo, deps.fileRepo)
	adminSvc := service.NewAdminService(deps.adminRepo,
		service.WithAdminAuditLogger(deps.auditLedger),
		service.WithAdminAuditLedger(deps.auditLedger),
	)
	var permissionChecker middleware.PermissionChecker = adminSvc
	if deps.cfg.SSO.AuthZRequired || deps.cfg.SSO.AuthZLegacyFallback {
		permissionChecker = service.NewDelegatingPermissionChecker(deps.ssoClient, adminSvc, deps.cfg.SSO.AuthZLegacyFallback)
	}
	accountSvc := service.NewAccountService(deps.accountRepo,
		service.WithAccountPermissionChecker(permissionChecker),
		service.WithAccountAudit(deps.auditLedger),
		service.WithAccountQuota(deps.billingRuntime.quota),
		service.WithAccountIssuer(deps.cfg.Account.Issuer),
		service.WithAccountRegion(deps.cfg.Account.Region),
		service.WithAccountHomeRegionResolver(region.NewResolver(deps.cfg.Account.HomeRegionVersion, deps.cfg.Account.Regions)),
		service.WithAccountRegionDirectory(repository.NewRegionDirectoryRepository(deps.gdb, deps.auditLedgerRepo), deps.cfg.Account.HomeRegionVersion),
	)
	regionFencingSvc := service.NewRegionFencingService(repository.NewRegionDirectoryRepository(deps.gdb, deps.auditLedgerRepo),
		service.WithRegionFencingAuditLogger(deps.auditLedger),
		service.WithRegionFencingAllowedRegions(deps.cfg.Account.Regions))
	cutoverSvc := service.NewLegacyCutoverService(repository.NewLegacyCutoverRepository(deps.gdb, deps.auditLedgerRepo),
		repository.NewLegacyCleanupRepository(deps.gdb), deps.auditLedger)
	legacyMigrationSvc := service.NewLegacyMigrationServiceWithAuditAndAccounts(
		repository.NewLegacyMigrationRepository(deps.gdb), deps.auditLedger, deps.accountRepo,
		deps.cfg.Account.Issuer, deps.sourceConnectors...)
	aggregationSvc := service.NewAggregationServiceWithAuthoritativeAccounts(accountSvc, deps.accountRepo,
		deps.snapshotRepo, deps.summaryApplication, deps.sourceConnectors...)
	eventIngestSvc := service.NewEventIngestService(deps.eventReceiptRepo,
		service.WithEventAllowedRegions(deps.cfg.EventIngest.AllowedRegions),
		service.WithEventIngestAudit(deps.auditLedger),
		service.WithEventIngestMetrics(deps.controlMetrics))

	// P3-04: optional NATS JetStream consumer feeding the same ingest service.
	stopEventBus, err := buildEventBusConsumer(deps.cfg, deps.log, eventIngestSvc)
	if err != nil {
		return serviceBundle{}, err
	}

	// P6-03: optional cross-region projection replication worker.
	replicationSvc, stopReplication, err := buildProjectionReplication(deps.cfg, deps.gdb, deps.log, deps.metricsReg, deps.auditLedger)
	if err != nil {
		return serviceBundle{}, err
	}
	operationRunner := service.NewConnectorOperationRunner(deps.accountRepo, deps.summaryApplication, deps.sourceConnectors...)
	operationSvc := service.NewOperationService(deps.operationRepo,
		service.WithOperationAudit(deps.auditLedger),
		service.WithOperationRegion(deps.cfg.Account.Region),
		service.WithOperationRunner(operationRunner),
		service.WithOperationAccountRepository(deps.accountRepo),
		service.WithOperationPermission(permissionChecker),
		service.WithOperationMetrics(deps.controlMetrics))
	var stopOperations func()
	var drainOperations func()
	if controller, ok := operationSvc.(service.OperationDrainController); ok {
		drainOperations = func() { controller.SetDraining(true) }
	}
	if worker, ok := operationSvc.(service.OperationWorkerService); ok {
		stopOperations = worker.Start(deps.workerContext, time.Second)
	}
	replaySvc := service.NewReplayService(deps.auditLedger, operationSvc,
		service.WithReplayAccountRepository(deps.accountRepo), service.WithReplaySnapshotRepository(deps.snapshotRepo),
		service.WithReplayPermission(permissionChecker))
	projectionReplaySvc := service.NewProjectionReplayService(repository.NewEventStoreRepository(deps.gdb),
		deps.snapshotRepo, deps.accountRepo, permissionChecker, service.WithProjectionReplayAudit(deps.auditLedger))
	syncJobSvc := service.NewSyncJobServiceWithOptions(deps.syncJobRepo, accountSvc, deps.accountRepo, deps.snapshotRepo,
		deps.summaryApplication, deps.store, deps.sourceConnectors,
		service.WithSyncJobPermission(permissionChecker),
		service.WithSyncJobAudit(deps.auditLedger),
		service.WithSyncJobAuditLedger(repository.NewAuditLedgerRepository(deps.gdb,
			repository.AuditLedgerConfig{SigningKey: deps.cfg.AuditLedger.SigningKey})),
		service.WithSyncExportConfig(deps.cfg.Export.EncryptionKey, deps.cfg.Export.MaxBytes, deps.cfg.Export.ArtifactTTL),
		service.WithSyncJobRegion(deps.cfg.Account.Region),
		service.WithSyncJobMetrics(deps.controlMetrics))
	stopSyncJobs := syncJobSvc.Start(deps.workerContext, time.Second)
	banSvc := service.NewBanService(deps.banRepo, adminSvc,
		service.WithBanAuditLogger(deps.auditLedger),
	)

	// FM-8 wiring assertion: every service that owns an `if s.audit != nil`
	// skip guard must be constructed with a non-nil audit logger — the legacy
	// six, the migrated preference/region-fencing/projection-replay services,
	// and (when enabled) the projection replication worker.
	assertAuditWiring(
		auditWiredService{name: "file", svc: fileSvc},
		auditWiredService{name: "message", svc: msgSvc},
		auditWiredService{name: "comment", svc: commentSvc},
		auditWiredService{name: "block", svc: blockSvc},
		auditWiredService{name: "admin", svc: adminSvc},
		auditWiredService{name: "ban", svc: banSvc},
		auditWiredService{name: "account", svc: accountSvc},
		auditWiredService{name: "legacy_migration", svc: legacyMigrationSvc},
		auditWiredService{name: "preference", svc: prefSvc},
		auditWiredService{name: "region_fencing", svc: regionFencingSvc},
		auditWiredService{name: "projection_replay", svc: projectionReplaySvc},
		auditWiredService{name: "event_ingest", svc: eventIngestSvc},
		auditWiredService{name: "operation", svc: operationSvc},
		auditWiredService{name: "replay", svc: replaySvc},
		auditWiredService{name: "sync_job", svc: syncJobSvc},
	)
	// The replication worker is nil when replication is disabled; assert only when wired.
	if replicationSvc != nil {
		assertAuditWiring(auditWiredService{name: "projection_replication", svc: replicationSvc})
	}

	return serviceBundle{
		fileSvc: fileSvc, msgSvc: msgSvc, commentSvc: commentSvc, notifSvc: notifSvc,
		blockSvc: blockSvc, prefSvc: prefSvc, searchSvc: searchSvc, adminSvc: adminSvc,
		accountSvc: accountSvc, regionFencingSvc: regionFencingSvc, cutoverSvc: cutoverSvc,
		legacyMigrationSvc: legacyMigrationSvc,
		aggregationSvc:     aggregationSvc, eventIngestSvc: eventIngestSvc,
		operationSvc: operationSvc, replaySvc: replaySvc, projectionReplaySvc: projectionReplaySvc,
		syncJobSvc: syncJobSvc, banSvc: banSvc, replicationSvc: replicationSvc,
		permissionChecker: permissionChecker,
		stopEventBus:      stopEventBus, stopReplication: stopReplication,
		drainOperations: drainOperations,
		stopOperations:  stopOperations, stopSyncJobs: stopSyncJobs,
	}, nil
}
