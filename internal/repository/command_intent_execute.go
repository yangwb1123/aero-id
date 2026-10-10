package repository

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *commandIntentRepo) Execute(ctx context.Context, input CommandIntentExecution) (*domain.CommandIntent, error) {
	var result *domain.CommandIntent
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := captureCommandPreconditionsTx(tx, input.Intent.Principal.TenantID, input.Intent.AccountID, input.Intent.Plan.Datasets)
		if err != nil {
			return err
		}
		var row commandIntentRow
		if err := intentPrincipalQuery(tx, input.Intent.Principal).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", input.Intent.ID).Take(&row).Error; err != nil {
			return err
		}
		intent, err := commandIntentFromRow(&row)
		if err != nil {
			return err
		}
		if err := validateIntentExecutionTx(tx, intent, current, input); err != nil {
			return err
		}
		if intent.Status == "executed" {
			result = intent
			return nil
		}
		kind, receipt, err := r.createIntentReceiptTx(ctx, tx, input)
		if err != nil {
			return err
		}
		if err := tx.Model(&row).Updates(map[string]any{"status": "executed", "version": row.Version + 1,
			"receipt_kind": kind, "receipt_id": receipt}).Error; err != nil {
			return err
		}
		if err := r.audit.AppendInTx(ctx, tx, input.ExecutedFact); err != nil {
			return err
		}
		intent.Status, intent.Version, intent.ReceiptKind, intent.ReceiptID = "executed", row.Version+1, kind, receipt
		result = intent
		return nil
	})
	return result, err
}

func validateIntentExecutionTx(tx *gorm.DB, intent *domain.CommandIntent, current domain.CommandPreconditions,
	input CommandIntentExecution) error {
	if intent.ConfirmationDigest != input.ConfirmationDigest || intent.RequestFingerprint != input.Intent.RequestFingerprint {
		return ErrCommandIntentConflict
	}
	if intent.Status == "executed" {
		if input.ExpectedVersion != intent.Version-1 {
			return ErrCommandIntentConflict
		}
		return nil // Same confirmed execution returns its original durable receipt.
	}
	if intent.Version != input.ExpectedVersion {
		return ErrCommandIntentConflict
	}
	var now time.Time
	if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return err
	}
	if !now.Before(intent.ExpiresAt) {
		return ErrCommandIntentExpired
	}
	if !sameCommandPreconditions(current, intent.Preconditions) {
		return ErrCommandIntentStale
	}
	return nil
}

func (r *commandIntentRepo) createIntentReceiptTx(ctx context.Context, tx *gorm.DB,
	input CommandIntentExecution) (string, domain.ID, error) {
	if input.RequestedFact == nil || input.ExecutedFact == nil {
		return "", domain.ID{}, ErrCommandIntentConflict
	}
	if input.Operation != nil && input.Job == nil {
		operation := input.Operation
		if !intentReceiptScope(input.Intent, operation.AccountID, operation.TenantID, operation.ActorUID, operation.IdempotencyKey) {
			return "", domain.ID{}, ErrCommandIntentConflict
		}
		repo := &operationRepo{db: tx, audit: r.audit}
		return "operation", operation.ID, repo.create(ctx, operation, input.RequestedFact)
	}
	if input.Job != nil && input.Operation == nil {
		job := input.Job
		if !intentReceiptScope(input.Intent, job.AccountID, job.TenantID, job.ActorUID, job.IdempotencyKey) {
			return "", domain.ID{}, ErrCommandIntentConflict
		}
		repo := &syncJobRepo{db: tx, audit: r.audit}
		return "sync_job", job.ID, repo.create(ctx, job, input.RequestedFact)
	}
	return "", domain.ID{}, ErrCommandIntentConflict
}

func intentReceiptScope(intent *domain.CommandIntent, accountID domain.ID, tenant, actor, key string) bool {
	return intent.AccountID == accountID && intent.Principal.TenantID == tenant && intent.Principal.ActorUID == actor &&
		key == "command-intent:"+intent.ID.String()
}
