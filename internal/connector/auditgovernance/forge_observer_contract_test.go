package auditgovernance

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// forgeRunObservedFixture is an embedded mirror of Catalyst's canonical
// docs/contracts/fixtures/forge-run-observed-v1.json. Keeping the test input
// inside this repository makes the contract test runnable from a standalone
// Aero-ID checkout and avoids a workspace-specific absolute path.
//
//go:embed testdata/forge-run-observed-v1.json
var forgeRunObservedFixture []byte

type forgeRunObservedContract struct {
	APIVersion       string                    `json:"api_version"`
	OwnerRef         string                    `json:"owner_ref"`
	ConversationID   string                    `json:"conversation_id"`
	RunID            string                    `json:"run_id"`
	PromptID         string                    `json:"prompt_id"`
	CreatedAtMS      int64                     `json:"created_at_ms"`
	LatestSequence   int64                     `json:"latest_sequence"`
	Status           string                    `json:"status"`
	MetadataObserved bool                      `json:"metadata_observed"`
	ContentIncluded  bool                      `json:"content_included"`
	Authority        forgeRunObservedAuthority `json:"authority"`
}

type forgeRunObservedAuthority struct {
	IdentityVerified          bool `json:"identity_verified"`
	OwnerAuthorized           bool `json:"owner_authorized"`
	RunAuthoritative          bool `json:"run_authoritative"`
	PersistenceAttested       bool `json:"persistence_attested"`
	ContentProvenanceVerified bool `json:"content_provenance_verified"`
	ReservationCreated        bool `json:"reservation_created"`
	ExecutionAuthorized       bool `json:"execution_authorized"`
	DispatchPerformed         bool `json:"dispatch_performed"`
}

func decodeForgeRunObservedFixture(t *testing.T) forgeRunObservedContract {
	t.Helper()
	fixture, err := decodeForgeRunObserved(forgeRunObservedFixture)
	if err != nil {
		t.Fatalf("decode Forge Run observed fixture: %v", err)
	}
	return fixture
}

func decodeForgeRunObserved(raw []byte) (forgeRunObservedContract, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return forgeRunObservedContract{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var fixture forgeRunObservedContract
	if err := decoder.Decode(&fixture); err != nil {
		return forgeRunObservedContract{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return forgeRunObservedContract{}, io.ErrUnexpectedEOF
		}
		return forgeRunObservedContract{}, err
	}
	return fixture, nil
}

// rejectDuplicateJSONKeys closes the gap between encoding/json's strict
// unknown-field check and its map-like handling of repeated object names.
// Forge observer values are evidence-only, but accepting a repeated name
// would still make different consumers disagree about which value was seen.
func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			keyText, ok := key.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			if _, duplicate := seen[keyText]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", keyText)
			}
			seen[keyText] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("JSON object did not close")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("JSON array did not close")
		}
	case '}', ']':
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}

func TestForgeRunObservedRejectsUnknownFields(t *testing.T) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(forgeRunObservedFixture, &shape); err != nil {
		t.Fatal(err)
	}
	shape["prompt"] = json.RawMessage(`"raw prompt must not cross the observer boundary"`)
	mutated, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeForgeRunObserved(mutated); err == nil {
		t.Fatal("observer fixture decoder accepted an unknown content field")
	}
}

func TestForgeRunObservedRejectsDuplicateFieldsAtEveryObjectDepth(t *testing.T) {
	rootDuplicate := strings.TrimSpace(string(forgeRunObservedFixture))
	rootDuplicate = strings.TrimSuffix(rootDuplicate, "}") +
		`,"api_version":"forge.run.observed.v1"}`
	if _, err := decodeForgeRunObserved([]byte(rootDuplicate)); err == nil {
		t.Fatal("observer decoder accepted a duplicate root field")
	}

	nestedDuplicate := strings.Replace(
		string(forgeRunObservedFixture),
		"    \"dispatch_performed\": false\n  }",
		"    \"dispatch_performed\": false,\n    \"dispatch_performed\": false\n  }",
		1,
	)
	if nestedDuplicate == string(forgeRunObservedFixture) {
		t.Fatal("nested duplicate mutation did not match fixture")
	}
	if _, err := decodeForgeRunObserved([]byte(nestedDuplicate)); err == nil {
		t.Fatal("observer decoder accepted a duplicate nested field")
	}
}

