package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

// AccountListFilter is the repository-owned filter set for administrative
// account queries. It deliberately contains no transport types.
type AccountListFilter struct {
	TenantID   string
	Query      string
	Status     string
	HomeRegion string
	Page       domain.Page
}

// AccountRepository persists canonical account control-plane data.
type AccountRepository interface {
	EnsureByIdentity(ctx context.Context, tenantID, issuer, subject, canonicalUID, homeRegion string) (*domain.Account, error)
	FindByIdentity(ctx context.Context, tenantID, issuer, subject string) (*domain.Account, error)
	GetByID(ctx context.Context, tenantID string, id domain.ID) (*domain.Account, error)
	GetProfile(ctx context.Context, id domain.ID) (*domain.AccountProfile, error)
	EraseAccount(ctx context.Context, tenantID string, id domain.ID) error
	UpdateProfile(ctx context.Context, profile *domain.AccountProfile) error
	ListSources(ctx context.Context, accountID domain.ID, page domain.Page) ([]*domain.AccountSource, string, error)
	ListMemberships(ctx context.Context, accountID domain.ID, page domain.Page) ([]*domain.AccountMembership, string, error)
	UpsertSource(ctx context.Context, source *domain.AccountSource) error
	UpsertMembership(ctx context.Context, membership *domain.AccountMembership) error
	ListAccounts(ctx context.Context, filter AccountListFilter) ([]*domain.Account, string, error)
}

type AccountProfileAuditRepository interface {
	UpdateProfileWithAudit(context.Context, *domain.AccountProfile, *domain.AuditFact) error
}

// SyncJobLeaseAccountRepository binds local erasure to the worker claim. It is
// optional on the broad account port so existing read-only fakes remain small.
type SyncJobLeaseAccountRepository interface {
	EraseAccountWithSyncJobLease(context.Context, string, domain.ID, domain.ID, string) error
}

type accountRepo struct {
	db    *gorm.DB
	audit AuditLedgerTransactionalRepository
}

func NewAccountRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) AccountRepository {
	return &accountRepo{db: db, audit: requireAuditLedger("NewAccountRepository", audit)}
}

type accountRow struct {
	ID           uuid.UUID `gorm:"column:id;primaryKey"`
	TenantID     string    `gorm:"column:tenant_id"`
	CanonicalUID string    `gorm:"column:canonical_uid"`
	HomeRegion   string    `gorm:"column:home_region"`
	Status       string    `gorm:"column:status"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

func (accountRow) TableName() string { return "accounts" }

func accountRowToDomain(row *accountRow) *domain.Account {
	return &domain.Account{
		ID:           row.ID,
		TenantID:     row.TenantID,
		CanonicalUID: row.CanonicalUID,
		HomeRegion:   row.HomeRegion,
		Status:       domain.AccountStatus(row.Status),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

type accountIdentityRow struct {
	ID           uuid.UUID `gorm:"column:id;primaryKey"`
	AccountID    uuid.UUID `gorm:"column:account_id"`
	TenantID     string    `gorm:"column:tenant_id"`
	Issuer       string    `gorm:"column:issuer"`
	Subject      string    `gorm:"column:subject"`
	CanonicalUID string    `gorm:"column:canonical_uid"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	LastSeenAt   time.Time `gorm:"column:last_seen_at"`
}

func (accountIdentityRow) TableName() string { return "account_identities" }

func (r *accountRepo) GetByID(ctx context.Context, tenantID string, id domain.ID) (*domain.Account, error) {
	var row accountRow
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).First(&row).Error; err != nil {
		return nil, err
	}
	return accountRowToDomain(&row), nil
}

func (r *accountRepo) FindByIdentity(ctx context.Context, tenantID, issuer, subject string) (*domain.Account, error) {
	var identity accountIdentityRow
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND issuer = ? AND subject = ?", tenantID, issuer, subject).First(&identity).Error; err != nil {
		return nil, err
	}
	return r.GetByID(ctx, tenantID, identity.AccountID)
}

func (r *accountRepo) EraseAccount(ctx context.Context, tenantID string, id domain.ID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return eraseAccountTx(tx, tenantID, id)
	})
}

func (r *accountRepo) EraseAccountWithSyncJobLease(ctx context.Context, tenantID string, id, jobID domain.ID, worker string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		account, err := lockAccountDispositionTx(tx, id)
		if err != nil {
			return err
		}
		if account.TenantID != tenantID {
			return gorm.ErrRecordNotFound
		}
		var lease struct{ ID uuid.UUID }
		locked := tx.Table("sync_jobs").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND account_id = ? AND tenant_id = ? AND status = ? AND lease_owner = ? AND lease_until > clock_timestamp()", jobID,
				id, tenantID, string(domain.SyncJobRunning), worker).First(&lease)
		if errors.Is(locked.Error, gorm.ErrRecordNotFound) {
			return ErrSyncJobLeaseLost
		}
		if locked.Error != nil {
			return locked.Error
		}
		return eraseAccountTx(tx, tenantID, id)
	})
}

func eraseAccountTx(tx *gorm.DB, tenantID string, id domain.ID) error {
	account, err := lockAccountDispositionTx(tx, id)
	if err != nil {
		return err
	}
	if account.TenantID != tenantID {
		return gorm.ErrRecordNotFound
	}
	if account.Status == string(domain.AccountErased) {
		return nil
	}
	now := time.Now().UTC()
	marker := "erased:" + id.String()
	if err := tx.Model(&accountRow{}).Where("id = ?", id).Updates(map[string]any{
		"status": string(domain.AccountErased), "canonical_uid": marker, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if err := tx.Model(&accountIdentityRow{}).Where("account_id = ?", id).
		Update("canonical_uid", marker).Error; err != nil {
		return err
	}
	if err := tx.Model(&accountProfileRow{}).Where("account_id = ?", id).Updates(map[string]any{
		"display_name": "", "avatar_url": "", "locale": "", "timezone": "", "preferences": datatypes.JSONMap{}, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if err := eraseAccountReferencesTx(tx, id, now); err != nil {
		return err
	}
	return eraseAccountSnapshotsTx(tx, id, now)
}

func (r *accountRepo) ListAccounts(ctx context.Context, filter AccountListFilter) ([]*domain.Account, string, error) {
	filter.Page.Normalize()
	q := r.db.WithContext(ctx).Model(&accountRow{}).Where("tenant_id = ?", filter.TenantID).Order("id DESC")
	if filter.Query != "" {
		q = q.Where("canonical_uid ILIKE ?", "%"+filter.Query+"%")
	}
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	if filter.HomeRegion != "" {
		q = q.Where("home_region = ?", filter.HomeRegion)
	}
	if filter.Page.Cursor != "" {
		q = q.Where("id < ?", filter.Page.Cursor)
	}
	var rows []accountRow
	if err := q.Limit(filter.Page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > filter.Page.Limit {
		next = rows[filter.Page.Limit-1].ID.String()
		rows = rows[:filter.Page.Limit]
	}
	out := make([]*domain.Account, len(rows))
	for i := range rows {
		out[i] = accountRowToDomain(&rows[i])
	}
	return out, next, nil
}
