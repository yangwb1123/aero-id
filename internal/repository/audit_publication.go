package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

type auditPublicationRow struct {
	ID            uuid.UUID  `gorm:"column:id;primaryKey"`
	FactID        uuid.UUID  `gorm:"column:fact_id"`
	Status        string     `gorm:"column:status"`
	Attempt       int        `gorm:"column:attempt"`
	LeaseOwner    string     `gorm:"column:lease_owner"`
	LeaseUntil    *time.Time `gorm:"column:lease_until"`
	NextAttemptAt time.Time  `gorm:"column:next_attempt_at"`
	ErrorCode     string     `gorm:"column:error_code"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
}

func (auditPublicationRow) TableName() string { return "audit_publications" }

func (auditLedgerRow) TableName() string { return "audit_ledger" }

// ClaimPublication leases due rows to one worker (FOR UPDATE SKIP LOCKED;
// pending-and-due plus expired-lease delivering legs). It deliberately does
// NOT touch attempt: incrementing at claim time would inflate the counter on
// lease-expiry re-claims that never published (crash after claim, saturated
// bulkhead, hydration failure) and could dead-letter a healthy row before any
// real publish (M-4). The attempt is persisted by FinishPublication, which the
// drain calls only after an actual publish outcome is computed.
//
// All lease math is DB-clock-only (M-1): the predicate and the lease write
// both use now(), so cross-instance clock skew cannot re-claim rows still
// actively held by a slow-skewed instance, and a backward NTP step cannot
// stall claims. next_attempt_at is written by the application clock (scheduling
// only, never a fencing decision), so mixed-clock comparison there only shifts
// due time, never correctness.
func (r *auditLedgerRepo) ClaimPublication(ctx context.Context, worker string, lease time.Duration, limit int) ([]*domain.AuditPublication, error) {
	if limit <= 0 {
		limit = 50
	}
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	rows := make([]auditPublicationRow, 0, limit)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Where("((status = 'pending' AND next_attempt_at <= now()) OR (status = 'delivering' AND lease_until < now()))").
			Order("next_attempt_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			if err := tx.Model(&auditPublicationRow{}).Where("id = ?", rows[i].ID).Updates(map[string]any{
				"status": "delivering", "lease_owner": worker,
				// M-1: the lease window is written and compared in DB time; the
				// app clock never participates in the fencing decision.
				"lease_until": gorm.Expr("now() + make_interval(secs => ?)", lease.Seconds()),
				"updated_at":  time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
			rows[i].Status = "delivering"
			rows[i].LeaseOwner = worker
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.hydratePublications(ctx, rows, lease)
}

// hydratePublications loads the ledger facts for claimed rows AFTER the claim
// tx committed (FM-1): a transient failure partway returns the hydrated
// prefix alongside ErrAuditPublicationFactHydration — the drain delivers the
// prefix now and the remainder re-claims after lease expiry. Attempt counters
// are untouched (M-4), so no premature dead_letter. The persisted lease_until
// is DB-clock; the returned struct carries an app-clock approximation for
// tests/logs only — the drain never makes fencing decisions from it (finish
// fences on (id, status, lease_owner)).
func (r *auditLedgerRepo) hydratePublications(ctx context.Context, rows []auditPublicationRow, lease time.Duration) ([]*domain.AuditPublication, error) {
	until := time.Now().UTC().Add(lease)
	result := make([]*domain.AuditPublication, 0, len(rows))
	for i := range rows {
		rows[i].LeaseUntil = &until
		var factRow auditLedgerRow
		if err := r.db.WithContext(ctx).Where("id = ?", rows[i].FactID).First(&factRow).Error; err != nil {
			// FM-12: the claim tx already committed (every row in the batch is
			// delivering), but post-commit fact hydration failed partway. Return
			// the hydrated prefix alongside the wrapped error: the drain delivers
			// the prefix now (phase=fact_hydration on the claim-failed metric)
			// and the remainder is re-claimed after lease expiry — the attempt
			// counter is untouched (M-4), so no premature dead_letter.
			return result, fmt.Errorf("%w: fact %s: %w", ErrAuditPublicationFactHydration, rows[i].FactID, err)
		}
		result = append(result, &domain.AuditPublication{ID: rows[i].ID, Fact: auditLedgerRowToFact(&factRow), Status: rows[i].Status,
			Attempt: rows[i].Attempt, LeaseOwner: rows[i].LeaseOwner, LeaseUntil: rows[i].LeaseUntil,
			NextAttemptAt: rows[i].NextAttemptAt, ErrorCode: rows[i].ErrorCode})
	}
	return result, nil
}

func (r *auditLedgerRepo) FinishPublication(ctx context.Context, id domain.ID, worker string, attempt int, status, errorCode string, next time.Time) error {
	result := r.db.WithContext(ctx).Model(&auditPublicationRow{}).Where("id = ? AND status = 'delivering' AND lease_owner = ?", id, worker).
		Updates(map[string]any{"status": status, "attempt": attempt, "error_code": errorCode, "next_attempt_at": next,
			"lease_owner": "", "lease_until": nil, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAuditPublicationLeaseLost
	}
	return nil
}
