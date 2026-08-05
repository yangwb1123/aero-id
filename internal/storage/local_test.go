package storage_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/storage"
)

func newLocal(t *testing.T) storage.FileStorage {
	t.Helper()
	return storage.NewLocal(storage.LocalConfig{
		Root:          t.TempDir(),
		SigningKey:    "test-signing-key",
		PublicBaseURL: "http://example.com",
	})
}

func TestLocal_PutGetStatDelete(t *testing.T) {
	ctx := context.Background()
	s := newLocal(t)
	key, n, err := s.Put(ctx, "uid-1", "hello.txt", strings.NewReader("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 11 {
		t.Errorf("size = %d", n)
	}
	if !strings.Contains(key, "uid-1") {
		t.Errorf("key doesn't include owner: %q", key)
	}

	rc, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	if string(body) != "hello world" {
		t.Errorf("body = %q", body)
	}

	info, err := s.Stat(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 11 {
		t.Errorf("stat size = %d", info.Size)
	}

	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("after delete, Get returned %v", err)
	}
}

func TestLocal_PresignVerifies(t *testing.T) {
	ctx := context.Background()
	s := newLocal(t)
	key, _, _ := s.Put(ctx, "uid-2", "f.bin", strings.NewReader("data"))

	url, err := s.PresignGet(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "http://example.com/files/dl?") {
		t.Errorf("url = %q", url)
	}

	local := s.(*storage.Local)
	if err := local.VerifyPresignedURL(url, time.Now()); err != nil {
		t.Errorf("verify fresh url: %v", err)
	}
	// Tamper: change query
	tampered := strings.Replace(url, "key=", "key=xx", 1)
	if err := local.VerifyPresignedURL(tampered, time.Now()); err == nil {
		t.Error("verify tampered url should fail")
	}
	// Expired
	if err := local.VerifyPresignedURL(url, time.Now().Add(time.Hour)); err == nil {
		t.Error("verify expired url should fail")
	}
}
