package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/handler/grpc/aeropb"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/service"
)

type OperationServer struct {
	aeropb.UnimplementedOperationServiceServer
	operations service.OperationService
}

func NewOperationServer(operations service.OperationService) *OperationServer {
	return &OperationServer{operations: operations}
}

func (s *OperationServer) Register(server *grpc.Server) {
	aeropb.RegisterOperationServiceServer(server, s)
}

var _ aeropb.OperationServiceServer = (*OperationServer)(nil)

func (s *OperationServer) CreateOperation(ctx context.Context, req *aeropb.CreateOperationRequest) (*aeropb.CreateOperationResponse, error) {
	accountID, err := parseOptionalOperationID(req.GetAccountId(), "account_id")
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if req.GetRequestPayload() != nil {
		payload = req.GetRequestPayload().AsMap()
	}
	steps := make([]service.OperationStepInput, 0, len(req.GetSteps()))
	for _, step := range req.GetSteps() {
		if step == nil {
			continue
		}
		var stepPayload map[string]any
		if step.GetRequestPayload() != nil {
			stepPayload = step.GetRequestPayload().AsMap()
		}
		steps = append(steps, service.OperationStepInput{StepKey: step.GetStepKey(), Source: step.GetSource(), Action: step.GetAction(),
			RequestPayload: stepPayload, CompensationAction: step.GetCompensationAction()})
	}
	operation, err := s.operations.Create(ctx, service.OperationInput{AccountID: accountID, HomeRegion: req.GetHomeRegion(),
		OperationType: req.GetOperationType(), IdempotencyKey: req.GetIdempotencyKey(), RequestPayload: payload, Steps: steps})
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result, err := operationToProto(operation)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.CreateOperationResponse{Operation: result}, nil
}

func (s *OperationServer) GetOperation(ctx context.Context, req *aeropb.GetOperationRequest) (*aeropb.GetOperationResponse, error) {
	id, err := parseID(req.GetOperationId(), "operation_id")
	if err != nil {
		return nil, err
	}
	operation, err := s.operations.Get(ctx, id)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result, err := operationToProto(operation)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.GetOperationResponse{Operation: result}, nil
}

func (s *OperationServer) ListOperations(ctx context.Context, req *aeropb.ListOperationsRequest) (*aeropb.ListOperationsResponse, error) {
	items, next, err := s.operations.List(ctx, pageFromProto(req.GetPage()))
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result := make([]*aeropb.Operation, 0, len(items))
	for _, item := range items {
		converted, err := operationToProto(item)
		if err != nil {
			return nil, apperr.GRPCError(err)
		}
		result = append(result, converted)
	}
	return &aeropb.ListOperationsResponse{Operations: result, NextCursor: next}, nil
}

func (s *OperationServer) RunOperation(ctx context.Context, req *aeropb.RunOperationRequest) (*aeropb.RunOperationResponse, error) {
	id, err := parseID(req.GetOperationId(), "operation_id")
	if err != nil {
		return nil, err
	}
	operation, err := s.operations.Run(ctx, id, req.GetWorkerId())
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result, err := operationToProto(operation)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.RunOperationResponse{Operation: result}, nil
}

func (s *OperationServer) ReconcileOperation(ctx context.Context, req *aeropb.ReconcileOperationRequest) (*aeropb.ReconcileOperationResponse, error) {
	id, err := parseID(req.GetOperationId(), "operation_id")
	if err != nil {
		return nil, err
	}
	operation, err := s.operations.Reconcile(ctx, id, req.GetWorkerId())
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	result, err := operationToProto(operation)
	if err != nil {
		return nil, apperr.GRPCError(err)
	}
	return &aeropb.ReconcileOperationResponse{Operation: result}, nil
}

func operationToProto(operation *domain.Operation) (*aeropb.Operation, error) {
	if operation == nil {
		return nil, nil
	}
	request, err := structFromMap(operation.RequestPayload)
	if err != nil {
		return nil, apperr.Internal("operation.payload_invalid", "operation payload is invalid").Wrap(err)
	}
	result, err := structFromMap(operation.Result)
	if err != nil {
		return nil, apperr.Internal("operation.result_invalid", "operation result is invalid").Wrap(err)
	}
	converted := &aeropb.Operation{OperationId: operation.ID.String(), ActorUid: operation.ActorUID,
		TenantId: operation.TenantID, RequestId: operation.RequestID,
		HomeRegion: operation.HomeRegion, OperationType: operation.OperationType, Status: string(operation.Status), IdempotencyKey: operation.IdempotencyKey,
		RequestPayload: request, Result: result, ErrorCode: operation.ErrorCode, ErrorMessage: operation.ErrorMessage,
		CreatedAt: timestamppb.New(operation.CreatedAt), UpdatedAt: timestamppb.New(operation.UpdatedAt), CompletedAt: timestampOf(operation.CompletedAt)}
	if operation.AccountID != (domain.ID{}) {
		converted.AccountId = operation.AccountID.String()
	}
	converted.Steps = make([]*aeropb.OperationStep, 0, len(operation.Steps))
	for _, step := range operation.Steps {
		item, err := operationStepToProto(step)
		if err != nil {
			return nil, err
		}
		converted.Steps = append(converted.Steps, item)
	}
	return converted, nil
}

func operationStepToProto(step *domain.OperationStep) (*aeropb.OperationStep, error) {
	request, err := structFromMap(step.RequestPayload)
	if err != nil {
		return nil, err
	}
	result, err := structFromMap(step.Result)
	if err != nil {
		return nil, err
	}
	compensationResult, err := structFromMap(step.CompensationResult)
	if err != nil {
		return nil, err
	}
	return &aeropb.OperationStep{Id: step.ID.String(), StepKey: step.StepKey, Source: step.Source, Action: step.Action,
		Status: string(step.Status), Attempt: protoCount(step.Attempt), LeaseUntil: timestampOf(step.LeaseUntil), SourceOperationId: step.SourceOperationID,
		RequestPayload: request, Result: result, ErrorCode: step.ErrorCode, ErrorMessage: step.ErrorMessage, CompensationAction: step.CompensationAction,
		CompensationStatus: string(step.CompensationStatus), CompensationAttempt: protoCount(step.CompensationAttempt),
		CompensationLeaseUntil: timestampOf(step.CompensationLeaseUntil), CompensationSourceOperationId: step.CompensationSourceOperationID,
		CompensationResult: compensationResult, CompensationErrorCode: step.CompensationErrorCode,
		CompensationErrorMessage: step.CompensationErrorMessage}, nil
}

func parseOptionalOperationID(value, field string) (domain.ID, error) {
	if value == "" {
		return domain.ID{}, nil
	}
	id, err := parseID(value, field)
	if err != nil {
		return domain.ID{}, apperr.GRPCError(apperr.BadRequest("operation.invalid_id", "invalid "+field))
	}
	return id, nil
}
