package http

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/middleware"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

type CallbackHandler struct {
	callbacks  service.CallbackService
	permission middleware.PermissionChecker
}

func NewCallbackHandler(callbacks service.CallbackService, permission middleware.PermissionChecker) *CallbackHandler {
	return &CallbackHandler{callbacks: callbacks, permission: permission}
}

func (h *CallbackHandler) RegisterRoutes(r Routes) {
	group := r.Auth.Group("/admin/callback-subscriptions")
	if h.permission != nil {
		group.Use(middleware.RequirePermission(domain.PermCallbackManage, h.permission))
	}
	group.GET("", h.List)
	group.GET("/:id/deliveries", h.ListDeliveries)
	group.GET("/:id/deliveries/:delivery_id", h.GetDelivery)
	group.POST("", h.Create)
	group.DELETE("/:id", h.Delete)
	group.POST("/deliveries/:id/replay", h.Replay)
}

func (h *CallbackHandler) List(c *gin.Context) {
	items, next, err := h.callbacks.List(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	result := make([]callbackSubscriptionResponse, len(items))
	for i, item := range items {
		result[i] = callbackSubscriptionResponseOf(item)
	}
	httpx.OK(c, gin.H{"subscriptions": result, "next_cursor": next})
}

type callbackCreateRequest struct {
	Endpoint   string   `json:"endpoint"`
	TenantID   string   `json:"tenant_id"`
	EventTypes []string `json:"event_types"`
}

func (h *CallbackHandler) Create(c *gin.Context) {
	var body callbackCreateRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.WriteError(c, err)
		return
	}
	input := service.CallbackInput{Endpoint: body.Endpoint, TenantID: body.TenantID, EventTypes: body.EventTypes}
	item, secret, err := h.callbacks.Register(c.Request.Context(), input)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	subscription := callbackSubscriptionResponseOf(item)
	safeBody, err := json.Marshal(callbackCreateReplayEnvelope{Data: callbackCreateReplayData{Subscription: subscription}})
	if err != nil {
		// Registration has already committed a durable callback; retaining the
		// idempotency reservation is safer than returning a secret-bearing body
		// that cannot be replayed without duplication.
		panic(err)
	}
	middleware.RequireIdempotencyReplayOnSuccess(c)
	if err := middleware.SetIdempotencyReplayBody(c, safeBody); err != nil {
		panic(err)
	}
	c.Header("Cache-Control", "no-store")
	httpx.Created(c, gin.H{"subscription": subscription, "secret": string(secret)})
}

func (h *CallbackHandler) Delete(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.callbacks.Delete(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *CallbackHandler) Replay(c *gin.Context) {
	// A replay changes durable delivery state and appends an audit fact. The
	// response is therefore not safe to lose and retry without a stable command
	// identity: ReplayDelivery intentionally mints a fresh audit event for each
	// user intent. Require the HTTP key at this boundary so a transport retry
	// replays the stored response instead of creating a second intent.
	key := c.GetHeader("Idempotency-Key")
	if strings.TrimSpace(key) == "" {
		httpx.WriteError(c, apperr.BadRequest("callback.replay_idempotency_key_required",
			"Idempotency-Key is required when replaying a callback delivery"))
		return
	}
	if len(key) > 200 {
		httpx.WriteError(c, apperr.BadRequest("callback.replay_idempotency_key_invalid",
			"Idempotency-Key is too long"))
		return
	}
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.callbacks.ReplayDelivery(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"status": "queued"})
}

type callbackCreateReplayEnvelope struct {
	Data callbackCreateReplayData `json:"data"`
}

type callbackCreateReplayData struct {
	Subscription callbackSubscriptionResponse `json:"subscription"`
}

type callbackSubscriptionResponse struct {
	ID         string   `json:"id"`
	OwnerUID   string   `json:"owner_uid"`
	TenantID   string   `json:"tenant_id,omitempty"`
	Endpoint   string   `json:"endpoint"`
	EventTypes []string `json:"event_types"`
	Enabled    bool     `json:"enabled"`
	Verified   bool     `json:"verified"`
}

func callbackSubscriptionResponseOf(item *domain.CallbackSubscription) callbackSubscriptionResponse {
	return callbackSubscriptionResponse{ID: item.ID.String(), OwnerUID: item.OwnerUID, TenantID: item.TenantID,
		Endpoint: item.Endpoint, EventTypes: item.EventTypes, Enabled: item.Enabled, Verified: item.Verified}
}
