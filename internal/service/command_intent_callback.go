package service

import (
	"context"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

func (s *commandIntentService) prepareIntentCallback(ctx context.Context, intent *domain.CommandIntent,
	plan domain.CommandPlan) (domain.CommandPlan, *domain.Operation, *domain.SyncJob, error) {
	if !plan.ValidCallbackReplayScope() {
		return plan, nil, nil, apperr.BadRequest("command.invalid_plan", "callback replay requires an explicit version-bound target")
	}
	// An explicit typed callback scope replaces datasets, not an empty wildcard.
	plan.Datasets = []domain.AccountDataset{}
	if err := s.authorizeCallbackCommand(ctx, intent); err != nil {
		return plan, nil, nil, err
	}
	return plan, nil, nil, nil
}

func (s *commandIntentService) authorizeCallbackCommand(ctx context.Context, intent *domain.CommandIntent) error {
	if isNilExecutionDependency(s.permission) {
		return apperr.Unavailable("callback.authorization_unavailable", "current callback authorization is unavailable")
	}
	if err := s.callbackIntentOwner(ctx, intent); err != nil {
		return err
	}
	allowed, err := s.permission.HasPermission(ctx, intent.Principal.ActorUID, domain.PermCallbackManage)
	if err != nil || ctx.Err() != nil {
		return apperr.Unavailable("callback.authorization_unavailable", "current callback authorization is unavailable")
	}
	if !allowed {
		return apperr.Forbidden("callback.authorization_denied", "current callback action is not authorized")
	}
	if !s.config.CallbackReplayEnabled {
		return apperr.Unavailable("callback.replay_unavailable", "callback replay admission is paused or unconfigured")
	}
	return nil
}

func (s *commandIntentService) callbackIntentOwner(ctx context.Context, intent *domain.CommandIntent) error {
	if isNilExecutionDependency(s.accountRepo) {
		return apperr.Unavailable("callback.authorization_unavailable", "current callback authorization is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, intent.Principal.TenantID, intent.AccountID)
	if err != nil || account == nil {
		return apperr.Unavailable("command.account_unavailable", "account context is unavailable")
	}
	if account.TenantID != intent.Principal.TenantID || account.CanonicalUID != intent.Principal.ActorUID || account.Status != domain.AccountActive {
		return apperr.Forbidden("callback.authorization_denied", "callback replay requires the original owner's active account")
	}
	return nil
}

func (s *commandIntentService) captureIntentPreconditions(ctx context.Context, intent *domain.CommandIntent) (domain.CommandPreconditions, error) {
	if intent.Plan.Kind == "callback_replay" {
		repo, ok := s.repo.(repository.CommandCallbackCaptureRepository)
		if !ok || isNilExecutionDependency(repo) {
			return domain.CommandPreconditions{}, apperr.Unavailable("command.planner_unavailable", "callback command planning is unavailable")
		}
		return repo.CaptureCallbackReplay(ctx, intent)
	}
	return s.repo.Capture(ctx, intent.Principal.TenantID, intent.AccountID, intent.Plan.Datasets)
}

func callbackReplayRequestedAudit(ctx context.Context, intent *domain.CommandIntent) *domain.AuditFact {
	target := intent.Plan.Callback
	fact := buildAuditFact(ctx, AuditFactInput{Source: "aero-id", SourceRegion: intent.Preconditions.HomeRegion,
		ActorUID: intent.Principal.ActorUID, TenantID: intent.Principal.TenantID, AccountID: intent.AccountID,
		Action: "callback.delivery.replayed", ResourceType: "callback_delivery", ResourceID: target.DeliveryID.String(),
		Payload: map[string]any{"intent_id": intent.ID.String(), "subscription_id": target.SubscriptionID.String(), "event_id": target.EventID.String(),
			"revision": target.Revision, "lease_generation": target.LeaseGeneration, "outcome": "queued"}})
	fact.EventID = fact.ID // Fresh durable execution identity; never the delivery ID.
	return fact
}
