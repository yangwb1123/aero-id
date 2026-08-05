package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// fakeReceiptRepo records quarantine calls and always applies events.
type fakeReceiptRepo struct {
	quarantined []string // error codes
	applied     []string // event ids
}

func (f *fakeReceiptRepo) ApplySnapshotEvent(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation) (repository.EventApplyResult, error) {
	f.applied = append(f.applied, envelope.EventID.String())
	return repository.EventApplyResult{Status: repository.EventApplied}, nil
}

func (f *fakeReceiptRepo) ApplySnapshotEventWithAudit(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation, fact *domain.AuditFact) (repository.EventApplyResult, error) {
	return f.ApplySnapshotEvent(ctx, envelope, mutation)
}

func (f *fakeReceiptRepo) QuarantineEvent(ctx context.Context, envelope *event.Envelope, code string) error {
	f.quarantined = append(f.quarantined, code)
	return nil
}

func testEnvelope(region string) *event.Envelope {
	accountID := idgen.NewID()
	return &event.Envelope{
		EventID:          idgen.NewID(),
		Source:           "aero-vault",
		SourceRegion:     region,
		EventType:        "vault.object.created",
		SchemaVersion:    1,
		AggregateType:    "object",
		AggregateID:      "obj-1",
		AggregateVersion: 3,
		TenantID:         "tenant-a",
		OccurredAt:       time.Now().UTC(),
		Payload: map[string]any{
			"account_id": accountID.String(),
			"dataset":    "aero-vault.buckets",
			"bucket_id":  "b1",
			"status":     "fresh",
			"version":    float64(3),
			"data":       map[string]any{"usage_bytes": 42},
		},
	}
}

func TestEventIngest_ResidencyPolicyQuarantinesDisallowedRegion(t *testing.T) {
	receipts := &fakeReceiptRepo{}
	svc := NewEventIngestService(receipts, WithEventAllowedRegions([]string{"us-west", "eu-west"}))

	_, err := svc.Ingest(context.Background(), testEnvelope("cn-north"))
	if err == nil {
		t.Fatal("ingest must reject a region outside the residency allow-list")
	}
	if len(receipts.quarantined) != 1 || receipts.quarantined[0] != "event.region_not_allowed" {
		t.Fatalf("quarantine codes = %v, want [event.region_not_allowed]", receipts.quarantined)
	}
	if len(receipts.applied) != 0 {
		t.Fatalf("event must not be applied, applied = %v", receipts.applied)
	}
}

func TestEventIngest_ResidencyPolicyAllowsListedRegion(t *testing.T) {
	receipts := &fakeReceiptRepo{}
	svc := NewEventIngestService(receipts, WithEventAllowedRegions([]string{"us-west"}))

	result, err := svc.Ingest(context.Background(), testEnvelope("us-west"))
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}
	if result.Status != "applied" {
		t.Errorf("status = %q, want applied", result.Status)
	}
	if len(receipts.quarantined) != 0 {
		t.Errorf("unexpected quarantine: %v", receipts.quarantined)
	}
}

func TestEventIngest_NoPolicyAcceptsAnyRegion(t *testing.T) {
	receipts := &fakeReceiptRepo{}
	svc := NewEventIngestService(receipts) // no regions => single-region compatibility

	result, err := svc.Ingest(context.Background(), testEnvelope("somewhere-else"))
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}
	if result.Status != "applied" {
		t.Errorf("status = %q, want applied", result.Status)
	}
	if len(receipts.quarantined) != 0 {
		t.Errorf("unexpected quarantine: %v", receipts.quarantined)
	}
}

func TestEventIngest_InvalidEnvelopeRejectedBeforeResidency(t *testing.T) {
	receipts := &fakeReceiptRepo{}
	svc := NewEventIngestService(receipts, WithEventAllowedRegions([]string{"us-west"}))

	envelope := testEnvelope("us-west")
	envelope.EventID = domain.ID{} // invalid
	if _, err := svc.Ingest(context.Background(), envelope); err == nil {
		t.Fatal("invalid envelope must be rejected")
	}
}

func TestEventIngest_ApplyFailureIsNotQuarantine(t *testing.T) {
	receipts := &failApplyReceiptRepo{}
	svc := NewEventIngestService(receipts, WithEventAllowedRegions([]string{"us-west"}))

	_, err := svc.Ingest(context.Background(), testEnvelope("us-west"))
	if err == nil {
		t.Fatal("apply failure must surface an error")
	}
	if len(receipts.quarantined) != 0 {
		t.Errorf("apply failure must not quarantine, got %v", receipts.quarantined)
	}
}

type failApplyReceiptRepo struct {
	fakeReceiptRepo
}

func (f *failApplyReceiptRepo) ApplySnapshotEvent(ctx context.Context, envelope *event.Envelope,
	mutation *event.SnapshotMutation) (repository.EventApplyResult, error) {
	return repository.EventApplyResult{}, errors.New("apply exploded")
}
