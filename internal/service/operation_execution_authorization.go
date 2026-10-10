package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

const operationAuthorizationBudget = 3 * time.Second

// Execution uses the durable initiator, not ambient operator/worker privileges.
// No historical credentials or fabricated verified TokenIdentity are restored.
// Unlike enqueue's self-service eligibility, delayed work requires current
// authoritative action grants; ownership alone is not a renewable worker grant.
func (s *operationService) authorizeOperationExecution(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	compensation bool) error {
	ctx, cancel := context.WithTimeout(ctx, operationAuthorizationBudget)
	defer cancel()
	if err := s.executionAccount(ctx, operation); err != nil {
		return err
	}
	permissions, err := operationExecutionPermissions(operation, step, compensation)
	if err != nil {
		return err
	}
	for _, permission := range permissions {
		allowed, err := s.executionPermission.HasPermission(ctx, operation.ActorUID, permission)
		if err != nil || ctx.Err() != nil {
			return apperr.Unavailable("operation.authorization_unavailable", "current operation authorization is unavailable")
		}
		if !allowed {
			return apperr.Forbidden("operation.authorization_denied", "current operation action is not authorized")
		}
	}
	return nil
}

func (s *operationService) executionAccount(ctx context.Context, operation *domain.Operation) error {
	if !validOperationExecutionScope(operation) {
		return apperr.Forbidden("operation.execution_scope_invalid", "durable operation scope is unavailable")
	}
	if s.accounts == nil || isNilExecutionDependency(s.executionPermission) {
		return apperr.Unavailable("operation.authorization_unavailable", "current operation authorization is unavailable")
	}
	account, err := s.accounts.GetByID(ctx, operation.TenantID, operation.AccountID)
	if err != nil {
		return apperr.Unavailable("operation.authorization_unavailable", "current operation target is unavailable")
	}
	if !sameOperationExecutionAccount(account, operation) {
		return apperr.Forbidden("operation.execution_scope_invalid", "durable operation scope is unavailable")
	}
	if account.Status == domain.AccountErased {
		return apperr.Forbidden("account.erased", "account is erased")
	}
	if account.HomeRegion != s.region || operation.HomeRegion != account.HomeRegion {
		return apperr.Conflict("account.home_region_required", "account write must be routed to its home region")
	}
	return nil
}

func validOperationExecutionScope(operation *domain.Operation) bool {
	return operation != nil && operation.AccountID != (domain.ID{}) &&
		strings.TrimSpace(operation.ActorUID) != "" && strings.TrimSpace(operation.TenantID) != ""
}

func sameOperationExecutionAccount(account *domain.Account, operation *domain.Operation) bool {
	return account != nil && account.ID == operation.AccountID && account.TenantID == operation.TenantID
}

func isNilExecutionDependency(checker any) bool {
	if checker == nil {
		return true
	}
	value := reflect.ValueOf(checker)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func operationExecutionPermissions(operation *domain.Operation, step *domain.OperationStep, compensation bool) ([]string, error) {
	if !allowedOperationType(operation.OperationType) {
		return nil, apperr.BadRequest("operation.invalid_type", "durable operation type is not supported")
	}
	permissions := []string{operationPermission(operation.OperationType)}
	steps := operation.Steps
	if step != nil {
		steps = []*domain.OperationStep{step}
	}
	for _, current := range steps {
		if current == nil {
			return nil, invalidOperationDatasetScope()
		}
		actions := []string{current.Action, current.CompensationAction}
		if step != nil {
			actions = []string{current.Action}
			if compensation {
				actions = []string{current.CompensationAction}
			}
		}
		for _, action := range actions {
			if action == "" {
				continue
			}
			if err := validateExecutionStep(current, action); err != nil {
				return nil, err
			}
			permissions = appendUniquePermission(permissions, operationActionPermission(action))
		}
	}
	return permissions, nil
}

func validateExecutionStep(step *domain.OperationStep, action string) error {
	if !allowedOperationSource(step.Source) || !allowedOperationAction(action) {
		return invalidOperationDatasetScope()
	}
	copy := *step
	copy.Action = action
	_, err := operationDatasetsForStep(&copy, connector.Source(step.Source))
	return err
}

func appendUniquePermission(permissions []string, permission string) []string {
	for _, value := range permissions {
		if value == permission {
			return permissions
		}
	}
	return append(permissions, permission)
}

func (s *operationService) blockOperationExecution(ctx context.Context, operation *domain.Operation, cause error) error {
	repo, ok := s.repo.(repository.OperationExecutionBlockRepository)
	if !ok {
		return apperr.Unavailable("operation.execution_audit_unavailable", "operation execution blocking is unavailable")
	}
	var app *apperr.AppError
	if !errors.As(cause, &app) {
		return apperr.Unavailable("operation.authorization_unavailable", "current operation authorization is unavailable")
	}
	fact := buildAuditFact(ctx, AuditFactInput{Source: "aero-id", SourceRegion: operation.HomeRegion,
		OperationID: operation.ID, AccountID: operation.AccountID, TenantID: operation.TenantID, ActorUID: operation.ActorUID, RequestID: operation.RequestID,
		Action: "operation.execution.blocked", ResourceType: "operation", ResourceID: operation.ID.String(), Payload: map[string]any{"error_code": app.Code}})
	// Each blocked execution gets its own durable fact ID, never resource-wide
	// dedup. Derive its event ID from that persisted execution identifier.
	fact.EventID = idgen.Anchor("operation.execution.blocked", operation.TenantID, operation.ID.String(), app.Code, fact.ID.String())
	// Ambient worker/operator identity is not this durable initiator's scope.
	fact.TenantID = operation.TenantID
	fact.PartitionKey = operation.TenantID + ":" + operation.AccountID.String()
	persistCtx, cancel := operationPersistenceContext(ctx)
	defer cancel()
	if err := repo.BlockExecutionWithAudit(persistCtx, operation.ID, operation.Status, app.Code, app.Message, fact); err != nil {
		return apperr.Unavailable("operation.execution_block_unknown", "operation blocking outcome is unavailable; query the original receipt").Wrap(err)
	}
	return cause
}
