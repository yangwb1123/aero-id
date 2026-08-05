package repository

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type AuditLedgerRepository interface {
	Append(context.Context, *domain.AuditFact) error
	List(context.Context, domain.AuditFactFilter) ([]*domain.AuditFact, string, error)
	VerifyChain(context.Context, string, string) error
	// ChainPartitions enumerates the (source, partition) chains (P4-03).
	ChainPartitions(ctx context.Context) ([]ChainPartition, error)
	// ChainHead returns the newest fact of one chain (anchor input).
	ChainHead(ctx context.Context, source, partition string) (*domain.AuditFact, error)
}

// ChainPartition identifies one hash chain.
type ChainPartition struct {
	Source    string
	Partition string
}

type AuditLedgerTransactionalRepository interface {
	AppendInTx(context.Context, *gorm.DB, *domain.AuditFact) error
}

type AuditPublicationRepository interface {
	ClaimPublication(context.Context, string, time.Duration, int) ([]*domain.AuditPublication, error)
	FinishPublication(context.Context, domain.ID, string, string, string, time.Time) error
}

type AuditLedgerConfig struct{ SigningKey string }

var ErrAuditDuplicate = errors.New("audit ledger: duplicate source event")
var ErrAuditPublicationLeaseLost = errors.New("audit publication lease lost")

type auditLedgerRepo struct {
	db         *gorm.DB
	signingKey []byte
}

func NewAuditLedgerRepository(db *gorm.DB, cfg AuditLedgerConfig) AuditLedgerRepository {
	return &auditLedgerRepo{db: db, signingKey: []byte(cfg.SigningKey)}
}

type auditLedgerRow struct {
	ID               uuid.UUID      `gorm:"column:id;primaryKey"`
	HashVersion      int            `gorm:"column:hash_version"`
	ChainSequence    int64          `gorm:"column:chain_sequence"`
	Source           string         `gorm:"column:source"`
	SourceRegion     string         `gorm:"column:source_region"`
	PartitionKey     string         `gorm:"column:partition_key"`
	EventID          *uuid.UUID     `gorm:"column:event_id"`
	OperationID      *uuid.UUID     `gorm:"column:operation_id"`
	CorrelationID    *uuid.UUID     `gorm:"column:correlation_id"`
	CausationID      *uuid.UUID     `gorm:"column:causation_id"`
	TraceID          string         `gorm:"column:trace_id"`
	RequestID        string         `gorm:"column:request_id"`
	ActorUID         string         `gorm:"column:actor_uid"`
	AccountID        *uuid.UUID     `gorm:"column:account_id"`
	TenantID         string         `gorm:"column:tenant_id"`
	Action           string         `gorm:"column:action"`
	ResourceType     string         `gorm:"column:resource_type"`
	ResourceID       string         `gorm:"column:resource_id"`
	OccurredAt       time.Time      `gorm:"column:occurred_at"`
	Payload          datatypes.JSON `gorm:"column:payload"`
	PayloadHash      string         `gorm:"column:payload_hash"`
	PreviousHash     string         `gorm:"column:previous_hash"`
	RecordHash       string         `gorm:"column:record_hash"`
	SignatureVersion int            `gorm:"column:signature_version"`
	Signature        string         `gorm:"column:signature"`
	CreatedAt        time.Time      `gorm:"column:created_at"`
}

