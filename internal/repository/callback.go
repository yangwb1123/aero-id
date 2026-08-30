package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type CallbackRepository interface {
	CreateSubscription(context.Context, *domain.CallbackSubscription) error
	GetSubscription(context.Context, domain.ID) (*domain.CallbackSubscription, error)
	ListSubscriptions(context.Context, string, string, domain.Page) ([]*domain.CallbackSubscription, string, error)
	DeleteSubscription(context.Context, string, string, domain.ID) error
	ListEnabledByAction(context.Context, string, string) ([]*domain.CallbackSubscription, error)
	EnqueueDelivery(context.Context, *domain.CallbackDelivery) error
	FetchDueDeliveries(context.Context, int, string, time.Duration) ([]*domain.CallbackDelivery, error)
	MarkDelivery(context.Context, domain.ID, string, int64, string, int, string, string, time.Time) error
	ResetDelivery(context.Context, domain.ID) error
	GetDelivery(context.Context, domain.ID) (*domain.CallbackDelivery, error)
	// RequeueDead moves dead_letter deliveries back to pending (attempt reset
	// to 0, lease cleared, error_code manual_replay) with a mandatory operator
	// intent: one in-tx audit fact per affected tenant (tenant derived from
	// the subscription); idempotent by the dead_letter status predicate.
	RequeueDead(context.Context, []domain.ID, int, ReplayIntent) (int, []*domain.AuditFact, error)
	// CountDeadLetterDeliveries returns exactly the number of rows the next
	// RequeueDead call would affect (bounded by ids/limit/tenant — dry-run
	// support).
	CountDeadLetterDeliveries(context.Context, []domain.ID, int, string) (int, error)
	// DeadLetterTenants lists the distinct tenants of the rows the next
	// requeue would affect (dry-run report + friendly binding pre-flight).
	DeadLetterTenants(context.Context, []domain.ID, int, string) ([]string, error)
	// PurgeDeadLetterDeliveries deletes dead_letter rows past cutoff, guarded
	// by lease-owner empty and lease expiry; bounded and keyset-ordered.
	PurgeDeadLetterDeliveries(context.Context, time.Time, int) (int64, error)
}

type callbackRepo struct {
	db    *gorm.DB
	audit AuditLedgerTransactionalRepository
}

var ErrCallbackDeliveryLeaseLost = errors.New("callback delivery lease lost")

// NewCallbackRepository wires the durable ledger fail-closed: the ledger is
// constructed unconditionally in the composition root before the callback repo,
// so production never hits the panic; a nil/typed-nil ledger is a programmer
// error and must stop startup (precedent: NewAccountRepository).
func NewCallbackRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) CallbackRepository {
	return &callbackRepo{db: db, audit: requireAuditLedger("NewCallbackRepository", audit)}
}

type callbackSubscriptionRow struct {
	ID               uuid.UUID      `gorm:"column:id;primaryKey"`
	OwnerUID         string         `gorm:"column:owner_uid"`
	TenantID         string         `gorm:"column:tenant_id"`
	Endpoint         string         `gorm:"column:endpoint"`
	EventTypes       pq.StringArray `gorm:"column:event_types;type:text[]"`
	SecretCiphertext string         `gorm:"column:secret_ciphertext"`
	Enabled          bool           `gorm:"column:enabled"`
	Verified         bool           `gorm:"column:verified"`
	CreatedAt        time.Time      `gorm:"column:created_at"`
	UpdatedAt        time.Time      `gorm:"column:updated_at"`
}

func (callbackSubscriptionRow) TableName() string { return "callback_subscriptions" }

