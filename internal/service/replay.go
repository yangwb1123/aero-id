package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type ReplayService interface {
	Create(context.Context, ReplayInput) (*ReplayResult, error)
	Get(context.Context, domain.ID) (*domain.Operation, error)
}

type ReplayInput struct {
	Mode           string
	AccountID      domain.ID
	OperationID    domain.ID
	From           *time.Time
	To             *time.Time
	Cursor         string
	Limit          int
	IdempotencyKey string
	Confirmed      bool
	Commands       []ReplayCommand
}

type ReplayCommand struct {
	Source  string
	Action  string
	Payload map[string]any
}

type ReplayResult struct {
	ReplayID   domain.ID
	Mode       string
	Status     string
	Facts      []*domain.AuditFact
	NextCursor string
	Operation  *domain.Operation
	Simulation map[string]any
}

type replayService struct {
	audit      AuditLedgerService
	operations OperationService
	accounts   repository.AccountRepository
	snapshots  repository.SnapshotRepository
	permission PermissionChecker
}

type ReplayOption func(*replayService)

func WithReplayAccountRepository(accounts repository.AccountRepository) ReplayOption {
	return func(s *replayService) { s.accounts = accounts }
}

func WithReplaySnapshotRepository(snapshots repository.SnapshotRepository) ReplayOption {
	return func(s *replayService) { s.snapshots = snapshots }
}

func WithReplayPermission(permission PermissionChecker) ReplayOption {
	return func(s *replayService) { s.permission = permission }
}

