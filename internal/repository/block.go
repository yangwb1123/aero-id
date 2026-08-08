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

// userBlockRow maps the user_blocks table: one row per (blocker, blocked) pair.
// Blocking is directional — blocker_uid has blocked blocked_uid — and private to
// the blocker.
type userBlockRow struct {
	BlockerUID string    `gorm:"column:blocker_uid;primaryKey"`
	BlockedUID string    `gorm:"column:blocked_uid;primaryKey"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (userBlockRow) TableName() string { return "user_blocks" }

// BlockRepository persists directional user-block records: blocker_uid has blocked
// blocked_uid.
type BlockRepository interface {
	// Block records that blocker has blocked blocked. It is idempotent: re-blocking an
	// existing pair is a no-op (ON CONFLICT DO NOTHING).
	Block(ctx context.Context, blocker, blocked string) error
	// Unblock removes the (blocker, blocked) record. Unblocking a non-existent pair is a no-op.
	Unblock(ctx context.Context, blocker, blocked string) error
	// BlockWithAudit records the block and appends the fact atomically (one gorm
	// transaction; the fact is unconditional — a re-block no-op still records).
	BlockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error
	// UnblockWithAudit removes the block pair and appends the fact atomically.
	UnblockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error
	// IsBlocked reports whether blocker has blocked blocked.
	IsBlocked(ctx context.Context, blocker, blocked string) (bool, error)
	// ListBlocked returns the uids blocker has blocked, newest first, paginated by the
	// created_at cursor.
	ListBlocked(ctx context.Context, blocker string, page domain.Page) ([]string, string, error)
}

type blockRepo struct {
	db    *gorm.DB
	audit AuditLedgerTransactionalRepository
}

func NewBlockRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) BlockRepository {
	return &blockRepo{db: db, audit: requireAuditLedger("NewBlockRepository", audit)}
}

func (r *blockRepo) Block(ctx context.Context, blocker, blocked string) error {
	row := userBlockRow{BlockerUID: blocker, BlockedUID: blocked, CreatedAt: time.Now().UTC()}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&row).Error; err != nil {
		return fmt.Errorf("create user block: %w", err)
	}
	return nil
}

func (r *blockRepo) Unblock(ctx context.Context, blocker, blocked string) error {
	if err := r.db.WithContext(ctx).
		Where("blocker_uid = ? AND blocked_uid = ?", blocker, blocked).
		Delete(&userBlockRow{}).Error; err != nil {
		return fmt.Errorf("delete user block: %w", err)
	}
	return nil
}

// BlockWithAudit records the block and appends the fact atomically (B2-1). The
// audit surface is part of BlockRepository so a block service without an audit
// path is unconstructible at compile time (fail-closed by construction).
func (r *blockRepo) BlockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := userBlockRow{BlockerUID: blocker, BlockedUID: blocked, CreatedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("create user block: %w", err)
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append block audit: %w", err)
		}
		return nil
	})
}

// UnblockWithAudit removes the block pair and appends the fact atomically.
func (r *blockRepo) UnblockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("blocker_uid = ? AND blocked_uid = ?", blocker, blocked).
			Delete(&userBlockRow{}).Error; err != nil {
			return fmt.Errorf("delete user block: %w", err)
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append block audit: %w", err)
		}
		return nil
	})
}

func (r *blockRepo) IsBlocked(ctx context.Context, blocker, blocked string) (bool, error) {
	var n int64
	if err := r.db.WithContext(ctx).
		Model(&userBlockRow{}).
		Where("blocker_uid = ? AND blocked_uid = ?", blocker, blocked).
		Count(&n).Error; err != nil {
		return false, fmt.Errorf("is blocked: %w", err)
	}
	return n > 0, nil
}

func (r *blockRepo) ListBlocked(ctx context.Context, blocker string, page domain.Page) ([]string, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).
		Model(&userBlockRow{}).
		Where("blocker_uid = ?", blocker).
		Order("created_at DESC, blocked_uid DESC").
		Limit(page.Limit + 1) // fetch one extra to detect "has more"
	if page.Cursor != "" {
		q = q.Where("created_at < ?", page.Cursor)
	}
	var rows []userBlockRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list blocked: %w", err)
	}
	nextCursor := ""
	if len(rows) > page.Limit {
		nextCursor = rows[page.Limit-1].CreatedAt.UTC().Format(time.RFC3339Nano)
		rows = rows[:page.Limit]
	}
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].BlockedUID
	}
	return out, nextCursor, nil
}
