package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// StorageRefRepository manages atomic reference counts for storage keys used
// by content-dedup (multiple files sharing one blob). Each file Create
// increments the ref count; each soft-delete decrements it. The blob is only
// removed when the count reaches zero, closing the race between a concurrent
// Create (reusing the same checksum) and a concurrent Delete.
type StorageRefRepository interface {
	// Increment ensures storage_key has a row (creating it if absent) and
	// increments ref_count atomically. Returns the new count.
	Increment(ctx context.Context, storageKey string) (int, error)
	// Decrement decrements ref_count for storage_key. When the count reaches
	// zero the row is deleted (it no longer needs tracking). Returns the new
	// count (0 when the row was deleted).
	Decrement(ctx context.Context, storageKey string) (int, error)
	// GetCount returns the current ref count for storage_key. Returns 0 when
	// no row exists (no references).
	GetCount(ctx context.Context, storageKey string) (int, error)
}

type storageRefRepo struct {
	db *gorm.DB
}

func NewStorageRefRepository(db *gorm.DB) StorageRefRepository {
	return &storageRefRepo{db: db}
}

// storageRefRow maps to the storage_refs table.
type storageRefRow struct {
	StorageKey string    `gorm:"column:storage_key;primaryKey"`
	RefCount   int       `gorm:"column:ref_count"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

func (storageRefRow) TableName() string { return "storage_refs" }

func (r *storageRefRepo) Increment(ctx context.Context, storageKey string) (int, error) {
	now := time.Now().UTC()
	// ON CONFLICT DO UPDATE SET ref_count = ref_count + 1
	var row storageRefRow
	err := r.db.WithContext(ctx).
		Where("storage_key = ?", storageKey).
		Assign(map[string]any{
			"ref_count":  gorm.Expr("storage_refs.ref_count + 1"),
			"updated_at": now,
		}).
		FirstOrCreate(&storageRefRow{
			StorageKey: storageKey,
			RefCount:   1,
			CreatedAt:  now,
			UpdatedAt:  now,
		}).Error
	if err != nil {
		return 0, fmt.Errorf("storage ref increment: %w", err)
	}
	// Re-read the row to get the post-upsert count.
	if err := r.db.WithContext(ctx).Where("storage_key = ?", storageKey).First(&row).Error; err != nil {
		return 0, fmt.Errorf("storage ref read after increment: %w", err)
	}
	return row.RefCount, nil
}

func (r *storageRefRepo) Decrement(ctx context.Context, storageKey string) (int, error) {
	// Atomically decrement and delete-if-zero using a transaction.
	var count int
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row storageRefRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("storage_key = ?", storageKey).
			First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				count = 0
				return nil
			}
			return err
		}
		row.RefCount--
		row.UpdatedAt = time.Now().UTC()
		if row.RefCount <= 0 {
			// No more references: delete the row.
			if err := tx.Delete(&row).Error; err != nil {
				return err
			}
			count = 0
		} else {
			if err := tx.Model(&row).UpdateColumns(map[string]any{
				"ref_count":  row.RefCount,
				"updated_at": row.UpdatedAt,
			}).Error; err != nil {
				return err
			}
			count = row.RefCount
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("storage ref decrement: %w", err)
	}
	return count, nil
}

func (r *storageRefRepo) GetCount(ctx context.Context, storageKey string) (int, error) {
	var row storageRefRow
	err := r.db.WithContext(ctx).Where("storage_key = ?", storageKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("storage ref get count: %w", err)
	}
	return row.RefCount, nil
}
