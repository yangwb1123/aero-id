package service

import (
	"context"
	stderrors "errors"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

// UpdateMetadata lets the file's owner change mutable metadata without re-uploading.
// Only the provided (non-nil) fields are applied; a provided meta map is merged into
// the existing meta. Validation: a provided name must be non-blank (BadRequest); a
// provided visibility must be a known value (BadRequest); a soft-deleted/missing file
// is NotFound; non-owners get Forbidden. updated_at is bumped on every change.
//
//nolint:cyclop,funlen // legacy metadata behavior remains compatibility-bound until Vault migration.
func (s *fileService) UpdateMetadata(ctx context.Context, id domain.ID, in UpdateFileMetadataInput) (*domain.File, error) {
	actor, err := mustActor(ctx)
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
		return f, nil // nothing requested; return the current state unchanged
	}

	now := time.Now().UTC()
	fields["updated_at"] = now
	if err := s.files.UpdateMetadata(ctx, id, fields); err != nil {
		if stderrors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NotFound("file.not_found", "file not found")
		}
		return nil, apperr.Internal("file.update_failed", "failed to update file").Wrap(err)
	}
	f.UpdatedAt = now
	return f, nil
}

// ShareFile grants read access on a file the caller owns to each grantee uid. It is
// idempotent and rejects blank uids and self-grants (the owner already has access).
//
//nolint:cyclop // legacy sharing behavior remains compatibility-bound until Vault migration.
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if s.shares == nil {
		return apperr.Internal("file.sharing_unavailable", "file sharing is not enabled")
	}
	f, err := s.ownedFile(ctx, id, actor)
	if err != nil {
		return err
	}
	// Validate and de-duplicate the grantee set: reject blank uids; silently drop a
	// self-grant since the owner already has access.
	seen := make(map[string]bool, len(granteeUIDs))
	grantees := make([]string, 0, len(granteeUIDs))
	for _, g := range granteeUIDs {
		g = strings.TrimSpace(g)
		if g == "" {
			return apperr.BadRequest("file.invalid_grantee", "grantee uid must not be blank")
		}
		if g == f.OwnerUID || seen[g] {
			continue
		}
		seen[g] = true
		grantees = append(grantees, g)
	}
	if len(grantees) == 0 {
		return nil
	}
	if err := s.shares.Grant(ctx, id, f.OwnerUID, grantees); err != nil {
		return apperr.Internal("file.share_failed", "failed to share file").Wrap(err)
	}
	if s.audit != nil {
		for _, g := range grantees {
			_ = s.audit.Log(ctx, &domain.AuditEvent{
				ActorUID:    actor,
				Action:      domain.AuditFileShareGrant,
				SubjectType: "file",
				SubjectID:   id.String(),
				TargetUID:   g,
			})
		}
	}
	return nil
}

// UnshareFile revokes a single grantee's access to a file the caller owns. Revoking a
// grant that does not exist is a no-op.
func (s *fileService) UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if s.shares == nil {
		return apperr.Internal("file.sharing_unavailable", "file sharing is not enabled")
	}
	if strings.TrimSpace(granteeUID) == "" {
		return apperr.BadRequest("file.invalid_grantee", "grantee uid must not be blank")
	}
	if _, err := s.ownedFile(ctx, id, actor); err != nil {
		return err
	}
	if err := s.shares.Revoke(ctx, id, granteeUID); err != nil {
		return apperr.Internal("file.unshare_failed", "failed to unshare file").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, &domain.AuditEvent{
			ActorUID:    actor,
			Action:      domain.AuditFileShareRevoke,
			SubjectType: "file",
			SubjectID:   id.String(),
			TargetUID:   granteeUID,
		})
	}
	return nil
}

// ListSharedWithMe lists files other owners have shared with the caller.
func (s *fileService) ListSharedWithMe(ctx context.Context, page domain.Page) (*FileList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.shares == nil {
		return &FileList{Files: []*domain.File{}}, nil
	}
	files, next, err := s.shares.ListFilesSharedWith(ctx, actor, page)
	if err != nil {
		return nil, apperr.Internal("file.list_failed", "failed to list shared files").Wrap(err)
	}
	return &FileList{Files: files, NextCursor: next}, nil
}

// ownedFile loads a file and asserts the actor is its owner, translating raw errors
// to the same NotFound/Forbidden AppErrors the other owner-only operations return.
func (s *fileService) ownedFile(ctx context.Context, id domain.ID, actor string) (*domain.File, error) {
	f, err := s.files.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NotFound("file.not_found", "file not found")
	}
	if err != nil {
		return nil, apperr.Internal("file.get_failed", "failed to load file").Wrap(err)
	}
	if f.OwnerUID != actor {
		return nil, apperr.Forbidden("file.forbidden", "only the owner can share this file")
	}
	return f, nil
}
