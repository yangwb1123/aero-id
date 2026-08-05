package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

// LegacyDataDigest summarizes one legacy table slice for shadow migration
// comparison (P8-02). Content bodies never leave the database: rows contribute
// md5(content) only, assembled into a batch sha256 digest.
type LegacyDataDigest struct {
	Count     int64  `json:"count"`
	SizeBytes int64  `json:"size_bytes,omitempty"` // files only
	Digest    string `json:"digest"`
	Truncated bool   `json:"truncated"` // limit hit; digest covers the scanned slice
}

// LegacyMigrationRepository exposes read-only digests of the legacy
// compatibility tables (ADR-011) for the P8-02 shadow migration comparison.
type LegacyMigrationRepository interface {
	LegacyFileDigest(ctx context.Context, ownerUID string, limit int) (*LegacyDataDigest, error)
	LegacyMessageDigest(ctx context.Context, uid string, limit int) (*LegacyDataDigest, error)
	LegacyCommentDigest(ctx context.Context, ownerUID string, limit int) (*LegacyDataDigest, error)
}

type legacyMigrationRepo struct {
	db *gorm.DB
}

func NewLegacyMigrationRepository(db *gorm.DB) LegacyMigrationRepository {
	return &legacyMigrationRepo{db: db}
}

// legacyFileRow is the canonical, content-free projection of a legacy file row.
type legacyFileRow struct {
	ID          string    `gorm:"column:id"`
	Name        string    `gorm:"column:name"`
	Size        int64     `gorm:"column:size"`
	Checksum    string    `gorm:"column:checksum"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (r *legacyMigrationRepo) LegacyFileDigest(ctx context.Context, ownerUID string, limit int) (*LegacyDataDigest, error) {
	var rows []legacyFileRow
	if err := r.db.WithContext(ctx).
		Table("files").
		Select("id, name, size, COALESCE(checksum, '') AS checksum, created_at").
		Where("owner_uid = ? AND deleted_at IS NULL", ownerUID).
		Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("scan legacy files: %w", err)
	}
	digest := &LegacyDataDigest{Count: int64(len(rows))}
	if len(rows) > limit {
		digest.Truncated = true
		rows = rows[:limit]
		digest.Count = int64(limit)
	}
	hasher := sha256.New()
	for i := range rows {
		row := &rows[i]
		digest.SizeBytes += row.Size
		_, _ = fmt.Fprintf(hasher, "%s|%s|%d|%s|%s\n", row.ID, row.Name, row.Size, row.Checksum, row.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	digest.Digest = hex.EncodeToString(hasher.Sum(nil))
	return digest, nil
}

type legacyMessageRow struct {
	ID          string    `gorm:"column:id"`
	Kind        int16     `gorm:"column:kind"`
	FromUID     string    `gorm:"column:from_uid"`
	ToUID       string    `gorm:"column:to_uid"`
	ContentHash string    `gorm:"column:content_hash"` // md5(content) computed in SQL
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (r *legacyMigrationRepo) LegacyMessageDigest(ctx context.Context, uid string, limit int) (*LegacyDataDigest, error) {
	var rows []legacyMessageRow
	if err := r.db.WithContext(ctx).
		Table("messages").
		Select("id, kind, from_uid, to_uid, md5(content) AS content_hash, created_at").
		Where("(from_uid = ? OR to_uid = ?) AND deleted_at IS NULL", uid, uid).
		Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("scan legacy messages: %w", err)
	}
	digest := &LegacyDataDigest{Count: int64(len(rows))}
	if len(rows) > limit {
		digest.Truncated = true
		rows = rows[:limit]
		digest.Count = int64(limit)
	}
	hasher := sha256.New()
	for i := range rows {
		row := &rows[i]
		_, _ = fmt.Fprintf(hasher, "%s|%d|%s|%s|%s|%s\n", row.ID, row.Kind, row.FromUID, row.ToUID, row.ContentHash, row.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	digest.Digest = hex.EncodeToString(hasher.Sum(nil))
	return digest, nil
}

type legacyCommentRow struct {
	ID          string    `gorm:"column:id"`
	TargetType  string    `gorm:"column:target_type"`
	TargetID    string    `gorm:"column:target_id"`
	ParentID    *string   `gorm:"column:parent_id"`
	ContentHash string    `gorm:"column:content_hash"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (r *legacyMigrationRepo) LegacyCommentDigest(ctx context.Context, ownerUID string, limit int) (*LegacyDataDigest, error) {
	var rows []legacyCommentRow
	if err := r.db.WithContext(ctx).
		Table("comments").
		Select("id, target_type, target_id::text AS target_id, COALESCE(parent_id::text, '') AS parent_id, md5(content) AS content_hash, created_at").
		Where("owner_uid = ? AND deleted_at IS NULL", ownerUID).
		Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("scan legacy comments: %w", err)
	}
	digest := &LegacyDataDigest{Count: int64(len(rows))}
	if len(rows) > limit {
		digest.Truncated = true
		rows = rows[:limit]
		digest.Count = int64(limit)
	}
	hasher := sha256.New()
	for i := range rows {
		row := &rows[i]
		parent := ""
		if row.ParentID != nil {
			parent = *row.ParentID
		}
		_, _ = fmt.Fprintf(hasher, "%s|%s|%s|%s|%s|%s\n", row.ID, row.TargetType, row.TargetID, parent, row.ContentHash, row.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	digest.Digest = hex.EncodeToString(hasher.Sum(nil))
	return digest, nil
}

var _ = idgen.NewID // keep import if future row ids are generated here
