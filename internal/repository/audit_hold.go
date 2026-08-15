package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// AuditHold is one evidence-preservation order.
// (AuditHoldScope + constants live in internal/domain as pure types.)
type AuditHold struct {
	ID        domain.ID
	TenantID  string
	ScopeType domain.AuditHoldScope
	ScopeID   string
	Reason    string
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt *time.Time
}

// AuditHoldRepository persists legal holds.
type AuditHoldRepository interface {
	// PutHold creates or refreshes a hold (upsert on the unique triple).
	PutHold(ctx context.Context, hold *AuditHold) error
	// RemoveHold lifts an active hold.
	RemoveHold(ctx context.Context, tenantID string, scopeType domain.AuditHoldScope, scopeID string) error
	// ListActiveHolds returns holds that are not expired.
	ListActiveHolds(ctx context.Context, tenantID string) ([]*AuditHold, error)
	// HoldScopeIDs returns the active scope ids of one kind (used by the
	// legacy cleaner to exempt actors under hold).
	HoldScopeIDs(ctx context.Context, scopeType domain.AuditHoldScope) ([]string, error)
}

// AuditHoldAuditRepository is the in-transaction audit surface for legal
// holds. Each method appends the fact in the SAME transaction as the domain
// write: an append failure rolls the whole mutation back (never an orphaned
// ledger/publication row, never a hold without its durable fact). The
// concrete hold repo implements it; the service type-asserts (pattern:
// CallbackAuditRepository).
type AuditHoldAuditRepository interface {
	PutHoldWithAudit(ctx context.Context, hold *AuditHold, fact *domain.AuditFact) error
	RemoveHoldWithAudit(ctx context.Context, tenantID string, scopeType domain.AuditHoldScope, scopeID string, fact *domain.AuditFact) error
}

type auditHoldRow struct {
	ID        domain.ID  `gorm:"column:id;primaryKey"`
	TenantID  string     `gorm:"column:tenant_id"`
	ScopeType string     `gorm:"column:scope_type"`
	ScopeID   string     `gorm:"column:scope_id"`
	Reason    string     `gorm:"column:reason"`
	CreatedBy string     `gorm:"column:created_by"`
	CreatedAt time.Time  `gorm:"column:created_at"`
	ExpiresAt *time.Time `gorm:"column:expires_at"`
}

func (auditHoldRow) TableName() string { return "audit_holds" }

type auditHoldRepo struct {
	db    *gorm.DB
	audit AuditLedgerTransactionalRepository
}

// NewAuditHoldRepository builds the hold repo. The transactional ledger is
// mandatory: a nil or typed-nil ledger would let a hold commit without its
// durable fact, so requireAuditLedger panics at wiring time (fail-closed).
func NewAuditHoldRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) AuditHoldRepository {
	return &auditHoldRepo{db: db, audit: requireAuditLedger("NewAuditHoldRepository", audit)}
}

// PutHold creates or refreshes a hold (upsert on the unique triple) without
// an audit append (0 = disabled convention: the service only reaches this
// path when no audit service is configured).
func (r *auditHoldRepo) PutHold(ctx context.Context, hold *AuditHold) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return r.putHoldInTx(tx, hold)
	})
}

