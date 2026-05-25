package repository

import (
	"context"
	"errors"
	"fmt"

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
