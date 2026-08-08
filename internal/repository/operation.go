package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type OperationRepository interface {
	Create(context.Context, *domain.Operation) error
	FindByIdempotency(context.Context, string, string, string) (*domain.Operation, error)
	Get(context.Context, domain.ID) (*domain.Operation, error)
	List(context.Context, string, string, domain.Page) ([]*domain.Operation, string, error)
	ListRunnable(context.Context, int) ([]domain.ID, error)
	ListReconcileRunnable(context.Context, int) ([]domain.ID, error)
	ClaimNextStep(context.Context, domain.ID, string, time.Duration) (*domain.OperationStep, error)
	ClaimUnknownStep(context.Context, domain.ID, string, time.Duration) (*domain.OperationStep, error)
	FinishStep(context.Context, domain.ID, string, domain.OperationStepStatus, string, string, map[string]any, string) error
	ClaimCompensationStep(context.Context, domain.ID, string, time.Duration) (*domain.OperationStep, error)
	ClaimUnknownCompensationStep(context.Context, domain.ID, string, time.Duration) (*domain.OperationStep, error)
	FinishCompensationStep(context.Context, domain.ID, string, domain.OperationStepStatus, string, string, map[string]any, string) error
	SkipPendingSteps(context.Context, domain.ID, string, string) error
	SetStatus(context.Context, domain.ID, domain.OperationStatus, string, string, map[string]any) error
}

type OperationAuditRepository interface {
	CreateWithAudit(context.Context, *domain.Operation, *domain.AuditFact) error
}

type OperationStateAuditRepository interface {
	FinishStepWithAudit(context.Context, domain.ID, string, domain.OperationStepStatus, string, string, map[string]any, string, *domain.AuditFact) error
	FinishCompensationStepWithAudit(context.Context, domain.ID, string, domain.OperationStepStatus,
		string, string, map[string]any, string, *domain.AuditFact) error
	SetStatusWithAudit(context.Context, domain.ID, domain.OperationStatus, string, string, map[string]any, *domain.AuditFact) error
}

type operationRepo struct {
	db    *gorm.DB
	audit AuditLedgerTransactionalRepository
}

var ErrOperationStepLeaseLost = errors.New("operation step lease lost")
var ErrOperationStepUnknown = errors.New("operation step outcome is unknown")

// ReconcileUnknownRetryDelay paces re-queries of steps whose outcome the source
// still cannot confirm: the step keeps a short lease so the reconciler does not
// hot-loop, while crash recovery (expired forward leases) stays prompt.
const ReconcileUnknownRetryDelay = time.Minute

func NewOperationRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) OperationRepository {
	return &operationRepo{db: db, audit: requireAuditLedger("NewOperationRepository", audit)}
}

type operationRow struct {
	ID             uuid.UUID      `gorm:"column:id;primaryKey"`
	AccountID      *uuid.UUID     `gorm:"column:account_id"`
	ActorUID       string         `gorm:"column:actor_uid"`
	TenantID       string         `gorm:"column:tenant_id"`
	RequestID      string         `gorm:"column:request_id"`
	HomeRegion     string         `gorm:"column:home_region"`
	OperationType  string         `gorm:"column:operation_type"`
	Status         string         `gorm:"column:status"`
	IdempotencyKey string         `gorm:"column:idempotency_key"`
	RequestPayload datatypes.JSON `gorm:"column:request_payload"`
	Result         datatypes.JSON `gorm:"column:result"`
	ErrorCode      string         `gorm:"column:error_code"`
	ErrorMessage   string         `gorm:"column:error_message"`
	CreatedAt      time.Time      `gorm:"column:created_at"`
	UpdatedAt      time.Time      `gorm:"column:updated_at"`
	CompletedAt    *time.Time     `gorm:"column:completed_at"`
}

func (operationRow) TableName() string { return "operations" }

