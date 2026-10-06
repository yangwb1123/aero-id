package connector

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

// correlationTestID is the same fixture idgen_test.go pins for the accepted
// wire grammar, so this file and the idgen contract stay aligned.
const (
	correlationTestID       = "0195b2c6-9f40-7c01-8000-000000000001"
	correlationTestAltID    = "0195b2c6-9f40-7c01-8000-000000000002"
	correlationTest32Hex    = "0195b2c69f407c018000000000000001"
	correlationTestSourceID = "source-op-1"
)

// TestSameCorrelationIDRenderingAndFallback pins the helper contract: every
// rendering idgen.Parse accepts is the same identity, nothing else is, and a
// requested value that is not a UUID falls back to exact string equality so
// legacy opaque correlation ids behave exactly as before.
func TestSameCorrelationIDRenderingAndFallback(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		echoed    string
		want      bool
	}{
		{name: "canonical", requested: correlationTestID, echoed: correlationTestID, want: true},
		{name: "upper-case hyphenated", requested: correlationTestID,
			echoed: "0195B2C6-9F40-7C01-8000-000000000001", want: true},
		{name: "mixed-case hyphenated", requested: correlationTestID,
			echoed: "0195b2C6-9F40-7c01-8000-000000000001", want: true},
		{name: "urn prefix lower-case", requested: correlationTestID,
			echoed: "urn:uuid:" + correlationTestID, want: true},
		{name: "urn prefix upper-case", requested: correlationTestID,
			echoed: "URN:UUID:" + strings.ToUpper(correlationTestID), want: true},
		{name: "32 hex lower-case", requested: correlationTestID, echoed: correlationTest32Hex, want: true},
		{name: "32 hex upper-case", requested: correlationTestID,
			echoed: strings.ToUpper(correlationTest32Hex), want: true},
		{name: "braced", requested: correlationTestID, echoed: "{" + correlationTestID + "}", want: true},
		{name: "surrounding whitespace", requested: correlationTestID,
			echoed: " " + correlationTestID + "\n", want: true},
		{name: "surrounding tabs", requested: correlationTestID, echoed: "\t" + correlationTestID + "\t", want: true},
		{name: "padded requested value", requested: "  " + correlationTestID + "  ", echoed: correlationTestID, want: true},

		{name: "different UUID", requested: correlationTestID, echoed: correlationTestAltID, want: false},
		{name: "different UUID in urn form", requested: correlationTestID,
			echoed: "urn:uuid:" + correlationTestAltID, want: false},
		{name: "different UUID mixed case", requested: correlationTestID,
			echoed: "0195b2C6-9F40-7c01-8000-00000000000a", want: false},
		{name: "truncated 29 hex form", requested: correlationTestID, echoed: "0195b2c69f407c018000000000001", want: false},
		{name: "first-8 truncation", requested: correlationTestID, echoed: correlationTestID[:8], want: false},
		{name: "unparseable echo", requested: correlationTestID, echoed: "not-a-uuid", want: false},
		{name: "whitespace only echo", requested: correlationTestID, echoed: "   \t\n", want: false},
		{name: "empty echo", requested: correlationTestID, echoed: "", want: false},
		{name: "zero UUID text echo", requested: correlationTestID,
			echoed: "00000000-0000-0000-0000-000000000000", want: false},
		{name: "control characters in echo", requested: correlationTestID,
			echoed: correlationTestID[:1] + "\x00\x1f" + correlationTestID[1:], want: false},
		{name: "512 byte non-uuid echo", requested: correlationTestID, echoed: strings.Repeat("a", 512), want: false},

		{name: "opaque fallback identical", requested: "logical-opaque-1", echoed: "logical-opaque-1", want: true},
		{name: "opaque fallback case difference", requested: "logical-opaque-1", echoed: "LOGICAL-OPAQUE-1", want: false},
		{name: "opaque fallback whitespace difference", requested: "logical-opaque-1", echoed: "logical-opaque-1 ", want: false},
		{name: "empty requested and echo", requested: "", echoed: "", want: true},
		{name: "empty requested with echo", requested: "", echoed: "x", want: false},
		{name: "zero UUID requested keeps exact compare", requested: "00000000-0000-0000-0000-000000000000",
			echoed: "00000000-0000-0000-0000-000000000000", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameCorrelationID(tc.requested, tc.echoed); got != tc.want {
				t.Fatalf("SameCorrelationID(%q, %q) = %v, want %v", tc.requested, tc.echoed, got, tc.want)
			}
		})
	}
}

