package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

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
		if result := tx.Model(&accountRow{}).Where("tenant_id = ? AND id = ?", tenantID, id).Updates(map[string]any{
			"status": string(domain.AccountErased), "canonical_uid": "erased:" + id.String(), "updated_at": time.Now().UTC(),
		}); result.Error != nil {
			return result.Error
		} else if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Model(&accountProfileRow{}).Where("account_id = ?", id).Updates(map[string]any{
			"display_name": "", "avatar_url": "", "locale": "", "preferences": datatypes.JSONMap{}, "updated_at": time.Now().UTC(),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&accountSnapshotRow{}).Where("account_id = ?", id).Updates(map[string]any{
			"status": string(domain.SnapshotUnavailable), "data": datatypes.JSONMap{}, "error_code": "account.erased", "updated_at": time.Now().UTC(),
		}).Error
	})
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
