package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

// BlockChecker is the narrow read-only view of the block store used by the message
// and notification guards. *blockService and repository.BlockRepository both satisfy
// it. IsBlocked(blocker, blocked) reports whether blocker has blocked blocked.
type BlockChecker interface {
	IsBlocked(ctx context.Context, blocker, blocked string) (bool, error)
}

type blockService struct {
	blocks repository.BlockRepository
	audit  AuditLogger // nil => audit inactive
}

// BlockOption configures optional blockService behaviour.
type BlockOption func(*blockService)

// WithBlockAuditLogger wires an AuditLogger into the block service.
func WithBlockAuditLogger(a AuditLogger) BlockOption {
	return func(s *blockService) { s.audit = a }
}

func NewBlockService(blocks repository.BlockRepository, opts ...BlockOption) BlockService {
	s := &blockService{blocks: blocks}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *blockService) Block(ctx context.Context, uid string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if uid == "" {
		return apperr.BadRequest("block.invalid", "uid is required")
	}
	if uid == actor {
		return apperr.BadRequest("block.invalid", "cannot block yourself")
	}
	if err := s.blocks.Block(ctx, actor, uid); err != nil {
		return apperr.Internal("block.block_failed", "failed to block user").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, &domain.AuditEvent{
			ActorUID:    actor,
			Action:      domain.AuditUserBlock,
			SubjectType: "user",
			SubjectID:   uid,
			TargetUID:   uid,
		})
	}
	return nil
}

func (s *blockService) Unblock(ctx context.Context, uid string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if uid == "" {
		return apperr.BadRequest("block.invalid", "uid is required")
	}
	if err := s.blocks.Unblock(ctx, actor, uid); err != nil {
		return apperr.Internal("block.unblock_failed", "failed to unblock user").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, &domain.AuditEvent{
			ActorUID:    actor,
			Action:      domain.AuditUserUnblock,
			SubjectType: "user",
			SubjectID:   uid,
			TargetUID:   uid,
		})
	}
	return nil
}

func (s *blockService) ListBlocked(ctx context.Context, page domain.Page) (*BlockList, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	uids, next, err := s.blocks.ListBlocked(ctx, actor, page)
	if err != nil {
		return nil, apperr.Internal("block.list_failed", "failed to list blocked users").Wrap(err)
	}
	return &BlockList{UIDs: uids, NextCursor: next}, nil
}
