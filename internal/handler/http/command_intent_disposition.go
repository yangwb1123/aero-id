package http

import (
	"crypto/sha256"
	"fmt"

	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

func (h *CommandIntentHandler) FindByKey(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	intent, err := h.intents.FindByKey(c.Request.Context(), c.GetHeader("Idempotency-Key"))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, struct {
		Intent    commandIntentResponse `json:"intent"`
		KeyDigest string                `json:"key_digest"`
	}{commandIntentResponseOf(intent), fmt.Sprintf("v1:%x", sha256.Sum256([]byte(intent.IdempotencyKey)))})
}

func (h *CommandIntentHandler) Abandon(c *gin.Context) {
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
	intent, err := h.intents.Abandon(c.Request.Context(), id, body.Version, body.ConfirmationDigest)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, commandIntentResponseOf(intent))
}
