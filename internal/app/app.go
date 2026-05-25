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
	"github.com/aero/aero-id/internal/pkg/db"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
	"github.com/aero/aero-id/internal/storage"
)

// presignTTL is how long a generated download URL stays valid.
const presignTTL = 5 * time.Minute

type App struct {
	cfg        *config.Config
	log        *zap.Logger
	sqlDB      *sql.DB
	sso        *sso.Client
	httpServer *http.Server
	grpcServer *grpclib.Server
	grpcLis    net.Listener
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

	ssoClient, err := sso.NewClient(ctx, cfg.SSO.Endpoint, cfg.SSO.Insecure, sso.ClientConfig{
		TokenCacheTTL: cfg.SSO.TokenCacheTTL,
		UserCacheTTL:  cfg.SSO.UserCacheTTL,
		Timeout:       cfg.SSO.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("sso client: %w", err)
	}

	fileRepo := repository.NewFileRepository(gdb)
	msgRepo := repository.NewMessageRepository(gdb)
	commentRepo := repository.NewCommentRepository(gdb)

	fileSvc := service.NewFileService(fileRepo, store, cfg.Storage.Type,
		service.WithMaxUploadBytes(cfg.Storage.MaxUploadBytes),
		service.WithAllowedMimeTypes(cfg.Storage.AllowedMimeTypes),
	)
	msgSvc := service.NewMessageService(msgRepo, fileRepo, ssoClient)
	commentSvc := service.NewCommentService(commentRepo, fileRepo, msgRepo, ssoClient)

	// HTTP transport. Readiness probes the DB (critical) and SSO (criticality is
	// configurable so an SSO-less dev stack stays "ready" rather than 503-ing).
	health := httpapi.NewHealthHandler(
		httpapi.Check{Name: "database", Critical: true, Probe: sqlDB.PingContext},
		httpapi.Check{Name: "sso", Critical: cfg.SSO.ReadinessCritical, Probe: ssoClient.HealthCheck},
	)
	router := httpapi.NewRouter(
		log,
		httpapi.RouterConfig{},
		ssoClient,
		health,
		httpapi.NewFileHandler(fileSvc, downloader, presignTTL),
		httpapi.NewMessageHandler(msgSvc),
		httpapi.NewCommentHandler(commentSvc),
	)
	httpServer := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.HTTP.Host, cfg.Server.HTTP.Port),
		Handler:      router,
		ReadTimeout:  cfg.Server.HTTP.ReadTimeout,
		WriteTimeout: cfg.Server.HTTP.WriteTimeout,
	}

	// gRPC transport
	grpcServer := grpcsrv.NewServer(
		ssoClient, log,
		grpcsrv.NewFileServer(fileSvc),
		grpcsrv.NewMessageServer(msgSvc),
		grpcsrv.NewCommentServer(commentSvc),
	)
	grpcAddr := fmt.Sprintf("%s:%d", cfg.Server.GRPC.Host, cfg.Server.GRPC.Port)
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return nil, fmt.Errorf("grpc listen on %s: %w", grpcAddr, err)
	}

	return &App{
		cfg:        cfg,
		log:        log,
		sqlDB:      sqlDB,
		sso:        ssoClient,
		httpServer: httpServer,
		grpcServer: grpcServer,
		grpcLis:    grpcLis,
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
	shCtx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.HTTP.ShutdownTimeout)
	defer cancel()
	if err := a.httpServer.Shutdown(shCtx); err != nil {
		a.log.Error("http shutdown error", zap.Error(err))
	}
	a.grpcServer.GracefulStop()
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
