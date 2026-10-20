package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/httpx"
)

func (h *CallbackHandler) ListDeliveries(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	items, next, err := h.callbacks.ListDeliveries(c.Request.Context(), id,
		domain.CallbackDeliveryFilter{Status: c.Query("status"), Page: httpx.PageFromQuery(c)})
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	result := make([]callbackDeliveryResponse, len(items))
	for i, item := range items {
		result[i] = callbackDeliveryResponseOf(item)
	}
	httpx.OK(c, gin.H{"deliveries": result, "next_cursor": next})
}

func (h *CallbackHandler) GetDelivery(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	subscriptionID, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	deliveryID, err := httpx.ParseIDFromParam(c, "delivery_id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	item, err := h.callbacks.GetDeliveryMetadata(c.Request.Context(), subscriptionID, deliveryID)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"delivery": callbackDeliveryResponseOf(item)})
}

type callbackDeliveryResponse struct {
	ID              string     `json:"id"`
	SubscriptionID  string     `json:"subscription_id"`
	EventID         string     `json:"event_id"`
	TenantID        string     `json:"tenant_id"`
	Action          string     `json:"action"`
	Status          string     `json:"status"`
	Attempt         int32      `json:"attempt"`
	Revision        int64      `json:"revision"`
	LeaseGeneration int64      `json:"lease_generation"`
	LeaseUntil      *time.Time `json:"lease_until,omitempty"`
	NextAttemptAt   time.Time  `json:"next_attempt_at"`
	StatusCode      int32      `json:"status_code"`
	ErrorCode       string     `json:"error_code"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func callbackDeliveryResponseOf(item *domain.CallbackDeliveryMetadata) callbackDeliveryResponse {
	return callbackDeliveryResponse{ID: item.ID.String(), SubscriptionID: item.SubscriptionID.String(),
		EventID: item.EventID.String(), TenantID: item.TenantID, Action: item.Action, Status: item.Status, Attempt: item.Attempt,
		Revision: item.Revision, LeaseGeneration: item.LeaseGeneration, LeaseUntil: item.LeaseUntil, NextAttemptAt: item.NextAttemptAt,
		StatusCode: item.StatusCode, ErrorCode: item.ErrorCode, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
}
