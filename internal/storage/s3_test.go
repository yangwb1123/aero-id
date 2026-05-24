//go:build integration

package storage_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/aero/aero-id/internal/storage"
)

func TestS3_RoundTrip(t *testing.T) {
	ctx := context.Background()
	m, err := minio.Run(ctx, "minio/minio:RELEASE.2025-01-20T14-49-07Z",
		minio.WithUsername("minio"), minio.WithPassword("minio123"))
	if err != nil {
		t.Fatalf("minio start: %v", err)
	}
	t.Cleanup(func() { _ = m.Terminate(ctx) })

	endpoint, _ := m.ConnectionString(ctx)
	s, err := storage.NewS3(ctx, storage.S3Config{
		Region:       "us-east-1",
		Bucket:       "test-bucket",
		Endpoint:     "http://" + endpoint,
		AccessKey:    "minio",
		SecretKey:    "minio123",
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Bucket creation is a one-time bootstrap; MinIO does not auto-create.
	// For MVP we assume infra creates buckets; here we create it via raw client.
	if err := createBucket(ctx, s, "test-bucket"); err != nil {
		t.Fatal(err)
	}

	key, n, err := s.Put(ctx, "uid", "f.bin", strings.NewReader("hello s3"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Errorf("size = %d", n)
	}

	rc, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != "hello s3" {
		t.Errorf("body = %q", body)
	}

	info, err := s.Stat(ctx, key)
	if err != nil || info.Size != 8 {
		t.Fatalf("stat %+v err=%v", info, err)
	}

	url, err := s.PresignGet(ctx, key, 5*time.Minute)
	if err != nil || !strings.Contains(url, "X-Amz-Signature") {
		t.Errorf("presign url = %q err=%v", url, err)
	}

	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, key); err != storage.ErrNotFound {
		t.Errorf("after delete: %v", err)
	}
}

// createBucket is a small helper because the public API doesn't expose CreateBucket;
// in production buckets are pre-provisioned by infra.
func createBucket(ctx context.Context, s *storage.S3, _ string) error {
	return s.EnsureBucket(ctx)
}
