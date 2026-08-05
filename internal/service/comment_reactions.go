package service

import (
	"context"
	stderrors "errors"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

// Like records a like on a comment. The actor must be able to read the
// comment's target; the like is idempotent. A "comment.liked" outbox event
// is emitted (best-effort) so the comment owner receives a notification.
func (s *commentService) Like(ctx context.Context, id domain.ID) (*ReactionResult, error) {
	count, err := s.react(ctx, id, true)
	if err != nil {
		return nil, err
	}
	// Emit a "comment.liked" domain event so the comment owner receives
	// a notification. The event is best-effort: a failed outbox write must
	// not fail the like itself.
	c, getErr := s.comments.Get(ctx, id)
	if getErr == nil && c.OwnerUID != "" {
		actor, _ := mustActor(ctx)
		if c.OwnerUID != actor {
			evt := newEvent("comment", id.String(), "comment.liked", map[string]any{
				"comment_id":        id.String(),
				"comment_owner_uid": c.OwnerUID,
				"liker_uid":         actor,
			})
			// Best-effort: publish the event into the outbox without failing the like.
			_ = s.comments.AppendEvent(ctx, evt)
		}
	}
	return &ReactionResult{Liked: true, LikeCount: count}, nil
}

// Unlike removes a like on a comment. The actor must be able to read the
// comment's target; removing a non-existent like is idempotent.
func (s *commentService) Unlike(ctx context.Context, id domain.ID) (*ReactionResult, error) {
	count, err := s.react(ctx, id, false)
	if err != nil {
		return nil, err
	}
	return &ReactionResult{Liked: false, LikeCount: count}, nil
}

// react authorizes a like/unlike — the actor must be able to read the comment's
// target — and applies it, returning the updated like count.
func (s *commentService) react(ctx context.Context, id domain.ID, like bool) (int, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return 0, err
	}
	c, err := s.comments.Get(ctx, id)
	if stderrors.Is(err, repository.ErrNotFound) {
		return 0, apperr.NotFound("comment.not_found", "comment not found")
	}
	if err != nil {
		return 0, apperr.Internal("comment.get_failed", "failed to load comment").Wrap(err)
	}
	if err := s.assertTargetReadable(ctx, actor, c.TargetType, c.TargetID); err != nil {
		return 0, err
	}
	var count int
	if like {
		count, err = s.comments.Like(ctx, id, actor)
	} else {
		count, err = s.comments.Unlike(ctx, id, actor)
	}
	if err != nil {
		return 0, apperr.Internal("comment.react_failed", "failed to update reaction").Wrap(err)
	}
	return count, nil
}
