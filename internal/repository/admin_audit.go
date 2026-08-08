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

// AdminAuditRepository is the in-transaction audit surface for role
// administration. Each method commits the domain write and its audit fact in
// one gorm transaction: an append failure rolls the whole mutation back (never
// an orphaned ledger row, never a mutation without its fact).
type AdminAuditRepository interface {
	GrantRoleWithAudit(ctx context.Context, uid, roleCode, grantedBy string, fact *domain.AuditFact) error
	RevokeRoleWithAudit(ctx context.Context, uid, roleCode string, fact *domain.AuditFact) error
}

// GrantRoleWithAudit grants role_code to uid and appends the fact atomically.
// The unconditional fact append matches the legacy Log-after-call placement
// (facts record even when the ON CONFLICT DO NOTHING made the grant a no-op).
// ErrAuditDuplicate folds in-tx only — after the domain write, so it can never
// mask a failed grant.
func (r *adminRepo) GrantRoleWithAudit(ctx context.Context, uid, roleCode, grantedBy string, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := &userRoleRow{UID: uid, RoleCode: roleCode, GrantedBy: grantedBy, CreatedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
			return fmt.Errorf("grant role: %w", err)
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append admin audit: %w", err)
		}
		return nil
	})
}

// RevokeRoleWithAudit revokes role_code from uid and appends the fact
// atomically. A revoke of an absent role is a 0-row no-op but still records
// the fact (legacy Log parity).
func (r *adminRepo) RevokeRoleWithAudit(ctx context.Context, uid, roleCode string, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("uid = ? AND role_code = ?", uid, roleCode).Delete(&userRoleRow{})
		if res.Error != nil {
			return fmt.Errorf("revoke role: %w", res.Error)
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append admin audit: %w", err)
		}
		return nil
	})
}
