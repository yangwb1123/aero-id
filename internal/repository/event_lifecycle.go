package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const erasedEventCode = "event.account_erased"

func duplicateEventDisposition(receipt *eventReceiptRow) EventApplyResult {
	if receipt.Status == string(EventQuarantined) {
		return EventApplyResult{Status: EventQuarantined, ErrorCode: receipt.ErrorCode}
	}
	return EventApplyResult{Status: EventDuplicate}
}

func (r *eventReceiptRepo) quarantineErasedEventTx(ctx context.Context, tx *gorm.DB, envelope *event.Envelope,
	mutation *event.SnapshotMutation, fact *domain.AuditFact, receipt *eventReceiptRow, owner string, now time.Time) (EventApplyResult, error) {
	payload, err := marshalQuarantinedEventPayload(envelope, EventQuarantineDetails{Stage: "lifecycle"})
	if err != nil {
		return EventApplyResult{}, err
	}
	if err := writeEventReceiptPayload(tx, receipt, owner, payload, erasedEventCode); err != nil {
		return EventApplyResult{}, err
	}
	if fact != nil {
		if fact.Payload == nil {
			fact.Payload = map[string]any{}
		}
		fact.Payload["error_code"], fact.Payload["stage"] = erasedEventCode, "lifecycle"
		fact.Payload["identity_kind"] = "canonical"
	}
	if err := r.appendEventAudit(ctx, tx, fact, mutation, EventQuarantined); err != nil {
		return EventApplyResult{}, err
	}
	return EventApplyResult{Status: EventQuarantined, ErrorCode: erasedEventCode},
		finishEventReceipt(tx, receipt.ID, owner, EventQuarantined, now)
}

func (r *eventReceiptRepo) applyWritableEventTx(ctx context.Context, tx *gorm.DB, envelope *event.Envelope,
	mutation *event.SnapshotMutation, fact *domain.AuditFact, receipt *eventReceiptRow, owner string,
	payload []byte, now time.Time) (EventApplyResult, error) {
	if err := writeEventReceiptPayload(tx, receipt, owner, payload, ""); err != nil {
		return EventApplyResult{}, err
	}
	if r.events != nil {
		if err := appendEventStore(tx, envelope); err != nil {
			return EventApplyResult{}, fmt.Errorf("append event store: %w", err)
		}
	}
	status, err := r.applySnapshotRow(tx, envelope, mutation, now)
	if err != nil {
		return EventApplyResult{}, err
	}
	if err := r.appendEventAudit(ctx, tx, fact, mutation, status); err != nil {
		return EventApplyResult{}, err
	}
	return EventApplyResult{Status: status}, finishEventReceipt(tx, receipt.ID, owner, status, now)
}

func writeEventReceiptPayload(tx *gorm.DB, receipt *eventReceiptRow, owner string, payload []byte, code string) error {
	result := tx.Model(receipt).Where("status = 'processing' AND lease_owner = ?", owner).
		Updates(map[string]any{"payload": datatypes.JSON(payload), "error_code": code})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrEventReceiptBusy
	}
	return nil
}
