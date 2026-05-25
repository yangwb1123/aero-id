package service

import (
	"context"
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

	// Cap the stream as defense-in-depth: even if the declared size is wrong (or
	// unknown), the backend can never write past the limit. cappedReader surfaces
	// errFileTooLarge, which the storage Put call propagates.
	reader := in.Reader
	if s.maxBytes > 0 {
		reader = &cappedReader{r: in.Reader, left: s.maxBytes}
	}

	key, size, err := s.store.Put(ctx, actor, in.Name, reader)
	if err != nil {
		if stderrors.Is(err, errFileTooLarge) {
			return nil, apperr.BadRequest("file.too_large", fmt.Sprintf("file exceeds the %d-byte limit", s.maxBytes))
		}
		return nil, apperr.Internal("file.store_failed", "failed to store file").Wrap(err)
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
		Visibility:  in.Visibility,
		Meta:        in.Meta,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.files.Create(ctx, f); err != nil {
		_ = s.store.Delete(ctx, key) // best-effort cleanup of the orphaned blob
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
	// The soft-deleted row is authoritative. Blob removal is best-effort: if it fails
	// the row is already gone, so we don't fail the request — the blob is left for a
	// future storage GC sweep rather than resurrecting the row.
	_ = s.store.Delete(ctx, f.StorageKey)
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
