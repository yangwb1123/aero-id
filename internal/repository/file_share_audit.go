package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

// FileShareAuditRepository is the in-transaction audit surface for file
// sharing (B2-1): grant/revoke plus their audit facts commit in one gorm
// transaction.
type FileShareAuditRepository interface {
	// GrantWithAudit records read access for each grantee and appends one fact
	// per grantee in the same transaction (len(facts) == len(granteeUIDs) is a
	// service guarantee). An empty grantee list is a no-op with no facts.
	GrantWithAudit(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string, facts []*domain.AuditFact) error
	RevokeWithAudit(ctx context.Context, fileID domain.ID, granteeUID string, fact *domain.AuditFact) error
}

// GrantWithAudit inserts N grant rows and N facts atomically; a failing fact
// append rolls the whole grant back.
func (r *fileShareRepo) GrantWithAudit(ctx context.Context, fileID domain.ID, ownerUID string, granteeUIDs []string, facts []*domain.AuditFact) error {
	if len(granteeUIDs) == 0 {
		return nil
	}
	if len(facts) != len(granteeUIDs) {
		return fmt.Errorf("grant facts (%d) must match grantees (%d)", len(facts), len(granteeUIDs))
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		rows := make([]fileShareRow, 0, len(granteeUIDs))
		for _, g := range granteeUIDs {
			rows = append(rows, fileShareRow{FileID: fileID, GranteeUID: g, OwnerUID: ownerUID, CreatedAt: now})
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
			return fmt.Errorf("grant file share: %w", err)
		}
		for _, fact := range facts {
			if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
				return fmt.Errorf("append file share audit: %w", err)
			}
		}
		return nil
	})
}

// RevokeWithAudit removes the grant and appends the fact atomically.
func (r *fileShareRepo) RevokeWithAudit(ctx context.Context, fileID domain.ID, granteeUID string, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("file_id = ? AND grantee_uid = ?", fileID, granteeUID).
			Delete(&fileShareRow{}).Error; err != nil {
			return fmt.Errorf("revoke file share: %w", err)
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append file share audit: %w", err)
		}
		return nil
	})
}
