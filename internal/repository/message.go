package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

type MessageRepository interface {
	Create(ctx context.Context, m *domain.Message) error
	Get(ctx context.Context, id domain.ID) (*domain.Message, error)
	ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
	ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error)
	MarkRead(ctx context.Context, id domain.ID, at time.Time) error
}

type messageRepo struct{ db *gorm.DB }

func NewMessageRepository(db *gorm.DB) MessageRepository { return &messageRepo{db: db} }

func (r *messageRepo) Create(ctx context.Context, m *domain.Message) error {
	if err := r.db.WithContext(ctx).Create(msgDomainToRow(m)).Error; err != nil {
		return fmt.Errorf("create message: %w", err)
	}
	return nil
}

func (r *messageRepo) Get(ctx context.Context, id domain.ID) (*domain.Message, error) {
	var row messageRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	return msgRowToDomain(&row), nil
}

func (r *messageRepo) ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).Where("to_uid = ?", toUID)
	if unreadOnly {
		q = q.Where("read_at IS NULL")
	}
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	var rows []messageRow
	if err := q.Order("created_at DESC, id DESC").Limit(page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list inbox: %w", err)
	}
	cursor := ""
	if len(rows) > page.Limit {
		cursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.Message, len(rows))
	for i := range rows {
		out[i] = msgRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *messageRepo) ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).Where(
		"(from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)",
		uidA, uidB, uidB, uidA,
	)
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	var rows []messageRow
	if err := q.Order("created_at DESC, id DESC").Limit(page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list conversation: %w", err)
	}
	cursor := ""
	if len(rows) > page.Limit {
		cursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.Message, len(rows))
	for i := range rows {
		out[i] = msgRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

func (r *messageRepo) MarkRead(ctx context.Context, id domain.ID, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&messageRow{}).
		Where("id = ? AND read_at IS NULL", id).
		Update("read_at", at)
	if res.Error != nil {
		return fmt.Errorf("mark read: %w", res.Error)
	}
	return nil
}
