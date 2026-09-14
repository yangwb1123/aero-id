package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

func (r *operationRepo) finishStep(ctx context.Context, stepID domain.ID, worker string, status domain.OperationStepStatus,
	sourceOperationID, errorCode string, result map[string]any, errorMessage string, fact *domain.AuditFact) error {
	data, err := jsonMap(result)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var step operationStepRow
		if err := tx.Where("id = ?", stepID).First(&step).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		sourceOperationID, err = durableOperationSourceID(step.SourceOperationID, sourceOperationID)
		if err != nil {
			return err
		}
		attemptKind := step.LeaseKind
		if attemptKind != "reconcile" {
			attemptKind = "forward"
		}
		// A reconcile attempt that still cannot determine the outcome keeps a
		// short lease instead of clearing it: ClaimUnknownStep only returns
		// unknown steps with an expired/nil lease, so this paces re-queries and
		// prevents a hot loop while the source stays uncertain (P6-09).
		leaseUntil := (*time.Time)(nil)
		if status == domain.StepUnknown && attemptKind == "reconcile" {
			until := now.Add(ReconcileUnknownRetryDelay)
			leaseUntil = &until
		}
		// Evaluate the lease after any row-lock wait. A late response from an
		// expired worker must not pass a predicate based on a stale application
		// timestamp and overwrite a reclaimed attempt.
		updated := tx.Model(&operationStepRow{}).Where("id = ? AND status = ? AND lease_owner = ? AND lease_until > clock_timestamp()", stepID,
			string(domain.StepRunning), worker).Updates(map[string]any{"status": string(status),
			"source_operation_id": sourceOperationID, "error_code": errorCode, "error_message": errorMessage,
			"result": data, "lease_owner": "", "lease_until": leaseUntil, "lease_kind": "", "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrOperationStepLeaseLost
		}
		finished := now
		if err := tx.Model(&operationAttemptRow{}).
			Where("step_id = ? AND attempt = ? AND attempt_kind = ? AND status = ?", stepID, step.Attempt, attemptKind, string(domain.StepRunning)).
			Updates(map[string]any{"status": string(status), "source_operation_id": sourceOperationID,
				"error_code": errorCode, "finished_at": &finished}).Error; err != nil {
			return err
		}
		return r.appendAudit(tx, ctx, fact)
	})
}

func (r *operationRepo) finishCompensationStep(ctx context.Context, stepID domain.ID, worker string, status domain.OperationStepStatus,
	sourceOperationID, errorCode string, result map[string]any, errorMessage string, fact *domain.AuditFact) error {
	data, err := jsonMap(result)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var step operationStepRow
		if err := tx.Where("id = ?", stepID).First(&step).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		sourceOperationID, err = durableOperationSourceID(step.CompensationSourceOperationID, sourceOperationID)
		if err != nil {
			return err
		}
		compensationLeaseUntil := (*time.Time)(nil)
		if status == domain.StepUnknown {
			until := now.Add(ReconcileUnknownRetryDelay)
			compensationLeaseUntil = &until
		}
		// As with forward steps, use the database clock in the fenced predicate
		// so a late compensation response cannot commit after its lease expires.
		updated := tx.Model(&operationStepRow{}).Where(
			"id = ? AND compensation_status = ? AND compensation_lease_owner = ? AND compensation_lease_until > clock_timestamp()",
			stepID, string(domain.StepRunning), worker).Updates(map[string]any{"compensation_status": string(status),
			"compensation_source_operation_id": sourceOperationID, "compensation_error_code": errorCode,
			"compensation_error_message": errorMessage, "compensation_result": data,
			"compensation_lease_owner": "", "compensation_lease_until": compensationLeaseUntil,
			"compensation_updated_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrOperationStepLeaseLost
		}
		finished := now
		if err := tx.Model(&operationAttemptRow{}).
			Where("step_id = ? AND attempt = ? AND attempt_kind = ? AND status = ?",
				stepID, step.CompensationAttempt, "compensation", string(domain.StepRunning)).
			Updates(map[string]any{"status": string(status), "source_operation_id": sourceOperationID,
				"error_code": errorCode, "finished_at": &finished}).Error; err != nil {
			return err
		}
		return r.appendAudit(tx, ctx, fact)
	})
}

//nolint:cyclop // terminal transition CAS distinguishes missing rows from stale terminal writers.
func durableOperationSourceID(existing, incoming string) (string, error) {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)
	if existing != "" && incoming != "" && existing != incoming {
		return "", ErrOperationSourceOperationConflict
	}
	if incoming == "" {
		return existing, nil
	}
	return incoming, nil
}

func (r *operationRepo) setStatus(ctx context.Context, id domain.ID, status domain.OperationStatus, code, message string,
	result map[string]any, fact *domain.AuditFact) error {
	data, err := jsonMap(result)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	updates := map[string]any{"status": string(status), "error_code": code, "error_message": message,
		"result": data, "updated_at": now, "completed_at": nil}
	if status == domain.OperationCompleted || status == domain.OperationFailed || status == domain.OperationPartiallyCompleted {
		updates["completed_at"] = now
	}
	terminal, terminalOutcome := operationStatusTransitionGuard(status)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Terminal state is a one-way boundary. Concurrent workers may both
		// compute a result from stale projections, but only the first terminal
		// transition wins; a later non-terminal write can never regress it.
		updated := tx.Model(&operationRow{}).Where("id = ? AND (status NOT IN ? OR ?)", id,
			terminal, terminalOutcome).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			var current operationRow
			if err := tx.Select("status").Where("id = ?", id).First(&current).Error; err != nil {
				return err
			}
			return ErrOperationStateConflict
		}
		return r.appendAudit(tx, ctx, fact)
	})
}

func operationStatusTransitionGuard(status domain.OperationStatus) ([]string, bool) {
	terminal := []string{string(domain.OperationUnknown), string(domain.OperationCompleted),
		string(domain.OperationFailed), string(domain.OperationPartiallyCompleted)}
	switch status {
	case domain.OperationUnknown, domain.OperationCompleted, domain.OperationFailed, domain.OperationPartiallyCompleted:
		return terminal, true
	default:
		return terminal, false
	}
}

func (r *operationRepo) appendAudit(tx *gorm.DB, ctx context.Context, fact *domain.AuditFact) error {
	if fact == nil {
		return nil
	}
	if r.audit == nil {
		return fmt.Errorf("transactional audit repository is not configured")
	}
	// ErrAuditDuplicate is swallowed in-tx (contract §4.2, event_receipt.go
	// precedent): a duplicate append implies the retried domain write already
	// committed with identical anchor inputs, which implies an identical
	// idempotent UPDATE; the retry's re-apply is a no-op-equivalent state
	// transition, so committing the transaction with the swallowed append
	// yields the same final state as the original commit. The swallow happens
	// after the domain write inside the same tx, so it can never mask a failed
	// domain write. Without it, a deterministic re-claim (ListRunnable re-claim,
	// unknown->unknown reconcile loop) would roll the whole tx back including
	// the domain write and poison the worker batch.
	if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
		return fmt.Errorf("append operation audit: %w", err)
	}
	return nil
}
