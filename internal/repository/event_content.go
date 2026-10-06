package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
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
	encoded, err := json.Marshal(event.RedactPayload(envelope.Payload))
	if err != nil {
		return nil, errors.New("event payload is not serializable")
	}
	return encoded, nil
}

func marshalQuarantinedEventPayload(envelope *event.Envelope, details EventQuarantineDetails) ([]byte, error) {
	if envelope == nil {
		return nil, errors.New("event is nil")
	}
	// Quarantine is metadata-only. Never persist an arbitrary source payload,
	// even if it happens to pass the deny-list redactor.
	metadata := map[string]any{"stage": details.Stage, "identity_kind": QuarantineIdentityKind(envelope, details)}
	if details.OriginalWireID != "" {
		metadata["wire_event_id"] = boundedWireID(details.OriginalWireID)
	}
	if details.WireFingerprint != "" {
		metadata["wire_fingerprint"] = details.WireFingerprint
	}
	encoded, err := json.Marshal(map[string]any{"_quarantine": metadata})
	if err != nil {
		return nil, errors.New("quarantine metadata is not serializable")
	}
	return encoded, nil
}

// QuarantineIdentityKind is the single oracle for the identity_kind label of a
// quarantined event. The persisted receipt metadata (marshalQuarantinedEventPayload)
// and the durable audit fact payload (service.addQuarantineAuditMetadata) must
// agree, so both classify through this function.
//
//   - "canonical": the stored event identity is the identity the source supplied.
//   - "derived": the stored identity was minted locally (an Anchor surrogate) or
//     differs from the supplied one.
//
// Rules (both details fields are optional):
//   - Wire text present: canonical iff idgen.CanonicalEqual(envelope.EventID,
//     wireText). Rendering is never compared, so every text form idgen.Parse
//     accepts — urn:uuid:, upper-case, 32-hex, braced — is canonical.
//   - Wire text absent but a wire fingerprint present: derived. A fingerprint is
//     written only on the gRPC canonicalization path
//     (grpc.finalizeWireIdentity), where an absent/blank wire event_id is
//     exactly the condition under which that path anchors
//     idgen.Anchor("event.quarantine.v1", fingerprint); the stored identity is
//     then derived by construction.
//   - Wire text absent and no fingerprint: canonical. This is the HTTP/NATS
//     service-API quarantine path (service.Ingest passes empty details and only
//     the stage is filled in), where the identity came from the submitted
//     envelope and nothing was derived.
//
// A nil envelope yields canonical: there is no stored identity to contradict.
// The label is metadata only — not a dedup key, not a hash input, and no
// consumer reads it.
func QuarantineIdentityKind(envelope *event.Envelope, details EventQuarantineDetails) string {
	if envelope == nil {
		return "canonical"
	}
	if details.OriginalWireID == "" {
		if details.WireFingerprint == "" {
			return "canonical"
		}
		return "derived"
	}
	if idgen.CanonicalEqual(envelope.EventID, details.OriginalWireID) {
		return "canonical"
	}
	return "derived"
}

func quarantineContentHash(envelope *event.Envelope, details EventQuarantineDetails) (string, error) {
	contentHash, err := event.CanonicalContentHash(envelope)
	if err != nil && details.WireFingerprint != "" {
		// The fingerprint is over the complete authenticated wire event and is
		// the safe fallback when malformed payload data cannot be JSON encoded.
		contentHash = details.WireFingerprint
		err = nil
	}
	if err != nil || details.WireFingerprint == "" {
		return contentHash, err
	}
	// A valid event ID plus malformed optional IDs must not silently merge with
	// a later, differently malformed wire representation.
	material := contentHash + "|quarantine-wire-v1|" + details.WireFingerprint
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:]), nil
}

const maxWireEventID = 256

func boundedWireID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	prefix := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, value)
	if len(prefix) > maxWireEventID-17 {
		prefix = prefix[:maxWireEventID-17]
	}
	return prefix + "#" + hex.EncodeToString(sum[:])[:16]
}
