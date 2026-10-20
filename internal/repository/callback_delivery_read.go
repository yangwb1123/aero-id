package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

// CallbackDeliveryReader never loads payloads, remote responses, endpoints,
// worker IDs or ciphertext. Failed audit persistence returns no metadata.
type CallbackDeliveryReader interface {
	ListDeliveryMetadataWithAudit(context.Context, string, string, domain.ID,
		domain.CallbackDeliveryFilter, *domain.AuditFact) ([]*domain.CallbackDeliveryMetadata, string, error)
	GetDeliveryMetadataWithAudit(context.Context, string, string, domain.ID, domain.ID, *domain.AuditFact) (*domain.CallbackDeliveryMetadata, error)
}

const callbackDeliveryMetadataColumns = "d.id, d.subscription_id, d.event_id, s.tenant_id, d.action, d.status, d.attempt, " +
	"d.revision, d.lease_generation, d.lease_until, d.next_attempt_at, d.status_code, d.error_code, d.created_at, d.updated_at"

func (r *callbackRepo) ListDeliveryMetadataWithAudit(ctx context.Context, owner, tenant string, subscriptionID domain.ID,
	filter domain.CallbackDeliveryFilter, fact *domain.AuditFact) ([]*domain.CallbackDeliveryMetadata, string, error) {
	filter.Page.Normalize()
	items := []*domain.CallbackDeliveryMetadata{}
	next := ""
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCallbackReadScope(tx, owner, tenant, subscriptionID, fact, "callback.deliveries.read", subscriptionID); err != nil {
			return err
		}
		query := callbackDeliveryMetadataQuery(tx, owner, tenant, subscriptionID).Order("d.id DESC")
		if filter.Status != "" {
			query = query.Where("d.status = ?", filter.Status)
		}
		if filter.Page.Cursor != "" {
			query = query.Where("d.id < ?", filter.Page.Cursor)
		}
		if err := query.Limit(filter.Page.Limit + 1).Scan(&items).Error; err != nil {
			return err
		}
		if len(items) > filter.Page.Limit {
			next = items[filter.Page.Limit-1].ID.String()
			items = items[:filter.Page.Limit]
		}
		return r.audit.AppendInTx(ctx, tx, fact)
	})
	if err != nil {
		return nil, "", err
	}
	return items, next, nil
}

func (r *callbackRepo) GetDeliveryMetadataWithAudit(ctx context.Context, owner, tenant string, subscriptionID, deliveryID domain.ID,
	fact *domain.AuditFact) (*domain.CallbackDeliveryMetadata, error) {
	var result domain.CallbackDeliveryMetadata
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCallbackReadScope(tx, owner, tenant, subscriptionID, fact, "callback.delivery.read", deliveryID); err != nil {
			return err
		}
		query := callbackDeliveryMetadataQuery(tx, owner, tenant, subscriptionID).Where("d.id = ?", deliveryID).Limit(1).Scan(&result)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected != 1 {
			return ErrNotFound
		}
		return r.audit.AppendInTx(ctx, tx, fact)
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func callbackDeliveryMetadataQuery(tx *gorm.DB, owner, tenant string, subscriptionID domain.ID) *gorm.DB {
	return tx.Table("callback_deliveries AS d").Select(callbackDeliveryMetadataColumns).
		Joins("JOIN callback_subscriptions AS s ON s.id = d.subscription_id").
		Where("s.id = ? AND s.owner_uid = ? AND s.tenant_id = ?", subscriptionID, owner, tenant)
}

// Lock only the parent's ID to serialize ownership/deletion against the read
// and its fact, without decoding the subscription secret or endpoint. Worker
// delivery settlement is independent; the page is not a global snapshot.
func lockCallbackReadScope(tx *gorm.DB, owner, tenant string, subscriptionID domain.ID, fact *domain.AuditFact,
	action string, resourceID domain.ID) error {
	if err := validateCallbackReadFact(owner, tenant, subscriptionID, fact, action, resourceID); err != nil {
		return err
	}
	var row callbackSubscriptionRow
	err := tx.Model(&callbackSubscriptionRow{}).Select("id").Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("id = ? AND owner_uid = ? AND tenant_id = ?", subscriptionID, owner, tenant).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func validateCallbackReadFact(owner, tenant string, subscriptionID domain.ID, fact *domain.AuditFact,
	action string, resourceID domain.ID) error {
	if !callbackReadScopePresent(owner, tenant, subscriptionID, resourceID, fact) {
		return ErrNotFound
	}
	resourceType := "callback_subscription"
	if action == "callback.delivery.read" {
		resourceType = "callback_delivery"
	}
	if fact.ActorUID != owner || fact.TenantID != tenant || fact.Action != action || fact.ResourceID != resourceID.String() ||
		fact.Source != "aero-id" || fact.ResourceType != resourceType {
		return fmt.Errorf("callback read audit scope is invalid")
	}
	return nil
}

func callbackReadScopePresent(owner, tenant string, subscriptionID, resourceID domain.ID, fact *domain.AuditFact) bool {
	return owner != "" && tenant != "" && subscriptionID != (domain.ID{}) && resourceID != (domain.ID{}) && fact != nil
}
