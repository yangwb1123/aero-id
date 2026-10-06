package service

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// provenanceWireID is the identity used across the provenance fixtures. Its
// canonical rendering, its urn:uuid:/upper-case renderings and its 32-hex form
// all denote the same 128-bit value, so all of them are canonical provenance.
const provenanceWireID = "0195b2c6-9f40-7c01-8000-000000000001"

func provenanceEnvelope(t *testing.T) *event.Envelope {
	t.Helper()
	id, err := idgen.Parse(provenanceWireID)
	if err != nil {
		t.Fatal(err)
	}
	return &event.Envelope{EventID: id, Source: "aero-im", SourceRegion: "us-west", EventType: "account.updated",
		SchemaVersion: 1, AggregateType: "account", AggregateID: "src-1", TenantID: "tenant-a", Payload: map[string]any{}}
}

func anchoredProvenanceEnvelope(fingerprint string) *event.Envelope {
	return &event.Envelope{EventID: idgen.Anchor("event.quarantine.v1", fingerprint), Source: "aero-im",
		SourceRegion: "us-west", EventType: "account.updated", SchemaVersion: 1, AggregateType: "account",
		AggregateID: "src-1", TenantID: "tenant-a", Payload: map[string]any{}}
}

func quarantineBlock(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	raw, ok := payload["_quarantine"]
	if !ok {
		t.Fatalf("payload has no _quarantine block: %v", payload)
	}
	block, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("_quarantine = %T, want map[string]any", raw)
	}
	return block
}

// TestAddQuarantineAuditMetadataIdentityKind pins the audit-fact side of the
// shared provenance oracle: identity provenance is decided on the parsed
// identity, never on the rendered text the source happened to send.
func TestAddQuarantineAuditMetadataIdentityKind(t *testing.T) {
	const fingerprint = "b1946ac92492d2347c6235b4d2611184"
	parsed := provenanceEnvelope(t)

	cases := []struct {
		name     string
		envelope *event.Envelope
		details  repository.EventQuarantineDetails
		stage    string
		want     string
	}{
		{"canonical rendering", parsed,
			repository.EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: provenanceWireID, WireFingerprint: fingerprint},
			"canonicalization", "canonical"},
		{"upper-case rendering", parsed,
			repository.EventQuarantineDetails{Stage: "canonicalization",
				OriginalWireID: "0195B2C6-9F40-7C01-8000-000000000001", WireFingerprint: fingerprint}, "canonicalization", "canonical"},
		{"urn rendering", parsed,
			repository.EventQuarantineDetails{Stage: "canonicalization",
				OriginalWireID: "urn:uuid:0195B2C6-9F40-7C01-8000-000000000001", WireFingerprint: fingerprint}, "canonicalization", "canonical"},
		{"32 hex rendering", parsed,
			repository.EventQuarantineDetails{Stage: "canonicalization",
				OriginalWireID: "0195b2c69f407c018000000000000001", WireFingerprint: fingerprint}, "canonicalization", "canonical"},
		{"braced rendering", parsed,
			repository.EventQuarantineDetails{Stage: "canonicalization",
				OriginalWireID: "{0195b2c6-9f40-7c01-8000-000000000001}", WireFingerprint: fingerprint}, "canonicalization", "canonical"},

		{"different valid UUID", parsed,
			repository.EventQuarantineDetails{Stage: "canonicalization",
				OriginalWireID: "0195b2c6-9f40-7c01-8000-000000000002", WireFingerprint: fingerprint}, "canonicalization", "derived"},
		{"anchored surrogate with no wire id", anchoredProvenanceEnvelope(fingerprint),
			repository.EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "", WireFingerprint: fingerprint},
			"canonicalization", "derived"},
		{"anchored surrogate with unparseable wire id", anchoredProvenanceEnvelope(fingerprint),
			repository.EventQuarantineDetails{Stage: "canonicalization", OriginalWireID: "not-a-uuid", WireFingerprint: fingerprint},
			"canonicalization", "derived"},
		{"anchored surrogate with bounded wire id", anchoredProvenanceEnvelope(fingerprint),
			repository.EventQuarantineDetails{Stage: "canonicalization",
				OriginalWireID: boundedWireID("urn:uuid:" + provenanceWireID), WireFingerprint: fingerprint}, "canonicalization", "derived"},

		{"no wire details (service API quarantine)", parsed,
			repository.EventQuarantineDetails{Stage: "residency"}, "residency", "canonical"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{}
			addQuarantineAuditMetadata(payload, tc.envelope, tc.details, tc.stage)
			block := quarantineBlock(t, payload)
			if got := block["identity_kind"]; got != tc.want {
				t.Fatalf("identity_kind = %v, want %q (payload %v)", got, tc.want, block)
			}
			if got := block["stage"]; got != tc.stage {
				t.Fatalf("stage = %v, want %q", got, tc.stage)
			}
			if tc.details.OriginalWireID != "" {
				if got := block["wire_event_id"]; got != tc.details.OriginalWireID {
					t.Fatalf("wire_event_id = %v, want the verbatim wire text %q", got, tc.details.OriginalWireID)
				}
			} else if _, present := block["wire_event_id"]; present {
				t.Fatalf("wire_event_id must be absent when no wire id was supplied: %v", block)
			}
			if tc.details.WireFingerprint != "" && block["wire_fingerprint"] != tc.details.WireFingerprint {
				t.Fatalf("wire_fingerprint = %v, want %q", block["wire_fingerprint"], tc.details.WireFingerprint)
			}
		})
	}
}

