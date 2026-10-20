package repository

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Abandonment closes only admission of an unexecuted preview. The immutable
// plan, original key and audit stay retained; an existing receipt is not canceled.
type CommandIntentAbandonment struct {
	Intent             *domain.CommandIntent
	ExpectedVersion    int64
	ConfirmationDigest string
	Region             string
	Fact               *domain.AuditFact
}

func (r *commandIntentRepo) Abandon(ctx context.Context, input CommandIntentAbandonment) (*domain.CommandIntent, error) {
	if !validCommandAbandonment(input) {
		return nil, ErrCommandIntentConflict
	}
	var result *domain.CommandIntent
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCommandDispositionTargetTx(tx, input); err != nil {
			return err
		}
		var row commandIntentRow
		if err := intentPrincipalQuery(tx, input.Intent.Principal).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND account_id = ?", input.Intent.ID, input.Intent.AccountID).Take(&row).Error; err != nil {
			return err
		}
		intent, err := commandIntentFromRow(&row)
		if err != nil {
			return err
		}
		if intent.ConfirmationDigest != input.ConfirmationDigest || intent.RequestFingerprint != input.Intent.RequestFingerprint {
			return ErrCommandIntentConflict
		}
		if intent.Status != "prepared" {
			result = intent // Execute won, or a prior abandonment already committed.
			return nil
		}
		if intent.Version != input.ExpectedVersion {
			return ErrCommandIntentConflict
		}
		result, err = r.commitCommandAbandonmentTx(ctx, tx, &row, intent, input.Fact)
		return err
	})
	return result, err
}

func (r *commandIntentRepo) commitCommandAbandonmentTx(ctx context.Context, tx *gorm.DB, row *commandIntentRow,
	intent *domain.CommandIntent, fact *domain.AuditFact) (*domain.CommandIntent, error) {
	var now time.Time
	if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return nil, err
	}
	nextVersion := row.Version + 1
	if err := tx.Model(row).Updates(map[string]any{"status": "abandoned", "version": nextVersion, "abandoned_at": now}).Error; err != nil {
		return nil, err
	}
	fact.Payload["version"], fact.Payload["status"] = nextVersion, "abandoned"
	if err := r.audit.AppendInTx(ctx, tx, fact); err != nil {
		return nil, err
	}
	intent.Status, intent.Version, intent.AbandonedAt = "abandoned", nextVersion, &now
	return intent, nil
}

func lockCommandDispositionTargetTx(tx *gorm.DB, input CommandIntentAbandonment) error {
	var directory regionDirectoryRow
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("tenant_id = ? AND account_id = ?", input.Intent.Principal.TenantID, input.Intent.AccountID).Take(&directory).Error; err != nil {
		return err
	}
	var epoch regionEpochRow
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Where("region = ?", directory.HomeRegion).Take(&epoch).Error; err != nil {
		return err
	}
	account, err := lockAccountDispositionTx(tx, input.Intent.AccountID)
	if err != nil {
		return err
	}
	if account.TenantID != input.Intent.Principal.TenantID {
		return ErrNotFound
	}
	if directory.HomeRegion != input.Region || account.HomeRegion != input.Region || directory.FencedEpoch != epoch.Epoch {
		return ErrCommandIntentDispositionFenced
	}
	return nil // Erased/suspended targets may still close an unexecuted preview.
}

func validCommandAbandonment(input CommandIntentAbandonment) bool {
	if input.Intent == nil || input.Fact == nil || input.ExpectedVersion != 1 || input.Region == "" {
		return false
	}
	return validCommandAbandonmentFact(input.Fact, input.Intent)
}

func validCommandAbandonmentFact(fact *domain.AuditFact, intent *domain.CommandIntent) bool {
	return fact.Action == "command.intent.abandoned" && fact.ResourceType == "command_intent" &&
		fact.ResourceID == intent.ID.String() && fact.AccountID == intent.AccountID &&
		fact.TenantID == intent.Principal.TenantID && fact.ActorUID == intent.Principal.ActorUID && fact.Payload != nil
}
