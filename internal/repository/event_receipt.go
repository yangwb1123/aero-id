package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type EventApplyStatus string

const (
	EventApplied     EventApplyStatus = "applied"
	EventDuplicate   EventApplyStatus = "duplicate"
	EventOutOfOrder  EventApplyStatus = "out_of_order"
	EventQuarantined EventApplyStatus = "quarantined"
)

type EventApplyResult struct {
	Status EventApplyStatus
}

type EventReceiptRepository interface {
	ApplySnapshotEvent(context.Context, *event.Envelope, *event.SnapshotMutation) (EventApplyResult, error)
	QuarantineEvent(context.Context, *event.Envelope, string) error
}

type EventSnapshotAuditRepository interface {
	ApplySnapshotEventWithAudit(context.Context, *event.Envelope, *event.SnapshotMutation, *domain.AuditFact) (EventApplyResult, error)
}

var ErrEventReceiptBusy = errors.New("event receipt is being processed")
var ErrEventTenantMismatch = errors.New("event account tenant mismatch")

type eventReceiptRepo struct {
	db     *gorm.DB
	audit  AuditLedgerTransactionalRepository
	events EventStoreRepository // optional; when set, applied events are appended atomically (P3-07)
}

func NewEventReceiptRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) EventReceiptRepository {
	return &eventReceiptRepo{db: db, audit: requireAuditLedger("NewEventReceiptRepository", audit)}
}

// NewEventReceiptRepositoryWithStore builds the receipt repository with a
// durable event store so applied events are persisted atomically with their
// receipt and projection update.
func NewEventReceiptRepositoryWithStore(db *gorm.DB, events EventStoreRepository, audit AuditLedgerTransactionalRepository) EventReceiptRepository {
	repo := NewEventReceiptRepository(db, audit).(*eventReceiptRepo)
	repo.events = events
	return repo
}

type eventReceiptRow struct {
	ID               uuid.UUID      `gorm:"column:id;primaryKey"`
	TenantID         string         `gorm:"column:tenant_id"`
	Source           string         `gorm:"column:source"`
	EventID          uuid.UUID      `gorm:"column:event_id"`
	SourceRegion     string         `gorm:"column:source_region"`
	AggregateType    string         `gorm:"column:aggregate_type"`
	AggregateID      string         `gorm:"column:aggregate_id"`
	AggregateVersion int64          `gorm:"column:aggregate_version"`
	Status           string         `gorm:"column:status"`
	ErrorCode        string         `gorm:"column:error_code"`
	Payload          datatypes.JSON `gorm:"column:payload"`
	Attempt          int            `gorm:"column:attempt"`
	LeaseOwner       string         `gorm:"column:lease_owner"`
	LeaseUntil       *time.Time     `gorm:"column:lease_until"`
	CreatedAt        time.Time      `gorm:"column:created_at"`
	UpdatedAt        time.Time      `gorm:"column:updated_at"`
}

func (eventReceiptRow) TableName() string { return "event_receipts" }

func (r *eventReceiptRepo) ApplySnapshotEvent(ctx context.Context, envelope *event.Envelope, mutation *event.SnapshotMutation) (EventApplyResult, error) {
	return r.applySnapshotEvent(ctx, envelope, mutation, nil)
}

func (r *eventReceiptRepo) ApplySnapshotEventWithAudit(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation, fact *domain.AuditFact) (EventApplyResult, error) {
	if r.audit == nil {
		return EventApplyResult{}, fmt.Errorf("transactional audit repository is not configured")
	}
	return r.applySnapshotEvent(ctx, envelope, mutation, fact)
}

func (r *eventReceiptRepo) applySnapshotEvent(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation, fact *domain.AuditFact) (EventApplyResult, error) {
	payload, err := json.Marshal(event.RedactPayload(envelope.Payload))
	if err != nil {
		return EventApplyResult{}, fmt.Errorf("marshal event payload: %w", err)
	}
	result := EventApplyResult{}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result.Status, err = r.applySnapshotEventTx(ctx, tx, envelope, mutation, fact, payload)
		return err
	})
	return result, err
}

