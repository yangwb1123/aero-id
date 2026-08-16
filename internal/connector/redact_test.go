package connector

import "testing"

// containsSensitiveSummaryKey walks a redacted map and reports whether any key
// still matches the sensitive-shape matcher. It reuses the production matcher,
// so a regression in canonicalization is caught at any nesting depth.
func containsSensitiveSummaryKey(value any) bool {
	switch nested := value.(type) {
	case map[string]any:
		for key, child := range nested {
			if sensitiveSummaryKey(key) || containsSensitiveSummaryKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range nested {
			if containsSensitiveSummaryKey(child) {
				return true
			}
		}
	}
	return false
}

func TestRedactSummaryDataDropsCamelCaseCredentialKeys(t *testing.T) {
	leakKeys := []string{"accessKey", "privateKey", "apiKey", "signingKey", "signing_key", "api_key"}
	legacyKeys := []string{"token", "body", "password", "client_secret", "secret", "secretAccessKey",
		"secret_access_key", "access_token", "authorization", "mfa", "recovery", "object_bytes",
		"blob", "message_body", "raw", "content", "messages", "message"}
	keys := append(append([]string(nil), leakKeys...), legacyKeys...)
	for _, key := range keys {
		t.Run("drops "+key, func(t *testing.T) {
			got := RedactSummaryData(map[string]any{key: "credential-value", "display_name": "Ada"})
			if got[key] != nil {
				t.Fatalf("credential key %q survived redaction: %#v", key, got)
			}
			if got["display_name"] != "Ada" {
				t.Fatalf("safe key display_name was dropped or rewritten: %#v", got)
			}
		})
	}
}

func TestRedactSummaryDataCanonicalizesSeparatorVariants(t *testing.T) {
	for _, key := range []string{"accessKey", "access_key", "access-key", "access.key", "access key"} {
		t.Run(key, func(t *testing.T) {
			if got := RedactSummaryData(map[string]any{key: "credential-value"}); len(got) != 0 {
				t.Fatalf("credential key %q survived redaction: %#v", key, got)
			}
		})
	}
}

func TestRedactSummaryDataNestedAndArrayCredentials(t *testing.T) {
	input := map[string]any{
		"display_name": "Ada",
		"tenant": map[string]any{
			"owner": map[string]any{"accessKey": "nested-access", "privateKey": "nested-private"},
		},
		"items": []any{
			map[string]any{"apiKey": "array-api", "display_name": "Grace"},
		},
	}
	got := RedactSummaryData(input)
	if containsSensitiveSummaryKey(got) {
		t.Fatalf("credential keys survived nested redaction: %#v", got)
	}
	if got["display_name"] != "Ada" {
		t.Fatalf("top-level display_name was dropped or rewritten: %#v", got)
	}
	tenant, ok := got["tenant"].(map[string]any)
	if !ok {
		t.Fatalf("tenant was dropped: %#v", got)
	}
	owner, ok := tenant["owner"].(map[string]any)
	if !ok {
		t.Fatalf("owner was dropped: %#v", tenant)
	}
	if len(owner) != 0 {
		t.Fatalf("depth-2 credential keys survived: %#v", owner)
	}
	items, ok := got["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items array was misredacted: %#v", got["items"])
	}
	first, ok := items[0].(map[string]any)
	if !ok || first["display_name"] != "Grace" || containsSensitiveSummaryKey(first) {
		t.Fatalf("array element misredacted: %#v", items[0])
	}
}

func TestRedactSummaryDataDoesNotBroadenGenericKey(t *testing.T) {
	input := map[string]any{"keyboard": "a", "monkey": "b", "keynote": "c", "display_name": "Ada"}
	got := RedactSummaryData(input)
	for _, key := range []string{"keyboard", "monkey", "keynote", "display_name"} {
		if got[key] == nil {
			t.Fatalf("safe key %q was dropped: %#v", key, got)
		}
	}
}
