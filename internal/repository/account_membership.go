package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type accountMembershipRow struct {
	ID             uuid.UUID         `gorm:"column:id;primaryKey"`
	AccountID      uuid.UUID         `gorm:"column:account_id"`
	Source         string            `gorm:"column:source"`
	SourceRegion   string            `gorm:"column:source_region"`
	ScopeType      string            `gorm:"column:scope_type"`
	ScopeID        string            `gorm:"column:scope_id"`
	SourceMemberID string            `gorm:"column:source_member_id"`
	Role           string            `gorm:"column:role"`
	Status         string            `gorm:"column:status"`
	Version        int64             `gorm:"column:version"`
	LastSyncedAt   *time.Time        `gorm:"column:last_synced_at"`
	Data           datatypes.JSONMap `gorm:"column:data"`
	CreatedAt      time.Time         `gorm:"column:created_at"`
	UpdatedAt      time.Time         `gorm:"column:updated_at"`
}

func (accountMembershipRow) TableName() string { return "account_memberships" }

func membershipRowToDomain(row *accountMembershipRow) *domain.AccountMembership {
	return &domain.AccountMembership{
		ID:             row.ID,
		AccountID:      row.AccountID,
		Source:         row.Source,
		SourceRegion:   row.SourceRegion,
		ScopeType:      row.ScopeType,
		ScopeID:        row.ScopeID,
		SourceMemberID: row.SourceMemberID,
		Role:           row.Role,
		Status:         row.Status,
		Version:        row.Version,
		LastSyncedAt:   row.LastSyncedAt,
		Data:           map[string]any(row.Data),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func membershipDomainToRow(membership *domain.AccountMembership) *accountMembershipRow {
	return &accountMembershipRow{
		ID:             membership.ID,
		AccountID:      membership.AccountID,
		Source:         membership.Source,
		SourceRegion:   membership.SourceRegion,
		ScopeType:      membership.ScopeType,
		ScopeID:        membership.ScopeID,
		SourceMemberID: membership.SourceMemberID,
		Role:           membership.Role,
		Status:         membership.Status,
		Version:        membership.Version,
		LastSyncedAt:   membership.LastSyncedAt,
		Data:           datatypes.JSONMap(membership.Data),
		CreatedAt:      membership.CreatedAt,
		UpdatedAt:      membership.UpdatedAt,
	}
}

func (r *accountRepo) ListMemberships(ctx context.Context, accountID domain.ID, page domain.Page) ([]*domain.AccountMembership, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).Where("account_id = ?", accountID).Order("id DESC")
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	var rows []accountMembershipRow
	if err := q.Limit(page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > page.Limit {
		next = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.AccountMembership, len(rows))
	for i := range rows {
		out[i] = membershipRowToDomain(&rows[i])
	}
	return out, next, nil
}

func (r *accountRepo) UpsertMembership(ctx context.Context, membership *domain.AccountMembership) error {
	now := time.Now().UTC()
	if membership.ID == (domain.ID{}) {
		membership.ID = idgen.NewID()
	}
	if membership.CreatedAt.IsZero() {
		membership.CreatedAt = now
	}
	membership.UpdatedAt = now
	row := membershipDomainToRow(membership)
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "account_id"}, {Name: "source"}, {Name: "scope_type"}, {Name: "scope_id"}, {Name: "source_member_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"source_region", "role", "status", "version", "last_synced_at", "data", "updated_at",
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "account_memberships.version <= EXCLUDED.version"}}},
	}).Create(row).Error
}
