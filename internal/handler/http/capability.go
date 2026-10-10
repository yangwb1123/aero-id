package http

import (
	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

type CapabilityHandler struct{ capabilities service.CapabilityService }

func NewCapabilityHandler(capabilities service.CapabilityService) *CapabilityHandler {
	return &CapabilityHandler{capabilities: capabilities}
}

func (h *CapabilityHandler) RegisterRoutes(r Routes) {
	r.ControlPlane.GET("/me/capabilities", h.GetMe)
}

func (h *CapabilityHandler) GetMe(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	report, err := h.capabilities.GetMeCapabilities(c.Request.Context())
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, report)
}