func TestForgeRunObservedCanonicalFixture(t *testing.T) {
	fixture := decodeForgeRunObservedFixture(t)
	if fixture.APIVersion != "forge.run.observed.v1" {
		t.Fatalf("api_version = %q, want forge.run.observed.v1", fixture.APIVersion)
	}
	if fixture.OwnerRef == "" || fixture.ConversationID == "" || fixture.RunID == "" || fixture.PromptID == "" {
		t.Fatal("observer fixture must carry non-empty bounded identifiers")
	}
	if fixture.CreatedAtMS < 0 || fixture.LatestSequence <= 0 {
		t.Fatalf("observer fixture has unsafe time/sequence: created_at_ms=%d latest_sequence=%d", fixture.CreatedAtMS, fixture.LatestSequence)
	}
	if fixture.Status != "nonterminal" {
		t.Fatalf("status = %q, want nonterminal fixture case", fixture.Status)
	}
	if !fixture.MetadataObserved {
		t.Fatal("metadata_observed must be true")
	}
	if fixture.ContentIncluded {
		t.Fatal("content_included must remain false")
	}

	authority := fixture.Authority
	for name, value := range map[string]bool{
		"identity_verified":           authority.IdentityVerified,
		"owner_authorized":            authority.OwnerAuthorized,
		"run_authoritative":           authority.RunAuthoritative,
		"persistence_attested":        authority.PersistenceAttested,
		"content_provenance_verified": authority.ContentProvenanceVerified,
		"reservation_created":         authority.ReservationCreated,
		"execution_authorized":        authority.ExecutionAuthorized,
		"dispatch_performed":          authority.DispatchPerformed,
	} {
		if value {
			t.Fatalf("authority.%s = true; observer evidence must not grant authority", name)
		}
	}
}

// TestForgeObserverCorrelationContract pins the contract-only bridge from a
// Forge observer audit fact to Audit Governance's top-level execution fields.
// It carries correlation metadata and deliberately does not grant, reserve,
// dispatch, or execute work on a device.
func TestForgeObserverCorrelationContract(t *testing.T) {
	fact := &domain.AuditFact{
		ID: idgen.NewID(), TenantID: "tenant-a", ActorUID: "owner-digest",
		Action: "forge.run.observed", ResourceType: "forge_run", ResourceID: "run-001",
		OccurredAt: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		Payload: map[string]any{
			"conversation_id":      "conversation-001",
			"run_id":               "run-001",
			"sequence":             float64(5),
			"observer_event_type":  "run_finished",
			"execution_run_id":     "run-001",
			"workflow_instance_id": "workflow-001",
			// Observer evidence stays content-free even when the source fact
			// contains unrelated payload keys.
			"content_included": false,
		},
	}
	publisher := &Publisher{
		schemaID: "forge.run.observed.v1", schemaVersion: 1,
		actorID: "aero-id", dataClassification: "internal", retentionClass: "standard",
	}
	event := publisher.event(fact, "aero-id.tenant-a")
	if event.ExecutionRunID != "run-001" {
		t.Fatalf("execution_run_id = %q, want run-001", event.ExecutionRunID)
	}
	if event.WorkflowInstanceID != "workflow-001" {
		t.Fatalf("workflow_instance_id = %q, want workflow-001", event.WorkflowInstanceID)
	}
	if event.EventType != "forge.run.observed.v1" || event.SchemaID != event.EventType {
		t.Fatalf("schema/event = %q/%q, want forge.run.observed.v1", event.SchemaID, event.EventType)
	}
	if event.Payload["content"] != nil {
		t.Fatal("observer event must not introduce prompt or result content")
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"execution_run_id", "workflow_instance_id"} {
		if got, ok := wire[key].(string); !ok || got == "" {
			t.Fatalf("wire %s = %#v, want non-empty string", key, wire[key])
		}
	}
}

func TestPayloadStringOmitsUntrustedShapes(t *testing.T) {
	for name, payload := range map[string]map[string]any{
		"missing":    {},
		"blank":      {"execution_run_id": "  "},
		"wrong type": {"execution_run_id": 42},
		"control":    {"execution_run_id": "run-001\nforged"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := payloadString(payload, "execution_run_id"); got != "" {
				t.Fatalf("payloadString = %q, want empty", got)
			}
		})
	}
	if got := payloadString(map[string]any{"execution_run_id": "  run-001  "}, "execution_run_id"); got != "run-001" {
		t.Fatalf("payloadString trim = %q, want run-001", got)
	}
}