func NewReplayService(audit AuditLedgerService, operations OperationService, opts ...ReplayOption) ReplayService {
	s := &replayService{audit: audit, operations: operations}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

//nolint:cyclop // replay mode selection is a security-sensitive command matrix.
func (s *replayService) Create(ctx context.Context, input ReplayInput) (*ReplayResult, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	input.Mode = strings.ToLower(strings.TrimSpace(input.Mode))
	if input.Mode != "forensic" && input.Mode != "dry-run" && input.Mode != "controlled" {
		return nil, apperr.BadRequest("replay.invalid_mode", "replay mode is not supported")
	}
	input.Cursor = strings.TrimSpace(input.Cursor)
	if input.Mode == "controlled" && input.Cursor != "" {
		return nil, apperr.BadRequest("replay.cursor_not_allowed", "controlled replay does not accept a cursor")
	}
	if len(input.Cursor) > 512 {
		return nil, apperr.BadRequest("replay.invalid_cursor", "replay cursor is too long")
	}
	if input.Mode == "controlled" {
		return s.createControlled(ctx, actor, input)
	}
	if input.Mode == "dry-run" {
		return s.createDryRun(ctx, actor, input)
	}
	if _, _, _, err := s.authorizeScope(ctx, actor, input.AccountID, input.OperationID); err != nil {
		return nil, err
	}
	if input.AccountID == (domain.ID{}) && input.OperationID == (domain.ID{}) {
		return nil, apperr.BadRequest("replay.scope_required", "forensic replay requires an account or operation scope")
	}
	if input.Limit <= 0 || input.Limit > domain.MaxPageLimit {
		input.Limit = domain.MaxPageLimit
	}
	facts, next, err := s.audit.List(ctx, domain.AuditFactFilter{AccountID: input.AccountID, OperationID: input.OperationID,
		From: input.From, To: input.To, Page: domain.Page{Limit: input.Limit, Cursor: input.Cursor}})
	if err != nil {
		return nil, apperr.Internal("replay.query_failed", "failed to load replay facts").Wrap(err)
	}
	return &ReplayResult{ReplayID: idgen.NewID(), Mode: input.Mode, Status: "completed", Facts: facts, NextCursor: next}, nil
}

//nolint:cyclop // dry-run replay validates scope, auditability, and source availability together.
func (s *replayService) createDryRun(ctx context.Context, actor string, input ReplayInput) (*ReplayResult, error) {
	if input.AccountID == (domain.ID{}) && input.OperationID == (domain.ID{}) {
		return nil, apperr.BadRequest("replay.scope_required", "dry-run requires an account or operation scope")
	}
	accountID, state, authorized, err := s.authorizeScope(ctx, actor, input.AccountID, input.OperationID)
	if err != nil {
		return nil, err
	}
	input.AccountID = accountID
	if len(input.Commands) > 32 {
		return nil, apperr.BadRequest("replay.invalid_commands", "dry-run must contain at most thirty-two commands")
	}
	commands := make([]map[string]any, 0, len(input.Commands))
	for _, command := range input.Commands {
		if !allowedReplayCommand(command.Source, command.Action) {
			return nil, apperr.BadRequest("replay.command_not_allowed", "dry-run command is not allow-listed")
		}
		commands = append(commands, map[string]any{"source": command.Source, "action": command.Action, "would_execute": false,
			"requires_new_idempotency_key": true, "authorization_rechecked": authorized})
	}
	limit := input.Limit
	if limit <= 0 || limit > domain.MaxPageLimit {
		limit = domain.MaxPageLimit
	}
	facts, next, err := s.audit.List(ctx, domain.AuditFactFilter{AccountID: input.AccountID, OperationID: input.OperationID,
		From: input.From, To: input.To, Page: domain.Page{Limit: limit, Cursor: input.Cursor}})
	if err != nil {
		return nil, apperr.Internal("replay.query_failed", "failed to load replay facts").Wrap(err)
	}
	replayID := idgen.NewID()
	simulation := map[string]any{"mode": "dry-run", "would_execute": false, "commands": commands,
		"authorization_rechecked": authorized, "historical_credentials_reused": false, "current_state": state}
	if s.audit != nil {
		// audit-site: standalone-fact — dry-run simulation fact; fresh
		// replayID per call, one row per execution by design.
		if err := s.audit.Record(ctx, AuditFactInput{Source: "aero-id", EventID: replayID, ActorUID: actor,
			AccountID: input.AccountID, OperationID: input.OperationID, Action: "audit.replay.dry_run",
			ResourceType: "replay", ResourceID: replayID.String(), Payload: map[string]any{"command_count": len(commands)}}); err != nil {
			return nil, apperr.Internal("replay.audit_failed", "failed to record replay audit fact").Wrap(err)
		}
	}
	return &ReplayResult{ReplayID: replayID, Mode: "dry-run", Status: "completed", Facts: facts, NextCursor: next, Simulation: simulation}, nil
}

//nolint:cyclop // replay scope authorization is deliberately fail-closed across account and operation targets.
func (s *replayService) authorizeScope(ctx context.Context, actor string, accountID, operationID domain.ID) (domain.ID, map[string]any, bool, error) {
	state := map[string]any{}
	if operationID != (domain.ID{}) {
		operation, err := s.operations.Get(ctx, operationID)
		if err != nil {
			return domain.ID{}, nil, false, err
		}
		if accountID == (domain.ID{}) {
			accountID = operation.AccountID
		}
		if operation.TenantID != TenantFrom(ctx) {
			return domain.ID{}, nil, false, apperr.NotFound("replay.operation_not_found", "replay operation not found")
		}
		state["operation_status"] = operation.Status
	}
	authorized := false
	if s.permission == nil {
		return domain.ID{}, nil, false, apperr.Forbidden("replay.permission_unavailable", "replay permission is not configured")
	}
	allowed, err := s.permission.HasPermission(ctx, actor, domain.PermAuditReplay)
	if err != nil {
		return domain.ID{}, nil, false, apperr.Internal("replay.permission_check_failed", "failed to verify replay permission").Wrap(err)
	}
	if !allowed {
		return domain.ID{}, nil, false, apperr.Forbidden("replay.forbidden", "replay permission is required")
	}
	authorized = true
	if accountID != (domain.ID{}) && s.accounts != nil {
		account, err := s.accounts.GetByID(ctx, TenantFrom(ctx), accountID)
		if err != nil {
			return domain.ID{}, nil, authorized, mapAccountError(err, "replay.account_lookup_failed", "failed to resolve replay account")
		}
		if account.Status == domain.AccountErased {
			return domain.ID{}, nil, authorized, apperr.Forbidden("replay.account_erased", "replay account is erased")
		}
		state["account_id"] = account.ID.String()
		state["account_status"] = account.Status
		if s.snapshots != nil {
			snapshots, err := s.snapshots.ListSnapshots(ctx, accountID, nil)
			if err != nil {
				return domain.ID{}, nil, authorized, apperr.Internal("replay.snapshot_lookup_failed", "failed to load replay state").Wrap(err)
			}
			state["datasets"] = replayDatasetState(snapshots)
		}
	}
	return accountID, state, authorized, nil
}

func replayDatasetState(snapshots []*domain.AccountSnapshot) []map[string]any {
	result := make([]map[string]any, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot == nil {
			continue
		}
		result = append(result, map[string]any{"source": snapshot.Source, "dataset": snapshot.Dataset,
			"status": snapshot.Status, "version": snapshot.Version, "generated_at": snapshot.GeneratedAt,
			"last_synced_at": snapshot.LastSyncedAt})
	}
	return result
}

//nolint:cyclop // controlled replay performs approval, reauthorization, and operation creation as one command.
func (s *replayService) createControlled(ctx context.Context, actor string, input ReplayInput) (*ReplayResult, error) {
	if !input.Confirmed {
		return nil, apperr.BadRequest("replay.confirmation_required", "controlled replay requires explicit confirmation")
	}
	if input.AccountID == (domain.ID{}) || strings.TrimSpace(input.IdempotencyKey) == "" {
		return nil, apperr.BadRequest("replay.command_scope_required", "controlled replay requires account and idempotency key")
	}
	if len(input.Commands) == 0 || len(input.Commands) > 32 {
		return nil, apperr.BadRequest("replay.invalid_commands", "controlled replay must contain one to thirty-two commands")
	}
	steps := make([]OperationStepInput, 0, len(input.Commands))
	for i, command := range input.Commands {
		if !allowedReplayCommand(command.Source, command.Action) {
			return nil, apperr.BadRequest("replay.command_not_allowed", "controlled replay command is not allow-listed")
		}
		steps = append(steps, OperationStepInput{StepKey: replayStepKey(i), Source: command.Source, Action: command.Action, RequestPayload: command.Payload})
	}
	operation, err := s.operations.Create(ctx, OperationInput{AccountID: input.AccountID, OperationType: "replay",
		IdempotencyKey: input.IdempotencyKey, RequestPayload: map[string]any{"mode": "controlled", "actor_uid": actor}, Steps: steps})
	if err != nil {
		return nil, err
	}
	if s.audit != nil {
		// audit-site: standalone-fact — controlled-replay intent fact anchored
		// on the operation ID (retry-dedup).
		if err := s.audit.Record(ctx, AuditFactInput{Source: "aero-id", EventID: operation.ID,
			OperationID: operation.ID, ActorUID: actor, AccountID: input.AccountID, Action: "audit.replay.requested",
			ResourceType: "replay", ResourceID: operation.ID.String(), Payload: map[string]any{
				"mode": input.Mode, "step_count": len(steps),
			}}); err != nil {
			return nil, apperr.Internal("replay.audit_failed", "failed to record replay audit fact").Wrap(err)
		}
	}
	return &ReplayResult{ReplayID: operation.ID, Mode: input.Mode, Status: string(operation.Status), Operation: operation}, nil
}

func (s *replayService) Get(ctx context.Context, id domain.ID) (*domain.Operation, error) {
	return s.operations.Get(ctx, id)
}

func allowedReplayCommand(source, action string) bool {
	if source != "snaplink" && source != "aero-im" && source != "aero-vault" {
		return false
	}
	switch strings.ToLower(action) {
	case "sync", "summary", "source.summary", "erase", "delete", "source.erase":
		return true
	default:
		return false
	}
}

func replayStepKey(index int) string { return fmt.Sprintf("replay-%02d", index+1) }
