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

// sensitiveSummaryParts lists credential shapes in human-readable snake_case.
// Every part is canonicalized before matching, so camelCase and separator-free
// spellings (accessKey, api_key, signing-key) resolve to the same shape.
var sensitiveSummaryParts = []string{
	"password", "token", "secret", "cookie", "mfa", "recovery", "credential", "private_key",
	"access_key", "refresh", "authorization", "object_bytes", "blob", "message_body",
	"api_key", "signing_key",
}

// sensitiveSummaryCanonicalParts is sensitiveSummaryParts canonicalized once so
// per-key matching never re-canonicalizes the constant set.
var sensitiveSummaryCanonicalParts = canonicalSummaryParts()

func canonicalSummaryParts() []string {
	parts := make([]string, len(sensitiveSummaryParts))
	for i, part := range sensitiveSummaryParts {
		parts[i] = canonicalSummaryKey(part)
	}
	return parts
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

// canonicalSummaryKey lowercases a key and strips every separator so that
// camelCase, snake_case, kebab-case, dotted, and space-separated spellings of
// the same credential shape collapse to one canonical form (accessKey,
// access_key, access-key, access.key, and "access key" all -> "accesskey").
func canonicalSummaryKey(key string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(key))
}

func sensitiveSummaryKey(key string) bool {
	canonical := canonicalSummaryKey(key)
	for _, part := range sensitiveSummaryCanonicalParts {
		if strings.Contains(canonical, part) {
			return true
		}
	}
	return canonical == "body" || canonical == "content" || canonical == "raw" || canonical == "messages" || canonical == "message"
}
