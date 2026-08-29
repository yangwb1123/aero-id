package event

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

func hashTestEnvelope() *Envelope {
	return &Envelope{
		EventID: idgen.NewID(), Source: "aero-im", SourceRegion: "us-west",
		EventType: "profile.updated", SchemaVersion: 1, AggregateType: "account",
		AggregateID: "source-account", AggregateVersion: 7, ActorUID: "actor-1",
		TenantID: "tenant-a", OperationID: idgen.NewID(), CorrelationID: idgen.NewID(),
		CausationID: idgen.NewID(), TraceID: "trace-1",
		OccurredAt: time.Date(2026, 8, 18, 12, 34, 56, 123456789, time.FixedZone("west", -7*60*60)),
		Payload: map[string]any{
			"z": 1, "a": map[string]any{"nested": "value"},
		},
	}
}

func TestCanonicalContentHashNormalizesJSONTimeAndUUIDs(t *testing.T) {
	first := hashTestEnvelope()
	second := *first
	second.OccurredAt = first.OccurredAt.UTC().Add(-500 * time.Nanosecond)
	second.Payload = map[string]any{
		"a": map[string]any{"nested": "value"},
		"z": float64(1),
	}
	first.Payload["raw"] = jsonRawMessage(` {"b":2,"a":1} `)
	second.Payload["raw"] = jsonRawMessage(`{"a":1,"b":2}`)
	firstHash, err := CanonicalContentHash(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := CanonicalContentHash(&second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("equivalent canonical content hashes differ: %s != %s", firstHash, secondHash)
	}
	if len(firstHash) != 64 || strings.Contains(firstHash, "value") {
		t.Fatalf("unexpected digest representation: %q", firstHash)
	}

	parsed, err := idgen.Parse(strings.ToUpper(first.EventID.String()))
	if err != nil {
		t.Fatal(err)
	}
	second.EventID = parsed
	thirdHash, err := CanonicalContentHash(&second)
	if err != nil {
		t.Fatal(err)
	}
	if thirdHash != firstHash {
		t.Fatalf("canonical UUID representation changed hash: %s != %s", thirdHash, firstHash)
	}
}

func TestCanonicalContentHashBindsEveryEnvelopeField(t *testing.T) {
	base := hashTestEnvelope()
	baseHash, err := CanonicalContentHash(base)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name string
		edit func(*Envelope)
	}{
		{"source_region", func(e *Envelope) { e.SourceRegion = "eu-west" }},
		{"event_type", func(e *Envelope) { e.EventType = "profile.deleted" }},
		{"schema_version", func(e *Envelope) { e.SchemaVersion = 2 }},
		{"aggregate_type", func(e *Envelope) { e.AggregateType = "workspace" }},
		{"aggregate_id", func(e *Envelope) { e.AggregateID = "other-account" }},
		{"aggregate_version", func(e *Envelope) { e.AggregateVersion++ }},
		{"actor_uid", func(e *Envelope) { e.ActorUID = "actor-2" }},
		{"tenant_id", func(e *Envelope) { e.TenantID = "tenant-b" }},
		{"operation_id", func(e *Envelope) { e.OperationID = idgen.NewID() }},
		{"correlation_id", func(e *Envelope) { e.CorrelationID = idgen.NewID() }},
		{"causation_id", func(e *Envelope) { e.CausationID = idgen.NewID() }},
		{"trace_id", func(e *Envelope) { e.TraceID = "trace-2" }},
		{"occurred_at", func(e *Envelope) { e.OccurredAt = e.OccurredAt.Add(time.Microsecond) }},
		{"payload", func(e *Envelope) { e.Payload["z"] = 2 }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := *base
			candidate.Payload = cloneHashPayload(base.Payload)
			mutation.edit(&candidate)
			candidateHash, err := CanonicalContentHash(&candidate)
			if err != nil {
				t.Fatal(err)
			}
			if candidateHash == baseHash {
				t.Fatalf("%s did not change canonical content hash", mutation.name)
			}
		})
	}
}

func TestCanonicalContentHashErrorDoesNotExposePayload(t *testing.T) {
	const secret = "source-payload-secret"
	envelope := hashTestEnvelope()
	envelope.Payload["unserializable"] = secretMarshalValue{secret: secret}
	_, err := CanonicalContentHash(envelope)
	if err == nil {
		t.Fatal("unserializable payload must fail hashing")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("hash error leaked source payload: %v", err)
	}
}

type jsonRawMessage string

func (value jsonRawMessage) MarshalJSON() ([]byte, error) { return []byte(value), nil }

type secretMarshalValue struct{ secret string }

func (value secretMarshalValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("marshal failed for " + value.secret)
}

func cloneHashPayload(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
