package file

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/storage"
)

func (s *fileService) DownloadURL(ctx context.Context, id domain.ID, ttl time.Duration) (string, error) {
	f, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	url, err := s.store.PresignGet(ctx, f.StorageKey, ttl)
	if err != nil {
		return "", apperr.Internal("file.presign_failed", "failed to presign download URL").Wrap(err)
	}
	return url, nil
}

func (s *fileService) PresignUpload(ctx context.Context, in PresignUploadInput, ttl time.Duration) (*PresignUploadResult, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.Name == "" {
		return nil, apperr.BadRequest("file.invalid", "name is required")
	}
	if !s.mimeAllowed(in.MimeType) {
		return nil, apperr.BadRequest("file.unsupported_type", "file content type is not allowed")
	}
	key := s.store.NewKey(actor)
	url, err := s.store.PresignPut(ctx, key, ttl)
	if stderrors.Is(err, storage.ErrNotSupported) {
		return nil, apperr.BadRequest("file.presign_unsupported", "direct upload is not supported by this storage backend")
	}
	if err != nil {
		return nil, apperr.Internal("file.presign_failed", "failed to presign upload URL").Wrap(err)
	}
	return &PresignUploadResult{FileID: idgen.NewID(), UploadURL: url, StorageKey: key}, nil
}

//nolint:cyclop // legacy upload completion preserves idempotent storage behavior.
func (s *fileService) CompleteUpload(ctx context.Context, in CompleteUploadInput) (*File, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.FileID == (domain.ID{}) {
		return nil, apperr.BadRequest("file.invalid", "file id is required")
	}
	if in.Name == "" {
		return nil, apperr.BadRequest("file.invalid", "name is required")
	}
	if !s.mimeAllowed(in.MimeType) {
		return nil, apperr.BadRequest("file.unsupported_type", "file content type is not allowed")
	}
	if !strings.HasPrefix(in.StorageKey, actor+"/") {
		return nil, apperr.Forbidden("file.forbidden", "not allowed to finalize this upload")
	}
	info, err := s.store.Stat(ctx, in.StorageKey)
	if stderrors.Is(err, storage.ErrNotFound) {
		return nil, apperr.BadRequest("file.not_uploaded", "no object found at the upload key")
	}
	if err != nil {
		return nil, apperr.Internal("file.stat_failed", "failed to stat uploaded object").Wrap(err)
	}
	if s.maxBytes > 0 && info.Size > s.maxBytes {
		_ = s.store.Delete(ctx, in.StorageKey)
		return nil, apperr.BadRequest("file.too_large", fmt.Sprintf("file exceeds the %d-byte limit", s.maxBytes))
	}

	now := time.Now().UTC()
	f := &File{
		ID:          in.FileID,
		OwnerUID:    actor,
		Name:        in.Name,
		Size:        info.Size,
		MimeType:    in.MimeType,
		StorageKey:  in.StorageKey,
		StorageType: s.storageType,
		Visibility:  in.Visibility,
		Meta:        in.Meta,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.files.Create(ctx, f); err != nil {
		_ = s.store.Delete(ctx, in.StorageKey)
		return nil, apperr.Internal("file.create_failed", "failed to persist file").Wrap(err)
	}
	return f, nil
}
