package repository

import (
	"context"
	"github.com/aero/aero-id/internal/domain"
	"gorm.io/gorm"
	"time"
)

func (r *commandIntentRepo) queueCallbackReplayTx(ctx context.Context, tx *gorm.DB, input CommandIntentExecution) (string, domain.ID, error) {
	intent := input.Intent
	if input.Job != nil || input.Operation != nil || !validCallbackReplayFact(intent, input.RequestedFact) {
		return "", domain.ID{}, ErrCommandIntentConflict
	}
	if err := lockCallbackReplayTargetTx(tx, intent); err != nil {
		return "", domain.ID{}, err
	}
	// Child lock waits consume TTL; recheck the database clock after them too.
	var now time.Time
	if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return "", domain.ID{}, err
	}
	if !now.Before(intent.ExpiresAt) {
		return "", domain.ID{}, ErrCommandIntentExpired
	}
	target := intent.Plan.Callback
	result := tx.Table("callback_deliveries").Where("id = ? AND revision = ? AND lease_generation = ?",
		target.DeliveryID, target.Revision, target.LeaseGeneration).
		Updates(map[string]any{"status": "pending", "next_attempt_at": now, "updated_at": now, "status_code": 0,
			"error_code": "manual_replay", "lease_generation": gorm.Expr("lease_generation + 1")})
	if result.Error != nil {
		return "", domain.ID{}, result.Error
	}
	if result.RowsAffected != 1 {
		return "", domain.ID{}, ErrCommandIntentStale
	}
	// Preserve original event/payload/attempt/diagnostics; queued is not delivered.
	if err := r.audit.AppendInTx(ctx, tx, input.RequestedFact); err != nil {
		return "", domain.ID{}, err
	}
	return "callback_replay", intent.ID, nil
}

func validCallbackReplayFact(intent *domain.CommandIntent, fact *domain.AuditFact) bool {
	if intent.Plan.Callback == nil || fact == nil {
		return false
	}
	return fact.ID != (domain.ID{}) && fact.EventID == fact.ID && callbackFactPrincipalMatches(intent, fact) &&
		fact.Source == "aero-id" && fact.Action == "callback.delivery.replayed" && fact.ResourceType == "callback_delivery" &&
		fact.ResourceID == intent.Plan.Callback.DeliveryID.String()
}

func callbackFactPrincipalMatches(intent *domain.CommandIntent, fact *domain.AuditFact) bool {
	return fact.ActorUID == intent.Principal.ActorUID && fact.TenantID == intent.Principal.TenantID && fact.AccountID == intent.AccountID
}
