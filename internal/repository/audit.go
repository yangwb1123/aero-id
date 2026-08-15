package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// AuditRepository persists audit events for sensitive operations.
// It also satisfies service.AuditLogger via the Log method.
type AuditRepository interface {
	Append(ctx context.Context, e *domain.AuditEvent) error
	Log(ctx context.Context, e *domain.AuditEvent) error
	// List returns audit events ordered newest-first, cursor-paginated. When
	// actorUID or action is non-empty, results are filtered to that value.
	// limit defaults to 50 (capped at 200).
	List(ctx context.Context, actorUID, action string, page domain.Page) ([]*domain.AuditEvent, string, error)
	// PurgeOlderThan deletes audit event rows created before cutoff, in batches
	// of batchSize (default 1000) with a 100ms pause between batches to limit IO
	// impact. Returns the total number of deleted rows.
	PurgeOlderThan(ctx context.Context, cutoff time.Time, batchSize int) (int64, error)
}

type auditRepo struct{ db *gorm.DB }

// NewAuditRepository returns an AuditRepository backed by GORM.
func NewAuditRepository(db *gorm.DB) AuditRepository { return &auditRepo{db: db} }

// Log satisfies service.AuditLogger so an AuditRepository can be passed directly.
func (r *auditRepo) Log(ctx context.Context, e *domain.AuditEvent) error {
	return r.Append(ctx, e)
}

// Append inserts a new audit event row. A UUID and timestamp are generated if
// not already set on the event.
func (r *auditRepo) Append(ctx context.Context, e *domain.AuditEvent) error {
	if e.ID == (uuid.UUID{}) {
		e.ID = uuid.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	row := auditEventDomainToRow(e)
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}

const (
	defaultAuditLimit = 50
	maxAuditLimit     = 200
)

func (r *auditRepo) List(ctx context.Context, actorUID, action string, page domain.Page) ([]*domain.AuditEvent, string, error) {
	q := r.db.WithContext(ctx).Model(&auditEventRow{}).Order("created_at DESC, id DESC")
	if actorUID != "" {
		q = q.Where("actor_uid = ?", actorUID)
	}
	if action != "" {
		q = q.Where("action = ?", action)
	}
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
	}
	limit := page.Limit
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	if limit > maxAuditLimit {
		limit = maxAuditLimit
	}
	var rows []auditEventRow
	if err := q.Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list audit events: %w", err)
	}
	cursor := ""
	if len(rows) > limit {
		cursor = rows[limit-1].ID.String()
		rows = rows[:limit]
	}
	out := make([]*domain.AuditEvent, len(rows))
	for i := range rows {
		out[i] = auditEventRowToDomain(&rows[i])
	}
	return out, cursor, nil
}

// batchPurgeSize is the default number of rows to delete per batch during TTL cleanup.
const batchPurgeSize = 1000

// purgeInterval is the pause between purge batches to spread IO load.
const purgeInterval = 100 * time.Millisecond

// PurgeOlderThan deletes audit event rows created before cutoff, in batches of
// up to batchSize (when <= 0, batchPurgeSize is used). Each batch is deleted
// with a 100ms pause in between to limit DB IO impact. Returns the total number
// of rows deleted.
func (r *auditRepo) PurgeOlderThan(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	return r.PurgeOlderThanExcluding(ctx, cutoff, batchSize, nil)
}

// PurgeOlderThanExcluding purges legacy audit rows before cutoff, exempting the
// given actor uids (legal hold, P4-07). Pass a non-empty set to honor holds.
func (r *auditRepo) PurgeOlderThanExcluding(ctx context.Context, cutoff time.Time, batchSize int, exemptActors []string) (int64, error) {
	if batchSize <= 0 {
		batchSize = batchPurgeSize
	}
	var total int64
	for {
		// Batch delete: subquery picks the oldest rows first so we make steady
		// progress through the table rather than scanning from the end repeatedly.
		sub := r.db.WithContext(ctx).Model(&auditEventRow{}).
			Where("created_at < ?", cutoff).
			Order("created_at ASC, id ASC").
			Select("id")
		if len(exemptActors) > 0 {
			sub = sub.Where("actor_uid NOT IN ?", exemptActors)
		}
		sub = sub.Limit(batchSize)
		res := r.db.WithContext(ctx).
			Where("id IN (?)", sub).
			Delete(&auditEventRow{})
		if res.Error != nil {
			return total, fmt.Errorf("purge audit events: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			break
		}
		total += res.RowsAffected
		// Pause between batches to avoid saturating the DB connection.
		time.Sleep(purgeInterval)
	}
	return total, nil
}

// RunAuditCleaner starts a background goroutine that periodically purges audit
// event rows older than retention (e.g. 90d) on a fixed interval. Returns a stop
// function. A non-positive interval disables the cleaner (returns no-op stop).
func RunAuditCleaner(repo AuditRepository, log *zap.Logger, retention time.Duration, interval time.Duration) func() {
	return RunAuditCleanerWithHolds(repo, nil, log, retention, interval)
}

// RunAuditCleanerWithHolds additionally consults a legal-hold registry so
// actors under hold are exempt from the retention purge (P4-07). A nil hold
// repository behaves like the plain cleaner.
func RunAuditCleanerWithHolds(repo AuditRepository, holds AuditHoldRepository, log *zap.Logger,
	retention time.Duration, interval time.Duration) func() {
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
				cutoff := time.Now().UTC().Add(-retention)
				var exempt []string
				if holds != nil {
					exempt, _ = holds.HoldScopeIDs(ctx, domain.AuditHoldScopeActor)
				}
				deleted, err := purgeAuditWithExempt(repo, ctx, cutoff, exempt)
				if err != nil {
					log.Warn("audit purge failed", zap.Error(err))
				} else if deleted > 0 {
					log.Info("audit purge complete",
						zap.Int64("deleted", deleted),
						zap.Time("cutoff", cutoff))
				}
			case <-done:
				t.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

// purgeAuditWithExempt routes to the exclusion-aware purge when holds exist.
func purgeAuditWithExempt(repo AuditRepository, ctx context.Context, cutoff time.Time, exempt []string) (int64, error) {
	if excluding, ok := repo.(interface {
		PurgeOlderThanExcluding(context.Context, time.Time, int, []string) (int64, error)
	}); ok {
		return excluding.PurgeOlderThanExcluding(ctx, cutoff, 0, exempt)
	}
	return repo.PurgeOlderThan(ctx, cutoff, 0)
}
