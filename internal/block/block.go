package block

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

type BlockChecker interface {
	IsBlocked(ctx context.Context, blocker, blocked string) (bool, error)
}

type Repository interface {
	Block(ctx context.Context, blocker, blocked string) error
	Unblock(ctx context.Context, blocker, blocked string) error
	ListBlocked(ctx context.Context, blocker string, page domain.Page) ([]string, string, error)
	IsBlocked(ctx context.Context, blocker, blocked string) (bool, error)
}

type BlockList struct {
	UIDs       []string
	NextCursor string
}

type Service interface {
	Block(ctx context.Context, uid string) error
	Unblock(ctx context.Context, uid string) error
	ListBlocked(ctx context.Context, page domain.Page) (*BlockList, error)
}

type blockService struct {
	blocks Repository
	audit  domain.AuditLogger
}

type Option func(*blockService)

func WithAuditLogger(a domain.AuditLogger) Option {
	return func(s *blockService) { s.audit = a }
}

func NewService(blocks Repository, opts ...Option) Service {
	s := &blockService{blocks: blocks}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *blockService) Block(ctx context.Context, uid string) error {
	actor, err := domain.MustActor(ctx)
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
			ActorUID: actor, Action: domain.AuditUserBlock,
			SubjectType: "user", SubjectID: uid, TargetUID: uid,
		})
	}
	return nil
}

func (s *blockService) Unblock(ctx context.Context, uid string) error {
	actor, err := domain.MustActor(ctx)
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
			ActorUID: actor, Action: domain.AuditUserUnblock,
			SubjectType: "user", SubjectID: uid, TargetUID: uid,
		})
	}
	return nil
}

func (s *blockService) ListBlocked(ctx context.Context, page domain.Page) (*BlockList, error) {
	actor, err := domain.MustActor(ctx)
	if err != nil {
		return nil, err
	}
	uids, next, err := s.blocks.ListBlocked(ctx, actor, page)
	if err != nil {
		return nil, apperr.Internal("block.list_failed", "failed to list blocked users").Wrap(err)
	}
	return &BlockList{UIDs: uids, NextCursor: next}, nil
}