// putHoldInTx creates or refreshes the hold row. The caller's struct is
// updated with the FINAL stored id/created_at/expires_at via RETURNING: on the
// conflict path the struct's freshly minted ID and the uncommitted `now` are
// never stored, so the response must reflect the row that actually committed.
// created_at means *hold last set/refreshed at* — the upsert refreshes it by
// design (it is in DoUpdates), so a re-put reports the refresh time; id is
// stable across refreshes (never in DoUpdates). The RETURNING copy-back
// touches only ID/CreatedAt/ExpiresAt — none of which feed the audit fact — so
// the fact inputs are all known before the transaction starts.
func (r *auditHoldRepo) putHoldInTx(tx *gorm.DB, hold *AuditHold) error {
	now := time.Now().UTC()
	row := &auditHoldRow{ID: idgen.NewID(), TenantID: hold.TenantID, ScopeType: string(hold.ScopeType),
		ScopeID: hold.ScopeID, Reason: hold.Reason, CreatedBy: hold.CreatedBy, CreatedAt: now, ExpiresAt: hold.ExpiresAt}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "scope_type"}, {Name: "scope_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"reason", "created_by", "expires_at", "created_at"}),
	}, clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}, {Name: "expires_at"}}}).
		Create(row).Error; err != nil {
		return err
	}
	hold.ID = row.ID
	hold.CreatedAt = row.CreatedAt
	hold.ExpiresAt = row.ExpiresAt
	return nil
}

// PutHoldWithAudit upserts the hold and appends the audit fact in the SAME
// transaction, the append LAST: a keyless/down ledger or any append error
// rolls the hold back (the mutation never commits without its durable fact),
// and a failed domain write can never leave a phantom fact behind. The
// caller's hold struct still receives the RETURNING copy-back before the
// append, as on the plain path.
func (r *auditHoldRepo) PutHoldWithAudit(ctx context.Context, hold *AuditHold, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := r.putHoldInTx(tx, hold); err != nil {
			return err
		}
		// ErrAuditDuplicate is swallowed in-tx (contract §4.2, defensive
		// parity): a duplicate append implies the retried hold write already
		// committed with identical anchor inputs, so the retry is a
		// no-op-equivalent re-apply; the swallow runs after the domain write
		// inside the same tx, so it can never mask a failed domain write.
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append audit hold audit: %w", err)
		}
		return nil
	})
}

func (r *auditHoldRepo) RemoveHold(ctx context.Context, tenantID string, scopeType domain.AuditHoldScope, scopeID string) error {
	return r.db.WithContext(ctx).Where("tenant_id = ? AND scope_type = ? AND scope_id = ?",
		tenantID, string(scopeType), scopeID).Delete(&auditHoldRow{}).Error
}

// RemoveHoldWithAudit deletes the hold and appends the fact in the SAME
// transaction. The fact records even when 0 rows were deleted — parity with
// the plain Remove path (REQ-4.4): lifting an already-lifted hold still
// evidences the attempted lift, and the append runs after the delete inside
// the same tx, so a failed append rolls the delete back.
func (r *auditHoldRepo) RemoveHoldWithAudit(ctx context.Context, tenantID string,
	scopeType domain.AuditHoldScope, scopeID string, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id = ? AND scope_type = ? AND scope_id = ?",
			tenantID, string(scopeType), scopeID).Delete(&auditHoldRow{}).Error; err != nil {
			return err
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append audit hold audit: %w", err)
		}
		return nil
	})
}

func (r *auditHoldRepo) ListActiveHolds(ctx context.Context, tenantID string) ([]*AuditHold, error) {
	var rows []auditHoldRow
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND (expires_at IS NULL OR expires_at > ?)", tenantID, time.Now().UTC()).
		Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*AuditHold, 0, len(rows))
	for i := range rows {
		out = append(out, auditHoldRowToDomain(&rows[i]))
	}
	return out, nil
}

func (r *auditHoldRepo) HoldScopeIDs(ctx context.Context, scopeType domain.AuditHoldScope) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Model(&auditHoldRow{}).
		Where("scope_type = ? AND (expires_at IS NULL OR expires_at > ?)", string(scopeType), time.Now().UTC()).
		Pluck("scope_id", &ids).Error
	return ids, err
}

func auditHoldRowToDomain(row *auditHoldRow) *AuditHold {
	return &AuditHold{ID: row.ID, TenantID: row.TenantID, ScopeType: domain.AuditHoldScope(row.ScopeType),
		ScopeID: row.ScopeID, Reason: row.Reason, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt}
}
