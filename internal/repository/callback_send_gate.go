package repository

import (
	"context"
	"errors"

	"github.com/aero/aero-id/internal/domain"
)

// CallbackDeliverySendRepository checks an existing claim at the local send
// admission point. It grants no caller permission and makes no remote call.
// A successful check cannot fence a receiver or recall an already admitted send.
type CallbackDeliverySendRepository interface {
	CheckDeliverySend(context.Context, *domain.CallbackDelivery, string) error
}

var ErrCallbackDeliverySendIneligible = errors.New("callback delivery send is ineligible")

func (r *callbackRepo) CheckDeliverySend(ctx context.Context, delivery *domain.CallbackDelivery, worker string) error {
	if !validCallbackSendClaim(delivery, worker) {
		return ErrCallbackDeliverySendIneligible
	}
	var eligible bool
	err := r.db.WithContext(ctx).Raw(`SELECT EXISTS (
		SELECT 1 FROM callback_deliveries d JOIN callback_subscriptions s ON s.id = d.subscription_id
		WHERE d.id = ? AND d.subscription_id = ? AND d.event_id = ? AND d.action = ? AND d.attempt = ?
		AND d.status = 'delivering' AND d.lease_owner = ? AND d.lease_generation = ?
		AND d.lease_until = ? AND d.lease_until > clock_timestamp()
		AND s.owner_uid = ? AND s.tenant_id = ? AND s.enabled = true AND s.verified = true
	)`, delivery.ID, delivery.SubscriptionID, delivery.EventID, delivery.Action, delivery.Attempt,
		worker, delivery.LeaseGeneration, delivery.LeaseUntil, delivery.OwnerUID, delivery.TenantID).Scan(&eligible).Error
	if err != nil {
		return err
	}
	if !eligible {
		return ErrCallbackDeliverySendIneligible
	}
	return nil
}

func validCallbackSendClaim(delivery *domain.CallbackDelivery, worker string) bool {
	if delivery == nil || worker == "" || delivery.LeaseOwner != worker || delivery.Status != "delivering" {
		return false
	}
	return validCallbackSendScope(delivery) && delivery.Attempt >= 0 && delivery.LeaseGeneration > 0 && delivery.LeaseUntil != nil
}

func validCallbackSendScope(delivery *domain.CallbackDelivery) bool {
	return delivery.ID != (domain.ID{}) && delivery.SubscriptionID != (domain.ID{}) && delivery.EventID != (domain.ID{}) &&
		delivery.Action != "" && delivery.OwnerUID != "" && delivery.TenantID != ""
}

var _ CallbackDeliverySendRepository = (*callbackRepo)(nil)