func (r *eventReceiptRepo) applySnapshotEventTx(ctx context.Context, tx *gorm.DB, envelope *event.Envelope,
	mutation *event.SnapshotMutation, fact *domain.AuditFact, payload []byte) (EventApplyStatus, error) {
	now := time.Now().UTC()
	if err := verifyEventAccountTenant(tx, envelope.TenantID, mutation.AccountID); err != nil {
		return "", err
	}
	receipt, owner, err := claimEventReceipt(tx, envelope, payload, now)
	if err != nil {
		return "", err
	}
	if owner == "" {
		return EventDuplicate, nil
	}
	if r.events != nil {
		// P3-07: the canonical event becomes durable in the same transaction as
		// its receipt and projection update, so replay can rebuild projections.
		if err := appendEventStore(tx, envelope); err != nil {
			return "", fmt.Errorf("append event store: %w", err)
		}
	}
	status, err := r.applySnapshotRow(tx, envelope, mutation, now)
	if err != nil {
		return "", err
	}
	if err := r.appendEventAudit(ctx, tx, fact, mutation, status); err != nil {
		return "", err
	}
	return status, finishEventReceipt(tx, receipt.ID, owner, status, now)
}

func (r *eventReceiptRepo) applySnapshotRow(tx *gorm.DB, envelope *event.Envelope,
	mutation *event.SnapshotMutation, now time.Time) (EventApplyStatus, error) {
	var existing accountSnapshotRow
	lookup := tx.Where("account_id = ? AND source = ? AND dataset = ?", mutation.AccountID, mutation.Source, mutation.Dataset).First(&existing)
	if lookup.Error == nil && existing.Version >= mutation.Version && mutation.Version > 0 {
		return EventOutOfOrder, nil
	}
	if lookup.Error != nil && !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("load snapshot version: %w", lookup.Error)
	}
	snapshot := &domain.AccountSnapshot{ID: idgen.NewID(), AccountID: mutation.AccountID, Source: mutation.Source,
		SourceRegion: mutation.SourceRegion, Dataset: mutation.Dataset, Status: mutation.Status, Version: mutation.Version,
		GeneratedAt: envelope.OccurredAt, LastSyncedAt: timePtr(envelope.OccurredAt), Data: mutation.Data,
		CreatedAt: now, UpdatedAt: now}
	insert := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "account_id"}, {Name: "source"}, {Name: "dataset"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"source_region", "status", "version", "generated_at", "last_synced_at", "error_code", "data", "updated_at",
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "account_snapshots.version <= EXCLUDED.version"}}}}).
		Create(snapshotDomainToRow(snapshot))
	if insert.Error != nil {
		return "", fmt.Errorf("apply account snapshot: %w", insert.Error)
	}
	if insert.RowsAffected == 0 {
		return EventOutOfOrder, nil
	}
	return EventApplied, nil
}

func (r *eventReceiptRepo) appendEventAudit(ctx context.Context, tx *gorm.DB, fact *domain.AuditFact,
	mutation *event.SnapshotMutation, status EventApplyStatus) error {
	if fact == nil {
		return nil
	}
	fact.AccountID = mutation.AccountID
	if fact.Payload == nil {
		fact.Payload = make(map[string]any)
	}
	fact.Payload["status"] = string(status)
	if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
		return fmt.Errorf("append event audit: %w", err)
	}
	return nil
}

