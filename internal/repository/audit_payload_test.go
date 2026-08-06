package repository

import (
	"strings"
	"testing"
)

// TestCanonicalAuditPayloadMatchesAppendSideBytes feeds jsonb-canonical text
// exactly as PostgreSQL stores it (length-first key order, expanded number
// literals) and asserts the canonical bytes equal what Append hashed via
// json.Marshal of the original map[string]any.
func TestCanonicalAuditPayloadMatchesAppendSideBytes(t *testing.T) {
	tests := []struct {
		name   string
		stored string
		want   string
	}{
		{"big int above 2^53", `{"n":9007199254740993}`, `{"n":9007199254740993}`},
		{"negative big int", `{"n":-9007199254740993}`, `{"n":-9007199254740993}`},
		{"float 1e21 expanded by jsonb", `{"n":1000000000000000000000}`, `{"n":1e+21}`},
		{"float 1e20 expanded by jsonb", `{"n":100000000000000000000}`, `{"n":100000000000000000000}`},
		{"float 1e19 expanded by jsonb", `{"n":10000000000000000000}`, `{"n":10000000000000000000}`},
		{"float 1e-7 expanded by jsonb", `{"n":0.0000001}`, `{"n":1e-7}`},
		{"integral float plain digits", `{"n":1234567}`, `{"n":1234567}`},
		{"jsonb length-first key order", `{"b": 1, "aa": 2}`, `{"aa":2,"b":1}`},
		{"integral float stored as 1.0", `{"n":1.0}`, `{"n":1}`},
		{"fractional float", `{"n":1.5}`, `{"n":1.5}`},
		{"uint64 max", `{"n":18446744073709551615}`, `{"n":18446744073709551615}`},
		{"huge exponent round-trips via float64", `{"n":1` + strings.Repeat("0", 100) + `}`, `{"n":1e+100}`},
		{"nested objects arrays and scalars", `{"o":{"b":1,"aa":2},"a":[0.0000001,9007199254740993,null,true,"x"]}`,
			`{"a":[1e-7,9007199254740993,null,true,"x"],"o":{"aa":2,"b":1}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalAuditPayload([]byte(tc.stored))
			if err != nil {
				t.Fatalf("canonicalAuditPayload(%s): %v", tc.stored, err)
			}
			if string(got) != tc.want {
				t.Fatalf("canonicalAuditPayload(%s) = %s, want %s", tc.stored, got, tc.want)
			}
		})
	}
}

func TestCanonicalAuditPayloadRejectsMalformedJSON(t *testing.T) {
	if _, err := canonicalAuditPayload([]byte(`{"n":`)); err == nil {
		t.Fatal("malformed payload must return an error")
	}
}
