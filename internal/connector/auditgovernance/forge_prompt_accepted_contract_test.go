package auditgovernance

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// forgePromptAcceptedAuditFixture is the receiver-side mirror of Catalyst's
// canonical forge.prompt.accepted.v1 envelope. It deliberately stays a
// contract test: Aero-ID does not publish, enqueue, or authorize Forge work
// from this value.
//
//go:embed testdata/forge-prompt-accepted-audit-v1.json
var forgePromptAcceptedAuditFixture []byte

type forgePromptAcceptedAuditContract struct {
	EventID            string                          `json:"event_id"`
	TenantID           string                          `json:"tenant_id"`
	SourceSystem       string                          `json:"source_system"`
	EventType          string                          `json:"event_type"`
	SchemaID           string                          `json:"schema_id"`
	SchemaVersion      int                             `json:"schema_version"`
	OccurredAt         string                          `json:"occurred_at"`
	OperationID        string                          `json:"operation_id"`
	Actor              forgePromptAcceptedAuditActor   `json:"actor"`
	AggregateType      string                          `json:"aggregate_type"`
	AggregateID        string                          `json:"aggregate_id"`
	AggregateVersion   int64                           `json:"aggregate_version"`
	Action             string                          `json:"action"`
	Outcome            string                          `json:"outcome"`
	Payload            forgePromptAcceptedAuditPayload `json:"payload"`
	DataClassification string                          `json:"data_classification"`
	RetentionClass     string                          `json:"retention_class"`
	IdempotencyKey     string                          `json:"idempotency_key"`
}

type forgePromptAcceptedAuditActor struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type forgePromptAcceptedAuditPayload struct {
	ContentIncluded bool `json:"content_included"`
}

func decodeForgePromptAcceptedAudit(raw []byte) (forgePromptAcceptedAuditContract, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return forgePromptAcceptedAuditContract{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var event forgePromptAcceptedAuditContract
	if err := decoder.Decode(&event); err != nil {
		return forgePromptAcceptedAuditContract{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return forgePromptAcceptedAuditContract{}, io.ErrUnexpectedEOF
		}
		return forgePromptAcceptedAuditContract{}, err
	}
	return event, nil
}

func forgePromptAcceptedAuditLowerSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

func TestForgePromptAcceptedAuditCanonicalFixture(t *testing.T) {
	event, err := decodeForgePromptAcceptedAudit(forgePromptAcceptedAuditFixture)
	if err != nil {
		t.Fatalf("decode Forge accepted-Prompt audit fixture: %v", err)
	}
	if event.SourceSystem != "forge-runtime" ||
		event.EventType != "forge.prompt.accepted.v1" ||
		event.SchemaID != event.EventType || event.SchemaVersion != 1 ||
		event.AggregateType != "forge_conversation" ||
		event.Action != "append_prompt" || event.Outcome != "accepted" ||
		event.Actor.Type != "user" || event.Payload.ContentIncluded {
		t.Fatalf("unexpected Forge accepted-Prompt audit contract: %#v", event)
	}
	if !forgePromptAcceptedAuditLowerSHA256(event.EventID) ||
		!forgePromptAcceptedAuditLowerSHA256(event.Actor.ID) ||
		event.IdempotencyKey != event.EventID {
		t.Fatalf("invalid Forge accepted-Prompt identity binding: %#v", event)
	}
	if _, err := time.Parse(time.RFC3339, event.OccurredAt); err != nil {
		t.Fatalf("occurred_at is not RFC3339: %v", err)
	}
	for name, value := range map[string]string{
		"tenant_id": event.TenantID, "operation_id": event.OperationID,
		"aggregate_id": event.AggregateID,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, " :/\\") {
			t.Fatalf("unsafe %s=%q", name, value)
		}
	}
	if event.AggregateVersion <= 0 || event.DataClassification != "internal" ||
		event.RetentionClass != "standard" {
		t.Fatalf("invalid Forge accepted-Prompt bounds: %#v", event)
	}
}

func TestForgePromptAcceptedAuditRejectsContentAndDuplicateFields(t *testing.T) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(forgePromptAcceptedAuditFixture, &shape); err != nil {
		t.Fatal(err)
	}
	shape["prompt"] = json.RawMessage(`"raw prompt must not cross the audit boundary"`)
	unknown, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeForgePromptAcceptedAudit(unknown); err == nil {
		t.Fatal("accepted-Prompt audit decoder accepted raw prompt content")
	}

	rootDuplicate := strings.TrimSpace(string(forgePromptAcceptedAuditFixture))
	rootDuplicate = strings.TrimSuffix(rootDuplicate, "}") + `,"event_id":"different"}`
	if _, err := decodeForgePromptAcceptedAudit([]byte(rootDuplicate)); err == nil {
		t.Fatal("accepted-Prompt audit decoder accepted a duplicate root field")
	}
	nestedDuplicate := strings.Replace(
		string(forgePromptAcceptedAuditFixture),
		"    \"content_included\": false\n  }",
		"    \"content_included\": false,\n    \"content_included\": false\n  }",
		1,
	)
	if nestedDuplicate == string(forgePromptAcceptedAuditFixture) {
		t.Fatal("nested duplicate mutation did not match Forge accepted-Prompt fixture")
	}
	if _, err := decodeForgePromptAcceptedAudit([]byte(nestedDuplicate)); err == nil {
		t.Fatal("accepted-Prompt audit decoder accepted a duplicate nested field")
	}
}