type operationStepRow struct {
	ID                            uuid.UUID      `gorm:"column:id;primaryKey"`
	OperationID                   uuid.UUID      `gorm:"column:operation_id"`
	StepKey                       string         `gorm:"column:step_key"`
	Source                        string         `gorm:"column:source"`
	Action                        string         `gorm:"column:action"`
	Status                        string         `gorm:"column:status"`
	Attempt                       int            `gorm:"column:attempt"`
	LeaseOwner                    string         `gorm:"column:lease_owner"`
	LeaseUntil                    *time.Time     `gorm:"column:lease_until"`
	LeaseKind                     string         `gorm:"column:lease_kind"`
	SourceOperationID             string         `gorm:"column:source_operation_id"`
	RequestPayload                datatypes.JSON `gorm:"column:request_payload"`
	Result                        datatypes.JSON `gorm:"column:result"`
	ErrorCode                     string         `gorm:"column:error_code"`
	ErrorMessage                  string         `gorm:"column:error_message"`
	CompensationAction            string         `gorm:"column:compensation_action"`
	CompensationStatus            string         `gorm:"column:compensation_status"`
	CompensationAttempt           int            `gorm:"column:compensation_attempt"`
	CompensationLeaseOwner        string         `gorm:"column:compensation_lease_owner"`
	CompensationLeaseUntil        *time.Time     `gorm:"column:compensation_lease_until"`
	CompensationSourceOperationID string         `gorm:"column:compensation_source_operation_id"`
	CompensationResult            datatypes.JSON `gorm:"column:compensation_result"`
	CompensationErrorCode         string         `gorm:"column:compensation_error_code"`
	CompensationErrorMessage      string         `gorm:"column:compensation_error_message"`
	CompensationUpdatedAt         *time.Time     `gorm:"column:compensation_updated_at"`
	CreatedAt                     time.Time      `gorm:"column:created_at"`
	UpdatedAt                     time.Time      `gorm:"column:updated_at"`
}

func (operationStepRow) TableName() string { return "operation_steps" }

type operationAttemptRow struct {
	ID                uuid.UUID  `gorm:"column:id;primaryKey"`
	OperationID       uuid.UUID  `gorm:"column:operation_id"`
	StepID            uuid.UUID  `gorm:"column:step_id"`
	Attempt           int        `gorm:"column:attempt"`
	Status            string     `gorm:"column:status"`
	SourceOperationID string     `gorm:"column:source_operation_id"`
	ErrorCode         string     `gorm:"column:error_code"`
	StartedAt         time.Time  `gorm:"column:started_at"`
	FinishedAt        *time.Time `gorm:"column:finished_at"`
	AttemptKind       string     `gorm:"column:attempt_kind"`
}

func (operationAttemptRow) TableName() string { return "operation_attempts" }

func (r *operationRepo) Create(ctx context.Context, operation *domain.Operation) error {
	return r.create(ctx, operation, nil)
}

func (r *operationRepo) CreateWithAudit(ctx context.Context, operation *domain.Operation, fact *domain.AuditFact) error {
	if r.audit == nil {
		return fmt.Errorf("transactional audit repository is not configured")
	}
	return r.create(ctx, operation, fact)
}

//nolint:cyclop // operation creation atomically persists the workflow graph and its audit fact.
func (r *operationRepo) create(ctx context.Context, operation *domain.Operation, fact *domain.AuditFact) error {
	now := time.Now().UTC()
	if operation.ID == (domain.ID{}) {
		operation.ID = idgen.NewID()
	}
	if operation.CreatedAt.IsZero() {
		operation.CreatedAt = now
	}
	operation.UpdatedAt = now
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := operationToRow(operation)
		if err != nil {
			return err
		}
		if err := tx.Create(row).Error; err != nil {
			return fmt.Errorf("create operation: %w", err)
		}
		for _, step := range operation.Steps {
			if step.ID == (domain.ID{}) {
				step.ID = idgen.NewID()
			}
			step.OperationID = operation.ID
			if step.CreatedAt.IsZero() {
				step.CreatedAt = now
			}
			step.UpdatedAt = now
			stepRow, err := operationStepToRow(step)
			if err != nil {
				return err
			}
			if err := tx.Create(stepRow).Error; err != nil {
				return fmt.Errorf("create operation step: %w", err)
			}
		}
		if fact != nil {
			// ErrAuditDuplicate is swallowed in-tx (contract §4.2): a duplicate
			// append implies the retried operation create already committed with
			// identical anchor inputs; the swallow runs after the domain write
			// inside the same tx, so it can never mask a failed domain write.
			if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
				return fmt.Errorf("append operation audit: %w", err)
			}
		}
		return nil
	})
}

