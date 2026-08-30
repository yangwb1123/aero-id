package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

const (
	QuotaSagaReserving      = "reserving"
	QuotaSagaPersisted      = "persisted"
	QuotaSagaCommitted      = "committed"
	QuotaSagaReleasePending = "release_pending"
	QuotaSagaReleased       = "released"
	QuotaSagaExpired        = "expired"
)

var (
	ErrQuotaAttemptActive = errors.New("account quota attempt is already being compensated")
	ErrQuotaAttemptStale  = errors.New("account quota attempt is stale")
)

type AccountQuotaSaga struct {
	ID              domain.ID
	TenantID        string
	Issuer          string
	Subject         string
	ReservationID   string
	AccountID       *domain.ID
	State           string
	ReleaseAttempt  int
	LeaseOwner      string
	LeaseUntil      *time.Time
	LeaseGeneration int64
}

type AccountQuotaSagaRepository interface {
	BeginAccountQuotaAttempt(context.Context, string, string, string, string) (*AccountQuotaSaga, error)
	MarkAccountQuotaPersisted(context.Context, string, string, domain.ID) error
	MarkAccountQuotaSagaCommitted(context.Context, string, string) error
	MarkAccountQuotaSagaExpired(context.Context, string, string) error
	ScheduleAccountQuotaRelease(context.Context, string, string) (bool, error)
	ScheduleAccountQuotaReleaseOwned(context.Context, domain.ID, int64) (bool, error)
	FinishAccountQuotaRelease(context.Context, string, string, bool, error) error
	ClaimAccountQuotaWork(context.Context, string, time.Duration) (*AccountQuotaSaga, error)
	FinishAccountQuotaWork(context.Context, string, domain.ID, int64, bool, error) error
}

