package file

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

type fileRow struct {
	ID          uuid.UUID         `gorm:"column:id;primaryKey"`
	OwnerUID    string            `gorm:"column:owner_uid"`
	Name        string            `gorm:"column:name"`
	Size        int64             `gorm:"column:size"`
	MimeType    string            `gorm:"column:mime_type"`
	StorageKey  string            `gorm:"column:storage_key"`
	StorageType string            `gorm:"column:storage_type"`
	Checksum    *string           `gorm:"column:checksum"`
	Visibility  int16             `gorm:"column:visibility"`
	Meta        datatypes.JSONMap `gorm:"column:meta"`
	CreatedAt   time.Time         `gorm:"column:created_at"`
	UpdatedAt   time.Time         `gorm:"column:updated_at"`
	DeletedAt   gorm.DeletedAt    `gorm:"column:deleted_at;index"`
}

func (fileRow) TableName() string { return "files" }

func fileRowToDomain(r *fileRow) *File {
	f := &File{
		ID:          domain.ID(r.ID),
		OwnerUID:    r.OwnerUID,
		Name:        r.Name,
		Size:        r.Size,
		MimeType:    r.MimeType,
		StorageKey:  r.StorageKey,
		StorageType: r.StorageType,
		Visibility:  domain.Visibility(r.Visibility),
		Meta:        map[string]any(r.Meta),
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
	if r.Checksum != nil {
		f.Checksum = *r.Checksum
	}
	return f
}

func fileDomainToRow(f *File) *fileRow {
	r := &fileRow{
		ID:          uuid.UUID(f.ID),
		OwnerUID:    f.OwnerUID,
		Name:        f.Name,
		Size:        f.Size,
		MimeType:    f.MimeType,
		StorageKey:  f.StorageKey,
		StorageType: f.StorageType,
		Visibility:  int16(f.Visibility),
		Meta:        datatypes.JSONMap(f.Meta),
		CreatedAt:   f.CreatedAt,
		UpdatedAt:   f.UpdatedAt,
	}
	if f.Checksum != "" {
		c := f.Checksum
		r.Checksum = &c
	}
	return r
}

type fileRepo struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) Repository {
	return &fileRepo{db: db}
}

func (r *fileRepo) Create(ctx context.Context, f *File) error {
	row := fileDomainToRow(f)
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	return nil
}

func (r *fileRepo) Get(ctx context.Context, id domain.ID) (*File, error) {
	var row fileRow
	err := r.db.WithContext(ctx).First(&row, "id = ?", id).Error
	if stderrors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}
	return fileRowToDomain(&row), nil
}

func (r *fileRepo) ListByOwner(ctx context.Context, ownerUID string, page domain.Page) ([]*File, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).
		Where("owner_uid = ?", ownerUID).
		Order("created_at DESC, id DESC").
		Limit(page.Limit + 1)
	if page.Cursor != "" {
		q = q.Where("id < ?", page.Cursor)
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
	out := make([]*File, len(rows))
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
		return domain.ErrNotFound
	}
	return nil
}

func (r *fileRepo) FindByChecksum(ctx context.Context, ownerUID, checksum string) (*File, error) {
	var row fileRow
	err := r.db.WithContext(ctx).
		Where("owner_uid = ? AND checksum = ?", ownerUID, checksum).
		First(&row).Error
	if stderrors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find by checksum: %w", err)
	}
	return fileRowToDomain(&row), nil
}

func (r *fileRepo) CountByStorageKey(ctx context.Context, storageKey string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Model(&fileRow{}).
		Where("storage_key = ?", storageKey).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count by storage key: %w", err)
	}
	return n, nil
}

func (r *fileRepo) ListSoftDeletedBefore(ctx context.Context, before time.Time, limit int) ([]*File, error) {
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
	out := make([]*File, len(rows))
	for i := range rows {
		out[i] = fileRowToDomain(&rows[i])
	}
	return out, nil
}

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

func (r *fileRepo) UpdateMetadata(ctx context.Context, id domain.ID, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	if meta, ok := fields["meta"].(map[string]any); ok {
		fields["meta"] = datatypes.JSONMap(meta)
	}
	res := r.db.WithContext(ctx).Model(&fileRow{}).
		Where("id = ?", id).
		Updates(fields)
	if res.Error != nil {
		return fmt.Errorf("update file metadata: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *fileRepo) CountAnyByStorageKey(ctx context.Context, storageKey string) (int64, error) {
	var n int64
	if err := r.db.WithContext(ctx).Unscoped().Model(&fileRow{}).
		Where("storage_key = ?", storageKey).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count any by storage key: %w", err)
	}
	return n, nil
}

func (r *fileRepo) SearchFiles(ctx context.Context, ownerUID, query string, limit int) ([]*File, error) {
	var rows []fileRow
	err := r.db.WithContext(ctx).Raw(
		`SELECT id, owner_uid, name, size, mime_type, storage_key, storage_type,
		        checksum, visibility, meta, created_at, updated_at
		 FROM files
		 WHERE deleted_at IS NULL
		   AND owner_uid = ?
		   AND search_vector @@ plainto_tsquery('simple', ?)
		 ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC
		 LIMIT ?`,
		ownerUID, query, query, limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search files: %w", err)
	}
	out := make([]*File, len(rows))
	for i := range rows {
		out[i] = fileRowToDomain(&rows[i])
	}
	return out, nil
}
