//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/aero/aero-id/internal/storage"
)

const minioFixtureTimeout = time.Minute

type minioFixtureOwner interface {
	Cleanup(func())
	Errorf(string, ...any)
	Logf(string, ...any)
}

func startS3Fixture(t *testing.T, bucket string) (context.Context, *storage.S3) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	startCtx, cancelStart := context.WithTimeout(ctx, minioFixtureTimeout)
	started := time.Now()
	m, err := minio.Run(startCtx, "minio/minio:RELEASE.2025-01-20T14-49-07Z",
		minio.WithUsername("minio"), minio.WithPassword("minio123"), minioOwnership(t))
	cancelStart()
	logMinioFixturePhase(t, "run", started, err)
	if err != nil {
		t.Fatalf("minio start: %v", err)
	}
	endpoint, err := m.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("minio endpoint: %v", err)
	}
	s, err := storage.NewS3(ctx, storage.S3Config{
		Region: "us-east-1", Bucket: bucket, Endpoint: "http://" + endpoint,
		AccessKey: "minio", SecretKey: "minio123", UsePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, s
}

func minioOwnership(t minioFixtureOwner) testcontainers.CustomizeRequestOption {
	return func(req *testcontainers.GenericContainerRequest) error {
		req.LifecycleHooks = append(req.LifecycleHooks, testcontainers.ContainerLifecycleHooks{
			PostCreates: []testcontainers.ContainerHook{
				func(_ context.Context, c testcontainers.Container) error {
					t.Logf("fixture=minio phase=owned container_id=%s", c.GetContainerID())
					t.Cleanup(func() { cleanupMinioFixture(t, c.Terminate) })
					return nil
				},
			},
		})
		return nil
	}
}

func cleanupMinioFixture(t minioFixtureOwner, terminate func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), minioFixtureTimeout)
	defer cancel()
	started := time.Now()
	err := terminate(ctx)
	logMinioFixturePhase(t, "terminate", started, err)
	if err != nil {
		t.Errorf("terminate MinIO container: %v", err)
	}
}

func logMinioFixturePhase(t minioFixtureOwner, phase string, started time.Time, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	t.Logf("fixture=minio phase=%s elapsed=%s outcome=%s", phase, time.Since(started).Round(time.Millisecond), outcome)
}
