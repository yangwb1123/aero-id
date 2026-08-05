package file

import (
	"context"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

//nolint:cyclop // legacy sharing keeps its deployed ACL and ownership semantics.
func (s *fileService) ShareFile(ctx context.Context, id domain.ID, granteeUIDs []string) error {
	actor, err := domain.MustActor(ctx)
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

func (s *fileService) UnshareFile(ctx context.Context, id domain.ID, granteeUID string) error {
	actor, err := domain.MustActor(ctx)
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

func (s *fileService) ListSharedWithMe(ctx context.Context, page domain.Page) (*FileList, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	if s.shares == nil {
		return &FileList{Files: []*File{}}, nil
	}
	files, next, err := s.shares.ListFilesSharedWith(ctx, actor, page)
	if err != nil {
		return nil, apperr.Internal("file.list_failed", "failed to list shared files").Wrap(err)
	}
	return &FileList{Files: files, NextCursor: next}, nil
}
