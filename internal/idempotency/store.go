// Package idempotency persists request/response records so that mutating HTTP
// requests carrying an Idempotency-Key can be safely retried: the first request
// runs and its response is stored; identical retries replay the stored response.
package idempotency

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// reservationTTL bounds how long an in-progress reservation is honored before it is
// considered stale (its leader likely crashed) and may be reclaimed by a retry.
const reservationTTL = 2 * time.Minute

// Record is the stored outcome of a request. Completed is false while a reservation
// is in flight; once the leader finishes, Status/Body/ContentType hold the response.
type Record struct {
	Status      int
	ContentType string
	Body        []byte
	Completed   bool
}

// Store reserves keys and persists responses for idempotent replay.
type Store interface {
	// Reserve claims (uid, endpoint, key). It returns reserved=true to the first caller
	// (which must run the handler and then Complete); subsequent callers get reserved=false
	// and the existing Record (Completed indicates whether a response is available).
	Reserve(ctx context.Context, uid, endpoint, key string) (rec *Record, reserved bool, err error)
	// Complete stores the final response against a reservation, keeping it for ttl.
	Complete(ctx context.Context, uid, endpoint, key string, rec Record, ttl time.Duration) error
	// Release removes a reservation so the client may retry (e.g. after a 5xx).
	Release(ctx context.Context, uid, endpoint, key string) error
}

type idempotencyRow struct {
	ID          uuid.UUID `gorm:"column:id;primaryKey"`
	UID         string    `gorm:"column:uid"`
	Endpoint    string    `gorm:"column:endpoint"`
	IdemKey     string    `gorm:"column:idem_key"`
	Status      int       `gorm:"column:status"`
	ContentType string    `gorm:"column:content_type"`
	Response    []byte    `gorm:"column:response"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	ExpiresAt   time.Time `gorm:"column:expires_at"`
}

func (idempotencyRow) TableName() string { return "idempotency_keys" }

// GormStore is the Postgres-backed Store. ExpiresAt is authoritative for both
// in-flight and completed records: once it passes, the key is reclaimed on the
// next Reserve, matching RedisStore's TTL behavior without waiting for the
// background cleaner.
type GormStore struct{ db *gorm.DB }

func NewGormStore(db *gorm.DB) *GormStore { return &GormStore{db: db} }

func (s *GormStore) Reserve(ctx context.Context, uid, endpoint, key string) (*Record, bool, error) {
	now := time.Now().UTC()
	row := idempotencyRow{
		ID: uuid.New(), UID: uid, Endpoint: endpoint, IdemKey: key,
		Status: 0, CreatedAt: now, ExpiresAt: now.Add(reservationTTL),
	}
	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return nil, false, fmt.Errorf("idempotency reserve: %w", res.Error)
	}
	if res.RowsAffected == 1 {
		return nil, true, nil // we claimed the key
	}

	// Conflict: inspect the existing record.
	var existing idempotencyRow
	if err := s.db.WithContext(ctx).
		Where("uid = ? AND endpoint = ? AND idem_key = ?", uid, endpoint, key).
		First(&existing).Error; err != nil {
		return nil, false, fmt.Errorf("idempotency load: %w", err)
	}
	// Reclaim any expired record, including a completed response whose replay TTL
	// elapsed. The CAS predicate includes the observed expires_at: the UPDATE
	// replaces it with a fresh future value, so at most one concurrent reclaimer
	// wins (losers match zero rows). Only the value read back from the DB is safe
	// here — Postgres stores microsecond-precision TIMESTAMPTZ, so a freshly
	// computed time would not round-trip stably.
	if now.After(existing.ExpiresAt) {
		updates := map[string]any{"created_at": now, "expires_at": now.Add(reservationTTL)}
		if existing.Status > 0 {
			updates["status"] = 0
			updates["content_type"] = ""
			updates["response"] = nil
		}
		reclaim := s.db.WithContext(ctx).Model(&idempotencyRow{}).
			Where("id = ? AND expires_at = ?", existing.ID, existing.ExpiresAt).
			Updates(updates)
		if reclaim.Error == nil && reclaim.RowsAffected == 1 {
			return nil, true, nil
		}
	}
	return &Record{
		Status:      existing.Status,
		ContentType: existing.ContentType,
		Body:        existing.Response,
		Completed:   existing.Status > 0,
	}, false, nil
}

func (s *GormStore) Complete(ctx context.Context, uid, endpoint, key string, rec Record, ttl time.Duration) error {
	res := s.db.WithContext(ctx).Model(&idempotencyRow{}).
		Where("uid = ? AND endpoint = ? AND idem_key = ?", uid, endpoint, key).
		Updates(map[string]any{
			"status":       rec.Status,
			"content_type": rec.ContentType,
			"response":     rec.Body,
			"expires_at":   time.Now().UTC().Add(ttl),
		})
	if res.Error != nil {
		return fmt.Errorf("idempotency complete: %w", res.Error)
	}
	return nil
}

func (s *GormStore) Release(ctx context.Context, uid, endpoint, key string) error {
	if err := s.db.WithContext(ctx).
		Where("uid = ? AND endpoint = ? AND idem_key = ?", uid, endpoint, key).
		Delete(&idempotencyRow{}).Error; err != nil {
		return fmt.Errorf("idempotency release: %w", err)
	}
	return nil
}

// RunCleaner starts a background goroutine that periodically purges expired
// idempotency key rows (whose expires_at is in the past). It returns a stop
// func that terminates the goroutine. A non-positive interval disables the
// cleaner and returns a no-op stop.
func (s *GormStore) RunCleaner(interval time.Duration) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	t := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-t.C:
				s.purgeExpired()
			case <-done:
				t.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}

// purgeExpired deletes all idempotency key rows whose expires_at is in the
// past. It runs in a best-effort fashion — errors are silently dropped because
// a missed tick is no worse than having the cleanup be slightly delayed.
func (s *GormStore) purgeExpired() {
	_ = s.db.Where("expires_at < ?", time.Now().UTC()).Delete(&idempotencyRow{}).Error
}
