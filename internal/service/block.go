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
}

// NewBlockService builds the block service. The WithAudit surface is part of
// BlockRepository, so a block service without an audit path is unconstructible
// at compile time (fail-closed by construction — the WithBlockAuditLogger gate
// was removed in the B2-2 closure).
func NewBlockService(blocks repository.BlockRepository) BlockService {
	return &blockService{blocks: blocks}
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
	if err := s.blocks.BlockWithAudit(ctx, actor, uid,
		buildLegacyAuditFact(ctx, actor, domain.AuditUserBlock, "user", uid, uid, nil, domain.ID{})); err != nil {
		return apperr.Internal("block.block_failed", "failed to block user").Wrap(err)
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
	if err := s.blocks.UnblockWithAudit(ctx, actor, uid,
		buildLegacyAuditFact(ctx, actor, domain.AuditUserUnblock, "user", uid, uid, nil, domain.ID{})); err != nil {
		return apperr.Internal("block.unblock_failed", "failed to unblock user").Wrap(err)
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
