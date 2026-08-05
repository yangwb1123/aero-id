package repository

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type accountSourceRow struct {
	ID              uuid.UUID         `gorm:"column:id;primaryKey"`
	AccountID       uuid.UUID         `gorm:"column:account_id"`
	Source          string            `gorm:"column:source"`
	SourceRegion    string            `gorm:"column:source_region"`
	SourceAccountID string            `gorm:"column:source_account_id"`
	ScopeType       string            `gorm:"column:scope_type"`
	ScopeID         string            `gorm:"column:scope_id"`
	Status          string            `gorm:"column:status"`
	Version         int64             `gorm:"column:version"`
	GeneratedAt     *time.Time        `gorm:"column:generated_at"`
	LastSyncedAt    *time.Time        `gorm:"column:last_synced_at"`
	ErrorCode       string            `gorm:"column:error_code"`
	Data            datatypes.JSONMap `gorm:"column:data"`
	CreatedAt       time.Time         `gorm:"column:created_at"`
	UpdatedAt       time.Time         `gorm:"column:updated_at"`
}

// AccountReferenceReconciler marks previously projected source references and
// memberships that a complete, versioned source summary no longer returns.
// It is optional so older repository implementations can still serve sync.
type AccountReferenceReconciler interface {
	MarkMissingReferences(context.Context, domain.ID, string, []*domain.AccountSource, []*domain.AccountMembership, int64) error
}

func (accountSourceRow) TableName() string { return "account_sources" }

func sourceRowToDomain(row *accountSourceRow) *domain.AccountSource {
	return &domain.AccountSource{
		ID:              row.ID,
		AccountID:       row.AccountID,
		Source:          row.Source,
		SourceRegion:    row.SourceRegion,
		SourceAccountID: row.SourceAccountID,
		ScopeType:       row.ScopeType,
		ScopeID:         row.ScopeID,
		Status:          row.Status,
		Version:         row.Version,
		GeneratedAt:     row.GeneratedAt,
		LastSyncedAt:    row.LastSyncedAt,
		ErrorCode:       row.ErrorCode,
		Data:            map[string]any(row.Data),
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func sourceDomainToRow(source *domain.AccountSource) *accountSourceRow {
	return &accountSourceRow{
		ID:              source.ID,
		AccountID:       source.AccountID,
		Source:          source.Source,
		SourceRegion:    source.SourceRegion,
		SourceAccountID: source.SourceAccountID,
		ScopeType:       source.ScopeType,
		ScopeID:         source.ScopeID,
		Status:          source.Status,
		Version:         source.Version,
		GeneratedAt:     source.GeneratedAt,
		LastSyncedAt:    source.LastSyncedAt,
		ErrorCode:       source.ErrorCode,
		Data:            datatypes.JSONMap(source.Data),
		CreatedAt:       source.CreatedAt,
		UpdatedAt:       source.UpdatedAt,
	}
}

func (r *accountRepo) ListSources(ctx context.Context, accountID domain.ID, page domain.Page) ([]*domain.AccountSource, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).Where("account_id = ?", accountID).Order("id DESC")
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	var rows []accountSourceRow
	if err := q.Limit(page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > page.Limit {
		next = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.AccountSource, len(rows))
	for i := range rows {
		out[i] = sourceRowToDomain(&rows[i])
	}
	return out, next, nil
}

func (r *accountRepo) UpsertSource(ctx context.Context, source *domain.AccountSource) error {
	now := time.Now().UTC()
	if source.ID == (domain.ID{}) {
		source.ID = idgen.NewID()
	}
	if source.CreatedAt.IsZero() {
		source.CreatedAt = now
	}
	source.UpdatedAt = now
	row := sourceDomainToRow(source)
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "account_id"}, {Name: "source"}, {Name: "source_account_id"}, {Name: "scope_type"}, {Name: "scope_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"source_region", "status", "version", "generated_at", "last_synced_at", "error_code", "data", "updated_at",
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "account_sources.version <= EXCLUDED.version"}}},
	}).Create(row).Error
}

func (r *accountRepo) MarkMissingReferences(ctx context.Context, accountID domain.ID, source string,
	returnedSources []*domain.AccountSource, returnedMemberships []*domain.AccountMembership, version int64) error {
	if version <= 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := markMissingSources(tx, accountID, source, compactSources(returnedSources), version); err != nil {
			return err
		}
		return markMissingMemberships(tx, accountID, source, compactMemberships(returnedMemberships), version)
	})
}

func markMissingSources(tx *gorm.DB, accountID domain.ID, source string, returned []*domain.AccountSource, version int64) error {
	query := tx.Model(&accountSourceRow{}).Where("account_id = ? AND source = ? AND version <= ?", accountID, source, version)
	if len(returned) > 0 {
		query = query.Where("NOT ("+tuplePredicate(len(returned), "source_account_id", "scope_type", "scope_id")+")", sourceArgs(returned)...)
	}
	return query.Updates(map[string]any{"status": "unavailable", "version": version,
		"error_code": "source.reconciled_absent", "last_synced_at": time.Now().UTC(), "updated_at": time.Now().UTC()}).Error
}

func markMissingMemberships(tx *gorm.DB, accountID domain.ID, source string, returned []*domain.AccountMembership, version int64) error {
	query := tx.Model(&accountMembershipRow{}).Where("account_id = ? AND source = ? AND version <= ?", accountID, source, version)
	if len(returned) > 0 {
		query = query.Where("NOT ("+tuplePredicate(len(returned), "scope_type", "scope_id", "source_member_id")+")", membershipArgs(returned)...)
	}
	return query.Updates(map[string]any{"status": "unavailable", "version": version,
		"error_code": "source.reconciled_absent", "last_synced_at": time.Now().UTC(), "updated_at": time.Now().UTC()}).Error
}

func tuplePredicate(count int, fields ...string) string {
	parts := make([]string, count)
	for i := range parts {
		conditions := make([]string, len(fields))
		for j, field := range fields {
			conditions[j] = field + " = ?"
		}
		parts[i] = "(" + strings.Join(conditions, " AND ") + ")"
	}
	return strings.Join(parts, " OR ")
}

func sourceArgs(values []*domain.AccountSource) []any {
	args := make([]any, 0, len(values)*3)
	for _, value := range values {
		args = append(args, value.SourceAccountID, value.ScopeType, value.ScopeID)
	}
	return args
}

func membershipArgs(values []*domain.AccountMembership) []any {
	args := make([]any, 0, len(values)*3)
	for _, value := range values {
		args = append(args, value.ScopeType, value.ScopeID, value.SourceMemberID)
	}
	return args
}

func compactSources(values []*domain.AccountSource) []*domain.AccountSource {
	result := make([]*domain.AccountSource, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, value)
		}
	}
	return result
}

func compactMemberships(values []*domain.AccountMembership) []*domain.AccountMembership {
	result := make([]*domain.AccountMembership, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, value)
		}
	}
	return result
}
