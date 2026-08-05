package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type EventIngestServer struct {
	aeropb.UnimplementedSourceIngestServiceServer
	ingest   service.EventIngestService
	bindings event.SourceBindings
}

const maxEventBatch = 100

type EventIngestOption func(*EventIngestServer)

func WithEventSourceBindings(bindings event.SourceBindings) EventIngestOption {
	return func(server *EventIngestServer) { server.bindings = bindings }
}

func NewEventIngestServer(ingest service.EventIngestService, options ...EventIngestOption) *EventIngestServer {
	server := &EventIngestServer{ingest: ingest}
	for _, option := range options {
		if option != nil {
			option(server)
		}
	}
	return server
}

func (s *EventIngestServer) Register(server *grpc.Server) {
	aeropb.RegisterSourceIngestServiceServer(server, s)
}

var _ aeropb.SourceIngestServiceServer = (*EventIngestServer)(nil)

func (s *EventIngestServer) IngestEvents(ctx context.Context, req *aeropb.IngestEventsRequest) (*aeropb.IngestEventsResponse, error) {
	if req == nil || len(req.GetEvents()) == 0 || len(req.GetEvents()) > maxEventBatch {
		return nil, grpcError(apperr.BadRequest("event.batch_invalid", "event batch must contain one to one hundred events"))
	}
	binding, err := s.resolveBinding(ctx)
	if err != nil {
		return nil, grpcError(err)
	}
	results := make([]*aeropb.EventIngestResult, 0, len(req.GetEvents()))
	for _, item := range req.GetEvents() {
		envelope, err := canonicalEventFromProto(item)
		if err != nil {
			return nil, grpcError(err)
		}
		if binding != nil {
			if err := binding.Apply(envelope); err != nil {
				return nil, grpcError(apperr.Forbidden("event.source_unauthorized", "event source is not authorized"))
			}
		}
		result, err := s.ingest.Ingest(ctx, envelope)
		if err != nil {
			return nil, grpcError(err)
		}
		results = append(results, &aeropb.EventIngestResult{EventId: result.EventID, Status: result.Status})
	}
	return &aeropb.IngestEventsResponse{Results: results}, nil
}

func (s *EventIngestServer) resolveBinding(ctx context.Context) (*event.SourceBinding, error) {
	identity, ok := service.IdentityFrom(ctx)
	if !ok {
		return nil, apperr.Unauthorized("event.source_identity_required", "verified source identity is required")
	}
	binding, err := s.bindings.Resolve(identity)
	if err != nil {
		return nil, apperr.Forbidden("event.source_unauthorized", "event source is not authorized")
	}
	return &binding, nil
}

func canonicalEventFromProto(item *aeropb.CanonicalEvent) (*event.Envelope, error) {
	if item == nil {
		return nil, apperr.BadRequest("event.invalid", "event is required")
	}
	eventID, err := idgen.Parse(item.GetEventId())
	if err != nil {
		return nil, apperr.BadRequest("event.invalid_id", "event_id is invalid")
	}
	operationID, err := optionalEventID(item.GetOperationId())
	if err != nil {
		return nil, err
	}
	correlationID, err := optionalEventID(item.GetCorrelationId())
	if err != nil {
		return nil, err
	}
	causationID, err := optionalEventID(item.GetCausationId())
	if err != nil {
		return nil, err
	}
	occurredAt := time.Time{}
	if item.GetOccurredAt() != nil {
		occurredAt = item.GetOccurredAt().AsTime()
	}
	var payload map[string]any
	if item.GetPayload() != nil {
		payload = item.GetPayload().AsMap()
	}
	return &event.Envelope{EventID: eventID, Source: item.GetSource(), SourceRegion: item.GetSourceRegion(),
		EventType: item.GetEventType(), SchemaVersion: int(item.GetSchemaVersion()), AggregateType: item.GetAggregateType(),
		AggregateID: item.GetAggregateId(), AggregateVersion: item.GetAggregateVersion(), ActorUID: item.GetActorUid(),
		TenantID: item.GetTenantId(), OperationID: operationID, CorrelationID: correlationID, CausationID: causationID,
		TraceID: item.GetTraceId(), OccurredAt: occurredAt, Payload: payload}, nil
}

func optionalEventID(value string) (domain.ID, error) {
	if value == "" {
		return domain.ID{}, nil
	}
	id, err := idgen.Parse(value)
	if err != nil {
		return domain.ID{}, apperr.BadRequest("event.invalid_id", "event identifier is invalid")
	}
	return id, nil
}
