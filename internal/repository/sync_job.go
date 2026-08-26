package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type SyncJobRepository interface {
	Create(context.Context, *domain.SyncJob) error
	FindByIdempotency(context.Context, string, string, string) (*domain.SyncJob, error)
	Get(context.Context, domain.ID) (*domain.SyncJob, error)
	List(context.Context, string, string, domain.Page) ([]*domain.SyncJob, string, error)
	Claim(context.Context, domain.ID, string, time.Duration) (*domain.SyncJob, error)
	ClaimUnknown(context.Context, domain.ID, string, time.Duration) (*domain.SyncJob, error)
	ClaimUnknownNext(context.Context, string, time.Duration) (*domain.SyncJob, error)
	ClaimNext(context.Context, string, time.Duration) (*domain.SyncJob, error)
	Finish(context.Context, domain.ID, string, domain.SyncJobStatus, string, string, map[string]any) error
	ListExpiredExports(context.Context, time.Time, int) ([]*domain.SyncJob, error)
	ClearExportArtifact(context.Context, domain.ID, string) error
}

// RegionalSyncJobWorkerRepository prevents a regional worker from claiming a
// job whose account belongs to another home region. Implementations may keep
// ClaimNext for compatibility with manually driven callers.
type RegionalSyncJobWorkerRepository interface {
	ClaimNextForRegion(context.Context, string, time.Duration, string) (*domain.SyncJob, error)
	ClaimUnknownNextForRegion(context.Context, string, time.Duration, string) (*domain.SyncJob, error)
}

type SyncJobAuditRepository interface {
	CreateWithAudit(context.Context, *domain.SyncJob, *domain.AuditFact) error
	FinishWithAudit(context.Context, domain.ID, string, domain.SyncJobStatus, string, string, map[string]any, *domain.AuditFact) error
}

type syncJobRepo struct {
	db    *gorm.DB
	audit AuditLedgerTransactionalRepository
}

var ErrSyncJobLeaseLost = errors.New("sync job lease lost")
var ErrSyncJobUnknown = errors.New("sync job outcome is unknown")

func NewSyncJobRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) SyncJobRepository {
	return &syncJobRepo{db: db, audit: requireAuditLedger("NewSyncJobRepository", audit)}
}

type syncJobRow struct {
	ID             uuid.UUID      `gorm:"column:id;primaryKey"`
	AccountID      uuid.UUID      `gorm:"column:account_id"`
	ActorUID       string         `gorm:"column:actor_uid"`
	TenantID       string         `gorm:"column:tenant_id"`
	RequestID      string         `gorm:"column:request_id"`
	JobType        string         `gorm:"column:job_type"`
	Status         string         `gorm:"column:status"`
	IdempotencyKey string         `gorm:"column:idempotency_key"`
	Request        datatypes.JSON `gorm:"column:request"`
	Result         datatypes.JSON `gorm:"column:result"`
	ErrorCode      string         `gorm:"column:error_code"`
	ErrorMessage   string         `gorm:"column:error_message"`
	Attempt        int            `gorm:"column:attempt"`
	LeaseOwner     string         `gorm:"column:lease_owner"`
	LeaseUntil     *time.Time     `gorm:"column:lease_until"`
	LeaseKind      string         `gorm:"column:lease_kind"`
	CreatedAt      time.Time      `gorm:"column:created_at"`
	UpdatedAt      time.Time      `gorm:"column:updated_at"`
	CompletedAt    *time.Time     `gorm:"column:completed_at"`
}

func (syncJobRow) TableName() string { return "sync_jobs" }

func (r *syncJobRepo) Create(ctx context.Context, job *domain.SyncJob) error {
	return r.create(ctx, job, nil)
}

func (r *syncJobRepo) CreateWithAudit(ctx context.Context, job *domain.SyncJob, fact *domain.AuditFact) error {
	if r.audit == nil {
		return fmt.Errorf("transactional audit repository is not configured")
	}
	return r.create(ctx, job, fact)
}

