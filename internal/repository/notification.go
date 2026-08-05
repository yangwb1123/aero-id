package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// NotificationRepository persists per-recipient notification rows and serves the
// recipient feed (newest-first, cursor-paginated) plus unread bookkeeping.
type NotificationRepository interface {
	Create(ctx context.Context, n *domain.Notification) error
	ListByRecipient(ctx context.Context, recipientUID string, page domain.Page) ([]*domain.Notification, string, error)
	// ListSince returns notifications for recipientUID created strictly after
	// since, ordered oldest-first and capped at limit. Used by SSE last_event_id
	// reconnection to replay missed notifications.
	ListSince(ctx context.Context, recipientUID string, since time.Time, limit int) ([]*domain.Notification, error)
	// FindRecentByTypeAndActor returns the most recent unread notification for a
	// recipient matching the given type and actor, created within the last
	// window. Used by the dispatcher to merge duplicate notifications.
	FindRecentByTypeAndActor(ctx context.Context, recipientUID, notifType, actorUID string, window time.Duration) (*domain.Notification, error)
	// IncrementCollapsedCount atomically increments the collapsed_count column
	// on a single notification row, extending the collapse window.
	IncrementCollapsedCount(ctx context.Context, id domain.ID) error
	// IncrementUnreadCount atomically adds delta to the materialized unread
	// counter for uid. Used by the dispatcher after creating a non-collapsed
	// notification so collapsed notifications are not double-counted.
	IncrementUnreadCount(ctx context.Context, uid string, delta int64) error
	MarkRead(ctx context.Context, id domain.ID, at time.Time) error
	MarkAllRead(ctx context.Context, recipientUID string, at time.Time) error
	CountUnread(ctx context.Context, recipientUID string) (int64, error)
	// PurgeReadOlderThan deletes notifications that are either read (past
	// readCutoff) or unread (past unreadCutoff), in batches. Returns the
	// total count of deleted rows.
	PurgeReadOlderThan(ctx context.Context, readCutoff, unreadCutoff time.Time, batchSize int) (int64, error)
}

type notificationRepo struct {
	db      *gorm.DB
	counter *UnreadCounter // materialized unread counter; nil = no-op
}

// NewNotificationRepository builds a notification repository. When counterOpt is
// provided, the unread counter table is updated atomically on create/read.
func NewNotificationRepository(db *gorm.DB, counterOpt ...*UnreadCounter) NotificationRepository {
	r := &notificationRepo{db: db}
	if len(counterOpt) > 0 {
		r.counter = counterOpt[0]
	}
	return r
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

func (r *notificationRepo) IncrementUnreadCount(ctx context.Context, uid string, delta int64) error {
	if r.counter == nil {
		return nil
	}
	return r.counter.IncrementNotif(ctx, r.db.WithContext(ctx), uid, delta)
}

func (r *notificationRepo) MarkRead(ctx context.Context, id domain.ID, at time.Time) error {
	// Load the notification to know the target uid for counter decrement.
	var row notificationRow
	if err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // Idempotent.
		}
		return fmt.Errorf("mark notification read (load): %w", err)
	}
	if row.ReadAt != nil {
		return nil // Already read.
	}
	res := r.db.WithContext(ctx).Model(&notificationRow{}).
		Where("id = ? AND read_at IS NULL", id).
		Update("read_at", at)
	if res.Error != nil {
		return fmt.Errorf("mark notification read: %w", res.Error)
	}
	if r.counter != nil && res.RowsAffected > 0 {
		if err := r.counter.IncrementNotif(ctx, r.db.WithContext(ctx), row.RecipientUID, -1); err != nil {
			return err
		}
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
	if r.counter != nil && res.RowsAffected > 0 {
		if err := r.counter.IncrementNotif(ctx, r.db.WithContext(ctx), recipientUID, -res.RowsAffected); err != nil {
			return err
		}
	}
	return nil
}

// CountUnread returns how many unread notifications target recipientUID.
// When the materialized counter is available, it reads from unread_counts for
// O(1) performance; otherwise it falls back to a COUNT(*) scan.
func (r *notificationRepo) CountUnread(ctx context.Context, recipientUID string) (int64, error) {
	if r.counter != nil {
		n, err := r.counter.ReadNotifCounter(ctx, recipientUID)
		if err != nil {
			return 0, fmt.Errorf("count unread notifications (counter): %w", err)
		}
		return n, nil
	}
	var n int64
	if err := r.db.WithContext(ctx).Model(&notificationRow{}).
		Where("recipient_uid = ? AND read_at IS NULL", recipientUID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count unread notifications: %w", err)
	}
	return n, nil
}

