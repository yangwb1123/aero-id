package event

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// RedactPayload applies the canonical event boundary policy before a receipt or
// quarantine record is persisted. It is exported for repositories so malformed
// events cannot bypass the same source-data minimization rule.
func RedactPayload(value map[string]any) map[string]any {
	return boundedProjectionData(redactProjectionData(value, 0))
}

func redactedEnvelope(envelope *Envelope) *Envelope {
	return BoundedEnvelope(envelope)
}

// BoundedEnvelope returns a persistence-safe copy. Oversized scalar fields are
// represented by a complete hash, never a truncated identity key. This is
// used for quarantine, where the original wire event may intentionally be
// malformed.
func BoundedEnvelope(envelope *Envelope) *Envelope {
	if envelope == nil {
		return nil
	}
	copy := *envelope
	copy.Source = boundedScalar(envelope.Source, MaxSourceBytes)
	copy.SourceRegion = boundedScalar(envelope.SourceRegion, MaxRegionBytes)
	copy.EventType = boundedScalar(envelope.EventType, MaxEventTypeBytes)
	copy.AggregateType = boundedScalar(envelope.AggregateType, MaxAggregateTypeBytes)
	copy.AggregateID = boundedScalar(envelope.AggregateID, MaxAggregateIDBytes)
	copy.ActorUID = boundedScalar(envelope.ActorUID, MaxActorUIDBytes)
	copy.TenantID = boundedScalar(envelope.TenantID, MaxTenantIDBytes)
	copy.TraceID = boundedScalar(envelope.TraceID, MaxTraceIDBytes)
	copy.Payload = RedactPayload(envelope.Payload)
	return &copy
}

func boundedScalar(value string, max int) string {
	if len(value) <= max && utf8.ValidString(value) && !containsControl(value) {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

const maxProjectionDataBytes = 64 * 1024

func boundedProjectionData(value map[string]any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		// Never return the original value after serialization failure: doing so
		// lets an unsupported value bypass the storage boundary.
		return map[string]any{"truncated": true, "reason": "unserializable"}
	}
	if len(encoded) > maxProjectionDataBytes {
		sum := sha256.Sum256(encoded)
		return map[string]any{"truncated": true, "sha256": hex.EncodeToString(sum[:])}
	}
	return value
}

var projectionSensitiveParts = []string{
	"password", "token", "secret", "cookie", "mfa", "recovery", "credential", "private_key",
	"privatekey", "access_key", "accesskey", "api_key", "apikey", "jwt", "refresh", "authorization",
	"object_bytes", "object_data", "blob", "message_body", "message_content",
}

func redactProjectionData(value map[string]any, depth int) map[string]any {
	if depth >= 6 {
		return map[string]any{"truncated": true}
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		if projectionSensitiveKey(key) {
			continue
		}
		result[key] = redactProjectionValue(item, depth+1)
	}
	return result
}

func redactProjectionValue(value any, depth int) any {
	if depth >= 6 {
		return map[string]any{"truncated": true}
	}
	switch nested := value.(type) {
	case map[string]any:
		return redactProjectionData(nested, depth)
	case []any:
		return redactProjectionArray(nested, depth)
	default:
		return redactProjectionScalar(nested)
	}
}

func redactProjectionArray(values []any, depth int) any {
	result := make([]any, 0, len(values))
	for _, child := range values {
		result = append(result, redactProjectionValue(child, depth+1))
	}
	return result
}

func redactProjectionScalar(value any) any {
	switch nested := value.(type) {
	case time.Time:
		return nested.UTC().Format(time.RFC3339Nano)
	case *time.Time:
		if nested == nil {
			return nil
		}
		return nested.UTC().Format(time.RFC3339Nano)
	case []string:
		return nested
	case nil, string, bool, float64, float32, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, json.Number:
		return value
	default:
		return map[string]any{"truncated": true, "reason": "unsupported"}
	}
}

func projectionSensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, part := range projectionSensitiveParts {
		if strings.Contains(key, part) {
			return true
		}
	}
	return key == "body" || key == "content" || key == "raw" || key == "messages" || key == "message"
}
