package file

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

type fileShareRow struct {
	FileID     uuid.UUID `gorm:"column:file_id;primaryKey"`
	GranteeUID string    `gorm:"column:grantee_uid;primaryKey"`
	OwnerUID   string    `gorm:"column:owner_uid"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (fileShareRow) TableName() string { return "file_shares" }

type fileShareRepo struct{ db *gorm.DB }

func NewShareRepository(db *gorm.DB) ShareRepository { return &fileShareRepo{db: db} }

func (r *fileShareRepo) Grant(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string) error {
	if len(granteeUIDs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]fileShareRow, 0, len(granteeUIDs))
	for _, g := range granteeUIDs {
		rows = append(rows, fileShareRow{FileID: uuid.UUID(fileID), GranteeUID: g, OwnerUID: ownerUID, CreatedAt: now})
	}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&rows).Error; err != nil {
		return fmt.Errorf("grant file share: %w", err)
	}
	return nil
}

func (r *fileShareRepo) Revoke(ctx context.Context, fileID domain.ID, granteeUID string) error {
	if err := r.db.WithContext(ctx).
		Where("file_id = ? AND grantee_uid = ?", fileID, granteeUID).
		Delete(&fileShareRow{}).Error; err != nil {
		return fmt.Errorf("revoke file share: %w", err)
	}
	return nil
}

func (r *fileShareRepo) IsSharedWith(ctx context.Context, fileID domain.ID, uid string) (bool, error) {
	var n int64
	if err := r.db.WithContext(ctx).
		Model(&fileShareRow{}).
		Where("file_id = ? AND grantee_uid = ?", fileID, uid).
		Count(&n).Error; err != nil {
		return false, fmt.Errorf("is shared with: %w", err)
	}
	return n > 0, nil
}

func (r *fileShareRepo) ListFilesSharedWith(ctx context.Context, uid string, page domain.Page) ([]*File, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).
		Model(&fileRow{}).
		Joins("JOIN file_shares ON file_shares.file_id = files.id").
		Where("file_shares.grantee_uid = ?", uid).
		Order("files.created_at DESC, files.id DESC").
		Limit(page.Limit + 1)
	if page.Cursor != "" {
		q = q.Where("files.id < ?", page.Cursor)
	}
	var rows []fileRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, "", fmt.Errorf("list files shared with: %w", err)
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
