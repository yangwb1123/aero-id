package repository

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// canonicalAuditPayload re-serializes stored payload bytes (jsonb text) into
// the canonical Go form that Append hashed: json.Marshal of the original
// map[string]any. jsonb canonicalizes object keys (length-first, unlike Go's
// bytewise order) and number literals (1e21 -> 1000000000000000000000,
// 1e-7 -> 0.0000001), so re-marshaling a float64 decode of the stored bytes is
// not byte-faithful. Rewriting every literal back to the Go numeric type that
// produced it reproduces the append-side bytes regardless of jsonb's form.
func canonicalAuditPayload(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(normalizeAuditNumbers(value))
}

// normalizeAuditNumbers rewrites json.Number literals to the Go type whose
// encoding/json output matches the append-side serialization: int64 for exact
// integer literals, uint64 for larger exact integers, and float64 otherwise.
// jsonb expands float64 exponents into plain digits (1e19 ->
// 10000000000000000000) and encoding/json also writes plain digits below 1e21,
// so integer paths reproduce those digits exactly; the float64 path emits Go's
// shortest round-trip form (1e+21, 1e-7) for everything else.
func normalizeAuditNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if integer, err := strconv.ParseUint(string(typed), 10, 64); err == nil {
			return integer
		}
		number, err := typed.Float64()
		if err != nil {
			return value
		}
		return number
	case map[string]any:
		for key, item := range typed {
			typed[key] = normalizeAuditNumbers(item)
		}
		return typed
	case []any:
		for i, item := range typed {
			typed[i] = normalizeAuditNumbers(item)
		}
		return typed
	default:
		return value
	}
}
