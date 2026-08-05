package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

//nolint:cyclop,funlen // legacy file creation preserves the deployed compatibility API.
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*File, error) {
	actor, err := domain.MustActor(ctx)
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

	reused := false
	if existing, ferr := s.files.FindByChecksum(ctx, actor, checksum); ferr == nil {
		if existing.StorageKey != key {
			_ = s.store.Delete(ctx, key)
			key = existing.StorageKey
		}
		reused = true
	} else if !stderrors.Is(ferr, domain.ErrNotFound) {
		_ = s.store.Delete(ctx, key)
		return nil, apperr.Internal("file.dedup_failed", "failed to check for an existing file").Wrap(ferr)
	}

	now := time.Now().UTC()
	f := &File{
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
			_ = s.store.Delete(ctx, key)
		}
		return nil, apperr.Internal("file.create_failed", "failed to persist file").Wrap(err)
	}
	return f, nil
}

func (s *fileService) Get(ctx context.Context, id domain.ID) (*File, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.files.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("file.not_found", "file not found")
	}
	if err != nil {
		return nil, apperr.Internal("file.get_failed", "failed to load file").Wrap(err)
	}
	allowed, err := s.canRead(ctx, actor, f)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, apperr.Forbidden("file.forbidden", "not allowed to access this file")
	}
	return f, nil
}

func (s *fileService) List(ctx context.Context, ownerUID string, page domain.Page) (*FileList, error) {
	actor, err := domain.MustActor(ctx)
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
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return err
	}
	f, err := s.files.Get(ctx, id)
	if stderrors.Is(err, domain.ErrNotFound) {
		return apperr.NotFound("file.not_found", "file not found")
	}
	if err != nil {
		return apperr.Internal("file.get_failed", "failed to load file").Wrap(err)
	}
	if f.OwnerUID != actor {
		return apperr.Forbidden("file.forbidden", "only the owner can delete this file")
	}
	if err := s.files.Delete(ctx, id); err != nil {
		return apperr.Internal("file.delete_failed", "failed to delete file").Wrap(err)
	}
	if n, cerr := s.files.CountByStorageKey(ctx, f.StorageKey); cerr == nil && n == 0 {
		_ = s.store.Delete(ctx, f.StorageKey)
	}
	return nil
}

//nolint:cyclop,funlen // legacy metadata mutation preserves source-compatible validation and ownership checks.
func (s *fileService) UpdateMetadata(ctx context.Context, id domain.ID, in UpdateFileMetadataInput) (*File, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.ownedFile(ctx, id, actor)
	if err != nil {
		return nil, err
	}

	fields := map[string]any{}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return nil, apperr.BadRequest("file.invalid", "name must not be blank")
		}
		fields["name"] = name
		f.Name = name
	}
	if in.Visibility != nil {
		v := *in.Visibility
		if v != domain.VisibilityPrivate && v != domain.VisibilityPublic {
			return nil, apperr.BadRequest("file.invalid_visibility", "visibility must be 0 (private) or 1 (public)")
		}
		fields["visibility"] = int16(v)
		f.Visibility = v
	}
	if in.Meta != nil {
		merged := make(map[string]any, len(f.Meta)+len(in.Meta))
		for k, v := range f.Meta {
			merged[k] = v
		}
		for k, v := range in.Meta {
			merged[k] = v
		}
		fields["meta"] = merged
		f.Meta = merged
	}
	if len(fields) == 0 {
		return f, nil
	}

	now := time.Now().UTC()
	fields["updated_at"] = now
	if err := s.files.UpdateMetadata(ctx, id, fields); err != nil {
		if stderrors.Is(err, domain.ErrNotFound) {
			return nil, apperr.NotFound("file.not_found", "file not found")
		}
		return nil, apperr.Internal("file.update_failed", "failed to update file").Wrap(err)
	}
	f.UpdatedAt = now
	return f, nil
}