type accountQuotaSagaRow struct {
	ID              uuid.UUID  `gorm:"column:id;primaryKey"`
	TenantID        string     `gorm:"column:tenant_id"`
	Issuer          string     `gorm:"column:issuer"`
	Subject         string     `gorm:"column:subject"`
	ReservationID   string     `gorm:"column:reservation_id;uniqueIndex"`
	AccountID       *uuid.UUID `gorm:"column:account_id"`
	State           string     `gorm:"column:state"`
	ReleaseAttempt  int        `gorm:"column:release_attempt"`
	NextAttemptAt   time.Time  `gorm:"column:next_attempt_at"`
	LeaseOwner      string     `gorm:"column:lease_owner"`
	LeaseUntil      *time.Time `gorm:"column:lease_until"`
	LeaseGeneration int64      `gorm:"column:lease_generation"`
	LastError       string     `gorm:"column:last_error"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (accountQuotaSagaRow) TableName() string { return "account_quota_sagas" }

func (r *accountRepo) BeginAccountQuotaAttempt(ctx context.Context, tenantID, issuer, subject, reservationID string) (*AccountQuotaSaga, error) {
	var result *AccountQuotaSaga
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := beginQuotaAttemptTx(tx, tenantID, issuer, subject, reservationID)
		if err != nil {
			return err
		}
		result = sagaRowToDomain(row)
		return nil
	})
	return result, err
}

func beginQuotaAttemptTx(tx *gorm.DB, tenantID, issuer, subject, reservationID string) (*accountQuotaSagaRow, error) {
	var row accountQuotaSagaRow
	query := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where(
		"tenant_id = ? AND issuer = ? AND subject = ?", tenantID, issuer, subject).First(&row)
	if errors.Is(query.Error, gorm.ErrRecordNotFound) {
		return createQuotaAttemptTx(tx, tenantID, issuer, subject, reservationID)
	}
	if query.Error != nil {
		return nil, query.Error
	}
	return resumeQuotaAttemptTx(tx, &row, reservationID)
}

func createQuotaAttemptTx(tx *gorm.DB, tenantID, issuer, subject, reservationID string) (*accountQuotaSagaRow, error) {
	now := time.Now().UTC()
	row := &accountQuotaSagaRow{ID: idgen.NewID(), TenantID: tenantID, Issuer: issuer, Subject: subject,
		ReservationID: reservationID, State: QuotaSagaReserving, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(row).Error; err != nil {
		return nil, fmt.Errorf("create account quota saga: %w", err)
	}
	return row, nil
}

func resumeQuotaAttemptTx(tx *gorm.DB, row *accountQuotaSagaRow, reservationID string) (*accountQuotaSagaRow, error) {
	if row.State == QuotaSagaReserving {
		if activeQuotaLease(row) {
			return nil, ErrQuotaAttemptActive
		}
		if err := refreshQuotaAttemptTx(tx, row); err != nil {
			return nil, err
		}
	}
	return resumeQuotaState(tx, row, reservationID)
}

func activeQuotaLease(row *accountQuotaSagaRow) bool {
	return row.LeaseUntil != nil && row.LeaseUntil.After(time.Now().UTC())
}

func refreshQuotaAttemptTx(tx *gorm.DB, row *accountQuotaSagaRow) error {
	now := time.Now().UTC()
	if err := tx.Model(&accountQuotaSagaRow{}).Where("id = ?", row.ID).
		Updates(map[string]any{"updated_at": now}).Error; err != nil {
		return err
	}
	row.UpdatedAt = now
	return nil
}

func resumeQuotaState(tx *gorm.DB, row *accountQuotaSagaRow, reservationID string) (*accountQuotaSagaRow, error) {
	switch row.State {
	case QuotaSagaReleasePending:
		return nil, ErrQuotaAttemptActive
	case QuotaSagaReleased:
		return renewQuotaAttemptTx(tx, row, reservationID)
	default:
		return row, nil
	}
}

func renewQuotaAttemptTx(tx *gorm.DB, row *accountQuotaSagaRow, reservationID string) (*accountQuotaSagaRow, error) {
	row.ID = idgen.NewID()
	row.ReservationID = reservationID + "-" + row.ID.String()
	row.State, row.AccountID = QuotaSagaReserving, nil
	row.ReleaseAttempt, row.LastError = 0, ""
	now := time.Now().UTC()
	row.NextAttemptAt, row.UpdatedAt = now, now
	if err := tx.Save(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

func (r *accountRepo) MarkAccountQuotaPersisted(ctx context.Context, tenantID, reservationID string, accountID domain.ID) error {
	result := r.db.WithContext(ctx).Model(&accountQuotaSagaRow{}).Where(
		"tenant_id = ? AND reservation_id = ? AND state = ?", tenantID, reservationID, QuotaSagaReserving).
		Updates(map[string]any{"account_id": accountID, "state": QuotaSagaPersisted, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var row accountQuotaSagaRow
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND reservation_id = ?", tenantID, reservationID).First(&row).Error; err != nil {
		return err
	}
	if row.State == QuotaSagaPersisted && row.AccountID != nil && *row.AccountID == accountID {
		return nil
	}
	return ErrQuotaAttemptStale
}

func markQuotaSagaPersistedTx(tx *gorm.DB, tenantID, reservationID string, accountID domain.ID) error {
	result := tx.Model(&accountQuotaSagaRow{}).Where("tenant_id = ? AND reservation_id = ? AND state = ? AND lease_owner = ''",
		tenantID, reservationID, QuotaSagaReserving).Updates(map[string]any{
		"account_id": accountID, "state": QuotaSagaPersisted, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var row accountQuotaSagaRow
		if err := tx.Where("tenant_id = ? AND reservation_id = ?", tenantID, reservationID).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if row.State == QuotaSagaPersisted && row.AccountID != nil && *row.AccountID == accountID {
			return nil
		}
		return ErrQuotaAttemptStale
	}
	return nil
}

func (r *accountRepo) MarkAccountQuotaSagaCommitted(ctx context.Context, tenantID, reservationID string) error {
	result := r.db.WithContext(ctx).Model(&accountQuotaSagaRow{}).Where(
		"tenant_id = ? AND reservation_id = ? AND state IN ?", tenantID, reservationID,
		[]string{QuotaSagaPersisted, QuotaSagaCommitted}).Updates(map[string]any{
		"state": QuotaSagaCommitted, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var row accountQuotaSagaRow
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND reservation_id = ?", tenantID, reservationID).First(&row).Error; err != nil {
		return err
	}
	if row.State == QuotaSagaCommitted {
		return nil
	}
	return ErrQuotaAttemptStale
}

func (r *accountRepo) MarkAccountQuotaSagaExpired(ctx context.Context, tenantID, reservationID string) error {
	result := r.db.WithContext(ctx).Model(&accountQuotaSagaRow{}).Where(
		"tenant_id = ? AND reservation_id = ? AND state = ?", tenantID, reservationID, QuotaSagaPersisted).
		Updates(map[string]any{"state": QuotaSagaExpired, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var row accountQuotaSagaRow
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND reservation_id = ?", tenantID, reservationID).First(&row).Error; err != nil {
		return err
	}
	if row.State == QuotaSagaExpired {
		return nil
	}
	return ErrQuotaAttemptStale
}

func (r *accountRepo) ScheduleAccountQuotaRelease(ctx context.Context, tenantID, reservationID string) (bool, error) {
	result := r.db.WithContext(ctx).Model(&accountQuotaSagaRow{}).Where(
		"tenant_id = ? AND reservation_id = ? AND state = ? AND lease_owner = ''", tenantID, reservationID, QuotaSagaReserving).
		Updates(map[string]any{"state": QuotaSagaReleasePending, "next_attempt_at": time.Now().UTC(), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (r *accountRepo) ScheduleAccountQuotaReleaseOwned(ctx context.Context, id domain.ID, generation int64) (bool, error) {
	result := r.db.WithContext(ctx).Model(&accountQuotaSagaRow{}).Where(
		"id = ? AND state = ? AND lease_generation = ? AND lease_owner <> ''", id, QuotaSagaReserving, generation).
		Updates(map[string]any{"state": QuotaSagaReleasePending, "next_attempt_at": time.Now().UTC(), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 1 {
		return true, nil
	}
	return false, nil
}

func (r *accountRepo) FinishAccountQuotaRelease(ctx context.Context, tenantID, reservationID string, released bool, workErr error) error {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if released {
		updates["state"], updates["last_error"] = QuotaSagaReleased, ""
	} else {
		updates["next_attempt_at"] = time.Now().UTC().Add(releaseRetryDelay(1))
		if workErr != nil {
			updates["last_error"] = truncateSagaError(workErr.Error())
		}
	}
	result := r.db.WithContext(ctx).Model(&accountQuotaSagaRow{}).Where(
		"tenant_id = ? AND reservation_id = ? AND state = ?", tenantID, reservationID, QuotaSagaReleasePending).Updates(updates)
	return result.Error
}

func (r *accountRepo) ClaimAccountQuotaWork(ctx context.Context, owner string, lease time.Duration) (*AccountQuotaSaga, error) {
	if lease <= 0 {
		lease = time.Minute
	}
	var row accountQuotaSagaRow
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: "SKIP LOCKED"}).Where(
			"(state = ? AND next_attempt_at <= ?) OR (state = ? AND updated_at < ?)",
			QuotaSagaReleasePending, time.Now().UTC(), QuotaSagaReserving, time.Now().UTC().Add(-lease)).First(&row)
		if q.Error != nil {
			return q.Error
		}
		now := time.Now().UTC()
		return tx.Model(&accountQuotaSagaRow{}).Where("id = ?", row.ID).Updates(map[string]any{
			"lease_owner": owner, "lease_until": now.Add(lease), "lease_generation": gorm.Expr("lease_generation + 1"), "updated_at": now}).Error
	})
	if err != nil {
		return nil, err
	}
	row.LeaseOwner, row.LeaseGeneration = owner, row.LeaseGeneration+1
	return sagaRowToDomain(&row), nil
}

func (r *accountRepo) FinishAccountQuotaWork(ctx context.Context, owner string, id domain.ID, generation int64, released bool, workErr error) error {
	updates := map[string]any{"lease_owner": "", "lease_until": nil, "updated_at": time.Now().UTC()}
	if released {
		updates["state"], updates["last_error"] = QuotaSagaReleased, ""
	} else {
		updates["release_attempt"] = gorm.Expr("release_attempt + 1")
		updates["next_attempt_at"] = time.Now().UTC().Add(releaseRetryDelay(1))
		if workErr != nil {
			updates["last_error"] = truncateSagaError(workErr.Error())
		}
	}
	result := r.db.WithContext(ctx).Model(&accountQuotaSagaRow{}).Where(
		"id = ? AND lease_owner = ? AND lease_generation = ?", id, owner, generation).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrQuotaAttemptStale
	}
	return nil
}

func releaseRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	return time.Duration(1<<(attempt-1)) * time.Second
}

func truncateSagaError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1024 {
		return value[:1024]
	}
	return value
}

func sagaRowToDomain(row *accountQuotaSagaRow) *AccountQuotaSaga {
	var accountID *domain.ID
	if row.AccountID != nil {
		value := domain.ID(*row.AccountID)
		accountID = &value
	}
	return &AccountQuotaSaga{ID: row.ID, TenantID: row.TenantID, Issuer: row.Issuer, Subject: row.Subject,
		ReservationID: row.ReservationID, AccountID: accountID, State: row.State, ReleaseAttempt: row.ReleaseAttempt,
		LeaseOwner: row.LeaseOwner, LeaseUntil: row.LeaseUntil, LeaseGeneration: row.LeaseGeneration}
}

var _ AccountQuotaSagaRepository = (*accountRepo)(nil)
