package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

type FileRepository interface {
	Create(ctx context.Context, f *domain.File) error
	Get(ctx context.Context, id domain.ID) (*domain.File, error)
	ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*domain.File, string, error)
	Delete(ctx context.Context, id domain.ID) error
	// FindByChecksum returns a live file owned by ownerUID with the given content
	// checksum, or ErrNotFound. Used to deduplicate identical uploads.
	FindByChecksum(ctx context.Context, ownerUID, checksum string) (*domain.File, error)
	// CountByStorageKey counts live files referencing a storage key, so the service
	// can decide whether deleting a row should also delete the shared blob.
	CountByStorageKey(ctx context.Context, storageKey string) (int64, error)
	// ListSoftDeletedBefore returns soft-deleted file rows whose deleted_at is strictly
	// older than before, capped at limit and ordered oldest-first. Used by the GC
	// reconciliation sweep to purge rows past their retention window.
	ListSoftDeletedBefore(ctx context.Context, before time.Time, limit int) ([]*domain.File, error)
	// HardDelete permanently removes file rows by id (bypassing the soft-delete scope).
	// It is idempotent: ids that no longer exist are silently skipped.
	HardDelete(ctx context.Context, ids []domain.ID) error
	// CountAnyByStorageKey counts files referencing a storage key INCLUDING soft-deleted
	// rows, so the orphan sweep never reaps a blob still referenced by any row (live or
	// soft-deleted-but-not-yet-purged).
	CountAnyByStorageKey(ctx context.Context, storageKey string) (int64, error)
}

type fileRepo struct {
	db *gorm.DB
}

func NewFileRepository(db *gorm.DB) FileRepository {
	return &fileRepo{db: db}
}

func (r *fileRepo) Create(ctx context.Context, f *domain.File) error {
	row := fileDomainToRow(f)
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	return nil
}

func (r *fileRepo) Get(ctx context.Context, id domain.ID) (*domain.File, error) {
	var row fileRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	return fileRowToDomain(&row), nil
}

func (r *fileRepo) ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*domain.File, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).
		Where("owner_uid = ?", ownerUID).
		Order("created_at DESC, id DESC").
		Limit(page.Limit + 1) // fetch one extra to detect "has more"
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor) // simple id-based cursor; works because UUID v7 is time-ordered
	}
	var rows []fileRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list files: %w", err)
	}
	nextCursor := ""
	if len(rows) > page.Limit {
		nextCursor = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	out := make([]*domain.File, len(rows))
	for i := range rows {
		out[i] = fileRowToDomain(&rows[i])
	}
	return out, nextCursor, nil
}

func (r *fileRepo) Delete(ctx context.Context, id domain.ID) error {
	res := r.db.WithContext(ctx).Where("id = ?", id).Delete(&fileRow{})
	if res.Error != nil {
		return fmt.Errorf("delete file: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// FindByChecksum returns a live (non-soft-deleted) file owned by ownerUID with the
// given checksum. GORM's soft-delete scope excludes deleted rows automatically.
func (r *fileRepo) FindByChecksum(ctx context.Context, ownerUID, checksum string) (*domain.File, error) {
	var row fileRow
	err := r.db.WithContext(ctx).
		Where("owner_uid = ? AND checksum = ?", ownerUID, checksum).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find by checksum: %w", err)
	}
	return fileRowToDomain(&row), nil
}

// CountByStorageKey counts live files referencing storageKey (soft-deleted rows are
// excluded), so callers can tell whether a blob is still shared.
func (r *fileRepo) CountByStorageKey(ctx context.Context, storageKey string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&fileRow{}).
		Where("storage_key = ?", storageKey).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count by storage key: %w", err)
	}
	return n, nil
}

// ListSoftDeletedBefore returns soft-deleted rows older than before, oldest-first.
// Unscoped() lifts GORM's soft-delete scope so the otherwise-hidden rows are visible.
func (r *fileRepo) ListSoftDeletedBefore(ctx context.Context, before time.Time, limit int) ([]*domain.File, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []fileRow
	if err := r.db.WithContext(ctx).Unscoped().
		Where("deleted_at IS NOT NULL AND deleted_at < ?", before).
		Order("deleted_at ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list soft-deleted before: %w", err)
	}
	out := make([]*domain.File, len(rows))
	for i := range rows {
		out[i] = fileRowToDomain(&rows[i])
	}
	return out, nil
}

// HardDelete permanently removes the given rows. Unscoped() turns GORM's soft delete
// into a real DELETE. No-op when ids is empty.
func (r *fileRepo) HardDelete(ctx context.Context, ids []domain.ID) error {
	if len(ids) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).Unscoped().
		Where("id IN ?", ids).
		Delete(&fileRow{}).Error; err != nil {
		return fmt.Errorf("hard delete files: %w", err)
	}
	return nil
}

// CountAnyByStorageKey counts files referencing storageKey including soft-deleted rows
// (Unscoped lifts the soft-delete scope). The orphan sweep uses this so a blob whose row
// is soft-deleted but not yet purged is never mistaken for an orphan.
func (r *fileRepo) CountAnyByStorageKey(ctx context.Context, storageKey string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Unscoped().Model(&fileRow{}).
		Where("storage_key = ?", storageKey).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count any by storage key: %w", err)
	}
	return n, nil
}
