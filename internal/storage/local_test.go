package storage_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/storage"
)

func newLocal(t *testing.T) storage.FileStorage {
	t.Helper()
	return newLocalAt(t, t.TempDir())
}

func newLocalAt(t *testing.T, root string) storage.FileStorage {
	t.Helper()
	return storage.NewLocal(storage.LocalConfig{
		Root:          root,
		SigningKey:    "test-signing-key",
		PublicBaseURL: "http://example.com",
	})
}

func TestLocal_RejectsTraversalKeys(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-outside")
	if err := os.WriteFile(outside, []byte("must remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	s := newLocalAt(t, root)
	keys := []string{"../outside", "/tmp/absolute", `owner\\..\\outside`, "", ".", "owner/../outside"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			if _, err := s.Get(context.Background(), key); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("Get(%q) error = %v, want ErrInvalidKey", key, err)
			}
			if err := s.Delete(context.Background(), key); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("Delete(%q) error = %v, want ErrInvalidKey", key, err)
			}
			if _, err := s.Stat(context.Background(), key); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("Stat(%q) error = %v, want ErrInvalidKey", key, err)
			}
		})
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("outside sentinel disappeared: %v", err)
	}
	if string(got) != "must remain" {
		t.Fatalf("outside sentinel changed to %q", got)
	}
}

func TestLocal_NewKeyRejectsUnsafeOwner(t *testing.T) {
	l := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	for _, owner := range []string{"", ".", "..", "../escape", `owner\\child`} {
		if key := l.NewKey(owner); key != "" {
			t.Errorf("NewKey(%q) = %q, want empty key", owner, key)
		}
	}
	key := l.NewKey("owner-1")
	if key == "" || !strings.HasPrefix(key, "owner-1/") || strings.Contains(key, "..") {
		t.Fatalf("safe owner key = %q", key)
	}
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
