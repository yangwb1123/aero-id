package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

// FileShareRepository persists explicit per-user grants that let uids other than
// the owner read a private file.
type FileShareRepository interface {
	// Grant records read access for each granteeUID on fileID, attributing the grant
	// to ownerUID. It is idempotent: re-granting an existing (file, grantee) pair is a
	// no-op (ON CONFLICT DO NOTHING). An empty granteeUIDs slice is a no-op.
	Grant(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string) error
	// Revoke removes a single (file, grantee) grant. Revoking a non-existent grant is a no-op.
	Revoke(ctx context.Context, fileID domain.ID, granteeUID string) error
	// ListGrantees returns the uids currently granted access to fileID.
	ListGrantees(ctx context.Context, fileID domain.ID) ([]string, error)
	// ListFilesSharedWith returns live files shared with uid, newest first, paginated by
	// the file id cursor (same convention as ListByOwner).
	ListFilesSharedWith(ctx context.Context, uid string, page domain.Page) ([]*domain.File, string, error)
	// IsSharedWith reports whether fileID has been shared with uid.
	IsSharedWith(ctx context.Context, fileID domain.ID, uid string) (bool, error)
}

type fileShareRepo struct{ db *gorm.DB }

func NewFileShareRepository(db *gorm.DB) FileShareRepository { return &fileShareRepo{db: db} }

func (r *fileShareRepo) Grant(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string) error {
	if len(granteeUIDs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]fileShareRow, 0, len(granteeUIDs))
	for _, g := range granteeUIDs {
		rows = append(rows, fileShareRow{FileID: fileID, GranteeUID: g, OwnerUID: ownerUID, CreatedAt: now})
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

func (r *fileShareRepo) ListGrantees(ctx context.Context, fileID domain.ID) ([]string, error) {
	var uids []string
	if err := r.db.WithContext(ctx).
		Model(&fileShareRow{}).
		Where("file_id = ?", fileID).
		Order("created_at DESC").
		Pluck("grantee_uid", &uids).Error; err != nil {
		return nil, fmt.Errorf("list grantees: %w", err)
	}
	return uids, nil
}

func (r *fileShareRepo) ListFilesSharedWith(ctx context.Context, uid string, page domain.Page) ([]*domain.File, string, error) {
	page.Normalize()
	q := r.db.WithContext(ctx).
		Model(&fileRow{}).
		Joins("JOIN file_shares ON file_shares.file_id = files.id").
		Where("file_shares.grantee_uid = ?", uid).
		Order("files.created_at DESC, files.id DESC").
		Limit(page.Limit + 1) // fetch one extra to detect "has more"
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
	out := make([]*domain.File, len(rows))
	for i := range rows {
		out[i] = fileRowToDomain(&rows[i])
	}
	return out, nextCursor, nil
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