func (r *syncJobRepo) create(ctx context.Context, job *domain.SyncJob, fact *domain.AuditFact) error {
	now := time.Now().UTC()
	if job.ID == (domain.ID{}) {
		job.ID = idgen.NewID()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	row, err := syncJobToRow(job)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		if fact != nil {
			// ErrAuditDuplicate is swallowed in-tx (contract §4.2): a duplicate
			// append implies the retried create already committed with identical
			// anchor inputs, so the retry is a no-op-equivalent re-apply; the
			// append runs after the domain write inside the same tx, so the
			// swallow can never mask a failed domain write.
			if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
				return fmt.Errorf("append sync job audit: %w", err)
			}
		}
		return nil
	})
}

func (r *syncJobRepo) FindByIdempotency(ctx context.Context, actor, tenant, key string) (*domain.SyncJob, error) {
	var row syncJobRow
	if err := r.db.WithContext(ctx).Where("actor_uid = ? AND tenant_id = ? AND idempotency_key = ?", actor, tenant, key).First(&row).Error; err != nil {
		return nil, err
	}
	return syncJobFromRow(&row)
}

func (r *syncJobRepo) Get(ctx context.Context, id domain.ID) (*domain.SyncJob, error) {
	var row syncJobRow
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return syncJobFromRow(&row)
}