// TestAddQuarantineAuditMetadataKeepsVerbatimURNWireText pins the evidence the
// audit fact exists to keep: the recorded wire_event_id is the text the source
// sent, not a normalized rendering of it.
func TestAddQuarantineAuditMetadataKeepsVerbatimURNWireText(t *testing.T) {
	const urnText = "urn:uuid:0195B2C6-9F40-7C01-8000-000000000001"
	payload := map[string]any{}
	addQuarantineAuditMetadata(payload, provenanceEnvelope(t), repository.EventQuarantineDetails{
		Stage: "canonicalization", OriginalWireID: urnText, WireFingerprint: "b1946ac92492d2347c6235b4d2611184",
	}, "canonicalization")

	block := quarantineBlock(t, payload)
	if got := block["wire_event_id"]; got != urnText {
		t.Fatalf("wire_event_id = %v, want verbatim %q", got, urnText)
	}
	if got := block["identity_kind"]; got != "canonical" {
		t.Fatalf("identity_kind = %v, want canonical", got)
	}
}

// TestAddQuarantineAuditMetadataEmptyDetailsWritesNoBlock keeps the documented
// no-details early return: an envelope-only audit input must not grow a
// quarantine block.
func TestAddQuarantineAuditMetadataEmptyDetailsWritesNoBlock(t *testing.T) {
	payload := map[string]any{}
	addQuarantineAuditMetadata(payload, provenanceEnvelope(t), repository.EventQuarantineDetails{}, "canonicalization")
	if _, present := payload["_quarantine"]; present {
		t.Fatalf("empty details must write no _quarantine block: %v", payload)
	}
}

