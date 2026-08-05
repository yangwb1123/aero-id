package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

type OperationService interface {
	Create(context.Context, OperationInput) (*domain.Operation, error)
	Get(context.Context, domain.ID) (*domain.Operation, error)
	List(context.Context, domain.Page) ([]*domain.Operation, string, error)
	Run(context.Context, domain.ID, string) (*domain.Operation, error)
	Reconcile(context.Context, domain.ID, string) (*domain.Operation, error)
}

type OperationInput struct {
	AccountID      domain.ID
	TenantID       string
	HomeRegion     string
	OperationType  string
	IdempotencyKey string
	RequestPayload map[string]any
	Steps          []OperationStepInput
}

type OperationStepInput struct {
	StepKey            string
	Source             string
	Action             string
	RequestPayload     map[string]any
	CompensationAction string
}

type OperationRunner interface {
	Run(context.Context, *domain.Operation, *domain.OperationStep) (string, map[string]any, error)
}

type OperationCompensationRunner interface {
	RunCompensation(context.Context, *domain.Operation, *domain.OperationStep) (string, map[string]any, error)
}

type OperationCompensationOutcomeReconciler interface {
	ReconcileCompensation(context.Context, *domain.Operation, *domain.OperationStep) (domain.OperationStepStatus, string, map[string]any, error)
}

type OperationOutcomeReconciler interface {
	Reconcile(context.Context, *domain.Operation, *domain.OperationStep) (domain.OperationStepStatus, string, map[string]any, error)
}

type operationService struct {
	repo       repository.OperationRepository
	accounts   repository.AccountRepository
	permission PermissionChecker
	audit      AuditLedgerService
	runner     OperationRunner
	region     string
}

type OperationOption func(*operationService)

func WithOperationAudit(audit AuditLedgerService) OperationOption {
	return func(s *operationService) { s.audit = audit }
}

func WithOperationRegion(region string) OperationOption {
	return func(s *operationService) {
		if strings.TrimSpace(region) != "" {
			s.region = strings.TrimSpace(region)
		}
	}
}

func WithOperationRunner(runner OperationRunner) OperationOption {
	return func(s *operationService) { s.runner = runner }
}

func WithOperationAccountRepository(accounts repository.AccountRepository) OperationOption {
	return func(s *operationService) { s.accounts = accounts }
}

func WithOperationPermission(checker PermissionChecker) OperationOption {
	return func(s *operationService) { s.permission = checker }
}

func NewOperationService(repo repository.OperationRepository, opts ...OperationOption) OperationService {
	s := &operationService{repo: repo, region: "local"}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *operationService) Create(ctx context.Context, input OperationInput) (*domain.Operation, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, err
	}
	operation, existing, err := s.prepareOperation(ctx, actor, tenantID, input)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	return s.persistCreatedOperation(ctx, actor, operation)
}

func (s *operationService) persistCreatedOperation(ctx context.Context, actor string,
	operation *domain.Operation) (*domain.Operation, error) {
	atomicAudit, err := s.createWithAudit(ctx, operation)
	if err != nil {
		if existing, findErr := s.repo.FindByIdempotency(ctx, actor, operation.TenantID, operation.IdempotencyKey); findErr == nil {
			return existing, nil
		}
		return nil, apperr.Internal("operation.create_failed", "failed to create operation").Wrap(err)
	}
	if s.audit != nil && !atomicAudit {
		if err := s.audit.Record(ctx, AuditFactInput{Source: "aero-id", SourceRegion: operation.HomeRegion,
			EventID: operation.ID, OperationID: operation.ID, RequestID: operation.RequestID, ActorUID: actor,
			AccountID: operation.AccountID, TenantID: operation.TenantID, Action: "operation.requested",
			ResourceType: "operation", ResourceID: operation.ID.String(), Payload: map[string]any{"type": operation.OperationType}}); err != nil {
			return nil, apperr.Internal("operation.audit_failed", "failed to record operation audit").Wrap(err)
		}
	}
	return operation, nil
}

func (s *operationService) Get(ctx context.Context, id domain.ID) (*domain.Operation, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, err
	}
	operation, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, apperr.NotFound("operation.not_found", "operation not found").Wrap(err)
	}
	if operation.TenantID != tenantID {
		return nil, apperr.NotFound("operation.not_found", "operation not found")
	}
	if operation.ActorUID != actor {
		if err := s.requireOperationPermission(ctx, actor, domain.PermOperationRead); err != nil {
			return nil, err
		}
	}
	return operation, nil
}

func (s *operationService) List(ctx context.Context, page domain.Page) ([]*domain.Operation, string, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, "", err
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return nil, "", err
	}
	items, next, err := s.repo.List(ctx, actor, tenantID, page)
	if err != nil {
		return nil, "", apperr.Internal("operation.list_failed", "failed to list operations").Wrap(err)
	}
	return items, next, nil
}

func (s *operationService) Run(ctx context.Context, id domain.ID, worker string) (*domain.Operation, error) {
	operation, err := s.authorizeRun(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.runClaimed(ctx, operation, worker)
}

func (s *operationService) Reconcile(ctx context.Context, id domain.ID, worker string) (*domain.Operation, error) {
	operation, err := s.authorizeRun(ctx, id)
	if err != nil {
		return nil, err
	}
	reconciler, ok := s.runner.(OperationOutcomeReconciler)
	if !ok {
		return operation, nil
	}
	if worker == "" {
		worker = "reconciler"
	}
	return s.reconcileClaimed(ctx, operation, worker, reconciler)
}

func (s *operationService) authorizeRun(ctx context.Context, id domain.ID) (*domain.Operation, error) {
	operation, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireOperationPermission(ctx, actor, domain.PermOperationManage); err != nil {
		return nil, err
	}
	if err := requireHomeRegion(ctx, s.accounts, operation.AccountID, s.region); err != nil {
		return nil, err
	}
	return operation, nil
}

func outcomeErrorCode(status domain.OperationStepStatus) string {
	if status == domain.StepFailed {
		return "operation.source_failed"
	}
	return ""
}

func outcomeErrorMessage(status domain.OperationStepStatus) string {
	if status == domain.StepFailed {
		return "source reported operation failure"
	}
	return ""
}

func normalizeOperationPayload(value map[string]any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 64*1024 {
		return nil, apperr.BadRequest("operation.payload_invalid", "operation payload is too large or invalid")
	}
	redacted := redactAuditPayload(value)
	encoded, err = json.Marshal(redacted)
	if err != nil || len(encoded) > 64*1024 {
		return nil, apperr.BadRequest("operation.payload_invalid", "operation payload is too large or invalid")
	}
	return redacted, nil
}