func (r *syncJobRepo) List(ctx context.Context, actor, tenant string, page domain.Page) ([]*domain.SyncJob, string, error) {
	page.Normalize()
	query := r.db.WithContext(ctx).Where("actor_uid = ? AND tenant_id = ?", actor, tenant).Order("id DESC")
	if page.Cursor != "" {
		query = query.Where("id < ?", page.Cursor)
	}
	var rows []syncJobRow
	if err := query.Limit(page.Limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > page.Limit {
		next = rows[page.Limit-1].ID.String()
		rows = rows[:page.Limit]
	}
	result := make([]*domain.SyncJob, len(rows))
	for i := range rows {
		converted, err := syncJobFromRow(&rows[i])
		if err != nil {
			return nil, "", err
		}
		result[i] = converted
	}
	return result, next, nil
}

func (r *syncJobRepo) Claim(ctx context.Context, id domain.ID, worker string, lease time.Duration) (*domain.SyncJob, error) {
	var result *domain.SyncJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row syncJobRow
		now := time.Now().UTC()
		query := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Where("id = ? AND status = ?", id, string(domain.SyncJobPending)).First(&row)
		if query.Error != nil {
			return query.Error
		}
		until := now.Add(lease)
		if err := tx.Model(&row).Updates(map[string]any{"status": string(domain.SyncJobRunning), "attempt": gorm.Expr("attempt + 1"),
			"lease_owner": worker, "lease_until": until, "lease_kind": "execute", "completed_at": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		row.Status = string(domain.SyncJobRunning)
		row.Attempt++
		row.LeaseOwner = worker
		row.LeaseUntil = &until
		row.LeaseKind = "execute"
		result, _ = syncJobFromRow(&row)
		return nil
	})
	return result, err
}

func (r *syncJobRepo) ClaimUnknown(ctx context.Context, id domain.ID, worker string, lease time.Duration) (*domain.SyncJob, error) {
	var result *domain.SyncJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row syncJobRow
		now := time.Now().UTC()
		query := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Where("id = ? AND ((status = ? AND (lease_until IS NULL OR lease_until < ?)) OR (status = ? AND lease_kind = ? AND lease_until < ?))",
				id, string(domain.SyncJobUnknown), now, string(domain.SyncJobRunning), "reconcile", now).First(&row)
		if query.Error != nil {
			return query.Error
		}
		until := now.Add(lease)
		if err := tx.Model(&row).Updates(map[string]any{"status": string(domain.SyncJobRunning), "attempt": gorm.Expr("attempt + 1"),
			"lease_owner": worker, "lease_until": until, "lease_kind": "reconcile", "completed_at": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		row.Status = string(domain.SyncJobRunning)
		row.Attempt++
		row.LeaseOwner = worker
		row.LeaseUntil = &until
		row.LeaseKind = "reconcile"
		result, _ = syncJobFromRow(&row)
		return nil
	})
	return result, err
}

func (r *syncJobRepo) ClaimUnknownNext(ctx context.Context, worker string, lease time.Duration) (*domain.SyncJob, error) {
	var result *domain.SyncJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row syncJobRow
		now := time.Now().UTC()
		query := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Where("job_type = ? AND ((status = ? AND (lease_until IS NULL OR lease_until < ?)) OR (status = ? AND lease_kind = ? AND lease_until < ?))",
				string(domain.SyncJobErase), string(domain.SyncJobUnknown), now, string(domain.SyncJobRunning), "reconcile", now).
			Order("updated_at ASC, id ASC").First(&row)
		if query.Error != nil {
			return query.Error
		}
		until := now.Add(lease)
		if err := tx.Model(&row).Updates(map[string]any{"status": string(domain.SyncJobRunning), "attempt": gorm.Expr("attempt + 1"),
			"lease_owner": worker, "lease_until": until, "lease_kind": "reconcile", "completed_at": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		row.Status, row.Attempt, row.LeaseOwner, row.LeaseUntil, row.LeaseKind = string(domain.SyncJobRunning), row.Attempt+1, worker, &until, "reconcile"
		result, _ = syncJobFromRow(&row)
		return nil
	})
	return result, err
}

func (r *syncJobRepo) ClaimNext(ctx context.Context, worker string, lease time.Duration) (*domain.SyncJob, error) {
	var result *domain.SyncJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row syncJobRow
		now := time.Now().UTC()
		query := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Where("status = ?", string(domain.SyncJobPending)).
			Order("created_at ASC, id ASC").First(&row)
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			var stale syncJobRow
			staleQuery := tx.Where("status = ? AND lease_kind = ? AND lease_until < ?", string(domain.SyncJobRunning), "execute", now).
				Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
				Order("lease_until ASC, created_at ASC, id ASC").First(&stale)
			if errors.Is(staleQuery.Error, gorm.ErrRecordNotFound) {
				return query.Error
			}
			if staleQuery.Error != nil {
				return staleQuery.Error
			}
			if err := tx.Model(&stale).Updates(map[string]any{"status": string(domain.SyncJobUnknown),
				"error_code": "sync.unknown_outcome", "error_message": "worker lease expired before the result was recorded",
				"lease_owner": "", "lease_until": nil, "lease_kind": "", "updated_at": now}).Error; err != nil {
				return err
			}
			return ErrSyncJobUnknown
		}
		if query.Error != nil {
			return query.Error
		}
		until := now.Add(lease)
		if err := tx.Model(&row).Updates(map[string]any{"status": string(domain.SyncJobRunning), "attempt": gorm.Expr("attempt + 1"),
			"lease_owner": worker, "lease_until": until, "lease_kind": "execute", "updated_at": now}).Error; err != nil {
			return err
		}
		row.Status = string(domain.SyncJobRunning)
		row.Attempt++
		row.LeaseOwner = worker
		row.LeaseUntil = &until
		row.LeaseKind = "execute"
		result, _ = syncJobFromRow(&row)
		return nil
	})
	return result, err
}

func (r *syncJobRepo) Finish(ctx context.Context, id domain.ID, worker string, status domain.SyncJobStatus, code, message string, result map[string]any) error {
	return r.finish(ctx, id, worker, status, code, message, result, nil)
}

func (r *syncJobRepo) FinishWithAudit(ctx context.Context, id domain.ID, worker string, status domain.SyncJobStatus,
	code, message string, result map[string]any, fact *domain.AuditFact) error {
	if r.audit == nil {
		return fmt.Errorf("transactional audit repository is not configured")
	}
	return r.finish(ctx, id, worker, status, code, message, result, fact)
}

