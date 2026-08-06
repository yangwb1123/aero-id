package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// EventStoreRepository persists canonical events durably (P3-07). Appends are
// idempotent on (tenant_id, source, event_id) and are executed inside the same
// transaction as the receipt/projection update by the receipt repository.
type EventStoreRepository interface {
	Append(ctx context.Context, envelope *event.Envelope) error
	ListByAggregate(ctx context.Context, tenantID, source, aggregateType, aggregateID string,
		fromVersion int64, limit int) ([]*event.Envelope, error)
	ListByCursor(ctx context.Context, tenantID, source string, cursor domain.ID, limit int) ([]*event.Envelope, domain.ID, error)
	ListByAccount(ctx context.Context, tenantID, source, accountID string, cursor domain.ID, limit int) ([]*event.Envelope, domain.ID, error)
}

type eventStoreRow struct {
	ID               domain.ID      `gorm:"column:id;primaryKey"`
	EventID          domain.ID      `gorm:"column:event_id"`
	TenantID         string         `gorm:"column:tenant_id"`
	Source           string         `gorm:"column:source"`
	SourceRegion     string         `gorm:"column:source_region"`
	EventType        string         `gorm:"column:event_type"`
	SchemaVersion    int            `gorm:"column:schema_version"`
	AggregateType    string         `gorm:"column:aggregate_type"`
	AggregateID      string         `gorm:"column:aggregate_id"`
	AggregateVersion int64          `gorm:"column:aggregate_version"`
	ActorUID         string         `gorm:"column:actor_uid"`
	OperationID      domain.ID      `gorm:"column:operation_id"`
	CorrelationID    domain.ID      `gorm:"column:correlation_id"`
	CausationID      domain.ID      `gorm:"column:causation_id"`
	TraceID          string         `gorm:"column:trace_id"`
	OccurredAt       time.Time      `gorm:"column:occurred_at"`
	ReceivedAt       time.Time      `gorm:"column:received_at"`
	Payload          datatypes.JSON `gorm:"column:payload"`
}

func (eventStoreRow) TableName() string { return "event_store" }

type eventStoreRepo struct {
	db *gorm.DB
}

func NewEventStoreRepository(db *gorm.DB) EventStoreRepository {
	return &eventStoreRepo{db: db}
}

// Append inserts the (redacted) canonical event idempotently. It is meant to be
// called from within the receipt transaction (applySnapshotEventTx); a standalone
// call still works and never overwrites an existing row.
func (r *eventStoreRepo) Append(ctx context.Context, envelope *event.Envelope) error {
	return appendEventStore(r.db.WithContext(ctx), envelope)
}

// appendEventStore runs on any *gorm.DB (plain or transaction) so the receipt
// repository can persist the event atomically with its projection update.
func appendEventStore(db *gorm.DB, envelope *event.Envelope) error {
	payload, err := json.Marshal(event.RedactPayload(envelope.Payload))
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	row := &eventStoreRow{
		ID: idgen.NewID(), EventID: envelope.EventID, TenantID: envelope.TenantID, Source: envelope.Source,
		SourceRegion: envelope.SourceRegion, EventType: envelope.EventType, SchemaVersion: envelope.SchemaVersion,
		AggregateType: envelope.AggregateType, AggregateID: envelope.AggregateID,
		AggregateVersion: envelope.AggregateVersion, ActorUID: envelope.ActorUID,
		OperationID: envelope.OperationID, CorrelationID: envelope.CorrelationID,
		CausationID: envelope.CausationID, TraceID: envelope.TraceID,
		OccurredAt: envelope.OccurredAt.UTC(), ReceivedAt: time.Now().UTC(), Payload: payload,
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

func (r *eventStoreRepo) ListByAggregate(ctx context.Context, tenantID, source, aggregateType, aggregateID string,
	fromVersion int64, limit int) ([]*event.Envelope, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	var rows []eventStoreRow
	query := r.db.WithContext(ctx).Where("tenant_id = ? AND source = ? AND aggregate_type = ? AND aggregate_id = ?",
		tenantID, source, aggregateType, aggregateID)
	if fromVersion > 0 {
		query = query.Where("aggregate_version >= ?", fromVersion)
	}
	if err := query.Order("aggregate_version ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*event.Envelope, 0, len(rows))
	for i := range rows {
		envelope, err := eventStoreRowToEnvelope(&rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, envelope)
	}
	return out, nil
}

func (r *eventStoreRepo) ListByCursor(ctx context.Context, tenantID, source string, cursor domain.ID, limit int) ([]*event.Envelope, domain.ID, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	var rows []eventStoreRow
	query := r.db.WithContext(ctx).Where("tenant_id = ? AND source = ?", tenantID, source)
	if cursor != (domain.ID{}) {
		query = query.Where("id < ?", cursor)
	}
	if err := query.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, domain.ID{}, err
	}
	next := domain.ID{}
	if len(rows) > limit {
		next = rows[limit-1].ID
		rows = rows[:limit]
	}
	out := make([]*event.Envelope, 0, len(rows))
	for i := range rows {
		envelope, err := eventStoreRowToEnvelope(&rows[i])
		if err != nil {
			return nil, domain.ID{}, err
		}
		out = append(out, envelope)
	}
	return out, next, nil
}

func (r *eventStoreRepo) ListByAccount(ctx context.Context, tenantID, source, accountID string,
	cursor domain.ID, limit int) ([]*event.Envelope, domain.ID, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	var rows []eventStoreRow
	query := r.db.WithContext(ctx).Where("tenant_id = ? AND source = ? AND payload->>'account_id' = ?",
		tenantID, source, accountID)
	if cursor != (domain.ID{}) {
		query = query.Where("id < ?", cursor)
	}
	if err := query.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, domain.ID{}, err
	}
	next := domain.ID{}
	if len(rows) > limit {
		next = rows[limit-1].ID
		rows = rows[:limit]
	}
	out := make([]*event.Envelope, 0, len(rows))
	for i := range rows {
		envelope, err := eventStoreRowToEnvelope(&rows[i])
		if err != nil {
			return nil, domain.ID{}, err
		}
		out = append(out, envelope)
	}
	return out, next, nil
}

func eventStoreRowToEnvelope(row *eventStoreRow) (*event.Envelope, error) {
	payload := map[string]any{}
	if len(row.Payload) > 0 {
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode stored event payload: %w", err)
		}
	}
	return &event.Envelope{
		EventID: row.EventID, Source: row.Source, SourceRegion: row.SourceRegion,
		EventType: row.EventType, SchemaVersion: row.SchemaVersion, AggregateType: row.AggregateType,
		AggregateID: row.AggregateID, AggregateVersion: row.AggregateVersion, ActorUID: row.ActorUID,
		TenantID: row.TenantID, OperationID: row.OperationID, CorrelationID: row.CorrelationID,
		CausationID: row.CausationID, TraceID: row.TraceID, OccurredAt: row.OccurredAt, Payload: payload,
	}, nil
}
