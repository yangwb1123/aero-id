package repository

import (
	"context"
	"errors"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CommandCallbackCaptureRepository interface {
	CaptureCallbackReplay(context.Context, *domain.CommandIntent) (domain.CommandPreconditions, error)
}

func (r *commandIntentRepo) CaptureCallbackReplay(ctx context.Context, intent *domain.CommandIntent) (domain.CommandPreconditions, error) {
	var result domain.CommandPreconditions
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = captureIntentAccountTx(tx, intent)
		if err != nil {
			return err
		}
		return lockCallbackReplayTargetTx(tx, intent)
	})
	return result, err
}

func captureIntentAccountTx(tx *gorm.DB, intent *domain.CommandIntent) (domain.CommandPreconditions, error) {
	if intent.Plan.Kind != "callback_replay" {
		if len(intent.Plan.Datasets) == 0 {
			return domain.CommandPreconditions{}, ErrCommandIntentConflict
		}
		return captureCommandPreconditionsTx(tx, intent.Principal.TenantID, intent.AccountID, intent.Plan.Datasets)
	}
	if !intent.Plan.ValidCallbackReplayScope() {
		return domain.CommandPreconditions{}, ErrCommandIntentConflict
	}
	pre, err := captureCommandPreconditionsTx(tx, intent.Principal.TenantID, intent.AccountID, nil)
	if err != nil {
		return pre, err
	}
	// Check canonical ownership after the account lock, never trust account ID alone.
	var account struct{ ID domain.ID }
	err = tx.Table("accounts").Select("id").Where("id = ? AND tenant_id = ? AND canonical_uid = ?",
		intent.AccountID, intent.Principal.TenantID, intent.Principal.ActorUID).Take(&account).Error
	return pre, err
}

// Account -> intent (on Execute) -> subscription -> delivery, then audit.
// No payload, response, endpoint, secret or worker identity is selected.
func lockCallbackReplayTargetTx(tx *gorm.DB, intent *domain.CommandIntent) error {
	target := intent.Plan.Callback
	if target == nil {
		return ErrCommandIntentConflict
	}
	var parent struct{ ID domain.ID }
	err := tx.Table("callback_subscriptions").Select("id").Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("id = ? AND owner_uid = ? AND tenant_id = ? AND enabled AND verified",
			target.SubscriptionID, intent.Principal.ActorUID, intent.Principal.TenantID).Take(&parent).Error
	if err != nil {
		return err
	}
	var row struct{ ID domain.ID }
	err = tx.Table("callback_deliveries").Select("id").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND subscription_id = ? AND event_id = ? AND revision = ? AND lease_generation = ?",
			target.DeliveryID, target.SubscriptionID, target.EventID, target.Revision, target.LeaseGeneration).
		Where("status = 'dead_letter' AND lease_owner = '' AND lease_until IS NULL").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrCommandIntentStale
	}
	return err
}
