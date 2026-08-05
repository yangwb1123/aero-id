package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

type EntitlementRepository interface {
	GetEntitlement(context.Context, string) (*domain.EntitlementProjection, error)
	SaveEntitlement(context.Context, *domain.EntitlementProjection) error
}

type entitlementRepo struct{ db *gorm.DB }

var ErrEntitlementRevisionConflict = errors.New("entitlement projection revision conflict")

func NewEntitlementRepository(db *gorm.DB) EntitlementRepository {
	return &entitlementRepo{db: db}
}

type entitlementRow struct {
	TenantID       string         `gorm:"column:tenant_id;primaryKey"`
	SubscriptionID string         `gorm:"column:subscription_id"`
	PlanID         string         `gorm:"column:plan_id"`
	PlanVersion    uint64         `gorm:"column:plan_version"`
	Revision       uint64         `gorm:"column:revision"`
	Active         bool           `gorm:"column:active"`
	Features       datatypes.JSON `gorm:"column:features"`
	Limits         datatypes.JSON `gorm:"column:limits"`
	EffectiveAt    time.Time      `gorm:"column:effective_at"`
	ExpiresAt      *time.Time     `gorm:"column:expires_at"`
	GeneratedAt    time.Time      `gorm:"column:generated_at"`
	FetchedAt      time.Time      `gorm:"column:fetched_at"`
}

func (entitlementRow) TableName() string { return "billing_entitlement_projections" }

func (r *entitlementRepo) GetEntitlement(ctx context.Context, tenantID string) (*domain.EntitlementProjection, error) {
	var row entitlementRow
	if err := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load entitlement projection: %w", err)
	}
	return entitlementFromRow(&row)
}

func (r *entitlementRepo) SaveEntitlement(ctx context.Context, projection *domain.EntitlementProjection) error {
	if projection == nil || projection.Validate(projection.TenantID) != nil {
		return domain.ErrInvalidEntitlement
	}
	row, err := entitlementToRow(projection)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", projection.TenantID).Error; err != nil {
			return fmt.Errorf("lock entitlement projection: %w", err)
		}
		var existing entitlementRow
		err := tx.Where("tenant_id = ?", projection.TenantID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(row).Error
		}
		if err != nil {
			return err
		}
		return saveEntitlementRevision(tx, &existing, row, projection)
	})
}

func saveEntitlementRevision(tx *gorm.DB, existing, incoming *entitlementRow,
	projection *domain.EntitlementProjection) error {
	if incoming.Revision < existing.Revision {
		return nil
	}
	if incoming.Revision > existing.Revision {
		return tx.Save(incoming).Error
	}
	stored, err := entitlementFromRow(existing)
	if err != nil {
		return err
	}
	if !sameEntitlementRevision(stored, projection) {
		return ErrEntitlementRevisionConflict
	}
	generatedAt := laterTime(existing.GeneratedAt, incoming.GeneratedAt)
	fetchedAt := laterTime(existing.FetchedAt, incoming.FetchedAt)
	return tx.Model(&entitlementRow{}).Where("tenant_id = ?", incoming.TenantID).
		Updates(map[string]any{"generated_at": generatedAt, "fetched_at": fetchedAt}).Error
}

func sameEntitlementRevision(left, right *domain.EntitlementProjection) bool {
	return left.TenantID == right.TenantID && left.SubscriptionID == right.SubscriptionID && left.Plan == right.Plan &&
		left.Revision == right.Revision && left.Active == right.Active && reflect.DeepEqual(left.Features, right.Features) &&
		reflect.DeepEqual(left.Limits, right.Limits) && samePostgresTime(left.EffectiveAt, right.EffectiveAt) &&
		samePostgresTime(left.ExpiresAt, right.ExpiresAt)
}

func samePostgresTime(left, right time.Time) bool {
	return left.UTC().Truncate(time.Microsecond).Equal(right.UTC().Truncate(time.Microsecond))
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func entitlementToRow(value *domain.EntitlementProjection) (*entitlementRow, error) {
	features, err := json.Marshal(value.Features)
	if err != nil {
		return nil, fmt.Errorf("marshal entitlement features: %w", err)
	}
	limits, err := json.Marshal(value.Limits)
	if err != nil {
		return nil, fmt.Errorf("marshal entitlement limits: %w", err)
	}
	var expires *time.Time
	if !value.ExpiresAt.IsZero() {
		copy := value.ExpiresAt
		expires = &copy
	}
	return &entitlementRow{
		TenantID: value.TenantID, SubscriptionID: value.SubscriptionID, PlanID: value.Plan.ID,
		PlanVersion: value.Plan.Version, Revision: value.Revision, Active: value.Active,
		Features: datatypes.JSON(features), Limits: datatypes.JSON(limits), EffectiveAt: value.EffectiveAt,
		ExpiresAt: expires, GeneratedAt: value.GeneratedAt, FetchedAt: value.FetchedAt,
	}, nil
}

func entitlementFromRow(row *entitlementRow) (*domain.EntitlementProjection, error) {
	projection := &domain.EntitlementProjection{
		TenantID: row.TenantID, SubscriptionID: row.SubscriptionID,
		Plan: domain.PlanRef{ID: row.PlanID, Version: row.PlanVersion}, Revision: row.Revision,
		Active: row.Active, EffectiveAt: row.EffectiveAt, GeneratedAt: row.GeneratedAt, FetchedAt: row.FetchedAt,
	}
	if row.ExpiresAt != nil {
		projection.ExpiresAt = *row.ExpiresAt
	}
	if err := json.Unmarshal(row.Features, &projection.Features); err != nil {
		return nil, fmt.Errorf("decode entitlement features: %w", err)
	}
	if err := json.Unmarshal(row.Limits, &projection.Limits); err != nil {
		return nil, fmt.Errorf("decode entitlement limits: %w", err)
	}
	if err := projection.Validate(row.TenantID); err != nil {
		return nil, err
	}
	return projection, nil
}
