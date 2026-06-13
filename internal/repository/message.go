package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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
	// MarkReadWithEvent stamps read_at and, when evt is non-nil, appends it to the
	// outbox in the SAME transaction so the read and its domain event commit together.
	MarkReadWithEvent(ctx context.Context, id domain.ID, at time.Time, evt *outbox.Event) error
	CountUnread(ctx context.Context, toUID string) (int64, error)
	// UpdateContent rewrites a (non-deleted) message's content and stamps edited_at.
	UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error
	// SoftDelete soft-deletes a message (sets deleted_at). Idempotent.
	SoftDelete(ctx context.Context, id domain.ID) error
	// MarkConversationRead marks every undeleted, unread message from peer to toUID
	// as read at the given time, in a single statement.
	MarkConversationRead(ctx context.Context, toUID, peer string, at time.Time) error
	// ReactToMessage records a reaction by uid. Idempotent (ON CONFLICT DO NOTHING).
	// Returns ErrNotFound when the message does not exist.
	ReactToMessage(ctx context.Context, messageID uuid.UUID, uid string) error
	// UnreactMessage removes a reaction by uid. Idempotent: returns nil even when
	// no reaction row existed.
	UnreactMessage(ctx context.Context, messageID uuid.UUID, uid string) error
	// SearchMessages returns messages visible to uid (sender or recipient) whose
	// content matches the full-text query, ranked by relevance.
	SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error)
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

// MarkReadWithEvent stamps read_at and, when evt is non-nil and an outbox
// repository is configured, appends evt within the same transaction so the
// read and the event commit atomically.
func (r *messageRepo) MarkReadWithEvent(ctx context.Context, id domain.ID, at time.Time, evt *outbox.Event) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&messageRow{}).
			Where("id = ? AND read_at IS NULL", id).
			Update("read_at", at)
		if res.Error != nil {
			return res.Error
		}
		if evt != nil && r.outbox != nil {
			if err := r.outbox.Append(ctx, tx, evt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("mark read with event: %w", err)
	}
	return nil
}

// UpdateContent rewrites the message's content and stamps edited_at. GORM's default
// soft-delete scope restricts the update to rows with deleted_at IS NULL, so a
// soft-deleted message is never touched here.
func (r *messageRepo) UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&messageRow{}).
		Where("id = ?", id).
		Updates(map[string]any{"content": content, "edited_at": at})
	if res.Error != nil {
		return fmt.Errorf("update message content: %w", res.Error)
	}
	return nil
}

// SoftDelete soft-deletes the message (GORM sets deleted_at). It is idempotent: an
// already-deleted row is filtered out by the default scope and silently matches nothing.
func (r *messageRepo) SoftDelete(ctx context.Context, id domain.ID) error {
	if err := r.db.WithContext(ctx).Where("id = ?", id).Delete(&messageRow{}).Error; err != nil {
		return fmt.Errorf("soft delete message: %w", err)
	}
	return nil
}

// MarkConversationRead stamps read_at on every undeleted, unread message from peer to
// toUID in one UPDATE. The default soft-delete scope adds deleted_at IS NULL.
func (r *messageRepo) MarkConversationRead(ctx context.Context, toUID, peer string, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&messageRow{}).
		Where("to_uid = ? AND from_uid = ? AND read_at IS NULL", toUID, peer).
		Update("read_at", at)
	if res.Error != nil {
		return fmt.Errorf("mark conversation read: %w", res.Error)
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

// ReactToMessage inserts a (messageID, uid) reaction row with ON CONFLICT DO NOTHING
// so a repeated reaction is a no-op. Returns ErrNotFound when the message is missing.
func (r *messageRepo) ReactToMessage(ctx context.Context, messageID uuid.UUID, uid string) error {
	var row messageRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", messageID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("react to message: %w", err)
	}
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&messageReactionRow{MessageID: messageID, UID: uid, CreatedAt: time.Now().UTC()})
	if res.Error != nil {
		return fmt.Errorf("react to message: %w", res.Error)
	}
	return nil
}

// UnreactMessage deletes the (messageID, uid) reaction row. Returns nil if no row existed.
func (r *messageRepo) UnreactMessage(ctx context.Context, messageID uuid.UUID, uid string) error {
	if err := r.db.WithContext(ctx).
		Where("message_id = ? AND uid = ?", messageID, uid).
		Delete(&messageReactionRow{}).Error; err != nil {
		return fmt.Errorf("unreact message: %w", err)
	}
	return nil
}

// SearchMessages returns messages the uid sent or received whose content matches
// the full-text query, ranked by relevance. Requires migration 000016.
func (r *messageRepo) SearchMessages(ctx context.Context, uid, query string, limit int) ([]*domain.Message, error) {
	var rows []messageRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, from_uid, to_uid, kind, content, file_id, read_at, edited_at, meta, created_at
		 FROM messages
		 WHERE deleted_at IS NULL
		   AND (from_uid = ? OR to_uid = ?)
		   AND search_vector @@ plainto_tsquery('simple', ?)
		 ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC
		 LIMIT ?`,
		uid, uid, query, query, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	out := make([]*domain.Message, len(rows))
	for i := range rows {
		out[i] = msgRowToDomain(&rows[i])
	}
	return out, nil
}
