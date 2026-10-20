package service

import (
	"context"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

// FindByKey is an original-caller metadata lookup, not preparation. NotFound
// cannot prove that a previously sent preparation will never commit.
func (s *commandIntentService) FindByKey(ctx context.Context, key string) (*domain.CommandIntent, error) {
	principal, err := commandPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 200 {
		return nil, apperr.BadRequest("command.invalid_key", "original idempotency key is required")
	}
	intent, err := s.repo.FindByKey(ctx, principal, key)
	if err != nil {
		return nil, mapCommandIntentError(err)
	}
	return intent, nil
}

// Only the verified original principal may withdraw its own unsent preview.
// Withdrawal does not need renewed generation/source grants: it performs no
// source effect and creates no receipt. New preparation/execution still does.
func (s *commandIntentService) Abandon(ctx context.Context, id domain.ID, version int64, confirmation string) (*domain.CommandIntent, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	intent, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if version != 1 || confirmation != intent.ConfirmationDigest {
		return nil, mapCommandIntentError(repository.ErrCommandIntentConflict)
	}
	if intent.Status != "prepared" {
		return intent, nil // Retained immutable disposition or original executed receipt.
	}
	fact := commandIntentAudit(ctx, intent, "command.intent.abandoned")
	fact.SourceRegion = s.config.Region
	result, err := s.repo.Abandon(ctx, repository.CommandIntentAbandonment{Intent: intent, ExpectedVersion: version,
		ConfirmationDigest: confirmation, Region: s.config.Region, Fact: fact})
	if err != nil {
		return nil, mapCommandIntentError(err)
	}
	return result, nil
}
