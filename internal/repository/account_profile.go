package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

type accountProfileRow struct {
	AccountID   uuid.UUID         `gorm:"column:account_id;primaryKey"`
	DisplayName string            `gorm:"column:display_name"`
	AvatarURL   string            `gorm:"column:avatar_url"`
	Locale      string            `gorm:"column:locale"`
	Timezone    string            `gorm:"column:timezone"`
	Preferences datatypes.JSONMap `gorm:"column:preferences"`
	CreatedAt   time.Time         `gorm:"column:created_at"`
	UpdatedAt   time.Time         `gorm:"column:updated_at"`
}

func (accountProfileRow) TableName() string { return "account_profiles" }

func profileRowToDomain(row *accountProfileRow) *domain.AccountProfile {
	return &domain.AccountProfile{
		AccountID:   row.AccountID,
		DisplayName: row.DisplayName,
		AvatarURL:   row.AvatarURL,
		Locale:      row.Locale,
		Timezone:    row.Timezone,
		Preferences: map[string]any(row.Preferences),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func profileDomainToRow(profile *domain.AccountProfile) *accountProfileRow {
	return &accountProfileRow{
		AccountID:   profile.AccountID,
		DisplayName: profile.DisplayName,
		AvatarURL:   profile.AvatarURL,
		Locale:      profile.Locale,
		Timezone:    profile.Timezone,
		Preferences: datatypes.JSONMap(profile.Preferences),
		CreatedAt:   profile.CreatedAt,
		UpdatedAt:   profile.UpdatedAt,
	}
}

func (r *accountRepo) GetProfile(ctx context.Context, id domain.ID) (*domain.AccountProfile, error) {
	var row accountProfileRow
	if err := r.db.WithContext(ctx).Where("account_id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return profileRowToDomain(&row), nil
}

func (r *accountRepo) UpdateProfile(ctx context.Context, profile *domain.AccountProfile) error {
	return r.updateProfile(ctx, profile, nil)
}

func (r *accountRepo) UpdateProfileWithAudit(ctx context.Context, profile *domain.AccountProfile, fact *domain.AuditFact) error {
	if r.audit == nil {
		return fmt.Errorf("transactional audit repository is not configured")
	}
	return r.updateProfile(ctx, profile, fact)
}

func (r *accountRepo) updateProfile(ctx context.Context, profile *domain.AccountProfile, fact *domain.AuditFact) error {
	now := time.Now().UTC()
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = now
	}
	profile.UpdatedAt = now
	row := profileDomainToRow(profile)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockWritableAccountTx(tx, profile.AccountID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "account_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"display_name": profile.DisplayName,
				"avatar_url":   profile.AvatarURL,
				"locale":       profile.Locale,
				"timezone":     profile.Timezone,
				"preferences":  row.Preferences,
				"updated_at":   now,
			}),
		}).Create(row).Error; err != nil {
			return err
		}
		if fact == nil {
			return nil
		}
		if r.audit == nil {
			return fmt.Errorf("transactional audit repository is not configured")
		}
		return r.audit.AppendInTx(ctx, tx, fact)
	})
}
