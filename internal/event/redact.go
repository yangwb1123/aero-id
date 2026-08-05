package event

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// RedactPayload applies the canonical event boundary policy before a receipt or
// quarantine record is persisted. It is exported for repositories so malformed
// events cannot bypass the same source-data minimization rule.
func RedactPayload(value map[string]any) map[string]any {
	return boundedProjectionData(redactProjectionData(value, 0))
}

const maxProjectionDataBytes = 64 * 1024

func boundedProjectionData(value map[string]any) map[string]any {
	encoded, err := json.Marshal(value)
	if err == nil && len(encoded) > maxProjectionDataBytes {
		sum := sha256.Sum256(encoded)
		return map[string]any{"truncated": true, "sha256": hex.EncodeToString(sum[:])}
	}
	return value
}

var projectionSensitiveParts = []string{
	"password", "token", "secret", "cookie", "mfa", "recovery", "credential", "private_key",
	"access_key", "refresh", "authorization", "object_bytes", "blob", "message_body",
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
		switch nested := item.(type) {
		case map[string]any:
			result[key] = redactProjectionData(nested, depth+1)
		case []any:
			items := make([]any, 0, len(nested))
			for _, child := range nested {
				if childMap, ok := child.(map[string]any); ok {
					items = append(items, redactProjectionData(childMap, depth+1))
				} else {
					items = append(items, child)
				}
			}
			result[key] = items
		default:
			result[key] = item
		}
	}
	return result
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
