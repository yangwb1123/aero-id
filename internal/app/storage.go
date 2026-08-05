package app

import (
	"context"
	"fmt"

	"github.com/aero/aero-id/internal/config"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/storage"
)

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
