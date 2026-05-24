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
type FileStorage interface {
	Put(ctx context.Context, ownerUID, name string, r io.Reader) (key string, size int64, err error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	Stat(ctx context.Context, key string) (*ObjectInfo, error)
}
