package http

import (
	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

type OperationHandler struct{ operations service.OperationService }

func NewOperationHandler(operations service.OperationService) *OperationHandler {
	return &OperationHandler{operations: operations}
}

func (h *OperationHandler) RegisterRoutes(r Routes) {
	r.Auth.POST("/operations", h.Create)
	r.Auth.GET("/operations", h.List)
	r.Auth.GET("/operations/:id", h.Get)
	r.Auth.POST("/operations/:id/run", h.Run)
	r.Auth.POST("/operations/:id/reconcile", h.Reconcile)
}

type operationRequest struct {
	AccountID      string                 `json:"account_id"`
	HomeRegion     string                 `json:"home_region"`
	OperationType  string                 `json:"operation_type"`
	IdempotencyKey string                 `json:"idempotency_key"`
	RequestPayload map[string]any         `json:"request_payload"`
	Steps          []operationStepRequest `json:"steps"`
}

type operationStepRequest struct {
	StepKey            string         `json:"step_key"`
	Source             string         `json:"source"`
	Action             string         `json:"action"`
	RequestPayload     map[string]any `json:"request_payload"`
	CompensationAction string         `json:"compensation_action"`
}

func (h *OperationHandler) Create(c *gin.Context) {
	var body operationRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("operation.invalid_request", "invalid request body"))
		return
	}
	accountID, err := parseOptionalID(body.AccountID, "account_id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	key := body.IdempotencyKey
	if key == "" {
		key = c.GetHeader("Idempotency-Key")
	}
	steps := make([]service.OperationStepInput, len(body.Steps))
	for i, step := range body.Steps {
		steps[i] = service.OperationStepInput{StepKey: step.StepKey, Source: step.Source, Action: step.Action,
			RequestPayload: step.RequestPayload, CompensationAction: step.CompensationAction}
	}
	operation, err := h.operations.Create(c.Request.Context(), service.OperationInput{AccountID: accountID, HomeRegion: body.HomeRegion,
		OperationType: body.OperationType, IdempotencyKey: key, RequestPayload: body.RequestPayload, Steps: steps})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, operationResponseOf(operation))
}

func (h *OperationHandler) List(c *gin.Context) {
	items, next, err := h.operations.List(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	result := make([]operationResponse, len(items))
	for i, item := range items {
		result[i] = operationResponseOf(item)
	}
	httpx.OK(c, gin.H{"operations": result, "next_cursor": next})
}

func (h *OperationHandler) Get(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	operation, err := h.operations.Get(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, operationResponseOf(operation))
}

func (h *OperationHandler) Run(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	operation, err := h.operations.Run(c.Request.Context(), id, c.GetHeader("X-Worker-ID"))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, operationResponseOf(operation))
}

func (h *OperationHandler) Reconcile(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	operation, err := h.operations.Reconcile(c.Request.Context(), id, c.GetHeader("X-Worker-ID"))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, operationResponseOf(operation))
}

type operationResponse struct {
	ID             string                  `json:"operation_id"`
	AccountID      string                  `json:"account_id,omitempty"`
	ActorUID       string                  `json:"actor_uid"`
	TenantID       string                  `json:"tenant_id,omitempty"`
	RequestID      string                  `json:"request_id,omitempty"`
	HomeRegion     string                  `json:"home_region"`
	OperationType  string                  `json:"operation_type"`
	Status         string                  `json:"status"`
	IdempotencyKey string                  `json:"idempotency_key"`
	RequestPayload map[string]any          `json:"request_payload"`
	Result         map[string]any          `json:"result"`
	ErrorCode      string                  `json:"error_code,omitempty"`
	ErrorMessage   string                  `json:"error_message,omitempty"`
	CreatedAt      any                     `json:"created_at"`
	UpdatedAt      any                     `json:"updated_at"`
	CompletedAt    any                     `json:"completed_at,omitempty"`
	Steps          []operationStepResponse `json:"steps"`
}

type operationStepResponse struct {
	ID                            string         `json:"id"`
	StepKey                       string         `json:"step_key"`
	Source                        string         `json:"source"`
	Action                        string         `json:"action"`
	Status                        string         `json:"status"`
	Attempt                       int            `json:"attempt"`
	LeaseUntil                    any            `json:"lease_until,omitempty"`
	SourceOperationID             string         `json:"source_operation_id,omitempty"`
	Result                        map[string]any `json:"result"`
	ErrorCode                     string         `json:"error_code,omitempty"`
	ErrorMessage                  string         `json:"error_message,omitempty"`
	CompensationAction            string         `json:"compensation_action,omitempty"`
	CompensationStatus            string         `json:"compensation_status,omitempty"`
	CompensationAttempt           int            `json:"compensation_attempt,omitempty"`
	CompensationLeaseUntil        any            `json:"compensation_lease_until,omitempty"`
	CompensationSourceOperationID string         `json:"compensation_source_operation_id,omitempty"`
	CompensationResult            map[string]any `json:"compensation_result,omitempty"`
	CompensationErrorCode         string         `json:"compensation_error_code,omitempty"`
	CompensationErrorMessage      string         `json:"compensation_error_message,omitempty"`
}

func operationResponseOf(operation *domain.Operation) operationResponse {
	result := operationResponse{ID: operation.ID.String(), ActorUID: operation.ActorUID, TenantID: operation.TenantID, RequestID: operation.RequestID,
		HomeRegion: operation.HomeRegion, OperationType: operation.OperationType, Status: string(operation.Status),
		IdempotencyKey: operation.IdempotencyKey, RequestPayload: operation.RequestPayload, Result: operation.Result,
		ErrorCode: operation.ErrorCode, ErrorMessage: operation.ErrorMessage, CreatedAt: operation.CreatedAt, UpdatedAt: operation.UpdatedAt,
		CompletedAt: operation.CompletedAt, Steps: make([]operationStepResponse, len(operation.Steps))}
	if operation.AccountID != (domain.ID{}) {
		result.AccountID = operation.AccountID.String()
	}
	for i, step := range operation.Steps {
		result.Steps[i] = operationStepResponse{ID: step.ID.String(), StepKey: step.StepKey, Source: step.Source,
			Action: step.Action, Status: string(step.Status), Attempt: step.Attempt, LeaseUntil: step.LeaseUntil,
			SourceOperationID: step.SourceOperationID, Result: step.Result, ErrorCode: step.ErrorCode, ErrorMessage: step.ErrorMessage,
			CompensationAction: step.CompensationAction, CompensationStatus: string(step.CompensationStatus),
			CompensationAttempt: step.CompensationAttempt, CompensationLeaseUntil: step.CompensationLeaseUntil,
			CompensationSourceOperationID: step.CompensationSourceOperationID, CompensationResult: step.CompensationResult,
			CompensationErrorCode: step.CompensationErrorCode, CompensationErrorMessage: step.CompensationErrorMessage}
	}
	return result
}

func parseOptionalID(value, field string) (domain.ID, error) {
	if value == "" {
		return domain.ID{}, nil
	}
	return parseIDString(value, field)
}

func parseIDString(value, field string) (domain.ID, error) {
	id, err := idgen.Parse(value)
	if err != nil {
		return domain.ID{}, apperr.BadRequest("request.invalid_id", "invalid "+field)
	}
	return id, nil
}