type auditPublicationRow struct {
	ID            uuid.UUID  `gorm:"column:id;primaryKey"`
	FactID        uuid.UUID  `gorm:"column:fact_id"`
	Status        string     `gorm:"column:status"`
	Attempt       int        `gorm:"column:attempt"`
	LeaseOwner    string     `gorm:"column:lease_owner"`
	LeaseUntil    *time.Time `gorm:"column:lease_until"`
	NextAttemptAt time.Time  `gorm:"column:next_attempt_at"`
	ErrorCode     string     `gorm:"column:error_code"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
}

func (auditPublicationRow) TableName() string { return "audit_publications" }

func (auditLedgerRow) TableName() string { return "audit_ledger" }

func (r *auditLedgerRepo) Append(ctx context.Context, fact *domain.AuditFact) error {
	if err := validateAuditFact(fact); err != nil {
		return err
	}
	payload, err := json.Marshal(fact.Payload)
	if err != nil {
		return fmt.Errorf("marshal audit payload: %w", err)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return r.appendInTx(tx, fact, payload)
	})
}

func (r *auditLedgerRepo) AppendInTx(ctx context.Context, tx *gorm.DB, fact *domain.AuditFact) error {
	if tx == nil {
		return fmt.Errorf("audit transaction is required")
	}
	if err := validateAuditFact(fact); err != nil {
		return err
	}
	payload, err := json.Marshal(fact.Payload)
	if err != nil {
		return fmt.Errorf("marshal audit payload: %w", err)
	}
	return r.appendInTx(tx.WithContext(ctx), fact, payload)
}

func (r *auditLedgerRepo) appendInTx(tx *gorm.DB, fact *domain.AuditFact, payload []byte) error {
	now := time.Now().UTC()
	if err := checkAuditDuplicate(tx, fact); err != nil {
		return err
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", fact.PartitionKey).Error; err != nil {
		return fmt.Errorf("lock audit partition: %w", err)
	}
	if err := checkAuditDuplicate(tx, fact); err != nil {
		return err
	}
	var previous auditLedgerRow
	query := tx.Where("source = ? AND partition_key = ?", fact.Source, fact.PartitionKey).Order("chain_sequence DESC").First(&previous)
	if query.Error != nil && !errors.Is(query.Error, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load audit chain: %w", query.Error)
	}
	fact.ChainSequence = previous.ChainSequence + 1
	fact.PreviousHash = previous.RecordHash
	fact.HashVersion = 2
	fact.PayloadHash = digest(payload)
	if fact.ID == (domain.ID{}) {
		fact.ID = idgen.NewID()
	}
	// PostgreSQL timestamptz stores microsecond precision; truncate before the
	// record hash is computed so chain verification round-trips (P4-03).
	fact.OccurredAt = fact.OccurredAt.Truncate(time.Microsecond)
	if fact.CreatedAt.IsZero() {
		fact.CreatedAt = now.Truncate(time.Microsecond)
	} else {
		fact.CreatedAt = fact.CreatedAt.Truncate(time.Microsecond)
	}
	return r.createAuditRows(tx, fact, payload, now)
}

func checkAuditDuplicate(tx *gorm.DB, fact *domain.AuditFact) error {
	if fact.EventID == (domain.ID{}) {
		return nil
	}
	var existing auditLedgerRow
	query := tx.Where("tenant_id = ? AND source = ? AND event_id = ?", fact.TenantID, fact.Source, fact.EventID).First(&existing)
	if query.Error == nil {
		return ErrAuditDuplicate
	}
	if !errors.Is(query.Error, gorm.ErrRecordNotFound) {
		return fmt.Errorf("check duplicate audit event: %w", query.Error)
	}
	return nil
}

func (r *auditLedgerRepo) createAuditRows(tx *gorm.DB, fact *domain.AuditFact, payload []byte, now time.Time) error {
	fact.RecordHash = auditRecordHash(fact)
	if len(r.signingKey) > 0 {
		fact.SignatureVersion = 1
		fact.Signature = sign(r.signingKey, fact.RecordHash)
	}
	if err := tx.Create(auditLedgerFactToRow(fact, payload)).Error; err != nil {
		return fmt.Errorf("append audit ledger: %w", err)
	}
	publication := &auditPublicationRow{ID: idgen.NewID(), FactID: fact.ID, Status: "pending", NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(publication).Error; err != nil {
		return fmt.Errorf("create audit publication: %w", err)
	}
	return nil
}

func (r *auditLedgerRepo) ClaimPublication(ctx context.Context, worker string, lease time.Duration, limit int) ([]*domain.AuditPublication, error) {
	if limit <= 0 {
		limit = 50
	}
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	rows := make([]auditPublicationRow, 0, limit)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Where("((status = 'pending' AND next_attempt_at <= ?) OR (status = 'delivering' AND lease_until < ?))", now, now).
			Order("next_attempt_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		until := now.Add(lease)
		for i := range rows {
			if err := tx.Model(&auditPublicationRow{}).Where("id = ?", rows[i].ID).Updates(map[string]any{
				"status": "delivering", "attempt": gorm.Expr("attempt + 1"), "lease_owner": worker,
				"lease_until": until, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			rows[i].Status = "delivering"
			rows[i].Attempt++
			rows[i].LeaseOwner = worker
			rows[i].LeaseUntil = &until
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]*domain.AuditPublication, 0, len(rows))
	for i := range rows {
		var factRow auditLedgerRow
		if err := r.db.WithContext(ctx).Where("id = ?", rows[i].FactID).First(&factRow).Error; err != nil {
			return nil, err
		}
		result = append(result, &domain.AuditPublication{ID: rows[i].ID, Fact: auditLedgerRowToFact(&factRow), Status: rows[i].Status,
			Attempt: rows[i].Attempt, LeaseOwner: rows[i].LeaseOwner, LeaseUntil: rows[i].LeaseUntil,
			NextAttemptAt: rows[i].NextAttemptAt, ErrorCode: rows[i].ErrorCode})
	}
	return result, nil
}

func (r *auditLedgerRepo) FinishPublication(ctx context.Context, id domain.ID, worker, status, errorCode string, next time.Time) error {
	result := r.db.WithContext(ctx).Model(&auditPublicationRow{}).Where("id = ? AND status = 'delivering' AND lease_owner = ?", id, worker).
		Updates(map[string]any{"status": status, "error_code": errorCode, "next_attempt_at": next,
			"lease_owner": "", "lease_until": nil, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAuditPublicationLeaseLost
	}
	return nil
}

func (r *auditLedgerRepo) List(ctx context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	filter.Page.Normalize()
	q := r.db.WithContext(ctx).Model(&auditLedgerRow{}).Order("occurred_at DESC, id DESC")
	q = applyAuditIdentityFilters(q, filter)
	q = applyAuditTimeFilters(q, filter)
	var rows []auditLedgerRow
	if err := q.Limit(filter.Page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > filter.Page.Limit {
		next = encodeAuditCursor(&rows[filter.Page.Limit-1])
		rows = rows[:filter.Page.Limit]
	}
	result := make([]*domain.AuditFact, len(rows))
	for i := range rows {
		result[i] = auditLedgerRowToFact(&rows[i])
	}
	return result, next, nil
}

func applyAuditIdentityFilters(q *gorm.DB, filter domain.AuditFactFilter) *gorm.DB {
	if filter.Source != "" {
		q = q.Where("source = ?", filter.Source)
	}
	if filter.ActorUID != "" {
		q = q.Where("actor_uid = ?", filter.ActorUID)
	}
	if filter.RequestID != "" {
		q = q.Where("request_id = ?", filter.RequestID)
	}
	if filter.AccountID != (domain.ID{}) {
		q = q.Where("account_id = ?", filter.AccountID)
	}
	if filter.TenantID != "" {
		q = q.Where("tenant_id = ?", filter.TenantID)
	}
	return applyAuditResourceFilters(q, filter)
}

func applyAuditResourceFilters(q *gorm.DB, filter domain.AuditFactFilter) *gorm.DB {
	if filter.Action != "" {
		q = q.Where("action = ?", filter.Action)
	}
	if filter.ResourceType != "" {
		q = q.Where("resource_type = ?", filter.ResourceType)
	}
	if filter.OperationID != (domain.ID{}) {
		q = q.Where("operation_id = ?", filter.OperationID)
	}
	return q
}

func applyAuditTimeFilters(q *gorm.DB, filter domain.AuditFactFilter) *gorm.DB {
	if filter.From != nil {
		q = q.Where("occurred_at >= ?", *filter.From)
	}
	if filter.To != nil {
		q = q.Where("occurred_at < ?", *filter.To)
	}
	if filter.Page.Cursor == "" {
		return q
	}
	if cursor, err := decodeAuditCursor(filter.Page.Cursor); err == nil {
		return q.Where("(occurred_at < ? OR (occurred_at = ? AND id < ?))", cursor.OccurredAt, cursor.OccurredAt, cursor.ID)
	}
	return q.Where("id < ?", filter.Page.Cursor)
}

func (r *auditLedgerRepo) VerifyChain(ctx context.Context, source, partition string) error {
	var rows []auditLedgerRow
	if err := r.db.WithContext(ctx).Where("source = ? AND partition_key = ?", source, partition).Order("chain_sequence ASC").Find(&rows).Error; err != nil {
		return err
	}
	previous := ""
	for _, row := range rows {
		fact := auditLedgerRowToFact(&row)
		payload, err := json.Marshal(fact.Payload)
		if err != nil || fact.PayloadHash != digest(payload) {
			return fmt.Errorf("audit payload verification failed at %s", row.ID)
		}
		if fact.PreviousHash != previous || fact.RecordHash != auditRecordHash(fact) {
			return fmt.Errorf("audit chain verification failed at %s", row.ID)
		}
		if len(r.signingKey) > 0 && !hmac.Equal([]byte(fact.Signature), []byte(sign(r.signingKey, fact.RecordHash))) {
			return fmt.Errorf("audit signature verification failed at %s", row.ID)
		}
		previous = fact.RecordHash
	}
	return nil
}

func auditLedgerFactToRow(fact *domain.AuditFact, payload []byte) *auditLedgerRow {
	return &auditLedgerRow{ID: fact.ID, HashVersion: fact.HashVersion, ChainSequence: fact.ChainSequence, Source: fact.Source,
		SourceRegion: fact.SourceRegion, PartitionKey: fact.PartitionKey, EventID: nullableID(fact.EventID),
		OperationID: nullableID(fact.OperationID), CorrelationID: nullableID(fact.CorrelationID), CausationID: nullableID(fact.CausationID),
		TraceID: fact.TraceID, RequestID: fact.RequestID, ActorUID: fact.ActorUID, AccountID: nullableID(fact.AccountID), TenantID: fact.TenantID,
		Action: fact.Action, ResourceType: fact.ResourceType, ResourceID: fact.ResourceID, OccurredAt: fact.OccurredAt,
		Payload: datatypes.JSON(payload), PayloadHash: fact.PayloadHash, PreviousHash: fact.PreviousHash,
		RecordHash: fact.RecordHash, SignatureVersion: fact.SignatureVersion, Signature: fact.Signature, CreatedAt: fact.CreatedAt}
}

func auditLedgerRowToFact(row *auditLedgerRow) *domain.AuditFact {
	var payload map[string]any
	_ = json.Unmarshal(row.Payload, &payload)
	return &domain.AuditFact{ID: row.ID, HashVersion: row.HashVersion, ChainSequence: row.ChainSequence, Source: row.Source, SourceRegion: row.SourceRegion,
		PartitionKey: row.PartitionKey, EventID: idFromPtr(row.EventID), OperationID: idFromPtr(row.OperationID),
		CorrelationID: idFromPtr(row.CorrelationID), CausationID: idFromPtr(row.CausationID), TraceID: row.TraceID, RequestID: row.RequestID,
		ActorUID: row.ActorUID, AccountID: idFromPtr(row.AccountID), TenantID: row.TenantID, Action: row.Action,
		ResourceType: row.ResourceType, ResourceID: row.ResourceID, OccurredAt: row.OccurredAt, Payload: payload,
		PayloadHash: row.PayloadHash, PreviousHash: row.PreviousHash, RecordHash: row.RecordHash,
		SignatureVersion: row.SignatureVersion, Signature: row.Signature, CreatedAt: row.CreatedAt}
}

func validateAuditFact(fact *domain.AuditFact) error {
	if fact == nil || fact.Source == "" || fact.SourceRegion == "" || fact.PartitionKey == "" || fact.TenantID == "" ||
		fact.Action == "" || fact.OccurredAt.IsZero() {
		return fmt.Errorf("audit fact is incomplete")
	}
	return nil
}

func auditRecordHash(fact *domain.AuditFact) string {
	if fact.HashVersion <= 0 {
		value, _ := json.Marshal(struct {
			Sequence                                int64
			Source, Partition, Previous, Payload    string
			Action, ResourceType, ResourceID, Actor string
			Occurred                                time.Time
		}{fact.ChainSequence, fact.Source, fact.PartitionKey, fact.PreviousHash, fact.PayloadHash,
			fact.Action, fact.ResourceType, fact.ResourceID, fact.ActorUID, fact.OccurredAt.UTC()})
		return digest(value)
	}
	if fact.HashVersion == 1 {
		value, _ := json.Marshal(struct {
			Version                                                    int
			ID, Source, SourceRegion, Partition, Previous, Payload     string
			EventID, OperationID, CorrelationID, CausationID           string
			Actor, AccountID, Tenant, Action, ResourceType, ResourceID string
			Sequence                                                   int64
			Occurred, Created                                          time.Time
		}{fact.HashVersion, fact.ID.String(), fact.Source, fact.SourceRegion, fact.PartitionKey, fact.PreviousHash, fact.PayloadHash,
			fact.EventID.String(), fact.OperationID.String(), fact.CorrelationID.String(), fact.CausationID.String(), fact.ActorUID,
			fact.AccountID.String(), fact.TenantID, fact.Action, fact.ResourceType, fact.ResourceID, fact.ChainSequence,
			fact.OccurredAt, fact.CreatedAt})
		return digest(value)
	}
	value, _ := json.Marshal(struct {
		Version                                                               int
		ID, Source, SourceRegion, Partition, Previous, Payload                string
		EventID, OperationID, CorrelationID, CausationID                      string
		Actor, RequestID, AccountID, Tenant, Action, ResourceType, ResourceID string
		Sequence                                                              int64
		Occurred, Created                                                     time.Time
	}{fact.HashVersion, fact.ID.String(), fact.Source, fact.SourceRegion, fact.PartitionKey, fact.PreviousHash, fact.PayloadHash,
		fact.EventID.String(), fact.OperationID.String(), fact.CorrelationID.String(), fact.CausationID.String(), fact.ActorUID,
		fact.RequestID, fact.AccountID.String(), fact.TenantID, fact.Action, fact.ResourceType, fact.ResourceID, fact.ChainSequence,
		fact.OccurredAt.UTC(), fact.CreatedAt.UTC()})
	return digest(value)
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func sign(key []byte, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func nullableID(value domain.ID) *uuid.UUID {
	if value == (domain.ID{}) {
		return nil
	}
	result := uuid.UUID(value)
	return &result
}

func idFromPtr(value *uuid.UUID) domain.ID {
	if value == nil {
		return domain.ID{}
	}
	return *value
}

func (r *auditLedgerRepo) ChainPartitions(ctx context.Context) ([]ChainPartition, error) {
	var rows []struct {
		Source    string `gorm:"column:source"`
		Partition string `gorm:"column:partition_key"`
	}
	if err := r.db.WithContext(ctx).
		Model(&auditLedgerRow{}).
		Select("DISTINCT source, partition_key").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ChainPartition, 0, len(rows))
	for _, row := range rows {
		out = append(out, ChainPartition{Source: row.Source, Partition: row.Partition})
	}
	return out, nil
}

func (r *auditLedgerRepo) ChainHead(ctx context.Context, source, partition string) (*domain.AuditFact, error) {
	var row auditLedgerRow
	if err := r.db.WithContext(ctx).
		Where("source = ? AND partition_key = ?", source, partition).
		Order("chain_sequence DESC, id DESC").
		First(&row).Error; err != nil {
		return nil, err
	}
	return auditLedgerRowToFact(&row), nil
}
