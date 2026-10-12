package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// CallbackDeliveryDeferrer parks a claimed delivery after a local configuration
// failure, without treating it as a failed remote attempt. No audit fact is
// erased and pending rows remain outside dead-letter retention.
type CallbackDeliveryDeferrer interface {
	DeferDelivery(context.Context, domain.ID, string, int64, string, time.Duration) error
}

func (r *callbackRepo) DeferDelivery(ctx context.Context, id domain.ID, worker string,
	generation int64, errorCode string, delay time.Duration) error {
	if delay <= 0 {
		delay = 5 * time.Minute
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	result := r.db.WithContext(ctx).Model(&callbackDeliveryRow{}).
		Where("id = ? AND status = 'delivering' AND lease_owner = ? AND lease_generation = ? AND lease_until >= clock_timestamp()",
			id, worker, generation).
		Updates(map[string]any{
			"status": "pending", "error_code": errorCode,
			"next_attempt_at": gorm.Expr("clock_timestamp() + ? * INTERVAL '1 millisecond'", delay.Milliseconds()),
			"lease_owner":     "", "lease_until": nil, "updated_at": gorm.Expr("clock_timestamp()"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCallbackDeliveryLeaseLost
	}
	return nil
}

var _ CallbackDeliveryDeferrer = (*callbackRepo)(nil)
