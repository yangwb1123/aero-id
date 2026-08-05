package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// PreferenceRepository persists per-uid notification preferences.
type PreferenceRepository interface {
	// Get returns the global preference for a single (uid, notificationType) pair.
	// When no row exists it returns nil, nil (caller treats as "default enabled").
	// Scoped preferences must be queried via GetScoped.
	Get(ctx context.Context, uid, notificationType string) (*domain.NotificationPreference, error)
	// GetScoped returns the preference for a (uid, notificationType, scopeType, scopeID)
	// combination. When no scoped row exists it falls back to the global preference
	// (calls Get). When neither exists it returns nil, nil.
	GetScoped(ctx context.Context, uid, notificationType, scopeType, scopeID string) (*domain.NotificationPreference, error)
	// ListAll returns every preference row for uid (empty slice when none set).
	// Includes both global and scoped rows.
	ListAll(ctx context.Context, uid string) ([]*domain.NotificationPreference, error)
	// Upsert creates or replaces a preference row. The caller constructs the
	// full preference (including ScopeType/ScopeID for scoped rows); rows not
	// included in an upsert batch are unaffected.
	Upsert(ctx context.Context, prefs []*domain.NotificationPreference) error
}

type preferenceRepo struct {
	db *gorm.DB
}

func NewPreferenceRepository(db *gorm.DB) PreferenceRepository {
	return &preferenceRepo{db: db}
}

// preferenceRow maps to the notification_preferences table.
type preferenceRow struct {
	UID              string    `gorm:"column:uid;primaryKey"`
	NotificationType string    `gorm:"column:notification_type;primaryKey"`
	ScopeType        string    `gorm:"column:scope_type;primaryKey"`
	ScopeID          string    `gorm:"column:scope_id;primaryKey"`
	Enabled          bool      `gorm:"column:enabled"`
	QuietHoursStart  string    `gorm:"column:quiet_hours_start"`
	QuietHoursEnd    string    `gorm:"column:quiet_hours_end"`
	Timezone         string    `gorm:"column:timezone"`
	CreatedAt        time.Time `gorm:"column:created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at"`
}

func (preferenceRow) TableName() string { return "notification_preferences" }

func prefToRow(p *domain.NotificationPreference) *preferenceRow {
	return &preferenceRow{
		UID:              p.UID,
		NotificationType: p.NotificationType,
		ScopeType:        p.ScopeType,
		ScopeID:          p.ScopeID,
		Enabled:          p.Enabled,
		QuietHoursStart:  p.QuietHoursStart,
		QuietHoursEnd:    p.QuietHoursEnd,
		Timezone:         p.Timezone,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        p.UpdatedAt,
	}
}

func rowToPref(r *preferenceRow) *domain.NotificationPreference {
	return &domain.NotificationPreference{
		UID:              r.UID,
		NotificationType: r.NotificationType,
		ScopeType:        r.ScopeType,
		ScopeID:          r.ScopeID,
		Enabled:          r.Enabled,
		QuietHoursStart:  r.QuietHoursStart,
		QuietHoursEnd:    r.QuietHoursEnd,
		Timezone:         r.Timezone,
		CreatedAt:        r.CreatedAt,
		UpdatedAt:        r.UpdatedAt,
	}
}

func (r *preferenceRepo) Get(ctx context.Context, uid, notificationType string) (*domain.NotificationPreference, error) {
	var row preferenceRow
	err := r.db.WithContext(ctx).
		Where("uid = ? AND notification_type = ? AND scope_type = '' AND scope_id = ''", uid, notificationType).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get preference: %w", err)
	}
	return rowToPref(&row), nil
}

func (r *preferenceRepo) GetScoped(ctx context.Context, uid, notificationType, scopeType, scopeID string) (*domain.NotificationPreference, error) {
	// First try the exact scoped row.
	var row preferenceRow
	err := r.db.WithContext(ctx).
		Where("uid = ? AND notification_type = ? AND scope_type = ? AND scope_id = ?", uid, notificationType, scopeType, scopeID).
		First(&row).Error
	if err == nil {
		return rowToPref(&row), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("get scoped preference: %w", err)
	}
	// Fall back to global preference.
	return r.Get(ctx, uid, notificationType)
}

func (r *preferenceRepo) ListAll(ctx context.Context, uid string) ([]*domain.NotificationPreference, error) {
	var rows []preferenceRow
	if err := r.db.WithContext(ctx).Where("uid = ?", uid).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list preferences: %w", err)
	}
	out := make([]*domain.NotificationPreference, len(rows))
	for i := range rows {
		out[i] = rowToPref(&rows[i])
	}
	return out, nil
}

func (r *preferenceRepo) Upsert(ctx context.Context, prefs []*domain.NotificationPreference) error {
	if len(prefs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for _, p := range prefs {
		p.UpdatedAt = now
		if p.CreatedAt.IsZero() {
			p.CreatedAt = now
		}
		row := prefToRow(p)
		// Scope-aware upsert: match on uid + notification_type + scope_type + scope_id.
		conds := map[string]any{
			"uid":               row.UID,
			"notification_type": row.NotificationType,
			"scope_type":        row.ScopeType,
			"scope_id":          row.ScopeID,
		}
		err := r.db.WithContext(ctx).
			Where(conds).
			Assign(map[string]any{
				"enabled":           row.Enabled,
				"quiet_hours_start": row.QuietHoursStart,
				"quiet_hours_end":   row.QuietHoursEnd,
				"timezone":          row.Timezone,
				"updated_at":        now,
			}).
			FirstOrCreate(row).Error
		if err != nil {
			return fmt.Errorf("upsert preference: %w", err)
		}
	}
	return nil
}
