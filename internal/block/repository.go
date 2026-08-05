package block

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

type blockRow struct {
	Blocker   string    `gorm:"column:blocker_uid;primaryKey"`
	Blocked   string    `gorm:"column:blocked_uid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (blockRow) TableName() string { return "user_blocks" }

type blockRepo struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &blockRepo{db: db}
}

func (r *blockRepo) Block(ctx context.Context, blocker, blocked string) error {
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&blockRow{Blocker: blocker, Blocked: blocked}).Error; err != nil {
		return fmt.Errorf("block user %s: %w", blocked, err)
	}
	return nil
}

func (r *blockRepo) Unblock(ctx context.Context, blocker, blocked string) error {
	if err := r.db.WithContext(ctx).
		Where("blocker_uid = ? AND blocked_uid = ?", blocker, blocked).
		Delete(&blockRow{}).Error; err != nil {
		return fmt.Errorf("unblock user %s: %w", blocked, err)
	}
	return nil
}

func (r *blockRepo) IsBlocked(ctx context.Context, blocker, blocked string) (bool, error) {
	var n int64
	if err := r.db.WithContext(ctx).
		Model(&blockRow{}).
		Where("blocker_uid = ? AND blocked_uid = ?", blocker, blocked).
		Count(&n).Error; err != nil {
		return false, fmt.Errorf("is blocked %s: %w", blocked, err)
	}
	return n > 0, nil
}

func (r *blockRepo) ListBlocked(ctx context.Context, blocker string, page domain.Page) ([]string, string, error) {
	page.Normalize()
	var rows []blockRow
	q := r.db.WithContext(ctx).
		Where("blocker_uid = ?", blocker).
		Order("blocked_uid ASC").
		Limit(page.Limit + 1)
	if page.Cursor != "" {
		q = q.Where("blocked_uid > ?", page.Cursor)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list blocked: %w", err)
	}
	nextCursor := ""
	if len(rows) > page.Limit {
		nextCursor = rows[page.Limit-1].Blocked
		rows = rows[:page.Limit]
	}
	uids := make([]string, len(rows))
	for i, r := range rows {
		uids[i] = r.Blocked
	}
	return uids, nextCursor, nil
}
