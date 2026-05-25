package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/storage"
)

type fileService struct {
	files       repository.FileRepository
	store       storage.FileStorage
	storageType string // "local" | "s3", recorded on each row for later routing
	maxBytes    int64  // 0 = unlimited
	allowedMime map[string]bool
}

// FileOption configures optional fileService policy (upload limits, type allow-list).
type FileOption func(*fileService)

// WithMaxUploadBytes caps the size of an uploaded file. Zero (the default) means no limit.
func WithMaxUploadBytes(n int64) FileOption {
	return func(s *fileService) { s.maxBytes = n }
}

// WithAllowedMimeTypes restricts uploads to the given media types (compared on the
// base type, case-insensitively). An empty list (the default) allows any type.
func WithAllowedMimeTypes(types []string) FileOption {
	return func(s *fileService) {
		if len(types) == 0 {
			return
		}
		m := make(map[string]bool, len(types))
		for _, t := range types {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
				m[t] = true
			}
		}
		s.allowedMime = m
	}
}

func NewFileService(files repository.FileRepository, store storage.FileStorage, storageType string, opts ...FileOption) FileService {
	s := &fileService{files: files, store: store, storageType: storageType}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*domain.File, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.Name == "" {
		return nil, apperr.BadRequest("file.invalid", "name is required")
	}
	if in.Reader == nil {
		return nil, apperr.BadRequest("file.invalid", "file content is required")
	}
	if s.maxBytes > 0 && in.Size > s.maxBytes {
		return nil, apperr.BadRequest("file.too_large", fmt.Sprintf("file exceeds the %d-byte limit", s.maxBytes))
	}
	if !s.mimeAllowed(in.MimeType) {
		return nil, apperr.BadRequest("file.unsupported_type", "file content type is not allowed")
	}

	// Cap the stream as defense-in-depth (the backend can never write past the limit
	// even if the declared size lies) and hash it in the same pass so we can record a
	// content checksum and deduplicate identical uploads.
	var reader io.Reader = in.Reader
	if s.maxBytes > 0 {
		reader = &cappedReader{r: in.Reader, left: s.maxBytes}
	}
	hasher := sha256.New()
	reader = io.TeeReader(reader, hasher)

	key, size, err := s.store.Put(ctx, actor, in.Name, reader)
	if err != nil {
		if stderrors.Is(err, errFileTooLarge) {
			return nil, apperr.BadRequest("file.too_large", fmt.Sprintf("file exceeds the %d-byte limit", s.maxBytes))
		}
		return nil, apperr.Internal("file.store_failed", "failed to store file").Wrap(err)
	}
	checksum := hex.EncodeToString(hasher.Sum(nil))

	// Deduplicate: if this owner already has an identical file, reuse its blob and drop
	// the copy we just wrote. `reused` guards cleanup so we never delete a shared blob.
	reused := false
	if existing, ferr := s.files.FindByChecksum(ctx, actor, checksum); ferr == nil {
		if existing.StorageKey != key {
			_ = s.store.Delete(ctx, key)
			key = existing.StorageKey
		}
		reused = true
	} else if !stderrors.Is(ferr, repository.ErrNotFound) {
		_ = s.store.Delete(ctx, key)
		return nil, apperr.Internal("file.dedup_failed", "failed to check for an existing file").Wrap(ferr)
	}

	now := time.Now().UTC()
	f := &domain.File{
		ID:          idgen.NewID(),
		OwnerUID:    actor,
		Name:        in.Name,
		Size:        size,
		MimeType:    in.MimeType,
		StorageKey:  key,
		StorageType: s.storageType,
		Checksum:    checksum,
		Visibility:  in.Visibility,
		Meta:        in.Meta,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.files.Create(ctx, f); err != nil {
		if !reused {
			_ = s.store.Delete(ctx, key) // only clean up a blob we exclusively own
		}
		return nil, apperr.Internal("file.create_failed", "failed to persist file").Wrap(err)
	}
	return f, nil
}

func (s *fileService) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.files.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NotFound("file.not_found", "file not found")
	}
	if err != nil {
		return nil, apperr.Internal("file.get_failed", "failed to load file").Wrap(err)
	}
	if !canReadFile(actor, f) {
		return nil, apperr.Forbidden("file.forbidden", "not allowed to access this file")
	}
	return f, nil
}

func (s *fileService) List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if ownerUID == "" {
		ownerUID = actor
	}
	if ownerUID != actor {
		return nil, apperr.Forbidden("file.forbidden", "can only list your own files")
	}
	files, next, err := s.files.ListByOwner(ctx, ownerUID, page)
	if err != nil {
		return nil, apperr.Internal("file.list_failed", "failed to list files").Wrap(err)
	}
	return &FileList{Files: files, NextCursor: next}, nil
}

func (s *fileService) Delete(ctx context.Context, id domain.ID) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	f, err := s.files.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("file.not_found", "file not found")
	}
	if err != nil {
		return apperr.Internal("file.get_failed", "failed to load file").Wrap(err)
	}
	if f.OwnerUID != actor {
		return apperr.Forbidden("file.forbidden", "only the owner can delete this file")
	}
	// Soft-delete the authoritative row first, then remove the blob.
	if err := s.files.Delete(ctx, id); err != nil {
		return apperr.Internal("file.delete_failed", "failed to delete file").Wrap(err)
	}
	// Blob removal is dedup-safe and best-effort: drop the physical object only once no
	// other live file references the same storage key, and never fail the request over
	// it (the row is already gone; any orphan is left for a future GC sweep).
	if n, cerr := s.files.CountByStorageKey(ctx, f.StorageKey); cerr == nil && n == 0 {
		_ = s.store.Delete(ctx, f.StorageKey)
	}
	return nil
}

func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
	f, err := s.Get(ctx, id) // reuses the read ACL above
	if err != nil {
		return "", err
	}
	url, err := s.store.PresignGet(ctx, f.StorageKey, ttl)
	if err != nil {
		return "", apperr.Internal("file.presign_failed", "failed to presign download URL").Wrap(err)
	}
	return url, nil
}

// mimeAllowed reports whether the content type passes the configured allow-list.
// With no allow-list configured, every type is accepted.
func (s *fileService) mimeAllowed(contentType string) bool {
	if len(s.allowedMime) == 0 {
		return true
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return s.allowedMime[strings.ToLower(mt)]
}

// errFileTooLarge is returned by cappedReader once the configured byte limit is exceeded.
var errFileTooLarge = stderrors.New("file exceeds maximum allowed size")

// cappedReader wraps an io.Reader and fails with errFileTooLarge as soon as more
// than `left` bytes have been read, so an oversized upload is never fully written.
type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left < 0 {
		return 0, errFileTooLarge
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, errFileTooLarge
	}
	return n, err
}
