package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// CallbackAuditRepository is the in-transaction audit surface for callback
// administration. Each method appends the fact in the SAME transaction as the
// domain write: an append failure rolls the whole mutation back (never an
// orphaned ledger/publication row, never a mutation without its fact). The
// concrete callback repo implements it; the service type-asserts (pattern:
// AccountProfileAuditRepository / OperationStateAuditRepository).
type CallbackAuditRepository interface {
	CreateSubscriptionWithAudit(ctx context.Context, value *domain.CallbackSubscription, fact *domain.AuditFact) error
	DeleteSubscriptionWithAudit(ctx context.Context, owner, tenantID string, id domain.ID, fact *domain.AuditFact) error
	ResetDeliveryWithAudit(ctx context.Context, id domain.ID, fact *domain.AuditFact) error
}

// CreateSubscriptionWithAudit inserts the subscription and appends the audit
// fact atomically. The service pre-assigns the subscription ID (ID-when-empty
// is preserved for plain callers); the fact must be freshly built per call —
// appendInTx mutates it in place.
func (r *callbackRepo) CreateSubscriptionWithAudit(ctx context.Context, value *domain.CallbackSubscription, fact *domain.AuditFact) error {
	if value.ID == (domain.ID{}) {
		value.ID = idgen.NewID()
	}
	now := time.Now().UTC()
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	value.UpdatedAt = now
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(callbackSubscriptionToRow(value)).Error; err != nil {
			return fmt.Errorf("create callback subscription: %w", err)
		}
		// ErrAuditDuplicate is swallowed in-tx (contract §4.2, defensive parity):
		// a duplicate append implies the retried domain write already committed
		// with identical anchor inputs, so the retry is a no-op-equivalent
		// re-apply; the swallow runs after the domain write inside the same tx,
		// so it can never mask a failed domain write.
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append callback audit: %w", err)
		}
		return nil
	})
}

// DeleteSubscriptionWithAudit deletes the subscription (cascading to its
// deliveries) and appends the fact only when a row was actually deleted —
// mirroring the ErrNotFound short-circuit of the plain path, so a phantom
// delete never leaves an orphan fact.
func (r *callbackRepo) DeleteSubscriptionWithAudit(ctx context.Context, owner, tenantID string, id domain.ID, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("owner_uid = ? AND tenant_id = ? AND id = ?", owner, tenantID, id).Delete(&callbackSubscriptionRow{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		// ErrAuditDuplicate is swallowed in-tx (contract §4.2, defensive parity):
		// a duplicate append implies the retried delete already committed with
		// identical anchor inputs; the swallow runs after the domain write inside
		// the same tx, so it can never mask a failed domain write.
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append callback audit: %w", err)
		}
		return nil
	})
}

// ResetDeliveryWithAudit re-arms a delivery (status pending, lease cleared,
// error_code manual_replay) and appends the fact only when the reset UPDATE
// affected a row — the service's GetDelivery pre-check makes the phantom case
// unreachable, but the gate prevents an orphan fact if a delivery is deleted
// between check and reset.
func (r *callbackRepo) ResetDeliveryWithAudit(ctx context.Context, id domain.ID, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&callbackDeliveryRow{}).Where("id = ?", id).Updates(map[string]any{
			"status": "pending", "next_attempt_at": time.Now().UTC(), "lease_owner": "", "lease_until": nil,
			"status_code": 0, "response_body": "", "error_code": "manual_replay", "updated_at": time.Now().UTC(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		// ErrAuditDuplicate is swallowed in-tx (contract §4.2, defensive parity):
		// a duplicate append implies the retried reset already committed with
		// identical anchor inputs; the swallow runs after the domain write inside
		// the same tx, so it can never mask a failed domain write.
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append callback audit: %w", err)
		}
		return nil
	})
}

