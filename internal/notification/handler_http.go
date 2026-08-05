package notification

import (
	"github.com/gin-gonic/gin"

	httphandler "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/sso"
)

type NotificationHandler struct {
	notifs Service
}

func NewNotificationHandler(notifs Service) *NotificationHandler {
	return &NotificationHandler{notifs: notifs}
}

func (h *NotificationHandler) RegisterRoutes(r httphandler.Routes) {
	r.Auth.GET("/notifications", h.List)
	r.Auth.GET("/notifications/unread_count", h.UnreadCount)
	r.Auth.POST("/notifications/:id/read", h.MarkRead)
	r.Auth.POST("/notifications/read-all", h.MarkAllRead)
}

func (h *NotificationHandler) List(c *gin.Context) {
	list, err := h.notifs.List(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"notifications": list.Notifications, "users": toUserMap(list.Users), "next_cursor": list.NextCursor})
}

type userResponse struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar"`
}

func toUserMap(users map[string]sso.User) map[string]userResponse {
	out := make(map[string]userResponse, len(users))
	for uid, u := range users {
		out[uid] = userResponse{UID: u.UID, Nickname: u.Nickname, Avatar: u.Avatar}
	}
	return out
}

func (h *NotificationHandler) MarkRead(c *gin.Context) {
	id, err := idgen.Parse(c.Param("id"))
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

func (h *NotificationHandler) UnreadCount(c *gin.Context) {
	n, err := h.notifs.UnreadCount(c.Request.Context())
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"unread_count": n})
}
