package service

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type commentService struct {
	comments repository.CommentRepository
	files    repository.FileRepository
	messages repository.MessageRepository
	dir      UserDirectory
}

func NewCommentService(
	comments repository.CommentRepository,
	files repository.FileRepository,
	messages repository.MessageRepository,
	dir UserDirectory,
) CommentService {
	return &commentService{comments: comments, files: files, messages: messages, dir: dir}
}

// assertTargetReadable verifies the comment target exists and the actor may read
// it, reusing the same ACL as the file/message services.
func (s *commentService) assertTargetReadable(ctx context.Context, actor string, t domain.TargetType, target domain.ID) error {
	switch t {
	case domain.TargetTypeFile:
		f, err := s.files.Get(ctx, target)
		if stderrors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("comment.target_not_found", "target file not found")
		}
		if err != nil {
			return apperr.Internal("comment.target_check_failed", "failed to verify target").Wrap(err)
		}
		if !canReadFile(actor, f) {
			return apperr.Forbidden("comment.forbidden", "not allowed to access the target")
		}
		return nil
	case domain.TargetTypeMessage:
		m, err := s.messages.Get(ctx, target)
		if stderrors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("comment.target_not_found", "target message not found")
		}
		if err != nil {
			return apperr.Internal("comment.target_check_failed", "failed to verify target").Wrap(err)
		}
		if !canReadMessage(actor, m) {
			return apperr.Forbidden("comment.forbidden", "not allowed to access the target")
		}
		return nil
	default:
		return apperr.BadRequest("comment.invalid", "unsupported target type")
	}
}

func (s *commentService) Create(ctx context.Context, in CreateCommentInput) (*domain.Comment, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if in.Content == "" {
		return nil, apperr.BadRequest("comment.invalid", "content is required")
	}
	if err := s.assertTargetReadable(ctx, actor, in.TargetType, in.TargetID); err != nil {
		return nil, err
	}

	var parent *domain.Comment
	if in.ParentID != nil {
		p, err := s.comments.Get(ctx, *in.ParentID)
		if stderrors.Is(err, repository.ErrNotFound) {
			return nil, apperr.BadRequest("comment.invalid", "parent comment not found")
		}
		if err != nil {
			return nil, apperr.Internal("comment.get_failed", "failed to load parent comment").Wrap(err)
		}
		if p.TargetType != in.TargetType || p.TargetID != in.TargetID {
			return nil, apperr.BadRequest("comment.invalid", "parent belongs to a different target")
		}
		// Enforce the two-level nesting rule at the domain layer.
		if err := domain.ValidateNewComment(p); err != nil {
			return nil, apperr.BadRequest("comment.nesting_too_deep", "replies may not be nested more than two levels")
		}
		parent = p
	}

	c := domain.NewComment(idgen.NewID(), actor, in.TargetType, in.TargetID, parent, in.Content)
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	if err := s.comments.Create(ctx, c); err != nil {
		return nil, apperr.Internal("comment.create_failed", "failed to create comment").Wrap(err)
	}
	if parent != nil {
		if err := s.comments.IncReplyCount(ctx, parent.ID); err != nil {
			return nil, apperr.Internal("comment.create_failed", "failed to update reply count").Wrap(err)
		}
	}
	return c, nil
}

func (s *commentService) List(ctx context.Context, t domain.TargetType, target domain.ID, page domain.Page) (*CommentList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.assertTargetReadable(ctx, actor, t, target); err != nil {
		return nil, err
	}
	cs, next, err := s.comments.ListByTarget(ctx, t, target, page)
	if err != nil {
		return nil, apperr.Internal("comment.list_failed", "failed to list comments").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, authorUIDs(cs))
	if err != nil {
		return nil, apperr.Internal("comment.enrich_failed", "failed to resolve authors").Wrap(err)
	}
	return &CommentList{Comments: cs, NextCursor: next, Users: users}, nil
}

func (s *commentService) Replies(ctx context.Context, rootID domain.ID, page domain.Page) (*CommentList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	root, err := s.comments.Get(ctx, rootID)
	if stderrors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NotFound("comment.not_found", "root comment not found")
	}
	if err != nil {
		return nil, apperr.Internal("comment.get_failed", "failed to load root comment").Wrap(err)
	}
	if err := s.assertTargetReadable(ctx, actor, root.TargetType, root.TargetID); err != nil {
		return nil, err
	}
	cs, next, err := s.comments.ListReplies(ctx, rootID, page)
	if err != nil {
		return nil, apperr.Internal("comment.list_failed", "failed to list replies").Wrap(err)
	}
	users, err := enrichUsers(ctx, s.dir, authorUIDs(cs))
	if err != nil {
		return nil, apperr.Internal("comment.enrich_failed", "failed to resolve authors").Wrap(err)
	}
	return &CommentList{Comments: cs, NextCursor: next, Users: users}, nil
}

func (s *commentService) Delete(ctx context.Context, id domain.ID) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if c.OwnerUID != actor {
		return apperr.Forbidden("comment.forbidden", "only the author can delete this comment")
	}
	if err := s.comments.SoftDelete(ctx, id); err != nil {
		return apperr.Internal("comment.delete_failed", "failed to delete comment").Wrap(err)
	}
	return nil
}

// authorUIDs returns the distinct owner_uids preserving first-seen order.
func authorUIDs(cs []*domain.Comment) []string {
	seen := make(map[string]struct{}, len(cs))
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		if _, ok := seen[c.OwnerUID]; ok {
			continue
		}
		seen[c.OwnerUID] = struct{}{}
		out = append(out, c.OwnerUID)
	}
	return out
}
