package repository

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

// AgentMembershipProjectionRepository exposes only the read required by the
// Hub's bounded-freshness membership gate.
type AgentMembershipProjectionRepository interface {
	HasFreshActiveMembership(context.Context, string, domain.ID, string, string, string, time.Time, time.Time) (bool, error)
}

func (r *accountRepo) HasFreshActiveMembership(ctx context.Context, tenantID string,
	accountID domain.ID, source, scopeType, scopeID string, freshAfter, checkedAt time.Time) (bool, error) {
	var eligible bool
	err := r.db.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM accounts AS account
			JOIN account_memberships AS membership ON membership.account_id = account.id
			WHERE account.tenant_id = ? AND account.id = ? AND account.status = 'active'
			  AND membership.source = ? AND membership.scope_type = ? AND membership.scope_id = ?
			  AND membership.status = 'active' AND membership.error_code = ''
			  AND membership.last_synced_at IS NOT NULL
			  AND membership.last_synced_at >= ? AND membership.last_synced_at <= ?
		)`, tenantID, accountID, source, scopeType, scopeID, freshAfter, checkedAt).Scan(&eligible).Error
	return eligible, err
}
