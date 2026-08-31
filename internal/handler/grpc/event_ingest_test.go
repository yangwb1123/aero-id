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
	items int
}

func (f *itemGRPCEventIngestService) IngestItem(_ context.Context, item service.EventIngestItem) *service.EventIngestResult {
	f.items++
	if item.CanonicalizationCode != "" {
		return &service.EventIngestResult{EventID: item.Envelope.EventID.String(), Status: "quarantined",
			ErrorCode: item.CanonicalizationCode}
	}
	return &service.EventIngestResult{EventID: item.Envelope.EventID.String(), Status: "applied"}
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
