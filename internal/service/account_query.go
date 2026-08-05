package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
)

func (s *accountService) ListSources(ctx context.Context, page domain.Page) (*AccountSourceList, error) {
	account, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := s.repo.ListSources(ctx, account.ID, page)
	if err != nil {
		return nil, mapAccountError(err, "account.sources_failed", "failed to list account sources")
	}
	return &AccountSourceList{Sources: rows, NextCursor: next}, nil
}

func (s *accountService) ListMemberships(ctx context.Context, page domain.Page) (*AccountMembershipList, error) {
	account, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	rows, next, err := s.repo.ListMemberships(ctx, account.ID, page)
	if err != nil {
		return nil, mapAccountError(err, "account.memberships_failed", "failed to list account memberships")
	}
	return &AccountMembershipList{Memberships: rows, NextCursor: next}, nil
}
