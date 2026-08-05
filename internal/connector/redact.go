package connector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// RedactSummaryData applies the source boundary's allow-list policy before a
// source response becomes a durable projection or export input. Source APIs
// should already return summaries, but the boundary remains defensive.
func RedactSummaryData(value map[string]any) map[string]any {
	result := redactSummaryMap(value, 0)
	encoded, err := json.Marshal(result)
	if err == nil && len(encoded) > maxSummaryDataBytes {
		sum := sha256.Sum256(encoded)
		return map[string]any{"truncated": true, "sha256": hex.EncodeToString(sum[:])}
	}
	return result
}

const maxSummaryDataBytes = 64 * 1024

var sensitiveSummaryParts = []string{
	"password", "token", "secret", "cookie", "mfa", "recovery", "credential", "private_key",
	"access_key", "refresh", "authorization", "object_bytes", "blob", "message_body",
}

func redactSummaryMap(value map[string]any, depth int) map[string]any {
	if depth >= 6 {
		return map[string]any{"truncated": true}
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		if sensitiveSummaryKey(key) {
			continue
		}
		switch nested := item.(type) {
		case map[string]any:
			result[key] = redactSummaryMap(nested, depth+1)
		case []any:
			values := make([]any, 0, len(nested))
			for _, child := range nested {
				if childMap, ok := child.(map[string]any); ok {
					values = append(values, redactSummaryMap(childMap, depth+1))
				} else {
					values = append(values, child)
				}
			}
			result[key] = values
		default:
			result[key] = item
		}
	}
	return result
}

func sensitiveSummaryKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, part := range sensitiveSummaryParts {
		if strings.Contains(key, part) {
			return true
		}
	}
	return key == "body" || key == "content" || key == "raw" || key == "messages" || key == "message"
}
