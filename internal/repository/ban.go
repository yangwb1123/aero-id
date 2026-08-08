package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

// BanRepository provides CRUD and query operations on the user_bans table.
// IsBanned checks whether a specific uid is currently banned (deleted_at IS
// NULL AND (expires_at IS NULL OR expires_at > now())). All write methods
// are idempotent.
type BanRepository interface {
	// Ban creates or re-activates a ban for uid. Idempotent: a second call
	// with the same uid overwrites the existing row (upsert).
	Ban(ctx context.Context, uid, actorUID, reason string, expiresAt *time.Time) error
	// Unban soft-deletes the ban row for uid. Idempotent: safe to call on a
	// uid that is not currently banned (no-op).
	Unban(ctx context.Context, uid string) error
	// IsBanned reports whether uid is under an active (non-expired, non-deleted)
	// ban.
	IsBanned(ctx context.Context, uid string) (bool, error)
	// ListBanned returns active bans, newest-first, cursor-paged by uid.
	ListBanned(ctx context.Context, page domain.Page) ([]*domain.UserBan, string, error)
	// ListExpired returns bans whose expires_at ≤ now, so a background
	// cleaner can unban them automatically.
	ListExpired(ctx context.Context, before time.Time) ([]*domain.UserBan, error)
}

type banRepo struct {
	db    *gorm.DB
	audit AuditLedgerTransactionalRepository
}

// NewBanRepository builds a BanRepository backed by the user_bans table.
func NewBanRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) BanRepository {
	return &banRepo{db: db, audit: requireAuditLedger("NewBanRepository", audit)}
}

type userBanRow struct {
	UID       string     `gorm:"column:uid;primaryKey"`
	ActorUID  string     `gorm:"column:actor_uid"`
	Reason    string     `gorm:"column:reason"`
	ExpiresAt *time.Time `gorm:"column:expires_at"`
	CreatedAt time.Time  `gorm:"column:created_at"`
	DeletedAt *time.Time `gorm:"column:deleted_at"`
}

func (userBanRow) TableName() string { return "user_bans" }

func userBanRowToDomain(r *userBanRow) *domain.UserBan {
	return &domain.UserBan{
		UID:       r.UID,
		ActorUID:  r.ActorUID,
		Reason:    r.Reason,
		ExpiresAt: r.ExpiresAt,
		CreatedAt: r.CreatedAt,
	}
}

// Ban creates or upserts a ban row. ON CONFLICT sets deleted_at = NULL so
// a re-ban after an unban also works.
func (r *banRepo) Ban(ctx context.Context, uid, actorUID, reason string, expiresAt *time.Time) error {
	row := &userBanRow{
		UID:       uid,
		ActorUID:  actorUID,
		Reason:    reason,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().UTC(),
	}
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "uid"}},
			DoUpdates: clause.AssignmentColumns([]string{"actor_uid", "reason", "expires_at", "deleted_at", "created_at"}),
		}).
		Create(row).Error
	if err != nil {
		return fmt.Errorf("ban %s: %w", uid, err)
	}
	return nil
}

// Unban soft-deletes the ban row.
func (r *banRepo) Unban(ctx context.Context, uid string) error {
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).
		Model(&userBanRow{}).
		Where("uid = ? AND deleted_at IS NULL", uid).
		Update("deleted_at", now).Error
	if err != nil {
		return fmt.Errorf("unban %s: %w", uid, err)
	}
	return nil
}

// IsBanned checks for an active (non-deleted, non-expired) ban.
func (r *banRepo) IsBanned(ctx context.Context, uid string) (bool, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Model(&userBanRow{}).
		Where("uid = ? AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())", uid).
		Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("is banned %s: %w", uid, err)
	}
	return n > 0, nil
}

// ListBanned returns active bans, cursor-paged by uid.
func (r *banRepo) ListBanned(ctx context.Context, page domain.Page) ([]*domain.UserBan, string, error) {
	page.Normalize()
	var rows []userBanRow
	q := r.db.WithContext(ctx).
		Model(&userBanRow{}).
		Where("deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())").
		Order("created_at DESC, uid ASC").
		Limit(page.Limit + 1)
	if page.Cursor != "" {
		q = q.Where("uid > ?", page.Cursor)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list banned: %w", err)
	}
	next := ""
	if len(rows) > page.Limit {
		next = rows[page.Limit-1].UID
		rows = rows[:page.Limit]
	}
	out := make([]*domain.UserBan, len(rows))
	for i := range rows {
		out[i] = userBanRowToDomain(&rows[i])
	}
	return out, next, nil
}

// ListExpired returns bans that have passed their expiry and can be cleaned up.
func (r *banRepo) ListExpired(ctx context.Context, before time.Time) ([]*domain.UserBan, error) {
	var rows []userBanRow
	err := r.db.WithContext(ctx).
		Model(&userBanRow{}).
		Where("deleted_at IS NULL AND expires_at IS NOT NULL AND expires_at <= ?", before).
		Find(&rows).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("list expired bans: %w", err)
	}
	out := make([]*domain.UserBan, len(rows))
	for i := range rows {
		out[i] = userBanRowToDomain(&rows[i])
	}
	return out, nil
}
