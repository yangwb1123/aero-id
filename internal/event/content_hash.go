package event

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// CanonicalContentHash returns the SHA-256 digest of the immutable canonical
// event content. The digest covers every Envelope field, including the
// unredacted payload; redaction is a storage policy and must not make a
// changed sensitive value look like the original event.
//
// JSON encoding supplies deterministic lexicographic ordering for string map
// keys. A decode/re-encode pass also removes representation-only whitespace
// (including nested json.RawMessage values) while preserving JSON numbers.
// UUIDs are already typed values and are rendered in canonical lowercase form.
// occurred_at is normalized to UTC and PostgreSQL's six-digit timestamp
// precision, matching the precision used by the event and audit persistence.
func CanonicalContentHash(envelope *Envelope) (string, error) {
	if envelope == nil {
		return "", errors.New("event is nil")
	}
	payload, err := canonicalPayloadJSON(envelope.Payload)
	if err != nil {
		return "", errors.New("event payload is not serializable")
	}
	content := canonicalEnvelopeContent{
		EventID:          envelope.EventID.String(),
		Source:           envelope.Source,
		SourceRegion:     envelope.SourceRegion,
		EventType:        envelope.EventType,
		SchemaVersion:    envelope.SchemaVersion,
		AggregateType:    envelope.AggregateType,
		AggregateID:      envelope.AggregateID,
		AggregateVersion: envelope.AggregateVersion,
		ActorUID:         envelope.ActorUID,
		TenantID:         envelope.TenantID,
		OperationID:      envelope.OperationID.String(),
		CorrelationID:    envelope.CorrelationID.String(),
		CausationID:      envelope.CausationID.String(),
		TraceID:          envelope.TraceID,
		OccurredAt:       canonicalEventTime(envelope.OccurredAt),
		Payload:          json.RawMessage(payload),
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", errors.New("event content is not serializable")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

type canonicalEnvelopeContent struct {
	EventID          string          `json:"event_id"`
	Source           string          `json:"source"`
	SourceRegion     string          `json:"source_region"`
	EventType        string          `json:"event_type"`
	SchemaVersion    int             `json:"schema_version"`
	AggregateType    string          `json:"aggregate_type"`
	AggregateID      string          `json:"aggregate_id"`
	AggregateVersion int64           `json:"aggregate_version"`
	ActorUID         string          `json:"actor_uid"`
	TenantID         string          `json:"tenant_id"`
	OperationID      string          `json:"operation_id"`
	CorrelationID    string          `json:"correlation_id"`
	CausationID      string          `json:"causation_id"`
	TraceID          string          `json:"trace_id"`
	OccurredAt       string          `json:"occurred_at"`
	Payload          json.RawMessage `json:"payload"`
}

func canonicalPayloadJSON(payload map[string]any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func canonicalEventTime(value time.Time) string {
	return value.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
}
