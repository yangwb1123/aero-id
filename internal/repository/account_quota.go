package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

const (
	accountQuotaPending   = "pending"
	accountQuotaCommitted = "committed"
)

type AccountQuotaRepository interface {
	EnsureByIdentityWithQuota(context.Context, string, string, string, string, string, string) (*domain.Account, error)
	PendingAccountQuota(context.Context, string, domain.ID) (string, error)
	MarkAccountQuotaCommitted(context.Context, string, domain.ID, string) error
}

type accountQuotaRow struct {
	AccountID     uuid.UUID `gorm:"column:account_id;primaryKey"`
	TenantID      string    `gorm:"column:tenant_id"`
	ReservationID string    `gorm:"column:reservation_id;uniqueIndex"`
	Status        string    `gorm:"column:status"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at"`
}

func (accountQuotaRow) TableName() string { return "account_quota_reservations" }

func (r *accountRepo) EnsureByIdentityWithQuota(ctx context.Context, tenantID, issuer, subject,
	canonicalUID, homeRegion, reservationID string) (*domain.Account, error) {
	input, err := normalizeAccountIdentity(tenantID, issuer, subject, canonicalUID, homeRegion)
	reservationID = strings.TrimSpace(reservationID)
	if err != nil || reservationID == "" {
		return nil, fmt.Errorf("account quota identity fields must not be blank")
	}
	var result *domain.Account
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result, err = ensureAccountIdentityTx(tx, input)
		if err != nil {
			return err
		}
		return ensureAccountQuotaTx(tx, input.tenantID, result.ID, reservationID)
	})
	return result, err
}

func ensureAccountQuotaTx(tx *gorm.DB, tenantID string, accountID domain.ID, reservationID string) error {
	now := time.Now().UTC()
	row := &accountQuotaRow{AccountID: accountID, TenantID: tenantID, ReservationID: reservationID,
		Status: accountQuotaPending, CreatedAt: now, UpdatedAt: now}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
		return fmt.Errorf("create account quota reservation: %w", err)
	}
	var existing accountQuotaRow
	if err := tx.Where("account_id = ?", accountID).First(&existing).Error; err != nil {
		return fmt.Errorf("load account quota reservation: %w", err)
	}
	if existing.TenantID != tenantID || existing.ReservationID != reservationID {
		return fmt.Errorf("account quota reservation mismatch")
	}
	return nil
}

func (r *accountRepo) PendingAccountQuota(ctx context.Context, tenantID string, accountID domain.ID) (string, error) {
	var row accountQuotaRow
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ? AND status = ?",
		tenantID, accountID, accountQuotaPending).First(&row).Error
	if err != nil {
		return "", err
	}
	return row.ReservationID, nil
}

func (r *accountRepo) MarkAccountQuotaCommitted(ctx context.Context, tenantID string,
	accountID domain.ID, reservationID string) error {
	result := r.db.WithContext(ctx).Model(&accountQuotaRow{}).
		Where("tenant_id = ? AND account_id = ? AND reservation_id = ? AND status = ?",
			tenantID, accountID, reservationID, accountQuotaPending).
		Updates(map[string]any{"status": accountQuotaCommitted, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var row accountQuotaRow
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ? AND reservation_id = ?",
		tenantID, accountID, reservationID).First(&row).Error
	if err == nil && row.Status == accountQuotaCommitted {
		return nil
	}
	return fmt.Errorf("account quota reservation state conflict")
}

var _ AccountQuotaRepository = (*accountRepo)(nil)
