package message

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/sso"
)

type HTTPHandler struct {
	svc Service
}

func NewHTTPHandler(svc Service) *HTTPHandler {
	return &HTTPHandler{svc: svc}
}

func (h *HTTPHandler) RegisterRoutes(r httpapi.Routes) {
	r.Auth.POST("/messages", h.Send)
	r.Auth.GET("/messages", h.Inbox)
	r.Auth.GET("/messages/unread_count", h.UnreadCount)
	r.Auth.GET("/messages/:id", h.Get)
	r.Auth.PATCH("/messages/:id", h.Edit)
	r.Auth.DELETE("/messages/:id", h.Delete)
	r.Auth.POST("/messages/:id/read", h.MarkRead)
	r.Auth.POST("/messages/:id/reactions", h.ReactToMessage)
	r.Auth.DELETE("/messages/:id/reactions", h.UnreactMessage)
	r.Auth.GET("/conversations", h.ListConversations)
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

func (h *HTTPHandler) Send(c *gin.Context) {
	var req sendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("message.invalid", "invalid request body"))
		return
	}
	in := SendMessageInput{
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
	m, err := h.svc.Send(c.Request.Context(), in)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.Created(c, toMessageResponse(m))
}

func (h *HTTPHandler) Get(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	m, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, toMessageResponse(m))
}

type editMessageRequest struct {
	Content string `json:"content"`
}

func (h *HTTPHandler) Edit(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	var req editMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteError(c, apperr.BadRequest("message.invalid", "invalid request body"))
		return
	}
	m, err := h.svc.Edit(c.Request.Context(), id, req.Content)
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, toMessageResponse(m))
}

func (h *HTTPHandler) Delete(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *HTTPHandler) MarkConversationRead(c *gin.Context) {
	if err := h.svc.MarkConversationRead(c.Request.Context(), c.Param("uid")); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *HTTPHandler) Inbox(c *gin.Context) {
	list, err := h.svc.Inbox(c.Request.Context(), httpx.PageFromQuery(c), c.Query("unread") == "true")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	renderMessageList(c, list)
}

func (h *HTTPHandler) ListConversations(c *gin.Context) {
	list, err := h.svc.ListConversations(c.Request.Context(), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	renderConversationList(c, list)
}

func (h *HTTPHandler) Conversation(c *gin.Context) {
	list, err := h.svc.Conversation(c.Request.Context(), c.Param("uid"), httpx.PageFromQuery(c))
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	renderMessageList(c, list)
}

func (h *HTTPHandler) MarkRead(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	if err := h.svc.MarkRead(c.Request.Context(), id); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *HTTPHandler) UnreadCount(c *gin.Context) {
	n, err := h.svc.UnreadCount(c.Request.Context())
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.OK(c, gin.H{"unread_count": n})
}

func (h *HTTPHandler) ReactToMessage(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	actor, ok := domain.ActorFrom(c.Request.Context())
	if !ok {
		httpx.WriteError(c, apperr.Unauthorized("auth.unauthorized", "missing authenticated actor"))
		return
	}
	if err := h.svc.ReactToMessage(c.Request.Context(), id, actor); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *HTTPHandler) UnreactMessage(c *gin.Context) {
	id, err := httpx.ParseIDFromParam(c, "id")
	if err != nil {
		httpx.WriteError(c, err)
		return
	}
	actor, ok := domain.ActorFrom(c.Request.Context())
	if !ok {
		httpx.WriteError(c, apperr.Unauthorized("auth.unauthorized", "missing authenticated actor"))
		return
	}
	if err := h.svc.UnreactMessage(c.Request.Context(), id, actor); err != nil {
		httpx.WriteError(c, err)
		return
	}
	httpx.NoContent(c)
}

func renderMessageList(c *gin.Context, list *MessageList) {
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

func renderConversationList(c *gin.Context, list *ConversationList) {
	items := make([]conversationResponse, 0, len(list.Conversations))
	for _, s := range list.Conversations {
		resp := conversationResponse{
			PeerUID:     s.PeerUID,
			LastMessage: toMessageResponse(s.LastMessage),
			UnreadCount: s.UnreadCount,
		}
		items = append(items, resp)
	}
	httpx.OK(c, gin.H{
		"conversations": items,
		"users":         toUserMap(list.Users),
		"next_cursor":   list.NextCursor,
	})
}

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

type conversationResponse struct {
	PeerUID     string          `json:"peer_uid"`
	LastMessage messageResponse `json:"last_message"`
	UnreadCount int64           `json:"unread_count"`
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
