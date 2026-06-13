package http

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/sso"
)

type MessageHandler struct {
	messages service.MessageService
}

func NewMessageHandler(messages service.MessageService) *MessageHandler {
	return &MessageHandler{messages: messages}
}

func (h *MessageHandler) RegisterRoutes(r Routes) {
	r.Auth.POST("/messages", h.Send)
	r.Auth.GET("/messages", h.Inbox)
	r.Auth.GET("/messages/unread_count", h.UnreadCount)
	r.Auth.GET("/messages/:id", h.Get)
	r.Auth.PATCH("/messages/:id", h.Edit)
	r.Auth.DELETE("/messages/:id", h.Delete)
	r.Auth.POST("/messages/:id/read", h.MarkRead)
	r.Auth.POST("/messages/:id/reactions", h.ReactToMessage)
	r.Auth.DELETE("/messages/:id/reactions", h.UnreactMessage)
	r.Auth.GET("/conversations/:uid", h.Conversation)
	r.Auth.POST("/conversations/:uid/read", h.MarkConversationRead)
}

type sendMessageRequest struct {
	ToUID   string         `json:"to_uid"`
	Kind    int16          `json:"kind"`
	Content string         `json:"content"`
	FileID  string         `json:"file_id"`
	Meta    map[string]any `json:"meta"`
}

func (h *MessageHandler) Send(c *gin.Context) {
	var req sendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("message.invalid", "invalid request body"))
		return
	}
	in := service.SendMessageInput{
		ToUID:   req.ToUID,
		Kind:    domain.MessageKind(req.Kind),
		Content: req.Content,
		Meta:    req.Meta,
	}
	if req.FileID != "" {
		fid, err := idgen.Parse(req.FileID)
		if err != nil {
			httpx.WriteError(c, apperr.BadRequest("message.invalid", "invalid file_id"))
			return
		}
		in.FileID = &fid
	}
	m, err := h.messages.Send(c.Request.Context(), in)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, toMessageResponse(m))
}

func (h *MessageHandler) Get(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	m, err := h.messages.Get(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, toMessageResponse(m))
}

type editMessageRequest struct {
	Content string `json:"content"`
}

func (h *MessageHandler) Edit(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	var req editMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("message.invalid", "invalid request body"))
		return
	}
	m, err := h.messages.Edit(c.Request.Context(), id, req.Content)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, toMessageResponse(m))
}

func (h *MessageHandler) Delete(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.messages.Delete(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *MessageHandler) MarkConversationRead(c *gin.Context) {
	if err := h.messages.MarkConversationRead(c.Request.Context(), c.Param("uid")); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *MessageHandler) Inbox(c *gin.Context) {
	list, err := h.messages.Inbox(c.Request.Context(), httpx.PageFromQuery(c), c.Query("unread") == "true")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	renderMessageList(c, list)
}

func (h *MessageHandler) Conversation(c *gin.Context) {
	list, err := h.messages.Conversation(c.Request.Context(), c.Param("uid"), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	renderMessageList(c, list)
}

func (h *MessageHandler) MarkRead(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.messages.MarkRead(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *MessageHandler) UnreadCount(c *gin.Context) {
	n, err := h.messages.UnreadCount(c.Request.Context())
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"unread_count": n})
}

func (h *MessageHandler) ReactToMessage(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	actor, ok := service.ActorFrom(c.Request.Context())
	if !ok {
		httpx.WriteError(c, apperr.Unauthorized("auth.unauthorized", "missing authenticated actor"))
		return
	}
	if err := h.messages.ReactToMessage(c.Request.Context(), id, actor); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *MessageHandler) UnreactMessage(c *gin.Context) {
	id, err := parseID(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	actor, ok := service.ActorFrom(c.Request.Context())
	if !ok {
		httpx.WriteError(c, apperr.Unauthorized("auth.unauthorized", "missing authenticated actor"))
		return
	}
	if err := h.messages.UnreactMessage(c.Request.Context(), id, actor); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func renderMessageList(c *gin.Context, list *service.MessageList) {
	items := make([]messageResponse, 0, len(list.Messages))
	for _, m := range list.Messages {
		items = append(items, toMessageResponse(m))
	}
	httpx.OK(c, gin.H{
		"messages":    items,
		"users":       toUserMap(list.Users),
		"next_cursor": list.NextCursor,
	})
}

// ---- presentation DTOs (userResponse/toUserMap reused by the comment handler) ----

type messageResponse struct {
	ID        string         `json:"id"`
	FromUID   string         `json:"from_uid"`
	ToUID     string         `json:"to_uid"`
	Kind      int16          `json:"kind"`
	Content   string         `json:"content"`
	FileID    *string        `json:"file_id,omitempty"`
	ReadAt    *time.Time     `json:"read_at,omitempty"`
	EditedAt  *time.Time     `json:"edited_at,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func toMessageResponse(m *domain.Message) messageResponse {
	resp := messageResponse{
		ID:        m.ID.String(),
		FromUID:   m.FromUID,
		ToUID:     m.ToUID,
		Kind:      int16(m.Kind),
		Content:   m.Content,
		ReadAt:    m.ReadAt,
		EditedAt:  m.EditedAt,
		Meta:      m.Meta,
		CreatedAt: m.CreatedAt,
	}
	if m.FileID != nil {
		s := m.FileID.String()
		resp.FileID = &s
	}
	return resp
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
