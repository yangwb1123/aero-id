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
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/datatypes"
	"gorm.io/gorm"

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
	FinishPublication(context.Context, domain.ID, string, int64, int, string, string, time.Time) error
	// RequeuePublication moves dead_letter publications back to pending
	// (attempt reset to 0, lease cleared, error_code manual_replay) with a
	// mandatory operator intent: one in-tx audit fact per affected tenant
	// (fact tenant derived from the rows); idempotent by the dead_letter
	// status predicate.
	RequeuePublication(context.Context, []domain.ID, int, ReplayIntent) (int, []*domain.AuditFact, error)
	// CountDeadLetterPublications returns exactly the number of rows the next
	// RequeuePublication call would affect (bounded by ids/limit/tenant —
	// dry-run support).
	CountDeadLetterPublications(context.Context, []domain.ID, int, string) (int, error)
	// DeadLetterTenants lists the distinct tenants of the rows the next
	// requeue would affect (dry-run report + friendly binding pre-flight).
	DeadLetterTenants(context.Context, []domain.ID, int, string) ([]string, error)
	// PurgeDeadLetterPublications deletes dead_letter rows past cutoff, guarded
	// by lease-owner empty and lease expiry; bounded and keyset-ordered.
	PurgeDeadLetterPublications(context.Context, time.Time, int) (int64, error)
}

type AuditLedgerConfig struct{ SigningKey string }

var ErrAuditDuplicate = errors.New("audit ledger: duplicate source event")
var ErrAuditPublicationLeaseLost = errors.New("audit publication lease lost")

// ErrAuditPublicationFactHydration wraps a post-claim fact-hydration failure
// (FM-12): the claim tx committed (every row in the batch is delivering) but
// loading a ledger fact failed partway. ClaimPublication returns the hydrated
// prefix alongside the wrapped error; the drain delivers the prefix and the
// remainder is re-claimed after lease expiry — attempt untouched (M-4).
var ErrAuditPublicationFactHydration = errors.New("audit publication fact hydration failed")

type auditLedgerRepo struct {
	db         *gorm.DB
	signingKey []byte
}

func NewAuditLedgerRepository(db *gorm.DB, cfg AuditLedgerConfig) FullAuditLedgerRepository {
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

func (r *auditLedgerRepo) Append(ctx context.Context, fact *domain.AuditFact) error {
	if len(r.signingKey) == 0 {
		return errors.New("audit ledger signing key is not configured")
	}
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
	if len(r.signingKey) == 0 {
		return errors.New("audit ledger signing key is not configured")
	}
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
		// The unique constraint uq_audit_ledger_tenant_event is the final guard
		// against duplicate (tenant_id, source, event_id). checkAuditDuplicate
		// is only a pre-check: two concurrent appends on different partitions
		// take different advisory locks, so the loser's pre-check cannot see the
		// winner's uncommitted row and its INSERT fails here with a raw 23505.
		// Translate it so the service-level duplicate contract holds (C1); a
		// uq_audit_ledger_chain_seq violation is impossible under the partition
		// advisory lock, and a publication-site violation stays raw (internal
		// corruption).
		//
		// The mapping is constraint-precise on purpose: any other 23505 (audit
		// ledger PK collision, uq_audit_ledger_chain_seq, publication PK) stays
		// raw so the enclosing transaction rolls back. A broad gorm.ErrDuplicatedKey
		// mapping would let an in-tx caller swallow those as ErrAuditDuplicate and
		// commit the domain row without its audit row — the silent-drop invariant.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_audit_ledger_tenant_event" {
			return ErrAuditDuplicate
		}
		return fmt.Errorf("append audit ledger: %w", err)
	}
	publication := &auditPublicationRow{ID: idgen.NewID(), FactID: fact.ID, Status: "pending", NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(publication).Error; err != nil {
		return fmt.Errorf("create audit publication: %w", err)
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
	if len(r.signingKey) == 0 {
		return errors.New("audit ledger signing key is not configured")
	}
	var rows []auditLedgerRow
	if err := r.db.WithContext(ctx).Where("source = ? AND partition_key = ?", source, partition).Order("chain_sequence ASC").Find(&rows).Error; err != nil {
		return err
	}
	previous := ""
	lastSignatureVersion := 0
	for _, row := range rows {
		next, version, err := verifyAuditRow(r.signingKey, previous, lastSignatureVersion, &row)
		if err != nil {
			return err
		}
		previous = next
		lastSignatureVersion = version
	}
	return nil
}

// verifyAuditRow validates one chain row: payload hash, hash-chain linkage,
// monotone signature_version (a downgrade would strip the key-bound HMAC), and
// the HMAC itself for signed rows. Legacy unsigned rows (version 0) may precede
// signed rows (version 1) but never follow them. It returns the row's record
// hash and signature version for the next iteration.
func verifyAuditRow(key []byte, previous string, lastSignatureVersion int, row *auditLedgerRow) (string, int, error) {
	fact := auditLedgerRowToFact(row)
	payload, err := canonicalAuditPayload(row.Payload)
	if err != nil || fact.PayloadHash != digest(payload) {
		return "", 0, fmt.Errorf("audit payload verification failed at %s", row.ID)
	}
	if fact.PreviousHash != previous || fact.RecordHash != auditRecordHash(fact) {
		return "", 0, fmt.Errorf("audit chain verification failed at %s", row.ID)
	}
	if fact.SignatureVersion < lastSignatureVersion {
		return "", 0, fmt.Errorf("audit signature version downgrade at %s", row.ID)
	}
	if fact.SignatureVersion >= 1 && !hmac.Equal([]byte(fact.Signature), []byte(sign(key, fact.RecordHash))) {
		return "", 0, fmt.Errorf("audit signature verification failed at %s", row.ID)
	}
	return fact.RecordHash, fact.SignatureVersion, nil
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