type callbackDeliveryRow struct {
	ID              uuid.UUID               `gorm:"column:id;primaryKey"`
	SubscriptionID  uuid.UUID               `gorm:"column:subscription_id"`
	EventID         uuid.UUID               `gorm:"column:event_id"`
	Action          string                  `gorm:"column:action"`
	Payload         datatypes.JSON          `gorm:"column:payload"`
	Status          string                  `gorm:"column:status"`
	Attempt         int                     `gorm:"column:attempt"`
	LeaseGeneration int64                   `gorm:"column:lease_generation"`
	LeaseOwner      string                  `gorm:"column:lease_owner"`
	LeaseUntil      *time.Time              `gorm:"column:lease_until"`
	NextAttemptAt   time.Time               `gorm:"column:next_attempt_at"`
	StatusCode      int                     `gorm:"column:status_code"`
	ResponseBody    string                  `gorm:"column:response_body"`
	ErrorCode       string                  `gorm:"column:error_code"`
	CreatedAt       time.Time               `gorm:"column:created_at"`
	UpdatedAt       time.Time               `gorm:"column:updated_at"`
	Subscription    callbackSubscriptionRow `gorm:"foreignKey:SubscriptionID;references:ID"`
}

func (callbackDeliveryRow) TableName() string { return "callback_deliveries" }

func (r *callbackRepo) CreateSubscription(ctx context.Context, value *domain.CallbackSubscription) error {
	if value.ID == (domain.ID{}) {
		value.ID = idgen.NewID()
	}
	now := time.Now().UTC()
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	value.UpdatedAt = now
	return r.db.WithContext(ctx).Create(callbackSubscriptionToRow(value)).Error
}

func (r *callbackRepo) GetSubscription(ctx context.Context, id domain.ID) (*domain.CallbackSubscription, error) {
	var row callbackSubscriptionRow
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return callbackSubscriptionToDomain(&row), nil
}

