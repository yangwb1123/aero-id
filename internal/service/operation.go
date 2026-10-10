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

// OperationDrainController fences transport-triggered writes and claims during
// shutdown or a rolling deployment. Durable lease tokens protect attempts that
// race with the drain.
type OperationDrainController interface {
	SetDraining(bool)
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
	repo                repository.OperationRepository
	accounts            repository.AccountRepository
	permission          PermissionChecker
	executionPermission PermissionChecker
	audit               AuditLedgerService
	runner              OperationRunner
	region              string
	metrics             ControlPlaneMetrics
	workerGate          *operationWorkerGate
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

func WithOperationMetrics(metrics ControlPlaneMetrics) OperationOption {
	return func(s *operationService) {
		if metrics != nil {
			s.metrics = metrics
		}
	}
}

func WithOperationPermission(checker PermissionChecker) OperationOption {
	return func(s *operationService) { s.permission = checker }
}

// Execution checks must use the authority directly, never legacy local fallback.
func WithOperationExecutionPermission(checker PermissionChecker) OperationOption {
	return func(s *operationService) { s.executionPermission = checker }
}

func NewOperationService(repo repository.OperationRepository, opts ...OperationOption) OperationService {
	s := &operationService{repo: repo, metrics: noopControlPlaneMetrics{}, workerGate: newOperationWorkerGate()}
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
	if err := s.ensureOperationWorkersAccepting(); err != nil {
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
	if err := s.createWithAudit(ctx, operation); err != nil {
		existing, findErr := s.repo.FindByIdempotency(ctx, actor, operation.TenantID, operation.IdempotencyKey)
		if findErr == nil {
			return reuseOperation(existing, operation.RequestFingerprint)
		}
		if !isOperationNotFound(findErr) {
			return nil, apperr.Internal("operation.idempotency_lookup_failed", "failed to check operation idempotency").Wrap(findErr)
		}
		return nil, apperr.Internal("operation.create_failed", "failed to create operation").Wrap(err)
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
	if err := s.ensureOperationWorkersAccepting(); err != nil {
		return nil, err
	}
	operation, err := s.authorizeRun(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.runClaimed(ctx, operation, worker)
}

func (s *operationService) Reconcile(ctx context.Context, id domain.ID, worker string) (*domain.Operation, error) {
	if err := s.ensureOperationWorkersAccepting(); err != nil {
		return nil, err
	}
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
	if err := s.authorizePersistedOperation(ctx, actor, operation); err != nil {
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
