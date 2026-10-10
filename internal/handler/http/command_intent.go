package http

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
	"github.com/gin-gonic/gin"
)

type CommandIntentHandler struct{ intents service.CommandIntentService }

func NewCommandIntentHandler(intents service.CommandIntentService) *CommandIntentHandler {
	return &CommandIntentHandler{intents: intents}
}

func (h *CommandIntentHandler) RegisterRoutes(r Routes) {
	r.ControlPlane.POST("/command-intents", h.Create)
	r.ControlPlane.GET("/command-intents/:id", h.Get)
	r.ControlPlane.POST("/command-intents/:id/execute", h.Execute)
}

type commandIntentRequest struct {
	AccountID string             `json:"account_id"`
	Plan      domain.CommandPlan `json:"plan"`
}

type commandIntentResponse struct {
	*domain.CommandIntent
	TenantID  string     `json:"tenant_id"`
	ClientID  string     `json:"client_id"`
	Issuer    string     `json:"issuer"`
	ReceiptID *domain.ID `json:"receipt_id,omitempty"`
}

func commandIntentResponseOf(intent *domain.CommandIntent) commandIntentResponse {
	result := commandIntentResponse{CommandIntent: intent, TenantID: intent.Principal.TenantID,
		ClientID: intent.Principal.ClientID, Issuer: intent.Principal.Issuer}
	if intent.Status == "executed" {
		result.ReceiptID = &intent.ReceiptID
	}
	return result
}

func (h *CommandIntentHandler) Create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var body commandIntentRequest
	if !decodeCommandIntent(c, &body) {
		return
	}
	accountID, err := parseOptionalID(body.AccountID, "account_id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	intent, err := h.intents.Create(c.Request.Context(), service.CommandIntentInput{AccountID: accountID,
		IdempotencyKey: c.GetHeader("Idempotency-Key"), Plan: body.Plan})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, commandIntentResponseOf(intent))
}

func (h *CommandIntentHandler) Get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	intent, err := h.intents.Get(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, commandIntentResponseOf(intent))
}

func (h *CommandIntentHandler) Execute(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	var body struct {
		Version            int64  `json:"version"`
		ConfirmationDigest string `json:"confirmation_digest"`
	}
	if !decodeCommandIntent(c, &body) {
		return
	}
	intent, err := h.intents.Execute(c.Request.Context(), id, body.Version, body.ConfirmationDigest)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, commandIntentResponseOf(intent))
}

func decodeCommandIntent(c *gin.Context, output any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(output); err == nil {
		var extra any
		if err := decoder.Decode(&extra); err == io.EOF {
			return true
		}
	}
	httpx.WriteError(c, apperr.BadRequest("command.invalid_request", "invalid or oversized command request"))
	return false
}
