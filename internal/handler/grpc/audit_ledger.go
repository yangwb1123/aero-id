package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type AuditLedgerServer struct {
	aeropb.UnimplementedAuditLedgerServiceServer
	audit service.AuditLedgerService
}

func NewAuditLedgerServer(audit service.AuditLedgerService) *AuditLedgerServer {
	return &AuditLedgerServer{audit: audit}
}

func (s *AuditLedgerServer) Register(server *grpc.Server) {
	aeropb.RegisterAuditLedgerServiceServer(server, s)
}

var _ aeropb.AuditLedgerServiceServer = (*AuditLedgerServer)(nil)

func (s *AuditLedgerServer) ListAuditFacts(ctx context.Context, req *aeropb.ListAuditFactsRequest) (*aeropb.ListAuditFactsResponse, error) {
	filter, err := auditFilterFromProto(req)
	if err != nil {
		return nil, err
	}
	items, next, err := s.audit.List(ctx, filter)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result := make([]*aeropb.AuditFact, 0, len(items))
	for _, item := range items {
		converted, err := auditFactToProto(item)
		if err != nil {
			return nil, apperr.GRPCError(err)
		}
		result = append(result, converted)
	}
	return &aeropb.ListAuditFactsResponse{Events: result, NextCursor: next}, nil
}

func auditFilterFromProto(req *aeropb.ListAuditFactsRequest) (domain.AuditFactFilter, error) {
	filter := domain.AuditFactFilter{Source: req.GetSource(), ActorUID: req.GetActorUid(), RequestID: req.GetRequestId(),
		TenantID: req.GetTenantId(), Action: req.GetAction(), ResourceType: req.GetResourceType(), Page: pageFromProto(req.GetPage())}
	if req.GetFrom() != nil {
		from := req.GetFrom().AsTime()
		filter.From = &from
	}
	if req.GetTo() != nil {
		to := req.GetTo().AsTime()
		filter.To = &to
	}
	if filter.From != nil && filter.To != nil && !filter.From.Before(*filter.To) {
		return domain.AuditFactFilter{}, apperr.GRPCError(apperr.BadRequest("audit.invalid_time_range", "audit from must be before to"))
	}
	var err error
	filter.AccountID, err = parseOptionalAuditID(req.GetAccountId(), "account_id")
	if err != nil {
		return domain.AuditFactFilter{}, err
	}
	filter.OperationID, err = parseOptionalAuditID(req.GetOperationId(), "operation_id")
	return filter, err
}

func auditFactToProto(fact *domain.AuditFact) (*aeropb.AuditFact, error) {
	if fact == nil {
		return nil, nil
	}
	payload, err := structpb.NewStruct(fact.Payload)
	if err != nil {
		return nil, apperr.Internal("audit.payload_invalid", "audit payload is invalid").Wrap(err)
	}
	return &aeropb.AuditFact{
		Id: fact.ID.String(), ChainSequence: fact.ChainSequence, Source: fact.Source, SourceRegion: fact.SourceRegion,
		PartitionKey: fact.PartitionKey, EventId: optionalID(fact.EventID), OperationId: optionalID(fact.OperationID),
		CorrelationId: optionalID(fact.CorrelationID), CausationId: optionalID(fact.CausationID), TraceId: fact.TraceID,
		RequestId: fact.RequestID, ActorUid: fact.ActorUID, AccountId: optionalID(fact.AccountID), TenantId: fact.TenantID, Action: fact.Action,
		ResourceType: fact.ResourceType, ResourceId: fact.ResourceID, OccurredAt: timestamppb.New(fact.OccurredAt),
		Payload: payload, PayloadHash: fact.PayloadHash, PreviousHash: fact.PreviousHash, RecordHash: fact.RecordHash,
		SignatureVersion: auditVersion(fact.SignatureVersion), Signature: fact.Signature,
		CreatedAt: timestamppb.New(fact.CreatedAt), HashVersion: auditVersion(fact.HashVersion),
	}, nil
}

func auditVersion(value int) int32 {
	if value < 0 {
		return 0
	}
	if value > 1<<31-1 {
		return 1<<31 - 1
	}
	return int32(value) //nolint:gosec // value is bounded to the int32 range above
}

func parseOptionalAuditID(value, field string) (domain.ID, error) {
	if value == "" {
		return domain.ID{}, nil
	}
	id, err := idgen.Parse(value)
	if err != nil {
		return domain.ID{}, apperr.GRPCError(apperr.BadRequest("audit.invalid_id", "invalid "+field))
	}
	return id, nil
}

func optionalID(value domain.ID) string {
	if value == (domain.ID{}) {
		return ""
	}
	return value.String()
}