// ListSince returns notifications for recipientUID created strictly after
// since, ordered oldest-first and capped at limit. It is used by SSE
// last_event_id reconnection to replay missed notifications.
func (r *notificationRepo) ListSince(ctx context.Context, recipientUID string, since time.Time, limit int) ([]*domain.Notification, error) {
	var rows []notificationRow
	if err := r.db.WithContext(ctx).
		Where("recipient_uid = ? AND created_at > ?", recipientUID, since).
		Order("created_at ASC, id ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list notifications since: %w", err)
	}
	out := make([]*domain.Notification, len(rows))
	for i := range rows {
		out[i] = notificationRowToDomain(&rows[i])
	}
	return out, nil
}

// FindRecentByTypeAndActor returns the most recent unread notification matching
// recipientUID + type + actorUID, created within the last window. Returns nil,
// nil when no match is found.
func (r *notificationRepo) FindRecentByTypeAndActor(ctx context.Context, recipientUID, notifType, actorUID string,
	window time.Duration) (*domain.Notification, error) {
	cutoff := time.Now().UTC().Add(-window)
	var row notificationRow
	err := r.db.WithContext(ctx).
		Where("recipient_uid = ? AND type = ? AND actor_uid = ? AND read_at IS NULL AND created_at >= ?",
			recipientUID, notifType, actorUID, cutoff).
		Order("created_at DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find recent notification: %w", err)
	}
	return notificationRowToDomain(&row), nil
}

// IncrementCollapsedCount atomically increments collapsed_count on a single
// notification row and resets its created_at to now, extending the collapse
// window so subsequent events keep merging.
func (r *notificationRepo) IncrementCollapsedCount(ctx context.Context, id domain.ID) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&notificationRow{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"collapsed_count": gorm.Expr("collapsed_count + 1"),
			"created_at":      now,
		})
	if res.Error != nil {
		return fmt.Errorf("increment collapsed count: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// notificationPurgeInterval is the pause between purge batches to spread IO load.
const notificationPurgeInterval = 100 * time.Millisecond

// PurgeReadOlderThan deletes notification rows that are either:
//   - read (read_at IS NOT NULL) and read longer ago than readCutoff, OR
//   - unread (read_at IS NULL) and created longer ago than unreadCutoff
//
// Deletion is done in batches of up to batchSize (when <= 0, 500 is used) with
// a 100ms pause between each batch to limit DB IO impact. Returns the total
// number of rows deleted.
func (r *notificationRepo) PurgeReadOlderThan(ctx context.Context, readCutoff, unreadCutoff time.Time, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	var total int64
	for {
		// Batch delete read notifications past readCutoff.
		sub := r.db.WithContext(ctx).Model(&notificationRow{}).
			Where("read_at IS NOT NULL AND read_at < ?", readCutoff).
			Order("created_at ASC, id ASC").
			Select("id").
			Limit(batchSize)
		res := r.db.WithContext(ctx).
			Where("id IN (?)", sub).
			Delete(&notificationRow{})
		if res.Error != nil {
			return total, fmt.Errorf("purge read notifications: %w", res.Error)
		}
		total += res.RowsAffected

		// Also purge unread notifications that are extremely old.
		sub2 := r.db.WithContext(ctx).Model(&notificationRow{}).
			Where("read_at IS NULL AND created_at < ?", unreadCutoff).
			Order("created_at ASC, id ASC").
			Select("id").
			Limit(batchSize)
		res2 := r.db.WithContext(ctx).
			Where("id IN (?) AND read_at IS NULL", sub2).
			Delete(&notificationRow{})
		if res2.Error != nil {
			return total, fmt.Errorf("purge old unread notifications: %w", res2.Error)
		}
		total += res2.RowsAffected

		if res.RowsAffected == 0 && res2.RowsAffected == 0 {
			break
		}
		time.Sleep(notificationPurgeInterval)
	}
	return total, nil
}

// RunNotificationCleaner starts a background goroutine that periodically purges
// old notification rows. Read notifications older than readRetention are deleted,
// and unread notifications older than unreadRetention are also deleted. Runs on
// the given interval. Returns a stop function. A non-positive interval disables
// the cleaner (returns no-op stop).
func RunNotificationCleaner(repo NotificationRepository, log *zap.Logger, readRetention, unreadRetention, interval time.Duration) func() {
	if interval <= 0 {
		return func() {}
	}
	t := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		ctx := context.Background()
		for {
			select {
			case <-t.C:
				now := time.Now().UTC()
				readCutoff := now.Add(-readRetention)
				unreadCutoff := now.Add(-unreadRetention)
				deleted, err := repo.PurgeReadOlderThan(ctx, readCutoff, unreadCutoff, 0)
				if err != nil {
					log.Warn("notification purge failed", zap.Error(err))
				} else if deleted > 0 {
					log.Info("notification purge complete",
						zap.Int64("deleted", deleted),
						zap.Time("read_cutoff", readCutoff),
						zap.Time("unread_cutoff", unreadCutoff))
				}
			case <-done:
				t.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}
