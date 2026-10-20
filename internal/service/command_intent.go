package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

type CommandIntentService interface {
	Create(context.Context, CommandIntentInput) (*domain.CommandIntent, error)
	Get(context.Context, domain.ID) (*domain.CommandIntent, error)
	FindByKey(context.Context, string) (*domain.CommandIntent, error)
	Execute(context.Context, domain.ID, int64, string) (*domain.CommandIntent, error)
	Abandon(context.Context, domain.ID, int64, string) (*domain.CommandIntent, error)
}

type CommandIntentInput struct {
	AccountID      domain.ID
	IdempotencyKey string
	Plan           domain.CommandPlan
}

type CommandIntentConfig struct {
	Region                string
	ExportEnabled         bool
	CallbackReplayEnabled bool
	Sources               map[string]SourceCapabilityConfig
}

type operationCommandPlanner interface {
	PrepareOperationCommand(context.Context, OperationInput) (*domain.Operation, error)
}
type syncCommandPlanner interface {
	PrepareSyncJobCommand(context.Context, SyncJobInput) (*domain.SyncJob, error)
}

type commandIntentService struct {
	repo        repository.CommandIntentRepository
	accounts    AccountService
	accountRepo repository.AccountRepository
	operations  OperationService
	syncJobs    SyncJobService
	permission  PermissionChecker
	config      CommandIntentConfig
}

func NewCommandIntentService(repo repository.CommandIntentRepository, accounts AccountService, accountRepo repository.AccountRepository,
	operations OperationService, syncJobs SyncJobService, permission PermissionChecker, cfg CommandIntentConfig) CommandIntentService {
	cfg.Sources = cloneSourceCapabilityConfig(cfg.Sources)
	return &commandIntentService{repo: repo, accounts: accounts, accountRepo: accountRepo, operations: operations,
		syncJobs: syncJobs, permission: permission, config: cfg}
}

func (s *commandIntentService) Create(ctx context.Context, input CommandIntentInput) (*domain.CommandIntent, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	principal, err := commandPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 200 {
		return nil, apperr.BadRequest("command.invalid_key", "idempotency key is required")
	}
	if input.AccountID == (domain.ID{}) {
		account, err := capabilityAccount(ctx, s.accounts)
		if err != nil {
			return nil, err
		}
		if account == nil {
			return nil, apperr.Unavailable("command.account_unavailable", "account context is unavailable")
		}
		input.AccountID = account.ID
	}
	intent, err := s.prepareIntent(ctx, principal, input)
	if err != nil {
		return nil, err
	}
	return s.persistIntent(ctx, intent)
}

func (s *commandIntentService) prepareIntent(ctx context.Context, principal domain.CommandPrincipal,
	input CommandIntentInput) (*domain.CommandIntent, error) {
	intent := &domain.CommandIntent{ID: idgen.NewID(), SchemaVersion: 1, AccountID: input.AccountID, Principal: principal,
		IdempotencyKey: input.IdempotencyKey, Version: 1, Status: "prepared", AuthorizationVersion: "unsupported", ResourceAuthorization: "unknown"}
	plan, _, _, err := s.prepareIntentCommand(ctx, intent, input.Plan)
	if err != nil {
		return nil, err
	}
	intent.Plan = plan
	intent.RequestFingerprint, err = commandFingerprint(ctx, struct {
		AccountID domain.ID
		Plan      domain.CommandPlan
	}{input.AccountID, plan})
	if err != nil {
		return nil, err
	}
	intent.Preconditions, err = s.captureIntentPreconditions(ctx, intent)
	if err != nil {
		return nil, mapCommandIntentError(err)
	}
	if intent.Preconditions.HomeRegion != s.config.Region || intent.Preconditions.AccountStatus != domain.AccountActive {
		return nil, apperr.Conflict("command.account_unavailable", "command target is not writable in this region")
	}
	intent.CreatedAt = time.Now().UTC()
	intent.ExpiresAt = intent.CreatedAt.Add(5 * time.Minute)
	intent.Destructive = commandPlanDestructive(plan)
	intent.ConfirmationDigest, err = commandConfirmationDigest(intent)
	return intent, err
}

