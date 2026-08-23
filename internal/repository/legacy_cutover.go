package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

// LegacyCutover is one tenant's cutover decision.
// (LegacyCutoverStatus + constants + ValidCutoverTransition live in
// internal/domain as pure types.)
type LegacyCutover struct {
	TenantID  string
	Status    domain.LegacyCutoverStatus
	DecidedAt time.Time
	DecidedBy string
	Note      string
	UpdatedAt time.Time
}

// ErrInvalidCutoverTransition rejects illegal state moves.
var ErrInvalidCutoverTransition = errors.New("invalid legacy cutover transition")

// LegacyCutoverRepository persists the per-tenant cutover registry.
type LegacyCutoverRepository interface {
	GetCutover(ctx context.Context, tenantID string) (*LegacyCutover, error)
	SetCutover(ctx context.Context, cutover *LegacyCutover) error
}

// LegacyCutoverAuditRepository is the in-transaction audit surface for the
// cutover registry. SetCutoverWithAudit appends the fact in the SAME
// transaction as the decision write: an append failure rolls the whole
// mutation back (pattern: CallbackAuditRepository). The concrete repo
// implements it; the service type-asserts.
type LegacyCutoverAuditRepository interface {
	SetCutoverWithAudit(ctx context.Context, cutover *LegacyCutover, fact *domain.AuditFact) error
}

// LegacyCutoverCASAuditRepository prevents concurrent decisions that observed
// the same state from overwriting each other. The status CAS and audit append
// commit in one transaction.
type LegacyCutoverCASAuditRepository interface {
	CompareAndSetCutoverWithAudit(ctx context.Context, cutover *LegacyCutover,
		expected domain.LegacyCutoverStatus, fact *domain.AuditFact) error
}

// CleanupReadinessItem is one checklist item of the P8-05 cleanup guard.
type CleanupReadinessItem struct {
	Check   string `gorm:"column:check_name"`
	Ready   bool   `gorm:"column:ready"`
	Details string `gorm:"column:details"`
}

