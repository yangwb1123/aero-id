package http

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
)

const maxAgentMembershipRequestBytes = 1024

type AgentMembershipHandler struct {
	service service.AgentMembershipCheckService
}

func NewAgentMembershipHandler(checker service.AgentMembershipCheckService) *AgentMembershipHandler {
	return &AgentMembershipHandler{service: checker}
}

func (h *AgentMembershipHandler) RegisterRoutes(routes Routes) {
	if h == nil || h.service == nil {
		return
	}
	routes.InternalControlPlane.POST("/internal/v1/agent/membership-check", h.Check)
}

func (h *AgentMembershipHandler) Check(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxAgentMembershipRequestBytes+1))
	if err != nil || len(body) > maxAgentMembershipRequestBytes {
		httpx.WriteError(c, apperr.BadRequest("agent_membership.invalid_request", "invalid membership check request"))
		return
	}
	accountID, err := decodeAgentMembershipRequest(body)
	if err != nil {
		httpx.WriteError(c, apperr.BadRequest("agent_membership.invalid_request", "invalid membership check request"))
		return
	}
	result, err := h.service.Check(c.Request.Context(), accountID)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, result)
}

func decodeAgentMembershipRequest(raw []byte) (domain.ID, error) {
	rawID, err := readAgentMembershipAccountID(raw)
	if err != nil {
		return domain.ID{}, err
	}
	id, err := idgen.Parse(rawID)
	if err != nil {
		return domain.ID{}, err
	}
	return id, nil
}

func readAgentMembershipAccountID(raw []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if !takeJSONToken(decoder, json.Delim('{')) || !decoder.More() {
		return "", io.ErrUnexpectedEOF
	}
	if !takeJSONField(decoder, "account_id") {
		return "", io.ErrUnexpectedEOF
	}
	accountID, err := decodeJSONText(decoder)
	if err != nil || decoder.More() || !takeJSONToken(decoder, json.Delim('}')) || !jsonAtEOF(decoder) {
		return "", io.ErrUnexpectedEOF
	}
	return accountID, nil
}

func takeJSONToken(decoder *json.Decoder, expected json.Delim) bool {
	token, err := decoder.Token()
	return err == nil && token == expected
}

func takeJSONField(decoder *json.Decoder, expected string) bool {
	token, err := decoder.Token()
	field, ok := token.(string)
	return err == nil && ok && field == expected
}

func decodeJSONText(decoder *json.Decoder) (string, error) {
	var value string
	err := decoder.Decode(&value)
	return value, err
}

func jsonAtEOF(decoder *json.Decoder) bool {
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
}
