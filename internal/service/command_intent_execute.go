package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

func (s *commandIntentService) Execute(ctx context.Context, id domain.ID, version int64, confirmation string) (*domain.CommandIntent, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	intent, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if version < 1 || confirmation != intent.ConfirmationDigest {
		return nil, apperr.Conflict("command.confirmation_conflict", "command confirmation or version differs")
	}
	if intent.Status == "abandoned" {
		return nil, mapCommandIntentError(repository.ErrCommandIntentAbandoned)
	}
	// Replan and reauthorize using only the immutable server-owned request, not
	// a client-supplied execution payload. No remote execution happens here.
	plan, operation, job, err := s.prepareIntentCommand(ctx, intent, intent.Plan)
	if err != nil {
		return nil, err
	}
	if !sameCommandPlan(plan, intent.Plan) {
		return nil, apperr.Conflict("command.plan_changed", "server command plan changed; prepare a new preview")
	}
	input := repository.CommandIntentExecution{Intent: intent, ExpectedVersion: version, ConfirmationDigest: confirmation,
		Operation: operation, Job: job, ExecutedFact: commandIntentAudit(ctx, intent, "command.intent.executed")}
	input.RequestedFact = intentRequestedFact(ctx, intent, operation, job)
	result, err := s.repo.Execute(ctx, input)
	if err != nil {
		return nil, mapCommandIntentError(err)
	}
	return result, nil
}

func intentRequestedFact(ctx context.Context, intent *domain.CommandIntent, operation *domain.Operation, job *domain.SyncJob) *domain.AuditFact {
	if intent.Plan.Kind == "callback_replay" {
		return callbackReplayRequestedAudit(ctx, intent)
	}
	if operation != nil {
		return operationRequestedAudit(ctx, operation)
	}
	if job != nil {
		return syncJobRequestedAudit(ctx, job)
	}
	return nil
}

func sameCommandPlan(a, b domain.CommandPlan) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && bytes.Equal(left, right)
}

func commandConfirmationDigest(intent *domain.CommandIntent) (string, error) {
	data, err := json.Marshal(struct {
		ID            domain.ID
		Principal     domain.CommandPrincipal
		AccountID     domain.ID
		Plan          domain.CommandPlan
		Preconditions domain.CommandPreconditions
		ExpiresAt     any
	}{intent.ID, intent.Principal, intent.AccountID, intent.Plan, intent.Preconditions, intent.ExpiresAt})
	if err != nil {
		return "", apperr.BadRequest("command.invalid_plan", "command plan cannot be confirmed")
	}
	digest := sha256.Sum256(data)
	return "v1:" + hex.EncodeToString(digest[:]), nil
}

func commandIntentAudit(ctx context.Context, intent *domain.CommandIntent, action string) *domain.AuditFact {
	eventID := intent.ID
	if action != "command.intent.prepared" {
		eventID = idgen.NewID()
	}
	return buildAuditFact(ctx, AuditFactInput{Source: "aero-id", SourceRegion: intent.Preconditions.HomeRegion,
		EventID: eventID, ActorUID: intent.Principal.ActorUID, TenantID: intent.Principal.TenantID, AccountID: intent.AccountID,
		RequestID: RequestIDFrom(ctx), Action: action, ResourceType: "command_intent", ResourceID: intent.ID.String(),
		Payload: map[string]any{"type": intent.Plan.Kind, "datasets": datasetNames(intent.Plan.Datasets), "version": intent.Version}})
}