// RequeueDead recovers operator-selected dead-lettered callback deliveries
// (B2-5 REPO-2). Semantics mirror RequeuePublication: status-scoped selection
// ('dead_letter' only), bounded (exact ids or oldest limit by
// (next_attempt_at, id)), attempt RESET to 0 (a full fresh retry budget — the
// behavioral difference from ResetDeliveryWithAudit, which preserves attempt),
// lease cleared, error_code manual_replay, one in-tx audit fact per affected
// tenant (tenant via the callback_subscriptions join; fact-mandatory and
// binding-checked exactly as the publication leg). Idempotent by the
// dead_letter status predicate.
func (r *callbackRepo) RequeueDead(ctx context.Context, ids []domain.ID, limit int, intent ReplayIntent) (int, []*domain.AuditFact, error) {
	if err := validateReplayIntent(intent); err != nil {
		return 0, nil, err
	}
	if limit <= 0 {
		limit = replayDefaultLimit
	}
	requeued := 0
	facts := []*domain.AuditFact{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		requeued, facts, err = requeueDeliveryRows(ctx, tx, r.audit.AppendInTx, ids, limit, intent)
		return err
	})
	if err != nil {
		return 0, nil, err
	}
	return requeued, facts, nil
}

// requeueDeliveryRows is the delivery-leg transaction body (twin of
// requeuePublicationRows; tenant via the subscription join, causal anchor the
// parent subscription).
func requeueDeliveryRows(ctx context.Context, tx *gorm.DB, appendInTx func(context.Context, *gorm.DB, *domain.AuditFact) error,
	ids []domain.ID, limit int, intent ReplayIntent) (int, []*domain.AuditFact, error) {
	locked, err := selectDeadLetterDeliveryIDs(tx, ids, limit, intent.TenantFilter, true)
	if err != nil {
		return 0, nil, err
	}
	rows, err := deliveryRequeueRows(tx, locked)
	if err != nil {
		return 0, nil, err
	}
	if err := checkReplayTenants(rows, intent.AllowedTenants); err != nil {
		return 0, nil, err
	}
	requeued, err := updateRequeuedRows(tx, &callbackDeliveryRow{}, locked)
	if err != nil {
		return 0, nil, err
	}
	if requeued != len(locked) {
		return 0, nil, fmt.Errorf("audit replay: requeue affected %d rows but locked selection is %d (programming error)",
			requeued, len(locked))
	}
	if requeued == 0 {
		return 0, []*domain.AuditFact{}, nil
	}
	facts, err := appendRequeueFacts(ctx, appendInTx, tx, intent, rows, replayScope(ids, limit), "delivery_ids",
		func(row requeueRow) domain.ID { return row.FactID })
	if err != nil {
		return 0, nil, err
	}
	return requeued, facts, nil
}

// deadLetterDeliverySelection is the callback-delivery twin of
// deadLetterPublicationSelection: same status predicate, bounds, and ordering;
// the tenant filter narrows via the subscription's tenant using EXISTS (the
// locked statement joins nothing, so FOR UPDATE never locks subscription
// rows).
func deadLetterDeliverySelection(tx *gorm.DB, ids []domain.ID, limit int, tenantFilter string, forUpdate bool) *gorm.DB {
	query := tx.Model(&callbackDeliveryRow{}).Where("status = 'dead_letter'")
	if tenantFilter != "" {
		query = query.Where("EXISTS (SELECT 1 FROM callback_subscriptions s "+
			"WHERE s.id = callback_deliveries.subscription_id AND s.tenant_id = ?)", tenantFilter)
	}
	if len(ids) > 0 {
		query = query.Where("id IN ?", ids)
	} else {
		query = query.Order("next_attempt_at ASC, id ASC").Limit(limit)
	}
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	return query.Select("id")
}

func selectDeadLetterDeliveryIDs(tx *gorm.DB, ids []domain.ID, limit int, tenantFilter string, forUpdate bool) ([]domain.ID, error) {
	var rows []callbackDeliveryRow
	if err := deadLetterDeliverySelection(tx, ids, limit, tenantFilter, forUpdate).Find(&rows).Error; err != nil {
		return nil, err
	}
	rowIDs := make([]domain.ID, 0, len(rows))
	for _, row := range rows {
		rowIDs = append(rowIDs, domain.ID(row.ID))
	}
	return rowIDs, nil
}