func (r *operationRepo) FindByIdempotency(ctx context.Context, actor, tenant, key string) (*domain.Operation, error) {
	var row operationRow
	if err := r.db.WithContext(ctx).Where("actor_uid = ? AND tenant_id = ? AND idempotency_key = ?", actor, tenant, key).First(&row).Error; err != nil {
		return nil, err
	}
	return operationRowToDomain(&row)
}

func (r *operationRepo) Get(ctx context.Context, id domain.ID) (*domain.Operation, error) {
	var row operationRow
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	var steps []operationStepRow
	if err := r.db.WithContext(ctx).Where("operation_id = ?", id).Order("created_at ASC, id ASC").Find(&steps).Error; err != nil {
		return nil, err
	}
	result, err := operationRowToDomain(&row)
	if err != nil {
		return nil, err
	}
	result.Steps = make([]*domain.OperationStep, len(steps))
	for i := range steps {
		result.Steps[i], err = operationStepRowToDomain(&steps[i])
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (r *operationRepo) List(ctx context.Context, actor, tenant string, page domain.Page) ([]*domain.Operation, string, error) {
	page.Normalize()
	query := r.db.WithContext(ctx).Where("actor_uid = ? AND tenant_id = ?", actor, tenant).Order("id DESC")
	if page.Cursor != "" {
		query = query.Where("id < ?", page.Cursor)
	}
	var rows []operationRow
	if err := query.Limit(page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > page.Limit {
		next = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	result := make([]*domain.Operation, len(rows))
	for i := range rows {
		converted, err := operationRowToDomain(&rows[i])
		if err != nil {
			return nil, "", err
		}
		result[i] = converted
	}
	return result, next, nil
}

func (r *operationRepo) ClaimNextStep(ctx context.Context, operationID domain.ID, worker string, lease time.Duration) (*domain.OperationStep, error) {
	var result *domain.OperationStep
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		var row operationStepRow
		query := tx.Where("operation_steps.operation_id = ? AND operation_steps.status = ?", operationID, string(domain.StepPending))
		query = query.Where(`NOT EXISTS (
			SELECT 1 FROM operation_steps previous_step
			WHERE previous_step.operation_id = operation_steps.operation_id
			  AND (previous_step.created_at < operation_steps.created_at OR
			       (previous_step.created_at = operation_steps.created_at AND previous_step.id < operation_steps.id))
			  AND previous_step.status IN ('pending', 'running', 'unknown', 'failed')
		)`)
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Order("created_at ASC, id ASC").First(&row)
		if query.Error == nil {
			return r.claimForwardStep(tx, &row, worker, lease, now, &result)
		}
		if !errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return query.Error
		}
		var stale operationStepRow
		staleQuery := tx.Where("operation_steps.operation_id = ? AND operation_steps.status = ? AND "+
			"operation_steps.lease_kind = ? AND operation_steps.lease_until < ?",
			operationID, string(domain.StepRunning), "forward", now).
			Where(`NOT EXISTS (
				SELECT 1 FROM operation_steps previous_step
				WHERE previous_step.operation_id = operation_steps.operation_id
				  AND (previous_step.created_at < operation_steps.created_at OR
				       (previous_step.created_at = operation_steps.created_at AND previous_step.id < operation_steps.id))
			  AND previous_step.status IN ('pending', 'running', 'unknown', 'failed')
			)`)
		staleQuery = staleQuery.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Order("lease_until ASC, created_at ASC, id ASC").First(&stale)
		if errors.Is(staleQuery.Error, gorm.ErrRecordNotFound) {
			return staleQuery.Error
		}
		if staleQuery.Error != nil {
			return staleQuery.Error
		}
		if err := markUnknownForwardStep(tx, &stale, now); err != nil {
			return err
		}
		return ErrOperationStepUnknown
	})
	return result, err
}

func (r *operationRepo) ClaimUnknownStep(ctx context.Context, operationID domain.ID, worker string, lease time.Duration) (*domain.OperationStep, error) {
	var result *domain.OperationStep
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		var row operationStepRow
		query := tx.Where("operation_id = ? AND ((status = ? AND (lease_until IS NULL OR lease_until < ?)) OR "+
			"(status = ? AND lease_kind = ? AND lease_until < ?))",
			operationID, string(domain.StepUnknown), now, string(domain.StepRunning), "reconcile", now).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Order("created_at ASC, id ASC").First(&row)
		if query.Error != nil {
			return query.Error
		}
		return r.claimReconcileStep(tx, &row, worker, lease, now, &result)
	})
	return result, err
}

