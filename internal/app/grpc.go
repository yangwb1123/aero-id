package app

import (
	"time"

	"go.uber.org/zap"
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/event"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/pkg/ratelimit"
	"github.com/aero/aero-id/internal/service"
)

type grpcDependencies struct {
	Files         service.FileService
	Messages      service.MessageService
	Comments      service.CommentService
	Notifications service.NotificationService
	Blocks        service.BlockService
	Search        service.SearchService
	Preferences   service.PreferenceService
	Admin         service.AdminService
	Bans          service.BanService
	Accounts      service.AccountService
	Aggregation   service.AggregationService
	Audit         service.AuditLedgerService
	Operations    service.OperationService
	Callbacks     service.CallbackService
	SyncJobs      service.SyncJobService
	EventIngest   service.EventIngestService
	EventBindings event.SourceBindings
}

func buildGRPCServer(cfg *config.Config, validator grpcsrv.TokenValidator, log *zap.Logger, metricsReg *metrics.Registry,
	limiter *ratelimit.Limiter, distributed ratelimit.DistributedLimiter,
	permission middleware.PermissionChecker, degradation *middleware.DegradationState, deps grpcDependencies) *grpclib.Server {
	keepalivePolicy := keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: false}
	if cfg.Server.GRPC.KeepaliveMinPingInterval > 0 {
		keepalivePolicy.MinTime = cfg.Server.GRPC.KeepaliveMinPingInterval
	}
	options := []grpclib.ServerOption{grpclib.KeepaliveEnforcementPolicy(keepalivePolicy)}
	if cfg.Server.GRPC.MaxConcurrentStreams > 0 {
		options = append(options, grpclib.MaxConcurrentStreams(cfg.Server.GRPC.MaxConcurrentStreams))
	}
	if cfg.Server.GRPC.MaxRecvMsgSize > 0 {
		options = append(options, grpclib.MaxRecvMsgSize(cfg.Server.GRPC.MaxRecvMsgSize))
	}
	if cfg.Server.GRPC.MaxSendMsgSize > 0 {
		options = append(options, grpclib.MaxSendMsgSize(cfg.Server.GRPC.MaxSendMsgSize))
	}
	if cfg.Server.GRPC.InitialWindowSize > 0 {
		options = append(options, grpclib.InitialWindowSize(cfg.Server.GRPC.InitialWindowSize))
	}
	if cfg.Server.GRPC.InitialConnWindowSize > 0 {
		options = append(options, grpclib.InitialConnWindowSize(cfg.Server.GRPC.InitialConnWindowSize))
	}
	return grpcsrv.NewServerWithOptions(validator, log, metricsReg, []grpcsrv.ServerOption{
		grpcsrv.WithRateLimit(limiter), grpcsrv.WithDistributedRateLimit(distributed),
		grpcsrv.WithPermissionChecker(permission), grpcsrv.WithDegradation(degradation),
		grpcsrv.WithGRPCServerOptions(options...),
	},
		grpcsrv.NewFileServer(deps.Files), grpcsrv.NewMessageServer(deps.Messages), grpcsrv.NewCommentServer(deps.Comments),
		grpcsrv.NewNotificationServer(deps.Notifications), grpcsrv.NewBlockServer(deps.Blocks), grpcsrv.NewSearchServer(deps.Search),
		grpcsrv.NewPreferenceServer(deps.Preferences), grpcsrv.NewAdminServer(deps.Admin, deps.Bans),
		grpcsrv.NewAccountServer(deps.Accounts), grpcsrv.NewAggregationServer(deps.Aggregation),
		grpcsrv.NewAuditLedgerServer(deps.Audit), grpcsrv.NewOperationServer(deps.Operations),
		grpcsrv.NewCallbackServer(deps.Callbacks), grpcsrv.NewSyncJobServer(deps.SyncJobs),
		grpcsrv.NewEventIngestServer(deps.EventIngest, grpcsrv.WithEventSourceBindings(deps.EventBindings)))
}