// deliveryRequeueRows decorates the locked selection with the subscription
// tenant and the prior values the fact payload is built from (the delivery
// leg's causal anchor is the parent subscription; deliveries have no ledger
// fact of their own).
func deliveryRequeueRows(tx *gorm.DB, rowIDs []domain.ID) ([]requeueRow, error) {
	if len(rowIDs) == 0 {
		return nil, nil
	}
	var rows []struct {
		ID        uuid.UUID `gorm:"column:id"`
		FactID    uuid.UUID `gorm:"column:fact_id"`
		TenantID  string    `gorm:"column:tenant_id"`
		ErrorCode string    `gorm:"column:error_code"`
		Attempt   int       `gorm:"column:attempt"`
	}
	if err := tx.Table("callback_deliveries d").
		Select("d.id, d.error_code, d.attempt, d.subscription_id AS fact_id, s.tenant_id").
		Joins("JOIN callback_subscriptions s ON s.id = d.subscription_id").
		Where("d.id IN ?", rowIDs).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]requeueRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, requeueRow{ID: domain.ID(row.ID), FactID: domain.ID(row.FactID),
			TenantID: row.TenantID, ErrorCode: row.ErrorCode, Attempt: row.Attempt})
	}
	return out, nil
}

// CountDeadLetterDeliveries returns exactly the number of rows the next
// RequeueDead call would affect (bounded by the same selection core — dry-run
// support).
func (r *callbackRepo) CountDeadLetterDeliveries(ctx context.Context, ids []domain.ID, limit int, tenantFilter string) (int, error) {
	if limit <= 0 {
		limit = replayDefaultLimit
	}
	sub := deadLetterDeliverySelection(r.db.WithContext(ctx), ids, limit, tenantFilter, false)
	var count int64
	if err := r.db.WithContext(ctx).Table("(?) AS s", sub).Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// DeadLetterTenants returns the distinct tenants of the rows the next
// RequeueDead call would affect (dry-run tenant report and friendly binding
// pre-flight; the authoritative check stays in-tx).
func (r *callbackRepo) DeadLetterTenants(ctx context.Context, ids []domain.ID, limit int, tenantFilter string) ([]string, error) {
	if limit <= 0 {
		limit = replayDefaultLimit
	}
	sub := deadLetterDeliverySelection(r.db.WithContext(ctx), ids, limit, tenantFilter, false)
	var tenants []string
	err := r.db.WithContext(ctx).Table("callback_deliveries d").
		Select("DISTINCT s.tenant_id").
		Joins("JOIN callback_subscriptions s ON s.id = d.subscription_id").
		Where("d.id IN (?)", sub).
		Order("s.tenant_id ASC").
		Scan(&tenants).Error
	if err != nil {
		return nil, err
	}
	return tenants, nil
}

// PurgeDeadLetterDeliveries deletes dead_letter deliveries past the retention
// cutoff (B2-5 WORKER-1), excluding leased rows, bounded, ordered by
// (updated_at, id) so the cutoff is an index range bound on
// idx_callback_deliveries_purge, with the outer status qual re-checked by EPQ
// against the new row version (a concurrently requeued row survives) and
// FOR UPDATE SKIP LOCKED in the subquery so purge never blocks on an
// in-flight requeue.
func (r *callbackRepo) PurgeDeadLetterDeliveries(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		limit = purgeDefaultLimit
	}
	sub := r.db.WithContext(ctx).Model(&callbackDeliveryRow{}).
		Where("status = 'dead_letter' AND lease_owner = '' AND (lease_until IS NULL OR lease_until < now()) AND updated_at < ?", cutoff).
		Order("updated_at ASC, id ASC").
		Select("id").
		Limit(limit).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked})
	result := r.db.WithContext(ctx).Where("id IN (?) AND status = 'dead_letter'", sub).Delete(&callbackDeliveryRow{})
	if result.Error != nil {
		return 0, fmt.Errorf("purge dead-letter deliveries: %w", result.Error)
	}
	return result.RowsAffected, nil
}