func (r *operationRepo) ClaimCompensationStep(ctx context.Context, operationID domain.ID, worker string, lease time.Duration) (*domain.OperationStep, error) {
	return r.claimCompensationStep(ctx, operationID, worker, lease, false)
}

func (r *operationRepo) ClaimUnknownCompensationStep(ctx context.Context, operationID domain.ID, worker string,
	lease time.Duration) (*domain.OperationStep, error) {
	return r.claimCompensationStep(ctx, operationID, worker, lease, true)
}

func (r *operationRepo) claimCompensationStep(ctx context.Context, operationID domain.ID, worker string,
	lease time.Duration, unknown bool) (*domain.OperationStep, error) {
	var result *domain.OperationStep
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		var row operationStepRow
		query := tx.Where("operation_id = ? AND status = ? AND compensation_action <> ''", operationID, string(domain.StepSucceeded))
		if unknown {
			condition := "((compensation_status = ? AND (compensation_lease_until IS NULL OR " +
				"compensation_lease_until < ?)) OR (compensation_status = ? AND compensation_lease_until < ?))"
			query = query.Where(condition,
				string(domain.StepUnknown), now, string(domain.StepRunning), now)
		} else {
			condition := "((compensation_status = ? AND (compensation_lease_until IS NULL OR " +
				"compensation_lease_until < ?)) OR (compensation_status = ? AND compensation_lease_until < ?))"
			query = query.Where(condition,
				string(domain.StepPending), now, string(domain.StepRunning), now)
		}
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Order("created_at DESC, id DESC")
		query = query.First(&row)
		if query.Error != nil {
			return query.Error
		}
		until := now.Add(lease)
		if err := tx.Model(&row).Updates(map[string]any{"compensation_status": string(domain.StepRunning),
			"compensation_attempt": gorm.Expr("compensation_attempt + 1"), "compensation_lease_owner": worker,
			"compensation_lease_until": until, "compensation_updated_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		row.CompensationStatus = string(domain.StepRunning)
		row.CompensationAttempt++
		row.CompensationLeaseOwner = worker
		row.CompensationLeaseUntil = &until
		row.CompensationUpdatedAt = &now
		attempt := &operationAttemptRow{ID: idgen.NewID(), OperationID: row.OperationID, StepID: row.ID,
			Attempt: row.CompensationAttempt, AttemptKind: "compensation", Status: string(domain.StepRunning), StartedAt: now}
		if err := tx.Create(attempt).Error; err != nil {
			return fmt.Errorf("create compensation attempt: %w", err)
		}
		var conversionErr error
		result, conversionErr = operationStepRowToDomain(&row)
		return conversionErr
	})
	return result, err
}

func (r *operationRepo) claimForwardStep(tx *gorm.DB, row *operationStepRow, worker string, lease time.Duration,
	now time.Time, result **domain.OperationStep) error {
	until := now.Add(lease)
	if err := tx.Model(row).Updates(map[string]any{"status": string(domain.StepRunning), "attempt": gorm.Expr("attempt + 1"),
		"lease_owner": worker, "lease_until": until, "lease_kind": "forward", "updated_at": now}).Error; err != nil {
		return err
	}
	row.Status, row.Attempt, row.LeaseOwner, row.LeaseUntil, row.LeaseKind = string(domain.StepRunning), row.Attempt+1, worker, &until, "forward"
	if err := tx.Create(&operationAttemptRow{ID: idgen.NewID(), OperationID: row.OperationID, StepID: row.ID,
		Attempt: row.Attempt, AttemptKind: "forward", Status: string(domain.StepRunning), StartedAt: now}).Error; err != nil {
		return fmt.Errorf("create operation attempt: %w", err)
	}
	converted, err := operationStepRowToDomain(row)
	*result = converted
	return err
}

