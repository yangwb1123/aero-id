package repository

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// fileRow is the GORM-mapped representation of the files table.
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

func fileRowToDomain(r *fileRow) *domain.File {
	f := &domain.File{
		ID:          r.ID,
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

func fileDomainToRow(f *domain.File) *fileRow {
	r := &fileRow{
		ID:          f.ID,
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
