package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UnreadCountRow maps to the unread_counts table.
type UnreadCountRow struct {
	UID        string `gorm:"column:uid;primaryKey"`
	MsgCount   int64  `gorm:"column:msg_count"`
	NotifCount int64  `gorm:"column:notif_count"`
}

func (UnreadCountRow) TableName() string { return "unread_counts" }

// UnreadCounter provides O(1) materialized unread counters. All methods are
// safe for concurrent use and use ON CONFLICT to handle first-access.
type UnreadCounter struct {
	db *gorm.DB
}

func NewUnreadCounter(db *gorm.DB) *UnreadCounter {
	return &UnreadCounter{db: db}
}

// incrementMsg adds delta to the message counter for uid. Use +1 for new
// messages, -1 for marking read.
func (c *UnreadCounter) IncrementMsg(ctx context.Context, tx *gorm.DB, uid string, delta int64) error {
	return c.increment(ctx, tx, uid, "msg_count", delta)
}

// IncrementNotif adds delta to the notification counter for uid.
func (c *UnreadCounter) IncrementNotif(ctx context.Context, tx *gorm.DB, uid string, delta int64) error {
	return c.increment(ctx, tx, uid, "notif_count", delta)
}

// increment is a generic helper that adds delta to the named column, creating
// the row if it does not exist.
func (c *UnreadCounter) increment(ctx context.Context, tx *gorm.DB, uid, column string, delta int64) error {
	// Upsert: insert with default 0, then add delta using the EXCLUDED row to
	// reference the inserted values.
	expr := fmt.Sprintf("%s = COALESCE(unread_counts.%s, 0) + ?", column, column)
	return tx.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "uid"}},
			DoUpdates: clause.Assignments(map[string]any{
				column:       gorm.Expr(expr, delta),
				"updated_at": gorm.Expr("now()"),
			}),
		}).
		Create(&UnreadCountRow{UID: uid}).Error
}

// ReadMsgCounter returns the materialized message unread count for uid.
func (c *UnreadCounter) ReadMsgCounter(ctx context.Context, uid string) (int64, error) {
	return c.readCounter(ctx, uid, "msg_count")
}

// ReadNotifCounter returns the materialized notification unread count for uid.
func (c *UnreadCounter) ReadNotifCounter(ctx context.Context, uid string) (int64, error) {
	return c.readCounter(ctx, uid, "notif_count")
}

func (c *UnreadCounter) readCounter(ctx context.Context, uid, column string) (int64, error) {
	var row UnreadCountRow
	err := c.db.WithContext(ctx).
		Where("uid = ?", uid).
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil // No counter row yet means zero.
		}
		return 0, fmt.Errorf("read unread counter: %w", err)
	}
	if column == "msg_count" {
		return row.MsgCount, nil
	}
	return row.NotifCount, nil
}
