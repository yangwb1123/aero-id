package http_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

// The stubs below exist only to satisfy constructor signatures so the
// reconcile gate can assemble the fully-wired router. Registration never
// dereferences a service (verified per constructor), so every stub returns
// zero values; the permission checker and the conditional dependencies must
// be non-nil so the optional route branches attach exactly as in production.

// stubPermissionChecker always grants. It is non-nil so RequirePermission /
// RequirePermissionIfConfigured attach as in production; the middleware
// dereferences the checker only at request time
// (internal/middleware/permission.go:20-46).
type stubPermissionChecker struct{}

func (stubPermissionChecker) HasPermission(context.Context, string, string) (bool, error) {
	return true, nil
}

type stubBanService struct{}

func (stubBanService) Ban(context.Context, string, string, time.Duration) error   { return nil }
func (stubBanService) Unban(context.Context, string) error                        { return nil }
func (stubBanService) ListBanned(context.Context, domain.Page) ([]*domain.UserBan, string, error) {
	return nil, "", nil
}

type stubRegionFencing struct{}

func (stubRegionFencing) Directory(context.Context, string, domain.ID) (*repository.RegionDirectory, error) {
	return nil, nil
}
func (stubRegionFencing) Promote(context.Context, string, domain.ID, string, int64) (int64, error) {
	return 0, nil
}
func (stubRegionFencing) RegionEpoch(context.Context, string) (int64, error) { return 0, nil }

type stubLegacyMigration struct{}

func (stubLegacyMigration) Compare(context.Context, service.LegacyCompareInput) (*service.LegacyCompareResult, error) {
	return nil, nil
}

type stubLegacyCutover struct{}

func (stubLegacyCutover) Get(context.Context, string) (domain.LegacyCutoverStatus, error) {
	return domain.LegacyCutoverStatusLegacy, nil
}
func (stubLegacyCutover) Set(context.Context, string, domain.LegacyCutoverStatus, string) (*repository.LegacyCutover, error) {
	return nil, nil
}
func (stubLegacyCutover) CleanupReadiness(context.Context, string) ([]repository.CleanupReadinessItem, bool, error) {
	return nil, false, nil
}
func (stubLegacyCutover) IsCutOver(context.Context, string) (bool, error) { return false, nil }

type stubAuditHold struct{}

func (stubAuditHold) List(context.Context, string) ([]*repository.AuditHold, error) { return nil, nil }
func (stubAuditHold) Put(context.Context, string, domain.AuditHoldScope, string, string, *time.Time) (*repository.AuditHold, error) {
	return nil, nil
}
func (stubAuditHold) Remove(context.Context, string, domain.AuditHoldScope, string) error { return nil }
func (stubAuditHold) ActiveScopeIDs(context.Context, domain.AuditHoldScope) ([]string, error) {
	return nil, nil
}

type stubAuditAnchor struct{}

func (stubAuditAnchor) AnchorOnce(context.Context) (*service.AnchorRunResult, error) { return nil, nil }
func (stubAuditAnchor) AnchorManifest(context.Context, string, string) (map[string]any, error) {
	return nil, nil
}

type stubSyncJob struct{}

func (stubSyncJob) Create(context.Context, service.SyncJobInput) (*domain.SyncJob, error) {
	return nil, nil
}
func (stubSyncJob) Get(context.Context, domain.ID) (*domain.SyncJob, error)            { return nil, nil }
func (stubSyncJob) List(context.Context, domain.Page) ([]*domain.SyncJob, string, error) {
	return nil, "", nil
}
func (stubSyncJob) Run(context.Context, domain.ID, string) (*domain.SyncJob, error) { return nil, nil }
func (stubSyncJob) Reconcile(context.Context, domain.ID, string) (*domain.SyncJob, error) {
	return nil, nil
}
func (stubSyncJob) DownloadExport(context.Context, domain.ID, bool) ([]byte, error) { return nil, nil }
func (stubSyncJob) Start(context.Context, time.Duration) func()                      { return func() {} }

type stubReplayProjection struct{}

func (stubReplayProjection) ReplayProjection(context.Context, service.ReplayProjectionInput) (*service.ProjectionReplayResult, error) {
	return nil, nil
}

type stubSignedDownloader struct{}

func (stubSignedDownloader) VerifyPresignedURL(string, time.Time) error { return nil }
func (stubSignedDownloader) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, nil
}

// newFullyWiredRouter mirrors internal/app/handlers.go:46-98 (buildRouter):
// all 18 registrars with every conditional dependency non-nil (region
// fencing, migration, cutover facade, bans, SSE hub, audit export/anchor/
// holds jobs, projection replay, signed downloader, permission checker) so
// the deployment-maximum route surface is exercised. The permission checker
// is a non-nil fake so RequirePermission / RequirePermissionIfConfigured
// attach as in production.
//
// UPDATE THIS LIST WHEN internal/app/handlers.go CHANGES — a registrar added
// or removed in production must be mirrored here.
func newFullyWiredRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	perm := stubPermissionChecker{}
	return httpapi.NewRouter(
		zap.NewNop(),
		httpapi.RouterConfig{},
		okValidator{},
		httpapi.NewHealthHandler(),                       // health
		httpapi.NewSourceHealthHandler(nil),              // 1 SourceHealth
		httpapi.NewFileHandler(nil, stubSignedDownloader{}, time.Minute), // 2 File (downloader non-nil -> /files/dl)
		httpapi.NewMessageHandler(nil),                   // 3 Message
		httpapi.NewCommentHandler(nil),                   // 4 Comment
		httpapi.NewNotificationHandler(nil),              // 5 Notification
		httpapi.NewBlockHandler(nil),                     // 6 Block
		httpapi.NewEventsHandler(nil, nil),               // 7 Events (SSE hub; nil is safe at registration)
		httpapi.NewPreferenceHandler(nil),                // 8 Preference
		httpapi.NewAdminHandler(nil,                      // 9 Admin (bans non-nil -> /admin/bans*)
			[]service.BanService{stubBanService{}},
			[]httpapi.HubDisconnector{nil}, perm),
		httpapi.NewSearchHandler(nil),                    // 10 Search
		httpapi.NewAccountHandlerWithExtensions(nil,      // 11 Account (fencing/migration/cutover non-nil)
			stubRegionFencing{}, stubLegacyMigration{}, stubLegacyCutover{}, perm),
		httpapi.NewAggregationHandler(nil, perm),         // 12 Aggregation
		httpapi.NewEventIngestHandler(nil, "", 1<<20, time.Minute, // 13 EventIngest (permission non-nil)
			httpapi.WithEventPermissionChecker(perm)),
		httpapi.NewAuditLedgerHandlerWithExtensions(nil,  // 14 AuditLedger (holds/anchor/jobs non-nil)
			stubAuditHold{}, stubAuditAnchor{}, stubSyncJob{}, perm),
		httpapi.NewCallbackHandler(nil, perm),            // 15 Callback
		httpapi.NewOperationHandler(nil),                 // 16 Operation
		httpapi.NewSyncJobHandler(nil, perm),             // 17 SyncJob
		httpapi.NewReplayHandlerWithProjection(nil,       // 18 Replay (projection non-nil)
			stubReplayProjection{}, perm),
	)
}
