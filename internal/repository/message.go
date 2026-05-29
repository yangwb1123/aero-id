package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
)

type MessageRepository interface {
	// Create inserts a message. When evt is non-nil it is appended to the outbox
	// in the SAME transaction, so the message and its domain event commit together
	// (or not at all).
	Create(ctx context.Context, m *domain.Message, evt *outbox.Event) error
	Get(ctx context.Context, id domain.ID) (*domain.Message, error)
	ListInbox(ctx context.Context, toUID string, page domain.Page, unreadOnly bool) ([]*domain.Message, string, error)
	ListConversation(ctx context.Context, uidA, uidB string, page domain.Page) ([]*domain.Message, string, error)
	MarkRead(ctx context.Context, id domain.ID, at time.Time) error
	CountUnread(ctx context.Context, toUID string) (int64, error)
}

type messageRepo struct {
	db     *gorm.DB
	outbox outbox.Repository
}

// NewMessageRepository builds a message repository. ob is the outbox repository
// used to append domain events transactionally with message writes; it may be nil
// (events are then simply not emitted), keeping existing wiring/tests working.
func NewMessageRepository(db *gorm.DB, ob outbox.Repository) MessageRepository {
	return &messageRepo{db: db, outbox: ob}
}

// Create inserts the message and, when evt is non-nil and an outbox repository is
// configured, appends evt within the same transaction so the row and the event
// commit atomically.
func (r *messageRepo) Create(ctx context.Context, m *domain.Message, evt *outbox.Event) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(msgDomainToRow(m)).Error; err != nil {
			return err
		}
		if evt != nil && r.outbox != nil {
			if err := r.outbox.Append(ctx, tx, evt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
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

// CountUnread returns how many undeleted, unread messages are addressed to toUID.
// It is served by the partial index idx_messages_unread.
func (r *messageRepo) CountUnread(ctx context.Context, toUID string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&messageRow{}).
		Where("to_uid = ? AND read_at IS NULL", toUID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count unread: %w", err)
	}
	return n, nil
}
