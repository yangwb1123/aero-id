package app

import (
	"net/http"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/hub"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

// routerDependencies carries the pieces the HTTP router assembly needs;
// extracted from new.go so the composition root stays under the file-size
// gate (C-7). The service and worker bundles keep the signature small,
// mirroring the outboxDependencies/grpcDependencies struct pattern.
type routerDependencies struct {
	cfg              *config.Config
	log              *zap.Logger
	gdb              *gorm.DB
	metricsReg       *metrics.Registry
	tokenValidator   grpcsrv.TokenValidator
	downloader       httpapi.SignedDownloader
	sseHub           *hub.Hub
	sourceConnectors []connector.Connector
	callbackSvc      service.CallbackService
	auditLedger      service.AuditLedgerService
	banRepo          repository.BanRepository
	notifRepo        repository.NotificationRepository
	svcs             serviceBundle
	workers          workerBundle
}

// buildRouter assembles the HTTP router: the audit-ledger handler (holds,
// anchors, sync jobs) followed by every httpapi handler registered on the
// /v1 surface (extracted from new.go so the composition root stays under the
// file-size gate). Pure construction — no worker starts, so no stop
// functions flow from here.
func buildRouter(deps routerDependencies) http.Handler {
	auditHandler := buildAuditLedgerHandler(deps.auditLedger, deps.workers.holdSvc, deps.workers.anchorSvc,
		deps.svcs.syncJobSvc, deps.svcs.permissionChecker)
	router := httpapi.NewRouter(
		deps.log,
		httpapi.RouterConfig{
			Metrics:                deps.metricsReg,
			Idempotency:            deps.workers.idemStore,
			IdempotencyTTL:         deps.cfg.Server.HTTP.IdempotencyTTL,
			RateLimiter:            deps.workers.limiter,
			DistributedRateLimiter: deps.workers.distributedLimiter,
			RequestTimeout:         deps.cfg.Server.HTTP.RequestTimeout,
			SampleRatio:            deps.cfg.Log.SampleRatio,
			BanChecker:             deps.banRepo, // repository.BanRepository satisfies middleware.BanChecker
			Degradation:            deps.workers.degradationState,
			LegacySunset:           deps.cfg.Server.HTTP.LegacySunset,
			RegionEpoch:            middleware.NewRegionEpochHeader(deps.svcs.regionFencingSvc, deps.cfg.Account.Region),
			CutoverChecker:         deps.svcs.cutoverSvc,
		},
		deps.tokenValidator,
		deps.workers.health,
		httpapi.NewSourceHealthHandler(deps.sourceConnectors),
		httpapi.NewFileHandler(deps.svcs.fileSvc, deps.downloader, presignTTL),
		httpapi.NewMessageHandler(deps.svcs.msgSvc),
		httpapi.NewCommentHandler(deps.svcs.commentSvc),
		httpapi.NewNotificationHandler(deps.svcs.notifSvc),
		httpapi.NewBlockHandler(deps.svcs.blockSvc),
		httpapi.NewEventsHandler(deps.sseHub, deps.notifRepo),
		httpapi.NewPreferenceHandler(deps.svcs.prefSvc),
		httpapi.NewAdminHandler(deps.svcs.adminSvc,
			[]service.BanService{deps.svcs.banSvc},
			[]httpapi.HubDisconnector{deps.sseHub},
			deps.svcs.permissionChecker),
		httpapi.NewSearchHandler(deps.svcs.searchSvc),
		httpapi.NewAccountHandlerWithExtensions(deps.svcs.accountSvc, deps.svcs.regionFencingSvc,
			service.NewLegacyMigrationService(repository.NewLegacyMigrationRepository(deps.gdb), deps.sourceConnectors...),
			deps.workers.cutoverFacadeSvc,
			deps.svcs.permissionChecker),
		httpapi.NewAggregationHandler(deps.svcs.aggregationSvc, deps.svcs.permissionChecker),
		httpapi.NewEventIngestHandler(deps.svcs.eventIngestSvc, deps.cfg.EventIngest.SigningKey,
			deps.cfg.EventIngest.MaxBodyBytes, deps.cfg.EventIngest.ClockSkew,
			httpapi.WithEventSourceKeys(deps.cfg.EventIngest.SourceKeys),
			httpapi.WithEventTimestampRequired(deps.cfg.EventIngest.RequireTimestamp),
			httpapi.WithEventSourceBindings(buildEventSourceBindings(deps.cfg.EventIngest.Bindings)),
			httpapi.WithEventPermissionChecker(deps.svcs.permissionChecker)),
		auditHandler,
		httpapi.NewCallbackHandler(deps.callbackSvc, deps.svcs.permissionChecker),
		httpapi.NewOperationHandler(deps.svcs.operationSvc),
		httpapi.NewSyncJobHandler(deps.svcs.syncJobSvc, deps.svcs.permissionChecker),
		httpapi.NewReplayHandlerWithProjection(deps.svcs.replaySvc,
			deps.svcs.projectionReplaySvc, deps.svcs.permissionChecker),
	)
	return router
}
