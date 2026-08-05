package message

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
)

type messageRow struct {
	ID        uuid.UUID         `gorm:"column:id;primaryKey"`
	FromUID   string            `gorm:"column:from_uid"`
	ToUID     string            `gorm:"column:to_uid"`
	Kind      int16             `gorm:"column:kind"`
	Content   string            `gorm:"column:content"`
	FileID    *uuid.UUID        `gorm:"column:file_id"`
	ReadAt    *time.Time        `gorm:"column:read_at"`
	EditedAt  *time.Time        `gorm:"column:edited_at"`
	Meta      datatypes.JSONMap `gorm:"column:meta"`
	CreatedAt time.Time         `gorm:"column:created_at"`
	DeletedAt gorm.DeletedAt    `gorm:"column:deleted_at;index"`
}

func (messageRow) TableName() string { return "messages" }

type messageReactionRow struct {
	MessageID uuid.UUID `gorm:"column:message_id;primaryKey"`
	UID       string    `gorm:"column:uid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (messageReactionRow) TableName() string { return "message_reactions" }

type conversationMessageRow struct {
	ConvKey   string    `gorm:"column:conv_key"`
	ID        uuid.UUID `gorm:"column:id"`
	FromUID   string    `gorm:"column:from_uid"`
	ToUID     string    `gorm:"column:to_uid"`
	Kind      int16     `gorm:"column:kind"`
	Content   string    `gorm:"column:content"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func msgRowToDomain(r *messageRow) *domain.Message {
	m := &domain.Message{
		ID:        r.ID,
		FromUID:   r.FromUID,
		ToUID:     r.ToUID,
		Kind:      domain.MessageKind(r.Kind),
		Content:   r.Content,
		FileID:    r.FileID,
		ReadAt:    r.ReadAt,
		EditedAt:  r.EditedAt,
		Meta:      map[string]any(r.Meta),
		CreatedAt: r.CreatedAt,
	}
	return m
}

func msgDomainToRow(m *domain.Message) *messageRow {
	return &messageRow{
		ID:        m.ID,
		FromUID:   m.FromUID,
		ToUID:     m.ToUID,
		Kind:      int16(m.Kind),
		Content:   m.Content,
		FileID:    m.FileID,
		ReadAt:    m.ReadAt,
		EditedAt:  m.EditedAt,
		Meta:      datatypes.JSONMap(m.Meta),
		CreatedAt: m.CreatedAt,
	}
}

type messageRepo struct {
	db     *gorm.DB
	outbox outbox.Repository
}

func NewMessageRepository(db *gorm.DB, ob outbox.Repository) MessageRepository {
	return &messageRepo{db: db, outbox: ob}
}

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
		return nil, domain.ErrNotFound
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

func (r *messageRepo) UpdateContent(ctx context.Context, id domain.ID, content string, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&messageRow{}).
		Where("id = ?", id).
		Updates(map[string]any{"content": content, "edited_at": at})
	if res.Error != nil {
		return fmt.Errorf("update message content: %w", res.Error)
	}
	return nil
}

func (r *messageRepo) SoftDelete(ctx context.Context, id domain.ID) error {
	if err := r.db.WithContext(ctx).Where("id = ?", id).Delete(&messageRow{}).Error; err != nil {
		return fmt.Errorf("soft delete message: %w", err)
	}
	return nil
}

func (r *messageRepo) MarkConversationRead(ctx context.Context, toUID, peer string, at time.Time) error {
	res := r.db.WithContext(ctx).Model(&messageRow{}).
		Where("to_uid = ? AND from_uid = ? AND read_at IS NULL", toUID, peer).
		Update("read_at", at)
	if res.Error != nil {
		return fmt.Errorf("mark conversation read: %w", res.Error)
	}
	return nil
}

func (r *messageRepo) CountUnread(ctx context.Context, toUID string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&messageRow{}).
		Where("to_uid = ? AND read_at IS NULL", toUID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count unread: %w", err)
	}
	return n, nil
}

func (r *messageRepo) ReactToMessage(ctx context.Context, messageID uuid.UUID, uid string) error {
	var row messageRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", messageID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ErrNotFound
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

func (r *messageRepo) UnreactMessage(ctx context.Context, messageID uuid.UUID, uid string) error {
	if err := r.db.WithContext(ctx).
		Where("message_id = ? AND uid = ?", messageID, uid).
		Delete(&messageReactionRow{}).Error; err != nil {
		return fmt.Errorf("unreact message: %w", err)
	}
	return nil
}

// ListConversations returns aggregated conversation summaries for the given actor.
// It uses a DISTINCT ON lateral pattern: for each conversation partner, it fetches
// the most recent message and counts unread messages in one pass.
func (r *messageRepo) ListConversations(ctx context.Context, uid string, page domain.Page) ([]*ConversationSummary, string, error) {
	page.Normalize()
	rows, err := r.loadConversationMessages(ctx, uid, page)
	if err != nil {
		return nil, "", err
	}
	cursor := conversationCursor(rows, page.Limit)
	if len(rows) > page.Limit {
		rows = rows[:page.Limit]
	}
	summaries, err := r.conversationSummaries(ctx, uid, rows)
	if err != nil {
		return nil, "", err
	}
	return summaries, cursor, nil
}

func (r *messageRepo) loadConversationMessages(ctx context.Context, uid string, page domain.Page) ([]conversationMessageRow, error) {
	var rows []conversationMessageRow
	if err := r.db.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (conv_key) *
		FROM (
			SELECT
				LEAST(from_uid, to_uid) || ':' || GREATEST(from_uid, to_uid) AS conv_key,
				id, from_uid, to_uid, kind, content, created_at
			FROM messages
			WHERE (from_uid = ? OR to_uid = ?)
			  AND deleted_at IS NULL
		) sub
		WHERE (? = '' OR conv_key > ?)
		ORDER BY conv_key, created_at DESC
		LIMIT ?
	`, uid, uid, page.Cursor, page.Cursor, page.Limit+1).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	return rows, nil
}

func conversationCursor(rows []conversationMessageRow, limit int) string {
	if len(rows) <= limit {
		return ""
	}
	return rows[limit-1].ConvKey
}

func (r *messageRepo) conversationSummaries(ctx context.Context, uid string,
	rows []conversationMessageRow) ([]*ConversationSummary, error) {
	summaries := make([]*ConversationSummary, 0, len(rows))
	for _, row := range rows {
		peer := row.FromUID
		if peer == uid {
			peer = row.ToUID
		}
		unread, err := r.CountUnreadFrom(ctx, uid, peer)
		if err != nil {
			return nil, fmt.Errorf("count unread for %s: %w", peer, err)
		}
		summaries = append(summaries, &ConversationSummary{
			PeerUID: peer, LastMessage: conversationMessage(row), UnreadCount: unread,
		})
	}
	return summaries, nil
}

func conversationMessage(row conversationMessageRow) *domain.Message {
	return &domain.Message{ID: row.ID, FromUID: row.FromUID, ToUID: row.ToUID,
		Kind: domain.MessageKind(row.Kind), Content: row.Content, CreatedAt: row.CreatedAt}
}

// CountUnreadFrom counts unread messages received by toUID from a specific peer.
func (r *messageRepo) CountUnreadFrom(ctx context.Context, toUID, fromUID string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&messageRow{}).
		Where("to_uid = ? AND from_uid = ? AND read_at IS NULL", toUID, fromUID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count unread from: %w", err)
	}
	return n, nil
}

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
