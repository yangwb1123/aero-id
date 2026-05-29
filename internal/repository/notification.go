package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// NotificationRepository persists per-recipient notification rows and serves the
// recipient feed (newest-first, cursor-paginated) plus unread bookkeeping.
type NotificationRepository interface {
	Create(ctx context.Context, n *domain.Notification) error
	ListByRecipient(ctx context.Context, recipientUID string, page domain.Page) ([]*domain.Notification, string, error)
	MarkRead(ctx context.Context, id domain.ID, at time.Time) error
	MarkAllRead(ctx context.Context, recipientUID string, at time.Time) error
	CountUnread(ctx context.Context, recipientUID string) (int64, error)
}

type notificationRepo struct {
	db *gorm.DB
}

func NewNotificationRepository(db *gorm.DB) NotificationRepository {
	return &notificationRepo{db: db}
}

func (r *notificationRepo) Create(ctx context.Context, n *domain.Notification) error {
	if err := r.db.WithContext(ctx).Create(notificationDomainToRow(n)).Error; err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	return nil
}

func (r *notificationRepo) ListByRecipient(ctx context.Context, recipientUID string, page domain.Page) ([]*domain.Notification, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).Where("recipient_uid = ?", recipientUID)
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	var rows []notificationRow
	if err := q.Order("created_at DESC, id DESC").Limit(page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list notifications: %w", err)
	}
	cursor := ""
	if len(rows) > page.Limit {
		cursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.Notification, len(rows))
	for i := range rows {
		out[i] = notificationRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *notificationRepo) MarkRead(ctx context.Context, id domain.ID, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&notificationRow{}).
		Where("id = ? AND read_at IS NULL", id).
		Update("read_at", at)
	if res.Error != nil {
		return fmt.Errorf("mark notification read: %w", res.Error)
	}
	return nil
}

func (r *notificationRepo) MarkAllRead(ctx context.Context, recipientUID string, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&notificationRow{}).
		Where("recipient_uid = ? AND read_at IS NULL", recipientUID).
		Update("read_at", at)
	if res.Error != nil {
		return fmt.Errorf("mark all notifications read: %w", res.Error)
	}
	return nil
}

// CountUnread returns how many unread notifications target recipientUID. It is
// served by the partial index idx_notifications_unread.
func (r *notificationRepo) CountUnread(ctx context.Context, recipientUID string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&notificationRow{}).
		Where("recipient_uid = ? AND read_at IS NULL", recipientUID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count unread notifications: %w", err)
	}
	return n, nil
}
