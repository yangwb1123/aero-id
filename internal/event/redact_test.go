package event

import "testing"

func TestRedactPayloadNestedArrays(t *testing.T) {
	got := RedactPayload(map[string]any{"items": []any{[]any{map[string]any{
		"authorization": "Bearer secret", "display_name": "Ada",
	}}}})
	items, ok := got["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v, want one nested array", got["items"])
	}
	inner, ok := items[0].([]any)
	if !ok || len(inner) != 1 {
		t.Fatalf("inner = %#v, want one map", items[0])
	}
	value, ok := inner[0].(map[string]any)
	if !ok || value["authorization"] != nil || value["display_name"] != "Ada" {
		t.Fatalf("nested payload was not redacted: %#v", got)
	}
}