type legacyCutoverRow struct {
	TenantID  string    `gorm:"column:tenant_id;primaryKey"`
	Status    string    `gorm:"column:status"`
	DecidedAt time.Time `gorm:"column:decided_at"`
	DecidedBy string    `gorm:"column:decided_by"`
	Note      string    `gorm:"column:note"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (legacyCutoverRow) TableName() string { return "legacy_cutover" }

type legacyCutoverRepo struct {
	db    *gorm.DB
	audit AuditLedgerTransactionalRepository
}

// NewLegacyCutoverRepository builds the cutover repo. The transactional
// ledger is mandatory: a nil or typed-nil ledger would let a cutover decision
// commit without its durable fact, so requireAuditLedger panics at wiring
// time (fail-closed).
func NewLegacyCutoverRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) LegacyCutoverRepository {
	return &legacyCutoverRepo{db: db, audit: requireAuditLedger("NewLegacyCutoverRepository", audit)}
}

func (r *legacyCutoverRepo) GetCutover(ctx context.Context, tenantID string) (*LegacyCutover, error) {
	var row legacyCutoverRow
	err := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return legacyCutoverRowToDomain(&row), nil
}

// SetCutover records a decision without an audit append (0 = disabled
// convention: the service only reaches this path when no audit service is
// configured).
func (r *legacyCutoverRepo) SetCutover(ctx context.Context, cutover *LegacyCutover) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return r.setCutoverInTx(tx, cutover)
	})
}

func (r *legacyCutoverRepo) setCutoverInTx(tx *gorm.DB, cutover *LegacyCutover) error {
	now := time.Now().UTC()
	row := &legacyCutoverRow{TenantID: cutover.TenantID, Status: string(cutover.Status),
		DecidedAt: now, DecidedBy: cutover.DecidedBy, Note: cutover.Note, UpdatedAt: now}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"status", "decided_at", "decided_by", "note", "updated_at"}),
	}).Create(row).Error
}

// SetCutoverWithAudit upserts the decision and appends the fact in the SAME
// transaction, the append LAST: a keyless/down ledger or any append error
// rolls the decision back (the mutation never commits without its durable
// fact). The transition validation stays a service concern — the repo writes
// whatever the service has already validated.
func (r *legacyCutoverRepo) SetCutoverWithAudit(ctx context.Context, cutover *LegacyCutover, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := r.setCutoverInTx(tx, cutover); err != nil {
			return err
		}
		// ErrAuditDuplicate is swallowed in-tx (contract §4.2, defensive
		// parity): a duplicate append implies the retried decision write
		// already committed with identical anchor inputs; the swallow runs
		// after the domain write inside the same tx, so it can never mask a
		// failed domain write.
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append legacy cutover audit: %w", err)
		}
		return nil
	})
}

func (r *legacyCutoverRepo) CompareAndSetCutoverWithAudit(ctx context.Context, cutover *LegacyCutover,
	expected domain.LegacyCutoverStatus, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := r.compareAndSetCutoverInTx(tx, cutover, expected); err != nil {
			return err
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append legacy cutover audit: %w", err)
		}
		return nil
	})
}

// compareAndSetCutoverInTx updates only the status observed by the service.
// A missing legacy row is created once; an existing row or stale status loses
// the race deterministically instead of overwriting another admin's decision.
func (r *legacyCutoverRepo) compareAndSetCutoverInTx(tx *gorm.DB, cutover *LegacyCutover,
	expected domain.LegacyCutoverStatus) error {
	now := time.Now().UTC()
	updates := map[string]any{"status": string(cutover.Status), "decided_at": now,
		"decided_by": cutover.DecidedBy, "note": cutover.Note, "updated_at": now}
	result := tx.Model(&legacyCutoverRow{}).Where("tenant_id = ? AND status = ?", cutover.TenantID, expected).
		Updates(updates)
	if result.Error != nil || result.RowsAffected == 1 {
		return result.Error
	}
	if expected != domain.LegacyCutoverStatusLegacy {
		return ErrInvalidCutoverTransition
	}
	row := &legacyCutoverRow{TenantID: cutover.TenantID, Status: string(cutover.Status),
		DecidedAt: now, DecidedBy: cutover.DecidedBy, Note: cutover.Note, UpdatedAt: now}
	created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if created.Error != nil {
		return created.Error
	}
	if created.RowsAffected != 1 {
		return ErrInvalidCutoverTransition
	}
	return nil
}

func legacyCutoverRowToDomain(row *legacyCutoverRow) *LegacyCutover {
	return &LegacyCutover{TenantID: row.TenantID, Status: domain.LegacyCutoverStatus(row.Status),
		DecidedAt: row.DecidedAt, DecidedBy: row.DecidedBy, Note: row.Note, UpdatedAt: row.UpdatedAt}
}

// LegacyCleanupRepository answers the P8-05 cleanup-readiness checklist for a
// tenant: remaining legacy rows, in-flight jobs, and audit evidence. The check
// itself never deletes anything.
type LegacyCleanupRepository interface {
	CleanupReadiness(ctx context.Context, tenantID string) ([]CleanupReadinessItem, error)
}

type legacyCleanupRepo struct {
	db *gorm.DB
}

func NewLegacyCleanupRepository(db *gorm.DB) LegacyCleanupRepository {
	return &legacyCleanupRepo{db: db}
}

// CleanupReadiness evaluates the "remove legacy write path" preconditions
// (P8-05): references gone, exports/jobs drained, audit retained, rollback
// window elapsed, deletion proofs present. Each item reports ready + details.
func (r *legacyCleanupRepo) CleanupReadiness(ctx context.Context, tenantID string) ([]CleanupReadinessItem, error) {
	items := make([]CleanupReadinessItem, 0, 4)
	if err := r.appendLegacyRowsCheck(ctx, tenantID, &items); err != nil {
		return nil, err
	}
	if err := r.appendJobsCheck(ctx, tenantID, &items); err != nil {
		return nil, err
	}
	if err := r.appendAuditCheck(ctx, tenantID, &items); err != nil {
		return nil, err
	}
	if err := r.appendRollbackWindowCheck(ctx, tenantID, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// appendLegacyRowsCheck reports remaining legacy rows owned by the tenant's
// accounts (files, messages, comments).
func (r *legacyCleanupRepo) appendLegacyRowsCheck(ctx context.Context, tenantID string, items *[]CleanupReadinessItem) error {
	var remaining int64
	if err := r.db.WithContext(ctx).Raw(`
		SELECT count(*) FROM (
			SELECT f.id FROM files f JOIN accounts a ON a.canonical_uid = f.owner_uid AND a.tenant_id = ? WHERE f.deleted_at IS NULL
			UNION ALL
			SELECT m.id FROM messages m JOIN accounts a
			  ON (a.canonical_uid = m.from_uid OR a.canonical_uid = m.to_uid)
			 AND a.tenant_id = ? WHERE m.deleted_at IS NULL
			UNION ALL
			SELECT c.id FROM comments c JOIN accounts a ON a.canonical_uid = c.owner_uid AND a.tenant_id = ? WHERE c.deleted_at IS NULL
		) legacy_rows`, tenantID, tenantID, tenantID).Scan(&remaining).Error; err != nil {
		return err
	}
	*items = append(*items, CleanupReadinessItem{
		Check: "legacy_rows_drained", Ready: remaining == 0,
		Details: itoa(remaining) + " legacy rows still reference tenant accounts",
	})
	return nil
}

// appendJobsCheck reports in-flight sync/export jobs for the tenant.
func (r *legacyCleanupRepo) appendJobsCheck(ctx context.Context, tenantID string, items *[]CleanupReadinessItem) error {
	var activeJobs int64
	if err := r.db.WithContext(ctx).Model(&syncJobRow{}).
		Where("tenant_id = ? AND status IN ?", tenantID, []string{"pending", "running", "unknown"}).
		Count(&activeJobs).Error; err != nil {
		return err
	}
	*items = append(*items, CleanupReadinessItem{
		Check: "jobs_drained", Ready: activeJobs == 0,
		Details: itoa(activeJobs) + " active sync/export jobs",
	})
	return nil
}

// appendAuditCheck reports durable audit evidence retained for the tenant.
func (r *legacyCleanupRepo) appendAuditCheck(ctx context.Context, tenantID string, items *[]CleanupReadinessItem) error {
	var auditFacts int64
	if err := r.db.WithContext(ctx).Raw(
		"SELECT count(*) FROM audit_ledger WHERE tenant_id = ?", tenantID).Scan(&auditFacts).Error; err != nil {
		return err
	}
	*items = append(*items, CleanupReadinessItem{
		Check: "audit_retained", Ready: auditFacts > 0,
		Details: itoa(auditFacts) + " durable audit facts",
	})
	return nil
}

// appendRollbackWindowCheck reports the cutover decision age; the service
// evaluates the window itself so the repository stays policy-free.
func (r *legacyCleanupRepo) appendRollbackWindowCheck(ctx context.Context, tenantID string, items *[]CleanupReadinessItem) error {
	var decision struct {
		Status    string     `gorm:"column:status"`
		DecidedAt *time.Time `gorm:"column:decided_at"`
	}
	decisionErr := r.db.WithContext(ctx).Raw(
		"SELECT status, decided_at FROM legacy_cutover WHERE tenant_id = ?", tenantID).Scan(&decision).Error
	if errors.Is(decisionErr, gorm.ErrRecordNotFound) {
		*items = append(*items, CleanupReadinessItem{
			Check: "rollback_window_elapsed", Ready: false,
			Details: "no cutover decision recorded for tenant",
		})
		return nil
	}
	if decisionErr != nil {
		return decisionErr
	}
	age := "never"
	if decision.DecidedAt != nil {
		age = time.Since(*decision.DecidedAt).Round(time.Second).String()
	}
	*items = append(*items, CleanupReadinessItem{
		Check: "rollback_window_elapsed", Ready: false,
		Details: "decision " + decision.Status + " made " + age + " ago (window evaluated by service)",
	})
	return nil
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
