package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

type LocalConfig struct {
	Root          string
	SigningKey    string
	PublicBaseURL string
}

type Local struct {
	cfg LocalConfig
}

func NewLocal(cfg LocalConfig) *Local { return &Local{cfg: cfg} }

func (l *Local) keyToPath(key string) string {
	return filepath.Join(l.cfg.Root, key)
}

// NewKey allocates an owner-scoped storage key (kept identical to Put's layout) so
// the interface stays consistent across backends.
func (l *Local) NewKey(ownerUID string) string {
	now := time.Now().UTC()
	id := idgen.NewID().String()
	return filepath.ToSlash(filepath.Join(ownerUID, now.Format("2006"), now.Format("01"), id))
}

func (l *Local) Put(ctx context.Context, ownerUID, _ string, r io.Reader) (string, int64, error) {
	key := l.NewKey(ownerUID)
	path := l.keyToPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", 0, fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return "", 0, fmt.Errorf("create: %w", err)
	}
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		_ = os.Remove(path) // don't leak a partial object when the write fails
		return "", 0, fmt.Errorf("write: %w", err)
	}
	return key, n, nil
}

func (l *Local) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	f, err := os.Open(l.keyToPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	return f, nil
}

func (l *Local) Delete(ctx context.Context, key string) error {
	err := os.Remove(l.keyToPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	return nil
}

func (l *Local) Stat(ctx context.Context, key string) (*ObjectInfo, error) {
	info, err := os.Stat(l.keyToPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	return &ObjectInfo{
		Key:      key,
		Size:     info.Size(),
		Modified: info.ModTime().UTC(),
	}, nil
}

func (l *Local) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	expires := time.Now().Add(ttl).Unix()
	sig := l.sign(key, expires)
	v := url.Values{}
	v.Set("key", key)
	v.Set("exp", strconv.FormatInt(expires, 10))
	v.Set("sig", sig)
	return l.cfg.PublicBaseURL + "/files/dl?" + v.Encode(), nil
}

// PresignPut is unsupported on the local backend: there is no object store to PUT
// to directly, so direct uploads must go through the normal Put path.
func (l *Local) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return "", ErrNotSupported
}

func (l *Local) sign(key string, expires int64) string {
	return presignHMAC(key, expires, l.cfg.SigningKey)
}

// VerifyPresignedURL is used by the download handler to authenticate a presigned URL.
// It validates the signature and rejects expired URLs based on `now`.
func (l *Local) VerifyPresignedURL(rawURL string, now time.Time) error {
	return verifyPresignedURL(rawURL, now, l.cfg.SigningKey)
}

// List walks the storage root and reports every regular file as an object, with its
// storage key (the root-relative slash path) and mtime. Directories are descended into
// but not reported. Implements Lister so the GC sweep can detect orphaned blobs.
func (l *Local) List(ctx context.Context, fn func(ObjectInfo) (bool, error)) error {
	root := l.cfg.Root
	stop := false
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		return l.walkEntry(ctx, root, &stop, path, d, walkErr, fn)
	})
	if err != nil {
		return fmt.Errorf("list local objects: %w", err)
	}
	return nil
}

//nolint:cyclop // filepath traversal handles filesystem, cancellation, and callback stop states.
func (l *Local) walkEntry(ctx context.Context, root string, stop *bool, path string, d os.DirEntry,
	walkErr error, fn func(ObjectInfo) (bool, error)) error {
	if walkErr != nil {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		return walkErr
	}
	if *stop {
		return filepath.SkipAll
	}
	if d.IsDir() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := d.Info()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	cont, err := fn(ObjectInfo{Key: filepath.ToSlash(rel), Size: info.Size(), Modified: info.ModTime().UTC()})
	if err != nil {
		return err
	}
	if !cont {
		*stop = true
		return filepath.SkipAll
	}
	return nil
}

// Ensure Local satisfies the interfaces.
var (
	_ FileStorage = (*Local)(nil)
	_ Lister      = (*Local)(nil)
)
