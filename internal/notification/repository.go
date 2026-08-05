package notification

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

type notificationRow struct {
	ID           uuid.UUID  `gorm:"column:id;primaryKey"`
	RecipientUID string     `gorm:"column:recipient_uid"`
	Type         string     `gorm:"column:type"`
	ActorUID     string     `gorm:"column:actor_uid"`
	SubjectType  string     `gorm:"column:subject_type"`
	SubjectID    string     `gorm:"column:subject_id"`
	ReadAt       *time.Time `gorm:"column:read_at"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
}

func (notificationRow) TableName() string { return "notifications" }

func rowToDomain(r *notificationRow) *Notification {
	return &Notification{
		ID:           domain.ID(r.ID),
		RecipientUID: r.RecipientUID,
		Type:         r.Type,
		ActorUID:     r.ActorUID,
		SubjectType:  r.SubjectType,
		SubjectID:    r.SubjectID,
		ReadAt:       r.ReadAt,
		CreatedAt:    r.CreatedAt,
	}
}

type notificationRepo struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &notificationRepo{db: db}
}

func (r *notificationRepo) Create(ctx context.Context, n *Notification) error {
	if n.ID == (domain.ID{}) {
		n.ID = domain.ID(uuid.New())
	}
	if err := r.db.WithContext(ctx).Create(&notificationRow{
		ID:           uuid.UUID(n.ID),
		RecipientUID: n.RecipientUID,
		Type:         n.Type,
		ActorUID:     n.ActorUID,
		SubjectType:  n.SubjectType,
		SubjectID:    n.SubjectID,
		CreatedAt:    n.CreatedAt,
	}).Error; err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	return nil
}

func (r *notificationRepo) ListByRecipient(ctx context.Context, recipientUID string, page domain.Page) ([]*Notification, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).
		Where("recipient_uid = ?", recipientUID).
		Order("created_at DESC, id DESC").
		Limit(page.Limit + 1)
	if page.Cursor != "" {
		if createdAt, id, ok := parseNotificationCursor(page.Cursor); ok {
			q = q.Where("(created_at, id) < (?, ?)", createdAt, id)
		} else {
			q = q.Where("id < ?", page.Cursor)
		}
	}
	var rows []notificationRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list notifications: %w", err)
	}
	nextCursor := ""
	if len(rows) > page.Limit {
		nextCursor = notificationCursor(&rows[page.Limit-1])
		rows = rows[:page.Limit]
	}
	out := make([]*Notification, len(rows))
	for i := range rows {
		out[i] = rowToDomain(&rows[i])
	}
	return out, nextCursor, nil
}

func notificationCursor(row *notificationRow) string {
	return row.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + row.ID.String()
}

func parseNotificationCursor(value string) (time.Time, uuid.UUID, bool) {
	createdAtText, idText, ok := strings.Cut(value, "|")
	if !ok {
		return time.Time{}, uuid.Nil, false
	}
	createdAt, err := time.Parse(time.RFC3339Nano, createdAtText)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	id, err := uuid.Parse(idText)
	return createdAt, id, err == nil
}

func (r *notificationRepo) MarkRead(ctx context.Context, id domain.ID, readAt time.Time) error {
	if err := r.db.WithContext(ctx).Model(&notificationRow{}).
		Where("id = ? AND read_at IS NULL", id).
		Update("read_at", readAt).Error; err != nil {
		return fmt.Errorf("mark notification read: %w", err)
	}
	return nil
}

func (r *notificationRepo) MarkAllRead(ctx context.Context, recipientUID string, readAt time.Time) error {
	if err := r.db.WithContext(ctx).Model(&notificationRow{}).
		Where("recipient_uid = ? AND read_at IS NULL", recipientUID).
		Update("read_at", readAt).Error; err != nil {
		return fmt.Errorf("mark all notifications read: %w", err)
	}
	return nil
}

func (r *notificationRepo) CountUnread(ctx context.Context, recipientUID string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&notificationRow{}).
		Where("recipient_uid = ? AND read_at IS NULL", recipientUID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count unread notifications: %w", err)
	}
	return n, nil
}
