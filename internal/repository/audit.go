package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// AuditRepository persists audit events for sensitive operations.
// It also satisfies service.AuditLogger via the Log method.
type AuditRepository interface {
	Append(ctx context.Context, e *domain.AuditEvent) error
	Log(ctx context.Context, e *domain.AuditEvent) error
}

type auditRepo struct{ db *gorm.DB }

// NewAuditRepository returns an AuditRepository backed by GORM.
func NewAuditRepository(db *gorm.DB) AuditRepository { return &auditRepo{db: db} }

// Log satisfies service.AuditLogger so an AuditRepository can be passed directly.
func (r *auditRepo) Log(ctx context.Context, e *domain.AuditEvent) error {
	return r.Append(ctx, e)
}

// Append inserts a new audit event row. A UUID and timestamp are generated if
// not already set on the event.
func (r *auditRepo) Append(ctx context.Context, e *domain.AuditEvent) error {
	if e.ID == (uuid.UUID{}) {
		e.ID = uuid.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	row := auditEventDomainToRow(e)
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}
