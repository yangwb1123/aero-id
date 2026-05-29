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

// TestS3_StreamingUploadAndPresignPut exercises the streaming (multipart) Put path
// with an object larger than the in-memory part size, plus the presigned direct-PUT
// URL. It must COMPILE under -tags integration; it is not run by the default suite.
func TestS3_StreamingUpload(t *testing.T) {
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
		Bucket:       "stream-bucket",
		Endpoint:     "http://" + endpoint,
		AccessKey:    "minio",
		SecretKey:    "minio123",
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}

	// ~12 MiB > the 5 MiB default part size, so the manager streams it in multiple
	// parts without buffering the whole body in RAM.
	const size = 12 << 20
	key, n, err := s.Put(ctx, "uid", "big.bin", io.LimitReader(zeroReader{}, size))
	if err != nil {
		t.Fatalf("streaming put: %v", err)
	}
	if n != size {
		t.Fatalf("size = %d, want %d", n, size)
	}

	info, err := s.Stat(ctx, key)
	if err != nil || info.Size != size {
		t.Fatalf("stat %+v err=%v", info, err)
	}

	// Presigned direct-PUT URL should be a usable, signed S3 URL.
	url, err := s.PresignPut(ctx, s.NewKey("uid"), 5*time.Minute)
	if err != nil || !strings.Contains(url, "X-Amz-Signature") {
		t.Errorf("presign put url = %q err=%v", url, err)
	}

	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
}

// zeroReader yields an endless stream of zero bytes; pair with io.LimitReader to feed
// the uploader a large body without allocating it up front.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// createBucket is a small helper because the public API doesn't expose CreateBucket;
// in production buckets are pre-provisioned by infra.
func createBucket(ctx context.Context, s *storage.S3, _ string) error {
	return s.EnsureBucket(ctx)
}
