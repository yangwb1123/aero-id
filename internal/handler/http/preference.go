package http

import (
	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

// PreferenceHandler exposes GET/PUT /v1/notification-preferences.
type PreferenceHandler struct {
	svc service.PreferenceService
}

func NewPreferenceHandler(svc service.PreferenceService) *PreferenceHandler {
	return &PreferenceHandler{svc: svc}
}

func (h *PreferenceHandler) RegisterRoutes(r Routes) {
	r.Auth.GET("/notification-preferences", h.Get)
	r.Auth.PUT("/notification-preferences", h.Set)
}

func (h *PreferenceHandler) Get(c *gin.Context) {
	prefs, err := h.svc.List(c.Request.Context())
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if prefs == nil {
		prefs = []*domain.NotificationPreference{}
	}
	httpx.OK(c, gin.H{"preferences": prefs})
}

// setPreferenceRequest mirrors the client-facing PUT body.
type setPreferenceRequest struct {
	Preferences []*domain.NotificationPreference `json:"preferences"`
}

func (h *PreferenceHandler) Set(c *gin.Context) {
	var req setPreferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("preference.invalid", "invalid request body"))
		return
	}
	if len(req.Preferences) == 0 {
		httpx.OK(c, gin.H{"status": "ok"})
		return
	}
	if err := h.svc.Set(c.Request.Context(), req.Preferences); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"status": "ok"})
}
