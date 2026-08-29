package repository

import (
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/event"
)

// ErrEventIntegrityConflict means an event ID was previously bound to
// different canonical content. It is deliberately content-free so callers
// cannot leak source payloads while reporting the conflict.
var ErrEventIntegrityConflict = errors.New("event id is bound to different content")

// ErrEventContentConflict is a compatibility alias for callers that describe
// the same invariant as a content conflict.
var ErrEventContentConflict = ErrEventIntegrityConflict

func eventContentMatches(stored, incoming string) error {
	if stored != "" && stored == incoming {
		return nil
	}
	// An empty stored hash is the explicit legacy/unbound marker. The old rows
	// did not retain enough canonical metadata to prove an exact replay, so the
	// safe behavior is to reject reuse rather than infer equality from redacted
	// fields.
	return ErrEventIntegrityConflict
}

func lockEventContentKey(db *gorm.DB, envelope *event.Envelope) error {
	key := "event-content:" + envelope.TenantID + ":" + envelope.Source + ":" + envelope.EventID.String()
	if err := db.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", key).Error; err != nil {
		return fmt.Errorf("lock event content key: %w", err)
	}
	return nil
}

func checkEventStoreContent(db *gorm.DB, envelope *event.Envelope, contentHash string) error {
	var existing eventStoreRow
	result := db.Where("tenant_id = ? AND source = ? AND event_id = ?",
		envelope.TenantID, envelope.Source, envelope.EventID).First(&existing)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	if result.Error != nil {
		return fmt.Errorf("check event store content: %w", result.Error)
	}
	return eventContentMatches(existing.ContentHash, contentHash)
}

func checkEventReceiptContent(db *gorm.DB, envelope *event.Envelope, contentHash string) error {
	var existing eventReceiptRow
	result := db.Where("tenant_id = ? AND source = ? AND event_id = ?",
		envelope.TenantID, envelope.Source, envelope.EventID).First(&existing)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	if result.Error != nil {
		return fmt.Errorf("check event receipt content: %w", result.Error)
	}
	return eventContentMatches(existing.ContentHash, contentHash)
}

func marshalRedactedEventPayload(envelope *event.Envelope) ([]byte, error) {
	if envelope == nil {
		return nil, errors.New("event is nil")
	}
	payload, err := json.Marshal(event.RedactPayload(envelope.Payload))
	if err != nil {
		return nil, errors.New("event payload is not serializable")
	}
	return payload, nil
}