func (r *operationRepo) claimReconcileStep(tx *gorm.DB, row *operationStepRow, worker string, lease time.Duration,
	now time.Time, result **domain.OperationStep) error {
	until := now.Add(lease)
	if err := tx.Model(row).Updates(map[string]any{"status": string(domain.StepRunning), "attempt": gorm.Expr("attempt + 1"),
		"lease_owner": worker, "lease_until": until, "lease_kind": "reconcile", "updated_at": now}).Error; err != nil {
		return err
	}
	row.Status, row.Attempt, row.LeaseOwner, row.LeaseUntil, row.LeaseKind = string(domain.StepRunning), row.Attempt+1, worker, &until, "reconcile"
	if err := tx.Create(&operationAttemptRow{ID: idgen.NewID(), OperationID: row.OperationID, StepID: row.ID,
		Attempt: row.Attempt, AttemptKind: "reconcile", Status: string(domain.StepRunning), StartedAt: now}).Error; err != nil {
		return fmt.Errorf("create operation reconciliation attempt: %w", err)
	}
	converted, err := operationStepRowToDomain(row)
	*result = converted
	return err
}

func markUnknownForwardStep(tx *gorm.DB, row *operationStepRow, now time.Time) error {
	updates := map[string]any{"status": string(domain.StepUnknown), "error_code": "operation.unknown_outcome",
		"error_message": "worker lease expired before the source outcome was recorded", "lease_owner": "",
		"lease_until": nil, "lease_kind": "", "updated_at": now}
	if err := tx.Model(row).Updates(updates).Error; err != nil {
		return err
	}
	return tx.Model(&operationAttemptRow{}).Where("step_id = ? AND attempt = ? AND attempt_kind = ? AND status = ?",
		row.ID, row.Attempt, "forward", string(domain.StepRunning)).Updates(map[string]any{
		"status": string(domain.StepUnknown), "error_code": "operation.unknown_outcome", "finished_at": &now,
	}).Error
}

func (r *operationRepo) FinishStep(ctx context.Context, stepID domain.ID, worker string, status domain.OperationStepStatus,
	sourceOperationID, errorCode string, result map[string]any, errorMessage string) error {
	return r.finishStep(ctx, stepID, worker, status, sourceOperationID, errorCode, result, errorMessage, nil)
}

func (r *operationRepo) FinishStepWithAudit(ctx context.Context, stepID domain.ID, worker string, status domain.OperationStepStatus,
	sourceOperationID, errorCode string, result map[string]any, errorMessage string, fact *domain.AuditFact) error {
	if r.audit == nil {
		return fmt.Errorf("transactional audit repository is not configured")
	}
	return r.finishStep(ctx, stepID, worker, status, sourceOperationID, errorCode, result, errorMessage, fact)
}

func (r *operationRepo) FinishCompensationStep(ctx context.Context, stepID domain.ID, worker string, status domain.OperationStepStatus,
	sourceOperationID, errorCode string, result map[string]any, errorMessage string) error {
	return r.finishCompensationStep(ctx, stepID, worker, status, sourceOperationID, errorCode, result, errorMessage, nil)
}

func (r *operationRepo) FinishCompensationStepWithAudit(ctx context.Context, stepID domain.ID, worker string, status domain.OperationStepStatus,
	sourceOperationID, errorCode string, result map[string]any, errorMessage string, fact *domain.AuditFact) error {
	if r.audit == nil {
		return fmt.Errorf("transactional audit repository is not configured")
	}
	return r.finishCompensationStep(ctx, stepID, worker, status, sourceOperationID, errorCode, result, errorMessage, fact)
}

func (r *operationRepo) SkipPendingSteps(ctx context.Context, operationID domain.ID, code, message string) error {
	return r.db.WithContext(ctx).Model(&operationStepRow{}).Where("operation_id = ? AND status = ?", operationID,
		string(domain.StepPending)).Updates(map[string]any{"status": string(domain.StepSkipped), "error_code": code,
		"error_message": message, "updated_at": time.Now().UTC()}).Error
}

func (r *operationRepo) SetStatus(ctx context.Context, id domain.ID, status domain.OperationStatus, code, message string, result map[string]any) error {
	return r.setStatus(ctx, id, status, code, message, result, nil)
}

func (r *operationRepo) SetStatusWithAudit(ctx context.Context, id domain.ID, status domain.OperationStatus, code, message string,
	result map[string]any, fact *domain.AuditFact) error {
	if r.audit == nil {
		return fmt.Errorf("transactional audit repository is not configured")
	}
	return r.setStatus(ctx, id, status, code, message, result, fact)
}
