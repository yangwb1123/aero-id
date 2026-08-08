package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/outbox"
)

// MessageAuditRepository is the in-transaction audit surface for message
// deletion (B2-1): the soft delete, its outbox event (when present) and the
// audit fact commit in one gorm transaction.
type MessageAuditRepository interface {
	// SoftDeleteWithEventAudit soft-deletes the message, appends the outbox
	// event (when evt != nil and the outbox is wired) and appends the audit
	// fact — one transaction. The anchored EventID (message ID) makes a retry
	// after a failed append fold via ErrAuditDuplicate with the domain delete
	// committing as a no-op.
	SoftDeleteWithEventAudit(ctx context.Context, id domain.ID, evt *outbox.Event, fact *domain.AuditFact) error
}

// SoftDeleteWithEventAudit merges SoftDeleteWithEvent and the audit fact into
// one transaction. When evt is nil or the outbox is unwired, the delete and
// fact still commit together.
func (r *messageRepo) SoftDeleteWithEventAudit(ctx context.Context, id domain.ID, evt *outbox.Event, fact *domain.AuditFact) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).Delete(&messageRow{}).Error; err != nil {
			return fmt.Errorf("soft delete message: %w", err)
		}
		if evt != nil && r.outbox != nil {
			if err := r.outbox.Append(ctx, tx, evt); err != nil {
				return err
			}
		}
		if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
			return fmt.Errorf("append message audit: %w", err)
		}
		return nil
	})
}
