package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// AuditHoldScope is the legal-hold target kind (P4-07).
type AuditHoldScope string

const (
	AuditHoldScopeTenant  AuditHoldScope = "tenant"
	AuditHoldScopeAccount AuditHoldScope = "account"
	AuditHoldScopeActor   AuditHoldScope = "actor" // legacy audit_events exemption
)

// AuditHold is one evidence-preservation order.
type AuditHold struct {
	ID        domain.ID
	TenantID  string
	ScopeType AuditHoldScope
	ScopeID   string
	Reason    string
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt *time.Time
}

// AuditHoldRepository persists legal holds.
type AuditHoldRepository interface {
	// PutHold creates or refreshes a hold (upsert on the unique triple).
	PutHold(ctx context.Context, hold *AuditHold) error
	// RemoveHold lifts an active hold.
	RemoveHold(ctx context.Context, tenantID string, scopeType AuditHoldScope, scopeID string) error
	// ListActiveHolds returns holds that are not expired.
	ListActiveHolds(ctx context.Context, tenantID string) ([]*AuditHold, error)
	// HoldScopeIDs returns the active scope ids of one kind (used by the
	// legacy cleaner to exempt actors under hold).
	HoldScopeIDs(ctx context.Context, scopeType AuditHoldScope) ([]string, error)
}

type auditHoldRow struct {
	ID        domain.ID    `gorm:"column:id;primaryKey"`
	TenantID  string       `gorm:"column:tenant_id"`
	ScopeType string       `gorm:"column:scope_type"`
	ScopeID   string       `gorm:"column:scope_id"`
	Reason    string       `gorm:"column:reason"`
	CreatedBy string       `gorm:"column:created_by"`
	CreatedAt time.Time    `gorm:"column:created_at"`
	ExpiresAt *time.Time   `gorm:"column:expires_at"`
}

func (auditHoldRow) TableName() string { return "audit_holds" }

type auditHoldRepo struct {
	db *gorm.DB
}

func NewAuditHoldRepository(db *gorm.DB) AuditHoldRepository {
	return &auditHoldRepo{db: db}
}

func (r *auditHoldRepo) PutHold(ctx context.Context, hold *AuditHold) error {
	now := time.Now().UTC()
	row := &auditHoldRow{ID: idgen.NewID(), TenantID: hold.TenantID, ScopeType: string(hold.ScopeType),
		ScopeID: hold.ScopeID, Reason: hold.Reason, CreatedBy: hold.CreatedBy, CreatedAt: now, ExpiresAt: hold.ExpiresAt}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "scope_type"}, {Name: "scope_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"reason", "created_by", "expires_at", "created_at"}),
	}).Create(row).Error
}

func (r *auditHoldRepo) RemoveHold(ctx context.Context, tenantID string, scopeType AuditHoldScope, scopeID string) error {
	return r.db.WithContext(ctx).Where("tenant_id = ? AND scope_type = ? AND scope_id = ?",
		tenantID, string(scopeType), scopeID).Delete(&auditHoldRow{}).Error
}

func (r *auditHoldRepo) ListActiveHolds(ctx context.Context, tenantID string) ([]*AuditHold, error) {
	var rows []auditHoldRow
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND (expires_at IS NULL OR expires_at > ?)", tenantID, time.Now().UTC()).
		Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*AuditHold, 0, len(rows))
	for i := range rows {
		out = append(out, auditHoldRowToDomain(&rows[i]))
	}
	return out, nil
}

func (r *auditHoldRepo) HoldScopeIDs(ctx context.Context, scopeType AuditHoldScope) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Model(&auditHoldRow{}).
		Where("scope_type = ? AND (expires_at IS NULL OR expires_at > ?)", string(scopeType), time.Now().UTC()).
		Pluck("scope_id", &ids).Error
	return ids, err
}

func auditHoldRowToDomain(row *auditHoldRow) *AuditHold {
	return &AuditHold{ID: row.ID, TenantID: row.TenantID, ScopeType: AuditHoldScope(row.ScopeType),
		ScopeID: row.ScopeID, Reason: row.Reason, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt}
}

var _ = errors.Is // keep errors import for future not-found mapping