// TestIngestItem_ProvenanceLabels drives the real gRPC-shaped ingest path and
// asserts the persisted audit fact payload: a source that supplied a legal
// alternative rendering of the parsed id is canonical, and a locally anchored
// surrogate (blank wire event_id) is derived.
func TestIngestItem_ProvenanceLabels(t *testing.T) {
	const fingerprint = "b1946ac92492d2347c6235b4d2611184"
	const urnText = "urn:uuid:0195B2C6-9F40-7C01-8000-000000000001"

	cases := []struct {
		name        string
		eventID     domain.ID
		wireText    string
		wantKind    string
		wantWireID  string
		wantEventID domain.ID
	}{
		{"urn rendering of the parsed id", provenanceEnvelope(t).EventID, urnText, "canonical", urnText,
			provenanceEnvelope(t).EventID},
		{"upper-case rendering of the parsed id", provenanceEnvelope(t).EventID,
			"0195B2C6-9F40-7C01-8000-000000000001", "canonical", "0195B2C6-9F40-7C01-8000-000000000001",
			provenanceEnvelope(t).EventID},
		{"blank wire id anchors a local surrogate", domain.ID{}, "", "derived", "",
			idgen.Anchor("event.quarantine.v1", fingerprint)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			receipts := &fakeReceiptRepo{}
			ledger := &quarantineAuditAppendSpy{seen: map[string]bool{}}
			itemSvc, ok := NewEventIngestService(receipts, WithEventIngestAudit(NewAuditLedgerService(ledger))).(EventItemIngestService)
			if !ok {
				t.Fatal("event ingest service must implement the item-oriented ingest contract")
			}

			envelope := &event.Envelope{EventID: tc.eventID, Source: "aero-im", SourceRegion: "us-west",
				EventType: "account.updated", SchemaVersion: 1, AggregateType: "account", AggregateID: "src-1",
				TenantID: "tenant-a", OccurredAt: testEnvelope("us-west").OccurredAt, Payload: map[string]any{}}
			result := itemSvc.IngestItem(context.Background(), EventIngestItem{
				Envelope: envelope, CanonicalizationCode: "event.invalid_id", CanonicalizationStage: "canonicalization",
				OriginalWireEventID: tc.wireText, WireFingerprint: fingerprint,
			})
			if result.Status != string(repository.EventQuarantined) || result.ErrorCode != "event.invalid_id" {
				t.Fatalf("result = %+v, want a quarantined event.invalid_id result", result)
			}
			if envelope.EventID != tc.wantEventID {
				t.Fatalf("stored event id = %s, want %s", envelope.EventID, tc.wantEventID)
			}
			if len(ledger.facts) != 1 {
				t.Fatalf("ledger facts = %d, want exactly 1", len(ledger.facts))
			}
			block := quarantineBlock(t, ledger.facts[0].Payload)
			if got := block["identity_kind"]; got != tc.wantKind {
				t.Fatalf("persisted identity_kind = %v, want %q (payload %v)", got, tc.wantKind, block)
			}
			if got, present := block["wire_event_id"]; tc.wantWireID == "" {
				if present {
					t.Fatalf("wire_event_id = %v, want absent", got)
				}
			} else if got != tc.wantWireID {
				t.Fatalf("persisted wire_event_id = %v, want verbatim %q", got, tc.wantWireID)
			}
		})
	}
}

// TestEventIngest_ServiceAPIQuarantineKeepsCanonicalIdentity is the regression
// pin for the no-wire-details path (HTTP ingest and the NATS adapter): there the
// identity comes from the submitted envelope and nothing is derived, so the
// label must stay canonical and must carry no wire metadata.
func TestEventIngest_ServiceAPIQuarantineKeepsCanonicalIdentity(t *testing.T) {
	receipts := &fakeReceiptRepo{}
	ledger := &quarantineAuditAppendSpy{seen: map[string]bool{}}
	svc := NewEventIngestService(receipts,
		WithEventAllowedRegions([]string{"us-west"}),
		WithEventIngestAudit(NewAuditLedgerService(ledger)))

	if _, err := svc.Ingest(context.Background(), testEnvelope("cn-north")); err == nil {
		t.Fatal("residency quarantine must return the deterministic error")
	}
	if len(ledger.facts) != 1 {
		t.Fatalf("ledger facts = %d, want exactly 1", len(ledger.facts))
	}
	block := quarantineBlock(t, ledger.facts[0].Payload)
	if got := block["identity_kind"]; got != "canonical" {
		t.Fatalf("identity_kind = %v, want canonical for a service-API quarantine", got)
	}
	if got := block["stage"]; got != "residency" {
		t.Fatalf("stage = %v, want residency", got)
	}
	if _, present := block["wire_event_id"]; present {
		t.Fatalf("wire_event_id must be absent without wire provenance: %v", block)
	}
}
