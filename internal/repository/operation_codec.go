package repository

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/datatypes"
)

func jsonMap(values map[string]any) (datatypes.JSON, error) {
	if values == nil {
		values = map[string]any{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("marshal operation json: %w", err)
	}
	return datatypes.JSON(data), nil
}

func operationToRow(operation *domain.Operation) (*operationRow, error) {
	request, err := jsonMap(operation.RequestPayload)
	if err != nil {
		return nil, err
	}
	result, err := jsonMap(operation.Result)
	if err != nil {
		return nil, err
	}
	return &operationRow{ID: operation.ID, AccountID: nullableUUID(operation.AccountID), ActorUID: operation.ActorUID,
		TenantID: operation.TenantID, RequestID: operation.RequestID, HomeRegion: operation.HomeRegion, OperationType: operation.OperationType,
		Status: string(operation.Status), IdempotencyKey: operation.IdempotencyKey, RequestPayload: request,
		Result: result, ErrorCode: operation.ErrorCode, ErrorMessage: operation.ErrorMessage, CreatedAt: operation.CreatedAt,
		UpdatedAt: operation.UpdatedAt, CompletedAt: operation.CompletedAt}, nil
}

func operationRowToDomain(row *operationRow) (*domain.Operation, error) {
	request, err := decodeMap(row.RequestPayload)
	if err != nil {
		return nil, err
	}
	result, err := decodeMap(row.Result)
	if err != nil {
		return nil, err
	}
	return &domain.Operation{ID: row.ID, AccountID: idFromNullableUUID(row.AccountID), ActorUID: row.ActorUID,
		TenantID: row.TenantID, RequestID: row.RequestID, HomeRegion: row.HomeRegion,
		OperationType: row.OperationType, Status: domain.OperationStatus(row.Status),
		IdempotencyKey: row.IdempotencyKey, RequestPayload: request, Result: result, ErrorCode: row.ErrorCode,
		ErrorMessage: row.ErrorMessage, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CompletedAt: row.CompletedAt}, nil
}

func operationStepToRow(step *domain.OperationStep) (*operationStepRow, error) {
	request, err := jsonMap(step.RequestPayload)
	if err != nil {
		return nil, err
	}
	result, err := jsonMap(step.Result)
	if err != nil {
		return nil, err
	}
	compensationResult, err := jsonMap(step.CompensationResult)
	if err != nil {
		return nil, err
	}
	compensationStatus := step.CompensationStatus
	if step.CompensationAction != "" && compensationStatus == "" {
		compensationStatus = domain.StepPending
	}
	leaseKind := step.LeaseKind
	if leaseKind == "" && step.LeaseOwner != "" {
		leaseKind = "forward"
	}
	return &operationStepRow{ID: step.ID, OperationID: step.OperationID, StepKey: step.StepKey, Source: step.Source,
		Action: step.Action, Status: string(step.Status), Attempt: step.Attempt, LeaseOwner: step.LeaseOwner,
		LeaseUntil: step.LeaseUntil, LeaseKind: leaseKind, SourceOperationID: step.SourceOperationID, RequestPayload: request, Result: result,
		ErrorCode: step.ErrorCode, ErrorMessage: step.ErrorMessage, CompensationAction: step.CompensationAction,
		CompensationStatus: string(compensationStatus), CompensationAttempt: step.CompensationAttempt,
		CompensationLeaseOwner: step.CompensationLeaseOwner, CompensationLeaseUntil: step.CompensationLeaseUntil,
		CompensationSourceOperationID: step.CompensationSourceOperationID, CompensationResult: compensationResult,
		CompensationErrorCode: step.CompensationErrorCode, CompensationErrorMessage: step.CompensationErrorMessage,
		CompensationUpdatedAt: step.CompensationUpdatedAt,
		CreatedAt:             step.CreatedAt, UpdatedAt: step.UpdatedAt}, nil
}

func operationStepRowToDomain(row *operationStepRow) (*domain.OperationStep, error) {
	request, err := decodeMap(row.RequestPayload)
	if err != nil {
		return nil, err
	}
	result, err := decodeMap(row.Result)
	if err != nil {
		return nil, err
	}
	compensationResult, err := decodeMap(row.CompensationResult)
	if err != nil {
		return nil, err
	}
	return &domain.OperationStep{ID: row.ID, OperationID: row.OperationID, StepKey: row.StepKey, Source: row.Source,
		Action: row.Action, Status: domain.OperationStepStatus(row.Status), Attempt: row.Attempt, LeaseOwner: row.LeaseOwner,
		LeaseUntil: row.LeaseUntil, LeaseKind: row.LeaseKind, SourceOperationID: row.SourceOperationID, RequestPayload: request, Result: result,
		ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage, CompensationAction: row.CompensationAction,
		CompensationStatus: domain.OperationStepStatus(row.CompensationStatus), CompensationAttempt: row.CompensationAttempt,
		CompensationLeaseOwner: row.CompensationLeaseOwner, CompensationLeaseUntil: row.CompensationLeaseUntil,
		CompensationSourceOperationID: row.CompensationSourceOperationID, CompensationResult: compensationResult,
		CompensationErrorCode: row.CompensationErrorCode, CompensationErrorMessage: row.CompensationErrorMessage,
		CompensationUpdatedAt: row.CompensationUpdatedAt,
		CreatedAt:             row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

func decodeMap(data []byte) (map[string]any, error) {
	result := map[string]any{}
	if len(data) == 0 {
		return result, nil
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode operation json: %w", err)
	}
	return result, nil
}

func nullableUUID(value domain.ID) *uuid.UUID {
	if value == (domain.ID{}) {
		return nil
	}
	result := uuid.UUID(value)
	return &result
}

func idFromNullableUUID(value *uuid.UUID) domain.ID {
	if value == nil {
		return domain.ID{}
	}
	return *value
}
