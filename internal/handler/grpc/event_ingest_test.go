package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeGRPCEventIngestService struct{ events []*event.Envelope }

func (f *fakeGRPCEventIngestService) Ingest(_ context.Context, value *event.Envelope) (*service.EventIngestResult, error) {
	f.events = append(f.events, value)
	return &service.EventIngestResult{EventID: value.EventID.String(), Status: "applied"}, nil
}

type itemGRPCEventIngestService struct {
	fakeGRPCEventIngestService
	items    int
	captured []service.EventIngestItem
}

func (f *itemGRPCEventIngestService) IngestItem(_ context.Context, item service.EventIngestItem) *service.EventIngestResult {
	f.items++
	f.captured = append(f.captured, item)
	if item.CanonicalizationCode != "" {
		return &service.EventIngestResult{EventID: item.Envelope.EventID.String(), Status: "quarantined",
			ErrorCode: item.CanonicalizationCode}
	}
	return &service.EventIngestResult{EventID: item.Envelope.EventID.String(), Status: "applied"}
}

// TestEventIngestServer_PreservesVerbatimWireEventID pins this module's upstream
// contract: a legal RFC-4122 alternative rendering (urn:uuid:, upper-case,
// 32-hex, braced) reaches the service verbatim as OriginalWireEventID, while
// Envelope.EventID carries the parsed value. The downstream provenance record
// must describe what the source actually sent — pre-normalizing it here would
// make the identity comparison trivially pass and destroy that evidence.
func TestEventIngestServer_PreservesVerbatimWireEventID(t *testing.T) {
	const parsedText = "0195b2c6-9f40-7c01-8000-000000000001"
	parsed, err := idgen.Parse(parsedText)
	if err != nil {
		t.Fatalf("fixture %q does not parse: %v", parsedText, err)
	}
	wireTexts := []string{
		"urn:uuid:0195B2C6-9F40-7C01-8000-000000000001",
		"0195B2C6-9F40-7C01-8000-000000000001",
		"0195b2c69f407c018000000000000001",
		"{0195b2c6-9f40-7c01-8000-000000000001}",
	}

	for _, wireText := range wireTexts {
		t.Run(wireText, func(t *testing.T) {
			fake := &itemGRPCEventIngestService{}
			server := NewEventIngestServer(fake, WithEventSourceBindings(event.SourceBindings{
				"source-client": {TenantID: "tenant-1", Source: "aero-im", SourceRegion: "west"},
			}))
			ctx := service.WithIdentity(context.Background(),
				&domain.TokenIdentity{UID: "source-client", TenantID: "tenant-1", ClientID: "source-client"})
			// The invalid optional operation_id forces canonicalization so the
			// wire-identity provenance fields are populated at all.
			if _, err := server.IngestEvents(ctx, &aeropb.IngestEventsRequest{Events: []*aeropb.CanonicalEvent{{
				EventId: wireText, OperationId: "not-a-uuid", Source: "aero-im", SourceRegion: "west",
				EventType: "account.updated", SchemaVersion: 1, AggregateType: "account", AggregateId: "source-account",
				AggregateVersion: 1, TenantId: "tenant-1", OccurredAt: timestamppb.Now(),
			}}}); err != nil {
				t.Fatal(err)
			}
			if len(fake.captured) != 1 {
				t.Fatalf("captured items = %d, want 1", len(fake.captured))
			}
			item := fake.captured[0]
			if item.CanonicalizationCode != "event.invalid_id" {
				t.Fatalf("canonicalization code = %q, want event.invalid_id", item.CanonicalizationCode)
			}
			if item.OriginalWireEventID != wireText {
				t.Fatalf("OriginalWireEventID = %q, want the verbatim wire text %q", item.OriginalWireEventID, wireText)
			}
			if item.Envelope == nil || item.Envelope.EventID != parsed {
				t.Fatalf("envelope event id = %v, want the parsed %s", item.Envelope, parsed)
			}
			if item.WireFingerprint == "" {
				t.Fatal("wire fingerprint must be set on the canonicalization path")
			}
		})
	}
}

// TestEventIngestServer_OmitsWireIdentityWithoutCanonicalization pins the other
// half of the contract: the wire-identity provenance fields are populated only
// for a canonicalized item, so a valid item never carries provenance text.
func TestEventIngestServer_OmitsWireIdentityWithoutCanonicalization(t *testing.T) {
	fake := &itemGRPCEventIngestService{}
	server := NewEventIngestServer(fake, WithEventSourceBindings(event.SourceBindings{
		"source-client": {TenantID: "tenant-1", Source: "aero-im", SourceRegion: "west"},
	}))
	ctx := service.WithIdentity(context.Background(),
		&domain.TokenIdentity{UID: "source-client", TenantID: "tenant-1", ClientID: "source-client"})
	if _, err := server.IngestEvents(ctx, &aeropb.IngestEventsRequest{Events: []*aeropb.CanonicalEvent{{
		EventId: idgen.NewID().String(), Source: "aero-im", SourceRegion: "west", EventType: "account.updated",
		SchemaVersion: 1, AggregateType: "account", AggregateId: "source-account", AggregateVersion: 1,
		TenantId: "tenant-1", OccurredAt: timestamppb.Now(),
	}}}); err != nil {
		t.Fatal(err)
	}
	if len(fake.captured) != 1 {
		t.Fatalf("captured items = %d, want 1", len(fake.captured))
	}
	item := fake.captured[0]
	if item.CanonicalizationCode != "" || item.OriginalWireEventID != "" || item.WireFingerprint != "" {
		t.Fatalf("valid item must carry no wire provenance: code=%q wire=%q fingerprint=%q",
			item.CanonicalizationCode, item.OriginalWireEventID, item.WireFingerprint)
	}
}

