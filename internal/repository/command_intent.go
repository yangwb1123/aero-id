package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var (
	ErrCommandIntentConflict = errors.New("command intent version or confirmation conflict")
	ErrCommandIntentStale    = errors.New("command intent local preconditions changed")
	ErrCommandIntentExpired  = errors.New("command intent expired")
	ErrCommandIntentTooLarge = errors.New("command intent metadata exceeds budget")
)

type CommandIntentExecution struct {
	Intent             *domain.CommandIntent
	ExpectedVersion    int64
	ConfirmationDigest string
	Operation          *domain.Operation
	Job                *domain.SyncJob
	RequestedFact      *domain.AuditFact
	ExecutedFact       *domain.AuditFact
}

type CommandIntentRepository interface {
	CreateWithAudit(context.Context, *domain.CommandIntent, *domain.AuditFact) error
	Get(context.Context, domain.CommandPrincipal, domain.ID) (*domain.CommandIntent, error)
	FindByKey(context.Context, domain.CommandPrincipal, string) (*domain.CommandIntent, error)
	Capture(context.Context, string, domain.ID, []domain.AccountDataset) (domain.CommandPreconditions, error)
	Execute(context.Context, CommandIntentExecution) (*domain.CommandIntent, error)
}

type commandIntentRepo struct {
	db    *gorm.DB
	audit AuditLedgerTransactionalRepository
}

func NewCommandIntentRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) CommandIntentRepository {
	return &commandIntentRepo{db: db, audit: requireAuditLedger("NewCommandIntentRepository", audit)}
}

type commandIntentRow struct {
	ID                 domain.ID      `gorm:"column:id;primaryKey"`
	AccountID          domain.ID      `gorm:"column:account_id"`
	TenantID           string         `gorm:"column:tenant_id"`
	ActorUID           string         `gorm:"column:actor_uid"`
	Issuer             string         `gorm:"column:issuer"`
	Subject            string         `gorm:"column:subject"`
	ClientID           string         `gorm:"column:client_id"`
	IdempotencyKey     string         `gorm:"column:idempotency_key"`
	RequestFingerprint string         `gorm:"column:request_fingerprint"`
	Version            int64          `gorm:"column:version"`
	Status             string         `gorm:"column:status"`
	Document           datatypes.JSON `gorm:"column:document"`
	ReceiptKind        string         `gorm:"column:receipt_kind"`
	ReceiptID          *domain.ID     `gorm:"column:receipt_id"`
	CreatedAt          time.Time      `gorm:"column:created_at"`
	ExpiresAt          time.Time      `gorm:"column:expires_at"`
}

func (commandIntentRow) TableName() string { return "command_intents" }

func intentPrincipalQuery(tx *gorm.DB, p domain.CommandPrincipal) *gorm.DB {
	return tx.Where("tenant_id = ? AND actor_uid = ? AND issuer = ? AND subject = ? AND client_id = ?",
		p.TenantID, p.ActorUID, p.Issuer, p.Subject, p.ClientID)
}

func (r *commandIntentRepo) CreateWithAudit(ctx context.Context, intent *domain.CommandIntent, fact *domain.AuditFact) error {
	row, err := commandIntentToRow(intent)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := captureCommandPreconditionsTx(tx, intent.Principal.TenantID, intent.AccountID, intent.Plan.Datasets)
		if err != nil {
			return err
		}
		if !sameCommandPreconditions(current, intent.Preconditions) {
			return ErrCommandIntentStale
		}
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		return r.audit.AppendInTx(ctx, tx, fact)
	})
}

func (r *commandIntentRepo) Get(ctx context.Context, principal domain.CommandPrincipal, id domain.ID) (*domain.CommandIntent, error) {
	var row commandIntentRow
	if err := intentPrincipalQuery(r.db.WithContext(ctx), principal).Where("id = ?", id).Take(&row).Error; err != nil {
		return nil, err
	}
	return commandIntentFromRow(&row)
}

func (r *commandIntentRepo) FindByKey(ctx context.Context, principal domain.CommandPrincipal, key string) (*domain.CommandIntent, error) {
	var row commandIntentRow
	if err := intentPrincipalQuery(r.db.WithContext(ctx), principal).Where("idempotency_key = ?", key).Take(&row).Error; err != nil {
		return nil, err
	}
	return commandIntentFromRow(&row)
}

func commandIntentToRow(intent *domain.CommandIntent) (*commandIntentRow, error) {
	data, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	if len(data) > 256*1024 {
		return nil, ErrCommandIntentTooLarge
	}
	p := intent.Principal
	return &commandIntentRow{ID: intent.ID, AccountID: intent.AccountID, TenantID: p.TenantID, ActorUID: p.ActorUID,
		Issuer: p.Issuer, Subject: p.Subject, ClientID: p.ClientID, IdempotencyKey: intent.IdempotencyKey,
		RequestFingerprint: intent.RequestFingerprint, Version: intent.Version, Status: intent.Status, Document: data,
		CreatedAt: intent.CreatedAt, ExpiresAt: intent.ExpiresAt}, nil
}
