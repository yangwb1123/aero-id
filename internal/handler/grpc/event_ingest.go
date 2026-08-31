package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

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
	resolver event.SourceBindingResolver
}

const maxEventBatch = 100

type EventIngestOption func(*EventIngestServer)

func WithEventSourceBindings(bindings event.SourceBindings) EventIngestOption {
	return func(server *EventIngestServer) { server.bindings = bindings }
}

func WithEventSourceBindingResolver(resolver event.SourceBindingResolver) EventIngestOption {
	return func(server *EventIngestServer) { server.resolver = resolver }
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
		return nil, apperr.GRPCError(apperr.BadRequest("event.batch_invalid", "event batch must contain one to one hundred events"))
	}
	binding, err := s.resolveBinding(ctx)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	itemService, itemMode := s.ingest.(service.EventItemIngestService)
	results := make([]*aeropb.EventIngestResult, 0, len(req.GetEvents()))
	for _, wire := range req.GetEvents() {
		result, err := s.ingestOne(ctx, wire, binding, itemService, itemMode)
		if err != nil {
			return nil, apperr.GRPCError(err)
		}
		results = append(results, resultProto(result))
	}
	return &aeropb.IngestEventsResponse{Results: results}, nil
}

func (s *EventIngestServer) ingestOne(ctx context.Context, wire *aeropb.CanonicalEvent,
	binding *event.SourceBinding, itemService service.EventItemIngestService, itemMode bool) (*service.EventIngestResult, error) {
	item := canonicalEventItemFromProto(wire)
	if item.Envelope == nil {
		if !itemMode {
			return nil, apperr.BadRequest("event.invalid", "event is required")
		}
		return &service.EventIngestResult{Status: "rejected", ErrorCode: "event.invalid"}, nil
	}
	if binding != nil {
		// Exact bindings provide the authenticated tenant before the event is
		// inspected. Wildcard bindings first prove the wire tenant is in the
		// verified delegation allow-list; the body is never authority by itself.
		if binding.TenantID != "*" {
			finalizeWireIdentity(&item, wire, binding.TenantID, binding.Source)
		}
		if err := binding.Apply(item.Envelope); err != nil {
			if !itemMode {
				return nil, apperr.Forbidden("event.source_unauthorized", "event source is not authorized")
			}
			return &service.EventIngestResult{EventID: parsedEventID(item), Status: "rejected",
				ErrorCode: "event.source_unauthorized"}, nil
		}
	}
	if binding == nil || binding.TenantID == "*" {
		finalizeWireIdentity(&item, wire, item.Envelope.TenantID, item.Envelope.Source)
	}
	if itemMode {
		return itemService.IngestItem(ctx, item), nil
	}
	// Compatibility path for older injected implementations.
	return s.ingest.Ingest(ctx, item.Envelope)
}

func resultProto(result *service.EventIngestResult) *aeropb.EventIngestResult {
	if result == nil {
		return &aeropb.EventIngestResult{Status: "failed", ErrorCode: "event.ingest_failed", Retryable: true}
	}
	eventID := ""
	if id, err := idgen.Parse(result.EventID); err == nil {
		eventID = id.String()
	}
	return &aeropb.EventIngestResult{EventId: eventID, Status: result.Status,
		ErrorCode: result.ErrorCode, Retryable: result.Retryable}
}

func (s *EventIngestServer) resolveBinding(ctx context.Context) (*event.SourceBinding, error) {
	identity, ok := service.IdentityFrom(ctx)
	if !ok {
		return nil, apperr.Unauthorized("event.source_identity_required", "verified source identity is required")
	}
	resolver := s.resolver
	if resolver == nil {
		resolver = s.bindings
	}
	binding, err := resolver.Resolve(identity)
	if err != nil {
		return nil, apperr.Forbidden("event.source_unauthorized", "event source is not authorized")
	}
	return &binding, nil
}

func canonicalEventItemFromProto(item *aeropb.CanonicalEvent) service.EventIngestItem {
	if item == nil {
		return service.EventIngestItem{CanonicalizationCode: "event.invalid"}
	}
	eventID, eventIDErr := parseEventID(item.GetEventId())
	operationID, operationErr := optionalEventID(item.GetOperationId())
	correlationID, correlationErr := optionalEventID(item.GetCorrelationId())
	causationID, causationErr := optionalEventID(item.GetCausationId())
	code := canonicalizationCode(eventIDErr, operationErr, correlationErr, causationErr)
	occurredAt, timestampErr := canonicalTimestamp(item)
	if code == "" && timestampErr {
		code = "event.invalid"
	}
	oversized := proto.Size(item) > event.MaxEventBytes
	var payload map[string]any
	if !oversized && item.GetPayload() != nil {
		payload = item.GetPayload().AsMap()
	}
	envelope := &event.Envelope{EventID: eventID, Source: item.GetSource(), SourceRegion: item.GetSourceRegion(),
		EventType: item.GetEventType(), SchemaVersion: int(item.GetSchemaVersion()), AggregateType: item.GetAggregateType(),
		AggregateID: item.GetAggregateId(), AggregateVersion: item.GetAggregateVersion(), ActorUID: item.GetActorUid(),
		TenantID: item.GetTenantId(), OperationID: operationID, CorrelationID: correlationID, CausationID: causationID,
		TraceID: item.GetTraceId(), OccurredAt: occurredAt, Payload: payload}
	if oversized || oversizedEnvelopeString(item) {
		code = "event.invalid"
	}
	result := service.EventIngestItem{Envelope: envelope, CanonicalizationCode: code}
	if code != "" {
		result.CanonicalizationStage = "canonicalization"
		result.OriginalWireEventID = safeWireID(wireID(item))
	}
	return result
}

