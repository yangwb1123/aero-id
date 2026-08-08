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

// BanAuditRepository is the in-transaction audit surface for ban lifecycle
// mutations (B2-1): Ban/Unban plus their audit facts commit in one gorm
// transaction.
type BanAuditRepository interface {
	BanWithAudit(ctx context.Context, uid, actorUID, reason string, expiresAt *time.Time, fact *domain.AuditFact) error
	UnbanWithAudit(ctx context.Context, uid string, fact *domain.AuditFact) error
}

// BanWithAudit upserts the ban row and appends the fact atomically. The fact
// is unconditional (legacy Log parity: a re-ban overwrite still records).
func (r *banRepo) BanWithAudit(ctx context.Context, uid, actorUID, reason string, expiresAt *time.Time, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := &userBanRow{UID: uid, ActorUID: actorUID, Reason: reason, ExpiresAt: expiresAt, CreatedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(row).Error; err != nil {
			return fmt.Errorf("ban user: %w", err)
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append ban audit: %w", err)
		}
		return nil
	})
}

// UnbanWithAudit soft-deletes the ban row and appends the fact atomically.
func (r *banRepo) UnbanWithAudit(ctx context.Context, uid string, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("uid = ?", uid).Delete(&userBanRow{}).Error; err != nil {
			return fmt.Errorf("unban user: %w", err)
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append ban audit: %w", err)
		}
		return nil
	})
}
