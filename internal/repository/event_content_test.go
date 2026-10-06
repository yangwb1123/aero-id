package repository

import (
	"encoding/json"
	"testing"

	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// mustParseEventID parses a fixture wire id with the same grammar the ingest
// path uses, so a fixture can never silently stop being a legal wire form.
func mustParseEventID(t *testing.T, text string) idgen.ID {
	t.Helper()
	id, err := idgen.Parse(text)
	if err != nil {
		t.Fatalf("fixture %q does not parse: %v", text, err)
	}
	return id
}

// quarantineEnvelope builds a minimal envelope whose EventID is the given id,
// matching what the ingest path hands to the classifier.
func quarantineEnvelope(id idgen.ID) *event.Envelope {
	return &event.Envelope{EventID: id, Source: "aero-vault", SourceRegion: "us-west", TenantID: "tenant-a"}
}

func anchoredQuarantineEnvelope(fingerprint string) *event.Envelope {
	return quarantineEnvelope(idgen.Anchor("event.quarantine.v1", fingerprint))
}

// TestQuarantineIdentityKind pins the shared provenance oracle. Every text form
// idgen.Parse accepts denotes the same 128-bit identity, so an alternative
// rendering of a source-supplied id is canonical, not derived; an Anchor-derived
// surrogate (empty wire text plus a fingerprint) is derived; and the HTTP/NATS
// no-details quarantine keeps canonical because nothing was derived there.
func TestQuarantineIdentityKind(t *testing.T) {
	const canonicalText = "0195b2c6-9f40-7c01-8000-000000000001"
	parsed := mustParseEventID(t, canonicalText)
	const fingerprint = "b1946ac92492d2347c6235b4d2611184"

	cases := []struct {
		name     string
		envelope *event.Envelope
		details  EventQuarantineDetails
		want     string
	}{
		{"canonical rendering", quarantineEnvelope(parsed),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: canonicalText}, "canonical"},
		{"upper-case hyphenated", quarantineEnvelope(parsed),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "0195B2C6-9F40-7C01-8000-000000000001"}, "canonical"},
		{"urn prefix", quarantineEnvelope(parsed),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "urn:uuid:0195B2C6-9F40-7C01-8000-000000000001"}, "canonical"},
		{"32 hex", quarantineEnvelope(parsed),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "0195b2c69f407c018000000000000001"}, "canonical"},
		{"braced", quarantineEnvelope(parsed),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "{0195b2c6-9f40-7c01-8000-000000000001}"}, "canonical"},
		{"alternative rendering with fingerprint", quarantineEnvelope(parsed),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "urn:uuid:0195B2C6-9F40-7C01-8000-000000000001",
				WireFingerprint: fingerprint}, "canonical"},

		{"different valid UUID", quarantineEnvelope(parsed),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "0195b2c6-9f40-7c01-8000-000000000002"}, "derived"},
		{"unparseable wire text against anchored surrogate", anchoredQuarantineEnvelope(fingerprint),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "not-a-uuid", WireFingerprint: fingerprint}, "derived"},
		{"empty wire text with fingerprint (anchor condition)", anchoredQuarantineEnvelope(fingerprint),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "", WireFingerprint: fingerprint}, "derived"},
		{"blank wire text with fingerprint", anchoredQuarantineEnvelope(fingerprint),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "   ", WireFingerprint: fingerprint}, "derived"},
		{"bounded wire text with fingerprint", anchoredQuarantineEnvelope(fingerprint),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: boundedWireID("urn:uuid:" + canonicalText),
				WireFingerprint: fingerprint}, "derived"},

		{"empty wire text without fingerprint (HTTP/NATS path)", quarantineEnvelope(parsed),
			EventQuarantineDetails{Stage: "residency"}, "canonical"},
		{"fully empty details", quarantineEnvelope(parsed), EventQuarantineDetails{}, "canonical"},
		{"nil envelope", nil, EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: canonicalText}, "canonical"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := QuarantineIdentityKind(tc.envelope, tc.details); got != tc.want {
				t.Fatalf("QuarantineIdentityKind() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMarshalQuarantinedEventPayloadIdentityKind pins the persisted receipt
// metadata: the persisted identity_kind must agree with the audit fact label for
// the same wire texts, while stage, wire_event_id and wire_fingerprint stay
// exactly as the existing metadata builder produced them.
func TestMarshalQuarantinedEventPayloadIdentityKind(t *testing.T) {
	const canonicalText = "0195b2c6-9f40-7c01-8000-000000000001"
	const urnText = "urn:uuid:0195B2C6-9F40-7C01-8000-000000000001"
	const fingerprint = "b1946ac92492d2347c6235b4d2611184"
	parsed := mustParseEventID(t, canonicalText)

	cases := []struct {
		name     string
		envelope *event.Envelope
		details  EventQuarantineDetails
		wantKind string
	}{
		{"urn rendering of the parsed id", quarantineEnvelope(parsed),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: urnText, WireFingerprint: fingerprint}, "canonical"},
		{"anchored surrogate with no wire id", anchoredQuarantineEnvelope(fingerprint),
			EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "", WireFingerprint: fingerprint}, "derived"},
		{"service API quarantine with no wire details", quarantineEnvelope(parsed),
			EventQuarantineDetails{Stage: "residency"}, "canonical"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := marshalQuarantinedEventPayload(tc.envelope, tc.details)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Quarantine map[string]any `json:"_quarantine"`
			}
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("unmarshal %s: %v", encoded, err)
			}
			if decoded.Quarantine == nil {
				t.Fatalf("payload %s has no _quarantine block", encoded)
			}
			if got := decoded.Quarantine["identity_kind"]; got != tc.wantKind {
				t.Fatalf("identity_kind = %v, want %q (payload %s)", got, tc.wantKind, encoded)
			}
			if got := decoded.Quarantine["stage"]; got != tc.details.Stage {
				t.Fatalf("stage = %v, want %q", got, tc.details.Stage)
			}
			if got := decoded.Quarantine["wire_fingerprint"]; got != wireFingerprintValue(tc.details) {
				t.Fatalf("wire_fingerprint = %v, want %q", got, wireFingerprintValue(tc.details))
			}
			if got, want := decoded.Quarantine["wire_event_id"], optionalWireIDValue(tc.details.OriginalWireID); got != want {
				t.Fatalf("wire_event_id = %v, want %v (bounded wire id must be unchanged)", got, want)
			}
		})
	}
}

// optionalWireIDValue mirrors the metadata builder: wire_event_id is emitted
// only when a wire id is present, and is always the bounded rendering.
func optionalWireIDValue(originalWireID string) any {
	if originalWireID == "" {
		return nil
	}
	return boundedWireID(originalWireID)
}

func wireFingerprintValue(details EventQuarantineDetails) any {
	if details.WireFingerprint == "" {
		return nil
	}
	return details.WireFingerprint
}

// TestMarshalQuarantinedEventPayloadNilEnvelope keeps the metadata-only
// contract: a nil envelope is still refused, never silently labelled.
func TestMarshalQuarantinedEventPayloadNilEnvelope(t *testing.T) {
	if _, err := marshalQuarantinedEventPayload(nil, EventQuarantineDetails{Stage: "residency"}); err == nil {
		t.Fatal("nil envelope must not serialize quarantine metadata")
	}
}