func canonicalizationCode(eventErr, operationErr, correlationErr, causationErr error) string {
	if eventErr != nil || operationErr != nil || correlationErr != nil || causationErr != nil {
		return "event.invalid_id"
	}
	return ""
}

func canonicalTimestamp(item *aeropb.CanonicalEvent) (time.Time, bool) {
	stamp := item.GetOccurredAt()
	if stamp == nil {
		return time.Time{}, false
	}
	if err := stamp.CheckValid(); err != nil {
		return time.Time{}, true
	}
	return stamp.AsTime(), false
}

func finalizeWireIdentity(item *service.EventIngestItem, wire *aeropb.CanonicalEvent, authenticatedTenant, authenticatedSource string) {
	if item.CanonicalizationCode == "" {
		return
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(wire)
	if err != nil {
		item.CanonicalizationCode = "event.invalid"
		return
	}
	material := fingerprintMaterial(authenticatedTenant, authenticatedSource, encoded)
	sum := sha256.Sum256(material)
	item.WireFingerprint = hex.EncodeToString(sum[:])
	if item.Envelope.EventID == (domain.ID{}) {
		item.Envelope.EventID = idgen.Anchor("event.quarantine.v1", item.WireFingerprint)
	}
}

func wireID(item *aeropb.CanonicalEvent) string {
	if item == nil {
		return ""
	}
	return item.GetEventId()
}

func safeWireID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, value)
	if len(value) <= 256 {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return value[:239] + "#" + hex.EncodeToString(sum[:])[:16]
}

func parsedEventID(item service.EventIngestItem) string {
	if item.Envelope != nil && item.Envelope.EventID != (domain.ID{}) {
		return item.Envelope.EventID.String()
	}
	return ""
}

func oversizedEnvelopeString(item *aeropb.CanonicalEvent) bool {
	values := []string{item.GetEventId(), item.GetOperationId(), item.GetCorrelationId(), item.GetCausationId(),
		item.GetSource(), item.GetSourceRegion(), item.GetEventType(), item.GetAggregateType(), item.GetAggregateId(),
		item.GetActorUid(), item.GetTenantId(), item.GetTraceId()}
	limits := []int{event.MaxIDBytes, event.MaxIDBytes, event.MaxIDBytes, event.MaxIDBytes, event.MaxSourceBytes,
		event.MaxRegionBytes, event.MaxEventTypeBytes, event.MaxAggregateTypeBytes, event.MaxAggregateIDBytes,
		event.MaxActorUIDBytes, event.MaxTenantIDBytes, event.MaxTraceIDBytes}
	for index, value := range values {
		if len(value) > limits[index] {
			return true
		}
	}
	return false
}

func fingerprintMaterial(tenant, source string, wire []byte) []byte {
	// The version and length prefixes make the authenticated context
	// unambiguous even if a future issuer permits separators in claim values.
	material := make([]byte, 0, len(wire)+len(tenant)+len(source)+64)
	material = append(material, "event.quarantine.fingerprint.v1"...)
	for _, part := range []string{strings.TrimSpace(tenant), strings.TrimSpace(source)} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		material = append(material, length[:]...)
		material = append(material, part...)
	}
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(wire)))
	material = append(material, length[:]...)
	return append(material, wire...)
}

func parseEventID(value string) (domain.ID, error) {
	if len(value) > event.MaxIDBytes {
		return domain.ID{}, idgen.ErrZeroID
	}
	return idgen.Parse(value)
}

func optionalEventID(value string) (domain.ID, error) {
	if value == "" {
		return domain.ID{}, nil
	}
	if len(value) > event.MaxIDBytes {
		return domain.ID{}, apperr.BadRequest("event.invalid_id", "event identifier is invalid")
	}
	id, err := idgen.Parse(value)
	if err != nil {
		return domain.ID{}, apperr.BadRequest("event.invalid_id", "event identifier is invalid")
	}
	return id, nil
}
