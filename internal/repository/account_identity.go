package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type accountIdentityInput struct {
	tenantID, issuer, subject, canonicalUID, homeRegion string
}

func (r *accountRepo) EnsureByIdentity(ctx context.Context, tenantID, issuer, subject,
	canonicalUID, homeRegion string) (*domain.Account, error) {
	input, err := normalizeAccountIdentity(tenantID, issuer, subject, canonicalUID, homeRegion)
	if err != nil {
		return nil, err
	}
	var result *domain.Account
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result, err = ensureAccountIdentityTx(tx, input)
		return err
	})
	return result, err
}

func normalizeAccountIdentity(tenantID, issuer, subject, canonicalUID,
	homeRegion string) (accountIdentityInput, error) {
	input := accountIdentityInput{strings.TrimSpace(tenantID), strings.TrimSpace(issuer), strings.TrimSpace(subject),
		strings.TrimSpace(canonicalUID), strings.TrimSpace(homeRegion)}
	if input.tenantID == "" || input.issuer == "" || input.subject == "" || input.canonicalUID == "" || input.homeRegion == "" {
		return accountIdentityInput{}, fmt.Errorf("account identity fields must not be blank")
	}
	return input, nil
}

func ensureAccountIdentityTx(tx *gorm.DB, input accountIdentityInput) (*domain.Account, error) {
	now := time.Now().UTC()
	createdID := idgen.NewID()
	if err := createAccountCandidate(tx, input, createdID, now); err != nil {
		return nil, err
	}
	resultID, err := bindAccountIdentity(tx, input, createdID, now)
	if err != nil {
		return nil, err
	}
	var row accountRow
	if err := tx.Where("id = ?", resultID).First(&row).Error; err != nil {
		return nil, fmt.Errorf("load ensured account: %w", err)
	}
	return accountRowToDomain(&row), nil
}

func createAccountCandidate(tx *gorm.DB, input accountIdentityInput, id domain.ID, now time.Time) error {
	created := &accountRow{ID: id, TenantID: input.tenantID, CanonicalUID: input.canonicalUID,
		HomeRegion: input.homeRegion, Status: string(domain.AccountActive), CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(created).Error; err != nil {
		return fmt.Errorf("create account: %w", err)
	}
	profile := &accountProfileRow{AccountID: id, Timezone: "UTC", Preferences: map[string]any{},
		CreatedAt: now, UpdatedAt: now}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(profile).Error; err != nil {
		return fmt.Errorf("create account profile: %w", err)
	}
	return nil
}

func bindAccountIdentity(tx *gorm.DB, input accountIdentityInput, createdID domain.ID, now time.Time) (domain.ID, error) {
	identity := &accountIdentityRow{ID: idgen.NewID(), AccountID: createdID, TenantID: input.tenantID,
		Issuer: input.issuer, Subject: input.subject, CanonicalUID: input.canonicalUID, CreatedAt: now, LastSeenAt: now}
	insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(identity)
	if insert.Error != nil {
		return domain.ID{}, fmt.Errorf("create account identity: %w", insert.Error)
	}
	if insert.RowsAffected == 1 {
		return createdID, nil
	}
	return useExistingAccountIdentity(tx, input, createdID, now)
}

func useExistingAccountIdentity(tx *gorm.DB, input accountIdentityInput, createdID domain.ID, now time.Time) (domain.ID, error) {
	var existing accountIdentityRow
	if err := tx.Where("tenant_id = ? AND issuer = ? AND subject = ?", input.tenantID, input.issuer, input.subject).
		First(&existing).Error; err != nil {
		return domain.ID{}, fmt.Errorf("load existing account identity: %w", err)
	}
	if err := tx.Model(&accountIdentityRow{}).Where("id = ?", existing.ID).
		Updates(map[string]any{"canonical_uid": input.canonicalUID, "last_seen_at": now}).Error; err != nil {
		return domain.ID{}, fmt.Errorf("touch account identity: %w", err)
	}
	if err := tx.Delete(&accountRow{}, "id = ?", createdID).Error; err != nil {
		return domain.ID{}, fmt.Errorf("remove competing account: %w", err)
	}
	return existing.AccountID, nil
}