// TestOperationStatusAeroIMAcceptsAlternateRenderingsOfCorrelationID is AC-1: a
// source that parses the UUID aero-id sent and re-renders it must not be
// reported as a correlation mismatch, and the source's raw text must reach
// CommandResult unnormalised.
func TestOperationStatusAeroIMAcceptsAlternateRenderingsOfCorrelationID(t *testing.T) {
	request := OperationStatusRequest{AccountID: domain.ID{1}, OperationID: correlationTestID,
		SourceOperationID: correlationTestSourceID}
	for _, tc := range []struct{ name, echo string }{
		{name: "canonical", echo: correlationTestID},
		{name: "upper-case", echo: "0195B2C6-9F40-7C01-8000-000000000001"},
		{name: "mixed-case", echo: "0195b2C6-9F40-7c01-8000-000000000001"},
		{name: "urn prefix", echo: "urn:uuid:" + correlationTestID},
		{name: "urn prefix upper-case", echo: "URN:UUID:" + strings.ToUpper(correlationTestID)},
		{name: "32 hex", echo: correlationTest32Hex},
		{name: "braced", echo: "{" + correlationTestID + "}"},
		{name: "whitespace wrapped", echo: " " + correlationTestID + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := operationStatusTestConnector(t, SourceAeroIM,
				correlationTestBody(t, tc.echo, correlationTestSourceID, "completed"), 0)
			result, err := client.OperationStatus(t.Context(), request)
			if err != nil {
				t.Fatalf("OperationStatus: %v", err)
			}
			if result.Status != "completed" || result.SourceOperationID != correlationTestSourceID {
				t.Fatalf("result = %+v, want completed/%s", result, correlationTestSourceID)
			}
			if result.OperationID != tc.echo {
				t.Fatalf("OperationID = %q, want the source's raw rendering %q", result.OperationID, tc.echo)
			}
		})
	}
}

// TestOperationStatusAeroIMRejectsNonIdentityCorrelationIDs is the AC-3
// negative set: only an echo that is not the same identity is a mismatch, and
// the error shape and redaction are unchanged.
func TestOperationStatusAeroIMRejectsNonIdentityCorrelationIDs(t *testing.T) {
	request := OperationStatusRequest{AccountID: domain.ID{1}, OperationID: correlationTestID,
		SourceOperationID: correlationTestSourceID}
	for _, tc := range []struct{ name, echo string }{
		{name: "different UUID", echo: correlationTestAltID},
		{name: "different UUID in urn form", echo: "urn:uuid:" + correlationTestAltID},
		{name: "truncated 29 hex form", echo: "0195b2c69f407c018000000000001"},
		{name: "first-8 truncation", echo: correlationTestID[:8]},
		{name: "last nibble flip", echo: correlationTestAltID},
		{name: "zero UUID text", echo: "00000000-0000-0000-0000-000000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := operationStatusTestConnector(t, SourceAeroIM,
				correlationTestBody(t, tc.echo, correlationTestSourceID, "completed"), 0)
			result, err := client.OperationStatus(t.Context(), request)
			assertOperationIDMismatch(t, result, err, correlationTestID, correlationTestSourceID)
		})
	}
}

// TestOperationStatusAeroIMKeepsSourceOperationIDExact is AC-4: the helper is
// scoped to the aero-id-owned correlation id, so the source-owned
// source_operation_id stays exact-string compared even when both values are
// UUID-shaped and denote the same identity.
func TestOperationStatusAeroIMKeepsSourceOperationIDExact(t *testing.T) {
	for _, tc := range []struct{ name, requested, echoed string }{
		{name: "non-UUID case difference", requested: "source-op-1", echoed: "Source-op-1"},
		{name: "non-UUID whitespace difference", requested: "source-op-1", echoed: "source-op-1 "},
		{name: "UUID-shaped case difference", requested: correlationTestID,
			echoed: "0195B2C6-9F40-7C01-8000-000000000001"},
		{name: "UUID-shaped alternate rendering of same identity", requested: correlationTestID, echoed: correlationTest32Hex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := operationStatusTestConnector(t, SourceAeroIM,
				correlationTestBody(t, correlationTestID, tc.echoed, "completed"), 0)
			result, err := client.OperationStatus(t.Context(), OperationStatusRequest{
				AccountID: domain.ID{1}, OperationID: correlationTestID, SourceOperationID: tc.requested,
			})
			assertOperationIDMismatch(t, result, err, correlationTestID, tc.requested)
		})
	}
}

// TestOperationStatusAeroIMToleratesOmittedCorrelationEcho pins the AC-3
// resolution: at the Aero IM decode an empty correlation echo means "omitted",
// not "contradicted", and the documented source_operation_id fallback still
// fills the source id from the echoed operation id.
func TestOperationStatusAeroIMToleratesOmittedCorrelationEcho(t *testing.T) {
	request := OperationStatusRequest{AccountID: domain.ID{1}, OperationID: correlationTestID,
		SourceOperationID: correlationTestSourceID}

	omittedCorrelation := operationStatusTestConnector(t, SourceAeroIM,
		correlationTestBody(t, "", correlationTestSourceID, "completed"), 0)
	result, err := omittedCorrelation.OperationStatus(t.Context(), request)
	if err != nil {
		t.Fatalf("omitted correlation echo: %v", err)
	}
	if result.SourceOperationID != correlationTestSourceID || result.OperationID != "" {
		t.Fatalf("result = %+v, want empty correlation echo preserved with source id %s", result, correlationTestSourceID)
	}

	omittedSource := operationStatusTestConnector(t, SourceAeroIM,
		correlationTestBody(t, correlationTestID, "", "completed"), 0)
	result, err = omittedSource.OperationStatus(t.Context(), request)
	if err != nil {
		t.Fatalf("omitted source echo: %v", err)
	}
	if result.SourceOperationID != correlationTestID {
		t.Fatalf("SourceOperationID = %q, want the documented fallback to the echoed operation id", result.SourceOperationID)
	}
}

func correlationTestBody(t *testing.T, operationID, sourceOperationID, status string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"operation_id": operationID, "source_operation_id": sourceOperationID, "status": status,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return string(body)
}
