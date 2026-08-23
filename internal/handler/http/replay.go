package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/middleware"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

type ReplayHandler struct {
	replays    service.ReplayService
	projection service.ReplayProjectionService // optional; nil disables the projection replay route
	permission middleware.PermissionChecker
}

func NewReplayHandler(replays service.ReplayService, permission middleware.PermissionChecker) *ReplayHandler {
	return &ReplayHandler{replays: replays, permission: permission}
}

// NewReplayHandlerWithProjection additionally wires the shadow projection
// replay route (P3-07).
func NewReplayHandlerWithProjection(replays service.ReplayService,
	projection service.ReplayProjectionService, permission middleware.PermissionChecker) *ReplayHandler {
	return &ReplayHandler{replays: replays, projection: projection, permission: permission}
}

func (h *ReplayHandler) RegisterRoutes(r Routes) {
	group := r.Auth.Group("/audit/replays")
	if h.permission != nil {
		group.Use(middleware.RequirePermission(domain.PermAuditReplay, h.permission))
	}
	group.POST("", h.Create)
	group.GET("/:id", h.Get)
	if h.projection != nil {
		// P3-07: rebuild a shadow projection from the event store and compare
		// with the live projection. Read-only; produces no side effects.
		group.POST("/projection", h.ReplayProjection)
	}
}

type replayRequest struct {
	Mode           string                 `json:"mode"`
	AccountID      string                 `json:"account_id"`
	OperationID    string                 `json:"operation_id"`
	From           string                 `json:"from"`
	To             string                 `json:"to"`
	Cursor         string                 `json:"cursor"`
	Limit          int                    `json:"limit"`
	IdempotencyKey string                 `json:"idempotency_key"`
	Confirmed      bool                   `json:"confirmed"`
	Commands       []replayCommandRequest `json:"commands"`
}

type replayCommandRequest struct {
	Source  string         `json:"source"`
	Action  string         `json:"action"`
	Payload map[string]any `json:"payload"`
}

func (h *ReplayHandler) Create(c *gin.Context) {
	var body replayRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, err)
		return
	}
	accountID, err := parseOptionalID(body.AccountID, "account_id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	operationID, err := parseOptionalID(body.OperationID, "operation_id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	from, err := parseOptionalTime(body.From, "from")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	to, err := parseOptionalTime(body.To, "to")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	key := body.IdempotencyKey
	if key == "" {
		key = c.GetHeader("Idempotency-Key")
	}
	commands := make([]service.ReplayCommand, len(body.Commands))
	for i, command := range body.Commands {
		commands[i] = service.ReplayCommand{Source: command.Source, Action: command.Action, Payload: command.Payload}
	}
	result, err := h.replays.Create(c.Request.Context(), service.ReplayInput{Mode: body.Mode, AccountID: accountID,
		OperationID: operationID, From: from, To: to, Cursor: body.Cursor, Limit: body.Limit,
		IdempotencyKey: key, Confirmed: body.Confirmed, Commands: commands})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, replayResponse(result))
}

type projectionReplayRequest struct {
	AccountID string `json:"account_id" binding:"required"`
	Source    string `json:"source" binding:"required"`
	Cursor    string `json:"cursor"`
	Limit     int    `json:"limit"`
}

func (h *ReplayHandler) ReplayProjection(c *gin.Context) {
	var body projectionReplayRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, apperr.BadRequest("replay.invalid_input", "account_id and source are required"))
		return
	}
	accountID, err := parseIDString(body.AccountID, "account_id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	cursor, err := parseOptionalID(body.Cursor, "cursor")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	result, err := h.projection.ReplayProjection(c.Request.Context(), service.ReplayProjectionInput{
		AccountID: accountID, Source: body.Source, Cursor: cursor, Limit: body.Limit,
	})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, result)
}

func (h *ReplayHandler) Get(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	operation, err := h.replays.Get(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"replay_id": operation.ID.String(), "mode": "controlled", "status": operation.Status, "operation": operation})
}

func replayResponse(result *service.ReplayResult) gin.H {
	events := make([]auditFactResponse, len(result.Facts))
	for i, fact := range result.Facts {
		events[i] = auditFactResponseOf(fact)
	}
	response := gin.H{"replay_id": result.ReplayID.String(), "mode": result.Mode, "status": result.Status,
		"next_cursor": result.NextCursor, "events": events}
	if result.Simulation != nil {
		response["simulation"] = result.Simulation
	}
	if result.Operation != nil {
		response["operation"] = result.Operation
	}
	return response
}

func parseOptionalTime(value, field string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, apperr.BadRequest("replay.invalid_time", "invalid "+field)
	}
	return &parsed, nil
}
