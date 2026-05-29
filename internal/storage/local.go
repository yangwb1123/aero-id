package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	mac := hmac.New(sha256.New, []byte(l.cfg.SigningKey))
	fmt.Fprintf(mac, "%s|%d", key, expires)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyPresignedURL is used by the download handler to authenticate a presigned URL.
// It validates the signature and rejects expired URLs based on `now`.
func (l *Local) VerifyPresignedURL(rawURL string, now time.Time) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	q := u.Query()
	key := q.Get("key")
	expStr := q.Get("exp")
	gotSig := q.Get("sig")
	if key == "" || expStr == "" || gotSig == "" {
		return errors.New("presign: missing params")
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return fmt.Errorf("presign: bad exp: %w", err)
	}
	if now.Unix() > exp {
		return errors.New("presign: expired")
	}
	wantSig := l.sign(key, exp)
	if !hmac.Equal([]byte(gotSig), []byte(wantSig)) {
		return errors.New("presign: bad signature")
	}
	return nil
}

// List walks the storage root and reports every regular file as an object, with its
// storage key (the root-relative slash path) and mtime. Directories are descended into
// but not reported. Implements Lister so the GC sweep can detect orphaned blobs.
func (l *Local) List(ctx context.Context, fn func(ObjectInfo) (bool, error)) error {
	root := l.cfg.Root
	stop := false
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil // root not created yet: nothing to sweep
			}
			return err
		}
		if stop {
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		key := filepath.ToSlash(rel)
		info, ierr := d.Info()
		if ierr != nil {
			if errors.Is(ierr, os.ErrNotExist) {
				return nil // raced with a delete; skip
			}
			return ierr
		}
		cont, ferr := fn(ObjectInfo{
			Key:      key,
			Size:     info.Size(),
			Modified: info.ModTime().UTC(),
		})
		if ferr != nil {
			return ferr
		}
		if !cont {
			stop = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("list local objects: %w", err)
	}
	return nil
}

// Ensure Local satisfies the interfaces.
var (
	_ FileStorage = (*Local)(nil)
	_ Lister      = (*Local)(nil)
)