func TestEventIngestServer_AppliesVerifiedSourceBinding(t *testing.T) {
	fake := &fakeGRPCEventIngestService{}
	server := NewEventIngestServer(fake, WithEventSourceBindings(event.SourceBindings{
		"source-client": {TenantID: "tenant-1", Source: "aero-im", SourceRegion: "west"},
	}))
	accountID := idgen.NewID()
	payload, err := structpb.NewStruct(map[string]any{
		"account_id": accountID.String(), "dataset": string(domain.AccountDataset("aero-im.profile")), "data": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := service.WithIdentity(context.Background(), &domain.TokenIdentity{UID: "source-client", TenantID: "tenant-1", ClientID: "source-client"})
	result, err := server.IngestEvents(ctx, &aeropb.IngestEventsRequest{Events: []*aeropb.CanonicalEvent{{
		EventId: idgen.NewID().String(), Source: "aero-im", SourceRegion: "west", EventType: "account.updated",
		SchemaVersion: 1, AggregateType: "account", AggregateId: "source-account", AggregateVersion: 1,
		TenantId: "tenant-1", OccurredAt: timestamppb.New(time.Now().UTC()), Payload: payload,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.GetResults()) != 1 || len(fake.events) != 1 || fake.events[0].TenantID != "tenant-1" {
		t.Fatalf("unexpected ingest result: %#v %#v", result, fake.events)
	}
}

func TestEventIngestServer_ReturnsOrderedPerItemResults(t *testing.T) {
	fake := &itemGRPCEventIngestService{}
	server := NewEventIngestServer(fake, WithEventSourceBindings(event.SourceBindings{
		"source-client": {TenantID: "tenant-1", Source: "aero-im", SourceRegion: "west"},
	}))
	ctx := service.WithIdentity(context.Background(), &domain.TokenIdentity{UID: "source-client", TenantID: "tenant-1", ClientID: "source-client"})
	valid := func(id, source string) *aeropb.CanonicalEvent {
		return &aeropb.CanonicalEvent{EventId: id, Source: source, SourceRegion: "west", EventType: "account.updated",
			SchemaVersion: 1, AggregateType: "account", AggregateId: "source-account", AggregateVersion: 1,
			TenantId: "tenant-1", OccurredAt: timestamppb.Now()}
	}
	response, err := server.IngestEvents(ctx, &aeropb.IngestEventsRequest{Events: []*aeropb.CanonicalEvent{
		valid("not-a-uuid", "aero-im"), valid(idgen.NewID().String(), "aero-vault"), valid(idgen.NewID().String(), "aero-im"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	results := response.GetResults()
	if len(results) != 3 || fake.items != 2 {
		t.Fatalf("results/items = %d/%d, want 3/2: %#v", len(results), fake.items, results)
	}
	if results[0].GetStatus() != "quarantined" || results[0].GetErrorCode() != "event.invalid_id" || results[0].GetEventId() == "not-a-uuid" || results[0].GetEventId() == "" {
		t.Fatalf("malformed result = %#v", results[0])
	}
	if results[1].GetStatus() != "rejected" || results[1].GetErrorCode() != "event.source_unauthorized" || results[1].GetEventId() == "" {
		t.Fatalf("binding result = %#v", results[1])
	}
	if results[2].GetStatus() != "applied" || results[2].GetErrorCode() != "" {
		t.Fatalf("valid result = %#v", results[2])
	}
}

func TestEventIngestServer_RejectsSourceBindingMismatch(t *testing.T) {
	server := NewEventIngestServer(&fakeGRPCEventIngestService{}, WithEventSourceBindings(event.SourceBindings{
		"source-client": {TenantID: "tenant-1", Source: "aero-im", SourceRegion: "west"},
	}))
	ctx := service.WithIdentity(context.Background(), &domain.TokenIdentity{UID: "source-client", TenantID: "tenant-1", ClientID: "source-client"})
	_, err := server.IngestEvents(ctx, &aeropb.IngestEventsRequest{Events: []*aeropb.CanonicalEvent{{
		EventId: idgen.NewID().String(), Source: "aero-vault", SourceRegion: "west", EventType: "account.updated",
		SchemaVersion: 1, AggregateType: "account", AggregateId: "source-account", AggregateVersion: 1,
		TenantId: "tenant-1", OccurredAt: timestamppb.Now(),
	}}})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status code = %s, want PermissionDenied: %v", status.Code(err), err)
	}
}
