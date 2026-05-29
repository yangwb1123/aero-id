package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned when the requested object does not exist.
var ErrNotFound = errors.New("storage: not found")

// ErrNotSupported is returned by PresignGet on backends that cannot pre-sign.
var ErrNotSupported = errors.New("storage: operation not supported by backend")

// ObjectInfo is the common metadata returned by Stat.
type ObjectInfo struct {
	Key      string
	Size     int64
	ETag     string
	MimeType string
	Modified time.Time
}

// FileStorage is the backend-agnostic file store used by the file service.
//
//   - Put returns a backend-generated storage key plus the number of bytes written.
//     The reader is consumed once; implementations may buffer to disk for size/checksum.
//   - Get returns a stream the caller MUST close.
//   - Delete returns ErrNotFound when the key doesn't exist.
//   - PresignGet returns a time-limited URL the client can fetch directly.
//     Backends that cannot pre-sign return ErrNotSupported.
//   - NewKey allocates an owner-scoped storage key without writing anything, so a
//     direct-to-backend upload (see PresignPut) and its later finalize agree on the key.
//   - PresignPut returns a time-limited URL the client can PUT an object to directly,
//     bypassing the app process. Backends that cannot pre-sign return ErrNotSupported.
type FileStorage interface {
	Put(ctx context.Context, ownerUID, name string, r io.Reader) (key string, size int64, err error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	Stat(ctx context.Context, key string) (*ObjectInfo, error)
	NewKey(ownerUID string) string
	PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// Lister is an OPTIONAL capability: backends that can enumerate their stored objects
// implement it so the GC reconciliation sweep can detect orphaned blobs (objects with
// no referencing file row). Backends that cannot list objects simply don't implement it;
// the sweep type-asserts for Lister and no-ops orphan detection when it's absent.
type Lister interface {
	// List invokes fn for every stored object. Iteration stops early (without error) if
	// fn returns false, and is aborted if fn returns an error. Each ObjectInfo carries at
	// least Key and Modified so the sweep can apply a minimum-age guard.
	List(ctx context.Context, fn func(ObjectInfo) (bool, error)) error
}
