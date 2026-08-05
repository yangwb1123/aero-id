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

func TestEventIngestServer_AppliesVerifiedSourceBinding(t *testing.T) {
	fake := &fakeGRPCEventIngestService{}
	server := NewEventIngestServer(fake, WithEventSourceBindings(event.SourceBindings{
		"source-client": {TenantID: "tenant-1", Source: "aero-im", SourceRegion: "west"},
	}))
	accountID := idgen.NewID()
	payload, err := structpb.NewStruct(map[string]any{
		"account_id": accountID.String(), "dataset": string(domain.DatasetAeroIMProfile), "data": map[string]any{},
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
