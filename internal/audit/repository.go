package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

type Repository interface {
	Append(ctx context.Context, e *domain.AuditEvent) error
	Log(ctx context.Context, e *domain.AuditEvent) error
}

type auditEventRow struct {
	ID          uuid.UUID         `gorm:"column:id;primaryKey"`
	ActorUID    string            `gorm:"column:actor_uid"`
	Action      string            `gorm:"column:action"`
	SubjectType string            `gorm:"column:subject_type"`
	SubjectID   string            `gorm:"column:subject_id"`
	TargetUID   string            `gorm:"column:target_uid"`
	Meta        datatypes.JSONMap `gorm:"column:meta"`
	CreatedAt   time.Time         `gorm:"column:created_at"`
}

func (auditEventRow) TableName() string { return "audit_events" }

func domainToRow(e *domain.AuditEvent) *auditEventRow {
	return &auditEventRow{
		ID:          e.ID,
		ActorUID:    e.ActorUID,
		Action:      e.Action,
		SubjectType: e.SubjectType,
		SubjectID:   e.SubjectID,
		TargetUID:   e.TargetUID,
		Meta:        datatypes.JSONMap(e.Meta),
		CreatedAt:   e.CreatedAt,
	}
}

type repo struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) Repository { return &repo{db: db} }

func (r *repo) Log(ctx context.Context, e *domain.AuditEvent) error {
	if err := r.Append(ctx, e); err != nil {
		return fmt.Errorf("log audit event: %w", err)
	}
	return nil
}

func (r *repo) Append(ctx context.Context, e *domain.AuditEvent) error {
	if e.ID == (uuid.UUID{}) {
		e.ID = uuid.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	row := domainToRow(e)
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("append audit event: %w", err)
	}
	return nil
}