func (r *eventReceiptRepo) QuarantineEvent(ctx context.Context, envelope *event.Envelope, code string) error {
	payload, _ := json.Marshal(event.RedactPayload(envelope.Payload))
	now := time.Now().UTC()
	row := &eventReceiptRow{
		ID: idgen.NewID(), TenantID: envelope.TenantID, Source: envelope.Source, EventID: envelope.EventID,
		SourceRegion: envelope.SourceRegion, AggregateType: envelope.AggregateType,
		AggregateID: envelope.AggregateID, AggregateVersion: envelope.AggregateVersion,
		Status: string(EventQuarantined), ErrorCode: code, Payload: datatypes.JSON(payload), Attempt: 1, CreatedAt: now, UpdatedAt: now,
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row)
		if insert.Error != nil || insert.RowsAffected > 0 {
			return insert.Error
		}
		var existing eventReceiptRow
		if err := tx.Where("tenant_id = ? AND source = ? AND event_id = ?",
			envelope.TenantID, envelope.Source, envelope.EventID).First(&existing).Error; err != nil {
			return err
		}
		if existing.Status == string(EventApplied) || existing.Status == string(EventDuplicate) {
			return nil
		}
		if existing.Status == "processing" && existing.LeaseUntil != nil && existing.LeaseUntil.After(now) {
			return ErrEventReceiptBusy
		}
		return tx.Model(&eventReceiptRow{}).Where("id = ?", existing.ID).Updates(map[string]any{
			"status": string(EventQuarantined), "error_code": code, "lease_owner": "", "lease_until": nil, "updated_at": now,
		}).Error
	})
}

func claimEventReceipt(tx *gorm.DB, envelope *event.Envelope, payload []byte, now time.Time) (*eventReceiptRow, string, error) {
	owner := idgen.NewID().String()
	receipt := &eventReceiptRow{ID: idgen.NewID(), TenantID: envelope.TenantID, Source: envelope.Source, EventID: envelope.EventID,
		SourceRegion: envelope.SourceRegion, AggregateType: envelope.AggregateType, AggregateID: envelope.AggregateID,
		AggregateVersion: envelope.AggregateVersion, Status: "processing", Payload: datatypes.JSON(payload), Attempt: 1,
		LeaseOwner: owner, LeaseUntil: timePtr(now.Add(5 * time.Minute)), CreatedAt: now, UpdatedAt: now}
	insert := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(receipt)
	if insert.Error != nil {
		return nil, "", fmt.Errorf("create event receipt: %w", insert.Error)
	}
	if insert.RowsAffected > 0 {
		return receipt, owner, nil
	}
	var existing eventReceiptRow
	query := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("tenant_id = ? AND source = ? AND event_id = ?", envelope.TenantID, envelope.Source, envelope.EventID).First(&existing)
	if query.Error != nil {
		return nil, "", query.Error
	}
	if existing.Status != "processing" {
		return &existing, "", nil
	}
	if existing.LeaseUntil != nil && existing.LeaseUntil.After(now) {
		return nil, "", ErrEventReceiptBusy
	}
	if err := tx.Model(&existing).Updates(map[string]any{"attempt": gorm.Expr("attempt + 1"), "lease_owner": owner,
		"lease_until": now.Add(5 * time.Minute), "updated_at": now}).Error; err != nil {
		return nil, "", err
	}
	existing.Attempt++
	existing.LeaseOwner, existing.LeaseUntil, existing.UpdatedAt = owner, timePtr(now.Add(5*time.Minute)), now
	return &existing, owner, nil
}

func finishEventReceipt(tx *gorm.DB, id uuid.UUID, owner string, status EventApplyStatus, now time.Time) error {
	result := tx.Model(&eventReceiptRow{}).Where("id = ? AND status = 'processing' AND lease_owner = ?", id, owner).
		Updates(map[string]any{"status": string(status), "lease_owner": "", "lease_until": nil, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrEventReceiptBusy
	}
	return nil
}

func timePtr(value time.Time) *time.Time { return &value }

func verifyEventAccountTenant(tx *gorm.DB, tenantID string, accountID domain.ID) error {
	if tenantID == "" || accountID == (domain.ID{}) {
		return ErrEventTenantMismatch
	}
	var count int64
	if err := tx.Model(&accountRow{}).Where("tenant_id = ? AND id = ?", tenantID, accountID).Count(&count).Error; err != nil {
		return fmt.Errorf("verify event account tenant: %w", err)
	}
	if count != 1 {
		return ErrEventTenantMismatch
	}
	return nil
}
