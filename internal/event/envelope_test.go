package event

import (
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

func TestEnvelopeSnapshotAllowListsAndRedacts(t *testing.T) {
	accountID := idgen.NewID()
	envelope := &Envelope{EventID: idgen.NewID(), Source: "aero-im", SourceRegion: "west", EventType: "profile.updated",
		SchemaVersion: 1, AggregateType: "account", AggregateID: "user-1", AggregateVersion: 2, OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"account_id": accountID.String(), "dataset": string(domain.AccountDataset("aero-im.profile")),
			"data": map[string]any{"display_name": "Ada", "access_token": "secret", "content": "body"}}}
	mutation, err := envelope.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if mutation.Data["display_name"] != "Ada" || mutation.Data["access_token"] != nil || mutation.Data["content"] != nil {
		t.Fatalf("projection redaction failed: %#v", mutation.Data)
	}
}

func TestEnvelopeSnapshotRejectsWrongSourceDataset(t *testing.T) {
	envelope := &Envelope{EventID: idgen.NewID(), Source: "aero-im", SourceRegion: "west", EventType: "changed",
		SchemaVersion: 1, AggregateType: "account", AggregateID: "user-1", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"account_id": idgen.NewID().String(), "dataset": string(domain.AccountDataset("aero-vault.usage")), "data": map[string]any{}}}
	if _, err := envelope.Snapshot(); err == nil {
		t.Fatal("wrong source dataset was accepted")
	}
}

func TestEnvelopeRejectsInvalidVersionAndStatus(t *testing.T) {
	base := &Envelope{EventID: idgen.NewID(), Source: "aero-im", SourceRegion: "west", EventType: "changed",
		SchemaVersion: 1, AggregateType: "account", AggregateID: "user-1", AggregateVersion: 1, OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"account_id": idgen.NewID().String(), "dataset": string(domain.AccountDataset("aero-im.profile")),
			"data": map[string]any{}, "status": "corrupted"}}
	if _, err := base.Snapshot(); err == nil {
		t.Fatal("invalid snapshot status was accepted")
	}
	base.Payload["status"] = string(domain.SnapshotFresh)
	base.AggregateVersion = 0
	if _, err := base.Snapshot(); err == nil {
		t.Fatal("zero aggregate version was accepted")
	}
}