func (r *syncJobRepo) ListExpiredExports(ctx context.Context, now time.Time, limit int) ([]*domain.SyncJob, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows []syncJobRow
	if err := r.db.WithContext(ctx).Where("job_type IN ? AND status = ?",
		[]string{string(domain.SyncJobExport), string(domain.SyncJobAuditExport)}, string(domain.SyncJobCompleted)).
		Order("updated_at ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]*domain.SyncJob, 0, len(rows))
	for i := range rows {
		job, err := syncJobFromRow(&rows[i])
		if err != nil {
			return nil, err
		}
		expires, ok := syncJobExpiry(job.Result["expires_at"])
		if !ok || !now.Before(expires) {
			// Export artifacts are always TTL-bound. Treat a missing or
			// malformed legacy expiry as expired so cleanup cannot leave an
			// artifact that the download path will correctly refuse.
			result = append(result, job)
		}
	}
	return result, nil
}

func (r *syncJobRepo) ClearExportArtifact(ctx context.Context, id domain.ID, storageKey string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row syncJobRow
		if err := tx.Where("id = ? AND job_type IN ?", id,
			[]string{string(domain.SyncJobExport), string(domain.SyncJobAuditExport)}).First(&row).Error; err != nil {
			return err
		}
		result, err := syncJobFromRow(&row)
		if err != nil {
			return err
		}
		if current, ok := result.Result["_storage_key"].(string); !ok || current != storageKey {
			return nil
		}
		delete(result.Result, "_storage_key")
		result.Result["artifact_deleted_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		payload, err := json.Marshal(result.Result)
		if err != nil {
			return fmt.Errorf("marshal expired export result: %w", err)
		}
		return tx.Model(&syncJobRow{}).Where("id = ?", id).Updates(map[string]any{
			"result": datatypes.JSON(payload), "updated_at": time.Now().UTC(),
		}).Error
	})
}

func syncJobToRow(job *domain.SyncJob) (*syncJobRow, error) {
	request, err := json.Marshal(resultOrEmpty(job.Request))
	if err != nil {
		return nil, err
	}
	result, err := json.Marshal(resultOrEmpty(job.Result))
	if err != nil {
		return nil, err
	}
	return &syncJobRow{ID: job.ID, AccountID: job.AccountID, ActorUID: job.ActorUID, TenantID: job.TenantID,
		RequestID: job.RequestID, JobType: string(job.JobType), Status: string(job.Status),
		IdempotencyKey: job.IdempotencyKey, Request: datatypes.JSON(request), Result: datatypes.JSON(result), ErrorCode: job.ErrorCode,
		ErrorMessage: job.ErrorMessage, Attempt: job.Attempt, LeaseOwner: job.LeaseOwner, LeaseUntil: job.LeaseUntil,
		LeaseKind: job.LeaseKind,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt, CompletedAt: job.CompletedAt}, nil
}

func syncJobFromRow(row *syncJobRow) (*domain.SyncJob, error) {
	request, err := mapFromJSON(row.Request)
	if err != nil {
		return nil, err
	}
	result, err := mapFromJSON(row.Result)
	if err != nil {
		return nil, err
	}
	return &domain.SyncJob{ID: row.ID, AccountID: row.AccountID, ActorUID: row.ActorUID, TenantID: row.TenantID,
		RequestID: row.RequestID, JobType: domain.SyncJobType(row.JobType),
		Status: domain.SyncJobStatus(row.Status), IdempotencyKey: row.IdempotencyKey, Request: request, Result: result,
		ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage, Attempt: row.Attempt, LeaseOwner: row.LeaseOwner,
		LeaseUntil: row.LeaseUntil, LeaseKind: row.LeaseKind, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CompletedAt: row.CompletedAt}, nil
}

func syncJobExpiry(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, !typed.IsZero()
	case *time.Time:
		if typed == nil {
			return time.Time{}, false
		}
		return *typed, !typed.IsZero()
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(typed))
		return parsed, err == nil && !parsed.IsZero()
	default:
		return time.Time{}, false
	}
}

func resultOrEmpty(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func mapFromJSON(data []byte) (map[string]any, error) {
	result := map[string]any{}
	if len(data) == 0 {
		return result, nil
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}
