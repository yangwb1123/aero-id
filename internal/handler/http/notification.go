package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

type NotificationHandler struct {
	notifs service.NotificationService
}

func NewNotificationHandler(notifs service.NotificationService) *NotificationHandler {
	return &NotificationHandler{notifs: notifs}
}

func (h *NotificationHandler) RegisterRoutes(r Routes) {
	r.Auth.GET("/notifications", h.List)
	// Static segments must precede the /:id param route so gin doesn't treat
	// "unread_count" / "read-all" as an id.
	r.Auth.GET("/notifications/unread_count", h.UnreadCount)
	r.Auth.POST("/notifications/read-all", h.MarkAllRead)
	r.Auth.POST("/notifications/:id/read", h.MarkRead)
}

func (h *NotificationHandler) List(c *gin.Context) {
	list, err := h.notifs.List(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	items := make([]notificationResponse, 0, len(list.Notifications))
	for _, n := range list.Notifications {
		items = append(items, toNotificationResponse(n))
	}
	httpx.OK(c, gin.H{
		"notifications": items,
		"users":         toUserMap(list.Users),
		"next_cursor":   list.NextCursor,
	})
}

func (h *NotificationHandler) UnreadCount(c *gin.Context) {
	n, err := h.notifs.UnreadCount(c.Request.Context())
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"unread_count": n})
}

func (h *NotificationHandler) MarkRead(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.notifs.MarkRead(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *NotificationHandler) MarkAllRead(c *gin.Context) {
	if err := h.notifs.MarkAllRead(c.Request.Context()); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

type notificationResponse struct {
	ID           string     `json:"id"`
	RecipientUID string     `json:"recipient_uid"`
	Type         string     `json:"type"`
	ActorUID     string     `json:"actor_uid,omitempty"`
	SubjectType  string     `json:"subject_type,omitempty"`
	SubjectID    string     `json:"subject_id,omitempty"`
	ReadAt       *time.Time `json:"read_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

func toNotificationResponse(n *domain.Notification) notificationResponse {
	return notificationResponse{
		ID:           n.ID.String(),
		RecipientUID: n.RecipientUID,
		Type:         n.Type,
		ActorUID:     n.ActorUID,
		SubjectType:  n.SubjectType,
		SubjectID:    n.SubjectID,
		ReadAt:       n.ReadAt,
		CreatedAt:    n.CreatedAt,
	}
}
