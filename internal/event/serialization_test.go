package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

func TestMarshalCanonicalEnvelopeUsesRedactedCopy(t *testing.T) {
	envelope := serializationTestEnvelope()
	before := *envelope
	before.Payload = cloneSerializationPayload(envelope.Payload)
	want, err := json.Marshal(BoundedEnvelope(envelope))
	if err != nil {
		t.Fatal(err)
	}

	got, err := marshalCanonicalEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("serialized envelope differs from bounded envelope:\n got %s\nwant %s", got, want)
	}
	wire := string(got)
	for _, secret := range []string{"Bearer wire-secret", "nested-token-secret", "object-bytes-secret", "message-content-secret"} {
		if strings.Contains(wire, secret) {
			t.Fatalf("wire payload leaked %q: %s", secret, wire)
		}
	}
	for _, key := range []string{"authorization", "token", "object_data", "message_content"} {
		if strings.Contains(strings.ToLower(wire), `"`+key+`"`) {
			t.Fatalf("wire payload retained sensitive key %q: %s", key, wire)
		}
	}
	if !strings.Contains(wire, "safe-value") || !strings.Contains(wire, "nested-safe") {
		t.Fatalf("wire payload lost safe values: %s", wire)
	}
	if !reflect.DeepEqual(*envelope, before) {
		t.Fatalf("serialization mutated the input envelope: before=%#v after=%#v", before, *envelope)
	}
}

func TestMarshalCanonicalEnvelopeRejectsUnserializablePayload(t *testing.T) {
	const secret = "marshal-failure-secret"
	envelope := serializationTestEnvelope()
	envelope.Payload["unserializable"] = serializationFailureValue{secret: secret}
	before := *envelope
	before.Payload = cloneSerializationPayload(envelope.Payload)

	body, err := marshalCanonicalEnvelope(envelope)
	if err == nil {
		t.Fatal("unserializable payload was accepted")
	}
	if body != nil {
		t.Fatalf("marshal failure returned a fallback body: %s", body)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("marshal error leaked source payload: %v", err)
	}
	if !reflect.DeepEqual(*envelope, before) {
		t.Fatalf("failed serialization mutated the input envelope: before=%#v after=%#v", before, *envelope)
	}
}

func TestMarshalCanonicalEnvelopeRejectsBoundedSerializationFailure(t *testing.T) {
	envelope := serializationTestEnvelope()
	envelope.OccurredAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)

	body, err := marshalCanonicalEnvelope(envelope)
	if err == nil {
		t.Fatal("out-of-range timestamp was accepted")
	}
	if body != nil {
		t.Fatalf("marshal failure returned a fallback body: %s", body)
	}
	if strings.Contains(err.Error(), "10000") || strings.Contains(err.Error(), "year") {
		t.Fatalf("marshal error exposed serialization details: %v", err)
	}
}

func TestBoundedEnvelopeBoundsScalarsWithoutMutation(t *testing.T) {
	envelope := serializationTestEnvelope()
	envelope.Source = strings.Repeat("s", MaxSourceBytes+1)
	envelope.ActorUID = strings.Repeat("a", MaxActorUIDBytes+1)
	before := *envelope
	before.Payload = cloneSerializationPayload(envelope.Payload)

	bounded := BoundedEnvelope(envelope)
	if bounded.Source == envelope.Source || !strings.HasPrefix(bounded.Source, "sha256:") {
		t.Fatalf("oversized source was not hashed: %q", bounded.Source)
	}
	if bounded.ActorUID == envelope.ActorUID || !strings.HasPrefix(bounded.ActorUID, "sha256:") {
		t.Fatalf("oversized actor was not hashed: %q", bounded.ActorUID)
	}
	if !reflect.DeepEqual(*envelope, before) {
		t.Fatal("bounding mutated the input envelope")
	}
}

func serializationTestEnvelope() *Envelope {
	accountID := idgen.NewID()
	return &Envelope{
		EventID: idgen.NewID(), Source: "aero-id", SourceRegion: "west",
		EventType: "projection.snapshot", SchemaVersion: CurrentSchemaVersion,
		AggregateType: "projection", AggregateID: accountID.String(), AggregateVersion: 1,
		TenantID: "tenant-a", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{
			"account_id": accountID.String(), "dataset": "aero-vault.buckets",
			"safe": "safe-value",
			"data": map[string]any{
				"nested": "nested-safe", "authorization": "Bearer wire-secret",
				"items": []any{
					map[string]any{"token": "nested-token-secret", "safe_item": true},
					[]any{map[string]any{"object_data": "object-bytes-secret", "count": 3}},
				},
				"message_content": "message-content-secret",
			},
		},
	}
}

type serializationFailureValue struct{ secret string }

func (value serializationFailureValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("cannot marshal " + value.secret)
}

func cloneSerializationPayload(source map[string]any) map[string]any {
	return cloneSerializationValue(source).(map[string]any)
}

func cloneSerializationValue(value any) any {
	switch nested := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(nested))
		for key, child := range nested {
			result[key] = cloneSerializationValue(child)
		}
		return result
	case []any:
		result := make([]any, len(nested))
		for index, child := range nested {
			result[index] = cloneSerializationValue(child)
		}
		return result
	case []string:
		return append([]string(nil), nested...)
	default:
		return value
	}
}