func (r *callbackRepo) ListSubscriptions(ctx context.Context, owner, tenantID string, page domain.Page) ([]*domain.CallbackSubscription, string, error) {
	page.Normalize()
	query := r.db.WithContext(ctx).Where("owner_uid = ? AND tenant_id = ?", owner, tenantID).Order("id DESC")
	if page.Cursor != "" {
		query = query.Where("id < ?", page.Cursor)
	}
	var rows []callbackSubscriptionRow
	if err := query.Limit(page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > page.Limit {
		next = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	result := make([]*domain.CallbackSubscription, len(rows))
	for i := range rows {
		result[i] = callbackSubscriptionToDomain(&rows[i])
	}
	return result, next, nil
}

func (r *callbackRepo) DeleteSubscription(ctx context.Context, owner, tenantID string, id domain.ID) error {
	result := r.db.WithContext(ctx).Where("owner_uid = ? AND tenant_id = ? AND id = ?", owner, tenantID, id).Delete(&callbackSubscriptionRow{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *callbackRepo) ListEnabledByAction(ctx context.Context, tenantID, action string) ([]*domain.CallbackSubscription, error) {
	var rows []callbackSubscriptionRow
	query := "tenant_id = ? AND enabled = true AND verified = true AND (array_length(event_types, 1) IS NULL OR ? = ANY(event_types))"
	if err := r.db.WithContext(ctx).Where(query, tenantID, action).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]*domain.CallbackSubscription, len(rows))
	for i := range rows {
		result[i] = callbackSubscriptionToDomain(&rows[i])
	}
	return result, nil
}

func (r *callbackRepo) EnqueueDelivery(ctx context.Context, value *domain.CallbackDelivery) error {
	if value.ID == (domain.ID{}) {
		value.ID = idgen.NewID()
	}
	now := time.Now().UTC()
	if value.CreatedAt.IsZero() {
		value.CreatedAt = now
	}
	if value.NextAttemptAt.IsZero() {
		value.NextAttemptAt = now
	}
	value.UpdatedAt = now
	payload, err := json.Marshal(value.Payload)
	if err != nil {
		return err
	}
	row := &callbackDeliveryRow{ID: value.ID, SubscriptionID: value.SubscriptionID, EventID: value.EventID,
		Action: value.Action, Payload: datatypes.JSON(payload), Status: "pending", Attempt: value.Attempt,
		LeaseGeneration: value.LeaseGeneration, NextAttemptAt: value.NextAttemptAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

func (r *callbackRepo) FetchDueDeliveries(ctx context.Context, limit int, worker string, lease time.Duration) ([]*domain.CallbackDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	var rows []callbackDeliveryRow
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Where("((status = 'pending' AND next_attempt_at <= ?) OR (status = 'delivering' AND lease_until < ?))", now, now).
			Order("next_attempt_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		until := now.Add(lease)
		for i := range rows {
			if err := tx.Model(&callbackDeliveryRow{}).Where("id = ?", rows[i].ID).Updates(map[string]any{
				"status": "delivering", "lease_owner": worker,
				"lease_generation": gorm.Expr("lease_generation + 1"),
				"lease_until":      until, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			rows[i].Status = "delivering"
			rows[i].LeaseGeneration++
			rows[i].LeaseOwner = worker
			rows[i].LeaseUntil = &until
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]*domain.CallbackDelivery, len(rows))
	for i := range rows {
		var subscription callbackSubscriptionRow
		if err := r.db.WithContext(ctx).Where("id = ?", rows[i].SubscriptionID).First(&subscription).Error; err != nil {
			return nil, err
		}
		rows[i].Subscription = subscription
		result[i] = callbackDeliveryToDomain(&rows[i])
	}
	return result, nil
}

func (r *callbackRepo) MarkDelivery(ctx context.Context, id domain.ID, worker string, generation int64, status string, code int,
	response, errorCode string, nextAttempt time.Time) error {
	result := r.db.WithContext(ctx).Model(&callbackDeliveryRow{}).
		Where("id = ? AND status = 'delivering' AND lease_owner = ? AND lease_generation = ? AND lease_until >= now()", id, worker, generation).
		Updates(map[string]any{
			"status": status, "status_code": code, "response_body": response, "error_code": errorCode,
			"attempt": gorm.Expr("attempt + 1"), "next_attempt_at": nextAttempt, "lease_owner": "", "lease_until": nil, "updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrCallbackDeliveryLeaseLost
	}
	return nil
}

func (r *callbackRepo) ResetDelivery(ctx context.Context, id domain.ID) error {
	return r.db.WithContext(ctx).Model(&callbackDeliveryRow{}).Where("id = ?", id).Updates(map[string]any{
		"status": "pending", "next_attempt_at": time.Now().UTC(), "lease_owner": "", "lease_until": nil,
		"status_code": 0, "response_body": "", "error_code": "manual_replay", "updated_at": time.Now().UTC(),
	}).Error
}

func (r *callbackRepo) GetDelivery(ctx context.Context, id domain.ID) (*domain.CallbackDelivery, error) {
	var row callbackDeliveryRow
	if err := r.db.WithContext(ctx).Preload("Subscription").Where("id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return callbackDeliveryToDomain(&row), nil
}

func callbackSubscriptionToRow(value *domain.CallbackSubscription) *callbackSubscriptionRow {
	return &callbackSubscriptionRow{ID: value.ID, OwnerUID: value.OwnerUID, TenantID: value.TenantID, Endpoint: value.Endpoint,
		EventTypes: pq.StringArray(value.EventTypes), SecretCiphertext: value.SecretCiphertext, Enabled: value.Enabled,
		Verified: value.Verified, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func callbackSubscriptionToDomain(row *callbackSubscriptionRow) *domain.CallbackSubscription {
	return &domain.CallbackSubscription{ID: row.ID, OwnerUID: row.OwnerUID, TenantID: row.TenantID, Endpoint: row.Endpoint,
		EventTypes: []string(row.EventTypes), SecretCiphertext: row.SecretCiphertext, Enabled: row.Enabled, Verified: row.Verified,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func callbackDeliveryToDomain(row *callbackDeliveryRow) *domain.CallbackDelivery {
	var payload map[string]any
	_ = json.Unmarshal(row.Payload, &payload)
	return &domain.CallbackDelivery{ID: row.ID, SubscriptionID: row.SubscriptionID, EventID: row.EventID, Action: row.Action,
		TenantID: row.Subscription.TenantID, Payload: payload, Status: row.Status, Attempt: row.Attempt,
		LeaseGeneration: row.LeaseGeneration, LeaseOwner: row.LeaseOwner, LeaseUntil: row.LeaseUntil,
		NextAttemptAt: row.NextAttemptAt, StatusCode: row.StatusCode,
		ResponseBody: row.ResponseBody, ErrorCode: row.ErrorCode, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Endpoint: row.Subscription.Endpoint, SecretCiphertext: row.Subscription.SecretCiphertext, OwnerUID: row.Subscription.OwnerUID}
}