func (s *commandIntentService) persistIntent(ctx context.Context, intent *domain.CommandIntent) (*domain.CommandIntent, error) {
	existing, err := s.repo.FindByKey(ctx, intent.Principal, intent.IdempotencyKey)
	if err == nil {
		return reuseCommandIntent(existing, intent.RequestFingerprint)
	}
	if !commandIntentNotFound(err) {
		return nil, mapCommandIntentError(err)
	}
	if err := s.repo.CreateWithAudit(ctx, intent, commandIntentAudit(ctx, intent, "command.intent.prepared")); err != nil {
		existing, findErr := s.repo.FindByKey(ctx, intent.Principal, intent.IdempotencyKey)
		if findErr == nil {
			return reuseCommandIntent(existing, intent.RequestFingerprint)
		}
		return nil, mapCommandIntentError(err)
	}
	return intent, nil
}

func (s *commandIntentService) Get(ctx context.Context, id domain.ID) (*domain.CommandIntent, error) {
	principal, err := commandPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	intent, err := s.repo.Get(ctx, principal, id)
	if err != nil {
		return nil, mapCommandIntentError(err)
	}
	return intent, nil
}

func commandPrincipal(ctx context.Context) (domain.CommandPrincipal, error) {
	identity, err := verifiedControlPlaneIdentity(ctx)
	if err != nil {
		return domain.CommandPrincipal{}, err
	}
	if len(identity.UID) > 200 || len(identity.TenantID) > 200 || len(identity.ClientID) > 200 ||
		len(identity.Issuer) > 2048 || len(identity.Subject) > 512 {
		return domain.CommandPrincipal{}, apperr.Unauthorized("auth.claims_invalid", "verified identity context is invalid")
	}
	return domain.CommandPrincipal{ActorUID: identity.UID, TenantID: identity.TenantID, Issuer: identity.Issuer,
		Subject: identity.Subject, ClientID: identity.ClientID}, nil
}

func commandIntentNotFound(err error) bool {
	return errors.Is(err, repository.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
}

func reuseCommandIntent(existing *domain.CommandIntent, fingerprint string) (*domain.CommandIntent, error) {
	if existing.RequestFingerprint != fingerprint {
		return nil, apperr.Conflict("command.idempotency_conflict", "idempotency key is bound to a different command preview")
	}
	return existing, nil
}

func mapCommandIntentError(err error) error {
	switch {
	case commandIntentNotFound(err):
		return apperr.NotFound("command.not_found", "command intent not found")
	case errors.Is(err, repository.ErrCommandIntentConflict):
		return apperr.Conflict("command.confirmation_conflict", "command confirmation or version differs")
	case errors.Is(err, repository.ErrCommandIntentStale):
		return apperr.Conflict("command.stale", "command local preconditions changed; prepare a new preview")
	case errors.Is(err, repository.ErrCommandIntentTooLarge):
		return apperr.BadRequest("command.too_large", "command intent metadata exceeds budget")
	case errors.Is(err, repository.ErrCommandIntentAbandoned):
		return apperr.Conflict("command.abandoned", "command preview was permanently abandoned; query its original disposition")
	case errors.Is(err, repository.ErrCommandIntentDispositionFenced):
		return apperr.Conflict("command.disposition_fenced", "intent disposition requires the current home-region writer")
	case errors.Is(err, repository.ErrCommandIntentExpired):
		return apperr.Conflict("command.expired", "command preview has expired")
	case errors.Is(err, repository.ErrAccountErased):
		return apperr.Forbidden("account.erased", "account is erased")
	default:
		return apperr.Unavailable("command.persistence_unknown", "command persistence outcome is unavailable; query the original intent").Wrap(err)
	}
}
