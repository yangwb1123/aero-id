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

// BlockAuditRepository is the in-transaction audit surface for user-block
// mutations (B2-1): Block/Unblock plus their audit facts commit in one gorm
// transaction.
type BlockAuditRepository interface {
	BlockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error
	UnblockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error
}

// BlockWithAudit records the block and appends the fact atomically. The fact
// is unconditional (legacy Log parity: a re-block no-op still records).
func (r *blockRepo) BlockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := userBlockRow{BlockerUID: blocker, BlockedUID: blocked, CreatedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("create user block: %w", err)
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append block audit: %w", err)
		}
		return nil
	})
}

// UnblockWithAudit removes the block pair and appends the fact atomically.
func (r *blockRepo) UnblockWithAudit(ctx context.Context, blocker, blocked string, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("blocker_uid = ? AND blocked_uid = ?", blocker, blocked).
			Delete(&userBlockRow{}).Error; err != nil {
			return fmt.Errorf("delete user block: %w", err)
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append block audit: %w", err)
		}
		return nil
	})
}
